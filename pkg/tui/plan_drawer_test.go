package tui

// plan_drawer_test.go covers the todo drawer: the width split, the pure layout
// over the newest plan frame, what the list shows (and what it deliberately
// hides), and the scroll/clamp behaviour the wheel and plan updates share.
//
// These tests drive the same entries the product does — buildPlanDrawerLayout
// for the layout, the renderer's latestPlanFrameLocked for the data — so a
// change to planText or to the frame routing shows up here rather than only on a
// real screen. They set the package-level terminal size, so none of them may run
// in parallel.

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/mattn/go-runewidth"
)

// planFrameFor builds the plan frame exactly as PlanUpdatedMsg does, so the
// drawer reads the same Content the transcript card shows.
func planFrameFor(payload event.PlanUpdatedPayload) Frame {
	return Frame{Kind: FramePlan, Title: planTitle(payload), Content: planText(payload), Final: true, AgentID: payload.AgentID}
}

func planTodoItem(content, status string) event.PlanUpdateItem {
	return event.PlanUpdateItem{Content: content, Status: status}
}

// planDrawerTestFrame is the seven-item list the real-machine acceptance uses,
// including one long CJK item that must wrap rather than ellipsize.
func planDrawerTestFrame() Frame {
	return planFrameFor(event.PlanUpdatedPayload{
		Items: []event.PlanUpdateItem{
			planTodoItem("归档计划并保留完整中文长路径，验证历史与抽屉都自动软换行而不截断", "completed"),
			planTodoItem("构建修复前二进制", "completed"),
			planTodoItem("新增回归测试", "completed"),
			planTodoItem("修改 pkg/tui/input_events.go 的鼠标交互并验证 hover", "in_progress"),
			planTodoItem("验证中文、路径和很长的事项正文在右侧抽屉中完整换行显示，不出现省略号", "pending"),
			planTodoItem("tmux 真机 A/B 验收", "pending"),
			planTodoItem("收口并清理临时环境", "pending"),
		},
	})
}

// plainPlanDrawerRows strips the styling so a test can read what the user sees.
func plainPlanDrawerRows(rows []string) []string {
	out := make([]string, len(rows))
	for i, row := range rows {
		out[i] = sgrPattern.ReplaceAllString(row, "")
	}
	return out
}

func TestPlanDrawerWidth(t *testing.T) {
	cases := []struct {
		total, drawer, left int
	}{
		{120, 40, 80},
		{80, 32, 48},
		{60, 30, 30},
		{200, 48, 152},
		{100, 33, 67},
		{40, 20, 20},
		{20, 10, 10},
	}
	for _, tc := range cases {
		drawer, left := planDrawerWidth(tc.total)
		if drawer != tc.drawer || left != tc.left {
			t.Fatalf("planDrawerWidth(%d) = (%d,%d), want (%d,%d)", tc.total, drawer, left, tc.drawer, tc.left)
		}
		if drawer+left != tc.total {
			t.Fatalf("planDrawerWidth(%d) splits into %d columns, want %d", tc.total, drawer+left, tc.total)
		}
		if drawer*2 > tc.total {
			t.Fatalf("planDrawerWidth(%d) gives the drawer %d columns, more than half the terminal", tc.total, drawer)
		}
	}
}

// Every row of a split frame is exactly the terminal width: the history region
// plus the drawer's own cells. A row one cell short leaves a hole at the right
// edge; one cell long wraps the terminal and shifts every row below it.
func TestPlanDrawerLayoutRowsFillTheTerminalWidth(t *testing.T) {
	frame := planDrawerTestFrame()
	for _, total := range []int{120, 80, 60} {
		drawerWidth, leftWidth := planDrawerWidth(total)
		layout := buildPlanDrawerLayout(frame, drawerWidth, 30, 0, true)
		layout.leftWidth = leftWidth
		for row := 0; row < 30; row++ {
			line := planDrawerBlankCells(layout, row, false)
			if got := displayLineWidth(line); got != total {
				t.Fatalf("width %d row %d paints %d cells, want %d: %q", total, row, got, total, line)
			}
			if strings.Contains(line, "\x1b[K") {
				t.Fatalf("width %d row %d fills its tail with \\x1b[K: %q", total, row, line)
			}
		}
	}
}

func TestPlanDrawerLayoutTitleAndCloseSpan(t *testing.T) {
	layout := buildPlanDrawerLayout(planDrawerTestFrame(), 40, 30, 0, true)
	if layout.title != "Updated Plan" {
		t.Fatalf("title = %q, want the plan frame's default title", layout.title)
	}
	// The close control occupies the title row's rightmost three body cells:
	// half-open [D-4, D-1) — columns 36, 37 and 38 for D = 40.
	if layout.closeSpan.row != 0 || layout.closeSpan.startCol != 36 || layout.closeSpan.endCol != 39 {
		t.Fatalf("close span = %+v, want row 0, cols [36,39)", layout.closeSpan)
	}
	titleRow := sgrPattern.ReplaceAllString(layout.planDrawerScreenRow(0, false), "")
	if !strings.Contains(titleRow, "Updated Plan") || !strings.Contains(titleRow, "×") {
		t.Fatalf("title row = %q, want the title and the close glyph", titleRow)
	}
	if got := displayLineWidth(layout.planDrawerScreenRow(0, false)); got != 40 {
		t.Fatalf("title row paints %d cells, want 40", got)
	}
	// The divider opens the drawer's column and the close control closes the
	// title row's body, followed by the body's one-cell right padding.
	if !strings.HasPrefix(titleRow, "│ ") {
		t.Fatalf("title row = %q, want it to open with the divider and one pad", titleRow)
	}
	if !strings.HasSuffix(titleRow, " ×  ") {
		t.Fatalf("title row = %q, want it to end with the close control", titleRow)
	}
	if !layout.closeSpan.contains(37, 0) || layout.closeSpan.contains(39, 0) || layout.closeSpan.contains(35, 0) {
		t.Fatalf("close span %+v is not the title row's last three body cells", layout.closeSpan)
	}
	// Hovering the close control paints only its own three cells.
	hovered := layout.planDrawerScreenRow(0, true)
	if !strings.Contains(hovered, "\x1b[48;5;"+hoverBackgroundColor+"m × \x1b[0m") {
		t.Fatalf("hovered title row = %q, want the close control on the hover background", hovered)
	}
	if got := displayLineWidth(hovered); got != 40 {
		t.Fatalf("hovered title row paints %d cells, want 40", got)
	}
}

// A plan whose title is long, and a terminal narrower than the title needs, must
// still produce a title row of exactly the drawer width.
func TestPlanDrawerLayoutLongTitleKeepsTheCloseControl(t *testing.T) {
	frame := planFrameFor(event.PlanUpdatedPayload{
		Title: "一条非常非常长的中文计划标题，长度远超抽屉宽度所允许的范围，用于验证标题截断后关闭控件仍在原位",
		Items: []event.PlanUpdateItem{planTodoItem("唯一事项", "in_progress")},
	})
	layout := buildPlanDrawerLayout(frame, 40, 10, 0, true)
	row := layout.planDrawerScreenRow(0, false)
	if got := displayLineWidth(row); got != 40 {
		t.Fatalf("long-title row paints %d cells, want 40", got)
	}
	if layout.closeSpan.contains(37, 0) == false {
		t.Fatalf("close span %+v does not cover the title row's last cells", layout.closeSpan)
	}
}

