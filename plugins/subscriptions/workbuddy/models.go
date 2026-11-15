// models.go owns the model catalog: the product's shipped table
// (models_static.go), the two live listings the official client merges on top
// of it — the account's own (/v3/config), narrowed to the conversation agent's
// declared list, which is what the official picker shows, and, for an
// enterprise account, the enterprise's
// (/console/enterprises/{id}/config/models) — and the conversation subset a
// surface offers.
//
// What each source answers is fixed: the live listings say which models exist,
// the shipped table says what each one can do (window, images, thinking
// levels). A live entry the table does not carry is kept as it stands, because
// it is a model the account can call, and its capabilities stay unknown rather
// than being answered "no".
package workbuddy

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

// modelListLimit caps a listing response: a model catalog is tens of
// kilobytes; anything larger is not a catalog.
const modelListLimit = 4 << 20

// workBuddyModel is one row of the product's model table, flattened: the shape
// of product.json's models[] and of both live listings, and what
// models_static.go is generated into.
type workBuddyModel struct {
	ID, Name                                    string
	MaxInputTokens, MaxOutputTokens             int64
	SupportsToolCall, SupportsImages, IsDefault bool
	// Efforts are reasoning.supportedEfforts, or the single reasoning.effort
	// when the listing names no set.
	Efforts []string
	// DefaultEffort is reasoning.defaultEffort, "" when the listing names none.
	DefaultEffort string
	// CanDisableThinking is reasoning.canDisableThinking as written: true and
	// false are answers, absent (nil) is not.
	CanDisableThinking *bool

	// imagesUndeclared marks a model no source characterized for image input:
	// its offer must then not answer for images at all, so the catalog answers
	// and the image gate fails open on a model nobody characterized
	// (subscription.Offer.ImagesUndeclared).
	imagesUndeclared bool
}

// wireModel is one entry of a live listing, as both endpoints spell it. The two
// booleans are pointers because a listing that stays silent about images or
// tool calls answers nothing, and that is not the same as answering "no".
type wireModel struct {
	ID               string         `json:"id"`
	Name             string         `json:"name"`
	MaxInputTokens   int64          `json:"maxInputTokens"`
	MaxOutputTokens  int64          `json:"maxOutputTokens"`
	SupportsToolCall *bool          `json:"supportsToolCall"`
	SupportsImages   *bool          `json:"supportsImages"`
	IsDefault        bool           `json:"isDefault"`
	Disabled         bool           `json:"disabled"`
	Reasoning        *wireReasoning `json:"reasoning"`
}

// wireAgent is one agent of a product config: the surfaces it serves (its
// tags) and the models it offers, in the order its picker lists them.
type wireAgent struct {
	Name   string   `json:"name"`
	Tags   []string `json:"tags"`
	Models []string `json:"models"`
}

// surfaceAgentTag marks the agent a conversation picker reads: the product
// tags it "model:craft", the intent every request this plugin sends declares.
const surfaceAgentTag = "model:craft"

// routerModelID is the product's routing alias. The official picker does not
// list it (owner 2026-11-15), so neither does a surface.
const routerModelID = "auto"

// wireReasoning is a listing's reasoning block.
type wireReasoning struct {
	Effort             string   `json:"effort"`
	SupportedEfforts   []string `json:"supportedEfforts"`
	DefaultEffort      string   `json:"defaultEffort"`
	CanDisableThinking *bool    `json:"canDisableThinking"`
}

// efforts is the levels the block names: the set when the listing carries one,
// otherwise the single effort it names.
func (r *wireReasoning) efforts() []string {
	if r == nil {
		return nil
	}
	if len(r.SupportedEfforts) > 0 {
		return append([]string(nil), r.SupportedEfforts...)
	}
	if effort := strings.TrimSpace(r.Effort); effort != "" {
		return []string{effort}
	}
	return nil
}

func (r *wireReasoning) defaultEffort() string {
	if r == nil {
		return ""
	}
	return r.DefaultEffort
}

func (r *wireReasoning) canDisableThinking() *bool {
	if r == nil {
		return nil
	}
	return r.CanDisableThinking
}

