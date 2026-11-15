package tui

// The exit_plan_mode approval panel picks a reviewer or a hand-off executor
// from the same model list /model shows. A list long enough to scan by eye
// carries the same filter bar, and typing in it narrows the rows the same way:
// these tests pin that bar, the filtering it drives, the keys that move between
// it and the rows, and the short lists that keep their old one-key navigation.

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

// planReviewFilterModels is a reviewer list of n opencode-go models.
func planReviewFilterModels(n int) []turn.PlanReviewModelOption {
	models := make([]turn.PlanReviewModelOption, 0, n)
	for i := 1; i <= n; i++ {
		models = append(models, turn.PlanReviewModelOption{
			ID:       fmt.Sprintf("row-%02d", i),
			Provider: "opencode-go",
			Model:    fmt.Sprintf("model-%02d", i),
			Endpoint: "https://opencode.ai/zen",
		})
	}
	return models
}

// planReviewFilterFixtures is the twelve-model list the filter tests read: three
// deepseek rows (the only rows a "deep" filter matches — no other name or
// endpoint holds it) and one row whose endpoint, and not whose name, says
// "anthropic".
func planReviewFilterFixtures() []turn.PlanReviewModelOption {
	const deepseekEndpoint = "https://api.deepseek.com/v1/chat/completions"
	return []turn.PlanReviewModelOption{
		{ID: "chat", Provider: "deepseek", Model: "deepseek-chat", Endpoint: deepseekEndpoint, EndpointTail: "/v1/chat/completions"},
		{ID: "reasoner", Provider: "deepseek", Model: "deepseek-reasoner", Endpoint: deepseekEndpoint, EndpointTail: "/v1/chat/completions"},
		{ID: "v4", Provider: "deepseek", Model: "deepseek-v4", Endpoint: deepseekEndpoint, EndpointTail: "/v1/chat/completions"},
		{ID: "luna", Provider: "chatgpt", Model: "gpt-5.6-luna", Endpoint: "https://chatgpt.com/backend-api/codex"},
		{ID: "glm", Provider: "zhipuai", Model: "glm-5.3", Endpoint: "https://open.bigmodel.cn/api/paas/v4"},
		{ID: "luna-tail", Provider: "chatgpt", Model: "gpt-5.6-luna-mini", Endpoint: "https://chatgpt.com/backend-api/codex"},
		{ID: "endpoint-only", Provider: "openai", Model: "gpt-5.1", Endpoint: "https://api.anthropic.com/v1/messages", EndpointTail: "/v1/messages"},
		{ID: "zen-1", Provider: "opencode-go", Model: "model-01", Endpoint: "https://opencode.ai/zen"},
		{ID: "zen-2", Provider: "opencode-go", Model: "model-02", Endpoint: "https://opencode.ai/zen"},
		{ID: "zen-3", Provider: "opencode-go", Model: "model-03", Endpoint: "https://opencode.ai/zen"},
		{ID: "zen-4", Provider: "opencode-go", Model: "model-04", Endpoint: "https://opencode.ai/zen"},
		{ID: "zen-5", Provider: "opencode-go", Model: "model-05", Endpoint: "https://opencode.ai/zen"},
	}
}

// planReviewFilterRequest is the fixture list on an exit_plan_mode request.
func planReviewFilterRequest(t *testing.T, models []turn.PlanReviewModelOption) turn.ToolApprovalRequest {
	t.Helper()
	req := exitPlanReviewRequest(t, "# Plan\n\nStep one.\n")
	req.PlanReviewModels = models
	return req
}

// planReviewFilterOpen opens the reviewer picker on req and drops the renderer.
func planReviewFilterOpen(t *testing.T, req turn.ToolApprovalRequest) *approvalOverlay {
	t.Helper()
	overlay := newApprovalOverlay(context.Background(), io.Discard, nil, req, nil)
	overlay.enterPlanReviewMode()
	return overlay
}

