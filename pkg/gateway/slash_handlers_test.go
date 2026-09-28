package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/require"
)

// The gateway reports permissions but never changes them: the preset picker is
// a terminal surface, and every rule-editing subcommand was removed with it.
func TestHandlePermissionsSlashReportsButDoesNotMutate(t *testing.T) {
	home := t.TempDir()
	s := &Server{
		Home:   home,
		Runner: &run.Runner{Deps: &run.Deps{Home: home}},
	}

	statusReply, handled := s.HandlePermissionsSlash("s1", "webchat", nil)
	require.True(t, handled)
	require.Contains(t, statusReply, "Forebrain Harness asks before going further")
	require.Contains(t, statusReply, "No allow, deny or ask rules refine it.")

	for _, args := range [][]string{
		{"mode", "status"},
		{"mode", "never", "session"},
		{"allow", "Bash", "go test ./...", "session"},
		{"audit"},
	} {
		reply, handled := s.HandlePermissionsSlash("s1", "webchat", args)
		require.True(t, handled, args)
		require.Contains(t, reply, "/permissions explain <tool> [input]", args)
	}
	require.Equal(t, safety.ModeOnRequest, s.Runner.PermissionSnapshotForSession("s1").Mode)
}

func TestHandleContextSlashStatsUsesRunnerStateRoot(t *testing.T) {
	root := t.TempDir()
	tool.ResetCompressors()
	t.Cleanup(tool.ResetCompressors)

	store, err := tool.OpenStore(filepath.Join(root, "state", "outfilter", "history.db"))
	require.NoError(t, err)
	_, err = store.Save(tool.Entry{
		Kind:          tool.KindMCP,
		OriginalBytes: 8192,
		FilteredBytes: 2048,
		SavedTokens:   200,
		CapabilityID:  tool.MCPCapabilityTOON,
	})
	require.NoError(t, err)
	require.NoError(t, store.Close())

	s := &Server{Runner: &run.Runner{Deps: &run.Deps{WorkspaceRoot: root}}}
	reply, handled := s.HandleContextSlash(context.Background(), "s1", "webchat", nil)
	require.True(t, handled)
	require.Contains(t, reply, "Filtered tool output, across this agent's conversations")
	require.Contains(t, reply, "MCP tools · 1 of 1 trimmed · about 200 tokens saved")
}

func TestHandlePermissionsSlashGatewayExplain(t *testing.T) {
	home := t.TempDir()
	s := &Server{
		Home:   home,
		Runner: &run.Runner{Deps: &run.Deps{Home: home}},
	}
	s.Runner.ApplyPermissionUpdate(safety.PermissionUpdate{
		Type:        safety.UpdateAddRules,
		Destination: safety.DestinationSession,
		SessionID:   "s1",
		Behavior:    safety.BehaviorAllow,
		Rules:       []safety.PermissionRuleValue{{ToolName: "Bash", RuleContent: "npm run:*"}},
	})

	reply, handled := s.HandlePermissionsSlash("s1", "webchat", []string{"explain", "Bash", "npm run build"})
	require.True(t, handled)
	require.Contains(t, reply, "Permissions: Bash")
	require.Contains(t, reply, "Allowed — runs without asking.")
	require.Contains(t, reply, "allow npm run:*  (this session)")

	usage, handled := s.HandlePermissionsSlash("s1", "webchat", []string{"explain"})
	require.True(t, handled)
	require.Contains(t, usage, "/permissions explain")
}

func TestHandleMCPSlashSummary(t *testing.T) {
	cfg := &appcfg.Root{
		Agents: appcfg.AgentsSection{
			Defaults: appcfg.AgentDefaults{
				MCPServers: []appcfg.MCPServerConfig{{
					Name:      "github",
					Transport: "streamable_http",
					URL:       "https://example.com/mcp",
				}},
			},
		},
	}
	// The session's frozen list is what /mcp reports; the config file's list
	// is not read in its place.
	s := &Server{
		Home:   t.TempDir(),
		Runner: &run.Runner{Deps: &run.Deps{MCPServers: cfg.Agents.Defaults.MCPServers}},
		Env:    &process.Environment{Deps: run.Deps{AppCfg: cfg}},
	}

	reply, handled := s.HandleMCPSlash("s1", "webchat")
	require.True(t, handled)
	require.Contains(t, reply, "MCP servers · 1 server")
	require.Contains(t, reply, "github")
	require.Contains(t, reply, "streamable_http")

	empty := &Server{
		Home:   t.TempDir(),
		Runner: &run.Runner{Deps: &run.Deps{}},
		Env:    &process.Environment{Deps: run.Deps{AppCfg: cfg}},
	}
	reply, _ = empty.HandleMCPSlash("s1", "webchat")
	require.Contains(t, reply, "No MCP servers configured")
}