func TestPlanDrawerLatestFrame(t *testing.T) {
	r := newComposerRenderer(t, 120, 40)
	r.RenderFrame(Frame{Kind: FrameStatus, Final: true, Content: "working"})
	first := planFrameFor(event.PlanUpdatedPayload{Items: []event.PlanUpdateItem{planTodoItem("第一版事项", "in_progress")}})
	r.RenderFrame(first)
	second := planFrameFor(event.PlanUpdatedPayload{Items: []event.PlanUpdateItem{planTodoItem("第二版事项", "completed")}})
	r.RenderFrame(second)

	r.mu.Lock()
	got, ok := r.latestPlanFrameLocked()
	r.mu.Unlock()
	if !ok {
		t.Fatal("latestPlanFrameLocked found no plan frame")
	}
	if !strings.Contains(got.Content, "第二版事项") || strings.Contains(got.Content, "第一版事项") {
		t.Fatalf("latest plan frame = %q, want the newest one", got.Content)
	}

	// A subagent's view answers with that subagent's own plan, and the
	// conversation keeps its own: the drawer never mixes two agents' lists.
	sub := planFrameFor(event.PlanUpdatedPayload{
		AgentID: "sub-1",
		Items:   []event.PlanUpdateItem{planTodoItem("子代理事项", "pending")},
	})
	vm := r.ensurePerAgentVM("sub-1")
	vm.append(sub)

	r.SetActiveView("sub-1")
	r.mu.Lock()
	got, ok = r.latestPlanFrameLocked()
	r.mu.Unlock()
	if !ok || !strings.Contains(got.Content, "子代理事项") {
		t.Fatalf("subagent view's latest plan frame = %q (ok=%v), want the subagent's own list", got.Content, ok)
	}
	r.SetActiveView("")
	r.mu.Lock()
	got, ok = r.latestPlanFrameLocked()
	r.mu.Unlock()
	if !ok || !strings.Contains(got.Content, "第二版事项") {
		t.Fatalf("conversation's latest plan frame = %q (ok=%v), want its own list back", got.Content, ok)
	}
}

// A plan list is shown with its own status glyphs and hanging indents; the
// Explanation line planText writes first is only shown when it IS the content.
func TestPlanDrawerContent(t *testing.T) {
	layout := buildPlanDrawerLayout(planDrawerTestFrame(), 40, 40, 0, true)
	rows := plainPlanDrawerRows(layout.rows)
	if len(rows) == 0 {
		t.Fatal("drawer laid out no rows for a seven-item plan")
	}
	join := strings.Join(rows, "\n")
	for _, want := range []string{"归档计划", "构建修复前二进制", "新增回归测试", "input_events.go", "tmux 真机 A/B 验收", "收口并清理临时环境"} {
		if !strings.Contains(join, want) {
			t.Fatalf("drawer rows are missing %q:\n%s", want, join)
		}
	}
	for _, row := range rows {
		if strings.Contains(row, "…") {
			t.Fatalf("drawer row is ellipsized: %q", row)
		}
		if got := displayLineWidth(row); got > planDrawerBodyWidth(40) {
			t.Fatalf("drawer row is %d cells wide, over the %d-cell body: %q", got, planDrawerBodyWidth(40), row)
		}
	}
	// The active item is the one bright row; its continuation rows hang under
	// the item's text rather than under the glyph.
	if layout.activeRow < 0 {
		t.Fatal("drawer found no ▣ item")
	}
	activeRow := sgrPattern.ReplaceAllString(layout.rows[layout.activeRow], "")
	if !strings.HasPrefix(activeRow, "▣ ") {
		t.Fatalf("active row = %q, want it to start with the ▣ glyph", activeRow)
	}
	for i := layout.activeRow + 1; i < len(layout.rows); i++ {
		row := sgrPattern.ReplaceAllString(layout.rows[i], "")
		if strings.HasPrefix(row, " ") && !strings.HasPrefix(row, "▣") {
			// A wrapped continuation of some item: it is indented past the
			// glyph column so text lines up with the item's own text.
			if !strings.HasPrefix(row, "  ") {
				t.Fatalf("continuation row %d is not aligned with the item text: %q", i, row)
			}
			continue
		}
		break
	}

	// planText writes the Explanation first, and emitPlanUpdateStep sets it to
	// the active item's own label. That line carries no status glyph, so the
	// drawer drops it instead of showing the active item twice.
	withExplanation := planFrameFor(event.PlanUpdatedPayload{
		Explanation: "修改 pkg/tui/input_events.go 的鼠标交互并验证 hover",
		Items: []event.PlanUpdateItem{
			planTodoItem("构建修复前二进制", "completed"),
			planTodoItem("修改 pkg/tui/input_events.go 的鼠标交互并验证 hover", "in_progress"),
		},
	})
	explained := buildPlanDrawerLayout(withExplanation, 40, 20, 0, true)
	active := 0
	for _, row := range plainPlanDrawerRows(explained.rows) {
		if strings.HasPrefix(strings.TrimRight(row, " "), "修改 pkg/tui") {
			t.Fatalf("the explanation line is drawn as an item-less row: %q\n%s", row, strings.Join(plainPlanDrawerRows(explained.rows), "\n"))
		}
		if strings.HasPrefix(row, "▣ ") {
			active++
		}
	}
	if active != 1 {
		t.Fatalf("the active item is drawn on %d rows, want exactly one glyph row:\n%s", active, strings.Join(plainPlanDrawerRows(explained.rows), "\n"))
	}

	// An emptied list is shown as its placeholder, not as nothing at all.
	empty := planFrameFor(event.PlanUpdatedPayload{})
	emptyLayout := buildPlanDrawerLayout(empty, 40, 20, 0, true)
	if got := strings.Join(plainPlanDrawerRows(emptyLayout.rows), "\n"); !strings.Contains(got, "(empty plan)") {
		t.Fatalf("empty plan drawer = %q, want the (empty plan) placeholder", got)
	}
}

// A CJK item wider than the body wraps onto as many rows as it needs, with no
// character split in half and no ellipsis.
func TestPlanDrawerContentWrapsCJKLines(t *testing.T) {
	long := "验证中文、路径和很长的事项正文在右侧抽屉中完整换行显示，不出现省略号，并且每一行都不超过抽屉正文的显示宽度"
	frame := planFrameFor(event.PlanUpdatedPayload{Items: []event.PlanUpdateItem{planTodoItem(long, "pending")}})
	layout := buildPlanDrawerLayout(frame, 40, 40, 0, true)
	rows := plainPlanDrawerRows(layout.rows)
	if len(rows) < 2 {
		t.Fatalf("long CJK item stayed on %d row(s), want it wrapped: %q", len(rows), rows)
	}
	var joined strings.Builder
	for _, row := range rows {
		if got := displayLineWidth(row); got > planDrawerBodyWidth(40) {
			t.Fatalf("wrapped row is %d cells wide, over the %d-cell body: %q", got, planDrawerBodyWidth(40), row)
		}
		joined.WriteString(strings.TrimSpace(row))
	}
	if !strings.Contains(joined.String(), long) {
		t.Fatalf("wrapping lost item text:\n%s", joined.String())
	}
}

