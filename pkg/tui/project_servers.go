// The project's MCP and language-server entries come with the repository:
// every entry nobody has decided about yet is confirmed once, on one panel,
// before the conversation that would run it opens — checked means allowed,
// unchecked means declined, and both are remembered for this project until
// the entry's configuration changes. Esc declines every listed entry; Ctrl+C
// closes the panel without recording anything, so the next conversation asks
// again.
package tui

import (
	"errors"
	"fmt"
)

type projectServerKind int

const (
	projectServerMCP projectServerKind = iota
	projectServerLSP
)

// projectServerItem is one entry the panel asks about.
type projectServerItem struct {
	Kind        projectServerKind
	Name        string
	Detail      string
	Fingerprint string
}

// projectServersAnswer is what the panel returns. Decided false means it was
// closed without an answer (Ctrl+C) and nothing is recorded; otherwise
// Allowed holds one entry per item shown.
type projectServersAnswer struct {
	Decided     bool
	Allowed     []bool
	AlwaysAllow bool
}

// projectServersConsenter is the session half: the pending entries and a way
// to record the answer. Optional, so a fake session need not offer it.
type projectServersConsenter interface {
	PendingProjectServers() []projectServerItem
	DecideProjectServers(items []projectServerItem, answer projectServersAnswer) error
}

// projectServersReviewer is the selector half — the interactive raw selector
// implements it; a line selector does not, and then nothing is asked.
type projectServersReviewer interface {
	ReviewProjectServers(items []projectServerItem) (projectServersAnswer, error)
}

// projectServersPanelState is the panel's whole state: the entries shown,
// which of them are checked, the always-allow switch, and the cursor, which
// ranges over the entries and then the switch row.
type projectServersPanelState struct {
	items   []projectServerItem
	checked []bool
	always  bool
	cursor  int
}

// newProjectServersPanelState opens on every entry checked, the switch off
// and the cursor on the first entry.
func newProjectServersPanelState(items []projectServerItem) *projectServersPanelState {
	checked := make([]bool, len(items))
	for i := range checked {
		checked[i] = true
	}
	return &projectServersPanelState{items: items, checked: checked}
}

// move moves the cursor by delta, clamped to the first entry and the switch
// row; it never wraps.
func (p *projectServersPanelState) move(delta int) {
	p.cursor = max(0, min(len(p.items), p.cursor+delta))
}

// toggle flips the row under the cursor. An entry flips itself, and one left
// unchecked turns the switch off with it; the switch flips itself, and turned
// on it checks every entry — the switch being on means every entry is
// checked.
func (p *projectServersPanelState) toggle() {
	if p.cursor < len(p.items) {
		p.checked[p.cursor] = !p.checked[p.cursor]
		if !p.checked[p.cursor] {
			p.always = false
		}
		return
	}
	p.always = !p.always
	if p.always {
		for i := range p.checked {
			p.checked[i] = true
		}
	}
}

// answer is the panel's decision as the session records it.
func (p *projectServersPanelState) answer() projectServersAnswer {
	return projectServersAnswer{Decided: true, Allowed: append([]bool(nil), p.checked...), AlwaysAllow: p.always}
}

// panel lays the panel out: the entries in two columns, MCP first then
// language servers, a group heading only for a kind that has entries, the
// switch row apart from the list, and the keys.
func (p *projectServersPanelState) panel() slashPanel {
	b := newPanelBuilder()
	b.title("Project servers")
	if len(p.items) == 1 {
		b.subtitle("1 server wants to run in this project")
	} else {
		b.subtitle(fmt.Sprintf("%d servers want to run in this project", len(p.items)))
	}
	nameWidth := 0
	for _, item := range p.items {
		nameWidth = maxInt(nameWidth, displayLineWidth(item.Name))
	}
	var sawMCP, sawLSP bool
	for i, item := range p.items {
		switch item.Kind {
		case projectServerMCP:
			if !sawMCP {
				b.group("MCP")
				sawMCP = true
			}
		case projectServerLSP:
			if !sawLSP {
				b.group("Language servers")
				sawLSP = true
			}
		}
		b.selectableColumns(i == p.cursor, checkLead(p.checked[i]), item.Name, nameWidth, item.Detail)
	}
	b.blank()
	b.selectable(p.cursor == len(p.items), checkLead(p.always), "Always allow this project's servers")
	b.hint("↑/↓ to navigate · Space to select · Enter to confirm · Esc to decline all")
	return b.panel()
}

// checkLead is the check-box column: the checked or the unchecked glyph,
// followed by the space the two-column row needs.
func checkLead(checked bool) string {
	if checked {
		return selectorCheckedGlyph + " "
	}
	return selectorUncheckedGlyph + " "
}

// ReviewProjectServers shows the project's undecided MCP and language-server
// entries on one panel and returns the answer: every checked entry allowed
// and every unchecked one declined, the always-allow switch with them. Enter
// takes the answer as shown, Esc declines every listed entry, and Ctrl+C
// closes the panel without one.
func (s *rawSelector) ReviewProjectServers(items []projectServerItem) (projectServersAnswer, error) {
	release, err := s.acquireComposerInput()
	if err != nil {
		return projectServersAnswer{}, err
	}
	defer release()
	st := newProjectServersPanelState(items)
	s.showPanel(st.panel())
	for {
		key, err := s.readKey()
		if err != nil {
			return projectServersAnswer{}, err
		}
		switch key.kind {
		case rawKeyMouse:
			s.dispatchMouse(key.mouse)
			continue
		case rawKeyUp:
			st.move(-1)
		case rawKeyDown:
			st.move(1)
		case rawKeySpace:
			st.toggle()
		case rawKeyEnter:
			return st.answer(), nil
		case rawKeyEscape:
			return projectServersAnswer{Decided: true, Allowed: make([]bool, len(items))}, nil
		case rawKeyCtrlC:
			return projectServersAnswer{}, s.dismissed(key.kind)
		default:
			continue
		}
		s.showPanel(st.panel())
	}
}

// askProjectServers asks about the project's undecided MCP and language-server
// entries on one panel and records the answer. Files that change under the
// open panel make the record fail as stale; the panel is then shown again with
// what is still pending, at most three times.
func askProjectServers(cmds *commandController, renderer *Renderer) {
	if cmds == nil {
		return
	}
	c, ok := cmds.session.(projectServersConsenter)
	if !ok {
		return
	}
	reviewer, ok := cmds.selector.(projectServersReviewer)
	if !ok {
		return
	}
	for attempt := 0; attempt < 3; attempt++ {
		items := c.PendingProjectServers()
		if len(items) == 0 {
			return
		}
		answer, err := reviewer.ReviewProjectServers(items)
		if err != nil {
			if renderer != nil {
				renderer.PrintError(err)
			}
			return
		}
		if !answer.Decided {
			return
		}
		err = c.DecideProjectServers(items, answer)
		var stale interface{ StaleNames() []string }
		if errors.As(err, &stale) {
			continue
		}
		if err != nil && renderer != nil {
			renderer.PrintError(err)
		}
		return
	}
}
