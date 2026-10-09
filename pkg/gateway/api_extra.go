package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	"github.com/forebrain-harness/forebrain-harness/pkg/assembly"
	"github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/memory"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/skill"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
	"github.com/joho/godotenv"
)

func (s *Server) AttachExtraRoutes(routes Routes) {
	if s != nil {
		s.approvalRecovery.Do(func() { go s.recoverResolvedApprovalWaits() })
	}
	api := routes.Group("/api")
	api.Post("/auth/session", s.handleWebSessionCreate)

	rules := api.Group("/rules")
	rules.Get("/agent", s.handleAgentRuleFiles)
	rules.Get("/agent/:name", s.handleAgentRuleFile)
	rules.Put("/agent/:name", s.handleAgentRuleFile)
	rules.Get("/project/:projectId", s.handleProjectRuleFiles)
	rules.Get("/project/:projectId/file", s.handleProjectRuleFile)
	rules.Put("/project/:projectId/file", s.handleProjectRuleFile)
	api.Get("/auth/session", s.handleWebSessionProbe)
	api.Get("/tools", s.handleToolsList)
	api.Get("/models", s.handleModelsList)

	permissions := api.Group("/permissions")
	permissions.Get("/approval-default", s.handleApprovalDefaultGet)
	permissions.Put("/approval-default", s.handleApprovalDefaultPut)
	permissions.Get("/session-preset", s.handleSessionPresetGet)
	permissions.Post("/session-preset", s.handleSessionPreset)
	permissions.Get("/rules", s.handlePermissionRules)
	permissions.Post("/evaluate", s.handlePermissionEvaluate)
	permissions.Get("/explain", s.handlePermissionExplain)
	permissions.Post("/updates", s.handlePermissionUpdate)

	api.Get("/slash/commands", s.handleSlashCommands)
	api.Get("/memories/settings", s.handleMemoriesSettings)
	api.Post("/memories/settings", s.handleMemoriesSettings)
	api.Post("/memories/reset", s.handleMemoriesReset)
	api.Get("/memories/files", s.handleMemoriesFilesList)
	api.Get("/memories/file", s.handleMemoriesFileRead)
	api.Put("/memories/file", s.handleMemoriesFileWrite)
	api.Post("/memories/files/delete", s.handleMemoriesFilesDelete)

	agents := api.Group("/agents")
	agents.Get("/primary", s.handlePrimaryAgents)
	agents.Post("/primary", s.handlePrimaryAgentCreate)
	agents.Put("/primary/:id", s.handlePrimaryAgentUpdate)
	agents.Delete("/primary/:id", s.handlePrimaryAgentDelete)
	agents.Post("/primary/switch", s.handlePrimaryAgentSwitch)
	agents.Get("/roster", s.handleAgentRoster)
	agents.Post("/cancel-all", s.handleAgentCancelAll)
	agents.Post("/:agentId/cancel", s.handlePrimaryAgentCancel)
	api.Post("/subagents/:id/cancel", s.handleSubagentCancel)

	skills := api.Group("/skills")
	skills.Get("/", s.handleSkillsList)
	skills.Post("/", s.handleSkillsCreate)
	skills.Post("/install", s.handleSkillsInstall)
	skills.Post("/install/upload", s.handleSkillsInstallUpload)
	skills.Get("/:name", s.handleSkillGet)
	skills.Put("/:name", s.handleSkillsUpdate)
	skills.Post("/toggle", s.handleSkillsToggle)
	skills.Get("/:name/download", s.handleSkillDownload)
	skills.Get("/:name/files", s.handleSkillFilesList)
	skills.Get("/:name/file", s.handleSkillFileRead)
	skills.Put("/:name/file", s.handleSkillFileWrite)
	skills.Post("/download", s.handleSkillsDownloadBatch)
	skills.Delete("/:name", s.handleSkillDelete)

	workspace := api.Group("/workspace")
	workspace.Get("/tree", s.handleWorkspaceTree)
	workspace.Get("/snippet", s.handleWorkspaceSnippet)
	workspace.Get("/mentions", s.handleMentionSearch)
	workspace.Post("/mentions/accept", s.handleMentionAccept)

	chatSessions := api.Group("/chat/sessions")
	chatSessions.Get("/", s.handleChatSessions)
	chatSessions.Post("/", s.handleChatSessionCreate)
	chatSessions.Post("/:id/title", s.handleChatSessionTitle)
	chatSessions.Get("/:id/messages", s.handleChatMessages)
	chatSessions.Get("/:id/events", s.handleChatSessionEvents)
	chatSessions.Get("/:id/context", s.handleChatSessionContext)
	chatSessions.Get("/:id/tool-audit", s.handleSessionToolAudit)
	chatSessions.Get("/:id/cost-summary", s.handleSessionCostSummary)
	chatSessions.Get("/:id/subagent-history", s.handleSessionSubagentHistory)
	chatSessions.Get("/:id/todos", s.handleSessionTodos)
	chatSessions.Get("/:id/plan-md", s.handleSessionPlanMarkdown)
	chatSessions.Get("/:id/approval-request", s.handleSessionApprovalRequest)
	chatSessions.Get("/:id/mode", s.handleSessionMode)
	chatSessions.Get("/:id", s.handleChatSession)
	chatSessions.Post("/:id/compact", s.handleSessionCompact)
	chatSessions.Post("/:id/rewind-last", s.handleSessionRewindLast)
	// A subagent's own view talks to that subagent through these — the same
	// semantics the terminal's subagent view has (plan 007), decided in the
	// engine (plan 005): send, recall, Esc's two meanings, compact, context,
	// and the budget it opens with.
	chatSessions.Post("/:id/subagents/:agent/input", s.handleSubagentInput)
	chatSessions.Post("/:id/subagents/:agent/queued-input", s.handleSubagentQueuedInput)
	chatSessions.Post("/:id/subagents/:agent/interrupt-send", s.handleSubagentInterruptSend)
	chatSessions.Post("/:id/subagents/:agent/withdraw", s.handleSubagentWithdraw)
	chatSessions.Post("/:id/subagents/:agent/compact", s.handleSubagentCompact)
	chatSessions.Get("/:id/subagents/:agent/context", s.handleSubagentContext)
	chatSessions.Get("/:id/subagents/:agent/budget", s.handleSubagentBudget)

	api.Post("/internal/worker-event", s.handleWorkerInternalEvent)

	actions := api.Group("/actions")
	actions.Get("/", s.handleActions)
	actions.Post("/ask", s.handleActionsAsk)
	actions.Post("/:id/answer", s.handleActionsAnswer)
	actions.Post("/:id/approve", s.handleActionsApprove)
	actions.Post("/:id/deny", s.handleActionsDeny)
	actions.Post("/:id/plan-review", s.handleActionPlanReview)

	projects := api.Group("/v1/projects")
	projects.Get("/", s.handleProjectsList)
	projects.Post("/", s.handleProjectsCreate)
	projects.Get("/:id", s.handleProjectOne)
	projects.Patch("/:id", s.handleProjectOne)
	projects.Delete("/:id", s.handleProjectDelete)
	projects.Post("/:id/pin", s.handleProjectPin)
	projects.Post("/:id/archive", s.handleProjectArchive)
	projects.Get("/:id/sessions", s.handleProjectSessionsList)
	projects.Post("/:id/sessions", s.handleProjectSessionsCreate)
	projects.Get("/:id/mcp", s.handleProjectMCPPreview)
	projects.Post("/:id/mcp/consent", s.handleProjectMCPConsent)
	projects.Get("/:id/lsp", s.handleProjectLSPPreview)
	projects.Post("/:id/lsp/consent", s.handleProjectLSPConsent)

	// Project-scoped skill lifecycle: /projects/:id/skills mirrors /skills
	// operation for operation, scoped to the route's project.
	projects.Get("/:id/skills", s.handleProjectSkillsList)
	projects.Post("/:id/skills", s.handleProjectSkillsCreate)
	projects.Post("/:id/skills/install", s.handleProjectSkillsInstall)
	projects.Post("/:id/skills/install/upload", s.handleProjectSkillsInstallUpload)
	projects.Get("/:id/skills/:name", s.handleProjectSkillGet)
	projects.Put("/:id/skills/:name", s.handleProjectSkillsUpdate)
	projects.Post("/:id/skills/toggle", s.handleProjectSkillsToggle)
	projects.Get("/:id/skills/:name/download", s.handleProjectSkillsDownload)
	projects.Post("/:id/skills/download", s.handleProjectSkillsDownloadBatch)
	projects.Delete("/:id/skills/:name", s.handleProjectSkillsDelete)
	projects.Get("/:id/permissions/rules", s.handleProjectPermissionRules)
	projects.Post("/:id/permissions/updates", s.handleProjectPermissionUpdate)
	projects.Get("/:id/permissions/explain", s.handleProjectPermissionExplain)

	mcp := api.Group("/v1/mcp")
	mcp.Get("/servers", s.handleMCPServersV1)
	mcp.Post("/servers/disable", s.handleMCPServerDisableV1)
	mcp.Post("/servers/enable", s.handleMCPServerEnableV1)
	mcp.Post("/oauth/pkce/start", s.handleMCPOAuthPKCEStart)
	mcp.Post("/oauth/pkce/finish", s.handleMCPOAuthPKCEFinish)

	lsp := api.Group("/v1/lsp")
	lsp.Get("/", s.handleLSPSnapshot)
	lsp.Post("/servers/enable", s.handleLSPServerEnable)
	lsp.Post("/servers/disable", s.handleLSPServerDisable)
	lsp.Post("/servers/restart", s.handleLSPServerRestart)
	lsp.Post("/servers/install", s.handleLSPServerInstall)
	lsp.Post("/recommendations/reset", s.handleLSPRecommendationsReset)
	lsp.Post("/recommendations/:id/decision", s.handleLSPRecommendationDecision)

	files := api.Group("/files")
	files.Post("/", s.handleFiles)
	files.Get("/:id", s.handleFileOne)
	files.Get("/:id/download", s.handleFileDownload)
	files.Post("/:id/parse", s.handleFileParse)
	files.Get("/:id/text", s.handleFileText)

	cron := api.Group("/cron")
	cron.Get("/", s.handleCronJobs)
	cron.Post("/preview", s.handleCronPreview)
	cron.Post("/", s.handleCronJobs)
	cron.Get("/:id", s.handleCronJob)
	cron.Put("/:id", s.handleCronJob)
	cron.Delete("/:id", s.handleCronJob)
	cron.Post("/:id/run", s.handleCronJobRun)
	cron.Get("/:id/runs", s.handleCronJobRuns)

	api.Get("/heartbeat", s.handleHeartbeat)
	api.Put("/heartbeat", s.handleHeartbeat)
	api.Delete("/heartbeat", s.handleHeartbeat)

	api.Get("/auto-continue", s.handleAutoContinue)
	api.Delete("/auto-continue", s.handleAutoContinue)

	api.Get("/config", s.handleConfigFile)
	api.Put("/config", s.handleConfigFile)
	api.Get("/channels", s.handleChannels)
	api.Put("/channels", s.handleChannels)
	api.Get("/providers", s.handleProviders)
	api.Put("/providers", s.handleProviders)
	api.Get("/hooks", s.handleHooks)
	api.Put("/hooks", s.handleHooks)
	api.Get("/cron-settings", s.handleCronSettings)
	api.Put("/cron-settings", s.handleCronSettings)

	runs := api.Group("/runs")
	runs.Post("/:id/cancel", s.handleRunCancel)
	runs.Post("/:id/input", s.handleRunInput)
	runs.Post("/:id/queued-input", s.handleRunQueuedInput)
	runs.Get("/:id/events", s.handleRunEvents)
	runs.Get("/:id/subagents", s.handleRunSubagents)
}

func (s *Server) recoverResolvedApprovalWaits() {
	if s == nil || s.RunRT == nil || s.Actions == nil || s.Runner == nil {
		return
	}
	s.recoverResolvedApprovalWaitsOnce()
	// A crashed process may have refreshed its lease immediately before exit.
	// One delayed pass reaches those rows once their fence expires; a genuinely
	// live owner keeps heartbeating and remains protected.
	time.AfterFunc(state.WaitResumeLease+250*time.Millisecond, s.recoverResolvedApprovalWaitsOnce)
}

func (s *Server) recoverResolvedApprovalWaitsOnce() {
	if s == nil || s.RunRT == nil || s.Actions == nil || s.Runner == nil {
		return
	}
	// Every session, deliberately: this server speaks for all of them and
	// publishes each report onto the event stream of the session that owns it,
	// so a client only ever sees what belongs to the conversation it is
	// watching. A surface bound to one conversation scopes its own scan instead.
	ids, err := s.RunRT.ListResolvedWaitActionIDs(context.Background(), "", 0)
	if err != nil {
		slog.Warn("approval recovery scan failed", "err", err)
		return
	}
	uncertain, uncertainErr := s.RunRT.ListUncertainResolvedWaits(context.Background(), "", 0)
	if uncertainErr != nil {
		slog.Warn("uncertain approval recovery scan failed", "err", uncertainErr)
	} else {
		for _, wait := range uncertain {
			s.interruptUncertainGatewayApproval(wait)
		}
	}
	for _, actionID := range ids {
		s.resumeGatewayRun(actionID, false)
	}
}

func (s *Server) interruptUncertainGatewayApproval(wait state.Wait) {
	if s == nil || s.RunRT == nil || strings.TrimSpace(wait.RunID) == "" {
		return
	}
	const reason = "approval continuation was interrupted after execution began; tool outcome is uncertain and was not retried"
	ctx := context.Background()
	if err := s.RunRT.MarkWaitResumeUncertain(ctx, wait.RunID, wait.ActionID); err != nil {
		return
	}
	runRecord, _ := s.RunRT.GetRun(ctx, wait.RunID)
	sessionID := ""
	if runRecord != nil {
		sessionID = runRecord.SessionID
	}
	s.runController().Cancel(wait.RunID, errors.New(reason))
	s.finishRun(ctx, sessionID, wait.RunID, state.RunStatusFailed)
	_ = s.publishGatewayRunEvent(ctx, sessionID, wait.RunID, event.RunEventTurnError, event.TurnErrorPayload{Error: reason, Message: reason})
	if strings.TrimSpace(wait.AgentID) != "" {
		parentRunID := ""
		if runRecord != nil {
			parentRunID = runRecord.ParentRunID
		}
		_ = s.RunEvents().Publish(ctx, event.NewRunEvent(
			"approval-uncertain:"+wait.ActionID, wait.RunID, sessionID, event.RunEventSubagentEnded,
			event.SubagentEndedPayload{AgentID: wait.AgentID, AgentType: wait.SubagentType, TaskID: wait.AgentID, Status: "interrupted", Error: reason, ParentRunID: parentRunID, ExecutionID: wait.RunID}, time.Now(),
		))
	}
}

// resolveWorkspacePath pins one workspace-relative path to the active
// primary agent's workspace, resolving symlinks, and rejects anything that
// lands outside it. The gateway's workspace endpoints are one workspace's
// own file view; a link pointing elsewhere is not part of that view.
func (s *Server) resolveWorkspacePath(rel string) (root, full string, err error) {
	root = s.activeWorkspaceRoot()
	joined := filepath.Join(root, filepath.FromSlash(rel))
	resolved, resolveErr := tool.ResolveWithinRoots(joined, []string{root})
	if resolveErr != nil {
		if errors.Is(resolveErr, tool.ErrPathNotAllowed) {
			return root, "", tool.ErrPathNotAllowed
		}
		return root, "", resolveErr
	}
	return root, resolved, nil
}

func (s *Server) handleWorkspaceTree(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	root, full, err := s.resolveWorkspacePath(strings.TrimSpace(r.URL.Query().Get("path")))
	if err != nil {
		http.Error(w, "path is outside the workspace", http.StatusBadRequest)
		return
	}
	st, statErr := os.Stat(full)
	if statErr != nil {
		http.Error(w, statErr.Error(), http.StatusNotFound)
		return
	}
	if !st.IsDir() {
		http.Error(w, "not a directory", http.StatusBadRequest)
		return
	}
	entries, readErr := os.ReadDir(full)
	if readErr != nil {
		http.Error(w, readErr.Error(), http.StatusInternalServerError)
		return
	}
	type row struct {
		Name  string `json:"name"`
		Path  string `json:"path"`
		IsDir bool   `json:"is_dir"`
	}
	out := make([]row, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if name == ".git" {
			continue
		}
		entryPath := filepath.Join(full, name)
		info, infoErr := os.Stat(entryPath)
		if infoErr != nil {
			// Broken link or unreadable entry: not something the tree can
			// open, so it is not listed.
			continue
		}
		resolved, resolveErr := tool.ResolveWithinRoots(entryPath, []string{root})
		if resolveErr != nil {
			continue
		}
		rel, relErr := filepath.Rel(root, resolved)
		if relErr != nil {
			continue
		}
		out = append(out, row{Name: name, Path: filepath.ToSlash(rel), IsDir: info.IsDir()})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		li, lj := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if li != lj {
			return li < lj
		}
		return out[i].Name < out[j].Name
	})
	relRoot, relErr := filepath.Rel(root, full)
	if relErr != nil {
		http.Error(w, relErr.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"path":    filepath.ToSlash(relRoot),
		"records": out,
	})
}

// modelsDiscoveryTimeout bounds one ChatGPT model discovery inside a /models
// request. The listing is one HTTP call on already-loaded credentials; a
// stuck upstream must not hold the HTTP request open with it.
const modelsDiscoveryTimeout = 10 * time.Second

func (s *Server) handleModelsList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	limit := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}
	q := turn.ModelCatalogQuery{
		Query:    strings.TrimSpace(r.URL.Query().Get("q")),
		Provider: strings.TrimSpace(r.URL.Query().Get("provider")),
		Limit:    limit,
	}
	ctx, cancel := context.WithTimeout(r.Context(), modelsDiscoveryTimeout)
	defer cancel()
	var listing turn.ModelCatalogListing
	if s != nil && s.Core != nil {
		listing = s.Core.ListModelCatalogLive(ctx, q)
	} else {
		listing = turn.ListModelCatalogLive(ctx, nil, q)
	}
	if listing.Records == nil {
		listing.Records = []turn.ModelRecord{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"records": listing.Records,
		"status":  listing.Status,
	})
}

func (s *Server) handleSlashCommands(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	surface, err := slashDiscoverySurfaceForChannel(r.URL.Query().Get("surface"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	opts := turn.DiscoveryOptions{
		DuringRun:        parseBoolQuery(r, "during_run"),
		SideConversation: parseBoolQuery(r, "side"),
		// The list a subagent's own view offers: the commands that act on the
		// conversation are dropped (plan 007's D4 classification).
		SubagentView: strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("view")), "subagent"),
	}
	type slashOptionsPayload struct {
		DuringRun        bool `json:"during_run"`
		SideConversation bool `json:"side_conversation"`
	}
	var records []turn.SlashCommandRecord
	if s != nil && s.Core != nil {
		records = s.Core.ListSlashCommands(surface, query, opts)
	} else {
		records = turn.ListSlashCommands(surface, query, opts)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"surface": surface,
		"query":   query,
		"options": slashOptionsPayload{
			DuringRun:        opts.DuringRun,
			SideConversation: opts.SideConversation,
		},
		"records": records,
	})
}

func (s *Server) handleWorkspaceSnippet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	rel := strings.TrimSpace(r.URL.Query().Get("path"))
	if rel == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}
	_, full, resolveErr := s.resolveWorkspacePath(rel)
	if resolveErr != nil {
		http.Error(w, "path is outside the workspace", http.StatusBadRequest)
		return
	}
	raw, err := os.ReadFile(full)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	start, _ := strconv.Atoi(r.URL.Query().Get("start"))
	end, _ := strconv.Atoi(r.URL.Query().Get("end"))
	if start <= 0 {
		start = 1
	}
	if end <= 0 || end > len(lines) {
		end = minInt(len(lines), start+200)
	}
	if start > end {
		start = 1
	}
	sel := lines[start-1 : end]
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"path":    rel,
		"start":   start,
		"end":     end,
		"content": strings.Join(sel, "\n"),
	})
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func parseBoolQuery(r *http.Request, key string) bool {
	raw := strings.TrimSpace(strings.ToLower(r.URL.Query().Get(key)))
	switch raw {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func (s *Server) handleToolsList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	if s.Runner == nil || s.Env.Tools() == nil {
		_ = json.NewEncoder(w).Encode([]any{})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.Env.Tools().ToolMetas())
}

// permissionRuleRow is one rule as the permission pages list it.
type permissionRuleRow struct {
	Source      safety.PermissionSource   `json:"source"`
	Behavior    safety.PermissionBehavior `json:"behavior"`
	ToolName    string                    `json:"tool_name"`
	RuleContent string                    `json:"rule_content,omitempty"`
	// Rule is the rule exactly as stored. RuleContent is its display text,
	// which for a command prefix or a literal command is not a field of the
	// rule at all, so removing a listed rule sends this back, never the text.
	Rule safety.PermissionRuleValue `json:"rule"`
}

// permissionRuleRows flattens a snapshot's rules into listing rows, filtered
// by source and behavior (empty matches all) and in one stable order.
func permissionRuleRows(snap safety.Snapshot, srcFilter, behaviorFilter string) []permissionRuleRow {
	rules := make([]permissionRuleRow, 0, 64)
	for src, byBehavior := range snap.Rules {
		if srcFilter != "" && !strings.EqualFold(string(src), srcFilter) {
			continue
		}
		for behavior, list := range byBehavior {
			if behaviorFilter != "" && !strings.EqualFold(string(behavior), behaviorFilter) {
				continue
			}
			for _, it := range list {
				rules = append(rules, permissionRuleRow{
					Source:   src,
					Behavior: behavior,
					ToolName: it.ToolName,
					// A rule's text can live in any of three fields; the engine
					// owns which one names it, so a prefix or a literal command
					// is not shown as a bare tool name here.
					RuleContent: safety.RuleContentDisplay(it),
					Rule:        it,
				})
			}
		}
	}
	sort.SliceStable(rules, func(i, j int) bool {
		if rules[i].Source != rules[j].Source {
			return rules[i].Source < rules[j].Source
		}
		if rules[i].Behavior != rules[j].Behavior {
			return rules[i].Behavior < rules[j].Behavior
		}
		if !strings.EqualFold(rules[i].ToolName, rules[j].ToolName) {
			return strings.ToLower(rules[i].ToolName) < strings.ToLower(rules[j].ToolName)
		}
		return rules[i].RuleContent < rules[j].RuleContent
	})
	return rules
}

func (s *Server) handlePermissionRules(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	srcFilter := strings.TrimSpace(r.URL.Query().Get("source"))
	behaviorFilter := strings.TrimSpace(r.URL.Query().Get("behavior"))
	var snap safety.Snapshot
	if sid := strings.TrimSpace(r.URL.Query().Get("session_id")); sid != "" {
		runner := s.runnerFor(r.Context(), sid)
		if runner == nil {
			http.Error(w, "permissions unavailable", http.StatusServiceUnavailable)
			return
		}
		snap = runner.PermissionSnapshotForSession(sid)
	} else {
		scope, ok := s.agentPermissionScope()
		if !ok {
			http.Error(w, "permissions unavailable", http.StatusServiceUnavailable)
			return
		}
		snap = scope.rt.Snapshot(scope.cfg)
	}
	rules := permissionRuleRows(snap, srcFilter, behaviorFilter)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"mode":  snap.Mode,
		"rules": rules,
	})
}

