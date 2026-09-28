package turn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	"github.com/forebrain-harness/forebrain-harness/pkg/assembly"
	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/stretchr/testify/require"
)

func TestTurnRequestJSONRoundTrip(t *testing.T) {
	want := TurnRequest{
		SessionID:   "s1",
		Origin:      Origin{Surface: SurfaceTUI, ChannelID: "term", RequestID: "r1"},
		UserText:    "hello",
		Parts:       []llm.ContentPart{llm.Text("hello")},
		Attachments: []Attachment{{ID: "a1", Path: "/tmp/a", MIMEType: "text/plain"}},
	}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got TurnRequest
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.SessionID != want.SessionID || got.Origin != want.Origin || got.UserText != want.UserText || len(got.Parts) != 1 || len(got.Attachments) != 1 {
		t.Fatalf("round trip = %#v", got)
	}
}

// fakeStatusRunStore is the run-store slice BuildStatusReport reads.
type fakeStatusRunStore struct {
	session state.SessionUsage
	last    state.LastRunUsage
}

func (f *fakeStatusRunStore) SessionUsageForSession(context.Context, string) (state.SessionUsage, error) {
	return f.session, nil
}

func (f *fakeStatusRunStore) LastRunUsageForSession(context.Context, string) (state.LastRunUsage, error) {
	return f.last, nil
}

// TestBuildStatusReportAssemblesThePanel pins that /status is assembled in one
// place: mode fallback, plan/todo gathering, usage totals with cache hit rate,
// and the omit-when-empty contract of the markdown renderer.
func TestBuildStatusReportAssemblesThePanel(t *testing.T) {
	t.Parallel()

	stateRoot := t.TempDir()
	require.NoError(t, state.Set(stateRoot, "s1", state.State{Mode: state.ModePlan, Phase: "draft"}))
	require.NoError(t, state.SetPlanForProject(stateRoot, "proj", "a plan"))
	if err := state.Save(stateRoot, "s1", state.List{Items: []state.Item{
		{Content: "one", Status: state.StatusCompleted},
		{Content: "two", Status: state.StatusPending},
	}}); err != nil {
		t.Fatalf("Save todos: %v", err)
	}

	rep := BuildStatusReport(context.Background(), StatusSource{
		StateRoot:  stateRoot,
		ProjectKey: "proj",
		SessionID:  "s1",
		RunStore: &fakeStatusRunStore{
			session: state.SessionUsage{PromptTokens: 51_000, CompletionTokens: 42_000, CacheReadTokens: 1_790_000, CacheWriteTokens: 60_000, LLMCalls: 38},
			last:    state.LastRunUsage{PromptTokens: 4_000, CompletionTokens: 1_200, CacheReadTokens: 42_000, CacheWriteTokens: 2_000},
		},
		// The footer's own input: the last API response's whole prompt.
		ContextUsage: func() (int, int) { return 48_000, 0 },
		Provider:     "openai",
		Model:        "gpt-main",
		Endpoint:     "https://user:secret@api.example.com/v1?key=k",
		FastOn:       true,
		MCP:          MCPInventorySource{Servers: []appcfg.MCPServerConfig{{Name: "docs"}}},
		Permissions:  StatusPermissions{Preset: "Workspace", Matched: true, Rules: 3},
		Sandbox:      "workspace-write · seatbelt",
		SkillOffer:   true,
	})
	out := RenderStatusMarkdown(rep)

	for _, want := range []string{
		"Session ID: s1",
		"Agent: plan (draft)",
		"Model: openai / gpt-main · fast on",
		// URL sanitization: credentials and query strings never render.
		"https://api.example.com/v1",
		"Workspace · 3 rules",
		"workspace-write · seatbelt",
		"MCP servers: 1 unknown · /mcp",
		// Usage tab: the footer's context gauge, session totals whose input
		// is everything sent (uncached + read + written), the hit rate over
		// the same three, and the last turn summed the same way.
		"Context: ",
		"48k of ",
		"Session: 1.9M input · 42k output · 38 requests",
		"Cache hit rate: 94% · 1.79M read · 60k written · 51k uncached",
		"Last turn: 48k input · 1.2k output · 87% cached",
		"Work: plan set · 1 of 2 todos done",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status markdown missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "secret") || strings.Contains(out, "key=k") {
		t.Errorf("status markdown leaked endpoint credentials:\n%s", out)
	}
	// Skill offer on: the row is omitted entirely, not shown as "on".
	if strings.Contains(out, "Skill offer") {
		t.Errorf("an enabled skill offer must not render a row:\n%s", out)
	}
}

