package tool

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rc4"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/pdf"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

// unixPermsObservable reports whether the platform exposes POSIX permission
// bits through os.FileInfo.Mode().Perm(). Windows does not implement them
// (Mode().Perm() reports 0666/0444 regardless of chmod), so permission
// assertions are skipped there; the underlying write is still exercised.
//
// This is a runtime check rather than a pair of build-tagged files because the
// difference is a value, not a compilation concern -- and two tag-split files
// for one boolean left pkg/tool with test files that had no production file to
// correspond to.
func unixPermsObservable() bool { return runtime.GOOS != "windows" }

func TestIsBlockedReadDevicePath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want bool
	}{
		{name: "zero", path: "/dev/zero", want: true},
		{name: "stdin", path: "/dev/stdin", want: true},
		{name: "proc fd", path: "/proc/self/fd/0", want: true},
		{name: "regular", path: "/tmp/file.txt", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isBlockedReadDevicePath(tt.path); got != tt.want {
				t.Fatalf("isBlockedReadDevicePath(%q)=%v want %v", tt.path, got, tt.want)
			}
		})
	}
}

// WriteFullFileForRoots was a production function that only the tests in this
// package ever called: a thin composition of live code. It lives here, beside
// the one test that calls it, so the production files carry no unused code
// while the tests keep exercising the live functions underneath.
func WriteFullFileForRoots(filePath, content string, roots []string) (string, error) {
	if len(roots) == 0 {
		return "", fmt.Errorf("no roots")
	}
	filePath = strings.TrimSpace(filePath)
	if filePath == "" {
		return "", fmt.Errorf("file_path required")
	}
	abs, err := ResolveWithinRoots(filePath, roots)
	if err != nil {
		return "", err
	}
	unlock := lockFileWrite(abs)
	defer unlock()
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", err
	}
	if err := atomicWrite(abs, []byte(content), 0o644); err != nil {
		return "", err
	}
	return abs, nil
}

func TestWriteFullFileForRootsPreservesExistingPermissions(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "script.sh")
	if err := os.WriteFile(path, []byte("old\n"), 0o755); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	if _, err := WriteFullFileForRoots(path, "new\n", []string{tmp}); err != nil {
		t.Fatalf("WriteFullFileForRoots: %v", err)
	}

	assertFilePerm(t, path, 0o755)
}

func assertFilePerm(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if !unixPermsObservable() {
		// Windows does not implement POSIX permission bits; the write itself
		// is still validated by the caller, only the perm comparison is skipped.
		return
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s perm=%#o want %#o", path, got, want)
	}
}

func TestWriteFileApprovalIncludesResolvedPath(t *testing.T) {
	workspace := t.TempDir()
	p := filepath.Join(workspace, "new.txt")
	st := NewState(workspace)
	st.SetPrimaryWorkspaceBoundary("", workspace, nil)
	var resolved string
	st.SetActionHook(func(ctx context.Context, kind string, payload any) (string, bool, error) {
		if kind != "write_file" {
			t.Fatalf("unexpected action kind %q", kind)
		}
		body, ok := payload.(map[string]any)
		if !ok {
			t.Fatalf("unexpected payload type %T", payload)
		}
		resolved, _ = body["resolved_file_path"].(string)
		return "", false, nil
	})
	tool, err := NewFileWriteTool(st, nil)
	if err != nil {
		t.Fatalf("NewFileWriteTool: %v", err)
	}
	if _, err := tool.Handle(context.Background(), `{"file_path":"`+escapeJSONPath(p)+`","content":"new"}`); err != nil {
		t.Fatalf("write_file: %v", err)
	}
	if resolved != p {
		t.Fatalf("resolved path=%q want %q", resolved, p)
	}
}

func TestWriteFileAllowedProjectRootIncludesResolvedPath(t *testing.T) {
	workspace := t.TempDir()
	project := t.TempDir()
	p := filepath.Join(project, "new.txt")
	st := NewState(workspace, project)
	st.SetPrimaryWorkspaceBoundary("", workspace, nil)
	var resolved string
	st.SetActionHook(func(ctx context.Context, kind string, payload any) (string, bool, error) {
		if kind != "write_file" {
			t.Fatalf("unexpected action kind %q", kind)
		}
		body, ok := payload.(map[string]any)
		if !ok {
			t.Fatalf("unexpected payload type %T", payload)
		}
		resolved, _ = body["resolved_file_path"].(string)
		return "", false, nil
	})
	tool, err := NewFileWriteTool(st, nil)
	if err != nil {
		t.Fatalf("NewFileWriteTool: %v", err)
	}
	if _, err := tool.Handle(context.Background(), `{"file_path":"`+escapeJSONPath(p)+`","content":"new"}`); err != nil {
		t.Fatalf("write_file: %v", err)
	}
	if resolved != p {
		t.Fatalf("resolved path=%q want %q", resolved, p)
	}
}

func TestWriteFileMarksReadStateAsWrite(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "note.txt")
	if err := os.WriteFile(p, []byte("old"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	st := NewState(tmp)
	readTool, err := NewFileReadTool(st)
	if err != nil {
		t.Fatalf("NewFileReadTool: %v", err)
	}
	if _, err := readTool.Handle(context.Background(), `{"file_path":"`+escapeJSONPath(p)+`"}`); err != nil {
		t.Fatalf("read_file: %v", err)
	}
	writeTool, err := NewFileWriteTool(st, nil)
	if err != nil {
		t.Fatalf("NewFileWriteTool: %v", err)
	}
	if _, err := writeTool.Handle(context.Background(), `{"file_path":"`+escapeJSONPath(p)+`","content":"new"}`); err != nil {
		t.Fatalf("write_file: %v", err)
	}
	rs, ok := st.GetReadState(p)
	if !ok {
		t.Fatal("expected read state after write")
	}
	if rs.TouchKind != "write" {
		t.Fatalf("touch kind=%q want write", rs.TouchKind)
	}
}

func escapeJSONPath(p string) string {
	b, _ := json.Marshal(p)
	return string(b[1 : len(b)-1])
}

func TestFileReadRejectsBinaryExtensionBeforeCachingRead(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "archive.zip")
	if err := os.WriteFile(p, []byte("not actually zip text"), 0o644); err != nil {
		t.Fatalf("write binary extension file: %v", err)
	}
	st := NewState(tmp)
	var observed bool
	st.SetReadObserver(func(context.Context, string, []byte) {
		observed = true
	})
	tool, err := NewFileReadTool(st)
	if err != nil {
		t.Fatalf("NewFileReadTool: %v", err)
	}

	_, err = tool.Handle(context.Background(), `{"file_path":"`+escapeJSONPath(p)+`"}`)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "binary") {
		t.Fatalf("err=%v want binary refusal", err)
	}
	if observed {
		t.Fatalf("binary file should not notify read observer")
	}
	if _, ok := st.GetReadState(p); ok {
		t.Fatalf("binary file should not be remembered as read")
	}
}

func TestFileReadRejectsBinaryContentBeforeCachingRead(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "payload.txt")
	if err := os.WriteFile(p, []byte{'h', 'i', 0, 'x'}, 0o644); err != nil {
		t.Fatalf("write binary content file: %v", err)
	}
	st := NewState(tmp)
	tool, err := NewFileReadTool(st)
	if err != nil {
		t.Fatalf("NewFileReadTool: %v", err)
	}

	_, err = tool.Handle(context.Background(), `{"file_path":"`+escapeJSONPath(p)+`"}`)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "binary") {
		t.Fatalf("err=%v want binary refusal", err)
	}
	if _, ok := st.GetReadState(p); ok {
		t.Fatalf("binary content should not be remembered as read")
	}
}

func TestFileReadAcceptsNotebookJSONForReadBeforeEdit(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "analysis.ipynb")
	raw := []byte(`{"cells":[{"cell_type":"markdown","source":"old"}],"metadata":{},"nbformat":4,"nbformat_minor":5}`)
	if err := os.WriteFile(p, raw, 0o644); err != nil {
		t.Fatalf("write notebook: %v", err)
	}
	st := NewState(tmp)
	tool, err := NewFileReadTool(st)
	if err != nil {
		t.Fatalf("NewFileReadTool: %v", err)
	}

	if _, err := tool.Handle(context.Background(), `{"file_path":"`+escapeJSONPath(p)+`"}`); err != nil {
		t.Fatalf("read notebook: %v", err)
	}
	if _, ok := st.GetReadState(p); !ok {
		t.Fatalf("notebook should be remembered as read")
	}
}

func TestFileReadStillReadsText(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "note.txt")
	if err := os.WriteFile(p, []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatalf("write text: %v", err)
	}
	tool, err := NewFileReadTool(NewState(tmp))
	if err != nil {
		t.Fatalf("NewFileReadTool: %v", err)
	}

	got, err := tool.Handle(context.Background(), `{"file_path":"`+escapeJSONPath(p)+`","offset":1,"limit":1}`)
	if err != nil {
		t.Fatalf("read text: %v", err)
	}
	if got.(string) != "2|beta" {
		t.Fatalf("got %q want line-numbered text", got)
	}
}

func TestFileReadOffsetIsZeroBased(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "note.txt")
	if err := os.WriteFile(p, []byte("alpha\nbeta\ngamma\n"), 0o644); err != nil {
		t.Fatalf("write text: %v", err)
	}
	tool, err := NewFileReadTool(NewState(tmp))
	if err != nil {
		t.Fatalf("NewFileReadTool: %v", err)
	}

	got, err := tool.Handle(context.Background(), `{"file_path":"`+escapeJSONPath(p)+`","offset":0,"limit":2}`)
	if err != nil {
		t.Fatalf("read text: %v", err)
	}
	if got.(string) != "1|alpha\n2|beta" {
		t.Fatalf("got %q want first two lines", got)
	}
}

func TestFileReadLargeFileAutoPagesWhenLimitMissing(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "big.txt")
	var b strings.Builder
	for i := 0; i < 450; i++ {
		b.WriteString("line ")
		b.WriteString(strings.Repeat("x", 80))
		b.WriteString("\n")
	}
	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write text: %v", err)
	}
	st := NewState(tmp)
	tool, err := NewFileReadTool(st)
	if err != nil {
		t.Fatalf("NewFileReadTool: %v", err)
	}

	ctx := WithToolCompletionCapture(context.Background())
	got, err := tool.Handle(ctx, `{"file_path":"`+escapeJSONPath(p)+`"}`)
	if err != nil {
		t.Fatalf("read text: %v", err)
	}
	lines := strings.Split(got.(string), "\n")
	if len(lines) != DefaultPageLimit {
		t.Fatalf("lines=%d want %d", len(lines), DefaultPageLimit)
	}
	completion, ok := ToolCompletionFromContext(ctx)
	if !ok {
		t.Fatal("expected tool completion capture")
	}
	if completion.Output["auto_paged"] != true {
		t.Fatalf("expected auto_paged output, got %#v", completion.Output)
	}
	if completion.Output["limit"] != DefaultPageLimit {
		t.Fatalf("expected limit=%d, got %#v", DefaultPageLimit, completion.Output)
	}
	if completion.Output["next_offset"] != DefaultPageLimit {
		t.Fatalf("expected next_offset=%d, got %#v", DefaultPageLimit, completion.Output)
	}
	if _, ok := completion.Output["estimated_tokens"]; ok {
		t.Fatalf("estimated_tokens must not be exposed: %#v", completion.Output)
	}
}

