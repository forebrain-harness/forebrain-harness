package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/skill"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// workspaceFileMaxBytes is the largest text file the workbench's editor reads
// or writes. The editor is for text; anything larger is not something to open
// in a browser textarea, so the read refuses it rather than truncating.
const workspaceFileMaxBytes = 1 << 20

// errSkillDirManaged is the refusal a whole-skill-directory delete or rename
// meets on the file endpoints: a skill directory carries versions and a toggle
// state, so it is the skills list's to remove, not a tree row's.
var errSkillDirManaged = errors.New("删除技能请在技能列表里操作")

// handleWorkspaceFileRead answers GET /api/workspace/file: the text of one
// file inside the conversation's file view. The root is whatever filesRoot
// resolves (the session's project, or a workshop task's skills directory), and
// the path must stay inside it.
func (s *Server) handleWorkspaceFileRead(w http.ResponseWriter, r *http.Request) {
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
	root, full, err := s.resolveFilesPath(r.Context(), q.Get("session_id"), q.Get("project_id"), rel)
	if err != nil {
		writeWorkspacePathError(w, err)
		return
	}
	_ = root
	st, statErr := os.Lstat(full)
	if statErr != nil {
		http.Error(w, statErr.Error(), http.StatusNotFound)
		return
	}
	if st.IsDir() {
		http.Error(w, "not a file", http.StatusBadRequest)
		return
	}
	if st.Size() > workspaceFileMaxBytes {
		http.Error(w, "file exceeds the 1 MiB read limit", http.StatusRequestEntityTooLarge)
		return
	}
	raw, readErr := os.ReadFile(full)
	if readErr != nil {
		http.Error(w, readErr.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{"path": rel, "content": string(raw)})
}

// workspaceFileRawMaxBytes bounds the image the raw endpoint serves: the
// largest a single read_file image part may be (llm.DefaultMaxImageBytes) times
// two, the headroom the encoder keeps between a source image and its scaled
// form. A larger file is refused rather than streamed — a thumbnail is a
// preview, and the file's own summary in the transcript is the way to read it.
const workspaceFileRawMaxBytes = 2 * llm.DefaultMaxImageBytes

// handleWorkspaceFileRaw answers GET /api/workspace/file/raw: the bytes of one
// image, for a surface thumbnail. It is the pixel channel a surface uses — no
// event or JSON response ever carries base64 — and it serves only images, so it
// never becomes a general file dump.
//
// It serves images from TWO legitimate roots, because an image the model read
// can live in either: the conversation's file view (the project root) and the
// primary agent workspace (where tool output such as an MCP screenshot is
// written). Both are resolved through the single containment primitive
// tool.ResolveWithinRoots; the root that applies to a relative path is still
// the file view (relative paths join to it as everywhere else), and an absolute
// path — which is what a tool step records — is matched against both.
func (s *Server) handleWorkspaceFileRaw(w http.ResponseWriter, r *http.Request) {
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
	full, err := s.resolveRawImagePath(r.Context(), q.Get("session_id"), q.Get("project_id"), rel)
	if err != nil {
		writeWorkspacePathError(w, err)
		return
	}
	st, statErr := os.Lstat(full)
	if statErr != nil {
		http.Error(w, statErr.Error(), http.StatusNotFound)
		return
	}
	if st.IsDir() {
		http.Error(w, "not a file", http.StatusBadRequest)
		return
	}
	if st.Size() > workspaceFileRawMaxBytes {
		http.Error(w, "image exceeds the raw preview limit; read the file's summary in the transcript instead", http.StatusRequestEntityTooLarge)
		return
	}
	raw, readErr := os.ReadFile(full)
	if readErr != nil {
		http.Error(w, readErr.Error(), http.StatusNotFound)
		return
	}
	mime := llm.ImageMIMEType(full, raw)
	switch mime {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
	default:
		http.Error(w, "not an image", http.StatusUnsupportedMediaType)
		return
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Cache-Control", "private, max-age=60")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

// resolveRawImagePath pins a thumbnail request to the image it names, which may
// live in the conversation's file view (the project root) or in the primary
// agent workspace (an MCP result or another tool artifact). Both roots are
// handed to tool.ResolveWithinRoots in ONE call, so containment is checked by
// the same primitive everywhere. A named session must be this agent's own
// before any root is named: a foreign session id is refused here rather than
// widening into the agent workspace.
//
// A relative path is joined to the file view root, exactly as the other file
// endpoints join it; a session with no file view at all (no project) has no
// root to join to and refuses a relative path, but still serves an absolute
// path — the shape a tool step records — from the agent workspace.
func (s *Server) resolveRawImagePath(ctx context.Context, sessionID, projectID, rel string) (string, error) {
	// A named session must be this agent's own before any root is resolved.
	// Every other file endpoint refuses a foreign session inside filesRoot;
	// this endpoint must refuse it too, not let the same errFilesNotFound
	// widen into the agent workspace the way a project-less session of our
	// own legitimately does.
	if sessionID = strings.TrimSpace(sessionID); sessionID != "" {
		owned, err := s.sessionOwned(ctx, sessionID)
		if err != nil {
			return "", err
		}
		if !owned {
			return "", errFilesNotFound
		}
	}
	var fileViewRoot string
	if root, err := s.filesRoot(ctx, sessionID, projectID); err == nil {
		fileViewRoot = strings.TrimSpace(root)
	} else if !errors.Is(err, errFilesNotFound) {
		return "", err
	}
	var roots []string
	for _, root := range []string{fileViewRoot, strings.TrimSpace(s.activeWorkspaceRoot())} {
		if root != "" {
			roots = append(roots, root)
		}
	}
	if len(roots) == 0 {
		return "", errFilesNotFound
	}
	target := filepath.FromSlash(rel)
	if !filepath.IsAbs(target) {
		if fileViewRoot == "" {
			return "", errFilesNotFound
		}
		target = filepath.Join(fileViewRoot, target)
	}
	return tool.ResolveWithinRoots(target, roots)
}

// handleWorkspaceFileWrite answers PUT /api/workspace/file: it writes a file
// (create=false, overwriting a save) or creates a new one (create=true, which
// refuses an existing path). A path inside a versioned skill's formal
// directory is redirected into that skill's draft first, so a tree edit never
// lands on the published copy. The parent directories of a new path are
// created under the root.
func (s *Server) handleWorkspaceFileWrite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	rel := strings.TrimSpace(q.Get("path"))
	create := parseBoolQuery(r, "create")
	var body struct {
		Path    string `json:"path"`
		Content string `json:"content"`
		Create  bool   `json:"create"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, workspaceFileMaxBytes+(1<<16))).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if trimmed := strings.TrimSpace(body.Path); trimmed != "" {
		rel = trimmed
	}
	if rel == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}
	create = create || body.Create
	if len(body.Content) > workspaceFileMaxBytes {
		http.Error(w, "content exceeds the 1 MiB write limit", http.StatusRequestEntityTooLarge)
		return
	}
	if err := workspaceReservedSegment(rel); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	root, full, err := s.resolveFilesPath(r.Context(), q.Get("session_id"), q.Get("project_id"), rel)
	if err != nil {
		writeWorkspacePathError(w, err)
		return
	}
	if filepath.Clean(full) == filepath.Clean(root) {
		http.Error(w, "path names the root", http.StatusBadRequest)
		return
	}
	target, redirectErr := s.skillRedirect(root, full)
	if redirectErr != nil {
		writeWorkspacePathError(w, redirectErr)
		return
	}
	if info, statErr := os.Lstat(target); statErr == nil {
		if create {
			http.Error(w, "file already exists", http.StatusConflict)
			return
		}
		if info.IsDir() {
			http.Error(w, "not a file", http.StatusBadRequest)
			return
		}
	} else if !os.IsNotExist(statErr) {
		http.Error(w, statErr.Error(), http.StatusInternalServerError)
		return
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := os.WriteFile(target, []byte(body.Content), 0o644); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "path": rel})
}

// handleWorkspaceRename answers POST /api/workspace/rename: it renames a file
// or a directory within the file view, refusing an existing target, the root
// itself, a move of a directory into itself, and any whole skill directory.
func (s *Server) handleWorkspaceRename(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	var body struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	from := strings.TrimSpace(body.From)
	to := strings.TrimSpace(body.To)
	if from == "" || to == "" {
		http.Error(w, "from and to are required", http.StatusBadRequest)
		return
	}
	if err := workspaceReservedSegment(from); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := workspaceReservedSegment(to); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	root, fromFull, err := s.resolveFilesPath(r.Context(), q.Get("session_id"), q.Get("project_id"), from)
	if err != nil {
		writeWorkspacePathError(w, err)
		return
	}
	if filepath.Clean(fromFull) == filepath.Clean(root) {
		http.Error(w, "path names the root", http.StatusBadRequest)
		return
	}
	_, toFull, err := s.resolveFilesPath(r.Context(), q.Get("session_id"), q.Get("project_id"), to)
	if err != nil {
		writeWorkspacePathError(w, err)
		return
	}
	if filepath.Clean(toFull) == filepath.Clean(root) {
		http.Error(w, "path names the root", http.StatusBadRequest)
		return
	}
	fromTarget, err := s.skillRedirect(root, fromFull)
	if err != nil {
		writeWorkspacePathError(w, err)
		return
	}
	toTarget, err := s.skillRedirect(root, toFull)
	if err != nil {
		writeWorkspacePathError(w, err)
		return
	}
	// Moving a directory inside itself would detach the subtree.
	if fromTarget != toTarget && pathWithin(fromTarget, toTarget) {
		http.Error(w, "cannot move a directory into itself", http.StatusBadRequest)
		return
	}
	if _, statErr := os.Lstat(fromTarget); statErr != nil {
		http.Error(w, statErr.Error(), http.StatusNotFound)
		return
	}
	if _, statErr := os.Lstat(toTarget); statErr == nil {
		http.Error(w, "target already exists", http.StatusConflict)
		return
	} else if !os.IsNotExist(statErr) {
		http.Error(w, statErr.Error(), http.StatusInternalServerError)
		return
	}
	if err := os.MkdirAll(filepath.Dir(toTarget), 0o755); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := os.Rename(fromTarget, toTarget); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "path": to})
}

// handleWorkspaceEntryDelete answers DELETE /api/workspace/entry: it removes
// one file, or one directory and everything under it, inside the file view. It
// refuses the root itself and any whole skill directory.
func (s *Server) handleWorkspaceEntryDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	rel := strings.TrimSpace(q.Get("path"))
	if rel == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}
	if err := workspaceReservedSegment(rel); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	root, full, err := s.resolveFilesPath(r.Context(), q.Get("session_id"), q.Get("project_id"), rel)
	if err != nil {
		writeWorkspacePathError(w, err)
		return
	}
	if filepath.Clean(full) == filepath.Clean(root) {
		http.Error(w, "path names the root", http.StatusBadRequest)
		return
	}
	target, redirectErr := s.skillRedirect(root, full)
	if redirectErr != nil {
		writeWorkspacePathError(w, redirectErr)
		return
	}
	if _, statErr := os.Lstat(target); statErr != nil {
		http.Error(w, statErr.Error(), http.StatusNotFound)
		return
	}
	if err := os.RemoveAll(target); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// workspaceReservedSegment refuses a path that names .git (or something inside
// it) or the version store .versions, before any root is resolved.
func workspaceReservedSegment(rel string) error {
	clean := strings.ReplaceAll(strings.TrimSpace(rel), "\\", "/")
	for _, seg := range strings.Split(clean, "/") {
		if seg == ".git" {
			return errors.New("path is inside .git")
		}
		if seg == ".versions" {
			return errors.New("路径指向版本库，不能修改")
		}
	}
	return nil
}

// skillRedirect sends a write aimed at a versioned skill's formal directory
// into that skill's draft, through the same resolver the skill file endpoints
// use. An ordinary path (nobody's skill) is returned unchanged; a refusal
// (the version store, or the skill directory itself) is returned as-is.
func (s *Server) skillRedirect(root, abs string) (string, error) {
	roots := skill.VersionedSkillRoots(s.Home, s.activeWorkspaceRoot(), root)
	target, handled, err := skill.ResolveDraftTarget(roots, abs)
	if err != nil {
		return "", err
	}
	if !handled {
		return abs, nil
	}
	return target.Target, nil
}

// pathWithin reports whether child is at or under parent (both cleaned
// absolute paths).
func pathWithin(parent, child string) bool {
	parent = filepath.Clean(parent)
	child = filepath.Clean(child)
	if parent == child {
		return true
	}
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// writeWorkspacePathError maps a write endpoint's path/root failure onto a
// status: a path outside the root is a 400, the skill-scope refusals are 400s
// too, and everything else goes through writeFilesRootError.
func writeWorkspacePathError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, tool.ErrPathNotAllowed):
		http.Error(w, "path is outside the workspace", http.StatusBadRequest)
	case errors.Is(err, skill.ErrSkillDirDeletion):
		http.Error(w, errSkillDirManaged.Error(), http.StatusBadRequest)
	case errors.Is(err, skill.ErrVersionStoreReadOnly):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		writeFilesRootError(w, err)
	}
}
