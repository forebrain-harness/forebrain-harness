//go:build unix

package safety

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
)

func TestRunHostCommandBoundsHundredMegabyteStderr(t *testing.T) {
	if os.Getenv("FOREBRAIN_RUN_STRESS_TESTS") != "1" {
		t.Skip("set FOREBRAIN_RUN_STRESS_TESTS=1 to run the 100 MiB output stress test")
	}
	result, err := runHostCommand(
		context.Background(), t.TempDir(), 30*time.Second,
		"head -c 104857600 /dev/zero >&2", nil, "", nil, t.TempDir(),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(result.OutputSpoolPath)
	if result.StderrBytes != 100*1024*1024 {
		t.Fatalf("stderr bytes=%d", result.StderrBytes)
	}
	if len(result.Stderr) > commandPreviewHeadBytes+commandPreviewTailBytes+100 || result.StderrOmittedBytes <= 0 {
		t.Fatalf("preview bytes=%d omitted=%d", len(result.Stderr), result.StderrOmittedBytes)
	}
	if result.OutputSpoolBytes != commandSpoolMaxBytes || result.OutputSpoolOmittedBytes != 36*1024*1024 {
		t.Fatalf("spool bytes=%d omitted=%d", result.OutputSpoolBytes, result.OutputSpoolOmittedBytes)
	}
}

func TestRunHostCommandPreservesSignalExitCode(t *testing.T) {
	signal := int(syscall.SIGSYS)
	result, err := runHostCommand(
		context.Background(),
		t.TempDir(),
		5*time.Second,
		fmt.Sprintf("kill -%d $$", signal),
		nil,
		"",
		nil,
	)

	var signalErr *SignalError
	if !errors.As(err, &signalErr) {
		t.Fatalf("error = %v, want SignalError", err)
	}
	if signalErr.Signal != signal {
		t.Fatalf("signal = %d, want %d", signalErr.Signal, signal)
	}
	if result.ExitCode != 128+signal {
		t.Fatalf("exit code = %d, want %d", result.ExitCode, 128+signal)
	}
	if runtime.GOOS == "linux" && !IsLikelySandboxDenied(SandboxDecision{UseSandbox: true, SandboxType: SandboxTypeLinuxSeccomp}, result) {
		t.Fatal("SIGSYS result was not classified as a sandbox denial")
	}
}

// A command is over when its process is over. os/exec reads the command's output
// pipes through its own goroutines, so a descendant that inherited stdout and
// outlived the command kept cmd.Wait blocked for as long as that descendant
// lived — and with it the ledger entry, the log and the goroutine that was
// waiting. The bound is DefaultWaitTimeout, and the abandoned pipe is not a
// failure of the command: the shell exited 0, so this reports exit 0 with no
// error.
func TestWaitIsBoundedByADescendantHoldingThePipes(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "holder.pid")

	started := time.Now()
	handle, err := NewManager().StartCommand(context.Background(), &appcfg.Root{SandboxMode: appcfg.SandboxModeDangerFullAccess}, CommandRequest{
		ToolKind: ToolKindShell,
		Command:  "sleep 60 & echo $! > '" + pidFile + "'; echo hi",
		WorkDir:  dir,
	})
	if err != nil {
		t.Fatalf("StartCommand: %v", err)
	}
	t.Cleanup(func() { killByPIDFile(t, pidFile) })

	select {
	case <-handle.Done():
	case <-time.After(DefaultWaitTimeout + 5*time.Second):
		t.Fatalf("the wait outlived DefaultWaitTimeout (%s): the descendant that holds the pipes still owns it", DefaultWaitTimeout)
	}
	if elapsed := time.Since(started); elapsed < DefaultWaitTimeout-time.Second || elapsed > DefaultWaitTimeout+3*time.Second {
		t.Fatalf("the wait returned after %s, want about DefaultWaitTimeout (%s): the bound is what ended it, not the descendant", elapsed, DefaultWaitTimeout)
	}
	res, waitErr := handle.Wait(context.Background())
	if waitErr != nil {
		t.Fatalf("a command whose descendant kept the pipes reported %v, want no error: the command itself exited 0", waitErr)
	}
	if res.ExitCode != 0 || !strings.Contains(res.AggregatedOutput, "hi") {
		t.Fatalf("exit=%d output=%q, want exit 0 carrying the command's own output", res.ExitCode, res.AggregatedOutput)
	}
}