func (s *Server) handlePermissionEvaluate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		ToolName  string `json:"tool_name"`
		Input     string `json:"input"`
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, "json", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.ToolName) == "" {
		http.Error(w, "tool_name required", http.StatusBadRequest)
		return
	}
	var d safety.Decision
	if sid := strings.TrimSpace(body.SessionID); sid != "" {
		runner := s.runnerFor(r.Context(), sid)
		if runner == nil {
			http.Error(w, "permissions unavailable", http.StatusServiceUnavailable)
			return
		}
		d = runner.EvaluatePermissionForSession(sid, body.ToolName, body.Input)
	} else {
		scope, ok := s.agentPermissionScope()
		if !ok {
			http.Error(w, "permissions unavailable", http.StatusServiceUnavailable)
			return
		}
		d = scope.rt.Evaluate("", body.ToolName, body.Input, scope.cfg, safety.RuntimeYOLOEnabled())
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(d)
}

func (s *Server) handlePermissionExplain(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	toolName := strings.TrimSpace(r.URL.Query().Get("tool_name"))
	input := strings.TrimSpace(r.URL.Query().Get("input"))
	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	if toolName == "" {
		http.Error(w, "tool_name required", http.StatusBadRequest)
		return
	}
	var ex safety.ExplainResult
	if sessionID != "" {
		runner := s.runnerFor(r.Context(), sessionID)
		if runner == nil {
			http.Error(w, "permissions unavailable", http.StatusServiceUnavailable)
			return
		}
		ex = runner.ExplainPermissionForSession(sessionID, toolName, input)
	} else {
		scope, ok := s.agentPermissionScope()
		if !ok {
			http.Error(w, "permissions unavailable", http.StatusServiceUnavailable)
			return
		}
		ex = scope.rt.Explain("", toolName, input, scope.cfg, safety.RuntimeYOLOEnabled())
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(ex)
}

// handlePermissionUpdate changes either one conversation's own permissions —
// in the runner that conversation runs on — or the active primary agent's
// local rules, which every live runner of the agent then holds too. A
// project's rules are the project space's to change
// (handleProjectPermissionUpdate); this endpoint is not bound to any project,
// least of all the one the gateway process was launched in.
func (s *Server) handlePermissionUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	var u safety.PermissionUpdate
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&u); err != nil {
		http.Error(w, "json", http.StatusBadRequest)
		return
	}
	if u.Type == "" {
		http.Error(w, "type required", http.StatusBadRequest)
		return
	}
	if u.Destination == "" {
		u.Destination = safety.DestinationSession
	}
	// Refuse an update that would never take effect instead of reporting
	// success for a rule the store will drop.
	if refusal := safety.ExplainRefusedUpdate(u.Destination, u.Behavior); refusal != "" {
		http.Error(w, refusal, http.StatusBadRequest)
		return
	}
	switch u.Destination {
	case safety.DestinationSession:
		sid := strings.TrimSpace(u.SessionID)
		if sid == "" {
			http.Error(w, "session_id required for session destination", http.StatusBadRequest)
			return
		}
		// The rule lands in the permission store of the conversation it
		// names, so that conversation must be this agent's own: another
		// agent's is answered as missing and its store is never written.
		owned, err := s.sessionOwned(r.Context(), sid)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !owned {
			http.NotFound(w, r)
			return
		}
		runner := s.runnerFor(r.Context(), sid)
		if runner == nil {
			http.Error(w, "permissions unavailable", http.StatusServiceUnavailable)
			return
		}
		runner.ApplyPermissionUpdate(u)
	case safety.DestinationLocalSettings:
		switch u.Type {
		case safety.UpdateAddRules, safety.UpdateReplaceRules, safety.UpdateRemoveRules:
		default:
			http.Error(w, "type must be addRules, replaceRules or removeRules", http.StatusBadRequest)
			return
		}
		scope, ok := s.agentPermissionScope()
		if !ok {
			http.Error(w, "permissions unavailable", http.StatusServiceUnavailable)
			return
		}
		scope.rt.ApplyUpdate(u, scope.cfg, scope.paths)
		for _, runner := range s.liveRunners() {
			runner.ApplyPermissionUpdate(u)
		}
	case safety.DestinationProjectSettings:
		http.Error(w, "project rules are changed in the project's own space", http.StatusBadRequest)
		return
	default:
		http.Error(w, "unsupported destination", http.StatusBadRequest)
		return
	}
	if s.Env != nil {
		s.Env.RefreshSandboxRuntime()
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

// permissionScope is a permission runtime loaded from exactly the files one
// scope's sessions read: the active primary agent's local settings, plus a
// project's own settings when the scope is a project.
type permissionScope struct {
	paths safety.Paths
	cfg   *config.Root
	rt    *safety.Runtime
}

func (s *Server) loadPermissionScope(projectRoot string) (permissionScope, bool) {
	cfg := s.liveCfg()
	if cfg == nil {
		return permissionScope{}, false
	}
	paths := safety.Paths{Home: strings.TrimSpace(s.Home), WorkspaceRoot: s.activeWorkspaceRoot(), ProjectRoot: projectRoot}
	rt := safety.NewRuntime()
	rt.LoadFromDisk(cfg, paths)
	return permissionScope{paths: paths, cfg: cfg, rt: rt}, true
}

// agentPermissionScope is the active primary agent's own permission state:
// its local rules and the configuration, with no project in it. An
// agent-level answer never comes from the runner of whichever project the
// gateway process happened to be launched in.
func (s *Server) agentPermissionScope() (permissionScope, bool) {
	return s.loadPermissionScope("")
}

// liveRunners is every runner the agent's conversations run on: the gateway's
// own and each project runner in the pool.
func (s *Server) liveRunners() []*run.Runner {
	var runners []*run.Runner
	if s.Runner != nil {
		runners = append(runners, s.Runner)
	}
	if s.Env != nil {
		if pool := s.Env.RunnerPool(); pool != nil {
			runners = append(runners, pool.Runners()...)
		}
	}
	return runners
}

func (s *Server) handleFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	if s.Files == nil {
		http.Error(w, "files disabled", http.StatusServiceUnavailable)
		return
	}
	maxBytes := s.Files.Cfg.MaxUploadBytes
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	if err := r.ParseMultipartForm(maxBytes); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// A file belongs to one of this agent's conversations: the row references
	// the session, and every read of it is filtered by the session's tenant.
	sessionID := strings.TrimSpace(r.FormValue("session_id"))
	if sessionID == "" {
		http.Error(w, "session_id is required to upload a file", http.StatusBadRequest)
		return
	}
	owned, ownershipErr := s.sessionOwned(r.Context(), sessionID)
	if ownershipErr != nil {
		http.Error(w, ownershipErr.Error(), http.StatusInternalServerError)
		return
	}
	if !owned {
		http.NotFound(w, r)
		return
	}
	fh, _, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "file required", http.StatusBadRequest)
		return
	}
	defer fh.Close()
	filename := "upload.bin"
	if mf := r.MultipartForm; mf != nil && mf.File != nil {
		if arr := mf.File["file"]; len(arr) > 0 && arr[0] != nil && strings.TrimSpace(arr[0].Filename) != "" {
			filename = arr[0].Filename
		}
	}
	mediaType := ""
	if mf := r.MultipartForm; mf != nil && mf.File != nil {
		if arr := mf.File["file"]; len(arr) > 0 && arr[0] != nil {
			mediaType = strings.TrimSpace(arr[0].Header.Get("Content-Type"))
		}
	}
	rec, err := s.Files.CreateFromReader(r.Context(), sessionID, filename, fh, 0, mediaType)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"file_id": rec.ID})
}

func (s *Server) handleFileOne(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	if s.Files == nil {
		http.Error(w, "files disabled", http.StatusServiceUnavailable)
		return
	}
	id := ParamsFromContext(r.Context()).ByName("id")
	if strings.TrimSpace(id) == "" {
		http.NotFound(w, r)
		return
	}
	f, err := s.Files.Get(r.Context(), s.Sessions.AgentID(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(f)
}

func (s *Server) handleFileDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	if s.Files == nil {
		http.Error(w, "files disabled", http.StatusServiceUnavailable)
		return
	}
	id := ParamsFromContext(r.Context()).ByName("id")
	f, err := s.Files.Get(r.Context(), s.Sessions.AgentID(), id)
	if err != nil || f == nil {
		http.NotFound(w, r)
		return
	}
	p, ok := s.Files.LocalPath(f)
	if !ok {
		if strings.EqualFold(f.StorageBackend, string(state.StorageBackendS3)) && strings.TrimSpace(f.StorageKey) != "" {
			rc, ct, _, _, err := s.Files.GetOriginalObject(r.Context(), f.StorageBucket, f.StorageKey)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			defer rc.Close()
			w.Header().Set("Content-Type", ct)
			w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", f.OriginalName))
			_, _ = io.Copy(w, rc)
			return
		}
		http.Error(w, "download not available for this storage backend", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", f.MediaType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", f.OriginalName))
	http.ServeFile(w, r, p)
}

func (s *Server) handleFileParse(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	if s.Files == nil {
		http.Error(w, "files disabled", http.StatusServiceUnavailable)
		return
	}
	id := ParamsFromContext(r.Context()).ByName("id")
	agentID := s.Sessions.AgentID()
	f, err := s.Files.Get(r.Context(), agentID, id)
	if err != nil || f == nil {
		http.NotFound(w, r)
		return
	}
	txt, parser, err := s.Files.EnsureParsedText(r.Context(), agentID, id)
	if err != nil {
		_ = s.Files.UpdateParseFailed(r.Context(), id, err.Error())
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "parser": parser, "text_bytes": len(txt)})
}

