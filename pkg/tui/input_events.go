// Terminal input: events, paste bursts, history, and the composer.
package tui

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
	"github.com/muesli/termenv"
	"golang.org/x/term"
)

type inputEventKind int

const (
	inputEventLine inputEventKind = iota
	inputEventPaste
	inputEventDraft
	inputEventHotkey
	inputEventMouseClick
	inputEventMouseWheel
	inputEventMouseMove
	inputEventMousePress
	inputEventMouseDrag
	inputEventMouseRelease
	inputEventFocusGained
	inputEventFocusLost
	inputEventDone
	// inputEventWake is not input: the idle loop's wait returns it when a
	// notification left the loop work of its own (a continuation whose wait
	// just ended), so the loop runs it without waiting for a key.
	inputEventWake
)

type inputHotkey string

const (
	hotkeyInterrupt          inputHotkey = "interrupt"
	hotkeyEscapeInterrupt    inputHotkey = "escape_interrupt"
	hotkeyClearInput         inputHotkey = "clear_input"
	hotkeyPasteImage         inputHotkey = "paste_image"
	hotkeyBackground         inputHotkey = "background"
	hotkeyQueueFollowUp      inputHotkey = "queue_follow_up"
	hotkeyEditLastQueued     inputHotkey = "edit_last_queued"
	hotkeyOverlayUp          inputHotkey = "overlay_up"
	hotkeyOverlayDown        inputHotkey = "overlay_down"
	hotkeyOverlayAccept      inputHotkey = "overlay_accept"
	hotkeyTogglePlanMode     inputHotkey = "toggle_plan_mode"
	hotkeyPanelLeft          inputHotkey = "panel_left"
	hotkeyPanelRight         inputHotkey = "panel_right"
	hotkeyPanelTab           inputHotkey = "panel_tab"
	hotkeyPanelPageUp        inputHotkey = "panel_page_up"
	hotkeyPanelPageDown      inputHotkey = "panel_page_down"
	hotkeyPanelAccept        inputHotkey = "panel_accept"
	hotkeyAgentControlPrefix inputHotkey = "agent_control_prefix"
	hotkeyAgentStopAll       inputHotkey = "agent_stop_all"
	hotkeyRosterStop         inputHotkey = "roster_stop"
	hotkeyJumpToBottom       inputHotkey = "jump_to_bottom"
	hotkeyRedraw             inputHotkey = "redraw"
)

// slashOverlayActive gates whether arrow keys route to slash-overlay nav
// (Up/Down mutate SelectedIdx) or to input-history nav. Set by run.go event
// loop when SlashOverlay nil-state changes. Atomic for race-free reader access.
var slashOverlayActive atomic.Bool

// mentionOverlayActive gates whether Up/Down route to overlay nav and Tab
// routes to overlay-accept instead of queue-follow-up.
var mentionOverlayActive atomic.Bool

// agentRosterActive reports whether the agent roster panel is currently
// visible (a subagent is running). Gates whether a bare Down at an empty
// history browse enters roster focus. Set by run.go's event loop.
var agentRosterActive atomic.Bool

// agentRosterFocused reports whether keyboard focus has moved onto the
// roster panel (via a prior Down). While true, both arrows route to roster
// navigation instead of input-history nav. Set by run.go's event loop.
var agentRosterFocused atomic.Bool

// agentRosterStopArmed reports whether a bare 'x' should stop the selected
// roster subagent instead of typing into the composer. True when the roster
// is focused and the selected row is a subagent. The raw reader additionally
// requires the composer line to be empty before intercepting, so an 'x'
// typed mid-sentence (e.g. "fix") still inserts. Set by run.go's event loop
// alongside agentRosterFocused.
var agentRosterStopArmed atomic.Bool

// Deprecated compatibility state for older focused tests. Queue recall does not
// consult this flag; bare Up remains history navigation.
var editableQueuedInputActive atomic.Bool

// composerContentWidth is the current composer content width in display
// columns (termWidth minus prefix and safety margins). Updated by the
// renderer whenever the composer is rendered. The raw input reader uses
// it to compute visual line breaks for cursorUp/cursorDown when handling
// soft-wrapped lines. Atomic for race-free reader access.
var composerContentWidth atomic.Int32

type inputEvent struct {
	kind       inputEventKind
	line       string
	draft      string
	cursor     int
	paste      string
	hotkey     inputHotkey
	mouseCol   int
	mouseRow   int
	wheelDelta int
	err        error
	// historyNavigation distinguishes an arrow-key recall from an ordinary
	// edit. The structured entry restores folded paste bodies and image paths
	// that are absent from the visible placeholder text.
	historyNavigation    bool
	historyEntryRecorded bool
	historyIndex         int
	historyEntry         rawInputHistoryEntry
}

type rawInputController interface {
	EnterRaw() error
	Restore() error
}

// Keep raw reads byte-sized so a leading slash hotkey cannot drain the first
// selector filter key from the terminal before the raw selector takes over stdin.
const rawInputReadBufferSize = 1

// interactiveInputSeed carries a replacement line buffer for the raw reader
// together with the cursor position it should adopt. seedCursorEnd (-1) is a
// sentinel requesting the cursor be placed at the end of the text; any other
// value is clamped to [0, len(runes)]. Carrying the cursor (instead of always
// resetting it to the end) is what lets a paste land its cursor at the end of
// the inserted text rather than the end of the whole buffer.
type interactiveInputSeed struct {
	text   string
	cursor int
}

const seedCursorEnd = -1

var (
	interactiveInputPaused    atomic.Bool
	interactiveInputPausedAck atomic.Bool
	interactiveInputSeedCh    = make(chan interactiveInputSeed, 1)

	// interactiveInputCaretCh carries a caret position requested by a mouse
	// click on the composer card. The raw reader owns the line buffer, so a
	// click cannot move the caret directly: the renderer resolves the clicked
	// cell to a rune offset in the text it painted and posts it here, and the
	// reader adopts it against its own buffer. Capacity 1 with replacement —
	// only the newest click matters, and a stale one must never move the caret
	// after a later click already did.
	interactiveInputCaretCh = make(chan int, 1)

	// interactiveInputPauseDepth is the nestable acquire/release count for the
	// background raw reader pause. Every modal that needs exclusive stdin
	// access (selector.Select, approvalOverlay.Run, questionOverlay.Run via
	// rawSelector.acquireComposerInput) acquires the pause and releases it on
	// exit. The reader is only actually paused on the first acquire (depth
	// 0 -> 1) and only resumed on the last release (depth 1 -> 0), so nested
	// modals - e.g. a /model selector opened during an active run while the
	// agent requests a tool approval - do not prematurely resume the reader
	// when the inner modal closes. Without this the inner release() resumed
	// the reader while the outer selector still owned stdin, racing the
	// background reader against the selector's readKey() and freezing the TUI
	// (a split escape sequence left readKey blocked forever).
	interactiveInputPauseDepth atomic.Int32
)

// pendingTerminalInput holds bytes that were taken off the terminal before the
// input pipeline started — the startup theme probe reads one byte to find out
// whether the terminal is answering its query, and that byte is the user's when
// it is not an answer. A tty has no pushback, so the reader hands it here and
// the pipeline delivers it as its first chunk. Anything else would silently
// drop a keystroke.
var (
	pendingTerminalInputMu sync.Mutex
	pendingTerminalInput   []byte
)

// keepTerminalInputForInputPipeline stores bytes read off the terminal that
// belong to the user rather than to the reader that took them.
func keepTerminalInputForInputPipeline(b []byte) {
	if len(b) == 0 {
		return
	}
	pendingTerminalInputMu.Lock()
	defer pendingTerminalInputMu.Unlock()
	pendingTerminalInput = append(pendingTerminalInput, b...)
}

// takePendingTerminalInput removes and returns whatever is waiting, so it is
// delivered exactly once.
func takePendingTerminalInput() []byte {
	pendingTerminalInputMu.Lock()
	defer pendingTerminalInputMu.Unlock()
	b := pendingTerminalInput
	pendingTerminalInput = nil
	return b
}

// stdinReadMu serialises reads from the terminal fd so the modal overlay's
// direct readKey() path and the background reader goroutine never race on
// the same fd. Without this the two goroutines can both block on Read(),
// and when a byte arrives the kernel delivers it to only one of them — the
// loser stays blocked, the winner may be the wrong goroutine, and the
// pause/resume handshake can deadlock because the inner reader goroutine
// never reaches the pause check (it is stuck in Read).
var stdinReadMu sync.Mutex

func pauseInteractiveInputRead() {
	interactiveInputPaused.Store(true)
}

// acquireInteractiveInputPause increments the nestable pause depth and pauses
// the background reader on the outermost acquire (depth 0 -> 1). Callers MUST
// pair it with releaseInteractiveInputPause. Used by acquireComposerInput so
// nested modals don't resume the reader prematurely.
func acquireInteractiveInputPause() {
	if interactiveInputPauseDepth.Add(1) == 1 {
		pauseInteractiveInputRead()
	}
}

// releaseInteractiveInputPause decrements the nestable pause depth and resumes
// the background reader only on the innermost release (depth 1 -> 0). This is
// the pair of acquireInteractiveInputPause; it must be used by every modal's
// release path so that a nested modal closing does not resume the reader while
// an outer modal still owns stdin.
func releaseInteractiveInputPause() {
	if interactiveInputPauseDepth.Add(-1) <= 0 {
		interactiveInputPauseDepth.Store(0)
		resumeInteractiveInputRead()
	}
}

// pauseInteractiveInputReadAndWait pauses the raw input reader and waits up to
// timeout for it to acknowledge by reaching its pause sleep. Returns true if
// the reader acknowledged (or no active reader is present and timeout elapses
// — callers treat that the same as a successful pause since there is nothing
// to race with).
func pauseInteractiveInputReadAndWait(timeout time.Duration) bool {
	pauseInteractiveInputRead()
	deadline := time.Now().Add(timeout)
	for !interactiveInputPausedAck.Load() {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(time.Millisecond)
	}
	return true
}

func resumeInteractiveInputRead() {
	interactiveInputPaused.Store(false)
}

