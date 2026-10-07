package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
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

func TestFileReadRejectsPDFWithSpecificMessage(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "paper.pdf")
	if err := os.WriteFile(p, []byte("%PDF-1.7\n"), 0o644); err != nil {
		t.Fatalf("write pdf: %v", err)
	}
	tool, err := NewFileReadTool(NewState(tmp))
	if err != nil {
		t.Fatalf("NewFileReadTool: %v", err)
	}

	_, err = tool.Handle(context.Background(), `{"file_path":"`+escapeJSONPath(p)+`"}`)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "pdf") {
		t.Fatalf("err=%v want pdf refusal", err)
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
