package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/stretchr/testify/require"
)

func TestSessionEventsAreIdempotentOrderedAndPageAtFixedHighWater(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	store := &RunStore{DB: db}
	ensureSessionsForRuns(t, db, "session-1")
	for _, runID := range []string{"run-parent", "run-child"} {
		_, err = db.ExecContext(ctx, `INSERT INTO fb_runs(id, session_id, input_text, status, created_at, updated_at) VALUES(?,'session-1','',?,0,0)`, runID, string(RunStatusRunning))
		require.NoError(t, err)
	}

	firstEvent := event.NewRunEvent("evt-1", "run-parent", "session-1", event.RunEventSubagentSpawned,
		event.SubagentSpawnedPayload{AgentID: "agent-1", Task: "inspect"}, time.Unix(10, 0))
	first := SessionEvent{ID: firstEvent.ID, RunID: firstEvent.RunID, SessionID: firstEvent.SessionID, Type: firstEvent.Type, Payload: firstEvent.Payload, CreatedAt: firstEvent.CreatedAt}
	written, err := store.AppendSessionEvent(ctx, first)
	require.NoError(t, err)
	require.EqualValues(t, 1, written.Sequence)

	duplicate := first
	duplicate.Payload = event.EncodePayload(event.SubagentSpawnedPayload{AgentID: "wrong"})
	writtenAgain, err := store.AppendSessionEvent(ctx, duplicate)
	require.NoError(t, err)
	require.Equal(t, written.Sequence, writtenAgain.Sequence)
	require.JSONEq(t, string(written.Payload), string(writtenAgain.Payload))

	secondEvent := event.NewRunEvent("evt-2", "run-child", "session-1", event.RunEventAssistantDelta,
		event.AssistantDeltaPayload{AgentID: "agent-1", Text: "answer"}, time.Unix(11, 0))
	_, err = store.AppendSessionEvent(ctx, SessionEvent{ID: secondEvent.ID, RunID: secondEvent.RunID, SessionID: secondEvent.SessionID, Type: secondEvent.Type, Payload: secondEvent.Payload, CreatedAt: secondEvent.CreatedAt})
	require.NoError(t, err)
	high, err := store.SessionEventHighWater(ctx, "session-1")
	require.NoError(t, err)
	require.Greater(t, high, written.Sequence)

	thirdEvent := event.NewRunEvent("evt-3", "run-child", "session-1", event.RunEventSubagentEnded,
		event.SubagentEndedPayload{AgentID: "agent-1", Status: "ok"}, time.Unix(12, 0))
	_, err = store.AppendSessionEvent(ctx, SessionEvent{ID: thirdEvent.ID, RunID: thirdEvent.RunID, SessionID: thirdEvent.SessionID, Type: thirdEvent.Type, Payload: thirdEvent.Payload, CreatedAt: thirdEvent.CreatedAt})
	require.NoError(t, err)

	page1, err := store.ListSessionEvents(ctx, "session-1", 0, high, 1)
	require.NoError(t, err)
	require.True(t, page1.HasMore)
	require.Len(t, page1.Events, 1)
	require.EqualValues(t, 1, page1.NextCursor)

	page2, err := store.ListSessionEvents(ctx, "session-1", page1.NextCursor, high, 1)
	require.NoError(t, err)
	require.False(t, page2.HasMore)
	require.Len(t, page2.Events, 1)
	require.Equal(t, "evt-2", page2.Events[0].ID)
	require.EqualValues(t, high, page2.HighWater)
}

