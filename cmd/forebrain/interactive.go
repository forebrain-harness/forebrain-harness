package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"

	"github.com/forebrain-harness/forebrain-harness/pkg/home"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/tui"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func printNonInteractiveHint(w io.Writer) {
	_, _ = fmt.Fprintln(w, "forebrain: stdin/stdout must be a terminal; use `forebrain gateway start` for the gateway service or run `forebrain --help`.")
}

var interactiveRunner = runStreamingTerminal
var interactiveIsTerminal = func(f *os.File) bool {
	return f != nil && term.IsTerminal(int(f.Fd()))
}
var interactiveEnsureWorkspaceTrusted = ensureWorkspaceTrusted
var interactiveEnsureProjectMCPConsent = ensureProjectMCPConsent
var interactiveEnsureProjectLSPConsent = ensureProjectLSPConsent
var interactiveNeedsFirstSetup = tui.NeedsFirstSetup
var onboardRunner = tui.RunOnboarding
var interactiveRunOnboard = onboardRunner
var interactiveStartupConfigError = tui.StartupConfigError
var interactiveOpenChatSession = func(ctx context.Context, cwd string) (*tui.ChatSession, error) {
	// Use the cached config from process.Resolve() so openChatSession
	// does not load and parse forebrain.yaml a third time.
	rt, err := process.Resolve()
	if err != nil {
		return nil, err
	}
	return tui.OpenChatSessionWithConfigForProject(ctx, rt.Config, cwd)
}
var interactiveTUIRun = tui.Run

func runInteractive(cmd *cobra.Command) error {
	return interactiveRunner(cmd)
}

func runStreamingTerminal(cmd *cobra.Command) error {
	return runStreamingTerminalWithInitialSessionID(cmd, "")
}

func runStreamingTerminalWithInitialSessionID(cmd *cobra.Command, initialSessionID string) error {
	if cmd == nil {
		return fmt.Errorf("nil command")
	}
	if !interactiveIsTerminal(os.Stdin) || !interactiveIsTerminal(os.Stdout) {
		return fmt.Errorf("stdin/stdout must be a terminal")
	}
	root, err := home.Root()
	if err != nil {
		return err
	}
	caret := hideLaunchCaret(cmd.OutOrStdout())
	defer caret.restore()
	allowed, err := interactiveEnsureWorkspaceTrusted(cmd.InOrStdin(), cmd.OutOrStdout(), root, "")
	if err != nil {
		return err
	}
	if !allowed {
		return tui.ErrCancelled
	}
	// Project-level MCP entries are confirmed right after the trust prompt:
	// the list is frozen for the session, so startup is the only moment to
	// ask. Anything left unconfirmed stays out of the session.
	if err := interactiveEnsureProjectMCPConsent(cmd.InOrStdin(), cmd.OutOrStdout(), root, ""); err != nil {
		return err
	}
	// The project's language-server entries follow the same rule, asked
	// right after the MCP ones (spec §5.2): an unconfirmed entry does not
	// apply, not even as an override of a built-in server.
	if err := interactiveEnsureProjectLSPConsent(cmd.InOrStdin(), cmd.OutOrStdout(), root, ""); err != nil {
		return err
	}
	restoreLog := tui.RedirectProcessLoggingForTUI(root)
	defer restoreLog()
	if needs, err := interactiveNeedsFirstSetup(); err != nil {
		return err
	} else if needs {
		if err := interactiveRunOnboard(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout()); err != nil {
			return err
		}
		// Onboarding is the gateway's first-run setup too, so it ends by
		// handing the caret back as a shell wants it. This launch is not
		// over: the TUI is still to come.
		caret.hide()
		process.ResetResolve()
	}
	// Pre-warm the runtime config cache so the subsequent OpenChatSession call
	// shares the same loaded config instead of parsing forebrain.yaml a second time.
	// The cached result also seeds the global model catalog.
	if err := interactiveStartupConfigError(); err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		cwd = ""
	}
	slog.Info("opening chat session for streaming terminal")
	sess, err := interactiveOpenChatSession(cmd.Context(), cwd)
	if err != nil {
		return err
	}
	defer sess.Close()
	// Typed input is tenant data: it is what the operator said to *this*
	// primary agent. It hangs off the agent workspace root like the other
	// per-agent stores, never off the shared home.
	workspaceRoot := strings.TrimSpace(sess.StateRoot())
	if workspaceRoot == "" {
		workspaceRoot = filepath.Join(root, home.WorkspaceDirName)
	}
	caret.handOff()
	return interactiveTUIRun(cmd.Context(), tui.Options{
		In:               cmd.InOrStdin(),
		Out:              cmd.OutOrStdout(),
		Err:              cmd.ErrOrStderr(),
		Session:          sess,
		InitialSessionID: strings.TrimSpace(initialSessionID),
		Banner:           "Forebrain Harness",
		Version:          home.Version,
		InputHistoryPath: filepath.Join(workspaceRoot, "state", "cli-input-history.txt"),
		WorkingDirectory: cwd,
		Home:             root,
		WorkspaceRoot:    workspaceRoot,
	})
}

// Caret visibility (DECTCEM).
const (
	hideCaretSeq = "\x1b[?25l"
	showCaretSeq = "\x1b[?25h"
)

// launchCaret keeps the terminal caret hidden for the part of an interactive
// launch that runs before the TUI paints its own. The trust prompt is a
// selector, onboarding draws its own caret and opening the session takes no
// input, so a bare caret blinking under the last prompt only claims the
// terminal wants typing when it does not. The one line typed at the caret,
// the project-MCP confirmation, shows it while it waits for the answer.
//
// Hiding it obliges the launch to bring it back on every way out. A return
// does so through restore. An interrupt while the terminal is still cooked
// would end the process with the shell left caretless, so one is caught and
// ended the same way until the TUI, which handles interrupts itself, takes
// the terminal.
type launchCaret struct {
	out     io.Writer
	intr    chan os.Signal
	handoff sync.Once
}

func hideLaunchCaret(out io.Writer) *launchCaret {
	c := &launchCaret{out: out, intr: make(chan os.Signal, 1)}
	c.hide()
	signal.Notify(c.intr, os.Interrupt)
	go func() {
		if _, ok := <-c.intr; ok {
			c.show()
			os.Exit(130)
		}
	}()
	return c
}

func (c *launchCaret) hide() { _, _ = io.WriteString(c.out, hideCaretSeq) }

func (c *launchCaret) show() { _, _ = io.WriteString(c.out, showCaretSeq) }

// handOff stops the launch's interrupt handling, leaving the TUI's own as
// the only one. The caret stays hidden for the TUI to paint its own.
func (c *launchCaret) handOff() {
	c.handoff.Do(func() {
		signal.Stop(c.intr)
		close(c.intr)
	})
}

// restore ends the launch's hold on the caret, however the launch ends.
func (c *launchCaret) restore() {
	c.handOff()
	c.show()
}
