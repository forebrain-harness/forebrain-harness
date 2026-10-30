package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
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

// A tri-state boolean lands as true, and only a save writes anything: the
// panel says when a change takes effect instead of applying it.
func TestConfigPanelSavesATriStateBoolean(t *testing.T) {
	session := newConfigPanelSession(nil)
	selector := &scriptedSelector{}
	renderer := runConfigPanel(t, session, selector, []richStep{
		{label: "memories", ok: true},
		{label: "使用记忆", ok: true},
		{label: configBoolOnLabel, ok: true},
		{label: configSaveRowLabel, ok: true},
		{ok: false},
	}, nil)

	if len(session.writes) != 1 {
		t.Fatalf("expected one write, got %d", len(session.writes))
	}
	write := session.writes[0]
	if write.section != "memories" {
		t.Fatalf("section = %q, want memories", write.section)
	}
	if string(write.raw) != `{"use_memories":true}` {
		t.Fatalf("raw = %s", write.raw)
	}
	if text := frameText(renderer); !strings.Contains(text, configSavedNotice) {
		t.Fatalf("the save must say when it takes effect; frames:\n%s", text)
	}
}

// Clearing a switch deletes the key rather than storing false, because nil is
// what selects the setting's default.
func TestConfigPanelClearsAFieldToItsDefault(t *testing.T) {
	session := newConfigPanelSession(map[string]string{"memories": `{"use_memories":false}`})
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, []richStep{
		{label: "memories", ok: true},
		{label: "使用记忆", ok: true},
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
		{label: "保留天数", ok: true},
		{label: "保留天数", ok: true},
		{label: configSaveRowLabel, ok: true},
		{ok: false},
	}, []selectorInput{{value: "99999", ok: true}, {value: "45", ok: true}})

	// The refusal is the page's own line, so it is in the prompt the panel
	// showed next — the user reads it before the value they retype.
	if refusals := strings.Join(selector.richLabels, "\n"); !strings.Contains(refusals, "不能大于 3650") {
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
		{label: "检索返回条数", ok: true},
		{label: configSaveRowLabel, ok: true},
		{ok: false},
	}, []selectorInput{{value: "99", ok: true}})

	if prompts := strings.Join(selector.richLabels, "\n"); !strings.Contains(prompts, "按上限存为 20") {
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
		{label: "memories", ok: true},
		{label: "使用记忆", ok: true},
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
	if !strings.HasPrefix(selector.richLabels[0], "cron · 定时任务") {
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
	if !strings.Contains(text, "不是已知配置段") || !strings.Contains(text, "sandbox_mode") {
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
// rows: permissions profiles, MCP servers and agent definitions all take this
// one path.
func TestConfigPanelEditsATableRowInPlace(t *testing.T) {
	session := newConfigPanelSession(map[string]string{"permissions": `{"work":{"description":"旧"}}`})
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, []richStep{
		{label: "permissions", ok: true},
		{label: "权限档案", ok: true},
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
	if raw := string(session.writes[0].raw); raw != `{"work":{"description":"新"}}` {
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
		{label: "额外可写根目录", ok: true},
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
		{label: "额外可写根目录", ok: true},
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
	session := newConfigPanelSession(map[string]string{"permissions": `{"work":{}}`})
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, []richStep{
		{label: "permissions", ok: true},
		{label: "权限档案", ok: true},
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

	if err := session.UpdateConfigSection(ctx, "memories", json.RawMessage(`{"use_memories":true}`)); err != nil {
		t.Fatalf("a loadable section must be storeable: %v", err)
	}
	snapshot, err := session.ConfigPanelSections(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(snapshot.Stored["memories"]); got != `{"use_memories":true}` {
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
	if got := string(after.Stored["memories"]); got != `{"use_memories":true}` {
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
// and the two kinds line up with the store's own section table: the 16 rows
// fb_config holds are editable, gateway and credentials are not.
//
// This is the panel's one hard constraint on new work: add a section to Root
// without adding a descriptor and the panel silently loses it, so the loss
// fails here instead.
func TestConfigPanelSchemaCoversEveryRootSection(t *testing.T) {
	types := rootSectionTypes(t)
	byName := make(map[string]configSectionSchema, len(configSectionSchemas))
	for _, schema := range configSectionSchemas {
		if _, dup := byName[schema.Name]; dup {
			t.Errorf("段 %q 有两条描述符", schema.Name)
		}
		byName[schema.Name] = schema
	}
	for name := range types {
		if _, ok := byName[name]; !ok {
			t.Errorf("Root 的段 %q 没有描述符，/config 面板里看不到它", name)
		}
	}
	for name := range byName {
		if _, ok := types[name]; !ok {
			t.Errorf("描述符 %q 不是 Root 的段", name)
		}
	}

	stored := make(map[string]bool)
	for _, name := range process.RootConfigSections() {
		stored[name] = true
	}
	for name, schema := range byName {
		if schema.YAMLOnly {
			if stored[name] {
				t.Errorf("%q 标了只读，却在 fb_config 的段表里", name)
			}
			if schema.Tab != configTabFile {
				t.Errorf("只读段 %q 必须归到 %q 页签", name, configTabFile)
			}
			continue
		}
		if !stored[name] {
			t.Errorf("%q 可编辑，但不在 process.RootConfigSections() 里", name)
		}
	}
}

// A descriptor's field paths are the section's own JSON keys, in both
// directions: no path names a setting that does not exist, and no top-level
// setting of a section is left without a way to reach it.
func TestConfigPanelSchemaFieldsMatchTheirSection(t *testing.T) {
	types := rootSectionTypes(t)
	for _, schema := range configSectionSchemas {
		typ, ok := types[schema.Name]
		if !ok {
			continue // already reported by the coverage test
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
		for _, field := range schema.Fields {
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
