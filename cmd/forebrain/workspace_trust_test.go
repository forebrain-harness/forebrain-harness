package main

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

// stubTrustPrompt answers the trust prompt with accepted, failing the test if
// the prompt is not expected to open.
func stubTrustPrompt(t *testing.T, accepted bool, expected bool) {
	t.Helper()
	prev := promptWorkspaceTrust
	t.Cleanup(func() { promptWorkspaceTrust = prev })
	promptWorkspaceTrust = func(io.Reader, io.Writer, string) (bool, error) {
		if !expected {
			t.Fatal("the trust prompt must not open")
		}
		return accepted, nil
	}
}

func TestEnsureWorkspaceTrustedAcceptsAndPersists(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	stubTrustPrompt(t, true, true)
	ok, err := ensureWorkspaceTrusted(nil, io.Discard, home, cwd)
	if err != nil {
		t.Fatalf("ensureWorkspaceTrusted error: %v", err)
	}
	if !ok {
		t.Fatalf("expected trust accepted")
	}
	target, err := workspaceTrustTarget(cwd)
	if err != nil {
		t.Fatalf("workspaceTrustTarget error: %v", err)
	}
	trusted, err := isWorkspaceTrusted(home, target)
	if err != nil {
		t.Fatalf("isWorkspaceTrusted error: %v", err)
	}
	if !trusted {
		t.Fatalf("expected trusted workspace persisted")
	}
	path := workspaceTrustFilePath(home)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected trust file created: %v", err)
	}
}

func TestEnsureWorkspaceTrustedDeclineReturnsFalse(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	stubTrustPrompt(t, false, true)
	ok, err := ensureWorkspaceTrusted(nil, io.Discard, home, cwd)
	if err != nil {
		t.Fatalf("ensureWorkspaceTrusted error: %v", err)
	}
	if ok {
		t.Fatalf("expected trust declined")
	}
	path := workspaceTrustFilePath(home)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected no trust file, got err=%v", err)
	}
}

// fakeTrustReviewer answers the trust page with a fixed choice and records
// what it was asked.
type fakeTrustReviewer struct {
	idx        int
	ok         bool
	err        error
	label      string
	facts      []turn.StatusFact
	actions    []string
	defaultIdx int
}

func (f *fakeTrustReviewer) Review(label string, facts []turn.StatusFact, actions []string, defaultIdx int) (int, bool, error) {
	f.label, f.facts, f.actions, f.defaultIdx = label, facts, actions, defaultIdx
	return f.idx, f.ok, f.err
}

// The page names the directory, says in one sentence what trusting it
// allows, and opens on Quit so trust is always an explicit choice.
func TestWorkspaceTrustPageOpensOnQuit(t *testing.T) {
	page := &fakeTrustReviewer{idx: workspaceTrustQuit, ok: true}
	ok, err := askWorkspaceTrust(page, "/tmp/project")
	if err != nil || ok {
		t.Fatalf("askWorkspaceTrust = (%v, %v), want Quit to decline", ok, err)
	}
	if page.defaultIdx != workspaceTrustQuit || page.actions[page.defaultIdx] != "Quit" {
		t.Fatalf("the page must open on Quit, opened on %d of %q", page.defaultIdx, page.actions)
	}
	if len(page.facts) != 1 || page.facts[0].Value != "/tmp/project" {
		t.Fatalf("the page must name the directory, got facts %+v", page.facts)
	}
	title, sentence, _ := strings.Cut(page.label, "\n")
	if title != "Trust this directory?" || strings.Count(sentence, ". ") != 0 || strings.Contains(page.label, ".md") {
		t.Fatalf("the page must ask one question and explain it in one sentence naming no file, got %q", page.label)
	}
}

func TestWorkspaceTrustPageAnswers(t *testing.T) {
	cancelled := errors.New("cancelled")
	for _, tc := range []struct {
		name   string
		page   fakeTrustReviewer
		want   bool
		wantEr error
	}{
		{name: "trust", page: fakeTrustReviewer{idx: workspaceTrustAccept, ok: true}, want: true},
		{name: "quit", page: fakeTrustReviewer{idx: workspaceTrustQuit, ok: true}},
		{name: "esc", page: fakeTrustReviewer{idx: -1}},
		{name: "ctrl-c", page: fakeTrustReviewer{idx: -1, err: cancelled}, wantEr: cancelled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ok, err := askWorkspaceTrust(&tc.page, "/tmp/project")
			if ok != tc.want || err != tc.wantEr {
				t.Fatalf("askWorkspaceTrust = (%v, %v), want (%v, %v)", ok, err, tc.want, tc.wantEr)
			}
		})
	}
}

func TestWorkspaceTrustTargetPrefersGitRoot(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	if err := os.MkdirAll(filepath.Join(repo, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	runCmd(t, repo, "git", "init")
	target, err := workspaceTrustTarget(filepath.Join(repo, "sub"))
	if err != nil {
		t.Fatalf("workspaceTrustTarget error: %v", err)
	}
	want := filepath.Clean(repo)
	if resolved, err := filepath.EvalSymlinks(want); err == nil && strings.TrimSpace(resolved) != "" {
		want = filepath.Clean(resolved)
	}
	if filepath.Clean(target) != want {
		t.Fatalf("expected repo root target, got %s want %s", target, want)
	}
}

func runCmd(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	command := exec.Command(name, args...)
	command.Dir = filepath.Clean(dir)
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("command failed: %s %v: %v\n%s", name, args, err, string(out))
	}
}

// A project the user has already rejected is not asked about again, and the
// session still starts: what holds it back is the stricter approval policy its
// trust level derives, not a refusal to launch.
func TestEnsureWorkspaceTrustedSkipsPromptForRecordedRejection(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	target, err := workspaceTrustTarget(cwd)
	if err != nil {
		t.Fatalf("workspaceTrustTarget error: %v", err)
	}
	writeUntrustedProject(t, home, target)

	stubTrustPrompt(t, false, false)
	ok, err := ensureWorkspaceTrusted(nil, io.Discard, home, cwd)
	if err != nil {
		t.Fatalf("ensureWorkspaceTrusted error: %v", err)
	}
	if !ok {
		t.Fatal("a recorded rejection must not stop the launch")
	}
}

func writeUntrustedProject(t *testing.T, home, target string) {
	t.Helper()
	path := workspaceTrustFilePath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := `{"trusted_directories":[],"untrusted_directories":["` + target + `"]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write trust state: %v", err)
	}
}

// The helpers below were production functions that only these tests called:
// each is a thin wrapper over the live function underneath. They live here
// so the production files carry no unused code.

func workspaceTrustFilePath(home string) string {
	return safety.StatePath(home)
}

func isWorkspaceTrusted(home string, target string) (bool, error) {
	return safety.IsTrusted(home, safety.Project{Root: target})
}
