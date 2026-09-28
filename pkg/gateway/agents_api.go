package gateway

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	"github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

type primaryAgentsResponse struct {
	ActiveID string           `json:"active_id"`
	Records  []config.Summary `json:"records"`
}

func (s *Server) primaryAgentResolver() (*config.Resolver, error) {
	var cfg = s.liveCfg()
	return config.NewResolver(strings.TrimSpace(s.Home), cfg)
}

func (s *Server) activePrimarySummary() (config.Summary, error) {
	resolver, err := s.primaryAgentResolver()
	if err != nil {
		return config.Summary{}, err
	}
	return resolver.Active()
}

func (s *Server) activeWorkspaceRoot() string {
	if s == nil {
		return ""
	}
	if active, err := s.activePrimarySummary(); err == nil && strings.TrimSpace(active.WorkspaceRoot) != "" {
		return active.WorkspaceRoot
	}
	return filepath.Join(strings.TrimSpace(s.Home), "workspace")
}

func (s *Server) handlePrimaryAgents(w http.ResponseWriter, r *http.Request) {
	resolver, err := s.primaryAgentResolver()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	active, err := resolver.Active()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeAgentsJSON(w, primaryAgentsResponse{ActiveID: active.ID, Records: resolver.All()})
}

func (s *Server) handlePrimaryAgentSwitch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AgentID string `json:"agent_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	resolver, err := s.primaryAgentResolver()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	active, err := resolver.Switch(req.AgentID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// A failed rebind means the new agent's isolation boundary was never
	// established, so the request fails instead of serving turns against a
	// half-switched runtime.
	if err := s.applyPrimaryAgent(active); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeAgentsJSON(w, primaryAgentsResponse{ActiveID: active.ID, Records: resolver.All()})
}

func (s *Server) handleAgentRoster(w http.ResponseWriter, r *http.Request) {
	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	roster, err := s.activeAgentRoster(r.Context(), sessionID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	writeAgentsJSON(w, roster)
}

func (s *Server) handlePrimaryAgentCancel(w http.ResponseWriter, r *http.Request) {
	agentID := strings.TrimSpace(ParamsFromContext(r.Context()).ByName("agentId"))
	cancelled, err := s.cancelPrimaryAgent(r.Context(), agentID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	writeAgentsJSON(w, map[string]any{"ok": true, "cancelled": cancelled})
}

func (s *Server) handleSubagentCancel(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(ParamsFromContext(r.Context()).ByName("id"))
	cancelled, err := s.cancelSubagent(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	writeAgentsJSON(w, map[string]any{"ok": true, "cancelled": cancelled})
}

func (s *Server) handleAgentCancelAll(w http.ResponseWriter, r *http.Request) {
	summary, err := s.cancelAllAgents(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	writeAgentsJSON(w, map[string]any{
		"ok":        true,
		"main":      summary.Main,
		"subagents": summary.Subagents,
	})
}

// applyPrimaryAgent rebinds the gateway to a primary agent. The isolation
// boundary itself is rebuilt by the shared routine, so the terminal and the web
// cannot drift; only gateway-owned state (upload root, tree cache, subscriber
// notification) is handled here.
func (s *Server) applyPrimaryAgent(active config.Summary) error {
	if s == nil {
		return nil
	}
	wsTreeCache.mu.Lock()
	wsTreeCache.records = nil
	wsTreeCache.expires = time.Time{}
	wsTreeCache.mu.Unlock()

	if s.Runner != nil && s.MemoryStore != nil {
		s.Runner.MemoryStore = s.MemoryStore
	}
	if err := process.ApplyAgent(s.agentSwitchDeps(), active); err != nil {
		return err
	}
	// The previous agent's bots must stop receiving before this one starts:
	// a channel that outlived the switch would deliver its owner's messages
	// into the agent that just became active.
	s.bindChannelsTo(context.Background(), active)
	// Standing work belongs to the tenant too: the previous agent's cron jobs
	// and heartbeats stop with it, and the new agent's start.
	s.bindSchedulerTo(context.Background(), active)

	if s.Notifier == nil {
		return nil
	}
	now := time.Now()
	s.Notifier.PublishAll(event.TaskEvent{
		EventKind: event.EventProgress,
		Task: event.Task{
			ID:        "primary-agent:" + active.ID,
			Kind:      event.KindWorkItem,
			State:     event.StateDone,
			Title:     "Primary agent switched",
			Result:    active.ID,
			CreatedAt: now,
			UpdatedAt: now,
		},
		Message: "primary agent switched: " + active.ID,
	})
	return nil
}

// agentSwitchDeps binds the gateway's runtime to the shared switch routine.
// Files.WorkspaceRoot rides along so uploads land inside the active agent's
// allowed roots, which is the gateway's own half of the same boundary.
func (s *Server) agentSwitchDeps() process.AgentDeps {
	if s == nil {
		return process.AgentDeps{}
	}
	deps := process.AgentDeps{
		Home:   strings.TrimSpace(s.Home),
		Cfg:    s.liveCfg(),
		Runner: s.Runner,
	}
	if s.Env != nil {
		deps.Sandbox = s.Env.Sandbox
	}
	deps.OnApplied = func(active config.Summary) {
		if s.Files != nil {
			s.Files.WorkspaceRoot = strings.TrimSpace(active.WorkspaceRoot)
		}
		// Every pooled runner belongs to the previous tenant's stores and
		// consent records, so the switch tears them down; the next project
		// session builds its own under the new agent.
		if s.Env != nil {
			if pool := s.Env.RunnerPool(); pool != nil {
				pool.RebindAgent(active.ID)
			}
		}
		if s.Projects != nil {
			s.Projects = nil // rebuilt lazily for the new tenant
		}
	}
	return deps
}

func (s *Server) activeAgentRoster(ctx context.Context, sessionID string) (config.Roster, error) {
	active, err := s.activePrimarySummary()
	if err != nil {
		return config.Roster{}, err
	}
	sessionID = strings.TrimSpace(sessionID)
	// The primary agent's row says whether it is working, as the terminal's
	// does, not that it is the agent in force — every row here belongs to it.
	rows := []config.RosterRow{{
		ID:        active.ID,
		Kind:      "primary",
		Label:     active.ID,
		Status:    "idle",
		SessionID: sessionID,
	}}
	if latest := s.latestPrimaryRun(ctx, sessionID); latest.ID != "" {
		rows[0].RunID = latest.ID
		// A run parked on an approval is still the agent at work.
		if latest.Status == state.RunStatusRunning || latest.Status == state.RunStatusWaitingAction {
			rows[0].Status = string(state.RunStatusRunning)
		}
		rows[0].Task = latest.InputText
		rows[0].TokenCount = latest.UsagePromptTokens + latest.UsageCompletionTokens
		rows[0].ElapsedSeconds = elapsedSeconds(latest.CreatedAt, latest.UpdatedAt)
	}

	seen := map[string]int{rosterKey(rows[0]): 0}
	if s != nil && s.Sessions != nil && sessionID != "" {
		children, err := s.Sessions.ListChildSessionsRecent(ctx, sessionID, 50)
		if err != nil {
			return config.Roster{}, err
		}
		for _, child := range children {
			row := config.RosterRow{
				ID:        child.ID,
				Kind:      "subagent",
				ParentID:  sessionID,
				Label:     child.Title,
				Status:    "available",
				SessionID: child.ID,
			}
			if strings.TrimSpace(row.Label) == "" {
				row.Label = child.ID
			}
			rows = appendOrReplaceRosterRow(rows, seen, row, false)
		}
	}

	for _, entry := range agent.RegistryFor(s.stateRoot()).List(agent.Query{SessionID: sessionID, Limit: 200}) {
		row := subagentRosterRow(entry)
		if row.ID == "" {
			continue
		}
		rows = appendOrReplaceRosterRow(rows, seen, row, true)
	}
	return config.Roster{Records: rows}, nil
}

func (s *Server) cancelPrimaryAgent(ctx context.Context, agentID string) (bool, error) {
	active, err := s.activePrimarySummary()
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(agentID) != active.ID {
		return false, nil
	}
	run, err := s.findCancellablePrimaryRun(ctx)
	if err != nil {
		return false, err
	}
	if run.ID == "" {
		return false, nil
	}
	s.runController().Cancel(run.ID, context.Canceled)
	turn.FinalizeCancel(ctx, s.RunRT, run.ID)
	return true, nil
}

func (s *Server) cancelSubagent(id string) (bool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return false, nil
	}
	reg := agent.RegistryFor(s.stateRoot())
	if reg.Cancel(agent.Query{TaskID: id}) {
		return true, nil
	}
	if reg.Cancel(agent.Query{RunID: id}) {
		return true, nil
	}
	for _, entry := range reg.List(agent.Query{Limit: 500}) {
		if entry.AgentID == id || entry.TaskID == id || entry.RunID == id {
			switch {
			case entry.TaskID != "":
				return reg.Cancel(agent.Query{TaskID: entry.TaskID}), nil
			case entry.RunID != "":
				return reg.Cancel(agent.Query{RunID: entry.RunID}), nil
			}
		}
	}
	return false, nil
}

func (s *Server) cancelAllAgents(ctx context.Context) (config.CancelSummary, error) {
	out := config.CancelSummary{}
	active, err := s.activePrimarySummary()
	if err == nil {
		if cancelled, err := s.cancelPrimaryAgent(ctx, active.ID); err != nil {
			return out, err
		} else if cancelled {
			out.Main = 1
		}
	}
	for _, entry := range agent.RegistryFor(s.stateRoot()).List(agent.Query{Limit: 500}) {
		id := entry.AgentID
		if id == "" {
			id = entry.TaskID
		}
		if id == "" {
			id = entry.RunID
		}
		if id == "" {
			continue
		}
		cancelled, err := s.cancelSubagent(id)
		if err != nil {
			return out, err
		}
		if cancelled {
			out.Subagents++
		}
	}
	return out, nil
}

func writeAgentsJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("agents API: JSON encode error: %v", err)
	}
}

func (s *Server) latestPrimaryRun(ctx context.Context, sessionID string) state.Run {
	if s == nil || s.RunRT == nil || strings.TrimSpace(sessionID) == "" {
		return state.Run{}
	}
	runs, err := s.RunRT.ListRunsBySession(ctx, sessionID, 1)
	if err != nil || len(runs) == 0 {
		return state.Run{}
	}
	return runs[0]
}

func (s *Server) findCancellablePrimaryRun(ctx context.Context) (state.Run, error) {
	if s == nil || s.RunRT == nil {
		return state.Run{}, nil
	}
	agentID := ""
	if active, err := s.activePrimarySummary(); err == nil {
		agentID = strings.TrimSpace(active.ID)
	}
	run, ok, err := s.RunRT.ActivePrimaryRun(ctx, agentID)
	if err != nil || !ok {
		return state.Run{}, err
	}
	return run, nil
}

func subagentRosterRow(entry agent.HistoryEntry) config.RosterRow {
	id := strings.TrimSpace(entry.AgentID)
	if id == "" {
		id = strings.TrimSpace(entry.TaskID)
	}
	if id == "" {
		id = strings.TrimSpace(entry.RunID)
	}
	label := strings.TrimSpace(entry.AgentType)
	if label == "" {
		label = strings.TrimSpace(entry.AgentKind)
	}
	if label == "" {
		label = id
	}
	status := string(entry.Status)
	if strings.TrimSpace(status) == "" {
		status = "running"
	}
	return config.RosterRow{
		ID:             id,
		Kind:           "subagent",
		Label:          label,
		Status:         status,
		Title:          strings.TrimSpace(entry.Title),
		Task:           strings.TrimSpace(entry.Task),
		SessionID:      strings.TrimSpace(entry.WorkerSessionID),
		RunID:          strings.TrimSpace(entry.RunID),
		ElapsedSeconds: elapsedSeconds(entry.StartedAt, entry.UpdatedAt),
	}
}

func appendOrReplaceRosterRow(rows []config.RosterRow, seen map[string]int, row config.RosterRow, replace bool) []config.RosterRow {
	key := rosterKey(row)
	if key == "" {
		return rows
	}
	if idx, ok := seen[key]; ok {
		if replace {
			rows[idx] = row
		}
		return rows
	}
	seen[key] = len(rows)
	return append(rows, row)
}

func rosterKey(row config.RosterRow) string {
	switch {
	case row.RunID != "":
		return "run:" + row.RunID
	case row.SessionID != "":
		return "session:" + row.SessionID
	case row.ID != "":
		return row.Kind + ":" + row.ID
	default:
		return ""
	}
}

func elapsedSeconds(start, updated int64) int {
	if start <= 0 {
		return 0
	}
	if updated <= 0 {
		updated = time.Now().Unix()
	}
	if updated < start {
		return 0
	}
	return int(updated - start)
}
