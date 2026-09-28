//go:build !unix

package safety

import "os/exec"

func processSignal(_ *exec.ExitError) (int, bool) {
	return 0, false
}

func sandboxSIGSYSExitCode() int {
	return -1
}
