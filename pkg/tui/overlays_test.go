package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
	"github.com/mattn/go-runewidth"
)

type approvalPaintWriter struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	painted chan struct{}
	once    sync.Once
	writes  chan struct{}
}

func newApprovalPaintWriter() *approvalPaintWriter {
	return &approvalPaintWriter{
		painted: make(chan struct{}),
		writes:  make(chan struct{}, 64),
	}
}

func (w *approvalPaintWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	n, err := w.buf.Write(p)
	hasOverlay := strings.Contains(w.buf.String(), "? for keys")
	w.mu.Unlock()
	if hasOverlay {
		w.once.Do(func() { close(w.painted) })
	}
	select {
	case w.writes <- struct{}{}:
	default:
	}
	return n, err
}

func (w *approvalPaintWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// clearWriteSignals drops pending write notifications so a later waitForWrites
// observes only paints that happen after this point.
func (w *approvalPaintWriter) clearWriteSignals() {
	for {
		select {
		case <-w.writes:
		default:
			return
		}
	}
}

func (w *approvalPaintWriter) waitForWrites(t *testing.T, n int) {
	t.Helper()
	for range n {
		select {
		case <-w.writes:
		case <-time.After(5 * time.Second):
			t.Fatal("approval overlay did not paint")
		}
	}
}

func (w *approvalPaintWriter) waitForPaint(t *testing.T) {
	t.Helper()
	select {
	case <-w.painted:
	case <-time.After(5 * time.Second):
		t.Fatal("approval overlay did not render")
	}
}

func TestApprovalOverlayRunDiscardsQueuedNavigationBeforeInitialRender(t *testing.T) {
	forcedTermWidth = 80
	forcedTermHeight = 24
	t.Cleanup(func() { forcedTermWidth, forcedTermHeight = 0, 0 })

	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() {
		readEnd.Close()
		writeEnd.Close()
	})

	out := newApprovalPaintWriter()
	r := NewRenderer(out, out)
	r.EnableViewportMode()
	t.Cleanup(r.DisableViewportMode)
	rs := newRawSelector(readEnd, r)
	overlay := newApprovalOverlay(context.Background(), out, rs, withServiceDecisions(turn.ToolApprovalRequest{
		ToolName:      "Bash",
		ToolInputJSON: `{"command":"ls"}`,
	}), nil)

	// This Down arrow represents navigation typed before the approval overlay
	// took exclusive ownership of stdin. It must not select option 2.
	if _, err := writeEnd.Write([]byte{0x1b, '[', 'B'}); err != nil {
		t.Fatalf("write stale navigation: %v", err)
	}

	type result struct {
		decision turn.ToolApprovalDecision
		err      error
	}
	done := make(chan result, 1)
	go func() {
		decision, err := overlay.Run()
		done <- result{decision: decision, err: err}
	}()

	out.waitForPaint(t)
	if _, err := writeEnd.Write([]byte{'\r'}); err != nil {
		t.Fatalf("write Enter: %v", err)
	}
	got := <-done
	if got.err != nil {
		t.Fatalf("Run: %v", got.err)
	}
	if !got.decision.Approved || got.decision.Denied || got.decision.Cancelled || got.decision.Update != nil {
		t.Fatalf("expected Enter to approve option 1 after discarding stale navigation, got %+v", got.decision)
	}
}

func TestApprovalOverlayRunResetsInitialFocusToFirstOption(t *testing.T) {
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() {
		readEnd.Close()
		writeEnd.Close()
	})

	rs := newRawSelector(readEnd, nil)
	overlay := newApprovalOverlay(context.Background(), &bytes.Buffer{}, rs, withServiceDecisions(turn.ToolApprovalRequest{
		ToolName:      "Bash",
		ToolInputJSON: `{"command":"ls"}`,
	}), nil)
	overlay.cursor = 3

	go func() {
		_, _ = writeEnd.Write([]byte{'\r'})
		_ = writeEnd.Close()
	}()

	decision, err := overlay.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !decision.Approved || decision.Denied || decision.Cancelled || decision.Update != nil {
		t.Fatalf("expected Enter to approve option 1 after open reset, got %+v", decision)
	}
}

func TestQuestionOverlayRunResetsInitialFocusToFirstOption(t *testing.T) {
	form := state.AskForm{Questions: []state.AskQuestion{{
		ID:     "q1",
		Prompt: "Pick one",
		Options: []state.AskOption{
			{ID: "a", Label: "Alpha"},
			{ID: "b", Label: "Beta"},
			{ID: "c", Label: "Gamma"},
		},
	}}}
	payload, err := json.Marshal(form)
	if err != nil {
		t.Fatalf("marshal form: %v", err)
	}

	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() {
		readEnd.Close()
		writeEnd.Close()
	})

	rs := newRawSelector(readEnd, nil)
	overlay, err := newQuestionOverlay(context.Background(), &bytes.Buffer{}, rs, turn.ToolApprovalRequest{AskFormJSON: string(payload)})
	if err != nil {
		t.Fatalf("newQuestionOverlay: %v", err)
	}
	overlay.cursor = 2

	go func() {
		_, _ = writeEnd.Write([]byte{'\r'})
		_ = writeEnd.Close()
	}()

	decision, err := overlay.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !decision.Approved {
		t.Fatalf("expected approved answer, got %+v", decision)
	}
	var answer state.AskAnswer
	if err := json.Unmarshal([]byte(decision.AskAnswerJSON), &answer); err != nil {
		t.Fatalf("decode answer: %v", err)
	}
	if len(answer.Answers) != 1 || len(answer.Answers[0].OptionIDs) != 1 || answer.Answers[0].OptionIDs[0] != "a" {
		t.Fatalf("expected first option selected after open reset, got %+v", answer)
	}
}

func TestViewportSelectionOverlayForceHidesDesyncedHardwareCursor(t *testing.T) {
	forcedTermWidth = 80
	forcedTermHeight = 24
	t.Cleanup(func() { forcedTermWidth, forcedTermHeight = 0, 0 })

	var out bytes.Buffer
	r := NewRenderer(&out, &out)
	r.EnableViewportMode()
	t.Cleanup(r.DisableViewportMode)
	r.RenderFrame(Frame{Kind: FrameSystem, Title: "sys", Content: "context", Final: true})

	// Simulate a desync where the renderer believes the cursor is hidden, but the
	// terminal may still have a visible hardware cursor from earlier output.
	r.mu.Lock()
	r.cursorShown = false
	r.mu.Unlock()
	out.Reset()

	rs := newRawSelector(nil, r)
	overlay := newApprovalOverlay(context.Background(), &out, rs, turn.ToolApprovalRequest{
		ToolName:      "Bash",
		ToolInputJSON: `{"command":"ls"}`,
	}, nil)
	overlay.renderViewport()

	rendered := out.String()
	if !strings.Contains(rendered, "\x1b[?25l") {
		t.Fatalf("selection overlay must explicitly hide a possibly desynced hardware cursor, got:\n%s", rendered)
	}
	if strings.Contains(rendered, "\x1b[?25h") {
		t.Fatalf("selection overlay must not show the hardware cursor, got:\n%s", rendered)
	}
}

