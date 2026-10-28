package tool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

func TestClassifyArtifactTable(t *testing.T) {
	cases := []struct {
		name                string
		kind, mime, preview string
	}{
		{"report.docx", "document", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", "docx"},
		{"old.doc", "document", "application/msword", "office_pdf"},
		{"open.odt", "document", "application/vnd.oasis.opendocument.text", "office_pdf"},
		{"legacy.rtf", "document", "application/rtf", "office_pdf"},
		{"book.xlsx", "spreadsheet", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "sheet"},
		{"macro.xlsm", "spreadsheet", "application/vnd.ms-excel.sheet.macroEnabled.12", "sheet"},
		{"legacy.xls", "spreadsheet", "application/vnd.ms-excel", "sheet"},
		{"open.ods", "spreadsheet", "application/vnd.oasis.opendocument.spreadsheet", "sheet"},
		{"data.csv", "spreadsheet", "text/csv", "text"},
		{"data.tsv", "spreadsheet", "text/tab-separated-values", "text"},
		{"deck.pptx", "presentation", "application/vnd.openxmlformats-officedocument.presentationml.presentation", "office_pdf"},
		{"old.ppt", "presentation", "application/vnd.ms-powerpoint", "office_pdf"},
		{"open.odp", "presentation", "application/vnd.oasis.opendocument.presentation", "office_pdf"},
		{"paper.pdf", "pdf", "application/pdf", "pdf"},
		{"shot.png", "image", "image/png", "image"},
		{"shot.jpg", "image", "image/jpeg", "image"},
		{"shot.jpeg", "image", "image/jpeg", "image"},
		{"shot.gif", "image", "image/gif", "image"},
		{"shot.webp", "image", "image/webp", "image"},
		{"shot.bmp", "image", "image/bmp", "image"},
		{"icon.svg", "image", "image/svg+xml", "image"},
		{"clip.mp4", "video", "video/mp4", "video"},
		{"clip.m4v", "video", "video/mp4", "video"},
		{"clip.webm", "video", "video/webm", "video"},
		{"clip.mov", "video", "video/quicktime", "video"},
		{"voice.mp3", "audio", "audio/mpeg", "audio"},
		{"voice.wav", "audio", "audio/wav", "audio"},
		{"voice.m4a", "audio", "audio/mp4", "audio"},
		{"voice.aac", "audio", "audio/aac", "audio"},
		{"voice.ogg", "audio", "audio/ogg", "audio"},
		{"voice.oga", "audio", "audio/ogg", "audio"},
		{"voice.flac", "audio", "audio/flac", "audio"},
		{"page.html", "html", "text/html", "html"},
		{"page.htm", "html", "text/html", "html"},
		{"notes.md", "markdown", "text/markdown", "markdown"},
		{"notes.markdown", "markdown", "text/markdown", "markdown"},
		{"plain.txt", "text", "text/plain", "text"},
		{"run.log", "text", "text/plain", "text"},
		{"doc.json", "text", "application/json", "text"},
		{"cfg.yaml", "text", "text/plain", "text"},
		{"cfg.yml", "text", "text/plain", "text"},
		{"cfg.xml", "text", "text/plain", "text"},
		{"q.sql", "text", "text/plain", "text"},
		{"m.py", "text", "text/plain", "text"},
		{"m.js", "text", "text/plain", "text"},
		{"m.ts", "text", "text/plain", "text"},
		{"m.go", "text", "text/plain", "text"},
		{"M.java", "text", "text/plain", "text"},
		{"m.c", "text", "text/plain", "text"},
		{"m.h", "text", "text/plain", "text"},
		{"m.cpp", "text", "text/plain", "text"},
		{"m.sh", "text", "text/plain", "text"},
		{"s.css", "text", "text/plain", "text"},
		{"bundle.zip", "archive", "application/zip", "archive"},
		{"bundle.tar", "archive", "application/x-tar", "archive"},
		{"bundle.tgz", "archive", "application/gzip", "archive"},
		{"bundle.tar.gz", "archive", "application/gzip", "archive"},
		{"pack.7z", "archive", "application/x-7z-compressed", ""},
		{"pack.rar", "archive", "application/vnd.rar", ""},
		{"art.psd", "design", "image/vnd.adobe.photoshop", ""},
		{"art.ai", "design", "application/octet-stream", ""},
		{"art.sketch", "design", "application/octet-stream", ""},
		{"art.fig", "design", "application/octet-stream", ""},
		{"art.xd", "design", "application/octet-stream", ""},
		{"flow.rp", "design", "application/octet-stream", ""},
		{"plan.dwg", "cad", "application/acad", ""},
		{"plan.dxf", "cad", "application/dxf", ""},
		{"mystery.zzy", "other", "application/octet-stream", ""},
	}
	covered := map[string]bool{}
	for _, tc := range cases {
		kind, mimeType, preview := ClassifyArtifact(tc.name, nil)
		if kind != tc.kind || mimeType != tc.mime || preview != tc.preview {
			t.Errorf("ClassifyArtifact(%q) = (%q, %q, %q), want (%q, %q, %q)", tc.name, kind, mimeType, preview, tc.kind, tc.mime, tc.preview)
		}
		if ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(tc.name), ".")); ext != "" && !strings.HasSuffix(strings.ToLower(tc.name), ".tar.gz") {
			covered[ext] = true
		}
	}
	// Every row of the production table is asserted above; a new extension
	// without a test row fails here rather than shipping unasserted.
	for ext := range artifactClasses {
		if !covered[ext] {
			t.Errorf("extension %q has no table assertion", ext)
		}
	}
	// No extension: the sniffed bytes decide.
	png := append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, make([]byte, 32)...)
	kind, mimeType, preview := ClassifyArtifact("screenshot", png)
	if kind != "image" || mimeType != "image/png" || preview != "image" {
		t.Fatalf("extension-less PNG = (%q, %q, %q), want image/image/png/image", kind, mimeType, preview)
	}
}

