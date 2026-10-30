package tui

// plan_drawer.go is the read-only todo drawer the composer's ☑N/M plan-progress
// token opens. It is deliberately small: one split of the history region into a
// narrower transcript plus a right-hand column, one pure layout over the plan
// frame the transcript already holds, and the renderer-side state that makes a
// click, a wheel notch and a hover resolve against the screen that was painted.
//
// Two things it does NOT do, on purpose:
//
//   - It keeps no todo state of its own. The list is the newest FramePlan of the
//     view being shown (Frame.Content, already produced by planText), so a plan
//     update, an item status change and `session_todo clear` reach the drawer by
//     the same path that reaches the transcript.
//   - It does not cover the history. The transcript is laid out at the narrower
//     width and reflows; only the composer, footer and caret keep the full
//     terminal width.

import (
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
)

const (
	// planDrawerMinWidth and planDrawerMaxWidth bound the drawer's column
	// count before the terminal-width cap applies: 120 columns gives 40, 80
	// gives 32, and a narrow terminal gets at most half its width.
	planDrawerMinWidth = 32
	planDrawerMaxWidth = 48
	// planDrawerAccent is the plan frame's own accent colour (the same 141
	// renderCompactFrame and headerStyleForFrame use), so the drawer reads as
	// the same object as the plan card above it.
	planDrawerAccent = "141"
	// planDrawerCloseCells is the width of the close control and of its hit
	// zone: the rightmost three cells of the title row's body.
	planDrawerCloseCells = 3
	// planDrawerChromeCells is everything the drawer spends outside its text
	// body: the divider column, the left padding and the right padding.
	planDrawerChromeCells = 3
)

// planDrawerWidth splits totalWidth into the drawer's own columns and the
// history region's, using the fixed budget the UX contract pins down:
// clamp(totalWidth/3, 32, 48) capped at half the terminal. Integer arithmetic
// only, so every executor and every test computes the same split.
func planDrawerWidth(totalWidth int) (drawerWidth, leftWidth int) {
	if totalWidth < 1 {
		totalWidth = 1
	}
	d := totalWidth / 3
	if d < planDrawerMinWidth {
		d = planDrawerMinWidth
	}
	if d > planDrawerMaxWidth {
		d = planDrawerMaxWidth
	}
	if d > totalWidth/2 {
		d = totalWidth / 2
	}
	if d < 1 {
		d = 1
	}
	return d, totalWidth - d
}

// planDrawerBodyWidth is the number of cells the drawer's own text may use: the
// columns between its padding, i.e. columns 2..D-2 of the split row.
func planDrawerBodyWidth(drawerWidth int) int {
	width := drawerWidth - planDrawerChromeCells
	if width < 1 {
		width = 1
	}
	return width
}

// planDrawerState is the renderer's whole drawer state. view is the active view
// it was opened for: a view switch closes it rather than showing one agent's
// plan beside another agent's transcript. followActive is the reading mode —
// true until the user scrolls the list, which is what lets a plan update pull
// the active item into view without stealing a position the user chose.
type planDrawerState struct {
	open         bool
	view         string
	scroll       int
	followActive bool
}

// planDrawerLayout is one frame's drawer, laid out for a given drawer width and
// body height. rows holds the visible body rows with their styling already
// applied (no divider, no padding — planDrawerScreenRow adds both), so a test
// can assert what the user reads without undoing the cell grid.
type planDrawerLayout struct {
	// width is the drawer's column count and leftWidth the history region's;
	// together they are the terminal width.
	width     int
	leftWidth int
	// title is the frame's own title (Frame.Title, defaulting to Updated Plan).
	title string
	// rows are the visible body rows, each at most planDrawerBodyWidth cells.
	rows []string
	// rowsTotal is how many body rows the content has in full, and scroll the
	// display-row offset rows starts at.
	rowsTotal int
	scroll    int
	// above and below count the display rows still off screen in each
	// direction; they are what the muted overflow hint reports.
	above int
	below int
	// activeRow is the full-list index of the ▣ item's first row, or -1.
	activeRow int
	// hintRow reports whether one body row was reserved for the overflow hint.
	// Without a row to draw it on there is no hint, so the list keeps the space.
	hintRow bool
	// closeSpan is the close control's hit zone in drawer-local cells: the
	// rightmost three body cells of the title row.
	closeSpan screenSpan
}

