package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	"github.com/forebrain-harness/forebrain-harness/pkg/assembly"
	"github.com/forebrain-harness/forebrain-harness/pkg/channel"
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
	// Web sign-in for the providers the models page can log in to: start a
	// flow and get an id back, poll that id for the authorization URL and the
	// ending, and cancel a login the page walked away from.
	auth := api.Group("/auth")
	auth.Post("/login/:provider", s.handleAuthLoginStart)
	auth.Get("/login/:provider/:loginId", s.handleAuthLoginStatus)
	auth.Post("/login/:provider/:loginId/cancel", s.handleAuthLoginCancel)
	// The redirect a ChatGPT sign-in sends the browser to is a localhost
	// address, so a gateway on another machine never sees it: the page asks the
	// user to paste the address back here instead.
	auth.Post("/login/:provider/:loginId/callback", s.handleAuthLoginCallback)
	api.Get("/tools", s.handleToolsList)
	api.Get("/models", s.handleModelsList)

	// The machine's own filesystem, for the new-chat page's folder picker: list
	// a directory's subdirectories, create one, and read/write a directory's
	// trust decision. All behind the same control-plane auth as every /api route.
	host := api.Group("/host")
	host.Get("/dirs", s.handleHostDirs)
	host.Post("/dirs", s.handleHostDirs)
	host.Get("/trust", s.handleHostTrust)
	host.Post("/trust", s.handleHostTrust)

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
	// One primary agent's own MCP servers (agents.definitions.<id>.mcp_servers),
	// managed from the web on the same terms as the shared list.
	agents.Post("/primary/:id/mcp/servers", s.handleMCPAgentServersWrite)
	agents.Post("/primary/:id/mcp/servers/test", s.handleMCPAgentServersTest)
	agents.Put("/primary/:id/mcp/servers/:name", s.handleMCPAgentServersWrite)
	agents.Delete("/primary/:id/mcp/servers/:name", s.handleMCPAgentServersWrite)
	// One primary agent's subagent configuration (plan 079): the built-ins its
	// model may dispatch, switched off or repointed one by one, and the custom
	// subagents it defines. The defaults endpoints carry the global switch the
	// settings page owns; the model options are the same list the conversation's
	// model menu offers, for the drawer's model picker.
	agents.Get("/defaults/subagents", s.handleSubagentsDefaultGet)
	agents.Put("/defaults/subagents", s.handleSubagentsDefaultPut)
	agents.Get("/primary/:id/model-options", s.handleAgentModelOptions)
	agents.Get("/primary/:id/subagents", s.handleAgentSubagentsList)
	agents.Post("/primary/:id/subagents", s.handleAgentSubagentCreate)
	agents.Put("/primary/:id/subagent-switch", s.handleAgentSubagentSwitch)
	agents.Get("/primary/:id/subagents/:name", s.handleAgentSubagentGet)
	agents.Put("/primary/:id/subagents/:name", s.handleAgentSubagentUpdate)
	agents.Delete("/primary/:id/subagents/:name", s.handleAgentSubagentDelete)
	agents.Put("/primary/:id/subagents/:name/enabled", s.handleAgentSubagentEnabled)
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
	// Drafts and versions: the same lifecycle, addressed by name under the
	// agent's own route set.
	skills.Get("/:name/versions", s.handleSkillVersions)
	skills.Post("/:name/publish", s.handleSkillPublish)
	skills.Post("/:name/draft/discard", s.handleSkillDraftDiscard)
	skills.Post("/:name/versions/:v/restore", s.handleSkillVersionRestore)
	skills.Post("/:name/pull", s.handleSkillPull)
	skills.Get("/:name/diff", s.handleSkillDiff)
	skills.Get("/:name/versions/:v/download", s.handleSkillVersionDownload)
	// Workshop artifacts: what an evaluation or trigger run left behind,
	// read back for the workbench's evaluation and trigger tabs.
	skills.Get("/:name/workshop/evals", s.handleSkillWorkshopEvals)
	skills.Put("/:name/workshop/evals/:iteration/feedback", s.handleSkillWorkshopFeedback)
	skills.Get("/:name/workshop/trigger", s.handleSkillWorkshopTrigger)

	workspace := api.Group("/workspace")
	workspace.Get("/tree", s.handleWorkspaceTree)
	workspace.Get("/snippet", s.handleWorkspaceSnippet)
	workspace.Get("/mentions", s.handleMentionSearch)
	workspace.Post("/mentions/accept", s.handleMentionAccept)
	// The workbench's file write endpoints (plan 017): read, write/create,
	// rename and delete, all rooted by filesRoot and contained by
	// ResolveWithinRoots. A path inside a versioned skill's formal directory
	// is redirected into that skill's draft.
	workspace.Get("/file", s.handleWorkspaceFileRead)
	// The image pixel channel: the same file view, served as bytes so a surface
	// can draw a thumbnail by path (never base64 in an event or JSON response).
	workspace.Get("/file/raw", s.handleWorkspaceFileRaw)
	workspace.Put("/file", s.handleWorkspaceFileWrite)
	workspace.Post("/rename", s.handleWorkspaceRename)
	workspace.Delete("/entry", s.handleWorkspaceEntryDelete)

	// The model a conversation that does not exist yet would start on, with the
	// same options and levels a session's own read lists: the new-chat page
	// draws its model and effort chips from it and carries the pick into the
	// session its first message opens.
	api.Get("/chat/model", s.handleChatModelDefault)

	chatSessions := api.Group("/chat/sessions")
	chatSessions.Get("/", s.handleChatSessions)
	chatSessions.Post("/", s.handleChatSessionCreate)
	chatSessions.Post("/:id/title", s.handleChatSessionTitle)
	chatSessions.Get("/:id/messages", s.handleChatMessages)
	// What one user message attached, by the message that holds it: the route a
	// path-recorded attachment (the terminal's pasted screenshot) is opened by.
	chatSessions.Get("/:id/messages/:row/attachments/:n", s.handleChatMessageAttachment)
	chatSessions.Get("/:id/events", s.handleChatSessionEvents)
	chatSessions.Get("/:id/context", s.handleChatSessionContext)
	chatSessions.Get("/:id/tool-audit", s.handleSessionToolAudit)
	chatSessions.Get("/:id/cost-summary", s.handleSessionCostSummary)
	chatSessions.Get("/:id/subagent-history", s.handleSessionSubagentHistory)
	chatSessions.Get("/:id/todos", s.handleSessionTodos)
	chatSessions.Get("/:id/plan-md", s.handleSessionPlanMarkdown)
	chatSessions.Get("/:id/plans", s.handleSessionPlans)
	chatSessions.Get("/:id/tool-output/:retrieve", s.handleChatToolOutput)
	chatSessions.Get("/:id/subagents/:agent/tool-output/:retrieve", s.handleSubagentToolOutput)
	chatSessions.Get("/:id/approval-request", s.handleSessionApprovalRequest)
	chatSessions.Get("/:id/mode", s.handleSessionMode)
	// The conversation's own model choice, session-scoped like /model's live
	// switch: the runner's published default is never touched here.
	chatSessions.Get("/:id/model", s.handleChatSessionModelGet)
	chatSessions.Put("/:id/model", s.handleChatSessionModelPut)
	chatSessions.Get("/:id", s.handleChatSession)
	chatSessions.Post("/:id/compact", s.handleSessionCompact)
	// "Rewind to before a message" (plan 033): preview, run, undo, and the
	// workbench's changed-files list. All three modes and both surfaces run the
	// same pkg/turn engine.
	chatSessions.Get("/:id/rewind-preview", s.handleSessionRewindPreview)
	chatSessions.Post("/:id/rewind", s.handleSessionRewind)
	chatSessions.Post("/:id/rewind/undo", s.handleSessionRewindUndo)
	chatSessions.Get("/:id/file-changes", s.handleSessionFileChanges)
	chatSessions.Post("/:id/file-changes/revert", s.handleSessionFileChangesRevert)
	chatSessions.Get("/:id/file-changes/diff", s.handleSessionFileChangesDiff)
	// A subagent's own view talks to that subagent through these — the same
	// semantics the terminal's subagent view has (plan 007), decided in the
	// engine (plan 005): steer, recall, interrupt-and-send, context, and the
	// budget it opens with. There is no withdraw (a view only steers a running
	// execution) and no compact (a subagent's own summary is not the user's to
	// run) — plan 142.
	chatSessions.Post("/:id/subagents/:agent/input", s.handleSubagentInput)
	chatSessions.Post("/:id/subagents/:agent/queued-input", s.handleSubagentQueuedInput)
	chatSessions.Post("/:id/subagents/:agent/interrupt-send", s.handleSubagentInterruptSend)
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
	projects.Get("/:id/delete-preview", s.handleProjectDeletePreview)
	projects.Post("/:id/pin", s.handleProjectPin)
	// The "save to" dialog's view of a project's own directories: list one
	// directory's subdirectories, and make a new one.
	projects.Get("/:id/dirs", s.handleProjectDirs)
	projects.Post("/:id/dirs", s.handleProjectDirs)
	projects.Get("/:id/sessions", s.handleProjectSessionsList)
	projects.Post("/:id/sessions", s.handleProjectSessionsCreate)
	projects.Get("/:id/mcp", s.handleProjectMCPPreview)
	projects.Post("/:id/mcp/consent", s.handleProjectMCPConsent)
	// The project's own MCP file, edited from the web: create, update, delete,
	// and a connection test that writes nothing.
	projects.Post("/:id/mcp/servers", s.handleProjectMCPServersWrite)
	projects.Post("/:id/mcp/servers/test", s.handleProjectMCPServersTest)
	projects.Put("/:id/mcp/servers/:name", s.handleProjectMCPServersWrite)
	projects.Delete("/:id/mcp/servers/:name", s.handleProjectMCPServersWrite)
	projects.Get("/:id/lsp", s.handleProjectLSPPreview)
	projects.Post("/:id/lsp/consent", s.handleProjectLSPConsent)
	// The one-screen project servers confirmation the session view opens, its
	// answer, and the project page's "always allow" switch.
	projects.Get("/:id/servers/pending", s.handleProjectServersPending)
	projects.Post("/:id/servers/consent", s.handleProjectServersConsent)
	projects.Put("/:id/servers/auto-allow", s.handleProjectServersAutoAllow)

	// Scheduled tasks and channels belong to a project, so their routes hang
	// off the project they configure.
	projects.Get("/:id/cron/jobs", s.handleCronJobs)
	projects.Post("/:id/cron/jobs", s.handleCronJobs)
	projects.Post("/:id/cron/preview", s.handleCronPreview)
	projects.Get("/:id/cron/jobs/:jobId", s.handleCronJob)
	projects.Put("/:id/cron/jobs/:jobId", s.handleCronJob)
	projects.Delete("/:id/cron/jobs/:jobId", s.handleCronJob)
	projects.Post("/:id/cron/jobs/:jobId/run", s.handleCronJobRun)
	projects.Get("/:id/cron/jobs/:jobId/runs", s.handleCronJobRuns)
	projects.Get("/:id/channels", s.handleChannels)
	projects.Put("/:id/channels", s.handleChannels)

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
	// Project-scoped drafts and versions, mirroring the agent's routes.
	projects.Get("/:id/skills/:name/versions", s.handleProjectSkillVersions)
	projects.Post("/:id/skills/:name/publish", s.handleProjectSkillPublish)
	projects.Post("/:id/skills/:name/draft/discard", s.handleProjectSkillDraftDiscard)
	projects.Post("/:id/skills/:name/versions/:v/restore", s.handleProjectSkillVersionRestore)
	projects.Post("/:id/skills/:name/pull", s.handleProjectSkillPull)
	projects.Get("/:id/skills/:name/diff", s.handleProjectSkillDiff)
	projects.Get("/:id/skills/:name/versions/:v/download", s.handleProjectSkillVersionDownload)
	projects.Get("/:id/skills/:name/workshop/evals", s.handleProjectSkillWorkshopEvals)
	projects.Put("/:id/skills/:name/workshop/evals/:iteration/feedback", s.handleProjectSkillWorkshopFeedback)
	projects.Get("/:id/skills/:name/workshop/trigger", s.handleProjectSkillWorkshopTrigger)
	projects.Get("/:id/permissions/rules", s.handleProjectPermissionRules)
	projects.Post("/:id/permissions/updates", s.handleProjectPermissionUpdate)
	projects.Get("/:id/permissions/explain", s.handleProjectPermissionExplain)

	mcp := api.Group("/v1/mcp")
	mcp.Get("/servers", s.handleMCPServersV1)
	// The shared MCP list in forebrain.yaml: create, update, delete, and a
	// connection test that writes nothing.
	mcp.Post("/servers", s.handleMCPGlobalServersWrite)
	mcp.Post("/servers/test", s.handleMCPServersTest)
	mcp.Put("/servers/:name", s.handleMCPGlobalServersWrite)
	mcp.Delete("/servers/:name", s.handleMCPGlobalServersWrite)
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

	api.Get("/heartbeats", s.handleHeartbeats)
	api.Get("/heartbeat", s.handleHeartbeat)
	api.Put("/heartbeat", s.handleHeartbeat)
	api.Delete("/heartbeat", s.handleHeartbeat)

	api.Get("/auto-continue", s.handleAutoContinue)
	api.Delete("/auto-continue", s.handleAutoContinue)

	api.Get("/config", s.handleConfigFile)
	api.Put("/config", s.handleConfigFile)
	// What the settings page's runtime tab shows: the facts only the gateway
	// knows. The version and the address the browser reached are the
	// browser's own.
	api.Get("/runtime", s.handleRuntimeInfo)
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

func (s *Server) handleWorkspaceTree(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	root, full, err := s.resolveFilesPath(r.Context(), q.Get("session_id"), q.Get("project_id"), strings.TrimSpace(q.Get("path")))
	if err != nil {
		if errors.Is(err, tool.ErrPathNotAllowed) {
			http.Error(w, "path is outside the workspace", http.StatusBadRequest)
			return
		}
		writeFilesRootError(w, err)
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
		// The version store's history is not workspace content: it never
		// shows in the tree, exactly like .git.
		if name == ".git" || name == ".versions" {
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
		// The endpoint each provider defaults to. It rides with the catalog
		// because it is catalog metadata, not a per-provider setting: a form
		// offering the providers fills the base URL in from here instead of
		// keeping its own copy of the table.
		"provider_base_urls": providerBaseURLDefaults(),
	})
}

// providerBaseURLDefault is one provider's default endpoint, as the listing
// reports it.
type providerBaseURLDefault struct {
	Provider string `json:"provider"`
	BaseURL  string `json:"base_url"`
}

