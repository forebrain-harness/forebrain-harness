package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/stretchr/testify/require"
)

// tinyPNG is a valid 1x1 PNG, generated here so no binary fixture is committed.
func tinyPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{G: 255, A: 255})
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

// TestWorkspaceFileRawServesImageBytes pins the pixel channel the thumbnail
// uses: an image inside the file view is served as bytes with its own content
// type, and a non-image is refused (415) so the endpoint never becomes a
// general file dump. An out-of-root path is refused (400).
func TestWorkspaceFileRawServesImageBytes(t *testing.T) {
	s, root := workspaceWriteServer(t)
	image := tinyPNG(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, "shot.png"), image, 0o644))

	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.handleWorkspaceFileRaw(rec, httptest.NewRequest(http.MethodGet, "/api/workspace/file/raw?session_id=S1&path="+path, nil))
		return rec
	}

	rec := get("shot.png")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "image/png", rec.Header().Get("Content-Type"))
	require.Equal(t, "private, max-age=60", rec.Header().Get("Cache-Control"))
	require.Equal(t, image, rec.Body.Bytes())

	// A tool step records the ABSOLUTE path, so the pixel request arrives
	// absolute: the same root resolver must take it and still serve the bytes.
	absRec := get(filepath.Join(root, "shot.png"))
	require.Equal(t, http.StatusOK, absRec.Code, absRec.Body.String())
	require.Equal(t, image, absRec.Body.Bytes())

	// A non-image refuses with 415.
	require.Equal(t, http.StatusUnsupportedMediaType, get("notes/seed.md").Code)
	// A traversal out of the file view root refuses with 400.
	require.Equal(t, http.StatusBadRequest, get("../../outside.png").Code)
	// An absolute path outside every served root refuses too.
	require.Equal(t, http.StatusBadRequest, get(filepath.Join(t.TempDir(), "outside.png")).Code)
}

// TestWorkspaceFileRawServesAgentWorkspaceImage is the case Step 5b exists to
// serve: an MCP screenshot (or any tool artifact) lives under the PRIMARY AGENT
// WORKSPACE, which is a different root from the conversation's file view (the
// project). Its absolute path — the shape a tool step records — must be served,
// or the web thumbnail is broken for exactly the flagship case. A PNG outside
// BOTH roots is still refused.
func TestWorkspaceFileRawServesAgentWorkspaceImage(t *testing.T) {
	s, projectRoot := workspaceWriteServer(t)
	image := tinyPNG(t)
	agentResults := filepath.Join(s.Home, "workspace", "state", "mcp-results")
	require.NoError(t, os.MkdirAll(agentResults, 0o700))
	agentImage := filepath.Join(agentResults, "mcp-shots-shot-1.png")
	require.NoError(t, os.WriteFile(agentImage, image, 0o644))

	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.handleWorkspaceFileRaw(rec, httptest.NewRequest(http.MethodGet, "/api/workspace/file/raw?session_id=S1&path="+path, nil))
		return rec
	}

	// The agent workspace is a distinct root from the file view.
	require.NotEqual(t, projectRoot, filepath.Join(s.Home, "workspace"))

	// An absolute path into the agent workspace serves the bytes...
	rec := get(agentImage)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "image/png", rec.Header().Get("Content-Type"))
	require.Equal(t, image, rec.Body.Bytes())

	// ...while a PNG outside BOTH roots is still refused.
	outside := filepath.Join(t.TempDir(), "outside.png")
	require.NoError(t, os.WriteFile(outside, image, 0o644))
	require.Equal(t, http.StatusBadRequest, get(outside).Code)

	// A *relative* path is still joined to the file view, never to the agent
	// workspace: "state/mcp-results/…" is not a file-view path, so the file only
	// in the agent workspace is not found (404), not served.
	require.Equal(t, http.StatusNotFound, get(filepath.ToSlash(filepath.Join("state", "mcp-results", "mcp-shots-shot-1.png"))).Code)
}

