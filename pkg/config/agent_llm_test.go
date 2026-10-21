package config

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

func TestMain(m *testing.M) {
	if data, err := llm.RepoModelsJSONForTest(); err == nil {
		if cat, err := llm.Parse(data); err == nil {
			llm.SetGlobalCatalogForTest(cat)
		}
	}
	os.Exit(m.Run())
}

func testConfig() Root {
	return Root{
		Agents: AgentsSection{
			Definitions: map[string]AgentDefinition{
				"main": {
					LLMProviders: []AgentLLMProviderConfig{
						{
							Provider: " openai ",
							Model:    " gpt-4o ",
							BaseURL:  " https://example.test ",
							APIKey:   " key ",
							Params:   LLMRequestParams(`{"temperature":0}`),
						},
						{Provider: "openai", Model: "gpt-4o-mini"},
					},
				},
				"custom": {
					LLMProviders: []AgentLLMProviderConfig{{Provider: "openai", Model: "custom-model"}},
				},
			},
		},
	}
}

func TestAgentLLMProviderConfigAPIPathJSON(t *testing.T) {
	t.Parallel()

	var cfg AgentLLMProviderConfig
	if err := json.Unmarshal([]byte(`{
		"provider": "openai",
		"model": "gpt-test",
		"api_key": "sk-test",
		"base_url": "http://example.test/v1",
		"api_path": "/responses"
	}`), &cfg); err != nil {
		t.Fatalf("UnmarshalJSON returned error: %v", err)
	}
	if got := cfg.APIPath; got != "/responses" {
		t.Fatalf("APIPath = %q, want /responses", got)
	}

	cloned := CloneAgentLLMProviderConfig(cfg)
	if got := cloned.APIPath; got != "/responses" {
		t.Fatalf("cloned APIPath = %q, want /responses", got)
	}
}

func TestReasoningEffortReadsParams(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		params LLMRequestParams
		want   string
	}{
		{"empty params", nil, ""},
		{"absent effort", LLMRequestParams(`{"top_p":1}`), ""},
		{"present effort", LLMRequestParams(`{"reasoning":{"effort":"high"}}`), "high"},
		{"padded effort", LLMRequestParams(`{"reasoning":{"effort":" medium "}}`), "medium"},
		{"invalid json", LLMRequestParams(`{not-json`), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ReasoningEffort(tc.params); got != tc.want {
				t.Fatalf("ReasoningEffort(%s) = %q, want %q", tc.params, got, tc.want)
			}
		})
	}
}

func TestWithReasoningEffortPreservesSiblings(t *testing.T) {
	t.Parallel()
	base := LLMRequestParams(`{"top_p":1,"reasoning":{"summary":"auto","effort":"low"}}`)

	set := WithReasoningEffort(base, " High ")
	if got := ReasoningEffort(set); got != "high" {
		t.Fatalf("effort after set = %q, want high", got)
	}
	var setPayload map[string]any
	if err := json.Unmarshal(set, &setPayload); err != nil {
		t.Fatalf("set params invalid json: %v", err)
	}
	if setPayload["top_p"] != float64(1) {
		t.Fatalf("top_p lost: %v", setPayload)
	}
	if reasoning, _ := setPayload["reasoning"].(map[string]any); reasoning["summary"] != "auto" {
		t.Fatalf("reasoning.summary lost: %v", setPayload)
	}
	if got := ReasoningEffort(base); got != "low" {
		t.Fatalf("input mutated: effort = %q, want low", got)
	}
	if string(base) != `{"top_p":1,"reasoning":{"summary":"auto","effort":"low"}}` {
		t.Fatalf("input bytes changed: %s", base)
	}

	cleared := WithReasoningEffort(base, "")
	if got := ReasoningEffort(cleared); got != "" {
		t.Fatalf("effort after clear = %q, want empty", got)
	}
	var clearedPayload map[string]any
	if err := json.Unmarshal(cleared, &clearedPayload); err != nil {
		t.Fatalf("cleared params invalid json: %v", err)
	}
	if clearedPayload["top_p"] != float64(1) {
		t.Fatalf("top_p lost on clear: %v", clearedPayload)
	}
	if reasoning, _ := clearedPayload["reasoning"].(map[string]any); reasoning["summary"] != "auto" {
		t.Fatalf("reasoning.summary lost on clear: %v", clearedPayload)
	}

	// Deep ownership: mutating the returned bytes cannot reach the input.
	mutable := WithReasoningEffort(base, "high")
	mutable[0] = ' '
	if got := ReasoningEffort(base); got != "low" {
		t.Fatalf("mutating returned bytes changed the input: effort = %q", got)
	}

	if got := WithReasoningEffort(nil, ""); got != nil {
		t.Fatalf("empty input with empty effort = %v, want nil", got)
	}
	if got := WithReasoningEffort(nil, "high"); got == nil || ReasoningEffort(got) != "high" {
		t.Fatalf("empty input with effort = %v, want reasoning.effort=high", got)
	}
}

