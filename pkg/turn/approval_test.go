package turn

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
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

func TestBuildToolApprovalRequestUsesCanonicalNetworkContext(t *testing.T) {
	req, _, _ := BuildToolApprovalRequest(ApprovalSource{
		SessionID: "sid", ActionID: "aid", RunID: "rid", ToolName: "shell", ActionKind: "shell",
		ToolInput: map[string]any{
			"command":                  "curl https://example.com",
			"network_approval_context": map[string]any{"host": "Example.COM.", "protocol": "https"},
			"network_port":             443,
		},
	}, approvalEval{})
	require.NotNil(t, req.NetworkApproval)
	require.Equal(t, "example.com", req.NetworkApproval.Host)
	require.Equal(t, 443, req.NetworkPort)
	require.Equal(t, []safety.PermissionDestination{safety.DestinationSession, safety.DestinationLocalSettings}, req.DestinationOptions)
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

func TestNetworkResultUsesPersistedAnswer(t *testing.T) {
	decision, done := NetworkResult(&state.Action{Status: state.ActionApproved, AnswerJSON: `{"decision":"apply_network_policy_amendment","network_policy_amendment":{"host":"example.com","action":"deny"}}`})
	if !done || decision != safety.NetworkApprovalDenyForSession {
		t.Fatalf("decision=%q done=%t", decision, done)
	}
	decision, done = NetworkResult(&state.Action{Status: state.ActionExpired})
	if !done || decision != safety.NetworkApprovalCancel {
		t.Fatalf("expired decision=%q done=%t", decision, done)
	}
	if _, done := NetworkResult(&state.Action{Status: state.ActionPending}); done {
		t.Fatal("pending action resolved")
	}
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
	response := RequestPermissionsFromJSON("request_permissions", `{"permissions":{"file_system":{"read":["."]}}}`)
	require.NotNil(t, response)
	require.Len(t, response.Permissions.FileSystem.Entries, 1)
	require.Nil(t, RequestPermissionsFromJSON("shell", `{}`))
}

func TestValidateNetworkSessionUpdate(t *testing.T) {
	update := safety.PermissionUpdate{
		Type: safety.UpdateAddRules, Destination: safety.DestinationSession, Behavior: safety.BehaviorAllow,
		Rules: []safety.PermissionRuleValue{{ToolName: safety.NetworkAccessPermissionTool, RuleContent: "https://example.com:443"}},
	}
	err := ValidateNetworkSessionUpdate(update, safety.NetworkApprovalContext{Host: "example.com", Protocol: safety.NetworkApprovalHTTPS}, 443)
	require.NoError(t, err)
	update.Rules[0].RuleContent = "https://other.example"
	require.Error(t, ValidateNetworkSessionUpdate(update, safety.NetworkApprovalContext{Host: "example.com", Protocol: safety.NetworkApprovalHTTPS}, 443))
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
		`UPDATE fb_run_waits SET resume_claimed_at_ms=? WHERE run_id=? AND action_id=?`,
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