func TestPlanDrawerScroll(t *testing.T) {
	frame := planDrawerTestFrame()
	const height = 8

	// Opening focuses the active item: with seven items and a short drawer the
	// list scrolls far enough to show the ▣ row instead of leaving it below the
	// fold.
	focused := buildPlanDrawerLayout(frame, 40, height, 0, true)
	if focused.activeRow < 0 {
		t.Fatal("no active row to focus")
	}
	if focused.activeRow < focused.scroll || focused.activeRow >= focused.scroll+len(focused.rows) {
		t.Fatalf("opening left the active row %d outside the visible rows [%d,%d)", focused.activeRow, focused.scroll, focused.scroll+len(focused.rows))
	}
	if focused.above != focused.scroll {
		t.Fatalf("above = %d, want the scroll offset %d", focused.above, focused.scroll)
	}

	// Once the user has scrolled the list themselves, a plan update only
	// clamps the offset: it never pulls the list back to the active item.
	manual := buildPlanDrawerLayout(frame, 40, height, 2, false)
	if manual.scroll != 2 {
		t.Fatalf("manual scroll moved to %d, want to stay at 2", manual.scroll)
	}
	// An offset past the end is clamped, and the leftover is what the overflow
	// hint counts in display rows.
	clamped := buildPlanDrawerLayout(frame, 40, height, 1<<20, false)
	if clamped.below != 0 {
		t.Fatalf("below = %d at the end of the list, want 0", clamped.below)
	}
	if clamped.scroll != clamped.rowsTotal-len(clamped.rows) {
		t.Fatalf("clamped scroll = %d, want %d (rows %d, visible %d)", clamped.scroll, clamped.rowsTotal-len(clamped.rows), clamped.rowsTotal, len(clamped.rows))
	}
	if clamped.above == 0 {
		t.Fatalf("a list scrolled to its end reports no rows above: %+v", clamped)
	}

	// A list that fits does not pretend to scroll.
	short := buildPlanDrawerLayout(planFrameFor(event.PlanUpdatedPayload{Items: []event.PlanUpdateItem{planTodoItem("只有一条", "in_progress")}}), 40, 20, 5, false)
	if short.scroll != 0 || short.above != 0 || short.below != 0 {
		t.Fatalf("a one-item list reports scroll %d, above %d, below %d; want 0,0,0", short.scroll, short.above, short.below)
	}
	if hint := short.planDrawerHint(); hint != "" {
		t.Fatalf("a list that fits shows the overflow hint %q", hint)
	}

	// The overflow hint counts display rows, in both directions at once.
	overflowing := buildPlanDrawerLayout(frame, 40, 6, 1, false)
	hint := overflowing.planDrawerHint()
	if overflowing.above > 0 && overflowing.below > 0 && !strings.Contains(hint, " above · ") {
		t.Fatalf("hint = %q, want it to report both directions", hint)
	}
	if overflowing.planDrawerMaxScroll() != overflowing.rowsTotal-len(overflowing.rows) {
		t.Fatalf("max scroll = %d, want %d", overflowing.planDrawerMaxScroll(), overflowing.rowsTotal-len(overflowing.rows))
	}

	// No active item: the list starts at the first row.
	noActive := buildPlanDrawerLayout(planFrameFor(event.PlanUpdatedPayload{Items: []event.PlanUpdateItem{
		planTodoItem("一", "completed"),
		planTodoItem("二", "pending"),
		planTodoItem("三", "pending"),
		planTodoItem("四", "pending"),
		planTodoItem("五", "pending"),
		planTodoItem("六", "pending"),
		planTodoItem("七", "pending"),
	}}), 40, 5, 0, true)
	if noActive.activeRow != -1 {
		t.Fatalf("activeRow = %d for a list with no ▣ item, want -1", noActive.activeRow)
	}
	if noActive.scroll != 0 {
		t.Fatalf("a list with no active item starts at row %d, want 0", noActive.scroll)
	}
}

// The overflow hint eats a body row only when there is something to report and
// room to keep two content rows, so a list that exactly fits is not truncated by
// a hint row it does not need.
func TestPlanDrawerScrollHintRowBudget(t *testing.T) {
	three := planFrameFor(event.PlanUpdatedPayload{Items: []event.PlanUpdateItem{
		planTodoItem("一", "in_progress"),
		planTodoItem("二", "pending"),
		planTodoItem("三", "pending"),
	}})
	// height 4 = title + three item rows: everything fits, no hint row.
	fits := buildPlanDrawerLayout(three, 40, 4, 0, true)
	if len(fits.rows) != 3 {
		t.Fatalf("three one-row items in a height-4 drawer laid out %d rows, want 3", len(fits.rows))
	}
	if hint := fits.planDrawerHint(); hint != "" {
		t.Fatalf("hint = %q, want none when the list fits", hint)
	}
	// height 3 = title + two item rows, and the list keeps those two rows
	// rather than spending one of them on the hint.
	tight := buildPlanDrawerLayout(three, 40, 3, 0, true)
	if len(tight.rows) != 2 {
		t.Fatalf("height-3 drawer laid out %d rows, want 2", len(tight.rows))
	}
	if hint := tight.planDrawerHint(); hint != "" {
		t.Fatalf("hint = %q, want none when the hint would cost half the list", hint)
	}

	// With room for two content rows and a hint, an overflowing list reports
	// what is left below it.
	five := planFrameFor(event.PlanUpdatedPayload{Items: []event.PlanUpdateItem{
		planTodoItem("一", "in_progress"),
		planTodoItem("二", "pending"),
		planTodoItem("三", "pending"),
		planTodoItem("四", "pending"),
		planTodoItem("五", "pending"),
	}})
	overflows := buildPlanDrawerLayout(five, 40, 4, 0, true)
	if len(overflows.rows) != 2 {
		t.Fatalf("height-4 drawer laid out %d rows, want 2 (title + hint are fixed)", len(overflows.rows))
	}
	if hint := overflows.planDrawerHint(); hint != "3 below" {
		t.Fatalf("hint = %q, want %q", hint, "3 below")
	}
}

// newPlanDrawerScreen is a viewport renderer sized to a fixed terminal, with
// the renderer's own output captured so a test can read what was painted.
func newPlanDrawerScreen(t *testing.T, width, height int) *Renderer {
	t.Helper()
	r, _ := newPlanDrawerScreenWithOut(t, width, height)
	return r
}

// newPlanDrawerScreenWithOut also returns the renderer's own output, which is
// how a hover test proves a repeated report writes nothing.
func newPlanDrawerScreenWithOut(t *testing.T, width, height int) (*Renderer, *bytes.Buffer) {
	t.Helper()
	forcedTermWidth, forcedTermHeight = width, height
	t.Cleanup(func() { forcedTermWidth, forcedTermHeight = 0, 0 })
	var out bytes.Buffer
	r := NewRenderer(&out, &out)
	r.EnableViewportMode()
	t.Cleanup(r.DisableViewportMode)
	t.Cleanup(func() { drainCaretRequest() })
	return r, &out
}

// fillPlanDrawerTranscript gives the renderer a transcript with something to
// reflow: long history lines, a full-bleed user card, and a plan frame.
func fillPlanDrawerTranscript(r *Renderer) {
	for i := 0; i < 8; i++ {
		r.RenderFrame(Frame{Kind: FrameAssistant, Final: true, Content: fmt.Sprintf(
			"历史消息第 %d 条，内容足够长，以便在历史区域变窄之后必须重新换行：the quick brown fox jumps over the lazy dog", i)})
	}
	r.RenderFrame(Frame{Kind: FrameUser, Final: true, Content: "建立七项待办并保留足够长的历史消息用于分栏重排验收"})
	r.RenderFrame(Frame{Kind: FrameAssistant, Final: true, Content: "好的，已经建立待办并开始执行。"})
	r.RenderFrame(planDrawerTestFrame())
}

// ansiEscapePattern matches every escape sequence a painted row can carry: the
// SGR styling, plus the cursor/erase controls some rows end with (the composer's
// status and preview rows terminate in \x1b[K rather than padding themselves to
// the terminal width). A cell reader has to drop all of them — an escape is not
// a glyph, and the erase leaves the terminal showing blank cells.
var ansiEscapePattern = regexp.MustCompile("\x1b\\[[0-9;?]*[A-Za-z]|\x1b\\][^\x07\x1b]*(?:\x07|\x1b\\\\)")

