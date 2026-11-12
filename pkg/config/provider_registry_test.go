package config

import (
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

// TestBuiltinProviderRegistryShape pins the invariants every surface relies
// on: one row per provider id, ids that can be used as a catalog prefix, and
// endpoints a client can append its protocol path to.
func TestBuiltinProviderRegistryShape(t *testing.T) {
	providers := BuiltinProviders()
	if len(providers) == 0 {
		t.Fatal("the registry is empty")
	}
	seen := make(map[string]bool, len(providers))
	protocols := map[string]bool{
		APIPathChatCompletions:   true,
		APIPathResponses:         true,
		APIPathAnthropicMessages: true,
	}
	for _, p := range providers {
		if p.ID == "" {
			t.Fatalf("provider %+v has no id", p)
		}
		if p.ID != strings.ToLower(strings.TrimSpace(p.ID)) {
			t.Fatalf("provider id %q is not lowercase and trimmed", p.ID)
		}
		if p.ID == CustomProvider {
			t.Fatalf("%q is the reserved custom-model provider id", p.ID)
		}
		if seen[p.ID] {
			t.Fatalf("provider id %q appears twice", p.ID)
		}
		seen[p.ID] = true
		if strings.TrimSpace(p.Label) == "" || strings.TrimSpace(p.LabelZH) == "" {
			t.Fatalf("provider %q misses a label", p.ID)
		}
		if len(p.Endpoints) == 0 {
			t.Fatalf("provider %q has no endpoint", p.ID)
		}
		if p.LoginBacked && len(p.Endpoints) != 1 {
			t.Fatalf("sign-in provider %q must name one endpoint, has %d", p.ID, len(p.Endpoints))
		}
		for i, e := range p.Endpoints {
			if !strings.HasPrefix(e.BaseURL, "https://") {
				t.Fatalf("%s endpoint %d: %q is not https", p.ID, i, e.BaseURL)
			}
			if strings.HasSuffix(e.BaseURL, "/") {
				t.Fatalf("%s endpoint %d: %q keeps a trailing slash", p.ID, i, e.BaseURL)
			}
			if p.LoginBacked {
				if e.APIPath != "" {
					t.Fatalf("%s endpoint %d: a sign-in endpoint takes no api path, has %q", p.ID, i, e.APIPath)
				}
				continue
			}
			if !protocols[e.APIPath] {
				t.Fatalf("%s endpoint %d: %q is not one of the three protocol paths", p.ID, i, e.APIPath)
			}
		}
		for i, m := range p.Models {
			if strings.TrimSpace(m.ID) == "" || m.ContextWindow <= 0 || m.MaxOutput <= 0 {
				t.Fatalf("%s model %d is incomplete: %+v", p.ID, i, m)
			}
		}
	}
	// A provider the registry offers must be findable by its own id, in any
	// case, and an id nobody registered must stay unknown.
	for _, p := range providers {
		got, ok := BuiltinProviderByID("  " + strings.ToUpper(p.ID) + " ")
		if !ok || got.ID != p.ID {
			t.Fatalf("BuiltinProviderByID(%q) = %+v, %v", p.ID, got, ok)
		}
	}
	if _, ok := BuiltinProviderByID(""); ok {
		t.Fatal("the empty id resolved to a provider")
	}
	if _, ok := BuiltinProviderByID("not-a-provider"); ok {
		t.Fatal("an unregistered id resolved to a provider")
	}
}

// TestBuiltinProviderNewProviderEndpoints pins the rows owner added on
// 2026-11-07 verbatim: an endpoint is a public contract (a request goes to
// exactly this host and path), so a typo here is a broken provider.
func TestBuiltinProviderNewProviderEndpoints(t *testing.T) {
	cases := map[string][]ProviderEndpoint{
		"meituan": {
			{Label: "OpenAI compatible", LabelZH: "OpenAI 兼容", BaseURL: "https://api.longcat.chat/openai/v1", APIPath: APIPathChatCompletions},
			{Label: "Anthropic compatible", LabelZH: "Anthropic 兼容", BaseURL: "https://api.longcat.chat/anthropic", APIPath: APIPathAnthropicMessages},
		},
		"tencent": {
			{Label: "China", LabelZH: "国内", BaseURL: "https://tokenhub.tencentmaas.com/v1", APIPath: APIPathChatCompletions},
			{Label: "Global", LabelZH: "国际站", BaseURL: "https://tokenhub-intl.tencentmaas.com/v1", APIPath: APIPathChatCompletions},
		},
	}
	for id, want := range cases {
		got, ok := BuiltinProviderByID(id)
		if !ok {
			t.Fatalf("provider %q is missing from the registry", id)
		}
		if len(got.Endpoints) != len(want) {
			t.Fatalf("%s has %d endpoints, want %d", id, len(got.Endpoints), len(want))
		}
		for i := range want {
			if got.Endpoints[i] != want[i] {
				t.Fatalf("%s endpoint %d = %+v, want %+v", id, i, got.Endpoints[i], want[i])
			}
		}
	}
	// The plan's own acceptance grep: the two Z.AI Coding Plan hosts carry
	// /api/coding/paas/v4, one per cloud.
	zhipuai, ok := BuiltinProviderByID("zhipuai")
	if !ok {
		t.Fatal("zhipuai is missing from the registry")
	}
	coding := 0
	for _, e := range zhipuai.Endpoints {
		if strings.Contains(e.BaseURL, "api/coding/paas/v4") {
			coding++
		}
	}
	if coding < 2 {
		t.Fatalf("zhipuai Coding Plan endpoints = %d, want 2", coding)
	}
}

// TestBuiltinProvidersAreUsableWithoutCatalogRows checks the two ways a
// registry provider can be resolved to a model: the catalog knows the provider
// prefix, or the registry carries the plan's model table itself (volcengine,
// byteplus). A provider in neither state would offer models whose context
// window, output limit and image support are all unknown.
func TestBuiltinProvidersAreUsableWithoutCatalogRows(t *testing.T) {
	data, err := llm.RepoModelsJSONForTest()
	if err != nil {
		t.Fatal(err)
	}
	cat, err := llm.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	llm.SetGlobalCatalogForTest(cat)
	defer llm.SetGlobalCatalogForTest(nil)

	for _, p := range BuiltinProviders() {
		if p.LoginBacked {
			// chatgpt's models come from the account; opencode-go's catalog
			// rows exist for reference only.
			continue
		}
		if cat.HasProvider(p.ID) || len(p.Models) > 0 {
			continue
		}
		t.Fatalf("provider %q has neither catalog rows nor a registry model table", p.ID)
	}
}

// A provider a local subscription plugin registers behaves like a built-in in
// every lookup, and a login-backed one answers CredentialsBackedProvider — the
// question the model write path and the web form ask before allowing an empty
// key and keeping the login's own endpoint.
func TestRegisterProviderPluginMarksLoginBackedAndCredentialsBacked(t *testing.T) {
	ResetProviderPluginsForTest()
	t.Cleanup(ResetProviderPluginsForTest)

	register := func() {
		RegisterProviderPlugin(BuiltinProvider{
			ID: " PlugIn ", Label: "Plug In", LabelZH: "Plug In",
			Hint: "Subscription login", LoginBacked: true,
			Endpoints: []ProviderEndpoint{{BaseURL: "https://plugin.example.test"}},
		})
	}
	register()
	// Re-registering replaces in place instead of appending a second row.
	register()
	// The reserved custom-model provider id is never installable.
	RegisterProviderPlugin(BuiltinProvider{ID: CustomProvider, Label: "never", LabelZH: "never",
		Endpoints: []ProviderEndpoint{{BaseURL: "https://never.example.test"}}})

	providers := BuiltinProviders()
	ids := make([]string, 0, len(providers))
	for _, p := range providers {
		ids = append(ids, p.ID)
	}
	if got, want := ids[len(ids)-1], "plugin"; got != want {
		t.Fatalf("plugin row appended last, got %v", ids)
	}
	seen := map[string]int{}
	for _, id := range ids {
		seen[id]++
	}
	if seen["plugin"] != 1 || seen[CustomProvider] != 0 {
		t.Fatalf("registry ids %v: plugin must appear once and custom never", ids)
	}

	row, ok := BuiltinProviderByID("PLUGIN")
	if !ok || row.ID != "plugin" || !row.LoginBacked || row.DefaultBaseURL() != "" {
		t.Fatalf("BuiltinProviderByID = %+v ok=%t", row, ok)
	}
	if !CredentialsBackedProvider(" Plugin ") {
		t.Fatal("a login-backed plugin provider is credentials-backed")
	}
	if CredentialsBackedProvider("zhipuai") {
		t.Fatal("a provider no plugin registered stays key-backed")
	}
	// The login-backed row keeps an empty api_path instead of being handed the
	// default Chat Completions protocol.
	_, _, _, apiPath := NormalizeLLMModelIdentity("plugin", "m", "https://plugin.example.test/", "")
	if apiPath != "" {
		t.Fatalf("login-backed plugin row keeps no api path, got %q", apiPath)
	}
}
