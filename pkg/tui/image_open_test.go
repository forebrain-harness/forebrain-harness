package tui

import (
	"bytes"
	"context"
	"io"
	"os"
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
	imagePreviewOpener = func(_ context.Context, path string) error {
		opened = append(opened, path)
		return nil
	}
	t.Cleanup(func() { imagePreviewOpener = prev })
	return &opened
}

// TestImageStepRendersMarkerLine pins the terminal marker for an image step: a
// single line naming the file, its pixel size and its encoded size, with the
// key that opens it — never a JSON dump of the metadata.
func TestImageStepRendersMarkerLine(t *testing.T) {
	meta := tool.ToolMeta{
		ToolName: "read_file", Status: "completed",
		PreviewKind: "image", PreviewPath: "/tmp/ws/state/mcp-results/mcp-shots-screenshot-1.png",
		PreviewMIME: "image/png", PreviewWidth: 1200, PreviewHeight: 600, PreviewBytes: 1450000,
	}
	plain := stripANSI(strings.Join(renderFrameLines(
		Frame{Kind: FrameTool, Title: "read_file", ToolMeta: meta, Final: true}, 120, DiffThemeDark, 0), "\n"))

	for _, want := range []string{"▤", "mcp-shots-screenshot-1.png", "1200×600", "1.4 MB", "Ctrl+O"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("image marker line missing %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, `"preview_kind"`) {
		t.Fatalf("image marker fell through to a JSON dump:\n%s", plain)
	}
}

// TestLatestImagePreviewFindsTheNewestImageStep pins the lookup Ctrl+O uses: it
// returns the most recent image step of the active view, and nothing once a
// later text step is the newest block that is not an image.
func TestLatestImagePreviewFindsTheNewestImageStep(t *testing.T) {
	r := NewRenderer(io.Discard, io.Discard)
	r.RenderFrame(Frame{Kind: FrameTool, Title: "read_file", Content: "text", Final: true})
	imageMeta := tool.ToolMeta{ToolName: "read_file", PreviewKind: "image", PreviewPath: "/tmp/one.png", PreviewBytes: 100}
	r.RenderFrame(Frame{Kind: FrameTool, Title: "read_file", ToolMeta: imageMeta, Final: true})

	if path, label := r.LatestImagePreview(); path != "/tmp/one.png" || label != "one.png" {
		t.Fatalf("LatestImagePreview = (%q, %q), want (/tmp/one.png, one.png)", path, label)
	}

	second := tool.ToolMeta{ToolName: "read_file", PreviewKind: "image", PreviewPath: "/tmp/two.jpg", PreviewBytes: 200}
	r.RenderFrame(Frame{Kind: FrameTool, Title: "read_file", ToolMeta: second, Final: true})
	if path, _ := r.LatestImagePreview(); path != "/tmp/two.jpg" {
		t.Fatalf("LatestImagePreview = %q, want the newest image step", path)
	}
}

// TestOpenImageHotkeyWithoutImageSpawnsNothing pins the Ctrl+O behaviour with
// no image step: it prints one hint line and never launches a viewer.
func TestOpenImageHotkeyWithoutImageSpawnsNothing(t *testing.T) {
	opened := stubImageOpener(t)
	r := NewRenderer(io.Discard, io.Discard)
	// A plain text step: nothing in the view is an image.
	r.RenderFrame(Frame{Kind: FrameTool, Title: "read_file", Content: "1  package main", Final: true})

	if path, _ := r.LatestImagePreview(); path != "" {
		t.Fatalf("LatestImagePreview = %q, want empty", path)
	}
	openLatestImagePreview(r, &streamState{})
	if len(*opened) != 0 {
		t.Fatalf("Ctrl+O spawned a viewer with no image step: %v", *opened)
	}

	blocks := r.vm.blocks
	if len(blocks) == 0 {
		t.Fatal("expected a hint frame")
	}
	last := blocks[len(blocks)-1].frame
	if last.Kind != FrameStatus || !strings.Contains(strings.ToLower(last.Content), "no image") {
		t.Fatalf("expected a one-line hint, got %#v", last)
	}
}

// TestOpenImageHotkeyOpensThePath pins the positive path: with an image step in
// the view, Ctrl+O hands its absolute path to the opener.
func TestOpenImageHotkeyOpensThePath(t *testing.T) {
	opened := stubImageOpener(t)
	r := NewRenderer(io.Discard, io.Discard)
	r.RenderFrame(Frame{Kind: FrameTool, Title: "read_file",
		ToolMeta: tool.ToolMeta{ToolName: "read_file", PreviewKind: "image", PreviewPath: "/tmp/shot.png", PreviewBytes: 10}, Final: true})

	openLatestImagePreview(r, &streamState{})
	if len(*opened) != 1 || (*opened)[0] != "/tmp/shot.png" {
		t.Fatalf("opened = %v, want [/tmp/shot.png]", *opened)
	}
}

// TestCtrlOIsNotABareLetterKey pins the key binding: Ctrl+O (0x0f) produces the
// open-image hotkey, and a bare 'o' typed into the composer does not — a bare
// letter would fire the image viewer on every 'o'. This is the regression the
// review called out.
func TestCtrlOIsNotABareLetterKey(t *testing.T) {
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

	for _, ev := range collect(func(w io.Writer) { _, _ = w.Write([]byte("o")) }) {
		if ev.kind == inputEventHotkey && ev.hotkey == hotkeyOpenImage {
			t.Fatal("a bare 'o' must not produce the open-image hotkey")
		}
	}

	found := false
	for _, ev := range collect(func(w io.Writer) { _, _ = w.Write([]byte{0x0f}) }) {
		if ev.kind == inputEventHotkey && ev.hotkey == hotkeyOpenImage {
			found = true
		}
	}
	if !found {
		t.Fatal("Ctrl+O (0x0f) did not produce the open-image hotkey")
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
