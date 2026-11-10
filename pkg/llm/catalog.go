// Model catalog, display labels, and local install paths.
package llm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Limits holds the token limits for a single model. A zero field means the
// underlying database did not specify that limit.
type Limits struct {
	// Context is the total context-window size in tokens.
	Context int
	// Input is the maximum input (prompt) tokens. Often absent (0).
	Input int
	// Output is the maximum output (completion) tokens.
	Output int
}

// ReasoningOption is one reasoning_options description a catalog entry carries.
// Type names the shape of the choice — "effort" (discrete levels in Values),
// "toggle" (reasons or does not), "budget_tokens" (a token range in Min/Max) —
// and a model can carry several at once.
type ReasoningOption struct {
	Type   string   `json:"type"`
	Values []string `json:"values,omitempty"`
	Min    int      `json:"min,omitempty"`
	Max    int      `json:"max,omitempty"`
}

// Hit holds model capability data from the catalog.
type Hit struct {
	CatalogID           string `json:"catalog_id,omitempty"`
	CompHash            string `json:"comp_hash,omitempty"`
	ContextWindow       int64  `json:"context_window,omitempty"`
	InputTokenLimit     int64  `json:"input_token_limit,omitempty"`
	DefaultMaxTokens    int64  `json:"default_max_tokens,omitempty"`
	CanReason           bool   `json:"can_reason,omitempty"`
	SupportsAttachments bool   `json:"supports_attachments,omitempty"`
	// Kind is the catalog's model type when it is not a chat model ("image",
	// "speech", "embedding", "decision", …), "" for chat models.
	Kind string `json:"kind,omitempty"`
	// Status is the catalog's lifecycle marker — "deprecated" for a model the
	// upstream has retired, "" when the entry names none.
	Status string `json:"status,omitempty"`
	// ReasoningOptions and ReasoningKnown carry the entry's reasoning_options
	// as written. ReasoningKnown is false when the entry has no such field at
	// all — different data from a field that is present and empty.
	ReasoningOptions []ReasoningOption `json:"reasoning_options,omitempty"`
	ReasoningKnown   bool              `json:"reasoning_known,omitempty"`
}

// Model is a full model entry in the catalog.
type Model struct {
	ID                  string `json:"id"`
	Name                string `json:"name"`
	Provider            string `json:"provider"`
	APIModel            string `json:"api_model"`
	CompHash            string `json:"comp_hash,omitempty"`
	ContextWindow       int64  `json:"context_window"`
	InputTokenLimit     int64  `json:"input_token_limit,omitempty"`
	DefaultMaxTokens    int64  `json:"default_max_tokens"`
	CanReason           bool   `json:"can_reason"`
	SupportsAttachments bool   `json:"supports_attachments"`
	// Kind is the catalog's model type when it is not a chat model, "" for
	// chat models. Chat() is the predicate to ask.
	Kind string `json:"kind,omitempty"`
	// Status is the catalog's lifecycle marker; see Hit.
	Status string `json:"status,omitempty"`
	// ReasoningOptions and ReasoningKnown carry the entry's reasoning_options
	// as written; see Hit.
	ReasoningOptions []ReasoningOption `json:"reasoning_options,omitempty"`
	ReasoningKnown   bool              `json:"reasoning_known,omitempty"`
}

// Chat reports whether the catalog entry is a chat model. A non-empty Kind
// names what the model is instead (image, speech, transcription, embedding,
// reranking, decision, realtime, video), and such an entry is reference data
// for a picker that offers chat models — never a choice.
func (m Model) Chat() bool { return strings.TrimSpace(m.Kind) == "" }

// reasoningEffortLevels is the closed set of levels a reasoning_options
// description may name. The catalog is third-party data, so a level outside the
// set is dropped here rather than reaching a surface that cannot send it.
var reasoningEffortLevels = map[string]bool{
	"none":    true,
	"minimal": true,
	"low":     true,
	"medium":  true,
	"high":    true,
	"xhigh":   true,
	"max":     true,
}