// acknowledgePauseIfRequested checks the pause flag and, if set, stores the
// pause acknowledgement and blocks until the pause is cleared. Returns false
// if ctx was cancelled during the wait. This lets the inner reader goroutine
// respond to pause requests even while it is blocked on readCh <- or <-ackCh
// (where it would otherwise never reach the top-of-loop pause check).
func acknowledgePauseIfRequested(ctx context.Context) bool {
	if !interactiveInputPaused.Load() {
		return true
	}
	for interactiveInputPaused.Load() {
		interactiveInputPausedAck.Store(true)
		select {
		case <-ctx.Done():
			interactiveInputPausedAck.Store(false)
			return false
		case <-time.After(2 * time.Millisecond):
		}
	}
	interactiveInputPausedAck.Store(false)
	return true
}

// requestComposerCaret asks the raw input reader to move the composer caret to
// the given rune offset in its buffer. The offset is derived from the text the
// renderer painted, so the reader clamps it to the buffer it holds now.
func requestComposerCaret(cursor int) {
	for {
		select {
		case <-interactiveInputCaretCh:
		default:
			select {
			case interactiveInputCaretCh <- cursor:
			default:
			}
			return
		}
	}
}

func seedInteractiveInput(text string, cursor int) {
	seed := interactiveInputSeed{text: text, cursor: cursor}
	for {
		select {
		case <-interactiveInputSeedCh:
		default:
			select {
			case interactiveInputSeedCh <- seed:
			default:
			}
			return
		}
	}
}

func startsEscapeSequence(buf []byte) bool {
	return len(buf) >= 2 && (buf[1] == '[' || buf[1] == 'O')
}

func escapeSequenceComplete(buf []byte) bool {
	if !startsEscapeSequence(buf) || len(buf) < 3 {
		return false
	}
	last := buf[len(buf)-1]
	return (last >= 'A' && last <= 'Z') || last == '~'
}

func isShiftArrowLeftSequence(seq []byte) bool {
	return bytes.Equal(seq, []byte{0x1b, '[', '1', ';', '2', 'D'})
}

// isCtrlEndSequence accepts the common xterm/Kitty modified End encodings.
// Terminals vary between the cursor-key final byte form and the tilde form.
func isCtrlEndSequence(seq []byte) bool {
	return bytes.Equal(seq, []byte{0x1b, '[', '1', ';', '5', 'F'}) ||
		bytes.Equal(seq, []byte{0x1b, '[', '1', ';', '5', '~'}) ||
		bytes.Equal(seq, []byte{0x1b, '[', '4', ';', '5', '~'})
}

func clearRawInputLine(out io.Writer) {
}

func historyBrowseDirection(seq []byte) int {
	if !escapeSequenceComplete(seq) {
		return 0
	}
	switch seq[len(seq)-1] {
	case 'A':
		return -1
	case 'B':
		return 1
	default:
		return 0
	}
}

func cloneRunes(src []rune) []rune {
	if len(src) == 0 {
		return nil
	}
	dst := make([]rune, len(src))
	copy(dst, src)
	return dst
}

func composerCursorClamp(text string, cursor int) int {
	return composerCursorClampRunes([]rune(text), cursor)
}

func composerCursorClampRunes(line []rune, cursor int) int {
	if cursor < 0 {
		return 0
	}
	if cursor > len(line) {
		return len(line)
	}
	return cursor
}

func composerCursorPrefixRunes(line []rune, cursor int) []rune {
	return line[:composerCursorClampRunes(line, cursor)]
}

func composerTextRunesPrefix(text string, cursor int) string {
	rs := []rune(text)
	return string(composerCursorPrefixRunes(rs, cursor))
}

func browseRawInputHistory(history []string, current []rune, draft []rune, index int, direction int) ([]rune, []rune, int, bool) {
	if len(history) == 0 {
		return current, draft, index, false
	}
	switch direction {
	case -1:
		if index < 0 {
			draft = cloneRunes(current)
			index = len(history) - 1
		} else if index > 0 {
			index--
		} else {
			return current, draft, index, false
		}
		return []rune(history[index]), draft, index, true
	case 1:
		if index < 0 {
			return current, draft, index, false
		}
		if index < len(history)-1 {
			index++
			return []rune(history[index]), draft, index, true
		}
		return cloneRunes(draft), nil, -1, true
	default:
		return current, draft, index, false
	}
}

// lineStart returns the index (in runes) of the first character of the visual
// line that contains the given cursor position.
func lineStart(line []rune, cursor int) int {
	cursor = composerCursorClampRunes(line, cursor)
	if cursor == 0 {
		return 0
	}
	for i := cursor - 1; i >= 0; i-- {
		if line[i] == '\n' {
			return i + 1
		}
	}
	return 0
}

// visualLineStart returns the rune index of the start of the visual line
// containing the given cursor position, accounting for both hard newlines and
// soft wrapping at the given display width. width <= 0 means no soft wrapping
// (only hard newlines are treated as line breaks).
func visualLineStart(line []rune, cursor int, width int) int {
	cursor = composerCursorClampRunes(line, cursor)
	if cursor == 0 {
		return 0
	}
	if width <= 0 {
		return lineStart(line, cursor)
	}
	// Find the hard line start (last \n before cursor).
	hardStart := lineStart(line, cursor)
	// Scan from hardStart to cursor, tracking visual width to find the
	// soft-wrap boundary within this hard line that contains the cursor.
	vs := hardStart
	visWidth := 0
	for i := hardStart; i < cursor; i++ {
		r := line[i]
		if r == '\n' {
			vs = i + 1
			visWidth = 0
			continue
		}
		rw := runewidth.RuneWidth(r)
		if visWidth+rw > width && visWidth > 0 {
			vs = i
			visWidth = rw
		} else {
			visWidth += rw
		}
	}
	return vs
}

// visualLineEnd returns the exclusive end (rune index just after the last
// character) of the visual line that starts at the given start position.
// width <= 0 means no soft wrapping.
func visualLineEnd(line []rune, start int, width int) int {
	if start >= len(line) {
		return len(line)
	}
	if width <= 0 {
		for i := start; i < len(line); i++ {
			if line[i] == '\n' {
				return i
			}
		}
		return len(line)
	}
	visWidth := 0
	for i := start; i < len(line); i++ {
		r := line[i]
		if r == '\n' {
			return i
		}
		rw := runewidth.RuneWidth(r)
		if visWidth+rw > width && visWidth > 0 {
			return i
		}
		visWidth += rw
	}
	return len(line)
}

// visualColumn returns the display width from the start of a visual line to
// the given cursor position within that same visual line.
func visualColumn(line []rune, start, cursor int) int {
	col := 0
	for i := start; i < cursor && i < len(line); i++ {
		col += runewidth.RuneWidth(line[i])
	}
	return col
}

// visualLinePosition returns the rune index within [start, end) that
// corresponds to the given display column, clamped to the visual line length.
func visualLinePosition(line []rune, start, end, col int) int {
	visWidth := 0
	for i := start; i < end; i++ {
		rw := runewidth.RuneWidth(line[i])
		if visWidth+rw > col {
			return i
		}
		visWidth += rw
	}
	return end
}

// cursorUp moves the cursor up one visual line within multi-line text.
// width is the composer content width in display columns; when > 0, soft
// wrapping is considered. Returns the new cursor position and true if
// movement happened (false if the cursor is already on the first line).
func cursorUp(line []rune, cursor int, width int) (int, bool) {
	cursor = composerCursorClampRunes(line, cursor)
	vs := visualLineStart(line, cursor, width)
	if vs == 0 {
		return cursor, false
	}
	col := visualColumn(line, vs, cursor)
	prevVS := visualLineStart(line, vs-1, width)
	prevEnd := visualLineEnd(line, prevVS, width)
	return visualLinePosition(line, prevVS, prevEnd, col), true
}

// cursorDown moves the cursor down one visual line within multi-line text.
// width is the composer content width in display columns; when > 0, soft
// wrapping is considered. Returns the new cursor position and true if
// movement happened (false if the cursor is already on the last line).
func cursorDown(line []rune, cursor int, width int) (int, bool) {
	cursor = composerCursorClampRunes(line, cursor)
	vs := visualLineStart(line, cursor, width)
	ve := visualLineEnd(line, vs, width)
	if ve >= len(line) {
		return cursor, false
	}
	// Determine the start of the next visual line: skip the \n for hard
	// newlines, or start at ve for soft wrap.
	nextVS := ve
	if line[ve] == '\n' {
		nextVS = ve + 1
	}
	if nextVS >= len(line) {
		return cursor, false
	}
	col := visualColumn(line, vs, cursor)
	nextEnd := visualLineEnd(line, nextVS, width)
	return visualLinePosition(line, nextVS, nextEnd, col), true
}

func redrawRawInputBuffer(out io.Writer, previous []rune, previousCursor int, next []rune, nextCursor int) {
}

func insertRunesAtCursor(line []rune, cursor int, insert []rune) ([]rune, int) {
	cursor = composerCursorClampRunes(line, cursor)
	if len(insert) == 0 {
		return line, cursor
	}
	out := make([]rune, 0, len(line)+len(insert))
	out = append(out, line[:cursor]...)
	out = append(out, insert...)
	out = append(out, line[cursor:]...)
	return out, cursor + len(insert)
}

func deleteRuneBeforeCursor(line []rune, cursor int) ([]rune, int, bool) {
	cursor = composerCursorClampRunes(line, cursor)
	if cursor == 0 {
		return line, cursor, false
	}
	out := make([]rune, 0, len(line)-1)
	out = append(out, line[:cursor-1]...)
	out = append(out, line[cursor:]...)
	return out, cursor - 1, true
}

func readInputEvents(ctx context.Context, in io.Reader, out io.Writer, tty rawInputController, historyStore rawInputHistoryStore) <-chan inputEvent {
	if f, ok := in.(*os.File); ok {
		if term.IsTerminal(int(f.Fd())) {
			return readRawInputEvents(ctx, f, out, tty, historyStore)
		}
	}
	ch := make(chan inputEvent)
	go func() {
		defer close(ch)
		<-ctx.Done()
	}()
	return ch
}

// Bracketed paste mode: with ?2004h the terminal wraps pastes in
// ESC[200~ ... ESC[201~ so they arrive as one atomic event instead of a raw
// char flood. Without it, large pastes reach the burst heuristic in arbitrary
// chunks and can leak into the line buffer before folding kicks in. Enabled
// once at interactive startup in Run(); the reader below already parses the
// 200~/201~ markers.
const (
	enableBracketedPasteSeq  = "\x1b[?2004h"
	disableBracketedPasteSeq = "\x1b[?2004l"
	// Focus reporting makes the terminal send CSI I / CSI O when its window or
	// tab gains or loses focus. Completion notifications use this to avoid
	// ringing while the user is already looking at the conversation.
	enableFocusReportingSeq  = "\x1b[?1004h"
	disableFocusReportingSeq = "\x1b[?1004l"
)

