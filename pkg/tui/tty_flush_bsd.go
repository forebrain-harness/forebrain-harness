//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package tui

import "golang.org/x/sys/unix"

func (s *rawTerminalSession) FlushInput() error {
	if s == nil {
		return nil
	}
	return unix.IoctlSetPointerInt(s.fd, unix.TIOCFLUSH, unix.TCIFLUSH)
}
