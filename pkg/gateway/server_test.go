package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/mcp"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

func TestParseSlashCommandRejectsPlanmodeAlias(t *testing.T) {
	s := &Server{Home: t.TempDir()}
	res := parseSlashCommand(s, "s1", "webchat", "/planmode off")
	require.True(t, res.Handled)
	require.Contains(t, res.Reply, "There is no /planmode")
}

func TestParseSlashCommandRejectsSkillsInlineArgs(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "skills", "demo", "SKILL.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("---\nname: demo\ndescription: test\n---\n\nSkill body"), 0o600))

	s := &Server{Home: home}
	res := parseSlashCommand(s, "s1", "webchat", "/skills run demo follow up")
	require.True(t, res.Handled)
	require.False(t, res.ShouldContinueRun)
	require.Contains(t, res.Reply, "does not accept inline arguments")
}

func TestHandleSkillsInstallRegistersDynamicSlashCommand(t *testing.T) {
	home := t.TempDir()
	src := filepath.Join(home, "skills-repo")
	require.NoError(t, os.MkdirAll(filepath.Join(src, "pptx"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(src, "pptx", "SKILL.md"), []byte(`---
name: pptx
description: demo
---

body`), 0o600))
	cmd := exec.Command("git", "init")
	cmd.Dir = src
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	cmd = exec.Command("git", "add", ".")
	cmd.Dir = src
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	cmd = exec.Command("git", "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "init")
	cmd.Dir = src
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, string(out))

	require.NoError(t, os.MkdirAll(filepath.Join(home, ".git"), 0o755))
	trustedProject, err := safety.Resolve(home)
	require.NoError(t, err)
	require.NoError(t, safety.MarkTrusted(home, trustedProject))
	launch, err := safety.ResolveProjectContext(home, home)
	require.NoError(t, err)

	// The launch project is the context the runtime loads skills for, so the
	// test states it explicitly instead of relying on the process working
	// directory.
	s := &Server{Home: home, LaunchProject: launch}

	// The agent's own skill routes work in no project: a project
	// destination belongs to that project's space.
	reqBody := strings.NewReader(`{"source_ref":"file://` + src + `","dest":"project","skill":"pptx","name":"pptx-installed"}`)
	req, err := http.NewRequest(http.MethodPost, "/api/skills/install", reqBody)
	require.NoError(t, err)
	rr := httptest.NewRecorder()
	s.handleSkillsInstall(rr, req)
	require.NotEqual(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Contains(t, rr.Body.String(), "no project root")

	reqBody = strings.NewReader(`{"source_ref":"file://` + src + `","dest":"workspace","skill":"pptx","name":"pptx-installed"}`)
	req, err = http.NewRequest(http.MethodPost, "/api/skills/install", reqBody)
	require.NoError(t, err)
	rr = httptest.NewRecorder()
	s.handleSkillsInstall(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Contains(t, rr.Body.String(), `"installed"`)
	require.Contains(t, rr.Body.String(), `"source":"git_repo"`)

	res := parseSlashCommand(s, "s1", "webchat", "/pptx demo-change")
	require.True(t, res.Handled)
	require.True(t, res.ShouldContinueRun)
	require.Equal(t, "demo-change", res.ContinueInput)
	require.Equal(t, "pptx", res.SkillName)
	require.NotEmpty(t, res.SkillPath)
}

func TestParseSlashCommandChannelAdapterBuiltInSlashSkillOnly(t *testing.T) {
	s := &Server{Home: t.TempDir()}

	res := parseSlashCommand(s, "s1", "slack", "/plan")
	require.True(t, res.Handled)
	require.False(t, res.ShouldContinueRun)
	require.Equal(t, "channel slash supports skill commands only", res.Reply)
}

func TestParseSlashCommandExplicitChannelAdapterAllowlistIncludesTelegram(t *testing.T) {
	s := &Server{Home: t.TempDir()}

	res := parseSlashCommand(s, "s1", "telegram", "/plan")
	require.True(t, res.Handled)
	require.False(t, res.ShouldContinueRun)
	require.Equal(t, "channel slash supports skill commands only", res.Reply)
}

func TestParseSlashCommandUnknownChannelRejectedBeforeSkillOnlyPath(t *testing.T) {
	s := &Server{Home: t.TempDir()}

	res := parseSlashCommand(s, "s1", "not-a-channel", "/plan")
	require.True(t, res.Handled)
	require.False(t, res.ShouldContinueRun)
	require.Equal(t, `unsupported slash channel "not-a-channel"`, res.Reply)
}

func TestParseSlashCommandChannelAdapterDynamicSkillSlashStillRuns(t *testing.T) {
	turn.ResetDynamic()
	t.Cleanup(turn.ResetDynamic)

	require.NoError(t, turn.ReplaceDynamicSource("test-slack-skill", []turn.DynamicCommand{
		{
			Command: turn.Command{
				Name:               "opsx:apply",
				Description:        "apply openspec change",
				AllowedSurfaces:    []turn.Surface{turn.SurfaceWebChat, turn.SurfaceTUI},
				SupportsInlineArgs: true,
				Visibility:         turn.VisibilityPublic,
			},
			Handler: func(ctx turn.Context, line string, toks []string) turn.Result {
				return turn.Result{
					Handled:           true,
					ShouldContinueRun: true,
					ContinueInput:     "dynamic-skill:" + strings.Join(toks[1:], " "),
				}
			},
		},
	}))

	s := &Server{Home: t.TempDir()}
	res := parseSlashCommand(s, "s1", "slack", "/opsx:apply demo-change")
	require.True(t, res.Handled)
	require.True(t, res.ShouldContinueRun)
	require.Equal(t, "dynamic-skill:demo-change", res.ContinueInput)
}

func TestHandleSkillsInstallCollectionPackageReturnsInstalledList(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	src := filepath.Join(home, "cc-skills-golang")
	require.NoError(t, os.MkdirAll(filepath.Join(src, "skills", "golang-cli"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(src, "skills", "golang-testing"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(src, "skills", "golang-cli", "SKILL.md"), []byte(`---
name: golang-cli
description: cli
---
body`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(src, "skills", "golang-testing", "SKILL.md"), []byte(`---
name: golang-testing
description: testing
---
body`), 0o600))
	cmd := exec.Command("git", "init")
	cmd.Dir = src
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	cmd = exec.Command("git", "add", ".")
	cmd.Dir = src
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	cmd = exec.Command("git", "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "init")
	cmd.Dir = src
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, string(out))

	s := &Server{Home: home}
	reqBody := strings.NewReader(`{"source_ref":"file://` + src + `","dest":"workspace"}`)
	req, err := http.NewRequest(http.MethodPost, "/api/skills/install", reqBody)
	require.NoError(t, err)
	rr := httptest.NewRecorder()
	s.handleSkillsInstall(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Contains(t, rr.Body.String(), `"count":2`)
	require.Contains(t, rr.Body.String(), `"name":"golang-cli"`)
	require.Contains(t, rr.Body.String(), `"name":"golang-testing"`)
}

func TestHandleSkillsInstallPublishesProgressAndDoneEvents(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	src := filepath.Join(home, "skills-repo")
	require.NoError(t, os.MkdirAll(filepath.Join(src, "pptx"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(src, "pptx", "SKILL.md"), []byte(`---
name: pptx
description: demo
---
body`), 0o600))
	cmd := exec.Command("git", "init")
	cmd.Dir = src
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	cmd = exec.Command("git", "add", ".")
	cmd.Dir = src
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	cmd = exec.Command("git", "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "init")
	cmd.Dir = src
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, string(out))

	notifier := event.NewNotifier()
	var events []event.TaskEvent
	unsub := notifier.Subscribe("default", func(evt event.TaskEvent) {
		events = append(events, evt)
	})
	defer unsub()

	s := &Server{Home: home, Notifier: notifier}
	reqBody := strings.NewReader(`{"source_ref":"file://` + src + `","dest":"workspace","skill":"pptx","name":"pptx-installed"}`)
	req, err := http.NewRequest(http.MethodPost, "/api/skills/install", reqBody)
	require.NoError(t, err)
	rr := httptest.NewRecorder()
	s.handleSkillsInstall(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	var sawProgress bool
	var sawDone bool
	for _, evt := range events {
		if evt.EventKind == event.EventProgress && evt.Task.Title == "Skill lifecycle" {
			sawProgress = true
		}
		if evt.EventKind == event.EventDone && evt.Task.Title == "Skill lifecycle" {
			sawDone = true
		}
	}
	require.True(t, sawProgress, "expected skill install progress event")
	require.True(t, sawDone, "expected skill install done event")
}

func TestHandleSkillsListIncludesInstalledSkills(t *testing.T) {
	home := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(home, "workspace", "skills", "demo"), 0o755))
	// The fixture carries a description because a description is what makes a
	// skill exist at all: discovery, the catalog and the picker all skip a
	// skill without one, so a fixture without it would be asserting that an
	// invisible skill is listed.
	require.NoError(t, os.WriteFile(filepath.Join(home, "workspace", "skills", "demo", "SKILL.md"), []byte("---\nname: demo\ndescription: demo\n---\n"), 0o600))
	s := &Server{Home: home}
	req := httptest.NewRequest(http.MethodGet, "/api/skills", nil)
	rr := httptest.NewRecorder()
	s.handleSkillsList(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Contains(t, rr.Body.String(), `"installed"`)
	require.Contains(t, rr.Body.String(), `"enabled":true`)
	require.NotContains(t, rr.Body.String(), `"candidates"`)
}

func TestHandleSkillsToggleUpdatesEnabledSet(t *testing.T) {
	home := t.TempDir()
	skillDir := filepath.Join(home, "workspace", "skills", "demo")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: demo
description: demo
---
body`), 0o600))

	s := &Server{Home: home}
	req := httptest.NewRequest(http.MethodPost, "/api/skills/toggle", strings.NewReader(`{"enabled_paths":[]}`))
	rr := httptest.NewRecorder()
	s.handleSkillsToggle(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Contains(t, rr.Body.String(), `"status":"ok"`)
	require.Contains(t, rr.Body.String(), `"enabled":false`)

	req = httptest.NewRequest(http.MethodPost, "/api/skills/toggle", strings.NewReader(`{"enabled_paths":["`+skillDir+`"]}`))
	rr = httptest.NewRecorder()
	s.handleSkillsToggle(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Contains(t, rr.Body.String(), `"enabled":true`)
}

func TestParseSlashCommandNewSwitchesWebchatSession(t *testing.T) {
	home := t.TempDir()
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(home, "state.db"))
	require.NoError(t, err)
	defer db.Close()

	s := &Server{
		Home:     home,
		Sessions: state.NewSessionStore(db, "main"),
	}
	res := parseSlashCommand(s, "s1", "webchat", "/new")
	require.True(t, res.Handled)
	require.True(t, res.SessionChanged)
	require.True(t, res.SessionSwitched)
	require.NotEmpty(t, res.SessionID)
	require.Equal(t, "New Session", res.SessionTitle)
}

func TestParseSlashCommandNewDoesNotCopyPriorTranscriptIntoNewSession(t *testing.T) {
	home := t.TempDir()
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(home, "state.db"))
	require.NoError(t, err)
	defer db.Close()

	sessions := state.NewSessionStore(db, "main")
	require.NoError(t, sessions.Ensure(context.Background(), "s1", "Existing"))
	_, err = sessions.Append(context.Background(), "s1", "user", "hello")
	require.NoError(t, err)
	_, err = sessions.Append(context.Background(), "s1", "assistant", "world")
	require.NoError(t, err)

	s := &Server{
		Home:     home,
		Sessions: sessions,
	}
	res := parseSlashCommand(s, "s1", "webchat", "/new")
	require.True(t, res.Handled)
	require.True(t, res.SessionSwitched)
	require.NotEmpty(t, res.SessionID)
	require.NotEqual(t, "s1", res.SessionID)

	turns, err := sessions.ListRecentMessages(context.Background(), res.SessionID, 20)
	require.NoError(t, err)
	require.Len(t, turns, 0)
}

func TestHandleChatWSNewOnlyBindsFreshSession(t *testing.T) {
	home := t.TempDir()
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(home, "state.db"))
	require.NoError(t, err)
	defer db.Close()

	s := &Server{
		Home:     home,
		Sessions: state.NewSessionStore(db, "main"),
	}
	server := httptest.NewServer(http.HandlerFunc(s.HandleChatWS))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer conn.Close()

	var connected wsServerMsg
	require.NoError(t, conn.ReadJSON(&connected))
	require.Equal(t, "connected", connected.Op)

	require.NoError(t, conn.WriteJSON(wsClientMsg{
		Op:        "start_run",
		RequestID: "req-new",
		SessionID: "old-session",
		Message:   wsClientMessage{Content: "/new", Role: "user"},
	}))

	var got []wsServerMsg
	for {
		var msg wsServerMsg
		require.NoError(t, conn.ReadJSON(&msg))
		got = append(got, msg)
		if msg.Op == "session_bound" && msg.RequestID == "req-new" {
			break
		}
	}
	require.Len(t, got, 1, "unexpected messages before session_bound: %s", mustJSON(t, got))
	require.Equal(t, "session_bound", got[0].Op)
	require.Equal(t, "req-new", got[0].RequestID)
	require.NotEmpty(t, got[0].SessionID)
	require.NotEqual(t, "old-session", got[0].SessionID)

	require.NoError(t, conn.SetReadDeadline(time.Now().Add(100*time.Millisecond)))
	var extra wsServerMsg
	err = conn.ReadJSON(&extra)
	require.Error(t, err, "session switch slash must not emit synthetic run events, got %s", mustJSON(t, extra))
}

// TestHandleChatWSSlashReplyIsUIOnly pins R-model on the web surface: a
// slash command's reply reaches the client as one dedicated slash_reply
// message — no synthetic run lifecycle, and nothing appended to the session
// transcript, so the next turn's assembled request cannot contain it.
func TestHandleChatWSSlashReplyIsUIOnly(t *testing.T) {
	home := t.TempDir()
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(home, "state.db"))
	require.NoError(t, err)
	defer db.Close()

	sessions := state.NewSessionStore(db, "main")
	require.NoError(t, sessions.Ensure(context.Background(), "s1", "Slash"))

	s := &Server{
		Home:     home,
		Sessions: sessions,
	}
	server := httptest.NewServer(http.HandlerFunc(s.HandleChatWS))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer conn.Close()

	var connected wsServerMsg
	require.NoError(t, conn.ReadJSON(&connected))
	require.Equal(t, "connected", connected.Op)

	require.NoError(t, conn.WriteJSON(wsClientMsg{
		Op:        "start_run",
		RequestID: "req-slash",
		SessionID: "s1",
		Message:   wsClientMessage{Content: "/fast", Role: "user"},
	}))

	var got []wsServerMsg
	for {
		var msg wsServerMsg
		require.NoError(t, conn.ReadJSON(&msg))
		got = append(got, msg)
		if msg.Op == "slash_reply" {
			break
		}
	}
	require.Len(t, got, 2, "slash command must emit only session_bound + slash_reply, got %s", mustJSON(t, got))
	require.Equal(t, "session_bound", got[0].Op)
	require.Equal(t, "slash_reply", got[1].Op)
	require.Equal(t, "req-slash", got[1].RequestID)
	require.Equal(t, "s1", got[1].SessionID)
	require.NotEmpty(t, got[1].Text)

	turns, err := sessions.ListRecentMessages(context.Background(), "s1", 20)
	require.NoError(t, err)
	require.Len(t, turns, 0, "slash replies must not enter the transcript: %s", mustJSON(t, turns))
}

// TestGatewayToolMetaPayloadCarriesTheSubagentCall pins the live web path's
// last hop: the card facts of a subagent_* call (plan 012) ride the tool meta
// the websocket payloads carry, so a call the model sees fail renders as the
// subagent card ("Failed to run …" header with its reason) on the web exactly
// as the terminal draws it — not as a generic tool card. The history-row path
// carries the same facts through handleChatMessages; both must stay in step.
func TestGatewayToolMetaPayloadCarriesTheSubagentCall(t *testing.T) {
	call := &event.SubagentCall{Verb: "run", Tasks: []event.SubagentCallTask{{
		Index: 0, Title: "Summarize README.md", AgentType: "general-purpose", Status: "failed",
	}}}
	meta := tool.ToolMeta{ToolName: "subagent_run", Status: "failed", Invocation: "run Summarize README.md", SubagentCall: call}

	started := gatewayToolStartedStepData("call-1", "tool subagent_run", "subagent_run", map[string]any{"task": "read"}, meta)
	raw, err := json.Marshal(started["tool_meta"])
	require.NoError(t, err)
	var carried struct {
		SubagentCall *event.SubagentCall `json:"subagent_call"`
	}
	require.NoError(t, json.Unmarshal(raw, &carried))
	require.NotNil(t, carried.SubagentCall, "live tool_meta must carry subagent_call: %s", raw)
	require.Equal(t, "run", carried.SubagentCall.Verb)
	require.Equal(t, "failed", carried.SubagentCall.Tasks[0].Status)

	completed := gatewayToolCompletedStepData("call-1", "tool subagent_run", "subagent_run", nil, "unexpected EOF", "", "", "", meta)
	raw, err = json.Marshal(completed["tool_meta"])
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &carried))
	require.NotNil(t, carried.SubagentCall, "a failed call keeps its card facts: %s", raw)

	// A meta that carries nothing at all still collapses to no payload.
	require.Nil(t, gatewayToolMetaPayload(tool.ToolMeta{}))
}

func TestParseSlashCommandBlocksLifecycleSlashDuringRun(t *testing.T) {
	home := t.TempDir()
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(home, "state.db"))
	require.NoError(t, err)
	defer db.Close()

	s := &Server{
		Home:     home,
		Sessions: state.NewSessionStore(db, "main"),
	}
	res := parseSlashCommandWithOptions(s, "s1", "webchat", "/new", turn.Context{DuringRun: true}, nil)
	require.True(t, res.Handled)
	require.Contains(t, res.Reply, "/new is unavailable while a task is running.")
	require.False(t, res.SessionSwitched)
}

func TestParseSlashCommandForkSwitchesWebchatSession(t *testing.T) {
	home := t.TempDir()
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(home, "state.db"))
	require.NoError(t, err)
	defer db.Close()

	sessions := state.NewSessionStore(db, "main")
	require.NoError(t, sessions.Ensure(context.Background(), "parent", "Parent"))
	_, err = sessions.Append(context.Background(), "parent", "user", "hello")
	require.NoError(t, err)

	// /fork now materializes and copies the source session's model choice,
	// so the fixture needs a bound configuration and a loaded runner.
	t.Setenv("FOREBRAIN_FORK_MODEL_TEST_KEY", "test-key")
	cfgPath := filepath.Join(home, "forebrain.yaml")
	cfg := appcfg.Root{Agents: appcfg.AgentsSection{Definitions: map[string]appcfg.AgentDefinition{
		"main": {LLMProviders: []appcfg.AgentLLMProviderConfig{
			{Provider: "openai", Model: "gpt-4o", APIKey: "${FOREBRAIN_FORK_MODEL_TEST_KEY}", BaseURL: "http://a.test/v1"},
		}},
	}}}
	require.NoError(t, appcfg.Save(cfgPath, cfg))
	env := &process.Environment{Root: home, ConfigPath: cfgPath, Deps: run.Deps{Home: home, AgentName: "main", AppCfg: &cfg}}
	runner := &run.Runner{Deps: &env.Deps}
	env.Runner = runner
	require.NoError(t, runner.Load())

	s := &Server{
		Home:     home,
		Runner:   runner,
		Env:      env,
		Sessions: sessions,
	}
	res := parseSlashCommand(s, "parent", "webchat", "/fork")
	require.True(t, res.Handled)
	require.True(t, res.SessionChanged)
	require.True(t, res.SessionSwitched)
	require.NotEmpty(t, res.SessionID)
	require.NotEqual(t, "parent", res.SessionID)
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	return string(raw)
}

// decodeMCPServersBody reads the /v1/mcp/servers envelope.
func decodeMCPServersBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v\n%s", err, rec.Body.String())
	}
	return body
}

func mcpServerRow(t *testing.T, body map[string]any, name string) map[string]any {
	t.Helper()
	rows, _ := body["servers"].([]any)
	for _, item := range rows {
		row, _ := item.(map[string]any)
		if row["name"] == name {
			return row
		}
	}
	t.Fatalf("no row for %q in %#v", name, body["servers"])
	return nil
}

// TestFindMCPServerMergedReadsTheResolvedLayers pins the OAuth lookups' source
// now that the base runner hosts no MCP list: the entry is found by resolving
// what a launch with no project resolves — the shared list plus the primary
// agent's own — with the base runner's own lists reading empty.
func TestFindMCPServerMergedReadsTheResolvedLayers(t *testing.T) {
	cfg := &appcfg.Root{}
	cfg.Agents.Defaults.MCPServers = []appcfg.MCPServerConfig{
		{Name: "docs", Transport: "stdio", Command: "/bin/docs"},
	}
	cfg.Agents.Definitions = map[string]appcfg.AgentDefinition{
		"main": {MCPServers: []appcfg.MCPServerConfig{
			{Name: "agenttool", Transport: "stdio", Command: "/bin/agenttool"},
		}},
	}
	// A base runner with no MCP lists of its own — its shape since it stopped
	// hosting servers — still names the workspace and agent the layers read.
	primary := &run.Runner{Deps: &run.Deps{Home: t.TempDir(), AgentName: "main"}}
	s := newMCPServersTestServer(t, cfg, primary)

	for _, name := range []string{"docs", "agenttool", "Docs"} {
		srv, err := s.findMCPServerMerged(name)
		if err != nil {
			t.Fatalf("findMCPServerMerged(%q): %v", name, err)
		}
		if mcp.SameServerName(srv.Name, name) != true {
			t.Fatalf("findMCPServerMerged(%q) = %q", name, srv.Name)
		}
	}
	if _, err := s.findMCPServerMerged("nope"); err == nil {
		t.Fatal("an unknown name must not be found")
	}
}

// TestMCPServersV1ReportsConfiguredServersWithNoRuntimeView pins the shape a UI
// gets when there is no Runner to ask: the configured entries, and an explicit
// statement that no runtime state answered.
//
// The distinction matters because the alternative — omitting the runtime fields
// silently — makes "we could not ask" indistinguishable from "nothing is
// running", and a client would render the second as fact.
func TestMCPServersV1ReportsConfiguredServersWithNoRuntimeView(t *testing.T) {
	cfg := &appcfg.Root{}
	cfg.Agents.Defaults.MCPServers = []appcfg.MCPServerConfig{
		{Name: "docs", Transport: "stdio", Command: "/bin/docs"},
		{Name: "web", Transport: "streamable_http", URL: "https://example.test/mcp"},
	}
	s := newMCPServersTestServer(t, cfg, nil)

	rec := httptest.NewRecorder()
	s.handleMCPServersV1(rec, httptest.NewRequest(http.MethodGet, "/v1/mcp/servers", nil))
	body := decodeMCPServersBody(t, rec)

	if body["runtime_available"] != false {
		t.Fatalf("runtime_available = %v, want false with no Runner to ask", body["runtime_available"])
	}
	// The base runner hosts no servers, so a request that names no session has
	// no runtime to quote at all: the scope says "none" rather than dressing
	// a runner that never started anything up as an answer.
	if body["runtime_scope"] != "none" {
		t.Fatalf("runtime_scope = %v, want none: the base runner reports nothing", body["runtime_scope"])
	}
	// The configured entries are still reported: a client with no runtime to show
	// still has a configuration to show.
	for _, name := range []string{"docs", "web"} {
		row := mcpServerRow(t, body, name)
		if _, ok := row["conn_status"]; ok {
			t.Fatalf("%s got a state with no runtime view: %#v", name, row)
		}
	}
	// And a config the server cannot read at all is refused, not guessed at.
	bare := &Server{Home: t.TempDir()}
	rec = httptest.NewRecorder()
	bare.handleMCPServersV1(rec, httptest.NewRequest(http.MethodGet, "/v1/mcp/servers", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want an explicit unavailability with no live config", rec.Code)
	}
}

// newMCPServersTestServer assembles a gateway server whose live config the
// handler can read, over the given server list and Runner.
func newMCPServersTestServer(t *testing.T, cfg *appcfg.Root, primary *run.Runner) *Server {
	t.Helper()
	s := &Server{Home: t.TempDir(), Runner: primary}
	s.Env = &process.Environment{}
	s.Env.Deps.AppCfg = cfg
	s.Env.Runner = primary
	s.Env.Root = s.Home
	return s
}

// TestMCPServersV1RuntimeScopeFollowsTheRequest pins the one thing a client
// cannot infer: which Runner answered. The base runner hosts no servers, so a
// request that names no session — or one whose session resolves to no project
// worker — is answered with scope "none": presenting the base runner's
// emptiness as a session's state would read as "nothing is running".
func TestMCPServersV1RuntimeScopeFollowsTheRequest(t *testing.T) {
	cfg := &appcfg.Root{}
	cfg.Agents.Defaults.MCPServers = []appcfg.MCPServerConfig{{Name: "docs", Transport: "stdio", Command: "/bin/docs"}}
	primary := &run.Runner{Deps: &run.Deps{Home: t.TempDir(), MCPServers: cfg.Agents.Defaults.MCPServers}}

	cases := []struct {
		name      string
		query     string
		wantScope string
	}{
		// No session named: the base runner is the only candidate and it
		// reports nothing.
		{name: "no session", wantScope: "none"},
		// A session named with no worker to resolve it: still nothing to
		// quote, still stated, rather than presented as this session's state.
		{name: "session with no worker", query: "?session_id=s-1", wantScope: "none"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// runnerFor resolves through the process environment's runner pool, which
			// answers with the primary Runner for a session it has no project for.
			s := newMCPServersTestServer(t, cfg, primary)

			rec := httptest.NewRecorder()
			s.handleMCPServersV1(rec, httptest.NewRequest(http.MethodGet, "/v1/mcp/servers"+tc.query, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			body := decodeMCPServersBody(t, rec)
			if body["runtime_scope"] != tc.wantScope {
				t.Fatalf("runtime_scope = %v, want %q", body["runtime_scope"], tc.wantScope)
			}
			// "none" means no runtime view rode along either.
			if body["runtime_available"] != false {
				t.Fatalf("runtime_available = %v, want false under scope none", body["runtime_available"])
			}
		})
	}
}

// TestMCPServersV1KeepsAFailedServerVisibleWithItsText pins the projection a UI
// needs: a server that failed keeps its place and carries the reason.
//
// A failed entry disappearing from the list is the failure mode this guards: an
// absent entry reads as "not configured", so the reader would go looking for a
// configuration mistake instead of fixing the server.
func TestMCPServersV1KeepsAFailedServerVisibleWithItsText(t *testing.T) {
	cfg := &appcfg.Root{}
	servers := []appcfg.MCPServerConfig{
		{Name: "docs", Transport: "stdio", Command: "/bin/docs"},
		{Name: "broken", Transport: "stdio", Command: "/bin/broken"},
	}
	cfg.Agents.Defaults.MCPServers = servers
	runner := &run.Runner{Deps: &run.Deps{Home: t.TempDir(), MCPServers: servers}}
	s := newMCPServersTestServer(t, cfg, runner)

	rec := httptest.NewRecorder()
	s.handleMCPServersV1(rec, httptest.NewRequest(http.MethodGet, "/v1/mcp/servers", nil))
	body := decodeMCPServersBody(t, rec)

	// Without a startable generation the runtime view has no records, so every
	// configured server is reported with no state rather than a guessed one.
	for _, name := range []string{"docs", "broken"} {
		row := mcpServerRow(t, body, name)
		if _, ok := row["conn_status"]; ok {
			t.Fatalf("%s got a conn_status without a runtime view: %#v", name, row)
		}
	}
	if body["runtime_available"] != false {
		t.Fatalf("runtime_available = %v, want false", body["runtime_available"])
	}
	// And the configured fields are still there, so the row is usable either way.
	row := mcpServerRow(t, body, "docs")
	if row["scope"] != "global" || row["transport"] != "stdio" {
		t.Fatalf("configured projection lost fields: %#v", row)
	}
}

// TestMCPServersV1RejectsNonGet pins the method contract, which a UI relies on
// when it probes this endpoint.
func TestMCPServersV1RejectsNonGet(t *testing.T) {
	s := newMCPServersTestServer(t, &appcfg.Root{}, nil)
	rec := httptest.NewRecorder()
	s.handleMCPServersV1(rec, httptest.NewRequest(http.MethodPost, "/v1/mcp/servers", strings.NewReader("{}")))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

// pwdStubLLM answers the first call with a single tool call, then finishes, so
// a turn runs end to end without a provider.
type pwdStubLLM struct{ calls int }

func (m *pwdStubLLM) Execute(_ context.Context, _ []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	m.calls++
	if m.calls == 1 {
		call := llm.ToolCall{
			ID:       "call-1",
			Type:     llm.ToolTypeFunction,
			Function: llm.FunctionCall{Name: "pwd_tool", Arguments: `{}`},
		}
		msg := llm.AssistantMessage(nil, call)
		return &llm.Result{Message: &msg, Usage: &llm.Usage{InputTokens: 1, OutputTokens: 1}}, nil
	}
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("done")})
	return &llm.Result{Message: &msg, Usage: &llm.Usage{InputTokens: 1, OutputTokens: 1}}, nil
}

func setRunnerMainForTest(t *testing.T, r *run.Runner, a *agent.Agent) {
	t.Helper()
	v := reflect.ValueOf(r).Elem().FieldByName("main")
	if !v.IsValid() || !v.CanAddr() {
		t.Fatalf("runner main field is not settable")
	}
	reflect.NewAt(v.Type(), unsafe.Pointer(v.UnsafeAddr())).Elem().Set(reflect.ValueOf(a))
}

// TestProjectRunnersExecuteInTheirOwnRoot models one gateway process serving
// two projects: the process directory is neither project, yet each project's
// runner executes its tools in its own root, because RunContent stamps the
// session project root onto the turn's context.
func TestProjectRunnersExecuteInTheirOwnRoot(t *testing.T) {
	processDir := t.TempDir()
	project1 := t.TempDir()
	project2 := t.TempDir()
	t.Chdir(processDir)

	sawDir := func(project string) string {
		var seen string
		rec, err := llm.NewTool("pwd_tool", "record the working dir", func(ctx context.Context, _ *struct{}) (string, error) {
			seen, _ = tool.WorkingDir(ctx)
			return seen, nil
		})
		if err != nil {
			t.Fatalf("NewTool: %v", err)
		}
		main, err := agent.New(&pwdStubLLM{}, "main", "desc")
		if err != nil {
			t.Fatalf("agent.New: %v", err)
		}
		if err := main.AddTool(rec); err != nil {
			t.Fatalf("AddTool: %v", err)
		}
		main.SetTracer(agent.Noop)
		r := &run.Runner{Deps: &run.Deps{ProjectRoot: project}}
		setRunnerMainForTest(t, r, main)
		if _, err := r.RunContent(context.Background(), []llm.ContentPart{llm.Text("go")}); err != nil {
			t.Fatalf("RunContent for %s: %v", project, err)
		}
		return seen
	}

	if got := sawDir(project1); filepath.Clean(got) != filepath.Clean(project1) {
		t.Fatalf("project 1 turn ran in %q, want %q", got, project1)
	}
	if got := sawDir(project2); filepath.Clean(got) != filepath.Clean(project2) {
		t.Fatalf("project 2 turn ran in %q, want %q", got, project2)
	}
}

// TestHandleChatWSRefusesATurnWithoutARunnableProject pins the start-run
// resolution (R1): a conversation whose project directory is gone, or which
// belongs to no project, is refused with a stable code BEFORE any run row
// exists — never silently run on a sandbox derived from the wrong directory.
func TestHandleChatWSRefusesATurnWithoutARunnableProject(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	// The real gateway shape: process.Open with NoLaunchProject, so the
	// environment genuinely has no project of its own (R4).
	t.Setenv("FOREBRAIN_GATEWAY_PROJECT_KEY", "test-key")
	cfgPath := filepath.Join(home, "forebrain.yaml")
	raw := "gateway:\n  auth:\n    mode: none\n" +
		"agents:\n  definitions:\n    main:\n      primary: true\n      llm_providers:\n" +
		"        - provider: openai\n          model: gpt-main\n          api_key: ${FOREBRAIN_GATEWAY_PROJECT_KEY}\n          base_url: http://127.0.0.1:9/v1\n"
	require.NoError(t, os.WriteFile(cfgPath, []byte(raw), 0o600))
	env, err := process.Open(ctx, process.OpenOptions{Home: home, ConfigPath: cfgPath, NoLaunchProject: true, SessionSource: "webchat"})
	require.NoError(t, err)
	defer env.Close()
	db := env.SQL
	sessions := env.Deps.SessionStore
	runStore := env.Deps.RunRT
	s := &Server{Home: home, Sessions: sessions, RunRT: runStore, Runner: env.Runner, Env: env}
	projects := state.NewProjectStore(db, "main")

	goneRoot := filepath.Join(t.TempDir(), "gone")
	pg, err := projects.Create(ctx, state.CreateProjectInput{Name: "gone", Root: goneRoot, ProjectKey: state.ProjectKeyForRoot(goneRoot)})
	require.NoError(t, err)
	require.NoError(t, sessions.Ensure(ctx, "s-gone", "s-gone"))
	require.NoError(t, projects.BindSession(ctx, "s-gone", pg.ID))
	require.NoError(t, sessions.Ensure(ctx, "s-orphan", "s-orphan"))

	server := httptest.NewServer(http.HandlerFunc(s.HandleChatWS))
	defer server.Close()

	refusal := func(t *testing.T, sid string) wsServerMsg {
		t.Helper()
		conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
		require.NoError(t, err)
		defer conn.Close()
		var connected wsServerMsg
		require.NoError(t, conn.ReadJSON(&connected))
		require.NoError(t, conn.SetReadDeadline(time.Now().Add(10*time.Second)))
		require.NoError(t, conn.WriteJSON(wsClientMsg{
			Op: "start_run", RequestID: "req-1", SessionID: sid,
			Message: wsClientMessage{Content: "hello", Role: "user"},
		}))
		for {
			var msg wsServerMsg
			require.NoError(t, conn.ReadJSON(&msg))
			if msg.Op == "run_error" {
				return msg
			}
		}
	}
	countRuns := func(t *testing.T, sid string) int {
		t.Helper()
		var n int
		require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM fb_runs WHERE session_id=?`, sid).Scan(&n))
		return n
	}

	gone := refusal(t, "s-gone")
	require.Equal(t, "session_directory_missing", gone.Message)
	require.Contains(t, gone.Error, goneRoot)
	goneData, _ := gone.Data.(map[string]any)
	require.Equal(t, "session_directory_missing", goneData["code"])
	require.Zero(t, countRuns(t, "s-gone"), "a refused turn must not leave a run behind")

	orphan := refusal(t, "s-orphan")
	require.Equal(t, "session_has_no_project", orphan.Message)
	orphanData, _ := orphan.Data.(map[string]any)
	require.Equal(t, "session_has_no_project", orphanData["code"])
	require.Zero(t, countRuns(t, "s-orphan"), "a refused turn must not leave a run behind")
}

// TestWorkshopTurnRunsInTheAgentSkillDirectory pins the one project-less
// conversation R1 lets run: a primary-agent workshop task runs on the agent's
// workshop runner, working in the agent's own skill directory, with no project
// MCP/LSP/skills loaded.
func TestWorkshopTurnRunsInTheAgentSkillDirectory(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.db"))
	require.NoError(t, err)
	defer db.Close()
	sessions := state.NewSessionStore(db, "main")
	require.NoError(t, sessions.EnsureAt(ctx, "s-workshop", "s-workshop", state.SessionBirth{Source: state.SessionSourceWorkshop}))

	cfg := &appcfg.Root{Agents: appcfg.AgentsSection{Definitions: map[string]appcfg.AgentDefinition{
		"main": {Primary: true, LLMProviders: []appcfg.AgentLLMProviderConfig{{Provider: "openai", Model: "gpt-main", APIKey: "test-key", BaseURL: "http://127.0.0.1:9/v1"}}},
	}}}
	runner := &run.Runner{Deps: &run.Deps{Home: home, AgentName: "main", AppCfg: cfg, SessionStore: sessions, WorkspaceRoot: workspace}}
	require.NoError(t, runner.Load())
	env := &process.Environment{Root: home, SQL: db, Runner: runner, RawConfig: *cfg,
		Deps: run.Deps{Home: home, AgentName: "main", AppCfg: cfg, SessionStore: sessions, WorkspaceRoot: workspace}}

	workshop, err := env.RunnerForTurn(ctx, "s-workshop")
	require.NoError(t, err)
	require.NotNil(t, workshop)
	require.Empty(t, workshop.LaunchProject.Project.Root, "the workshop entry loads no project config")
	require.Equal(t, filepath.Join(workspace, "skills"), filepath.Clean(workshop.ProjectRoot))

	// A fake tool records the turn's working dir; the workshop runner's own
	// agent is swapped for one carrying it, the way the project-root test does.
	var seen string
	rec, err := llm.NewTool("pwd_tool", "record the working dir", func(tctx context.Context, _ *struct{}) (string, error) {
		seen, _ = tool.WorkingDir(tctx)
		return seen, nil
	})
	require.NoError(t, err)
	main, err := agent.New(&pwdStubLLM{}, "main", "desc")
	require.NoError(t, err)
	require.NoError(t, main.AddTool(rec))
	main.SetTracer(agent.Noop)
	setRunnerMainForTest(t, workshop, main)
	_, err = workshop.RunContent(ctx, []llm.ContentPart{llm.Text("go")})
	require.NoError(t, err)
	require.Equal(t, filepath.Join(workspace, "skills"), filepath.Clean(seen))
}