func (s *Server) handleFileText(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	if s.Files == nil {
		http.Error(w, "files disabled", http.StatusServiceUnavailable)
		return
	}
	id := ParamsFromContext(r.Context()).ByName("id")
	f, err := s.Files.Get(r.Context(), s.Sessions.AgentID(), id)
	if err != nil || f == nil {
		http.NotFound(w, r)
		return
	}
	p, ok := s.Files.ParsedTextLocalPath(f)
	if !ok {
		http.Error(w, "text not ready", http.StatusNotFound)
		return
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		http.Error(w, "text not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(raw)
}

func (s *Server) handleChatSessionCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Title  string `json:"title"`
		Source string `json:"source"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body)
	// The session-purpose whitelist stays here, at the API edge; the store
	// writes whatever it is handed.
	switch strings.TrimSpace(body.Source) {
	case state.SessionSourceConversation, state.SessionSourceWorkshop:
	default:
		http.Error(w, "source must be empty or workshop", http.StatusBadRequest)
		return
	}
	if s.Core != nil {
		res, err := s.Core.CreateSession(r.Context(), body.Title)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if src := strings.TrimSpace(body.Source); src != "" {
			if s.Sessions == nil {
				http.Error(w, "session source unavailable", http.StatusServiceUnavailable)
				return
			}
			if err := s.Sessions.SetSessionSource(r.Context(), res.ID, src); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(res)
		return
	}
	if s.Sessions == nil {
		http.Error(w, "sessions disabled", http.StatusServiceUnavailable)
		return
	}
	res, err := turn.New(turn.WithSessionStore(s.Sessions)).CreateSession(r.Context(), body.Title)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if src := strings.TrimSpace(body.Source); src != "" {
		if err := s.Sessions.SetSessionSource(r.Context(), res.ID, src); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(res)
}

func (s *Server) handleChatSessionTitle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	sid := strings.TrimSpace(ParamsFromContext(r.Context()).ByName("id"))
	if sid == "" {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, "json", http.StatusBadRequest)
		return
	}
	if s.Core != nil {
		if err := s.Core.RenameSession(r.Context(), sid, body.Title); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	} else if s.Sessions != nil {
		if err := turn.New(turn.WithSessionStore(s.Sessions)).RenameSession(r.Context(), sid, body.Title); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	} else {
		http.Error(w, "sessions disabled", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "id": sid, "title": strings.TrimSpace(body.Title)})
}

func (s *Server) handleChatSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	// The session-purpose whitelist stays here, at the API edge, the same
	// one the create endpoint applies.
	source := strings.TrimSpace(r.URL.Query().Get("source"))
	switch source {
	case state.SessionSourceConversation, state.SessionSourceWorkshop:
	default:
		http.Error(w, "source must be empty or workshop", http.StatusBadRequest)
		return
	}
	if s.Sessions == nil || s.Sessions.DB() == nil {
		_ = json.NewEncoder(w).Encode(map[string]any{"records": []any{}})
		return
	}
	summaries, err := s.Sessions.ListSessionsOfSource(r.Context(), source, 200)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	type row struct {
		ID         string `json:"id"`
		Title      string `json:"title"`
		CreateTime string `json:"create_time"`
		UpdateTime string `json:"update_time"`
		Source     string `json:"source,omitempty"`
	}
	out := make([]row, 0, len(summaries))
	for _, sum := range summaries {
		t := time.Unix(sum.UpdatedAt, 0).UTC().Format(time.RFC3339)
		out = append(out, row{ID: sum.ID, Title: sessionDisplayTitle(sum), CreateTime: t, UpdateTime: t, Source: sum.Source})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"records": out})
}

// handleChatSession names one conversation for the page that has it open:
// its title and the project it belongs to. The page reads this rather than
// looking itself up in the drawer's list, which lists only the agent's own
// conversations — a project's session, or a scheduled task's fire, is never
// in it.
func (s *Server) handleChatSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	sid := strings.TrimSpace(ParamsFromContext(r.Context()).ByName("id"))
	if sid == "" {
		http.NotFound(w, r)
		return
	}
	owned, err := s.sessionOwned(r.Context(), sid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !owned {
		http.NotFound(w, r)
		return
	}
	title, err := s.Sessions.SessionTitle(r.Context(), sid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// The lookup is the agent's project store, so another tenant's session
	// reads as belonging to no project.
	var project map[string]any
	if store := s.projectStore(); store != nil {
		p, ok, err := store.ProjectForSession(r.Context(), sid)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if ok {
			project = map[string]any{"id": p.ID, "name": p.Name}
		}
	}
	writeJSON(w, map[string]any{"id": sid, "title": title, "project": project})
}

// sessionDisplayTitle is the title a web list shows. An unnamed session is
// stored titled with its own id; that is not a name, so it reads as none and
// the client draws its own placeholder, the way the store's SessionTitle does.
func sessionDisplayTitle(sum state.SessionSummary) string {
	if title := strings.TrimSpace(sum.Title); title != strings.TrimSpace(sum.ID) {
		return title
	}
	return ""
}

func (s *Server) handleChatMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	if s.Core == nil && s.Sessions == nil {
		_ = json.NewEncoder(w).Encode([]any{})
		return
	}
	sid := ParamsFromContext(r.Context()).ByName("id")
	if strings.TrimSpace(sid) == "" {
		http.NotFound(w, r)
		return
	}
	limit := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if n, parseErr := strconv.Atoi(raw); parseErr == nil && n >= 0 {
			limit = n
		}
	}
	if limit > 5000 {
		limit = 5000
	}
	var (
		turns []state.Message
		err   error
	)
	// Work activity is stored structurally in the transcript (assistant tool
	// call + tool result), so load enough preceding context before applying the
	// response limit. Otherwise a limit boundary that starts at the final
	// assistant row would lose its Worked-for eligibility.
	const historyLimit = 5000
	if s.Sessions != nil {
		turns, err = s.Sessions.ListAllMessages(r.Context(), sid, 0)
	} else if s.Core != nil {
		turns, err = s.Core.ListSessionMessages(r.Context(), sid, historyLimit)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Each finished compaction is a row of its own, at the position the live
	// conversation drew it — the rule the terminal's replay uses too.
	compactionsBefore := map[int][]chatCompactionRow{}
	if s.RunRT != nil {
		compactions, cerr := s.sessionCompactionEvents(r.Context(), sid)
		if cerr != nil {
			http.Error(w, cerr.Error(), http.StatusInternalServerError)
			return
		}
		for _, evt := range compactions {
			if pos, placed := turn.CompactionPosition(turns, evt); placed {
				if row, ok := chatCompactionRowFromEvent(evt); ok {
					compactionsBefore[pos] = append(compactionsBefore[pos], row)
				}
			}
		}
	}
	// A goal's lines, at the positions the terminal's replay draws them.
	goalsBefore := map[int][]chatGoalRow{}
	if s.RunRT != nil {
		goals, gerr := s.sessionEventsOfTypes(r.Context(), sid, event.RunEventGoalStarted, event.RunEventGoalRoundStarted, event.RunEventGoalCompleted)
		if gerr != nil {
			http.Error(w, gerr.Error(), http.StatusInternalServerError)
			return
		}
		for _, evt := range goals {
			if pos, placed := turn.GoalPosition(turns, evt); placed {
				if row, ok := chatGoalRowFromEvent(evt); ok {
					goalsBefore[pos] = append(goalsBefore[pos], row)
				}
			}
		}
	}
	type msg struct {
		ID             string              `json:"id,omitempty"`
		RowID          int64               `json:"row_id,omitempty"`
		Role           string              `json:"role"`
		Content        string              `json:"content"`
		MemoryCitation *llm.MemoryCitation `json:"memory_citation,omitempty"`
		RunStartedAt   string              `json:"run_started_at,omitempty"`
		RunFinishedAt  string              `json:"run_finished_at,omitempty"`
		WorkedMs       int64               `json:"worked_duration_ms,omitempty"`
		PlanDone       int                 `json:"plan_done,omitempty"`
		PlanTotal      int                 `json:"plan_total,omitempty"`
		PlanActive     string              `json:"plan_active,omitempty"`
		Compaction     *chatCompactionRow  `json:"compaction,omitempty"`
		Goal           *chatGoalRow        `json:"goal,omitempty"`
		PartsJSON      string              `json:"parts_json,omitempty"`
		ToolStepID     string              `json:"tool_step_id,omitempty"`
		ToolMetaJSON   string              `json:"tool_meta_json,omitempty"`
		CreatedAt      int64               `json:"created_at,omitempty"`
		RunID          string              `json:"run_id,omitempty"`
		Origin         string              `json:"origin,omitempty"`
		Attachments    []chatAttachment    `json:"attachments,omitempty"`
		// SubagentCall is a tool row's card facts: what the subagent_* call
		// it answered was about, derived once by the engine so the reloaded
		// card says what the live one said. Transport only; the derivation
		// lives in pkg/turn.
		SubagentCall *event.SubagentCall `json:"subagent_call,omitempty"`
	}
	// The card facts of every subagent_* call the transcript answered, keyed
	// by tool call id, from the same derivation the live path applied.
	subagentCalls := turn.SubagentCallsInTranscript(turns)
	visibleIndexes := make([]int, 0, len(turns))
	for i, turn := range turns {
		if strings.EqualFold(strings.TrimSpace(turn.Role), "system") {
			continue
		}
		// Runtime-authored rows - the environment context block, the plan-mode
		// reminder, and the context-clear anchor's copy of the gated assistant
		// message - were never shown to anyone. The terminal drops them from its
		// replay for that reason; sending them here put them on screen as though
		// the user had typed them, and split the turn they sit inside in two.
		// IsMeta is a structural part, never inferred from text, because a user
		// may legitimately type the same tags. The marker is role-blind: it
		// means "no surface drew this row", whichever role carries it.
		if _, _, _, isMeta := state.ParseMessageParts(turn.PartsJSON, ""); isMeta {
			continue
		}
		visibleIndexes = append(visibleIndexes, i)
	}
	if limit > 0 && len(visibleIndexes) > limit {
		visibleIndexes = visibleIndexes[len(visibleIndexes)-limit:]
	}
	visible := make(map[int]struct{}, len(visibleIndexes))
	for _, i := range visibleIndexes {
		visible[i] = struct{}{}
	}
	out := make([]msg, 0, len(visibleIndexes))
	firstVisible := len(turns)
	if len(visibleIndexes) > 0 {
		firstVisible = visibleIndexes[0]
	}
	emitCompactions := func(pos int) {
		if pos < firstVisible {
			return
		}
		for i := range compactionsBefore[pos] {
			row := compactionsBefore[pos][i]
			out = append(out, msg{Role: "compaction", Compaction: &row})
		}
		for i := range goalsBefore[pos] {
			row := goalsBefore[pos][i]
			out = append(out, msg{Role: "goal", Goal: &row})
		}
	}
	// Every run closes with its "Worked for" line after the last row it wrote
	// — finished, failed or stopped, whether or not it said anything — by the
	// rule the terminal's replay closes it with. The line is a row of its own,
	// carrying the run's clock and its checklist facts, read from that run's
	// plan events so replay and live agree by construction.
	workedLines := turn.RunWorkedLines(turns)
	for i, t := range turns {
		emitCompactions(i)
		if _, ok := visible[i]; ok {
			// A row's own execution window — a tool call, a `!cmd` — is its
			// timing; the run's clock belongs to the worked row below.
			runStartedAt, runFinishedAt, workedMs := "", "", int64(0)
			if t.ExecStartedAtMs != 0 {
				runStartedAt = time.UnixMilli(t.ExecStartedAtMs).UTC().Format(time.RFC3339Nano)
				runFinishedAt = time.UnixMilli(t.ExecFinishedAtMs).UTC().Format(time.RFC3339Nano)
				workedMs = t.ExecDurationMs
			}
			m := msg{
				ID:            t.MessageID,
				RowID:         t.RowID,
				Role:          t.Role,
				Content:       t.Content,
				RunStartedAt:  runStartedAt,
				RunFinishedAt: runFinishedAt,
				WorkedMs:      workedMs,
				PartsJSON:     t.PartsJSON,
				ToolStepID:    t.ToolStepID,
				ToolMetaJSON:  t.ToolMetaJSON,
				CreatedAt:     t.CreatedAt,
				RunID:         strings.TrimSpace(t.RunID),
			}
			for _, ref := range state.MessageAttachments(t.PartsJSON) {
				m.Attachments = append(m.Attachments, chatAttachmentOf(ref))
			}
			m.Origin = state.MessageOrigin(t.PartsJSON)
			if citation, found := state.ParseMemoryCitationPart(t.PartsJSON); found {
				m.MemoryCitation = citation
			}
			if call, answered := subagentCalls[strings.TrimSpace(t.ToolStepID)]; answered {
				m.SubagentCall = &call
			}
			out = append(out, m)
		}
		if line, ok := workedLines[i]; ok && i >= firstVisible {
			worked := msg{
				ID:            "worked-" + line.RunID,
				Role:          "worked",
				RunID:         line.RunID,
				RunStartedAt:  line.StartedAt.UTC().Format(time.RFC3339Nano),
				RunFinishedAt: line.FinishedAt.UTC().Format(time.RFC3339Nano),
				WorkedMs:      line.Worked.Milliseconds(),
			}
			if s.RunRT != nil {
				progress := lastPlanProgressOfRun(r.Context(), s.RunRT, line.RunID)
				worked.PlanDone, worked.PlanTotal, worked.PlanActive = progress.Done, progress.Total, progress.Active
			}
			out = append(out, worked)
		}
	}
	emitCompactions(len(turns))
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (s *Server) handleChatSessionEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	sid := strings.TrimSpace(ParamsFromContext(r.Context()).ByName("id"))
	if sid == "" || s.RunRT == nil || s.Sessions == nil {
		http.NotFound(w, r)
		return
	}
	owned, err := s.Sessions.HasSession(r.Context(), sid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !owned {
		http.NotFound(w, r)
		return
	}
	parseCursor := func(name string) int64 {
		value, _ := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get(name)), 10, 64)
		return value
	}
	limit := 500
	if raw := parseCursor("limit"); raw > 0 && raw <= 5000 {
		limit = int(raw)
	}
	page, err := s.RunRT.ListSessionEvents(r.Context(), sid, parseCursor("cursor"), parseCursor("high_water"), limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(page)
}

// chatCompactionRow is one finished compaction as the web history shows it:
// how it ended, and the record of what it did.
type chatCompactionRow struct {
	Status string `json:"status"`
	event.ContextCompactedPayload
	Error string `json:"error,omitempty"`
}

// chatAttachment is one thing a user message attached, as its bubble names
// it: an upload by its file id, which the page can open, or a workspace image
// by the path it was picked at.
type chatAttachment struct {
	FileID    string `json:"file_id,omitempty"`
	Path      string `json:"path,omitempty"`
	Name      string `json:"name"`
	MediaType string `json:"media_type,omitempty"`
}

// chatAttachmentOf reads an attachment back from its stored reference. A
// workspace image is stored by its absolute path, which the page never sees:
// it is named by the path it was picked at.
func chatAttachmentOf(ref state.FileRefInfo) chatAttachment {
	out := chatAttachment{Name: firstNonBlank(ref.Label, ref.FileID), MediaType: ref.MIMEType}
	if filepath.IsAbs(ref.FileID) {
		out.Path = out.Name
		out.Name = filepath.Base(out.Name)
		return out
	}
	out.FileID = ref.FileID
	return out
}

// chatGoalRow is one line of a /goal in the chat history: how it opened
// (phase "started"), a continuation round ("round"), or how it ended
// ("completed").
type chatGoalRow struct {
	Phase        string `json:"phase"`
	Objective    string `json:"objective,omitempty"`
	Round        int    `json:"round,omitempty"`
	Why          string `json:"why,omitempty"`
	Status       string `json:"status,omitempty"`
	Rounds       int    `json:"rounds,omitempty"`
	DurationMs   int64  `json:"duration_ms,omitempty"`
	CheckAgentID string `json:"check_agent_id,omitempty"`
}

func chatGoalRowFromEvent(evt event.RunEvent) (chatGoalRow, bool) {
	switch evt.Type {
	case event.RunEventGoalStarted:
		var p event.GoalStartedPayload
		if json.Unmarshal(evt.Payload, &p) != nil {
			return chatGoalRow{}, false
		}
		return chatGoalRow{Phase: "started", Objective: p.Objective}, true
	case event.RunEventGoalRoundStarted:
		var p event.GoalRoundStartedPayload
		if json.Unmarshal(evt.Payload, &p) != nil {
			return chatGoalRow{}, false
		}
		return chatGoalRow{Phase: "round", Round: p.Round, Why: p.Why, CheckAgentID: p.CheckAgentID}, true
	case event.RunEventGoalCompleted:
		var p event.GoalCompletedPayload
		if json.Unmarshal(evt.Payload, &p) != nil {
			return chatGoalRow{}, false
		}
		return chatGoalRow{Phase: "completed", Objective: p.Objective, Status: p.Status, Why: p.Why, Rounds: p.Rounds, DurationMs: p.DurationMs, CheckAgentID: p.CheckAgentID}, true
	default:
		return chatGoalRow{}, false
	}
}

// sessionCompactionEvents reads every finished compaction of the conversation
// from its event log, in the order they ended.
func (s *Server) sessionCompactionEvents(ctx context.Context, sessionID string) ([]event.RunEvent, error) {
	return s.sessionEventsOfTypes(ctx, sessionID, event.RunEventContextCompacted, event.RunEventContextCompactError)
}

// sessionEventsOfTypes reads the conversation's events of the given types
// from its event log, in the order they were recorded.
func (s *Server) sessionEventsOfTypes(ctx context.Context, sessionID string, eventTypes ...string) ([]event.RunEvent, error) {
	var out []event.RunEvent
	for _, eventType := range eventTypes {
		records, err := s.RunRT.ListSessionEventsOfType(ctx, sessionID, eventType, 5000)
		if err != nil {
			return nil, err
		}
		for _, record := range records {
			out = append(out, turn.RunEventFromRecord(record))
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Sequence < out[j].Sequence })
	return out, nil
}

func chatCompactionRowFromEvent(evt event.RunEvent) (chatCompactionRow, bool) {
	switch evt.Type {
	case event.RunEventContextCompacted:
		var payload event.ContextCompactedPayload
		if json.Unmarshal(evt.Payload, &payload) != nil {
			return chatCompactionRow{}, false
		}
		return chatCompactionRow{Status: "done", ContextCompactedPayload: payload.Canonicalized()}, true
	case event.RunEventContextCompactError:
		var payload event.ContextCompactFailedPayload
		if json.Unmarshal(evt.Payload, &payload) != nil {
			return chatCompactionRow{}, false
		}
		row := chatCompactionRow{Status: "failed", Error: payload.Error, ContextCompactedPayload: event.ContextCompactedPayload{
			CompactionID: payload.CompactionID, AgentID: payload.AgentID, Trigger: payload.Trigger,
		}}
		if payload.Cancelled {
			row.Status = "cancelled"
		}
		return row, true
	default:
		return chatCompactionRow{}, false
	}
}

func (s *Server) handleSessionTodos(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	sid := ParamsFromContext(r.Context()).ByName("id")
	if strings.TrimSpace(sid) == "" {
		http.NotFound(w, r)
		return
	}
	l, err := state.Load(s.stateRoot(), sid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(l)
}

func (s *Server) handleSessionPlanMarkdown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	sid := ParamsFromContext(r.Context()).ByName("id")
	if strings.TrimSpace(sid) == "" {
		http.NotFound(w, r)
		return
	}
	// Project-scoped, like every other plan resolution: a hardcoded empty key
	// reads <stateRoot>/plans instead of the session's <stateRoot>/plans/<project>.
	p, err := state.GetPlanForProject(s.stateRoot(), s.projectKey())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"markdown": p})
}

func (s *Server) handleSessionMode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	sid := strings.TrimSpace(ParamsFromContext(r.Context()).ByName("id"))
	if sid == "" {
		http.NotFound(w, r)
		return
	}
	st, err := state.Get(s.stateRoot(), sid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if st.Mode == "" {
		st.Mode = state.ModeAgent
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"mode":       string(st.Mode),
		"phase":      st.Phase,
		"updated_at": st.UpdatedAt,
	})
}

func (s *Server) handleSessionCompact(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	if s == nil || s.Sessions == nil {
		http.Error(w, "session store unavailable", http.StatusServiceUnavailable)
		return
	}
	sid := strings.TrimSpace(ParamsFromContext(r.Context()).ByName("id"))
	if sid == "" {
		http.NotFound(w, r)
		return
	}
	svc := run.CompactionService(s.runnerFor(r.Context(), sid), s.Sessions)
	res, err := svc.ManualCompactSession(r.Context(), sid, "manual")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(compactResultBody(res))
}

// compactResultBody is the JSON a finished manual compaction answers with,
// shared by the conversation's /compact and a subagent's, so both read the
// same.
func compactResultBody(res assembly.Result) map[string]any {
	payload := assembly.CompactResultPayload(res)
	body := map[string]any{
		"session_id": res.SessionID, "trigger": payload.Trigger, "strategy": payload.Strategy,
		"reason": payload.Reason, "summary_source": payload.SummarySource, "summary": payload.Summary,
		"boundary_id": payload.BoundaryID, "replaced_items": payload.ReplacedItems,
		"window_number": payload.WindowNumber, "tokens_before": payload.TokensBefore,
		"tokens_after": payload.TokensAfter, "duration_ms": res.Duration.Milliseconds(),
		"banner": assembly.FormatCompactBanner(payload, res.Duration),
	}
	if res.Strategy == "local" {
		body["warning"] = assembly.WarningMessage
	}
	return body
}

// The stable codes a subagent-view request is refused with. The page turns
// each into one sentence in the viewer's language; the gateway never writes a
// sentence for the page.
const (
	subagentRunningCode      = "subagent_running"
	subagentViewCommandCode  = "subagent_view_command"
	useDedicatedEndpointCode = "use_dedicated_endpoint"
)

// writeSubagentViewError answers a refused subagent-view request with its
// stable code, so the page can say the sentence in the viewer's language.
func writeSubagentViewError(w http.ResponseWriter, code, message string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": message, "code": code})
}

// subagentRoute resolves the conversation and the agent key a subagent-view
// route names. A conversation this primary agent does not own is not found
// here (the tenancy the conversation's own routes enforce).
func (s *Server) subagentRoute(w http.ResponseWriter, r *http.Request) (sid, agentKey string, ok bool) {
	params := ParamsFromContext(r.Context())
	sid = strings.TrimSpace(params.ByName("id"))
	agentKey = strings.TrimSpace(params.ByName("agent"))
	if sid == "" || agentKey == "" {
		http.NotFound(w, r)
		return "", "", false
	}
	owned, err := s.sessionOwned(r.Context(), sid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return "", "", false
	}
	if !owned {
		http.NotFound(w, r)
		return "", "", false
	}
	return sid, agentKey, true
}

// subagentRunner resolves the engine runner for the conversation a
// subagent-view route names; nil when none is available.
func (s *Server) subagentRunner(ctx context.Context, sid string) *run.Runner {
	if s == nil {
		return nil
	}
	return s.runnerFor(ctx, sid)
}

// subagentNamed reports whether this conversation names the agent key. The
// recall, withdraw and interrupt engine calls answer false for a key that is
// not there, so their routes check tenancy here — the same lookup the engine's
// channel resolution does, another conversation's subagent being not found.
func (s *Server) subagentNamed(sid, agentKey string) bool {
	if s == nil {
		return false
	}
	_, ok, err := agent.GetMerged(s.stateRoot(), agent.Query{SessionID: strings.TrimSpace(sid), TaskID: strings.TrimSpace(agentKey)})
	return err == nil && ok
}

// subagentConversationSurface is how a message the user sends a subagent from
// the web is framed: the same approval and tool-step seams a detached turn
// installs (subagent steps carry their roster key on the context, so the step
// hook publishes them as canonical events itself), and the queue's boundary
// decision travels back as the conversation's queued_input_released event.
func (s *Server) subagentConversationSurface(sid string) run.SubagentSurface {
	sid = strings.TrimSpace(sid)
	return run.SubagentSurface{
		Frame: func(ctx context.Context) context.Context {
			return s.withDetachedGatewayApprovalHooks(ctx, sid, "")
		},
		OnBoundary: func(agentKey string, send, restore []run.Input) {
			s.publishSubagentQueuedInputReleased(sid, agentKey, send, restore)
		},
	}
}

// publishSubagentQueuedInputReleased hands a subagent's queue boundary decision
// to every open page of the conversation: what runs next is sent, the rest go
// back to that subagent's composer.
func (s *Server) publishSubagentQueuedInputReleased(sid, agentKey string, send, restore []run.Input) {
	if s == nil || s.RunEvents() == nil || (len(send) == 0 && len(restore) == 0) {
		return
	}
	payload := event.QueuedInputReleasedPayload{AgentID: strings.TrimSpace(agentKey)}
	for _, in := range restore {
		payload.Inputs = append(payload.Inputs, releasedInput(in))
	}
	for _, in := range send {
		payload.Next = append(payload.Next, releasedInput(in))
	}
	if err := s.RunEvents().Publish(context.Background(), event.NewRunEvent("", "", strings.TrimSpace(sid), event.RunEventQueuedInputReleased, payload, time.Now())); err != nil {
		slog.Error("publish subagent queued input release", "session_id", sid, "agent_id", agentKey, "err", err)
	}
}

// publishSubagentPendingInputUpdated reports a subagent's queue as its own view
// shows it, tagged with its roster key so it lands there and nowhere else.
func (s *Server) publishSubagentPendingInputUpdated(ctx context.Context, sid, agentKey string, runner *run.Runner) {
	if s == nil || s.RunEvents() == nil || runner == nil {
		return
	}
	preview := subagentInputPreview(runner, sid, agentKey)
	payload := event.PendingInputUpdatedPayload{
		AgentID:        strings.TrimSpace(agentKey),
		PendingSteers:  preview.PendingSteers,
		RejectedSteers: preview.RejectedSteers,
		QueuedMessages: preview.QueuedMessages,
	}
	if err := s.RunEvents().Publish(ctx, event.NewRunEvent("", "", strings.TrimSpace(sid), event.RunEventPendingInputUpdated, payload, time.Now())); err != nil {
		slog.Error("publish subagent pending input", "session_id", sid, "agent_id", agentKey, "err", err)
	}
}

// subagentInputPreview is a subagent's queued input as its own view shows it.
func subagentInputPreview(r *run.Runner, sid, agentKey string) turn.PendingInputPreview {
	p := run.SubagentInputPreview(r, sid, agentKey)
	return turn.PendingInputPreview{PendingSteers: p.Steers, RejectedSteers: p.Rejected, QueuedMessages: p.FollowUp}
}

// subagentInputRequest is the body of POST .../subagents/:agent/input.
type subagentInputRequest struct {
	Message       string   `json:"message"`
	Attachments   []string `json:"attachments"`
	MentionImages []string `json:"mention_images"`
	// Mode is "steer" (the default) or "follow_up": where the message goes when
	// the subagent is already running.
	Mode string `json:"mode"`
}

func (s *Server) handleSubagentInput(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	sid, agentKey, ok := s.subagentRoute(w, r)
	if !ok {
		return
	}
	runner := s.subagentRunner(r.Context(), sid)
	if runner == nil {
		http.Error(w, "subagent input unavailable", http.StatusServiceUnavailable)
		return
	}
	var req subagentInputRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	in := run.Input{Text: strings.TrimSpace(req.Message), Attachments: req.Attachments, MentionImages: req.MentionImages}
	if strings.HasPrefix(in.Text, "/") {
		if code, ok := s.subagentCommandInput(&in); !ok {
			writeSubagentViewError(w, code, "this command belongs to the conversation", http.StatusBadRequest)
			return
		}
	}
	if in.Text == "" && len(in.Attachments) == 0 && len(in.MentionImages) == 0 {
		http.Error(w, "empty message", http.StatusBadRequest)
		return
	}
	mode := run.TurnInputModeSteer
	if reqMode := strings.ToLower(strings.TrimSpace(req.Mode)); reqMode == "follow_up" || reqMode == "queue" {
		mode = run.TurnInputModeFollowUp
	}
	delivery, err := run.SendToSubagent(r.Context(), runner, s.subagentConversationSurface(sid), sid, agentKey, in, mode)
	if err != nil {
		if errors.Is(err, run.ErrSubagentNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.publishSubagentPendingInputUpdated(r.Context(), sid, agentKey, runner)
	writeJSON(w, map[string]any{"delivery": string(delivery), "preview": subagentInputPreview(runner, sid, agentKey)})
}

// subagentCommandInput turns a slash line the user typed in a subagent's view
// into what is sent. A skill command is expanded through the shared slash path
// and its trusted selection rides along; a command that belongs to the
// conversation is refused with its stable code (the caller writes it).
func (s *Server) subagentCommandInput(in *run.Input) (code string, ok bool) {
	fields := strings.Fields(strings.TrimSpace(in.Text))
	name := strings.ToLower(strings.TrimPrefix(fields[0], "/"))
	switch name {
	case "compact", "context":
		return useDedicatedEndpointCode, false
	}
	if _, isBuiltin := turn.Find(name); isBuiltin {
		// A built-in that acts on the conversation (hidden, or global) is not
		// sent here; the page runs it in the main view instead.
		return subagentViewCommandCode, false
	}
	outcome := parseSlashCommandWithOptions(s, "", "webchat", in.Text, turn.Context{}, nil)
	if strings.TrimSpace(outcome.SkillName) == "" && strings.TrimSpace(outcome.SkillPath) == "" {
		return subagentViewCommandCode, false
	}
	in.SkillName = strings.TrimSpace(outcome.SkillName)
	in.SkillPath = strings.TrimSpace(outcome.SkillPath)
	if expanded := strings.TrimSpace(outcome.ContinueInput); expanded != "" {
		in.Text = expanded
	}
	return "", true
}

func (s *Server) handleSubagentQueuedInput(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	sid, agentKey, ok := s.subagentRoute(w, r)
	if !ok {
		return
	}
	runner := s.subagentRunner(r.Context(), sid)
	if runner == nil {
		http.Error(w, "subagent input unavailable", http.StatusServiceUnavailable)
		return
	}
	if !s.subagentNamed(sid, agentKey) {
		http.NotFound(w, r)
		return
	}
	var req runInputRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	if !strings.EqualFold(strings.TrimSpace(req.Action), "edit_last") {
		http.Error(w, "unsupported action", http.StatusBadRequest)
		return
	}
	item, accepted := run.RecallSubagentInput(runner, sid, agentKey)
	s.publishSubagentPendingInputUpdated(r.Context(), sid, agentKey, runner)
	if !accepted {
		writeJSON(w, map[string]any{"accepted": false, "preview": subagentInputPreview(runner, sid, agentKey)})
		return
	}
	writeJSON(w, map[string]any{
		"accepted":       true,
		"message":        item.Text,
		"attachments":    item.Attachments,
		"mention_images": item.MentionImages,
		"preview":        subagentInputPreview(runner, sid, agentKey),
	})
}

func (s *Server) handleSubagentInterruptSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	sid, agentKey, ok := s.subagentRoute(w, r)
	if !ok {
		return
	}
	runner := s.subagentRunner(r.Context(), sid)
	if runner == nil {
		http.Error(w, "subagent input unavailable", http.StatusServiceUnavailable)
		return
	}
	if !s.subagentNamed(sid, agentKey) {
		http.NotFound(w, r)
		return
	}
	interrupted := run.InterruptSubagentToSend(runner, sid, agentKey)
	writeJSON(w, map[string]any{"interrupted": interrupted})
}

func (s *Server) handleSubagentWithdraw(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	sid, agentKey, ok := s.subagentRoute(w, r)
	if !ok {
		return
	}
	runner := s.subagentRunner(r.Context(), sid)
	if runner == nil {
		http.Error(w, "subagent input unavailable", http.StatusServiceUnavailable)
		return
	}
	if !s.subagentNamed(sid, agentKey) {
		http.NotFound(w, r)
		return
	}
	inputs, withdrawn := run.WithdrawSubagentInput(runner, sid, agentKey)
	s.publishSubagentPendingInputUpdated(r.Context(), sid, agentKey, runner)
	msgs := make([]map[string]any, 0, len(inputs))
	for _, in := range inputs {
		msgs = append(msgs, map[string]any{"message": in.Text, "attachments": in.Attachments, "mention_images": in.MentionImages})
	}
	writeJSON(w, map[string]any{"withdrawn": msgs, "any": withdrawn, "preview": subagentInputPreview(runner, sid, agentKey)})
}

func (s *Server) handleSubagentCompact(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	if s == nil || s.Sessions == nil {
		http.Error(w, "session store unavailable", http.StatusServiceUnavailable)
		return
	}
	sid, agentKey, ok := s.subagentRoute(w, r)
	if !ok {
		return
	}
	runner := s.subagentRunner(r.Context(), sid)
	if runner == nil {
		http.Error(w, "subagent compact unavailable", http.StatusServiceUnavailable)
		return
	}
	subCtx, workerSessionID, err := run.SubagentCompactTarget(r.Context(), runner, sid, agentKey)
	if err != nil {
		switch {
		case errors.Is(err, run.ErrSubagentRunning):
			writeSubagentViewError(w, subagentRunningCode, "the subagent is running", http.StatusConflict)
		case errors.Is(err, run.ErrSubagentNotFound):
			http.NotFound(w, r)
		default:
			http.Error(w, err.Error(), http.StatusBadRequest)
		}
		return
	}
	svc := run.CompactionService(runner, s.Sessions)
	res, err := svc.ManualCompactSession(subCtx, workerSessionID, "manual")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, compactResultBody(res))
}

func (s *Server) handleSubagentContext(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	sid, agentKey, ok := s.subagentRoute(w, r)
	if !ok {
		return
	}
	runner := s.subagentRunner(r.Context(), sid)
	if runner == nil {
		http.Error(w, "context unavailable", http.StatusServiceUnavailable)
		return
	}
	workerSessionID, provider, model, used, explicitLimit, err := run.SubagentContextGauge(r.Context(), runner, sid, agentKey)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if s.Env.Tools() == nil {
		http.Error(w, "context unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.subagentContextBody(r.Context(), workerSessionID, provider, model, used, explicitLimit))
}

// subagentContextBody is /context for a subagent: the same report the
// conversation's own /context returns, fed the subagent's worker session, its
// model and its own occupancy, so the page draws it with the same component.
func (s *Server) subagentContextBody(ctx context.Context, workerSessionID, provider, model string, used, explicitLimit int) map[string]any {
	workerSessionID = strings.TrimSpace(workerSessionID)
	body := map[string]any{}
	var compactions []event.ContextCompactedPayload
	if s.RunRT != nil {
		read, err := turn.ConversationCompactions(ctx, s.RunRT, workerSessionID)
		if err == nil {
			compactions = read
		}
	}
	if st := s.Env.Tools(); st != nil {
		snapshot, ok := st.GetContextSnapshotForRun(workerSessionID, tool.RunIDFromContext(ctx), compactions)
		if ok && len(snapshot) > 0 {
			_ = json.Unmarshal(snapshot, &body)
			if body == nil {
				body = map[string]any{}
			}
		}
	}
	gauge := turn.ContextGaugeOf(provider, model, used, explicitLimit)
	body["model_context_tokens"] = gauge.WindowTokens
	body["available_window_tokens"] = gauge.WindowTokens
	body["remaining_context_tokens"] = gauge.UsedTokens
	body["percent_left"] = gauge.PercentLeft
	if s.Sessions != nil {
		if boundaryID, part, err := s.Sessions.LatestCompactBoundary(ctx, workerSessionID); err == nil && boundaryID > 0 {
			body["window_number"] = part.WindowNumber
			body["active_boundary_id"] = strings.TrimSpace(formatOptionalBoundaryRowID(boundaryID))
			body["active_window_id"] = strings.TrimSpace(part.WindowID)
		}
	}
	if timeline, ok := body["context_timeline"].([]any); ok && len(timeline) > 0 {
		body["compact_audit"] = map[string]any{
			"session_id": workerSessionID,
			"run_id":     tool.RunIDFromContext(ctx),
			"timeline":   timeline,
		}
		body["compact_diff"] = buildCompactDiff(timeline)
	}
	if spills, ok := body["tool_result_spills"].([]any); ok {
		body["tool_result_spill_count"] = len(spills)
	}
	return body
}

func (s *Server) handleSubagentBudget(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	sid, agentKey, ok := s.subagentRoute(w, r)
	if !ok {
		return
	}
	runner := s.subagentRunner(r.Context(), sid)
	if runner == nil {
		http.Error(w, "budget unavailable", http.StatusServiceUnavailable)
		return
	}
	payload, ok := run.SubagentContextBudget(r.Context(), runner, sid, agentKey)
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, payload)
}

func (s *Server) handleSessionRewindLast(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	if s.RunRT == nil || s.Runner == nil || s.Env.Tools() == nil {
		http.Error(w, "rewind unavailable", http.StatusServiceUnavailable)
		return
	}
	sid := ParamsFromContext(r.Context()).ByName("id")
	if strings.TrimSpace(sid) == "" {
		http.NotFound(w, r)
		return
	}
	owned, ownershipErr := s.sessionOwned(r.Context(), sid)
	if ownershipErr != nil {
		http.Error(w, ownershipErr.Error(), http.StatusInternalServerError)
		return
	}
	if !owned {
		http.NotFound(w, r)
		return
	}
	absPath, found, err := s.RunRT.LatestRewindableToolCall(r.Context(), sid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "no rewritable file found", http.StatusBadRequest)
		return
	}
	snap, ok := s.Env.Tools().LatestSnapshot(absPath)
	if !ok {
		http.Error(w, "no snapshot found", http.StatusBadRequest)
		return
	}
	raw, err := os.ReadFile(snap)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := os.WriteFile(absPath, raw, 0o644); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":   "ok",
		"abs_path": absPath,
		"snapshot": snap,
	})
}

func (s *Server) handleSessionToolAudit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	if s.RunRT == nil {
		http.Error(w, "runrt disabled", http.StatusServiceUnavailable)
		return
	}
	sid := ParamsFromContext(r.Context()).ByName("id")
	if strings.TrimSpace(sid) == "" {
		http.NotFound(w, r)
		return
	}
	owned, ownershipErr := s.sessionOwned(r.Context(), sid)
	if ownershipErr != nil {
		http.Error(w, ownershipErr.Error(), http.StatusInternalServerError)
		return
	}
	if !owned {
		http.NotFound(w, r)
		return
	}
	limit := 200
	if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	rows, err := s.RunRT.ListSessionToolCalls(r.Context(), sid,
		r.URL.Query().Get("run_id"), r.URL.Query().Get("tool_name"), limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(rows)
}

func (s *Server) handleSessionCostSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	if s.RunRT == nil {
		http.Error(w, "runrt disabled", http.StatusServiceUnavailable)
		return
	}
	sid := ParamsFromContext(r.Context()).ByName("id")
	if strings.TrimSpace(sid) == "" {
		http.NotFound(w, r)
		return
	}
	owned, ownershipErr := s.sessionOwned(r.Context(), sid)
	if ownershipErr != nil {
		http.Error(w, ownershipErr.Error(), http.StatusInternalServerError)
		return
	}
	if !owned {
		http.NotFound(w, r)
		return
	}
	byTool, total, err := s.RunRT.CountSessionToolCalls(r.Context(), sid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	usage, err := s.RunRT.SessionUsageForSession(r.Context(), sid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// What the session spent, in the same figures /status reports: the
	// requests' input and output, the cache split and its hit rate, and the
	// tool calls made.
	totals := turn.SessionUsageTotalsOf(usage)
	writeJSON(w, map[string]any{
		"session_id":        sid,
		"input_tokens":      totals.InputTokens,
		"output_tokens":     totals.OutputTokens,
		"cache_read":        totals.CacheRead,
		"cache_written":     totals.CacheWritten,
		"uncached":          totals.Uncached,
		"requests":          totals.Requests,
		"cache_hit_percent": totals.CacheHitPercent,
		"tool_calls":        total,
		"by_tool":           byTool,
	})
}

func (s *Server) handleSessionSubagentHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	sid := ParamsFromContext(r.Context()).ByName("id")
	if strings.TrimSpace(sid) == "" {
		http.NotFound(w, r)
		return
	}
	owned, ownershipErr := s.sessionOwned(r.Context(), sid)
	if ownershipErr != nil {
		http.Error(w, ownershipErr.Error(), http.StatusInternalServerError)
		return
	}
	if !owned {
		http.NotFound(w, r)
		return
	}
	out := []agent.HistoryEntry{}
	if s.Core != nil {
		records, err := s.Core.ListSubagentHistory(turn.SubagentHistoryQuery{
			SessionID: sid,
			// This endpoint is the compatibility source for sessions created
			// before the canonical paged event log. Returning only the newest
			// 200 made older cards permanently unreachable after a refresh.
			Limit: -1,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out = records
	} else {
		records, err := agent.ListMerged(s.stateRoot(), agent.Query{
			SessionID: sid,
			Limit:     -1,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out = records
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"session_id": sid,
		"records":    out,
	})
}

func (s *Server) handleRunSubagents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	runID := strings.TrimSpace(ParamsFromContext(r.Context()).ByName("id"))
	if runID == "" {
		http.NotFound(w, r)
		return
	}
	if s.RunRT != nil {
		rn, runErr := s.RunRT.GetRun(r.Context(), runID)
		if runErr != nil || rn == nil {
			http.NotFound(w, r)
			return
		}
		owned, ownershipErr := s.sessionOwned(r.Context(), rn.SessionID)
		if ownershipErr != nil {
			http.Error(w, ownershipErr.Error(), http.StatusInternalServerError)
			return
		}
		if !owned {
			http.NotFound(w, r)
			return
		}
	}
	limit := 200
	if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	statuses := parseRunStatusFilter(r.URL.Query().Get("status"))
	if s.Core != nil {
		listing, err := s.Core.ListRunSubagents(r.Context(), runID, limit, statuses...)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"run_id":  runID,
			"runs":    listing.Runs,
			"records": listing.Records,
		})
		return
	}
	runs := []state.Run{}
	if s.RunRT != nil {
		list, err := s.RunRT.ListChildRuns(r.Context(), runID, limit, statuses...)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		runs = list
	}
	records, err := agent.ListMerged(s.stateRoot(), agent.Query{
		ParentRunID: runID,
		Limit:       limit,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"run_id":  runID,
		"runs":    runs,
		"records": records,
	})
}

func parseRunStatusFilter(raw string) []state.RunStatus {
	parts := strings.Split(strings.TrimSpace(raw), ",")
	out := make([]state.RunStatus, 0, len(parts))
	for _, part := range parts {
		switch st := state.RunStatus(strings.TrimSpace(part)); st {
		case state.RunStatusRunning, state.RunStatusWaitingAction, state.RunStatusDone, state.RunStatusFailed, state.RunStatusCancelled:
			out = append(out, st)
		}
	}
	return out
}

func (s *Server) handleRunEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	runID := strings.TrimSpace(ParamsFromContext(r.Context()).ByName("id"))
	if runID == "" {
		http.NotFound(w, r)
		return
	}
	if s.RunRT != nil {
		rn, runErr := s.RunRT.GetRun(r.Context(), runID)
		if runErr != nil || rn == nil {
			http.NotFound(w, r)
			return
		}
		owned, ownershipErr := s.sessionOwned(r.Context(), rn.SessionID)
		if ownershipErr != nil {
			http.Error(w, ownershipErr.Error(), http.StatusInternalServerError)
			return
		}
		if !owned {
			http.NotFound(w, r)
			return
		}
	}
	limit := 500
	if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	if s.Core == nil {
		http.Error(w, "run events unavailable", http.StatusServiceUnavailable)
		return
	}
	events, err := s.Core.ListRunEvents(r.Context(), runID, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sessionID := ""
	if len(events) > 0 {
		sessionID = events[0].SessionID
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"run_id":     runID,
		"session_id": sessionID,
		"events":     events,
	})
}

func (s *Server) handleActions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	if s.Actions == nil {
		_ = json.NewEncoder(w).Encode([]any{})
		return
	}
	st := strings.TrimSpace(r.URL.Query().Get("status"))
	limit := 500
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if n, parseErr := strconv.Atoi(raw); parseErr == nil && n > 0 {
			limit = min(n, 5000)
		}
	}
	sessionFilter := strings.TrimSpace(r.URL.Query().Get("session_id"))
	agentFilter := strings.TrimSpace(r.URL.Query().Get("agent_id"))
	if sessionFilter != "" && s.Sessions != nil {
		owned, ownershipErr := s.Sessions.HasSession(r.Context(), sessionFilter)
		if ownershipErr != nil {
			http.Error(w, ownershipErr.Error(), http.StatusInternalServerError)
			return
		}
		if !owned {
			http.NotFound(w, r)
			return
		}
	}
	// One tenant-scoped query: this server's sessions store names the primary
	// agent whose conversations the list may show, and the query narrows to
	// the requested conversation and status inside the database.
	agentID := ""
	if s.Sessions != nil {
		agentID = s.Sessions.AgentID()
	}
	list, err := s.Actions.List(r.Context(), agentID, state.ActionFilter{SessionID: sessionFilter, Status: st, Requester: agentFilter}, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rows := make([]actionListRow, 0, len(list))
	for i := range list {
		rows = append(rows, s.actionListRow(r.Context(), list[i]))
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(rows)
}

type actionListRow struct {
	state.Action
	PermissionSuggestion map[string]any `json:"permission_suggestion,omitempty"`
	// AgentID and SubagentType name the worker that raised this approval when a
	// subagent did. Several fanout children can be waiting on the same person at
	// once, so the row has to say which one is blocked rather than leaving them
	// to guess from the command.
	AgentID      string `json:"agent_id,omitempty"`
	SubagentType string `json:"subagent_type,omitempty"`
}

func (s *Server) actionListRow(ctx context.Context, a state.Action) actionListRow {
	row := actionListRow{Action: a}
	if sugg := s.actionPermissionSuggestion(ctx, a); len(sugg) > 0 {
		row.PermissionSuggestion = sugg
	}
	row.AgentID, row.SubagentType = turn.ActionSubagent(&a)
	return row
}

func (s *Server) actionPermissionSuggestion(ctx context.Context, a state.Action) map[string]any {
	kind := strings.TrimSpace(a.Kind)
	if kind == "" {
		return nil
	}
	base := approvalWSData(s.sessionPermissions(ctx, a.SessionID), a.SessionID, a.ID, kind, kind, a.PayloadJSON)
	raw, _ := base["permission_suggestion"].(map[string]any)
	return raw
}

func (s *Server) handleActionsAsk(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	if s.Actions == nil {
		http.Error(w, "actions disabled", http.StatusServiceUnavailable)
		return
	}
	var form state.AskForm
	if err := json.NewDecoder(r.Body).Decode(&form); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(form.SessionID) == "" {
		http.Error(w, "session_id is required to ask a question", http.StatusBadRequest)
		return
	}
	owned, ownershipErr := s.sessionOwned(r.Context(), form.SessionID)
	if ownershipErr != nil {
		http.Error(w, ownershipErr.Error(), http.StatusInternalServerError)
		return
	}
	if !owned {
		http.NotFound(w, r)
		return
	}
	a, err := s.Actions.CreateAsk(r.Context(), form)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(a)
}

func (s *Server) handleActionsAnswer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	if s.Actions == nil {
		http.Error(w, "actions disabled", http.StatusServiceUnavailable)
		return
	}
	id := ParamsFromContext(r.Context()).ByName("id")
	if strings.TrimSpace(id) == "" {
		http.NotFound(w, r)
		return
	}
	var ans state.AskAnswer
	if err := json.NewDecoder(r.Body).Decode(&ans); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	raw, err := json.Marshal(ans)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	err = s.resolveGatewayApproval(r.Context(), id, turn.ApprovalReply{AskAnswerJSON: string(raw)})
	if err != nil {
		if errors.Is(err, turn.ErrApprovalConflict) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		if errors.Is(err, state.ErrRunNotFound) || errors.Is(err, state.ErrActionNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	a, err := s.Actions.Get(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(a)
}

func (s *Server) handleActionsApprove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	if s.Actions == nil {
		http.Error(w, "actions disabled", http.StatusServiceUnavailable)
		return
	}
	id := ParamsFromContext(r.Context()).ByName("id")
	if strings.TrimSpace(id) == "" {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Reason                     string                             `json:"reason"`
		Update                     *safety.PermissionUpdate           `json:"update,omitempty"`
		ClearContext               bool                               `json:"clear_context,omitempty"`
		Decision                   string                             `json:"decision,omitempty"`
		ExecPolicyAmendment        safety.ExecPolicyAmendment         `json:"execpolicy_amendment,omitempty"`
		NetworkPolicyAmendment     *safety.NetworkPolicyAmendment     `json:"network_policy_amendment,omitempty"`
		RequestPermissionsResponse *safety.RequestPermissionsResponse `json:"request_permissions_response,omitempty"`
	}
	decoder := json.NewDecoder(r.Body)
	decodeErr := decoder.Decode(&body)
	if decodeErr != nil && !errors.Is(decodeErr, io.EOF) {
		http.Error(w, decodeErr.Error(), http.StatusBadRequest)
		return
	}
	if decodeErr == nil {
		var trailing json.RawMessage
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			http.Error(w, "approval request must contain exactly one JSON value", http.StatusBadRequest)
			return
		}
	}
	decision := strings.ToLower(strings.TrimSpace(body.Decision))
	if decision == "" {
		decision = "accept"
	}
	err := s.resolveGatewayApproval(r.Context(), id, turn.ApprovalReply{
		Choice: decision, Exec: body.ExecPolicyAmendment, Network: body.NetworkPolicyAmendment,
		Permissions: body.RequestPermissionsResponse, Update: body.Update, Reason: body.Reason, ClearContext: body.ClearContext,
	})
	if err != nil {
		if errors.Is(err, turn.ErrApprovalConflict) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		if errors.Is(err, state.ErrNotPending) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		if errors.Is(err, state.ErrRunNotFound) || errors.Is(err, state.ErrActionNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	a, err := s.Actions.Get(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(a)
}

// handleSessionApprovalRequest answers "what is this session parked on" with
// the typed approval request the web's exit-plan card is drawn from — the
// same request the TUI's overlay is prompted with, plan text, review models
// and collected reviews included. Nothing parked is not an error: 204.
func (s *Server) handleSessionApprovalRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	sid := strings.TrimSpace(ParamsFromContext(r.Context()).ByName("id"))
	if sid == "" {
		http.NotFound(w, r)
		return
	}
	owned, ownershipErr := s.sessionOwned(r.Context(), sid)
	if ownershipErr != nil {
		http.Error(w, ownershipErr.Error(), http.StatusInternalServerError)
		return
	}
	if !owned {
		http.NotFound(w, r)
		return
	}
	req, err := s.approvalGate().Pending(r.Context(), sid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if req == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// The plan the gate is asking about, read from the path the shared gate
	// resolved: the card has to show the document itself, not a pointer to it.
	planText := ""
	if path := strings.TrimSpace(req.PlanFilePath); path != "" {
		if raw, readErr := os.ReadFile(path); readErr == nil {
			planText = string(raw)
		}
	}
	models := make([]planReviewModelWire, 0, len(req.PlanReviewModels))
	for _, option := range req.PlanReviewModels {
		models = append(models, planReviewModelWire{
			Provider: option.Provider, Model: option.Model, Label: option.Label, Current: option.Current,
		})
	}
	reviews := make([]planReviewNoteWire, 0, len(req.PlanReviews))
	for _, note := range req.PlanReviews {
		reviews = append(reviews, planReviewNoteWire{
			Provider: note.Provider, Model: note.Model, Text: note.Text, DurationMs: note.Duration.Milliseconds(),
		})
	}
	out := sessionApprovalRequestWire{
		ActionID: req.ActionID, Kind: req.ActionKind, ToolName: req.ToolName,
		PlanText: planText, PlanReviewModels: models, PlanReviews: reviews,
	}
	// A review in flight is a fact of the conversation's events, the only
	// source every surface and every replica can agree on without sharing
	// memory: a started review whose closing event has not arrived.
	if active, open := turn.ActivePlanReview(r.Context(), s.RunRT, sid, req.ActionID); open {
		out.PlanReviewActive = &planReviewInFlightWire{
			ReviewID: active.ReviewID, Provider: active.Provider, Model: active.Model,
			Label: active.Label, AgentID: active.AgentID,
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// The approval-request wire shapes: the card reads these fields alone, in the
// snake_case the gateway's API speaks, with durations in milliseconds.
type sessionApprovalRequestWire struct {
	ActionID         string                  `json:"action_id"`
	Kind             string                  `json:"kind"`
	ToolName         string                  `json:"tool_name"`
	PlanText         string                  `json:"plan_text"`
	PlanReviewModels []planReviewModelWire   `json:"plan_review_models"`
	PlanReviews      []planReviewNoteWire    `json:"plan_reviews"`
	PlanReviewActive *planReviewInFlightWire `json:"plan_review_active"`
}

type planReviewModelWire struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Label    string `json:"label,omitempty"`
	Current  bool   `json:"current,omitempty"`
}

type planReviewNoteWire struct {
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	Text       string `json:"text"`
	DurationMs int64  `json:"duration_ms,omitempty"`
}

type planReviewInFlightWire struct {
	ReviewID string `json:"review_id"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model"`
	Label    string `json:"label,omitempty"`
	AgentID  string `json:"agent_id,omitempty"`
}

// handleActionPlanReview starts one second-opinion review of a parked
// exit-plan approval, in the background, through the same shared flow the
// TUI's overlay uses. The request is accepted before the review runs: the
// review's own events are its progress and its result, on the conversation.
func (s *Server) handleActionPlanReview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	if s.Actions == nil {
		http.Error(w, "actions disabled", http.StatusServiceUnavailable)
		return
	}
	id := strings.TrimSpace(ParamsFromContext(r.Context()).ByName("id"))
	if id == "" {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	act, err := s.Actions.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, state.ErrActionNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if act == nil || act.Status != state.ActionPending || !strings.EqualFold(strings.TrimSpace(act.Kind), "exit_plan_mode") {
		http.Error(w, "plan review requires a pending exit_plan_mode approval", http.StatusBadRequest)
		return
	}
	sid := strings.TrimSpace(act.SessionID)
	if sid != "" {
		owned, ownershipErr := s.sessionOwned(r.Context(), sid)
		if ownershipErr != nil {
			http.Error(w, ownershipErr.Error(), http.StatusInternalServerError)
			return
		}
		if !owned {
			http.NotFound(w, r)
			return
		}
	}
	// The model must be one the session is configured with: a reviewer has to
	// run on credentials the session already has.
	model := turn.Model{Provider: strings.TrimSpace(body.Provider), Model: strings.TrimSpace(body.Model)}
	allowed := false
	for _, option := range s.approvalGate().ReviewModels() {
		if option.MatchesSelection(model.Provider, model.Model) {
			allowed = true
			model.Label = option.Label
			break
		}
	}
	if !allowed {
		http.Error(w, "the model is not configured for this session", http.StatusBadRequest)
		return
	}
	runID := ""
	if s.RunRT != nil {
		runID, _, _ = s.RunRT.FindRunByAction(r.Context(), id)
	}
	// The review's tool steps and its own tool approvals flow through the same
	// detached seams a resumed gateway run uses, so the reviewer's cards and
	// gates reach every page watching the conversation.
	detached := s.withDetachedGatewayApprovalHooks(
		process.AgentContextForProject(context.Background(), s.stateRoot(), sid, s.projectKey()), sid, runID,
	)
	// A session without a transcript store reviews without a task context
	// rather than handing the flow a typed-nil store.
	var transcripts turn.PlanReviewTranscripts
	if sessions := s.Sessions; sessions != nil {
		transcripts = sessions
	}
	review := turn.PlanReviewRun{
		ActionID: id, Model: model, SessionID: sid, RunID: runID,
		StateRoot: s.stateRoot(), ProjectKey: s.projectKey(),
		Transcripts: transcripts,
		Reviewer: &process.PlanReviewer{
			Runner: s.Runner, Model: model, Config: s.modelConfig(),
			StepHook: tool.StepHookFromContext(detached, nil),
			Approve:  tool.SubagentApprovalHookFromContext(detached, nil),
		},
		Publish: func(ctx context.Context, evt event.RunEvent) error {
			bus := s.RunEvents()
			if bus == nil {
				return nil
			}
			return bus.Publish(ctx, evt)
		},
	}
	go func() {
		if err := turn.RunPlanReview(detached, review); err != nil {
			return
		}
		s.deliverGatewayPlanReview(detached, id)
	}()
	w.WriteHeader(http.StatusAccepted)
}

// deliverGatewayPlanReview hands a finished review to the planning model on
// the web's path: the approval closes as the marker denial, the resolution
// event lands on the conversation, and the denied resume — the same resume a
// user's deny dispatches — carries the review into the parked run. An
// approval the user already decided on reports ErrNotPending and the delivery
// stops there: the user's decision keeps the last word.
func (s *Server) deliverGatewayPlanReview(ctx context.Context, actionID string) {
	if s == nil || s.Actions == nil {
		return
	}
	act, err := turn.DeliverPlanReview(ctx, s.Actions, actionID)
	if errors.Is(err, state.ErrNotPending) {
		// ErrNotPending is the race the user won: their decision stands.
		return
	}
	if err != nil || act == nil {
		slog.Error("deliver plan review to planner failed",
			"action_id", actionID, "err", err)
		return
	}
	sid := strings.TrimSpace(act.SessionID)
	runID := ""
	if s.RunRT != nil {
		runID, _, _ = s.RunRT.FindRunByAction(ctx, act.ID)
	}
	agentID, subagentType := turn.ActionSubagent(act)
	_ = s.RunEvents().Publish(ctx, event.NewRunEvent(
		"approval-resolved:"+act.ID+":"+string(act.Status), runID, sid,
		event.RunEventApprovalResolved, event.ApprovalResolvedPayload{
			ActionID: act.ID, ActionKind: act.Kind, Decision: string(act.Status),
			Reason: act.Error, AgentID: agentID, SubagentType: subagentType,
		}, time.Now(),
	))
	go s.resumeGatewayRun(act.ID, false)
}

// cancelInFlightPlanReview stops the review an exit-plan approval is still
// waiting on: approving the plan ends the second opinion, because its result
// has no decision left to inform. Best-effort by design — a review that
// finishes first delivers against a closed gate and the delivery stops there.
func (s *Server) cancelInFlightPlanReview(ctx context.Context, sessionID, actionID string) {
	if s == nil || s.RunRT == nil {
		return
	}
	open, ok := turn.ActivePlanReview(ctx, s.RunRT, sessionID, actionID)
	if !ok || strings.TrimSpace(open.AgentID) == "" {
		return
	}
	if _, err := s.cancelSubagent(open.AgentID); err != nil {
		slog.Error("cancel in-flight plan review", "action_id", actionID, "agent_id", open.AgentID, "err", err)
	}
}

func (s *Server) clearContextForAction(ctx context.Context, actionID string) {
	if s.Actions == nil {
		return
	}
	act, err := s.Actions.Get(ctx, actionID)
	if err != nil || act == nil || strings.TrimSpace(act.PayloadJSON) == "" {
		return
	}
	var p struct {
		SessionID string `json:"session_id"`
	}
	if json.Unmarshal([]byte(act.PayloadJSON), &p) != nil {
		return
	}
	sid := strings.TrimSpace(p.SessionID)
	if sid == "" {
		return
	}
	if s.Sessions != nil {
		_, _ = s.Sessions.SetSessionContextResetToLatest(ctx, sid)
	}
}

func (s *Server) handleActionsDeny(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	if s.Actions == nil {
		http.Error(w, "actions disabled", http.StatusServiceUnavailable)
		return
	}
	id := ParamsFromContext(r.Context()).ByName("id")
	if strings.TrimSpace(id) == "" {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	err := s.resolveGatewayApproval(r.Context(), id, turn.ApprovalReply{Choice: "deny", Reason: body.Reason})
	if err != nil {
		if errors.Is(err, turn.ErrApprovalConflict) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		if errors.Is(err, state.ErrRunNotFound) || errors.Is(err, state.ErrActionNotFound) {
			http.NotFound(w, r)
			return
		}
		if errors.Is(err, state.ErrNotPending) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	a, err := s.Actions.Get(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(a)
}

func (s *Server) resolveGatewayApproval(ctx context.Context, actionID string, reply turn.ApprovalReply) error {
	if s == nil || s.Actions == nil {
		return state.ErrActionNotFound
	}
	pending, err := s.Actions.Get(ctx, actionID)
	if err != nil {
		return err
	}
	sid := strings.TrimSpace(pending.SessionID)
	if s.Sessions != nil && sid == "" {
		return state.ErrActionNotFound
	}
	if sid != "" {
		owned, ownershipErr := s.sessionOwned(ctx, sid)
		if ownershipErr != nil {
			return ownershipErr
		}
		if !owned {
			return state.ErrActionNotFound
		}
	}
	result, err := s.approvalService().Decide(ctx, actionID, reply)
	if err != nil {
		return err
	}
	if result.Idempotent {
		// The first response may have committed the decision and then failed to
		// dispatch its continuation. An exact HTTP/WS retry is the recovery
		// signal; the durable claim below still guarantees only one execution.
		// A retried approval is still an approval: it ends any review still
		// running against the plan, exactly as the first response did.
		if result.Approved && strings.EqualFold(strings.TrimSpace(pending.Kind), "exit_plan_mode") {
			s.cancelInFlightPlanReview(ctx, sid, actionID)
		}
		if result.Cancelled {
			s.abortGatewayRunForAction(result.Action)
		} else {
			go s.resumeGatewayRun(actionID, turn.ActionClearedContext(result.Action))
		}
		return nil
	}
	if result.Approved && turn.ActionClearedContext(result.Action) {
		s.clearContextForAction(ctx, actionID)
	}
	// Approving the plan ends any review still running against it: the
	// approval is the decision, and a second opinion with no decision left to
	// inform should not keep burning a model.
	if result.Approved && strings.EqualFold(strings.TrimSpace(pending.Kind), "exit_plan_mode") {
		s.cancelInFlightPlanReview(ctx, sid, actionID)
	}
	if result.Action != nil {
		runID := ""
		if s.RunRT != nil {
			runID, _, _ = s.RunRT.FindRunByAction(ctx, result.Action.ID)
		}
		agentID, subagentType := turn.ActionSubagent(result.Action)
		_ = s.RunEvents().Publish(ctx, event.NewRunEvent(
			"approval-resolved:"+result.Action.ID+":"+string(result.Action.Status), runID, sid,
			event.RunEventApprovalResolved, event.ApprovalResolvedPayload{
				ActionID: result.Action.ID, ActionKind: result.Action.Kind, Decision: string(result.Action.Status),
				Reason: result.Action.Error, AgentID: agentID, SubagentType: subagentType,
			}, time.Now(),
		))
	}
	if result.Cancelled {
		s.abortGatewayRunForAction(result.Action)
		return nil
	}
	go s.resumeGatewayRun(actionID, turn.ActionClearedContext(result.Action))
	return nil
}

// gatewayResumeState projects one canonical approval resume into the resume
// state the parked run executes under. Field-for-field: anything the engine
// adds to turn.ApprovalResume must land here, or the surface silently drops
// it — the DeliveredReview copy is exactly such a field.
func gatewayResumeState(resume turn.ApprovalResume) *tool.ToolApprovalResumeState {
	return &tool.ToolApprovalResumeState{
		Session:         resume.Session,
		Denied:          resume.Denied,
		DenyReason:      resume.Reason,
		DenyFeedback:    resume.Feedback,
		DeliveredReview: resume.DeliveredReview,
	}
}

// resumeGatewayRun resumes the paused run for actionID. When
// clearedContext is true (exit_plan_mode option 1), the pre-clear session
// snapshot is trimmed to a minimal anchor so the cleared history is not replayed
// back into the model, consistent with the store-side context reset.
func (s *Server) resumeGatewayRun(actionID string, clearedContext bool) {
	if s == nil || s.RunRT == nil || s.Runner == nil {
		return
	}
	ctx := context.Background()
	runID, w, err := s.RunRT.FindRunByAction(ctx, actionID)
	if err != nil || runID == "" || w == nil {
		return
	}
	rn, err := s.RunRT.GetRun(ctx, runID)
	if err != nil || rn == nil {
		return
	}
	var action *state.Action
	if s.Actions != nil {
		if act, aerr := s.Actions.Get(ctx, actionID); aerr == nil && act != nil {
			action = act
			clearedContext = clearedContext || turn.ActionClearedContext(act)
			if mode, ok := turn.PlanModeForAction(act); ok {
				_, _ = state.Switch(s.stateRoot(), rn.SessionID, mode)
			}
		}
	}
	if action == nil || action.Status == state.ActionPending {
		return
	}
	switch action.Status {
	case state.ActionCancelled:
		s.abortGatewayRunForAction(action)
		return
	case state.ActionExpired:
		s.expireGatewayApproval(ctx, action.ID)
		return
	case state.ActionError:
		_ = s.RunRT.ClearWait(ctx, runID)
		reason := firstNonEmptyString(action.Error, "approval action failed")
		s.finishRun(ctx, rn.SessionID, runID, state.RunStatusFailed)
		_ = s.publishGatewayRunEvent(ctx, rn.SessionID, runID, event.RunEventTurnError, event.TurnErrorPayload{Error: reason, Message: reason})
		return
	case state.ActionApproved, state.ActionAnswered, state.ActionDenied:
		// These are the only decisions with continuation semantics.
	default:
		return
	}
	claimed, claimErr := s.RunRT.ClaimWaitResume(ctx, runID, actionID, s.approvalResumeOwner())
	if claimErr != nil || !claimed {
		return
	}
	if action != nil && action.Status == state.ActionApproved {
		if effectErr := s.approvalService().ApplyResolvedEffect(action, runID); effectErr != nil {
			_ = s.RunRT.ClearWait(ctx, runID)
			s.finishRun(ctx, rn.SessionID, runID, state.RunStatusFailed)
			_ = s.publishGatewayRunEvent(ctx, rn.SessionID, runID, event.RunEventTurnError, event.TurnErrorPayload{Error: effectErr.Error(), Message: effectErr.Error()})
			slog.Error("restore approval policy effect", "run_id", runID, "action_id", actionID, "err", effectErr)
			return
		}
	}
	if !s.runController().ClaimResume(runID, rn.SessionID) {
		_ = s.RunRT.ReleaseWaitResumeOwner(ctx, runID, actionID, s.approvalResumeOwner())
		return
	}
	_ = s.RunRT.SetStatus(ctx, runID, state.RunStatusRunning)

	resumeCh := "webchat"
	sid := strings.TrimSpace(rn.SessionID)
	if sid == "" {
		sid = "default"
	}
	// A gate raised inside a subagent's own tool call is resumed under that
	// subagent's own WorkerSessionID, not the top-level chat session: state-
	// scoped tools (intermediate_tool, session_todo, mode) are keyed by
	// AgentSessionID, and the subagent wrote them under its own worker
	// session before the gate paused it. w.SubagentRunID (persisted
	// alongside AgentID/SubagentType when the gate fired) names that
	// subagent's run; agent.GetMerged reads both the live registry and the
	// durable subagent-history ledger, so this recovers the correct scope
	// even after a process restart. No match falls back to sid, same as
	// before.
	agentSessionID := sid
	if subRunID := strings.TrimSpace(w.SubagentRunID); subRunID != "" {
		if entry, ok, err := agent.GetMerged(s.stateRoot(), agent.Query{RunID: subRunID}); err == nil && ok {
			if wsid := strings.TrimSpace(entry.WorkerSessionID); wsid != "" {
				agentSessionID = wsid
			}
		}
	}
	ctx2 := llm.WithAgentSessionID(ctx, agentSessionID)
	ctx2 = tool.WithConversationSessionID(ctx2, sid)
	ctx2 = tool.WithRunID(ctx2, runID)
	if q, _, ok := s.runController().Queue(runID); ok && q.Runtime() != nil {
		ctx2 = run.WithTurnInputRuntime(ctx2, q.Runtime())
	}
	// Same wiring as every other surface: a denied exit-plan approval carries
	// the reviews it collected, composed here from the conversation's events
	// — the same guidance the TUI's resume composes — while the user's own
	// words stay what everything displays.
	resume := turn.BuildApprovalResumeWithPlanReviews(ctx, s.RunRT, action, w.SessionSnapshot, w.ToolName, clearedContext)
	if resume.Approved {
		ctx2 = tool.WithApprovedActionID(ctx2, actionID)
	}
	if len(resume.Session) > 0 {
		if resume.Cleared && !resume.Denied {
			// The user cleared context: do not replay the pre-clear history.
			// Persist the post-reset anchor before replay. The gateway's normal
			// success path later appends the full assistant outcome, but the
			// orchestration replay may persist/emit a tool result first; anchoring
			// the assistant tool_call here keeps the retained transcript valid.
			if s.Sessions != nil {
				_ = s.Sessions.AppendMessageSequence(ctx, sid, resume.Session, "", "")
			}
		}
		resumeState := gatewayResumeState(resume)
		// The durable fence is crossed by the replay itself, not here: this
		// path still has prompt assembly, pre-hooks and possibly a compaction
		// ahead of it, and a process that dies in that preamble has executed
		// nothing to be uncertain about.
		turn.BindApprovalContinuationFence(resumeState, s.RunRT, runID, actionID, s.approvalResumeOwner())
		ctx2 = tool.WithToolApprovalResume(ctx2, resumeState)
	}
	// Same wiring as every other surface; a hand-rolled variant here allowed
	// writes to a plan directory other than the one the model is told to write.
	ctx2 = process.AgentContextForProject(ctx2, s.stateRoot(), agentSessionID, s.projectKey())
	ctx2 = s.withDetachedGatewayApprovalHooks(ctx2, sid, runID)
	var streamed bool
	ctx2 = s.withAnswerStream(ctx2, runID, sid, &streamed, &turn.StreamPartial{})
	startedAt := time.Unix(rn.CreatedAt, 0)
	if stored, ok := s.runStartedAt.Load(runID); ok {
		if value, valid := stored.(time.Time); valid && !value.IsZero() {
			startedAt = value
		}
	}
	// P5-12/P5-13: continuing past an approval gate is a turn like any other,
	// so it goes through the canonical path. ExistingRunID names the gated run,
	// which also leaves the finished transition to this function -- it
	// finalises the queued input before finishing, and Controller.Finish clears
	// that queue.
	outcome, runErr := s.Core.Submit(ctx2, turn.TurnRequest{
		SessionID:     rn.SessionID,
		Origin:        turn.Origin{Surface: turn.SurfaceWebChat, ChannelID: resumeCh},
		Trigger:       "resume",
		UserText:      rn.InputText,
		ExistingRunID: runID,
	}, nil)
	res := outcome.Result
	if outcome.Status == turn.TurnWaitingApproval && outcome.Resume != nil {
		runErr = &tool.RequiresActionError{
			RunID:           outcome.RunID,
			ActionID:        outcome.Resume.ActionID,
			ToolName:        outcome.Resume.ToolName,
			SessionSnapshot: outcome.Resume.SessionSnapshot,
		}
	}
	finishedAt := time.Now()
	elapsed := finishedAt.Sub(startedAt)
	if elapsed < 0 {
		elapsed = 0
	}
	if runErr != nil {
		var rae *tool.RequiresActionError
		if errors.As(runErr, &rae) {
			// Another approval gate: the turn is still open, so the queue stays
			// as it is and waits for the run that resumes past this gate too.
			s.publishDetachedGatewayApprovalRequest(ctx, sid, rae)
			return
		}
		_ = s.RunRT.ClearWait(ctx, runID)
		errText := llm.ExplainError(runErr)
		s.finishRun(ctx, sid, runID, state.RunStatusFailed)
		_ = s.publishGatewayRunEvent(ctx, sid, runID, event.RunEventTurnError, event.TurnErrorPayload{Error: errText, Message: errText, Detail: newTurnErrorDetail(runErr)})
		slog.Error("resume run after action", "run_id", runID, "action_id", actionID, "err", runErr)
		return
	}
	ans := ""
	if res != nil {
		ans = res.TextContent()
	}
	if lc := s.liveCfg(); lc != nil {
		ans = safety.SanitizeOutbound(lc, ans)
	}
	if strings.TrimSpace(ans) != "" {
		if !streamed {
			// Not streamed — sent whole by the provider, or held for the
			// output guardrail — so it reaches the page once, as the event its
			// stream would have been.
			_ = s.RunEvents().Publish(ctx, event.NewRunEvent("", runID, sid, event.RunEventAssistantDelta, event.AssistantDeltaPayload{Text: ans}, time.Now()))
		}
	}
	s.finishSuccessfulTurn(ctx, gatewayPostTurnOptions{
		SessionID:       rn.SessionID,
		ChannelID:       resumeCh,
		RunID:           runID,
		AssistantText:   ans,
		RunStartedAt:    startedAt,
		RunFinishedAt:   finishedAt,
		WorkedMs:        elapsed.Milliseconds(),
		AssistantResult: res,
		AppendAssistant: true,
	})
	// What the resumed run never took goes back before its end is reported,
	// the same way the webchat turn loop closes out a run it owns.
	_ = s.RunRT.ClearWait(ctx, runID)
	s.finishRun(ctx, sid, runID, state.RunStatusDone)
	_ = s.publishGatewayRunEvent(ctx, sid, runID, event.RunEventTurnCompleted, event.TurnCompletedPayload{
		Text:      ans,
		ElapsedMS: elapsed.Milliseconds(),
	})
}

func (s *Server) handleSkillsList(w http.ResponseWriter, r *http.Request) {
	s.handleSkillsListWith(agentSkillScope(), "", w, r)
}

// handleSkillsListWith serves the skill listing both the global and the
// project routes use. projectID is empty for the global route and names the
// route project otherwise; it only decides which download URLs the rows
// carry.
func (s *Server) handleSkillsListWith(launch safety.ProjectContext, projectID string, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	svc, err := s.skillLifecycleService(launch)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	overview, err := svc.Overview()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"installed": s.decorateSkillList(launch, projectID, svc, overview.Installed),
		"actions":   overview.Actions,
	})
}
func (s *Server) handleSkillsCreate(w http.ResponseWriter, r *http.Request) {
	s.handleSkillsCreateWith(agentSkillScope(), w, r)
}

func (s *Server) handleSkillsCreateWith(launch safety.ProjectContext, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Name    string `json:"name"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	svc, err := s.skillLifecycleService(launch)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	item, err := svc.Create(skill.CreateRequest{
		Name:    body.Name,
		Content: body.Content,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.publishSkillLifecycleNotify("", "created", strings.TrimSpace(body.Name), map[string]any{"path": item.Path})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "skill": item})
}

func (s *Server) handleSkillsUpdate(w http.ResponseWriter, r *http.Request) {
	s.handleSkillsUpdateWith(agentSkillScope(), w, r)
}

func (s *Server) handleSkillsUpdateWith(launch safety.ProjectContext, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	name := ParamsFromContext(r.Context()).ByName("name")
	if strings.TrimSpace(name) == "" {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	svc, err := s.skillLifecycleService(launch)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	item, err := svc.Update(skill.UpdateRequest{
		Name:    name,
		Content: body.Content,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.publishSkillLifecycleNotify("", "updated", strings.TrimSpace(name), map[string]any{"path": item.Path})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "skill": item})
}

func (s *Server) handleSkillGet(w http.ResponseWriter, r *http.Request) {
	s.handleSkillGetWith(agentSkillScope(), w, r)
}

func (s *Server) handleSkillGetWith(launch safety.ProjectContext, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	name := ParamsFromContext(r.Context()).ByName("name")
	if strings.TrimSpace(name) == "" {
		http.NotFound(w, r)
		return
	}
	svc, err := s.skillLifecycleService(launch)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	item, err := svc.Inspect(name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	content := ""
	if mdPath := strings.TrimSpace(item.Path); mdPath != "" {
		if raw, readErr := os.ReadFile(filepath.Join(mdPath, "SKILL.md")); readErr == nil {
			content = string(raw)
		} else if raw, readErr := os.ReadFile(mdPath); readErr == nil {
			content = string(raw)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"skill":   item,
		"content": content,
	})
}

func (s *Server) handleSkillsToggle(w http.ResponseWriter, r *http.Request) {
	s.handleSkillsToggleWith(agentSkillScope(), "", w, r)
}

// handleSkillsToggleWith takes the listing's project context so the refreshed
// rows it answers with carry the same origin/download fields the page loaded.
func (s *Server) handleSkillsToggleWith(launch safety.ProjectContext, projectID string, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		EnabledPaths []string `json:"enabled_paths"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	svc, err := s.skillLifecycleService(launch)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	if err := svc.SetEnabledPaths(body.EnabledPaths); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	updated, err := s.decoratedSkillList(launch, projectID, svc)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.publishSkillLifecycleNotify("", "updated", "skills", map[string]any{
		"kind":          "toggle",
		"enabled_paths": body.EnabledPaths,
	})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "ok",
		"skills": updated,
	})
}

