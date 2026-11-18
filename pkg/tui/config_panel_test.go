package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

// configPanelWrite is one section write the panel made.
type configPanelWrite struct {
	section string
	raw     json.RawMessage
}

// configPanelTestSession is a session double for the panel: the snapshot it
// opens on, and every write it received.
type configPanelTestSession struct {
	*fakeSession
	snapshot configPanelSnapshot
	writeErr error
	writes   []configPanelWrite
}

func (s *configPanelTestSession) ConfigPanelSections(context.Context) (configPanelSnapshot, error) {
	if s.snapshot.Stored == nil {
		s.snapshot.Stored = map[string]json.RawMessage{}
	}
	if s.snapshot.File == nil {
		s.snapshot.File = map[string]string{}
	}
	return s.snapshot, nil
}

func (s *configPanelTestSession) UpdateConfigSection(_ context.Context, section string, raw json.RawMessage) error {
	if s.writeErr != nil {
		return s.writeErr
	}
	s.writes = append(s.writes, configPanelWrite{section: section, raw: append(json.RawMessage(nil), raw...)})
	return nil
}

// infoPanelSelector is a scripted selector that can also show the read-only
// overlay a forebrain.yaml section opens in.
type infoPanelSelector struct {
	*scriptedSelector
	titles []string
	bodies []string
}

func (s *infoPanelSelector) ShowInfo(label, body string) error {
	s.titles = append(s.titles, label)
	s.bodies = append(s.bodies, body)
	return nil
}

func (s *infoPanelSelector) ConfirmInfo(label, body, _ string) (bool, error) {
	return false, s.ShowInfo(label, body)
}

// newConfigPanelSession builds a panel double holding the stored sections given as
// section name → its JSON.
func newConfigPanelSession(stored map[string]string) *configPanelTestSession {
	session := &configPanelTestSession{
		fakeSession: &fakeSession{},
		snapshot:    configPanelSnapshot{Stored: map[string]json.RawMessage{}, File: map[string]string{}},
	}
	for name, raw := range stored {
		session.snapshot.Stored[name] = json.RawMessage(raw)
	}
	return session
}

// runConfigPanel drives the panel with the scripted answers in steps and
// inputs, and returns the renderer the frames landed on.
func runConfigPanel(t *testing.T, session *configPanelTestSession, selector Selector, steps []richStep, inputs []selectorInput, args ...string) *Renderer {
	t.Helper()
	scripted, ok := selector.(*scriptedSelector)
	if !ok {
		scripted = selector.(*infoPanelSelector).scriptedSelector
	}
	scripted.richSteps = steps
	scripted.inputs = inputs
	var out bytes.Buffer
	renderer := NewRenderer(&out, &out)
	ctrl := newCommandController(session, renderer, selector, nil, "")
	ctrl.handleConfig(context.Background(), args)
	return renderer
}

// frameText is everything the renderer retained as transcript frames: the
// panel's result and refusal lines land here, not on the raw writer.
func frameText(r *Renderer) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	vm := r.activeVMLocked()
	parts := make([]string, 0, len(vm.blocks))
	for _, block := range vm.blocks {
		parts = append(parts, block.frame.Title, block.frame.Content)
	}
	return strings.Join(parts, "\n")
}

// The panel opens on the section list, and that list is the whole stored
// configuration plus the two file sections — the one place a reader checks
// that a newly added section is reachable at all.
func TestConfigPanelListsEverySection(t *testing.T) {
	session := newConfigPanelSession(nil)
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, nil, nil)

	if len(selector.richItems) != 1 {
		t.Fatalf("expected the section list alone, got %d pages", len(selector.richItems))
	}
	got := map[string]bool{}
	tabs := map[string]bool{}
	for _, item := range selector.richItems[0] {
		got[item.Label] = true
		tabs[item.Category] = true
		if strings.TrimSpace(item.Description) == "" {
			t.Errorf("%s 的行没有说明", item.Label)
		}
	}
	for _, name := range process.RootConfigSections() {
		if !got[name] {
			t.Errorf("段 %q 不在面板的段列表里", name)
		}
	}
	for _, name := range []string{"gateway", "credentials"} {
		if !got[name] {
			t.Errorf("只读段 %q 不在面板的段列表里", name)
		}
	}
	for _, tab := range []string{configTabApproval, configTabSession, configTabAgents, configTabFile} {
		if !tabs[tab] {
			t.Errorf("缺少页签 %q", tab)
		}
	}
}

// With no sandbox the panel lists nothing about approvals: the whole
// "Approval & sandbox" tab is gone with its two sections, and naming one of them directly
// is answered like any other unknown section (owner 2026-10-31, plan 215 §3.4).
func TestConfigPanelHidesTheApprovalSectionsWithoutASandbox(t *testing.T) {
	restore := safety.SetSandboxAvailableForTest(false)
	defer restore()
	session := newConfigPanelSession(nil)
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, nil, nil)

	if len(selector.richItems) != 1 {
		t.Fatalf("expected the section list alone, got %d pages", len(selector.richItems))
	}
	for _, item := range selector.richItems[0] {
		if item.Category == configTabApproval {
			t.Errorf("段 %q 在没有沙箱时仍在列表里", item.Label)
		}
	}
	if _, ok := panelSectionSchemaFor("approval_preset"); ok {
		t.Fatal("approval_preset must not resolve without a sandbox")
	}
	if _, ok := panelSectionSchemaFor("sandbox_workspace_write"); ok {
		t.Fatal("sandbox_workspace_write must not resolve without a sandbox")
	}
	for _, name := range configSectionNames() {
		if name == "approval_preset" || name == "sandbox_workspace_write" {
			t.Fatalf("段 %q 仍在可用清单里", name)
		}
	}
}

// A tri-state boolean lands as true, and only a save writes anything: the
// panel says when a change takes effect instead of applying it.
func TestConfigPanelSavesATriStateBoolean(t *testing.T) {
	session := newConfigPanelSession(nil)
	selector := &scriptedSelector{}
	renderer := runConfigPanel(t, session, selector, []richStep{
		{label: "features", ok: true},
		{label: "Memory", ok: true},
		{label: configBoolOnLabel, ok: true},
		{label: configSaveRowLabel, ok: true},
		{ok: false},
	}, nil)

	if len(session.writes) != 1 {
		t.Fatalf("expected one write, got %d", len(session.writes))
	}
	write := session.writes[0]
	if write.section != "features" {
		t.Fatalf("section = %q, want features", write.section)
	}
	if string(write.raw) != `{"memories":true}` {
		t.Fatalf("raw = %s", write.raw)
	}
	if text := frameText(renderer); !strings.Contains(text, configSavedNotice) {
		t.Fatalf("the save must say when it takes effect; frames:\n%s", text)
	}
}

// Clearing a switch deletes the key rather than storing false, because nil is
// what selects the setting's default.
func TestConfigPanelClearsAFieldToItsDefault(t *testing.T) {
	session := newConfigPanelSession(map[string]string{"features": `{"memories":false}`})
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, []richStep{
		{label: "features", ok: true},
		{label: "Memory", ok: true},
		{label: configBoolUnsetLabel, ok: true},
		{label: configSaveRowLabel, ok: true},
		{ok: false},
	}, nil)

	if len(session.writes) != 1 {
		t.Fatalf("expected one write, got %d", len(session.writes))
	}
	// The section's last key is gone, so the whole section has nothing left:
	// an absent row is how "this install uses the default" reads.
	if raw := session.writes[0].raw; len(raw) != 0 {
		t.Fatalf("raw = %s, want the row removed", raw)
	}
}

// A number outside the loader's range never reaches the store, and the page
// says so in the field's own words before the next attempt.
func TestConfigPanelRefusesAnOutOfRangeNumber(t *testing.T) {
	session := newConfigPanelSession(nil)
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, []richStep{
		{label: "cron", ok: true},
		{label: "Keep days", ok: true},
		{label: "Keep days", ok: true},
		{label: configSaveRowLabel, ok: true},
		{ok: false},
	}, []selectorInput{{value: "99999", ok: true}, {value: "45", ok: true}})

	// The refusal is the page's own line, so it is in the prompt the panel
	// showed next — the user reads it before the value they retype.
	if refusals := strings.Join(selector.richLabels, "\n"); !strings.Contains(refusals, "may not be larger than 3650") {
		t.Fatalf("the refusal must be shown on the page; prompts:\n%s", refusals)
	}
	if len(session.writes) != 1 {
		t.Fatalf("expected the second attempt to be the only write, got %d", len(session.writes))
	}
	if raw := string(session.writes[0].raw); raw != `{"retention_days":45}` {
		t.Fatalf("raw = %s", raw)
	}
}

// A bound the loader simply honours lands on the bound, and the panel says
// which value it stored instead of pretending the typed one did.
func TestConfigPanelClampsANumberToItsCeiling(t *testing.T) {
	session := newConfigPanelSession(nil)
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, []richStep{
		{label: "memories", ok: true},
		{label: "Results per search", ok: true},
		{label: configSaveRowLabel, ok: true},
		{ok: false},
	}, []selectorInput{{value: "99", ok: true}})

	if prompts := strings.Join(selector.richLabels, "\n"); !strings.Contains(prompts, "Stored as the maximum, 20") {
		t.Fatalf("the clamp must be reported; prompts:\n%s", prompts)
	}
	if len(session.writes) != 1 || string(session.writes[0].raw) != `{"search_top_k":20}` {
		t.Fatalf("writes = %+v", session.writes)
	}
}

