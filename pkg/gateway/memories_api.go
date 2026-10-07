package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/memory"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

type memoriesSettingsRequest struct {
	FeatureEnabled   *bool  `json:"feature_enabled"`
	UseMemories      *bool  `json:"use_memories"`
	GenerateMemories *bool  `json:"generate_memories"`
	ThreadID         string `json:"thread_id"`
}

func (s *Server) handleMemoriesSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeMemoriesSettings(w, memory.SettingsFromConfig(s.liveCfg()))
	case http.MethodPost:
		if s == nil || s.Env == nil || strings.TrimSpace(s.Env.ConfigPath) == "" {
			http.Error(w, "memory settings unavailable", http.StatusServiceUnavailable)
			return
		}
		var req memoriesSettingsRequest
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			http.Error(w, "trailing JSON content", http.StatusBadRequest)
			return
		}
		if req.GenerateMemories != nil && strings.TrimSpace(req.ThreadID) != "" && (s.MemoryStore == nil || s.MemoryStore.DB == nil) {
			http.Error(w, "memory settings unavailable", http.StatusServiceUnavailable)
			return
		}
		patch := appcfg.MemoryPatch{
			FeatureEnabled:   req.FeatureEnabled,
			UseMemories:      req.UseMemories,
			GenerateMemories: req.GenerateMemories,
		}
		if err := appcfg.PatchMemory(s.Env.ConfigPath, patch); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := s.Env.ReloadConfig(); err != nil {
			http.Error(w, "settings saved but reload failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if req.GenerateMemories != nil && strings.TrimSpace(req.ThreadID) != "" && s.MemoryStore != nil {
			mode := memory.ThreadMemoryDisabled
			if *req.GenerateMemories {
				mode = memory.ThreadMemoryEnabled
			}
			if err := s.MemoryStore.SetThreadMemoryMode(r.Context(), req.ThreadID, mode); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		writeMemoriesSettings(w, memory.SettingsFromConfig(s.liveCfg()))
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}

func writeMemoriesSettings(w http.ResponseWriter, settings memory.Settings) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"settings": settings})
}

