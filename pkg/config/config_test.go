package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	yamlv2 "go.yaml.in/yaml/v2"
)

type badYAMLValue struct{}

func (badYAMLValue) MarshalYAML() (interface{}, error) {
	return nil, errors.New("boom")
}

func TestEnvFileHelpers(t *testing.T) {
	t.Run("merge missing and CRLF env file", func(t *testing.T) {
		dir := t.TempDir()
		env := map[string]string{"KEEP": "old"}
		if err := mergeEnvFileInto(env, filepath.Join(dir, "missing.env")); err != nil {
			t.Fatalf("missing env file error = %v", err)
		}
		path := filepath.Join(dir, ".env")
		if err := os.WriteFile(path, []byte("A=1\r\nB=two\r\n"), 0o600); err != nil {
			t.Fatalf("write env: %v", err)
		}
		if err := mergeEnvFileInto(env, path); err != nil {
			t.Fatalf("merge env file: %v", err)
		}
		if env["KEEP"] != "old" || env["A"] != "1" || env["B"] != "two" {
			t.Fatalf("merged env = %#v", env)
		}
	})

	t.Run("invalid env file returns path error", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), ".env")
		if err := os.WriteFile(path, []byte("BAD-KEY=value"), 0o600); err != nil {
			t.Fatalf("write env: %v", err)
		}
		err := mergeEnvFileInto(map[string]string{}, path)
		if err == nil || !strings.Contains(err.Error(), path) {
			t.Fatalf("mergeEnvFileInto error = %v, want path context", err)
		}
	})

	t.Run("read error", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), ".env")
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("mkdir env path: %v", err)
		}
		if err := mergeEnvFileInto(map[string]string{}, path); err == nil {
			t.Fatal("mergeEnvFileInto directory read error = nil")
		}
	})

	t.Run("apply process env", func(t *testing.T) {
		t.Setenv("FOREBRAIN_TEST_ENV_APPLY", "")
		if err := applyMergedEnvToProcess(map[string]string{"FOREBRAIN_TEST_ENV_APPLY": "ok"}); err != nil {
			t.Fatalf("applyMergedEnvToProcess: %v", err)
		}
		if got := os.Getenv("FOREBRAIN_TEST_ENV_APPLY"); got != "ok" {
			t.Fatalf("process env = %q, want ok", got)
		}
		if err := applyMergedEnvToProcess(map[string]string{"BAD=KEY": "x"}); err == nil {
			t.Fatal("applyMergedEnvToProcess invalid key error = nil")
		}
	})

	t.Run("apply llm providers from map", func(t *testing.T) {
		chain := []AgentLLMProviderConfig{
			{Provider: "openai"},
			{Provider: "deepseek"},
		}
		got := applyLLMProvidersFromMap(chain, map[string]string{
			"OPENAI_API_KEY":    "openai-key",
			"OPENAI_BASE_URL":   "http://openai.test/v1",
			"DEEPSEEK_API_KEY":  "deepseek-key",
			"DEEPSEEK_BASE_URL": "http://deepseek.test/v1",
		})
		if got[0].APIKey != "openai-key" || got[0].BaseURL != "http://openai.test/v1" {
			t.Fatalf("openai provider env = %+v", got[0])
		}
		if got[1].APIKey != "deepseek-key" || got[1].BaseURL != "http://deepseek.test/v1" {
			t.Fatalf("deepseek provider env = %+v", got[1])
		}
		got = applyLLMProvidersFromMap(got, nil)
		if got[0].APIKey != "openai-key" {
			t.Fatalf("nil env map should preserve existing config, got %+v", got[0])
		}
	})

	t.Run("apply merged env returns process error", func(t *testing.T) {
		want := errors.New("boom")
		err := applyMergedEnvForLoad(&Root{}, map[string]string{"FEISHU_APP_ID": "app-id"}, func(map[string]string) error {
			return want
		})
		if !errors.Is(err, want) {
			t.Fatalf("applyMergedEnvForLoad error = %v, want %v", err, want)
		}
	})
}

func TestMergeDotEnvFromForebrainHomeBranches(t *testing.T) {
	t.Run("home lookup error", func(t *testing.T) {
		t.Setenv("FOREBRAIN_HOME", "")
		t.Setenv("HOME", "")
		if _, _, err := mergeDotEnvFromForebrainHome(); err == nil {
			t.Fatal("mergeDotEnvFromForebrainHome home lookup error = nil")
		}
	})

	t.Run("loads default env from forebrain home", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("FOREBRAIN_HOME", dir)
		path := filepath.Join(dir, ".env")
		if err := os.WriteFile(path, []byte("A=1\n"), 0o600); err != nil {
			t.Fatalf("write default env: %v", err)
		}
		got, gotPath, err := mergeDotEnvFromForebrainHome()
		if err != nil {
			t.Fatalf("mergeDotEnvFromForebrainHome: %v", err)
		}
		if gotPath != path {
			t.Fatalf("mergeDotEnvFromForebrainHome path = %q, want %q", gotPath, path)
		}
		if got["A"] != "1" {
			t.Fatalf("mergeDotEnvFromForebrainHome env = %#v", got)
		}
	})
}

