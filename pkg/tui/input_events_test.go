package tui

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type testRawInputHistoryStore struct {
	history []string
}

func (s testRawInputHistoryStore) Load() ([]string, error) {
	return append([]string(nil), s.history...), nil
}

func (testRawInputHistoryStore) Append(string) error { return nil }

func drainInteractiveInputSeedQueue() {
	for {
		select {
		case <-interactiveInputSeedCh:
		default:
			return
		}
	}
}

func nextNonDraftEvent(t *testing.T, events <-chan inputEvent) inputEvent {
	t.Helper()
	for {
		ev, ok := <-events
		if !ok {
			t.Fatal("expected input event, channel closed")
		}
		if ev.kind == inputEventDraft {
			continue
		}
		return ev
	}
}

func TestReadRawInputEventsSlashEmitsDraftEvent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe error: %v", err)
	}
	defer r.Close()

	events := readRawInputEvents(ctx, r, io.Discard, stubRawInputController{}, nil)
	if _, err := w.Write([]byte("/")); err != nil {
		t.Fatalf("write slash: %v", err)
	}
	var draft inputEvent
	for {
		ev, ok := <-events
		if !ok {
			t.Fatal("channel closed before draft event")
		}
		if ev.kind == inputEventDraft {
			draft = ev
			break
		}
	}
	if draft.draft != "/" {
		t.Fatalf("expected draft %q, got %q", "/", draft.draft)
	}
	_ = w.Close()
}

func TestReadRawInputEventsShiftTabEmitsTogglePlanMode(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe error: %v", err)
	}
	defer r.Close()

	events := readRawInputEvents(ctx, r, io.Discard, stubRawInputController{}, nil)
	if _, err := w.Write([]byte{0x1b, '[', 'Z'}); err != nil {
		t.Fatalf("write shift-tab: %v", err)
	}
	ev := nextNonDraftEvent(t, events)
	if ev.kind != inputEventHotkey || ev.hotkey != hotkeyTogglePlanMode {
		t.Fatalf("expected toggle-plan-mode hotkey, got %#v", ev)
	}
	_ = w.Close()
}

func TestReadRawInputEventsEmitsTerminalFocusChanges(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe error: %v", err)
	}
	defer r.Close()

	events := readRawInputEvents(ctx, r, io.Discard, stubRawInputController{}, nil)
	if _, err := w.Write([]byte("\x1b[O\x1b[I")); err != nil {
		t.Fatalf("write focus reports: %v", err)
	}
	if ev := nextNonDraftEvent(t, events); ev.kind != inputEventFocusLost {
		t.Fatalf("first focus event = %#v, want focus lost", ev)
	}
	if ev := nextNonDraftEvent(t, events); ev.kind != inputEventFocusGained {
		t.Fatalf("second focus event = %#v, want focus gained", ev)
	}
	_ = w.Close()
}

func TestReadRawInputEventsEmitsTabQueueFollowUpHotkey(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe error: %v", err)
	}
	defer r.Close()

	events := readRawInputEvents(ctx, r, io.Discard, stubRawInputController{}, nil)
	if _, err := w.Write([]byte{0x09}); err != nil {
		t.Fatalf("write tab: %v", err)
	}
	select {
	case ev := <-events:
		if ev.kind != inputEventHotkey || ev.hotkey != hotkeyQueueFollowUp {
			t.Fatalf("expected tab to emit queue-follow-up hotkey, got %#v", ev)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected tab queue-follow-up hotkey")
	}
	_ = w.Close()
}

func TestReadRawInputEventsEmitsShiftLeftEditLastQueuedHotkey(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe error: %v", err)
	}
	defer r.Close()

	events := readRawInputEvents(ctx, r, io.Discard, stubRawInputController{}, nil)
	if _, err := w.Write([]byte{0x1b, '[', '1', ';', '2', 'D'}); err != nil {
		t.Fatalf("write shift-left: %v", err)
	}
	ev := nextNonDraftEvent(t, events)
	if ev.kind != inputEventHotkey || ev.hotkey != hotkeyEditLastQueued {
		t.Fatalf("expected edit-last-queued hotkey, got %#v", ev)
	}
	_ = w.Close()
}

func TestReadRawInputEventsAltEnterInsertsNewline(t *testing.T) {
	for name, seq := range map[string][]byte{
		"esc-cr": {0x1b, '\r'},
		"esc-lf": {0x1b, '\n'},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatalf("pipe error: %v", err)
			}
			defer r.Close()

			events := readRawInputEvents(ctx, r, io.Discard, stubRawInputController{}, nil)
			if _, err := w.Write([]byte("ab")); err != nil {
				t.Fatalf("write ab: %v", err)
			}
			time.Sleep(20 * time.Millisecond)
			if _, err := w.Write(seq); err != nil {
				t.Fatalf("write alt+enter: %v", err)
			}
			time.Sleep(20 * time.Millisecond)
			if _, err := w.Write([]byte("cd")); err != nil {
				t.Fatalf("write cd: %v", err)
			}
			time.Sleep(20 * time.Millisecond)
			_ = w.Close()

			var lastDraft string
			for ev := range events {
				if ev.kind == inputEventLine {
					t.Fatalf("alt+enter must not submit, got line event %q", ev.line)
				}
				if ev.kind == inputEventHotkey && ev.hotkey == hotkeyEscapeInterrupt {
					t.Fatal("alt+enter must not emit escape-interrupt hotkey")
				}
				if ev.kind == inputEventDraft {
					lastDraft = ev.draft
				}
			}
			if lastDraft != "ab\ncd" {
				t.Fatalf("final draft = %q, want %q", lastDraft, "ab\ncd")
			}
		})
	}
}