func TestSessionEventsPageCompletelyPastLegacyCutoffs(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	store := &RunStore{DB: db}
	ensureSessionsForRuns(t, db, "large-session")
	run, err := store.CreateRun(ctx, "large-session", "bulk")
	require.NoError(t, err)

	const total = 100001
	_, err = db.ExecContext(ctx, `
WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < ?)
INSERT INTO fb_session_events(session_id,event_id,run_id,event_type,payload_json,occurred_at_ms)
SELECT 'large-session',printf('bulk-event-%05d',i),?,'assistant_delta','{}',i FROM n`, total, run.ID)
	require.NoError(t, err)
	highWater, err := store.SessionEventHighWater(ctx, "large-session")
	require.NoError(t, err)

	// This event is deliberately outside the snapshot. Paging at highWater must
	// neither include it nor lose/duplicate anything that was part of the
	// original snapshot.
	_, err = store.AppendSessionEvent(ctx, SessionEvent{
		ID:        "event-after-high-water",
		SessionID: "large-session",
		RunID:     run.ID,
		Type:      "turn_completed",
	})
	require.NoError(t, err)

	seen := make(map[string]struct{}, total)
	var cursor int64
	var previousSequence int64
	for {
		page, listErr := store.ListSessionEvents(ctx, "large-session", cursor, highWater, 777)
		require.NoError(t, listErr)
		require.Equal(t, highWater, page.HighWater)
		for _, stored := range page.Events {
			require.Greater(t, stored.Sequence, previousSequence)
			require.LessOrEqual(t, stored.Sequence, highWater)
			_, duplicate := seen[stored.ID]
			require.False(t, duplicate, "duplicate event %s", stored.ID)
			seen[stored.ID] = struct{}{}
			previousSequence = stored.Sequence
		}
		cursor = page.NextCursor
		if !page.HasMore {
			break
		}
	}

	require.Len(t, seen, total)
	require.Contains(t, seen, "bulk-event-00001")
	require.Contains(t, seen, "bulk-event-100001")
	require.NotContains(t, seen, "event-after-high-water")
}

func TestAppendSessionEventAllocatesUniqueSequenceUnderFanoutConcurrency(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	store := &RunStore{DB: db}
	ensureSessionsForRuns(t, db, "conversation")
	run, err := store.CreateRun(ctx, "conversation", "parent")
	require.NoError(t, err)

	const count = 64
	errCh := make(chan error, count)
	var wg sync.WaitGroup
	for index := range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, appendErr := store.AppendSessionEventOnce(ctx, SessionEvent{
				ID:        fmt.Sprintf("evt-%d", index),
				SessionID: "conversation",
				RunID:     run.ID,
				Type:      "subagent_completed",
				Payload:   json.RawMessage(fmt.Sprintf(`{"task_index":%d}`, index)),
			})
			errCh <- appendErr
		}()
	}
	wg.Wait()
	close(errCh)
	for appendErr := range errCh {
		require.NoError(t, appendErr)
	}

	events, err := store.ListRunEvents(ctx, run.ID, count)
	require.NoError(t, err)
	require.Len(t, events, count)
	seen := map[int64]bool{}
	for _, evt := range events {
		require.False(t, seen[evt.Sequence], "duplicate sequence %d", evt.Sequence)
		seen[evt.Sequence] = true
	}
}

func TestWaitResumeOwnerSurvivesReload(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	store := &RunStore{DB: db}
	ensureSessionsForRuns(t, db, "conversation")
	run, err := store.CreateRun(ctx, "conversation", "task")
	require.NoError(t, err)
	mustWaitAction(t, db, "conversation", "action-1")
	require.NoError(t, store.SetWaitingAction(ctx, run.ID, Wait{RunID: run.ID, ActionID: "action-1", ToolName: "shell", ToolInputJSON: "{}", AgentID: "agent-1"}))
	require.NoError(t, store.MarkWaitResumeOwner(ctx, run.ID, "action-1", "process-A"))

	_, wait, err := store.FindRunByAction(ctx, "action-1")
	require.NoError(t, err)
	require.Equal(t, "process-A", wait.ResumeOwner)
	require.Equal(t, "agent-1", wait.AgentID)
}