// planReviewFilterType types text into the picker one rune at a time, the way
// the terminal delivers it.
func planReviewFilterType(t *testing.T, overlay *approvalOverlay, text string) {
	t.Helper()
	for _, r := range text {
		if _, done := overlay.handlePlanReviewKey(parsedKey{kind: rawKeyRune, r: r}); done {
			t.Fatalf("typing %q ended the picker", r)
		}
	}
}

// planReviewFilterLines renders the overlay body at width with styling stripped.
func planReviewFilterLines(t *testing.T, overlay *approvalOverlay, width int) []string {
	t.Helper()
	rendered, _ := overlay.buildApprovalContent(width)
	lines := make([]string, len(rendered))
	for i, line := range rendered {
		lines[i] = stripANSI(line)
	}
	return lines
}

// planReviewFilterLine is the index of the first rendered line holding want, -1
// when no line does.
func planReviewFilterLine(lines []string, want string) int {
	for i, line := range lines {
		if strings.Contains(line, want) {
			return i
		}
	}
	return -1
}

// The bar is the long list's, not the short list's: a reviewer list under
// pickerFilterMinRows opens straight on its rows, the way it always has, and one
// at the threshold draws /model's five-row bar — air, the padded bar row, the
// text's row, the padded row, air — between its question and its first model.
func TestPlanReviewPickerFilterBarNeedsALongList(t *testing.T) {
	const question = "Which model should review this plan?"

	short := planReviewFilterLines(t, planReviewFilterOpen(t, planReviewFilterRequest(t, planReviewFilterModels(7))), 100)
	if at := planReviewFilterLine(short, panelFilterGlyph); at >= 0 {
		t.Fatalf("a seven-model list drew a filter bar on line %d:\n%s", at, strings.Join(short, "\n"))
	}
	questionAt := planReviewFilterLine(short, question)
	if questionAt < 0 {
		t.Fatalf("the picker is missing its question:\n%s", strings.Join(short, "\n"))
	}
	if questionAt+1 >= len(short) || strings.TrimSpace(short[questionAt+1]) != "" {
		t.Fatalf("no blank line between the question and the models:\n%s", strings.Join(short, "\n"))
	}
	if questionAt+2 >= len(short) || !strings.Contains(short[questionAt+2], "opencode-go/model-01") {
		t.Fatalf("a short list must start its models under the blank line:\n%s", strings.Join(short, "\n"))
	}

	long := planReviewFilterLines(t, planReviewFilterOpen(t, planReviewFilterRequest(t, planReviewFilterModels(8))), 100)
	questionAt = planReviewFilterLine(long, question)
	bar := planReviewFilterLine(long, panelFilterGlyph)
	if bar < 0 {
		t.Fatalf("an eight-model list drew no filter bar:\n%s", strings.Join(long, "\n"))
	}
	if !strings.Contains(long[bar], "Type to filter") {
		t.Fatalf("the bar's row must offer %q, got %q", "Type to filter", long[bar])
	}
	if want := questionAt + 3; bar != want {
		t.Fatalf("the bar's text row is line %d, want %d (question, air, padded row):\n%s",
			bar, want, strings.Join(long, "\n"))
	}
	// The five rows between the question and the first model are /model's bar:
	// air, padded row, the text's row, padded row, air.
	for _, at := range []int{questionAt + 1, bar - 1, bar + 1, bar + 2} {
		if at < 0 || at >= len(long) || strings.TrimSpace(long[at]) != "" {
			t.Fatalf("the bar's row %d is not blank: %q\n%s", at, long[at], strings.Join(long, "\n"))
		}
	}
	if bar+3 >= len(long) || !strings.Contains(long[bar+3], "opencode-go/model-01") {
		t.Fatalf("the models must start under the bar's trailing air:\n%s", strings.Join(long, "\n"))
	}
}