// TestFileReadSchemaOnlyRequiresFilePath keeps the schema aligned with
// resolveReadFileRange, which treats a zero offset/limit as "not given". Marking
// them required makes the model guess a page window for a file it has not seen.
func TestFileReadSchemaOnlyRequiresFilePath(t *testing.T) {
	tool, err := NewFileReadTool(NewState(t.TempDir()))
	if err != nil {
		t.Fatalf("NewFileReadTool: %v", err)
	}
	required, _ := tool.InputSchema()["required"].([]any)
	if len(required) != 1 || required[0] != "file_path" {
		t.Fatalf("read_file required = %v, want [file_path]", required)
	}
}

// TestFileReadSchemaDocumentsClamping pins that the clamping contract lives in
// the schema the model sees, not only in the notes it gets afterwards: the cap,
// the end-clamped-to-last-page rule and the start-past-end empty read must all
// be knowable before the first call.
func TestFileReadSchemaDocumentsClamping(t *testing.T) {
	tool, err := NewFileReadTool(NewState(t.TempDir()))
	if err != nil {
		t.Fatalf("NewFileReadTool: %v", err)
	}
	properties, _ := tool.InputSchema()["properties"].(map[string]any)
	if properties == nil {
		t.Fatalf("read_file schema has no properties: %#v", tool.InputSchema())
	}
	field := func(name string) string {
		t.Helper()
		prop, _ := properties[name].(map[string]any)
		desc, _ := prop["description"].(string)
		if desc == "" {
			t.Fatalf("schema property %q has no description: %#v", name, prop)
		}
		return desc
	}
	pages := field("pages")
	for _, want := range []string{"returns through the last page", "at most 5 pages", "returns no pages and names the document's page count"} {
		if !strings.Contains(pages, want) {
			t.Fatalf("pages description %q does not document %q", pages, want)
		}
	}
	if limit := field("limit"); !strings.Contains(limit, "returns through the last line") {
		t.Fatalf("limit description %q does not document the EOF clamp", limit)
	}
	if offset := field("offset"); !strings.Contains(offset, "names the file's line count") {
		t.Fatalf("offset description %q does not document the empty read", offset)
	}
}

func TestFileReadSmallFileWithoutLimitRemainsFullRead(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "small.txt")
	if err := os.WriteFile(p, []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatalf("write text: %v", err)
	}
	st := NewState(tmp)
	tool, err := NewFileReadTool(st)
	if err != nil {
		t.Fatalf("NewFileReadTool: %v", err)
	}

	ctx := WithToolCompletionCapture(context.Background())
	got, err := tool.Handle(ctx, `{"file_path":"`+escapeJSONPath(p)+`"}`)
	if err != nil {
		t.Fatalf("read text: %v", err)
	}
	if got.(string) != "1|alpha\n2|beta" {
		t.Fatalf("got %q want full read", got)
	}
	completion, ok := ToolCompletionFromContext(ctx)
	if !ok {
		t.Fatal("expected tool completion capture")
	}
	// Small file under 200 lines: limit is still set to DefaultPageLimit,
	// but clamped to totalLines (2), so auto_paged is false (no more content).
	if completion.Output["auto_paged"] != nil {
		t.Fatalf("small file under 200 lines must not auto page: %#v", completion.Output)
	}
	// Even though we got the full file, limit was applied (then clamped to EOF).
	if completion.Output["limit"] != 2 {
		t.Fatalf("expected limit=2 (clamped to totalLines), got %#v", completion.Output["limit"])
	}
}

func TestFileReadMarksOnlyLoadedSkillMainFile(t *testing.T) {
	root := t.TempDir()
	skillRoot := filepath.Join(root, ".forebrain", "skills", "review")
	skillFile := filepath.Join(skillRoot, "SKILL.md")
	referenceFile := filepath.Join(skillRoot, "references", "guide.md")
	if err := os.MkdirAll(filepath.Dir(referenceFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skillFile, []byte("# Review\n\nFollow the checklist.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(referenceFile, []byte("reference\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st := NewState(root)
	st.ReplaceLoadedSkillCatalog([]LoadedSkill{{Name: "review", RootDir: skillRoot}})
	readTool, err := NewFileReadTool(st)
	if err != nil {
		t.Fatal(err)
	}

	ctx := WithToolCompletionCapture(context.Background())
	if _, err := readTool.Handle(ctx, `{"file_path":`+quoteJSON(skillFile)+`}`); err != nil {
		t.Fatal(err)
	}
	completion, ok := ToolCompletionFromContext(ctx)
	if !ok {
		t.Fatal("skill read recorded no completion")
	}
	if completion.Output["preview_kind"] != "skill" || completion.Output["skill_name"] != "review" || completion.Output["skill_path"] != normalizeRootPath(skillFile) {
		t.Fatalf("skill metadata = %#v", completion.Output)
	}

	referenceCtx := WithToolCompletionCapture(context.Background())
	if _, err := readTool.Handle(referenceCtx, `{"file_path":`+quoteJSON(referenceFile)+`}`); err != nil {
		t.Fatal(err)
	}
	referenceCompletion, ok := ToolCompletionFromContext(referenceCtx)
	if !ok {
		t.Fatal("reference read recorded no completion")
	}
	if referenceCompletion.Output["preview_kind"] == "skill" {
		t.Fatalf("skill reference was mislabeled as a skill load: %#v", referenceCompletion.Output)
	}
}

func TestFileReadPreservesSkillIdentityWhenMainFileFails(t *testing.T) {
	root := t.TempDir()
	skillRoot := filepath.Join(root, ".forebrain", "skills", "missing")
	if err := os.MkdirAll(skillRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	skillFile := filepath.Join(skillRoot, "SKILL.md")
	st := NewState(root)
	st.ReplaceLoadedSkillCatalog([]LoadedSkill{{Name: "missing", RootDir: skillRoot}})
	readTool, err := NewFileReadTool(st)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithToolCompletionCapture(context.Background())
	if _, err := readTool.Handle(ctx, `{"file_path":`+quoteJSON(skillFile)+`}`); err == nil {
		t.Fatal("missing SKILL.md read unexpectedly succeeded")
	}
	completion, ok := ToolCompletionFromContext(ctx)
	if !ok || completion.Output["skill_name"] != "missing" || completion.Output["skill_path"] != normalizeRootPath(skillFile) {
		t.Fatalf("failed skill read lost identity: %#v, ok=%v", completion, ok)
	}
}

func TestFileReadSmallFileOver200LinesCappedAt200(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "small_but_long.txt")
	var b strings.Builder
	// Small file (each line is short) but with 250 lines.
	for i := 0; i < 250; i++ {
		b.WriteString("line\n")
	}
	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write text: %v", err)
	}
	st := NewState(tmp)
	tool, err := NewFileReadTool(st)
	if err != nil {
		t.Fatalf("NewFileReadTool: %v", err)
	}

	ctx := WithToolCompletionCapture(context.Background())
	got, err := tool.Handle(ctx, `{"file_path":"`+escapeJSONPath(p)+`"}`)
	if err != nil {
		t.Fatalf("read text: %v", err)
	}
	lines := strings.Split(got.(string), "\n")
	if len(lines) != DefaultPageLimit {
		t.Fatalf("lines=%d want %d (DefaultPageLimit)", len(lines), DefaultPageLimit)
	}
	completion, ok := ToolCompletionFromContext(ctx)
	if !ok {
		t.Fatal("expected tool completion capture")
	}
	if completion.Output["limit"] != DefaultPageLimit {
		t.Fatalf("expected limit=%d, got %#v", DefaultPageLimit, completion.Output["limit"])
	}
	if completion.Output["auto_paged"] != true {
		t.Fatalf("expected auto_paged=true (250 lines > 200), got %#v", completion.Output["auto_paged"])
	}
	if completion.Output["next_offset"] != DefaultPageLimit {
		t.Fatalf("expected next_offset=%d, got %#v", DefaultPageLimit, completion.Output["next_offset"])
	}
}

func TestFileReadCompletionOutputJSONSafe(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "small.txt")
	if err := os.WriteFile(p, []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatalf("write text: %v", err)
	}
	st := NewState(tmp)
	tool, err := NewFileReadTool(st)
	if err != nil {
		t.Fatalf("NewFileReadTool: %v", err)
	}
	ctx := WithToolCompletionCapture(context.Background())
	if _, err := tool.Handle(ctx, `{"file_path":"`+escapeJSONPath(p)+`"}`); err != nil {
		t.Fatalf("read text: %v", err)
	}
	completion, ok := ToolCompletionFromContext(ctx)
	if !ok {
		t.Fatal("expected tool completion capture")
	}
	if _, err := json.Marshal(completion.Output); err != nil {
		t.Fatalf("completion output must marshal: %v", err)
	}
}

func TestFileReadOutsideAllowedRootsReadsWithoutApproval(t *testing.T) {
	root := t.TempDir()
	outsideDir := t.TempDir()
	p := filepath.Join(outsideDir, "note.txt")
	if err := os.WriteFile(p, []byte("alpha\n"), 0o644); err != nil {
		t.Fatalf("write outside text: %v", err)
	}
	st := NewState(root)
	st.SetActionHook(func(context.Context, string, any) (string, bool, error) {
		t.Error("reading outside the roots must not raise an approval action")
		return "", false, nil
	})
	tool, err := NewFileReadTool(st)
	if err != nil {
		t.Fatalf("NewFileReadTool: %v", err)
	}

	ctx := WithToolUseID(WithToolCompletionCapture(context.Background()), "read-call-1")
	out, err := tool.Handle(ctx, `{"file_path":"`+escapeJSONPath(p)+`"}`)
	if err != nil {
		t.Fatalf("read outside the roots: %v", err)
	}
	if !strings.Contains(fmt.Sprint(out), "alpha") {
		t.Fatalf("unexpected output %q", out)
	}
}

// deny_read is the boundary that survives: it is a policy refusal, not a
// question the operator can be asked.
func TestFileReadOutsideAllowedRootsStillHonorsDenyRead(t *testing.T) {
	root := t.TempDir()
	outsideDir := t.TempDir()
	p := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(p, []byte("alpha\n"), 0o644); err != nil {
		t.Fatalf("write outside text: %v", err)
	}
	st := NewState(root)
	st.SetReadPolicy([]string{outsideDir}, nil, nil, root)
	tool, err := NewFileReadTool(st)
	if err != nil {
		t.Fatalf("NewFileReadTool: %v", err)
	}
	if _, err := tool.Handle(context.Background(), `{"file_path":"`+escapeJSONPath(p)+`"}`); !errors.Is(err, ErrPathReadDenied) {
		t.Fatalf("expected ErrPathReadDenied, got %v", err)
	}
}

// authorizeRead is the shared authorization of every reading tool, so it must
// answer the same path the same way whatever tool name it is asked for: the
// only thing allowed to differ is the name carried by the approval request.
func TestAuthorizeReadMatchesReadFile(t *testing.T) {
	root := t.TempDir()
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "note.txt")
	if err := os.WriteFile(outside, []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write outside text: %v", err)
	}
	deniedDir := t.TempDir()
	denied := filepath.Join(deniedDir, "secret.txt")
	if err := os.WriteFile(denied, []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write denied text: %v", err)
	}
	inside := filepath.Join(root, "main.go")
	if err := os.WriteFile(inside, []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write inside text: %v", err)
	}

	confined := NewState(root)
	confined.ConfineToRoot(root)
	denying := NewState(root)
	denying.SetReadPolicy([]string{deniedDir}, nil, nil, root)

	for _, tc := range []struct {
		name  string
		state *State
		path  string
	}{
		{name: "inside root", state: confined, path: inside},
		{name: "outside confined root", state: confined, path: outside},
		{name: "deny_read", state: denying, path: denied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, readErr := authorizeRead(context.Background(), tc.state, "read_file", tc.path)
			_, lspErr := authorizeRead(context.Background(), tc.state, "lsp", tc.path)
			if (readErr == nil) != (lspErr == nil) {
				t.Fatalf("authorizeRead disagrees with itself: read_file err=%v, lsp err=%v", readErr, lspErr)
			}
			if readErr == nil {
				return
			}
			if !errors.Is(readErr, ErrPathNotAllowed) && !errors.Is(readErr, ErrPathReadDenied) {
				t.Fatalf("read_file err = %v, want ErrPathNotAllowed or ErrPathReadDenied", readErr)
			}
			if errors.Is(lspErr, ErrPathNotAllowed) != errors.Is(readErr, ErrPathNotAllowed) {
				t.Fatalf("error classes differ: read_file=%v lsp=%v", readErr, lspErr)
			}
			if errors.Is(lspErr, ErrPathReadDenied) != errors.Is(readErr, ErrPathReadDenied) {
				t.Fatalf("error classes differ: read_file=%v lsp=%v", readErr, lspErr)
			}
		})
	}

	// A protected FOREBRAIN_HOME path asks through an action whose kind and
	// tool are the reading tool that triggered it, not always read_file.
	home := t.TempDir()
	active := t.TempDir()
	protected := filepath.Join(home, "settings.yaml")
	if err := os.WriteFile(protected, []byte("kind: spark\n"), 0o644); err != nil {
		t.Fatalf("write protected text: %v", err)
	}
	st := NewState(root)
	st.SetPrimaryWorkspaceBoundary(home, active, nil)
	var hookedKind string
	st.SetActionHook(func(_ context.Context, kind string, _ any) (string, bool, error) {
		hookedKind = kind
		return "act-1", true, nil
	})
	for _, toolName := range []string{"read_file", "lsp"} {
		hookedKind = ""
		_, err := authorizeRead(context.Background(), st, toolName, protected)
		var req *RequiresActionError
		if !errors.As(err, &req) {
			t.Fatalf("%s: err = %v, want RequiresActionError", toolName, err)
		}
		if req.ToolName != toolName || req.ActionKind != toolName {
			t.Fatalf("%s: RequiresActionError names %q/%q", toolName, req.ActionKind, req.ToolName)
		}
		if hookedKind != toolName {
			t.Fatalf("%s: action hook saw kind %q", toolName, hookedKind)
		}
	}
}

