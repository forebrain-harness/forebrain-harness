package tool

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitRepoFixture is a repository with one commit, its path resolved the way
// git prints it. The user's own git configuration — a global ignore file that
// already lists .worktrees among it — is kept out of the answer.
func gitRepoFixture(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "init", "-q")
	gitIn(t, repo, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "init")
	return repo
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// forgetIgnored lets a test call EnsureWorktreesIgnored again for a directory
// an earlier call in this process already handled.
func forgetIgnored(t *testing.T, dirs ...string) {
	t.Cleanup(func() {
		for _, dir := range dirs {
			worktreesIgnored.Delete(filepath.Clean(dir))
		}
	})
	for _, dir := range dirs {
		worktreesIgnored.Delete(filepath.Clean(dir))
	}
}

// TestMainWorktreeRootIsTheSameFromEveryCheckout pins where worktrees go: the
// main worktree's .worktrees, whether the agent asks from the main checkout, a
// subdirectory of it or a linked worktree — never a worktree inside another.
func TestMainWorktreeRootIsTheSameFromEveryCheckout(t *testing.T) {
	repo := gitRepoFixture(t)
	sub := filepath.Join(repo, "pkg", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(repo, WorktreesDirName, "feature")
	gitIn(t, repo, "worktree", "add", "-q", "--detach", linked, "HEAD")
	ctx := context.Background()
	for _, dir := range []string{repo, sub, linked} {
		if got := MainWorktreeRoot(ctx, dir); got != repo {
			t.Errorf("MainWorktreeRoot(%s) = %q, want %q", dir, got, repo)
		}
		if got, want := WorktreesDir(ctx, dir), filepath.Join(repo, WorktreesDirName); got != want {
			t.Errorf("WorktreesDir(%s) = %q, want %q", dir, got, want)
		}
	}
	if got := MainWorktreeRoot(ctx, t.TempDir()); got != "" {
		t.Errorf("MainWorktreeRoot outside a repository = %q, want none", got)
	}
	if got := MainWorktreeRoot(ctx, ""); got != "" {
		t.Errorf("MainWorktreeRoot(\"\") = %q, want none", got)
	}
}

func TestMainWorktreeFromListSkipsBareRepositories(t *testing.T) {
	for name, tc := range map[string]struct{ list, want string }{
		"main checkout": {
			list: "worktree /src/app\nHEAD abc\nbranch refs/heads/main\n\nworktree /src/app/.worktrees/x\nHEAD abc\ndetached\n\n",
			want: "/src/app",
		},
		"bare repository": {
			list: "worktree /src/app.git\nbare\n\nworktree /src/app-x\nHEAD abc\ndetached\n\n",
			want: "",
		},
		"no trailing blank line": {list: "worktree /src/app\nHEAD abc", want: "/src/app"},
		"empty":                  {list: "", want: ""},
	} {
		if got := mainWorktreeFromList(tc.list); got != filepath.FromSlash(tc.want) {
			t.Errorf("%s: mainWorktreeFromList = %q, want %q", name, got, tc.want)
		}
	}
}

// TestEnsureWorktreesIgnoredWritesTheExcludeOnce checks the runtime keeps
// .worktrees out of git status without touching a tracked file: one exclude
// line, written once however often it is asked, and a worktree added there
// leaves the tree clean.
func TestEnsureWorktreesIgnoredWritesTheExcludeOnce(t *testing.T) {
	repo := gitRepoFixture(t)
	forgetIgnored(t, repo)
	ctx := context.Background()
	EnsureWorktreesIgnored(ctx, repo)
	forgetIgnored(t, repo)
	EnsureWorktreesIgnored(ctx, repo)

	exclude, err := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(exclude), "\n"+worktreesExcludePattern+"\n"); n != 1 {
		t.Fatalf("exclude carries %d %q lines, want 1:\n%s", n, worktreesExcludePattern, exclude)
	}
	if _, err := os.Stat(filepath.Join(repo, ".gitignore")); !os.IsNotExist(err) {
		t.Errorf("a .gitignore was written: %v", err)
	}
	gitIn(t, repo, "worktree", "add", "-q", "--detach", filepath.Join(repo, WorktreesDirName, "feature"), "HEAD")
	if status := strings.TrimSpace(gitIn(t, repo, "status", "--porcelain")); status != "" {
		t.Errorf("git status shows the worktrees directory: %q", status)
	}
}

