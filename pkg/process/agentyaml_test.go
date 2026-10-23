package process

import (
	"os"
	"testing"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
)

// /connect writes through ApplyAgentLLMToConfigForAgent, and its add-or-replace
// identity is the provider+model pair: a second model under the same provider
// must become its own llm_providers entry, not overwrite the model the
// provider's existing entry carries.

func applyConnectForTest(t *testing.T, cfg *appcfg.Root, provider, model, apiKey, baseURL string) {
	t.Helper()
	if err := ApplyAgentLLMToConfigForAgent(cfg, "", provider, model, apiKey, baseURL, "", false, nil); err != nil {
		t.Fatalf("ApplyAgentLLMToConfigForAgent(%s/%s): %v", provider, model, err)
	}
}

func TestApplyAgentLLMSecondModelSameProviderKeepsExistingEntry(t *testing.T) {
	cfg := appcfg.Root{}
	applyConnectForTest(t, &cfg, "zhipuai", "glm-5.3", "${ZHIPUAI_API_KEY}", "https://open.bigmodel.cn/api/paas/v4")
	applyConnectForTest(t, &cfg, "zhipuai", "glm-5.3-flash", "${ZHIPUAI_API_KEY}", "https://open.bigmodel.cn/api/paas/v4")

	def, ok := cfg.Agents.Definitions["main"]
	if !ok {
		t.Fatal("main definition missing")
	}
	if len(def.LLMProviders) != 2 {
		t.Fatalf("llm_providers entries = %d, want 2: %+v", len(def.LLMProviders), def.LLMProviders)
	}
	// The just-configured entry is appended at the end; the existing entry
	// keeps both its position and its settings.
	appended := def.LLMProviders[len(def.LLMProviders)-1]
	if appended.Model != "glm-5.3-flash" {
		t.Fatalf("appended model = %q, want glm-5.3-flash", appended.Model)
	}
	kept := def.LLMProviders[0]
	if kept.Provider != "zhipuai" || kept.Model != "glm-5.3" {
		t.Fatalf("kept entry = %s/%s, want zhipuai/glm-5.3", kept.Provider, kept.Model)
	}
	if kept.APIKey != "${ZHIPUAI_API_KEY}" || kept.BaseURL != "https://open.bigmodel.cn/api/paas/v4" {
		t.Fatalf("kept entry lost its connection settings: %+v", kept)
	}
}

// TestApplyAgentLLMClearsAKeyTheLoginReplaces pins what /connect writes after a
// login for a provider that authenticates from a file: the entry carries no
// key, and a key it already had — the one belonging to whatever provider used
// this slot before — must not survive. For OpenCode a configured api_key
// outranks the login in the client's resolution order, so keeping it means the
// fresh login is never sent (the 401 this fix comes from); ChatGPT's client
// ignores a key, which leaves only a misleading foreign credential.
func TestApplyAgentLLMClearsAKeyTheLoginReplaces(t *testing.T) {
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
			path := dir + "/forebrain.yaml"
			seed := "agents:\n  definitions:\n    main:\n      llm_providers:\n" +
				"      - provider: " + tc.provider + "\n" +
				"        model: " + tc.model + "\n" +
				"        api_key: ${DEEPSEEK_API_KEY}\n" +
				"        base_url: " + tc.baseURL + "\n"
			if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
				t.Fatalf("write seed: %v", err)
			}
			cfg, err := appcfg.LoadPersisted(path)
			if err != nil {
				t.Fatalf("LoadPersisted: %v", err)
			}

			// The /connect write after a device login: no key supplied.
			applyConnectForTest(t, &cfg, tc.provider, tc.model, "", tc.baseURL)

			entry := cfg.Agents.Definitions["main"].LLMProviders[0]
			if entry.APIKey != "" {
				t.Fatalf("%s kept api_key %q after a login-configured write; want none", tc.provider, entry.APIKey)
			}
			if !entry.APIKeyCleared {
				t.Fatalf("%s: the clear is not marked, so Save would restore the disk value", tc.provider)
			}
			// And the save this write is followed by must persist the clear,
			// not the key the entry still has on disk.
			if err := appcfg.Save(path, cfg); err != nil {
				t.Fatalf("Save: %v", err)
			}
			saved, err := appcfg.LoadPersisted(path)
			if err != nil {
				t.Fatalf("reload: %v", err)
			}
			if got := saved.Agents.Definitions["main"].LLMProviders[0].APIKey; got != "" {
				t.Fatalf("%s came back from the save with api_key %q", tc.provider, got)
			}
		})
	}

	// API-key mode stays supported: a key the user supplied is the credential.
	t.Run("a supplied key is kept", func(t *testing.T) {
		cfg := appcfg.Root{}
		applyConnectForTest(t, &cfg, "opencode-go", "minimax-m3.1-flash-free", "${OPENCODE_API_KEY}", "https://opencode.ai/inference/go")
		entry := cfg.Agents.Definitions["main"].LLMProviders[0]
		if entry.APIKey != "${OPENCODE_API_KEY}" {
			t.Fatalf("api_key = %q, want ${OPENCODE_API_KEY}", entry.APIKey)
		}
	})

	// A provider that authenticates with a key is untouched by the rule.
	t.Run("a key-backed provider keeps its stored key", func(t *testing.T) {
		cfg := appcfg.Root{Agents: appcfg.AgentsSection{Definitions: map[string]appcfg.AgentDefinition{
			"main": {Primary: true, LLMProviders: []appcfg.AgentLLMProviderConfig{{
				Provider: "zhipuai",
				Model:    "glm-5.3",
				APIKey:   "${ZHIPUAI_API_KEY}",
				BaseURL:  "https://open.bigmodel.cn/api/paas/v4",
			}}},
		}}}
		applyConnectForTest(t, &cfg, "zhipuai", "glm-5.3", "", "https://open.bigmodel.cn/api/paas/v4")
		entry := cfg.Agents.Definitions["main"].LLMProviders[0]
		if entry.APIKey != "${ZHIPUAI_API_KEY}" {
			t.Fatalf("zhipuai api_key = %q, want the stored ${ZHIPUAI_API_KEY}", entry.APIKey)
		}
	})
}

