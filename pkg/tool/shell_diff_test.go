package tool

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// newShellDiffRepo builds a committed scratch repository and returns its
// resolved work tree root. The registry entry is dropped when the test ends.
func newShellDiffRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	gitRun := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	gitRun("init", "-q")
	gitRun("config", "user.email", "t@t")
	gitRun("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a\nb\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("ignored/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun("add", "-A")
	gitRun("commit", "-qm", "init")
	top := strings.TrimSpace(gitOutput(t, root, "rev-parse", "--show-toplevel"))
	t.Cleanup(func() { forgetShellDiffRepo(top) })
	return top
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// applyShellDiffFixtureChanges makes the change set the capture tests run under
// their begin/finish pair: modify a.txt, create new.py, create an empty file,
// and write into the ignored directory.
func applyShellDiffFixtureChanges(t *testing.T, root string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a\nB\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "new.py"), []byte("print()\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "empty.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "ignored"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ignored", "x"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func shellDiffFileByBase(diff *ShellEditDiff, base string) *ShellEditDiffFile {
	for i := range diff.Files {
		if filepath.Base(diff.Files[i].Path) == base {
			return &diff.Files[i]
		}
	}
	return nil
}

func TestShellEditDiffCapturesModifyCreateDelete(t *testing.T) {
	root := newShellDiffRepo(t)
	ctx := context.Background()
	run := beginShellEditDiff(ctx, "sed -i s/b/B/ a.txt", root, false)
	if run == nil {
		t.Fatal("beginShellEditDiff returned nil in a git work tree")
	}
	applyShellDiffFixtureChanges(t, root)
	diff := run.finish(ctx, "sed -i s/b/B/ a.txt", 0, nil)
	if diff == nil {
		t.Fatal("finish returned nil")
	}
	if len(diff.Files) != 3 {
		t.Fatalf("files = %+v, want 3 entries", diff.Files)
	}
	// The empty created file comes first and carries no hunks.
	empty := diff.Files[0]
	if filepath.Base(empty.Path) != "empty.txt" || empty.Status != "created" || empty.UnifiedDiff != "" {
		t.Fatalf("first file = %+v, want empty.txt created without hunks", empty)
	}
	if empty.DisplayPath != "empty.txt" || !filepath.IsAbs(empty.Path) {
		t.Fatalf("empty file paths: abs=%q display=%q", empty.Path, empty.DisplayPath)
	}
	names := []string{filepath.Base(diff.Files[1].Path), filepath.Base(diff.Files[2].Path)}
	if !reflect.DeepEqual(names, []string{"a.txt", "new.py"}) {
		t.Fatalf("remaining files = %v", names)
	}
	a := shellDiffFileByBase(diff, "a.txt")
	if a == nil || a.Status != "updated" || !strings.Contains(a.UnifiedDiff, "+B") {
		t.Fatalf("a.txt entry = %+v", a)
	}
	n := shellDiffFileByBase(diff, "new.py")
	if n == nil || n.Status != "created" || !strings.Contains(n.UnifiedDiff, "+print()") {
		t.Fatalf("new.py entry = %+v", n)
	}
	for _, f := range diff.Files {
		if strings.Contains(f.Path, "ignored") {
			t.Fatalf("ignored file reported: %+v", f)
		}
	}
	if len(diff.ChangedFiles) != 3 || !filepath.IsAbs(diff.ChangedFiles[0]) {
		t.Fatalf("changed files = %#v", diff.ChangedFiles)
	}
	if diff.MoreFiles != 0 || diff.Unavailable || diff.Skipped || diff.Shared || diff.TreeMoved {
		t.Fatalf("unexpected diff flags: %+v", diff)
	}
}

func TestShellEditDiffLeavesTheUserRepositoryAlone(t *testing.T) {
	root := newShellDiffRepo(t)
	gitDir := strings.TrimSpace(gitOutput(t, root, "rev-parse", "--absolute-git-dir"))
	// Warm the stat cache first so the control readings are stable.
	gitOutput(t, root, "status", "--porcelain")
	countObjects := func() string {
		out := gitOutput(t, root, "count-objects", "-v")
		var b strings.Builder
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(line, "count:") || strings.HasPrefix(line, "in-pack:") {
				b.WriteString(line + "\n")
			}
		}
		return b.String()
	}
	readIndex := func() []byte {
		data, err := os.ReadFile(filepath.Join(gitDir, "index"))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	objectsBefore := countObjects()
	indexBefore := readIndex()

	ctx := context.Background()
	run := beginShellEditDiff(ctx, "make changes", root, false)
	if run == nil {
		t.Fatal("beginShellEditDiff returned nil")
	}
	applyShellDiffFixtureChanges(t, root)
	if diff := run.finish(ctx, "make changes", 0, nil); diff == nil || len(diff.Files) == 0 {
		t.Fatalf("finish = %+v, want a diff", diff)
	}

	if objectsAfter := countObjects(); objectsAfter != objectsBefore {
		t.Fatalf("user repository objects changed:\nbefore:\n%safter:\n%s", objectsBefore, objectsAfter)
	}
	if indexAfter := readIndex(); !bytes.Equal(indexBefore, indexAfter) {
		t.Fatal("the user's .git/index was modified by the snapshot")
	}
	watched := gitOutput(t, root, "status", "--porcelain")
	control := newShellDiffRepo(t)
	applyShellDiffFixtureChanges(t, control)
	if direct := gitOutput(t, control, "status", "--porcelain"); watched != direct {
		t.Fatalf("status differs from a repository that never saw a snapshot:\nwatched:\n%sdirect:\n%s", watched, direct)
	}
}

func TestShellEditDiffNotAGitRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	run := beginShellEditDiff(context.Background(), "echo hi > a.txt", t.TempDir(), false)
	if run != nil {
		t.Fatalf("beginShellEditDiff outside a git work tree = %+v, want nil", run)
	}
}

