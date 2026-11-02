package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The approval preset is the only approval setting there is (plan 203), and it
// has exactly two values. Anything else has to be refused at load, so a typo in
// a hand-edited file cannot become a runtime surprise.
func TestApprovalPresetAcceptsTheTwoPresets(t *testing.T) {
	for _, preset := range []ApprovalPresetID{ApprovalPresetWorkspaceWrite, ApprovalPresetFullAccess} {
		path := filepath.Join(t.TempDir(), "forebrain.yaml")
		if err := os.WriteFile(path, []byte("approval_preset: "+string(preset)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := loadDocumentForTest(path)
		if err != nil {
			t.Fatalf("%s: %v", preset, err)
		}
		if cfg.ApprovalPreset != preset {
			t.Fatalf("approval_preset=%q, want %q", cfg.ApprovalPreset, preset)
		}
	}
}

func TestApprovalPresetRejectsAnUnknownValue(t *testing.T) {
	for _, raw := range []string{"read_only", "read-only", "auto", "full-access", "everything"} {
		path := filepath.Join(t.TempDir(), "forebrain.yaml")
		if err := os.WriteFile(path, []byte("approval_preset: "+raw+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadDocumentForTest(path); err == nil || !strings.Contains(err.Error(), "approval_preset must be workspace_write or full_access") {
			t.Fatalf("%s: err=%v", raw, err)
		}
	}
}

// An unset preset is not an error: it means the workspace-write default, which
// the state database's value fills in once plan 207 writes it.
func TestApprovalPresetUnsetIsValid(t *testing.T) {
	if err := ValidateAgentRoot(&Root{}); err != nil {
		t.Fatalf("unset approval_preset: %v", err)
	}
	if got := (Root{}).ApprovalPreset.Effective(); got != ApprovalPresetWorkspaceWrite {
		t.Fatalf("Effective()=%q, want %q", got, ApprovalPresetWorkspaceWrite)
	}
	if got := (Root{ApprovalPreset: ApprovalPresetFullAccess}).ApprovalPreset.Effective(); got != ApprovalPresetFullAccess {
		t.Fatalf("Effective()=%q, want %q", got, ApprovalPresetFullAccess)
	}
}

// approval_policy and sandbox_mode are no longer configuration keys: a file
// that still carries them loads, and the values it names change nothing,
// because the preset derives both.
func TestApprovalPolicyAndSandboxModeAreNotConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forebrain.yaml")
	body := "approval_policy: never\nsandbox_mode: danger-full-access\napprovals_reviewer: auto_review\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadDocumentForTest(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ApprovalPolicy.Mode != "" || cfg.SandboxMode != "" {
		t.Fatalf("legacy keys must not reach the runtime: policy=%q sandbox=%q", cfg.ApprovalPolicy.Mode, cfg.SandboxMode)
	}
}

func TestSandboxWorkspaceWriteDefaultsAndOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forebrain.yaml")
	if err := os.WriteFile(path, []byte("sandbox_workspace_write:\n  writable_roots: [./cache]\n  network_access: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadDocumentForTest(path)
	if err != nil {
		t.Fatal(err)
	}
	workspace := cfg.SandboxWorkspaceWrite
	if len(workspace.WritableRoots) != 1 || workspace.WritableRoots[0] != "./cache" || !workspace.EffectiveNetworkAccess() {
		t.Fatalf("sandbox_workspace_write=%+v", workspace)
	}
}