// TestApplyLLMProvidersFromMapDoesNotClobberExplicitPlaceholder is the regression
// test for the reported TUI LLM auth bug. When a provider (e.g. "deepseek") has
// an explicit api_key: ${OPENAI_API_KEY} placeholder, and ~/.forebrain/.env contains
// BOTH OPENAI_API_KEY and DEEPSEEK_API_KEY, applyLLMProvidersFromMap must NOT
// overwrite the explicit placeholder with the provider-name-derived
// DEEPSEEK_API_KEY. Otherwise the wrong key is sent to the LLM endpoint at
// startup (causing an auth error), while the /model path (which uses
// LoadPersisted and skips this map) resolves the placeholder correctly - exactly
// the "startup fails, /model succeeds" symptom the user observed.
func TestApplyLLMProvidersFromMapDoesNotClobberExplicitPlaceholder(t *testing.T) {
	chain := []AgentLLMProviderConfig{
		{Provider: "deepseek", Model: "deepseek-v4-pro", APIKey: "${OPENAI_API_KEY}", BaseURL: "https://ark.cn-beijing.volces.com/api/coding/v3"},
	}
	got := applyLLMProvidersFromMap(chain, map[string]string{
		"OPENAI_API_KEY":   "sk-openai",
		"DEEPSEEK_API_KEY": "sk-deepseek",
	})
	if got[0].APIKey != "${OPENAI_API_KEY}" {
		t.Fatalf("explicit ${OPENAI_API_KEY} placeholder was clobbered to %q; must be preserved so expandRootEnvReferences resolves it", got[0].APIKey)
	}
	if got[0].BaseURL != "https://ark.cn-beijing.volces.com/api/coding/v3" {
		t.Fatalf("explicit base_url was clobbered to %q", got[0].BaseURL)
	}

	// Empty api_key still falls back to the provider-name-derived env var.
	empty := []AgentLLMProviderConfig{{Provider: "deepseek"}}
	gotEmpty := applyLLMProvidersFromMap(empty, map[string]string{"DEEPSEEK_API_KEY": "sk-deepseek"})
	if gotEmpty[0].APIKey != "sk-deepseek" {
		t.Fatalf("empty api_key should fall back to DEEPSEEK_API_KEY, got %q", gotEmpty[0].APIKey)
	}
}

func TestApplyMergedEnvOverridesCoversGatewaySearchAndLLM(t *testing.T) {
	root := &Root{
		Agents: AgentsSection{
			Definitions: map[string]AgentDefinition{
				"main": {
					LLMProviders: []AgentLLMProviderConfig{{Provider: "openai"}},
				},
			},
		},
	}
	env := map[string]string{
		"FOREBRAIN_GATEWAY_TOKEN":  "gw-token",
		"TAVILY_API_KEY":           "tavily",
		"BRAVE_SEARCH_API_KEY":     "brave",
		"BAIDU_WEB_SEARCH_API_KEY": "baidu",
		"OPENAI_API_KEY":           "openai",
		"OPENAI_BASE_URL":          "http://openai.test/v1",
		"DEEPSEEK_API_KEY":         "deepseek",
		"DEEPSEEK_BASE_URL":        "http://deepseek.test",
	}
	applyMergedEnvOverrides(root, env)

	if root.Gateway.Auth.Token != "gw-token" {
		t.Fatalf("gateway env overrides not applied: %+v", root.Gateway.Auth)
	}
	if root.Agents.Defaults.WebSearch.Tavily.APIKey != "tavily" ||
		root.Agents.Defaults.WebSearch.Brave.APIKey != "brave" ||
		root.Agents.Defaults.WebSearch.Baidu.APIKey != "baidu" {
		t.Fatalf("web search env overrides not applied: %+v", root.Agents.Defaults.WebSearch)
	}
	def := root.Agents.Definitions["main"]
	if def.LLMProviders[0].APIKey != "openai" {
		t.Fatalf("llm provider env overrides not applied: %+v", def)
	}
}

// A bare channel variable in the environment would configure "the" channel of
// no particular agent. Channels are per primary agent, so the environment must
// not be able to reach one: a secret gets there through an ${ENV_NAME}
// reference written under the agent that owns the channel.
func TestApplyMergedEnvOverridesLeavesChannelsAlone(t *testing.T) {
	root := &Root{
		Agents: AgentsSection{
			Definitions: map[string]AgentDefinition{
				"main": {Channels: ChannelsSection{Telegram: Telegram{Enabled: true}}},
			},
		},
	}
	applyMergedEnvOverrides(root, map[string]string{
		"TELEGRAM_BOT_TOKEN":          "tg-token",
		"FOREBRAIN_FEISHU_APP_SECRET": "feishu-secret",
		"WECOM_CORP_SECRET":           "wecom-secret",
	})

	ch := ChannelsForAgent(root, "main")
	if ch.Telegram.BotToken != "" || ch.Feishu.AppSecret != "" || ch.WeCom.CorpSecret != "" {
		t.Fatalf("environment reached an agent's channels: %+v", ch)
	}
}

func TestLoadAndSaveJSONYAML(t *testing.T) {
	dir := t.TempDir()

	jsonPath := filepath.Join(dir, "forebrain.json")
	root := Root{
		Gateway: Gateway{HTTPAddr: "127.0.0.1:6060"},
		Agents: AgentsSection{Definitions: map[string]AgentDefinition{
			"main": {LLMProviders: []AgentLLMProviderConfig{{Provider: "openai", Model: "gpt"}}},
		}},
	}
	if err := Save(jsonPath, root); err != nil {
		t.Fatalf("Save json: %v", err)
	}
	raw, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatalf("read saved json: %v", err)
	}
	if !json.Valid(raw) {
		t.Fatalf("saved json is invalid: %s", raw)
	}
	loaded, err := Load(jsonPath)
	if err != nil {
		t.Fatalf("Load json: %v", err)
	}
	if _, ok := loaded.Agents.Definitions["main"]; !ok {
		t.Fatalf("loaded json root = %+v", loaded)
	}

	yamlPath := filepath.Join(dir, "forebrain.yaml")
	if err := Save(yamlPath, Root{}); err != nil {
		t.Fatalf("Save yaml: %v", err)
	}
	if raw, err := os.ReadFile(yamlPath); err != nil || !strings.Contains(string(raw), "gateway") {
		t.Fatalf("saved yaml raw=%q err=%v", raw, err)
	}
	rawYAML, err := os.ReadFile(yamlPath)
	if err != nil {
		t.Fatalf("read saved yaml: %v", err)
	}
	text := string(rawYAML)
	for _, want := range []string{
		"warn_remaining_tokens: 25000",
		"block_remaining_tokens: 13000",
		"max_runes: 200000",
		"max_chunk_runes: 120000",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("saved yaml missing %q:\n%s", want, text)
		}
	}
	// Empty provider/model fields are omitted, not persisted as zero values.
	for _, bad := range []string{
		"provider: \"\"",
		"model: \"\"",
	} {
		if strings.Contains(text, bad) {
			t.Fatalf("saved yaml should omit empty field %q:\n%s", bad, text)
		}
	}
	if !strings.Contains(text, "\n      primary: false") {
		t.Fatalf("saved yaml should preserve the main primary false value:\n%s", text)
	}
	for _, bad := range []string{
		"debug:",
	} {
		if strings.Contains(text, bad) {
			t.Fatalf("saved yaml should not contain %q:\n%s", bad, text)
		}
	}

	unknownPath := filepath.Join(dir, "forebrain.conf")
	if err := os.WriteFile(unknownPath, []byte("gateway:\n  http_addr: 127.0.0.1:6060\n"), 0o600); err != nil {
		t.Fatalf("write unknown ext config: %v", err)
	}
	if _, err := Load(unknownPath); err != nil {
		t.Fatalf("Load unknown extension as yaml: %v", err)
	}

	badPath := filepath.Join(dir, "bad.conf")
	if err := os.WriteFile(badPath, []byte("{bad json and: [bad yaml"), 0o600); err != nil {
		t.Fatalf("write bad config: %v", err)
	}
	if _, err := Load(badPath); err == nil {
		t.Fatal("Load bad config error = nil")
	}
}

