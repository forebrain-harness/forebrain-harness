package gateway

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
	"github.com/stretchr/testify/require"
)

// mustGatewaySession creates the conversation row a test's runs and actions
// reference; the foreign keys require both to belong to one.
func mustGatewaySession(t *testing.T, db *sql.DB, sid string) {
	t.Helper()
	if err := state.NewSessionStore(db, "main").Ensure(context.Background(), sid, sid); err != nil {
		t.Fatal(err)
	}
}

func TestDetachedApprovalResumePublishesToolAndNestedApprovalHistory(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(ctx, filepath.Join(t.TempDir(), "state.db"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	runs := &state.RunStore{DB: db}
	require.NoError(t, state.NewSessionStore(db, "main").Ensure(ctx, "conversation-1", "conversation-1"))
	mustGatewaySession(t, db, "conversation-1")
	runRecord, err := runs.CreateRun(ctx, "conversation-1", "resume")
	require.NoError(t, err)
	s := &Server{RunRT: runs}

	runCtx := tool.WithConversationSessionID(tool.WithRunID(ctx, runRecord.ID), "conversation-1")
	runCtx = s.withDetachedGatewayApprovalHooks(runCtx, "conversation-1", runRecord.ID)
	stepHook := tool.StepHookFromContext(runCtx, nil)
	require.NotNil(t, stepHook)
	stepHook(runCtx, tool.StepEvent{
		Kind: tool.StepKindToolCompleted, StepID: "call-1", ToolName: "read_file",
		Output: map[string]any{"content": "done"},
	})
	s.publishDetachedGatewayApprovalRequest(ctx, "conversation-1", &tool.RequiresActionError{
		RunID: runRecord.ID, ActionID: "action-2", ActionKind: "shell", ToolName: "shell",
		AgentID: "agent-2", SubagentType: "explore", ToolInput: map[string]any{"command": "git status"},
	})

	page, err := runs.ListSessionEvents(ctx, "conversation-1", 0, 0, 10)
	require.NoError(t, err)
	require.Len(t, page.Events, 2)
	require.Equal(t, event.RunEventToolCompleted, page.Events[0].Type)
	require.Equal(t, event.RunEventApprovalReq, page.Events[1].Type)
	var approval event.ApprovalRequestedPayload
	require.NoError(t, json.Unmarshal(page.Events[1].Payload, &approval))
	require.Equal(t, "action-2", approval.ActionID)
	require.Equal(t, "agent-2", approval.AgentID)
	require.Equal(t, "explore", approval.SubagentType)
}

func TestHTTPApprovalRejectsWidenedExecPolicyAmendment(t *testing.T) {
	s, actions := newApprovalHTTPServer(t)
	action, err := actions.CreatePending(context.Background(), "session-1", "shell", map[string]any{
		"command": "npm run build", "session_id": "session-1",
	})
	require.NoError(t, err)

	rr := postApprovalDecision(t, s, action.ID, map[string]any{
		"decision":             "accept_with_execpolicy_amendment",
		"execpolicy_amendment": []string{"npm", "run"},
	})
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
	stored, err := actions.Get(context.Background(), action.ID)
	require.NoError(t, err)
	require.Equal(t, state.ActionPending, stored.Status)
	require.Nil(t, s.Runner.EvaluatePermission("Bash", "npm run test").Matched)
}

func TestHTTPApprovalRejectsActionOwnedByAnotherPrimaryAgent(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(ctx, filepath.Join(t.TempDir(), "state.db"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	otherSessions := state.NewSessionStore(db, "other-agent")
	require.NoError(t, otherSessions.Ensure(ctx, "other-session", "private"))
	currentSessions := state.NewSessionStore(db, "current-agent")
	require.NoError(t, currentSessions.Ensure(ctx, "current-session", "mine"))
	actions := &state.ActionService{DB: db}
	action, err := actions.CreatePending(ctx, "other-session", "shell", map[string]any{
		"command": "cat private.txt", "session_id": "other-session",
	})
	require.NoError(t, err)
	s := &Server{
		Actions: actions, Sessions: currentSessions,
		Runner: &run.Runner{Deps: &run.Deps{Home: t.TempDir()}},
	}

	rr := postApprovalDecision(t, s, action.ID, map[string]any{"decision": "accept"})
	require.Equal(t, http.StatusNotFound, rr.Code, rr.Body.String())
	stored, err := actions.Get(ctx, action.ID)
	require.NoError(t, err)
	require.Equal(t, state.ActionPending, stored.Status)
}

func TestScopedPendingActionsAreFilteredBeforeTheGlobalDisplayLimit(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(ctx, filepath.Join(t.TempDir(), "state.db"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	sessions := state.NewSessionStore(db, "current-agent")
	require.NoError(t, sessions.Ensure(ctx, "target-session", "target"))
	require.NoError(t, sessions.Ensure(ctx, "other-session", "other"))
	actions := &state.ActionService{DB: db}
	_, err = db.ExecContext(ctx, `
INSERT INTO fb_actions(id,session_id,kind,status,payload_json,answer_json,error,created_at,updated_at)
VALUES('target-action','target-session','shell','pending','{"session_id":"target-session"}','','',1,1)`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `
WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < 501)
INSERT INTO fb_actions(id,session_id,kind,status,payload_json,answer_json,error,created_at,updated_at)
SELECT printf('noise-%04d',i),'other-session','shell','pending','{"session_id":"other-session"}','','',i+10,i+10 FROM n`)
	require.NoError(t, err)

	s := &Server{Home: t.TempDir(), Actions: actions, Sessions: sessions}
	req := httptest.NewRequest(http.MethodGet, "/api/actions?status=pending&session_id=target-session&limit=10", nil)
	rr := httptest.NewRecorder()
	s.handleActions(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var rows []actionListRow
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &rows))
	require.Len(t, rows, 1)
	require.Equal(t, "target-action", rows[0].ID)
}

func TestHTTPPatchApprovalIsScopedToOwningSession(t *testing.T) {
	s, actions := newApprovalHTTPServer(t)
	action, err := actions.CreatePending(context.Background(), "session-1", "apply_patch", map[string]any{
		"resolved_paths": []string{"/repo/a.go", "/repo/b.go"}, "session_id": "session-1",
	})
	require.NoError(t, err)

	rr := postApprovalDecision(t, s, action.ID, map[string]any{"decision": "accept_for_session"})
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	for _, path := range []string{"/repo/a.go", "/repo/b.go"} {
		owning := s.Runner.EvaluatePermissionForSession("session-1", "apply_patch", path)
		require.NotNil(t, owning.Matched)
		require.True(t, owning.BypassSandbox)
		other := s.Runner.EvaluatePermissionForSession("session-2", "apply_patch", path)
		require.Nil(t, other.Matched)
		require.False(t, other.BypassSandbox)
	}
}

func TestHTTPRequestPermissionsDecisionControlsScope(t *testing.T) {
	s, actions := newApprovalHTTPServer(t)
	action, err := actions.CreatePending(context.Background(), "session-1", "request_permissions", map[string]any{
		"session_id": "session-1",
		"cwd":        "/repo",
		"permissions": map[string]any{
			"network": map[string]any{"enabled": true},
		},
	})
	require.NoError(t, err)

	rr := postApprovalDecision(t, s, action.ID, map[string]any{"decision": "grant_for_session"})
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	stored, err := actions.Get(context.Background(), action.ID)
	require.NoError(t, err)
	var response safety.RequestPermissionsResponse
	require.NoError(t, json.Unmarshal([]byte(stored.AnswerJSON), &response))
	require.Equal(t, safety.GrantScopeSession, response.Scope)
	require.False(t, response.StrictAutoReview)
	require.NotNil(t, response.Permissions.Network)
	require.True(t, response.Permissions.Network.AllowsNetwork())
}

func TestConcurrentHTTPApprovalLoserCannotApplyRememberedRule(t *testing.T) {
	s, actions := newApprovalHTTPServer(t)
	action, err := actions.CreatePending(context.Background(), "session-1", "shell", map[string]any{
		"command": "npm run build", "session_id": "session-1",
	})
	require.NoError(t, err)

	first := postApprovalDecision(t, s, action.ID, map[string]any{"decision": "accept"})
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	second := postApprovalDecision(t, s, action.ID, map[string]any{
		"decision":             "accept_with_execpolicy_amendment",
		"execpolicy_amendment": []string{"npm", "run", "build"},
	})
	require.Equal(t, http.StatusConflict, second.Code, second.Body.String())
	require.Nil(t, s.Runner.EvaluatePermission("Bash", "npm run build -- --watch").Matched)
}

func TestConcurrentHTTPRequestPermissionsDecisionReturnsConflict(t *testing.T) {
	s, actions := newApprovalHTTPServer(t)
	action, err := actions.CreatePending(context.Background(), "session-1", "request_permissions", map[string]any{
		"session_id": "session-1", "cwd": "/repo",
		"permissions": map[string]any{"network": map[string]any{"enabled": true}},
	})
	require.NoError(t, err)

	first := postApprovalDecision(t, s, action.ID, map[string]any{"decision": "grant_for_turn"})
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	second := postApprovalDecision(t, s, action.ID, map[string]any{"decision": "decline"})
	require.Equal(t, http.StatusConflict, second.Code, second.Body.String())
	stored, err := actions.Get(context.Background(), action.ID)
	require.NoError(t, err)
	require.Equal(t, state.ActionApproved, stored.Status)
}

func TestHTTPApprovalRejectsTrailingJSON(t *testing.T) {
	s, actions := newApprovalHTTPServer(t)
	action, err := actions.CreatePending(context.Background(), "session-1", "shell", map[string]any{
		"command": "go test ./...", "session_id": "session-1",
	})
	require.NoError(t, err)

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/actions/"+action.ID+"/approve",
		bytes.NewBufferString(`{"decision":"accept"} {"decision":"cancel"}`),
	)
	req = req.WithContext(context.WithValue(req.Context(), ParamsKey, Params{{Key: "id", Value: action.ID}}))
	rr := httptest.NewRecorder()
	s.handleActionsApprove(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
	stored, err := actions.Get(context.Background(), action.ID)
	require.NoError(t, err)
	require.Equal(t, state.ActionPending, stored.Status)
}

func TestHTTPApprovalRejectsClearContextOutsideExitPlan(t *testing.T) {
	s, actions := newApprovalHTTPServer(t)
	action, err := actions.CreatePending(context.Background(), "session-1", "shell", map[string]any{
		"command": "go test ./...", "session_id": "session-1",
	})
	require.NoError(t, err)

	rr := postApprovalDecision(t, s, action.ID, map[string]any{"clear_context": true})
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
	stored, err := actions.Get(context.Background(), action.ID)
	require.NoError(t, err)
	require.Equal(t, state.ActionPending, stored.Status)
}

func TestHTTPExitPlanApprovalAcceptsClearContextWithoutPermissionUpdate(t *testing.T) {
	s, actions := newApprovalHTTPServer(t)
	action, err := actions.CreatePending(context.Background(), "session-1", "exit_plan_mode", map[string]any{
		"action": "exit", "session_id": "session-1",
	})
	require.NoError(t, err)

	rr := postApprovalDecision(t, s, action.ID, map[string]any{"clear_context": true})
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	stored, err := actions.Get(context.Background(), action.ID)
	require.NoError(t, err)
	require.Equal(t, state.ActionApproved, stored.Status)
	var answer struct {
		ClearContext bool `json:"clear_context"`
	}
	require.NoError(t, json.Unmarshal([]byte(stored.AnswerJSON), &answer))
	require.True(t, answer.ClearContext)
	require.Equal(t, safety.ModeOnRequest, s.Runner.PermissionSnapshotForSession("session-1").Mode)
}

func TestHTTPRequestPermissionsRequiresServerDecision(t *testing.T) {
	s, actions := newApprovalHTTPServer(t)
	action, err := actions.CreatePending(context.Background(), "session-1", "request_permissions", map[string]any{
		"session_id": "session-1", "cwd": "/repo",
		"permissions": map[string]any{"network": map[string]any{"enabled": true}},
	})
	require.NoError(t, err)

	rr := postApprovalDecision(t, s, action.ID, map[string]any{
		"request_permissions_response": map[string]any{
			"scope": "session", "permissions": map[string]any{"network": map[string]any{"enabled": true}},
		},
	})
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
	stored, err := actions.Get(context.Background(), action.ID)
	require.NoError(t, err)
	require.Equal(t, state.ActionPending, stored.Status)
}

func newApprovalHTTPServer(t *testing.T) (*Server, *state.ActionService) {
	t.Helper()
	db, err := state.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	sessions := state.NewSessionStore(db, "main")
	for _, sid := range []string{"session-1", "session-2", "other-session", "current-session"} {
		require.NoError(t, sessions.Ensure(context.Background(), sid, sid))
	}
	actions := &state.ActionService{DB: db}
	return &Server{Actions: actions, Runner: &run.Runner{Deps: &run.Deps{Home: t.TempDir()}}}, actions
}

func postApprovalDecision(t *testing.T, s *Server, actionID string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/actions/"+actionID+"/approve", bytes.NewReader(raw))
	req = req.WithContext(context.WithValue(req.Context(), ParamsKey, Params{{Key: "id", Value: actionID}}))
	rr := httptest.NewRecorder()
	s.handleActionsApprove(rr, req)
	return rr
}

func (s *Server) applyGatewayApprovalDecision(action *state.Action, choice string, network *safety.NetworkPolicyAmendment, exec safety.ExecPolicyAmendment) error {
	effect, err := s.approvalService().Prepare(action, turn.ApprovalReply{Choice: choice, Network: network, Exec: exec})
	if err == nil {
		effect()
	}
	return err
}

func TestApprovalWSDataIncludesPermissionSuggestion(t *testing.T) {
	data := approvalWSData(fakePermissionEvaluator{}, "session-1", "a1", "shell", "shell", map[string]any{
		"command":             "npm run build",
		"sandbox_permissions": "require_escalated",
	})
	if data["id"] != "a1" || data["kind"] != "shell" {
		t.Fatalf("unexpected base action payload: %#v", data)
	}
	raw, ok := data["permission_suggestion"].(map[string]any)
	if !ok {
		t.Fatalf("expected permission suggestion payload: %#v", data)
	}
	if raw["permission_tool_name"] != "Bash" {
		t.Fatalf("unexpected permission tool name: %#v", raw["permission_tool_name"])
	}
	if raw["prefix_rule_content"] != "npm run build:*" {
		t.Fatalf("unexpected prefix rule content: %#v", raw["prefix_rule_content"])
	}
	if raw["bypass_sandbox"] != true {
		t.Fatalf("expected sandbox bypass suggestion for host-approved shell action: %#v", raw)
	}
	opts, ok := raw["destination_options"].([]safety.PermissionDestination)
	if !ok {
		t.Fatalf("unexpected destination options payload: %#v", raw["destination_options"])
	}
	if len(opts) != 1 || opts[0] != safety.DestinationLocalSettings {
		t.Fatalf("unexpected destination options: %#v", opts)
	}
}

func TestApprovalWSDataSuppressesExecAmendmentAfterPolicyMatch(t *testing.T) {
	data := approvalWSData(matchedPermissionEvaluator{}, "session-1", "a-policy", "shell", "shell", map[string]any{
		"command": "npm run build",
	})
	raw := data["permission_suggestion"].(map[string]any)
	if options, ok := raw["destination_options"].([]safety.PermissionDestination); !ok || len(options) != 0 {
		t.Fatalf("destination_options=%#v", raw["destination_options"])
	}
	if _, ok := raw["proposed_execpolicy_amendment"]; ok {
		t.Fatalf("matched policy exposed an exec amendment: %#v", raw)
	}
	decisions, ok := raw["available_decisions"].([]any)
	if !ok || len(decisions) != 2 || decisions[0] != "accept" || decisions[1] != "cancel" {
		t.Fatalf("available_decisions=%#v", raw["available_decisions"])
	}
}

// An approval card is judged by the runner the conversation runs on: a rule
// that session holds shapes its card, whatever the gateway's own runner holds.
func TestApprovalCardsAreJudgedByTheSessionsOwnRunner(t *testing.T) {
	facade := &run.Runner{Deps: &run.Deps{}}
	sessionRunner := &run.Runner{Deps: &run.Deps{}}
	sessionRunner.ApplyPermissionUpdate(safety.PermissionUpdate{
		Type: safety.UpdateAddRules, Destination: safety.DestinationSession, SessionID: "session-1", Behavior: safety.BehaviorAsk,
		Rules: []safety.PermissionRuleValue{{ToolName: "Bash", CommandPrefix: []string{"npm", "run", "build"}}},
	})
	s := &Server{Core: turn.New(turn.WithPermissionFacade(facade)), Runner: facade, Env: &process.Environment{Runner: sessionRunner}}
	action := state.Action{ID: "a-session", Kind: "shell", SessionID: "session-1", PayloadJSON: `{"command":"npm run build"}`}

	raw := s.actionPermissionSuggestion(context.Background(), action)
	require.NotNil(t, raw)
	_, proposed := raw["proposed_execpolicy_amendment"]
	require.False(t, proposed, "the session's own ask rule decided this card: %#v", raw)
	options, _ := raw["destination_options"].([]safety.PermissionDestination)
	require.Empty(t, options)
}

func TestApprovalWSDataIncludesMCPSuggestion(t *testing.T) {
	toolName := "mcp__code_review_graph__get_minimal_context_tool"
	data := approvalWSData(fakePermissionEvaluator{}, "session-1", "a2", toolName, toolName, map[string]any{
		"repo_root": "/tmp/repo",
	})
	raw, ok := data["permission_suggestion"].(map[string]any)
	if !ok {
		t.Fatalf("expected mcp permission suggestion payload: %#v", data)
	}
	if raw["permission_tool_name"] != toolName {
		t.Fatalf("unexpected permission tool name: %#v", raw["permission_tool_name"])
	}
	exact, _ := raw["exact_rule_content"].(string)
	if exact != "*" || raw["prefix_rule_content"] != "" {
		t.Fatalf("unexpected mcp rule suggestions: %#v", raw)
	}
}

func TestApprovalWSDataRestrictsPromptMCPToOneShot(t *testing.T) {
	toolName := "mcp__demo__write"
	data := approvalWSData(fakePermissionEvaluator{}, "session-1", "a3", toolName, toolName, map[string]any{
		"mcp_approval_mode": "writes",
	})
	raw := data["permission_suggestion"].(map[string]any)
	if raw["one_shot_only"] != true || raw["suggested_destination"] != safety.PermissionDestination("") {
		t.Fatalf("suggestion=%#v", raw)
	}
	if options, ok := raw["destination_options"].([]safety.PermissionDestination); !ok || len(options) != 0 {
		t.Fatalf("destination_options=%#v", raw["destination_options"])
	}
}

func TestGatewayExecPolicyDecisionOnlyAppliesProposedAmendment(t *testing.T) {
	runner := &run.Runner{Deps: &run.Deps{Home: t.TempDir()}}
	s := &Server{Runner: runner}
	payload, _ := json.Marshal(map[string]any{"command": "go test ./...", "session_id": "s1"})
	action := &state.Action{Kind: "shell", PayloadJSON: string(payload)}

	if err := s.applyGatewayApprovalDecision(action, "accept_with_execpolicy_amendment", nil, safety.ExecPolicyAmendment{"go"}); err == nil {
		t.Fatal("expected a widened amendment to be rejected")
	}
	if err := s.applyGatewayApprovalDecision(action, "accept_with_execpolicy_amendment", nil, safety.ExecPolicyAmendment{"go", "test"}); err != nil {
		t.Fatal(err)
	}
	decision := runner.EvaluatePermission("Bash", "go test ./pkg/tui -run TestX")
	if decision.Behavior != safety.BehaviorAllow || !decision.BypassSandbox || decision.Matched == nil {
		t.Fatalf("expected persisted proposed amendment, got %+v", decision)
	}
}

func TestGatewayExecPolicyDecisionRejectsExistingPolicyMatch(t *testing.T) {
	runner := &run.Runner{Deps: &run.Deps{Home: t.TempDir()}}
	runner.ApplyPermissionUpdate(safety.PermissionUpdate{
		Type: safety.UpdateAddRules, Destination: safety.DestinationLocalSettings,
		Behavior: safety.BehaviorAsk,
		Rules:    []safety.PermissionRuleValue{{ToolName: "Bash", CommandPrefix: []string{"npm", "run", "build"}}},
	})
	s := &Server{Runner: runner}
	payload, _ := json.Marshal(map[string]any{"command": "npm run build", "session_id": "s1"})
	action := &state.Action{Kind: "shell", PayloadJSON: string(payload)}
	if err := s.applyGatewayApprovalDecision(
		action, "accept_with_execpolicy_amendment", nil, safety.ExecPolicyAmendment{"npm", "run", "build"},
	); err == nil {
		t.Fatal("expected an exec amendment driven by an existing policy match to be rejected")
	}
}

func TestApprovalWSDataIncludesTypedDecisionInputsForActionPayloadJSON(t *testing.T) {
	payload, err := json.Marshal(map[string]any{
		"command":                  "curl https://example.com",
		"network_approval_context": map[string]any{"host": "Example.COM.", "protocol": "https"},
		"network_port":             443,
	})
	if err != nil {
		t.Fatal(err)
	}
	data := approvalWSData(fakePermissionEvaluator{}, "session-1", "a4", "shell", "shell", string(payload))
	raw := data["permission_suggestion"].(map[string]any)
	if raw["network_port"] != 443 {
		t.Fatalf("network_port=%#v", raw["network_port"])
	}
	context, ok := raw["network_approval_context"].(*safety.NetworkApprovalContext)
	if !ok || context.Host != "example.com" || context.Protocol != safety.NetworkApprovalHTTPS {
		t.Fatalf("network context=%#v", raw["network_approval_context"])
	}
	decisions, ok := raw["available_decisions"].([]any)
	if !ok || len(decisions) != 4 {
		t.Fatalf("available_decisions=%#v", raw["available_decisions"])
	}
	allowContainer, ok := decisions[2].(map[string]any)["apply_network_policy_amendment"].(map[string]any)
	allow, ok := allowContainer["network_policy_amendment"].(safety.NetworkPolicyAmendment)
	if !ok || allow.Action != safety.NetworkPolicyAllow || decisions[3] != "cancel" {
		t.Fatalf("persistent allow decision=%#v", decisions)
	}
}

func TestGatewayPatchSessionDecisionDoesNotCrossSessions(t *testing.T) {
	runner := &run.Runner{Deps: &run.Deps{}}
	s := &Server{Runner: runner}
	payload, _ := json.Marshal(map[string]any{"resolved_paths": []string{"/repo/a.go", "/repo/b.go"}, "session_id": "s1"})
	action := &state.Action{SessionID: "s1", Kind: "apply_patch", PayloadJSON: string(payload)}
	if err := s.applyGatewayApprovalDecision(action, "accept_for_session", nil, nil); err != nil {
		t.Fatal(err)
	}
	if decision := runner.EvaluatePermissionForSession("s1", "apply_patch", "/repo/a.go"); decision.Matched == nil || !decision.BypassSandbox {
		t.Fatalf("expected patch approval in owning session, got %+v", decision)
	}
	if decision := runner.EvaluatePermissionForSession("s2", "apply_patch", "/repo/a.go"); decision.Matched != nil || decision.BypassSandbox {
		t.Fatalf("patch approval leaked across sessions: %+v", decision)
	}
}

// A command with no derivable prefix is offered as itself, so a remote client
// sees the same persistent choice the terminal does.
func TestApprovalWSDataOffersExactCommandWhenNoPrefixDerivable(t *testing.T) {
	data := approvalWSData(fakePermissionEvaluator{}, "session-1", "a-exact", "shell", "shell", map[string]any{
		"command": "gofmt -w internal/a.go && go mod tidy",
	})
	raw := data["permission_suggestion"].(map[string]any)
	if _, ok := raw["proposed_execpolicy_amendment"]; ok {
		t.Fatalf("a compound command must not propose a prefix: %#v", raw)
	}
	decisions, ok := raw["available_decisions"].([]any)
	if !ok || len(decisions) != 3 {
		t.Fatalf("available_decisions=%#v", raw["available_decisions"])
	}
	// The row has to say what remembering covers, so the remember decision
	// carries the server-derived scope rather than only its name: the web
	// words its own sentence from it instead of guessing.
	entry, ok := decisions[1].(map[string]any)
	if !ok {
		t.Fatalf("decisions[1]=%#v", decisions[1])
	}
	body, ok := entry["accept_and_remember"].(map[string]any)
	if !ok {
		t.Fatalf("decisions[1]=%#v", decisions[1])
	}
	scope, ok := body["command_scope"].(safety.CommandApprovalScope)
	if !ok || scope.Kind != safety.CommandApprovalScopeCommandWithVariants ||
		len(scope.Prefixes) != 1 || scope.Prefixes[0] != "go mod" {
		t.Fatalf("command_scope=%#v", body["command_scope"])
	}
	options, ok := raw["destination_options"].([]safety.PermissionDestination)
	if !ok || len(options) != 1 || options[0] != safety.DestinationLocalSettings {
		t.Fatalf("destination_options=%#v", raw["destination_options"])
	}
}

func TestGatewayRememberDecisionPersistsTheExactCommand(t *testing.T) {
	runner := &run.Runner{Deps: &run.Deps{Home: t.TempDir()}}
	s := &Server{Runner: runner}
	command := "gofmt -w internal/a.go && go mod tidy"
	payload, _ := json.Marshal(map[string]any{"command": command, "session_id": "s1"})
	action := &state.Action{Kind: "shell", PayloadJSON: string(payload)}

	if err := s.applyGatewayApprovalDecision(action, "accept_and_remember", nil, nil); err != nil {
		t.Fatal(err)
	}
	decision := runner.EvaluatePermission("Bash", command)
	if decision.Behavior != safety.BehaviorAllow || !decision.BypassSandbox || decision.Matched == nil {
		t.Fatalf("remembered command was not applied: %+v", decision)
	}
	if other := runner.EvaluatePermission("Bash", command+" && rm -rf /tmp/x"); other.Matched != nil {
		t.Fatalf("exact rule leaked to a longer command: %+v", other)
	}
}

func TestGatewayRememberDecisionPersistsWebFetchDomain(t *testing.T) {
	runner := &run.Runner{Deps: &run.Deps{Home: t.TempDir()}}
	s := &Server{Runner: runner}
	payload, _ := json.Marshal(map[string]any{"url": "https://example.com/docs", "session_id": "s1"})
	action := &state.Action{Kind: "WebFetch", PayloadJSON: string(payload)}

	if err := s.applyGatewayApprovalDecision(action, "accept_and_remember", nil, nil); err != nil {
		t.Fatal(err)
	}
	if decision := runner.EvaluatePermission("WebFetch", "https://example.com/other"); decision.Behavior != safety.BehaviorAllow {
		t.Fatalf("remembered domain was not applied: %+v", decision)
	}
	if decision := runner.EvaluatePermission("WebFetch", "https://elsewhere.example/other"); decision.Matched != nil {
		t.Fatalf("domain rule leaked to another host: %+v", decision)
	}
}

// Remembering is only offered where the service proposed a rule; a client
// cannot invent one for a tool that never had one.
func TestGatewayRememberDecisionRejectsToolsWithNothingProposed(t *testing.T) {
	s := &Server{Runner: &run.Runner{Deps: &run.Deps{Home: t.TempDir()}}}
	payload, _ := json.Marshal(map[string]any{"query": "golang generics", "session_id": "s1"})
	action := &state.Action{Kind: "WebSearch", PayloadJSON: string(payload)}
	if err := s.applyGatewayApprovalDecision(action, "accept_and_remember", nil, nil); err == nil {
		t.Fatal("expected accept_and_remember to be unavailable for WebSearch")
	}
}

// An allow rule that carries no sandbox bypass still produces an escalation
// prompt. Upgrading it is the point of that prompt, so the choice stays.
func TestGatewayExecPolicyDecisionAllowsUpgradingAMatchedAllowRule(t *testing.T) {
	runner := &run.Runner{Deps: &run.Deps{Home: t.TempDir()}}
	runner.ApplyPermissionUpdate(safety.PermissionUpdate{
		Type: safety.UpdateAddRules, Destination: safety.DestinationLocalSettings,
		Behavior: safety.BehaviorAllow,
		Rules:    []safety.PermissionRuleValue{{ToolName: "Bash", CommandPrefix: []string{"go", "test"}}},
	})
	s := &Server{Runner: runner}
	payload, _ := json.Marshal(map[string]any{"command": "go test ./...", "session_id": "s1"})
	action := &state.Action{Kind: "shell", PayloadJSON: string(payload)}
	if err := s.applyGatewayApprovalDecision(
		action, "accept_with_execpolicy_amendment", nil, safety.ExecPolicyAmendment{"go", "test"},
	); err != nil {
		t.Fatal(err)
	}
	if decision := runner.EvaluatePermission("Bash", "go test ./..."); !decision.BypassSandbox {
		t.Fatalf("the escalation the user approved was not persisted: %+v", decision)
	}
}

type fakePermissionEvaluator struct{}

func (fakePermissionEvaluator) EvaluatePermissionForSession(sessionID, toolName, input string) safety.Decision {
	return safety.Decision{
		Behavior: safety.BehaviorAsk,
		Mode:     safety.ModeOnRequest,
		Reason:   "default_ask",
	}
}

type matchedPermissionEvaluator struct{}

func (matchedPermissionEvaluator) EvaluatePermissionForSession(sessionID, toolName, input string) safety.Decision {
	return safety.Decision{
		Behavior: safety.BehaviorAsk,
		Mode:     safety.ModeOnRequest,
		Reason:   "matched_ask_rule",
		Matched: &safety.PermissionRule{
			Behavior: safety.BehaviorAsk,
			Value:    safety.PermissionRuleValue{ToolName: toolName, CommandPrefix: []string{"npm", "run", "build"}},
			Source:   safety.SourceLocalSettings,
		},
	}
}

// The web surface answers a subagent's gate where the subagent is running, so
// a fanout child pauses on a question instead of being reported as a failed
// task. This is the same behaviour the terminal has; both go through pkg/turn.
func TestGatewaySubagentApprovalResumesTheChildInPlace(t *testing.T) {
	s, actions := newApprovalHTTPServer(t)
	ctx := context.Background()
	action, err := actions.CreatePending(ctx, "session-1", "shell", map[string]any{
		"command": "git grep -l todo", "session_id": "session-1", "subagent_type": "plan",
	})
	require.NoError(t, err)

	rae := &tool.RequiresActionError{
		RunID: "child-run-1", ActionID: action.ID, ActionKind: "shell", ToolName: "shell",
		SubagentType: "plan", AgentID: "task-1",
		ToolInput:       map[string]any{"command": "git grep -l todo"},
		SessionSnapshot: []llm.Message{llm.UserMessage(llm.Text("investigate"))},
	}

	var published []wsServerMsg
	var mu sync.Mutex
	go func() {
		// Stands in for the person clicking Approve in the web UI.
		require.Eventually(t, func() bool {
			mu.Lock()
			defer mu.Unlock()
			return len(published) > 0
		}, 2*time.Second, 5*time.Millisecond)
		_, err := actions.Approve(context.Background(), action.ID, "web")
		require.NoError(t, err)
	}()

	resumeCtx, err := s.promptGatewaySubagentApproval(ctx, rae, "req-1", "session-1", func(msg wsServerMsg) {
		mu.Lock()
		published = append(published, msg)
		mu.Unlock()
	})
	require.NoError(t, err)
	require.NotNil(t, resumeCtx)

	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, published)
	require.Equal(t, "requires_action", published[0].Op)
	require.Contains(t, published[0].Message, "plan")

	require.Equal(t, action.ID, tool.ApprovedActionIDFromContext(resumeCtx))
	resume := tool.ToolApprovalResumeFromContext(resumeCtx)
	require.NotNil(t, resume)
	require.False(t, resume.Denied)
	require.Len(t, resume.Session, 1)
}

// A denial is an answer, not a failure: the child continues and adapts, with
// the person's feedback in front of it.
func TestGatewaySubagentApprovalDenialResumesWithFeedback(t *testing.T) {
	s, actions := newApprovalHTTPServer(t)
	ctx := context.Background()
	action, err := actions.CreatePending(ctx, "session-1", "shell", map[string]any{"command": "rm -rf /", "session_id": "session-1"})
	require.NoError(t, err)
	rae := &tool.RequiresActionError{
		RunID: "child-run-1", ActionID: action.ID, ActionKind: "shell", ToolName: "shell",
		SessionSnapshot: []llm.Message{llm.UserMessage(llm.Text("investigate"))},
	}
	go func() {
		time.Sleep(20 * time.Millisecond)
		_, err := actions.Deny(context.Background(), action.ID, "read the file instead")
		require.NoError(t, err)
	}()

	resumeCtx, err := s.promptGatewaySubagentApproval(ctx, rae, "req-1", "session-1", func(wsServerMsg) {})
	require.NoError(t, err)
	resume := tool.ToolApprovalResumeFromContext(resumeCtx)
	require.NotNil(t, resume)
	require.True(t, resume.Denied)
	require.Equal(t, "read the file instead", resume.DenyReason)
}

// Cancelling ends the child's run: there is no answer to continue from.
func TestGatewaySubagentApprovalCancelEndsTheChild(t *testing.T) {
	s, actions := newApprovalHTTPServer(t)
	ctx := context.Background()
	action, err := actions.CreatePending(ctx, "session-1", "shell", map[string]any{"command": "ls", "session_id": "session-1"})
	require.NoError(t, err)
	rae := &tool.RequiresActionError{RunID: "child-run-1", ActionID: action.ID, ActionKind: "shell", ToolName: "shell"}
	go func() {
		time.Sleep(20 * time.Millisecond)
		_, err := actions.Cancel(context.Background(), action.ID, "not now")
		require.NoError(t, err)
	}()

	_, err = s.promptGatewaySubagentApproval(ctx, rae, "req-1", "session-1", func(wsServerMsg) {})
	require.Error(t, err)
	require.Contains(t, err.Error(), "cancelled while waiting for tool approval")
}

// The live dispatcher owns the resume before the request goes out, while the
// durable wait remains available for restart recovery.
func TestGatewaySubagentApprovalReleasesTheParkBeforeAsking(t *testing.T) {
	db, err := state.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	ctx := context.Background()
	actions := &state.ActionService{DB: db}
	runs := &state.RunStore{DB: db}
	s := &Server{Actions: actions, RunRT: runs, Runner: &run.Runner{Deps: &run.Deps{Home: t.TempDir()}}}

	mustGatewaySession(t, db, "session-1")
	child, err := runs.CreateRun(ctx, "session-1", "investigate")
	require.NoError(t, err)
	action, err := actions.CreatePending(ctx, "session-1", "shell", map[string]any{"command": "ls", "session_id": "session-1"})
	require.NoError(t, err)
	require.NoError(t, runs.SetWaitingAction(ctx, child.ID, state.Wait{
		RunID: child.ID, ActionID: action.ID, ToolName: "shell", ToolInputJSON: `{"command":"ls"}`,
	}))
	s.runController().TrackRuntime(child.ID, "session-1", func() {}, nil)
	require.True(t, s.runController().WaitApproval(child.ID))

	rae := &tool.RequiresActionError{RunID: child.ID, ActionID: action.ID, ActionKind: "shell", ToolName: "shell"}
	go func() {
		time.Sleep(20 * time.Millisecond)
		_, err := actions.Approve(context.Background(), action.ID, "web")
		require.NoError(t, err)
	}()
	resumeCtx, err := s.promptGatewaySubagentApproval(ctx, rae, "req-1", "session-1", func(wsServerMsg) {})
	require.NoError(t, err)

	// The process owner marker plus controller CAS stop a second continuation;
	// the row is intentionally retained until the resumed attempt settles.
	foundRun, wait, findErr := runs.FindRunByAction(ctx, action.ID)
	require.NoError(t, findErr)
	require.Equal(t, child.ID, foundRun)
	require.Equal(t, s.approvalResumeOwner(), wait.ResumeOwner)
	rn, err := runs.GetRun(ctx, child.ID)
	require.NoError(t, err)
	require.Equal(t, state.RunStatusRunning, rn.Status, "an answered child is running again, whether the answer was yes or no")
	require.False(t, s.runController().ClaimResume(child.ID, "session-1"), "the run phase must no longer be claimable")
	resume := tool.ToolApprovalResumeFromContext(resumeCtx)
	require.NotNil(t, resume)
	require.NotNil(t, resume.BeginContinuation)
	require.NotNil(t, resume.EndContinuation)

	// The fence is the replay's, not the decision's: it opens when the approved
	// call runs and closes with it, so nothing outside that window looks like a
	// tool whose outcome is unknown.
	require.NoError(t, resume.BeginContinuation(ctx))
	_, fenced, err := runs.FindRunByAction(ctx, action.ID)
	require.NoError(t, err)
	require.Equal(t, state.WaitResumePhaseExecutionStarted, fenced.ResumePhase)
	resume.EndContinuation(ctx)
	stored, err := runs.GetWaitForRun(ctx, child.ID)
	require.NoError(t, err)
	require.Nil(t, stored, "the replay is over, so its continuation is too")
}

func TestGatewayRecoveryDoesNotContinueCancelledApproval(t *testing.T) {
	db, err := state.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	ctx := context.Background()
	actions := &state.ActionService{DB: db}
	runs := &state.RunStore{DB: db}
	s := &Server{Actions: actions, RunRT: runs, Runner: &run.Runner{Deps: &run.Deps{Home: t.TempDir()}}}

	mustGatewaySession(t, db, "session-1")
	rn, err := runs.CreateRun(ctx, "session-1", "do not continue")
	require.NoError(t, err)
	action, err := actions.CreatePending(ctx, "session-1", "shell", map[string]any{"command": "rm nope", "session_id": "session-1"})
	require.NoError(t, err)
	require.NoError(t, runs.SetWaitingAction(ctx, rn.ID, state.Wait{RunID: rn.ID, ActionID: action.ID, ToolName: "shell"}))
	_, err = actions.Cancel(ctx, action.ID, "cancelled while gateway was down")
	require.NoError(t, err)

	s.resumeGatewayRun(action.ID, false)
	_, _, err = runs.FindRunByAction(ctx, action.ID)
	require.ErrorIs(t, err, state.ErrRunNotFound)
	stored, err := runs.GetRun(ctx, rn.ID)
	require.NoError(t, err)
	require.Equal(t, state.RunStatusCancelled, stored.Status)
}

func TestGatewayApprovalExpiryIsDurableVisibleAndTerminal(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(ctx, filepath.Join(t.TempDir(), "state.db"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	actions := &state.ActionService{DB: db}
	runs := &state.RunStore{DB: db}
	s := &Server{Actions: actions, RunRT: runs}

	mustGatewaySession(t, db, "session-expiry")
	runRecord, err := runs.CreateRun(ctx, "session-expiry", "wait for approval")
	require.NoError(t, err)
	action, err := actions.CreatePending(ctx, "session-expiry", "shell", map[string]any{"session_id": "session-expiry"})
	require.NoError(t, err)
	require.NoError(t, runs.SetWaitingAction(ctx, runRecord.ID, state.Wait{
		RunID: runRecord.ID, ActionID: action.ID, ToolName: "shell",
	}))
	expired, err := actions.ExpireIfPending(ctx, action.ID, "approval ttl expired")
	require.NoError(t, err)
	require.True(t, expired)

	s.expireGatewayApproval(ctx, action.ID)

	stored, err := runs.GetRun(ctx, runRecord.ID)
	require.NoError(t, err)
	require.Equal(t, state.RunStatusFailed, stored.Status)
	_, _, err = runs.FindRunByAction(ctx, action.ID)
	require.ErrorIs(t, err, state.ErrRunNotFound)
	page, err := runs.ListSessionEvents(ctx, "session-expiry", 0, 0, 10)
	require.NoError(t, err)
	require.Len(t, page.Events, 2)
	require.Equal(t, event.RunEventApprovalResolved, page.Events[0].Type)
	require.Equal(t, event.RunEventTurnError, page.Events[1].Type)
	var resolved event.ApprovalResolvedPayload
	require.NoError(t, json.Unmarshal(page.Events[0].Payload, &resolved))
	require.Equal(t, "expired", resolved.Decision)
}

// Every way a run ends carries its checklist facts — a cancelled or failed
// run closes with the same worked line a completed one does — and a
// subagent's checklist recorded under the run is never what it reports.
func TestRunEndingsCarryTheRunsChecklistFacts(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(ctx, filepath.Join(t.TempDir(), "state.db"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	runs := &state.RunStore{DB: db}
	s := &Server{RunRT: runs}

	mustGatewaySession(t, db, "session-plan")
	rn, err := runs.CreateRun(ctx, "session-plan", "work through a checklist")
	require.NoError(t, err)
	require.NoError(t, s.RunEvents().Publish(ctx, event.NewRunEvent("plan-1", rn.ID, "session-plan", event.RunEventPlanUpdated,
		event.PlanUpdatedPayload{Completed: 2, Total: 5, Items: []event.PlanUpdateItem{{ID: "3", Content: "wire the API", Status: "in_progress"}}}, time.Now())))
	require.NoError(t, s.RunEvents().Publish(ctx, event.NewRunEvent("plan-sub", rn.ID, "session-plan", event.RunEventPlanUpdated,
		event.PlanUpdatedPayload{Completed: 9, Total: 9, AgentID: "worker-1"}, time.Now())))

	require.NoError(t, s.publishGatewayRunEvent(ctx, "session-plan", rn.ID, event.RunEventTurnCancelled, event.TurnCancelledPayload{Message: "cancelled"}))
	require.NoError(t, s.publishGatewayRunEvent(ctx, "session-plan", rn.ID, event.RunEventTurnError, event.TurnErrorPayload{Error: "boom", Message: "boom"}))

	want := event.RunPlanFacts{PlanDone: 2, PlanTotal: 5, PlanActive: "wire the API"}
	cancelled, err := runs.ListRunEventsOfTypes(ctx, rn.ID, event.RunEventTurnCancelled)
	require.NoError(t, err)
	require.Len(t, cancelled, 1)
	var cancelledPayload event.TurnCancelledPayload
	require.NoError(t, json.Unmarshal(cancelled[0].Payload, &cancelledPayload))
	require.Equal(t, want, cancelledPayload.RunPlanFacts)

	failed, err := runs.ListRunEventsOfTypes(ctx, rn.ID, event.RunEventTurnError)
	require.NoError(t, err)
	require.Len(t, failed, 1)
	var failedPayload event.TurnErrorPayload
	require.NoError(t, json.Unmarshal(failed[0].Payload, &failedPayload))
	require.Equal(t, want, failedPayload.RunPlanFacts)
}