// providerBaseURLDefaults lists the table in provider order. A list rather
// than an object: a client that camel-cases response keys would rewrite an
// object's keys, and a provider id is data — arcee-ai must not arrive as
// arceeAi.
func providerBaseURLDefaults() []providerBaseURLDefault {
	table := config.ProviderDefaultBaseURLTable()
	providers := make([]string, 0, len(table))
	for provider := range table {
		providers = append(providers, provider)
	}
	sort.Strings(providers)
	out := make([]providerBaseURLDefault, 0, len(providers))
	for _, provider := range providers {
		out = append(out, providerBaseURLDefault{Provider: provider, BaseURL: table[provider]})
	}
	return out
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
	q := r.URL.Query()
	rel := strings.TrimSpace(q.Get("path"))
	if rel == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}
	_, full, resolveErr := s.resolveFilesPath(r.Context(), q.Get("session_id"), q.Get("project_id"), rel)
	if resolveErr != nil {
		if errors.Is(resolveErr, tool.ErrPathNotAllowed) {
			http.Error(w, "path is outside the workspace", http.StatusBadRequest)
			return
		}
		writeFilesRootError(w, resolveErr)
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

// toolMetas is the tool table the active primary agent's runtime exposes, or
// nothing when there is no runtime to read it from.
func (s *Server) toolMetas() []event.ToolMeta {
	if s.Runner == nil || s.Env.Tools() == nil {
		return []event.ToolMeta{}
	}
	return s.Env.Tools().ToolMetas()
}

// toolMetaMatches is the tool table's search box: it reads the tool's own name
// and the description the model is given.
func toolMetaMatches(meta event.ToolMeta, q string) bool {
	needle := strings.ToLower(q)
	return strings.Contains(strings.ToLower(meta.Name), needle) ||
		strings.Contains(strings.ToLower(meta.Description), needle)
}

func (s *Server) handleToolsList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	metas := s.toolMetas()
	q := readListQuery(r)
	if !q.Paged {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(metas)
		return
	}
	// The category dropdown offers every category the runtime exposes, not
	// only the ones the current filters leave: a filter that could hide the
	// choice that undoes it would be a trap.
	categories := []string{}
	seen := map[string]bool{}
	for _, meta := range metas {
		category := strings.TrimSpace(meta.Category)
		if category == "" || seen[category] {
			continue
		}
		seen[category] = true
		categories = append(categories, category)
	}
	sort.Strings(categories)
	categoryFilter := strings.TrimSpace(r.URL.Query().Get("category"))
	typeFilter := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("type")))
	matched := make([]event.ToolMeta, 0, len(metas))
	for _, meta := range metas {
		if categoryFilter != "" && !strings.EqualFold(strings.TrimSpace(meta.Category), categoryFilter) {
			continue
		}
		switch typeFilter {
		case "readonly":
			if !meta.ReadOnly {
				continue
			}
		case "write":
			if meta.ReadOnly {
				continue
			}
		}
		if q.Q != "" && !toolMetaMatches(meta, q.Q) {
			continue
		}
		matched = append(matched, meta)
	}
	sort.SliceStable(matched, func(i, j int) bool {
		return strings.ToLower(matched[i].Name) < strings.ToLower(matched[j].Name)
	})
	page, servedPage := pageOf(matched, q)
	writeListPage(w, "tools", page, len(matched), servedPage, q, map[string]any{"categories": categories})
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
	q := readListQuery(r)
	if !q.Paged {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"mode":  snap.Mode,
			"rules": rules,
		})
		return
	}
	// The search box reads the tool the rule names and the rule's own text.
	needle := strings.ToLower(q.Q)
	matched := make([]permissionRuleRow, 0, len(rules))
	for _, rule := range rules {
		if q.Q != "" &&
			!strings.Contains(strings.ToLower(rule.ToolName), needle) &&
			!strings.Contains(strings.ToLower(rule.RuleContent), needle) {
			continue
		}
		matched = append(matched, rule)
	}
	page, servedPage := pageOf(matched, q)
	writeListPage(w, "rules", page, len(matched), servedPage, q, map[string]any{"mode": snap.Mode})
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
	rec, err := s.createUpload(r.Context(), sessionID, r.FormValue("dir"), filename, fh, mediaType)
	if err != nil {
		writeUploadError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"file_id": rec.ID})
}

// createUpload saves one upload for a session. On the local backend the file is
// written into the directory dir named under the session's project root (an
// empty dir is the root itself, which must exist — the new-folder endpoint is
// what makes a directory, so an upload never creates one implicitly) and the
// project remembers dir as its last attach directory. The S3 backend has no
// project path: it ignores dir and keeps its workspace-backed behaviour.
func (s *Server) createUpload(ctx context.Context, sessionID, dir, filename string, body io.Reader, mediaType string) (*state.File, error) {
	if s.Files == nil {
		return nil, fmt.Errorf("files disabled")
	}
	if s.Files.Cfg.DefaultStorageBackend == state.StorageBackendS3 {
		return s.Files.CreateFromReader(ctx, sessionID, filename, body, 0, mediaType)
	}
	store := s.projectStore()
	if store == nil {
		return nil, errProjectRequired
	}
	project, ok, err := store.ProjectForSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if !ok {
		// A conversation with no project — a primary-agent workshop task —
		// has nowhere in a project to save an attachment to.
		return nil, errProjectRequired
	}
	root, err := existingFilesRoot(project.Root)
	if err != nil {
		return nil, err
	}
	rel, abs, err := projectDir(root, dir)
	if err != nil {
		return nil, err
	}
	if st, statErr := os.Stat(abs); statErr != nil || !st.IsDir() {
		return nil, errUploadDirMissing
	}
	rec, err := s.Files.CreateInProject(ctx, sessionID, root, rel, filename, body, 0, mediaType)
	if err != nil {
		return nil, err
	}
	// The dialog reopens here. A failure to remember is not worth failing the
	// upload over: the file is already saved.
	_ = store.SetLastAttachDir(ctx, project.ID, rel)
	return rec, nil
}

// writeUploadError maps an upload's failure onto a status: a conversation with
// no project is the session-create refusal, a directory outside the project or
// missing is a 400, and anything else keeps the 400 the endpoint has always
// answered a bad upload with.
func writeUploadError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errProjectRequired):
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "project_required"})
	case errors.Is(err, errUploadDirOutside), errors.Is(err, errUploadDirMissing):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		http.Error(w, err.Error(), http.StatusBadRequest)
	}
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
	p, ok := s.Files.OriginalPath(r.Context(), f)
	if ok && attachmentWantsInline(r) {
		// A page drawing the file in place asks for it; the file is shown only
		// when it is a kind a browser can draw safely, a download otherwise.
		if file, openErr := os.Open(p); openErr == nil {
			defer file.Close()
			serveAttachmentFile(w, r, file, f.OriginalName, true)
			return
		}
	}
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

// createSessionResult is what both session-create endpoints answer with: the
// new conversation's id and title, plus — when it was opened into a project —
// the project row and whether opening it also created the project.
type createSessionResult struct {
	ID      string         `json:"id"`
	Title   string         `json:"title"`
	Project map[string]any `json:"project,omitempty"`
	Created bool           `json:"created,omitempty"`
}

// errSessionsDisabled reports a create request reaching a server with neither
// a turn service nor a session store wired.
var errSessionsDisabled = errors.New("sessions disabled")

// createSessionWithBirth opens a conversation born into one project: its
// directory is the project root and its project binding is fixed at birth, so
// the memory pipeline and the runner pool agree about where it runs from its
// first turn. It is the single path both create endpoints take, project space
// and new-chat page alike, so a session's identity never depends on which
// route opened it.
func (s *Server) createSessionWithBirth(ctx context.Context, title string, p state.Project, source string) (createSessionResult, error) {
	birth := state.SessionBirth{
		Cwd:       p.Root,
		GitBranch: memory.GitBranch(p.Root),
		ProjectID: p.ID,
		Source:    source,
	}
	var res createSessionResult
	switch {
	case s.Core != nil:
		out, err := s.Core.CreateSessionAt(ctx, title, birth)
		if err != nil {
			return res, err
		}
		res.ID, res.Title = out.ID, out.Title
	case s.Sessions != nil:
		id := state.NewID("web")
		stored := title
		if stored == "" {
			stored = id
		}
		if err := s.Sessions.EnsureAt(ctx, id, stored, birth); err != nil {
			return res, err
		}
		res.ID, res.Title = id, title
	default:
		return res, errSessionsDisabled
	}
	if err := s.bindSessionToProject(ctx, res.ID, p); err != nil {
		return res, err
	}
	return res, nil
}