// Typing narrows the list by the shown name — what the user reads — and by the
// endpoint under it, which is the other column a second row of the same model
// differs by.
func TestPlanReviewPickerFilterMatchesNameAndEndpoint(t *testing.T) {
	overlay := planReviewFilterOpen(t, planReviewFilterRequest(t, planReviewFilterFixtures()))
	planReviewFilterType(t, overlay, "deep")
	lines := planReviewFilterLines(t, overlay, 120)
	plain := strings.Join(lines, "\n")
	for _, want := range []string{"deepseek/deepseek-chat", "deepseek/deepseek-reasoner", "deepseek/deepseek-v4"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("the filter dropped the matching row %q:\n%s", want, plain)
		}
	}
	for _, gone := range []string{"gpt-5.6-luna", "glm-5.3", "gpt-5.1", "model-01", "model-02", "model-03", "model-04", "model-05"} {
		if strings.Contains(plain, gone) {
			t.Fatalf("the filter kept the row %q:\n%s", gone, plain)
		}
	}
	bar := planReviewFilterLine(lines, panelFilterGlyph)
	if bar < 0 || !strings.Contains(lines[bar], "deep") {
		t.Fatalf("the bar must show the query it is filtering by:\n%s", plain)
	}

	// The endpoint column matches on its own: that row's name says nothing the
	// query holds.
	overlay = planReviewFilterOpen(t, planReviewFilterRequest(t, planReviewFilterFixtures()))
	planReviewFilterType(t, overlay, "anthropic")
	plain = strings.Join(planReviewFilterLines(t, overlay, 120), "\n")
	if !strings.Contains(plain, "openai/gpt-5.1") {
		t.Fatalf("the endpoint-only match was dropped:\n%s", plain)
	}
	for _, gone := range []string{"deepseek", "gpt-5.6-luna", "glm-5.3", "opencode-go"} {
		if strings.Contains(plain, gone) {
			t.Fatalf("an endpoint query kept the row %q:\n%s", gone, plain)
		}
	}

	// Nothing matching says so, and leaves the bar in place.
	overlay = planReviewFilterOpen(t, planReviewFilterRequest(t, planReviewFilterFixtures()))
	planReviewFilterType(t, overlay, "zzz")
	lines = planReviewFilterLines(t, overlay, 120)
	plain = strings.Join(lines, "\n")
	if !strings.Contains(plain, "No matches") {
		t.Fatalf("an empty result must say %q:\n%s", "No matches", plain)
	}
	if planReviewFilterLine(lines, panelFilterGlyph) < 0 {
		t.Fatalf("the bar must stay while the list is empty:\n%s", plain)
	}
	for _, gone := range []string{"deepseek", "gpt-5.6-luna", "glm-5.3", "opencode-go"} {
		if strings.Contains(plain, gone) {
			t.Fatalf("an empty result kept the row %q:\n%s", gone, plain)
		}
	}
}

// The keyboard moves between the bar and the rows with ↑/↓, the query is edited
// with Backspace, and Esc still leaves the picker without deciding anything.
func TestPlanReviewPickerFilterKeys(t *testing.T) {
	overlay := planReviewFilterOpen(t, planReviewFilterRequest(t, planReviewFilterFixtures()))
	if overlay.planReviewInFilter {
		t.Fatal("the picker must open with the keyboard on its rows")
	}
	planReviewFilterType(t, overlay, "deepseek-chat")
	if !overlay.planReviewInFilter || overlay.planReviewFilter.text != "deepseek-chat" {
		t.Fatalf("typing must fill the filter: inFilter=%v text=%q", overlay.planReviewInFilter, overlay.planReviewFilter.text)
	}
	if len(overlay.planReviewFiltered) != 1 {
		t.Fatalf("the filtered list has %d rows, want 1", len(overlay.planReviewFiltered))
	}

	if _, done := overlay.handlePlanReviewKey(parsedKey{kind: rawKeyDown}); done || overlay.planReviewInFilter {
		t.Fatalf("Down must drop onto the first row: inFilter=%v done=%v", overlay.planReviewInFilter, done)
	}
	if overlay.planReviewCursor != 0 {
		t.Fatalf("Down left the cursor at %d, want the first row", overlay.planReviewCursor)
	}
	if _, done := overlay.handlePlanReviewKey(parsedKey{kind: rawKeyUp}); done || !overlay.planReviewInFilter {
		t.Fatalf("Up from the first row must climb back into the filter: inFilter=%v done=%v", overlay.planReviewInFilter, done)
	}

	// Backspace edits the query and re-filters: six characters shorter, the list
	// is the three rows "deepseek" matches again.
	for i := 0; i < 5; i++ {
		if _, done := overlay.handlePlanReviewKey(parsedKey{kind: rawKeyBackspace}); done {
			t.Fatal("Backspace ended the picker")
		}
	}
	if overlay.planReviewFilter.text != "deepseek" {
		t.Fatalf("filter text = %q, want %q", overlay.planReviewFilter.text, "deepseek")
	}
	if len(overlay.planReviewFiltered) != 3 {
		t.Fatalf("the re-filtered list has %d rows, want 3", len(overlay.planReviewFiltered))
	}

	decision, done := overlay.handlePlanReviewKey(parsedKey{kind: rawKeyEscape})
	if done {
		t.Fatal("Esc must go back, not decide")
	}
	if overlay.planReviewPicking {
		t.Fatal("Esc must close the picker")
	}
	if decision != (turn.ToolApprovalDecision{}) {
		t.Fatalf("Esc must decide nothing, got %#v", decision)
	}
}

