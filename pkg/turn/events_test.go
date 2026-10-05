package turn

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

func TestExtractTurnDiffPayloadIncludesStatsAndHunks(t *testing.T) {
	payload, ok := ExtractTurnDiffPayload(map[string]any{
		"kind":      event.RunEventToolCompleted,
		"step_id":   "write_file-1",
		"tool_name": "write_file",
		"output": map[string]any{
			"status": "ok",
			"turn_diff": map[string]any{
				"path":         "/tmp/demo.txt",
				"added":        1,
				"deleted":      1,
				"unified_diff": "--- a\n+++ b\n@@ -1 +1 @@\n-old\n+new\n",
			},
		},
	})
	require.True(t, ok)
	require.Len(t, payload.Files, 1)
	f := payload.Files[0]
	require.Equal(t, "/tmp/demo.txt", f.Path)
	require.Equal(t, 1, f.Added)
	require.Equal(t, 1, f.Deleted)
	require.NotEmpty(t, payload.Summary)
	require.NotEmpty(t, f.Hunks)
	var hasAdd, hasDel bool
	for _, l := range f.Hunks[0].Lines {
		switch l.Kind {
		case event.DiffLineAdd:
			hasAdd = true
		case event.DiffLineDelete:
			hasDel = true
		}
	}
	require.True(t, hasAdd && hasDel)
}

func TestExtractTurnDiffPayloadHasNoRawDiffKey(t *testing.T) {
	payload, ok := ExtractTurnDiffPayload(map[string]any{
		"output": map[string]any{
			"turn_diff": map[string]any{
				"path":         "file.go",
				"added":        2,
				"deleted":      1,
				"unified_diff": "diff --git a/file.go b/file.go\nindex a..b 100644\n--- a/file.go\n+++ b/file.go\n@@ -1,3 +1,4 @@\n ctx\n-old\n+new\n+extra\n",
			},
		},
	})
	require.True(t, ok)
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	_, present := decoded["diff"]
	require.False(t, present, "raw diff string must not appear in payload")
}

func TestExtractTurnDiffPayloadRejectsPayloadWithoutTurnDiff(t *testing.T) {
	_, ok := ExtractTurnDiffPayload(map[string]any{"output": map[string]any{"status": "ok"}})
	require.False(t, ok)
}

type stubRunEventStore struct {
	run    *state.Run
	events []state.SessionEvent
}

func (s stubRunEventStore) GetRun(ctx context.Context, id string) (*state.Run, error) {
	return s.run, nil
}

func (s stubRunEventStore) ListRunEvents(ctx context.Context, runID string, limit int) ([]state.SessionEvent, error) {
	if limit <= 0 || limit >= len(s.events) {
		return append([]state.SessionEvent(nil), s.events...), nil
	}
	return append([]state.SessionEvent(nil), s.events[:limit]...), nil
}

func (s stubRunEventStore) ListRunsBySession(ctx context.Context, sessionID string, limit int) ([]state.Run, error) {
	if s.run == nil || s.run.SessionID != sessionID {
		return nil, nil
	}
	return []state.Run{*s.run}, nil
}

// The run events API reads the durable log, not a projection of a step ledger:
// every stored row of the run reaches the wire in order, stamped with the
// current schema version the row no longer carries.
func TestListRunEventsReturnsStoredEventsInOrder(t *testing.T) {
	svc := New(WithRunEventStore(stubRunEventStore{
		run: &state.Run{ID: "r1", SessionID: "s1"},
		events: []state.SessionEvent{
			{ID: "evt-1", Sequence: 1, RunID: "r1", SessionID: "s1", Type: event.RunEventToolStarted, Payload: json.RawMessage(`{"tool_name":"Bash"}`)},
			{ID: "evt-2", Sequence: 2, RunID: "r1", SessionID: "s1", Type: event.RunEventAssistantDelta, Payload: json.RawMessage(`{"text":"hello"}`)},
			{ID: "evt-3", Sequence: 3, RunID: "r1", SessionID: "s1", Type: event.RunEventApprovalReq, Payload: json.RawMessage(`{"action_id":"a1"}`)},
		},
	}))

	events, err := svc.ListRunEvents(context.Background(), "r1", 100)
	require.NoError(t, err)
	require.Len(t, events, 3)
	require.Equal(t, event.RunEventToolStarted, events[0].Type)
	require.Equal(t, event.RunEventAssistantDelta, events[1].Type)
	require.Equal(t, event.RunEventApprovalReq, events[2].Type)
	require.Equal(t, "s1", events[0].SessionID)
	require.Equal(t, "r1", events[0].RunID)
	require.Equal(t, event.RunEventSchemaVersion, events[0].SchemaVersion)
	require.EqualValues(t, 2, events[1].Sequence)
}

// --- Abandoned-run reaping ---------------------------------------------------

