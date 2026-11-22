package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	return s.snapshot, nil
}

func (s *configPanelTestSession) UpdateConfigSection(_ context.Context, section string, raw json.RawMessage) error {
	if s.writeErr != nil {
		return s.writeErr
	}
	s.writes = append(s.writes, configPanelWrite{section: section, raw: append(json.RawMessage(nil), raw...)})
	return nil
}

// newConfigPanelSession builds a panel double holding the stored sections given as
// section name → its JSON.
func newConfigPanelSession(stored map[string]string) *configPanelTestSession {
	session := &configPanelTestSession{
		fakeSession: &fakeSession{},
		snapshot:    configPanelSnapshot{Stored: map[string]json.RawMessage{}},
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
	scripted := selector.(*scriptedSelector)
	scripted.richSteps = steps
	scripted.inputs = inputs
	var out bytes.Buffer
	renderer := NewRenderer(&out, &out)
	ctrl := newCommandController(session, renderer, selector, nil, "")
	ctrl.handleConfig(context.Background(), args)
	return renderer
}

// actionPanelSelector is a scripted selector that carries the footer-action
// capability (plan 326): the panel's interactive path opens through it, so a
// test drives footer actions the way a keyboard does. Plain pages still play
// through the scripted rich steps.
type actionPanelSelector struct {
	*scriptedSelector
	actionSteps    []actionStep
	actionIdx      int
	inputSteps     []inputActionStep
	inputIdx       int
	labels         []string
	items          [][]SelectItem
	defaults       []int
	actionDefaults []int
	opts           []ActionOptions
	inputLabels    []string
	inputDefaults  []string
	inputOpts      []ActionOptions
	// secretLabels and secretDefaults record the private editor's asks
	// (plan 328 D3): the label it opened under and the initial value it
	// was handed, which must be empty — a replacement is asked for, never
	// the stored secret.
	secretLabels   []string
	secretDefaults []string
}

// An action step answers a footer action when its kind is set, and a row
// otherwise — by label when set, else by index. tabStep is the arrow a page
// answered with instead of a row (the /config panel's tab switch).
type actionStep struct {
	action  FooterActionKind
	row     int
	label   string
	ok      bool
	err     error
	tabStep int
}

// An input step answers an action-bearing editor: the action when its kind
// is set, the typed text otherwise.
type inputActionStep struct {
	action FooterActionKind
	value  string
	ok     bool
	err    error
}

// SelectRich records the row the caller preselected, so a test can read
// which row a confirmation opens on.
func (s *actionPanelSelector) SelectRich(label string, items []SelectItem, defaultIdx int) (int, bool, error) {
	s.defaults = append(s.defaults, defaultIdx)
	return s.scriptedSelector.SelectRich(label, items, defaultIdx)
}

func (s *actionPanelSelector) SelectRichActions(label string, items []SelectItem, defaultIdx int, opts ActionOptions) (ActionResult, error) {
	s.labels = append(s.labels, label)
	s.items = append(s.items, append([]SelectItem(nil), items...))
	s.actionDefaults = append(s.actionDefaults, defaultIdx)
	s.opts = append(s.opts, opts)
	if s.actionIdx >= len(s.actionSteps) {
		return ActionResult{Row: -1}, nil
	}
	step := s.actionSteps[s.actionIdx]
	s.actionIdx++
	if step.action != footerActionNone {
		return ActionResult{Row: step.row, Action: step.action, OK: step.ok}, step.err
	}
	if step.tabStep != 0 {
		return ActionResult{Row: -1, OK: step.ok, TabStep: step.tabStep}, step.err
	}
	if step.label != "" {
		for i, item := range items {
			if item.Label == step.label {
				return ActionResult{Row: i, OK: step.ok}, step.err
			}
		}
		return ActionResult{Row: -1}, step.err
	}
	return ActionResult{Row: step.row, OK: step.ok}, step.err
}

func (s *actionPanelSelector) InputActions(label string, defaultValue string, opts ActionOptions) (InputResult, error) {
	s.inputLabels = append(s.inputLabels, label)
	s.inputDefaults = append(s.inputDefaults, defaultValue)
	s.inputOpts = append(s.inputOpts, opts)
	if s.inputIdx >= len(s.inputSteps) {
		return InputResult{}, nil
	}
	step := s.inputSteps[s.inputIdx]
	s.inputIdx++
	if step.action != footerActionNone {
		return InputResult{Action: step.action, OK: step.ok}, step.err
	}
	return InputResult{Text: step.value, OK: step.ok}, step.err
}

// Secret records the private editor's ask, then answers it from the same
// input steps the action-bearing editors read: only one editor is open at a
// time, so the two share one queue.
func (s *actionPanelSelector) Secret(label string, defaultValue string) (string, bool, error) {
	s.secretLabels = append(s.secretLabels, label)
	s.secretDefaults = append(s.secretDefaults, defaultValue)
	if s.inputIdx >= len(s.inputSteps) {
		return "", false, nil
	}
	step := s.inputSteps[s.inputIdx]
	s.inputIdx++
	return step.value, step.ok, step.err
}

// runActionConfigPanel drives the panel with a selector that carries the
// footer-action capability (plan 326): rich steps for the plain pages,
// action steps for the action-aware ones, input steps for the editors.
func runActionConfigPanel(t *testing.T, session *configPanelTestSession, selector *actionPanelSelector, steps []richStep, actionSteps []actionStep, inputSteps []inputActionStep, args ...string) *Renderer {
	t.Helper()
	selector.richSteps = steps
	selector.inputs = nil
	selector.actionSteps = actionSteps
	selector.inputSteps = inputSteps
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
		if item.Status != configDefaultStatus {
			t.Errorf("段 %q 的状态 = %q, want %q", schema.Title, item.Status, configDefaultStatus)
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
	for _, tab := range []string{configTabApproval, configTabSession, configTabAgents} {
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
				{label: "Default approval mode", ok: true},
				{label: "Full access", ok: true},
			},
			want: `"full_access"`,
		},
		{
			name: "number", section: "cron",
			steps: []richStep{
				{label: "Scheduled tasks", ok: true},
				{label: "Keep task records for", ok: true},
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
				{label: "Built-in subagents", ok: true},
				{label: "goal-evaluator", ok: true},
				{label: "Model", ok: true},
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
// The edit view's footer carries Default (plan 326 D4): the action unsets
// the key — Ctrl+U on a selector with footer actions — and it is the only
// place Default appears. An empty Enter saves nothing.
func TestConfigPanelEditFooterDefaultUnsets(t *testing.T) {
	session := newConfigPanelSession(map[string]string{"scheduled_tasks": `{"retention_days":45}`})
	selector := &actionPanelSelector{scriptedSelector: &scriptedSelector{}}
	runActionConfigPanel(t, session, selector, []richStep{
		{label: "Scheduled tasks", ok: true},
		{ok: false},
	},
		[]actionStep{{label: "Keep task records for", ok: true}, {ok: false}},
		[]inputActionStep{{action: FooterActionDefault, ok: true}})

	// The editor's footer offered exactly the Default action.
	if len(selector.inputOpts) != 1 || len(selector.inputOpts[0].FooterActions) != 1 {
		t.Fatalf("the editor's footer actions = %+v, want Default alone", selector.inputOpts)
	}
	action := selector.inputOpts[0].FooterActions[0]
	if action.Label != configDefaultActionLabel || action.Kind != FooterActionDefault {
		t.Fatalf("the edit footer's action = %+v, want Default", action)
	}
	// The footer shows the key beside the label whenever the action has one
	// (owner 2026-11-19): Default fires on Ctrl+U, so it must read as a keyed
	// action rather than a bare word.
	if action.Key != configDefaultActionKey {
		t.Fatalf("the edit footer's Default key = %q, want %q", action.Key, configDefaultActionKey)
	}

	if len(session.writes) != 1 {
		t.Fatalf("expected one write, got %d", len(session.writes))
	}
	// The section's last key is gone, so the whole section has nothing left:
	// an absent row is how "this install uses the default" reads.
	if raw := session.writes[0].raw; len(raw) != 0 {
		t.Fatalf("raw = %s, want the row removed", raw)
	}
}

// A choice page lists the real values and nothing else (plan 326 D4, owner
// 2026-11-18): no Default row and no choice-footer Default — the way back to
// a setting's own default is the edit view's footer action, which a boolean
// and an enum never open.
func TestConfigPanelBoolEnumChoiceHasNoDefault(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stored map[string]string
		steps  []richStep
		want   []string
	}{
		{
			name:   "a boolean",
			stored: map[string]string{"features": `{"memories":true}`},
			steps:  []richStep{{label: "Feature switches", ok: true}, {label: "Memory", ok: true}},
			want:   []string{configBoolOnLabel, configBoolOffLabel},
		},
		{
			name:   "an enum",
			stored: map[string]string{"lsp": `{"diagnostics":{"min_severity":"warning","late_delivery":true}}`},
			steps:  []richStep{{label: "Language servers", ok: true}, {label: "Minimum severity", ok: true}},
			want:   []string{"Error", "Warning", "Information", "Hint"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := newConfigPanelSession(tc.stored)
			selector := &scriptedSelector{}
			runConfigPanel(t, session, selector, append(append([]richStep{}, tc.steps...), richStep{ok: false}, richStep{ok: false}), nil)

			// The section list, then the section's page, then the choice page.
			if len(selector.richItems) < 3 {
				t.Fatalf("the choice page never opened: %v", selector.richLabels)
			}
			choice := selector.richItems[2]
			if got := rowLabels(choice); !slices.Equal(got, tc.want) {
				t.Fatalf("choice list = %v, want the values alone: %v", got, tc.want)
			}
			for _, item := range choice {
				if item.Label == configDefaultActionLabel {
					t.Fatalf("the choice list carries a Default row; the edit view's footer owns that")
				}
				if item.Description != "" {
					t.Fatalf("%q carries %q; a value row names the value alone", item.Label, item.Description)
				}
			}
		})
	}
}

// Every removal asks first (plan 326 D5): the confirmation's rows are Remove
// item and Keep item with Keep preselected — Enter alone changes nothing —
// and its heading names the row and what it holds. The footer's Remove fires
// on the entry row the cursor is on (plan 326 D2).
func TestConfigPanelDeleteConfirmsKeepPreselected(t *testing.T) {
	session := newConfigPanelSession(map[string]string{"sandbox_workspace_write": `{"writable_roots":["/a"]}`})
	selector := &actionPanelSelector{scriptedSelector: &scriptedSelector{}}
	runActionConfigPanel(t, session, selector, []richStep{
		{label: "Workspace sandbox", ok: true},
		{label: configKeepConfirmLabel, ok: true},
		{label: configRemoveConfirmLabel, ok: true},
		{ok: false},
	},
		[]actionStep{
			{label: "Extra writable roots", ok: true},
			{action: FooterActionRemove, row: 0, ok: true},
			{action: FooterActionRemove, row: 0, ok: true},
			{ok: false},
			{ok: false},
		}, nil)

	// Two confirmations opened — the first kept the row, so only the second
	// wrote — and both preselected Keep. A list's row is named by its place,
	// and the line under the heading shows what it holds.
	confirmations := 0
	for _, label := range selector.richLabels[1:] {
		if strings.HasPrefix(label, "Remove #1?\n/a") {
			confirmations++
		}
	}
	if confirmations != 2 {
		t.Fatalf("expected two removal confirmations headed with the row and its value, labels: %v", selector.richLabels)
	}
	// richLabels[0] is the section list; the confirmations follow it.
	if selector.defaults[1] != 1 || selector.defaults[2] != 1 {
		t.Fatalf("confirmation defaults = %v, want Keep (row 1) preselected", selector.defaults)
	}
	rows := selector.richItems[1]
	if got := rowLabels(rows); !slices.Equal(got, []string{configRemoveConfirmLabel, configKeepConfirmLabel}) {
		t.Fatalf("confirmation rows = %v", got)
	}
	// The Remove row alone is the danger row (prototype .item.danger): the
	// choice that discards the item is the one the renderer paints red.
	if !rows[0].Danger || rows[1].Danger {
		t.Fatalf("confirmation danger flags = %v/%v, want Remove only", rows[0].Danger, rows[1].Danger)
	}
	if len(session.writes) != 1 {
		t.Fatalf("expected the second confirmation alone to write, got %+v", session.writes)
	}
	if raw := string(session.writes[0].raw); raw != `{"writable_roots":[]}` {
		t.Fatalf("raw = %s", raw)
	}
}

// The confirmation's Remove row is painted the panel's own failure red, the
// prototype's .item.danger .item-label — and nothing else is: Keep stays the
// terminal's foreground, and a picker no list armed with the flag carries no
// red at all, so the colour cannot leak into another panel.
func TestConfigPanelRemoveConfirmRowIsRed(t *testing.T) {
	useANSI256ColorProfile(t)
	items := []SelectItem{
		{Label: configRemoveConfirmLabel, Danger: true},
		{Label: configKeepConfirmLabel},
	}
	lines := renderRichPage(items, "Remove #1?\n/a", 120)
	remove := rawRowFor(t, lines, configRemoveConfirmLabel)
	if !strings.Contains(remove, panelFailStyle.Render(configRemoveConfirmLabel)) {
		t.Fatalf("the Remove row is not painted the failure red: %q", remove)
	}
	if keep := rawRowFor(t, lines, configKeepConfirmLabel); strings.Contains(keep, "\x1b[38;5;203m") {
		t.Fatalf("the Keep row took the danger colour: %q", keep)
	}

	// Another panel's picker over rows that set no flag: no red anywhere.
	plain := &richPickerState{items: []SelectItem{{Label: configRemoveConfirmLabel}, {Label: configKeepConfirmLabel}}}
	plain.rebuildSelectable()
	for _, line := range plain.panel("Pick one").layout(120, 0, 0, false).lines {
		if strings.Contains(line, "\x1b[38;5;203m") {
			t.Fatalf("a picker without a danger row drew the failure red: %q", line)
		}
	}
}

// rawRowFor is one row's painted line, before ANSI is stripped, found by the
// name it starts with once the row's margin is off.
func rawRowFor(t *testing.T, lines []string, name string) string {
	t.Helper()
	for _, line := range lines {
		body := strings.TrimLeft(stripANSI(line), " ")
		body = strings.TrimPrefix(body, selectorCursorGlyph+" ")
		if strings.HasPrefix(body, name) {
			return line
		}
	}
	t.Fatalf("no row %q among:\n%s", name, strings.Join(stripANSIAll(lines), "\n"))
	return ""
}

// A number outside the loader's range never reaches the store, and the page
// says so in the field's own words before the next attempt.
func TestConfigPanelRefusesAnOutOfRangeNumber(t *testing.T) {
	session := newConfigPanelSession(nil)
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, []richStep{
		{label: "Scheduled tasks", ok: true},
		{label: "Keep task records for", ok: true},
		{label: "Keep task records for", ok: true},
		{ok: false},
	}, []selectorInput{{value: "99999", ok: true}, {value: "45", ok: true}})

	// The refusal is the page's own line, so it is in the prompt the panel
	// showed next — the user reads it before the value they retype.
	if refusals := strings.Join(selector.richLabels, "\n"); !strings.Contains(refusals, "Use a value no greater than 3650.") {
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

	if prompts := strings.Join(selector.richLabels, "\n"); !strings.Contains(prompts, "Adjusted to the maximum: 20.") {
		t.Fatalf("the clamp must be reported; prompts:\n%s", prompts)
	}
	if len(session.writes) != 1 || string(session.writes[0].raw) != `{"search_top_k":20}` {
		t.Fatalf("writes = %+v", session.writes)
	}
}

// The LSP idle exit's floor is its own rule (plan 327 D3, prototype
// html:137): 0 is the default and every other value must be a real timeout,
// so 1–59 are refused in the prototype's own words while 0 lands.
func TestConfigPanelIdleTimeoutNeedsSixtySeconds(t *testing.T) {
	session := newConfigPanelSession(nil)
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, []richStep{
		{label: "Language servers", ok: true},
		{label: "Exit when idle for", ok: true},
		{label: "Exit when idle for", ok: true},
		{ok: false},
	}, []selectorInput{{value: "30", ok: true}, {value: "0", ok: true}})

	prompts := strings.Join(selector.richLabels, "\n")
	if !strings.Contains(prompts, "Use 0 for the default, or at least 60 seconds.") {
		t.Fatalf("the floor refusal must be shown on the page; prompts:\n%s", prompts)
	}
	if strings.Contains(prompts, "Use a value of at least") {
		t.Fatalf("the floor is not the min bound; prompts:\n%s", prompts)
	}
	if len(session.writes) != 1 || string(session.writes[0].raw) != `{"idle_timeout":0}` {
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
	// the default: the refusal is not a half-applied edit. An unset switch
	// reads as the value its default gives it (plan 350).
	page := lastPageListing(selector, "Next-step suggestions")
	if page == nil {
		t.Fatalf("the features page was never drawn after the refusal: %v", selector.richLabels)
	}
	if row := findRow(t, page, "Memory"); row.Value != configBoolOnLabel {
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
	if _, ok := findRowLoose(selector.richItems[0], "Keep task records for"); !ok {
		t.Fatalf("first page = %v, want the cron section page", rowLabels(selector.richItems[0]))
	}
	// The page carries no heading of its own (plan 350): the tab bar above it
	// and its rows say everything.
	if selector.richLabels[0] != "" {
		t.Fatalf("the cron page's label = %q, want no heading", selector.richLabels[0])
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

// Secrets never reach a page (plan 328 D3, prototype html:106/118): a value
// at a masked path — an MCP server's header values, the oauth tokens — reads
// [hidden] or Not set in every column, the delete preview hides it whole, and
// the edit asks for a replacement empty-handed. On a line-based selector the
// private edit is refused rather than echoed.
func TestConfigPanelMasksSecrets(t *testing.T) {
	stored := `{"definitions":{"main":{"primary":true,"mcp_servers":[{"name":"api","transport":"http","url":"https://api.internal","headers":{"Authorization":"Bearer sk-live-9999","X-Trace":"t1"}}]}}}`
	walkToHeaders := []richStep{
		{label: "Agents", ok: true},
		{label: "Agent definitions", ok: true},
		{label: "main", ok: true},
		{label: "MCP servers", ok: true},
		{label: "#1", ok: true},
		{label: "Headers", ok: true},
	}

	t.Run("the stored value is hidden in every column", func(t *testing.T) {
		session := newConfigPanelSession(map[string]string{"agents": stored})
		selector := &scriptedSelector{}
		runConfigPanel(t, session, selector, append(append([]richStep{}, walkToHeaders...), richStep{ok: false}, richStep{ok: false}, richStep{ok: false}, richStep{ok: false}, richStep{ok: false}), nil)

		// The Headers field's own row on the server's page: a map at a
		// masked path is hidden, not counted.
		server := lastPageListing(selector, "Headers")
		if server == nil {
			t.Fatalf("the server page never opened: %v", selector.richLabels)
		}
		if row := findRow(t, server, "Headers"); row.Value != configSecretHiddenStatus {
			t.Fatalf("the Headers row's value = %q, want %q", row.Value, configSecretHiddenStatus)
		}
		// The entries under it: each header's value column.
		headers := lastPageListing(selector, "Authorization")
		if headers == nil {
			t.Fatalf("the headers page never opened: %v", selector.richLabels)
		}
		if row := findRow(t, headers, "Authorization"); row.Value != configSecretHiddenStatus {
			t.Fatalf("the Authorization row's value = %q, want %q", row.Value, configSecretHiddenStatus)
		}
		if row := findRow(t, headers, "X-Trace"); row.Value != configSecretHiddenStatus {
			t.Fatalf("the X-Trace row's value = %q, want %q", row.Value, configSecretHiddenStatus)
		}
		// And nowhere else did the value slip through.
		for _, items := range selector.richItems {
			for _, item := range items {
				for _, text := range []string{item.Label, item.Description, item.Value, item.Status} {
					if strings.Contains(text, "sk-live-9999") || strings.Contains(text, "Bearer") {
						t.Fatalf("a secret reached the page: %q", text)
					}
				}
			}
		}
		for _, label := range selector.richLabels {
			if strings.Contains(label, "sk-live-9999") || strings.Contains(label, "Bearer") {
				t.Fatalf("a secret reached a heading: %q", label)
			}
		}
		if len(session.writes) != 0 {
			t.Fatalf("browsing writes nothing, got %+v", session.writes)
		}
	})

	t.Run("the line mode refuses the private edit", func(t *testing.T) {
		session := newConfigPanelSession(map[string]string{"agents": stored})
		selector := &scriptedSelector{}
		runConfigPanel(t, session, selector, append(append([]richStep{}, walkToHeaders...),
			richStep{label: "Authorization", ok: true}, richStep{ok: false}, richStep{ok: false}), nil)

		if !strings.Contains(pageLabels(selector), configSecretLineNotice) {
			t.Fatalf("the line mode must refuse the private edit: %s", pageLabels(selector))
		}
		if selector.inputIdx != 0 {
			t.Fatalf("the secret was asked in the clear, got %d inputs", selector.inputIdx)
		}
		if len(session.writes) != 0 {
			t.Fatalf("a refused edit writes nothing, got %+v", session.writes)
		}
	})

	t.Run("the replacement is asked empty-handed and lands", func(t *testing.T) {
		session := newConfigPanelSession(map[string]string{"agents": stored})
		selector := &actionPanelSelector{scriptedSelector: &scriptedSelector{}}
		// The section list, the agent's page and the server's page are plain
		// pages (rich steps); the section's fields and the rows pages carry
		// footer actions (action steps); the private editor answers from the
		// input steps.
		runActionConfigPanel(t, session, selector,
			[]richStep{
				{label: "Agents", ok: true},
				{label: "MCP servers", ok: true},
				{label: "Headers", ok: true},
			},
			[]actionStep{
				{label: "Agent definitions", ok: true},
				{label: "main", ok: true},
				{label: "#1", ok: true},
				{label: "Authorization", ok: true},
			},
			[]inputActionStep{{value: "Bearer sk-rotated-4242", ok: true}})

		if len(session.writes) != 1 {
			t.Fatalf("expected one write, got %d; editors asked: %v", len(session.writes), selector.inputOpts)
		}
		if raw := string(session.writes[0].raw); !strings.Contains(raw, "sk-rotated-4242") {
			t.Fatalf("the replacement must land, raw = %s", raw)
		}
		// The private editor opened empty-handed and masked: the stored
		// secret was never handed to a field, and neither secret reached a
		// page.
		if len(selector.inputOpts) != 1 || !selector.inputOpts[0].Secret {
			t.Fatalf("the private editor never opened masked: %+v", selector.inputOpts)
		}
		if selector.inputDefaults[0] != "" {
			t.Fatalf("the private editor must start empty, got %q", selector.inputDefaults[0])
		}
		if selector.inputOpts[0].Label != configSecretReplacementLabel {
			t.Fatalf("the private editor's label = %q, want the replacement label", selector.inputOpts[0].Label)
		}
		for _, items := range selector.richItems {
			for _, item := range items {
				for _, text := range []string{item.Label, item.Description, item.Value, item.Status} {
					if strings.Contains(text, "sk-live-9999") || strings.Contains(text, "sk-rotated-4242") {
						t.Fatalf("a secret reached the page: %q", text)
					}
				}
			}
		}
		for _, label := range selector.richLabels {
			if strings.Contains(label, "sk-live-9999") || strings.Contains(label, "sk-rotated-4242") {
				t.Fatalf("a secret reached a heading: %q", label)
			}
		}
	})
}

// The multiline editor saves what was typed, untrimmed, and an empty save is
// the unset (plan 328 D2): a prompt with no words is no prompt. The editor
// opens as the prototype's textarea — the capability the interactive selector
// is asked for — and the single-line ladder keeps working under it.
func TestConfigPanelMultilineSavesOnCtrlEnter(t *testing.T) {
	t.Run("the text keeps its whitespace and newlines", func(t *testing.T) {
		session := newConfigPanelSession(map[string]string{"compact": `{}`})
		selector := &actionPanelSelector{scriptedSelector: &scriptedSelector{}}
		runActionConfigPanel(t, session, selector,
			[]richStep{{label: "Context compaction", ok: true}, {ok: false}},
			[]actionStep{{label: "Hand-off prompt", ok: true}, {ok: false}},
			[]inputActionStep{{value: "  keep my spaces \nand my second line ", ok: true}})

		if len(selector.inputOpts) != 1 || !selector.inputOpts[0].Multiline {
			t.Fatalf("the multiline field must open the textarea, opts = %+v", selector.inputOpts)
		}
		if len(session.writes) != 1 {
			t.Fatalf("expected one write, got %d", len(session.writes))
		}
		if raw := string(session.writes[0].raw); !strings.Contains(raw, `"prompt":"  keep my spaces \nand my second line "`) {
			t.Fatalf("the multiline value must keep its shape, raw = %s", raw)
		}
	})

	t.Run("an empty save unsets the key", func(t *testing.T) {
		session := newConfigPanelSession(map[string]string{"compact": `{"prompt":"old words"}`})
		selector := &actionPanelSelector{scriptedSelector: &scriptedSelector{}}
		runActionConfigPanel(t, session, selector,
			[]richStep{{label: "Context compaction", ok: true}, {ok: false}},
			[]actionStep{{label: "Hand-off prompt", ok: true}, {ok: false}},
			[]inputActionStep{{value: " ", ok: true}})

		if len(session.writes) != 1 || len(session.writes[0].raw) != 0 {
			t.Fatalf("an empty multiline save unsets the key, writes = %+v", session.writes)
		}
	})

	t.Run("a single-line field still opens the one-line editor", func(t *testing.T) {
		session := newConfigPanelSession(nil)
		selector := &actionPanelSelector{scriptedSelector: &scriptedSelector{}}
		runActionConfigPanel(t, session, selector,
			[]richStep{{label: "Scheduled tasks", ok: true}, {ok: false}},
			[]actionStep{{label: "Keep task records for", ok: true}, {ok: false}},
			[]inputActionStep{{value: "45", ok: true}})

		if len(selector.inputOpts) != 1 || selector.inputOpts[0].Multiline {
			t.Fatalf("a single-line field keeps the one-line editor, opts = %+v", selector.inputOpts)
		}
		if raw := string(session.writes[0].raw); !strings.Contains(raw, `"retention_days":45`) {
			t.Fatalf("raw = %s", raw)
		}
	})
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
// scalar row, so adding one opens its text editor directly (plan 329). A
// delete asks first (plan 326 D5): the menu's delete opens the confirmation
// whose Keep row is preselected, so choosing Remove item is what writes.
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
		{label: "Configured servers", ok: true},
		{label: "gopls", ok: true},
		{label: "Server settings", ok: true},
		{label: "legacy", ok: true},
		{label: configRowDropLabel, ok: true},
		{label: configRemoveConfirmLabel, ok: true},
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

// Every section fb_config stores has a descriptor, every descriptor names a
// Root section, and the table lines up with the store's own: the 11 rows
// fb_config holds are the ones the panel shows. The two Root sections only
// forebrain.yaml carries — gateway and credentials — are deliberately outside
// the table: the terminal panel does not show them (owner 2026-11-21).
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
// Root section or projects one that is stored.
func configSchemaCoverageProblems(types map[string]reflect.Type, schemas []configSectionSchema, stored map[string]bool) []string {
	var problems []string
	byName := make(map[string]configSectionSchema, len(schemas))
	for _, schema := range schemas {
		if _, dup := byName[schema.Name]; dup {
			problems = append(problems, "段 "+schema.Name+" 有两条描述符")
		}
		byName[schema.Name] = schema
	}
	for name := range stored {
		if _, ok := byName[name]; !ok {
			problems = append(problems, "fb_config 的段 "+name+" 没有描述符，/config 面板里看不到它")
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
			// The Built-in subagents entry owns the three keys its page
			// renders (P4): the per-type rows and the two system types'
			// global rows are reachable through it alone.
			if field.Kind == configFieldBuiltinSubagents {
				covered["plan_reviewer"] = true
				covered["goal_evaluator"] = true
			}
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
		for _, field := range schema.Fields {
			check(schema.Name+" field label "+field.Label, field.Label)
			check(schema.Name+" field hint of "+field.Label, field.Hint)
			check(schema.Name+" field group of "+field.Label, field.Group)
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
		"configRowBackLabel":       configRowBackLabel,
		"configAddActionLabel":     configAddActionLabel,
		"configRemoveActionLabel":  configRemoveActionLabel,
		"configRemoveConfirmLabel": configRemoveConfirmLabel,
		"configKeepConfirmLabel":   configKeepConfirmLabel,
		"configBackRowLabel":       configBackRowLabel,
		"configAddRowLabel":        configAddRowLabel,
		"configUpRowLabel":         configUpRowLabel,
		"configRowEditLabel":       configRowEditLabel,
		"configRowOpenLabel":       configRowOpenLabel,
		"configRowDropLabel":       configRowDropLabel,
		"configBoolOnLabel":        configBoolOnLabel,
		"configBoolOffLabel":       configBoolOffLabel,
		"configDefaultActionLabel": configDefaultActionLabel,
		"configNewTextRow":         configNewTextRow,
		"configNewNumberRow":       configNewNumberRow,
		"configNewTrueRow":         configNewTrueRow,
		"configNewFalseRow":        configNewFalseRow,
		"configNewBooleanRow":      configNewBooleanRow,
		"configNewListRow":         configNewListRow,
		"configNewObjectRow":       configNewObjectRow,
		"configSecretHiddenStatus": configSecretHiddenStatus,
		"configSecretUnsetStatus":  configSecretUnsetStatus,
		"configSecretLineNotice":   configSecretLineNotice,
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
// lastPageListing is the last page that lists a row with this label: the
// pages carry no headings to find them by any more (plan 350), so a test names
// the page by a row only it lists.
func lastPageListing(selector *scriptedSelector, row string) []SelectItem {
	for i := len(selector.richItems) - 1; i >= 0; i-- {
		if _, ok := findRowLoose(selector.richItems[i], row); ok {
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
		tab    string
		titles []string
	}{
		{tab: configTabApproval, titles: []string{"Approval preset", "Workspace sandbox", "Batch projects"}},
		{tab: configTabSession, titles: []string{"Context compaction", "Memory", "Scheduled tasks", "Feature switches"}},
		{tab: configTabAgents, titles: []string{"Agents", "Hooks", "Language servers", "Peer messaging"}},
	} {
		t.Run(tc.tab, func(t *testing.T) {
			lines := renderTabbedPage(items, first[tc.tab], 120)
			// The tab bar carries every tab and its row count, so a reader
			// always sees where the three groups are.
			bar := strings.Join(stripANSIAll(lines), "\n")
			if chip := tc.tab + " " + strconv.Itoa(len(sectionsOfTab(tc.tab))); !strings.Contains(bar, chip) {
				t.Fatalf("the tab bar is missing %q:\n%s", chip, bar)
			}
			for _, title := range tc.titles {
				// The fixed status track narrows the description column (plan
				// 325 D3), so the longest summary folds onto continuations under
				// its row; the whole text still reads there.
				full := renderedRowText(t, lines, title)
				if !strings.Contains(full, panel.sectionSchema(title).Summary) {
					t.Fatalf("%s's row carries no summary: %q", title, full)
				}
				row := renderedRow(t, lines, title)
				// A section with no stored row reads Default, at the right
				// edge of the panel.
				if !strings.HasSuffix(row, configDefaultStatus) {
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

// A section page paints each setting as a label and the value it holds in a
// right-aligned track — and nothing else: the field's help belongs to its
// editor, and a setting carries no status of its own (plan 350).
func TestConfigPanelRendersTheValueColumn(t *testing.T) {
	// A settings page: two switches, one set and one not, so the values differ.
	panel := sectionPanel(map[string]string{"features": `{"memories":true}`})
	features, ok := configSectionSchemaFor("features")
	if !ok {
		t.Fatal("no features section")
	}
	items, _ := panel.fieldItems(features, panel.sectionDoc("features"), false)
	lines := renderRichPage(items, features.Title, 120)

	memories := renderedRow(t, lines, "Memory")
	if !strings.HasSuffix(memories, configBoolOnLabel) {
		t.Fatalf("the Memory row = %q, want its value at the right edge", memories)
	}
	if strings.Contains(memories, "Remember useful context across conversations.") {
		t.Fatalf("the Memory row = %q, want no help sentence beside the name", memories)
	}
	// An unset switch reads as the value its default gives it, and no setting
	// carries a status of its own (plan 350, the prototype's display()).
	servers := renderedRow(t, lines, "Language servers")
	if !strings.HasSuffix(servers, configBoolOnLabel) {
		t.Fatalf("the Language servers row = %q, want the default's own value", servers)
	}
	if strings.Contains(servers, configDefaultStatus) {
		t.Fatalf("the Language servers row = %q, want no Default status", servers)
	}
	// Both values end at the same column: the track is fixed, not pushed along
	// by the length of what a row holds.
	if endsAt(memories, configBoolOnLabel) != endsAt(servers, configBoolOnLabel) {
		t.Fatalf("the values are not aligned:\n%q\n%q", memories, servers)
	}

	// The Built-in subagents entry's page (P4, plan 324 D7): the six
	// install-wide pins each name the model row their value column holds and
	// the thinking level their status carries, with the prototype's help
	// sentence as the description — a pin nobody set reads as the sentence it
	// follows and marks nothing.
	builtin := sectionPanel(map[string]string{"agents": `{
		"subagent_models":{
			"plan":{"model":"llm-1","reasoning_effort":"high"},
			"explore":{"model":"llm-1","reasoning_effort":"medium"}
		},
		"plan_reviewer":{"model":"llm-1"}
	}`})
	builtin.models = []appcfg.LLMModelConfig{{ID: "llm-1", Provider: "openai", Model: "gpt-main"}}
	agentsSchema, ok := configSectionSchemaFor("agents")
	if !ok {
		t.Fatal("no agents section")
	}
	items, _ = builtin.fieldItems(agentsSchema, builtin.sectionDoc("agents"), true)
	lines = renderRichPage(items, "Agents", 120)
	entry := renderedRow(t, lines, "Built-in subagents")
	// The entry is a collection: it names itself and opens its rows, and
	// counts nothing beside them (plan 350).
	if strings.TrimSpace(entry) != "Built-in subagents" {
		t.Fatalf("the entry row = %q, want the label alone", entry)
	}
	items = builtin.builtinSubagentItems(builtin.sectionDoc("agents"))
	lines = renderRichPage(items, "Built-in subagents", 120)
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
	// The help sentence belongs to the type's editor, never to its row (plan
	// 350): the row carries the model row's label and the level in force.
	plan := renderedRow(t, lines, "plan")
	if strings.Contains(plan, "Install-wide default model and thinking level") {
		t.Fatalf("the plan row = %q, want no help sentence", plan)
	}
	if !strings.Contains(plan, "openai/gpt-main") || strings.Contains(plan, "openai/gp...") {
		t.Fatalf("the plan row = %q, want the model row's label whole", plan)
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

// The value column holds a model row's whole label (plan 325 D3): the cap is
// 44 cells now, so a long provider/model label reads whole where the old
// 24-cell cap elided it, and a type page's Model row carries its value
// right-aligned the same way (D4).
func TestConfigPanelValueColumnNotElided(t *testing.T) {
	panel := sectionPanel(map[string]string{"agents": `{
		"subagent_models":{
			"plan":{"model":"row-2","reasoning_effort":"high"},
			"explore":{"model":"row-2"}
		}
	}`})
	panel.models = []appcfg.LLMModelConfig{
		{ID: "row-1", Provider: "openai", Model: "gpt-main", BaseURL: "https://api.one.example.com"},
		{ID: "row-2", Provider: "openai", Model: "gpt-main-extended-preview", BaseURL: "https://api.two.example.com"},
	}
	doc := panel.sectionDoc("agents")
	items := panel.builtinSubagentItems(doc)
	lines := renderRichPage(items, "Built-in subagents", 120)
	label := "openai/gpt-main-extended-preview"
	if displayLineWidth(label) <= panelNarrowValueColumnMax {
		t.Fatalf("the fixture's label must outgrow the old cap: %q", label)
	}
	plan := renderedRow(t, lines, "plan")
	if !strings.Contains(plan, label) || strings.Contains(plan, "…") {
		t.Fatalf("the plan row = %q, want the whole label in the value track", plan)
	}
	if !strings.HasSuffix(plan, "High") {
		t.Fatalf("the plan row = %q, want the level's status beyond the value", plan)
	}

	// The type page: its Model row carries the value right-aligned (D4).
	explore, _ := configLookupValue(doc, []string{"subagent_models", "explore"})
	exploreSettings, _ := explore.(map[string]any)
	typeItems, _ := panel.templateItems(configTemplateTypeDef, "explore", exploreSettings, false, false)
	typeLines := renderRichPage(typeItems, "explore", 120)
	// The value names the row the way every picker does: the bare
	// provider/model label, the endpoint drawn beside it (owner 2026-11-22).
	modelRow := renderedRow(t, typeLines, "Model")
	if !strings.HasSuffix(modelRow, label) || strings.Contains(modelRow, "…") {
		t.Fatalf("the type page's Model row = %q, want its value right-aligned", modelRow)
	}
}

// The /config pages breathe (owner 2026-11-18 「选项和tabs组件标题之间拉开间距」
// 「布局太拥挤了」, plan 325 D5): one blank line under the head or tab bar
// before the first row, one blank line between rows, the blank line between
// groups kept — never doubled, never cutting a heading from its rows. At 68
// columns the focused row and the footer stay reachable: the layout scrolls
// to the focus and keeps the keys.
func TestConfigPanelAirySpacing(t *testing.T) {
	panel := sectionPanel(map[string]string{"features": `{"memories":true}`})
	features, ok := configSectionSchemaFor("features")
	if !ok {
		t.Fatal("no features section")
	}
	items, _ := panel.fieldItems(features, panel.sectionDoc("features"), false)
	lines := renderRichPage(items, features.Title+"\n"+features.Summary, 120)
	plain := stripANSIAll(lines)

	// One blank line under the head, before the first row.
	if strings.TrimSpace(plain[2]) != "" {
		t.Fatalf("the line under the head = %q, want a blank one:\n%s", plain[2], strings.Join(plain, "\n"))
	}
	memories, servers := -1, -1
	for i, line := range plain {
		body := strings.TrimPrefix(strings.TrimSpace(line), selectorCursorGlyph+" ")
		switch {
		case strings.HasPrefix(body, "Memory"):
			memories = i
		case strings.HasPrefix(body, "Language servers"):
			servers = i
		}
	}
	if memories < 0 || servers < 0 {
		t.Fatalf("the features page lost its rows:\n%s", strings.Join(plain, "\n"))
	}
	// One blank line between the two rows — the air the owner asked for.
	if servers != memories+2 || strings.TrimSpace(plain[memories+1]) != "" {
		t.Fatalf("the rows are cramped:\n%s", strings.Join(plain, "\n"))
	}
	// Never doubled: no two blank lines sit together.
	for i := 1; i < len(plain); i++ {
		if strings.TrimSpace(plain[i]) == "" && strings.TrimSpace(plain[i-1]) == "" {
			t.Fatalf("a doubled blank at line %d:\n%s", i, strings.Join(plain, "\n"))
		}
	}

	// The overview: one blank line under the tab bar, one between rows.
	sectionItems, _ := panel.sectionItems()
	overview := stripANSIAll(renderTabbedPage(sectionItems, 0, 120))
	tabBar := -1
	for i, line := range overview {
		if strings.Contains(line, configTabApproval) {
			tabBar = i
			break
		}
	}
	if tabBar < 0 || tabBar >= len(overview)-1 || strings.TrimSpace(overview[tabBar+1]) != "" {
		t.Fatalf("the overview's tab bar is not followed by one blank line:\n%s", strings.Join(overview, "\n"))
	}
	// No title and no subtitle above it (owner 2026-11-21 「删掉 Configuration
	// 标题行和 Open a section ... 副标题行」): the tab bar is the first line,
	// and the read-only File only tab is gone with the sections it listed.
	if tabBar != 0 {
		t.Fatalf("the overview carries %d lines above its tab bar:\n%s", tabBar, strings.Join(overview, "\n"))
	}
	whole := strings.Join(overview, "\n")
	for _, gone := range []string{"Configuration", "Open a section", "File only"} {
		if strings.Contains(whole, gone) {
			t.Fatalf("the overview still reads %q:\n%s", gone, whole)
		}
	}

	// At 68 columns the focused row and the footer stay reachable.
	state := newTabbedPickerState(sectionItems, TabbedSelectOptions{DefaultIdx: 4}, false, "")
	state.configPage = true
	state.noFilter = true
	narrow := state.panel("").layout(68, 14, 0, true)
	joined := strings.Join(stripANSIAll(narrow.lines), "\n")
	if !strings.Contains(joined, selectorCursorGlyph) {
		t.Fatalf("the focused row is not reachable at 68 columns:\n%s", joined)
	}
	if !strings.Contains(joined, "Esc to cancel") {
		t.Fatalf("the footer keys are not reachable at 68 columns:\n%s", joined)
	}
	for _, line := range narrow.lines {
		if w := displayLineWidth(stripANSI(line)); w > 68 {
			t.Fatalf("a row overflows a 68-cell panel: %q", stripANSI(line))
		}
	}
}

// The /config pages carry no filter box (owner 2026-11-18 「删掉…所有的filter
// 输入框，不需要」, plan 325 D6): a page with more rows than every other
// picker's threshold still renders no filter field — the overview neither —
// while another panel's picker over the same rows keeps its filter.
func TestConfigPanelNoFilterBox(t *testing.T) {
	if !pickerShowsFilter(8) {
		t.Fatal("the fixture needs a row count every other picker filters at")
	}
	rows := make([]SelectItem, 0, 8)
	for i := 0; i < 8; i++ {
		rows = append(rows, SelectItem{Label: "setting-" + strconv.Itoa(i)})
	}
	page := &richPickerState{items: rows, configPage: true, noFilter: true}
	for i := range rows {
		page.selectable = append(page.selectable, i)
	}
	if field := page.panel("Memory").field; field != nil {
		t.Fatalf("the config page carries a filter field: %+v", field)
	}
	for _, line := range page.panel("Memory").layout(120, 0, 0, false).lines {
		if strings.Contains(stripANSI(line), "Type to filter") {
			t.Fatalf("the config page drew a filter box: %q", stripANSI(line))
		}
	}

	// The overview neither, over the whole section list.
	panel := sectionPanel(nil)
	sectionItems, _ := panel.sectionItems()
	overview := newTabbedPickerState(sectionItems, TabbedSelectOptions{}, false, "")
	overview.configPage = true
	overview.noFilter = true
	if field := overview.panel("Configuration").field; field != nil {
		t.Fatalf("the config overview carries a filter field: %+v", field)
	}

	// Another panel's picker over the same rows keeps its filter.
	other := &richPickerState{items: rows}
	other.rebuildSelectable()
	other.noFilter = !pickerShowsFilter(len(other.selectable))
	if field := other.panel("Memory").field; field == nil {
		t.Fatal("a plain picker lost its filter")
	}
}

// The model picker opts into the filter (owner 2026-11-22): a /config page
// that otherwise carries no filter box narrows its rows like /model's own
// list, and Enter still picks the cursor's row in the FULL list — while a
// page without the opt-in ignores the typing and keeps the opening row.
func TestConfigPanelModelPickerFilters(t *testing.T) {
	items := make([]SelectItem, 0, 10)
	for i := 0; i < 10; i++ {
		items = append(items, SelectItem{Label: "provider/model-" + strconv.Itoa(i), Description: "https://example.com/v1"})
	}
	sel := pickerKeys(t, "model-7"+keyEnter)
	res, err := sel.SelectConfigPage("Model", items, 0, ActionOptions{Page: true, Filter: true})
	if err != nil || !res.OK {
		t.Fatalf("filtered pick = %+v, %v; want ok", res, err)
	}
	if res.Row != 7 {
		t.Fatalf("filtered Enter picked row %d, want absolute row 7", res.Row)
	}

	sel = pickerKeys(t, "model-7"+keyEnter)
	res, err = sel.SelectConfigPage("Model", items, 0, ActionOptions{Page: true})
	if err != nil || !res.OK || res.Row != 0 {
		t.Fatalf("no-filter pick = %+v, %v; want the opening row 0", res, err)
	}
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
	return panel
}

// renderTabbedPage lays the config overview out at width, opened on the tab
// that holds items[start]: the tabbed picker as the panel's own page draws it
// (plan 325) — no title and no subtitle above the tab bar (owner 2026-11-21),
// a blank line under it, airier rows, no filter box.
func renderTabbedPage(items []SelectItem, start, width int) []string {
	p := newTabbedPickerState(items, TabbedSelectOptions{DefaultIdx: start}, false, "")
	p.configPage = true
	p.noFilter = true
	return p.panel("").layout(width, 0, 0, false).lines
}

// renderRichPage lays one /config list page out at width, the way the panel's
// pages draw them (plan 325): the plain rich picker with ruled groups, a blank
// line under the head, airier rows and no filter box.
func renderRichPage(items []SelectItem, label string, width int) []string {
	p := &richPickerState{items: items, configPage: true, noFilter: true}
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

// renderedRowText is one row's whole text: the description column of its line
// plus the wrapped continuations under it, or — for one of the /config
// overview's two-line blocks (plan 350) — the summary hanging on the line under
// the title. The fixed status track narrows a two-column row's description
// (plan 325 D3), so a long summary folds across lines, and the track itself
// sits at the line's right edge — two or more spaces off the text, which is
// what tells it from the description's own single spaces.
func renderedRowText(t *testing.T, lines []string, name string) string {
	t.Helper()
	for i, line := range lines {
		body := strings.TrimLeft(stripANSI(line), " ")
		body = strings.TrimPrefix(body, selectorCursorGlyph+" ")
		if !strings.HasPrefix(body, name) {
			continue
		}
		parts := splitRowColumns(stripANSI(line))
		var cont []string
		for _, next := range lines[i+1:] {
			plain := stripANSI(next)
			// A continuation hangs at the panel's own indent and a block's
			// summary does too, two cells in from the row above it; a blank
			// line ends either, and so does the next row's title.
			if !strings.HasPrefix(plain, panelIndent) || strings.HasPrefix(plain, panelIndent+" ") || strings.TrimSpace(plain) == "" {
				break
			}
			cont = append(cont, strings.TrimSpace(plain))
		}
		if len(cont) > 0 {
			return strings.Join(cont, " ")
		}
		if len(parts) > 1 {
			return parts[1]
		}
		t.Fatalf("row %q carries no text: %q", name, stripANSI(line))
	}
	t.Fatalf("no row %q among:\n%s", name, strings.Join(stripANSIAll(lines), "\n"))
	return ""
}

// splitRowColumns splits a painted row into the columns its layout keeps
// apart: the name, the description, then the value and status tracks. A run
// of two or more spaces is a column gap; a single space is inside a column.
func splitRowColumns(row string) []string {
	parts := make([]string, 0, 4)
	field, run := "", 0
	for _, r := range row {
		if r == ' ' {
			run++
			continue
		}
		if run >= 2 && field != "" {
			parts = append(parts, field)
			field = ""
		} else if run == 1 && field != "" {
			field += " "
		}
		run = 0
		field += string(r)
	}
	if field != "" {
		parts = append(parts, field)
	}
	return parts
}

func stripANSIAll(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, stripANSI(line))
	}
	return out
}

// The agents section reaches the plan reviewer's two global settings beside the
// goal evaluator's, and the definitions page marks a primary agent the way the
// prototype does — a status at the row's right edge, with the description left
// as the value it holds (plan 313).
func TestConfigPanelAgentsListsThePlanReviewerAndMarksPrimary(t *testing.T) {
	session := newConfigPanelSession(map[string]string{"agents": `{"definitions":{
		"main":{"primary":true,"subagents":[]},
		"repo-auditor":{"primary":false,"system_prompt":"You audit repositories."},
		"shell-agent":{"display_name":"Shell"}
	}}`})
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
	if !slices.Contains(labels, "Built-in subagents") {
		t.Fatalf("the agents page is missing %q: %v", "Built-in subagents", labels)
	}
	// The plan reviewer's row moved to the "Subagents" page, which is a
	// projection of this same document (plan 314): one place owns every
	// install-wide subagent row, and an agent page keeps only its own defaults.
	for _, gone := range []string{"Plan reviewer model", "Plan reviewer thinking level"} {
		if slices.Contains(labels, gone) {
			t.Fatalf("the agents page still lists %q: %v", gone, labels)
		}
	}

	// The three role words the prototype's list writes (P3): an explicit true
	// is a primary agent, an explicit false is the install-wide subagent role,
	// and a definition that never chose is just an agent.
	main := findRow(t, selector.richItems[1], "main")
	if main.Status != configPrimaryStatus {
		t.Fatalf("main's status = %q, want %q", main.Status, configPrimaryStatus)
	}
	auditor := findRow(t, selector.richItems[1], "repo-auditor")
	if auditor.Status != configSubagentStatus {
		t.Fatalf("repo-auditor's status = %q, want %q", auditor.Status, configSubagentStatus)
	}
	shell := findRow(t, selector.richItems[1], "shell-agent")
	if shell.Status != configAgentStatus {
		t.Fatalf("shell-agent's status = %q, want %q", shell.Status, configAgentStatus)
	}
	// A container row holds no value of its own: it opens the rows beneath it
	// (plan 350, the prototype's display() on a container).
	if main.Value != "" || main.Description != "" {
		t.Fatalf("main's row = %q / %q, want a label alone", main.Value, main.Description)
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
	// The entries the panel knows more about carry their fact as the
	// description; the custom entry — a plain container — counts what it
	// holds in the value column (plan 325 D4).
	cases := map[string]string{
		"#1": "agent override: openai/gpt-sub",
		"#2": "global default: openai/gpt-main / xHigh",
		"#3": configFollowConversation,
		"#5": configSystemSubagentNotice,
	}
	for label, want := range cases {
		if row := findRow(t, rows, label); row.Description != want {
			t.Errorf("%s = %q, want %q", label, row.Description, want)
		}
	}
	custom := findRow(t, rows, "#4")
	if custom.Value != "" || custom.Description != "" {
		t.Errorf("#4 = %q / %q, want a container row to hold no value of its own (plan 350)", custom.Value, custom.Description)
	}
	if !strings.Contains(pageLabels(selector), configSystemSubagentNotice) {
		t.Fatalf("the refusal must reach the page: %s", pageLabels(selector))
	}
	if len(session.writes) != 0 {
		t.Fatalf("nothing here writes, got %+v", session.writes)
	}
}

// The unified create is one form, and it writes once (owner 2026-11-22): `a`
// draws every field the saved page draws with nothing written, each row is an
// ordinary edit held in the section document, and the footer's Save lands the
// whole definition. Esc leaves nothing behind; a refusal keeps every value.
func TestConfigPanelCreatesAnAgentDefinition(t *testing.T) {
	newCase := func(t *testing.T, steps []richStep, inputs []selectorInput, stored string) (*configPanelTestSession, *scriptedSelector) {
		t.Helper()
		session := newConfigPanelSession(map[string]string{"agents": stored})
		selector := &scriptedSelector{}
		runConfigPanel(t, session, selector, steps, inputs, "agents")
		return session, selector
	}
	openDefinitions := richStep{label: "Agent definitions", ok: true}
	addRow := richStep{label: configAddRowLabel, ok: true}
	const storedMain = `{"definitions":{"main":{"primary":true}}}`
	// The create's submit on a picker without footer actions: the form's own
	// last row.
	save := richStep{label: configCreateSaveLabel, ok: true}
	// The form's page carries no locked rows: a create has nothing locked yet.
	createPage := func(t *testing.T, selector *scriptedSelector) []SelectItem {
		t.Helper()
		for _, items := range selector.richItems {
			if row, ok := findRowLoose(items, "Primary"); ok {
				if row.Locked {
					t.Fatalf("a create form must not lock its Primary row: %v", rowLabels(items))
				}
				return items
			}
		}
		t.Fatalf("no create form was drawn: %v", pageLabels(selector))
		return nil
	}

	t.Run("the create form comes first", func(t *testing.T) {
		// `a` draws the whole form at once — no name editor between (owner
		// 2026-11-22, the prototype's startAdd).
		session, selector := newCase(t,
			[]richStep{openDefinitions, addRow, {ok: false}, {ok: false}},
			nil, storedMain,
		)
		items := createPage(t, selector)
		name, ok := findRowLoose(items, "Name")
		if !ok {
			t.Fatalf("the create form must show its Name row: %v", rowLabels(items))
		}
		if name.Value != "" || name.Locked {
			t.Fatalf("the Name row = %+v, want empty and unlocked", name)
		}
		if len(session.writes) != 0 {
			t.Fatalf("opening the form writes nothing, got %+v", session.writes)
		}
	})

	t.Run("the save writes it whole", func(t *testing.T) {
		// Every field is held, then the one submit lands them together.
		session, _ := newCase(t,
			[]richStep{
				openDefinitions, addRow,
				{label: "Name", ok: true},
				{label: "Primary", ok: true},
				{label: configBoolOffLabel, ok: true},
				{label: "Description", ok: true},
				{label: "System prompt", ok: true},
				save,
			},
			[]selectorInput{
				{value: "repo-auditor", ok: true},
				{value: "Audits repos.", ok: true},
				{value: "You audit repositories.", ok: true},
			},
			storedMain,
		)
		if len(session.writes) != 1 {
			t.Fatalf("expected exactly one write, got %d: %+v", len(session.writes), session.writes)
		}
		if raw := string(session.writes[0].raw); !strings.Contains(raw, `"repo-auditor":{"description":"Audits repos.","primary":false,"system_prompt":"You audit repositories."}`) {
			t.Fatalf("raw = %s", raw)
		}
	})

	t.Run("Primary flips the role in memory", func(t *testing.T) {
		// A subagent-role value is held, then the role flips back and the
		// cleared value never reaches the store.
		session, selector := newCase(t,
			[]richStep{
				openDefinitions, addRow,
				{label: "Name", ok: true},
				{label: "Primary", ok: true},
				{label: configBoolOffLabel, ok: true},
				{label: "Tool access", ok: true},
				{label: "Pick", ok: true},
				{label: "Primary", ok: true},
				{label: configBoolOnLabel, ok: true},
				save,
			},
			[]selectorInput{{value: "review-agent", ok: true}},
			storedMain,
		)
		if !strings.Contains(pageLabels(selector), configCreateRoleNotice) {
			t.Fatalf("the role flip must say what it cleared: %s", pageLabels(selector))
		}
		if len(session.writes) != 1 {
			t.Fatalf("expected exactly one write, got %d: %+v", len(session.writes), session.writes)
		}
		raw := string(session.writes[0].raw)
		if !strings.Contains(raw, `"review-agent":{"primary":true}`) {
			t.Fatalf("raw = %s", raw)
		}
		if strings.Contains(raw, `"tools"`) {
			t.Fatalf("the cleared value must not save: %s", raw)
		}
	})

	t.Run("a name is required to save", func(t *testing.T) {
		session, selector := newCase(t,
			[]richStep{openDefinitions, addRow, save, {ok: false}, {ok: false}},
			nil, storedMain,
		)
		if !strings.Contains(pageLabels(selector), "A key is required.") {
			t.Fatalf("the empty-name refusal must be stated: %s", pageLabels(selector))
		}
		if len(session.writes) != 0 {
			t.Fatalf("a refused submit writes nothing, got %+v", session.writes)
		}
	})

	t.Run("an id the loader refuses is refused here", func(t *testing.T) {
		session, selector := newCase(t,
			[]richStep{
				openDefinitions, addRow,
				{label: "Name", ok: true},
				save, {ok: false}, {ok: false},
			},
			[]selectorInput{{value: "Bad ID", ok: true}},
			storedMain,
		)
		if !strings.Contains(pageLabels(selector), configPrimaryIDRefusal) {
			t.Fatalf("the id rule must be stated: %s", pageLabels(selector))
		}
		if len(session.writes) != 0 {
			t.Fatalf("a refused id writes nothing, got %+v", session.writes)
		}
	})

	t.Run("a subagent definition takes an underscore in its id", func(t *testing.T) {
		// The two roles have one id rule (owner 2026-11-22): the same name the
		// custom subagent entry's pattern would refuse is a definition's key.
		session, _ := newCase(t,
			[]richStep{
				openDefinitions, addRow,
				{label: "Primary", ok: true},
				{label: configBoolOffLabel, ok: true},
				{label: "Name", ok: true},
				save,
			},
			[]selectorInput{{value: "my_agent", ok: true}},
			storedMain,
		)
		if len(session.writes) != 1 {
			t.Fatalf("expected exactly one write, got %d: %+v", len(session.writes), session.writes)
		}
		if raw := string(session.writes[0].raw); !strings.Contains(raw, `"my_agent":{"primary":false}`) {
			t.Fatalf("raw = %s", raw)
		}
	})

	t.Run("a reserved subagent name is refused in both roles", func(t *testing.T) {
		for _, role := range []struct {
			name  string
			steps []richStep
		}{
			{name: "primary"},
			{name: "subagent", steps: []richStep{
				{label: "Primary", ok: true},
				{label: configBoolOffLabel, ok: true},
			}},
		} {
			t.Run(role.name, func(t *testing.T) {
				steps := append([]richStep{openDefinitions, addRow}, role.steps...)
				steps = append(steps, richStep{label: "Name", ok: true}, save, richStep{ok: false}, richStep{ok: false})
				session, selector := newCase(t, steps,
					[]selectorInput{{value: "explore", ok: true}},
					storedMain,
				)
				if !strings.Contains(pageLabels(selector), configReservedIDRefusal) {
					t.Fatalf("the reserved-name refusal must be stated: %s", pageLabels(selector))
				}
				if len(session.writes) != 0 {
					t.Fatalf("a refused name writes nothing, got %+v", session.writes)
				}
			})
		}
	})

	t.Run("a duplicate key is refused", func(t *testing.T) {
		session, selector := newCase(t,
			[]richStep{
				openDefinitions, addRow,
				{label: "Name", ok: true},
				save, {ok: false}, {ok: false},
			},
			[]selectorInput{{value: "main", ok: true}},
			storedMain,
		)
		if !strings.Contains(pageLabels(selector), "That key already exists. Choose a different key.") {
			t.Fatalf("the duplicate refusal must be stated: %s", pageLabels(selector))
		}
		if len(session.writes) != 0 {
			t.Fatalf("a duplicate key writes nothing, got %+v", session.writes)
		}
	})

	t.Run("Esc discards without a write", func(t *testing.T) {
		// The form's rows page counts the definitions it lists, so the same
		// count after Esc is the proof nothing landed.
		session, selector := newCase(t,
			[]richStep{openDefinitions, addRow, {ok: false}, {ok: false}, {ok: false}},
			nil, storedMain,
		)
		if len(session.writes) != 0 {
			t.Fatalf("Esc writes nothing, got %+v", session.writes)
		}
		if got := len(selector.richItems); got == 0 {
			t.Fatal("the list must be drawn again after the form closed")
		}
		definitions := selector.richItems[len(selector.richItems)-1]
		for _, item := range definitions {
			if item.Label == "" || item.Label == configAddRowLabel || item.Label == configUpRowLabel {
				continue
			}
			if strings.HasPrefix(item.Label, "New") && !strings.Contains(item.Label, "main") {
				t.Fatalf("the discarded draft reached the list: %v", rowLabels(definitions))
			}
		}
	})

	t.Run("a rejected submit keeps the form and every value", func(t *testing.T) {
		// The store's own refusal (a shape the loader will not take) leaves
		// the form open with what the user typed: the draft goes back under
		// its unnamed key and the loader's words become the page's notice.
		session := newConfigPanelSession(map[string]string{"agents": storedMain})
		session.writeErr = errors.New("agents.definitions.worker: invalid agent id")
		selector := &scriptedSelector{}
		runConfigPanel(t, session, selector, []richStep{
			{label: "Agent definitions", ok: true},
			addRow,
			{label: "Name", ok: true},
			{label: "Description", ok: true},
			{label: configCreateSaveLabel, ok: true},
			{ok: false}, {ok: false},
		}, []selectorInput{
			{value: "worker", ok: true},
			{value: "Audits repos.", ok: true},
		}, "agents")

		if len(session.writes) != 0 {
			t.Fatalf("the refused write must not be recorded, got %+v", session.writes)
		}
		if !strings.Contains(pageLabels(selector), "agents.definitions.worker: invalid agent id") {
			t.Fatalf("the loader's own sentence must be shown: %s", pageLabels(selector))
		}
		// The page after the refusal still holds what was typed.
		var kept []SelectItem
		for _, items := range selector.richItems {
			if row, ok := findRowLoose(items, "Name"); ok && !row.Locked {
				kept = items
			}
		}
		if kept == nil {
			t.Fatalf("the form must stay open after a refusal: %v", pageLabels(selector))
		}
		if row, _ := findRowLoose(kept, "Name"); row.Value != "worker" {
			t.Fatalf("the refused form lost its name: %+v", row)
		}
		if row, _ := findRowLoose(kept, "Description"); row.Value != "Audits repos." {
			t.Fatalf("the refused form lost its description: %+v", row)
		}
	})

	t.Run("a tab arrow asks before discarding a filled form", func(t *testing.T) {
		// ←/→ on a filled form asks first (owner 2026-11-22); Keep editing
		// stays on the form, and the submit still lands it.
		session := newConfigPanelSession(map[string]string{"agents": storedMain})
		selector := &actionPanelSelector{scriptedSelector: &scriptedSelector{}}
		runActionConfigPanel(t, session, selector,
			[]richStep{
				{label: "Agent definitions", ok: true},
				// The discard question, answered Keep editing.
				{label: configKeepEditingLabel, ok: true},
				{ok: false},
			},
			[]actionStep{
				{label: "Agent definitions", ok: true},
				{action: FooterActionAdd, ok: true},
				{label: "Name", ok: true},
				{tabStep: 1, ok: true},
				{action: FooterActionSave, ok: true},
				{ok: false},
			},
			[]inputActionStep{{value: "worker", ok: true}},
			"agents")

		if !strings.Contains(pageLabels(selector.scriptedSelector), "Discard the new definition?") {
			t.Fatalf("a tab arrow on a filled form must ask: %v", selector.richLabels)
		}
		if len(session.writes) != 1 {
			t.Fatalf("expected one write after Keep editing, got %d: %+v", len(session.writes), session.writes)
		}
		if raw := string(session.writes[0].raw); !strings.Contains(raw, `"worker":{"primary":true}`) {
			t.Fatalf("raw = %s", raw)
		}
	})

	t.Run("the footer's s Save submits the form", func(t *testing.T) {
		session := newConfigPanelSession(map[string]string{"agents": storedMain})
		selector := &actionPanelSelector{scriptedSelector: &scriptedSelector{}}
		runActionConfigPanel(t, session, selector,
			[]richStep{
				{label: "Agent definitions", ok: true},
				{ok: false},
			},
			[]actionStep{
				{label: "Agent definitions", ok: true},
				{action: FooterActionAdd, ok: true},
				{label: "Name", ok: true},
				{action: FooterActionSave, ok: true},
				{ok: false},
			},
			[]inputActionStep{{value: "worker", ok: true}},
			"agents")

		if len(session.writes) != 1 {
			t.Fatalf("expected exactly one write, got %d: %+v", len(session.writes), session.writes)
		}
		if raw := string(session.writes[0].raw); !strings.Contains(raw, `"worker":{"primary":true}`) {
			t.Fatalf("raw = %s", raw)
		}
		// The form's footer offers the submit under its own key.
		found := false
		for _, opts := range selector.opts {
			for _, action := range opts.FooterActions {
				if action.Kind == FooterActionSave && action.Label == configCreateSaveLabel && action.Key == configCreateSaveKey {
					found = true
				}
			}
		}
		if !found {
			t.Fatalf("the create form's footer must carry %s %s: %+v", configCreateSaveKey, configCreateSaveLabel, selector.opts)
		}
	})
}

// The create form's rows carry no lock: templateItems' creating flag is what
// tells a create page from a saved one (owner 2026-11-22).
func TestConfigPanelCreateFormLeavesTheCreatedFieldsOpen(t *testing.T) {
	panel := sectionPanel(map[string]string{"agents": `{"definitions":{"worker":{"primary":true}}}`})
	draft := map[string]any{"primary": true}
	items, _ := panel.templateItems(configTemplateAgent, "worker", draft, false, true)
	for _, label := range []string{"Name", "Primary"} {
		row, ok := findRowLoose(items, label)
		if !ok {
			t.Fatalf("the create page must show its %s row: %v", label, rowLabels(items))
		}
		if row.Locked {
			t.Fatalf("%s is locked on a create page: %+v", label, row)
		}
	}
	// The saved page keeps the lock: nothing exists to decide any more.
	items, _ = panel.templateItems(configTemplateAgent, "worker", draft, false, false)
	for _, label := range []string{"Name", "Primary"} {
		row, ok := findRowLoose(items, label)
		if !ok {
			t.Fatalf("the saved page must show its %s row: %v", label, rowLabels(items))
		}
		if !row.Locked {
			t.Fatalf("%s is open on a saved page: %+v", label, row)
		}
	}
}

// The two fields a create decides are locked on a saved definition's page
// (P3, the prototype's readOnly keyName/createEditable): opening either
// answers with the created-fields sentence instead of an editor.
func TestConfigPanelSavedDefinitionLocksItsCreatedFields(t *testing.T) {
	session := newConfigPanelSession(map[string]string{"agents": `{"definitions":{"main":{"primary":true}}}`})
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, []richStep{
		{label: "Agent definitions", ok: true},
		{label: "main", ok: true},
		{label: "Name", ok: true},
		{ok: false},
		{label: "Primary", ok: true},
		{ok: false},
		{ok: false},
		{ok: false},
	}, nil, "agents")
	if !strings.Contains(pageLabels(selector), configCreatedFieldsNotice) {
		t.Fatalf("the created-fields sentence must be stated: %s", pageLabels(selector))
	}
	if len(session.writes) != 0 {
		t.Fatalf("a locked field writes nothing, got %+v", session.writes)
	}
}

// findRowLoose reports whether one page carries a row labelled label.
func findRowLoose(items []SelectItem, label string) (SelectItem, bool) {
	for _, item := range items {
		if item.Label == label {
			return item, true
		}
	}
	return SelectItem{}, false
}

func TestConfigPanelEmptyStateCopy(t *testing.T) {
	// An empty collection: the rows page's own two lines, with the add
	// action the page offers.
	session := newConfigPanelSession(map[string]string{"sandbox_workspace_write": `{"writable_roots":[]}`})
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, []richStep{
		{label: "Workspace sandbox", ok: true},
		{label: "Extra writable roots", ok: true},
		{ok: false},
		{ok: false},
	}, nil)
	rowsPrompt := selector.richLabels[2]
	for _, want := range []string{"Nothing configured yet", "Add the first entry."} {
		if !strings.Contains(rowsPrompt, want) {
			t.Fatalf("the empty rows page is missing %q: %q", want, rowsPrompt)
		}
	}
	if strings.Contains(rowsPrompt, "This level has no rows yet") {
		t.Fatalf("the old empty line survived: %q", rowsPrompt)
	}
	if got := selector.richItems[2]; !slices.Contains(rowLabels(got), configAddRowLabel) {
		t.Fatalf("the empty rows page offers no add action: %v", rowLabels(got))
	}

	// A section the Claude Code gate emptied: the page says what is missing.
	session = newConfigPanelSession(nil)
	selector = &scriptedSelector{}
	runConfigPanel(t, session, selector, []richStep{{ok: false}}, nil, "peers")
	peersPrompt := selector.richLabels[0]
	for _, want := range []string{"No compatible installation found", "Claude Code interop appears when a local installation is available."} {
		if !strings.Contains(peersPrompt, want) {
			t.Fatalf("the gated peers page is missing %q: %q", want, peersPrompt)
		}
	}
}

// The prototype's copy contract, pinned verbatim (plan 327 D5): the section
// summaries and the field labels, hints and option labels the amended
// prototype fixes, so a wording drift fails here rather than in a screenshot.
func TestConfigPanelPrototypeCopy(t *testing.T) {
	summaries := map[string]string{
		"approval_preset":         "Choose what new sessions may do.",
		"sandbox_workspace_write": "Control writable paths and network access.",
		"batch":                   "Set the starting policy for new batch projects.",
		"compact":                 "Keep long conversations within their context window.",
		"memories":                "Tune retrieval, retention and background processing.",
		"cron":                    "Choose how long task records are retained.",
		"features":                "Enable memory, language intelligence and next-step suggestions.",
		"agents":                  "Manage definitions, shared defaults.",
		"hooks":                   "Run configured actions at lifecycle events.",
		"lsp":                     "Manage servers, timeouts and diagnostic delivery.",
		"peers":                   "Coordinate with other agent processes on this machine.",
	}
	for name, want := range summaries {
		schema, ok := configSectionSchemaFor(name)
		if !ok {
			t.Errorf("no %s section", name)
			continue
		}
		if schema.Summary != want {
			t.Errorf("%s's summary = %q, want the prototype's %q", name, schema.Summary, want)
		}
	}
	// Representative field labels and hints, quoted from the prototype's own
	// section table (html:72-87) — the corrections this plan exists for.
	type fieldCopy struct{ section, path, label, hint string }
	for _, want := range []fieldCopy{
		{"approval_preset", "", "Default approval mode", "This setting affects new sessions, not the current conversation."},
		{"sandbox_workspace_write", "network_access", "Network access", "Allow sandboxed commands to access the network."},
		{"batch", "sandbox_default", "Enable sandbox", "Existing batch projects keep their own sandbox policy."},
		{"compact", "auto_compact_percent", "Auto-compact at", "Compact when context usage reaches this percentage."},
		{"compact", "prompt", "Hand-off prompt", "Leave empty to use the built-in compaction prompt."},
		{"memories", "max_raw_memories_for_consolidation", "Memories per consolidation", ""},
		{"memories", "max_unused_days", "Expire unused memories after", ""},
		{"memories", "max_rollout_age_days", "Keep records for", ""},
		{"memories", "min_rollout_idle_hours", "Minimum idle time", ""},
		{"cron", "retention_days", "Keep task records for", ""},
		{"features", "memories", "Memory", "Remember useful context across conversations."},
		{"features", "prompt_suggestions", "Next-step suggestions", "Predict your next message as ghost text in the idle composer; Tab or Right takes it."},
		{"agents", "defaults.network_retry_max", "Network retries", "Zero disables network retries."},
		{"agents", "defaults.stream_idle_timeout_seconds", "Stream idle timeout", "Zero disables the watchdog. Positive values below 10 run as 10."},
		{"agents", "defaults.context_inject.warn_remaining_percent", "Warn below remaining context", ""},
		{"agents", "defaults.mcp_servers", "MCP servers", "Shared with all agents. Agent entries with the same name override these."},
		{"agents", "subagent_models", "Built-in subagents", "The 6 built-in subagent types, always on. Each carries an install-wide model and thinking level; an agent may override them."},
		{"hooks", "", "Hook events", "Command, prompt, agent and HTTP hooks are supported."},
		{"lsp", "recommendations", "Recommend language servers", "Suggest useful servers for this project."},
		{"lsp", "servers", "Configured servers", "Each server maps files and languages to a process."},
		{"lsp", "max_servers", "Concurrent servers", ""},
		{"lsp", "idle_timeout", "Exit when idle for", "Zero uses the default. Other values must be at least 60 seconds."},
		{"lsp", "diagnostics.after_edit", "Diagnose after edits", "Request diagnostics after a file changes."},
		{"lsp", "diagnostics.wait_ms", "Wait for diagnostics", "Zero does not wait."},
		{"lsp", "diagnostics.min_severity", "Minimum severity", "Include this severity and all more severe diagnostics."},
		{"lsp", "diagnostics.max_per_file", "Diagnostics per file", ""},
		{"lsp", "diagnostics.max_files", "Files per report", ""},
		{"lsp", "diagnostics.late_delivery", "Deliver late diagnostics", "Deliver diagnostics that arrive after the initial wait."},
		{"peers", "claude_code", "Claude Code interop", "Processes with different interop settings cannot see each other."},
	} {
		schema, ok := configSectionSchemaFor(want.section)
		if !ok {
			t.Errorf("no %s section", want.section)
			continue
		}
		found := false
		for _, field := range schema.Fields {
			if field.Path != want.path {
				continue
			}
			found = true
			if field.Label != want.label {
				t.Errorf("%s.%s's label = %q, want %q", want.section, want.path, field.Label, want.label)
			}
			if field.Hint != want.hint {
				t.Errorf("%s.%s's hint = %q, want %q", want.section, want.path, field.Hint, want.hint)
			}
		}
		if !found {
			t.Errorf("%s has no field at %q", want.section, want.path)
		}
	}
	// An empty hint on a number is the allowed-range fallback (plan 327 D3):
	// the hint the page shows, not the one the descriptor carries.
	lsp, _ := configSectionSchemaFor("lsp")
	for _, field := range lsp.Fields {
		if field.Path == "max_servers" {
			if got := configFieldHint(field); got != "Allowed range: 0–32 servers." {
				t.Errorf("max_servers's help = %q, want the allowed-range fallback", got)
			}
		}
	}
}

// An unset setting reads as nothing at all, bar a switch, which reads as the
// value its default gives it; a stored number carries its unit; and no setting
// carries a status of its own (plan 350, the prototype's display()).
func TestConfigPanelDefaultArrowUnitsAndStatus(t *testing.T) {
	// A whole section nobody stored: every row is unset.
	panel := sectionPanel(nil)
	cases := []struct {
		section, label, value string
	}{
		{"cron", "Keep task records for", ""},
		{"approval_preset", "Default approval mode", ""},
		{"features", "Memory", configBoolOnLabel},
		{"agents", "Enable subagents", configBoolOffLabel},
		{"compact", "Hand-off prompt", ""},
		{"lsp", "Minimum severity", ""},
	}
	for _, tc := range cases {
		schema, ok := configSectionSchemaFor(tc.section)
		if !ok {
			t.Fatalf("no %s section", tc.section)
		}
		items, _ := panel.fieldItems(schema, panel.sectionDoc(tc.section), false)
		row := findRow(t, items, tc.label)
		if row.Value != tc.value {
			t.Errorf("%s's value = %q, want %q", tc.label, row.Value, tc.value)
		}
		if row.Status != "" {
			t.Errorf("%s's status = %q, want none", tc.label, row.Status)
		}
	}

	// A stored number carries the unit it is counted in and marks nothing.
	panel = sectionPanel(map[string]string{"cron": `{"retention_days":45}`, "agents": `{"defaults":{"network_retry_max":10}}`})
	cron, _ := configSectionSchemaFor("cron")
	items, _ := panel.fieldItems(cron, panel.sectionDoc("cron"), false)
	if row := findRow(t, items, "Keep task records for"); row.Value != "45 days" || row.Status != "" {
		t.Fatalf("the stored number = %q %q, want 45 days and no status", row.Value, row.Status)
	}
	agents, _ := configSectionSchemaFor("agents")
	items, _ = panel.fieldItems(agents, panel.sectionDoc("agents"), false)
	if row := findRow(t, items, "Network retries"); row.Value != "10 attempts" || row.Status != "" {
		t.Fatalf("the stored retries = %q %q, want 10 attempts and no status", row.Value, row.Status)
	}
}

// A write that names a system subagent entry is still the loader's to refuse:
// the panel's early guard explains the row, and the validator judges the
// section the edit would store.
func TestConfigPanelSurfacesTheLoadersSystemEntryRefusal(t *testing.T) {
	session := newConfigPanelSession(map[string]string{"agents": `{"definitions":{"main":{"primary":true,"subagents":[{"name":"goal-evaluator"}]}}}`})
	session.writeErr = errStubSystemEntryRefusal
	selector := &scriptedSelector{}
	renderer := runConfigPanel(t, session, selector, []richStep{
		{label: "Network retries", ok: true},
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
	return `agents.definitions.main.subagents[0]: "goal-evaluator" is dispatched by the system and cannot be configured.`
}

// The prototype's validation rules are the store's, surfaced word for word
// (plan 328 D5): the built-in restriction and the effort that requires a
// model are enforced where the section is written, and the panel's own gates
// — the effort picker's notice, the read-only system row, the fields a
// built-in row never offers — keep the same states from being reached. This
// pins the parity: each refusal reaches the page as the store said it, and
// nothing is written.
func TestConfigPanelValidationMessages(t *testing.T) {
	for _, tc := range []struct {
		name    string
		stored  map[string]string
		err     error
		steps   []richStep
		inputs  []selectorInput
		section string
		want    string
	}{
		{
			name:    "a built-in given settings it cannot take",
			stored:  map[string]string{"agents": `{"definitions":{"main":{"primary":true,"subagents":[{"name":"explore","description":"no"}]}}}`},
			err:     stubBuiltInRestrictionError{},
			steps:   []richStep{{label: "Network retries", ok: true}, {ok: false}},
			inputs:  []selectorInput{{value: "5", ok: true}},
			section: "agents",
			want:    `is a built-in: it takes a model, a reasoning effort, a system prompt and enabled, nothing else.`,
		},
		{
			name:    "a goal evaluator level without a model",
			stored:  map[string]string{"agents": `{"definitions":{"main":{"primary":true}},"goal_evaluator":{"reasoning_effort":"high"}}`},
			err:     stubGoalEvaluatorEffortError{},
			steps:   []richStep{{label: "Network retries", ok: true}, {ok: false}},
			inputs:  []selectorInput{{value: "5", ok: true}},
			section: "agents",
			want:    "Thinking level requires a Goal Evaluator model.",
		},
		{
			name:    "a per-type level without a model",
			stored:  map[string]string{"agents": `{"definitions":{"main":{"primary":true}},"subagent_models":{"explore":{"reasoning_effort":"high"}}}`},
			err:     stubEffortNeedsModelError{where: "subagent_models.explore"},
			steps:   []richStep{{label: "Network retries", ok: true}, {ok: false}},
			inputs:  []selectorInput{{value: "5", ok: true}},
			section: "agents",
			want:    "subagent_models.explore: thinking level requires a model",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := newConfigPanelSession(tc.stored)
			session.writeErr = tc.err
			selector := &scriptedSelector{}
			runConfigPanel(t, session, selector, tc.steps, tc.inputs, tc.section)

			if !strings.Contains(pageLabels(selector), tc.want) {
				t.Fatalf("the store's refusal must reach the page: %s", pageLabels(selector))
			}
			if len(session.writes) != 0 {
				t.Fatalf("a refused candidate writes nothing, got %+v", session.writes)
			}
		})
	}
}

// stubBuiltInRestrictionError stands in for the loader's own sentence about a
// built-in subagent entry given settings it cannot take
// (pkg/config/agents.go: the where-prefixed wording).
type stubBuiltInRestrictionError struct{}

func (stubBuiltInRestrictionError) Error() string {
	return `agents.definitions.main.subagents[0]: "explore" is a built-in: it takes a model, a reasoning effort, a system prompt and enabled, nothing else.`
}

// stubEffortNeedsModelError stands in for the loader's own sentence about a
// thinking level that names no model (pkg/config/agents.go
// validateSubagentModel, where-prefixed: plan reviewer, type row, per-agent
// entry).
type stubEffortNeedsModelError struct{ where string }

func (e stubEffortNeedsModelError) Error() string {
	return e.where + ": thinking level requires a model."
}

// stubGoalEvaluatorEffortError stands in for the loader's own sentence about a
// goal evaluator thinking level that names no model: the goal row's own words,
// with no where prefix.
type stubGoalEvaluatorEffortError struct{}

func (stubGoalEvaluatorEffortError) Error() string {
	return "Thinking level requires a Goal Evaluator model."
}

// No page of the panel names a setting by its dotted configuration path: the
// row that cannot hold settings is named the way the row list names it, so a
// reader never sees a key they cannot type anywhere (plan 313 Step 5b).
func TestConfigPanelNamesRowsWithoutADottedPath(t *testing.T) {
	session := newConfigPanelSession(map[string]string{"agents": `{"defaults":"taken"}`})
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, []richStep{
		{label: "Network retries", ok: true},
		{ok: false},
		{ok: false},
	}, []selectorInput{{value: "5", ok: true}}, "agents")

	pages := pageLabels(selector)
	if !strings.Contains(pages, "Network retries is not an object or a list") {
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

// The Built-in subagents entry is the six install-wide rows in one page
// (P4, the prototype's builtinSubagents kind): the four public types' pins
// under agents.subagent_models and the two system types' global rows under
// agents.plan_reviewer and agents.goal_evaluator, each read as the pin it is
// (plan 324 D7) and saved back to the agents section, whose unseen keys
// survive the write. Its Model field opens the global-row picker, whose pick
// takes the pin's level with it (D5).
func TestConfigPanelBuiltinSubagentsEntryProjectsTheAgentsStore(t *testing.T) {
	agents := `{
		"definitions":{"main":{"primary":true,"subagents":[]},"repo-auditor":{"primary":false,"description":"audits one repository area","system_prompt":"You audit repositories.","tools":"read_only"}},
		"plan_reviewer":{"model":"llm-2"},
		"subagent_models":{"explore":{"model":"llm-1","reasoning_effort":"high"}}
	}`
	session := newConfigPanelSession(map[string]string{"agents": agents})
	session.snapshot.Models = []appcfg.LLMModelConfig{
		{ID: "llm-1", Provider: "openai", Model: "gpt-main"},
		{ID: "llm-2", Provider: "deepseek", Model: "deepseek-v4-pro"},
	}
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, []richStep{
		{label: "Built-in subagents", ok: true},
		{label: "explore", ok: true},
		{label: "Model", ok: true},
		{label: "deepseek/deepseek-v4-pro", ok: true},
	}, nil, "agents")

	if len(selector.richItems) < 2 {
		t.Fatal("the entry page never opened")
	}
	page := selector.richItems[1]
	// A pin names the model row its value column holds, with the thinking
	// level as its status and the prototype's help sentence as its
	// description — the one sentence all six rows carry.
	explore := findRow(t, page, "explore")
	if explore.Value != "openai/gpt-main" || explore.Status != "High" {
		t.Fatalf("explore's row = %q %q, want the named row and its level", explore.Value, explore.Status)
	}
	// The help sentence belongs to the type's editor, not to its row (plan
	// 350): the row is the name, the model row it holds and the level.
	if explore.Description != "" {
		t.Fatalf("explore's row carries %q; the help belongs to its editor", explore.Description)
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
	// The goal evaluator is the sixth row, the plan reviewer's sibling.
	findRow(t, page, "goal-evaluator")

	// The save lands in the agents section — the document the page presents —
	// and every key the page does not show comes back untouched. The pick
	// changed the pin's model, so its level went with it in the same write.
	if len(session.writes) != 1 {
		t.Fatalf("expected one write, got %+v", session.writes)
	}
	if session.writes[0].section != "agents" {
		t.Fatalf("the page saved to %q, want the agents store", session.writes[0].section)
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
}

func sharedRowsSession(agentSubagents string) *configPanelTestSession {
	agents := `{
		"definitions":{
			"main":{"primary":true,"subagents":` + agentSubagents + `},
			"repo-auditor":{"primary":false,"order":1,"description":"audits one repository area","system_prompt":"You audit repositories.","model":"llm-1","reasoning_effort":"high","tools":"read_only"}
		},
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
	// definition's description marked as shared. The page is the agent's own
	// subagent list, not the definitions page that also lists the definition.
	var rowsPage []SelectItem
	for _, items := range selector.richItems {
		labels := rowLabels(items)
		if slices.Contains(labels, "repo-auditor") && !slices.Contains(labels, "main") {
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
	// not marked shared, and its description is its own. The agent's own
	// subagent list is the page that holds the definition's name without the
	// definitions page's rows.
	var rowsPage []SelectItem
	for _, items := range selector.richItems {
		labels := rowLabels(items)
		if slices.Contains(labels, "repo-auditor") && !slices.Contains(labels, "main") {
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
func effortPickerItems(t *testing.T, selector *scriptedSelector) []SelectItem {
	t.Helper()
	for _, items := range selector.richItems {
		if slices.Contains(rowLabels(items), configRowDefaultEffort) {
			return items
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
		{label: "Built-in subagents", ok: true},
		{label: "goal-evaluator", ok: true},
		{label: "Thinking level", ok: true},
		{ok: false},
	}, nil, "agents")

	got := rowLabels(effortPickerItems(t, selector))
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
		{label: "Built-in subagents", ok: true},
		{label: "goal-evaluator", ok: true},
		{label: "Thinking level", ok: true},
		{ok: false},
	}, nil, "agents")

	got := rowLabels(effortPickerItems(t, selector))
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
		{label: "Built-in subagents", ok: true},
		{label: "goal-evaluator", ok: true},
		{label: "Thinking level", ok: true},
		{ok: false},
	}, nil, "agents")

	got := rowLabels(effortPickerItems(t, selector))
	want := []string{"Low", "Medium", "High", "xHigh", configRowDefaultEffort}
	if !slices.Equal(got, want) {
		t.Fatalf("picker = %v, want %v", got, want)
	}
}

// A level belongs to the model beside it: with no model there is nothing to
// offer, and the page says which setting to choose first instead of inventing a
// list. The loader refuses such a pair too ("Thinking level requires a goal
// check model.").
func TestConfigPanelEffortPickerNeedsAModel(t *testing.T) {
	useSetupCatalogWith(t, configEffortCatalog)
	session := newConfigPanelSession(map[string]string{"agents": `{"goal_evaluator":{}}`})
	session.snapshot.Models = configEffortModels()
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, []richStep{
		{label: "Built-in subagents", ok: true},
		{label: "goal-evaluator", ok: true},
		{label: "Thinking level", ok: true},
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
				{label: "Built-in subagents", ok: true},
				{label: "goal-evaluator", ok: true},
				{label: "Thinking level", ok: true},
				{label: tc.pick, ok: true},
			}, nil, "agents")

			if len(session.writes) != 1 {
				t.Fatalf("expected one write, got %+v", session.writes)
			}
			var saved struct {
				GoalEvaluator map[string]any `json:"goal_evaluator"`
			}
			if err := json.Unmarshal(session.writes[0].raw, &saved); err != nil {
				t.Fatalf("the saved document does not parse: %v", err)
			}
			level, ok := saved.GoalEvaluator["reasoning_effort"]
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
		{label: "Built-in subagents", ok: true},
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
	}, nil, "agents")

	got := rowLabels(effortPickerItems(t, selector))
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

// The effort value wording is the prototype's frozen two-word split (plan 327
// D1, prototype html:117-118/127): a stored level — the empty string a Row
// default pick stores included — is a choice and reads as the row's own
// default, while an effort key nobody stored leaves the choice to the
// provider; the projection's overview status reads the stored level alone and
// carries nothing when no model is pinned.
func TestConfigPanelEffortValueLabels(t *testing.T) {
	useSetupCatalogWith(t, configEffortCatalog)
	agents := `{
		"definitions":{"main":{"primary":true,"subagents":[{"name":"explore","model":"llm-grok"}]}},
		"goal_evaluator":{"model":"llm-grok"},
		"plan_reviewer":{"model":"llm-grok"},
		"subagent_models":{
			"plan":{"model":"llm-grok","reasoning_effort":""},
			"explore":{"model":"llm-grok"},
			"verification":{}
		}
	}`

	// Path (a), the goal evaluator's own frame: its reasoning_effort key is
	// absent, so the provider decides.
	session := newConfigPanelSession(map[string]string{"agents": agents})
	session.snapshot.Models = configEffortModels()
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, []richStep{
		{label: "Built-in subagents", ok: true},
		{label: "goal-evaluator", ok: true},
		{ok: false},
		{ok: false},
	}, nil, "agents")
	var goalPage []SelectItem
	for _, items := range selector.richItems {
		if _, ok := findRowLoose(items, "Thinking level"); ok {
			goalPage = items
			break
		}
	}
	if goalPage == nil {
		t.Fatalf("the goal evaluator's page never opened: %v", selector.richLabels)
	}
	sectionLevel := findRow(t, goalPage, "Thinking level")
	if sectionLevel.Value != configRowDefaultEffort {
		t.Fatalf("the unset frame level = %q, want %q", sectionLevel.Value, configRowDefaultEffort)
	}

	// Path (b) and the type frames, over the projection's own page.
	selector = &scriptedSelector{}
	runConfigPanel(t, session, selector, []richStep{
		{label: "Built-in subagents", ok: true},
		{label: "plan", ok: true},
		{ok: false},
		{label: "explore", ok: true},
		{ok: false},
	}, nil, "agents")
	overview := selector.richItems[1]
	// The entry page's status names the stored level: "" is the row's own
	// default, and a pin nobody set carries no status at all.
	if plan := findRow(t, overview, "plan"); plan.Status != configRowDefaultEffort {
		t.Fatalf("plan's status = %q, want %q", plan.Status, configRowDefaultEffort)
	}
	verification := findRow(t, overview, "verification")
	if verification.Status != "" || verification.Value != configFollowConversation {
		t.Fatalf("verification's row = %q %q, want the follow sentence and no status", verification.Value, verification.Status)
	}
	// The stored empty level on a type frame is the row's own default; the
	// absent one beside it leaves the choice to the provider.
	var planPage, explorePage []SelectItem
	for _, items := range selector.richItems[1:] {
		labels := rowLabels(items)
		if !slices.Contains(labels, "Model") || !slices.Contains(labels, "Thinking level") {
			continue
		}
		if slices.Contains(labels, "System prompt") {
			continue // a definition's subagent entry, not a type frame
		}
		if planPage == nil {
			planPage = items
		} else {
			explorePage = items
		}
	}
	if planPage == nil || explorePage == nil {
		t.Fatalf("both type frames must open: %v", selector.richLabels)
	}
	if got := findRow(t, planPage, "Thinking level").Value; got != configRowDefaultEffort {
		t.Fatalf("the stored empty level = %q, want %q", got, configRowDefaultEffort)
	}
	if got := findRow(t, explorePage, "Thinking level").Value; got != configRowDefaultEffort {
		t.Fatalf("the unset frame level = %q, want %q", got, configRowDefaultEffort)
	}
}

// A choice page is the option list alone (plan 327 D2, owner 2026-11-18
// 「删掉 Choose one 这种废话、无意义的文案」): no "Choose one", no "Choose one
// model; …", no "Choose the default for …" — the head is the field's own label
// (and help, where the schema carries one), and the option the field already
// holds is the row carrying the check.
func TestConfigPanelChoiceHeadCopy(t *testing.T) {
	useSetupCatalogWith(t, configEffortCatalog)
	// The goal evaluator's frame carries the pair (P4): its page is the
	// system row's template, whose fields the choice copy is judged on.
	var effortField, modelField configField
	for _, field := range configTemplates[configTemplateGlobalSystemRow] {
		switch {
		case field.Kind == configFieldEffort:
			effortField = field
		case field.Kind == configFieldModel:
			modelField = field
		}
	}
	if effortField.Label == "" || modelField.Label == "" {
		t.Fatal("the system row template must carry the effort and model fields")
	}
	withHint := func(field configField) string {
		if strings.TrimSpace(field.Hint) == "" {
			return field.Label
		}
		return field.Label + "\n" + field.Hint
	}

	session := newConfigPanelSession(map[string]string{"agents": `{"goal_evaluator":{"model":"llm-grok","reasoning_effort":"low"}}`})
	session.snapshot.Models = configEffortModels()
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, []richStep{
		{label: "Built-in subagents", ok: true},
		{label: "goal-evaluator", ok: true},
		{label: effortField.Label, ok: true},
		{ok: false},
		{label: modelField.Label, ok: true},
		{ok: false},
	}, nil, "agents")

	for _, label := range selector.richLabels {
		for _, filler := range []string{"Choose one", "Choose the default"} {
			if strings.Contains(label, filler) {
				t.Errorf("a choice page still carries the filler %q: %q", filler, label)
			}
		}
	}
	// A choice page carries no head of its own (plan 350): the tab bar stays
	// and the rows are the values alone, so neither the field's label nor its
	// help reaches the page.
	for _, label := range selector.richLabels {
		for _, gone := range []string{effortField.Label, modelField.Label, withHint(effortField)} {
			if strings.Contains(label, gone) {
				t.Fatalf("a choice page still heads itself with %q: %q", gone, label)
			}
		}
	}
	// The stored choices carry the check, and only they do.
	if row := findRow(t, effortPickerItems(t, selector), "Low"); row.Status != configChoiceCheckStatus {
		t.Fatalf("the stored level's option = %+v, want the check", row)
	}
	page := modelPickerPage(t, selector)
	if row := findRow(t, page, "xai/grok-effort"); row.Status != configChoiceCheckStatus {
		t.Fatalf("the stored model row = %+v, want the check", row)
	}
	if row := findRow(t, page, "Follow conversation"); row.Status != "" {
		t.Fatalf("the empty option carries %q, want no check while a row is stored", row.Status)
	}

	// A switch's choice page: the option it holds carries the check, and an
	// unset switch names neither.
	session = newConfigPanelSession(map[string]string{"features": `{"memories":true}`})
	selector = &scriptedSelector{}
	runConfigPanel(t, session, selector, []richStep{
		{label: "Feature switches", ok: true},
		{label: "Memory", ok: true},
		{ok: false},
		{label: "Language servers", ok: true},
		{ok: false},
		{ok: false},
	}, nil)
	pages := selector.richItems
	on := findRow(t, pages[2], configBoolOnLabel)
	off := findRow(t, pages[2], configBoolOffLabel)
	if on.Status != configChoiceCheckStatus || off.Status != "" {
		t.Fatalf("the switch's options = %q/%q, want the check on On alone", on.Status, off.Status)
	}
	if unset := findRow(t, pages[4], configBoolOnLabel); unset.Status != "" {
		t.Fatalf("an unset switch's On option carries %q, want no check", unset.Status)
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

	got := rowLabels(effortPickerItems(t, selector))
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

// modelPickerPage is the one page a model picker drew, found by the empty
// option every model picker lists first (plan 327 D2: the page's head is the
// field's label alone, so the rows are what identify it).
func modelPickerPage(t *testing.T, selector *scriptedSelector) []SelectItem {
	t.Helper()
	for _, items := range selector.richItems {
		if len(items) > 0 && strings.HasPrefix(items[0].Label, "Follow ") {
			return items
		}
	}
	t.Fatalf("no model picker opened: %v", selector.richLabels)
	return nil
}

// A model reference is picked from the install's global table, never typed
// (plan 324): the picker's first row is the field's own empty value — the goal
// check's carries the conversation sentence — and every row after it names a
// model row and where its requests go. A stored reference the table does not
// carry is not kept on the goal evaluator's list.
func TestConfigPanelModelPickerListsGlobalRows(t *testing.T) {
	session := newConfigPanelSession(map[string]string{"agents": `{"goal_evaluator":{"model":"llm-gone"}}`})
	session.snapshot.Models = []appcfg.LLMModelConfig{
		{ID: "llm-main", Provider: "openai", Model: "gpt-main", BaseURL: "https://api.openai.com", APIPath: "/v1/chat/completions"},
		{ID: "llm-claude", Provider: "anthropic", Model: "claude-sonnet-4-5", BaseURL: "https://eu.api.anthropic.com", APIPath: "/v1/messages"},
	}
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, []richStep{
		{label: "Built-in subagents", ok: true},
		{label: "goal-evaluator", ok: true},
		{label: "Model", ok: true},
		{ok: false},
	}, nil, "agents")

	page := modelPickerPage(t, selector)
	labels := rowLabels(page)
	want := []string{"Follow conversation", "openai/gpt-main", "anthropic/claude-sonnet-4-5", "llm-gone"}
	if !slices.Equal(labels, want) {
		t.Fatalf("picker = %v, want %v", labels, want)
	}
	empty := findRow(t, page, "Follow conversation")
	if empty.Description != "No global pin; the system chooses at dispatch." {
		t.Fatalf("the goal evaluator's empty option = %q, want the system row's sentence", empty.Description)
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
		{label: "Built-in subagents", ok: true},
		{label: "explore", ok: true},
		{label: "Model", ok: true},
		{label: "openai/gpt-main", ok: true},
	}, nil, "agents")

	// The section row names the dangling pin as written, and the picker keeps
	// it after the real rows with no description of its own.
	if explore := findRow(t, selector.richItems[1], "explore"); explore.Value != "llm-gone" {
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

// The model row label is the bare provider/model display name (owner
// 2026-11-22: no host suffix): two rows of one provider and model read the
// same name, told apart by the endpoint the picker draws beside them, and a
// reference nothing carries is shown as written.
func TestConfigPanelModelRowLabelIsProviderModel(t *testing.T) {
	panel := &configPanel{models: []appcfg.LLMModelConfig{
		{ID: "llm-main", Provider: "example", Model: "reasoner-pro", BaseURL: "https://models.example.com"},
		{ID: "llm-eu", Provider: "example", Model: "reasoner-pro", BaseURL: "https://eu.models.example.com"},
		{ID: "llm-fast", Provider: "example", Model: "fast-small", BaseURL: "https://models.example.com"},
	}}
	for _, tc := range []struct{ ref, want string }{
		{"llm-main", "example/reasoner-pro"},
		{"llm-eu", "example/reasoner-pro"},
		{"llm-fast", "example/fast-small"},
		{"llm-gone", "llm-gone"},
		{"example/reasoner-pro", "example/reasoner-pro"},
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
		{label: "Built-in subagents", ok: true},
		{label: "explore", ok: true},
		{label: "Model", ok: true},
		{label: "Follow conversation", ok: true},
	}, nil, "agents")

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
		{label: "Model", ok: true},
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
		{label: "Built-in subagents", ok: true},
		{label: "goal-evaluator", ok: true},
		{label: "Model", ok: true},
		{label: "deepseek/deepseek-v4-pro", ok: true},
	}, nil, "agents")

	if len(session.writes) != 1 {
		t.Fatalf("the change is one write, got %+v", session.writes)
	}
	var saved struct {
		GoalEvaluator map[string]any `json:"goal_evaluator"`
	}
	if err := json.Unmarshal(session.writes[0].raw, &saved); err != nil {
		t.Fatalf("the saved document does not parse: %v", err)
	}
	if got, _ := saved.GoalEvaluator["model"].(string); got != "llm-2" {
		t.Fatalf("goal_evaluator.model = %q, want the picked row", got)
	}
	if _, kept := saved.GoalEvaluator["reasoning_effort"]; kept {
		t.Fatalf("goal_evaluator.reasoning_effort = %v, want the old model's level gone", saved.GoalEvaluator["reasoning_effort"])
	}
}

// Picking the row a reference already names changes nothing but the write
// itself (plan 324 D5): the level it was chosen for stays.
func TestConfigPanelModelSameRowKeepsEffort(t *testing.T) {
	session := newConfigPanelSession(map[string]string{"agents": `{"goal_evaluator":{"model":"llm-1","reasoning_effort":"high"}}`})
	session.snapshot.Models = []appcfg.LLMModelConfig{{ID: "llm-1", Provider: "openai", Model: "gpt-main"}}
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, []richStep{
		{label: "Built-in subagents", ok: true},
		{label: "goal-evaluator", ok: true},
		{label: "Model", ok: true},
		{label: "openai/gpt-main", ok: true},
	}, nil, "agents")

	if len(session.writes) != 1 {
		t.Fatalf("expected one write, got %+v", session.writes)
	}
	var saved struct {
		GoalEvaluator map[string]any `json:"goal_evaluator"`
	}
	if err := json.Unmarshal(session.writes[0].raw, &saved); err != nil {
		t.Fatalf("the saved document does not parse: %v", err)
	}
	if got, _ := saved.GoalEvaluator["model"].(string); got != "llm-1" {
		t.Fatalf("goal_evaluator.model = %q, want the same row", got)
	}
	if got, _ := saved.GoalEvaluator["reasoning_effort"].(string); got != "high" {
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
		{label: "Built-in subagents", ok: true},
		{label: "goal-evaluator", ok: true},
		{label: "Model", ok: true},
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
		{label: "Built-in subagents", ok: true},
		{label: "goal-evaluator", ok: true},
		{label: "Thinking level", ok: true},
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
		{label: "Built-in subagents", ok: true},
		{label: "explore", ok: true},
		{label: "Model", ok: true},
		{label: "deepseek/r1", ok: true},
	}, "agents")
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
		{label: "Built-in subagents", ok: true},
		{label: "explore", ok: true},
		{label: "Model", ok: true},
		{label: configFollowConversation, ok: true},
	}, "agents")
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

// --- Plan 325: the config pages as plain shared pickers ---

// renderChromePickerPage was the chrome-page helper of plan 329; the pages it
// laid out are the plain config pages now (plan 325).
func renderConfigPickerPage(items []SelectItem, label string, width int) []string {
	return renderRichPage(items, label, width)
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
		{label: "MCP servers", ok: true},
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
		{label: "Built-in subagents", ok: true},
		{label: "explore", ok: true},
		{ok: false},
	}, nil, "agents")

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
	if findRow(t, page, "Thinking level").Value != configRowDefaultEffort {
		t.Fatalf("the unset level must read %q, the model row's own default", configRowDefaultEffort)
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
		// The field's help belongs to its editor, never to the row (plan
		// 350): a read-only page says what it is with its status alone.
		if item.Description != "" {
			t.Errorf("%s's row carries %q; a row names the setting and its value alone", item.Label, item.Description)
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

// A /config sub-page is a plain shared picker, the way every other slash
// panel's page is (owner 2026-11-18, plan 325 D1): its head is the title and
// the subtitle alone — no breadcrumb line, no persistent tab bar, no tagged
// title — while the overview keeps its tab bar, which is navigation the way
// /status keeps its own.
func TestConfigPanelSubPagesHaveNoBreadcrumbOrTabs(t *testing.T) {
	panel := sectionPanel(map[string]string{"agents": `{"definitions":{"main":{"primary":true,"display_name":"Main agent"}}}`})
	agents, ok := configSectionSchemaFor("agents")
	if !ok {
		t.Fatal("no agents section")
	}
	items, _ := panel.fieldItems(agents, panel.sectionDoc("agents"), false)
	lines := renderConfigPickerPage(items, agents.Title+"\n"+agents.Summary, 120)
	plain := stripANSIAll(lines)
	if len(plain) < 4 {
		t.Fatalf("the section page is too short to check:\n%s", strings.Join(plain, "\n"))
	}
	// The head is the title and the subtitle alone, then the page's own
	// blank line (plan 325 D5): anything else — a tab bar, a breadcrumb —
	// would take one of these lines.
	if strings.TrimSpace(plain[0]) != agents.Title {
		t.Fatalf("the page's first head line = %q, want the title", plain[0])
	}
	if strings.TrimSpace(plain[1]) != agents.Summary {
		t.Fatalf("the page's second head line = %q, want the summary", plain[1])
	}
	if strings.TrimSpace(plain[2]) != "" {
		t.Fatalf("the line under the head = %q, want the blank line before the first row", plain[2])
	}
	for i, line := range plain {
		if i < 3 {
			continue
		}
		if trail := strings.TrimSpace(line); strings.HasPrefix(trail, "Configuration") && strings.Contains(trail, " / ") {
			t.Errorf("a breadcrumb line survived: %q", line)
		}
		for _, tab := range []string{configTabApproval, configTabSession, configTabAgents} {
			if strings.HasPrefix(strings.TrimSpace(line), tab+" ") {
				t.Errorf("the sub-page carries the persistent tab bar: %q", line)
			}
		}
	}

	// The overview keeps its tab bar — navigation, the way /status keeps its
	// own.
	sectionItems, _ := panel.sectionItems()
	overview := renderTabbedPage(sectionItems, 0, 120)
	bar := strings.Join(stripANSIAll(overview), "\n")
	for _, tab := range []string{configTabApproval, configTabSession, configTabAgents} {
		count := strconv.Itoa(len(sectionsOfTab(tab)))
		if !strings.Contains(bar, tab+" "+count) {
			t.Errorf("the overview's tab bar is missing %q:\n%s", tab+" "+count, bar)
		}
	}
}

// A /config field page groups its settings under the prototype's ruled labels
// (plan 325 D2): upper-cased, a faint rule running to the panel's right edge,
// one blank line before every group after the first — and no heading row
// above them, no effect tag anywhere (owner 2026-11-18). A plain picker's
// categories keep the heading they always drew — the ruled group is the
// config page's alone.
func TestConfigPanelFieldPageShowsGroups(t *testing.T) {
	panel := sectionPanel(nil)
	lsp, ok := configSectionSchemaFor("lsp")
	if !ok {
		t.Fatal("no lsp section")
	}
	items, _ := panel.fieldItems(lsp, panel.sectionDoc("lsp"), false)
	lines := renderConfigPickerPage(items, lsp.Title+"\n"+lsp.Summary, 120)
	plain := stripANSIAll(lines)
	found := map[string]int{}
	for i, line := range plain {
		// The heading carries its rule to the edge, so it is matched by its
		// upper-cased prefix alone.
		for _, group := range []string{"Discovery", "Lifecycle", "Diagnostics"} {
			if strings.HasPrefix(strings.TrimSpace(line), strings.ToUpper(group)) {
				found[group] = i
			}
		}
	}
	if len(found) != 3 {
		t.Fatalf("the lsp page does not show every group: %v\n%s", found, strings.Join(plain, "\n"))
	}
	if !(found["Discovery"] < found["Lifecycle"] && found["Lifecycle"] < found["Diagnostics"]) {
		t.Fatalf("the groups are out of order: %v", found)
	}
	for group, at := range found {
		if !strings.Contains(lines[at], "─") {
			t.Errorf("the %s heading carries no rule: %q", group, lines[at])
		}
		if w := displayLineWidth(lines[at]); w != 120 {
			t.Errorf("the %s rule stops at %d cells, want the panel's edge", group, w)
		}
		if at == 0 || strings.TrimSpace(plain[at-1]) != "" {
			t.Errorf("the %s group needs a blank row before it:\n%s", group, strings.Join(plain, "\n"))
		}
	}
	// No heading row between the head and the groups: the head is the title
	// and the summary alone, then the page's own blank line.
	if strings.TrimSpace(plain[0]) != lsp.Title || strings.TrimSpace(plain[1]) != lsp.Summary || strings.TrimSpace(plain[2]) != "" {
		t.Fatalf("the head carries more than the title and summary:\n%s", strings.Join(plain[:4], "\n"))
	}

	// A plain rich picker keeps its own heading: the category as written,
	// accented, unruled — every other panel's page is unchanged.
	plainPicker := &richPickerState{items: []SelectItem{
		{Label: "Workspace write", Category: "Approval & sandbox"},
	}}
	for i := range plainPicker.items {
		plainPicker.selectable = append(plainPicker.selectable, i)
	}
	plainLines := plainPicker.panel("Configuration").layout(120, 0, 0, false).lines
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
			// The unified definition's two role field sets never render on one
			// page (P3): a PrimaryOnly field and a SubagentOnly one may share
			// a label — the prototype's two Model rows — because only one
			// role's set is visible at a time. Fields that can co-render may
			// not.
			visibility := 0
			if !field.PrimaryOnly {
				visibility |= 1
			}
			if !field.SubagentOnly {
				visibility |= 2
			}
			roleKey := fmt.Sprintf("%d:%s", visibility, field.Label)
			if labels[roleKey] {
				t.Errorf("模板 %s 的字段标签 %q 重复", template, field.Label)
			}
			labels[roleKey] = true
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

// A container row is an entry like any other (plan 350, the prototype's
// rowField marks every collection row entry): the footer offers Remove on an
// agent definition, the confirmation names the row, Keep is preselected, and
// confirming takes the definition out of the map beside it.
func TestConfigPanelRemovesAContainerCollectionRow(t *testing.T) {
	session := newConfigPanelSession(map[string]string{"agents": `{"definitions":{"main":{"primary":true},"spare":{"primary":false}}}`})
	selector := &actionPanelSelector{scriptedSelector: &scriptedSelector{}}
	runActionConfigPanel(t, session, selector,
		[]richStep{
			{label: "Agent definitions", ok: true},
			{label: "Agent definitions", ok: true},
			{label: configRemoveConfirmLabel, ok: true},
			{ok: false}, {ok: false},
		},
		[]actionStep{
			{label: "Agent definitions", ok: true},
			{action: FooterActionRemove, row: 0, ok: true},
			{label: "spare", ok: true},
			{action: FooterActionRemove, row: 1, ok: true},
			{ok: false},
			{ok: false},
		},
		nil, "agents")

	confirmation := ""
	for _, label := range selector.richLabels {
		if strings.HasPrefix(label, "Remove ") {
			confirmation = label
		}
	}
	if !strings.HasPrefix(confirmation, "Remove spare?") {
		t.Fatalf("the confirmation = %q, want the row it removes named", confirmation)
	}
	if !slices.Contains(selector.defaults, 1) {
		t.Fatalf("the confirmation opened on %v, want Keep (row 1) preselected", selector.defaults)
	}
	if len(session.writes) != 1 {
		t.Fatalf("expected one write, got %d", len(session.writes))
	}
	raw := string(session.writes[0].raw)
	if strings.Contains(raw, "spare") || !strings.Contains(raw, "main") {
		t.Fatalf("the removed definition is still there, raw = %s", raw)
	}
}

// A saved definition's Name is the key it lives under, and the two fields a
// create decides are locked: the page draws them muted and opening either
// answers the created-fields sentence without leaving the page (plan 350).
func TestConfigPanelNamesAndLocksTheCreatedFields(t *testing.T) {
	session := newConfigPanelSession(map[string]string{"agents": `{"definitions":{"main":{"primary":true}}}`})
	selector := &scriptedSelector{}
	runConfigPanel(t, session, selector, []richStep{
		{label: "Agent definitions", ok: true},
		{label: "main", ok: true},
		{label: "Name", ok: true},
		{ok: false}, {ok: false}, {ok: false},
	}, nil, "agents")

	var page []SelectItem
	for _, items := range selector.richItems {
		if row, ok := findRowLoose(items, "Name"); ok && row.Value == "main" {
			page = items
		}
	}
	if page == nil {
		t.Fatalf("no page shows the definition's own key: %v", pageLabels(selector))
	}
	for _, tc := range []struct {
		label, value string
		locked       bool
	}{
		{"Name", "main", true},
		{"Primary", configBoolOnLabel, true},
		{"Display name", "", false},
	} {
		row := findRow(t, page, tc.label)
		if row.Value != tc.value {
			t.Errorf("%s's value = %q, want %q", tc.label, row.Value, tc.value)
		}
		if row.Locked != tc.locked {
			t.Errorf("%s's locked = %v, want %v", tc.label, row.Locked, tc.locked)
		}
	}
	if !strings.Contains(pageLabels(selector), configCreatedFieldsNotice) {
		t.Fatalf("opening a locked field must answer the created-fields sentence: %s", pageLabels(selector))
	}
	if len(session.writes) != 0 {
		t.Fatalf("nothing here writes, got %+v", session.writes)
	}
}

// The /config panel's header is the tab bar and nothing else, on every page and
// at every depth (plan 350, the prototype's render() emitting the tabs above
// every view and its empty heading()/breadcrumb()). At 120 columns a tab reads
// its full label and the number of sections it holds; under 100 columns the
// short label alone, as the prototype's narrow rule has it.
func TestConfigPanelChromeIsTheTabBarOnEveryPage(t *testing.T) {
	chrome := &panelChrome{
		Labels: []string{configTabApproval, configTabSession, configTabAgents},
		Short:  []string{"Safety", "Session", "Agents"},
		Counts: []int{3, 4, 4},
		Active: 2,
	}
	items := []SelectItem{{Label: "Agent definitions"}, {Label: "Hooks"}}

	for _, tc := range []struct {
		name string
		keys *configPageKeys
	}{
		{"section page", configFieldKeys},
		{"choice page", configChoiceKeys},
		{"confirm page", configConfirmKeys},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &richPickerState{
				items: items, selectable: []int{0, 1}, configPage: true, noFilter: true,
				chrome: chrome, keys: tc.keys,
			}
			lines := stripANSIAll(p.panel("").layout(120, 0, 0, false).lines)
			head := strings.Join(lines[:2], "\n")
			for _, want := range []string{configTabApproval, configTabSession, configTabAgents} {
				if !strings.Contains(head, want) {
					t.Fatalf("the page's header is missing the %q tab:\n%s", want, head)
				}
			}
			if !strings.Contains(head, configTabAgents+" 4") {
				t.Fatalf("the tab bar carries no counts:\n%s", head)
			}
			// The page carries no title block under the bar: one blank row
			// sets the bar off the body, and the body starts with the page's
			// own first row.
			if blank := strings.TrimSpace(lines[1]); blank != "" {
				t.Fatalf("the page heads itself with %q; the bar is the header (plan 350)", blank)
			}
			if first := strings.TrimSpace(lines[2]); !strings.Contains(first, "Agent definitions") {
				t.Fatalf("the body's first row = %q, want the page's own first row", first)
			}

			narrow := stripANSIAll(p.panel("").layout(88, 0, 0, false).lines)
			short := strings.Join(narrow[:2], "\n")
			for _, want := range []string{"Safety", "Session", "Agents"} {
				if !strings.Contains(short, want) {
					t.Fatalf("the narrow tab bar is missing %q:\n%s", want, short)
				}
			}
			if strings.Contains(short, configTabApproval) {
				t.Fatalf("a narrow bar keeps the full labels:\n%s", short)
			}
		})
	}
}

// A page's footer names its keys in the prototype's words, and the page's label
// — the outcome line under the bar — is drawn under the bar rather than as a
// heading (plan 350).
func TestConfigPanelPageKeysAndNoticeLine(t *testing.T) {
	chrome := &panelChrome{Labels: []string{configTabAgents}, Short: []string{"Agents"}, Counts: []int{4}}
	p := &richPickerState{
		items: []SelectItem{{Label: "Agent definitions"}}, selectable: []int{0},
		configPage: true, noFilter: true, chrome: chrome, keys: configFieldKeys,
		actions: []FooterAction{{Label: configAddActionLabel, Key: "a", Kind: FooterActionAdd}},
	}
	lines := stripANSIAll(p.panel("A value that did not land").layout(120, 0, 0, false).lines)
	if !strings.Contains(lines[1], "A value that did not land") {
		t.Fatalf("the page's outcome line is not under the bar:\n%s", strings.Join(lines, "\n"))
	}
	hint := lines[len(lines)-1]
	if want := "↑/↓ to navigate Enter to open Esc to go back a Add"; !strings.Contains(hint, want) {
		t.Fatalf("the footer = %q, want %q", hint, want)
	}
}

// An overview row is the prototype's two-line block: the section's title with
// its status at the right edge, and its summary on the line under it — never
// one crowded line, and never the summary glued to the title (plan 350).
func TestConfigPanelOverviewRowsAreTwoLineBlocks(t *testing.T) {
	panel := sectionPanel(nil)
	items, _ := panel.sectionItems()
	lines := stripANSIAll(renderTabbedPage(items, 0, 120))
	var block []string
	for _, line := range lines {
		if strings.HasSuffix(line, configDefaultStatus) {
			block = append(block, line)
			continue
		}
		if len(block) == 1 && strings.HasPrefix(line, panelIndent) && !strings.HasPrefix(line, panelIndent+" ") {
			block = append(block, line)
			break
		}
	}
	if len(block) != 2 {
		t.Fatalf("an overview row is not a two-line block:\n%s", strings.Join(lines, "\n"))
	}
	if !strings.Contains(block[0], "Approval preset") || !strings.HasSuffix(block[0], configDefaultStatus) {
		t.Fatalf("the block's first line = %q, want the section's title and its status", block[0])
	}
	if strings.Contains(block[0], "Choose what new sessions may do.") {
		t.Fatalf("the title line carries the summary too: %q", block[0])
	}
	if strings.TrimSpace(block[1]) != "Choose what new sessions may do." {
		t.Fatalf("the block's second line = %q, want the section's summary", block[1])
	}
}

// A field row is its label and the value it holds, right-aligned, with the
// field's help nowhere near it: the help belongs to the setting's editor (plan
// 350, the prototype's fields view setting `help`, never `desc`).
func TestConfigPanelFieldRowsCarryNoHelp(t *testing.T) {
	panel := sectionPanel(map[string]string{"features": `{"memories":true}`})
	features, ok := configSectionSchemaFor("features")
	if !ok {
		t.Fatal("no features section")
	}
	items, _ := panel.fieldItems(features, panel.sectionDoc("features"), false)
	lines := stripANSIAll(renderRichPage(items, "", 120))
	for _, field := range features.Fields {
		row, ok := findRowLoose(items, field.Label)
		if !ok {
			t.Fatalf("no row for %q", field.Label)
		}
		if row.Description != "" {
			t.Fatalf("%s's row carries %q; the help belongs to its editor", field.Label, row.Description)
		}
	}
	joined := strings.Join(lines, "\n")
	for _, hint := range []string{
		"Remember useful context across conversations.",
		"Enable code intelligence and language diagnostics.",
	} {
		if strings.Contains(joined, hint) {
			t.Fatalf("a field's help reached the page: %q", hint)
		}
	}
	if !strings.Contains(joined, configBoolOnLabel) {
		t.Fatalf("the page does not show the switch's value:\n%s", joined)
	}
}

// The overview's tab bar answers its arrows the way /status's own tabs do, and
// Tab keeps switching: three ways to move, one tab shown (plan 350).
func TestConfigPanelOverviewArrowsAndTabAgree(t *testing.T) {
	panel := sectionPanel(nil)
	items, names := panel.sectionItems()
	chrome := panel.chrome()
	// The first section of the tab beside the one the overview opens on: a step
	// lands on the first row of the adjacent tab, not the next row of this one.
	firstOf := func(tab string) int {
		for i, name := range names {
			if schema, ok := configSectionSchemaFor(name); ok && schema.Tab == tab {
				return i
			}
		}
		t.Fatalf("no section on the %q tab", tab)
		return -1
	}
	for _, tc := range []struct {
		name string
		keys string
		want int
	}{
		{"→", "\x1b[C", firstOf(configTabSession)},
		{"←", "\x1b[D", firstOf(configTabAgents)},
		{"Tab", "\t", firstOf(configTabSession)},
		{"Shift+Tab", "\x1b[Z", firstOf(configTabAgents)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sel := pickerKeys(t, tc.keys+keyEnter)
			idx, ok, err := sel.SelectRichTabbedPage("", items, TabbedSelectOptions{}, chrome)
			if err != nil || !ok {
				t.Fatalf("%s then Enter = %d, %v, %v", tc.name, idx, ok, err)
			}
			if idx != tc.want {
				t.Fatalf("%s lands on row %d (%q), want %q", tc.name, idx, names[idx], names[tc.want])
			}
		})
	}
}

// An arrow on a section page steps the whole panel to the tab beside it, and
// the list reopens on that tab's first section — however deep the page that
// took the arrow sat (plan 350, the prototype's goOverview).
func TestConfigPanelSectionArrowStepsTabs(t *testing.T) {
	panel := sectionPanel(nil)
	panel.tab = configTabAgents
	if got := panel.stepTab(1); got != configTabApproval {
		t.Fatalf("→ from the last tab lands on %q, want %q", got, configTabApproval)
	}
	if got := panel.stepTab(-1); got != configTabSession {
		t.Fatalf("← from the agents tab lands on %q, want %q", got, configTabSession)
	}

	// A page that took one leaves the list on that tab's first section.
	panel.overviewRow = 0
	panel.tabStep = -1
	panel.leaveSection("agents")
	sections := panelSections()
	if got := sections[panel.overviewRow]; got.Tab != configTabSession {
		t.Fatalf("the list reopened on %q, want the session tab's first section", got.Tab)
	}
	if panel.tabStep != 0 {
		t.Fatalf("the step was not taken: %d", panel.tabStep)
	}
}

// The tabs a machine offers are the tabs its sections carry: a platform with no
// sandbox loses the first one, and the bar counts what is left (plan 215 §3.4,
// plan 350).
func TestConfigPanelChromeFollowsTheOfferedTabs(t *testing.T) {
	panel := sectionPanel(nil)
	chrome := panel.chrome()
	if !slices.Equal(chrome.Labels, []string{configTabApproval, configTabSession, configTabAgents}) {
		t.Fatalf("the bar = %v", chrome.Labels)
	}
	if len(chrome.Counts) != len(chrome.Labels) || chrome.Counts[0] != 3 || chrome.Counts[1] != 4 || chrome.Counts[2] != 4 {
		t.Fatalf("the bar's counts = %v, want the prototype's 3/4/4", chrome.Counts)
	}
	if !slices.Equal(chrome.Short, []string{"Safety", "Session", "Agents"}) {
		t.Fatalf("the narrow labels = %v", chrome.Short)
	}
}
