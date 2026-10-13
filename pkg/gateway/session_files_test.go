package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/stretchr/testify/require"
)

// TestFilesRootFollowsTheSessionsProject pins the file view's root: a
// conversation's tree, snippet and @ picker all resolve against the project
// the session belongs to (or, for a not-yet-started conversation, the project
// it names), never the agent's own workspace. The one project-less session
// that still has a view is a workshop task, rooted at the agent's skills.
func TestFilesRootFollowsTheSessionsProject(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()

	// W: the agent's own workspace, distinct from every project.
	workspace := filepath.Join(home, "workspace")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "skills", "demo"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "memories"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "SOUL.md"), []byte("soul"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "skills", "demo", "SKILL.md"), []byte("skill"), 0o644))

	// A: project P's root, holding a file and a directory.
	projectRoot := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(projectRoot, "pkg"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(projectRoot, "a.txt"), []byte("a"), 0o644))

	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	projects := state.NewProjectStore(db, "main")
	sessions := state.NewSessionStore(db, "main")
	project, err := projects.Create(ctx, state.CreateProjectInput{Name: "P", Root: projectRoot})
	require.NoError(t, err)

	// S1: an ordinary conversation filed under P.
	require.NoError(t, sessions.Ensure(ctx, "S1", "S1"))
	require.NoError(t, projects.BindSession(ctx, "S1", project.ID))
	// S2: the one project-less session that still names a root — a workshop.
	require.NoError(t, sessions.EnsureAt(ctx, "S2", "S2", state.SessionBirth{Source: state.SessionSourceWorkshop}))
	// S3: a project-less, non-workshop session has no root at all.
	require.NoError(t, sessions.Ensure(ctx, "S3", "S3"))
	// S4: another agent's conversation is not this agent's file view.
	require.NoError(t, state.NewSessionStore(db, "other").Ensure(ctx, "S4", "S4"))

	s := &Server{Home: home, Sessions: sessions, Projects: projects}

	tree := func(query string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.handleWorkspaceTree(rec, httptest.NewRequest(http.MethodGet, "/api/workspace/tree"+query, nil))
		return rec
	}
	names := func(rec *httptest.ResponseRecorder) []string {
		var body struct {
			Records []struct {
				Name string `json:"name"`
			} `json:"records"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		out := make([]string, 0, len(body.Records))
		for _, r := range body.Records {
			out = append(out, r.Name)
		}
		return out
	}

	// The session's project root, not the agent workspace.
	rec := tree("?session_id=S1")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.ElementsMatch(t, []string{"a.txt", "pkg"}, names(rec))
	require.NotContains(t, names(rec), "SOUL.md")
	require.NotContains(t, names(rec), "memories")

	// A traversal out of the root is still refused.
	require.Equal(t, http.StatusBadRequest, tree("?session_id=S1&path=../").Code)

	// A conversation not started yet names its project directly.
	rec = tree("?project_id=" + project.ID)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.ElementsMatch(t, []string{"a.txt", "pkg"}, names(rec))

	// A symlink inside the root that points outside it is not part of the
	// view: the tree does not list it and a request for it is refused.
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("s"), 0o644))
	require.NoError(t, os.Symlink(outside, filepath.Join(projectRoot, "link")))
	rec = tree("?session_id=S1")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotContains(t, names(rec), "link")
	require.Equal(t, http.StatusBadRequest, tree("?session_id=S1&path=link/secret.txt").Code)

	// The workshop session's root is the agent's skills directory.
	rec = tree("?session_id=S2")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, []string{"demo"}, names(rec))

	// Every root kind refuses a traversal out of itself: the session's project
	// root, a project named directly, and the workshop skills directory.
	require.Equal(t, http.StatusBadRequest, tree("?project_id="+project.ID+"&path=../").Code)
	require.Equal(t, http.StatusBadRequest, tree("?session_id=S2&path=../").Code)

	// A project-less, non-workshop session, no scope at all, and another
	// agent's conversation all resolve to no root.
	require.Equal(t, http.StatusNotFound, tree("?session_id=S3").Code)
	require.Equal(t, http.StatusNotFound, tree("").Code)
	require.Equal(t, http.StatusNotFound, tree("?session_id=S4").Code)

	// The @ picker reads the same root.
	rec = httptest.NewRecorder()
	s.handleMentionSearch(rec, httptest.NewRequest(http.MethodGet, "/api/workspace/mentions?session_id=S1&q=a.t", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var mentions struct {
		Records []struct {
			Path string `json:"path"`
		} `json:"records"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &mentions))
	paths := make([]string, 0, len(mentions.Records))
	for _, r := range mentions.Records {
		paths = append(paths, r.Path)
	}
	require.Contains(t, paths, "a.txt")

	// A root whose directory is gone answers 404 with the sentence the UI shows.
	require.NoError(t, os.RemoveAll(projectRoot))
	rec = tree("?session_id=S1")
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Contains(t, rec.Body.String(), "目录已不存在")
}