// SGR mouse reporting. ?1002h reports button press/release, drag, and wheel
// events without emitting no-button hover motion; ?1006h switches to SGR
// encoding so coordinates beyond column/row 223 are not truncated (the legacy
// X10 encoding packs them into single bytes). Reports arrive as ESC [ < Cb ; Cx
// ; Cy (M|m): M=press/motion, m=release, Cx/Cy 1-based. Avoiding ?1003h is
// important because its all-motion reports can be copied or pasted as text by
// terminal/multiplexer edge cases. Capturing the mouse means the terminal's own
// text selection now needs Shift held (vim/tmux/htop behave the same — a
// protocol limitation).
const (
	enableMouseSeq  = "\x1b[?1003l\x1b[?1002h\x1b[?1006h"
	disableMouseSeq = "\x1b[?1006l\x1b[?1002l\x1b[?1003l"
)

// sgrMouseEvent is a decoded SGR mouse report.
type sgrMouseEvent struct {
	button  int  // Cb low bits: 0=left,1=middle,2=right; 64=wheel-up,65=wheel-down
	col     int  // 0-based column (decoded from 1-based Cx)
	row     int  // 0-based row (decoded from 1-based Cy)
	press   bool // true for 'M' (press), false for 'm' (release)
	wheel   bool // true when the report is a scroll-wheel notch
	wheelUp bool // wheel direction when wheel is true
}

// isSGRMousePrefix reports whether seq is the start of (or a complete) SGR
// mouse report: ESC [ < … . A complete report ends in 'M' or 'm'.
func isSGRMousePrefix(seq []byte) bool {
	return len(seq) >= 3 && seq[0] == 0x1b && seq[1] == '[' && seq[2] == '<'
}

// sgrMouseComplete reports whether seq is a fully-terminated SGR mouse report.
func sgrMouseComplete(seq []byte) bool {
	if !isSGRMousePrefix(seq) {
		return false
	}
	last := seq[len(seq)-1]
	return last == 'M' || last == 'm'
}

// parseSGRMouse decodes a complete ESC [ < Cb ; Cx ; Cy (M|m) report. Returns
// false on any malformed field rather than guessing, so a corrupt sequence is
// dropped instead of producing a bogus click.
func parseSGRMouse(seq []byte) (sgrMouseEvent, bool) {
	if !sgrMouseComplete(seq) {
		return sgrMouseEvent{}, false
	}
	term := seq[len(seq)-1]
	body := string(seq[3 : len(seq)-1]) // between "<" and terminator
	fields := strings.Split(body, ";")
	if len(fields) != 3 {
		return sgrMouseEvent{}, false
	}
	cb, ok1 := atoiStrict(fields[0])
	cx, ok2 := atoiStrict(fields[1])
	cy, ok3 := atoiStrict(fields[2])
	if !ok1 || !ok2 || !ok3 {
		return sgrMouseEvent{}, false
	}
	ev := sgrMouseEvent{
		button: cb,
		col:    cx - 1,
		row:    cy - 1,
		press:  term == 'M',
	}
	if ev.col < 0 {
		ev.col = 0
	}
	if ev.row < 0 {
		ev.row = 0
	}
	// Wheel notches set bit 6 (64) of Cb: 64=up, 65=down. They report as a
	// press ('M') with no matching release.
	if cb&0x40 != 0 {
		ev.wheel = true
		ev.wheelUp = (cb & 0x01) == 0
	}
	return ev, true
}

// atoiStrict parses a non-negative base-10 integer, rejecting empty input or
// any non-digit byte. Used for SGR mouse fields where a loose parse could mask
// a desynced escape buffer.
func atoiStrict(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}

// mouseEventFromSGR converts a decoded SGR report into the inputEvent the run
// loop consumes, or false when the report should be ignored (button release,
// non-left button press, or a wheel notch is mapped to a wheel event instead).
// wheelScrollLines is how many rows one wheel notch scrolls.
//
// Motion events carry Cb >= 32 (bit 5 set). 32=motion+left,
// 33=motion+middle, 34=motion+right, and 35=motion+no-button. The reader
// accepts left drags, but deliberately drops no-button motion from stale
// ?1003h terminal state rather than reintroducing hover-driven input traffic.
const wheelScrollLines = 3

func mouseEventFromSGR(ev sgrMouseEvent) (inputEvent, bool) {
	if ev.wheel {
		// One wheel notch is one PRESS report (Cb 64/65, terminator 'M')
		// without the motion bit. Terminals that answer a wheel gesture with
		// release reports (same Cb, terminator 'm') or wheel-bit motion
		// samples (Cb 96/97 = 64+32) must not scroll again for each of
		// those: this branch used to turn every report carrying the wheel
		// bit into a 3-row scroll, so one physical notch scrolled 2x
		// (press+release) or Nx (press+motion stream+release).
		if !ev.press || ev.button&0x20 != 0 {
			return inputEvent{}, false
		}
		delta := wheelScrollLines
		if ev.wheelUp {
			delta = -wheelScrollLines
		}
		// Carry the pointer position so ViewportScrollAt can route the
		// notch to the overlay composer (when the cursor is over it) or
		// the transcript. Without col/row the receiver sees (0,0) and
		// always scrolls the transcript, making tall overlays unscrollable.
		return inputEvent{kind: inputEventMouseWheel, wheelDelta: delta, mouseCol: ev.col, mouseRow: ev.row}, true
	}
	// Motion events set bit 5 (32) in Cb. Only a left-button drag affects
	// selection. In particular, discard Cb=35 (motion with no button), which an
	// old ?1003h terminal state can still emit after startup.
	if ev.button >= 32 && ev.press {
		if ev.button&0x03 == 0 {
			return inputEvent{kind: inputEventMouseDrag, mouseCol: ev.col, mouseRow: ev.row}, true
		}
		return inputEvent{}, false
	}
	// Left button press: start selection.
	if ev.press && (ev.button&0x03) == 0 {
		return inputEvent{kind: inputEventMousePress, mouseCol: ev.col, mouseRow: ev.row}, true
	}
	// Left button release: end selection. If no drag happened the caller
	// can still handle this as a click toggle.
	if !ev.press && (ev.button&0x03) == 0 {
		return inputEvent{kind: inputEventMouseRelease, mouseCol: ev.col, mouseRow: ev.row}, true
	}
	// Other button presses/releases are dropped.
	return inputEvent{}, false
}

// The composer card is drawn on a fixed dark-gray background (color 238). The
// hardware text cursor rests inside that card at idle, so a terminal's default
// cursor color — often light/white on dark themes — blends into the gray and is
// invisible (the user can't see where they're typing). Pin the cursor to the
// accent blue (matching the "›" prompt marker) while the TUI owns the screen so
// it contrasts the card, and reset it to the terminal default on teardown.
// OSC 12 sets the cursor color; OSC 112 resets it.
// Use the String Terminator (ESC \) instead of BEL so Terminal.app does not
// treat the terminator as an audible/visual bell.
const (
	setComposerCursorColorSeq   = "\x1b]12;#0087ff\x1b\\"
	resetComposerCursorColorSeq = "\x1b]112\x1b\\"
)