// TestReadRawInputEventsBareLFInsertsNewline covers the macOS Terminal.app
// Option+Enter case: with "Use Option as Meta key" off (the default), the
// terminal emits a bare LF (0x0a, same as Ctrl+J) with no ESC prefix. That must
// insert a newline into the draft, not submit.
func TestReadRawInputEventsBareLFInsertsNewline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe error: %v", err)
	}
	defer r.Close()

	events := readRawInputEvents(ctx, r, io.Discard, stubRawInputController{}, nil)
	if _, err := w.Write([]byte("ab")); err != nil {
		t.Fatalf("write ab: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	if _, err := w.Write([]byte{'\n'}); err != nil { // Ctrl+J / Terminal.app Option+Enter
		t.Fatalf("write LF: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	if _, err := w.Write([]byte("cd")); err != nil {
		t.Fatalf("write cd: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	_ = w.Close()

	var lastDraft string
	for ev := range events {
		if ev.kind == inputEventLine {
			t.Fatalf("bare LF must not submit, got line event %q", ev.line)
		}
		if ev.kind == inputEventDraft {
			lastDraft = ev.draft
		}
	}
	if lastDraft != "ab\ncd" {
		t.Fatalf("final draft = %q, want %q", lastDraft, "ab\ncd")
	}
}

func TestReadRawInputEventsSlashFollowedByCharEmitsDrafts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe error: %v", err)
	}
	defer r.Close()

	events := readRawInputEvents(ctx, r, io.Discard, stubRawInputController{}, nil)
	if _, err := w.Write([]byte("/")); err != nil {
		t.Fatalf("write slash: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	if _, err := w.Write([]byte("m")); err != nil {
		t.Fatalf("write m: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	_ = w.Close()

	var drafts []string
	for ev := range events {
		if ev.kind == inputEventDraft {
			drafts = append(drafts, ev.draft)
		}
	}
	if len(drafts) < 2 {
		t.Fatalf("expected at least 2 draft events, got %d: %v", len(drafts), drafts)
	}
	if drafts[0] != "/" {
		t.Fatalf("first draft = %q, want %q", drafts[0], "/")
	}
	if drafts[1] != "/m" {
		t.Fatalf("second draft = %q, want %q", drafts[1], "/m")
	}
}

func TestPauseAndResumeInteractiveInputRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pauseInteractiveInputRead()
	started := make(chan struct{})
	done := make(chan struct {
		paused bool
		ok     bool
	}, 1)
	go func() {
		close(started)
		paused, ok := waitIfInteractiveInputPaused(ctx)
		done <- struct {
			paused bool
			ok     bool
		}{paused: paused, ok: ok}
	}()
	<-started
	time.Sleep(2 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("wait returned before resume")
	default:
	}
	resumeInteractiveInputRead()
	res := <-done
	if !res.ok || !res.paused {
		t.Fatal("expected wait to return true after resume")
	}
}

func TestReadRawInputEventsProcessesPausedHotkeyAfterResume(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pauseInteractiveInputRead()
	defer resumeInteractiveInputRead()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe error: %v", err)
	}
	defer r.Close()

	var out bytes.Buffer
	events := readRawInputEvents(ctx, r, &out, stubRawInputController{}, testRawInputHistoryStore{})
	received := make(chan string, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range events {
			if ev.kind == inputEventHotkey && ev.hotkey == hotkeyBackground {
				select {
				case received <- "background hotkey":
				default:
				}
				return
			}
			if ev.kind == inputEventDone {
				return
			}
		}
	}()

	if _, err := w.Write([]byte{0x02}); err != nil {
		t.Fatalf("write hotkey: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	resumeInteractiveInputRead()
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for raw input reader to finish")
	}
	select {
	case msg := <-received:
		// After resume, the hotkey byte is processed normally.
		_ = msg
	default:
		t.Fatal("expected background hotkey to be processed after resume")
	}
}

// TestPauseAckSucceedsWhileInnerReaderBlockedOnReadCh reproduces the freeze
// that occurs after multiple tool-approval overlays. The inner reader goroutine
// can get stuck on <-ackCh when the middle goroutine is blocked sending an
// event on the unbuffered ch channel because the main event loop is busy
// rendering. Without the fix, the inner goroutine never reaches its top-of-loop
// pause check, pauseInteractiveInputReadAndWait times out, and the overlay's
// readKey races with the inner goroutine for stdin bytes — causing the TUI to
// freeze.
func TestPauseAckSucceedsWhileInnerReaderBlockedOnReadCh(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Ensure clean pause state.
	resumeInteractiveInputRead()
	drainInteractiveInputSeedQueue()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe error: %v", err)
	}
	defer r.Close()
	defer w.Close()

	var out bytes.Buffer
	events := readRawInputEvents(ctx, r, &out, stubRawInputController{}, testRawInputHistoryStore{})

	// Write 0x02 (Ctrl+B) which causes the middle goroutine to immediately
	// block on ch <- (sending hotkeyBackground) because we don't receive
	// from events. This blocks the inner goroutine on <-ackCh because the
	// middle goroutine never reaches the ackCh send.
	if _, err := w.Write([]byte{0x02}); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Give the pipeline time to back up: inner goroutine reads 0x02, sends
	// to readCh, middle goroutine receives and blocks on ch <-.
	time.Sleep(50 * time.Millisecond)

	// Now request a pause. The inner goroutine is stuck on <-ackCh and
	// cannot reach its top-of-loop pause check. Without the fix, this would
	// time out after 50ms. With the fix, acknowledgePauseIfRequested runs
	// inside the <-ackCh wait loop and stores the ack.
	pauseInteractiveInputRead()
	ackDone := make(chan bool, 1)
	go func() {
		ackDone <- pauseInteractiveInputReadAndWait(500 * time.Millisecond)
	}()

	select {
	case ok := <-ackDone:
		if !ok {
			t.Fatal("pauseInteractiveInputReadAndWait returned false (timeout) — inner goroutine did not acknowledge pause while blocked on ackCh")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pauseInteractiveInputReadAndWait did not return — deadlock")
	}

	// Clean up: drain the event and resume.
	select {
	case <-events:
	case <-time.After(time.Second):
		t.Fatal("timeout draining event from events channel")
	}
	resumeInteractiveInputRead()
}

func TestBrowseRawInputHistoryNavigatesUpAndDown(t *testing.T) {
	history := []string{"first", "second", "third"}
	current := []rune("draft")
	line, draft, index, changed := browseRawInputHistory(history, current, nil, -1, -1)
	if !changed || string(line) != "third" || string(draft) != "draft" || index != 2 {
		t.Fatalf("unexpected first up browse: changed=%v line=%q draft=%q index=%d", changed, string(line), string(draft), index)
	}
	line, draft, index, changed = browseRawInputHistory(history, line, draft, index, -1)
	if !changed || string(line) != "second" || string(draft) != "draft" || index != 1 {
		t.Fatalf("unexpected second up browse: changed=%v line=%q draft=%q index=%d", changed, string(line), string(draft), index)
	}
	line, draft, index, changed = browseRawInputHistory(history, line, draft, index, 1)
	if !changed || string(line) != "third" || string(draft) != "draft" || index != 2 {
		t.Fatalf("unexpected first down browse: changed=%v line=%q draft=%q index=%d", changed, string(line), string(draft), index)
	}
	line, draft, index, changed = browseRawInputHistory(history, line, draft, index, 1)
	if !changed || string(line) != "draft" || draft != nil || index != -1 {
		t.Fatalf("unexpected second down browse: changed=%v line=%q draft=%v index=%d", changed, string(line), draft, index)
	}
}

func TestReadRawInputEventsUsesArrowEscapeSequenceForHistoryBrowse(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe error: %v", err)
	}
	defer r.Close()
	var out bytes.Buffer
	events := readRawInputEvents(ctx, r, &out, stubRawInputController{}, nil)
	if _, err := w.Write([]byte("first")); err != nil {
		t.Fatalf("write content: %v", err)
	}
	time.Sleep(pasteBurstCharInterval + 2*time.Millisecond)
	if _, err := w.Write([]byte{'\r'}); err != nil {
		t.Fatalf("write submit: %v", err)
	}
	ev := nextNonDraftEvent(t, events)
	if ev.kind != inputEventLine || ev.line != "first" {
		t.Fatalf("expected first submitted line, got %#v", ev)
	}
	if _, err := w.Write([]byte("second")); err != nil {
		t.Fatalf("write second content: %v", err)
	}
	time.Sleep(pasteBurstCharInterval + 2*time.Millisecond)
	if _, err := w.Write([]byte{0x1b, '[', 'A', '\r'}); err != nil {
		t.Fatalf("write history up sequence: %v", err)
	}
	ev = nextNonDraftEvent(t, events)
	if ev.kind != inputEventLine || ev.line != "first" {
		t.Fatalf("expected up arrow to recall first line, got %#v", ev)
	}
	_ = w.Close()
}

func TestReadRawInputEventsIgnoresSplitArrowEscapeSequence(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe error: %v", err)
	}
	defer r.Close()
	events := readRawInputEvents(ctx, r, io.Discard, stubRawInputController{}, nil)
	parts := [][]byte{{0x1b}, {'['}, {'A'}}
	for _, part := range parts {
		if _, err := w.Write(part); err != nil {
			t.Fatalf("write arrow chunk: %v", err)
		}
		select {
		case ev := <-events:
			t.Fatalf("expected split arrow chunk ignored, got %#v", ev)
		case <-time.After(50 * time.Millisecond):
		}
	}
	cancel()
	_ = w.Close()
}

func TestReadRawInputEventsArrowWithoutHistoryProducesNoEvent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe error: %v", err)
	}
	defer r.Close()
	events := readRawInputEvents(ctx, r, io.Discard, stubRawInputController{}, nil)
	if _, err := w.Write([]byte{0x1b, '[', 'A'}); err != nil {
		t.Fatalf("write arrow sequence: %v", err)
	}
	select {
	case ev := <-events:
		t.Fatalf("expected empty-history arrow sequence ignored, got %#v", ev)
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	_ = w.Close()
}