// A wait row that is not there and a wait row another process holds are
// different facts, and the message a surface shows says which. Reporting a
// missing continuation as contended sent a real subagent failure looking for a
// second forebrain process that did not exist.
func TestMarkWaitResumeOwnerSeparatesAMissingContinuationFromAContendedOne(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	store := &RunStore{DB: db}
	ensureSessionsForRuns(t, db, "conversation")
	run, err := store.CreateRun(ctx, "conversation", "task")
	require.NoError(t, err)

	err = store.MarkWaitResumeOwner(ctx, run.ID, "action-missing", "process-A")
	require.ErrorIs(t, err, ErrNoWaitContinuation)
	require.NotContains(t, err.Error(), "another live process")

	mustWaitAction(t, db, "conversation", "action-held")
	require.NoError(t, store.SetWaitingAction(ctx, run.ID, Wait{
		RunID: run.ID, ActionID: "action-held", ToolName: "shell", ToolInputJSON: "{}",
	}))
	require.NoError(t, store.MarkWaitResumeOwner(ctx, run.ID, "action-held", "process-A"))
	err = store.MarkWaitResumeOwner(ctx, run.ID, "action-held", "process-B")
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrNoWaitContinuation)
	require.Contains(t, err.Error(), "another live process")
}

func TestClaimWaitResumeIsAtomicAndPreservesSourceMetadata(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	store := &RunStore{DB: db}
	ensureSessionsForRuns(t, db, "conversation")
	run, err := store.CreateRun(ctx, "conversation", "task")
	require.NoError(t, err)
	childRun, err := store.CreateSubagentRun(ctx, run.ID, "conversation", "child")
	require.NoError(t, err)
	mustWaitAction(t, db, "conversation", "action-claim")
	require.NoError(t, store.SetWaitingAction(ctx, run.ID, Wait{
		RunID: run.ID, ActionID: "action-claim", ToolName: "shell", ToolInputJSON: "{}",
		AgentID: "agent-1", SubagentType: "worker", SubagentRunID: childRun.ID,
	}))

	claimed, err := store.ClaimWaitResume(ctx, run.ID, "action-claim", "process-A")
	require.NoError(t, err)
	require.True(t, claimed)
	claimed, err = store.ClaimWaitResume(ctx, run.ID, "action-claim", "process-A")
	require.NoError(t, err)
	require.False(t, claimed, "one owner cannot execute the same continuation twice")
	claimed, err = store.ClaimWaitResume(ctx, run.ID, "action-claim", "process-B")
	require.NoError(t, err)
	require.False(t, claimed, "a second live process must not steal the continuation lease")
	_, wait, err := store.FindRunByAction(ctx, "action-claim")
	require.NoError(t, err)
	wait.ResumeClaimedAt = time.Now().Add(-WaitResumeLease - time.Second).UnixMilli()
	_, err = db.ExecContext(ctx, `UPDATE fb_run_waits SET resume_claimed_at_ms=? WHERE run_id=? AND action_id=?`, wait.ResumeClaimedAt, run.ID, "action-claim")
	require.NoError(t, err)
	claimed, err = store.ClaimWaitResume(ctx, run.ID, "action-claim", "process-B")
	require.NoError(t, err)
	require.True(t, claimed, "a restarted process can take over a stale durable wait")

	_, wait, err = store.FindRunByAction(ctx, "action-claim")
	require.NoError(t, err)
	require.Equal(t, "process-B", wait.ResumeOwner)
	require.Equal(t, "agent-1", wait.AgentID)
	require.Equal(t, "worker", wait.SubagentType)
	require.Equal(t, childRun.ID, wait.SubagentRunID)
}

