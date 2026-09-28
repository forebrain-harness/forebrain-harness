package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/home"
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
	prevNeedsSetup := interactiveNeedsFirstSetup
	prevRunOnboard := interactiveRunOnboard
	prevStartupError := interactiveStartupConfigError
	prevOpenChat := interactiveOpenChatSession
	prevTUIRun := interactiveTUIRun
	t.Cleanup(func() {
		interactiveIsTerminal = prevIsTerminal
		interactiveEnsureWorkspaceTrusted = prevTrusted
		interactiveEnsureProjectMCPConsent = prevMCPConsent
		interactiveNeedsFirstSetup = prevNeedsSetup
		interactiveRunOnboard = prevRunOnboard
		interactiveStartupConfigError = prevStartupError
		interactiveOpenChatSession = prevOpenChat
		interactiveTUIRun = prevTUIRun
	})
	interactiveIsTerminal = func(*os.File) bool { return true }
	interactiveEnsureWorkspaceTrusted = func(io.Reader, io.Writer, string, string) (bool, error) { return true, nil }
	interactiveEnsureProjectMCPConsent = func(io.Reader, io.Writer, string, string) error { return nil }
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