func TestReadRawInputEventsBackspaceRedrawsWideRunes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe error: %v", err)
	}
	defer r.Close()
	var out bytes.Buffer
	events := readRawInputEvents(ctx, r, &out, stubRawInputController{}, nil)
	defer func() {
		cancel()
		_ = w.Close()
		for range events {
		}
	}()

	if _, err := w.Write([]byte("ＡＢ")); err != nil {
		t.Fatalf("write wide runes: %v", err)
	}
	if _, err := w.Write([]byte{0x7f, 0x7f, '\r'}); err != nil {
		t.Fatalf("write backspaces: %v", err)
	}
	ev := nextNonDraftEvent(t, events)
	if ev.kind != inputEventLine || ev.line != "" {
		t.Fatalf("expected empty submitted line after deleting all runes, got %#v", ev)
	}

	got := out.String()
	if got != "" {
		t.Fatalf("raw input reader must not paint backspace redraws directly, got %q", got)
	}
}

func TestReadRawInputEventsBackspaceToEmptyEmitsEmptyDraft(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe error: %v", err)
	}
	defer r.Close()
	events := readRawInputEvents(ctx, r, io.Discard, stubRawInputController{}, nil)
	defer func() {
		cancel()
		_ = w.Close()
		for range events {
		}
	}()

	if _, err := w.Write([]byte("a")); err != nil {
		t.Fatalf("write char: %v", err)
	}
	time.Sleep(pasteBurstCharInterval + 2*time.Millisecond)

	var sawTypedDraft bool
	for !sawTypedDraft {
		ev := <-events
		if ev.kind == inputEventDraft && ev.draft == "a" {
			sawTypedDraft = true
		}
	}

	if _, err := w.Write([]byte{0x7f}); err != nil {
		t.Fatalf("write backspace: %v", err)
	}
	for {
		ev := <-events
		if ev.kind == inputEventDraft {
			if ev.draft != "" {
				t.Fatalf("expected empty draft after deleting last char, got %#v", ev)
			}
			return
		}
	}
}

func TestReadRawInputEventsLeftRightArrowsMoveCursorForMidLineInsert(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe error: %v", err)
	}
	defer r.Close()
	var out bytes.Buffer
	events := readRawInputEvents(ctx, r, &out, stubRawInputController{}, nil)
	resultCh := make(chan inputEvent, 1)
	go func() {
		for ev := range events {
			if ev.kind == inputEventLine {
				resultCh <- ev
				return
			}
		}
	}()

	if _, err := w.Write([]byte("ac")); err != nil {
		t.Fatalf("write seed text: %v", err)
	}
	time.Sleep(pasteBurstCharInterval + 2*time.Millisecond)
	if _, err := w.Write([]byte{0x1b, '[', 'D'}); err != nil {
		t.Fatalf("write left arrow: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	if _, err := w.Write([]byte("b")); err != nil {
		t.Fatalf("write inserted rune: %v", err)
	}
	time.Sleep(pasteBurstCharInterval + 2*time.Millisecond)
	if _, err := w.Write([]byte{'\r'}); err != nil {
		t.Fatalf("write enter: %v", err)
	}

	ev := <-resultCh
	if ev.kind != inputEventLine || ev.line != "abc" {
		t.Fatalf("expected mid-line insert result abc, got %#v", ev)
	}
	_ = w.Close()

	got := out.String()
	if got != "" {
		t.Fatalf("raw input reader must not paint mid-line edits directly, got %q", got)
	}
}

func TestReadRawInputEventsSeededSecondPasteStillAllowsArrowAndBackspaceEditing(t *testing.T) {
	drainInteractiveInputSeedQueue()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe error: %v", err)
	}
	defer r.Close()
	var out bytes.Buffer
	events := readRawInputEvents(ctx, r, &out, stubRawInputController{}, nil)
	defer func() {
		_ = w.Close()
		for range events {
		}
	}()

	if _, err := w.Write([]byte("first")); err != nil {
		t.Fatalf("write initial text: %v", err)
	}
	time.Sleep(pasteBurstCharInterval + 2*time.Millisecond)

	seedInteractiveInput("second", seedCursorEnd)

	var sawSeed bool
	deadline := time.After(time.Second)
	for !sawSeed {
		select {
		case ev := <-events:
			if ev.kind == inputEventDraft && ev.draft == "second" {
				sawSeed = true
			}
		case <-deadline:
			t.Fatal("timed out waiting for seeded draft")
		}
	}

	if _, err := w.Write([]byte{0x1b, '[', 'D', 0x7f, '\r'}); err != nil {
		t.Fatalf("write left+backspace+enter: %v", err)
	}

	for {
		select {
		case ev := <-events:
			if ev.kind == inputEventLine {
				if ev.line != "secod" {
					t.Fatalf("expected left+backspace after second seed to submit %q, got %#v", "secod", ev)
				}
				return
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for edited seeded submission")
		}
	}
}

func TestRedrawRawInputBufferDoesNotWriteScreenInComposerMode(t *testing.T) {
	var out bytes.Buffer
	redrawRawInputBuffer(&out, []rune("old"), len([]rune("old")), []rune("new pasted text"), len([]rune("new pasted text")))
	if out.Len() != 0 {
		t.Fatalf("raw input reader must not paint composer text directly, got %q", out.String())
	}
}

func TestReadRawInputEventsEmitsBracketedPasteEvent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe error: %v", err)
	}
	defer r.Close()
	events := readRawInputEvents(ctx, r, io.Discard, stubRawInputController{}, nil)
	if _, err := w.Write([]byte{0x1b, '[', '2', '0', '0', '~'}); err != nil {
		t.Fatalf("write paste start: %v", err)
	}
	if _, err := w.Write([]byte("line1\nline2")); err != nil {
		t.Fatalf("write paste content: %v", err)
	}
	if _, err := w.Write([]byte{0x1b, '[', '2', '0', '1', '~'}); err != nil {
		t.Fatalf("write paste end: %v", err)
	}
	ev := <-events
	if ev.kind != inputEventPaste || ev.paste != "line1\nline2" {
		t.Fatalf("expected paste event, got %#v", ev)
	}
	_ = w.Close()
}

