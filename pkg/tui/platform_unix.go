//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris || aix

package tui

import (
	"context"
	"os"
	"os/signal"
	"time"

	"golang.org/x/sys/unix"
)

const (
	// drainEscapeTailGraceMs bridges the sub-millisecond gaps inside a single
	// terminal escape burst. A tty does not deliver ESC [ B atomically, so a
	// zero-timeout poll can stop mid-sequence and leave the final byte behind
	// for the modal to read as a plain rune ('B') instead of an arrow key.
	drainEscapeTailGraceMs = 15
	// drainBudget bounds the whole drain so a peer that writes without pause
	// cannot keep a modal from reaching its first paint.
	drainBudget = 250 * time.Millisecond
)

// drainImmediatelyAvailableInput discards every byte that is ready on in without
// waiting for future terminal input. It is called while stdinReadMu is held, so
// no other input reader can consume a partial escape sequence concurrently.
//
// Once anything has been discarded the poll switches to a short grace timeout so
// the tail of an in-flight escape sequence is consumed with its prefix rather
// than surviving into the modal's key loop.
func drainImmediatelyAvailableInput(in *os.File) {
	if in == nil {
		return
	}

	fd := int(in.Fd())
	buf := make([]byte, 4096)
	deadline := time.Now().Add(drainBudget)
	timeoutMs := 0
	for {
		readable, err := pollReadableInputFD(fd, timeoutMs)
		if err != nil || !readable {
			return
		}
		n, err := in.Read(buf)
		if n == 0 || err != nil {
			return
		}
		if !time.Now().Before(deadline) {
			return
		}
		timeoutMs = drainEscapeTailGraceMs
	}
}

// canGateTerminalReads reports whether pollReadableInputFD really gates a read
// on readiness. Where it does, code that queries the terminal can collect the
// answer without ever blocking in a read it would have to abandon; where it
// does not, such a query must be skipped entirely rather than leave a reader
// parked on the terminal fd stealing the user's keystrokes.
const canGateTerminalReads = true

// pollReadableInputFD waits up to timeoutMs for the fd to become readable.
// Returns (true, nil) when at least one byte is available, (false, nil) on
// timeout, and (_, err) on a real poll error (EINTR is treated as a timeout).
func pollReadableInputFD(fd int, timeoutMs int) (bool, error) {
	fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	n, err := unix.Poll(fds, timeoutMs)
	if err != nil {
		if err == unix.EINTR {
			return false, nil
		}
		return false, err
	}
	if n > 0 && fds[0].Revents&(unix.POLLIN|unix.POLLHUP|unix.POLLERR) != 0 {
		return true, nil
	}
	return false, nil
}

// watchTerminalResize repaints the viewport on every SIGWINCH (terminal size
// change) until ctx is cancelled. Unix delivers a signal on resize, so this is
// a clean event-driven watch with no polling. The renderer reads the new size
// inside ViewportResize, so a resized terminal re-wraps and re-pins the
// composer immediately. No-op for the non-interactive renderer (ViewportResize
// returns early when viewportMode is off).
func watchTerminalResize(ctx context.Context, renderer *Renderer) {
	if renderer == nil {
		return
	}
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, unix.SIGWINCH)
	go func() {
		defer signal.Stop(ch)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ch:
				renderer.ViewportResize()
			}
		}
	}()
}