// planDrawerHint is the muted overflow line the painter puts under the list, or
// "" when the whole list fits or no row was reserved for it.
func (l planDrawerLayout) planDrawerHint() string {
	if !l.hintRow {
		return ""
	}
	switch {
	case l.above > 0 && l.below > 0:
		return strconv.Itoa(l.above) + " above · " + strconv.Itoa(l.below) + " below"
	case l.above > 0:
		return strconv.Itoa(l.above) + " above"
	case l.below > 0:
		return strconv.Itoa(l.below) + " below"
	}
	return ""
}

// planDrawerMaxScroll is how far the list can scroll: the rows that do not fit
// between the fixed title and the overflow hint.
func (l planDrawerLayout) planDrawerMaxScroll() int {
	max := l.rowsTotal - l.visibleRowCount()
	if max < 0 {
		return 0
	}
	return max
}

// visibleRowCount is how many content rows fit under the title and above the
// overflow hint. It is a method of the layout rather than a field so the
// painter and the wheel clamp cannot disagree about it.
func (l planDrawerLayout) visibleRowCount() int {
	return len(l.rows)
}

// buildPlanDrawerLayout lays the plan frame out for a drawer of drawerWidth
// columns and height rows (the history body height the drawer shares), scrolled
// to scroll display rows. followActive keeps the ▣ item in view; once the user
// has scrolled the list themselves the caller passes false and the offset is
// only clamped, never moved to the active item.
func buildPlanDrawerLayout(frame Frame, drawerWidth, height, scroll int, followActive bool) planDrawerLayout {
	width := drawerWidth
	if width < 1 {
		width = 1
	}
	bodyWidth := planDrawerBodyWidth(width)
	rows, activeRow := planDrawerContentRows(frame.Content, bodyWidth)

	layout := planDrawerLayout{
		width:     width,
		title:     strings.TrimSpace(frame.Title),
		rowsTotal: len(rows),
		scroll:    scroll,
		activeRow: activeRow,
	}
	if layout.title == "" {
		layout.title = "Updated Plan"
	}
	// The title row is fixed; the overflow hint, when there is one, is fixed
	// too, so the list scrolls between them.
	contentRows := height - 1
	if contentRows < 0 {
		contentRows = 0
	}
	if len(rows) > contentRows && contentRows > 2 {
		// The hint costs a body row, and it only buys clarity while at least
		// two content rows remain: in a two-row drawer the list itself is the
		// better use of the space.
		contentRows--
		layout.hintRow = true
	}
	if layout.scroll < 0 {
		layout.scroll = 0
	}
	maxScroll := len(rows) - contentRows
	if maxScroll < 0 {
		maxScroll = 0
	}
	if followActive && activeRow >= 0 {
		// Bring the active item's first row into view without scrolling a
		// list that already shows it, so a plan update does not jog a
		// position that is still valid.
		if activeRow < layout.scroll || activeRow >= layout.scroll+contentRows {
			layout.scroll = activeRow
		}
	}
	if layout.scroll > maxScroll {
		layout.scroll = maxScroll
	}
	if layout.scroll < 0 {
		layout.scroll = 0
	}
	end := layout.scroll + contentRows
	if end > len(rows) {
		end = len(rows)
	}
	if layout.scroll < len(rows) {
		layout.rows = rows[layout.scroll:end]
	}
	layout.above = layout.scroll
	layout.below = len(rows) - end
	layout.closeSpan = planDrawerSpanFor(0, width)
	return layout
}

