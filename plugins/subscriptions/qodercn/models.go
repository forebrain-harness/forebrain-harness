// models.go answers "which models does this Qoder CN account unlock": the
// remote /algo/api/v2/model/list?Encode=1 listing (decoded from the custom
// alphabet), the official client's local catalog-v7 cache when the network is
// unreachable, and a static fallback of the thirteen production models so an
// offer list exists even before the first successful listing.
package qodercn

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

// modelListLimit caps the listing response: a model catalog is tens of
// kilobytes; anything larger is not a catalog.
const modelListLimit = 4 << 20

// qoderModelRecord is one entry of the scene listing the gateway (and the
// catalog cache) return.
type qoderModelRecord struct {
	Key             string   `json:"key"`
	DisplayName     string   `json:"display_name"`
	MaxInputTokens  int64    `json:"max_input_tokens"`
	MaxOutputTokens int64    `json:"max_output_tokens"`
	IsVL            bool     `json:"is_vl"`
	IsReasoning     bool     `json:"is_reasoning"`
	Efforts         []string `json:"efforts"`
	DefaultEffort   string   `json:"default_effort"`
	IsDefault       bool     `json:"is_default"`
	// Contexts are the context tiers the listing declares
	// ({"1M":{"token_count":1000000},…}); contextWindow serves the top one.
	// Efforts/DefaultEffort above are the older flat shape of the same idea as
	// Thinking.
	Contexts map[string]qoderContextTier `json:"context_config"`
	Thinking qoderThinkingConfig         `json:"thinking_config"`
}

// qoderContextTier is one context window a listing declares for a model.
type qoderContextTier struct {
	TokenCount int64 `json:"token_count"`
	IsDefault  bool  `json:"is_default"`
}

// qoderThinkingConfig is the thinking block a listing declares: the switch that
// turns thinking off entirely, and the levels the model offers with the one it
// defaults to.
type qoderThinkingConfig struct {
	Disabled json.RawMessage `json:"disabled"`
	Enabled  struct {
		Efforts   map[string]qoderThinkingEffort `json:"efforts"`
		IsDefault bool                           `json:"is_default"`
	} `json:"enabled"`
}

// qoderThinkingEffort is one level a model offers; the listing marks the one it
// defaults to and says nothing else about it.
type qoderThinkingEffort struct {
	IsDefault bool `json:"is_default"`
}

// thinking is the thinking_config a listing declares: whether thinking can be
// turned off, the level it defaults to, and the levels it offers.
func thinking(off bool, def string, levels ...string) qoderThinkingConfig {
	config := qoderThinkingConfig{}
	if off {
		config.Disabled = json.RawMessage("{}")
	}
	config.Enabled.IsDefault = len(levels) > 0
	config.Enabled.Efforts = make(map[string]qoderThinkingEffort, len(levels))
	for _, level := range levels {
		config.Enabled.Efforts[level] = qoderThinkingEffort{IsDefault: level == def}
	}
	return config
}

// reasoningEffortOrder is the one order the wire accepts, and the same closed
// set the host can send (pkg/llm's reasoningEffortLevels): a level outside it
// would reach a surface that cannot ask for it, so it is dropped here.
var reasoningEffortOrder = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

// modelListResponse is the decoded /model/list body in its two older shapes: an
// envelope around the scenes, or a bare models array (the cache shape). The
// account's own listing carries the scenes at the top level of the body, which
// decodeModelList reads before falling back to these.
type modelListResponse struct {
	Data struct {
		Scenes map[string][]qoderModelRecord `json:"scenes"`
	} `json:"data"`
	Models []qoderModelRecord `json:"models"`
}

// scenePreference is the scene order the official client reads: app first,
// then the older chat/assistant spellings.
var scenePreference = []string{"app", "chat", "assistant"}

// productionContextTiers are the tiers every production model declares (200K by
// default, 400K, 1M); contextWindow serves the top one, which is where the
// vendor client runs the main model.
var productionContextTiers = map[string]qoderContextTier{
	"200K": {TokenCount: 200000, IsDefault: true},
	"400K": {TokenCount: 400000},
	"1M":   {TokenCount: 1000000},
}