// A refused write keeps the panel open and repeats the validator's own words:
// a section the loader will not compose is explained, never swallowed.
func TestConfigPanelShowsTheValidatorsRefusal(t *testing.T) {
	session := newConfigPanelSession(nil)
	session.writeErr = errStubConfigRefusal
	selector := &scriptedSelector{}
	renderer := runConfigPanel(t, session, selector, []richStep{
		{label: "features", ok: true},
		{label: "Memory", ok: true},
		{label: configBoolOnLabel, ok: true},
		{label: configSaveRowLabel, ok: true},
		{ok: false},
		{ok: false},
	}, nil)

	if prompts := strings.Join(selector.richLabels, "\n"); !strings.Contains(prompts, "config section memories: invalid") {
		t.Fatalf("the refusal text must reach the page; prompts:\n%s", prompts)
	}
	if text := frameText(renderer); strings.Contains(text, configSavedNotice) {
		t.Fatalf("a refused save must not report success; frames:\n%s", text)
	}
	if len(session.writes) != 0 {
		t.Fatalf("a refused save must not reach the store again, got %+v", session.writes)
	}
}

// /config <section> opens that section's page directly.
func TestConfigPanelOpensASectionByName(t *testing.T) {
	session := newConfigPanelSession(nil)
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, []richStep{{ok: false}, {ok: false}}, nil, "cron")

	if len(selector.richLabels) == 0 {
		t.Fatal("no page opened")
	}
	if !strings.HasPrefix(selector.richLabels[0], "cron Scheduled tasks") {
		t.Fatalf("first page = %q, want the cron section page", selector.richLabels[0])
	}
}

// An unknown section name is answered with the ones that do exist, and no
// picker opens.
func TestConfigPanelRejectsAnUnknownSectionName(t *testing.T) {
	session := newConfigPanelSession(nil)
	selector := &scriptedSelector{}
	renderer := runConfigPanel(t, session, selector, nil, nil, "nope")

	if len(selector.richItems) != 0 {
		t.Fatalf("no picker may open for an unknown section, got %d", len(selector.richItems))
	}
	text := frameText(renderer)
	if !strings.Contains(text, "is not a known configuration section") || !strings.Contains(text, "approval_preset") {
		t.Fatalf("frames:\n%s", text)
	}
}

// A forebrain.yaml section is shown, never edited, and says where it is
// edited and when it takes effect.
func TestConfigPanelShowsAFileSectionReadOnly(t *testing.T) {
	session := newConfigPanelSession(nil)
	session.snapshot.File["gateway"] = "gateway:\n  http_addr: 127.0.0.1:8080\n"
	selector := &infoPanelSelector{scriptedSelector: &scriptedSelector{}}
	runConfigPanel(t, session, selector, []richStep{{label: "gateway", ok: true}, {ok: false}}, nil)

	if len(selector.bodies) != 1 {
		t.Fatalf("expected one read-only overlay, got %d", len(selector.bodies))
	}
	body := selector.bodies[0]
	if !strings.Contains(body, "http_addr: 127.0.0.1:8080") || !strings.Contains(body, configFileOnlyNote) {
		t.Fatalf("body = %q", body)
	}
	if len(session.writes) != 0 {
		t.Fatalf("a file section must never be written, got %+v", session.writes)
	}
}

// A table is edited one row at a time, and a row holding a table opens its own
// rows: agent definitions, MCP servers and LSP servers all take this one path.
func TestConfigPanelEditsATableRowInPlace(t *testing.T) {
	session := newConfigPanelSession(map[string]string{"agents": `{"definitions":{"work":{"description":"旧"}}}`})
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, []richStep{
		{label: "agents", ok: true},
		{label: "Agent definitions", ok: true},
		{label: "work", ok: true},
		{label: configRowOpenLabel, ok: true},
		{label: "description", ok: true},
		{label: configRowEditLabel, ok: true},
		{label: configNewTextRow, ok: true},
		{label: configUpRowLabel, ok: true},
		{label: configUpRowLabel, ok: true},
		{label: configSaveRowLabel, ok: true},
		{ok: false},
	}, []selectorInput{{value: "新", ok: true}})

	if len(session.writes) != 1 {
		t.Fatalf("expected one write, got %d", len(session.writes))
	}
	if raw := string(session.writes[0].raw); raw != `{"definitions":{"work":{"description":"新"}}}` {
		t.Fatalf("raw = %s", raw)
	}
}

// Rows can be added and deleted, and a list's rows are its elements — the
// shape an unset list section has no value to read the shape from.
func TestConfigPanelAddsAndDeletesARow(t *testing.T) {
	session := newConfigPanelSession(map[string]string{"sandbox_workspace_write": `{"writable_roots":["/a"]}`})
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, []richStep{
		{label: "sandbox_workspace_write", ok: true},
		{label: "Extra writable roots", ok: true},
		{label: configAddRowLabel, ok: true},
		{label: configNewTextRow, ok: true},
		{label: "#1", ok: true},
		{label: configRowDropLabel, ok: true},
		{label: configUpRowLabel, ok: true},
		{label: configSaveRowLabel, ok: true},
		{ok: false},
	}, []selectorInput{{value: "/b", ok: true}})

	if len(session.writes) != 1 {
		t.Fatalf("expected one write, got %d", len(session.writes))
	}
	if raw := string(session.writes[0].raw); raw != `{"writable_roots":["/b"]}` {
		t.Fatalf("raw = %s", raw)
	}
}

// An unset list still grows a list: the descriptor carries the shape, because
// there is no stored value to read it from.
func TestConfigPanelAddsToAnUnsetList(t *testing.T) {
	session := newConfigPanelSession(nil)
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, []richStep{
		{label: "sandbox_workspace_write", ok: true},
		{label: "Extra writable roots", ok: true},
		{label: configAddRowLabel, ok: true},
		{label: configNewTextRow, ok: true},
		{label: configUpRowLabel, ok: true},
		{label: configSaveRowLabel, ok: true},
		{ok: false},
	}, []selectorInput{{value: "/tmp2", ok: true}})

	if len(session.writes) != 1 {
		t.Fatalf("expected one write, got %d", len(session.writes))
	}
	if raw := string(session.writes[0].raw); raw != `{"writable_roots":["/tmp2"]}` {
		t.Fatalf("raw = %s", raw)
	}
}

// Esc backs out one level at a time: from a row page to its parent, from a
// section to the list, and from the list out of the panel.
func TestConfigPanelEscBacksOutOneLevel(t *testing.T) {
	session := newConfigPanelSession(map[string]string{"agents": `{"definitions":{"work":{}}}`})
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, []richStep{
		{label: "agents", ok: true},
		{label: "Agent definitions", ok: true},
		{ok: false}, // the row page closes
		{ok: false}, // the section page closes
	}, nil)

	// list → field page → row page → field page (the row page's Esc) → list
	// (the field page's Esc), and the list then closes on its own Esc.
	if len(selector.richItems) != 5 {
		t.Fatalf("expected 5 pages, got %d", len(selector.richItems))
	}
	if len(session.writes) != 0 {
		t.Fatalf("backing out must not write, got %+v", session.writes)
	}
}

// errStubConfigRefusal stands in for state.InvalidSectionError's message, so
// the panel's own words can be told apart from the store's.
var errStubConfigRefusal = stubRefusalError{}

type stubRefusalError struct{}

func (stubRefusalError) Error() string { return "config section memories: invalid approval_policy" }

