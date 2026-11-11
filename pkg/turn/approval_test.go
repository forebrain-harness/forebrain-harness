package turn

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/state/dbx"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
	"github.com/stretchr/testify/require"
)

type approvalStoreStub struct{ action *state.Action }

func (s *approvalStoreStub) Get(context.Context, string) (*state.Action, error) {
	return s.action, nil
}

func (s *approvalStoreStub) ApproveWithAnswer(_ context.Context, _ string, _ string, answer string) (*state.Action, error) {
	if s.action.Status != state.ActionPending {
		return nil, state.ErrNotPending
	}
	s.action.Status = state.ActionApproved
	s.action.AnswerJSON = answer
	return s.action, nil
}

func (s *approvalStoreStub) AnswerAsk(_ context.Context, _ string, answer state.AskAnswer) (*state.Action, error) {
	if s.action.Status != state.ActionPending {
		return nil, state.ErrNotPending
	}
	s.action.Status = state.ActionAnswered
	raw, _ := json.Marshal(answer)
	s.action.AnswerJSON = string(raw)
	return s.action, nil
}

func (s *approvalStoreStub) Deny(_ context.Context, _ string, reason string) (*state.Action, error) {
	if s.action.Status != state.ActionPending {
		return nil, state.ErrNotPending
	}
	s.action.Status = state.ActionDenied
	s.action.Error = reason
	return s.action, nil
}

func (s *approvalStoreStub) DenyWithAnswer(_ context.Context, _ string, reason string, answer string) (*state.Action, error) {
	if s.action.Status != state.ActionPending {
		return nil, state.ErrNotPending
	}
	s.action.Status = state.ActionDenied
	s.action.Error = reason
	s.action.AnswerJSON = answer
	return s.action, nil
}

func (s *approvalStoreStub) Cancel(_ context.Context, _ string, reason string) (*state.Action, error) {
	if s.action.Status != state.ActionPending {
		return nil, state.ErrNotPending
	}
	s.action.Status = state.ActionCancelled
	s.action.Error = reason
	return s.action, nil
}

type approvalPolicyStub struct{ updates []safety.PermissionUpdate }

func (s *approvalPolicyStub) EvaluatePermissionForSession(string, string, string) safety.Decision {
	return safety.Decision{Behavior: safety.BehaviorAsk}
}

func (s *approvalPolicyStub) ApplyPermissionUpdate(update safety.PermissionUpdate) {
	s.updates = append(s.updates, update)
}

type approvalEval struct{ d safety.Decision }

func (e approvalEval) EvaluatePermissionForSession(string, string, string) safety.Decision {
	return e.d
}

func TestBuildToolApprovalRequestNormalizesJSONInput(t *testing.T) {
	fromObject, _, _ := BuildToolApprovalRequest(ApprovalSource{ToolName: "shell", ActionKind: "shell", ToolInput: map[string]any{"command": "npm run build"}}, approvalEval{})
	fromJSON, _, _ := BuildToolApprovalRequest(ApprovalSource{ToolName: "shell", ActionKind: "shell", ToolInput: `{"command":"npm run build"}`}, approvalEval{})
	require.Equal(t, fromObject.PermissionInput, fromJSON.PermissionInput)
	require.Equal(t, fromObject.AvailableDecisions, fromJSON.AvailableDecisions)
}

