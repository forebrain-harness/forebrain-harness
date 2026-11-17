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

// TestValidateAgentLLMConfiguredUsesDefaultModel pins that completeness is
// judged on the row an empty selection actually resolves to, not the table's
// first row.
func TestValidateAgentLLMConfiguredUsesDefaultModel(t *testing.T) {
	t.Parallel()
	cfg := Root{
		LLMModels: []LLMModelConfig{
			{ID: "llm-a", Provider: "openai", Model: "gpt-a", APIKey: "key", BaseURL: "https://a.test"},
			{ID: "llm-b", Provider: "anthropic", Model: "claude-b"}, // incomplete
		},
		Agents: AgentsSection{Definitions: map[string]AgentDefinition{
			"main": {DefaultModel: "llm-b"},
		}},
	}
	v := ValidateAgentLLMConfigured(&cfg, "main")
	if v.Provider != "anthropic" || v.Model != "claude-b" {
		t.Fatalf("validation read %+v, want the default_model row anthropic/claude-b", v)
	}
	if v.Complete() {
		t.Fatal("incomplete default entry judged complete")
	}
	if v.BaseURL != "" || v.APIKey != "" {
		t.Fatalf("incomplete default entry reported fields it does not carry: %+v", v)
	}
}
