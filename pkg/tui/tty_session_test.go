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
	if rows := r.composerHintRows(80); len(rows) != 0 {
		t.Fatalf("hint shown without an image: %v", rows)
	}
	r.composerHint = clipboardImageHintText
	rows := r.composerHintRows(80)
	if len(rows) != 1 || !strings.Contains(rows[0], clipboardImageHintText) {
		t.Fatalf("hint missing: %v", rows)
	}
	if w := runewidth.StringWidth(stripANSI(rows[0])); w != 80 {
		t.Fatalf("hint not right-aligned, width %d", w)
	}
	// Owner's 2026-11-14 ruling: a hint narrower than its text WRAPS, it is
	// never truncated.
	narrow := r.composerHintRows(5)
	if len(narrow) < 2 {
		t.Fatalf("hint did not wrap at width 5: %v", narrow)
	}
	// Every row carries its own SGR (the grey the clipboard hint has), so the
	// text must be compared with the styling stripped: joining the styled
	// strings would break a word across an escape sequence.
	var plain strings.Builder
	for _, row := range narrow {
		text := stripANSI(row)
		if w := runewidth.StringWidth(text); w > 5 {
			t.Fatalf("row %q is %d cells wide, terminal is 5", text, w)
		}
		plain.WriteString(text)
	}
	if !strings.Contains(plain.String(), "clipboard") {
		t.Fatalf("wrapped hint lost its text: %q", plain.String())
	}
}

// hintText is the composer hint as one string, for the tests that only care
// what it says.
func hintText(r *Renderer) string {
	return strings.Join(hintRowsFor(r, 80), "\n")
}

// TestOneComposerHintSaysOneThingAtATime pins the slot as a single message: two
// producers (the clipboard watcher and a dead image click) share the rows, so a
// new message replaces the old one and each producer can only take down its own.
func TestOneComposerHintSaysOneThingAtATime(t *testing.T) {
	r := NewRenderer(nil, nil)
	r.ShowComposerHint(clipboardImageHintText, "", time.Second)
	if got := hintText(r); !strings.Contains(got, clipboardImageHintText) {
		t.Fatalf("hint = %q, want the clipboard text", got)
	}

	r.ShowComposerHint("open a.png: file no longer exists", "file no longer exists", time.Second)
	if got := hintText(r); !strings.Contains(got, "open a.png: file no longer exists") {
		t.Fatalf("a second message must replace the first, got %q", got)
	}

	// The clipboard's own falling edge must not wipe the click's hint.
	r.HideComposerHint(clipboardImageHintText)
	if got := hintText(r); !strings.Contains(got, "open a.png") {
		t.Fatalf("another producer's text must not be hidden, got %q", got)
	}

	r.HideComposerHint("open a.png: file no longer exists")
	if got := hintText(r); got != "" {
		t.Fatalf("the hint's own text must take it down, got %q", got)
	}
}

// TestComposerHintExpiresOnItsOwn pins that no caller has to take the hint
// down: the clock started when it was shown does it.
func TestComposerHintExpiresOnItsOwn(t *testing.T) {
	prev := composerHintTimeout
	composerHintTimeout = 50 * time.Millisecond
	t.Cleanup(func() { composerHintTimeout = prev })

	r := NewRenderer(nil, nil)
	r.ShowComposerHint("open a.png: file no longer exists", "", composerHintTimeout)
	if got := hintText(r); got == "" {
		t.Fatal("the hint must be up as soon as ShowComposerHint returns")
	}
	eventually(t, "the hint to expire on its own", func() bool { return hintText(r) == "" })
}

// TestComposerHintDropsTheNameBeforeItWraps pins the two forms: the full
// sentence names the file, and once one row cannot hold it the reason alone is
// what stays — wrapped, never cut.
func TestComposerHintDropsTheNameBeforeItWraps(t *testing.T) {
	r := NewRenderer(nil, nil)
	r.ShowComposerHint("open logs-debug.png: file no longer exists", "file no longer exists", time.Second)

	wide := hintRowsFor(r, 80)
	if len(wide) != 1 || wide[0] != "open logs-debug.png: file no longer exists" {
		t.Fatalf("width 80 = %v, want the whole sentence on one row", wide)
	}

	// Exactly the reason's width: the full sentence no longer fits, so the
	// name goes rather than the words being broken up.
	exact := hintRowsFor(r, len("file no longer exists"))
	if len(exact) != 1 || exact[0] != "file no longer exists" {
		t.Fatalf("width 21 = %v, want the reason alone on one row", exact)
	}

	narrow := hintRowsFor(r, 8)
	if len(narrow) < 2 {
		t.Fatalf("width 8 = %v, want the reason wrapped over several rows", narrow)
	}
	for _, row := range narrow {
		if w := runewidth.StringWidth(row); w > 8 {
			t.Fatalf("row %q is %d cells wide, terminal is 8", row, w)
		}
	}
	joined := strings.Join(narrow, "")
	for _, want := range []string{"file", "longer", "exists"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("wrapped hint lost %q: %v", want, narrow)
		}
	}
	if strings.Contains(joined, "…") {
		t.Fatalf("the hint was cut, not wrapped: %v", narrow)
	}
}

