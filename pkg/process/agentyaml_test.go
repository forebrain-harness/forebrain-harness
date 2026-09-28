package process

import (
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
	// The just-configured entry is promoted to primary.
	primary := def.LLMProviders[0]
	if primary.Model != "glm-5.3-flash" {
		t.Fatalf("primary model = %q, want glm-5.3-flash", primary.Model)
	}
	kept := def.LLMProviders[1]
	if kept.Provider != "zhipuai" || kept.Model != "glm-5.3" {
		t.Fatalf("kept entry = %s/%s, want zhipuai/glm-5.3", kept.Provider, kept.Model)
	}
	if kept.APIKey != "${ZHIPUAI_API_KEY}" || kept.BaseURL != "https://open.bigmodel.cn/api/paas/v4" {
		t.Fatalf("kept entry lost its connection settings: %+v", kept)
	}
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