// drawnCellAt returns the rune the terminal shows at a 0-based display column
// of a painted row, and "" past the end of what the row paints.
func drawnCellAt(row string, col int) string {
	plain := ansiEscapePattern.ReplaceAllString(row, "")
	at := 0
	for _, rn := range plain {
		width := runewidth.RuneWidth(rn)
		if at <= col && col < at+width {
			return string(rn)
		}
		at += width
	}
	return ""
}

// planDrawerOpenForTest opens the drawer the way a click on the progress token
// does, without going through the mouse protocol.
func planDrawerOpenForTest(t *testing.T, r *Renderer) {
	t.Helper()
	r.mu.Lock()
	opened := r.openPlanDrawerLocked()
	r.mu.Unlock()
	if !opened {
		t.Fatal("the drawer did not open over a transcript that has a plan frame")
	}
}

// TestPlanDrawerSplitNarrowsTheHistoryAndKeepsTheComposer is the core of the
// "non-covering" half of the contract: the history region gives the drawer its
// columns and reflows, while the composer's own rows, its caret and the body
// height stay exactly as they were.
func TestPlanDrawerSplitNarrowsTheHistoryAndKeepsTheComposer(t *testing.T) {
	const width, height = 120, 40
	r := newPlanDrawerScreen(t, width, height)
	fillPlanDrawerTranscript(r)
	cursor := 3
	r.RenderComposerState(ComposerRenderState{Text: "draft text", Cursor: &cursor})

	composerBefore := append([]string(nil), r.vpLastComposer.lines...)
	cursorRowBefore, cursorColBefore := r.vpLastComposer.cursorRow, r.vpLastComposer.cursorCol
	bodyHeightBefore := r.vpBodyHeight
	caretBefore := [2]int{r.cursorScreenRow, r.cursorScreenCol}

	planDrawerOpenForTest(t, r)

	if !r.vpDrawerOpen {
		t.Fatal("the paint did not record an open drawer")
	}
	if got, want := r.vpDrawerLayout.leftWidth, 80; got != want {
		t.Fatalf("history region is %d columns, want %d (120 - 40)", got, want)
	}
	if r.vpBodyHeight != bodyHeightBefore {
		t.Fatalf("body height changed from %d to %d; the drawer must not steal composer rows", bodyHeightBefore, r.vpBodyHeight)
	}
	if strings.Join(r.vpLastComposer.lines, "\n") != strings.Join(composerBefore, "\n") {
		t.Fatalf("the composer changed when the drawer opened:\nbefore:\n%s\nafter:\n%s",
			strings.Join(composerBefore, "\n"), strings.Join(r.vpLastComposer.lines, "\n"))
	}
	if r.vpLastComposer.cursorRow != cursorRowBefore || r.vpLastComposer.cursorCol != cursorColBefore {
		t.Fatalf("the composer caret moved from (%d,%d) to (%d,%d)", cursorRowBefore, cursorColBefore, r.vpLastComposer.cursorRow, r.vpLastComposer.cursorCol)
	}
	if got := [2]int{r.cursorScreenRow, r.cursorScreenCol}; got != caretBefore {
		t.Fatalf("the hardware caret moved from %v to %v", caretBefore, got)
	}

	// Every body row is exactly the terminal width, and the seam is the
	// drawer's divider at the history region's last column + 1.
	for row := 0; row < r.vpBodyHeight; row++ {
		painted := r.vpPainted[row]
		if got := displayLineWidth(painted); got != width {
			t.Fatalf("body row %d paints %d cells, want exactly %d: %q", row, got, width, painted)
		}
		if got := drawnCellAt(painted, r.vpDrawerLayout.leftWidth); got != "│" {
			t.Fatalf("body row %d has %q at the seam column %d, want the divider: %q", row, got, r.vpDrawerLayout.leftWidth, painted)
		}
		if strings.Contains(painted, "\x1b[K") {
			t.Fatalf("body row %d fills its tail with \\x1b[K: %q", row, painted)
		}
	}
	// The history itself was laid out narrower. Plain blocks keep the region's
	// right padding; a user card is full-bleed and runs one padding column
	// further, exactly to the divider.
	for _, b := range r.vm.blocks {
		span, ok := r.vpLastRender.spanForBlock(b.id)
		if !ok {
			continue
		}
		budget := r.vpDrawerLayout.leftWidth - viewportRightPadding
		if b.frame.Kind == FrameUser {
			budget = r.vpDrawerLayout.leftWidth
		}
		for row := span.startRow; row < span.startRow+span.rowCount; row++ {
			if got := displayLineWidth(r.vpLastRender.allLines[row]); got > budget {
				t.Fatalf("%s block line %d is %d columns wide, over its %d-column budget: %q",
					b.frame.Kind, row, got, budget, r.vpLastRender.allLines[row])
			}
		}
	}
	// The drawer's head is on the first body row.
	titleRow := r.vpPainted[0]
	if !strings.Contains(sgrPattern.ReplaceAllString(titleRow, ""), "Updated Plan") {
		t.Fatalf("the drawer's title is not on the first body row: %q", titleRow)
	}
	// The close control sits in the middle of its three-cell zone, which is the
	// title row's rightmost three body cells.
	closeCell := r.vpDrawerLayout.leftWidth + r.vpDrawerLayout.closeSpan.startCol + 1
	if got := drawnCellAt(titleRow, closeCell); got != "×" {
		t.Fatalf("the close control is not drawn at column %d (found %q): %q", closeCell, got, titleRow)
	}
}

// Opening the drawer must reflow the history rather than clip it: the same text
// comes back, wrapped at the narrower width.
func TestPlanDrawerSplitReflowsTheHistory(t *testing.T) {
	r := newPlanDrawerScreen(t, 120, 40)
	long := "这一条历史消息很长，用来验证变窄之后每一行都按新的宽度重新换行而不是被裁掉或者被抽屉覆盖 the quick brown fox jumps over the lazy dog again and again"
	r.RenderFrame(Frame{Kind: FrameAssistant, Final: true, Content: long})
	r.RenderFrame(planDrawerTestFrame())

	widthBefore := 0
	for _, line := range r.vpLastRender.allLines {
		if got := displayLineWidth(line); got > widthBefore {
			widthBefore = got
		}
	}
	planDrawerOpenForTest(t, r)
	widthAfter := 0
	for _, line := range r.vpLastRender.allLines {
		if got := displayLineWidth(line); got > widthAfter {
			widthAfter = got
		}
	}
	if widthAfter >= widthBefore {
		t.Fatalf("the transcript did not reflow: widest line %d columns before, %d after narrowing", widthBefore, widthAfter)
	}
	if widthAfter > r.vpDrawerLayout.leftWidth {
		t.Fatalf("a transcript line is still %d columns wide, past the %d-column region", widthAfter, r.vpDrawerLayout.leftWidth)
	}
	var joined strings.Builder
	for _, line := range r.vpLastRender.allLines {
		joined.WriteString(strings.TrimSpace(sgrPattern.ReplaceAllString(line, "")))
	}
	if !strings.Contains(joined.String(), "again and again") {
		t.Fatalf("reflowing dropped history text:\n%s", joined.String())
	}
}

