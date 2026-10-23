package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestSavePreservesLLMProviderSecretsAfterReorder guards the regression where
// a list reorder (any writer moving llm_providers entries) is followed by
// Save's positional secret restore, which used to swap api_key placeholders
// between entries.
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

	// Simulate a list reorder (move anthropic to index 0) to exercise the
	// positional secret restore.
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

// TestSaveDoesNotLendAnAPIKeyToANewProviderEntry reproduces the /connect
// failure this regression comes from: a login-backed provider is saved without
// an api_key, is promoted to the primary slot, and the positional secret
// restore hands it the key of the entry it displaced. OpenCode then sends that
// foreign key instead of the login (HTTP 401); ChatGPT persists it as a dead,
// misleading credential. Both flows write the same shape, so one case table
// covers them.
func TestSaveDoesNotLendAnAPIKeyToANewProviderEntry(t *testing.T) {
	for _, tc := range []struct {
		provider string
		model    string
		baseURL  string
	}{
		{provider: "opencode-go", model: "minimax-m3.1-flash-free", baseURL: "https://opencode.ai/inference/go"},
		{provider: "chatgpt", model: "gpt-5.6-luna", baseURL: "https://chatgpt.com/backend-api/codex"},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("FOREBRAIN_HOME", dir)
			path := filepath.Join(dir, "forebrain.yaml")
			seed := `agents:
  definitions:
    main:
      llm_providers:
      - provider: deepseek
        model: deepseek-chat
        api_key: ${DEEPSEEK_API_KEY}
        base_url: https://api.deepseek.com
`
			if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
				t.Fatalf("write seed: %v", err)
			}
			cfg, err := LoadPersisted(path)
			if err != nil {
				t.Fatalf("LoadPersisted: %v", err)
			}

			// The /connect write: one entry per provider+model, no key for a
			// provider that authenticates from a login file, promoted to the
			// primary slot.
			def := cfg.Agents.Definitions["main"]
			def.LLMProviders = append([]AgentLLMProviderConfig{{
				Provider: tc.provider,
				Model:    tc.model,
				BaseURL:  tc.baseURL,
			}}, def.LLMProviders...)
			cfg.Agents.Definitions["main"] = def

			if err := Save(path, cfg); err != nil {
				t.Fatalf("Save: %v", err)
			}

			saved, err := LoadPersisted(path)
			if err != nil {
				t.Fatalf("reload: %v", err)
			}
			entries := saved.Agents.Definitions["main"].LLMProviders
			if len(entries) != 2 {
				t.Fatalf("llm_providers = %+v, want 2 entries", entries)
			}
			if !strings.EqualFold(entries[0].Provider, tc.provider) {
				t.Fatalf("provider[0] = %q, want %s", entries[0].Provider, tc.provider)
			}
			if entries[0].APIKey != "" {
				t.Fatalf("%s entry borrowed api_key %q from the entry it displaced; want none", tc.provider, entries[0].APIKey)
			}
			if !strings.EqualFold(entries[1].Provider, "deepseek") || entries[1].APIKey != "${DEEPSEEK_API_KEY}" {
				t.Fatalf("displaced entry = %+v, want deepseek keeping ${DEEPSEEK_API_KEY}", entries[1])
			}
		})
	}
}

