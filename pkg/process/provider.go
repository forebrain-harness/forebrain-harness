package process

import (
	"fmt"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

// ProviderKeyEnvMap returns the environment variable mapping for a provider's
// API key. The variable name follows the uniform scheme
// UPPERCASE(provider)_API_KEY.
func ProviderKeyEnvMap(provider string, key string) map[string]string {
	envName := strings.ToUpper(strings.TrimSpace(provider)) + "_API_KEY"
	return map[string]string{envName: key}
}

// VerifyProvider reports whether provider is a known LLM provider. The set of
// known providers is derived entirely from models.json (each catalog key's
// prefix before the first '/').
func VerifyProvider(provider string) error {
	p := strings.ToLower(strings.TrimSpace(provider))
	if p == "" {
		return fmt.Errorf("provider is required")
	}
	if p == "chatgpt" {
		return nil
	}
	if llm.KnownProvider(p) {
		return nil
	}
	return fmt.Errorf("unsupported provider: %s (not found in models.json)", provider)
}
