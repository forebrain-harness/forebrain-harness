//go:build unix

package safety

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"syscall"
	"testing"
	"time"
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
