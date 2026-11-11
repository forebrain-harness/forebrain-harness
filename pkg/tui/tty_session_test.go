package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mattn/go-runewidth"
)

// TestClipboardImageReaderWritesIntoTheProjectAttachments pins that a pasted
// image is stored with the project, under <project root>/.forebrain/attachments/,
// rather than in the system temp directory or loose in the project root.
func TestClipboardImageReaderWritesIntoTheProjectAttachments(t *testing.T) {
	prev := clipboardWritePNG
	t.Cleanup(func() { clipboardWritePNG = prev })
	clipboardWritePNG = func(_ context.Context, path string) error {
		return os.WriteFile(path, []byte("png-bytes"), 0o600)
	}
	root := t.TempDir()
	att, err := DefaultClipboardImageReader(root).ReadClipboardImage(context.Background())
	if err != nil {
		t.Fatalf("ReadClipboardImage: %v", err)
	}
	wantDir := filepath.Join(root, ".forebrain", "attachments")
	if dir := filepath.Dir(att.Path); filepath.Clean(dir) != filepath.Clean(wantDir) {
		t.Fatalf("pasted image landed in %q, want %q", dir, wantDir)
	}
	if att.MIMEType != "image/png" {
		t.Fatalf("mime = %q, want image/png", att.MIMEType)
	}
}

// TestClipboardImageReaderFallsBackToTempWithoutAProject pins that a reader
// with no project root (a test, or a surface with no project) keeps the old
// temp-directory behavior.
func TestClipboardImageReaderFallsBackToTempWithoutAProject(t *testing.T) {
	prev := clipboardWritePNG
	t.Cleanup(func() { clipboardWritePNG = prev })
	clipboardWritePNG = func(_ context.Context, path string) error {
		return os.WriteFile(path, []byte("png-bytes"), 0o600)
	}
	att, err := DefaultClipboardImageReader("").ReadClipboardImage(context.Background())
	if err != nil {
		t.Fatalf("ReadClipboardImage: %v", err)
	}
	if dir := filepath.Dir(att.Path); filepath.Clean(dir) != filepath.Clean(os.TempDir()) {
		t.Fatalf("pasted image landed in %q, want the temp directory %q", dir, os.TempDir())
	}
}

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