func readRawInputEvents(ctx context.Context, in *os.File, out io.Writer, tty rawInputController, historyStore rawInputHistoryStore) <-chan inputEvent {
	// Small buffer so the middle goroutine never blocks on ch<- during brief
	// windows when the main loop is busy rendering or draining notifications.
	// Without this a single blocked send cascades: the inner goroutine waits
	// for ackCh, and the whole input pipeline freezes.
	ch := make(chan inputEvent, 8)
	go func() {
		defer close(ch)
		if tty == nil {
			ch <- inputEvent{kind: inputEventDone, err: errors.New("raw terminal session unavailable")}
			return
		}
		if err := tty.EnterRaw(); err != nil {
			ch <- inputEvent{kind: inputEventDone, err: err}
			return
		}
		defer tty.Restore()

		var line []rune
		cursor := 0
		var pendingUTF8 []byte
		var pendingEscape []byte
		var pendingEscapeLastActivity time.Time
		var pasteBuf bytes.Buffer
		collectingPaste := false
		pasteBurst := newPasteBurstState()
		historyEntries := loadRawInputHistoryEntries(historyStore)
		history := rawInputHistoryEntryTexts(historyEntries)
		historyIndex := -1
		var historyDraft []rune

		type rawRead struct {
			data []byte
			err  error
		}
		readCh := make(chan rawRead)
		ackCh := make(chan struct{}, 1)
		// Keep the reader blocked until the current chunk is fully handled.
		// Slash autocomplete relies on this to avoid pre-reading the next key.
		go func() {
			buf := make([]byte, rawInputReadBufferSize)
			fd := int(in.Fd())
			const pollIntervalMs = 20
			// Bytes a startup reader had to take off the terminal before this
			// pipeline existed are the first thing it delivers. They go through
			// the same chunk path as a real read, so the pause handshake and
			// the ack that keeps the reader one chunk behind still hold.
			carried := takePendingTerminalInput()
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				for interactiveInputPaused.Load() {
					interactiveInputPausedAck.Store(true)
					select {
					case <-ctx.Done():
						interactiveInputPausedAck.Store(false)
						return
					case <-time.After(2 * time.Millisecond):
					}
				}
				interactiveInputPausedAck.Store(false)
				var n int
				var err error
				if len(carried) > 0 {
					n = copy(buf, carried)
					carried = carried[n:]
				} else {
					readable, perr := pollReadableInputFD(fd, pollIntervalMs)
					if perr != nil {
						select {
						case <-ctx.Done():
							return
						case readCh <- rawRead{err: perr}:
						}
						return
					}
					if !readable {
						continue
					}
					stdinReadMu.Lock()
					n, err = in.Read(buf)
					stdinReadMu.Unlock()
				}
				if n > 0 {
					chunk := append([]byte(nil), buf[:n]...)
					// Send the chunk to the middle goroutine, but also check the
					// pause flag periodically. If a modal overlay requests pause
					// while the middle goroutine is blocked (e.g. on ch <- to a
					// busy main event loop), the inner goroutine would otherwise
					// never reach the top-of-loop pause check and
					// pauseInteractiveInputReadAndWait would time out, letting
					// the overlay's readKey race with the inner goroutine for
					// stdin bytes.
					for {
						select {
						case <-ctx.Done():
							return
						case readCh <- rawRead{data: chunk}:
							goto ackWait
						case <-time.After(2 * time.Millisecond):
							if !acknowledgePauseIfRequested(ctx) {
								return
							}
						}
					}
				ackWait:
					for {
						select {
						case <-ctx.Done():
							return
						case <-ackCh:
							goto nextIter
						case <-time.After(2 * time.Millisecond):
							if !acknowledgePauseIfRequested(ctx) {
								return
							}
						}
					}
				nextIter:
					continue
				}
				select {
				case <-ctx.Done():
					return
				case readCh <- rawRead{err: err}:
				}
				return
			}
		}()
		flushBurst := func(now time.Time) {
			switch flush := pasteBurst.flushIfDue(now); flush.kind {
			case pasteBurstResultPaste:
				content := sanitizeTerminalInputText(flush.text)
				if content == "" {
					return
				}
				ch <- inputEvent{kind: inputEventPaste, paste: content}
				line, cursor = insertRunesAtCursor(line, cursor, []rune(content))
			case pasteBurstResultTyped:
				content := sanitizeTerminalInputText(flush.text)
				if content == "" {
					return
				}
				previous := cloneRunes(line)
				previousCursor := cursor
				line, cursor = insertRunesAtCursor(line, cursor, []rune(content))
				redrawRawInputBuffer(out, previous, previousCursor, line, cursor)
				ch <- inputEvent{kind: inputEventDraft, draft: string(line), cursor: cursor}
			}
		}
		flushBeforeHotkey := func() {
			if pasted := pasteBurst.flushBeforeModifiedInput(); pasted != "" {
				previous := cloneRunes(line)
				previousCursor := cursor
				line, cursor = insertRunesAtCursor(line, cursor, []rune(pasted))
				if out != nil {
					redrawRawInputBuffer(out, previous, previousCursor, line, cursor)
				}
			}
			pasteBurst.clearWindowAfterNonChar()
		}
		applyCaret := func(requested int) {
			// A pending paste burst belongs at the caret the user was typing
			// at, not at the one they just clicked, so it is committed first.
			if pasted := pasteBurst.flushBeforeModifiedInput(); pasted != "" {
				previous := cloneRunes(line)
				previousCursor := cursor
				line, cursor = insertRunesAtCursor(line, cursor, []rune(pasted))
				if out != nil {
					redrawRawInputBuffer(out, previous, previousCursor, line, cursor)
				}
			}
			pasteBurst.clearWindowAfterNonChar()
			next := composerCursorClampRunes(line, requested)
			if next == cursor {
				return
			}
			cursor = next
			// Moving the caret is not an edit: the history browse position and
			// its stashed draft stay as they are, so Up still steps to the
			// entry before the one being read.
			ch <- inputEvent{kind: inputEventDraft, draft: string(line), cursor: cursor}
		}
		applySeed := func(seed interactiveInputSeed) {
			if pasted := pasteBurst.flushBeforeModifiedInput(); pasted != "" {
				previous := cloneRunes(line)
				previousCursor := cursor
				line, cursor = insertRunesAtCursor(line, cursor, []rune(pasted))
				if out != nil {
					redrawRawInputBuffer(out, previous, previousCursor, line, cursor)
				}
			}
			pasteBurst.clear()
			pendingUTF8 = pendingUTF8[:0]
			pendingEscape = pendingEscape[:0]
			historyIndex = -1
			historyDraft = nil
			previous := cloneRunes(line)
			previousCursor := cursor
			text, c := sanitizeTerminalDraft(seed.text, seed.cursor)
			line = []rune(text)
			if seed.cursor < 0 {
				c = len(line)
			}
			cursor = composerCursorClampRunes(line, c)
			redrawRawInputBuffer(out, previous, previousCursor, line, cursor)
			ch <- inputEvent{kind: inputEventDraft, draft: string(line), cursor: cursor}
		}

		for {
			select {
			case seed := <-interactiveInputSeedCh:
				applySeed(seed)
				continue
			case caret := <-interactiveInputCaretCh:
				applyCaret(caret)
				continue
			default:
			}
			select {
			case <-ctx.Done():
				return
			case seed := <-interactiveInputSeedCh:
				applySeed(seed)
			case caret := <-interactiveInputCaretCh:
				applyCaret(caret)
			case <-time.After(time.Millisecond):
				flushBurst(time.Now())
				// When the background reader is paused (a modal overlay owns
				// stdin via readKey), any partial escape sequence in
				// pendingEscape is stale: the overlay's readKey consumes the
				// remaining bytes from stdin, so the sequence can never
				// complete. Clear it to prevent the SGR-mouse prefix check
				// below from swallowing all subsequent input into a dead
				// buffer, which would freeze the TUI (no keyboard, no mouse,
				// no Ctrl+C). This is the root-cause fix for the long-standing
				// "scroll during conversation freezes viewport" bug.
				// Also clear pendingEscape when it has been sitting for more
				// than 500ms without completing — a partial SGR sequence that
				// was split across reads and never completed (e.g. because the
				// chunk was consumed by a modal overlay's readKey without a
				// clean pause handshake) would otherwise grow indefinitely.
				if len(pendingEscape) > 0 {
					if interactiveInputPaused.Load() || time.Since(pendingEscapeLastActivity) > 500*time.Millisecond {
						pendingEscape = pendingEscape[:0]
					}
				}
			case rr := <-readCh:
				if rr.err != nil {
					if !errors.Is(rr.err, io.EOF) {
						ch <- inputEvent{kind: inputEventDone, err: rr.err}
					} else {
						flushBurst(time.Now().Add(pasteBurstIdleTimeout + time.Millisecond))
						ch <- inputEvent{kind: inputEventDone}
					}
					return
				}

				flushBurst(time.Now())
				for i := 0; i < len(rr.data); i++ {
					b := rr.data[i]
					if collectingPaste {
						pasteBuf.WriteByte(b)
						if bytes.HasSuffix(pasteBuf.Bytes(), []byte{0x1b, '[', '2', '0', '1', '~'}) {
							raw := pasteBuf.Bytes()
							content := sanitizeTerminalInputText(string(raw[:len(raw)-6]))
							pasteBuf.Reset()
							collectingPaste = false
							pasteBurst.clearAfterExplicitPaste()
							if content != "" {
								ch <- inputEvent{kind: inputEventPaste, paste: content}
							}
						}
						continue
					}
					if len(pendingEscape) > 0 {
						pendingEscape = append(pendingEscape, b)
						pendingEscapeLastActivity = time.Now()
						// Safety: discard escape sequences that exceed the maximum
						// valid length. A partial SGR mouse sequence whose
						// terminator was consumed by a modal overlay's readKey()
						// would otherwise grow indefinitely: isSGRMousePrefix
						// returns true for any buffer starting with ESC [ <, and
						// the unconditional continue swallows every subsequent
						// byte. This mirrors the 32-byte guard in
						// rawSelector.readKey's SGR mouse loop.
						if len(pendingEscape) > 32 {
							pendingEscape = pendingEscape[:0]
							continue
						}
						// SGR mouse reports (ESC [ < … M/m) must be handled before
						// the generic sequence checks below: a press ends in 'M'
						// (which escapeSequenceComplete treats as a normal CSI and
						// silently drops) and a release ends in 'm' (which never
						// satisfies escapeSequenceComplete and would grow the buffer
						// without bound). Intercept both here.
						if isSGRMousePrefix(pendingEscape) {
							if sgrMouseComplete(pendingEscape) {
								seq := append([]byte(nil), pendingEscape...)
								pendingEscape = pendingEscape[:0]
								if mev, ok := parseSGRMouse(seq); ok {
									if mouseEv, ok := mouseEventFromSGR(mev); ok {
										ch <- mouseEv
									}
								}
							}
							continue
						}
						if isShiftArrowLeftSequence(pendingEscape) {
							pendingEscape = pendingEscape[:0]
							ch <- inputEvent{kind: inputEventHotkey, hotkey: hotkeyEditLastQueued}
							continue
						}
						if isCtrlEndSequence(pendingEscape) {
							pendingEscape = pendingEscape[:0]
							ch <- inputEvent{kind: inputEventHotkey, hotkey: hotkeyJumpToBottom}
							continue
						}
						if len(pendingEscape) == 2 && pendingEscape[1] == 0x1b {
							continue
						}
						if bytes.Equal(pendingEscape, []byte{0x1b, '[', '2', '0', '0', '~'}) {
							pendingEscape = pendingEscape[:0]
							collectingPaste = true
							pasteBuf.Reset()
							continue
						}
						if len(pendingEscape) == 2 && (pendingEscape[1] == '\r' || pendingEscape[1] == '\n') {
							// Alt+Enter (terminal sends ESC then CR/LF) inserts a
							// literal newline into the draft instead of submitting,
							// giving the composer a cross-terminal multi-line hotkey.
							pendingEscape = pendingEscape[:0]
							if pasted := pasteBurst.flushBeforeModifiedInput(); pasted != "" {
								previous := cloneRunes(line)
								previousCursor := cursor
								line, cursor = insertRunesAtCursor(line, cursor, []rune(pasted))
								if out != nil {
									redrawRawInputBuffer(out, previous, previousCursor, line, cursor)
								}
							}
							pasteBurst.clearWindowAfterNonChar()
							pendingUTF8 = pendingUTF8[:0]
							historyIndex = -1
							historyDraft = nil
							previous := cloneRunes(line)
							previousCursor := cursor
							line, cursor = insertRunesAtCursor(line, cursor, []rune{'\n'})
							redrawRawInputBuffer(out, previous, previousCursor, line, cursor)
							ch <- inputEvent{kind: inputEventDraft, draft: string(line), cursor: cursor}
							continue
						}
						if len(pendingEscape) == 2 && !startsEscapeSequence(pendingEscape) {
							pendingEscape = pendingEscape[:0]
							ch <- inputEvent{kind: inputEventHotkey, hotkey: hotkeyEscapeInterrupt}
							i--
							continue
						}
						if escapeSequenceComplete(pendingEscape) {
							if pasted := pasteBurst.flushBeforeModifiedInput(); pasted != "" {
								previous := cloneRunes(line)
								previousCursor := cursor
								line, cursor = insertRunesAtCursor(line, cursor, []rune(pasted))
								if out != nil {
									redrawRawInputBuffer(out, previous, previousCursor, line, cursor)
								}
							}
							if bytes.Equal(pendingEscape, []byte{0x1b, '[', 'Z'}) {
								pendingEscape = pendingEscape[:0]
								ch <- inputEvent{kind: inputEventHotkey, hotkey: hotkeyTogglePlanMode}
								continue
							}
							if bytes.Equal(pendingEscape, []byte{0x1b, '[', 'I'}) {
								pendingEscape = pendingEscape[:0]
								ch <- inputEvent{kind: inputEventFocusGained}
								continue
							}
							if bytes.Equal(pendingEscape, []byte{0x1b, '[', 'O'}) {
								pendingEscape = pendingEscape[:0]
								ch <- inputEvent{kind: inputEventFocusLost}
								continue
							}
							if direction := historyBrowseDirection(pendingEscape); direction != 0 {
								pendingEscape = pendingEscape[:0]
								if slashOverlayActive.Load() || mentionOverlayActive.Load() || agentRosterFocused.Load() || uiPanelActive.Load() {
									if direction < 0 {
										ch <- inputEvent{kind: inputEventHotkey, hotkey: hotkeyOverlayUp}
									} else {
										ch <- inputEvent{kind: inputEventHotkey, hotkey: hotkeyOverlayDown}
									}
									continue
								}
								// Try moving the cursor within multi-line text before
								// falling back to history navigation. This applies whether
								// or not we are already browsing history: if the current
								// text (draft or history item) is multi-line and the cursor
								// is not on the boundary line, move within the text; only
								// when the cursor is on the top/bottom line do we fall
								// through to history navigation.
								if direction < 0 {
									if nc, ok := cursorUp(line, cursor, int(composerContentWidth.Load())); ok {
										cursor = nc
										ch <- inputEvent{kind: inputEventDraft, draft: string(line), cursor: cursor}
										continue
									}
								} else {
									if nc, ok := cursorDown(line, cursor, int(composerContentWidth.Load())); ok {
										cursor = nc
										ch <- inputEvent{kind: inputEventDraft, draft: string(line), cursor: cursor}
										continue
									}
								}
								// First Down while the roster is visible and no history
								// browse is in progress enters roster focus. Down is a
								// total no-op below (browseRawInputHistory returns
								// changed=false for direction=1, index<0), so this adds
								// behavior without regressing history browsing.
								if direction > 0 && historyIndex < 0 && agentRosterActive.Load() {
									ch <- inputEvent{kind: inputEventHotkey, hotkey: hotkeyOverlayDown}
									continue
								}
								// Composer metadata is added after the main event loop pairs
								// this visible line with its attachments and folded pastes.
								// Reload before navigation so an in-process recall sees it.
								if _, ok := historyStore.(structuredRawInputHistoryStore); ok {
									historyEntries = loadRawInputHistoryEntries(historyStore)
									history = rawInputHistoryEntryTexts(historyEntries)
								}
								nextLine, nextDraft, nextIndex, changed := browseRawInputHistory(history, line, historyDraft, historyIndex, direction)
								if changed {
									redrawRawInputBuffer(out, line, cursor, nextLine, len(nextLine))
									line = nextLine
									cursor = len(line)
									historyDraft = nextDraft
									historyIndex = nextIndex
									ev := inputEvent{kind: inputEventDraft, draft: string(line), cursor: cursor, historyNavigation: true, historyIndex: nextIndex}
									if nextIndex >= 0 && nextIndex < len(historyEntries) {
										ev.historyEntry = cloneRawInputHistoryEntry(historyEntries[nextIndex])
									}
									ch <- ev
								}
								continue
							}
							if bytes.Equal(pendingEscape, []byte{0x1b, '[', 'C'}) || bytes.Equal(pendingEscape, []byte{0x1b, 'O', 'C'}) {
								pendingEscape = pendingEscape[:0]
								if uiPanelActive.Load() {
									ch <- inputEvent{kind: inputEventHotkey, hotkey: hotkeyPanelRight}
									continue
								}
								if cursor < len(line) {
									cursor++
									ch <- inputEvent{kind: inputEventDraft, draft: string(line), cursor: cursor}
								}
								continue
							}
							if bytes.Equal(pendingEscape, []byte{0x1b, '[', 'D'}) || bytes.Equal(pendingEscape, []byte{0x1b, 'O', 'D'}) {
								pendingEscape = pendingEscape[:0]
								if uiPanelActive.Load() {
									ch <- inputEvent{kind: inputEventHotkey, hotkey: hotkeyPanelLeft}
									continue
								}
								if cursor > 0 {
									cursor--
									ch <- inputEvent{kind: inputEventDraft, draft: string(line), cursor: cursor}
								}
								continue
							}
							// Page Up / Page Down (ESC [ 5 ~ / ESC [ 6 ~) page an open
							// slash panel; the composer has no use for them.
							if bytes.Equal(pendingEscape, []byte{0x1b, '[', '5', '~'}) {
								pendingEscape = pendingEscape[:0]
								if uiPanelActive.Load() {
									ch <- inputEvent{kind: inputEventHotkey, hotkey: hotkeyPanelPageUp}
								}
								continue
							}
							if bytes.Equal(pendingEscape, []byte{0x1b, '[', '6', '~'}) {
								pendingEscape = pendingEscape[:0]
								if uiPanelActive.Load() {
									ch <- inputEvent{kind: inputEventHotkey, hotkey: hotkeyPanelPageDown}
								}
								continue
							}
							pendingEscape = pendingEscape[:0]
						}
						continue
					}
					if len(pendingUTF8) > 0 && b < 0x80 {
						pendingUTF8 = pendingUTF8[:0]
					}
					if uiPanelActive.Load() && b != 0x1b && b != 0x03 && b != '\t' {
						// A slash panel is the key target: Enter confirms its
						// row, and nothing typed enters the line buffer — text
						// typed behind a panel would otherwise surface in the
						// composer the moment the panel closes.
						pendingUTF8 = pendingUTF8[:0]
						if b == '\r' || b == '\n' {
							ch <- inputEvent{kind: inputEventHotkey, hotkey: hotkeyPanelAccept}
						}
						continue
					}
					switch b {
					case 0x03:
						if pasted := pasteBurst.flushBeforeModifiedInput(); pasted != "" {
							previous := cloneRunes(line)
							previousCursor := cursor
							line, cursor = insertRunesAtCursor(line, cursor, []rune(pasted))
							if out != nil {
								redrawRawInputBuffer(out, previous, previousCursor, line, cursor)
							}
						}
						hasPendingInput := len(line) > 0 || len(pendingUTF8) > 0
						pasteBurst.clear()
						if hasPendingInput {
							line = line[:0]
							cursor = 0
							pendingUTF8 = pendingUTF8[:0]
							historyIndex = -1
							historyDraft = nil
							clearRawInputLine(out)
							ch <- inputEvent{kind: inputEventDraft, draft: "", cursor: 0}
							ch <- inputEvent{kind: inputEventHotkey, hotkey: hotkeyClearInput}
							continue
						}
						ch <- inputEvent{kind: inputEventHotkey, hotkey: hotkeyInterrupt}
					case 0x1b:
						if pasted := pasteBurst.flushBeforeModifiedInput(); pasted != "" {
							previous := cloneRunes(line)
							previousCursor := cursor
							line, cursor = insertRunesAtCursor(line, cursor, []rune(pasted))
							if out != nil {
								redrawRawInputBuffer(out, previous, previousCursor, line, cursor)
							}
						}
						pasteBurst.clearWindowAfterNonChar()
						pendingEscape = append(pendingEscape[:0], b)
						pendingEscapeLastActivity = time.Now()
					case 0x02:
						if pasted := pasteBurst.flushBeforeModifiedInput(); pasted != "" {
							previous := cloneRunes(line)
							previousCursor := cursor
							line, cursor = insertRunesAtCursor(line, cursor, []rune(pasted))
							if out != nil {
								redrawRawInputBuffer(out, previous, previousCursor, line, cursor)
							}
						}
						pasteBurst.clearWindowAfterNonChar()
						ch <- inputEvent{kind: inputEventHotkey, hotkey: hotkeyBackground}
					case 0x16:
						if pasted := pasteBurst.flushBeforeModifiedInput(); pasted != "" {
							previous := cloneRunes(line)
							previousCursor := cursor
							line, cursor = insertRunesAtCursor(line, cursor, []rune(pasted))
							if out != nil {
								redrawRawInputBuffer(out, previous, previousCursor, line, cursor)
							}
						}
						pasteBurst.clearWindowAfterNonChar()
						ch <- inputEvent{kind: inputEventHotkey, hotkey: hotkeyPasteImage}
					case 0x18:
						flushBeforeHotkey()
						ch <- inputEvent{kind: inputEventHotkey, hotkey: hotkeyAgentControlPrefix}
					case 0x0b:
						flushBeforeHotkey()
						ch <- inputEvent{kind: inputEventHotkey, hotkey: hotkeyAgentStopAll}
					case 0x0c:
						// Ctrl+L: the user's one-shot "repaint everything".
						// The paint shadow is a model of the physical screen;
						// when reality has diverged (an external writer, a
						// terminal that redrew itself) this re-aligns it.
						flushBeforeHotkey()
						ch <- inputEvent{kind: inputEventHotkey, hotkey: hotkeyRedraw}
					case 0x09:
						if pasted := pasteBurst.flushBeforeModifiedInput(); pasted != "" {
							previous := cloneRunes(line)
							previousCursor := cursor
							line, cursor = insertRunesAtCursor(line, cursor, []rune(pasted))
							if out != nil {
								redrawRawInputBuffer(out, previous, previousCursor, line, cursor)
							}
						}
						pasteBurst.clearWindowAfterNonChar()
						pendingUTF8 = pendingUTF8[:0]
						if uiPanelActive.Load() {
							ch <- inputEvent{kind: inputEventHotkey, hotkey: hotkeyPanelTab}
							continue
						}
						if slashOverlayActive.Load() {
							ch <- inputEvent{kind: inputEventHotkey, hotkey: hotkeyOverlayAccept}
							continue
						}
						if mentionOverlayActive.Load() {
							ch <- inputEvent{kind: inputEventHotkey, hotkey: hotkeyOverlayAccept}
							continue
						}
						ch <- inputEvent{kind: inputEventHotkey, hotkey: hotkeyQueueFollowUp}
						continue
					case '/':
						if pasted := pasteBurst.flushBeforeModifiedInput(); pasted != "" {
							previous := cloneRunes(line)
							previousCursor := cursor
							line, cursor = insertRunesAtCursor(line, cursor, []rune(pasted))
							if out != nil {
								redrawRawInputBuffer(out, previous, previousCursor, line, cursor)
							}
						}
						pasteBurst.clearWindowAfterNonChar()
						pendingUTF8 = pendingUTF8[:0]
						historyIndex = -1
						historyDraft = nil
						// Slash is treated as a normal rune. The overlay is owned by
						// run.go and activates inside ComposerState.HandleDraftUpdate
						// when the resulting DraftText is exactly "/". The previous
						// pause/resume hotkey path (newSlashHotkeyEvent + <-resume)
						// was only required because the modal slash picker took the
						// raw input fd; the non-modal overlay shares the composer's
						// reader, so no ack is needed.
						previous := cloneRunes(line)
						previousCursor := cursor
						line, cursor = insertRunesAtCursor(line, cursor, []rune{'/'})
						redrawRawInputBuffer(out, previous, previousCursor, line, cursor)
						ch <- inputEvent{kind: inputEventDraft, draft: string(line), cursor: cursor}
					case '\r', '\n':
						if pasteBurst.appendNewlineIfActive(time.Now()) {
							continue
						}
						if pasteBurst.newlineShouldInsertInsteadOfSubmit(time.Now()) {
							previous := cloneRunes(line)
							previousCursor := cursor
							line, cursor = insertRunesAtCursor(line, cursor, []rune{'\n'})
							if out != nil {
								redrawRawInputBuffer(out, previous, previousCursor, line, cursor)
							}
							pasteBurst.extendWindow(time.Now())
							continue
						}
						// Bare LF (0x0a) is Ctrl+J on every terminal, and also what
						// macOS Terminal.app emits for Option/Alt+Enter when "Use
						// Option as Meta key" is off (the common default) — there it
						// is the only newline-insert gesture, since Alt+Enter never
						// reaches us as ESC+CR. Raw mode disables CR/LF translation,
						// so Enter is a clean \r and Ctrl+J a clean \n: insert a
						// newline on \n, submit only on \r.
						if b == '\n' {
							previous := cloneRunes(line)
							previousCursor := cursor
							line, cursor = insertRunesAtCursor(line, cursor, []rune{'\n'})
							if out != nil {
								redrawRawInputBuffer(out, previous, previousCursor, line, cursor)
							}
							pasteBurst.clearWindowAfterNonChar()
							pendingUTF8 = pendingUTF8[:0]
							historyIndex = -1
							historyDraft = nil
							ch <- inputEvent{kind: inputEventDraft, draft: string(line), cursor: cursor}
							continue
						}
						flushBurst(time.Now().Add(pasteBurstIdleTimeout + time.Millisecond))
						pasteBurst.clearWindowAfterNonChar()
						pendingUTF8 = pendingUTF8[:0]
						submitted := string(line)
						submittedHistoryIndex := -1
						if strings.TrimSpace(submitted) != "" {
							submittedHistoryIndex = len(history)
							historyEntries = append(historyEntries, rawInputHistoryEntry{Text: submitted})
							history = append(history, submitted)
							appendRawInputHistory(historyStore, submitted)
						}
						ch <- inputEvent{kind: inputEventLine, line: submitted, historyEntryRecorded: submittedHistoryIndex >= 0, historyIndex: submittedHistoryIndex}
						line = line[:0]
						cursor = 0
						ch <- inputEvent{kind: inputEventDraft, draft: "", cursor: 0}
						historyIndex = -1
						historyDraft = nil
					case 0x7f, 0x08:
						if pasted := pasteBurst.flushBeforeModifiedInput(); pasted != "" {
							previous := cloneRunes(line)
							previousCursor := cursor
							line, cursor = insertRunesAtCursor(line, cursor, []rune(pasted))
							if out != nil {
								redrawRawInputBuffer(out, previous, previousCursor, line, cursor)
							}
						}
						pasteBurst.clearWindowAfterNonChar()
						pendingUTF8 = pendingUTF8[:0]
						historyIndex = -1
						historyDraft = nil
						if len(line) > 0 {
							previous := cloneRunes(line)
							previousCursor := cursor
							var changed bool
							line, cursor, changed = deleteRuneBeforeCursor(line, cursor)
							if changed {
								redrawRawInputBuffer(out, previous, previousCursor, line, cursor)
								ch <- inputEvent{kind: inputEventDraft, draft: string(line), cursor: cursor}
							}
						}
					default:
						if b < 0x20 {
							if pasted := pasteBurst.flushBeforeModifiedInput(); pasted != "" {
								previous := cloneRunes(line)
								previousCursor := cursor
								line, cursor = insertRunesAtCursor(line, cursor, []rune(pasted))
								if out != nil {
									redrawRawInputBuffer(out, previous, previousCursor, line, cursor)
								}
							}
							pasteBurst.clearWindowAfterNonChar()
							continue
						}
						historyIndex = -1
						historyDraft = nil
						pendingUTF8 = append(pendingUTF8, b)
						if !utf8.FullRune(pendingUTF8) {
							continue
						}
						r, size := utf8.DecodeRune(pendingUTF8)
						if r == utf8.RuneError && size == 1 {
							pendingUTF8 = pendingUTF8[:0]
							continue
						}
						// Intercept bare 'x' to stop the selected roster subagent
						// when the roster is focused, the selected row is a subagent,
						// and the composer line is empty. Held text counts as typed
						// input here: it is committed first so the shortcut cannot
						// swallow a character the reader has already accepted, and
						// so an 'x' that follows one is treated as ordinary input.
						if r == 'x' && agentRosterStopArmed.Load() {
							if held := pasteBurst.flushBeforeModifiedInput(); held != "" {
								previous := cloneRunes(line)
								previousCursor := cursor
								line, cursor = insertRunesAtCursor(line, cursor, []rune(held))
								if out != nil {
									redrawRawInputBuffer(out, previous, previousCursor, line, cursor)
								}
								ch <- inputEvent{kind: inputEventDraft, draft: string(line), cursor: cursor}
							}
							if len(line) == 0 {
								pasteBurst.clearWindowAfterNonChar()
								pendingUTF8 = pendingUTF8[:0]
								ch <- inputEvent{kind: inputEventHotkey, hotkey: hotkeyRosterStop}
								continue
							}
						}
						now := time.Now()
						if r >= 0 && r <= 0x7f {
							switch pasteBurst.onASCIIChar(r, now) {
							case pasteBurstCharDecisionBufferAppend:
								pasteBurst.appendCharToBuffer(r, now)
							case pasteBurstCharDecisionBeginBuffer:
								if cursor == len(line) && pasteBurst.beginBufferFromTypedPrefix(line, now) {
									previous := cloneRunes(line)
									line = line[:0]
									cursor = 0
									if out != nil {
										redrawRawInputBuffer(out, previous, len(previous), line, cursor)
									}
									pasteBurst.appendCharToBuffer(r, now)
								} else {
									previous := cloneRunes(line)
									previousCursor := cursor
									line, cursor = insertRunesAtCursor(line, cursor, []rune{r})
									redrawRawInputBuffer(out, previous, previousCursor, line, cursor)
									ch <- inputEvent{kind: inputEventDraft, draft: string(line), cursor: cursor}
								}
							case pasteBurstCharDecisionBeginBufferFromPending:
								pasteBurst.appendCharToBuffer(r, now)
							case pasteBurstCharDecisionRetainFirstChar:
								// Keep pending briefly to avoid flicker.
							default:
								previous := cloneRunes(line)
								previousCursor := cursor
								line, cursor = insertRunesAtCursor(line, cursor, []rune{r})
								redrawRawInputBuffer(out, previous, previousCursor, line, cursor)
								ch <- inputEvent{kind: inputEventDraft, draft: string(line), cursor: cursor}
							}
						} else {
							switch pasteBurst.onNonASCIIChar(r, now) {
							case pasteBurstCharDecisionBufferAppend:
								pasteBurst.appendCharToBuffer(r, now)
							case pasteBurstCharDecisionBeginBuffer:
								if cursor == len(line) && pasteBurst.beginBufferFromTypedPrefix(line, now) {
									previous := cloneRunes(line)
									line = line[:0]
									cursor = 0
									if out != nil {
										redrawRawInputBuffer(out, previous, len(previous), line, cursor)
									}
									pasteBurst.appendCharToBuffer(r, now)
								} else {
									previous := cloneRunes(line)
									previousCursor := cursor
									line, cursor = insertRunesAtCursor(line, cursor, []rune{r})
									redrawRawInputBuffer(out, previous, previousCursor, line, cursor)
									ch <- inputEvent{kind: inputEventDraft, draft: string(line), cursor: cursor}
								}
							case pasteBurstCharDecisionBeginBufferFromPending:
								pasteBurst.appendCharToBuffer(r, now)
							case pasteBurstCharDecisionRetainFirstChar:
								// Keep pending briefly to avoid flicker.
							default:
								previous := cloneRunes(line)
								previousCursor := cursor
								line, cursor = insertRunesAtCursor(line, cursor, []rune{r})
								redrawRawInputBuffer(out, previous, previousCursor, line, cursor)
								ch <- inputEvent{kind: inputEventDraft, draft: string(line), cursor: cursor}
							}
						}
						pendingUTF8 = pendingUTF8[size:]
					}
				}

				// After processing chunk, if pendingEscape contains only bare Esc (0x1b)
				// with no follow-up bytes, check if more data is immediately available.
				// If no data arrives within 60ms, treat as bare Esc interrupt.
				if len(pendingEscape) == 1 && pendingEscape[0] == 0x1b {
					fd := int(in.Fd())
					readable, _ := pollReadableInputFD(fd, 60)
					if !readable {
						// No follow-up bytes within 60ms - bare Esc press
						pendingEscape = pendingEscape[:0]
						ch <- inputEvent{kind: inputEventHotkey, hotkey: hotkeyEscapeInterrupt}
					}
				}

				select {
				case ackCh <- struct{}{}:
				default:
				}
			}
		}
	}()
	return ch
}