// TestSaveDoesNotLendARemovedProvidersKeyToTheNextEntry covers the same leak
// through a deletion instead of an insertion: removing the head entry shifts
// the next provider into a slot that used to hold another provider's key.
func TestSaveDoesNotLendARemovedProvidersKeyToTheNextEntry(t *testing.T) {
	const seed = `agents:
  definitions:
    main:
      llm_providers:
      - provider: deepseek
        model: deepseek-chat
        api_key: ${DEEPSEEK_API_KEY}
        base_url: https://api.deepseek.com
      - provider: opencode-go
        model: minimax-m3.1-flash-free
        base_url: https://opencode.ai/inference/go
`

	// An entry that still identifies itself keeps its own row's secret — here
	// nothing, because a login-backed entry carries no key.
	t.Run("survivor keeps its own row's secret", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("FOREBRAIN_HOME", dir)
		path := filepath.Join(dir, "forebrain.yaml")
		if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
			t.Fatalf("write seed: %v", err)
		}
		cfg, err := LoadPersisted(path)
		if err != nil {
			t.Fatalf("LoadPersisted: %v", err)
		}
		def := cfg.Agents.Definitions["main"]
		def.LLMProviders = def.LLMProviders[1:]
		cfg.Agents.Definitions["main"] = def

		if err := Save(path, cfg); err != nil {
			t.Fatalf("Save: %v", err)
		}
		saved, err := LoadPersisted(path)
		if err != nil {
			t.Fatalf("reload: %v", err)
		}
		entry := saved.Agents.Definitions["main"].LLMProviders[0]
		if entry.APIKey != "" {
			t.Fatalf("surviving entry api_key = %q, want none", entry.APIKey)
		}
	})

	// An entry that no longer identifies itself cannot be paired with its own
	// stored row, so it must start without a key rather than take the removed
	// provider's: a secret never migrates across providers.
	t.Run("edited survivor does not borrow the removed key", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("FOREBRAIN_HOME", dir)
		path := filepath.Join(dir, "forebrain.yaml")
		if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
			t.Fatalf("write seed: %v", err)
		}
		cfg, err := LoadPersisted(path)
		if err != nil {
			t.Fatalf("LoadPersisted: %v", err)
		}
		def := cfg.Agents.Definitions["main"]
		def.LLMProviders = def.LLMProviders[1:]
		// A mirror pointing the same entry at a proxy, edited in the same
		// change as the deletion: its own stored row no longer matches.
		def.LLMProviders[0].BaseURL = "https://mirror.example.invalid/inference"
		cfg.Agents.Definitions["main"] = def

		if err := Save(path, cfg); err != nil {
			t.Fatalf("Save: %v", err)
		}
		saved, err := LoadPersisted(path)
		if err != nil {
			t.Fatalf("reload: %v", err)
		}
		entry := saved.Agents.Definitions["main"].LLMProviders[0]
		if entry.APIKey != "" {
			t.Fatalf("edited surviving entry api_key = %q, want none (never the removed provider's key)", entry.APIKey)
		}
	})
}

// TestSaveDropsAnotherProvidersKeyFromALoginEntry is the ownership rule: a
// login-backed entry carrying a reference another provider in the same list
// declares as its own loses it on any save. This is what heals the reported
// contamination without the user having to touch the entry, and it is why the
// web settings page — which never sees the key and writes the stored one back —
// can no longer keep a foreign credential alive. Both loaders are covered: the
// runtime one expands the reference in memory, the editor one keeps it literal.
func TestSaveDropsAnotherProvidersKeyFromALoginEntry(t *testing.T) {
	for _, tc := range []struct {
		provider string
		model    string
		baseURL  string
	}{
		{provider: "opencode-go", model: "minimax-m3.1-flash-free", baseURL: "https://opencode.ai/inference/go"},
		{provider: "chatgpt", model: "gpt-5.6-luna", baseURL: "https://chatgpt.com/backend-api/codex"},
	} {
		for _, load := range []struct {
			name string
			fn   func(string) (Root, error)
		}{
			{name: "persisted", fn: LoadPersisted},
			{name: "expanded", fn: Load},
		} {
			for _, envSet := range []bool{true, false} {
				name := tc.provider + "/" + load.name + "/env set"
				if !envSet {
					name = tc.provider + "/" + load.name + "/env unset"
				}
				t.Run(name, func(t *testing.T) {
					dir := t.TempDir()
					t.Setenv("FOREBRAIN_HOME", dir)
					if envSet {
						t.Setenv("DEEPSEEK_API_KEY", "sk-deepseek-scene")
					} else {
						t.Setenv("DEEPSEEK_API_KEY", "")
					}
					path := filepath.Join(dir, "forebrain.yaml")
					seed := "agents:\n  definitions:\n    main:\n      llm_providers:\n" +
						"      - provider: deepseek\n" +
						"        model: deepseek-chat\n" +
						"        api_key: ${DEEPSEEK_API_KEY}\n" +
						"        base_url: https://api.deepseek.com\n" +
						"      - provider: " + tc.provider + "\n" +
						"        model: " + tc.model + "\n" +
						"        api_key: ${DEEPSEEK_API_KEY}\n" +
						"        base_url: " + tc.baseURL + "\n"
					if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
						t.Fatalf("write seed: %v", err)
					}
					cfg, err := load.fn(path)
					if err != nil {
						t.Fatalf("%s: %v", load.name, err)
					}

					// An ordinary save: nothing about the entries changes.
					if err := Save(path, cfg); err != nil {
						t.Fatalf("Save: %v", err)
					}
					saved, err := LoadPersisted(path)
					if err != nil {
						t.Fatalf("reload: %v", err)
					}
					for _, entry := range saved.Agents.Definitions["main"].LLMProviders {
						if !strings.EqualFold(entry.Provider, tc.provider) {
							continue
						}
						if entry.APIKey != "" {
							t.Fatalf("%s kept api_key %q, the reference another provider declares as its own", tc.provider, entry.APIKey)
						}
					}
					raw := string(mustRead(t, path))
					if !strings.Contains(raw, "${DEEPSEEK_API_KEY}") {
						t.Fatalf("the deepseek entry lost its own key:\n%s", raw)
					}
					if strings.Contains(raw, "sk-deepseek-scene") {
						t.Fatalf("save wrote the expanded secret into the file:\n%s", raw)
					}
				})
			}
		}
	}
}

