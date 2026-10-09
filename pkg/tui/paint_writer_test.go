package tui

import (
	"bytes"
	"sync"
	"testing"
)

// gatingWriter lets a test hold the writer goroutine inside a Write for as
// long as it likes, playing the role of a terminal that stopped draining.
type gatingWriter struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	entered  chan struct{}
	release  chan struct{}
	blocking bool
}

func (g *gatingWriter) Write(p []byte) (int, error) {
	if g.blocking {
		g.entered <- struct{}{}
		<-g.release
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.buf.Write(p)
}

func (g *gatingWriter) bytes() []byte {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]byte(nil), g.buf.Bytes()...)
}

// A frame that arrives while the writer is stuck inside a slow Write is
// replaced by the newer one, and the drop is observable.
func TestAsyncPaintWriterDropsSupersededFrame(t *testing.T) {
	g := &gatingWriter{entered: make(chan struct{}, 1), release: make(chan struct{}), blocking: true}
	w := newAsyncPaintWriter(g)

	w.submit([]byte("frame-1"))
	<-g.entered // writer is now blocked writing frame-1
	w.submit([]byte("frame-2"))
	w.submit([]byte("frame-3")) // supersedes frame-2 before it is picked up

	if !w.consumeDrop() {
		t.Fatal("frame-2 was dropped but consumeDrop reported nothing")
	}
	if w.consumeDrop() {
		t.Fatal("consumeDrop must clear the flag")
	}

	close(g.release)
	w.stop()
	got := string(g.bytes())
	if got != "frame-1frame-3" {
		t.Fatalf("written %q, want frame-1 then the superseding frame-3", got)
	}
}

// flush returns only once every submitted frame is fully written, so an
// ordered control write that follows cannot overtake a pending frame.
func TestAsyncPaintWriterFlushOrdersControlWrites(t *testing.T) {
	g := &gatingWriter{entered: make(chan struct{}, 1), release: make(chan struct{}), blocking: true}
	w := newAsyncPaintWriter(g)

	w.submit([]byte("paint"))
	<-g.entered
	// Slow the first write down, then let it finish while flush is waiting.
	close(g.release)

	w.flush()
	if got := string(g.bytes()); got != "paint" {
		t.Fatalf("after flush the terminal holds %q, want %q", got, "paint")
	}
	w.stop()
}

// stop drains whatever is pending before retiring the writer, so teardown
// never loses the last frame.
func TestAsyncPaintWriterStopDrainsPending(t *testing.T) {
	var mu sync.Mutex
	var buf bytes.Buffer
	out := funcWriter(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return buf.Write(p)
	})
	w := newAsyncPaintWriter(out)
	w.submit([]byte("last-frame"))
	w.stop()
	mu.Lock()
	got := buf.String()
	mu.Unlock()
	if got != "last-frame" {
		t.Fatalf("terminal holds %q after stop, want the last frame", got)
	}
	// A submit after stop falls back to a synchronous write instead of
	// being lost.
	w.submit([]byte("+late"))
	mu.Lock()
	got = buf.String()
	mu.Unlock()
	if got != "last-frame+late" {
		t.Fatalf("terminal holds %q after late submit, want %q", got, "last-frame+late")
	}
}

// The renderer keeps its historical synchronous behaviour for non-terminal
// writers: no async painter is started, so tests observe bytes immediately.
func TestRendererKeepsSynchronousPaintForBuffers(t *testing.T) {
	var out bytes.Buffer
	r := NewRenderer(&out, &out)
	r.EnableViewportMode()
	t.Cleanup(r.DisableViewportMode)
	if r.asyncPaint != nil {
		t.Fatal("async painter activated for a bytes.Buffer output")
	}
	r.RenderFrame(Frame{Kind: FrameAssistant, Content: "hello", Final: true})
	if !bytes.Contains(out.Bytes(), []byte("hello")) {
		t.Fatal("synchronous buffer paint lost the frame")
	}
}

type funcWriter func([]byte) (int, error)

func (f funcWriter) Write(p []byte) (int, error) { return f(p) }