// A reader who scrolled away from the bottom keeps the block they were reading
// at the top of the narrower region — the anchor is a block and a row inside it,
// not a row offset that the rewrap would invalidate.
func TestPlanDrawerSplitKeepsTheReaderPosition(t *testing.T) {
	r := newPlanDrawerScreen(t, 120, 30)
	fillPlanDrawerTranscript(r)
	r.ViewportScroll(-14)

	topBlock, _ := r.vpLastRender.blockAtRow(0)
	if topBlock < 0 {
		t.Fatalf("the test did not scroll onto a block (top row is a gap)")
	}
	spanBefore, ok := r.vpLastRender.spanForBlock(topBlock)
	if !ok {
		t.Fatal("top block has no span")
	}
	relBefore := r.vpLastRender.firstRow - spanBefore.startRow

	planDrawerOpenForTest(t, r)

	topAfter, _ := r.vpLastRender.blockAtRow(0)
	if topAfter != topBlock {
		t.Fatalf("the top of the history moved from block %d to %d across the reflow", topBlock, topAfter)
	}
	spanAfter, ok := r.vpLastRender.spanForBlock(topBlock)
	if !ok {
		t.Fatal("top block lost its span")
	}
	if relAfter := r.vpLastRender.firstRow - spanAfter.startRow; relAfter != relBefore {
		t.Fatalf("the reader's row inside block %d moved from %d to %d", topBlock, relBefore, relAfter)
	}
	if r.vpFollow {
		t.Fatal("opening the drawer re-armed follow mode and dropped the reader's position")
	}
}

// A slash/overlay panel replaces the composer and shortens the history region.
// The drawer stays open, its own row count follows the shorter body, and it
// registers no progress hot zone for a status row that is not on screen.
func TestPlanDrawerSplitSurvivesAnOverlay(t *testing.T) {
	r := newPlanDrawerScreen(t, 120, 30)
	fillPlanDrawerTranscript(r)
	cursor := 0
	r.RenderComposerState(ComposerRenderState{Text: "draft", Cursor: &cursor})
	r.installTransientStatus(transientSourceWorking, func(string) string {
		return "| Working (3s • esc to interrupt) · ☑3/7"
	})
	t.Cleanup(func() { r.FinishTransientStatus(transientSourceWorking) })

	planDrawerOpenForTest(t, r)
	bodyBefore := r.vpBodyHeight
	if r.vpProgressSpan.row < 0 {
		t.Fatalf("no progress hot zone for a status line that shows ☑3/7 (span %+v)", r.vpProgressSpan)
	}

	lines := make([]string, 20)
	for i := range lines {
		lines[i] = fmt.Sprintf("overlay line %d", i)
	}
	r.BeginComposerOverlay()
	r.SetOverlayComposer(lines, -1, 0)

	if !r.planDrawer.open || !r.vpDrawerOpen {
		t.Fatal("the drawer closed when an overlay took the composer")
	}
	if r.vpBodyHeight >= bodyBefore {
		t.Fatalf("the history region did not shrink for the overlay: %d then %d rows", bodyBefore, r.vpBodyHeight)
	}
	if r.vpProgressSpan.row >= 0 {
		t.Fatalf("an overlay frame registered a progress hot zone at %+v; there is no status row on it", r.vpProgressSpan)
	}
	for row := 0; row < r.vpBodyHeight; row++ {
		if got := drawnCellAt(r.vpPainted[row], r.vpDrawerLayout.leftWidth); got != "│" {
			t.Fatalf("body row %d lost the divider beside the overlay: %q", row, r.vpPainted[row])
		}
	}
}

// The floating jump control belongs to the history region: with the drawer open
// it centres on the left column and never lands on the list.
func TestPlanDrawerSplitJumpStaysLeft(t *testing.T) {
	r := newPlanDrawerScreen(t, 120, 30)
	fillPlanDrawerTranscript(r)
	planDrawerOpenForTest(t, r)
	r.ViewportScroll(-10)

	span := r.jumpButtonSpanLocked()
	if span.row < 0 {
		t.Fatal("the jump control is not shown over a scrolled transcript")
	}
	if span.endCol > r.vpDrawerLayout.leftWidth || span.startCol < 0 {
		t.Fatalf("jump control spans columns [%d,%d), outside the %d-column history region", span.startCol, span.endCol, r.vpDrawerLayout.leftWidth)
	}
	if span.row >= r.vpBodyHeight {
		t.Fatalf("jump control sits on row %d, below the %d-row body", span.row, r.vpBodyHeight)
	}
	// The control is painted by addressing its own cells inside the row, so
	// the row keeps its width and the label lands in the history region.
	cup := fmt.Sprintf("\x1b[%d;%dH", span.row+1, span.startCol+1)
	if !strings.Contains(r.vpPainted[span.row], cup) {
		t.Fatalf("the jump control is not addressed at column %d of row %d: %q", span.startCol+1, span.row+1, r.vpPainted[span.row])
	}
}

// A user-message card is full-bleed: with the drawer open it runs exactly to the
// divider instead of being clipped a column short of it.
func TestPlanDrawerSplitUserCardReachesTheDivider(t *testing.T) {
	r := newPlanDrawerScreen(t, 120, 40)
	fillPlanDrawerTranscript(r)
	planDrawerOpenForTest(t, r)

	userBlock := -1
	for _, b := range r.vm.blocks {
		if b.frame.Kind == FrameUser {
			userBlock = b.id
		}
	}
	if userBlock < 0 {
		t.Fatal("no user card in the transcript")
	}
	span, ok := r.vpLastRender.spanForBlock(userBlock)
	if !ok {
		t.Fatal("the user card was not painted")
	}
	row := span.startRow - r.vpLastRender.firstRow
	if row < 0 || row >= r.vpBodyHeight {
		t.Fatalf("the user card's first row (%d) is off screen", row)
	}
	if got := drawnCellAt(r.vpPainted[row], r.vpDrawerLayout.leftWidth-2); got != "─" {
		t.Fatalf("the user card's rule stops short of the divider: column %d is %q in %q", r.vpDrawerLayout.leftWidth-2, got, r.vpPainted[row])
	}
	if got := drawnCellAt(r.vpPainted[row], r.vpDrawerLayout.leftWidth); got != "│" {
		t.Fatalf("the divider is not the cell after the user card: %q", got)
	}
}

// Closing the drawer gives the history its width back, leaving the composer and
// the reading position exactly as they were.
func TestPlanDrawerSplitCloseRestoresTheLayout(t *testing.T) {
	r := newPlanDrawerScreen(t, 120, 40)
	fillPlanDrawerTranscript(r)
	cursor := 0
	r.RenderComposerState(ComposerRenderState{Text: "draft", Cursor: &cursor})
	fullWidthLines := len(r.vpLastRender.allLines)
	composerBefore := append([]string(nil), r.vpLastComposer.lines...)

	planDrawerOpenForTest(t, r)
	r.mu.Lock()
	closed := r.closePlanDrawerLocked()
	r.mu.Unlock()
	if !closed {
		t.Fatal("closePlanDrawerLocked reported nothing to close")
	}
	if r.vpDrawerOpen {
		t.Fatal("the drawer is still recorded as painted after closing")
	}
	if got := len(r.vpLastRender.allLines); got != fullWidthLines {
		t.Fatalf("the transcript kept its narrow layout after closing: %d lines, want %d", got, fullWidthLines)
	}
	if strings.Join(r.vpLastComposer.lines, "\n") != strings.Join(composerBefore, "\n") {
		t.Fatalf("the composer changed when the drawer closed")
	}
	for row := 0; row < r.vpBodyHeight; row++ {
		if got := displayLineWidth(r.vpPainted[row]); got > 120 {
			t.Fatalf("body row %d paints %d cells after closing", row, got)
		}
	}
}

