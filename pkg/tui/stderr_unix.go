//go:build !windows

package tui

import (
	"os"

	"golang.org/x/sys/unix"
)

var (
	unixOpenFile = os.OpenFile
	unixDup      = unix.Dup
	unixDup2     = unix.Dup2
	unixClose    = unix.Close
	stderrFD     = func() int { return int(os.Stderr.Fd()) }
)

// attachProcessStderr points the process's stderr descriptor at path: a panic's
// trace and a C library's complaint reach the file even if the Go side is
// already dying. The returned func puts the original descriptor back and closes
// the file.
func attachProcessStderr(path string) func() {
	lf, err := unixOpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return func() {}
	}
	fd := stderrFD()
	orig, err := unixDup(fd)
	if err != nil {
		_ = lf.Close()
		return func() {}
	}
	if err := unixDup2(int(lf.Fd()), fd); err != nil {
		_ = unixClose(orig)
		_ = lf.Close()
		return func() {}
	}
	return func() {
		_ = unixDup2(orig, fd)
		_ = unixClose(orig)
		_ = lf.Close()
	}
}