func (s *Server) handleSkillsInstall(w http.ResponseWriter, r *http.Request) {
	s.handleSkillsInstallWith(agentSkillScope(), w, r)
}

func (s *Server) handleSkillsInstallWith(launch safety.ProjectContext, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		SourceRef string `json:"source_ref"`
		Skill     string `json:"skill"`
		Ref       string `json:"ref"`
		Dest      string `json:"dest"`
		Name      string `json:"name"`
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	req := skill.InstallRequest{
		SourceRef: body.SourceRef,
		Skill:     body.Skill,
		Name:      body.Name,
		Ref:       body.Ref,
		DestScope: skill.ScopeGlobal,
	}
	if strings.EqualFold(strings.TrimSpace(body.Dest), "project") {
		req.DestScope = skill.ScopeProject
	} else if strings.EqualFold(strings.TrimSpace(body.Dest), "workspace") {
		req.DestScope = skill.ScopeWorkspace
	}
	if strings.TrimSpace(body.SourceRef) == "" {
		http.Error(w, "source_ref required", http.StatusBadRequest)
		return
	}
	notifySid := strings.TrimSpace(body.SessionID)
	if notifySid == "" {
		notifySid = strings.TrimSpace(r.Header.Get("X-Forebrain-Notify-Session"))
	}
	if notifySid == "" {
		notifySid = "default"
	}
	progressDetail := map[string]any{
		"source_ref": strings.TrimSpace(body.SourceRef),
		"dest":       strings.TrimSpace(body.Dest),
		"skill":      strings.TrimSpace(body.Skill),
		"name":       strings.TrimSpace(body.Name),
		"dest_scope": req.DestScope,
	}
	taskID := s.beginSkillLifecycleTask(notifySid, "skills install started", map[string]any{
		"phase":       "starting",
		"phase_label": "Preparing",
		"progress":    0.05,
		"session_id":  notifySid,
		"kind":        "install",
		"task_id":     "",
	})
	svc, err := s.skillLifecycleService(launch)
	if err != nil {
		s.publishSkillLifecycleFailedWithTaskID(notifySid, taskID, err.Error(), map[string]any{
			"phase":       "failed",
			"phase_label": "Failed",
			"source_ref":  strings.TrimSpace(body.SourceRef),
			"dest_scope":  req.DestScope,
			"skill":       strings.TrimSpace(body.Skill),
			"name":        strings.TrimSpace(body.Name),
		})
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	svc.OnInstallProgress = func(progress skill.InstallProgress) {
		detail := cloneJSONMap(progressDetail)
		detail["phase"] = strings.TrimSpace(progress.Phase)
		detail["phase_label"] = strings.TrimSpace(progress.PhaseLabel)
		detail["progress"] = progress.Progress
		if progress.DestScope != "" {
			detail["dest_scope"] = progress.DestScope
		}
		if strings.TrimSpace(progress.Skill) != "" {
			detail["skill"] = strings.TrimSpace(progress.Skill)
		}
		if strings.TrimSpace(progress.Name) != "" {
			detail["name"] = strings.TrimSpace(progress.Name)
		}
		if progress.Count > 0 {
			detail["count"] = progress.Count
		}
		if len(progress.InstalledNames) > 0 {
			detail["installed_names"] = append([]string(nil), progress.InstalledNames...)
		}
		s.publishSkillLifecycleProgressWithTaskID(notifySid, taskID, progress.Progress, skillLifecyclePhaseMessage(progress), detail)
	}
	installed, err := svc.Install(r.Context(), req)
	if err != nil {
		s.publishSkillLifecycleFailedWithTaskID(notifySid, taskID, err.Error(), map[string]any{
			"phase":       "failed",
			"phase_label": "Failed",
			"source_ref":  strings.TrimSpace(body.SourceRef),
			"dest_scope":  req.DestScope,
			"skill":       strings.TrimSpace(body.Skill),
			"name":        strings.TrimSpace(body.Name),
		})
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	notifyName := strings.TrimSpace(installed.Name)
	if len(installed.Installed) > 1 {
		notifyName = fmt.Sprintf("%d skills", len(installed.Installed))
	}
	notifyDetail := map[string]any{
		"phase":       "completed",
		"phase_label": "Completed",
		"progress":    1,
		"task_id":     taskID,
		"source":      installed.Source,
		"source_ref":  installed.SourceRef,
		"dest_scope":  installed.DestScope,
		"skill":       installed.Skill,
		"count":       installed.Count,
		"installed":   installed.Installed,
	}
	s.publishSkillLifecycleNotifyWithTaskID(notifySid, taskID, "installed", notifyName, notifyDetail)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":     "ok",
		"task_id":    taskID,
		"session_id": notifySid,
		"installed":  installed,
	})
}

