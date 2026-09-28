package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSavePreservesLLMProviderSecretsAfterReorder guards the regression where
// PromoteMatchingLLMProvider reorders llm_providers, then Save's positional
// secret restore swaps api_key placeholders between entries.
func TestSavePreservesLLMProviderSecretsAfterReorder(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FOREBRAIN_HOME", dir)
	t.Setenv("OPENAI_API_KEY", "sk-openai-secret")
	t.Setenv("ANTHROPIC_API_KEY", "sk-anthropic-secret")

	path := filepath.Join(dir, "forebrain.yaml")

	// Seed config with two llm_providers: openai first, anthropic second.
	seed := `agents:
  definitions:
    main:
      llm_providers:
      - provider: openai
        model: gpt-4
        api_key: ${OPENAI_API_KEY}
        base_url: https://api.openai.com/v1
      - provider: anthropic
        model: claude-opus-5
        api_key: ${ANTHROPIC_API_KEY}
        base_url: https://api.anthropic.com
`
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	// LoadPersisted: api_key values are ${...} placeholders (no env merge).
	cfg, err := LoadPersisted(path)
	if err != nil {
		t.Fatalf("LoadPersisted: %v", err)
	}
	mainDef := cfg.Agents.Definitions["main"]
	if len(mainDef.LLMProviders) != 2 {
		t.Fatalf("expected 2 providers, got %d", len(mainDef.LLMProviders))
	}
	if mainDef.LLMProviders[0].APIKey != "${OPENAI_API_KEY}" {
		t.Fatalf("provider[0] api_key: got %q, want ${OPENAI_API_KEY}", mainDef.LLMProviders[0].APIKey)
	}
	if mainDef.LLMProviders[1].APIKey != "${ANTHROPIC_API_KEY}" {
		t.Fatalf("provider[1] api_key: got %q, want ${ANTHROPIC_API_KEY}", mainDef.LLMProviders[1].APIKey)
	}

	// Simulate /model switch: reorder providers (move anthropic to index 0).
	// This mimics PromoteMatchingLLMProvider.
	entry := mainDef.LLMProviders[1]
	copy(mainDef.LLMProviders[1:2], mainDef.LLMProviders[0:1])
	mainDef.LLMProviders[0] = entry
	cfg.Agents.Definitions["main"] = mainDef

	// After reorder:
	// [0] = anthropic / ${ANTHROPIC_API_KEY}
	// [1] = openai / ${OPENAI_API_KEY}
	if !strings.EqualFold(mainDef.LLMProviders[0].Provider, "anthropic") {
		t.Fatalf("after reorder, provider[0]: got %q, want anthropic", mainDef.LLMProviders[0].Provider)
	}
	if mainDef.LLMProviders[0].APIKey != "${ANTHROPIC_API_KEY}" {
		t.Fatalf("after reorder, provider[0] api_key: got %q, want ${ANTHROPIC_API_KEY}", mainDef.LLMProviders[0].APIKey)
	}

	// Save must preserve api_key placeholders in their reordered positions.
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Read back the saved YAML.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	saved := string(raw)

	// The file must still contain both placeholders.
	if !strings.Contains(saved, "${OPENAI_API_KEY}") {
		t.Fatalf("saved config missing ${OPENAI_API_KEY}:\n%s", saved)
	}
	if !strings.Contains(saved, "${ANTHROPIC_API_KEY}") {
		t.Fatalf("saved config missing ${ANTHROPIC_API_KEY}:\n%s", saved)
	}

	// Reload to verify the providers are correctly matched with their secrets.
	reloaded, err := LoadPersisted(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	reloadedMain := reloaded.Agents.Definitions["main"]
	if len(reloadedMain.LLMProviders) != 2 {
		t.Fatalf("reloaded: expected 2 providers, got %d", len(reloadedMain.LLMProviders))
	}

	// Provider[0] should be anthropic with ${ANTHROPIC_API_KEY}
	if !strings.EqualFold(reloadedMain.LLMProviders[0].Provider, "anthropic") {
		t.Errorf("reloaded provider[0]: got %q, want anthropic", reloadedMain.LLMProviders[0].Provider)
	}
	if reloadedMain.LLMProviders[0].APIKey != "${ANTHROPIC_API_KEY}" {
		t.Errorf("reloaded provider[0] api_key: got %q, want ${ANTHROPIC_API_KEY}", reloadedMain.LLMProviders[0].APIKey)
	}

	// Provider[1] should be openai with ${OPENAI_API_KEY}
	if !strings.EqualFold(reloadedMain.LLMProviders[1].Provider, "openai") {
		t.Errorf("reloaded provider[1]: got %q, want openai", reloadedMain.LLMProviders[1].Provider)
	}
	if reloadedMain.LLMProviders[1].APIKey != "${OPENAI_API_KEY}" {
		t.Errorf("reloaded provider[1] api_key: got %q, want ${OPENAI_API_KEY}", reloadedMain.LLMProviders[1].APIKey)
	}
}