// The panel's write path end to end: a save lands as the section's fb_config
// row, forebrain.yaml is not touched, the panel reads the value back on the
// next open, and a value the loader would not accept is refused with the
// store's own message.
func TestConfigPanelWritesTheSectionAndLeavesTheFileAlone(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	cfgPath := filepath.Join(home, "forebrain.yaml")
	seed := "gateway:\n  http_addr: 127.0.0.1:9\n"
	if err := os.WriteFile(cfgPath, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state", "forebrain.state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	process.InstallConfigSectionValidator(cfgPath, db)
	t.Cleanup(func() { state.SetSectionValidator(nil) })

	env := &process.Environment{Root: home, SQL: db}
	env.ConfigPath = cfgPath
	session := &ChatSession{Env: env}

	if err := session.UpdateConfigSection(ctx, "memories", json.RawMessage(`{"max_unused_days":30}`)); err != nil {
		t.Fatalf("a loadable section must be storeable: %v", err)
	}
	snapshot, err := session.ConfigPanelSections(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(snapshot.Stored["memories"]); got != `{"max_unused_days":30}` {
		t.Fatalf("stored memories = %s", got)
	}
	if body, err := os.ReadFile(cfgPath); err != nil || string(body) != seed {
		t.Fatalf("forebrain.yaml changed: %q (err %v)", string(body), err)
	}

	// The validator is the store's, so a value the loader rejects never lands
	// and never replaces what was there.
	refused := session.UpdateConfigSection(ctx, "memories", json.RawMessage(`{"search_top_k":"many"}`))
	if refused == nil {
		t.Fatal("an unloadable section must be refused")
	}
	if !strings.Contains(refused.Error(), "memories") {
		t.Fatalf("refusal = %v, want the store's message", refused)
	}
	after, err := session.ConfigPanelSections(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(after.Stored["memories"]); got != `{"max_unused_days":30}` {
		t.Fatalf("a refused write changed the row: %s", got)
	}
}

// The descriptor table's own guard rails (plan 190 §3.1): every Root section
// has a descriptor, every descriptor's paths name real settings, and every
// field carries the data its widget needs.

// rootSectionTypes maps each Root section's yaml name to its Go type: the one
// description of the configuration the panel must keep up with.
func rootSectionTypes(t *testing.T) map[string]reflect.Type {
	t.Helper()
	types := make(map[string]reflect.Type)
	rt := reflect.TypeOf(appcfg.Root{})
	for i := 0; i < rt.NumField(); i++ {
		name := strings.Split(rt.Field(i).Tag.Get("yaml"), ",")[0]
		if name == "" || name == "-" {
			continue
		}
		types[name] = rt.Field(i).Type
	}
	return types
}

// jsonFieldNames lists a struct's top-level JSON names — the keys its section
// value holds in fb_config.
func jsonFieldNames(typ reflect.Type) []string {
	names := make([]string, 0, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		tag := strings.Split(field.Tag.Get("json"), ",")[0]
		switch tag {
		case "-":
			continue
		case "":
			names = append(names, field.Name)
		default:
			names = append(names, tag)
		}
	}
	return names
}

// Every Root section has a descriptor, every descriptor names a Root section,
// and the two kinds line up with the store's own section table: the 11 rows
// fb_config holds are editable, gateway and credentials are not.
//
// This is the panel's one hard constraint on new work: add a section to Root
// without adding a descriptor and the panel silently loses it, so the loss
// fails here instead.
func TestConfigPanelSchemaCoversEveryRootSection(t *testing.T) {
	stored := make(map[string]bool)
	for _, name := range process.RootConfigSections() {
		stored[name] = true
	}
	for _, problem := range configSchemaCoverageProblems(rootSectionTypes(t), configSectionSchemas, stored) {
		t.Error(problem)
	}
}

// configSchemaCoverageProblems is the guard above as a function, so its new
// projection rules can be proved against a table that is wrong on purpose
// (plan 314). Every Root section needs a descriptor; every descriptor names a
// Root section or projects one that is stored; and the read-only/editable split
// lines up with the store's own section list.
func configSchemaCoverageProblems(types map[string]reflect.Type, schemas []configSectionSchema, stored map[string]bool) []string {
	var problems []string
	byName := make(map[string]configSectionSchema, len(schemas))
	for _, schema := range schemas {
		if _, dup := byName[schema.Name]; dup {
			problems = append(problems, "段 "+schema.Name+" 有两条描述符")
		}
		byName[schema.Name] = schema
	}
	for name := range types {
		if _, ok := byName[name]; !ok {
			problems = append(problems, "Root 的段 "+name+" 没有描述符，/config 面板里看不到它")
		}
	}
	for name, schema := range byName {
		if _, ok := types[name]; ok {
			continue
		}
		// A projection presents another section's document: its own name is
		// not a Root section, and the section it presents has to be one.
		if schema.StoreSection == "" {
			problems = append(problems, "描述符 "+name+" 不是 Root 的段")
			continue
		}
		if _, ok := types[schema.StoreSection]; !ok {
			problems = append(problems, "投影段 "+name+" 的 StoreSection "+schema.StoreSection+" 不是 Root 的段")
		}
	}
	for name, schema := range byName {
		// The section whose row the descriptor reads and writes: itself, or the
		// one a projection presents.
		store := schema.storeSection()
		if schema.YAMLOnly {
			if stored[store] {
				problems = append(problems, name+" 标了只读，却在 fb_config 的段表里")
			}
			if schema.Tab != configTabFile {
				problems = append(problems, "只读段 "+name+" 必须归到 "+configTabFile+" 页签")
			}
			continue
		}
		if !stored[store] {
			problems = append(problems, name+" 可编辑，但不在 process.RootConfigSections() 里")
		}
	}
	return problems
}

// The projection rules are guards, not conveniences (plan 314): a projection
// that presents a section nothing stores would silently save nowhere, and one
// that presents no section at all is a section the panel cannot read.
func TestConfigPanelSchemaRejectsUnstoredProjections(t *testing.T) {
	types := rootSectionTypes(t)
	stored := map[string]bool{"agents": true}

	unstored := append(append([]configSectionSchema{}, configSectionSchemas...),
		configSectionSchema{Name: "ghost_models", Title: "Ghost", Summary: "s", Tab: configTabAgents, StoreSection: "ghost"})
	if problems := configSchemaCoverageProblems(types, unstored, stored); !containsProblem(problems, "投影段 ghost_models 的 StoreSection ghost 不是 Root 的段") {
		t.Fatalf("a projection of a section nothing stores passed the guard: %v", problems)
	}

	// A descriptor that names neither a Root section nor one to project is the
	// pre-plan-314 failure, and it still fails.
	orphan := append(append([]configSectionSchema{}, configSectionSchemas...),
		configSectionSchema{Name: "ghost_models", Title: "Ghost", Summary: "s", Tab: configTabAgents})
	if problems := configSchemaCoverageProblems(types, orphan, stored); !containsProblem(problems, "描述符 ghost_models 不是 Root 的段") {
		t.Fatalf("a descriptor that is no section at all passed the guard: %v", problems)
	}

	// And a projection whose section exists but is not stored fails the
	// editable/stored half of the rule.
	notStored := configSectionSchema{Name: "agents", Title: "a", Summary: "s", Tab: configTabAgents, StoreSection: "definitions"}
	if problems := configSchemaCoverageProblems(types, []configSectionSchema{notStored}, map[string]bool{}); !containsProblem(problems, "agents 可编辑，但不在 process.RootConfigSections() 里") {
		t.Fatalf("a projection of a section the store does not hold passed the guard: %v", problems)
	}
}

// containsProblem reports whether the guard's answer carries one exact problem,
// so a test asserts on the rule rather than on the slice's shape.
func containsProblem(problems []string, want string) bool {
	for _, problem := range problems {
		if problem == want {
			return true
		}
	}
	return false
}

// A descriptor's field paths are the section's own JSON keys, in both
// directions: no path names a setting that does not exist, and no top-level
// setting of a section is left without a way to reach it.
func TestConfigPanelSchemaFieldsMatchTheirSection(t *testing.T) {
	types := rootSectionTypes(t)
	// A projection's fields are paths into the section it presents, so they are
	// counted as reaching that section's settings (plan 314): moving a setting
	// from one page to the other keeps it reachable, and neither page may leave
	// one unreachable.
	projected := make(map[string][]configField)
	for _, schema := range configSectionSchemas {
		if schema.StoreSection != "" {
			projected[schema.StoreSection] = append(projected[schema.StoreSection], schema.Fields...)
		}
	}
	for _, schema := range configSectionSchemas {
		typ, ok := types[schema.Name]
		if !ok {
			continue // a projection: checked by its own test and the coverage guard
		}
		if schema.YAMLOnly {
			if len(schema.Fields) != 0 {
				t.Errorf("只读段 %q 不应有编辑字段", schema.Name)
			}
			continue
		}
		if len(schema.Fields) == 0 {
			t.Errorf("段 %q 没有任何可编辑字段", schema.Name)
		}
		whole := false
		for _, field := range schema.Fields {
			if field.Path == "" {
				whole = true
			}
		}
		if typ.Kind() != reflect.Struct {
			// A section that is a bare string or a map is edited as its own
			// value, so a path inside it would point at nothing.
			for _, field := range schema.Fields {
				if field.Path != "" {
					t.Errorf("段 %q 的字段 %q 不是对象段，路径必须为空", schema.Name, field.Label)
				}
			}
			continue
		}
		names := make(map[string]bool)
		for _, name := range jsonFieldNames(typ) {
			names[name] = true
		}
		covered := make(map[string]bool)
		fields := append(append([]configField{}, schema.Fields...), projected[schema.Name]...)
		for _, field := range fields {
			if field.Path == "" {
				continue
			}
			head := strings.Split(field.Path, ".")[0]
			if !names[head] {
				t.Errorf("段 %q 的字段 %q 指向不存在的设置 %q", schema.Name, field.Label, head)
				continue
			}
			covered[head] = true
		}
		if whole {
			// A descriptor on the section value itself edits the whole
			// object, which covers every key it may hold.
			continue
		}
		for name := range names {
			if !covered[name] {
				t.Errorf("段 %q 的设置 %q 没有描述符，/config 面板里改不到它", schema.Name, name)
			}
		}
	}
}

// Each field carries the data its widget needs: an enum its option list, a
// number its bounds, and a label no sibling repeats.
func TestConfigPanelSchemaFieldDataIsComplete(t *testing.T) {
	for _, schema := range configSectionSchemas {
		if strings.TrimSpace(schema.Title) == "" || strings.TrimSpace(schema.Summary) == "" || strings.TrimSpace(schema.Tab) == "" {
			t.Errorf("段 %q 缺标题、说明或页签", schema.Name)
		}
		labels := make(map[string]bool, len(schema.Fields))
		for _, field := range schema.Fields {
			if strings.TrimSpace(field.Label) == "" {
				t.Errorf("段 %q 有字段没有标签", schema.Name)
			}
			if labels[field.Label] {
				t.Errorf("段 %q 的字段标签 %q 重复，用户无法分辨", schema.Name, field.Label)
			}
			labels[field.Label] = true
			switch field.Kind {
			case configFieldEnum:
				if len(field.Options) < 2 {
					t.Errorf("%s.%s 是枚举，至少要有两个选项", schema.Name, field.Label)
				}
				seen := make(map[string]bool, len(field.Options))
				for _, option := range field.Options {
					if strings.TrimSpace(option) == "" || seen[option] {
						t.Errorf("%s.%s 的选项 %q 为空或重复", schema.Name, field.Label, option)
					}
					seen[option] = true
				}
			case configFieldNumber:
				if field.Max > 0 && field.Min > field.Max {
					t.Errorf("%s.%s 的上下界反了：%d > %d", schema.Name, field.Label, field.Min, field.Max)
				}
			default:
				if len(field.Options) != 0 {
					t.Errorf("%s.%s 不是枚举，却带着选项", schema.Name, field.Label)
				}
			}
			if field.Clamp && field.Kind != configFieldNumber {
				t.Errorf("%s.%s 不是数字，不能声明 clamp", schema.Name, field.Label)
			}
			if field.List && field.Kind != configFieldRows {
				t.Errorf("%s.%s 不是行列表，不能声明 list", schema.Name, field.Label)
			}
		}
	}
}

// configPanelNonEnglishRune answers the first CJK or fullwidth rune in text,
// and -1 when there is none. The ranges are what an East Asian input produces:
// CJK punctuation (U+3000–U+303F), unified ideographs (U+4E00–U+9FFF) and the
// fullwidth forms (U+FF00–U+FFEF) — the three the owner named when the panel
// became English-only.
func configPanelNonEnglishRune(text string) (int, rune) {
	for i, r := range text {
		switch {
		case r >= 0x3000 && r <= 0x303F, r >= 0x4E00 && r <= 0x9FFF, r >= 0xFF00 && r <= 0xFFEF:
			return i, r
		}
	}
	return -1, 0
}

// TestConfigPanelSaysEverythingInEnglish is the panel's language gate (owner
// 2026-11-12: 「TUI /config面板必须全英文」). Everything the panel puts on the
// screen is English, and this is what keeps it that way: the descriptor table
// structurally, then every string literal in the panel's own two source files,
// so a new constant cannot arrive in Chinese just by not being listed here.
//
// It does not scan a rendered page, and that is deliberate: a rendered page
// carries the user's own data — agent ids, subagent descriptions, model names,
// and whatever a section's values happen to hold — and those may legitimately
// be Chinese. The rule is about the panel's own vocabulary, not about the
// configuration it displays. For the same reason it reads literals rather than
// file text: comments in these files may stay Chinese, and are not the UI.
func TestConfigPanelSaysEverythingInEnglish(t *testing.T) {
	check := func(what, text string) {
		t.Helper()
		if _, r := configPanelNonEnglishRune(text); r != 0 {
			t.Errorf("%s carries %q (U+%04X) — the /config panel is English-only: %q", what, r, r, text)
		}
	}

	for _, schema := range configSectionSchemas {
		check("section "+schema.Name+" title", schema.Title)
		check("section "+schema.Name+" summary", schema.Summary)
		check("section "+schema.Name+" tab", schema.Tab)
		for _, field := range schema.Fields {
			check(schema.Name+" field label "+field.Label, field.Label)
			check(schema.Name+" field hint of "+field.Label, field.Hint)
			// The keys are stored values and never shown; only the labels are.
			for stored, label := range field.OptionLabels {
				check(schema.Name+" option label "+stored, label)
			}
		}
	}

	// The chrome around the table: the fixed rows, the notices and the
	// three-state words. Named so a failure points at the constant rather than
	// at a line number that moves.
	chrome := map[string]string{
		"configSaveRowLabel":   configSaveRowLabel,
		"configBackRowLabel":   configBackRowLabel,
		"configAddRowLabel":    configAddRowLabel,
		"configUpRowLabel":     configUpRowLabel,
		"configRowEditLabel":   configRowEditLabel,
		"configRowOpenLabel":   configRowOpenLabel,
		"configRowDropLabel":   configRowDropLabel,
		"configRowBackLabel":   configRowBackLabel,
		"configSavedNotice":    configSavedNotice,
		"configFileOnlyNote":   configFileOnlyNote,
		"configBoolOnLabel":    configBoolOnLabel,
		"configBoolOffLabel":   configBoolOffLabel,
		"configBoolUnsetLabel": configBoolUnsetLabel,
		"configUnsetLabel":     configUnsetLabel,
		"configNewTextRow":     configNewTextRow,
		"configNewNumberRow":   configNewNumberRow,
		"configNewTrueRow":     configNewTrueRow,
		"configNewFalseRow":    configNewFalseRow,
		"configNewObjectRow":   configNewObjectRow,
	}
	for name, text := range chrome {
		check(name, text)
	}

	// The sweep: no string literal in the panel's own files may carry one of
	// those runes. This is the half that cannot rot — a constant added later is
	// covered without being named above.
	set := token.NewFileSet()
	for _, path := range []string{"config_panel.go", "config_panel_schema.go"} {
		file, err := parser.ParseFile(set, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			lit, ok := node.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			if _, r := configPanelNonEnglishRune(value); r != 0 {
				t.Errorf("%s:%d carries %q (U+%04X) in a string literal — the /config panel is English-only: %q",
					path, set.Position(lit.Pos()).Line, r, r, value)
			}
			return true
		})
	}
}

// rowLabels is one page's labels, for the "is this setting reachable" checks.
func rowLabels(items []SelectItem) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.Label)
	}
	return out
}