func TestPresentFilesDescriptionGolden(t *testing.T) {
	st := NewState(t.TempDir())
	tool, err := newPresentFilesTool(st, &AgentToolRuntime{Sess: newStateSessionStoreForTest(t)})
	if err != nil {
		t.Fatalf("newPresentFilesTool: %v", err)
	}
	if got := tool.Description(); got != presentFilesToolDescription {
		t.Fatalf("Description() drifted from the constant:\n%q\n%q", got, presentFilesToolDescription)
	}
	golden, err := os.ReadFile(filepath.Join("testdata", "present_files_description.golden"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if want := strings.TrimSuffix(string(golden), "\n"); want != presentFilesToolDescription {
		t.Fatalf("golden drifted from the constant:\n%q\n%q", want, presentFilesToolDescription)
	}
}

// newStateSessionStoreForTest opens the state database the artifact rows land
// in, as the session store of the main agent.
func newStateSessionStoreForTest(t *testing.T) *state.SessionStore {
	t.Helper()
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return state.NewSessionStore(db, "main")
}

func presentFilesTestCtx(t *testing.T, root string) context.Context {
	t.Helper()
	ctx := WithProjectRoot(context.Background(), root)
	ctx = WithConversationSessionID(ctx, "sess-art")
	ctx = WithRunID(ctx, "run-art")
	ctx = WithToolUseID(ctx, "toolu_art")
	return ctx
}

func TestPresentFilesRecordsEveryFile(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "outputs"), 0o755); err != nil {
		t.Fatal(err)
	}
	aPath := filepath.Join(root, "outputs", "季度报告.docx")
	bPath := filepath.Join(root, "b.pptx")
	if err := os.WriteFile(aPath, []byte("fake docx bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bPath, []byte("fake pptx bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	sess := newStateSessionStoreForTest(t)
	if err := sess.Ensure(ctx, "sess-art", "conversation"); err != nil {
		t.Fatal(err)
	}
	st := NewState(t.TempDir())
	tool, err := newPresentFilesTool(st, &AgentToolRuntime{Sess: sess})
	if err != nil {
		t.Fatalf("newPresentFilesTool: %v", err)
	}
	args, err := json.Marshal(presentFilesInput{Files: []presentFileInput{
		{Path: "outputs/季度报告.docx", Title: "Q3 report", Description: "The quarterly operating review"},
		{Path: "b.pptx"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	out, err := tool.Handle(presentFilesTestCtx(t, root), string(args))
	if err != nil {
		t.Fatalf("present_files: %v", err)
	}
	outText, _ := out.(string)
	rows, err := sess.ListArtifactsForRun(ctx, "run-art")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	first := rows[0]
	if first.SessionID != "sess-art" || first.RunID != "run-art" || first.ToolCallID != "toolu_art" {
		t.Fatalf("first row keys = %+v", first)
	}
	if first.Path != aPath || first.RelPath != "outputs/季度报告.docx" || first.Name != "季度报告.docx" || first.Title != "Q3 report" || first.Description != "The quarterly operating review" {
		t.Fatalf("first row file fields = %+v", first)
	}
	if first.Kind != "document" || first.Preview != "docx" || first.MIME != "application/vnd.openxmlformats-officedocument.wordprocessingml.document" {
		t.Fatalf("first row class = %+v", first)
	}
	sum := sha256.Sum256([]byte("fake docx bytes"))
	if first.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("first row sha256 = %s, want the whole-file digest", first.SHA256)
	}
	if first.Size != int64(len("fake docx bytes")) || first.Origin != state.ArtifactOriginModel {
		t.Fatalf("first row size/origin = %d/%q", first.Size, first.Origin)
	}
	second := rows[1]
	if second.Title != "b.pptx" || second.Kind != "presentation" || second.Preview != "office_pdf" {
		t.Fatalf("second row = %+v", second)
	}
	for _, id := range []string{first.ID, second.ID} {
		if !strings.Contains(outText, `"id":"`+id+`"`) {
			t.Fatalf("response %s lost id %s", outText, id)
		}
	}
	if strings.Contains(outText, root) {
		t.Fatalf("response leaks the absolute project path: %s", outText)
	}
}

func TestPresentFilesRefusesOutsideTheProject(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	inside := filepath.Join(root, "a.docx")
	if err := os.WriteFile(inside, []byte("inside"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(os.TempDir(), fmt.Sprintf("plan146-outside-%d.docx", os.Getpid()))
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(outside)
	sess := newStateSessionStoreForTest(t)
	if err := sess.Ensure(ctx, "sess-art", "conversation"); err != nil {
		t.Fatal(err)
	}
	st := NewState(t.TempDir())
	tool, err := newPresentFilesTool(st, &AgentToolRuntime{Sess: sess})
	if err != nil {
		t.Fatalf("newPresentFilesTool: %v", err)
	}
	args, err := json.Marshal(presentFilesInput{Files: []presentFileInput{
		{Path: "a.docx"},
		{Path: outside},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Handle(presentFilesTestCtx(t, root), string(args)); err == nil {
		t.Fatal("a file outside the project was presented")
	} else if !strings.Contains(err.Error(), "outside the project directory") {
		t.Fatalf("error = %v, want the outside-the-project message", err)
	}
	rows, err := sess.ListArtifactsForRun(ctx, "run-art")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("a refused call still wrote %d rows", len(rows))
	}
}

func TestPresentFilesRefusesSymlinkEscape(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	secret := filepath.Join(os.TempDir(), fmt.Sprintf("plan146-secret-%d.docx", os.Getpid()))
	if err := os.WriteFile(secret, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(secret)
	link := filepath.Join(root, "escape.docx")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	sess := newStateSessionStoreForTest(t)
	if err := sess.Ensure(ctx, "sess-art", "conversation"); err != nil {
		t.Fatal(err)
	}
	st := NewState(t.TempDir())
	tool, err := newPresentFilesTool(st, &AgentToolRuntime{Sess: sess})
	if err != nil {
		t.Fatalf("newPresentFilesTool: %v", err)
	}
	args, err := json.Marshal(presentFilesInput{Files: []presentFileInput{{Path: "escape.docx"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Handle(presentFilesTestCtx(t, root), string(args)); err == nil {
		t.Fatal("a symlink out of the project was presented")
	}
	rows, err := sess.ListArtifactsForRun(ctx, "run-art")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("a refused symlink still wrote %d rows", len(rows))
	}
}

func TestPresentFilesRefusedForSubagents(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.docx"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	sess := newStateSessionStoreForTest(t)
	if err := sess.Ensure(ctx, "sess-art", "conversation"); err != nil {
		t.Fatal(err)
	}
	st := NewState(t.TempDir())
	tool, err := newPresentFilesTool(st, &AgentToolRuntime{Sess: sess})
	if err != nil {
		t.Fatalf("newPresentFilesTool: %v", err)
	}
	subCtx := WithSubagentRun(presentFilesTestCtx(t, root), true)
	if _, err := tool.Handle(subCtx, `{"files":[{"path":"a.docx"}]}`); err == nil {
		t.Fatal("a subagent presented files")
	}
	// The guard holds the same line before the handler ever runs.
	if err := st.GuardTool(subCtx, "present_files"); err == nil {
		t.Fatal("GuardTool allowed a subagent's present_files")
	}
	if err := st.GuardTool(presentFilesTestCtx(t, root), "present_files"); err != nil {
		t.Fatalf("GuardTool refused the primary agent's present_files: %v", err)
	}
}

func TestPresentFilesRegisteredOnlyForOfficeAndBatch(t *testing.T) {
	for _, tc := range []struct {
		projectType string
		want        bool
	}{
		{projectType: state.ProjectTypeCoding, want: false},
		{projectType: state.ProjectTypeOffice, want: true},
		{projectType: state.ProjectTypeBatch, want: true},
	} {
		st := NewState(t.TempDir())
		a, err := agent.New(registryNoopLLM{}, "main", "test")
		if err != nil {
			t.Fatalf("agent.New: %v", err)
		}
		if err := RegisterDefaultTools(a, st, &AgentToolRuntime{Cfg: &appcfg.Root{}, ProjectType: tc.projectType}); err != nil {
			t.Fatalf("RegisterDefaultTools(%s): %v", tc.projectType, err)
		}
		registered := false
		for _, tool := range a.Tools() {
			if tool.Name() == "present_files" {
				registered = true
			}
		}
		if registered != tc.want {
			t.Fatalf("project type %q registered present_files = %v, want %v", tc.projectType, registered, tc.want)
		}
		if _, ok := st.ToolMetaByName("present_files"); ok != tc.want {
			t.Fatalf("project type %q ToolMeta = %v, want %v", tc.projectType, ok, tc.want)
		}
	}
}

func TestFormatPresentFilesStepNeverShowsJSON(t *testing.T) {
	normal := StepEvent{ToolName: "present_files", Output: map[string]any{
		"presented": []any{
			map[string]any{"id": "art-1", "name": "a.docx", "title": "Q3 report", "kind": "document", "rel_path": "outputs/a.docx", "preview": "docx", "size": float64(1259520)},
			map[string]any{"id": "art-2", "name": "b.pptx", "title": "b.pptx", "kind": "presentation", "rel_path": "b.pptx", "preview": "office_pdf", "size": float64(512)},
		},
	}}
	body, _ := FormatToolStepResult(normal, DefaultMaxFormattedBody)
	wantLines := []string{
		"- **Q3 report** · document · 1.2 MB · `outputs/a.docx`",
		"- **b.pptx** · presentation · 512 B · `b.pptx`",
	}
	for _, want := range wantLines {
		if !strings.Contains(body, want) {
			t.Fatalf("body %q lost line %q", body, want)
		}
	}
	for _, bad := range []StepEvent{
		{ToolName: "present_files", Output: map[string]any{"presented": "not-a-list"}},
		{ToolName: "present_files", Output: map[string]any{"other": "shape"}},
		{ToolName: "present_files", Output: nil},
	} {
		body, _ = FormatToolStepResult(bad, DefaultMaxFormattedBody)
		if body != "Presented files." {
			t.Fatalf("malformed output body = %q, want the fallback line", body)
		}
	}
}
