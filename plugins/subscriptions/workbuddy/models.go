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
	"sync"

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

// ratePair is one pair of ids the product sells at two rates: the free tier its
// picker lists, and the paid twin a request falls back to once that tier's
// allowance is spent. It is the product's own declaration
// (data.productFeaturesConfig.ModelRateLimitCap.lines), which the official
// client calls ModelRateLimitCap; the shipped copy is shippedRatePairs.
type ratePair struct {
	// DisplayName is the product's name for the pair ("Hy4"), the name the paid
	// twin carries.
	DisplayName string
	FreeID      string
	PaidID      string
	// AllowPaidSwitch is the product's own flag for the pair: requests may fall
	// back to PaidID, and a surface offers the switch, only when it is set.
	AllowPaidSwitch bool
}

// wireRatePair is one line of the product's rate-limit-cap declaration.
type wireRatePair struct {
	AllowPaidSwitch bool   `json:"allowPaidSwitch"`
	DisplayName     string `json:"displayName"`
	FreeID          string `json:"freeId"`
	PaidID          string `json:"paidId"`
}

// wireRateLimitCap is that declaration: the lines it holds, one per pair.
type wireRateLimitCap struct {
	Lines []wireRatePair `json:"lines"`
}

// ratePairs is the declaration as the plugin reads it: blank lines dropped,
// every id trimmed.
func (c wireRateLimitCap) ratePairs() []ratePair {
	out := make([]ratePair, 0, len(c.Lines))
	for _, line := range c.Lines {
		free, paid := strings.TrimSpace(line.FreeID), strings.TrimSpace(line.PaidID)
		if free == "" || paid == "" {
			continue
		}
		out = append(out, ratePair{
			DisplayName:     strings.TrimSpace(line.DisplayName),
			FreeID:          free,
			PaidID:          paid,
			AllowPaidSwitch: line.AllowPaidSwitch,
		})
	}
	return out
}

// freeTierName is the display name a free tier's row carries: the product's own
// name with " Free" behind it, so the free and the paid row of one pair read
// differently on a surface with no credits column (the official picker tells
// them apart by the credits beside the name; ours has no such column). It is
// idempotent: a name that already says so is left alone.
func freeTierName(displayName string) string {
	name := strings.TrimSpace(displayName)
	if name == "" || strings.HasSuffix(name, " Free") {
		return name
	}
	return name + " Free"
}

// catalog is one resolution of the account's model listing: the rows, the pairs
// the product declared for them, and the login it was read for.
type catalog struct {
	records []workBuddyModel
	pairs   []ratePair
	// account is the login the listing was read with. signedIn is false when
	// there was none (or it was dead), in which case the shipped table is what
	// answered.
	account  Credentials
	signedIn bool
	// fromStatic says no live listing answered.
	fromStatic bool
}

// FetchModels resolves the account's catalog: the account's own live listing
// first (already narrowed to its conversation agent), the enterprise listing on
// top of it for an enterprise account, and the shipped table for what a live
// entry leaves out. It also answers the product's free/paid pairs, which the
// offers collapse and the transport switches on. fromStatic says no live
// listing answered — the shipped table is what the surface is showing, so the
// model page can point at the console as the authority.
func FetchModels(ctx context.Context, db *sql.DB, home string) (records []workBuddyModel, pairs []ratePair, fromStatic bool) {
	resolved := fetchCatalog(ctx, db, home)
	return resolved.records, resolved.pairs, resolved.fromStatic
}