// planDrawerContentRows turns the plan frame's Content into display rows for a
// body of bodyWidth cells.
//
// planText writes the plan's Explanation first and the items after it, and
// emitPlanUpdateStep sets the Explanation to the active item's own label when
// exactly one item is active — which would show the active item twice, once
// without a status glyph. A leading line that is not an item line is therefore
// dropped, unless it is the whole content (an explanation-only or empty plan
// list, which is shown as it stands). Items are soft-wrapped by display width
// with their continuation rows hanging under the item's text: an item's text is
// never ellipsized.
func planDrawerContentRows(content string, bodyWidth int) (rows []string, activeRow int) {
	activeRow = -1
	if bodyWidth < 1 {
		bodyWidth = 1
	}
	lines := strings.Split(content, "\n")
	if len(lines) > 1 && !planDrawerIsItemLine(lines[0]) {
		lines = lines[1:]
	}
	for _, line := range lines {
		line = strings.TrimRight(line, " \t")
		if strings.TrimSpace(line) == "" {
			continue
		}
		glyph, text, ok := planDrawerItemParts(line)
		if !ok {
			for _, part := range wrapCardLine(line, bodyWidth) {
				rows = append(rows, "\x1b[2m"+part+"\x1b[0m")
			}
			continue
		}
		indent := runewidth.StringWidth(glyph) + 1
		textWidth := bodyWidth - indent
		if textWidth < 1 {
			textWidth = 1
		}
		wrapped := wrapCardLine(text, textWidth)
		first := len(rows)
		for i, part := range wrapped {
			if i == 0 {
				rows = append(rows, planDrawerGlyphCell(glyph)+" "+part)
				continue
			}
			rows = append(rows, strings.Repeat(" ", indent)+part)
		}
		if glyph == "▣" && activeRow < 0 {
			activeRow = first
		}
	}
	return rows, activeRow
}

// planDrawerIsItemLine reports whether a Content line is a plan item: planText
// prefixes every item with statusGlyph and one space, and prefixes nothing else.
func planDrawerIsItemLine(line string) bool {
	_, _, ok := planDrawerItemParts(line)
	return ok
}

// planDrawerItemParts splits an item line into its status glyph and the text
// after the single space that follows it.
func planDrawerItemParts(line string) (glyph, text string, ok bool) {
	runes := []rune(line)
	if len(runes) == 0 {
		return "", "", false
	}
	glyph = string(runes[0])
	switch glyph {
	case "✓", "▣", "□", "✕":
	default:
		return "", "", false
	}
	rest := string(runes[1:])
	if rest != "" && !strings.HasPrefix(rest, " ") {
		// A glyph glued to text is not the glyph+space prefix planText writes;
		// treat it as ordinary text rather than eating a character.
		return "", "", false
	}
	return glyph, strings.TrimPrefix(rest, " "), true
}

// planDrawerGlyphCell styles one status glyph: the active item's ▣ in the plan
// accent, every other state faint, so the list has exactly one bright row.
func planDrawerGlyphCell(glyph string) string {
	if glyph == "▣" {
		return "\x1b[38;5;" + planDrawerAccent + "m" + glyph + "\x1b[0m"
	}
	return "\x1b[2m" + glyph + "\x1b[0m"
}

// planDrawerSpanFor is the close control's hit zone expressed in screen cells
// for a drawer occupying the leftWidth..leftWidth+width columns. It covers the
// last three cells of the title row's body: columns 36, 37 and 38 at D = 40,
// i.e. the half-open [D-4, D-1). The row's final cell is the body's right
// padding and stays outside the zone.
func planDrawerSpanFor(leftWidth, width int) screenSpan {
	start := leftWidth + width - planDrawerCloseCells - 1
	return screenSpan{row: 0, startCol: start, endCol: start + planDrawerCloseCells}
}

