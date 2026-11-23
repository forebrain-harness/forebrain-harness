package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPermissionAndSandboxDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadDocumentForTest(path)
	if err != nil {
		t.Fatal(err)
	}
	// approval_preset stays unset so the state database's value supplies it, and
	// the derived approval policy and sandbox mode stay empty until
	// safety.EffectiveConfig runs.
	if cfg.ApprovalPreset != "" || cfg.ApprovalPolicy.Mode != "" || cfg.SandboxMode != "" {
		t.Fatalf("defaults=%+v", cfg)
	}
	if !cfg.EffectiveFeatures().Memories || !cfg.EffectiveFeatures().LSP {
		t.Fatalf("feature defaults=%+v", cfg.Features)
	}
}

// Loopback is the setting a command needs to reach a server the same sandbox
// started, and it is on unless the user turns it off: an absent key must not
// read as "no localhost" (plans/357).
func TestEffectiveNetworkLoopbackDefaultsOn(t *testing.T) {
	var unset SandboxWorkspaceWrite
	if !unset.EffectiveNetworkLoopback() {
		t.Fatal("an unset network_loopback must default to on")
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("sandbox_workspace_write:\n  network_access: false\n  network_loopback: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadDocumentForTest(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SandboxWorkspaceWrite.EffectiveNetworkLoopback() || cfg.SandboxWorkspaceWrite.EffectiveNetworkAccess() {
		t.Fatalf("sandbox_workspace_write=%+v, want both off", cfg.SandboxWorkspaceWrite)
	}
}
