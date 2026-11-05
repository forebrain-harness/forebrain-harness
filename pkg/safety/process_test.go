package safety

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
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
	if got := probeSandbox(PlatformLinux); got != "bubblewrap_unusable: exit 1" {
		t.Fatalf("probeSandbox with a bwrap that fails = %q, want bubblewrap_unusable: exit 1", got)
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

// installFakeBubblewrap puts a bubblewrap on PATH that runs body: the probe
// reads only its exit status, so a shell script is the backend as far as it can
// tell.
func installFakeBubblewrap(t *testing.T, dir, body string) {
	t.Helper()
	script := "#!/bin/sh\n" + body
	if err := os.WriteFile(filepath.Join(dir, "bwrap"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake bubblewrap: %v", err)
	}
}

// A probe that fails once is not a machine without a sandbox. The answer is
// cached for the life of the process, and an unusable sandbox means every
// command runs uncontained (plan 215), so one 5s timeout under load would
// remove containment for good. The retry is exactly one, and the answer it
// produces is the one that is cached (plan 225).
func TestProbeRetriesOnceThenCaches(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake bubblewrap is a POSIX shell script")
	}
	dir := t.TempDir()
	// dir first, so lookBubblewrap finds the fake; the system directories stay
	// on PATH because the fake is a shell script and counts with wc.
	t.Setenv("PATH", dir+":/usr/bin:/bin")
	calls := filepath.Join(dir, "calls")

	// The cache is process-wide, so each case starts from unprobed, and the
	// last one leaves it unprobed for whatever runs after this test.
	unprobe := func() {
		sandboxProbe.mu.Lock()
		sandboxProbe.done = false
		sandboxProbe.reason = ""
		sandboxProbe.mu.Unlock()
	}
	unprobe()
	t.Cleanup(unprobe)

	// Launches counts how many times the probe asked the backend.
	launches := func() int {
		b, err := os.ReadFile(calls)
		if err != nil {
			if os.IsNotExist(err) {
				return 0
			}
			t.Fatalf("read probe counter: %v", err)
		}
		return strings.Count(string(b), "\n")
	}

	// The first launch fails and the second succeeds: a probe that lost a race
	// with a host that was busy for a moment.
	installFakeBubblewrap(t, dir, "echo x >> '"+calls+"'\n"+
		"n=$(wc -l < '"+calls+"' | tr -d ' ')\n"+
		"[ \"$n\" -lt 2 ] && exit 1\n"+
		"exit 0\n")
	if got := probeSandboxReason(PlatformLinux); got != "" {
		t.Fatalf("probeSandboxReason after a failed then a successful probe = %q, want \"\"", got)
	}
	if n := launches(); n != 2 {
		t.Fatalf("probe launches = %d, want 2 (one failure, one retry)", n)
	}
	// The retry's success is the cached answer: asking again launches nothing.
	if got := probeSandboxReason(PlatformLinux); got != "" {
		t.Fatalf("cached answer = %q, want \"\"", got)
	}
	if n := launches(); n != 2 {
		t.Fatalf("probe launches after reading the cached answer = %d, want 2", n)
	}

	// Two failures are the machine's answer, and the second one is the last:
	// the reason is cached rather than probed a third time.
	if err := os.Remove(calls); err != nil {
		t.Fatalf("reset probe counter: %v", err)
	}
	installFakeBubblewrap(t, dir, "echo x >> '"+calls+"'\nexit 1\n")
	unprobe()
	reason := probeSandboxReason(PlatformLinux)
	if !strings.HasPrefix(reason, "bubblewrap_unusable") {
		t.Fatalf("reason after two failed probes = %q, want bubblewrap_unusable", reason)
	}
	if n := launches(); n != 2 {
		t.Fatalf("probe launches = %d, want 2 (one failure, one retry)", n)
	}
	if again := probeSandboxReason(PlatformLinux); again != reason {
		t.Fatalf("cached reason = %q, want %q", again, reason)
	}
	if n := launches(); n != 2 {
		t.Fatalf("probe launches after reading the cached failure = %d, want 2", n)
	}
}

// A probe's failure now says which failure it was: "unusable" alone sent a
// maintainer looking for a missing binary when the probe had timed out on a
// busy host.
func TestProbeFailureReasonNamesTheCause(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake bubblewrap is a POSIX shell script")
	}
	dir := t.TempDir()
	t.Setenv("PATH", dir+":/usr/bin:/bin")

	installFakeBubblewrap(t, dir, "exit 3\n")
	if got := probeSandbox(PlatformLinux); got != "bubblewrap_unusable: exit 3" {
		t.Fatalf("probeSandbox for a backend that exits 3 = %q, want bubblewrap_unusable: exit 3", got)
	}

	// A process killed at the deadline reports a signal, not an exit status:
	// what the probe names is the deadline it hit, and never the command's own
	// output (it can contain paths, and this string is shown).
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := exec.CommandContext(ctx, "/bin/sh", "-c", "exec sleep 30").Run(); err == nil {
		t.Fatal("a command that outlives its deadline returned no error")
	} else if got := sandboxProbeFailureDetail(ctx, err); got != "timeout" {
		t.Fatalf("detail for a probe that ran out of time = %q, want timeout", got)
	}
}

// A suffixed code is still shown as a sentence: /status and /sandbox print
// whatever SandboxReasonInWords returns, so the discriminator has to be worded
// too or those surfaces would show the raw code.
func TestSandboxReasonInWordsTranslatesProbeDetail(t *testing.T) {
	timeout := SandboxReasonInWords("bubblewrap_unusable: timeout")
	if strings.Contains(timeout, "bubblewrap_unusable") || !strings.Contains(timeout, "timed out after 5s") {
		t.Fatalf(`SandboxReasonInWords("bubblewrap_unusable: timeout") = %q, want a sentence naming the timeout`, timeout)
	}
	exited := SandboxReasonInWords("seatbelt_unusable: exit 64")
	if strings.Contains(exited, "seatbelt_unusable") || !strings.Contains(exited, "exited with code 64") {
		t.Fatalf(`SandboxReasonInWords("seatbelt_unusable: exit 64") = %q, want a sentence naming the exit code`, exited)
	}
	// A code the table does not know is still shown as it is, suffix and all.
	if got := SandboxReasonInWords("some_future_code: exit 2"); got != "some_future_code: exit 2" {
		t.Fatalf("SandboxReasonInWords for an unknown code = %q, want it verbatim", got)
	}
}

// The one unavailable answer the probe does not produce. With no configuration
// at all, a sandboxed decision fails closed and says why in words; the fallback
// is deliberate (plan 225), not an accident of a missing config, so it is
// pinned here rather than left to the code string reaching a surface raw.
func TestDecideWithoutConfigFailsClosedAndSaysWhy(t *testing.T) {
	decision := NewManager().Decide(nil, ToolKindShell)
	if decision.UseSandbox || decision.Reason != "sandbox_unavailable" {
		t.Fatalf("Decide with no config must not fall back to the host: %+v", decision)
	}
	if decision.UnavailableReason == "" {
		t.Fatalf("Decide with no config must say why: %+v", decision)
	}
	if words := SandboxReasonInWords(decision.UnavailableReason); words == decision.UnavailableReason {
		t.Fatalf("reason %q is shown as a code, with no words for a surface", decision.UnavailableReason)
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
