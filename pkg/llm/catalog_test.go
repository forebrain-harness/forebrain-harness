package llm

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

// A catalog entry carries the upstream model type and its reasoning_options,
// and an absent reasoning_options field is not the same data as an empty one:
// the first says nothing, the second says "reasons with nothing to pick".
func TestParseCarriesKindAndReasoningOptions(t *testing.T) {
	data := []byte(`{
		"openai/gpt-5": {"id":"openai/gpt-5","reasoning":true,"limit":{"context":400000},
			"reasoning_options":[{"type":"effort","values":["minimal","low"]},{"type":"toggle"}]},
		"openai/gpt-4": {"id":"openai/gpt-4","reasoning":false,"limit":{"context":8192}},
		"zhipuai/glm-4.5": {"id":"zhipuai/glm-4.5","reasoning":true,"reasoning_options":[{"type":"toggle"}]},
		"openai/whisper-large-v3": {"id":"openai/whisper-large-v3","type":"transcription"}
	}`)
	c, err := Parse(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	gpt5, ok := c.LookupModel("openai/gpt-5")
	if !ok {
		t.Fatal("openai/gpt-5 missing")
	}
	if !gpt5.Chat() {
		t.Fatalf("openai/gpt-5 Kind=%q, want a chat model", gpt5.Kind)
	}
	if !gpt5.ReasoningKnown {
		t.Fatal("openai/gpt-5 must report its reasoning_options as known")
	}
	if levels, _ := gpt5.ReasoningEfforts(); !reflect.DeepEqual(levels, []string{"minimal", "low"}) {
		t.Fatalf("openai/gpt-5 levels = %v, want [minimal low]", levels)
	}

	whisper, ok := c.LookupModel("openai/whisper-large-v3")
	if !ok {
		t.Fatal("openai/whisper-large-v3 missing")
	}
	if whisper.Chat() {
		t.Fatalf("whisper Kind=%q must not be a chat model", whisper.Kind)
	}
	if whisper.ReasoningKnown {
		t.Fatal("an entry with no reasoning_options must report known=false")
	}

	// Toggle only: known, and no level to pick.
	glm, _ := c.LookupModel("zhipuai/glm-4.5")
	if levels, known := glm.ReasoningEfforts(); !known || len(levels) != 0 {
		t.Fatalf("glm-4.5 = %v, known=%v; want known with no levels", levels, known)
	}

	// A level outside the closed set — an edit to the file, or an upstream
	// value this build cannot send — is dropped, not offered.
	edited := []byte(`{"a/b":{"id":"a/b","reasoning_options":[{"type":"effort","values":["low","ultra","high","low"]}]}}`)
	editedCat, err := Parse(edited)
	if err != nil {
		t.Fatalf("parse edited: %v", err)
	}
	m, _ := editedCat.LookupModel("a/b")
	if levels, _ := m.ReasoningEfforts(); !reflect.DeepEqual(levels, []string{"low", "high"}) {
		t.Fatalf("hand-edited levels = %v, want [low high]", levels)
	}
}

// Several effort descriptions on one model merge in file order and
// de-duplicate; the other description types contribute nothing.
func TestReasoningEffortsMergesEffortsInOrder(t *testing.T) {
	data := []byte(`{"alibaba/qwen3.8-max":{"id":"alibaba/qwen3.8-max","reasoning":true,"reasoning_options":[
		{"type":"toggle"},
		{"type":"effort","values":["low","medium"]},
		{"type":"budget_tokens","min":0,"max":262144},
		{"type":"effort","values":["xhigh","low"]}
	]}}`)
	c, err := Parse(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	m, _ := c.LookupModel("alibaba/qwen3.8-max")
	if levels, known := m.ReasoningEfforts(); !known || !reflect.DeepEqual(levels, []string{"low", "medium", "xhigh"}) {
		t.Fatalf("levels = %v, known=%v; want [low medium xhigh]", levels, known)
	}
}

// requireRepoAssets points the shipped-catalog lookup at the repository's
// assets/ directory and restores the environment afterwards.
func requireRepoAssets(t *testing.T) string {
	t.Helper()
	assets, err := RepoAssetsDirForTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(AssetsDirEnv, assets)
	return assets
}

// writeShippedCatalog writes a catalog beside a binary — the one place the
// runtime reads — and points the lookup at it.
func writeShippedCatalog(t *testing.T, content []byte) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "models.json"), content, 0o644); err != nil {
		t.Fatalf("write shipped models.json: %v", err)
	}
	t.Setenv(AssetsDirEnv, dir)
	return filepath.Join(dir, "models.json")
}

