// Tool-facing settings: web search, web fetch, token estimation, context injection.
package config

type WebSearchTavily struct {
	APIKey      string `yaml:"api_key,omitempty" json:"api_key"`
	SearchDepth string `yaml:"search_depth,omitempty" json:"search_depth"`
}

type WebSearchBrave struct {
	APIKey string `yaml:"api_key,omitempty" json:"api_key"`
}

type WebSearchBaidu struct {
	APIKey string `yaml:"api_key,omitempty" json:"api_key"`
}

type WebSearchDeepSeek struct {
	APIKey string `yaml:"api_key,omitempty" json:"api_key"`
}

// WebSearchProviderNone is the stored agents.defaults.web_search.provider of an
// install that chose no service. It means exactly what the empty value meant
// before plan 207 started writing the setting into the database: the runtime
// picks the first provider it has a key for, so an install that never chose one
// keeps working. The web settings page shows it as 「不启用」.
const WebSearchProviderNone = "none"

type WebSearchConfig struct {
	// Provider selects the search service: none, tavily, brave, baidu or
	// deepseek. Empty is the same as none and is only what a configuration
	// written before plan 207 still carries.
	Provider string             `yaml:"provider,omitempty" json:"provider"`
	Tavily   *WebSearchTavily   `yaml:"tavily,omitempty" json:"tavily,omitempty"`
	Brave    *WebSearchBrave    `yaml:"brave,omitempty" json:"brave,omitempty"`
	Baidu    *WebSearchBaidu    `yaml:"baidu,omitempty" json:"baidu,omitempty"`
	DeepSeek *WebSearchDeepSeek `yaml:"deepseek,omitempty" json:"deepseek,omitempty"`
}

// WebFetchConfig holds operator-controlled settings for the web_fetch tool.
//
// AllowPrivateIP deliberately lives here rather than in the tool's input
// schema: it widens what the agent can reach, and content the agent fetches can
// carry instructions, so leaving it model-settable would let a fetched page ask
// for internal network access. Only the operator can grant it. Cloud metadata
// endpoints stay blocked either way.
type WebFetchDeepSeek struct {
	APIKey string `yaml:"api_key,omitempty" json:"api_key"`
}

// WebFetchProviderBuiltin is the stored agents.defaults.web_fetch.provider of an
// install fetching through the built-in direct HTTP fetcher — the value the web
// settings page shows as 「内置抓取」, and the meaning the empty value had before
// plan 207 started writing it into the database.
const WebFetchProviderBuiltin = "builtin"

type WebFetchConfig struct {
	// Provider selects the fetch path: builtin or deepseek. Empty is the same
	// as builtin and is only what a configuration written before plan 207
	// still carries. The built-in fetcher is never auto-routed away from: only
	// an explicit "deepseek" switches fetches to DeepSeek's Fetch API
	// (server-side rendering to Markdown), so an unchanged install keeps its
	// default behavior.
	Provider       string            `yaml:"provider,omitempty" json:"provider"`
	DeepSeek       *WebFetchDeepSeek `yaml:"deepseek,omitempty" json:"deepseek,omitempty"`
	AllowPrivateIP bool              `yaml:"allow_private_ip,omitempty" json:"allow_private_ip"`
}

