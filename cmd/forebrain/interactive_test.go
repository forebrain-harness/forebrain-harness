package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/home"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/tui"
	"github.com/spf13/cobra"
)

func TestRunStreamingTerminalAutoRunsOnboardWhenNeeded(t *testing.T) {
	root := t.TempDir()
	t.Setenv("FOREBRAIN_HOME", root)

	prevIsTerminal := interactiveIsTerminal
	prevTrusted := interactiveEnsureWorkspaceTrusted
	prevNeedsSetup := interactiveNeedsFirstSetup
	prevRunOnboard := interactiveRunOnboard
	prevStartupError := interactiveStartupConfigError
	prevOpenChat := interactiveOpenChatSession
	prevTUIRun := interactiveTUIRun
	t.Cleanup(func() {
		interactiveIsTerminal = prevIsTerminal
		interactiveEnsureWorkspaceTrusted = prevTrusted
		interactiveNeedsFirstSetup = prevNeedsSetup
		interactiveRunOnboard = prevRunOnboard
		interactiveStartupConfigError = prevStartupError
		interactiveOpenChatSession = prevOpenChat
		interactiveTUIRun = prevTUIRun
	})

	interactiveIsTerminal = func(*os.File) bool { return true }
	interactiveEnsureWorkspaceTrusted = func(in io.Reader, out io.Writer, home string, cwd string) (bool, error) {
		return true, nil
	}
	interactiveNeedsFirstSetup = func() (bool, error) { return true, nil }
	onboardCalled := false
	interactiveRunOnboard = func(ctx context.Context, in io.Reader, out io.Writer) error {
		onboardCalled = true
		return nil
	}
	interactiveStartupConfigError = func() error { return nil }
	sessionOpened := false
	interactiveOpenChatSession = func(context.Context, string) (*tui.ChatSession, error) {
		sessionOpened = true
		return &tui.ChatSession{}, nil
	}
	streamRunCalled := false
	wantDir, _ := os.Getwd()
	interactiveTUIRun = func(ctx context.Context, opts tui.Options) error {
		streamRunCalled = true
		if opts.Version != home.Version {
			t.Fatalf("expected version %q, got %q", home.Version, opts.Version)
		}
		if opts.WorkingDirectory != wantDir {
			t.Fatalf("expected working directory %q, got %q", wantDir, opts.WorkingDirectory)
		}
		return nil
	}

	err := runStreamingTerminal(&cobra.Command{})
	if err != nil {
		t.Fatalf("runStreamingTerminal error: %v", err)
	}
	if !onboardCalled {
		t.Fatal("expected onboard to run")
	}
	if !sessionOpened {
		t.Fatal("expected chat session to open after onboard")
	}
	if !streamRunCalled {
		t.Fatal("expected stream terminal to start")
	}
}

func TestRunStreamingTerminalStopsWhenOnboardCancelled(t *testing.T) {
	prevIsTerminal := interactiveIsTerminal
	prevTrusted := interactiveEnsureWorkspaceTrusted
	prevNeedsSetup := interactiveNeedsFirstSetup
	prevRunOnboard := interactiveRunOnboard
	prevStartupError := interactiveStartupConfigError
	prevOpenChat := interactiveOpenChatSession
	prevTUIRun := interactiveTUIRun
	t.Cleanup(func() {
		interactiveIsTerminal = prevIsTerminal
		interactiveEnsureWorkspaceTrusted = prevTrusted
		interactiveNeedsFirstSetup = prevNeedsSetup
		interactiveRunOnboard = prevRunOnboard
		interactiveStartupConfigError = prevStartupError
		interactiveOpenChatSession = prevOpenChat
		interactiveTUIRun = prevTUIRun
	})

	interactiveIsTerminal = func(*os.File) bool { return true }
	interactiveEnsureWorkspaceTrusted = func(in io.Reader, out io.Writer, home string, cwd string) (bool, error) {
		return true, nil
	}
	interactiveNeedsFirstSetup = func() (bool, error) { return true, nil }
	interactiveRunOnboard = func(context.Context, io.Reader, io.Writer) error { return tui.ErrCancelled }
	interactiveStartupConfigError = func() error { return nil }
	interactiveOpenChatSession = func(context.Context, string) (*tui.ChatSession, error) {
		t.Fatal("chat session should not open after onboard cancellation")
		return nil, nil
	}
	interactiveTUIRun = func(context.Context, tui.Options) error {
		t.Fatal("tui should not start after onboard cancellation")
		return nil
	}

	err := runStreamingTerminal(&cobra.Command{})
	if err == nil || err != tui.ErrCancelled {
		t.Fatalf("expected ErrCancelled, got %v", err)
	}
}

