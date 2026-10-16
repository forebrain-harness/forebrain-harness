package tui

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// stubImageOpener swaps the system viewer for the duration of one test and
// reports what (if anything) it was asked to open.
func stubImageOpener(t *testing.T) *[]string {
	t.Helper()
	prev := imagePreviewOpener
	var opened []string
	imagePreviewOpener = func(path string) error {
		opened = append(opened, path)
		return nil
	}
	t.Cleanup(func() { imagePreviewOpener = prev })
	return &opened
}

// TestImageStepRendersMarkerLine pins the terminal marker for an image step: a
// single line naming the file, its pixel size and its encoded size, with the
// affordance that opens it — never a JSON dump of the metadata.
func TestImageStepRendersMarkerLine(t *testing.T) {
	meta := tool.ToolMeta{
		ToolName: "read_file", Status: "completed",
		PreviewKind: "image", PreviewPath: "/tmp/ws/state/mcp-results/mcp-shots-screenshot-1.png",
		PreviewMIME: "image/png", PreviewWidth: 1200, PreviewHeight: 600, PreviewBytes: 1450000,
	}
	plain := stripANSI(strings.Join(renderFrameLines(
		Frame{Kind: FrameTool, Title: "read_file", ToolMeta: meta, Final: true}, 120, DiffThemeDark, 0), "\n"))

	for _, want := range []string{"▤", "mcp-shots-screenshot-1.png", "1200×600", "1.4 MB", "click to open"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("image marker line missing %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, `"preview_kind"`) {
		t.Fatalf("image marker fell through to a JSON dump:\n%s", plain)
	}
	// The key binding is gone: the line must not still advertise it.
	if strings.Contains(strings.ToLower(plain), "ctrl+o") {
		t.Fatalf("image marker still advertises the retired Ctrl+O binding:\n%s", plain)
	}
}

// TestImageMarkerLineOmitsUnknownPixelSize pins the surface contract shared
// with the web card: a surface omits the W×H segment rather than showing 0×0.
// An image whose size was never learned — a corrupt header behind sniffable
// magic bytes, or an MCP degrade path reporting w,h=0 — still gets its marker,
// with the size simply absent, and a size that IS known still shows.
func TestImageMarkerLineOmitsUnknownPixelSize(t *testing.T) {
	zero := imagePreviewFrameLine(tool.ToolMeta{
		PreviewKind: "image", PreviewPath: "/x/a.png", PreviewBytes: 123,
	})
	if want := "▤ a.png · 123 bytes · click to open"; zero != want {
		t.Fatalf("zero-dims marker = %q, want %q", zero, want)
	}
	if strings.Contains(zero, "0×0") {
		t.Fatalf("zero-dims marker must not print 0×0: %q", zero)
	}

	known := imagePreviewFrameLine(tool.ToolMeta{
		PreviewKind: "image", PreviewPath: "/x/a.png",
		PreviewWidth: 7, PreviewHeight: 3, PreviewBytes: 123,
	})
	if want := "▤ a.png · 7×3 · 123 bytes · click to open"; known != want {
		t.Fatalf("known-dims marker = %q, want %q", known, want)
	}
}

// imageStepFrame is a completed image tool step for path, as the engine reports
// it: PreviewKind "image" is what makes the terminal draw the marker line.
func imageStepFrame(path string) Frame {
	return Frame{Kind: FrameTool, Title: "read_file", Final: true, ToolMeta: tool.ToolMeta{
		ToolName: "read_file", Status: "completed",
		PreviewKind: "image", PreviewPath: path, PreviewWidth: 640, PreviewHeight: 480, PreviewBytes: 1000,
	}}
}

// markerRowOf returns the 0-based screen row of the marker line naming name, or
// -1 when no painted row shows it.
func markerRowOf(lines []string, name string) int {
	for i, line := range lines {
		if strings.HasPrefix(stripANSI(line), "  └ ▤ "+name) {
			return i
		}
	}
	return -1
}

// firstRowWithPrefix returns the 0-based screen row of the first painted line
// whose style-stripped text opens on prefix, or -1. The marker's first row is
// the only body row that opens on "  └ ▤ " — a wrapped file name is cut by the
// row width, so the full name cannot be matched there.
func firstRowWithPrefix(lines []string, prefix string) int {
	for i, line := range lines {
		if strings.HasPrefix(stripANSI(line), prefix) {
			return i
		}
	}
	return -1
}

// TestImageMarkerHitTestOpensTheClickedStep pins what replaced Ctrl+O: the row
// the click landed on decides which file opens. Two image steps sit in the view
// with a text step between them, and hitting the FIRST marker must answer with
// the first file — the old "open the most recent image" lookup would have said
// two.jpg no matter which row was clicked.
func TestImageMarkerHitTestOpensTheClickedStep(t *testing.T) {
	vm := viewModel{}
	vm.append(imageStepFrame("/tmp/one.png"))
	vm.append(Frame{Kind: FrameTool, Title: "read_file", Content: "1|package main", Summary: "read lines.txt", Final: true})
	vm.append(imageStepFrame("/tmp/two.jpg"))

	vr := renderViewport(&vm, 80, 20, 0, DiffThemeDark)
	row := markerRowOf(vr.lines, "one.png")
	if row < 0 {
		t.Fatalf("no marker row for one.png in:\n%s", stripANSI(strings.Join(vr.lines, "\n")))
	}

	path, label, ok := vm.imageMarkerAtScreenRow(vr, row)
	if !ok || path != "/tmp/one.png" || label != "one.png" {
		t.Fatalf("imageMarkerAtScreenRow(row %d) = (%q, %q, %v), want (/tmp/one.png, one.png, true)", row, path, label, ok)
	}
	// The same release must not also fold the card: an image step whose marker
	// fits one row has no fold, so opening and toggling cannot both fire.
	if vm.toggleAtScreenRow(vr, row) {
		t.Fatal("clicking the image marker row must not toggle the block")
	}

	if row := markerRowOf(vr.lines, "two.jpg"); row < 0 {
		t.Fatal("no marker row for two.jpg")
	} else if path, _, ok := vm.imageMarkerAtScreenRow(vr, row); !ok || path != "/tmp/two.jpg" {
		t.Fatalf("imageMarkerAtScreenRow(two.jpg row) = (%q, %v), want (/tmp/two.jpg, true)", path, ok)
	}
}

// TestImageMarkerHitTestIgnoresEverythingButTheMarkerRow pins the boundaries of
// the click target: the card's own title (which also names the file), another
// step's body and an empty separator row all answer nothing, so only the marker
// line is a button.
func TestImageMarkerHitTestIgnoresEverythingButTheMarkerRow(t *testing.T) {
	vm := viewModel{}
	vm.append(imageStepFrame("/tmp/one.png"))
	vm.append(Frame{Kind: FrameTool, Title: "read_file", Content: "1|package main", Summary: "read lines.txt", Final: true})

	vr := renderViewport(&vm, 80, 20, 0, DiffThemeDark)
	if len(vr.spans) != 2 {
		t.Fatalf("expected two spans, got %d", len(vr.spans))
	}
	titleRow := vr.spans[0].startRow - vr.firstRow
	if row := markerRowOf(vr.lines, "one.png"); row < 0 || row == titleRow {
		t.Fatalf("expected the marker row below the title row, got marker=%d title=%d", row, titleRow)
	}

	textRow, blankRow := -1, -1
	for i, line := range vr.lines {
		plain := stripANSI(line)
		if textRow < 0 && strings.Contains(plain, "package main") {
			textRow = i
		}
		if blankRow < 0 && i > 0 && strings.TrimSpace(plain) == "" {
			blankRow = i
		}
	}
	if textRow < 0 || blankRow < 0 {
		t.Fatalf("fixture did not paint a text body row (%d) and a separator row (%d):\n%s", textRow, blankRow, stripANSI(strings.Join(vr.lines, "\n")))
	}

	for name, row := range map[string]int{"title": titleRow, "text body": textRow, "separator": blankRow} {
		if path, label, ok := vm.imageMarkerAtScreenRow(vr, row); ok {
			t.Fatalf("the %s row (%d) must not open anything, got (%q, %q)", name, row, path, label)
		}
	}
}

// TestImageMarkerRowPredicateSkipsEveryHintRow pins the row predicate directly.
// The body prefix it keys on ("  └ " and its "    " continuations) is shared by
// two summary rows the fold code synthesises, so those are excluded by their
// leading ellipsis — otherwise clicking "… 3 earlier lines" or "··· 12 more
// lines (click to expand)" would launch a viewer on top of toggling the block.
func TestImageMarkerRowPredicateSkipsEveryHintRow(t *testing.T) {
	cases := []struct {
		name string
		line string
		want bool
	}{
		{"marker line", "  └ ▤ shot.png · 640×480 · 602.7 KB · click to open", true},
		{"wrapped marker continuation", "    4 KB · click to open", true},
		{"streaming hint", "    … 3 earlier lines", false},
		{"fold hint", "    ··· 12 more lines (click to expand)", false},
		{"card title", "● Read shot.png <0.1s", false},
		{"empty row", "", false},
		// A text read's body row carries the same prefix; only the block type
		// distinguishes it, which the vm-level test above covers.
		{"text body row", "  └ 1|package main", true},
	}
	for _, tc := range cases {
		if got := isImageMarkerRow(tc.line); got != tc.want {
			t.Errorf("isImageMarkerRow(%q) = %v, want %v", tc.line, got, tc.want)
		}
	}
}

// clickRow drives one mouse release through the production handler and reports
// what the row resolved to.
func clickRow(t *testing.T, r *Renderer, row int) (path string, isMarker bool) {
	t.Helper()
	path, _, isMarker = r.ImagePreviewAtScreenRow(row)
	clickImagePreviewRow(r, 0, row)
	return path, isMarker
}

// TestClickingTheImageMarkerRowLaunchesTheClickedFile pins the launch from a
// real renderer: the marker row hands its absolute path to the opener, while
// the title row and a row past the transcript window (the composer's) hand over
// nothing at all.
func TestClickingTheImageMarkerRowLaunchesTheClickedFile(t *testing.T) {
	opened := stubImageOpener(t)
	r := NewRenderer(io.Discard, io.Discard)
	r.viewportMode = true
	r.composerSuppressed = true
	r.vm.append(imageStepFrame("/tmp/shot.png"))
	r.vpHeight = 10
	r.vpBodyHeight = 10
	r.vpLastRender = renderViewport(&r.vm, 80, 10, 0, DiffThemeDark)

	row := markerRowOf(r.vpLastRender.lines, "shot.png")
	if row < 0 {
		t.Fatalf("no marker row painted:\n%s", stripANSI(strings.Join(r.vpLastRender.lines, "\n")))
	}
	if _, isMarker := clickRow(t, r, row); !isMarker {
		t.Fatalf("the marker row (%d) did not resolve to an image step", row)
	}
	if len(*opened) != 1 || (*opened)[0] != "/tmp/shot.png" {
		t.Fatalf("opened = %v, want [/tmp/shot.png]", *opened)
	}

	titleRow := r.vpLastRender.spans[0].startRow - r.vpLastRender.firstRow
	if _, isMarker := clickRow(t, r, titleRow); isMarker {
		t.Fatal("the card title must not resolve to the marker row")
	}
	if len(*opened) != 1 {
		t.Fatalf("clicking the title row opened %v", *opened)
	}

	if _, isMarker := clickRow(t, r, r.vpBodyHeight); isMarker {
		t.Fatal("a row below the transcript body must not resolve to a marker row")
	}
	if len(*opened) != 1 {
		t.Fatalf("a click below the transcript body opened %v", *opened)
	}

	// Nothing painted at all: a click must not invent a target.
	openImagePreview(NewRenderer(io.Discard, io.Discard), "/tmp/shot.png", "shot.png")
	if len(*opened) != 2 {
		t.Fatalf("openImagePreview on a bare renderer opened %v", *opened)
	}
}

// wrappedImageStepFrame is an image step whose file name is long enough to
// wrap its marker line past the fold budget — the shape that used to make an
// image card collapsible. Since the owner's 2026-10-16 ruling it no longer
// does: the card never folds, so the wrap only makes the card taller.
func wrappedImageStepFrame(name string) Frame {
	return imageStepFrame("/tmp/" + name)
}

// TestImageCardIsNeverCollapsibleAndItsMarkerOpensTheFile pins the owner's
// 2026-10-16 ruling: an image preview card's output area is never collapsible
// and is always shown in full. The card's whole output is its marker line —
// every row a long file name wraps it into — so there are no "more lines" to
// hide: no fold hint is painted, no row of the card toggles it, and clicking
// the marker means exactly one thing, opening the file. (The design this
// replaced deliberately folded a wrapped image card like any other tool card
// and treated its marker rows as the only mouse path back from expanded.)
func TestImageCardIsNeverCollapsibleAndItsMarkerOpensTheFile(t *testing.T) {
	opened := stubImageOpener(t)
	forcedTermWidth, forcedTermHeight = 40, 30
	t.Cleanup(func() { forcedTermWidth, forcedTermHeight = 0, 0 })

	name := strings.Repeat("c", 200) + ".png"
	r := NewRenderer(io.Discard, io.Discard)
	r.viewportMode = true
	r.vm.append(wrappedImageStepFrame(name))
	r.paintViewportLocked()

	plain := stripANSI(strings.Join(r.vpLastRender.lines, "\n"))
	if block := r.vm.blocks[0]; block == nil || block.collapsible {
		t.Fatal("an image preview card must never be collapsible")
	}
	if strings.Contains(plain, "more lines (click to expand)") {
		t.Fatalf("an image card must not draw a fold hint:\n%s", plain)
	}
	// Fully shown means the marker's late segments are on screen: the pixel
	// and byte sizes sit many rows past the fold budget, so a folded preview
	// would have cut them. ("click to open" itself is too weak a probe — the
	// wrap can split it across rows.)
	if !strings.Contains(plain, "640×480") || !strings.Contains(plain, "1000 bytes") {
		t.Fatalf("the wrapped marker is not shown in full:\n%s", plain)
	}

	// Clicking the marker's first row opens the file and flips nothing: the
	// click is resolved against the render it landed on, and with no fold
	// there is no toggle to ride along.
	markerRow := firstRowWithPrefix(r.vpLastRender.lines, "  └ ▤ ")
	if markerRow < 0 {
		t.Fatalf("no marker row painted:\n%s", plain)
	}
	path, isMarker := clickRow(t, r, markerRow)
	if !isMarker || path != "/tmp/"+name {
		t.Fatalf("the marker row (%d) resolved to (%q, %v), want (%q, true)", markerRow, path, isMarker, "/tmp/"+name)
	}
	if len(*opened) != 1 || (*opened)[0] != path {
		t.Fatalf("opened = %v, want [%s]", *opened, path)
	}
	if r.vm.blocks[0].expanded {
		t.Fatal("opening the file must not change the card's fold state")
	}

	// The wrapped tail of the marker line is still the marker: clicking it
	// opens the same file, never folds.
	tailRow := -1
	for i, line := range r.vpLastRender.lines {
		if strings.Contains(stripANSI(line), "1000 bytes") {
			tailRow = i
		}
	}
	if tailRow < 0 || tailRow == markerRow {
		t.Fatalf("expected a wrapped continuation row past the marker's first row, got tail=%d marker=%d", tailRow, markerRow)
	}
	if path, isMarker := clickRow(t, r, tailRow); !isMarker || path != "/tmp/"+name {
		t.Fatalf("the marker's wrapped row (%d) resolved to (%q, %v), want (%q, true)", tailRow, path, isMarker, "/tmp/"+name)
	}
	if len(*opened) != 2 || (*opened)[1] != "/tmp/"+name {
		t.Fatalf("opened = %v, want two opens of %s", *opened, "/tmp/"+name)
	}

	// No row of the card folds it — not the title, not any body row.
	titleRow := r.vpLastRender.spans[0].startRow - r.vpLastRender.firstRow
	for rowName, row := range map[string]int{"title": titleRow, "marker": markerRow, "marker tail": tailRow} {
		if r.vm.toggleAtScreenRow(r.vpLastRender, row) {
			t.Fatalf("clicking the %s row (%d) must not toggle an image card", rowName, row)
		}
	}
	if r.vm.blocks[0].expanded {
		t.Fatal("no click on the image card may change its fold state")
	}
}

// TestCtrlOIsUnboundAndNeverEntersTheComposer pins the retirement of the
// open-image hotkey. Ctrl+O (0x0f) is now an ordinary C0 byte the reader drops:
// it must produce no hotkey and reach neither the composer draft nor a
// submitted line. A bare 'o' still types an 'o' — that half of the old
// regression guard is what keeps the key from being re-bound to a letter.
func TestCtrlOIsUnboundAndNeverEntersTheComposer(t *testing.T) {
	collect := func(write func(w io.Writer)) []inputEvent {
		t.Helper()
		drainInteractiveInputSeedQueue()
		ctx, cancel := context.WithCancel(context.Background())
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("pipe: %v", err)
		}
		defer r.Close()
		events := readRawInputEvents(ctx, r, io.Discard, stubRawInputController{}, nil)
		var got []inputEvent
		done := make(chan struct{})
		go func() {
			for ev := range events {
				got = append(got, ev)
			}
			close(done)
		}()
		write(w)
		time.Sleep(160 * time.Millisecond)
		cancel()
		_ = w.Close()
		<-done
		return got
	}

	for _, ev := range collect(func(w io.Writer) { _, _ = w.Write([]byte{0x0f}) }) {
		switch ev.kind {
		case inputEventHotkey:
			t.Fatalf("Ctrl+O is unbound but produced hotkey %q", ev.hotkey)
		case inputEventDraft, inputEventLine:
			t.Fatalf("Ctrl+O must not reach the composer, got %+v", ev)
		}
	}

	drafted := false
	for _, ev := range collect(func(w io.Writer) { _, _ = w.Write([]byte("o")) }) {
		switch ev.kind {
		case inputEventHotkey:
			t.Fatalf("a bare 'o' must not produce a hotkey, got %q", ev.hotkey)
		case inputEventDraft, inputEventLine:
			drafted = true
		}
	}
	if !drafted {
		t.Fatal("a bare 'o' must still be typed into the composer")
	}
}

