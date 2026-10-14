package gateway

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// errFilesNotFound reports a file view whose root cannot be named: a
// conversation that is not this agent's, one that belongs to no project and is
// not a workshop task, a project that is not this agent's, or a request that
// named neither a session nor a project.
var errFilesNotFound = errors.New("file view not found")

// errFilesDirectoryMissing reports a file view whose root directory no longer
// exists on disk. It mirrors process.ErrSessionDirectoryMissing for the file
// endpoints, which answer the browser a 404 rather than a run's refusal.
type errFilesDirectoryMissing struct{ Dir string }

func (e errFilesDirectoryMissing) Error() string {
	return "file view directory missing: " + e.Dir
}

// filesRoot is the root of a conversation's file view: the root of the project
// the session belongs to, or — for an agent-level skill-workshop session, the
// one session without a project — the agent's skills directory. A conversation
// not started yet names its project instead of a session. Every file view
// (tree, snippet, @ mentions, @ images) and nothing else resolves its root
// here.
func (s *Server) filesRoot(ctx context.Context, sessionID, projectID string) (string, error) {
	if s == nil {
		return "", errFilesNotFound
	}
	sessionID = strings.TrimSpace(sessionID)
	projectID = strings.TrimSpace(projectID)
	switch {
	case sessionID != "":
		owned, err := s.sessionOwned(ctx, sessionID)
		if err != nil {
			return "", err
		}
		if !owned {
			return "", errFilesNotFound
		}
		// One project per session: the root is the project the session was
		// filed under at birth, never a directory some later request chose.
		project, ok, perr := s.projectStore().ProjectForSession(ctx, sessionID)
		if perr != nil {
			return "", perr
		}
		if ok {
			return existingFilesRoot(project.Root)
		}
		// No project: the only session that may still name a root is a
		// primary-agent workshop task, whose directory is the agent's own
		// skills. It is created here so a workshop conversation always has a
		// tree, the same way the pool builds its workshop runner.
		source, serr := s.sessionSource(ctx, sessionID)
		if serr != nil {
			return "", serr
		}
		if strings.EqualFold(source, state.SessionSourceWorkshop) {
			skills := filepath.Join(s.activeWorkspaceRoot(), "skills")
			if err := os.MkdirAll(skills, 0o755); err != nil {
				return "", err
			}
			return skills, nil
		}
		return "", errFilesNotFound
	case projectID != "":
		store := s.projectStore()
		if store == nil {
			return "", errFilesNotFound
		}
		project, err := store.Get(ctx, projectID)
		if err != nil {
			return "", errFilesNotFound
		}
		return existingFilesRoot(project.Root)
	default:
		return "", errFilesNotFound
	}
}

// existingFilesRoot returns a bound root that is still on disk, or the
// directory-missing refusal the file view answers a 404 with.
func existingFilesRoot(root string) (string, error) {
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		return "", errFilesDirectoryMissing{Dir: root}
	}
	return root, nil
}

// sessionSource reads one conversation's purpose (fb_sessions.source): empty
// for an ordinary conversation, "workshop" for a primary-agent workshop task.
// The project store owns the session table's project binding but exposes no
// single-row source read, so it is read here under the same agent scope the
// session-ownership check used.
func (s *Server) sessionSource(ctx context.Context, sessionID string) (string, error) {
	if s == nil || s.Sessions == nil || s.Sessions.DB() == nil {
		return "", nil
	}
	var source string
	err := s.Sessions.DB().QueryRowContext(ctx,
		`SELECT COALESCE(source, '') FROM fb_sessions WHERE id = ? AND agent_id = ?`,
		strings.TrimSpace(sessionID), s.Sessions.AgentID()).Scan(&source)
	if err != nil {
		return "", err
	}
	return source, nil
}

// resolveFilesPath pins one file-view-relative path to the conversation's
// files root, resolving symlinks, and rejects anything that lands outside it.
// The root is the project root (or the workshop skills directory): a link
// pointing elsewhere is not part of this view.
//
// A path already spelled absolutely is taken as the full path (and still
// containment-checked) instead of being joined under the root. The image raw
// endpoint needs this: a tool step records an image's ABSOLUTE path, so its
// pixel request arrives absolute while the file editor speaks relative paths.
// Relative paths — every other caller — resolve exactly as before.
func (s *Server) resolveFilesPath(ctx context.Context, sessionID, projectID, rel string) (root, full string, err error) {
	root, err = s.filesRoot(ctx, sessionID, projectID)
	if err != nil {
		return "", "", err
	}
	target := filepath.FromSlash(rel)
	if !filepath.IsAbs(target) {
		target = filepath.Join(root, target)
	}
	resolved, resolveErr := tool.ResolveWithinRoots(target, []string{root})
	if resolveErr != nil {
		if errors.Is(resolveErr, tool.ErrPathNotAllowed) {
			return root, "", tool.ErrPathNotAllowed
		}
		return root, "", resolveErr
	}
	return root, resolved, nil
}

// writeFilesRootError maps a filesRoot failure onto the status a file-view
// endpoint answers with: an unnamed root is a 404, a root whose directory is
// gone is a 404 that names it, and anything else is a server error.
func writeFilesRootError(w http.ResponseWriter, err error) {
	var missing errFilesDirectoryMissing
	switch {
	case errors.Is(err, errFilesNotFound):
		http.Error(w, "file view not found", http.StatusNotFound)
	case errors.As(err, &missing):
		http.Error(w, "目录已不存在："+missing.Dir, http.StatusNotFound)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