func TestFileReadOffsetBeyondEOFReturnsMessageNotError(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "note.txt")
	if err := os.WriteFile(p, []byte("alpha\nbeta\ngamma\n"), 0o644); err != nil {
		t.Fatalf("write text: %v", err)
	}
	tool, err := NewFileReadTool(NewState(tmp))
	if err != nil {
		t.Fatalf("NewFileReadTool: %v", err)
	}

	ctx := WithToolCompletionCapture(context.Background())
	got, err := tool.Handle(ctx, `{"file_path":"`+escapeJSONPath(p)+`","offset":100,"limit":10}`)
	if err != nil {
		t.Fatalf("offset beyond EOF must not error: %v", err)
	}
	s := got.(string)
	if !strings.Contains(s, "No content at offset") {
		t.Fatalf("got %q want friendly no-content message", s)
	}

	completion, ok := ToolCompletionFromContext(ctx)
	if !ok {
		t.Fatal("expected tool completion capture")
	}
	if completion.Output["result_lines"] != 0 {
		t.Fatalf("expected result_lines=0, got %#v", completion.Output["result_lines"])
	}
	if completion.Output["total_lines"] != 3 {
		t.Fatalf("expected total_lines=3, got %#v", completion.Output["total_lines"])
	}
	if completion.Output["offset"] != 3 {
		t.Fatalf("expected clamped offset=3, got %#v", completion.Output["offset"])
	}
}

func TestFileReadOffsetAtEOFReturnsMessageNotError(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "note.txt")
	if err := os.WriteFile(p, []byte("alpha\nbeta\ngamma\n"), 0o644); err != nil {
		t.Fatalf("write text: %v", err)
	}
	tool, err := NewFileReadTool(NewState(tmp))
	if err != nil {
		t.Fatalf("NewFileReadTool: %v", err)
	}

	// offset == totalLines (3): exactly at EOF, no line to read.
	if _, err := tool.Handle(context.Background(), `{"file_path":"`+escapeJSONPath(p)+`","offset":3}`); err != nil {
		t.Fatalf("offset at EOF must not error: %v", err)
	}
}

func TestFileReadLimitBeyondEOFCappedToRemaining(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "note.txt")
	if err := os.WriteFile(p, []byte("alpha\nbeta\ngamma\n"), 0o644); err != nil {
		t.Fatalf("write text: %v", err)
	}
	tool, err := NewFileReadTool(NewState(tmp))
	if err != nil {
		t.Fatalf("NewFileReadTool: %v", err)
	}

	ctx := WithToolCompletionCapture(context.Background())
	got, err := tool.Handle(ctx, `{"file_path":"`+escapeJSONPath(p)+`","offset":1,"limit":1000}`)
	if err != nil {
		t.Fatalf("limit beyond EOF must not error: %v", err)
	}
	// offset=1 (0-based) -> start at line 2; only lines 2-3 remain.
	if got.(string) != "2|beta\n3|gamma" {
		t.Fatalf("got %q want lines 2-3", got)
	}
	completion, ok := ToolCompletionFromContext(ctx)
	if !ok {
		t.Fatal("expected tool completion capture")
	}
	if completion.Output["limit"] != 2 {
		t.Fatalf("expected limit clamped to 2, got %#v", completion.Output["limit"])
	}
	if completion.Output["result_lines"] != 2 {
		t.Fatalf("expected result_lines=2, got %#v", completion.Output["result_lines"])
	}
}

func TestFileReadNegativeOffsetClampedToZero(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "note.txt")
	if err := os.WriteFile(p, []byte("alpha\nbeta\ngamma\n"), 0o644); err != nil {
		t.Fatalf("write text: %v", err)
	}
	tool, err := NewFileReadTool(NewState(tmp))
	if err != nil {
		t.Fatalf("NewFileReadTool: %v", err)
	}

	got, err := tool.Handle(context.Background(), `{"file_path":"`+escapeJSONPath(p)+`","offset":-5,"limit":2}`)
	if err != nil {
		t.Fatalf("negative offset must not error: %v", err)
	}
	// Clamped to offset=0, so read first two lines.
	if got.(string) != "1|alpha\n2|beta" {
		t.Fatalf("got %q want first two lines", got)
	}
}

func TestFileReadLargeFileLimitCappedToDefaultPageLimit(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "big.txt")
	var b strings.Builder
	for i := 0; i < 450; i++ {
		b.WriteString("line ")
		b.WriteString(strings.Repeat("x", 80))
		b.WriteString("\n")
	}
	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write text: %v", err)
	}
	st := NewState(tmp)
	tool, err := NewFileReadTool(st)
	if err != nil {
		t.Fatalf("NewFileReadTool: %v", err)
	}

	ctx := WithToolCompletionCapture(context.Background())
	// Request a limit far beyond DefaultPageLimit for a large file.
	got, err := tool.Handle(ctx, `{"file_path":"`+escapeJSONPath(p)+`","offset":0,"limit":1000}`)
	if err != nil {
		t.Fatalf("read text: %v", err)
	}
	lines := strings.Split(got.(string), "\n")
	if len(lines) != DefaultPageLimit {
		t.Fatalf("lines=%d want %d (capped to DefaultPageLimit)", len(lines), DefaultPageLimit)
	}
	completion, ok := ToolCompletionFromContext(ctx)
	if !ok {
		t.Fatal("expected tool completion capture")
	}
	if completion.Output["limit"] != DefaultPageLimit {
		t.Fatalf("expected limit=%d, got %#v", DefaultPageLimit, completion.Output["limit"])
	}
	if completion.Output["auto_paged"] != true {
		t.Fatalf("expected auto_paged=true for large file, got %#v", completion.Output["auto_paged"])
	}
	if completion.Output["next_offset"] != DefaultPageLimit {
		t.Fatalf("expected next_offset=%d, got %#v", DefaultPageLimit, completion.Output["next_offset"])
	}
}

func TestFileReadLargeFileExplicitLimitWithinDefaultNotCapped(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "big.txt")
	var b strings.Builder
	for i := 0; i < 450; i++ {
		b.WriteString("line ")
		b.WriteString(strings.Repeat("x", 80))
		b.WriteString("\n")
	}
	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write text: %v", err)
	}
	tool, err := NewFileReadTool(NewState(tmp))
	if err != nil {
		t.Fatalf("NewFileReadTool: %v", err)
	}

	ctx := WithToolCompletionCapture(context.Background())
	// Explicit limit within DefaultPageLimit should be honored as-is.
	got, err := tool.Handle(ctx, `{"file_path":"`+escapeJSONPath(p)+`","offset":0,"limit":50}`)
	if err != nil {
		t.Fatalf("read text: %v", err)
	}
	lines := strings.Split(got.(string), "\n")
	if len(lines) != 50 {
		t.Fatalf("lines=%d want 50", len(lines))
	}
	completion, ok := ToolCompletionFromContext(ctx)
	if !ok {
		t.Fatal("expected tool completion capture")
	}
	if completion.Output["limit"] != 50 {
		t.Fatalf("expected limit=50, got %#v", completion.Output["limit"])
	}
	if completion.Output["auto_paged"] != true {
		t.Fatalf("expected auto_paged=true for large file, got %#v", completion.Output["auto_paged"])
	}
	if completion.Output["next_offset"] != 50 {
		t.Fatalf("expected next_offset=50, got %#v", completion.Output["next_offset"])
	}
}