// A view switch and a session reset both drop the drawer's geometry with the
// plan it described.
func TestPlanDrawerSplitClosedByViewSwitchAndSessionReset(t *testing.T) {
	r := newPlanDrawerScreen(t, 120, 40)
	fillPlanDrawerTranscript(r)
	r.RenderFrame(planFrameFor(event.PlanUpdatedPayload{
		AgentID: "sub-1",
		Items:   []event.PlanUpdateItem{planTodoItem("子代理事项", "in_progress")},
	}))
	planDrawerOpenForTest(t, r)

	r.SetActiveView("sub-1")
	if r.planDrawer.open || r.vpDrawerOpen {
		t.Fatal("the drawer survived a view switch; it belongs to the view it was opened on")
	}

	r.SetActiveView("")
	planDrawerOpenForTest(t, r)
	r.ViewportResetSession()
	if r.planDrawer.open || r.vpDrawerOpen {
		t.Fatal("the drawer survived a session reset")
	}
}

// installPlanProgressStatus puts the live working line on screen with the plan
// progress token on it, the way a running turn does.
func installPlanProgressStatus(t *testing.T, r *Renderer, line string) {
	t.Helper()
	r.installTransientStatus(transientSourceWorking, func(string) string { return line })
	t.Cleanup(func() { r.FinishTransientStatus(transientSourceWorking) })
}

// The ☑N/M token is the drawer's entry, and only the token is: the hot zone
// starts on the ☑ and covers the numbers the terminal actually draws.
func TestPlanDrawerProgressTokenIsTheEntry(t *testing.T) {
	r := newPlanDrawerScreen(t, 120, 40)
	fillPlanDrawerTranscript(r)
	installPlanProgressStatus(t, r, "| Working (3s • esc to interrupt) · ☑3/7 · 修改 pkg/tui/input_events.go 的鼠标交互")

	span := r.vpProgressSpan
	if span.row < r.vpBodyHeight {
		t.Fatalf("progress hot zone is on row %d, inside the %d-row transcript", span.row, r.vpBodyHeight)
	}
	if got := drawnCellAt(r.vpPainted[span.row], span.startCol); got != "☑" {
		t.Fatalf("the hot zone starts on %q, not the ☑ of the drawn token: %q", got, r.vpPainted[span.row])
	}
	if got, want := span.endCol-span.startCol, 4; got != want {
		t.Fatalf("hot zone is %d cells wide, want %d (☑3/7)", got, want)
	}
	// The active task text after the token is not part of the entry.
	if got := drawnCellAt(r.vpPainted[span.row], span.endCol); got != " " {
		t.Fatalf("hot zone runs into %q past the token; the task text must stay outside it", got)
	}

	clickAt(r, span.startCol+1, span.row)
	if !r.planDrawer.open || !r.vpDrawerOpen {
		t.Fatal("clicking the progress token did not open the drawer")
	}
	// Clicking the same token again closes it.
	clickAt(r, span.startCol+1, span.row)
	if r.planDrawer.open || r.vpDrawerOpen {
		t.Fatal("clicking the progress token again did not close the drawer")
	}
}

// A token the terminal width cut in half is not on screen, so it is not an
// entry: the hit zone is only ever the whole token the user can see.
func TestPlanDrawerTruncatedTokenHasNoHotZone(t *testing.T) {
	if _, end, ok := planProgressToken("| Working (3s) · ☑12/1…"); ok {
		t.Fatalf("a truncated token (%q) registered a hot zone ending at %d", "☑12/1…", end)
	}
	r := newPlanDrawerScreen(t, 40, 24)
	fillPlanDrawerTranscript(r)
	installPlanProgressStatus(t, r, "| Working (3s • esc to interrupt) · ☑12/17")
	if r.vpProgressSpan.row >= 0 {
		t.Fatalf("a token the 40-column line cut off registered a hot zone at %+v", r.vpProgressSpan)
	}
	// The whole token registers where it fits.
	if start, end, ok := planProgressToken("☑3/7 · Step 3"); !ok || start != 0 || end != 4 {
		t.Fatalf("planProgressToken(☑3/7 · Step 3) = (%d,%d,%v), want (0,4,true)", start, end, ok)
	}
}

// The × on the drawer's title row closes it, and only its own three cells are
// the target.
func TestPlanDrawerCloseControlClosesIt(t *testing.T) {
	r := newPlanDrawerScreen(t, 120, 40)
	fillPlanDrawerTranscript(r)
	planDrawerOpenForTest(t, r)

	closeCell := r.vpDrawerLayout.leftWidth + r.vpDrawerLayout.closeSpan.startCol + 1
	clickAt(r, closeCell, 0)
	if r.planDrawer.open {
		t.Fatal("clicking the drawer's close control did not close it")
	}
}

// A click inside the open drawer is consumed: it neither closes the drawer nor
// reaches the cells the transcript would have had there.
func TestPlanDrawerClickInsideIsConsumed(t *testing.T) {
	r := newPlanDrawerScreen(t, 120, 40)
	fillPlanDrawerTranscript(r)
	planDrawerOpenForTest(t, r)
	drainCaretRequest()

	viewBefore := r.ActiveView()
	scrollBefore := r.vpScrollOffset
	clickAt(r, r.vpDrawerLayout.leftWidth+5, 4)

	if !r.planDrawer.open {
		t.Fatal("a click inside the drawer closed it")
	}
	if r.selectStartRow >= 0 {
		t.Fatalf("a click inside the drawer started a text selection at row %d", r.selectStartRow)
	}
	if got := drainCaretRequest(); got != -1 {
		t.Fatalf("a click inside the drawer requested composer caret %d", got)
	}
	if r.ActiveView() != viewBefore {
		t.Fatalf("a click inside the drawer switched the view to %q", r.ActiveView())
	}
	if r.vpScrollOffset != scrollBefore {
		t.Fatalf("a click inside the drawer scrolled the transcript from %d to %d", scrollBefore, r.vpScrollOffset)
	}
}

// The drawer is opened and closed through its own controls — the token, the
// close control, Escape. Using the screen beside it is not a close: the drawer
// covers only the right column, so the history and the composer on its left
// keep working while it stays open.
func TestPlanDrawerOutsideClickDoesNotClose(t *testing.T) {
	r := newPlanDrawerScreen(t, 120, 40)
	toolRow := strings.Join([]string{
		"row 01", "row 02", "row 03", "row 04", "row 05", "row 06",
		"row 07", "row 08", "row 09", "row 10", "row 11", "row 12", "row 13",
	}, "\n")
	r.RenderFrame(Frame{Kind: FrameTool, Title: "shell", Content: toolRow, Summary: "ran echo"})
	r.RenderFrame(planDrawerTestFrame())
	planDrawerOpenForTest(t, r)
	drainCaretRequest()

	// A click on the tool card's preview body folds it, and leaves the drawer
	// open: the card is not under the drawer, so nothing about it is swallowed.
	blockID, _ := r.vpLastRender.blockAtRow(1)
	if blockID < 0 {
		t.Fatal("row 1 is not part of the tool card")
	}
	viewBefore := r.ActiveView()
	offsetBefore := r.vpScrollOffset
	clickAt(r, 2, 1)

	if !r.planDrawer.open {
		t.Fatal("a click on the history closed the drawer; only its own controls may")
	}
	if b := r.activeVMLocked().blockByID(blockID); b == nil || !b.expanded {
		t.Fatal("the click on the card did not fold it while the drawer was open")
	}
	if got := drainCaretRequest(); got != -1 {
		t.Fatalf("the click on the card requested composer caret %d", got)
	}
	if r.ActiveView() != viewBefore {
		t.Fatalf("the click on the card switched the view to %q", r.ActiveView())
	}
	if r.vpScrollOffset != offsetBefore {
		t.Fatalf("the click on the card jumped the transcript from %d to %d", offsetBefore, r.vpScrollOffset)
	}

	// A click in the composer places the caret and leaves the drawer open too.
	cursor := len("hello world")
	r.RenderComposerState(ComposerRenderState{Text: "hello world", Cursor: &cursor})
	planDrawerOpenForTest(t, r)
	drainCaretRequest()
	row := composerTextRowScreen(r, 0)
	prefix := r.vpLastComposer.text.rows[0].prefixWidth
	clickAt(r, prefix+6, row)
	if got := drainCaretRequest(); got != 6 {
		t.Fatalf("clicking the composer with the drawer open requested caret %d, want 6", got)
	}
	if !r.planDrawer.open {
		t.Fatal("a click in the composer closed the drawer; only its own controls may")
	}

	// The close control is still the way out.
	planDrawerOpenForTest(t, r)
	closeCell := r.vpDrawerLayout.leftWidth + r.vpDrawerLayout.closeSpan.startCol + 1
	clickAt(r, closeCell, 0)
	if r.planDrawer.open {
		t.Fatal("the close control no longer closes the drawer")
	}
}

