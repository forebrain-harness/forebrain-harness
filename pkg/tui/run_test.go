package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/assembly"
	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	mcppkg "github.com/forebrain-harness/forebrain-harness/pkg/mcp"
	runpkg "github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/skill"
	statepkg "github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
	"github.com/stretchr/testify/require"
)

// The main agent's plan update has no transcript row of its own: the hook
// draws its card live and lays down the canonical event a resume replays, in
// the same call, so the two can never disagree about what the plan says.
func TestMainAgentPlanUpdateIsBothDrawnAndPersisted(t *testing.T) {
	ctx := context.Background()
	db, err := statepkg.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	require.NoError(t, err)
	defer db.Close()

	runSvc := &statepkg.RunStore{DB: db}
	require.NoError(t, statepkg.NewSessionStore(db, "main").Ensure(ctx, "sid", "sid"))
	run, err := runSvc.CreateRun(ctx, "sid", "coordinate work")
	require.NoError(t, err)

	cfg := &appcfg.Root{
		Agents: appcfg.AgentsSection{
			Definitions: map[string]appcfg.AgentDefinition{
				"main": {
					LLMProviders: []appcfg.AgentLLMProviderConfig{{
						Provider: "openai",
						Model:    "test-model",
						APIKey:   "test-key",
						BaseURL:  "http://127.0.0.1:9/v1",
					}},
				},
			},
		},
	}
	runner := &runpkg.Runner{Deps: &runpkg.Deps{Home: t.TempDir(), AppCfg: cfg}}
	// Env is what production always has, and tool state is now read through
	// it rather than through the Runner (R4).
	s := sessionEnv{Home: runner.Home, RunSvc: runSvc, Runner: runner}.session()
	gotCh := make(chan PlanUpdatedMsg, 1)
	s.PrependUINotify(func(msg any) {
		if m, ok := msg.(PlanUpdatedMsg); ok {
			gotCh <- m
		}
	})

	restore := s.installRunAuditStepHook("sid")
	defer restore()
	tools := runner.Tools()
	require.NotNil(t, tools)
	hook := tools.StepHook()
	require.NotNil(t, hook)

	plan := event.PlanUpdatedPayload{
		Title: "Updated Plan",
		Items: []event.PlanUpdateItem{
			{ID: "1", Content: "Inspect code", Status: "in_progress"},
		},
		Total: 1,
	}
	// A plan update is the user-facing form of a session_todo call, so it
	// carries that tool's name the way the runtime emits it.
	hook(tool.WithRunID(llm.WithAgentSessionID(ctx, "sid"), run.ID), tool.StepEvent{
		Kind:       event.RunEventPlanUpdated,
		ToolName:   "session_todo",
		PlanUpdate: &plan,
	})

	require.Eventually(t, func() bool {
		select {
		case got := <-gotCh:
			return got.Payload.Title == "Updated Plan"
		default:
			return false
		}
	}, time.Second, 10*time.Millisecond)

	events, err := runSvc.ListRunEvents(ctx, run.ID, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, event.RunEventPlanUpdated, events[0].Type)
}

func TestPreparedTUIRunKeepsItsAuditHookAfterForegroundCleanup(t *testing.T) {
	ctx := context.Background()
	db, err := statepkg.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	require.NoError(t, err)
	defer db.Close()

	runSvc := &statepkg.RunStore{DB: db}
	require.NoError(t, statepkg.NewSessionStore(db, "main").Ensure(ctx, "session-a", "session-a"))
	runRecord, err := runSvc.CreateRun(ctx, "session-a", "background task")
	require.NoError(t, err)
	cfg := &appcfg.Root{
		Agents: appcfg.AgentsSection{
			Definitions: map[string]appcfg.AgentDefinition{
				"main": {
					LLMProviders: []appcfg.AgentLLMProviderConfig{{
						Provider: "openai",
						Model:    "test-model",
						APIKey:   "test-key",
						BaseURL:  "http://127.0.0.1:9/v1",
					}},
				},
			},
		},
	}
	runner := &runpkg.Runner{Deps: &runpkg.Deps{Home: t.TempDir(), AppCfg: cfg}}
	s := sessionEnv{Home: runner.Home, RunSvc: runSvc, Runner: runner}.session()

	runCtx, _, cleanupStream := s.prepareTUIAgentBase("session-a", context.Background())
	require.NotNil(t, tool.StepHookFromContext(runCtx, nil))

	cleanupStream()
	require.Nil(t, runner.Tools().StepHook(), "foreground cleanup should restore the shared fallback")
	frozen := tool.StepHookFromContext(runCtx, nil)
	require.NotNil(t, frozen, "detached work must retain its per-run hook")
	plan := event.PlanUpdatedPayload{Title: "Frozen Plan", Total: 1, Items: []event.PlanUpdateItem{{ID: "1", Content: "stay up to date"}}}
	frozen(tool.WithRunID(runCtx, runRecord.ID), tool.StepEvent{Kind: event.RunEventPlanUpdated, ToolName: "session_todo", PlanUpdate: &plan})
	events, err := runSvc.ListRunEvents(ctx, runRecord.ID, 10)
	require.NoError(t, err)
	require.Len(t, events, 1, "the frozen hook must remain functional after foreground cleanup")
}

// TestRunAuditStepHookPopulatesSubagentToolContent guards that a completed
// subagent tool step (HookAgentID set in context) emits a MsgKindTool with
// non-empty Content. Without it the subagent's per-agent view renders
// "(no output)" for every tool, since that view is fed only by these
// AgentID-tagged frames — the main-agent Content path (notifyToolStepHooks)
// never fires for subagent steps.
func TestRunAuditStepHookPopulatesSubagentToolContent(t *testing.T) {
	ctx := context.Background()
	db, err := statepkg.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	require.NoError(t, err)
	defer db.Close()

	runSvc := &statepkg.RunStore{DB: db}
	require.NoError(t, statepkg.NewSessionStore(db, "main").Ensure(ctx, "sid", "sid"))
	run, err := runSvc.CreateRun(ctx, "sid", "subagent work")
	require.NoError(t, err)

	cfg := &appcfg.Root{
		Agents: appcfg.AgentsSection{
			Definitions: map[string]appcfg.AgentDefinition{
				"main": {
					LLMProviders: []appcfg.AgentLLMProviderConfig{{
						Provider: "openai",
						Model:    "test-model",
						APIKey:   "test-key",
						BaseURL:  "http://127.0.0.1:9/v1",
					}},
				},
			},
		},
	}
	runner := &runpkg.Runner{Deps: &runpkg.Deps{Home: t.TempDir(), AppCfg: cfg}}
	// Env is what production always has, and tool state is now read through
	// it rather than through the Runner (R4).
	s := sessionEnv{Home: runner.Home, RunSvc: runSvc, Runner: runner}.session()
	toolCh := make(chan Message, 4)
	s.PrependUINotify(func(msg any) {
		if m, ok := msg.(NewMessageMsg); ok && m.Msg.Kind == MsgKindTool {
			toolCh <- m.Msg
		}
	})

	restore := s.installRunAuditStepHook("sid")
	defer restore()
	tools := runner.Tools()
	require.NotNil(t, tools)
	hook := tools.StepHook()
	require.NotNil(t, hook)

	stepCtx := tool.WithHookAgentID(
		tool.WithRunID(llm.WithAgentSessionID(ctx, "sid"), run.ID),
		"task-42",
	)
	hook(stepCtx, tool.StepEvent{
		Kind:     event.RunEventToolCompleted,
		StepID:   "call-1",
		ToolName: "read_file",
		Input:    map[string]any{"file_path": "/tmp/a.go"},
		Output:   map[string]any{"preview_text": "package main\n\nfunc main() {}\n"},
		Duration: 325 * time.Millisecond,
	})

	require.Eventually(t, func() bool {
		select {
		case got := <-toolCh:
			return got.AgentID == "task-42" &&
				strings.TrimSpace(got.Content) != "" &&
				strings.Contains(got.Content, "package main") &&
				got.Duration == 325*time.Millisecond &&
				!got.Timestamp.IsZero()
		default:
			return false
		}
	}, time.Second, 10*time.Millisecond)
}

// TestRunAuditStepHookEmitsPrimaryToolMessagesDirectly guards the main-agent
// path against dependence on the asynchronous RunSvc event mirror. The TUI must
// receive both state transitions before the hook returns, even if the run ends
// before a mirror subscriber can consume its persisted steps.
func TestRunAuditStepHookEmitsPrimaryToolMessagesDirectly(t *testing.T) {
	ctx := context.Background()
	db, err := statepkg.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	require.NoError(t, err)
	defer db.Close()

	runSvc := &statepkg.RunStore{DB: db}
	require.NoError(t, statepkg.NewSessionStore(db, "main").Ensure(ctx, "sid", "sid"))
	run, err := runSvc.CreateRun(ctx, "sid", "inspect source")
	require.NoError(t, err)

	cfg := &appcfg.Root{
		Agents: appcfg.AgentsSection{
			Definitions: map[string]appcfg.AgentDefinition{
				"main": {
					LLMProviders: []appcfg.AgentLLMProviderConfig{{
						Provider: "openai",
						Model:    "test-model",
						APIKey:   "test-key",
						BaseURL:  "http://127.0.0.1:9/v1",
					}},
				},
			},
		},
	}
	runner := &runpkg.Runner{Deps: &runpkg.Deps{Home: t.TempDir(), AppCfg: cfg}}
	// Env is what production always has, and tool state is now read through
	// it rather than through the Runner (R4).
	s := sessionEnv{Home: runner.Home, RunSvc: runSvc, Runner: runner}.session()
	toolCh := make(chan Message, 2)
	s.PrependUINotify(func(msg any) {
		if m, ok := msg.(NewMessageMsg); ok && m.Msg.Kind == MsgKindTool {
			toolCh <- m.Msg
		}
	})

	restore := s.installRunAuditStepHook("sid")
	defer restore()
	hook := runner.Tools().StepHook()
	require.NotNil(t, hook)
	stepCtx := tool.WithRunID(llm.WithAgentSessionID(ctx, "sid"), run.ID)
	step := tool.StepEvent{
		StepID:   "call-primary-1",
		ToolName: "read_file",
		Input:    map[string]any{"file_path": "/tmp/a.go"},
		Output:   map[string]any{"preview_text": "package main\n"},
	}

	step.Kind = event.RunEventToolStarted
	hook(stepCtx, step)
	step.Kind = event.RunEventToolCompleted
	hook(stepCtx, step)

	// No Eventually: direct publication must occur in the StepHook call rather
	// than relying on a later mirror goroutine.
	started := <-toolCh
	completed := <-toolCh
	for _, got := range []Message{started, completed} {
		require.Empty(t, got.AgentID)
		require.Equal(t, "call-primary-1", got.StepID)
		require.Equal(t, "read_file", got.ToolName)
	}
	require.Equal(t, "running", started.ToolMeta.Status)
	require.Equal(t, "completed", completed.ToolMeta.Status)

	events, err := runSvc.ListRunEvents(ctx, run.ID, 10)
	require.NoError(t, err)
	require.Len(t, events, 2)
}

// TestPublishRunEventDrawsTheCompactionLifecycle pins the terminal's reading
// of a compaction's events: each becomes the message that moves its card, and
// the canonical payload reaches the reducer whole.
func TestPublishRunEventDrawsTheCompactionLifecycle(t *testing.T) {
	s := &ChatSession{}
	defer s.Close()
	got := make(chan any, 8)
	s.PrependUINotify(func(msg any) { got <- msg })
	publish := func(typ string, payload any) {
		t.Helper()
		if err := s.publishRunEvent(context.Background(), event.NewRunEvent("", "run-1", "session-1", typ, payload, time.Now())); err != nil {
			t.Fatal(err)
		}
	}
	publish(event.RunEventContextCompacting, event.ContextCompactingPayload{CompactionID: "c1", Trigger: "auto", TokensBefore: 182_000, AgentID: "explorer"})
	publish(event.RunEventContextCompactProgress, event.ContextCompactProgressPayload{CompactionID: "c1", Percent: 40, Phase: event.CompactPhaseSummarizing, AgentID: "explorer"})
	publish(event.RunEventContextCompacted, event.ContextCompactedPayload{
		CompactionID: "c1", AgentID: "explorer", Strategy: "remote_v2", SummarySource: "remote_compaction",
		Scope: "total", BoundaryID: "window-3", WindowNumber: 3, Reactive: true, TokensBefore: 182_000, TokensAfter: 64_000,
	})
	publish(event.RunEventContextCompactError, event.ContextCompactFailedPayload{CompactionID: "c2", Cancelled: true})
	next := func() any {
		t.Helper()
		select {
		case msg := <-got:
			return msg
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for a compaction message")
			return nil
		}
	}
	if m, ok := next().(ContextCompactingMsg); !ok || m.CompactionID != "c1" || m.AgentID != "explorer" || m.TokensBefore != 182_000 {
		t.Fatalf("started = %#v", m)
	}
	if m, ok := next().(ContextCompactProgressMsg); !ok || m.Percent != 40 || m.Phase != event.CompactPhaseSummarizing {
		t.Fatalf("progress = %#v", m)
	}
	done, ok := next().(ContextCompactedMsg)
	if !ok || done.Payload.Strategy != "remote_v2" || done.Payload.BoundaryID != "window-3" || done.Payload.WindowNumber != 3 || !done.Payload.Reactive {
		t.Fatalf("compacted = %#v", done)
	}
	// A subagent's compaction leaves the conversation's footer as it was, so
	// the next message is the cancellation, not a budget update.
	if m, ok := next().(ContextCompactFailedMsg); !ok || !m.Cancelled || m.CompactionID != "c2" {
		t.Fatalf("cancelled = %#v", m)
	}
}

func TestRunEventProjectorMapsSubagentSpawn(t *testing.T) {
	s := &ChatSession{}
	defer s.Close()
	got := make(chan any, 1)
	s.PrependUINotify(func(msg any) { got <- msg })
	evt := event.NewRunEvent("", "run-1", "session-1", event.RunEventSubagentSpawned, event.SubagentSpawnedPayload{
		AgentID: "task-1", AgentType: "explore", TaskID: "task-1", Task: "inspect",
	}, time.Now())
	if err := s.publishRunEvent(context.Background(), evt); err != nil {
		t.Fatal(err)
	}
	select {
	case raw := <-got:
		msg, ok := raw.(SubagentSpawnedMsg)
		if !ok || msg.AgentID != "task-1" || msg.Task != "inspect" {
			t.Fatalf("message = %#v", raw)
		}
	case <-time.After(time.Second):
		t.Fatal("projected event was not delivered")
	}
}

// A subagent's provider-executed web search arrives as a run event rather than
// through the StepHook (it has no client-side tool call). It must reach the
// surface as a tool message carrying the roster key, which is what the renderer
// routes on: without the tag the search card would be drawn in the primary
// conversation, and without the message it would not be shown at all.
func TestRunEventProjectorMapsSubagentProviderWebSearch(t *testing.T) {
	s := &ChatSession{}
	defer s.Close()
	got := make(chan any, 2)
	s.PrependUINotify(func(msg any) { got <- msg })

	evt := event.NewRunEvent("", "child-run-1", "session-1", event.RunEventToolCompleted, event.ToolCallCompletedPayload{
		Kind:        event.RunEventToolCompleted,
		StepID:      tool.ProviderWebSearchStepID("ws-1"),
		Description: tool.ProviderWebSearchSummary("forebrain routing", true),
		ToolName:    tool.ProviderWebSearchToolName,
		ToolMeta: event.ToolCallMeta{
			ToolName: tool.ProviderWebSearchToolName,
			Status:   "completed",
			AgentID:  "task-search-1",
		},
	}, time.Now())
	if err := s.publishRunEvent(context.Background(), evt); err != nil {
		t.Fatal(err)
	}

	select {
	case raw := <-got:
		msg, ok := raw.(NewMessageMsg)
		if !ok {
			t.Fatalf("message = %#v", raw)
		}
		if msg.Msg.Kind != MsgKindTool || msg.Msg.AgentID != "task-search-1" {
			t.Fatalf("message = %#v, want a tool message tagged with the roster key", msg.Msg)
		}
		if msg.Msg.ToolName != tool.ProviderWebSearchToolName || msg.Msg.Summary != "Searched the web for forebrain routing" {
			t.Fatalf("message = %#v, want the shared web-search step shape", msg.Msg)
		}
		// The renderer keys the per-agent view off this frame's AgentID.
		r := &Reducer{}
		frames := r.Reduce(msg).Frames
		if len(frames) == 0 || frames[len(frames)-1].AgentID != "task-search-1" {
			t.Fatalf("frames = %#v, want the tool frame tagged for the subagent's view", frames)
		}
	case <-time.After(time.Second):
		t.Fatal("projected event was not delivered")
	}
}

type stubRawInputController struct{}

func (stubRawInputController) EnterRaw() error { return nil }
func (stubRawInputController) Restore() error  { return nil }

type shutdownCallRecorder struct {
	calls  []string
	writes string
}

func (r *shutdownCallRecorder) Write(p []byte) (int, error) {
	r.calls = append(r.calls, "disable")
	r.writes += string(p)
	return len(p), nil
}

func (r *shutdownCallRecorder) FlushInput() error {
	r.calls = append(r.calls, "flush")
	return nil
}

func (r *shutdownCallRecorder) Restore() error {
	r.calls = append(r.calls, "restore")
	return nil
}

type fakeSession struct {
	mu                       sync.Mutex
	notify                   func(any)
	subagentModels           map[string]ComposerFooter
	dispatched               []string
	shellCommands            []string
	dispatchedDisplay        []string
	dispatchedAttachments    []InputAttachment
	steered                  []string
	queuedFollowUps          []string
	sessionIDs               []string
	approvalRecoverySessions []string
	approvalSet              bool
	transcript               string
	transcriptTurns          []statepkg.Message
	sessionEvents            []event.RunEvent
	sessionEventsErr         error
	activeContextTurns       []statepkg.Message
	transcriptTurnsErr       error
	recent                   []SessionSummary
	permissions              string
	model                    string
	fastEnabled              bool
	skills                   string
	models                   []string
	skillToggleOpts          []skill.Entry
	memorySkillOpts          []MemorySkillOption
	promotedMemorySkills     []string
	promoteMemoryErr         error
	chooseFn                 func(turn.SlashChoice) SlashOutcome
	choices                  []turn.SlashChoice
	subagentOpts             []string
	currentReasoningEffort   string
	appliedModel             string
	reloadedConfig           int
	sandboxReply             string
	sandboxArgs              [][]string
	reloadConfigErr          error
	appliedReasoning         string
	appliedSkill             string
	appliedEnabledPaths      []string
	installedSkill           string
	skillActivationName      string
	appliedLoadMode          string
	skillActivationErr       error
	switchedPrimaryQuery     string
	statusReply              string
	mcpReply                 string
	diffReply                string
	streamSlashReply         map[string]SlashOutcome
	streamSlashFn            func(context.Context, string, string) (SlashOutcome, bool)
	permissionCalls          [][]string
	activePreset             string
	appliedPresets           []string
	applyPresetErr           error
	cancelActive             bool
	cancelCalls              int
	// mcpSkipResult is what SkipOptionalMCPStartup reports: true means the
	// optional servers were skipped, false means nothing was left to skip. The
	// count records how often Escape consulted it.
	mcpSkipResult        bool
	mcpSkipCalls         int
	cancelAllAgentsCalls int
	cancelSubagentQuery  SubagentControlQuery
	steerAccepted        bool
	queueAccepted        bool
	steerRetracted       bool
	retractCalls         int
	pendingSteerCount    int
	pendingSteerCountOK  bool
	cancelled            chan struct{}
	dispatchStarted      chan struct{}
	dispatchWait         <-chan struct{}
	dispatchCancelWait   <-chan struct{}
	dispatchStartOnce    sync.Once
	dispatchErr          error
	preferredSessionID   string
	memoryEnabled        bool
	memoryUse            bool
	memoryGenerate       bool
	memoryUpdates        []memorySettingsUpdate
	memoryResetCalls     int
	memoryResetAllCalls  []bool
	browseState          RendererBrowseState
	browseFound          bool
	savedBrowseSession   string
	savedBrowseState     RendererBrowseState
}

type recordingSelector struct {
	selects     []selectorCall
	selectSteps []selectorStep
	selectIdx   int
}

type selectorCall struct {
	label         string
	options       []string
	defaultOption string
}

func (s *recordingSelector) Select(label string, options []string, defaultOption string) (string, bool, error) {
	s.selects = append(s.selects, selectorCall{
		label:         label,
		options:       append([]string(nil), options...),
		defaultOption: defaultOption,
	})
	if s.selectIdx >= len(s.selectSteps) {
		return "", false, nil
	}
	step := s.selectSteps[s.selectIdx]
	s.selectIdx++
	return step.value, step.ok, step.err
}

func (s *recordingSelector) MultiSelect(string, []string, []string) ([]string, bool, error) {
	return nil, false, nil
}

func (s *recordingSelector) Input(string, string) (string, bool, error) { return "", false, nil }
func (s *recordingSelector) Confirm(string, bool) (bool, bool, error)   { return false, false, nil }

// SelectRich records a rich picker as the plain call it amounts to — its
// labels, with the row it opens on as the default — and answers by label.
func (s *recordingSelector) SelectRich(label string, items []SelectItem, defaultIdx int) (int, bool, error) {
	labels := make([]string, len(items))
	opensOn := ""
	for i, item := range items {
		labels[i] = item.Label
		if i == defaultIdx {
			opensOn = item.Label
		}
	}
	chosen, ok, err := s.Select(label, labels, opensOn)
	if err != nil || !ok {
		return -1, ok, err
	}
	for i, l := range labels {
		if l == chosen {
			return i, true, nil
		}
	}
	return -1, false, nil
}