func TestEditFileSerializesConcurrentWritesToSameFile(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "notes.md")
	var src strings.Builder
	for i := 0; i < 64; i++ {
		src.WriteString(fmt.Sprintf("line-%02d: old-%02d\n", i, i))
	}
	if err := os.WriteFile(path, []byte(src.String()), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat source: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	st := NewState(tmp)
	st.RememberRead(path, fi.ModTime(), fi.Size(), raw)
	tool, err := NewFileEditTool(st)
	if err != nil {
		t.Fatalf("NewFileEditTool: %v", err)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for i := 0; i < 64; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			args, _ := json.Marshal(FileEditInput{
				FilePath:   path,
				OldString:  fmt.Sprintf("old-%02d", i),
				NewString:  fmt.Sprintf("new-%02d", i),
				ReplaceAll: false,
			})
			if _, err := tool.Handle(context.Background(), string(args)); err != nil {
				errs <- err
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent edit failed: %v", err)
	}
	outRaw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	out := string(outRaw)
	for i := 0; i < 64; i++ {
		if strings.Contains(out, fmt.Sprintf("old-%02d", i)) {
			t.Fatalf("lost concurrent edit for old-%02d:\n%s", i, out)
		}
		if !strings.Contains(out, fmt.Sprintf("new-%02d", i)) {
			t.Fatalf("missing concurrent edit for new-%02d:\n%s", i, out)
		}
	}
}

func TestEditFileToleratesTabsVsSpaces(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "foo.go")
	src := "package foo\n\nfunc bar() {\n\tvalue := 1\n\treturn value\n}\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	fi, _ := os.Stat(path)
	raw, _ := os.ReadFile(path)
	st := NewState(tmp)
	st.RememberRead(path, fi.ModTime(), fi.Size(), raw)
	tool, err := NewFileEditTool(st)
	if err != nil {
		t.Fatalf("NewFileEditTool: %v", err)
	}
	// Caller retransmits with 4-space indentation instead of the file's tabs.
	args, _ := json.Marshal(FileEditInput{
		FilePath:  path,
		OldString: "    value := 1\n    return value\n",
		NewString: "    value := 2\n    return value\n",
	})
	if _, err := tool.Handle(context.Background(), string(args)); err != nil {
		t.Fatalf("edit failed (should tolerate tabs vs spaces): %v", err)
	}
	out, _ := os.ReadFile(path)
	if !strings.Contains(string(out), "value := 2") {
		t.Fatalf("edit not applied: %s", out)
	}
	// On-disk tab indentation must be preserved in the replacement.
	if !strings.Contains(string(out), "\tvalue := 2") {
		t.Fatalf("file indentation not preserved as tabs: %q", out)
	}
}

// editDiagStub records the code-intelligence calls the edit tools make and
// answers DidWrite with a canned delta. It embeds nopCodeIntel for the rest of
// the port.
type editDiagStub struct {
	nopCodeIntel
	delta     DiagnosticsDelta
	calls     []editDiagCall
	runShells int
}

type editDiagCall struct {
	sid     string
	changes []FileChange
}

func (s *editDiagStub) DidWrite(ctx context.Context, agentSessionID string, changes []FileChange) DiagnosticsDelta {
	recorded := append([]FileChange(nil), changes...)
	for i := range recorded {
		recorded[i].Before = append([]byte(nil), recorded[i].Before...)
		recorded[i].After = append([]byte(nil), recorded[i].After...)
	}
	s.calls = append(s.calls, editDiagCall{sid: agentSessionID, changes: recorded})
	return s.delta
}

func (s *editDiagStub) DidRunShell(ctx context.Context) { s.runShells++ }

func TestWriteFileReportsDiagnostics(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "note.txt")
	st := NewState(tmp)
	stub := &editDiagStub{delta: DiagnosticsDelta{
		Text:    "<diagnostics>\n1 new problem after this edit (gopls)\n</diagnostics>",
		Summary: event.LSPDiagnosticsSummary{New: 1, Files: 1},
	}}
	tool, err := NewFileWriteTool(st, &AgentToolRuntime{CodeIntel: stub})
	if err != nil {
		t.Fatalf("NewFileWriteTool: %v", err)
	}
	ctx := llm.WithAgentSessionID(WithToolCompletionCapture(context.Background()), "sess-diag")
	got, err := tool.Handle(ctx, `{"file_path":"`+escapeJSONPath(p)+`","content":"body\n"}`)
	if err != nil {
		t.Fatalf("write_file: %v", err)
	}
	want := "ok\n\n<diagnostics>\n1 new problem after this edit (gopls)\n</diagnostics>"
	if got.(string) != want {
		t.Fatalf("result=%q want %q", got, want)
	}
	completion, ok := ToolCompletionFromContext(ctx)
	if !ok {
		t.Fatal("expected tool completion capture")
	}
	if _, ok := completion.Output["lsp_diagnostics"]; !ok {
		t.Fatalf("expected lsp_diagnostics on output, got %#v", completion.Output)
	}
	if len(stub.calls) != 1 {
		t.Fatalf("DidWrite calls=%d want 1", len(stub.calls))
	}
	call := stub.calls[0]
	if call.sid != "sess-diag" {
		t.Fatalf("agent session id=%q want sess-diag", call.sid)
	}
	if len(call.changes) != 1 {
		t.Fatalf("changes=%d want 1", len(call.changes))
	}
	if call.changes[0].AbsPath != p {
		t.Fatalf("change path=%q want %q", call.changes[0].AbsPath, p)
	}
	if call.changes[0].Before != nil {
		t.Fatalf("new file Before=%q want nil", call.changes[0].Before)
	}
	if string(call.changes[0].After) != "body\n" {
		t.Fatalf("change After=%q want %q", call.changes[0].After, "body\n")
	}

	// Overwriting the same file reports the previous content as Before.
	stub.calls = nil
	got, err = tool.Handle(ctx, `{"file_path":"`+escapeJSONPath(p)+`","content":"replaced\n"}`)
	if err != nil {
		t.Fatalf("overwrite write_file: %v", err)
	}
	if got.(string) != want {
		t.Fatalf("overwrite result=%q want %q", got, want)
	}
	if len(stub.calls) != 1 || string(stub.calls[0].changes[0].Before) != "body\n" {
		t.Fatalf("overwrite Before: %+v", stub.calls)
	}
	if string(stub.calls[0].changes[0].After) != "replaced\n" {
		t.Fatalf("overwrite After=%q", stub.calls[0].changes[0].After)
	}
}

func TestWriteFileWithoutCodeIntelUnchanged(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "plain.txt")
	st := NewState(tmp)
	tool, err := NewFileWriteTool(st, nil)
	if err != nil {
		t.Fatalf("NewFileWriteTool: %v", err)
	}
	ctx := WithToolCompletionCapture(context.Background())
	got, err := tool.Handle(ctx, `{"file_path":"`+escapeJSONPath(p)+`","content":"plain\n"}`)
	if err != nil {
		t.Fatalf("write_file: %v", err)
	}
	if got.(string) != "ok" {
		t.Fatalf("result=%q want ok", got)
	}
	completion, ok := ToolCompletionFromContext(ctx)
	if !ok {
		t.Fatal("expected tool completion capture")
	}
	if _, ok := completion.Output["lsp_diagnostics"]; ok {
		t.Fatalf("lsp_diagnostics must stay absent: %#v", completion.Output)
	}
}

func TestEditFileReportsDiagnostics(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "edit.txt")
	if err := os.WriteFile(p, []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	st := NewState(tmp)
	readTool, err := NewFileReadTool(st)
	if err != nil {
		t.Fatalf("NewFileReadTool: %v", err)
	}
	if _, err := readTool.Handle(context.Background(), `{"file_path":"`+escapeJSONPath(p)+`"}`); err != nil {
		t.Fatalf("read_file: %v", err)
	}
	stub := &editDiagStub{delta: DiagnosticsDelta{
		Text:    "<diagnostics>\n1 new problem after this edit (gopls)\n</diagnostics>",
		Summary: event.LSPDiagnosticsSummary{New: 1, Files: 1},
	}}
	tool, err := NewFileEditToolWithOptions(st, FileEditOptions{CodeIntel: stub})
	if err != nil {
		t.Fatalf("NewFileEditToolWithOptions: %v", err)
	}
	ctx := llm.WithAgentSessionID(WithToolCompletionCapture(context.Background()), "sess-edit-diag")
	got, err := tool.Handle(ctx, `{"file_path":"`+escapeJSONPath(p)+`","old_string":"beta","new_string":"gamma"}`)
	if err != nil {
		t.Fatalf("edit_file: %v", err)
	}
	want := "ok\n\n<diagnostics>\n1 new problem after this edit (gopls)\n</diagnostics>"
	if got.(string) != want {
		t.Fatalf("result=%q want %q", got, want)
	}
	completion, ok := ToolCompletionFromContext(ctx)
	if !ok {
		t.Fatal("expected tool completion capture")
	}
	if _, ok := completion.Output["lsp_diagnostics"]; !ok {
		t.Fatalf("expected lsp_diagnostics on output, got %#v", completion.Output)
	}
	if len(stub.calls) != 1 {
		t.Fatalf("DidWrite calls=%d want 1", len(stub.calls))
	}
	call := stub.calls[0]
	if call.sid != "sess-edit-diag" {
		t.Fatalf("agent session id=%q", call.sid)
	}
	if string(call.changes[0].Before) != "alpha\nbeta\n" {
		t.Fatalf("Before=%q want pre-edit content", call.changes[0].Before)
	}
	if string(call.changes[0].After) != "alpha\ngamma\n" {
		t.Fatalf("After=%q want post-edit content", call.changes[0].After)
	}
}

func TestEditFileEmptyDeltaAddsNothing(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "clean.txt")
	if err := os.WriteFile(p, []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	st := NewState(tmp)
	readTool, err := NewFileReadTool(st)
	if err != nil {
		t.Fatalf("NewFileReadTool: %v", err)
	}
	if _, err := readTool.Handle(context.Background(), `{"file_path":"`+escapeJSONPath(p)+`"}`); err != nil {
		t.Fatalf("read_file: %v", err)
	}
	stub := &editDiagStub{}
	tool, err := NewFileEditToolWithOptions(st, FileEditOptions{CodeIntel: stub})
	if err != nil {
		t.Fatalf("NewFileEditToolWithOptions: %v", err)
	}
	ctx := WithToolCompletionCapture(context.Background())
	got, err := tool.Handle(ctx, `{"file_path":"`+escapeJSONPath(p)+`","old_string":"beta","new_string":"gamma"}`)
	if err != nil {
		t.Fatalf("edit_file: %v", err)
	}
	if got.(string) != "ok" {
		t.Fatalf("result=%q want ok", got)
	}
	completion, ok := ToolCompletionFromContext(ctx)
	if !ok {
		t.Fatal("expected tool completion capture")
	}
	if _, ok := completion.Output["lsp_diagnostics"]; ok {
		t.Fatalf("lsp_diagnostics must stay absent: %#v", completion.Output)
	}
	if len(stub.calls) != 1 {
		t.Fatalf("DidWrite still runs once, calls=%d", len(stub.calls))
	}
}