// TestBuildStatusReportOmitsEmptyRowsAndNamesSkillOfferOff pins the R-json
// sibling of the status panel: no placeholder rows, and the skill-offer row
// exists only to name the switch that turned it off.
func TestBuildStatusReportOmitsEmptyRowsAndNamesSkillOfferOff(t *testing.T) {
	out := RenderStatusMarkdown(BuildStatusReport(context.Background(), StatusSource{
		StateRoot: t.TempDir(),
		SessionID: "",
	}))
	if !strings.Contains(out, "Session ID: default") {
		t.Errorf("missing session id should render as default:\n%s", out)
	}
	if strings.Contains(out, "Work:") || strings.Contains(out, "Session: ") || strings.Contains(out, "Proxy:") {
		t.Errorf("empty data must omit the row:\n%s", out)
	}

	off := RenderStatusMarkdown(BuildStatusReport(context.Background(), StatusSource{
		StateRoot:  t.TempDir(),
		SessionID:  "s1",
		SkillOffer: false,
	}))
	if !strings.Contains(off, "Skill offer: off (features.skill_offer)") {
		t.Errorf("the off row must name the setting:\n%s", off)
	}
}

// TestStatusSideConversationSaysWhoseSessionItIs pins the side label: a side
// conversation's report describes its own session and says so.
func TestStatusSideConversationSaysWhoseSessionItIs(t *testing.T) {
	out := RenderStatusMarkdown(BuildStatusReport(context.Background(), StatusSource{
		StateRoot: t.TempDir(), SessionID: "side-1", Side: true, SkillOffer: true,
	}))
	if !strings.HasPrefix(out, "Session Status · side conversation") || !strings.Contains(out, "Session ID: side-1") {
		t.Fatalf("side report:\n%s", out)
	}
}

// TestStatusCustomPermissionsNeverShowPlaceholders pins the custom form: the
// halves that are known are named, an unknown one is left out, never "-".
func TestStatusCustomPermissionsNeverShowPlaceholders(t *testing.T) {
	if got := RenderPermissionsLine(StatusPermissions{Approval: "on-request"}); got != "Custom · on-request" {
		t.Fatalf("custom line = %q", got)
	}
	if got := RenderPermissionsLine(StatusPermissions{Approval: "never", Sandbox: "read-only", Rules: 1}); got != "Custom · never · read-only · 1 rule" {
		t.Fatalf("custom line = %q", got)
	}
}

// TestCacheHitPercent pins the provider-level hit-rate definition: read over
// read+written+uncached, and zero stays zero.
func TestCacheHitPercent(t *testing.T) {
	if got := CacheHitPercent(0, 0, 0); got != 0 {
		t.Fatalf("empty = %d, want 0", got)
	}
	if got := CacheHitPercent(94, 3, 3); got != 94 {
		t.Fatalf("94/3/3 = %d, want 94", got)
	}
}

// TestStatusInstructionsLine pins the Instructions row: agent files carry the
// (agent) marker, a file that did not make it into the body says why, and a
// cut one says where it was cut.
func TestStatusInstructionsLine(t *testing.T) {
	line := RenderInstructionsLine([]assembly.RuleSource{
		{Name: "AGENTS.md", Agent: true, Loaded: true},
		{Name: "FOREBRAIN.md", Loaded: false, NotLoadedReason: "project not trusted"},
		{Name: "docs/FOREBRAIN.md", Loaded: true, TruncatedTo: 120_000},
	})
	for _, want := range []string{
		"AGENTS.md (agent)",
		"FOREBRAIN.md (not loaded: project not trusted)",
		"docs/FOREBRAIN.md (truncated to 120k chars)",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("instructions line %q missing %q", line, want)
		}
	}
	if RenderInstructionsLine(nil) != "" {
		t.Fatal("no sources must render an empty row")
	}
}

