package main

import (
	"errors"
	"io"
	"os"

	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/tui"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

func ensureWorkspaceTrusted(in io.Reader, out io.Writer, home string, cwd string) (bool, error) {
	target, err := workspaceTrustTarget(cwd)
	if err != nil {
		return false, err
	}
	level, err := safety.TrustLevel(home, safety.Project{Root: target})
	if err != nil {
		return false, err
	}
	if level != safety.LevelUnknown {
		// The prompt exists to obtain a decision, so a recorded one ends it —
		// including a recorded rejection, which would otherwise be re-litigated
		// on every launch with "no" as the only way past it.
		//
		// A rejection does not stop the session either. What holds an untrusted
		// project back is the approval policy its trust level derives (see
		// safety.EffectiveConfig): every command that is not provably safe
		// is put to the user. Only declining the prompt below ends a launch.
		return true, nil
	}
	accepted, err := promptWorkspaceTrust(in, out, target)
	if err != nil || !accepted {
		return false, err
	}
	if err := markWorkspaceTrusted(home, target); err != nil {
		return false, err
	}
	return true, nil
}

// promptWorkspaceTrust asks on a page of its own whether target is trusted.
var promptWorkspaceTrust = func(in io.Reader, out io.Writer, target string) (bool, error) {
	f, _ := in.(*os.File)
	sel, _, leave, ok := tui.NewStandaloneRawSelector(f, out)
	if !ok {
		return false, errors.New("the trust prompt could not take over the terminal")
	}
	defer leave()
	return askWorkspaceTrust(sel, target)
}

// trustReviewer is the page the trust prompt is asked on.
type trustReviewer interface {
	Review(label string, facts []turn.StatusFact, actions []string, defaultIdx int) (int, bool, error)
}

// The answers, in the order the page lists them. Quitting comes first and is
// where the page opens: trusting a directory has to be chosen, not fallen
// into with Enter.
const (
	workspaceTrustQuit = iota
	workspaceTrustAccept
)

var workspaceTrustActions = []string{
	workspaceTrustQuit:   "Quit",
	workspaceTrustAccept: "Trust and continue",
}

const workspaceTrustLabel = "Trust this directory?\n" +
	"Once trusted, Forebrain Harness can edit files here and follow the directory's own instructions and skills — trust only code you know."

// askWorkspaceTrust reports whether the user trusts target. Esc answers as
// Quit does; Ctrl+C comes back as tui.ErrCancelled.
func askWorkspaceTrust(sel trustReviewer, target string) (bool, error) {
	idx, ok, err := sel.Review(workspaceTrustLabel, []turn.StatusFact{{Label: "Path", Value: target}}, workspaceTrustActions, workspaceTrustQuit)
	if err != nil {
		return false, err
	}
	return ok && idx == workspaceTrustAccept, nil
}

func workspaceTrustTarget(cwd string) (string, error) {
	project, err := safety.Resolve(cwd)
	if err != nil {
		return "", err
	}
	return project.Root, nil
}

func markWorkspaceTrusted(home string, target string) error {
	return safety.MarkTrusted(home, safety.Project{Root: target})
}