func TestReadRawInputEventsPreservesTrailingBracketedPasteNewlines(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe error: %v", err)
	}
	defer r.Close()
	events := readRawInputEvents(ctx, r, io.Discard, stubRawInputController{}, nil)
	if _, err := w.Write([]byte("\x1b[200~line1\rline2\rline3\r\x1b[201~")); err != nil {
		t.Fatalf("write paste: %v", err)
	}

	ev := <-events
	want := "line1\nline2\nline3\n"
	if ev.kind != inputEventPaste || ev.paste != want {
		t.Fatalf("expected newline-preserving paste event, got %#v", ev)
	}
	_ = w.Close()
}

func TestReadRawInputEventsSanitizesBracketedPaste(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe error: %v", err)
	}
	defer r.Close()
	events := readRawInputEvents(ctx, r, io.Discard, stubRawInputController{}, nil)
	if _, err := w.Write([]byte("\x1b[200~hello\x1b[<35;48;27M[<35;48;27Mworld\x1b[201~")); err != nil {
		t.Fatalf("write paste: %v", err)
	}
	ev := <-events
	if ev.kind != inputEventPaste || ev.paste != "helloworld" {
		t.Fatalf("expected sanitized paste event, got %#v", ev)
	}
	_ = w.Close()
}

func TestReadRawInputEventsFastLargeCharBurstBecomesPasteEvent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe error: %v", err)
	}
	defer r.Close()

	events := readRawInputEvents(ctx, r, io.Discard, stubRawInputController{}, nil)
	large := strings.Repeat("x", largePasteCharThreshold+5)
	if _, err := w.Write([]byte(large)); err != nil {
		t.Fatalf("write burst content: %v", err)
	}

	select {
	case ev := <-events:
		if ev.kind != inputEventPaste || ev.paste != large {
			t.Fatalf("expected large char burst to flush as paste event, got %#v", ev)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected paste burst event")
	}

	_ = w.Close()
}

func TestReadRawInputEventsRepeatedShortChineseBurstsStayLinear(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe error: %v", err)
	}
	defer r.Close()

	events := readRawInputEvents(ctx, r, io.Discard, stubRawInputController{}, nil)
	phrase := "我是中国人"
	want := strings.Repeat(phrase, 4)

	for i := 0; i < 4; i++ {
		if _, err := w.Write([]byte(phrase)); err != nil {
			t.Fatalf("write chinese burst %d: %v", i+1, err)
		}
		time.Sleep(30 * time.Millisecond)
	}
	_ = w.Close()

	var gotDraft string
	for ev := range events {
		if ev.kind == inputEventPaste {
			t.Fatalf("expected repeated short Chinese bursts to stay in draft updates, got paste event %#v with draft %q", ev, gotDraft)
		}
		if ev.kind == inputEventDraft {
			gotDraft = ev.draft
		}
	}

	if gotDraft != want {
		t.Fatalf("final draft = %q, want %q", gotDraft, want)
	}
}

func TestReadRawInputEventsHistoryBrowseClearsWideRunes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe error: %v", err)
	}
	defer r.Close()
	var out bytes.Buffer
	events := readRawInputEvents(ctx, r, &out, stubRawInputController{}, nil)
	defer func() {
		cancel()
		_ = w.Close()
		for range events {
		}
	}()

	if _, err := w.Write([]byte("ＡＢ")); err != nil {
		t.Fatalf("write history seed: %v", err)
	}
	time.Sleep(pasteBurstIdleTimeout + 2*time.Millisecond)
	if _, err := w.Write([]byte("\r")); err != nil {
		t.Fatalf("write history seed enter: %v", err)
	}
	ev := nextNonDraftEvent(t, events)
	if ev.kind != inputEventLine || ev.line != "ＡＢ" {
		t.Fatalf("expected first submitted line to seed history, got %#v", ev)
	}

	if _, err := w.Write([]byte{0x1b, '[', 'A', 0x1b, '[', 'B', '\r'}); err != nil {
		t.Fatalf("write history browse sequences: %v", err)
	}
	ev = nextNonDraftEvent(t, events)
	if ev.kind != inputEventLine || ev.line != "" {
		t.Fatalf("expected empty draft restored after browsing down, got %#v", ev)
	}

	got := out.String()
	if got != "" {
		t.Fatalf("raw input reader must not paint history redraws directly, got %q", got)
	}
}

func TestFileRawInputHistoryStoreAppendAndLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "cli-input-history.txt")
	store := newRawInputHistoryStore(path)
	if store == nil {
		t.Fatal("expected file history store")
	}
	if err := store.Append("first"); err != nil {
		t.Fatalf("append first: %v", err)
	}
	if err := store.Append(" "); err != nil {
		t.Fatalf("append blank: %v", err)
	}
	if err := store.Append(" second "); err != nil {
		t.Fatalf("append second: %v", err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatalf("load history: %v", err)
	}
	want := []string{"first", " second "}
	if len(got) != len(want) {
		t.Fatalf("history len=%d want=%d history=%#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("history[%d]=%q want=%q history=%#v", i, got[i], want[i], got)
		}
	}
}

func TestFileRawInputHistoryStoreRestoresStructuredComposerMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "cli-input-history.txt")
	store := newRawInputHistoryStore(path)
	structured, ok := store.(structuredRawInputHistoryStore)
	if !ok {
		t.Fatal("expected structured file history store")
	}
	line := "inspect [Image #1] [Pasted Content 1200 chars]"
	if err := store.Append(line); err != nil {
		t.Fatalf("append history: %v", err)
	}
	want := rawInputHistoryEntry{
		Text:          line,
		Attachments:   []InputAttachment{{Path: "/tmp/clip.png", MIMEType: "image/png", Label: "clip.png"}},
		PendingPastes: []PendingPaste{{ID: "paste-1", Content: strings.Repeat("x", 1200), Placeholder: "[Pasted Content 1200 chars]"}},
	}
	if err := structured.Enrich(0, want); err != nil {
		t.Fatalf("enrich history: %v", err)
	}

	entries, err := structured.LoadEntries()
	if err != nil {
		t.Fatalf("reload structured history: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("history len=%d want=1: %#v", len(entries), entries)
	}
	got := entries[0]
	if got.Text != want.Text || len(got.Attachments) != 1 || got.Attachments[0] != want.Attachments[0] {
		t.Fatalf("restored entry attachment mismatch: got=%#v want=%#v", got, want)
	}
	if len(got.PendingPastes) != 1 || got.PendingPastes[0] != want.PendingPastes[0] {
		t.Fatalf("restored entry paste mismatch: got=%#v want=%#v", got, want)
	}

	// The public legacy loader still exposes one visible history line rather
	// than leaking the appended metadata record as a second composer item.
	plain, err := store.Load()
	if err != nil || len(plain) != 1 || plain[0] != line {
		t.Fatalf("legacy history view=%#v err=%v", plain, err)
	}
}

