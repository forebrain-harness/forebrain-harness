package turn

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
	"github.com/stretchr/testify/require"
)

func TestSubagentApprovalGateFromError(t *testing.T) {
	snapshot := []llm.Message{llm.UserMessage(llm.Text("investigate"))}
	gate, ok := SubagentApprovalGateFromError(&tool.RequiresActionError{
		RunID: "child-1", ActionID: "act-1", ActionKind: "shell", ToolName: "shell",
		AgentID: "task-1", SubagentType: "plan", SessionSnapshot: snapshot,
	})
	require.True(t, ok)
	require.Equal(t, "child-1", gate.RunID)
	require.Equal(t, "act-1", gate.ActionID)
	require.Equal(t, "plan", gate.SubagentType)
	require.Len(t, gate.Session, 1)

	_, ok = SubagentApprovalGateFromError(errors.New("boom"))
	require.False(t, ok)
}

// The request every surface renders names the worker that is blocked, and is
// evaluated against the conversation that dispatched it rather than the
// child's throwaway worker session.
func TestSubagentApprovalRequestNamesTheWorker(t *testing.T) {
	gate := SubagentApprovalGate{
		ActionID: "act-1", RunID: "child-1", ToolName: "shell", ActionKind: "shell",
		SubagentType: "plan", ToolInput: map[string]any{"command": "git grep -l todo"},
	}
	req := SubagentApprovalRequest(gate, "session-1", nil)
	require.Equal(t, "session-1", req.SessionID)
	require.Equal(t, "act-1", req.ActionID)
	require.Equal(t, "Requested by plan.", req.Description)

	anonymous := SubagentApprovalRequest(SubagentApprovalGate{ActionID: "act-2", ToolName: "shell"}, "session-1", nil)
	require.Equal(t, "Requested by a subagent.", anonymous.Description)
}

// A denial is an answer the child continues from; only a cancellation ends the
// run, because there is nothing to continue with.
func TestSubagentApprovalResumeContext(t *testing.T) {
	snapshot := []llm.Message{llm.UserMessage(llm.Text("investigate"))}
	gate := SubagentApprovalGate{ActionID: "act-1", ToolName: "shell", Session: snapshot}

	approved, err := SubagentApprovalResumeContext(context.Background(), gate,
		ApprovalResume{Session: snapshot, Approved: true}, "the subagent")
	require.NoError(t, err)
	require.Equal(t, "act-1", tool.ApprovedActionIDFromContext(approved))
	state := tool.ToolApprovalResumeFromContext(approved)
	require.NotNil(t, state)
	require.False(t, state.Denied)
	require.Len(t, state.Session, 1)

	denied, err := SubagentApprovalResumeContext(context.Background(), gate,
		ApprovalResume{Session: snapshot, Denied: true, Reason: "read the file instead"}, "the subagent")
	require.NoError(t, err)
	deniedState := tool.ToolApprovalResumeFromContext(denied)
	require.NotNil(t, deniedState)
	require.True(t, deniedState.Denied)
	require.Equal(t, "read the file instead", deniedState.DenyReason)

	_, err = SubagentApprovalResumeContext(context.Background(), gate, ApprovalResume{}, "the subagent")
	require.ErrorContains(t, err, "cancelled while waiting for tool approval")
}

type phaseRecorder struct{ resumed []string }

func (p *phaseRecorder) Resume(runID string) bool {
	p.resumed = append(p.resumed, runID)
	return true
}

// The live phase is claimed but the durable snapshot remains until the resumed
// attempt settles, so a crash in between can reconstruct the continuation.
func TestReleaseSubagentApprovalWaitRetainsDurableSnapshot(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(ctx, filepath.Join(t.TempDir(), "state.db"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	runs := &state.RunStore{DB: db}
	if err := state.NewSessionStore(db, "main").Ensure(ctx, "session-1", "session-1"); err != nil {
		t.Fatal(err)
	}
	if err := state.NewSessionStore(db, "main").Ensure(ctx, "session-1", "session-1"); err != nil {
		t.Fatal(err)
	}
	run, err := runs.CreateRun(ctx, "session-1", "investigate")
	require.NoError(t, err)
	if _, err := db.Exec(`INSERT INTO fb_actions(id,session_id,kind,status,payload_json,created_at,updated_at) VALUES('act-1','session-1','shell','pending','{}',1,1)`); err != nil {
		t.Fatal(err)
	}
	require.NoError(t, runs.SetWaitingAction(ctx, run.ID, state.Wait{
		RunID: run.ID, ActionID: "act-1", ToolName: "shell", ToolInputJSON: "{}",
	}))

	phases := &phaseRecorder{}
	ReleaseSubagentApprovalWait(ctx, runs, phases, run.ID)

	foundRunID, wait, findErr := runs.FindRunByAction(ctx, "act-1")
	require.NoError(t, findErr)
	require.Equal(t, run.ID, foundRunID)
	require.Equal(t, "act-1", wait.ActionID)
	stored, err := runs.GetRun(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, state.RunStatusWaitingAction, stored.Status)
	require.Equal(t, []string{run.ID}, phases.resumed)
}