// TestOverlaySeparatorStaysPinnedWhileScrolling guards the fix for a tall modal
// overlay losing its top border: the separator rule used to be prepended INTO
// the scrollable slice, so the first wheel notch scrolled it away and the
// overlay appeared fused to the transcript above it. The rule is pinned to row
// 0 of the composer block and only the content below it scrolls.
func TestOverlaySeparatorStaysPinnedWhileScrolling(t *testing.T) {
	forcedTermWidth = 80
	forcedTermHeight = 12
	t.Cleanup(func() { forcedTermWidth, forcedTermHeight = 0, 0 })

	var out bytes.Buffer
	r := NewRenderer(&out, &out)
	r.EnableViewportMode()
	t.Cleanup(r.DisableViewportMode)

	for i := 0; i < 40; i++ {
		r.RenderFrame(Frame{Kind: FrameStatus, Content: fmt.Sprintf("transcript line %d", i), Final: true})
	}

	// 20 lines against a maxComposerHeight of 12 - 4 = 8: the overlay is tall
	// enough to be independently scrollable.
	lines := make([]string, 20)
	for i := range lines {
		lines[i] = fmt.Sprintf("overlay line %d", i)
	}
	r.BeginComposerOverlay()
	r.SetOverlayComposer(lines, -1, 0)

	sep := overlaySeparatorLine(forcedTermWidth - viewportRightPadding)

	// Scroll from the top all the way to the bottom; the rule must head the
	// block at every offset, with the content sliding underneath it.
	for step := 0; ; step++ {
		got := r.vpLastComposer.lines
		if len(got) == 0 {
			t.Fatalf("step %d: overlay painted no rows", step)
		}
		if got[0] != sep {
			t.Fatalf("step %d (scroll offset %d): first overlay row = %q, want the separator rule %q",
				step, r.overlayScrollOffset, got[0], sep)
		}
		if len(got) < 2 {
			t.Fatalf("step %d: overlay painted only the separator", step)
		}
		want := fmt.Sprintf("overlay line %d", r.overlayScrollOffset)
		if got[1] != want {
			t.Fatalf("step %d: row below the separator = %q, want %q", step, got[1], want)
		}
		// The pinned rule replaces one content row, so the block height is
		// unchanged from the pre-fix layout.
		if len(got) != 8 {
			t.Fatalf("step %d: overlay block height = %d, want 8", step, len(got))
		}

		before := r.overlayScrollOffset
		r.ViewportScrollOverlay(1)
		if r.overlayScrollOffset == before {
			break // reached max scroll
		}
		if step > 40 {
			t.Fatalf("overlay scroll did not converge")
		}
	}

	// At the bottom the last overlay line is visible and the rule is still there.
	got := r.vpLastComposer.lines
	if got[0] != sep {
		t.Fatalf("at max scroll: first overlay row = %q, want the separator rule", got[0])
	}
	if last := got[len(got)-1]; last != "overlay line 19" {
		t.Fatalf("at max scroll: last overlay row = %q, want %q", last, "overlay line 19")
	}
}

// TestOverlayCursorRowAccountsForPinnedSeparator guards the cursor offset math
// across the pin: the caret row is reported in content coordinates and must be
// shifted past the separator only after the content slice is taken.
func TestOverlayCursorRowAccountsForPinnedSeparator(t *testing.T) {
	forcedTermWidth = 80
	forcedTermHeight = 12
	t.Cleanup(func() { forcedTermWidth, forcedTermHeight = 0, 0 })

	var out bytes.Buffer
	r := NewRenderer(&out, &out)
	r.EnableViewportMode()
	t.Cleanup(r.DisableViewportMode)

	lines := make([]string, 20)
	for i := range lines {
		lines[i] = fmt.Sprintf("overlay line %d", i)
	}

	// A caret near the end forces an auto-scroll; wherever it lands, the
	// reported row must point at the line that actually holds the caret.
	const caret = 18
	r.BeginComposerOverlay()
	r.SetOverlayComposer(lines, caret, 0)

	cb := r.vpLastComposer
	if cb.cursorRow <= 0 {
		t.Fatalf("caret row = %d, want a row below the pinned separator", cb.cursorRow)
	}
	if cb.cursorRow >= len(cb.lines) {
		t.Fatalf("caret row %d is outside the %d painted overlay rows", cb.cursorRow, len(cb.lines))
	}
	if got, want := cb.lines[cb.cursorRow], fmt.Sprintf("overlay line %d", caret); got != want {
		t.Fatalf("caret row %d holds %q, want %q", cb.cursorRow, got, want)
	}
}

// TestMouseWheelEventCarriesPointerPosition guards the fix to
// mouseEventFromSGR: wheel notches must carry the pointer's col/row so
// ViewportScrollAt can route them to the overlay (when the cursor is over it)
// instead of always scrolling the transcript at row 0.
func TestMouseWheelEventCarriesPointerPosition(t *testing.T) {
	// Wheel up: Cb=64 (bit6 set, bit0 clear => up), Cx=6 => col 5, Cy=11 => row 10.
	ev, ok := parseSGRMouse([]byte("\x1b[<64;6;11M"))
	if !ok {
		t.Fatalf("parseSGRMouse failed to decode wheel-up report")
	}
	if !ev.wheel || !ev.wheelUp {
		t.Fatalf("decoded wheel-up report wrong: %+v", ev)
	}
	got, ok := mouseEventFromSGR(ev)
	if !ok {
		t.Fatalf("mouseEventFromSGR dropped a wheel report")
	}
	if got.kind != inputEventMouseWheel {
		t.Fatalf("expected inputEventMouseWheel, got %v", got.kind)
	}
	if got.wheelDelta != -wheelScrollLines {
		t.Fatalf("wheel-up delta = %d, want %d", got.wheelDelta, -wheelScrollLines)
	}
	if got.mouseCol != 5 || got.mouseRow != 10 {
		t.Fatalf("wheel event must carry pointer position col=5 row=10, got col=%d row=%d", got.mouseCol, got.mouseRow)
	}

	// Wheel down: Cb=65 (bit6 set, bit0 set => down), same position.
	ev2, ok := parseSGRMouse([]byte("\x1b[<65;6;11M"))
	if !ok {
		t.Fatalf("parseSGRMouse failed to decode wheel-down report")
	}
	got2, ok := mouseEventFromSGR(ev2)
	if !ok {
		t.Fatalf("mouseEventFromSGR dropped a wheel report")
	}
	if got2.wheelDelta != wheelScrollLines {
		t.Fatalf("wheel-down delta = %d, want %d", got2.wheelDelta, wheelScrollLines)
	}
	if got2.mouseCol != 5 || got2.mouseRow != 10 {
		t.Fatalf("wheel-down event must carry pointer position col=5 row=10, got col=%d row=%d", got2.mouseCol, got2.mouseRow)
	}
}

// TestViewportScrollAtOverTallOverlayScrollsOverlay verifies that a wheel notch
// whose pointer is over a tall (scrollable) composer overlay scrolls the overlay
// content, not the transcript.
func TestViewportScrollAtOverTallOverlayScrollsOverlay(t *testing.T) {
	forcedTermWidth = 80
	forcedTermHeight = 12
	t.Cleanup(func() { forcedTermWidth, forcedTermHeight = 0, 0 })

	var out bytes.Buffer
	r := NewRenderer(&out, &out)
	r.EnableViewportMode()
	t.Cleanup(r.DisableViewportMode)

	// Enough transcript to be independently scrollable, so a leaked wheel would
	// be observable as a transcript scroll.
	for i := 0; i < 40; i++ {
		r.RenderFrame(Frame{Kind: FrameStatus, Content: fmt.Sprintf("transcript line %d", i), Final: true})
	}
	out.Reset()

	// Tall overlay: 20 lines + separator = 21 > maxComposerHeight (= 12 - 4 = 8).
	lines := make([]string, 20)
	for i := range lines {
		lines[i] = fmt.Sprintf("overlay line %d", i)
	}
	r.BeginComposerOverlay()
	r.SetOverlayComposer(lines, -1, 0)

	if r.overlayScrollOffset != 0 {
		t.Fatalf("tall overlay should start unscrolled, got offset %d", r.overlayScrollOffset)
	}
	bodyHeight := r.vpBodyHeight

	// Wheel down over the overlay area.
	r.ViewportScrollAt(1, 0, bodyHeight)
	if r.overlayScrollOffset != 1 {
		t.Fatalf("wheel over tall overlay should scroll overlay by 1, got offset %d", r.overlayScrollOffset)
	}
	// The transcript must be untouched: follow stays armed and offset pinned.
	if !r.vpFollow {
		t.Fatalf("wheel over overlay leaked to transcript (follow disarmed)")
	}
}

