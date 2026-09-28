package process

import (
	"reflect"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

func loadTestCatalog(t *testing.T) {
	t.Helper()
	cat, err := llm.Parse(llm.EmbeddedModelsJSON())
	if err != nil {
		t.Fatalf("parse embedded models.json: %v", err)
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