// Enter takes the row the filter leaves — not the row the cursor sat on before
// the query narrowed the list — whether the keyboard is on the rows or still in
// the bar, where the first match is the top row. A query that matches nothing is
// not an answer: Enter keeps the picker open on it.
func TestPlanReviewPickerFilterEnterReturnsTheVisibleRow(t *testing.T) {
	take := func(t *testing.T, leaveFilter bool) turn.ToolApprovalDecision {
		t.Helper()
		overlay := planReviewFilterOpen(t, planReviewFilterRequest(t, planReviewFilterFixtures()))
		planReviewFilterType(t, overlay, "deepseek-reasoner")
		if leaveFilter {
			if _, done := overlay.handlePlanReviewKey(parsedKey{kind: rawKeyDown}); done {
				t.Fatal("Down ended the picker")
			}
		}
		decision, done := overlay.handlePlanReviewKey(parsedKey{kind: rawKeyEnter})
		if !done {
			t.Fatal("Enter on a matched row must decide")
		}
		return decision
	}
	for _, leaveFilter := range []bool{true, false} {
		decision := take(t, leaveFilter)
		if decision.RequestPlanReview == nil {
			t.Fatalf("leaveFilter=%v: Enter must request a review, got %#v", leaveFilter, decision)
		}
		if got := decision.RequestPlanReview.ID; got != "reasoner" {
			t.Fatalf("leaveFilter=%v: Enter took row %q, want the matched %q", leaveFilter, got, "reasoner")
		}
		if got := decision.RequestPlanReview.Endpoint; !strings.Contains(got, "api.deepseek.com") {
			t.Fatalf("leaveFilter=%v: row endpoint = %q", leaveFilter, got)
		}
	}

	overlay := planReviewFilterOpen(t, planReviewFilterRequest(t, planReviewFilterFixtures()))
	planReviewFilterType(t, overlay, "zzz")
	if _, done := overlay.handlePlanReviewKey(parsedKey{kind: rawKeyEnter}); done {
		t.Fatal("Enter with no match must not close the picker on a model the user never saw")
	}
	if !overlay.planReviewPicking {
		t.Fatal("the picker must stay open on an empty result")
	}
	if overlay.planReviewFilter.text != "zzz" {
		t.Fatalf("filter text = %q, want the query kept", overlay.planReviewFilter.text)
	}
}