// TestEnsureWorktreesIgnoredLeavesAnIgnoringRepositoryAlone checks a
// repository whose own .gitignore already covers .worktrees gets no exclude
// line, and that a linked worktree's call lands in the shared exclude file.
func TestEnsureWorktreesIgnoredLeavesAnIgnoringRepositoryAlone(t *testing.T) {
	repo := gitRepoFixture(t)
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(".worktrees/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	forgetIgnored(t, repo)
	EnsureWorktreesIgnored(context.Background(), repo)
	exclude, _ := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude"))
	if strings.Contains(string(exclude), worktreesExcludePattern) {
		t.Errorf("an exclude line was added though .gitignore covers it:\n%s", exclude)
	}

	other := gitRepoFixture(t)
	linked := filepath.Join(other, WorktreesDirName, "feature")
	gitIn(t, other, "worktree", "add", "-q", "--detach", linked, "HEAD")
	forgetIgnored(t, linked)
	EnsureWorktreesIgnored(context.Background(), linked)
	exclude, _ = os.ReadFile(filepath.Join(other, ".git", "info", "exclude"))
	if !strings.Contains(string(exclude), "\n"+worktreesExcludePattern+"\n") {
		t.Errorf("a linked worktree's call did not reach the repository's exclude:\n%s", exclude)
	}
	if status := strings.TrimSpace(gitIn(t, other, "status", "--porcelain")); status != "" {
		t.Errorf("git status shows the worktrees directory: %q", status)
	}
}

func TestEnsureWorktreesIgnoredOutsideARepositoryWritesNothing(t *testing.T) {
	dir := t.TempDir()
	forgetIgnored(t, dir)
	EnsureWorktreesIgnored(context.Background(), dir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("a plain directory gained %d entries", len(entries))
	}
}

func TestAppendExcludePatternKeepsTheFileIntact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "info", "exclude")
	if err := appendExcludePattern(path, worktreesExcludePattern); err != nil {
		t.Fatalf("creating the file: %v", err)
	}
	if err := os.WriteFile(path, []byte("*.log"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := appendExcludePattern(path, worktreesExcludePattern); err != nil {
		t.Fatal(err)
	}
	if err := appendExcludePattern(path, worktreesExcludePattern); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	want := "*.log\n# forebrain: git worktrees agents create\n" + worktreesExcludePattern + "\n"
	if string(got) != want {
		t.Errorf("exclude = %q, want %q", got, want)
	}
}

// TestIsLinkedWorktreeTopKnowsALinkedWorktreeFromASubmodule checks the one
// fact the peer guards build the tree boundary on: a linked worktree's top
// counts; the main checkout, a subdirectory, a submodule and a plain directory
// do not.
func TestIsLinkedWorktreeTopKnowsALinkedWorktreeFromASubmodule(t *testing.T) {
	repo := gitRepoFixture(t)
	linked := filepath.Join(repo, WorktreesDirName, "feature")
	gitIn(t, repo, "worktree", "add", "-q", "--detach", linked, "HEAD")
	// A submodule's .git file names <common dir>/modules/<name>, which holds
	// no commondir.
	submodule := filepath.Join(repo, "vendor", "lib")
	if err := os.MkdirAll(filepath.Join(repo, ".git", "modules", "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(submodule, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(submodule, ".git"), []byte("gitdir: ../../.git/modules/lib\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for dir, want := range map[string]bool{
		linked:                       true,
		repo:                         false,
		filepath.Join(linked, "pkg"): false,
		submodule:                    false,
		t.TempDir():                  false,
	} {
		if got := IsLinkedWorktreeTop(dir); got != want {
			t.Errorf("IsLinkedWorktreeTop(%s) = %v, want %v", dir, got, want)
		}
	}
}

// TestCreateWorktreeChecksOutANewBranchUnderWorktrees pins what a worktree
// dispatch asks git for (owner 2026-11-10): the one directory worktrees live
// in, a new branch named after the worktree, checked out from HEAD, and the
// resolved path the dispatch reports.
func TestCreateWorktreeChecksOutANewBranchUnderWorktrees(t *testing.T) {
	repo := gitRepoFixture(t)
	ctx := context.Background()
	dir, err := CreateWorktree(ctx, repo, "demo-exec")
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	if want := filepath.Join(repo, WorktreesDirName, "demo-exec"); dir != want {
		t.Fatalf("CreateWorktree = %q, want %q", dir, want)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("the worktree directory does not exist: %v", err)
	}
	if !IsLinkedWorktreeTop(dir) {
		t.Fatalf("%s is not a linked worktree top", dir)
	}
	if branch := strings.TrimSpace(gitIn(t, dir, "rev-parse", "--abbrev-ref", "HEAD")); branch != "demo-exec" {
		t.Fatalf("checked-out branch = %q, want demo-exec", branch)
	}
	if got := MainWorktreeRoot(ctx, dir); got != repo {
		t.Fatalf("MainWorktreeRoot(%s) = %q, want the repository %q", dir, got, repo)
	}
}

// TestCreateWorktreeRefusesATakenName pins that a dispatch never reuses or
// clears what is already there: a directory the name already owns, and a
// branch git already has, both refuse instead of checking out over them.
func TestCreateWorktreeRefusesATakenName(t *testing.T) {
	repo := gitRepoFixture(t)
	ctx := context.Background()
	if _, err := CreateWorktree(ctx, repo, "taken"); err != nil {
		t.Fatalf("first CreateWorktree: %v", err)
	}
	if _, err := CreateWorktree(ctx, repo, "taken"); err == nil {
		t.Fatal("a worktree directory that already exists must be refused")
	}
	gitIn(t, repo, "branch", "branch-only")
	if _, err := CreateWorktree(ctx, repo, "branch-only"); err == nil {
		t.Fatal("a branch that already exists must be refused")
	} else if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("the branch refusal must carry git's own words, got %v", err)
	}
}

// TestCreateWorktreeNeedsARepository pins the refusal a directory outside a
// repository gets: there is no main worktree to put a worktree in.
func TestCreateWorktreeNeedsARepository(t *testing.T) {
	if _, err := CreateWorktree(context.Background(), t.TempDir(), "demo"); err == nil {
		t.Fatal("CreateWorktree outside a repository must fail")
	}
}

// TestLinkedWorktreeTopFindsTheInnermostWorktree pins the path-to-tree answer
// the write ledger records: a file inside a linked worktree names that
// worktree, the innermost one when worktrees are nested, and a path in the
// main checkout or outside a repository names none.
func TestLinkedWorktreeTopFindsTheInnermostWorktree(t *testing.T) {
	repo := gitRepoFixture(t)
	linked := filepath.Join(repo, WorktreesDirName, "feature")
	gitIn(t, repo, "worktree", "add", "-q", "--detach", linked, "HEAD")
	inner := filepath.Join(linked, WorktreesDirName, "inner")
	gitIn(t, linked, "worktree", "add", "-q", "--detach", inner, "HEAD")
	if err := os.MkdirAll(filepath.Join(linked, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		filepath.Join(linked, "pkg", "a.go"): linked,
		filepath.Join(inner, "pkg", "a.go"):  inner,
		filepath.Join(linked, "a.go"):        linked,
		filepath.Join(repo, "pkg", "a.go"):   "",
		filepath.Join(repo, "go.mod"):        "",
		t.TempDir():                          "",
	} {
		if got := LinkedWorktreeTop(path); got != want {
			t.Errorf("LinkedWorktreeTop(%s) = %q, want %q", path, got, want)
		}
	}
}