func TestHandleMCPServersV1MarksOfficialRegistryURLs(t *testing.T) {
	t.Setenv("FOREBRAIN_MCP_OFFICIAL_REGISTRY_JSON", `{
		"servers": [
			{"server": {"remotes": [{"url": "https://example.com/mcp?token=redacted"}]}}
		]
	}`)
	cfg := &appcfg.Root{
		Agents: appcfg.AgentsSection{
			Defaults: appcfg.AgentDefaults{
				MCPServers: []appcfg.MCPServerConfig{{
					Name:      "docs",
					Transport: "streamable_http",
					URL:       "https://example.com/mcp",
				}},
			},
		},
	}
	s := &Server{
		Home: t.TempDir(),
		Env:  &process.Environment{Deps: run.Deps{AppCfg: cfg}},
	}
	rec := httptest.NewRecorder()

	s.handleMCPServersV1(rec, httptest.NewRequest(http.MethodGet, "/api/v1/mcp/servers", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Servers []map[string]any `json:"servers"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Servers, 1)
	require.Equal(t, true, body.Servers[0]["official_url"])
}

// /diff shows the diff of the project the session works in, the directory
// /status names — not the agent's home workspace.
func TestHandleDiffSlashShowsGitDiff(t *testing.T) {
	home := t.TempDir()
	ws := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(home, "workspace"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(ws, "docs"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(ws, "docs", "plan.md"), []byte("old\n"), 0o600))
	runInDirGateway(t, ws, "git", "init")
	runInDirGateway(t, ws, "git", "config", "user.name", "Test")
	runInDirGateway(t, ws, "git", "config", "user.email", "test@example.com")
	runInDirGateway(t, ws, "git", "add", ".")
	runInDirGateway(t, ws, "git", "commit", "-m", "init")
	require.NoError(t, os.WriteFile(filepath.Join(ws, "docs", "plan.md"), []byte("slash diff\n"), 0o600))

	s := &Server{
		Home:   home,
		Runner: &run.Runner{Deps: &run.Deps{Home: home, ProjectRoot: ws}},
	}
	reply, handled := s.HandleDiffSlash("s1", "webchat", nil)
	require.True(t, handled)
	require.Contains(t, reply, "diff --git")
	require.Contains(t, reply, "docs/plan.md")
	require.Contains(t, reply, "+slash diff")
	require.Contains(t, reply, "-old")
}

func runInDirGateway(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
}

// /model on the web offers the agent's configured models as a picker, the one
// in force marked, and a pick is written to the config and taken live — the
// same engine choice the terminal makes.
func TestWebModelPickerSwitchesTheModel(t *testing.T) {
	home := t.TempDir()
	cfgPath := filepath.Join(home, "forebrain.yaml")
	t.Setenv("FOREBRAIN_WEB_MODEL_TEST_KEY", "test-key")
	cfg := appcfg.Root{Agents: appcfg.AgentsSection{Definitions: map[string]appcfg.AgentDefinition{
		"main": {LLMProviders: []appcfg.AgentLLMProviderConfig{
			{Provider: "openai", Model: "gpt-4o", APIKey: "${FOREBRAIN_WEB_MODEL_TEST_KEY}", BaseURL: "http://a.test/v1"},
			{Provider: "deepseek", Model: "deepseek-chat", APIKey: "${FOREBRAIN_WEB_MODEL_TEST_KEY}", BaseURL: "http://b.test/v1"},
		}},
	}}}
	require.NoError(t, appcfg.Save(cfgPath, cfg))
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(home, "state.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	sess := state.NewSessionStore(db, "main")
	require.NoError(t, sess.Ensure(context.Background(), "s1", "s1"))
	env := &process.Environment{Root: home, ConfigPath: cfgPath, Deps: run.Deps{Home: home, AgentName: "main", AppCfg: &cfg, SessionStore: sess}}
	runner := &run.Runner{Deps: &env.Deps}
	env.Runner = runner
	s := &Server{
		Home:     home,
		Runner:   runner,
		Sessions: sess,
		Env:      env,
	}
	require.NoError(t, s.Env.ReloadConfig())

	res := parseSlashCommandWithOptions(s, "s1", "webchat", "/model", turn.Context{}, nil)
	require.True(t, res.Handled)
	require.NotNil(t, res.Picker, res.Reply)
	require.Equal(t, "model", res.Picker.Command)
	require.Len(t, res.Picker.Items, 2)
	require.True(t, res.Picker.Items[0].Current)

	chosen := parseSlashCommandWithOptions(s, "s1", "webchat", "/model", turn.Context{}, &turn.SlashChoice{Command: "model", Value: res.Picker.Items[1].Value})
	require.Equal(t, "Switched to deepseek / deepseek-chat.", chosen.Reply)
	saved, err := appcfg.LoadPersisted(cfgPath)
	require.NoError(t, err)
	require.Equal(t, "deepseek-chat", saved.Agents.Definitions["main"].LLMProviders[0].Model)
	// The pick is the session's own durable selection; the runner's published
	// default is what the next session without a row starts from.
	row, hasRow, rowErr := sess.SessionModelSelection(context.Background(), "s1")
	require.NoError(t, rowErr)
	require.True(t, hasRow)
	require.Equal(t, "deepseek-chat", row.Model)
}

func TestGatewaySlashHelpersOnProductPaths(t *testing.T) {
	require.Contains(t, gatewayPermissionsUsage(), "/permissions")
}

func TestGatewayStatusAndModelHelpers(t *testing.T) {
	cfg := &appcfg.Root{}
	cfg.Agents.Definitions = map[string]appcfg.AgentDefinition{
		"main": {
			LLMProviders: []appcfg.AgentLLMProviderConfig{{Provider: "openai", Model: "main-model"}},
		},
		"worker": {
			LLMProviders: []appcfg.AgentLLMProviderConfig{{Provider: "openai", Model: "worker-model"}},
		},
	}
	cfg.Agents.Defaults.MCPServers = []appcfg.MCPServerConfig{{Name: "docs"}, {Name: "code"}}
	s := &Server{
		Home:   t.TempDir(),
		Runner: &run.Runner{Deps: &run.Deps{AppCfg: cfg, AgentName: "worker"}},
		Env:    &process.Environment{Deps: run.Deps{AppCfg: cfg}},
	}

	// The active-agent model resolution lives in run.PrimaryModel now; it is
	// asserted against the worker agent, then against a missing Runner.
	provider, model := run.PrimaryModel(s.Runner)
	require.Equal(t, "openai", provider)
	require.Equal(t, "worker-model", model)
	provider, model = run.PrimaryModel(nil)
	require.Empty(t, provider)
	require.Empty(t, model)

}

func TestHandleStatusSlashUsesRuntimeStores(t *testing.T) {
	home := t.TempDir()
	// Main agent state lives under its workspace root, not raw home.
	stateRoot := filepath.Join(home, "workspace")
	require.NoError(t, state.Set(stateRoot, "s1", state.State{Mode: state.ModePlan, Phase: "draft"}))
	require.NoError(t, state.SetPlanForProject(stateRoot, "", "1. do work"))
	require.NoError(t, state.Save(stateRoot, "s1", state.List{Items: []state.Item{
		{ID: "1", Content: "todo", Status: state.StatusPending},
	}}))
	cfg := &appcfg.Root{}
	cfg.Agents.Definitions = map[string]appcfg.AgentDefinition{
		"main": {LLMProviders: []appcfg.AgentLLMProviderConfig{{Provider: "openai", Model: "gpt-main"}}},
	}
	s := &Server{
		Home:   home,
		Runner: &run.Runner{Deps: &run.Deps{AppCfg: cfg, AgentName: "main"}},
		Env:    &process.Environment{Deps: run.Deps{AppCfg: cfg}},
	}
	s.Runner.ApplyPermissionUpdate(safety.PermissionUpdate{
		Type:        safety.UpdateSetMode,
		Destination: safety.DestinationSession,
		SessionID:   "s1",
		Mode:        safety.ModeNever,
	})

	reply, handled := s.HandleStatusSlash("s1", "webchat", false)
	require.True(t, handled)
	require.Contains(t, reply, "s1")
	require.Contains(t, reply, "plan")
	require.Contains(t, reply, "draft")
	require.Contains(t, reply, "gpt-main")
	require.Contains(t, reply, "never")
	require.NotContains(t, reply, "side conversation")

	reply, handled = s.HandleStatusSlash("s1", "webchat", true)
	require.True(t, handled)
	require.Contains(t, reply, "side conversation")
}

// TestHandleStatusSlashReportsUnavailableInsteadOfPanicking pins a real
// regression: every other handler in this file guards s.liveCfg() before
// dereferencing it (HandleMCPSlash, HandleSandboxSlash, ...), but
// HandleStatusSlash read s.liveCfg().Agents.Defaults.MCPServers unguarded.
// liveCfg returns nil whenever Env is nil -- a real, reachable state
// before the environment is attached, not a should-never-happen invariant --
// so /status crashed the whole process at exactly the moment a caller most
// wants a status reply instead of a panic.
func TestHandleStatusSlashReportsUnavailableInsteadOfPanicking(t *testing.T) {
	s := &Server{Home: t.TempDir()}

	reply, handled := s.HandleStatusSlash("s1", "webchat", false)
	require.True(t, handled)
	require.Contains(t, reply, "unavailable")
}

func TestHandleSandboxSlashStatus(t *testing.T) {
	cfg := &appcfg.Root{}
	cfg.SandboxMode = appcfg.SandboxModeWorkspaceWrite
	cfg.SandboxWorkspaceWrite.WritableRoots = []string{"/tmp/allowed"}
	cfg.SandboxWorkspaceWrite.NetworkAccess = appcfg.BoolPtr(true)
	s := &Server{
		Home:   t.TempDir(),
		Runner: &run.Runner{Deps: &run.Deps{}},
		Env:    &process.Environment{Deps: run.Deps{AppCfg: cfg}},
	}
	reply, handled := s.HandleSandboxSlash("s1", "webchat", nil)
	require.True(t, handled)
	require.Contains(t, reply, "Files: commands can read files and change them only in the workspace, $TMPDIR, /tmp, /tmp/allowed.")
	require.Contains(t, reply, "Network: commands can reach the internet.")
	require.Contains(t, reply, "Enforcement: ")
	require.NotContains(t, reply, "_")
}

// This surface reports the sandbox and never sets it — it has no preset picker,
// so setting the sandbox here would move one half of the decision with no way
// to move the other. Durable choices are declared in forebrain.yaml.
func TestHandleSandboxSlashDoesNotChangeMode(t *testing.T) {
	home := t.TempDir()
	cfgPath := filepath.Join(home, "forebrain.yaml")
	body := "sandbox_mode: read-only\n"
	require.NoError(t, os.WriteFile(cfgPath, []byte(body), 0o600))
	s := &Server{
		Home:   home,
		Runner: &run.Runner{Deps: &run.Deps{}},
		Env: &process.Environment{
			Root:       home,
			ConfigPath: cfgPath,
		},
	}
	require.NoError(t, s.Env.ReloadConfig())

	for _, mode := range []string{"workspace-write", "danger-full-access", "read-only"} {
		reply, handled := s.HandleSandboxSlash("s1", "webchat", []string{mode})
		require.True(t, handled)
		require.NotContains(t, reply, "sandbox: updated", mode)
		require.Contains(t, reply, "Files: commands can read files but not change them.", mode)
	}

	status, handled := s.HandleSandboxSlash("s1", "webchat", nil)
	require.True(t, handled)
	require.Contains(t, status, "Files: commands can read files but not change them.")
	require.Contains(t, status, "Enforcement: ", "the report carries the diagnostics too")
	onDisk, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	require.Equal(t, body, string(onDisk), "/sandbox must not write the config")
}

// The diagnostics are part of the single report; there is no `doctor`
// subcommand to reach them with.
func TestSandboxReportShowsUnavailableReason(t *testing.T) {
	t.Setenv("PATH", "")
	cfg := &appcfg.Root{}
	cfg.SandboxMode = appcfg.SandboxModeWorkspaceWrite
	s := &Server{
		Home:   t.TempDir(),
		Runner: &run.Runner{Deps: &run.Deps{}},
		Env:    &process.Environment{Deps: run.Deps{AppCfg: cfg}},
	}
	reply, handled := s.HandleSandboxSlash("s1", "webchat", nil)
	require.True(t, handled)
	require.Contains(t, reply, "Enforcement: nothing can enforce it here (")
	require.Contains(t, reply, "is not ")
	require.Contains(t, reply, "so commands that need it are refused.")
}

// TestMCPServerToggleSharesTheTerminalsAction pins the web's Disable/Enable:
// it records the choice in the serving Runner's agent store exactly as the
// terminal panel does, refuses a required server with its own sentence, and
// the servers list then shows a server the store kept out of the session as
// not running, so it can be re-enabled from the web too.
func TestMCPServerToggleSharesTheTerminalsAction(t *testing.T) {
	ws := t.TempDir()
	running := []appcfg.MCPServerConfig{{Name: "docs", Transport: "stdio", Command: "docs"}, {Name: "must", Command: "must", Required: true}}
	kept := []appcfg.MCPServerConfig{{Name: "paused", Transport: "stdio", Command: "paused"}}
	cfg := &appcfg.Root{}
	s := &Server{
		Home:   t.TempDir(),
		Runner: &run.Runner{Deps: &run.Deps{WorkspaceRoot: ws, MCPServers: running, MCPDisabled: kept}},
		Env:    &process.Environment{Deps: run.Deps{AppCfg: cfg}},
	}
	post := func(path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		if strings.HasSuffix(path, "disable") {
			s.handleMCPServerDisableV1(rec, req)
		} else {
			s.handleMCPServerEnableV1(rec, req)
		}
		return rec
	}
	rec := post("/api/v1/mcp/servers/disable", `{"name":"docs"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "docs: disabled from next session")
	rec = post("/api/v1/mcp/servers/disable", `{"name":"must"}`)
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Contains(t, rec.Body.String(), "required by config")
	rec = post("/api/v1/mcp/servers/enable", `{"name":"paused"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	list := httptest.NewRecorder()
	s.handleMCPServersV1(list, httptest.NewRequest(http.MethodGet, "/api/v1/mcp/servers", nil))
	require.Equal(t, http.StatusOK, list.Code)
	var body struct {
		Servers []map[string]any `json:"servers"`
	}
	require.NoError(t, json.Unmarshal(list.Body.Bytes(), &body))
	byName := map[string]map[string]any{}
	for _, row := range body.Servers {
		byName[row["name"].(string)] = row
	}
	require.Equal(t, true, byName["docs"]["running"])
	require.Equal(t, true, byName["docs"]["disabled_next_session"])
	require.Equal(t, false, byName["paused"]["running"])
	require.Equal(t, false, byName["paused"]["disabled_next_session"])
	require.Equal(t, true, byName["must"]["required"])
}

// /clear works on the web the way it does in the terminal: the conversation
// stays for reading, and nothing before it is sent to the model again.
func TestWebClearStartsTheModelContextOver(t *testing.T) {
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	sessions := state.NewSessionStore(db, "main")
	for _, text := range []string{"first", "second"} {
		if _, err := sessions.Append(ctx, "s1", "user", text); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{Sessions: sessions}

	res := turn.Execute(turn.Context{Surface: turn.SurfaceWebChat, SessionID: "s1", Sessions: sessions, Clear: s}, "/clear")
	require.True(t, res.Handled)
	require.Equal(t, turn.ContextClearedReply, res.Reply)

	sent, err := sessions.ListTranscriptMessages(ctx, "s1", 0)
	require.NoError(t, err)
	require.Empty(t, sent)
	var kept int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM fb_messages WHERE session_id='s1'`).Scan(&kept))
	require.Equal(t, 2, kept)
}

// gatewayModelEnv builds a Server whose base runner is loaded over a config
// with two reasoning-capable providers under main.
func gatewayModelServer(t *testing.T) *Server {
	t.Helper()
	t.Setenv("FOREBRAIN_GATEWAY_MODEL_TEST_KEY", "test-key")
	home := t.TempDir()
	key := "${FOREBRAIN_GATEWAY_MODEL_TEST_KEY}"
	cfg := &appcfg.Root{Agents: appcfg.AgentsSection{Definitions: map[string]appcfg.AgentDefinition{
		"main": {Primary: true, LLMProviders: []appcfg.AgentLLMProviderConfig{
			{Provider: "openai", Model: "gpt-a", APIKey: key, BaseURL: "http://a.test/v1"},
			{Provider: "openai", Model: "gpt-b", APIKey: key, BaseURL: "http://b.test/v1",
				Params: appcfg.LLMRequestParams(`{"reasoning":{"effort":"high"}}`)},
		}},
	}}}
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(home, "state.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	sess := state.NewSessionStore(db, "main")
	runner := &run.Runner{Deps: &run.Deps{Home: home, AgentName: "main", AppCfg: cfg, SessionStore: sess}}
	require.NoError(t, runner.Load())
	cfgPath := filepath.Join(home, "forebrain.yaml")
	require.NoError(t, appcfg.Save(cfgPath, *cfg))
	return &Server{
		Home:     home,
		Runner:   runner,
		Sessions: sess,
		Env: &process.Environment{
			Root:       home,
			ConfigPath: cfgPath,
			Deps:       run.Deps{AppCfg: cfg},
			Runner:     runner,
		},
	}
}

func requireGatewaySelection(t *testing.T, s *Server, sessionID, provider, model, effort string) {
	t.Helper()
	settings, err := s.ModelSettings(sessionID)
	require.NoError(t, err)
	require.True(t, settings.Selection.Set)
	require.Equal(t, provider, settings.Selection.Provider)
	require.Equal(t, model, settings.Selection.Model)
	require.Equal(t, effort, settings.Selection.Effort)
}

// TestGatewayModelSettingsReadsSessionSelection pins the marker's source:
// the session's own stored row, resolved against the runner's live config —
// never config order alone.
func TestGatewayModelSettingsReadsSessionSelection(t *testing.T) {
	s := gatewayModelServer(t)
	require.NoError(t, s.Sessions.Ensure(context.Background(), "s1", "s1"))
	requireGatewaySelection(t, s, "s1", "openai", "gpt-a", "")

	// nil effort derives the entry's configured effort once.
	require.NoError(t, s.SelectModel(context.Background(), "s1", turn.ModelChoice{Provider: "openai", Model: "gpt-b"}, nil))
	requireGatewaySelection(t, s, "s1", "openai", "gpt-b", "high")
	// A session with no row keeps reading the runner's published selection.
	requireGatewaySelection(t, s, "s2", "openai", "gpt-a", "")
	// The catalog still comes from the live config.
	settings, err := s.ModelSettings("s1")
	require.NoError(t, err)
	require.Len(t, settings.Config.Agents.Definitions["main"].LLMProviders, 2)
}

func TestGatewayModelSettingsWithoutConfiguration(t *testing.T) {
	s := &Server{Runner: &run.Runner{Deps: &run.Deps{}}}
	_, err := s.ModelSettings("s1")
	require.Error(t, err, "no configuration is bound")
}

func TestGatewaySelectModelNeedsARunner(t *testing.T) {
	s := &Server{Env: &process.Environment{Deps: run.Deps{}}}
	err := s.SelectModel(context.Background(), "s1", turn.ModelChoice{Provider: "openai", Model: "gpt-a"}, nil)
	require.Error(t, err)
}

// TestGatewayModelSelectionIsPerSession pins the fix for the runner-level
// boundary this test used to document: two web sessions sharing one runner
// keep independent selections. SelectModel writes only the session's own row;
// the runner's published selection — the process default — never moves.
func TestGatewayModelSelectionIsPerSession(t *testing.T) {
	s := gatewayModelServer(t)
	ctx := context.Background()
	require.NoError(t, s.Sessions.Ensure(ctx, "s1", "s1"))
	require.NoError(t, s.Sessions.Ensure(ctx, "s2", "s2"))

	require.NoError(t, s.SelectModel(ctx, "s1", turn.ModelChoice{Provider: "openai", Model: "gpt-b"}, nil))
	// s2 resolves to the same base runner and observes nothing of s1's choice.
	requireGatewaySelection(t, s, "s2", "openai", "gpt-a", "")
	requireGatewaySelection(t, s, "s1", "openai", "gpt-b", "high")

	// The runner's own published selection is the process default, untouched.
	sel := run.PrimaryModelSelection(s.Runner)
	require.True(t, sel.Set)
	require.Equal(t, "gpt-a", sel.Model, "a web selection must not move the shared runner's pin")

	// s2 chooses differently; both markers stay independent.
	require.NoError(t, s.SelectModel(ctx, "s2", turn.ModelChoice{Provider: "openai", Model: "gpt-a"}, nil))
	requireGatewaySelection(t, s, "s1", "openai", "gpt-b", "high")
	requireGatewaySelection(t, s, "s2", "openai", "gpt-a", "")
}

// TestGatewayProjectRunnerSelectionIsolated proves sessions on different
// project runners do not share a selection: the seam resolves the exact
// runner for the session and only that runner moves.
func TestGatewayProjectRunnerSelectionIsolated(t *testing.T) {
	s := gatewayModelServer(t)
	// Two hand-built project runners over their own Deps, the way the pool
	// builds them; the base runner stays untouched by their selections.
	projRunner := func() *run.Runner {
		r := &run.Runner{Deps: &run.Deps{
			Home: t.TempDir(), AgentName: "main",
			AppCfg: s.Runner.AppCfg,
		}}
		require.NoError(t, r.Load())
		return r
	}
	p1, p2 := projRunner(), projRunner()

	require.NoError(t, p1.SetPrimaryModel("openai", "gpt-b", nil))
	require.Equal(t, "gpt-b", run.PrimaryModelSelection(p1).Model)
	require.Equal(t, "gpt-a", run.PrimaryModelSelection(p2).Model)
	require.Equal(t, "gpt-a", run.PrimaryModelSelection(s.Runner).Model)
	// The gateway seam routes through the pool's resolution; here it settles
	// on the base runner, which the project selections never touched.
	requireGatewaySelection(t, s, "any-session", "openai", "gpt-a", "")
}

// TestGatewayCatalogAgreesAfterPropagatedReload pins the fix for the boundary
// this test used to document: a pooled runner's config no longer lags behind
// the live config. ReloadModelCatalog routes through Environment.ReloadConfig,
// which propagates the reloaded config to every live pool entry, so the
// picker catalog (liveCfg) and the runner's snapshot marker describe the same
// provider set.
func TestGatewayCatalogAgreesAfterPropagatedReload(t *testing.T) {
	s := gatewayModelServer(t)
	// A project runner built through the environment's own pool, the way a
	// web session bound to a project gets one.
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(s.Home, "state.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	s.Env.SQL = db
	s.Env.Deps.AgentName = "main"
	s.Env.Deps.SessionStore = state.NewSessionStore(db, "main")
	s.Sessions = s.Env.Deps.SessionStore
	s.Env.EnsureRunnerPool()
	t.Cleanup(s.Env.CloseRunnerPool)

	projects := state.NewProjectStore(db, "main")
	proj, err := projects.Create(context.Background(), state.CreateProjectInput{
		Name: "propagate", Root: "/tmp/gw-propagate", ProjectKey: "-tmp-gw-propagate",
	})
	require.NoError(t, err)
	sid := "sess-propagate"
	require.NoError(t, s.Sessions.Ensure(context.Background(), sid, sid))
	require.NoError(t, projects.BindSession(context.Background(), sid, proj.ID))
	pooled := s.Env.RunnerForSession(context.Background(), sid)
	require.NotNil(t, pooled)
	require.NotSame(t, s.Runner, pooled)
	require.NoError(t, pooled.SetPrimaryModel("openai", "gpt-b", nil))

	// The file changes underneath: reordered, with a rotated credential.
	t.Setenv("FOREBRAIN_GATEWAY_PROPAGATED_KEY", "rotated-key")
	body := "agents:\n" +
		"  definitions:\n" +
		"    main:\n" +
		"      llm_providers:\n" +
		"        - provider: openai\n" +
		"          model: gpt-b\n" +
		"          api_key: ${FOREBRAIN_GATEWAY_PROPAGATED_KEY}\n" +
		"          base_url: http://propagated.test/v1\n" +
		"        - provider: openai\n" +
		"          model: gpt-a\n" +
		"          api_key: ${FOREBRAIN_GATEWAY_PROPAGATED_KEY}\n" +
		"          base_url: http://a.test/v1\n"
	require.NoError(t, os.WriteFile(s.Env.ConfigPath, []byte(body), 0o600))

	require.NoError(t, s.ReloadModelCatalog(context.Background(), sid))

	// The pooled runner adopted the reloaded config and kept its pin; the
	// catalog and the marker now describe the same world.
	require.Same(t, s.Env.Deps.AppCfg, pooled.AppCfg)
	sel := run.PrimaryModelSelection(pooled)
	require.True(t, sel.Set)
	require.Equal(t, "gpt-b", sel.Model)
	require.Equal(t, "http://propagated.test/v1", run.PrimaryEndpoint(pooled))
	settings, err := s.ModelSettings(sid)
	require.NoError(t, err)
	require.Equal(t, sel.Provider, settings.Selection.Provider)
	require.Equal(t, sel.Model, settings.Selection.Model)
	for _, choice := range turn.ModelChoices(settings.Config, settings.AgentName) {
		if strings.EqualFold(choice.Model, "gpt-b") || strings.EqualFold(choice.Model, "gpt-a") {
			continue
		}
		t.Fatalf("catalog lists a model the reloaded config does not define: %+v", choice)
	}
}

func TestGatewayReloadModelCatalogReloadsEnvironment(t *testing.T) {
	s := gatewayModelServer(t)
	require.NoError(t, s.SelectModel(context.Background(), "s1", turn.ModelChoice{Provider: "openai", Model: "gpt-b"}, nil))
	// A catalog reload refreshes the base runner's provider details while
	// retaining its pin; it does not select config index zero.
	require.NoError(t, s.ReloadModelCatalog(context.Background(), "s1"))
	requireGatewaySelection(t, s, "s1", "openai", "gpt-b", "high")
}

// TestGatewaySurfaceNamesEachSessionsOwnModel pins the surface identity
// chain: with two web sessions sharing the base runner on different stored
// selections, the persisted assistant model and the /status report each name
// that session's own model — not the runner's default, and not the other
// session's choice.
func TestGatewaySurfaceNamesEachSessionsOwnModel(t *testing.T) {
	s := gatewayModelServer(t)
	ctx := context.Background()
	for _, sid := range []string{"s1", "s2"} {
		require.NoError(t, s.Sessions.Ensure(ctx, sid, sid))
	}
	require.NoError(t, s.SelectModel(ctx, "s1", turn.ModelChoice{Provider: "openai", Model: "gpt-b"}, nil))
	require.NoError(t, s.SelectModel(ctx, "s2", turn.ModelChoice{Provider: "openai", Model: "gpt-a"}, nil))

	for _, tc := range []struct{ sid, want string }{
		{"s1", "gpt-b"},
		{"s2", "gpt-a"},
	} {
		s.appendTranscriptTurns(ctx, tc.sid, gatewayPostTurnOptions{
			AppendAssistant: true,
			AssistantText:   "answer for " + tc.sid,
		})
		rows, err := s.Sessions.ListRecentMessages(ctx, tc.sid, 4)
		require.NoError(t, err)
		require.NotEmpty(t, rows)
		last := rows[len(rows)-1]
		require.Equal(t, tc.want, last.Model, "session %s persisted model", tc.sid)

		reply, handled := s.HandleStatusSlash(tc.sid, "webchat", false)
		require.True(t, handled)
		require.Contains(t, reply, tc.want, "session %s status model", tc.sid)
	}
}
