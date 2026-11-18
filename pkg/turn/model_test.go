package turn

import (
	"bytes"
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
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
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
	session    state.SessionUsage
	last       state.LastRunUsage
	rateTokens int
	rateMs     int64
}

func (f *fakeStatusRunStore) SessionUsageForSession(context.Context, string) (state.SessionUsage, error) {
	return f.session, nil
}

func (f *fakeStatusRunStore) LastRunUsageForSession(context.Context, string) (state.LastRunUsage, error) {
	return f.last, nil
}

func (f *fakeStatusRunStore) SessionOutputRate(context.Context, string) (int, int64, error) {
	return f.rateTokens, f.rateMs, nil
}

// TestBuildStatusReportAssemblesThePanel pins that /status is assembled in one
// place: mode fallback, plan/todo gathering, usage totals with cache hit rate,
// and the omit-when-empty contract of the markdown renderer.
func TestBuildStatusReportAssemblesThePanel(t *testing.T) {
	t.Parallel()

	stateRoot := t.TempDir()
	require.NoError(t, state.Set(stateRoot, "s1", state.State{Mode: state.ModePlan, Phase: "draft"}))
	require.NoError(t, state.SetPlanForSession(stateRoot, "proj", "s1", "a plan"))
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
		ContextUsage: func() (int, state.TokenBudgetOptions) { return 48_000, state.TokenBudgetOptions{} },
		Provider:     "openai",
		Model:        "gpt-main",
		Endpoint:     "https://user:secret@api.example.com/v1?key=k",
		MCP:          MCPInventorySource{Servers: []appcfg.MCPServerConfig{{Name: "docs"}}},
		Permissions:  StatusPermissions{Preset: "Workspace", Matched: true, Rules: 3},
		Sandbox:      "workspace-write seatbelt",
	})
	out := RenderStatusMarkdown(rep)

	for _, want := range []string{
		"Session ID: s1",
		"Agent: plan (draft)",
		"Model: openai/gpt-main",
		// URL sanitization: credentials and query strings never render.
		"https://api.example.com/v1",
		"Workspace 3 rules",
		"workspace-write seatbelt",
		"MCP servers: 1 unknown /mcp",
		// Usage tab: the footer's context gauge, session totals whose input
		// is everything sent (uncached + read + written), the hit rate over
		// the same three, and the last turn summed the same way.
		"Context: ",
		"48k of ",
		"Session: 1.9M input 42k output 38 requests",
		"Cache hit rate: 94% 1.79M read 60k written 51k uncached",
		"Last turn: 48k input 1.2k output 87% cached",
		"Work: plan set 1 of 2 todos done",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status markdown missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "secret") || strings.Contains(out, "key=k") {
		t.Errorf("status markdown leaked endpoint credentials:\n%s", out)
	}
}

// TestBuildStatusReportOmitsEmptyRows pins the R-json sibling of the status
// panel: no placeholder rows.
func TestBuildStatusReportOmitsEmptyRows(t *testing.T) {
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
}

// TestStatusAgentLineOmitsTheDefaultMode pins what /status stays silent about:
// the default agent mode is not named beside the agent.
func TestStatusAgentLineOmitsTheDefaultMode(t *testing.T) {
	stateRoot := t.TempDir()
	require.NoError(t, state.Set(stateRoot, "s1", state.State{Mode: state.ModeAgent}))
	defaults := BuildStatusReport(context.Background(), StatusSource{
		StateRoot: stateRoot, SessionID: "s1", AgentID: "main",
		Provider: "openai", Model: "gpt-main",
	})
	if got := StatusAgentLine(defaults); got != "main" {
		t.Fatalf("default agent line = %q, want the agent alone", got)
	}
	if got := defaults.ModelLine; got != "openai/gpt-main" {
		t.Fatalf("model line = %q, want the bare provider/model", got)
	}
}

// TestStatusSideConversationSaysWhoseSessionItIs pins the side label: a side
// conversation's report describes its own session and says so.
func TestStatusSideConversationSaysWhoseSessionItIs(t *testing.T) {
	out := RenderStatusMarkdown(BuildStatusReport(context.Background(), StatusSource{
		StateRoot: t.TempDir(), SessionID: "side-1", Side: true,
	}))
	if !strings.HasPrefix(out, "Session Status side conversation") || !strings.Contains(out, "Session ID: side-1") {
		t.Fatalf("side report:\n%s", out)
	}
}

// TestStatusCustomPermissionsNeverShowPlaceholders pins the custom form: the
// halves that are known are named, an unknown one is left out, never "-".
func TestStatusCustomPermissionsNeverShowPlaceholders(t *testing.T) {
	if got := RenderPermissionsLine(StatusPermissions{Approval: "on-request"}); got != "Custom on-request" {
		t.Fatalf("custom line = %q", got)
	}
	if got := RenderPermissionsLine(StatusPermissions{Approval: "never", Sandbox: "read-only", Rules: 1}); got != "Custom never read-only 1 rule" {
		t.Fatalf("custom line = %q", got)
	}
}

// TestOutputTokensPerSecondRoundsToOneDecimal pins the one rate computation
// both surfaces share: completion tokens over model-call time, one decimal,
// and zero rather than a division when either side is missing.
func TestOutputTokensPerSecondRoundsToOneDecimal(t *testing.T) {
	if got := OutputTokensPerSecond(1000, 3000); got != 333.3 {
		t.Fatalf("1000 tokens over 3000 ms = %v, want 333.3", got)
	}
	if got := OutputTokensPerSecond(1000, 0); got != 0 {
		t.Fatalf("no time = %v, want 0", got)
	}
	if got := OutputTokensPerSecond(0, 3000); got != 0 {
		t.Fatalf("no tokens = %v, want 0", got)
	}
}