// TestSaveHonoursAClearedKeyWithNoOtherProviderToBlame covers the other half of
// the healing path: /connect clears the key in memory and marks the entry, and
// Save must not restore the disk value even when no other entry explains it.
// This is what lets a user re-run /connect and pick the same provider+model
// after deleting the provider whose key had been lent.
func TestSaveHonoursAClearedKeyWithNoOtherProviderToBlame(t *testing.T) {
	for _, tc := range []struct {
		provider string
		model    string
		baseURL  string
	}{
		{provider: "opencode-go", model: "minimax-m3.1-flash-free", baseURL: "https://opencode.ai/inference/go"},
		{provider: "chatgpt", model: "gpt-5.6-luna", baseURL: "https://chatgpt.com/backend-api/codex"},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("FOREBRAIN_HOME", dir)
			path := filepath.Join(dir, "forebrain.yaml")
			seed := "agents:\n  definitions:\n    main:\n      llm_providers:\n" +
				"      - provider: " + tc.provider + "\n" +
				"        model: " + tc.model + "\n" +
				"        api_key: ${OPENCODE_GO_API_KEY}\n" +
				"        base_url: " + tc.baseURL + "\n"
			if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
				t.Fatalf("write seed: %v", err)
			}
			cfg, err := LoadPersisted(path)
			if err != nil {
				t.Fatalf("LoadPersisted: %v", err)
			}
			// What pkg/process.applyAgentLLMFields leaves behind for a
			// login-configured write: no key, and the marker saying so.
			def := cfg.Agents.Definitions["main"]
			def.LLMProviders[0].APIKey = ""
			def.LLMProviders[0].APIKeyCleared = true
			cfg.Agents.Definitions["main"] = def

			if err := Save(path, cfg); err != nil {
				t.Fatalf("Save: %v", err)
			}
			saved, err := LoadPersisted(path)
			if err != nil {
				t.Fatalf("reload: %v", err)
			}
			if got := saved.Agents.Definitions["main"].LLMProviders[0].APIKey; got != "" {
				t.Fatalf("cleared entry came back with api_key %q", got)
			}
		})
	}
}

// TestSaveKeepsAnUnresolvedReference pins the line between "cleared on purpose"
// and "resolved to nothing": a ${ENV} reference whose variable is unset in the
// saving process is still the user's configuration, so it survives the save.
// Judging emptiness alone would delete it — the placeholder is what lets the
// variable be supplied later.
func TestSaveKeepsAnUnresolvedReference(t *testing.T) {
	for _, provider := range []string{"opencode-go", "chatgpt"} {
		t.Run(provider, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("FOREBRAIN_HOME", dir)
			t.Setenv("MY_CUSTOM_OPENCODE_KEY", "")
			path := filepath.Join(dir, "forebrain.yaml")
			seed := "agents:\n  definitions:\n    main:\n      llm_providers:\n" +
				"      - provider: " + provider + "\n" +
				"        model: some-model\n" +
				"        api_key: ${MY_CUSTOM_OPENCODE_KEY}\n" +
				"        base_url: https://example.invalid/v1\n"
			if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
				t.Fatalf("write seed: %v", err)
			}
			cfg, err := Load(path)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if err := Save(path, cfg); err != nil {
				t.Fatalf("Save: %v", err)
			}
			if raw := string(mustRead(t, path)); !strings.Contains(raw, "${MY_CUSTOM_OPENCODE_KEY}") {
				t.Fatalf("save dropped the unresolved reference:\n%s", raw)
			}
		})
	}
}