// TokenEstimateConfig controls local token counting for context assembly and tools.
//
// Local estimation counts tokens with the tokenizer of the model in use, not
// with one global encoder. The tokenizer files for the mainstream model
// families ship beside the binary; on first use one is copied into the cache
// directory ($FOREBRAIN_HOME/cache/tokenizers/<id>/<file>) or extracted from
// beside the binary, and only when neither has it is it downloaded, from
// HuggingFace first and ModelScope second (configure the hubs and endpoints
// below). An enterprise/offline install can run forebrain once on a connected
// machine and copy the whole cache directory across, and set download: false so
// a tokenizer missing there is never fetched; the layout is the decompressed
// files exactly as they ship. The built-in ids and the model names they cover
// are listed in assets/tokenizers/README.md.
//
// The zero value is the out-of-the-box default: a missing tokenizer is
// downloaded, the download hubs are HuggingFace then ModelScope, the endpoints
// are their public URLs, and the default tokenizer is gpt-o200k.
type TokenEstimateConfig struct {
	// Hubs is the download order; each entry is "huggingface" or "modelscope".
	// Empty means both, in that order — use Download: false to never download.
	Hubs []string `yaml:"hubs,omitempty" json:"hubs,omitempty"`
	// Download: nil or true downloads a missing tokenizer; false never does
	// (offline/intranet installs).
	Download *bool `yaml:"download,omitempty" json:"download,omitempty"`
	// HuggingFaceEndpoint overrides the HuggingFace base URL ("" → $HF_ENDPOINT
	// → https://huggingface.co).
	HuggingFaceEndpoint string `yaml:"huggingface_endpoint,omitempty" json:"huggingface_endpoint,omitempty"`
	// ModelScopeEndpoint overrides the ModelScope base URL ("" →
	// https://modelscope.cn).
	ModelScopeEndpoint string `yaml:"modelscope_endpoint,omitempty" json:"modelscope_endpoint,omitempty"`
	// DefaultTokenizer is the built-in id used when neither a model prefix nor
	// a provider maps to one ("" → gpt-o200k).
	DefaultTokenizer string `yaml:"default_tokenizer,omitempty" json:"default_tokenizer,omitempty"`
	// Tokenizers maps model-name prefixes to specific tokenizers.
	Tokenizers []TokenizerOverride `yaml:"tokenizers,omitempty" json:"tokenizers,omitempty"`

	// Deprecated: ignored since the estimator picks a tokenizer per model.
	Encoding      string `yaml:"encoding,omitempty" json:"encoding,omitempty"`
	Model         string `yaml:"model,omitempty" json:"model,omitempty"`
	TokenizerPath string `yaml:"tokenizer_path,omitempty" json:"tokenizer_path,omitempty"`
}

// TokenizerOverride maps a model-name prefix to a tokenizer: exactly one of
// Tokenizer (a built-in id) or Path (the absolute path of a local
// tokenizer.json) is set.
type TokenizerOverride struct {
	Model     string `yaml:"model" json:"model"`                             // model-name prefix, case-insensitive
	Tokenizer string `yaml:"tokenizer,omitempty" json:"tokenizer,omitempty"` // a built-in tokenizer id
	Path      string `yaml:"path,omitempty" json:"path,omitempty"`           // an absolute path to a local tokenizer.json
}

// The context-pressure warning fires when this share of the effective input
// window is all that is left. It is the only pressure threshold: pressure has
// two levels, ok and warn.
const (
	MinWarnRemainingPercent     = 1
	MaxWarnRemainingPercent     = 50
	DefaultWarnRemainingPercent = 12
)

type ContextInjectConfig struct {
	MaxPreHookRulesChars int `yaml:"max_pre_hook_rules_chars,omitempty" json:"max_pre_hook_rules_chars,omitempty"`
	// ModelContextTokens is the configured context window, used when the model
	// catalog knows no window for the model in play. Both the auto-compaction
	// threshold and the pressure warning measure against it as a fallback.
	ModelContextTokens int `yaml:"model_context_tokens,omitempty" json:"model_context_tokens,omitempty"`
	// WarnRemainingPercent is the share of the effective input window that may
	// be left before the context warning fires. Unset (0) selects the default.
	WarnRemainingPercent int `yaml:"warn_remaining_percent,omitempty" json:"warn_remaining_percent,omitempty"`
}

// EffectiveWarnRemainingPercent is the share of the window that may be left
// before the warning fires: the configured percent, or the default when unset.
func (c ContextInjectConfig) EffectiveWarnRemainingPercent() int {
	if c.WarnRemainingPercent > 0 {
		return c.WarnRemainingPercent
	}
	return DefaultWarnRemainingPercent
}

func (c ContextInjectConfig) MaxPreHookRulesCharsOr(defaultN int) int {
	if c.MaxPreHookRulesChars > 0 {
		return c.MaxPreHookRulesChars
	}
	return defaultN
}