func TestApplyPatchReportsDiagnosticsOnce(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	st := NewState(root)
	mustWriteFile(t, root+"/a.txt", "a-one\n")
	mustWriteFile(t, root+"/b.txt", "b-one\n")
	mustWriteFile(t, root+"/gone.txt", "delete me\n")
	mustWriteFile(t, root+"/old.txt", "old\n")
	stub := &editDiagStub{delta: DiagnosticsDelta{
		Text:    "<diagnostics>\n2 new problems after this edit (gopls)\n</diagnostics>",
		Summary: event.LSPDiagnosticsSummary{New: 2, Files: 2},
	}}
	tool, err := NewShellTool(st, &AgentToolRuntime{
		Cfg:       &appcfg.Root{SandboxMode: appcfg.SandboxModeDangerFullAccess},
		CodeIntel: stub,
	})
	if err != nil {
		t.Fatalf("NewShellTool: %v", err)
	}
	cmd := `apply_patch <<'PATCH'
*** Begin Patch
*** Update File: a.txt
@@
-a-one
+a-two
*** Update File: b.txt
@@
-b-one
+b-two
*** Delete File: gone.txt
*** Update File: old.txt
*** Move to: nested/new.txt
@@
-old
+new
*** End Patch
PATCH`
	args, err := json.Marshal(map[string]string{"command": cmd})
	if err != nil {
		t.Fatalf("encode args: %v", err)
	}
	ctx := llm.WithAgentSessionID(WithToolCompletionCapture(context.Background()), "sess-patch-diag")
	got, err := tool.Handle(ctx, string(args))
	if err != nil {
		t.Fatalf("apply_patch: %v", err)
	}
	if len(stub.calls) != 1 {
		t.Fatalf("DidWrite calls=%d want 1 (one patch shares one wait window)", len(stub.calls))
	}
	byPath := map[string]FileChange{}
	for _, ch := range stub.calls[0].changes {
		byPath[ch.AbsPath] = ch
	}
	if len(byPath) != 5 {
		t.Fatalf("changes=%d want 5 (a, b, delete, move source, move target): %+v", len(byPath), stub.calls[0].changes)
	}
	if c := byPath[root+"/a.txt"]; string(c.Before) != "a-one\n" || string(c.After) != "a-two\n" {
		t.Fatalf("a.txt change: %+v", c)
	}
	if c := byPath[root+"/b.txt"]; string(c.Before) != "b-one\n" || string(c.After) != "b-two\n" {
		t.Fatalf("b.txt change: %+v", c)
	}
	if c := byPath[root+"/gone.txt"]; string(c.Before) != "delete me\n" || c.After != nil {
		t.Fatalf("deleted change: %+v", c)
	}
	if c := byPath[root+"/old.txt"]; string(c.Before) != "old\n" || c.After != nil {
		t.Fatalf("move source change: %+v", c)
	}
	if c := byPath[root+"/nested/new.txt"]; c.Before != nil || string(c.After) != "new\n" {
		t.Fatalf("move target change: %+v", c)
	}
	if stub.calls[0].sid != "sess-patch-diag" {
		t.Fatalf("agent session id=%q", stub.calls[0].sid)
	}
	var payload struct {
		Stdout string `json:"stdout"`
	}
	if err := json.Unmarshal([]byte(got.(string)), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if !strings.HasSuffix(strings.TrimSpace(payload.Stdout), "</diagnostics>") {
		t.Fatalf("stdout must end with the diagnostics text: %q", payload.Stdout)
	}
	if !strings.Contains(payload.Stdout, "2 new problems after this edit") {
		t.Fatalf("stdout missing diagnostics text: %q", payload.Stdout)
	}
	completion, ok := ToolCompletionFromContext(ctx)
	if !ok {
		t.Fatal("expected tool completion capture")
	}
	if _, ok := completion.Output["lsp_diagnostics"]; !ok {
		t.Fatalf("expected lsp_diagnostics on completion output: %#v", completion.Output)
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(got.(string)), &raw); err != nil {
		t.Fatalf("decode raw payload: %v", err)
	}
	if _, ok := raw["lsp_diagnostics"]; !ok {
		t.Fatalf("expected lsp_diagnostics on returned payload: %#v", raw)
	}
}

// recordingFileChanges captures the file-change rows the file tools report.
type recordingFileChanges struct {
	mu  sync.Mutex
	got []state.FileChange
}

func (r *recordingFileChanges) RecordFileChange(_ context.Context, c state.FileChange) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, c)
	return nil
}

// TestFileToolsRecordChangesForRewind pins Step 2: every file-tool write — an
// overwrite, a create — reports a ledger row stamped with the conversation,
// run and anchor the context carries, and the pre-write snapshot lands under
// the conversation's own directory. A subagent resolves to its parent
// conversation, so its writes are recorded there.
func TestFileToolsRecordChangesForRewind(t *testing.T) {
	dir := t.TempDir()
	hist := filepath.Join(t.TempDir(), "history")
	a := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(a, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st := NewState(dir)
	st.SetPrimaryWorkspaceBoundary("", dir, nil)
	st.SetHistoryDir(hist)
	rec := &recordingFileChanges{}
	st.SetFileChangeRecorder(rec)

	ctx := WithConversationSessionID(context.Background(), "parent")
	ctx = llm.WithAgentSessionID(ctx, "worker")
	ctx = WithRunID(ctx, "run-1")
	ctx = WithTurnAnchorMessageID(ctx, 42)

	readTool, err := NewFileReadTool(st)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readTool.Handle(ctx, `{"file_path":"`+escapeJSONPath(a)+`"}`); err != nil {
		t.Fatalf("read_file: %v", err)
	}
	writeTool, err := NewFileWriteTool(st, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writeTool.Handle(ctx, `{"file_path":"`+escapeJSONPath(a)+`","content":"new\n"}`); err != nil {
		t.Fatalf("write_file overwrite: %v", err)
	}
	b := filepath.Join(dir, "b.txt")
	if _, err := writeTool.Handle(ctx, `{"file_path":"`+escapeJSONPath(b)+`","content":"b\n"}`); err != nil {
		t.Fatalf("write_file create: %v", err)
	}

	if len(rec.got) != 2 {
		t.Fatalf("recorded changes = %d, want 2", len(rec.got))
	}
	over := rec.got[0]
	if over.SessionID != "parent" || over.RunID != "run-1" || over.AnchorMessageID != 42 {
		t.Fatalf("overwrite change identity = %+v", over)
	}
	if !over.ExistedBefore || over.Deleted {
		t.Fatalf("overwrite change flags = %+v, want existed, not deleted", over)
	}
	if over.BeforeSnapshot == "" || !strings.Contains(over.BeforeSnapshot, string(filepath.Separator)+"parent"+string(filepath.Separator)) {
		t.Fatalf("overwrite snapshot %q is not under the conversation directory", over.BeforeSnapshot)
	}
	if raw, err := os.ReadFile(over.BeforeSnapshot); err != nil || string(raw) != "old\n" {
		t.Fatalf("overwrite snapshot content = %q, %v; want the pre-write bytes", raw, err)
	}
	created := rec.got[1]
	if created.ExistedBefore || created.BeforeSnapshot != "" {
		t.Fatalf("created-file change = %+v, want existed_before false and no snapshot", created)
	}
	if created.SessionID != "parent" {
		t.Fatalf("a subagent's write recorded under session %q, want the parent conversation", created.SessionID)
	}
}

// writeTestPNG writes a small, real PNG (the given size) to a temp file and
// returns its path and bytes.
func writeTestPNG(t *testing.T, dir string, w, h int) (string, []byte) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 40), G: uint8(y * 40), B: 0x20, A: 0xff})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	path := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write png: %v", err)
	}
	return path, buf.Bytes()
}

// TestAppendPDFPagePartDegradesUnsendablePage pins the read_file PDF sibling
// of the MCP image-block fix: a page whose part cannot be built (here 4 MiB of
// non-decodable bytes over the per-part budget) becomes a text line naming
// the stored PNG and the reason, so one bad page never costs the note or the
// pages that did encode.
func TestAppendPDFPagePartDegradesUnsendablePage(t *testing.T) {
	tmp := t.TempDir()
	_, goodPNG := writeTestPNG(t, tmp, 2, 2)

	var parts []llm.ContentPart
	parts = appendPDFPagePart(parts, filepath.Join(tmp, "page-1.png"), pdf.Page{Number: 1, PNG: goodPNG, Width: 2, Height: 2})
	if len(parts) != 1 || parts[0].Type != llm.ContentTypeImageBase64 {
		t.Fatalf("good page did not produce an image part: %#v", parts)
	}

	// Over the 3 MiB budget, so the downscale path runs and image.Decode
	// rejects the bytes — the decode-branch failure of ImagePartWithin.
	badPath := filepath.Join(tmp, "page-2.png")
	parts = appendPDFPagePart(parts, badPath, pdf.Page{Number: 2, PNG: bytes.Repeat([]byte{0x00}, 4<<20), Width: 64, Height: 64})
	if len(parts) != 2 {
		t.Fatalf("bad page dropped the good page's part: %#v", parts)
	}
	if parts[1].Type != llm.ContentTypeText {
		t.Fatalf("bad page did not degrade to a text part: %#v", parts)
	}
	if !strings.Contains(parts[1].Text, "was not sent as an image") || !strings.Contains(parts[1].Text, badPath) {
		t.Fatalf("degraded page line missing path/reason: %q", parts[1].Text)
	}
}

// TestFileReadReturnsImagePartsForImageFile pins that read_file hands the
// model a text note plus a real image part, and that the image part is the
// file's own bytes (base64, image/png).
func TestFileReadReturnsImagePartsForImageFile(t *testing.T) {
	tmp := t.TempDir()
	path, raw := writeTestPNG(t, tmp, 4, 4)
	tool, err := NewFileReadTool(NewState(tmp))
	if err != nil {
		t.Fatalf("NewFileReadTool: %v", err)
	}

	got, err := tool.Handle(context.Background(), `{"file_path":"`+escapeJSONPath(path)+`"}`)
	if err != nil {
		t.Fatalf("read image: %v", err)
	}
	parts, ok := got.([]llm.ContentPart)
	if !ok {
		t.Fatalf("read_file returned %T, want []llm.ContentPart", got)
	}
	if len(parts) != 2 {
		t.Fatalf("parts=%d want 2 (note + image)", len(parts))
	}
	if parts[0].Type != llm.ContentTypeText || !strings.Contains(parts[0].Text, "image 4x4") {
		t.Fatalf("first part = %#v, want a note naming the 4x4 image", parts[0])
	}
	if parts[1].Type != llm.ContentTypeImageBase64 || parts[1].MIMEType != "image/png" {
		t.Fatalf("image part = %#v, want image/png image_base64", parts[1])
	}
	decoded, err := base64.StdEncoding.DecodeString(parts[1].ImageBase64)
	if err != nil {
		t.Fatalf("decode image base64: %v", err)
	}
	if !bytes.Equal(decoded, raw) {
		t.Fatal("image part bytes differ from the file")
	}
}

// TestFileReadImageDoesNotEnterReadLedger pins that an image read is not
// remembered as text (nothing may then edit it) and does not reach the read
// observers (the language-server index is for source files).
func TestFileReadImageDoesNotEnterReadLedger(t *testing.T) {
	tmp := t.TempDir()
	path, _ := writeTestPNG(t, tmp, 4, 4)
	st := NewState(tmp)
	observed := false
	st.SetReadObserver(func(context.Context, string, []byte) { observed = true })
	tool, err := NewFileReadTool(st)
	if err != nil {
		t.Fatalf("NewFileReadTool: %v", err)
	}

	if _, err := tool.Handle(context.Background(), `{"file_path":"`+escapeJSONPath(path)+`"}`); err != nil {
		t.Fatalf("read image: %v", err)
	}
	if _, ok := st.GetReadState(path); ok {
		t.Fatal("an image read must not be remembered as read")
	}
	if observed {
		t.Fatal("an image read must not notify the read observer")
	}
}