func (s *Server) handleChatSessionCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Title     string `json:"title"`
		Source    string `json:"source"`
		ProjectID string `json:"project_id"`
		Folder    string `json:"folder"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body)
	// The session-purpose whitelist stays here, at the API edge; the store
	// writes whatever it is handed.
	source := strings.TrimSpace(body.Source)
	switch source {
	case state.SessionSourceConversation, state.SessionSourceWorkshop:
	default:
		http.Error(w, "source must be empty or workshop", http.StatusBadRequest)
		return
	}

	// The conversation must belong to a project: one named by id, or one for a
	// folder the caller chose (created from the folder when none exists). A
	// primary-agent-level skill-workshop task is the one exception.
	projectID := strings.TrimSpace(body.ProjectID)
	folder := strings.TrimSpace(body.Folder)
	var project state.Project
	var created bool
	haveProject := false
	switch {
	case projectID != "" && folder != "":
		http.Error(w, "project_id and folder are mutually exclusive", http.StatusBadRequest)
		return
	case projectID != "":
		store := s.projectStore()
		if store == nil {
			http.Error(w, "projects unavailable", http.StatusServiceUnavailable)
			return
		}
		p, err := store.Get(r.Context(), projectID)
		if err != nil {
			http.Error(w, "project not found", http.StatusNotFound)
			return
		}
		if !hostDirExists(p.Root) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "session_directory_missing"})
			return
		}
		project, haveProject = p, true
	case folder != "":
		store := s.projectStore()
		if store == nil {
			http.Error(w, "projects unavailable", http.StatusServiceUnavailable)
			return
		}
		root, err := state.ValidateProjectRoot(s.Home, folder)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		p, wasCreated, err := store.FindOrCreateByRoot(r.Context(), root, state.CreateProjectInput{})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		project, created, haveProject = p, wasCreated, true
	default:
		if source != state.SessionSourceWorkshop {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "project_required"})
			return
		}
	}

	if haveProject {
		res, err := s.createSessionWithBirth(r.Context(), body.Title, project, source)
		if err != nil {
			if errors.Is(err, errSessionsDisabled) {
				http.Error(w, "sessions disabled", http.StatusServiceUnavailable)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		res.Project = s.projectRow(project)
		res.Created = created
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(res)
		return
	}

	// No project: the primary-agent-level skill-workshop task, whose session
	// carries no directory of its own.
	if s.Core != nil {
		res, err := s.Core.CreateSession(r.Context(), body.Title)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if source != "" {
			if s.Sessions == nil {
				http.Error(w, "session source unavailable", http.StatusServiceUnavailable)
				return
			}
			if err := s.Sessions.SetSessionSource(r.Context(), res.ID, source); err != nil {
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
	if source != "" {
		if err := s.Sessions.SetSessionSource(r.Context(), res.ID, source); err != nil {
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

// handleChatSessions serves the two chat session lists the web reads. The
// drawer's (an empty source) is every conversation the agent may reopen —
// ordinary conversations across every project plus the primary agent's
// project-less skill-workshop tasks, with the project-less ordinary rows of
// pre-project versions left out — searched and paged on the server the way
// every list endpoint is. The workshop page's list (source=workshop) stays
// the primary agent's own tasks, served exactly as it always was: whole,
// unpaged, unsearched.
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
	if source == state.SessionSourceWorkshop {
		summaries, err := s.Sessions.ListSessionsOfSource(r.Context(), source, 200)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// The workshop list's answer keeps the shape it always had; its rows
		// are project-less by definition, so they carry no project name.
		type workshopRow struct {
			ID         string `json:"id"`
			Title      string `json:"title"`
			CreateTime string `json:"create_time"`
			UpdateTime string `json:"update_time"`
			Source     string `json:"source,omitempty"`
		}
		out := make([]workshopRow, 0, len(summaries))
		for _, sum := range summaries {
			t := time.Unix(sum.UpdatedAt, 0).UTC().Format(time.RFC3339)
			out = append(out, workshopRow{ID: sum.ID, Title: sessionDisplayTitle(sum), CreateTime: t, UpdateTime: t, Source: sum.Source})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"records": out})
		return
	}
	// The drawer's list: paged only when the request carries "page", the
	// list contract every table endpoint follows; without it the answer
	// keeps the shape it always gave, one window of the newest 100 rows.
	q := readListQuery(r)
	limit, page := 100, 1
	if q.Paged {
		limit, page = q.PageSize, q.Page
	}
	// project_id narrows the drawer to one project's conversations; the
	// filter is the server's, so the count and the pages follow it.
	projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
	summaries, total, err := s.Sessions.ListConversationsPage(r.Context(), q.Q, projectID, limit, (page-1)*limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	names, err := s.projectNamesFor(r.Context(), summaries)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	type row struct {
		ID          string `json:"id"`
		Title       string `json:"title"`
		CreateTime  string `json:"create_time"`
		UpdateTime  string `json:"update_time"`
		Source      string `json:"source,omitempty"`
		ProjectName string `json:"project_name"`
	}
	out := make([]row, 0, len(summaries))
	for _, sum := range summaries {
		t := time.Unix(sum.UpdatedAt, 0).UTC().Format(time.RFC3339)
		out = append(out, row{ID: sum.ID, Title: sessionDisplayTitle(sum), CreateTime: t, UpdateTime: t, Source: sum.Source, ProjectName: names[sum.ProjectID]})
	}
	if !q.Paged {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"records": out})
		return
	}
	// The store read the last page when the request asked past it; the answer
	// names the page it actually served.
	servedPage := min(q.Page, max((total+q.PageSize-1)/q.PageSize, 1))
	writeListPage(w, "records", out, total, servedPage, q, nil)
}

// projectNamesFor names the projects one page of drawer rows belongs to, one
// lookup per distinct project on the page (at most the page size). The
// project store draws the agent boundary the session store does, so a row
// the agent cannot name — another tenant's project — shows no project line.
func (s *Server) projectNamesFor(ctx context.Context, summaries []state.SessionSummary) (map[string]string, error) {
	names := make(map[string]string, len(summaries))
	store := s.projectStore()
	if store == nil {
		return names, nil
	}
	for _, sum := range summaries {
		if sum.ProjectID == "" {
			continue
		}
		if _, seen := names[sum.ProjectID]; seen {
			continue
		}
		p, err := store.Get(ctx, sum.ProjectID)
		if errors.Is(err, state.ErrProjectNotFound) {
			names[sum.ProjectID] = ""
			continue
		}
		if err != nil {
			return nil, err
		}
		names[sum.ProjectID] = p.Name
	}
	return names, nil
}

// handleChatSession names one conversation for the page that has it open:
// its title and the project it belongs to. The page reads this rather than
// looking itself up in the drawer's list — a drawer row carries only id,
// title, times and project name, and a scheduled task's fire, for one, is
// never in it.
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
			// Root and missing say whether the conversation can still run: a
			// directory that is gone leaves the page its read-only notice.
			project = map[string]any{"id": p.ID, "name": p.Name, "root": p.Root, "missing": !hostDirExists(p.Root), "last_attach_dir": p.LastAttachDir}
		}
	}
	writeJSON(w, map[string]any{"id": sid, "title": title, "project": project})
}

// sessionModelOption is one model this conversation's primary agent is
// configured with, as the session's model chip offers it: the configured pair
// plus the reasoning levels and image support the shared catalog reports for
// it.
type sessionModelOption struct {
	Provider       string   `json:"provider"`
	Model          string   `json:"model"`
	Label          string   `json:"label"`
	Efforts        []string `json:"efforts"`
	SupportsImages bool     `json:"supports_images"`
}

// sessionModelEfforts names the reasoning levels one model offers: the
// levels the shared model catalog — the same listing /api/models serves —
// reported for it when it reported any, else the levels the configured
// choice itself carries (turn.ModelReasoningEfforts, which reads both the
// catalog and this repository's OpenCode tables), and none at all for a model
// with nothing to pick. The catalog is the single source: no list is invented
// here.
func sessionModelEfforts(records []turn.ModelRecord, fallback []string, provider, model string) []string {
	model = strings.TrimSpace(model)
	for _, rec := range records {
		if strings.EqualFold(strings.TrimSpace(rec.Provider), strings.TrimSpace(provider)) && strings.TrimSpace(rec.APIModel) == model {
			out := make([]string, 0, len(rec.ReasoningEfforts))
			for _, effort := range rec.ReasoningEfforts {
				if effort = strings.TrimSpace(effort); effort != "" {
					out = append(out, effort)
				}
			}
			if len(out) > 0 {
				return out
			}
		}
	}
	if len(fallback) == 0 {
		return nil
	}
	return append([]string(nil), fallback...)
}

// sessionModelSupportsImages reports whether one configured option accepts
// image input: the shared catalog listing's record for it when there is one
// (models.json, or the ChatGPT account's input modalities), else the
// configured choice's own catalog entry.
func sessionModelSupportsImages(records []turn.ModelRecord, choice turn.ModelChoice) bool {
	model := strings.TrimSpace(choice.Model)
	for _, rec := range records {
		if strings.EqualFold(strings.TrimSpace(rec.Provider), strings.TrimSpace(choice.Provider)) && strings.TrimSpace(rec.APIModel) == model {
			return rec.SupportsAttachments
		}
	}
	return choice.SupportsImages
}

// sessionModelOptions lists the models this conversation's agent is configured
// with — the same list /model offers — each carrying its reasoning levels.
func (s *Server) sessionModelOptions(cfg *config.Root, agentName string) []sessionModelOption {
	var records []turn.ModelRecord
	if s != nil && s.Core != nil {
		records = s.Core.ListModelCatalog(turn.ModelCatalogQuery{})
	}
	choices := turn.ModelChoices(cfg, agentName)
	options := make([]sessionModelOption, 0, len(choices))
	for _, choice := range choices {
		options = append(options, sessionModelOption{
			Provider:       choice.Provider,
			Model:          choice.Model,
			Label:          choice.Label,
			Efforts:        sessionModelEfforts(records, choice.ReasoningEfforts, choice.Provider, choice.Model),
			SupportsImages: sessionModelSupportsImages(records, choice),
		})
	}
	return options
}

// sessionModelEffortsFor names the reasoning levels one configured option
// offers, straight from the shared catalog.
func (s *Server) sessionModelEffortsFor(cfg *config.Root, agentName, provider, model string) []string {
	for _, option := range s.sessionModelOptions(cfg, agentName) {
		if strings.EqualFold(option.Provider, provider) && strings.EqualFold(option.Model, model) {
			return option.Efforts
		}
	}
	return nil
}

// handleChatSessionModelGet serves one conversation's effective model choice
// and the options it may be switched to. The marker is the same resolution
// /model draws (stored row validated against the session's runner, falling
// back to the runner's published selection), so both surfaces agree about
// which model a conversation runs on.
func (s *Server) handleChatSessionModelGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	sid := strings.TrimSpace(ParamsFromContext(r.Context()).ByName("id"))
	if sid == "" {
		http.NotFound(w, r)
		return
	}
	runner := s.runnerFor(r.Context(), sid)
	if runner == nil {
		http.Error(w, "no runner is bound to this session", http.StatusServiceUnavailable)
		return
	}
	cfg := s.liveCfg()
	if cfg == nil {
		http.Error(w, "no configuration is bound", http.StatusServiceUnavailable)
		return
	}
	agentName := "main"
	if name := strings.TrimSpace(runner.AgentName); name != "" {
		agentName = name
	}
	sel := s.sessionModelSelection(runner, sid)
	writeJSON(w, map[string]any{
		"provider": sel.Provider,
		"model":    sel.Model,
		"effort":   sel.Effort,
		"options":  s.sessionModelOptions(cfg, agentName),
	})
}

// handleChatModelDefault answers the model read for a conversation that has
// not been opened yet: the agent's published selection and the options it may
// be switched to, in the shape handleChatSessionModelGet answers. Nothing is
// written — the page holds the choice until its first message opens the
// session, and then writes it as that session's own.
func (s *Server) handleChatModelDefault(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	runner := s.runnerFor(r.Context(), "")
	if runner == nil {
		http.Error(w, "no runner is bound", http.StatusServiceUnavailable)
		return
	}
	cfg := s.liveCfg()
	if cfg == nil {
		http.Error(w, "no configuration is bound", http.StatusServiceUnavailable)
		return
	}
	agentName := "main"
	if name := strings.TrimSpace(runner.AgentName); name != "" {
		agentName = name
	}
	// The reading every fresh conversation follows (owner 2026-10-23): the
	// agent's most recently used model — the newest session row — with the
	// runner's published selection as the fallback when no row resolves (a
	// fresh install, or a newest row the config no longer offers).
	sel := run.NewSessionModel(runner, runnerSessionStore(runner))
	if !sel.Set {
		sel = run.PrimaryModelSelection(runner)
	}
	writeJSON(w, map[string]any{
		"provider": sel.Provider,
		"model":    sel.Model,
		"effort":   sel.Effort,
		"options":  s.sessionModelOptions(cfg, agentName),
	})
}

// runnerSessionStore is the runner's session store, nil when this server was
// assembled without one.
func runnerSessionStore(runner *run.Runner) *state.SessionStore {
	if runner == nil || runner.Deps == nil {
		return nil
	}
	return runner.Deps.SessionStore
}

// handleChatSessionModelPut records this conversation's own model choice,
// session-scoped: the runner's published default stays untouched and the
// session's next turn resolves the row into its run context, so a mid-run
// change lands on the next turn exactly as /model's does. A pair or an effort
// the agent's options do not offer is refused here, before anything is
// written. An empty effort is the explicit "model default" choice — the row
// pins no level and the provider applies its own.
func (s *Server) handleChatSessionModelPut(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	sid := strings.TrimSpace(ParamsFromContext(r.Context()).ByName("id"))
	if sid == "" {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Provider string  `json:"provider"`
		Model    string  `json:"model"`
		Effort   *string `json:"effort"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	runner := s.runnerFor(r.Context(), sid)
	if runner == nil {
		http.Error(w, "no runner is bound to this session", http.StatusServiceUnavailable)
		return
	}
	cfg := s.liveCfg()
	if cfg == nil {
		http.Error(w, "no configuration is bound", http.StatusServiceUnavailable)
		return
	}
	agentName := "main"
	if name := strings.TrimSpace(runner.AgentName); name != "" {
		agentName = name
	}
	provider := strings.TrimSpace(body.Provider)
	model := strings.TrimSpace(body.Model)
	var chosen turn.ModelChoice
	for _, option := range s.sessionModelOptions(cfg, agentName) {
		if strings.EqualFold(option.Provider, provider) && strings.EqualFold(option.Model, model) {
			chosen = turn.ModelChoice{Label: option.Label, Provider: option.Provider, Model: option.Model}
			break
		}
	}
	if chosen.Model == "" {
		http.Error(w, "model is not offered for this agent", http.StatusBadRequest)
		return
	}
	effort := ""
	if body.Effort != nil {
		effort = strings.TrimSpace(*body.Effort)
	}
	if effort != "" && !slices.Contains(s.sessionModelEffortsFor(cfg, agentName, chosen.Provider, chosen.Model), effort) {
		http.Error(w, "reasoning effort is not offered for this model", http.StatusBadRequest)
		return
	}
	if err := s.SelectModel(r.Context(), sid, chosen, &effort); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	sel := s.sessionModelSelection(runner, sid)
	writeJSON(w, map[string]any{
		"provider": sel.Provider,
		"model":    sel.Model,
		"effort":   sel.Effort,
	})
}

// sessionDisplayTitle is the title a web list shows. An unnamed session is
// stored titled with its own id; that is not a name, so it reads as none and
// the client draws its own placeholder, the way the store's SessionTitle does.
func sessionDisplayTitle(sum state.SessionSummary) string {
	return sessionTitleOrEmpty(sum.ID, sum.Title)
}

