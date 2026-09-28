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
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	// approval_policy and sandbox_mode both stay unset so the launch project's
	// trust decision resolves them.
	if cfg.ApprovalPolicy.Mode != "" || cfg.ApprovalsReviewer != "user" || cfg.SandboxMode != "" {
		t.Fatalf("defaults=%+v", cfg)
	}
	ef := cfg.EffectiveFeatures()
	if ef.ExecPermissionApprovals || ef.RequestPermissionsTool || cfg.Features.NetworkProxy.EnabledValue() {
		t.Fatalf("feature defaults=%+v", cfg.Features)
	}
	if cfg.Windows.Sandbox != "" || !cfg.Windows.UseSandboxPrivateDesktop() {
		t.Fatalf("windows defaults=%+v", cfg.Windows)
	}
}

func TestWindowsSandboxSettings(t *testing.T) {
	off := false
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("windows:\n  sandbox: unelevated\n  sandbox_private_desktop: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Windows.Sandbox != WindowsSandboxUnelevated || cfg.Windows.SandboxPrivateDesktop == nil || *cfg.Windows.SandboxPrivateDesktop != off || cfg.Windows.UseSandboxPrivateDesktop() {
		t.Fatalf("windows=%+v", cfg.Windows)
	}
}