// ReasoningEfforts returns the discrete thinking levels this entry's
// reasoning_options name, in file order, de-duplicated, and restricted to
// levels this build knows how to send. known is false when the entry carries
// no reasoning_options field at all; known with no levels means the model
// reasons with nothing to pick between (a toggle or a token budget).
func (m Model) ReasoningEfforts() ([]string, bool) {
	return effortsFromOptions(m.ReasoningOptions, m.ReasoningKnown)
}

// ReasoningEfforts is Model.ReasoningEfforts for a lookup hit.
func (h Hit) ReasoningEfforts() ([]string, bool) {
	return effortsFromOptions(h.ReasoningOptions, h.ReasoningKnown)
}

func effortsFromOptions(options []ReasoningOption, known bool) ([]string, bool) {
	if !known {
		return nil, false
	}
	var out []string
	seen := make(map[string]bool, len(options))
	for _, option := range options {
		if !strings.EqualFold(strings.TrimSpace(option.Type), "effort") {
			continue
		}
		for _, value := range option.Values {
			level := strings.ToLower(strings.TrimSpace(value))
			if level == "" || !reasoningEffortLevels[level] || seen[level] {
				continue
			}
			seen[level] = true
			out = append(out, level)
		}
	}
	return out, true
}

// Limits reconstructs the catalog Limits view for this global lookup hit.
func (h Hit) Limits() Limits {
	return Limits{Context: int(h.ContextWindow), Input: int(h.InputTokenLimit), Output: int(h.DefaultMaxTokens)}
}

// EffectiveOutputReserve returns the number of tokens to reserve for model
// output, falling back to a fraction of the context window when the database
// omits an explicit output limit.
func (l Limits) EffectiveOutputReserve() int {
	if l.Output > 0 {
		return l.Output
	}
	if l.Context > 0 {
		return l.Context / 6
	}
	return 0
}

// EffectiveInputLimit returns the usable input budget: the explicit input
// limit when present, otherwise the full context window for compatibility with
// older catalog entries that only specify context/output.
func (l Limits) EffectiveInputLimit() int {
	if l.Input > 0 {
		return l.Input
	}
	if l.Context > 0 {
		return l.Context
	}
	return 0
}

// Catalog is an immutable lookup table from model id to Limits and Model.
type Catalog struct {
	byID     map[string]Limits
	bySuffix map[string]Limits // segment after the last '/', lowercased; unambiguous only
	models   map[string]Model  // full model info by id
}

type rawEntry struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	CompHash   string `json:"comp_hash"`
	Reasoning  bool   `json:"reasoning"`
	Attachment bool   `json:"attachment"`
	// Type is the catalog's non-chat model type; absent for chat models.
	Type string `json:"type"`
	// Status is the upstream lifecycle marker ("deprecated" for a retired
	// model); absent when the catalog names none.
	Status string `json:"status"`
	// ReasoningOptions is a pointer so "the field is absent" and "the field is
	// an empty array" stay distinguishable: an empty array is a model that
	// reasons with no discrete level, an absent field is a model the catalog
	// says nothing about.
	ReasoningOptions *[]ReasoningOption `json:"reasoning_options"`
	Limit            struct {
		Context int `json:"context"`
		Input   int `json:"input"`
		Output  int `json:"output"`
	} `json:"limit"`
}