func TestSetLLMProviderReasoningEffortExactEntry(t *testing.T) {
	t.Parallel()
	cfg := Root{
		Agents: AgentsSection{
			Definitions: map[string]AgentDefinition{
				"main": {
					LLMProviders: []AgentLLMProviderConfig{
						{Provider: "anthropic", Model: "claude-a", APIKey: "anthropic-key", BaseURL: "https://anthropic.test", Params: LLMRequestParams(`{"max_tokens":128}`)},
						{Provider: "openai", Model: "gpt-b", APIKey: "openai-key", BaseURL: "https://openai.test", Params: LLMRequestParams(`{"top_p":0.5,"reasoning":{"effort":"low"}}`)},
					},
				},
			},
		},
	}
	if err := SetLLMProviderReasoningEffort(&cfg, "main", "openai", "gpt-b", "high"); err != nil {
		t.Fatalf("SetLLMProviderReasoningEffort error: %v", err)
	}
	def := cfg.Agents.Definitions["main"]
	if len(def.LLMProviders) != 2 {
		t.Fatalf("provider count = %d, want 2", len(def.LLMProviders))
	}
	promoted := def.LLMProviders[0]
	if promoted.Provider != "openai" || promoted.Model != "gpt-b" {
		t.Fatalf("promoted entry = %s/%s, want openai/gpt-b", promoted.Provider, promoted.Model)
	}
	if promoted.APIKey != "openai-key" || promoted.BaseURL != "https://openai.test" {
		t.Fatalf("promoted entry lost its own credentials: %+v", promoted)
	}
	if got := ReasoningEffort(promoted.Params); got != "high" {
		t.Fatalf("promoted effort = %q, want high", got)
	}
	var payload map[string]any
	if err := json.Unmarshal(promoted.Params, &payload); err != nil {
		t.Fatalf("promoted params invalid json: %v", err)
	}
	if payload["top_p"] != float64(0.5) {
		t.Fatalf("promoted params lost top_p: %v", payload)
	}
	demoted := def.LLMProviders[1]
	if demoted.Provider != "anthropic" || demoted.Model != "claude-a" || demoted.APIKey != "anthropic-key" {
		t.Fatalf("demoted entry changed: %+v", demoted)
	}
	if got := ReasoningEffort(demoted.Params); got != "" {
		t.Fatalf("demoted entry gained an effort: %q", got)
	}
	if string(demoted.Params) != `{"max_tokens":128}` {
		t.Fatalf("demoted params changed: %s", demoted.Params)
	}

	// Clearing the effort removes it without touching anything else.
	if err := SetLLMProviderReasoningEffort(&cfg, "main", "openai", "gpt-b", ""); err != nil {
		t.Fatalf("clear effort error: %v", err)
	}
	if got := ReasoningEffort(cfg.Agents.Definitions["main"].LLMProviders[0].Params); got != "" {
		t.Fatalf("effort after clear = %q, want empty", got)
	}
}

func TestSetLLMProviderReasoningEffortModelsList(t *testing.T) {
	t.Parallel()
	cfg := Root{
		Agents: AgentsSection{
			Definitions: map[string]AgentDefinition{
				"main": {
					LLMProviders: []AgentLLMProviderConfig{{
						Provider: "openai",
						Models:   StringList{"gpt-first", "gpt-second"},
						APIKey:   "openai-key",
						Params:   LLMRequestParams(`{"top_p":0.9}`),
					}},
				},
			},
		},
	}
	if err := SetLLMProviderReasoningEffort(&cfg, "main", "openai", "gpt-second", "medium"); err != nil {
		t.Fatalf("models-list second item error: %v", err)
	}
	def := cfg.Agents.Definitions["main"]
	if len(def.LLMProviders) != 2 {
		t.Fatalf("expanded entries = %d, want 2", len(def.LLMProviders))
	}
	if def.LLMProviders[0].Model != "gpt-second" {
		t.Fatalf("promoted model = %q, want gpt-second", def.LLMProviders[0].Model)
	}
	if got := ReasoningEffort(def.LLMProviders[0].Params); got != "medium" {
		t.Fatalf("promoted effort = %q, want medium", got)
	}
	if def.LLMProviders[0].APIKey != "openai-key" {
		t.Fatalf("expanded entry lost credentials: %+v", def.LLMProviders[0])
	}
	if got := ReasoningEffort(def.LLMProviders[1].Params); got != "" {
		t.Fatalf("sibling gained effort: %q", got)
	}
}

func TestSetLLMProviderReasoningEffortMissingPairLeavesConfigUnchanged(t *testing.T) {
	t.Parallel()
	cfg := Root{
		Agents: AgentsSection{
			Definitions: map[string]AgentDefinition{
				"main": {
					LLMProviders: []AgentLLMProviderConfig{
						{Provider: "openai", Model: "gpt-a", Params: LLMRequestParams(`{"reasoning":{"effort":"low"}}`)},
						{Provider: "openai", Model: "gpt-b"},
					},
				},
			},
		},
	}
	if err := SetLLMProviderReasoningEffort(&cfg, "main", "openai", "gone-model", "high"); err == nil {
		t.Fatal("expected missing exact pair error")
	}
	def := cfg.Agents.Definitions["main"]
	if len(def.LLMProviders) != 2 || def.LLMProviders[0].Model != "gpt-a" || def.LLMProviders[1].Model != "gpt-b" {
		t.Fatalf("config changed on missing pair: %+v", def.LLMProviders)
	}
	if got := ReasoningEffort(def.LLMProviders[0].Params); got != "low" {
		t.Fatalf("config params changed on missing pair: %q", got)
	}
	if err := SetLLMProviderReasoningEffort(&cfg, "missing-agent", "openai", "gpt-a", "high"); err == nil {
		t.Fatal("expected unknown agent error")
	}
}