// sessionTitleOrEmpty is the rule sessionDisplayTitle applies, for the lists
// whose rows are not a SessionSummary.
func sessionTitleOrEmpty(id, title string) string {
	if title := strings.TrimSpace(title); title != strings.TrimSpace(id) {
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
	before := int64(0)
	if raw := strings.TrimSpace(r.URL.Query().Get("before")); raw != "" {
		if v, parseErr := strconv.ParseInt(raw, 10, 64); parseErr == nil && v > 0 {
			before = v
		}
	}
	// The paging contract (plan 173): no parameters is the whole
	// conversation — the subagent worker view and any external consumer rely
	// on it — while a limit or a before cursor asks for one page, 200 rows
	// by default and 1000 at most: the newest visible rows strictly below
	// the cursor. The cursor is a row id, so a session still being written
	// cannot shift what the next page returns. hasMore rides along with the
	// page but stays on the server: the response remains the plain array the
	// contract froze, and the client reads exhaustion off the page's own
	// length. A limit on the store-less Core fallback keeps its old meaning
	// below — the last N projected rows — because that store has no keyset.
	paged := s.Sessions != nil && (before > 0 || limit > 0)
	if paged {
		if limit <= 0 {
			limit = 200
		}
		if limit > 1000 {
			limit = 1000
		}
	} else if limit > 5000 {
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
	if paged {
		turns, _, err = s.Sessions.ListMessagesBefore(r.Context(), sid, before, limit)
	} else if s.Sessions != nil {
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
	var compactionEvents []event.RunEvent
	if s.RunRT != nil {
		events, cerr := s.sessionCompactionEvents(r.Context(), sid)
		if cerr != nil {
			http.Error(w, cerr.Error(), http.StatusInternalServerError)
			return
		}
		compactionEvents = events
	}
	// A goal's lines, at the positions the terminal's replay draws them.
	var goalEvents []event.RunEvent
	if s.RunRT != nil {
		events, gerr := s.sessionEventsOfTypes(r.Context(), sid, event.RunEventGoalStarted, event.RunEventGoalRoundStarted, event.RunEventGoalCompleted)
		if gerr != nil {
			http.Error(w, gerr.Error(), http.StatusInternalServerError)
			return
		}
		goalEvents = events
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
		// The run's first-token wait, on its "worked" row (plan 015). Absent
		// for a run that never measured it.
		FirstTokenMs int64              `json:"first_token_ms,omitempty"`
		PlanDone     int                `json:"plan_done,omitempty"`
		PlanTotal    int                `json:"plan_total,omitempty"`
		PlanActive   string             `json:"plan_active,omitempty"`
		Compaction   *chatCompactionRow `json:"compaction,omitempty"`
		Goal         *chatGoalRow       `json:"goal,omitempty"`
		PartsJSON    string             `json:"parts_json,omitempty"`
		ToolStepID   string             `json:"tool_step_id,omitempty"`
		ToolMetaJSON string             `json:"tool_meta_json,omitempty"`
		CreatedAt    int64              `json:"created_at,omitempty"`
		RunID        string             `json:"run_id,omitempty"`
		Origin       string             `json:"origin,omitempty"`
		Attachments  []chatAttachment   `json:"attachments,omitempty"`
		// Header is a tool row's card header, worded by the engine exactly as the
		// terminal words it (tool.DisplayHeader).
		Header string `json:"header,omitempty"`
		// HeaderParts is the same header in its four parts, for a surface that
		// lays the header out itself; CallHeaderList is, on an assistant row, the
		// header each call it issued would carry if it never ran, as a list —
		// the web camel-cases response keys deeply, which rewrites a call id
		// used as a map key, so new fields carry ids as list entries.
		HeaderParts    *tool.HeaderParts `json:"header_parts,omitempty"`
		CallHeaderList []callHeaderEntry `json:"call_header_list,omitempty"`
		// ShellCard is a shell row's facts (streams, exit code, truncation
		// counts, changed files), read back by the engine from the same body
		// the live card rendered; FileDiff is a file tool row's turn diff,
		// read the same way.
		ShellCard *tool.ShellCard `json:"shell_card,omitempty"`
		FileDiff  *tool.FileDiff  `json:"file_diff,omitempty"`
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
	if limit > 0 && !paged && len(visibleIndexes) > limit {
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
	// Display rows place by row id (plan 173): an index into turns only
	// means something on the page that computed it, while a row id names
	// the same row on every page. An event the positioner anchors before the
	// page's first visible row belongs to an older page — it is that page's
	// tail — and on a paged request an event anchored at index 0 hangs off a
	// row below the page entirely; the page is not widened by one row to go
	// find that anchor, so both stay unplaced here.
	compactionsBefore := map[int64][]chatCompactionRow{}
	compactionsTail := []chatCompactionRow{}
	goalsBefore := map[int64][]chatGoalRow{}
	goalsTail := []chatGoalRow{}
	// anchorRow resolves "drawn before index pos" to the visible row that
	// carries the event — the first visible row at or after pos, because an
	// anchor on a hidden row shows ahead of the next row a surface draws —
	// or to the page's tail when no visible row follows.
	anchorRow := func(pos int) (int, bool, bool) {
		if pos < firstVisible || (paged && pos == 0) {
			return 0, false, false
		}
		for _, i := range visibleIndexes {
			if i >= pos {
				return i, true, false
			}
		}
		return 0, false, true
	}
	for _, evt := range compactionEvents {
		pos, placed := turn.CompactionPosition(turns, evt)
		if !placed {
			continue
		}
		row, ok := chatCompactionRowFromEvent(evt)
		if !ok {
			continue
		}
		if i, onRow, atTail := anchorRow(pos); onRow {
			key := turns[i].RowID
			compactionsBefore[key] = append(compactionsBefore[key], row)
		} else if atTail {
			compactionsTail = append(compactionsTail, row)
		}
	}
	for _, evt := range goalEvents {
		pos, placed := turn.GoalPosition(turns, evt)
		if !placed {
			continue
		}
		row, ok := chatGoalRowFromEvent(evt)
		if !ok {
			continue
		}
		if i, onRow, atTail := anchorRow(pos); onRow {
			key := turns[i].RowID
			goalsBefore[key] = append(goalsBefore[key], row)
		} else if atTail {
			goalsTail = append(goalsTail, row)
		}
	}
	// Every run closes with its "Worked for" line after the last row it wrote
	// — finished, failed or stopped, whether or not it said anything — by the
	// rule the terminal's replay closes it with. The line is a row of its own,
	// carrying the run's clock and its checklist facts, read from that run's
	// plan events so replay and live agree by construction.
	workedLines := turn.RunWorkedLines(turns)
	// Paths in a card's header are shown relative to the conversation's own
	// directory — the project root — and absolute only outside it.
	// It is looked up once, and only if a row has a card to head.
	displayRoot := sync.OnceValue(func() string { return s.sessionDisplayRoot(r.Context(), sid) })
	for i, t := range turns {
		for _, row := range compactionsBefore[t.RowID] {
			out = append(out, msg{Role: "compaction", Compaction: &row})
		}
		for _, row := range goalsBefore[t.RowID] {
			out = append(out, msg{Role: "goal", Goal: &row})
		}
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
				RunStartedAt:  runStartedAt,
				RunFinishedAt: runFinishedAt,
				WorkedMs:      workedMs,
				PartsJSON:     t.PartsJSON,
				ToolStepID:    t.ToolStepID,
				ToolMetaJSON:  t.ToolMetaJSON,
				CreatedAt:     t.CreatedAt,
				RunID:         strings.TrimSpace(t.RunID),
			}
			// Attachments are what a user row attached. A tool row's image
			// file_reference is the pixels the model was sent, not something the
			// composer pinned, so it must not surface as an attachment.
			if strings.EqualFold(strings.TrimSpace(t.Role), "user") {
				for _, ref := range state.MessageAttachments(t.PartsJSON) {
					m.Attachments = append(m.Attachments, chatAttachmentOf(ref))
				}
			}
			switch strings.ToLower(strings.TrimSpace(t.Role)) {
			case "tool":
				header, parts, body, toolName := storedToolRowHeaderParts(displayRoot(), t.PartsJSON, t.ToolMetaJSON, time.Duration(t.ExecDurationMs)*time.Millisecond)
				m.Header = header
				if parts.Action != "" {
					m.HeaderParts = &parts
				}
				if card := shellCardFor(toolName, body); card != nil {
					m.ShellCard = card
				}
				if diff := fileDiffFor(toolName, body); diff != nil {
					m.FileDiff = diff
				}
			case "assistant":
				m.CallHeaderList = canceledCallHeaderList(displayRoot, t.PartsJSON)
			}
			// A tool row's Content is the model-facing result — for a shell,
			// the JSON map with its stdout — and the page draws that row from
			// parts_json and shell_card instead. Sending it here repeated the
			// same bytes a second and third time, so the history DTO carries the
			// display copy only.
			if !strings.EqualFold(strings.TrimSpace(t.Role), "tool") {
				m.Content = t.Content
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
		// On a paged request below the newest page (before > 0), a run whose
		// last in-page row is the page's newest row runs on past the page
		// boundary: its ending — and the Worked row that closes it — is on
		// the newer page above, the only page that holds the run's actual
		// last row. The newest page has nothing above it, so its top run
		// really does end there.
		if line, ok := workedLines[i]; ok && i >= firstVisible && !(paged && before > 0 && i == len(turns)-1) {
			worked := msg{
				ID:            "worked-" + line.RunID,
				Role:          "worked",
				RunID:         line.RunID,
				RunStartedAt:  line.StartedAt.UTC().Format(time.RFC3339Nano),
				RunFinishedAt: line.FinishedAt.UTC().Format(time.RFC3339Nano),
				WorkedMs:      line.Worked.Milliseconds(),
				FirstTokenMs:  line.FirstToken.Milliseconds(),
			}
			if s.RunRT != nil {
				progress := lastPlanProgressOfRun(r.Context(), s.RunRT, line.RunID)
				worked.PlanDone, worked.PlanTotal, worked.PlanActive = progress.Done, progress.Total, progress.Active
			}
			out = append(out, worked)
		}
	}
	for _, row := range compactionsTail {
		out = append(out, msg{Role: "compaction", Compaction: &row})
	}
	for _, row := range goalsTail {
		out = append(out, msg{Role: "goal", Goal: &row})
	}
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
	page.Events = s.withoutSupersededAbandonedErrors(r.Context(), page.Events)
	// A tool call's card reads with the terminal's header wording, paths
	// relative to the conversation's directory.
	root := sync.OnceValue(func() string { return s.sessionDisplayRoot(r.Context(), sid) })
	for i := range page.Events {
		if t := page.Events[i].Type; t == event.RunEventToolStarted || t == event.RunEventToolCompleted {
			page.Events[i].Payload = toolEventPayloadWithHeader(root(), t, page.Events[i].Payload)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(page)
}

// withoutSupersededAbandonedErrors drops the "process stopped" error of a run
// that also has a real turn_error. A run reaped as abandoned while its process
// was alive (a machine that slept past the lease) goes on to report how it
// really ended; the page keys a run's error block by run id and keeps the first
// one, so the reap's guess would hide the real error. The real one is looked up
// in the store, not the page, because the two can land on different pages.
func (s *Server) withoutSupersededAbandonedErrors(ctx context.Context, events []state.SessionEvent) []state.SessionEvent {
	out := events[:0:0]
	for _, evt := range events {
		if evt.Type == event.RunEventTurnError && strings.TrimSpace(evt.RunID) != "" && isAbandonedTurnError(evt.Payload) {
			if s.runHasRealTurnError(ctx, evt.RunID) {
				continue
			}
		}
		out = append(out, evt)
	}
	return out
}

func isAbandonedTurnError(payload json.RawMessage) bool {
	var p event.TurnErrorPayload
	return json.Unmarshal(payload, &p) == nil && p.Detail != nil && p.Detail.Code == "run_abandoned"
}

func (s *Server) runHasRealTurnError(ctx context.Context, runID string) bool {
	records, err := s.RunRT.ListRunEventsOfTypes(ctx, runID, event.RunEventTurnError)
	if err != nil {
		return false
	}
	for _, rec := range records {
		if !isAbandonedTurnError(rec.Payload) {
			return true
		}
	}
	return false
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
	// A conversation's plans are only its owner's to read: an unknown or
	// foreign id is 404, like every other per-session read here, never an
	// empty answer that looks like "this session planned nothing".
	if s.Sessions != nil {
		owned, err := s.Sessions.HasSession(r.Context(), sid)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !owned {
			http.NotFound(w, r)
			return
		}
	}
	// Project- and session-scoped, like every other plan resolution: plans
	// live per conversation, so plan-md returns the plan of exactly the
	// session in the URL — never another conversation's newer file.
	p, err := state.GetPlanForSession(s.stateRoot(), s.sessionProjectKey(r.Context(), sid), sid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"markdown": p})
}

// handleSessionPlans serves a conversation's plan history: every plan file it
// ever wrote, newest first, each with its text. plan-md names only the newest
// file, which leaves the previous versions of a replanned conversation
// unreachable from the web; the directory itself is that history.
func (s *Server) handleSessionPlans(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	sid := strings.TrimSpace(ParamsFromContext(r.Context()).ByName("id"))
	if sid == "" {
		http.NotFound(w, r)
		return
	}
	// Whole plans go out here, so the ownership guard is not optional: an
	// unknown or foreign id is 404, never another conversation's documents.
	if s.Sessions != nil {
		owned, err := s.Sessions.HasSession(r.Context(), sid)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !owned {
			http.NotFound(w, r)
			return
		}
	}
	root := s.stateRoot()
	plans, err := state.PlansForSession(root, s.sessionProjectKey(r.Context(), sid), sid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	type planListRow struct {
		Name string `json:"name"`
		// Path is relative to the state root — "plans/<projectKey>/<session>/…",
		// the same shape the workbench shows — so the user's home directory
		// never reaches the browser.
		Path      string `json:"path"`
		UpdatedAt int64  `json:"updated_at"`
		Markdown  string `json:"markdown"`
	}
	rows := make([]planListRow, 0, len(plans))
	for _, p := range plans {
		rel, _ := filepath.Rel(root, p.Path)
		rows = append(rows, planListRow{
			Name:      p.Name,
			Path:      filepath.ToSlash(rel),
			UpdatedAt: p.UpdatedAt.Unix(),
			Markdown:  p.Markdown,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"plans": rows})
}

// handleChatToolOutput serves a shell call's full spooled output — the whole
// text a truncated card's "open the output" drawer shows — for the
// conversation that ran it. Another conversation's output is not this
// reader's, and reads as not found.
func (s *Server) handleChatToolOutput(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	sid := strings.TrimSpace(ParamsFromContext(r.Context()).ByName("id"))
	if sid == "" {
		http.NotFound(w, r)
		return
	}
	if s.Sessions != nil {
		owned, err := s.Sessions.HasSession(r.Context(), sid)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !owned {
			http.NotFound(w, r)
			return
		}
	}
	s.serveToolOutput(w, r, s.runnerFor(r.Context(), sid), ParamsFromContext(r.Context()).ByName("retrieve"), sid)
}

// serveToolOutput answers one spooled output read: the retrieval id from the
// URL, the runner whose state root holds the spool, and the session the entry
// must belong to — a spool recorded by another session is never served here.
func (s *Server) serveToolOutput(w http.ResponseWriter, r *http.Request, runner *run.Runner, retrieve, sessionID string) {
	id, err := strconv.ParseInt(strings.TrimSpace(retrieve), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "retrieve id", http.StatusBadRequest)
		return
	}
	if runner == nil {
		http.NotFound(w, r)
		return
	}
	entry, err := tool.CompressorFor(runner.StateRoot()).Store().Get(id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if strings.TrimSpace(entry.SessionID) != strings.TrimSpace(sessionID) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"command":             entry.Command,
		"text":                entry.OriginalOutput,
		"spool_omitted_bytes": entry.SpoolOmittedBytes,
	})
}

// handleSubagentToolOutput is serveToolOutput from a subagent's own view: the
// spool belongs to the subagent's worker session, so that is the session the
// entry must name.
func (s *Server) handleSubagentToolOutput(w http.ResponseWriter, r *http.Request) {
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
	workerSessionID, _, _, _, _, err := run.SubagentContextGauge(r.Context(), runner, sid, agentKey)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.serveToolOutput(w, r, runner, ParamsFromContext(r.Context()).ByName("retrieve"), workerSessionID)
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
	subagentNotRunningCode   = "subagent_not_running"
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
// recall and interrupt engine calls answer false for a key that is not there,
// so their routes check tenancy here — the same lookup the engine's channel
// resolution does, another conversation's subagent being not found.
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
		OnReturned: func(agentKey string, returned []run.Input) {
			s.publishSubagentQueuedInputReleased(sid, agentKey, nil, returned)
		},
		// Every change to the subagent's queue reaches its open views: the
		// web reads the queue only from these events, so a message the model
		// took, or one the queue settled, would otherwise stay on screen as
		// queued and recallable.
		OnQueueChanged: func(agentKey string, preview run.QueuePreview) {
			s.publishSubagentPreview(context.Background(), sid, agentKey, toPendingInputPreview(preview))
		},
	}
}

// publishSubagentQueuedInputReleased hands back to every open page of the
// conversation the messages a subagent's execution ended without taking; the
// payload's Inputs carries them, and Next is always empty now that an execution
// cannot be followed by another one on the subagent's own initiative.
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
// shows it, read from the live channel.
func (s *Server) publishSubagentPendingInputUpdated(ctx context.Context, sid, agentKey string, runner *run.Runner) {
	if runner == nil {
		return
	}
	s.publishSubagentPreview(ctx, sid, agentKey, subagentInputPreview(runner, sid, agentKey))
}

// publishSubagentPreview reports a subagent's queue as its own view shows it,
// tagged with its roster key so it lands there and nowhere else.
func (s *Server) publishSubagentPreview(ctx context.Context, sid, agentKey string, preview turn.PendingInputPreview) {
	if s == nil || s.RunEvents() == nil {
		return
	}
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
	delivery, err := run.SendToSubagent(r.Context(), runner, s.subagentConversationSurface(sid), sid, agentKey, in)
	if err != nil {
		switch {
		case errors.Is(err, run.ErrSubagentNotFound):
			http.NotFound(w, r)
		case errors.Is(err, run.ErrSubagentNotRunning):
			// The subagent is idle: its view takes no messages, because the
			// agent that dispatched it decides what it does next.
			writeSubagentViewError(w, subagentNotRunningCode, "the subagent is not running", http.StatusConflict)
		default:
			http.Error(w, err.Error(), http.StatusBadRequest)
		}
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
	case "context":
		return useDedicatedEndpointCode, false
	}
	// The registry lists skill commands beside the built-ins, so a name it
	// finds may still be a skill: only the built-in table itself says
	// otherwise. A built-in that acts on the conversation (hidden, or global)
	// is not sent here; the page runs it in the main view instead.
	if turn.IsBuiltinName(name) {
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

// rewindStore assembles the shared rewind engine's dependencies for one
// request: the conversation store, the run store for the busy check, and the
// tool state whose snapshot writer saves a rewind's undo copies.
func (s *Server) rewindStore() *turn.RewindStore {
	if s == nil || s.Sessions == nil {
		return nil
	}
	var snap turn.SnapshotWriter
	if s.Env != nil && s.Env.Tools() != nil {
		snap = s.Env.Tools()
	}
	return &turn.RewindStore{Store: s.Sessions, Runs: s.RunRT, Snap: snap}
}

// rewindRoute resolves the session id and ownership every rewind route needs.
func (s *Server) rewindRoute(w http.ResponseWriter, r *http.Request) (string, bool) {
	sid := strings.TrimSpace(ParamsFromContext(r.Context()).ByName("id"))
	if sid == "" {
		http.NotFound(w, r)
		return "", false
	}
	owned, err := s.sessionOwned(r.Context(), sid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return "", false
	}
	if !owned {
		http.NotFound(w, r)
		return "", false
	}
	return sid, true
}

func (s *Server) handleSessionRewindPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	store := s.rewindStore()
	if store == nil {
		http.Error(w, "rewind unavailable", http.StatusServiceUnavailable)
		return
	}
	sid, ok := s.rewindRoute(w, r)
	if !ok {
		return
	}
	messageID, _ := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("message_id")), 10, 64)
	preview, err := turn.PreviewRewind(r.Context(), store, sid, messageID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, preview)
}

type rewindRequest struct {
	MessageID int64    `json:"message_id"`
	Mode      string   `json:"mode"`
	KeepPaths []string `json:"keep_paths"`
}

func (s *Server) handleSessionRewind(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	store := s.rewindStore()
	if store == nil {
		http.Error(w, "rewind unavailable", http.StatusServiceUnavailable)
		return
	}
	sid, ok := s.rewindRoute(w, r)
	if !ok {
		return
	}
	var req rewindRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	result, err := turn.Rewind(r.Context(), store, sid, req.MessageID, req.Mode, req.KeepPaths)
	if errors.Is(err, turn.ErrRewindBusy) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "session_busy"})
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, result)
}

func (s *Server) handleSessionRewindUndo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	store := s.rewindStore()
	if store == nil {
		http.Error(w, "rewind unavailable", http.StatusServiceUnavailable)
		return
	}
	sid, ok := s.rewindRoute(w, r)
	if !ok {
		return
	}
	if err := turn.UndoRewind(r.Context(), store, sid); err != nil {
		if errors.Is(err, turn.ErrNoPendingRewind) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"status": "ok"})
}

func (s *Server) handleSessionFileChanges(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	store := s.rewindStore()
	if store == nil {
		http.Error(w, "file changes unavailable", http.StatusServiceUnavailable)
		return
	}
	sid, ok := s.rewindRoute(w, r)
	if !ok {
		return
	}
	files, err := turn.SessionFileChanges(r.Context(), store, sid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"files": files})
}

func (s *Server) handleSessionFileChangesRevert(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	store := s.rewindStore()
	if store == nil {
		http.Error(w, "file changes unavailable", http.StatusServiceUnavailable)
		return
	}
	sid, ok := s.rewindRoute(w, r)
	if !ok {
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	if err := turn.RevertFile(r.Context(), store, sid, req.Path); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"status": "ok"})
}