// TestFileReadImageCapturesSurfaceOutputWithoutPixels pins the surface map: it
// carries the marker, dimensions and byte counts, and never base64.
func TestFileReadImageCapturesSurfaceOutputWithoutPixels(t *testing.T) {
	tmp := t.TempDir()
	path, _ := writeTestPNG(t, tmp, 8, 6)
	st := NewState(tmp)
	tool, err := NewFileReadTool(st)
	if err != nil {
		t.Fatalf("NewFileReadTool: %v", err)
	}
	ctx := WithToolCompletionCapture(context.Background())
	if _, err := tool.Handle(ctx, `{"file_path":"`+escapeJSONPath(path)+`"}`); err != nil {
		t.Fatalf("read image: %v", err)
	}
	completion, ok := ToolCompletionFromContext(ctx)
	if !ok {
		t.Fatal("expected tool completion capture")
	}
	if completion.Output["preview_kind"] != "image" {
		t.Fatalf("preview_kind=%#v want image", completion.Output["preview_kind"])
	}
	if completion.Output["image_width"] != 8 || completion.Output["image_height"] != 6 {
		t.Fatalf("dims = %vx%v want 8x6", completion.Output["image_width"], completion.Output["image_height"])
	}
	if n, _ := completion.Output["image_bytes"].(int); n <= 0 {
		t.Fatalf("image_bytes=%#v want > 0", completion.Output["image_bytes"])
	}
	encoded, err := json.Marshal(completion.Output)
	if err != nil {
		t.Fatalf("marshal output: %v", err)
	}
	if bytes.Contains(encoded, []byte("iVBOR")) {
		t.Fatal("surface output must not carry image base64")
	}
}

// TestFileReadUndecodableImageExtensionIsActionable pins that an image
// extension we cannot decode gets a one-step "convert it" error rather than a
// blind binary refusal.
func TestFileReadUndecodableImageExtensionIsActionable(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "old.bmp")
	if err := os.WriteFile(path, []byte("BM\x00\x00\x00\x00binary-ish"), 0o644); err != nil {
		t.Fatalf("write bmp: %v", err)
	}
	tool, err := NewFileReadTool(NewState(tmp))
	if err != nil {
		t.Fatalf("NewFileReadTool: %v", err)
	}

	_, err = tool.Handle(context.Background(), `{"file_path":"`+escapeJSONPath(path)+`"}`)
	if err == nil {
		t.Fatal("expected an actionable error for a .bmp read")
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "png") || !strings.Contains(msg, "convert") {
		t.Fatalf("err=%q want a convert-to-png hint", err)
	}
}

// TestFileReadRefusesImageWhenGateRefuses pins the image-input gate's effect
// on read_file: a session model that cannot take image input gets the
// actionable refusal sentence, not pixels, and a text read is untouched.
func TestFileReadRefusesImageWhenGateRefuses(t *testing.T) {
	tmp := t.TempDir()
	imgPath, _ := writeTestPNG(t, tmp, 4, 4)
	txtPath := filepath.Join(tmp, "note.txt")
	if err := os.WriteFile(txtPath, []byte("alpha\n"), 0o644); err != nil {
		t.Fatalf("write text: %v", err)
	}
	st := NewState(tmp)
	st.SetImageInputGate(func(context.Context) string {
		return "the session model text-only does not accept image input; switch model with /model, then read the image again"
	})
	tool, err := NewFileReadTool(st)
	if err != nil {
		t.Fatalf("NewFileReadTool: %v", err)
	}

	_, err = tool.Handle(context.Background(), `{"file_path":"`+escapeJSONPath(imgPath)+`"}`)
	if err == nil || !strings.Contains(err.Error(), "does not accept image input") {
		t.Fatalf("err=%v want the gate's refusal sentence", err)
	}

	if _, err := tool.Handle(context.Background(), `{"file_path":"`+escapeJSONPath(txtPath)+`"}`); err != nil {
		t.Fatalf("a text read must not be gated: %v", err)
	}
}

// readMCPResultFixture builds a State whose allowed roots are one project, with
// the primary agent workspace set as a distinct read-exempt boundary — the
// production shape of a project session (Step 5b). It writes one result file
// under the agent workspace's state/mcp-results and returns its path.
func readMCPResultFixture(t *testing.T, siblings []string) (st *State, resultsPath string, home string) {
	t.Helper()
	home = t.TempDir()
	agentWS := filepath.Join(home, "workspace")
	projectRoot := t.TempDir()
	resultsDir := filepath.Join(agentWS, "state", "mcp-results")
	if err := os.MkdirAll(resultsDir, 0o700); err != nil {
		t.Fatalf("mkdir results dir: %v", err)
	}
	resultsPath = filepath.Join(resultsDir, "mcp-shots-shot-1.json")
	if err := os.WriteFile(resultsPath, []byte("{\"ok\":true}\n"), 0o600); err != nil {
		t.Fatalf("write result file: %v", err)
	}
	st = NewState(projectRoot)
	st.SetPrimaryWorkspaceBoundary(home, agentWS, siblings)
	return st, resultsPath, home
}

// TestFileReadMCPResultsNeedsNoApproval pins Step 5b: an MCP tool's result file
// lives under the primary agent workspace's state/mcp-results, a read-exempt
// root, so the model reading back its own screenshot needs no approval — even
// though that directory sits outside the project's allowed roots. The approval
// hook is armed to fail the test if it is ever consulted.
func TestFileReadMCPResultsNeedsNoApproval(t *testing.T) {
	st, resultsPath, _ := readMCPResultFixture(t, nil)
	st.SetActionHook(func(context.Context, string, any) (string, bool, error) {
		t.Error("reading the agent workspace's own mcp-results must not ask for approval")
		return "", false, nil
	})
	fileRead, err := NewFileReadTool(st)
	if err != nil {
		t.Fatalf("NewFileReadTool: %v", err)
	}
	out, err := fileRead.Handle(context.Background(), `{"file_path":"`+escapeJSONPath(resultsPath)+`"}`)
	if err != nil {
		t.Fatalf("read mcp-results: %v", err)
	}
	if !strings.Contains(fmt.Sprint(out), "ok") {
		t.Fatalf("unexpected output %q", out)
	}
}

// TestFileReadStillAsksInsideHomeOutsideWorkspace proves Step 5b did not widen
// the FOREBRAIN_HOME protection: a path in home but NOT under the primary
// workspace (the credentials and the state database) still raises the approval
// action.
func TestFileReadStillAsksInsideHomeOutsideWorkspace(t *testing.T) {
	st, _, home := readMCPResultFixture(t, nil)
	st.SetActionHook(func(context.Context, string, any) (string, bool, error) {
		return "act-1", true, nil
	})
	fileRead, err := NewFileReadTool(st)
	if err != nil {
		t.Fatalf("NewFileReadTool: %v", err)
	}
	for _, rel := range []string{"auth.json", filepath.Join("state", "forebrain.state.sqlite")} {
		p := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(p, []byte("secret\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
		_, err := fileRead.Handle(context.Background(), `{"file_path":"`+escapeJSONPath(p)+`"}`)
		var req *RequiresActionError
		if !errors.As(err, &req) {
			t.Fatalf("%s: err = %v, want RequiresActionError", rel, err)
		}
	}
}

// TestFileReadMCPResultsNeedsNoApprovalInProjectSession is the machine-checkable
// proof of the load-bearing claim in Step 5b: a PROJECT session's active
// workspace is still the primary agent workspace, so an mcp-results file there
// is read-exempt even when the session also has its own project root and the
// boundary carries sibling workspaces. All three MCP scopes share this one
// directory, so this is what makes "global / primary / project results need no
// approval" true.
func TestFileReadMCPResultsNeedsNoApprovalInProjectSession(t *testing.T) {
	sibling := filepath.Join(t.TempDir(), "review-agent-workspace")
	st, resultsPath, _ := readMCPResultFixture(t, []string{sibling})
	st.SetActionHook(func(context.Context, string, any) (string, bool, error) {
		t.Error("a project session's own mcp-results under the primary workspace must not ask for approval")
		return "", false, nil
	})
	fileRead, err := NewFileReadTool(st)
	if err != nil {
		t.Fatalf("NewFileReadTool: %v", err)
	}
	if _, err := fileRead.Handle(context.Background(), `{"file_path":"`+escapeJSONPath(resultsPath)+`"}`); err != nil {
		t.Fatalf("read mcp-results in a project session: %v", err)
	}
}

// TestFileReadRendersPDFPages pins the PDF branch: a PDF is not read as text,
// it is rendered page by page and handed to the model the way an image is — a
// note naming the document and the pages shown, then one pixel part per page.
// The surface output reuses the image read's shape and its preview path points
// at a written PNG (never the PDF, which the image endpoint cannot serve).
func TestFileReadRendersPDFPages(t *testing.T) {
	tmp := t.TempDir()
	pdfPath := filepath.Join(tmp, "report.pdf")
	if err := os.WriteFile(pdfPath, newTestPDF(3), 0o644); err != nil {
		t.Fatalf("write pdf: %v", err)
	}
	st := NewState(tmp)
	resultDir := filepath.Join(tmp, "state", "tool-outputs")
	st.SetToolResultDir(resultDir)
	readTool, err := NewFileReadTool(st)
	if err != nil {
		t.Fatalf("NewFileReadTool: %v", err)
	}

	ctx := WithToolCompletionCapture(context.Background())
	got, err := readTool.Handle(ctx, `{"file_path":"`+escapeJSONPath(pdfPath)+`"}`)
	if err != nil {
		t.Fatalf("read pdf: %v", err)
	}
	parts, ok := got.([]llm.ContentPart)
	if !ok {
		t.Fatalf("read_file returned %T, want []llm.ContentPart", got)
	}
	if len(parts) != 4 {
		t.Fatalf("parts=%d want 4 (note + 3 pages)", len(parts))
	}
	note := parts[0]
	if note.Type != llm.ContentTypeText {
		t.Fatalf("first part = %#v, want a text note", note)
	}
	for _, want := range []string{pdfPath, "3 pages", "rendered pages 1-3"} {
		if !strings.Contains(note.Text, want) {
			t.Fatalf("note %q does not name %q", note.Text, want)
		}
	}
	for i := 1; i < len(parts); i++ {
		if parts[i].Type != llm.ContentTypeImageBase64 || parts[i].MIMEType != "image/png" {
			t.Fatalf("page part %d = %#v, want image/png image_base64", i, parts[i])
		}
	}

	completion, ok := ToolCompletionFromContext(ctx)
	if !ok {
		t.Fatal("expected tool completion capture")
	}
	if completion.Output["preview_kind"] != "image" {
		t.Fatalf("preview_kind=%#v want image", completion.Output["preview_kind"])
	}
	if got, _ := completion.Output["pdf_page_count"].(int); got != 3 {
		t.Fatalf("pdf_page_count=%#v want 3", completion.Output["pdf_page_count"])
	}
	if got, _ := completion.Output["pdf_rendered"].(int); got != 3 {
		t.Fatalf("pdf_rendered=%#v want 3", completion.Output["pdf_rendered"])
	}
	preview, _ := completion.Output["abs_path"].(string)
	if !strings.HasSuffix(preview, ".png") || preview == pdfPath {
		t.Fatalf("preview path %q must be a written PNG, not the PDF", preview)
	}
	if _, err := os.Stat(preview); err != nil {
		t.Fatalf("preview PNG was not written: %v", err)
	}
	encoded, err := json.Marshal(completion.Output)
	if err != nil {
		t.Fatalf("marshal output: %v", err)
	}
	if bytes.Contains(encoded, []byte("iVBOR")) {
		t.Fatal("surface output must not carry image base64")
	}
}

// TestFileReadPDFNamesThePagesItDidNotRender pins that a document longer than
// the render cap still reports its true length and says how many pages the
// model has not seen.
func TestFileReadPDFNamesThePagesItDidNotRender(t *testing.T) {
	tmp := t.TempDir()
	pdfPath := filepath.Join(tmp, "long.pdf")
	if err := os.WriteFile(pdfPath, newTestPDF(12), 0o644); err != nil {
		t.Fatalf("write pdf: %v", err)
	}
	st := NewState(tmp)
	st.SetToolResultDir(filepath.Join(tmp, "state", "tool-outputs"))
	readTool, err := NewFileReadTool(st)
	if err != nil {
		t.Fatalf("NewFileReadTool: %v", err)
	}

	ctx := WithToolCompletionCapture(context.Background())
	got, err := readTool.Handle(ctx, `{"file_path":"`+escapeJSONPath(pdfPath)+`"}`)
	if err != nil {
		t.Fatalf("read pdf: %v", err)
	}
	parts, ok := got.([]llm.ContentPart)
	if !ok {
		t.Fatalf("read_file returned %T, want []llm.ContentPart", got)
	}
	note := parts[0].Text
	for _, want := range []string{"12 pages", "rendered pages 1-5", "7 not rendered"} {
		if !strings.Contains(note, want) {
			t.Fatalf("note %q does not name %q", note, want)
		}
	}
	completion, _ := ToolCompletionFromContext(ctx)
	if got, _ := completion.Output["pdf_rendered"].(int); got != 5 {
		t.Fatalf("pdf_rendered=%#v want 5", completion.Output["pdf_rendered"])
	}
}

// TestFileReadPDFActionableErrors pins that a PDF the renderer cannot turn into
// pages fails with a message the model can act on, not a bare error code.
func TestFileReadPDFActionableErrors(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{name: "damaged", data: []byte("%PDF-1.7\nthis is not a real pdf body\n"), want: "could not be"},
		{name: "password", data: newEncryptedTestPDF("hunter2"), want: "password-protected"},
		{name: "no pages", data: newTestPDF(0), want: "no pages"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			pdfPath := filepath.Join(tmp, "file.pdf")
			if err := os.WriteFile(pdfPath, tc.data, 0o644); err != nil {
				t.Fatalf("write pdf: %v", err)
			}
			st := NewState(tmp)
			st.SetToolResultDir(filepath.Join(tmp, "state", "tool-outputs"))
			readTool, err := NewFileReadTool(st)
			if err != nil {
				t.Fatalf("NewFileReadTool: %v", err)
			}
			_, err = readTool.Handle(context.Background(), `{"file_path":"`+escapeJSONPath(pdfPath)+`"}`)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v want it to mention %q", err, tc.want)
			}
		})
	}
}