func TestReadRawInputEventsHistoryRecallCarriesStructuredMetadata(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	path := filepath.Join(t.TempDir(), "cli-input-history.txt")
	store := newRawInputHistoryStore(path)
	line := "inspect [Image #1] [Pasted Content 1201 chars]"
	if err := store.Append(line); err != nil {
		t.Fatalf("append history: %v", err)
	}
	wantAttachment := InputAttachment{Path: "/tmp/clip.png", MIMEType: "image/png"}
	wantPaste := PendingPaste{ID: "paste-1", Content: strings.Repeat("z", 1201), Placeholder: "[Pasted Content 1201 chars]"}
	if err := store.(structuredRawInputHistoryStore).Enrich(0, rawInputHistoryEntry{
		Text: line, Attachments: []InputAttachment{wantAttachment}, PendingPastes: []PendingPaste{wantPaste},
	}); err != nil {
		t.Fatalf("enrich history: %v", err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer r.Close()
	events := readRawInputEvents(ctx, r, io.Discard, stubRawInputController{}, store)
	if _, err := w.Write([]byte{0x1b, '[', 'A'}); err != nil {
		t.Fatalf("write up arrow: %v", err)
	}
	select {
	case ev := <-events:
		if ev.kind != inputEventDraft || !ev.historyNavigation || ev.historyIndex != 0 || ev.draft != line {
			t.Fatalf("unexpected history recall event: %#v", ev)
		}
		if len(ev.historyEntry.Attachments) != 1 || ev.historyEntry.Attachments[0] != wantAttachment ||
			len(ev.historyEntry.PendingPastes) != 1 || ev.historyEntry.PendingPastes[0] != wantPaste {
			t.Fatalf("history recall lost structured metadata: %#v", ev.historyEntry)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for structured history recall")
	}
	_ = w.Close()
}

func TestReadRawInputEventsLoadsPersistedHistoryAcrossSessions(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	path := filepath.Join(t.TempDir(), "state", "cli-input-history.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir history dir: %v", err)
	}
	if err := os.WriteFile(path, []byte("persisted\n"), 0o600); err != nil {
		t.Fatalf("seed history file: %v", err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe error: %v", err)
	}
	defer r.Close()
	events := readRawInputEvents(ctx, r, io.Discard, stubRawInputController{}, newRawInputHistoryStore(path))
	if _, err := w.Write([]byte{0x1b, '[', 'A', '\r'}); err != nil {
		t.Fatalf("write persisted history browse: %v", err)
	}
	ev := nextNonDraftEvent(t, events)
	if ev.kind != inputEventLine || ev.line != "persisted" {
		t.Fatalf("expected persisted history replay, got %#v", ev)
	}
	_ = w.Close()
	cancel()
	for range events {
	}
}

func TestLineStart(t *testing.T) {
	tests := []struct {
		name   string
		line   string
		cursor int
		want   int
	}{
		{"empty line cursor 0", "", 0, 0},
		{"single line cursor at start", "hello", 0, 0},
		{"single line cursor mid", "hello", 2, 0},
		{"single line cursor at end", "hello", 5, 0},
		{"two lines cursor on first", "ab\ncd", 0, 0},
		{"two lines cursor on first mid", "ab\ncd", 1, 0},
		{"two lines cursor at newline", "ab\ncd", 2, 0},
		{"two lines cursor on second line start", "ab\ncd", 3, 3},
		{"two lines cursor on second line mid", "ab\ncd", 4, 3},
		{"three lines cursor on middle", "a\nbc\ndef", 4, 2},
		{"three lines cursor on last", "a\nbc\ndef", 5, 5},
		{"cursor past end clamped", "abc", 100, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := lineStart([]rune(tt.line), tt.cursor)
			if got != tt.want {
				t.Fatalf("lineStart(%q, %d) = %d, want %d", tt.line, tt.cursor, got, tt.want)
			}
		})
	}
}

func TestCursorUp(t *testing.T) {
	tests := []struct {
		name    string
		line    string
		cursor  int
		wantCur int
		wantOk  bool
	}{
		{"single line", "hello", 2, 2, false},
		{"two lines cursor on second, same col", "ab\ncd", 4, 1, true},
		{"two lines cursor on second, longer col clamped", "abc\nd", 4, 0, true},
		{"two lines cursor on first line", "ab\ncd", 1, 1, false},
		{"three lines middle to first", "a\nbc\ndef", 4, 1, true},
		{"empty line", "", 0, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCur, gotOk := cursorUp([]rune(tt.line), tt.cursor, 0)
			if gotCur != tt.wantCur || gotOk != tt.wantOk {
				t.Fatalf("cursorUp(%q, %d) = (%d, %v), want (%d, %v)", tt.line, tt.cursor, gotCur, gotOk, tt.wantCur, tt.wantOk)
			}
		})
	}
}

func TestCursorDown(t *testing.T) {
	tests := []struct {
		name    string
		line    string
		cursor  int
		wantCur int
		wantOk  bool
	}{
		{"single line", "hello", 2, 2, false},
		{"two lines cursor on first, same col", "ab\ncd", 1, 4, true},
		{"two lines cursor on first, longer col clamped", "abc\nd", 2, 5, true},
		{"two lines cursor on second line", "ab\ncd", 4, 4, false},
		{"three lines first to middle", "a\nbc\ndef", 1, 3, true},
		{"three lines last line no-op", "a\nbc\ndef", 5, 5, false},
		{"empty line", "", 0, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCur, gotOk := cursorDown([]rune(tt.line), tt.cursor, 0)
			if gotCur != tt.wantCur || gotOk != tt.wantOk {
				t.Fatalf("cursorDown(%q, %d) = (%d, %v), want (%d, %v)", tt.line, tt.cursor, gotCur, gotOk, tt.wantCur, tt.wantOk)
			}
		})
	}
}

func TestCursorUpWithSoftWrap(t *testing.T) {
	tests := []struct {
		name    string
		line    string
		width   int
		cursor  int
		wantCur int
		wantOk  bool
	}{
		{"single line shorter than width", "hello", 10, 2, 2, false},
		{"single line wrapped, move up from second visual line", "abcdefghij", 3, 4, 1, true},
		{"single line wrapped, cursor on first visual line", "abcdefghij", 3, 1, 1, false},
		{"single line wrapped, move from third to second", "abcdefghij", 3, 7, 4, true},
		{"single line wrapped, move from last line", "abcdefghij", 3, 9, 6, true},
		{"hard newline still works with width", "ab\ncd", 10, 4, 1, true},
		{"mixed hard and soft, move up within hard line", "abc\ndefghijkl", 3, 8, 5, true},
		{"empty line", "", 10, 0, 0, false},
		{"width=0 falls back to hard newlines only", "abcdefghij", 0, 4, 4, false},
		{"width=0 with hard newlines", "ab\ncd", 0, 4, 1, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCur, gotOk := cursorUp([]rune(tt.line), tt.cursor, tt.width)
			if gotCur != tt.wantCur || gotOk != tt.wantOk {
				t.Fatalf("cursorUp(%q, %d, %d) = (%d, %v), want (%d, %v)", tt.line, tt.cursor, tt.width, gotCur, gotOk, tt.wantCur, tt.wantOk)
			}
		})
	}
}