func TestExecutionFencePreventsStaleApprovalReplay(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	runs := &RunStore{DB: db}
	actions := &ActionService{DB: db}
	ensureSessionsForRuns(t, db, "conversation")
	run, err := runs.CreateRun(ctx, "conversation", "task")
	require.NoError(t, err)
	action, err := actions.CreatePending(ctx, "conversation", "shell", map[string]any{"session_id": "conversation"})
	require.NoError(t, err)
	require.NoError(t, runs.SetWaitingAction(ctx, run.ID, Wait{
		RunID: run.ID, ActionID: action.ID, ToolName: "shell", ToolInputJSON: "{}", AgentID: "agent-1",
	}))
	_, err = actions.Approve(ctx, action.ID, "approved")
	require.NoError(t, err)
	claimed, err := runs.ClaimWaitResume(ctx, run.ID, action.ID, "process-A")
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, runs.BeginWaitResumeExecution(ctx, run.ID, action.ID, "process-A"))

	// Even an arbitrarily old lease cannot be stolen after the execution fence:
	// the original process may already have changed the outside world.
	_, wait, err := runs.FindRunByAction(ctx, action.ID)
	require.NoError(t, err)
	wait.ResumeClaimedAt = time.Now().Add(-10 * WaitResumeLease).UnixMilli()
	_, err = db.ExecContext(ctx, `UPDATE fb_run_waits SET resume_claimed_at_ms=? WHERE run_id=? AND action_id=?`, wait.ResumeClaimedAt, run.ID, action.ID)
	require.NoError(t, err)
	claimed, err = runs.ClaimWaitResume(ctx, run.ID, action.ID, "process-B")
	require.NoError(t, err)
	require.False(t, claimed)
	require.Error(t, runs.ReleaseWaitResumeOwner(ctx, run.ID, action.ID, "process-A"))

	resumable, err := runs.ListResolvedWaitActionIDs(ctx, "", 0)
	require.NoError(t, err)
	require.NotContains(t, resumable, action.ID)
	uncertain, err := runs.ListUncertainResolvedWaits(ctx, "", 0)
	require.NoError(t, err)
	require.Len(t, uncertain, 1)
	require.Equal(t, action.ID, uncertain[0].ActionID)

	require.NoError(t, runs.MarkWaitResumeUncertain(ctx, run.ID, action.ID))
	_, wait, err = runs.FindRunByAction(ctx, action.ID)
	require.NoError(t, err)
	require.Equal(t, WaitResumePhaseUncertain, wait.ResumePhase)
	uncertain, err = runs.ListUncertainResolvedWaits(ctx, "", 0)
	require.NoError(t, err)
	require.Empty(t, uncertain, "startup must classify an uncertain wait only once")
}

// Every forebrain on this machine shares one state database, so an
// execution_started row means "some process is inside the tool call", not "some
// process died inside it". The lease is what tells them apart: a second
// instance that skipped this test read the first instance's running tool as
// abandoned, reported an approval the user never interrupted, and failed a run
// the other process was still driving.
func TestLiveContinuationIsNotClassifiedUncertain(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	runs := &RunStore{DB: db}
	actions := &ActionService{DB: db}
	ensureSessionsForRuns(t, db, "conversation")
	run, err := runs.CreateRun(ctx, "conversation", "task")
	require.NoError(t, err)
	action, err := actions.CreatePending(ctx, "conversation", "shell", map[string]any{"session_id": "conversation"})
	require.NoError(t, err)
	require.NoError(t, runs.SetWaitingAction(ctx, run.ID, Wait{
		RunID: run.ID, ActionID: action.ID, ToolName: "shell", ToolInputJSON: "{}",
	}))
	_, err = actions.Approve(ctx, action.ID, "approved")
	require.NoError(t, err)
	claimed, err := runs.ClaimWaitResume(ctx, run.ID, action.ID, "process-A")
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, runs.BeginWaitResumeExecution(ctx, run.ID, action.ID, "process-A"))

	uncertain, err := runs.ListUncertainResolvedWaits(ctx, "", 0)
	require.NoError(t, err)
	require.Empty(t, uncertain, "a tool call another live process is running is not an interrupted one")

	// An approved command can outlast many leases, so the owner keeps renewing
	// the one it holds for as long as the tool runs.
	expireWaitResumeLease(ctx, t, db, runs, run.ID, action.ID)
	require.NoError(t, runs.MarkWaitResumeOwner(ctx, run.ID, action.ID, "process-A"))
	uncertain, err = runs.ListUncertainResolvedWaits(ctx, "", 0)
	require.NoError(t, err)
	require.Empty(t, uncertain, "renewing the lease keeps a long-running tool call live")

	// Only when the owner stops renewing — because it died holding the fence —
	// is the outcome of the call genuinely unknown.
	expireWaitResumeLease(ctx, t, db, runs, run.ID, action.ID)
	uncertain, err = runs.ListUncertainResolvedWaits(ctx, "", 0)
	require.NoError(t, err)
	require.Len(t, uncertain, 1)
	require.Equal(t, action.ID, uncertain[0].ActionID)
}