// TestUsageFactsCarriesTheOutputRate pins the Session row's rate segment and
// the assembly that fills it: the paired rate query feeds the one algorithm,
// the fact carries the result when there is one, and a session with no
// recorded model-call time shows no rate at all — "0.0 tok/s" is not a speed
// anyone wants to read.
func TestUsageFactsCarriesTheOutputRate(t *testing.T) {
	rep := BuildStatusReport(context.Background(), StatusSource{
		StateRoot: t.TempDir(),
		SessionID: "s1",
		RunStore: &fakeStatusRunStore{
			session:    state.SessionUsage{CompletionTokens: 3000, LLMCalls: 4},
			rateTokens: 3000,
			rateMs:     16_000,
		},
	})
	var sessionLine string
	for _, fact := range UsageFacts(rep) {
		if fact.Label == "Session" {
			sessionLine = fact.Value
		}
	}
	if !strings.Contains(sessionLine, "187.5 tok/s") {
		t.Fatalf("Session fact = %q, want it to carry 187.5 tok/s (3000 tokens over 16 s)", sessionLine)
	}
	if rep.Session.GenerationMs != 16_000 {
		t.Fatalf("GenerationMs = %d, want 16000", rep.Session.GenerationMs)
	}

	// No run recorded a time: the segment stays out, not zero.
	without := BuildStatusReport(context.Background(), StatusSource{
		StateRoot: t.TempDir(),
		SessionID: "s1",
		RunStore: &fakeStatusRunStore{
			session: state.SessionUsage{CompletionTokens: 3000, LLMCalls: 4},
		},
	})
	for _, fact := range UsageFacts(without) {
		if fact.Label == "Session" && strings.Contains(fact.Value, "tok/s") {
			t.Fatalf("Session fact = %q, want no rate segment without a recorded time", fact.Value)
		}
	}
	if without.Session.OutputTokensPerSecond != 0 {
		t.Fatalf("OutputTokensPerSecond = %v, want 0", without.Session.OutputTokensPerSecond)
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

// TestStatusInstructionsLine pins the Instructions row: a file's name is all
// that says where it came from (no origin marker), a file that did not make it
// into the body says why, and a cut one says how much of it arrived.
func TestStatusInstructionsLine(t *testing.T) {
	line := RenderInstructionsLine([]assembly.RuleSource{
		{Name: "AGENTS.md", Loaded: true},
		{Name: "FOREBRAIN.md", Loaded: false, NotLoadedReason: "project not trusted"},
		{Name: "docs/FOREBRAIN.md", Loaded: true, TruncatedTo: 120_000},
	})
	want := "AGENTS.md, FOREBRAIN.md (not loaded: project not trusted), docs/FOREBRAIN.md (truncated to 120k runes)"
	if line != want {
		t.Fatalf("instructions line = %q, want %q", line, want)
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

	// live is what the runtime publishes when the session has no row of its own;
	// nil means "the same as selection", which is what every existing fixture
	// assumes.
	live *ModelSelectionState
	// rows is the install's model table the fixture's configuration resolves
	// against. It is a table of its own because fb_llm_models is where a model
	// is configured (plan 315), and a configuration document cannot carry one.
	rows []appcfg.LLMModelConfig
}

// turnModelRows is how a fixture seeds the install's model table (plan 315):
// one row per model an entry names — `model: [a, b]` expanding the way a
// provider list used to — with the row ids llm-1, llm-2, … in order.
func turnModelRows(entries ...appcfg.AgentLLMProviderConfig) []appcfg.LLMModelConfig {
	var rows []appcfg.LLMModelConfig
	for _, entry := range entries {
		models := append(appcfg.StringList(nil), entry.Models...)
		if len(models) == 0 && entry.Model != "" {
			models = appcfg.StringList{entry.Model}
		}
		for _, model := range models {
			rows = append(rows, appcfg.LLMModelConfig{
				ID:          fmt.Sprintf("llm-%d", len(rows)+1),
				Provider:    entry.Provider,
				Model:       model,
				BaseURL:     entry.BaseURL,
				APIPath:     entry.APIPath,
				APIKey:      entry.APIKey,
				Params:      append(appcfg.LLMRequestParams(nil), entry.Params...),
				URLVerbatim: entry.URLVerbatim,
			})
		}
	}
	return rows
}

// modelSelectionFixtureConfig is the /model fixture's configuration: main runs
// the model table's first row.
const modelSelectionFixtureConfig = "agents:\n  definitions:\n    main:\n      default_model: llm-1\n"

// modelSelectionDocDefaultRow is the configuration document whose main agent
// runs the model table's first row.
const modelSelectionDocDefaultRow = "agents:\n  definitions:\n    main:\n      default_model: llm-1\n"

// modelSelectionFixtureRows are the two models the /model fixture's picker
// switches between: two distinct providers, in table order.
func modelSelectionFixtureRows() []appcfg.LLMModelConfig {
	return turnModelRows(
		appcfg.AgentLLMProviderConfig{Provider: "openai", Model: "gpt-5", APIKey: "${FOREBRAIN_TURN_MODEL_TEST_KEY}", BaseURL: "http://a.test/v1"},
		appcfg.AgentLLMProviderConfig{Provider: "deepseek", Model: "deepseek-chat", APIKey: "${FOREBRAIN_TURN_MODEL_TEST_KEY}", BaseURL: "http://b.test/v1"},
	)
}

func newModelSelectionFixture(t *testing.T, selection ModelSelectionState) *modelSelectionFixture {
	t.Helper()
	return newModelSelectionFixtureWith(t, modelSelectionFixtureConfig, selection, modelSelectionFixtureRows()...)
}

// newModelSelectionFixtureWith is newModelSelectionFixture over a configuration
// document of the test's own: twin rows of one provider/model, each with its
// own endpoint, need a document nothing else in the fixture assumes.
func newModelSelectionFixtureWith(t *testing.T, doc string, selection ModelSelectionState, rows ...appcfg.LLMModelConfig) *modelSelectionFixture {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "forebrain.yaml")
	t.Setenv("FOREBRAIN_TURN_MODEL_TEST_KEY", "test-key")
	if err := os.WriteFile(cfgPath, []byte(doc), 0o600); err != nil {
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
	fx.rows = rows
	h.settings = fx.modelSettings
	return fx
}

// modelSettings answers from the temp config file, the way a surface's live
// handler does.
func (f *modelSelectionFixture) modelSettings(_ string) (ModelSettings, error) {
	cfg, err := loadPersistedDocumentForTest(f.cfgPath)
	if err != nil {
		return ModelSettings{}, err
	}
	cfg.LLMModels = f.rows
	live := f.handler.selection
	if f.live != nil {
		live = *f.live
	}
	return ModelSettings{Config: &cfg, AgentName: "main", Selection: f.handler.selection, LiveSelection: live}, nil
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

// choiceID names the configured row a model is, the way /model's picker and an
// effort choice do.
func (f *modelSelectionFixture) choiceID(t *testing.T, provider, model string) string {
	t.Helper()
	settings, err := f.modelSettings("")
	require.NoError(t, err)
	for _, choice := range ModelChoices(settings.Config, settings.AgentName) {
		if strings.EqualFold(choice.Provider, provider) && strings.EqualFold(choice.Model, model) {
			return choice.ID
		}
	}
	t.Fatalf("no configured choice for %s/%s", provider, model)
	return ""
}

func (f *modelSelectionFixture) reloadConfig(t *testing.T) *appcfg.Root {
	t.Helper()
	cfg, err := loadPersistedDocumentForTest(f.cfgPath)
	require.NoError(t, err)
	cfg.LLMModels = f.rows
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
	require.Equal(t, "deepseek/deepseek-chat", res.Picker.Items[current].Label, "current item = %+v", res.Picker.Items[current])
}

// TestChooseModelAppliesLiveSelectionWithoutWritingTheConfigFile pins the
// cross-process contract: a model picked in one conversation is applied to
// that conversation's runtime and recorded on its row, and the shared
// configuration file — which every process on the machine watches and every
// new runner loads — keeps its exact bytes.
func TestChooseModelAppliesLiveSelectionWithoutWritingTheConfigFile(t *testing.T) {
	f := newModelSelectionFixture(t, ModelSelectionState{})
	before, err := os.ReadFile(f.cfgPath)
	require.NoError(t, err)
	label := f.choiceLabel(t, "deepseek", "deepseek-chat")
	res := Choose(f.ctx, SlashChoice{Command: "model", Value: label})
	require.True(t, res.Handled)
	require.Equal(t, "Switched to deepseek/deepseek-chat.", res.Reply)
	require.NotEqual(t, -1, slices.Index(f.calls(), "select(s1,deepseek/deepseek-chat)"), "SelectModel was not called: %v", f.calls())
	after, err := os.ReadFile(f.cfgPath)
	require.NoError(t, err)
	require.True(t, bytes.Equal(before, after), "the shared config file changed:\nbefore:\n%s\nafter:\n%s", before, after)
	require.Equal(t, "gpt-5", f.reloadConfig(t).LLMModels[0].Model, "config order was rewritten")
}

func TestChooseModelSameSelectionIsNoOp(t *testing.T) {
	f := newModelSelectionFixture(t, ModelSelectionState{Provider: "deepseek", Model: "deepseek-chat", Set: true})
	label := f.choiceLabel(t, "deepseek", "deepseek-chat")
	res := Choose(f.ctx, SlashChoice{Command: "model", Value: label})
	require.True(t, res.Handled)
	require.Equal(t, -1, slices.Index(f.calls(), "select("), "a repeat selection applied a live switch: %v", f.calls())
	require.Equal(t, "Still using deepseek/deepseek-chat.", res.Reply)
}

func newNoRowDeepseekFixture(t *testing.T) *modelSelectionFixture {
	t.Helper()
	f := newModelSelectionFixture(t, ModelSelectionState{}) // 会话没有自己的行
	live := ModelSelectionState{Provider: "deepseek", Model: "deepseek-chat", Set: true}
	f.live = &live
	return f
}

// The picker marks what the conversation actually runs on — the effort picker's
// rule — not the config's order-based default.
func TestExecModelMarksWhatTheRuntimeCarries(t *testing.T) {
	f := newNoRowDeepseekFixture(t)
	res := execModel(f.ctx)
	require.NotNil(t, res.Picker)
	current := -1
	for i, item := range res.Picker.Items {
		if item.Current {
			require.Equal(t, -1, current, "more than one current item")
			current = i
		}
	}
	require.NotEqual(t, -1, current, "no current item: %v", res.Picker.Items)
	require.Equal(t, "deepseek/deepseek-chat", res.Picker.Items[current].Label, "current = %+v", res.Picker.Items[current])
}

func TestChooseModelInForceModelIsNotASwitch(t *testing.T) {
	f := newNoRowDeepseekFixture(t)
	label := f.choiceLabel(t, "deepseek", "deepseek-chat")
	res := Choose(f.ctx, SlashChoice{Command: "model", Value: label})
	require.True(t, res.Handled)
	// calls() 的记录形状是 "select(<sessionID>,<provider/model>)"（recordingSlashHandlers.record），
	// 所以这里必须匹配完整串——`slices.Index(f.calls(), "select(")` 是精确等值匹配，恒为 -1，
	// 那是空断言（既有 :373/:665 就带着这个弱点，不要照抄；:536 的完整串才是有效写法）。
	require.Equal(t, -1, slices.Index(f.calls(), "select(s1,deepseek/deepseek-chat)"), "a repeat selection applied a live switch: %v", f.calls())
	require.Equal(t, "Still using deepseek/deepseek-chat.", res.Reply)
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
	require.Contains(t, res.Reply, "Switched to deepseek/deepseek-chat, but the choice could not be saved")

	f = newModelSelectionFixture(t, ModelSelectionState{})
	f.handler.selectErr = errors.New("provider not configured")
	res = Choose(f.ctx, SlashChoice{Command: "model", Value: label})
	require.True(t, res.Handled)
	require.Contains(t, res.Reply, "Could not switch to deepseek/deepseek-chat: provider not configured")
}

// addConfigEntry appends a provider entry to the fixture's config file, for
// tests that need a specific catalog model configured.
// addConfigEntry adds one more model to the fixture's table: what a write path
// does when a model is configured (plan 315: the table is the only store).
func (f *modelSelectionFixture) addConfigEntry(t *testing.T, provider, model string) {
	t.Helper()
	f.rows = append(f.rows, turnModelRows(appcfg.AgentLLMProviderConfig{
		Provider: provider, Model: model,
		APIKey: "${FOREBRAIN_TURN_MODEL_TEST_KEY}", BaseURL: "http://c.test/v1",
	})...)
}

// reasoningCapableOpenAIModel picks a catalog model that reasons, so the
// effort picker path runs.
// reasoningCapableModel names the openai model these tests drive /model with.
// It is pinned to one id instead of scanning for any reasoning-capable model:
// the levels a model offers are its own catalog data now, and a scan would pick
// whichever model a map walk yielded first — including rows that name a token
// budget instead of discrete levels.
func reasoningCapableModel(t *testing.T) llm.Model {
	t.Helper()
	for _, m := range llm.AllModels() {
		if strings.EqualFold(strings.TrimSpace(m.ID), "openai/gpt-5") {
			return m
		}
	}
	t.Fatal("openai/gpt-5 is missing from the catalog")
	return llm.Model{}
}

func TestChooseModelEffortPickerMarksLiveEffort(t *testing.T) {
	m := reasoningCapableModel(t)
	f := newModelSelectionFixture(t, ModelSelectionState{Provider: "openai", Model: m.APIModel, Effort: "medium", Set: true})
	f.addConfigEntry(t, "openai", m.APIModel)
	// The row, not the label: this fixture configures openai/gpt-5 twice (the
	// base entry and the catalog one added below), so a label names neither.
	res := Choose(f.ctx, SlashChoice{Command: "model", Value: f.choiceID(t, "openai", m.APIModel)})
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

// /model offers a model's own thinking levels rather than one global list: the
// Go subscription's glm-5.3 has three, a model that reasons with no discrete
// level gets no picker at all, and every level the protocol's client can send
// is offered — xhigh included, now that the Anthropic client supports it.
func TestChooseModelUsesTheModelsOwnEffortLevels(t *testing.T) {
	f := newModelSelectionFixture(t, ModelSelectionState{})
	f.addConfigEntry(t, "opencode-go", "glm-5.3")
	f.addConfigEntry(t, "opencode-go", "minimax-m3")
	f.addConfigEntry(t, "opencode-go", "qwen3.8-max")

	for _, tc := range []struct {
		model string
		want  []string
	}{
		{model: "glm-5.3", want: []string{"low", "high", "max"}},
		{model: "minimax-m3"},
		// Anthropic messages: the catalog's levels, all of which the client
		// can send.
		{model: "qwen3.8-max", want: []string{"low", "medium", "xhigh"}},
	} {
		res := Choose(f.ctx, SlashChoice{Command: "model", Value: f.choiceLabel(t, "opencode-go", tc.model)})
		require.True(t, res.Handled)
		if len(tc.want) == 0 {
			require.Nil(t, res.Picker, "%s: nothing to pick, so no effort page", tc.model)
			require.Equal(t, "Switched to opencode-go/"+tc.model+".", res.Reply)
			continue
		}
		require.NotNil(t, res.Picker, tc.model)
		require.Equal(t, "model-effort", res.Picker.Command)
		labels := make([]string, 0, len(res.Picker.Items))
		for _, item := range res.Picker.Items {
			labels = append(labels, item.Label)
		}
		// This conversation pins no level, so the bare 「Default」 entry leads
		// the list, exactly as the web's effort chip draws it; the model's own
		// levels follow, in the one fixed order.
		require.Equal(t, append([]string{""}, tc.want...), labels, tc.model)
	}

	// A level this model does not offer is refused rather than written.
	stale := encodeEffortChoice(f.choiceID(t, "opencode-go", "glm-5.3"), f.choiceLabel(t, "opencode-go", "glm-5.3"), "xhigh", false)
	res := Choose(f.ctx, SlashChoice{Command: "model-effort", Value: stale})
	require.True(t, res.Handled)
	require.Equal(t, "That reasoning effort is no longer offered for this model.", res.Reply)
	require.Equal(t, -1, slices.Index(f.calls(), "select-effort("), "a rejected effort still reached the runtime: %v", f.calls())
}

// twinModelFixture configures deepseek/deepseek-v4-flash twice: one provider
// and model reaching two endpoints, the configuration a pair alone could not
// tell apart.
func twinModelFixture(t *testing.T, selection ModelSelectionState) *modelSelectionFixture {
	t.Helper()
	return newModelSelectionFixtureWith(t, "agents:\n  definitions:\n    main:\n      default_model: llm-1\n", selection,
		turnModelRows(
			appcfg.AgentLLMProviderConfig{Provider: "deepseek", Model: "deepseek-v4-flash", APIKey: "${FOREBRAIN_TURN_MODEL_TEST_KEY}", BaseURL: "http://one.test"},
			appcfg.AgentLLMProviderConfig{Provider: "deepseek", Model: "deepseek-v4-flash", APIKey: "${FOREBRAIN_TURN_MODEL_TEST_KEY}", BaseURL: "http://two.test", APIPath: "/beta/chat/completions"},
		)...)
}

// TestModelPickerTellsTwinRowsApart pins plan 261's first item: two rows of one
// provider/model are two choices — a row id each and each row's own request URL
// in the description column — and picking the second selects that row, which is
// what a surface records on the session row.
func TestModelPickerTellsTwinRowsApart(t *testing.T) {
	f := twinModelFixture(t, ModelSelectionState{})
	res := execModel(f.ctx)
	require.NotNil(t, res.Picker)
	// Exactly the two configured rows.
	require.Len(t, res.Picker.Items, 2)
	first, second := res.Picker.Items[0], res.Picker.Items[1]
	require.NotEmpty(t, first.Value)
	require.NotEmpty(t, second.Value)
	require.NotEqual(t, first.Value, second.Value, "twin rows share a picker value: %+v", res.Picker.Items)
	require.Equal(t, "deepseek/deepseek-v4-flash", first.Label)
	require.Equal(t, "deepseek/deepseek-v4-flash", second.Label)
	require.Equal(t, "http://one.test/chat/completions", first.Description)
	require.Equal(t, "http://two.test/beta/chat/completions", second.Description)
	// Each row names the end of its description a narrow column must keep: the
	// api path, so a shortened URL still says which protocol it speaks.
	require.Equal(t, "/chat/completions", first.EndpointTail)
	require.Equal(t, "/beta/chat/completions", second.EndpointTail)

	// The description is base_url + api_path joined exactly as the client joins
	// them; a 「自定义协议」 row sends to its base url and shows that alone; a
	// row with no endpoint (a login-backed provider) has nothing to show.
	require.Equal(t, "http://two.test/beta/chat/completions", ModelEndpointLabel("http://two.test", "/beta/chat/completions", false))
	require.Equal(t, "http://one.test", ModelEndpointLabel("http://one.test", "/chat/completions", true))
	require.Equal(t, "", ModelEndpointLabel("", "", false))

	// The second row is what the pick selects and records — id included.
	res = Choose(f.ctx, SlashChoice{Command: "model", Value: second.Value})
	require.True(t, res.Handled)
	require.Equal(t, "Switched to deepseek/deepseek-v4-flash.", res.Reply)
	require.Len(t, f.handler.chosen, 1, "calls = %v", f.calls())
	require.Equal(t, second.Value, f.handler.chosen[0].ID, "the record kept the first twin")
}

// TestModelPickerOffersNoAddCustomRow pins the picker as a list of configured
// models only: no row names anything but a model, and every surface is offered
// exactly the same list. Adding a model — a custom endpoint included — is
// /connect's, so nothing here opens it.
func TestModelPickerOffersNoAddCustomRow(t *testing.T) {
	f := twinModelFixture(t, ModelSelectionState{})
	webCtx := f.ctx
	webCtx.Surface = SurfaceWebChat

	term := execModel(f.ctx).Picker
	web := execModel(webCtx).Picker
	require.NotNil(t, term)
	require.NotNil(t, web)
	require.Len(t, term.Items, 2)
	require.Equal(t, len(web.Items), len(term.Items), "the two surfaces offer different lists")
	for i, item := range term.Items {
		require.NotEmpty(t, item.Value)
		require.NotContains(t, item.Value, "add_custom", "a row offers to add a model: %+v", item)
		require.Equal(t, web.Items[i].Value, item.Value)
	}
}

// TestChooseReasoningEffortKeepsTheTwinRowItPicked pins plan 261's second item:
// the effort picker's value carries the row (four segments), so picking a level
// for the second twin keeps the second twin; a value drawn before values named
// the row, whose label two rows share, switches nothing.
func TestChooseReasoningEffortKeepsTheTwinRowItPicked(t *testing.T) {
	f := twinModelFixture(t, ModelSelectionState{})
	items := execModel(f.ctx).Picker.Items
	require.Len(t, items, 2)
	second := items[1]

	res := Choose(f.ctx, SlashChoice{Command: "model", Value: second.Value})
	require.True(t, res.Handled)
	require.NotNil(t, res.Picker)
	require.Equal(t, "model-effort", res.Picker.Command)
	high := ""
	for _, item := range res.Picker.Items {
		if item.Label == "high" {
			high = item.Value
		}
	}
	require.NotEmpty(t, high, "the model's own levels: %+v", res.Picker.Items)
	require.Len(t, strings.Split(high, "\n"), 4, "effort value = %q", high)
	require.Equal(t, second.Value, strings.SplitN(high, "\n", 2)[0], "the effort value names no row")

	res = Choose(f.ctx, SlashChoice{Command: "model-effort", Value: high})
	require.True(t, res.Handled)
	require.Equal(t, "Reasoning effort set to High.", res.Reply, "the reply names the level by its English display label")
	require.Len(t, f.handler.chosen, 2, "calls = %v", f.calls())
	require.Equal(t, second.Value, f.handler.chosen[1].ID, "the effort moved the conversation to the first twin")
	require.NotEqual(t, items[0].Value, f.handler.chosen[1].ID)
	require.NotEqual(t, -1, slices.Index(f.calls(), "select-effort(s1,high)"), "the level did not reach the runtime: %v", f.calls())

	// A picker drawn before its values named the row sends three segments; a
	// label two rows share names neither, so nothing switches.
	res = Choose(f.ctx, SlashChoice{Command: "model-effort", Value: second.Label + "\nhigh\nfalse"})
	require.True(t, res.Handled)
	require.Equal(t, "Several models are configured as deepseek/deepseek-v4-flash; pick one from /model.", res.Reply)
	require.Len(t, f.handler.chosen, 2, "a value naming twins switched anyway: %v", f.calls())
}

// TestChooseModelRefusesALabelThatNamesTwins pins plan 261's third item: a
// client that still sends the label (an unrefreshed web page, an old picker)
// names neither row when two rows share it, so nothing switches and the answer
// says how to pick one.
func TestChooseModelRefusesALabelThatNamesTwins(t *testing.T) {
	f := twinModelFixture(t, ModelSelectionState{})
	res := Choose(f.ctx, SlashChoice{Command: "model", Value: "deepseek/deepseek-v4-flash"})
	require.True(t, res.Handled)
	require.Equal(t, "Several models are configured as deepseek/deepseek-v4-flash; pick one from /model.", res.Reply)
	require.Nil(t, res.Picker, "a value that names neither row must not open the effort page")
	require.Empty(t, f.handler.chosen, "a label naming twins switched anyway: %v", f.calls())
}

// TestExecModelMarksALegacyRowByItsPair pins plan 261's fourth item: a session
// row written before ids existed names no row, so the picker marks the row its
// provider+model pair is — and the marked item still carries that row's id.
func TestExecModelMarksALegacyRowByItsPair(t *testing.T) {
	f := newModelSelectionFixture(t, ModelSelectionState{Provider: "deepseek", Model: "deepseek-chat", Effort: "high", Set: true})
	res := execModel(f.ctx)
	require.NotNil(t, res.Picker)
	current := -1
	for i, item := range res.Picker.Items {
		if item.Current {
			require.Equal(t, -1, current, "more than one current item: %+v", res.Picker.Items)
			current = i
		}
	}
	require.NotEqual(t, -1, current, "no current item: %+v", res.Picker.Items)
	require.Equal(t, "deepseek/deepseek-chat", res.Picker.Items[current].Label)
	require.Equal(t, f.choiceID(t, "deepseek", "deepseek-chat"), res.Picker.Items[current].Value)
}

// TestChooseReasoningEffortDoesNotWriteTheConfigFile pins the same contract for
// the effort half: the effort lands on this conversation's runtime and row,
// and the configured default entry keeps the effort the file already had.
func TestChooseReasoningEffortDoesNotWriteTheConfigFile(t *testing.T) {
	m := reasoningCapableModel(t)
	f := newModelSelectionFixture(t, ModelSelectionState{Provider: "openai", Model: m.APIModel, Effort: "low", Set: true})
	f.addConfigEntry(t, "openai", m.APIModel)
	before, err := os.ReadFile(f.cfgPath)
	require.NoError(t, err)
	value := encodeEffortChoice(f.choiceID(t, "openai", m.APIModel), f.choiceLabel(t, "openai", m.APIModel), "high", false)
	res := Choose(f.ctx, SlashChoice{Command: "model-effort", Value: value})
	require.True(t, res.Handled)
	require.Equal(t, "Reasoning effort set to High.", res.Reply, "the reply names the level by its English display label")
	require.NotEqual(t, -1, slices.Index(f.calls(), "select-effort(s1,high)"), "explicit effort pointer not passed: %v", f.calls())
	after, err := os.ReadFile(f.cfgPath)
	require.NoError(t, err)
	require.True(t, bytes.Equal(before, after), "the shared config file changed:\nbefore:\n%s\nafter:\n%s", before, after)
}

// TestNewConversationInheritsNoModel pins owner's ruling (2026-10-23) that
// /new inherits nothing: a model is a choice of the conversation that made
// it, not of the next one. The new conversation follows the same rule every
// fresh conversation does — the agent's most recently used model, adopted as
// its own row when its first message goes out — and nothing is copied here,
// and the shared configuration file never moves.
func TestNewConversationInheritsNoModel(t *testing.T) {
	f := newModelSelectionFixture(t, ModelSelectionState{Provider: "deepseek", Model: "deepseek-chat", Effort: "high", Set: true})
	before, err := os.ReadFile(f.cfgPath)
	require.NoError(t, err)
	res := Execute(f.ctx, "/new")
	require.True(t, res.Handled)
	require.True(t, res.SessionSwitched, "the surface must switch to the new conversation")
	require.True(t, res.SessionChanged)
	require.NotEmpty(t, res.SessionID)
	require.Empty(t, f.store.copiedModelFrom, "/new must not copy the left conversation's model row")
	require.Empty(t, f.store.copiedModelTo)
	require.Equal(t, "Started a new conversation.", res.Reply)
	after, err := os.ReadFile(f.cfgPath)
	require.NoError(t, err)
	require.True(t, bytes.Equal(before, after), "the shared config file changed")
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

// TestForkCopiesThePlansIntoTheForksOwnDirectory pins /fork's plan semantics:
// the fork starts with its own copy of the source's plan files, the source
// keeps everything, and nothing leaks into the flat project directory.
func TestForkCopiesThePlansIntoTheForksOwnDirectory(t *testing.T) {
	f := newModelSelectionFixture(t, ModelSelectionState{})
	stateRoot := f.ctx.stateRoot()
	srcDir := state.PlanDirForSession(stateRoot, f.ctx.projectKey(), "s1")
	require.NoError(t, os.MkdirAll(srcDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "plan-a.md"), []byte("# Plan A\n"), 0o600))

	res := execFork(f.ctx)
	require.True(t, res.Handled)
	require.True(t, res.SessionSwitched)

	dstDir := state.PlanDirForSession(stateRoot, f.ctx.projectKey(), res.SessionID)
	got, err := os.ReadFile(filepath.Join(dstDir, "plan-a.md"))
	require.NoError(t, err, "the fork must carry a copy of the source's plan")
	require.Equal(t, "# Plan A\n", string(got))
	if _, err := os.Stat(filepath.Join(srcDir, "plan-a.md")); err != nil {
		t.Fatalf("the source's plan must stay where it was: %v", err)
	}
	// Nothing new appears flat in the project plans directory.
	flat := state.PlanDirForProject(stateRoot, f.ctx.projectKey())
	if entries, err := os.ReadDir(flat); err == nil {
		for _, e := range entries {
			require.NotEqual(t, "plan-a.md", e.Name(), "the copy must not land in the flat plans directory")
		}
	} else {
		require.True(t, os.IsNotExist(err), "reading the flat plans directory: %v", err)
	}
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

// The /status Language servers row appears only when a server is enabled,
// with the /status counts line, right after MCP servers.
func TestStatusFactsLSPRow(t *testing.T) {
	withoutLSP := BuildStatusReport(context.Background(), StatusSource{})
	for _, f := range StatusFacts(withoutLSP) {
		require.NotEqual(t, "Language servers", f.Label, "a report without LSP data must not carry the row")
	}

	snap := event.LSPSnapshot{Servers: []event.LSPServerStatus{
		{ID: "gopls", Enabled: true, State: event.LSPStateReady},
		{ID: "gopls2", Enabled: true, State: event.LSPStateReady},
		{ID: "pyright", Enabled: true, State: event.LSPStateFailed},
	}}
	rep := BuildStatusReport(context.Background(), StatusSource{LSP: &snap})
	require.Equal(t, "2 running, 1 failed", rep.LSP)
	rep.MCP = MCPCountLine{Connected: 1}

	labels := make([]string, 0)
	values := map[string]string{}
	for _, f := range StatusFacts(rep) {
		labels = append(labels, f.Label)
		values[f.Label] = f.Value
	}
	require.Equal(t, "2 running, 1 failed /lsp", values["Language servers"])
	require.Greater(t, slices.Index(labels, "Language servers"), slices.Index(labels, "MCP servers"), "the LSP row must follow MCP servers:\n%v", labels)
}

// With no live selection, the picker's "current" mark and chooseModel's
// "changed" diff follow the effective default — the entry default_model names
// — not index zero.
func TestModelPickerMarksTheDefaultModelEntry(t *testing.T) {
	f := newModelSelectionFixture(t, ModelSelectionState{})
	seed, err := os.ReadFile(f.cfgPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(f.cfgPath, append([]byte(""), bytes.Replace(seed,
		[]byte("      default_model: llm-1\n"), []byte("      default_model: llm-2\n"), 1)...), 0o600))

	res := execModel(f.ctx)
	require.True(t, res.Handled)
	require.NotNil(t, res.Picker)
	current := -1
	for i, item := range res.Picker.Items {
		if item.Current {
			require.Equal(t, -1, current, "more than one current item")
			current = i
		}
	}
	require.NotEqual(t, -1, current, "no current item")
	require.Equal(t, "deepseek/deepseek-chat", res.Picker.Items[current].Label, "current item = %+v, want the default_model entry", res.Picker.Items[current])

	// Choosing the default_model entry is a no-op, not a switch.
	label := f.choiceLabel(t, "deepseek", "deepseek-chat")
	res = Choose(f.ctx, SlashChoice{Command: "model", Value: label})
	require.True(t, res.Handled)
	require.Equal(t, -1, slices.Index(f.calls(), "select("), "re-selecting the default applied a live switch: %v", f.calls())
	require.Equal(t, "Still using deepseek/deepseek-chat.", res.Reply)
}

// test-only readers for a whole configuration document.

// loadDocumentForTest reads a whole configuration document — stored sections
// included — the way appcfg.Load read one before plan 207 narrowed forebrain.yaml
// to its supported keys: parse and normalize, then Load's tail (.env merge,
// ${ENV} expansion, validation). A test that assembles a runtime configuration
// in a file goes through here, because the file itself no longer carries those
// sections.
func loadDocumentForTest(path string) (appcfg.Root, error) {
	root, err := loadPersistedDocumentForTest(path)
	if err != nil {
		return appcfg.Root{}, err
	}
	if err := appcfg.FinishLoad(&root); err != nil {
		return appcfg.Root{}, err
	}
	return root, nil
}

// loadPersistedDocumentForTest is the same read without Load's tail: a ${ENV}
// reference stays a reference, as appcfg.LoadPersisted left it.
func loadPersistedDocumentForTest(path string) (appcfg.Root, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return appcfg.Root{}, err
	}
	return appcfg.ParseRootYAML(body)
}

// A review request's identity is the configured row it names. The pair is
// always required and must agree with the row — a request that names no model
// is not a request — and the id, when it carries one, narrows that pair to the
// exact row, so two rows of one provider/model stay apart.
func TestPlanReviewSelectionMatchesTheRowItNames(t *testing.T) {
	first := PlanReviewModelOption{ID: "row-1", Provider: "openai", Model: "gpt-4o"}
	second := PlanReviewModelOption{ID: "row-2", Provider: "openai", Model: "gpt-4o"}

	require.True(t, second.MatchesSelection("row-2", "openai", "gpt-4o"))
	require.False(t, first.MatchesSelection("row-2", "openai", "gpt-4o"), "the twin row is not the row the request named")
	require.False(t, second.MatchesSelection("row-2", "openai", "gpt-4o-mini"), "a row only answers for its own pair")

	// A client that predates ids sends the pair alone: both rows answer, and
	// the first one serves it, the way it always did.
	require.True(t, first.MatchesSelection("", "openai", "gpt-4o"))
	require.True(t, second.MatchesSelection("", "openai", "gpt-4o"))

	// An id and a pair that disagree name no configured row at all.
	require.False(t, first.MatchesSelection("row-2", "openai", "gpt-4o-mini"))
	require.False(t, second.MatchesSelection("row-1", "openai", "gpt-4o-mini"))

	// A request with no model is not a request: the id alone does not match.
	require.False(t, first.MatchesSelection("row-1", "", ""))
	require.False(t, first.MatchesSelection("row-1", "openai", ""))
	require.False(t, first.MatchesSelection("row-1", "", "gpt-4o"))

	// An offer built from a config that carries no ids cannot answer an id.
	anonymous := PlanReviewModelOption{Provider: "openai", Model: "gpt-4o"}
	require.True(t, anonymous.MatchesSelection("", "openai", "gpt-4o"))
	require.False(t, anonymous.MatchesSelection("row-1", "openai", "gpt-4o"))
}

// installRowEfforts gives one configured row the thinking levels the model
// table would project, the way adopting a configuration installs them.
func installRowEfforts(t *testing.T, rowID, pair string, efforts []string) {
	t.Helper()
	llm.SetModelOverrides(
		map[string]llm.ModelOverride{rowID: {Efforts: efforts}},
		map[string]string{rowID: pair},
	)
	t.Cleanup(func() { llm.SetModelOverrides(nil, nil) })
}

// modelEffortFixture configures one openai/gpt-5 entry whose row configures
// `configured` as its thinking level ("" = none), for a conversation pinned to
// `pinned`.
func modelEffortFixture(t *testing.T, configured, pinned string) *modelSelectionFixture {
	t.Helper()
	m := reasoningCapableModel(t)
	entry := appcfg.AgentLLMProviderConfig{
		Provider: "openai", Model: m.APIModel,
		APIKey: "${FOREBRAIN_TURN_MODEL_TEST_KEY}", BaseURL: "http://c.test/v1",
	}
	if configured != "" {
		entry.Params = appcfg.WithReasoningEffort(nil, configured)
	}
	selection := ModelSelectionState{Provider: "openai", Model: m.APIModel, Set: true}
	if pinned != "" {
		selection.Effort = pinned
	}
	return newModelSelectionFixtureWith(t, modelSelectionDocDefaultRow, selection, turnModelRows(entry)...)
}

func effortPickerItems(t *testing.T, f *modelSelectionFixture, provider, model string) []PickerItem {
	t.Helper()
	res := Choose(f.ctx, SlashChoice{Command: "model", Value: f.choiceID(t, provider, model)})
	require.True(t, res.Handled)
	require.NotNil(t, res.Picker, "no effort page: %+v", res)
	require.Equal(t, "model-effort", res.Picker.Command)
	return res.Picker.Items
}

// The /model effort picker draws the same list the web's effort chip does: the
// levels in the one fixed order (none brought to the front even when the row
// lists it last), and the levels this build does not rank after the known ones.
// The levels themselves are the catalog's whenever it describes the model, so
// the row's own stale list is not what the picker offers (owner 2026-11-16).
func TestModelEffortPickerMatchesTheWebChipOrder(t *testing.T) {
	m := reasoningCapableModel(t)
	f := modelEffortFixture(t, "", "low")
	installRowEfforts(t, f.choiceID(t, "openai", m.APIModel), "openai/"+m.APIModel,
		[]string{"high", "none", "medium", "low"})

	var labels, values []string
	for _, item := range effortPickerItems(t, f, "openai", m.APIModel) {
		labels = append(labels, item.Label)
		values = append(values, item.ValueLabel)
	}
	// This conversation is pinned to "low" and the row configures no level, so
	// the bare Default entry leads; the catalog's levels follow in EFFORT_LEVELS
	// order — the row's list, which names "none" and drops "minimal", is not
	// what the vendor says this model offers.
	require.Equal(t, []string{"", "minimal", "low", "medium", "high"}, labels,
		"the levels are the catalog's, in EFFORT_LEVELS order")
	require.Equal(t, []string{"Default", "Minimal", "Low", "Medium", "High"}, values,
		"each level carries the English name the TUI shows")

	// The approval gate's second screen sorts through the same helper, so the
	// two pickers cannot drift apart.
	require.Equal(t, []string{"none", "low", "medium", "high"},
		OrderedEfforts([]string{"high", "none", "medium", "low"}))
}

// A row whose model the catalog does not describe keeps the levels it was
// configured with — the setup wizard's gap-filler, the only answer it has — and
// those still come out in the one order every picker draws, not the order the
// row listed them.
func TestModelEffortPickerOrdersTheRowsOwnLevelsWhenTheCatalogHasNone(t *testing.T) {
	const model = "gpt-effort-none"
	f := newModelSelectionFixtureWith(t, modelSelectionDocDefaultRow,
		ModelSelectionState{Provider: "openai", Model: model, Set: true},
		turnModelRows(appcfg.AgentLLMProviderConfig{
			Provider: "openai", Model: model,
			APIKey: "${FOREBRAIN_TURN_MODEL_TEST_KEY}", BaseURL: "http://c.test/v1",
		})...)
	installRowEfforts(t, f.choiceID(t, "openai", model), "openai/"+model,
		[]string{"high", "none", "medium", "low"})

	var labels []string
	for _, item := range effortPickerItems(t, f, "openai", model) {
		labels = append(labels, item.Label)
	}
	require.Equal(t, []string{"", "none", "low", "medium", "high"}, labels,
		"a row the catalog does not describe still offers its own levels, in EFFORT_LEVELS order")
}

// The bare 「Default」 entry appears only when the empty level is a real choice:
// the conversation pins no level, or the row configures none. When both have a
// value, offering it would be a second copy of the one the row configures.
func TestModelEffortPickerOffersDefaultOnlyWhenTheTruthIsToShow(t *testing.T) {
	m := reasoningCapableModel(t)

	// Nothing pinned: Default leads the list.
	unpinned := modelEffortFixture(t, "", "")
	require.Equal(t, "", effortPickerItems(t, unpinned, "openai", m.APIModel)[0].Label,
		"an unpinned conversation starts on Default")

	// A pinned level with a row that configures none: still Default first.
	rowWithoutConfig := modelEffortFixture(t, "", "medium")
	require.Equal(t, "", effortPickerItems(t, rowWithoutConfig, "openai", m.APIModel)[0].Label,
		"a row that configures no level still offers the empty one")

	// Both have a value: no Default entry at all.
	bothSet := modelEffortFixture(t, "high", "medium")
	for _, item := range effortPickerItems(t, bothSet, "openai", m.APIModel) {
		require.NotEqual(t, "", item.Label,
			"a pinned level with a configured row offers no Default entry")
	}
}

// 「the level the row configures」 and 「the level this conversation uses」 are two
// different facts: the gear marks the first, Current marks the second. Merging
// them is what made the old picker look as though the setting had not taken.
func TestModelEffortPickerMarksFromSettingApartFromCurrent(t *testing.T) {
	m := reasoningCapableModel(t)
	f := modelEffortFixture(t, "high", "medium")
	installRowEfforts(t, f.choiceID(t, "openai", m.APIModel), "openai/"+m.APIModel,
		[]string{"low", "medium", "high"})

	var fromSetting, current []string
	for _, item := range effortPickerItems(t, f, "openai", m.APIModel) {
		if item.FromSetting {
			fromSetting = append(fromSetting, item.Label)
		}
		if item.Current {
			current = append(current, item.Label)
		}
	}
	require.Equal(t, []string{"high"}, fromSetting, "the gear marks the level the row is configured with")
	require.Equal(t, []string{"medium"}, current, "Current marks the level the conversation is pinned to")
}

// The English label table is a cross-surface contract: the TUI's lists and the
// web's English UI must name the levels the same way. The web is the source, not
// a copy of it: the values are read out of its own locale file, so a wording
// changed there fails here instead of drifting silently (plan 318 F8).
func TestEffortEnglishLabelMatchesTheWebEnTable(t *testing.T) {
	keys := map[string]string{
		"":        "chat.effortDefault",
		"none":    "chat.effortNone",
		"minimal": "chat.effortMinimal",
		"low":     "chat.effortLow",
		"medium":  "chat.effortMedium",
		"high":    "chat.effortHigh",
		"xhigh":   "chat.effortXhigh",
		"max":     "chat.effortMax",
	}
	en := webLocaleBlock(t, "en")
	zh := webLocaleBlock(t, "zh")
	for id, key := range keys {
		want, ok := en[key]
		require.True(t, ok, "frontend/src/locales/index.ts en is missing %q", key)
		require.Equal(t, want, EffortEnglishLabel(id),
			"level %q is named %q in frontend/src/locales/index.ts (en %s)", id, want, key)
		// en is typed Record<keyof typeof zh, string>, so the zh block defines the
		// key set: a key only one block carries would not type-check.
		require.NotEmpty(t, zh[key], "frontend/src/locales/index.ts zh is missing %q", key)
	}
	require.Equal(t, "ultra", EffortEnglishLabel("ultra"), "a level this build does not rank keeps its own id")
	require.Equal(t, "High", EffortEnglishLabel(" HIGH "), "the id is matched case- and space-insensitively")
}

// The pickers share one order as well as one wording: the web chip's
// EFFORT_LEVELS is the sequence every surface draws, read out of its own source
// (plan 318 F5/F8).
func TestEffortOrderMatchesTheWebEffortLevels(t *testing.T) {
	want := webEffortLevels(t)
	reversed := make([]string, 0, len(want))
	for i := len(want) - 1; i >= 0; i-- {
		reversed = append(reversed, want[i])
	}
	require.Equal(t, want, OrderedEfforts(reversed),
		"frontend/src/lib/effortLabel.ts EFFORT_LEVELS is the order every picker draws")
}

// webLocaleBlock reads one locale block out of the web's own source: the values
// are the contract, so the test reads the file rather than restating them.
func webLocaleBlock(t *testing.T, locale string) map[string]string {
	t.Helper()
	text := readWebSource(t, filepath.Join("locales", "index.ts"))
	out := map[string]string{}
	inBlock := false
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "const "+locale) {
			inBlock = true
			continue
		}
		if !inBlock {
			continue
		}
		if trimmed == "}" {
			break
		}
		if key, value, ok := webLocaleEntry(trimmed); ok {
			out[key] = value
		}
	}
	require.NotEmpty(t, out, "frontend/src/locales/index.ts carries no %s block", locale)
	return out
}

// webLocaleEntry reads one `'key': 'value',` line of a locale block.
func webLocaleEntry(line string) (string, string, bool) {
	if !strings.HasPrefix(line, "'") {
		return "", "", false
	}
	keyEnd := strings.Index(line[1:], "':")
	if keyEnd < 0 {
		return "", "", false
	}
	rest := strings.TrimSpace(line[1+keyEnd+2:])
	if !strings.HasPrefix(rest, "'") {
		return "", "", false
	}
	valueEnd := strings.Index(rest[1:], "'")
	if valueEnd < 0 {
		return "", "", false
	}
	return line[1 : 1+keyEnd], rest[1 : 1+valueEnd], true
}

// webEffortLevels reads the web chip's EFFORT_LEVELS, the order every picker
// draws its levels in.
func webEffortLevels(t *testing.T) []string {
	t.Helper()
	const marker = "EFFORT_LEVELS = ["
	text := readWebSource(t, filepath.Join("lib", "effortLabel.ts"))
	start := strings.Index(text, marker)
	require.GreaterOrEqual(t, start, 0, "frontend/src/lib/effortLabel.ts declares no %s", strings.TrimSpace(marker))
	rest := text[start+len(marker):]
	end := strings.Index(rest, "]")
	require.GreaterOrEqual(t, end, 0, "frontend/src/lib/effortLabel.ts EFFORT_LEVELS is not closed")
	var levels []string
	for _, part := range strings.Split(rest[:end], ",") {
		if part = strings.TrimSpace(part); len(part) >= 2 && strings.HasPrefix(part, "'") {
			levels = append(levels, strings.Trim(part, "'"))
		}
	}
	require.NotEmpty(t, levels, "frontend/src/lib/effortLabel.ts declares no level")
	return levels
}

// readWebSource reads one file of the web surface's own source tree, so a test
// pins what ships instead of a copy of it.
func readWebSource(t *testing.T, rel string) string {
	t.Helper()
	path := filepath.Join("..", "..", "frontend", "src", rel)
	data, err := os.ReadFile(path)
	require.NoError(t, err, "read %s", path)
	return string(data)
}

// The bare Default entry sends an empty level, and the encoded value has to
// survive the round trip with its three companions intact.
func TestEffortChoiceRoundTripsTheEmptyDefault(t *testing.T) {
	value := encodeEffortChoice("row-2", "openai/gpt-5", "", false)
	require.Len(t, strings.Split(value, "\n"), 4)
	modelID, label, effort, modelChanged := decodeEffortChoice(value)
	require.Equal(t, "row-2", modelID)
	require.Equal(t, "openai/gpt-5", label)
	require.Equal(t, "", effort)
	require.False(t, modelChanged)
}

// Taking the bare Default entry is a real choice: it pins the empty level (the
// provider decides), while a level the row does not offer is still refused.
func TestChooseReasoningEffortAcceptsTheExplicitDefault(t *testing.T) {
	m := reasoningCapableModel(t)
	f := modelEffortFixture(t, "", "high")
	value := encodeEffortChoice(f.choiceID(t, "openai", m.APIModel), f.choiceLabel(t, "openai", m.APIModel), "", false)

	res := Choose(f.ctx, SlashChoice{Command: "model-effort", Value: value})
	require.True(t, res.Handled)
	require.Equal(t, "Reasoning effort set to Default.", res.Reply)
	require.NotEqual(t, -1, slices.Index(f.calls(), "select-effort(s1,)"),
		"the empty level must reach the runtime: %v", f.calls())

	// An unknown non-empty level is still refused, and nothing is written.
	before := len(f.handler.chosen)
	stale := encodeEffortChoice(f.choiceID(t, "openai", m.APIModel), f.choiceLabel(t, "openai", m.APIModel), "ultra", false)
	res = Choose(f.ctx, SlashChoice{Command: "model-effort", Value: stale})
	require.True(t, res.Handled)
	require.Equal(t, "That reasoning effort is no longer offered for this model.", res.Reply)
	require.Len(t, f.handler.chosen, before, "an out-of-range level wrote anyway: %v", f.calls())
	require.NotContains(t, res.Reply, "at  reasoning effort", "the reply must never spell an empty level into the sentence")
}