func (s *recordingSelector) Secret(label string, defaultValue string) (string, bool, error) {
	return s.Input(label, defaultValue)
}

func (s *recordingSelector) Review(string, []turn.StatusFact, []string, int) (int, bool, error) {
	return -1, false, nil
}

func (f *fakeSession) DispatchSurfaceTurn(_ context.Context, submission turn.TurnSubmission) error {
	f.mu.Lock()
	sessionID := submission.SessionID
	text := submission.UserText
	f.dispatched = append(f.dispatched, text)
	f.dispatchedDisplay = append(f.dispatchedDisplay, submission.DisplayText)
	f.dispatchedAttachments = append([]InputAttachment(nil), submission.Attachments...)
	f.sessionIDs = append(f.sessionIDs, sessionID)
	notify := f.notify
	wait := f.dispatchWait
	dispatchErr := f.dispatchErr
	f.dispatchWait = nil
	f.mu.Unlock()
	if f.dispatchStarted != nil {
		f.dispatchStartOnce.Do(func() {
			close(f.dispatchStarted)
		})
	}
	if wait != nil {
		select {
		case <-wait:
		case <-f.cancelled:
			if f.dispatchCancelWait != nil {
				<-f.dispatchCancelWait
			}
			return context.Canceled
		}
	}
	if notify != nil {
		notify(RunStartedMsg{RunID: "r1"})
		if text == "/new" {
			notify(SessionSwitchedMsg{SessionID: "s2", Title: "session two"})
		}
		if text == "/permissions" {
			notify(PermissionManagementRequestedMsg{})
		}
		if text == "/skills" {
			notify(SkillSelectionRequestedMsg{})
		}
		if text == "/plan" {
			notify(SlashPickerRequestedMsg{Picker: &turn.Picker{Command: "model"}})
		}
		if dispatchErr != nil && !submission.CallerRendersReturnedError {
			notify(NewMessageMsg{Msg: Message{Kind: MsgKindError, Content: dispatchErr.Error()}})
		} else if dispatchErr == nil {
			notify(NewMessageMsg{Msg: Message{Kind: MsgKindAssistant, Content: "ok"}})
		}
		runEnded := RunEndedMsg{RunID: "r1"}
		if dispatchErr != nil {
			runEnded.WorkedDuration = time.Second
		}
		notify(runEnded)
	}
	return dispatchErr
}

func (f *fakeSession) RunSurfaceShellCommand(_ context.Context, sessionID string, channel string, rawInput string) error {
	f.mu.Lock()
	f.shellCommands = append(f.shellCommands, rawInput)
	f.sessionIDs = append(f.sessionIDs, sessionID)
	notify := f.notify
	f.mu.Unlock()
	_ = channel
	if notify != nil {
		notify(NewMessageMsg{Msg: Message{Kind: MsgKindTool, ToolName: "shell", Content: "ok", Summary: "ran " + strings.TrimSpace(strings.TrimPrefix(rawInput, "!"))}})
	}
	return nil
}

func (f *fakeSession) SurfaceTranscript(context.Context, string, int) (string, error) {
	return strings.TrimSpace(f.transcript), nil
}

func (f *fakeSession) SurfaceTranscriptMessages(context.Context, string) ([]statepkg.Message, error) {
	if f.transcriptTurnsErr != nil {
		return nil, f.transcriptTurnsErr
	}
	if len(f.transcriptTurns) > 0 {
		return append([]statepkg.Message(nil), f.transcriptTurns...), nil
	}
	text := strings.TrimSpace(f.transcript)
	if text == "" {
		return nil, nil
	}
	return []statepkg.Message{{Role: "system", Content: text}}, nil
}

// SurfaceComposerTokenStats computes the footer budget for the fake's model
// label the way ChatSession computes it for its runner's model.
func (f *fakeSession) SurfaceComposerTokenStats(usage int) ComposerTokenStats {
	provider, model := llm.ParseProviderModelLabel(f.model)
	if strings.TrimSpace(model) == "" {
		return ComposerTokenStats{}
	}
	limits, _ := llm.Lookup(provider, model)
	budget := statepkg.CalculateTokenBudgetWithOptions(usage, model, limits, statepkg.TokenBudgetOptions{})
	if budget.ContextWindow <= 0 {
		return ComposerTokenStats{}
	}
	return composerTokenStatsFromBudget(TokenBudgetUpdatedMsg{TokenUsage: budget.TokenUsage, PercentLeft: budget.PercentLeft, ContextWindow: budget.ContextWindow})
}

func (f *fakeSession) SurfaceActiveContextMessages(ctx context.Context, sessionID string) ([]statepkg.Message, error) {
	if f.activeContextTurns != nil {
		return append([]statepkg.Message(nil), f.activeContextTurns...), nil
	}
	return f.SurfaceTranscriptMessages(ctx, sessionID)
}

func (f *fakeSession) SurfaceSessionEvents(context.Context, string) ([]event.RunEvent, error) {
	if f.sessionEventsErr != nil {
		return nil, f.sessionEventsErr
	}
	return append([]event.RunEvent(nil), f.sessionEvents...), nil
}

func (f *fakeSession) SaveSurfaceBrowseState(_ context.Context, sessionID string, browse RendererBrowseState) error {
	f.savedBrowseSession = sessionID
	f.savedBrowseState = browse
	return nil
}

func (f *fakeSession) LoadSurfaceBrowseState(context.Context, string) (RendererBrowseState, bool, error) {
	return f.browseState, f.browseFound, nil
}

func (f *fakeSession) ResumeSession(_ context.Context, sessionID string) (string, string, string, error) {
	if strings.TrimSpace(sessionID) == "" {
		return "", "", "", nil
	}
	return strings.TrimSpace(sessionID), "resumed " + strings.TrimSpace(sessionID), "", nil
}

func (f *fakeSession) ActivateSessionModel(_ context.Context, _ string) (string, error) {
	return "", nil
}

func (f *fakeSession) SelectModel(_ context.Context, _ string, _ turn.ModelChoice, _ *string) error {
	return nil
}

func (f *fakeSession) ListSessionsRecent(_ context.Context, limit int) ([]SessionSummary, error) {
	if f.recent == nil {
		return nil, nil
	}
	// Mirror the real store contract: the requested limit bounds the page.
	if limit > 0 && len(f.recent) > limit {
		return append([]SessionSummary(nil), f.recent[:limit]...), nil
	}
	return append([]SessionSummary(nil), f.recent...), nil
}

func (f *fakeSession) ModelSummaryString() string {
	return f.model
}

func (f *fakeSession) SkillListString() string {
	return f.skills
}

func (f *fakeSession) AvailableSkillToggleOptions() []skill.Entry {
	return append([]skill.Entry(nil), f.skillToggleOpts...)
}

func (f *fakeSession) MemorySkillOptions() []MemorySkillOption {
	return append([]MemorySkillOption(nil), f.memorySkillOpts...)
}

func (f *fakeSession) PromoteMemorySkills(names []string) (string, error) {
	f.promotedMemorySkills = append([]string(nil), names...)
	if f.promoteMemoryErr != nil {
		return "", f.promoteMemoryErr
	}
	return "skills: promoted " + strings.Join(names, ", "), nil
}

func (f *fakeSession) HandleSandboxSlash(sessionID, channel string, args []string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sandboxArgs = append(f.sandboxArgs, args)
	if strings.TrimSpace(f.sandboxReply) == "" {
		return "", false
	}
	return f.sandboxReply, true
}

func (f *fakeSession) ReloadConfig() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reloadedConfig++
	return f.reloadConfigErr
}

func (f *fakeSession) CurrentModelOption() string {
	return strings.TrimSpace(f.model)
}

// subagentModels lets a test give a subagent type its own model, the way
// agents.definitions[<type>].llm_providers does in forebrain.yaml. A type absent
// from the map runs on the primary agent's model.
func (f *fakeSession) SubagentModelSummary(agentType string) (string, string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	own, ok := f.subagentModels[strings.TrimSpace(agentType)]
	if !ok {
		return "", "", false
	}
	return own.Model, own.ReasoningEffort, true
}

func (f *fakeSession) IsFastMode() bool {
	return f.fastEnabled
}

func (f *fakeSession) SetFastMode(enabled bool) error {
	f.fastEnabled = enabled
	return nil
}

func (f *fakeSession) CurrentModelReasoningEffort() string {
	return strings.TrimSpace(f.currentReasoningEffort)
}

func (f *fakeSession) ApplySkillSelection(label string) (string, error) {
	f.appliedSkill = label
	return "skill: " + label, nil
}

func (f *fakeSession) ApplySkillLoadMode(skillPath string, mode string) (string, error) {
	f.appliedLoadMode = strings.TrimSpace(skillPath) + ":" + strings.TrimSpace(mode)
	return "skills: load mode " + f.appliedLoadMode, nil
}

func (f *fakeSession) ApplySkillEnabledSelection(enabledPaths []string) (string, error) {
	f.appliedEnabledPaths = append([]string(nil), enabledPaths...)
	return "skills toggled: " + strings.Join(enabledPaths, ","), nil
}

func (f *fakeSession) InstallSkillPackageAsync(_ context.Context, sourceRef string, destScope string) {
	f.installedSkill = strings.TrimSpace(sourceRef) + ":" + strings.TrimSpace(destScope)
}

func (f *fakeSession) SkillSelectionByName(name string) (string, string, error) {
	f.skillActivationName = strings.TrimSpace(name)
	if f.skillActivationErr != nil {
		return "", "", f.skillActivationErr
	}
	return strings.TrimSpace(name), "/skills/" + strings.TrimSpace(name) + "/SKILL.md", nil
}

func (f *fakeSession) HandleStatusSlash(sessionID, channel string, side bool) (string, bool) {
	_, _, _ = sessionID, channel, side
	if strings.TrimSpace(f.statusReply) == "" {
		return "", false
	}
	return f.statusReply, true
}

func (f *fakeSession) PermissionPresets(sessionID string) ([]safety.ApprovalPreset, string) {
	_ = sessionID
	return safety.BuiltinApprovalPresets(), f.activePreset
}

func (f *fakeSession) ApplyPermissionPreset(sessionID, presetID string) (string, error) {
	_ = sessionID
	if f.applyPresetErr != nil {
		return "", f.applyPresetErr
	}
	f.appliedPresets = append(f.appliedPresets, presetID)
	f.activePreset = presetID
	return "permissions: applied " + presetID, nil
}

func (f *fakeSession) HandlePermissionsSlash(sessionID, channel string, args []string) (string, bool) {
	_, _, _ = sessionID, channel, args
	cp := append([]string(nil), args...)
	f.permissionCalls = append(f.permissionCalls, cp)
	if len(args) == 0 {
		if strings.TrimSpace(f.permissions) != "" {
			return f.permissions, true
		}
		return "Permissions\n\n- Mode: on-request", true
	}
	return "permissions handled: " + strings.Join(args, " "), true
}

func (f *fakeSession) HandleMCPSlash(sessionID, channel string) (string, bool) {
	_, _ = sessionID, channel
	if strings.TrimSpace(f.mcpReply) == "" {
		return "", false
	}
	return f.mcpReply, true
}

func (f *fakeSession) HandleDiffSlash(sessionID, channel string, args []string) (string, bool) {
	_, _, _ = sessionID, channel, args
	if strings.TrimSpace(f.diffReply) == "" {
		return "", false
	}
	return f.diffReply, true
}

func (f *fakeSession) ChooseSurfaceSlash(_ context.Context, _ string, choice turn.SlashChoice) SlashOutcome {
	f.choices = append(f.choices, choice)
	if f.chooseFn != nil {
		return f.chooseFn(choice)
	}
	return SlashOutcome{Handled: true}
}

func (f *fakeSession) ExecuteSurfaceSlash(ctx context.Context, sessionID string, line string) (SlashOutcome, bool) {
	if f.streamSlashFn != nil {
		return f.streamSlashFn(ctx, sessionID, line)
	}
	_, _ = sessionID, line
	if f.streamSlashReply == nil {
		return SlashOutcome{}, false
	}
	out, ok := f.streamSlashReply[strings.TrimSpace(line)]
	return out, ok
}

func (f *fakeSession) PreferredSurfaceTranscriptSessionID(context.Context) string {
	if strings.TrimSpace(f.preferredSessionID) != "" {
		return strings.TrimSpace(f.preferredSessionID)
	}
	return "s1"
}
func (f *fakeSession) NewSessionID(string) string { return "s1" }

// Approval display records are the real session's job against its store; the
// fake only has to show the sink it can take one.
func (f *fakeSession) RecordApprovalGate(turn.ToolApprovalRequest) {}

func (f *fakeSession) RecordApprovalDecision(turn.ToolApprovalRequest, turn.ToolApprovalDecision, string) {
}

func (f *fakeSession) PrependUINotify(fn func(any)) {
	f.mu.Lock()
	f.notify = fn
	f.mu.Unlock()
}

func (f *fakeSession) StartApprovalRecovery(sessionID string) {
	f.mu.Lock()
	f.approvalRecoverySessions = append(f.approvalRecoverySessions, strings.TrimSpace(sessionID))
	f.mu.Unlock()
}

func (f *fakeSession) SetToolApprovalSink(turn.ToolApprovalDecisionSink) {
	f.mu.Lock()
	f.approvalSet = true
	f.mu.Unlock()
}

// SkipOptionalMCPStartup is the interactive half of the MCP ready barrier: it
// reports whether it skipped anything the run can do without.
func (f *fakeSession) SkipOptionalMCPStartup() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mcpSkipCalls++
	return f.mcpSkipResult
}

func (f *fakeSession) CancelActiveRun() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelCalls++
	if f.cancelled != nil {
		select {
		case <-f.cancelled:
		default:
			close(f.cancelled)
		}
	}
	return f.cancelActive
}

func (f *fakeSession) AgentRosterSnapshot(sessionID string) AgentRosterSnapshot {
	_ = sessionID
	return AgentRosterSnapshot{Rows: []AgentRosterRow{}}
}

func (f *fakeSession) CancelSubagent(query SubagentControlQuery) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelSubagentQuery = query
	return true
}

func (f *fakeSession) CancelAllAgents() AgentCancelSummary {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelAllAgentsCalls++
	return AgentCancelSummary{Main: 1, Subagents: 1}
}

func (f *fakeSession) SteerSurfaceRun(sessionID string, channel string, parts []llm.ContentPart) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.steered = append(f.steered, llm.TextContent(parts...))
	f.sessionIDs = append(f.sessionIDs, sessionID)
	_, _ = channel, parts
	return f.steerAccepted
}

func (f *fakeSession) QueueSurfaceFollowUp(sessionID string, channel string, parts []llm.ContentPart) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queuedFollowUps = append(f.queuedFollowUps, llm.TextContent(parts...))
	f.sessionIDs = append(f.sessionIDs, sessionID)
	_, _ = channel, parts
	return f.queueAccepted
}

func (f *fakeSession) RetractSurfaceSteer(sessionID string, channel string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, _ = sessionID, channel
	f.retractCalls++
	return f.steerRetracted
}

func (f *fakeSession) SurfacePendingSteerCount(sessionID string, channel string) (int, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, _ = sessionID, channel
	return f.pendingSteerCount, f.pendingSteerCountOK
}

type memorySettingsUpdate struct {
	sessionID string
	feature   *bool
	use       *bool
	generate  *bool
}

func (f *fakeSession) MemorySettings() (bool, bool, bool) {
	return f.memoryEnabled, f.memoryUse, f.memoryGenerate
}

func (f *fakeSession) UpdateMemorySettings(sessionID string, featureEnabled, useMemories, generateMemories *bool) error {
	f.memoryUpdates = append(f.memoryUpdates, memorySettingsUpdate{sessionID: sessionID, feature: featureEnabled, use: useMemories, generate: generateMemories})
	if featureEnabled != nil {
		f.memoryEnabled = *featureEnabled
	}
	if useMemories != nil {
		f.memoryUse = *useMemories
	}
	if generateMemories != nil {
		f.memoryGenerate = *generateMemories
	}
	return nil
}

func (f *fakeSession) ResetMemories(_ context.Context, all bool) error {
	f.memoryResetCalls++
	f.memoryResetAllCalls = append(f.memoryResetAllCalls, all)
	return nil
}

// The reset submenu's default row is "Go back": walking away from it (or
// picking the wrong-labeled row) must never call ResetMemories, and picking
// either of the two real reset choices must call it with the right scope.
func TestHandleMemoriesSettingsAndResetAreSeparateActions(t *testing.T) {
	session := &fakeSession{memoryEnabled: true, memoryUse: true, memoryGenerate: true}
	selector := &recordingSelector{selectSteps: []selectorStep{
		{value: "Reset memories — Clear local memory files and summaries. Threads remain intact.", ok: true},
		{value: "Go back", ok: true},
		{value: "Save memory settings", ok: false},
	}}
	var out bytes.Buffer
	ctrl := newCommandController(session, NewRenderer(&out, &out), selector, nil, "")
	if !ctrl.handleMemories("thread-1") {
		t.Fatal("expected memories command handled")
	}
	if session.memoryResetCalls != 0 {
		t.Fatalf("go back should not reset memories: %d", session.memoryResetCalls)
	}
	if len(session.memoryUpdates) != 0 {
		t.Fatalf("reset action should not update settings: %#v", session.memoryUpdates)
	}
	if len(selector.selects) != 3 ||
		selector.selects[1].options[0] != "Reset this project's memories" ||
		selector.selects[1].options[1] != "Reset everything (every project + global)" ||
		selector.selects[1].options[2] != "Go back" {
		t.Fatalf("unexpected selector flow: %#v", selector.selects)
	}

	session = &fakeSession{memoryEnabled: true, memoryUse: true, memoryGenerate: true}
	selector = &recordingSelector{selectSteps: []selectorStep{
		{value: "Reset memories — Clear local memory files and summaries. Threads remain intact.", ok: true},
		{value: "Reset this project's memories", ok: true},
	}}
	ctrl = newCommandController(session, NewRenderer(&out, &out), selector, nil, "")
	_ = ctrl.handleMemories("thread-1")
	if session.memoryResetCalls != 1 || len(session.memoryUpdates) != 0 {
		t.Fatalf("reset should only invoke reset operation: resets=%d updates=%#v", session.memoryResetCalls, session.memoryUpdates)
	}
	if len(session.memoryResetAllCalls) != 1 || session.memoryResetAllCalls[0] {
		t.Fatalf("project reset should pass all=false: %#v", session.memoryResetAllCalls)
	}

	session = &fakeSession{memoryEnabled: true, memoryUse: true, memoryGenerate: true}
	selector = &recordingSelector{selectSteps: []selectorStep{
		{value: "Reset memories — Clear local memory files and summaries. Threads remain intact.", ok: true},
		{value: "Reset everything (every project + global)", ok: true},
	}}
	ctrl = newCommandController(session, NewRenderer(&out, &out), selector, nil, "")
	_ = ctrl.handleMemories("thread-1")
	if session.memoryResetCalls != 1 || len(session.memoryUpdates) != 0 {
		t.Fatalf("reset should only invoke reset operation: resets=%d updates=%#v", session.memoryResetCalls, session.memoryUpdates)
	}
	if len(session.memoryResetAllCalls) != 1 || !session.memoryResetAllCalls[0] {
		t.Fatalf("reset-everything should pass all=true: %#v", session.memoryResetAllCalls)
	}
}

func TestHandleMemoriesSettingActionUpdatesOnlySelectedSetting(t *testing.T) {
	session := &fakeSession{memoryEnabled: true, memoryUse: false, memoryGenerate: true}
	selector := &recordingSelector{selectSteps: []selectorStep{
		{value: "Use memories — Use memories in following threads. Applied at next thread.", ok: true},
		{value: "Save memory settings", ok: true},
	}}
	var out bytes.Buffer
	ctrl := newCommandController(session, NewRenderer(&out, &out), selector, nil, "")
	_ = ctrl.handleMemories("thread-1")
	if len(session.memoryUpdates) != 1 || session.memoryUpdates[0].use == nil || session.memoryUpdates[0].generate == nil {
		t.Fatalf("selected use action changed wrong settings: %#v", session.memoryUpdates)
	}
	if !*session.memoryUpdates[0].use || !*session.memoryUpdates[0].generate {
		t.Fatal("expected staged settings to be saved together")
	}
}

func TestHandleIdleHotkeyPasteImageSeedsPlaceholderDraft(t *testing.T) {
	drainInteractiveInputSeedQueue()
	t.Cleanup(drainInteractiveInputSeedQueue)

	session := &fakeSession{}
	state := &streamState{sessionID: "s1"}
	var out bytes.Buffer
	renderer := NewRenderer(&out, &out)
	handled := handleIdleHotkey(context.Background(), session, renderer, state, ClipboardImageFunc(func(context.Context) (InputAttachment, error) {
		return InputAttachment{Path: "/tmp/forebrain-clipboard-test.png", MIMEType: "image/png"}, nil
	}), nil, hotkeyPasteImage)
	if !handled {
		t.Fatal("expected paste-image hotkey handled")
	}
	if state.composer.DraftText != "[Image #1]" {
		t.Fatalf("expected placeholder in draft, got %q", state.composer.DraftText)
	}
	if state.composer.Cursor != len([]rune("[Image #1]")) {
		t.Fatalf("expected cursor after placeholder, got %d", state.composer.Cursor)
	}
	if strings.Contains(out.String(), "image attached") {
		t.Fatalf(" image paste should not render a transient status frame, got %q", out.String())
	}
	select {
	case seed := <-interactiveInputSeedCh:
		if seed.text != "[Image #1]" {
			t.Fatalf("expected raw input seed after placeholder, got %q", seed.text)
		}
	default:
		t.Fatal("expected raw input seed for image placeholder")
	}
}

