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
	"sync"
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
)

type workspaceTreeCache struct {
	mu      sync.Mutex
	expires time.Time
	root    string
	records []map[string]any
}

var wsTreeCache workspaceTreeCache

func (s *Server) AttachExtraRoutes(routes Routes) {
	if s != nil {
		s.approvalRecovery.Do(func() { go s.recoverResolvedApprovalWaits() })
	}
	api := routes.Group("/api")
	api.Get("/tools", s.handleToolsList)
	api.Get("/models", s.handleModelsList)

	permissions := api.Group("/permissions")
	permissions.Get("/rules", s.handlePermissionRules)
	permissions.Post("/evaluate", s.handlePermissionEvaluate)
	permissions.Get("/explain", s.handlePermissionExplain)
	permissions.Post("/updates", s.handlePermissionUpdate)

	api.Get("/slash/commands", s.handleSlashCommands)
	api.Get("/memories/settings", s.handleMemoriesSettings)
	api.Post("/memories/settings", s.handleMemoriesSettings)
	api.Post("/memories/reset", s.handleMemoriesReset)

	agents := api.Group("/agents")
	agents.Get("/primary", s.handlePrimaryAgents)
	agents.Post("/primary/switch", s.handlePrimaryAgentSwitch)
	agents.Get("/roster", s.handleAgentRoster)
	agents.Post("/cancel-all", s.handleAgentCancelAll)
	agents.Post("/:agentId/cancel", s.handlePrimaryAgentCancel)
	api.Post("/subagents/:id/cancel", s.handleSubagentCancel)

	skills := api.Group("/skills")
	skills.Get("/", s.handleSkillsList)
	skills.Post("/", s.handleSkillsCreate)
	skills.Post("/install", s.handleSkillsInstall)
	skills.Get("/:name", s.handleSkillGet)
	skills.Put("/:name", s.handleSkillsUpdate)
	skills.Post("/toggle", s.handleSkillsToggle)

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
	chatSessions.Get("/:id/mode", s.handleSessionMode)
	chatSessions.Post("/:id/compact", s.handleSessionCompact)
	chatSessions.Post("/:id/rewind-last", s.handleSessionRewindLast)

	api.Post("/internal/worker-event", s.handleWorkerInternalEvent)

	actions := api.Group("/actions")
	actions.Get("/", s.handleActions)
	actions.Post("/ask", s.handleActionsAsk)
	actions.Post("/:id/answer", s.handleActionsAnswer)
	actions.Post("/:id/approve", s.handleActionsApprove)
	actions.Post("/:id/deny", s.handleActionsDeny)

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

	// Project-scoped skill lifecycle: /projects/:id/skills mirrors /skills
	// operation for operation, scoped to the route's project.
	projects.Get("/:id/skills", s.handleProjectSkillsList)
	projects.Post("/:id/skills", s.handleProjectSkillsCreate)
	projects.Post("/:id/skills/install", s.handleProjectSkillsInstall)
	projects.Get("/:id/skills/:name", s.handleProjectSkillGet)
	projects.Put("/:id/skills/:name", s.handleProjectSkillsUpdate)
	projects.Post("/:id/skills/toggle", s.handleProjectSkillsToggle)

	mcp := api.Group("/v1/mcp")
	mcp.Get("/servers", s.handleMCPServersV1)
	mcp.Post("/servers/disable", s.handleMCPServerDisableV1)
	mcp.Post("/servers/enable", s.handleMCPServerEnableV1)
	mcp.Post("/oauth/pkce/start", s.handleMCPOAuthPKCEStart)
	mcp.Post("/oauth/pkce/finish", s.handleMCPOAuthPKCEFinish)

	files := api.Group("/files")
	files.Post("/", s.handleFiles)
	files.Get("/:id", s.handleFileOne)
	files.Get("/:id/download", s.handleFileDownload)
	files.Post("/:id/parse", s.handleFileParse)
	files.Get("/:id/text", s.handleFileText)

	cron := api.Group("/cron")
	cron.Get("/", s.handleCronJobs)
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
	_ = s.RunRT.SetStatus(ctx, wait.RunID, state.RunStatusFailed)
	runRecord, _ := s.RunRT.GetRun(ctx, wait.RunID)
	sessionID := ""
	if runRecord != nil {
		sessionID = runRecord.SessionID
	}
	s.runController().Cancel(wait.RunID, errors.New(reason))
	s.finishRun(ctx, sessionID, wait.RunID)
	_ = s.publishGatewayRunEvent(ctx, sessionID, wait.RunID, "turn_error", event.TurnErrorPayload{Error: reason, Message: reason})
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

func (s *Server) handleWorkspaceTree(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	root := s.activeWorkspaceRoot()
	wsTreeCache.mu.Lock()
	if wsTreeCache.root == root && time.Now().Before(wsTreeCache.expires) && len(wsTreeCache.records) > 0 {
		rec := wsTreeCache.records
		wsTreeCache.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"records": rec})
		return
	}
	wsTreeCache.mu.Unlock()
	type row struct {
		Path  string `json:"path"`
		IsDir bool   `json:"is_dir"`
	}
	var out []row
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, ".git/") || strings.Contains(rel, "/.git/") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		out = append(out, row{Path: rel, IsDir: d.IsDir()})
		return nil
	})
	cacheRows := make([]map[string]any, 0, len(out))
	for _, r := range out {
		cacheRows = append(cacheRows, map[string]any{"path": r.Path, "is_dir": r.IsDir})
	}
	wsTreeCache.mu.Lock()
	wsTreeCache.root = root
	wsTreeCache.records = cacheRows
	wsTreeCache.expires = time.Now().Add(2 * time.Second)
	wsTreeCache.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"records": out})
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
	root := s.activeWorkspaceRoot()
	full := filepath.Clean(filepath.Join(root, filepath.FromSlash(rel)))
	if rr, err := filepath.Rel(root, full); err != nil || strings.HasPrefix(rr, "..") {
		http.Error(w, "invalid path", http.StatusBadRequest)
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

func (s *Server) handlePermissionRules(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	perm := s.permissionFacade()
	if perm == nil {
		http.Error(w, "permissions unavailable", http.StatusServiceUnavailable)
		return
	}
	srcFilter := strings.TrimSpace(r.URL.Query().Get("source"))
	behaviorFilter := strings.TrimSpace(r.URL.Query().Get("behavior"))
	snap := perm.PermissionSnapshotForSession(strings.TrimSpace(r.URL.Query().Get("session_id")))
	type row struct {
		Source      safety.PermissionSource   `json:"source"`
		Behavior    safety.PermissionBehavior `json:"behavior"`
		ToolName    string                    `json:"tool_name"`
		RuleContent string                    `json:"rule_content,omitempty"`
	}
	rules := make([]row, 0, 64)
	for src, byBehavior := range snap.Rules {
		if srcFilter != "" && !strings.EqualFold(string(src), srcFilter) {
			continue
		}
		for behavior, list := range byBehavior {
			if behaviorFilter != "" && !strings.EqualFold(string(behavior), behaviorFilter) {
				continue
			}
			for _, it := range list {
				rules = append(rules, row{
					Source:   src,
					Behavior: behavior,
					ToolName: it.ToolName,
					// A rule's text can live in any of three fields; the engine
					// owns which one names it, so a prefix or a literal command
					// is not shown as a bare tool name here.
					RuleContent: safety.RuleContentDisplay(it),
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
	perm := s.permissionFacade()
	if perm == nil {
		http.Error(w, "permissions unavailable", http.StatusServiceUnavailable)
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
	d := perm.EvaluatePermissionForSession(body.SessionID, body.ToolName, body.Input)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(d)
}

func (s *Server) handlePermissionExplain(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	perm := s.permissionFacade()
	if perm == nil {
		http.Error(w, "permissions unavailable", http.StatusServiceUnavailable)
		return
	}
	toolName := strings.TrimSpace(r.URL.Query().Get("tool_name"))
	input := strings.TrimSpace(r.URL.Query().Get("input"))
	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	if toolName == "" {
		http.Error(w, "tool_name required", http.StatusBadRequest)
		return
	}
	ex := perm.ExplainPermissionForSession(sessionID, toolName, input)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(ex)
}

func (s *Server) handlePermissionUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	perm := s.permissionFacade()
	if perm == nil {
		http.Error(w, "permissions unavailable", http.StatusServiceUnavailable)
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
	if u.Destination == safety.DestinationSession && strings.TrimSpace(u.SessionID) == "" {
		http.Error(w, "session_id required for session destination", http.StatusBadRequest)
		return
	}
	switch u.Destination {
	case safety.DestinationSession, safety.DestinationLocalSettings, safety.DestinationProjectSettings:
	default:
		http.Error(w, "unsupported destination", http.StatusBadRequest)
		return
	}
	if refusal := safety.ExplainRefusedUpdate(u.Destination, u.Behavior); refusal != "" {
		http.Error(w, refusal, http.StatusBadRequest)
		return
	}
	// Refuse an update that would never take effect instead of reporting
	// success for a rule the store will drop.
	if refusal := safety.ExplainRefusedUpdate(u.Destination, u.Behavior); refusal != "" {
		http.Error(w, refusal, http.StatusBadRequest)
		return
	}
	perm.ApplyPermissionUpdate(u)
	if s.Env != nil {
		s.Env.RefreshSandboxRuntime()
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
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
		Title string `json:"title"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body)
	if s.Core != nil {
		res, err := s.Core.CreateSession(r.Context(), body.Title)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
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
	if s.Core == nil && (s.Sessions == nil || s.Sessions.DB() == nil) {
		_ = json.NewEncoder(w).Encode(map[string]any{"records": []any{}})
		return
	}
	type row struct {
		ID         string `json:"id"`
		Title      string `json:"title"`
		CreateTime string `json:"create_time"`
		UpdateTime string `json:"update_time"`
	}
	var (
		summaries []turn.SessionSummary
		err       error
	)
	if s.Core != nil {
		summaries, err = s.Core.ListSessionsRecent(r.Context(), 200)
	} else {
		summaries, err = s.Sessions.ListSessionsRecent(r.Context(), 200)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var out []row
	for _, sum := range summaries {
		t := time.Unix(sum.UpdatedAt, 0).UTC().Format(time.RFC3339)
		out = append(out, row{ID: sum.ID, Title: sessionDisplayTitle(sum), CreateTime: t, UpdateTime: t})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"records": out})
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
		Compaction     *chatCompactionRow  `json:"compaction,omitempty"`
		Goal           *chatGoalRow        `json:"goal,omitempty"`
		PartsJSON      string              `json:"parts_json,omitempty"`
		ToolStepID     string              `json:"tool_step_id,omitempty"`
		ToolMetaJSON   string              `json:"tool_meta_json,omitempty"`
		CreatedAt      int64               `json:"created_at,omitempty"`
		RunID          string              `json:"run_id,omitempty"`
		Attachments    []chatAttachment    `json:"attachments,omitempty"`
	}
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
	for i, t := range turns {
		emitCompactions(i)
		if _, ok := visible[i]; !ok {
			continue
		}
		runID := strings.TrimSpace(t.RunID)
		// Timing at the boundary: a row's own execution window (a tool
		// call, a `!cmd`), or — for the assistant row of a timed run — the
		// run's window and worked time, read from fb_runs through the row.
		runStartedAt, runFinishedAt, workedMs := "", "", int64(0)
		switch {
		case t.ExecStartedAtMs != 0:
			runStartedAt = time.UnixMilli(t.ExecStartedAtMs).UTC().Format(time.RFC3339Nano)
			runFinishedAt = time.UnixMilli(t.ExecFinishedAtMs).UTC().Format(time.RFC3339Nano)
			workedMs = t.ExecDurationMs
		case t.Role == llm.RoleAssistant && t.RunWorkedMs != 0:
			runStartedAt = time.UnixMilli(t.RunStartedAtMs).UTC().Format(time.RFC3339Nano)
			runFinishedAt = time.UnixMilli(t.RunFinishedAtMs).UTC().Format(time.RFC3339Nano)
			workedMs = t.RunWorkedMs
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
			RunID:         runID,
		}
		for _, ref := range state.MessageAttachments(t.PartsJSON) {
			m.Attachments = append(m.Attachments, chatAttachmentOf(ref))
		}
		if citation, found := state.ParseMemoryCitationPart(t.PartsJSON); found {
			m.MemoryCitation = citation
		}
		out = append(out, m)
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
	payload := assembly.CompactResultPayload(res)
	w.Header().Set("Content-Type", "application/json")
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
	_ = json.NewEncoder(w).Encode(body)
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
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"session_id": sid,
		"tool_calls": total,
		"by_tool":    byTool,
		"note":       "model token cost is not natively available yet; this summary tracks tool-call cost surface",
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
		rows = append(rows, s.actionListRow(list[i]))
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

func (s *Server) actionListRow(a state.Action) actionListRow {
	row := actionListRow{Action: a}
	if sugg := s.actionPermissionSuggestion(a); len(sugg) > 0 {
		row.PermissionSuggestion = sugg
	}
	row.AgentID, row.SubagentType = turn.ActionSubagent(&a)
	return row
}

func (s *Server) actionPermissionSuggestion(a state.Action) map[string]any {
	kind := strings.TrimSpace(a.Kind)
	if kind == "" {
		return nil
	}
	base := approvalWSData(s.permissionFacade(), a.SessionID, a.ID, kind, kind, a.PayloadJSON)
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
		_ = s.RunRT.SetStatus(ctx, runID, state.RunStatusFailed)
		s.finishRun(ctx, rn.SessionID, runID)
		_ = s.publishGatewayRunEvent(ctx, rn.SessionID, runID, "turn_error", event.TurnErrorPayload{Error: reason, Message: reason})
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
			_ = s.RunRT.SetStatus(ctx, runID, state.RunStatusFailed)
			s.finishRun(ctx, rn.SessionID, runID)
			_ = s.publishGatewayRunEvent(ctx, rn.SessionID, runID, "turn_error", event.TurnErrorPayload{Error: effectErr.Error(), Message: effectErr.Error()})
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
	resume := turn.BuildApprovalResume(action, w.SessionSnapshot, w.ToolName, clearedContext)
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
		resumeState := &tool.ToolApprovalResumeState{
			Session:    resume.Session,
			Denied:     resume.Denied,
			DenyReason: resume.Reason,
		}
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
		s.finishRun(ctx, sid, runID)
		_ = s.publishGatewayRunEvent(ctx, sid, runID, "turn_error", event.TurnErrorPayload{Error: errText, Message: errText, Detail: newTurnErrorDetail(runErr)})
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
	s.finishRun(ctx, sid, runID)
	_ = s.publishGatewayRunEvent(ctx, sid, runID, "turn_completed", event.TurnCompletedPayload{
		Text:      ans,
		ElapsedMS: elapsed.Milliseconds(),
	})
}

func (s *Server) handleSkillsList(w http.ResponseWriter, r *http.Request) {
	s.handleSkillsListWith(s.gatewayLaunchProject(), w, r)
}

func (s *Server) handleSkillsListWith(launch safety.ProjectContext, w http.ResponseWriter, r *http.Request) {
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
	_ = json.NewEncoder(w).Encode(overview)
}

func (s *Server) handleSkillsCreate(w http.ResponseWriter, r *http.Request) {
	s.handleSkillsCreateWith(s.gatewayLaunchProject(), w, r)
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
	s.handleSkillsUpdateWith(s.gatewayLaunchProject(), w, r)
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
	s.handleSkillGetWith(s.gatewayLaunchProject(), w, r)
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
	s.handleSkillsToggleWith(s.gatewayLaunchProject(), w, r)
}

func (s *Server) handleSkillsToggleWith(launch safety.ProjectContext, w http.ResponseWriter, r *http.Request) {
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
	updated, err := svc.List()
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
	s.handleSkillsInstallWith(s.gatewayLaunchProject(), w, r)
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
	svc.OnRefresh = func() error { return turn.RefreshSkills(svc.Home, svc.Workspace(), launch) }
	svc.IsBuiltin = turn.IsBuiltinName
	return svc, nil
}

// gatewayLaunchProject is the launch project this gateway process was started
// in, frozen with its runtime. It is the project the global (non-project-
// scoped) skill routes act on: a gateway serving one checkout — the common
// case — resolves it here exactly as its runner does, and a gateway serving
// several projects reaches each one through /projects/:id/skills instead.
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
	launch, err := safety.ResolveProjectContext(s.Home, p.Root)
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
	s.handleSkillsListWith(launch, w, r)
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
	s.handleSkillsToggleWith(launch, w, r)
}

func (s *Server) handleProjectSkillsInstall(w http.ResponseWriter, r *http.Request) {
	launch, ok := s.projectSkillLaunch(w, r)
	if !ok {
		return
	}
	s.handleSkillsInstallWith(launch, w, r)
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
		providers := append([]config.AgentLLMProviderConfig(nil), cfg.Agents.Definitions[active.ID].LLMProviders...)
		config.RedactSecrets(&providers)
		writeAgentsJSON(w, map[string]any{"agent_id": active.ID, "providers": providers})
	case http.MethodPut:
		var req struct {
			Providers []config.AgentLLMProviderConfig `json:"providers"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		config.ClearRedactedSecrets(&req.Providers)
		cfg, err := s.persistedConfig()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if cfg.Agents.Definitions == nil {
			cfg.Agents.Definitions = map[string]config.AgentDefinition{}
		}
		def := cfg.Agents.Definitions[active.ID]
		def.LLMProviders = req.Providers
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
		writeAgentsJSON(w, map[string]any{"agent_id": agentID, "records": jobs})
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
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(projectRow(p))
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
// pooled runner serves it.
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
	if s.Sessions != nil {
		// The session row records the project root as its cwd so history and
		// memory scopes read the directory the conversation is about.
		s.Sessions.ConfigureMemoryDefaults(memory.SessionModeForConfig(s.liveCfg()), memory.SessionSourceWebchat, p.Root, memory.GitBranch(p.Root))
	}
	var res struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	if s.Core != nil {
		out, err := s.Core.CreateSession(r.Context(), title)
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
		if err := s.Sessions.Ensure(r.Context(), res.ID, stored); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	} else {
		http.Error(w, "sessions disabled", http.StatusServiceUnavailable)
		return
	}
	if err := store.BindSession(r.Context(), res.ID, p.ID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if s.Env != nil {
		s.Env.EnsureRunnerPool()
		if pool := s.Env.RunnerPool(); pool != nil {
			if err := pool.BindSession(r.Context(), res.ID, p.ID); err == nil {
				// Pre-building the runner here means the first message in the
				// new session does not pay for it. A failure is not fatal:
				// the first turn resolves lazily instead.
				_ = err
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(res)
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
