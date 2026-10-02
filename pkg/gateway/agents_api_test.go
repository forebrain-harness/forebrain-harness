package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/stretchr/testify/require"
)

func TestPrimaryAgentsAPIListsAndSwitches(t *testing.T) {
	home := t.TempDir()
	cfg := &appcfg.Root{}
	cfg.Agents.Definitions = map[string]appcfg.AgentDefinition{
		"main": {
			LLMProviders: []appcfg.AgentLLMProviderConfig{{
				Provider: "openai",
				Model:    "gpt-5",
				APIKey:   "test-key",
				BaseURL:  "http://127.0.0.1:9/v1",
			}},
		},
		"review": {
			Primary: true,
			LLMProviders: []appcfg.AgentLLMProviderConfig{{
				Provider: "openai",
				Model:    "gpt-5",
				APIKey:   "test-key",
				BaseURL:  "http://127.0.0.1:9/v1",
			}},
		},
	}
	runner := &run.Runner{Deps: &run.Deps{Home: home, AppCfg: cfg, AgentName: "main", WorkspaceRoot: filepath.Join(home, "workspace")}}
	s := &Server{Home: home, Env: &process.Environment{Deps: run.Deps{AppCfg: cfg}}, Runner: runner}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/agents/primary", nil)
	s.handlePrimaryAgents(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"active_id":"main"`)
	require.Contains(t, rr.Body.String(), filepath.ToSlash(filepath.Join(home, "workspace")))

	body, _ := json.Marshal(map[string]string{"agent_id": "review"})
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/agents/primary/switch", bytes.NewReader(body))
	s.handlePrimaryAgentSwitch(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"active_id":"review"`)
	require.Equal(t, "review", runner.AgentName)
	require.Equal(t, filepath.Join(home, "workspaces", "review"), runner.WorkspaceRoot)

	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/agents/primary", nil)
	s.handlePrimaryAgents(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"active_id":"review"`)
}

func TestAgentRosterIncludesPrimaryAndLiveSubagent(t *testing.T) {
	home := t.TempDir()
	cfg := &appcfg.Root{}
	cfg.Agents.Definitions = map[string]appcfg.AgentDefinition{
		"main": {},
	}
	s := &Server{Home: home, Env: &process.Environment{Deps: run.Deps{AppCfg: cfg}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := agent.RegistryFor(s.stateRoot()).Start(agent.HistoryEntry{
		AgentID:     "agent-1",
		TaskID:      "task-1",
		RunID:       "run-1",
		SessionID:   "roster-session",
		Task:        "review API",
		Status:      agent.StatusRunning,
		StartedAt:   time.Now().Unix(),
		UpdatedAt:   time.Now().Unix(),
		AgentType:   "verification",
		RuntimeKind: "typed_subagent",
	}, cancel)
	t.Cleanup(func() {
		h.Finish(agent.HistoryEntry{
			AgentID:   "agent-1",
			TaskID:    "task-1",
			RunID:     "run-1",
			SessionID: "roster-session",
			Status:    agent.StatusCancelled,
			UpdatedAt: time.Now().Unix(),
		})
	})
	_ = ctx

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/agents/roster?session_id=roster-session", nil)
	s.handleAgentRoster(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"kind":"primary"`)
	require.Contains(t, rr.Body.String(), `"id":"main"`)
	require.Contains(t, rr.Body.String(), `"kind":"subagent"`)
	require.Contains(t, rr.Body.String(), `"id":"agent-1"`)
	require.Contains(t, rr.Body.String(), `"run_id":"run-1"`)
}

func TestAgentAPICancelsLiveSubagentByStableID(t *testing.T) {
	home := t.TempDir()
	s := &Server{Home: home}
	called := false
	ctx, cancel := context.WithCancel(context.Background())
	h := agent.RegistryFor(s.stateRoot()).Start(agent.HistoryEntry{
		AgentID:   "agent-cancel",
		TaskID:    "task-cancel",
		RunID:     "run-cancel",
		SessionID: "session-cancel",
		Status:    agent.StatusRunning,
		UpdatedAt: time.Now().Unix(),
	}, func() {
		called = true
		cancel()
	})
	t.Cleanup(func() {
		h.Finish(agent.HistoryEntry{
			AgentID:   "agent-cancel",
			TaskID:    "task-cancel",
			RunID:     "run-cancel",
			SessionID: "session-cancel",
			Status:    agent.StatusCancelled,
			UpdatedAt: time.Now().Unix(),
		})
	})
	_ = ctx

	cancelled, err := s.cancelSubagent("agent-cancel")

	require.NoError(t, err)
	require.True(t, cancelled)
	require.True(t, called)
}

// crudServer builds a Server whose persisted config is a temp forebrain.yaml,
// so the write paths exercise real save + reload like production.
var crudHome string