func TestResumeCommandUsesExplicitSessionID(t *testing.T) {
	prevIsTerminal := interactiveIsTerminal
	prevTrusted := interactiveEnsureWorkspaceTrusted
	prevNeedsSetup := interactiveNeedsFirstSetup
	prevRunOnboard := interactiveRunOnboard
	prevStartupError := interactiveStartupConfigError
	prevOpenChat := interactiveOpenChatSession
	prevTUIRun := interactiveTUIRun
	t.Cleanup(func() {
		interactiveIsTerminal = prevIsTerminal
		interactiveEnsureWorkspaceTrusted = prevTrusted
		interactiveNeedsFirstSetup = prevNeedsSetup
		interactiveRunOnboard = prevRunOnboard
		interactiveStartupConfigError = prevStartupError
		interactiveOpenChatSession = prevOpenChat
		interactiveTUIRun = prevTUIRun
	})

	interactiveIsTerminal = func(*os.File) bool { return true }
	interactiveEnsureWorkspaceTrusted = func(in io.Reader, out io.Writer, home string, cwd string) (bool, error) {
		return true, nil
	}
	interactiveNeedsFirstSetup = func() (bool, error) { return false, nil }
	interactiveRunOnboard = func(context.Context, io.Reader, io.Writer) error { return nil }
	interactiveStartupConfigError = func() error { return nil }
	interactiveOpenChatSession = func(context.Context, string) (*tui.ChatSession, error) {
		return &tui.ChatSession{}, nil
	}
	gotInitial := ""
	interactiveTUIRun = func(ctx context.Context, opts tui.Options) error {
		gotInitial = opts.InitialSessionID
		return nil
	}

	if err := resumeCmd.RunE(&cobra.Command{}, []string{"session-123"}); err != nil {
		t.Fatalf("resume command error: %v", err)
	}
	if gotInitial != "session-123" {
		t.Fatalf("initial session id = %q, want session-123", gotInitial)
	}
}

// stubInteractiveLaunch replaces every stage of an interactive launch with a
// stub that does nothing, for the test to override the ones it is about.
func stubInteractiveLaunch(t *testing.T) {
	t.Helper()
	t.Setenv("FOREBRAIN_HOME", t.TempDir())
	prevIsTerminal := interactiveIsTerminal
	prevTrusted := interactiveEnsureWorkspaceTrusted
	prevMCPConsent := interactiveEnsureProjectMCPConsent
	prevLSPConsent := interactiveEnsureProjectLSPConsent
	prevNeedsSetup := interactiveNeedsFirstSetup
	prevRunOnboard := interactiveRunOnboard
	prevStartupError := interactiveStartupConfigError
	prevOpenChat := interactiveOpenChatSession
	prevTUIRun := interactiveTUIRun
	t.Cleanup(func() {
		interactiveIsTerminal = prevIsTerminal
		interactiveEnsureWorkspaceTrusted = prevTrusted
		interactiveEnsureProjectMCPConsent = prevMCPConsent
		interactiveEnsureProjectLSPConsent = prevLSPConsent
		interactiveNeedsFirstSetup = prevNeedsSetup
		interactiveRunOnboard = prevRunOnboard
		interactiveStartupConfigError = prevStartupError
		interactiveOpenChatSession = prevOpenChat
		interactiveTUIRun = prevTUIRun
	})
	interactiveIsTerminal = func(*os.File) bool { return true }
	interactiveEnsureWorkspaceTrusted = func(io.Reader, io.Writer, string, string) (bool, error) { return true, nil }
	interactiveEnsureProjectMCPConsent = func(io.Reader, io.Writer, string, string) error { return nil }
	interactiveEnsureProjectLSPConsent = func(io.Reader, io.Writer, string, string) error { return nil }
	interactiveNeedsFirstSetup = func() (bool, error) { return false, nil }
	interactiveRunOnboard = func(context.Context, io.Reader, io.Writer) error { return nil }
	interactiveStartupConfigError = func() error { return nil }
	interactiveOpenChatSession = func(context.Context, string) (*tui.ChatSession, error) {
		return &tui.ChatSession{}, nil
	}
	interactiveTUIRun = func(context.Context, tui.Options) error { return nil }
}

// caretShown reports whether the last caret-visibility sequence in out shows
// the caret.
func caretShown(out string) bool {
	return strings.LastIndex(out, showCaretSeq) > strings.LastIndex(out, hideCaretSeq)
}