// A decision another live process has already claimed is that process's to
// continue. Recovery does not only resume what this scan returns — a cancelled,
// expired or errored action ends its run outright — so offering a live claim
// here fails a run someone else is driving.
func TestRecoveryScanSkipsClaimsALiveProcessHolds(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	runs := &RunStore{DB: db}
	actions := &ActionService{DB: db}
	ensureSessionsForRuns(t, db, "conversation")
	run, err := runs.CreateRun(ctx, "conversation", "task")
	require.NoError(t, err)
	action, err := actions.CreatePending(ctx, "conversation", "shell", map[string]any{"session_id": "conversation"})
	require.NoError(t, err)
	require.NoError(t, runs.SetWaitingAction(ctx, run.ID, Wait{
		RunID: run.ID, ActionID: action.ID, ToolName: "shell", ToolInputJSON: "{}",
	}))
	_, err = actions.Approve(ctx, action.ID, "approved")
	require.NoError(t, err)

	unclaimed, err := runs.ListResolvedWaitActionIDs(ctx, "", 0)
	require.NoError(t, err)
	require.Contains(t, unclaimed, action.ID, "a decision nobody is driving is exactly what recovery is for")

	claimed, err := runs.ClaimWaitResume(ctx, run.ID, action.ID, "process-A")
	require.NoError(t, err)
	require.True(t, claimed)
	held, err := runs.ListResolvedWaitActionIDs(ctx, "", 0)
	require.NoError(t, err)
	require.NotContains(t, held, action.ID)

	expireWaitResumeLease(ctx, t, db, runs, run.ID, action.ID)
	abandoned, err := runs.ListResolvedWaitActionIDs(ctx, "", 0)
	require.NoError(t, err)
	require.Contains(t, abandoned, action.ID, "an owner that stopped renewing has left it to whoever comes next")
}

func expireWaitResumeLease(ctx context.Context, t *testing.T, db *sql.DB, runs *RunStore, runID, actionID string) {
	t.Helper()
	_, wait, err := runs.FindRunByAction(ctx, actionID)
	require.NoError(t, err)
	wait.ResumeClaimedAt = time.Now().Add(-WaitResumeLease - time.Second).UnixMilli()
	_, err = db.ExecContext(ctx, `UPDATE fb_run_waits SET resume_claimed_at_ms=? WHERE run_id=? AND action_id=?`, wait.ResumeClaimedAt, runID, actionID)
	require.NoError(t, err)
}

func TestResolvedApprovalRecoveryScanHasNoImplicitFiveThousandRowCutoff(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	runs := &RunStore{DB: db}
	ensureSessionsForRuns(t, db, "bulk-conv")
	const total = 5001
	_, err = db.ExecContext(ctx, `
WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < ?)
INSERT INTO fb_actions(id,session_id,kind,status,payload_json,answer_json,error,created_at,updated_at)
SELECT printf('bulk-action-%05d',i),'bulk-conv','shell','approved','{}','','',i,i FROM n`, total)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `
WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < ?)
INSERT INTO fb_runs(id,session_id,input_text,status,created_at,updated_at)
SELECT printf('bulk-run-%05d',i),'bulk-conv','bulk','done',i,i FROM n`, total)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `
WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < ?)
INSERT INTO fb_run_waits(run_id,action_id,tool_name,tool_input_json,session_snapshot_json,created_at,updated_at)
SELECT printf('bulk-run-%05d',i),printf('bulk-action-%05d',i),'shell','{}','',i,i FROM n`, total)
	require.NoError(t, err)

	all, err := runs.ListResolvedWaitActionIDs(ctx, "", 0)
	require.NoError(t, err)
	require.Len(t, all, total)
	limited, err := runs.ListResolvedWaitActionIDs(ctx, "", 17)
	require.NoError(t, err)
	require.Len(t, limited, 17)
}