// fetchCatalog is FetchModels with the login it resolved, which the offers need
// to decide whether the account is inside the product's paid line.
//
// The published ids and pairs are remembered for the transport, which resolves
// every request against them: a send carries no listing of its own.
func fetchCatalog(ctx context.Context, db *sql.DB, home string) catalog {
	shipped := staticWorkBuddyModels()
	byID := make(map[string]workBuddyModel, len(shipped))
	for _, m := range shipped {
		byID[m.ID] = m
	}

	creds, err := EnsureFreshCredentials(ctx, db, home)
	if err != nil {
		// Not signed in (or a dead login) is not an error here: the shipped
		// table is what an account with no live listing gets.
		out := catalog{records: chatModels(shipped), pairs: shippedRatePairs(), fromStatic: true}
		rememberCatalog(modelIDs(out.records), out.pairs)
		return out
	}
	creds = ensureAccountType(ctx, db, home, creds)
	live, declared, err := fetchAccountModels(ctx, creds)
	if err != nil || len(live) == 0 {
		out := catalog{records: chatModels(shipped), pairs: shippedRatePairs(), account: creds, signedIn: true, fromStatic: true}
		rememberCatalog(modelIDs(out.records), out.pairs)
		return out
	}
	if strings.TrimSpace(creds.EnterpriseID) != "" {
		// The enterprise listing is an addition, never a precondition: a
		// personal account's endpoint answers 401/403, and failing there must
		// not cost the account the models its own listing named.
		if enterprise, entErr := fetchEnterpriseModels(ctx, creds); entErr == nil {
			live = mergeEnterpriseModels(live, enterprise)
		}
	}

	records := make([]workBuddyModel, 0, len(live))
	for _, entry := range live {
		shippedRow, known := byID[strings.TrimSpace(entry.ID)]
		if !keepLiveModel(entry, shippedRow, known) {
			continue
		}
		records = append(records, mergeLiveModel(entry, shippedRow, known))
	}
	if len(records) == 0 {
		out := catalog{records: chatModels(shipped), pairs: shippedRatePairs(), account: creds, signedIn: true, fromStatic: true}
		rememberCatalog(modelIDs(out.records), out.pairs)
		return out
	}
	// A listing that names no declaration gets the shipped one (the client's own
	// fallback), but it does NOT rename the rows it named: only the product's
	// live declaration says which two ids are one pair, and a name is not
	// guessed from an id.
	if len(declared) == 0 {
		out := catalog{records: records, pairs: shippedRatePairs(), account: creds, signedIn: true}
		rememberCatalog(modelIDs(out.records), out.pairs)
		return out
	}
	for i, pair := range declared {
		declared[i].DisplayName = strings.TrimSpace(pair.DisplayName)
	}
	records = nameFreeTiers(records, declared)
	out := catalog{records: records, pairs: declared, account: creds, signedIn: true}
	rememberCatalog(modelIDs(out.records), out.pairs)
	return out
}

// nameFreeTiers spells the free row of each declared pair as the product's name
// with " Free" behind it. Every other row keeps the name its source gave it.
func nameFreeTiers(records []workBuddyModel, pairs []ratePair) []workBuddyModel {
	free := make(map[string]string, len(pairs))
	for _, pair := range pairs {
		if name := freeTierName(pair.DisplayName); name != "" {
			free[pair.FreeID] = name
		}
	}
	if len(free) == 0 {
		return records
	}
	for i, record := range records {
		if name, ok := free[record.ID]; ok {
			records[i].Name = name
		}
	}
	return records
}

