package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