// TestFileReadRefusesPDFWhenGateRefuses pins that a session model which cannot
// take image input gets the gate's sentence for a PDF, and no page is rendered
// (the gate is consulted before the renderer runs).
func TestFileReadRefusesPDFWhenGateRefuses(t *testing.T) {
	tmp := t.TempDir()
	pdfPath := filepath.Join(tmp, "report.pdf")
	if err := os.WriteFile(pdfPath, newTestPDF(3), 0o644); err != nil {
		t.Fatalf("write pdf: %v", err)
	}
	st := NewState(tmp)
	resultDir := filepath.Join(tmp, "state", "tool-outputs")
	st.SetToolResultDir(resultDir)
	st.SetImageInputGate(func(context.Context) string {
		return "the session model text-only does not accept image input; switch model with /model, then read the file again"
	})
	readTool, err := NewFileReadTool(st)
	if err != nil {
		t.Fatalf("NewFileReadTool: %v", err)
	}

	_, err = readTool.Handle(context.Background(), `{"file_path":"`+escapeJSONPath(pdfPath)+`"}`)
	if err == nil || !strings.Contains(err.Error(), "does not accept image input") {
		t.Fatalf("err=%v want the gate's refusal sentence", err)
	}
	if pngs := countPNGs(t, resultDir); pngs != 0 {
		t.Fatalf("a gated PDF read must render nothing, but wrote %d PNG(s)", pngs)
	}
}

// The PDF page-range tests below pin the pages argument end to end: parse,
// cap-before-everything, renderer clamping, empty reads and the note wording
// the model navigates by. They all build their PDFs with newTestPDF so the
// rendered pages are distinguishable by their captions.

// readPDFWithPages is the shared driver: write pages PDF, read it with the
// given raw arguments, and return the parts, the note text and the completion.
func readPDFWithPages(t *testing.T, pageCount int, args string) ([]llm.ContentPart, string, ToolCompletionPayload) {
	t.Helper()
	tmp := t.TempDir()
	pdfPath := filepath.Join(tmp, "doc.pdf")
	if err := os.WriteFile(pdfPath, newTestPDF(pageCount), 0o644); err != nil {
		t.Fatalf("write pdf: %v", err)
	}
	st := NewState(tmp)
	st.SetToolResultDir(filepath.Join(tmp, "state", "tool-outputs"))
	readTool, err := NewFileReadTool(st)
	if err != nil {
		t.Fatalf("NewFileReadTool: %v", err)
	}
	ctx := WithToolCompletionCapture(context.Background())
	got, err := readTool.Handle(ctx, `{"file_path":"`+escapeJSONPath(pdfPath)+`"`+args+`}`)
	if err != nil {
		t.Fatalf("read pdf: %v", err)
	}
	parts, ok := got.([]llm.ContentPart)
	if !ok {
		t.Fatalf("read_file returned %T, want []llm.ContentPart", got)
	}
	if len(parts) == 0 || parts[0].Type != llm.ContentTypeText {
		t.Fatalf("first part = %#v, want the text note", parts[0])
	}
	completion, ok := ToolCompletionFromContext(ctx)
	if !ok {
		t.Fatal("expected tool completion capture")
	}
	return parts, parts[0].Text, completion
}

// TestFileReadPDFPagesRange pins an in-bounds range read: only the requested
// pages are rendered, the note names them, the surface output lists their real
// numbers, and the written PNGs are named after them.
func TestFileReadPDFPagesRange(t *testing.T) {
	parts, note, completion := readPDFWithPages(t, 12, `, "pages":"9-11"`)
	if !strings.Contains(note, "rendered pages 9-11") {
		t.Fatalf("note %q does not name rendered pages 9-11", note)
	}
	if len(parts) != 4 {
		t.Fatalf("parts=%d want 4 (note + 3 pages)", len(parts))
	}
	for i := 1; i < len(parts); i++ {
		if parts[i].Type != llm.ContentTypeImageBase64 || parts[i].MIMEType != "image/png" {
			t.Fatalf("page part %d = %#v, want image/png image_base64", i, parts[i])
		}
	}
	if got, _ := completion.Output["pdf_pages"].([]int); len(got) != 3 || got[0] != 9 || got[2] != 11 {
		t.Fatalf("pdf_pages=%v want [9 10 11]", got)
	}
	preview, _ := completion.Output["abs_path"].(string)
	dir := filepath.Dir(preview)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read result dir: %v", err)
	}
	foundP9 := false
	for _, e := range entries {
		if strings.Contains(e.Name(), "-p9.png") {
			foundP9 = true
		}
	}
	if !foundP9 {
		t.Fatalf("no PNG named -p9 was written under %s", dir)
	}
}

// TestFileReadPDFPagesSinglePage pins the one-page syntax: pages="7" renders
// exactly page 7.
func TestFileReadPDFPagesSinglePage(t *testing.T) {
	parts, note, _ := readPDFWithPages(t, 12, `, "pages":"7"`)
	if !strings.Contains(note, "rendered pages 7-7") {
		t.Fatalf("note %q does not name rendered pages 7-7", note)
	}
	if len(parts) != 2 {
		t.Fatalf("parts=%d want 2 (note + 1 page)", len(parts))
	}
}

// TestFileReadPDFPagesCapsAtFiveAndPointsToNext pins the iron rule: the cap
// applies before anything else — a whole-document request renders the first 5
// pages, and the note points at the range that continues the read.
func TestFileReadPDFPagesCapsAtFiveAndPointsToNext(t *testing.T) {
	t.Run("whole document", func(t *testing.T) {
		parts, note, completion := readPDFWithPages(t, 50, `, "pages":"1-50"`)
		if len(parts) != 6 {
			t.Fatalf("parts=%d want 6 (note + 5 pages)", len(parts))
		}
		if !strings.Contains(note, "rendered pages 1-5") || !strings.Contains(note, `pass pages="6-10"`) {
			t.Fatalf("note %q must name rendered pages 1-5 and the continuation range", note)
		}
		if got, _ := completion.Output["pdf_rendered"].(int); got != 5 {
			t.Fatalf("pdf_rendered=%d want 5", got)
		}
	})
	t.Run("range window", func(t *testing.T) {
		_, note, _ := readPDFWithPages(t, 12, `, "pages":"2-9"`)
		if !strings.Contains(note, "rendered pages 2-6") || !strings.Contains(note, `pass pages="7-11"`) {
			t.Fatalf("note %q must name rendered pages 2-6 and the continuation range", note)
		}
	})
}

// TestFileReadPDFPagesClampsEndToLastPage pins that an end past the last page
// is clamped silently — the model asked for pages that exist and got them, so
// the note carries no explanation suffix at all.
func TestFileReadPDFPagesClampsEndToLastPage(t *testing.T) {
	parts, note, _ := readPDFWithPages(t, 12, `, "pages":"10-20"`)
	if !strings.Contains(note, "rendered pages 10-12") {
		t.Fatalf("note %q does not name rendered pages 10-12", note)
	}
	for _, banned := range []string{"ends at", "do not exist", "not rendered", "capped"} {
		if strings.Contains(note, banned) {
			t.Fatalf("note %q must not carry a suffix about %q", note, banned)
		}
	}
	if len(parts) != 4 {
		t.Fatalf("parts=%d want 4 (note + 3 pages)", len(parts))
	}
}