// TestImageViewerCommandIsPlatformSpecific is a smoke check that the opener
// resolves a launch command on every platform.
func TestImageViewerCommandIsPlatformSpecific(t *testing.T) {
	name, args := imageViewerCommand("/tmp/x.png")
	if strings.TrimSpace(name) == "" {
		t.Fatal("no viewer command resolved")
	}
	if !bytes.Contains([]byte(strings.Join(args, " ")), []byte("/tmp/x.png")) {
		t.Fatalf("viewer args %v do not name the file", args)
	}
}

// TestOpenWithSystemViewerLetsTheViewerOutliveTheHandler pins the root cause of
// "open image does nothing": the launcher hands the path to the platform and
// exits on its own schedule, so a launcher killed the moment the handler
// returns never opens anything. A fake viewer is put on PATH: it sleeps past
// the handler's return and then records that it finished.
func TestOpenWithSystemViewerLetsTheViewerOutliveTheHandler(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the viewer program is cmd.exe on Windows")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "opened")
	program, _ := imageViewerCommand("x.webp") // "open" (darwin) / "xdg-open"
	// The fake shadows the platform's program by name. An absolute path from
	// imageViewerCommand would silently stop intercepting and launch the real
	// viewer instead, so refuse to run rather than pass for the wrong reason.
	if program == "" || program != filepath.Base(program) {
		t.Skipf("imageViewerCommand resolved %q; this test needs a bare program name", program)
	}
	script := filepath.Join(dir, program)
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 0.3\n: > \"$FB_VIEWER_MARKER\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FB_VIEWER_MARKER", marker)

	if err := openWithSystemViewer(filepath.Join(dir, "shot.webp")); err != nil {
		t.Fatalf("launch: %v", err)
	}
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		if _, err := os.Stat(marker); err == nil {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("the viewer process did not survive the handler: nothing was opened")
}