// TestSaveKeepsAKeyModeLoginEntry's own reference: a login-backed provider in
// api-key mode names its key exactly as the write path would, so the ownership
// rule leaves it alone.
func TestSaveKeepsAKeyModeLoginEntrysOwnReference(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FOREBRAIN_HOME", dir)
	path := filepath.Join(dir, "forebrain.yaml")
	seed := `agents:
  definitions:
    main:
      llm_providers:
      - provider: deepseek
        model: deepseek-chat
        api_key: ${DEEPSEEK_API_KEY}
        base_url: https://api.deepseek.com
      - provider: opencode-go
        model: glm-5.3
        api_key: ${OPENCODE_GO_API_KEY}
        base_url: https://opencode.ai/inference/go
`
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	cfg, err := LoadPersisted(path)
	if err != nil {
		t.Fatalf("LoadPersisted: %v", err)
	}
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if raw := string(mustRead(t, path)); !strings.Contains(raw, "${OPENCODE_GO_API_KEY}") {
		t.Fatalf("save dropped the entry's own key reference:\n%s", raw)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return raw
}

// TestExampleConfigLoads is a smoke test for the repository's example
// forebrain.yaml: if it stops loading cleanly, every fresh install inherits
// the break.
func TestExampleConfigLoads(t *testing.T) {
	// Isolate from the real home so only the file under test contributes.
	t.Setenv("FOREBRAIN_HOME", t.TempDir())
	if _, err := Load("../../forebrain.yaml"); err != nil {
		t.Fatalf("example forebrain.yaml failed to load: %v", err)
	}
}

// TestParseRootYAMLCronRetentionRange pins the one validation behind every
// write path: the YAML editor's verdict is the loader's verdict, so a
// retention outside 1..3650 is rejected before anything reaches disk.
func TestParseRootYAMLCronRetentionRange(t *testing.T) {
	for _, bad := range []string{"0", "3651", "-3"} {
		_, err := ParseRootYAML([]byte("cron:\n  retention_days: " + bad + "\n"))
		if err == nil || !strings.Contains(err.Error(), "cron.retention_days") {
			t.Fatalf("retention_days %s = %v, want a cron.retention_days error", bad, err)
		}
	}
	for _, ok := range []string{"1", "3650"} {
		r, err := ParseRootYAML([]byte("cron:\n  retention_days: " + ok + "\n"))
		if err != nil {
			t.Fatalf("retention_days %s rejected: %v", ok, err)
		}
		if r.Cron.RetentionDays == nil {
			t.Fatalf("retention_days %s parsed to nil", ok)
		}
	}
}

// TestForebrainYamlExampleStaysValid keeps the repo-root example loadable: it
// documents current settings, so it must pass the same validation as a real
// config, including the lsp and cron sections it demonstrates.
func TestForebrainYamlExampleStaysValid(t *testing.T) {
	data, err := os.ReadFile("../../forebrain.yaml")
	if err != nil {
		t.Skip("example not found")
	}
	r, err := ParseRootYAML(data)
	if err != nil {
		t.Fatalf("forebrain.yaml example rejected: %v", err)
	}
	if got := r.CronRetentionDays(); got != 30 {
		t.Fatalf("cron.retention_days example = %d, want 30", got)
	}
	if eff := r.EffectiveLSP(); eff.MaxServers != 6 || eff.IdleTimeout != 600*time.Second || eff.RequestTimeout != 30*time.Second {
		t.Fatalf("lsp example resolved unexpectedly: %+v", eff)
	}
	if len(r.LSP.Servers) != 2 || r.LSP.Servers["gopls"].Command != "gopls" || r.LSP.Servers["pyright-diagnostics"].Role != "diagnostics" {
		t.Fatalf("lsp.servers example wrong: %+v", r.LSP.Servers)
	}
	if r.AutoReview.Policy != "" || r.Credentials.ChatGPT != "" {
		t.Fatalf("auto_review/credentials example wrong: %+v %+v", r.AutoReview, r.Credentials)
	}
	if !r.Tools.CommandRewrite.UseRewrite() {
		t.Fatal("tools.command_rewrite example should resolve to enabled")
	}
	f := r.EffectiveFeatures()
	if !f.Memories && r.Features.Memories == nil || !f.SkillOffer || !f.LSP {
		t.Fatalf("features example resolved unexpectedly: %+v", f)
	}
	if srv := r.Agents.Defaults.MCPServers; len(srv) > 0 {
		if string(srv[0].DefaultToolsApprovalMode) != "auto" || srv[0].StartupTimeout != 30 || srv[0].Required {
			t.Fatalf("mcp demo_stdio example wrong: mode=%q startup=%v required=%v", srv[0].DefaultToolsApprovalMode, srv[0].StartupTimeout, srv[0].Required)
		}
	}
	if r.Agents.Defaults.EnableSubagent != nil && *r.Agents.Defaults.EnableSubagent {
		t.Fatal("agents.defaults.enable_subagent example should stay off")
	}
	if w := r.Compact.ModelAutoCompactTokenLimitScope; w != "total" || r.Compact.ModelAutoCompactTokenLimit != 0 {
		t.Fatalf("compact example wrong: %+v", r.Compact)
	}
}

func TestLoadRejectsInvalidTokenEstimate(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "unknown hub",
			body: "agents:\n  defaults:\n    token_estimate:\n      hubs: [\"gitlab\"]\n",
			want: "unknown hub",
		},
		{
			name: "tokenizer and path",
			body: "agents:\n  defaults:\n    token_estimate:\n      tokenizers:\n        - model: \"x\"\n          tokenizer: \"qwen-3.8\"\n          path: \"/tmp/tokenizer.json\"\n",
			want: "exactly one of",
		},
		{
			name: "neither tokenizer nor path",
			body: "agents:\n  defaults:\n    token_estimate:\n      tokenizers:\n        - model: \"x\"\n",
			want: "exactly one of",
		},
		{
			name: "missing model",
			body: "agents:\n  defaults:\n    token_estimate:\n      tokenizers:\n        - tokenizer: \"qwen-3.8\"\n",
			want: "model is required",
		},
		{
			name: "empty hub",
			body: "agents:\n  defaults:\n    token_estimate:\n      hubs: [\"\"]\n",
			want: "unknown hub",
		},
		{
			name: "relative path",
			body: "agents:\n  defaults:\n    token_estimate:\n      tokenizers:\n        - model: \"x\"\n          path: \"relative/tokenizer.json\"\n",
			want: "path must be absolute",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "forebrain.yaml")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if err == nil {
				t.Fatalf("Load accepted an invalid token_estimate")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestLoadAcceptsValidTokenEstimate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "forebrain.yaml")
	// An absolute path on every OS; single-quoted so a Windows path's
	// backslashes stay literal.
	tokenizerPath := filepath.Join(dir, "tokenizer.json")
	body := "agents:\n  defaults:\n    token_estimate:\n      hubs: [\"modelscope\"]\n      default_tokenizer: \"kimi-k3\"\n      tokenizers:\n        - model: \"my-\"\n          tokenizer: \"qwen-3.8\"\n        - model: \"other-\"\n          path: '" + tokenizerPath + "'\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := r.Agents.Defaults.TokenEstimate.DefaultTokenizer; got != "kimi-k3" {
		t.Fatalf("default_tokenizer = %q, want kimi-k3", got)
	}
	if got := len(r.Agents.Defaults.TokenEstimate.Tokenizers); got != 2 {
		t.Fatalf("tokenizers = %d entries, want 2", got)
	}
	if got := r.Agents.Defaults.TokenEstimate.Tokenizers[1].Path; got != tokenizerPath {
		t.Fatalf("path = %q, want %q", got, tokenizerPath)
	}
}

