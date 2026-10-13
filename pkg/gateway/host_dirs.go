package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
)

// host_dirs.go is the web surface's window on the machine's own filesystem:
// listing the subdirectories of a directory and creating one. A browser cannot
// name an absolute path it was never told, so without these two the new-chat
// page could only ever bind a project the user typed a path for. Every route
// here sits behind the same control-plane middleware as the rest of /api, and
// none of them touches a file's contents — only directory names leave.
//
// The listing is deliberately directory-only: the folder picker chooses a
// project root, and a root is a directory. The forebrain home (and anything
// inside it) is listed but flagged blocked — the picker shows it as a locked
// row — because a project may never live there.

// hostDirEntry is one subdirectory in a listing. project carries the primary
// agent's project when this directory is exactly a project root; blocked marks
// a directory that may not be chosen (the forebrain home or inside it).
type hostDirEntry struct {
	Name    string     `json:"name"`
	Mtime   int64      `json:"mtime"`
	Project *hostDirPj `json:"project,omitempty"`
	Blocked bool       `json:"blocked"`
}

type hostDirPj struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// hostPlace is one fixed shortcut in the picker's sidebar.
type hostPlace struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

func (s *Server) handleHostDirs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleHostDirsList(w, r)
	case http.MethodPost:
		s.handleHostDirCreate(w, r)
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}

// homeForBlock reports the canonical forebrain home a directory may not be
// chosen inside, and whether candidate is that home or inside it.
func (s *Server) homeForBlock() (string, bool) {
	raw := strings.TrimSpace(s.Home)
	if raw == "" {
		return "", false
	}
	canonical, err := safety.CanonicalPath(raw)
	if err != nil {
		return "", false
	}
	return canonical, true
}

// dirIsBlocked reports whether dir is the forebrain home or inside it.
func dirIsBlocked(home string, hasHome bool, dir string) bool {
	if !hasHome {
		return false
	}
	if dir == home {
		return true
	}
	return strings.HasPrefix(dir, home+string(filepath.Separator))
}

func (s *Server) handleHostDirsList(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(r.URL.Query().Get("path"))
	if raw == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			http.Error(w, "cannot resolve home directory", http.StatusInternalServerError)
			return
		}
		raw = home
	}
	dir, err := safety.CanonicalPath(raw)
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		http.Error(w, "not a directory", http.StatusNotFound)
		return
	}

	home, hasHome := s.homeForBlock()
	entries, err := os.ReadDir(dir)
	if err != nil {
		http.Error(w, "cannot read directory", http.StatusInternalServerError)
		return
	}
	out := make([]hostDirEntry, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		child := filepath.Join(dir, entry.Name())
		blocked := dirIsBlocked(home, hasHome, child)
		// A hidden directory (".foo") is not shown — the only exception is the
		// blocked forebrain home itself, which the picker draws as a locked row
		// so the user can see why it cannot be chosen.
		if strings.HasPrefix(entry.Name(), ".") && !blocked {
			continue
		}
		item := hostDirEntry{Name: entry.Name(), Blocked: blocked}
		if info, statErr := entry.Info(); statErr == nil {
			item.Mtime = info.ModTime().Unix()
		}
		if !blocked {
			if store := s.projectStore(); store != nil {
				if p, ok, getErr := store.GetByRoot(r.Context(), child); getErr == nil && ok {
					item.Project = &hostDirPj{ID: p.ID, Name: p.Name}
				}
			}
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})

	parent := filepath.Dir(dir)
	if parent == dir {
		parent = ""
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"path":      dir,
		"parent":    parent,
		"ancestors": hostAncestors(dir),
		"places":    hostPlaces(),
		"entries":   out,
	})
}

// hostAncestors is the directory chain from dir up to the filesystem root, the
// list the picker's "current location" dropdown offers.
func hostAncestors(dir string) []string {
	out := []string{}
	for cur := dir; ; {
		out = append(out, cur)
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	return out
}

// hostPlaces is the picker's fixed sidebar: home, Desktop, Documents and
// Downloads, each only when it exists.
func hostPlaces() []hostPlace {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return []hostPlace{}
	}
	candidates := []hostPlace{
		{Name: "主目录", Path: home},
		{Name: "桌面", Path: filepath.Join(home, "Desktop")},
		{Name: "文稿", Path: filepath.Join(home, "Documents")},
		{Name: "下载", Path: filepath.Join(home, "Downloads")},
	}
	out := make([]hostPlace, 0, len(candidates))
	for _, place := range candidates {
		if info, statErr := os.Stat(place.Path); statErr == nil && info.IsDir() {
			out = append(out, place)
		}
	}
	return out
}

func (s *Server) handleHostDirCreate(w http.ResponseWriter, r *http.Request) {
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
	parent, err := safety.CanonicalPath(body.Parent)
	if err != nil {
		http.Error(w, "invalid parent path", http.StatusBadRequest)
		return
	}
	if info, statErr := os.Stat(parent); statErr != nil || !info.IsDir() {
		http.Error(w, "parent is not a directory", http.StatusNotFound)
		return
	}
	if home, hasHome := s.homeForBlock(); dirIsBlocked(home, hasHome, parent) {
		http.Error(w, "cannot create inside the forebrain home directory", http.StatusBadRequest)
		return
	}
	target := filepath.Join(parent, name)
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
	created, err := safety.CanonicalPath(target)
	if err != nil {
		created = target
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"path": created})
}

// trustLevelString spells a trust level for the wire: the store's LevelUnknown
// is the empty string, which the picker reads as "not judged yet", so it is
// named explicitly rather than left blank.
func trustLevelString(level safety.Level) string {
	switch level {
	case safety.LevelTrusted:
		return "trusted"
	case safety.LevelUntrusted:
		return "untrusted"
	default:
		return "unknown"
	}
}

// handleHostTrust reads and writes one directory's persisted trust decision —
// the same record the terminal's launch prompt writes and the project gates
// read, so a folder trusted on the web is trusted in the terminal too.
func (s *Server) handleHostTrust(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		raw := strings.TrimSpace(r.URL.Query().Get("path"))
		if raw == "" {
			http.Error(w, "path is required", http.StatusBadRequest)
			return
		}
		level, err := safety.TrustLevel(s.Home, safety.Project{Root: raw})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"level": trustLevelString(level)})
	case http.MethodPost:
		var body struct {
			Path    string `json:"path"`
			Trusted bool   `json:"trusted"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
			http.Error(w, "json", http.StatusBadRequest)
			return
		}
		path := strings.TrimSpace(body.Path)
		if path == "" {
			http.Error(w, "path is required", http.StatusBadRequest)
			return
		}
		var err error
		if body.Trusted {
			err = safety.MarkTrusted(s.Home, safety.Project{Root: path})
		} else {
			err = safety.MarkUntrusted(s.Home, safety.Project{Root: path})
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		level, levelErr := safety.TrustLevel(s.Home, safety.Project{Root: path})
		if levelErr != nil {
			level = safety.LevelUnknown
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "level": trustLevelString(level)})
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}

// projectTrustLevel is the trust decision for a project's root, defaulting to
// unknown when the home is unset or the state cannot be read.
func projectTrustLevel(home, root string) string {
	level, err := safety.TrustLevel(home, safety.Project{Root: root})
	if err != nil {
		return "unknown"
	}
	return trustLevelString(level)
}

// hostDirExists reports whether a stored project root is still a directory.
func hostDirExists(root string) bool {
	info, err := os.Stat(strings.TrimSpace(root))
	return err == nil && info.IsDir()
}
