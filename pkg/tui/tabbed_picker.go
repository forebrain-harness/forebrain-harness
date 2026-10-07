package tui

import (
	"sort"
	"strconv"
	"strings"
)

// selectRichTabbed opens the tabbed picker when the selector supports it and
// falls back to the plain rich picker for every other selector, so callers
// name the shape they want without having to probe for the capability.
func selectRichTabbed(sel Selector, label string, items []SelectItem, opts TabbedSelectOptions) (int, bool, error) {
	if tabbed, ok := sel.(TabbedRichSelector); ok {
		return tabbed.SelectRichTabbed(label, items, opts)
	}
	return sel.SelectRich(label, items, opts.DefaultIdx)
}

// multiSelectRichTabbed opens the tabbed multi-select, falling back to a plain
// MultiSelect whose options name each row's category so the tabs' information
// survives even without the tabbed picker.
func multiSelectRichTabbed(sel Selector, label string, items []SelectItem, opts TabbedSelectOptions) ([]int, bool, error) {
	if tabbed, ok := sel.(TabbedRichSelector); ok {
		return tabbed.MultiSelectRichTabbed(label, items, opts)
	}
	labels := tabbedFallbackLabels(items)
	defaults := make([]string, 0, len(opts.Checked))
	for _, idx := range opts.Checked {
		defaults = append(defaults, labels[idx])
	}
	selected, ok, err := sel.MultiSelect(label, labels, defaults)
	if err != nil || !ok {
		return nil, ok, err
	}
	index := make(map[string]int, len(labels))
	for i, l := range labels {
		index[strings.TrimSpace(l)] = i
	}
	out := make([]int, 0, len(selected))
	for _, label := range selected {
		out = append(out, index[strings.TrimSpace(label)])
	}
	sort.Ints(out)
	return out, true, nil
}

// tabbedFallbackLabels names each row for a selector that cannot show tabs:
// the label with its category in parentheses, de-duplicated so every option
// stays unique when one name sits in several tabs.
func tabbedFallbackLabels(items []SelectItem) []string {
	seen := make(map[string]int, len(items))
	labels := make([]string, 0, len(items))
	for _, item := range items {
		label := strings.TrimSpace(item.Label) + " (" + strings.TrimSpace(item.Category) + ")"
		if n, dup := seen[label]; dup {
			n++
			seen[label] = n
			label += " #" + strconv.Itoa(n)
		} else {
			seen[label] = 1
		}
		labels = append(labels, label)
	}
	return labels
}

// tabbedPickerState is a tabbed picker's whole state, kept apart from the key
// loop so the layout can be laid out and checked on its own.
type tabbedPickerState struct {
	items     []SelectItem
	tabs      []string // categories, in first-appearance order
	tabOf     []int    // per item, its index in tabs
	uncounted map[string]bool
	tab       int   // the tab shown
	matches   []int // per tab, its selectable rows matching the filter
	visible   []int // indices into items: the shown tab's matching rows, best match first
	cursor    int   // index into visible
	nameWidth int   // widest label of all items, at most panelNameColumnMax
	filter    filterField
	multi     bool
	checked   map[int]bool // indices into items (multi-select only)
	esc       string
}

// newTabbedPickerState opens on the row the options name — the row the user
// left the picker on — or on the first row of the first tab. nameWidth is
// taken over every item, so neither typing a filter nor switching tabs moves
// the description column.
func newTabbedPickerState(items []SelectItem, opts TabbedSelectOptions, multi bool, esc string) *tabbedPickerState {
	p := &tabbedPickerState{
		items:     items,
		uncounted: make(map[string]bool, len(opts.Uncounted)),
		multi:     multi,
		checked:   make(map[int]bool, len(opts.Checked)),
		esc:       esc,
	}
	for _, name := range opts.Uncounted {
		p.uncounted[name] = true
	}
	for _, idx := range opts.Checked {
		p.checked[idx] = true
	}
	tabIndex := make(map[string]int, len(items))
	p.tabOf = make([]int, len(items))
	for i, item := range items {
		if _, ok := tabIndex[item.Category]; !ok {
			tabIndex[item.Category] = len(p.tabs)
			p.tabs = append(p.tabs, item.Category)
		}
		p.tabOf[i] = tabIndex[item.Category]
		if w := displayLineWidth(item.Label); w > p.nameWidth {
			p.nameWidth = w
		}
	}
	p.nameWidth = min(p.nameWidth, panelNameColumnMax)
	p.rebuild()
	p.focus(opts.DefaultIdx)
	return p
}