func TestHandleIdleHotkeyPasteImageIgnoresTextClipboardSilently(t *testing.T) {
	drainInteractiveInputSeedQueue()
	t.Cleanup(drainInteractiveInputSeedQueue)

	session := &fakeSession{}
	state := &streamState{sessionID: "s1"}
	var out bytes.Buffer
	renderer := NewRenderer(&out, &out)
	handled := handleIdleHotkey(context.Background(), session, renderer, state, ClipboardImageFunc(func(context.Context) (InputAttachment, error) {
		return InputAttachment{}, errClipboardNoImage
	}), nil, hotkeyPasteImage)
	if !handled {
		t.Fatal("expected paste-image hotkey handled")
	}
	if out.String() != "" {
		t.Fatalf("expected no output for a text clipboard, got %q", out.String())
	}
	if state.composer.DraftText != "" || len(state.composer.Attachments) != 0 {
		t.Fatalf("expected composer untouched, got draft=%q attachments=%d", state.composer.DraftText, len(state.composer.Attachments))
	}
}

func TestClipboardImageToolExitStatusMeansNoImage(t *testing.T) {
	err := runClipboardImageTool(exec.Command("false"))
	if !errors.Is(err, errClipboardNoImage) {
		t.Fatalf("expected a non-zero exit to mean no image, got %v", err)
	}
	err = runClipboardImageTool(exec.Command("forebrain-clipboard-tool-that-does-not-exist"))
	if err == nil || errors.Is(err, errClipboardNoImage) {
		t.Fatalf("expected a missing helper to stay a real error, got %v", err)
	}
}

func TestBuildComposerTextKeepsAttachmentsInline(t *testing.T) {
	got := buildComposerText("hello", []PendingPaste{{Placeholder: "[Pasted Content 12 chars]"}}, []InputAttachment{{Path: "/tmp/a.png"}})
	if got != "hello [Pasted Content 12 chars] [Image #1]" {
		t.Fatalf("expected inline composer text, got %q", got)
	}
}

func TestBuildComposerTextDoesNotDuplicateInlineImagePlaceholder(t *testing.T) {
	got := buildComposerText("[Image #1]ssss", nil, []InputAttachment{{Path: "/tmp/a.png"}})
	if got != "[Image #1]ssss" {
		t.Fatalf("expected existing image placeholder kept once, got %q", got)
	}
}

func TestBuildComposerDisplayTextColorsImagePlaceholder(t *testing.T) {
	got := buildComposerDisplayText("hello", nil, []InputAttachment{{Path: "/tmp/a.png"}})
	if !strings.Contains(got, "\x1b[") {
		t.Fatalf("expected ANSI styling in composer display text, got %q", got)
	}
	if strings.Contains(got, "[1m") || strings.Contains(got, ";1m") {
		t.Fatalf("did not expect bold image placeholder styling, got %q", got)
	}
	if !strings.Contains(stripANSI(got), "hello [Image #1]") {
		t.Fatalf("expected readable composer display text, got %q", got)
	}
}

func TestBuildComposerDisplayTextRestoresComposerBackgroundAfterImagePlaceholder(t *testing.T) {
	got := buildComposerDisplayText("[Image #1]这张图片", nil, []InputAttachment{{Path: "/tmp/a.png"}})
	want := sharedBlockTextStyle + "这张图片"
	if !strings.Contains(got, want) {
		t.Fatalf("expected composer text style restored after image placeholder, got %q", got)
	}
	if !strings.Contains(stripANSI(got), "[Image #1]这张图片") {
		t.Fatalf("expected readable display text, got %q", got)
	}
}

func TestStreamStateAppendPasteFoldsLargeContentAndExpansionRestoresBody(t *testing.T) {
	var state streamState
	pasted := strings.Repeat("a", largePasteCharThreshold+5)
	state.appendPaste(pasted)
	if len(state.composer.PendingPastes) != 1 {
		t.Fatalf("expected one folded paste, got %#v", state.composer.PendingPastes)
	}
	display := state.composerDisplay()
	if !strings.Contains(display, "[Pasted Content ") {
		t.Fatalf("expected pasted placeholder, got %q", display)
	}
	actual := expandComposerText(buildComposerText("", state.composer.PendingPastes, nil), state.composer.PendingPastes)
	if actual != pasted {
		t.Fatalf("expected placeholder expansion to restore pasted body, got %q", actual)
	}
}

func TestHistoryRecallSubmissionRestoresImageAndLargePaste(t *testing.T) {
	imagePath := filepath.Join(t.TempDir(), "clip.png")
	if err := os.WriteFile(imagePath, []byte("\x89PNG\r\n\x1a\nimage-data"), 0o600); err != nil {
		t.Fatalf("write image: %v", err)
	}
	placeholder := "[Pasted Content 1200 chars]"
	pasted := strings.Repeat("payload", 200)
	line := "inspect [Image #1] " + placeholder
	attachment := InputAttachment{Path: imagePath, MIMEType: "image/png", Label: "clip.png"}
	paste := PendingPaste{ID: "paste-1", Content: pasted, Placeholder: placeholder}
	state := &streamState{}
	ev := inputEvent{
		kind:              inputEventDraft,
		draft:             line,
		cursor:            len([]rune(line)),
		historyNavigation: true,
		historyIndex:      0,
		historyEntry: rawInputHistoryEntry{
			Text: line, Attachments: []InputAttachment{attachment}, PendingPastes: []PendingPaste{paste},
		},
	}
	state.prepareHistoryDraft(ev, line)
	state.syncPendingPastesWithDraft(line)
	state.syncAttachmentsWithDraft(line)
	state.composer.DraftText = line
	state.composer.Cursor = ev.cursor
	state.composer.Text = line

	submission, ok := state.composerSubmission()
	if !ok {
		t.Fatal("expected recalled composer submission")
	}
	if strings.Contains(submission.Text, placeholder) || !strings.Contains(submission.Text, pasted) {
		t.Fatalf("submission did not restore pasted content: %q", submission.Text)
	}
	if strings.Contains(submission.Text, "[Image #1]") {
		t.Fatalf("image placeholder leaked into model text: %q", submission.Text)
	}
	if len(submission.Parts) != 2 || submission.Parts[1].Type != llm.ContentTypeImageBase64 || submission.Parts[1].MIMEType != "image/png" {
		t.Fatalf("recalled image was not restored as an image part: %#v", submission.Parts)
	}
}

func TestHistoryBrowseDownRestoresOriginalDraftMetadata(t *testing.T) {
	originalAttachment := InputAttachment{Path: "/tmp/original.png", MIMEType: "image/png"}
	originalPaste := PendingPaste{Content: "original", Placeholder: "[Pasted Content 8 chars]"}
	state := &streamState{composer: ComposerState{
		DraftText:     "draft [Image #1] [Pasted Content 8 chars]",
		Attachments:   []InputAttachment{originalAttachment},
		PendingPastes: []PendingPaste{originalPaste},
	}}
	historyLine := "old [Image #1]"
	state.prepareHistoryDraft(inputEvent{
		historyNavigation: true,
		historyIndex:      0,
		historyEntry: rawInputHistoryEntry{
			Text: historyLine, Attachments: []InputAttachment{{Path: "/tmp/old.png", MIMEType: "image/png"}},
		},
	}, historyLine)
	state.composer.DraftText = historyLine
	state.prepareHistoryDraft(inputEvent{historyNavigation: true, historyIndex: -1}, "draft [Image #1] [Pasted Content 8 chars]")
	if len(state.composer.Attachments) != 1 || state.composer.Attachments[0] != originalAttachment ||
		len(state.composer.PendingPastes) != 1 || state.composer.PendingPastes[0] != originalPaste {
		t.Fatalf("original draft metadata was not restored: %+v", state.composer)
	}
}

func TestStreamStateAppendPasteMatchesLargePasteThreshold(t *testing.T) {
	var state streamState
	exact := strings.Repeat("x", largePasteCharThreshold)
	state.appendPaste(exact)
	if len(state.composer.PendingPastes) != 0 {
		t.Fatalf("expected exact-threshold paste to stay inline, got %#v", state.composer.PendingPastes)
	}
	if state.composer.Text != exact {
		t.Fatalf("expected exact-threshold paste in composer text, got %q", state.composer.Text)
	}

	var largeState streamState
	large := strings.Repeat("y", largePasteCharThreshold+1)
	largeState.appendPaste(large)
	if len(largeState.composer.PendingPastes) != 1 {
		t.Fatalf("expected over-threshold paste to fold, got %#v", largeState.composer.PendingPastes)
	}
	if largeState.composer.PendingPastes[0].Placeholder != "[Pasted Content 1001 chars]" {
		t.Fatalf("unexpected folded placeholder: %q", largeState.composer.PendingPastes[0].Placeholder)
	}
}

func TestStreamStateAppendPasteNumbersDuplicatePlaceholders(t *testing.T) {
	var state streamState
	first := strings.Repeat("a", largePasteCharThreshold+4)
	second := strings.Repeat("b", largePasteCharThreshold+4)
	third := strings.Repeat("c", largePasteCharThreshold+4)

	state.appendPaste(first)
	state.appendPaste(second)
	state.appendPaste(third)

	if len(state.composer.PendingPastes) != 3 {
		t.Fatalf("expected three folded pastes, got %#v", state.composer.PendingPastes)
	}
	want := []string{
		"[Pasted Content 1004 chars]",
		"[Pasted Content 1004 chars] #2",
		"[Pasted Content 1004 chars] #3",
	}
	for i, placeholder := range want {
		if state.composer.PendingPastes[i].Placeholder != placeholder {
			t.Fatalf("placeholder[%d]=%q want=%q", i, state.composer.PendingPastes[i].Placeholder, placeholder)
		}
	}
}

func TestStreamStateAppendPasteAppendsToExistingDraft(t *testing.T) {
	var state streamState
	state.composer.DraftText = "Merge .worktrees/"
	state.composer.Cursor = len([]rune(state.composer.DraftText))

	state.appendPaste("Merge .worktrees/wt-20260523-1 into main\n")

	want := "Merge .worktrees/Merge .worktrees/wt-20260523-1 into main\n"
	if state.composer.DraftText != want {
		t.Fatalf("draft=%q want=%q", state.composer.DraftText, want)
	}
	if state.composer.Text != want {
		t.Fatalf("text=%q want=%q", state.composer.Text, want)
	}
}

func TestStreamStateAppendPastePreservesMultilineContentAtCursor(t *testing.T) {
	var state streamState
	state.composer.DraftText = "before after"
	state.composer.Cursor = len([]rune("before "))

	state.appendPaste("line1\r\nline2\rline3\n\n")

	want := "before line1\nline2\nline3\n\nafter"
	if state.composer.DraftText != want {
		t.Fatalf("draft=%q want=%q", state.composer.DraftText, want)
	}
	if state.composer.Text != want {
		t.Fatalf("text=%q want=%q", state.composer.Text, want)
	}
	wantCursor := len([]rune("before line1\nline2\nline3\n\n"))
	if state.composer.Cursor != wantCursor {
		t.Fatalf("cursor=%d want=%d", state.composer.Cursor, wantCursor)
	}
}

func TestStreamStateComposerSubmissionPreservesMultilinePaste(t *testing.T) {
	var state streamState
	state.composer.DraftText = "prefix "
	state.composer.Cursor = len([]rune(state.composer.DraftText))
	state.appendPaste("line1\nline2\n")

	submission, ok := state.composerSubmission()
	if !ok {
		t.Fatal("expected composer submission")
	}
	want := "prefix line1\nline2\n"
	if submission.Text != want {
		t.Fatalf("submission text=%q want=%q", submission.Text, want)
	}
	if submission.DisplayText != want {
		t.Fatalf("submission display text=%q want=%q", submission.DisplayText, want)
	}
	if got := llm.TextContent(submission.Parts...); got != want {
		t.Fatalf("submission parts text=%q want=%q", got, want)
	}
}

func TestStreamStateAppendPasteRepeatsIdenticalSmallPaste(t *testing.T) {
	var state streamState
	state.appendPaste("again")
	state.appendPaste("again")

	want := "againagain"
	if state.composer.DraftText != want {
		t.Fatalf("draft=%q want=%q", state.composer.DraftText, want)
	}
	if state.composer.Text != want {
		t.Fatalf("text=%q want=%q", state.composer.Text, want)
	}
}

func TestStreamStateAppendPasteAppendsWhenNextPasteExtendsPreviousPaste(t *testing.T) {
	var state streamState
	state.appendPaste("abc")
	state.appendPaste("abcdef")

	want := "abcabcdef"
	if state.composer.DraftText != want {
		t.Fatalf("draft=%q want=%q", state.composer.DraftText, want)
	}
	if state.composer.Text != want {
		t.Fatalf("text=%q want=%q", state.composer.Text, want)
	}
}

func TestStreamStateAppendPastePreservesUserTypedSpace(t *testing.T) {
	var state streamState
	state.appendPaste("abc")
	state.composer.Cursor = len([]rune(state.composer.DraftText))
	state.composer.DraftText = state.composer.DraftText + " "
	state.composer.Cursor = len([]rune(state.composer.DraftText))
	state.appendPaste("def")

	want := "abc def"
	if state.composer.DraftText != want {
		t.Fatalf("draft=%q want=%q", state.composer.DraftText, want)
	}
	if state.composer.Text != want {
		t.Fatalf("text=%q want=%q", state.composer.Text, want)
	}
}

func TestStreamStateSyncPendingPastesWithDraftRemovesDeletedPlaceholderAfterSlashCommand(t *testing.T) {
	var state streamState
	state.composer.DraftText = "/review note"
	state.composer.Cursor = len([]rune(state.composer.DraftText))
	state.appendPaste(strings.Repeat("x", largePasteCharThreshold+5))
	if len(state.composer.PendingPastes) != 1 {
		t.Fatalf("expected one folded paste, got %#v", state.composer.PendingPastes)
	}
	withPlaceholder := state.composerDisplay()
	if !strings.Contains(withPlaceholder, "[Pasted Content ") {
		t.Fatalf("expected placeholder in composer display, got %q", withPlaceholder)
	}

	// User deletes the placeholder from the draft (e.g. via the slash flow):
	// the next draft event carries text without the placeholder.
	state.composer.DraftText = "/review note"
	state.syncPendingPastesWithDraft("/review note")
	if len(state.composer.PendingPastes) != 0 {
		t.Fatalf("expected deleted placeholder to be removed, got %#v", state.composer.PendingPastes)
	}
	if got := state.composerDisplay(); got != "/review note" {
		t.Fatalf("expected plain slash command text after deleting paste, got %q", got)
	}
}

func TestStreamStateAppendPasteFoldInsertsPlaceholderIntoDraftAtCursor(t *testing.T) {
	var state streamState
	state.composer.DraftText = "before after"
	state.composer.Cursor = len([]rune("before "))

	pasted := strings.Repeat("z", largePasteCharThreshold+7)
	state.appendPaste(pasted)

	if len(state.composer.PendingPastes) != 1 {
		t.Fatalf("expected one folded paste, got %#v", state.composer.PendingPastes)
	}
	placeholder := state.composer.PendingPastes[0].Placeholder
	want := "before " + placeholder + "after"
	if state.composer.DraftText != want {
		t.Fatalf("draft=%q want=%q", state.composer.DraftText, want)
	}
	wantCursor := len([]rune("before " + placeholder))
	if state.composer.Cursor != wantCursor {
		t.Fatalf("cursor=%d want=%d", state.composer.Cursor, wantCursor)
	}

	// Draft survives a sync round-trip because the placeholder is in the draft.
	state.syncPendingPastesWithDraft(state.composer.DraftText)
	if len(state.composer.PendingPastes) != 1 {
		t.Fatalf("expected pending paste to survive draft sync, got %#v", state.composer.PendingPastes)
	}

	// Submission expands the inline placeholder back to the pasted body.
	state.composer.Text = state.composer.DraftText
	expanded := expandComposerText(buildComposerText(state.composer.Text, state.composer.PendingPastes, nil), state.composer.PendingPastes)
	wantExpanded := "before " + pasted + "after"
	if expanded != wantExpanded {
		t.Fatalf("expanded=%q want=%q", expanded, wantExpanded)
	}
}

func TestBuildComposerTextDoesNotDuplicateInlinePastePlaceholder(t *testing.T) {
	pastes := []PendingPaste{{Content: strings.Repeat("a", 1200), Placeholder: "[Pasted Content 1200 chars]"}}
	got := buildComposerText("ask about [Pasted Content 1200 chars] please", pastes, nil)
	if got != "ask about [Pasted Content 1200 chars] please" {
		t.Fatalf("expected inline paste placeholder kept once, got %q", got)
	}
}

func TestBuildComposerDisplayTextColorsPastePlaceholder(t *testing.T) {
	pastes := []PendingPaste{{Content: strings.Repeat("a", 1200), Placeholder: "[Pasted Content 1200 chars]"}}
	got := buildComposerDisplayText("ask [Pasted Content 1200 chars] now", pastes, nil)
	if !strings.Contains(got, "\x1b[") {
		t.Fatalf("expected ANSI styling for paste placeholder, got %q", got)
	}
	if !strings.Contains(stripANSI(got), "ask [Pasted Content 1200 chars] now") {
		t.Fatalf("expected readable display text, got %q", got)
	}
}

func TestExpandComposerTextExpandsSuffixedPlaceholderBeforeBase(t *testing.T) {
	pastes := []PendingPaste{
		{Content: "FIRST", Placeholder: "[Pasted Content 1004 chars]"},
		{Content: "SECOND", Placeholder: "[Pasted Content 1004 chars] #2"},
	}
	text := "[Pasted Content 1004 chars] and [Pasted Content 1004 chars] #2"
	got := expandComposerText(text, pastes)
	if got != "FIRST and SECOND" {
		t.Fatalf("expanded=%q want=%q", got, "FIRST and SECOND")
	}
}

func TestActiveRunInputCtrlCProgressionClearThenCancelThenExit(t *testing.T) {
	session := &fakeSession{cancelActive: true, steerAccepted: true, cancelled: make(chan struct{})}
	state := &streamState{sessionID: "s1"}
	var out bytes.Buffer
	renderer := NewRenderer(&out, &out)

	handleActiveRunInput(context.Background(), session, renderer, nil, state, ClipboardImageFunc(func(context.Context) (InputAttachment, error) {
		return InputAttachment{Path: "/tmp/clip.png", MIMEType: "image/png"}, nil
	}), inputEvent{kind: inputEventHotkey, hotkey: hotkeyPasteImage}, nil)
	state.composer.Text = "next turn"
	state.composer.DraftText = "next turn"
	handleActiveRunInput(context.Background(), session, renderer, nil, state, nil, inputEvent{kind: inputEventLine, line: "next turn"}, nil)
	state.composer.Text = "queue later"
	state.composer.DraftText = "queue later"
	handleActiveRunInput(context.Background(), session, renderer, nil, state, nil, inputEvent{kind: inputEventHotkey, hotkey: hotkeyQueueFollowUp}, nil)
	state.composer.Text = "draft after queue"
	state.composer.DraftText = "draft after queue"
	handleActiveRunInput(context.Background(), session, renderer, nil, state, nil, inputEvent{kind: inputEventHotkey, hotkey: hotkeyClearInput}, nil)
	if state.composer.Text != "" || state.composer.DraftText != "" {
		t.Fatalf("expected first ctrl+c to clear composer, got text=%q draft=%q", state.composer.Text, state.composer.DraftText)
	}
	if !state.isActiveRunCtrlCArmed() {
		t.Fatal("expected first ctrl+c to arm active-run cancel")
	}
	session.mu.Lock()
	cancelCalls := session.cancelCalls
	steered := append([]string(nil), session.steered...)
	session.mu.Unlock()
	if cancelCalls != 0 {
		t.Fatalf("expected no cancel call on first ctrl+c, got %d", cancelCalls)
	}

	handleActiveRunInput(context.Background(), session, renderer, nil, state, nil, inputEvent{kind: inputEventHotkey, hotkey: hotkeyInterrupt}, nil)
	if !state.isActiveRunCtrlCExitArmed() {
		t.Fatal("expected second ctrl+c to arm active-run exit")
	}
	session.mu.Lock()
	cancelCalls = session.cancelCalls
	session.mu.Unlock()
	if cancelCalls != 1 {
		t.Fatalf("expected one cancel call on second ctrl+c, got %d", cancelCalls)
	}

	handleActiveRunInput(context.Background(), session, renderer, nil, state, nil, inputEvent{kind: inputEventHotkey, hotkey: hotkeyInterrupt}, nil)
	if !state.quitRequested {
		t.Fatal("expected third ctrl+c to request quit")
	}

	if len(state.pendingSteers) != 1 || state.pendingSteers[0].Text != "next turn" {
		t.Fatalf("expected pending steer, got %#v", state.pendingSteers)
	}
	if len(state.pendingSteers[0].Attachments) != 1 || state.pendingSteers[0].Attachments[0].Path != "/tmp/clip.png" {
		t.Fatalf("expected steer attachment preserved, got %#v", state.pendingSteers[0].Attachments)
	}
	if len(state.queuedTurns) != 1 || state.queuedTurns[0].Submission.Text != "queue later" {
		t.Fatalf("expected queued follow-up, got %#v", state.queuedTurns)
	}
	if len(steered) != 1 || steered[0] != "next turn" {
		t.Fatalf("expected steer request recorded, got %#v", steered)
	}
}