// The hand-off row's picker is the same list under the same filter: Enter hands
// the plan to the row the query left.
func TestPlanHandOffPickerFiltersModels(t *testing.T) {
	overlay := newApprovalOverlay(context.Background(), io.Discard, nil, planReviewFilterRequest(t, planReviewFilterFixtures()), nil)
	overlay.enterHandOffMode()
	planReviewFilterType(t, overlay, "deepseek-v4")
	if len(overlay.planReviewFiltered) != 1 {
		t.Fatalf("the filtered list has %d rows, want 1", len(overlay.planReviewFiltered))
	}
	decision, done := overlay.handlePlanReviewKey(parsedKey{kind: rawKeyEnter})
	if !done {
		t.Fatal("Enter on the matched row must decide")
	}
	if !decision.Approved || decision.HandOffModel == nil {
		t.Fatalf("the hand-off must approve and name the executor, got %#v", decision)
	}
	if got := decision.HandOffModel.ID; got != "v4" {
		t.Fatalf("handed off to row %q, want the matched %q", got, "v4")
	}
	if decision.RequestPlanReview != nil {
		t.Fatalf("a hand-off must not also request a review: %#v", decision)
	}
	if overlay.planReviewPicking {
		t.Fatal("the pick made must close the picker")
	}
}

// The bar's caret is the only cursor the picker shows while the keyboard is in
// it — the rows keep no ❯ then — and it walks right through the query as the
// user types. On the rows the caret is gone and the ❯ is back.
func TestPlanReviewPickerFilterTakesTheCaret(t *testing.T) {
	overlay := planReviewFilterOpen(t, planReviewFilterRequest(t, planReviewFilterFixtures()))
	lines := planReviewFilterLines(t, overlay, 100)
	if bar := planReviewFilterLine(lines, panelFilterGlyph); bar < 0 {
		t.Fatalf("a twelve-model list drew no filter bar:\n%s", strings.Join(lines, "\n"))
	}
	if _, cursorLine := overlay.buildApprovalContent(100); cursorLine != -1 {
		t.Fatalf("the picker must hide the hardware cursor on its rows, got row %d", cursorLine)
	}

	start := overlay.planReviewFilter.pickerField(true).caretCol()
	planReviewFilterType(t, overlay, "deep")
	rendered, cursorLine := overlay.buildApprovalContent(100)
	lines = make([]string, len(rendered))
	for i, line := range rendered {
		lines[i] = stripANSI(line)
	}
	bar := planReviewFilterLine(lines, panelFilterGlyph)
	if bar < 0 {
		t.Fatalf("the bar vanished while typing:\n%s", strings.Join(lines, "\n"))
	}
	if cursorLine != bar {
		t.Fatalf("the caret is on line %d, want the bar's text row %d", cursorLine, bar)
	}
	if moved := overlay.planReviewFilter.pickerField(true).caretCol(); moved != start+displayLineWidth("deep") {
		t.Fatalf("the caret is at column %d, want %d (start %d plus the query)", moved, start+displayLineWidth("deep"), start)
	}

	// Back on the rows the bar is blurred: no caret, and the ❯ marks the row.
	if _, done := overlay.handlePlanReviewKey(parsedKey{kind: rawKeyDown}); done {
		t.Fatal("Down ended the picker")
	}
	rendered, cursorLine = overlay.buildApprovalContent(100)
	if cursorLine != -1 {
		t.Fatalf("the blurred bar must not show the caret, got row %d", cursorLine)
	}
	plain := stripANSI(strings.Join(rendered, "\n"))
	if !strings.Contains(plain, selectorCursorGlyph+" deepseek") {
		t.Fatalf("the ❯ must come back to the selected row:\n%s", plain)
	}
}