func TestApprovalServiceAppliesEffectAfterApprove(t *testing.T) {
	store := &approvalStoreStub{action: &state.Action{
		ID: "a1", Kind: "shell", Status: state.ActionPending,
		PayloadJSON: `{"command":"go test ./...","session_id":"s1"}`,
	}}
	policy := &approvalPolicyStub{}
	service := ApprovalService{Actions: store, Policy: policy}
	_, err := service.Decide(context.Background(), "a1", ApprovalReply{
		Choice: "accept_with_execpolicy_amendment", Exec: safety.ExecPolicyAmendment{"go", "test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if store.action.Status != state.ActionApproved || len(policy.updates) != 1 {
		t.Fatalf("action=%+v updates=%#v", store.action, policy.updates)
	}
	result, err := service.Decide(context.Background(), "a1", ApprovalReply{
		Choice: "accept_with_execpolicy_amendment", Exec: safety.ExecPolicyAmendment{"go", "test"},
	})
	require.NoError(t, err)
	require.True(t, result.Idempotent)
	if len(policy.updates) != 1 {
		t.Fatal("duplicate approval applied an effect")
	}
	_, err = service.Decide(context.Background(), "a1", ApprovalReply{Choice: "accept"})
	require.ErrorIs(t, err, ErrApprovalConflict)
}

func TestApprovalServiceReplaysDurableEffectAfterRestart(t *testing.T) {
	action := &state.Action{
		ID: "a-recover", SessionID: "conversation-1", Kind: "apply_patch", Status: state.ActionApproved,
		PayloadJSON: `{"resolved_paths":["/repo/a.go"],"session_id":"conversation-1"}`,
		AnswerJSON:  `{"decision":"accept_for_session"}`,
	}
	policy := &approvalPolicyStub{}
	err := (ApprovalService{Policy: policy}).ApplyResolvedEffect(action, "run-1")
	require.NoError(t, err)
	require.Len(t, policy.updates, 1)
	update := policy.updates[0]
	require.Equal(t, safety.DestinationSession, update.Destination)
	require.Equal(t, "conversation-1", update.SessionID)
	require.Len(t, update.Rules, 1)
	require.Equal(t, "/repo/a.go", update.Rules[0].RuleContent)
	require.True(t, update.Rules[0].BypassSandbox)
}

func TestApprovalServiceReplaysRequestPermissionsEffectWithRunScope(t *testing.T) {
	action := &state.Action{
		ID: "a-permissions", SessionID: "conversation-1", Kind: "request_permissions", Status: state.ActionApproved,
		PayloadJSON: `{"session_id":"conversation-1","cwd":"/repo","permissions":{"network":{"enabled":true}}}`,
		AnswerJSON:  `{"permissions":{"network":{"enabled":true}},"scope":"turn"}`,
	}
	policy := &approvalPolicyStub{}
	err := (ApprovalService{Policy: policy}).ApplyResolvedEffect(action, "run-1")
	require.NoError(t, err)
	require.Len(t, policy.updates, 1)
	update := policy.updates[0]
	require.Equal(t, "conversation-1", update.SessionID)
	require.Len(t, update.NetworkGrants, 1)
	require.Equal(t, safety.GrantScopeTurn, update.NetworkGrants[0].Scope)
	require.Equal(t, "run-1", update.NetworkGrants[0].RunID)
}

func TestApprovalServiceRequestPermissionsExactRetryIsIdempotent(t *testing.T) {
	store := &approvalStoreStub{action: &state.Action{
		ID: "a-permissions", SessionID: "conversation-1", Kind: "request_permissions", Status: state.ActionPending,
		PayloadJSON: `{"session_id":"conversation-1","cwd":"/repo","permissions":{"network":{"enabled":true}}}`,
	}}
	service := ApprovalService{Actions: store}

	first, err := service.Decide(context.Background(), store.action.ID, ApprovalReply{Choice: "grant_for_session"})
	require.NoError(t, err)
	require.True(t, first.Approved)
	require.False(t, first.Idempotent)

	retry, err := service.Decide(context.Background(), store.action.ID, ApprovalReply{Choice: "grant_for_session"})
	require.NoError(t, err)
	require.True(t, retry.Approved)
	require.True(t, retry.Idempotent)

	_, err = service.Decide(context.Background(), store.action.ID, ApprovalReply{Choice: "grant_for_turn"})
	require.ErrorIs(t, err, ErrApprovalConflict)
}

func TestActionClearedContext(t *testing.T) {
	if !ActionClearedContext(&state.Action{Status: state.ActionApproved, AnswerJSON: `{"clear_context":true}`}) {
		t.Fatal("approved clear-context action was not recognized")
	}
	if ActionClearedContext(&state.Action{Status: state.ActionPending, AnswerJSON: `{"clear_context":true}`}) {
		t.Fatal("pending action reset context")
	}
}

func TestBuildApprovalResumeKeepsDeniedSnapshot(t *testing.T) {
	snapshot := []llm.Message{llm.UserMessage(llm.Text("old"))}
	resume := BuildApprovalResume(&state.Action{Status: state.ActionDenied, Error: "no"}, snapshot, "shell", true)
	if !resume.Denied || resume.Cleared == false || resume.Reason != "no" || len(resume.Session) != 1 {
		t.Fatalf("resume=%+v", resume)
	}
}

func TestBuildApprovalResumeDoesNotContinueExpiredAction(t *testing.T) {
	snapshot := []llm.Message{llm.UserMessage(llm.Text("old"))}
	resume := BuildApprovalResume(&state.Action{Status: state.ActionExpired, Error: "approval ttl expired"}, snapshot, "shell", false)
	if resume.Approved || resume.Denied || resume.Reason != "approval ttl expired" || len(resume.Session) != 1 {
		t.Fatalf("resume=%+v", resume)
	}
}

func TestPlanModeForAction(t *testing.T) {
	mode, ok := PlanModeForAction(&state.Action{Status: state.ActionApproved, Kind: "exit_plan_mode", PayloadJSON: `{"action":"exit"}`})
	if !ok || mode != state.ModeAgent {
		t.Fatalf("mode=%q ok=%t", mode, ok)
	}
	if _, ok := PlanModeForAction(&state.Action{Status: state.ActionPending, Kind: "enter_plan_mode"}); ok {
		t.Fatal("pending plan action selected a mode")
	}
}

func TestParseAskAnswer(t *testing.T) {
	answer, err := ParseAskAnswer(``, `{"questions":[{"id":"q1","prompt":"continue?","options":[{"id":"yes","label":"Yes"},{"id":"no","label":"No"}]}]}`)
	if err != nil || len(answer.Answers) != 1 || len(answer.Answers[0].OptionIDs) != 1 || answer.Answers[0].OptionIDs[0] != "yes" {
		t.Fatalf("answer=%+v err=%v", answer, err)
	}
}

func TestApprovalServiceAnswersAsk(t *testing.T) {
	store := &approvalStoreStub{action: &state.Action{
		ID: "a1", Kind: "user_interaction", Status: state.ActionPending,
		PayloadJSON: `{"questions":[{"id":"q1","prompt":"continue?","options":[{"id":"yes","label":"Yes"},{"id":"no","label":"No"}]}]}`,
	}}
	result, err := (ApprovalService{Actions: store}).Decide(context.Background(), "a1", ApprovalReply{AskAnswerJSON: `{"answers":[{"question_id":"q1","option_ids":["no"]}]}`})
	require.NoError(t, err)
	require.True(t, result.Answered)
	require.Equal(t, state.ActionAnswered, store.action.Status)
	result, err = (ApprovalService{Actions: store}).Decide(context.Background(), "a1", ApprovalReply{AskAnswerJSON: `{"answers":[{"question_id":"q1","option_ids":["no"]}]}`})
	require.NoError(t, err)
	require.True(t, result.Idempotent)
}

func TestApprovalServiceRejectsInvalidClearContext(t *testing.T) {
	store := &approvalStoreStub{action: &state.Action{ID: "a1", Kind: "shell", Status: state.ActionPending, PayloadJSON: `{}`}}
	_, err := (ApprovalService{Actions: store}).Decide(context.Background(), "a1", ApprovalReply{ClearContext: true})
	require.ErrorContains(t, err, "clear context")
	require.Equal(t, state.ActionPending, store.action.Status)
}

func TestDecidePlanOnlyOnlyForApprovedExitPlan(t *testing.T) {
	// Non exit_plan_mode kind.
	store := &approvalStoreStub{action: &state.Action{ID: "a1", Kind: "shell", Status: state.ActionPending, PayloadJSON: `{}`}}
	_, err := (ApprovalService{Actions: store}).Decide(context.Background(), "a1", ApprovalReply{PlanOnly: true})
	require.ErrorContains(t, err, "plan only is only valid")
	require.Equal(t, state.ActionPending, store.action.Status)

	// A denied exit-plan decision cannot keep the plan only.
	store = &approvalStoreStub{action: &state.Action{ID: "a2", Kind: "exit_plan_mode", Status: state.ActionPending, PayloadJSON: `{}`}}
	_, err = (ApprovalService{Actions: store}).Decide(context.Background(), "a2", ApprovalReply{Choice: "deny", PlanOnly: true})
	require.ErrorContains(t, err, "plan only is only valid")
	require.Equal(t, state.ActionPending, store.action.Status)

	// plan_only and clear_context are mutually exclusive.
	store = &approvalStoreStub{action: &state.Action{ID: "a3", Kind: "exit_plan_mode", Status: state.ActionPending, PayloadJSON: `{}`}}
	_, err = (ApprovalService{Actions: store}).Decide(context.Background(), "a3", ApprovalReply{PlanOnly: true, ClearContext: true})
	require.ErrorContains(t, err, "cannot be combined")
	require.Equal(t, state.ActionPending, store.action.Status)
}

func TestPlanOnlyIsRecordedAndIdempotent(t *testing.T) {
	store := &approvalStoreStub{action: &state.Action{ID: "a1", Kind: "exit_plan_mode", Status: state.ActionPending, PayloadJSON: `{}`}}
	service := ApprovalService{Actions: store}

	result, err := service.Decide(context.Background(), "a1", ApprovalReply{PlanOnly: true})
	require.NoError(t, err)
	require.True(t, result.Approved)
	require.Contains(t, store.action.AnswerJSON, `"plan_only":true`)

	retry, err := service.Decide(context.Background(), "a1", ApprovalReply{PlanOnly: true})
	require.NoError(t, err)
	require.True(t, retry.Approved)
	require.True(t, retry.Idempotent)

	_, err = service.Decide(context.Background(), "a1", ApprovalReply{})
	require.ErrorIs(t, err, ErrApprovalConflict)
}

func TestActionKeptPlanOnly(t *testing.T) {
	if !ActionKeptPlanOnly(&state.Action{Status: state.ActionApproved, AnswerJSON: `{"plan_only":true}`}) {
		t.Fatal("approved plan-only action was not recognized")
	}
	if ActionKeptPlanOnly(&state.Action{Status: state.ActionPending, AnswerJSON: `{"plan_only":true}`}) {
		t.Fatal("pending action kept the plan only")
	}
	if ActionKeptPlanOnly(&state.Action{Status: state.ActionApproved, AnswerJSON: `{}`}) {
		t.Fatal("ordinary approval kept the plan only")
	}
}

func TestBuildToolApprovalRequestSuppressesPersistentCommandChoicesAfterMatch(t *testing.T) {
	req, _, blocked := BuildToolApprovalRequest(ApprovalSource{
		SessionID: "sid", ToolName: "shell", ActionKind: "shell", ToolInput: map[string]any{"command": "npm run build"},
	}, approvalEval{d: safety.Decision{Matched: &safety.PermissionRule{}}})
	require.True(t, blocked)
	require.Empty(t, req.DestinationOptions)
	for _, option := range req.AvailableDecisions {
		require.NotEqual(t, safety.DecisionAcceptWithExecPolicyAmendment, option.Decision)
	}
}

func TestRequestPermissionsFromJSON(t *testing.T) {
	ctx := context.Background()
	response := RequestPermissionsFromJSON(ctx, "request_permissions", `{"permissions":{"file_system":{"read":["."]}}}`)
	require.NotNil(t, response)
	require.Len(t, response.Permissions.FileSystem.Entries, 1)
	require.Nil(t, RequestPermissionsFromJSON(ctx, "shell", `{}`))
}

// fakeApprovalRuns is the run-store slice PendingApprovalGate reads.
type fakeApprovalRuns struct {
	waitingSession string
	runID          string
	wait           *state.Wait
	firstErr       error
}

func (f *fakeApprovalRuns) SessionHasRunWaitingOnToolApproval(_ context.Context, sessionID string) (bool, error) {
	return sessionID == f.waitingSession, nil
}

func (f *fakeApprovalRuns) FirstWaitingRunIDForSession(_ context.Context, sessionID string) (string, error) {
	if f.firstErr != nil {
		return "", f.firstErr
	}
	if sessionID != f.waitingSession {
		return "", nil
	}
	return f.runID, nil
}

func (f *fakeApprovalRuns) GetWaitForRun(_ context.Context, runID string) (*state.Wait, error) {
	if runID != f.runID {
		return nil, nil
	}
	return f.wait, nil
}

func (f *fakeApprovalRuns) ListSessionEventsOfType(context.Context, string, string, int) ([]state.SessionEvent, error) {
	return nil, nil
}

type fakeApprovalActions struct{ action *state.Action }

func (f *fakeApprovalActions) Get(context.Context, string) (*state.Action, error) {
	return f.action, nil
}

func TestPendingApprovalGateReportsTheParkedApproval(t *testing.T) {
	t.Parallel()

	runs := &fakeApprovalRuns{
		waitingSession: "s1",
		runID:          "run-1",
		wait: &state.Wait{
			RunID:            "run-1",
			ActionID:         "act-1",
			ToolName:         "Bash",
			ToolInputJSON:    `{"command":"curl example.com","description":"fetch the page","sandbox_denial_reason":"network blocked"}`,
			SandboxProfile:   "readonly",
			RequestedProfile: "workspace-write",
			ProfileElevation: true,
		},
	}
	gate := &PendingApprovalGate{
		Runs:    runs,
		Actions: &fakeApprovalActions{action: &state.Action{Kind: "shell"}},
	}

	if !gate.Waiting(context.Background(), "s1") {
		t.Fatal("Waiting = false, want true for the parked session")
	}
	if gate.Waiting(context.Background(), "other") {
		t.Fatal("Waiting = true for a session with no open gate")
	}

	req, err := gate.Pending(context.Background(), "s1")
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if req == nil {
		t.Fatal("Pending = nil, want the parked approval")
	}
	if req.RunID != "run-1" || req.ActionID != "act-1" || req.ToolName != "Bash" {
		t.Fatalf("Pending identity = run %q action %q tool %q", req.RunID, req.ActionID, req.ToolName)
	}
	if req.ActionKind != "shell" {
		t.Fatalf("ActionKind = %q, want the action's kind, not the tool name", req.ActionKind)
	}
	if req.Description != "fetch the page" {
		t.Fatalf("Description = %q", req.Description)
	}
	// No justification was supplied, so the sandbox denial has to explain the
	// request instead of leaving the user with a bare command.
	if req.Reason != "The command was blocked by the sandbox: network blocked" {
		t.Fatalf("Reason = %q", req.Reason)
	}
	if req.SandboxProfile != "readonly" || req.RequestedProfile != "workspace-write" || !req.ProfileElevation {
		t.Fatalf("wait-derived fields = %q/%q/%v", req.SandboxProfile, req.RequestedProfile, req.ProfileElevation)
	}
}

func TestPendingApprovalGateReportsNothingWhenIdle(t *testing.T) {
	t.Parallel()

	var nilGate *PendingApprovalGate
	if nilGate.Waiting(context.Background(), "s1") {
		t.Fatal("a nil gate must report nothing waiting")
	}
	if req, err := nilGate.Pending(context.Background(), "s1"); err != nil || req != nil {
		t.Fatalf("nil gate Pending = %v, %v; want nil, nil", req, err)
	}

	gate := &PendingApprovalGate{Runs: &fakeApprovalRuns{waitingSession: "other", runID: "run-1"}}
	req, err := gate.Pending(context.Background(), "s1")
	if err != nil || req != nil {
		t.Fatalf("Pending for an idle session = %v, %v; want nil, nil", req, err)
	}
}

func TestPendingApprovalGatePrefersTheCallersRunIDHint(t *testing.T) {
	t.Parallel()

	// The store search fails, so a request can only come back if the hint was
	// used -- which is how the TUI avoids the search for an approval it is
	// already holding.
	runs := &fakeApprovalRuns{
		waitingSession: "s1",
		runID:          "run-1",
		firstErr:       errors.New("store search must not be reached"),
		wait:           &state.Wait{RunID: "run-1", ActionID: "act-1", ToolName: "read_file"},
	}
	gate := &PendingApprovalGate{Runs: runs, RunIDHint: func() string { return "run-1" }}
	req, err := gate.Pending(context.Background(), "s1")
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if req == nil || req.RunID != "run-1" {
		t.Fatalf("Pending = %v, want the hinted run", req)
	}
}

// PendingForRun builds the same request Pending does for the run it names
// directly (plan 127): an in-place gate knows its run, so the store search is
// not consulted, and a run that is not parked answers nil.
func TestPendingForRunNamesTheRunWithoutSearching(t *testing.T) {
	t.Parallel()

	// The store search fails, so the request can only come back if the named
	// run was read directly.
	runs := &fakeApprovalRuns{
		waitingSession: "s1",
		runID:          "run-1",
		firstErr:       errors.New("store search must not be reached"),
		wait:           &state.Wait{RunID: "run-1", ActionID: "act-1", ToolName: "read_file"},
	}
	gate := &PendingApprovalGate{Runs: runs}
	req, err := gate.PendingForRun(context.Background(), "s1", "run-1")
	if err != nil {
		t.Fatalf("PendingForRun: %v", err)
	}
	if req == nil || req.RunID != "run-1" || req.ActionID != "act-1" {
		t.Fatalf("PendingForRun = %+v, want the named run's wait", req)
	}
	// A run with no wait row answers nil, not an error: "not parked" is the
	// answer an in-place caller hands back to the unwind path.
	runs.wait = nil
	req, err = gate.PendingForRun(context.Background(), "s1", "run-2")
	if err != nil || req != nil {
		t.Fatalf("PendingForRun for an unparked run = %v, %v; want nil, nil", req, err)
	}
}

func fencedWait(t *testing.T, ctx context.Context) (*sql.DB, *state.RunStore, string, string) {
	t.Helper()
	db, err := state.Open(ctx, filepath.Join(t.TempDir(), "state.db"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	runs := &state.RunStore{DB: db}
	actions := &state.ActionService{DB: db}
	if err := state.NewSessionStore(db, "main").Ensure(ctx, "session-1", "session-1"); err != nil {
		t.Fatal(err)
	}
	run, err := runs.CreateRun(ctx, "session-1", "investigate")
	require.NoError(t, err)
	action, err := actions.CreatePending(ctx, "session-1", "shell", map[string]any{"session_id": "session-1"})
	require.NoError(t, err)
	require.NoError(t, runs.SetWaitingAction(ctx, run.ID, state.Wait{
		RunID: run.ID, ActionID: action.ID, ToolName: "shell", ToolInputJSON: "{}",
	}))
	_, err = actions.Approve(ctx, action.ID, "approved")
	require.NoError(t, err)
	claimed, err := runs.ClaimWaitResume(ctx, run.ID, action.ID, "process-A")
	require.NoError(t, err)
	require.True(t, claimed)
	return db, runs, run.ID, action.ID
}

// expireLease is what dying looks like from another process: the owner stops
// renewing.
func expireLease(ctx context.Context, t *testing.T, db *sql.DB, runID, actionID string) {
	t.Helper()
	stale := time.Now().Add(-2 * state.WaitResumeLease).UnixMilli()
	_, err := db.ExecContext(ctx,
		dbx.Rebind(dbx.DialectOf(db), `UPDATE fb_run_waits SET resume_claimed_at_ms=? WHERE run_id=? AND action_id=?`),
		stale, runID, actionID)
	require.NoError(t, err)
}

// The fence opens on the replay and closes with it: before it opens the
// continuation is still replayable, and once it closes there is nothing left
// for a later process to be uncertain about.
func TestBoundFenceOpensOnTheReplayAndClearsTheWaitAfterIt(t *testing.T) {
	ctx := context.Background()
	db, runs, runID, actionID := fencedWait(t, ctx)

	resume := &tool.ToolApprovalResumeState{}
	BindApprovalContinuationFence(resume, runs, runID, actionID, "process-A")

	// Short of the replay the continuation is still replayable: were this
	// process to die here, the next one is meant to pick it up and run the tool.
	expireLease(ctx, t, db, runID, actionID)
	resumable, err := runs.ListResolvedWaitActionIDs(ctx, "", 0)
	require.NoError(t, err)
	require.Contains(t, resumable, actionID)

	require.NoError(t, tool.BeginApprovalContinuation(tool.WithToolApprovalResume(ctx, resume)))
	_, wait, err := runs.FindRunByAction(ctx, actionID)
	require.NoError(t, err)
	require.Equal(t, state.WaitResumePhaseExecutionStarted, wait.ResumePhase)
	expireLease(ctx, t, db, runID, actionID)
	resumable, err = runs.ListResolvedWaitActionIDs(ctx, "", 0)
	require.NoError(t, err)
	require.NotContains(t, resumable, actionID, "a tool call that started must never be replayed automatically")
	uncertain, err := runs.ListUncertainResolvedWaits(ctx, "", 0)
	require.NoError(t, err)
	require.Len(t, uncertain, 1, "an owner that stopped renewing mid-call leaves an unknown outcome")

	tool.EndApprovalContinuation(tool.WithToolApprovalResume(ctx, resume))
	stored, err := runs.GetWaitForRun(ctx, runID)
	require.NoError(t, err)
	require.Nil(t, stored, "the continuation is over, so nothing is left to recover")
	uncertain, err = runs.ListUncertainResolvedWaits(ctx, "", 0)
	require.NoError(t, err)
	require.Empty(t, uncertain)
}

// An approved command can run for far longer than one lease. While it does,
// this process keeps saying so, or a second forebrain sharing the state database
// reports the running tool as an interrupted one.
func TestBoundFenceKeepsTheLeaseFreshWhileTheToolRuns(t *testing.T) {
	ctx := context.Background()
	_, runs, runID, actionID := fencedWait(t, ctx)

	resume := &tool.ToolApprovalResumeState{}
	BindApprovalContinuationFence(resume, runs, runID, actionID, "process-A")
	require.NoError(t, tool.BeginApprovalContinuation(tool.WithToolApprovalResume(ctx, resume)))
	t.Cleanup(func() { tool.EndApprovalContinuation(tool.WithToolApprovalResume(ctx, resume)) })

	_, wait, err := runs.FindRunByAction(ctx, actionID)
	require.NoError(t, err)
	opened := wait.ResumeClaimedAt
	require.Eventually(t, func() bool {
		_, current, err := runs.FindRunByAction(ctx, actionID)
		return err == nil && current.ResumeClaimedAt > opened
	}, 3*state.WaitResumeLease, 100*time.Millisecond, "the owner must renew the lease it holds")

	uncertain, err := runs.ListUncertainResolvedWaits(ctx, "", 0)
	require.NoError(t, err)
	require.Empty(t, uncertain)
}

// "decline" is the name the service's decision proposal gives a refusal that
// leaves the run alive, and every surface sends it back verbatim. It has to
// resolve for whatever kind of action offered it — a declined memory note
// denies the call and the turn carries on from there.
func TestApprovalServiceDeclineDeniesAnyAction(t *testing.T) {
	store := &approvalStoreStub{action: &state.Action{
		ID: "a-note", Kind: "memories_add_ad_hoc_note", Status: state.ActionPending,
		PayloadJSON: `{"filename":"2026-09-11T08-54-34-note.md","note":"# Note"}`,
	}}
	service := ApprovalService{Actions: store}

	result, err := service.Decide(context.Background(), "a-note", ApprovalReply{Choice: "decline"})
	require.NoError(t, err)
	require.True(t, result.Denied)
	require.False(t, result.Cancelled)
	require.Equal(t, state.ActionDenied, store.action.Status)

	retry, err := service.Decide(context.Background(), "a-note", ApprovalReply{Choice: "decline"})
	require.NoError(t, err)
	require.True(t, retry.Denied)
	require.True(t, retry.Idempotent)
}

// A plan-mode shell approval is a question, and the sentence the shell tool put
// on the payload is what the surface shows for it: the shell path of every
// approval overlay renders Reason, so the justification has to survive the trip
// from the pending action's payload into the request.
func TestShellApprovalNarrativeCarriesThePlanModeJustification(t *testing.T) {
	t.Parallel()

	justification := "Plan mode is active: this command could not be proven read-only, so it will run only if you allow it."
	runs := &fakeApprovalRuns{
		waitingSession: "s1",
		runID:          "run-1",
		wait: &state.Wait{
			RunID:         "run-1",
			ActionID:      "act-1",
			ToolName:      "shell",
			ToolInputJSON: `{"command":"python3 script.py","force_tool_approval":true,"approval_reason":"plan_mode_unproven_command","justification":` + strconv.Quote(justification) + `}`,
		},
	}
	gate := &PendingApprovalGate{
		Runs:    runs,
		Actions: &fakeApprovalActions{action: &state.Action{Kind: "shell"}},
	}
	req, err := gate.Pending(context.Background(), "s1")
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if req == nil {
		t.Fatal("Pending = nil, want the parked approval")
	}
	if req.Reason != justification {
		t.Fatalf("Reason = %q, want the plan-mode justification", req.Reason)
	}
	if req.ToolInputJSON == "" {
		t.Fatal("the overlay needs the payload to render the approval")
	}
}

// planReviewEventDB opens one state database with a session row, for the
// plan-review record tests: the conversation's event log is where a review's
// result lives, so the tests read it back exactly as another process would.
func planReviewEventDB(t *testing.T) (*sql.DB, *state.RunStore, string) {
	t.Helper()
	ctx := context.Background()
	db, err := state.Open(ctx, filepath.Join(t.TempDir(), "state.db"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	runs := &state.RunStore{DB: db}
	require.NoError(t, state.NewSessionStore(db, "main").Ensure(ctx, "session-1", "session-1"))
	return db, runs, "session-1"
}

// appendPlanReviewed stores one plan_reviewed event the way RunPlanReview's
// publisher does.
func appendPlanReviewed(t *testing.T, runs *state.RunStore, sessionID, actionID, reviewID, outcome, text string) {
	t.Helper()
	payload, err := json.Marshal(event.PlanReviewedPayload{
		ActionID: actionID, ReviewID: reviewID,
		Provider: "openai", Model: "gpt-5.1",
		Text: text, DurationMs: 125000, Outcome: outcome,
	})
	require.NoError(t, err)
	_, err = runs.AppendSessionEvent(context.Background(), state.SessionEvent{
		ID: "plan-review:" + reviewID + ":reviewed", SessionID: sessionID,
		Type: event.RunEventPlanReviewed, Payload: payload,
	})
	require.NoError(t, err)
}

// The reviews a pending approval collected live on the conversation, not in
// the process that asked for them: only the completed ones are reviews, and a
// second reader over the same database — another surface, a restarted
// process — sees the same list in the same order.
func TestPlanReviewsLiveOnTheConversation(t *testing.T) {
	t.Parallel()

	db, runs, sessionID := planReviewEventDB(t)
	appendPlanReviewed(t, runs, sessionID, "act-1", "rev-failed", "failed", "")
	appendPlanReviewed(t, runs, sessionID, "act-1", "rev-done", "done", "Verdict: rework.")
	appendPlanReviewed(t, runs, sessionID, "act-2", "rev-other", "done", "Another action's review.")

	results, err := PlanReviewsForAction(context.Background(), runs, sessionID, "act-1")
	require.NoError(t, err)
	require.Len(t, results, 1, "a failed review is a report, not a review")
	require.Equal(t, Model{Provider: "openai", Model: "gpt-5.1"}, results[0].Model)
	require.Equal(t, "Verdict: rework.", results[0].Text)
	require.Equal(t, 125000*time.Millisecond, results[0].Duration)

	// A fresh store over the same database is what another process reads.
	other := &state.RunStore{DB: db}
	again, err := PlanReviewsForAction(context.Background(), other, sessionID, "act-1")
	require.NoError(t, err)
	require.Equal(t, results, again)
}

// A pending exit-plan request carries everything its card needs from the
// shared layer: the plan file under the session's own scope, the models a
// review may be handed to, and the reviews the conversation already holds.
func TestPendingExitPlanRequestCarriesPlanReviewsAndModels(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, runs, sessionID := planReviewEventDB(t)
	actions := &state.ActionService{DB: runs.DB}
	run, err := runs.CreateRun(ctx, sessionID, "plan the work")
	require.NoError(t, err)
	action, err := actions.CreatePending(ctx, sessionID, "exit_plan_mode", map[string]any{"session_id": sessionID})
	require.NoError(t, err)
	require.NoError(t, runs.SetWaitingAction(ctx, run.ID, state.Wait{
		RunID: run.ID, ActionID: action.ID, ToolName: "exit_plan_mode", ToolInputJSON: "{}",
	}))
	appendPlanReviewed(t, runs, sessionID, action.ID, "rev-done", "done", "Verdict: rework.")
	stateRoot := t.TempDir()
	require.NoError(t, state.SetPlanForSession(stateRoot, "proj", sessionID, "# Plan\n\n1. Ship it."))

	gate := &PendingApprovalGate{
		Runs: runs, Actions: actions,
		PlanScope: func(context.Context, string) (string, string) { return stateRoot, "proj" },
		ReviewModels: func(string) []PlanReviewModelOption {
			return []PlanReviewModelOption{
				{Provider: "openai", Model: "gpt-5.1", Current: true},
				{Provider: "zhipuai", Model: "glm-5.3-flash"},
			}
		},
	}
	req, err := gate.Pending(ctx, sessionID)
	require.NoError(t, err)
	require.NotNil(t, req)
	require.Equal(t, state.PlanPathForSession(stateRoot, "proj", sessionID), req.PlanFilePath)
	require.Len(t, req.PlanReviewModels, 2)
	require.True(t, req.PlanReviewModels[0].Current)
	require.Len(t, req.PlanReviews, 1)
	require.Equal(t, "gpt-5.1", req.PlanReviewModels[0].Model)
	require.Contains(t, req.PlanReviews[0].Text, "rework")
}

// Each reviewer offer names the endpoint its row sends to — the picker's second
// column — and the api path that column keeps when it is too narrow for the
// whole URL. Two rows of one provider/model are two offers, each naming its
// own endpoint.
func TestPlanReviewModelOptionsCarryEachRowsEndpoint(t *testing.T) {
	// The rows come from the install's model table, the one source whose ids
	// are kept verbatim; an id written into a definition's llm_providers list
	// is rewritten to a content hash.
	cfg := &appcfg.Root{LLMModels: []appcfg.LLMModelConfig{
		{ID: "row-1", Provider: "deepseek", Model: "deepseek-v4-flash", BaseURL: "http://one.test"},
		{ID: "row-2", Provider: "deepseek", Model: "deepseek-v4-flash", BaseURL: "http://two.test", APIPath: "/beta/chat/completions"},
		{ID: "row-3", Provider: "openai", Model: "gpt-5.1", BaseURL: "http://three.test/v1", URLVerbatim: true},
	}}
	options := PlanReviewModelOptions(cfg, "main", "", "deepseek", "deepseek-v4-flash")
	require.Len(t, options, 3)
	require.Equal(t, "row-1", options[0].ID)
	require.Equal(t, "http://one.test/chat/completions", options[0].Endpoint)
	require.Equal(t, "/chat/completions", options[0].EndpointTail)
	require.True(t, options[0].Current)
	require.Equal(t, "row-2", options[1].ID)
	require.Equal(t, "http://two.test/beta/chat/completions", options[1].Endpoint)
	require.Equal(t, "/beta/chat/completions", options[1].EndpointTail)
	// A selection that names no row — one recorded before ids existed — is a
	// statement about the pair, so both twin rows carry the mark, exactly as
	// they always did.
	require.True(t, options[1].Current)
	// A 「自定义协议」 row sends to its base url as stored: its own endpoint, and
	// no path to keep.
	require.Equal(t, "row-3", options[2].ID)
	require.Equal(t, "http://three.test/v1", options[2].Endpoint)
	require.Equal(t, "", options[2].EndpointTail)

	// A selection that names a row marks that row alone: the conversation runs
	// on row-2, so row-1 — same provider/model — is not what it is on.
	picked := PlanReviewModelOptions(cfg, "main", "row-2", "deepseek", "deepseek-v4-flash")
	require.False(t, picked[0].Current, "the twin row the conversation is not on must not be marked")
	require.True(t, picked[1].Current, "the row the conversation runs on is the marked one")
}

// TestPendingExitPlanRequestResolvesOnlyItsOwnSessionPlan is the gate-layer
// regression test for the cross-conversation plan leak: the approval card the
// user is looking at (TUI overlay and web card render the same request) must
// show this conversation's plan even when another conversation — or a legacy
// flat file — was written more recently.
func TestPendingExitPlanRequestResolvesOnlyItsOwnSessionPlan(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, runs, sessionID := planReviewEventDB(t)
	actions := &state.ActionService{DB: runs.DB}
	run, err := runs.CreateRun(ctx, sessionID, "plan the work")
	require.NoError(t, err)
	action, err := actions.CreatePending(ctx, sessionID, "exit_plan_mode", map[string]any{"session_id": sessionID})
	require.NoError(t, err)
	require.NoError(t, runs.SetWaitingAction(ctx, run.ID, state.Wait{
		RunID: run.ID, ActionID: action.ID, ToolName: "exit_plan_mode", ToolInputJSON: "{}",
	}))

	stateRoot := t.TempDir()
	require.NoError(t, state.SetPlanForSession(stateRoot, "proj", sessionID, "# Mine"))

	// Two foreign plans, both newer than the session's own: another
	// conversation's directory and the legacy flat project directory.
	foreign := func(dir, name string) {
		require.NoError(t, os.MkdirAll(dir, 0o755))
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, []byte("# Theirs\n"), 0o600))
		future := time.Now().Add(time.Hour)
		require.NoError(t, os.Chtimes(p, future, future))
	}
	foreign(state.PlanDirForSession(stateRoot, "proj", "another-session"), "theirs.md")
	foreign(state.PlanDirForProject(stateRoot, "proj"), "legacy.md")

	gate := &PendingApprovalGate{
		Runs: runs, Actions: actions,
		PlanScope: func(context.Context, string) (string, string) { return stateRoot, "proj" },
	}
	req, err := gate.Pending(ctx, sessionID)
	require.NoError(t, err)
	require.NotNil(t, req)
	require.Equal(t, state.PlanPathForSession(stateRoot, "proj", sessionID), req.PlanFilePath)
	body, err := os.ReadFile(req.PlanFilePath)
	require.NoError(t, err)
	require.Equal(t, "# Mine", strings.TrimSpace(string(body)))
}

// The delivery denial is the one write that closes a completed review's
// approval from the engine side: it lands only while the action is a pending
// exit-plan gate, and it carries the marker reason every surface recognizes.
func TestDeliverPlanReviewDeniesPendingExitPlan(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db, runs, sessionID := planReviewEventDB(t)
	actions := &state.ActionService{DB: runs.DB}
	planAction, err := actions.CreatePending(ctx, sessionID, "exit_plan_mode", map[string]any{"session_id": sessionID})
	require.NoError(t, err)
	shellAction, err := actions.CreatePending(ctx, sessionID, "shell", map[string]any{"command": "ls"})
	require.NoError(t, err)
	_ = db

	delivered, err := DeliverPlanReview(ctx, actions, planAction.ID)
	require.NoError(t, err)
	require.Equal(t, state.ActionDenied, delivered.Status)
	require.Equal(t, PlanReviewDeliveredReason, delivered.Error)

	// A decision the user already made wins the race: the delivery is a no-op,
	// not an overwrite.
	_, err = DeliverPlanReview(ctx, actions, planAction.ID)
	require.ErrorIs(t, err, state.ErrNotPending)

	// Only an exit-plan gate hands reviews back to the planner.
	_, err = DeliverPlanReview(ctx, actions, shellAction.ID)
	require.ErrorIs(t, err, state.ErrNotPending)
}

// Delivery guidance is the model input for a review handed back with no user
// decision behind it: the instruction header, then the reviews — and never a
// "user's own feedback" section, because the user has not spoken.
func TestReviewDeliveryGuidanceCarriesReviewWithoutUserFeedback(t *testing.T) {
	t.Parallel()

	got := ComposeReviewDeliveryGuidance([]PlanReviewResult{{
		Model: Model{Provider: "zhipuai", Model: "glm-5.3-flash"},
		Text:  "Verdict: approve with changes. Add a test for the cancel race.",
	}})
	for _, want := range []string{
		"The plan review you asked for has returned.",
		"call exit_plan_mode again",
		`<review model="zhipuai/glm-5.3-flash">`,
		"Add a test for the cancel race.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("delivery guidance missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "outranks the reviews") {
		t.Fatalf("delivery must not claim user feedback the user never gave:\n%s", got)
	}
}

// The stored marker reason is plumbing, not the user's words: the resume the
// planning model receives leads with the delivery instruction and carries the
// review, while every display of the action's own feedback sees none.
func TestBuildApprovalResumeTreatsDeliveredReasonAsNoUserWords(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, runs, sessionID := planReviewEventDB(t)
	action := &state.Action{
		ID: "act-1", SessionID: sessionID, Kind: "exit_plan_mode",
		Status: state.ActionDenied, Error: PlanReviewDeliveredReason,
		AnswerJSON: planReviewDeliveredAnswer,
	}
	appendPlanReviewed(t, runs, sessionID, "act-1", "rev-done", "done", "Verdict: rework the cache story.")

	out := BuildApprovalResumeWithPlanReviews(ctx, runs, action, nil, "exit_plan_mode", false)
	if !out.Denied {
		t.Fatal("the delivered action resumes as denied")
	}
	if !out.DeliveredReview {
		t.Fatal("the delivery must mark the resume: its display half has no user words")
	}
	for _, want := range []string{
		"The plan review you asked for has returned.",
		"Verdict: rework the cache story.",
	} {
		if !strings.Contains(out.Reason, want) {
			t.Fatalf("resume reason missing %q:\n%s", want, out.Reason)
		}
	}
	if strings.Contains(out.Reason, PlanReviewDeliveredReason) {
		t.Fatalf("the marker must not reach the model:\n%s", out.Reason)
	}
	if out.Feedback != "" {
		t.Fatalf("Feedback = %q, want empty: the marker is not the user's words", out.Feedback)
	}

	// A denial the user typed keeps its shape: no delivery flag, their words
	// are both halves' anchor.
	plain := BuildApprovalResumeWithPlanReviews(ctx, runs, &state.Action{
		ID: "act-2", SessionID: sessionID, Kind: "exit_plan_mode",
		Status: state.ActionDenied, Error: "改成先写测试",
	}, nil, "exit_plan_mode", false)
	if plain.DeliveredReview || plain.Feedback != "改成先写测试" {
		t.Fatalf("plain denial = %+v, want the user's words and no delivery flag", plain)
	}
}

// The marker string is also a sentence a curious user can type verbatim as
// their deny feedback. Only the delivery's structured answer stamp tells the
// two apart, so the typed words stay the user's: kept as Feedback, composed
// into the model-facing reason, no delivery flag.
func TestManualDenyTypingTheMarkerKeepsTheUsersWords(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, runs, sessionID := planReviewEventDB(t)
	action := &state.Action{
		ID: "act-typed", SessionID: sessionID, Kind: "exit_plan_mode",
		Status: state.ActionDenied, Error: PlanReviewDeliveredReason,
	}
	appendPlanReviewed(t, runs, sessionID, "act-typed", "rev-done", "done", "Verdict: rework the cache story.")

	out := BuildApprovalResumeWithPlanReviews(ctx, runs, action, nil, "exit_plan_mode", false)
	if !out.Denied {
		t.Fatal("a manual denial resumes as denied")
	}
	if out.DeliveredReview {
		t.Fatal("a typed marker is the user's words, not a delivery")
	}
	if out.Feedback != PlanReviewDeliveredReason {
		t.Fatalf("Feedback = %q, want the user's typed words kept", out.Feedback)
	}
	if !strings.Contains(out.Reason, PlanReviewDeliveredReason) {
		t.Fatalf("the typed words must reach the model inside the composed reason:\n%s", out.Reason)
	}
	if !strings.Contains(out.Reason, "Verdict: rework the cache story.") {
		t.Fatalf("the review must still ride along:\n%s", out.Reason)
	}
}

// The delivery denial carries a second read the user's own deny cannot set:
// the structured answer stamp, which is what lets every consumer treat the
// marker in the reason column as plumbing rather than words someone typed.
func TestDeliverPlanReviewStampsTheAnswer(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, runs, sessionID := planReviewEventDB(t)
	actions := &state.ActionService{DB: runs.DB}
	planAction, err := actions.CreatePending(ctx, sessionID, "exit_plan_mode", map[string]any{"session_id": sessionID})
	require.NoError(t, err)

	delivered, err := DeliverPlanReview(ctx, actions, planAction.ID)
	require.NoError(t, err)
	require.Equal(t, state.ActionDenied, delivered.Status)
	require.Equal(t, PlanReviewDeliveredReason, delivered.Error)
	require.Contains(t, delivered.AnswerJSON, "plan_review_delivered")

	stored, err := actions.Get(ctx, planAction.ID)
	require.NoError(t, err)
	if !ActionIsReviewDelivered(stored) {
		t.Fatalf("the delivered row must satisfy the discriminator: %+v", stored)
	}

	// The marker alone is not a delivery: a manual deny stores the reason and
	// nothing in the answer column.
	manualAction, err := actions.CreatePending(ctx, sessionID, "exit_plan_mode", map[string]any{"session_id": sessionID})
	require.NoError(t, err)
	_, err = actions.Deny(ctx, manualAction.ID, PlanReviewDeliveredReason)
	require.NoError(t, err)
	manual, err := actions.Get(ctx, manualAction.ID)
	require.NoError(t, err)
	if ActionIsReviewDelivered(manual) {
		t.Fatalf("a typed marker without the stamp is the user's words: %+v", manual)
	}
}

// Every surface that records an approval gate names the gated call through
// ApprovalToolStepID, so the record anchors to the same call whichever
// surface wrote it.
func TestApprovalToolStepIDNamesTheGatedCall(t *testing.T) {
	t.Parallel()

	require.Equal(t, "", ApprovalToolStepID(nil))

	withOrigin := &tool.RequiresActionError{
		OriginatingToolCallID: "call-orig",
		ToolName:              "exit_plan_mode",
		SessionSnapshot: []llm.Message{
			llm.AssistantMessage(nil,
				llm.ToolCall{ID: "call-other", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "exit_plan_mode", Arguments: "{}"}},
			),
		},
	}
	require.Equal(t, "call-orig", ApprovalToolStepID(withOrigin))

	fromSnapshot := &tool.RequiresActionError{
		ToolName: "exit_plan_mode",
		SessionSnapshot: []llm.Message{
			llm.UserMessage(llm.Text("plan")),
			llm.AssistantMessage(nil,
				llm.ToolCall{ID: "call-exit-1", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "exit_plan_mode", Arguments: "{}"}},
			),
		},
	}
	require.Equal(t, "call-exit-1", ApprovalToolStepID(fromSnapshot))
}

func newPlanReviewTaskStore(t *testing.T) *state.SessionStore {
	t.Helper()
	store, _ := newPlanReviewTaskDB(t)
	return store
}

func newPlanReviewTaskDB(t *testing.T) (*state.SessionStore, *sql.DB) {
	t.Helper()
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	store := state.NewSessionStore(db, "main")
	require.NoError(t, store.Ensure(ctx, "s1", "s1"))
	return store, db
}

func appendUserTurn(t *testing.T, store *state.SessionStore, text string) {
	t.Helper()
	require.NoError(t, store.AppendNewMessages(context.Background(), "s1", "", []llm.Message{
		llm.UserMessage(llm.Text(text)),
	}, "", ""))
}

func appendInvestigationRows(t *testing.T, store *state.SessionStore, n int) {
	t.Helper()
	rows := make([]llm.Message, 0, n)
	for i := 0; i < n; i++ {
		rows = append(rows, llm.AssistantMessage([]llm.ContentPart{
			llm.Text(fmt.Sprintf("investigation step %d: reading the installer", i)),
		}))
	}
	require.NoError(t, store.AppendNewMessages(context.Background(), "s1", "", rows, "", ""))
}

func metaEnvironmentMessage() llm.Message {
	msg := llm.UserMessage(llm.Text("<forebrain_environment_context>\nproject=/work\n</forebrain_environment_context>"))
	msg.IsMeta = true
	return msg
}

// A plan-mode turn writes hundreds of the agent's own rows after the request.
// The reviewer's material must still be the request, not whatever the newest
// transcript rows happen to be.
func TestPlanReviewTaskContextSurvivesALongInvestigation(t *testing.T) {
	ctx := context.Background()
	store := newPlanReviewTaskStore(t)
	require.NoError(t, store.AppendNewMessages(ctx, "s1", "", []llm.Message{
		llm.UserMessage(llm.Text("Add S3 sync to the skill installer.")),
	}, "", ""))
	appendInvestigationRows(t, store, 300)
	require.NoError(t, store.AppendNewMessages(ctx, "s1", "", []llm.Message{
		metaEnvironmentMessage(),
	}, "", ""))

	got, _ := planReviewTaskContext(ctx, store, "s1", time.Time{})
	require.Contains(t, got, "Add S3 sync to the skill installer.")
	require.NotContains(t, got, "forebrain_environment_context")
}

// The runtime-injected environment block is stored as a user row. It is not
// something the person asked for, so it is not reviewer material.
func TestPlanReviewTaskContextSkipsRuntimeMetaRows(t *testing.T) {
	ctx := context.Background()
	store := newPlanReviewTaskStore(t)
	require.NoError(t, store.AppendNewMessages(ctx, "s1", "", []llm.Message{
		llm.UserMessage(llm.Text("Add S3 sync to the skill installer.")),
		metaEnvironmentMessage(),
	}, "", ""))

	got, _ := planReviewTaskContext(ctx, store, "s1", time.Time{})
	require.Contains(t, got, "Add S3 sync to the skill installer.")
	require.NotContains(t, got, "forebrain_environment_context")
}

// A long conversation is the case the review matters most for. Every message
// the person sent reaches the reviewer, oldest first — the first one usually
// states the task — however many turns and injected meta rows came since.
func TestPlanReviewTaskContextKeepsEveryMessageOfALongConversation(t *testing.T) {
	ctx := context.Background()
	store := newPlanReviewTaskStore(t)
	appendUserTurn(t, store, "/improve execute 282")
	for i := 1; i <= 9; i++ {
		require.NoError(t, store.AppendNewMessages(ctx, "s1", "", []llm.Message{metaEnvironmentMessage()}, "", ""))
		appendInvestigationRows(t, store, 20)
		appendUserTurn(t, store, fmt.Sprintf("follow-up %d", i))
	}

	got, _ := planReviewTaskContext(ctx, store, "s1", time.Time{})
	require.True(t, strings.HasPrefix(got, "<message n=\"1\">\n/improve execute 282\n</message>"), got)
	last := -1
	for i := 1; i <= 9; i++ {
		at := strings.Index(got, fmt.Sprintf("<message n=\"%d\">\nfollow-up %d\n</message>", i+1, i))
		require.Greater(t, at, last, "follow-up %d missing or out of order:\n%s", i, got)
		last = at
	}
	require.NotContains(t, got, "forebrain_environment_context")
	require.NotContains(t, got, "left out")
}

// What the user decided in a form and what they said when they sent a plan
// back are as binding as their messages. A denial the engine wrote to deliver
// a review is not the user's and stays out.
func TestPlanReviewTaskContextCarriesAnswersAndPlanFeedback(t *testing.T) {
	ctx := context.Background()
	store, db := newPlanReviewTaskDB(t)
	actions := &state.ActionService{DB: db}
	appendUserTurn(t, store, "Add S3 sync to the skill installer.")

	ask, err := actions.CreateAsk(ctx, state.AskForm{SessionID: "s1", Questions: []state.AskQuestion{{
		ID: "Scope", Prompt: "Sync on install only, or on every run?",
		Options: []state.AskOption{{ID: "Install only", Label: "Install only"}, {ID: "Every run", Label: "Every run"}},
	}}})
	require.NoError(t, err)
	_, err = actions.AnswerAsk(ctx, ask.ID, state.AskAnswer{Answers: []state.AskAnswerItem{
		{QuestionID: "Scope", OptionIDs: []string{"Install only"}},
	}})
	require.NoError(t, err)

	rejected, err := actions.CreatePending(ctx, "s1", "exit_plan_mode", map[string]any{"action": "exit"})
	require.NoError(t, err)
	_, err = actions.Deny(ctx, rejected.ID, "Keep the old bucket layout.")
	require.NoError(t, err)

	delivered, err := actions.CreatePending(ctx, "s1", "exit_plan_mode", map[string]any{"action": "exit"})
	require.NoError(t, err)
	_, err = DeliverPlanReview(ctx, actions, delivered.ID)
	require.NoError(t, err)

	got, _ := planReviewTaskContext(ctx, store, "s1", time.Time{})
	require.Contains(t, got, "<message n=\"1\">\nAdd S3 sync to the skill installer.\n</message>")
	require.Contains(t, got, "<answers>\nSync on install only, or on every run?\n  → Install only\n</answers>")
	require.Contains(t, got, "<plan_feedback>\nKeep the old bucket layout.\n</plan_feedback>")
	require.NotContains(t, got, PlanReviewDeliveredReason)
	require.Less(t, strings.Index(got, "<message"), strings.Index(got, "<answers>"))
}

// When everything does not fit, the newest message — what the plan is for —
// and the first one — what started the work — are kept, the rest fills from
// newest back, and what is left out is said so where it was. The result fits
// the bound BuildPrompt applies, so BuildPrompt never cuts the newest entry
// off the end.
func TestPlanReviewTaskContextKeepsTheFirstAndNewestMessageWhenItDoesNotFit(t *testing.T) {
	ctx := context.Background()
	store := newPlanReviewTaskStore(t)
	appendUserTurn(t, store, "Add S3 sync to the skill installer.")
	for i := 0; i < 12; i++ {
		appendUserTurn(t, store, fmt.Sprintf("middle turn %d: %s", i, strings.Repeat("a", 3000)))
	}
	appendUserTurn(t, store, "Now also cover the uninstall path.")

	got, _ := planReviewTaskContext(ctx, store, "s1", time.Time{})
	require.True(t, strings.HasPrefix(got, "<message n=\"1\">\nAdd S3 sync to the skill installer.\n</message>"), got[:200])
	require.True(t, strings.HasSuffix(got, "<message n=\"14\">\nNow also cover the uninstall path.\n</message>"))
	require.Regexp(t, `</message>\n\n\[\d+ entries left out to fit the length limit\]\n\n<message n="\d+">\nmiddle turn`, got)
	require.Contains(t, got, "middle turn 11:", "the newest middle turns are kept before older ones")
	require.NotContains(t, got, "middle turn 0:")
	require.LessOrEqual(t, len([]rune(got)), MaxTaskChars)
	require.Equal(t, got, strings.TrimSpace(Truncate(got, MaxTaskChars)))
}

// A screenshot is part of what the user said. Every message that attached
// one is handed over with its number, whether or not its text fit, and a
// message that sent only images keeps its place in the request.
func TestPlanReviewTaskContextHandsOverEveryMessagesImages(t *testing.T) {
	ctx := context.Background()
	store := newPlanReviewTaskStore(t)
	appendUserTurn(t, store, "Fix the header colour.")
	require.NoError(t, store.AppendNewMessages(ctx, "s1", "", []llm.Message{
		llm.UserMessage(llm.Text("[Image #1] like this"), llm.ImageBase64("image/png", "iVBORw0KGgo=")),
	}, "", ""))
	require.NoError(t, store.AppendNewMessages(ctx, "s1", "", []llm.Message{
		llm.UserMessage(llm.ImageBase64("image/png", "iVBORw0KGgo=")),
	}, "", ""))
	// A row written before the display form was split still carries the path
	// note and the marker inside its content: the reviewer gets the image and
	// its note after it, so neither line belongs in the message text again.
	require.NoError(t, store.AppendNewMessages(ctx, "s1", "", []llm.Message{
		llm.UserMessage(llm.Text("and here\n[Image: source: /tmp/a.png]\n[Image #1]"), llm.ImageBase64("image/png", "iVBORw0KGgo=")),
	}, "", ""))

	// The zero since keeps the whole request: this is a fresh review's
	// context (the Again path is covered by its own test below).
	got, images := planReviewTaskContext(ctx, store, "s1", time.Time{})
	require.Contains(t, got, "<message n=\"2\">\nlike this\n</message>")
	require.Contains(t, got, "<message n=\"3\">\n"+planReviewImageOnly+"\n</message>")
	require.Contains(t, got, "<message n=\"4\">\nand here\n</message>")
	require.NotContains(t, got, "[Image #1]")
	require.NotContains(t, got, "[Image: source:")
	require.Len(t, images, 3)
	require.Equal(t, 2, images[0].Message)
	require.Equal(t, 3, images[1].Message)
	require.Equal(t, 4, images[2].Message)
	require.Contains(t, images[0].PartsJSON, "iVBORw0KGgo=")
}

// A failure the engine classified travels with its code, so a surface can say
// it in the viewer's language; the sentence stays for everything else.
func TestRunPlanReviewCarriesAClassifiedFailuresCode(t *testing.T) {
	ctx := context.Background()
	stateRoot := t.TempDir()
	require.NoError(t, state.SetPlanForSession(stateRoot, "proj", "s1", "# Plan"))
	var events []event.RunEvent
	err := RunPlanReview(ctx, PlanReviewRun{
		ActionID: "a1", Model: Model{Provider: "acme", Model: "text-only"},
		SessionID: "s1", StateRoot: stateRoot, ProjectKey: "proj",
		Reviewer: failingReviewer{err: codedReviewError{code: "image_input_unsupported"}},
		Publish: func(_ context.Context, evt event.RunEvent) error {
			events = append(events, evt)
			return nil
		},
	})
	require.Error(t, err)
	var reviewed event.PlanReviewedPayload
	for _, evt := range events {
		if evt.Type == event.RunEventPlanReviewed {
			require.NoError(t, json.Unmarshal(evt.Payload, &reviewed))
		}
	}
	require.Equal(t, planReviewOutcomeFailed, reviewed.Outcome)
	require.Equal(t, "image_input_unsupported", reviewed.Code)
	require.Equal(t, "coded failure", reviewed.Error)
}

type codedReviewError struct{ code string }

func (e codedReviewError) Error() string { return "coded failure" }
func (e codedReviewError) Code() string  { return e.code }

type failingReviewer struct{ err error }

func (r failingReviewer) Review(context.Context, Request) (PlanReviewResult, error) {
	return PlanReviewResult{}, r.err
}

type recordingReviewer struct {
	task     string
	plan     string
	planPath string
}

func (r *recordingReviewer) Review(_ context.Context, req Request) (PlanReviewResult, error) {
	r.task = req.Task
	r.plan = req.Plan
	r.planPath = req.PlanPath
	return PlanReviewResult{Model: req.Model, Text: "looks fine"}, nil
}

// The wiring-level counterpart: what the reviewer is handed through
// RunPlanReview is the person's request, not the fallback.
func TestRunPlanReviewHandsTheRequestToTheReviewer(t *testing.T) {
	ctx := context.Background()
	store := newPlanReviewTaskStore(t)
	require.NoError(t, store.AppendNewMessages(ctx, "s1", "", []llm.Message{
		llm.UserMessage(llm.Text("Add S3 sync to the skill installer.")),
	}, "", ""))
	appendInvestigationRows(t, store, 300)

	stateRoot := t.TempDir()
	require.NoError(t, state.SetPlanForSession(stateRoot, "proj", "s1", "# Plan\n\nbody"))

	reviewer := &recordingReviewer{}
	require.NoError(t, RunPlanReview(ctx, PlanReviewRun{
		ActionID: "a1", Model: Model{Provider: "openai", Model: "gpt-4o"},
		SessionID: "s1", StateRoot: stateRoot, ProjectKey: "proj",
		Transcripts: store, Reviewer: reviewer,
	}))
	require.Contains(t, reviewer.task, "Add S3 sync to the skill installer.")
	require.Contains(t, reviewer.plan, "# Plan")
	require.Equal(t, state.PlanPathForSession(stateRoot, "proj", "s1"), reviewer.planPath)
}

// The row a review runs on is part of its record, and it survives the round
// trip: both events name it, and reading the conversation's reviews back says
// which row answered — the pair alone cannot, when it is configured twice.
func TestRunPlanReviewRecordsTheRowItRanOn(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, runs, sessionID := planReviewEventDB(t)
	stateRoot := t.TempDir()
	require.NoError(t, state.SetPlanForSession(stateRoot, "proj", sessionID, "# Plan"))

	var events []event.RunEvent
	require.NoError(t, RunPlanReview(ctx, PlanReviewRun{
		ActionID: "act-row", Model: Model{ID: "row-2", Provider: "openai", Model: "gpt-4o"},
		SessionID: sessionID, StateRoot: stateRoot, ProjectKey: "proj",
		Reviewer: &recordingReviewer{},
		Publish: func(ctx context.Context, evt event.RunEvent) error {
			events = append(events, evt)
			// The bus writes the event where every reader of the conversation
			// finds it; this stub is that write.
			_, err := runs.AppendSessionEvent(ctx, state.SessionEvent{
				ID: evt.ID, SessionID: sessionID, Type: evt.Type, Payload: evt.Payload,
			})
			return err
		},
	}))

	var started event.PlanReviewStartedPayload
	var reviewed event.PlanReviewedPayload
	for _, evt := range events {
		switch evt.Type {
		case event.RunEventPlanReviewStarted:
			require.NoError(t, json.Unmarshal(evt.Payload, &started))
		case event.RunEventPlanReviewed:
			require.NoError(t, json.Unmarshal(evt.Payload, &reviewed))
		}
	}
	require.Equal(t, "row-2", started.ModelID, "the opening event names the row the review runs on")
	require.Equal(t, "row-2", reviewed.ModelID)
	require.Equal(t, started.ReviewID, reviewed.ReviewID)
	require.Equal(t, planReviewOutcomeDone, reviewed.Outcome)

	results, err := PlanReviewsForAction(ctx, runs, sessionID, "act-row")
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, Model{ID: "row-2", Provider: "openai", Model: "gpt-4o"}, results[0].Model)
}

// TestDecideHandOffModelValidatedAndRecorded pins the hand-off decision's
// rules (语义 2): only an approved exit-plan decision may carry it, it cannot
// combine with clear-context or plan-only, and it must name a configured row
// by id. A valid one lands in answer_json.hand_off_model and replays
// idempotently; a different model on the same action conflicts.
func TestDecideHandOffModelValidatedAndRecorded(t *testing.T) {
	handOff := &Model{ID: "llm_row2", Provider: "zhipuai", Model: "glm-5.3"}

	// Non exit_plan_mode kind.
	store := &approvalStoreStub{action: &state.Action{ID: "a1", Kind: "shell", Status: state.ActionPending, PayloadJSON: `{}`}}
	_, err := (ApprovalService{Actions: store}).Decide(context.Background(), "a1", ApprovalReply{HandOffModel: handOff})
	require.ErrorContains(t, err, "hand off is only valid")
	require.Equal(t, state.ActionPending, store.action.Status)

	// A denied exit-plan decision cannot hand off.
	store = &approvalStoreStub{action: &state.Action{ID: "a2", Kind: "exit_plan_mode", Status: state.ActionPending, PayloadJSON: `{}`}}
	_, err = (ApprovalService{Actions: store}).Decide(context.Background(), "a2", ApprovalReply{Choice: "deny", HandOffModel: handOff})
	require.ErrorContains(t, err, "hand off is only valid")
	require.Equal(t, state.ActionPending, store.action.Status)

	// hand_off_model cannot combine with clear context or plan only.
	for name, reply := range map[string]ApprovalReply{
		"clear context": {ClearContext: true, HandOffModel: handOff},
		"plan only":     {PlanOnly: true, HandOffModel: handOff},
	} {
		store = &approvalStoreStub{action: &state.Action{ID: "a3", Kind: "exit_plan_mode", Status: state.ActionPending, PayloadJSON: `{}`}}
		_, err = (ApprovalService{Actions: store}).Decide(context.Background(), "a3", reply)
		require.ErrorContains(t, err, "cannot be combined", name)
		require.Equal(t, state.ActionPending, store.action.Status, name)
	}

	// The row id and the model name are both required: a hand-off resolves the
	// row by id later, so a pair alone is not an identity.
	for name, model := range map[string]*Model{
		"no id":    {Provider: "zhipuai", Model: "glm-5.3"},
		"no model": {ID: "llm_row2", Provider: "zhipuai"},
	} {
		store = &approvalStoreStub{action: &state.Action{ID: "a4", Kind: "exit_plan_mode", Status: state.ActionPending, PayloadJSON: `{}`}}
		_, err = (ApprovalService{Actions: store}).Decide(context.Background(), "a4", ApprovalReply{HandOffModel: model})
		require.ErrorContains(t, err, "hand off requires a configured model id", name)
		require.Equal(t, state.ActionPending, store.action.Status, name)
	}

	// The valid decision records the row in the answer.
	store = &approvalStoreStub{action: &state.Action{ID: "a5", Kind: "exit_plan_mode", Status: state.ActionPending, PayloadJSON: `{}`}}
	service := ApprovalService{Actions: store}
	result, err := service.Decide(context.Background(), "a5", ApprovalReply{HandOffModel: handOff})
	require.NoError(t, err)
	require.True(t, result.Approved)
	require.Contains(t, store.action.AnswerJSON, `"hand_off_model":{"id":"llm_row2","provider":"zhipuai","model":"glm-5.3"}`)

	got, ok := ActionHandOffModel(store.action)
	require.True(t, ok)
	require.Equal(t, Model{ID: "llm_row2", Provider: "zhipuai", Model: "glm-5.3"}, got)
	if _, ok := ActionHandOffModel(&state.Action{Status: state.ActionApproved, Kind: "exit_plan_mode", AnswerJSON: `{}`}); ok {
		t.Fatal("an answer without a hand-off reported one")
	}

	// The same decision replays idempotently; a different model conflicts.
	retry, err := service.Decide(context.Background(), "a5", ApprovalReply{HandOffModel: handOff})
	require.NoError(t, err)
	require.True(t, retry.Approved)
	require.True(t, retry.Idempotent)
	other := &Model{ID: "llm_row9", Provider: "zhipuai", Model: "glm-5.3"}
	if _, err := service.Decide(context.Background(), "a5", ApprovalReply{HandOffModel: other}); !errors.Is(err, ErrApprovalConflict) {
		t.Fatalf("different model on the decided action = %v, want ErrApprovalConflict", err)
	}
}

// gateEventsRuns serves one parked run plus a canned event log, the shape the
// previous-review and hand-off facts are derived from.
type gateEventsRuns struct {
	fakeApprovalRuns
	events map[string][]state.SessionEvent
}

func (f *gateEventsRuns) ListSessionEventsOfType(_ context.Context, _ string, eventType string, _ int) ([]state.SessionEvent, error) {
	return f.events[eventType], nil
}

func gateEventRow(t *testing.T, id, eventType string, payload any, at time.Time) state.SessionEvent {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return state.SessionEvent{ID: id, Type: eventType, Payload: raw, CreatedAt: at}
}

// TestLastPlanReviewTakesTheNewestStartedWithSpawn pins 语义 6's "previous
// review": the newest plan_review_started the conversation holds, whatever its
// outcome and whichever approval it served, offered back only when that very
// review dispatched a subagent — an older review behind a spawnless newest one
// is stale, not "the previous review".
func TestLastPlanReviewTakesTheNewestStartedWithSpawn(t *testing.T) {
	at := time.Now().UTC()
	startedOf := func(reviewID, actionID, rowID string, ts time.Time) state.SessionEvent {
		return gateEventRow(t, reviewID+":started", event.RunEventPlanReviewStarted,
			event.PlanReviewStartedPayload{ActionID: actionID, ReviewID: reviewID, ModelID: rowID, Provider: "zhipuai", Model: "glm-5.3"}, ts)
	}
	spawnOf := func(reviewID, agentID, taskID string) state.SessionEvent {
		return gateEventRow(t, reviewID+":spawn", event.RunEventSubagentSpawned,
			event.SubagentSpawnedPayload{AgentID: agentID, TaskID: taskID, ParentToolCallID: reviewID}, at.Add(time.Second))
	}
	olderAt, newerAt := at.Add(-time.Minute), at
	runs := &gateEventsRuns{events: map[string][]state.SessionEvent{
		event.RunEventPlanReviewStarted: {
			startedOf("plan-review:old", "act-1", "llm_row1", olderAt),
			startedOf("plan-review:new", "act-2", "llm_row2", newerAt),
		},
		event.RunEventSubagentSpawned: {
			spawnOf("plan-review:old", "agent-old", "task-old"),
			spawnOf("plan-review:new", "agent-new", "task-new"),
		},
	}}
	again, model, ok := LastPlanReview(context.Background(), runs, "s1")
	require.True(t, ok)
	require.Equal(t, "plan-review:new", again.ReviewID)
	require.Equal(t, "agent-new", again.AgentID)
	require.Equal(t, "task-new", again.TaskID)
	require.Equal(t, newerAt, again.StartedAt)
	require.Equal(t, Model{ID: "llm_row2", Provider: "zhipuai", Model: "glm-5.3"}, model)

	// The newest started has no spawn: no continuation, and no falling back to
	// the older review behind one.
	runs.events[event.RunEventSubagentSpawned] = []state.SessionEvent{spawnOf("plan-review:old", "agent-old", "task-old")}
	if _, _, ok := LastPlanReview(context.Background(), runs, "s1"); ok {
		t.Fatal("a newest review without a subagent must not fall back to an older one")
	}

	// A spawn whose record lost its agent or task keys cannot be continued.
	runs.events[event.RunEventSubagentSpawned] = []state.SessionEvent{spawnOf("plan-review:new", "", "task-new")}
	if _, _, ok := LastPlanReview(context.Background(), runs, "s1"); ok {
		t.Fatal("a spawn without an agent id must not be continuable")
	}
}

// TestPendingForRunOffersPreviousReviewAndHandOff pins the gate's two new
// facts: PreviousPlanReview is the newest continuable review whose model is
// still configured, and HandOffAvailable needs a git repository plus a model
// that is not the current one — the models are checked before git runs, so a
// session with nothing to hand to never spawns the subprocess.
func TestPendingForRunOffersPreviousReviewAndHandOff(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "init")

	at := time.Now().UTC()
	models := []PlanReviewModelOption{
		{ID: "llm_row1", Provider: "zhipuai", Model: "glm-5.3", Current: true},
		{ID: "llm_row2", Provider: "deepseek", Model: "deepseek-v4-flash"},
	}
	runs := &gateEventsRuns{fakeApprovalRuns: fakeApprovalRuns{
		waitingSession: "s1", runID: "run-1",
		wait: &state.Wait{RunID: "run-1", ActionID: "act-1", ToolName: "exit_plan_mode"},
	}, events: map[string][]state.SessionEvent{
		event.RunEventPlanReviewStarted: {
			gateEventRow(t, "e1", event.RunEventPlanReviewStarted,
				event.PlanReviewStartedPayload{ActionID: "act-1", ReviewID: "plan-review:r1", ModelID: "llm_row2", Provider: "deepseek", Model: "deepseek-v4-flash"}, at),
		},
		event.RunEventSubagentSpawned: {
			gateEventRow(t, "e2", event.RunEventSubagentSpawned,
				event.SubagentSpawnedPayload{AgentID: "agent-r1", TaskID: "task-r1", ParentToolCallID: "plan-review:r1"}, at.Add(time.Second)),
		},
	}}
	gitCalls := 0
	gate := &PendingApprovalGate{
		Runs:         runs,
		Actions:      &fakeApprovalActions{action: &state.Action{Kind: "exit_plan_mode"}},
		ReviewModels: func(string) []PlanReviewModelOption { return models },
		ProjectRoot: func(context.Context, string) string {
			gitCalls++
			return repo
		},
	}
	req, err := gate.PendingForRun(context.Background(), "s1", "run-1")
	require.NoError(t, err)
	require.NotNil(t, req)
	require.True(t, req.HandOffAvailable)
	require.Equal(t, 1, gitCalls)
	if req.PreviousPlanReview == nil {
		t.Fatal("PreviousPlanReview = nil, want the newest continuable review")
	}
	require.Equal(t, "plan-review:r1", req.PreviousPlanReview.ReviewID)
	require.Equal(t, "task-r1", req.PreviousPlanReview.TaskID)
	require.Equal(t, "llm_row2", req.PreviousPlanReview.Model.ID)

	// The review's model left the configuration: no row to offer back.
	gate.ReviewModels = func(string) []PlanReviewModelOption { return models[:1] }
	req, err = gate.PendingForRun(context.Background(), "s1", "run-1")
	require.NoError(t, err)
	if req.PreviousPlanReview != nil {
		t.Fatal("a review whose model is gone was still offered")
	}

	// Only the current model configured: no hand-off, and git never runs.
	gate.ReviewModels = func(string) []PlanReviewModelOption { return models[:1] }
	before := gitCalls
	req, err = gate.PendingForRun(context.Background(), "s1", "run-1")
	require.NoError(t, err)
	require.False(t, req.HandOffAvailable)
	require.Equal(t, before, gitCalls, "the git subprocess ran although no model is available")

	// A project outside any repository cannot take a hand-off.
	gate.ReviewModels = func(string) []PlanReviewModelOption { return models }
	gate.ProjectRoot = func(context.Context, string) string { return t.TempDir() }
	req, err = gate.PendingForRun(context.Background(), "s1", "run-1")
	require.NoError(t, err)
	require.False(t, req.HandOffAvailable)
}