// Parse builds a Catalog from raw models.json bytes.
func Parse(data []byte) (*Catalog, error) {
	var raw map[string]rawEntry
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("modelcatalog: parse: %w", err)
	}
	c := &Catalog{
		byID:     make(map[string]Limits, len(raw)),
		bySuffix: make(map[string]Limits, len(raw)),
		models:   make(map[string]Model, len(raw)),
	}
	suffixSeen := make(map[string]int, len(raw))
	for id, e := range raw {
		lim := Limits{Context: e.Limit.Context, Input: e.Limit.Input, Output: e.Limit.Output}
		lowerID := strings.ToLower(strings.TrimSpace(id))
		c.byID[lowerID] = lim
		if seg := lastSegment(id); seg != "" {
			suffixSeen[seg]++
			c.bySuffix[seg] = lim
		}
		// Extract provider and API model from id (e.g., "deepseek/deepseek-v4-flash")
		provider, apiModel := "", ""
		if slash := strings.LastIndex(id, "/"); slash >= 0 {
			provider, apiModel = id[:slash], id[slash+1:]
		}
		model := Model{
			ID:                  e.ID,
			Name:                e.Name,
			Provider:            provider,
			APIModel:            apiModel,
			CompHash:            strings.TrimSpace(e.CompHash),
			ContextWindow:       int64(e.Limit.Context),
			InputTokenLimit:     int64(e.Limit.Input),
			DefaultMaxTokens:    int64(e.Limit.Output),
			CanReason:           e.Reasoning,
			SupportsAttachments: e.Attachment,
			Kind:                strings.TrimSpace(e.Type),
			Status:              strings.TrimSpace(e.Status),
		}
		if e.ReasoningOptions != nil {
			model.ReasoningKnown = true
			model.ReasoningOptions = append([]ReasoningOption(nil), (*e.ReasoningOptions)...)
		}
		c.models[lowerID] = model
	}
	// Drop ambiguous suffixes (same short name across providers).
	for seg, n := range suffixSeen {
		if n > 1 {
			delete(c.bySuffix, seg)
		}
	}
	return c, nil
}

// errNoCatalog reports that this build has no catalog to load at all. It is
// the one failure Resolve rewords: every other failure is about a file that IS
// there, and its own error — which names the file and the offending byte — is
// more useful than "not found".
var errNoCatalog = errors.New("modelcatalog: no catalog")

// errModelDataNotFound is what a surface may show when this build ships no
// catalog: one sentence, no machine layout (owner 2026-11-07).
var errModelDataNotFound = errors.New("model data not found; reinstall forebrain")

// Load reads the model catalog this build ships from basePath. There is one
// file and one layer: models.json beside the binary, exactly as the build
// carries it. Nothing merges over it and nothing is added beside it (owner
// 2026-11-09); a configured row's own overrides are a separate layer that
// Lookup applies, not a second document. A missing base is errNoCatalog rather
// than an empty catalog, because "this build carries no data" is a state the
// caller has to be able to name.
func Load(basePath string) (*Catalog, error) {
	baseData, err := readOptionalFile(basePath)
	if err != nil {
		return nil, err
	}
	if baseData == nil {
		return nil, fmt.Errorf("%w at %q", errNoCatalog, basePath)
	}
	cat, err := Parse(baseData)
	if err != nil {
		// The file is in the build, so a parse failure is a defect in the
		// data: the error names the path and the byte, never "not found".
		return nil, fmt.Errorf("%q: %w", basePath, err)
	}
	return cat, nil
}

// readOptionalFile reads path, reporting nil bytes when it does not exist.
func readOptionalFile(path string) ([]byte, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("modelcatalog: load %q: %w", path, err)
	}
	return data, nil
}

// Lookup returns the limits for a model id. It matches exactly (case
// insensitive) first, then falls back to the unambiguous short name after the
// last '/'. Returns ok=false when the model is unknown or the catalog is nil.
func (c *Catalog) Lookup(model string) (Limits, bool) {
	if c == nil {
		return Limits{}, false
	}
	key := strings.ToLower(strings.TrimSpace(model))
	if key == "" {
		return Limits{}, false
	}
	if lim, ok := c.byID[key]; ok {
		return lim, true
	}
	if seg := lastSegment(key); seg != "" {
		if lim, ok := c.bySuffix[seg]; ok {
			return lim, true
		}
	}
	return Limits{}, false
}