// A short list keeps its old contract: no bar, j/k move the selection, and the
// keys that would edit a filter do nothing at all — digits included.
func TestPlanReviewPickerFilterShortListKeepsJK(t *testing.T) {
	overlay := planReviewFilterOpen(t, planReviewFilterRequest(t, planReviewFilterModels(3)))
	if bar := planReviewFilterLine(planReviewFilterLines(t, overlay, 100), panelFilterGlyph); bar >= 0 {
		t.Fatal("a three-model list must not draw a filter bar")
	}
	if overlay.planReviewCursor != 0 {
		t.Fatalf("cursor = %d, want the first row", overlay.planReviewCursor)
	}
	if _, done := overlay.handlePlanReviewKey(parsedKey{kind: rawKeyRune, r: 'j'}); done || overlay.planReviewCursor != 1 {
		t.Fatalf("j left the cursor at %d (done=%v)", overlay.planReviewCursor, done)
	}
	if _, done := overlay.handlePlanReviewKey(parsedKey{kind: rawKeyRune, r: 'j'}); done || overlay.planReviewCursor != 2 {
		t.Fatalf("j left the cursor at %d (done=%v)", overlay.planReviewCursor, done)
	}
	if _, done := overlay.handlePlanReviewKey(parsedKey{kind: rawKeyRune, r: 'k'}); done || overlay.planReviewCursor != 1 {
		t.Fatalf("k left the cursor at %d (done=%v)", overlay.planReviewCursor, done)
	}
	// No bar means no typing: a letter is neither a query nor a jump, and a
	// digit is still not a shortcut.
	for _, r := range []rune{'d', '1', '9'} {
		if _, done := overlay.handlePlanReviewKey(parsedKey{kind: rawKeyRune, r: r}); done {
			t.Fatalf("%q ended the picker", r)
		}
		if overlay.planReviewCursor != 1 || overlay.planReviewInFilter || overlay.planReviewFilter.text != "" {
			t.Fatalf("%q changed the picker: cursor=%d inFilter=%v text=%q",
				r, overlay.planReviewCursor, overlay.planReviewInFilter, overlay.planReviewFilter.text)
		}
	}
}

// Backspace on an empty filter the keyboard is not in must not steal the ❯ to
// edit nothing — the guard every other picker keeps — while a filter that does
// hold text still takes the keyboard back from the rows.
func TestPlanReviewPickerFilterEmptyKeepsTheKeyboardOnTheRows(t *testing.T) {
	overlay := planReviewFilterOpen(t, planReviewFilterRequest(t, planReviewFilterFixtures()))
	for _, key := range []parsedKey{{kind: rawKeyBackspace}, {kind: rawKeyDelete}} {
		if _, done := overlay.handlePlanReviewKey(key); done {
			t.Fatalf("%v ended the picker", key.kind)
		}
		if overlay.planReviewInFilter {
			t.Fatalf("%v took the keyboard into an empty filter", key.kind)
		}
		if len(overlay.planReviewFiltered) != len(planReviewFilterFixtures()) {
			t.Fatalf("%v changed the list to %d rows", key.kind, len(overlay.planReviewFiltered))
		}
	}
	plain := strings.Join(planReviewFilterLines(t, overlay, 120), "\n")
	if !strings.Contains(plain, selectorCursorGlyph+" deepseek/deepseek-chat") {
		t.Fatalf("the ❯ must stay on the selected row:\n%s", plain)
	}

	// The way back: a query does hold text, so Backspace from the rows edits it
	// and re-filters.
	planReviewFilterType(t, overlay, "deepseek-chat")
	if _, done := overlay.handlePlanReviewKey(parsedKey{kind: rawKeyDown}); done || overlay.planReviewInFilter {
		t.Fatalf("Down must put the keyboard back on the rows: inFilter=%v done=%v", overlay.planReviewInFilter, done)
	}
	for i := 0; i < 5; i++ {
		if _, done := overlay.handlePlanReviewKey(parsedKey{kind: rawKeyBackspace}); done {
			t.Fatal("Backspace ended the picker")
		}
	}
	if !overlay.planReviewInFilter {
		t.Fatal("Backspace on a non-empty filter must take the keyboard back")
	}
	if overlay.planReviewFilter.text != "deepseek" {
		t.Fatalf("filter text = %q, want %q", overlay.planReviewFilter.text, "deepseek")
	}
	if len(overlay.planReviewFiltered) != 3 {
		t.Fatalf("the re-filtered list has %d rows, want 3", len(overlay.planReviewFiltered))
	}
}