// nonChatModelID matches the ids of the non-conversation models the product
// table carries beside the chat ones: image and video generators, the code
// completion and rewrite helpers, and the small NES models.
var nonChatModelID = regexp.MustCompile(`image|video|kling|hunyuan-image|codewise|nes|jump|rewrite|completion-gf|completions`)

// chatCapable reports whether a shipped row is a conversation choice: it calls
// tools, states an input window, and is not one of the helper models above.
func chatCapable(m workBuddyModel) bool {
	return m.SupportsToolCall && m.MaxInputTokens > 0 && !nonChatModelID.MatchString(m.ID)
}

// chatModels narrows a table to its conversation choices.
func chatModels(rows []workBuddyModel) []workBuddyModel {
	out := make([]workBuddyModel, 0, len(rows))
	for _, m := range rows {
		if chatCapable(m) {
			out = append(out, m)
		}
	}
	return out
}

// effortLevels is a model's thinking levels in wire order, nil when no source
// named any: an empty list and no list mean the same thing to a surface.
func effortLevels(m workBuddyModel) []string {
	if len(m.Efforts) == 0 {
		return nil
	}
	return m.Efforts
}

// FetchModels resolves the account's catalog: the account's own live listing
// first (already narrowed to its conversation agent), the enterprise listing on
// top of it for an enterprise account, and the shipped table for what a live
// entry leaves out. fromStatic says no live listing answered — the shipped
// table is what the surface is showing, so the model page can point at the
// console as the authority.
func FetchModels(ctx context.Context, db *sql.DB, home string) (records []workBuddyModel, fromStatic bool) {
	shipped := staticWorkBuddyModels()
	byID := make(map[string]workBuddyModel, len(shipped))
	for _, m := range shipped {
		byID[m.ID] = m
	}

	creds, err := EnsureFreshCredentials(ctx, db, home)
	if err != nil {
		// Not signed in (or a dead login) is not an error here: the shipped
		// table is what an account with no live listing gets.
		return chatModels(shipped), true
	}
	live, err := fetchAccountModels(ctx, creds)
	if err != nil || len(live) == 0 {
		return chatModels(shipped), true
	}
	if strings.TrimSpace(creds.EnterpriseID) != "" {
		// The enterprise listing is an addition, never a precondition: a
		// personal account's endpoint answers 401/403, and failing there must
		// not cost the account the models its own listing named.
		if enterprise, entErr := fetchEnterpriseModels(ctx, creds); entErr == nil {
			live = mergeEnterpriseModels(live, enterprise)
		}
	}

	records = make([]workBuddyModel, 0, len(live))
	for _, entry := range live {
		shippedRow, known := byID[strings.TrimSpace(entry.ID)]
		if !keepLiveModel(entry, shippedRow, known) {
			continue
		}
		records = append(records, mergeLiveModel(entry, shippedRow, known))
	}
	if len(records) == 0 {
		return chatModels(shipped), true
	}
	return records, false
}

// keepLiveModel reports whether one live entry is a conversation choice. The
// shipped table characterizes the ids it carries, so their rows decide; an id
// the table does not carry is kept as the listing named it — the account can
// call it, and dropping it would only cost the user a choice — unless the
// listing itself says it cannot call tools.
func keepLiveModel(entry wireModel, shipped workBuddyModel, known bool) bool {
	if entry.SupportsToolCall != nil && !*entry.SupportsToolCall {
		return false
	}
	if !known {
		return strings.TrimSpace(entry.ID) != ""
	}
	return chatCapable(shipped)
}

