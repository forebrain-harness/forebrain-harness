package llm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestParseAndLookup(t *testing.T) {
	data := []byte(`{
		"anthropic/claude-opus-4-5": {"id":"anthropic/claude-opus-4-5","limit":{"context":200000,"output":64000}},
		"openai/gpt-5.5": {"id":"openai/gpt-5.5","limit":{"context":1050000,"input":922000,"output":128000}},
		"foo/dup": {"id":"foo/dup","limit":{"context":1}},
		"bar/dup": {"id":"bar/dup","limit":{"context":2}}
	}`)
	c, err := Parse(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if c.Len() != 4 {
		t.Fatalf("len=%d want 4", c.Len())
	}

	// Exact id.
	lim, ok := c.Lookup("openai/gpt-5.5")
	if !ok || lim.Context != 1050000 || lim.Input != 922000 || lim.Output != 128000 {
		t.Fatalf("gpt-5.5 lookup: %+v ok=%v", lim, ok)
	}
	if got := lim.EffectiveInputLimit(); got != 922000 {
		t.Fatalf("input limit=%d want 922000", got)
	}

	// Short name fallback (unambiguous).
	lim, ok = c.Lookup("claude-opus-4-5")
	if !ok || lim.Context != 200000 {
		t.Fatalf("short-name lookup: %+v ok=%v", lim, ok)
	}
	// No explicit input -> context window compatibility fallback.
	if got := lim.EffectiveOutputReserve(); got != 64000 {
		t.Fatalf("output reserve=%d want 64000", got)
	}
	if got := lim.EffectiveInputLimit(); got != 200000 {
		t.Fatalf("input fallback=%d want %d", got, 200000)
	}
	m, ok := c.LookupModel("openai/gpt-5.5")
	if !ok || m.InputTokenLimit != 922000 || m.Limits().EffectiveInputLimit() != 922000 {
		t.Fatalf("model input limit not preserved: %+v ok=%v", m, ok)
	}

	// Ambiguous short name "dup" must not resolve.
	if _, ok := c.Lookup("dup"); ok {
		t.Fatalf("ambiguous short name should not resolve")
	}

	// Unknown / nil safety.
	if _, ok := c.Lookup("nope"); ok {
		t.Fatalf("unknown should not resolve")
	}
	var nilCat *Catalog
	if _, ok := nilCat.Lookup("anything"); ok {
		t.Fatalf("nil catalog must return false")
	}
}

func TestEffectiveOutputReserveFallback(t *testing.T) {
	l := Limits{Context: 120000}
	if got := l.EffectiveOutputReserve(); got != 20000 {
		t.Fatalf("reserve=%d want 20000", got)
	}
}

func TestLookupPreservesInputLimit(t *testing.T) {
	data := []byte(`{"openai/gpt-5.6-sol":{"id":"openai/gpt-5.6-sol","limit":{"context":1050000,"input":922000,"output":128000}}}`)
	c, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	oldGlobal := globalCat
	globalCat = c
	t.Cleanup(func() { globalCat = oldGlobal })
	hit, ok := Lookup("openai", "gpt-5.6-sol")
	if !ok || hit.ContextWindow != 1050000 || hit.InputTokenLimit != 922000 || hit.DefaultMaxTokens != 128000 {
		t.Fatalf("hit=%+v ok=%v", hit, ok)
	}
	if got := hit.Limits().EffectiveInputLimit(); got != 922000 {
		t.Fatalf("effective input=%d want 922000", got)
	}
}

// The helpers below were production functions that only the tests in this
// package ever called. They live here so the production files carry no
// unused code while the tests keep exercising the live code underneath.

// Limits reconstructs the catalog Limits view for this model entry.
func (m Model) Limits() Limits {
	return Limits{Context: int(m.ContextWindow), Input: int(m.InputTokenLimit), Output: int(m.DefaultMaxTokens)}
}

// Len reports the number of models in the catalog.
func (c *Catalog) Len() int {
	if c == nil {
		return 0
	}
	return len(c.byID)
}

// A fresh home has no models.json until the catalog beside the binary is
// seeded into it. Initializing the global catalog seeds it first, so the very
// first process knows every model's limits instead of reading them all as
// unknown.
func TestInitGlobalCatalogSeedsAFreshHome(t *testing.T) {
	globalCatMu.RLock()
	previous := globalCat
	globalCatMu.RUnlock()
	t.Cleanup(func() { SetGlobalCatalogForTest(previous) })
	SetGlobalCatalogForTest(nil)

	assets, err := RepoAssetsDirForTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(AssetsDirEnv, assets)

	home := t.TempDir()
	if err := InitGlobalCatalog(home, "(devel)"); err != nil {
		t.Fatalf("InitGlobalCatalog: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "models.json")); err != nil {
		t.Fatalf("models.json was not seeded: %v", err)
	}
	hit, ok := Lookup("deepseek", "deepseek-chat")
	if !ok || hit.ContextWindow <= 0 {
		t.Fatalf("Lookup after a fresh init = %+v, %v", hit, ok)
	}
}

// catalogEntry mirrors the parts of a models.json entry the capability gate
// reads: whether it takes attachments, and which input modalities it names.
type catalogEntry struct {
	Attachment bool `json:"attachment"`
	Modalities struct {
		Input []string `json:"input"`
	} `json:"modalities"`
}

func readCatalogEntry(t *testing.T, id string) catalogEntry {
	t.Helper()
	data, err := RepoModelsJSONForTest()
	if err != nil {
		t.Fatalf("read repo models.json: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("parse models.json: %v", err)
	}
	body, ok := raw[id]
	if !ok {
		t.Fatalf("models.json has no entry %q", id)
	}
	var entry catalogEntry
	if err := json.Unmarshal(body, &entry); err != nil {
		t.Fatalf("parse entry %q: %v", id, err)
	}
	return entry
}

func hasModality(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// TestCatalogMarksTheOwnerDefaultAsMultimodal pins the catalog data fix owner
// confirmed on 2026-10-14: the DeepSeek endpoint routes deepseek-v4-flash to
// deepseek-flash, which accepts image input, so this entry must be multimodal.
// The product docs and the owner's default config both treat it as such, and
// the capability gate (pkg/run.ImageInputRefusal) reads exactly this field —
// a lagging table would make reading an image refuse on the owner's own model.
func TestCatalogMarksTheOwnerDefaultAsMultimodal(t *testing.T) {
	entry := readCatalogEntry(t, "deepseek/deepseek-v4-flash")
	if !entry.Attachment {
		t.Fatalf("deepseek/deepseek-v4-flash attachment=false; owner confirmed it accepts image input")
	}
	if !hasModality(entry.Modalities.Input, "image") {
		t.Fatalf("deepseek/deepseek-v4-flash modalities.input=%v; want it to include image", entry.Modalities.Input)
	}
}

// TestCatalogKeepsTextOnlyDeepseekModelsAsTextOnly pins the same-provider,
// same-family text-only entries. The deepseek-flash family holds both a
// multimodal member (deepseek-v4-flash) and a text-only one
// (deepseek-v4-flash-0731), so capability is per model and must never be
// inferred from a family — the gate relies on these staying false.
func TestCatalogKeepsTextOnlyDeepseekModelsAsTextOnly(t *testing.T) {
	for _, id := range []string{"deepseek/deepseek-v4-pro", "deepseek/deepseek-v4-flash-0731"} {
		entry := readCatalogEntry(t, id)
		if entry.Attachment {
			t.Fatalf("%s attachment=true; owner confirmed it is a text-only model", id)
		}
		if hasModality(entry.Modalities.Input, "image") {
			t.Fatalf("%s modalities.input=%v; a text-only model must not name image", id, entry.Modalities.Input)
		}
	}
}
