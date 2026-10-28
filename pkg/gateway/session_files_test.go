package gateway

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
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

// --- Deliverables (plan 146) ---

// artifactServerForTest opens a server whose sessions and artifacts live in
// one throwaway database.
func artifactServerForTest(t *testing.T) (*Server, *state.SessionStore) {
	t.Helper()
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(t.TempDir(), "state.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	sessions := state.NewSessionStore(db, "main")
	require.NoError(t, sessions.Ensure(context.Background(), "s1", "conversation"))
	return &Server{Home: t.TempDir(), Sessions: sessions}, sessions
}

// artifactRequest drives one artifact route through its handler with the path
// params the router would have captured.
func artifactRequest(t *testing.T, s *Server, method, sid, aid, suffix, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "/api/chat/sessions/"+sid+"/artifacts/"+aid+suffix+query, nil)
	req = req.WithContext(context.WithValue(req.Context(), ParamsKey, Params{{Key: "id", Value: sid}, {Key: "aid", Value: aid}}))
	rec := httptest.NewRecorder()
	switch suffix {
	case "":
		s.handleSessionArtifact(rec, req)
	case "/content":
		s.handleSessionArtifactContent(rec, req)
	case "/entries":
		s.handleSessionArtifactEntries(rec, req)
	case "/preview.pdf":
		s.handleSessionArtifactPreviewPDF(rec, req)
	default:
		t.Fatalf("unknown artifact suffix %q", suffix)
	}
	return rec
}

// addArtifactFile writes a file into a scratch project directory and
// registers it as this conversation's deliverable, classified the way the
// presenting tool classified it.
func addArtifactFile(t *testing.T, sessions *state.SessionStore, name, body string) state.Artifact {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, name)
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	st, err := os.Stat(path)
	require.NoError(t, err)
	kind, mimeType, preview := tool.ClassifyArtifact(name, nil)
	row, err := sessions.AddArtifact(context.Background(), state.Artifact{
		SessionID: "s1", Path: path, RelPath: name, Name: name, Title: name,
		MIME: mimeType, Kind: kind, Preview: preview, Size: st.Size(), MTimeNS: st.ModTime().UnixNano(),
	})
	require.NoError(t, err)
	return row
}