func TestApplyAgentLLMSameProviderModelUpdatesInPlace(t *testing.T) {
	cfg := appcfg.Root{}
	applyConnectForTest(t, &cfg, "zhipuai", "glm-5.3", "old-key", "https://open.bigmodel.cn/api/paas/v4")
	applyConnectForTest(t, &cfg, "zhipuai", "glm-5.3", "new-key", "https://open.bigmodel.cn/api/paas/v4")

	def := cfg.Agents.Definitions["main"]
	if len(def.LLMProviders) != 1 {
		t.Fatalf("llm_providers entries = %d, want 1: %+v", len(def.LLMProviders), def.LLMProviders)
	}
	if def.LLMProviders[0].APIKey != "new-key" {
		t.Fatalf("api_key = %q, want refreshed new-key", def.LLMProviders[0].APIKey)
	}
}

func TestApplyAgentLLMSameProviderModelsPersistBothEntries(t *testing.T) {
	home := t.TempDir()
	cfgPath := home + "/forebrain.yaml"
	cfg := appcfg.Root{}
	applyConnectForTest(t, &cfg, "zhipuai", "glm-5.3", "${ZHIPUAI_API_KEY}", "https://open.bigmodel.cn/api/paas/v4")
	if err := appcfg.Save(cfgPath, cfg); err != nil {
		t.Fatalf("save first connect: %v", err)
	}
	persisted, err := appcfg.LoadPersisted(cfgPath)
	if err != nil {
		t.Fatalf("load persisted: %v", err)
	}
	applyConnectForTest(t, &persisted, "zhipuai", "glm-5.3-flash", "${ZHIPUAI_API_KEY}", "https://open.bigmodel.cn/api/paas/v4")
	if err := appcfg.Save(cfgPath, persisted); err != nil {
		t.Fatalf("save second connect: %v", err)
	}

	final, err := appcfg.LoadPersisted(cfgPath)
	if err != nil {
		t.Fatalf("reload persisted: %v", err)
	}
	models := map[string]bool{}
	for _, entry := range final.Agents.Definitions["main"].LLMProviders {
		models[entry.Model] = true
	}
	if !models["glm-5.3"] || !models["glm-5.3-flash"] {
		t.Fatalf("persisted llm_providers = %+v, want entries for both glm-5.3 and glm-5.3-flash", final.Agents.Definitions["main"].LLMProviders)
	}
}