func crudServer(t *testing.T) (*Server, string) {
	t.Helper()
	home := t.TempDir()
	cfgPath := filepath.Join(home, "forebrain.yaml")
	seed := "agents:\n  definitions:\n    main:\n      primary: true\n      llm_providers:\n      - provider: openai\n        model: gpt-5\n        api_key: ${TEST_KEY}\n        base_url: http://127.0.0.1:9/v1\n"
	require.NoError(t, os.WriteFile(cfgPath, []byte(seed), 0o600))
	cfg, err := appcfg.LoadPersisted(cfgPath)
	require.NoError(t, err)
	crudHome = home
	env := &process.Environment{Deps: run.Deps{Home: home, AppCfg: &cfg}}
	env.ConfigPath = cfgPath
	s := &Server{Home: home, Env: env}
	t.Cleanup(func() {})
	return s, cfgPath
}

func postJSON(t *testing.T, s *Server, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(method, target, bytes.NewReader(raw))
	s.handlePrimaryAgentCreate(rr, req)
	return rr
}

// withID puts a router-style :id param into the request context the way the
// real router would, for handlers tested outside a routed server.
func withID(r *http.Request, id string) *http.Request {
	return withNamedParam(r, "id", id)
}

func withNamedParam(r *http.Request, name, value string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), ParamsKey, Params{{Key: name, Value: value}}))
}

func TestPrimaryAgentCreatePersistsDefinition(t *testing.T) {
	s, cfgPath := crudServer(t)
	rr := postJSON(t, s, http.MethodPost, "/api/agents/primary", map[string]string{
		"id": "helper", "name": "Helper", "description": "extra hands",
	})
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Contains(t, rr.Body.String(), `"id":"helper"`)
	require.Contains(t, rr.Body.String(), `"name":"Helper"`)

	raw, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	require.Contains(t, string(raw), "helper:")
	require.Contains(t, string(raw), "display_name: Helper")
	// Workspace subdirectories are materialised for the new tenant at its
	// conventional root.
	require.DirExists(t, filepath.Join(home(), "workspaces", "helper", "skills"))
}

func TestPrimaryAgentCreateRejectsDuplicateID(t *testing.T) {
	s, _ := crudServer(t)
	rr := postJSON(t, s, http.MethodPost, "/api/agents/primary", map[string]string{
		"id": "main",
	})
	require.Equal(t, http.StatusConflict, rr.Code)
}

func TestPrimaryAgentCreateRejectsMissingWorkspace(t *testing.T) {
	s, _ := crudServer(t)
	rr := postJSON(t, s, http.MethodPost, "/api/agents/primary", map[string]string{
		"id": "ghost", "workspace_root": filepath.Join(t.TempDir(), "nope"),
	})
	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "existing directory")
}

func TestPrimaryAgentUpdateChangesOnlyPresentation(t *testing.T) {
	s, _ := crudServer(t)
	rr := postJSON(t, s, http.MethodPost, "/api/agents/primary", map[string]string{
		"id": "helper", "name": "Helper",
	})
	require.Equal(t, http.StatusOK, rr.Code)

	raw, _ := json.Marshal(map[string]string{"name": "Renamed", "description": "new desc"})
	rr = httptest.NewRecorder()
	req := withID(httptest.NewRequest(http.MethodPut, "/api/agents/primary/helper", bytes.NewReader(raw)), "helper")
	s.handlePrimaryAgentUpdate(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Contains(t, rr.Body.String(), `"name":"Renamed"`)

	// The definition's other fields (primary flag) are untouched: a created
	// tenant stays a tenant.
	persisted, err := s.persistedConfig()
	require.NoError(t, err)
	def := persisted.Agents.Definitions["helper"]
	require.Equal(t, "Renamed", def.DisplayName)
	require.Equal(t, "new desc", def.Description)
	require.True(t, def.Primary)
}

func TestPrimaryAgentDeleteGuards(t *testing.T) {
	s, _ := crudServer(t)
	rr := postJSON(t, s, http.MethodPost, "/api/agents/primary", map[string]string{
		"id": "helper",
	})
	require.Equal(t, http.StatusOK, rr.Code)

	// The built-in default agent cannot be deleted — and as the active one it
	// is refused twice over.
	req := withID(httptest.NewRequest(http.MethodDelete, "/api/agents/primary/main", nil), "main")
	rr = httptest.NewRecorder()
	s.handlePrimaryAgentDelete(rr, req)
	require.Equal(t, http.StatusConflict, rr.Code)

	// Deleting a non-default, non-active agent removes it from the listing.
	rr = httptest.NewRecorder()
	req = withID(httptest.NewRequest(http.MethodDelete, "/api/agents/primary/helper", nil), "helper")
	s.handlePrimaryAgentDelete(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.NotContains(t, rr.Body.String(), `"id":"helper"`)
}

// home returns the temp home the CRUD fixture rooted its config in.
func home() string {
	return crudHome
}
