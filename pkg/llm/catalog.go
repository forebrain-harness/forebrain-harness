// Model catalog, display labels, and local install paths.
package llm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	DisplayName         string `json:"display_name,omitempty"`
	CompHash            string `json:"comp_hash,omitempty"`
	ContextWindow       int64  `json:"context_window,omitempty"`
	InputTokenLimit     int64  `json:"input_token_limit,omitempty"`
	DefaultMaxTokens    int64  `json:"default_max_tokens,omitempty"`
	CanReason           bool   `json:"can_reason,omitempty"`
	SupportsAttachments bool   `json:"supports_attachments,omitempty"`
	// Kind is the catalog's model type when it is not a chat model ("image",
	// "speech", "embedding", "decision", …), "" for chat models.
	Kind string `json:"kind,omitempty"`
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
// description may name. models.json is an editable file, so a level outside
// the set is dropped here rather than reaching a surface that cannot send it.
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

// errNoCatalog reports that neither a shipped base nor an overlay exists, so
// there is no catalog to load at all. It is the one failure Resolve rewords:
// every other failure is about a file that IS there, and its own error — which
// names the file and the offending byte — is more useful than "not found".
var errNoCatalog = errors.New("modelcatalog: no catalog")

// Load reads a model catalog from basePath and merges overlayPath over it.
//
// basePath is the shipped catalog (assets/models.json beside the binary);
// overlayPath is $FOREBRAIN_HOME/models.json, a file the user owns and edits.
// The merge is per key and, inside a key, per field:
//
//   - a top-level key only in the base stays as shipped;
//   - a top-level key only in the overlay is added;
//   - a key in both keeps every shipped field the overlay does not name, and
//     takes the overlay's value for every field it does name;
//   - an overlay entry (or field) that is null removes the key (or falls the
//     field back to nothing), which is how a user deletes a model.
//
// Arrays and nested objects (`reasoning_options`, `limit`, `modalities`) are
// values like any other: the overlay replaces them whole and never merges them
// element-wise, so naming a field replaces that field's value entirely. A
// missing overlay is not an error (a user may never have written one); a
// missing base is not an error either, because a source build may ship no
// asset — the overlay is then the whole catalog. Both missing is an error.
func Load(basePath, overlayPath string) (*Catalog, error) {
	baseData, err := readOptionalFile(basePath)
	if err != nil {
		return nil, err
	}
	overlayData, err := readOptionalFile(overlayPath)
	if err != nil {
		return nil, err
	}
	if baseData == nil && overlayData == nil {
		return nil, fmt.Errorf("%w at %q or overlay at %q", errNoCatalog, basePath, overlayPath)
	}
	merged, err := mergeCatalogJSON(baseData, overlayData)
	if err != nil {
		return nil, mergedCatalogError(overlayPath, basePath, err)
	}
	cat, err := Parse(merged)
	if err != nil {
		return nil, mergedCatalogError(overlayPath, basePath, err)
	}
	return cat, nil
}

