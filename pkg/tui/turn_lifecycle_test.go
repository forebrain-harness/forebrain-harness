package tui

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
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
