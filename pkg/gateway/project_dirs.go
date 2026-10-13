package gateway

// project_dirs.go is the web "save to" dialog's window on a project's own
// directory tree: listing the subdirectories of one directory inside a project
// and making a new one. It is deliberately directory-only — the dialog chooses
// where an upload lands, and that is a directory — and every path it is given
// is pinned to the project root with the same containment check the file view
// uses, so a "..'" or a symlink cannot lead outside it.

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// errProjectRequired reports an upload for a conversation that belongs to no
// project (a primary-agent workshop task): there is no root to save into.
var errProjectRequired = errors.New("project_required")

// errUploadDirOutside reports a directory that escapes the project root: an
// absolute path, a ".." segment, or a path that resolves outside the root
// through a symlink.
var errUploadDirOutside = errors.New("upload directory is outside the project")

// errUploadDirMissing reports a directory that is named but not there.
var errUploadDirMissing = errors.New("upload directory does not exist")

// projectDir resolves dir — a '/'-separated path relative to root, where "" is
// the root itself — to an absolute directory pinned inside root. It refuses an
// absolute path, any ".." segment, and anything that resolves outside root
// (a symlink escape); the containment check is tool.ResolveWithinRoots, the
// same guard the file view uses. It returns the cleaned '/' spelling of the
// relative directory and its absolute path. The directory's existence is the
// caller's to check.
func projectDir(root, dir string) (rel string, abs string, err error) {
	root = strings.TrimSpace(root)
	raw := strings.TrimSpace(dir)
	if strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "\\") || filepath.IsAbs(raw) {
		return "", "", errUploadDirOutside
	}
	if len(raw) >= 2 && raw[1] == ':' {
		return "", "", errUploadDirOutside
	}
	rel = strings.Trim(strings.ReplaceAll(raw, "\\", "/"), "/")
	for _, seg := range strings.Split(rel, "/") {
		if seg == ".." {
			return "", "", errUploadDirOutside
		}
	}
	joined := filepath.Join(root, filepath.FromSlash(rel))
	abs, resolveErr := tool.ResolveWithinRoots(joined, []string{root})
	if resolveErr != nil {
		if errors.Is(resolveErr, tool.ErrPathNotAllowed) {
			return "", "", errUploadDirOutside
		}
		return "", "", resolveErr
	}
	return rel, abs, nil
}

// dirListEntry is one subdirectory the dialog can descend into. HasChildren
// says whether drawing it would find anything, so the tree shows a chevron
// only where expanding it is not an empty gesture.
type dirListEntry struct {
	Name        string `json:"name"`
	HasChildren bool   `json:"has_children"`
}

// handleProjectDirs answers GET and POST /api/projects/:id/dirs: a listing of
// one directory's subdirectories inside the project, or a new directory made
// in the parent the request names. Every path is relative to the project root.
func (s *Server) handleProjectDirs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleProjectDirsList(w, r)
	case http.MethodPost:
		s.handleProjectDirCreate(w, r)
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}

// projectRootForDirs resolves the route's project and its root, writing the
// refusal itself: a missing project is a 404, a root that is gone a 404 that
// names it.
func (s *Server) projectRootForDirs(w http.ResponseWriter, r *http.Request) (root string, ok bool) {
	store := s.projectStore()
	if store == nil {
		http.Error(w, "projects unavailable", http.StatusServiceUnavailable)
		return "", false
	}
	id := ParamsFromContext(r.Context()).ByName("id")
	project, err := store.Get(r.Context(), id)
	if err != nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return "", false
	}
	root, err = existingFilesRoot(project.Root)
	if err != nil {
		writeFilesRootError(w, err)
		return "", false
	}
	return root, true
}

func (s *Server) handleProjectDirsList(w http.ResponseWriter, r *http.Request) {
	root, ok := s.projectRootForDirs(w, r)
	if !ok {
		return
	}
	rel, abs, err := projectDir(root, r.URL.Query().Get("path"))
	if err != nil {
		writeProjectDirError(w, err, http.StatusNotFound)
		return
	}
	st, statErr := os.Stat(abs)
	if statErr != nil || !st.IsDir() {
		writeProjectDirError(w, errUploadDirMissing, http.StatusNotFound)
		return
	}
	children, readErr := os.ReadDir(abs)
	if readErr != nil {
		http.Error(w, "cannot read directory", http.StatusInternalServerError)
		return
	}
	entries := make([]dirListEntry, 0, len(children))
	for _, child := range children {
		if !child.IsDir() {
			continue
		}
		name := child.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		item := dirListEntry{Name: name}
		if grand, gerr := os.ReadDir(filepath.Join(abs, name)); gerr == nil {
			for _, g := range grand {
				if g.IsDir() {
					item.HasChildren = true
					break
				}
			}
		}
		entries = append(entries, item)
	}
	sort.Slice(entries, func(i, j int) bool {
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	writeJSON(w, map[string]any{"path": rel, "entries": entries})
}

func (s *Server) handleProjectDirCreate(w http.ResponseWriter, r *http.Request) {
	root, ok := s.projectRootForDirs(w, r)
	if !ok {
		return
	}
	var body struct {
		Parent string `json:"parent"`
		Name   string `json:"name"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		http.Error(w, "json", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
		http.Error(w, "invalid folder name", http.StatusBadRequest)
		return
	}
	rel, abs, err := projectDir(root, body.Parent)
	if err != nil {
		writeProjectDirError(w, err, http.StatusBadRequest)
		return
	}
	if st, statErr := os.Stat(abs); statErr != nil || !st.IsDir() {
		http.Error(w, "parent is not a directory", http.StatusNotFound)
		return
	}
	target := filepath.Join(abs, name)
	if err := os.Mkdir(target, 0o755); err != nil {
		if os.IsExist(err) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "exists"})
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	created := name
	if rel != "" {
		created = rel + "/" + name
	}
	writeJSON(w, map[string]any{"path": created})
}

// writeProjectDirError maps a directory-resolution failure onto a status: an
// escaped path is a 400, a missing one the status the caller chose, and
// anything else is passed through as a 400.
func writeProjectDirError(w http.ResponseWriter, err error, missingStatus int) {
	switch {
	case errors.Is(err, errUploadDirOutside):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, errUploadDirMissing):
		http.Error(w, err.Error(), missingStatus)
	default:
		http.Error(w, err.Error(), http.StatusBadRequest)
	}
}