// mergeLiveModel is one live entry with what the shipped table knows about it:
// what the listing stated stands, and the table fills in the rest.
func mergeLiveModel(entry wireModel, shipped workBuddyModel, known bool) workBuddyModel {
	model := workBuddyModel{
		ID:                 strings.TrimSpace(entry.ID),
		Name:               strings.TrimSpace(entry.Name),
		MaxInputTokens:     entry.MaxInputTokens,
		MaxOutputTokens:    entry.MaxOutputTokens,
		SupportsToolCall:   entry.SupportsToolCall != nil && *entry.SupportsToolCall,
		SupportsImages:     entry.SupportsImages != nil && *entry.SupportsImages,
		IsDefault:          entry.IsDefault,
		Efforts:            entry.Reasoning.efforts(),
		DefaultEffort:      strings.TrimSpace(entry.Reasoning.defaultEffort()),
		CanDisableThinking: entry.Reasoning.canDisableThinking(),
	}
	if !known {
		model.imagesUndeclared = entry.SupportsImages == nil
		return model
	}
	if model.Name == "" {
		model.Name = shipped.Name
	}
	if model.MaxInputTokens == 0 {
		model.MaxInputTokens = shipped.MaxInputTokens
	}
	if model.MaxOutputTokens == 0 {
		model.MaxOutputTokens = shipped.MaxOutputTokens
	}
	if entry.SupportsImages == nil {
		model.SupportsImages = shipped.SupportsImages
	}
	if len(model.Efforts) == 0 && model.DefaultEffort == "" {
		model.Efforts, model.DefaultEffort = shipped.Efforts, shipped.DefaultEffort
	}
	if model.CanDisableThinking == nil {
		model.CanDisableThinking = shipped.CanDisableThinking
	}
	return model
}

// mergeEnterpriseModels puts the enterprise listing on top of the account's own:
// an entry replaces the account's entry for the same id in place, and an id the
// account's listing did not name is appended after it.
func mergeEnterpriseModels(personal, enterprise []wireModel) []wireModel {
	if len(enterprise) == 0 {
		return personal
	}
	merged := make([]wireModel, len(personal))
	copy(merged, personal)
	index := make(map[string]int, len(merged))
	for i, entry := range merged {
		index[strings.TrimSpace(entry.ID)] = i
	}
	for _, entry := range enterprise {
		id := strings.TrimSpace(entry.ID)
		if id == "" {
			continue
		}
		if at, ok := index[id]; ok {
			merged[at] = entry
			continue
		}
		index[id] = len(merged)
		merged = append(merged, entry)
	}
	return merged
}

