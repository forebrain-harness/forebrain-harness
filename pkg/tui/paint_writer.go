package tui

// asyncPaintWriter decouples viewport paint writes from the input pipeline.
//
// Root cause it fixes: paintViewportLocked ran on the caller's goroutine —
// the main event loop for every wheel notch, keystroke and streaming delta —
// and wrote the frame to the terminal synchronously while holding r.mu. A
// terminal that stops draining its pty (iTerm2 and friends pause reading
// while they render a momentum scroll through a long scrollback) fills the
// kernel pty buffer, and that blocked Write froze the whole surface: keys,
// wheel and clicks all queue behind r.mu until the terminal catches up. The
// freeze reported as "wheel to the top of a running subagent's view makes
// everything dead for seconds" — a running subagent keeps repainting (task
// clocks, spinner, streamed deltas) so the pty stays saturated, which is why
// an idle primary view never showed it.
//
// Frames are idempotent full diffs, so the writer may drop a frame that a
// newer one has already superseded; the renderer notices via consumeDrop and
// discards its paint shadow so the next written frame is a full repaint.
// Control-sequence writes that must not be dropped (alt-screen switches,
// cursor visibility, OSC 52) go through flush first: the caller waits until
// every submitted frame is on the wire, then writes in order.
//
// The writer is only active when the renderer paints to a real terminal; a
// bytes.Buffer keeps the historical synchronous path so tests observe painted
// bytes the moment paint returns.

import (
	"fmt"
	"io"
	"os"
	"sync"

	"golang.org/x/term"
)

type asyncPaintWriter struct {
	mu      sync.Mutex
	cond    *sync.Cond
	out     io.Writer
	pending []byte // newest frame not yet picked up by the writer goroutine
	writing bool   // writer goroutine is inside out.Write
	dropped bool   // a frame was superseded before being written
	stopped bool
	done    chan struct{}
}

func newAsyncPaintWriter(out io.Writer) *asyncPaintWriter {
	w := &asyncPaintWriter{out: out, done: make(chan struct{})}
	w.cond = sync.NewCond(&w.mu)
	go w.loop()
	return w
}

// submit offers the newest frame, replacing one that has not started writing.
// It never blocks, so the event loop stays live no matter how slow the
// terminal drains.
func (w *asyncPaintWriter) submit(frame []byte) {
	w.mu.Lock()
	if w.stopped {
		w.mu.Unlock()
		_, _ = w.out.Write(frame)
		return
	}
	if w.pending != nil {
		w.dropped = true
	}
	w.pending = frame
	w.cond.Signal()
	w.mu.Unlock()
}

// consumeDrop reports and clears whether a frame was dropped since the last
// call. The caller answers by discarding its paint shadow.
func (w *asyncPaintWriter) consumeDrop() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	d := w.dropped
	w.dropped = false
	return d
}

// flush returns once every submitted frame has been fully written. It is the
// ordering barrier before control-sequence writes that must not overtake a
// pending frame; under a terminal stall it waits for the drain, by design.
func (w *asyncPaintWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for (w.pending != nil || w.writing) && !w.stopped {
		w.cond.Wait()
	}
}

// stop drains pending frames and waits for the writer goroutine to exit.
func (w *asyncPaintWriter) stop() {
	w.flush()
	w.mu.Lock()
	w.stopped = true
	w.cond.Broadcast()
	w.mu.Unlock()
	<-w.done
}

func (w *asyncPaintWriter) loop() {
	defer close(w.done)
	for {
		w.mu.Lock()
		for w.pending == nil && !w.stopped {
			w.cond.Wait()
		}
		if w.pending == nil && w.stopped {
			w.cond.Broadcast()
			w.mu.Unlock()
			return
		}
		frame := w.pending
		w.pending = nil
		w.writing = true
		w.mu.Unlock()

		// May block for as long as the terminal stalls; only this goroutine
		// waits, never the input pipeline.
		_, _ = w.out.Write(frame)

		w.mu.Lock()
		w.writing = false
		w.cond.Broadcast()
		w.mu.Unlock()
	}
}

// writeTTYOrderedLocked writes bytes that must reach the terminal in order
// with paint frames and must never be dropped: flush() first, then write.
// Caller holds r.mu; flush may wait for a stalled terminal to drain, which is
// the price of ordering and only applies to these rare control writes.
func (r *Renderer) writeTTYOrderedLocked(format string, args ...any) {
	if r.asyncPaint != nil {
		r.asyncPaint.flush()
	}
	_, _ = fmt.Fprintf(r.out, format, args...)
}

// startAsyncPaintWriterLocked activates the asynchronous paint writer when the
// renderer paints to a real terminal. Caller holds r.mu.
func (r *Renderer) startAsyncPaintWriterLocked() {
	if r.asyncPaint != nil || r.out == nil {
		return
	}
	inner := io.Writer(r.out)
	if sw, ok := r.out.(*syncWriter); ok {
		inner = sw.w
	}
	if nw, ok := inner.(*ttyNewlineWriter); ok {
		inner = nw.dst
	}
	if f, ok := inner.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		r.asyncPaint = newAsyncPaintWriter(r.out)
	}
}

// stopAsyncPaintWriterLocked drains and retires the writer. Teardown only:
// the last frame must be on the wire before mode-switch escapes are written.
// Caller holds r.mu.
func (r *Renderer) stopAsyncPaintWriterLocked() {
	w := r.asyncPaint
	if w == nil {
		return
	}
	r.asyncPaint = nil
	w.stop()
}