// TestAssistantOutcomeTextDropsAReasoningOnlyEcho pins the rule both surfaces
// now share: when the assistant's visible answer is nothing but its own
// reasoning in a thinking fence, it is not persisted as an answer.
//
// Only the terminal used to do this, so the same run stored a duplicated
// answer on the web and not in the terminal.
func TestAssistantOutcomeTextDropsAReasoningOnlyEcho(t *testing.T) {
	t.Parallel()

	reasoning := "step one\nstep two"
	echo := &agent.Result{
		Parts:     []llm.ContentPart{llm.Text("```thinking\n" + reasoning + "\n```")},
		Reasoning: reasoning,
	}
	text, gotReasoning := AssistantOutcomeText(echo)
	if text != "" {
		t.Fatalf("text = %q, want empty: a reasoning-only echo is not an answer", text)
	}
	if gotReasoning != reasoning {
		t.Fatalf("reasoning = %q, want %q", gotReasoning, reasoning)
	}
}

func TestAssistantOutcomeTextKeepsARealAnswer(t *testing.T) {
	t.Parallel()

	reasoning := "step one"
	res := &agent.Result{
		Parts:     []llm.ContentPart{llm.Text("Here is the answer.")},
		Reasoning: reasoning,
	}
	text, gotReasoning := AssistantOutcomeText(res)
	if text != "Here is the answer." {
		t.Fatalf("text = %q, want the real answer kept", text)
	}
	if gotReasoning != reasoning {
		t.Fatalf("reasoning = %q", gotReasoning)
	}

	// An answer that merely quotes some of its reasoning is still an answer:
	// the echo check is equality, not containment.
	quoting := &agent.Result{
		Parts:     []llm.ContentPart{llm.Text("```thinking\n" + reasoning + "\n```\nAnd so: yes.")},
		Reasoning: reasoning,
	}
	if text, _ := AssistantOutcomeText(quoting); text == "" {
		t.Fatal("an answer that quotes its reasoning must not be dropped")
	}

	if text, reasoningOut := AssistantOutcomeText(nil); text != "" || reasoningOut != "" {
		t.Fatalf("nil result = %q/%q, want empty", text, reasoningOut)
	}
}

// modelSelectionFixture wires the recording handler to a real temp config
// file, so the default-write half of /model runs for real.
type modelSelectionFixture struct {
	handler *recordingSlashHandlers
	cfgPath string
	ctx     Context
	store   *fakeNewSessionStore
}

