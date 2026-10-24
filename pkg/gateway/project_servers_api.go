package gateway

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
)

// projectServerChoice is one row of the session consent dialog's answer: the
// entry as the dialog showed it — an MCP server name or a language-server id,
// with the fingerprint it had — and whether it was left checked.
type projectServerChoice struct {
	Name        string `json:"name"`
	Fingerprint string `json:"fingerprint"`
	Allow       bool   `json:"allow"`
}

// handleProjectServersPending is the one-screen confirmation the web session
// view opens: the project's undecided MCP and language-server entries in two
// groups, plus the project's "always allow" switch. A dialog that gets two
// empty groups has nothing to ask and stays closed.
func (s *Server) handleProjectServersPending(w http.ResponseWriter, r *http.Request) {
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
	mcpRows := make([]map[string]string, 0)
	for _, item := range process.PendingProjectMCPConsents(workspace, launch) {
		mcpRows = append(mcpRows, map[string]string{
			"name": item.Name, "detail": item.Detail, "fingerprint": item.Fingerprint,
		})
	}
	lspRows := make([]map[string]string, 0)
	for _, item := range process.PendingProjectLSPConsents(workspace, launch) {
		// The dialog names a language server by its id; it is the same name
		// the consent answer has to send back.
		lspRows = append(lspRows, map[string]string{
			"name": item.ID, "detail": item.Detail, "fingerprint": item.Fingerprint,
		})
	}
	writeJSON(w, map[string]any{
		"mcp":        mcpRows,
		"lsp":        lspRows,
		"auto_allow": process.ProjectServersAutoAllowed(workspace, launch),
	})
}

// handleProjectServersConsent records the session dialog's answer: every entry
// it lists is decided — checked means allow, unchecked means deny — and
// always_allow turns the project's switch on as well. Each choice carries the
// fingerprint the dialog showed, so a project file that moved under the
// operator is refused whole with 409 and nothing recorded; the surface reloads
// and asks again about what is still pending.
func (s *Server) handleProjectServersConsent(w http.ResponseWriter, r *http.Request) {
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
		MCP []projectServerChoice `json:"mcp"`
		LSP []projectServerChoice `json:"lsp"`
		// AlwaysAllow turns the project's "always allow" switch on once both
		// kinds of the same answer were recorded.
		AlwaysAllow bool `json:"always_allow"`
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
	if err := process.DecideProjectServers(workspace, launch, process.ProjectServersDecision{
		MCP:         projectServerChoices(body.MCP),
		LSP:         projectServerChoices(body.LSP),
		AlwaysAllow: body.AlwaysAllow,
	}); err != nil {
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
	writeJSON(w, map[string]any{"ok": true})
}

// handleProjectServersAutoAllow turns the project's "always allow" switch: the
// project page's toolbar toggle. On, undecided entries load without asking;
// off, new and changed entries ask again.
func (s *Server) handleProjectServersAutoAllow(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
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
		On bool `json:"on"`
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
	if err := process.SetProjectServersAutoAllow(workspace, launch, body.On); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// The switch changes what a project's sessions would load, so the pooled
	// runners for it are rebuilt rather than reused.
	if s.Env != nil {
		if pool := s.Env.RunnerPool(); pool != nil {
			pool.RebindAgent(s.Runner.AgentName)
		}
	}
	writeJSON(w, map[string]any{"ok": true, "on": body.On})
}

// projectServerChoices re-words one kind's rows into the process package's own
// choice type. A row without a name carries no entry to bind a decision to and
// is dropped by the process layer, not here.
func projectServerChoices(rows []projectServerChoice) []process.ProjectServerChoice {
	choices := make([]process.ProjectServerChoice, 0, len(rows))
	for _, row := range rows {
		choices = append(choices, process.ProjectServerChoice{
			Name:        row.Name,
			Fingerprint: row.Fingerprint,
			Allow:       row.Allow,
		})
	}
	return choices
}