const pasteBurstMinChars = 3

var (
	pasteBurstCharInterval = 8 * time.Millisecond
	pasteBurstIdleTimeout  = func() time.Duration {
		if runtime.GOOS == "windows" {
			return 60 * time.Millisecond
		}
		return 8 * time.Millisecond
	}()
	pasteEnterSuppressWindow = 120 * time.Millisecond
)

type pasteBurstKind int

const (
	pasteBurstResultNone pasteBurstKind = iota
	pasteBurstResultTyped
	pasteBurstResultPaste
)

type pasteBurstResult struct {
	kind pasteBurstKind
	text string
}

type pasteBurstCharDecision int

const (
	pasteBurstCharDecisionNone pasteBurstCharDecision = iota
	pasteBurstCharDecisionBufferAppend
	pasteBurstCharDecisionBeginBuffer
	pasteBurstCharDecisionRetainFirstChar
	pasteBurstCharDecisionBeginBufferFromPending
)

type pasteBurstState struct {
	lastPlainCharTime     time.Time
	consecutivePlainChars uint16
	burstWindowUntil      time.Time
	buffer                []rune
	active                bool
	// held is printable input the reader has already accepted but not yet handed
	// to the composer, in typing order, with heldAt the time of its newest rune.
	// Holding is what keeps a fast burst from being painted character by
	// character, so everything else in this machine must leave it alone: a
	// single-slot variant of this used to be overwritten by the next keystroke
	// and wiped by burst-window resets, which silently dropped typed characters.
	// Anything that stops holding must hand the text back (see
	// flushBeforeModifiedInput) or emit it (see flushIfDue).
	held   []rune
	heldAt time.Time
}