// findRow finds one page's row by label.
func findRow(t *testing.T, items []SelectItem, label string) SelectItem {
	t.Helper()
	for _, item := range items {
		if item.Label == label {
			return item
		}
	}
	t.Fatalf("no row labelled %q in %+v", label, rowLabels(items))
	return SelectItem{}
}

// pageLabels joins every page's heading the panel showed, so a refusal or a
// notice can be read where the user read it.
func pageLabels(selector *scriptedSelector) string {
	return strings.Join(selector.richLabels, "\n")
}

// The agents section reaches the plan reviewer's two global settings beside the
// goal check's, and the definitions page marks a primary agent the way the
// prototype does — a status at the row's right edge, with the description left
// as the value it holds (plan 313).
func TestConfigPanelAgentsListsThePlanReviewerAndMarksPrimary(t *testing.T) {
	session := newConfigPanelSession(map[string]string{"agents": `{"definitions":{"main":{"primary":true,"subagents":[]}}}`})
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, []richStep{
		{label: "Agent definitions", ok: true},
		{ok: false},
		{ok: false},
	}, nil, "agents")

	if len(selector.richItems) < 2 {
		t.Fatalf("expected the section page and the definitions page, got %d", len(selector.richItems))
	}
	labels := rowLabels(selector.richItems[0])
	if !slices.Contains(labels, "Goal check model") {
		t.Fatalf("the agents page is missing %q: %v", "Goal check model", labels)
	}
	// The plan reviewer's row moved to the "Subagent models" page, which is a
	// projection of this same document (plan 314): one place owns every
	// install-wide subagent row, and an agent page keeps only its own defaults.
	for _, gone := range []string{"Plan reviewer model", "Plan reviewer thinking level"} {
		if slices.Contains(labels, gone) {
			t.Fatalf("the agents page still lists %q: %v", gone, labels)
		}
	}

	main := findRow(t, selector.richItems[1], "main")
	if main.Status != configPrimaryStatus {
		t.Fatalf("main's status = %q, want %q", main.Status, configPrimaryStatus)
	}
	if main.Description != "› 2 items" {
		t.Fatalf("main's description = %q, want the value alone", main.Description)
	}
}

// One agent's subagents read the way dispatch resolves them (plan 313): this
// agent's own entry first, then the install-wide row of that type, then the
// conversation's model — and an entry naming a system-dispatched type says so
// and refuses to open.
func TestConfigPanelSubagentRowsNameTheirModelSource(t *testing.T) {
	session := newConfigPanelSession(map[string]string{"agents": `{
		"definitions":{"main":{"primary":true,"subagents":[
			{"name":"explore","model":"openai/gpt-sub"},
			{"name":"plan"},
			{"name":"verification"},
			{"name":"db-migrator","description":"d","system_prompt":"p"},
			{"name":"goal-evaluator"}
		]}},
		"subagent_models":{"plan":{"model":"llm-1","reasoning_effort":"xhigh"}}
	}`})
	session.snapshot.Models = []appcfg.LLMModelConfig{{ID: "llm-1", Provider: "openai", Model: "gpt-main"}}
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, []richStep{
		{label: "Agent definitions", ok: true},
		{label: "main", ok: true},
		{label: configRowOpenLabel, ok: true},
		{label: "subagents", ok: true},
		{label: configRowOpenLabel, ok: true},
		// The system entry refuses to open, and the page says why.
		{label: "#5", ok: true},
		{ok: false},
		{ok: false},
		{ok: false},
		{ok: false},
		{ok: false},
	}, nil, "agents")

	if len(selector.richItems) < 6 {
		t.Fatalf("expected the subagent rows page, got %d pages", len(selector.richItems))
	}
	rows := selector.richItems[5]
	cases := map[string]string{
		"#1": "agent override: openai/gpt-sub",
		"#2": "global default: openai/gpt-main / xHigh",
		"#3": configFollowConversationModel,
		"#4": "› 3 items",
		"#5": configSystemSubagentNotice,
	}
	for label, want := range cases {
		if row := findRow(t, rows, label); row.Description != want {
			t.Errorf("%s = %q, want %q", label, row.Description, want)
		}
	}
	if !strings.Contains(pageLabels(selector), configSystemSubagentNotice) {
		t.Fatalf("the refusal must reach the page: %s", pageLabels(selector))
	}
	if len(session.writes) != 0 {
		t.Fatalf("nothing here writes, got %+v", session.writes)
	}
}