// LookupModel returns the full model entry for a model id.
func (c *Catalog) LookupModel(model string) (Model, bool) {
	if c == nil {
		return Model{}, false
	}
	key := strings.ToLower(strings.TrimSpace(model))
	if key == "" {
		return Model{}, false
	}
	if m, ok := c.models[key]; ok {
		return m, true
	}
	if seg := lastSegment(key); seg != "" {
		// A bare model name several ids share resolves to the first id in sorted
		// order: the answer has to be stable run to run, and map iteration is not
		// (the suffix table drops ambiguous names, LookupModel must still answer
		// one — a custom model typed in is named this way).
		matches := make([]string, 0, 2)
		for id := range c.models {
			if lastSegment(id) == seg {
				matches = append(matches, id)
			}
		}
		if len(matches) > 0 {
			sort.Strings(matches)
			return c.models[matches[0]], true
		}
	}
	return Model{}, false
}

// AllModels returns all models in the catalog.
func (c *Catalog) AllModels() []Model {
	if c == nil {
		return nil
	}
	out := make([]Model, 0, len(c.models))
	for _, m := range c.models {
		out = append(out, m)
	}
	return out
}

// HasProvider reports whether the catalog contains any model for the given
// provider prefix (case-insensitive).
func (c *Catalog) HasProvider(provider string) bool {
	if c == nil {
		return false
	}
	prefix := strings.ToLower(strings.TrimSpace(provider)) + "/"
	for id := range c.models {
		if strings.HasPrefix(id, prefix) {
			return true
		}
	}
	return false
}

// JoinProviderModel builds a models.json lookup key from a provider and api
// model id, e.g. ("anthropic","claude-opus-4-5") -> "anthropic/claude-opus-4-5".
// When the provider is empty it returns the model alone (the short-name
// fallback in Lookup still applies).
func JoinProviderModel(provider, model string) string {
	model = strings.TrimSpace(model)
	provider = strings.TrimSpace(provider)
	if model == "" {
		return ""
	}
	if provider == "" {
		return model
	}
	return provider + "/" + model
}

func lastSegment(id string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	if id == "" {
		return ""
	}
	if i := strings.LastIndex(id, "/"); i >= 0 && i+1 < len(id) {
		return id[i+1:]
	}
	return id
}

// Resolve loads the model catalog this build ships: models.json beside the
// binary (see shippedCatalogPath), exactly as it was built. There is no home
// copy and nothing merges over the file (owner 2026-11-09), so a catalog the
// user's configuration changes is expressed as that row's overrides, not as a
// second document. The result is cached per base path and file content, so a
// replaced file takes effect on the next resolve.
//
// Only "there is no catalog anywhere" is reworded to the sentence a surface
// may show. Every other failure is returned as it came: a catalog the build
// shipped broken has to report the byte that broke, not look like a missing
// file.
func Resolve() (*Catalog, error) {
	basePath := shippedCatalogPath()
	cat, err := loadCached(basePath)
	if err == nil {
		return cat, nil
	}
	if errors.Is(err, errNoCatalog) {
		// The path is for the debug log; the error is what a surface may show.
		slog.Debug("model data not found", "path", basePath)
		return nil, errModelDataNotFound
	}
	return nil, err
}

// shippedCatalogPath is the models.json that ships beside the binary (or is
// named by FOREBRAIN_ASSETS_DIR). Empty when there is no asset directory to
// resolve.
func shippedCatalogPath() string {
	dir, err := AssetsDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, "models.json")
}

var (
	cacheMu     sync.Mutex
	cache       = map[string]*Catalog{}
	globalCat   *Catalog
	globalCatMu sync.RWMutex
)

func loadCached(basePath string) (*Catalog, error) {
	base := absolutePath(basePath)
	key := base + "\x00" + fileDigest(basePath)
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if c, ok := cache[key]; ok {
		return c, nil
	}
	c, err := Load(basePath)
	if err != nil {
		return nil, err
	}
	cache[key] = c
	return c, nil
}

