//go:build unix

package safety

import (
	"os/exec"
	"syscall"
)

func processSignal(exitErr *exec.ExitError) (int, bool) {
	if exitErr == nil {
		return 0, false
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() {
		return 0, false
	}
	return int(status.Signal()), true
}

func sandboxSIGSYSExitCode() int {
	return 128 + int(syscall.SIGSYS)
}