// Creating a primary agent is a guided flow, not a type menu (plan 313): an id,
// an optional display name, the prototype's seed, and the new agent's own page
// opened with the guidance that says what to do next.
func TestConfigPanelCreatesAPrimaryAgent(t *testing.T) {
	newCase := func(t *testing.T, steps []richStep, inputs []selectorInput, stored string) (*configPanelTestSession, *scriptedSelector) {
		t.Helper()
		session := newConfigPanelSession(map[string]string{"agents": stored})
		selector := &scriptedSelector{}
		runConfigPanel(t, session, selector, steps, inputs, "agents")
		return session, selector
	}
	openDefinitions := richStep{label: "Agent definitions", ok: true}
	addRow := richStep{label: configAddRowLabel, ok: true}

	t.Run("the id and the display name seed a primary agent", func(t *testing.T) {
		session, selector := newCase(t,
			[]richStep{
				openDefinitions, addRow,
				{ok: false}, // the new agent's page closes
				{ok: false}, // the definitions page closes
				{label: configSaveRowLabel, ok: true},
				{ok: false},
			},
			[]selectorInput{{value: "review-agent", ok: true}, {value: "", ok: true}},
			`{"definitions":{"main":{"primary":true}}}`,
		)
		if len(session.writes) != 1 {
			t.Fatalf("expected one write, got %d", len(session.writes))
		}
		raw := string(session.writes[0].raw)
		if !strings.Contains(raw, `"review-agent":{"display_name":"Review-agent","mcp_servers":[],"primary":true,"subagents":[]}`) {
			t.Fatalf("raw = %s", raw)
		}
		// The guidance is the prototype's sentence, word for word, and it is on
		// the page the creation opens.
		if !strings.Contains(pageLabels(selector), configPrimaryCreatedNotice) {
			t.Fatalf("the created agent must open with its guidance: %s", pageLabels(selector))
		}
	})

	t.Run("a typed display name wins over the title-cased id", func(t *testing.T) {
		session, _ := newCase(t,
			[]richStep{openDefinitions, addRow, {ok: false}, {ok: false}, {label: configSaveRowLabel, ok: true}, {ok: false}},
			[]selectorInput{{value: "a_b", ok: true}, {value: "Review Bot", ok: true}},
			`{"definitions":{"main":{"primary":true}}}`,
		)
		if raw := string(session.writes[0].raw); !strings.Contains(raw, `"a_b":{"display_name":"Review Bot"`) {
			t.Fatalf("raw = %s", raw)
		}
	})

	t.Run("an id the loader would refuse is refused here", func(t *testing.T) {
		session, selector := newCase(t,
			[]richStep{openDefinitions, addRow, {ok: false}, {ok: false}},
			[]selectorInput{{value: "Bad ID", ok: true}},
			`{"definitions":{"main":{"primary":true}}}`,
		)
		if !strings.Contains(pageLabels(selector), "use a lowercase agent id: letters, digits, hyphens or underscores, starting with a letter") {
			t.Fatalf("the id rule must be stated: %s", pageLabels(selector))
		}
		if len(session.writes) != 0 {
			t.Fatalf("a refused id writes nothing, got %+v", session.writes)
		}
	})

	t.Run("a built-in subagent type is not a primary agent", func(t *testing.T) {
		session, selector := newCase(t,
			[]richStep{openDefinitions, addRow, {ok: false}, {ok: false}},
			[]selectorInput{{value: "explore", ok: true}},
			`{"definitions":{"main":{"primary":true}}}`,
		)
		if !strings.Contains(pageLabels(selector), `"explore" names a built-in subagent type, not a primary agent; choose a different id`) {
			t.Fatalf("the type-name refusal must be stated: %s", pageLabels(selector))
		}
		if len(session.writes) != 0 {
			t.Fatalf("a refused id writes nothing, got %+v", session.writes)
		}
	})

	t.Run("an id that already exists is refused", func(t *testing.T) {
		_, selector := newCase(t,
			[]richStep{openDefinitions, addRow, {ok: false}, {ok: false}},
			[]selectorInput{{value: "main", ok: true}},
			`{"definitions":{"main":{"primary":true}}}`,
		)
		if !strings.Contains(pageLabels(selector), `"main" already exists`) {
			t.Fatalf("the duplicate refusal must be stated: %s", pageLabels(selector))
		}
	})

	t.Run("adding a key inside a definition keeps the ordinary type menu", func(t *testing.T) {
		session, selector := newCase(t,
			[]richStep{
				openDefinitions,
				{label: "main", ok: true},
				{label: configRowOpenLabel, ok: true},
				addRow,
				{label: configNewTextRow, ok: true},
				{ok: false},
				{ok: false},
				{label: configSaveRowLabel, ok: true},
				{ok: false},
			},
			[]selectorInput{{value: "note", ok: true}, {value: "hello", ok: true}},
			`{"definitions":{"main":{"primary":true}}}`,
		)
		// The guard is keyed on the real path, so the nested add still asks what
		// type the value is — never for an agent id.
		if !strings.Contains(pageLabels(selector), "What type is this row's value?") {
			t.Fatalf("the nested add must keep the type menu: %s", pageLabels(selector))
		}
		if len(session.writes) != 1 || !strings.Contains(string(session.writes[0].raw), `"note":"hello"`) {
			t.Fatalf("writes = %+v", session.writes)
		}
	})
}

// A save that names a system subagent entry is still the loader's to refuse: the
// panel's early guard explains the row, and the validator judges the section.
func TestConfigPanelSurfacesTheLoadersSystemEntryRefusal(t *testing.T) {
	session := newConfigPanelSession(map[string]string{"agents": `{"definitions":{"main":{"primary":true,"subagents":[{"name":"goal-evaluator"}]}}}`})
	session.writeErr = errStubSystemEntryRefusal
	selector := &scriptedSelector{}
	renderer := runConfigPanel(t, session, selector, []richStep{
		{label: "Agent definitions", ok: true},
		{ok: false},
		{label: configSaveRowLabel, ok: true},
		{ok: false},
		{ok: false},
	}, nil, "agents")

	if !strings.Contains(pageLabels(selector), "is dispatched by the system and cannot be configured") {
		t.Fatalf("the loader's refusal must reach the page: %s", pageLabels(selector))
	}
	if text := frameText(renderer); strings.Contains(text, configSavedNotice) {
		t.Fatalf("a refused save must not report success: %s", text)
	}
}

// errStubSystemEntryRefusal stands in for the loader's own sentence about a
// system-dispatched subagent entry.
var errStubSystemEntryRefusal = stubSystemEntryError{}

type stubSystemEntryError struct{}

func (stubSystemEntryError) Error() string {
	return `agents.definitions.main.subagents[0]: "goal-evaluator" is dispatched by the system and cannot be configured`
}

// No page of the panel names a setting by its dotted configuration path: the
// row that cannot hold settings is named the way the row list names it, so a
// reader never sees a key they cannot type anywhere (plan 313 Step 5b).
func TestConfigPanelNamesRowsWithoutADottedPath(t *testing.T) {
	session := newConfigPanelSession(map[string]string{"agents": `{"defaults":"taken"}`})
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, []richStep{
		{label: "Network retry limit", ok: true},
		{ok: false},
		{ok: false},
	}, []selectorInput{{value: "5", ok: true}}, "agents")

	pages := pageLabels(selector)
	if !strings.Contains(pages, "Network retry limit is not an object or a list") {
		t.Fatalf("the refusal must name the setting, not its path: %s", pages)
	}
	for _, leak := range []string{"defaults.network_retry_max", "network_retry_max is not"} {
		if strings.Contains(pages, leak) {
			t.Fatalf("a raw configuration key reached the page (%q): %s", leak, pages)
		}
	}
	if len(session.writes) != 0 {
		t.Fatalf("nothing landed, got %+v", session.writes)
	}
}