func TestArtifactListPagesWithTotal(t *testing.T) {
	s, sessions := artifactServerForTest(t)
	for _, name := range []string{"a.docx", "b.pptx", "c.pdf"} {
		addArtifactFile(t, sessions, name, "x")
	}
	req := httptest.NewRequest(http.MethodGet, "/api/chat/sessions/s1/artifacts?limit=2&page=1", nil)
	req = req.WithContext(context.WithValue(req.Context(), ParamsKey, Params{{Key: "id", Value: "s1"}}))
	rec := httptest.NewRecorder()
	s.handleSessionArtifacts(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var page struct {
		Items []map[string]any `json:"items"`
		Total int              `json:"total"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
	require.Len(t, page.Items, 2)
	require.Equal(t, 3, page.Total)
	require.Equal(t, "a.docx", page.Items[0]["name"])
	// Every row tells the client how to fetch it.
	require.Contains(t, page.Items[0]["download_url"], "/api/chat/sessions/s1/artifacts/")
	require.Contains(t, page.Items[0]["download_url"], "/content?disposition=attachment")
}

func TestArtifactContentDownloadsByDefault(t *testing.T) {
	s, sessions := artifactServerForTest(t)
	row := addArtifactFile(t, sessions, "报告.docx", "docx bytes")
	rec := artifactRequest(t, s, http.MethodGet, "s1", row.ID, "/content", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.True(t, strings.HasPrefix(rec.Header().Get("Content-Disposition"), "attachment"), rec.Header().Get("Content-Disposition"))
	require.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	require.Equal(t, "application/vnd.openxmlformats-officedocument.wordprocessingml.document", rec.Header().Get("Content-Type"))
	require.Equal(t, "docx bytes", rec.Body.String())
}

func TestArtifactContentInlineOnlyForSafeTypes(t *testing.T) {
	s, sessions := artifactServerForTest(t)
	html := addArtifactFile(t, sessions, "page.html", "<html><body>hi</body></html>")
	fakePNG := addArtifactFile(t, sessions, "fake.png", "<html><body>not an image</body></html>")
	md := addArtifactFile(t, sessions, "notes.md", "# notes")

	rec := artifactRequest(t, s, http.MethodGet, "s1", html.ID, "/content", "?disposition=inline")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.True(t, strings.HasPrefix(rec.Header().Get("Content-Disposition"), "inline"))
	require.Contains(t, rec.Header().Get("Content-Type"), "text/html")
	csp := rec.Header().Get("Content-Security-Policy")
	require.Contains(t, csp, "sandbox")
	require.NotContains(t, csp, "allow-same-origin")

	// Text wearing an image's extension never earns inline: the signature,
	// not the name, decides.
	rec = artifactRequest(t, s, http.MethodGet, "s1", fakePNG.ID, "/content", "?disposition=inline")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.True(t, strings.HasPrefix(rec.Header().Get("Content-Disposition"), "attachment"), rec.Header().Get("Content-Disposition"))

	// Markdown is data for the client to render, served as text.
	rec = artifactRequest(t, s, http.MethodGet, "s1", md.ID, "/content", "?disposition=inline")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.True(t, strings.HasPrefix(rec.Header().Get("Content-Disposition"), "inline"))
	require.Contains(t, rec.Header().Get("Content-Type"), "text/plain")
}

func TestArtifactContentServesRanges(t *testing.T) {
	s, sessions := artifactServerForTest(t)
	row := addArtifactFile(t, sessions, "clip.mp4", "0123456789")
	req := httptest.NewRequest(http.MethodGet, "/api/chat/sessions/s1/artifacts/"+row.ID+"/content", nil)
	req.Header.Set("Range", "bytes=0-3")
	req = req.WithContext(context.WithValue(req.Context(), ParamsKey, Params{{Key: "id", Value: "s1"}, {Key: "aid", Value: row.ID}}))
	rec := httptest.NewRecorder()
	s.handleSessionArtifactContent(rec, req)
	require.Equal(t, http.StatusPartialContent, rec.Code, rec.Body.String())
	require.Equal(t, "0123", rec.Body.String())
}

func TestArtifactOfAnotherSessionIs404(t *testing.T) {
	s, sessions := artifactServerForTest(t)
	require.NoError(t, state.NewSessionStore(sessions.DB(), "other").Ensure(context.Background(), "s-other", "other"))
	other, err := state.NewSessionStore(sessions.DB(), "other").AddArtifact(context.Background(), state.Artifact{
		SessionID: "s-other", Path: "/tmp/x.docx", Name: "x.docx", Kind: "document", MIME: "text/plain",
	})
	require.NoError(t, err)
	rec := artifactRequest(t, s, http.MethodGet, "s-other", other.ID, "", "")
	require.Equal(t, http.StatusNotFound, rec.Code)
	// An id learned from another conversation reads the same in this one.
	rec = artifactRequest(t, s, http.MethodGet, "s1", other.ID, "", "")
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Contains(t, rec.Body.String(), "artifact_not_found")
}

func TestArtifactMissingFileIs404WithCode(t *testing.T) {
	s, sessions := artifactServerForTest(t)
	row := addArtifactFile(t, sessions, "gone.docx", "bye")
	require.NoError(t, os.Remove(row.Path))
	rec := artifactRequest(t, s, http.MethodGet, "s1", row.ID, "/content", "")
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Contains(t, rec.Body.String(), "artifact_file_missing")
	// The row itself still answers, and says the file is gone.
	rec = artifactRequest(t, s, http.MethodGet, "s1", row.ID, "", "")
	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, true, body["missing"])
}

func TestArtifactEntriesListsAZip(t *testing.T) {
	s, sessions := artifactServerForTest(t)
	root := t.TempDir()
	zipPath := filepath.Join(root, "bundle.zip")
	zf, err := os.Create(zipPath)
	require.NoError(t, err)
	zw := zip.NewWriter(zf)
	hello, err := zw.Create("docs/hello.txt")
	require.NoError(t, err)
	_, err = hello.Write([]byte("hello"))
	require.NoError(t, err)
	world, err := zw.Create("docs/world.txt")
	require.NoError(t, err)
	_, err = world.Write([]byte("world"))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	require.NoError(t, zf.Close())
	st, err := os.Stat(zipPath)
	require.NoError(t, err)
	row, err := sessions.AddArtifact(context.Background(), state.Artifact{
		SessionID: "s1", Path: zipPath, RelPath: "bundle.zip", Name: "bundle.zip", Title: "bundle.zip",
		MIME: "application/zip", Kind: "archive", Preview: "archive", Size: st.Size(), MTimeNS: st.ModTime().UnixNano(),
	})
	require.NoError(t, err)
	rec := artifactRequest(t, s, http.MethodGet, "s1", row.ID, "/entries", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body struct {
		Entries []struct {
			Name string `json:"name"`
			Size int64  `json:"size"`
			Dir  bool   `json:"dir"`
		} `json:"entries"`
		Total     int  `json:"total"`
		Truncated bool `json:"truncated"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, 2, body.Total)
	require.False(t, body.Truncated)
	require.Equal(t, "docs/hello.txt", body.Entries[0].Name)
	require.Equal(t, int64(5), body.Entries[0].Size)
	// A non-archive never lists.
	docx := addArtifactFile(t, sessions, "a.docx", "x")
	rec = artifactRequest(t, s, http.MethodGet, "s1", docx.ID, "/entries", "")
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestArtifactPreviewWithoutLibreOfficeIs503(t *testing.T) {
	t.Setenv("FOREBRAIN_SOFFICE", "/nonexistent/soffice")
	t.Setenv("PATH", t.TempDir())
	s, sessions := artifactServerForTest(t)
	row := addArtifactFile(t, sessions, "deck.pptx", "pptx bytes")
	rec := artifactRequest(t, s, http.MethodGet, "s1", row.ID, "/preview.pdf", "")
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "libreoffice_unavailable")
}

func TestArtifactPreviewUsesTheCache(t *testing.T) {
	// A soffice that does not exist proves the cache served: any conversion
	// attempt would have failed.
	t.Setenv("FOREBRAIN_SOFFICE", "/nonexistent/soffice")
	s, sessions := artifactServerForTest(t)
	row := addArtifactFile(t, sessions, "deck.pptx", "pptx bytes")
	sum := sha256.Sum256([]byte("pptx bytes"))
	cached := filepath.Join(s.Home, "cache", "artifact-previews", hex.EncodeToString(sum[:])+".pdf")
	require.NoError(t, os.MkdirAll(filepath.Dir(cached), 0o755))
	require.NoError(t, os.WriteFile(cached, []byte("%PDF-1.4\ncached preview"), 0o644))
	rec := artifactRequest(t, s, http.MethodGet, "s1", row.ID, "/preview.pdf", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Header().Get("Content-Type"), "application/pdf")
	require.True(t, strings.HasPrefix(rec.Header().Get("Content-Disposition"), "inline"))
	require.True(t, strings.HasPrefix(rec.Body.String(), "%PDF-1.4"))
}

// TestArtifactPreviewConvertsAPptx is the real-machine acceptance of the
// LibreOffice conversion: a real presentation, converted through the handler
// (scratch profile, cache write, inline serve). It runs only where both
// halves exist — a soffice on PATH and a presentation to convert, named by
// FOREBRAIN_TEST_PPTX — and records how long the conversion took.
func TestArtifactPreviewConvertsAPptx(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	if _, err := exec.LookPath("soffice"); err != nil {
		t.Skip("no soffice on PATH")
	}
	source := strings.TrimSpace(os.Getenv("FOREBRAIN_TEST_PPTX"))
	if source == "" {
		t.Skip("no pptx fixture: set FOREBRAIN_TEST_PPTX to a local presentation")
	}
	t.Setenv("FOREBRAIN_SOFFICE", "")
	s, sessions := artifactServerForTest(t)
	st, err := os.Stat(source)
	require.NoError(t, err)
	row, err := sessions.AddArtifact(context.Background(), state.Artifact{
		SessionID: "s1", Path: source, RelPath: filepath.Base(source), Name: filepath.Base(source), Title: filepath.Base(source),
		MIME: "application/vnd.openxmlformats-officedocument.presentationml.presentation",
		Kind: "presentation", Preview: "office_pdf", Size: st.Size(), MTimeNS: st.ModTime().UnixNano(),
	})
	require.NoError(t, err)

	started := time.Now()
	rec := artifactRequest(t, s, http.MethodGet, "s1", row.ID, "/preview.pdf", "")
	elapsed := time.Since(started)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview.pdf = %d: %s (took %s)", rec.Code, rec.Body.String(), elapsed)
	}
	require.Contains(t, rec.Header().Get("Content-Type"), "application/pdf")
	require.True(t, strings.HasPrefix(rec.Body.String(), "%PDF-"), "converted bytes are not a PDF")
	t.Logf("converted %s (%d bytes) to a %d-byte PDF in %s", filepath.Base(source), st.Size(), rec.Body.Len(), elapsed)

	// The second read is the cache: it answers without a conversion.
	started = time.Now()
	rec = artifactRequest(t, s, http.MethodGet, "s1", row.ID, "/preview.pdf", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.True(t, strings.HasPrefix(rec.Body.String(), "%PDF-"))
	t.Logf("cache read answered in %s", time.Since(started))
}