// TestViewportScrollAtOverShortOverlayFallsThroughToTranscript verifies that a
// wheel notch over a short overlay that cannot scroll falls through to the
// transcript instead of being swallowed. This preserves the pre-overlay-scroll
// behaviour where wheeling during a short picker scrolled the conversation.
func TestViewportScrollAtOverShortOverlayFallsThroughToTranscript(t *testing.T) {
	forcedTermWidth = 80
	forcedTermHeight = 12
	t.Cleanup(func() { forcedTermWidth, forcedTermHeight = 0, 0 })

	var out bytes.Buffer
	r := NewRenderer(&out, &out)
	r.EnableViewportMode()
	t.Cleanup(r.DisableViewportMode)

	for i := 0; i < 40; i++ {
		r.RenderFrame(Frame{Kind: FrameStatus, Content: fmt.Sprintf("transcript line %d", i), Final: true})
	}
	out.Reset()

	// Short overlay: 3 lines + separator = 4, fits in maxComposerHeight (8).
	r.BeginComposerOverlay()
	r.SetOverlayComposer([]string{"a", "b", "c"}, -1, 0)

	if r.overlayScrollOffset != 0 {
		t.Fatalf("short overlay must not scroll, got offset %d", r.overlayScrollOffset)
	}
	bodyHeight := r.vpBodyHeight

	// Wheel up over the overlay area: overlay can't scroll, so it must fall
	// through to the transcript.
	r.ViewportScrollAt(-1, 0, bodyHeight)
	if r.overlayScrollOffset != 0 {
		t.Fatalf("short overlay must not scroll, got offset %d", r.overlayScrollOffset)
	}
	if r.vpFollow {
		t.Fatalf("wheel over short overlay should fall through and scroll transcript (disarm follow)")
	}
}

// TestApprovalOverlayWheelScrollsTallOverlay verifies the approval overlay's
// Run loop routes mouse-wheel notches to the viewport so a tall overlay (long
// wrapped command, file diff, keymap/explain panes) can be scrolled with the
// wheel instead of only PgUp/PgDn. Before the fix Run dropped every mouse
// event including wheel notches.
func TestApprovalOverlayWheelScrollsTallOverlay(t *testing.T) {
	forcedTermWidth = 40
	forcedTermHeight = 16
	t.Cleanup(func() { forcedTermWidth, forcedTermHeight = 0, 0 })

	out := newApprovalPaintWriter()
	r := NewRenderer(out, out)
	r.EnableViewportMode()
	t.Cleanup(r.DisableViewportMode)
	r.RenderFrame(Frame{Kind: FrameSystem, Title: "sys", Content: "context", Final: true})

	// A long command so the "Command:" line wraps to many rows, pushing the
	// overlay taller than its allocated area (independently scrollable).
	longCmd := strings.Repeat("echo-"+strings.Repeat("x", 30)+" ", 20)
	req := turn.ToolApprovalRequest{
		ToolName:      "Bash",
		ToolInputJSON: fmt.Sprintf(`{"command":%q}`, longCmd),
	}

	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() {
		readEnd.Close()
		writeEnd.Close()
	})

	rs := newRawSelector(readEnd, r)
	overlay := newApprovalOverlay(context.Background(), out, rs, req, nil)

	// Render once to establish the overlay layout. A tall overlay opens
	// anchored at the end of its content so the option rows are on screen.
	overlay.renderViewport()
	r.mu.Lock()
	startOffset := r.overlayScrollOffset
	bodyHeight := r.vpBodyHeight
	r.mu.Unlock()
	if startOffset <= 0 {
		t.Fatalf("tall overlay should open anchored on its options, got offset %d", startOffset)
	}
	if bodyHeight < 1 {
		t.Fatalf("unexpected bodyHeight %d", bodyHeight)
	}

	// Input must arrive after Run's first paint; earlier bytes are pre-open
	// input that Run discards.
	out.clearWriteSignals()
	done := make(chan error, 1)
	go func() {
		_, err := overlay.Run()
		done <- err
	}()

	out.waitForWrites(t, 1)
	// Inject a wheel-up SGR report over the overlay area (row well within the
	// bottom-pinned overlay block), then Enter to commit option 1 and exit.
	// The overlay opens at the end of its content, so wheeling UP is what
	// proves the notch reached it.
	// ESC [ < 64 ; 5 ; <row+1> M  => wheel up at col 4, screen row `row`.
	wheelRow := bodyHeight + 2
	seq := fmt.Sprintf("\x1b[<64;5;%dM", wheelRow+1)
	if _, err := writeEnd.Write([]byte(seq)); err != nil {
		t.Fatalf("write wheel event: %v", err)
	}
	if _, err := writeEnd.Write([]byte{'\r'}); err != nil {
		t.Fatalf("write Enter: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("overlay run failed: %v", err)
	}

	r.mu.Lock()
	got := r.overlayScrollOffset
	r.mu.Unlock()
	if got >= startOffset {
		t.Fatalf("wheel-up over tall approval overlay should scroll it; overlayScrollOffset=%d (open offset %d, bodyHeight=%d)", got, startOffset, bodyHeight)
	}
}

// TestApprovalOverlayReopenDropsStaleScrollOffset checks that an offset left by
// a previous modal never carries into a freshly opened approval: the new
// overlay positions itself from its own content, anchored at the end so its
// option rows are visible under a body (a full file diff, a wrapped command)
// taller than the overlay area.
func TestApprovalOverlayReopenDropsStaleScrollOffset(t *testing.T) {
	forcedTermWidth = 40
	forcedTermHeight = 16
	t.Cleanup(func() { forcedTermWidth, forcedTermHeight = 0, 0 })

	out := newApprovalPaintWriter()
	r := NewRenderer(out, out)
	r.EnableViewportMode()
	t.Cleanup(r.DisableViewportMode)
	r.RenderFrame(Frame{Kind: FrameSystem, Title: "sys", Content: "context", Final: true})

	longCmd := strings.Repeat("echo-"+strings.Repeat("x", 30)+" ", 20)
	req := turn.ToolApprovalRequest{
		ToolName:      "Bash",
		ToolInputJSON: fmt.Sprintf(`{"command":%q}`, longCmd),
	}

	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() {
		readEnd.Close()
		writeEnd.Close()
	})

	rs := newRawSelector(readEnd, r)
	overlay := newApprovalOverlay(context.Background(), out, rs, req, nil)

	r.mu.Lock()
	r.overlayScrollOffset = 5
	r.mu.Unlock()

	out.clearWriteSignals()
	done := make(chan error, 1)
	go func() {
		_, err := overlay.Run()
		done <- err
	}()

	out.waitForWrites(t, 1)
	r.mu.Lock()
	openOffset := r.overlayScrollOffset
	wantOffset := r.overlayMaxScrollLocked()
	r.mu.Unlock()
	if openOffset == 5 {
		t.Fatalf("reopened approval overlay kept the stale scroll offset")
	}
	if openOffset != wantOffset {
		t.Fatalf("reopened approval overlay should anchor on its option rows (offset %d), got %d", wantOffset, openOffset)
	}

	if _, err := writeEnd.Write([]byte{'\r'}); err != nil {
		t.Fatalf("write Enter: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("overlay run failed: %v", err)
	}
}

