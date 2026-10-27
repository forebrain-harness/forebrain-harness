package tui

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/stretchr/testify/require"
)

// turnLifecycleRig is the shared fixture: an isolated state DB, one started
// conversation, one run, and a session bound to the same store the gateway
// reads. The run carries a two-item checklist with one item done and none of it
// attributed to a subagent, which is what the worked line reports.
func turnLifecycleRig(t *testing.T) (context.Context, *ChatSession, *state.RunStore, string) {
	t.Helper()
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, state.NewSessionStore(db, "main").Ensure(ctx, "sid", "sid"))

	runSvc := &state.RunStore{DB: db, Owner: "tui-test"}
	run, err := runSvc.CreateRun(ctx, "sid", "do the work")
	require.NoError(t, err)

	s := sessionEnv{Home: t.TempDir(), SQL: db, RunSvc: runSvc}.session()
	s.persistRunEvent(ctx, event.NewRunEvent("", run.ID, "sid", event.RunEventPlanUpdated, event.PlanUpdatedPayload{
		Items: []event.PlanUpdateItem{
			{ID: "1", Content: "done item", Status: "completed"},
			{ID: "2", Content: "next", Status: "in_progress"},
		},
		Completed: 1,
		Total:     2,
	}, time.Now()))
	return ctx, s, runSvc, run.ID
}

// turnLifecycleEvents reads the conversation's log the way a surface pages it.
func turnLifecycleEvents(t *testing.T, runs *state.RunStore, sessionID string) []state.SessionEvent {
	t.Helper()
	page, err := runs.ListSessionEvents(context.Background(), sessionID, 0, 0, 20)
	require.NoError(t, err)
	return page.Events
}

func turnLifecycleEventsOfType(events []state.SessionEvent, eventType string) []state.SessionEvent {
	var out []state.SessionEvent
	for _, evt := range events {
		if evt.Type == eventType {
			out = append(out, evt)
		}
	}
	return out
}

// TestTurnLifecycleIsRecordedLikeTheGateway pins the two records a main-agent
// turn leaves in the shared log: the start, written once even though a
// continuation past an approval re-observes the same run id, and the end, which
// carries the worked line's clock and the run's checklist facts and never a
// copy of the answer.
func TestTurnLifecycleIsRecordedLikeTheGateway(t *testing.T) {
	ctx, s, runSvc, runID := turnLifecycleRig(t)

	// The start is written at run start; a continuation past an approval reuses
	// the run id, so the same fixed id must not record a second start.
	start := func() {
		s.persistRunEvent(ctx, event.NewRunEvent("turn-started:"+runID, runID, "sid", event.RunEventTurnStarted, event.TurnStartedPayload{}, time.Now()))
	}
	start()
	start()

	// The end goes through the same constructor the surface uses on a finished
	// turn, so this pins what appendAssistantOutcome writes.
	s.persistRunEvent(ctx, s.turnCompletedEvent("sid", runID, 1200*time.Millisecond))

	events := turnLifecycleEvents(t, runSvc, "sid")
	require.Len(t, turnLifecycleEventsOfType(events, event.RunEventTurnStarted), 1,
		"turn_started must be recorded exactly once for the run")

	completed := turnLifecycleEventsOfType(events, event.RunEventTurnCompleted)
	require.Len(t, completed, 1)
	var payload event.TurnCompletedPayload
	require.NoError(t, json.Unmarshal(completed[0].Payload, &payload))
	require.Equal(t, int64(1200), payload.ElapsedMS)
	require.Equal(t, 1, payload.PlanDone)
	require.Equal(t, 2, payload.PlanTotal)
	require.Empty(t, payload.Text, "the answer text belongs to the transcript, not the lifecycle event")
	require.NotContains(t, string(completed[0].Payload), `"text"`,
		"a turn with no answer text must not serialise an empty text field")
}

// TestCancelledAndFailedTurnsCarryPlanFacts pins that the two ways a run can
// end without completing still name the checklist: the cancelled record the
// surface publishes and the error record it persists both carry the same facts
// the completed turn does.
// TestTUIStateEventsAreRecordedLikeTheGateway pins the other main-agent facts
// the terminal now files alongside its cards: a delivered steer and a mode
// switch. None of them draws anything itself — the terminal still paints from
// its own messages — but each is what a reload reads, so the conversation
// reads the same on either surface. (The turn-end context budget is the
// engine's own record now; pkg/process files it once per turn.)
func TestTUIStateEventsAreRecordedLikeTheGateway(t *testing.T) {
	_, s, runSvc, runID := turnLifecycleRig(t)
	s.noteTUIRunID(runID)
	require.Equal(t, runID, s.currentTUIRunID())

	// A steer the model has received, filed where the model got it.
	s.recordDeliveredSteers("sid", []ComposerSubmission{{DisplayText: "also run lint"}})

	events := turnLifecycleEvents(t, runSvc, "sid")

	delivered := turnLifecycleEventsOfType(events, event.RunEventInputDelivered)
	require.Len(t, delivered, 1)
	require.Equal(t, runID, delivered[0].RunID)
	var input event.InputDeliveredPayload
	require.NoError(t, json.Unmarshal(delivered[0].Payload, &input))
	require.Equal(t, "also run lint", input.Text)
}