// The catalog is the one this build ships beside its binary. A file of the same
// name in the forebrain home is not a data source any more: it is not read,
// nothing merges over the shipped row, and the file is left byte for byte alone
// (owner 2026-11-09).
func TestCatalogReadsTheShippedFileOnly(t *testing.T) {
	writeShippedCatalog(t, []byte(`{"openai/gpt-5":{"id":"openai/gpt-5","attachment":false,"limit":{"context":400000}}}`))
	restoreGlobalCatalog(t)

	// The home copy exists before the catalog is loaded: initializing must
	// ignore it, and nothing may write it back.
	home := t.TempDir()
	homeCopy := []byte(`{"openai/gpt-5":{"attachment":true},"local/private-model":{"id":"local/private-model","limit":{"context":8192}}}`)
	homePath := filepath.Join(home, "models.json")
	if err := os.WriteFile(homePath, homeCopy, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := InitGlobalCatalog(); err != nil {
		t.Fatalf("InitGlobalCatalog: %v", err)
	}

	hit, ok := Lookup("openai", "gpt-5")
	if !ok {
		t.Fatal("openai/gpt-5 missing from the shipped catalog")
	}
	if hit.SupportsAttachments || hit.ContextWindow != 400000 {
		t.Fatalf("hit = %+v; the home copy must not reach the answer", hit)
	}
	if _, ok := Lookup("local", "private-model"); ok {
		t.Fatal("the home copy's model resolved; the home file must not be read")
	}
	got, err := os.ReadFile(homePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, homeCopy) {
		t.Fatal("the home copy was rewritten")
	}
}

// Initializing the catalog writes nothing into a home: a fresh home stays
// exactly as it was, and the answer still comes from beside the binary. The
// first process used to seed a full copy into the home; that semantic is gone
// (owner 2026-11-09).
func TestInitGlobalCatalogDoesNotWriteHome(t *testing.T) {
	writeShippedCatalog(t, []byte(`{"deepseek/deepseek-chat":{"id":"deepseek/deepseek-chat","limit":{"context":128000,"output":8192}}}`))
	restoreGlobalCatalog(t)
	home := t.TempDir()

	if err := InitGlobalCatalog(); err != nil {
		t.Fatalf("InitGlobalCatalog: %v", err)
	}
	hit, ok := Lookup("deepseek", "deepseek-chat")
	if !ok || hit.ContextWindow != 128000 {
		t.Fatalf("hit = %+v ok=%v", hit, ok)
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("the home gained %v; the catalog is not installed there any more", names)
	}
}

// A catalog that shipped broken reports its own path and byte; a build that
// ships no catalog at all reports the one sentence a surface may show. The
// second case used to be reached through a broken hand-edited overlay, so both
// halves stay pinned on the one file that is left.
func TestResolveReportsTheShippedCatalogPrecisely(t *testing.T) {
	restoreGlobalCatalog(t)
	path := writeShippedCatalog(t, []byte(`{"openai/gpt-5": [}`))

	_, err := Resolve()
	if err == nil {
		t.Fatal("Resolve accepted a shipped catalog that is not JSON")
	}
	if !strings.Contains(err.Error(), path) {
		t.Fatalf("error %q does not name the file that is broken (%s)", err, path)
	}
	if strings.Contains(err.Error(), "not found") {
		t.Fatalf("error %q reads as a missing file; the catalog is there and broken", err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	_, err = Resolve()
	if err == nil || !strings.Contains(err.Error(), "model data not found; reinstall forebrain") {
		t.Fatalf("Resolve with no catalog = %v, want the model-data sentence", err)
	} else if strings.Contains(err.Error(), "models.json") {
		t.Fatalf("error %q names models.json: a surface never shows that file", err)
	}
}

func restoreGlobalCatalog(t *testing.T) {
	t.Helper()
	globalCatMu.RLock()
	previous := globalCat
	globalCatMu.RUnlock()
	t.Cleanup(func() { SetGlobalCatalogForTest(previous) })
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

// installCatalogForOverrideTest points the global lookup at a small catalog and
// restores both the catalog and the override layer afterwards, so the override
// tests cannot leak into any other test in this package.
func installCatalogForOverrideTest(t *testing.T, raw string) {
	t.Helper()
	cat, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	restoreGlobalCatalog(t)
	SetGlobalCatalogForTest(cat)
	SetModelOverrides(nil, nil)
	t.Cleanup(func() { SetModelOverrides(nil, nil) })
}

const overrideTestCatalog = `{
	"openai/gpt-4o": {"id":"openai/gpt-4o","limit":{"context":128000,"input":128000,"output":16384},"attachment":true,"reasoning":true,
		"reasoning_options":[{"type":"effort","values":["low","medium","high"]}]},
	"anthropic/claude-sonnet-5": {"id":"anthropic/claude-sonnet-5","limit":{"context":200000,"output":64000},
		"reasoning_options":[{"type":"effort","values":["low","high"]}]}
}`

// TestModelOverrideAbsentLeavesTheCatalog pins the no-override case: the
// layers above the catalog must change nothing for a row that carries no
// capabilities.
func TestModelOverrideAbsentLeavesTheCatalog(t *testing.T) {
	installCatalogForOverrideTest(t, overrideTestCatalog)
	hit, ok := Lookup("openai", "gpt-4o")
	if !ok {
		t.Fatal("openai/gpt-4o did not resolve")
	}
	want := Hit{CatalogID: "openai/gpt-4o", ContextWindow: 128000, InputTokenLimit: 128000, DefaultMaxTokens: 16384,
		CanReason: true, SupportsAttachments: true, ReasoningKnown: true,
		ReasoningOptions: []ReasoningOption{{Type: "effort", Values: []string{"low", "medium", "high"}}}}
	if !reflect.DeepEqual(hit, want) {
		t.Fatalf("hit = %+v, want %+v", hit, want)
	}
	if levels, known := ReasoningEfforts("openai", "gpt-4o"); !known || !reflect.DeepEqual(levels, []string{"low", "medium", "high"}) {
		t.Fatalf("levels = %v known=%v", levels, known)
	}
}

// TestModelOverrideReplacesCapabilities pins the row-level override of the
// fields the model dialog asks about (images, context window, output limit).
// setOverridesByPair installs overrides the way a caller that knows only the
// pair does: each key is a pair and names its only row, which is the shape a
// one-row install has (two rows naming one pair need LookupRow, and the pair
// alone gets the catalog's values — owner 2026-11-10).
func setOverridesByPair(m map[string]ModelOverride) {
	pairOf := make(map[string]string, len(m))
	for key := range m {
		pairOf[key] = key
	}
	SetModelOverrides(m, pairOf)
}

func TestModelOverrideReplacesCapabilities(t *testing.T) {
	installCatalogForOverrideTest(t, overrideTestCatalog)
	no := false
	context := int64(64000)
	output := int64(4096)
	setOverridesByPair(map[string]ModelOverride{
		"openai/gpt-4o": {SupportsImages: &no, ContextWindow: &context, MaxOutput: &output},
	})
	hit, ok := Lookup("openai", "gpt-4o")
	if !ok {
		t.Fatal("openai/gpt-4o did not resolve")
	}
	if hit.SupportsAttachments {
		t.Fatal("SupportsImages=false did not clear the catalog's attachment support")
	}
	if hit.ContextWindow != 64000 || hit.DefaultMaxTokens != 4096 {
		t.Fatalf("window/output = %d/%d, want 64000/4096", hit.ContextWindow, hit.DefaultMaxTokens)
	}
	if !hit.CanReason || !reflect.DeepEqual(hit.ReasoningOptions, []ReasoningOption{{Type: "effort", Values: []string{"low", "medium", "high"}}}) {
		t.Fatalf("a row that names no levels must keep the catalog's: %+v", hit)
	}
}

// TestModelOverrideEffortsAndReasoningOff pins the levels a row names, with
// "none" first when the row allows turning reasoning off.
func TestModelOverrideEffortsAndReasoningOff(t *testing.T) {
	installCatalogForOverrideTest(t, overrideTestCatalog)
	off := true
	setOverridesByPair(map[string]ModelOverride{
		"anthropic/claude-sonnet-5": {Efforts: []string{"low", "high"}, ReasoningOff: &off},
	})
	if levels, known := ReasoningEfforts("anthropic", "claude-sonnet-5"); !known || !reflect.DeepEqual(levels, []string{"none", "low", "high"}) {
		t.Fatalf("levels = %v known=%v; want [none low high]", levels, known)
	}
}

// TestModelOverrideCanReasonFalseDropsLevels pins "this model does not
// reason": no levels at all, and that answer is known rather than missing.
func TestModelOverrideCanReasonFalseDropsLevels(t *testing.T) {
	installCatalogForOverrideTest(t, overrideTestCatalog)
	no := false
	setOverridesByPair(map[string]ModelOverride{"anthropic/claude-sonnet-5": {CanReason: &no, Efforts: []string{"low", "high"}}})
	hit, ok := Lookup("anthropic", "claude-sonnet-5")
	if !ok {
		t.Fatal("anthropic/claude-sonnet-5 did not resolve")
	}
	if hit.CanReason || hit.ReasoningOptions != nil || !hit.ReasoningKnown {
		t.Fatalf("hit = %+v, want no reasoning and no levels", hit)
	}
	if levels, known := ReasoningEfforts("anthropic", "claude-sonnet-5"); !known || len(levels) != 0 {
		t.Fatalf("levels = %v known=%v", levels, known)
	}
}

// TestModelOverrideCustomModelBorrowsCatalogEntry pins the custom-model rule: a
// name that is itself a catalog id borrows that entry's capabilities, a name
// with no slash does not, and a row's own overrides still apply to either.
func TestModelOverrideCustomModelBorrowsCatalogEntry(t *testing.T) {
	installCatalogForOverrideTest(t, overrideTestCatalog)
	hit, ok := Lookup("custom", "openai/gpt-4o")
	if !ok {
		t.Fatal("custom/openai/gpt-4o did not borrow the catalog entry")
	}
	if hit.ContextWindow != 128000 || !hit.SupportsAttachments {
		t.Fatalf("borrowed hit = %+v", hit)
	}
	if _, ok := Lookup("custom", "my-model"); ok {
		t.Fatal("a custom model with no slash and no override must not resolve")
	}
	if _, ok := Lookup("custom", "nobody/unknown-model"); ok {
		t.Fatal("a custom model the catalog does not know must not resolve")
	}
	// An override makes a model the catalog cannot name resolve anyway.
	context := int64(8000)
	setOverridesByPair(map[string]ModelOverride{"custom/my-model": {ContextWindow: &context}})
	if hit, ok := Lookup("custom", "my-model"); !ok || hit.ContextWindow != 8000 {
		t.Fatalf("a row-level override must resolve on its own: %+v ok=%v", hit, ok)
	}
}

// TestModelOverrideClearRestoresTheCatalog pins SetModelOverrides(nil, nil): the
// process clears the layer when a configuration carries no capabilities.
func TestModelOverrideClearRestoresTheCatalog(t *testing.T) {
	installCatalogForOverrideTest(t, overrideTestCatalog)
	context := int64(1)
	setOverridesByPair(map[string]ModelOverride{"openai/gpt-4o": {ContextWindow: &context}})
	if hit, _ := Lookup("openai", "gpt-4o"); hit.ContextWindow != 1 {
		t.Fatalf("override not applied: %+v", hit)
	}
	SetModelOverrides(nil, nil)
	if hit, ok := Lookup("openai", "gpt-4o"); !ok || hit.ContextWindow != 128000 {
		t.Fatalf("cleared override left %+v ok=%v", hit, ok)
	}
	if _, ok := Lookup("custom", "my-model"); ok {
		t.Fatal("a cleared layer must not keep resolving custom names")
	}
}

// TestBuiltinModelsFillProvidersTheCatalogMisses pins SetBuiltinModels: a
// provider models.json has no rows for (volcengine, byteplus) still answers
// with the plan table, and KnownProvider accepts it.
func TestBuiltinModelsFillProvidersTheCatalogMisses(t *testing.T) {
	installCatalogForOverrideTest(t, overrideTestCatalog)
	t.Cleanup(func() { SetBuiltinModels("volcengine", nil) })
	if KnownProvider("volcengine") {
		t.Fatal("volcengine is not in this test's catalog")
	}
	SetBuiltinModels("volcengine", []Model{
		{ID: "doubao-seed-evolving", ContextWindow: 1024000, DefaultMaxTokens: 256000, SupportsAttachments: true},
	})
	if !KnownProvider("volcengine") {
		t.Fatal("an installed built-in model table must make its provider known")
	}
	hit, ok := Lookup("volcengine", "doubao-seed-evolving")
	if !ok || hit.ContextWindow != 1024000 || hit.DefaultMaxTokens != 256000 {
		t.Fatalf("hit = %+v ok=%v", hit, ok)
	}
	// The catalog still outranks the table, and a row's overrides outrank
	// both.
	if hit, ok := Lookup("openai", "gpt-4o"); !ok || hit.ContextWindow != 128000 {
		t.Fatalf("catalog lookup changed: %+v ok=%v", hit, ok)
	}
	output := int64(1024)
	setOverridesByPair(map[string]ModelOverride{"volcengine/doubao-seed-evolving": {MaxOutput: &output}})
	if hit, _ := Lookup("volcengine", "doubao-seed-evolving"); hit.DefaultMaxTokens != 1024 {
		t.Fatalf("a row override must outrank the built-in table: %+v", hit)
	}
	SetBuiltinModels("volcengine", nil)
	if KnownProvider("volcengine") {
		t.Fatal("clearing the table must make the provider unknown again")
	}
}

// TestCustomProviderIDIsTheConfigOne pins the one constant llm restates because
// it sits below pkg/config; pkg/process's TestCustomProviderIDsAgree holds the
// other side of it.
func TestCustomProviderIDIsTheConfigOne(t *testing.T) {
	if customModelProvider != "custom" {
		t.Fatalf("customModelProvider = %q", customModelProvider)
	}
	if !KnownProvider("custom") {
		t.Fatal("the custom-model provider must always be known")
	}
}

// A bare model name that several ids share resolves to the first id in sorted
// order: the capability answer a surface shows must not depend on map iteration
// order (plan A0c). The suffix LIMITS table drops ambiguous names; LookupModel
// still has to answer one, because a custom model is typed this way.
func TestLookupCatalogSuffixPickIsDeterministic(t *testing.T) {
	installCatalogForOverrideTest(t, `{
		"zeta/dup": {"id":"zeta/dup","limit":{"context":3}},
		"mid/dup": {"id":"mid/dup","limit":{"context":2}},
		"alpha/dup": {"id":"alpha/dup","limit":{"context":1}}
	}`)
	// Go randomizes map iteration on every range, so a repeated lookup would see
	// a random pick if the code still returned the first iterated entry.
	for i := 0; i < 50; i++ {
		hit, ok := LookupCatalog("custom", "dup")
		if !ok {
			t.Fatal("custom/dup did not resolve through the suffix fallback")
		}
		if hit.CatalogID != "alpha/dup" || hit.ContextWindow != 1 {
			t.Fatalf("lookup %d = %+v; want the first id in sorted order (alpha/dup)", i, hit)
		}
	}
}

// Two configured rows may name one provider/model and carry different
// capabilities: the row id is the identity, so each row's own settings answer
// for it, and a caller that knows only the pair gets the catalog's values
// instead of one row's (owner 2026-11-10, plan 283 Part E).
func TestLookupRowKeepsSameNamedRowsApart(t *testing.T) {
	installCatalogForOverrideTest(t, overrideTestCatalog)
	t.Cleanup(func() { SetModelOverrides(nil, nil) })
	wide, narrow := int64(64000), int64(32000)
	SetModelOverrides(
		map[string]ModelOverride{
			"row-wide":   {ContextWindow: &wide},
			"row-narrow": {ContextWindow: &narrow},
		},
		map[string]string{
			"row-wide":   "openai/gpt-4o",
			"row-narrow": "openai/gpt-4o",
		},
	)

	hit, ok := LookupRow("row-wide", "openai", "gpt-4o")
	if !ok || hit.ContextWindow != 64000 {
		t.Fatalf("row-wide hit = %+v ok=%v, want its own window", hit, ok)
	}
	hit, ok = LookupRow("row-narrow", "openai", "gpt-4o")
	if !ok || hit.ContextWindow != 32000 {
		t.Fatalf("row-narrow hit = %+v ok=%v, want its own window", hit, ok)
	}
	// The pair alone has no single answer: the catalog's window stands.
	if hit, ok := Lookup("openai", "gpt-4o"); !ok || hit.ContextWindow != 128000 {
		t.Fatalf("pair hit = %+v ok=%v, want the catalog's window", hit, ok)
	}
	// A row the pair does not name has no overrides of its own to apply.
	if _, ok := LookupRow("row-unknown", "openai", "gpt-4o"); ok {
		hit, _ := LookupRow("row-unknown", "openai", "gpt-4o")
		if hit.ContextWindow != 128000 {
			t.Fatalf("unknown row = %+v, want the catalog's values", hit)
		}
	}
}

// One row naming the pair is still "the" row: a caller that knows only the pair
// keeps the behaviour it has today.
func TestLookupAppliesTheOnlyRowForItsPair(t *testing.T) {
	installCatalogForOverrideTest(t, overrideTestCatalog)
	t.Cleanup(func() { SetModelOverrides(nil, nil) })
	window := int64(64000)
	SetModelOverrides(
		map[string]ModelOverride{"row-only": {ContextWindow: &window}},
		map[string]string{"row-only": "openai/gpt-4o"},
	)
	if hit, ok := Lookup("openai", "gpt-4o"); !ok || hit.ContextWindow != 64000 {
		t.Fatalf("pair hit = %+v ok=%v, want the only row's window", hit, ok)
	}
	// A row that names no levels leaves the catalog's: the override lays over
	// the catalog, it does not replace it.
	if levels, known := ReasoningEffortsRow("row-only", "openai", "gpt-4o"); !known || !reflect.DeepEqual(levels, []string{"low", "medium", "high"}) {
		t.Fatalf("levels = %v known=%v, want the catalog's", levels, known)
	}
}
