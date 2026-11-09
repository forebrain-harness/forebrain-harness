package process

import (
	"fmt"
	"strings"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

// VerifyProvider reports whether provider is a known LLM provider. The set of
// known providers is derived entirely from models.json (each catalog key's
// prefix before the first '/'), plus the built-in model table a provider with
// no catalog rows is installed through, plus the reserved custom-model
// provider, which accepts any name by design.
//
// chatgpt and the two OpenCode surfaces are allowed explicitly through
// config.CredentialsBackedProvider: models.json is installed once and never
// overwritten, so a catalog entry added later would leave every existing home
// unable to save these providers. Their model metadata lives in their own
// packages instead (pkg/llm/opencode for opencode/opencode-go), the same way
// the ChatGPT subscription's does.
func VerifyProvider(provider string) error {
	p := strings.ToLower(strings.TrimSpace(provider))
	if p == "" {
		return fmt.Errorf("provider is required")
	}
	if appcfg.CredentialsBackedProvider(p) {
		return nil
	}
	if p == appcfg.CustomProvider {
		// A user-defined model: the provider id is a fixed placeholder and
		// the model name is whatever the user typed, so there is nothing to
		// look up.
		return nil
	}
	if llm.KnownProvider(p) {
		return nil
	}
	return fmt.Errorf("unsupported model provider: %s", provider)
}