func newModelSelectionFixture(t *testing.T, selection ModelSelectionState) *modelSelectionFixture {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "forebrain.yaml")
	t.Setenv("FOREBRAIN_TURN_MODEL_TEST_KEY", "test-key")
	if err := os.WriteFile(cfgPath, []byte("agents:\n  definitions:\n    main:\n      llm_providers:\n        - provider: openai\n          model: gpt-5\n          api_key: ${FOREBRAIN_TURN_MODEL_TEST_KEY}\n          base_url: http://a.test/v1\n        - provider: deepseek\n          model: deepseek-chat\n          api_key: ${FOREBRAIN_TURN_MODEL_TEST_KEY}\n          base_url: http://b.test/v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := &recordingSlashHandlers{selection: selection}
	store := &fakeNewSessionStore{copiedModel: true}
	fx := &modelSelectionFixture{
		handler: h,
		cfgPath: cfgPath,
		store:   store,
		ctx: Context{
			CommandContext: context.Background(),
			Home:           t.TempDir(),
			SessionID:      "s1",
			Surface:        SurfaceTUI,
			Model:          h,
			Sessions:       store,
		},
	}
	h.settings = fx.modelSettings
	return fx
}

// modelSettings answers from the temp config file, the way a surface's live
// handler does.
func (f *modelSelectionFixture) modelSettings(_ string) (ModelSettings, error) {
	cfg, err := appcfg.LoadPersisted(f.cfgPath)
	if err != nil {
		return ModelSettings{}, err
	}
	return ModelSettings{Config: &cfg, AgentName: "main", ConfigPath: f.cfgPath, Selection: f.handler.selection}, nil
}

// choiceLabel names a configured model the way /model's picker does.
func (f *modelSelectionFixture) choiceLabel(t *testing.T, provider, model string) string {
	t.Helper()
	settings, err := f.modelSettings("")
	require.NoError(t, err)
	for _, choice := range ModelChoices(settings.Config, settings.AgentName) {
		if strings.EqualFold(choice.Provider, provider) && strings.EqualFold(choice.Model, model) {
			return choice.Label
		}
	}
	t.Fatalf("no configured choice for %s/%s", provider, model)
	return ""
}

func (f *modelSelectionFixture) reloadConfig(t *testing.T) *appcfg.Root {
	t.Helper()
	cfg, err := appcfg.LoadPersisted(f.cfgPath)
	require.NoError(t, err)
	return &cfg
}

func (f *modelSelectionFixture) calls() []string { return f.handler.calls }

func TestExecModelMarksCurrentByLiveSelection(t *testing.T) {
	f := newModelSelectionFixture(t, ModelSelectionState{Provider: "deepseek", Model: "deepseek-chat", Effort: "high", Set: true})
	res := execModel(f.ctx)
	require.True(t, res.Handled)
	require.NotNil(t, res.Picker)
	require.Equal(t, "model", res.Picker.Command)
	current := -1
	for i, item := range res.Picker.Items {
		if item.Current {
			require.Equal(t, -1, current, "more than one current item")
			current = i
		}
	}
	require.NotEqual(t, -1, current, "no current item")
	require.True(t, strings.Contains(res.Picker.Items[current].Value, "deepseek"), "current item = %s", res.Picker.Items[current].Value)
}

func TestChooseModelAppliesLiveSelectionBeforeDefault(t *testing.T) {
	f := newModelSelectionFixture(t, ModelSelectionState{})
	label := f.choiceLabel(t, "deepseek", "deepseek-chat")
	res := Choose(f.ctx, SlashChoice{Command: "model", Value: label})
	require.True(t, res.Handled)
	require.Equal(t, "Switched to deepseek / deepseek-chat.", res.Reply)
	// Live apply first, then the default write, then the catalog reload.
	selectAt := slices.Index(f.calls(), "select(s1,deepseek/deepseek-chat)")
	require.NotEqual(t, -1, selectAt, "SelectModel was not called: %v", f.calls())
	cfg, err := appcfg.LoadPersisted(f.cfgPath)
	require.NoError(t, err)
	require.Equal(t, "deepseek-chat", cfg.Agents.Definitions["main"].LLMProviders[0].Model, "default file was not promoted")
	require.NotEqual(t, -1, slices.Index(f.calls(), "reload-catalog(s1,)"), "ReloadModelCatalog was not called")
}

func TestChooseModelSameSelectionIsNoOp(t *testing.T) {
	f := newModelSelectionFixture(t, ModelSelectionState{Provider: "deepseek", Model: "deepseek-chat", Set: true})
	label := f.choiceLabel(t, "deepseek", "deepseek-chat")
	res := Choose(f.ctx, SlashChoice{Command: "model", Value: label})
	require.True(t, res.Handled)
	require.Equal(t, -1, slices.Index(f.calls(), "select("), "a repeat selection applied a live switch: %v", f.calls())
	require.Equal(t, "Still using deepseek / deepseek-chat.", res.Reply)
}

func TestChooseModelReportsPartialDefaultSaveFailure(t *testing.T) {
	f := newModelSelectionFixture(t, ModelSelectionState{})
	label := f.choiceLabel(t, "deepseek", "deepseek-chat")
	// A read-only config file fails the default write only: reads still
	// work, so the live apply succeeds and only the file save is reported
	// failed.
	require.NoError(t, os.Chmod(f.cfgPath, 0o400))
	defer func() { _ = os.Chmod(f.cfgPath, 0o600) }()
	res := Choose(f.ctx, SlashChoice{Command: "model", Value: label})
	require.True(t, res.Handled)
	require.Contains(t, res.Reply, "Switched to deepseek / deepseek-chat.")
	require.Contains(t, res.Reply, "The default for new sessions was not saved")
	require.NotEqual(t, -1, slices.Index(f.calls(), "select(s1,deepseek/deepseek-chat)"), "live apply did not run")
}

func TestChooseModelClassifiesSelectionOutcomes(t *testing.T) {
	f := newModelSelectionFixture(t, ModelSelectionState{})
	f.handler.selectErr = NewModelSelectionError(ModelRuntimeUncertain, errors.New("rollback failed"))
	label := f.choiceLabel(t, "deepseek", "deepseek-chat")
	res := Choose(f.ctx, SlashChoice{Command: "model", Value: label})
	require.True(t, res.Handled)
	require.Contains(t, res.Reply, "could not be restored")
	require.Contains(t, res.Reply, "Reload the configuration or restart")
	require.NotContains(t, res.Reply, "Switched to")

	f = newModelSelectionFixture(t, ModelSelectionState{})
	f.handler.selectErr = NewModelSelectionError(ModelAppliedNotDurable, errors.New("row save failed"))
	res = Choose(f.ctx, SlashChoice{Command: "model", Value: label})
	require.True(t, res.Handled)
	require.Contains(t, res.Reply, "Switched to deepseek / deepseek-chat, but the choice could not be saved")

	f = newModelSelectionFixture(t, ModelSelectionState{})
	f.handler.selectErr = errors.New("provider not configured")
	res = Choose(f.ctx, SlashChoice{Command: "model", Value: label})
	require.True(t, res.Handled)
	require.Contains(t, res.Reply, "Could not switch to deepseek / deepseek-chat: provider not configured")
}

// addConfigEntry appends a provider entry to the fixture's config file, for
// tests that need a specific catalog model configured.
func (f *modelSelectionFixture) addConfigEntry(t *testing.T, provider, model string) {
	t.Helper()
	entry := fmt.Sprintf("        - provider: %s\n          model: %s\n          api_key: ${FOREBRAIN_TURN_MODEL_TEST_KEY}\n          base_url: http://c.test/v1\n", provider, model)
	raw, err := os.ReadFile(f.cfgPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(f.cfgPath, append(raw, []byte(entry)...), 0o600))
}

// reasoningCapableOpenAIModel picks a catalog model that reasons, so the
// effort picker path runs.
func reasoningCapableModel(t *testing.T) llm.Model {
	t.Helper()
	for _, m := range llm.AllModels() {
		if m.CanReason && strings.EqualFold(strings.TrimSpace(m.Provider), "openai") {
			return m
		}
	}
	t.Fatal("no reasoning-capable openai model in the catalog")
	return llm.Model{}
}

func TestChooseModelEffortPickerMarksLiveEffort(t *testing.T) {
	m := reasoningCapableModel(t)
	f := newModelSelectionFixture(t, ModelSelectionState{Provider: "openai", Model: m.APIModel, Effort: "medium", Set: true})
	f.addConfigEntry(t, "openai", m.APIModel)
	res := Choose(f.ctx, SlashChoice{Command: "model", Value: f.choiceLabel(t, "openai", m.APIModel)})
	require.True(t, res.Handled)
	require.NotNil(t, res.Picker)
	require.Equal(t, "model-effort", res.Picker.Command)
	current := 0
	for i, item := range res.Picker.Items {
		if item.Current {
			current = i
		}
	}
	require.Equal(t, "medium", res.Picker.Items[current].Label, "effort marker did not read the live selection")
}

func TestChooseReasoningEffortAppliesLiveThenDefault(t *testing.T) {
	m := reasoningCapableModel(t)
	f := newModelSelectionFixture(t, ModelSelectionState{Provider: "openai", Model: m.APIModel, Effort: "low", Set: true})
	f.addConfigEntry(t, "openai", m.APIModel)
	value := encodeEffortChoice(f.choiceLabel(t, "openai", m.APIModel), "high", false)
	res := Choose(f.ctx, SlashChoice{Command: "model-effort", Value: value})
	require.True(t, res.Handled)
	require.Equal(t, "Reasoning effort set to high.", res.Reply)
	require.NotEqual(t, -1, slices.Index(f.calls(), "select-effort(s1,high)"), "explicit effort pointer not passed: %v", f.calls())
	cfg, err := appcfg.LoadPersisted(f.cfgPath)
	require.NoError(t, err)
	require.Equal(t, "high", appcfg.ReasoningEffort(cfg.Agents.Definitions["main"].LLMProviders[0].Params))
}

func TestForkCopiesMaterializedModelSelection(t *testing.T) {
	f := newModelSelectionFixture(t, ModelSelectionState{Provider: "deepseek", Model: "deepseek-chat", Effort: "high", Set: true})
	res := execFork(f.ctx)
	require.True(t, res.Handled)
	require.True(t, res.SessionSwitched)
	require.NotEqual(t, -1, slices.Index(f.calls(), "select(s1,deepseek/deepseek-chat)"), "source selection was not materialized")
	require.Equal(t, -1, slices.Index(f.calls(), "reload-catalog("), "fork must not reload the catalog")
	require.Equal(t, "s1", f.store.copiedModelFrom)
	require.Equal(t, res.SessionID, f.store.copiedModelTo)

	// The copied selection becomes the new session's live selection.
	require.Equal(t, ModelSelectionState{Provider: "deepseek", Model: "deepseek-chat", Effort: "high", Set: true}, f.handler.selection)
}

func TestForkRequiresCopiedSelectionOnTUI(t *testing.T) {
	f := newModelSelectionFixture(t, ModelSelectionState{Provider: "deepseek", Model: "deepseek-chat", Set: true})
	f.store.copiedModel = false
	res := execFork(f.ctx)
	require.True(t, res.Handled)
	require.Contains(t, res.Reply, "Could not fork the conversation")

	// The web surface does not persist per-session model rows yet; a
	// non-copied selection there is a no-op, not a failed fork.
	f.ctx.Surface = SurfaceWebChat
	res = execFork(f.ctx)
	require.True(t, res.Handled)
	require.True(t, res.SessionSwitched)
}

func TestForkWithoutLiveSelectionSkipsModelCopy(t *testing.T) {
	f := newModelSelectionFixture(t, ModelSelectionState{})
	res := execFork(f.ctx)
	require.True(t, res.Handled)
	require.True(t, res.SessionSwitched)
	require.Equal(t, -1, slices.Index(f.calls(), "select("), "no-op fork applied a selection")
	require.Empty(t, f.store.copiedModelFrom)
}

func TestSelectionApplyOutcomeClassification(t *testing.T) {
	require.Equal(t, ModelNotApplied, SelectionApplyOutcome(nil))
	require.Equal(t, ModelNotApplied, SelectionApplyOutcome(errors.New("plain")))
	require.Equal(t, ModelAppliedNotDurable, SelectionApplyOutcome(NewModelSelectionError(ModelAppliedNotDurable, errors.New("x"))))
	require.Equal(t, ModelRuntimeUncertain, SelectionApplyOutcome(NewModelSelectionError(ModelRuntimeUncertain, errors.New("x"))))
	var carrier modelSelectionOutcome = &ModelSelectionError{Outcome: ModelAppliedNotDurable, Err: errors.New("x")}
	require.Equal(t, ModelAppliedNotDurable, carrier.ModelApplyOutcome())
	require.Nil(t, NewModelSelectionError(ModelNotApplied, nil))
}
