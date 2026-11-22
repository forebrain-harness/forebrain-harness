package tui

import (
	"strings"

	"github.com/mattn/go-runewidth"
)

// panelChipGap is the room between two tabs, as /status spaces its tabs.
const panelChipGap = 3

// panelColumnGap is the fixed room between a two-column row's name and its
// description, as the slash menu spaces its commands.
const panelColumnGap = 2

// panelNameColumnMax caps a two-column list's name column, so one long name
// cannot push every description to the right edge; the row layout narrows it
// further to two fifths of the row.
const panelNameColumnMax = 40

// panelValueColumnMax caps a row's value track, so one long path or model name
// cannot take the row: a longer value is elided with an ellipsis, the way the
// prototype's value column cuts it (.item-value max-width:44ch, plan 325 D3).
// Only the /config panel sets a value.
const panelValueColumnMax = 44

// panelNarrowValueColumnMax is the cap the same track takes on a narrow
// terminal (the prototype's .narrow .item-value max-width:24ch, plan 325 D3).
const panelNarrowValueColumnMax = 24

// panelNarrowWidth is the width below which a panel is narrow: its rows hide
// the status and cap the value tighter (prototype .terminal.narrow, applied
// under 100 columns, plan 325 D3).
const panelNarrowWidth = 100

// panelStatusColumnMax is the status track's fixed width, so every status on
// a page ends at the same column whatever its own length (the prototype's
// .field-table .item-status flex-basis:16ch, plan 325 D3).
const panelStatusColumnMax = 16