// mergedCatalogError reports a failure that came out of the merged document,
// naming the files it was built from, the user's overlay first because that is
// the one they can fix. The overlay is the only file anybody hand-edits, so a
// typo in it is the likeliest failure the two-layer scheme has; its error has
// to point at their path — not at a role ("parse overlay") they cannot map to a
// file, and never at a "not found" that sends them hunting for a file they are
// looking at. No "modelcatalog:" prefix: everything wrapped here already
// carries it.
func mergedCatalogError(overlayPath, basePath string, err error) error {
	if strings.TrimSpace(overlayPath) == "" {
		return fmt.Errorf("%q: %w", basePath, err)
	}
	return fmt.Errorf("%q over %q: %w", overlayPath, basePath, err)
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

// mergeCatalogJSON merges overlay model entries over base ones, per field, and
// returns the merged document. A null overlay entry deletes the model; a null
// overlay field drops that field from the shipped entry.
func mergeCatalogJSON(baseData, overlayData []byte) ([]byte, error) {
	base := map[string]json.RawMessage{}
	if len(baseData) > 0 {
		if err := json.Unmarshal(baseData, &base); err != nil {
			return nil, fmt.Errorf("modelcatalog: parse base: %w", err)
		}
	}
	overlay := map[string]json.RawMessage{}
	if len(overlayData) > 0 {
		if err := json.Unmarshal(overlayData, &overlay); err != nil {
			return nil, fmt.Errorf("modelcatalog: parse overlay: %w", err)
		}
	}
	merged := make(map[string]json.RawMessage, len(base)+len(overlay))
	for key, raw := range base {
		merged[key] = raw
	}
	for key, raw := range overlay {
		if isJSONNull(raw) {
			delete(merged, key)
			continue
		}
		shipped, ok := merged[key]
		if !ok {
			merged[key] = raw
			continue
		}
		entry, err := mergeEntryJSON(shipped, raw)
		if err != nil {
			return nil, fmt.Errorf("modelcatalog: merge %q: %w", key, err)
		}
		merged[key] = entry
	}
	out, err := json.Marshal(merged)
	if err != nil {
		return nil, fmt.Errorf("modelcatalog: merge: %w", err)
	}
	return out, nil
}

// mergeEntryJSON merges one entry's fields: every field the overlay names wins,
// a null field is dropped, and every field it does not name keeps its shipped
// value. Non-object values (a malformed hand edit) let the overlay win whole.
func mergeEntryJSON(shipped, overlay json.RawMessage) (json.RawMessage, error) {
	shippedFields := map[string]json.RawMessage{}
	overlayFields := map[string]json.RawMessage{}
	if json.Unmarshal(shipped, &shippedFields) != nil || json.Unmarshal(overlay, &overlayFields) != nil {
		return overlay, nil
	}
	for field, raw := range overlayFields {
		if isJSONNull(raw) {
			delete(shippedFields, field)
			continue
		}
		shippedFields[field] = raw
	}
	out, err := json.Marshal(shippedFields)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func isJSONNull(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "null"
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
		// Search by suffix (short name) - return first match
		for _, m := range c.models {
			if lastSegment(m.ID) == seg {
				return m, true
			}
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

// Resolve loads the effective model catalog: the shipped catalog beside the
// binary as the base, with $FOREBRAIN_HOME/models.json merged over it field by
// field (see Load). The home file is the user's: whatever it names wins, and a
// shipped field it does not name (a column their copy predates, say) still
// arrives. The result is cached per base path, overlay path and overlay
// content, so editing the overlay takes effect on the next resolve.
//
// Only "there is no catalog anywhere" is reworded to name the home. Every other
// failure is returned as it came: an overlay a user broke while editing it has
// to report the byte that broke, not look like a missing file.
func Resolve(home string) (*Catalog, error) {
	basePath := shippedCatalogPath()
	overlayPath := ""
	if h := strings.TrimSpace(home); h != "" {
		overlayPath = filepath.Join(h, "models.json")
	}
	cat, err := loadCached(basePath, overlayPath)
	if err == nil {
		return cat, nil
	}
	if errors.Is(err, errNoCatalog) {
		return nil, fmt.Errorf("modelcatalog: models.json not found (home=%q)", home)
	}
	return nil, err
}

// shippedCatalogPath is the models.json that ships beside the binary (or is
// named by FOREBRAIN_ASSETS_DIR). Empty when there is no asset directory to
// resolve — a build without shipped assets then reads the home overlay alone.
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

func loadCached(basePath, overlayPath string) (*Catalog, error) {
	base := absolutePath(basePath)
	overlay := absolutePath(overlayPath)
	key := base + "\x00" + overlay + "\x00" + fileDigest(overlay)
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if c, ok := cache[key]; ok {
		return c, nil
	}
	c, err := Load(basePath, overlayPath)
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

// fileDigest is the content digest the catalog cache key carries, so a home
// file the user edited is re-read instead of answering from a stale cache.
// "" means the file is absent.
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

// InitGlobalCatalog initializes the global catalog: it makes sure the home
// holds its copy of models.json (writing a full copy only when the file is
// missing), then resolves the shipped catalog with the home copy merged over
// it. Loading and seeding are one step: a fresh home's first process must not
// run without the catalog it installs a moment later, reading every model's
// limits as unknown. version is passed in (llm sits below pkg/home) so a build
// with no catalog beside it can download its release's copy.
func InitGlobalCatalog(home, version string) error {
	if err := Install(home, version); err != nil {
		return err
	}
	cat, err := Resolve(home)
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
func Lookup(provider, apiModel string) (Hit, bool) {
	globalCatMu.RLock()
	cat := globalCat
	globalCatMu.RUnlock()
	if cat == nil {
		return Hit{}, false
	}
	key := JoinProviderModel(provider, apiModel)
	if m, ok := cat.LookupModel(key); ok {
		return Hit{
			CatalogID:           m.ID,
			DisplayName:         m.Name,
			CompHash:            m.CompHash,
			ContextWindow:       m.ContextWindow,
			InputTokenLimit:     m.InputTokenLimit,
			DefaultMaxTokens:    m.DefaultMaxTokens,
			CanReason:           m.CanReason,
			SupportsAttachments: m.SupportsAttachments,
			Kind:                m.Kind,
			ReasoningOptions:    m.ReasoningOptions,
			ReasoningKnown:      m.ReasoningKnown,
		}, true
	}
	return Hit{}, false
}

// ReasoningEfforts names the discrete thinking levels the catalog's merged data
// gives (provider, apiModel), and whether it says anything at all. It is the
// single source of truth for that question: the levels live in models.json's
// reasoning_options, shipped base and home overlay already merged, so a value
// the user wrote in their overlay is what this returns.
func ReasoningEfforts(provider, apiModel string) (levels []string, known bool) {
	hit, ok := Lookup(provider, apiModel)
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
// prefix before the first '/'). When the global catalog is not loaded the
// function is permissive (returns true) so callers without a catalog are not
// blocked.
func KnownProvider(provider string) bool {
	p := strings.ToLower(strings.TrimSpace(provider))
	if p == "" {
		return false
	}
	globalCatMu.RLock()
	cat := globalCat
	globalCatMu.RUnlock()
	if cat == nil {
		return true // permissive when catalog unavailable
	}
	return cat.HasProvider(p)
}

// SetGlobalCatalogForTest installs a catalog as the global catalog for testing.
// Production code should use InitGlobalCatalog to load from models.json.
func SetGlobalCatalogForTest(cat *Catalog) {
	globalCatMu.Lock()
	globalCat = cat
	globalCatMu.Unlock()
}

// IsOpus reports whether (provider, apiModel) refers to an Anthropic Opus
// variant. The Anthropic /fast service tier is gated to Opus models; other
// Anthropic models (Sonnet, Haiku) and non-Anthropic providers return false.
func IsOpus(provider, apiModel string) bool {
	if !strings.EqualFold(strings.TrimSpace(provider), "anthropic") {
		return false
	}
	return strings.Contains(strings.ToLower(strings.TrimSpace(apiModel)), "opus")
}

// IsOpusLabel parses a "provider / model" or "provider/model - extra" label
// and reports whether it is an Anthropic Opus variant.
func IsOpusLabel(label string) bool {
	provider, model := ParseProviderModelLabel(label)
	return IsOpus(provider, model)
}

// FormatProviderModel renders a provider/model pair as the "provider / model"
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
		return provider + " / " + model
	}
}

func ParseProviderModelLabel(label string) (string, string) {
	base := strings.TrimSpace(strings.SplitN(label, " - ", 2)[0])
	provider, model, ok := strings.Cut(base, "/")
	if !ok {
		return "", strings.TrimSpace(base)
	}
	return strings.TrimSpace(provider), strings.TrimSpace(model)
}

// Install makes sure $FOREBRAIN_HOME/models.json exists, writing a full copy of
// the shipped catalog (assets/models.json beside the binary) when the home has
// none. Existing files are NEVER written, upgraded, rewritten, backed up or
// migrated — not one byte: the file belongs to the user. It is both a complete
// editable copy and an overlay, because Resolve merges its keys and fields over
// the shipped catalog: what the user writes wins, and a field their copy does
// not carry falls back to what this build ships. A release build that ships no
// catalog beside it downloads this version's models.json instead; a source
// build has neither, and the error names the install script that puts it there.
// Errors are non-fatal — callers should log a warning and continue.
func Install(forebrainHome, version string) error {
	if strings.TrimSpace(forebrainHome) == "" {
		return fmt.Errorf("modelcatalog: empty forebrainHome")
	}
	target := filepath.Join(forebrainHome, "models.json")

	if _, err := os.Stat(target); err == nil {
		// Already exists — never touch the user's file.
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("modelcatalog: stat %q: %w", target, err)
	}

	if err := os.MkdirAll(forebrainHome, 0o755); err != nil {
		return fmt.Errorf("modelcatalog: mkdir %q: %w", forebrainHome, err)
	}

	data, err := sourceModelsJSON(version)
	if err != nil {
		return err
	}
	if _, err := Parse(data); err != nil {
		return fmt.Errorf("modelcatalog: %w", err)
	}
	if err := writeCatalogAtomic(target, data); err != nil {
		return fmt.Errorf("modelcatalog: write %q: %w", target, err)
	}
	return nil
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