func (s *Server) skillLifecycleService(launch safety.ProjectContext) (*skill.Service, error) {
	if s == nil || strings.TrimSpace(s.Home) == "" {
		return nil, fmt.Errorf("skill lifecycle home unavailable")
	}
	active, err := s.activePrimarySummary()
	if err != nil {
		return nil, err
	}
	// The launch context is the resolved project trust decision for the route:
	// its root owns every project-scope read and write here, and an empty root
	// means this surface has no project, so writes say so instead of guessing
	// one from the process.
	svc := skill.NewServiceForWorkspace(s.Home, active.WorkspaceRoot)
	svc.ProjectRoot = strings.TrimSpace(launch.Project.Root)
	// What a page lists and what the running gateway loads are separate
	// facts. The skill commands the runtime answers are one registry for this
	// process, built for the gateway's own launch project; a page working in
	// another context (the agent's own layers, a project space) changes files,
	// and the registry is rebuilt for the runtime it serves, never replaced by
	// that page's listing.
	runtimeLaunch := s.gatewayLaunchProject()
	svc.OnRefresh = func() error { return turn.RefreshSkills(svc.Home, svc.Workspace(), runtimeLaunch) }
	svc.IsBuiltin = turn.IsBuiltinName
	return svc, nil
}

// agentSkillScope is the project context the primary agent's own skill
// routes work in: none. The agent's page manages its own layer and shows the
// shared, built-in and cross-tool layers it inherits; a project's skills are
// listed and managed in that project's space. The gateway's launch project is
// a project too, and listing it here would put a project's skills on the
// agent's page and make the agent's toggles answer for them.
func agentSkillScope() safety.ProjectContext {
	return safety.ProjectContext{}
}