func TestPromoteMatchingLLMProviderExpandsModelsList(t *testing.T) {
	t.Parallel()
	cfg := Root{
		Agents: AgentsSection{
			Definitions: map[string]AgentDefinition{
				"main": {
					LLMProviders: []AgentLLMProviderConfig{
						{Provider: "anthropic", Model: "claude-a"},
						{Provider: "openai", Models: StringList{"gpt-first", "gpt-second"}},
					},
				},
			},
		},
	}
	if err := PromoteMatchingLLMProvider(&cfg, "main", "openai", "gpt-second"); err != nil {
		t.Fatalf("second-item promotion error: %v", err)
	}
	def := cfg.Agents.Definitions["main"]
	if len(def.LLMProviders) != 3 {
		t.Fatalf("expanded entries = %d, want 3", len(def.LLMProviders))
	}
	if def.LLMProviders[0].Provider != "openai" || def.LLMProviders[0].Model != "gpt-second" {
		t.Fatalf("promoted entry = %s/%s, want openai/gpt-second", def.LLMProviders[0].Provider, def.LLMProviders[0].Model)
	}
	if def.LLMProviders[1].Provider != "anthropic" || def.LLMProviders[1].Model != "claude-a" {
		t.Fatalf("old primary moved wrong: %+v", def.LLMProviders[1])
	}

	before := cfg
	if err := PromoteMatchingLLMProvider(&cfg, "main", "openai", "missing"); err == nil {
		t.Fatal("expected missing pair error")
	}
	if len(cfg.Agents.Definitions["main"].LLMProviders) != len(before.Agents.Definitions["main"].LLMProviders) {
		t.Fatalf("missing pair changed the provider list")
	}
}

// TestCredentialBackedProvidersSkipAPIKey pins the three providers whose
// credential is a file this machine holds rather than an api_key in
// configuration. They must validate with no api_key, and every other provider
// must still require one — the exemption exists so an OpenCode login, not a
// mounted secret, is what makes those rows complete.
func TestCredentialBackedProvidersSkipAPIKey(t *testing.T) {
	t.Parallel()
	const baseURL = "https://opencode.ai/inference"
	cases := []struct {
		name      string
		provider  string
		baseURL   string
		apiKey    string
		wantOK    bool
		wantField string
	}{
		{name: "opencode without key", provider: "opencode", baseURL: baseURL, wantOK: true},
		{name: "opencode-go without key", provider: "opencode-go", baseURL: baseURL + "/go", wantOK: true},
		{name: "opencode still needs base_url", provider: "opencode", baseURL: "", wantField: "base_url"},
		{name: "opencode-go still needs base_url", provider: "opencode-go", wantField: "base_url"},
		{name: "chatgpt unchanged", provider: "chatgpt", baseURL: "https://chatgpt.com/backend-api/codex", wantOK: true},
		{name: "unknown provider still needs key", provider: "openai", baseURL: baseURL, wantField: "api_key"},
		{name: "known provider with key", provider: "openai", baseURL: baseURL, apiKey: "sk-test", wantOK: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v := MainAgentLLMValidation{Provider: tc.provider, Model: "some-model", BaseURL: tc.baseURL, APIKey: tc.apiKey}
			if got := v.Complete(); got != tc.wantOK {
				t.Fatalf("Complete() = %v, want %v (missing=%v)", got, tc.wantOK, v.MissingFields())
			}
			missing := v.MissingFields()
			if tc.wantOK {
				if len(missing) != 0 {
					t.Fatalf("MissingFields() = %v, want none", missing)
				}
				return
			}
			if len(missing) != 1 || missing[0] != tc.wantField {
				t.Fatalf("MissingFields() = %v, want [%s]", missing, tc.wantField)
			}
		})
	}
}

// TestCredentialBackedProviderMatchingIsCaseInsensitive guards the two call
// sites that read a hand-written config: the exemption must not depend on how
// the provider name was typed.
func TestCredentialBackedProviderMatchingIsCaseInsensitive(t *testing.T) {
	t.Parallel()
	for _, provider := range []string{"opencode", "OpenCode", " OPENCODE-GO ", "opencode-go", "ChatGPT"} {
		if !CredentialsBackedProvider(provider) {
			t.Fatalf("CredentialsBackedProvider(%q) = false, want true", provider)
		}
	}
	for _, provider := range []string{"", "openai", "opencode-go-", "opencode2"} {
		if CredentialsBackedProvider(provider) {
			t.Fatalf("CredentialsBackedProvider(%q) = true, want false", provider)
		}
	}
}