// staticQoderModels is the production listing (captured 2026-11-13 from the app
// scene): the fallback that keeps offers and their capabilities — windows,
// thinking levels — available when the account cannot be listed live.
func staticQoderModels() []qoderModelRecord {
	return []qoderModelRecord{
		{Key: "auto", DisplayName: "Auto", MaxInputTokens: 200000, IsVL: true, IsReasoning: true, IsDefault: true, Contexts: productionContextTiers},
		{Key: "fast", DisplayName: "Fast", MaxInputTokens: 200000, IsVL: true, IsReasoning: true, Contexts: productionContextTiers},
		{Key: "qmodel_latest", DisplayName: "Qwen4-Max-Preview", MaxInputTokens: 1000000, IsVL: true, IsReasoning: true, Contexts: productionContextTiers,
			Thinking: thinking(true, "xhigh", "xhigh", "high", "low")},
		{Key: "qmodel_38max", DisplayName: "Qwen3.8-Max", MaxInputTokens: 180000, IsVL: true, IsReasoning: true, Contexts: productionContextTiers,
			Thinking: thinking(true, "medium", "xhigh", "low", "medium")},
		{Key: "qfmodel", DisplayName: "Qwen3.8-Flash", MaxInputTokens: 180000, IsVL: true, IsReasoning: true, Contexts: productionContextTiers,
			Thinking: thinking(true, "medium", "xhigh", "low", "medium")},
		{Key: "dmodel", DisplayName: "DeepSeek-V4-Pro", MaxInputTokens: 96000, IsVL: true, IsReasoning: true, Contexts: productionContextTiers,
			Thinking: thinking(true, "max", "high", "max")},
		{Key: "dfmodel", DisplayName: "DeepSeek-Flash", MaxInputTokens: 180000, IsVL: true, Contexts: productionContextTiers,
			Thinking: thinking(true, "high", "high", "max", "low")},
		{Key: "gmodel", DisplayName: "GLM-5.3", MaxInputTokens: 180000, IsVL: true, IsReasoning: true, Contexts: productionContextTiers,
			Thinking: thinking(false, "high", "high", "low", "max")},
		{Key: "gfmodel", DisplayName: "GLM-5.3-Flash", MaxInputTokens: 1000000, IsVL: true, IsReasoning: true, Contexts: productionContextTiers,
			Thinking: thinking(false, "max", "high", "max")},
		{Key: "kmodel_latest", DisplayName: "Kimi-K3.1-Pro", MaxInputTokens: 180000, IsVL: true, Contexts: productionContextTiers,
			Thinking: thinking(false, "high", "high", "low", "max")},
		{Key: "kmodel", DisplayName: "Kimi-K2.8-Preview", MaxInputTokens: 180000, IsVL: true, IsReasoning: true, Contexts: productionContextTiers,
			Thinking: thinking(false, "high", "high", "low", "max")},
		{Key: "jmodel", DisplayName: "Step-5-Preview", MaxInputTokens: 1000000, IsVL: true, IsReasoning: true, Contexts: productionContextTiers,
			Thinking: thinking(true, "high", "high", "low", "medium")},
		{Key: "mmodel", DisplayName: "MiniMax-M3.1-Flash", MaxInputTokens: 180000, IsVL: true, IsReasoning: true, Contexts: productionContextTiers,
			Thinking: thinking(false, "xhigh", "xhigh", "high", "low", "max", "medium")},
	}
}

// defaultCheckedModels names the offers pre-ticked in a first provision.
var defaultCheckedModels = map[string]bool{"qmodel_latest": true, "qmodel_38max": true, "auto": true}

// defaultMaxOutputTokens is the output budget a request asks for when the
// listing names none: the account's listing carries no max_output_tokens at
// all, and 32k is what this table has always asked for.
const defaultMaxOutputTokens = 32000

// contextWindow is the window the model actually serves: the largest context
// tier the listing declares, and — when it declares none — the model's own
// max_input_tokens. The vendor client runs the main model at that top tier
// (its selectMain takes max(contextWindows)), so this is the number both the
// host's compaction budget and the request's context_length read.
func (r qoderModelRecord) contextWindow() int64 {
	served := int64(0)
	for _, tier := range r.Contexts {
		if tier.TokenCount > served {
			served = tier.TokenCount
		}
	}
	if served > 0 {
		return served
	}
	return r.MaxInputTokens
}

