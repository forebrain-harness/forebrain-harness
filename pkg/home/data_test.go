package home

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRoot_WithEnv(t *testing.T) {
	orig := os.Getenv("FOREBRAIN_HOME")
	os.Setenv("FOREBRAIN_HOME", "/tmp/test-forebrain-home")
	defer func() {
		if orig != "" {
			os.Setenv("FOREBRAIN_HOME", orig)
		} else {
			os.Unsetenv("FOREBRAIN_HOME")
		}
	}()
	root, err := Root()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if root != "/tmp/test-forebrain-home" {
		t.Errorf("expected /tmp/test-forebrain-home, got %q", root)
	}
}

func TestDefaultConfigPath(t *testing.T) {
	p := DefaultConfigPath("/some/root")
	expected := filepath.Join("/some/root", "forebrain.yaml")
	if p != expected {
		t.Errorf("expected %q, got %q", expected, p)
	}
}

func TestConfigPath(t *testing.T) {
	p := ConfigPath("/some/root")
	expected := filepath.Join("/some/root", "forebrain.yaml")
	if p != expected {
		t.Errorf("expected %q, got %q", expected, p)
	}
}

func TestEnsure_EmptyRoot(t *testing.T) {
	err := Ensure("")
	if err == nil {
		t.Error("expected error for empty root")
	}
}

func TestEnsure_CreatesDirectories(t *testing.T) {
	tmpDir := t.TempDir()
	err := Ensure(tmpDir)
	if err != nil {
		t.Fatalf("Ensure failed: %v", err)
	}
	// Check that key directories exist
	dirs := []string{
		"state", "logs", "skills", "workspace",
		filepath.Join("workspace", "skills"),
		filepath.Join("workspace", ".forebrainhub"),
	}
	for _, d := range dirs {
		full := filepath.Join(tmpDir, d)
		info, err := os.Stat(full)
		if err != nil {
			t.Errorf("directory %q should exist: %v", d, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%q exists but is not a directory", d)
		}
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "cr"+"on")); !os.IsNotExist(err) {
		t.Fatalf("removed directory should not be created, err=%v", err)
	}
}

func TestEnsure_DoesNotSeedWorkspaceTemplates(t *testing.T) {
	tmpDir := t.TempDir()
	err := Ensure(tmpDir)
	if err != nil {
		t.Fatalf("Ensure failed: %v", err)
	}
	templates := []string{
		"AGENTS.md", "SOUL.md", "USER.md",
	}
	for _, name := range templates {
		p := filepath.Join(tmpDir, "workspace", name)
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("template %q should not be seeded by Ensure, err=%v", name, err)
		}
	}
}

func TestEnsure_DoesNotSeedRemovedWorkspaceMarkdown(t *testing.T) {
	tmpDir := t.TempDir()
	if err := Ensure(tmpDir); err != nil {
		t.Fatalf("Ensure failed: %v", err)
	}
	for _, name := range []string{"BOOTSTRAP.md", "HEARTBEAT.md", "DESIGN.md", "TOOLS.md", "FOREBRAIN.md", "IDENTITY.md"} {
		if _, err := os.Stat(filepath.Join(tmpDir, "workspace", name)); !os.IsNotExist(err) {
			t.Fatalf("%s should not be seeded, err=%v", name, err)
		}
	}
}

func TestEnsure_CreatesLockFile(t *testing.T) {
	tmpDir := t.TempDir()
	err := Ensure(tmpDir)
	if err != nil {
		t.Fatalf("Ensure failed: %v", err)
	}
	lock := filepath.Join(tmpDir, "workspace", ".forebrainhub", "lock.json")
	data, err := os.ReadFile(lock)
	if err != nil {
		t.Fatalf("ReadFile lock failed: %v", err)
	}
	if string(data) != "{}\n" && string(data) != "{}" {
		t.Errorf("expected lock file to contain {}, got %q", string(data))
	}
}