// TestFileReadPDFPagesBeyondEndIsEmptyRead pins the empty read: a start past
// the last page renders nothing, writes nothing, and hands back the note plus
// a completion map with no image preview keys — the pixel dereferences happen
// only on the has-pages path. This mirrors the text path's "No content at
// offset N: file has M line(s).".
func TestFileReadPDFPagesBeyondEndIsEmptyRead(t *testing.T) {
	tmp := t.TempDir()
	pdfPath := filepath.Join(tmp, "doc.pdf")
	if err := os.WriteFile(pdfPath, newTestPDF(12), 0o644); err != nil {
		t.Fatalf("write pdf: %v", err)
	}
	st := NewState(tmp)
	resultDir := filepath.Join(tmp, "state", "tool-outputs")
	st.SetToolResultDir(resultDir)
	readTool, err := NewFileReadTool(st)
	if err != nil {
		t.Fatalf("NewFileReadTool: %v", err)
	}

	ctx := WithToolCompletionCapture(context.Background())
	got, err := readTool.Handle(ctx, `{"file_path":"`+escapeJSONPath(pdfPath)+`", "pages":"20-24"}`)
	if err != nil {
		t.Fatalf("read pdf: %v", err)
	}
	parts, ok := got.([]llm.ContentPart)
	if !ok {
		t.Fatalf("read_file returned %T, want []llm.ContentPart", got)
	}
	if len(parts) != 1 {
		t.Fatalf("parts=%d want exactly 1 (the note, no images)", len(parts))
	}
	note := parts[0].Text
	for _, want := range []string{"no pages at 20-24", "the document has 12 pages"} {
		if !strings.Contains(note, want) {
			t.Fatalf("note %q does not name %q", note, want)
		}
	}
	if pngs := countPNGs(t, resultDir); pngs != 0 {
		t.Fatalf("an empty read must write no PNG, but wrote %d", pngs)
	}
	completion, _ := ToolCompletionFromContext(ctx)
	if got, _ := completion.Output["pdf_rendered"].(int); got != 0 {
		t.Fatalf("pdf_rendered=%d want 0", got)
	}
	if pages, _ := completion.Output["pdf_pages"].([]int); len(pages) != 0 {
		t.Fatalf("pdf_pages=%v want empty", pages)
	}
	preview, _ := completion.Output["preview_text"].(string)
	if preview != note {
		t.Fatalf("preview_text=%q must exactly match returned note %q", preview, note)
	}
	for _, banned := range []string{"preview_kind", "abs_path"} {
		if _, ok := completion.Output[banned]; ok {
			t.Fatalf("empty read output must not carry %q", banned)
		}
	}
	for key := range completion.Output {
		if strings.HasPrefix(key, "image_") {
			t.Fatalf("empty read output must not carry image key %q", key)
		}
	}
}

// TestFileReadPDFPagesRejectsBadSpec pins that a malformed pages value is an
// actionable error naming the syntax, never a silent default.
func TestFileReadPDFPagesRejectsBadSpec(t *testing.T) {
	for _, spec := range []string{"5-2", "abc", "0", "+7", "7-", "-3", "0-3"} {
		t.Run(spec, func(t *testing.T) {
			tmp := t.TempDir()
			pdfPath := filepath.Join(tmp, "doc.pdf")
			if err := os.WriteFile(pdfPath, newTestPDF(12), 0o644); err != nil {
				t.Fatalf("write pdf: %v", err)
			}
			st := NewState(tmp)
			st.SetToolResultDir(filepath.Join(tmp, "state", "tool-outputs"))
			readTool, err := NewFileReadTool(st)
			if err != nil {
				t.Fatalf("NewFileReadTool: %v", err)
			}
			_, err = readTool.Handle(context.Background(), `{"file_path":"`+escapeJSONPath(pdfPath)+`", "pages":"`+spec+`"}`)
			if err == nil || !strings.Contains(err.Error(), "pages must be") {
				t.Fatalf("pages=%q err=%v, want an error naming the pages syntax", spec, err)
			}
		})
	}
}

// TestFileReadTextIgnoresPages pins that the text path never looks at pages:
// passing it alongside a text read changes nothing.
func TestFileReadTextIgnoresPages(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "a.txt")
	if err := os.WriteFile(p, []byte("alpha\nbeta\ngamma\n"), 0o644); err != nil {
		t.Fatalf("write text: %v", err)
	}
	st := NewState(tmp)
	readTool, err := NewFileReadTool(st)
	if err != nil {
		t.Fatalf("NewFileReadTool: %v", err)
	}
	plainCtx := WithToolCompletionCapture(context.Background())
	plain, err := readTool.Handle(plainCtx, `{"file_path":"`+escapeJSONPath(p)+`"}`)
	if err != nil {
		t.Fatalf("read without pages: %v", err)
	}
	pagesCtx := WithToolCompletionCapture(context.Background())
	withPages, err := readTool.Handle(pagesCtx, `{"file_path":"`+escapeJSONPath(p)+`", "pages":"3-5"}`)
	if err != nil {
		t.Fatalf("read with pages: %v", err)
	}
	if plain != withPages {
		t.Fatalf("text read changed by pages:\nplain=%v\nwithPages=%v", plain, withPages)
	}
	plainDone, _ := ToolCompletionFromContext(plainCtx)
	pagesDone, _ := ToolCompletionFromContext(pagesCtx)
	if !reflect.DeepEqual(plainDone.Output, pagesDone.Output) {
		t.Fatalf("text output map changed by pages:\nplain=%v\nwithPages=%v", plainDone.Output, pagesDone.Output)
	}
}

// countPNGs counts the .png files under dir, treating a missing dir as zero.
func countPNGs(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	count := 0
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".png") {
			count++
		}
	}
	return count
}

// The PDF builders below are this package's own: pkg/pdf writes its fixtures in
// its own tests, and a committed binary fixture is reserved for pkg/pdf/testdata
// (the only exception in the repository). They produce small, valid PDFs so the
// tool-layer tests can render real pages without depending on those fixtures.

// newTestPDF builds an unencrypted PDF with the given number of pages, each
// carrying a short Helvetica caption.
func newTestPDF(pages int) []byte {
	var objects []string
	add := func(body string) int {
		objects = append(objects, body)
		return len(objects)
	}
	catalog := add("")
	pagesNum := add("")
	font := add("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	var kids []string
	for i := 1; i <= pages; i++ {
		content := fmt.Sprintf("BT /F1 24 Tf 72 700 Td (Page %d) Tj ET", i)
		contentNum := add(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content))
		pageNum := add(fmt.Sprintf(
			"<< /Type /Page /Parent %d 0 R /MediaBox [0 0 595 842] /Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R >>",
			pagesNum, font, contentNum))
		kids = append(kids, fmt.Sprintf("%d 0 R", pageNum))
	}
	objects[catalog-1] = fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pagesNum)
	objects[pagesNum-1] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), pages)
	return assembleTestPDF(objects, fmt.Sprintf("%d 0 R", catalog), "")
}

// assembleTestPDF serialises objects (1-based) into a PDF, with extra appended
// to the trailer dictionary.
func assembleTestPDF(objects []string, root, extra string) []byte {
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n")
	offsets := make([]int, len(objects)+1)
	for i, body := range objects {
		offsets[i+1] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", i+1, body)
	}
	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n", len(objects)+1)
	buf.WriteString("0000000000 65535 f \n")
	for i := 1; i <= len(objects); i++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root %s%s >>\nstartxref\n%d\n%%%%EOF\n",
		len(objects)+1, root, extra, xref)
	return buf.Bytes()
}

// testPasswordPad is the fixed 32-byte string the standard security handler
// appends to a password (PDF 1.7, algorithm 2).
var testPasswordPad = []byte{
	0x28, 0xBF, 0x4E, 0x5E, 0x4E, 0x75, 0x8A, 0x41, 0x64, 0x00, 0x4E, 0x56, 0xFF, 0xFA, 0x01, 0x08,
	0x2E, 0x2E, 0x00, 0xB6, 0xD0, 0x68, 0x3E, 0x80, 0x2F, 0x0C, 0xA9, 0xFE, 0x64, 0x53, 0x69, 0x7A,
}

// newEncryptedTestPDF builds a one-page PDF protected by the standard security
// handler, revision 2 (40-bit RC4), with the given user password, so opening it
// without a password fails.
func newEncryptedTestPDF(userPassword string) []byte {
	const ownerPassword = "forebrain-owner"
	permissions := int32(-3904)
	id0 := []byte("0123456789abcdef")

	paddedUser := testPadPassword(userPassword)
	paddedOwner := testPadPassword(ownerPassword)

	ownerKey := testMD5Sum(paddedOwner)[:5]
	o := testRC4Crypt(ownerKey, paddedUser)

	h := md5.New()
	h.Write(paddedUser)
	h.Write(o)
	var pbuf [4]byte
	binary.LittleEndian.PutUint32(pbuf[:], uint32(permissions))
	h.Write(pbuf[:])
	h.Write(id0)
	fileKey := h.Sum(nil)[:5]

	u := testRC4Crypt(fileKey, testPasswordPad)

	var objects []string
	add := func(body string) int {
		objects = append(objects, body)
		return len(objects)
	}
	catalog := add("")
	pagesNum := add("")
	font := add("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	content := "BT /F1 24 Tf 72 700 Td (Secret) Tj ET"
	// The content stream is encrypted with this object's key.
	contentNum := len(objects) + 1
	encrypted := testRC4Crypt(testObjectKey(fileKey, contentNum, 0), []byte(content))
	add(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(encrypted), encrypted))
	pageNum := add(fmt.Sprintf(
		"<< /Type /Page /Parent %d 0 R /MediaBox [0 0 595 842] /Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R >>",
		pagesNum, font, contentNum))
	objects[catalog-1] = fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pagesNum)
	objects[pagesNum-1] = fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", pageNum)

	encrypt := add(fmt.Sprintf(
		"<< /Filter /Standard /V 1 /R 2 /Length 40 /O <%s> /U <%s> /P %d >>",
		hex.EncodeToString(o), hex.EncodeToString(u), permissions))

	extra := fmt.Sprintf(" /Encrypt %d 0 R /ID [<%s> <%s>]",
		encrypt, hex.EncodeToString(id0), hex.EncodeToString(id0))
	return assembleTestPDF(objects, fmt.Sprintf("%d 0 R", catalog), extra)
}

func testPadPassword(password string) []byte {
	out := make([]byte, 32)
	n := copy(out, password)
	copy(out[n:], testPasswordPad)
	return out
}

func testObjectKey(fileKey []byte, num, gen int) []byte {
	h := md5.New()
	h.Write(fileKey)
	h.Write([]byte{byte(num), byte(num >> 8), byte(num >> 16), byte(gen), byte(gen >> 8)})
	limit := len(fileKey) + 5
	if limit > 16 {
		limit = 16
	}
	return h.Sum(nil)[:limit]
}

func testMD5Sum(data []byte) []byte {
	sum := md5.Sum(data)
	return sum[:]
}

func testRC4Crypt(key, data []byte) []byte {
	cipher, err := rc4.NewCipher(key)
	if err != nil {
		panic(err)
	}
	out := make([]byte, len(data))
	cipher.XORKeyStream(out, data)
	return out
}