// planDrawerTitleRow builds the fixed title row's body cells (bodyWidth of
// them): the accent dot, the plan's title, and the close control in the
// rightmost three cells. Hovering the close control gives exactly those three
// cells the shared hover background; nothing else on the row changes.
func (l planDrawerLayout) planDrawerTitleRow(bodyWidth int, closeHover bool) string {
	closeCells := " × "
	if closeHover {
		closeCells = "\x1b[48;5;" + hoverBackgroundColor + "m × \x1b[0m"
	}
	budget := bodyWidth - planDrawerCloseCells - 2
	if budget < 1 {
		budget = 1
	}
	title := runewidth.Truncate(l.title, budget, "…")
	head := "\x1b[38;5;" + planDrawerAccent + "m● " + title + "\x1b[0m"
	spaces := bodyWidth - planDrawerCloseCells - displayLineWidth(head)
	if spaces < 0 {
		spaces = 0
	}
	return head + strings.Repeat(" ", spaces) + closeCells
}

// planDrawerScreenRow composes the drawer's cells for one screen row of the
// drawer's own height: exactly width cells, so the history region beside it and
// the drawer itself sum to the terminal width. Every cell is written
// explicitly — no \x1b[K fill, no cursor jumps inside the row — so the row
// paints the same on a terminal without back-colour-erase.
func (l planDrawerLayout) planDrawerScreenRow(row int, closeHover bool) string {
	bodyWidth := planDrawerBodyWidth(l.width)
	blank := strings.Repeat(" ", bodyWidth)
	var body string
	switch {
	case row == 0:
		body = l.planDrawerTitleRow(bodyWidth, closeHover)
	case row-1 >= 0 && row-1 < len(l.rows):
		body = planDrawerPadCells(l.rows[row-1], bodyWidth)
	case row-1 == len(l.rows) && l.planDrawerHint() != "":
		body = planDrawerPadCells("\x1b[2m"+l.planDrawerHint()+"\x1b[0m", bodyWidth)
	default:
		body = blank
	}
	return planDrawerDividerCell() + " " + body + " "
}

// planDrawerDividerCell is the single muted column that separates the history
// from the drawer.
func planDrawerDividerCell() string {
	return "\x1b[2m│\x1b[0m"
}

// planDrawerBlankCells is the whole-row fill for a split row with no history
// content on it: the left region is blank, so the row is written as its own
// spaces plus the drawer's cells.
func planDrawerBlankCells(layout planDrawerLayout, row int, closeHover bool) string {
	left := strings.Repeat(" ", layout.leftWidth)
	return left + layout.planDrawerScreenRow(row, closeHover)
}

// planDrawerPadCells pads a styled body row to exactly width cells. Styling is
// zero width, so the measure is the visible width, the same one the transcript's
// wrap math uses.
func planDrawerPadCells(text string, width int) string {
	if width < 1 {
		width = 1
	}
	pad := width - displayLineWidth(text)
	if pad <= 0 {
		return text
	}
	return text + strings.Repeat(" ", pad)
}

// hoverBackgroundColor is the shared hover background (SGR colour 236) used by
// the transcript's clickable rows, the plan-progress token and the drawer's
// close control.
const hoverBackgroundColor = "236"

// The local controls that highlight their own cells on hover. A pointer over
// anything else hovers nothing here (the transcript's row hover is a separate
// piece of state: vpHoverRow).
const (
	hoverTargetNone = iota
	hoverTargetProgress
	hoverTargetClose
)

// latestPlanFrameLocked returns the newest plan frame of the view that is on
// screen — the conversation's own, or the viewed subagent's — which is the same
// plan the transcript shows last. It is the drawer's only source of todo state.
// Caller holds r.mu.
func (r *Renderer) latestPlanFrameLocked() (Frame, bool) {
	vm := r.activeVMLocked()
	for i := len(vm.blocks) - 1; i >= 0; i-- {
		if frame := vm.blocks[i].frame; frame.Kind == FramePlan {
			return frame, true
		}
	}
	return Frame{}, false
}

// planDrawerOpenLocked reports whether the drawer is currently on screen: open,
// opened for the view being shown, and still backed by a plan frame.
// Caller holds r.mu.
func (r *Renderer) planDrawerOpenLocked() bool {
	if !r.planDrawer.open || r.planDrawer.view != r.activeView {
		return false
	}
	_, ok := r.latestPlanFrameLocked()
	return ok
}