// narrowToSurface is the picker's list: the models the config's conversation
// agent declares, in the order it declares them. The catalog alone is every
// model the product answers for — routing aliases, preview aliases, the
// disabled lite entry, image and video generators — and offering it would not
// match the official dropdown. A config that names no conversation agent
// leaves the catalog as it stands: nothing is dropped on a guess.
func narrowToSurface(catalog []wireModel, agents []wireAgent) []wireModel {
	agent, ok := surfaceAgent(agents)
	if !ok {
		return catalog
	}
	byID := make(map[string]wireModel, len(catalog))
	for _, entry := range catalog {
		byID[strings.TrimSpace(entry.ID)] = entry
	}
	out := make([]wireModel, 0, len(agent.Models))
	for _, id := range agent.Models {
		id = strings.TrimSpace(id)
		if id == "" || id == routerModelID {
			continue
		}
		entry, listed := byID[id]
		if !listed || entry.Disabled {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// surfaceAgent is the config's conversation agent: the one tagged model:craft,
// or, when the product tags none that way, the agent named "cli".
func surfaceAgent(agents []wireAgent) (wireAgent, bool) {
	for _, agent := range agents {
		for _, tag := range agent.Tags {
			if strings.TrimSpace(tag) == surfaceAgentTag {
				return agent, true
			}
		}
	}
	for _, agent := range agents {
		if strings.TrimSpace(agent.Name) == "cli" {
			return agent, true
		}
	}
	return wireAgent{}, false
}

// fetchAccountModels asks for the account's own listing: the product config,
// whose models hang under data.models and whose conversation agent lists the
// picker's subset under data.agents.
func fetchAccountModels(ctx context.Context, creds Credentials) ([]wireModel, error) {
	body, err := listingJSON(ctx, creds, "/v3/config")
	if err != nil {
		return nil, err
	}
	var answer struct {
		Data struct {
			Models json.RawMessage `json:"models"`
			Agents []wireAgent     `json:"agents"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		return nil, fmt.Errorf("workbuddy: product config answer: %w", err)
	}
	catalog, err := decodeModels(answer.Data.Models)
	if err != nil {
		return nil, err
	}
	return narrowToSurface(catalog, answer.Data.Agents), nil
}

// fetchEnterpriseModels asks for the enterprise's listing, whose array hangs
// straight on the body's data (the account listing nests it one level deeper).
func fetchEnterpriseModels(ctx context.Context, creds Credentials) ([]wireModel, error) {
	enterprise := strings.TrimSpace(creds.EnterpriseID)
	if enterprise == "" {
		return nil, errors.New("workbuddy: this login names no enterprise")
	}
	body, err := listingJSON(ctx, creds, "/console/enterprises/"+url.PathEscape(enterprise)+"/config/models")
	if err != nil {
		return nil, err
	}
	var answer struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		return nil, fmt.Errorf("workbuddy: enterprise model listing answer: %w", err)
	}
	return decodeModels(answer.Data)
}

// decodeModels reads a model array in the spellings the product's listings use:
// the array itself, and one level of object around it ({"model": [...]} or
// {"models": [...]}, or a single model object).
func decodeModels(raw json.RawMessage) ([]wireModel, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil, nil
	}
	var models []wireModel
	if err := json.Unmarshal(trimmed, &models); err == nil {
		return models, nil
	}
	var wrapper struct {
		Model  json.RawMessage `json:"model"`
		Models json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal(trimmed, &wrapper); err != nil {
		return nil, fmt.Errorf("workbuddy: model listing: %w", err)
	}
	inner := wrapper.Model
	if len(inner) == 0 {
		inner = wrapper.Models
	}
	if len(inner) == 0 {
		return nil, errors.New("workbuddy: the model listing carried no models")
	}
	if err := json.Unmarshal(inner, &models); err == nil {
		return models, nil
	}
	var single wireModel
	if err := json.Unmarshal(inner, &single); err == nil && strings.TrimSpace(single.ID) != "" {
		return []wireModel{single}, nil
	}
	return nil, errors.New("workbuddy: the model listing carried no models")
}

// listingJSON asks one listing endpoint with the login's credential and
// identity headers. A non-200 is an error carrying the status, so a caller can
// tell a refusal from a shape it cannot read.
func listingJSON(ctx context.Context, creds Credentials, path string) ([]byte, error) {
	target, err := requestURL(workBuddyBaseURL, path)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header = disguiseHeaders(creds, "")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, modelListLimit))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &llm.APIError{
			StatusCode: resp.StatusCode,
			Message:    fmt.Sprintf("workbuddy: %s answered %s", path, strings.TrimSpace(string(body))),
		}
	}
	return body, nil
}

// toLLMModel converts one row into the host's capability row. The window is the
// input window the product states; InputTokenLimit stays zero on purpose,
// because the host reads that field as the compaction ceiling (this is not it).
func toLLMModel(record workBuddyModel) llm.Model {
	model := llm.Model{
		ID:                  record.ID,
		Name:                record.Name,
		ContextWindow:       record.MaxInputTokens,
		DefaultMaxTokens:    record.MaxOutputTokens,
		SupportsAttachments: record.SupportsImages,
	}
	if levels := thinkingLevels(record); len(levels) > 0 {
		model.CanReason = true
		model.ReasoningKnown = true
		model.ReasoningOptions = []llm.ReasoningOption{{Type: "effort", Values: levels}}
	}
	return model
}

// thinkingLevels is a model's levels as the host's builtin table spells them:
// the levels in wire order, with "none" first when the model can turn thinking
// off (the host keeps that switch in its own column and renders it as the head
// of the list).
func thinkingLevels(record workBuddyModel) []string {
	levels := effortLevels(record)
	if record.CanDisableThinking != nil && *record.CanDisableThinking {
		return append([]string{"none"}, levels...)
	}
	return levels
}

// installBuiltinModels publishes records as the workbuddy capability rows: the
// windows and thinking levels a surface can offer before the first live listing
// replaces them.
func installBuiltinModels(records []workBuddyModel) {
	models := make([]llm.Model, 0, len(records))
	for _, record := range records {
		models = append(models, toLLMModel(record))
	}
	llm.SetBuiltinModels("workbuddy", models)
}