// The wheel over the drawer scrolls the list — the divider column included — in
// display rows, and never the transcript under it. At the list's end the notch
// falls through to the transcript instead of being swallowed.
func TestPlanDrawerWheelScrollsTheList(t *testing.T) {
	r := newPlanDrawerScreen(t, 120, 24)
	fillPlanDrawerTranscript(r)
	items := make([]event.PlanUpdateItem, 0, 40)
	for i := 0; i < 40; i++ {
		items = append(items, planTodoItem(fmt.Sprintf("事项 %02d", i), "pending"))
	}
	r.RenderFrame(planFrameFor(event.PlanUpdatedPayload{Items: items}))
	planDrawerOpenForTest(t, r)
	// Start at the top of the list so both directions are exercisable.
	r.mu.Lock()
	r.planDrawer.scroll = 0
	r.planDrawer.followActive = false
	r.paintViewportLocked()
	r.mu.Unlock()

	scrollBefore := r.vpScrollOffset
	notch := func(up bool, col, row int) {
		delta := wheelScrollLines
		if up {
			delta = -wheelScrollLines
		}
		r.HandleMouse(inputEvent{kind: inputEventMouseWheel, wheelDelta: delta, mouseCol: col, mouseRow: row, at: time.Now()})
	}
	notch(false, r.vpDrawerLayout.leftWidth+2, 3)
	if got, want := r.planDrawer.scroll, wheelScrollLines; got != want {
		t.Fatalf("one wheel notch over the drawer scrolled the list to %d, want %d display rows", got, want)
	}
	if r.vpScrollOffset != scrollBefore {
		t.Fatalf("wheel over the drawer scrolled the transcript from %d to %d", scrollBefore, r.vpScrollOffset)
	}
	if r.planDrawer.followActive {
		t.Fatal("a manual wheel left the list in follow-the-active-item mode")
	}

	// The divider column belongs to the drawer.
	before := r.planDrawer.scroll
	notch(false, r.vpDrawerLayout.leftWidth, 5)
	if r.planDrawer.scroll == before {
		t.Fatalf("a notch on the divider column (%d) did not scroll the list", r.vpDrawerLayout.leftWidth)
	}

	// At the top the notch falls through to the transcript.
	r.mu.Lock()
	r.planDrawer.scroll = 0
	r.paintViewportLocked()
	r.mu.Unlock()
	r.vpFollow = true
	notch(true, r.vpDrawerLayout.leftWidth+2, 3)
	if r.planDrawer.scroll != 0 {
		t.Fatalf("the list scrolled past its top to %d", r.planDrawer.scroll)
	}
	if r.vpFollow {
		t.Fatal("a notch the list could not use was swallowed instead of scrolling the transcript")
	}
}

// Hovering the token paints the shared background over exactly the token's
// cells, with no underline and no other decoration, and moving off restores the
// faint line. A pointer that keeps reporting the same cell writes nothing: with
// ?1003h that is the difference between a highlight and a flooded terminal.
func TestPlanDrawerHoverHighlightsOnlyTheToken(t *testing.T) {
	r, out := newPlanDrawerScreenWithOut(t, 120, 40)
	fillPlanDrawerTranscript(r)
	installPlanProgressStatus(t, r, "| Working (3s • esc to interrupt) · ☑3/7")
	span := r.vpProgressSpan
	if span.row < 0 {
		t.Fatal("no progress hot zone to hover")
	}
	plainBefore := r.vpPainted[span.row]
	drainCaretRequest()

	move := func(col, row int) {
		r.HandleMouse(inputEvent{kind: inputEventMouseMove, mouseCol: col, mouseRow: row, at: time.Now()})
	}
	move(span.startCol+1, span.row)
	hovered := r.vpPainted[span.row]
	if !strings.Contains(hovered, "\x1b[48;5;"+hoverBackgroundColor+"m") {
		t.Fatalf("hovering the token painted no hover background: %q", hovered)
	}
	if !strings.Contains(hovered, "\x1b[48;5;"+hoverBackgroundColor+"m☑3/7\x1b[49m") {
		t.Fatalf("the hover background is not confined to the token: %q", hovered)
	}
	if strings.Contains(strings.ReplaceAll(hovered, "\x1b[48;5;"+hoverBackgroundColor+"m", ""), "\x1b[4m") {
		t.Fatalf("the hovered token is underlined: %q", hovered)
	}
	// Only the token's cells changed.
	if got := strings.ReplaceAll(hovered, "\x1b[48;5;"+hoverBackgroundColor+"m☑3/7\x1b[49m", "☑3/7"); got != plainBefore {
		t.Fatalf("hovering changed more than the token:\nbefore %q\nafter  %q", plainBefore, got)
	}

	// The same cell, reported again and again, must write nothing at all.
	out.Reset()
	for i := 0; i < 20; i++ {
		move(span.startCol+1, span.row)
	}
	if out.Len() != 0 {
		t.Fatalf("20 repeated hover reports on the same cell wrote %d bytes", out.Len())
	}

	// Moving off the token restores the faint status line.
	move(1, 1)
	if got := r.vpPainted[span.row]; got != plainBefore {
		t.Fatalf("moving off the token did not restore the line:\nwant %q\ngot  %q", plainBefore, got)
	}

	// The drawer does not take the token's hover with it: the entry that opened
	// the drawer is still the entry that closes it, so it keeps its own
	// highlight on the split screen too.
	planDrawerOpenForTest(t, r)
	if got := r.vpProgressSpan; got != span {
		t.Fatalf("opening the drawer moved the token's hot zone to %+v, want %+v", got, span)
	}
	move(span.startCol+1, span.row)
	if got := r.vpPainted[span.row]; !strings.Contains(got, "\x1b[48;5;"+hoverBackgroundColor+"m☑3/7\x1b[49m") {
		t.Fatalf("hovering the token with the drawer open painted no highlight on it: %q", got)
	}
}

