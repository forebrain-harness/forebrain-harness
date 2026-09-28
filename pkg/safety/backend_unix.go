//go:build !windows

package safety

import (
	"context"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// hostShellCommand builds the host shell invocation for a command string. On
// Unix this is the canonical login shell `/bin/sh -lc`; the shell preference
// argument is Windows-only and ignored here.
//
// Cancellation is NOT built in (no exec.CommandContext). The caller must
// set up process-group cancellation via setupProcessGroup / killProcessGroup
// and check ctx.Err() after Wait.
func hostShellCommand(ctx context.Context, command string, _ string) *exec.Cmd {
	return exec.Command("/bin/sh", "-lc", command)
}

func replacePermissionFile(source, target string) error {
	return os.Rename(source, target)
}

// setupProcessGroup configures cmd to create a new process group. The process
// group is used by killProcessGroup to terminate the entire child subtree
// (including grandchildren) when the context is cancelled.
func setupProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killProcessGroup sends SIGKILL to the process group of cmd (identified by
// the negative PID on Unix). It is safe to call multiple times; errors from
// an already-exited or non-existent process are silently ignored.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil || cmd.Process.Pid <= 0 {
		return
	}
	// Negative pid = process group on Unix.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

// DefaultWaitTimeout is the maximum time to wait for a killed process
// group's pipes to drain before the caller stops waiting.
const DefaultWaitTimeout = 3 * time.Second
