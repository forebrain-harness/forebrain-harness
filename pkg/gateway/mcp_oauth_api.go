package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/mcp"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

type gatewayMcpPKCE struct {
	ServerName   string
	CodeVerifier string
	ExpiresUnix  int64
}

func (s *Server) mcpPKCEPut(state string, e gatewayMcpPKCE) {
	if s == nil {
		return
	}
	s.mcpOAuthPKCEMu.Lock()
	defer s.mcpOAuthPKCEMu.Unlock()
	if s.mcpOAuthPKCE == nil {
		s.mcpOAuthPKCE = make(map[string]gatewayMcpPKCE)
	}
	s.mcpOAuthPKCE[state] = e
}

func (s *Server) mcpPKCETake(state string) (gatewayMcpPKCE, bool) {
	if s == nil {
		return gatewayMcpPKCE{}, false
	}
	s.mcpOAuthPKCEMu.Lock()
	defer s.mcpOAuthPKCEMu.Unlock()
	if s.mcpOAuthPKCE == nil {
		return gatewayMcpPKCE{}, false
	}
	e, ok := s.mcpOAuthPKCE[state]
	if ok {
		delete(s.mcpOAuthPKCE, state)
	}
	return e, ok
}

func (s *Server) findMCPServerMerged(name string) (appcfg.MCPServerConfig, error) {
	if s == nil {
		return appcfg.MCPServerConfig{}, fmt.Errorf("nil server")
	}
	lc := s.liveCfg()
	if lc == nil {
		return appcfg.MCPServerConfig{}, fmt.Errorf("config unavailable")
	}
	nm := strings.TrimSpace(name)
	// A server the disable store kept out of this session can still be
	// authenticated for the next one.
	list := lc.Agents.Defaults.MCPServers
	if s.Runner != nil {
		list = append(append([]appcfg.MCPServerConfig(nil), s.Runner.MCPServers...), s.Runner.MCPDisabled...)
	}
	for _, srv := range list {
		if mcp.SameServerName(srv.Name, nm) {
			return mcp.MergeMCPServerOAuth(s.Home, srv), nil
		}
	}
	return appcfg.MCPServerConfig{}, fmt.Errorf("mcp server %q not found", name)
}