func TestQuestionOverlayWheelScrollsTallOverlay(t *testing.T) {
	forcedTermWidth = 40
	forcedTermHeight = 16
	t.Cleanup(func() { forcedTermWidth, forcedTermHeight = 0, 0 })

	out := newApprovalPaintWriter()
	r := NewRenderer(out, out)
	r.EnableViewportMode()
	t.Cleanup(r.DisableViewportMode)
	r.RenderFrame(Frame{Kind: FrameSystem, Title: "sys", Content: "context", Final: true})

	// 30 options => the option list wraps well past the overlay's allocated
	// area, making it independently scrollable.
	opts := make([]state.AskOption, 30)
	for i := range opts {
		opts[i] = state.AskOption{
			ID:          fmt.Sprintf("opt-%d", i),
			Label:       fmt.Sprintf("Option %d", i),
			Description: fmt.Sprintf("description text for option number %d", i),
		}
	}
	form := state.AskForm{
		Title:     "Pick one",
		Questions: []state.AskQuestion{{ID: "q1", Prompt: "Choose an option", Options: opts}},
	}
	payload, err := json.Marshal(form)
	if err != nil {
		t.Fatalf("marshal form: %v", err)
	}
	req := turn.ToolApprovalRequest{AskFormJSON: string(payload)}

	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() {
		readEnd.Close()
		writeEnd.Close()
	})

	rs := newRawSelector(readEnd, r)
	overlay, err := newQuestionOverlay(context.Background(), out, rs, req)
	if err != nil {
		t.Fatalf("newQuestionOverlay: %v", err)
	}

	// Render once to establish layout; the overlay should start unscrolled
	// (the question overlay hides the hardware cursor with cursorRow=-1, so
	// no auto-scroll fights the manual offset).
	overlay.render()
	r.mu.Lock()
	startOffset := r.overlayScrollOffset
	bodyHeight := r.vpBodyHeight
	r.mu.Unlock()
	if startOffset != 0 {
		t.Fatalf("overlay should start unscrolled, got offset %d", startOffset)
	}
	if bodyHeight < 1 {
		t.Fatalf("unexpected bodyHeight %d", bodyHeight)
	}

	// Input must arrive after Run's first paint; earlier bytes are pre-open
	// input that Run discards.
	out.clearWriteSignals()
	done := make(chan error, 1)
	go func() {
		_, err := overlay.Run()
		done <- err
	}()

	out.waitForWrites(t, 1)
	// Inject a wheel-down SGR report over the overlay area, then Enter.
	// Single-question single-select auto-submits on Enter, exiting Run.
	wheelRow := bodyHeight + 2
	seq := fmt.Sprintf("\x1b[<65;5;%dM", wheelRow+1)
	if _, err := writeEnd.Write([]byte(seq)); err != nil {
		t.Fatalf("write wheel event: %v", err)
	}
	if _, err := writeEnd.Write([]byte{'\r'}); err != nil {
		t.Fatalf("write Enter: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("overlay run failed: %v", err)
	}

	r.mu.Lock()
	got := r.overlayScrollOffset
	r.mu.Unlock()
	if got <= 0 {
		t.Fatalf("wheel-down over tall question overlay should scroll it; overlayScrollOffset=%d (bodyHeight=%d)", got, bodyHeight)
	}
}

func TestQuestionOverlayReopenResetsScrollOffset(t *testing.T) {
	forcedTermWidth = 40
	forcedTermHeight = 16
	t.Cleanup(func() { forcedTermWidth, forcedTermHeight = 0, 0 })

	out := newApprovalPaintWriter()
	r := NewRenderer(out, out)
	r.EnableViewportMode()
	t.Cleanup(r.DisableViewportMode)
	r.RenderFrame(Frame{Kind: FrameSystem, Title: "sys", Content: "context", Final: true})

	opts := make([]state.AskOption, 30)
	for i := range opts {
		opts[i] = state.AskOption{
			ID:          fmt.Sprintf("opt-%d", i),
			Label:       fmt.Sprintf("Option %d", i),
			Description: fmt.Sprintf("description text for option number %d", i),
		}
	}
	form := state.AskForm{
		Title:     "Pick one",
		Questions: []state.AskQuestion{{ID: "q1", Prompt: "Choose an option", Options: opts}},
	}
	payload, err := json.Marshal(form)
	if err != nil {
		t.Fatalf("marshal form: %v", err)
	}
	req := turn.ToolApprovalRequest{AskFormJSON: string(payload)}

	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() {
		readEnd.Close()
		writeEnd.Close()
	})

	rs := newRawSelector(readEnd, r)
	overlay, err := newQuestionOverlay(context.Background(), out, rs, req)
	if err != nil {
		t.Fatalf("newQuestionOverlay: %v", err)
	}

	r.mu.Lock()
	r.overlayScrollOffset = 7
	r.mu.Unlock()

	out.clearWriteSignals()
	done := make(chan error, 1)
	go func() {
		_, err := overlay.Run()
		done <- err
	}()

	out.waitForWrites(t, 1)
	if _, err := writeEnd.Write([]byte{'\r'}); err != nil {
		t.Fatalf("write Enter: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("overlay run failed: %v", err)
	}

	r.mu.Lock()
	got := r.overlayScrollOffset
	r.mu.Unlock()
	if got != 0 {
		t.Fatalf("reopened question overlay should reset scroll offset to 0, got %d", got)
	}
}

// TestSelectorInputClickInsertsAtTheClickedPosition drives the modal text input
// end to end: click into the middle of the value, type, and the character lands
// where the click was.
func TestSelectorInputClickInsertsAtTheClickedPosition(t *testing.T) {
	r := newComposerRenderer(t, 80, 24)
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pr.Close(); pw.Close() })

	sel := newRawSelector(pr, r)
	type result struct {
		value string
		ok    bool
	}
	done := make(chan result, 1)
	go func() {
		value, ok, err := sel.Input("Name", "hello")
		if err != nil {
			t.Errorf("Input: %v", err)
		}
		done <- result{value, ok}
	}()

	// The panel is bottom-pinned in its fixed height: the rule, the
	// question, then the field.
	row := 24 - slashPanelHeight(24) + 2
	col := displayLineWidth(panelFieldLead) + 2 // between "he" and "llo"
	writeMouseClick(t, pw, col, row)
	time.Sleep(50 * time.Millisecond)
	pw.Write([]byte("X\r"))

	select {
	case got := <-done:
		if !got.ok || got.value != "heXllo" {
			t.Fatalf("typing after a click should insert at the click, got %q (ok=%v)", got.value, got.ok)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Input did not return")
	}
}

// TestSelectorFilterClickInsertsAtTheClickedPosition covers the picker filter,
// which is an editable field like any other: the caret goes where it is
// clicked and typing inserts there.
func TestSelectorFilterClickInsertsAtTheClickedPosition(t *testing.T) {
	st := &selectState{
		options:  []string{"alpha", "beta"},
		filtered: make([]int, 0, 2),
		label:    "pick",
	}
	st.filter = filterField{text: "abc", cursor: 3}
	st.applyFilter()

	sel := &rawSelector{}
	filterCol := displayLineWidth(panelFieldLead)
	if !sel.placeFieldCaret(st.panel(), &st.filter, st.panel().fieldRow(sel.panelWidth()), filterCol+1) {
		t.Fatal("a click on the filter row did not reach the filter")
	}
	st.filter.insert("X")
	if st.filter.text != "aXbc" {
		t.Fatalf("typing after a click should insert at the click, got %q", st.filter.text)
	}
	if got := st.filter.panelField().caretCol(); got != filterCol+2 {
		t.Fatalf("the caret column must follow the insertion, got %d", got)
	}
}

// TestSelectorFilterCaretCellHoldsWhatIsUnderIt walks the exact path the
// software caret takes across a picker's filter row: panelField.row builds the
// row, panelField.caretCol says which display column the caret occupies, and
// cellAtDisplayCol hands the painter the one cell to repaint there. The row
// opens with ›, an East Asian Ambiguous glyph, so a second width model anywhere
// in that chain shifts the lookup one column and the caret repaints the
// character in front of it: typing "a" painted "aa", and on an empty filter the
// placeholder's leading "T" was replaced by a blank.
func TestSelectorFilterCaretCellHoldsWhatIsUnderIt(t *testing.T) {
	cell := func(f *filterField) string {
		field := f.panelField()
		_, glyph := cellAtDisplayCol([]string{field.row()}, 0, field.caretCol())
		return glyph
	}

	// Empty filter: the caret sits on the placeholder's first cell.
	if glyph := cell(&filterField{}); glyph != "T" {
		t.Errorf("caret cell on an empty filter = %q, want the placeholder's %q", glyph, "T")
	}

	// Typing: the caret trails the text, so its cell is blank — never a copy of
	// the character before it.
	for _, text := range []string{"a", "as", "ass", "中文"} {
		field := &filterField{text: text, cursor: len([]rune(text))}
		if glyph := cell(field); glyph != " " {
			t.Errorf("filter %q: caret cell = %q, want a blank cell", text, glyph)
		}
		// The row still claims for the text exactly the columns the caret
		// arithmetic assumes it does.
		runes := []rune(text)
		lastRune := string(runes[len(runes)-1])
		last := field.panelField().caretCol() - lipgloss.Width(lastRune)
		if _, glyph := cellAtDisplayCol([]string{field.panelField().row()}, 0, last); glyph != lastRune {
			t.Errorf("filter %q: cell before the caret = %q, want %q", text, glyph, lastRune)
		}
	}

	// Caret moved back into the text: the cell is the character it covers.
	if glyph := cell(&filterField{text: "abc", cursor: 1}); glyph != "b" {
		t.Fatalf("caret cell inside the filter = %q, want %q", glyph, "b")
	}
}