func absolutePath(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

// fileDigest is the content digest the catalog cache key carries, so a catalog
// file that was replaced (a new build installed beside a running binary) is
// re-read instead of answering from a stale cache. "" means the file is absent.
func fileDigest(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// InitGlobalCatalog initializes the global catalog from the models.json beside
// the binary. It is the only model-data source: nothing is copied into a home
// and nothing in a home is read (owner 2026-11-09), so every process on this
// build answers from the same bytes, and a build with no catalog is a warning
// its caller logs — not a catalog this process can download or invent.
func InitGlobalCatalog() error {
	cat, err := Resolve()
	if err != nil {
		return err
	}
	globalCatMu.Lock()
	globalCat = cat
	globalCatMu.Unlock()
	return nil
}

// Lookup returns the model entry for a (provider, apiModel) pair using the global catalog.
// The global catalog is initialized by InitGlobalCatalog at startup.
//
// Three layers answer, in order. The catalog (models.json) is the base; the
// built-in model table covers providers it has no rows for (volcengine,
// byteplus) and ranks below it; a configured row's own overrides win over
// both.
//
// A caller that knows only the pair gets a row's overrides only when exactly
// one configured row names that pair: with several rows naming it, none of
// them is "the" answer, so the catalog's values stand alone. Callers that know
// the row use LookupRow. A custom model the catalog does not list borrows the capabilities of
// the catalog entry its name happens to be (a user typing "openai/gpt-4o"
// gets gpt-4o's context window), and a custom model that matches nothing has
// no entry at all.
func Lookup(provider, apiModel string) (Hit, bool) {
	hit, ok := LookupCatalog(provider, apiModel)
	if override, found := modelOverrideForPair(provider, apiModel); found {
		return applyModelOverride(hit, override), true
	}
	return hit, ok
}

// LookupRow is Lookup for a caller that knows which configured row it asks
// about (owner 2026-11-10). Two rows may name the same provider/model and carry
// different capabilities, so the row — not the pair — is the identity: the
// catalog's entry for the pair, with that row's own overrides laid over it.
// rowID "" behaves like Lookup.
func LookupRow(rowID, provider, apiModel string) (Hit, bool) {
	hit, ok := LookupCatalog(provider, apiModel)
	if override, found := modelOverrideForRow(strings.TrimSpace(rowID), provider, apiModel); found {
		return applyModelOverride(hit, override), true
	}
	return hit, ok
}

// LookupCatalog is Lookup without the configured-row override layer: the
// catalog, the built-in model table, and the entry a custom model's name
// borrows. A surface asking what a model can do *by default* reads this — the
// models page compares a row's overrides against these values, so an override
// of the row being edited must not become part of the answer (plan 265).
func LookupCatalog(provider, apiModel string) (Hit, bool) {
	globalCatMu.RLock()
	cat := globalCat
	globalCatMu.RUnlock()
	key := JoinProviderModel(provider, apiModel)
	var hit Hit
	ok := false
	if cat != nil {
		if m, found := cat.LookupModel(key); found {
			hit, ok = hitFromModel(m), true
		}
	}
	if !ok {
		if m, found := builtinModel(provider, apiModel); found {
			hit, ok = hitFromModel(m), true
		}
	}
	if !ok && cat != nil && strings.EqualFold(strings.TrimSpace(provider), customModelProvider) && strings.Contains(apiModel, "/") {
		if m, found := cat.LookupModel(apiModel); found {
			hit, ok = hitFromModel(m), true
		}
	}
	return hit, ok
}

func hitFromModel(m Model) Hit {
	return Hit{
		CatalogID:           m.ID,
		CompHash:            m.CompHash,
		ContextWindow:       m.ContextWindow,
		InputTokenLimit:     m.InputTokenLimit,
		DefaultMaxTokens:    m.DefaultMaxTokens,
		CanReason:           m.CanReason,
		SupportsAttachments: m.SupportsAttachments,
		Kind:                m.Kind,
		Status:              m.Status,
		ReasoningOptions:    m.ReasoningOptions,
		ReasoningKnown:      m.ReasoningKnown,
	}
}

// customModelProvider is pkg/config's CustomProvider, restated here because
// llm sits below it and may not import it. pkg/process's
// TestCustomProviderIDsAgree pins the two spellings together.
const customModelProvider = "custom"

// ModelOverride is one configured row's capability overrides (the
// fb_llm_models cap_* columns). A nil pointer, a nil slice or "" leaves the
// catalog's value.
type ModelOverride struct {
	SupportsImages, SupportsPDF, SupportsVideo, SupportsAudio, CanReason, ReasoningOff *bool
	// Efforts replaces the levels a model offers when non-nil. PDF, Video and
	// Audio are carried for the surfaces that consume those capabilities;
	// nothing here reads them yet.
	Efforts []string
	// ContextWindow and MaxOutput replace the windows the catalog carries;
	// MaxOutput is the row's own output limit.
	ContextWindow, MaxOutput *int64
}

var (
	overrideMu sync.RWMutex
	// overrides is what each configured row says about its own model, keyed by
	// the row id: the row is the identity, so two rows naming the same
	// provider/model keep their own capabilities (owner 2026-11-10).
	overrides map[string]ModelOverride
	// rowsByPair maps JoinProviderModel(provider, model) to the ids of the rows
	// that name it, so a caller that knows only the pair can find the row that
	// owns its overrides — and only when there is exactly one.
	rowsByPair map[string][]string

	builtinModelMu sync.RWMutex
	builtinModels  map[string][]Model
)

// SetModelOverrides installs the overrides the configured rows carry, keyed by
// row id, with pairOf naming each row's JoinProviderModel(provider, model).
// The process installs them every time it adopts a configuration; nil clears
// them.
func SetModelOverrides(byRow map[string]ModelOverride, pairOf map[string]string) {
	installed := make(map[string]ModelOverride, len(byRow))
	pairs := make(map[string][]string, len(byRow))
	for rawID, override := range byRow {
		id := strings.ToLower(strings.TrimSpace(rawID))
		if id == "" {
			continue
		}
		override.Efforts = append([]string(nil), override.Efforts...)
		installed[id] = override
		pair := strings.ToLower(strings.TrimSpace(pairOf[rawID]))
		if pair == "" {
			continue
		}
		pairs[pair] = append(pairs[pair], id)
	}
	overrideMu.Lock()
	overrides = installed
	rowsByPair = pairs
	overrideMu.Unlock()
}

// modelOverrideForRow is the override a lookup applies: the named row's own
// when a row is named, the pair's only row otherwise.
func modelOverrideForRow(rowID, provider, apiModel string) (ModelOverride, bool) {
	overrideMu.RLock()
	defer overrideMu.RUnlock()
	if len(overrides) == 0 {
		return ModelOverride{}, false
	}
	if id := strings.ToLower(strings.TrimSpace(rowID)); id != "" {
		o, ok := overrides[id]
		return o, ok
	}
	return overrideForPairLocked(provider, apiModel)
}

// modelOverrideForPair answers a caller that knows only the pair.
func modelOverrideForPair(provider, apiModel string) (ModelOverride, bool) {
	overrideMu.RLock()
	defer overrideMu.RUnlock()
	if len(overrides) == 0 {
		return ModelOverride{}, false
	}
	return overrideForPairLocked(provider, apiModel)
}

// overrideForPairLocked applies a row's overrides to a pair only when exactly
// one configured row names that pair.
func overrideForPairLocked(provider, apiModel string) (ModelOverride, bool) {
	pair := strings.ToLower(strings.TrimSpace(JoinProviderModel(provider, apiModel)))
	ids := rowsByPair[pair]
	if len(ids) != 1 {
		return ModelOverride{}, false
	}
	o, ok := overrides[ids[0]]
	return o, ok
}

// OverlayModelOverride lays one row's capabilities over a hit — the same
// projection applyModelOverride makes, exported for a caller that read the row
// live (a dispatch on a model the turn's configuration does not know yet, owner
// 2026-11-10). A field the row says nothing about keeps the catalog's value.
func OverlayModelOverride(hit Hit, override ModelOverride) Hit {
	return applyModelOverride(hit, override)
}

// applyModelOverride lays one configured row's capabilities over a hit. A row
// that says nothing about a field leaves the catalog's value in place.
func applyModelOverride(hit Hit, override ModelOverride) Hit {
	if override.SupportsImages != nil {
		hit.SupportsAttachments = *override.SupportsImages
	}
	if override.CanReason != nil {
		hit.CanReason = *override.CanReason
	}
	if override.ContextWindow != nil {
		hit.ContextWindow = *override.ContextWindow
	}
	if override.MaxOutput != nil {
		hit.DefaultMaxTokens = *override.MaxOutput
	}
	switch {
	case override.CanReason != nil && !*override.CanReason:
		// A model the row says does not reason: no levels to pick from, and
		// that is known rather than missing.
		hit.ReasoningOptions = nil
		hit.ReasoningKnown = true
	case override.Efforts != nil || override.ReasoningOff != nil:
		var levels []string
		if override.Efforts != nil {
			levels = override.Efforts
		} else {
			levels, _ = effortsFromOptions(hit.ReasoningOptions, hit.ReasoningKnown)
		}
		if override.ReasoningOff != nil && *override.ReasoningOff {
			levels = append([]string{"none"}, levels...)
		}
		hit.ReasoningOptions = []ReasoningOption{{Type: "effort", Values: levels}}
		hit.ReasoningKnown = true
	}
	return hit
}

// SetBuiltinModels installs capability rows for providers the catalog has no
// entries for (pkg/config's registry Models: volcengine, byteplus). They rank
// below the catalog and above nothing: a configured row's overrides still win.
// provider is the catalog prefix; an empty model list removes what it had.
func SetBuiltinModels(provider string, models []Model) {
	p := strings.ToLower(strings.TrimSpace(provider))
	if p == "" {
		return
	}
	builtinModelMu.Lock()
	defer builtinModelMu.Unlock()
	if builtinModels == nil {
		builtinModels = map[string][]Model{}
	}
	if len(models) == 0 {
		delete(builtinModels, p)
		return
	}
	installed := make([]Model, 0, len(models))
	for _, m := range models {
		if strings.TrimSpace(m.ID) == "" {
			continue
		}
		m.Provider = p
		installed = append(installed, m)
	}
	builtinModels[p] = installed
}

func builtinModel(provider, apiModel string) (Model, bool) {
	p := strings.ToLower(strings.TrimSpace(provider))
	model := strings.ToLower(strings.TrimSpace(apiModel))
	if p == "" || model == "" {
		return Model{}, false
	}
	builtinModelMu.RLock()
	defer builtinModelMu.RUnlock()
	for _, m := range builtinModels[p] {
		if strings.EqualFold(strings.TrimSpace(m.ID), model) || strings.EqualFold(strings.TrimSpace(m.APIModel), model) {
			return m, true
		}
	}
	return Model{}, false
}

// ReasoningEfforts names the discrete thinking levels the catalog's data gives
// (provider, apiModel), and whether it says anything at all. It is the single
// source of truth for that question: the levels live in the shipped models.json
// reasoning_options, so a configured row's override of them is read through
// Lookup, not through here.
func ReasoningEfforts(provider, apiModel string) (levels []string, known bool) {
	hit, ok := Lookup(provider, apiModel)
	if !ok {
		return nil, false
	}
	return hit.ReasoningEfforts()
}

// ReasoningEffortsRow is ReasoningEfforts for a caller that knows the
// configured row: its own levels win over the catalog's and over a same-pair
// sibling's.
func ReasoningEffortsRow(rowID, provider, apiModel string) (levels []string, known bool) {
	hit, ok := LookupRow(rowID, provider, apiModel)
	if !ok {
		return nil, false
	}
	return hit.ReasoningEfforts()
}

// LoginBackedProvider reports whether provider's catalog belongs to an account
// rather than to models.json: ChatGPT's models come from the subscription
// backend, and the two OpenCode surfaces' from the Console / Go catalog after
// login. models.json carries reference rows for the OpenCode models (windows,
// modalities, levels) and nothing for ChatGPT, but neither is a static list a
// picker should sell — a listing skips them, and bare-model provider inference
// ignores them, because their real set is answered live or not at all.
func LoginBackedProvider(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "chatgpt", "opencode", "opencode-go":
		return true
	}
	return false
}

// AllModels returns all models from the global catalog.
func AllModels() []Model {
	globalCatMu.RLock()
	cat := globalCat
	globalCatMu.RUnlock()
	if cat == nil {
		return nil
	}
	return cat.AllModels()
}

// KnownProvider reports whether provider is a known LLM provider. The set of
// known providers is derived entirely from models.json (each catalog key's
// prefix before the first '/'), plus the built-in model table a provider with
// no catalog rows is installed through (volcengine, byteplus), plus the
// reserved custom-model provider, which by definition accepts anything. When
// the global catalog is not loaded the function is permissive (returns true)
// so callers without a catalog are not blocked.
func KnownProvider(provider string) bool {
	p := strings.ToLower(strings.TrimSpace(provider))
	if p == "" {
		return false
	}
	if p == customModelProvider {
		return true
	}
	globalCatMu.RLock()
	cat := globalCat
	globalCatMu.RUnlock()
	if cat == nil {
		return true // permissive when catalog unavailable
	}
	if cat.HasProvider(p) {
		return true
	}
	builtinModelMu.RLock()
	defer builtinModelMu.RUnlock()
	return len(builtinModels[p]) > 0
}

// SetGlobalCatalogForTest installs a catalog as the global catalog for testing.
// Production code initializes it through InitGlobalCatalog.
func SetGlobalCatalogForTest(cat *Catalog) {
	globalCatMu.Lock()
	globalCat = cat
	globalCatMu.Unlock()
}

// FormatProviderModel renders a provider/model pair as the "provider/model"
// label used across the UI: the /model picker, the composer footer, approval
// rows and plan-review reviewer names. It is the inverse of
// ParseProviderModelLabel, and lives beside it so both halves of the format
// have exactly one definition. A missing half yields the half that is present
// rather than a label with a dangling separator.
func FormatProviderModel(provider, model string) string {
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	switch {
	case provider == "" && model == "":
		return ""
	case provider == "":
		return model
	case model == "":
		return provider
	default:
		return provider + "/" + model
	}
}

func ParseProviderModelLabel(label string) (string, string) {
	provider, model, ok := strings.Cut(strings.TrimSpace(label), "/")
	if !ok {
		return "", strings.TrimSpace(label)
	}
	return strings.TrimSpace(provider), strings.TrimSpace(model)
}

func LeadingTokenLooksLikeFilesystemPath(tok string) bool {
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return false
	}
	if len(tok) >= 3 {
		c0 := tok[0]
		isLetter := (c0 >= 'A' && c0 <= 'Z') || (c0 >= 'a' && c0 <= 'z')
		if isLetter && tok[1] == ':' && (tok[2] == '\\' || tok[2] == '/') {
			return true
		}
	}
	if strings.HasPrefix(tok, "/") && strings.Count(tok, "/") >= 2 {
		return true
	}
	if strings.HasPrefix(tok, "~/") || tok == "~" {
		return true
	}
	if strings.HasPrefix(tok, "./") || strings.HasPrefix(tok, "../") {
		return true
	}
	return false
}