// The "Subagent models" page is a projection of the agents store (plan 314): it
// presents the per-type defaults, the plan reviewer's global row and the shared
// custom registry — every install-wide subagent row in one place — reads the
// agents document, and saves back to it so the keys it does not show survive.
func TestConfigPanelSubagentModelsProjectsTheAgentsStore(t *testing.T) {
	agents := `{
		"definitions":{"main":{"primary":true,"subagents":[]}},
		"plan_reviewer":{"model":"llm-2"},
		"subagent_models":{"explore":{"model":"llm-1","reasoning_effort":"high"}},
		"shared_subagents":[{"name":"repo-auditor","description":"audits one repository area","system_prompt":"You audit repositories.","tools":"read_only"}]
	}`
	session := newConfigPanelSession(map[string]string{"agents": agents})
	session.snapshot.Models = []appcfg.LLMModelConfig{
		{ID: "llm-1", Provider: "openai", Model: "gpt-main"},
		{ID: "llm-2", Provider: "deepseek", Model: "deepseek-v4-pro"},
	}
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, []richStep{
		{label: "explore", ok: true},
		{label: "model", ok: true},
		{label: configRowEditLabel, ok: true},
		{label: configNewTextRow, ok: true},
		{label: configUpRowLabel, ok: true},
		{label: configSaveRowLabel, ok: true},
	}, []selectorInput{{value: "llm-conditional", ok: true}}, "subagent_models")

	if len(selector.richItems) == 0 {
		t.Fatal("the projection page never opened")
	}
	page := selector.richItems[0]
	// A per-type row reads the model and the level it names, and its status
	// column says the row is this install's global default for the type.
	explore := findRow(t, page, "explore")
	if explore.Description != "openai/gpt-main / High" {
		t.Fatalf("explore's description = %q, want the model and its level", explore.Description)
	}
	if explore.Status != configGlobalDefaultStatus {
		t.Fatalf("explore's status = %q, want %q", explore.Status, configGlobalDefaultStatus)
	}
	// A type with no row of its own follows the conversation, and marks nothing.
	verification := findRow(t, page, "verification")
	if verification.Description != configFollowConversationModel || verification.Status != "" {
		t.Fatalf("verification's row = %q %q, want the follow sentence and no status", verification.Description, verification.Status)
	}
	// The plan reviewer's global row reads the same way, with the status the
	// prototype gives a system type's row.
	reviewer := findRow(t, page, "plan-reviewer")
	if reviewer.Description != "deepseek/deepseek-v4-pro / Row default" {
		t.Fatalf("plan-reviewer's description = %q, want the model and the row default", reviewer.Description)
	}
	if reviewer.Status != configGlobalRowStatus {
		t.Fatalf("plan-reviewer's status = %q, want %q", reviewer.Status, configGlobalRowStatus)
	}
	// The registry is one row of this page; its entries are its own page, named
	// by the name each carries (the test below).
	findRow(t, page, "Shared custom subagents")

	// The save lands in the agents section — the document the page presents —
	// and every key the page does not show comes back untouched.
	if len(session.writes) != 1 {
		t.Fatalf("expected one write, got %+v", session.writes)
	}
	if session.writes[0].section != "agents" {
		t.Fatalf("the projection saved to %q, want the agents store", session.writes[0].section)
	}
	var saved map[string]any
	if err := json.Unmarshal(session.writes[0].raw, &saved); err != nil {
		t.Fatalf("the saved document does not parse: %v", err)
	}
	types, _ := saved["subagent_models"].(map[string]any)
	exploreRow, _ := types["explore"].(map[string]any)
	if got, _ := exploreRow["model"].(string); got != "llm-conditional" {
		t.Fatalf("subagent_models.explore.model = %q, want the edited row", got)
	}
	if got, _ := exploreRow["reasoning_effort"].(string); got != "high" {
		t.Fatalf("subagent_models.explore.reasoning_effort = %q, want the level it had", got)
	}
	reviewerRow, _ := saved["plan_reviewer"].(map[string]any)
	if got, _ := reviewerRow["model"].(string); got != "llm-2" {
		t.Fatalf("plan_reviewer.model = %q, want it untouched", got)
	}
	if _, ok := saved["definitions"]; !ok {
		t.Fatal("the agents page's definitions were dropped from the saved document")
	}
	registry, _ := saved["shared_subagents"].([]any)
	if len(registry) != 1 {
		t.Fatalf("shared_subagents = %v, want the registry untouched", saved["shared_subagents"])
	}
}

// A shared subagent is created from the projection page (plan 314): the add row
// seeds a full definition — a name to type, the description a model reads, the
// prompt to run on, and the read-only tier a subagent starts at — and opens it.
// The entry lands under shared_subagents, and what the seed holds until it is
// named is exactly what the loader refuses.
func TestConfigPanelSubagentModelsSeedsASharedSubagent(t *testing.T) {
	session := newConfigPanelSession(map[string]string{"agents": `{"definitions":{"main":{"primary":true}}}`})
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, []richStep{
		{label: "Shared custom subagents", ok: true},
		{label: configAddRowLabel, ok: true},
		{label: configUpRowLabel, ok: true},
		{label: configUpRowLabel, ok: true},
		{label: configSaveRowLabel, ok: true},
	}, nil, "subagent_models")

	if len(session.writes) != 1 {
		t.Fatalf("expected one write, got %+v", session.writes)
	}
	var saved struct {
		Shared []appcfg.SubagentConfig `json:"shared_subagents"`
	}
	if err := json.Unmarshal(session.writes[0].raw, &saved); err != nil {
		t.Fatalf("the saved document does not parse: %v", err)
	}
	if len(saved.Shared) != 1 {
		t.Fatalf("shared_subagents = %+v, want the seeded entry", saved.Shared)
	}
	seed := saved.Shared[0]
	if seed.Name != "" || seed.Description != "" || seed.SystemPrompt != "" || seed.Tools != appcfg.SubagentToolsReadOnly {
		t.Fatalf("seeded entry = %+v, want the empty name and prompt with the read-only tier", seed)
	}
	// The seed is what the loader refuses until the name and the prompt are
	// typed: the page hands the judgement to the registry's own validator.
	if err := appcfg.ValidateSharedSubagents(saved.Shared, nil, nil, nil); err == nil || !strings.Contains(err.Error(), "invalid name") {
		t.Fatalf("err = %v, want the loader to refuse an unnamed shared subagent", err)
	}
}

// The projection's field paths are keys of the section it presents (plan 314),
// so a typo'd path is caught here rather than by a page that shows nothing. The
// projection is skipped by the section-by-section check above (it is not a Root
// section), which is why this test exists at all.
func TestConfigPanelSchemaProjectionFieldsNameStoreSectionKeys(t *testing.T) {
	types := rootSectionTypes(t)
	projections := 0
	for _, schema := range configSectionSchemas {
		if schema.StoreSection == "" {
			continue
		}
		projections++
		typ, ok := types[schema.StoreSection]
		if !ok {
			t.Fatalf("投影段 %q 的 StoreSection %q 不是 Root 的段", schema.Name, schema.StoreSection)
		}
		if len(schema.Fields) == 0 {
			t.Errorf("投影段 %q 没有任何可编辑字段", schema.Name)
		}
		names := make(map[string]bool)
		for _, name := range jsonFieldNames(typ) {
			names[name] = true
		}
		for _, field := range schema.Fields {
			head := strings.Split(field.Path, ".")[0]
			if !names[head] {
				t.Errorf("投影段 %q 的字段 %q 指向 %q 中不存在的设置 %q", schema.Name, field.Label, schema.StoreSection, head)
			}
		}
	}
	if projections == 0 {
		t.Fatal("no projection section exists: the mechanism is unreachable")
	}
}

// sharedRowsSession is one primary agent's page over an agents document holding
// one shared registry entry, plus whatever entries the caller put on the agent
// (plan 314).
func sharedRowsSession(agentSubagents string) *configPanelTestSession {
	agents := `{
		"definitions":{"main":{"primary":true,"subagents":` + agentSubagents + `}},
		"shared_subagents":[{"name":"repo-auditor","description":"audits one repository area","system_prompt":"You audit repositories.","model":"llm-1","reasoning_effort":"high","tools":"read_only"}],
		"subagent_models":{"plan":{"model":"llm-1"}}
	}`
	session := newConfigPanelSession(map[string]string{"agents": agents})
	session.snapshot.Models = []appcfg.LLMModelConfig{{ID: "llm-1", Provider: "openai", Model: "gpt-main"}}
	session.snapshot.ClaudeCode = false
	return session
}

// agentSubagentsSteps walks from the section page to one agent's subagent list,
// then runs the steps the caller scripts on that page.
func agentSubagentsSteps(more []richStep) []richStep {
	steps := []richStep{
		{label: "Agent definitions", ok: true},
		{label: "main", ok: true},
		{label: configRowOpenLabel, ok: true},
		{label: "subagents", ok: true},
		{label: configRowOpenLabel, ok: true},
	}
	return append(steps, more...)
}

// savedAgentsDoc is the one agents write a run made, decoded.
func savedAgentsDoc(t *testing.T, session *configPanelTestSession) map[string]any {
	t.Helper()
	if len(session.writes) != 1 {
		t.Fatalf("expected one write, got %+v", session.writes)
	}
	if session.writes[0].section != "agents" {
		t.Fatalf("the write went to %q, want agents", session.writes[0].section)
	}
	var doc map[string]any
	if err := json.Unmarshal(session.writes[0].raw, &doc); err != nil {
		t.Fatalf("the saved document does not parse: %v", err)
	}
	return doc
}

// agentSubagentsOf reads one agent's subagent entries out of a saved document.
func agentSubagentsOf(t *testing.T, doc map[string]any, agent string) []map[string]any {
	t.Helper()
	definitions, _ := doc["definitions"].(map[string]any)
	def, _ := definitions[agent].(map[string]any)
	rows, _ := def["subagents"].([]any)
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		entry, _ := row.(map[string]any)
		out = append(out, entry)
	}
	return out
}