// planDrawerLayoutLocked lays the open drawer out for the current terminal
// width and history body height. ok is false when there is nothing to lay out
// (the drawer is closed, or the view it belonged to is gone).
// Caller holds r.mu.
func (r *Renderer) planDrawerLayoutLocked(width, bodyHeight int) (planDrawerLayout, bool) {
	if !r.planDrawer.open || r.planDrawer.view != r.activeView {
		return planDrawerLayout{}, false
	}
	frame, ok := r.latestPlanFrameLocked()
	if !ok {
		return planDrawerLayout{}, false
	}
	drawerWidth, leftWidth := planDrawerWidth(width)
	layout := buildPlanDrawerLayout(frame, drawerWidth, bodyHeight, r.planDrawer.scroll, r.planDrawer.followActive)
	layout.leftWidth = leftWidth
	return layout, true
}

// planDrawerSplitRow joins the history region's row and the drawer's cells into
// one physical row: exactly rowWidth + drawerWidth = the terminal width cells,
// with the drawer's divider as the seam.
func planDrawerSplitRow(line string, rowWidth int, layout planDrawerLayout, row int, closeHover bool) string {
	return planDrawerPadCells(line, rowWidth) + layout.planDrawerScreenRow(row, closeHover)
}

// highlightCells paints bgSeq over the cells [startCol, endCol) of one rendered
// row and leaves everything else about the row alone: the range is opened with
// the background and closed with a background-only reset (\x1b[49m), so the
// status line's faint text and the row's other attributes carry on past it.
// Wide runes are copied whole, so a range that ends inside one still highlights
// every cell it occupies.
func highlightCells(row string, startCol, endCol int, bgSeq string) string {
	if startCol >= endCol {
		return row
	}
	var b strings.Builder
	b.Grow(len(row) + 2*len(bgSeq))
	col := 0
	open := false
	for i := 0; i < len(row); {
		if row[i] == 0x1b {
			j := escapeSeqEnd(row, i)
			b.WriteString(row[i:j])
			i = j
			continue
		}
		rn, size := utf8.DecodeRuneInString(row[i:])
		if !open && col >= startCol && col < endCol {
			b.WriteString(bgSeq)
			open = true
		}
		b.WriteString(row[i : i+size])
		col += runewidth.RuneWidth(rn)
		if open && col >= endCol {
			// Close the background on the last cell of the range rather than at
			// the next visible rune: what follows may be the row's own \x1b[K,
			// which a terminal that erases with the current background would
			// then paint in the hover colour to the end of the row.
			b.WriteString("\x1b[49m")
			open = false
		}
		i += size
	}
	if open {
		b.WriteString("\x1b[49m")
	}
	return b.String()
}

// planDrawerHistoryAnchor names the block that sat at the top of the history
// region, and how far into it the view was. Re-laying the transcript out at a
// different width renumbers rows, so this — not a row offset — is what keeps a
// reader's place across a drawer toggle: the same block stays at the top, at the
// same line inside it.
type planDrawerHistoryAnchor struct {
	blockID int
	rel     int
	ok      bool
}

// captureHistoryAnchorLocked records the top of the history region as a block
// and a row inside it. Following the bottom has no anchor worth keeping: the
// newest rows stay the newest rows however they rewrap.
// Caller holds r.mu.
func (r *Renderer) captureHistoryAnchorLocked() planDrawerHistoryAnchor {
	if r.vpFollow || len(r.vpLastRender.lines) == 0 {
		return planDrawerHistoryAnchor{}
	}
	blockID, _ := r.vpLastRender.blockAtRow(0)
	if blockID < 0 {
		return planDrawerHistoryAnchor{}
	}
	span, ok := r.vpLastRender.spanForBlock(blockID)
	if !ok {
		return planDrawerHistoryAnchor{}
	}
	return planDrawerHistoryAnchor{blockID: blockID, rel: r.vpLastRender.firstRow - span.startRow, ok: true}
}