// gatewayLaunchProject is the launch project this gateway process was started
// in, frozen with its runtime: the context the base runner loads skills for,
// and so the one its skill command registry and a web conversation's explicit
// skill selection answer for.
func (s *Server) gatewayLaunchProject() safety.ProjectContext {
	if s == nil {
		return safety.ProjectContext{}
	}
	if s.Runner != nil {
		return s.Runner.LaunchProject
	}
	if s.Env != nil {
		return s.Env.LaunchProject
	}
	return s.LaunchProject
}

// projectSkillLaunch resolves the route's :id project into its frozen launch
// context, the same resolution the project MCP routes perform.
func (s *Server) projectSkillLaunch(w http.ResponseWriter, r *http.Request) (safety.ProjectContext, bool) {
	store := s.projectStore()
	if store == nil {
		http.Error(w, "projects unavailable", http.StatusServiceUnavailable)
		return safety.ProjectContext{}, false
	}
	p, err := store.Get(r.Context(), strings.TrimSpace(ParamsFromContext(r.Context()).ByName("id")))
	if err != nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return safety.ProjectContext{}, false
	}
	// A registered project's boundary is the path the user drew; resolving it
	// as a launch directory would walk up to an enclosing .git and relocate
	// this project's space onto the whole checkout.
	launch, err := safety.ResolveRegisteredContext(s.Home, p.Root)
	if err != nil {
		http.Error(w, "project could not be resolved", http.StatusBadRequest)
		return safety.ProjectContext{}, false
	}
	return launch, true
}

// The project-scoped skill lifecycle: every route under /projects/:id/skills
// resolves the route project and serves the same operation its global
// counterpart serves, scoped to that project.
func (s *Server) handleProjectSkillsList(w http.ResponseWriter, r *http.Request) {
	launch, ok := s.projectSkillLaunch(w, r)
	if !ok {
		return
	}
	s.handleSkillsListWith(launch, strings.TrimSpace(ParamsFromContext(r.Context()).ByName("id")), w, r)
}

func (s *Server) handleProjectSkillsCreate(w http.ResponseWriter, r *http.Request) {
	launch, ok := s.projectSkillLaunch(w, r)
	if !ok {
		return
	}
	s.handleSkillsCreateWith(launch, w, r)
}

func (s *Server) handleProjectSkillGet(w http.ResponseWriter, r *http.Request) {
	launch, ok := s.projectSkillLaunch(w, r)
	if !ok {
		return
	}
	s.handleSkillGetWith(launch, w, r)
}

func (s *Server) handleProjectSkillsUpdate(w http.ResponseWriter, r *http.Request) {
	launch, ok := s.projectSkillLaunch(w, r)
	if !ok {
		return
	}
	s.handleSkillsUpdateWith(launch, w, r)
}

func (s *Server) handleProjectSkillsToggle(w http.ResponseWriter, r *http.Request) {
	launch, ok := s.projectSkillLaunch(w, r)
	if !ok {
		return
	}
	s.handleSkillsToggleWith(launch, strings.TrimSpace(ParamsFromContext(r.Context()).ByName("id")), w, r)
}

func (s *Server) handleProjectSkillsInstall(w http.ResponseWriter, r *http.Request) {
	launch, ok := s.projectSkillLaunch(w, r)
	if !ok {
		return
	}
	s.handleSkillsInstallWith(launch, w, r)
}

func (s *Server) handleProjectSkillsInstallUpload(w http.ResponseWriter, r *http.Request) {
	launch, ok := s.projectSkillLaunch(w, r)
	if !ok {
		return
	}
	s.handleSkillsInstallUploadWith(launch, strings.TrimSpace(ParamsFromContext(r.Context()).ByName("id")), w, r)
}

func (s *Server) handleProjectSkillsDownload(w http.ResponseWriter, r *http.Request) {
	launch, ok := s.projectSkillLaunch(w, r)
	if !ok {
		return
	}
	s.handleSkillDownloadWith(launch, w, r)
}

func (s *Server) handleProjectSkillsDownloadBatch(w http.ResponseWriter, r *http.Request) {
	launch, ok := s.projectSkillLaunch(w, r)
	if !ok {
		return
	}
	s.handleSkillsDownloadBatchWith(launch, w, r)
}

func (s *Server) handleProjectSkillsDelete(w http.ResponseWriter, r *http.Request) {
	launch, ok := s.projectSkillLaunch(w, r)
	if !ok {
		return
	}
	s.handleSkillDeleteWith(launch, strings.TrimSpace(ParamsFromContext(r.Context()).ByName("id")), w, r)
}

func (s *Server) publishSkillLifecycleNotify(sessionID, action, name string, detail map[string]any) {
	s.publishSkillLifecycleNotifyWithTaskID(sessionID, "", action, name, detail)
}

func (s *Server) publishSkillLifecycleNotifyWithTaskID(sessionID, taskID, action, name string, detail map[string]any) {
	if s == nil || s.Notifier == nil {
		return
	}
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		sid = "default"
	}
	now := time.Now()
	// A skill reaches the model as a tool, and the tool table sits in the prompt
	// prefix, so no lifecycle operation touches the running agent. Say so on
	// every event rather than letting a client assume the change is already live.
	if detail == nil {
		detail = map[string]any{}
	}
	detail["applies_to"] = "next_session"
	b, _ := json.Marshal(detail)
	msg := strings.TrimSpace(action)
	if strings.TrimSpace(name) != "" {
		msg += ": " + strings.TrimSpace(name)
	}
	s.Notifier.Publish(sid, event.TaskEvent{
		EventKind: event.EventDone,
		Message:   msg,
		Task: event.Task{
			ID:        skillLifecycleTaskID(taskID, now),
			Kind:      event.KindWorkItem,
			State:     event.StateDone,
			SessionID: sid,
			Title:     "Skill lifecycle",
			Result:    strings.TrimSpace(string(b)),
			CreatedAt: now,
			UpdatedAt: now,
		},
	})
}

func (s *Server) beginSkillLifecycleTask(sessionID, message string, detail map[string]any) string {
	return s.publishSkillLifecycleProgressWithTaskID(sessionID, "", 0.05, message, detail)
}

func (s *Server) publishSkillLifecycleProgressWithTaskID(sessionID, taskID string, progress float64, message string, detail map[string]any) string {
	if s == nil || s.Notifier == nil {
		return ""
	}
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		sid = "default"
	}
	now := time.Now()
	b, _ := json.Marshal(detail)
	taskID = skillLifecycleTaskID(taskID, now)
	s.Notifier.Publish(sid, event.TaskEvent{
		EventKind: event.EventProgress,
		Message:   strings.TrimSpace(message),
		Task: event.Task{
			ID:        taskID,
			Kind:      event.KindWorkItem,
			State:     event.StateRunning,
			SessionID: sid,
			Title:     "Skill lifecycle",
			Result:    strings.TrimSpace(string(b)),
			Progress:  progress,
			CreatedAt: now,
			UpdatedAt: now,
		},
	})
	return taskID
}

func (s *Server) publishSkillLifecycleFailedWithTaskID(sessionID, taskID, errText string, detail map[string]any) {
	if s == nil || s.Notifier == nil {
		return
	}
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		sid = "default"
	}
	now := time.Now()
	b, _ := json.Marshal(detail)
	s.Notifier.Publish(sid, event.TaskEvent{
		EventKind: event.EventFailed,
		Message:   strings.TrimSpace(errText),
		Task: event.Task{
			ID:        skillLifecycleTaskID(taskID, now),
			Kind:      event.KindWorkItem,
			State:     event.StateFailed,
			SessionID: sid,
			Title:     "Skill lifecycle",
			Result:    strings.TrimSpace(string(b)),
			Error:     strings.TrimSpace(errText),
			CreatedAt: now,
			UpdatedAt: now,
		},
	})
}

func skillLifecycleTaskID(taskID string, now time.Time) string {
	taskID = strings.TrimSpace(taskID)
	if taskID != "" {
		return taskID
	}
	return "skill-lifecycle-" + strconv.FormatInt(now.UnixNano(), 10)
}

func cloneJSONMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// skillLifecyclePhaseMessage speaks the shared install vocabulary so the web
// surface reports a phase in the same words the terminal does.
func skillLifecyclePhaseMessage(progress skill.InstallProgress) string {
	return "skills: " + progress.Describe()
}

