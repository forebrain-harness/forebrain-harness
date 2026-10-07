package tui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
	"github.com/stretchr/testify/require"
)

// The withdrawal window closes at the model's first output-bearing event and at
// nothing else. Each callback the provider can reach first has to close it, and
// the events that carry no model output must leave it open — otherwise Esc
// either withdraws a submission the model has already answered, or refuses to
// withdraw one it has not.
func TestPrepareTUIAgentBaseClosesWithdrawalWindowOnFirstOutput(t *testing.T) {
	closers := map[string]func(sink *llm.StreamSink){
		"response started":                func(sink *llm.StreamSink) { sink.OnResponseStarted() },
		"assistant delta":                 func(sink *llm.StreamSink) { sink.OnDelta("hello") },
		"reasoning delta":                 func(sink *llm.StreamSink) { sink.OnReasoningDelta("thinking") },
		"provider web search":             func(sink *llm.StreamSink) { sink.OnWebSearch("ws-1", "query", false) },
		"non-streamed response completed": func(sink *llm.StreamSink) { sink.OnResponseCompleted() },
	}
	for name, closeWindow := range closers {
		t.Run(name, func(t *testing.T) {
			s, cleanup := newSurfaceTestSession(t)
			defer cleanup()
			foreground := &foregroundTurn{}
			base := context.WithValue(context.Background(), foregroundTurnKey{}, foreground)
			ctx, _, cleanupStream := s.prepareTUIAgentBase("sid", base)
			defer cleanupStream()
			sink := llm.StreamSinkFrom(ctx)
			require.NotNil(t, sink)

			closeWindow(sink)
			require.False(t, foreground.withdraw(), "Esc must fall back to ordinary cancellation once the model has produced output")
		})
	}

	// Events that carry no model output must not close it: they say the request
	// is alive, not that it has been answered.
	for name, nonOutput := range map[string]func(sink *llm.StreamSink){
		"usage delta":    func(sink *llm.StreamSink) { sink.OnUsage(120, 0) },
		"usage snapshot": func(sink *llm.StreamSink) { sink.OnUsageSnapshot(120, 0) },
		"empty delta":    func(sink *llm.StreamSink) { sink.OnDelta("") },
	} {
		t.Run(name, func(t *testing.T) {
			s, cleanup := newSurfaceTestSession(t)
			defer cleanup()
			foreground := &foregroundTurn{}
			base := context.WithValue(context.Background(), foregroundTurnKey{}, foreground)
			ctx, _, cleanupStream := s.prepareTUIAgentBase("sid", base)
			defer cleanupStream()
			sink := llm.StreamSinkFrom(ctx)
			require.NotNil(t, sink)
			if sink.OnUsage == nil || sink.OnUsageSnapshot == nil {
				t.Skip("sink does not expose usage callbacks")
			}
			nonOutput(sink)
			require.True(t, foreground.withdraw(), "a lifecycle/usage event must leave the withdrawal window open")
		})
	}
}

// A nested/internal call (compaction, an evaluator) runs under a muted sink. It
// did not receive this submission and must not acknowledge it on its behalf.
func TestMutedForegroundStreamCannotCloseTheWithdrawalWindow(t *testing.T) {
	s, cleanup := newSurfaceTestSession(t)
	defer cleanup()
	foreground := &foregroundTurn{}
	base := context.WithValue(context.Background(), foregroundTurnKey{}, foreground)
	ctx, _, cleanupStream := s.prepareTUIAgentBase("sid", base)
	defer cleanupStream()

	muted := llm.StreamSinkFrom(llm.WithMutedForegroundStream(ctx))
	require.NotNil(t, muted)
	require.Nil(t, muted.OnResponseStarted)
	require.Nil(t, muted.OnDelta)
	require.True(t, foreground.withdraw(), "a nested call must not commit the foreground submission")
}

// compactionHoldingLLM is the conversation's model. The compaction's summary
// request is sent to it as the conversation's next request, and it holds that
// request open until the request's context ends, reporting that it did.
type compactionHoldingLLM struct {
	entered   chan struct{}
	once      *sync.Once
	cancelled *atomic.Bool
}

