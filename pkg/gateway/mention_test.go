package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

// pngTestHeader is the full 8-byte PNG signature. Mention classification
// sniffs content, so a stub shorter than this is not recognized as an image.
var pngTestHeader = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}

func newMentionWorkspace(t *testing.T) (home string, workspace string) {
	t.Helper()
	home = t.TempDir()
	workspace = filepath.Join(home, "workspace")
	if err := os.MkdirAll(filepath.Join(workspace, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(rel string, data []byte) {
		if err := os.WriteFile(filepath.Join(workspace, rel), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("main.go", []byte("package main\n"))
	write(filepath.Join("docs", "note.txt"), []byte("workspace note"))
	write(filepath.Join("docs", "shot.png"), pngTestHeader)
	// Sits outside the workspace and must never be reachable.
	if err := os.WriteFile(filepath.Join(home, "secret.png"), pngTestHeader, 0o600); err != nil {
		t.Fatal(err)
	}
	return home, workspace
}

func TestHandleMentionSearchReturnsWorkspaceCandidates(t *testing.T) {
	home, _ := newMentionWorkspace(t)
	s := &Server{Home: home}

	rec := httptest.NewRecorder()
	s.handleMentionSearch(rec, httptest.NewRequest(http.MethodGet, "/api/workspace/mentions?q=note", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		Query   string `json:"query"`
		Records []struct {
			Path  string `json:"path"`
			IsDir bool   `json:"is_dir"`
		} `json:"records"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Query != "note" {
		t.Errorf("query = %q", body.Query)
	}
	found := false
	for _, r := range body.Records {
		if r.Path == "docs/note.txt" {
			found = true
		}
	}
	if !found {
		t.Fatalf("docs/note.txt missing from %#v", body.Records)
	}
}

// The web picker is workspace-scoped: a traversal query must not list files
// the browser has no business seeing.
func TestHandleMentionSearchRejectsEscapes(t *testing.T) {
	home, _ := newMentionWorkspace(t)
	s := &Server{Home: home}

	rec := httptest.NewRecorder()
	s.handleMentionSearch(rec, httptest.NewRequest(http.MethodGet, "/api/workspace/mentions?q=../", nil))

	var body struct {
		Records []struct {
			Path string `json:"path"`
		} `json:"records"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	for _, r := range body.Records {
		if strings.Contains(r.Path, "secret") || strings.HasPrefix(r.Path, "..") {
			t.Fatalf("escape leaked: %#v", body.Records)
		}
	}
}

func acceptMention(t *testing.T, s *Server, payload string) (*httptest.ResponseRecorder, mentionAcceptResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/workspace/mentions/accept", strings.NewReader(payload))
	rec := httptest.NewRecorder()
	s.handleMentionAccept(rec, req)
	var body mentionAcceptResponse
	if rec.Code == http.StatusOK {
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
	}
	return rec, body
}

// The web composer holds no semantics of its own: accepting a file yields the
// same bare path the terminal composer produces.
func TestHandleMentionAcceptResolvesFileToBarePath(t *testing.T) {
	home, _ := newMentionWorkspace(t)
	s := &Server{Home: home}

	rec, body := acceptMention(t, s, `{"draft":"read @main","token_start":5,"token_end":10,"path":"main.go"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if body.Draft != "read main.go " {
		t.Errorf("draft = %q", body.Draft)
	}
	if body.Cursor != len([]rune("read main.go ")) {
		t.Errorf("cursor = %d", body.Cursor)
	}
	if body.ImagePath != "" || body.KeepOpen {
		t.Errorf("unexpected routing: %#v", body)
	}
}

func TestHandleMentionAcceptKeepsDirectoryTokenOpen(t *testing.T) {
	home, _ := newMentionWorkspace(t)
	s := &Server{Home: home}

	_, body := acceptMention(t, s, `{"draft":"@doc","token_start":0,"token_end":4,"path":"docs","is_dir":true}`)

	if body.Draft != "@docs/" || !body.KeepOpen {
		t.Fatalf("dir accept = %#v", body)
	}
}

// An accepted image drops out of the text and comes back as a
// workspace-relative path, which the turn re-resolves before attaching.
func TestHandleMentionAcceptAttachesImageAsRelativePath(t *testing.T) {
	home, _ := newMentionWorkspace(t)
	s := &Server{Home: home}

	_, body := acceptMention(t, s,
		`{"draft":"see @docs/shot.png","token_start":4,"token_end":18,"path":"docs/shot.png"}`)

	if body.ImagePath != "docs/shot.png" {
		t.Fatalf("image path = %q, want a workspace-relative path", body.ImagePath)
	}
	if body.Draft != "see " {
		t.Fatalf("draft = %q, want the token removed", body.Draft)
	}
}

func TestHandleMentionAcceptRejectsEscape(t *testing.T) {
	home, _ := newMentionWorkspace(t)
	s := &Server{Home: home}

	rec, _ := acceptMention(t, s, `{"draft":"@x","token_start":0,"token_end":2,"path":"../secret.png"}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// Paths arrive from the browser, so a turn re-resolves and reads each image
// rather than trusting the picker's earlier answer, and a path it cannot
// show the model fails the message instead of being dropped from it.
func TestWebTurnInputValidatesClientImagePaths(t *testing.T) {
	home, _ := newMentionWorkspace(t)
	s := &Server{Home: home}
	ctx := context.Background()

	in, err := s.prepareWebTurnInput(ctx, "compare", nil, []string{"docs/shot.png", "docs/shot.png"})
	if err != nil {
		t.Fatalf("prepareWebTurnInput: %v", err)
	}
	if refs := state.MessageAttachments(in.partsJSON); len(refs) != 1 || refs[0].Label != "docs/shot.png" || refs[0].MIMEType != "image/png" {
		t.Fatalf("an image picked twice is attached once: %+v", refs)
	}
	for _, bad := range []string{"../secret.png", "docs/note.txt", "docs/missing.png"} {
		if _, err := s.prepareWebTurnInput(ctx, "compare", nil, []string{"docs/shot.png", bad}); err == nil {
			t.Errorf("%s was accepted", bad)
		}
	}
}

// A web message is stored as exactly what its model is sent: the parts read
// back from the stored row are the turn's input, so the turn's end finds its
// own message already on the transcript instead of writing it a second time,
// and the next request replays it byte for byte.
func TestWebTurnInputIsTheMessageItStores(t *testing.T) {
	home, workspace := newMentionWorkspace(t)
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	files := &state.FileStore{DB: db, Home: home, WorkspaceRoot: workspace, Cfg: state.Config{FilesDirRel: "files", FilesTextDirRel: "files-text", TmpDirRel: "tmp"}}
	sessions := state.NewSessionStore(db, "main")
	if err := sessions.Ensure(ctx, "s1", "s1"); err != nil {
		t.Fatal(err)
	}
	report, err := files.CreateFromReader(ctx, "s1", "report.pdf", strings.NewReader("%PDF-1.4"), 8, "application/pdf")
	if err != nil {
		t.Fatal(err)
	}
	photo, err := files.CreateFromReader(ctx, "s1", "photo.png", bytes.NewReader(pngTestHeader), int64(len(pngTestHeader)), "image/png")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Home: home, Files: files, Sessions: sessions}

	in, err := s.prepareWebTurnInput(ctx, "look at these", []string{report.ID, photo.ID}, []string{"docs/shot.png"})
	if err != nil {
		t.Fatalf("prepareWebTurnInput: %v", err)
	}
	if !strings.HasPrefix(in.text, "look at these\n\n[Attachment report.pdf (application/pdf)]\nSaved at: ") || in.display != "look at these" {
		t.Fatalf("text = %q, display = %q", in.text, in.display)
	}
	var names []string
	for _, ref := range state.MessageAttachments(in.partsJSON) {
		names = append(names, ref.Label)
	}
	if strings.Join(names, ",") != "report.pdf,photo.png,docs/shot.png" {
		t.Fatalf("attachments = %v", names)
	}

	resolver := run.ChainResolver{run.FilertResolver{Resolve: func(ctx context.Context, fileID string) (string, bool) {
		abs, _, err := files.EnsureLocalFile(ctx, "main", fileID)
		return abs, err == nil
	}}, run.PathResolver{}}
	sent := run.UserInputParts(ctx, resolver, in.partsJSON)
	var kinds []string
	for _, part := range sent {
		kinds = append(kinds, string(part.Type))
	}
	// The text, then the two images; the pdf is named in the text only.
	if strings.Join(kinds, ",") != string(llm.ContentTypeText)+","+string(llm.ContentTypeImageBase64)+","+string(llm.ContentTypeImageBase64) || sent[0].Text != in.text {
		t.Fatalf("sent parts = %v", kinds)
	}

	if _, err := turn.PersistUserTurn(ctx, sessions, turn.UserTurn{SessionID: "s1", ModelInput: in.text, RawInput: "look at these", PartsJSON: in.partsJSON}); err != nil {
		t.Fatal(err)
	}
	if err := sessions.AppendMessageSequence(ctx, "s1", []llm.Message{
		llm.UserMessage(sent...),
		llm.AssistantMessage([]llm.ContentPart{llm.Text("I see them.")}),
	}, "", ""); err != nil {
		t.Fatal(err)
	}
	var users int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM fb_messages WHERE session_id = 's1' AND role = 'user'`).Scan(&users); err != nil {
		t.Fatal(err)
	}
	if users != 1 {
		t.Fatalf("the transcript holds the message %d times", users)
	}
}

// A message that attaches only an image reads the way the terminal shows one:
// the image's marker is its text, sent to the model and shown on the row.
func TestWebTurnInputWithNothingTyped(t *testing.T) {
	home, _ := newMentionWorkspace(t)
	s := &Server{Home: home}
	in, err := s.prepareWebTurnInput(context.Background(), "", nil, []string{"docs/shot.png"})
	if err != nil {
		t.Fatalf("prepareWebTurnInput: %v", err)
	}
	if in.text != "[Image #1]" || in.display != "[Image #1]" {
		t.Fatalf("text = %q, display = %q", in.text, in.display)
	}
}
