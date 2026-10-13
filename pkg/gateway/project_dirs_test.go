package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/stretchr/testify/require"
)

// projectDirServer is a gateway with one project bound to a fresh scratch root
// and one conversation filed under it — the shape every "save to" endpoint
// serves. It returns the server, the project id and the root.
func projectDirServer(t *testing.T) (*Server, string, string) {
	t.Helper()
	ctx := context.Background()
	db, err := state.Open(ctx, filepath.Join(t.TempDir(), "state.db"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	mustGatewaySession(t, db, "s-1")
	root := t.TempDir()
	projects := state.NewProjectStore(db, "main")
	project, err := projects.Create(ctx, state.CreateProjectInput{Name: "proj", Root: root})
	require.NoError(t, err)
	require.NoError(t, projects.BindSession(ctx, "s-1", project.ID))
	s := &Server{
		Files:    &state.FileStore{DB: db, Home: t.TempDir(), Cfg: state.LoadConfigFromEnv()},
		Sessions: state.NewSessionStore(db, "main"),
		Projects: projects,
	}
	return s, project.ID, root
}

// uploadFile POSTs one file through handleFiles with an optional project dir.
func uploadFile(t *testing.T, s *Server, sessionID, dir, name string, content []byte) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	form := multipart.NewWriter(&buf)
	require.NoError(t, form.WriteField("session_id", sessionID))
	if dir != "" {
		require.NoError(t, form.WriteField("dir", dir))
	}
	part, err := form.CreateFormFile("file", name)
	require.NoError(t, err)
	_, err = part.Write(content)
	require.NoError(t, err)
	require.NoError(t, form.Close())
	req := httptest.NewRequest(http.MethodPost, "/api/files", &buf)
	req.Header.Set("Content-Type", form.FormDataContentType())
	rec := httptest.NewRecorder()
	s.handleFiles(rec, req)
	return rec
}

// projectDirsRequest drives handleProjectDirs for projectID with the route
// param set, marshalling a JSON body when one is given.
func projectDirsRequest(t *testing.T, s *Server, projectID, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, target, reader)
	req = req.WithContext(context.WithValue(req.Context(), ParamsKey, Params{{Key: "id", Value: projectID}}))
	rec := httptest.NewRecorder()
	s.handleProjectDirs(rec, req)
	return rec
}

func fileIDOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var out struct {
		FileID string `json:"file_id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.NotEmpty(t, out.FileID)
	return out.FileID
}

// TestUploadSavesIntoProjectDir pins the happy path: a file uploaded with dir
// lands in that directory inside the project, its row records the relative
// path, the project remembers the directory, and a name already taken gains a
// " (n)" suffix rather than overwriting.
func TestUploadSavesIntoProjectDir(t *testing.T) {
	s, projectID, root := projectDirServer(t)
	ctx := context.Background()
	require.NoError(t, os.Mkdir(filepath.Join(root, "docs"), 0o755))

	rec := uploadFile(t, s, "s-1", "docs", "x.png", []byte("png"))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	f, err := s.Files.Get(ctx, "main", fileIDOf(t, rec))
	require.NoError(t, err)
	require.Equal(t, "docs/x.png", f.ProjectPath)
	require.Empty(t, f.StorageKey)
	require.FileExists(t, filepath.Join(root, "docs", "x.png"))

	project, err := s.Projects.Get(ctx, projectID)
	require.NoError(t, err)
	require.Equal(t, "docs", project.LastAttachDir)

	// The same name again does not overwrite: it takes the smallest free suffix.
	require.Equal(t, http.StatusCreated, uploadFile(t, s, "s-1", "docs", "x.png", []byte("png2")).Code)
	require.FileExists(t, filepath.Join(root, "docs", "x (1).png"))
	require.Equal(t, http.StatusCreated, uploadFile(t, s, "s-1", "docs", "x.png", []byte("png3")).Code)
	require.FileExists(t, filepath.Join(root, "docs", "x (2).png"))

	// An empty dir is the project root itself.
	rootRec := uploadFile(t, s, "s-1", "", "at-root.txt", []byte("r"))
	require.Equal(t, http.StatusCreated, rootRec.Code, rootRec.Body.String())
	top, err := s.Files.Get(ctx, "main", fileIDOf(t, rootRec))
	require.NoError(t, err)
	require.Equal(t, "at-root.txt", top.ProjectPath)
	project, err = s.Projects.Get(ctx, projectID)
	require.NoError(t, err)
	require.Equal(t, "", project.LastAttachDir)
}

// TestUploadRefusesMissingDir pins that an upload never makes a directory: a
// directory that is not there is a refusal, not an implicit MkdirAll.
func TestUploadRefusesMissingDir(t *testing.T) {
	s, _, _ := projectDirServer(t)
	rec := uploadFile(t, s, "s-1", "nope", "x.png", []byte("x"))
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

// TestUploadRequiresAProject pins that a conversation with no project — a
// primary-agent workshop task — cannot save a file, in the same
// "project_required" shape the session-create endpoint answers with.
func TestUploadRequiresAProject(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(ctx, filepath.Join(t.TempDir(), "state.db"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	mustGatewaySession(t, db, "s-np")
	s := &Server{
		Files:    &state.FileStore{DB: db, Home: t.TempDir(), Cfg: state.LoadConfigFromEnv()},
		Sessions: state.NewSessionStore(db, "main"),
		Projects: state.NewProjectStore(db, "main"),
	}
	rec := uploadFile(t, s, "s-np", "", "x.png", []byte("x"))
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "project_required")
}

// TestUploadRefusesDirOutsideProject is the path-refusal test for the upload
// endpoint: a ".." segment, an absolute path and a symlink that leads out of
// the project are all refused, and nothing is written.
func TestUploadRefusesDirOutsideProject(t *testing.T) {
	s, _, root := projectDirServer(t)
	outside := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(outside, "escape"), 0o755))
	require.NoError(t, os.Symlink(filepath.Join(outside, "escape"), filepath.Join(root, "link")))

	for _, dir := range []string{"../x", "/etc", "..", "a/../..", "link"} {
		rec := uploadFile(t, s, "s-1", dir, "x.png", []byte("x"))
		require.Equal(t, http.StatusBadRequest, rec.Code, "dir=%q must be refused: %s", dir, rec.Body.String())
	}
	require.NoFileExists(t, filepath.Join(outside, "escape", "x.png"))
}

// TestProjectDirsListRefusesOutsideAndSymlink is the path-refusal test for the
// directory listing: an escaping path is a 400, never a window onto something
// outside the project.
func TestProjectDirsListRefusesOutsideAndSymlink(t *testing.T) {
	s, projectID, root := projectDirServer(t)
	outside := t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "link")))

	for _, p := range []string{"../x", "/etc", "link"} {
		rec := projectDirsRequest(t, s, projectID, http.MethodGet,
			"/api/projects/p/dirs?path="+url.QueryEscape(p), nil)
		require.Equal(t, http.StatusBadRequest, rec.Code, "path=%q: %s", p, rec.Body.String())
	}
}

// TestProjectDirCreateRefusesOutsideAndSymlink is the path-refusal test for
// the create endpoint: it never makes a directory outside the project.
func TestProjectDirCreateRefusesOutsideAndSymlink(t *testing.T) {
	s, projectID, root := projectDirServer(t)
	outside := t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "link")))

	for _, parent := range []string{"../x", "/etc", "link"} {
		rec := projectDirsRequest(t, s, projectID, http.MethodPost, "/api/projects/p/dirs",
			map[string]any{"parent": parent, "name": "sub"})
		require.Equal(t, http.StatusBadRequest, rec.Code, "parent=%q: %s", parent, rec.Body.String())
	}
	require.NoDirExists(t, filepath.Join(outside, "sub"))
}

// TestProjectDirCreateAndList pins the directory-only tree the dialog draws:
// the listing names subdirectories and whether each has children, and a new
// directory is made in the named parent, answering 409 when the name is taken.
func TestProjectDirCreateAndList(t *testing.T) {
	s, projectID, root := projectDirServer(t)
	require.NoError(t, os.Mkdir(filepath.Join(root, "inbox"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "inbox", "deep", "deeper"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "inbox", "note.txt"), []byte("x"), 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(root, ".hidden"), 0o755))

	list := projectDirsRequest(t, s, projectID, http.MethodGet, "/api/projects/p/dirs", nil)
	require.Equal(t, http.StatusOK, list.Code, list.Body.String())
	var out struct {
		Path    string `json:"path"`
		Entries []struct {
			Name        string `json:"name"`
			HasChildren bool   `json:"has_children"`
		} `json:"entries"`
	}
	require.NoError(t, json.Unmarshal(list.Body.Bytes(), &out))
	require.Equal(t, "", out.Path)
	// Only the directory shows: the file and the hidden directory do not.
	require.Len(t, out.Entries, 1)
	require.Equal(t, "inbox", out.Entries[0].Name)
	require.True(t, out.Entries[0].HasChildren)

	create := projectDirsRequest(t, s, projectID, http.MethodPost, "/api/projects/p/dirs",
		map[string]any{"parent": "inbox", "name": "new"})
	require.Equal(t, http.StatusOK, create.Code, create.Body.String())
	require.DirExists(t, filepath.Join(root, "inbox", "new"))

	conflict := projectDirsRequest(t, s, projectID, http.MethodPost, "/api/projects/p/dirs",
		map[string]any{"parent": "inbox", "name": "new"})
	require.Equal(t, http.StatusConflict, conflict.Code, conflict.Body.String())
}

// TestFileDownloadServesProjectOriginal pins that the download endpoint reads
// the original from the project, not the workspace.
func TestFileDownloadServesProjectOriginal(t *testing.T) {
	s, _, root := projectDirServer(t)
	require.NoError(t, os.Mkdir(filepath.Join(root, "docs"), 0o755))
	rec := uploadFile(t, s, "s-1", "docs", "x.txt", []byte("hello project"))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	req := httptest.NewRequest(http.MethodGet, "/api/files/x/download", nil)
	req = req.WithContext(context.WithValue(req.Context(), ParamsKey, Params{{Key: "id", Value: fileIDOf(t, rec)}}))
	dl := httptest.NewRecorder()
	s.handleFileDownload(dl, req)
	require.Equal(t, http.StatusOK, dl.Code)
	require.Equal(t, "hello project", dl.Body.String())
}