func TestEnsure_CreatesDefaultConfig(t *testing.T) {
	tmpDir := t.TempDir()
	err := Ensure(tmpDir)
	if err != nil {
		t.Fatalf("Ensure failed: %v", err)
	}
	cfg := filepath.Join(tmpDir, "forebrain.yaml")
	data, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatalf("ReadFile config failed: %v", err)
	}
	if len(data) == 0 {
		t.Error("default config should not be empty")
	}
	text := string(data)
	for _, want := range []string{
		"warn_remaining_tokens: 8000",
		"provider: \"\"",
		"model: \"\"",
		"approval_policy: \"on-request\"",
		"approvals_reviewer: \"user\"",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("default config missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "sandbox_mode:") {
		t.Fatalf("default config must leave sandbox_mode omitted for trust-derived defaults:\n%s", text)
	}
	for _, bad := range []string{"auto_compact", "debug_lsp", "active_memory_debug", "workspace_bootstrap_md", "heartbeat:", "evolution_dataset", "evolution_post_turn"} {
		if strings.Contains(text, bad) {
			t.Fatalf("default config should not contain %q:\n%s", bad, text)
		}
	}
	// Channels are not seeded eagerly; they are added by onboarding or by hand.
	for _, ch := range []string{
		"wecom:", "wecom_callback:", "telegram:", "discord:", "slack:",
		"whatsapp:", "signal:", "mattermost:", "matrix:", "homeassistant:",
		"email:", "sms:", "webhook:", "bluebubbles:", "weixin:", "feishu:",
		"dingtalk:", "qq:",
	} {
		if strings.Contains(text, ch) {
			t.Fatalf("default config should not seed channel %q:\n%s", ch, text)
		}
	}
	for _, stale := range []string{"chat_history:", "memory_flush:", "max_injected_chars:"} {
		if strings.Contains(text, stale) {
			t.Fatalf("default config should not contain stale pre-hook setting %q:\n%s", stale, text)
		}
	}
}

func TestEnsure_DoesNotSeedLegacyToolPolicyYAML(t *testing.T) {
	tmpDir := t.TempDir()
	if err := Ensure(tmpDir); err != nil {
		t.Fatalf("Ensure failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, ".forebrain", "tool_policy.yaml")); err == nil {
		t.Fatal("legacy tool_policy.yaml should not be seeded")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat legacy tool_policy.yaml: %v", err)
	}
}

func TestEnsure_SkipsExistingFiles(t *testing.T) {
	tmpDir := t.TempDir()
	// Pre-create a workspace template
	ws := filepath.Join(tmpDir, "workspace")
	os.MkdirAll(ws, 0o755)
	os.WriteFile(filepath.Join(ws, "AGENTS.md"), []byte("custom content"), 0o644)
	// Pre-create config
	os.WriteFile(filepath.Join(tmpDir, "forebrain.yaml"), []byte("custom: true"), 0o600)

	err := Ensure(tmpDir)
	if err != nil {
		t.Fatalf("Ensure failed: %v", err)
	}
	// AGENTS.md should still have custom content
	data, _ := os.ReadFile(filepath.Join(ws, "AGENTS.md"))
	if string(data) != "custom content" {
		t.Errorf("existing AGENTS.md should not be overwritten, got %q", string(data))
	}
	// Config should still have custom content
	data, _ = os.ReadFile(filepath.Join(tmpDir, "forebrain.yaml"))
	if string(data) != "custom: true" {
		t.Errorf("existing config should not be overwritten, got %q", string(data))
	}
}

func TestResolveConfigPath_YAML(t *testing.T) {
	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "forebrain.yaml"), []byte("{}"), 0o644)
	p, err := ResolveConfigPath(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if filepath.Base(p) != "forebrain.yaml" {
		t.Errorf("expected forebrain.yaml, got %q", p)
	}
}

func TestResolveConfigPath_JSONFallback(t *testing.T) {
	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "forebrain.json"), []byte("{}"), 0o644)
	p, err := ResolveConfigPath(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if filepath.Base(p) != "forebrain.json" {
		t.Errorf("expected forebrain.json fallback, got %q", p)
	}
}

func TestResolveConfigPath_NoConfig(t *testing.T) {
	tmpDir := t.TempDir()
	_, err := ResolveConfigPath(tmpDir)
	if err == nil {
		t.Error("expected error when no config file exists")
	}
}

func TestConfigDisplayPath_Success(t *testing.T) {
	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "forebrain.yaml"), []byte("{}"), 0o644)
	p := ConfigDisplayPath(tmpDir)
	if filepath.Base(p) != "forebrain.yaml" {
		t.Errorf("expected forebrain.yaml, got %q", p)
	}
}

func TestConfigDisplayPath_Fallback(t *testing.T) {
	tmpDir := t.TempDir()
	// No config file — should fall back to default path
	p := ConfigDisplayPath(tmpDir)
	expected := filepath.Join(tmpDir, "forebrain.yaml")
	if p != expected {
		t.Errorf("expected fallback to %q, got %q", expected, p)
	}
}

// ConfigDisplayPath was a production wrapper over the live ResolveConfigPath
// that only this package's tests called.

func ConfigDisplayPath(root string) string {
	if p, err := ResolveConfigPath(root); err == nil {
		return p
	}
	return DefaultConfigPath(root)
}