// filterPass scores every row against the filter and counts the matches of
// every tab in the same pass.
func (p *tabbedPickerState) filterPass() (query string, ranks map[int]int) {
	p.matches = make([]int, len(p.tabs))
	ranks = make(map[int]int, len(p.items))
	query = strings.ToLower(strings.TrimSpace(p.filter.text))
	for i, item := range p.items {
		if item.Disabled {
			continue
		}
		rank := pickerMatchRank(item.Label, item.Description, query)
		if rank < 0 {
			continue
		}
		ranks[i] = rank
		p.matches[p.tabOf[i]]++
	}
	return query, ranks
}

// revisible gathers the shown tab's matching rows, best match first, in the
// items' own order within a rank.
func (p *tabbedPickerState) revisible(ranks map[int]int) {
	p.visible = p.visible[:0]
	for i := range p.items {
		if _, ok := ranks[i]; !ok || p.tabOf[i] != p.tab {
			continue
		}
		p.visible = append(p.visible, i)
	}
	sort.SliceStable(p.visible, func(a, b int) bool {
		return ranks[p.visible[a]] < ranks[p.visible[b]]
	})
}

func (p *tabbedPickerState) rebuild() {
	query, ranks := p.filterPass()
	// A filter the shown tab cannot answer would leave the user staring at
	// "No matches" while another tab holds the answer, so the picker moves to
	// the first tab that does match.
	if query != "" && p.matches[p.tab] == 0 {
		for t, n := range p.matches {
			if n > 0 {
				p.tab = t
				break
			}
		}
	}
	p.revisible(ranks)
	// The cursor returns to the first row after every filter change, as the
	// plain rich picker does.
	p.cursor = 0
}

// switchTab steps to the next tab that has matches, wrapping around the tab
// bar; when no other tab matches the filter it keeps the tab, leaving the
// filter visible as the reason the list is empty.
func (p *tabbedPickerState) switchTab(step int) {
	if len(p.tabs) == 0 {
		return
	}
	for n := 1; n <= len(p.tabs); n++ {
		t := ((p.tab+step*n)%len(p.tabs) + len(p.tabs)) % len(p.tabs)
		if t == p.tab {
			return
		}
		if p.matches[t] == 0 {
			continue
		}
		p.tab = t
		p.cursor = 0
		_, ranks := p.filterPass()
		p.revisible(ranks)
		return
	}
}

// focus moves to the row's tab and puts the cursor on the row, so a picker
// reopened after a cancelled sub-flow lands where the user left it.
func (p *tabbedPickerState) focus(idx int) {
	if idx < 0 || idx >= len(p.items) || p.items[idx].Disabled {
		return
	}
	p.tab = p.tabOf[idx]
	_, ranks := p.filterPass()
	p.revisible(ranks)
	for ci, i := range p.visible {
		if i == idx {
			p.cursor = ci
			return
		}
	}
	p.cursor = 0
}

func (p *tabbedPickerState) current() int {
	if len(p.visible) == 0 {
		return -1
	}
	return p.visible[p.cursor]
}

func (p *tabbedPickerState) toggle() {
	if !p.multi {
		return
	}
	idx := p.current()
	if idx < 0 {
		return
	}
	if p.checked[idx] {
		delete(p.checked, idx)
	} else {
		p.checked[idx] = true
	}
}

func (p *tabbedPickerState) checkedIndices() []int {
	out := make([]int, 0, len(p.checked))
	for idx, on := range p.checked {
		if on {
			out = append(out, idx)
		}
	}
	sort.Ints(out)
	return out
}

// panel lays the tabbed picker out as a slash panel: the label and the filter
// in the head, the tab bar under them when there is more than one tab, the
// shown tab's rows in two columns, and the keys.
func (p *tabbedPickerState) panel(label string) slashPanel {
	b := newPanelBuilder()
	pickerHead(b, label)
	if len(p.tabs) > 1 {
		b.tabs(p.tabChips())
	}
	for ci, idx := range p.visible {
		lead := ""
		if p.multi {
			lead = selectorUncheckedGlyph + " "
			if p.checked[idx] {
				lead = selectorCheckedGlyph + " "
			}
		}
		b.selectableColumns(ci == p.cursor, lead, p.items[idx].Label, p.nameWidth, strings.TrimSpace(p.items[idx].Description))
	}
	if len(p.visible) == 0 {
		b.text(panelIndent, "No matches", nil)
	}
	b.hint(tabbedFooterHint(len(p.tabs) > 1, p.multi, p.esc))
	panel := b.panel()
	panel.field = p.filter.panelField()
	return panel
}

