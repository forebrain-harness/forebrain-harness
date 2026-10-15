package tui

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// imagePreviewOpener launches a file with the operating system's viewer. It is
// a variable so a test can inject a fake and assert whether (and what) it was
// asked to open; production uses the platform's own handler.
var imagePreviewOpener = openWithSystemViewer

// openWithSystemViewer opens path with the platform's default application.
// Starting rather than waiting is deliberate: the viewer owns the screen until
// the user closes it, and the terminal must stay interactive behind it.
func openWithSystemViewer(path string) error {
	name, args := imageViewerCommand(path)
	if name == "" {
		return fmt.Errorf("no system viewer is available on this platform")
	}
	return startDetached(name, args...)
}

// startDetached starts a program that has to outlive its caller: Start returns
// as soon as the process is running and a goroutine reaps it, so it is never
// left as a zombie. It deliberately takes no context — a cancelable command
// context kills the child the moment the caller returns, and a launcher's whole
// job (handing the file to the platform) happens after that. That was the bug
// behind "open image does nothing": the handler cancelled its own timeout
// context a moment after Start, SIGKILLing a viewer that had not opened the
// file yet.
func startDetached(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// imageViewerCommand is the platform's "open this file" command.
func imageViewerCommand(path string) (string, []string) {
	switch runtime.GOOS {
	case "darwin":
		return "open", []string{path}
	case "windows":
		return "cmd", []string{"/c", "start", "", path}
	default:
		return "xdg-open", []string{path}
	}
}

// clickImagePreviewRow handles a completed mouse click at a 0-based screen cell:
// it ends the gesture and, when that cell was an image step's marker row, opens
// the file the row names.
//
// The target is resolved BEFORE the gesture ends, on the render the click landed
// on. Ending it may fold or expand the card under the row, and that repaint
// shifts every row below the click — resolving afterwards would name a different
// row than the one the user pressed (clicking the "··· N more lines" hint used to
// expand the card and then open a file the expansion had just revealed).
func clickImagePreviewRow(renderer *Renderer, col, row int) {
	if renderer == nil {
		return
	}
	path, label, isMarker := renderer.ImagePreviewAtScreenRow(row)
	if renderer.ViewportSelectEnd(col, row) && isMarker {
		openImagePreview(renderer, path, label)
	}
}

// openImagePreview hands one already-resolved image step to the system viewer.
// A miss is not an error worth a status line: the marker line is the affordance,
// so nothing at all happens when no file was named.
func openImagePreview(renderer *Renderer, path, label string) {
	if renderer == nil || strings.TrimSpace(path) == "" {
		return
	}
	if err := imagePreviewOpener(path); err != nil {
		renderer.PrintError(fmt.Errorf("open %s: %w", label, err))
	}
}

// imagePreviewFrameLine is the one-line marker an image step shows in place of
// a text body: the file, its pixel size, its encoded size, and the affordance
// that opens it. It never carries base64.
func imagePreviewFrameLine(meta tool.ToolMeta) string {
	line := fmt.Sprintf("▤ %s · %d×%d · %s",
		imagePreviewLabel(meta), meta.PreviewWidth, meta.PreviewHeight, humanBytesLabel(meta.PreviewBytes))
	if meta.PreviewScaled {
		line += " · scaled"
	}
	return line + " · click to open"
}

// imagePreviewLabel names an image for a one-line marker: its file name, or,
// failing that, the invocation the call carried.
func imagePreviewLabel(meta tool.ToolMeta) string {
	if base := strings.TrimSpace(filepath.Base(strings.TrimSpace(meta.PreviewPath))); base != "" && base != "." && base != string(filepath.Separator) {
		return base
	}
	if inv := strings.TrimSpace(meta.Invocation); inv != "" {
		return inv
	}
	return "image"
}

// humanBytesLabel formats a byte count the way every surface does: bytes under
// one KiB, then KB and MB with one decimal.
func humanBytesLabel(n int) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d bytes", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
}