// Recovery does not only tidy rows: it fails the run it recovers and reports an
// interrupted approval to a user. A surface speaks for one conversation, so the
// scan it drives must not reach another one's continuations — starting a fresh
// forebrain session greeted it with an earlier conversation's interrupted
// subagent, and a resumable continuation would have been replayed into it.
//
// A subagent's run records the agent session that dispatched it, so a nested
// child carries its parent's worker session rather than the conversation:
// ancestry, not session_id alone, decides what a conversation owns.
func TestRecoveryScanIsScopedToTheConversationThatOwnsIt(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	runs := &RunStore{DB: db}
	actions := &ActionService{DB: db}

	park := func(sessionID, runID, actionID string) {
		t.Helper()
		_, err := db.ExecContext(ctx, `
INSERT INTO fb_actions(id,session_id,kind,status,payload_json,answer_json,error,created_at,updated_at)
VALUES(?,?,?,?,?,?,?,?,?)`, actionID, sessionID, "shell", string(ActionPending), "{}", "", "", 1, 1)
		require.NoError(t, err)
		require.NoError(t, runs.SetWaitingAction(ctx, runID, Wait{
			RunID: runID, ActionID: actionID, ToolName: "shell", ToolInputJSON: "{}",
		}))
		_, err = actions.Approve(ctx, actionID, "approved")
		require.NoError(t, err)
	}

	ensureSessionsForRuns(t, db, "conversation-a", "worker-session-of-child", "conversation-b")
	mine, err := runs.CreateRun(ctx, "conversation-a", "review the diff")
	require.NoError(t, err)
	child, err := runs.CreateSubagentRun(ctx, mine.ID, "conversation-a", "verification")
	require.NoError(t, err)
	// The child dispatches its own subagent, which records the child's worker
	// session instead of the conversation.
	grandchild, err := runs.CreateSubagentRun(ctx, child.ID, "worker-session-of-child", "fanout")
	require.NoError(t, err)
	theirs, err := runs.CreateRun(ctx, "conversation-b", "something else")
	require.NoError(t, err)

	park("worker-session-of-child", grandchild.ID, "action-mine")
	park("conversation-b", theirs.ID, "action-theirs")

	scoped, err := runs.ListResolvedWaitActionIDs(ctx, "conversation-a", 0)
	require.NoError(t, err)
	require.Equal(t, []string{"action-mine"}, scoped, "a conversation owns every run beneath it and nothing else")

	other, err := runs.ListResolvedWaitActionIDs(ctx, "conversation-b", 0)
	require.NoError(t, err)
	require.Equal(t, []string{"action-theirs"}, other)

	all, err := runs.ListResolvedWaitActionIDs(ctx, "", 0)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"action-mine", "action-theirs"}, all, "a multi-session server still drains every session")

	// The same boundary governs continuations whose outcome is unknown, which
	// are the ones recovery reports to the user as interrupted.
	for _, runID := range []string{grandchild.ID, theirs.ID} {
		actionID := "action-mine"
		if runID == theirs.ID {
			actionID = "action-theirs"
		}
		claimed, err := runs.ClaimWaitResume(ctx, runID, actionID, "process-that-died")
		require.NoError(t, err)
		require.True(t, claimed)
		require.NoError(t, runs.BeginWaitResumeExecution(ctx, runID, actionID, "process-that-died"))
		expireWaitResumeLease(ctx, t, db, runs, runID, actionID)
	}

	uncertain, err := runs.ListUncertainResolvedWaits(ctx, "conversation-a", 0)
	require.NoError(t, err)
	require.Len(t, uncertain, 1)
	require.Equal(t, "action-mine", uncertain[0].ActionID)

	uncertain, err = runs.ListUncertainResolvedWaits(ctx, "", 0)
	require.NoError(t, err)
	require.Len(t, uncertain, 2)
}

// TestListSessionEventsOfTypeReturnsTheNewestOldestFirst pins the read the
// context report and the web history make of one kind of event: only that
// type, only that session, the newest limit of them, in the order they
// happened.
func TestListSessionEventsOfTypeReturnsTheNewestOldestFirst(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	store := &RunStore{DB: db}
	ensureSessionsForRuns(t, db, "s", "other")
	for i, typ := range []string{"context_compacted", "assistant_delta", "context_compacted", "context_compacted"} {
		_, err := store.AppendSessionEvent(ctx, SessionEvent{SessionID: "s", Type: typ, Payload: []byte(`{"n":` + string(rune('0'+i)) + `}`)})
		require.NoError(t, err)
	}
	_, err = store.AppendSessionEvent(ctx, SessionEvent{SessionID: "other", Type: "context_compacted"})
	require.NoError(t, err)

	got, err := store.ListSessionEventsOfType(ctx, "s", "context_compacted", 2)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.JSONEq(t, `{"n":2}`, string(got[0].Payload))
	require.JSONEq(t, `{"n":3}`, string(got[1].Payload))
	require.Less(t, got[0].Sequence, got[1].Sequence)
}

