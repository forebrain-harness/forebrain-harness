package tui

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// imagePreviewOpener launches a file with the operating system's viewer. It is
// a variable so a test can inject a fake and assert whether (and what) it was
// asked to open; production uses the platform's own handler.
var imagePreviewOpener = openWithSystemViewer

// openWithSystemViewer opens path with the platform's default application.
// Starting rather than waiting is deliberate: the viewer owns the screen until
// the user closes it, and the terminal must stay interactive behind it.
func openWithSystemViewer(ctx context.Context, path string) error {
	name, args := imageViewerCommand(path)
	if name == "" {
		return fmt.Errorf("no system viewer is available on this platform")
	}
	return exec.CommandContext(ctx, name, args...).Start()
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

// LatestImagePreview returns the absolute path and display label of the most
// recent image step drawn in the active view, or ("", "") when the view holds
// none. The terminal has no notion of a selected step, so "open the image"
// means the newest one — the screenshot the user just watched the model read.
func (r *Renderer) LatestImagePreview() (path, label string) {
	if r == nil {
		return "", ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	vm := r.activeVMLocked()
	if vm == nil {
		return "", ""
	}
	for i := len(vm.blocks) - 1; i >= 0; i-- {
		b := vm.blocks[i]
		if b == nil || b.frame.Kind != FrameTool {
			continue
		}
		meta := b.frame.ToolMeta
		if !strings.EqualFold(strings.TrimSpace(meta.PreviewKind), "image") {
			continue
		}
		p := strings.TrimSpace(meta.PreviewPath)
		if p == "" {
			continue
		}
		return p, imagePreviewLabel(meta)
	}
	return "", ""
}

// openLatestImagePreview is the Ctrl+O handler: it opens the most recent image
// step of the active view with the system viewer, or, when the view holds no
// image step, prints one line and spawns nothing.
func openLatestImagePreview(renderer *Renderer, state *streamState) {
	if renderer == nil {
		return
	}
	path, label := renderer.LatestImagePreview()
	if strings.TrimSpace(path) == "" {
		renderer.RenderFrame(Frame{
			Kind:    FrameStatus,
			Title:   "open image",
			Content: "no image to open yet",
			Final:   true,
		})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := imagePreviewOpener(ctx, path); err != nil {
		renderer.PrintError(fmt.Errorf("open %s: %w", label, err))
	}
}

// imagePreviewFrameLine is the one-line marker an image step shows in place of
// a text body: the file, its pixel size, its encoded size, and the key that
// opens it. It never carries base64.
func imagePreviewFrameLine(meta tool.ToolMeta) string {
	line := fmt.Sprintf("▤ %s · %d×%d · %s",
		imagePreviewLabel(meta), meta.PreviewWidth, meta.PreviewHeight, humanBytesLabel(meta.PreviewBytes))
	if meta.PreviewScaled {
		line += " · scaled"
	}
	return line + " · Ctrl+O 打开"
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