// download: false is the one way to turn downloading off, and it survives a
// Save; hubs: [] means the default hubs with downloading on, before and after a
// Save (which drops the empty list).
func TestTokenEstimateDownloadAndEmptyHubsSurviveSave(t *testing.T) {
	// Load then Save back to the same file, the way the config editor does.
	load := func(t *testing.T, body string) (TokenEstimateConfig, string) {
		t.Helper()
		dir := t.TempDir()
		// Isolate the data-dir home so Load does not merge the real
		// ~/.forebrain/.env (see TestSavePreservesSecretEnvRefs).
		t.Setenv("FOREBRAIN_HOME", dir)
		path := filepath.Join(dir, "forebrain.yaml")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		r, err := Load(path)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if err := Save(path, r); err != nil {
			t.Fatalf("Save: %v", err)
		}
		return r.Agents.Defaults.TokenEstimate, path
	}
	reload := func(t *testing.T, path string) TokenEstimateConfig {
		t.Helper()
		r, err := Load(path)
		if err != nil {
			t.Fatalf("Load after Save: %v", err)
		}
		return r.Agents.Defaults.TokenEstimate
	}

	t.Run("download false", func(t *testing.T) {
		first, saved := load(t, "agents:\n  defaults:\n    token_estimate:\n      download: false\n")
		if raw, err := os.ReadFile(saved); err != nil || !strings.Contains(string(raw), "download: false") {
			t.Fatalf("saved file lost download: false (err=%v):\n%s", err, raw)
		}
		for name, cfg := range map[string]TokenEstimateConfig{"loaded": first, "saved and reloaded": reload(t, saved)} {
			if cfg.Download == nil || *cfg.Download {
				t.Fatalf("%s: download = %v, want a non-nil false", name, cfg.Download)
			}
		}
	})
	t.Run("empty hubs", func(t *testing.T) {
		first, saved := load(t, "agents:\n  defaults:\n    token_estimate:\n      hubs: []\n")
		for name, cfg := range map[string]TokenEstimateConfig{"loaded": first, "saved and reloaded": reload(t, saved)} {
			if cfg.Download != nil && !*cfg.Download {
				t.Fatalf("%s: download turned off by hubs: []", name)
			}
			if len(cfg.Hubs) != 0 {
				t.Fatalf("%s: hubs = %v, want empty (the default order)", name, cfg.Hubs)
			}
		}
	})
}

