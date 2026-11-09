package llm

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
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
	// The merged catalog is what a caller sees: shipped data plus the home
	// copy the seed just wrote, which is the same bytes.
	shipped, err := RepoModelsJSONForTest()
	if err != nil {
		t.Fatal(err)
	}
	want, err := Parse(shipped)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(AllModels()); got != len(want.AllModels()) {
		t.Fatalf("merged catalog has %d models, want %d", got, len(want.AllModels()))
	}
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

func restoreGlobalCatalog(t *testing.T) {
	t.Helper()
	globalCatMu.RLock()
	previous := globalCat
	globalCatMu.RUnlock()
	t.Cleanup(func() { SetGlobalCatalogForTest(previous) })
}

// A home with no models.json gets a full copy of the shipped catalog — the file
// a user opens and edits — and nothing else: no marker, no backup, no
// second file of any kind.
func TestInstallWritesAFullCopyWhenMissing(t *testing.T) {
	assets := requireRepoAssets(t)
	shipped, err := os.ReadFile(filepath.Join(assets, "models.json"))
	if err != nil {
		t.Fatal(err)
	}

	home := t.TempDir()
	if err := Install(home, "(devel)"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(home, "models.json"))
	if err != nil {
		t.Fatalf("read seeded copy: %v", err)
	}
	if !bytes.Equal(got, shipped) {
		t.Fatalf("seeded copy is %d bytes, shipped is %d", len(got), len(shipped))
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "models.json" {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("home holds %v; a missing-copy install writes models.json and nothing else", names)
	}
}

// An existing home file is the user's: Install leaves its bytes and its mtime
// exactly as they were, however outdated or damaged they look.
func TestInstallLeavesAnExistingFileByteIdentical(t *testing.T) {
	requireRepoAssets(t)

	home := t.TempDir()
	target := filepath.Join(home, "models.json")
	trimmed := []byte(`{"openai/gpt-5":{"id":"openai/gpt-5","name":"my name","attachment":false,"limit":{"context":12345}}}`)
	if err := os.WriteFile(target, trimmed, 0o644); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(target, past, past); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}

	if err := Install(home, "(devel)"); err != nil {
		t.Fatalf("Install: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, trimmed) {
		t.Fatalf("Install rewrote the user's file: %s", got)
	}
	after, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("Install touched the user's file: mtime %v -> %v", before.ModTime(), after.ModTime())
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("Install added files beside the user's copy: %d entries", len(entries))
	}
}