// handleMemoriesReset is the one endpoint every memory clear goes through
// (decision D10). The request body names the scope: {"scope": "session"} (the
// default an empty body keeps answering) clears the calling session's
// project, "all" wipes every scope of the calling primary agent, "global"
// clears the agent's cross-project memory, and "project" clears one
// registered project (project_id required). Store rows and files are cleared
// as a pair — either alone leaves the half-cleared state the other half
// remembers.
func (s *Server) handleMemoriesReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	if s == nil || s.MemoryStore == nil || s.MemoryStore.DB == nil || s.Runner == nil {
		http.Error(w, "memory reset unavailable", http.StatusServiceUnavailable)
		return
	}
	roots, err := memory.ResolveRootsForAgent(strings.TrimSpace(s.Runner.StateRoot()))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var body struct {
		Scope     string `json:"scope"`
		ProjectID string `json:"project_id"`
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(bytes.TrimSpace(raw)) > 0 {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	scope := strings.TrimSpace(body.Scope)
	if scope == "" {
		scope = "session"
	}
	clearScope := func(target memory.Scope) error {
		if err := s.MemoryStore.ResetScope(r.Context(), target); err != nil {
			return err
		}
		return memory.Clear(roots.Scope(target))
	}
	switch scope {
	case "session":
		key := strings.TrimSpace(s.Runner.ProjectKey)
		if key == "" {
			http.Error(w, `this session has no project to reset; use {"scope":"all"}`, http.StatusBadRequest)
			return
		}
		if err := clearScope(memory.Scope{Kind: memory.ScopeProject, Key: key}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	case "all":
		if err := s.MemoryStore.Reset(r.Context()); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := memory.Clear(roots.Scope(memory.GlobalScope())); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		scopes, err := roots.ListProjectScopes()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for _, projectScope := range scopes {
			if err := memory.Clear(roots.Scope(projectScope)); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
	case "global":
		if err := clearScope(memory.GlobalScope()); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	case "project":
		id := strings.TrimSpace(body.ProjectID)
		if id == "" {
			http.Error(w, "project_id required with scope=project", http.StatusBadRequest)
			return
		}
		store := s.projectStore()
		if store == nil {
			http.Error(w, "projects unavailable", http.StatusServiceUnavailable)
			return
		}
		project, err := store.Get(r.Context(), id)
		if err != nil {
			http.Error(w, "project not found", http.StatusNotFound)
			return
		}
		target, ok := memory.ProjectScope(strings.TrimSpace(project.ProjectKey))
		if !ok {
			http.Error(w, "this project has no independent memory scope", http.StatusBadRequest)
			return
		}
		if err := clearScope(target); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	default:
		http.Error(w, "scope must be session, all, global or project", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "scope": scope})
}

func (s *Server) HandleMemoriesSlash(ctx context.Context, sessionID, channel string, args []string) (string, bool) {
	_ = ctx
	_ = sessionID
	_ = channel
	if len(args) != 0 {
		return "memories: usage /memories", true
	}
	return "Open memory settings to configure memory use and generation.", true
}

// The memory-file management endpoints: the paginated listing, single-file
// read/write, and batch delete the agent and project memory pages are built
// on. Clearing stays on POST /api/memories/reset (decision D10) — these
// endpoints never delete a whole scope.

// The memory file core names, one fact with pkg/memory's instruction layer:
// these two are the files consolidation owns. They may be edited or cleared
// but never deleted one at a time.
var memoryCoreFiles = map[string]bool{
	"MEMORY.md":         true,
	"memory_summary.md": true,
}

const (
	memoryFileMaxBytes  = 2 << 20 // one memory file's read and write ceiling
	memoryListMaxPage   = 50
	memoryDeleteMaxPath = 100
)

type memoryFileRow struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
	Core      bool   `json:"core"`
}

// memoriesScopeTarget is one resolved memory scope: the root every file
// operation is confined to, plus the scope the reset endpoints name it by.
type memoriesScopeTarget struct {
	Root  memory.Root
	Scope memory.Scope
	Label string
}

// resolveMemoriesScope maps the scope query parameters onto one memory root
// of the active primary agent. scope=global is the agent's own cross-project
// memory; scope=project names a registered project, whose memory boundary is
// its project key — the same key the engine keys consolidation on.
func (s *Server) resolveMemoriesScope(w http.ResponseWriter, r *http.Request) (memoriesScopeTarget, bool) {
	if s == nil || s.Runner == nil {
		http.Error(w, "memory store unavailable", http.StatusServiceUnavailable)
		return memoriesScopeTarget{}, false
	}
	roots, err := memory.ResolveRootsForAgent(strings.TrimSpace(s.Runner.StateRoot()))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return memoriesScopeTarget{}, false
	}
	switch strings.TrimSpace(r.URL.Query().Get("scope")) {
	case "global":
		scope := memory.GlobalScope()
		return memoriesScopeTarget{Root: roots.Scope(scope), Scope: scope, Label: "global"}, true
	case "project":
		id := strings.TrimSpace(r.URL.Query().Get("project_id"))
		if id == "" {
			http.Error(w, "project_id required with scope=project", http.StatusBadRequest)
			return memoriesScopeTarget{}, false
		}
		store := s.projectStore()
		if store == nil {
			http.Error(w, "projects unavailable", http.StatusServiceUnavailable)
			return memoriesScopeTarget{}, false
		}
		project, err := store.Get(r.Context(), id)
		if err != nil {
			http.Error(w, "project not found", http.StatusNotFound)
			return memoriesScopeTarget{}, false
		}
		scope, ok := memory.ProjectScope(strings.TrimSpace(project.ProjectKey))
		if !ok {
			http.Error(w, "this project has no independent memory scope", http.StatusBadRequest)
			return memoriesScopeTarget{}, false
		}
		return memoriesScopeTarget{Root: roots.Scope(scope), Scope: scope, Label: "project"}, true
	default:
		http.Error(w, "scope must be global or project", http.StatusBadRequest)
		return memoriesScopeTarget{}, false
	}
}

// resolveMemoryFilePath pins one relative memory-file path inside its scope's
// root: cleaned, non-absolute, traversal-free, not a symlink, and resolved
// within the root.
func resolveMemoryFilePath(root memory.Root, rel string) (string, error) {
	rel = strings.TrimSpace(rel)
	if err := tool.ValidateArchiveRelPath(rel); err != nil {
		return "", err
	}
	target := filepath.Join(root.MemoryRoot, filepath.FromSlash(rel))
	resolved, err := tool.ResolveWithinRoots(target, []string{root.MemoryRoot})
	if err != nil {
		return "", err
	}
	if info, err := os.Lstat(resolved); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", tool.ErrPathNotAllowed
	}
	return resolved, nil
}

