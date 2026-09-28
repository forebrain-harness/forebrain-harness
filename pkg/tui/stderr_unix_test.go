//go:build !windows

package tui

import (
	"errors"
	"os"
	"testing"
)

// The vars this test swaps (unixOpenFile, unixDup, unixDup2, unixClose,
// stderrFD) live in stderr_unix.go behind the same constraint; the test never
// compiles on Windows, where attachProcessStderrToErrorLog has its own
// platform_windows.go half.
func TestAttachProcessStderrToErrorLog(t *testing.T) {
	origAttach := attachStderrToError
	t.Cleanup(func() { attachStderrToError = origAttach })
	origStderrFD := stderrFD
	t.Cleanup(func() { stderrFD = origStderrFD })

	t.Run("open fail", func(t *testing.T) {
		origOpen := unixOpenFile
		t.Cleanup(func() { unixOpenFile = origOpen })
		unixOpenFile = func(string, int, os.FileMode) (*os.File, error) {
			return nil, errors.New("open failed")
		}
		restore := attachProcessStderrToErrorLog(t.TempDir())
		restore()
	})

	t.Run("dup fail", func(t *testing.T) {
		origOpen := unixOpenFile
		origDup := unixDup
		t.Cleanup(func() {
			unixOpenFile = origOpen
			unixDup = origDup
		})
		unixOpenFile = func(name string, flag int, perm os.FileMode) (*os.File, error) {
			return os.OpenFile(name, flag, perm)
		}
		unixDup = func(int) (int, error) {
			return 0, errors.New("dup failed")
		}
		restore := attachProcessStderrToErrorLog(t.TempDir())
		restore()
	})

	t.Run("dup2 fail", func(t *testing.T) {
		origOpen := unixOpenFile
		origDup := unixDup
		origDup2 := unixDup2
		origClose := unixClose
		t.Cleanup(func() {
			unixOpenFile = origOpen
			unixDup = origDup
			unixDup2 = origDup2
			unixClose = origClose
		})
		unixOpenFile = func(name string, flag int, perm os.FileMode) (*os.File, error) {
			return os.OpenFile(name, flag, perm)
		}
		unixDup = func(int) (int, error) {
			return 12345, nil
		}
		unixDup2 = func(int, int) error {
			return errors.New("dup2 failed")
		}
		unixClose = func(int) error { return nil }
		restore := attachProcessStderrToErrorLog(t.TempDir())
		restore()
	})

	t.Run("restore path", func(t *testing.T) {
		attachStderrToError = func(string) func() { return func() {} }
		origOpen := unixOpenFile
		origDup := unixDup
		origDup2 := unixDup2
		origClose := unixClose
		origFD := stderrFD
		t.Cleanup(func() {
			unixOpenFile = origOpen
			unixDup = origDup
			unixDup2 = origDup2
			unixClose = origClose
			stderrFD = origFD
		})
		unixOpenFile = func(name string, flag int, perm os.FileMode) (*os.File, error) {
			return os.OpenFile(name, flag, perm)
		}
		unixDup = func(int) (int, error) { return 2, nil }
		unixDup2 = func(int, int) error { return nil }
		unixClose = func(int) error { return errors.New("close failed") }
		stderrFD = func() int {
			f := createTempLog(t)
			t.Cleanup(func() { _ = f.Close() })
			return int(f.Fd())
		}
		restore := attachProcessStderrToErrorLog(t.TempDir())
		restore()
	})
}