// Withdrawal recovers everything the user still owns as one editable draft, in
// the order they wrote it: the withdrawn message, then work queued behind it,
// then whatever is still uncommitted in the composer.
func TestRestoreWithdrawnWorkMergesInWrittenOrder(t *testing.T) {
	session := &fakeSession{}
	foreground := &foregroundTurn{submission: mustComposerSubmission("the withdrawn message")}
	state := &streamState{sessionID: "s1", activeForeground: foreground}
	foreground.withdraw()
	state.enqueueTurn(mustComposerSubmission("queued behind it"), queuedSubmissionActionTurn)
	state.composer.DraftText = "still typing"
	state.composer.Cursor = len("still typing")

	state.restoreWithdrawnWork(session)

	want := "the withdrawn message\nqueued behind it\nstill typing"
	if state.composer.DraftText != want {
		t.Fatalf("recovered draft = %q, want %q", state.composer.DraftText, want)
	}
	if state.composer.Text != "" {
		t.Fatalf("recovered draft is committed and can auto-submit: %q", state.composer.Text)
	}
	if len(state.pendingSteers)+len(state.rejectedSteers)+len(state.queuedTurns) != 0 {
		t.Fatal("recovered work was left in the queues as well")
	}
	if state.submitPendingSteersAfterInterrupt {
		t.Fatal("withdrawal armed interrupt-and-send")
	}
}

// Esc restores the composer immediately, but the run is still draining and the
// user can type into it. The second pass at the cleanup barrier must fold that
// in without dropping or reordering what the first pass already recovered.
func TestRestoreWithdrawnWorkFoldsInputQueuedWhileCancelling(t *testing.T) {
	session := &fakeSession{}
	foreground := &foregroundTurn{submission: mustComposerSubmission("the withdrawn message")}
	state := &streamState{sessionID: "s1", activeForeground: foreground}
	foreground.withdraw()

	state.restoreWithdrawnWork(session)
	if state.composer.DraftText != "the withdrawn message" {
		t.Fatalf("first pass = %q", state.composer.DraftText)
	}
	// A second pass with nothing new must not duplicate the recovered draft.
	state.restoreWithdrawnWork(session)
	if state.composer.DraftText != "the withdrawn message" {
		t.Fatalf("second pass duplicated the draft: %q", state.composer.DraftText)
	}

	state.enqueueTurn(mustComposerSubmission("typed while cancelling"), queuedSubmissionActionTurn)
	state.restoreWithdrawnWork(session)

	want := "the withdrawn message\ntyped while cancelling"
	if state.composer.DraftText != want {
		t.Fatalf("barrier pass = %q, want %q", state.composer.DraftText, want)
	}
}

// The merge is the settled recovery shape, so it has to hold for every queue a
// withdrawn turn can have work sitting in — a pending steer, a steer the run
// refused, and an ordinary queued follow-up — and it has to bring the real
// payloads back, not the placeholder text standing in for them. Image and
// folded-paste placeholders are per-submission names, so merging has to renumber
// both or the first one wins and the rest resolve to the wrong content.
func TestRestoreWithdrawnWorkMergesEveryQueueAndKeepsPayloads(t *testing.T) {
	session := &fakeSession{steerRetracted: true}
	withdrawn := pastedComposerSubmission("the withdrawn message", "paste body alpha")
	foreground := &foregroundTurn{submission: withdrawn}
	state := &streamState{sessionID: "s1", activeForeground: foreground}
	foreground.withdraw()

	state.enqueuePendingSteer(pastedComposerSubmission("steered follow-up", "paste body bravo"))
	state.enqueueRejectedSteer(pastedComposerSubmission("refused steer", "paste body delta"))
	state.enqueueTurn(pastedComposerSubmission("queued follow-up", "paste body gamma"), queuedSubmissionActionTurn)

	state.restoreWithdrawnWork(session)

	for _, want := range []string{"the withdrawn message", "steered follow-up", "refused steer", "queued follow-up"} {
		if !strings.Contains(state.composer.DraftText, want) {
			t.Fatalf("draft %q lost %q", state.composer.DraftText, want)
		}
	}
	if i, j := strings.Index(state.composer.DraftText, "the withdrawn message"), strings.Index(state.composer.DraftText, "queued follow-up"); i > j {
		t.Fatalf("written order not preserved: %q", state.composer.DraftText)
	}
	if session.retractCalls != 1 {
		t.Fatalf("pending steer retractions = %d, want 1", session.retractCalls)
	}
	if len(state.pendingSteers)+len(state.rejectedSteers)+len(state.queuedTurns) != 0 {
		t.Fatal("work was recovered into the draft and left in the queues as well")
	}

	// All four bodies are the same length, so they all fold behind the same base
	// placeholder and the merge must rename them apart. Every paste must survive
	// under its own name, and every name in the draft must resolve to exactly one.
	if len(state.composer.PendingPastes) != 4 {
		t.Fatalf("recovered pastes = %d, want 4: %+v", len(state.composer.PendingPastes), state.composer.PendingPastes)
	}
	seen := map[string]string{}
	for _, paste := range state.composer.PendingPastes {
		if prev, dup := seen[paste.Placeholder]; dup {
			t.Fatalf("placeholder %q names both %q and %q", paste.Placeholder, prev, paste.Content)
		}
		seen[paste.Placeholder] = paste.Content
		if !strings.Contains(state.composer.DraftText, paste.Placeholder) {
			t.Fatalf("draft %q does not reference recovered paste %q", state.composer.DraftText, paste.Placeholder)
		}
	}
	for _, body := range []string{"paste body alpha", "paste body bravo", "paste body delta", "paste body gamma"} {
		found := false
		for _, paste := range state.composer.PendingPastes {
			found = found || paste.Content == body
		}
		if !found {
			t.Fatalf("paste content %q was lost, only placeholders survived: %+v", body, state.composer.PendingPastes)
		}
	}
}

// Images are the other per-submission placeholder. Recovering a withdrawn
// message that carried one, merged with queued work that carried another, has to
// renumber them against the merged attachment list — otherwise both draft
// placeholders resolve to the first image.
func TestRestoreWithdrawnWorkRenumbersImagesAcrossTheMerge(t *testing.T) {
	dir := t.TempDir()
	imageA := filepath.Join(dir, "a.png")
	imageB := filepath.Join(dir, "b.png")
	for _, path := range []string{imageA, imageB} {
		if err := os.WriteFile(path, []byte("\x89PNG\r\n\x1a\nimage-data"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	withImage := func(text, path string) ComposerSubmission {
		submission, ok := buildComposerSubmission(text, text+" "+imageAttachmentPlaceholder(0),
			[]InputAttachment{{Path: path, MIMEType: "image/png"}})
		if !ok {
			t.Fatalf("empty submission for %q", text)
		}
		return submission
	}

	session := &fakeSession{}
	foreground := &foregroundTurn{submission: withImage("look at this", imageA)}
	state := &streamState{sessionID: "s1", activeForeground: foreground}
	foreground.withdraw()
	state.enqueueTurn(withImage("and this one", imageB), queuedSubmissionActionTurn)

	state.restoreWithdrawnWork(session)

	if len(state.composer.Attachments) != 2 {
		t.Fatalf("recovered attachments = %d, want 2: %+v", len(state.composer.Attachments), state.composer.Attachments)
	}
	if state.composer.Attachments[0].Path != imageA || state.composer.Attachments[1].Path != imageB {
		t.Fatalf("attachments merged out of order: %+v", state.composer.Attachments)
	}
	draft := state.composer.DraftText
	for i := range state.composer.Attachments {
		if !strings.Contains(draft, imageAttachmentPlaceholder(i)) {
			t.Fatalf("draft %q is missing placeholder %s; both images collapsed onto one", draft, imageAttachmentPlaceholder(i))
		}
	}
}

// A queued follow-up that the previous turn replayed automatically is a turn in
// its own right and is withdrawable on the same terms — and a withdrawn turn
// must never hand the queue back to the automatic replay path, which would
// resend the very message the user just took back.
func TestWithdrawnTurnStopsAutomaticReplay(t *testing.T) {
	state := &streamState{sessionID: "s1"}
	state.enqueueTurn(mustComposerSubmission("queued follow-up"), queuedSubmissionActionTurn)

	if _, ok := nextAutomaticSubmission(state, runTurnWithdrawn); ok {
		t.Fatal("a withdrawn turn fed its queue back into automatic replay")
	}
	// The queue is not discarded either: an ordinary completion still replays it.
	next, ok := nextAutomaticSubmission(state, runTurnCompleted)
	if !ok || next.Text != "queued follow-up" {
		t.Fatalf("ordinary completion lost the queue: ok=%v next=%q", ok, next.Text)
	}
}

// pastedComposerSubmission builds a submission whose display text folds a large
// paste behind a placeholder, the shape the merge has to renumber.
func pastedComposerSubmission(text string, pasteBody string) ComposerSubmission {
	placeholder := nextLargePastePlaceholderForCount(len([]rune(pasteBody)), nil)
	display := text + " " + placeholder
	submission, ok := buildComposerSubmission(text+" "+pasteBody, display, nil)
	if !ok {
		panic("empty submission: " + text)
	}
	submission.PendingPastes = []PendingPaste{{ID: text, Content: pasteBody, Placeholder: placeholder}}
	return submission
}

func mustComposerSubmission(text string) ComposerSubmission {
	submission, ok := buildComposerSubmission(text, text, nil)
	if !ok {
		panic("empty submission: " + text)
	}
	return submission
}

func TestActiveRunInputQueuesWhenSteerUnavailable(t *testing.T) {
	session := &fakeSession{}
	state := &streamState{sessionID: "s1"}
	var out bytes.Buffer
	renderer := NewRenderer(&out, &out)

	handleActiveRunInput(context.Background(), session, renderer, nil, state, nil, inputEvent{kind: inputEventDraft, draft: "queue this"}, nil)
	if state.composer.DraftText != "queue this" {
		t.Fatalf("expected live draft visible, got %q", state.composer.DraftText)
	}

	handleActiveRunInput(context.Background(), session, renderer, nil, state, nil, inputEvent{kind: inputEventLine, line: "queue this"}, nil)
	if len(state.pendingSteers) != 0 {
		t.Fatalf("expected no pending steer when session rejects steer, got %#v", state.pendingSteers)
	}
	if len(state.rejectedSteers) != 1 || state.rejectedSteers[0].Submission.Text != "queue this" {
		t.Fatalf("expected rejected steer queue, got %#v", state.rejectedSteers)
	}
	if state.composer.Text != "" || state.composer.DraftText != "" {
		t.Fatalf("expected composer cleared after queueing, got text=%q draft=%q", state.composer.Text, state.composer.DraftText)
	}
}

func TestActiveRunEditLastQueuedMessageRestoresLastQueuedFollowUp(t *testing.T) {
	session := &fakeSession{}
	state := &streamState{
		sessionID: "s1",
		queuedTurns: []queuedSubmission{
			{
				Action: queuedSubmissionActionRejectedSteer,
				Submission: ComposerSubmission{
					Text:        "retry at end",
					Parts:       []llm.ContentPart{llm.Text("retry at end")},
					DisplayText: "retry at end",
				},
			},
			{
				Action: queuedSubmissionActionTurn,
				Submission: ComposerSubmission{
					Text:        "first follow-up",
					Parts:       []llm.ContentPart{llm.Text("first follow-up")},
					DisplayText: "first follow-up",
				},
			},
			{
				Action: queuedSubmissionActionTurn,
				Submission: ComposerSubmission{
					Text:        "second follow-up",
					Parts:       []llm.ContentPart{llm.Text("second follow-up")},
					DisplayText: "second follow-up",
				},
			},
		},
	}
	var out bytes.Buffer
	renderer := NewRenderer(&out, &out)

	handleActiveRunInput(context.Background(), session, renderer, nil, state, nil, inputEvent{kind: inputEventHotkey, hotkey: hotkeyEditLastQueued}, nil)

	if state.composer.DraftText != "second follow-up" {
		t.Fatalf("expected last queued follow-up restored to composer, got %q", state.composer.DraftText)
	}
	if len(state.queuedTurns) != 2 {
		t.Fatalf("expected only last queued follow-up removed, got %#v", state.queuedTurns)
	}
	if state.queuedTurns[0].Action != queuedSubmissionActionRejectedSteer || state.queuedTurns[1].Submission.Text != "first follow-up" {
		t.Fatalf("expected rejected steer and earlier follow-up preserved, got %#v", state.queuedTurns)
	}
}

func TestActiveRunEditLastQueuedMessageQueuesEditedDraft(t *testing.T) {
	session := &fakeSession{}
	state := &streamState{
		sessionID: "s1",
		queuedTurns: []queuedSubmission{
			{
				Action: queuedSubmissionActionTurn,
				Submission: ComposerSubmission{
					Text:        "original follow-up",
					Parts:       []llm.ContentPart{llm.Text("original follow-up")},
					DisplayText: "original follow-up",
				},
			},
		},
	}
	var out bytes.Buffer
	renderer := NewRenderer(&out, &out)

	handleActiveRunInput(context.Background(), session, renderer, nil, state, nil, inputEvent{kind: inputEventHotkey, hotkey: hotkeyEditLastQueued}, nil)
	handleActiveRunInput(context.Background(), session, renderer, nil, state, nil, inputEvent{kind: inputEventDraft, draft: "edited follow-up", cursor: len("edited follow-up")}, nil)
	handleActiveRunInput(context.Background(), session, renderer, nil, state, nil, inputEvent{kind: inputEventHotkey, hotkey: hotkeyQueueFollowUp}, nil)

	if len(state.queuedTurns) != 1 {
		t.Fatalf("expected edited follow-up requeued once, got %#v", state.queuedTurns)
	}
	if got := state.queuedTurns[0].Submission.Text; got != "edited follow-up" {
		t.Fatalf("expected edited draft to replace original queued text, got %q", got)
	}
	if got := state.queuedTurns[0].Submission.DisplayText; got != "edited follow-up" {
		t.Fatalf("expected edited draft to replace original display text, got %q", got)
	}
}

func TestActiveRunEditLastQueuedMessageLeavesPendingSteerUntouched(t *testing.T) {
	session := &fakeSession{}
	state := &streamState{
		sessionID: "s1",
		pendingSteers: []ComposerSubmission{
			{Text: "auto fix all bugs and issues", Parts: []llm.ContentPart{llm.Text("auto fix all bugs and issues")}, DisplayText: "auto fix all bugs and issues"},
			{Text: "auto fix all bugs and issue", Parts: []llm.ContentPart{llm.Text("auto fix all bugs and issue")}, DisplayText: "auto fix all bugs and issue"},
		},
	}
	var out bytes.Buffer
	renderer := NewRenderer(&out, &out)

	handleActiveRunInput(context.Background(), session, renderer, nil, state, nil, inputEvent{kind: inputEventHotkey, hotkey: hotkeyEditLastQueued}, nil)

	if state.composer.DraftText != "" {
		t.Fatalf("pending steer must not be recalled, got %q", state.composer.DraftText)
	}
	if len(state.pendingSteers) != 2 {
		t.Fatalf("pending steers changed: %#v", state.pendingSteers)
	}
}

func TestActiveRunEditLastQueuedMessageKeepsSteerWhenAlreadyDelivered(t *testing.T) {
	session := &fakeSession{}
	state := &streamState{
		sessionID: "s1",
		pendingSteers: []ComposerSubmission{
			{Text: "already delivered", Parts: []llm.ContentPart{llm.Text("already delivered")}, DisplayText: "already delivered"},
		},
		queuedTurns: []queuedSubmission{
			{Action: queuedSubmissionActionTurn, Submission: ComposerSubmission{Text: "follow up", Parts: []llm.ContentPart{llm.Text("follow up")}, DisplayText: "follow up"}},
		},
	}
	var out bytes.Buffer
	renderer := NewRenderer(&out, &out)

	handleActiveRunInput(context.Background(), session, renderer, nil, state, nil, inputEvent{kind: inputEventHotkey, hotkey: hotkeyEditLastQueued}, nil)

	// Retract failed (the agent already drained the steer), so the steer stays
	// put and editing falls back to the locally-held follow-up.
	if state.composer.DraftText != "follow up" {
		t.Fatalf("expected fallback to follow-up when steer not retractable, got %q", state.composer.DraftText)
	}
	if len(state.pendingSteers) != 1 {
		t.Fatalf("expected pending steer preserved when not retractable, got %#v", state.pendingSteers)
	}
	if len(state.queuedTurns) != 0 {
		t.Fatalf("expected follow-up consumed, got %#v", state.queuedTurns)
	}
}

func TestActiveRunSlashContinueInputSteersCurrentTurn(t *testing.T) {
	session := &fakeSession{
		steerAccepted: true,
		streamSlashReply: map[string]SlashOutcome{
			"/review": {
				Handled:           true,
				ShouldContinueRun: true,
				ContinueInput:     "review generated prompt",
			},
		},
	}
	state := &streamState{sessionID: "s1"}
	var out bytes.Buffer
	renderer := NewRenderer(&out, &out)
	cmds := newCommandController(session, renderer, &stubSelector{}, nil, "")

	handleActiveRunInput(context.Background(), session, renderer, nil, state, nil, inputEvent{kind: inputEventLine, line: "/review"}, cmds)

	session.mu.Lock()
	steered := append([]string(nil), session.steered...)
	session.mu.Unlock()
	if len(steered) != 1 || steered[0] != "review generated prompt" {
		t.Fatalf("expected active-run slash continue input to steer current turn, got %#v", steered)
	}
	if len(state.pendingSteers) != 1 || state.pendingSteers[0].Text != "review generated prompt" {
		t.Fatalf("expected pending steer recorded, got %#v", state.pendingSteers)
	}
	if len(state.pendingSteers) != 1 || state.pendingSteers[0].DisplayText != "/review" {
		t.Fatalf("expected pending steer display text to preserve raw slash command, got %#v", state.pendingSteers)
	}
	if len(state.queuedTurns) != 0 {
		t.Fatalf("expected no queued turns for steerable slash continue input, got %#v", state.queuedTurns)
	}
}

// TestDispatchStreamSlashInitSubmitsBuiltPrompt locks the /init gate fix: an
// inject-prompt command reports Handled:false with ShouldContinueRun:true, and
// the dispatcher must feed the built prompt to the model (submitting it) rather
// than dropping it as unhandled and sending the raw "/init" text to the LLM.
func TestDispatchStreamSlashInitSubmitsBuiltPrompt(t *testing.T) {
	session := &fakeSession{
		streamSlashReply: map[string]SlashOutcome{
			"/init": {
				Handled:           false,
				ShouldContinueRun: true,
				ContinueInput:     "Analyze this codebase and create a FOREBRAIN.md guidance file",
			},
		},
	}
	state := &streamState{sessionID: "s1"}
	var out bytes.Buffer
	renderer := NewRenderer(&out, &out)
	tracker := NewTracker()
	cmds := newCommandController(session, renderer, &stubSelector{}, nil, "")

	handled, continueRun, sub, _ := dispatchStreamSlashCommand(context.Background(), cmds, renderer, tracker, state, "/init")

	if !handled {
		t.Fatalf("expected /init to be handled, got handled=false")
	}
	if !continueRun {
		t.Fatalf("expected /init to continue the run with the built prompt")
	}
	if sub.Text != "Analyze this codebase and create a FOREBRAIN.md guidance file" {
		t.Fatalf("expected built init prompt submitted to model, got %q", sub.Text)
	}
	if sub.DisplayText != "/init" {
		t.Fatalf("expected raw /init preserved as display text, got %q", sub.DisplayText)
	}
}

func TestActiveRunBangShellExecutesImmediately(t *testing.T) {
	session := &fakeSession{}
	state := &streamState{sessionID: "s1"}
	var out bytes.Buffer
	renderer := NewRenderer(&out, &out)

	handleActiveRunInput(context.Background(), session, renderer, nil, state, nil, inputEvent{kind: inputEventLine, line: "!echo hi"}, nil)

	deadline := time.Now().Add(time.Second)
	for {
		session.mu.Lock()
		got := append([]string(nil), session.shellCommands...)
		session.mu.Unlock()
		if len(got) == 1 && got[0] == "!echo hi" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected immediate shell execution, got %#v", got)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(state.queuedTurns) != 0 {
		t.Fatalf("expected no queued shell turn for enter bang command, got %#v", state.queuedTurns)
	}
}

func TestActiveRunTabQueuesBangShellAsShellAction(t *testing.T) {
	session := &fakeSession{}
	state := &streamState{sessionID: "s1"}
	var out bytes.Buffer
	renderer := NewRenderer(&out, &out)

	state.composer.Text = "!echo hi"
	state.composer.DraftText = "!echo hi"
	handleActiveRunInput(context.Background(), session, renderer, nil, state, nil, inputEvent{kind: inputEventHotkey, hotkey: hotkeyQueueFollowUp}, nil)

	if len(state.queuedTurns) != 1 {
		t.Fatalf("expected one queued item, got %#v", state.queuedTurns)
	}
	if state.queuedTurns[0].Action != queuedSubmissionActionShell {
		t.Fatalf("expected queued shell action, got %#v", state.queuedTurns[0])
	}
	if state.queuedTurns[0].Submission.Text != "!echo hi" {
		t.Fatalf("expected queued shell text preserved, got %#v", state.queuedTurns[0])
	}
}

func TestNextAutomaticSubmissionDrainsQueuedShellBeforeQueuedTurn(t *testing.T) {
	state := &streamState{
		queuedTurns: []queuedSubmission{
			{Action: queuedSubmissionActionShell, Submission: ComposerSubmission{Text: "!echo hi", DisplayText: "!echo hi"}},
			{Action: queuedSubmissionActionTurn, Submission: ComposerSubmission{Text: "after shell", Parts: []llm.ContentPart{llm.Text("after shell")}, DisplayText: "after shell"}},
		},
	}
	first, ok := nextAutomaticSubmission(state, runTurnCompleted)
	if !ok {
		t.Fatal("expected first queued submission")
	}
	if first.Text != "!echo hi" {
		t.Fatalf("expected queued shell first, got %#v", first)
	}
	second, ok := nextAutomaticSubmission(state, runTurnCompleted)
	if !ok {
		t.Fatal("expected second queued submission")
	}
	if second.Text != "after shell" {
		t.Fatalf("expected queued turn after shell, got %#v", second)
	}
}

func TestActiveRunSlashNativePickerDoesNotQueueTurn(t *testing.T) {
	session := &fakeSession{streamSlashReply: map[string]SlashOutcome{
		"/model": {Handled: true, Picker: &turn.Picker{Command: "model", Title: "Model", Items: []turn.PickerItem{{Value: "openai / gpt-4.1 - GPT 4.1", Label: "openai / gpt-4.1 - GPT 4.1", Current: true}}}},
	}}
	state := &streamState{sessionID: "s1"}
	var out bytes.Buffer
	renderer := NewRenderer(&out, &out)
	cmds := newCommandController(session, renderer, &sequenceSelector{
		steps: []selectorStep{{value: "openai / gpt-4.1 - GPT 4.1", ok: true}},
	}, nil, "")

	handleActiveRunInput(context.Background(), session, renderer, nil, state, nil, inputEvent{kind: inputEventLine, line: "/model"}, cmds)

	if len(session.choices) != 1 || session.choices[0].Command != "model" {
		t.Fatalf("expected /model during active run to use native picker, choices=%+v", session.choices)
	}
	if len(state.pendingSteers) != 0 {
		t.Fatalf("expected no pending steers for native /model picker, got %#v", state.pendingSteers)
	}
	if len(state.queuedTurns) != 0 {
		t.Fatalf("expected no queued turns for native /model picker, got %#v", state.queuedTurns)
	}
}

func TestReadRawInputEventsRecognizesCtrlCAsClearWhenComposerHasContent(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe error: %v", err)
	}
	defer r.Close()
	events := readRawInputEvents(context.Background(), r, io.Discard, stubRawInputController{}, nil)
	if _, err := w.Write([]byte("hello")); err != nil {
		t.Fatalf("write content: %v", err)
	}
	if _, err := w.Write([]byte{0x03}); err != nil {
		t.Fatalf("write ctrl+c: %v", err)
	}
	ev := nextNonDraftEvent(t, events)
	if ev.kind != inputEventHotkey || ev.hotkey != hotkeyClearInput {
		t.Fatalf("expected ctrl+c with pending input to clear composer, got %#v", ev)
	}
	_ = w.Close()
}

func TestStreamStateResumeInputReadIfNeededInvokesOnce(t *testing.T) {
	calls := 0
	state := &streamState{
		resumeRawRead: func() { calls++ },
	}
	state.resumeInputReadIfNeeded()
	state.resumeInputReadIfNeeded()
	if calls != 1 {
		t.Fatalf("expected resume callback once, got %d", calls)
	}
}

func TestStreamStateClearComposerClearsInputAndAttachments(t *testing.T) {
	state := &streamState{
		composer: ComposerState{
			Text: "hello",
			Attachments: []InputAttachment{
				{Path: "/tmp/a.png", MIMEType: "image/png"},
			},
		},
	}
	if !state.clearComposer() {
		t.Fatal("expected clearComposer to report cleared state")
	}
	if state.composer.Text != "" {
		t.Fatalf("expected pending input cleared, got %q", state.composer.Text)
	}
	if len(state.composer.Attachments) != 0 {
		t.Fatalf("expected attachments cleared, got %#v", state.composer.Attachments)
	}
}

func TestStreamStateBeginResumableInputPauseResumesPreviousReader(t *testing.T) {
	oldCalls := 0
	newCalls := 0
	state := &streamState{
		resumeRawRead: func() { oldCalls++ },
	}
	state.beginResumableInputPause(func() { newCalls++ })
	if oldCalls != 1 {
		t.Fatalf("expected previous reader resumed once, got %d", oldCalls)
	}
	if newCalls != 0 {
		t.Fatalf("expected new reader deferred, got %d", newCalls)
	}
	state.resumeInputReadIfNeeded()
	if newCalls != 1 {
		t.Fatalf("expected deferred reader resumed once, got %d", newCalls)
	}
}

func TestHandleIdleHotkeyInterruptClearsComposerBeforeExit(t *testing.T) {
	session := &fakeSession{}
	state := &streamState{
		sessionID: "s1",
		composer: ComposerState{
			Text: "draft",
			Attachments: []InputAttachment{
				{Path: "/tmp/a.png", MIMEType: "image/png"},
			},
		},
	}
	var out bytes.Buffer
	renderer := NewRenderer(&out, &out)
	handled := handleIdleHotkey(context.Background(), session, renderer, state, nil, nil, hotkeyInterrupt)
	if !handled {
		t.Fatal("expected ctrl+c handled")
	}
	if state.quitRequested {
		t.Fatal("expected composer clear before exit")
	}
	if state.composer.Text != "" || len(state.composer.Attachments) != 0 {
		t.Fatalf("expected composer cleared, got input=%q attachments=%#v", state.composer.Text, state.composer.Attachments)
	}
}

func TestHandleIdleHotkeyInterruptQuitsWithAlreadyFinishingController(t *testing.T) {
	session := sessionEnv{}.session()
	ctl := session.tuiController()
	ctl.Track("run-1", "s1", func() {})
	if !ctl.Cancel("run-1", context.Canceled) {
		t.Fatal("expected initial controller cancellation")
	}

	state := &streamState{sessionID: "s1"}
	var out bytes.Buffer
	renderer := NewRenderer(&out, &out)
	if !handleIdleHotkey(context.Background(), session, renderer, state, nil, nil, hotkeyInterrupt) {
		t.Fatal("expected ctrl+c handled")
	}
	if !state.quitRequested {
		t.Fatal("empty idle composer should request quit when no new cancellation work starts")
	}
	if strings.Contains(out.String(), "cancelling run") {
		t.Fatalf("stale controller must not render another cancelling status: %q", out.String())
	}
}

func TestWorkingStatusFormatterIncludesSpinnerElapsedAndEscapeInterruptHint(t *testing.T) {
	t.Setenv("TMUX", "")
	formatter := workingStatusFormatter(nil, nil)
	got := formatter(time.Second, 0, "")
	if got != "| Working (1s • esc to interrupt)" {
		t.Fatalf("unexpected working status title under 2s: %q", got)
	}
	got = formatter(4*time.Minute+30*time.Second, 0, "")
	if got != "| Working (4m 30s • esc to interrupt)" {
		t.Fatalf("unexpected working status title at 4m30s: %q", got)
	}
	got = formatter(time.Minute+time.Second, 3, "")
	if got != "\\ Working (1m 01s • esc to interrupt)" {
		t.Fatalf("expected spinner and zero-padded seconds, got %q", got)
	}
}

func TestWorkingStatusFormatterDoesNotAdvertiseBackgroundInTmux(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-1000/default,12345,0")
	formatter := workingStatusFormatter(nil, nil)
	got := formatter(3*time.Second, 0, "")
	if got != "| Working (3s • esc to interrupt)" {
		t.Fatalf("unexpected background hint inside tmux: %q", got)
	}
}

// The counters the line paints are the plan checklist alone; token and tool
// figures are tracked but never shown, and the line re-reads the checklist on
// every tick.
func TestWorkingStatusFormatterAppendsLatestCounters(t *testing.T) {
	counters := Counters{Tools: 1, InputTokens: 1500, OutputTokens: 300, Agents: 1, PlanDone: 1, PlanTotal: 3, PlanActive: "Writing tests"}
	formatter := workingStatusFormatter(func() Counters {
		return counters
	}, nil)
	got := formatter(20*time.Second, 2, "")
	if got != "- Working (20s • esc to interrupt) · ☑1/3 · Writing tests" {
		t.Fatalf("unexpected working status counters: %q", got)
	}
	counters = Counters{Tools: 3, InputTokens: 2000, OutputTokens: 1000, PlanDone: 2, PlanTotal: 3, PlanActive: "Reviewing the diff"}
	got = formatter(21*time.Second, 3, "")
	if got != "\\ Working (21s • esc to interrupt) · ☑2/3 · Reviewing the diff" {
		t.Fatalf("expected formatter to read latest counters each tick, got %q", got)
	}
}

// Inside a subagent's view the working line describes that subagent. The two
// runs agree about nothing — the conversation's turn started earlier and has
// spent more — so borrowing its figures, as the line used to, told the reader
// about a transcript they were not looking at. Escape returns to the
// conversation from that view rather than interrupting, so the hint goes too.
func TestWorkingStatusFormatterReportsTheSubagentWhoseViewIsOpen(t *testing.T) {
	const agentID = "subagent-7"
	tracker := NewTracker()
	tracker.StartRun("run-1")
	tracker.ObserveToolStep("", "call-0", "read_file", "")
	tracker.ObserveUsageDelta("run-1", 150000, 1000)
	tracker.ObserveAgent(agentID, time.Now().Add(-57*time.Second))
	tracker.ObserveToolStep(agentID, "call-1", "shell", "")
	tracker.ObserveSubagentUsage("run-1", agentID, 5000, 300)

	formatter := workingStatusFormatter(tracker.SnapshotActiveRun, tracker.SnapshotAgentRun)

	if got := formatter(75*time.Second, 0, ""); got != "| Working (1m 15s • esc to interrupt)" {
		t.Fatalf("conversation working line = %q", got)
	}
	if got := formatter(75*time.Second, 0, agentID); got != "| Working (57s)" {
		t.Fatalf("subagent working line = %q, want this subagent's own clock and spend", got)
	}

	// A finished subagent's view is closed by its own "Worked for" line; a
	// working line that kept counting beside it would contradict it.
	tracker.ObserveAgentEnded(agentID, time.Now())
	if got := formatter(75*time.Second, 0, agentID); got != "" {
		t.Fatalf("finished subagent still shows a working line: %q", got)
	}
	// An agent the tracker never saw start has nothing to report, and must not
	// fall back to the conversation's figures.
	if got := formatter(75*time.Second, 0, "subagent-unknown"); got != "" {
		t.Fatalf("unknown view = %q, want no line", got)
	}
}

// A subagent's clock is frozen at the moment its run ended, so its view reports
// how long it worked rather than how long ago it was read.
func TestTrackerFreezesASubagentsClockWhenItEnds(t *testing.T) {
	const agentID = "subagent-7"
	started := time.Now().Add(-10 * time.Minute)
	tracker := NewTracker()
	tracker.ObserveAgent(agentID, started)
	tracker.ObserveAgentEnded(agentID, started.Add(4*time.Minute+12*time.Second))

	snap, ok := tracker.SnapshotAgentRun(agentID)
	if !ok || !snap.Ended || snap.Elapsed != 4*time.Minute+12*time.Second {
		t.Fatalf("snapshot = %+v ok=%t, want a finished 4m12s run", snap, ok)
	}
	if _, ok := tracker.SnapshotAgentRun("subagent-never-spawned"); ok {
		t.Fatal("an agent that never started must report nothing")
	}
}

func TestPostTurnCompletionNotificationUsesBELOnlyWhenWaitingUnfocused(t *testing.T) {
	assistantSubmission := ComposerSubmission{Parts: []llm.ContentPart{llm.Text("hello")}}
	shellSubmission := ComposerSubmission{Parts: []llm.ContentPart{llm.Text("!pwd")}}
	tests := []struct {
		name        string
		disposition runTurnDisposition
		focused     bool
		submission  ComposerSubmission
		wantPosted  bool
	}{
		{name: "completed and unfocused", disposition: runTurnCompleted, submission: assistantSubmission, wantPosted: true},
		{name: "focused", disposition: runTurnCompleted, focused: true, submission: assistantSubmission},
		{name: "interrupted", disposition: runTurnInterrupted, submission: assistantSubmission},
		{name: "cancelled", disposition: runTurnCancelled, submission: assistantSubmission},
		{name: "local shell", disposition: runTurnCompleted, submission: shellSubmission},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			posted := postTurnCompletionNotification(&out, tc.disposition, tc.focused, tc.submission)
			if posted != tc.wantPosted {
				t.Fatalf("posted = %v, want %v", posted, tc.wantPosted)
			}
			want := ""
			if tc.wantPosted {
				want = "\x07"
			}
			if got := out.String(); got != want {
				t.Fatalf("notification output = %q, want %q", got, want)
			}
		})
	}
}

