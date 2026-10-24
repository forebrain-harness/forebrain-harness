// The trust question, asked inside the TUI once it is on screen: whether the
// folder it started in may be worked in. Quit comes first and is where the
// panel opens — trusting a folder has to be chosen, not fallen into with
// Enter — and Esc and Ctrl+C both answer Quit.
package tui

import (
	"os"

	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

const (
	workspaceTrustQuit = iota
	workspaceTrustAccept
)

var workspaceTrustActions = []string{
	workspaceTrustQuit:   "Quit",
	workspaceTrustAccept: "Trust and continue",
}

const workspaceTrustLabel = "Trust this folder?\n" +
	"Forebrain Harness can edit files here and follow the folder's own instructions and skills."

// workspaceTruster is the session half; optional so fake sessions need not offer it.
type workspaceTruster interface {
	WorkspaceTrustPending() (folder string, pending bool, err error)
	TrustWorkspace() error
}

// workspaceTrustReviewer is the selector half; the interactive raw selector implements it.
type workspaceTrustReviewer interface {
	ReviewWorkspaceTrust(folder string) (trusted bool, err error)
}

// askWorkspaceTrust asks when the launch folder has no decision yet and
// records a trust. It reports whether the surface must quit.
func askWorkspaceTrust(cmds *commandController, renderer *Renderer) (quit bool, err error) {
	if cmds == nil {
		return false, nil
	}
	c, ok := cmds.session.(workspaceTruster)
	if !ok {
		return false, nil
	}
	reviewer, ok := cmds.selector.(workspaceTrustReviewer)
	if !ok {
		return false, nil
	}
	folder, pending, err := c.WorkspaceTrustPending()
	if err != nil {
		// Reading the trust store failed: fail closed rather than open the
		// session on a folder nobody could judge.
		return true, err
	}
	if !pending {
		return false, nil
	}
	trusted, err := reviewer.ReviewWorkspaceTrust(folder)
	if err != nil {
		return true, err
	}
	if !trusted {
		return true, nil
	}
	if err := c.TrustWorkspace(); err != nil {
		return true, err
	}
	return false, nil
}

// ReviewWorkspaceTrust asks the trust question on one panel and reports
// whether the folder was trusted. Enter takes the action the cursor is on —
// the panel opens on Quit — and Esc and Ctrl+C both answer Quit.
func (s *rawSelector) ReviewWorkspaceTrust(folder string) (bool, error) {
	idx, ok, err := s.reviewWith(workspaceTrustLabel,
		[]turn.StatusFact{{Label: "Path", Value: displayFolder(folder)}},
		workspaceTrustActions, workspaceTrustQuit, "Esc to quit")
	if err != nil {
		return false, err
	}
	return ok && idx == workspaceTrustAccept, nil
}

// displayFolder shows a folder the way the rest of the surface shows paths:
// the home directory abbreviated to ~.
func displayFolder(folder string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return folder
	}
	return abbreviateHomePath(folder, home)
}
