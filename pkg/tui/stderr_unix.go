//go:build !windows

package tui

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

var (
	unixOpenFile = os.OpenFile
	unixDup      = unix.Dup
	unixDup2     = unix.Dup2
	unixClose    = unix.Close
	stderrFD     = func() int { return int(os.Stderr.Fd()) }
)

func attachProcessStderrToErrorLog(logDir string) func() {
	lf, err := unixOpenFile(filepath.Join(logDir, "error.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return func() {}
	}
	fd := stderrFD()
	orig, err := unixDup(fd)
	if err != nil {
		_ = fileClose(lf)
		return func() {}
	}
	if err := unixDup2(int(lf.Fd()), fd); err != nil {
		_ = unixClose(orig)
		_ = fileClose(lf)
		return func() {}
	}
	return func() {
		_ = unixDup2(orig, fd)
		_ = unixClose(orig)
		_ = fileClose(lf)
	}
}