// TestOverlayRulesSpanTheFullContentWidth pins the rules that head the modal
// overlays to the width they are asked for. They are built from ─, another
// ambiguous glyph, and used to divide their dash count by whatever width
// go-runewidth attributed to it — which drew them at half width on the very
// locales that workaround was written for. The overlay rule stops one column
// short, like renderSharedBlockBorderLine, so it never reaches the terminal's
// autowrap threshold; the approval rule is measured at its requested width.
func TestOverlayRulesSpanTheFullContentWidth(t *testing.T) {
	for _, width := range []int{1, 20, 118} {
		want := width - 1
		if width < 2 {
			want = 1
		}
		if got := lipgloss.Width(overlaySeparatorLine(width)); got != want {
			t.Errorf("overlaySeparatorLine(%d) spans %d columns, want %d", width, got, want)
		}
		if got := lipgloss.Width(approvalSeparatorLine(width)); got != width {
			t.Errorf("approvalSeparatorLine(%d) spans %d columns", width, got)
		}
	}
}

func TestApprovalFeedbackLayoutTracksHardAndSoftWraps(t *testing.T) {
	text := "abcd中文ef\n\nlast\n"
	layout := layoutApprovalFeedback(text, len([]rune(text)), 12)
	wantLines := []string{
		"> abcd中文",
		"  ef",
		"  ",
		"  last",
		"  ",
	}
	if fmt.Sprint(layout.lines) != fmt.Sprint(wantLines) {
		t.Fatalf("feedback lines = %#v, want %#v", layout.lines, wantLines)
	}
	if layout.cursorRow != 4 || layout.cursorCol != 2 {
		t.Fatalf("trailing-newline cursor = (%d,%d), want (4,2)", layout.cursorRow, layout.cursorCol)
	}

	cursor := len([]rune("abcd中文"))
	layout = layoutApprovalFeedback(text, cursor, 12)
	if layout.cursorRow != 1 || layout.cursorCol != 2 {
		t.Fatalf("soft-wrap boundary cursor = (%d,%d), want (1,2)", layout.cursorRow, layout.cursorCol)
	}
}

func TestApprovalFeedbackBuildUsesLayoutCursor(t *testing.T) {
	o := &approvalOverlay{planDenyEditing: true, out: io.Discard}
	o.feedbackBuf = []rune("abcd中文ef\nnext")
	o.feedbackCursor = len([]rune("abcd中文"))

	lines, cursorLine := o.buildApprovalContent(12)
	if cursorLine != o.feedbackFirstRow+1 {
		t.Fatalf("cursor line = %d, want second feedback row at %d", cursorLine, o.feedbackFirstRow+1)
	}
	if o.feedbackLayout.cursorCol != 2 {
		t.Fatalf("cursor column = %d, want 2", o.feedbackLayout.cursorCol)
	}
	if got := lines[o.feedbackFirstRow : o.feedbackFirstRow+len(o.feedbackLayout.lines)]; fmt.Sprint(got) != fmt.Sprint(o.feedbackLayout.lines) {
		t.Fatalf("rendered feedback rows = %#v, want %#v", got, o.feedbackLayout.lines)
	}
}

// At the width most terminals open at, the feedback editor's own rows — its
// title, the field and its footer — each stay one row wide. A title long enough
// to wrap arrives above the field as a broken half-sentence, and the key hints
// it would be carrying are already spelled out in the footer.
func TestApprovalFeedbackRowsFitTheViewportWidth(t *testing.T) {
	const width = 80
	o := &approvalOverlay{planDenyEditing: true, out: io.Discard}
	o.feedbackBuf = []rune(strings.Repeat("feedback ", 40) + "中文内容")
	o.feedbackCursor = len(o.feedbackBuf)

	lines, _ := o.buildApprovalContent(width)
	max := width - viewportRightPadding
	for i, line := range lines {
		if got := displayLineWidth(line); got > max {
			t.Fatalf("row %d is %d columns wide, over the %d the viewport paints: %q", i, got, max, stripANSI(line))
		}
	}
	title := stripANSI(lines[o.feedbackFirstRow-1])
	if !strings.HasPrefix(title, "Feedback for Forebrain Harness") || !strings.HasSuffix(title, ":") {
		t.Fatalf("the row above the field is not the whole title, so the title wrapped: %q", title)
	}
	footer := stripANSI(lines[len(lines)-1])
	if !strings.Contains(footer, "newline") || !strings.Contains(footer, "submit feedback") {
		t.Fatalf("the footer must carry the newline and submit hints on one row, got %q", footer)
	}
}

// feedbackLayout addresses feedbackBuf by rune range, so dropping the field
// must drop the layout with it: a layout left behind describes a buffer that is
// no longer there.
func TestApprovalFeedbackLayoutIsClearedWithTheBuffer(t *testing.T) {
	o := &approvalOverlay{planDenyEditing: true, out: io.Discard}
	o.feedbackBuf = []rune("some feedback\nover two rows")
	o.feedbackCursor = len(o.feedbackBuf)
	o.buildApprovalContent(40)
	if len(o.feedbackLayout.rows) < 2 {
		t.Fatalf("expected the field to occupy several rows, got %d", len(o.feedbackLayout.rows))
	}

	o.handlePlanDenyKey(parsedKey{kind: rawKeyEscape})
	if o.feedbackBuf != nil || o.feedbackLayout.rows != nil || o.feedbackLayout.lines != nil {
		t.Fatalf("cancelling feedback left buf=%q layout=%#v", string(o.feedbackBuf), o.feedbackLayout)
	}
	if o.feedbackCursor != 0 || o.feedbackFirstRow != 0 {
		t.Fatalf("cancelling feedback left cursor=%d firstRow=%d", o.feedbackCursor, o.feedbackFirstRow)
	}
}

func TestApprovalFeedbackVerticalNavigationUsesVisualRows(t *testing.T) {
	o := &approvalOverlay{planDenyEditing: true, out: io.Discard}
	o.feedbackBuf = []rune("abcdefghi\nxy")
	o.feedbackCursor = 4

	o.buildApprovalContent(10)
	o.handlePlanDenyKey(parsedKey{kind: rawKeyDown})
	if o.feedbackCursor != 9 {
		t.Fatalf("Down to short soft-wrapped row cursor = %d, want 9", o.feedbackCursor)
	}

	o.buildApprovalContent(10)
	o.handlePlanDenyKey(parsedKey{kind: rawKeyDown})
	if o.feedbackCursor != 12 {
		t.Fatalf("Down across hard newline cursor = %d, want 12", o.feedbackCursor)
	}

	o.buildApprovalContent(10)
	o.handlePlanDenyKey(parsedKey{kind: rawKeyUp})
	if o.feedbackCursor != 8 {
		t.Fatalf("Up from hard line cursor = %d, want 8", o.feedbackCursor)
	}
}