// TestTUIPrimaryBudgetIsNotFiledPerResponse pins the unified contract's
// primary half: OnUsageSnapshot is a live gauge a provider may fire once per
// chunk, so the terminal refreshes the footer from each snapshot but files
// nothing — the durable budget is the one measured record at turn end, filed
// by the engine (run.PublishContextBudget), and a reload recomputes the
// footer from persisted usage rows.
func TestTUIPrimaryBudgetIsNotFiledPerResponse(t *testing.T) {
	ctx, s, runSvc, runID := turnLifecycleRig(t)
	s.noteTUIRunID(runID)

	budgetCh := make(chan TokenBudgetUpdatedMsg, 4)
	s.PrependUINotify(func(msg any) {
		if m, ok := msg.(TokenBudgetUpdatedMsg); ok {
			budgetCh <- m
		}
	})

	agentCtx, _, cleanupStream := s.prepareTUIAgentBase("sid", ctx)
	defer cleanupStream()
	sink := llm.StreamSinkFrom(agentCtx)
	require.NotNil(t, sink)
	require.NotNil(t, sink.OnUsageSnapshot)

	// A compatible stream carrying its cumulative usage on every chunk: three
	// snapshots, one response, then the request's end boundaries.
	sink.OnUsageSnapshot(1000, 10)
	sink.OnUsageSnapshot(1000, 20)
	sink.OnUsageSnapshot(1000, 30)
	if sink.OnResponseCompleted != nil {
		sink.OnResponseCompleted()
	}
	if sink.OnEnd != nil {
		sink.OnEnd()
	}

	// The footer still follows every snapshot live.
	for i, want := range []int{1010, 1020, 1030} {
		m := requireUIMessage(t, budgetCh)
		require.Equal(t, want, m.TokenUsage, "footer message %d", i)
	}

	// Nothing is filed: no per-response budget row exists at all.
	events := turnLifecycleEvents(t, runSvc, "sid")
	require.Empty(t, turnLifecycleEventsOfType(events, event.CompactEventBudgetUpdated),
		"a mid-response snapshot must not file a durable budget row")
}

// TestTUIQueuePreviewIsRecordedLikeTheGateway files the conversation queue's
// preview through the same change hook the surface uses: a follow-up queued
// while a turn runs reaches the log as pending_input_updated, so a reload
// shows the same waiting message the footer already does.
func TestTUIQueuePreviewIsRecordedLikeTheGateway(t *testing.T) {
	_, s, runSvc, runID := turnLifecycleRig(t)
	s.noteTUIRunID(runID)
	// Installing the turn's input runtime is what arms the queue's change hook.
	require.NotNil(t, s.ensureTUITurnInputRuntime("sid"))
	q := s.SurfaceInputQueue("sid")
	require.NotNil(t, q)
	require.True(t, q.FollowUp(run.Input{Text: "queued next"}))

	events := turnLifecycleEvents(t, runSvc, "sid")
	previews := turnLifecycleEventsOfType(events, event.RunEventPendingInputUpdated)
	require.NotEmpty(t, previews)
	var preview event.PendingInputUpdatedPayload
	require.NoError(t, json.Unmarshal(previews[len(previews)-1].Payload, &preview))
	require.Equal(t, []string{"queued next"}, preview.QueuedMessages)
}

// TestTUIModeSwitchIsRecordedLikeTheGateway files a mode switch the way the
// gateway does, and only for a switch: the first look at a conversation adopts
// its mode silently, and the record carries no run because a keyboard switch
// happens with none in flight.
func TestTUIModeSwitchIsRecordedLikeTheGateway(t *testing.T) {
	_, s, runSvc, _ := turnLifecycleRig(t)
	view := &streamState{sessionID: "sid", session: s}

	// First look: adopt agent mode without filing anything.
	view.recordModeChange(state.State{Mode: state.ModeAgent})
	// The switch to plan mode is filed; a second look at the same mode is not.
	view.recordModeChange(state.State{Mode: state.ModePlan, Phase: "plan"})
	view.recordModeChange(state.State{Mode: state.ModePlan, Phase: "plan"})

	events := turnLifecycleEvents(t, runSvc, "sid")
	modes := turnLifecycleEventsOfType(events, event.RunEventModeChanged)
	require.Len(t, modes, 1, "only the actual switch is recorded")
	require.Empty(t, modes[0].RunID, "a keyboard switch belongs to no run")
	var mode event.ModeChangedPayload
	require.NoError(t, json.Unmarshal(modes[0].Payload, &mode))
	require.Equal(t, "plan", mode.Mode)
	require.Equal(t, "plan", mode.Phase)
}

// TestCancelledAndFailedTurnsCarryPlanFacts pins that the two ways a run can
// end without completing still name the checklist: the cancelled record the
// surface publishes and the error record it persists both carry the same facts
// the completed turn does.
func TestCancelledAndFailedTurnsCarryPlanFacts(t *testing.T) {
	ctx, s, runSvc, runID := turnLifecycleRig(t)

	require.NoError(t, s.publishTUIRunEvent(ctx, "sid", runID, event.RunEventTurnCancelled, event.TurnCancelledPayload{Message: "cancelled"}))
	s.persistRunTurnError("sid", runID, "boom")

	events := turnLifecycleEvents(t, runSvc, "sid")

	cancelled := turnLifecycleEventsOfType(events, event.RunEventTurnCancelled)
	require.Len(t, cancelled, 1)
	var cancelledPayload event.TurnCancelledPayload
	require.NoError(t, json.Unmarshal(cancelled[0].Payload, &cancelledPayload))
	require.Equal(t, "cancelled", cancelledPayload.Message)
	require.Equal(t, 1, cancelledPayload.PlanDone)
	require.Equal(t, 2, cancelledPayload.PlanTotal)

	failed := turnLifecycleEventsOfType(events, event.RunEventTurnError)
	require.Len(t, failed, 1)
	var errorPayload event.TurnErrorPayload
	require.NoError(t, json.Unmarshal(failed[0].Payload, &errorPayload))
	require.Equal(t, "boom", errorPayload.Error)
	require.Equal(t, 1, errorPayload.PlanDone)
	require.Equal(t, 2, errorPayload.PlanTotal)
}
