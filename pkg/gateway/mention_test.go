package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
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

// scopedMentionServer builds a server whose file view resolves to the
// workspace: a project bound to it and one conversation filed under that
// project. Every mention and web-turn request reaches the workspace through
// this session, exactly as the workbench does, so the file view is the
// conversation's — never the agent's workspace by default.
func scopedMentionServer(t *testing.T, home, workspace string) (*Server, string) {
	t.Helper()
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	projects := state.NewProjectStore(db, "main")
	project, err := projects.Create(ctx, state.CreateProjectInput{Name: "workspace", Root: workspace})
	if err != nil {
		t.Fatal(err)
	}
	sessions := state.NewSessionStore(db, "main")
	if err := sessions.Ensure(ctx, "s1", "s1"); err != nil {
		t.Fatal(err)
	}
	if err := projects.BindSession(ctx, "s1", project.ID); err != nil {
		t.Fatal(err)
	}
	return &Server{Home: home, Sessions: sessions, Projects: projects}, "s1"
}

func TestHandleMentionSearchReturnsWorkspaceCandidates(t *testing.T) {
	home, workspace := newMentionWorkspace(t)
	s, sid := scopedMentionServer(t, home, workspace)

	rec := httptest.NewRecorder()
	s.handleMentionSearch(rec, httptest.NewRequest(http.MethodGet, "/api/workspace/mentions?session_id="+sid+"&q=note", nil))

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
	home, workspace := newMentionWorkspace(t)
	s, sid := scopedMentionServer(t, home, workspace)

	rec := httptest.NewRecorder()
	s.handleMentionSearch(rec, httptest.NewRequest(http.MethodGet, "/api/workspace/mentions?session_id="+sid+"&q=../", nil))

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
	home, workspace := newMentionWorkspace(t)
	s, sid := scopedMentionServer(t, home, workspace)

	rec, body := acceptMention(t, s, `{"session_id":"`+sid+`","draft":"read @main","token_start":5,"token_end":10,"path":"main.go"}`)

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
	home, workspace := newMentionWorkspace(t)
	s, sid := scopedMentionServer(t, home, workspace)

	_, body := acceptMention(t, s, `{"session_id":"`+sid+`","draft":"@doc","token_start":0,"token_end":4,"path":"docs","is_dir":true}`)

	if body.Draft != "@docs/" || !body.KeepOpen {
		t.Fatalf("dir accept = %#v", body)
	}
}

// An accepted image drops out of the text and comes back as a
// workspace-relative path, which the turn re-resolves before attaching.
func TestHandleMentionAcceptAttachesImageAsRelativePath(t *testing.T) {
	home, workspace := newMentionWorkspace(t)
	s, sid := scopedMentionServer(t, home, workspace)

	_, body := acceptMention(t, s,
		`{"session_id":"`+sid+`","draft":"see @docs/shot.png","token_start":4,"token_end":18,"path":"docs/shot.png"}`)

	if body.ImagePath != "docs/shot.png" {
		t.Fatalf("image path = %q, want a workspace-relative path", body.ImagePath)
	}
	if body.Draft != "see " {
		t.Fatalf("draft = %q, want the token removed", body.Draft)
	}
}

