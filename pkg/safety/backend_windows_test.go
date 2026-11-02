//go:build windows

package safety

import (
	"testing"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
)

func TestWindowsCapabilityPolicyKeyIncludesRelevantDenials(t *testing.T) {
	t.Parallel()

	root := `C:\workspace`
	plain := windowsCapabilityPolicyKey(root, nil)
	unrelated := windowsCapabilityPolicyKey(root, []string{`D:\secret`})
	if unrelated != plain {
		t.Fatalf("unrelated denial changed capability key: %q != %q", unrelated, plain)
	}

	first := windowsCapabilityPolicyKey(root, []string{`C:\workspace\a`, `C:\workspace\b`})
	reordered := windowsCapabilityPolicyKey(root, []string{`C:\workspace\b`, `C:\workspace\a`})
	if first != reordered {
		t.Fatalf("denial order changed capability key: %q != %q", first, reordered)
	}
	if first == plain {
		t.Fatal("relevant denial did not isolate the capability SID")
	}
}

// A workspace-write preset stays writable on Windows now that the
// restricted-token sandbox is always there: it is no longer gated on a
// configured windows.sandbox (plan 206).
func TestWindowsWorkspaceWriteProfileIsWritable(t *testing.T) {
	t.Parallel()

	cfg := &appcfg.Root{SandboxMode: appcfg.SandboxModeWorkspaceWrite}
	if got := profileForSandboxMode(cfg); got != ProfileWorkspaceWrite {
		t.Fatalf("profile = %q, want %q", got, ProfileWorkspaceWrite)
	}
	if decision := NewManager().DecideShellCommand(cfg, "", SandboxPermissionsUseDefault, "", false); decision.Reason == "windows_sandbox_disabled" {
		t.Fatalf("the Windows sandbox must not report itself as disabled: %+v", decision)
	}
}

// Windows never says the sandbox took the network away: a restricted token
// cannot, so the sentence would describe isolation that is not there (plan 206
// §3.2, owner 2026-10-31).
func TestWindowsNetworkInWordsIsAlwaysOpen(t *testing.T) {
	t.Parallel()

	blocked := &appcfg.Root{SandboxWorkspaceWrite: appcfg.SandboxWorkspaceWrite{NetworkAccess: appcfg.BoolPtr(false)}}
	if got := SandboxNetworkInWords(blocked); got != "commands can reach the internet." {
		t.Fatalf("network words = %q", got)
	}
}

// The batch caveat is stated rather than left to be discovered: writes stay
// inside the task's directory, reads are not isolated (plan 206 §3.3).
func TestWindowsDoctorStatesBatchReadsAreNotIsolated(t *testing.T) {
	t.Parallel()

	report := NewManager().Doctor(nil)
	found := false
	for _, note := range report.DependencyWarning {
		if note == "windows_batch_reads_not_isolated" {
			found = true
		}
	}
	if !found {
		t.Fatalf("doctor warnings = %v, want the batch read caveat", report.DependencyWarning)
	}
	if words := SandboxReasonInWords("windows_batch_reads_not_isolated"); words == "windows_batch_reads_not_isolated" {
		t.Fatal("the batch read caveat needs a sentence of its own")
	}
}