// againRecordingReviewer records the whole request a continuation is handed.
type againRecordingReviewer struct{ req Request }

func (r *againRecordingReviewer) Review(_ context.Context, req Request) (PlanReviewResult, error) {
	r.req = req
	return PlanReviewResult{Model: req.Model, Text: "still fine"}, nil
}

// TestRunPlanReviewAgainContinuesThePreviousReview pins the continuation's
// wiring: the started event names the review it continues and still gets its
// own id, the reviewer is handed the Again record, and its task context holds
// only what was said since the previous review started.
func TestRunPlanReviewAgainContinuesThePreviousReview(t *testing.T) {
	ctx := context.Background()
	store := newPlanReviewTaskStore(t)
	appendUserTurn(t, store, "Add S3 sync to the skill installer.")
	// At is Unix seconds and a same-second input stays (the review itself may
	// have started between two rows of one second), so the new input has to
	// land in a later second than the old one for the cutoff to divide them.
	oldInputs, err := store.ListUserInputs(ctx, "s1")
	require.NoError(t, err)
	require.NotEmpty(t, oldInputs)
	oldAt := oldInputs[0].At
	if wait := time.Until(time.Unix(oldAt+1, 0).Add(300 * time.Millisecond)); wait > 0 {
		time.Sleep(wait)
	}
	appendUserTurn(t, store, "Use the blue theme instead.")
	cutoff := time.Unix(oldAt+1, 0)

	stateRoot := t.TempDir()
	require.NoError(t, state.SetPlanForSession(stateRoot, "proj", "s1", "# Plan\n\nbody"))
	var events []event.RunEvent
	reviewer := &againRecordingReviewer{}
	err = RunPlanReview(ctx, PlanReviewRun{
		ActionID: "a2", Model: Model{ID: "llm_row2", Provider: "deepseek", Model: "deepseek-v4-flash"},
		SessionID: "s1", StateRoot: stateRoot, ProjectKey: "proj",
		Transcripts: store, Reviewer: reviewer,
		Again: &PlanReviewAgain{ReviewID: "plan-review:r1", AgentID: "agent-r1", TaskID: "task-r1", StartedAt: cutoff},
		Publish: func(_ context.Context, evt event.RunEvent) error {
			events = append(events, evt)
			return nil
		},
	})
	require.NoError(t, err)

	var started event.PlanReviewStartedPayload
	var reviewed event.PlanReviewedPayload
	for _, evt := range events {
		switch evt.Type {
		case event.RunEventPlanReviewStarted:
			require.NoError(t, json.Unmarshal(evt.Payload, &started))
		case event.RunEventPlanReviewed:
			require.NoError(t, json.Unmarshal(evt.Payload, &reviewed))
		}
	}
	require.Equal(t, "plan-review:r1", started.ContinuesReviewID)
	require.NotEqual(t, "plan-review:r1", started.ReviewID, "the continuation gets its own review id")
	require.Equal(t, started.ReviewID, reviewed.ReviewID)
	require.Equal(t, "done", reviewed.Outcome)

	require.NotNil(t, reviewer.req.Again)
	require.Equal(t, "plan-review:r1", reviewer.req.Again.ReviewID)
	require.Equal(t, "task-r1", reviewer.req.Again.TaskID)
	require.Contains(t, reviewer.req.Task, "Use the blue theme instead.")
	require.NotContains(t, reviewer.req.Task, "Add S3 sync to the skill installer.",
		"the reviewer already saw the earlier request; only what came after its review belongs here")
}

