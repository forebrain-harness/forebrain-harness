package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
	"github.com/mattn/go-runewidth"
)

// stubImageOpener swaps the system viewer for the duration of one test and
// reports what (if anything) it was asked to open.
func stubImageOpener(t *testing.T) *[]string {
	t.Helper()
	prev := imagePreviewOpener
	var opened []string
	imagePreviewOpener = func(path string, _ func(error)) error {
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

// clickRow drives one plain click — a press and its release on the same cell,
// as the terminal reports them — through the production handlers and reports
// what the row resolved to. Each call is a fresh single click: the multi-click
// clock is reset so two calls in a row never read as a double-click.
func clickRow(t *testing.T, r *Renderer, row int) (path string, isMarker bool) {
	t.Helper()
	path, _, isMarker = r.activeVMLocked().imageMarkerAtScreenRow(r.vpLastRender, row)
	r.lastClickTime = time.Time{}
	r.ViewportSelectStart(0, row)
	r.ViewportSelectEnd(0, row)
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

// streamingRenderer is a following viewport whose transcript is a filler card,
// then the given blocks, then an answer still streaming below them; stream adds
// one more paragraph to that answer, as a delta would, and repaints.
func streamingRenderer(t *testing.T, blocks ...Frame) (r *Renderer, stream func()) {
	t.Helper()
	forcedTermWidth, forcedTermHeight = 80, 24
	t.Cleanup(func() { forcedTermWidth, forcedTermHeight = 0, 0 })
	r = NewRenderer(io.Discard, io.Discard)
	r.viewportMode = true
	r.vpFollow = true
	r.RenderFrame(Frame{Kind: FrameTool, Title: "read_file", Content: "1|package main", Summary: "read lines.txt", Final: true})
	for _, f := range blocks {
		r.RenderFrame(f)
	}
	var paragraphs []string
	stream = func() {
		paragraphs = append(paragraphs, fmt.Sprintf("paragraph %d", len(paragraphs)))
		r.RenderFrame(Frame{Kind: FrameAssistant, Title: "assistant", Content: strings.Join(paragraphs, "\n\n")})
	}
	// Stream until the answer overflows the screen: from here on every new
	// row scrolls the transcript up under a stationary pointer.
	for r.vpLastRender.firstRow == 0 {
		stream()
	}
	return r, stream
}

// TestImageMarkerClickSurvivesRowsStreamedBetweenPressAndRelease pins the
// "click to open does nothing while a turn streams" fix. In follow mode each
// streamed row scrolls the screen up, so between the press and the release the
// marker slides off the pointer's row. The click must still open the file the
// press landed on — resolving at release read the row that slid under the
// pointer instead and opened nothing.
func TestImageMarkerClickSurvivesRowsStreamedBetweenPressAndRelease(t *testing.T) {
	opened := stubImageOpener(t)
	r, stream := streamingRenderer(t, imageStepFrame("/tmp/shot.png"))

	row := markerRowOf(r.vpLastRender.lines, "shot.png")
	if row < 1 {
		t.Fatalf("marker must sit below the top row before the press, got %d:\n%s", row, stripANSI(strings.Join(r.vpLastRender.lines, "\n")))
	}
	r.ViewportSelectStart(6, row)
	stream()
	moved := markerRowOf(r.vpLastRender.lines, "shot.png")
	if moved == row {
		t.Fatalf("the streamed row did not scroll the marker (still at %d); the fixture proves nothing", row)
	}
	if _, _, ok := r.activeVMLocked().imageMarkerAtScreenRow(r.vpLastRender, row); ok {
		t.Fatalf("row %d is still a marker on the latest paint; the fixture proves nothing", row)
	}
	if !r.ViewportSelectEnd(6, row) {
		t.Fatal("a press and release on one cell must end as a plain click")
	}
	if len(*opened) != 1 || (*opened)[0] != "/tmp/shot.png" {
		t.Fatalf("opened = %v, want [/tmp/shot.png]", *opened)
	}
}

// TestPressCellReportAfterStreamedRowsIsNotADrag pins the other half: a motion
// report naming the press cell is no motion, even when streamed rows scrolled
// the transcript under the pointer since the press. The selection's end anchor
// is an absolute row, so taking that report read the scroll as a one-row drag:
// the click became a selection and the marker opened nothing. Moving to another
// cell is still a drag, and a drag still opens nothing.
func TestPressCellReportAfterStreamedRowsIsNotADrag(t *testing.T) {
	opened := stubImageOpener(t)
	r, stream := streamingRenderer(t, imageStepFrame("/tmp/shot.png"))

	row := markerRowOf(r.vpLastRender.lines, "shot.png")
	if row < 1 {
		t.Fatalf("marker must sit below the top row before the press, got %d", row)
	}
	r.ViewportSelectStart(6, row)
	stream()
	r.ViewportSelectDrag(6, row)
	if r.selectDidDrag {
		t.Fatal("a report of the press cell must not start a selection")
	}
	if !r.ViewportSelectEnd(6, row) {
		t.Fatal("the gesture must still end as a plain click")
	}
	if len(*opened) != 1 || (*opened)[0] != "/tmp/shot.png" {
		t.Fatalf("opened = %v, want [/tmp/shot.png]", *opened)
	}

	row = markerRowOf(r.vpLastRender.lines, "shot.png")
	r.lastClickTime = time.Time{}
	r.ViewportSelectStart(6, row)
	r.ViewportSelectDrag(7, row)
	if r.ViewportSelectEnd(7, row) {
		t.Fatal("leaving the press cell is a drag, not a click")
	}
	if len(*opened) != 1 {
		t.Fatalf("a drag across the marker opened %v", *opened)
	}
}

// TestFoldClickSurvivesRowsStreamedBetweenPressAndRelease pins that the fold
// toggle follows the press the same way: the card pressed is the card that
// expands, even after streamed rows moved it off the pointer's row.
func TestFoldClickSurvivesRowsStreamedBetweenPressAndRelease(t *testing.T) {
	var long []string
	for i := 0; i < 3*toolOutputMaxLines; i++ {
		long = append(long, fmt.Sprintf("%d|line %d", i+1, i))
	}
	r, stream := streamingRenderer(t, Frame{Kind: FrameTool, Title: "read_file", Content: strings.Join(long, "\n"), Summary: "read long.txt", Final: true})
	card := r.vm.blocks[1]
	if !card.collapsible || card.expanded {
		t.Fatalf("fixture card must be a folded collapsible card (collapsible=%v expanded=%v)", card.collapsible, card.expanded)
	}
	span, ok := r.vpLastRender.spanForBlock(card.id)
	if !ok {
		t.Fatal("the long card is not painted")
	}
	row := span.startRow + span.rowCount - 1 - r.vpLastRender.firstRow
	if row < 1 || row >= len(r.vpLastRender.lines) {
		t.Fatalf("the card's last row (%d) must be on screen below the top row", row)
	}
	r.ViewportSelectStart(4, row)
	stream()
	if id, _ := r.vpLastRender.blockAtRow(row); id == card.id {
		t.Fatalf("row %d still shows the card on the latest paint; the fixture proves nothing", row)
	}
	r.ViewportSelectEnd(4, row)
	if !card.expanded {
		t.Fatal("the click must expand the card its press landed on")
	}
}

// TestPressReadAfterAnUnseenPaintActsOnTheScreenShown pins the busy-loop case:
// the loop finishes painting a streamed delta before it reads a press that was
// made on the screen before it, so the press reaches the renderer after the
// marker already slid off the pointer's row. The event carries when it was
// read, and is resolved against the screen on the terminal at that moment.
func TestPressReadAfterAnUnseenPaintActsOnTheScreenShown(t *testing.T) {
	opened := stubImageOpener(t)
	r, stream := streamingRenderer(t, imageStepFrame("/tmp/shot.png"))

	row := markerRowOf(r.vpLastRender.lines, "shot.png")
	if row < 1 {
		t.Fatalf("marker must sit below the top row, got %d", row)
	}
	pressed := time.Now()
	time.Sleep(time.Millisecond)
	stream() // painted after the press, before the loop read it
	if markerRowOf(r.vpLastRender.lines, "shot.png") == row {
		t.Fatal("the streamed row did not scroll the marker; the fixture proves nothing")
	}
	r.HandleMouse(inputEvent{kind: inputEventMousePress, mouseCol: 6, mouseRow: row, at: pressed})
	if click := r.HandleMouse(inputEvent{kind: inputEventMouseRelease, mouseCol: 6, mouseRow: row, at: time.Now()}); !click.plain {
		t.Fatal("a press and release on one cell must end as a plain click")
	}
	if len(*opened) != 1 || (*opened)[0] != "/tmp/shot.png" {
		t.Fatalf("opened = %v, want [/tmp/shot.png]", *opened)
	}
}

// TestDoubleClickSelectsTheWordItsFirstPressPointedAt pins the multi-click
// half: a double-click is two presses on one cell, and when a streamed row
// scrolls the transcript between them the second press lands on whatever slid
// under the pointer. The word selected is the one the sequence began on.
func TestDoubleClickSelectsTheWordItsFirstPressPointedAt(t *testing.T) {
	stubImageOpener(t)
	r, stream := streamingRenderer(t, imageStepFrame("/tmp/shot.png"))

	row := markerRowOf(r.vpLastRender.lines, "shot.png")
	if row < 1 {
		t.Fatalf("marker must sit below the top row, got %d", row)
	}
	plain := stripANSI(r.vpLastRender.lines[row])
	col := runewidth.StringWidth(plain[:strings.Index(plain, "shot.png")]) + 1 // the "h" of "shot"
	r.lastClickTime = time.Time{}
	r.ViewportSelectStart(col, row)
	r.ViewportSelectEnd(col, row)
	stream()
	if markerRowOf(r.vpLastRender.lines, "shot.png") == row {
		t.Fatal("the streamed row did not scroll the marker; the fixture proves nothing")
	}
	r.ViewportSelectStart(col, row)
	if r.clickCount != 2 {
		t.Fatalf("the second press must continue the sequence, click count %d", r.clickCount)
	}
	if got := r.extractSelectedTextLocked(); got != "shot" {
		t.Fatalf("double-click selected %q, want the word the first press pointed at (shot)", got)
	}
	r.ViewportSelectEnd(col, row)
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

// TestSubagentViewDrawsTheConversationImageCard pins that a child agent's image
// step draws the card the conversation draws. Every step a subagent runs
// reaches the terminal as a canonical run event — the StepHook publishes a
// child's step instead of drawing it — so the view is rebuilt from
// event.ToolCallMeta by subagentToolStepMsg, the one door between the event and
// the card. A preview fact dropped at that door is not a lost detail: the frame
// fails the renderer's preview gate, so the card falls back to the preformatted
// "path: … · image W×H · bytes" body and the marker row stops being the row
// that opens the file.
func TestSubagentViewDrawsTheConversationImageCard(t *testing.T) {
	const agentID = "task-explore-1"
	const absPath = "/tmp/ws/.forebrain/attachments/shot.png"
	opened := stubImageOpener(t)

	// One image read as the engine reports it: the artifact is facts — kind,
	// path, pixels, encoded size — and never base64.
	ctx := tool.WithHookAgentID(context.Background(), agentID)
	step := tool.StepEvent{
		Kind:     event.RunEventToolCompleted,
		StepID:   "step-1",
		ToolName: "read_file",
		Input:    map[string]any{"file_path": absPath},
		Duration: 90 * time.Millisecond,
		Output: map[string]any{
			"preview_kind": "image",
			"abs_path":     absPath,
			"image_mime":   "image/png",
			"image_bytes":  1000,
			"image_width":  640,
			"image_height": 480,
			"result_lines": 0,
		},
	}
	canonical, ok := tool.RunEventFromStep(ctx, "sess-1", "run-1", "tui", step)
	if !ok {
		t.Fatal("an image read must reach the surfaces as a canonical run event")
	}
	var payload event.ToolCallCompletedPayload
	if err := json.Unmarshal(canonical.Payload, &payload); err != nil {
		t.Fatalf("decode the published payload: %v", err)
	}

	// What the surface consumes off the event stream and paints on that
	// agent's own view.
	msg := subagentToolStepMsg(canonical, payload.StepID, payload.ToolName, payload.Summary, payload.ToolMeta, canonical.Type)
	msg.Msg.Content = payload.DisplayBody

	// The mirror must rebuild, field for field, the meta the conversation's
	// own StepHook path builds for the same step: same facts in, same card
	// out.
	want := tool.EnrichToolMeta(ctx, tool.BuildToolMeta(step))
	if got := msg.Msg.ToolMeta; !reflect.DeepEqual(got, want) {
		t.Fatalf("the event mirror rebuilt a different meta:\n got %+v\nwant %+v", got, want)
	}

	r := NewRenderer(io.Discard, io.Discard)
	r.viewportMode = true
	r.composerSuppressed = true
	var red Reducer
	for _, f := range red.Reduce(msg).Frames {
		r.RenderFrame(f)
	}
	vm := r.perAgentVM[agentID]
	if vm == nil {
		t.Fatalf("a subagent's tool step must land in that agent's own view: %v", r.perAgentVM)
	}
	vr := renderViewport(vm, 100, 40, 0, DiffThemeDark)
	plain := stripANSI(strings.Join(vr.lines, "\n"))
	for _, seg := range []string{"▤ shot.png", "640×480", "1000 bytes", "click to open"} {
		if !strings.Contains(plain, seg) {
			t.Fatalf("the subagent's image card is missing %q:\n%s", seg, plain)
		}
	}
	if strings.Contains(plain, "path: `") {
		t.Fatalf("the subagent's image card fell through to the text body the marker line replaces:\n%s", plain)
	}

	// The marker row is the card's one button in either view: clicking it in
	// the subagent's view opens the file it names.
	row := markerRowOf(vr.lines, "shot.png")
	if row < 0 {
		t.Fatalf("no image marker row in the subagent's view:\n%s", plain)
	}
	r.SetActiveView(agentID)
	r.vpHeight, r.vpBodyHeight = 40, 40
	r.vpLastRender = vr
	if path, isMarker := clickRow(t, r, row); !isMarker || path != absPath {
		t.Fatalf("the subagent's marker row (%d) resolved to (%q, %v), want (%q, true)", row, path, isMarker, absPath)
	}
	if len(*opened) != 1 || (*opened)[0] != absPath {
		t.Fatalf("opened = %v, want [%s]", *opened, absPath)
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

	shot := filepath.Join(dir, "shot.webp")
	if err := os.WriteFile(shot, []byte("RIFF"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := openWithSystemViewer(shot, nil); err != nil {
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

// fakeViewer puts a program named like the platform's launcher first on PATH,
// running body as a shell script, and returns the directory it lives in.
func fakeViewer(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the viewer program is cmd.exe on Windows")
	}
	dir := t.TempDir()
	program, _ := imageViewerCommand("x.png")
	if program == "" || program != filepath.Base(program) {
		t.Skipf("imageViewerCommand resolved %q; this test needs a bare program name", program)
	}
	if err := os.WriteFile(filepath.Join(dir, program), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

// lastErrorFrame returns the content of the newest error block in the main
// transcript, or "".
func lastErrorFrame(r *Renderer) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.vm.blocks) - 1; i >= 0; i-- {
		if f := r.vm.blocks[i].frame; f.Kind == FrameError {
			return f.Content
		}
	}
	return ""
}

// TestClickingTheMarkerOfAFileSinceDeletedSaysSo pins the "click to open does
// nothing while a shell command runs" report: the command was rebuilding the
// directory the image had been read from, so the file was gone, and the only
// thing that noticed — the launcher's exit status — was thrown away. A missing
// file is now caught before anything is launched, and the click says so.
func TestClickingTheMarkerOfAFileSinceDeletedSaysSo(t *testing.T) {
	viewerDir := fakeViewer(t, ": > \"$0.ran\"\n")
	gone := filepath.Join(t.TempDir(), "shot.png")
	r := NewRenderer(io.Discard, io.Discard)
	r.viewportMode = true
	r.composerSuppressed = true

	openImagePreview(r, gone, "shot.png")
	if got := lastErrorFrame(r); got != "open shot.png: file no longer exists" {
		t.Fatalf("error frame = %q, want the missing file named", got)
	}
	program, _ := imageViewerCommand(gone)
	if _, err := os.Stat(filepath.Join(viewerDir, program+".ran")); err == nil {
		t.Fatal("nothing may be launched for a file that is gone")
	}
}

// TestALauncherThatRefusesTheFileIsReported pins the other half: a launcher
// checks what it was handed only once it runs, so a refusal arrives as its exit
// status after the click returned. It must reach the transcript with what the
// launcher said, instead of the click silently doing nothing.
func TestALauncherThatRefusesTheFileIsReported(t *testing.T) {
	fakeViewer(t, "echo 'No application knows how to open it.' >&2\nexit 1\n")
	shot := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(shot, []byte("\x89PNG"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := NewRenderer(io.Discard, io.Discard)
	r.viewportMode = true
	r.composerSuppressed = true

	openImagePreview(r, shot, "shot.png")
	want := "open shot.png: No application knows how to open it."
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		if got := lastErrorFrame(r); got == want {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("error frame = %q, want %q", lastErrorFrame(r), want)
}