func (m compactionHoldingLLM) Execute(ctx context.Context, msgs []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	if n := len(msgs); n > 0 && strings.Contains(msgs[n-1].TextContent(), "CONTEXT CHECKPOINT COMPACTION") {
		m.once.Do(func() { close(m.entered) })
		<-ctx.Done()
		m.cancelled.Store(true)
		return nil, ctx.Err()
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

// Esc landing inside a pre-turn auto-compaction stops it together with the
// turn it was making room for: the compaction's request is cancelled, its
// checkpoint — which commits atomically or not at all — is never written, the
// history is exactly what it was, and the submission leaves no trace.
func TestWithdrawDuringAutoCompactionCancelsItCleanly(t *testing.T) {
	entered := make(chan struct{})
	var cancelled atomic.Bool
	model := compactionHoldingLLM{entered: entered, once: &sync.Once{}, cancelled: &cancelled}

	s := newCharacterizationSession(t, model)
	sessionID := "withdraw-during-compaction"

	// Force the threshold low enough that a short seeded history crosses it.
	// CompactionService reads this config at call time.
	s.runner().AppCfg.Compact.ModelAutoCompactTokenLimit = 40

	store := s.sessStore()
	seedCtx := context.Background()
	for i := 0; i < 8; i++ {
		if _, err := store.Append(seedCtx, sessionID, "user", fmt.Sprintf("earlier question number %d with enough words to count", i)); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Append(seedCtx, sessionID, "assistant", fmt.Sprintf("earlier answer number %d with enough words to count", i)); err != nil {
			t.Fatal(err)
		}
	}
	seeded, err := store.ListRecentMessages(seedCtx, sessionID, 100)
	if err != nil {
		t.Fatal(err)
	}
	seededCount := len(seeded)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	foreground := &foregroundTurn{cancel: cancel}
	ctx = context.WithValue(ctx, foregroundTurnKey{}, foreground)

	done := make(chan error, 1)
	go func() {
		done <- s.DispatchSurfaceTurn(ctx, turn.TurnSubmission{SessionID: sessionID, UserText: "the question that triggers compaction"})
	}()
	select {
	case <-entered:
	case <-time.After(20 * time.Second):
		t.Fatal("auto-compaction never started; the threshold did not trip")
	}

	// Esc, mid-rewrite.
	if !foreground.withdraw() {
		t.Fatal("withdrawal refused while the compaction was still running")
	}

	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("dispatch returned %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("dispatch never returned")
	}
	require.True(t, cancelled.Load(), "the compaction's request outlived the withdrawal")

	// Nothing was rewritten: no checkpoint, the history as it was.
	boundaryID, _, err := store.LatestCompactBoundary(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("LatestCompactBoundary: %v", err)
	}
	if boundaryID != 0 {
		t.Fatalf("a cancelled compaction left a checkpoint behind: boundary=%d", boundaryID)
	}

	// The submission left nothing behind: it never got as far as its own row.
	rows, err := store.ListRecentMessages(context.Background(), sessionID, 200)
	if err != nil {
		t.Fatalf("ListRecentMessages: %v", err)
	}
	for _, row := range rows {
		if strings.Contains(row.Content, "the question that triggers compaction") {
			t.Fatalf("the withdrawn submission survived the compaction boundary: %+v", row)
		}
	}
	if len(rows) != seededCount {
		t.Fatalf("transcript went from %d to %d rows across a withdrawn turn", seededCount, len(rows))
	}
}

// TestPrimaryFooterBudgetIsUnchangedByTheEngineMove pins the main view's
// footer numbers across the move of the budget math into the engine. The
// turn-end notification and the startup/resume footer must keep producing
// exactly these values for a session whose last assistant response measured a
// known usage on a known model; if the move changes any of them, it changed
// the user-visible gauge, not just where the computation lives.
func TestPrimaryFooterBudgetIsUnchangedByTheEngineMove(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	store := state.NewSessionStore(db, "main")
	cfg := appcfg.Root{}
	cfg.Agents.Definitions = map[string]appcfg.AgentDefinition{"main": {
		Primary:      true,
		LLMProviders: []appcfg.AgentLLMProviderConfig{{Provider: "zhipuai", Model: "glm-5.3", APIKey: "k", BaseURL: "http://127.0.0.1:9/v1"}},
	}}
	s := sessionEnv{
		Home:      home,
		SQL:       db,
		SessStore: store,
		Config:    cfg,
		Runner:    &run.Runner{Deps: &run.Deps{Home: home, AppCfg: &cfg, SessionStore: store}},
	}.session()
	require.NoError(t, store.Ensure(ctx, "s-budget", "s-budget"))
	require.NoError(t, store.AppendMessageSequence(ctx, "s-budget", []llm.Message{
		llm.AssistantMessage([]llm.ContentPart{llm.Text("answer")}),
	}, "glm-5.3", `{"input_tokens":150000,"output_tokens":2500}`))

	// The turn-end notification: the session's occupancy, budgeted on the
	// conversation's model, no configured compact limit.
	got, ok := s.tokenBudgetMessage(ctx, "s-budget")
	require.True(t, ok)
	require.Equal(t, TokenBudgetUpdatedMsg{
		Model:                "glm-5.3",
		TokenUsage:           152500,
		PercentLeft:          83,
		ContextWindow:        1000000,
		EffectiveWindow:      1000000,
		AutoCompactThreshold: 900000,
	}, got)

	// The startup/resume footer for the same measured context, and a fresh
	// context opening on the whole window.
	require.Equal(t, ComposerTokenStats{Active: true, PercentLeft: 83, ContextWindow: 1000000}, s.SurfaceComposerTokenStats("s-budget", 152500))
	require.Equal(t, ComposerTokenStats{Active: true, PercentLeft: 100, ContextWindow: 1000000}, s.SurfaceComposerTokenStats("s-budget", 0))
}

// newAuditHookEnv builds the session shape runAuditStepHook requires — a
// runner that loaded with a main agent, a run store, and the hook installed
// process-wide for one turn of sess-B — over two sessions the gate can tell
// apart. The runner carries no event sink, so the subagent branch's canonical
// publish is unavailable and its fallback notify path is the one under test.
func newAuditHookEnv(t *testing.T) (*ChatSession, *run.Runner, *state.RunStore, string, func()) {
	t.Helper()
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	runSvc := &state.RunStore{DB: db}
	require.NoError(t, state.NewSessionStore(db, "main").Ensure(ctx, "sess-A", "sess-A"))
	require.NoError(t, state.NewSessionStore(db, "main").Ensure(ctx, "sess-B", "sess-B"))

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
	runner := &run.Runner{Deps: &run.Deps{Home: t.TempDir(), AppCfg: cfg}}
	s := sessionEnv{Home: runner.Home, RunSvc: runSvc, Runner: runner}.session()
	created, err := runSvc.CreateRun(ctx, "sess-B", "coordinate work")
	require.NoError(t, err)
	restore := s.installRunAuditStepHook("sess-B")
	return s, runner, runSvc, created.ID, restore
}

// The main agent's plan card is drawn by the hook itself (:276's direct
// notify), not through the event funnel. While the surface looks at another
// conversation the card must stay off it — the canonical event is still laid
// down in the turn's own session log.
func TestMainAgentPlanUpdateCardGatesOnViewingSession(t *testing.T) {
	ctx := context.Background()
	s, runner, runSvc, runID, restore := newAuditHookEnv(t)
	defer restore()

	var painted atomic.Int32
	s.PrependUINotify(func(msg any) {
		if _, ok := msg.(PlanUpdatedMsg); ok {
			painted.Add(1)
		}
	})
	hook := runner.Tools().StepHook()
	require.NotNil(t, hook)
	plan := func(title string) *event.PlanUpdatedPayload {
		return &event.PlanUpdatedPayload{
			Title: title,
			Items: []event.PlanUpdateItem{{ID: "1", Content: "Inspect code", Status: "in_progress"}},
			Total: 1,
		}
	}

	// The turn runs in sess-B while the surface looks at sess-A: no card.
	s.SetViewingSession("sess-A")
	hook(tool.WithRunID(llm.WithAgentSessionID(ctx, "sess-B"), runID), tool.StepEvent{
		Kind:       event.RunEventPlanUpdated,
		ToolName:   "session_todo",
		PlanUpdate: plan("Foreign Plan"),
	})
	waitForQueuedNotifications(t, s)
	if painted.Load() != 0 {
		t.Fatalf("another conversation's plan card painted %d message(s)", painted.Load())
	}
	// The canonical event still landed in the run's own session log.
	events, err := runSvc.ListRunEvents(ctx, runID, 10)
	require.NoError(t, err)
	require.Len(t, events, 1, "the plan update must still be persisted to its own session")

	// Looking back at the turn's conversation, the card paints.
	s.SetViewingSession("sess-B")
	hook(tool.WithRunID(llm.WithAgentSessionID(ctx, "sess-B"), runID), tool.StepEvent{
		Kind:       event.RunEventPlanUpdated,
		StepID:     "todo-2",
		ToolName:   "session_todo",
		PlanUpdate: plan("Own Plan"),
	})
	waitForQueuedNotifications(t, s)
	if painted.Load() != 1 {
		t.Fatalf("the viewed conversation's plan card must paint once, got %d", painted.Load())
	}
}

// A subagent's steps reach their view through the canonical event stream; the
// direct notify below it is the fallback for a runner with no event sink. It
// draws subagent plan cards and tool cards, so it is gated the same way as
// every other session-carrying funnel.
func TestSubagentStepFallbackGatesOnViewingSession(t *testing.T) {
	ctx := context.Background()
	s, runner, _, runID, restore := newAuditHookEnv(t)
	defer restore()
	require.Nil(t, runner.Events, "the fallback path needs a runner with no event sink")

	var painted atomic.Int32
	s.PrependUINotify(func(msg any) { painted.Add(1) })
	hook := runner.Tools().StepHook()
	require.NotNil(t, hook)
	subagentCtx := tool.WithHookAgentID(tool.WithRunID(llm.WithAgentSessionID(ctx, "sess-B"), runID), "task-42")

	s.SetViewingSession("sess-A")
	hook(subagentCtx, tool.StepEvent{
		Kind:       event.RunEventPlanUpdated,
		ToolName:   "session_todo",
		PlanUpdate: &event.PlanUpdatedPayload{Title: "Subagent Plan", Total: 1},
	})
	waitForQueuedNotifications(t, s)
	if painted.Load() != 0 {
		t.Fatalf("another conversation's subagent plan card painted %d message(s)", painted.Load())
	}
	hook(subagentCtx, tool.StepEvent{
		Kind:     event.RunEventToolCompleted,
		StepID:   "call-1",
		ToolName: "read_file",
		Input:    map[string]any{"file_path": "/tmp/a.go"},
		Output:   map[string]any{"preview_text": "package main\n"},
	})
	waitForQueuedNotifications(t, s)
	if painted.Load() != 0 {
		t.Fatalf("another conversation's subagent tool card painted %d message(s)", painted.Load())
	}

	s.SetViewingSession("sess-B")
	hook(subagentCtx, tool.StepEvent{
		Kind:       event.RunEventPlanUpdated,
		StepID:     "todo-2",
		ToolName:   "session_todo",
		PlanUpdate: &event.PlanUpdatedPayload{Title: "Subagent Plan 2", Total: 1},
	})
	waitForQueuedNotifications(t, s)
	if painted.Load() != 1 {
		t.Fatalf("the viewed conversation's subagent plan card must paint once, got %d", painted.Load())
	}
	hook(subagentCtx, tool.StepEvent{
		Kind:     event.RunEventToolCompleted,
		StepID:   "call-2",
		ToolName: "read_file",
		Input:    map[string]any{"file_path": "/tmp/b.go"},
		Output:   map[string]any{"preview_text": "package b\n"},
	})
	waitForQueuedNotifications(t, s)
	if painted.Load() != 2 {
		t.Fatalf("the viewed conversation's subagent tool card must paint once more, got %d", painted.Load())
	}
}

// The main agent's live stream is drawn by the sink prepareTUIAgentBase
// installs — its deltas never travel the run-event funnel (RunEventAssistantDelta
// has no main-agent producer). A foreground turn the surface switched away from
// (/resume mid-run) keeps running with that sink, so every callback has to gate
// on the conversation being looked at. What the gate holds back was never
// persisted live anyway: PartialSessionCapture lands at turn end, so switching
// back replays exactly what it always replayed.
func TestForeignStreamSinkDeltasGateOnViewingSession(t *testing.T) {
	s, cleanup := newSurfaceTestSession(t)
	defer cleanup()
	store := s.sessStore()
	require.NoError(t, store.Ensure(context.Background(), "sess-A", "sess-A"))
	require.NoError(t, store.Ensure(context.Background(), "sess-B", "sess-B"))
	var painted atomic.Int32
	s.PrependUINotify(func(msg any) { painted.Add(1) })

	sinkFor := func(sessionID string) *llm.StreamSink {
		t.Helper()
		ctx, _, cleanupStream := s.prepareTUIAgentBase(sessionID, context.Background())
		t.Cleanup(cleanupStream)
		sink := llm.StreamSinkFrom(ctx)
		require.NotNil(t, sink)
		return sink
	}

	// The turn runs in sess-B while the surface looks at sess-A: none of the
	// sink's callbacks may paint. OnUsageSnapshot is asserted one-way (the
	// budget message itself needs a runner these fixtures do not build).
	s.SetViewingSession("sess-A")
	foreign := sinkFor("sess-B")
	foreign.OnDelta("hello")
	foreign.OnReasoningDelta("thinking")
	foreign.OnReasoningDone()
	foreign.OnWebSearch("ws-1", "query", false)
	foreign.OnUsage(120, 30)
	foreign.OnUsageSnapshot(200, 40)
	waitForQueuedNotifications(t, s)
	if painted.Load() != 0 {
		t.Fatalf("another conversation's live stream painted %d message(s)", painted.Load())
	}

	// The viewed conversation's own stream paints.
	own := sinkFor("sess-A")
	own.OnDelta("hello")
	waitForQueuedNotifications(t, s)
	if painted.Load() != 1 {
		t.Fatalf("the viewed conversation's assistant delta must paint once, got %d", painted.Load())
	}
	own.OnReasoningDelta("thinking")
	own.OnReasoningDone()
	own.OnWebSearch("ws-2", "query", true)
	own.OnUsage(10, 5)
	waitForQueuedNotifications(t, s)
	if painted.Load() != 5 {
		t.Fatalf("the viewed conversation's stream callbacks must all paint, got %d", painted.Load())
	}
}

// A non-streaming provider's final assistant message (and its reasoning) is
// drawn by appendAssistantOutcome directly. It gates the same way: the outcome
// is persisted to the turn's own session regardless, so switching back to that
// conversation replays it exactly as before.
func TestAppendAssistantOutcomeGatesOnViewingSession(t *testing.T) {
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	store := state.NewSessionStore(db, "main")
	require.NoError(t, store.Ensure(ctx, "sess-A", "sess-A"))
	require.NoError(t, store.Ensure(ctx, "sess-B", "sess-B"))
	s := sessionEnv{Home: t.TempDir(), SQL: db, SessStore: store}.session()
	// The rows a turn outcome writes are bound to its run, and fb_messages
	// carries that foreign key — so the run has to exist for real.
	runSvc := &state.RunStore{DB: db}
	created, err := runSvc.CreateRun(ctx, "sess-B", "do the work")
	require.NoError(t, err)
	var painted atomic.Int32
	s.PrependUINotify(func(msg any) { painted.Add(1) })

	var persisted func() int
	{
		var n int
		require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM fb_messages WHERE session_id='sess-B'`).Scan(&n))
		persisted = func() int {
			var count int
			require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM fb_messages WHERE session_id='sess-B'`).Scan(&count))
			return count
		}
		before := persisted()
		require.Zero(t, before, "the fixture must start with no messages in sess-B")
	}

	outcome := func(sessionID string) {
		t.Helper()
		painted.Store(0)
		s.appendAssistantOutcome(sessionID, created.ID, "tui", "do the work", completeTurn(time.Now().Add(-2*time.Second)),
			&agent.Result{
				Parts:     []llm.ContentPart{llm.Text("final answer")},
				Reasoning: "thinking it through",
			}, false, nil)
		waitForQueuedNotifications(t, s)
	}

	// The turn finished in sess-B while the surface looks at sess-A: nothing
	// paints, but the outcome is still persisted to sess-B's transcript.
	s.SetViewingSession("sess-A")
	outcome("sess-B")
	if painted.Load() != 0 {
		t.Fatalf("another conversation's final assistant message painted %d message(s)", painted.Load())
	}
	require.NotZero(t, persisted(), "the outcome must still be persisted to its own session")

	// The viewed conversation's outcome paints: reasoning, its done marker and
	// the final assistant message.
	outcome("sess-A")
	if painted.Load() != 3 {
		t.Fatalf("the viewed conversation's outcome must paint three messages, got %d", painted.Load())
	}
}
