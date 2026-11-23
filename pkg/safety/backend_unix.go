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
// set up process-group cancellation via setupProcessGroup / killCommandGroup
// and check ctx.Err() after Wait.
func hostShellCommand(ctx context.Context, command string, _ string) *exec.Cmd {
	cmd := exec.Command("/bin/sh", "-lc", command)
	// A descendant may inherit the output pipes and outlive the shell; without a
	// bound, cmd.Wait reads them until every holder exits (DefaultWaitTimeout).
	cmd.WaitDelay = DefaultWaitTimeout
	return cmd
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

// killGrace bounds how long killCommandGroup waits before signalling a command's
// process group a second time. A package-level variable only so tests can
// shorten it; production code must not write it.
var killGrace = 100 * time.Millisecond

// killCommandGroup ends a command's process tree. It signals the group, waits
// killGrace, and signals it again.
//
// One signal is not enough: a descendant forked while it was in flight is in the
// group but missed the delivery, and the output pipe it holds is what keeps
// cmd.Wait from returning (os/exec reads those pipes through its own goroutines,
// so Wait ends only when every holder exits). The second signal catches that
// straggler.
//
// The second signal is withheld as soon as reaped is closed, which means Wait has
// returned. Signalling the group after that is how a cancellation watcher kills a
// PID — and with it a process-group ID — the kernel has already recycled (see
// waitForCommandContext), and no grace is short enough to make that safe.
//
// While the wait is outstanding the group is still this command's to end: the
// leader's PID is only recycled once its slot comes round again in the kernel's
// sequential allocation, which is not something one killGrace can span.
func killCommandGroup(cmd *exec.Cmd, reaped <-chan struct{}) {
	killProcessGroup(cmd)
	if cmd == nil || cmd.Process == nil || cmd.Process.Pid <= 0 {
		return
	}
	select {
	case <-reaped:
		return
	case <-time.After(killGrace):
	}
	// Re-checked immediately before the signal, so the wait is still outstanding
	// at the moment the group is signalled again.
	select {
	case <-reaped:
		return
	default:
	}
	killProcessGroup(cmd)
}

// DefaultWaitTimeout is the maximum time to wait for a killed process
// group's pipes to drain before the caller stops waiting.
const DefaultWaitTimeout = 3 * time.Second
