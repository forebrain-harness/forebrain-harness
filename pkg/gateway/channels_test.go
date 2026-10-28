package gateway

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	"github.com/forebrain-harness/forebrain-harness/pkg/channel"
	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
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

// twoAgentChannelServer builds a gateway whose two primary agents each own a
// project with a webhook channel on its own inbound path. Channels are project
// data now, so they live in the state database.
func twoAgentChannelServer(t *testing.T, home string) *Server {
	t.Helper()
	t.Setenv("FOREBRAIN_HOME", home)
	cfg := &appcfg.Root{}
	cfg.Agents.Definitions = map[string]appcfg.AgentDefinition{
		"main": {},
		"acme": {Primary: true},
	}
	require.NoError(t, os.MkdirAll(filepath.Join(home, "state"), 0o755))
	db, err := state.OpenStateFromHome(context.Background(), home, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	seedProjectChannel(t, db, "main", filepath.Join(home, "workspace"), "/main/hook")
	seedProjectChannel(t, db, "acme", filepath.Join(home, "workspaces", "acme"), "/acme/hook")
	return &Server{
		Home:     home,
		Channels: channel.NewRegistry(),
		Env:      &process.Environment{SQL: db, Deps: run.Deps{AppCfg: cfg}},
	}
}

func seedProjectChannel(t *testing.T, db *sql.DB, agentID, root, inboundPath string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(root, 0o755))
	p, err := state.NewProjectStore(db, agentID).Create(context.Background(), state.CreateProjectInput{Name: agentID + "-p", Root: root})
	require.NoError(t, err)
	body := `{"enabled":true,"inbound_path":"` + inboundPath + `"}`
	require.NoError(t, state.NewProjectChannelStore(db, agentID).Upsert(context.Background(),
		state.ProjectChannel{ProjectID: p.ID, Kind: "webhook", Enabled: true, ConfigJSON: body}))
}

func summaryFor(home, id string) appcfg.Summary {
	root := filepath.Join(home, "workspace")
	if id != "main" {
		root = filepath.Join(home, "workspaces", id)
	}
	return appcfg.Summary{ID: id, WorkspaceRoot: root}
}

// Switching the active primary agent has to move the channels with it. A bot
// or inbound endpoint that outlived the switch would keep delivering its
// owner's messages into the agent that just became active.
func TestPrimaryAgentSwitchRebindsChannelsToTheNewAgent(t *testing.T) {
	home := t.TempDir()
	s := twoAgentChannelServer(t, home)

	s.bindChannelsTo(context.Background(), summaryFor(home, "main"))
	require.Equal(t, "main", s.Channels.AgentID())
	_, mounted := s.Channels.Route(http.MethodPost, "/main/hook")
	require.True(t, mounted, "main's inbound path should be mounted")
	_, mounted = s.Channels.Route(http.MethodPost, "/acme/hook")
	require.False(t, mounted, "another agent's inbound path must not be mounted")

	s.bindChannelsTo(context.Background(), summaryFor(home, "acme"))

	require.Equal(t, "acme", s.Channels.AgentID())
	_, mounted = s.Channels.Route(http.MethodPost, "/acme/hook")
	require.True(t, mounted, "acme's inbound path should be mounted after the switch")
	_, mounted = s.Channels.Route(http.MethodPost, "/main/hook")
	require.False(t, mounted, "the previous agent's inbound path survived the switch")
}

// Requests to a mounted channel path are served by the registry rather than by
// the static router, which cannot withdraw a route when the agent changes.
func TestChannelRoutesDispatchToTheBoundAgentsChannels(t *testing.T) {
	home := t.TempDir()
	s := twoAgentChannelServer(t, home)
	s.bindChannelsTo(context.Background(), summaryFor(home, "main"))

	fellThrough := false
	handler := s.channelHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fellThrough = true
		w.WriteHeader(http.StatusTeapot)
	}))

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/main/hook", nil))
	require.False(t, fellThrough, "a mounted channel path must not reach the static router")

	// A path belonging to another agent is not mounted, so it falls through
	// and 404s like any unknown route.
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/acme/hook", nil))
	require.True(t, fellThrough)
	require.Equal(t, http.StatusTeapot, rr.Code)
}

// Control-plane auth exemption follows the same agent: an inbound path is
// exempt only while the agent that owns it is active.
func TestControlPlaneExemptionFollowsTheActiveAgentsChannels(t *testing.T) {
	main := appcfg.ChannelsSection{Webhook: appcfg.Webhook{Enabled: true, InboundPath: "/main/hook"}}
	acme := appcfg.ChannelsSection{Webhook: appcfg.Webhook{Enabled: true, InboundPath: "/acme/hook"}}
	require.True(t, appcfg.GatewayControlPlaneAuthExemptPath("/main/hook", []appcfg.ChannelsSection{main}))
	require.False(t, appcfg.GatewayControlPlaneAuthExemptPath("/main/hook", []appcfg.ChannelsSection{acme}))
	require.False(t, appcfg.GatewayControlPlaneAuthExemptPath("/main/hook", nil))
}

func TestBuildToolStepNotificationForGatewayHook(t *testing.T) {
	var got tool.NotificationHookInput
	hook := tool.FuncNotificationHook(func(ctx context.Context, in tool.NotificationHookInput) error {
		got = in
		return nil
	})
	in, ok := tool.BuildToolStepHookInput("sess", "run", "webchat", tool.StepEvent{
		Kind:     event.RunEventToolCompleted,
		StepID:   "step-1",
		ToolName: "read_file",
		Output:   map[string]any{"preview_text": "match"},
	})
	if !ok {
		t.Fatal("expected tool step hook input")
	}
	if err := hook.Notify(context.Background(), in); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	if got.HookEventName != "Notification" {
		t.Fatalf("hook event=%q", got.HookEventName)
	}
	if got.SessionID != "sess" || got.RunID != "run" || got.Channel != "webchat" {
		t.Fatalf("lineage not preserved: %+v", got)
	}
	if got.NotificationType != "tool_result" || got.ToolName != "read_file" || got.ToolUseID != "step-1" {
		t.Fatalf("tool fields not preserved: %+v", got)
	}
}

func TestGatewayHookSurfacesRequestPermissions(t *testing.T) {
	in, ok := tool.BuildToolStepHookInput("sess", "run", "webchat", tool.StepEvent{
		Kind:     event.RunEventToolCompleted,
		ToolName: "request_permissions",
		Output: map[string]any{
			"scope":           "session",
			"permissions":     map[string]any{},
			"approval_status": "auto_approved",
			"approval_reason": "permission mode is auto-approve, which approves all tools without prompting",
		},
	})
	if !ok {
		t.Fatal("request_permissions must reach the Gateway notification hook")
	}
	if in.Message == "" {
		t.Fatal("request_permissions notification must carry an explanatory message")
	}
}

func TestServerNotifyRuntimeHookUsesStableNotificationContract(t *testing.T) {
	var got tool.NotificationHookInput
	s := &Server{
		NotificationHook: tool.FuncNotificationHook(func(ctx context.Context, in tool.NotificationHookInput) error {
			got = in
			return nil
		}),
	}

	s.notifyRuntimeHook(context.Background(), tool.NotificationRequest{
		Key:       "startup",
		Message:   "gateway ready",
		SessionID: "sess",
		RunID:     "run",
		Channel:   "gateway",
	})

	if got.HookEventName != "Notification" {
		t.Fatalf("hook event=%q", got.HookEventName)
	}
	if got.NotificationType != "startup" || got.Title != "Startup" || got.Message != "gateway ready" {
		t.Fatalf("notification contract mismatch: %+v", got)
	}
	if got.SessionID != "sess" || got.RunID != "run" || got.Channel != "gateway" {
		t.Fatalf("lineage not preserved: %+v", got)
	}
}

func TestServerNotifyRuntimeHookDropsUnknownNotifications(t *testing.T) {
	called := false
	s := &Server{
		NotificationHook: tool.FuncNotificationHook(func(ctx context.Context, in tool.NotificationHookInput) error {
			called = true
			return nil
		}),
	}

	s.notifyRuntimeHook(context.Background(), tool.NotificationRequest{Key: "remote-session", Message: "skip"})
	s.notifyRuntimeHook(context.Background(), tool.NotificationRequest{Key: "startup"})

	if called {
		t.Fatalf("unknown or empty notification should not call hook")
	}
}

