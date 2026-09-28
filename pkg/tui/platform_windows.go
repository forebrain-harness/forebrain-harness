//go:build windows

package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
)

// drainImmediatelyAvailableInput is a no-op on Windows because the Windows
// poll fallback cannot distinguish ready input from a blocking console read.
func drainImmediatelyAvailableInput(in *os.File) {
	_ = in
}

// canGateTerminalReads is false on Windows: pollReadableInputFD cannot tell a
// ready console from an idle one, so a caller that "polls" then reads would
// block indefinitely. Terminal queries are skipped there instead.
const canGateTerminalReads = false

// pollReadableInputFD is a no-op on Windows: callers fall back to a plain
// blocking Read. The Unix variant uses unix.Poll to gate Reads so paused
// raw selector sessions are not racing against this goroutine.
func pollReadableInputFD(fd int, timeoutMs int) (bool, error) {
	_ = fd
	_ = timeoutMs
	return true, nil
}

func attachProcessStderrToErrorLog(logDir string) func() {
	lf, err := os.OpenFile(filepath.Join(logDir, "error.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return func() {}
	}
	prevH, ghErr := windows.GetStdHandle(windows.STD_ERROR_HANDLE)
	if ghErr != nil {
		prevH = windows.Handle(os.Stderr.Fd())
	}
	if prevH == 0 || prevH == windows.InvalidHandle {
		if cerr := lf.Close(); cerr != nil {
			return func() {}
		}
		return func() {}
	}
	logH := windows.Handle(lf.Fd())
	if err := windows.SetStdHandle(windows.STD_ERROR_HANDLE, logH); err != nil {
		if cerr := lf.Close(); cerr != nil {
			return func() {}
		}
		return func() {}
	}
	prevStderr := os.Stderr
	os.Stderr = lf
	return func() {
		rerr := windows.SetStdHandle(windows.STD_ERROR_HANDLE, prevH)
		os.Stderr = prevStderr
		cerr := lf.Close()
		if rerr != nil || cerr != nil {
			if _, werr := fmt.Fprintf(prevStderr, "forebrain: stderr restore: setStd=%v closeLog=%v\n", rerr, cerr); werr != nil {
				return
			}
		}
	}
}

// watchTerminalResize repaints the viewport when the terminal size changes.
// Windows has no SIGWINCH, so this polls the terminal dimensions at a low
// cadence and repaints only when they differ from the last observed size. The
// repaint itself is mutex-guarded in the renderer. No-op for the
// non-interactive renderer (ViewportResize returns early when viewportMode is
// off).
func watchTerminalResize(ctx context.Context, renderer *Renderer) {
	if renderer == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		lastW, lastH := termWidthOrDefault(), termHeightOrDefault()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				w, h := termWidthOrDefault(), termHeightOrDefault()
				if w != lastW || h != lastH {
					lastW, lastH = w, h
					renderer.ViewportResize()
				}
			}
		}
	}()
}

func (s *rawTerminalSession) FlushInput() error {
	if s == nil {
		return nil
	}
	return windows.FlushConsoleInputBuffer(windows.Handle(uintptr(s.fd)))
}