// collectMemoryFiles walks one memory root and returns every regular file,
// never following a symlink out of it.
func collectMemoryFiles(root memory.Root) ([]memoryFileRow, error) {
	out := []memoryFileRow{}
	err := filepath.WalkDir(root.MemoryRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if path == root.MemoryRoot {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		rel, relErr := filepath.Rel(root.MemoryRoot, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		updatedAt := info.ModTime().Unix()
		createdAt := updatedAt
		if birth, ok := memoryFileBirthTime(info); ok {
			createdAt = birth.Unix()
		}
		out = append(out, memoryFileRow{
			Path:      rel,
			SizeBytes: info.Size(),
			CreatedAt: createdAt,
			UpdatedAt: updatedAt,
			Core:      memoryCoreFiles[rel],
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// memoryFileMatches reports whether one file answers the listing's keyword:
// the keyword appears in the file's bytes or in its own name. This is a file
// manager's literal lookup, not the model's recall — the FTS recall path ranks
// lines against a relevance floor calibrated for real-scale stores, which a
// user's exact keyword must not be subject to (a small store scores every term
// above the floor and the search would answer nothing).
func memoryFileMatches(target memoriesScopeTarget, row memoryFileRow, q string) bool {
	lower := strings.ToLower(q)
	if strings.Contains(strings.ToLower(row.Path), lower) {
		return true
	}
	if row.SizeBytes > memoryFileMaxBytes {
		// Oversized content is not scanned for keywords; the name match
		// above still reaches it.
		return false
	}
	data, err := os.ReadFile(filepath.Join(target.Root.MemoryRoot, filepath.FromSlash(row.Path)))
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(string(data)), lower)
}

func (s *Server) handleMemoriesFilesList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	target, ok := s.resolveMemoriesScope(w, r)
	if !ok {
		return
	}
	query := r.URL.Query()
	page, err := strconv.Atoi(strings.TrimSpace(query.Get("page")))
	if err != nil || page < 1 {
		page = 1
	}
	pageSize, err := strconv.Atoi(strings.TrimSpace(query.Get("page_size")))
	if err != nil || pageSize < 1 {
		pageSize = 20
	}
	if pageSize > memoryListMaxPage {
		pageSize = memoryListMaxPage
	}
	sortKey := strings.TrimSpace(query.Get("sort"))
	if sortKey != "created" {
		sortKey = "updated"
	}
	sortDesc := true
	if strings.TrimSpace(query.Get("order")) == "asc" {
		sortDesc = false
	}

	files, err := collectMemoryFiles(target.Root)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if q := strings.TrimSpace(query.Get("q")); q != "" {
		filtered := files[:0]
		for _, file := range files {
			if memoryFileMatches(target, file, q) {
				filtered = append(filtered, file)
			}
		}
		files = filtered
	}
	sort.Slice(files, func(i, j int) bool {
		var a, b int64
		if sortKey == "created" {
			a, b = files[i].CreatedAt, files[j].CreatedAt
		} else {
			a, b = files[i].UpdatedAt, files[j].UpdatedAt
		}
		if a == b {
			return files[i].Path < files[j].Path
		}
		if sortDesc {
			return a > b
		}
		return a < b
	})
	total := len(files)
	start := (page - 1) * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"total":     total,
		"page":      page,
		"page_size": pageSize,
		"files":     files[start:end],
	})
}

func (s *Server) handleMemoriesFileRead(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	target, ok := s.resolveMemoriesScope(w, r)
	if !ok {
		return
	}
	abs, err := resolveMemoryFilePath(target.Root, r.URL.Query().Get("path"))
	if err != nil {
		http.Error(w, "path must stay inside the memory root", http.StatusBadRequest)
		return
	}
	info, err := os.Lstat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "file not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !info.Mode().IsRegular() {
		http.Error(w, "not a regular file", http.StatusBadRequest)
		return
	}
	if info.Size() > memoryFileMaxBytes {
		http.Error(w, "file exceeds the 2 MiB read limit", http.StatusRequestEntityTooLarge)
		return
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rel, _ := filepath.Rel(target.Root.MemoryRoot, abs)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"path":    filepath.ToSlash(rel),
		"content": string(data),
		"core":    isCoreMemoryFile(target.Root, info),
	})
}