func TestTryResumeRunAfterActionUsesWaitSnapshotForDeniedRuns(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.sqlite"))
	require.NoError(t, err)
	defer db.Close()

	if err := state.NewSessionStore(db, "main").Ensure(ctx, "s1", "s1"); err != nil {
		require.NoError(t, err)
	}
	actions := &state.ActionService{DB: db}
	runs := &state.RunStore{DB: db}
	mustGatewaySession(t, db, "s1")
	run, err := runs.CreateRun(ctx, "s1", "review request")
	require.NoError(t, err)
	act, err := actions.CreatePending(ctx, "s1", "shell", map[string]any{"command": "git status --short"})
	require.NoError(t, err)

	snapshot := []llm.Message{
		llm.UserMessage(llm.Text("review request")),
		llm.AssistantMessage([]llm.ContentPart{llm.Text("checking status")}, llm.ToolCall{
			ID:       "call-1",
			Type:     llm.ToolTypeFunction,
			Function: llm.FunctionCall{Name: "shell", Arguments: `{"command":"git status --short"}`},
		}),
	}
	require.NoError(t, runs.SetWaitingAction(ctx, run.ID, state.Wait{
		RunID:           run.ID,
		ActionID:        act.ID,
		ToolName:        "shell",
		ToolInputJSON:   `{"command":"git status --short"}`,
		SessionSnapshot: snapshot,
	}))

	runID, wait, err := runs.FindRunByAction(ctx, act.ID)
	require.NoError(t, err)
	require.Equal(t, run.ID, runID)
	require.NotNil(t, wait)
	require.Len(t, wait.SessionSnapshot, 2)
	require.Equal(t, "checking status", wait.SessionSnapshot[1].TextContent())
}

