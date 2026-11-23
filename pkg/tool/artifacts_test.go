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
	"unicode/utf8"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
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

// TestPresentFilesDescriptionPerProjectType pins each project type's own
// description: the tool a session gets must say what that type's prompt asks
// for, and the golden beside it is the text a reviewer diffs.
func TestPresentFilesDescriptionPerProjectType(t *testing.T) {
	for _, tc := range []struct {
		projectType string
		description string
		golden      string
	}{
		{projectType: state.ProjectTypeCoding, description: presentFilesToolDescriptionCoding, golden: "present_files_description_coding.golden"},
		{projectType: state.ProjectTypeOffice, description: presentFilesToolDescriptionOffice, golden: "present_files_description_office.golden"},
		{projectType: state.ProjectTypeBatch, description: presentFilesToolDescriptionBatch, golden: "present_files_description_batch.golden"},
	} {
		st := NewState(t.TempDir())
		tl, err := newPresentFilesTool(st, &AgentToolRuntime{Sess: newStateSessionStoreForTest(t), ProjectType: tc.projectType})
		if err != nil {
			t.Fatalf("newPresentFilesTool(%s): %v", tc.projectType, err)
		}
		got := tl.Description()
		if got != tc.description {
			t.Fatalf("%s: Description() drifted from the constant:\n%q\n%q", tc.projectType, got, tc.description)
		}
		golden, err := os.ReadFile(filepath.Join("testdata", tc.golden))
		if err != nil {
			t.Fatalf("read golden %s: %v", tc.golden, err)
		}
		if want := strings.TrimSuffix(string(golden), "\n"); want != tc.description {
			t.Fatalf("%s: golden drifted from the constant:\n%q\n%q", tc.projectType, want, tc.description)
		}
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

func TestPresentFilesRegisteredForEveryProjectType(t *testing.T) {
	for _, tc := range []struct {
		projectType string
		want        bool
	}{
		{projectType: state.ProjectTypeCoding, want: true},
		{projectType: state.ProjectTypeOffice, want: true},
		{projectType: state.ProjectTypeBatch, want: true},
		{projectType: "", want: true},
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
				// The registered tool carries this type's words, not a
				// neighbour's: one registration site, per-type text.
				if want, _ := presentFilesCopyFor(tc.projectType); tool.Description() != want {
					t.Fatalf("project type %q registered present_files with another type's description:\n%q\n%q", tc.projectType, tool.Description(), want)
				}
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

// TestPresentFilesCompletionCarriesStructuredRows is the reproduction of the
// deliverable card's empty body: the tool answered with a JSON string, so the
// step output carried {"output":"<json>"} and the card could only ever draw
// the "Presented files." fallback. The rows must reach the completion capture
// as the structured map the card and every surface read.
func TestPresentFilesCompletionCarriesStructuredRows(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "ddl.sql"), []byte("create "), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := WithToolCompletionCapture(presentFilesTestCtx(t, root))
	sess := newStateSessionStoreForTest(t)
	if err := sess.Ensure(ctx, "sess-art", "conversation"); err != nil {
		t.Fatal(err)
	}
	tl, err := newPresentFilesTool(NewState(t.TempDir()), &AgentToolRuntime{Sess: sess})
	if err != nil {
		t.Fatalf("newPresentFilesTool: %v", err)
	}
	args, _ := json.Marshal(presentFilesInput{Files: []presentFileInput{{Path: "ddl.sql", Title: "PostgreSQL DDL"}}})
	out, err := tl.Handle(ctx, string(args))
	if err != nil {
		t.Fatalf("present_files: %v", err)
	}
	payload, ok := ToolCompletionFromContext(ctx)
	if !ok {
		t.Fatal("present_files captured nothing: the step output stays the text envelope")
	}
	rows, ok := payload.Output["presented"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("presented = %#v, want one row", payload.Output["presented"])
	}
	row, ok := rows[0].(map[string]any)
	if !ok {
		t.Fatalf("row = %#v, want a map", rows[0])
	}
	if row["rel_path"] != "ddl.sql" || row["kind"] != "text" || row["title"] != "PostgreSQL DDL" {
		t.Fatalf("row = %#v", row)
	}
	if want := int64(len("create ")); row["size"] != want {
		t.Fatalf("row size = %#v, want %d", row["size"], want)
	}
	// The card's facts and the model's result are the same map: one payload,
	// two readers. A second, hand-shaped answer is how the two drift apart.
	raw, err := json.Marshal(payload.Output)
	if err != nil {
		t.Fatal(err)
	}
	outText, _ := out.(string)
	if string(raw) != outText {
		t.Fatalf("model payload %s and captured payload %s disagree", outText, raw)
	}
	if strings.Contains(outText, root) {
		t.Fatalf("the payload leaks the absolute project path: %s", outText)
	}
	body, _ := FormatToolStepResult(StepEvent{Kind: StepKindToolCompleted, ToolName: "present_files",
		Output: payload.Output}, DefaultMaxFormattedBody)
	if want := "- **PostgreSQL DDL** text 7 B `ddl.sql`"; !strings.Contains(body, want) {
		t.Fatalf("body %q lost %q", body, want)
	}
}

// TestPresentFilesCompletionKeepsInputOrder pins that the rows — and so the
// card and the "this turn's deliverables" group — list the files in the order
// the model presented them, which is the order the user should see them in.
func TestPresentFilesCompletionKeepsInputOrder(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"second.md", "first.md", "third.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctx := WithToolCompletionCapture(presentFilesTestCtx(t, root))
	sess := newStateSessionStoreForTest(t)
	if err := sess.Ensure(ctx, "sess-art", "conversation"); err != nil {
		t.Fatal(err)
	}
	tl, err := newPresentFilesTool(NewState(t.TempDir()), &AgentToolRuntime{Sess: sess})
	if err != nil {
		t.Fatalf("newPresentFilesTool: %v", err)
	}
	args, _ := json.Marshal(presentFilesInput{Files: []presentFileInput{
		{Path: "second.md"}, {Path: "first.md"}, {Path: "third.txt"},
	}})
	if _, err := tl.Handle(ctx, string(args)); err != nil {
		t.Fatalf("present_files: %v", err)
	}
	payload, ok := ToolCompletionFromContext(ctx)
	if !ok {
		t.Fatal("present_files captured nothing")
	}
	rows, _ := payload.Output["presented"].([]any)
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}
	for i, want := range []string{"second.md", "first.md", "third.txt"} {
		row, _ := rows[i].(map[string]any)
		if row["rel_path"] != want {
			t.Fatalf("row %d rel_path = %#v, want %q", i, row["rel_path"], want)
		}
	}
}

// TestPresentFilesMissingPathCapturesNothing is the pre-write validation
// contract: a rejected path leaves the conversation with no half-delivered
// set, so nothing is captured and the card's body is the error.
func TestPresentFilesMissingPathCapturesNothing(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.md"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := WithToolCompletionCapture(presentFilesTestCtx(t, root))
	sess := newStateSessionStoreForTest(t)
	if err := sess.Ensure(ctx, "sess-art", "conversation"); err != nil {
		t.Fatal(err)
	}
	tl, err := newPresentFilesTool(NewState(t.TempDir()), &AgentToolRuntime{Sess: sess})
	if err != nil {
		t.Fatalf("newPresentFilesTool: %v", err)
	}
	args, _ := json.Marshal(presentFilesInput{Files: []presentFileInput{{Path: "a.md"}, {Path: "gone.md"}}})
	if _, err := tl.Handle(ctx, string(args)); err == nil {
		t.Fatal("present_files accepted a missing path")
	}
	if payload, ok := ToolCompletionFromContext(ctx); ok {
		t.Fatalf("a rejected set still captured %#v", payload.Output)
	}
	if rows, err := sess.ListArtifactsForRun(context.Background(), "run-art"); err != nil {
		t.Fatal(err)
	} else if len(rows) != 0 {
		t.Fatalf("a rejected set still wrote %d rows", len(rows))
	}
}

// TestPresentFilesLegacyShapeStaysProse is the old sessions' contract: a step
// whose output is the text envelope {"output":"<json>"} has no rows, so the
// card keeps its one line of prose.
func TestPresentFilesLegacyShapeStaysProse(t *testing.T) {
	legacy := `{"presented":[{"id":"art-1","name":"a.md","rel_path":"a.md","title":"a.md","kind":"markdown","preview":"markdown","size":288994}],"note":"…"}`
	evt := StepEvent{Kind: StepKindToolCompleted, ToolName: "present_files", Output: map[string]any{"output": legacy}}
	body, _ := FormatToolStepResult(evt, DefaultMaxFormattedBody)
	if body != "Presented files." {
		t.Fatalf("legacy body = %q, want the fallback line", body)
	}
}

// TestBuildToolMetaCarriesDeliverableRows is the criterion the card and the
// turn's group both branch on: the rows ride the meta, live and replay alike,
// and a step with no rows carries none.
func TestBuildToolMetaCarriesDeliverableRows(t *testing.T) {
	evt := StepEvent{Kind: StepKindToolCompleted, ToolName: "present_files", Output: map[string]any{
		"presented": []any{
			map[string]any{"id": "art-1", "name": "ddl.sql", "rel_path": "db/ddl.sql",
				"title": "PostgreSQL DDL", "kind": "text", "preview": "text", "size": int64(288994)},
			map[string]any{"id": "art-2", "name": "a.md", "kind": "markdown", "size": float64(2048)},
		},
	}}
	meta := BuildToolMeta(evt)
	if len(meta.Deliverables) != 2 {
		t.Fatalf("deliverables = %#v, want two rows", meta.Deliverables)
	}
	first := meta.Deliverables[0]
	if first.Title != "PostgreSQL DDL" || first.RelPath != "db/ddl.sql" || first.Kind != "text" || first.Size != 288994 {
		t.Fatalf("first row = %+v", first)
	}
	// A row without a title or a path still names a file: both fall back to
	// the name, so no surface has to guess what to print.
	second := meta.Deliverables[1]
	if second.Title != "a.md" || second.RelPath != "a.md" || second.Size != 2048 {
		t.Fatalf("second row = %+v", second)
	}
	// The old text envelope has no rows, which is exactly what keeps an old
	// session's card unchanged.
	legacy := BuildToolMeta(StepEvent{Kind: StepKindToolCompleted, ToolName: "present_files",
		Output: map[string]any{"output": `{"presented":[{"name":"a.md"}]}`}})
	if len(legacy.Deliverables) != 0 {
		t.Fatalf("the text envelope yielded rows: %#v", legacy.Deliverables)
	}
}

// TestDeliverablesCrossTheEventBoundary pins the mirror: the canonical event
// carries the same rows (they are what the web card and a replayed row read),
// and the shape converts back into the tool's without loss — except the
// artifact id, which the event mirror has never carried: a replayed row
// reads without one and the surfaces' dedup falls back to the composite key.
func TestDeliverablesCrossTheEventBoundary(t *testing.T) {
	evt := StepEvent{Kind: StepKindToolCompleted, ToolName: "present_files", StepID: "call_1",
		Output: map[string]any{"presented": []any{map[string]any{
			"id": "art-1", "name": "a.md", "rel_path": "out/a.md", "title": "Report",
			"kind": "markdown", "preview": "markdown", "size": int64(2048),
		}}}}
	direct := DeliverablesFrom(evt.Output)
	if len(direct) != 1 || direct[0].ID != "art-1" {
		t.Fatalf("DeliverablesFrom = %#v, want the row carrying its artifact id", direct)
	}
	run, ok := RunEventFromStep(context.Background(), "sess", "run", "tui", evt)
	if !ok {
		t.Fatal("RunEventFromStep dropped the completed call")
	}
	var payload event.ToolCallCompletedPayload
	if err := json.Unmarshal(run.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.ToolMeta.Deliverables) != 1 {
		t.Fatalf("event rows = %#v, want one", payload.ToolMeta.Deliverables)
	}
	back := DeliverablesFromEvent(payload.ToolMeta.Deliverables)
	if len(back) != 1 || back[0] != (Deliverable{Title: "Report", Name: "a.md", Kind: "markdown", RelPath: "out/a.md", Size: 2048}) {
		t.Fatalf("round trip = %#v", back)
	}
}

// TestArtifactKindGlyphsCoverEveryClass is the coverage gate the glyph table
// exists under: every class the classification table can produce has a glyph
// of its own, so adding a format (one row in artifactClasses) fails here until
// its row is drawn too.
func TestArtifactKindGlyphsCoverEveryClass(t *testing.T) {
	seen := map[string]string{}
	for ext, class := range artifactClasses {
		glyph := ArtifactKindGlyph(class.kind)
		if glyph == "◇" {
			t.Errorf("%s (extension %q) has no glyph of its own", class.kind, ext)
		}
		if want, ok := seen[glyph]; ok && want != class.kind {
			t.Errorf("kinds %q and %q share the glyph %q", want, class.kind, glyph)
		}
		seen[glyph] = class.kind
	}
	// The glyph is one rune: a grapheme cluster would take its own columns and
	// shift everything after it.
	for kind, glyph := range artifactKindGlyphs {
		if n := utf8.RuneCountInString(glyph); n != 1 {
			t.Errorf("%s glyph %q is %d runes", kind, glyph, n)
		}
	}
	// Nothing recognized reads as the unknown-class mark.
	if got := ArtifactKindGlyph(""); got != "◇" {
		t.Errorf("empty kind glyph = %q, want ◇", got)
	}
	if got := ArtifactKindGlyph("hologram"); got != "◇" {
		t.Errorf("unknown kind glyph = %q, want ◇", got)
	}
}

func TestFormatPresentFilesStepNeverShowsJSON(t *testing.T) {
	// Both size types a step output can carry: the live capture keeps the Go
	// value AddArtifact returned (int64), while a row read back from JSON is a
	// float64. The card must read either as the same size.
	normal := StepEvent{ToolName: "present_files", Output: map[string]any{
		"presented": []any{
			map[string]any{"id": "art-1", "name": "a.docx", "title": "Q3 report", "kind": "document", "rel_path": "outputs/a.docx", "preview": "docx", "size": float64(1259520)},
			map[string]any{"id": "art-2", "name": "b.pptx", "title": "b.pptx", "kind": "presentation", "rel_path": "b.pptx", "preview": "office_pdf", "size": float64(512)},
			map[string]any{"id": "art-3", "name": "c.pdf", "title": "c.pdf", "kind": "pdf", "rel_path": "c.pdf", "preview": "pdf", "size": int64(1259520)},
		},
	}}
	body, _ := FormatToolStepResult(normal, DefaultMaxFormattedBody)
	wantLines := []string{
		"- **Q3 report** document 1.2 MB `outputs/a.docx`",
		"- **b.pptx** presentation 512 B `b.pptx`",
		"- **c.pdf** pdf 1.2 MB `c.pdf`",
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

// presentFilesMechanicsSentence is the closing sentence all three
// descriptions share verbatim: the tool's mechanics have one wording, only
// what counts as a deliverable changes with the project type.
const presentFilesMechanicsSentence = "Call it once, after the files are final, listing every deliverable; each one gets a download and preview card. Paths must be inside the project directory — save or copy a deliverable there first if it was written elsewhere. Presenting the same path again later records a new version."

// TestPresentFilesDescriptionNamesEachTypesRule pins that each project type
// is taught its own delivery rule — and not another type's.
func TestPresentFilesDescriptionNamesEachTypesRule(t *testing.T) {
	coding, office, batch := presentFilesToolDescriptionCoding, presentFilesToolDescriptionOffice, presentFilesToolDescriptionBatch

	for name, desc := range map[string]string{"coding": coding, "office": office, "batch": batch} {
		// The first rule of all three is the user's named set.
		if !strings.Contains(desc, "present exactly those") {
			t.Errorf("%s: description lost the named-set rule:\n%s", name, desc)
		}
		if !strings.HasSuffix(desc, presentFilesMechanicsSentence) {
			t.Errorf("%s: description does not end with the shared mechanics sentence:\n%s", name, desc)
		}
	}

	for _, want := range []string{"source code, scripts", "no kind of file is excluded"} {
		if !strings.Contains(coding, want) {
			t.Errorf("coding: description lost %q:\n%s", want, coding)
		}
	}
	// The office exclusion is exactly what a coding turn must not be told.
	if strings.Contains(coding, "intermediate scripts") {
		t.Errorf("coding: description still teaches the office rule:\n%s", coding)
	}

	for _, want := range []string{"helper scripts", ".work/", "unless the user explicitly asked for them"} {
		if !strings.Contains(office, want) {
			t.Errorf("office: description lost %q:\n%s", want, office)
		}
	}
	if strings.Contains(office, "source code, scripts") {
		t.Errorf("office: description teaches the coding rule:\n%s", office)
	}

	for _, want := range []string{"outputs/", ".work/", "calling system"} {
		if !strings.Contains(batch, want) {
			t.Errorf("batch: description lost %q:\n%s", want, batch)
		}
	}

	if coding == office || coding == batch || office == batch {
		t.Fatal("two project types share one description: the selector is not choosing")
	}
}

// TestPresentFilesNoteNamesTheReceiver pins the sentence the model reads back:
// the receiver differs between a conversation and a calling system.
func TestPresentFilesNoteNamesTheReceiver(t *testing.T) {
	for _, tc := range []struct {
		projectType string
		want        string
	}{
		{projectType: state.ProjectTypeCoding, want: presentFilesToolNoteUser},
		{projectType: state.ProjectTypeOffice, want: presentFilesToolNoteUser},
		{projectType: state.ProjectTypeBatch, want: presentFilesToolNoteBatch},
	} {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "delivered.txt"), []byte("final"), 0o644); err != nil {
			t.Fatal(err)
		}
		ctx := presentFilesTestCtx(t, root)
		sess := newStateSessionStoreForTest(t)
		if err := sess.Ensure(ctx, "sess-art", "conversation"); err != nil {
			t.Fatal(err)
		}
		tl, err := newPresentFilesTool(NewState(t.TempDir()), &AgentToolRuntime{Sess: sess, ProjectType: tc.projectType})
		if err != nil {
			t.Fatalf("newPresentFilesTool(%s): %v", tc.projectType, err)
		}
		args, _ := json.Marshal(presentFilesInput{Files: []presentFileInput{{Path: "delivered.txt"}}})
		out, err := tl.Handle(ctx, string(args))
		if err != nil {
			t.Fatalf("%s: present_files: %v", tc.projectType, err)
		}
		text, _ := out.(string)
		var payload struct {
			Note string `json:"note"`
		}
		if err := json.Unmarshal([]byte(text), &payload); err != nil {
			t.Fatalf("%s: result is not the JSON payload: %v\n%s", tc.projectType, err, text)
		}
		if payload.Note != tc.want {
			t.Fatalf("%s: note = %q, want %q", tc.projectType, payload.Note, tc.want)
		}
		if tc.projectType == state.ProjectTypeBatch && strings.Contains(payload.Note, "The user") {
			t.Fatalf("batch: note still addresses a person: %q", payload.Note)
		}
	}
}

// TestPresentFilesSchemaHasNoFileCap pins that the schema declares no ceiling
// the tool no longer enforces: one call lists the whole deliverable set, and
// the count is bounded by the model's context, not by a number in the schema.
func TestPresentFilesSchemaHasNoFileCap(t *testing.T) {
	tl, err := newPresentFilesTool(NewState(t.TempDir()), &AgentToolRuntime{Sess: newStateSessionStoreForTest(t)})
	if err != nil {
		t.Fatalf("newPresentFilesTool: %v", err)
	}
	props, _ := tl.InputSchema()["properties"].(map[string]any)
	files, ok := props["files"].(map[string]any)
	if !ok {
		t.Fatalf("files property not found in the tool schema: %#v", tl.InputSchema())
	}
	if limit, capped := files["maxItems"]; capped {
		t.Fatalf("files schema still caps the count: %v", limit)
	}
	raw, declared := files["minItems"]
	if !declared {
		t.Fatalf("files schema lost its one-file minimum: %#v", files)
	}
	if b, _ := json.Marshal(raw); string(b) != "1" {
		t.Fatalf("files minItems = %s, want 1", b)
	}
}

// TestPresentFilesAcceptsPastTheOldCap is the runtime half of the removed
// ceiling: a call listing more files than the schema used to allow is
// recorded whole, not rejected.
func TestPresentFilesAcceptsPastTheOldCap(t *testing.T) {
	root := t.TempDir()
	files := make([]presentFileInput, 0, 21)
	for i := 0; i < 21; i++ {
		name := fmt.Sprintf("part-%02d.txt", i)
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
		files = append(files, presentFileInput{Path: name})
	}
	ctx := presentFilesTestCtx(t, root)
	sess := newStateSessionStoreForTest(t)
	if err := sess.Ensure(ctx, "sess-art", "conversation"); err != nil {
		t.Fatal(err)
	}
	tl, err := newPresentFilesTool(NewState(t.TempDir()), &AgentToolRuntime{Sess: sess, ProjectType: state.ProjectTypeCoding})
	if err != nil {
		t.Fatalf("newPresentFilesTool: %v", err)
	}
	args, _ := json.Marshal(presentFilesInput{Files: files})
	out, err := tl.Handle(ctx, string(args))
	if err != nil {
		t.Fatalf("present_files refused 21 files: %v", err)
	}
	text, _ := out.(string)
	var payload struct {
		Presented []map[string]any `json:"presented"`
	}
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		t.Fatalf("result is not the JSON payload: %v", err)
	}
	if len(payload.Presented) != 21 {
		t.Fatalf("presented = %d rows, want 21", len(payload.Presented))
	}
	rows, err := sess.ListArtifactsForRun(context.Background(), "run-art")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 21 {
		t.Fatalf("artifact rows = %d, want 21", len(rows))
	}
}

// TestPresentFilesDescriptionDefaultsToCoding pins the default arm: a runtime
// with no project type, or a type the tool does not know, is a coding session
// — the same normalization the runner and NormalizeProjectType apply.
func TestPresentFilesDescriptionDefaultsToCoding(t *testing.T) {
	for _, projectType := range []string{"", "wiki"} {
		desc, note := presentFilesCopyFor(projectType)
		if desc != presentFilesToolDescriptionCoding {
			t.Fatalf("project type %q did not fall back to the coding description:\n%s", projectType, desc)
		}
		if note != presentFilesToolNoteUser {
			t.Fatalf("project type %q: note = %q, want %q", projectType, note, presentFilesToolNoteUser)
		}
	}
}

// TestPresentFilesDescriptionKeepsThePromptPhrases cross-checks the tool's
// text against the prompts that name the tool: the two are written in two
// packages, and a phrase that drifts apart is the mismatch this selector
// exists to remove. The shared fragment is verb-neutral on purpose — the
// prompts say "deliver", the tool says "present".
func TestPresentFilesDescriptionKeepsThePromptPhrases(t *testing.T) {
	shared := "exactly those — no others and none missing"
	for name, desc := range map[string]string{
		"coding": presentFilesToolDescriptionCoding,
		"office": presentFilesToolDescriptionOffice,
		"batch":  presentFilesToolDescriptionBatch,
	} {
		if !strings.Contains(desc, shared) {
			t.Errorf("%s: description lost the shared named-set fragment %q", name, shared)
		}
	}
	for _, tc := range []struct {
		name   string
		a, b   string
		shared string
	}{
		{name: "coding delivery", a: presentFilesToolDescriptionCoding, b: agent.CodingDeliveryPrompt, shared: "every file this turn created or modified"},
		{name: "office work", a: presentFilesToolDescriptionOffice, b: agent.OfficeWorkPrompt, shared: ".work/"},
		{name: "batch task", a: presentFilesToolDescriptionBatch, b: agent.BatchTaskPrompt, shared: "outputs/"},
	} {
		if !strings.Contains(tc.a, tc.shared) {
			t.Errorf("%s: tool description lost %q", tc.name, tc.shared)
		}
		if !strings.Contains(tc.b, tc.shared) {
			t.Errorf("%s: prompt lost %q", tc.name, tc.shared)
		}
	}
}

// PrimaryOnlyToolName is the one list of actions that speak for the whole
// conversation: it withholds them from a subagent's tool table and GuardTool
// refuses a call to them inside one, so every name it holds needs a reason that
// belongs to the user rather than to the child.
func TestPrimaryOnlyToolNameList(t *testing.T) {
	for _, name := range []string{"present_files", "skill_publish", "  skill_publish  "} {
		if !PrimaryOnlyToolName(name) {
			t.Fatalf("%q must be primary only", name)
		}
	}
	for _, name := range []string{"", "skill", "read_file", "write_file", "present_files_x"} {
		if PrimaryOnlyToolName(name) {
			t.Fatalf("%q must stay available to a subagent", name)
		}
	}
}