// The home file is an overlay read field by field: a field it names wins, a
// field it does not name still comes from the shipped row.
func TestOverlayOverridesSingleField(t *testing.T) {
	requireRepoAssets(t)
	restoreGlobalCatalog(t)

	home := t.TempDir()
	overlay := []byte(`{"openai/gpt-5":{"attachment":true}}`)
	if err := os.WriteFile(filepath.Join(home, "models.json"), overlay, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := InitGlobalCatalog(home, "(devel)"); err != nil {
		t.Fatalf("InitGlobalCatalog: %v", err)
	}

	hit, ok := Lookup("openai", "gpt-5")
	if !ok {
		t.Fatal("openai/gpt-5 missing after the overlay merge")
	}
	if !hit.SupportsAttachments {
		t.Fatal("the overlay's attachment:true did not win")
	}
	if hit.ContextWindow != 400000 {
		t.Fatalf("context window = %d; an unnamed field must come from the shipped row", hit.ContextWindow)
	}
	if levels, known := hit.ReasoningEfforts(); !known || !reflect.DeepEqual(levels, []string{"minimal", "low", "medium", "high"}) {
		t.Fatalf("reasoning levels = %v, known=%v; want the shipped ones", levels, known)
	}
	// The shipped copy is untouched by any of this.
	shipped, err := RepoModelsJSONForTest()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(shipped, []byte(`"openai/gpt-5"`)) {
		t.Fatal("the shipped asset lost openai/gpt-5")
	}
}

// The overlay can add a model the shipped catalog has never carried and delete
// one it does, without disturbing anything else.
func TestOverlayAddsAModelAndDeletesOne(t *testing.T) {
	requireRepoAssets(t)
	restoreGlobalCatalog(t)

	home := t.TempDir()
	overlay := []byte(`{
		"local/private-model":{"id":"local/private-model","name":"Private","reasoning":true,
			"reasoning_options":[{"type":"effort","values":["low","high"]}],"limit":{"context":32768,"output":4096}},
		"openai/gpt-4":null
	}`)
	if err := os.WriteFile(filepath.Join(home, "models.json"), overlay, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := InitGlobalCatalog(home, "(devel)"); err != nil {
		t.Fatalf("InitGlobalCatalog: %v", err)
	}

	added, ok := Lookup("local", "private-model")
	if !ok || added.ContextWindow != 32768 {
		t.Fatalf("added model = %+v, %v", added, ok)
	}
	if levels, known := added.ReasoningEfforts(); !known || !reflect.DeepEqual(levels, []string{"low", "high"}) {
		t.Fatalf("added model levels = %v, known=%v", levels, known)
	}
	if _, ok := Lookup("openai", "gpt-4"); ok {
		t.Fatal("openai/gpt-4 must be deleted by its null overlay entry")
	}
	if hit, ok := Lookup("openai", "gpt-5"); !ok || hit.ContextWindow != 400000 {
		t.Fatalf("an unrelated model changed: %+v, %v", hit, ok)
	}
}

// The levels a surface offers are the merged data's levels: a user who narrows
// one model's efforts in their overlay sees exactly those, on every surface
// that asks (TUI /model and the gateway both read through ModelReasoningEfforts).
func TestOverlayAppliesToReasoningOptions(t *testing.T) {
	requireRepoAssets(t)
	restoreGlobalCatalog(t)

	home := t.TempDir()
	overlay := []byte(`{"zhipuai/glm-5.3-flash":{"reasoning_options":[{"type":"effort","values":["high"]}]}}`)
	if err := os.WriteFile(filepath.Join(home, "models.json"), overlay, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := InitGlobalCatalog(home, "(devel)"); err != nil {
		t.Fatalf("InitGlobalCatalog: %v", err)
	}
	levels, known := ReasoningEfforts("zhipuai", "glm-5.3-flash")
	if !known || !reflect.DeepEqual(levels, []string{"high"}) {
		t.Fatalf("levels = %v, known=%v; want [high] from the overlay", levels, known)
	}
}

// A home copy written before this build added `type` and `reasoning_options`
// still gains both — from the shipped row — while keeping every value the user
// already had. This is why an old copy never has to be rewritten or migrated.
func TestLegacyFullCopyGainsTheNewColumns(t *testing.T) {
	requireRepoAssets(t)
	restoreGlobalCatalog(t)

	shipped, err := RepoModelsJSONForTest()
	if err != nil {
		t.Fatal(err)
	}
	var entries map[string]map[string]json.RawMessage
	if err := json.Unmarshal(shipped, &entries); err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		delete(entry, "type")
		delete(entry, "reasoning_options")
	}
	// What a user changed in their own copy: a narrower window and no images.
	entries["openai/gpt-5"]["limit"] = json.RawMessage(`{"context":12345,"output":1000}`)
	entries["openai/gpt-5"]["attachment"] = json.RawMessage(`false`)
	legacy, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}

	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "models.json"), legacy, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := InitGlobalCatalog(home, "(devel)"); err != nil {
		t.Fatalf("InitGlobalCatalog: %v", err)
	}

	// The user's own values still win.
	gpt5, ok := Lookup("openai", "gpt-5")
	if !ok {
		t.Fatal("openai/gpt-5 missing")
	}
	if gpt5.ContextWindow != 12345 {
		t.Fatalf("context window = %d; the user's value must win", gpt5.ContextWindow)
	}
	if gpt5.SupportsAttachments {
		t.Fatal("the user's attachment:false must win")
	}
	// The columns their copy predates arrive from the shipped base.
	if levels, known := gpt5.ReasoningEfforts(); !known || !reflect.DeepEqual(levels, []string{"minimal", "low", "medium", "high"}) {
		t.Fatalf("levels = %v, known=%v; a legacy copy must gain the shipped reasoning_options", levels, known)
	}
	whisper, ok := Lookup("openai", "whisper-large-v3")
	if !ok {
		t.Fatal("openai/whisper-large-v3 missing")
	}
	if whisper.Kind != "transcription" {
		t.Fatalf("whisper kind = %q; a legacy copy must gain the shipped type", whisper.Kind)
	}
	// And the user's file is still theirs, byte for byte.
	got, err := os.ReadFile(filepath.Join(home, "models.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, legacy) {
		t.Fatal("loading a legacy copy rewrote it")
	}
}

// A broken overlay is the likeliest failure of the whole two-layer scheme —
// the file exists precisely so a user edits it — so its error has to name the
// file and the byte. Reporting it as "not found" would send them looking for a
// missing file they are looking at.
func TestResolveReportsABrokenOverlayPrecisely(t *testing.T) {
	requireRepoAssets(t)
	restoreGlobalCatalog(t)

	home := t.TempDir()
	overlayPath := filepath.Join(home, "models.json")
	broken := []byte(`{"zhipuai/glm-5.3-flash": {"reasoning_options": [}`)
	if err := os.WriteFile(overlayPath, broken, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Resolve(home)
	if err == nil {
		t.Fatal("Resolve accepted a hand-edited overlay that is not JSON")
	}
	if !strings.Contains(err.Error(), overlayPath) {
		t.Fatalf("error %q does not name the file the user edited (%s)", err, overlayPath)
	}
	if strings.Contains(err.Error(), "not found") {
		t.Fatalf("error %q reads as a missing file; the overlay is there and broken", err)
	}

	// The same through the startup path, which is where a user meets it.
	if err := InitGlobalCatalog(home, "(devel)"); err == nil {
		t.Fatal("InitGlobalCatalog accepted a broken overlay")
	} else if !strings.Contains(err.Error(), overlayPath) {
		t.Fatalf("InitGlobalCatalog error %q does not name the overlay", err)
	}

	// The genuinely absent case keeps its own message: one sentence a surface
	// can show, naming neither models.json nor this machine's layout (owner
	// 2026-11-07).
	empty := t.TempDir()
	t.Setenv(AssetsDirEnv, t.TempDir())
	if _, err := Resolve(empty); err == nil || !strings.Contains(err.Error(), "model data not found; reinstall forebrain") {
		t.Fatalf("Resolve with no catalog anywhere = %v, want the model-data sentence", err)
	} else if strings.Contains(err.Error(), "models.json") {
		t.Fatalf("error %q names models.json: a surface never shows that file", err)
	}
}

// A malformed entry (a key whose value is not an object) is a user's editing
// mistake too, and reaches the same precise error rather than a silent partial
// merge.
func TestResolveRejectsANonObjectOverlayEntry(t *testing.T) {
	requireRepoAssets(t)
	restoreGlobalCatalog(t)

	home := t.TempDir()
	overlayPath := filepath.Join(home, "models.json")
	if err := os.WriteFile(overlayPath, []byte(`{"openai/gpt-5": "gpt-5"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cat, err := Resolve(home)
	if err != nil {
		// Either answer is defensible for a non-object entry: a precise error
		// or a merge that lets the overlay win whole. What must not happen is
		// a silent loss of the shipped row without a word.
		if !strings.Contains(err.Error(), overlayPath) {
			t.Fatalf("error %q does not name the overlay", err)
		}
		return
	}
	if _, ok := cat.LookupModel("openai/gpt-5"); !ok {
		t.Fatal("the overlay's entry vanished from the merged catalog")
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
	SetModelOverrides(nil)
	t.Cleanup(func() { SetModelOverrides(nil) })
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
func TestModelOverrideReplacesCapabilities(t *testing.T) {
	installCatalogForOverrideTest(t, overrideTestCatalog)
	no := false
	context := int64(64000)
	output := int64(4096)
	SetModelOverrides(map[string]ModelOverride{
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
	SetModelOverrides(map[string]ModelOverride{
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
	SetModelOverrides(map[string]ModelOverride{"anthropic/claude-sonnet-5": {CanReason: &no, Efforts: []string{"low", "high"}}})
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
	SetModelOverrides(map[string]ModelOverride{"custom/my-model": {ContextWindow: &context}})
	if hit, ok := Lookup("custom", "my-model"); !ok || hit.ContextWindow != 8000 {
		t.Fatalf("a row-level override must resolve on its own: %+v ok=%v", hit, ok)
	}
}

// TestModelOverrideClearRestoresTheCatalog pins SetModelOverrides(nil): the
// process clears the layer when a configuration carries no capabilities.
func TestModelOverrideClearRestoresTheCatalog(t *testing.T) {
	installCatalogForOverrideTest(t, overrideTestCatalog)
	context := int64(1)
	SetModelOverrides(map[string]ModelOverride{"openai/gpt-4o": {ContextWindow: &context}})
	if hit, _ := Lookup("openai", "gpt-4o"); hit.ContextWindow != 1 {
		t.Fatalf("override not applied: %+v", hit)
	}
	SetModelOverrides(nil)
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
	SetModelOverrides(map[string]ModelOverride{"volcengine/doubao-seed-evolving": {MaxOutput: &output}})
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