func startClipboardWatcher(t *testing.T, probe ClipboardImageProbe, interval time.Duration, tune ...func(*clipboardImageWatcher)) (*clipboardImageWatcher, *Renderer) {
	t.Helper()
	r := NewRenderer(nil, nil)
	w := newClipboardImageWatcher(probe, r)
	w.interval = interval
	// The real debounce is 1s; tests wait on 2s deadlines, so shorten it
	// unless a test tunes it further. Tuning runs before the goroutine
	// starts, so the run loop never races the fields it reads.
	w.debounce = 5 * time.Millisecond
	for _, f := range tune {
		f(w)
	}
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

// TestClipboardImageHintExpiresWhileImageStays pins the hint as transient:
// gone after clipboardImageHintTimeout even though nothing told the renderer
// the clipboard changed, hidden at once by false, and a repeated true while
// it is up must not push its expiry back.
func TestClipboardImageHintExpiresWhileImageStays(t *testing.T) {
	prev := clipboardImageHintTimeout
	clipboardImageHintTimeout = 50 * time.Millisecond
	t.Cleanup(func() { clipboardImageHintTimeout = prev })

	r := NewRenderer(nil, nil)
	r.SetClipboardImageAvailable(true)
	if !clipboardHintState(r) {
		t.Fatal("the hint must be up after SetClipboardImageAvailable(true)")
	}
	eventually(t, "the hint to expire while the image stays", func() bool { return !clipboardHintState(r) })

	r.SetClipboardImageAvailable(true)
	eventually(t, "a fresh true to show the hint again", func() bool { return clipboardHintState(r) })
	r.mu.Lock()
	armed := r.clipboardImageTimer
	r.mu.Unlock()
	r.SetClipboardImageAvailable(true) // already up: keep the original expiry
	r.mu.Lock()
	rearmed := r.clipboardImageTimer != armed
	r.mu.Unlock()
	if rearmed {
		t.Fatal("a repeated true while the hint is up must not re-arm its expiry")
	}

	r.SetClipboardImageAvailable(false)
	if clipboardHintState(r) {
		t.Fatal("false must hide the hint at once")
	}
	r.SetClipboardImageAvailable(false)
	if clipboardHintState(r) {
		t.Fatal("a second false must not show anything")
	}
}

// TestClipboardWatcherDebounceDropsAFleetingImage pins the debounce: an image
// that leaves before the rising edge's re-check never produces a hint.
func TestClipboardWatcherDebounceDropsAFleetingImage(t *testing.T) {
	probe := &countingClipboardProbe{}
	_, r := startClipboardWatcher(t, probe, 5*time.Millisecond, func(w *clipboardImageWatcher) {
		w.debounce = 100 * time.Millisecond
	})
	probe.image.Store(true)
	time.Sleep(50 * time.Millisecond) // rising edge seen, re-check pending
	probe.image.Store(false)          // the image leaves first
	time.Sleep(200 * time.Millisecond)
	if clipboardHintState(r) {
		t.Fatal("a fleeting image must not produce a hint")
	}
}

// TestClipboardWatcherRateLimitsRepeatHints pins the rate limit: once a hint
// has been shown, a new image inside the limit — falling edge, rising edge
// and re-check all happening — stays silent.
func TestClipboardWatcherRateLimitsRepeatHints(t *testing.T) {
	prev := clipboardImageHintTimeout
	clipboardImageHintTimeout = 50 * time.Millisecond
	t.Cleanup(func() { clipboardImageHintTimeout = prev })

	probe := &countingClipboardProbe{}
	probe.image.Store(true)
	_, r := startClipboardWatcher(t, probe, 5*time.Millisecond, func(w *clipboardImageWatcher) {
		w.rateLimit = 10 * time.Second
	})
	eventually(t, "the first hint to show", func() bool { return clipboardHintState(r) })
	eventually(t, "the first hint to expire", func() bool { return !clipboardHintState(r) })

	probe.image.Store(false)
	time.Sleep(50 * time.Millisecond) // a tick sees the falling edge
	probe.image.Store(true)
	time.Sleep(300 * time.Millisecond) // rising edge and re-check happen inside the limit
	if clipboardHintState(r) {
		t.Fatal("a repeat hint inside the rate limit must stay silent")
	}
}

// TestClipboardWatcherRetriesConfirmWhileUnfocused pins the re-check's focus
// gate: a debounce that matures while the terminal is in the background
// probes nothing, and the re-armed one after refocus shows the hint.
func TestClipboardWatcherRetriesConfirmWhileUnfocused(t *testing.T) {
	probe := &countingClipboardProbe{}
	probe.image.Store(true)
	w, r := startClipboardWatcher(t, probe, time.Hour, func(w *clipboardImageWatcher) {
		w.debounce = 50 * time.Millisecond
	})
	eventually(t, "the rising edge's probe", func() bool { return probe.calls.Load() >= 1 })
	w.SetFocused(false)
	time.Sleep(120 * time.Millisecond) // the debounce matures unfocused and re-arms
	if got := probe.calls.Load(); got != 1 {
		t.Fatalf("an unfocused re-check must not probe, got %d probes", got)
	}
	if clipboardHintState(r) {
		t.Fatal("nothing may be shown from an unfocused re-check")
	}
	w.SetFocused(true)
	eventually(t, "the re-armed re-check to show the hint after refocus", func() bool { return clipboardHintState(r) })
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
// probes immediately rather than on the next tick, so the hint's state is
// re-evaluated the moment it can be seen.
func TestClipboardWatcherProbesAtOnceWhenFocusReturns(t *testing.T) {
	probe := &countingClipboardProbe{}
	probe.image.Store(true)
	w, r := startClipboardWatcher(t, probe, time.Hour) // no tick fires in this test
	eventually(t, "the debounced re-check to show the hint", func() bool { return clipboardHintState(r) })

	w.SetFocused(false)
	probe.image.Store(false)
	w.SetFocused(true)
	eventually(t, "regaining focus to clear the hint", func() bool { return !clipboardHintState(r) })
}

// TestEndsOnErrorRecognisesAStopsClosingLine pins plans/257: a stop's closing
// status line carries its reason as content, and that is the view already
// saying the sentence — a global error mirroring the same words in is a
// repeat, not news. Every other status still annotates the ending and is
// looked through.
func TestEndsOnErrorRecognisesAStopsClosingLine(t *testing.T) {
	const s = "approval continuation was interrupted after execution began; tool outcome is uncertain and was not retried"

	vm := viewModel{}
	vm.append(Frame{Kind: FrameStatus, Title: "Interrupted ·", Content: s, Final: true})
	if !vm.endsOnError(s) {
		t.Fatal("a stop's closing line saying the sentence must count as the view having said it")
	}

	vm = viewModel{}
	vm.append(Frame{Kind: FrameStatus, Title: "Worked for 3s", Content: "", Final: true})
	if vm.endsOnError(s) {
		t.Fatal("an annotating status that says nothing must still be looked through")
	}

	vm = viewModel{}
	vm.append(Frame{Kind: FrameError, Title: "subagent failed", Content: s, Final: true})
	vm.append(Frame{Kind: FrameStatus, Title: "Worked for 3s", Content: "", Final: true})
	if !vm.endsOnError(s) {
		t.Fatal("an error under an annotating status must keep counting (original behavior)")
	}
}