func newPasteBurstState() pasteBurstState {
	return pasteBurstState{}
}

func (p *pasteBurstState) onASCIIChar(r rune, now time.Time) pasteBurstCharDecision {
	return p.onPlainChar(r, now)
}

func (p *pasteBurstState) onNonASCIIChar(r rune, now time.Time) pasteBurstCharDecision {
	return p.onPlainChar(r, now)
}

// onPlainChar decides what happens to one printable character. ASCII and
// non-ASCII differ only in how the caller inserts the rune, never in when it is
// held, so both entry points share this decision.
func (p *pasteBurstState) onPlainChar(r rune, now time.Time) pasteBurstCharDecision {
	p.notePlainChar(now)

	if p.active {
		p.extendWindow(now)
		return pasteBurstCharDecisionBufferAppend
	}

	if len(p.held) > 0 && now.Sub(p.heldAt) <= pasteBurstCharInterval {
		// Fast enough to read as one burst: the held runes are older than the
		// current one, so they move into the candidate body ahead of it.
		p.buffer = append(p.buffer, p.held...)
		p.held = p.held[:0]
		p.active = true
		p.extendWindow(now)
		return pasteBurstCharDecisionBeginBufferFromPending
	}

	if p.consecutivePlainChars >= pasteBurstMinChars {
		return pasteBurstCharDecisionBeginBuffer
	}

	// Too slow to be a burst yet, so the rune waits for the idle flush instead of
	// going straight to the composer — and stays queued behind anything already
	// waiting rather than replacing it.
	p.held = append(p.held, r)
	p.heldAt = now
	return pasteBurstCharDecisionRetainFirstChar
}