// TestComposerHintRowsNeverExceedTheTerminal pins the alignment against the
// WIDEST row: padding to the narrowest one would push the wide rows past the
// terminal edge, where the painter cuts their tail off — exactly the reason the
// hint exists to show.
func TestComposerHintRowsNeverExceedTheTerminal(t *testing.T) {
	r := NewRenderer(nil, nil)
	r.ShowComposerHint("open screenshot-of-the-sidebar.png: file no longer exists", "", time.Hour)
	for _, width := range []int{8, 5} {
		for _, row := range hintRowsFor(r, width) {
			if w := runewidth.StringWidth(row); w > width {
				t.Fatalf("width %d: row %q is %d cells wide", width, row, w)
			}
		}
	}
}

// TestComposerHintNeverBreaksItsRows pins the sanitising: these rows are spliced
// straight into the paint stream, so a launcher's stderr carrying a newline, a
// tab or an escape must arrive as plain text, and the only escapes on the row
// are the two that make it grey.
func TestComposerHintNeverBreaksItsRows(t *testing.T) {
	r := NewRenderer(nil, nil)
	r.ShowComposerHint("open a.png: boom\nsecond line\twith tab\x1b[31m red", "", time.Hour)

	r.mu.Lock()
	raw := r.composerHintRows(20)
	r.mu.Unlock()
	if len(raw) == 0 {
		t.Fatal("no hint rows painted")
	}
	for _, row := range raw {
		plain := stripANSI(row)
		if strings.ContainsAny(plain, "\n\t") {
			t.Fatalf("row %q carries a line break or a tab", plain)
		}
		for _, ru := range plain {
			if ru < 0x20 || ru == 0x7f {
				t.Fatalf("row %q carries control character %q", plain, ru)
			}
		}
		if w := runewidth.StringWidth(plain); w > 20 {
			t.Fatalf("row %q is %d cells wide, terminal is 20", plain, w)
		}
		styled := strings.TrimLeft(row, " ")
		if !strings.HasPrefix(styled, sharedBlockFooterStyle) || !strings.HasSuffix(styled, sharedBlockReset) {
			t.Fatalf("row %q is not the grey hint row", row)
		}
		if body := strings.TrimSuffix(strings.TrimPrefix(styled, sharedBlockFooterStyle), sharedBlockReset); strings.Contains(body, "\x1b") {
			t.Fatalf("row %q carries an escape of its own: %q", row, body)
		}
	}
}

// TestAVeryLongFailureWrapsWithoutEverCutting pins the owner's ruling for a
// huge message (a launcher's 2KB stderr): every row wraps, nothing is ever
// elided, so the end of the reason is still on screen.
func TestAVeryLongFailureWrapsWithoutEverCutting(t *testing.T) {
	r := NewRenderer(nil, nil)
	r.ShowComposerHint("open a.png: "+strings.Repeat("x", 2000)+" file no longer exists", "", time.Hour)

	rows := hintRowsFor(r, 80)
	if len(rows) <= 2 {
		t.Fatalf("a 2KB message took %d rows at width 80, want it wrapped", len(rows))
	}
	for _, row := range rows {
		if w := runewidth.StringWidth(row); w > 80 {
			t.Fatalf("row is %d cells wide, terminal is 80", w)
		}
	}
	joined := strings.Join(rows, " ")
	for _, want := range []string{"file", "longer", "exists"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("the long message lost %q", want)
		}
	}
	if last := rows[len(rows)-1]; !strings.Contains(last, "exists") {
		t.Fatalf("the message ends with %q, want its tail intact", last)
	}
	if strings.Contains(joined, "…") {
		t.Fatal("the long message was elided instead of wrapped")
	}
}

