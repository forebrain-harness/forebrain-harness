package memory

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	phase2WorkspaceDiffFile = "phase2_workspace_diff.md"
	maxWorkspaceDiffBytes   = 4 << 20
)

func prepareMemoryWorkspace(ctx context.Context, root string) error {
	root = strings.TrimSpace(root)
	if root == "" {
		return fmt.Errorf("memory workspace root empty")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("create memory workspace: %w", err)
	}
	if err := removeWorkspaceDiff(root); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); err == nil {
		if _, err := runGit(ctx, root, "rev-parse", "--verify", "HEAD"); err == nil {
			return nil
		}
	}
	return resetGitBaseline(ctx, root, time.Time{})
}

// resetGitBaseline rebuilds the baseline every future diff is taken against.
//
// Extension inputs — ad-hoc notes above all — that appeared after snapshot are
// deliberately left out of the commit. The pass that is finishing consolidated
// the diff it took at snapshot and never saw them, so committing them here
// would bury them in the baseline and no later pass would ever show them to the
// consolidation agent. Left untracked, they surface in the next pass's diff.
// A zero snapshot excludes nothing, which is what a first-time baseline wants.
func resetGitBaseline(ctx context.Context, root string, snapshot time.Time) error {
	gitPath := filepath.Join(root, ".git")
	if info, err := os.Lstat(gitPath); err == nil {
		if info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
			if err := os.RemoveAll(gitPath); err != nil {
				return fmt.Errorf("remove memory git metadata: %w", err)
			}
		} else if err := os.Remove(gitPath); err != nil {
			return fmt.Errorf("remove memory git metadata: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect memory git metadata: %w", err)
	}
	if _, err := runGit(ctx, root, "init", "-q"); err != nil {
		return err
	}
	if _, err := runGit(ctx, root, "config", "user.name", "Forebrain Harness Memory"); err != nil {
		return err
	}
	if _, err := runGit(ctx, root, "config", "user.email", "memory@forebrain.local"); err != nil {
		return err
	}
	add := []string{"add", "-A", "--", "."}
	for _, path := range extensionInputsModifiedAfter(root, snapshot) {
		add = append(add, ":(exclude)"+path)
	}
	if _, err := runGit(ctx, root, add...); err != nil {
		return err
	}
	if _, err := runGit(ctx, root, "commit", "--allow-empty", "-qm", "memory baseline"); err != nil {
		return err
	}
	return nil
}

// extensionInputsModifiedAfter lists the memory-root-relative paths of extension
// files written after snapshot. Only extension folders are considered: they hold
// inputs written from outside the consolidation pass, while every file the pass
// itself produces lives elsewhere in the root and must be committed.
func extensionInputsModifiedAfter(root string, snapshot time.Time) []string {
	if snapshot.IsZero() {
		return nil
	}
	base := filepath.Join(root, extensionsDir)
	var paths []string
	err := filepath.WalkDir(base, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || !info.ModTime().After(snapshot) {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		paths = append(paths, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		return nil
	}
	sort.Strings(paths)
	return paths
}

func memoryWorkspaceDiff(ctx context.Context, root string) (string, bool, error) {
	if err := removeWorkspaceDiff(root); err != nil {
		return "", false, err
	}
	status, err := runGit(ctx, root, "status", "--porcelain=v1", "--untracked-files=all", "--no-renames")
	if err != nil {
		return "", false, err
	}
	status = strings.TrimSpace(status)
	if status == "" {
		return "", false, nil
	}
	diff, err := runGit(ctx, root, "diff", "--binary", "HEAD", "--")
	if err != nil {
		return "", false, err
	}
	if untracked, uerr := runGit(ctx, root, "ls-files", "--others", "--exclude-standard"); uerr == nil {
		for _, path := range strings.Split(strings.TrimSpace(untracked), "\n") {
			path = strings.TrimSpace(path)
			if path == "" || path == phase2WorkspaceDiffFile {
				continue
			}
			one, derr := runGit(ctx, root, "diff", "--no-index", "--", "/dev/null", path)
			// git diff --no-index returns 1 when differences exist.
			if derr != nil && !strings.Contains(derr.Error(), "exit status 1") {
				return "", false, derr
			}
			diff += one
		}
	}
	return renderWorkspaceDiff(status, diff), true, nil
}

func renderWorkspaceDiff(status, diff string) string {
	var b strings.Builder
	b.WriteString("# Memory Workspace Diff\n\nGenerated before Phase 2 memory consolidation. Read this file first and do not edit it.\n\n## Status\n")
	changes := normalizedWorkspaceChanges(status)
	for _, change := range changes {
		b.WriteString("- ")
		b.WriteString(change)
		b.WriteByte('\n')
	}
	b.WriteString("\n## Diff\n\n```diff\n")
	if len(diff) > maxWorkspaceDiffBytes {
		boundary := maxWorkspaceDiffBytes
		for boundary > 0 && !utf8Boundary(diff, boundary) {
			boundary--
		}
		diff = diff[:boundary] + fmt.Sprintf("\n[workspace diff truncated at %d bytes]\n", maxWorkspaceDiffBytes)
	}
	b.WriteString(diff)
	if diff != "" && !strings.HasSuffix(diff, "\n") {
		b.WriteByte('\n')
	}
	b.WriteString("```\n")
	return b.String()
}

func normalizedWorkspaceChanges(status string) []string {
	changes := make([]string, 0)
	for _, line := range strings.Split(strings.TrimSpace(status), "\n") {
		if len(line) < 4 {
			continue
		}
		code, path := line[:2], strings.TrimSpace(line[3:])
		label := "M"
		switch {
		case code == "??", strings.Contains(code, "A"):
			label = "A"
		case strings.Contains(code, "D"):
			label = "D"
		}
		changes = append(changes, label+" "+path)
	}
	sort.Strings(changes)
	return changes
}

func utf8Boundary(s string, index int) bool {
	return index >= 0 && index <= len(s) && (index == len(s) || index == 0 || s[index]&0xC0 != 0x80)
}

func writeMemoryWorkspaceDiff(root, body string) error {
	return writeFileAtomic(filepath.Join(root, phase2WorkspaceDiffFile), body)
}

func validateConsolidationArtifacts(root string) error {
	memoryPath := filepath.Join(root, "MEMORY.md")
	st, err := os.Stat(memoryPath)
	if err != nil {
		return fmt.Errorf("validate MEMORY.md: %w", err)
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("MEMORY.md is not a regular file")
	}
	summary, err := os.ReadFile(filepath.Join(root, "memory_summary.md"))
	if err != nil {
		return fmt.Errorf("validate memory_summary.md: %w", err)
	}
	first := strings.SplitN(strings.ReplaceAll(string(summary), "\r\n", "\n"), "\n", 2)[0]
	if first != "v1" {
		return fmt.Errorf("memory_summary.md must start with v1")
	}
	return nil
}

func resetMemoryWorkspaceBaseline(ctx context.Context, root string, snapshot time.Time) error {
	if err := removeWorkspaceDiff(root); err != nil {
		return err
	}
	return resetGitBaseline(ctx, root, snapshot)
}

func removeWorkspaceDiff(root string) error {
	path := filepath.Join(root, phase2WorkspaceDiffFile)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove workspace diff: %w", err)
	}
	return nil
}

func runGit(ctx context.Context, root string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
