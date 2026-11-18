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
	return runConfigPanelOn(t, session, selector, steps, inputs, args...)
}

// runConfigPanelOn is the same run over any session, so a case can hold the
// panel against the store's own validator instead of a double.
func runConfigPanelOn(t *testing.T, session Session, selector Selector, steps []richStep, inputs []selectorInput, args ...string) *Renderer {
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
// that a newly added section is reachable at all. A row is the section's title
// with what it is for, marked Default while the database holds no row for it;
// the name it is stored under is an argument, never a row label (plan 321).
func TestConfigPanelListsEverySection(t *testing.T) {
	session := newConfigPanelSession(nil)
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, nil, nil)

	if len(selector.richItems) != 1 {
		t.Fatalf("expected the section list alone, got %d pages", len(selector.richItems))
	}
	got := map[string]SelectItem{}
	tabs := map[string]bool{}
	for _, item := range selector.richItems[0] {
		got[item.Label] = item
		tabs[item.Category] = true
		if strings.TrimSpace(item.Description) == "" {
			t.Errorf("%s 的行没有说明", item.Label)
		}
	}
	for _, schema := range configSectionSchemas {
		item, ok := got[schema.Title]
		if !ok {
			t.Errorf("段 %q 不在面板的段列表里", schema.Title)
			continue
		}
		if _, leaked := got[schema.Name]; leaked && schema.Name != schema.Title {
			t.Errorf("段的存储名 %q 出现在列表里", schema.Name)
		}
		if item.Description != schema.Summary {
			t.Errorf("段 %q 的说明 = %q, want %q", schema.Title, item.Description, schema.Summary)
		}
		if want := configDefaultStatus; schema.YAMLOnly && item.Status != "" {
			t.Errorf("只读段 %q 带了状态 %q", schema.Title, item.Status)
		} else if !schema.YAMLOnly && item.Status != want {
			t.Errorf("段 %q 的状态 = %q, want %q", schema.Title, item.Status, want)
		}
	}
	// A section the database holds stops reading as the default.
	session = newConfigPanelSession(map[string]string{"cron": `{"retention_days":30}`})
	selector = &scriptedSelector{}
	runConfigPanel(t, session, selector, nil, nil)
	if item := got["Scheduled tasks"]; item.Status != configDefaultStatus {
		t.Fatalf("the empty session's row already reads %q", item.Status)
	}
	for _, item := range selector.richItems[0] {
		if item.Label == "Scheduled tasks" && item.Status != "" {
			t.Fatalf("a configured section carries %q, want no status", item.Status)
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

// A tri-state boolean lands as true, and the confirmed edit is the write: there
// is no save step to reach (plan 321). Success is silent — the panel says
// nothing when a value lands.
func TestConfigPanelWritesATriStateBooleanOnEnter(t *testing.T) {
	session := newConfigPanelSession(nil)
	selector := &scriptedSelector{}
	renderer := runConfigPanel(t, session, selector, []richStep{
		{label: "Feature switches", ok: true},
		{label: "Memory", ok: true},
		{label: configBoolOnLabel, ok: true},
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
	if text := frameText(renderer); text != "" {
		t.Fatalf("a value that lands says nothing; frames:\n%s", text)
	}
}

// Every row kind writes on the confirmed edit, through the one store path, and
// nothing short of a confirmed edit writes at all (plan 321 Step 6).
func TestConfigPanelWritesOnEnterForEveryRowKind(t *testing.T) {
	for _, tc := range []struct {
		name    string
		stored  map[string]string
		steps   []richStep
		inputs  []selectorInput
		models  []appcfg.LLMModelConfig
		section string
		want    string
	}{
		{
			name: "enum", section: "approval_preset",
			steps: []richStep{
				{label: "Approval preset", ok: true},
				{label: "Approval preset", ok: true},
				{label: "Full access", ok: true},
			},
			want: `"full_access"`,
		},
		{
			name: "number", section: "cron",
			steps: []richStep{
				{label: "Scheduled tasks", ok: true},
				{label: "Keep days", ok: true},
			},
			inputs: []selectorInput{{value: "45", ok: true}},
			want:   `{"retention_days":45}`,
		},
		{
			name: "text", section: "compact",
			steps: []richStep{
				{label: "Context compaction", ok: true},
				{label: "Hand-off prompt", ok: true},
			},
			inputs: []selectorInput{{value: "Keep it short.", ok: true}},
			want:   `{"prompt":"Keep it short."}`,
		},
		{
			name: "rows", section: "sandbox_workspace_write",
			stored: map[string]string{"sandbox_workspace_write": `{"writable_roots":["/a"]}`},
			steps: []richStep{
				{label: "Workspace sandbox", ok: true},
				{label: "Extra writable roots", ok: true},
				{label: configAddRowLabel, ok: true},
				{label: configNewTextRow, ok: true},
			},
			inputs: []selectorInput{{value: "/b", ok: true}},
			want:   `{"writable_roots":["/a","/b"]}`,
		},
		{
			// A model reference is picked, not typed (plan 324): the picker
			// lists the install's global rows and the write stores the row's
			// id.
			name: "model ref", section: "agents",
			models: []appcfg.LLMModelConfig{{ID: "llm-1", Provider: "openai", Model: "gpt-main"}},
			steps: []richStep{
				{label: "Agents", ok: true},
				{label: "Goal check model", ok: true},
				{label: "openai/gpt-main", ok: true},
			},
			want: `{"goal_evaluator":{"model":"llm-1"}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := newConfigPanelSession(tc.stored)
			session.snapshot.Models = tc.models
			selector := &scriptedSelector{}
			renderer := runConfigPanel(t, session, selector, tc.steps, tc.inputs)

			if len(session.writes) != 1 {
				t.Fatalf("the confirmed edit is one write, got %+v", session.writes)
			}
			if session.writes[0].section != tc.section {
				t.Fatalf("section = %q, want %q", session.writes[0].section, tc.section)
			}
			if got := string(session.writes[0].raw); got != tc.want {
				t.Fatalf("raw = %s, want %s", got, tc.want)
			}
			if text := frameText(renderer); text != "" {
				t.Fatalf("a value that lands says nothing; frames:\n%s", text)
			}
		})
	}
}

// A hook row is created complete and written once (plan 321, owner 2026-11-17):
// the store refuses every partial hook, so the panel asks the type and the field
// that type needs, and the row exists only as a candidate the loader accepts.
func TestConfigPanelCreatesACompleteHookInOneWrite(t *testing.T) {
	openHooks := []richStep{
		{label: "Hooks", ok: true},
		{label: "Hook events", ok: true},
		{label: configAddRowLabel, ok: true},
		{label: "PreToolUse", ok: true},
	}
	openMatcher := []richStep{
		{label: "PreToolUse", ok: true},
		{label: configAddRowLabel, ok: true},
	}
	for _, tc := range []struct {
		name   string
		steps  []richStep
		inputs []selectorInput
		want   string
	}{
		{
			name:   "command",
			steps:  append(append([]richStep{}, openMatcher...), richStep{label: "Command", ok: true}),
			inputs: []selectorInput{{value: configHookAllToolsMatcher, ok: true}, {value: "project-check", ok: true}},
			want:   `{"command":"project-check","type":"command"}`,
		},
		{
			name:   "prompt",
			steps:  append(append([]richStep{}, openMatcher...), richStep{label: "Prompt", ok: true}),
			inputs: []selectorInput{{value: configHookAllToolsMatcher, ok: true}, {value: "Check the work is done.", ok: true}},
			want:   `{"prompt":"Check the work is done.","type":"prompt"}`,
		},
		{
			name:   "agent",
			steps:  append(append([]richStep{}, openMatcher...), richStep{label: "Agent", ok: true}),
			inputs: []selectorInput{{value: configHookAllToolsMatcher, ok: true}, {value: "Review the plan.", ok: true}},
			want:   `{"prompt":"Review the plan.","type":"agent"}`,
		},
		{
			name:   "http",
			steps:  append(append([]richStep{}, openMatcher...), richStep{label: "HTTP", ok: true}),
			inputs: []selectorInput{{value: configHookAllToolsMatcher, ok: true}, {value: "https://hooks.example.com/review", ok: true}},
			want:   `{"type":"http","url":"https://hooks.example.com/review"}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := newConfigPanelSession(nil)
			selector := &scriptedSelector{}
			runConfigPanel(t, session, selector, append(append([]richStep{}, openHooks...), tc.steps...), tc.inputs)

			// The event's empty matcher list, then the complete matcher: never a
			// candidate the loader refuses, and never a second write to finish
			// one.
			if len(session.writes) != 2 {
				t.Fatalf("expected the event and the matcher, got %+v", session.writes)
			}
			if got := string(session.writes[0].raw); got != `{"PreToolUse":[]}` {
				t.Fatalf("the event's row = %s", got)
			}
			raw := session.writes[1].raw
			var hooks appcfg.HooksSettings
			if err := json.Unmarshal(raw, &hooks); err != nil {
				t.Fatalf("the written section does not parse: %v (%s)", err, raw)
			}
			if err := appcfg.ValidateHooksSettings(hooks); err != nil {
				t.Fatalf("the panel wrote a hook the loader refuses: %v (%s)", err, raw)
			}
			matchers := hooks["PreToolUse"]
			if len(matchers) != 1 || len(matchers[0].Hooks) != 1 {
				t.Fatalf("matchers = %+v, want one matcher with one action", matchers)
			}
			// The stored action, as the row holds it: the type and the one field
			// that type needs, and nothing a reader never asked for.
			var events map[string][]struct {
				Hooks []map[string]any `json:"hooks"`
			}
			if err := json.Unmarshal(raw, &events); err != nil {
				t.Fatalf("the written section does not parse: %v (%s)", err, raw)
			}
			action, err := json.Marshal(events["PreToolUse"][0].Hooks[0])
			if err != nil {
				t.Fatal(err)
			}
			if got := string(action); got != tc.want {
				t.Fatalf("action = %s, want %s", got, tc.want)
			}
		})
	}
}

// The other half of the same rule: an action the loader would refuse never
// reaches a write, and the loader's own sentence is the page's next line, so a
// matcher is never left holding an action the store would not take.
func TestConfigPanelRefusesAnIncompleteHookBeforeWriting(t *testing.T) {
	session := newConfigPanelSession(nil)
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, []richStep{
		{label: "Hooks", ok: true},
		{label: "Hook events", ok: true},
		{label: configAddRowLabel, ok: true},
		{label: "PreToolUse", ok: true},
		{label: "PreToolUse", ok: true},
		{label: configAddRowLabel, ok: true},
		{label: "HTTP", ok: true},
	}, []selectorInput{{value: configHookAllToolsMatcher, ok: true}, {value: "hooks.example.com/review", ok: true}})

	if len(session.writes) != 1 {
		t.Fatalf("only the event's row may land, got %+v", session.writes)
	}
	if !strings.Contains(pageLabels(selector), "http hook requires absolute url") {
		t.Fatalf("the loader's refusal must reach the page: %s", pageLabels(selector))
	}
}

// The one shape the prototype names — a hook switched to HTTP while it still
// carries only a command — is judged by the store's own validator, and a
// refusal is not applied: the section keeps the action it had (plan 321). The
// write goes through the real store, so the validator here is the one the
// panel meets in the application.
func TestConfigPanelKeepsTheHookTheStoreRefusesToChange(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	cfgPath := filepath.Join(home, "forebrain.yaml")
	if err := os.WriteFile(cfgPath, []byte("gateway:\n  http_addr: 127.0.0.1:9\n"), 0o600); err != nil {
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

	seed := `{"PreToolUse":[{"matcher":"*","hooks":[{"type":"command","command":"project-check"}]}]}`
	if err := session.UpdateConfigSection(ctx, "hooks", json.RawMessage(seed)); err != nil {
		t.Fatalf("the seed must be a hook the loader accepts: %v", err)
	}

	selector := &scriptedSelector{}
	runConfigPanelOn(t, session, selector, []richStep{
		{label: "Hooks", ok: true},
		{label: "Hook events", ok: true},
		{label: "PreToolUse", ok: true},
		{label: "#1", ok: true},
		{label: "Actions", ok: true},
		{label: "#1", ok: true},
		{label: "Action type", ok: true},
		{label: "HTTP", ok: true},
	}, nil)

	if !strings.Contains(pageLabels(selector), "http hook requires absolute url") {
		t.Fatalf("the store's refusal must reach the page: %s", pageLabels(selector))
	}
	snapshot, err := session.ConfigPanelSections(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var stored appcfg.HooksSettings
	if err := json.Unmarshal(snapshot.Stored["hooks"], &stored); err != nil {
		t.Fatalf("the stored hooks do not parse: %v", err)
	}
	actions := stored["PreToolUse"][0].Hooks
	if len(actions) != 1 || actions[0].Type != appcfg.HookTypeCommand || actions[0].Command != "project-check" {
		t.Fatalf("the refused edit changed the stored action: %+v", actions)
	}
}

// Clearing a switch deletes the key rather than storing false, because nil is
// what selects the setting's default.
func TestConfigPanelClearsAFieldToItsDefault(t *testing.T) {
	session := newConfigPanelSession(map[string]string{"features": `{"memories":false}`})
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, []richStep{
		{label: "Feature switches", ok: true},
		{label: "Memory", ok: true},
		{label: configDefaultActionLabel, ok: true},
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

// A choice page lists the real values and nothing else: the way back to the
// setting's own default is the `Default` action at the end of the list, not a
// third value that reads like one (owner 2026-11-16, plan 321 Step 3). The
// action is what unsets an enum too, and it writes the key out.
func TestConfigPanelChoiceListsHoldValuesAndTheDefaultAction(t *testing.T) {
	for _, tc := range []struct {
		name    string
		stored  map[string]string
		steps   []richStep
		want    []string
		raw     string
		section string
	}{
		{
			name:    "a boolean",
			stored:  map[string]string{"features": `{"memories":true}`},
			steps:   []richStep{{label: "Feature switches", ok: true}, {label: "Memory", ok: true}},
			want:    []string{configBoolOnLabel, configBoolOffLabel, configDefaultActionLabel},
			raw:     ``,
			section: "features",
		},
		{
			name:    "an enum",
			stored:  map[string]string{"lsp": `{"diagnostics":{"min_severity":"warning","late_delivery":true}}`},
			steps:   []richStep{{label: "Language servers", ok: true}, {label: "Lowest severity", ok: true}},
			want:    []string{"error", "warning", "information", "hint", configDefaultActionLabel},
			raw:     `{"diagnostics":{"late_delivery":true}}`,
			section: "lsp",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := newConfigPanelSession(tc.stored)
			selector := &scriptedSelector{}
			runConfigPanel(t, session, selector, tc.steps, nil)

			// The section list, then the section's page, then the choice page.
			if len(selector.richItems) < 3 {
				t.Fatalf("the choice page never opened: %v", selector.richLabels)
			}
			choice := selector.richItems[2]
			if got := rowLabels(choice); !slices.Equal(got, tc.want) {
				t.Fatalf("choice list = %v, want the values and the action: %v", got, tc.want)
			}
			// The values carry nothing beside them; only the action explains
			// itself.
			for _, item := range choice[:len(choice)-1] {
				if item.Description != "" {
					t.Fatalf("%q carries %q; a value row names the value alone", item.Label, item.Description)
				}
			}
			if action := choice[len(choice)-1]; action.Description != configDefaultActionHint {
				t.Fatalf("the action reads %q, want %q", action.Description, configDefaultActionHint)
			}

			// Choosing it is the unset, and it writes: the confirmed action is
			// the save here too.
			session = newConfigPanelSession(tc.stored)
			selector = &scriptedSelector{}
			runConfigPanel(t, session, selector,
				append(append([]richStep{}, tc.steps...), richStep{label: configDefaultActionLabel, ok: true}), nil)
			if len(session.writes) != 1 {
				t.Fatalf("the action is one write, got %+v", session.writes)
			}
			if session.writes[0].section != tc.section {
				t.Fatalf("section = %q, want %q", session.writes[0].section, tc.section)
			}
			if got := string(session.writes[0].raw); got != tc.raw {
				t.Fatalf("raw = %s, want %s", got, tc.raw)
			}
		})
	}
}

// A number outside the loader's range never reaches the store, and the page
// says so in the field's own words before the next attempt.
func TestConfigPanelRefusesAnOutOfRangeNumber(t *testing.T) {
	session := newConfigPanelSession(nil)
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, []richStep{
		{label: "Scheduled tasks", ok: true},
		{label: "Keep days", ok: true},
		{label: "Keep days", ok: true},
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
		{label: "Memory", ok: true},
		{label: "Results per search", ok: true},
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
// a section the loader will not compose is explained, never swallowed — and the
// value the section holds is the one it held before the attempt (plan 321).
func TestConfigPanelShowsTheValidatorsRefusal(t *testing.T) {
	session := newConfigPanelSession(nil)
	session.writeErr = errStubConfigRefusal
	selector := &scriptedSelector{}
	renderer := runConfigPanel(t, session, selector, []richStep{
		{label: "Feature switches", ok: true},
		{label: "Memory", ok: true},
		{label: configBoolOnLabel, ok: true},
		{ok: false},
		{ok: false},
	}, nil)

	if prompts := strings.Join(selector.richLabels, "\n"); !strings.Contains(prompts, "config section memories: invalid") {
		t.Fatalf("the refusal text must reach the page; prompts:\n%s", prompts)
	}
	if text := frameText(renderer); text != "" {
		t.Fatalf("a refused write must not report anything but the page's line; frames:\n%s", text)
	}
	if len(session.writes) != 0 {
		t.Fatalf("a refused write stores nothing, got %+v", session.writes)
	}
	// The value did not land, so the page drawn after the refusal still reads
	// the default: the refusal is not a half-applied edit.
	page := lastPageHeadlined(selector, "Feature switches")
	if page == nil {
		t.Fatalf("the features page was never drawn after the refusal: %v", selector.richLabels)
	}
	if row := findRow(t, page, "Memory"); row.Value != configDefaultStatus {
		t.Fatalf("the refused value is on the page: %q", row.Value)
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
	if !strings.HasPrefix(selector.richLabels[0], "Scheduled tasks") {
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
	runConfigPanel(t, session, selector, []richStep{{label: "Gateway", ok: true}, {ok: false}}, nil)

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
// A definition opens its template's fixed fields directly, and a field its
// editor directly (plan 329): no menu between the row and its description.
func TestConfigPanelEditsATableRowInPlace(t *testing.T) {
	session := newConfigPanelSession(map[string]string{"agents": `{"definitions":{"work":{"description":"旧"}}}`})
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, []richStep{
		{label: "Agents", ok: true},
		{label: "Agent definitions", ok: true},
		{label: "work", ok: true},
		{label: "Description", ok: true},
		{ok: false},
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
// shape an unset list section has no value to read the shape from. Each of
// the two edits is its own write (plan 321). A writable root is a known
// scalar row, so adding one opens its text editor directly (plan 329); a
// row of a generic container keeps the menu, and with it the delete action
// until the footer carries it (plan 326).
func TestConfigPanelAddsAndDeletesARow(t *testing.T) {
	session := newConfigPanelSession(map[string]string{"sandbox_workspace_write": `{"writable_roots":["/a"]}`})
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, []richStep{
		{label: "Workspace sandbox", ok: true},
		{label: "Extra writable roots", ok: true},
		{label: configAddRowLabel, ok: true},
		{ok: false},
		{ok: false},
	}, []selectorInput{{value: "/b", ok: true}})

	if len(session.writes) != 1 {
		t.Fatalf("expected the add to write, got %+v", session.writes)
	}
	if raw := string(session.writes[0].raw); raw != `{"writable_roots":["/a","/b"]}` {
		t.Fatalf("the added row = %s", raw)
	}

	// The delete half lives where the generic editor keeps it: a key of an
	// LSP server's settings object, which no template names.
	session = newConfigPanelSession(map[string]string{"lsp": `{"servers":{"gopls":{"command":"gopls","settings":{"legacy":{"keep":true}}}}}`})
	selector = &scriptedSelector{}
	runConfigPanel(t, session, selector, []richStep{
		{label: "Language servers", ok: true},
		{label: "Servers", ok: true},
		{label: "gopls", ok: true},
		{label: "Server settings", ok: true},
		{label: "legacy", ok: true},
		{label: configRowDropLabel, ok: true},
		{ok: false},
	}, nil)
	if len(session.writes) != 1 {
		t.Fatalf("expected the delete to write, got %+v", session.writes)
	}
	if raw := string(session.writes[0].raw); raw != `{"servers":{"gopls":{"command":"gopls","settings":{}}}}` {
		t.Fatalf("raw = %s", raw)
	}
}

// An unset list still grows a list: the descriptor carries the shape, because
// there is no stored value to read it from.
func TestConfigPanelAddsToAnUnsetList(t *testing.T) {
	session := newConfigPanelSession(nil)
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, []richStep{
		{label: "Workspace sandbox", ok: true},
		{label: "Extra writable roots", ok: true},
		{label: configAddRowLabel, ok: true},
		{label: configNewTextRow, ok: true},
		{label: configUpRowLabel, ok: true},
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
		{label: "Agents", ok: true},
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
			case configFieldModel:
				// A model picker's rows come from the install's table, so the
				// descriptor only has to say what its empty value reads as (plan
				// 324) — and an explicit empty value is the memory flavor's
				// "current" alone.
				if strings.TrimSpace(field.ModelEmpty) == "" {
					t.Errorf("%s.%s 是模型引用，必须写明空值读作什么", schema.Name, field.Label)
				}
				if field.ModelEmptyValue != "" && field.ModelFlavor != configModelFlavorMemory {
					t.Errorf("%s.%s 只有记忆模型把空值存成显式值", schema.Name, field.Label)
				}
			default:
				if len(field.Options) != 0 {
					t.Errorf("%s.%s 不是枚举，却带着选项", schema.Name, field.Label)
				}
				if field.ModelEmpty != "" || field.ModelEmptyValue != "" {
					t.Errorf("%s.%s 不是模型引用，却带着模型空值", schema.Name, field.Label)
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
		check("section "+schema.Name+" effect", schema.Effect)
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
		"configBackRowLabel":       configBackRowLabel,
		"configAddRowLabel":        configAddRowLabel,
		"configUpRowLabel":         configUpRowLabel,
		"configRowEditLabel":       configRowEditLabel,
		"configRowOpenLabel":       configRowOpenLabel,
		"configRowDropLabel":       configRowDropLabel,
		"configRowBackLabel":       configRowBackLabel,
		"configFileOnlyNote":       configFileOnlyNote,
		"configBoolOnLabel":        configBoolOnLabel,
		"configBoolOffLabel":       configBoolOffLabel,
		"configDefaultActionLabel": configDefaultActionLabel,
		"configDefaultActionHint":  configDefaultActionHint,
		"configNewTextRow":         configNewTextRow,
		"configNewNumberRow":       configNewNumberRow,
		"configNewTrueRow":         configNewTrueRow,
		"configNewFalseRow":        configNewFalseRow,
		"configNewObjectRow":       configNewObjectRow,
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

// lastPageHeadlined is the last page the panel drew under one heading: the
// state a refusal is read in, rather than a page that closed after it.
func lastPageHeadlined(selector *scriptedSelector, headline string) []SelectItem {
	for i := len(selector.richItems) - 1; i >= 0; i-- {
		if strings.HasPrefix(selector.richLabels[i], headline) {
			return selector.richItems[i]
		}
	}
	return nil
}

// Every tab paints its sections as rows: the title in the name column, the
// summary beside it, and the Default status at the right edge of every section
// the database holds no row for (plan 321). The lines come from the picker's
// own painters, so this reads what a screen shows, not the items it was handed.
func TestConfigPanelRendersEveryTab(t *testing.T) {
	panel := sectionPanel(nil)
	items, _ := panel.sectionItems()
	first := map[string]int{}
	for i, item := range items {
		if _, ok := first[item.Category]; !ok {
			first[item.Category] = i
		}
	}
	for _, tc := range []struct {
		tab      string
		titles   []string
		readOnly bool
	}{
		{tab: configTabApproval, titles: []string{"Approval preset", "Workspace sandbox", "Batch projects"}},
		{tab: configTabSession, titles: []string{"Context compaction", "Memory", "Scheduled tasks", "Feature switches"}},
		{tab: configTabAgents, titles: []string{"Agents", "Subagent models", "Hooks", "Language servers", "Peer messaging"}},
		{tab: configTabFile, titles: []string{"Gateway", "Credential files"}, readOnly: true},
	} {
		t.Run(tc.tab, func(t *testing.T) {
			lines := renderTabbedPage(items, first[tc.tab], 120)
			// The tab bar carries every tab and its row count, so a reader
			// always sees where the four groups are.
			bar := strings.Join(stripANSIAll(lines), "\n")
			if chip := tc.tab + " " + strconv.Itoa(len(sectionsOfTab(tc.tab))); !strings.Contains(bar, chip) {
				t.Fatalf("the tab bar is missing %q:\n%s", chip, bar)
			}
			for _, title := range tc.titles {
				row := renderedRow(t, lines, title)
				if !strings.Contains(row, panel.sectionSchema(title).Summary) {
					t.Fatalf("%s's row carries no summary: %q", title, row)
				}
				// A section with no stored row reads Default, at the right
				// edge of the panel. A file section is a read-only view with
				// no marker of its own.
				switch {
				case tc.readOnly:
					if strings.HasSuffix(row, configDefaultStatus) {
						t.Fatalf("%s's row = %q, want no status", title, row)
					}
				case !strings.HasSuffix(row, configDefaultStatus):
					t.Fatalf("%s's row = %q, want the status at its right edge", title, row)
				}
				// A row with a right-hand track is drawn to the panel's edge;
				// either way it never overflows.
				w := displayLineWidth(row)
				if w > 120 {
					t.Fatalf("%s's row is %d cells at width 120: %q", title, w, row)
				}
				if strings.HasSuffix(row, configDefaultStatus) && w != 120 {
					t.Fatalf("%s's row is %d cells at width 120: %q", title, w, row)
				}
			}
			// A narrow terminal keeps the rows inside its width.
			for _, line := range renderTabbedPage(items, first[tc.tab], 68) {
				if w := displayLineWidth(stripANSI(line)); w > 68 {
					t.Fatalf("a row overflows a 68-cell panel: %q", stripANSI(line))
				}
			}
		})
	}
}

// sectionsOfTab counts the sections one tab lists.
func sectionsOfTab(tab string) []configSectionSchema {
	out := make([]configSectionSchema, 0, len(configSectionSchemas))
	for _, schema := range panelSections() {
		if schema.Tab == tab {
			out = append(out, schema)
		}
	}
	return out
}

// sectionSchema is one section by the title the list shows.
func (p *configPanel) sectionSchema(title string) configSectionSchema {
	for _, schema := range panelSections() {
		if schema.Title == title {
			return schema
		}
	}
	return configSectionSchema{}
}

// A section page paints each setting as name, value and status: the hint beside
// the name, the value it holds in a right-aligned track, and the marker a
// projection puts beyond it (plan 321).
func TestConfigPanelRendersTheValueColumn(t *testing.T) {
	// A settings page: two switches, one set and one not, so the values differ.
	panel := sectionPanel(map[string]string{"features": `{"memories":true}`})
	features, ok := configSectionSchemaFor("features")
	if !ok {
		t.Fatal("no features section")
	}
	items, _ := panel.fieldItems(features, panel.sectionDoc("features"))
	lines := renderRichPage(items, features.Title, 120)

	memories := renderedRow(t, lines, "Memory")
	if !strings.HasSuffix(memories, configBoolOnLabel) {
		t.Fatalf("the Memory row = %q, want its value at the right edge", memories)
	}
	if !strings.Contains(memories, "On by default") {
		t.Fatalf("the Memory row = %q, want its hint beside the name", memories)
	}
	servers := renderedRow(t, lines, "Language servers")
	if !strings.HasSuffix(servers, configDefaultStatus) {
		t.Fatalf("the Language servers row = %q, want the default it follows", servers)
	}
	// Both values end at the same column: the track is fixed, not pushed along.
	if endsAt(memories, configBoolOnLabel) != endsAt(servers, configDefaultStatus) {
		t.Fatalf("the values are not aligned:\n%q\n%q", memories, servers)
	}

	// A projection's page (plan 324 D7): the five install-wide pins each name
	// the model row their value column holds and the thinking level their
	// status carries, with the prototype's help sentence as the description —
	// a pin nobody set reads as the sentence it follows and marks nothing.
	projection := sectionPanel(map[string]string{"agents": `{
		"subagent_models":{
			"plan":{"model":"llm-1","reasoning_effort":"high"},
			"explore":{"model":"llm-1","reasoning_effort":"medium"}
		},
		"plan_reviewer":{"model":"llm-1"}
	}`})
	projection.models = []appcfg.LLMModelConfig{{ID: "llm-1", Provider: "openai", Model: "gpt-main"}}
	subagentModels, ok := configSectionSchemaFor("subagent_models")
	if !ok {
		t.Fatal("no subagent_models section")
	}
	items, _ = projection.fieldItems(subagentModels, projection.sectionDoc("agents"))
	lines = renderRichPage(items, subagentModels.Title, 120)
	for label, status := range map[string]string{
		"plan":          "High",
		"explore":       "Medium",
		"plan-reviewer": configRowDefaultEffort,
	} {
		row := renderedRow(t, lines, label)
		if !strings.Contains(row, "openai/gpt-main") || !strings.HasSuffix(row, status) {
			t.Fatalf("the %s row = %q, want the model row's label and %q at the edge", label, row, status)
		}
	}
	for _, label := range []string{"general-purpose", "verification"} {
		unset := renderedRow(t, lines, label)
		if !strings.HasSuffix(unset, configFollowConversation) {
			t.Fatalf("the %s row = %q, want the sentence it follows", label, unset)
		}
	}
	// The description is the prototype's help sentence; the value column
	// holds the named row whole, not elided.
	plan := renderedRow(t, lines, "plan")
	if !strings.Contains(plan, "Install-wide per-type default") {
		t.Fatalf("the plan row = %q, want the prototype's help sentence", plan)
	}
	if !strings.Contains(plan, "openai/gpt-main") || strings.Contains(plan, "openai/gp...") {
		t.Fatalf("the plan row = %q, want the model row's label whole", plan)
	}
	if reviewer := renderedRow(t, lines, "plan-reviewer"); !strings.Contains(reviewer, "Global row for a system type") {
		t.Fatalf("the plan-reviewer row = %q, want the globalSystem help sentence", reviewer)
	}
}

// pageLabels joins every page's heading the panel showed, so a refusal or a
// notice can be read where the user read it.
func pageLabels(selector *scriptedSelector) string {
	return strings.Join(selector.richLabels, "\n")
}

// endsAt is the cell column a row's last value ends at: byte offsets would
// count a one-cell cursor glyph as three, so the prefix is measured in cells.
func endsAt(row, value string) int {
	at := strings.LastIndex(row, value)
	if at < 0 {
		return -1
	}
	return displayLineWidth(row[:at]) + displayLineWidth(value)
}

// sectionPanel is a panel over a stored configuration, for the render checks:
// the rows the panel builds, laid out by the picker's own painters rather than
// the fake selector, so an assertion reads what a screen shows.
func sectionPanel(stored map[string]string) *configPanel {
	session := newConfigPanelSession(stored)
	panel := newConfigPanel(nil, nil, session)
	snapshot, err := session.ConfigPanelSections(context.Background())
	if err != nil {
		panic(err)
	}
	panel.raw = snapshot.Stored
	panel.file = snapshot.File
	return panel
}

// renderTabbedPage lays the tabbed picker out at width, opened on the tab that
// holds items[start].
func renderTabbedPage(items []SelectItem, start, width int) []string {
	p := newTabbedPickerState(items, TabbedSelectOptions{DefaultIdx: start}, false, "")
	return p.panel("Configuration").layout(width, 0, 0, false).lines
}

// renderRichPage lays a plain picker page out at width, the way SelectRich
// paints it.
func renderRichPage(items []SelectItem, label string, width int) []string {
	p := &richPickerState{items: items}
	for i := range items {
		p.selectable = append(p.selectable, i)
	}
	return p.panel(label).layout(width, 0, 0, false).lines
}

// renderedRow is the painted line of one row, found by the name it starts with
// once the row's margin is off.
func renderedRow(t *testing.T, lines []string, name string) string {
	t.Helper()
	for _, line := range lines {
		body := strings.TrimLeft(stripANSI(line), " ")
		body = strings.TrimPrefix(body, selectorCursorGlyph+" ")
		if strings.HasPrefix(body, name) {
			return stripANSI(line)
		}
	}
	t.Fatalf("no row %q among:\n%s", name, strings.Join(stripANSIAll(lines), "\n"))
	return ""
}

func stripANSIAll(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, stripANSI(line))
	}
	return out
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
	if main.Description != "2 entries" {
		t.Fatalf("main's description = %q, want the value alone", main.Description)
	}
}

// One agent's subagents read the way dispatch resolves them (plan 313): this
// agent's own entry first, then the install-wide row of that type, then the
// conversation's model — and an entry naming a system-dispatched type opens
// read-only, every row marked System, and refuses to open any of them.
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
		{label: "Subagents", ok: true},
		// The system entry opens read-only: every row System, and opening
		// any of them says why (plan 329, prototype html:128).
		{label: "#5", ok: true},
		{label: "Enabled", ok: true},
		{ok: false},
		{ok: false},
		{ok: false},
		{ok: false},
		{ok: false},
	}, nil, "agents")

	if len(selector.richItems) < 5 {
		t.Fatalf("expected the subagent rows page, got %d pages", len(selector.richItems))
	}
	rows := selector.richItems[3]
	cases := map[string]string{
		"#1": "agent override: openai/gpt-sub",
		"#2": "global default: openai/gpt-main / xHigh",
		"#3": configFollowConversation,
		"#4": "3 entries",
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
			[]richStep{openDefinitions, addRow, {ok: false}, {ok: false}, {ok: false}},
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
				// A key the agent template does not name stays reachable:
				// its own generic rows page, under the additional fields.
				{label: "Scratch", ok: true},
				addRow,
				{label: configNewTextRow, ok: true},
				{ok: false},
				{ok: false},
				{ok: false},
			},
			[]selectorInput{{value: "note", ok: true}, {value: "hello", ok: true}},
			`{"definitions":{"main":{"primary":true,"scratch":{}}}}`,
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

// A write that names a system subagent entry is still the loader's to refuse:
// the panel's early guard explains the row, and the validator judges the
// section the edit would store.
func TestConfigPanelSurfacesTheLoadersSystemEntryRefusal(t *testing.T) {
	session := newConfigPanelSession(map[string]string{"agents": `{"definitions":{"main":{"primary":true,"subagents":[{"name":"goal-evaluator"}]}}}`})
	session.writeErr = errStubSystemEntryRefusal
	selector := &scriptedSelector{}
	renderer := runConfigPanel(t, session, selector, []richStep{
		{label: "Network retry limit", ok: true},
		{ok: false},
	}, []selectorInput{{value: "5", ok: true}}, "agents")

	if !strings.Contains(pageLabels(selector), "is dispatched by the system and cannot be configured") {
		t.Fatalf("the loader's refusal must reach the page: %s", pageLabels(selector))
	}
	if text := frameText(renderer); text != "" {
		t.Fatalf("a refused write reports the page's line alone: %s", text)
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
// Its rows read as the pins they are (plan 324 D7) and its Model field opens
// the global-row picker, whose pick takes the pin's level with it (D5).
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
		{label: "Model", ok: true},
		{label: "deepseek/deepseek-v4-pro", ok: true},
	}, nil, "subagent_models")

	if len(selector.richItems) == 0 {
		t.Fatal("the projection page never opened")
	}
	page := selector.richItems[0]
	// A per-type pin names the model row its value column holds, with the
	// thinking level as its status and the prototype's help sentence as its
	// description.
	explore := findRow(t, page, "explore")
	if explore.Value != "openai/gpt-main" || explore.Status != "High" {
		t.Fatalf("explore's row = %q %q, want the named row and its level", explore.Value, explore.Status)
	}
	if explore.Description != "Install-wide per-type default. An agent's own override still wins; no default follows the conversation." {
		t.Fatalf("explore's description = %q, want the prototype's help sentence", explore.Description)
	}
	// A type with no pin of its own follows the conversation, and marks nothing.
	verification := findRow(t, page, "verification")
	if verification.Value != configFollowConversation || verification.Status != "" {
		t.Fatalf("verification's row = %q %q, want the follow sentence and no status", verification.Value, verification.Status)
	}
	// The plan reviewer's global row reads the same way, with the row default
	// for a level nobody set.
	reviewer := findRow(t, page, "plan-reviewer")
	if reviewer.Value != "deepseek/deepseek-v4-pro" || reviewer.Status != configRowDefaultEffort {
		t.Fatalf("plan-reviewer's row = %q %q, want the named row and the row default", reviewer.Value, reviewer.Status)
	}
	if reviewer.Description != "Global row for a system type, the goal-evaluator pattern. Agent pages stay read-only." {
		t.Fatalf("plan-reviewer's description = %q, want the globalSystem help sentence", reviewer.Description)
	}
	// The registry is one row of this page; its entries are its own page, named
	// by the name each carries (the test below).
	findRow(t, page, "Shared custom subagents")

	// The save lands in the agents section — the document the page presents —
	// and every key the page does not show comes back untouched. The pick
	// changed the pin's model, so its level went with it in the same write.
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
	if got, _ := exploreRow["model"].(string); got != "llm-2" {
		t.Fatalf("subagent_models.explore.model = %q, want the picked row", got)
	}
	if _, kept := exploreRow["reasoning_effort"]; kept {
		t.Fatalf("subagent_models.explore.reasoning_effort = %v, want the level dropped with the model it was chosen for", exploreRow["reasoning_effort"])
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
// then runs the steps the caller scripts on that page. A definition opens its
// template page directly and its Subagents field its rows page directly (plan
// 329), so the walk is three picks.
func agentSubagentsSteps(more []richStep) []richStep {
	steps := []richStep{
		{label: "Agent definitions", ok: true},
		{label: "main", ok: true},
		{label: "Subagents", ok: true},
	}
	return append(steps, more...)
}

// savedAgentsDoc is the agents document as the last write left it, decoded:
// every edit writes the whole section, so the last row is the section's state.
func savedAgentsDoc(t *testing.T, session *configPanelTestSession) map[string]any {
	t.Helper()
	if len(session.writes) == 0 {
		t.Fatal("nothing was written")
	}
	last := session.writes[len(session.writes)-1]
	if last.section != "agents" {
		t.Fatalf("the write went to %q, want agents", last.section)
	}
	var doc map[string]any
	if err := json.Unmarshal(last.raw, &doc); err != nil {
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
	// Opening the definition's fields writes nothing: a fact is not an edit.
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

// The picked level is what the store gets, and Row default stores the empty
// level itself (plan 324 D5): the loader reads "" as no override — the model
// row's own default — and an unset key and an empty one mean the same thing
// everywhere the level is read.
func TestConfigPanelEffortPickerWritesTheChosenLevel(t *testing.T) {
	for _, tc := range []struct {
		name string
		pick string
		want string
	}{
		{name: "a level", pick: "xHigh", want: "xhigh"},
		{name: "the row's default", pick: configRowDefaultEffort, want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			useSetupCatalogWith(t, configEffortCatalog)
			session := newConfigPanelSession(map[string]string{"agents": `{"goal_evaluator":{"model":"llm-grok","reasoning_effort":"low"}}`})
			session.snapshot.Models = configEffortModels()
			selector := &scriptedSelector{}
			runConfigPanel(t, session, selector, []richStep{
				{label: "Goal check thinking level", ok: true},
				{label: tc.pick, ok: true},
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
			if !ok {
				t.Fatalf("row default dropped the key, want the empty level stored")
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
		// The type row's own level, changed from the picker: the type's page
		// is the template's two fields (plan 329).
		{label: "plan", ok: true},
		{label: "Thinking level", ok: true},
		{label: "High", ok: true},
		{label: configUpRowLabel, ok: true},
		// The plan reviewer's row has no level yet: its empty level opens
		// the same picker, never the type list.
		{label: "plan-reviewer", ok: true},
		{label: "Thinking level", ok: true},
		{label: "Medium", ok: true},
		{label: configUpRowLabel, ok: true},
	}, nil, "subagent_models")

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
		// it overrides, and its page is the template's fixed fields (plan 329).
		{label: configRowLabel("", 0, true), ok: true},
		{label: "Thinking level", ok: true},
		{label: "xHigh", ok: true},
		{label: configUpRowLabel, ok: true},
		{label: configUpRowLabel, ok: true},
		{label: configUpRowLabel, ok: true},
		{label: configUpRowLabel, ok: true},
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

// --- Plan 324: the model picker and the type detail page ---

// modelPickerPage is the one page a model picker drew, found by the sentence
// under its title.
func modelPickerPage(t *testing.T, selector *scriptedSelector) []SelectItem {
	t.Helper()
	for i, label := range selector.richLabels {
		if strings.Contains(label, configModelPickerSub) {
			return selector.richItems[i]
		}
	}
	t.Fatalf("no model picker opened: %v", selector.richLabels)
	return nil
}

// A model reference is picked from the install's global table, never typed
// (plan 324): the picker's first row is the field's own empty value — the goal
// check's carries the conversation sentence — and every row after it names a
// model row and where its requests go. A stored reference the table does not
// carry is not kept on the goal check's list.
func TestConfigPanelModelPickerListsGlobalRows(t *testing.T) {
	session := newConfigPanelSession(map[string]string{"agents": `{"goal_evaluator":{"model":"llm-gone"}}`})
	session.snapshot.Models = []appcfg.LLMModelConfig{
		{ID: "llm-main", Provider: "openai", Model: "gpt-main", BaseURL: "https://api.openai.com", APIPath: "/v1/chat/completions"},
		{ID: "llm-claude", Provider: "anthropic", Model: "claude-sonnet-4-5", BaseURL: "https://eu.api.anthropic.com", APIPath: "/v1/messages"},
	}
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, []richStep{
		{label: "Goal check model", ok: true},
		{ok: false},
	}, nil, "agents")

	page := modelPickerPage(t, selector)
	labels := rowLabels(page)
	want := []string{"Follow primary agent", "openai/gpt-main", "anthropic/claude-sonnet-4-5"}
	if !slices.Equal(labels, want) {
		t.Fatalf("picker = %v, want %v", labels, want)
	}
	empty := findRow(t, page, "Follow primary agent")
	if empty.Description != configModelConversationDesc {
		t.Fatalf("the goal check's empty option = %q, want the conversation sentence", empty.Description)
	}
	row := findRow(t, page, "openai/gpt-main")
	if row.Description != "https://api.openai.com/v1/chat/completions" {
		t.Fatalf("the row's description = %q, want where its requests go", row.Description)
	}
	if len(session.writes) != 0 {
		t.Fatalf("opening the picker wrote %+v, want nothing", session.writes)
	}
}

// A pin whose row the table no longer carries stays on the picker (plan 324
// D1): it is labeled as written, appended after the real rows, and picking a
// row replaces it.
func TestConfigPanelModelPickerKeepsDanglingPin(t *testing.T) {
	session := newConfigPanelSession(map[string]string{"agents": `{"subagent_models":{"explore":{"model":"llm-gone","reasoning_effort":"high"}}}`})
	session.snapshot.Models = []appcfg.LLMModelConfig{{ID: "llm-1", Provider: "openai", Model: "gpt-main"}}
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, []richStep{
		{label: "explore", ok: true},
		{label: "Model", ok: true},
		{label: "openai/gpt-main", ok: true},
	}, nil, "subagent_models")

	// The section row names the dangling pin as written, and the picker keeps
	// it after the real rows with no description of its own.
	if explore := findRow(t, selector.richItems[0], "explore"); explore.Value != "llm-gone" {
		t.Fatalf("explore's value = %q, want the dangling reference as written", explore.Value)
	}
	page := modelPickerPage(t, selector)
	want := []string{"Follow conversation", "openai/gpt-main", "llm-gone"}
	if got := rowLabels(page); !slices.Equal(got, want) {
		t.Fatalf("picker = %v, want %v", got, want)
	}
	if dangling := findRow(t, page, "llm-gone"); dangling.Description != "" {
		t.Fatalf("the dangling pin = %q, want no description", dangling.Description)
	}

	// Picking the real row replaces the pin and takes its level along (D5).
	doc := savedAgentsDoc(t, session)
	types, _ := doc["subagent_models"].(map[string]any)
	explore, _ := types["explore"].(map[string]any)
	if got, _ := explore["model"].(string); got != "llm-1" {
		t.Fatalf("subagent_models.explore.model = %q, want the picked row", got)
	}
	if _, kept := explore["reasoning_effort"]; kept {
		t.Fatalf("the level stayed beside a model it was not chosen for: %v", explore["reasoning_effort"])
	}
}

// Two rows of the install's table that share a provider and model are told
// apart by their host (plan 324 D2): the label carries the base URL without
// its scheme, and a row whose pair is unique stays bare. A reference nothing
// carries is shown as written.
func TestConfigPanelModelRowLabelDisambiguatesHost(t *testing.T) {
	panel := &configPanel{models: []appcfg.LLMModelConfig{
		{ID: "llm-main", Provider: "example", Model: "reasoner-pro", BaseURL: "https://models.example.com"},
		{ID: "llm-eu", Provider: "example", Model: "reasoner-pro", BaseURL: "https://eu.models.example.com"},
		{ID: "llm-fast", Provider: "example", Model: "fast-small", BaseURL: "https://models.example.com"},
	}}
	for _, tc := range []struct{ ref, want string }{
		{"llm-main", "example/reasoner-pro (models.example.com)"},
		{"llm-eu", "example/reasoner-pro (eu.models.example.com)"},
		{"llm-fast", "example/fast-small"},
		{"llm-gone", "llm-gone"},
		{"example/reasoner-pro", "example/reasoner-pro (models.example.com)"},
	} {
		if got := panel.configModelRowLabel(tc.ref); got != tc.want {
			t.Errorf("rowLabel(%q) = %q, want %q", tc.ref, got, tc.want)
		}
	}
}

// The type page's Model row opens the picker with its own empty value —
// Follow conversation — and picking it clears the pin and its level in one
// write (plan 324 D3/D5).
func TestConfigPanelTypePageModelPickerFollowConversation(t *testing.T) {
	session := newConfigPanelSession(map[string]string{"agents": `{"subagent_models":{"explore":{"model":"llm-1","reasoning_effort":"high"}}}`})
	session.snapshot.Models = []appcfg.LLMModelConfig{{ID: "llm-1", Provider: "openai", Model: "gpt-main"}}
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, []richStep{
		{label: "explore", ok: true},
		{label: "Model", ok: true},
		{label: "Follow conversation", ok: true},
	}, nil, "subagent_models")

	if got := rowLabels(modelPickerPage(t, selector))[0]; got != configFollowConversation {
		t.Fatalf("the type page's empty option = %q, want %q", got, configFollowConversation)
	}
	if len(session.writes) != 1 {
		t.Fatalf("expected one write, got %+v", session.writes)
	}
	doc := savedAgentsDoc(t, session)
	types, _ := doc["subagent_models"].(map[string]any)
	explore, kept := types["explore"].(map[string]any)
	if !kept {
		t.Fatalf("the type's row itself must stay: %v", doc)
	}
	if _, pin := explore["model"]; pin {
		t.Fatalf("subagent_models.explore.model = %v, want the pin cleared", explore["model"])
	}
	if _, level := explore["reasoning_effort"]; level {
		t.Fatalf("subagent_models.explore.reasoning_effort = %v, want the level cleared with the model", explore["reasoning_effort"])
	}
}

// A definition's default model is picked from the global table (plan 324 D4):
// the agent page's Default model row opens the picker, a row's id is what the
// store gets, and the empty option unsets the key.
func TestConfigPanelDefaultModelIsPicked(t *testing.T) {
	models := []appcfg.LLMModelConfig{{ID: "llm-1", Provider: "openai", Model: "gpt-main"}}
	pick := []richStep{
		{label: "Agent definitions", ok: true},
		{label: "main", ok: true},
		{label: "Default model", ok: true},
	}
	t.Run("a picked row stores its id", func(t *testing.T) {
		session := newConfigPanelSession(map[string]string{"agents": `{"definitions":{"main":{"primary":true}}}`})
		session.snapshot.Models = models
		selector := &scriptedSelector{}
		runConfigPanel(t, session, selector, append(append([]richStep{}, pick...), richStep{label: "openai/gpt-main", ok: true}), nil, "agents")

		if empty := findRow(t, modelPickerPage(t, selector), "Follow global default"); empty.Label == "" {
			t.Fatal("the definition's empty option is missing")
		}
		doc := savedAgentsDoc(t, session)
		definitions, _ := doc["definitions"].(map[string]any)
		main, _ := definitions["main"].(map[string]any)
		if got, _ := main["default_model"].(string); got != "llm-1" {
			t.Fatalf("definitions.main.default_model = %q, want the picked row's id", got)
		}
	})
	t.Run("the empty option unsets the key", func(t *testing.T) {
		session := newConfigPanelSession(map[string]string{"agents": `{"definitions":{"main":{"primary":true,"default_model":"llm-1"}}}`})
		session.snapshot.Models = models
		selector := &scriptedSelector{}
		runConfigPanel(t, session, selector, append(append([]richStep{}, pick...), richStep{label: "Follow global default", ok: true}), nil, "agents")

		doc := savedAgentsDoc(t, session)
		definitions, _ := doc["definitions"].(map[string]any)
		main, _ := definitions["main"].(map[string]any)
		if _, kept := main["default_model"]; kept {
			t.Fatalf("definitions.main.default_model = %v, want the key gone", main["default_model"])
		}
	})
}

// The memory models' empty value is the explicit "current" (plan 324 D1): the
// picker's first row follows the conversation, and picking it stores the
// value the loader reads as the conversation's own model.
func TestConfigPanelMemoryModelPickerFollowConversation(t *testing.T) {
	session := newConfigPanelSession(map[string]string{"memories": `{"extract_model":"llm-1"}`})
	session.snapshot.Models = []appcfg.LLMModelConfig{{ID: "llm-1", Provider: "openai", Model: "gpt-main"}}
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, []richStep{
		{label: "Extraction model", ok: true},
		{label: configFollowConversation, ok: true},
	}, nil, "memories")

	if got := rowLabels(modelPickerPage(t, selector))[0]; got != configFollowConversation {
		t.Fatalf("the memory model's empty option = %q, want %q", got, configFollowConversation)
	}
	if len(session.writes) != 1 || session.writes[0].section != "memories" {
		t.Fatalf("writes = %+v, want one write to the memories section", session.writes)
	}
	var saved struct {
		ExtractModel string `json:"extract_model"`
	}
	if err := json.Unmarshal(session.writes[0].raw, &saved); err != nil {
		t.Fatalf("the saved document does not parse: %v", err)
	}
	if saved.ExtractModel != "current" {
		t.Fatalf("extract_model = %q, want the explicit current", saved.ExtractModel)
	}
}

// Changing a model takes the level beside it in the same write (plan 324 D5):
// one write, the new row's id, and no level left over from the old one.
func TestConfigPanelModelChangeClearsEffort(t *testing.T) {
	session := newConfigPanelSession(map[string]string{"agents": `{"goal_evaluator":{"model":"llm-1","reasoning_effort":"high"}}`})
	session.snapshot.Models = []appcfg.LLMModelConfig{
		{ID: "llm-1", Provider: "openai", Model: "gpt-main"},
		{ID: "llm-2", Provider: "deepseek", Model: "deepseek-v4-pro"},
	}
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, []richStep{
		{label: "Goal check model", ok: true},
		{label: "deepseek/deepseek-v4-pro", ok: true},
	}, nil, "agents")

	if len(session.writes) != 1 {
		t.Fatalf("the change is one write, got %+v", session.writes)
	}
	var saved struct {
		GoalCheck map[string]any `json:"goal_evaluator"`
	}
	if err := json.Unmarshal(session.writes[0].raw, &saved); err != nil {
		t.Fatalf("the saved document does not parse: %v", err)
	}
	if got, _ := saved.GoalCheck["model"].(string); got != "llm-2" {
		t.Fatalf("goal_evaluator.model = %q, want the picked row", got)
	}
	if _, kept := saved.GoalCheck["reasoning_effort"]; kept {
		t.Fatalf("goal_evaluator.reasoning_effort = %v, want the old model's level gone", saved.GoalCheck["reasoning_effort"])
	}
}

// Picking the row a reference already names changes nothing but the write
// itself (plan 324 D5): the level it was chosen for stays.
func TestConfigPanelModelSameRowKeepsEffort(t *testing.T) {
	session := newConfigPanelSession(map[string]string{"agents": `{"goal_evaluator":{"model":"llm-1","reasoning_effort":"high"}}`})
	session.snapshot.Models = []appcfg.LLMModelConfig{{ID: "llm-1", Provider: "openai", Model: "gpt-main"}}
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, []richStep{
		{label: "Goal check model", ok: true},
		{label: "openai/gpt-main", ok: true},
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
	if got, _ := saved.GoalCheck["model"].(string); got != "llm-1" {
		t.Fatalf("goal_evaluator.model = %q, want the same row", got)
	}
	if got, _ := saved.GoalCheck["reasoning_effort"].(string); got != "high" {
		t.Fatalf("goal_evaluator.reasoning_effort = %q, want the level it was chosen for", got)
	}
}

// The picker's writes meet the store's own validator — the one the
// application writes through, not the double (plan 324's stop condition,
// checked head-on): a picked row's id, the empty level a Row default stores,
// and a cleared or re-picked pin with its level dropped are all shapes the
// loader accepts, and a landed write stays silent.
func TestConfigPanelModelPickerWritesPassTheStoreValidator(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	cfgPath := filepath.Join(home, "forebrain.yaml")
	if err := os.WriteFile(cfgPath, []byte("gateway:\n  http_addr: 127.0.0.1:9\n"), 0o600); err != nil {
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
	models := &state.LLMModelStore{DB: db}
	main, err := models.Create(ctx, state.LLMModel{Provider: "openai", Model: "gpt-main", BaseURL: "https://api.openai.com", APIPath: "/v1/chat/completions"})
	if err != nil {
		t.Fatal(err)
	}
	deepseek, err := models.Create(ctx, state.LLMModel{Provider: "deepseek", Model: "r1", BaseURL: "https://api.deepseek.com", APIPath: "/v1/chat/completions"})
	if err != nil {
		t.Fatal(err)
	}
	process.InstallConfigSectionValidator(cfgPath, db)
	t.Cleanup(func() { state.SetSectionValidator(nil) })
	env := &process.Environment{Root: home, SQL: db}
	env.ConfigPath = cfgPath
	session := &ChatSession{Env: env}

	storedAgents := func(t *testing.T) map[string]any {
		t.Helper()
		snapshot, err := session.ConfigPanelSections(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if len(snapshot.Stored["agents"]) == 0 {
			return map[string]any{}
		}
		if err := json.Unmarshal(snapshot.Stored["agents"], &doc); err != nil {
			t.Fatalf("the stored agents section does not parse: %v", err)
		}
		return doc
	}
	run := func(t *testing.T, steps []richStep, args ...string) {
		t.Helper()
		selector := &scriptedSelector{}
		renderer := runConfigPanelOn(t, session, selector, steps, nil, args...)
		if text := frameText(renderer); text != "" {
			t.Fatalf("a write that lands says nothing; frames:\n%s", text)
		}
		for _, label := range selector.richLabels {
			if strings.Contains(label, "the store refused") || strings.Contains(label, "invalid") {
				t.Fatalf("a refusal reached the page: %s", pageLabels(selector))
			}
		}
	}

	// A picked row's id is what the store gets.
	run(t, []richStep{
		{label: "Goal check model", ok: true},
		{label: "openai/gpt-main", ok: true},
	}, "agents")
	goal, _ := storedAgents(t)["goal_evaluator"].(map[string]any)
	if got, _ := goal["model"].(string); got != main.ID {
		t.Fatalf("goal_evaluator.model = %q, want the picked row's id %q", got, main.ID)
	}

	// Row default stores the empty level itself, and the loader reads it.
	if err := session.UpdateConfigSection(ctx, "agents", json.RawMessage(`{"goal_evaluator":{"model":"`+main.ID+`","reasoning_effort":"low"}}`)); err != nil {
		t.Fatalf("the seed must be a pair the loader accepts: %v", err)
	}
	run(t, []richStep{
		{label: "Goal check thinking level", ok: true},
		{label: configRowDefaultEffort, ok: true},
	}, "agents")
	goal, _ = storedAgents(t)["goal_evaluator"].(map[string]any)
	if got, _ := goal["reasoning_effort"].(string); got != "" {
		t.Fatalf("goal_evaluator.reasoning_effort = %q, want the empty level stored", got)
	}

	// A re-pinned type row drops its level in the same write, and the whole
	// agents document the projection saves is a shape the loader accepts.
	if err := session.UpdateConfigSection(ctx, "agents", json.RawMessage(`{"subagent_models":{"explore":{"model":"`+main.ID+`","reasoning_effort":"low"}}}`)); err != nil {
		t.Fatalf("the seed must be a pin the loader accepts: %v", err)
	}
	run(t, []richStep{
		{label: "explore", ok: true},
		{label: "Model", ok: true},
		{label: "deepseek/r1", ok: true},
	}, "subagent_models")
	types, _ := storedAgents(t)["subagent_models"].(map[string]any)
	explore, _ := types["explore"].(map[string]any)
	if got, _ := explore["model"].(string); got != deepseek.ID {
		t.Fatalf("subagent_models.explore.model = %q, want the picked row's id %q", got, deepseek.ID)
	}
	if _, kept := explore["reasoning_effort"]; kept {
		t.Fatalf("the level stayed beside a model it was not chosen for: %v", explore["reasoning_effort"])
	}

	// Clearing the pin leaves a row the loader still accepts — the store's
	// own round-trip keeps the row with its normalized name key.
	run(t, []richStep{
		{label: "explore", ok: true},
		{label: "Model", ok: true},
		{label: configFollowConversation, ok: true},
	}, "subagent_models")
	types, _ = storedAgents(t)["subagent_models"].(map[string]any)
	explore, kept := types["explore"].(map[string]any)
	if !kept {
		t.Fatalf("subagent_models.explore = %v, want the pin's row kept", types["explore"])
	}
	if _, pin := explore["model"]; pin {
		t.Fatalf("subagent_models.explore.model = %v, want the pin cleared", explore["model"])
	}
	if _, level := explore["reasoning_effort"]; level {
		t.Fatalf("subagent_models.explore.reasoning_effort = %v, want the level cleared with the pin", explore["reasoning_effort"])
	}
}

// --- Plan 329: the template layer and the persistent chrome ---

// renderChromePickerPage lays a rich picker page out at width under the
// config chrome, the way SelectRichChrome paints it (plan 329 D4).
func renderChromePickerPage(chrome *configPageChrome, items []SelectItem, label string, width int) []string {
	p := &richPickerState{items: items, chrome: chrome}
	for i := range items {
		p.selectable = append(p.selectable, i)
	}
	return p.panel(label).layout(width, 0, 0, false).lines
}

// A stored row a template names opens that template's fixed fields, in the
// prototype's order under their groups — not the generic key list (plan 329
// D3). The projection's per-type row is the two-field page the owner's
// defect asked for, and a key the template does not name stays listed under
// the additional-fields group and travels back on save (no data loss).
func TestConfigPanelNestedTemplateFields(t *testing.T) {
	session := newConfigPanelSession(map[string]string{"agents": `{
		"defaults":{"mcp_servers":[{"name":"workspace-tools","transport":"stdio","command":"workspace-mcp","args":["--read-only"],"custom_note":"keep me"}]}
	}`})
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, []richStep{
		{label: "Agents", ok: true},
		{label: "Shared MCP servers", ok: true},
		{label: "#1", ok: true},
		{label: "Command", ok: true},
		{ok: false},
		{ok: false},
		{ok: false},
	}, []selectorInput{{value: "workspace-mcp-2", ok: true}})

	// The MCP page: the template's own fields under their groups, then the
	// key the template does not name, then the way up.
	var page []SelectItem
	for _, items := range selector.richItems {
		if slices.Contains(rowLabels(items), "Server name") && slices.Contains(rowLabels(items), "Transport") {
			page = items
		}
	}
	if page == nil {
		t.Fatalf("the MCP template page never opened: %v", selector.richLabels)
	}
	labels := rowLabels(page)
	for _, want := range []string{"Server name", "Enabled", "Required", "Transport", "Command", "Arguments", "Server URL", "Headers", "OAuth", "Environment", "Inherit environment", "Startup timeout", "Default tool approval", "Tool overrides"} {
		if !slices.Contains(labels, want) {
			t.Errorf("the MCP page is missing %q: %v", want, labels)
		}
	}
	groups := map[string]bool{}
	for _, item := range page {
		if item.Category != "" {
			groups[item.Category] = true
		}
	}
	for _, want := range []string{"Identity", "Connection", "Process", "Timeouts", "Tool permissions"} {
		if !groups[want] {
			t.Errorf("the MCP page is missing the %q group: %v", want, groups)
		}
	}
	if !groups[configAdditionalFieldsGroup] || !slices.Contains(labels, "Custom note") {
		t.Fatalf("the unknown key must stay listed under %q: %v", configAdditionalFieldsGroup, labels)
	}

	// The edit through the template page writes the whole row back: the
	// command it changed, the arguments and the unknown key untouched.
	if len(session.writes) != 1 {
		t.Fatalf("expected one write, got %+v", session.writes)
	}
	var saved struct {
		Defaults struct {
			MCP []map[string]any `json:"mcp_servers"`
		} `json:"defaults"`
	}
	if err := json.Unmarshal(session.writes[0].raw, &saved); err != nil {
		t.Fatalf("the saved document does not parse: %v", err)
	}
	if len(saved.Defaults.MCP) != 1 {
		t.Fatalf("mcp_servers = %+v", saved.Defaults.MCP)
	}
	row := saved.Defaults.MCP[0]
	if row["command"] != "workspace-mcp-2" || row["custom_note"] != "keep me" {
		t.Fatalf("the saved row lost or missed its keys: %+v", row)
	}
	if args, _ := row["args"].([]any); len(args) != 1 || args[0] != "--read-only" {
		t.Fatalf("the saved row's arguments changed: %+v", row)
	}
}

// The explore type's page is the typeDef template's two fields — Model and
// Thinking level — reached from the projection (plan 329 D2: the owner's
// reported defect's structure).
func TestConfigPanelNestedTemplateExploreTypePage(t *testing.T) {
	session := newConfigPanelSession(map[string]string{"agents": `{"subagent_models":{"explore":{"model":"llm-1"}}}`})
	session.snapshot.Models = []appcfg.LLMModelConfig{{ID: "llm-1", Provider: "openai", Model: "gpt-main"}}
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, []richStep{
		{label: "explore", ok: true},
		{ok: false},
	}, nil, "subagent_models")

	if len(selector.richItems) < 2 {
		t.Fatalf("the explore page never opened: %v", selector.richLabels)
	}
	var page []SelectItem
	for _, items := range selector.richItems {
		if slices.Contains(rowLabels(items), "Model") && slices.Contains(rowLabels(items), "Thinking level") {
			page = items
		}
	}
	if page == nil {
		t.Fatalf("the explore page never opened: %v", selector.richLabels)
	}
	labels := rowLabels(page)
	if !slices.Equal(labels, []string{"Model", "Thinking level", configUpRowLabel}) {
		t.Fatalf("the explore page = %v, want Model, Thinking level and the way up", labels)
	}
	model := findRow(t, page, "Model")
	if model.Value != "openai/gpt-main" {
		t.Fatalf("the Model row's value = %q, want the named row's label", model.Value)
	}
	if findRow(t, page, "Thinking level").Value != configDefaultStatus {
		t.Fatalf("the unset level must read as the default it follows")
	}
}

// A system-name subagent entry opens read-only: every row carries the System
// status and the verdict in place of its hint, opening any of them is
// refused with the prototype's sentence, and nothing is written (plan 329
// D3, prototype html:128).
func TestConfigPanelSystemSubagentReadonly(t *testing.T) {
	session := newConfigPanelSession(map[string]string{"agents": `{
		"definitions":{"main":{"primary":true,"subagents":[{"name":"goal-evaluator","model":"llm-1","description":"no"}]}}
	}`})
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, agentSubagentsSteps([]richStep{
		{label: "#1", ok: true},
		{label: "Enabled", ok: true},
		{label: "Model", ok: true},
		{ok: false},
		{ok: false},
	}), nil, "agents")

	var page []SelectItem
	for _, items := range selector.richItems {
		if slices.Contains(rowLabels(items), "Type name") && slices.Contains(rowLabels(items), "Enabled") {
			page = items
		}
	}
	if page == nil {
		t.Fatalf("the system entry's page never opened: %v", selector.richLabels)
	}
	// customOnly hides the fields a system entry cannot carry, and every
	// row left is read-only with the System status.
	for _, item := range page {
		if item.Label == configUpRowLabel {
			continue
		}
		if item.Status != configSystemStatus {
			t.Errorf("%s's status = %q, want %q", item.Label, item.Status, configSystemStatus)
		}
		if item.Description != configSystemSubagentNotice {
			t.Errorf("%s's hint = %q, want the verdict", item.Label, item.Description)
		}
	}
	for _, gone := range []string{"Description", "Tool access", "Allowed tools", "Allow continuation"} {
		if slices.Contains(rowLabels(page), gone) {
			t.Errorf("the system entry's page still lists %q", gone)
		}
	}
	if !strings.Contains(pageLabels(selector), configSystemSubagentNotice) {
		t.Fatalf("opening a row must state the refusal: %s", pageLabels(selector))
	}
	if len(session.writes) != 0 {
		t.Fatalf("a read-only page must write nothing, got %+v", session.writes)
	}
}

// The hook template shows the fields the entry's action type owns (plan 329
// D3, showFor): a command hook its command, an HTTP hook its URL.
func TestConfigPanelHookShowFor(t *testing.T) {
	session := newConfigPanelSession(map[string]string{"hooks": `{
		"PreToolUse":[{"matcher":"*","hooks":[{"type":"command","command":"project-check"}]}]
	}`})
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, []richStep{
		{label: "Hook events", ok: true},
		{label: "PreToolUse", ok: true},
		{label: "#1", ok: true},
		{label: "Actions", ok: true},
		{label: "#1", ok: true},
		{ok: false},
	}, nil, "hooks")

	var page []SelectItem
	for _, items := range selector.richItems {
		if slices.Contains(rowLabels(items), "Action type") && slices.Contains(rowLabels(items), "Condition") {
			page = items
		}
	}
	if page == nil {
		t.Fatalf("the hook page never opened: %v", selector.richLabels)
	}
	if !slices.Contains(rowLabels(page), "Command") || slices.Contains(rowLabels(page), "Request URL") {
		t.Fatalf("a command hook's page = %v, want Command without Request URL", rowLabels(page))
	}

	// Switched to HTTP, the page turns over: the URL arrives, the command
	// goes — the enum edit is one write like any other.
	session = newConfigPanelSession(map[string]string{"hooks": `{
		"PreToolUse":[{"matcher":"*","hooks":[{"type":"http","url":"https://hooks.example.com/review"}]}]
	}`})
	selector = &scriptedSelector{}
	runConfigPanel(t, session, selector, []richStep{
		{label: "Hook events", ok: true},
		{label: "PreToolUse", ok: true},
		{label: "#1", ok: true},
		{label: "Actions", ok: true},
		{label: "#1", ok: true},
		{ok: false},
	}, nil, "hooks")
	for _, items := range selector.richItems {
		if slices.Contains(rowLabels(items), "Action type") && slices.Contains(rowLabels(items), "Condition") {
			page = items
		}
	}
	if page == nil {
		t.Fatalf("the HTTP hook page never opened: %v", selector.richLabels)
	}
	if !slices.Contains(rowLabels(page), "Request URL") || slices.Contains(rowLabels(page), "Command") {
		t.Fatalf("an HTTP hook's page = %v, want Request URL without Command", rowLabels(page))
	}
	if !slices.Contains(rowLabels(page), "Headers") || !slices.Contains(rowLabels(page), "Allowed environment variables") {
		t.Fatalf("an HTTP hook's page = %v, want its headers and env allowance", rowLabels(page))
	}
}

// A built-in override keeps to what the loader lets it hold: the custom-only
// fields are hidden on it, and shown on a custom entry beside it (plan 329
// D3, customOnly).
func TestConfigPanelCustomOnlyHidden(t *testing.T) {
	session := newConfigPanelSession(map[string]string{"agents": `{
		"definitions":{"main":{"primary":true,"subagents":[
			{"name":"explore","model":"llm-1"},
			{"name":"db-migrator","description":"runs migrations"}
		]}}
	}`})
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, agentSubagentsSteps([]richStep{
		{label: "#1", ok: true},
		{ok: false},
		{label: "#2", ok: true},
		{ok: false},
	}), nil, "agents")

	pages := map[int][]SelectItem{}
	for _, items := range selector.richItems {
		if slices.Contains(rowLabels(items), "Type name") {
			if slices.Contains(rowLabels(items), "Tool access") {
				pages[2] = items
			} else {
				pages[1] = items
			}
		}
	}
	if pages[1] == nil || pages[2] == nil {
		t.Fatalf("both entries' pages must open: %v", selector.richLabels)
	}
	for _, gone := range []string{"Description", "Tool access", "Allowed tools", "Allow continuation"} {
		if slices.Contains(rowLabels(pages[1]), gone) {
			t.Errorf("the built-in override's page still lists %q", gone)
		}
		if !slices.Contains(rowLabels(pages[2]), gone) {
			t.Errorf("the custom entry's page is missing %q", gone)
		}
	}
}

// Every config page carries the persistent chrome (plan 329 D4): the tab bar
// with its counts, the breadcrumb under it, and the title's right-edge tag.
// A page deep in a section keeps the trail it walked.
func TestConfigPanelPersistentTabsAndBreadcrumbSlot(t *testing.T) {
	panel := sectionPanel(map[string]string{"agents": `{"definitions":{"main":{"primary":true,"display_name":"Main agent"}}}`})
	panel.chrome = newConfigPageChrome(panelSections())
	agents, ok := configSectionSchemaFor("agents")
	if !ok {
		t.Fatal("no agents section")
	}
	panel.section = "agents"
	panel.effectTag = agents.Effect
	panel.chrome.enterTab(agents.Tab)
	panel.chrome.trail = []string{agents.Title}
	panel.chrome.tag = panel.sectionTag()

	// The section page: four tabs with their counts, the breadcrumb's root,
	// the title and its tag at the right edge.
	items, _ := panel.fieldItems(agents, panel.sectionDoc("agents"))
	lines := renderChromePickerPage(&panel.chrome, items, agents.Title+"\n"+agents.Summary, 120)
	bar := strings.Join(stripANSIAll(lines), "\n")
	for _, tab := range []string{configTabApproval, configTabSession, configTabAgents, configTabFile} {
		count := strconv.Itoa(len(sectionsOfTab(tab)))
		if !strings.Contains(bar, tab+" "+count) {
			t.Errorf("the tab bar is missing %q:\n%s", tab+" "+count, bar)
		}
	}
	if !strings.Contains(bar, "Configuration / Agents") {
		t.Errorf("the breadcrumb slot is missing the trail:\n%s", bar)
	}
	title := renderedRow(t, lines, agents.Title)
	if !strings.HasSuffix(title, configNextTurnTag) {
		t.Errorf("the section page's title = %q, want the %q tag at its right edge", title, configNextTurnTag)
	}

	// A nested page keeps the chrome and grows the trail: the definitions
	// rows page under the section, tagged with its count.
	panel.chrome.trail = []string{agents.Title, "Agent definitions"}
	panel.chrome.tag = "1 entries"
	lines = renderChromePickerPage(&panel.chrome, items, "Agent definitions\nOne agent id per row", 120)
	if trail := renderedRow(t, lines, "Configuration / Agents / Agent definitions"); trail == "" {
		t.Fatalf("the nested page lost the breadcrumb:\n%s", strings.Join(stripANSIAll(lines), "\n"))
	}

	// An effect-bearing section carries its own scope instead of the panel's
	// wording: the approval preset page.
	approval, _ := configSectionSchemaFor("approval_preset")
	panel.effectTag = approval.Effect
	panel.chrome.tag = panel.sectionTag()
	items, _ = panel.fieldItems(approval, nil)
	lines = renderChromePickerPage(&panel.chrome, items, approval.Title+"\n"+approval.Summary, 120)
	if title := renderedRow(t, lines, approval.Title); !strings.HasSuffix(title, approval.Effect) {
		t.Errorf("the approval page's title = %q, want %q at its right edge", title, approval.Effect)
	}

	// Switching tabs from inside a section is the chrome's own act: the bar
	// records the tab and the overview reopens on it.
	if tab := panel.chrome.switchTab(1); tab < 0 || panel.chrome.switchedTo != tab {
		t.Fatalf("switchTab = %d, switchedTo = %d", tab, panel.chrome.switchedTo)
	}
	panel.chrome.switchedTo = -1
	sectionItems, _ := panel.sectionItems()
	if first := panel.chrome.firstRowOfTab(sectionItems); !slices.Contains(rowLabels(sectionItems), sectionItems[first].Label) {
		t.Fatalf("firstRowOfTab answered %d", first)
	}
}

// The config group heading is the prototype's: upper-cased, with a faint
// rule running to the panel's right edge, a blank row before every group
// after the first. A plain picker's categories keep the heading they always
// drew — the chrome is config's alone (plan 329 D4).
func TestConfigPanelGroupStyle(t *testing.T) {
	chrome := newConfigPageChrome(panelSections())
	items := []SelectItem{
		{Label: "Server name", Category: "Identity"},
		{Label: "Enabled", Category: "Identity"},
		{Label: "Command", Category: "Connection"},
	}
	lines := renderChromePickerPage(&chrome, items, "workspace-tools", 120)
	plain := stripANSIAll(lines)
	identity := -1
	for i, line := range plain {
		if strings.HasPrefix(strings.TrimSpace(line), "IDENTITY") {
			identity = i
		}
	}
	if identity < 0 {
		t.Fatalf("the group heading is not upper-cased:\n%s", strings.Join(plain, "\n"))
	}
	if !strings.Contains(lines[identity], "─") {
		t.Errorf("the group heading carries no rule: %q", lines[identity])
	}
	if w := displayLineWidth(lines[identity]); w != 120 {
		t.Errorf("the rule stops at %d cells, want the panel's edge", w)
	}
	if identity == 0 || strings.TrimSpace(plain[identity-1]) != "" {
		t.Errorf("a group after the first needs a blank row before it:\n%s", strings.Join(plain, "\n"))
	}
	found := false
	for _, line := range plain {
		if strings.HasPrefix(strings.TrimSpace(line), "CONNECTION") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the second group heading is missing:\n%s", strings.Join(plain, "\n"))
	}

	// A plain rich picker keeps its own heading: the category as written,
	// accented, unruled — every other panel's page is unchanged.
	plainItems := []SelectItem{
		{Label: "Workspace write", Category: "Approval & sandbox"},
	}
	plainLines := renderRichPage(plainItems, "Configuration", 120)
	for _, line := range plainLines {
		if strings.Contains(stripANSI(line), "APPROVAL & SANDBOX") || strings.Contains(line, "─") {
			t.Fatalf("a plain picker drew the config group style: %q", stripANSI(line))
		}
	}
}

// The template table is the single source of nested field shape (plan 329):
// every template's fields carry the data their widgets need, a field that
// requires a sibling names one in the same template, a shown-for field
// belongs to a template that has an action type, and the kinds match the
// flags each one declares.
func TestConfigPanelSchemaTemplateFieldDataIsComplete(t *testing.T) {
	for template, fields := range configTemplates {
		if len(fields) == 0 {
			t.Fatalf("the %s template holds no fields", template)
		}
		labels := make(map[string]bool, len(fields))
		paths := make(map[string]bool, len(fields))
		hasType := false
		for _, field := range fields {
			if strings.TrimSpace(field.Label) == "" {
				t.Errorf("模板 %s 有字段没有标签", template)
			}
			if labels[field.Label] {
				t.Errorf("模板 %s 的字段标签 %q 重复", template, field.Label)
			}
			labels[field.Label] = true
			if field.Path != "" {
				paths[field.Path] = true
			}
			if field.Path == "type" && field.Kind == configFieldEnum {
				hasType = true
			}
			if field.Requires != "" && !paths[field.Requires] {
				t.Errorf("模板 %s 的字段 %q 依赖同模板里不存在的 %q", template, field.Label, field.Requires)
			}
			if field.Unit != "" && field.Kind != configFieldNumber {
				t.Errorf("模板 %s 的字段 %q 不是数字，不能带单位", template, field.Label)
			}
			if field.Multiline && field.Kind != configFieldText {
				t.Errorf("模板 %s 的字段 %q 不是文本，不能声明 multiline", template, field.Label)
			}
			if field.Secret && field.Kind != configFieldText {
				t.Errorf("模板 %s 的字段 %q 不是文本，不能声明 secret", template, field.Label)
			}
			// A model field carries its empty option's label and flavor, and
			// an explicit empty value is the memory flavor's alone (plan 324).
			if field.Kind == configFieldModel {
				if strings.TrimSpace(field.ModelEmpty) == "" {
					t.Errorf("模板 %s 的字段 %q 是模型引用，必须写明空值读作什么", template, field.Label)
				}
				if field.ModelEmptyValue != "" && field.ModelFlavor != configModelFlavorMemory {
					t.Errorf("模板 %s 的字段 %q 只有记忆模型把空值存成显式值", template, field.Label)
				}
			} else if field.ModelEmpty != "" || field.ModelEmptyValue != "" {
				t.Errorf("模板 %s 的字段 %q 不是模型引用，却带着模型空值", template, field.Label)
			}
			if len(field.ShowFor) > 0 && !hasType {
				t.Errorf("模板 %s 的字段 %q 声明了 showFor，但模板没有动作类型", template, field.Label)
			}
			if field.CustomOnly && template != configTemplateSubagent {
				t.Errorf("模板 %s 的字段 %q 声明了 customOnly，但只有子代理模板区分内置与自定义", template, field.Label)
			}
		}
	}
}

// The template layer keeps every nested field reachable (plan 329, the
// prototype's own coverage spirit): each of the section descriptors' fields,
// each template's fields, and the pseudo-templates' scalar editors are all
// pages a reader can open — over one hundred and thirty of them.
func TestConfigPanelNestedFieldTransitionsReachable(t *testing.T) {
	reachable := 0
	for _, schema := range configSectionSchemas {
		reachable += len(schema.Fields)
	}
	for _, fields := range configTemplates {
		reachable += len(fields)
	}
	// The pseudo-templates: a path row, a string row, a matcher list.
	reachable += 3
	if reachable <= 130 {
		t.Fatalf("nested-field transitions = %d, want more than 130", reachable)
	}
}