// handleMCPServersV1 reports the configured servers and, with them, what each
// one is doing right now.
//
// The runtime half is what makes this endpoint answer the question a UI has:
// "why are this server\'s tools missing". It is read from the Runner that serves
// the request — the session\'s own Runner when ?session_id= names one, the
// primary Runner otherwise — because the process-wide mirror only knows what the
// last Runner to publish happened to see. Which of the two answered is stated in
// the response rather than left for the client to guess.
func (s *Server) handleMCPServersV1(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	if s == nil {
		http.Error(w, "nil", http.StatusInternalServerError)
		return
	}
	lc := s.liveCfg()
	if lc == nil {
		http.Error(w, "config unavailable", http.StatusServiceUnavailable)
		return
	}
	var rows []map[string]any
	// The runtime source is the session\'s Runner when the caller named one, so a
	// project session reports the servers its own generation started.
	//
	// A session name that resolves to no worker (a channel session, a stale id)
	// falls back to the primary Runner, and the response says so through
	// runtime_scope: reporting a different Runner\'s state as this session\'s
	// would be worse than saying which Runner answered.
	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	runtimeRunner := s.Runner
	runtimeScope := "primary"
	if sessionID != "" {
		if resolved := s.runnerFor(r.Context(), sessionID); resolved != nil {
			runtimeRunner = resolved
			if resolved != s.Runner {
				runtimeScope = "session"
			}
		} else {
			runtimeScope = "primary"
		}
	}
	var runtime *turn.MCPRuntimeView
	if runtimeRunner != nil {
		snapshot := runtimeRunner.MCPStartup().Snapshot()
		if len(snapshot.Servers) > 0 {
			runtime = &turn.MCPRuntimeView{
				Servers:    snapshot.Servers,
				Generation: snapshot.Generation,
				Pending:    snapshot.Pending,
			}
		}
	}
	// The rows are the /mcp inventory both surfaces share: the session's
	// frozen list and the servers its agent's disable store kept out of it.
	// Only a Server assembled without any Runner falls back to the config
	// file's list, with no runtime to report.
	rc := s.mcpRowsContext(lc, runtimeRunner)
	// ?scope=global|agent lists one configuration layer as a client edits it,
	// which is not the same question as "what is running": an entry a higher
	// layer shadows is still configured and still editable.
	if scope := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("scope"))); scope == appcfg.MCPServerScopeGlobal || scope == appcfg.MCPServerScopeAgent {
		s.handleMCPLayerList(w, r, lc, rc, scope)
		return
	}
	// ?view=agent is the menu bar's MCP page: the primary agent's own layer on
	// top of the shared one, the agent's rows first and a same-named shared
	// entry hidden behind it. It is neither "what is running" (that is this
	// listing) nor one layer alone (that is ?scope).
	if view := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("view"))); view == "agent" {
		s.handleMCPAgentView(w, r, lc, rc)
		return
	}
	configs := map[string]appcfg.MCPServerConfig{}
	if runtimeRunner != nil {
		for _, list := range [][]appcfg.MCPServerConfig{runtimeRunner.MCPServers, runtimeRunner.MCPDisabled} {
			for _, srv := range list {
				configs[strings.ToLower(strings.TrimSpace(srv.Name))] = srv
			}
		}
	} else {
		for _, srv := range lc.Agents.Defaults.MCPServers {
			configs[strings.ToLower(strings.TrimSpace(srv.Name))] = srv
		}
	}
	for _, group := range rc.inv.Groups {
		for i := range group.Entries {
			entry := group.Entries[i]
			srv := configs[strings.ToLower(entry.Name)]
			rows = append(rows, s.mcpServerRow(rc, srv, entry.Scope, entry.Source, entry.Running, &entry))
		}
	}
	projectBlock := map[string]any{}
	if runtimeRunner != nil {
		summary := runtimeRunner.MCPProjectStatus()
		if len(summary.OverriddenGlobal) > 0 || len(summary.NotApplied) > 0 || summary.PendingReload {
			projectBlock["project_root"] = summary.ProjectRoot
			projectBlock["pending_reload"] = summary.PendingReload
			if len(summary.OverriddenGlobal) > 0 {
				projectBlock["overridden_global"] = summary.OverriddenGlobal
			}
			if len(summary.NotApplied) > 0 {
				items := make([]map[string]string, 0, len(summary.NotApplied))
				for _, item := range summary.NotApplied {
					items = append(items, map[string]string{"name": item.Name, "reason": item.Reason})
				}
				projectBlock["not_applied"] = items
			}
		}
	}
	body := map[string]any{"servers": rows, "project": projectBlock}
	// Which Runner answered, so a client can tell a session\'s own servers from the
	// primary\'s rather than assuming either.
	body["runtime_scope"] = runtimeScope
	body["runtime_available"] = runtime != nil
	if runtime != nil {
		body["generation"] = runtime.Generation
		body["pending"] = runtime.Pending
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func (s *Server) handleMCPOAuthPKCEStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		ServerName string `json:"server_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	srv, err := s.findMCPServerMerged(body.ServerName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	o := srv.OAuth
	if strings.TrimSpace(o.AuthorizationURL) == "" || strings.TrimSpace(o.TokenURL) == "" || strings.TrimSpace(o.ClientID) == "" || strings.TrimSpace(o.RedirectURL) == "" {
		http.Error(w, "server oauth missing authorization_url token_url client_id redirect_url", http.StatusBadRequest)
		return
	}
	verifier, err := mcp.PKCEVerifier()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	challenge := mcp.PKCEChallengeS256(verifier)
	state, err := mcp.PKCEVerifier()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.mcpPKCEPut(state, gatewayMcpPKCE{
		ServerName:   strings.TrimSpace(srv.Name),
		CodeVerifier: verifier,
		ExpiresUnix:  time.Now().Unix() + 900,
	})
	authURL, err := mcp.OAuthAuthorizeURL(o, state, challenge)
	if err != nil {
		_, _ = s.mcpPKCETake(state)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"server_name":        srv.Name,
		"state":              state,
		"authorize_url":      authURL,
		"expires_in_seconds": 900,
	})
}

func (s *Server) handleMCPOAuthPKCEFinish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		ServerName string `json:"server_name"`
		State      string `json:"state"`
		Code       string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ent, ok := s.mcpPKCETake(strings.TrimSpace(body.State))
	if !ok || time.Now().Unix() > ent.ExpiresUnix {
		http.Error(w, "invalid or expired state", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(ent.ServerName) != strings.TrimSpace(body.ServerName) {
		http.Error(w, "server_name mismatch", http.StatusBadRequest)
		return
	}
	srv, err := s.findMCPServerMerged(body.ServerName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	tok, err := mcp.ExchangeOAuthAuthCode(ctx, srv.OAuth, strings.TrimSpace(body.Code), ent.CodeVerifier)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	delta := appcfg.MCPOAuthConfig{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		TokenType:    tok.TokenType,
	}
	if strings.TrimSpace(tok.RefreshToken) != "" {
		delta.Mode = "refresh"
	} else {
		delta.Mode = "static"
	}
	if err := mcp.SaveOAuthOverlayMerge(s.Home, mcp.OverlayProjectKey(srv), srv.Name, delta); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.notifyRuntimeHook(r.Context(), tool.NotificationRequest{
		Key:     "mcp-connectivity",
		Message: "MCP OAuth completed for " + strings.TrimSpace(srv.Name),
		Title:   "MCP OAuth",
		Channel: "gateway",
	})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "server_name": srv.Name})
}

// handleProjectMCPPreview resolves the project-level MCP view for a project
// without a session: which servers would load, which are pending consent, which
// higher-layer entries they override, and what each effective entry is. The web
// MCP view uses it so the confirmation entry can stand on its own, and each row
// carries the whole entry (secrets masked) so the same row can be opened in the
// edit modal.
func (s *Server) handleProjectMCPPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
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
	launch, err := safety.ResolveRegisteredContext(s.Home, p.Root)
	if err != nil {
		http.Error(w, "project could not be resolved", http.StatusBadRequest)
		return
	}
	workspace := ""
	agents := appcfg.AgentsSection{}
	agentName := ""
	if cfg := s.liveCfg(); cfg != nil {
		agents = cfg.Agents
	}
	if s.Runner != nil {
		workspace = s.Runner.WorkspaceRoot
		agentName = s.Runner.AgentName
	}
	res := process.ResolveSessionMCP(s.Home, workspace, agents, agentName, launch)
	servers := make([]map[string]any, 0, len(res.Servers))
	for _, srv := range res.Servers {
		servers = append(servers, mcpPreviewRow(srv))
	}
	notApplied := make([]map[string]string, 0, len(res.Summary.NotApplied))
	for _, item := range res.Summary.NotApplied {
		notApplied = append(notApplied, map[string]string{"name": item.Name, "reason": item.Reason})
	}
	pending := process.PendingProjectMCPConsents(workspace, launch)
	pendingRows := make([]map[string]string, 0, len(pending))
	for _, item := range pending {
		pendingRows = append(pendingRows, map[string]string{"name": item.Name, "summary": item.Summary})
	}
	// A declined entry does not load, but it is still on record and can be
	// allowed again, so the paged view lists it as a row of its own.
	declined := process.DeclinedProjectMCPConsents(workspace, launch)
	// The three notes above ride every answer: they say what the project's file
	// asks for and did not get, which is the same question whether or not the
	// caller is paging a table. not_applied repeats neither the pending nor the
	// declined entries: a paged answer lists those as rows, and the note under
	// the table says what the table cannot show.
	listed := make(map[string]struct{}, len(pending)+len(declined))
	for _, item := range pending {
		listed[strings.ToLower(strings.TrimSpace(item.Name))] = struct{}{}
	}
	for _, item := range declined {
		listed[strings.ToLower(strings.TrimSpace(item.Name))] = struct{}{}
	}
	unlisted := make([]map[string]string, 0, len(notApplied))
	for _, item := range notApplied {
		if _, ok := listed[strings.ToLower(strings.TrimSpace(item["name"]))]; ok {
			continue
		}
		unlisted = append(unlisted, item)
	}
	extra := map[string]any{
		"overridden_global": res.Summary.OverriddenGlobal,
		"not_applied":       unlisted,
		"pending_consent":   pendingRows,
	}
	if q := readListQuery(r); q.Paged {
		// A table's order is its own, and it follows the project entry's own
		// lifecycle: the entries this page still has to decide (the
		// unconfirmed ones first, so the decisions sit at the top), then the
		// ones it allowed, then the ones it turned down and can take back.
		// The unconfirmed and declined entries are rows here — the table is
		// where "允许 / 拒绝" is offered — while the unpaged answer keeps them
		// in pending_consent alone.
		rows := make([]map[string]any, 0, len(res.Servers)+len(pending)+len(declined))
		lc := s.liveCfg()
		rc := s.mcpRowsContext(lc, s.Runner)
		source := turn.GlobalMCPSource(lc)
		for _, row := range mcpConsentPreviewRows(pending, launch, "pending") {
			rows = append(rows, row)
		}
		for _, srv := range res.Servers {
			if mcpServerScopeOf(srv) != appcfg.MCPServerScopeProject {
				continue
			}
			row := mcpPreviewRow(srv)
			row["allowed"] = true
			rows = append(rows, row)
		}
		rows = append(rows, mcpConsentPreviewRows(declined, launch, "declined")...)
		for _, scope := range []string{appcfg.MCPServerScopeAgent, appcfg.MCPServerScopeGlobal} {
			for _, srv := range res.Servers {
				if mcpServerScopeOf(srv) != scope {
					continue
				}
				entry := mcpLayerEntry(rc, srv, scope)
				rows = append(rows, s.mcpServerRow(rc, srv, scope, source, entry != nil, entry))
			}
		}
		writeMCPRows(w, q, mcpLayerFilter(r.URL.Query().Get("source")), rows, extra)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"servers":           servers,
		"overridden_global": res.Summary.OverriddenGlobal,
		"not_applied":       notApplied,
		"pending_consent":   pendingRows,
	})
}

// mcpPreviewRow renders one entry of a project view's effective list: the whole
// entry as a client edits it, plus the layer it resolved from. Being in this
// list means a session would load it.
func mcpPreviewRow(srv appcfg.MCPServerConfig) map[string]any {
	row := readMCPServerWrite(srv)
	row["scope"] = mcpServerScopeOf(srv)
	row["url_set"] = strings.TrimSpace(srv.URL) != ""
	row["running"] = true
	return row
}

// mcpConsentPreviewRows renders the project entries that are configured but
// not allowed, as table rows: the entry's own fields from the project's file
// (secrets masked, the same rendering every other row uses) and the one-line
// summary the confirmation prompt shows. flag names the state the rows are in
// — "pending" for an entry no one decided yet, "declined" for one that was
// turned down and can still be allowed. An entry the file no longer parses is
// still a row — it is listed by name, because something is asking to load it.
func mcpConsentPreviewRows(items []mcp.PendingProjectConsent, launch safety.ProjectContext, flag string) []map[string]any {
	entries, _ := mcp.LoadProjectMCPServers(strings.TrimSpace(launch.Project.Root))
	byName := make(map[string]appcfg.MCPServerConfig, len(entries))
	for _, srv := range entries {
		byName[strings.ToLower(strings.TrimSpace(srv.Name))] = srv
	}
	rows := make([]map[string]any, 0, len(items))
	for _, item := range items {
		srv, ok := byName[strings.ToLower(strings.TrimSpace(item.Name))]
		var row map[string]any
		if ok {
			row = mcpPreviewRow(srv)
		} else {
			row = map[string]any{"name": strings.TrimSpace(item.Name)}
		}
		// An entry read straight from the project's file carries no layer
		// stamp (the merge stamps what it resolves), and it is this project's
		// own entry either way.
		row["scope"] = appcfg.MCPServerScopeProject
		row[flag] = true
		row["running"] = false
		row["summary"] = item.Summary
		rows = append(rows, row)
	}
	return rows
}

// handleProjectMCPConsent is the web confirmation entry for project-level MCP
// servers. Body: {"allow": [...], "decide": [...]}. decide names the entries
// the request decides — allow the ones also in allow, deny the rest — and is
// what the project page's per-row buttons send, so a row decides only itself
// and a declined entry is allowed again by deciding it once more. Without
// decide the call is the startup prompt's batch answer: every pending entry
// not in allow is recorded as declined.
func (s *Server) handleProjectMCPConsent(w http.ResponseWriter, r *http.Request) {
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
		Allow []string `json:"allow"`
		// Decide names the entries this request is a decision for. Absent
		// means the caller speaks for the whole batch, the way the startup
		// prompt does: everything not allowed is recorded as declined. An
		// empty list decides nothing.
		Decide *[]string `json:"decide"`
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
	// A table row decides its own entry, and a declined entry is allowed
	// again by deciding it once more; the startup prompt's batch semantics
	// stay behind the callers that send no decide.
	if body.Decide != nil {
		err = process.DecideProjectMCPConsentsFor(workspace, launch, *body.Decide, body.Allow)
	} else {
		err = process.DecideProjectMCPConsents(workspace, launch, body.Allow)
	}
	if err != nil {
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
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "allowed": body.Allow})
}

// handleMCPServerDisableV1 marks a server disabled from the next session.
// POST /api/v1/mcp/servers/disable with {"name": "...", "session_id": "..."}.
func (s *Server) handleMCPServerDisableV1(w http.ResponseWriter, r *http.Request) {
	s.handleMCPServerToggle(w, r, true)
}

// handleMCPServerEnableV1 clears a server's disabled mark.
// POST /api/v1/mcp/servers/enable with {"name": "...", "session_id": "..."}.
func (s *Server) handleMCPServerEnableV1(w http.ResponseWriter, r *http.Request) {
	s.handleMCPServerToggle(w, r, false)
}

// handleMCPServerToggle runs the same Disable/Enable action as the terminal's
// /mcp panel, in the serving Runner's agent store. The user's YAML is never
// rewritten, so a project consent fingerprint cannot rotate, and the running
// session's servers and tool table do not move. A refusal (a required server,
// an unknown name) is reported with its own text.
func (s *Server) handleMCPServerToggle(w http.ResponseWriter, r *http.Request, disable bool) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	var in struct {
		Name      string `json:"name"`
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	runner := s.Runner
	if id := strings.TrimSpace(in.SessionID); id != "" {
		if resolved := s.runnerFor(r.Context(), id); resolved != nil {
			runner = resolved
		}
	}
	if runner == nil {
		http.Error(w, "no runner", http.StatusServiceUnavailable)
		return
	}
	reply, err := turn.SetMCPServerDisabled(runner.WorkspaceRoot, runner.MCPServers, runner.MCPDisabled, name, disable)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "reply": reply})
}
