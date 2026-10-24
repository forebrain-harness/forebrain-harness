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
	"github.com/forebrain-harness/forebrain-harness/pkg/tui"
	"github.com/spf13/cobra"
)

func TestRunStreamingTerminalAutoRunsOnboardWhenNeeded(t *testing.T) {
	root := t.TempDir()
	t.Setenv("FOREBRAIN_HOME", root)

	prevIsTerminal := interactiveIsTerminal
	prevNeedsSetup := interactiveNeedsFirstSetup
	prevRunOnboard := interactiveRunOnboard
	prevStartupError := interactiveStartupConfigError
	prevOpenChat := interactiveOpenChatSession
	prevTUIRun := interactiveTUIRun
	t.Cleanup(func() {
		interactiveIsTerminal = prevIsTerminal
		interactiveNeedsFirstSetup = prevNeedsSetup
		interactiveRunOnboard = prevRunOnboard
		interactiveStartupConfigError = prevStartupError
		interactiveOpenChatSession = prevOpenChat
		interactiveTUIRun = prevTUIRun
	})

	interactiveIsTerminal = func(*os.File) bool { return true }
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
	// Every sibling of this test pins the home to a temp directory and this one
	// must too. Without it the session open below reaches the developer's real
	// ~/.forebrain, which is harmless only while the schema happens to be
	// current: a build with a newer schema migrates the real database as a side
	// effect of running the suite, and blocks on whatever process holds its
	// write lock.
	t.Setenv("FOREBRAIN_HOME", t.TempDir())
	prevIsTerminal := interactiveIsTerminal
	prevNeedsSetup := interactiveNeedsFirstSetup
	prevRunOnboard := interactiveRunOnboard
	prevStartupError := interactiveStartupConfigError
	prevOpenChat := interactiveOpenChatSession
	prevTUIRun := interactiveTUIRun
	t.Cleanup(func() {
		interactiveIsTerminal = prevIsTerminal
		interactiveNeedsFirstSetup = prevNeedsSetup
		interactiveRunOnboard = prevRunOnboard
		interactiveStartupConfigError = prevStartupError
		interactiveOpenChatSession = prevOpenChat
		interactiveTUIRun = prevTUIRun
	})

	interactiveIsTerminal = func(*os.File) bool { return true }
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
	prevNeedsSetup := interactiveNeedsFirstSetup
	prevRunOnboard := interactiveRunOnboard
	prevStartupError := interactiveStartupConfigError
	prevOpenChat := interactiveOpenChatSession
	prevTUIRun := interactiveTUIRun
	t.Cleanup(func() {
		interactiveIsTerminal = prevIsTerminal
		interactiveNeedsFirstSetup = prevNeedsSetup
		interactiveRunOnboard = prevRunOnboard
		interactiveStartupConfigError = prevStartupError
		interactiveOpenChatSession = prevOpenChat
		interactiveTUIRun = prevTUIRun
	})

	interactiveIsTerminal = func(*os.File) bool { return true }
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
	prevNeedsSetup := interactiveNeedsFirstSetup
	prevRunOnboard := interactiveRunOnboard
	prevStartupError := interactiveStartupConfigError
	prevOpenChat := interactiveOpenChatSession
	prevTUIRun := interactiveTUIRun
	t.Cleanup(func() {
		interactiveIsTerminal = prevIsTerminal
		interactiveNeedsFirstSetup = prevNeedsSetup
		interactiveRunOnboard = prevRunOnboard
		interactiveStartupConfigError = prevStartupError
		interactiveOpenChatSession = prevOpenChat
		interactiveTUIRun = prevTUIRun
	})
	interactiveIsTerminal = func(*os.File) bool { return true }
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
// launch until the TUI paints its own it stays hidden — including after
// onboarding, which hands it back as the gateway's shell wants it.
func TestRunStreamingTerminalKeepsCaretHiddenUntilTheTUI(t *testing.T) {
	stubInteractiveLaunch(t)
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
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

// A launch inside the forebrain data directory (or below it) is refused before
// any prompt: the data directory is not a project.
func TestRunStreamingTerminalRefusesDataDirectoryLaunch(t *testing.T) {
	stubInteractiveLaunch(t)
	dataRoot, err := home.Root()
	if err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(dataRoot, "workspace", "sub")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(inside)

	err = runStreamingTerminal(&cobra.Command{})
	if err == nil || !strings.Contains(err.Error(), "数据目录") {
		t.Fatalf("launch inside the data directory must be refused with the data-directory message, got %v", err)
	}
}
