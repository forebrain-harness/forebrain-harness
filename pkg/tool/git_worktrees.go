package tool

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// WorktreesDirName is the one directory every git worktree an agent creates
// lives in: <main worktree>/.worktrees/<name>. Left to choose, agents scattered
// worktrees over /tmp, $TMPDIR, scratch directories, sibling directories and
// folders of the checkout, and the ones under /tmp disappeared under the OS
// cleaner while git still listed them.
//
// Inside the checkout is where the workspace sandbox already lets the shell and
// the file tools write, so working in a worktree asks for no approval; a dot
// directory is skipped by `go ./...`, ripgrep and the other tools that walk the
// project; and EnsureWorktreesIgnored keeps it out of git status. The Claude
// Code layout, .claude/worktrees, has no counterpart here: the sandbox denies
// every command write under .forebrain, where project instructions and skills
// live.
const WorktreesDirName = ".worktrees"

// worktreesExcludePattern is the info/exclude line that ignores the worktrees
// directory at the top of every worktree of the repository.
const worktreesExcludePattern = "/" + WorktreesDirName + "/"

// worktreeGitTimeout bounds each git call made to locate or ignore the
// worktrees directory. Missing that is always better than a stalled load.
const worktreeGitTimeout = 2 * time.Second

// MainWorktreeRoot is the main worktree of the repository dir belongs to: the
// first entry `git worktree list` prints, which is the same from the main
// checkout, any subdirectory of it and any linked worktree. It is "" outside a
// repository, for a bare repository (which has no main worktree), and when git
// does not answer in time.
func MainWorktreeRoot(ctx context.Context, dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return ""
	}
	out, err := shellDiffRun(ctx, shellDiffDiscoveryEnv(), worktreeGitTimeout, nil, "-C", dir, "worktree", "list", "--porcelain")
	if err != nil {
		return ""
	}
	return mainWorktreeFromList(out)
}

// mainWorktreeFromList reads the first record of `git worktree list
// --porcelain`. Records are separated by a blank line; a bare repository's
// record carries a "bare" line and no checkout.
func mainWorktreeFromList(out string) string {
	root := ""
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		switch {
		case line == "":
			return root
		case line == "bare":
			return ""
		case root == "" && strings.HasPrefix(line, "worktree "):
			root = filepath.Clean(strings.TrimPrefix(line, "worktree "))
		}
	}
	return root
}

// WorktreesDir is <main worktree>/.worktrees for the repository dir belongs
// to, "" when MainWorktreeRoot has no answer.
func WorktreesDir(ctx context.Context, dir string) string {
	mainRoot := MainWorktreeRoot(ctx, dir)
	if mainRoot == "" {
		return ""
	}
	return filepath.Join(mainRoot, WorktreesDirName)
}

// IsLinkedWorktreeTop reports whether dir is the top of a linked git
// worktree: its .git is a file naming an administrative directory that holds a
// commondir file, which git writes for linked worktrees only. A main
// checkout's .git is a directory and a submodule's names a directory without
// commondir, so neither counts. It reads two small files and runs no git.
func IsLinkedWorktreeTop(dir string) bool {
	dotGit := filepath.Join(dir, ".git")
	info, err := os.Lstat(dotGit)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 {
		return false
	}
	data, err := os.ReadFile(dotGit)
	if err != nil {
		return false
	}
	line, _, _ := strings.Cut(string(data), "\n")
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(strings.ToLower(line), "gitdir:") {
		return false
	}
	gitDir := strings.TrimSpace(line[len("gitdir:"):])
	if gitDir == "" {
		return false
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(dir, gitDir)
	}
	commondir, err := os.Stat(filepath.Join(gitDir, "commondir"))
	return err == nil && commondir.Mode().IsRegular()
}

// worktreesIgnored remembers the directories EnsureWorktreesIgnored has
// already handled in this process, so reloading a runner costs no git calls.
var worktreesIgnored sync.Map

// EnsureWorktreesIgnored keeps the worktrees directory of the repository dir
// belongs to out of git status. When no ignore rule covers .worktrees/ yet,
// "/.worktrees/" is appended to the repository's info/exclude, which is local
// to this clone and never a tracked file. It is best effort and runs once per
// directory per process: a directory outside a repository, a bare repository
// or a file it cannot write is left as it is.
func EnsureWorktreesIgnored(ctx context.Context, dir string) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return
	}
	if _, done := worktreesIgnored.LoadOrStore(filepath.Clean(dir), struct{}{}); done {
		return
	}
	mainRoot := MainWorktreeRoot(ctx, dir)
	if mainRoot == "" {
		return
	}
	env := shellDiffDiscoveryEnv()
	// check-ignore exits 0 only for an ignored path. The trailing slash makes
	// a directory rule match before the directory exists, and --no-index asks
	// the ignore rules alone, without reading the index.
	if _, err := shellDiffRun(ctx, env, worktreeGitTimeout, nil, "-C", mainRoot, "check-ignore", "-q", "--no-index", WorktreesDirName+"/"); err == nil {
		return
	}
	common, err := shellDiffRun(ctx, env, worktreeGitTimeout, nil, "-C", mainRoot, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil || strings.TrimSpace(common) == "" {
		return
	}
	_ = appendExcludePattern(filepath.Join(filepath.Clean(strings.TrimSpace(common)), "info", "exclude"), worktreesExcludePattern)
}

// appendExcludePattern adds pattern to an info/exclude file unless a line of
// it already reads exactly that, creating the file when git has not.
func appendExcludePattern(path, pattern string) error {
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, line := range strings.Split(string(existing), "\n") {
		if strings.TrimSpace(line) == pattern {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	text := "# forebrain: git worktrees agents create\n" + pattern + "\n"
	if len(existing) > 0 && existing[len(existing)-1] != '\n' {
		text = "\n" + text
	}
	if _, err := f.WriteString(text); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
