// Workspace-scoped tools: diffs, patch application, and turn diffs.
package tool

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
)

// ErrNotGitRepository is ProjectDiff's answer for a directory git does not
// track.
var ErrNotGitRepository = errors.New("not a git repository")

// ProjectDiff is the project's uncommitted work as one unified diff: every
// change to a tracked file against the last commit, staged or not, followed by
// each file git does not track yet as an addition. paths narrow it to those
// files or directories. An empty diff means there is nothing uncommitted.
func ProjectDiff(dir string, paths []string) (string, error) {
	dir = strings.TrimSpace(dir)
	if _, err := runGit(dir, "rev-parse", "--is-inside-work-tree"); err != nil {
		return "", ErrNotGitRepository
	}
	pathspec := append([]string{"--"}, paths...)
	// Against HEAD so staged changes count too; a repository with no commit
	// yet has only its index to compare with.
	base := []string{"diff", "HEAD"}
	if _, err := runGit(dir, "rev-parse", "--verify", "--quiet", "HEAD"); err != nil {
		base = []string{"diff", "--cached"}
	}
	var out strings.Builder
	tracked, err := runGit(dir, append(base, pathspec...)...)
	if err != nil {
		return "", err
	}
	out.WriteString(tracked)
	untracked, err := runGit(dir, append([]string{"ls-files", "--others", "--exclude-standard"}, pathspec...)...)
	if err != nil {
		return "", err
	}
	nullPath := "/dev/null"
	if runtime.GOOS == "windows" {
		nullPath = "NUL"
	}
	for _, line := range strings.Split(untracked, "\n") {
		file := strings.TrimSpace(line)
		if file == "" {
			continue
		}
		added, err := runGit(dir, "diff", "--no-index", "--", nullPath, file)
		if err != nil {
			return "", err
		}
		out.WriteString(added)
	}
	return out.String(), nil
}

// runGit runs git in dir and returns its output. A diff that found changes
// exits 1, which is an answer, not a failure; any other failure carries what
// git printed.
func runGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return stdout.String(), nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 && len(args) > 0 && args[0] == "diff" {
		return stdout.String(), nil
	}
	if msg := strings.TrimSpace(stderr.String()); msg != "" {
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
}

func attachTurnDiff(output map[string]any, absPath string, before, after []byte) {
	if output == nil {
		return
	}
	summary, err := event.Build(absPath, before, after)
	if err != nil {
		return
	}
	output["turn_diff"] = summary
}