func (p *pasteBurstState) notePlainChar(now time.Time) {
	if !p.lastPlainCharTime.IsZero() && now.Sub(p.lastPlainCharTime) <= pasteBurstCharInterval {
		p.consecutivePlainChars++
	} else {
		p.consecutivePlainChars = 1
	}
	p.lastPlainCharTime = now
}

// flushIfDue emits whatever the reader is holding once it has been idle long
// enough. The candidate burst body goes first: it is older than any held rune,
// and while a body is buffering nothing else is held.
func (p *pasteBurstState) flushIfDue(now time.Time) pasteBurstResult {
	if p.isActiveInternal() && !p.lastPlainCharTime.IsZero() {
		if now.Sub(p.lastPlainCharTime) > pasteBurstIdleTimeout {
			out := sanitizeTerminalInputText(string(p.buffer))
			if looksPastey([]rune(out)) {
				p.clearAfterExplicitPaste()
				return pasteBurstResult{kind: pasteBurstResultPaste, text: out}
			}
			p.buffer = p.buffer[:0]
			p.clearWindowAfterNonChar()
			return pasteBurstResult{kind: pasteBurstResultTyped, text: out}
		}
		return pasteBurstResult{kind: pasteBurstResultNone}
	}

	// Held runes time out against their own clock, not the burst window's. That
	// independence is the point: any reset of the burst bookkeeping used to
	// strand them here for good.
	if len(p.held) > 0 {
		if now.Sub(p.heldAt) <= pasteBurstCharInterval {
			return pasteBurstResult{kind: pasteBurstResultNone}
		}
		out := sanitizeTerminalInputText(string(p.held))
		p.held = p.held[:0]
		if out == "" {
			return pasteBurstResult{kind: pasteBurstResultNone}
		}
		return pasteBurstResult{kind: pasteBurstResultTyped, text: out}
	}

	return pasteBurstResult{kind: pasteBurstResultNone}
}

// flushBeforeModifiedInput hands back everything the reader is holding, oldest
// first, for the caller to commit before an editing key takes effect. Returning
// "" means there is genuinely nothing held.
func (p *pasteBurstState) flushBeforeModifiedInput() string {
	if !p.isActive() {
		return ""
	}
	out := make([]rune, 0, len(p.buffer)+len(p.held))
	out = append(out, p.buffer...)
	out = append(out, p.held...)
	p.active = false
	p.buffer = p.buffer[:0]
	p.held = p.held[:0]
	return sanitizeTerminalInputText(string(out))
}

func (p *pasteBurstState) appendCharToBuffer(r rune, now time.Time) {
	p.buffer = append(p.buffer, r)
	p.active = true
	p.extendWindow(now)
}

func (p *pasteBurstState) beginBufferFromTypedPrefix(prefix []rune, now time.Time) bool {
	if !looksPastey(prefix) {
		return false
	}
	p.buffer = append(p.buffer[:0], prefix...)
	p.active = true
	p.extendWindow(now)
	return true
}

func (p *pasteBurstState) appendNewlineIfActive(now time.Time) bool {
	if !p.isActive() {
		return false
	}
	// Held runes were accepted before this newline, so they fold in ahead of it.
	p.buffer = append(p.buffer, p.held...)
	p.held = p.held[:0]
	p.buffer = append(p.buffer, '\n')
	p.extendWindow(now)
	return true
}

func (p *pasteBurstState) newlineShouldInsertInsteadOfSubmit(now time.Time) bool {
	if p.isActive() {
		return true
	}
	return !p.burstWindowUntil.IsZero() && !now.After(p.burstWindowUntil)
}

func (p *pasteBurstState) extendWindow(now time.Time) {
	p.burstWindowUntil = now.Add(pasteEnterSuppressWindow)
}

// clearWindowAfterNonChar ends the burst window: the next character counts as
// the first of a new burst, and Enter stops being read as part of one. It leaves
// held text alone on purpose — those keystrokes have been accepted, and the idle
// flush is their way out.
func (p *pasteBurstState) clearWindowAfterNonChar() {
	p.consecutivePlainChars = 0
	p.lastPlainCharTime = time.Time{}
	p.burstWindowUntil = time.Time{}
	p.active = false
}

func (p *pasteBurstState) clearAfterExplicitPaste() {
	p.clearWindowAfterNonChar()
	p.buffer = p.buffer[:0]
}

// clear drops the held text as well. Only callers that are replacing the draft
// wholesale (a seed from the app, Ctrl+C) may use it: there the app's own state
// supersedes anything the reader was still holding.
func (p *pasteBurstState) clear() {
	if len(p.held) > 0 || len(p.buffer) > 0 {
	}
	p.clearAfterExplicitPaste()
	p.held = p.held[:0]
}

func (p *pasteBurstState) isActive() bool {
	return p.isActiveInternal() || len(p.held) > 0
}

func (p *pasteBurstState) isActiveInternal() bool {
	return p.active || len(p.buffer) > 0
}

func looksPastey(rs []rune) bool {
	if len(rs) < pasteBurstMinChars {
		return false
	}
	if len(rs) >= 16 {
		return true
	}
	for _, r := range rs {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			return true
		}
	}
	return false
}

const rawInputHistoryMetadataPrefix = "# forebrain-history-metadata-v1 "

type rawInputHistoryEntry struct {
	Text          string
	Attachments   []InputAttachment
	PendingPastes []PendingPaste
}

type rawInputHistoryMetadataRecord struct {
	Index         int               `json:"index"`
	Text          string            `json:"text"`
	Attachments   []InputAttachment `json:"attachments,omitempty"`
	PendingPastes []PendingPaste    `json:"pending_pastes,omitempty"`
}

type structuredRawInputHistoryStore interface {
	LoadEntries() ([]rawInputHistoryEntry, error)
	Enrich(index int, entry rawInputHistoryEntry) error
}

func rawInputHistoryEntryTexts(entries []rawInputHistoryEntry) []string {
	texts := make([]string, 0, len(entries))
	for _, entry := range entries {
		texts = append(texts, entry.Text)
	}
	return texts
}

func cloneRawInputHistoryEntry(entry rawInputHistoryEntry) rawInputHistoryEntry {
	entry.Attachments = append([]InputAttachment(nil), entry.Attachments...)
	entry.PendingPastes = append([]PendingPaste(nil), entry.PendingPastes...)
	return entry
}

type rawInputHistoryStore interface {
	Load() ([]string, error)
	Append(line string) error
}

func newRawInputHistoryStore(path string) rawInputHistoryStore {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	return &fileRawInputHistoryStore{path: path}
}

type fileRawInputHistoryStore struct {
	path    string
	mu      sync.Mutex
	loaded  bool
	entries []rawInputHistoryEntry
}

func (s *fileRawInputHistoryStore) Load() ([]string, error) {
	entries, err := s.LoadEntries()
	if err != nil {
		return nil, err
	}
	return rawInputHistoryEntryTexts(entries), nil
}