// Home and End work on the hard line the caret is on, not on the visual row:
// a soft-wrapped row that broke mid-word shares its last position with the next
// row's first, so a visual-row End would render the caret a row below the key
// that was pressed.
func TestApprovalFeedbackHomeEndMoveToTheHardLineEdges(t *testing.T) {
	o := &approvalOverlay{planDenyEditing: true, out: io.Discard}
	o.feedbackBuf = []rune("abcdefghi\nxy\n")
	wrapped := len([]rune("abcdefghi"))

	// A caret inside the second visual row of the first hard line.
	o.feedbackCursor = 7
	o.buildApprovalContent(10)
	if len(o.feedbackLayout.rows) != 4 {
		t.Fatalf("expected the fixture to lay out as four rows, got %d", len(o.feedbackLayout.rows))
	}
	o.handlePlanDenyKey(parsedKey{kind: rawKeyHome})
	if o.feedbackCursor != 0 {
		t.Fatalf("Home from a wrapped row = %d, want the hard line start 0", o.feedbackCursor)
	}
	o.buildApprovalContent(10)
	if o.feedbackLayout.cursorRow != 0 || o.feedbackLayout.cursorCol != 2 {
		t.Fatalf("Home left the caret at (%d,%d), want the first row at column 2", o.feedbackLayout.cursorRow, o.feedbackLayout.cursorCol)
	}

	o.handlePlanDenyKey(parsedKey{kind: rawKeyEnd})
	if o.feedbackCursor != wrapped {
		t.Fatalf("End from the first row = %d, want the hard line end %d", o.feedbackCursor, wrapped)
	}
	o.buildApprovalContent(10)
	if o.feedbackLayout.cursorRow != 1 || o.feedbackLayout.cursorCol != 5 {
		t.Fatalf("End left the caret at (%d,%d), want the row that ends the hard line at column 5", o.feedbackLayout.cursorRow, o.feedbackLayout.cursorCol)
	}

	// The empty row a trailing newline leaves is its own hard line, so both keys
	// stay put on it.
	o.feedbackCursor = len(o.feedbackBuf)
	o.buildApprovalContent(10)
	for _, key := range []rawKey{rawKeyHome, rawKeyEnd} {
		o.handlePlanDenyKey(parsedKey{kind: key})
		if o.feedbackCursor != len(o.feedbackBuf) {
			t.Fatalf("key %v on the trailing empty line moved the caret to %d", key, o.feedbackCursor)
		}
	}
}

// Esc steps back to the option list while editing feedback, so Ctrl+C is the
// key that abandons the approval, the same as on every other modal row.
func TestApprovalFeedbackCtrlCCancelsTheApproval(t *testing.T) {
	o := &approvalOverlay{planDenyEditing: true, out: io.Discard}
	o.feedbackBuf = []rune("half-written feedback")
	o.feedbackCursor = len(o.feedbackBuf)

	decision, done := o.handlePlanDenyKey(parsedKey{kind: rawKeyCtrlC})
	if !done || !decision.Cancelled {
		t.Fatalf("Ctrl+C while editing feedback = (%#v, done=%v), want a cancelled approval", decision, done)
	}
	if decision.Denied || decision.Approved {
		t.Fatalf("Ctrl+C must not decide the approval: %#v", decision)
	}
}

// The page keys scroll the plan or diff the feedback is about. The paint keeps a
// visible caret on screen by scrolling back to it, so a render that follows a
// scroll must report the caret hidden or the scroll is undone on the spot.
func TestApprovalFeedbackPageKeysScrollTheOverlay(t *testing.T) {
	forcedTermWidth = 80
	forcedTermHeight = 24
	t.Cleanup(func() { forcedTermWidth, forcedTermHeight = 0, 0 })

	var out bytes.Buffer
	r := NewRenderer(&out, &out)
	r.EnableViewportMode()
	t.Cleanup(r.DisableViewportMode)
	o := newApprovalOverlay(context.Background(), io.Discard, newRawSelector(nil, r), editApprovalRequest(t, 57), nil)
	o.resetFocusForOpen()
	o.planDenyEditing = true
	o.feedbackBuf = []rune("why this plan is wrong")
	o.feedbackCursor = len(o.feedbackBuf)
	o.renderViewport()

	r.mu.Lock()
	pinned := r.overlayScrollOffset
	r.mu.Unlock()
	if pinned == 0 {
		t.Fatalf("the fixture must be tall enough to scroll")
	}

	if _, done := o.handlePlanDenyKey(parsedKey{kind: rawKeyPageUp}); done {
		t.Fatal("PageUp ended the overlay")
	}
	o.renderViewport()
	r.mu.Lock()
	scrolled := r.overlayScrollOffset
	cursorRow := r.overlayComposer.cursorRow
	r.mu.Unlock()
	if scrolled >= pinned {
		t.Fatalf("PageUp left the overlay at offset %d, want it above the pinned %d", scrolled, pinned)
	}
	if cursorRow >= 0 {
		t.Fatalf("a render that follows a scroll must hide the caret, got row %d", cursorRow)
	}

	// Typing re-anchors the overlay on the caret and brings it back.
	if _, done := o.handlePlanDenyKey(parsedKey{kind: rawKeyRune, r: '!'}); done {
		t.Fatal("typing ended the overlay")
	}
	o.renderViewport()
	r.mu.Lock()
	cursorRow = r.overlayComposer.cursorRow
	r.mu.Unlock()
	if cursorRow < 0 {
		t.Fatal("typing after a scroll must show the caret again")
	}
}

func TestApprovalFeedbackClickPlacesCaret(t *testing.T) {
	o := &approvalOverlay{planDenyEditing: true, out: io.Discard}
	o.feedbackBuf = []rune("abcdefgh中文")
	o.feedbackCursor = len(o.feedbackBuf)
	o.feedbackFirstRow = 7
	o.feedbackLayout = layoutApprovalFeedback(string(o.feedbackBuf), o.feedbackCursor, 10)
	if len(o.feedbackLayout.lines) != 2 {
		t.Fatalf("expected two feedback rows, got %d", len(o.feedbackLayout.lines))
	}

	o.placeFeedbackCaret(o.feedbackFirstRow, 5)
	if o.feedbackCursor != 3 {
		t.Fatalf("click on the first feedback row should place the caret at 3, got %d", o.feedbackCursor)
	}

	o.feedbackLayout = layoutApprovalFeedback(string(o.feedbackBuf), o.feedbackCursor, 10)
	o.placeFeedbackCaret(o.feedbackFirstRow+1, 5)
	if o.feedbackCursor != 8 {
		t.Fatalf("click inside the wide rune on the continuation row should place the caret at 8, got %d", o.feedbackCursor)
	}

	o.feedbackLayout = layoutApprovalFeedback(string(o.feedbackBuf), o.feedbackCursor, 10)
	o.placeFeedbackCaret(o.feedbackFirstRow+1, 4)
	if o.feedbackCursor != 8 {
		t.Fatalf("both cells of one wide rune must resolve to the same caret, got %d", o.feedbackCursor)
	}

	o.feedbackLayout = layoutApprovalFeedback(string(o.feedbackBuf), o.feedbackCursor, 10)
	o.placeFeedbackCaret(o.feedbackFirstRow+1, 7)
	if o.feedbackCursor != 9 {
		t.Fatalf("a click on the cell after the wide rune should place the caret at 9, got %d", o.feedbackCursor)
	}

	o.feedbackLayout = layoutApprovalFeedback(string(o.feedbackBuf), o.feedbackCursor, 10)
	o.placeFeedbackCaret(o.feedbackFirstRow+1, 9)
	if o.feedbackCursor != 10 {
		t.Fatalf("a click past the last cell must place the caret at the row end 10, got %d", o.feedbackCursor)
	}
}

func TestRawSelectorFeedbackNewlineModeIsIsolated(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		multiline bool
		want      rawKey
	}{
		{name: "normal CR submits", input: "\r", want: rawKeyEnter},
		{name: "normal LF submits", input: "\n", want: rawKeyEnter},
		{name: "normal Alt CR escapes", input: "\x1b\r", want: rawKeyEscape},
		{name: "normal Alt LF escapes", input: "\x1b\n", want: rawKeyEscape},
		{name: "feedback CR submits", input: "\r", multiline: true, want: rawKeyEnter},
		{name: "feedback LF inserts newline", input: "\n", multiline: true, want: rawKeyNewline},
		{name: "feedback Alt CR inserts newline", input: "\x1b\r", multiline: true, want: rawKeyNewline},
		{name: "feedback Alt LF inserts newline", input: "\x1b\n", multiline: true, want: rawKeyNewline},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			readEnd, writeEnd, err := os.Pipe()
			if err != nil {
				t.Fatalf("os.Pipe: %v", err)
			}
			t.Cleanup(func() {
				readEnd.Close()
				writeEnd.Close()
			})
			if _, err := writeEnd.Write([]byte(tt.input)); err != nil {
				t.Fatalf("write input: %v", err)
			}
			if err := writeEnd.Close(); err != nil {
				t.Fatalf("close input: %v", err)
			}

			rs := newRawSelector(readEnd, nil)
			key, err := rs.readKeyWithMode(tt.multiline)
			if err != nil {
				t.Fatalf("readKeyWithMode: %v", err)
			}
			if key.kind != tt.want {
				t.Fatalf("key kind = %v, want %v", key.kind, tt.want)
			}
		})
	}
}