// modelIDs is the id set of a resolved catalog.
func modelIDs(records []workBuddyModel) []string {
	ids := make([]string, 0, len(records))
	for _, record := range records {
		ids = append(ids, record.ID)
	}
	return ids
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
// whose models hang under data.models, whose conversation agent lists the
// picker's subset under data.agents, and whose free/paid pairs are declared
// under data.productFeaturesConfig.ModelRateLimitCap.
func fetchAccountModels(ctx context.Context, creds Credentials) ([]wireModel, []ratePair, error) {
	body, err := listingJSON(ctx, creds, "/v3/config")
	if err != nil {
		return nil, nil, err
	}
	var answer struct {
		Data struct {
			Models   json.RawMessage `json:"models"`
			Agents   []wireAgent     `json:"agents"`
			Features *struct {
				ModelRateLimitCap wireRateLimitCap `json:"ModelRateLimitCap"`
			} `json:"productFeaturesConfig"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		return nil, nil, fmt.Errorf("workbuddy: product config answer: %w", err)
	}
	catalog, err := decodeModels(answer.Data.Models)
	if err != nil {
		return nil, nil, err
	}
	var declared []ratePair
	if answer.Data.Features != nil {
		declared = answer.Data.Features.ModelRateLimitCap.ratePairs()
	}
	return narrowToSurface(catalog, answer.Data.Agents), declared, nil
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

// shippedIDs is the id set of the shipped table.
func shippedIDs() map[string]bool {
	rows := staticWorkBuddyModels()
	ids := make(map[string]bool, len(rows))
	for _, row := range rows {
		ids[row.ID] = true
	}
	return ids
}

// knownCatalog is what a send is resolved against: the ids the last listing
// carried and the pairs the product declared for them. The client asks the same
// two questions of its own catalog (catalogHasPaid, modelLines); here they are
// remembered once per listing rather than read on every request, because a send
// carries no listing of its own. Until the first listing answers, the shipped
// declaration stands — the same fallback the client ships.
var knownCatalog struct {
	mu    sync.RWMutex
	ids   map[string]bool
	pairs []ratePair
	ready bool
}

// rememberCatalog records what a resolved catalog published. No ids and no
// pairs restore the shipped declaration.
func rememberCatalog(ids []string, pairs []ratePair) {
	published := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			published[id] = true
		}
	}
	if len(published) == 0 && len(pairs) == 0 {
		published, pairs = shippedIDs(), shippedRatePairs()
	}
	knownCatalog.mu.Lock()
	knownCatalog.ids, knownCatalog.pairs, knownCatalog.ready = published, pairs, true
	knownCatalog.mu.Unlock()
}

// catalogSnapshot answers the published ids and pairs, filling in the shipped
// declaration the first time it is asked.
func catalogSnapshot() (map[string]bool, []ratePair) {
	knownCatalog.mu.RLock()
	ids, pairs, ready := knownCatalog.ids, knownCatalog.pairs, knownCatalog.ready
	knownCatalog.mu.RUnlock()
	if ready {
		return ids, pairs
	}
	knownCatalog.mu.Lock()
	if !knownCatalog.ready {
		knownCatalog.ids, knownCatalog.pairs, knownCatalog.ready = shippedIDs(), shippedRatePairs(), true
	}
	ids, pairs = knownCatalog.ids, knownCatalog.pairs
	knownCatalog.mu.Unlock()
	return ids, pairs
}

// catalogHasModel reports whether the published catalog carries id.
func catalogHasModel(id string) bool {
	ids, _ := catalogSnapshot()
	return ids[strings.TrimSpace(id)]
}

// pairForFree answers the declared pair a free id belongs to.
func pairForFree(id string) (ratePair, bool) {
	_, pairs := catalogSnapshot()
	id = strings.TrimSpace(id)
	for _, pair := range pairs {
		if pair.FreeID == id {
			return pair, true
		}
	}
	return ratePair{}, false
}

// pairForPaid answers the declared pair a paid id belongs to.
func pairForPaid(id string) (ratePair, bool) {
	_, pairs := catalogSnapshot()
	id = strings.TrimSpace(id)
	for _, pair := range pairs {
		if pair.PaidID == id {
			return pair, true
		}
	}
	return ratePair{}, false
}

// paidLineEligible reports whether the account is inside the product's paid
// line, which is what collapses a pair into one row. It is the client's
// isEligiblePaidLineUser minus its denyList — that list is a constant inside the
// client with no public source, so only the account type decides here (§3.1).
// An account whose type is unknown reads as outside: both rows show, named
// apart, which is the safe half (nothing is hidden from a reader we cannot
// place).
func paidLineEligible(creds Credentials) bool {
	switch strings.ToLower(strings.TrimSpace(creds.AccountType)) {
	case "personal", "pro":
		return true
	}
	return false
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
