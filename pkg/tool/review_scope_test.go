package tool

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
)

func TestIsTestFilePathFollowsTestRunnerConventions(t *testing.T) {
	for rel, want := range map[string]bool{
		"pkg/llm/blank_output_test.go":            true,
		"pkg/llm/blank_output.go":                 false,
		"frontend/src/lib/api.test.ts":            true,
		"frontend/e2e/project-types.spec.ts":      true,
		"frontend/src/views/ProjectsView.vue":     false,
		"scripts/test_repro.py":                   true,
		"scripts/repro_test.py":                   true,
		"scripts/repro.py":                        false,
		"spec/models/user_spec.rb":                true,
		"src/main/java/app/OrderService.java":     false,
		"src/test/java/app/OrderServiceTest.java": true,
		"app/OrderServiceTests.cs":                true,
		"tests/integration.rs":                    true,
		"pkg/agent/testdata/new_case.json":        true,
		"__tests__/render.js":                     true,
		"node_modules/lib/test/index.js":          false,
		"vendor/x/y_test.go":                      false,
		".git/hooks/pre-commit_test.go":           false,
		"../outside_test.go":                      false,
		"Makefile":                                false,
	} {
		if got := isTestFilePath(rel); got != want {
			t.Errorf("isTestFilePath(%q) = %v, want %v", rel, got, want)
		}
	}
}

