package tool

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ProjectDiff is the project's uncommitted work: a staged change and an
// unstaged one against the last commit, and a file git does not track yet.
// Paths narrow it.
func TestProjectDiffShowsAllUncommittedWork(t *testing.T) {
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q")
	writeFile(t, filepath.Join(dir, "staged.go"), "package a\n")
	writeFile(t, filepath.Join(dir, "edited.go"), "package a\n")
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "base")
	writeFile(t, filepath.Join(dir, "staged.go"), "package a\n\nvar Staged = 1\n")
	gitIn(t, dir, "add", "staged.go")
	writeFile(t, filepath.Join(dir, "edited.go"), "package a\n\nvar Edited = 1\n")
	writeFile(t, filepath.Join(dir, "docs", "new.md"), "# new\n")

	diff, err := ProjectDiff(dir, nil)
	if err != nil {
		t.Fatalf("ProjectDiff: %v", err)
	}
	for _, want := range []string{"+var Staged = 1", "+var Edited = 1", "+# new"} {
		if !strings.Contains(diff, want) {
			t.Fatalf("diff misses %q:\n%s", want, diff)
		}
	}
	narrowed, err := ProjectDiff(dir, []string{"docs"})
	if err != nil || !strings.Contains(narrowed, "+# new") || strings.Contains(narrowed, "Staged") {
		t.Fatalf("diff of docs = %q, %v", narrowed, err)
	}
}

// A repository with no commit yet compares with its index.
func TestProjectDiffBeforeTheFirstCommit(t *testing.T) {
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q")
	writeFile(t, filepath.Join(dir, "a.txt"), "hello\n")
	diff, err := ProjectDiff(dir, nil)
	if err != nil || !strings.Contains(diff, "+hello") {
		t.Fatalf("diff = %q, %v", diff, err)
	}
}

func TestProjectDiffOutsideRepository(t *testing.T) {
	if _, err := ProjectDiff(t.TempDir(), nil); !errors.Is(err, ErrNotGitRepository) {
		t.Fatalf("err = %v, want ErrNotGitRepository", err)
	}
}

// The helpers below were production functions that only the tests in this
// package ever called: each is a thin composition of live code. They live
// here so the production files carry no unused code while the tests keep
// exercising the live functions underneath.

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
