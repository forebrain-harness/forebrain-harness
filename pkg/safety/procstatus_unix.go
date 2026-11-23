//go:build unix

package safety

import "syscall"

func sandboxSIGSYSExitCode() int {
	return 128 + int(syscall.SIGSYS)
}