func TestSaveRejectsInvalidPrimaryAgentConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forebrain.yaml")
	root := Root{
		Agents: AgentsSection{
			Definitions: map[string]AgentDefinition{
				"Bad": {Primary: true},
			},
		},
	}
	if err := Save(path, root); err == nil {
		t.Fatal("Save invalid primary agent config error = nil")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("invalid config should not be written, stat err = %v", err)
	}
}

func TestAgentLLMHelpersAndYAMLUnmarshal(t *testing.T) {
	if PrimaryLLM(AgentDefinition{}) != nil {
		t.Fatal("PrimaryLLM empty = non-nil")
	}
	NormalizeLLMProviderEntries(nil)

	var cfg AgentLLMProviderConfig
	raw := []byte("provider: openai\nmodel: gpt\nparams:\n  temperature: 0.1\n")
	if err := yamlv2.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("yaml unmarshal provider: %v", err)
	}
	if cfg.Provider != "openai" || !strings.Contains(string(cfg.Params), "temperature") {
		t.Fatalf("provider yaml = %+v", cfg)
	}

	var bad AgentLLMProviderConfig
	if err := bad.UnmarshalYAML(func(interface{}) error { return errors.New("decode") }); err == nil {
		t.Fatal("UnmarshalYAML error = nil")
	}
	if err := json.Unmarshal([]byte(`{"params":{"top_p":0.5}}`), &cfg); err != nil {
		t.Fatalf("json unmarshal provider params: %v", err)
	}
	if !strings.Contains(string(cfg.Params), "top_p") || strings.Contains(string(cfg.Params), "temperature") {
		t.Fatalf("flat params should replace prior params, got %s", cfg.Params)
	}
	if err := json.Unmarshal([]byte(`{`), &cfg); err == nil {
		t.Fatal("provider invalid json error = nil")
	}
	if err := cfg.UnmarshalJSON([]byte(`{`)); err == nil {
		t.Fatal("direct provider invalid json error = nil")
	}
}

func TestLLMProviderModelArrayExpandsProviderEntries(t *testing.T) {
	raw := []byte(`
agents:
  definitions:
    main:
      llm_providers:
        - provider: openai
          model:
            - gpt-5.4
            - gpt-5.5
          api_key: ${OPENAI_API_KEY}
          base_url: https://api.openai.com/v1
`)
	path := filepath.Join(t.TempDir(), "forebrain.yaml")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadPersisted(path)
	if err != nil {
		t.Fatalf("LoadPersisted: %v", err)
	}
	got := cfg.Agents.Definitions["main"].LLMProviders
	if len(got) != 2 {
		t.Fatalf("llm providers len=%d want 2: %+v", len(got), got)
	}
	if got[0].Provider != "openai" || got[0].Model != "gpt-5.4" {
		t.Fatalf("first provider = %+v", got[0])
	}
	if got[1].Provider != "openai" || got[1].Model != "gpt-5.5" {
		t.Fatalf("second provider = %+v", got[1])
	}
	if got[0].APIKey != "${OPENAI_API_KEY}" || got[1].APIKey != "${OPENAI_API_KEY}" {
		t.Fatalf("expanded providers did not clone config: %+v", got)
	}
}