// TestConfigPanelAgentPageListsSharedSubagentsDisabled pins the owner's rule on
// the TUI side of the agent page (plan 314): a shared subagent is a row whether
// or not this agent enabled it, it starts off, its description says it is shared
// — and switching it on writes the reference entry, nothing else.
func TestConfigPanelAgentPageListsSharedSubagentsDisabled(t *testing.T) {
	session := sharedRowsSession(`[]`)
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, agentSubagentsSteps([]richStep{
		{label: "repo-auditor", ok: true},
		{label: "Enabled", ok: true},
		{label: configUpRowLabel, ok: true},
		{label: configUpRowLabel, ok: true},
		{label: configUpRowLabel, ok: true},
		{label: configSaveRowLabel, ok: true},
	}), nil, "agents")

	// The row list carries the shared subagent with its status and the
	// registry's description marked as shared.
	var rowsPage []SelectItem
	for _, items := range selector.richItems {
		if slices.Contains(rowLabels(items), "repo-auditor") {
			rowsPage = items
		}
	}
	if rowsPage == nil {
		t.Fatalf("the agent's subagent list never showed the shared subagent: %v", selector.richLabels)
	}
	shared := findRow(t, rowsPage, "repo-auditor")
	if shared.Status != configSharedStatus {
		t.Fatalf("the shared row's status = %q, want %q", shared.Status, configSharedStatus)
	}
	if shared.Description != "audits one repository area (shared)" {
		t.Fatalf("the shared row's description = %q, want the registry's description marked shared", shared.Description)
	}

	// Switching it on writes the reference entry: the name and enabled true.
	entries := agentSubagentsOf(t, savedAgentsDoc(t, session), "main")
	if len(entries) != 1 {
		t.Fatalf("saved subagents = %+v, want the one reference entry", entries)
	}
	if entries[0]["name"] != "repo-auditor" || entries[0]["enabled"] != true {
		t.Fatalf("reference entry = %+v, want the name and enabled true", entries[0])
	}
	if _, ok := entries[0]["description"]; ok {
		t.Fatalf("reference entry = %+v, want nothing but the name and the switch", entries[0])
	}
}

// TestConfigPanelAgentPageDisablesASharedSubagent pins the other direction
// (plan 314): switching an enabled shared subagent off removes the reference
// entry, because absence is what disabled means.
func TestConfigPanelAgentPageDisablesASharedSubagent(t *testing.T) {
	session := sharedRowsSession(`[{"name":"repo-auditor","enabled":true},{"name":"db-migrator","description":"runs migrations","system_prompt":"You migrate."}]`)
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, agentSubagentsSteps([]richStep{
		{label: "repo-auditor", ok: true},
		{label: "Enabled", ok: true},
		{label: configUpRowLabel, ok: true},
		{label: configUpRowLabel, ok: true},
		{label: configUpRowLabel, ok: true},
		{label: configSaveRowLabel, ok: true},
	}), nil, "agents")

	entries := agentSubagentsOf(t, savedAgentsDoc(t, session), "main")
	if len(entries) != 1 {
		t.Fatalf("saved subagents = %+v, want the per-agent custom subagent alone", entries)
	}
	if entries[0]["name"] != "db-migrator" {
		t.Fatalf("saved subagents = %+v, want the shared reference gone", entries)
	}
}

// TestConfigPanelAgentPageSharedDefinitionIsReadOnly pins the read-only half
// (plan 314): a shared subagent's fields are facts this page shows, never
// settings it edits, so opening one of them says where the definition lives and
// writes nothing — while a per-agent custom subagent and a built-in override
// keep their own editing.
func TestConfigPanelAgentPageSharedDefinitionIsReadOnly(t *testing.T) {
	session := sharedRowsSession(`[{"name":"explore","model":"llm-1"},{"name":"db-migrator","description":"runs migrations","system_prompt":"You migrate."}]`)
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, agentSubagentsSteps([]richStep{
		{label: "repo-auditor", ok: true},
		{label: "Model", ok: true},
		{label: configUpRowLabel, ok: true},
		{label: configUpRowLabel, ok: true},
		{label: configUpRowLabel, ok: true},
		{label: configUpRowLabel, ok: true},
		{label: configRowBackLabel, ok: true},
	}), nil, "agents")

	// The definition's facts are on the page, read from the registry, and the
	// page says where the definition itself is edited.
	var sharedPage []SelectItem
	for _, items := range selector.richItems {
		if slices.Contains(rowLabels(items), "Description") && slices.Contains(rowLabels(items), "Enabled") {
			sharedPage = items
		}
	}
	if sharedPage == nil {
		t.Fatalf("the shared subagent's page never opened: %v", selector.richLabels)
	}
	for label, want := range map[string]string{
		"Description": "audits one repository area (shared)",
		"Model":       "openai/gpt-main / High",
		"Tools":       "read_only",
		"Enabled":     "Off: this agent does not dispatch it",
	} {
		if got := findRow(t, sharedPage, label).Description; got != want {
			t.Fatalf("%s row = %q, want %q", label, got, want)
		}
	}
	if !strings.Contains(pageLabels(selector), configSharedSubagentNotice) {
		t.Fatalf("the shared row's page does not say where the definition is edited: %s", pageLabels(selector))
	}
	// Nothing was staged by opening the definition's fields, and nothing was
	// written: there are no edits to save.
	if len(session.writes) != 0 {
		t.Fatalf("opening a shared subagent's fields wrote %+v, want nothing", session.writes)
	}

	// A per-agent custom subagent keeps its own row and its own editing: it is
	// not marked shared, and its description is its own.
	var rowsPage []SelectItem
	for _, items := range selector.richItems {
		if slices.Contains(rowLabels(items), "repo-auditor") {
			rowsPage = items
		}
	}
	if rowsPage == nil {
		t.Fatalf("the agent's subagent list never carried the shared row: %v", selector.richLabels)
	}
	// Exactly one row is marked shared: the per-agent entries the agent already
	// had keep their own rows and no status — they are still its own to edit.
	marked := make([]string, 0, len(rowsPage))
	for _, item := range rowsPage {
		if item.Status != "" {
			marked = append(marked, item.Label+"="+item.Status)
		}
	}
	if len(marked) != 1 || marked[0] != "repo-auditor="+configSharedStatus {
		t.Fatalf("marked rows = %v, want the shared one alone: %+v", marked, rowsPage)
	}
}

// configEffortCatalog is the two models the effort picker's tests name: one with
// four levels and no off switch, and one the catalog lets turn thinking off.
const configEffortCatalog = `{
	"xai/grok-effort": {"name": "Grok Effort", "reasoning": true,
		"reasoning_options": [{"type": "effort", "values": ["low", "medium", "high", "xhigh"]}],
		"limit": {"context": 200000, "output": 8000}},
	"openai/gpt-off": {"name": "GPT Off", "reasoning": true,
		"reasoning_options": [{"type": "effort", "values": ["none", "low", "medium", "high", "xhigh"]}],
		"limit": {"context": 200000, "output": 8000}}
}`

// configEffortModels is the install-wide table a level is resolved through: the
// row ids every effort row names.
func configEffortModels() []appcfg.LLMModelConfig {
	return []appcfg.LLMModelConfig{
		{ID: "llm-grok", Provider: "xai", Model: "grok-effort"},
		{ID: "llm-off", Provider: "openai", Model: "gpt-off"},
		{ID: "llm-messages", Provider: "openai", Model: "gpt-off", APIPath: "/v1/messages"},
	}
}

// effortPickerItems is the page a level's picker drew: the one page whose rows
// end in Row default.
func effortPickerItems(t *testing.T, selector *scriptedSelector) []string {
	t.Helper()
	for _, items := range selector.richItems {
		if slices.Contains(rowLabels(items), configRowDefaultEffort) {
			return rowLabels(items)
		}
	}
	t.Fatalf("no effort picker opened: %v", selector.richLabels)
	return nil
}

// A thinking level is not typed: the picker offers the levels the model row
// beside it can be given, in the one order every picker draws them, and the
// empty value last as the row's own default (plan 319).
func TestConfigPanelEffortPickerOffersTheModelsLevels(t *testing.T) {
	useSetupCatalogWith(t, configEffortCatalog)
	session := newConfigPanelSession(map[string]string{"agents": `{"goal_evaluator":{"model":"llm-grok"}}`})
	session.snapshot.Models = configEffortModels()
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, []richStep{
		{label: "Goal check thinking level", ok: true},
		{ok: false},
	}, nil, "agents")

	got := effortPickerItems(t, selector)
	want := []string{"Low", "Medium", "High", "xHigh", configRowDefaultEffort}
	if !slices.Equal(got, want) {
		t.Fatalf("picker = %v, want %v", got, want)
	}
	if len(session.writes) != 0 {
		t.Fatalf("opening the picker wrote %+v, want nothing", session.writes)
	}
}

// The off switch is a capability of the row, not a level: a row the catalog lets
// turn thinking off offers Off first, and the level it stores is the wire id.
func TestConfigPanelEffortPickerOffersOffWhenTheRowAllowsIt(t *testing.T) {
	useSetupCatalogWith(t, configEffortCatalog)
	session := newConfigPanelSession(map[string]string{"agents": `{"goal_evaluator":{"model":"llm-off"}}`})
	session.snapshot.Models = configEffortModels()
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, []richStep{
		{label: "Goal check thinking level", ok: true},
		{ok: false},
	}, nil, "agents")

	got := effortPickerItems(t, selector)
	want := []string{"Off", "Low", "Medium", "High", "xHigh", configRowDefaultEffort}
	if !slices.Equal(got, want) {
		t.Fatalf("picker = %v, want %v", got, want)
	}
}