// reviewFixture is a project with one production file and one existing test,
// and a scope over it with its own scratch directory and a worktree directory
// under the project's .worktrees.
func reviewFixture(t *testing.T) (project, scratch string, scope *ReviewWriteScope) {
	t.Helper()
	project = t.TempDir()
	scratch = t.TempDir()
	worktree := filepath.Join(project, WorktreesDirName, "plan-review-1")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"pkg/a.go", "pkg/a_test.go"} {
		path := filepath.Join(project, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("package pkg\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return project, scratch, NewReviewWriteScope(project, scratch, filepath.Join(project, ".git", "worktrees"), worktree)
}

// TestReviewWriteScopeAdmitsOnlyNewTestsAndScratch pins the whole write set of
// a coding review: its scratch directory, its worktree directory (a worktree's
// production files included), new test files in the project and its own files
// after that — never production code, never a test that existed before, never
// another worktree, never anywhere else.
func TestReviewWriteScopeAdmitsOnlyNewTestsAndScratch(t *testing.T) {
	project, scratch, scope := reviewFixture(t)
	admitted := []string{
		filepath.Join(scratch, "repro.sh"),
		filepath.Join(scope.Worktree(), "pkg", "a.go"),
		filepath.Join(project, "pkg", "repro_test.go"),
		// Its own file stays writable on a second write.
		filepath.Join(project, "pkg", "repro_test.go"),
	}
	for _, path := range admitted {
		if err := scope.admitWrite(path); err != nil {
			t.Errorf("admitWrite(%s) = %v, want admitted", path, err)
		}
	}
	refused := map[string]string{
		filepath.Join(project, "pkg", "a.go"):                                   "not a test file",
		filepath.Join(project, "pkg", "b.go"):                                   "not a test file",
		filepath.Join(project, "pkg", "a_test.go"):                              "already exists",
		filepath.Join(project, WorktreesDirName, "someone-else", "pkg", "a.go"): "not a test file",
		filepath.Join(t.TempDir(), "elsewhere.sh"):                              "outside the project",
	}
	for path, reason := range refused {
		err := scope.admitWrite(path)
		if err == nil || !strings.Contains(err.Error(), reason) {
			t.Errorf("admitWrite(%s) = %v, want a refusal saying %q", path, err, reason)
		}
	}
}

// TestGuardWriteAppliesTheReviewScope checks the scope reaches the one gate
// every file write passes — write_file, edit_file and apply_patch alike.
func TestGuardWriteAppliesTheReviewScope(t *testing.T) {
	project, scratch, scope := reviewFixture(t)
	st := NewState(project)
	ctx := WithReviewWriteScope(WithSubagentType(context.Background(), "plan-reviewer"), scope)
	if err := st.GuardWrite(ctx, filepath.Join(project, "pkg", "a.go")); err == nil {
		t.Fatal("a review wrote production code")
	}
	if err := st.GuardWrite(ctx, filepath.Join(project, "pkg", "new_test.go")); err != nil {
		t.Fatalf("a review could not add a test: %v", err)
	}
	if err := st.GuardWrite(ctx, filepath.Join(scratch, "probe.py")); err != nil {
		t.Fatalf("a review could not write its scratch directory: %v", err)
	}
	// Without a scope nothing changes for anyone else.
	if err := st.GuardWrite(context.Background(), filepath.Join(project, "pkg", "a.go")); err != nil {
		t.Fatalf("an ordinary write was refused: %v", err)
	}
}

// TestReviewScopeLiftsOnlyTheFileWritingRefusals checks the static table keeps
// the reviewer read-only unless its run carries a scope, and that the scope
// lifts exactly write_file and edit_file — the plan-mode tools stay refused.
func TestReviewScopeLiftsOnlyTheFileWritingRefusals(t *testing.T) {
	st := NewState(t.TempDir())
	reviewer := WithSubagentType(context.Background(), "plan-reviewer")
	for _, name := range []string{"write_file", "edit_file"} {
		if err := st.GuardTool(reviewer, name); err == nil {
			t.Errorf("a reviewer without a scope may call %s", name)
		}
	}
	_, _, scope := reviewFixture(t)
	scoped := WithReviewWriteScope(reviewer, scope)
	for _, name := range []string{"write_file", "edit_file"} {
		if err := st.GuardTool(scoped, name); err != nil {
			t.Errorf("a scoped reviewer may not call %s: %v", name, err)
		}
	}
	for _, name := range []string{"enter_plan_mode", "exit_plan_mode"} {
		if err := st.GuardTool(scoped, name); err == nil {
			t.Errorf("the scope also lifted %s", name)
		}
	}
	all := []string{"read_file", "shell", "write_file", "edit_file", "exit_plan_mode"}
	if got := VisibleToolsForSubagentRun(reviewer, "plan-reviewer", all); containsTool(got, "write_file") || containsTool(got, "edit_file") {
		t.Errorf("an unscoped reviewer sees the writing tools: %v", got)
	}
	got := VisibleToolsForSubagentRun(scoped, "plan-reviewer", all)
	if !containsTool(got, "write_file") || !containsTool(got, "edit_file") || containsTool(got, "exit_plan_mode") {
		t.Errorf("scoped reviewer tools = %v, want the writing tools and no plan-mode tool", got)
	}
	// The type still counts as one that cannot write freely: it keeps running
	// in agent mode and cannot ask for write grants.
	if !TypedSubagentBlocksFileWrites("plan-reviewer") {
		t.Error("plan-reviewer stopped counting as write-free")
	}
}

// TestReleaseKeepsOnlyWhatTheReviewerWrote checks a finished review's
// scratch: temporary files go, a directory nothing was written to goes, and
// anything the reviewer did write stays.
func TestReleaseKeepsOnlyWhatTheReviewerWrote(t *testing.T) {
	base := t.TempDir()
	empty := filepath.Join(base, "empty")
	if err := os.MkdirAll(filepath.Join(empty, "tmp", "go-build1"), 0o755); err != nil {
		t.Fatal(err)
	}
	unusedTree := filepath.Join(base, WorktreesDirName, "plan-review-1")
	if err := os.MkdirAll(unusedTree, 0o755); err != nil {
		t.Fatal(err)
	}
	NewReviewWriteScope(base, empty, filepath.Join(base, ".git", "worktrees"), unusedTree).Release()
	if _, err := os.Stat(empty); !os.IsNotExist(err) {
		t.Errorf("an unused scratch directory survived: %v", err)
	}
	if _, err := os.Stat(unusedTree); !os.IsNotExist(err) {
		t.Errorf("an unused worktree directory survived: %v", err)
	}
	used := filepath.Join(base, "used")
	if err := os.MkdirAll(filepath.Join(used, "tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(used, "repro.sh")
	if err := os.WriteFile(script, []byte("echo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	usedTree := filepath.Join(base, WorktreesDirName, "plan-review-2")
	if err := os.MkdirAll(usedTree, 0o755); err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Join(usedTree, "main.go")
	if err := os.WriteFile(checkout, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	NewReviewWriteScope(base, used, filepath.Join(base, ".git", "worktrees"), usedTree).Release()
	if _, err := os.Stat(script); err != nil {
		t.Errorf("the reviewer's script was removed: %v", err)
	}
	if _, err := os.Stat(checkout); err != nil {
		t.Errorf("the reviewer's worktree was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(used, "tmp")); !os.IsNotExist(err) {
		t.Errorf("tmp/ survived: %v", err)
	}
}

func TestPlanReviewerShellIsReadOnly(t *testing.T) {
	profile, elevated := shellSandboxProfile(WithSubagentType(context.Background(), "plan-reviewer"))
	if profile != safety.ProfileReadOnly || elevated {
		t.Fatalf("plan-reviewer shell profile = %v elevated = %v, want read-only", profile, elevated)
	}
}

// TestPlanReviewShellWritesOnlyItsScratchAndWorktree runs real sandboxed
// commands as a coding plan reviewer: the project refuses a write, the scratch
// directory takes one, `git worktree add` registers a worktree in the review's
// directory under the project's .worktrees — which git status does not show —
// the worktree's production files are free to change, and temporary files land
// in the scratch directory.
func TestPlanReviewShellWritesOnlyItsScratchAndWorktree(t *testing.T) {
	if testing.Short() {
		t.Skip("runs real sandboxed commands")
	}
	if !safety.SandboxAvailable() {
		t.Skip("the boundary needs a sandbox; this machine has none")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"add", "main.go"},
		{"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "init"},
	} {
		cmd := exec.Command("git", append([]string{"-C", project}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	scope, err := PrepareReviewWriteScope(context.Background(), project)
	if err != nil {
		t.Fatalf("PrepareReviewWriteScope: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(scope.Scratch()) })
	if scope.GitWorktrees() == "" {
		t.Fatal("a git project got no worktrees registry")
	}
	resolvedProject, err := filepath.EvalSymlinks(project)
	if err != nil {
		t.Fatal(err)
	}
	worktree := scope.Worktree()
	if filepath.Dir(worktree) != filepath.Join(resolvedProject, WorktreesDirName) {
		t.Fatalf("review worktree %q is not under the project's %s", worktree, WorktreesDirName)
	}

	st := NewState(project)
	shellTool, err := NewShellTool(st, &AgentToolRuntime{Home: t.TempDir(), WorkspaceRoot: project, Cfg: &appcfg.Root{}})
	if err != nil {
		t.Fatalf("NewShellTool: %v", err)
	}
	ctx := WithProjectRoot(context.Background(), project)
	ctx = WithSubagentType(ctx, "plan-reviewer")
	ctx = WithReviewWriteScope(ctx, scope)
	// ok is whether the command ran and exited 0; a sandbox denial comes back
	// as the tool's error.
	run := func(command string) (stdout string, ok bool) {
		raw, _ := json.Marshal(map[string]any{"command": command})
		out, handleErr := shellTool.Handle(ctx, string(raw))
		body, _ := out.(string)
		var parsed struct {
			Stdout   string `json:"stdout"`
			ExitCode int    `json:"exit_code"`
		}
		if json.Unmarshal([]byte(body), &parsed) != nil {
			return "", false
		}
		return parsed.Stdout, handleErr == nil && parsed.ExitCode == 0
	}

	if _, ok := run("echo changed > main.go"); ok {
		t.Error("the reviewer's shell wrote the project")
	}
	if body, _ := os.ReadFile(filepath.Join(project, "main.go")); string(body) != "package main\n" {
		t.Fatalf("project file changed: %q", body)
	}
	if _, ok := run("echo ok > " + filepath.Join(scope.Scratch(), "probe.txt")); !ok {
		t.Error("the reviewer's shell could not write its scratch directory")
	}
	if _, ok := run("git worktree add --detach " + worktree + " HEAD"); !ok {
		t.Fatal("the reviewer could not add a worktree")
	}
	t.Cleanup(func() { _ = exec.Command("git", "-C", project, "worktree", "remove", "--force", worktree).Run() })
	if status, err := exec.Command("git", "-C", project, "status", "--porcelain").Output(); err != nil || strings.TrimSpace(string(status)) != "" {
		t.Errorf("git status after adding the review worktree = %q, %v; want a clean tree", status, err)
	}
	if _, ok := run("echo changed > " + filepath.Join(worktree, "main.go")); !ok {
		t.Error("the reviewer could not change production code inside its worktree")
	}
	if stdout, _ := run(`printf '%s' "$TMPDIR"`); stdout != filepath.Join(scope.Scratch(), "tmp") {
		t.Errorf("TMPDIR = %q, want the scratch tmp directory", stdout)
	}
}
