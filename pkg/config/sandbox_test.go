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
