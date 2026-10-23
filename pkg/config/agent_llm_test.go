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

// TestEffectiveDefaultLLM pins the three states of the default-model
// resolution: absent default_model → first expanded entry; a configured
// default_model → the matched entry; a configured default_model naming
// nothing → nil (an error for callers, never a fall-back to the first entry).
func TestEffectiveDefaultLLM(t *testing.T) {
	t.Parallel()
	def := AgentDefinition{
		LLMProviders: []AgentLLMProviderConfig{
			{Provider: "anthropic", Model: "claude-a", APIKey: "anthropic-key"},
			{Provider: "openai", Models: StringList{"gpt-first", "deep/model-id"}, APIKey: "openai-key"},
		},
	}

	if got := EffectiveDefaultLLM(def); got == nil || got.Provider != "anthropic" || got.Model != "claude-a" {
		t.Fatalf("absent default_model = %+v, want first entry anthropic/claude-a", got)
	}
	if got := EffectiveDefaultLLM(AgentDefinition{}); got != nil {
		t.Fatalf("empty definition = %+v, want nil", got)
	}

	// Case-insensitive pair match, including a model id that itself
	// contains a slash (split on the FIRST slash only).
	second := def
	second.DefaultModel = " OpenAI / deep/model-id "
	if got := EffectiveDefaultLLM(second); got == nil || got.Provider != "openai" || got.Model != "deep/model-id" {
		t.Fatalf("default_model openai/deep/model-id = %+v, want the second expanded entry", got)
	}

	// Dangling value → nil, even though the list is non-empty.
	dangling := def
	dangling.DefaultModel = "openai/gone"
	if got := EffectiveDefaultLLM(dangling); got != nil {
		t.Fatalf("dangling default_model = %+v, want nil", got)
	}

	// Mutating the returned entry must not touch the definition's slice.
	got := EffectiveDefaultLLM(def)
	got.APIKey = "mutated"
	if def.LLMProviders[0].APIKey != "anthropic-key" {
		t.Fatal("returned entry aliases the definition's slice")
	}
}

// TestValidateAgentLLMConfiguredUsesDefaultModel pins that completeness is
// judged on the entry an empty selection actually resolves to, not the raw
// first list entry.
func TestValidateAgentLLMConfiguredUsesDefaultModel(t *testing.T) {
	t.Parallel()
	cfg := Root{Agents: AgentsSection{Definitions: map[string]AgentDefinition{
		"main": {
			LLMProviders: []AgentLLMProviderConfig{
				{Provider: "openai", Model: "gpt-a", APIKey: "key", BaseURL: "https://a.test"},
				{Provider: "anthropic", Model: "claude-b"}, // incomplete
			},
			DefaultModel: "anthropic/claude-b",
		},
	}}}
	v := ValidateAgentLLMConfigured(&cfg, "main")
	if v.Provider != "anthropic" || v.Model != "claude-b" {
		t.Fatalf("validation read %+v, want the default_model entry anthropic/claude-b", v)
	}
	if v.Complete() {
		t.Fatal("incomplete default entry judged complete")
	}
	if v.BaseURL != "" || v.APIKey != "" {
		t.Fatalf("incomplete default entry reported fields it does not carry: %+v", v)
	}
}