func (s *Server) handleWorkerInternalEvent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	sec := strings.TrimSpace(os.Getenv("FOREBRAIN_WORKER_CALLBACK_SECRET"))
	if sec == "" {
		http.Error(w, "disabled", http.StatusServiceUnavailable)
		return
	}
	if strings.TrimSpace(r.Header.Get("X-Forebrain-Worker-Secret")) != sec {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var ev callback
	if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// --- Configuration, channels, providers and hooks -------------------------
//
// These endpoints are how the web surface configures what the terminal
// configures through its wizards. They all follow the same three steps: read
// the file as it is persisted, apply the caller's change to that value, then
// save and reload. Reading the file rather than the live config matters —
// the live config carries runtime overlays (permission presets, trust-derived
// sandbox settings) that must not be written back into forebrain.yaml.

// configWriteResult is the shared answer of every settings write: what the
// runtime is running now, and where it came from.
type configWriteResult struct {
	Path    string `json:"path"`
	AgentID string `json:"agent_id,omitempty"`
	Applied bool   `json:"applied"`
}

func (s *Server) configPath() string {
	if s == nil || s.Env == nil {
		return ""
	}
	return strings.TrimSpace(s.Env.ConfigPath)
}

// persistedConfig reads forebrain.yaml as written, without the .env merge or the
// ${ENV} expansion Load performs. Every write path starts here so a save never
// bakes an expanded secret back into the file.
func (s *Server) persistedConfig() (config.Root, error) {
	path := s.configPath()
	if path == "" {
		return config.Root{}, fmt.Errorf("no config path is bound")
	}
	cfg, err := config.LoadPersisted(path)
	if err != nil && os.IsNotExist(err) {
		// A machine that has not been configured yet has no file. The settings
		// screens must still open on an empty configuration — the same reading
		// startup gives it — or nothing could ever be configured from them.
		return config.Root{}, nil
	}
	return cfg, err
}

// saveAndReload writes the config and brings the running process onto it.
func (s *Server) saveAndReload(cfg config.Root) error {
	path := s.configPath()
	if path == "" {
		return fmt.Errorf("no config path is bound")
	}
	if err := config.Save(path, cfg); err != nil {
		return err
	}
	if s.Env == nil {
		return nil
	}
	return s.Env.ReloadConfig()
}

func (s *Server) handleConfigFile(w http.ResponseWriter, r *http.Request) {
	path := s.configPath()
	switch r.Method {
	case http.MethodGet:
		raw, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		active := ""
		if sum, aerr := s.activePrimarySummary(); aerr == nil {
			active = sum.ID
		}
		writeAgentsJSON(w, map[string]any{
			"path":     path,
			"agent_id": active,
			// The editor is given the file with its secrets masked. Saving it
			// back unchanged keeps them: the mask is the same placeholder the
			// structured endpoints use.
			"yaml": config.RedactConfigText(string(raw)),
		})
	case http.MethodPut:
		var req struct {
			YAML string `json:"yaml"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		next, err := config.ParseRootYAML([]byte(req.YAML))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		config.ClearRedactedSecrets(&next)
		if err := s.saveAndReload(next); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeAgentsJSON(w, configWriteResult{Path: path, Applied: true})
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}

// handleChannels reads and writes the active primary agent's channels. They are
// tenant data: a channel binds an external account to one agent's sessions, so
// the write lands in that agent's definition and nowhere else.
func (s *Server) handleChannels(w http.ResponseWriter, r *http.Request) {
	active, err := s.activePrimarySummary()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		cfg, err := s.persistedConfig()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		section := config.ChannelsForAgent(&cfg, active.ID)
		config.RedactSecrets(&section)
		writeAgentsJSON(w, map[string]any{"agent_id": active.ID, "channels": section})
	case http.MethodPut:
		var section config.ChannelsSection
		if err := json.NewDecoder(r.Body).Decode(&section); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		config.ClearRedactedSecrets(&section)
		cfg, err := s.persistedConfig()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if cfg.Agents.Definitions == nil {
			cfg.Agents.Definitions = map[string]config.AgentDefinition{}
		}
		def := cfg.Agents.Definitions[active.ID]
		def.Channels = section
		cfg.Agents.Definitions[active.ID] = def
		if err := s.saveAndReload(cfg); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeAgentsJSON(w, configWriteResult{Path: s.configPath(), AgentID: active.ID, Applied: true})
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}

// handleProviders reads and writes the active agent's model providers. The list
// is ordered: the first entry is the agent's primary provider, so replacing the
// whole list is the only honest write — patching one entry would leave the
// order, and therefore the choice of primary, ambiguous.
// The providers DTO: the web surface's shape for one model service row. The
// engine stores one entry per model (yaml model is a scalar after expansion);
// adjacent entries sharing a signature fold into one row with a models list,
// which is the shape the user edits. api_key never travels: the GET carries
// only whether it is set and its last four characters, and a PUT with no key
// fields at all means "leave the stored key alone". params travels as its
// JSON text: its keys are the provider's request fields, verbatim, and an
// object would have them re-cased by a client that normalizes response keys —
// then saved back under names the provider does not know.
type providerRowDTO struct {
	Provider   string   `json:"provider"`
	BaseURL    string   `json:"base_url,omitempty"`
	APIPath    string   `json:"api_path,omitempty"`
	Params     string   `json:"params,omitempty"`
	Models     []string `json:"models"`
	APIKeySet  bool     `json:"api_key_set"`
	APIKeyHint string   `json:"api_key_hint,omitempty"`
}

// providerPutRow is one row of the PUT. params decodes through the engine's
// own type, so anything but a JSON object is refused here rather than
// written into a config the loader would then reject.
type providerPutRow struct {
	Provider    string                  `json:"provider"`
	BaseURL     string                  `json:"base_url,omitempty"`
	APIPath     string                  `json:"api_path,omitempty"`
	Params      config.LLMRequestParams `json:"params,omitempty"`
	Model       string                  `json:"model,omitempty"`
	Models      []string                `json:"models,omitempty"`
	APIKey      string                  `json:"api_key,omitempty"`
	APIKeyPlain string                  `json:"api_key_plain,omitempty"`
}

// providerSignature is what two adjacent entries must share to fold into one
// row: same service pointed at the same endpoint, same tuning. The key is
// part of it too — two rows of one provider hold the same key by construction
// (the env name is provider-scoped), so including it changes nothing but
// keeps the fold honest.
func providerSignature(item config.AgentLLMProviderConfig) string {
	return strings.Join([]string{
		strings.TrimSpace(item.Provider),
		strings.TrimSpace(item.BaseURL),
		strings.TrimSpace(item.APIPath),
		strings.TrimSpace(string(item.Params)),
		strings.TrimSpace(item.APIKey),
	}, "\x00")
}

func providerRowsFor(home string, entries []config.AgentLLMProviderConfig) []providerRowDTO {
	rows := make([]providerRowDTO, 0, len(entries))
	prevSig := ""
	for _, item := range entries {
		models := []string{}
		if len(item.Models) > 0 {
			models = append(models, item.Models...)
		} else if strings.TrimSpace(item.Model) != "" {
			models = append(models, item.Model)
		}
		key := strings.TrimSpace(item.APIKey)
		row := providerRowDTO{
			Provider:  strings.TrimSpace(item.Provider),
			BaseURL:   strings.TrimSpace(item.BaseURL),
			APIPath:   strings.TrimSpace(item.APIPath),
			Params:    strings.TrimSpace(string(item.Params)),
			Models:    models,
			APIKeySet: key != "",
		}
		if key != "" {
			row.APIKeyHint = providerKeyHint(home, key)
		}
		// Fold only adjacent rows: an interleaved order (deepseek, openai,
		// deepseek) is a deliberate fallback order, not one service.
		if sig := providerSignature(item); len(rows) > 0 && sig == prevSig && rows[len(rows)-1].Provider == row.Provider {
			rows[len(rows)-1].Models = append(rows[len(rows)-1].Models, models...)
			continue
		} else {
			prevSig = providerSignature(item)
		}
		rows = append(rows, row)
	}
	return rows
}

// providerKeyHint is the last four characters of the key the config refers
// to. A stored ${ENV} reference is resolved through the home's .env — the
// hint is meant for the user who typed the key, and "KEY}" would be noise. A
// reference with no stored value, and any key four characters or shorter, is
// masked whole.
func providerKeyHint(home string, key string) string {
	if name := strings.TrimSpace(key); strings.HasPrefix(name, "${") && strings.HasSuffix(name, "}") {
		envName := strings.TrimSuffix(strings.TrimPrefix(name, "${"), "}")
		if env, err := godotenv.Read(filepath.Join(strings.TrimSpace(home), ".env")); err == nil {
			key = strings.TrimSpace(env[envName])
		} else {
			return "\u2022\u2022\u2022\u2022"
		}
	}
	if key == "" || len(key) <= 4 {
		return "\u2022\u2022\u2022\u2022"
	}
	return key[len(key)-4:]
}

func (s *Server) handleProviders(w http.ResponseWriter, r *http.Request) {
	active, err := s.activePrimarySummary()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		cfg, err := s.persistedConfig()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		def := cfg.Agents.Definitions[active.ID]
		writeAgentsJSON(w, map[string]any{
			"agent_id":  active.ID,
			"providers": providerRowsFor(strings.TrimSpace(s.Home), def.LLMProviders),
		})
	case http.MethodPut:
		var req struct {
			Providers []providerPutRow `json:"providers"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		cfg, err := s.persistedConfig()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if cfg.Agents.Definitions == nil {
			cfg.Agents.Definitions = map[string]config.AgentDefinition{}
		}
		def := cfg.Agents.Definitions[active.ID]
		// A key the request leaves empty is a key the user did not touch:
		// carry the stored one for that provider forward instead of clearing
		// it — the DTO never held the old value to send back.
		oldKeyByProvider := map[string]string{}
		for _, item := range def.LLMProviders {
			if key := strings.TrimSpace(item.APIKey); key != "" {
				if _, seen := oldKeyByProvider[strings.TrimSpace(item.Provider)]; !seen {
					oldKeyByProvider[strings.TrimSpace(item.Provider)] = key
				}
			}
		}
		entries := make([]config.AgentLLMProviderConfig, 0, len(req.Providers))
		for _, row := range req.Providers {
			// The engine builds a client only for a provider its model catalog
			// knows; any other name would save and then fail the next runner
			// load (an agent switch, a restart). Setup refuses it the same way.
			if err := process.VerifyProvider(row.Provider); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			models := row.Models
			if len(models) == 0 && strings.TrimSpace(row.Model) != "" {
				models = []string{row.Model}
			}
			if len(models) == 0 {
				models = []string{""}
			}
			apiKey := strings.TrimSpace(row.APIKey)
			if plain := strings.TrimSpace(row.APIKeyPlain); plain != "" {
				// A plaintext key never enters the config: it is stored in the
				// home's .env and the yaml keeps only the ${ENV} reference.
				apiKey, err = process.ProviderAPIKeyConfigReference(strings.TrimSpace(s.Home), strings.TrimSpace(row.Provider), plain)
				if err != nil {
					http.Error(w, "could not store the api key: "+err.Error(), http.StatusInternalServerError)
					return
				}
			} else if apiKey == "" {
				apiKey = oldKeyByProvider[strings.TrimSpace(row.Provider)]
			}
			for _, model := range models {
				entries = append(entries, config.AgentLLMProviderConfig{
					Provider: strings.TrimSpace(row.Provider),
					Model:    strings.TrimSpace(model),
					APIKey:   apiKey,
					BaseURL:  strings.TrimSpace(row.BaseURL),
					APIPath:  strings.TrimSpace(row.APIPath),
					Params:   row.Params,
				})
			}
		}
		def.LLMProviders = entries
		cfg.Agents.Definitions[active.ID] = def
		// The first row is the agent's primary model. Startup refuses a
		// primary missing any of these, so a table that leaves one out would
		// save fine and then keep the gateway from starting again.
		if missing := config.ValidateAgentLLMConfigured(&cfg, active.ID).MissingFields(); len(missing) > 0 {
			http.Error(w, "the first model service is the primary and needs "+strings.Join(missing, ", "), http.StatusBadRequest)
			return
		}
		if err := s.saveAndReload(cfg); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeAgentsJSON(w, configWriteResult{Path: s.configPath(), AgentID: active.ID, Applied: true})
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}

// handleHooks reads and writes the hook table. Hooks run commands, so the write
// is validated against the hook schema before it is persisted rather than
// failing later at the moment a hook was supposed to fire.
func (s *Server) handleHooks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg, err := s.persistedConfig()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeAgentsJSON(w, map[string]any{
			"hooks":       cfg.Hooks,
			"known_types": config.PersistentHookTypes(),
			"events":      config.HookEventNames(),
		})
	case http.MethodPut:
		var req struct {
			Hooks config.HooksSettings `json:"hooks"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := config.ValidateHooksSettings(req.Hooks); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		cfg, err := s.persistedConfig()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		cfg.Hooks = req.Hooks
		if err := s.saveAndReload(cfg); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeAgentsJSON(w, configWriteResult{Path: s.configPath(), Applied: true})
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}

// handleCronSettings reads and writes the install's scheduled-task settings —
// today the conversation retention. It is global configuration, so it is
// served apart from the agent-scoped /cron routes. The write is validated by
// the same function the loader uses, so a value the YAML editor would reject
// never reaches the file from here either.
func (s *Server) handleCronSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg, err := s.persistedConfig()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeAgentsJSON(w, map[string]any{
			"retention_days": cfg.CronRetentionDays(),
			"configured":     cfg.Cron.RetentionDays != nil,
			"default_days":   config.DefaultCronRetentionDays,
			"min_days":       config.MinCronRetentionDays,
			"max_days":       config.MaxCronRetentionDays,
		})
	case http.MethodPut:
		var req struct {
			RetentionDays *int `json:"retention_days"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := config.ValidateCronSection(config.CronSection{RetentionDays: req.RetentionDays}); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		cfg, err := s.persistedConfig()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		cfg.Cron = config.CronSection{RetentionDays: req.RetentionDays}
		if err := s.saveAndReload(cfg); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeAgentsJSON(w, configWriteResult{Path: s.configPath(), Applied: true})
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}

// --- Cron jobs and heartbeats ---------------------------------------------
//
// A cron job is a standing instruction for the active primary agent: it fires
// a prompt in a fresh session on a schedule and delivers the answer. A
// heartbeat is the other half of the same idea, kept inside one conversation:
// a recurring prompt that fires into that session while it is idle.

type cronJobRequest struct {
	Name        string `json:"name"`
	Schedule    string `json:"schedule"`
	Prompt      string `json:"prompt"`
	Deliver     string `json:"deliver"`
	Enabled     *bool  `json:"enabled"`
	RepeatLimit int    `json:"repeat_limit"`
	// ProjectID binds the job to one project; empty is an agent-wide job.
	ProjectID string `json:"project_id"`
}

func (r cronJobRequest) input() process.CronJobInput {
	return process.CronJobInput{
		Name:        r.Name,
		Schedule:    r.Schedule,
		Prompt:      r.Prompt,
		Deliver:     r.Deliver,
		Enabled:     r.Enabled,
		RepeatLimit: r.RepeatLimit,
		ProjectID:   r.ProjectID,
	}
}

// cronScope resolves the tenant a standing-work request belongs to. Jobs are
// the active primary agent's, so a request never names an agent: it operates on
// whichever one the runtime is currently bound to.
func (s *Server) cronScope(w http.ResponseWriter) (*process.CronService, string, bool) {
	svc := s.cron()
	if svc == nil {
		http.Error(w, "cron service unavailable", http.StatusServiceUnavailable)
		return nil, "", false
	}
	active, err := s.activePrimarySummary()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return nil, "", false
	}
	return svc, active.ID, true
}

func (s *Server) handleCronJobs(w http.ResponseWriter, r *http.Request) {
	svc, agentID, ok := s.cronScope(w)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		jobs, err := svc.ListJobs(r.Context(), agentID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// The layer split the two cron surfaces ask for: the agent page lists
		// only agent-wide jobs, a project tab only its own. Either way the
		// filter is in memory — the standing-job count per agent is small and
		// the storage interface stays untouched.
		projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
		if projectID != "" {
			store := s.projectStore()
			if store == nil {
				http.Error(w, "projects unavailable", http.StatusServiceUnavailable)
				return
			}
			if _, err := store.Get(r.Context(), projectID); err != nil {
				http.Error(w, "project not found", http.StatusNotFound)
				return
			}
		}
		filtered := jobs[:0]
		for _, job := range jobs {
			if strings.TrimSpace(job.ProjectID) == projectID {
				filtered = append(filtered, job)
			}
		}
		writeAgentsJSON(w, map[string]any{"agent_id": agentID, "records": filtered})
	case http.MethodPost:
		var req cronJobRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		job, err := svc.CreateJob(r.Context(), agentID, req.input())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeAgentsJSON(w, job)
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleCronJob(w http.ResponseWriter, r *http.Request) {
	svc, agentID, ok := s.cronScope(w)
	if !ok {
		return
	}
	id := strings.TrimSpace(ParamsFromContext(r.Context()).ByName("id"))
	switch r.Method {
	case http.MethodGet:
		job, err := svc.Job(r.Context(), agentID, id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		writeAgentsJSON(w, job)
	case http.MethodPut:
		var req cronJobRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		job, err := svc.UpdateJob(r.Context(), agentID, id, req.input())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeAgentsJSON(w, job)
	case http.MethodDelete:
		if err := svc.DeleteJob(r.Context(), agentID, id); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		writeAgentsJSON(w, map[string]any{"deleted": id})
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}

// handleCronJobRun fires a job now without disturbing its schedule. The run is
// asynchronous: its answer lands in the job's run history, which is where a
// surface reads it back.
func (s *Server) handleCronJobRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	svc, agentID, ok := s.cronScope(w)
	if !ok {
		return
	}
	id := strings.TrimSpace(ParamsFromContext(r.Context()).ByName("id"))
	if err := svc.RunJobNow(context.Background(), agentID, id); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeAgentsJSON(w, map[string]any{"started": id})
}

func (s *Server) handleCronJobRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	svc, agentID, ok := s.cronScope(w)
	if !ok {
		return
	}
	id := strings.TrimSpace(ParamsFromContext(r.Context()).ByName("id"))
	limit := 50
	if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	runs, err := svc.JobRuns(r.Context(), agentID, id, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeAgentsJSON(w, map[string]any{"job_id": id, "records": runs})
}

// handleHeartbeat reads, sets and clears one session's recurring instruction.
// A heartbeat is part of its conversation, so every method answers only for a
// session this agent owns; another agent's is indistinguishable from none.
func (s *Server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.cronScope(w)
	if !ok {
		return
	}
	requireOwned := func(sid string) bool {
		owned, err := s.sessionOwned(r.Context(), sid)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return false
		}
		if !owned {
			http.NotFound(w, r)
			return false
		}
		return true
	}
	switch r.Method {
	case http.MethodGet:
		sid := strings.TrimSpace(r.URL.Query().Get("session_id"))
		if sid == "" {
			http.Error(w, "session_id is required", http.StatusBadRequest)
			return
		}
		if !requireOwned(sid) {
			return
		}
		hb, err := svc.Heartbeat(r.Context(), sid)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeAgentsJSON(w, map[string]any{"session_id": sid, "heartbeat": heartbeatResponse(hb)})
	case http.MethodPut:
		var req struct {
			SessionID   string `json:"session_id"`
			IntervalSec int    `json:"interval_seconds"`
			Prompt      string `json:"prompt"`
			Paused      bool   `json:"paused"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(req.SessionID) != "" && !requireOwned(req.SessionID) {
			return
		}
		hb, err := svc.SetHeartbeat(r.Context(), state.Heartbeat{
			SessionID:   strings.TrimSpace(req.SessionID),
			IntervalSec: req.IntervalSec,
			Prompt:      req.Prompt,
		}, req.Paused)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeAgentsJSON(w, heartbeatResponse(&hb))
	case http.MethodDelete:
		sid := strings.TrimSpace(r.URL.Query().Get("session_id"))
		if sid == "" {
			http.Error(w, "session_id is required", http.StatusBadRequest)
			return
		}
		if !requireOwned(sid) {
			return
		}
		if err := svc.ClearHeartbeat(r.Context(), sid); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeAgentsJSON(w, map[string]any{"cleared": sid})
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}

// heartbeatResponse renders a heartbeat for a surface. The pause flag is not
// stored: it is derived here from the schedule, so the wire field a client
// reads stays the same while the store keeps one representation of "paused".
func heartbeatResponse(hb *state.Heartbeat) map[string]any {
	if hb == nil {
		return nil
	}
	out := map[string]any{
		"session_id":       hb.SessionID,
		"interval_seconds": hb.IntervalSec,
		"prompt":           hb.Prompt,
		"paused":           hb.NextRunAt == nil,
	}
	if hb.NextRunAt != nil {
		out["next_run_at"] = *hb.NextRunAt
	}
	if hb.LastFiredAt != nil {
		out["last_fired_at"] = *hb.LastFiredAt
	}
	return out
}

func intFromQuery(raw string, def int) int {
	if v, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && v > 0 {
		return v
	}
	return def
}

func timeNowUnix() int64 { return time.Now().Unix() }

type projectPayload struct {
	Name           string `json:"name"`
	Icon           string `json:"icon"`
	Description    string `json:"description"`
	Instructions   string `json:"instructions"`
	Root           string `json:"root"`
	Trust          bool   `json:"trust"`
	MemoryScope    string `json:"memory_scope"`
	ResourceAccess *bool  `json:"resource_access"`
	Pinned         *bool  `json:"pinned"`
	Archived       *bool  `json:"archived"`
}

// projectIsTrusted reads the persisted trust decision for one project root —
// the same record the launch prompt writes and the skill/MCP gates read.
func projectIsTrusted(home string, root string) bool {
	home = strings.TrimSpace(home)
	root = strings.TrimSpace(root)
	if home == "" || root == "" {
		return false
	}
	project, err := safety.ResolveRegisteredProject(root)
	if err != nil {
		return false
	}
	trusted, err := safety.IsTrusted(home, project)
	return err == nil && trusted
}

func projectRow(p state.Project) map[string]any {
	return map[string]any{
		"id":              p.ID,
		"name":            p.Name,
		"icon":            p.Icon,
		"description":     p.Description,
		"instructions":    p.Instructions,
		"root":            p.Root,
		"project_key":     p.ProjectKey,
		"memory_scope":    p.MemoryScope,
		"resource_access": p.ResourceAccess,
		"pinned":          p.Pinned,
		"archived_at":     p.ArchivedAt,
		"created_at":      p.CreatedAt,
		"updated_at":      p.UpdatedAt,
	}
}

func (s *Server) handleProjectsList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	store := s.projectStore()
	if store == nil {
		http.Error(w, "projects unavailable", http.StatusServiceUnavailable)
		return
	}
	q := r.URL.Query()
	includeArchived := strings.EqualFold(strings.TrimSpace(q.Get("archived")), "true") || strings.TrimSpace(q.Get("archived")) == "1"
	limit := intFromQuery(q.Get("limit"), 50)
	offset := intFromQuery(q.Get("offset"), 0)
	projects, err := store.ListProjects(r.Context(), state.ListProjectsOptions{
		Search:          strings.TrimSpace(q.Get("search")),
		Sort:            strings.TrimSpace(q.Get("sort")),
		IncludeArchived: includeArchived,
	}, limit, offset)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rows := make([]map[string]any, 0, len(projects))
	for _, p := range projects {
		rows = append(rows, projectRow(p))
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"projects": rows})
}

// handleProjectsCreate creates a project. The trust flag must be an explicit
// operator choice: the request has to carry "trust": true to record one, and
// the response says which decision was recorded.
func (s *Server) handleProjectsCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	store := s.projectStore()
	if store == nil {
		http.Error(w, "projects unavailable", http.StatusServiceUnavailable)
		return
	}
	var body projectPayload
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	root, err := validateProjectRoot(s.Home, body.Root)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	resourceAccess := true
	if body.ResourceAccess != nil {
		resourceAccess = *body.ResourceAccess
	}
	p, err := store.Create(r.Context(), state.CreateProjectInput{
		Name:           body.Name,
		Icon:           body.Icon,
		Description:    body.Description,
		Instructions:   body.Instructions,
		Root:           root,
		ProjectKey:     memory.ProjectKey(root),
		MemoryScope:    body.MemoryScope,
		ResourceAccess: resourceAccess,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	trustRecorded := false
	if body.Trust {
		if err := safety.MarkTrusted(s.Home, safety.Project{Root: root}); err == nil {
			trustRecorded = true
		}
	}
	row := projectRow(p)
	row["trust_recorded"] = trustRecorded
	if !trustRecorded && body.Trust {
		row["trust_error"] = "the trust decision could not be recorded"
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(row)
}

// validateProjectRoot checks the bound directory is a real place a project
// can live: it exists, is a directory, is not the forebrain home, and is not the
// filesystem root.
//
// It returns the canonical path, because that is the spelling every other part
// of the process uses for the same directory: the trust store records it that
// way, the launch context a route resolves reports it that way, and so does
// the project key written into the very same row. Keeping the path as the
// caller typed it would put two names for one directory in one record — a
// client could not tell that a skill listed under the canonical path belongs
// to the project it is showing, and the same checkout could be bound twice
// under two spellings.
//
// Existence is therefore checked before anything is compared: only a directory
// that is there can be resolved, and a check that mixed a resolved path with
// an unresolved one would silently stop catching what it is there to catch.
func validateProjectRoot(home, candidate string) (string, error) {
	root := strings.TrimSpace(candidate)
	if root == "" {
		return "", fmt.Errorf("root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("root must be an absolute path")
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("root directory does not exist")
	}
	if !info.IsDir() {
		return "", fmt.Errorf("root must be a directory")
	}
	abs, err = safety.CanonicalPath(abs)
	if err != nil {
		return "", fmt.Errorf("root directory could not be resolved")
	}
	if abs == string(filepath.Separator) {
		return "", fmt.Errorf("root must not be the filesystem root")
	}
	if trimmedHome := strings.TrimSpace(home); trimmedHome != "" {
		canonicalHome, err := safety.CanonicalPath(trimmedHome)
		if err != nil {
			return "", fmt.Errorf("the forebrain home directory could not be resolved")
		}
		if abs == canonicalHome {
			return "", fmt.Errorf("root must not be the forebrain home directory")
		}
		if strings.HasPrefix(abs, canonicalHome+string(filepath.Separator)) {
			return "", fmt.Errorf("root must not be inside the forebrain home directory")
		}
	}
	return abs, nil
}

func (s *Server) handleProjectOne(w http.ResponseWriter, r *http.Request) {
	store := s.projectStore()
	if store == nil {
		http.Error(w, "projects unavailable", http.StatusServiceUnavailable)
		return
	}
	id := strings.TrimSpace(ParamsFromContext(r.Context()).ByName("id"))
	p, err := store.Get(r.Context(), id)
	if err != nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}
	switch r.Method {
	case http.MethodGet:
		row := projectRow(p)
		// The detail page's trust gate reads this: without it, a project the
		// user already trusted shows the gate again on every reload.
		row["trusted"] = projectIsTrusted(s.Home, p.Root)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(row)
	case http.MethodPatch:
		var body projectPayload
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		in := state.UpdateProjectInput{}
		if body.Name != "" {
			in.Name = &body.Name
		}
		in.Icon = &body.Icon
		in.Description = &body.Description
		in.Instructions = &body.Instructions
		if body.MemoryScope != "" {
			in.MemoryScope = &body.MemoryScope
		}
		if body.ResourceAccess != nil {
			in.ResourceAccess = body.ResourceAccess
		}
		updated, err := store.Update(r.Context(), id, in)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if body.Trust {
			_ = safety.MarkTrusted(s.Home, safety.Project{Root: updated.Root})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(projectRow(updated))
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleProjectPin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	store := s.projectStore()
	if store == nil {
		http.Error(w, "projects unavailable", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Pinned bool `json:"pinned"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body)
	if err := store.SetPinned(r.Context(), strings.TrimSpace(ParamsFromContext(r.Context()).ByName("id")), body.Pinned); err != nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "pinned": body.Pinned})
}

func (s *Server) handleProjectArchive(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	store := s.projectStore()
	if store == nil {
		http.Error(w, "projects unavailable", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Archived bool `json:"archived"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body)
	var at int64
	if body.Archived {
		at = timeNowUnix()
	}
	if err := store.SetArchived(r.Context(), strings.TrimSpace(ParamsFromContext(r.Context()).ByName("id")), at); err != nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "archived": body.Archived})
}

func (s *Server) handleProjectDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	store := s.projectStore()
	if store == nil {
		http.Error(w, "projects unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := store.Delete(r.Context(), strings.TrimSpace(ParamsFromContext(r.Context()).ByName("id"))); err != nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}
	if s.Env != nil {
		if pool := s.Env.RunnerPool(); pool != nil {
			pool.RebindAgent(s.Runner.AgentName)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

// handleProjectSessionsCreate opens a new session inside one project: the
// session's cwd is the project root and its project binding decides which
// pooled runner serves it. That identity is given to the session at birth;
// it never goes through the store's process-wide defaults.
func (s *Server) handleProjectSessionsCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	store := s.projectStore()
	if store == nil {
		http.Error(w, "projects unavailable", http.StatusServiceUnavailable)
		return
	}
	p, err := store.Get(r.Context(), strings.TrimSpace(ParamsFromContext(r.Context()).ByName("id")))
	if err != nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}
	var body struct {
		Title string `json:"title"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body)
	// No title leaves the session unnamed, so its first message names it.
	title := strings.TrimSpace(body.Title)
	birth := state.SessionBirth{Cwd: p.Root, GitBranch: memory.GitBranch(p.Root)}
	var res struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	if s.Core != nil {
		out, err := s.Core.CreateSessionAt(r.Context(), title, birth)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		res = out
	} else if s.Sessions != nil {
		res.ID = state.NewID("web")
		res.Title = title
		stored := title
		if stored == "" {
			stored = res.ID
		}
		if err := s.Sessions.EnsureAt(r.Context(), res.ID, stored, birth); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	} else {
		http.Error(w, "sessions disabled", http.StatusServiceUnavailable)
		return
	}
	if err := s.bindSessionToProject(r.Context(), res.ID, p); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(res)
}

// bindSessionToProject ties a session to one project: the project store's
// binding, which lists the session under the project, and the runner pool's,
// which decides which pooled runner serves it. A project session a page opens
// and a fire of a project's job take the same path, so the fire's conversation
// runs with the project's tools and instructions from its first turn.
func (s *Server) bindSessionToProject(ctx context.Context, sessionID string, p state.Project) error {
	if err := s.projectStore().BindSession(ctx, sessionID, p.ID); err != nil {
		return err
	}
	if s.Env != nil {
		s.Env.EnsureRunnerPool()
		if pool := s.Env.RunnerPool(); pool != nil {
			// Pre-building the runner here means the first message in the
			// session does not pay for it. A failure is not fatal:
			// the first turn resolves lazily instead.
			_ = pool.BindSession(ctx, sessionID, p.ID)
		}
	}
	return nil
}

func (s *Server) handleProjectSessionsList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	store := s.projectStore()
	if store == nil {
		http.Error(w, "projects unavailable", http.StatusServiceUnavailable)
		return
	}
	if s.Sessions == nil {
		http.Error(w, "sessions disabled", http.StatusServiceUnavailable)
		return
	}
	q := r.URL.Query()
	sessions, err := s.Sessions.ListSessionsForProject(r.Context(), strings.TrimSpace(ParamsFromContext(r.Context()).ByName("id")), intFromQuery(q.Get("limit"), 50), intFromQuery(q.Get("offset"), 0))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rows := make([]map[string]any, 0, len(sessions))
	for _, item := range sessions {
		rows = append(rows, map[string]any{"id": item.ID, "title": sessionDisplayTitle(item), "updated_at": item.UpdatedAt})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"sessions": rows})
}

// The cron schedule preview: a pure function over the engine's own parser, so
// the builder a web user fills in and the scheduler that later fires the job
// can never disagree about what an expression means.

// cronPreviewNext is how many upcoming fire times a preview shows. Three is
// enough to see the shape of a schedule without paging.
const cronPreviewNext = 3

func (s *Server) handleCronPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Schedule string `json:"schedule"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	raw := strings.TrimSpace(body.Schedule)
	response := map[string]any{"raw": raw, "valid": false}
	if raw != "" {
		schedule, err := turn.ParseSchedule(raw, time.Now())
		if err != nil {
			// A preview of a not-yet-valid expression is the endpoint's
			// normal job, not an error condition: the builder shows the
			// engine's own message while the user is still typing.
			response["error"] = err.Error()
		} else {
			response["valid"] = true
			response["kind"] = string(schedule.ScheduleKind)
			next := make([]string, 0, cronPreviewNext)
			after := time.Now()
			for len(next) < cronPreviewNext {
				at, ok := schedule.Next(after)
				if !ok {
					break
				}
				next = append(next, at.Format(time.RFC3339))
				after = at
			}
			response["next"] = next
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

// agentRuleFiles is the primary agent's instruction-file whitelist. It
// mirrors pkg/home's orderedBootstrapFiles (AGENTS.md/SOUL.md/USER.md): the
// files the workspace bootstrap seeds are the files this surface may create
// and edit. A change in what the assembler reads must be made there first,
// then mirrored here.
var agentRuleFiles = []string{"AGENTS.md", "SOUL.md", "USER.md"}

// projectRuleFile is the only instruction file a project layer carries.
const projectRuleFile = "FOREBRAIN.md"

// assemblyBudgetBytes mirrors pkg/home's per-file markdown budget: files
// larger than this still save, but the assembler truncates them, and the
// write answer says so.
const assemblyBudgetBytes = 12_000

const rulesBodyLimit = 64 << 10

func isAgentRuleFile(name string) bool {
	for _, candidate := range agentRuleFiles {
		if candidate == name {
			return true
		}
	}
	return false
}

type ruleFileRow struct {
	Name      string `json:"name"`
	Exists    bool   `json:"exists"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
	UpdatedAt int64  `json:"updated_at,omitempty"`
}

func (s *Server) handleAgentRuleFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	root := s.activeWorkspaceRoot()
	rows := make([]ruleFileRow, 0, len(agentRuleFiles))
	for _, name := range agentRuleFiles {
		row := ruleFileRow{Name: name}
		if info, err := os.Stat(filepath.Join(root, name)); err == nil {
			row.Exists = true
			row.SizeBytes = info.Size()
			row.UpdatedAt = info.ModTime().Unix()
		}
		rows = append(rows, row)
	}
	writeJSON(w, map[string]any{"files": rows})
}

