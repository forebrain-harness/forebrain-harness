package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
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
// webhook channel on their own inbound path.
func twoAgentChannelServer(t *testing.T, home string) *Server {
	t.Helper()
	cfg := &appcfg.Root{}
	cfg.Agents.Definitions = map[string]appcfg.AgentDefinition{
		"main": {Channels: appcfg.ChannelsSection{
			Webhook: appcfg.Webhook{Enabled: true, InboundPath: "/main/hook"},
		}},
		"acme": {Primary: true, Channels: appcfg.ChannelsSection{
			Webhook: appcfg.Webhook{Enabled: true, InboundPath: "/acme/hook"},
		}},
	}
	require.NoError(t, os.MkdirAll(filepath.Join(home, "state"), 0o755))
	return &Server{
		Home:     home,
		Channels: channel.NewRegistry(),
		Env:      &process.Environment{Deps: run.Deps{AppCfg: cfg}},
	}
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

	require.NoError(t, s.applyPrimaryAgent(summaryFor(home, "acme")))

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
	cfg := &appcfg.Root{}
	cfg.Agents.Definitions = map[string]appcfg.AgentDefinition{
		"main": {Channels: appcfg.ChannelsSection{
			Webhook: appcfg.Webhook{Enabled: true, InboundPath: "/main/hook"},
		}},
		"acme": {Primary: true},
	}
	require.True(t, appcfg.GatewayControlPlaneAuthExemptPath("/main/hook", cfg, "main"))
	require.False(t, appcfg.GatewayControlPlaneAuthExemptPath("/main/hook", cfg, "acme"))
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

func TestGatewayWorkspaceAPIsUseActivePrimaryWorkspace(t *testing.T) {
	home := t.TempDir()
	mainWS := filepath.Join(home, "workspace")
	reviewWS := filepath.Join(home, "workspaces", "review")
	require.NoError(t, os.MkdirAll(mainWS, 0o755))
	require.NoError(t, os.MkdirAll(reviewWS, 0o755))
	// One marker file per workspace plus a nested file only in the review
	// workspace: the tree must read the active agent's workspace, and only one
	// level of it.
	require.NoError(t, os.WriteFile(filepath.Join(mainWS, "main-only.md"), []byte("main only"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(reviewWS, "review-only.md"), []byte("review only"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(reviewWS, "docs"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(reviewWS, "docs", "a.md"), []byte("nested"), 0o644))
	s := newActiveReviewGatewayServer(t, home)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/workspace/tree", nil)
	s.handleWorkspaceTree(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Contains(t, rr.Body.String(), `"path":"review-only.md"`)
	require.Contains(t, rr.Body.String(), `"name":"docs"`)
	require.NotContains(t, rr.Body.String(), "main-only.md")
	require.NotContains(t, rr.Body.String(), "docs/a.md")

	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/workspace/tree?path=docs", nil)
	s.handleWorkspaceTree(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Contains(t, rr.Body.String(), `"path":"docs/a.md"`)

	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/workspace/snippet?path=review-only.md", nil)
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
	if cat, err := llm.Parse(llm.EmbeddedModelsJSON()); err == nil {
		llm.SetGlobalCatalogForTest(cat)
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
		require.NotEmpty(t, rec.ModelName)
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

// Plan files live in <stateRoot>/plans/<projectKey>, and the plan-mode
// reminder tells the model that exact directory. A gateway context wired with a
// different key (it used to hardcode the project-less one) permits writes to
// another directory, so every plan write the model attempts is rejected.
func TestGatewayPlanContextMatchesAdvertisedPlanDir(t *testing.T) {
	home := t.TempDir()
	sid := "sid-gateway-plan"
	s := &Server{Home: home, Runner: &run.Runner{Deps: &run.Deps{Home: home, ProjectKey: "-Users-ada-work-forebrain"}}}

	if got := s.projectKey(); got != "-Users-ada-work-forebrain" {
		t.Fatalf("projectKey()=%q, want the runner's key", got)
	}

	stateRoot := s.stateRoot()
	if _, err := state.Switch(stateRoot, sid, state.ModePlan); err != nil {
		t.Fatalf("enter plan mode: %v", err)
	}

	ctx := process.AgentContextForProject(context.Background(), stateRoot, sid, s.projectKey())
	want := state.PlanDirForProject(stateRoot, s.projectKey())
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