// The path users take most: the keyboard is on the rows the picker opened with,
// and typing narrows the list from there — no ↓ or click first.
func TestPlanReviewPickerFilterTypesFromTheRows(t *testing.T) {
	overlay := planReviewFilterOpen(t, planReviewFilterRequest(t, planReviewFilterFixtures()))
	if overlay.planReviewInFilter {
		t.Fatal("the picker must open with the keyboard on its rows")
	}
	planReviewFilterType(t, overlay, "deep")
	if !overlay.planReviewInFilter {
		t.Fatal("typing from the rows must move the keyboard into the bar")
	}
	if overlay.planReviewFilter.text != "deep" {
		t.Fatalf("filter text = %q, want %q", overlay.planReviewFilter.text, "deep")
	}
	plain := strings.Join(planReviewFilterLines(t, overlay, 120), "\n")
	for _, want := range []string{"deepseek/deepseek-chat", "deepseek/deepseek-reasoner", "deepseek/deepseek-v4"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("typing from the rows dropped the matching row %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "model-01") {
		t.Fatalf("typing from the rows did not narrow the list:\n%s", plain)
	}
}

// The bar follows the caret row, so a plan tall enough to scroll its overlay
// cannot take the bar off screen while the user types: the query and its caret
// stay visible on every keystroke, and the rows below hold no ❯ until the
// keyboard returns to them.
func TestPlanReviewPickerFilterBarStaysOnScreen(t *testing.T) {
	r := newComposerRenderer(t, 80, 24)
	req := exitPlanReviewRequest(t, "# Plan\n\n"+strings.Repeat("- a step of the plan\n", 60))
	req.PlanReviewModels = planReviewFilterModels(12)
	overlay := newApprovalOverlay(context.Background(), io.Discard, newRawSelector(nil, r), withServiceDecisions(req), nil)
	overlay.resetFocusForOpen()
	overlay.enterPlanReviewMode()
	overlay.renderViewport()
	if !strings.Contains(paintedScreen(r), panelFilterGlyph) {
		t.Fatalf("the picker must open with its bar on screen:\n%s", paintedScreen(r))
	}

	for _, key := range []parsedKey{
		{kind: rawKeyRune, r: 'm'}, {kind: rawKeyRune, r: 'o'}, {kind: rawKeyRune, r: 'd'},
		{kind: rawKeyRune, r: 'e'}, {kind: rawKeyRune, r: 'l'}, {kind: rawKeyRune, r: '-'},
	} {
		if _, done := overlay.handleKey(key); done {
			t.Fatalf("%q ended the overlay", key.r)
		}
		overlay.renderViewport()
		screen := paintedScreen(r)
		if !strings.Contains(screen, panelFilterGlyph) {
			t.Fatalf("the bar scrolled off screen while typing %q:\n%s", key.r, screen)
		}
		if strings.Contains(screen, selectorCursorGlyph+" opencode-go") {
			t.Fatalf("the rows must hold no ❯ while the keyboard is in the bar (typed %q):\n%s", key.r, screen)
		}
	}
	if !strings.Contains(paintedScreen(r), "model-0") {
		t.Fatalf("the bar must show the query it is filtering by:\n%s", paintedScreen(r))
	}
}

// The bar's three rows fill the panel exactly at every width the overlay is
// drawn at: the padded rows and the text's row all run from the margin to the
// right edge, so the bar is one block rather than a ragged strip.
func TestPlanReviewPickerFilterBarWidth(t *testing.T) {
	for _, width := range []int{120, 80, 68, 40} {
		overlay := planReviewFilterOpen(t, planReviewFilterRequest(t, planReviewFilterModels(8)))
		rendered, _ := overlay.buildApprovalContent(width)
		bar := -1
		for i, line := range rendered {
			if strings.Contains(stripANSI(line), panelFilterGlyph) {
				bar = i
				break
			}
		}
		if bar < 0 {
			t.Fatalf("width %d drew no bar:\n%s", width, stripANSI(strings.Join(rendered, "\n")))
		}
		if bar < 1 || bar+1 >= len(rendered) {
			t.Fatalf("width %d: the bar's text row is line %d of %d", width, bar, len(rendered))
		}
		for _, at := range []int{bar - 1, bar, bar + 1} {
			if got := displayLineWidth(rendered[at]); got != width {
				t.Fatalf("width %d: bar row %d is %d columns wide, want %d: %q",
					width, at, got, width, stripANSI(rendered[at]))
			}
		}
	}
}
