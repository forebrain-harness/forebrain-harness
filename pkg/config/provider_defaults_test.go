package config

import (
	"net/url"
	"testing"
)

func TestProviderDefaultBaseURL(t *testing.T) {
	cases := map[string]string{
		"deepseek":    "https://api.deepseek.com",
		"  OpenAI  ":  "https://api.openai.com/v1",
		"minimax":     "https://api.minimax.io/anthropic/",
		"arcee-ai":    "",
		"chatgpt":     "",
		"opencode-go": "",
		"":            "",
	}
	for provider, want := range cases {
		if got := ProviderDefaultBaseURL(provider); got != want {
			t.Fatalf("ProviderDefaultBaseURL(%q) = %q, want %q", provider, got, want)
		}
	}
}

func TestProviderDefaultBaseURLTable(t *testing.T) {
	table := ProviderDefaultBaseURLTable()
	if len(table) == 0 {
		t.Fatal("the table is empty")
	}
	for provider, raw := range table {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("%s: %q does not parse: %v", provider, raw, err)
		}
		if u.Scheme != "https" || u.Host == "" {
			t.Fatalf("%s: %q is not an https endpoint", provider, raw)
		}
		if ProviderDefaultBaseURL(provider) != raw {
			t.Fatalf("%s: lookup and table disagree", provider)
		}
	}
	// A copy: a caller must not be able to edit what every surface reads.
	table["deepseek"] = "https://example.test"
	if ProviderDefaultBaseURL("deepseek") == "https://example.test" {
		t.Fatal("the table handed out its own map")
	}
}
