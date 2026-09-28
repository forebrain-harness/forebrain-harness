package gateway

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

// mentionRoot is the workspace the web composer picks files from.
func (s *Server) mentionRoot() string {
	if s == nil {
		return ""
	}
	root := filepath.Join(strings.TrimSpace(s.Home), "workspace")
	if active, err := s.activePrimarySummary(); err == nil && strings.TrimSpace(active.WorkspaceRoot) != "" {
		root = active.WorkspaceRoot
	}
	return root
}

type mentionCandidatePayload struct {
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
}

// handleMentionSearch answers the web composer's @ typeahead from the same
// engine the terminal composer uses, so both surfaces rank and cap candidates
// identically instead of each growing its own file search.
func (s *Server) handleMentionSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	query := r.URL.Query().Get("q")
	root := s.mentionRoot()
	resolver := turn.WorkspaceResolver{Root: root}
	records := make([]mentionCandidatePayload, 0)
	for _, cand := range turn.Search(root, query) {
		// The picker is workspace-scoped on this surface: a query like "~/" or
		// "../" would otherwise walk outside it. Report the resolver's own
		// relative form so a traversal that lands back inside the workspace is
		// named canonically rather than as "../workspace/...".
		_, rel, ok := resolver.Resolve(cand.Path)
		if !ok || rel == "." {
			continue
		}
		if cand.IsDir {
			rel += "/"
		}
		records = append(records, mentionCandidatePayload{Path: rel, IsDir: cand.IsDir})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"query":   strings.TrimSpace(query),
		"records": records,
	})
}

type mentionAcceptRequest struct {
	Draft      string `json:"draft"`
	TokenStart int    `json:"token_start"`
	TokenEnd   int    `json:"token_end"`
	Path       string `json:"path"`
	IsDir      bool   `json:"is_dir"`
}

type mentionAcceptResponse struct {
	Draft  string `json:"draft"`
	Cursor int    `json:"cursor"`
	// ImagePath is workspace-relative, both to keep server paths off the wire
	// and because the turn submission re-resolves it against the workspace.
	ImagePath string `json:"image_path,omitempty"`
	KeepOpen  bool   `json:"keep_open"`
}

// handleMentionAccept applies a picked candidate to the draft server-side, so
// the web composer holds no mention semantics of its own: which selections
// become a bare path, which are attached as images, and where the cursor lands
// are all decided by the shared engine.
func (s *Server) handleMentionAccept(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	var req mentionAcceptRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	root := s.mentionRoot()
	cand := turn.Candidate{Path: strings.TrimSpace(req.Path), IsDir: req.IsDir}
	if cand.Path == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}
	if _, _, ok := (turn.WorkspaceResolver{Root: root}).Resolve(cand.Path); !ok {
		http.Error(w, "path outside workspace", http.StatusBadRequest)
		return
	}
	acc, ok := turn.Accept(req.Draft, req.TokenStart, req.TokenEnd, cand, root)
	if !ok {
		http.Error(w, "token out of range", http.StatusBadRequest)
		return
	}
	resp := mentionAcceptResponse{Draft: acc.Draft, Cursor: acc.Cursor, KeepOpen: acc.KeepOpen}
	if acc.ImagePath != "" {
		if rel, err := filepath.Rel(root, acc.ImagePath); err == nil {
			resp.ImagePath = filepath.ToSlash(rel)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