func (s *fileRawInputHistoryStore) LoadEntries() ([]rawInputHistoryEntry, error) {
	if s == nil || strings.TrimSpace(s.path) == "" {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadEntriesLocked(); err != nil {
		return nil, err
	}
	return cloneRawInputHistoryEntries(s.entries), nil
}

func (s *fileRawInputHistoryStore) Append(line string) error {
	if s == nil || strings.TrimSpace(s.path) == "" {
		return nil
	}
	line, ok := normalizeRawInputHistoryLine(line)
	if !ok {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadEntriesLocked(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(line + "\n")
	if err == nil {
		s.entries = append(s.entries, rawInputHistoryEntry{Text: line})
	}
	return err
}

func (s *fileRawInputHistoryStore) Enrich(index int, entry rawInputHistoryEntry) error {
	if s == nil || strings.TrimSpace(s.path) == "" || index < 0 {
		return nil
	}
	if len(entry.Attachments) == 0 && len(entry.PendingPastes) == 0 {
		return nil
	}
	text, ok := normalizeRawInputHistoryLine(entry.Text)
	if !ok {
		return nil
	}
	record := rawInputHistoryMetadataRecord{
		Index:         index,
		Text:          text,
		Attachments:   append([]InputAttachment(nil), entry.Attachments...),
		PendingPastes: append([]PendingPaste(nil), entry.PendingPastes...),
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	encoded := base64.RawStdEncoding.EncodeToString(raw)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadEntriesLocked(); err != nil {
		return err
	}
	if index >= len(s.entries) || s.entries[index].Text != text {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(rawInputHistoryMetadataPrefix + encoded + "\n")
	if err == nil {
		s.entries[index].Attachments = append([]InputAttachment(nil), entry.Attachments...)
		s.entries[index].PendingPastes = append([]PendingPaste(nil), entry.PendingPastes...)
	}
	return err
}

func (s *fileRawInputHistoryStore) loadEntriesLocked() error {
	if s.loaded {
		return nil
	}
	entries, err := loadRawInputHistoryEntriesFile(s.path)
	if err != nil {
		return err
	}
	s.entries = entries
	s.loaded = true
	return nil
}

func cloneRawInputHistoryEntries(entries []rawInputHistoryEntry) []rawInputHistoryEntry {
	out := make([]rawInputHistoryEntry, len(entries))
	for i := range entries {
		out[i] = cloneRawInputHistoryEntry(entries[i])
	}
	return out
}

func loadRawInputHistoryEntries(store rawInputHistoryStore) []rawInputHistoryEntry {
	if store == nil {
		return nil
	}
	if structured, ok := store.(structuredRawInputHistoryStore); ok {
		entries, err := structured.LoadEntries()
		if err != nil {
			return nil
		}
		return entries
	}
	history, err := store.Load()
	if err != nil {
		return nil
	}
	entries := make([]rawInputHistoryEntry, 0, len(history))
	for _, text := range history {
		entries = append(entries, rawInputHistoryEntry{Text: text})
	}
	return entries
}

func appendRawInputHistory(store rawInputHistoryStore, line string) {
	if store == nil {
		return
	}
	_ = store.Append(line)
}

func enrichRawInputHistory(store rawInputHistoryStore, index int, entry rawInputHistoryEntry) {
	structured, ok := store.(structuredRawInputHistoryStore)
	if !ok {
		return
	}
	_ = structured.Enrich(index, entry)
}

func loadRawInputHistoryEntriesFile(path string) ([]rawInputHistoryEntry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	history := make([]rawInputHistoryEntry, 0, len(lines))
	for _, line := range lines {
		if record, ok := parseRawInputHistoryMetadata(line); ok {
			if record.Index >= 0 && record.Index < len(history) && history[record.Index].Text == record.Text {
				history[record.Index].Attachments = append([]InputAttachment(nil), record.Attachments...)
				history[record.Index].PendingPastes = append([]PendingPaste(nil), record.PendingPastes...)
			}
			continue
		}
		line, ok := normalizeRawInputHistoryLine(line)
		if !ok {
			continue
		}
		history = append(history, rawInputHistoryEntry{Text: line})
	}
	return history, nil
}

func parseRawInputHistoryMetadata(line string) (rawInputHistoryMetadataRecord, bool) {
	encoded := strings.TrimPrefix(line, rawInputHistoryMetadataPrefix)
	if encoded == line || strings.TrimSpace(encoded) == "" {
		return rawInputHistoryMetadataRecord{}, false
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return rawInputHistoryMetadataRecord{}, false
	}
	var record rawInputHistoryMetadataRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return rawInputHistoryMetadataRecord{}, false
	}
	return record, true
}

func normalizeRawInputHistoryLine(line string) (string, bool) {
	line = strings.ReplaceAll(line, "\r", "")
	line = strings.ReplaceAll(line, "\n", " ")
	if strings.TrimSpace(line) == "" {
		return "", false
	}
	return line, true
}

const largePasteCharThreshold = 1000

func imageAttachmentPlaceholder(index int) string {
	return fmt.Sprintf("[Image #%d]", index+1)
}

func renderComposerImagePlaceholder(index int) string {
	return termenv.ANSI.String(imageAttachmentPlaceholder(index)).
		Foreground(termenv.ANSI.Color("15")).
		Background(termenv.ANSI.Color("27")).
		String() + sharedBlockTextStyle
}

func renderComposerPastePlaceholder(placeholder string) string {
	return termenv.ANSI.String(placeholder).
		Foreground(termenv.ANSI.Color("15")).
		Background(termenv.ANSI.Color("27")).
		String() + sharedBlockTextStyle
}

func buildComposerText(text string, pastes []PendingPaste, attachments []InputAttachment) string {
	return buildComposerTextWithRenderers(text, pastes, attachments, func(i int) string {
		return fmt.Sprintf("[Image #%d]", i+1)
	}, nil)
}

func buildComposerDisplayText(text string, pastes []PendingPaste, attachments []InputAttachment) string {
	return buildComposerTextWithRenderers(text, pastes, attachments, func(i int) string {
		return renderComposerImagePlaceholder(i)
	}, renderComposerPastePlaceholder)
}

func buildComposerTextWithRenderers(text string, pastes []PendingPaste, attachments []InputAttachment, renderAttachment func(i int) string, renderPaste func(placeholder string) string) string {
	var parts []string
	if text != "" {
		inline := renderInlineAttachmentPlaceholders(text, attachments, renderAttachment)
		inline = renderInlinePastePlaceholders(inline, pastes, renderPaste)
		parts = append(parts, inline)
	}
	for _, paste := range pastes {
		placeholder := strings.TrimSpace(paste.Placeholder)
		if placeholder == "" {
			continue
		}
		if strings.Contains(text, placeholder) {
			continue
		}
		if renderPaste != nil {
			placeholder = renderPaste(placeholder)
		}
		parts = append(parts, placeholder)
	}
	for i := range attachments {
		if strings.Contains(text, imageAttachmentPlaceholder(i)) {
			continue
		}
		parts = append(parts, renderAttachment(i))
	}
	return strings.Join(parts, " ")
}

// renderInlinePastePlaceholders styles paste placeholders that live inline in
// the draft text. A single Replacer pass with longer placeholders first keeps
// "[... chars]" from matching inside its own "#N" variants or inside already
// substituted output.
func renderInlinePastePlaceholders(text string, pastes []PendingPaste, renderPaste func(placeholder string) string) string {
	if text == "" || len(pastes) == 0 || renderPaste == nil {
		return text
	}
	placeholders := make([]string, 0, len(pastes))
	for _, paste := range pastes {
		if p := strings.TrimSpace(paste.Placeholder); p != "" {
			placeholders = append(placeholders, p)
		}
	}
	if len(placeholders) == 0 {
		return text
	}
	sort.SliceStable(placeholders, func(i, j int) bool {
		return len(placeholders[i]) > len(placeholders[j])
	})
	pairs := make([]string, 0, 2*len(placeholders))
	for _, p := range placeholders {
		pairs = append(pairs, p, renderPaste(p))
	}
	return strings.NewReplacer(pairs...).Replace(text)
}

func renderInlineAttachmentPlaceholders(text string, attachments []InputAttachment, renderAttachment func(i int) string) string {
	if text == "" || len(attachments) == 0 || renderAttachment == nil {
		return text
	}
	out := text
	for i := range attachments {
		out = strings.ReplaceAll(out, imageAttachmentPlaceholder(i), renderAttachment(i))
	}
	return out
}

func stripComposerAttachmentPlaceholders(text string, attachments []InputAttachment) string {
	if text == "" || len(attachments) == 0 {
		return text
	}
	out := text
	for i := range attachments {
		out = strings.ReplaceAll(out, imageAttachmentPlaceholder(i), "")
	}
	return out
}

func expandComposerText(text string, pastes []PendingPaste) string {
	out := text
	// Expand longer placeholders first: "[Pasted Content N chars]" is a string
	// prefix of "[Pasted Content N chars] #2", so expanding the base first
	// could splice paste #1's content into the middle of paste #2's marker.
	ordered := append([]PendingPaste(nil), pastes...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return len(strings.TrimSpace(ordered[i].Placeholder)) > len(strings.TrimSpace(ordered[j].Placeholder))
	})
	for _, paste := range ordered {
		placeholder := strings.TrimSpace(paste.Placeholder)
		if placeholder == "" {
			continue
		}
		idx := strings.Index(out, placeholder)
		if idx < 0 {
			continue
		}
		out = out[:idx] + paste.Content + out[idx+len(placeholder):]
	}
	return out
}

func shouldFoldLargePaste(content string) bool {
	return len([]rune(content)) > largePasteCharThreshold
}

func nextLargePastePlaceholderForCount(count int, existing []PendingPaste) string {
	base := fmt.Sprintf("[Pasted Content %d chars]", count)
	prefix := base + " #"
	maxSuffix := 0
	for _, paste := range existing {
		placeholder := strings.TrimSpace(paste.Placeholder)
		if placeholder == "" {
			continue
		}
		if placeholder == base {
			if maxSuffix < 1 {
				maxSuffix = 1
			}
			continue
		}
		if !strings.HasPrefix(placeholder, prefix) {
			continue
		}
		suffix := strings.TrimSpace(strings.TrimPrefix(placeholder, prefix))
		if suffix == "" {
			continue
		}
		var value int
		if _, err := fmt.Sscanf(suffix, "%d", &value); err == nil && value > maxSuffix {
			maxSuffix = value
		}
	}
	if maxSuffix == 0 {
		return base
	}
	return fmt.Sprintf("%s #%d", base, maxSuffix+1)
}

// uiPanelActive marks the main-loop-owned panel (slash /status, /mcp) as the
// current key target while it is open: the raw reader forwards arrows, tab
// and page keys as panel hotkeys instead of composer edits.
var uiPanelActive atomic.Bool

func setUIPanelActive(v bool) { uiPanelActive.Store(v) }