// TestWorkspaceFileRawServesAgentWorkspaceImageWithoutFileView covers a session
// with no file view at all (no project): the file view root cannot be named, so
// every relative path is refused, but an ABSOLUTE path — the shape a tool step
// records — is still served from the agent workspace. Before this, the endpoint
// refused every image in such a session.
func TestWorkspaceFileRawServesAgentWorkspaceImageWithoutFileView(t *testing.T) {
	home := t.TempDir()
	agentResults := filepath.Join(home, "workspace", "state", "mcp-results")
	require.NoError(t, os.MkdirAll(agentResults, 0o700))
	image := tinyPNG(t)
	agentImage := filepath.Join(agentResults, "mcp-shots-shot-1.png")
	require.NoError(t, os.WriteFile(agentImage, image, 0o644))

	// No Sessions/Projects: filesRoot answers errFilesNotFound.
	s := &Server{Home: home}
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.handleWorkspaceFileRaw(rec, httptest.NewRequest(http.MethodGet, "/api/workspace/file/raw?session_id=s1&path="+path, nil))
		return rec
	}

	rec := get(agentImage)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, image, rec.Body.Bytes())
	// A relative path has no file view to join to: refused (404), not silently
	// resolved against the agent workspace.
	require.Equal(t, http.StatusNotFound, get("shot.png").Code)
}

// TestWorkspaceFileRawRefusesForeignSession pins the ownership gate every
// other file endpoint already has: a session id this agent does not own must
// be refused (404) rather than falling through to the agent workspace root,
// which is where a tool-recorded absolute path would otherwise be served.
// An owned session with no file view keeps that legitimate service, and the
// sniff-authoritative MIME gate holds even for a file whose extension
// promises an image.
func TestWorkspaceFileRawRefusesForeignSession(t *testing.T) {
	s, _ := workspaceWriteServer(t)
	image := tinyPNG(t)
	agentResults := filepath.Join(s.Home, "workspace", "state", "mcp-results")
	require.NoError(t, os.MkdirAll(agentResults, 0o700))
	agentImage := filepath.Join(agentResults, "mcp-shots-shot-1.png")
	require.NoError(t, os.WriteFile(agentImage, image, 0o644))

	get := func(session, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.handleWorkspaceFileRaw(rec, httptest.NewRequest(http.MethodGet,
			"/api/workspace/file/raw?session_id="+session+"&path="+path, nil))
		return rec
	}

	// A session this agent does not own — forged or another agent's — is
	// refused the way every other file endpoint refuses it: 404, never the
	// agent workspace root.
	require.Equal(t, http.StatusNotFound, get("S-FOREIGN", agentImage).Code)

	// An owned session with no file view (no project) still serves the
	// tool-recorded absolute path from the agent workspace: the legitimate
	// case the gate must not break.
	require.NoError(t, s.Sessions.Ensure(context.Background(), "S2", "S2"))
	rec := get("S2", agentImage)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, image, rec.Body.Bytes())

	// Text bytes named .png are refused by the MIME gate (415): the
	// extension no longer vouches for the bytes.
	fake := filepath.Join(agentResults, "fake.png")
	require.NoError(t, os.WriteFile(fake, []byte("this is not an image"), 0o644))
	require.Equal(t, http.StatusUnsupportedMediaType, get("S2", fake).Code)
}

// workspaceWriteServer builds a gateway whose file view is one project's root,
// with one conversation filed under it. The agent's own workspace is a
// sibling, reachable only through the versioned skill roots, exactly as in
// production.
func workspaceWriteServer(t *testing.T) (s *Server, projectRoot string) {
	t.Helper()
	ctx := context.Background()
	home := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(home, "workspace", "skills"), 0o755))

	projectRoot = t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(projectRoot, "notes"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(projectRoot, "notes", "seed.md"), []byte("seed"), 0o644))

	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	projects := state.NewProjectStore(db, "main")
	sessions := state.NewSessionStore(db, "main")
	project, err := projects.Create(ctx, state.CreateProjectInput{Name: "P", Root: projectRoot})
	require.NoError(t, err)
	require.NoError(t, sessions.Ensure(ctx, "S1", "S1"))
	require.NoError(t, projects.BindSession(ctx, "S1", project.ID))

	return &Server{Home: home, Sessions: sessions, Projects: projects}, projectRoot
}

func writeReq(t *testing.T, method, url string, body any) *http.Request {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	return httptest.NewRequest(method, url, reader)
}