// tabChips styles the tab bar: the tab shown in the /status tab colour, a tab
// the filter leaves empty dimmed (Tab skips it), its row count beside every
// counted tab.
func (p *tabbedPickerState) tabChips() []string {
	chips := make([]string, len(p.tabs))
	for t, name := range p.tabs {
		text := name
		if !p.uncounted[name] {
			text = name + " " + strconv.Itoa(p.matches[t])
		}
		switch {
		case t == p.tab:
			chips[t] = panelTabStyle.Render(text)
		case p.matches[t] == 0:
			chips[t] = panelHintStyle.Render(text)
		default:
			chips[t] = text
		}
	}
	return chips
}

func tabbedFooterHint(switchable, multi bool, esc string) string {
	if esc == "" {
		esc = "Esc to cancel"
	}
	parts := make([]string, 0, 4)
	if switchable {
		parts = append(parts, "Tab to switch")
	}
	parts = append(parts, "↑/↓ to navigate")
	if multi {
		parts = append(parts, "Space to select")
	}
	parts = append(parts, "Enter to confirm", esc)
	return strings.Join(parts, " · ")
}

// SelectRichTabbed opens the tabbed picker and returns the index into items
// of the row chosen.
func (s *rawSelector) SelectRichTabbed(label string, items []SelectItem, opts TabbedSelectOptions) (int, bool, error) {
	p, ok, err := s.tabbedKeyLoop(label, items, opts, false)
	if err != nil || !ok || p.current() < 0 {
		return -1, false, err
	}
	return p.current(), true, nil
}

// MultiSelectRichTabbed opens the tabbed picker with check boxes and returns
// the indices into items checked on confirm, ascending.
func (s *rawSelector) MultiSelectRichTabbed(label string, items []SelectItem, opts TabbedSelectOptions) ([]int, bool, error) {
	p, ok, err := s.tabbedKeyLoop(label, items, opts, true)
	if err != nil || !ok {
		return nil, false, err
	}
	return p.checkedIndices(), true, nil
}

// tabbedKeyLoop is the one key loop behind both tabbed primitives, shaped
// after selectRichPaged: it renders the panel, then reads keys until the user
// confirms or cancels, reporting ok only for a confirmation.
func (s *rawSelector) tabbedKeyLoop(label string, items []SelectItem, opts TabbedSelectOptions, multi bool) (*tabbedPickerState, bool, error) {
	if len(items) == 0 {
		return nil, false, nil
	}
	release, err := s.acquireComposerInput()
	if err != nil {
		return nil, false, err
	}
	defer release()

	p := newTabbedPickerState(items, opts, multi, s.escHint())
	s.showPanel(p.panel(label))
	defer s.withCaretPlacer(func(line, col int) {
		if s.placeFieldCaret(p.panel(label), &p.filter, line, col) {
			s.showPanel(p.panel(label))
		}
	})()

	for {
		key, err := s.readKey()
		if err != nil {
			return nil, false, err
		}
		switch key.kind {
		case rawKeyNone:
			continue
		case rawKeyMouse:
			s.dispatchMouse(key.mouse)
			continue
		case rawKeyUp:
			if p.cursor > 0 {
				p.cursor--
			}
		case rawKeyDown:
			if p.cursor < len(p.visible)-1 {
				p.cursor++
			}
		case rawKeyTab:
			p.switchTab(1)
		case rawKeyShiftTab:
			p.switchTab(-1)
		case rawKeyEnter:
			if multi {
				return p, true, nil
			}
			if p.current() < 0 {
				return p, false, nil
			}
			return p, true, nil
		case rawKeyEscape, rawKeyCtrlC:
			return nil, false, s.dismissed(key.kind)
		case rawKeyBackspace:
			if p.filter.backspace() {
				p.rebuild()
			}
		case rawKeyLeft:
			p.filter.move(-1)
		case rawKeyRight:
			p.filter.move(1)
		case rawKeyHome:
			p.filter.moveTo(0)
		case rawKeyEnd:
			p.filter.moveTo(len(p.filter.runes()))
		case rawKeyPaste:
			p.filter.insert(strings.ReplaceAll(key.text, "\n", " "))
			p.rebuild()
		case rawKeyRune:
			p.filter.insert(string(key.r))
			p.rebuild()
		case rawKeySpace:
			if multi {
				p.toggle()
			} else {
				p.filter.insert(" ")
				p.rebuild()
			}
		}
		s.showPanel(p.panel(label))
	}
}

var _ TabbedRichSelector = (*rawSelector)(nil)