func TestHandleActionsEnrichesToolApprovalsWithPermissionSuggestion(t *testing.T) {
	db, err := state.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"), nil)
	require.NoError(t, err)
	defer db.Close()

	if err := state.NewSessionStore(db, "main").Ensure(context.Background(), "s1", "s1"); err != nil {
		require.NoError(t, err)
	}
	actions := &state.ActionService{DB: db}
	_, err = actions.CreatePending(context.Background(), "s1", "shell", map[string]any{"command": "npm run build"})
	require.NoError(t, err)

	runner := &run.Runner{Deps: &run.Deps{}}
	runner.ApplyPermissionUpdate(safety.PermissionUpdate{Type: safety.UpdateSetMode, Destination: safety.DestinationSession, SessionID: "session-1", Mode: safety.ModeOnRequest})
	s := &Server{Actions: actions, Runner: runner}

	req := httptest.NewRequest(http.MethodGet, "/api/actions?status=pending", nil)
	rr := httptest.NewRecorder()
	s.handleActions(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	var rows []struct {
		Kind                 string `json:"kind"`
		PermissionSuggestion *struct {
			PermissionToolName string   `json:"permission_tool_name"`
			PermissionInput    string   `json:"permission_input"`
			ExactRuleContent   string   `json:"exact_rule_content"`
			PrefixRuleContent  string   `json:"prefix_rule_content"`
			DestinationOptions []string `json:"destination_options"`
			SuggestedDest      string   `json:"suggested_destination"`
			PermissionMode     string   `json:"permission_mode"`
		} `json:"permission_suggestion"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &rows))
	require.Len(t, rows, 1)
	require.Equal(t, "shell", rows[0].Kind)
	require.NotNil(t, rows[0].PermissionSuggestion)
	require.Equal(t, "Bash", rows[0].PermissionSuggestion.PermissionToolName)
	require.Equal(t, "npm run build", rows[0].PermissionSuggestion.PermissionInput)
	require.Equal(t, "npm run build", rows[0].PermissionSuggestion.ExactRuleContent)
	require.Equal(t, "npm run build:*", rows[0].PermissionSuggestion.PrefixRuleContent)
	require.Contains(t, rows[0].PermissionSuggestion.DestinationOptions, string(safety.DestinationLocalSettings))
	require.Equal(t, string(safety.DestinationLocalSettings), rows[0].PermissionSuggestion.SuggestedDest)
	require.Equal(t, string(safety.ModeOnRequest), rows[0].PermissionSuggestion.PermissionMode)
}

// The file view belongs to the conversation, not the process: its root is the
// project the session is filed under, so the tree reads that project's
// directory and never the active agent's own workspace.
func TestGatewayWorkspaceAPIsUseTheConversationsProject(t *testing.T) {
	home := t.TempDir()
	mainWS := filepath.Join(home, "workspace")
	reviewWS := filepath.Join(home, "workspaces", "review")
	require.NoError(t, os.MkdirAll(mainWS, 0o755))
	require.NoError(t, os.MkdirAll(reviewWS, 0o755))
	// One marker file per directory plus a nested file only in the project
	// root: the tree must read the conversation's project, and only one level
	// of it.
	require.NoError(t, os.WriteFile(filepath.Join(mainWS, "main-only.md"), []byte("main only"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(reviewWS, "review-only.md"), []byte("review only"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(reviewWS, "docs"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(reviewWS, "docs", "a.md"), []byte("nested"), 0o644))
	s := newActiveReviewGatewayServer(t, home)
	sid := scopeServerToRoot(t, s, home, reviewWS)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/workspace/tree?session_id="+sid, nil)
	s.handleWorkspaceTree(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Contains(t, rr.Body.String(), `"path":"review-only.md"`)
	require.Contains(t, rr.Body.String(), `"name":"docs"`)
	require.NotContains(t, rr.Body.String(), "main-only.md")
	require.NotContains(t, rr.Body.String(), "docs/a.md")

	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/workspace/tree?session_id="+sid+"&path=docs", nil)
	s.handleWorkspaceTree(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Contains(t, rr.Body.String(), `"path":"docs/a.md"`)

	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/workspace/snippet?session_id="+sid+"&path=review-only.md", nil)
	s.handleWorkspaceSnippet(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Contains(t, rr.Body.String(), "review only")
	require.NotContains(t, rr.Body.String(), "main only")
}

func TestGatewaySkillsListUsesActivePrimaryWorkspace(t *testing.T) {
	home := t.TempDir()
	mainSkill := filepath.Join(home, "workspace", "skills", "main-skill", "SKILL.md")
	reviewSkill := filepath.Join(home, "workspaces", "review", "skills", "review-skill", "SKILL.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(mainSkill), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Dir(reviewSkill), 0o755))
	require.NoError(t, os.WriteFile(mainSkill, []byte("---\nname: main-skill\ndescription: main\n---\n"), 0o600))
	require.NoError(t, os.WriteFile(reviewSkill, []byte("---\nname: review-skill\ndescription: review\n---\n"), 0o600))
	s := newActiveReviewGatewayServer(t, home)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/skills", nil)
	s.handleSkillsList(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	var body struct {
		Installed []struct {
			Name     string `json:"name"`
			RootPath string `json:"root_path"`
		} `json:"installed"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	var names []string
	for _, item := range body.Installed {
		names = append(names, item.Name)
	}
	require.Contains(t, names, "review-skill")
	require.NotContains(t, names, "main-skill")
}

func newActiveReviewGatewayServer(t *testing.T, home string) *Server {
	t.Helper()
	cfg := &appcfg.Root{}
	cfg.Agents.Definitions = map[string]appcfg.AgentDefinition{
		"main":   {},
		"review": {Primary: true},
	}
	require.NoError(t, os.MkdirAll(filepath.Join(home, "state"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(home, "state", "primary-agent.json"), []byte(`{"active":"review"}`+"\n"), 0o600))
	_ = context.Background()
	return &Server{Home: home, Env: &process.Environment{Deps: run.Deps{AppCfg: cfg}}}
}

func TestWebchatStepDisplayBodyMatchesAgentnotifyFormatter(t *testing.T) {
	t.Parallel()
	evt := tool.StepEvent{
		Kind:     event.RunEventToolCompleted,
		ToolName: "read_file",
		Input:    map[string]any{"file_path": "a.go"},
		Output:   map[string]any{"preview_text": "1|x\n", "preview_kind": "file"},
	}
	got, trunc := tool.FormatToolStepResult(evt, tool.DefaultMaxFormattedBody)
	if trunc {
		t.Fatal("unexpected trunc")
	}
	if !strings.Contains(got, "```go") {
		t.Fatalf("expected fenced go: %q", got)
	}
}

func TestWebchatIntermediateToolDisplayBodyMatchesAgentnotifyFormatter(t *testing.T) {
	t.Parallel()
	evt := tool.StepEvent{
		Kind:     event.RunEventToolCompleted,
		ToolName: "intermediate_tool",
		Input:    map[string]any{"action": "read"},
		Output:   map[string]any{"action": "read", "content": "Conclusion A\nConclusion B"},
	}
	got, trunc := tool.FormatToolStepResult(evt, tool.DefaultMaxFormattedBody)
	if trunc {
		t.Fatal("unexpected trunc")
	}
	if !strings.Contains(got, "Conclusion A") {
		t.Fatalf("expected intermediate tool display body, got %q", got)
	}
}

func TestGatewayControlPlaneAuthTokenUsesGatewayToken(t *testing.T) {
	want := strings.Repeat("a", 64)
	rt := process.Context{}
	rt.Config.Gateway.Auth.Token = want
	if got := gatewayControlPlaneAuthToken(rt); got != want {
		t.Fatalf("gatewayControlPlaneAuthToken = %q, want %q", got, want)
	}
}

func TestGatewayControlPlaneAuthTokenEmptyWhenUnset(t *testing.T) {
	if got := gatewayControlPlaneAuthToken(process.Context{}); got != "" {
		t.Fatalf("gatewayControlPlaneAuthToken = %q, want empty", got)
	}
}

func TestMain(m *testing.M) {
	if data, err := llm.RepoModelsJSONForTest(); err == nil {
		if cat, err := llm.Parse(data); err == nil {
			llm.SetGlobalCatalogForTest(cat)
		}
	}
	// Point the OS user home at a scratch directory for the whole package.
	// Skill discovery reads user-level roots (~/.agents, ~/.claude, ~/.codex),
	// so without this a developer's own skill library registers real slash
	// commands into the global registry and assertions about which commands
	// exist depend on whose machine runs the tests.
	home, err := os.MkdirTemp("", "gateway-test-home")
	if err == nil {
		os.Setenv("HOME", home)
		os.Setenv("USERPROFILE", home)
	}
	code := m.Run()
	if home != "" {
		os.RemoveAll(home)
	}
	os.Exit(code)
}

func TestHandleModelsListUsesAppCoreCatalog(t *testing.T) {
	s := &Server{Core: turn.New()}
	req := httptest.NewRequest(http.MethodGet, "/api/models?provider=openai&limit=5", nil)
	rr := httptest.NewRecorder()

	s.handleModelsList(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	var out struct {
		Records []turn.ModelRecord `json:"records"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
	require.NotEmpty(t, out.Records)
	require.LessOrEqual(t, len(out.Records), 5)
	for _, rec := range out.Records {
		require.Equal(t, "openai", rec.Provider)
		require.NotEmpty(t, rec.ModelID)
		require.NotEmpty(t, rec.APIModel)
	}
}

func TestPermissionRulesAndEvaluateEndpoints(t *testing.T) {
	const sessionID = "session-1"
	runner := &run.Runner{Deps: &run.Deps{}}
	runner.ApplyPermissionUpdate(safety.PermissionUpdate{
		Type:        safety.UpdateAddRules,
		Destination: safety.DestinationSession,
		SessionID:   sessionID,
		Behavior:    safety.BehaviorAllow,
		Rules: []safety.PermissionRuleValue{{
			ToolName:    "Bash",
			RuleContent: "npm run:*",
		}},
	})
	s := &Server{Runner: runner}

	reqList := httptest.NewRequest(http.MethodGet, "/api/permissions/rules?session_id="+sessionID, nil)
	rrList := httptest.NewRecorder()
	s.handlePermissionRules(rrList, reqList)
	if rrList.Code != http.StatusOK {
		t.Fatalf("rules status=%d body=%s", rrList.Code, rrList.Body.String())
	}
	var listResp struct {
		Mode  string `json:"mode"`
		Rules []struct {
			ToolName    string `json:"tool_name"`
			RuleContent string `json:"rule_content"`
		} `json:"rules"`
	}
	if err := json.Unmarshal(rrList.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("rules decode: %v", err)
	}
	if len(listResp.Rules) == 0 {
		t.Fatalf("expected at least one rule")
	}

	body := map[string]string{"tool_name": "Bash", "input": "npm run build", "session_id": sessionID}
	raw, _ := json.Marshal(body)
	reqEval := httptest.NewRequest(http.MethodPost, "/api/permissions/evaluate", bytes.NewReader(raw))
	rrEval := httptest.NewRecorder()
	s.handlePermissionEvaluate(rrEval, reqEval)
	if rrEval.Code != http.StatusOK {
		t.Fatalf("evaluate status=%d body=%s", rrEval.Code, rrEval.Body.String())
	}
	var dec safety.Decision
	if err := json.Unmarshal(rrEval.Body.Bytes(), &dec); err != nil {
		t.Fatalf("evaluate decode: %v", err)
	}
	if dec.Behavior != safety.BehaviorAllow {
		t.Fatalf("expected allow, got %s (%s)", dec.Behavior, dec.Reason)
	}

	reqExplain := httptest.NewRequest(http.MethodGet, "/api/permissions/explain?tool_name=Bash&input=npm%20run%20build&session_id="+sessionID, nil)
	rrExplain := httptest.NewRecorder()
	s.handlePermissionExplain(rrExplain, reqExplain)
	if rrExplain.Code != http.StatusOK {
		t.Fatalf("explain status=%d body=%s", rrExplain.Code, rrExplain.Body.String())
	}
	var ex struct {
		Decision safety.Decision `json:"decision"`
		Rules    []struct {
			Matched bool `json:"matched"`
		} `json:"rules"`
	}
	if err := json.Unmarshal(rrExplain.Body.Bytes(), &ex); err != nil {
		t.Fatalf("explain decode: %v", err)
	}
	if ex.Decision.Behavior != safety.BehaviorAllow {
		t.Fatalf("expected explain decision allow, got %s", ex.Decision.Behavior)
	}
	foundMatched := false
	for _, it := range ex.Rules {
		if it.Matched {
			foundMatched = true
			break
		}
	}
	if !foundMatched {
		t.Fatalf("expected at least one matched rule in explain output")
	}
}

func TestPermissionUpdateEndpointRefreshesSandboxRuntime(t *testing.T) {
	const sessionID = "session-1"
	home := t.TempDir()
	runner := &run.Runner{Deps: &run.Deps{Home: home}}
	cfg := &appcfg.Root{}
	s := &Server{
		Home:   home,
		Runner: runner,
		Env:    &process.Environment{Deps: run.Deps{AppCfg: cfg}, Root: home, Sandbox: safety.NewManager(), Runner: runner},
	}
	body := safety.PermissionUpdate{
		Type:        safety.UpdateAddRules,
		Destination: safety.DestinationSession,
		SessionID:   sessionID,
		Behavior:    safety.BehaviorAllow,
		Rules: []safety.PermissionRuleValue{{
			ToolName:    "Edit",
			RuleContent: "//tmp/runtime-shared",
		}},
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/permissions/updates", bytes.NewReader(raw))
	rr := httptest.NewRecorder()
	s.handlePermissionUpdate(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", rr.Code, rr.Body.String())
	}
	rc := s.Env.Sandbox.RuntimeConfig()
	for _, p := range rc.Filesystem.AllowWrite {
		if p == "/tmp/runtime-shared" {
			t.Fatalf("session-scoped path leaked into global sandbox runtime: %+v", rc.Filesystem.AllowWrite)
		}
	}
	if d := runner.EvaluatePermissionForSession(sessionID, "Edit", "//tmp/runtime-shared"); d.Behavior != safety.BehaviorAllow {
		t.Fatalf("expected owning session to allow the path, got %+v", d)
	}
	if d := runner.EvaluatePermissionForSession("session-2", "Edit", "//tmp/runtime-shared"); d.Matched != nil {
		t.Fatalf("session path rule leaked to another session: %+v", d)
	}
}

func TestPermissionUpdateEndpointSessionOnly(t *testing.T) {
	const sessionID = "session-1"
	home := t.TempDir()
	runner := &run.Runner{Deps: &run.Deps{Home: home}}
	s := &Server{Home: home, Runner: runner, Env: &process.Environment{Deps: run.Deps{Home: home, AppCfg: &appcfg.Root{}}, Root: home, Sandbox: safety.NewManager(), Runner: runner}}
	body := safety.PermissionUpdate{
		Type:        safety.UpdateAddRules,
		Destination: safety.DestinationSession,
		SessionID:   sessionID,
		Behavior:    safety.BehaviorAllow,
		Rules:       []safety.PermissionRuleValue{{ToolName: "Bash", RuleContent: "git status"}},
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/permissions/updates", bytes.NewReader(raw))
	rr := httptest.NewRecorder()
	s.handlePermissionUpdate(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", rr.Code, rr.Body.String())
	}
	d := runner.EvaluatePermissionForSession(sessionID, "Bash", "git status")
	if d.Behavior != safety.BehaviorAllow {
		t.Fatalf("expected allow after update, got %s", d.Behavior)
	}
	if other := runner.EvaluatePermissionForSession("session-2", "Bash", "git status"); other.Matched != nil {
		t.Fatalf("session rule leaked to another session: %+v", other)
	}

	persistent := safety.PermissionUpdate{
		Type:        safety.UpdateAddRules,
		Destination: safety.DestinationLocalSettings,
		Behavior:    safety.BehaviorAllow,
		Rules:       []safety.PermissionRuleValue{{ToolName: "Bash", RuleContent: "git status"}},
	}
	rawPersistent, _ := json.Marshal(persistent)
	reqPersistent := httptest.NewRequest(http.MethodPost, "/api/permissions/updates", bytes.NewReader(rawPersistent))
	rrPersistent := httptest.NewRecorder()
	s.handlePermissionUpdate(rrPersistent, reqPersistent)
	if rrPersistent.Code != http.StatusOK {
		t.Fatalf("expected 200 for localSettings destination, got %d: %s", rrPersistent.Code, rrPersistent.Body.String())
	}
	// The agent's rule is on disk and in the runner that is already running.
	if _, err := os.Stat(filepath.Join(home, "workspace", "state", "permissions", "local_settings.json")); err != nil {
		t.Fatalf("local rule not persisted: %v", err)
	}
	if d := runner.EvaluatePermissionForSession("session-3", "Bash", "git status"); d.Matched == nil || d.Matched.Source != safety.SourceLocalSettings {
		t.Fatalf("live runner did not take the agent's rule: %+v", d)
	}

	// A project's rules belong to the project space, never to whichever
	// project this process was launched in.
	project := safety.PermissionUpdate{Type: safety.UpdateAddRules, Destination: safety.DestinationProjectSettings, Behavior: safety.BehaviorDeny, Rules: []safety.PermissionRuleValue{{ToolName: "Bash", RuleContent: "rm:*"}}}
	rawProject, _ := json.Marshal(project)
	rrProject := httptest.NewRecorder()
	s.handlePermissionUpdate(rrProject, httptest.NewRequest(http.MethodPost, "/api/permissions/updates", bytes.NewReader(rawProject)))
	if rrProject.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for projectSettings destination, got %d", rrProject.Code)
	}

	invalid := map[string]any{"type": "addRules", "destination": "cliArg", "behavior": "allow", "rules": []map[string]any{{"tool_name": "Bash", "rule_content": "x"}}}
	rawInvalid, _ := json.Marshal(invalid)
	reqInvalid := httptest.NewRequest(http.MethodPost, "/api/permissions/updates", bytes.NewReader(rawInvalid))
	rrInvalid := httptest.NewRecorder()
	s.handlePermissionUpdate(rrInvalid, reqInvalid)
	if rrInvalid.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unsupported destination, got %d", rrInvalid.Code)
	}
}

// A conversation's permissions are answered and changed in the runner that
// conversation runs on — a project session's pooled runner — never in the
// process facade, which is bound to the gateway's own runner.
func TestPermissionEndpointsAnswerFromTheSessionsOwnRunner(t *testing.T) {
	const sessionID = "session-1"
	facade := &run.Runner{Deps: &run.Deps{}}
	sessionRunner := &run.Runner{Deps: &run.Deps{}}
	s := &Server{Core: turn.New(turn.WithPermissionFacade(facade)), Env: &process.Environment{Runner: sessionRunner}}

	body := safety.PermissionUpdate{
		Type:        safety.UpdateAddRules,
		Destination: safety.DestinationSession,
		SessionID:   sessionID,
		Behavior:    safety.BehaviorAllow,
		Rules:       []safety.PermissionRuleValue{{ToolName: "Bash", RuleContent: "npm run:*"}},
	}
	raw, _ := json.Marshal(body)
	rrUpdate := httptest.NewRecorder()
	s.handlePermissionUpdate(rrUpdate, httptest.NewRequest(http.MethodPost, "/api/permissions/updates", bytes.NewReader(raw)))
	if rrUpdate.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", rrUpdate.Code, rrUpdate.Body.String())
	}
	if d := facade.EvaluatePermissionForSession(sessionID, "Bash", "npm run build"); d.Matched != nil {
		t.Fatalf("session rule landed in the process facade: %+v", d)
	}

	reqList := httptest.NewRequest(http.MethodGet, "/api/permissions/rules?session_id="+sessionID, nil)
	rrList := httptest.NewRecorder()
	s.handlePermissionRules(rrList, reqList)
	if rrList.Code != http.StatusOK || !strings.Contains(rrList.Body.String(), "npm run:*") {
		t.Fatalf("rules status=%d body=%s", rrList.Code, rrList.Body.String())
	}

	evalBody, _ := json.Marshal(map[string]string{"tool_name": "Bash", "input": "npm run build", "session_id": sessionID})
	rrEval := httptest.NewRecorder()
	s.handlePermissionEvaluate(rrEval, httptest.NewRequest(http.MethodPost, "/api/permissions/evaluate", bytes.NewReader(evalBody)))
	if rrEval.Code != http.StatusOK {
		t.Fatalf("evaluate status=%d body=%s", rrEval.Code, rrEval.Body.String())
	}
	var dec safety.Decision
	if err := json.Unmarshal(rrEval.Body.Bytes(), &dec); err != nil {
		t.Fatalf("evaluate decode: %v", err)
	}
	if dec.Behavior != safety.BehaviorAllow {
		t.Fatalf("expected allow from the session's runner, got %s (%s)", dec.Behavior, dec.Reason)
	}
}

// The agent's permission page answers for the agent alone: the rules of the
// project the gateway was launched in are neither listed nor consulted.
func TestAgentPermissionEndpointsLeaveTheLaunchProjectOut(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(project, ".git"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(project, ".forebrain"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(project, ".forebrain", "safety.json"),
		[]byte(`{"rules":{"deny":[{"tool_name":"Bash","rule_content":"git push:*"}]}}`), 0o600))
	launched := &run.Runner{Deps: &run.Deps{Home: home, ProjectRoot: project}}
	launched.ApplyPermissionUpdate(safety.PermissionUpdate{
		Type: safety.UpdateAddRules, Destination: safety.DestinationProjectSettings, Behavior: safety.BehaviorDeny,
		Rules: []safety.PermissionRuleValue{{ToolName: "Bash", RuleContent: "git push:*"}},
	})
	require.Equal(t, safety.BehaviorDeny, launched.EvaluatePermissionForSession("", "Bash", "git push").Behavior,
		"the launched runner holds the project's rule")
	s := &Server{Home: home, Runner: launched, Env: &process.Environment{Deps: run.Deps{Home: home, AppCfg: &appcfg.Root{}}, Root: home, Runner: launched}}

	rr := httptest.NewRecorder()
	s.handlePermissionRules(rr, httptest.NewRequest(http.MethodGet, "/api/permissions/rules", nil))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.NotContains(t, rr.Body.String(), "git push")

	rr = httptest.NewRecorder()
	s.handlePermissionExplain(rr, httptest.NewRequest(http.MethodGet, "/api/permissions/explain?tool_name=Bash&input=git%20push", nil))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var ex safety.ExplainResult
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &ex))
	require.NotEqual(t, safety.BehaviorDeny, ex.Decision.Behavior, rr.Body.String())
}

// Plan files live in <stateRoot>/plans/<projectKey>/<sessionID>, and the
// plan-mode reminder tells the model that exact directory. The key is the
// session's own project key: a gateway process has no launch project of its
// own, so the base runner carries none, and a context wired from the base
// runner (or any other key) would permit writes to another directory, so every
// plan write the model attempts is rejected.
func TestGatewayPlanContextMatchesAdvertisedPlanDir(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	sid := "sid-gateway-plan"
	projects := state.NewProjectStore(db, "main")
	project, err := projects.Create(ctx, state.CreateProjectInput{Name: "planctx", Root: t.TempDir()})
	require.NoError(t, err)
	require.NoError(t, state.NewSessionStore(db, "main").Ensure(ctx, sid, sid))
	require.NoError(t, projects.BindSession(ctx, sid, project.ID))

	s := &Server{Home: home, Projects: projects, Runner: &run.Runner{Deps: &run.Deps{Home: home}}}

	key := s.sessionProjectKey(ctx, sid)
	if key != state.ProjectKeyForRoot(project.Root) {
		t.Fatalf("sessionProjectKey()=%q, want the session project's key", key)
	}

	stateRoot := s.stateRoot()
	if _, err := state.Switch(stateRoot, sid, state.ModePlan); err != nil {
		t.Fatalf("enter plan mode: %v", err)
	}

	ctx = process.AgentContextForProject(ctx, stateRoot, sid, s.sessionProjectKey(ctx, sid))
	want := state.PlanDirForSession(stateRoot, key, sid)
	if got := tool.AllowedPlanPathFromContext(ctx); got != want {
		t.Fatalf("allowed plan path=%q, want the advertised plan dir %q", got, want)
	}
}

func TestRestServerVerbHelpersAndGroups(t *testing.T) {
	srv := NewRestServer("127.0.0.1:6060")
	var calls []string
	srv.Use(namedMiddleware("outer", &calls), namedMiddleware("inner", &calls))

	srv.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, "handler")
		_, _ = w.Write([]byte("ok"))
	})
	api := srv.Group("/api")
	api.Get("/resources/:id", func(w http.ResponseWriter, r *http.Request) {
		id := ParamsFromContext(r.Context()).ByName("id")
		_, _ = w.Write([]byte(id))
	})
	api.Group("/jobs").Post("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})

	rr := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rr.Code != http.StatusOK || rr.Body.String() != "ok" {
		t.Fatalf("GET /health code=%d body=%q", rr.Code, rr.Body.String())
	}
	wantCalls := []string{"outer before", "inner before", "handler", "inner after", "outer after"}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("middleware calls=%v want %v", calls, wantCalls)
	}

	rr = httptest.NewRecorder()
	srv.Handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/resources/t-1", nil))
	if rr.Code != http.StatusOK || rr.Body.String() != "t-1" {
		t.Fatalf("GET /api/resources/:id code=%d body=%q", rr.Code, rr.Body.String())
	}

	rr = httptest.NewRecorder()
	srv.Handler.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/jobs", nil))
	if rr.Code != http.StatusCreated {
		t.Fatalf("POST /api/jobs code=%d want %d", rr.Code, http.StatusCreated)
	}
}

func TestRestServerRouteAdderAdapter(t *testing.T) {
	srv := NewRestServer("127.0.0.1:6060")
	add := srv.RouteAdder()
	add(http.MethodGet, "/legacy", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("legacy"))
	})

	rr := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/legacy", nil))
	if rr.Code != http.StatusOK || rr.Body.String() != "legacy" {
		t.Fatalf("GET /legacy code=%d body=%q", rr.Code, rr.Body.String())
	}
}

func TestNewRestServerDefaultsAddr(t *testing.T) {
	srv := NewRestServer("")
	if srv.Addr != "127.0.0.1:6060" {
		t.Fatalf("Addr = %q, want default 127.0.0.1:6060", srv.Addr)
	}
}

func namedMiddleware(name string, calls *[]string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			*calls = append(*calls, name+" before")
			next.ServeHTTP(w, r)
			*calls = append(*calls, name+" after")
		})
	}
}

// hit records which route fired and the params it saw.
type hit struct {
	tag    string
	params Params
}

func handler(tag string, rec *hit) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rec.tag = tag
		rec.params = ParamsFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}
}

func TestRouterMatching(t *testing.T) {
	var rec hit
	r := New()
	r.Handle(http.MethodGet, "/api/resources", handler("list", &rec))
	r.Handle(http.MethodGet, "/api/resources/board", handler("board", &rec))
	r.Handle(http.MethodGet, "/api/resources/:id", handler("one", &rec))
	r.Handle(http.MethodGet, "/api/resources/:id/items/:itemId", handler("item", &rec))
	r.Handle(http.MethodGet, "/assets/*filepath", handler("assets", &rec))
	r.Handle(http.MethodGet, "/", handler("root", &rec))
	r.Handle(http.MethodGet, "/*filepath", handler("spa", &rec))
	r.Handle(http.MethodPost, "/api/resources", handler("create", &rec))

	cases := []struct {
		method, path string
		wantTag      string
		wantParams   map[string]string
	}{
		{http.MethodGet, "/api/resources", "list", nil},
		{http.MethodGet, "/api/resources/board", "board", nil},                                                // static beats :id
		{http.MethodGet, "/api/resources/abc", "one", map[string]string{"id": "abc"}},                         // named param
		{http.MethodGet, "/api/resources/r1/items/i9", "item", map[string]string{"id": "r1", "itemId": "i9"}}, // multi param
		{http.MethodGet, "/assets/js/app.js", "assets", map[string]string{"filepath": "/js/app.js"}},          // catch-all
		{http.MethodGet, "/", "root", nil},                                                                    // exact root beats /*filepath
		{http.MethodGet, "/dashboard", "spa", map[string]string{"filepath": "/dashboard"}},                    // spa fallback
		{http.MethodPost, "/api/resources", "create", nil},                                                    // method discrimination
	}
	for _, c := range cases {
		rec = hit{}
		req := httptest.NewRequest(c.method, c.path, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s %s: status %d", c.method, c.path, w.Code)
		}
		if rec.tag != c.wantTag {
			t.Fatalf("%s %s: matched %q, want %q", c.method, c.path, rec.tag, c.wantTag)
		}
		for k, v := range c.wantParams {
			if got := rec.params.ByName(k); got != v {
				t.Fatalf("%s %s: param %q = %q, want %q", c.method, c.path, k, got, v)
			}
		}
	}
}

func TestRouterNotFoundAndMethodNotAllowed(t *testing.T) {
	var rec hit
	r := New()
	r.Handle(http.MethodGet, "/api/ping", handler("ping", &rec))

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/missing", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing path: status %d, want 404", w.Code)
	}

	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/ping", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("wrong method: status %d, want 405", w.Code)
	}
}

// TestEmbeddedStaticServing wires the real router, the SPA handler, and the
// embedded frontend FS together — the same composition serve_run.go builds —
// and verifies API routes still win over the SPA catch-all.
func TestEmbeddedStaticServing(t *testing.T) {
	fsys, ok := FS()
	if !ok {
		t.Skip("frontend not built (placeholder only); skipping embedded serving test")
	}

	r := New()
	r.Handle(http.MethodGet, "/api/health", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	serve := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { serveSPA(w, req, fsys) })
	r.Handle(http.MethodGet, "/", serve)
	r.Handle(http.MethodGet, "/*filepath", serve)

	// pick a real asset to request
	assets, err := fs.ReadDir(fsys, "assets")
	if err != nil || len(assets) == 0 {
		t.Fatalf("no embedded assets: %v", err)
	}
	assetPath := "/assets/" + assets[0].Name()

	cases := []struct {
		path     string
		wantCode int
		wantBody string // exact match when non-empty
	}{
		{"/api/health", http.StatusOK, "ok"}, // API beats /*filepath
		{assetPath, http.StatusOK, ""},       // real embedded asset
		{"/dashboard", http.StatusOK, ""},    // SPA fallback → index.html
		{"/", http.StatusOK, ""},             // root → index.html
	}
	for _, c := range cases {
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, c.path, nil))
		if rr.Code != c.wantCode {
			t.Fatalf("%s: code=%d want %d", c.path, rr.Code, c.wantCode)
		}
		if c.wantBody != "" && rr.Body.String() != c.wantBody {
			t.Fatalf("%s: body=%q want %q", c.path, rr.Body.String(), c.wantBody)
		}
	}
}

type captureGetRoutes struct {
	routes []string
}

func (r *captureGetRoutes) Get(path string, h http.HandlerFunc) {
	r.routes = append(r.routes, http.MethodGet+" "+path)
}

func TestStaticSPAHandlers(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "assets"), 0o755); err != nil {
		t.Fatalf("mkdir assets: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("index"), 0o600); err != nil {
		t.Fatalf("write index: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "assets", "app.js"), []byte("asset"), 0o600); err != nil {
		t.Fatalf("write asset: %v", err)
	}

	// StaticDist registers exactly the root + catch-all routes.
	capture := &captureGetRoutes{}
	(&Server{StaticDist: root}).attachStaticUI(capture)
	if len(capture.routes) != 2 {
		t.Fatalf("routes=%v", capture.routes)
	}
	// No static source → no routes.
	empty := &captureGetRoutes{}
	(&Server{}).attachStaticUI(empty)
	if len(empty.routes) != 0 {
		t.Fatalf("no static source should not add routes: %v", empty.routes)
	}
	missing := &captureGetRoutes{}
	(&Server{StaticDist: filepath.Join(root, "missing")}).attachStaticUI(missing)
	if len(missing.routes) != 0 {
		t.Fatalf("missing static dist should not add routes: %v", missing.routes)
	}

	fsys := (&Server{StaticDist: root}).resolveStaticFS()
	if fsys == nil {
		t.Fatal("resolveStaticFS returned nil for valid dir")
	}

	cases := []struct {
		path     string
		wantCode int
		wantBody string
	}{
		{"/assets/app.js", http.StatusOK, "asset"},       // existing nested file
		{"/index.html", http.StatusMovedPermanently, ""}, // stdlib redirects /index.html → /
		{"/deep/link", http.StatusOK, "index"},           // SPA fallback
		{"/", http.StatusOK, "index"},                    // root → index
		{"/api/v1", http.StatusNotFound, ""},             // api never falls back
		{"/ws", http.StatusNotFound, ""},                 // ws never falls back
		{"/../etc/passwd", http.StatusOK, "index"},       // traversal rejected → fallback
	}
	for _, c := range cases {
		rr := httptest.NewRecorder()
		serveSPA(rr, httptest.NewRequest(http.MethodGet, c.path, nil), fsys)
		if rr.Code != c.wantCode {
			t.Fatalf("%s: code=%d want %d", c.path, rr.Code, c.wantCode)
		}
		if c.wantBody != "" && rr.Body.String() != c.wantBody {
			t.Fatalf("%s: body=%q want %q", c.path, rr.Body.String(), c.wantBody)
		}
	}
}

func TestHandleSessionSubagentHistoryFiltersBySession(t *testing.T) {
	s := &Server{Home: t.TempDir()}
	// The ledger belongs to the active primary agent, so it is written where
	// the server resolves it rather than at the shared home.
	workspace := s.stateRoot()
	for _, entry := range []agent.HistoryEntry{
		{
			TaskID:      "task-1",
			RunID:       "child-1",
			ParentRunID: "parent-1",
			SessionID:   "session-1",
			Task:        "first",
			Status:      agent.StatusRunning,
			StartedAt:   10,
			UpdatedAt:   10,
		},
		{
			TaskID:      "task-1",
			RunID:       "child-1",
			ParentRunID: "parent-1",
			SessionID:   "session-1",
			Task:        "first",
			Status:      agent.StatusOK,
			Output:      "done",
			StartedAt:   10,
			UpdatedAt:   20,
			FinishedAt:  20,
		},
		{
			TaskID:      "task-2",
			RunID:       "child-2",
			ParentRunID: "parent-2",
			SessionID:   "session-2",
			Task:        "second",
			Status:      agent.StatusFailed,
			Error:       "boom",
			StartedAt:   11,
			UpdatedAt:   21,
			FinishedAt:  21,
		},
		{
			TaskID:      "task-3",
			RunID:       "child-3",
			ParentRunID: "parent-1",
			SessionID:   "session-1",
			Task:        "third",
			Status:      agent.StatusFailed,
			Error:       "bad",
			StartedAt:   15,
			UpdatedAt:   25,
			FinishedAt:  25,
		},
	} {
		if err := agent.AppendHistory(workspace, entry); err != nil {
			t.Fatalf("append history: %v", err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/chat/sessions/session-1/subagent-history", nil)
	req = req.WithContext(context.WithValue(req.Context(), ParamsKey, Params{
		{Key: "id", Value: "session-1"},
	}))
	rr := httptest.NewRecorder()

	s.handleSessionSubagentHistory(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		SessionID string               `json:"session_id"`
		Records   []agent.HistoryEntry `json:"records"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.SessionID != "session-1" {
		t.Fatalf("unexpected session id: %q", body.SessionID)
	}
	if len(body.Records) != 2 {
		t.Fatalf("expected 2 filtered records, got %d", len(body.Records))
	}
	if body.Records[0].TaskID != "task-3" || body.Records[1].TaskID != "task-1" {
		t.Fatalf("unexpected record ordering: %#v", body.Records)
	}
}

func TestHandleSessionSubagentHistoryUsesAppCore(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, agent.AppendHistory(workspace, agent.HistoryEntry{
		TaskID:      "task-1",
		RunID:       "child-1",
		ParentRunID: "parent-1",
		SessionID:   "session-1",
		Task:        "first",
		Status:      agent.StatusOK,
		Output:      "done",
		StartedAt:   10,
		UpdatedAt:   20,
		FinishedAt:  20,
	}))

	s := &Server{Core: turn.New(turn.WithWorkspaceRoot(workspace))}
	req := httptest.NewRequest(http.MethodGet, "/api/chat/sessions/session-1/subagent-history", nil)
	req = req.WithContext(context.WithValue(req.Context(), ParamsKey, Params{
		{Key: "id", Value: "session-1"},
	}))
	rr := httptest.NewRecorder()

	s.handleSessionSubagentHistory(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		SessionID string               `json:"session_id"`
		Records   []agent.HistoryEntry `json:"records"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	require.Equal(t, "session-1", body.SessionID)
	require.Len(t, body.Records, 1)
	require.Equal(t, "task-1", body.Records[0].TaskID)
}

func setRunnerToolsForTest(r *run.Runner, st *tool.State) {
	if r == nil {
		return
	}
	v := reflect.ValueOf(r).Elem().FieldByName("tools")
	if !v.IsValid() || !v.CanAddr() {
		return
	}
	reflect.NewAt(v.Type(), unsafe.Pointer(v.UnsafeAddr())).Elem().Set(reflect.ValueOf(st))
}

func TestRuntimeWarningPayloadIncludesYOLOFields(t *testing.T) {
	// YOLO is read from the environment, not carried on the Runner.
	t.Setenv(safety.EnvYOLO, "1")
	s := &Server{Runner: &run.Runner{Deps: &run.Deps{}}}
	msg, data, ok := s.runtimeDangerWarning()
	if !ok {
		t.Fatalf("expected yolo runtime warning")
	}
	if msg == "" {
		t.Fatalf("expected warning message")
	}
	if data["yolo"] != true || data["approval_bypassed_by_yolo"] != true {
		t.Fatalf("unexpected warning payload: %#v", data)
	}
	if data["sandbox_mode"] != "danger-full-access" {
		t.Fatalf("unexpected sandbox mode payload: %#v", data)
	}
}

// TestHandleModelsListSkipsNonChatModels pins the reference-data/chat-list
// split on the HTTP surface: models.json records image, speech, transcription,
// embedding and decision models for reference, and none of them may be offered
// as a chat model by /api/models.
func TestHandleModelsListSkipsNonChatModels(t *testing.T) {
	s := &Server{Core: turn.New()}
	req := httptest.NewRequest(http.MethodGet, "/api/models?provider=openai", nil)
	rr := httptest.NewRecorder()
	s.handleModelsList(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	var out struct {
		Records []turn.ModelRecord `json:"records"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
	require.NotEmpty(t, out.Records)
	ids := make(map[string]bool, len(out.Records))
	for _, rec := range out.Records {
		ids[rec.ModelID] = true
	}
	require.Contains(t, ids, "openai/gpt-5")
	require.NotContains(t, ids, "openai/whisper-large-v3", "a transcription model is not a chat model")
	require.NotContains(t, ids, "openai/gpt-image-1", "an image model is not a chat model")

	// The login-backed providers' reference rows are not a list either.
	rr = httptest.NewRecorder()
	s.handleModelsList(rr, httptest.NewRequest(http.MethodGet, "/api/models", nil))
	require.Equal(t, http.StatusOK, rr.Code)
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
	for _, rec := range out.Records {
		require.False(t, llm.LoginBackedProvider(rec.Provider), "%s came from a login-backed provider", rec.ModelID)
	}
}

// batchChannelGateway builds a gateway over one state database with a batch
// project whose channel is bound to a recording handler: the channel carries
// the project's tasks in and its notifications out, exactly as a real bot does.
func batchChannelGateway(t *testing.T) (*Server, state.Project, *batchNotifyChannel) {
	t.Helper()
	ctx := context.Background()
	home := t.TempDir()
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	cfg := &appcfg.Root{}
	s := &Server{
		Home:     home,
		RunRT:    &state.RunStore{DB: db},
		Env:      &process.Environment{SQL: db, Deps: run.Deps{AppCfg: cfg}},
		Sessions: state.NewSessionStore(db, "main"),
		Channels: channel.NewRegistry(),
	}
	s.Projects = state.NewProjectStore(db, "main")
	root := filepath.Join(home, "batch")
	require.NoError(t, os.MkdirAll(root, 0o755))
	project, err := s.Projects.Create(ctx, state.CreateProjectInput{
		Name: "batch", Root: root, ProjectKey: state.ProjectKeyForRoot(root), ProjectType: state.ProjectTypeBatch,
	})
	require.NoError(t, err)
	rec := &batchNotifyChannel{id: "wecom"}
	require.NoError(t, s.Channels.BindProjects(ctx, "main", []channel.BoundChannel{{ProjectID: project.ID, Handler: rec}}, channelBus{s: s}))
	t.Cleanup(func() { _ = s.Channels.Stop(ctx) })
	return s, project, rec
}

// batchNotifyChannel is a project channel that records what the gateway
// delivered and, when failWith is set, refuses it the way a dead bot does.
type batchNotifyChannel struct {
	id       string
	failWith error
	mu       sync.Mutex
	sent     []channel.Outbound
}

func (c *batchNotifyChannel) ID() string { return c.id }

func (c *batchNotifyChannel) Start(context.Context, channel.RouteAdder, channel.Bus) error {
	return nil
}

func (c *batchNotifyChannel) Stop(context.Context) error { return nil }

func (c *batchNotifyChannel) DeliverOutbound(_ context.Context, out channel.Outbound) error {
	c.mu.Lock()
	c.sent = append(c.sent, out)
	c.mu.Unlock()
	return c.failWith
}

func (c *batchNotifyChannel) delivered() []channel.Outbound {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]channel.Outbound(nil), c.sent...)
}

func (c *batchNotifyChannel) texts() []string {
	out := make([]string, 0, len(c.delivered()))
	for _, item := range c.delivered() {
		out = append(out, item.Text)
	}
	return out
}

// inboundBatch sends one message through the project's channel, the way the
// channel's own HTTP endpoint would.
func inboundBatch(t *testing.T, s *Server, projectID, sessionID, text string) {
	t.Helper()
	require.NoError(t, channelBus{s: s}.PublishInbound(context.Background(), channel.Inbound{
		ChannelID: "wecom", ProjectID: projectID, SessionID: sessionID, Text: text,
	}))
}

// A batch project's channel takes tasks, not a conversation: a message opens a
// task with its own execution and workspace, the chat is told the task was
// taken, and the turn runs unattended from there (plan 151).
func TestBatchChannelMessageStartsATask(t *testing.T) {
	ctx := context.Background()
	s, project, rec := batchChannelGateway(t)
	text := "把上个月的 CSV 汇总成 xlsx"
	inboundBatch(t, s, project.ID, "u1", text)

	var row state.TaskRow
	require.Eventually(t, func() bool {
		rows, _, err := s.Projects.ListTasksPage(ctx, project.ID, state.ListTasksOptions{}, 10, 0)
		if err != nil || len(rows) != 1 {
			return false
		}
		row = rows[0]
		return true
	}, 5*time.Second, 10*time.Millisecond)

	require.Equal(t, state.TaskOriginChannel, row.Origin)
	require.Equal(t, state.SessionTitleFromContent(text), row.Title)
	require.Equal(t, state.TaskStatusPending, row.Latest.Status)
	require.True(t, strings.HasPrefix(row.Latest.SessionID, "batch-"), "a message opens an execution of its own, got %q", row.Latest.SessionID)
	for _, dir := range []string{"inputs", "outputs", ".work"} {
		_, statErr := os.Stat(filepath.Join(row.Latest.Workspace, dir))
		require.NoError(t, statErr, "execution workspace lacks %s", dir)
	}
	// The task's input is fixed by the first turn, never written here: a
	// message the model never saw must not look like a fixed input.
	require.False(t, row.InputSet)
	require.Empty(t, row.InputText)
	// The execution's conversation is born in the batch project, so the
	// engine's admission sees it as a batch turn.
	projectType, err := s.Projects.SessionProjectType(ctx, row.Latest.SessionID)
	require.NoError(t, err)
	require.Equal(t, state.ProjectTypeBatch, projectType)

	// The ref names the chat the task came from, which is where its end
	// notification will go.
	ref, ok := parseChannelTaskRef(row.OriginRef)
	require.True(t, ok)
	require.Equal(t, "wecom", ref.Channel)
	require.Equal(t, state.WrapProjectChannelSession("wecom", project.ID, "u1"), ref.Session)

	// The chat is answered with the acknowledgement, in the chat it came from,
	// and nothing else: the result arrives as the end notification.
	require.Eventually(t, func() bool { return len(rec.delivered()) > 0 }, 5*time.Second, 10*time.Millisecond)
	delivered := rec.delivered()[0]
	require.Equal(t, "已接收任务："+row.Title+"（"+row.ID+"），完成后通知。", delivered.Text)
	require.Equal(t, "u1", delivered.SessionID, "the acknowledgement goes back to the sender, not the task's session")
}

// An ordinary project's channel keeps the conversation it always had: nothing
// about a batch project's door may open a task for it.
func TestChannelMessageOnACodingProjectOpensNoTask(t *testing.T) {
	ctx := context.Background()
	s, project, _ := batchChannelGateway(t)
	codingRoot := filepath.Join(t.TempDir(), "coding")
	require.NoError(t, os.MkdirAll(codingRoot, 0o755))
	coding, err := s.Projects.Create(ctx, state.CreateProjectInput{
		Name: "coding", Root: codingRoot, ProjectKey: state.ProjectKeyForRoot(codingRoot), ProjectType: state.ProjectTypeCoding,
	})
	require.NoError(t, err)

	inboundBatch(t, s, coding.ID, "u1", "帮我看一下这个仓库")

	rows, _, err := s.Projects.ListTasksPage(ctx, coding.ID, state.ListTasksOptions{}, 10, 0)
	require.NoError(t, err)
	require.Empty(t, rows, "a coding project's channel never opens tasks")
	rows, _, err = s.Projects.ListTasksPage(ctx, project.ID, state.ListTasksOptions{}, 10, 0)
	require.NoError(t, err)
	require.Empty(t, rows, "the batch project opened nothing for another project's message")
}

// A built-in slash command steers a conversation, and a batch project has no
// conversation to steer: the chat is told so and no task is opened.
func TestBatchChannelRefusesBuiltinCommands(t *testing.T) {
	ctx := context.Background()
	s, project, rec := batchChannelGateway(t)
	inboundBatch(t, s, project.ID, "u1", "/plan 换个做法")

	require.Eventually(t, func() bool { return len(rec.delivered()) == 1 }, 5*time.Second, 10*time.Millisecond)
	require.Equal(t, []string{channelTaskCommandRefusal}, rec.texts())
	rows, _, err := s.Projects.ListTasksPage(ctx, project.ID, state.ListTasksOptions{}, 10, 0)
	require.NoError(t, err)
	require.Empty(t, rows)
}

// batchNotifyEnd is one settled execution, as the engine reports it.
func batchNotifyEnd(task state.Task, exec state.TaskExecution, status string) turn.BatchExecutionEnd {
	exec.Status = status
	return turn.BatchExecutionEnd{
		SessionID: exec.SessionID, RunID: "run-1", Task: task, Execution: exec,
		Status: status, ErrorCode: exec.ErrorCode, ErrorText: exec.ErrorText,
	}
}

// A task that came in through a channel is answered in that same chat, with
// the template's status line, task identity and deliverables — and a link only
// when the gateway knows the address people reach it at (plan 151).
func TestBatchEndNotifiesTheOriginChat(t *testing.T) {
	ctx := context.Background()
	s, project, rec := batchChannelGateway(t)
	wrapped := state.WrapProjectChannelSession("wecom", project.ID, "u1")
	now := time.Now().Unix()
	task := state.Task{ID: "tsk-abc", ProjectID: project.ID, Title: "汇总上个月数据",
		Origin: state.TaskOriginChannel, OriginRef: newChannelTaskRef("wecom", wrapped)}
	exec := state.TaskExecution{SessionID: "batch-1", TaskID: task.ID, Seq: 2, Status: state.TaskStatusCompleted,
		StartedAt: now - 83, FinishedAt: now}
	end := batchNotifyEnd(task, exec, state.TaskStatusCompleted)
	end.Artifacts = []state.Artifact{{Name: "汇总.xlsx", Size: 2048}, {Name: "明细.csv", Size: 512}}

	s.notifyBatchExecutionEnd(ctx, end)
	require.Eventually(t, func() bool { return len(rec.delivered()) == 1 }, 5*time.Second, 10*time.Millisecond)
	text := rec.texts()[0]
	require.Contains(t, text, "【任务完成】汇总上个月数据")
	require.Contains(t, text, "任务 tsk-abc · 第 2 次执行 · 用时 1m23s")
	require.Contains(t, text, "汇总.xlsx（2.0 KB）")
	require.Contains(t, text, "明细.csv（512 B）")
	require.NotContains(t, text, "查看：", "no public URL, no link")
	require.Equal(t, "u1", rec.delivered()[0].SessionID, "the notification goes back to the chat the task came from")

	// With a public URL configured the notification points at the task.
	s.Env.Deps.AppCfg.Gateway.PublicURL = "https://forebrain.example.com/"
	s.notifyBatchExecutionEnd(ctx, end)
	require.Eventually(t, func() bool { return len(rec.delivered()) == 2 }, 5*time.Second, 10*time.Millisecond)
	require.Contains(t, rec.texts()[1], "查看：https://forebrain.example.com/projects/"+project.ID+"/tasks/tsk-abc")
}

// A task that did not come in through a channel goes to the project's own
// notification targets. One dead target does not silence the other, and the
// delivery that failed is recorded on the execution — without touching what
// the task itself ended as (plan 151).
func TestBatchEndNotifiesProjectTargets(t *testing.T) {
	ctx := context.Background()
	s, project, rec := batchChannelGateway(t)
	targets := []state.TaskNotifyTarget{{Channel: "wecom", Target: "ops-room"}, {Channel: "feishu", Target: "oc_dead"}}
	_, err := s.Projects.Update(ctx, project.ID, state.UpdateProjectInput{TaskNotify: &targets})
	require.NoError(t, err)
	dead := &batchNotifyChannel{id: "feishu", failWith: errors.New("feishu: chat not found")}
	require.NoError(t, s.Channels.BindProjects(ctx, "main", []channel.BoundChannel{
		{ProjectID: project.ID, Handler: rec},
		{ProjectID: project.ID, Handler: dead},
	}, channelBus{s: s}))

	task, err := s.Projects.CreateTask(ctx, state.Task{ProjectID: project.ID, Title: "月度对账", Origin: state.TaskOriginAPI})
	require.NoError(t, err)
	require.NoError(t, s.Sessions.Ensure(ctx, "batch-1", "batch-1"))
	exec, err := s.Projects.AddTaskExecution(ctx, task.ID, "batch-1", project.Root)
	require.NoError(t, err)
	require.NoError(t, s.Projects.MarkExecutionEnded(ctx, exec.SessionID, state.TaskStatusCompleted, "", ""))
	exec, task, ok, err := s.Projects.ExecutionForSession(ctx, exec.SessionID)
	require.NoError(t, err)
	require.True(t, ok)

	s.notifyBatchExecutionEnd(ctx, batchNotifyEnd(task, exec, state.TaskStatusCompleted))

	require.Eventually(t, func() bool { return len(rec.delivered()) == 1 && len(dead.delivered()) == 1 }, 5*time.Second, 10*time.Millisecond)
	require.Contains(t, rec.texts()[0], "【任务完成】月度对账")
	require.Equal(t, "ops-room", rec.delivered()[0].SessionID)
	require.Equal(t, project.ID, rec.delivered()[0].ProjectID, "a project target is delivered through that project's own channel")
	require.Eventually(t, func() bool {
		text, readErr := s.Projects.ExecutionNotifyError(ctx, exec.SessionID)
		return readErr == nil && strings.Contains(text, "feishu") && strings.Contains(text, "chat not found")
	}, 5*time.Second, 10*time.Millisecond)
	// The task's own ending is untouched by a delivery that failed.
	after, _, ok, err := s.Projects.ExecutionForSession(ctx, exec.SessionID)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, state.TaskStatusCompleted, after.Status)
	require.Empty(t, after.ErrorCode)
}

// A stopped execution and a failed one say so, and a failure explains itself
// with the code a client switches on plus what the failure said (plan 151).
func TestBatchStoppedAndFailedWording(t *testing.T) {
	ctx := context.Background()
	s, project, rec := batchChannelGateway(t)
	wrapped := state.WrapProjectChannelSession("wecom", project.ID, "u1")
	task := state.Task{ID: "tsk-abc", ProjectID: project.ID, Title: "汇总上个月数据",
		Origin: state.TaskOriginChannel, OriginRef: newChannelTaskRef("wecom", wrapped)}
	now := time.Now().Unix()
	exec := state.TaskExecution{SessionID: "batch-1", TaskID: task.ID, Seq: 1, StartedAt: now - 3, FinishedAt: now}

	s.notifyBatchExecutionEnd(ctx, batchNotifyEnd(task, exec, state.TaskStatusStopped))
	require.Eventually(t, func() bool { return len(rec.delivered()) == 1 }, 5*time.Second, 10*time.Millisecond)
	require.Contains(t, rec.texts()[0], "【任务已停止】汇总上个月数据")
	require.NotContains(t, rec.texts()[0], "原因：")

	failed := exec
	failed.ErrorCode = "provider_timeout"
	failed.ErrorText = "upstream model did not answer within the request timeout"
	s.notifyBatchExecutionEnd(ctx, batchNotifyEnd(task, failed, state.TaskStatusFailed))
	require.Eventually(t, func() bool { return len(rec.delivered()) == 2 }, 5*time.Second, 10*time.Millisecond)
	require.Contains(t, rec.texts()[1], "【任务失败】汇总上个月数据")
	require.Contains(t, rec.texts()[1], "原因：provider_timeout：upstream model did not answer within the request timeout")
}
