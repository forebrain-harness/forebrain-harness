// The built-in provider registry: the one list every surface offers — the
// terminal's setup and /connect, the web model form, and the gateway's
// catalog response. Before it existed the same providers were spelled out in
// three places that disagreed (pkg/config's default-URL table, pkg/tui's
// providerGroups, the web form's own dropdown), so adding one meant editing
// each of them and remembering which surface offered which regional plan.
//
// The registry is data only: nothing here talks to a network, and no surface
// derives behaviour from a row beyond the fields it reads.
package config

import "strings"

// CustomProvider is the provider id every user-defined model is stored under
// (owner 2026-11-07: the custom form asks for no provider; the model name is
// free text and may carry its own "vendor/" prefix). No built-in may use it.
const CustomProvider = "custom"

// Protocol paths: the full endpoint is base_url + one of these. A client
// appends its protocol's path to the base URL it is handed, so the path names
// the protocol rather than a URL anything is sent to — except for the custom
// protocol (URLVerbatim), which sends to base_url exactly as stored.
const (
	APIPathChatCompletions   = "/chat/completions"
	APIPathResponses         = "/responses"
	APIPathAnthropicMessages = "/v1/messages"
)

// ProviderEndpoint is one endpoint a provider serves. A provider with more
// than one endpoint offers them as separate choices (regional hosts, and
// subscription plans that live on their own host).
type ProviderEndpoint struct {
	Label, LabelZH string // "Global" / "国际站"; "" when the provider has one endpoint
	BaseURL        string
	APIPath        string // one of the protocol paths above
	// ChoiceLabel is the label the terminal's provider list shows for this
	// endpoint. "" derives it: the provider's API key label when the endpoint
	// has no Label, the Label itself otherwise. The field exists because two
	// providers' wording predates the registry and a list a user already knows
	// must not change.
	ChoiceLabel string
	// Hint is the line under that choice. "" derives the endpoint's host.
	Hint string
}

// BuiltinModel is capability data for a provider the model catalog does not
// carry (volcengine, byteplus): without it their context window, output limit
// and image support are unknown.
type BuiltinModel struct {
	ID                       string // the request model id, verbatim
	ContextWindow, MaxOutput int64
	Images                   bool
}

// BuiltinProvider is one built-in provider and the endpoints it serves.
type BuiltinProvider struct {
	ID             string // the catalog prefix: "deepseek", "meituan", …
	Label, LabelZH string // "DeepSeek" / "深度求索"
	// Hint is what the terminal's provider list says about the group. ""
	// means "API key"; the two sign-in providers say what they sign in to.
	Hint string
	// APIKeyLabel is what the key page asks for and what a choice without its
	// own label shows. "" means "<Label> API key".
	APIKeyLabel string
	// Endpoints, first = default.
	Endpoints []ProviderEndpoint
	// LoginBacked: the endpoint comes from a sign-in, not from the user.
	LoginBacked bool
	// Models is capability data for a provider the catalog has no row for.
	// Empty for every provider the catalog already knows.
	Models []BuiltinModel
}

// The endpoint values for the two sign-in providers are the client packages'
// own constants, copied here because pkg/config sits above them and may not
// import pkg/llm/openai or pkg/llm/opencode. provider_registry_test.go asserts
// the copies still equal the originals (openai.CodexBaseURL,
// opencode.ProviderBaseURL("opencode-go")).
const (
	chatGPTEndpointBaseURL = "https://chatgpt.com/backend-api/codex"
	openCodeGoEndpointURL  = "https://opencode.ai/inference/go"
)