func TestCursorDownWithSoftWrap(t *testing.T) {
	tests := []struct {
		name    string
		line    string
		width   int
		cursor  int
		wantCur int
		wantOk  bool
	}{
		{"single line shorter than width", "hello", 10, 2, 2, false},
		{"single line wrapped, move down from first visual line", "abcdefghij", 3, 1, 4, true},
		{"single line wrapped, cursor on last visual line", "abcdefghij", 3, 10, 10, false},
		{"single line wrapped, move from second to third", "abcdefghij", 3, 4, 7, true},
		{"hard newline still works with width", "ab\ncd", 10, 1, 4, true},
		{"mixed hard and soft, move down across hard line", "abc\ndefghijkl", 3, 1, 5, true},
		{"empty line", "", 10, 0, 0, false},
		{"width=0 falls back to hard newlines only", "abcdefghij", 0, 1, 1, false},
		{"width=0 with hard newlines", "ab\ncd", 0, 1, 4, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCur, gotOk := cursorDown([]rune(tt.line), tt.cursor, tt.width)
			if gotCur != tt.wantCur || gotOk != tt.wantOk {
				t.Fatalf("cursorDown(%q, %d, %d) = (%d, %v), want (%d, %v)", tt.line, tt.cursor, tt.width, gotCur, gotOk, tt.wantCur, tt.wantOk)
			}
		})
	}
}

// TestReadRawInputEventsUpArrowMovesCursorInMultiLine verifies that pressing
// Up inside a multi-line composer moves the cursor up one visual line instead
// of immediately jumping to history.
func TestReadRawInputEventsUpArrowMovesCursorInMultiLine(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe error: %v", err)
	}
	defer r.Close()
	var out bytes.Buffer
	events := readRawInputEvents(ctx, r, &out, stubRawInputController{}, nil)
	defer func() {
		cancel()
		_ = w.Close()
		for range events {
		}
	}()

	// Use seed to set multi-line text directly, avoiding paste-burst timing issues.
	seedInteractiveInput("ab\ncd", seedCursorEnd)

	// Wait for the seeded draft at cursor position 5 (end).
	var sawSeed bool
	deadline := time.After(time.Second)
	for !sawSeed {
		select {
		case ev := <-events:
			if ev.kind == inputEventDraft && ev.draft == "ab\ncd" {
				sawSeed = true
				if ev.cursor != 5 {
					t.Fatalf("seeded draft cursor = %d, want 5", ev.cursor)
				}
			}
		case <-deadline:
			t.Fatal("timed out waiting for seeded draft")
		}
	}

	// Drain any additional events.
	for {
		select {
		case <-events:
		case <-time.After(50 * time.Millisecond):
			goto drained
		}
	}
drained:

	// Press Up arrow — should move cursor to first line, not history
	if _, err := w.Write([]byte{0x1b, '[', 'A'}); err != nil {
		t.Fatalf("write up arrow: %v", err)
	}
	select {
	case ev := <-events:
		if ev.kind == inputEventHotkey {
			t.Fatalf("up arrow should move cursor in multi-line text, not emit hotkey %q", ev.hotkey)
		}
		if ev.kind == inputEventDraft {
			if ev.cursor > 2 {
				t.Fatalf("cursor should be in first line (≤2), got cursor=%d draft=%q", ev.cursor, ev.draft)
			}
		} else {
			t.Fatalf("expected draft event from cursor movement, got %#v", ev)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("expected up arrow cursor movement draft event")
	}
}