func TestShellEditDiffReadOnlyAndFailedCommands(t *testing.T) {
	root := newShellDiffRepo(t)
	ctx := context.Background()
	if run := beginShellEditDiff(ctx, "cat a.txt", root, true); run != nil {
		t.Fatalf("read-only command snapshotted: %+v", run)
	}
	run := beginShellEditDiff(ctx, "make", root, false)
	if run == nil {
		t.Fatal("beginShellEditDiff returned nil")
	}
	if diff := run.finish(ctx, "make", 1, nil); diff != nil {
		t.Fatalf("failed command produced a diff: %+v", diff)
	}
	// grep's exit 1 means "no match", not failure.
	run = beginShellEditDiff(ctx, "grep b a.txt", root, false)
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a\nc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if diff := run.finish(ctx, "grep b a.txt", 1, nil); diff == nil || len(diff.Files) == 0 {
		t.Fatalf("grep exit 1 must still diff, got %+v", diff)
	}
	run = beginShellEditDiff(ctx, "git diff", root, false)
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a\nd\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if diff := run.finish(ctx, "git diff", 1, nil); diff == nil || len(diff.Files) == 0 {
		t.Fatalf("git diff exit 1 must still diff, got %+v", diff)
	}
	cancelCtx, cancel := context.WithCancel(context.Background())
	run = beginShellEditDiff(cancelCtx, "echo hi > x.txt", root, false)
	cancel()
	if diff := run.finish(cancelCtx, "echo hi > x.txt", 0, nil); diff != nil {
		t.Fatalf("canceled context produced a diff: %+v", diff)
	}
}

