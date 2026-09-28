// Model catalog, display labels, and local install paths.
package llm

import (
	"encoding/json"
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
	Limit      struct {
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
		c.models[lowerID] = Model{
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
		}
	}
	// Drop ambiguous suffixes (same short name across providers).
	for seg, n := range suffixSeen {
		if n > 1 {
			delete(c.bySuffix, seg)
		}
	}
	return c, nil
}

// Load reads and parses a models.json file.
func Load(path string) (*Catalog, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("modelcatalog: load %q: %w", path, err)
	}
	return Parse(data)
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

// Resolve loads $FOREBRAIN_HOME/models.json — the single canonical location for
// the model catalog database. The file is seeded by Install on first run and
// is user-editable; this function only reads it. The result is cached per
// resolved path.
func Resolve(home string) (*Catalog, error) {
	for _, p := range candidatePaths(home) {
		if p == "" {
			continue
		}
		if c, err := loadCached(p); err == nil {
			return c, nil
		}
	}
	return nil, fmt.Errorf("modelcatalog: models.json not found (home=%q)", home)
}

func candidatePaths(home string) []string {
	var out []string
	if h := strings.TrimSpace(home); h != "" {
		out = append(out, filepath.Join(h, "models.json"))
	}
	return out
}

var (
	cacheMu     sync.Mutex
	cache       = map[string]*Catalog{}
	globalCat   *Catalog
	globalCatMu sync.RWMutex
)

func loadCached(path string) (*Catalog, error) {
	abs := path
	if a, err := filepath.Abs(path); err == nil {
		abs = a
	}
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if c, ok := cache[abs]; ok {
		return c, nil
	}
	c, err := Load(abs)
	if err != nil {
		return nil, err
	}
	cache[abs] = c
	return c, nil
}

// InitGlobalCatalog initializes the global catalog from $FOREBRAIN_HOME/models.json,
// seeding that file from the embedded catalog first when the home has none.
// Loading and seeding are one step: a fresh home's first process must not
// run without the catalog it installs a moment later, reading every model's
// limits as unknown.
func InitGlobalCatalog(home string) error {
	if err := Install(home); err != nil {
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
		}, true
	}
	return Hit{}, false
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

// Install seeds $FOREBRAIN_HOME/models.json from the embedded catalog when it
// does not already exist. Existing files are NEVER overwritten so user
// customizations are preserved. Errors are non-fatal — callers should log a
// warning and continue.
func Install(forebrainHome string) error {
	if strings.TrimSpace(forebrainHome) == "" {
		return fmt.Errorf("modelcatalog: empty forebrainHome")
	}
	target := filepath.Join(forebrainHome, "models.json")

	if _, err := os.Stat(target); err == nil {
		// Already exists — never overwrite user customizations.
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("modelcatalog: stat %q: %w", target, err)
	}

	if err := os.MkdirAll(forebrainHome, 0o755); err != nil {
		return fmt.Errorf("modelcatalog: mkdir %q: %w", forebrainHome, err)
	}

	if err := os.WriteFile(target, embeddedModelsJSON, 0o644); err != nil {
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
