package safety

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// A sandbox the presence check can see but cannot run is the case a bare
// LookPath answers wrong: containers ship bubblewrap and then refuse its
// namespaces. The probe has to tell the two apart, because "installed" used to
// mean every command was attempted and failed.
func TestProbeSandboxTellsInstalledFromUsable(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)

	// Nothing installed at all.
	if got := probeSandbox(PlatformLinux); got != "bubblewrap_not_found" {
		t.Fatalf("probeSandbox with no bubblewrap = %q, want bubblewrap_not_found", got)
	}

	// Installed, but the command it starts fails the way a blocked namespace
	// makes it fail.
	fake := filepath.Join(dir, "bwrap")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write fake bubblewrap: %v", err)
	}
	if got := probeSandbox(PlatformLinux); got != "bubblewrap_unusable" {
		t.Fatalf("probeSandbox with a bwrap that fails = %q, want bubblewrap_unusable", got)
	}

	// Installed and it really runs.
	ok := "#!/bin/sh\nexit 0\n"
	if runtime.GOOS == "windows" {
		ok = "@exit 0\r\n"
	}
	if err := os.WriteFile(fake, []byte(ok), 0o755); err != nil {
		t.Fatalf("write working fake bubblewrap: %v", err)
	}
	if got := probeSandbox(PlatformLinux); got != "" {
		t.Fatalf("probeSandbox with a working bwrap = %q, want a usable sandbox", got)
	}
}

// The cached answer is what the rest of the process reads, and the reason it
// carries is the one the sandbox report words for the user.
func TestSandboxAvailableReadsTheProbe(t *testing.T) {
	restore := SetSandboxAvailableForTest(false)
	if SandboxAvailable() {
		t.Fatal("a pinned unavailable machine still reports a sandbox")
	}
	if platform := detectPlatform(); platform != PlatformWindows {
		reason := backendUnavailableReason(platform)
		if reason == "" {
			t.Fatalf("backendUnavailableReason on %s = %q with no usable sandbox", platform, reason)
		}
		if SandboxReasonInWords(reason) == reason {
			t.Fatalf("reason %q has no words", reason)
		}
	}
	restore()

	if runtime.GOOS == "darwin" {
		// This machine ships sandbox-exec, and the probe runs it for real.
		if got := probeSandbox(PlatformDarwin); got != "" {
			t.Fatalf("probeSandbox(PlatformDarwin) = %q on macOS", got)
		}
	}
}
