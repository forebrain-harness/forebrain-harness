package config

import "strings"

// ProviderDefaultBaseURLs are the endpoint each API-key provider's API is
// served at when the config names none. They are the same canonical endpoints
// the terminal's setup form offers; that form additionally offers regional and
// subscription variants of a few providers (two Qwen hosts, two MiniMax hosts),
// which a single default per provider cannot express, so it keeps its own
// list of choices while this table is what a form fills in.
//
// Providers served only by a sign-in (chatgpt, opencode, opencode-go) are
// deliberately absent: their endpoint comes from the login, and a manually
// entered row without that login could not be used.
var providerDefaultBaseURLs = map[string]string{
	"openai":     "https://api.openai.com/v1",
	"anthropic":  "https://api.anthropic.com",
	"alibaba":    "https://dashscope.aliyuncs.com/compatible-mode/v1",
	"google":     "https://generativelanguage.googleapis.com/v1beta/openai",
	"deepseek":   "https://api.deepseek.com",
	"xai":        "https://api.x.ai/v1",
	"mistral":    "https://api.mistral.ai/v1",
	"moonshotai": "https://api.moonshot.ai/v1",
	"zhipuai":    "https://api.z.ai/api/paas/v4",
	"minimax":    "https://api.minimax.io/anthropic/",
}

// ProviderDefaultBaseURL returns the default endpoint for a provider, or "" for
// one this table does not know (those need a base_url in the config).
func ProviderDefaultBaseURL(provider string) string {
	return providerDefaultBaseURLs[strings.ToLower(strings.TrimSpace(provider))]
}

// ProviderDefaultBaseURLTable returns a copy of the table, for a surface that
// offers the whole list rather than looking one provider up.
func ProviderDefaultBaseURLTable() map[string]string {
	out := make(map[string]string, len(providerDefaultBaseURLs))
	for provider, url := range providerDefaultBaseURLs {
		out[provider] = url
	}
	return out
}