func TestApprovalOverlay_PlanDenyMultilineInput(t *testing.T) {
	req := withServiceDecisions(turn.ToolApprovalRequest{
		ToolName:           "exit_plan_mode",
		ToolInputJSON:      `{}`,
		PermissionToolName: "exit_plan_mode",
	})
	f := newFakeRawSelector(t)
	out := newApprovalPaintWriter()
	overlay := newApprovalOverlay(context.Background(), out, f.rs, req, nil)
	done := make(chan struct{})
	var decision turn.ToolApprovalDecision
	var runErr error
	go func() {
		decision, runErr = overlay.Run()
		close(done)
	}()

	out.waitForPaint(t)
	f.write([]byte{'3', '\r'})
	f.write([]byte("\x1b[200~first\r\nsecond\x1b[201~"))

	select {
	case <-done:
		t.Fatalf("overlay submitted during paste: decision=%#v err=%v", decision, runErr)
	case <-time.After(100 * time.Millisecond):
	}

	f.write([]byte{'\n'})
	f.write([]byte("third\r"))
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("overlay did not submit after explicit Enter")
	}
	if runErr != nil {
		t.Fatalf("overlay run failed: %v", runErr)
	}
	if !decision.Denied || decision.DenyReason != "first\nsecond\nthird" {
		t.Fatalf("unexpected denial decision: %#v", decision)
	}
}

// Every editable field in a modal takes Home and End to the ends of its text.
// The Other answer is one line, so both are absolute.
func TestQuestionOtherHomeEndMoveToTheTextEdges(t *testing.T) {
	o := &questionOverlay{editingOther: true, otherRow: 3, out: io.Discard}
	o.otherBuf = []rune("hello world")
	o.otherCursor = 4

	o.handleOtherEditKey(parsedKey{kind: rawKeyHome})
	if o.otherCursor != 0 {
		t.Fatalf("Home = %d, want 0", o.otherCursor)
	}
	o.handleOtherEditKey(parsedKey{kind: rawKeyEnd})
	if o.otherCursor != len(o.otherBuf) {
		t.Fatalf("End = %d, want %d", o.otherCursor, len(o.otherBuf))
	}
}

// TestQuestionOtherClickPlacesCaret covers the Other field, whose caret is
// drawn as a reverse-video cell inside the text: a click past it must not drift
// by that extra column.
func TestQuestionOtherClickPlacesCaret(t *testing.T) {
	o := &questionOverlay{editingOther: true, otherRow: 3, otherPrefixWidth: 10, out: io.Discard}
	o.otherBuf = []rune("hello world")
	o.otherCursor = 2

	o.placeOtherCaret(3, 10+1)
	if o.otherCursor != 1 {
		t.Fatalf("click before the caret cell should place the caret at 1, got %d", o.otherCursor)
	}

	o.otherCursor = 2
	// "he" + caret cell + "llo world": column 10+2 is the caret cell itself,
	// 10+3 is the "l" right after it.
	o.placeOtherCaret(3, 10+3)
	if o.otherCursor != 2 {
		t.Fatalf("click just past the caret cell should stay at 2, got %d", o.otherCursor)
	}
	o.otherCursor = 2
	o.placeOtherCaret(3, 10+5)
	if o.otherCursor != 4 {
		t.Fatalf("click two cells past the caret cell should place the caret at 4, got %d", o.otherCursor)
	}
	o.placeOtherCaret(4, 12)
	if o.otherCursor != 4 {
		t.Fatalf("a click on another row must not move the Other caret, got %d", o.otherCursor)
	}
}

// writeMouseClick writes the SGR press/release pair a left-button click at the
// given 0-based cell produces.
func writeMouseClick(t *testing.T, w *os.File, col, row int) {
	t.Helper()
	if _, err := fmt.Fprintf(w, "\x1b[<0;%d;%dM\x1b[<0;%d;%dm", col+1, row+1, col+1, row+1); err != nil {
		t.Fatalf("write mouse click: %v", err)
	}
}

// approvalJustification answers "why is this approval on my screen?" for every
// reason the runtime can raise, not for one of them. Rendering only the
// protected-path reason made every later reason - a plan-mode command whose
// effect could not be proven read-only, for instance - show up as the pending
// action's id, which tells the user nothing they can act on.
func TestApprovalJustificationRendersAnyReason(t *testing.T) {
	tests := []struct {
		name string
		req  turn.ToolApprovalRequest
		want string
	}{
		{
			name: "protected path",
			req:  turn.ToolApprovalRequest{ToolInputJSON: `{"file_path":"/x","approval_reason":"protected_path","justification":"Writing /x changes the release skill."}`},
			want: "Writing /x changes the release skill.",
		},
		{
			name: "plan mode unproven command",
			req:  turn.ToolApprovalRequest{ToolInputJSON: `{"command":"python3 script.py","approval_reason":"plan_mode_unproven_command","justification":"Plan mode is active: this command could not be proven read-only."}`},
			want: "Plan mode is active: this command could not be proven read-only.",
		},
		{
			name: "no reason",
			req:  turn.ToolApprovalRequest{ToolInputJSON: `{"command":"ls","approval_reason":"unknown_shell_command"}`},
			want: "",
		},
		{
			name: "no payload",
			req:  turn.ToolApprovalRequest{ToolInputJSON: "{}"},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := approvalJustification(tt.req); got != tt.want {
				t.Fatalf("approvalJustification = %q, want %q", got, tt.want)
			}
		})
	}
}

// The sentence has to reach the screen: an approval raised for a reason the
// overlay has never seen before still shows it.
func TestApprovalOverlayShowsAPlanModeReason(t *testing.T) {
	req := withServiceDecisions(turn.ToolApprovalRequest{
		ToolName:      "write_file",
		ToolInputJSON: `{"file_path":"/src/main.go","resolved_file_path":"/src/main.go","content":"package main\n","force_tool_approval":true,"approval_reason":"plan_mode_unproven_command","justification":"Plan mode is active: this command could not be proven read-only."}`,
	})
	overlay := newApprovalOverlay(context.Background(), io.Discard, nil, req, nil)
	lines, _ := overlay.buildApprovalContent(100)
	plain := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "Reason: Plan mode is active") {
		t.Fatalf("plan-mode reason missing from the overlay:\n%s", plain)
	}
}