// TestReadRawInputEventsDownArrowMovesCursorInMultiLine verifies that pressing
// Down inside a multi-line composer moves the cursor down one visual line.
func TestReadRawInputEventsDownArrowMovesCursorInMultiLine(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe error: %v", err)
	}
	defer r.Close()
	var out bytes.Buffer
	events := readRawInputEvents(ctx, r, &out, stubRawInputController{}, nil)
	defer func() {
		cancel()
		_ = w.Close()
		for range events {
		}
	}()

	// Use seed to set multi-line text directly, avoiding paste-burst timing issues.
	seedInteractiveInput("ab\ncd", seedCursorEnd)

	// Wait for the seeded draft.
	var sawSeed bool
	deadline := time.After(time.Second)
	for !sawSeed {
		select {
		case ev := <-events:
			if ev.kind == inputEventDraft && ev.draft == "ab\ncd" {
				sawSeed = true
			}
		case <-deadline:
			t.Fatal("timed out waiting for seeded draft")
		}
	}

	// Move cursor left to position 2 (at the newline)
	if _, err := w.Write([]byte{0x1b, '[', 'D', 0x1b, '[', 'D', 0x1b, '[', 'D'}); err != nil {
		t.Fatalf("write left arrows: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	// Drain any draft events from left-arrow cursor moves
	for {
		select {
		case <-events:
		case <-time.After(50 * time.Millisecond):
			goto drainedLeft
		}
	}
drainedLeft:

	// Press Down arrow — should move cursor to second line
	if _, err := w.Write([]byte{0x1b, '[', 'B'}); err != nil {
		t.Fatalf("write down arrow: %v", err)
	}
	select {
	case ev := <-events:
		if ev.kind == inputEventDraft {
			if ev.cursor < 3 {
				t.Fatalf("cursor should be in second line (≥3), got cursor=%d draft=%q", ev.cursor, ev.draft)
			}
		} else {
			t.Fatalf("expected draft event from cursor movement, got %#v", ev)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("expected down arrow cursor movement draft event")
	}
}

// TestReadRawInputEventsUpAtTopLineBrowsesHistory verifies that pressing Up when
// the cursor is already on the first line falls through to history browsing.
func TestReadRawInputEventsUpAtTopLineBrowsesHistory(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe error: %v", err)
	}
	defer r.Close()
	events := readRawInputEvents(ctx, r, io.Discard, stubRawInputController{},
		testRawInputHistoryStore{history: []string{"history item"}})
	defer func() {
		cancel()
		_ = w.Close()
		for range events {
		}
	}()

	// Use seed to set multi-line text directly.
	seedInteractiveInput("ab\ncd", seedCursorEnd)

	// Wait for the seeded draft.
	var sawSeed bool
	deadline := time.After(time.Second)
	for !sawSeed {
		select {
		case ev := <-events:
			if ev.kind == inputEventDraft && ev.draft == "ab\ncd" {
				sawSeed = true
			}
		case <-deadline:
			t.Fatal("timed out waiting for seeded draft")
		}
	}

	// Move cursor to first line, position 0.
	for i := 0; i < 10; i++ {
		if _, err := w.Write([]byte{0x1b, '[', 'D'}); err != nil {
			t.Fatalf("write left arrow: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	// Drain cursor-move drafts.
	for {
		select {
		case <-events:
		case <-time.After(50 * time.Millisecond):
			goto drainedLeft
		}
	}
drainedLeft:

	// Now cursor is at 0 (first line). Press Up — should browse history.
	if _, err := w.Write([]byte{0x1b, '[', 'A'}); err != nil {
		t.Fatalf("write up arrow: %v", err)
	}

	var sawHistory bool
	deadline2 := time.After(500 * time.Millisecond)
	for !sawHistory {
		select {
		case ev := <-events:
			if ev.kind == inputEventDraft && ev.draft == "history item" {
				sawHistory = true
			}
		case <-deadline2:
			t.Fatal("up arrow at top line should have browsed to history item")
		}
	}
}

// TestReadRawInputEventsUpMovesCursorWithinMultiLineHistory verifies that after
// browsing to a multi-line history item, pressing Up moves the cursor within
// that history item instead of immediately jumping to the previous history
// entry. This is a regression test for a bug where the cursor-movement path was
// gated by `historyIndex < 0`, causing it to be skipped while browsing history.
func TestReadRawInputEventsUpMovesCursorWithinMultiLineHistory(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe error: %v", err)
	}
	defer r.Close()
	events := readRawInputEvents(ctx, r, io.Discard, stubRawInputController{},
		testRawInputHistoryStore{history: []string{"ef\ngh"}})
	defer func() {
		cancel()
		_ = w.Close()
		for range events {
		}
	}()

	seedInteractiveInput("ab\ncd", seedCursorEnd)

	var sawSeed bool
	seedDeadline := time.After(time.Second)
	for !sawSeed {
		select {
		case ev := <-events:
			if ev.kind == inputEventDraft && ev.draft == "ab\ncd" {
				sawSeed = true
			}
		case <-seedDeadline:
			t.Fatal("timed out waiting for seeded draft")
		}
	}

	// Move cursor to the first line so Up falls through to history browsing.
	for i := 0; i < 10; i++ {
		if _, err := w.Write([]byte{0x1b, '[', 'D'}); err != nil {
			t.Fatalf("write left arrow: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	for {
		select {
		case <-events:
		case <-time.After(50 * time.Millisecond):
			goto drainedLeft
		}
	}
drainedLeft:

	// Press Up — should browse to the multi-line history item "ef\ngh".
	if _, err := w.Write([]byte{0x1b, '[', 'A'}); err != nil {
		t.Fatalf("write up arrow: %v", err)
	}
	var sawHistory bool
	histDeadline := time.After(500 * time.Millisecond)
	for !sawHistory {
		select {
		case ev := <-events:
			if ev.kind == inputEventDraft && ev.draft == "ef\ngh" {
				sawHistory = true
				if ev.cursor != 5 {
					t.Fatalf("history item cursor = %d, want 5 (end of last line)", ev.cursor)
				}
			}
		case <-histDeadline:
			t.Fatal("up arrow should have browsed to multi-line history item")
		}
	}

	// Drain any extra events.
	for {
		select {
		case <-events:
		case <-time.After(50 * time.Millisecond):
			goto drainedHistory
		}
	}
drainedHistory:

	// Press Up again — the cursor is at the end of "gh" (last line of the
	// multi-line history item). It should move UP within the history text to
	// the first line, NOT jump further back in history.
	if _, err := w.Write([]byte{0x1b, '[', 'A'}); err != nil {
		t.Fatalf("write up arrow: %v", err)
	}
	select {
	case ev := <-events:
		if ev.kind != inputEventDraft {
			t.Fatalf("expected draft event, got %#v", ev)
		}
		if ev.draft != "ef\ngh" {
			t.Fatalf("draft should still be the history item %q, got %q (cursor moved to previous history instead of within the item)", "ef\ngh", ev.draft)
		}
		if ev.cursor > 2 {
			t.Fatalf("cursor should have moved to first line (≤2), got cursor=%d draft=%q", ev.cursor, ev.draft)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("expected up arrow to move cursor within multi-line history item")
	}
}

// TestReadRawInputEventsSeededCursorPreservedAtInsertionPoint is a regression
// test for a bug where pasting text in the middle of the composer moved the
// cursor to the end of the whole buffer instead of the end of the inserted
// text. The raw reader's applySeed used to hardcode cursor = len(line); it must
// instead honour the cursor carried by the seed so that both the visible cursor
// and the subsequent character insertion point land right after the paste.
func TestReadRawInputEventsSeededCursorPreservedAtInsertionPoint(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe error: %v", err)
	}
	defer r.Close()

	events := readRawInputEvents(ctx, r, io.Discard, stubRawInputController{}, nil)
	defer func() {
		cancel()
		_ = w.Close()
		for range events {
		}
	}()

	// Seed with text whose cursor sits in the middle (position 6, right after
	// the inserted "XYZ"), mirroring the composer state after a mid-buffer
	// paste of "XYZ" into "abcdefg".
	seedInteractiveInput("abcXYZdefg", 6)

	var sawSeed bool
	deadline := time.After(time.Second)
	for !sawSeed {
		select {
		case ev := <-events:
			if ev.kind == inputEventDraft && ev.draft == "abcXYZdefg" {
				sawSeed = true
				if ev.cursor != 6 {
					t.Fatalf("seeded draft cursor = %d, want 6 (end of inserted text, not %d end of buffer)", ev.cursor, len([]rune("abcXYZdefg")))
				}
			}
		case <-deadline:
			t.Fatal("timed out waiting for seeded draft")
		}
	}

	// Type a single character; it must be inserted at the preserved cursor
	// (position 6), proving the raw reader's internal cursor - not just the
	// emitted draft cursor - is correct.
	if _, err := w.Write([]byte("Q")); err != nil {
		t.Fatalf("write Q: %v", err)
	}

	var sawTyped bool
	typedDeadline := time.After(time.Second)
	for !sawTyped {
		select {
		case ev := <-events:
			if ev.kind == inputEventDraft {
				sawTyped = true
				if ev.draft != "abcXYZQdefg" {
					t.Fatalf("typed char inserted at wrong position: draft=%q want %q", ev.draft, "abcXYZQdefg")
				}
				if ev.cursor != 7 {
					t.Fatalf("cursor after typing = %d, want 7", ev.cursor)
				}
			}
		case <-typedDeadline:
			t.Fatal("timed out waiting for typed-char draft")
		}
	}
}

// The helpers below were production functions that only the tests ever
// called: each is a thin composition of live code. They live here so the
// production files carry no unused code while the tests keep exercising
// the live functions underneath.

func waitIfInteractiveInputPaused(ctx context.Context) (paused bool, ok bool) {
	for interactiveInputPaused.Load() {
		paused = true
		select {
		case <-ctx.Done():
			return paused, false
		default:
		}
		time.Sleep(time.Millisecond)
	}
	return paused, true
}

// TestRawReaderAdoptsRequestedCaretAndInsertsThere is the other half of the
// composer gesture: the renderer resolves the click, the raw input reader —
// which owns the line buffer — adopts the position, and the next keystroke
// lands there.
func TestRawReaderAdoptsRequestedCaretAndInsertsThere(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe error: %v", err)
	}
	defer pr.Close()
	defer pw.Close()
	drainCaretRequest()

	events := readRawInputEvents(ctx, pr, io.Discard, stubRawInputController{}, nil)
	if _, err := pw.Write([]byte("hello")); err != nil {
		t.Fatalf("write: %v", err)
	}
	waitForDraft(t, events, "hello", 5)

	requestComposerCaret(2)
	moved := waitForDraft(t, events, "hello", 2)
	if moved.cursor != 2 {
		t.Fatalf("reader should adopt the requested caret, got %d", moved.cursor)
	}

	if _, err := pw.Write([]byte("X")); err != nil {
		t.Fatalf("write: %v", err)
	}
	typed := waitForDraft(t, events, "heXllo", 3)
	if typed.draft != "heXllo" {
		t.Fatalf("typing after a click should insert at the caret, got %q", typed.draft)
	}
}

// TestRawReaderClampsRequestedCaretToItsBuffer covers the race the request is
// designed for: the click was resolved against a painted buffer, so a position
// past the buffer the reader holds now is clamped instead of trusted.
func TestRawReaderClampsRequestedCaretToItsBuffer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe error: %v", err)
	}
	defer pr.Close()
	defer pw.Close()
	drainCaretRequest()

	events := readRawInputEvents(ctx, pr, io.Discard, stubRawInputController{}, nil)
	if _, err := pw.Write([]byte("ab")); err != nil {
		t.Fatalf("write: %v", err)
	}
	waitForDraft(t, events, "ab", 2)

	requestComposerCaret(99)
	if _, err := pw.Write([]byte("Z")); err != nil {
		t.Fatalf("write: %v", err)
	}
	typed := waitForDraft(t, events, "abZ", 3)
	if typed.draft != "abZ" {
		t.Fatalf("an out-of-range caret must clamp to the end of the buffer, got %q", typed.draft)
	}
}

// waitForDraft reads events until a draft with the given text and cursor
// arrives.
func waitForDraft(t *testing.T, events <-chan inputEvent, draft string, cursor int) inputEvent {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatalf("input channel closed waiting for draft %q cursor %d", draft, cursor)
			}
			if ev.kind == inputEventDraft && ev.draft == draft && ev.cursor == cursor {
				return ev
			}
		case <-deadline:
			t.Fatalf("timed out waiting for draft %q cursor %d", draft, cursor)
		}
	}
}