// restoreHistoryAnchorLocked puts the anchored block back at the top of the
// history region after a re-layout, and reports whether the offset moved (the
// caller repaints only then). A block that is gone — a reset, a view switch, a
// folded block that vanished — falls back to following the bottom, which is the
// one place that always exists. Caller holds r.mu.
func (r *Renderer) restoreHistoryAnchorLocked(anchor planDrawerHistoryAnchor) bool {
	if !anchor.ok {
		return false
	}
	span, ok := r.vpLastRender.spanForBlock(anchor.blockID)
	if !ok {
		if r.vpFollow {
			return false
		}
		r.vpFollow = true
		r.vpScrollOffset = r.vpLastRender.maxScrollOffset()
		return true
	}
	rel := anchor.rel
	if rel >= span.rowCount {
		rel = span.rowCount - 1
	}
	if rel < 0 {
		rel = 0
	}
	offset := span.startRow + rel
	if max := r.vpLastRender.maxScrollOffset(); offset > max {
		offset = max
	}
	if offset == r.vpScrollOffset {
		return false
	}
	r.vpScrollOffset = offset
	return true
}

// togglePlanDrawerLocked opens or closes the drawer and repaints the split, so
// one click produces one stable frame: no per-column animation, and the reading
// position survives the reflow. Caller holds r.mu.
func (r *Renderer) togglePlanDrawerLocked() bool {
	if r.planDrawer.open {
		return r.closePlanDrawerLocked()
	}
	return r.openPlanDrawerLocked()
}

// openPlanDrawerLocked opens the drawer for the view on screen. A view with no
// plan frame yet opens nothing: an empty drawer would be a second, less useful
// copy of the empty screen. Caller holds r.mu.
func (r *Renderer) openPlanDrawerLocked() bool {
	if _, ok := r.latestPlanFrameLocked(); !ok {
		return false
	}
	anchor := r.captureHistoryAnchorLocked()
	r.planDrawer = planDrawerState{open: true, view: r.activeView, followActive: true}
	r.repaintAfterDrawerToggleLocked(anchor)
	return true
}

// closePlanDrawerLocked closes the drawer and gives the history its width back,
// keeping the reader on the block they were looking at. Caller holds r.mu.
func (r *Renderer) closePlanDrawerLocked() bool {
	if !r.planDrawer.open {
		return false
	}
	anchor := r.captureHistoryAnchorLocked()
	r.planDrawer = planDrawerState{}
	r.repaintAfterDrawerToggleLocked(anchor)
	return true
}

// repaintAfterDrawerToggleLocked paints the new split and then restores the
// reading anchor. The second paint happens only when the anchor moved the
// offset: the first paint is what wraps the blocks at the new width, and the
// block spans it returns are the only place the anchored block's new row is
// known. Caller holds r.mu.
func (r *Renderer) repaintAfterDrawerToggleLocked(anchor planDrawerHistoryAnchor) {
	r.vpControlHover = hoverTargetNone
	r.vpHoverRow = -1
	r.paintViewportLocked()
	if r.restoreHistoryAnchorLocked(anchor) {
		r.paintViewportLocked()
	}
}

// ClosePlanDrawer closes the todo drawer if it is open, reporting whether it
// closed one. Esc is handled with it: the drawer is the lowest-priority surface
// on screen, so a key that would otherwise cancel the run closes it first.
func (r *Renderer) ClosePlanDrawer() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.viewportMode {
		return false
	}
	return r.closePlanDrawerLocked()
}

// scrollPlanDrawerLocked scrolls the list by delta display rows, clamped to its
// ends. It reports whether the list moved; a notch at either end reports false
// so the caller can hand it to the transcript instead of swallowing it. A manual
// scroll also ends follow-the-active-item mode, so a later plan update cannot
// pull the reader back. Caller holds r.mu.
func (r *Renderer) scrollPlanDrawerLocked(delta int) bool {
	if delta == 0 || !r.planDrawer.open {
		return false
	}
	next := r.planDrawer.scroll + delta
	if max := r.vpDrawerLayout.planDrawerMaxScroll(); next > max {
		next = max
	}
	if next < 0 {
		next = 0
	}
	if next == r.planDrawer.scroll {
		return false
	}
	r.planDrawer.scroll = next
	r.planDrawer.followActive = false
	r.paintViewportLocked()
	return true
}