// TestWorkspaceFileReadWriteCreateRenameDelete covers the plain lifecycle a
// workbench tree row drives: create a file (with its parent directories),
// read it, save it, rename it, then delete its directory recursively.
func TestWorkspaceFileReadWriteCreateRenameDelete(t *testing.T) {
	s, root := workspaceWriteServer(t)

	// Create a new file, parent directories included, via create=true.
	rec := httptest.NewRecorder()
	s.handleWorkspaceFileWrite(rec, writeReq(t, http.MethodPut, "/api/workspace/file?session_id=S1",
		map[string]any{"path": "notes/a.md", "content": "hello", "create": true}))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.FileExists(t, filepath.Join(root, "notes", "a.md"))

	// Creating the same path again is a conflict.
	rec = httptest.NewRecorder()
	s.handleWorkspaceFileWrite(rec, writeReq(t, http.MethodPut, "/api/workspace/file?session_id=S1",
		map[string]any{"path": "notes/a.md", "content": "again", "create": true}))
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())

	// Read it back.
	rec = httptest.NewRecorder()
	s.handleWorkspaceFileRead(rec, httptest.NewRequest(http.MethodGet, "/api/workspace/file?session_id=S1&path=notes/a.md", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var read struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &read))
	require.Equal(t, "notes/a.md", read.Path)
	require.Equal(t, "hello", read.Content)

	// Save over it (create=false).
	rec = httptest.NewRecorder()
	s.handleWorkspaceFileWrite(rec, writeReq(t, http.MethodPut, "/api/workspace/file?session_id=S1",
		map[string]any{"path": "notes/a.md", "content": "world"}))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	raw, err := os.ReadFile(filepath.Join(root, "notes", "a.md"))
	require.NoError(t, err)
	require.Equal(t, "world", string(raw))

	// Rename the file.
	rec = httptest.NewRecorder()
	s.handleWorkspaceRename(rec, writeReq(t, http.MethodPost, "/api/workspace/rename?session_id=S1",
		map[string]any{"from": "notes/a.md", "to": "notes/b.md"}))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoFileExists(t, filepath.Join(root, "notes", "a.md"))
	require.FileExists(t, filepath.Join(root, "notes", "b.md"))

	// Renaming onto an existing path is a conflict.
	rec = httptest.NewRecorder()
	s.handleWorkspaceRename(rec, writeReq(t, http.MethodPost, "/api/workspace/rename?session_id=S1",
		map[string]any{"from": "notes/b.md", "to": "notes/seed.md"}))
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())

	// Delete the directory recursively.
	rec = httptest.NewRecorder()
	s.handleWorkspaceEntryDelete(rec, httptest.NewRequest(http.MethodDelete, "/api/workspace/entry?session_id=S1&path=notes", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoDirExists(t, filepath.Join(root, "notes"))
}

// TestWorkspaceFileReadLimit refuses a file too large to open as text.
func TestWorkspaceFileReadLimit(t *testing.T) {
	s, root := workspaceWriteServer(t)
	big := filepath.Join(root, "big.txt")
	require.NoError(t, os.WriteFile(big, bytes.Repeat([]byte("x"), workspaceFileMaxBytes+1), 0o644))
	rec := httptest.NewRecorder()
	s.handleWorkspaceFileRead(rec, httptest.NewRequest(http.MethodGet, "/api/workspace/file?session_id=S1&path=big.txt", nil))
	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code, rec.Body.String())
}

// TestWorkspaceWritesRefuseEscapes pins the containment: a traversal, a
// symlink out of the root, .git, .versions, and the root itself are all
// refused by every write endpoint.
func TestWorkspaceWritesRefuseEscapes(t *testing.T) {
	s, root := workspaceWriteServer(t)

	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("s"), 0o644))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "link")))

	put := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.handleWorkspaceFileWrite(rec, writeReq(t, http.MethodPut, "/api/workspace/file?session_id=S1",
			map[string]any{"path": path, "content": "x"}))
		return rec
	}
	del := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.handleWorkspaceEntryDelete(rec, httptest.NewRequest(http.MethodDelete, "/api/workspace/entry?session_id=S1&path="+path, nil))
		return rec
	}
	rename := func(from, to string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.handleWorkspaceRename(rec, writeReq(t, http.MethodPost, "/api/workspace/rename?session_id=S1",
			map[string]any{"from": from, "to": to}))
		return rec
	}

	// A path traversal out of the root.
	require.Equal(t, http.StatusBadRequest, put("../outside.txt").Code)
	// A symlink inside the root that points outside it.
	require.Equal(t, http.StatusBadRequest, put("link/evil.txt").Code)
	require.Equal(t, http.StatusBadRequest, del("link/secret.txt").Code)
	// .git is never part of the file view's writable surface.
	require.Equal(t, http.StatusBadRequest, put(".git/config").Code)
	require.Equal(t, http.StatusBadRequest, del(".git").Code)
	// The version store is read-only.
	require.Equal(t, http.StatusBadRequest, put(".versions/x").Code)
	// The root itself is never a target.
	require.Equal(t, http.StatusBadRequest, del("").Code)
	require.Equal(t, http.StatusBadRequest, del(".").Code)
	require.Equal(t, http.StatusBadRequest, rename(".", "moved").Code)
	require.Equal(t, http.StatusBadRequest, rename("notes/seed.md", ".").Code)

	// None of the refused writes touched the outsider.
	require.NoFileExists(t, filepath.Join(outside, "evil.txt"))
	raw, err := os.ReadFile(filepath.Join(outside, "secret.txt"))
	require.NoError(t, err)
	require.Equal(t, "s", string(raw))
}