// Nothing before the TUI is typed at the caret, so from the first byte of the
// launch until the TUI paints its own it stays hidden — including after the
// trust prompt is answered and after onboarding, which hands it back as the
// gateway's shell wants it.
func TestRunStreamingTerminalKeepsCaretHiddenUntilTheTUI(t *testing.T) {
	stubInteractiveLaunch(t)
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	interactiveEnsureWorkspaceTrusted = func(_ io.Reader, w io.Writer, _ string, _ string) (bool, error) {
		if caretShown(out.String()) || !strings.HasPrefix(out.String(), hideCaretSeq) {
			t.Fatalf("the caret must be hidden before the trust prompt, got %q", out.String())
		}
		_, _ = io.WriteString(w, "Trust this directory?\r\n")
		return true, nil
	}
	interactiveNeedsFirstSetup = func() (bool, error) { return true, nil }
	interactiveRunOnboard = func(_ context.Context, _ io.Reader, w io.Writer) error {
		_, _ = io.WriteString(w, "\x1b[0m"+showCaretSeq)
		return nil
	}
	tuiStarted := false
	interactiveTUIRun = func(context.Context, tui.Options) error {
		tuiStarted = true
		if caretShown(out.String()) {
			t.Fatalf("the caret must still be hidden when the TUI starts, got %q", out.String())
		}
		return nil
	}

	if err := runStreamingTerminal(cmd); err != nil {
		t.Fatalf("runStreamingTerminal error: %v", err)
	}
	if !tuiStarted {
		t.Fatal("expected the TUI to start")
	}
	if !caretShown(out.String()) {
		t.Fatalf("the caret must be back when the launch ends, got %q", out.String())
	}
}

// A launch that ends before the TUI — here by declining trust — leaves the
// caret visible for the shell.
func TestRunStreamingTerminalRestoresCaretWhenTrustDeclined(t *testing.T) {
	stubInteractiveLaunch(t)
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	interactiveEnsureWorkspaceTrusted = func(io.Reader, io.Writer, string, string) (bool, error) { return false, nil }
	interactiveTUIRun = func(context.Context, tui.Options) error {
		t.Fatal("tui should not start after trust is declined")
		return nil
	}

	if err := runStreamingTerminal(cmd); err != tui.ErrCancelled {
		t.Fatalf("expected declining trust to end the launch as a quit, got %v", err)
	}
	if !strings.HasPrefix(out.String(), hideCaretSeq) || !caretShown(out.String()) {
		t.Fatalf("expected the caret hidden during the launch and back after it, got %q", out.String())
	}
}

// The language-server prompt runs immediately after the MCP one: both ask
// about project files, in the order the plan fixes.
func TestInteractiveAsksProjectLSPConsentAfterMCP(t *testing.T) {
	stubInteractiveLaunch(t)
	var order []string
	interactiveEnsureProjectMCPConsent = func(io.Reader, io.Writer, string, string) error {
		order = append(order, "mcp")
		return nil
	}
	interactiveEnsureProjectLSPConsent = func(io.Reader, io.Writer, string, string) error {
		order = append(order, "lsp")
		return nil
	}
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetIn(strings.NewReader(""))
	if err := runStreamingTerminal(cmd); err != nil {
		t.Fatalf("runStreamingTerminal error: %v", err)
	}
	if len(order) != 2 || order[0] != "mcp" || order[1] != "lsp" {
		t.Fatalf("call order = %v, want mcp then lsp", order)
	}
}

// The startup prompt asks entry by entry: y allows, anything else declines,
// both texts verbatim, and the answers land in the consent store.
func TestEnsureProjectLSPConsentPrompt(t *testing.T) {
	t.Setenv("FOREBRAIN_HOME", t.TempDir())
	process.ResetResolve()
	t.Cleanup(process.ResetResolve)
	home, err := home.Root()
	if err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := safety.MarkTrusted(home, safety.Project{Root: project}); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(project, ".forebrain", "lsp_servers.yaml")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "servers:\n  first: {command: /bin/first}\n  second: {command: /bin/second}\n"
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := ensureProjectLSPConsent(strings.NewReader("y\nn\n"), &out, home, project); err != nil {
		t.Fatalf("ensureProjectLSPConsent: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"This project configures language servers. Confirm each one before it runs in this session.",
		"first: runs `/bin/first`",
		"second: runs `/bin/second`",
		"Allow it? [y/N] ",
		"  confirmed — it applies from now on",
		"  skipped — confirm later on the project page or at the next start",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("prompt output missing %q:\n%s", want, got)
		}
	}
	if n := strings.Count(got, "Allow it? [y/N] "); n != 2 {
		t.Fatalf("asked %d times, want 2:\n%s", n, got)
	}

	launch := safety.ProjectContext{Project: safety.Project{Root: project, VersionControlled: true}, TrustLevel: safety.LevelTrusted}
	workspace := activeWorkspace(t)
	_, _, allowed, denied, _ := process.InspectProjectLSP(workspace, launch)
	if len(allowed) != 1 || allowed[0] != "first" {
		t.Fatalf("allowed = %v", allowed)
	}
	if len(denied) != 1 || denied[0] != "second" {
		t.Fatalf("denied = %v", denied)
	}
}