func TestHandleMentionAcceptRejectsEscape(t *testing.T) {
	home, workspace := newMentionWorkspace(t)
	s, sid := scopedMentionServer(t, home, workspace)

	rec, _ := acceptMention(t, s, `{"session_id":"`+sid+`","draft":"@x","token_start":0,"token_end":2,"path":"../secret.png"}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// Paths arrive from the browser, so a turn re-resolves and reads each image
// rather than trusting the picker's earlier answer. A path outside the
// conversation's workspace fails the message instead of being silently
// dropped from it; a path that resolves but cannot be read or shown to the
// model is a different case — it degrades to a note and the turn still runs
// (TestPrepareWebTurnInputDegradesUnsendableImage).
func TestWebTurnInputValidatesClientImagePaths(t *testing.T) {
	home, workspace := newMentionWorkspace(t)
	s, sid := scopedMentionServer(t, home, workspace)
	ctx := context.Background()

	in, err := s.prepareWebTurnInput(ctx, sid, "compare", nil, []string{"docs/shot.png", "docs/shot.png"})
	if err != nil {
		t.Fatalf("prepareWebTurnInput: %v", err)
	}
	if refs := state.MessageAttachments(in.partsJSON); len(refs) != 1 || refs[0].Label != "docs/shot.png" || refs[0].MIMEType != "image/png" {
		t.Fatalf("an image picked twice is attached once: %+v", refs)
	}
	for _, bad := range []string{"../secret.png", "/etc/passwd"} {
		if _, err := s.prepareWebTurnInput(ctx, sid, "compare", nil, []string{"docs/shot.png", bad}); err == nil {
			t.Errorf("%s was accepted", bad)
		}
	}
}

// TestPrepareWebTurnInputDegradesUnsendableImage pins the web half of the
// self-heal: a file the image ladder cannot send does not fail the message.
// It gets no file_reference — the model is sent this row through
// run.UserInputParts, i.e. the replay path, so the one note it must see comes
// from rehydrateMessageImages — and the sentence rides in the message text,
// where the user reads it. Writing it here as well would say it twice.
func TestPrepareWebTurnInputDegradesUnsendableImage(t *testing.T) {
	home, workspace := newMentionWorkspace(t)
	s, sid := scopedMentionServer(t, home, workspace)
	ctx := context.Background()

	in, err := s.prepareWebTurnInput(ctx, sid, "read this", nil, []string{"docs/note.txt"})
	if err != nil {
		t.Fatalf("an unsendable image must not fail the message: %v", err)
	}
	if !strings.Contains(in.text, "[attachment] docs/note.txt") || !strings.Contains(in.text, "not sent:") {
		t.Fatalf("text = %q want the unsendable-image note", in.text)
	}
	if !strings.Contains(in.text, "read this") {
		t.Fatalf("text = %q want the typed message kept", in.text)
	}
	if refs := state.MessageAttachments(in.partsJSON); len(refs) != 0 {
		t.Fatalf("parts_json holds %+v, want no file_reference for a file that was not sent", refs)
	}
	// The note is in the text part, so the model is told exactly once.
	if got := strings.Count(in.text, "not sent:"); got != 1 {
		t.Fatalf("note appears %d times, want once", got)
	}
	sent := run.UserInputParts(ctx, run.PathResolver{}, in.partsJSON)
	for _, part := range sent {
		if part.Type != llm.ContentTypeText {
			t.Fatalf("sent a %s part for a file that could not be sent", part.Type)
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
	// The conversation's file view is its project's root, so it is filed there.
	projects := state.NewProjectStore(db, "main")
	project, err := projects.Create(ctx, state.CreateProjectInput{Name: "workspace", Root: workspace})
	if err != nil {
		t.Fatal(err)
	}
	if err := projects.BindSession(ctx, "s1", project.ID); err != nil {
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
	s := &Server{Home: home, Files: files, Sessions: sessions, Projects: projects}

	in, err := s.prepareWebTurnInput(ctx, "s1", "look at these", []string{report.ID, photo.ID}, []string{"docs/shot.png"})
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
	home, workspace := newMentionWorkspace(t)
	s, sid := scopedMentionServer(t, home, workspace)
	in, err := s.prepareWebTurnInput(context.Background(), sid, "", nil, []string{"docs/shot.png"})
	if err != nil {
		t.Fatalf("prepareWebTurnInput: %v", err)
	}
	if in.text != "[Image #1]" || in.display != "[Image #1]" {
		t.Fatalf("text = %q, display = %q", in.text, in.display)
	}
}

// TestWebImageUploadUsesFileReferencePart guards the image branch of the
// upload path: an image/* upload must be stored as a file_reference part, so
// the turn and every replay re-read it as pixels; a non-image upload stays a
// name-only attachment. This channel already worked — the test pins it.
func TestWebImageUploadUsesFileReferencePart(t *testing.T) {
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
	projects := state.NewProjectStore(db, "main")
	project, err := projects.Create(ctx, state.CreateProjectInput{Name: "workspace", Root: workspace})
	if err != nil {
		t.Fatal(err)
	}
	if err := projects.BindSession(ctx, "s1", project.ID); err != nil {
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
	s := &Server{Home: home, Files: files, Sessions: sessions, Projects: projects}

	in, err := s.prepareWebTurnInput(ctx, "s1", "look", []string{report.ID, photo.ID}, nil)
	if err != nil {
		t.Fatalf("prepareWebTurnInput: %v", err)
	}
	var parts []map[string]any
	if err := json.Unmarshal([]byte(in.partsJSON), &parts); err != nil {
		t.Fatalf("unmarshal parts: %v", err)
	}
	byLabel := map[string]string{}
	for _, p := range parts {
		if label, _ := p["label"].(string); label != "" {
			ptype, _ := p["type"].(string)
			byLabel[label] = ptype
		}
	}
	if byLabel["photo.png"] != state.PartTypeFileReference {
		t.Fatalf("photo.png part type = %q, want %q", byLabel["photo.png"], state.PartTypeFileReference)
	}
	if byLabel["report.pdf"] != state.PartTypeAttachment {
		t.Fatalf("report.pdf part type = %q, want %q", byLabel["report.pdf"], state.PartTypeAttachment)
	}
}

// TestWebImageGateRefusesTheTurnNotTheAttachment pins the split the web now
// shares with the terminal: preparing a message attaches an image upload
// whatever the model is — the row it becomes keeps the picture, so switching
// model later still sends it — and the turn gate refuses only the request,
// naming the model and pointing at /model. A non-image upload is not the
// image gate's business.
func TestWebImageGateRefusesTheTurnNotTheAttachment(t *testing.T) {
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
	projects := state.NewProjectStore(db, "main")
	project, err := projects.Create(ctx, state.CreateProjectInput{Name: "workspace", Root: workspace})
	if err != nil {
		t.Fatal(err)
	}
	if err := projects.BindSession(ctx, "s1", project.ID); err != nil {
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

	// A runner whose configured session model is the text-only deepseek-v4-pro
	// (owner-confirmed). TestMain installs the repository catalog, so the gate
	// resolves it.
	cfg := &appcfg.Root{}
	cfg.Agents.Definitions = map[string]appcfg.AgentDefinition{"main": {
		Primary:      true,
		LLMProviders: []appcfg.AgentLLMProviderConfig{{Provider: "deepseek", Model: "deepseek-v4-pro"}},
	}}
	runner := &run.Runner{Deps: &run.Deps{Home: home, AgentName: "main", AppCfg: cfg}}
	s := &Server{Home: home, Files: files, Sessions: sessions, Projects: projects, Runner: runner}

	// The attachment step no longer judges the model: the picture is attached
	// and recorded as the reference a later request replays.
	prepared, err := s.prepareWebTurnInput(ctx, "s1", "look", []string{photo.ID}, nil)
	if err != nil {
		t.Fatalf("an image upload must still attach on a text-only model: %v", err)
	}
	if !strings.Contains(prepared.partsJSON, state.PartTypeFileReference) || !strings.Contains(prepared.partsJSON, "image/png") {
		t.Fatalf("prepared parts = %q, want the image kept as a file reference with its MIME type", prepared.partsJSON)
	}

	// The turn gate refuses the request, not the message.
	refusal := run.ImageTurnRefusal(ctx, runner, "s1", s.webMessageAttachesImage(ctx, []string{photo.ID}, nil))
	if refusal == nil {
		t.Fatal("expected the turn gate to refuse an image upload on a text-only model")
	}
	if !strings.Contains(refusal.Error(), "deepseek-v4-pro") || !strings.Contains(refusal.Error(), "/model") {
		t.Fatalf("refusal = %q, want it to name the model and point at /model", refusal)
	}
	var detail *run.ImageInputRefusedError
	if !errors.As(refusal, &detail) || detail.Provider != "deepseek" || detail.Model != "deepseek-v4-pro" {
		t.Fatalf("refusal = %#v, want the typed refusal naming deepseek/deepseek-v4-pro", refusal)
	}

	// A non-image upload attaches and is not the image gate's business.
	if _, err := s.prepareWebTurnInput(ctx, "s1", "look", []string{report.ID}, nil); err != nil {
		t.Fatalf("non-image upload must not be refused: %v", err)
	}
	if run.ImageTurnRefusal(ctx, runner, "s1", s.webMessageAttachesImage(ctx, []string{report.ID}, nil)) != nil {
		t.Fatal("a message carrying no image must not be refused by the image gate")
	}
}