// chipRows lays the chips out left to right, panelChipGap cells apart, wrapping
// whole: a chip too wide to join the row starts the next one, so a tab's name
// and its count never part across rows. A chip wider than the whole row only
// happens in a terminal too narrow for any tab bar; it takes a row of its own,
// hard-folded, rather than sharing one or disappearing.
func (l panelLine) chipRows(width int) []string {
	chips := l.chips
	if width < panelNarrowWidth && len(l.chipsNarrow) > 0 {
		chips = l.chipsNarrow
	}
	leadW := displayLineWidth(l.lead)
	avail := maxInt(1, width-leadW)
	lead := l.lead
	hang := strings.Repeat(" ", leadW)
	var rows []string
	cur, curW := "", 0
	for _, chip := range chips {
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

// descriptionRows lays one two-column row's description into width cells: a
// row with an elideTail keeps that tail whole and cuts the text around it, and
// every other row wraps.
func (l panelLine) descriptionRows(width int) []string {
	if l.elideTail != "" {
		return elideEndpoint(strings.TrimSpace(l.text), l.elideTail, width)
	}
	return wrapPanelWords(strings.TrimSpace(l.text), width)
}

// elideEndpoint lays one endpoint into a description column narrower than the
// URL, keeping the api path whole: the base URL is cut from its right end and…
// marks the cut, so https://ark…/v1/messages still names the protocol it speaks.
// A row that names no path — 「自定义协议」, which sends to its base url as stored
// — has nothing to keep, so its whole URL takes the same cut.
func elideEndpoint(url, tail string, width int) []string {
	if width < 1 {
		return []string{url}
	}
	if displayLineWidth(url) <= width {
		return []string{url}
	}
	tail = strings.TrimSpace(tail)
	if tail == "" || displayLineWidth(tail)+1 >= width {
		return []string{runewidth.Truncate(firstNonEmpty(tail, url), width, "")}
	}
	room := width - displayLineWidth(tail) - 1
	front, used := "", 0
	for _, r := range strings.TrimSuffix(url, tail) {
		runes := runewidth.RuneWidth(r)
		if used+runes > room {
			break
		}
		front += string(r)
		used += runes
	}
	return []string{front + "…" + tail}
}

// columnRows lays one two-column row: the name in its own column, the
// description panelColumnGap cells right of it, each left-aligned and the
// description wrapping inside its column. A name too wide for the column
// turns the row into a stacked pair instead: the name on its own rows, the
// description hung under the name — never indented to the description
// column, which would float it in the middle of the panel. A row without a
// description is laid out exactly like a plain row.
//
// A row with a value, a status, or both reserves each a track of its own at
// the right edge and the description column narrows by them: the /config
// panel's value column and its "Primary"/"Default" marker (plans 313, 321).
// Both are drawn whole on the row's first line — never wrapped, truncated or
// given a line of their own — and a stacked row drops them, the way the
// prototype hides its right-hand columns on a narrow terminal.
func (l panelLine) columnRows(width int) []string {
	if strings.TrimSpace(l.text) == "" {
		// A row with no description keeps its mark right after the name, one
		// space over, so a gear never floats in a column of its own.
		if l.mark != "" {
			return l.withTrailingTracks(panelLine{lead: l.lead, text: l.col + " " + l.mark}.rows(width), width)
		}
		return l.withTrailingTracks(panelLine{lead: l.lead, text: l.col}.rows(width), width)
	}
	leadW := displayLineWidth(l.lead)
	colW := min(l.colWidth, maxInt(1, (width-leadW)*2/5))
	nameW := displayLineWidth(l.col)
	tracksW := l.trailingTrackWidth(width)
	pad := strings.Repeat(" ", leadW+colW+panelColumnGap)
	texts := l.descriptionRows(maxInt(1, width-leadW-colW-panelColumnGap-tracksW))
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
		return l.withTrailingTracks(rows, width)
	}
	return l.columnRowsStacked(width)
}

// trailingTracks is the row's right-hand tracks as they are drawn at width —
// the value, the gap between the two, then the status. The value takes the
// room it needs capped at panelValueColumnMax (24 on a narrow panel) and is
// elided only when that cap bites; the status sits in a fixed
// panelStatusColumnMax track so the statuses of one page align, and a narrow
// panel hides it the way the prototype's does (plan 325 D3). Empty when the
// row has neither.
func (l panelLine) trailingTracks(width int) []string {
	tracks := make([]string, 0, 2)
	if value := strings.TrimSpace(l.value); value != "" {
		cap := panelValueColumnMax
		if width < panelNarrowWidth {
			cap = panelNarrowValueColumnMax
		}
		tracks = append(tracks, runewidth.Truncate(value, cap, "…"))
	}
	if width >= panelNarrowWidth {
		if status := strings.TrimSpace(l.status); status != "" {
			tracks = append(tracks, padStatusTrack(status))
		}
	}
	return tracks
}

// padStatusTrack lays one status in the fixed status track: left-padded to
// panelStatusColumnMax cells so it right-aligns with every other status on
// the page. A status longer than the track keeps its whole word — statuses
// are the panel's own vocabulary, and none of it is that long.
func padStatusTrack(status string) string {
	pad := panelStatusColumnMax - displayLineWidth(status)
	if pad <= 0 {
		return status
	}
	return strings.Repeat(" ", pad) + status
}

// trailingTrackWidth is the room a row's value and status take out of its
// description column at width: both tracks, the gap before the first and the
// gap between them, and nothing when the row carries neither.
func (l panelLine) trailingTrackWidth(width int) int {
	tracks := l.trailingTracks(width)
	if len(tracks) == 0 {
		return 0
	}
	return displayLineWidth(strings.Join(tracks, strings.Repeat(" ", panelColumnGap))) + panelColumnGap
}

// withTrailingTracks puts the row's value and status in their tracks at the
// right edge of the first line. Neither — or no room left for them — leaves the
// rows exactly as they were: the label is never shortened to fit, and never
// moves to a later line.
func (l panelLine) withTrailingTracks(rows []string, width int) []string {
	raw := l.trailingTracks(width)
	if len(raw) == 0 || len(rows) == 0 {
		return rows
	}
	// The value reads in the terminal's own foreground — the brightness of a
	// setting in force — while the status stays faint, as every status does.
	drawn := make([]string, 0, len(raw))
	for i, track := range raw {
		if i == 0 && strings.TrimSpace(l.value) != "" {
			drawn = append(drawn, track)
			continue
		}
		drawn = append(drawn, panelHintStyle.Render(track))
	}
	gap := strings.Repeat(" ", panelColumnGap)
	pad := width - displayLineWidth(rows[0]) - displayLineWidth(strings.Join(raw, gap))
	if pad < 0 {
		return rows
	}
	styled := append([]string(nil), rows...)
	styled[0] += strings.Repeat(" ", pad) + strings.Join(drawn, gap)
	return styled
}

// columnRowsStacked lays a two-column row that ran out of column as a
// vertical pair: the name wrapped at the row's width, the description laid out
// the same way directly under it, both hung at the name's left edge. A row
// without a description is laid out exactly like a plain row.
func (l panelLine) columnRowsStacked(width int) []string {
	if strings.TrimSpace(l.text) == "" {
		return panelLine{lead: l.lead, text: l.col}.rows(width)
	}
	leadW := displayLineWidth(l.lead)
	hang := strings.Repeat(" ", leadW)
	render := func(chunk string) string {
		if l.style != nil && chunk != "" {
			return l.style.Render(chunk)
		}
		return chunk
	}
	rows := make([]string, 0, 4)
	for i, chunk := range wrapPanelWords(l.col, maxInt(1, width-leadW)) {
		if i == 0 {
			rows = append(rows, l.lead+chunk)
			continue
		}
		rows = append(rows, hang+chunk)
	}
	for _, chunk := range l.descriptionRows(maxInt(1, width-leadW)) {
		rows = append(rows, hang+render(chunk))
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
// description wrapping inside its own column. elideTail, when set, is the end
// of desc that column must keep visible (an endpoint's api path); every other
// row passes "" and wraps. mark, when given, is an already-styled glyph drawn
// tight against the name — the /model effort picker's gear; no other list
// passes one.
func (b *panelBuilder) selectableColumns(selected bool, lead, name string, nameWidth int, desc, elideTail string, mark ...string) {
	b.selectableColumnsValue(selected, lead, name, nameWidth, desc, elideTail, "", "", mark...)
}

// selectableColumnsStatus is selectableColumns with the row's status — the
// quiet label in its own track at the panel's right edge, the /config
// definitions list's "Primary" (plan 313). It is a second entry point rather
// than a second variadic, so every list that draws no status keeps its call as
// it was.
func (b *panelBuilder) selectableColumnsStatus(selected bool, lead, name string, nameWidth int, desc, elideTail, status string, mark ...string) {
	b.selectableColumnsValue(selected, lead, name, nameWidth, desc, elideTail, "", status, mark...)
}

// selectableColumnsValue is the full row: the same two columns, then the
// row's value in a right-aligned track and its status beyond it — the /config
// panel's name, value and status (plan 321). A list that draws neither keeps
// its own entry point above.
func (b *panelBuilder) selectableColumnsValue(selected bool, lead, name string, nameWidth int, desc, elideTail, value, status string, mark ...string) {
	m := ""
	if len(mark) > 0 {
		m = mark[0]
	}
	b.addSelectable(selected, panelLine{lead: lead, col: name, colWidth: nameWidth, text: desc, elideTail: elideTail, mark: m, value: value, status: status})
}