// TestAbandonedRunReaperReportsEachEndingOnce seeds one expired primary run
// and one expired child run with a spawned event on record, and pins what
// ReapOnce publishes: a turn_error for the primary, a subagent_ended carrying
// the spawned event's facts plus the stamped finish, and nothing at all on a
// second pass.
func TestAbandonedRunReaperReportsEachEndingOnce(t *testing.T) {
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	sessions := state.NewSessionStore(db, "main")
	require.NoError(t, sessions.Ensure(ctx, "s1", "s1"))
	require.NoError(t, sessions.Ensure(ctx, "s2", "s2"))
	dead := &state.RunStore{DB: db, Owner: "owner-dead"}
	stop, err := dead.HoldOwnerLease(ctx)
	require.NoError(t, err)
	primary, err := dead.CreateRun(ctx, "s1", "a turn the process never finished")
	require.NoError(t, err)
	// The abandoned subagent lives in its own conversation: a parent and its
	// children share a session, and no session holds two primary runs.
	parent, err := dead.CreateRun(ctx, "s2", "parent")
	require.NoError(t, err)
	child, err := dead.CreateSubagentRun(ctx, parent.ID, "s2", "child task")
	require.NoError(t, err)
	spawned := event.SubagentSpawnedPayload{
		AgentID: "agent-7", AgentType: "general-purpose", TaskID: "agent-7",
		WorkerSessionID: "worker-7", ParentRunID: parent.ID, ParentToolCallID: "call-9",
		TaskIndex: 2, ExecutionID: child.ID,
	}
	_, _, err = dead.AppendSessionEventOnce(ctx, state.SessionEvent{
		RunID: child.ID, SessionID: "s2", Type: event.RunEventSubagentSpawned,
		Payload: mustMarshalTurnEvent(t, spawned), CreatedAt: time.Now().UTC(),
	})
	require.NoError(t, err)
	// The owner dies: its lease row goes away with it.
	stop()

	type published struct {
		sessionID, runID, eventType string
		payload                     any
	}
	var got []published
	recoveries := 0
	mapEventsByRun := func(items []published) map[string]string {
		out := map[string]string{}
		for _, p := range items {
			out[p.runID] = p.eventType
		}
		return out
	}
	reaper := AbandonedRunReaper{
		Runs: dead,
		Publish: func(ctx context.Context, sessionID, runID, eventType string, payload any) error {
			got = append(got, published{sessionID, runID, eventType, payload})
			return nil
		},
		Recover: func(context.Context) { recoveries++ },
	}
	reaper.ReapOnce(ctx)
	// The dead process left three runs: the primary in s1, and in s2 a parent
	// (itself a primary run of that conversation) and its child.
	require.Len(t, got, 3)
	require.Equal(t, recoveries, 1)

	var turnErr *event.TurnErrorPayload
	var ended *event.SubagentEndedPayload
	for _, p := range got {
		switch p.eventType {
		case event.RunEventTurnError:
			if tp, ok := p.payload.(event.TurnErrorPayload); ok {
				turnErr = &tp
			}
		case event.RunEventSubagentEnded:
			if ep, ok := p.payload.(event.SubagentEndedPayload); ok {
				ended = &ep
			}
		}
	}
	require.NotNil(t, turnErr, "the primary run's ending must be published: %+v", got)
	require.Equal(t, map[string]string{
		primary.ID: event.RunEventTurnError,
		parent.ID:  event.RunEventTurnError,
		child.ID:   event.RunEventSubagentEnded,
	}, mapEventsByRun(got))
	require.Equal(t, AbandonedRunReason, turnErr.Error)
	require.NotNil(t, turnErr.Detail)
	require.Equal(t, "run_abandoned", turnErr.Detail.Code)

	require.NotNil(t, ended, "the child run's ending must be published: %+v", got)
	require.Equal(t, spawned.AgentID, ended.AgentID)
	require.Equal(t, spawned.AgentType, ended.AgentType)
	require.Equal(t, spawned.TaskID, ended.TaskID)
	require.Equal(t, spawned.WorkerSessionID, ended.WorkerSessionID)
	require.Equal(t, spawned.ParentRunID, ended.ParentRunID)
	require.Equal(t, spawned.ParentToolCallID, ended.ParentToolCallID)
	require.Equal(t, spawned.TaskIndex, ended.TaskIndex)
	require.Equal(t, spawned.ExecutionID, ended.ExecutionID)
	require.Equal(t, "failed", ended.Status)
	require.Equal(t, AbandonedRunReason, ended.Error)
	// The finish is the dead owner's last heartbeat window, recorded on the
	// run the reap stamped — not the moment this process noticed.
	var finishedAt int64
	require.NoError(t, db.QueryRow(`SELECT finished_at_ms FROM fb_runs WHERE id=?`, child.ID).Scan(&finishedAt))
	require.NotZero(t, finishedAt)
	require.Equal(t, finishedAt, ended.FinishedAtMs)

	got = nil
	reaper.ReapOnce(ctx)
	require.Empty(t, got, "a reaped run is reported exactly once")
}

func mustMarshalTurnEvent(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return json.RawMessage(b)
}