// The kill path has the same wedge. A descendant that inherited the pipes and is
// out of the command's process group — its own session here, which is what a
// daemonized helper does — is not reached by the group's SIGKILL, and before
// cmd.WaitDelay the wait, the ledger entry and the waiting goroutine lived as
// long as that descendant did. The kill returns, the wait ends at
// DefaultWaitTimeout, and the killed command keeps the exit its own signal
// produced.
func TestKillIsBoundedWhenAPipeHolderEscapesTheGroup(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "holder.pid")

	handle, err := NewManager().StartCommand(context.Background(), &appcfg.Root{SandboxMode: appcfg.SandboxModeDangerFullAccess}, CommandRequest{
		ToolKind: ToolKindShell,
		Command:  "'" + strings.ReplaceAll(os.Args[0], "'", `'\''`) + "' -test.run=TestSafetyPipeHolderHelper$",
		WorkDir:  dir,
		Env:      []string{"SAFETY_HOLDER_ROLE=leader", "SAFETY_HOLDER_DIR=" + dir},
	})
	if err != nil {
		t.Fatalf("StartCommand: %v", err)
	}
	if !waitFor(func() bool { _, ok := readPID(pidFile); return ok }, 10*time.Second) {
		t.Fatalf("the command never forked its pipe holder: %s", helperDiagnosis(dir))
	}
	t.Cleanup(func() { killByPIDFile(t, pidFile) })

	if err := handle.Kill(); err != nil {
		t.Fatalf("kill: %v", err)
	}
	started := time.Now()
	select {
	case <-handle.Done():
	case <-time.After(DefaultWaitTimeout + 5*time.Second):
		t.Fatalf("the wait outlived DefaultWaitTimeout (%s) after the kill: the escaped pipe holder still owns it", DefaultWaitTimeout)
	}
	if elapsed := time.Since(started); elapsed < DefaultWaitTimeout-time.Second || elapsed > DefaultWaitTimeout+3*time.Second {
		t.Fatalf("the wait returned %s after the kill, want about DefaultWaitTimeout (%s)", elapsed, DefaultWaitTimeout)
	}
	res, _ := handle.Wait(context.Background())
	if want := 128 + int(syscall.SIGKILL); res.ExitCode != want {
		t.Fatalf("exit=%d, want %d: a killed command keeps the exit its own signal produced", res.ExitCode, want)
	}
	// The holder is out of the group on purpose: it survives the signal, so the
	// bound — not the holder's exit — is what ended the wait.
	holder, ok := readPID(pidFile)
	if !ok || syscall.Kill(holder, 0) != nil {
		t.Fatal("the escaped holder did not outlive the group signal, so the bound was not what ended the wait")
	}
}

// The second group signal is withheld once the command has been waited for: by
// then the kernel may have handed the leader's PID, and with it the group ID, to
// a new group, and the signal would reach a command that has nothing to do with
// this one. Both sides of that gate.
func TestKillCommandGroupWithholdsTheSecondSignalAfterTheWait(t *testing.T) {
	grace := killGrace
	killGrace = 200 * time.Millisecond
	t.Cleanup(func() { killGrace = grace })

	cmd := exec.Command("/bin/sh", "-c", "true")
	setupProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}

	reaped := make(chan struct{})
	close(reaped)
	started := time.Now()
	killCommandGroup(cmd, reaped)
	if elapsed := time.Since(started); elapsed >= killGrace/2 {
		t.Fatalf("killCommandGroup waited %s after the wait returned: the reaped gate did not stop the grace", elapsed)
	}

	// The other side: while the wait is outstanding the grace is paid, because
	// the group is still this command's to signal.
	running := exec.Command("/bin/sh", "-c", "sleep 30")
	setupProcessGroup(running)
	if err := running.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	started = time.Now()
	killCommandGroup(running, make(chan struct{}))
	if elapsed := time.Since(started); elapsed < killGrace/2 {
		t.Fatalf("killCommandGroup returned after %s with the wait outstanding: no grace was paid", elapsed)
	}
	if err := running.Wait(); err == nil {
		t.Fatal("the second signal's command was not killed")
	}
}

// killByPIDFile ends the descendant a test left behind, by the PID its command
// wrote out: nothing else would, and a suite must not leave a sleep running.
func killByPIDFile(t *testing.T, pidFile string) {
	t.Helper()
	pid, ok := readPID(pidFile)
	if !ok {
		return
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
}

// readPID reads a pid a command wrote to a file.
func readPID(path string) (int, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// waitFor polls cond for up to d. These tests coordinate with processes through
// files and PIDs, which have no other way to say that they are ready.
func waitFor(cond func() bool, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

// helperNote records what a spawned helper is or why it stopped, for the failure
// a test reports.
func helperNote(dir, name string, err error) {
	_ = os.WriteFile(filepath.Join(dir, name), []byte(err.Error()), 0o644)
}

// helperDiagnosis is what the helpers left behind, or that they said nothing.
func helperDiagnosis(dir string) string {
	var parts []string
	for _, name := range []string{"holder.pid", "leader.err"} {
		if b, err := os.ReadFile(filepath.Join(dir, name)); err == nil {
			parts = append(parts, name+"="+string(b))
		}
	}
	if len(parts) == 0 {
		return "the helper reported nothing"
	}
	return strings.Join(parts, " ")
}

// TestSafetyPipeHolderHelper is not a test: it is the process
// TestKillIsBoundedWhenAPipeHolderEscapesTheGroup spawns, which cannot be a
// shell script because leaving the command's process group needs setsid.
func TestSafetyPipeHolderHelper(t *testing.T) {
	role := os.Getenv("SAFETY_HOLDER_ROLE")
	if role == "" {
		t.Skip("helper process: spawned by TestKillIsBoundedWhenAPipeHolderEscapesTheGroup")
	}
	dir := os.Getenv("SAFETY_HOLDER_DIR")
	switch role {
	case "leader":
		// The holder inherits this command's stdout, which is the pipe Wait
		// reads, and takes a session of its own: the group signal cannot reach it.
		holder := exec.Command(os.Args[0], "-test.run=TestSafetyPipeHolderHelper$")
		holder.Env = append(os.Environ(), "SAFETY_HOLDER_ROLE=holder")
		holder.Stdout = os.Stdout
		holder.Stderr = os.Stderr
		holder.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := holder.Start(); err != nil {
			helperNote(dir, "leader.err", err)
			os.Exit(2)
		}
		if err := os.WriteFile(filepath.Join(dir, "holder.pid"), []byte(strconv.Itoa(holder.Process.Pid)), 0o644); err != nil {
			helperNote(dir, "leader.err", err)
			os.Exit(3)
		}
	case "holder":
	default:
		os.Exit(2)
	}
	for {
		time.Sleep(time.Minute)
	}
}
