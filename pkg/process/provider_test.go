package process

import (
	"reflect"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

func loadTestCatalog(t *testing.T) {
	t.Helper()
	data, err := llm.RepoModelsJSONForTest()
	if err != nil {
		t.Fatalf("read repository models.json: %v", err)
	}
	cat, err := llm.Parse(data)
	if err != nil {
		t.Fatalf("parse repository models.json: %v", err)
	}
	llm.SetGlobalCatalogForTest(cat)
}

func TestVerifyProviderMatchesCatalogProviders(t *testing.T) {
	loadTestCatalog(t)
	// Providers derived from models.json (catalog prefixes).
	for _, provider := range []string{
		"openai",
		"anthropic",
		"deepseek",
		"google",
		"alibaba",
		"minimax",
		"xai",
		"mistral",
		"moonshotai",
		"zhipuai",
		"cohere",
		"meta",
		"nvidia",
		"perplexity",
		"stepfun",
		"tencent",
		"xiaomi",
	} {
		if err := VerifyProvider(provider); err != nil {
			t.Fatalf("VerifyProvider(%q) error = %v", provider, err)
		}
	}
	// Legacy aliases, removed gateway/local providers, and strings that are
	// not provider prefixes in models.json are rejected.
	for _, provider := range []string{"qwen", "moonshot", "zai", "openrouter", "groq", "ollama", "gpt", "claude-sonnet", "nous", "gemini", "anthropic-custom"} {
		if err := VerifyProvider(provider); err == nil {
			t.Fatalf("VerifyProvider(%q) error = nil, want unsupported", provider)
		}
	}
}

func TestProviderKeyEnvMapUsesUniformProviderVariables(t *testing.T) {
	cases := map[string]string{
		"openai":     "OPENAI_API_KEY",
		"anthropic":  "ANTHROPIC_API_KEY",
		"deepseek":   "DEEPSEEK_API_KEY",
		"google":     "GOOGLE_API_KEY",
		"alibaba":    "ALIBABA_API_KEY",
		"minimax":    "MINIMAX_API_KEY",
		"xai":        "XAI_API_KEY",
		"mistral":    "MISTRAL_API_KEY",
		"moonshotai": "MOONSHOTAI_API_KEY",
		"zhipuai":    "ZHIPUAI_API_KEY",
	}
	for provider, envName := range cases {
		want := map[string]string{envName: "key"}
		if got := ProviderKeyEnvMap(provider, "key"); !reflect.DeepEqual(got, want) {
			t.Fatalf("ProviderKeyEnvMap(%q) = %#v, want %#v", provider, got, want)
		}
	}
}

// TestVerifyProviderAllowsCredentialBackedProviders pins the three providers
// that carry no models.json entry but are configured by a login file
// (opencode.json / auth.json). Without the explicit allowlist an existing
// install — whose models.json is installed once and never overwritten — could
// never save them from the web or the terminal.
func TestVerifyProviderAllowsCredentialBackedProviders(t *testing.T) {
	loadTestCatalog(t)
	cases := []struct {
		provider string
		wantOK   bool
	}{
		{provider: "chatgpt", wantOK: true},
		{provider: "opencode", wantOK: true},
		{provider: "opencode-go", wantOK: true},
		{provider: " OpenCode ", wantOK: true},
		{provider: "opencode2", wantOK: false},
		{provider: "opencode-go-free", wantOK: false},
	}
	for _, tc := range cases {
		err := VerifyProvider(tc.provider)
		if tc.wantOK && err != nil {
			t.Fatalf("VerifyProvider(%q) error = %v, want nil", tc.provider, err)
		}
		if !tc.wantOK && err == nil {
			t.Fatalf("VerifyProvider(%q) error = nil, want unsupported", tc.provider)
		}
	}
}