// TestToolAuditReadsWhatTheSurfacesDrew pins the audit readers to the cards
// the surfaces drew: a permission request and a plan update that succeeded are
// not tool cards, a plan-emitting call that failed keeps its own card, and a
// rewind targets the last edit that actually changed a file.
func TestToolAuditReadsWhatTheSurfacesDrew(t *testing.T) {
	ctx := context.Background()
	db := openSessionDB(t)
	if err := NewSessionStore(db, "main").Ensure(ctx, "s", "s"); err != nil {
		t.Fatal(err)
	}
	runs := &RunStore{DB: db}
	for i, payload := range []string{
		`{"tool_name":"read_file"}`,
		`{"tool_name":"request_permissions"}`,
		`{"tool_name":"session_todo"}`,
		`{"tool_name":"session_todo","error":"bad plan"}`,
		`{"tool_name":"edit_file","tool_meta":{"input":{"file_path":"/repo/a.go"}}}`,
		`{"tool_name":"edit_file","error":"old_string not found","tool_meta":{"input":{"file_path":"/repo/b.go"}}}`,
	} {
		if _, err := runs.AppendSessionEvent(ctx, SessionEvent{ID: fmt.Sprintf("evt-%d", i), SessionID: "s", Type: "tool_call_completed", Payload: json.RawMessage(payload)}); err != nil {
			t.Fatal(err)
		}
	}
	byTool, total, err := runs.CountSessionToolCalls(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	if total != 4 || byTool["session_todo"] != 1 || byTool["request_permissions"] != 0 || byTool["edit_file"] != 2 {
		t.Fatalf("audited calls = %v (total %d), want read_file, the failed session_todo and both edits", byTool, total)
	}
	path, ok, err := runs.LatestRewindableToolCall(ctx, "s")
	if err != nil || !ok || path != "/repo/a.go" {
		t.Fatalf("rewind target = %q, %v, %v; want the edit that changed /repo/a.go", path, ok, err)
	}
}

// TestEventOfAConversationThatHasNotStartedIsNotRecorded pins the one rule for
// an event observed before its conversation exists: it is reported as not
// recorded, so the surface shows it without writing it into a history no
// session owns — and without the reference to fb_sessions failing the write.
func TestEventOfAConversationThatHasNotStartedIsNotRecorded(t *testing.T) {
	ctx := context.Background()
	db := openSessionDB(t)
	runs := &RunStore{DB: db}
	_, inserted, err := runs.AppendSessionEventOnce(ctx, SessionEvent{ID: "evt-early", SessionID: "not-yet", Type: "context_compaction_started"})
	if !errors.Is(err, ErrSessionNotStarted) || inserted {
		t.Fatalf("append before the session exists = inserted %v, %v; want ErrSessionNotStarted", inserted, err)
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM fb_session_events`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("events recorded = %d, %v; want none", n, err)
	}
	if err := NewSessionStore(db, "main").Ensure(ctx, "not-yet", "not-yet"); err != nil {
		t.Fatal(err)
	}
	if _, inserted, err := runs.AppendSessionEventOnce(ctx, SessionEvent{ID: "evt-early", SessionID: "not-yet", Type: "context_compaction_started"}); err != nil || !inserted {
		t.Fatalf("append once the session exists = inserted %v, %v; want recorded", inserted, err)
	}
	if _, inserted, err := runs.AppendSessionEventOnce(ctx, SessionEvent{ID: "evt-early", SessionID: "not-yet", Type: "context_compaction_started"}); err != nil || inserted {
		t.Fatalf("repeat append = inserted %v, %v; want the existing row", inserted, err)
	}
}