func TestStreamStateTracksTerminalFocusReports(t *testing.T) {
	state := streamState{terminalFocused: true}
	if !state.updateTerminalFocus(inputEvent{kind: inputEventFocusLost}) || state.terminalFocused {
		t.Fatal("focus-lost report was not applied")
	}
	if !state.updateTerminalFocus(inputEvent{kind: inputEventFocusGained}) || !state.terminalFocused {
		t.Fatal("focus-gained report was not applied")
	}
	if state.updateTerminalFocus(inputEvent{kind: inputEventDraft}) {
		t.Fatal("non-focus input was consumed as a focus report")
	}
}

// A turn that ends in an error is still closed by its worked line: the error
// says what went wrong, the worked line says where the turn stopped.
func TestRunTurnRendersReturnedErrorWithWorkedStatus(t *testing.T) {
	runErr := errors.New("provider rejected max_tokens")
	session := &fakeSession{dispatchErr: runErr}
	notifyCh := make(chan any, 3)
	session.notify = func(m any) { notifyCh <- m }

	tracker := NewTracker()
	var reducer Reducer
	reducer.WithTracker(tracker)
	var out bytes.Buffer
	renderer := NewRenderer(&out, &out)
	processNotify := func(m any) {
		ev := reducer.Reduce(m)
		for _, frame := range ev.Frames {
			renderer.RenderFrame(frame)
		}
		if ev.WorkedStatus != "" {
			renderer.RenderFrame(Frame{Kind: FrameStatus, Title: ev.WorkedStatus, Final: true})
		}
	}

	disposition, err := runTurn(
		context.Background(),
		make(chan os.Signal),
		make(chan inputEvent),
		notifyCh,
		processNotify,
		session,
		renderer,
		tracker,
		&streamState{sessionID: "s1"},
		"hello",
		"hello",
		"",
		nil,
		"",
		nil,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("runTurn returned error: %v", err)
	}
	if disposition != runTurnCompleted {
		t.Fatalf("runTurn disposition = %q, want %q", disposition, runTurnCompleted)
	}
	var errorFrames, workedFrames int
	for _, block := range renderer.vm.blocks {
		frame := block.frame
		if frame.Content == runErr.Error() {
			errorFrames++
		}
		if strings.HasPrefix(frame.Title, "Worked for 1s") {
			workedFrames++
		}
	}
	if errorFrames != 1 {
		t.Fatalf("error rendered %d times, want once: %#v", errorFrames, renderer.vm.blocks)
	}
	if workedFrames != 1 {
		t.Fatalf("failed turn rendered %d worked statuses, want one: %#v", workedFrames, renderer.vm.blocks)
	}
}

func TestRunTurnRendersReturnedErrorWithoutNotification(t *testing.T) {
	runErr := errors.New("user prompt hook failed")
	session := &fakeSession{dispatchErr: runErr}
	var out bytes.Buffer
	renderer := NewRenderer(&out, &out)

	disposition, err := runTurn(
		context.Background(),
		make(chan os.Signal),
		make(chan inputEvent),
		nil,
		nil,
		session,
		renderer,
		nil,
		&streamState{sessionID: "s1"},
		"hello",
		"hello",
		"",
		nil,
		"",
		nil,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("runTurn returned error: %v", err)
	}
	if disposition != runTurnCompleted {
		t.Fatalf("runTurn disposition = %q, want %q", disposition, runTurnCompleted)
	}
	var errorFrames int
	for _, block := range renderer.vm.blocks {
		frame := block.frame
		if frame.Title == "error" && frame.Content == runErr.Error() {
			errorFrames++
		}
	}
	if errorFrames != 1 {
		t.Fatalf("unnotified error rendered %d times, want once: %#v", errorFrames, renderer.vm.blocks)
	}
}

// Esc while steers are pending means "interrupt and send immediately" (the hint
// rendered above the pending-steer preview). At the interrupted boundary,
// pending steers are merged and submitted as one fresh turn rather than being
// restored into the composer.
func TestRunTurnEscapeSubmitsPendingSteersImmediatelyWhenCancelSwallowed(t *testing.T) {
	dispatchStarted := make(chan struct{})
	dispatchWait := make(chan struct{})
	// No `cancelled` channel: DispatchSurfaceTurn returns nil after the wait,
	// reproducing production where the cancellation error is swallowed.
	session := &fakeSession{
		cancelActive:    true,
		dispatchStarted: dispatchStarted,
		dispatchWait:    dispatchWait,
	}
	state := &streamState{
		sessionID: "s1",

		pendingSteers: []ComposerSubmission{
			{Text: "auto fix all bugs", Parts: []llm.ContentPart{llm.Text("auto fix all bugs")}, DisplayText: "auto fix all bugs"},
		},
	}
	var out bytes.Buffer
	renderer := NewRenderer(&out, &out)
	events := make(chan inputEvent, 1)
	sigCh := make(chan os.Signal, 1)
	done := make(chan runTurnDisposition, 1)

	go func() {
		disposition, _ := runTurn(context.Background(), sigCh, events, nil, nil, session, renderer, nil, state, "hello", "hello", "", nil, "", nil, nil, nil)
		done <- disposition
	}()
	<-dispatchStarted
	events <- inputEvent{kind: inputEventHotkey, hotkey: hotkeyEscapeInterrupt}
	// Let the dispatch return its swallowed (nil) result only after esc has been
	// handled, so the interrupt-resubmit flag is set first.
	waitForCancelCalls(t, session, 1)
	close(dispatchWait)

	if got := <-done; got != runTurnInterrupted {
		t.Fatalf("expected runTurnInterrupted so queued steers resubmit, got %q", got)
	}
	next, ok := nextAutomaticSubmission(state, runTurnInterrupted)
	if !ok {
		t.Fatalf("expected pending steer submitted immediately after esc, got ok=false state=%#v", state)
	}
	if got := strings.TrimSpace(next.Text); got != "auto fix all bugs" {
		t.Fatalf("expected pending steer submitted as the next turn, got %q", got)
	}
	if len(state.pendingSteers) != 0 {
		t.Fatalf("expected pendingSteers drained after immediate submit, got %#v", state.pendingSteers)
	}
	if got := strings.TrimSpace(state.composer.DraftText); got != "" {
		t.Fatalf("expected composer left empty when steers are submitted, got %q", got)
	}
}

func TestRunTurnCtrlCCancelDoesNotReplayPendingSteers(t *testing.T) {
	dispatchStarted := make(chan struct{})
	dispatchWait := make(chan struct{})
	session := &fakeSession{
		cancelActive:    true,
		dispatchStarted: dispatchStarted,
		dispatchWait:    dispatchWait,
	}
	state := &streamState{
		sessionID: "s1",

		pendingSteers: []ComposerSubmission{
			{Text: "auto fix all bugs", Parts: []llm.ContentPart{llm.Text("auto fix all bugs")}, DisplayText: "auto fix all bugs"},
		},
		activeRunCtrlCExitArmed: true,
	}
	var out bytes.Buffer
	renderer := NewRenderer(&out, &out)
	events := make(chan inputEvent, 1)
	sigCh := make(chan os.Signal, 1)
	done := make(chan runTurnDisposition, 1)

	go func() {
		disposition, _ := runTurn(context.Background(), sigCh, events, nil, nil, session, renderer, nil, state, "hello", "hello", "", nil, "", nil, nil, nil)
		done <- disposition
	}()
	<-dispatchStarted
	events <- inputEvent{kind: inputEventHotkey, hotkey: hotkeyInterrupt}
	waitForCancelCalls(t, session, 1)
	close(dispatchWait)

	if got := <-done; got != runTurnCancelled {
		t.Fatalf("expected runTurnCancelled after ctrl+c cancel, got %q", got)
	}
	next, ok := nextAutomaticSubmission(state, runTurnCancelled)
	if ok {
		t.Fatalf("expected no automatic replay after ctrl+c cancel, got %#v", next)
	}
	if got := strings.TrimSpace(state.composer.DraftText); got != "auto fix all bugs" {
		t.Fatalf("expected pending steer restored to composer after ctrl+c cancel, got %q", got)
	}
}