func TestLLMRequestParamsYAMLAndErrors(t *testing.T) {
	var params LLMRequestParams
	if err := yamlv2.Unmarshal([]byte("temperature: 0.2\nnested:\n  key: value\nitems:\n  - name: a\n"), &params); err != nil {
		t.Fatalf("yaml map unmarshal: %v", err)
	}
	if !json.Valid(params) || !strings.Contains(string(params), "nested") {
		t.Fatalf("params json = %s", params)
	}

	var fromString LLMRequestParams
	if err := yamlv2.Unmarshal([]byte("' {\"temperature\":0.3}'\n"), &fromString); err != nil {
		t.Fatalf("yaml string unmarshal: %v", err)
	}
	if string(fromString) != `{"temperature":0.3}` {
		t.Fatalf("yaml string params = %s", fromString)
	}

	tests := []struct {
		name string
		raw  string
	}{
		{name: "invalid json string", raw: "'not-json'\n"},
		{name: "json string array", raw: "'[1]'\n"},
		{name: "yaml array", raw: "- 1\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var p LLMRequestParams
			if err := yamlv2.Unmarshal([]byte(tt.raw), &p); err == nil {
				t.Fatal("yaml unmarshal error = nil")
			}
		})
	}

	var nilParams LLMRequestParams
	if err := yamlv2.Unmarshal([]byte("null\n"), &nilParams); err != nil {
		t.Fatalf("yaml null unmarshal: %v", err)
	}
	if nilParams != nil {
		t.Fatalf("nil params = %s", nilParams)
	}
	if b, err := LLMRequestParams(nil).MarshalJSON(); err != nil || string(b) != "null" {
		t.Fatalf("nil MarshalJSON = %q err=%v", b, err)
	}
	if y, err := (LLMRequestParams)([]byte(`{"nested":{"x":1},"items":[{"y":2}]}`)).MarshalYAML(); err != nil {
		t.Fatalf("MarshalYAML nested: %v", err)
	} else if m, ok := y.(map[string]interface{}); !ok || m["nested"] == nil || m["items"] == nil {
		t.Fatalf("MarshalYAML nested output = %#v", y)
	}
	if _, err := (LLMRequestParams)([]byte(`not-json`)).MarshalYAML(); err == nil {
		t.Fatal("MarshalYAML invalid json error = nil")
	}
	if _, err := ParseLLMRequestParamsJSON(``); err != nil {
		t.Fatalf("ParseLLMRequestParamsJSON empty: %v", err)
	}
	if _, err := ParseLLMRequestParamsJSON(`bad`); err == nil {
		t.Fatal("ParseLLMRequestParamsJSON bad error = nil")
	}
}

func TestPrimaryAgentsConfigDefaultsMainDefinition(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forebrain.yaml")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadPersisted(path)
	if err != nil {
		t.Fatalf("LoadPersisted: %v", err)
	}

	def, ok := cfg.Agents.Definitions["main"]
	if !ok {
		t.Fatal("main definition missing")
	}
	if def.Primary {
		t.Fatalf("main primary = %v, want false", def.Primary)
	}
	if !IsEffectivePrimaryAgent("main", def) {
		t.Fatal("main should be effective primary")
	}
}

func TestPrimaryAgentsConfigIgnoresPrimaryOnReservedDefinitions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forebrain.yaml")
	raw := []byte(`
agents:
  definitions:
    main:
      primary: false
    explore:
      primary: true
`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadPersisted(path)
	if err != nil {
		t.Fatalf("LoadPersisted: %v", err)
	}

	if cfg.Agents.Definitions["main"].Primary {
		t.Fatal("main primary field should remain false")
	}
	if !IsEffectivePrimaryAgent("main", cfg.Agents.Definitions["main"]) {
		t.Fatal("main should be effective primary")
	}
	if !cfg.Agents.Definitions["explore"].Primary {
		t.Fatal("reserved subagent stored primary field should be preserved")
	}
	if IsEffectivePrimaryAgent(" explore ", cfg.Agents.Definitions["explore"]) {
		t.Fatal("reserved subagent should not be effective primary")
	}
}

func TestPrimaryAgentsConfigParsesMultipleDefinitions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forebrain.yaml")
	raw := []byte(`
agents:
  definitions:
    main: {}
    helper:
      primary: true
    reviewer:
      primary: true
`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadPersisted(path)
	if err != nil {
		t.Fatalf("LoadPersisted: %v", err)
	}

	if _, ok := cfg.Agents.Definitions["main"]; !ok {
		t.Fatal("main definition missing")
	}
	if !cfg.Agents.Definitions["helper"].Primary {
		t.Fatal("helper should be primary")
	}
	if !cfg.Agents.Definitions["reviewer"].Primary {
		t.Fatal("reviewer should be primary")
	}
}

func TestPrimaryAgentsConfigRejectsInvalidAgentID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forebrain.yaml")
	raw := []byte(`
agents:
  definitions:
    Bad: {}
`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := LoadPersisted(path)
	if err == nil || !strings.Contains(err.Error(), "Bad") {
		t.Fatalf("LoadPersisted error = %v, want invalid agent id error", err)
	}
}

func TestProviderEnvLookups(t *testing.T) {
	env := map[string]string{
		"OPENAI_API_KEY":      " openai ",
		"ANTHROPIC_API_KEY":   "anthropic",
		"DEEPSEEK_API_KEY":    "deepseek",
		"GOOGLE_API_KEY":      "google",
		"ALIBABA_API_KEY":     "alibaba",
		"MINIMAX_API_KEY":     "minimax",
		"XAI_API_KEY":         "xai",
		"MISTRAL_API_KEY":     "mistral",
		"MOONSHOTAI_API_KEY":  "moonshotai",
		"ZHIPUAI_API_KEY":     "zhipuai",
		"OPENAI_BASE_URL":     "http://openai",
		"DEEPSEEK_BASE_URL":   "http://deepseek",
		"ALIBABA_BASE_URL":    "http://alibaba",
		"GOOGLE_BASE_URL":     "http://google",
		"XAI_BASE_URL":        "http://xai",
		"MISTRAL_BASE_URL":    "http://mistral",
		"MINIMAX_BASE_URL":    "http://minimax",
		"MOONSHOTAI_BASE_URL": "http://moonshotai",
		"ZHIPUAI_BASE_URL":    "http://zhipuai",
	}
	get := func(k string) string { return env[k] }
	keyCases := map[string]string{
		"openai":     "openai",
		"anthropic":  "anthropic",
		"deepseek":   "deepseek",
		"google":     "google",
		"alibaba":    "alibaba",
		"minimax":    "minimax",
		"xai":        "xai",
		"mistral":    "mistral",
		"moonshotai": "moonshotai",
		"zhipuai":    "zhipuai",
		"unknown":    "",
	}
	for provider, want := range keyCases {
		if got := ProviderAPIKeyLookup(get, provider); got != want {
			t.Fatalf("ProviderAPIKeyLookup(%q) = %q, want %q", provider, got, want)
		}
	}
	if got := ProviderAPIKeyLookup(func(string) string { return "" }, "unknown"); got != "" {
		t.Fatalf("empty unknown key = %q", got)
	}
	baseCases := map[string]string{
		"openai":     "http://openai",
		"deepseek":   "http://deepseek",
		"alibaba":    "http://alibaba",
		"google":     "http://google",
		"xai":        "http://xai",
		"mistral":    "http://mistral",
		"minimax":    "http://minimax",
		"moonshotai": "http://moonshotai",
		"zhipuai":    "http://zhipuai",
		"unknown":    "",
	}
	for provider, want := range baseCases {
		if got := ProviderBaseURLLookup(get, provider); got != want {
			t.Fatalf("ProviderBaseURLLookup(%q) = %q, want %q", provider, got, want)
		}
	}
}

