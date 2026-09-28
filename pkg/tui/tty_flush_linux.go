//go:build linux || aix || solaris

package tui

import "golang.org/x/sys/unix"

func (s *rawTerminalSession) FlushInput() error {
	if s == nil {
		return nil
	}
	return unix.IoctlSetInt(s.fd, unix.TCFLSH, unix.TCIFLUSH)
}
