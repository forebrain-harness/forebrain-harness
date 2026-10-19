package tui

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// imagePreviewOpener launches a file with the operating system's viewer. It is
// a variable so a test can inject a fake and assert whether (and what) it was
// asked to open; production uses the platform's own handler. An error it
// returns is a launch that never happened; failed is called, from another
// goroutine, when a launch that did start ends unsuccessfully.
var imagePreviewOpener = openWithSystemViewer

// openWithSystemViewer opens path with the platform's default application.
// Starting rather than waiting is deliberate: the viewer owns the screen until
// the user closes it, and the terminal must stay interactive behind it.
//
// The file is checked first. A marker line outlives its file — a command the
// agent is running may be rebuilding the very directory it was read from — and
// the platform launcher reports a missing file only through its exit status
// (macOS `open` prints "The file … does not exist." and exits 1), which a
// click that only started it never saw: the click did nothing at all.
func openWithSystemViewer(path string, failed func(error)) error {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return errors.New("file no longer exists")
		}
		return err
	}
	name, args := imageViewerCommand(path)
	if name == "" {
		return fmt.Errorf("no system viewer is available on this platform")
	}
	return startDetached(failed, name, args...)
}

// startDetached starts a program that has to outlive its caller: Start returns
// as soon as the process is running and a goroutine reaps it, so it is never
// left as a zombie. It deliberately takes no context — a cancelable command
// context kills the child the moment the caller returns, and a launcher's whole
// job (handing the file to the platform) happens after that. That was the bug
// behind "open image does nothing": the handler cancelled its own timeout
// context a moment after Start, SIGKILLing a viewer that had not opened the
// file yet.
//
// A launcher checks what it was handed only once it is running — the file,
// a handler for its type — so how it ended is part of the answer: an
// unsuccessful exit goes to failed, with what the launcher said on stderr.
func startDetached(failed func(error), name string, args ...string) error {
	cmd := exec.Command(name, args...)
	stderr := &headBuffer{max: 2048}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() {
		err := cmd.Wait()
		if err == nil || failed == nil {
			return
		}
		if said := strings.TrimSpace(string(stderr.buf)); said != "" {
			err = errors.New(said)
		}
		failed(err)
	}()
	return nil
}

// headBuffer keeps the first max bytes written to it and drops the rest, so a
// launcher that hands the file to a viewer inheriting its stderr cannot grow it
// without bound.
type headBuffer struct {
	buf []byte
	max int
}

func (h *headBuffer) Write(p []byte) (int, error) {
	if room := h.max - len(h.buf); room > 0 {
		h.buf = append(h.buf, p[:min(room, len(p))]...)
	}
	return len(p), nil
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

// openImagePreview hands one already-resolved image step to the system viewer.
// A click resolves it on the screen its press landed on (viewportRelease). A
// miss is not an error worth a status line: the marker line is the affordance,
// so nothing at all happens when no file was named. A file that was named and
// could not be opened is said, whether the launch failed to start or the
// launcher refused it afterwards.
func openImagePreview(renderer *Renderer, path, label string) {
	if renderer == nil || strings.TrimSpace(path) == "" {
		return
	}
	report := func(err error) {
		renderer.PrintError(fmt.Errorf("open %s: %w", label, err))
	}
	if err := imagePreviewOpener(path, report); err != nil {
		report(err)
	}
}

// imagePreviewFrameLine is the one-line marker an image step shows in place of
// a text body: the file, its pixel size, its encoded size, and the affordance
// that opens it. It never carries base64.
//
// The pixel size appears only when both dimensions are known: a surface omits
// them rather than showing 0×0 — an image whose header could not be parsed
// behind sniffable magic bytes, or an MCP degrade path, reports no size. The
// web card (ToolCallCard.vue) omits the segment the same way.
func imagePreviewFrameLine(meta tool.ToolMeta) string {
	dims := ""
	if meta.PreviewWidth > 0 && meta.PreviewHeight > 0 {
		dims = fmt.Sprintf("%d×%d · ", meta.PreviewWidth, meta.PreviewHeight)
	}
	line := fmt.Sprintf("▤ %s · %s%s", imagePreviewLabel(meta), dims, humanBytesLabel(meta.PreviewBytes))
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