func TestGatewayWebhookInboundPathsAndAnonymousGET(t *testing.T) {
	if got := gatewayWebhookInboundPaths(ChannelsSection{}); got != nil {
		t.Fatalf("nil gateway webhook paths = %#v", got)
	}
	channels := ChannelsSection{
		Webhook:       Webhook{Enabled: true, InboundPath: " /webhook "},
		BlueBubbles:   BlueBubbles{Enabled: true, InboundPath: "/blue"},
		SMS:           SMS{Enabled: true, InboundPath: "/sms"},
		Slack:         Slack{Enabled: true, InboundPath: "/slack"},
		Email:         Email{Enabled: true, InboundPath: "/email"},
		WhatsApp:      WhatsApp{Enabled: true, InboundPath: "/whatsapp"},
		WeCom:         WeCom{Enabled: true, CallbackPath: "/wecom"},
		WeComCallback: WeComCallback{Enabled: true, CallbackPath: "/wecom_callback"},
	}
	got := strings.Join(gatewayWebhookInboundPaths(channels), ",")
	want := "/webhook,/blue,/sms,/slack,/email,/whatsapp,/wecom,/wecom_callback"
	if got != want {
		t.Fatalf("gatewayWebhookInboundPaths = %q, want %q", got, want)
	}
	channels.Webhook.InboundPath = " "
	if strings.Contains(strings.Join(gatewayWebhookInboundPaths(channels), ","), "webhook") {
		t.Fatal("empty enabled webhook path should be skipped")
	}
	channels.Webhook.InboundPath = " /webhook "
	root := &Root{Agents: AgentsSection{Definitions: map[string]AgentDefinition{
		"main": {Channels: channels},
	}}}
	if !GatewayControlPlaneAuthExemptPath(" /webhook ", root, "main") || !GatewayControlPlaneAuthExemptPath("/channels/x/health", root, "main") {
		t.Fatal("expected gateway path auth exemption")
	}
	if GatewayControlPlaneAuthExemptPath("/api", root, "main") {
		t.Fatal("unexpected gateway auth exemption")
	}
	if GatewayAllowsAnonymousGET("") || GatewayAllowsAnonymousGET("/ws/chat") || GatewayAllowsAnonymousGET("/api/x") || GatewayAllowsAnonymousGET("/v1/x") {
		t.Fatal("anonymous GET should reject empty, ws chat, api and v1")
	}
	if !GatewayAllowsAnonymousGET("/docs") {
		t.Fatal("anonymous GET should allow non-control path")
	}
}

func TestCurrentApprovalAndSandboxDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forebrain.yaml")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	// Like sandbox_mode below, an omitted approval_policy stays unset so the
	// launch project's trust decision can resolve it. String() still reports the
	// display fallback.
	if cfg.ApprovalPolicy.Mode != "" || cfg.ApprovalsReviewer != "user" {
		t.Fatalf("approval defaults = %q/%q", cfg.ApprovalPolicy.Mode, cfg.ApprovalsReviewer)
	}
	if got := cfg.ApprovalPolicy.String(); got != string(ApprovalPolicyOnRequest) {
		t.Fatalf("approval display fallback = %q", got)
	}
	if cfg.SandboxMode != "" {
		t.Fatalf("sandbox mode default = %q", cfg.SandboxMode)
	}
}