// TestExecuteComposerSubmissionPreservesCtrlCEscalationAcrossAutoContinuation
// guards the fix for a TUI hang: mashing Ctrl+C during an active run printed
// "cancelling run" over and over and the process never exited, because every
// automatically chained continuation (a queued/pending submission
// nextAutomaticSubmission replays right after a turn ends) reset the
// cancel/quit escalation sequence back to its start. A user whose Ctrl+C had
// already armed "exit" on one chained turn would have that progress silently
// discarded the instant the next chained turn began, so the escalation could
// never reach "quit" as long as chained turns kept arriving faster than a
// second keypress. This test starts a turn the way an automatic continuation
// does (preserveComposer true) with the exit-arm already set from a prior
// turn, and asserts a single Ctrl+C finishes the escalation (quits directly)
// instead of restarting it (cancelling again).
func TestExecuteComposerSubmissionPreservesCtrlCEscalationAcrossAutoContinuation(t *testing.T) {
	dispatchStarted := make(chan struct{})
	dispatchWait := make(chan struct{})
	session := &fakeSession{
		cancelActive:    true,
		dispatchStarted: dispatchStarted,
		dispatchWait:    dispatchWait,
	}
	state := &streamState{
		sessionID:               "s1",
		activeRunCtrlCExitArmed: true,
	}
	var out bytes.Buffer
	renderer := NewRenderer(&out, &out)
	events := make(chan inputEvent, 1)
	sigCh := make(chan os.Signal, 1)
	done := make(chan runTurnDisposition, 1)

	submission := ComposerSubmission{
		Text:        "queued follow-up",
		Parts:       []llm.ContentPart{llm.Text("queued follow-up")},
		DisplayText: "queued follow-up",
	}

	go func() {
		disposition, _ := executeComposerSubmission(context.Background(), sigCh, events, nil, nil, session, renderer, nil, state, submission, nil, nil, true, nil)
		done <- disposition
	}()
	<-dispatchStarted
	// The event is queued before the dispatch goroutine is released, so the
	// run loop's per-iteration input drain (which always runs before it can
	// observe errCh) is guaranteed to process it first.
	events <- inputEvent{kind: inputEventHotkey, hotkey: hotkeyInterrupt}
	close(dispatchWait)

	// <-done is the only safe synchronization point for reading state/session
	// from this goroutine: the run loop that mutates them only stops once it
	// returns, right before sending here.
	if got := <-done; got != runTurnCancelled {
		t.Fatalf("expected runTurnCancelled once ctrl+c finishes the already-armed escalation, got %q", got)
	}
	if !state.quitRequested {
		t.Fatal("expected the carried-over exit-arm to have set quitRequested")
	}
	session.mu.Lock()
	cancelCalls := session.cancelCalls
	session.mu.Unlock()
	if cancelCalls != 0 {
		t.Fatalf("expected no new cancel call: exit was already armed from the prior chained turn, so one ctrl+c should quit directly, got %d cancel call(s)", cancelCalls)
	}
}

func TestRunTurnCancelWaitsForCancelledTurnPersistence(t *testing.T) {
	tests := []struct {
		name            string
		hotkeys         []inputHotkey
		wantDisposition runTurnDisposition
		wantQuit        bool
	}{
		{
			name:            "escape",
			hotkeys:         []inputHotkey{hotkeyEscapeInterrupt},
			wantDisposition: runTurnInterrupted,
		},
		{
			name:            "ctrl-c then quit",
			hotkeys:         []inputHotkey{hotkeyInterrupt, hotkeyInterrupt},
			wantDisposition: runTurnCancelled,
			wantQuit:        true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dispatchStarted := make(chan struct{})
			dispatchWait := make(chan struct{})
			persisted := make(chan struct{})
			session := &fakeSession{
				cancelActive:       true,
				cancelled:          make(chan struct{}),
				dispatchStarted:    dispatchStarted,
				dispatchWait:       dispatchWait,
				dispatchCancelWait: persisted,
			}
			state := &streamState{sessionID: "s1"}
			var out bytes.Buffer
			renderer := NewRenderer(&out, &out)
			events := make(chan inputEvent, len(tt.hotkeys))
			done := make(chan runTurnDisposition, 1)

			go func() {
				disposition, _ := runTurn(context.Background(), nil, events, nil, nil, session, renderer, nil, state, "hello", "hello", "", nil, "", nil, nil, nil)
				done <- disposition
			}()
			<-dispatchStarted

			for _, hotkey := range tt.hotkeys {
				events <- inputEvent{kind: inputEventHotkey, hotkey: hotkey}
			}
			waitForCancelCalls(t, session, 1)
			select {
			case got := <-done:
				t.Fatalf("runTurn returned %q before cancelled history was persisted", got)
			case <-time.After(25 * time.Millisecond):
			}

			close(persisted)
			if got := <-done; got != tt.wantDisposition {
				t.Fatalf("runTurn disposition = %q, want %q", got, tt.wantDisposition)
			}
			if state.quitRequested != tt.wantQuit {
				t.Fatalf("quitRequested = %v, want %v", state.quitRequested, tt.wantQuit)
			}
		})
	}
}

func waitForCancelCalls(t *testing.T, session *fakeSession, want int) {
	t.Helper()
	for i := 0; i < 2000; i++ {
		session.mu.Lock()
		got := session.cancelCalls
		session.mu.Unlock()
		if got >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d cancel call(s)", want)
}

func TestSwitchStreamSessionResetsTransientSessionState(t *testing.T) {
	tracker := NewTracker()
	tracker.ObserveAgent("agent-1", time.Time{})
	tracker.ObserveToolStep("", "read-1", "read_file", "")
	tracker.ObserveUsageDelta("run-1", 1500, 300)
	tracker.ObservePlanProgress(1, 3, "drafting")

	state := &streamState{
		sessionID: "s1",
		composer: ComposerState{
			Text:          "draft",
			DraftText:     "draft",
			Cursor:        5,
			Attachments:   []InputAttachment{{Path: "/tmp/a.png", MIMEType: "image/png"}},
			PendingPastes: []PendingPaste{{ID: "paste-1", Placeholder: "[Pasted Content 5 chars]", Content: "hello"}},
		},
		pendingSteers:   []ComposerSubmission{{Text: "pending steer"}},
		queuedTurns:     []queuedSubmission{{Action: queuedSubmissionActionTurn, Submission: ComposerSubmission{Text: "queued"}}},
		holdComposer:    true,
		userInterrupted: true,
		quitRequested:   true,
	}
	var out bytes.Buffer
	renderer := NewRenderer(&out, &out)
	renderer.SetComposerTokenStats(ComposerTokenStats{Active: true, InputTokens: 1500, OutputTokens: 300, PercentLeft: 93})

	state.session = &fakeSession{model: "deepseek/deepseek-v4-flash"}
	if _, _, err := switchStreamSession(context.Background(), state, renderer, tracker, "s2"); err != nil {
		t.Fatalf("switchStreamSession: %v", err)
	}

	if state.sessionID != "s2" {
		t.Fatalf("expected session switched to s2, got %q", state.sessionID)
	}
	if state.composer.Text != "" || state.composer.DraftText != "" || state.composer.Cursor != 0 {
		t.Fatalf("expected composer cleared, got %+v", state.composer)
	}
	if len(state.composer.Attachments) != 0 || len(state.composer.PendingPastes) != 0 {
		t.Fatalf("expected composer attachments/pastes cleared, got %+v", state.composer)
	}
	if len(state.pendingSteers) != 0 || len(state.queuedTurns) != 0 {
		t.Fatalf("expected pending submissions cleared, got steers=%#v queued=%#v", state.pendingSteers, state.queuedTurns)
	}
	if state.holdComposer || state.userInterrupted || state.quitRequested {
		t.Fatalf("expected transient flags cleared, got hold=%v interrupt=%v quit=%v", state.holdComposer, state.userInterrupted, state.quitRequested)
	}
	if snap := tracker.SnapshotSession(); snap != (Counters{}) {
		t.Fatalf("expected tracker reset, got %+v", snap)
	}
	stats := renderer.ComposerTokenStats()
	if !stats.Active || stats.PercentLeft != 100 || stats.ContextWindow <= 0 {
		t.Fatalf("expected fresh-session token budget, got %+v", stats)
	}
	if stats.InputTokens != 0 || stats.OutputTokens != 0 {
		t.Fatalf("expected prior session token usage cleared, got %+v", stats)
	}
	footer := formatComposerTokenStats(stats)
	if !strings.Contains(footer, "100%") || !(strings.Contains(footer, "k") || strings.Contains(footer, "M")) {
		t.Fatalf("expected auto-compact percentage and context window in footer, got %q", footer)
	}
}

func TestAbbreviateHomePathUsesTilde(t *testing.T) {
	got := abbreviateHomePath("/home/ubuntu/workspace/forebrain", "/home/ubuntu")
	if got != "~/workspace/forebrain" {
		t.Fatalf("abbreviateHomePath()=%q want %q", got, "~/workspace/forebrain")
	}
}

func TestSummarizeComposerModelFormatsProviderAndModelWithoutSpaces(t *testing.T) {
	session := &fakeSession{model: "custom / qwen3.5-122b"}
	if got := summarizeComposerModel(session); got != "custom/qwen3.5-122b" {
		t.Fatalf("summarizeComposerModel()=%q", got)
	}
}

func TestSummarizeComposerReasoningEffortTrimsValue(t *testing.T) {
	session := &fakeSession{currentReasoningEffort: " high "}
	if got := summarizeComposerReasoningEffort(session); got != "high" {
		t.Fatalf("summarizeComposerReasoningEffort()=%q", got)
	}
}

func TestInitialComposerTokenStatsSeedsFullBudget(t *testing.T) {
	// A fresh/just-resumed session has no usage yet. The footer must show
	// "100%" from the first frame, not the
	// "compact pending" placeholder that PercentLeft==0 renders.
	session := &fakeSession{model: "deepseek/deepseek-v4-flash"}
	stats := initialComposerTokenStats(session)
	if !stats.Active {
		t.Fatalf("initialComposerTokenStats() Active=false, want true")
	}
	if stats.PercentLeft != 100 {
		t.Fatalf("initialComposerTokenStats() PercentLeft=%d, want 100", stats.PercentLeft)
	}
	if stats.ContextWindow <= 0 {
		t.Fatalf("initialComposerTokenStats() ContextWindow=%d, want >0", stats.ContextWindow)
	}
	if got := formatComposerTokenStats(stats); !strings.Contains(got, "100%") {
		t.Fatalf("formatComposerTokenStats()=%q, want it to contain %q", got, "100%")
	}
}

func TestInitialComposerTokenStatsUnknownModelStaysInactive(t *testing.T) {
	session := &fakeSession{model: ""}
	if stats := initialComposerTokenStats(session); stats.Active {
		t.Fatalf("initialComposerTokenStats() Active=true for empty model, want false")
	}
}

// TestSurfaceComposerTokenStatsCountsTheConfiguredCompactLimit pins that the
// footer seeded at startup and on resume is the live footer: the same budget,
// the configured auto-compact limit included, so a resumed session about to
// compact does not read "100%".
func TestSurfaceComposerTokenStatsCountsTheConfiguredCompactLimit(t *testing.T) {
	cfg := appcfg.Root{}
	cfg.Agents.Definitions = map[string]appcfg.AgentDefinition{"main": {
		Primary:      true,
		LLMProviders: []appcfg.AgentLLMProviderConfig{{Provider: "deepseek", Model: "deepseek-chat", APIKey: "k", BaseURL: "http://127.0.0.1:9/v1"}},
	}}
	cfg.Compact.ModelAutoCompactTokenLimit = 1000
	home := t.TempDir()
	s := sessionEnv{Home: home, Config: cfg, Runner: &runpkg.Runner{Deps: &runpkg.Deps{Home: home, AppCfg: &cfg}}}.session()
	full := s.SurfaceComposerTokenStats(0)
	if !full.Active || full.PercentLeft != 100 {
		t.Fatalf("empty context = %+v, want the full budget", full)
	}
	over := s.SurfaceComposerTokenStats(5000)
	live, ok := s.tokenBudgetMessageFromUsage(5000)
	if !ok || over != composerTokenStatsFromBudget(live) {
		t.Fatalf("footer %+v differs from the live budget %+v", over, live)
	}
	if over.PercentLeft != 0 {
		t.Fatalf("a context past the configured limit reads %d%% left", over.PercentLeft)
	}
}

func TestReadRawInputEventsRoutesCtrlBToBackground(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe error: %v", err)
	}
	defer r.Close()
	events := readRawInputEvents(context.Background(), r, io.Discard, stubRawInputController{}, nil)
	if _, err := w.Write([]byte{0x02}); err != nil {
		t.Fatalf("write ctrl+b: %v", err)
	}
	ev := nextNonDraftEvent(t, events)
	if ev.kind != inputEventHotkey || ev.hotkey != hotkeyBackground {
		t.Fatalf("expected raw ctrl+b to emit background hotkey, got %#v", ev)
	}
	_ = w.Close()
}

func TestRunTurnCtrlBDoesNotDetachAssistantTurn(t *testing.T) {
	dispatchStarted := make(chan struct{})
	dispatchWait := make(chan struct{})
	session := &fakeSession{
		dispatchStarted: dispatchStarted,
		dispatchWait:    dispatchWait,
	}
	state := &streamState{sessionID: "s1"}
	var out bytes.Buffer
	renderer := NewRenderer(&out, &out)
	events := make(chan inputEvent, 1)
	sigCh := make(chan os.Signal, 1)
	done := make(chan struct {
		disposition runTurnDisposition
		err         error
	}, 1)

	go func() {
		disposition, err := runTurn(context.Background(), sigCh, events, nil, nil, session, renderer, nil, state, "hello", "hello", "", nil, "", nil, nil, nil)
		done <- struct {
			disposition runTurnDisposition
			err         error
		}{disposition: disposition, err: err}
	}()
	<-dispatchStarted
	events <- inputEvent{kind: inputEventHotkey, hotkey: hotkeyBackground}

	select {
	case result := <-done:
		t.Fatalf("runTurn detached the assistant turn after ctrl+b: disposition=%s err=%v", result.disposition, result.err)
	case <-time.After(50 * time.Millisecond):
	}
	close(dispatchWait)
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("runTurn error after dispatch completed: %v", result.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runTurn did not return after dispatch completed")
	}
	got := out.String()
	if strings.Contains(got, "backgrounded") || strings.Contains(got, "turn detached") {
		t.Fatalf("ctrl+b must not advertise assistant-turn backgrounding, got %q", got)
	}
}

// At idle, ctrl+b is a no-op (handled=true), preventing the
// input loop from exiting on the default branch.
func TestHandleIdleHotkeyBackgroundIsNoop(t *testing.T) {
	session := &fakeSession{}
	state := &streamState{sessionID: "s1"}
	var out bytes.Buffer
	renderer := NewRenderer(&out, &out)
	handled := handleIdleHotkey(context.Background(), session, renderer, state, nil, nil, hotkeyBackground)
	if !handled {
		t.Fatal("expected handleIdleHotkey to swallow idle ctrl+b")
	}
	if state.quitRequested {
		t.Fatal("idle ctrl+b must not request quit")
	}
	if out.Len() != 0 {
		t.Fatalf("idle ctrl+b must not emit output, got %q", out.String())
	}
}

func TestCtrlXCtrlKRequestsCancelAllAgents(t *testing.T) {
	session := &fakeSession{}
	state := &streamState{sessionID: "s1"}
	renderer := NewRenderer(io.Discard, io.Discard)

	if !handleIdleHotkey(context.Background(), session, renderer, state, nil, nil, hotkeyAgentControlPrefix) {
		t.Fatal("expected ctrl+x prefix to be handled")
	}
	if !handleIdleHotkey(context.Background(), session, renderer, state, nil, nil, hotkeyAgentStopAll) {
		t.Fatal("expected ctrl+k after ctrl+x to be handled")
	}
	if session.cancelAllAgentsCalls != 1 {
		t.Fatalf("CancelAllAgents calls=%d want 1", session.cancelAllAgentsCalls)
	}
}

func TestAgentRosterEnterSwitchesSelectedView(t *testing.T) {
	session := &fakeSession{}
	state := &streamState{
		sessionID:           "parent",
		agentRosterSelected: 1,
		agentRosterFocused:  true,
		agentRoster: AgentRosterSnapshot{Rows: []AgentRosterRow{
			{ID: "main", Kind: "primary", SessionID: "parent"},
			{ID: "child-agent", Kind: "subagent", SessionID: "child-session"},
		}},
	}
	renderer := NewRenderer(io.Discard, io.Discard)
	tracker := NewTracker()

	if !state.handleAgentRosterLineInput(session, renderer, tracker, "") {
		t.Fatal("expected Enter on selected roster row to be handled")
	}
	// Enter on subagent row switches the viewport view, NOT the chat state.
	if state.sessionID != "parent" {
		t.Fatalf("sessionID=%q want parent (view switch, not session switch)", state.sessionID)
	}

	// Enter on primary row switches back to the primary view.
	state.agentRosterSelected = 0
	if !state.handleAgentRosterLineInput(session, renderer, tracker, "") {
		t.Fatal("expected Enter on primary roster row to be handled")
	}
	if state.sessionID != "parent" {
		t.Fatalf("sessionID=%q want parent", state.sessionID)
	}
}

func TestAgentRosterXUsesSelectedRowCancel(t *testing.T) {
	session := &fakeSession{}
	state := &streamState{
		sessionID:           "parent",
		agentRosterSelected: 0,
		agentRosterFocused:  true,
		agentRoster: AgentRosterSnapshot{Rows: []AgentRosterRow{
			{ID: "child-agent", Kind: "subagent", Label: "review", SessionID: "child-session", RunID: "run-1"},
		}},
	}
	renderer := NewRenderer(io.Discard, io.Discard)

	if !state.handleAgentRosterLineInput(session, renderer, nil, "x") {
		t.Fatal("expected x on selected roster row to be handled")
	}
	if session.cancelSubagentQuery.AgentID != "child-agent" || session.cancelSubagentQuery.RunID != "run-1" {
		t.Fatalf("cancel query=%#v want child-agent/run-1", session.cancelSubagentQuery)
	}
}

func TestAgentRosterHotkeyStopCancelsSubagent(t *testing.T) {
	session := &fakeSession{}
	state := &streamState{
		sessionID:           "parent",
		agentRosterSelected: 0,
		agentRosterFocused:  true,
		agentRoster: AgentRosterSnapshot{Rows: []AgentRosterRow{
			{ID: "child-agent", Kind: "subagent", Label: "review", SessionID: "child-session", RunID: "run-1"},
		}},
	}
	renderer := NewRenderer(io.Discard, io.Discard)

	handleActiveRunInput(context.Background(), session, renderer, nil, state, nil,
		inputEvent{kind: inputEventHotkey, hotkey: hotkeyRosterStop}, nil)

	if session.cancelSubagentQuery.AgentID != "child-agent" || session.cancelSubagentQuery.RunID != "run-1" {
		t.Fatalf("cancel query=%#v want child-agent/run-1", session.cancelSubagentQuery)
	}
}

func TestAgentRosterHotkeyStopNoOpWhenNotFocused(t *testing.T) {
	session := &fakeSession{}
	state := &streamState{
		sessionID:           "parent",
		agentRosterSelected: 0,
		agentRosterFocused:  false,
		agentRoster: AgentRosterSnapshot{Rows: []AgentRosterRow{
			{ID: "child-agent", Kind: "subagent", Label: "review", SessionID: "child-session", RunID: "run-1"},
		}},
	}
	renderer := NewRenderer(io.Discard, io.Discard)

	handleActiveRunInput(context.Background(), session, renderer, nil, state, nil,
		inputEvent{kind: inputEventHotkey, hotkey: hotkeyRosterStop}, nil)

	if session.cancelSubagentQuery.AgentID != "" {
		t.Fatalf("expected no cancel when roster not focused, got %#v", session.cancelSubagentQuery)
	}
}

func TestHotkeyTogglePlanModeFlipsModestoreState(t *testing.T) {
	home := t.TempDir()
	session := &fakeSession{}
	var out bytes.Buffer
	renderer := NewRenderer(&out, &out)
	state := &streamState{sessionID: "s1", home: home, workspaceRoot: home}

	if !handleIdleHotkey(context.Background(), session, renderer, state, nil, nil, hotkeyTogglePlanMode) {
		t.Fatal("expected hotkey handled")
	}
	st, err := statepkg.Get(home, "s1")
	if err != nil {
		t.Fatalf("state.Get: %v", err)
	}
	if st.Mode != statepkg.ModePlan {
		t.Fatalf("first toggle: mode=%q want plan", st.Mode)
	}

	if !handleIdleHotkey(context.Background(), session, renderer, state, nil, nil, hotkeyTogglePlanMode) {
		t.Fatal("expected hotkey handled second time")
	}
	st, err = statepkg.Get(home, "s1")
	if err != nil {
		t.Fatalf("state.Get: %v", err)
	}
	if st.Mode != statepkg.ModeAgent {
		t.Fatalf("second toggle: mode=%q want agent", st.Mode)
	}
}

func TestFormatWorkingCountersIncludesPlanProgress(t *testing.T) {
	got := formatWorkingCounters(Counters{
		PlanDone:     1,
		PlanTotal:    3,
		PlanActive:   "Writing tests",
		Agents:       2,
		Tools:        4,
		InputTokens:  1500,
		OutputTokens: 300,
	})
	if got != "☑1/3 · Writing tests" {
		t.Fatalf("working counters = %q, want plan progress and the active title only (agents, tools and tokens are no longer reported)", got)
	}
	if got := formatWorkingCounters(Counters{InputTokens: 1500, OutputTokens: 300, Tools: 2}); got != "" {
		t.Fatalf("counters without a checklist must render nothing, got %q", got)
	}
}

// With a checklist active, the checklist is the only counter reported — the
// ☑N/M progress and the shortest active title — and the headline keeps saying
// "Working", never the title.
func TestWorkingStatusFormatterKeepsWorkingHeadlineAndShowsActiveTask(t *testing.T) {
	counters := Counters{PlanDone: 1, PlanTotal: 3, PlanActive: "Writing tests", InputTokens: 1500, OutputTokens: 300}
	formatter := workingStatusFormatter(func() Counters { return counters }, nil)
	got := formatter(20*time.Second, 2, "")
	if got != "- Working (20s • esc to interrupt) · ☑1/3 · Writing tests" {
		t.Fatalf("working line = %q, want literal headline with checklist progress only", got)
	}
}