// A picker shows every option whole: a row too long for the terminal wraps
// under its own text instead of being cut, the panel keeps its height while
// the filter narrows, and the body scrolls just far enough to show the
// focused option on every line it takes.
func TestPickersShowEveryRowWhole(t *testing.T) {
	long := "Full access: runs anything, anywhere, without asking first"
	st := &selectState{
		options: []string{"Default: asks before leaving the project", "Read only", long, "Auto"},
		label:   "Permissions",
	}
	st.applyFilter()
	st.cursor = 2
	const width, rows = 38, 9
	layout := st.panel().layout(width, rows, 0, true)
	plain := make([]string, len(layout.lines))
	for i, line := range layout.lines {
		plain[i] = stripAnsi(line)
		if w := runewidth.StringWidth(plain[i]); w > width {
			t.Fatalf("row %q is %d columns wide", plain[i], w)
		}
	}
	if len(plain) != rows {
		t.Fatalf("picker takes %d rows, want %d: %q", len(plain), rows, plain)
	}
	joined := strings.Join(plain, "\n")
	if strings.Contains(joined, "...") || strings.Contains(joined, "…") {
		t.Fatalf("a row was cut:\n%s", joined)
	}
	// At this width the focused option takes two rows: the ❯ row and the one
	// hanging under it.
	var focused []string
	for i, line := range plain {
		if strings.HasPrefix(line, selectorCursorGlyph+" ") && i+1 < len(plain) {
			focused = []string{strings.TrimPrefix(line, selectorCursorGlyph+" "), strings.TrimSpace(plain[i+1])}
		}
	}
	if strings.Join(focused, " ") != long {
		t.Fatalf("focused option reads %q, want %q whole:\n%s", strings.Join(focused, " "), long, joined)
	}
	if !strings.Contains(joined, "↑ ") {
		t.Fatalf("the scroll note does not say rows are above:\n%s", joined)
	}

	st.filter = filterField{text: "auto", cursor: 4}
	st.applyFilter()
	if got := st.panel().layout(width, rows, layout.top, true).lines; len(got) != rows {
		t.Fatalf("the panel changed height as the filter narrowed: %q", got)
	}

	p := &richPickerState{items: []SelectItem{
		{Label: "gpt-5", Description: "the default model for everyday work", Category: "OpenAI"},
		{Label: "deepseek-chat", Description: long, Category: "DeepSeek"},
	}}
	p.rebuildSelectable()
	p.cursor = 1
	rich := stripAnsi(strings.Join(p.panel("Model").layout(40, 0, 0, false).lines, "\n"))
	if strings.Contains(rich, "...") || !strings.Contains(strings.Join(strings.Fields(rich), " "), "without asking first") {
		t.Fatalf("rich picker cut a row:\n%s", rich)
	}
	// A label too long for the terminal wraps whole above the filter row.
	long2 := slashPanel{field: &panelField{placeholder: "Type to filter"}}
	b := newPanelBuilder()
	pickerHead(b, "Migrate\nImport another agent's history, memories and skills")
	long2.head = b.head
	head := long2.layout(40, 0, 0, false)
	if head.caretRow != long2.fieldRow(40) || head.caretRow < 2 || !strings.Contains(stripAnsi(head.lines[head.caretRow]), "Type to filter") {
		t.Fatalf("long label head = %q", head.lines)
	}
}

// requirePlainLines lays nothing out itself: it strips a panel's rendered
// lines of their styling and pins them, row for row, to want.
func requirePlainLines(t *testing.T, lines []string, want []string) {
	t.Helper()
	plain := make([]string, len(lines))
	for i, line := range lines {
		plain[i] = stripANSI(line)
	}
	if !slices.Equal(plain, want) {
		t.Fatalf("picker rows:\ngot:  %q\nwant: %q", plain, want)
	}
}

// richPickerPanelLines builds the picker's panel and lays it out, returning
// its still-styled rows.
func richPickerPanelLines(t *testing.T, p *richPickerState, label string, width int) []string {
	t.Helper()
	return p.panel(label).layout(width, 0, 0, false).lines
}

// The rich picker lays each item in two columns: the label in a column as
// wide as the widest label, the description a fixed gap right of it, so every
// description starts — and wraps — in one column of its own.
func TestRichPickerLaysRowsInTwoColumns(t *testing.T) {
	p := &richPickerState{items: []SelectItem{
		{Label: "Default", Description: "Asks before edits and commands outside the workspace."},
		{Label: "Read only", Description: "Never writes."},
	}}
	p.rebuildSelectable()
	p.cursor = 0
	requirePlainLines(t, richPickerPanelLines(t, p, "Permissions\nWhat may run without asking", 80), []string{
		"  Permissions",
		"  What may run without asking",
		"› Type to filter",
		"",
		"❯ Default    Asks before edits and commands outside the workspace.",
		"  Read only  Never writes.",
		"",
		"  ↑/↓ to navigate · Enter to confirm · Esc to cancel",
	})
	for _, line := range richPickerPanelLines(t, p, "Permissions\nWhat may run without asking", 80) {
		if strings.Contains(stripANSI(line), " — ") {
			t.Fatalf("a row still joins name and description with a dash: %q", stripANSI(line))
		}
	}
}

// Groups share one name column: every category's descriptions start in the
// column the widest label across all of them set.
func TestRichPickerAlignsDescriptionsAcrossGroups(t *testing.T) {
	p := &richPickerState{items: []SelectItem{
		{Label: "gpt-5", Description: "the default model", Category: "OpenAI"},
		{Label: "deepseek-chat", Description: "fast and cheap", Category: "DeepSeek"},
	}}
	p.rebuildSelectable()
	requirePlainLines(t, richPickerPanelLines(t, p, "Model", 80), []string{
		"  Model",
		"› Type to filter",
		"",
		"  OpenAI",
		"❯ gpt-5" + strings.Repeat(" ", 10) + "the default model",
		"",
		"  DeepSeek",
		"  deepseek-chat  fast and cheap",
		"",
		"  ↑/↓ to navigate · Enter to confirm · Esc to cancel",
	})
}

// containsRun reports whether needle appears in haystack as one unbroken run.
func containsRun(haystack, needle []string) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if slices.Equal(haystack[i:i+len(needle)], needle) {
			return true
		}
	}
	return false
}

// An item without a description (/resume labels its rows whole) keeps the
// rows it had before the two-column layout: exactly what a plain selectable
// row of the same label produces.
func TestRichPickerWithoutDescriptionsKeepsItsRows(t *testing.T) {
	p := &richPickerState{items: []SelectItem{
		{Label: "just now New conversation"},
		{Label: "2h ago   " + strings.Repeat("a long conversation title ", 4)},
	}}
	p.rebuildSelectable()
	for _, width := range []int{80, 30} {
		var want []string
		for ci, idx := range p.selectable {
			lead := panelIndent
			if ci == p.cursor {
				lead = panelCursor
			}
			for _, row := range (panelLine{lead: lead, text: p.items[idx].Label}).rows(width) {
				want = append(want, stripANSI(row))
			}
		}
		plain := make([]string, 0, len(want))
		for _, line := range richPickerPanelLines(t, p, "Resume", width) {
			plain = append(plain, stripANSI(line))
		}
		if !containsRun(plain, want) {
			t.Fatalf("width %d: description-less rows changed:\ngot:  %q\nwant: %q", width, plain, want)
		}
	}
}

// The column holds still while the filter narrows: its width follows every
// item the picker holds, not only the ones matching right now.
func TestRichPickerColumnHoldsStillWhileFiltering(t *testing.T) {
	p := &richPickerState{items: []SelectItem{
		{Label: "Default", Description: "Asks before edits and commands outside the workspace."},
		{Label: "Read only", Description: "Never writes."},
	}}
	p.filter = filterField{text: "read", cursor: 4}
	p.rebuildSelectable()
	for _, line := range richPickerPanelLines(t, p, "Permissions", 80) {
		if stripANSI(line) == "❯ Read only  Never writes." {
			return
		}
	}
	t.Fatalf("the filtered row lost the column shared with every item:\n%q", richPickerPanelLines(t, p, "Permissions", 80))
}

// Whatever the width, every row fits it whole: nothing is cut, nothing is
// ellipsized, the description wraps inside its column.
func TestRichPickerRowsFitAtEveryWidth(t *testing.T) {
	pickers := []*richPickerState{
		{items: []SelectItem{
			{Label: "Default", Description: "Asks before edits and commands outside the workspace."},
			{Label: "Read only", Description: "Never writes."},
		}},
		{items: []SelectItem{
			{Label: "gpt-5", Description: "the default model", Category: "OpenAI"},
			{Label: "deepseek-chat", Description: "fast and cheap", Category: "DeepSeek"},
		}},
	}
	for _, p := range pickers {
		p.rebuildSelectable()
		for _, width := range []int{120, 80, 50, 30} {
			lines := richPickerPanelLines(t, p, "Model", width)
			assertRowsFit(t, lines, width)
			for _, line := range lines {
				if plain := stripANSI(line); strings.Contains(plain, "…") || strings.Contains(plain, "...") {
					t.Fatalf("width %d cut a row: %q", width, plain)
				}
			}
		}
	}
}