func TestSavePreservesFalseBooleanConfig(t *testing.T) {
	for _, ext := range []string{"yaml", "json"} {
		t.Run(ext, func(t *testing.T) {
			t.Setenv("FOREBRAIN_HOME", t.TempDir())
			path := filepath.Join(t.TempDir(), "forebrain."+ext)
			if err := os.WriteFile(path, []byte(`
agents:
  definitions:
    main:
      channels:
        weixin:
          enabled: false
          silk_voice_decode: false
    worker:
      primary: false
hooks:
  PreToolUse:
    - matcher: "*"
      hooks:
        - type: command
          command: echo test
          once: false
          async: false
          async_rewake: false
approval_policy:
  granular:
    sandbox_approval: false
    rules: false
    skill_approval: false
    request_permissions: false
    mcp_elicitations: false
memories:
  disable_on_external_context: false
`), 0o600); err != nil {
				t.Fatal(err)
			}
			if ext == "json" {
				if err := os.WriteFile(path, []byte(`{"agents":{"definitions":{"main":{"channels":{"weixin":{"enabled":false,"silk_voice_decode":false}}},"worker":{"primary":false}}},"hooks":{"PreToolUse":[{"matcher":"*","hooks":[{"type":"command","command":"echo test","once":false,"async":false,"async_rewake":false}]}]},"approval_policy":{"granular":{"sandbox_approval":false,"rules":false,"skill_approval":false,"request_permissions":false,"mcp_elicitations":false}},"memories":{"disable_on_external_context":false}}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			cfg, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := Save(path, cfg); err != nil {
				t.Fatal(err)
			}
			got, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			gotWeixin := ChannelsForAgent(&got, "main").Weixin
			if gotWeixin.Enabled || gotWeixin.SilkVoiceDecode || got.Agents.Definitions["worker"].Primary || got.ApprovalPolicy.Granular.SandboxApproval || got.ApprovalPolicy.Granular.Rules || got.ApprovalPolicy.Granular.SkillApproval || got.ApprovalPolicy.Granular.RequestPermissions || got.ApprovalPolicy.Granular.MCPElicitations || got.Memories.DisableOnExternalContext == nil || *got.Memories.DisableOnExternalContext {
				t.Fatalf("explicit false values changed after Save: %+v", got)
			}
			hook := got.Hooks["PreToolUse"][0].Hooks[0]
			if hook.Once || hook.Async || hook.AsyncRewake {
				t.Fatalf("hook false values changed after Save: %+v", hook)
			}
		})
	}
}

func TestSecretScanBranches(t *testing.T) {
	if err := rejectPlaintextConfigSecrets("bad.yaml", []byte("x: [")); err != nil {
		t.Fatalf("invalid yaml should skip secret scan error, got %v", err)
	}
	if !isSecretLikeConfigKey("my-api-key") || isSecretLikeConfigKey("max_tokens") {
		t.Fatal("secret key classification failed")
	}
	if isAllowedConfigSecretValue(123) || !isAllowedConfigSecretValue(nil) || !isAllowedConfigSecretValue(" ${ENV_1} ") {
		t.Fatal("allowed secret value classification failed")
	}
	for _, s := range []string{"${}", "$ENV", "${BAD-NAME}", "${1BAD}"} {
		if isSingleEnvReference(s) {
			t.Fatalf("isSingleEnvReference(%q) = true", s)
		}
	}
}

func TestContextInjectDefaultBranches(t *testing.T) {
	cfg := ContextInjectConfig{}
	if cfg.MaxPreHookRulesCharsOr(10) != 10 {
		t.Fatal("context inject max default branches failed")
	}
}

func TestDotEnvAfterNormalizeBranches(t *testing.T) {
	rootDir := t.TempDir()
	t.Setenv("FOREBRAIN_HOME", rootDir)
	if err := os.WriteFile(filepath.Join(rootDir, ".env"), []byte("WEBHOOK_TOKEN=from-dotenv\n"), 0o600); err != nil {
		t.Fatalf("write dotenv: %v", err)
	}
	var root Root
	if err := applyDotEnvAfterNormalize("", &root); err != nil {
		t.Fatalf("applyDotEnvAfterNormalize: %v", err)
	}
	// .env is merged into the process environment, where ${WEBHOOK_TOKEN}
	// references in any agent's channels resolve from. It no longer writes
	// into the configuration itself.
	if os.Getenv("WEBHOOK_TOKEN") != "from-dotenv" {
		t.Fatalf("dotenv not applied to the process env: %q", os.Getenv("WEBHOOK_TOKEN"))
	}
	_ = root

	fileRoot := filepath.Join(t.TempDir(), "not-dir")
	if err := os.WriteFile(fileRoot, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file root: %v", err)
	}
	t.Setenv("FOREBRAIN_HOME", fileRoot)
	if err := applyDotEnvAfterNormalize("", &Root{}); err == nil {
		t.Fatal("applyDotEnvAfterNormalize with file root error = nil")
	}
	if err := applyMergedEnvForLoad(&Root{}, map[string]string{"WEBHOOK_TOKEN": "x"}, nil); err != nil {
		t.Fatalf("nil apply process should not error: %v", err)
	}
	applyMergedEnvOverrides(nil, map[string]string{"WEBHOOK_TOKEN": "x"})
	applyMergedEnvOverrides(&Root{}, nil)
}

func TestLoadNormalizeAndErrorBranches(t *testing.T) {
	dir := t.TempDir()
	if _, err := Load(filepath.Join(dir, "missing.yaml")); err == nil {
		t.Fatal("Load missing file error = nil")
	}
	badYAML := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(badYAML, []byte("x: ["), 0o600); err != nil {
		t.Fatalf("write bad yaml: %v", err)
	}
	if _, err := Load(badYAML); err == nil {
		t.Fatal("Load bad yaml error = nil")
	}
	badJSON := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(badJSON, []byte("{"), 0o600); err != nil {
		t.Fatalf("write bad json: %v", err)
	}
	if _, err := Load(badJSON); err == nil {
		t.Fatal("Load bad json error = nil")
	}

	path := filepath.Join(dir, "normalize.yaml")
	body := strings.Join([]string{
		"gateway:",
		"  http_addr: 127.0.0.1:8081",
		"agents:",
		"  definitions:",
		"    main:",
		"      llm_providers: []",
		"    worker:",
		"      llm_providers:",
		"        - provider: openai",
		"          model: gpt",
	}, "\n")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write normalize config: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load normalize config: %v", err)
	}
	if cfg.Gateway.HTTPAddr != "127.0.0.1:8081" || len(cfg.Agents.Definitions["main"].LLMProviders) != 1 || len(cfg.Agents.Definitions["worker"].LLMProviders) != 1 {
		t.Fatalf("normalize branches not applied: %+v", cfg)
	}

	unknownConfigPath := filepath.Join(dir, "unknown-top-level.yaml")
	unknownConfigBody := strings.Join([]string{
		"unknown_gateway_legacy:",
		"  http_addr: \"127.0.0.1:9099\"",
	}, "\n")
	if err := os.WriteFile(unknownConfigPath, []byte(unknownConfigBody), 0o600); err != nil {
		t.Fatalf("write unknown top-level config: %v", err)
	}
	unknownConfig, err := Load(unknownConfigPath)
	if err != nil {
		t.Fatalf("Load unknown top-level config: %v", err)
	}
	// gateway.http_addr falls back to its default when unset; the check is
	// that the unknown key did not reach it.
	if unknownConfig.Gateway.HTTPAddr != "127.0.0.1:6060" {
		t.Fatalf("unknown top-level config should be ignored, got gateway http_addr %q", unknownConfig.Gateway.HTTPAddr)
	}

	if err := Save(dir, Root{}); err == nil {
		t.Fatal("Save to directory error = nil")
	}
	if err := Save(dir, Root{}); err == nil {
		t.Fatal("Save yaml to directory error = nil")
	}

	envRoot := t.TempDir()
	t.Setenv("FOREBRAIN_HOME", envRoot)
	if err := os.WriteFile(filepath.Join(envRoot, ".env"), []byte("BAD-KEY=value"), 0o600); err != nil {
		t.Fatalf("write bad dotenv: %v", err)
	}
	loadPath := filepath.Join(dir, "with-bad-env.yaml")
	if err := os.WriteFile(loadPath, []byte("gateway:\n  http_addr: 127.0.0.1:6060\n"), 0o600); err != nil {
		t.Fatalf("write load config: %v", err)
	}
	if _, err := Load(loadPath); err == nil {
		t.Fatal("Load with bad dotenv error = nil")
	}
}

func TestSecretScanArrayBranch(t *testing.T) {
	err := scanPlaintextConfigSecrets("cfg.yaml", nil, []interface{}{
		map[interface{}]interface{}{"token": "plain"},
	})
	if err == nil || !strings.Contains(err.Error(), "[0].token") {
		t.Fatalf("array secret scan error = %v", err)
	}
}

// TestSavePreservesSecretEnvRefs guards the onboarding regression where Load
// expands ${ENV} secret refs to plaintext, and a subsequent Save wrote that
// plaintext back — so the next Load rejected the file with
// "plaintext secret is not allowed at gateway.auth.token".
func TestSavePreservesSecretEnvRefs(t *testing.T) {
	dir := t.TempDir()
	// Isolate the data-dir home so Load does not merge the real ~/.forebrain/.env,
	// which would shadow our process env with the on-disk token.
	t.Setenv("FOREBRAIN_HOME", dir)
	t.Setenv("FOREBRAIN_GATEWAY_TOKEN", "deadbeefsecret")

	path := filepath.Join(dir, "forebrain.yaml")

	// Seed an on-disk config that stores the secret as an env reference.
	seed := "" +
		"gateway:\n" +
		"  http_addr: 127.0.0.1:6060\n" +
		"  auth:\n" +
		"    mode: token\n" +
		"    token: ${FOREBRAIN_GATEWAY_TOKEN}\n"
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatalf("write seed config: %v", err)
	}

	// Load expands the env ref to plaintext in memory.
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Gateway.Auth.Token != "deadbeefsecret" {
		t.Fatalf("env ref not expanded on load: %q", loaded.Gateway.Auth.Token)
	}

	// Save must write the ${ENV} reference back, not the expanded plaintext.
	if err := Save(path, loaded); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	if !strings.Contains(string(raw), "${FOREBRAIN_GATEWAY_TOKEN}") {
		t.Fatalf("saved config dropped env ref:\n%s", raw)
	}
	if strings.Contains(string(raw), "deadbeefsecret") {
		t.Fatalf("saved config leaked plaintext secret:\n%s", raw)
	}

	// The saved file must load again without the plaintext-secret rejection.
	if _, err := Load(path); err != nil {
		t.Fatalf("reload after save: %v", err)
	}
}

func TestWebSearchConfig_AllowsProviderAndNestedConfig(t *testing.T) {
	cfg := WebSearchConfig{
		Provider: "tavily",
		Tavily:   &WebSearchTavily{APIKey: "k", SearchDepth: "advanced"},
	}
	if cfg.Provider != "tavily" {
		t.Fatalf("Provider = %q, want tavily", cfg.Provider)
	}
	if cfg.Tavily == nil || cfg.Tavily.SearchDepth != "advanced" {
		t.Fatalf("Tavily config = %+v, want advanced depth", cfg.Tavily)
	}
}

func TestContextInjectConfig_MaxOrHelpers(t *testing.T) {
	cfg := ContextInjectConfig{
		MaxPreHookRulesChars: 120,
	}
	if got := cfg.MaxPreHookRulesCharsOr(10); got != 120 {
		t.Fatalf("MaxPreHookRulesCharsOr = %d, want 120", got)
	}
}

func TestLLMRequestParams_JSONRoundTrip(t *testing.T) {
	params, err := ParseLLMRequestParamsJSON(`{"temperature":0.2}`)
	if err != nil {
		t.Fatalf("ParseLLMRequestParamsJSON error = %v", err)
	}
	if got := string(params.Bytes()); got != `{"temperature":0.2}` {
		t.Fatalf("Bytes() = %q, want original JSON", got)
	}
	b, err := params.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON error = %v", err)
	}
	if string(b) != `{"temperature":0.2}` {
		t.Fatalf("MarshalJSON() = %q, want original JSON", string(b))
	}
}

func TestLLMRequestParams_RejectsNonObjectJSON(t *testing.T) {
	var params LLMRequestParams
	err := json.Unmarshal([]byte(`["not","an","object"]`), &params)
	if err == nil {
		t.Fatal("UnmarshalJSON error = nil, want non-object rejection")
	}
}

func TestRedactConfigText_RedactsKnownSecrets(t *testing.T) {
	input := strings.Join([]string{
		"token: abc123",
		"client_secret: topsecret",
		"normal: keep",
	}, "\n")
	got := RedactConfigText(input)
	if strings.Contains(got, "abc123") || strings.Contains(got, "topsecret") {
		t.Fatalf("RedactConfigText leaked secret data: %q", got)
	}
	if !strings.Contains(got, "normal: keep") {
		t.Fatalf("RedactConfigText removed non-secret field: %q", got)
	}
}

func TestExecutionMaxParallelSubagentsDefaultUsesLogicalCoresMinusOne(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "forebrain.yaml")
	if err := os.WriteFile(path, []byte("agents:\n  defaults: {}\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := runtime.NumCPU() - 1
	if want < 0 {
		want = 0
	}
	if got := loaded.Agents.Defaults.Execution.MaxParallelSubagentsValue(); got != want {
		t.Fatalf("MaxParallelSubagentsValue = %d, want %d", got, want)
	}
}

func TestExecutionMaxParallelSubagentsExplicitZeroMeansMainAgentOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "forebrain.yaml")
	if err := os.WriteFile(path, []byte("agents:\n  defaults:\n    execution:\n      max_parallel_subagents: 0\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := loaded.Agents.Defaults.Execution.MaxParallelSubagentsValue(); got != 0 {
		t.Fatalf("MaxParallelSubagentsValue = %d, want 0", got)
	}
}

func TestExecutionMaxParallelSubagentsNegativeClampsToZero(t *testing.T) {
	root := Root{Agents: AgentsSection{Defaults: AgentDefaults{
		Execution: ExecutionConfig{MaxParallelSubagents: intPtr(-7)},
	}}}
	if got := root.Agents.Defaults.Execution.MaxParallelSubagentsValue(); got != 0 {
		t.Fatalf("MaxParallelSubagentsValue = %d, want 0", got)
	}
}

// Restored after the unused helpers these tests also touched were removed:
// the assertions below cover live code and would otherwise have gone with
// them.

func TestHookSchemaRemainingBranches(t *testing.T) {
	tests := []struct {
		name string
		hook HookCommand
	}{
		{name: "negative timeout", hook: HookCommand{Type: HookTypeCommand, Command: "echo ok", Timeout: -1}},
		{name: "unsupported shell", hook: HookCommand{Type: HookTypeCommand, Command: "echo ok", Shell: "zsh"}},
		{name: "empty prompt", hook: HookCommand{Type: HookTypePrompt}},
		{name: "empty agent prompt", hook: HookCommand{Type: HookTypeAgent}},
		{name: "bad http url", hook: HookCommand{Type: HookTypeHTTP, URL: "not-absolute"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateHookCommand(tt.hook); err == nil {
				t.Fatal("ValidateHookCommand error = nil")
			}
		})
	}
	if LooksLikePermissionRule("") {
		t.Fatal("empty permission rule should be false")
	}
	if HookConditionMatches("not a rule", "shell", "git status") {
		t.Fatal("invalid hook condition should not match")
	}
}

func TestSandboxValidateRemainingBranches(t *testing.T) {
	if err := validateSandboxConfig(nil); err != nil {
		t.Fatalf("nil sandbox config validation: %v", err)
	}
	if err := validateSandboxConfig(&Root{SandboxWorkspaceWrite: SandboxWorkspaceWrite{WritableRoots: []string{""}}}); err == nil {
		t.Fatal("expected writable root validation error")
	}
	normalizeSandboxConfig(nil)
	if got := normalizePathList(nil); got != nil {
		t.Fatalf("normalizePathList nil = %#v", got)
	}
	if got := normalizePathList([]string{"a", "a", " "}); len(got) != 2 || got[0] != "a" || got[1] != "" {
		t.Fatalf("normalizePathList duplicate/blank = %#v", got)
	}
	if got := normalizePathList([]string{" ", " "}); len(got) != 2 || got[0] != "" || got[1] != "" {
		t.Fatalf("normalizePathList blanks = %#v", got)
	}
	if got := normalizePathList([]string{" "}); len(got) != 1 || got[0] != "" {
		t.Fatalf("normalizePathList blank path preserves validation sentinel = %#v", got)
	}
	if err := validateSandboxPath("bad\x00path", "field"); err == nil {
		t.Fatal("NUL sandbox path error = nil")
	}
}

// The offer is on unless the user turns it off: the switch exists so the
// behavior can be stopped, not so it has to be started.
func TestEffectiveFeaturesSkillOfferDefaultsOn(t *testing.T) {
	var nilRoot *Root
	if !nilRoot.EffectiveFeatures().SkillOffer {
		t.Fatal("a nil config must read the offer as on")
	}
	if !(&Root{}).EffectiveFeatures().SkillOffer {
		t.Fatal("an unset features.skill_offer must read as on")
	}
	off := false
	cfg := &Root{Features: FeaturesSection{SkillOffer: &off}}
	if cfg.EffectiveFeatures().SkillOffer {
		t.Fatal("features.skill_offer: false must read as off")
	}
	on := true
	cfg = &Root{Features: FeaturesSection{SkillOffer: &on}}
	if !cfg.EffectiveFeatures().SkillOffer {
		t.Fatal("features.skill_offer: true must read as on")
	}
}

// The switch is one of the resolved features, so anything that reads the
// effective set sees the same answer /status reports.
func TestSkillOfferIsPartOfTheResolvedFeatureSet(t *testing.T) {
	off := false
	unset := (&Root{}).EffectiveFeatures()
	set := (&Root{Features: FeaturesSection{SkillOffer: &off}}).EffectiveFeatures()
	if unset.SkillOffer == set.SkillOffer {
		t.Fatalf("resolving the features lost features.skill_offer: %+v vs %+v", unset, set)
	}
	// The other flags must not have moved: the new field joins the set, it does
	// not replace it.
	if unset.Memories != set.Memories || unset.ExecPermissionApprovals != set.ExecPermissionApprovals {
		t.Fatalf("resolving skill_offer changed an unrelated flag: %+v vs %+v", unset, set)
	}
}

// TestLoadRecordsTheFilesTheConfigurationCameFrom pins /status's "Config
// files" row: the configuration names the file it was read from and the .env
// it applied, on the loaded value itself rather than in process-wide state.
func TestLoadRecordsTheFilesTheConfigurationCameFrom(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FOREBRAIN_HOME", dir)
	// Load applies .env to the process; registering the key restores it.
	t.Setenv("FOREBRAIN_SOURCE_FILES_TEST", "")
	cfgPath := filepath.Join(dir, "forebrain.yaml")
	if err := os.WriteFile(cfgPath, []byte("sandbox_mode: workspace-write\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.SourceFiles) != 1 || r.SourceFiles[0] != cfgPath {
		t.Fatalf("SourceFiles = %v, want just the config file", r.SourceFiles)
	}
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte("FOREBRAIN_SOURCE_FILES_TEST=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err = Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.SourceFiles) != 2 || r.SourceFiles[1] != envPath {
		t.Fatalf("SourceFiles = %v, want the config file then the applied .env", r.SourceFiles)
	}
}