// thinkingLevels is what the model's own thinking_config says: the levels it
// offers in the one order the wire accepts, the level it marks as its default,
// and whether the listing carries a mode that turns thinking off. "none" is not
// a level here — it is that off switch. The flat efforts field answers only when
// the listing declares no thinking_config at all.
func (r qoderModelRecord) thinkingLevels() (levels []string, def string, off bool) {
	offered := make(map[string]bool, len(r.Thinking.Enabled.Efforts))
	for level, effort := range r.Thinking.Enabled.Efforts {
		name := strings.ToLower(strings.TrimSpace(level))
		offered[name] = true
		if effort.IsDefault {
			def = name
		}
	}
	if len(offered) == 0 {
		for _, level := range r.Efforts {
			offered[strings.ToLower(strings.TrimSpace(level))] = true
		}
	}
	off = len(r.Thinking.Disabled) > 0 && r.thinkingCanBeDisabled()
	levels = make([]string, 0, len(offered))
	namedDefault := ""
	for _, level := range reasoningEffortOrder {
		if level == "none" || !offered[level] {
			continue
		}
		levels = append(levels, level)
		if level == def {
			namedDefault = def
		}
	}
	// A default the host cannot send is not a default: the level set is the one
	// closed order above, and a surface can only pre-pick what it can offer.
	if len(levels) == 0 {
		levels = nil
	}
	return levels, namedDefault, off
}

// effortLevels is thinkingLevels as the host's builtin table spells it: the
// levels, with "none" first when the listing carries the off switch.
func (r qoderModelRecord) effortLevels() []string {
	levels, _, off := r.thinkingLevels()
	if !off {
		return levels
	}
	return append([]string{"none"}, levels...)
}

// thinkingCanBeDisabled reports whether the listing carries a mode that turns
// thinking off: an object says yes, and the JSON null an absent block decodes to
// says no.
func (r qoderModelRecord) thinkingCanBeDisabled() bool {
	disabled := strings.TrimSpace(string(r.Thinking.Disabled))
	return disabled != "" && disabled != "null"
}

// toLLMModel converts one listing record into the host's capability row: the
// window is what the model serves (contextWindow), never max_input_tokens —
// InputTokenLimit stays zero on purpose, because the host reads it as the
// compaction ceiling and max_input_tokens is not that.
func toLLMModel(record qoderModelRecord) llm.Model {
	maxOutput := record.MaxOutputTokens
	if maxOutput <= 0 {
		maxOutput = defaultMaxOutputTokens
	}
	model := llm.Model{
		ID:                  record.Key,
		Name:                record.DisplayName,
		ContextWindow:       record.contextWindow(),
		DefaultMaxTokens:    maxOutput,
		SupportsAttachments: record.IsVL,
		CanReason:           record.IsReasoning,
	}
	if levels := record.effortLevels(); len(levels) > 0 {
		model.ReasoningKnown = true
		model.ReasoningOptions = []llm.ReasoningOption{{Type: "effort", Values: levels}}
	}
	return model
}

// fetchModelList asks the gateway for the account's listing, wearing the full
// client disguise; a 401/403 refreshes the token once and retries.
func fetchModelList(ctx context.Context, db *sql.DB, home string) ([]qoderModelRecord, error) {
	creds, err := EnsureFreshCredentials(ctx, db, home)
	if err != nil {
		return nil, err
	}
	identity := LoadOrCreateMachineIdentity(home)
	records, status, err := requestModelList(ctx, creds, identity)
	if (status == http.StatusUnauthorized || status == http.StatusForbidden) && err != nil {
		// The token went stale between the freshness check and the call:
		// refresh once, then ask again.
		if creds, refreshErr := ForceRefreshCredentials(ctx, db, home); refreshErr == nil {
			records, status, err = requestModelList(ctx, creds, identity)
		}
	}
	if err != nil {
		return nil, err
	}
	return records, nil
}

func requestModelList(ctx context.Context, creds Credentials, identity MachineIdentity) ([]qoderModelRecord, int, error) {
	url, headers, _, err := PrepareRequestHeaders(creds, identity, PrepareSpec{
		Path:     "/api/v2/model/list?Encode=1",
		SignPath: "/api/v2/model/list",
	})
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header = headers
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		return nil, resp.StatusCode, &llm.APIError{
			StatusCode: resp.StatusCode,
			Message:    fmt.Sprintf("qodercn: model list answered %s", strings.TrimSpace(string(body))),
		}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, modelListLimit))
	resp.Body.Close()
	if err != nil {
		return nil, resp.StatusCode, err
	}
	records, err := decodeModelList(body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if len(records) == 0 {
		return nil, resp.StatusCode, errors.New("qodercn: the model listing carried no models")
	}
	return records, resp.StatusCode, nil
}