// The composer holds a typed character for up to one burst interval before it is
// inserted, so that a fast burst is not painted character by character. Every
// path that can run during that window must hand the held character back, never
// drop it: the reader has already accepted those keystrokes. These three cases
// are the paths that used to lose one character each.

func TestPasteBurstHeldCharacterSurvivesTheNextKey(t *testing.T) {
	base := time.Now()
	var p pasteBurstState

	if got := p.onASCIIChar('x', base); got != pasteBurstCharDecisionRetainFirstChar {
		t.Fatalf("first char decision = %v, want the character to be held", got)
	}
	// The next key flushes whatever is held before it acts — Tab, a click, '/',
	// Escape and every other non-plain key take this path.
	if got := p.flushBeforeModifiedInput(); got != "x" {
		t.Fatalf("flushBeforeModifiedInput() = %q, want %q: the held character was discarded", got, "x")
	}
}

func TestPasteBurstHeldCharacterSurvivesABurstWindowReset(t *testing.T) {
	base := time.Now()
	var p pasteBurstState

	p.onASCIIChar('x', base)
	// The non-character paths reset the burst window (consecutive-char count and
	// the Enter-suppression deadline). That reset must not strand a character
	// the reader is holding, because the idle flush is the only other way it can
	// reach the composer.
	p.clearWindowAfterNonChar()

	flush := p.flushIfDue(base.Add(time.Second))
	if flush.kind != pasteBurstResultTyped || flush.text != "x" {
		t.Fatalf("flushIfDue() = %+v, want the held character typed out as %q", flush, "x")
	}
}

func TestPasteBurstSecondCharacterDoesNotEraseTheFirst(t *testing.T) {
	base := time.Now()
	var p pasteBurstState

	p.onASCIIChar('r', base)
	// Just outside the burst interval: too slow to continue a burst, so the
	// second character must not overwrite the first.
	p.onASCIIChar('e', base.Add(pasteBurstCharInterval+time.Millisecond))

	flush := p.flushIfDue(base.Add(time.Second))
	if flush.text != "re" {
		t.Fatalf("flushIfDue() = %+v, want both characters as %q", flush, "re")
	}
}

// TestReadRawInputEventsKeepsACharTypedBeforeTheRosterStopKey drives the same
// window through the real reader. The roster-stop shortcut intercepts a bare 'x'
// only while the composer is empty, and text the reader is still holding is
// typed input: it must be committed rather than dropped along with the
// shortcut's burst-window reset, and the 'x' after it must be ordinary input.
func TestReadRawInputEventsKeepsACharTypedBeforeTheRosterStopKey(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe error: %v", err)
	}
	defer r.Close()

	// The shortcut only arms while a roster row is selected.
	agentRosterStopArmed.Store(true)
	defer agentRosterStopArmed.Store(false)

	events := readRawInputEvents(ctx, r, io.Discard, stubRawInputController{}, nil)
	if _, err := w.Write([]byte("a")); err != nil { // held, not yet committed
		t.Fatalf("write char: %v", err)
	}
	if _, err := w.Write([]byte("x")); err != nil { // roster stop, if the line is empty
		t.Fatalf("write roster key: %v", err)
	}
	// Let both characters settle into the reader's line before submitting: Enter
	// pressed while input is still held is the multi-line/paste gesture, not a
	// submit.
	time.Sleep(40 * time.Millisecond)
	if _, err := w.Write([]byte("\r")); err != nil { // submit
		t.Fatalf("write enter: %v", err)
	}

	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				_ = w.Close()
				t.Fatal("channel closed before the submitted line")
			}
			if ev.kind != inputEventLine {
				continue
			}
			_ = w.Close()
			if ev.line != "ax" {
				t.Fatalf("submitted line = %q, want %q: input typed before the roster key was dropped", ev.line, "ax")
			}
			return
		case <-deadline:
			_ = w.Close()
			t.Fatal("timed out waiting for the submitted line")
		}
	}
}

// TestReadRawInputEventsSubmitsEveryCharacterWhateverTheTiming is the
// pipeline-level statement of the contract this reader owes the composer: every
// character written to the terminal reaches the submitted line, in order, at any
// typing speed. The speeds below are the ones the burst heuristic treats
// differently — an instantaneous write that reads as a paste, a fast burst, and
// a pace just inside and just outside the burst interval — so a regression in
// that machinery shows up here as a missing or reordered character.
func TestReadRawInputEventsSubmitsEveryCharacterWhateverTheTiming(t *testing.T) {
	const text = "read the sam"
	for _, gap := range []time.Duration{0, 2 * time.Millisecond, 5 * time.Millisecond, 9 * time.Millisecond} {
		t.Run(gap.String(), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatalf("pipe error: %v", err)
			}
			defer r.Close()

			events := readRawInputEvents(ctx, r, io.Discard, stubRawInputController{}, nil)
			go func() {
				for _, b := range []byte(text) {
					if _, err := w.Write([]byte{b}); err != nil {
						return
					}
					if gap > 0 {
						time.Sleep(gap)
					}
				}
				// Let the reader settle the last characters, then submit: Enter
				// pressed while input is still held inserts a newline instead.
				time.Sleep(60 * time.Millisecond)
				_, _ = w.Write([]byte("\r"))
			}()

			deadline := time.After(5 * time.Second)
			for {
				select {
				case ev, ok := <-events:
					if !ok {
						_ = w.Close()
						t.Fatal("channel closed before the submitted line")
					}
					if ev.kind != inputEventLine {
						continue
					}
					_ = w.Close()
					if ev.line != text {
						t.Fatalf("submitted line = %q, want %q", ev.line, text)
					}
					return
				case <-deadline:
					_ = w.Close()
					t.Fatal("timed out waiting for the submitted line")
				}
			}
		})
	}
}