// planDrawerClickLocked resolves one plain click against the screen s, in the
// order the contract fixes: the progress token toggles the drawer, the close
// control closes it, a click inside it is consumed, and a click outside it closes
// it and stops there — the click must not also fold a card, open a subagent,
// move the composer's caret or jump to the bottom. handled reports that this
// gesture was the drawer's; changed reports that the screen changed. Caller
// holds r.mu.
//
// A gesture the drawer consumes also ends the multi-click sequence: the next
// press acts on the new screen as a first click, so repeating a dismissal on the
// same cell folds the card under it instead of being read as the second click of
// a double-click.
func (r *Renderer) planDrawerClickLocked(s shownScreen, col, row int) (handled, changed bool) {
	handled, changed = r.planDrawerConsumeClickLocked(s, col, row)
	if handled {
		r.lastClickTime = time.Time{}
		r.lastClickCol, r.lastClickRow = -1, -1
		r.clickCount = 0
	}
	return handled, changed
}

// planDrawerConsumeClickLocked is planDrawerClickLocked without the click
// sequence bookkeeping. Caller holds r.mu.
func (r *Renderer) planDrawerConsumeClickLocked(s shownScreen, col, row int) (handled, changed bool) {
	if s.progress.contains(col, row) {
		return true, r.togglePlanDrawerLocked()
	}
	if !s.drawerOpen {
		return false, false
	}
	if s.drawerClose.contains(col, row) {
		return true, r.closePlanDrawerLocked()
	}
	if col >= s.drawerLeft && row < s.bodyHeight {
		// Inside the list: a click neither closes it nor reaches the cells the
		// history region would have had there.
		return true, false
	}
	return true, r.closePlanDrawerLocked()
}

// planDrawerPressLocked reports whether a press at this cell is on a drawer
// control or inside the open drawer — the cells where a press must arm the
// control instead of starting a text selection, exactly as the jump control
// does. Caller holds r.mu.
func (r *Renderer) planDrawerPressLocked(s shownScreen, col, row int) bool {
	if s.progress.contains(col, row) || s.drawerClose.contains(col, row) {
		return true
	}
	return s.drawerOpen && col >= s.drawerLeft && row < s.bodyHeight
}

// planDrawerHoverTarget identifies the local control under a screen cell of s.
func planDrawerHoverTarget(s shownScreen, col, row int) int {
	switch {
	case s.progress.contains(col, row):
		return hoverTargetProgress
	case s.drawerClose.contains(col, row):
		return hoverTargetClose
	}
	return hoverTargetNone
}

// planProgressToken locates the ☑N/M token in a rendered composer status line's
// plain text and returns its display-column span. ok is false when the line
// (already folded and truncated for the terminal width) does not show a
// complete token — the entry's hit zone has to be exactly what is on screen, so
// a token the truncation cut is not an entry.
func planProgressToken(text string) (startCol, endCol int, ok bool) {
	idx := strings.Index(text, "☑")
	if idx < 0 {
		return 0, 0, false
	}
	tail := text[idx:]
	digitsFrom := func(i int) int {
		for i < len(tail) && tail[i] >= '0' && tail[i] <= '9' {
			i++
		}
		return i
	}
	done := digitsFrom(len("☑"))
	if done == len("☑") || done >= len(tail) || tail[done] != '/' {
		return 0, 0, false
	}
	end := digitsFrom(done + 1)
	if end == done+1 {
		return 0, 0, false
	}
	if strings.HasPrefix(tail[end:], "…") {
		// The drawn text is the head of a longer number, not a whole token.
		return 0, 0, false
	}
	startCol = runewidth.StringWidth(text[:idx])
	return startCol, startCol + runewidth.StringWidth(tail[:end]), true
}