// TestWorkspaceSkillWriteLandsInDraft pins plan 036's rule: a tree write aimed
// at a versioned skill's formal directory lands in that skill's draft, the
// skill directory itself cannot be deleted or renamed here, and its version
// store cannot be written.
func TestWorkspaceSkillWriteLandsInDraft(t *testing.T) {
	s, root := workspaceWriteServer(t)
	skillsRoot := filepath.Join(root, ".forebrain", "skills")
	require.NoError(t, os.MkdirAll(filepath.Join(skillsRoot, "gateway-ops"), 0o755))
	formal := filepath.Join(skillsRoot, "gateway-ops", "SKILL.md")
	require.NoError(t, os.WriteFile(formal, []byte("original"), 0o644))

	// Editing the formal file redirects into the draft.
	rec := httptest.NewRecorder()
	s.handleWorkspaceFileWrite(rec, writeReq(t, http.MethodPut, "/api/workspace/file?session_id=S1",
		map[string]any{"path": ".forebrain/skills/gateway-ops/SKILL.md", "content": "edited"}))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	raw, err := os.ReadFile(formal)
	require.NoError(t, err)
	require.Equal(t, "original", string(raw), "the formal file must not change")
	draft := filepath.Join(skillsRoot, ".drafts", "gateway-ops", "SKILL.md")
	raw, err = os.ReadFile(draft)
	require.NoError(t, err)
	require.Equal(t, "edited", string(raw), "the edit must land in the draft")

	// Deleting the whole skill directory belongs to the skills list.
	rec = httptest.NewRecorder()
	s.handleWorkspaceEntryDelete(rec, httptest.NewRequest(http.MethodDelete, "/api/workspace/entry?session_id=S1&path=.forebrain/skills/gateway-ops", nil))
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "删除技能请在技能列表里操作")
	require.DirExists(t, filepath.Join(skillsRoot, "gateway-ops"))

	// Renaming the whole skill directory is refused too.
	rec = httptest.NewRecorder()
	s.handleWorkspaceRename(rec, writeReq(t, http.MethodPost, "/api/workspace/rename?session_id=S1",
		map[string]any{"from": ".forebrain/skills/gateway-ops", "to": ".forebrain/skills/renamed"}))
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())

	// The version store is never writable through the tree.
	rec = httptest.NewRecorder()
	s.handleWorkspaceFileWrite(rec, writeReq(t, http.MethodPut, "/api/workspace/file?session_id=S1",
		map[string]any{"path": ".forebrain/skills/gateway-ops/.versions/1/SKILL.md", "content": "x"}))
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

// TestWorkspaceWritesNeedAKnownRoot keeps the file view's root rule: a session
// that is not this agent's, and a request that names nothing, resolve to no
// root and every write endpoint answers 404.
func TestWorkspaceWritesNeedAKnownRoot(t *testing.T) {
	s, _ := workspaceWriteServer(t)
	rec := httptest.NewRecorder()
	s.handleWorkspaceFileWrite(rec, writeReq(t, http.MethodPut, "/api/workspace/file",
		map[string]any{"path": "a.txt", "content": "x"}))
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())

	rec = httptest.NewRecorder()
	s.handleWorkspaceEntryDelete(rec, httptest.NewRequest(http.MethodDelete, "/api/workspace/entry?session_id=missing&path=a.txt", nil))
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}