// A level the client cannot send is not offered, whatever the row declares: the
// Messages protocol rejects "none" outright, so Off is missing there while the
// same model on the chat-completions path offers it.
func TestConfigPanelEffortPickerDropsOffOnTheMessagesProtocol(t *testing.T) {
	useSetupCatalogWith(t, configEffortCatalog)
	session := newConfigPanelSession(map[string]string{"agents": `{"goal_evaluator":{"model":"llm-messages"}}`})
	session.snapshot.Models = configEffortModels()
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, []richStep{
		{label: "Goal check thinking level", ok: true},
		{ok: false},
	}, nil, "agents")

	got := effortPickerItems(t, selector)
	want := []string{"Low", "Medium", "High", "xHigh", configRowDefaultEffort}
	if !slices.Equal(got, want) {
		t.Fatalf("picker = %v, want %v", got, want)
	}
}

// A level belongs to the model beside it: with no model there is nothing to
// offer, and the page says which setting to choose first instead of inventing a
// list. The loader refuses such a pair too ("reasoning_effort requires model").
func TestConfigPanelEffortPickerNeedsAModel(t *testing.T) {
	useSetupCatalogWith(t, configEffortCatalog)
	session := newConfigPanelSession(map[string]string{"agents": `{"goal_evaluator":{}}`})
	session.snapshot.Models = configEffortModels()
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, []richStep{
		{label: "Goal check thinking level", ok: true},
		{ok: false},
	}, nil, "agents")

	if !strings.Contains(pageLabels(selector), configEffortNeedsModelNotice) {
		t.Fatalf("the page must say a model comes first: %v", selector.richLabels)
	}
	for _, items := range selector.richItems {
		if slices.Contains(rowLabels(items), configRowDefaultEffort) {
			t.Fatalf("a picker opened with no model beside it: %v", rowLabels(items))
		}
	}
}

// The picked level is what the store gets, and Row default stores nothing: the
// key goes away, which is how "follow the model row's own default" reads.
func TestConfigPanelEffortPickerWritesTheChosenLevel(t *testing.T) {
	for _, tc := range []struct {
		name string
		pick string
		want string
		gone bool
	}{
		{name: "a level", pick: "xHigh", want: "xhigh"},
		{name: "the row's default", pick: configRowDefaultEffort, gone: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			useSetupCatalogWith(t, configEffortCatalog)
			session := newConfigPanelSession(map[string]string{"agents": `{"goal_evaluator":{"model":"llm-grok","reasoning_effort":"low"}}`})
			session.snapshot.Models = configEffortModels()
			selector := &scriptedSelector{}
			runConfigPanel(t, session, selector, []richStep{
				{label: "Goal check thinking level", ok: true},
				{label: tc.pick, ok: true},
				{label: configSaveRowLabel, ok: true},
			}, nil, "agents")

			if len(session.writes) != 1 {
				t.Fatalf("expected one write, got %+v", session.writes)
			}
			var saved struct {
				GoalCheck map[string]any `json:"goal_evaluator"`
			}
			if err := json.Unmarshal(session.writes[0].raw, &saved); err != nil {
				t.Fatalf("the saved document does not parse: %v", err)
			}
			level, ok := saved.GoalCheck["reasoning_effort"]
			if tc.gone {
				if ok {
					t.Fatalf("row default stored %v, want the key gone", level)
				}
				return
			}
			if level != tc.want {
				t.Fatalf("reasoning_effort = %v, want %q", level, tc.want)
			}
		})
	}
}

// The level a row offers is the model row's, read through the one helper every
// surface shares: a row id, a legacy pair, a row whose protocol drops Off, and a
// reference nothing answers all read from the same table (plan 318/319).
func TestConfigPanelEffortLevelsFollowTheModelRow(t *testing.T) {
	useSetupCatalogWith(t, configEffortCatalog)
	panel := &configPanel{models: configEffortModels()}
	for _, tc := range []struct {
		name string
		ref  string
		want []string
	}{
		{name: "a row id", ref: "llm-grok", want: []string{"low", "medium", "high", "xhigh"}},
		{name: "an off-capable row", ref: "llm-off", want: []string{"none", "low", "medium", "high", "xhigh"}},
		{name: "a legacy pair", ref: "xai/grok-effort", want: []string{"low", "medium", "high", "xhigh"}},
		{name: "an unknown pair", ref: "xai/not-in-the-catalog"},
		{name: "an unknown row id", ref: "llm-gone"},
		{name: "no reference at all", ref: " "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := panel.configModelEfforts(tc.ref); !slices.Equal(got, tc.want) {
				t.Fatalf("levels for %q = %v, want %v", tc.ref, got, tc.want)
			}
		})
	}

	// A row that declares the off switch itself carries it into the list: the
	// row's own answer, over a catalog that names no "none".
	off := true
	llm.SetModelOverrides(map[string]llm.ModelOverride{"llm-grok": {ReasoningOff: &off}}, nil)
	t.Cleanup(func() { llm.SetModelOverrides(nil, nil) })
	if got, want := panel.configModelEfforts("llm-grok"), []string{"none", "low", "medium", "high", "xhigh"}; !slices.Equal(got, want) {
		t.Fatalf("levels with the row's off switch = %v, want %v", got, want)
	}
}

// A stored model row is where a level lives beside its model: the picker opens
// from the row's own key, whether that key already holds a level or is being
// added — a type row under subagent_models, the plan reviewer's global row, and
// one entry of a definition's subagents list all read the same way (plan 319).
func TestConfigPanelEffortPickerOnAStoredModelRow(t *testing.T) {
	useSetupCatalogWith(t, configEffortCatalog)
	agents := `{
		"definitions":{"main":{"primary":true,"subagents":[{"name":"explore","model":"llm-grok"}]}},
		"plan_reviewer":{"model":"llm-off"},
		"subagent_models":{"plan":{"model":"llm-grok","reasoning_effort":"low"}}
	}`
	session := newConfigPanelSession(map[string]string{"agents": agents})
	session.snapshot.Models = configEffortModels()
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, []richStep{
		// The type row's own level, changed from the picker.
		{label: "plan", ok: true},
		{label: configEffortKey, ok: true},
		{label: configRowEditLabel, ok: true},
		{label: "High", ok: true},
		{label: configUpRowLabel, ok: true},
		// The plan reviewer's row has no level yet: adding the key asks the same
		// picker, never the type list.
		{label: "plan-reviewer", ok: true},
		{label: configAddRowLabel, ok: true},
		{label: "Medium", ok: true},
		{label: configUpRowLabel, ok: true},
		{label: configSaveRowLabel, ok: true},
	}, []selectorInput{{value: configEffortKey, ok: true}}, "subagent_models")

	got := effortPickerItems(t, selector)
	want := []string{"Low", "Medium", "High", "xHigh", configRowDefaultEffort}
	if !slices.Equal(got, want) {
		t.Fatalf("picker = %v, want %v", got, want)
	}
	doc := savedAgentsDoc(t, session)
	types, _ := doc["subagent_models"].(map[string]any)
	plan, _ := types["plan"].(map[string]any)
	if plan["reasoning_effort"] != "high" {
		t.Fatalf("subagent_models.plan.reasoning_effort = %v, want the picked level", plan["reasoning_effort"])
	}
	reviewer, _ := doc["plan_reviewer"].(map[string]any)
	if reviewer["reasoning_effort"] != "medium" {
		t.Fatalf("plan_reviewer.reasoning_effort = %v, want the picked level", reviewer["reasoning_effort"])
	}
	// The definition's entry is untouched: nothing on this page edited it.
	entries := agentSubagentsOf(t, doc, "main")
	if len(entries) != 1 || entries[0]["reasoning_effort"] != nil {
		t.Fatalf("subagents = %+v, want the entry as it was", entries)
	}
}

// A definition's subagent list is the same rule one level deeper: the entry's
// level is picked from the model the entry names, and the entry's own model is
// the sibling the picker reads (plan 319).
func TestConfigPanelEffortPickerOnASubagentEntry(t *testing.T) {
	useSetupCatalogWith(t, configEffortCatalog)
	session := newConfigPanelSession(map[string]string{"agents": `{
		"definitions":{"main":{"primary":true,"subagents":[{"name":"explore","model":"llm-grok","reasoning_effort":"low"}]}}
	}`})
	session.snapshot.Models = configEffortModels()
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, agentSubagentsSteps([]richStep{
		// A per-agent entry is named by its place in the list, not by the type
		// it overrides.
		{label: configRowLabel("", 0, true), ok: true},
		{label: configRowOpenLabel, ok: true},
		{label: configEffortKey, ok: true},
		{label: configRowEditLabel, ok: true},
		{label: "xHigh", ok: true},
		{label: configUpRowLabel, ok: true},
		{label: configUpRowLabel, ok: true},
		{label: configUpRowLabel, ok: true},
		{label: configUpRowLabel, ok: true},
		{label: configSaveRowLabel, ok: true},
	}), nil, "agents")

	got := effortPickerItems(t, selector)
	want := []string{"Low", "Medium", "High", "xHigh", configRowDefaultEffort}
	if !slices.Equal(got, want) {
		t.Fatalf("picker = %v, want %v", got, want)
	}
	doc := savedAgentsDoc(t, session)
	entries := agentSubagentsOf(t, doc, "main")
	if len(entries) != 1 || entries[0]["reasoning_effort"] != "xhigh" {
		t.Fatalf("subagents = %+v, want the picked level on the entry", entries)
	}
}