func (s *Server) handleSessionFileChangesDiff(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	store := s.rewindStore()
	if store == nil {
		http.Error(w, "file changes unavailable", http.StatusServiceUnavailable)
		return
	}
	sid, ok := s.rewindRoute(w, r)
	if !ok {
		return
	}
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	diff, err := turn.FileChangeDiff(r.Context(), store, sid, path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"path": path, "diff": diff})
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
	// tool calls made. The output rate comes from the same paired query and
	// the same algorithm /status uses, so the two surfaces cannot disagree.
	totals := turn.SessionUsageTotalsOf(usage)
	var rate float64
	var generationMs int64
	if tokens, ms, rerr := s.RunRT.SessionOutputRate(r.Context(), sid); rerr == nil {
		generationMs = ms
		rate = turn.OutputTokensPerSecond(tokens, ms)
	}
	// The latest turn's attempt ledger (plan 165 prototype): every physical
	// model request its run made, failures and the settling attempt alike, so
	// "how many times did this answer try" outlives the stream that showed
	// it. The tree includes the subagents' runs; each row's run_id names
	// whose request it was.
	attempts := []state.LLMAttempt{}
	if rows, aerr := s.RunRT.LatestPrimaryRunTreeAttempts(r.Context(), sid, 200); aerr != nil {
		http.Error(w, aerr.Error(), http.StatusInternalServerError)
		return
	} else if len(rows) > 0 {
		attempts = rows
	}
	writeJSON(w, map[string]any{
		"session_id":               sid,
		"input_tokens":             totals.InputTokens,
		"output_tokens":            totals.OutputTokens,
		"cache_read":               totals.CacheRead,
		"cache_written":            totals.CacheWritten,
		"uncached":                 totals.Uncached,
		"requests":                 totals.Requests,
		"cache_hit_percent":        totals.CacheHitPercent,
		"output_tokens_per_second": rate,
		"generation_ms":            generationMs,
		"tool_calls":               total,
		"by_tool":                  byTool,
		"attempts":                 attempts,
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
			Provider: option.Provider, Model: option.Model, Current: option.Current,
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
			AgentID: active.AgentID,
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
	for _, option := range s.approvalGate().ReviewModels(sid) {
		if option.MatchesSelection(model.Provider, model.Model) {
			allowed = true
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
		process.AgentContextForProject(context.Background(), s.stateRoot(), sid, s.sessionProjectKey(context.Background(), sid)), sid, runID,
	)
	// A session without a transcript store reviews without a task context
	// rather than handing the flow a typed-nil store.
	var transcripts turn.PlanReviewTranscripts
	if sessions := s.Sessions; sessions != nil {
		transcripts = sessions
	}
	review := turn.PlanReviewRun{
		ActionID: id, Model: model, SessionID: sid, RunID: runID,
		StateRoot: s.stateRoot(), ProjectKey: s.sessionProjectKey(context.Background(), sid),
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
			s.resumeGatewayRunIfParked(ctx, actionID, turn.ActionClearedContext(result.Action))
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
	s.resumeGatewayRunIfParked(ctx, actionID, turn.ActionClearedContext(result.Action))
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

// dispatchGatewayResume starts the continuation of an answered action. It is a
// variable only so a test can observe the dispatch without running the run.
var dispatchGatewayResume = func(s *Server, actionID string, clearedContext bool) {
	go s.resumeGatewayRun(actionID, clearedContext)
}

// resumeGatewayRunIfParked dispatches the continuation for an answered action,
// but only while a wait row still parks a run on it. A run with no wait row has
// nothing to continue: resumeGatewayRun looks one up, finds none and returns. A
// goroutine dispatched anyway would outlive the response that started it and
// reach the stores after the surface may already be shutting them down, so the
// decision to dispatch is taken here, while the request is still open, and the
// resume keeps its own check for the row being retired in between.
func (s *Server) resumeGatewayRunIfParked(ctx context.Context, actionID string, clearedContext bool) {
	if s == nil || s.RunRT == nil {
		return
	}
	// The answer is already stored by the time this runs; whether to continue the
	// run must not depend on the client still being connected. WithoutCancel keeps
	// the request's values and drops its cancellation — the lookup is one short read.
	_, w, err := s.RunRT.FindRunByAction(context.WithoutCancel(ctx), actionID)
	if err != nil || w == nil {
		return
	}
	dispatchGatewayResume(s, actionID, clearedContext)
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
	ctx2 = process.AgentContextForProject(ctx2, s.stateRoot(), agentSessionID, s.sessionProjectKey(ctx2, agentSessionID))
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
	// No answer on the completion record: the run's words are in the transcript,
	// written as each block ends, and this durable event only says the run ended.
	_ = s.publishGatewayRunEvent(ctx, sid, runID, event.RunEventTurnCompleted, event.TurnCompletedPayload{
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
	installed := s.decorateSkillList(r.Context(), launch, projectID, svc, overview.Installed)
	q := readListQuery(r)
	if !q.Paged {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"installed": installed,
			"actions":   overview.Actions,
		})
		return
	}
	// origin is the layer filter: one layer, or several as a comma-separated
	// list (the shared library page asks for "shared,builtin"). A row's layer
	// is what the listing already classified it as, never a second rule.
	origins := map[string]bool{}
	for _, part := range strings.Split(r.URL.Query().Get("origin"), ",") {
		if layer := strings.ToLower(strings.TrimSpace(part)); layer != "" {
			origins[layer] = true
		}
	}
	matched := make([]skillListEntry, 0, len(installed))
	for _, row := range installed {
		if len(origins) > 0 && !origins[string(row.Origin)] {
			continue
		}
		if q.Q != "" && !strings.Contains(strings.ToLower(row.Name+" "+row.Description), strings.ToLower(q.Q)) {
			continue
		}
		matched = append(matched, row)
	}
	page, servedPage := pageOf(matched, q)
	writeListPage(w, "installed", page, len(matched), servedPage, q, map[string]any{"actions": overview.Actions})
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
		// A toggle on a never-published skill is the caller's mistake, not
		// the server's: the engine's refusal reaches the page as a 400.
		writeSkillDraftNotPublished(w, err, http.StatusInternalServerError)
		return
	}
	updated, err := s.decoratedSkillList(r.Context(), launch, projectID, svc)
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

// withConfigWrite runs one read-modify-write of forebrain.yaml with every
// other such write in this process held off. fn must read the file (via
// persistedConfig) inside the call, not before it, and must not call
// withConfigWrite itself.
func (s *Server) withConfigWrite(fn func() error) error {
	s.configWriteMu.Lock()
	defer s.configWriteMu.Unlock()
	return fn()
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

// handleChannels reads and writes one project's channels. Channels are project
// data: a channel binds an external account to one project's conversations, so
// the write lands in that project's rows and nowhere else. GET returns each
// kind's stored config with its secret fields masked; PUT writes the kinds the
// body names, leaving a masked secret untouched.
func (s *Server) handleChannels(w http.ResponseWriter, r *http.Request) {
	project, agentID, ok := s.projectScope(w, r)
	if !ok {
		return
	}
	store := s.projectChannelStore(agentID)
	if store == nil {
		http.Error(w, "channels unavailable", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		rows, err := store.List(r.Context(), project.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		stored := map[string]state.ProjectChannel{}
		for _, row := range rows {
			stored[row.Kind] = row
		}
		// Every kind the engine can build is offered, so a page can configure a
		// channel a project has nothing stored for; a kind with no row is
		// rendered from its own defaults.
		q := readListQuery(r)
		out := map[string]any{}
		table := make([]projectChannelRow, 0, len(channel.ChannelKinds))
		for _, kind := range channel.ChannelKinds {
			row, saved := stored[kind]
			fields, configured, ok := projectChannelFields(kind, row, saved)
			if !ok {
				// A saved configuration that does not parse is left out of both
				// answers: there is nothing trustworthy to show or to edit.
				continue
			}
			if !q.Paged {
				redactChannelFields(fields)
				out[kind] = fields
				continue
			}
			// The hints are read from the stored values, so they are taken
			// before the fields are masked.
			secretKeys, hints := projectChannelSecrets(s.Home, fields)
			redactChannelFields(fields)
			enabled, _ := fields["enabled"].(bool)
			labelZh, labelEn := channelLabel(kind)
			table = append(table, projectChannelRow{
				Kind:        kind,
				LabelZh:     labelZh,
				LabelEn:     labelEn,
				Enabled:     enabled,
				Configured:  configured,
				Fields:      fields,
				SecretKeys:  secretKeys,
				SecretHints: hints,
			})
		}
		if !q.Paged {
			writeAgentsJSON(w, map[string]any{"project_id": project.ID, "kinds": channel.ChannelKinds, "channels": out})
			return
		}
		// The search box reads the kind and either of its names, which is what
		// the table shows.
		needle := strings.ToLower(q.Q)
		matched := make([]projectChannelRow, 0, len(table))
		for _, row := range table {
			if q.Q != "" &&
				!strings.Contains(strings.ToLower(row.Kind), needle) &&
				!strings.Contains(strings.ToLower(row.LabelZh), needle) &&
				!strings.Contains(strings.ToLower(row.LabelEn), needle) {
				continue
			}
			matched = append(matched, row)
		}
		page, servedPage := pageOf(matched, q)
		writeListPage(w, "rows", page, len(matched), servedPage, q, nil)
	case http.MethodPut:
		var section map[string]map[string]any
		if err := json.NewDecoder(r.Body).Decode(&section); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := s.saveProjectChannels(r.Context(), store, project.ID, section); err != nil {
			var taken state.ErrChannelAccountTaken
			if errors.As(err, &taken) {
				http.Error(w, taken.Error(), http.StatusConflict)
				return
			}
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// The channels just changed, so the running set is rebound.
		if active, aerr := s.activePrimarySummary(); aerr == nil {
			s.bindChannelsTo(r.Context(), active)
		}
		writeAgentsJSON(w, map[string]any{"project_id": project.ID, "applied": true})
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}

// projectChannelRow is one row of a project's channel table: the kind named the
// way the page names it, whether a configuration is saved for it, and the fields
// the editor fills in.
type projectChannelRow struct {
	Kind    string `json:"kind"`
	LabelZh string `json:"label_zh"`
	LabelEn string `json:"label_en"`
	Enabled bool   `json:"enabled"`
	// Configured says a configuration is really saved — a stored row whose JSON
	// is blank or `{}` holds nothing the person filled in, so its switches and
	// editors are not offered as though it did.
	Configured  bool              `json:"configured"`
	Fields      map[string]any    `json:"fields"`
	SecretKeys  []string          `json:"secret_keys"`
	SecretHints map[string]string `json:"secret_hints"`
}

// projectChannelFields renders one kind's field map the way a page reads it: the
// saved configuration when there is one, the kind's own defaults otherwise, with
// "enabled" beside them. ok is false for a saved configuration that does not
// parse; the kind is then left out of the answer entirely.
func projectChannelFields(kind string, row state.ProjectChannel, saved bool) (fields map[string]any, configured bool, ok bool) {
	fields = map[string]any{}
	if !saved {
		// Every field the kind declares, not only the ones that carry a
		// default: a form for a channel nobody configured yet still has to
		// offer its inputs.
		for key, value := range config.ChannelKindFields(kind) {
			fields[key] = value
		}
		fields["enabled"] = false
		return fields, false, true
	}
	if raw := strings.TrimSpace(row.ConfigJSON); raw != "" && raw != "{}" {
		if err := json.Unmarshal([]byte(raw), &fields); err != nil {
			return nil, false, false
		}
		configured = true
	}
	fields["enabled"] = row.Enabled
	return fields, configured, true
}

// projectChannelSecrets names the secret fields of one kind's configuration and
// the hint a page shows for each one that holds a value. It reads the stored
// values, so it runs before the fields are masked.
func projectChannelSecrets(home string, fields map[string]any) ([]string, map[string]string) {
	keys := make([]string, 0, 2)
	hints := map[string]string{}
	for key, value := range fields {
		if !config.IsSecretLikeConfigKey(key) {
			continue
		}
		keys = append(keys, key)
		text, _ := value.(string)
		if strings.TrimSpace(text) == "" {
			continue
		}
		hints[key] = channelSecretHint(home, text)
	}
	sort.Strings(keys)
	return keys, hints
}

// channelSecretHint is the hint a page shows for one stored secret: `••••` and
// the value's last four characters, or the mask alone when the value cannot be
// read back (a short one, or a ${VAR} reference with nothing behind it). It is
// the model-service page's hint with its mask, so the two read alike.
func channelSecretHint(home, value string) string {
	hint := providerKeyHint(home, value)
	if hint == providerSecretMask {
		return providerSecretMask
	}
	return providerSecretMask + hint
}

// saveProjectChannels writes the kinds a page submitted and clears the ones it
// dropped, so the stored set matches what the page showed. A kind whose secret
// came back masked keeps the stored value under it.
func (s *Server) saveProjectChannels(ctx context.Context, store *state.ProjectChannelStore, projectID string, section map[string]map[string]any) error {
	existing, err := store.List(ctx, projectID)
	if err != nil {
		return err
	}
	kept := map[string]bool{}
	for kind, fields := range section {
		kind = strings.TrimSpace(kind)
		if kind == "" {
			continue
		}
		kept[kind] = true
		prior, hasPrior := channelRowFor(existing, kind)
		// A masked secret that came back untouched keeps the stored value: the
		// page never saw it, so it must not overwrite it with the mask.
		mergeRedactedChannelFields(fields, prior.ConfigJSON, hasPrior)
		enabled, _ := fields["enabled"].(bool)
		delete(fields, "enabled")
		// Every secret the page typed is moved to ~/.forebrain/.env and left in
		// the stored config only as a ${VAR} reference, so the state database
		// carries no plaintext credential.
		s.storeChannelSecretRefs(projectID, kind, fields)
		raw, merr := json.Marshal(fields)
		if merr != nil {
			return merr
		}
		accountKey := channelAccountKey(kind, raw)
		if err := store.Upsert(ctx, state.ProjectChannel{
			ProjectID:  projectID,
			Kind:       kind,
			Enabled:    enabled,
			ConfigJSON: string(raw),
			AccountKey: accountKey,
		}); err != nil {
			return err
		}
	}
	for _, row := range existing {
		if kept[row.Kind] {
			continue
		}
		if err := store.Delete(ctx, projectID, row.Kind); err != nil {
			return err
		}
	}
	return nil
}

func channelRowFor(rows []state.ProjectChannel, kind string) (state.ProjectChannel, bool) {
	for _, row := range rows {
		if row.Kind == kind {
			return row, true
		}
	}
	return state.ProjectChannel{}, false
}

// redactChannelFields masks every secret field a page reads back. An empty
// field stays empty: "unset" and "set but hidden" are different facts.
func redactChannelFields(fields map[string]any) {
	for k, v := range fields {
		if !config.IsSecretLikeConfigKey(k) {
			continue
		}
		if s, ok := v.(string); ok && s != "" {
			fields[k] = config.RedactedSecretPlaceholder
		}
	}
}

// mergeRedactedChannelFields restores the stored value under a secret field the
// page sent back untouched (its masked placeholder), so a round-tripped form
// keeps the secret already on disk. A masked field with nothing stored before
// is dropped rather than saved as the mask.
func mergeRedactedChannelFields(fields map[string]any, priorJSON string, hasPrior bool) {
	var prior map[string]any
	if hasPrior {
		_ = json.Unmarshal([]byte(strings.TrimSpace(priorJSON)), &prior)
	}
	for k, v := range fields {
		if !config.IsSecretLikeConfigKey(k) {
			continue
		}
		s, ok := v.(string)
		if !ok || s != config.RedactedSecretPlaceholder {
			continue
		}
		if pv, ok := prior[k].(string); ok && strings.TrimSpace(pv) != "" {
			fields[k] = pv
			continue
		}
		delete(fields, k)
	}
}

// storeChannelSecretRefs moves every plaintext secret a page typed into the
// home's .env and replaces it in place with the ${VAR} reference the state
// database stores. A field that already holds a reference is left as it is.
func (s *Server) storeChannelSecretRefs(projectID, kind string, fields map[string]any) {
	for k, v := range fields {
		if !config.IsSecretLikeConfigKey(k) {
			continue
		}
		str, ok := v.(string)
		if !ok || strings.TrimSpace(str) == "" {
			continue
		}
		ref, err := process.SecretConfigReferenceForOnboard(s.Home, process.ChannelSecretEnvNameForProject(projectID, kind, k), str)
		if err != nil {
			continue
		}
		fields[k] = ref
	}
}

// channelAccountKey computes the account identity a project channel's stored
// config speaks as, resolving its ${VAR} secrets the way a bind would, so one
// bot account cannot silently belong to two projects.
func channelAccountKey(kind string, rawJSON []byte) string {
	section, err := config.ParseChannelsSectionJSON(`{"` + kind + `":` + string(rawJSON) + `}`)
	if err != nil {
		return ""
	}
	return channel.AccountKey(kind, section)
}

// projectScope resolves the project a route's :id names, on behalf of the
// active primary agent, answering the request itself when either is
// unavailable.
func (s *Server) projectScope(w http.ResponseWriter, r *http.Request) (state.Project, string, bool) {
	active, err := s.activePrimarySummary()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return state.Project{}, "", false
	}
	if s.Env == nil || s.Env.SQL == nil {
		http.Error(w, "projects unavailable", http.StatusServiceUnavailable)
		return state.Project{}, "", false
	}
	project, err := state.NewProjectStore(s.Env.SQL, active.ID).Get(r.Context(), strings.TrimSpace(ParamsFromContext(r.Context()).ByName("id")))
	if err != nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return state.Project{}, "", false
	}
	return project, active.ID, true
}

// handleProviders reads and writes the active agent's model providers. The
// list's order is pure storage — user data no operation reorders; the default
// model is stated by agents.definitions.<agent>.default_model (absent = the
// first entry) and travels as the agent-level "default_model" field so the
// page never has to recompute it.
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

// providerListRow is one row of the model-services table: the stored row plus
// its position in the whole stored list. The table pages, and the position is
// a stable row id across pages — the list's order is storage, not priority.
type providerListRow struct {
	providerRowDTO
	Index int `json:"index"`
}

// effectiveDefaultModelID returns the "provider/model" an empty model
// selection for this agent resolves to: default_model when set, otherwise the
// first llm_providers entry. Both GET branches send it so the page's default
// badge matches the engine's resolution exactly.
func effectiveDefaultModelID(def config.AgentDefinition) string {
	if entry := config.EffectiveDefaultLLM(def); entry != nil {
		return strings.TrimSpace(entry.Provider) + "/" + strings.TrimSpace(entry.Model)
	}
	return ""
}

// effectiveNonPlaceholderDefault is the effective default with normalize's
// empty slot (the bare entry main carries before first setup, dropped on
// save) not counting as a default row: deleting it is not deleting a model.
func effectiveNonPlaceholderDefault(def config.AgentDefinition) *config.AgentLLMProviderConfig {
	entry := config.EffectiveDefaultLLM(def)
	if entry == nil ||
		(strings.TrimSpace(entry.Provider) == "" && strings.TrimSpace(entry.Model) == "") {
		return nil
	}
	return entry
}

// providerRowMatches is the model-services search box: it reads the service
// name, the models it serves and the endpoint they are reached at.
func providerRowMatches(row providerRowDTO, q string) bool {
	needle := strings.ToLower(q)
	for _, value := range append([]string{row.Provider, row.BaseURL}, row.Models...) {
		if strings.Contains(strings.ToLower(value), needle) {
			return true
		}
	}
	return false
}

// providerPutRow is one row of the PUT. params decodes through the engine's
// own type, so anything but a JSON object is refused here rather than
// written into a config the loader would then reject.
type providerPutRow struct {
	Provider string                  `json:"provider"`
	BaseURL  string                  `json:"base_url,omitempty"`
	APIPath  string                  `json:"api_path,omitempty"`
	Params   config.LLMRequestParams `json:"params,omitempty"`
	Model    string                  `json:"model,omitempty"`
	Models   []string                `json:"models,omitempty"`
	// APIKey is the client *stating* the row's key, which is why the wire
	// separates present from absent: absent means the user did not touch the
	// field (the page never receives the stored value, so it must not send a
	// guess back) and the stored key is kept; present and empty is the user
	// having emptied the field, and that is the delete semantics — the row is
	// written without any api_key. A ${ENV} reference is stored as written.
	APIKey      *string `json:"api_key,omitempty"`
	APIKeyPlain string  `json:"api_key_plain,omitempty"`
	// Replaces is set only on a row the page edited. See providerReplaces.
	Replaces *providerReplaces `json:"replaces,omitempty"`
}

// providerReplaces is the row an edit started from, exactly as the page loaded
// it. It lets the merge replace that row's stored entries in place; without it
// an edit looks like "these entries vanished, these new ones appeared" and the
// merge would delete the old ones and append the new ones at the end.
type providerReplaces struct {
	Provider string   `json:"provider"`
	BaseURL  string   `json:"base_url,omitempty"`
	APIPath  string   `json:"api_path,omitempty"`
	Models   []string `json:"models,omitempty"`
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

// providerEntryIdentity is the merge identity of one expanded llm_providers
// entry: provider+model at one endpoint. provider and model compare
// case-insensitively (the engine resolves them that way); base_url and
// api_path compare exactly — the same provider+model at a different endpoint
// is a legal separate entry, so provider+model alone must not be the identity.
func providerEntryIdentity(item config.AgentLLMProviderConfig) string {
	return strings.Join([]string{
		strings.ToLower(strings.TrimSpace(item.Provider)),
		strings.TrimSpace(item.BaseURL),
		strings.TrimSpace(item.APIPath),
		strings.ToLower(strings.TrimSpace(item.Model)),
	}, "\x00")
}

// storedKeyForRow is the key the stored list already carries for this request
// row's models, matched by providerEntryIdentity — provider+model at one
// endpoint. A second entry of the same provider at another endpoint therefore
// never inherits the first one's key: the key the user typed for b.example is
// the only one that may be sent there. Empty means "no stored entry for these
// models at this endpoint", and the caller's own api_key validation refuses the
// write rather than guessing; two stored keys that disagree for one row are the
// same "do not guess".
func storedKeyForRow(stored []config.AgentLLMProviderConfig, row providerPutRow, models []string) string {
	byIdentity := make(map[string]string, len(stored))
	for _, item := range stored {
		if key := strings.TrimSpace(item.APIKey); key != "" {
			byIdentity[providerEntryIdentity(item)] = key
		}
	}
	key := ""
	for _, model := range models {
		want := providerEntryIdentity(config.AgentLLMProviderConfig{
			Provider: strings.TrimSpace(row.Provider),
			Model:    strings.TrimSpace(model),
			BaseURL:  strings.TrimSpace(row.BaseURL),
			APIPath:  strings.TrimSpace(row.APIPath),
		})
		got := byIdentity[want]
		if got == "" {
			continue
		}
		if key != "" && key != got {
			return ""
		}
		key = got
	}
	return key
}

// providerListHasEntry reports whether the expanded list carries the
// provider+model pair, matching the way the engine resolves a default.
func providerListHasEntry(entries []config.AgentLLMProviderConfig, provider, model string) bool {
	provider = strings.ToLower(strings.TrimSpace(provider))
	model = strings.ToLower(strings.TrimSpace(model))
	for _, item := range entries {
		if strings.ToLower(strings.TrimSpace(item.Provider)) == provider &&
			strings.ToLower(strings.TrimSpace(item.Model)) == model {
			return true
		}
	}
	return false
}

// indexOfProviderModel is providerListHasEntry's position: the first entry for
// the provider+model pair (the one EffectiveDefaultLLM resolves), or -1.
func indexOfProviderModel(entries []config.AgentLLMProviderConfig, provider, model string) int {
	provider = strings.ToLower(strings.TrimSpace(provider))
	model = strings.ToLower(strings.TrimSpace(model))
	for i, item := range entries {
		if strings.ToLower(strings.TrimSpace(item.Provider)) == provider &&
			strings.ToLower(strings.TrimSpace(item.Model)) == model {
			return i
		}
	}
	return -1
}

// providerMergeRow is one request row after expansion: its entries (one per
// model, in the row's order) and, for an edited row, where it started.
type providerMergeRow struct {
	Entries  []config.AgentLLMProviderConfig
	Replaces *providerReplaces
}

// providerMergeResult is the merged list plus what every replaced stored slot
// became. A nil value means the edit removed that slot's model.
type providerMergeResult struct {
	Merged   []config.AgentLLMProviderConfig
	Replaced map[int]*config.AgentLLMProviderConfig
}

// mergeProviderEntries merges the request into the stored (expanded) list
// while keeping the stored order, which is user data:
//
//  1. An edited row (Replaces set) claims the stored slots its old entries
//     occupy. Its i-th new entry takes its i-th slot; extra entries go right
//     after its last slot (so the row stays adjacent and folds into one row on
//     read); slots it no longer fills are deleted.
//  2. Every other stored entry is kept when an unused request entry has the
//     same identity (updated in place), and dropped otherwise.
//  3. Request entries nobody used are new rows, appended in request order.
//
// A Replaces that finds none of its entries in the stored list (the page was
// stale) claims nothing and the row falls back to identity matching.
func mergeProviderEntries(stored []config.AgentLLMProviderConfig, rows []providerMergeRow) providerMergeResult {
	owner := make([]int, len(stored))
	for i := range owner {
		owner[i] = -1
	}
	slots := make([][]int, len(rows))
	for r, row := range rows {
		if row.Replaces == nil {
			continue
		}
		models := row.Replaces.Models
		if len(models) == 0 {
			models = []string{""}
		}
		for _, model := range models {
			want := providerEntryIdentity(config.AgentLLMProviderConfig{
				Provider: row.Replaces.Provider,
				BaseURL:  row.Replaces.BaseURL,
				APIPath:  row.Replaces.APIPath,
				Model:    model,
			})
			for p := range stored {
				if owner[p] == -1 && providerEntryIdentity(stored[p]) == want {
					owner[p] = r
					slots[r] = append(slots[r], p)
					break
				}
			}
		}
		sort.Ints(slots[r])
	}

	used := make([][]bool, len(rows))
	for r := range rows {
		used[r] = make([]bool, len(rows[r].Entries))
	}
	replaced := map[int]*config.AgentLLMProviderConfig{}
	after := map[int][]config.AgentLLMProviderConfig{}
	for r, row := range rows {
		n := len(slots[r])
		for i, p := range slots[r] {
			if i < len(row.Entries) {
				entry := row.Entries[i]
				replaced[p] = &entry
				used[r][i] = true
			} else {
				replaced[p] = nil
			}
		}
		if n > 0 && len(row.Entries) > n {
			last := slots[r][n-1]
			for i := n; i < len(row.Entries); i++ {
				after[last] = append(after[last], row.Entries[i])
				used[r][i] = true
			}
		}
	}

	merged := make([]config.AgentLLMProviderConfig, 0, len(stored))
	for p, item := range stored {
		if owner[p] >= 0 {
			if entry := replaced[p]; entry != nil {
				merged = append(merged, *entry)
			}
			merged = append(merged, after[p]...)
			continue
		}
		found := false
		for r := range rows {
			for i, entry := range rows[r].Entries {
				if !used[r][i] && providerEntryIdentity(entry) == providerEntryIdentity(item) {
					used[r][i] = true
					merged = append(merged, entry)
					found = true
					break
				}
			}
			if found {
				break
			}
		}
	}
	for r := range rows {
		for i, entry := range rows[r].Entries {
			if !used[r][i] {
				merged = append(merged, entry)
			}
		}
	}
	return providerMergeResult{Merged: merged, Replaced: replaced}
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
		// deepseek) is stored as three rows on purpose, not one service.
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

// providerSecretMask is what a page shows instead of a secret it may not read.
const providerSecretMask = "\u2022\u2022\u2022\u2022"

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
			return providerSecretMask
		}
	}
	if key == "" || len(key) <= 4 {
		return providerSecretMask
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
		providers := providerRowsFor(strings.TrimSpace(s.Home), def.LLMProviders)
		defaultModel := effectiveDefaultModelID(def)
		q := readListQuery(r)
		if !q.Paged {
			writeAgentsJSON(w, map[string]any{
				"agent_id":      active.ID,
				"default_model": defaultModel,
				"providers":     providers,
			})
			return
		}
		matched := make([]providerListRow, 0, len(providers))
		for index, row := range providers {
			if q.Q != "" && !providerRowMatches(row, q.Q) {
				continue
			}
			matched = append(matched, providerListRow{providerRowDTO: row, Index: index})
		}
		// The order is pure storage order and never changes here: the table
		// pages through it as it stands, and every row carries its position
		// in the whole list as a stable row id.
		page, servedPage := pageOf(matched, q)
		writeListPage(w, "providers", page, len(matched), servedPage, q, map[string]any{
			"agent_id":      active.ID,
			"default_model": defaultModel,
		})
	case http.MethodPut:
		var req struct {
			Providers    []providerPutRow `json:"providers"`
			DefaultModel *string          `json:"default_model"`
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
		// The expanded list as it stands on disk: the base the merge below works
		// on, and the source of the keys a request row without one carries
		// forward (storedKeyForRow).
		stored := config.ResolvedLLMConfigs(def)
		entries := make([]config.AgentLLMProviderConfig, 0, len(req.Providers))
		mergeRows := make([]providerMergeRow, 0, len(req.Providers))
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
			apiKey := ""
			cleared := false
			if plain := strings.TrimSpace(row.APIKeyPlain); plain != "" {
				// A plaintext key never enters the config: it is stored in the
				// home's .env and the yaml keeps only the ${ENV} reference.
				apiKey, err = process.ProviderAPIKeyConfigReference(strings.TrimSpace(s.Home), strings.TrimSpace(row.Provider), plain)
				if err != nil {
					http.Error(w, "could not store the api key: "+err.Error(), http.StatusInternalServerError)
					return
				}
			} else if row.APIKey != nil {
				// Present on the wire means the client states the value: ""
				// is the user having emptied the field, and that removes the
				// key. The marker tells config.Save this emptiness is a
				// decision — the row on disk still carries the ${ENV}
				// reference, and Save would otherwise restore it.
				apiKey = strings.TrimSpace(*row.APIKey)
				cleared = apiKey == ""
			} else {
				// No key on the wire means the user did not touch it: carry the
				// stored key of this entry — the same provider+model at this
				// endpoint — never another endpoint's.
				apiKey = storedKeyForRow(stored, row, models)
			}
			start := len(entries)
			for _, model := range models {
				entries = append(entries, config.AgentLLMProviderConfig{
					Provider:      strings.TrimSpace(row.Provider),
					Model:         strings.TrimSpace(model),
					APIKey:        apiKey,
					BaseURL:       strings.TrimSpace(row.BaseURL),
					APIPath:       strings.TrimSpace(row.APIPath),
					Params:        row.Params,
					APIKeyCleared: cleared,
				})
			}
			// Remember which entries this row expanded into: an edited row
			// claims the stored slots its own entries occupy.
			mergeRows = append(mergeRows, providerMergeRow{Entries: entries[start:], Replaces: row.Replaces})
		}
		// Order-preserving merge. The list's order is user data, so the disk
		// order is the base: a request row that maps onto a stored entry by
		// identity updates that entry in place; a row the request no longer
		// names is deleted; a genuinely new row is appended at the end in
		// request order. A row that carries the shape it was edited from
		// (replaces) claims those stored slots instead, so an edit of a row
		// stays an edit of that row. Without this, a page opened before some
		// other surface appended a row would write its stale snapshot back
		// and both rewind the order and swallow the new row.
		prevDefault := effectiveNonPlaceholderDefault(def)
		prevSlot := -1
		if prevDefault != nil {
			prevSlot = indexOfProviderModel(stored, prevDefault.Provider, prevDefault.Model)
		}
		result := mergeProviderEntries(stored, mergeRows)
		merged := result.Merged
		def.LLMProviders = merged
		// The default model must survive the write and stay resolvable. An
		// explicit default_model is validated against the merged list; an
		// untouched one means the row it resolved to before the write (the
		// default_model row, or the first row when unset) must still be
		// there after the merge.
		if req.DefaultModel != nil {
			if strings.TrimSpace(*req.DefaultModel) == "" {
				http.Error(w, "default_model 不能清空：请将其他模型服务设为默认", http.StatusBadRequest)
				return
			}
			def.DefaultModel = strings.TrimSpace(*req.DefaultModel)
			if config.EffectiveDefaultLLM(def) == nil {
				http.Error(w, "default_model "+def.DefaultModel+" 不在模型服务列表中，请先对该列表中的模型设为默认", http.StatusBadRequest)
				return
			}
		} else if prevDefault != nil {
			if moved, replacedHere := result.Replaced[prevSlot]; prevSlot >= 0 && replacedHere {
				if moved == nil {
					// The edit removed the very model that is the default.
					http.Error(w, "该模型服务当前是默认模型，请先将其他模型设为默认再删除", http.StatusBadRequest)
					return
				}
				// The default row was edited in place: the default follows the
				// edit. An implicit default (first row) needs nothing — the slot
				// kept its position — an explicit one is retargeted.
				if strings.TrimSpace(def.DefaultModel) != "" {
					def.DefaultModel = strings.TrimSpace(moved.Provider) + "/" + strings.TrimSpace(moved.Model)
				}
			} else if !providerListHasEntry(def.LLMProviders, prevDefault.Provider, prevDefault.Model) {
				http.Error(w, "该模型服务当前是默认模型，请先将其他模型设为默认再删除", http.StatusBadRequest)
				return
			}
		}
		cfg.Agents.Definitions[active.ID] = def
		// The default model service is what an empty selection resolves to.
		// Startup refuses a default missing any of these, so a table that
		// leaves one out would save fine and then keep the gateway from
		// starting again.
		if missing := config.ValidateAgentLLMConfigured(&cfg, active.ID).MissingFields(); len(missing) > 0 {
			http.Error(w, "the default model service needs "+strings.Join(missing, ", "), http.StatusBadRequest)
			return
		}
		// Every key-mode row needs its own key, not only the default: a
		// keyless row saves fine and then answers 401 the moment a session
		// selects it. So emptying such a row's field is refused here, not
		// written. The login-backed providers are the exception — their
		// credential is the file the login writes, and an empty api_key is
		// their normal shape.
		for _, entry := range merged {
			if config.CredentialsBackedProvider(entry.Provider) || strings.TrimSpace(entry.APIKey) != "" {
				continue
			}
			http.Error(w, strings.TrimSpace(entry.Provider)+" needs api_key: this model service authenticates with a key, not a login", http.StatusBadRequest)
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

// hookRow is one row of the settings page's hooks table: one command under one
// matcher of one event. The two indices locate it inside HooksSettings, which
// is what an edit or a delete addresses — both write the whole table back.
type hookRow struct {
	Event      string `json:"event"`
	Matcher    string `json:"matcher"`
	Command    string `json:"command"`
	MatcherIdx int    `json:"matcher_idx"`
	CommandIdx int    `json:"command_idx"`
}

// hookTable flattens the stored map into the table's rows: events in name
// order, then each event's matchers and their commands in the order they are
// written, so the same configuration always renders the same table. The
// command column reads whichever field the hook's kind carries, which is what
// a reader looks for there.
func hookTable(hooks config.HooksSettings) []hookRow {
	events := make([]string, 0, len(hooks))
	for event := range hooks {
		events = append(events, event)
	}
	sort.Strings(events)
	rows := make([]hookRow, 0, len(events))
	for _, event := range events {
		for matcherIdx, matcher := range hooks[event] {
			for commandIdx, cmd := range matcher.Hooks {
				command := cmd.Command
				if command == "" {
					command = cmd.Prompt
				}
				if command == "" {
					command = cmd.URL
				}
				rows = append(rows, hookRow{
					Event:      event,
					Matcher:    matcher.Matcher,
					Command:    command,
					MatcherIdx: matcherIdx,
					CommandIdx: commandIdx,
				})
			}
		}
	}
	return rows
}

// handleHooks reads and writes the hook table. Hooks run commands, so the write
// is validated against the hook schema before it is persisted rather than
// failing later at the moment a hook was supposed to fire.
//
// A read with "page" answers one page of the flattened table (hooks are stored
// as a map of events, which a table cannot page directly); an edit or a delete
// still writes the whole map back, so it reads the unpaged answer first.
func (s *Server) handleHooks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg, err := s.persistedConfig()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		q := readListQuery(r)
		if !q.Paged {
			writeAgentsJSON(w, map[string]any{
				"hooks":       cfg.Hooks,
				"known_types": config.PersistentHookTypes(),
				"events":      config.HookEventNames(),
			})
			return
		}
		table := hookTable(cfg.Hooks)
		// The search reads the three columns the table shows.
		needle := strings.ToLower(q.Q)
		matched := make([]hookRow, 0, len(table))
		for _, row := range table {
			if q.Q != "" &&
				!strings.Contains(strings.ToLower(row.Event), needle) &&
				!strings.Contains(strings.ToLower(row.Matcher), needle) &&
				!strings.Contains(strings.ToLower(row.Command), needle) {
				continue
			}
			matched = append(matched, row)
		}
		page, servedPage := pageOf(matched, q)
		writeListPage(w, "rows", page, len(matched), servedPage, q, map[string]any{
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

// handleRuntimeInfo answers GET /api/runtime: the install facts the settings
// page's runtime tab shows that only the gateway knows — the state it is in,
// the auth mode it reads requests under, how many primary agents are
// configured, and the three directories that hold its data. The version and
// the address the browser reached are the browser's own, so they are not
// repeated here.
func (s *Server) handleRuntimeInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	agents := 0
	if resolver, err := s.primaryAgentResolver(); err == nil {
		agents = len(resolver.All())
	}
	payload := map[string]any{
		"status": "ok",
		"agents": agents,
		"home":   s.Home,
		// The data directory's own spelling, beside "state" below: the
		// gateway does not import the bootstrap package for two names.
		"logs_dir":  filepath.Join(s.Home, "logs"),
		"state_dir": filepath.Join(s.Home, "state"),
	}
	// A gateway started without a configuration has no auth mode to name; the
	// key is left out rather than answered with a mode it is not running.
	if cfg := s.liveCfg(); cfg != nil {
		payload["auth"] = gatewayAuthMode(cfg)
	}
	writeJSON(w, payload)
}

// --- Cron jobs and heartbeats ---------------------------------------------
//
// A cron job is a standing instruction for the active primary agent: it fires
// a prompt in a fresh session on a schedule and delivers the answer. A
// heartbeat is the other half of the same idea, kept inside one conversation:
// a recurring prompt that fires into that session while it is idle.

type cronJobRequest struct {
	Name     string `json:"name"`
	Schedule string `json:"schedule"`
	Prompt   string `json:"prompt"`
	// Deliver is a pointer so an edit that does not name a delivery target
	// leaves the job's own alone: the table's pause switch sends only
	// {"enabled": …}, and a job must not lose where it posts when it is paused.
	Deliver     *string `json:"deliver"`
	Enabled     *bool   `json:"enabled"`
	RepeatLimit int     `json:"repeat_limit"`
	// ProjectID is ignored on the wire: a job belongs to the project the route
	// names, which the handler supplies.
	ProjectID string `json:"project_id"`
}

func (r cronJobRequest) input() process.CronJobInput {
	in := process.CronJobInput{
		Name:        r.Name,
		Schedule:    r.Schedule,
		Prompt:      r.Prompt,
		Enabled:     r.Enabled,
		RepeatLimit: r.RepeatLimit,
		ProjectID:   r.ProjectID,
	}
	if r.Deliver != nil {
		in.Deliver = *r.Deliver
		in.DeliverSet = true
	}
	return in
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
	svc := s.cron()
	if svc == nil {
		http.Error(w, "cron service unavailable", http.StatusServiceUnavailable)
		return
	}
	project, agentID, ok := s.projectScope(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		jobs, err := svc.ListJobsForProject(r.Context(), agentID, project.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		q := readListQuery(r)
		if !q.Paged {
			writeAgentsJSON(w, map[string]any{"agent_id": agentID, "project_id": project.ID, "records": jobs})
			return
		}
		// The search box reads the task's name and what it is asked to do, the
		// two things a person recognises a task by.
		needle := strings.ToLower(q.Q)
		matched := make([]state.CronJob, 0, len(jobs))
		for _, job := range jobs {
			if q.Q != "" &&
				!strings.Contains(strings.ToLower(job.Name), needle) &&
				!strings.Contains(strings.ToLower(job.Prompt), needle) {
				continue
			}
			matched = append(matched, job)
		}
		page, servedPage := pageOf(matched, q)
		writeListPage(w, "records", page, len(matched), servedPage, q, map[string]any{
			"agent_id":   agentID,
			"project_id": project.ID,
		})
	case http.MethodPost:
		var req cronJobRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		in := req.input()
		// The job belongs to the project the route names, not to anything the
		// body claims.
		in.ProjectID = project.ID
		job, err := svc.CreateJob(r.Context(), agentID, in)
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
	svc := s.cron()
	if svc == nil {
		http.Error(w, "cron service unavailable", http.StatusServiceUnavailable)
		return
	}
	project, agentID, ok := s.projectScope(w, r)
	if !ok {
		return
	}
	id := strings.TrimSpace(ParamsFromContext(r.Context()).ByName("jobId"))
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
		in := req.input()
		in.ProjectID = project.ID
		job, err := svc.UpdateJob(r.Context(), agentID, id, in)
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
	svc := s.cron()
	if svc == nil {
		http.Error(w, "cron service unavailable", http.StatusServiceUnavailable)
		return
	}
	_, agentID, ok := s.projectScope(w, r)
	if !ok {
		return
	}
	id := strings.TrimSpace(ParamsFromContext(r.Context()).ByName("jobId"))
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
	svc := s.cron()
	if svc == nil {
		http.Error(w, "cron service unavailable", http.StatusServiceUnavailable)
		return
	}
	_, agentID, ok := s.projectScope(w, r)
	if !ok {
		return
	}
	id := strings.TrimSpace(ParamsFromContext(r.Context()).ByName("jobId"))
	q := readListQuery(r)
	if q.Paged {
		// A table's page replaces the old "limit": the window is page and
		// page_size, and the answer says how many fires the job has in all.
		runs, total, err := svc.JobRunsPage(r.Context(), agentID, id, q.PageSize, (q.Page-1)*q.PageSize)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		servedPage := min(q.Page, max((total+q.PageSize-1)/q.PageSize, 1))
		writeListPage(w, "runs", runs, total, servedPage, q, map[string]any{"job_id": id})
		return
	}
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

// handleHeartbeats lists the heartbeats on this agent's conversations, each
// with the title of the conversation it belongs to, so a surface can show
// which conversations are being asked again without opening each one.
func (s *Server) handleHeartbeats(w http.ResponseWriter, r *http.Request) {
	svc, agentID, ok := s.cronScope(w)
	if !ok {
		return
	}
	beats, err := svc.ListHeartbeats(r.Context(), agentID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	records := make([]map[string]any, 0, len(beats))
	for i := range beats {
		title, err := s.Sessions.SessionTitle(r.Context(), beats[i].SessionID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		row := heartbeatResponse(&beats[i])
		row["session_title"] = title
		records = append(records, row)
	}
	writeAgentsJSON(w, map[string]any{"records": records})
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

func (s *Server) projectRow(p state.Project) map[string]any {
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
		"last_attach_dir": p.LastAttachDir,
		"created_at":      p.CreatedAt,
		"updated_at":      p.UpdatedAt,
		// The picker reads these two to badge a row: a root that is no longer
		// a directory cannot be opened, and a root nobody has judged yet asks
		// for the trust prompt before it can be used.
		"trust_level": projectTrustLevel(s.Home, p.Root),
		"missing":     !hostDirExists(p.Root),
	}
}

// projectRows answers a project page's rows: the stored project, the facts a
// table draws beside it (a trust decision, a missing root) and the count of
// the project's conversations — one grouped query for the whole page, not one
// per row.
func (s *Server) projectRows(ctx context.Context, projects []state.Project) ([]map[string]any, error) {
	ids := make([]string, 0, len(projects))
	for _, p := range projects {
		ids = append(ids, p.ID)
	}
	counts := map[string]int{}
	if s.Sessions != nil {
		var err error
		if counts, err = s.Sessions.ConversationCountsByProject(ctx, ids); err != nil {
			return nil, err
		}
	}
	rows := make([]map[string]any, 0, len(projects))
	for _, p := range projects {
		row := s.projectRow(p)
		row["session_count"] = counts[p.ID]
		rows = append(rows, row)
	}
	return rows, nil
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
	q := readListQuery(r)
	if !q.Paged {
		// The chat project picker and the folder dialog read this list whole,
		// windowed by their own limit/offset: only a table pages it.
		query := r.URL.Query()
		limit := intFromQuery(query.Get("limit"), 50)
		offset := intFromQuery(query.Get("offset"), 0)
		projects, err := store.ListProjects(r.Context(), state.ListProjectsOptions{
			Search: strings.TrimSpace(query.Get("search")),
			Sort:   strings.TrimSpace(query.Get("sort")),
		}, limit, offset)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		rows, err := s.projectRows(r.Context(), projects)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"projects": rows})
		return
	}
	// "q" is the table's search box; "search" is the picker's, and a request
	// carrying only that must still filter.
	search := q.Q
	if search == "" {
		search = strings.TrimSpace(r.URL.Query().Get("search"))
	}
	opts := state.ListProjectsOptions{Search: search, Sort: q.Sort}
	if q.Order != "" {
		desc := q.Order == "desc"
		opts.Desc = &desc
	}
	projects, total, err := store.ListProjectsPage(r.Context(), opts, q.PageSize, (q.Page-1)*q.PageSize)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rows, err := s.projectRows(r.Context(), projects)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// The store read the last page when the request asked past it; the answer
	// names the page it actually served.
	servedPage := min(q.Page, max((total+q.PageSize-1)/q.PageSize, 1))
	writeListPage(w, "projects", rows, total, servedPage, q, nil)
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
	root, err := state.ValidateProjectRoot(s.Home, body.Root)
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
		ProjectKey:     state.ProjectKeyForRoot(root),
		MemoryScope:    body.MemoryScope,
		ResourceAccess: resourceAccess,
	})
	if errors.Is(err, state.ErrProjectRootTaken) {
		// One project per (agent, root): the row the caller collided with is
		// handed back so the client can open it instead.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "project_root_taken", "project": s.projectRow(p)})
		return
	}
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
	row := s.projectRow(p)
	row["trust_recorded"] = trustRecorded
	if !trustRecorded && body.Trust {
		row["trust_error"] = "the trust decision could not be recorded"
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(row)
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
		row := s.projectRow(p)
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
		_ = json.NewEncoder(w).Encode(s.projectRow(updated))
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
	err := store.Delete(r.Context(), strings.TrimSpace(ParamsFromContext(r.Context()).ByName("id")))
	switch {
	case errors.Is(err, state.ErrProjectBusy):
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "project_busy"})
		return
	case err != nil:
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

// handleProjectDeletePreview reports what deleting a project would take with
// it, so a confirmation dialog can show the counts before the user commits.
func (s *Server) handleProjectDeletePreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	store := s.projectStore()
	if store == nil {
		http.Error(w, "projects unavailable", http.StatusServiceUnavailable)
		return
	}
	sessions, cronJobs, running, err := store.DeletePreview(r.Context(), strings.TrimSpace(ParamsFromContext(r.Context()).ByName("id")))
	if errors.Is(err, state.ErrProjectNotFound) {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"sessions": sessions, "cron_jobs": cronJobs, "running": running})
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
	res, err := s.createSessionWithBirth(r.Context(), title, p, state.SessionSourceConversation)
	if err != nil {
		if errors.Is(err, errSessionsDisabled) {
			http.Error(w, "sessions disabled", http.StatusServiceUnavailable)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	res.Project = s.projectRow(p)
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
	q := readListQuery(r)
	if !q.Paged {
		query := r.URL.Query()
		sessions, err := s.Sessions.ListSessionsForProject(r.Context(), strings.TrimSpace(ParamsFromContext(r.Context()).ByName("id")), intFromQuery(query.Get("limit"), 50), intFromQuery(query.Get("offset"), 0))
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
		return
	}
	projectID := strings.TrimSpace(ParamsFromContext(r.Context()).ByName("id"))
	sessions, total, err := s.Sessions.ListSessionsForProjectPage(r.Context(), projectID, state.ProjectSessionsQuery{
		Search: q.Q,
		Sort:   q.Sort,
		Desc:   q.desc(true),
	}, q.PageSize, (q.Page-1)*q.PageSize)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Every heartbeat of the agent is read once for the whole page: a project's
	// conversations are a handful, and one query beats one per row.
	beats := s.activeHeartbeatRows(r.Context())
	rows := make([]map[string]any, 0, len(sessions))
	for _, item := range sessions {
		row := map[string]any{
			"id":         item.ID,
			"title":      sessionTitleOrEmpty(item.ID, item.Title),
			"created_at": item.CreatedAt,
			"updated_at": item.UpdatedAt,
			// A session that never pinned a model answers an empty name — never
			// the agent's default — and the table draws its own placeholder.
			"model":         item.Model,
			"message_count": item.MessageCount,
		}
		if beat, ok := beats[item.ID]; ok {
			row["heartbeat"] = beat
		}
		rows = append(rows, row)
	}
	servedPage := min(q.Page, max((total+q.PageSize-1)/q.PageSize, 1))
	writeListPage(w, "sessions", rows, total, servedPage, q, nil)
}

// activeHeartbeatRows reads every heartbeat on the active primary agent's
// conversations once, keyed by session, so a conversation table can draw its
// heartbeat column without a lookup per row. A missing cron service or primary
// agent answers an empty map: the column is then simply not drawn, and the list
// still answers. It never writes to the response — a caller that cannot read
// heartbeats must still get its rows.
func (s *Server) activeHeartbeatRows(ctx context.Context) map[string]map[string]any {
	beats := map[string]map[string]any{}
	svc := s.cron()
	if svc == nil {
		return beats
	}
	active, err := s.activePrimarySummary()
	if err != nil {
		return beats
	}
	list, err := svc.ListHeartbeats(ctx, active.ID)
	if err != nil {
		return beats
	}
	for i := range list {
		full := heartbeatResponse(&list[i])
		beats[list[i].SessionID] = map[string]any{
			"interval_seconds": full["interval_seconds"],
			"paused":           full["paused"],
		}
	}
	return beats
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
	q := readListQuery(r)
	if !q.Paged {
		writeJSON(w, map[string]any{"files": rows})
		return
	}
	// The purpose column is a locale string the page derives from the file
	// name, so the name is the only text the server holds to search.
	matched := make([]ruleFileRow, 0, len(rows))
	for _, row := range rows {
		if q.Q != "" && !strings.Contains(strings.ToLower(row.Name), strings.ToLower(q.Q)) {
			continue
		}
		matched = append(matched, row)
	}
	page, servedPage := pageOf(matched, q)
	writeListPage(w, "files", page, len(matched), servedPage, q, nil)
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
	q := readListQuery(r)
	if !q.Paged {
		writeJSON(w, map[string]any{"files": rows, "create": creatable})
		return
	}
	// The file column reads `dir/FOREBRAIN.md`, and the name is the same for
	// every row, so the directory is the only text worth searching.
	needle := strings.ToLower(q.Q)
	matched := make([]row, 0, len(rows))
	for _, item := range rows {
		if q.Q != "" && !strings.Contains(strings.ToLower(item.Dir), needle) {
			continue
		}
		matched = append(matched, item)
	}
	page, servedPage := pageOf(matched, q)
	// The layers with no file yet are not a list the table pages through: they
	// are what the create dialog offers, and they stay whole whatever the search
	// leaves.
	writeListPage(w, "files", page, len(matched), servedPage, q, map[string]any{"create": creatable})
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
	// Effective is the preset actually executing after the trust gate (a
	// persisted full-access in an untrusted directory narrows to Default);
	// Source is who set the stored row (picker | yolo | observed) — backend
	// and logs only, the surfaces never label it.
	Effective *string `json:"effective,omitempty"`
	Source    string  `json:"source,omitempty"`
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
	// Persist first, apply second: the row is what the other surface and the
	// next process restore, so a choice that failed to persist never ran
	// only in memory either.
	s.saveSessionPresetRow(r.Context(), sid, preset.ID)
	for _, update := range preset.SessionUpdates(sid) {
		runner.ApplyPermissionUpdate(update)
	}
	writeJSON(w, map[string]any{"ok": true, "description": preset.Description})
}

// saveSessionPresetRow persists a picker preset choice for one session. The
// store is the gateway runner's own; a session another agent owns reads as
// no row, which the ownership check above has already ruled out.
func (s *Server) saveSessionPresetRow(ctx context.Context, sid, presetID string) {
	if s == nil || s.Runner == nil || s.Runner.Deps == nil || s.Runner.Deps.SessionStore == nil {
		return
	}
	if _, err := s.Runner.Deps.SessionStore.SaveSessionPermissionPreset(ctx, sid, presetID, state.SessionPresetSourcePicker); err != nil {
		slog.Warn("persist session approval preset", "session_id", sid, "err", err)
	}
}

// handleSessionPresetGet names the preset one conversation is running under —
// the shared reading (Runner.SessionPresetFor): the persisted row first (with
// lazy YOLO inference), the live in-process preset when no row exists, the
// configuration default last. current is the row's choice, effective what
// actually executes after the trust gate, and null when nothing matches.
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
	st := runner.SessionPresetFor(r.Context(), sid)
	if st.Current == "" {
		writeJSON(w, approvalDefaultAnswer{Current: nil})
		return
	}
	current := st.Current
	effective := st.Effective
	answer := approvalDefaultAnswer{Current: &current, Effective: &effective, Source: st.Source}
	if preset, ok := safety.ApprovalPresetByID(st.Current); ok {
		answer.Description = preset.Description
	}
	writeJSON(w, answer)
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
	rules := permissionRuleRows(scope.rt.Snapshot(scope.cfg), string(safety.SourceProjectSettings), "")
	q := readListQuery(r)
	if !q.Paged {
		writeJSON(w, map[string]any{
			"applies": applies,
			"rules":   rules,
		})
		return
	}
	// The search box reads the tool the rule names and the rule's own text, the
	// same two fields the agent's permission table searches.
	needle := strings.ToLower(q.Q)
	matched := make([]permissionRuleRow, 0, len(rules))
	for _, rule := range rules {
		if q.Q != "" &&
			!strings.Contains(strings.ToLower(rule.ToolName), needle) &&
			!strings.Contains(strings.ToLower(rule.RuleContent), needle) {
			continue
		}
		matched = append(matched, rule)
	}
	page, servedPage := pageOf(matched, q)
	// applies travels with the page: it is why an empty table is empty, and it
	// is not a row of the table.
	writeListPage(w, "rules", page, len(matched), servedPage, q, map[string]any{"applies": applies})
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

// lspServerPage is one page of the settings page's language-server table: the
// snapshot's own fields (the project, the switches, the notes) stay on top and
// its servers become the page — the same answer, cut, so a surface that reads
// the whole snapshot still finds everything it read before.
type lspServerPage struct {
	event.LSPSnapshot
	Servers  []event.LSPServerStatus `json:"servers"`
	Total    int                     `json:"total"`
	Page     int                     `json:"page"`
	PageSize int                     `json:"page_size"`
}

// handleLSPSnapshot answers GET /api/v1/lsp?session_id=… with the same
// snapshot the terminal's /lsp panel and the web chat's text fallback render.
// A request carrying "page" answers one page of the servers, searched over the
// id and the languages the table lists.
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
	snap := ctl.Snapshot()
	q := readListQuery(r)
	if !q.Paged {
		writeJSON(w, snap)
		return
	}
	needle := strings.ToLower(q.Q)
	matched := make([]event.LSPServerStatus, 0, len(snap.Servers))
	for _, server := range snap.Servers {
		if q.Q != "" && !lspServerMatches(server, needle) {
			continue
		}
		matched = append(matched, server)
	}
	page, servedPage := pageOf(matched, q)
	writeJSON(w, lspServerPage{
		LSPSnapshot: snap,
		Servers:     page,
		Total:       len(matched),
		Page:        servedPage,
		PageSize:    q.PageSize,
	})
}

// lspServerMatches reports whether the search term is in the two columns the
// language-server table searches: the id and the languages it serves.
func lspServerMatches(server event.LSPServerStatus, needle string) bool {
	if strings.Contains(strings.ToLower(server.ID), needle) {
		return true
	}
	for _, language := range server.Languages {
		if strings.Contains(strings.ToLower(language), needle) {
			return true
		}
	}
	return false
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
	q := readListQuery(r)
	if !q.Paged {
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
			// The project page's "always allow" switch reads the same answer
			// both paged and unpaged, so the toolbar toggle never guesses.
			"auto_allow": process.ProjectServersAutoAllowed(workspace, launch),
		})
		return
	}
	// The table is one list of entries: the ones awaiting confirmation first,
	// then the allowed ones, then the denied ones — each group in the order its
	// own reader gives. Only a pending entry carries a summary: the consent store
	// records the others by id alone. Every row carries the entry's current
	// fingerprint from one shared read, so the per-row decision the page sends
	// back can be bound to exactly the entry that was shown (all three groups
	// from the same map keeps the wording consistent).
	fps := process.ProjectLSPFingerprints(workspace, launch)
	type lspRow struct {
		ID          string `json:"id"`
		Summary     string `json:"summary"`
		Status      string `json:"status"`
		Fingerprint string `json:"fingerprint"`
	}
	all := make([]lspRow, 0, len(pending)+len(allowed)+len(denied))
	for _, item := range pending {
		all = append(all, lspRow{ID: item.ID, Summary: item.Summary, Status: lspStatusPending, Fingerprint: fps[item.ID]})
	}
	for _, id := range allowed {
		all = append(all, lspRow{ID: id, Status: lspStatusAllowed, Fingerprint: fps[id]})
	}
	for _, id := range denied {
		all = append(all, lspRow{ID: id, Status: lspStatusDenied, Fingerprint: fps[id]})
	}
	needle := strings.ToLower(q.Q)
	matched := make([]lspRow, 0, len(all))
	for _, row := range all {
		if q.Q != "" &&
			!strings.Contains(strings.ToLower(row.ID), needle) &&
			!strings.Contains(strings.ToLower(row.Summary), needle) {
			continue
		}
		matched = append(matched, row)
	}
	page, servedPage := pageOf(matched, q)
	writeListPage(w, "rows", page, len(matched), servedPage, q, map[string]any{
		"trusted":    trusted,
		"notes":      notes,
		"auto_allow": process.ProjectServersAutoAllowed(workspace, launch),
	})
}

// The three decisions a project's language-server entry can be in, as the web
// table words them.
const (
	lspStatusPending = "pending"
	lspStatusAllowed = "allowed"
	lspStatusDenied  = "denied"
)

// handleProjectLSPConsent is the web confirmation entry for project-level
// language servers. Body: {"allow": [...], "decide": [...], "fingerprints":
// [{name, fingerprint}]}. decide names the entries the request decides —
// allow the ones also in allow, deny the rest — and is what the project
// page's per-row buttons send, so a row decides only itself and a denied
// entry is allowed again by deciding it once more. fingerprints carries what
// the page showed for each decided entry; any entry that no longer matches
// it fails the whole request with 409 and records nothing. Without decide
// the call is the startup prompt's batch answer: every pending entry not in
// allow is recorded as denied.
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
		// Decide names the entries this request is a decision for. Absent
		// means the caller speaks for the whole batch, the way the startup
		// prompt does: everything not allowed is recorded as denied. An
		// empty list decides nothing.
		Decide *[]string `json:"decide"`
		// Fingerprints carries the per-entry fingerprints the page showed
		// (keyed by id in Name), and is required whenever decide is present:
		// the decision is recorded only against entries that still match
		// them.
		Fingerprints []consentFingerprint `json:"fingerprints"`
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
	if body.Decide != nil {
		fps, missing, ok := consentFingerprintMap(*body.Decide, body.Fingerprints)
		if !ok {
			http.Error(w, fmt.Sprintf("fingerprint required for %q", missing), http.StatusBadRequest)
			return
		}
		err = process.DecideProjectLSPConsentsFor(workspace, launch, *body.Decide, body.Allow, fps)
	} else {
		err = process.DecideProjectLSPConsents(workspace, launch, body.Allow)
	}
	if err != nil {
		var stale interface{ StaleNames() []string }
		if errors.As(err, &stale) {
			writeStaleConsent(w, stale)
			return
		}
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