// builtinProviders is the display order every surface shows. Adding a
// provider is adding a row here.
var builtinProviders = []BuiltinProvider{
	{
		ID: "chatgpt", Label: "ChatGPT", LabelZH: "ChatGPT",
		Hint: "OAuth subscription login", LoginBacked: true,
		Endpoints: []ProviderEndpoint{{
			BaseURL:     chatGPTEndpointBaseURL,
			ChoiceLabel: "Sign in with ChatGPT",
			Hint:        "Use Codex included with your ChatGPT plan",
		}},
	},
	{
		ID: "opencode-go", Label: "OpenCode", LabelZH: "OpenCode",
		Hint: "Go subscription", LoginBacked: true,
		Endpoints: []ProviderEndpoint{{
			BaseURL:     openCodeGoEndpointURL,
			ChoiceLabel: "Sign in with OpenCode (Go subscription)",
			Hint:        "Device-code login; use your Go/Go Plus quota",
		}},
	},
	{
		ID: "openai", Label: "OpenAI", LabelZH: "OpenAI",
		Endpoints: []ProviderEndpoint{{
			BaseURL: "https://api.openai.com/v1",
			APIPath: APIPathChatCompletions,
			Hint:    "GPT models via OpenAI API",
		}},
	},
	{
		ID: "anthropic", Label: "Anthropic", LabelZH: "Anthropic",
		Endpoints: []ProviderEndpoint{{
			BaseURL: "https://api.anthropic.com",
			APIPath: APIPathAnthropicMessages,
			Hint:    "Claude models via Anthropic API",
		}},
	},
	{
		ID: "alibaba", Label: "Qwen Cloud", LabelZH: "通义千问",
		APIKeyLabel: "Qwen API key",
		Endpoints: []ProviderEndpoint{
			{Label: "Standard · China", LabelZH: "标准版·国内", BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1", APIPath: APIPathChatCompletions},
			{Label: "Standard · Global", LabelZH: "标准版·国际", BaseURL: "https://dashscope-intl.aliyuncs.com/compatible-mode/v1", APIPath: APIPathChatCompletions},
			{Label: "Coding Plan · China", LabelZH: "编程套餐·国内", BaseURL: "https://coding.dashscope.aliyuncs.com/v1", APIPath: APIPathChatCompletions},
			{Label: "Coding Plan · Global", LabelZH: "编程套餐·国际", BaseURL: "https://coding-intl.dashscope.aliyuncs.com/v1", APIPath: APIPathChatCompletions},
		},
	},
	{
		ID: "google", Label: "Google", LabelZH: "Google Gemini",
		Hint:        "Gemini API key",
		APIKeyLabel: "Google Gemini API key",
		Endpoints: []ProviderEndpoint{{
			BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai",
			APIPath: APIPathChatCompletions,
			Hint:    "Gemini via OpenAI-compatible endpoint",
		}},
	},
	{
		ID: "deepseek", Label: "DeepSeek", LabelZH: "深度求索",
		Endpoints: []ProviderEndpoint{{
			BaseURL: "https://api.deepseek.com",
			APIPath: APIPathChatCompletions,
			Hint:    "DeepSeek chat/reasoner models",
		}},
	},
	{
		ID: "xai", Label: "xAI", LabelZH: "xAI",
		Endpoints: []ProviderEndpoint{{
			BaseURL: "https://api.x.ai/v1",
			APIPath: APIPathChatCompletions,
			Hint:    "Grok via OpenAI-compatible endpoint",
		}},
	},
	{
		ID: "mistral", Label: "Mistral AI", LabelZH: "Mistral AI",
		APIKeyLabel: "Mistral API key",
		Endpoints: []ProviderEndpoint{{
			BaseURL: "https://api.mistral.ai/v1",
			APIPath: APIPathChatCompletions,
			Hint:    "Mistral via OpenAI-compatible endpoint",
		}},
	},
	{
		ID: "moonshotai", Label: "Moonshot AI", LabelZH: "月之暗面 Kimi",
		Hint:        "Kimi API key",
		APIKeyLabel: "Moonshot API key",
		Endpoints: []ProviderEndpoint{{
			BaseURL: "https://api.moonshot.ai/v1",
			APIPath: APIPathChatCompletions,
			Hint:    "Moonshot/Kimi OpenAI-compatible endpoint",
			// The row a person picks names the model family, the key page
			// names the vendor: both are today's wording.
			ChoiceLabel: "Kimi API key",
		}},
	},
	{
		// Z.AI's own pay-as-you-go endpoint keeps its place as the default
		// one; the Coding Plan endpoints below it are the subscription
		// plans' dedicated hosts (owner 2026-11-07). The plan's Anthropic
		// endpoints speak Messages natively, so they are listed with that
		// protocol rather than as chat-compatible ones.
		ID: "zhipuai", Label: "Z.AI", LabelZH: "智谱",
		Endpoints: []ProviderEndpoint{
			{BaseURL: "https://api.z.ai/api/paas/v4", APIPath: APIPathChatCompletions, Hint: "GLM via OpenAI-compatible endpoint"},
			{Label: "Coding Plan · Global", LabelZH: "Coding Plan·国际", BaseURL: "https://api.z.ai/api/anthropic", APIPath: APIPathAnthropicMessages},
			{Label: "Coding Plan · Global (OpenAI)", LabelZH: "Coding Plan·国际（OpenAI 兼容）", BaseURL: "https://api.z.ai/api/coding/paas/v4", APIPath: APIPathChatCompletions},
			{Label: "Coding Plan · China", LabelZH: "Coding Plan·国内", BaseURL: "https://open.bigmodel.cn/api/anthropic", APIPath: APIPathAnthropicMessages},
			{Label: "Coding Plan · China (OpenAI)", LabelZH: "Coding Plan·国内（OpenAI 兼容）", BaseURL: "https://open.bigmodel.cn/api/coding/paas/v4", APIPath: APIPathChatCompletions},
			{Label: "Standard · China", LabelZH: "标准·国内", BaseURL: "https://open.bigmodel.cn/api/paas/v4", APIPath: APIPathChatCompletions},
		},
	},
	{
		ID: "minimax", Label: "MiniMax", LabelZH: "MiniMax",
		Endpoints: []ProviderEndpoint{
			{Label: "Global", LabelZH: "国际站", BaseURL: "https://api.minimax.io/anthropic", APIPath: APIPathAnthropicMessages, ChoiceLabel: "MiniMax API key (Global)", Hint: "Endpoint: api.minimax.io"},
			{Label: "CN", LabelZH: "国内", BaseURL: "https://api.minimaxi.com/anthropic", APIPath: APIPathAnthropicMessages, ChoiceLabel: "MiniMax API key (CN)", Hint: "Endpoint: api.minimaxi.com"},
		},
	},
	{
		// Meituan LongCat: the catalog already carries meituan/longcat-2.0 and
		// longcat-2.5-preview, so no Models data is needed here.
		ID: "meituan", Label: "Meituan LongCat", LabelZH: "美团 LongCat",
		Endpoints: []ProviderEndpoint{
			{Label: "OpenAI compatible", LabelZH: "OpenAI 兼容", BaseURL: "https://api.longcat.chat/openai/v1", APIPath: APIPathChatCompletions},
			{Label: "Anthropic compatible", LabelZH: "Anthropic 兼容", BaseURL: "https://api.longcat.chat/anthropic", APIPath: APIPathAnthropicMessages},
		},
	},
	{
		// Tencent Cloud TokenHub. The international host is documented only by
		// third parties as of 2026-11-07: it is offered as a hand-entered
		// endpoint, and the manual acceptance list verifies it with a real key.
		ID: "tencent", Label: "Tencent Cloud TokenHub", LabelZH: "腾讯云 TokenHub",
		Endpoints: []ProviderEndpoint{
			{Label: "China", LabelZH: "国内", BaseURL: "https://tokenhub.tencentmaas.com/v1", APIPath: APIPathChatCompletions},
			{Label: "Global", LabelZH: "国际站", BaseURL: "https://tokenhub-intl.tencentmaas.com/v1", APIPath: APIPathChatCompletions},
		},
	},
	{
		// Volcengine Ark: the plans' data planes speak Anthropic Messages and
		// have OpenAI-compatible twins under /v3 (owner 2026-11-07: Anthropic
		// is the default protocol). The catalog has no volcengine rows, so the
		// plan model table is capability data here.
		ID: "volcengine", Label: "Volcengine Ark", LabelZH: "火山方舟",
		Endpoints: []ProviderEndpoint{
			{Label: "Coding Plan", LabelZH: "Coding Plan", BaseURL: "https://ark.cn-beijing.volces.com/api/coding", APIPath: APIPathAnthropicMessages},
			{Label: "Agent Plan", LabelZH: "Agent Plan", BaseURL: "https://ark.cn-beijing.volces.com/api/plan", APIPath: APIPathAnthropicMessages},
			{Label: "Coding Plan · OpenAI", LabelZH: "Coding Plan · OpenAI 兼容", BaseURL: "https://ark.cn-beijing.volces.com/api/coding/v3", APIPath: APIPathChatCompletions},
			{Label: "Agent Plan · OpenAI", LabelZH: "Agent Plan · OpenAI 兼容", BaseURL: "https://ark.cn-beijing.volces.com/api/plan/v3", APIPath: APIPathChatCompletions},
			{Label: "Pay as you go", LabelZH: "按量付费", BaseURL: "https://ark.cn-beijing.volces.com/api/v3", APIPath: APIPathChatCompletions},
		},
		Models: []BuiltinModel{
			{ID: "doubao-seed-evolving", ContextWindow: 1024000, MaxOutput: 256000, Images: true},
			{ID: "doubao-seed-2.1-pro", ContextWindow: 256000, MaxOutput: 256000, Images: true},
			{ID: "doubao-seed-2.1-turbo", ContextWindow: 256000, MaxOutput: 256000, Images: true},
			{ID: "doubao-seed-2.0-lite", ContextWindow: 256000, MaxOutput: 128000, Images: true},
			{ID: "doubao-seed-2.0-mini", ContextWindow: 256000, MaxOutput: 128000, Images: true},
			{ID: "glm-5.3", ContextWindow: 1024000, MaxOutput: 128000},
			{ID: "kimi-k3", ContextWindow: 1024000, MaxOutput: 128000, Images: true},
			{ID: "deepseek-v4-pro", ContextWindow: 1024000, MaxOutput: 384000},
			{ID: "deepseek-v4-flash", ContextWindow: 1024000, MaxOutput: 384000},
			{ID: "minimax-m3", ContextWindow: 1024000, MaxOutput: 128000, Images: true},
		},
	},
	{
		// BytePlus ModelArk (the overseas Ark): only the Coding Plan host is
		// offered, and only its two plan models are known here.
		ID: "byteplus", Label: "BytePlus ModelArk", LabelZH: "BytePlus（海外）",
		Endpoints: []ProviderEndpoint{{
			Label: "Coding Plan", LabelZH: "Coding Plan",
			BaseURL: "https://ark.ap-southeast.bytepluses.com/api/coding",
			APIPath: APIPathAnthropicMessages,
		}},
		Models: []BuiltinModel{
			{ID: "doubao-seed-2.1-turbo", ContextWindow: 256000, MaxOutput: 65536, Images: true},
			{ID: "doubao-seed-2.1-pro", ContextWindow: 256000, MaxOutput: 65536, Images: true},
		},
	},
}

// BuiltinProviders returns a copy of the registry, in display order.
func BuiltinProviders() []BuiltinProvider {
	out := make([]BuiltinProvider, len(builtinProviders))
	for i, p := range builtinProviders {
		out[i] = p
		out[i].Endpoints = append([]ProviderEndpoint(nil), p.Endpoints...)
		out[i].Models = append([]BuiltinModel(nil), p.Models...)
	}
	return out
}

// BuiltinProviderByID returns the registry row for a provider id, matching
// case-insensitively and ignoring surrounding space.
func BuiltinProviderByID(id string) (BuiltinProvider, bool) {
	want := strings.ToLower(strings.TrimSpace(id))
	if want == "" {
		return BuiltinProvider{}, false
	}
	for _, p := range builtinProviders {
		if p.ID != want {
			continue
		}
		p.Endpoints = append([]ProviderEndpoint(nil), p.Endpoints...)
		p.Models = append([]BuiltinModel(nil), p.Models...)
		return p, true
	}
	return BuiltinProvider{}, false
}

// DefaultBaseURL is the endpoint a provider uses when its configuration names
// none: its first endpoint. "" for one this registry does not know or one
// whose endpoint comes from a sign-in.
func (p BuiltinProvider) DefaultBaseURL() string {
	if p.LoginBacked || len(p.Endpoints) == 0 {
		return ""
	}
	return p.Endpoints[0].BaseURL
}