func TestShellEditDiffSkipsGitTreeMoves(t *testing.T) {
	root := newShellDiffRepo(t)
	ctx := context.Background()
	run := beginShellEditDiff(ctx, "git checkout main", root, false)
	if run == nil {
		t.Fatal("beginShellEditDiff returned nil")
	}
	if !run.skipped {
		t.Fatal("git checkout alone must be a skipped run")
	}
	diff := run.finish(ctx, "git checkout main", 0, nil)
	if diff == nil || !diff.Skipped || len(diff.Files) != 0 {
		t.Fatalf("git checkout alone = %+v, want skipped", diff)
	}
	// A compound command still diffs; it just says the tree may have moved.
	run = beginShellEditDiff(ctx, "make && git pull", root, false)
	if run == nil {
		t.Fatal("beginShellEditDiff returned nil")
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a\nC\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	diff = run.finish(ctx, "make && git pull", 0, nil)
	if diff == nil || !diff.TreeMoved || len(diff.Files) == 0 {
		t.Fatalf("make && git pull = %+v, want TreeMoved with files", diff)
	}
}

func TestShellEditDiffMoreFilesAndLimits(t *testing.T) {
	root := newShellDiffRepo(t)
	t.Cleanup(func() {
		shellDiffMaxFiles = 5
		shellDiffMaxHunkLines = 400
	})
	shellDiffMaxFiles = 2
	shellDiffMaxHunkLines = 5
	ctx := context.Background()
	run := beginShellEditDiff(ctx, "touch f1 f2 f3 f4", root, false)
	if run == nil {
		t.Fatal("beginShellEditDiff returned nil")
	}
	for _, name := range []string{"f1.txt", "f2.txt", "f3.txt", "f4.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// f2 grows past the shrunken hunk limit and must only be counted.
	growth := &strings.Builder{}
	for i := 0; i < 10; i++ {
		growth.WriteString("extra\n")
	}
	if err := os.WriteFile(filepath.Join(root, "f2.txt"), []byte("1\n"+growth.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	diff := run.finish(ctx, "touch f1 f2 f3 f4", 0, nil)
	if diff == nil {
		t.Fatal("finish returned nil")
	}
	if len(diff.Files) != 2 {
		t.Fatalf("files = %d (%+v), want 2", len(diff.Files), diff.Files)
	}
	if diff.MoreFiles != 2 {
		t.Fatalf("MoreFiles = %d, want 2", diff.MoreFiles)
	}
	// A binary file is never selected, only counted.
	run = beginShellEditDiff(ctx, "write bin.dat", root, false)
	if run == nil {
		t.Fatal("beginShellEditDiff returned nil")
	}
	if err := os.WriteFile(filepath.Join(root, "bin.dat"), []byte("a\x00b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	diff = run.finish(ctx, "write bin.dat", 0, nil)
	if diff == nil {
		t.Fatal("finish returned nil")
	}
	if f := shellDiffFileByBase(diff, "bin.dat"); f != nil {
		t.Fatalf("binary file selected: %+v", f)
	}
	if diff.MoreFiles < 1 {
		t.Fatalf("binary file not counted: %+v", diff)
	}
}

func TestShellEditDiffLargeFilesAreUnavailable(t *testing.T) {
	root := newShellDiffRepo(t)
	t.Cleanup(func() { shellDiffMaxHashBytes = int64(20_000_000) })
	shellDiffMaxHashBytes = 10
	ctx := context.Background()
	run := beginShellEditDiff(ctx, "write big.bin", root, false)
	if run == nil {
		t.Fatal("beginShellEditDiff returned nil")
	}
	if err := os.WriteFile(filepath.Join(root, "big.bin"), make([]byte, 20), 0o644); err != nil {
		t.Fatal(err)
	}
	diff := run.finish(ctx, "write big.bin", 0, nil)
	if diff == nil || !diff.Unavailable {
		t.Fatalf("large file diff = %+v, want unavailable", diff)
	}
	if diff.MoreFiles < 1 {
		t.Fatalf("large file not counted: %+v", diff)
	}
	if len(diff.Files) != 0 {
		t.Fatalf("large file selected: %+v", diff.Files)
	}
}

func TestShellEditDiffShared(t *testing.T) {
	root := newShellDiffRepo(t)
	ctx := context.Background()
	a := beginShellEditDiff(ctx, "cmd-a", root, false)
	b := beginShellEditDiff(ctx, "cmd-b", root, false)
	if a == nil || b == nil {
		t.Fatal("beginShellEditDiff returned nil")
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a\nZ\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	da := a.finish(ctx, "cmd-a", 0, nil)
	db := b.finish(ctx, "cmd-b", 0, nil)
	if da == nil || !da.Shared {
		t.Fatalf("first overlapping run = %+v, want Shared", da)
	}
	if db == nil || !db.Shared {
		t.Fatalf("second overlapping run = %+v, want Shared", db)
	}
}

func TestShellEditDiffGivesUpAfterTwoFailures(t *testing.T) {
	root := newShellDiffRepo(t)
	ctx := context.Background()
	run := beginShellEditDiff(ctx, "echo x", root, false)
	if run == nil {
		t.Fatal("beginShellEditDiff returned nil")
	}
	run.finish(ctx, "echo x", 0, nil) // no changes: nil is fine, the repo is registered now
	repo := shellDiffRepoFor(ctx, root)
	if repo == nil {
		t.Fatal("repository not registered")
	}
	t.Cleanup(func() { shellDiffGit = "git" })
	shellDiffGit = "forebrain-test-missing-git"
	repo.mu.Lock()
	_, err1 := repo.snapshot(ctx)
	_, err2 := repo.snapshot(ctx)
	repo.mu.Unlock()
	if err1 == nil || err2 == nil {
		t.Fatalf("snapshots with a missing git = %v, %v; want errors", err1, err2)
	}
	if repo.private != "" {
		t.Fatal("private directory must be removed after a failure")
	}
	if repo.failures != shellDiffGiveUpAfter {
		t.Fatalf("failures = %d, want %d", repo.failures, shellDiffGiveUpAfter)
	}
	shellDiffGit = "git"
	if run := beginShellEditDiff(ctx, "echo x", root, false); run != nil {
		t.Fatalf("repository must be given up for this process, got %+v", run)
	}
}

// The private repository holds a copy of the user's index; a process that ran one
// shell command must not leave it behind for the 24-hour sweep.
func TestCloseShellDiffReposRemovesThePrivateDirectories(t *testing.T) {
	ctx := context.Background()
	root := newShellDiffRepo(t)
	repo := shellDiffRepoFor(ctx, root)
	if repo == nil {
		t.Fatal("repository not registered")
	}
	repo.mu.Lock()
	_, err := repo.snapshot(ctx)
	repo.mu.Unlock()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if strings.TrimSpace(repo.private) == "" {
		t.Fatal("a snapshot must set up the private repository")
	}
	if _, err := os.Stat(repo.private); err != nil {
		t.Fatalf("the private directory must exist before the release: %v", err)
	}

	CloseShellDiffRepos()

	if _, err := os.Stat(repo.private); !os.IsNotExist(err) {
		t.Fatalf("the private directory must be gone after the release, stat err = %v", err)
	}
	if _, ok := shellDiffRepos.m[root]; ok {
		t.Fatal("the registry entry must be dropped as well")
	}
	CloseShellDiffRepos() // safe to call more than once
}

func TestShellExitIsFailure(t *testing.T) {
	cases := []struct {
		command string
		code    int
		want    bool
	}{
		{"grep x f", 0, false},
		{"grep x f", 1, false},
		{"grep x f", 2, true},
		{"rg x", 1, false},
		{"egrep x f", 2, true},
		{"fgrep x f", 1, false},
		{"find . -name x", 1, false},
		{"find . -name x", 2, true},
		{"diff a b", 1, false},
		{"diff a b", 2, true},
		{"test -f x", 1, false},
		{"[ -f x ]", 1, false},
		{"git diff", 1, false},
		{"git diff", 2, true},
		{"git -C repo diff", 1, false},
		{"git -c a=b grep foo", 1, false},
		{"git status", 1, true},
		{"git commit -m x", 1, true},
		{"echo hi && grep x f", 1, false},
		{"grep x f && echo hi", 1, true},
		{"make", 2, true},
		{"ls", 0, false},
		{"ls", 1, true},
		{"", 0, false},
	}
	for _, tc := range cases {
		if got := shellExitIsFailure(tc.command, tc.code); got != tc.want {
			t.Errorf("shellExitIsFailure(%q, %d) = %v, want %v", tc.command, tc.code, got, tc.want)
		}
	}
}

func TestShellCommandMovesGitTree(t *testing.T) {
	cases := []struct {
		command string
		alone   bool
		moves   bool
	}{
		{"git checkout main", true, true},
		{"git   stash", true, true},
		{"git checkout --orphan branch2", false, true},
		{"sudo git reset --hard", false, true},
		{"git cherry-pick abc123", true, true},
		{"git revert HEAD", true, true},
		{"git status", false, false},
		{"git log --oneline", false, false},
		{"git commit -m x", false, false},
		{"git diff", false, false},
		{"make && git pull", false, true},
		{"git pull || true", false, true},
		{"cd sub && git switch other", false, true},
		{"echo hi", false, false},
		{"git checkout -- file || echo restored", false, true},
		{"GIT_DIR=x git checkout y", false, false},
	}
	for _, tc := range cases {
		if got := shellCommandMovesGitTreeAlone(tc.command); got != tc.alone {
			t.Errorf("shellCommandMovesGitTreeAlone(%q) = %v, want %v", tc.command, got, tc.alone)
		}
		if got := shellCommandMovesGitTree(tc.command); got != tc.moves {
			t.Errorf("shellCommandMovesGitTree(%q) = %v, want %v", tc.command, got, tc.moves)
		}
	}
}

func TestHookToolResponse(t *testing.T) {
	// A non-shell result passes through unchanged: the same map pointer, the
	// same string.
	sentinel := map[string]any{"stdout": "hi"}
	if got := HookToolResponse(context.Background(), "read_file", sentinel); reflect.ValueOf(got).Pointer() != reflect.ValueOf(sentinel).Pointer() {
		t.Fatalf("non-shell map result = %#v", got)
	}
	if got := HookToolResponse(context.Background(), "read_file", "ok"); got != "ok" {
		t.Fatalf("non-shell string result = %#v", got)
	}

	// Shell: the encoded result object the model was given decodes to an
	// object carrying the command's diff from the completion capture.
	ctx := WithToolCompletionCapture(context.Background())
	CaptureToolOutput(ctx, map[string]any{"shell_edit_diff": ShellEditDiff{
		Files:        []ShellEditDiffFile{{Path: "/abs/a.txt", Status: "updated"}},
		ChangedFiles: []string{"/abs/a.txt"},
	}})
	got := HookToolResponse(ctx, "shell", `{"stdout":"hi","exit_code":0}`)
	obj, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("shell result = %#v, want an object", got)
	}
	if obj["stdout"] != "hi" {
		t.Fatalf("stdout=%#v", obj["stdout"])
	}
	diff, ok := DecodeShellEditDiff(obj["shell_edit_diff"])
	if !ok || !reflect.DeepEqual(diff.ChangedFiles, []string{"/abs/a.txt"}) {
		t.Fatalf("shell_edit_diff=%#v", obj["shell_edit_diff"])
	}

	// Shell with a capture that holds no diff: the object, without the key.
	plainCtx := WithToolCompletionCapture(context.Background())
	CaptureToolOutput(plainCtx, map[string]any{"stdout": "hi"})
	got = HookToolResponse(plainCtx, "shell", `{"stdout":"hi","exit_code":0}`)
	obj, ok = got.(map[string]any)
	if !ok {
		t.Fatalf("shell result = %#v, want an object", got)
	}
	if _, exists := obj["shell_edit_diff"]; exists {
		t.Fatalf("shell_edit_diff=%#v, want absent", obj["shell_edit_diff"])
	}

	// Shell result that is not JSON: handed over unchanged.
	if got := HookToolResponse(context.Background(), "shell", "plain output"); got != "plain output" {
		t.Fatalf("non-JSON shell result = %#v", got)
	}
}