// Stopping a review is cancelling its context — the surface's Esc reaches
// exactly that context. The closing plan_reviewed(stopped) is the review's
// only trace, so it must still publish even though the context it ran under is
// gone: a sink that honours the context (as the store-backed one does) would
// otherwise drop the record of the stop (plan 283's driver run caught this
// live: subagent_ended(cancelled) landed, plan_reviewed never did).
func TestRunPlanReviewStopStillRecordsTheOutcome(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	stateRoot := t.TempDir()
	require.NoError(t, state.SetPlanForSession(stateRoot, "proj", "s1", "# Plan"))
	var events []event.RunEvent
	publish := func(pctx context.Context, evt event.RunEvent) error {
		if err := pctx.Err(); err != nil {
			return err // the store-backed sink's own behaviour on a dead context
		}
		events = append(events, evt)
		return nil
	}
	stoppingReviewer := stoppingReviewerFunc(func(rctx context.Context, _ Request) (PlanReviewResult, error) {
		cancel() // what Esc does to the review's own context
		<-rctx.Done()
		return PlanReviewResult{}, context.Canceled
	})
	err := RunPlanReview(ctx, PlanReviewRun{
		ActionID: "a1", Model: Model{Provider: "acme", Model: "text-only"},
		SessionID: "s1", StateRoot: stateRoot, ProjectKey: "proj",
		Reviewer: stoppingReviewer,
		Publish:  publish,
	})
	require.ErrorIs(t, err, context.Canceled)
	var reviewed event.PlanReviewedPayload
	var seen bool
	for _, evt := range events {
		if evt.Type == event.RunEventPlanReviewed {
			seen = true
			require.NoError(t, json.Unmarshal(evt.Payload, &reviewed))
		}
	}
	require.True(t, seen, "the stopped review must still close itself with plan_reviewed")
	require.Equal(t, planReviewOutcomeStopped, reviewed.Outcome)
}

// stoppingReviewerFunc adapts a review function to the Reviewer interface the
// way the failing/recording stubs above do.
type stoppingReviewerFunc func(context.Context, Request) (PlanReviewResult, error)

func (f stoppingReviewerFunc) Review(ctx context.Context, req Request) (PlanReviewResult, error) {
	return f(ctx, req)
}