// TestSaveLeavesNoTempFileAndKeepsMode pins Save's on-disk behaviour now that
// it writes via writeFileAtomic: saving twice leaves exactly the target file
// in the directory (no temp-file residue) with 0600 permissions, for both the
// YAML and the JSON path.
func TestSaveLeavesNoTempFileAndKeepsMode(t *testing.T) {
	for _, name := range []string{"forebrain.yaml", "forebrain.json"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("FOREBRAIN_HOME", dir)
			path := filepath.Join(dir, name)
			if err := Save(path, Root{}); err != nil {
				t.Fatalf("first Save: %v", err)
			}
			if err := Save(path, Root{}); err != nil {
				t.Fatalf("second Save: %v", err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatalf("ReadDir: %v", err)
			}
			if len(entries) != 1 || entries[0].Name() != name {
				t.Fatalf("directory must hold only %s, got %v", name, entries)
			}
			if runtime.GOOS != "windows" {
				info, err := os.Stat(path)
				if err != nil {
					t.Fatalf("Stat: %v", err)
				}
				if info.Mode().Perm() != 0o600 {
					t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
				}
			}
		})
	}
}

// TestDanglingDefaultModelRejectedBothWays pins that a default_model naming
// no llm_providers entry is refused by load AND by Save — no write path can
// persist a yaml the process would refuse to read back.
func TestDanglingDefaultModelRejectedBothWays(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FOREBRAIN_HOME", dir)
	t.Setenv("OPENAI_API_KEY", "sk-test")
	path := filepath.Join(dir, "forebrain.yaml")
	seed := `agents:
  definitions:
    main:
      default_model: openai/gone
      llm_providers:
      - provider: openai
        model: gpt-4
        api_key: ${OPENAI_API_KEY}
        base_url: https://api.openai.com/v1
`
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	if _, err := LoadPersisted(path); err == nil {
		t.Fatal("load accepted a dangling default_model")
	} else if !strings.Contains(err.Error(), "agents.definitions.main.default_model") {
		t.Fatalf("load error does not name the field path: %v", err)
	}

	cfg := Root{Agents: AgentsSection{Definitions: map[string]AgentDefinition{
		"main": {
			Primary: true,
			LLMProviders: []AgentLLMProviderConfig{
				{Provider: "openai", Model: "gpt-4", APIKey: "${OPENAI_API_KEY}", BaseURL: "https://api.openai.com/v1"},
			},
			DefaultModel: "openai/gone",
		},
	}}}
	if err := Save(path, cfg); err == nil {
		t.Fatal("Save accepted a dangling default_model")
	} else if !strings.Contains(err.Error(), "agents.definitions.main.default_model") {
		t.Fatalf("Save error does not name the field path: %v", err)
	}

	// A default_model that does resolve saves cleanly, and a first-entry
	// default (absent field) still loads.
	main := cfg.Agents.Definitions["main"]
	main.DefaultModel = "openai/gpt-4"
	cfg.Agents.Definitions["main"] = main
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save with resolvable default_model: %v", err)
	}
	if _, err := LoadPersisted(path); err != nil {
		t.Fatalf("load after save: %v", err)
	}
}