// isCoreMemoryFile reports whether a file is one of its scope's core files.
// The answer is by file identity, not by how the request spelled the path:
// "./MEMORY.md", a case-folded name on a case-insensitive volume and a hard
// link all reach the very file consolidation owns.
func isCoreMemoryFile(root memory.Root, info os.FileInfo) bool {
	for name := range memoryCoreFiles {
		core, err := os.Lstat(filepath.Join(root.MemoryRoot, name))
		if err == nil && os.SameFile(info, core) {
			return true
		}
	}
	return false
}

// memoryEditableExtensions is the new-file whitelist: the memory root holds
// prose the model reads and the consolidation pipeline writes, not an
// arbitrary file library.
var memoryEditableExtensions = map[string]bool{
	".md":   true,
	".txt":  true,
	".json": true,
}

func (s *Server) handleMemoriesFileWrite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	target, ok := s.resolveMemoriesScope(w, r)
	if !ok {
		return
	}
	var body struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, memoryFileMaxBytes+(1<<16))).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(body.Content) > memoryFileMaxBytes {
		http.Error(w, "content exceeds the 2 MiB write limit", http.StatusRequestEntityTooLarge)
		return
	}
	rel := strings.TrimSpace(r.URL.Query().Get("path"))
	abs, err := resolveMemoryFilePath(target.Root, rel)
	if err != nil {
		http.Error(w, "path must stay inside the memory root", http.StatusBadRequest)
		return
	}
	if _, statErr := os.Lstat(abs); statErr != nil {
		if !os.IsNotExist(statErr) {
			http.Error(w, statErr.Error(), http.StatusInternalServerError)
			return
		}
		if !memoryEditableExtensions[strings.ToLower(filepath.Ext(abs))] {
			http.Error(w, "new memory files must be .md, .txt or .json", http.StatusBadRequest)
			return
		}
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := os.WriteFile(abs, []byte(body.Content), 0o644); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":   true,
		"path": filepath.ToSlash(rel),
	})
}

func (s *Server) handleMemoriesFilesDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	target, ok := s.resolveMemoriesScope(w, r)
	if !ok {
		return
	}
	var body struct {
		Paths []string `json:"paths"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(body.Paths) == 0 {
		http.Error(w, "paths required", http.StatusBadRequest)
		return
	}
	if len(body.Paths) > memoryDeleteMaxPath {
		http.Error(w, "at most 100 paths per delete", http.StatusBadRequest)
		return
	}
	type deleteResult struct {
		Path  string `json:"path"`
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	}
	results := make([]deleteResult, 0, len(body.Paths))
	deleted := 0
	for _, raw := range body.Paths {
		rel := strings.TrimSpace(raw)
		result := deleteResult{Path: rel, OK: false}
		abs, err := resolveMemoryFilePath(target.Root, rel)
		if err != nil {
			result.Error = "path must stay inside the memory root"
		} else if info, statErr := os.Lstat(abs); statErr != nil {
			result.Error = "file not found"
		} else if !info.Mode().IsRegular() {
			result.Error = "not a regular file"
		} else if isCoreMemoryFile(target.Root, info) {
			result.Error = "core memory files can be edited or cleared, not deleted"
		} else if err := os.Remove(abs); err != nil {
			result.Error = err.Error()
		} else {
			result.OK = true
			deleted++
		}
		results = append(results, result)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":      true,
		"deleted": deleted,
		"results": results,
	})
}

// osFileInfo keeps the platform helpers' signature independent of os.FileInfo
// so neither has to import os for it.
type osFileInfo = fs.FileInfo
