package turn

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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

func TestDecideAskClarifyDeniesWithClarifyReason(t *testing.T) {
	payload := `{"questions":[{"id":"q1","prompt":"continue?","options":[{"id":"yes","label":"Yes"},{"id":"no","label":"No"}]}]}`
	store := &approvalStoreStub{action: &state.Action{
		ID: "a1", Kind: "user_interaction", Status: state.ActionPending, PayloadJSON: payload,
	}}
	service := ApprovalService{Actions: store}
	result, err := service.Decide(context.Background(), "a1", ApprovalReply{
		Choice: "clarify",
		// What the person had already answered rides along without making this
		// a submitted form.
		AskAnswerJSON: `{"answers":[{"question_id":"q1","option_ids":["yes"]}]}`,
	})
	require.NoError(t, err)
	require.True(t, result.Denied)
	require.Equal(t, state.ActionDenied, store.action.Status)
	require.Contains(t, store.action.Error, "The user wants to clarify these questions.")
	require.Contains(t, store.action.Error, `- "continue?"`)
	require.Contains(t, store.action.Error, "Answer: Yes")

	// A retried clarify (a double click, a re-sent request) is the same
	// verdict, not a conflict.
	retry, err := service.Decide(context.Background(), "a1", ApprovalReply{Choice: "clarify"})
	require.NoError(t, err)
	require.True(t, retry.Denied)
	require.True(t, retry.Idempotent)

	// An empty partial answer still denies: the questions stay unanswered and
	// the model is told so.
	store = &approvalStoreStub{action: &state.Action{
		ID: "a2", Kind: "user_interaction", Status: state.ActionPending, PayloadJSON: payload,
	}}
	result, err = (ApprovalService{Actions: store}).Decide(context.Background(), "a2", ApprovalReply{Choice: "clarify"})
	require.NoError(t, err)
	require.True(t, result.Denied)
	require.Contains(t, store.action.Error, "(No answer provided)")
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
	// The rows come from the install's model table, the one source of models
	// and of the ids a reference names.
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

// The reviewer offer names the model the way every human label does: the
// vendor listing's display name, not the opaque subscription wire id — while
// identity (Model) keeps the wire id.
func TestPlanReviewModelOptionsCarryDisplayName(t *testing.T) {
	llm.SetBuiltinModels("qodercn", []llm.Model{{ID: "gfmodel", Name: "GLM-5.3-Flash"}})
	t.Cleanup(func() { llm.SetBuiltinModels("qodercn", nil) })
	cfg := &appcfg.Root{LLMModels: []appcfg.LLMModelConfig{
		{ID: "row-qoder", Provider: "qodercn", Model: "gfmodel", BaseURL: "http://qoder.test"},
		{ID: "row-deepseek", Provider: "deepseek", Model: "deepseek-v4-flash", BaseURL: "http://deepseek.test"},
	}}
	options := PlanReviewModelOptions(cfg, "main", "row-qoder", "qodercn", "gfmodel")
	require.Len(t, options, 2)
	byID := map[string]PlanReviewModelOption{}
	for _, o := range options {
		byID[o.ID] = o
	}
	require.Equal(t, "GLM-5.3-Flash", byID["row-qoder"].ModelName)
	require.Equal(t, "gfmodel", byID["row-qoder"].Model, "identity keeps the wire id")
	require.Equal(t, "", byID["row-deepseek"].ModelName, "a listing that names none carries no display name")
}

// The exit gate's reviewer offers the same list /model and the web chip draw,
// so they come in the one display order too — and the mark is still the row
// the conversation runs on, wherever the order puts it.
func TestPlanReviewModelOptionsFollowTheDisplayOrder(t *testing.T) {
	cfg := &appcfg.Root{LLMModels: []appcfg.LLMModelConfig{
		// Storage order: openai before deepseek, so only the sort puts
		// deepseek first.
		{ID: "row-openai", Provider: "openai", Model: "gpt-5.1", BaseURL: "http://openai.test/v1"},
		{ID: "row-deepseek", Provider: "deepseek", Model: "deepseek-v4-flash", BaseURL: "http://deepseek.test"},
		{ID: "row-zhipuai", Provider: "zhipuai", Model: "glm-5.3", BaseURL: "http://zhipu.test/api"},
	}}
	options := PlanReviewModelOptions(cfg, "main", "row-openai", "openai", "gpt-5.1")
	require.Len(t, options, 3)
	var order []string
	for _, option := range options {
		order = append(order, option.Provider+"/"+option.Model)
	}
	require.Equal(t, []string{"deepseek/deepseek-v4-flash", "openai/gpt-5.1", "zhipuai/glm-5.3"}, order)
	require.False(t, options[0].Current)
	require.True(t, options[1].Current, "the row the conversation runs on carries the mark")
	require.False(t, options[2].Current)
}

// The exit gate's model picker opens a second screen of thinking levels, so
// each offer carries the levels its row actually offers and the one the row is
// configured with. The order is the one every other effort picker draws (the
// web chip's EFFORT_LEVELS), not the catalog's file order — a row whose
// reasoning_options list them as high/low/none must still offer none/low/high
// — and a row that configures no effort says so with "".
func TestPlanReviewModelOptionsCarryTheRowsEfforts(t *testing.T) {
	data, err := llm.RepoModelsJSONForTest()
	require.NoError(t, err)
	repoCatalog, err := llm.Parse(data)
	require.NoError(t, err)
	cat, err := llm.Parse([]byte(`{
		"openai/gpt-effort-a": {"id": "openai/gpt-effort-a", "reasoning": true,
			"reasoning_options": [{"type": "effort", "values": ["high", "low", "none"]}]},
		"openai/gpt-effort-b": {"id": "openai/gpt-effort-b", "reasoning": true,
			"reasoning_options": [{"type": "effort", "values": ["medium", "minimal"]}]}
	}`))
	require.NoError(t, err)
	llm.SetGlobalCatalogForTest(cat)
	t.Cleanup(func() { llm.SetGlobalCatalogForTest(repoCatalog) })

	cfg := &appcfg.Root{LLMModels: []appcfg.LLMModelConfig{
		{ID: "row-a", Provider: "openai", Model: "gpt-effort-a", BaseURL: "http://a.test",
			Params: appcfg.LLMRequestParams(`{"reasoning":{"effort":"low"}}`)},
		{ID: "row-b", Provider: "openai", Model: "gpt-effort-b", BaseURL: "http://b.test"},
	}}
	options := PlanReviewModelOptions(cfg, "main", "", "openai", "gpt-effort-a")
	require.Len(t, options, 2)
	byID := map[string]PlanReviewModelOption{}
	for _, o := range options {
		byID[o.ID] = o
	}
	require.Equal(t, []string{"none", "low", "high"}, byID["row-a"].Efforts, "the levels come in the one fixed order, not the catalog's")
	require.Equal(t, "low", byID["row-a"].DefaultEffort, "the level the row is configured with is what the second screen opens on")
	require.Equal(t, []string{"minimal", "medium"}, byID["row-b"].Efforts)
	require.Equal(t, "", byID["row-b"].DefaultEffort, "a row that configures none reports none")
}

// planReviewerConfig is the install's model table with the exit-plan reviewer
// row configured against it: the approval picker opens on that row, and a
// setting that names nothing of the table moves nothing.
func planReviewerConfig(t *testing.T, ref string) *appcfg.Root {
	t.Helper()
	return &appcfg.Root{
		LLMModels: []appcfg.LLMModelConfig{
			{ID: "row-glm", Provider: "zhipuai", Model: "glm-5.3", BaseURL: "http://zhipu.test/api"},
			{ID: "row-gpt", Provider: "openai", Model: "gpt-5.1", BaseURL: "http://openai.test/v1"},
			{ID: "row-ds", Provider: "deepseek", Model: "deepseek-v4-flash", BaseURL: "http://deepseek.test"},
		},
		Agents: appcfg.AgentsSection{PlanReviewer: appcfg.SubagentConfig{Model: ref}},
	}
}

func preferredOptionIDs(options []PlanReviewModelOption) []string {
	var ids []string
	for _, option := range options {
		if option.Preferred {
			ids = append(ids, option.ID)
		}
	}
	return ids
}

// The reviewer the install configured is the one offer that opens marked, and
// it is the row — never the provider/model label the picker draws — that
// carries the mark (plan 320).
func TestPlanReviewModelOptionsMarkTheConfiguredReviewer(t *testing.T) {
	cfg := planReviewerConfig(t, "row-glm")
	options := PlanReviewModelOptions(cfg, "main", "row-gpt", "openai", "gpt-5.1")
	require.Len(t, options, 3)
	require.Equal(t, []string{"row-glm"}, preferredOptionIDs(options),
		"the configured reviewer is the row the picker opens on, and the only one")
	for _, option := range options {
		if option.ID == "row-gpt" {
			require.True(t, option.Current, "the conversation's own model keeps its mark")
			require.False(t, option.Preferred, "and is not what the picker opens on")
		}
	}

	// A legacy label names the row it splits into, the way every other reader
	// of the setting resolves it.
	legacy := PlanReviewModelOptions(planReviewerConfig(t, "zhipuai/glm-5.3"), "main", "row-gpt", "openai", "gpt-5.1")
	require.Equal(t, []string{"row-glm"}, preferredOptionIDs(legacy))
}

// An unset reviewer, and one that names a row the table no longer holds, mark
// nothing: a dangling reference behaves as unset (plan 315), so the picker
// keeps the opening row it has always had (plan 320).
func TestPlanReviewModelOptionsMarkNothingWithoutAConfiguredRow(t *testing.T) {
	for _, ref := range []string{"", "row-gone", "not/a/configured-row"} {
		options := PlanReviewModelOptions(planReviewerConfig(t, ref), "main", "row-gpt", "openai", "gpt-5.1")
		require.Len(t, options, 3)
		require.Empty(t, preferredOptionIDs(options), "setting %q must move no cursor", ref)
	}
	// A config with no table at all has nothing to open on either.
	require.Empty(t, PlanReviewModelOptions(&appcfg.Root{}, "main", "", "", ""))
}

// planReviewerConfigWithAgentPin is planReviewerConfig with one primary agent
// that pinned the reviewer with a subagent entry of its own.
func planReviewerConfigWithAgentPin(t *testing.T, globalRef, agentName string, entry appcfg.SubagentConfig) *appcfg.Root {
	t.Helper()
	cfg := planReviewerConfig(t, globalRef)
	cfg.Agents.Definitions = map[string]appcfg.AgentDefinition{
		agentName: {Primary: true, Subagents: []appcfg.SubagentConfig{entry}},
	}
	return cfg
}

// The reviewer runs per agent, so an agent's own pin is the reviewer that agent
// gets (plan 352): it is the offer the picker opens on, and its level is what
// the second screen opens on — the level belongs to the same choice the row
// does, so marking the row without it would show a level the run never uses.
func TestPlanReviewModelOptionsPreferTheAgentsOwnPin(t *testing.T) {
	cfg := planReviewerConfigWithAgentPin(t, "", "scout", appcfg.SubagentConfig{
		Name: "plan-reviewer", Model: "row-glm", ReasoningEffort: "high",
	})
	options := PlanReviewModelOptions(cfg, "scout", "row-gpt", "openai", "gpt-5.1")
	require.Len(t, options, 3)
	require.Equal(t, []string{"row-glm"}, preferredOptionIDs(options))
	for _, option := range options {
		if option.ID == "row-glm" {
			require.Equal(t, "high", option.DefaultEffort, "the pin's level is what the second screen opens on")
		}
	}

	// The install-wide row is the fallback, and its level is now read too: it
	// was a setting nothing consumed until the pin chain claimed it (plan 352).
	global := planReviewerConfig(t, "row-ds")
	global.Agents.PlanReviewer.ReasoningEffort = "low"
	options = PlanReviewModelOptions(global, "scout", "row-gpt", "openai", "gpt-5.1")
	require.Equal(t, []string{"row-ds"}, preferredOptionIDs(options))
	for _, option := range options {
		if option.ID == "row-ds" {
			require.Equal(t, "low", option.DefaultEffort)
		}
	}

	// A pin nothing resolves falls back to the install-wide row, and a pin with
	// no model of its own does the same: neither may move the cursor off it.
	for _, entry := range []appcfg.SubagentConfig{
		{Name: "plan-reviewer", Model: "row-gone"},
		{Name: "plan-reviewer", ReasoningEffort: "high"},
	} {
		cfg := planReviewerConfigWithAgentPin(t, "row-glm", "scout", entry)
		require.Equal(t, []string{"row-glm"}, preferredOptionIDs(PlanReviewModelOptions(cfg, "scout", "row-gpt", "openai", "gpt-5.1")),
			"entry %#v must leave the install-wide row where it was", entry)
	}

	// Nothing configured anywhere still marks nothing, and marks it without
	// panicking on the definition lookup.
	cfg = planReviewerConfigWithAgentPin(t, "", "scout", appcfg.SubagentConfig{Name: "plan-reviewer"})
	require.Empty(t, preferredOptionIDs(PlanReviewModelOptions(cfg, "scout", "row-gpt", "openai", "gpt-5.1")))
}

// The row the picker opens on is the row the review records, so a decided
// approval replays the reviewer it actually ran on — the options list, the
// review request it produces and the started event's record are one row, not
// three (plan 320).
func TestConfiguredReviewerIsTheRowTheReviewRecords(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, runs, sessionID := planReviewEventDB(t)
	stateRoot := t.TempDir()
	require.NoError(t, state.SetPlanForSession(stateRoot, "proj", sessionID, "# Plan"))

	options := PlanReviewModelOptions(planReviewerConfig(t, "row-ds"), "main", "row-gpt", "openai", "gpt-5.1")
	require.Equal(t, []string{"row-ds"}, preferredOptionIDs(options))
	var opening PlanReviewModelOption
	for _, option := range options {
		if option.Preferred {
			opening = option
		}
	}

	var events []event.RunEvent
	require.NoError(t, RunPlanReview(ctx, PlanReviewRun{
		ActionID: "act-row",
		// What a surface hands the review: the offer it opened on, projected
		// into the request's model the way the TUI and the web both do.
		Model:     Model{ID: opening.ID, Provider: opening.Provider, Model: opening.Model},
		SessionID: sessionID, StateRoot: stateRoot, ProjectKey: "proj",
		Reviewer: &recordingReviewer{},
		Publish: func(ctx context.Context, evt event.RunEvent) error {
			events = append(events, evt)
			_, err := runs.AppendSessionEvent(ctx, state.SessionEvent{
				ID: evt.ID, SessionID: sessionID, Type: evt.Type, Payload: evt.Payload,
			})
			return err
		},
	}))

	var started event.PlanReviewStartedPayload
	for _, evt := range events {
		if evt.Type == event.RunEventPlanReviewStarted {
			require.NoError(t, json.Unmarshal(evt.Payload, &started))
		}
	}
	require.Equal(t, "row-ds", started.ModelID,
		"the reviewer the install configured is the row the review opened on and recorded")
	require.Equal(t, "deepseek-v4-flash", started.Model)
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
		Reviewer: failingReviewer{err: codedReviewError{code: "model_not_configured"}},
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
	require.Equal(t, "model_not_configured", reviewed.Code)
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
	plan     string
	planPath string
}

func (r *recordingReviewer) Review(_ context.Context, req Request) (PlanReviewResult, error) {
	r.plan = req.Plan
	r.planPath = req.PlanPath
	return PlanReviewResult{Model: req.Model, Text: "looks fine"}, nil
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

// The level the user picked after the model is part of the decision: it lands
// in answer_json.hand_off_model.effort, and the replay reads it back — the row
// alone would let the executor run at whatever the row configures, which is
// exactly what the second screen exists to override.
func TestHandOffAnswerCarriesThePickedEffort(t *testing.T) {
	store := &approvalStoreStub{action: &state.Action{ID: "a1", Kind: "exit_plan_mode", Status: state.ActionPending, PayloadJSON: `{}`}}
	service := ApprovalService{Actions: store}
	handOff := &Model{ID: "llm_row2", Provider: "zhipuai", Model: "glm-5.3", Effort: "high"}

	result, err := service.Decide(context.Background(), "a1", ApprovalReply{HandOffModel: handOff})
	require.NoError(t, err)
	require.True(t, result.Approved)
	require.Contains(t, store.action.AnswerJSON,
		`"hand_off_model":{"id":"llm_row2","provider":"zhipuai","model":"glm-5.3","effort":"high"}`)

	got, ok := ActionHandOffModel(store.action)
	require.True(t, ok)
	require.Equal(t, "high", got.Effort, "the replay reads the level back")

	// The same decision replays idempotently, effort and all.
	retry, err := service.Decide(context.Background(), "a1", ApprovalReply{HandOffModel: handOff})
	require.NoError(t, err)
	require.True(t, retry.Idempotent)
}

// An approved answer written before the picker had a second screen carries no
// effort: it still decodes on every path that reads it back, and behaves the
// way it always did — the executor runs at the row's own level.
func TestHandOffAnswerWithoutEffortStillReplays(t *testing.T) {
	legacy := `{"decision":"accept","clear_context":false,"plan_only":false,` +
		`"hand_off_model":{"id":"llm_row2","provider":"zhipuai","model":"glm-5.3"}}`
	act := &state.Action{
		ID: "a-legacy", Kind: "exit_plan_mode", Status: state.ActionApproved,
		PayloadJSON: `{}`, AnswerJSON: legacy,
	}

	// The reply writer's replay path decodes the old shape without complaint
	// and materializes the same no-op effect it always did.
	require.NoError(t, (ApprovalService{Actions: &approvalStoreStub{action: act}}).ApplyResolvedEffect(act, "run-1"))

	got, ok := ActionHandOffModel(act)
	require.True(t, ok, "an answer without an effort is still a hand-off")
	require.Equal(t, Model{ID: "llm_row2", Provider: "zhipuai", Model: "glm-5.3"}, got,
		"the missing effort decodes as the empty level, with no other field disturbed")

	// A surface that re-sends the same decision — the row alone, no level —
	// is a retry, not a conflict: old clients keep working.
	store := &approvalStoreStub{action: act}
	retry, err := (ApprovalService{Actions: store}).Decide(context.Background(), "a-legacy",
		ApprovalReply{HandOffModel: &Model{ID: "llm_row2", Provider: "zhipuai", Model: "glm-5.3"}})
	require.NoError(t, err)
	require.True(t, retry.Idempotent)
	require.Equal(t, legacy, act.AnswerJSON, "an idempotent retry rewrites nothing")
}

// The level is part of what the user decided: the same row at a different
// level is a different answer, so a second dispatch does not read as a retry
// of the first.
func TestSameHandOffTreatsDifferentEffortsAsDifferent(t *testing.T) {
	store := &approvalStoreStub{action: &state.Action{ID: "a1", Kind: "exit_plan_mode", Status: state.ActionPending, PayloadJSON: `{}`}}
	service := ApprovalService{Actions: store}
	_, err := service.Decide(context.Background(), "a1",
		ApprovalReply{HandOffModel: &Model{ID: "llm_row2", Provider: "zhipuai", Model: "glm-5.3", Effort: "high"}})
	require.NoError(t, err)

	// Same row, different level: not the same decision.
	_, err = service.Decide(context.Background(), "a1",
		ApprovalReply{HandOffModel: &Model{ID: "llm_row2", Provider: "zhipuai", Model: "glm-5.3", Effort: "low"}})
	require.ErrorIs(t, err, ErrApprovalConflict)

	// And the level is compared with the same trimming every other field gets,
	// so a round trip that pads it stays a retry.
	retry, err := service.Decide(context.Background(), "a1",
		ApprovalReply{HandOffModel: &Model{ID: "llm_row2", Provider: " zhipuai", Model: "glm-5.3", Effort: " high "}})
	require.NoError(t, err)
	require.True(t, retry.Idempotent)
}

// RunPlanReview rebuilds the reviewer from the run it is given, and that
// rebuild is where a field goes missing quietly: the review has to reach the
// reviewer at the level the user picked, or the second screen changes nothing.
func TestRunPlanReviewKeepsTheModelsEffort(t *testing.T) {
	ctx := context.Background()
	stateRoot := t.TempDir()
	require.NoError(t, state.SetPlanForSession(stateRoot, "proj", "s1", "# Plan"))
	reviewer := &modelRecordingReviewer{}
	err := RunPlanReview(ctx, PlanReviewRun{
		ActionID: "a-effort", Model: Model{ID: "row-2", Provider: "openai", Model: "gpt-4o", Effort: "xhigh"},
		SessionID: "s1", StateRoot: stateRoot, ProjectKey: "proj",
		Reviewer: reviewer,
	})
	require.NoError(t, err)
	require.Equal(t, "xhigh", reviewer.model.Effort, "the picked level reaches the reviewer")
}

// modelRecordingReviewer keeps the reviewer the run built, which is the only
// place a dropped field shows up.
type modelRecordingReviewer struct{ model Model }

func (r *modelRecordingReviewer) Review(_ context.Context, req Request) (PlanReviewResult, error) {
	r.model = req.Model
	return PlanReviewResult{Model: req.Model, Text: "looks fine"}, nil
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
// own id, and the reviewer is handed the Again record.
func TestRunPlanReviewAgainContinuesThePreviousReview(t *testing.T) {
	ctx := context.Background()
	stateRoot := t.TempDir()
	require.NoError(t, state.SetPlanForSession(stateRoot, "proj", "s1", "# Plan\n\nbody"))
	var events []event.RunEvent
	reviewer := &againRecordingReviewer{}
	err := RunPlanReview(ctx, PlanReviewRun{
		ActionID: "a2", Model: Model{ID: "llm_row2", Provider: "deepseek", Model: "deepseek-v4-flash"},
		SessionID: "s1", StateRoot: stateRoot, ProjectKey: "proj",
		Reviewer: reviewer,
		Again:    &PlanReviewAgain{ReviewID: "plan-review:r1", AgentID: "agent-r1", TaskID: "task-r1"},
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
	require.Equal(t, "# Plan\n\nbody", reviewer.req.Plan)
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