// Several checklist items can be in flight at once, but the working line names
// only one — the shortest in-progress title, ties keeping the first. Anything
// else (no in-progress item, no items at all) falls back to the payload's own
// summary, and titles are compared and reported trimmed.
func TestShortestActiveTaskTitle(t *testing.T) {
	cases := []struct {
		name     string
		items    []event.PlanUpdateItem
		fallback string
		want     string
	}{
		{
			name: "several in-progress items pick the shortest",
			items: []event.PlanUpdateItem{
				{Content: "write the long integration test suite", Status: "in_progress"},
				{Content: "writing tests", Status: "in_progress"},
				{Content: "review the whole diff carefully", Status: "in_progress"},
				{Content: "pending item", Status: "pending"},
			},
			fallback: "explanation",
			want:     "writing tests",
		},
		{
			name: "the active form wins over the checklist content",
			items: []event.PlanUpdateItem{
				{Content: "P1 pkg/llm/openai 发现客户端 + 契约测试", Active: "实现P1订阅模型发现客户端", Status: "in_progress"},
				{Content: "P2 pkg/turn", Active: "实现P2", Status: "pending"},
			},
			fallback: "explanation",
			want:     "实现P1订阅模型发现客户端",
		},
		{
			name: "a tie keeps the first",
			items: []event.PlanUpdateItem{
				{Content: "ab", Status: "in_progress"},
				{Content: "cd", Status: "in_progress"},
			},
			fallback: "explanation",
			want:     "ab",
		},
		{
			name:     "no in-progress item falls back",
			items:    []event.PlanUpdateItem{{Content: "done thing", Status: "completed"}},
			fallback: "explanation",
			want:     "explanation",
		},
		{
			name:     "empty items fall back",
			fallback: "explanation",
			want:     "explanation",
		},
		{
			name: "surrounding whitespace is trimmed",
			items: []event.PlanUpdateItem{
				{Content: "  writing tests  ", Status: "in_progress"},
				{Content: "pending item", Status: "pending"},
			},
			fallback: "explanation",
			want:     "writing tests",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shortestActiveTaskTitle(tc.items, tc.fallback); got != tc.want {
				t.Fatalf("shortestActiveTaskTitle() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCancelableCompactKeepsNotificationsResponsiveAndCancelsHandler(t *testing.T) {
	notifyCh := make(chan any, 2)
	events := make(chan inputEvent, 1)
	processed := make(chan struct{})
	cancelObserved := make(chan struct{})
	session := &fakeSession{}
	session.streamSlashFn = func(ctx context.Context, sessionID, line string) (SlashOutcome, bool) {
		if sessionID != "s1" || line != "/compact" {
			t.Errorf("unexpected slash call session=%q line=%q", sessionID, line)
		}
		notifyCh <- ContextCompactingMsg{CompactionID: "compact-test"}
		<-ctx.Done()
		close(cancelObserved)
		return SlashOutcome{Handled: true}, true
	}
	renderer := NewRenderer(io.Discard, io.Discard)
	state := &streamState{sessionID: "s1", session: session}
	cmds := &commandController{session: session, renderer: renderer}

	type result struct {
		handled bool
	}
	done := make(chan result, 1)
	go func() {
		handled, _, _, _ := dispatchCancelableStreamSlashCommand(
			context.Background(), nil, events, notifyCh,
			func(msg any) {
				if _, ok := msg.(ContextCompactingMsg); ok {
					select {
					case <-processed:
					default:
						close(processed)
					}
				}
			},
			cmds, renderer, nil, state, "/compact",
		)
		done <- result{handled: handled}
	}()

	select {
	case <-processed:
		// The running notification was handled while ExecuteSurfaceSlash was
		// still blocked, proving the UI loop did not freeze behind /assembly.
	case <-time.After(time.Second):
		t.Fatal("compact notification was not processed while handler was running")
	}
	events <- inputEvent{kind: inputEventHotkey, hotkey: hotkeyInterrupt}

	select {
	case <-cancelObserved:
	case <-time.After(time.Second):
		t.Fatal("Ctrl+C did not cancel the compact handler context")
	}
	select {
	case got := <-done:
		if !got.handled {
			t.Fatal("cancelled compact slash should remain handled")
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled compact slash did not return")
	}
}

func TestShutdownTerminalInputCancelsDisablesFlushesThenRestores(t *testing.T) {
	inputCtx, cancelInput := context.WithCancel(context.Background())
	recorder := &shutdownCallRecorder{}

	shutdownTerminalInput(recorder, recorder, cancelInput)

	if inputCtx.Err() != context.Canceled {
		t.Fatalf("input context error=%v want %v", inputCtx.Err(), context.Canceled)
	}
	want := []string{"disable", "flush", "restore"}
	if len(recorder.calls) != len(want) {
		t.Fatalf("shutdown calls=%v want %v", recorder.calls, want)
	}
	for i := range want {
		if recorder.calls[i] != want[i] {
			t.Fatalf("shutdown calls=%v want %v", recorder.calls, want)
		}
	}
	for _, seq := range []string{disableMouseSeq, disableFocusReportingSeq, disableBracketedPasteSeq, resetComposerCursorColorSeq} {
		if !strings.Contains(recorder.writes, seq) {
			t.Fatalf("terminal shutdown output missing %q: %q", seq, recorder.writes)
		}
	}
}

// The helpers below were production functions that only the tests ever
// called: each is a thin composition of live code. They live here so the
// production files carry no unused code while the tests keep exercising
// the live functions underneath.

func runTurn(ctx context.Context, sigCh <-chan os.Signal, events <-chan inputEvent, notifyCh <-chan any, processNotify func(any), session Session, renderer *Renderer, tracker *Tracker, state *streamState, line string, displayText string, rawInput string, attachments []InputAttachment, goalObjective string, clipboard ClipboardImageReader, cmds *commandController, turnWorkedStatus *string) (runTurnDisposition, error) {
	return runTurnWithSkill(ctx, sigCh, events, notifyCh, processNotify, session, renderer, tracker, state, line, displayText, rawInput, attachments, goalObjective, "", "", clipboard, cmds, turnWorkedStatus, true)
}

func (s *streamState) beginResumableInputPause(resume func()) {
	if s == nil {
		if resume != nil {
			resume()
		}
		return
	}
	s.resumeInputReadIfNeeded()
	s.resumeRawRead = resume
}

// The /resume picker must page through the session store instead of loading
// the whole history: this file pins the page size, the LIMIT/OFFSET ladder the
// picker walks as the cursor reaches the bottom, and the mapping from the
// selected row back to its session ID across page boundaries.

type sessionPageQuery struct {
	limit  int
	offset int
}

// pagedSessionFake extends fakeSession with the optional paginated lister the
// resume picker looks for, recording every query it receives.
type pagedSessionFake struct {
	*fakeSession
	all     []SessionSummary
	queries []sessionPageQuery
}

func (f *pagedSessionFake) ListSessionsRecentPaged(_ context.Context, limit, offset int) ([]SessionSummary, error) {
	f.queries = append(f.queries, sessionPageQuery{limit: limit, offset: offset})
	out := []SessionSummary{}
	for i := offset; i < len(f.all) && len(out) < limit; i++ {
		out = append(out, f.all[i])
	}
	return out, nil
}

// TestPromptRecentSessionPagesThroughHistory drives the real interactive
// picker with down-arrow keystrokes: holding the down arrow walks to the very
// last session of a
// 45-session history, loading pages of 20 on demand, and Enter returns that
// session's ID. The full history must never be requested at once.
func TestPromptRecentSessionPagesThroughHistory(t *testing.T) {
	const total = 45
	fake := &pagedSessionFake{fakeSession: &fakeSession{}}
	for i := 0; i < total; i++ {
		// Newest first: index 0 is the most recent session.
		fake.all = append(fake.all, SessionSummary{
			ID:        fmt.Sprintf("s-%d", i),
			Title:     fmt.Sprintf("session %02d", i),
			UpdatedAt: int64(1_000_000 - i),
		})
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	sel := newRawSelector(r, nil)

	// 45 down-arrow presses walk the cursor onto the last row (each page boundary
	// costs one extra press to fetch the next page), then Enter confirms.
	keys := make([]byte, 0, total*3+1)
	for i := 0; i < total; i++ {
		keys = append(keys, '\x1b', '[', 'B')
	}
	keys = append(keys, '\r')
	go func() {
		if _, err := w.Write(keys); err != nil {
			t.Errorf("feed keys: %v", err)
		}
	}()

	got, _ := promptRecentSession(context.Background(), fake, sel, func(context.Context) (string, error) { return "", nil }, "")
	if got != "s-44" {
		t.Fatalf("selected %q, want the last session s-44", got)
	}

	wantQueries := []sessionPageQuery{
		{limit: 20, offset: 0},
		{limit: 20, offset: 20},
		{limit: 20, offset: 40}, // short page (5 < 20) already marks exhaustion
	}
	if len(fake.queries) != len(wantQueries) {
		t.Fatalf("store saw %d queries %+v, want %d", len(fake.queries), fake.queries, len(wantQueries))
	}
	for i, want := range wantQueries {
		if fake.queries[i] != want {
			t.Fatalf("query %d = %+v, want %+v", i, fake.queries[i], want)
		}
	}
}

// TestPromptRecentSessionFallbackSinglePage pins the non-paginated fallback:
// a session surface without the paginated lister still gets the first page of
// 20 through the plain selector contract.
func TestPromptRecentSessionFallbackSinglePage(t *testing.T) {
	fake := &fakeSession{}
	for i := 0; i < 30; i++ {
		fake.recent = append(fake.recent, SessionSummary{
			ID:        fmt.Sprintf("s-%d", i),
			Title:     fmt.Sprintf("session %02d", i),
			UpdatedAt: int64(1_000_000 - i),
		})
	}
	sel := &stubSelector{selectRichIdx: 19, selectRichOK: true}

	got, _ := promptRecentSession(context.Background(), fake, sel, func(context.Context) (string, error) { return "", nil }, "")
	if got != "s-19" {
		t.Fatalf("selected %q, want s-19", got)
	}
	if len(sel.selectRichItems) != resumeSessionPageSize {
		t.Fatalf("fallback picker got %d items, want one page of %d", len(sel.selectRichItems), resumeSessionPageSize)
	}
}

func TestForegroundWithdrawalArbitration(t *testing.T) {
	for _, responseFirst := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		turn := &foregroundTurn{cancel: cancel}
		if responseFirst {
			turn.acknowledgeResponse()
		}
		if got := turn.withdraw(); got == responseFirst {
			t.Fatalf("responseFirst=%v, withdrawal=%v", responseFirst, got)
		}
		if responseFirst {
			if ctx.Err() != nil {
				t.Fatal("response winner was cancelled by withdrawal")
			}
		} else {
			if ctx.Err() != context.Canceled || turn.acknowledgeResponse() || !turn.withdraw() {
				t.Fatal("withdrawal must cancel, reject output, and be idempotent")
			}
		}
		cancel()
	}
	finished := &foregroundTurn{}
	finished.finish()
	if finished.withdraw() {
		t.Fatal("withdrew a completed dispatch")
	}
}

func TestForegroundWithdrawalRace(t *testing.T) {
	for range 200 {
		turn := &foregroundTurn{}
		start := make(chan struct{})
		var response, withdrawal bool
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); <-start; response = turn.acknowledgeResponse() }()
		go func() { defer wg.Done(); <-start; withdrawal = turn.withdraw() }()
		close(start)
		wg.Wait()
		if response == withdrawal {
			t.Fatalf("exactly one must win: response=%v withdrawal=%v", response, withdrawal)
		}
	}
}

// composerProbeSelector records what the composer looked like at the instant a
// modal handler opened. That instant is the whole point: a submitted slash
// command must already be out of the composer by then, and its popup gone,
// because the handler can own the screen for as long as it likes.
type composerProbeSelector struct {
	renderer    *Renderer
	opened      bool
	composer    string
	overlayRows int
}

func (s *composerProbeSelector) capture() {
	s.opened = true
	s.renderer.mu.Lock()
	s.composer = s.renderer.composerState.Text
	s.overlayRows = len(s.renderer.composerState.OverlayRows)
	s.renderer.mu.Unlock()
}

func (s *composerProbeSelector) Select(string, []string, string) (string, bool, error) {
	s.capture()
	return "", false, nil
}

func (s *composerProbeSelector) MultiSelect(string, []string, []string) ([]string, bool, error) {
	s.capture()
	return nil, false, nil
}

func (s *composerProbeSelector) Input(string, string) (string, bool, error) {
	s.capture()
	return "", false, nil
}

func (s *composerProbeSelector) Confirm(string, bool) (bool, bool, error) {
	s.capture()
	return false, false, nil
}

func (s *composerProbeSelector) SelectRich(string, []SelectItem, int) (int, bool, error) {
	s.capture()
	return -1, false, nil
}

func (s *composerProbeSelector) Secret(label string, defaultValue string) (string, bool, error) {
	return s.Input(label, defaultValue)
}

func (s *composerProbeSelector) Review(string, []turn.StatusFact, []string, int) (int, bool, error) {
	return -1, false, nil
}

func composerProbeState() *streamState {
	state := &streamState{sessionID: "s1"}
	state.composer.DraftText = "/perm"
	state.composer.Text = "/perm"
	state.composer.Cursor = len("/perm")
	state.composer.SlashOverlay = &SlashOverlay{
		Active:      true,
		FilterText:  "perm",
		SelectedIdx: 0,
		Visible:     []turn.Command{{Name: "permissions", Description: "permission presets"}},
	}
	return state
}

// The submitted command has left the composer the moment the loop takes it, so
// a handler that opens a picker — or starts a job — must never be presented
// behind a composer still showing the text that launched it.
func TestActiveRunSlashClearsComposerBeforeHandlerOpens(t *testing.T) {
	session := &fakeSession{}
	var out bytes.Buffer
	renderer := NewRenderer(&out, &out)
	probe := &composerProbeSelector{renderer: renderer}
	cmds := newCommandController(session, renderer, probe, nil, "")
	state := composerProbeState()

	handleActiveRunInput(context.Background(), session, renderer, NewTracker(), state, nil,
		inputEvent{kind: inputEventLine, line: "/permissions"}, cmds)

	require.True(t, probe.opened, "the permissions picker never opened")
	require.Empty(t, probe.composer, "the submitted command was still on screen when the picker opened")
	require.Zero(t, probe.overlayRows, "the slash popup outlived the submission that closed it")
	require.Empty(t, state.composer.DraftText)
}

// Installing is a background job: the picker flow returns as soon as the
// answers are in, and the fetch reports itself through notifications instead
// of holding the event loop.
func TestSkillsInstallReturnsWithoutWaitingForTheFetch(t *testing.T) {
	session := skillsTestSession()
	selector := &scriptedSelector{
		richSteps: []richStep{
			{idx: 0, ok: true}, // Add a skill
			{idx: 2, ok: true}, // From a GitHub repo or URL
			{idx: 0, ok: true}, // global
		},
		inputs: []selectorInput{{value: "openai/skills", ok: true}},
	}
	var out bytes.Buffer
	ctrl := newCommandController(session, NewRenderer(&out, &out), selector, nil, "")

	done := make(chan struct{})
	go func() {
		defer close(done)
		ctrl.handleSkills(context.Background())
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the skills flow blocked on the install instead of backgrounding it")
	}
	require.Equal(t, "openai/skills:global", session.installedSkill)
}

// Escape during an MCP startup skips the optional servers and keeps the
// submission, instead of withdrawing it.
//
// This is the precedence the barrier needs and the reason it takes the branch
// before withdraw(): the ready barrier is waited on in exactly the window a
// submission has been committed and the model has produced nothing yet, which
// is the window withdraw() claims. Without this branch, "press Escape to stop
// waiting on the server you can do without" would instead take the user's
// message back out of the run.
func TestEscapeDuringMCPStartupSkipsOptionalServersAndKeepsTheSubmission(t *testing.T) {
	session := &fakeSession{mcpSkipResult: true}
	renderer := NewRenderer(nil, nil)
	foreground := &foregroundTurn{submission: mustComposerSubmission("deploy the release")}
	state := &streamState{sessionID: "s1", activeForeground: foreground}

	handleActiveRunInput(context.Background(), session, renderer, nil, state, nil,
		inputEvent{kind: inputEventHotkey, hotkey: hotkeyEscapeInterrupt}, nil)

	if session.mcpSkipCalls != 1 {
		t.Fatalf("Escape consulted the MCP skipper %d times, want once", session.mcpSkipCalls)
	}
	if foreground.isWithdrawn() {
		t.Fatal("Escape withdrew the submission instead of skipping the servers")
	}
	if state.composer.DraftText != "" {
		t.Fatalf("the submission was pulled back into the composer: %q", state.composer.DraftText)
	}
	if session.cancelCalls != 0 {
		t.Fatalf("Escape cancelled the run %d times; skipping servers is not cancelling", session.cancelCalls)
	}
	if state.activeRunCtrlCArmed || state.activeRunCtrlCExitArmed {
		t.Fatal("Escape must still reset the Ctrl+C escalation sequence")
	}
}

// Escape with only required servers left falls back to the existing withdraw
// behaviour: a required server cannot be skipped, and the user must be able to
// take their message back rather than be told something was skipped that was
// not.
func TestEscapeWithOnlyRequiredServersLeftWithdrawsTheSubmission(t *testing.T) {
	session := &fakeSession{mcpSkipResult: false}
	renderer := NewRenderer(nil, nil)
	foreground := &foregroundTurn{submission: mustComposerSubmission("deploy the release")}
	state := &streamState{sessionID: "s1", activeForeground: foreground}

	handleActiveRunInput(context.Background(), session, renderer, nil, state, nil,
		inputEvent{kind: inputEventHotkey, hotkey: hotkeyEscapeInterrupt}, nil)

	if session.mcpSkipCalls != 1 {
		t.Fatalf("Escape consulted the MCP skipper %d times, want once", session.mcpSkipCalls)
	}
	if !foreground.isWithdrawn() {
		t.Fatal("Escape must fall back to withdrawing the submission")
	}
	if state.composer.DraftText != "deploy the release" {
		t.Fatalf("withdrawn submission was not restored: %q", state.composer.DraftText)
	}
	// The fake reports an active run only when told to, so the withdraw path
	// must ask for the cancellation rather than assume it landed.
	if session.cancelCalls != 1 {
		t.Fatalf("the withdrawn turn's run was cancelled %d times, want once", session.cancelCalls)
	}
}

// Escape keeps its existing precedences: the roster and a subagent view are
// still backed out of first, and neither of them consults the MCP skipper.
func TestEscapePrecedencesStayAheadOfTheMCPStartupSkip(t *testing.T) {
	renderer := NewRenderer(nil, nil)
	renderer.NoteSubagentSpawned("sub-1", "explore")

	// A focused roster swallows Escape.
	session := &fakeSession{mcpSkipResult: true}
	state := &streamState{sessionID: "s1", agentRosterFocused: true, activeForeground: &foregroundTurn{submission: mustComposerSubmission("keep me")}}
	handleActiveRunInput(context.Background(), session, renderer, nil, state, nil,
		inputEvent{kind: inputEventHotkey, hotkey: hotkeyEscapeInterrupt}, nil)
	if state.agentRosterFocused {
		t.Fatal("Escape must unfocus the roster first")
	}
	if session.mcpSkipCalls != 0 {
		t.Fatalf("a focused roster must not reach the MCP skip (%d calls)", session.mcpSkipCalls)
	}

	// An open subagent view is closed next. The view only exists in viewport
	// mode, which is where a subagent's own alt-screen transcript lives.
	renderer.viewportMode = true
	renderer.SetActiveView("sub-1")
	if renderer.ActiveView() != "sub-1" {
		t.Fatal("the subagent view was not opened")
	}
	session = &fakeSession{mcpSkipResult: true}
	state = &streamState{sessionID: "s1", activeForeground: &foregroundTurn{submission: mustComposerSubmission("keep me")}}
	handleActiveRunInput(context.Background(), session, renderer, nil, state, nil,
		inputEvent{kind: inputEventHotkey, hotkey: hotkeyEscapeInterrupt}, nil)
	if renderer.ActiveView() != "" {
		t.Fatal("Escape must leave the subagent view first")
	}
	if session.mcpSkipCalls != 0 {
		t.Fatalf("an open subagent view must not reach the MCP skip (%d calls)", session.mcpSkipCalls)
	}
}

// TestEscapeAtIdleSkipsTheOptionalServersToo pins the other half of the skip.
//
// The startup line is on screen from the moment the servers begin connecting —
// before anything has been submitted — and it offers "esc to skip" there. An
// Escape that only reached the skipper once a turn was in flight would make
// that offer false for exactly the window it is most likely to be read in: the
// one where the user is still deciding what to type.
func TestEscapeAtIdleSkipsTheOptionalServersToo(t *testing.T) {
	session := &fakeSession{mcpSkipResult: true}
	renderer := NewRenderer(nil, nil)
	state := &streamState{sessionID: "s1"}

	if !handleIdleHotkey(context.Background(), session, renderer, state, nil, nil, hotkeyEscapeInterrupt) {
		t.Fatal("idle Escape must be handled")
	}
	if session.mcpSkipCalls != 1 {
		t.Fatalf("idle Escape consulted the MCP skipper %d times, want once", session.mcpSkipCalls)
	}

	// A subagent view is still backed out of first: Escape means "leave what I
	// am looking at" before it means anything about the servers.
	renderer.viewportMode = true
	renderer.NoteSubagentSpawned("sub-1", "explore")
	renderer.SetActiveView("sub-1")
	session = &fakeSession{mcpSkipResult: true}
	if !handleIdleHotkey(context.Background(), session, renderer, state, nil, nil, hotkeyEscapeInterrupt) {
		t.Fatal("idle Escape must be handled")
	}
	if renderer.ActiveView() != "" {
		t.Fatal("idle Escape must leave the subagent view first")
	}
	if session.mcpSkipCalls != 0 {
		t.Fatalf("an open subagent view must not reach the MCP skip (%d calls)", session.mcpSkipCalls)
	}
}

// TestMCPStatusLineKeepsItsOwnClock pins that the startup line reports the time
// the generation has actually been running.
//
// The snapshot only changes when a server does, so a line rendered once per
// status message freezes its elapsed time at whatever it read when the last
// server moved — a startup that takes thirty seconds spends them all saying
// "0s". The line is installed as a timed source instead, and the formatter is
// what the renderer evaluates on every paint.
func TestMCPStatusLineKeepsItsOwnClock(t *testing.T) {
	renderer := NewRenderer(nil, nil)
	line := &mcpStatusLine{}
	started := time.Now().Add(-90 * time.Second)
	line.apply(renderer, MCPStatusMsg{
		Snapshot: runpkg.MCPSnapshot{Servers: []mcppkg.ServerRecord{{Name: "docs", ConnStatus: mcppkg.ConnStatusConnecting}}},
		Progress: runpkg.MCPStartupProgress{Total: 1, InFlight: 1, OptionalInFlight: 1, StartedAt: started},
	})
	// The line is installed by the timed source's own goroutine, which is what
	// gives it a clock at all.
	require.Eventually(t, func() bool {
		return renderer.TransientStatusSourceLive(transientSourceMCP)
	}, 2*time.Second, 5*time.Millisecond, "a startup in flight must own the MCP status line")
	text := line.render(0, 0, "")
	if !strings.Contains(text, "mcp 0/1") || !strings.Contains(text, "docs") {
		t.Fatalf("status line = %q, want the generation's servers", text)
	}
	if !strings.Contains(text, "1m 30s") {
		t.Fatalf("status line = %q, want the generation's own elapsed time", text)
	}
	if !strings.Contains(text, "esc to skip") {
		t.Fatalf("status line = %q, want the skip offer while an optional server is in flight", text)
	}

	// The settled generation ends the line rather than leaving it on screen.
	line.apply(renderer, MCPStatusMsg{
		Snapshot: runpkg.MCPSnapshot{Servers: []mcppkg.ServerRecord{{Name: "docs", ConnStatus: mcppkg.ConnStatusConnected}}},
		Progress: runpkg.MCPStartupProgress{Total: 1, Connected: 1, StartedAt: started},
	})
	if renderer.TransientStatusSourceLive(transientSourceMCP) {
		t.Fatal("the MCP status line outlived the startup it describes")
	}
}

// --- interactive panel tests (/status, /mcp) ---

func testCtx() context.Context { return context.Background() }

// goldenStatusPanel builds the same /status report the real session assembles
// (via the shared turn builder) and renders the panel at a width.
func goldenStatusPanel(t *testing.T, width, tab int) []string {
	t.Helper()
	rep := turn.BuildStatusReport(testCtx(), turn.StatusSource{
		Version:     "v0.0.1",
		SessionName: "panel golden",
		SessionID:   "d8f83e8d-c9ff-466d-9d60-3412e0d2d282",
		Directory:   "/Users/doudou/workspace/unionj-cloud/forebrain-harness/a/very/long/path/that/has/no/spaces/to/break/at/all/anywhere/deep/deeper",
		AgentID:     "main",
		StateRoot:   t.TempDir(),
		Provider:    "deepseek",
		Model:       "deepseek-chat",
		Endpoint:    "https://api.deepseek.com",
		Permissions: turn.StatusPermissions{Preset: "Workspace", Matched: true, Rules: 3},
		Sandbox:     "workspace-write · seatbelt",
		MCP: turn.MCPInventorySource{
			Servers: []appcfg.MCPServerConfig{{Name: "a"}, {Name: "b"}},
			Runtime: &turn.MCPRuntimeView{Servers: []mcppkg.ServerRecord{
				{Name: "a", ConnStatus: mcppkg.ConnStatusConnected},
				{Name: "b", ConnStatus: mcppkg.ConnStatusError, Error: "boom"},
			}},
		},
		Instructions: []assembly.RuleSource{
			{Name: "AGENTS.md", Agent: true, Loaded: true},
			{Name: "FOREBRAIN.md", Loaded: true},
		},
		SkillOffer:   true,
		ContextUsage: func() (int, int) { return 48_000, 0 },
	})
	panel := &uiPanel{kind: "status", tab: tab, status: &rep}
	lines, focus := panelTestLines(panel, width, "⠋")
	if focus != -1 {
		t.Fatalf("status panel focus = %d, want none", focus)
	}
	return lines
}

// panelTestLines lays the panel out whole at width and returns its rows and
// the row the ❯ marks (-1 when the page has no selection).
func panelTestLines(panel *uiPanel, width int, spinner string) ([]string, int) {
	lines := buildUIPanel(panel, spinner).layout(width, 0, 0, false).lines
	focus := -1
	for i, line := range lines {
		if strings.HasPrefix(stripANSI(line), selectorCursorGlyph+" ") {
			focus = i
			break
		}
	}
	return lines, focus
}

// assertRowsFit pins the one-physical-row invariant: every panel row fits the
// width it was built for, however long the value or unbroken the token.
func assertRowsFit(t *testing.T, lines []string, width int) {
	t.Helper()
	for i, line := range lines {
		if w := displayLineWidth(line); w > width {
			t.Fatalf("row %d is %d cells wide at width %d: %q", i, w, width, stripANSI(line))
		}
	}
}

// TestStatusPanelGoldenWidths pins the panel's layout at three widths: the
// tabs, fact labels and values never disappear, a long value wraps in its own
// column — never truncated, never past the edge — and the MCP row carries the
// same counts /mcp would.
func TestStatusPanelGoldenWidths(t *testing.T) {
	for _, width := range []int{120, 80, 50} {
		lines := goldenStatusPanel(t, width, 0)
		assertRowsFit(t, lines, width)
		joined := stripANSI(strings.Join(lines, "\n"))
		// A value longer than its column folds inside it: read the rows back
		// with the folds and the alignment padding removed.
		unfolded := strings.Join(strings.Fields(joined), "")
		for _, want := range []string{"Status", "Usage", "Version:", "SessionID:", "d8f83e8d-c9ff-466d-9d60-3412e0d2d282", "Model:", "Permissions:", "1connected,1failed·/mcp", "deep/deeper", "fastoff"} {
			if !strings.Contains(unfolded, want) {
				t.Fatalf("width %d: panel missing %q:\n%s", width, want, joined)
			}
		}
		// Words fold whole; only an unbroken token wider than the column is
		// broken inside itself.
		if strings.Contains(joined, "fast o\n") {
			t.Fatalf("width %d: a word was split:\n%s", width, joined)
		}
		if strings.Contains(joined, "…") {
			t.Fatalf("width %d: a value was truncated:\n%s", width, joined)
		}
	}
}

// TestStatusPanelUsageTab pins the Usage tab: the footer's number format, the
// session input as everything sent, the hit rate, omit-empty.
func TestStatusPanelUsageTab(t *testing.T) {
	rep := &turn.StatusReport{
		SessionID: "s1",
		Context:   turn.StatusContext{PercentLeft: 62, UsedTokens: 48_000, WindowTokens: 128_000, AutoCompactAt: 102_400},
		Session:   turn.StatusUsageTotals{InputTokens: 1_901_000, OutputTokens: 42_000, CacheRead: 1_790_000, CacheWritten: 60_000, Uncached: 51_000, Requests: 38, CacheHitPercent: 94},
		LastTurn:  turn.StatusLastTurn{InputTokens: 48_000, OutputTokens: 1_200, CacheHitPercent: 91},
		Work:      turn.StatusWork{PlanSet: true, Done: 3, Total: 7},
	}
	panel := &uiPanel{kind: "status", tab: 1, status: rep}
	lines, _ := panelTestLines(panel, 120, "⠋")
	joined := strings.Join(strings.Fields(stripANSI(strings.Join(lines, "\n"))), " ")
	for _, want := range []string{
		"62% left · 48k of 128k",
		"Auto-compact at: 102k",
		"Session: 1.9M input · 42k output · 38 requests",
		"Cache hit rate: 94% · 1.79M read · 60k written · 51k uncached",
		"Last turn: 48k input · 1.2k output · 91% cached",
		"Work: plan set · 3 of 7 todos done",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("usage tab missing %q:\n%s", want, joined)
		}
	}

	empty := &uiPanel{kind: "status", tab: 1, status: &turn.StatusReport{SessionID: "s1"}}
	lines, _ = panelTestLines(empty, 80, "⠋")
	if joined := stripANSI(strings.Join(lines, "\n")); !strings.Contains(joined, "No usage recorded yet") {
		t.Fatalf("empty usage tab:\n%s", joined)
	}
}

func panelInventory(t *testing.T) *turn.MCPInventory {
	t.Helper()
	inv := turn.BuildMCPInventory(turn.MCPInventorySource{
		Servers: []appcfg.MCPServerConfig{
			{Name: "codegraph", Transport: "stdio", Command: "codegraph", Args: []string{"serve", "--mcp"}, Env: map[string]string{"CODEGRAPH_DB": "secret"}},
			{Name: "broken", Transport: "stdio", Command: "broken"},
			{Name: "must", Transport: "stdio", Command: "must", Required: true},
		},
		Disabled: []appcfg.MCPServerConfig{{Name: "paused", Transport: "stdio", Command: "paused"}},
		DisabledNext: func(srv appcfg.MCPServerConfig) bool {
			return srv.Name == "paused"
		},
		Runtime: &turn.MCPRuntimeView{Servers: []mcppkg.ServerRecord{
			{Name: "codegraph", ConnStatus: mcppkg.ConnStatusConnected},
			{Name: "broken", ConnStatus: mcppkg.ConnStatusError, Error: "exec: \"broken\": executable file not found in $PATH"},
			{Name: "must", ConnStatus: mcppkg.ConnStatusConnecting},
		}},
		Tools:        mcpToolsForPanel("codegraph"),
		GlobalSource: "/home/tester/.forebrain/forebrain.yaml",
		ErrorLogPath: "/home/tester/.forebrain/logs/error.log",
	})
	return &inv
}

// TestMCPPanelGoldenWidths pins the /mcp list at three widths: the scope
// group with its file, every state worded once, a server kept out of the
// session shown as disabled, the notes, and the selected row as the focus.
func TestMCPPanelGoldenWidths(t *testing.T) {
	panel := &uiPanel{kind: "mcp", page: "list", inv: panelInventory(t), cursor: 1}
	for _, width := range []int{120, 80, 50} {
		lines, focus := panelTestLines(panel, width, "⠋")
		assertRowsFit(t, lines, width)
		joined := stripANSI(strings.Join(lines, "\n"))
		for _, want := range []string{"Manage MCP servers", "4 servers", "Global MCPs (/home/tester/.forebrain/forebrain.yaml)", "codegraph · ✓ connected · 1 tool", "broken · ✗ failed", "⠋ connecting…", "paused · ○ disabled", "Error logs:"} {
			if !strings.Contains(strings.Join(strings.Fields(joined), " "), want) {
				t.Fatalf("width %d: mcp panel missing %q:\n%s", width, want, joined)
			}
		}
		if focus < 0 || !strings.Contains(stripANSI(lines[focus]), "❯ broken") {
			t.Fatalf("width %d: focus row %d is not the selected server:\n%s", width, focus, joined)
		}
	}
}

// TestMCPPanelDetailToolAndActions pins the drill-down: the detail facts
// (env values hidden, a failure's own text), actions per state — a required
// server offers no Disable, a kept-out server offers Enable — the tool's
// parameter table without schema JSON, and Esc returning to the row it opened.
func TestMCPPanelDetailToolAndActions(t *testing.T) {
	session := &fakePanelSession{}
	panel := &uiPanel{kind: "mcp", page: "list", inv: panelInventory(t), session: session,
		notice: map[string]string{}, auth: map[string]*panelAuthFlow{}, resources: map[string]*panelResources{}}

	panelAccept(panel) // codegraph
	if panel.page != "detail" || panel.server != "codegraph" {
		t.Fatalf("accept on list = %q/%q", panel.page, panel.server)
	}
	lines, _ := panelTestLines(panel, 80, "⠋")
	detail := stripANSI(strings.Join(lines, "\n"))
	for _, want := range []string{"codegraph MCP server", "codegraph serve --mcp", "CODEGRAPH_DB (values hidden)", "View tools", "Disable (takes effect in a new session)"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("detail missing %q:\n%s", want, detail)
		}
	}
	if strings.Contains(detail, "secret") {
		t.Fatalf("an env value leaked:\n%s", detail)
	}

	panelAccept(panel) // View tools
	panelAccept(panel) // search
	if panel.page != "tool" || panel.tool != "search" {
		t.Fatalf("drill to tool = %q/%q", panel.page, panel.tool)
	}
	lines, _ = panelTestLines(panel, 60, "⠋")
	assertRowsFit(t, lines, 60)
	tool := stripANSI(strings.Join(lines, "\n"))
	for _, want := range []string{"Parameters", "query", "string", "required", "what to look for", "depth", "one of 1 | 2"} {
		if !strings.Contains(tool, want) {
			t.Fatalf("tool page missing %q:\n%s", want, tool)
		}
	}
	if strings.Contains(tool, "{") {
		t.Fatalf("tool page must never show schema JSON:\n%s", tool)
	}
	if !panel.back() || panel.page != "tools" {
		t.Fatalf("back from tool = %q", panel.page)
	}
	if !panel.back() || panel.page != "detail" {
		t.Fatalf("back from tools = %q", panel.page)
	}
	if !panel.back() || panel.page != "list" || panel.cursor != 0 {
		t.Fatalf("back from detail = %q cursor %d", panel.page, panel.cursor)
	}
	if panel.back() {
		t.Fatal("back at the list must close the panel")
	}

	panel.page, panel.server, panel.cursor = "detail", "must", 0
	lines, _ = panelTestLines(panel, 80, "⠋")
	if detail := stripANSI(strings.Join(lines, "\n")); strings.Contains(detail, "Disable") || !strings.Contains(detail, "required by config") {
		t.Fatalf("required server detail:\n%s", detail)
	}

	panel.server = "paused"
	rows := panelSelectableRows(panel)
	if len(rows) != 1 || rows[0].action != panelEnable {
		t.Fatalf("kept-out server rows = %+v, want Enable", rows)
	}
	panelAccept(panel)
	if len(session.toggles) != 1 || session.toggles[0] != "enable paused" {
		t.Fatalf("toggles = %v", session.toggles)
	}
	if panel.notice["paused"] != "paused: enabled from next session" {
		t.Fatalf("notice = %q", panel.notice["paused"])
	}
}

// TestPanelKeysNeverReachTheComposer pins the panel as the key target: arrows
// move among selectable rows only, text events do nothing, Ctrl+C closes, and
// a page with no selection scrolls instead.
func TestPanelKeysNeverReachTheComposer(t *testing.T) {
	state := &streamState{panel: &uiPanel{kind: "mcp", page: "list", inv: panelInventory(t), session: &fakePanelSession{}}}
	hot := func(k inputHotkey) bool {
		return handleUIPanelKey(nil, state, inputEvent{kind: inputEventHotkey, hotkey: k})
	}
	if !hot(hotkeyOverlayDown) || !hot(hotkeyPanelPageDown) || state.panel.cursor != 3 {
		t.Fatalf("cursor = %d, want clamped to the last server", state.panel.cursor)
	}
	if !handleUIPanelKey(nil, state, inputEvent{kind: inputEventDraft, draft: "abc"}) || state.composer.Text != "" {
		t.Fatal("a text event must be swallowed")
	}
	if hot(hotkeyInterrupt) {
		t.Fatal("ctrl+c must close the panel")
	}
}

// TestPanelYieldsToApprovalBeforeItPaints pins the handoff order: the approval
// sink asks the loop to close the panel and waits until it is down, because
// closing the panel clears the overlay area the approval is about to use.
func TestPanelYieldsToApprovalBeforeItPaints(t *testing.T) {
	var loopSeen []any
	sink := &ApprovalSink{rawSel: &rawSelector{}}
	sink.notify = func(msg any) {
		loopSeen = append(loopSeen, msg)
		if closeMsg, ok := msg.(PanelCloseMsg); ok {
			setUIPanelActive(false)
			close(closeMsg.Done)
		}
	}
	setUIPanelActive(true)
	t.Cleanup(func() { setUIPanelActive(false) })
	// The request itself is malformed (no form payload); the handoff happens
	// before the overlay is built, and the handoff is what is observed.
	_, _ = sink.PromptToolApproval(context.Background(), turn.ToolApprovalRequest{ActionKind: "user_interaction"})
	if len(loopSeen) != 1 {
		t.Fatalf("loop saw %v, want one PanelCloseMsg", loopSeen)
	}
	state := &streamState{panel: &uiPanel{kind: "status", status: &turn.StatusReport{}}}
	setUIPanelActive(true)
	closeUIPanel(nil, state)
	if state.panel != nil || uiPanelActive.Load() {
		t.Fatal("panel must be closed and deactivated")
	}
}

// fakePanelSession records panel actions.
type fakePanelSession struct {
	toggles []string
}

func (f *fakePanelSession) PanelStatusReport(string) (turn.StatusReport, error) {
	return turn.StatusReport{}, nil
}
func (f *fakePanelSession) PanelMCPInventory() (*turn.MCPInventory, error) {
	return nil, errPanelUnavailable
}
func (f *fakePanelSession) PanelAuthenticateMCP(string)            {}
func (f *fakePanelSession) PanelListMCPResources(string)           {}
func (f *fakePanelSession) SubscribeMCPStatusTick() (func(), bool) { return nil, false }
func (f *fakePanelSession) PanelSetMCPDisabled(name string, disable bool) (string, error) {
	verb := "enable"
	if disable {
		verb = "disable"
	}
	f.toggles = append(f.toggles, verb+" "+name)
	return name + ": " + verb + "d from next session", nil
}

func mcpToolsForPanel(server string) []*llm.Tool {
	tool, err := llm.NewRawTool("mcp__"+server+"__search", "Search the code graph",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "what to look for",
				},
				"depth": map[string]any{
					"type": "integer",
					"enum": []any{1, 2},
				},
			},
			"required": []any{"query"},
		},
		func(ctx context.Context, args string) (any, error) { return "", nil },
	)
	if err != nil {
		panic(err)
	}
	return []*llm.Tool{tool}
}