// decodeModelList unwraps an Encode=1 (or plain JSON) listing body and picks
// the scene the client reads.
//
// The account's own listing carries its scenes at the TOP level of the body
// ({"chat":[…],"app":[…]}, 2026-11-13); the envelope and the bare models array
// below are what older gateway builds and the client's catalog cache answer, so
// all three decode.
func decodeModelList(body []byte) ([]qoderModelRecord, error) {
	plain, err := DecodePayload(string(body))
	if err != nil {
		return nil, fmt.Errorf("qodercn: model list payload: %w", err)
	}
	var scenes map[string]json.RawMessage
	if err := json.Unmarshal(plain, &scenes); err == nil {
		for _, scene := range scenePreference {
			raw, ok := scenes[scene]
			if !ok || len(raw) == 0 {
				continue
			}
			var records []qoderModelRecord
			if json.Unmarshal(raw, &records) == nil && len(records) > 0 {
				return records, nil
			}
		}
	}
	var listing modelListResponse
	if err := json.Unmarshal(plain, &listing); err != nil {
		return nil, fmt.Errorf("qodercn: model list json: %w", err)
	}
	for _, scene := range scenePreference {
		if records := listing.Data.Scenes[scene]; len(records) > 0 {
			return records, nil
		}
	}
	if len(listing.Models) > 0 {
		return listing.Models, nil
	}
	return nil, nil
}

// readCatalogCache decrypts the official client's catalog-v7 for the account,
// when this machine has one. Read-only.
func readCatalogCache(uid string) ([]qoderModelRecord, error) {
	root := QoderHomeDir()
	if root == "" || strings.TrimSpace(uid) == "" {
		return nil, errors.New("no catalog cache to read")
	}
	raw, err := os.ReadFile(filepath.Join(root, ".models", uid, "catalog-v7"))
	if err != nil {
		return nil, err
	}
	plain, err := DecryptModelCache(strings.TrimSpace(string(raw)), uid)
	if err != nil {
		return nil, err
	}
	records, err := decodeModelList(plain)
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, errors.New("the catalog cache carried no models")
	}
	return records, nil
}

// FetchModels resolves the account's listing and installs it as the qodercn
// capability table: remote first, the local catalog cache when the network is
// unreachable, the static production table last.
func FetchModels(ctx context.Context, db *sql.DB, home string) ([]qoderModelRecord, error) {
	records, err := fetchModelList(ctx, db, home)
	if err == nil {
		return records, nil
	}
	if creds, ok, loadErr := loadCredentials(ctx, db, home); loadErr == nil && ok {
		if cached, cacheErr := readCatalogCache(creds.UID); cacheErr == nil {
			return cached, nil
		}
	}
	return staticQoderModels(), nil
}

// installBuiltinModels publishes the records as the qodercn capability rows, and
// the input budget each one declares beside them (the request needs the budget,
// the host must not read it as a window — see toLLMModel).
func installBuiltinModels(records []qoderModelRecord) {
	models := make([]llm.Model, 0, len(records))
	limits := make(map[string]int64, len(records))
	for _, record := range records {
		models = append(models, toLLMModel(record))
		if record.MaxInputTokens > 0 {
			limits[strings.ToLower(strings.TrimSpace(record.Key))] = record.MaxInputTokens
		}
	}
	llm.SetBuiltinModels("qodercn", models)
	setModelInputLimits(limits)
}

// modelInputLimits is the input budget each model declares (the listing's
// max_input_tokens), kept beside the capability rows because it cannot ride in
// them: llm.Model.InputTokenLimit means the compaction ceiling to the host
// (state.EffectiveInputWindow prefers it over the context window), and this
// field is not that. installBuiltinModels fills both from one listing, so a
// request never pays for a second read of the network or the cache.
var (
	modelInputLimitsMu sync.RWMutex
	modelInputLimits   = map[string]int64{}
)

// setModelInputLimits replaces the input budgets; nil or empty clears them.
func setModelInputLimits(limits map[string]int64) {
	modelInputLimitsMu.Lock()
	defer modelInputLimitsMu.Unlock()
	if len(limits) == 0 {
		modelInputLimits = map[string]int64{}
		return
	}
	modelInputLimits = limits
}

// inputLimitOf is the input budget one model declared, 0 when no listing has
// named it.
func inputLimitOf(model string) int64 {
	modelInputLimitsMu.RLock()
	defer modelInputLimitsMu.RUnlock()
	return modelInputLimits[strings.ToLower(strings.TrimSpace(model))]
}