func (s *Server) handleAgentRuleFile(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(pathParam(r, "name"))
	if !isAgentRuleFile(name) {
		http.Error(w, "unsupported rule file", http.StatusBadRequest)
		return
	}
	root := s.activeWorkspaceRoot()
	full := filepath.Join(root, name)
	switch r.Method {
	case http.MethodGet:
		content, exists, err := readRuleFile(full)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"name": name, "exists": exists, "content": content})
	case http.MethodPut:
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, rulesBodyLimit))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := os.WriteFile(full, body, 0o644); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		answer := map[string]any{"name": name, "bytes": len(body)}
		if len(body) > assemblyBudgetBytes {
			answer["warning"] = "exceeds_assembly_budget"
		}
		writeJSON(w, answer)
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}

// readRuleFile reads one instruction file for the editor. A file that does
// not exist yet is an empty, not-yet-created file — the same answer shape as
// one that does, so the editor never has to tell content from a status reply.
func readRuleFile(full string) (content string, exists bool, err error) {
	body, err := os.ReadFile(full)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	return string(body), true, nil
}

// projectRuleLayers lists the directories a project's FOREBRAIN.md chain is
// kept in: the root ("") and its first-level subdirectories, sorted. A
// symlinked directory is not a layer of this project — it may lead anywhere —
// and .git is the repository's own machinery. ReadDir reports a link as a
// link rather than as what it points to, so IsDir leaves links out.
func projectRuleLayers(projectRoot string) []string {
	layers := []string{""}
	entries, err := os.ReadDir(projectRoot)
	if err != nil {
		return layers
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == ".git" {
			continue
		}
		layers = append(layers, entry.Name())
	}
	sort.Strings(layers[1:])
	return layers
}

// projectRuleCreateLimit caps the create dropdown: a project with more
// first-level directories than this is curated from the file system, not a
// dropdown. Existing files are listed and editable whatever the count.
const projectRuleCreateLimit = 50

func (s *Server) projectForRules(w http.ResponseWriter, r *http.Request) (root string, ok bool) {
	store := s.projectStore()
	if store == nil {
		http.Error(w, "projects unavailable", http.StatusServiceUnavailable)
		return "", false
	}
	p, err := store.Get(r.Context(), pathParam(r, "projectId"))
	if err != nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return "", false
	}
	return p.Root, true
}

// projectRulePath maps the ?dir= layer onto its FOREBRAIN.md. Only a layer
// projectRuleLayers lists is accepted, so the editor can neither reach past
// the first level, nor into .git, nor through a symlinked directory.
func projectRulePath(projectRoot, dir string) (string, bool) {
	dir = strings.TrimSpace(dir)
	for _, layer := range projectRuleLayers(projectRoot) {
		if layer == dir {
			return filepath.Join(projectRoot, dir, projectRuleFile), true
		}
	}
	return "", false
}

func (s *Server) handleProjectRuleFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	projectRoot, ok := s.projectForRules(w, r)
	if !ok {
		return
	}
	type row struct {
		Dir       string `json:"dir"`
		Exists    bool   `json:"exists"`
		SizeBytes int64  `json:"size_bytes,omitempty"`
		UpdatedAt int64  `json:"updated_at,omitempty"`
	}
	// The file list is the chain as it stands — the root, always, and every
	// layer that holds a FOREBRAIN.md; the create dropdown offers the
	// subdirectory layers that do not yet (the root row is already listed,
	// so opening it is how its file is created).
	rows := []row{}
	creatable := []string{}
	for _, dir := range projectRuleLayers(projectRoot) {
		item := row{Dir: dir}
		if info, err := os.Stat(filepath.Join(projectRoot, dir, projectRuleFile)); err == nil {
			item.Exists = true
			item.SizeBytes = info.Size()
			item.UpdatedAt = info.ModTime().Unix()
		}
		if item.Exists || dir == "" {
			rows = append(rows, item)
		}
		if !item.Exists && dir != "" && len(creatable) < projectRuleCreateLimit {
			creatable = append(creatable, dir)
		}
	}
	writeJSON(w, map[string]any{"files": rows, "create": creatable})
}

func (s *Server) handleProjectRuleFile(w http.ResponseWriter, r *http.Request) {
	projectRoot, ok := s.projectForRules(w, r)
	if !ok {
		return
	}
	full, valid := projectRulePath(projectRoot, r.URL.Query().Get("dir"))
	if !valid {
		http.Error(w, "unsupported directory", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		content, exists, err := readRuleFile(full)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"dir": strings.TrimSpace(r.URL.Query().Get("dir")), "exists": exists, "content": content})
	case http.MethodPut:
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, rulesBodyLimit))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := os.WriteFile(full, body, 0o644); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		answer := map[string]any{"bytes": len(body)}
		if len(body) > assemblyBudgetBytes {
			answer["warning"] = "exceeds_assembly_budget"
		}
		writeJSON(w, answer)
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}

// --- Approval presets --------------------------------------------------------

type approvalDefaultAnswer struct {
	Current     *string `json:"current"`
	Description string  `json:"description,omitempty"`
}

func (s *Server) handleApprovalDefaultGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	cfg := s.liveCfg()
	mode := s.permissionRuntimeMode()
	preset, ok := safety.MatchApprovalPreset(mode, cfg)
	if !ok {
		writeJSON(w, approvalDefaultAnswer{Current: nil})
		return
	}
	id := preset.ID
	writeJSON(w, approvalDefaultAnswer{Current: &id, Description: preset.Description})
}

func (s *Server) handleApprovalDefaultPut(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Preset string `json:"preset"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	preset, ok := safety.ApprovalPresetByID(req.Preset)
	if !ok {
		http.Error(w, "unknown approval preset", http.StatusBadRequest)
		return
	}
	cfg, err := s.persistedConfig()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// ApplyToConfig is the same pairing the terminal preset uses: the named
	// profile must go for the sandbox half to take effect, and the approval
	// half persists through the policy mode.
	preset.ApplyToConfig(&cfg)
	cfg.ApprovalPolicy = config.NewApprovalPolicy(config.ApprovalPolicyMode(preset.Approval))
	if err := s.saveAndReload(cfg); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id := preset.ID
	writeJSON(w, approvalDefaultAnswer{Current: &id, Description: preset.Description})
}

// handleSessionPreset switches one conversation to a built-in preset for its
// remaining lifetime. Both halves are scoped to that conversation in the
// permission store and nothing is written to disk or to the live config — the
// same rule the terminal's /permissions follows: a preset picked to get
// through one task governs neither the other conversations, channels and
// scheduled jobs of this gateway nor the next session.
func (s *Server) handleSessionPreset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		SessionID string `json:"session_id"`
		Preset    string `json:"preset"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	sid := strings.TrimSpace(req.SessionID)
	if sid == "" {
		http.Error(w, "session_id required", http.StatusBadRequest)
		return
	}
	// A preset is part of the conversation it is set on, so the endpoint
	// answers only for a session this agent owns: another agent's is
	// indistinguishable from missing, and its permission store is never
	// written.
	owned, err := s.sessionOwned(r.Context(), sid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !owned {
		http.NotFound(w, r)
		return
	}
	preset, ok := safety.ApprovalPresetByID(req.Preset)
	if !ok {
		http.Error(w, "unknown approval preset", http.StatusBadRequest)
		return
	}
	runner := s.runnerFor(r.Context(), sid)
	if runner == nil {
		http.Error(w, "permissions unavailable", http.StatusServiceUnavailable)
		return
	}
	for _, update := range preset.SessionUpdates(sid) {
		runner.ApplyPermissionUpdate(update)
	}
	writeJSON(w, map[string]any{"ok": true, "description": preset.Description})
}

// handleSessionPresetGet names the preset one conversation is running under,
// or null when its approval mode and sandbox add up to none of them — the
// same reading the terminal's /permissions picker opens on.
func (s *Server) handleSessionPresetGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	sid := strings.TrimSpace(r.URL.Query().Get("session_id"))
	if sid == "" {
		http.Error(w, "session_id required", http.StatusBadRequest)
		return
	}
	runner := s.runnerFor(r.Context(), sid)
	if runner == nil {
		http.Error(w, "permissions unavailable", http.StatusServiceUnavailable)
		return
	}
	snap := runner.PermissionSnapshotForSession(sid)
	preset, ok := safety.MatchApprovalPreset(snap.Mode, safety.ConfigForSnapshot(runner.AppCfg, snap))
	if !ok {
		writeJSON(w, approvalDefaultAnswer{Current: nil})
		return
	}
	id := preset.ID
	writeJSON(w, approvalDefaultAnswer{Current: &id, Description: preset.Description})
}

// permissionRuntimeMode reads the live permission mode the way the running
// process enforces it, falling back to the configured policy when no runtime
// is mounted yet.
func (s *Server) permissionRuntimeMode() safety.PermissionMode {
	if s.Runner != nil && s.Runner.AppCfg != nil && strings.TrimSpace(string(s.Runner.AppCfg.ApprovalPolicy.Mode)) != "" {
		return safety.PermissionMode(s.Runner.AppCfg.ApprovalPolicy.Mode)
	}
	if cfg := s.liveCfg(); cfg != nil {
		return safety.PermissionMode(cfg.ApprovalPolicy.Mode)
	}
	return safety.ApprovalOnRequest
}

// --- Project permission rules ------------------------------------------------
//
// A project space manages that project's own rules (its .forebrain/safety.json),
// whichever project the gateway process itself was launched in. Each request
// loads a permission runtime from exactly the files a session of that project
// reads — the agent's local settings plus the project's settings — so the
// listing, the write and the explanation all answer for that project.

type projectPermissionScope struct {
	permissionScope
	project state.Project
}

func (s *Server) loadProjectPermissionScope(w http.ResponseWriter, r *http.Request) (projectPermissionScope, bool) {
	store := s.projectStore()
	if store == nil {
		http.Error(w, "projects unavailable", http.StatusServiceUnavailable)
		return projectPermissionScope{}, false
	}
	p, err := store.Get(r.Context(), pathParam(r, "id"))
	if err != nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return projectPermissionScope{}, false
	}
	scope, ok := s.loadPermissionScope(p.Root)
	if !ok {
		http.Error(w, "permissions unavailable", http.StatusServiceUnavailable)
		return projectPermissionScope{}, false
	}
	return projectPermissionScope{permissionScope: scope, project: p}, true
}

// handleProjectPermissionRules lists the project's own rules. applies says
// whether the engine honors project rules for it at all — only a trusted,
// version-controlled project has any — so the page can say why it is empty
// rather than offer a form whose writes would be dropped.
func (s *Server) handleProjectPermissionRules(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	scope, ok := s.loadProjectPermissionScope(w, r)
	if !ok {
		return
	}
	_, applies := safety.ProjectSettingsPath(scope.paths)
	writeJSON(w, map[string]any{
		"applies": applies,
		"rules":   permissionRuleRows(scope.rt.Snapshot(scope.cfg), string(safety.SourceProjectSettings), ""),
	})
}

// handleProjectPermissionUpdate adds, replaces or removes rules in the
// project's own settings file, then has every running session of that project
// read the file again.
func (s *Server) handleProjectPermissionUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	scope, ok := s.loadProjectPermissionScope(w, r)
	if !ok {
		return
	}
	var u safety.PermissionUpdate
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&u); err != nil {
		http.Error(w, "json", http.StatusBadRequest)
		return
	}
	switch u.Type {
	case safety.UpdateAddRules, safety.UpdateReplaceRules, safety.UpdateRemoveRules:
	default:
		http.Error(w, "type must be addRules, replaceRules or removeRules", http.StatusBadRequest)
		return
	}
	u.Destination = safety.DestinationProjectSettings
	if refusal := safety.ExplainRefusedUpdate(u.Destination, u.Behavior); refusal != "" {
		http.Error(w, refusal, http.StatusBadRequest)
		return
	}
	if _, applies := safety.ProjectSettingsPath(scope.paths); !applies {
		http.Error(w, "project rules apply only to a trusted, version-controlled project", http.StatusConflict)
		return
	}
	scope.rt.ApplyUpdate(u, scope.cfg, scope.paths)
	s.propagateProjectPermissionUpdate(scope, u)
	writeJSON(w, map[string]any{
		"ok":    true,
		"rules": permissionRuleRows(scope.rt.Snapshot(scope.cfg), string(safety.SourceProjectSettings), ""),
	})
}

// handleProjectPermissionExplain answers how a call would be judged inside
// the project: by the agent's local rules and the project's own.
func (s *Server) handleProjectPermissionExplain(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	scope, ok := s.loadProjectPermissionScope(w, r)
	if !ok {
		return
	}
	toolName := strings.TrimSpace(r.URL.Query().Get("tool_name"))
	if toolName == "" {
		http.Error(w, "tool_name required", http.StatusBadRequest)
		return
	}
	input := strings.TrimSpace(r.URL.Query().Get("input"))
	writeJSON(w, scope.rt.Explain("", toolName, input, scope.cfg, safety.RuntimeYOLOEnabled()))
}

// propagateProjectPermissionUpdate hands a project-settings update to every
// live runner that reads that very file — the project's pooled runners and,
// when its launch project is the same one, the gateway's own — so sessions
// already running judge by the rules just written. Each applies it to its
// in-memory store; the file it re-persists already holds the same rules.
func (s *Server) propagateProjectPermissionUpdate(scope projectPermissionScope, u safety.PermissionUpdate) {
	target, _ := safety.ProjectSettingsPath(scope.paths)
	var runners []*run.Runner
	if s.Env != nil {
		if pool := s.Env.RunnerPool(); pool != nil {
			runners = append(runners, pool.RunnersForProject(scope.project.ID)...)
		}
	}
	if s.Runner != nil {
		own := safety.Paths{Home: scope.paths.Home, WorkspaceRoot: scope.paths.WorkspaceRoot, ProjectRoot: s.Runner.ProjectRoot}
		if path, ok := safety.ProjectSettingsPath(own); ok && path == target {
			runners = append(runners, s.Runner)
		}
	}
	for _, runner := range runners {
		runner.ApplyPermissionUpdate(u)
	}
	if len(runners) > 0 && s.Env != nil {
		s.Env.RefreshSandboxRuntime()
	}
}

func pathParam(r *http.Request, name string) string {
	return strings.TrimSpace(ParamsFromContext(r.Context()).ByName(name))
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

// lspControlFor resolves the session's Runner the way the MCP endpoints do
// and answers its language-server control plane. A gateway or session without
// one answers false; the caller writes the 503.
func (s *Server) lspControlFor(r *http.Request, sessionID string) (tool.CodeIntelControl, bool) {
	runner := s.Runner
	if id := strings.TrimSpace(sessionID); id != "" {
		if resolved := s.runnerFor(r.Context(), id); resolved != nil {
			runner = resolved
		}
	}
	if runner == nil || runner.CodeIntelControl == nil {
		return nil, false
	}
	return runner.CodeIntelControl, true
}

// handleLSPSnapshot answers GET /api/v1/lsp?session_id=… with the same
// snapshot the terminal's /lsp panel and the web chat's text fallback render.
func (s *Server) handleLSPSnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	ctl, ok := s.lspControlFor(r, r.URL.Query().Get("session_id"))
	if !ok {
		http.Error(w, "language servers are not available", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, ctl.Snapshot())
}

// handleLSPServerEnable clears the server's disabled mark.
// POST /api/v1/lsp/servers/enable with {"id": "gopls", "session_id": "…"}.
func (s *Server) handleLSPServerEnable(w http.ResponseWriter, r *http.Request) {
	s.handleLSPServerAction(w, r, true)
}

// handleLSPServerDisable records the server as disabled; its instances in
// this project stop, the same action the terminal panel runs.
func (s *Server) handleLSPServerDisable(w http.ResponseWriter, r *http.Request) {
	s.handleLSPServerAction(w, r, false)
}

// handleLSPServerAction runs Enable/Disable through the control plane; a
// refusal (an unknown server, a store that cannot be written) answers 409
// with its own text.
func (s *Server) handleLSPServerAction(w http.ResponseWriter, r *http.Request, enable bool) {
	id, ctl, ok := s.lspServerActionInput(w, r)
	if !ok {
		return
	}
	if err := ctl.SetEnabled(id, enable); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handleLSPServerRestart restarts the server's instances in this project.
func (s *Server) handleLSPServerRestart(w http.ResponseWriter, r *http.Request) {
	id, ctl, ok := s.lspServerActionInput(w, r)
	if !ok {
		return
	}
	if err := ctl.Restart(id); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handleLSPServerInstall starts the server's install recipe and answers 202
// at once: progress and outcome are read from the snapshot. The install must
// survive the request that started it (§6.5: the user's explicit action, not
// a connection's lifetime), hence WithoutCancel.
func (s *Server) handleLSPServerInstall(w http.ResponseWriter, r *http.Request) {
	id, ctl, ok := s.lspServerActionInput(w, r)
	if !ok {
		return
	}
	go func() { _ = ctl.Install(context.WithoutCancel(r.Context()), id, nil) }()
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, map[string]any{"ok": true, "started": true})
}

// handleLSPRecommendationsReset turns language-server recommendations back
// on for this agent.
func (s *Server) handleLSPRecommendationsReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	var in struct {
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctl, ok := s.lspControlFor(r, in.SessionID)
	if !ok {
		http.Error(w, "language servers are not available", http.StatusServiceUnavailable)
		return
	}
	if err := ctl.ResetRecommendations(); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handleLSPRecommendationDecision applies the user's answer to one
// recommendation. POST /api/v1/lsp/recommendations/:id/decision with
// {"choice": "enable", "session_id": "…"}. The web card's answer and the
// terminal modal's run the same control plane.
func (s *Server) handleLSPRecommendationDecision(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	var in struct {
		Choice    string `json:"choice"`
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "invalid choice", http.StatusBadRequest)
		return
	}
	choice := event.LSPRecommendationChoice(strings.TrimSpace(in.Choice))
	if !choice.Valid() {
		http.Error(w, "invalid choice", http.StatusBadRequest)
		return
	}
	ctl, ok := s.lspControlFor(r, in.SessionID)
	if !ok {
		http.Error(w, "language servers are not available", http.StatusServiceUnavailable)
		return
	}
	if err := ctl.DecideRecommendation(pathParam(r, "id"), choice); err != nil {
		if errors.Is(err, tool.ErrUnknownLSPRecommendation) {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// lspServerActionInput decodes the shared {"id", "session_id"} body and
// resolves the control plane, answering the request itself on refusal.
func (s *Server) lspServerActionInput(w http.ResponseWriter, r *http.Request) (string, tool.CodeIntelControl, bool) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return "", nil, false
	}
	var in struct {
		ID        string `json:"id"`
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return "", nil, false
	}
	id := strings.TrimSpace(in.ID)
	if id == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return "", nil, false
	}
	ctl, ok := s.lspControlFor(r, in.SessionID)
	if !ok {
		http.Error(w, "language servers are not available", http.StatusServiceUnavailable)
		return "", nil, false
	}
	return id, ctl, true
}

// handleProjectLSPPreview resolves the project-level language-server view for
// a project without a session: which entries the project file declares, which
// await confirmation, which were allowed or denied, and why anything was
// ignored. The web project page uses it so the confirmation entry can stand
// on its own; the data comes from pkg/process because the gateway does not
// import the lsp runtime.
func (s *Server) handleProjectLSPPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	store := s.projectStore()
	if store == nil {
		http.Error(w, "projects unavailable", http.StatusServiceUnavailable)
		return
	}
	p, err := store.Get(r.Context(), pathParam(r, "id"))
	if err != nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}
	launch, err := safety.ResolveRegisteredContext(s.Home, p.Root)
	if err != nil {
		http.Error(w, "project could not be resolved", http.StatusBadRequest)
		return
	}
	workspace := ""
	if s.Runner != nil {
		workspace = s.Runner.WorkspaceRoot
	}
	trusted, pending, allowed, denied, notes := process.InspectProjectLSP(workspace, launch)
	rows := make([]map[string]string, 0, len(pending))
	for _, item := range pending {
		rows = append(rows, map[string]string{"id": item.ID, "summary": item.Summary})
	}
	writeJSON(w, map[string]any{
		"trusted": trusted,
		"pending": rows,
		"allowed": allowed,
		"denied":  denied,
		"notes":   notes,
	})
}

// handleProjectLSPConsent is the web confirmation entry for project-level
// language servers: the same decision the terminal's startup prompt records.
func (s *Server) handleProjectLSPConsent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	store := s.projectStore()
	if store == nil {
		http.Error(w, "projects unavailable", http.StatusServiceUnavailable)
		return
	}
	p, err := store.Get(r.Context(), pathParam(r, "id"))
	if err != nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}
	var body struct {
		Allow []string `json:"allow"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	launch, err := safety.ResolveRegisteredContext(s.Home, p.Root)
	if err != nil {
		http.Error(w, "project could not be resolved", http.StatusBadRequest)
		return
	}
	workspace := ""
	if s.Runner != nil {
		workspace = s.Runner.WorkspaceRoot
	}
	if err := process.DecideProjectLSPConsents(workspace, launch, body.Allow); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// A consent changes what a project's sessions would load, so the pooled
	// runners for it are rebuilt rather than reused.
	if s.Env != nil {
		if pool := s.Env.RunnerPool(); pool != nil {
			pool.RebindAgent(s.Runner.AgentName)
		}
	}
	writeJSON(w, map[string]any{"ok": true, "allowed": body.Allow})
}