// TestActiveRunKeysDriveAnOpenPanel pins the panel as the key target during a
// run too: typed text never reaches the composer, and Esc closes the panel
// instead of interrupting the run streaming above it.
func TestActiveRunKeysDriveAnOpenPanel(t *testing.T) {
	state := &streamState{panel: &uiPanel{kind: "status", status: &turn.StatusReport{}}}
	setUIPanelActive(true)
	t.Cleanup(func() { setUIPanelActive(false) })
	handleActiveRunInput(context.Background(), nil, nil, nil, state, nil, inputEvent{kind: inputEventDraft, draft: "abc", cursor: 3}, nil)
	if state.panel == nil || state.composer.DraftText != "" {
		t.Fatalf("typed text reached the composer (%q) or closed the panel", state.composer.DraftText)
	}
	handleActiveRunInput(context.Background(), nil, nil, nil, state, nil, inputEvent{kind: inputEventHotkey, hotkey: hotkeyEscapeInterrupt}, nil)
	if state.panel != nil || uiPanelActive.Load() {
		t.Fatal("Esc must close the panel")
	}
	if state.activeRunCtrlCArmed || state.activeRunCtrlCExitArmed {
		t.Fatal("closing the panel must not arm interrupting the run")
	}
}

// The footer names Plan mode for as long as the conversation is in it, however
// it got there.
func TestFooterShowsPlanModeWhileItLasts(t *testing.T) {
	root := t.TempDir()
	state := &streamState{sessionID: "s1", workspaceRoot: root}
	renderer := NewRenderer(io.Discard, io.Discard)
	renderer.SetComposerFooter(ComposerFooter{Model: "openai/gpt-5"})
	footer := func() string {
		renderer.mu.Lock()
		defer renderer.mu.Unlock()
		return renderer.composerFooterText(120)
	}

	syncPlanModeIndicator(renderer, state)
	if strings.Contains(footer(), planModeFooterLabel) {
		t.Fatalf("agent mode footer = %q", footer())
	}
	if _, err := statepkg.Switch(root, "s1", statepkg.ModePlan); err != nil {
		t.Fatal(err)
	}
	syncPlanModeIndicator(renderer, state)
	if got := footer(); !strings.Contains(got, planModeFooterLabel) || !strings.Contains(got, "openai/gpt-5") {
		t.Fatalf("plan mode footer = %q", got)
	}
	if _, err := statepkg.Switch(root, "s1", statepkg.ModeAgent); err != nil {
		t.Fatal(err)
	}
	syncPlanModeIndicator(renderer, state)
	if strings.Contains(footer(), planModeFooterLabel) {
		t.Fatalf("footer still says plan mode: %q", footer())
	}
}

// /resume lists every conversation except the one on screen, which is not
// necessarily the one with the newest message (a /new chat has none yet).
func TestResumeLeavesOutTheConversationOnScreen(t *testing.T) {
	fake := &fakeSession{recent: []SessionSummary{
		{ID: "older-with-messages", Title: "first question", UpdatedAt: 2},
		{ID: "on-screen", Title: "", UpdatedAt: 3},
	}}
	sel := &stubSelector{selectRichIdx: 0, selectRichOK: true}
	got, listed := promptRecentSession(context.Background(), fake, sel, func(context.Context) (string, error) { return "", nil }, "on-screen")
	if !listed || got != "older-with-messages" {
		t.Fatalf("picked %q (listed %v)", got, listed)
	}
	for _, item := range sel.selectRichItems {
		if strings.Contains(item.Label, "on-screen") {
			t.Fatalf("the conversation on screen was offered: %+v", sel.selectRichItems)
		}
	}
	if _, listed := promptRecentSession(context.Background(), &fakeSession{recent: []SessionSummary{{ID: "on-screen"}}}, sel, func(context.Context) (string, error) { return "", nil }, "on-screen"); listed {
		t.Fatal("a picker was offered with nothing else to resume")
	}
}