// TestAStaleComposerHintTimerCannotWipeANewerHint pins the generation check: the
// 'first' timer has already fired by the time ShowComposerHint stops it, so only
// the generation can tell it that the hint on screen is somebody else's.
func TestAStaleComposerHintTimerCannotWipeANewerHint(t *testing.T) {
	r := NewRenderer(nil, nil)
	r.ShowComposerHint("first", "", time.Nanosecond)
	r.ShowComposerHint("second", "", time.Hour)

	for deadline := time.Now().Add(100 * time.Millisecond); time.Now().Before(deadline); {
		if got := hintText(r); !strings.Contains(got, "second") {
			t.Fatalf("a stale timer wiped the newer hint: %q", got)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestComposerHintIsPaintedByEveryComposerLayout pins the four layouts a hint
// has to survive: the editable composer (where it replaces the blank spacer, or
// adds rows below a queued preview), slash mode, and the read-only view an idle
// subagent shows. A click is answered where the user made it.
func TestComposerHintIsPaintedByEveryComposerLayout(t *testing.T) {
	r := NewRenderer(nil, nil)
	cursor := 0
	const hint = "open shot.png: file no longer exists"
	text := func(block composerBlock) string {
		lines := make([]string, len(block.lines))
		for i, line := range block.lines {
			lines[i] = stripANSI(line)
		}
		return strings.Join(lines, "\n")
	}

	empty := ComposerRenderState{Text: "", Cursor: &cursor}
	withoutHint := r.buildComposerBlock(empty, 80)
	// Set directly: no timer goroutine runs, so this layout call needs no lock.
	r.composerHint = hint
	withHint := r.buildComposerBlock(empty, 80)
	if !strings.Contains(text(withHint), hint) {
		t.Fatalf("the plain composer dropped the hint: %q", text(withHint))
	}
	if len(withHint.lines) != len(withoutHint.lines) {
		t.Fatalf("the hint replaced the spacer, so the row count must not move: %d -> %d",
			len(withoutHint.lines), len(withHint.lines))
	}

	queued := empty
	queued.PendingInput = ComposerPendingInputPreview{PendingSteers: []string{"queued while running"}}
	r.composerHint = ""
	queuedWithout := r.buildComposerBlock(queued, 80)
	r.composerHint = hint
	queuedWith := r.buildComposerBlock(queued, 80)
	if !strings.Contains(text(queuedWith), hint) {
		t.Fatalf("a queued preview hid the hint: %q", text(queuedWith))
	}
	if got, want := len(queuedWith.lines), len(queuedWithout.lines)+len(hintRowsFor(r, 80)); got != want {
		t.Fatalf("a hint below the queue preview must add its rows: %d -> %d, want %d", len(queuedWithout.lines), got, want)
	}

	slashCursor := len([]rune("/model"))
	slash := ComposerRenderState{Text: "/model", Cursor: &slashCursor}
	if got := text(r.buildComposerBlock(slash, 80)); !strings.Contains(got, hint) {
		t.Fatalf("slash mode dropped the hint: %q", got)
	}

	readOnly := ComposerRenderState{Text: "", Cursor: &cursor, ReadOnly: true}
	if got := text(r.buildComposerBlock(readOnly, 80)); !strings.Contains(got, hint) {
		t.Fatalf("the read-only composer dropped the hint: %q", got)
	}
	r.composerHint = ""
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
	return r.composerHint != ""
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
// gone after composerHintTimeout even though nothing told the renderer
// the clipboard changed, hidden at once by false, and a repeated true while
// it is up must not push its expiry back.
func TestClipboardImageHintExpiresWhileImageStays(t *testing.T) {
	prev := composerHintTimeout
	composerHintTimeout = 50 * time.Millisecond
	t.Cleanup(func() { composerHintTimeout = prev })

	r := NewRenderer(nil, nil)
	r.SetClipboardImageAvailable(true)
	if !clipboardHintState(r) {
		t.Fatal("the hint must be up after SetClipboardImageAvailable(true)")
	}
	eventually(t, "the hint to expire while the image stays", func() bool { return !clipboardHintState(r) })

	r.SetClipboardImageAvailable(true)
	eventually(t, "a fresh true to show the hint again", func() bool { return clipboardHintState(r) })
	r.mu.Lock()
	armed := r.composerHintTimer
	r.mu.Unlock()
	r.SetClipboardImageAvailable(true) // already up: keep the original expiry
	r.mu.Lock()
	rearmed := r.composerHintTimer != armed
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
	prev := composerHintTimeout
	composerHintTimeout = 50 * time.Millisecond
	t.Cleanup(func() { composerHintTimeout = prev })

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
