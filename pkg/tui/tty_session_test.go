package tui

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mattn/go-runewidth"
)

func TestClipboardImageHintLine(t *testing.T) {
	r := NewRenderer(nil, nil)
	if got := r.clipboardImageHintLine(80); got != "" {
		t.Fatalf("hint shown without an image: %q", got)
	}
	r.clipboardImage = true
	got := r.clipboardImageHintLine(80)
	if !strings.Contains(got, clipboardImageHintText) {
		t.Fatalf("hint missing: %q", got)
	}
	if w := runewidth.StringWidth(stripANSI(got)); w != 80-sharedBlockFooterTruncateExtraRoom {
		t.Fatalf("hint not right-aligned, width %d", w)
	}
	if got := r.clipboardImageHintLine(5); strings.Contains(got, "ctrl+v to paste") {
		t.Fatalf("hint not truncated: %q", got)
	}
}

// TestClipboardTypesHoldPastableImage pins that the hint promises no more than
// ctrl+v delivers: a type the reader cannot extract is not an image to paste.
func TestClipboardTypesHoldPastableImage(t *testing.T) {
	const macPNG = "«class PNGf», 1234, «class 8BPS», 99, TIFF picture, 5678"
	const macTIFF = "TIFF picture, 5678, «class utf8», 12"
	cases := []struct {
		name      string
		goos      string
		types     string
		pngpaste  bool
		wantImage bool
	}{
		{"mac png", "darwin", macPNG, false, true},
		{"mac tiff via pngpaste", "darwin", macTIFF, true, true},
		{"mac tiff without pngpaste cannot be pasted", "darwin", macTIFF, false, false},
		{"mac text", "darwin", "«class utf8», 12, string, 12", true, false},
		{"linux png", "linux", "text/plain\nimage/png\n", false, true},
		{"linux jpeg only cannot be pasted", "linux", "image/jpeg\n", false, false},
		{"linux mac type names do not count", "linux", "TIFF\nPNGf\n", false, false},
		{"windows", "windows", "image/png", true, false},
	}
	for _, tc := range cases {
		if got := clipboardTypesHoldPastableImage(tc.goos, tc.types, tc.pngpaste); got != tc.wantImage {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.wantImage)
		}
	}
}

type countingClipboardProbe struct {
	calls atomic.Int32
	image atomic.Bool
}

func (p *countingClipboardProbe) ClipboardHasImage(context.Context) bool {
	p.calls.Add(1)
	return p.image.Load()
}

func clipboardHintState(r *Renderer) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.clipboardImage
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func startClipboardWatcher(t *testing.T, probe ClipboardImageProbe, interval time.Duration) (*clipboardImageWatcher, *Renderer) {
	t.Helper()
	r := NewRenderer(nil, nil)
	w := newClipboardImageWatcher(probe, r)
	w.interval = interval
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return w, r
}

// TestClipboardWatcherProbesOnlyWhileFocused pins that the watcher spawns no
// probe while the terminal is in the background.
func TestClipboardWatcherProbesOnlyWhileFocused(t *testing.T) {
	probe := &countingClipboardProbe{}
	probe.image.Store(true)
	w, r := startClipboardWatcher(t, probe, 5*time.Millisecond)
	eventually(t, "the focused watcher to show the hint", func() bool { return clipboardHintState(r) })

	w.SetFocused(false)
	time.Sleep(20 * time.Millisecond) // let a probe already in flight finish
	before := probe.calls.Load()
	time.Sleep(60 * time.Millisecond)
	if after := probe.calls.Load(); after != before {
		t.Fatalf("watcher probed %d times while unfocused", after-before)
	}
}

// TestLookupSessionTitleReadsByID pins that the terminal title reads a
// session's name by its id rather than by scanning a recent list: a
// conversation older than any recent list still has its name, and an unnamed
// one still reads as none.
func TestLookupSessionTitleReadsByID(t *testing.T) {
	session := &fakeSession{
		recent: make([]SessionSummary, 100),
		titles: map[string]string{"old": "An old conversation"},
	}
	for i := range session.recent {
		session.recent[i] = SessionSummary{ID: fmt.Sprintf("newer-%03d", i), Title: "newer", UpdatedAt: int64(1000 - i)}
	}

	if got := lookupSessionTitle(context.Background(), session, "old"); got != "An old conversation" {
		t.Fatalf("title = %q, want the older conversation's own title", got)
	}
	// An id the store has no title for — unnamed or unknown alike — reads
	// as no title at all, the same contract SessionTitle has.
	if got := lookupSessionTitle(context.Background(), session, "absent"); got != "" {
		t.Fatalf("title = %q, want empty", got)
	}
}

// TestClipboardWatcherProbesAtOnceWhenFocusReturns pins that regaining focus
// probes immediately rather than on the next tick, so the hint is current the
// moment it can be seen.
func TestClipboardWatcherProbesAtOnceWhenFocusReturns(t *testing.T) {
	probe := &countingClipboardProbe{}
	probe.image.Store(true)
	w, r := startClipboardWatcher(t, probe, time.Hour) // no tick fires in this test
	eventually(t, "the first probe to show the hint", func() bool { return clipboardHintState(r) })

	w.SetFocused(false)
	probe.image.Store(false)
	w.SetFocused(true)
	eventually(t, "regaining focus to clear the hint", func() bool { return !clipboardHintState(r) })
}
