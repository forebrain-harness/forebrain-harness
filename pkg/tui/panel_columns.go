package tui

import "strings"

// panelChipGap is the room between two tabs, as /status spaces its tabs.
const panelChipGap = 3

// panelColumnGap is the fixed room between a two-column row's name and its
// description, as the slash menu spaces its commands.
const panelColumnGap = 2

// panelNameColumnMax caps a two-column list's name column, so one long name
// cannot push every description to the right edge; the row layout narrows it
// further to two fifths of the row.
const panelNameColumnMax = 40

// chipRows lays the chips out left to right, panelChipGap cells apart, wrapping
// whole: a chip too wide to join the row starts the next one, so a tab's name
// and its count never part across rows. A chip wider than the whole row only
// happens in a terminal too narrow for any tab bar; it takes a row of its own,
// hard-folded, rather than sharing one or disappearing.
func (l panelLine) chipRows(width int) []string {
	leadW := displayLineWidth(l.lead)
	avail := maxInt(1, width-leadW)
	lead := l.lead
	hang := strings.Repeat(" ", leadW)
	var rows []string
	cur, curW := "", 0
	for _, chip := range l.chips {
		w := displayLineWidth(chip)
		switch {
		case w > avail:
			if cur != "" {
				rows = append(rows, lead+cur)
				lead = hang
				cur = ""
			}
			for _, chunk := range wrapCardLine(chip, avail) {
				rows = append(rows, lead+chunk)
				lead = hang
			}
		case cur == "":
			cur, curW = chip, w
		case curW+panelChipGap+w <= avail:
			cur += strings.Repeat(" ", panelChipGap) + chip
			curW += panelChipGap + w
		default:
			rows = append(rows, lead+cur)
			lead = hang
			cur, curW = chip, w
		}
	}
	if cur != "" || len(rows) == 0 {
		rows = append(rows, lead+cur)
	}
	return rows
}

// columnRows lays one two-column row: the name in its own column, the
// description panelColumnGap cells right of it, each left-aligned and the
// description wrapping inside its column. A name too wide for the column takes
// rows of its own and the description starts under the column, so the
// description column stays put whatever the names look like. A row without a
// description is laid out exactly like a plain row.
func (l panelLine) columnRows(width int) []string {
	if strings.TrimSpace(l.text) == "" {
		return panelLine{lead: l.lead, text: l.col}.rows(width)
	}
	leadW := displayLineWidth(l.lead)
	colW := min(l.colWidth, maxInt(1, (width-leadW)*2/5))
	nameW := displayLineWidth(l.col)
	pad := strings.Repeat(" ", leadW+colW+panelColumnGap)
	texts := wrapPanelWords(strings.TrimSpace(l.text), maxInt(1, width-leadW-colW-panelColumnGap))
	render := func(chunk string) string {
		if l.style != nil && chunk != "" {
			return l.style.Render(chunk)
		}
		return chunk
	}
	if nameW <= colW {
		rows := make([]string, 0, len(texts))
		first := l.lead + l.col + strings.Repeat(" ", colW-nameW+panelColumnGap) + render(texts[0])
		rows = append(rows, first)
		for _, chunk := range texts[1:] {
			rows = append(rows, pad+render(chunk))
		}
		return rows
	}
	rows := make([]string, 0, len(texts)+1)
	for i, chunk := range wrapPanelWords(l.col, maxInt(1, width-leadW)) {
		if i == 0 {
			rows = append(rows, l.lead+chunk)
			continue
		}
		rows = append(rows, strings.Repeat(" ", leadW)+chunk)
	}
	for _, chunk := range texts {
		rows = append(rows, pad+render(chunk))
	}
	return rows
}

// tabs adds the tab bar to the pinned head, one pre-styled chip per tab.
func (b *panelBuilder) tabs(chips []string) {
	b.head = append(b.head, panelLine{lead: panelIndent, chips: chips})
}

// selectableColumns adds one cursor-addressable row in two columns: name in
// a column nameWidth cells wide (at most two fifths of the row), desc in the
// column panelColumnGap cells right of it — each left-aligned, the
// description wrapping inside its own column.
func (b *panelBuilder) selectableColumns(selected bool, lead, name string, nameWidth int, desc string) {
	b.addSelectable(selected, panelLine{lead: lead, col: name, colWidth: nameWidth, text: desc})
}