// The drawer's close control hovers the same way, on its own cells only, and
// repaints nothing while the pointer stays on it.
func TestPlanDrawerHoverCloseControl(t *testing.T) {
	r, out := newPlanDrawerScreenWithOut(t, 120, 40)
	fillPlanDrawerTranscript(r)
	planDrawerOpenForTest(t, r)
	closeCell := r.vpDrawerLayout.leftWidth + r.vpDrawerLayout.closeSpan.startCol + 1
	plainTitle := r.vpPainted[0]

	move := func(col, row int) {
		r.HandleMouse(inputEvent{kind: inputEventMouseMove, mouseCol: col, mouseRow: row, at: time.Now()})
	}
	move(closeCell, 0)
	hovered := r.vpPainted[0]
	if !strings.Contains(hovered, "\x1b[48;5;"+hoverBackgroundColor+"m × \x1b[0m") {
		t.Fatalf("hovering the close control painted no background over its cells: %q", hovered)
	}
	if got := strings.ReplaceAll(hovered, "\x1b[48;5;"+hoverBackgroundColor+"m × \x1b[0m", " × "); got != plainTitle {
		t.Fatalf("hovering the close control changed more than its cells:\nbefore %q\nafter  %q", plainTitle, got)
	}
	out.Reset()
	for i := 0; i < 20; i++ {
		move(closeCell, 0)
	}
	if out.Len() != 0 {
		t.Fatalf("20 repeated hover reports on the close control wrote %d bytes", out.Len())
	}
	// Leaving it restores the row.
	move(1, 1)
	if got := r.vpPainted[0]; got != plainTitle {
		t.Fatalf("leaving the close control did not restore the title row:\nwant %q\ngot  %q", plainTitle, got)
	}
}

// A drag that starts on the drawer never becomes a text selection of the cells
// the drawer covers, and a drag from the history into the drawer keeps selecting
// from the history's own text.
func TestPlanDrawerSelectionDoesNotReachUnderIt(t *testing.T) {
	r := newPlanDrawerScreen(t, 120, 40)
	fillPlanDrawerTranscript(r)
	planDrawerOpenForTest(t, r)
	drainCaretRequest()

	// Press inside the drawer, drag across it, release: nothing is selected.
	r.ViewportSelectStart(r.vpDrawerLayout.leftWidth+3, 3)
	r.ViewportSelectDrag(r.vpDrawerLayout.leftWidth+9, 6)
	r.ViewportSelectEnd(r.vpDrawerLayout.leftWidth+9, 6)
	if r.selectDidDrag {
		t.Fatal("a drag inside the drawer became a text selection")
	}
	if !r.planDrawer.open {
		t.Fatal("a drag inside the drawer closed it")
	}

	// A drag that starts in the history and runs into the drawer still copies
	// history text, and only history text.
	r.ViewportSelectStart(1, 2)
	r.ViewportSelectDrag(r.vpDrawerLayout.leftWidth+4, 2)
	r.ViewportSelectEnd(r.vpDrawerLayout.leftWidth+4, 2)
	if !r.selectDidDrag {
		t.Fatal("a drag from the history into the drawer selected nothing")
	}
	text := r.extractSelectedTextLocked()
	if strings.Contains(text, "│") || strings.Contains(text, "Updated Plan") {
		t.Fatalf("the selection picked up drawer cells: %q", text)
	}
}

// Esc closes the open drawer on each of the three paths that would otherwise
// cancel the turn, and it does nothing else: no cancellation, no draft change,
// no view switch, no session switch. The second Esc is the one the original
// behaviour sees, and the layers above the drawer keep Esc for themselves.
func TestPlanDrawerEscClosesItBeforeTheCancelPaths(t *testing.T) {
	esc := inputEvent{kind: inputEventHotkey, hotkey: hotkeyEscapeInterrupt}

	t.Run("active run", func(t *testing.T) {
		r := newPlanDrawerScreen(t, 120, 40)
		r.RenderFrame(planDrawerTestFrame())
		planDrawerOpenForTest(t, r)
		foreground := &foregroundTurn{submission: mustComposerSubmission("keep me")}
		session := &fakeSession{mcpSkipResult: true}
		state := &streamState{sessionID: "s1", activeForeground: foreground, composer: ComposerState{DraftText: "typing"}}
		view := r.ActiveView()

		handleActiveRunInput(context.Background(), session, r, nil, state, nil, esc, nil)
		if r.planDrawer.open {
			t.Fatal("Esc did not close the open drawer")
		}
		if session.cancelCalls != 0 {
			t.Fatalf("closing the drawer cancelled the run %d times", session.cancelCalls)
		}
		if foreground.isWithdrawn() {
			t.Fatal("closing the drawer withdrew the running submission")
		}
		if session.mcpSkipCalls != 0 {
			t.Fatalf("closing the drawer skipped MCP servers (%d calls) instead of stopping", session.mcpSkipCalls)
		}
		if state.composer.DraftText != "typing" {
			t.Fatalf("closing the drawer changed the draft to %q", state.composer.DraftText)
		}
		if r.ActiveView() != view {
			t.Fatalf("closing the drawer switched the view to %q", r.ActiveView())
		}

		// The next Esc is the run's again.
		handleActiveRunInput(context.Background(), session, r, nil, state, nil, esc, nil)
		if session.mcpSkipCalls != 1 {
			t.Fatalf("the second Esc consulted the MCP skipper %d times, want once", session.mcpSkipCalls)
		}
	})

	t.Run("idle", func(t *testing.T) {
		r := newPlanDrawerScreen(t, 120, 40)
		r.RenderFrame(planDrawerTestFrame())
		planDrawerOpenForTest(t, r)
		session := &fakeSession{mcpSkipResult: true}
		state := &streamState{sessionID: "s1", composer: ComposerState{DraftText: "typing"}}

		if !handleIdleHotkey(context.Background(), session, r, state, nil, nil, hotkeyEscapeInterrupt) {
			t.Fatal("idle Esc must be handled")
		}
		if r.planDrawer.open {
			t.Fatal("idle Esc did not close the open drawer")
		}
		if session.mcpSkipCalls != 0 {
			t.Fatalf("idle Esc closed the drawer and then skipped MCP servers (%d calls)", session.mcpSkipCalls)
		}
		if state.composer.DraftText != "typing" {
			t.Fatalf("closing the drawer changed the draft to %q", state.composer.DraftText)
		}
		if !handleIdleHotkey(context.Background(), session, r, state, nil, nil, hotkeyEscapeInterrupt) {
			t.Fatal("the second idle Esc must be handled")
		}
		if session.mcpSkipCalls != 1 {
			t.Fatalf("the second idle Esc consulted the MCP skipper %d times, want once", session.mcpSkipCalls)
		}
	})

	t.Run("blocking slash", func(t *testing.T) {
		r := newPlanDrawerScreen(t, 120, 40)
		r.RenderFrame(planDrawerTestFrame())
		planDrawerOpenForTest(t, r)
		session := &fakeSession{}
		state := &streamState{sessionID: "s1", composer: ComposerState{DraftText: "typing"}}

		if cancel := handleBlockingSlashInput(session, r, state, esc); cancel {
			t.Fatal("Esc closed the drawer and also cancelled the blocking operation")
		}
		if r.planDrawer.open {
			t.Fatal("Esc did not close the drawer while a blocking command was in flight")
		}
		if state.composer.DraftText != "typing" {
			t.Fatalf("closing the drawer changed the draft to %q", state.composer.DraftText)
		}
		if cancel := handleBlockingSlashInput(session, r, state, esc); !cancel {
			t.Fatal("the second Esc must cancel the blocking operation as before")
		}
	})

	// A layer above the drawer keeps Esc: a focused roster swallows it and the
	// drawer stays open.
	t.Run("roster first", func(t *testing.T) {
		r := newPlanDrawerScreen(t, 120, 40)
		r.RenderFrame(planDrawerTestFrame())
		planDrawerOpenForTest(t, r)
		state := &streamState{sessionID: "s1", agentRosterFocused: true, activeForeground: &foregroundTurn{submission: mustComposerSubmission("keep me")}}

		handleActiveRunInput(context.Background(), &fakeSession{}, r, nil, state, nil, esc, nil)
		if state.agentRosterFocused {
			t.Fatal("Esc did not unfocus the roster")
		}
		if !r.planDrawer.open {
			t.Fatal("Esc was spent on the drawer before the roster that has focus")
		}
	})
}
