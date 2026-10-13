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

type WebSearchConfig struct {
	Provider string           `yaml:"provider,omitempty" json:"provider"`
	Tavily   *WebSearchTavily `yaml:"tavily,omitempty" json:"tavily,omitempty"`
	Brave    *WebSearchBrave  `yaml:"brave,omitempty" json:"brave,omitempty"`
	Baidu    *WebSearchBaidu  `yaml:"baidu,omitempty" json:"baidu,omitempty"`
}

// WebFetchConfig holds operator-controlled settings for the web_fetch tool.
//
// AllowPrivateIP deliberately lives here rather than in the tool's input
// schema: it widens what the agent can reach, and content the agent fetches can
// carry instructions, so leaving it model-settable would let a fetched page ask
// for internal network access. Only the operator can grant it. Cloud metadata
// endpoints stay blocked either way.
type WebFetchConfig struct {
	AllowPrivateIP bool `yaml:"allow_private_ip,omitempty" json:"allow_private_ip"`
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
// machine and copy the whole cache directory across; the layout is the
// decompressed original files. The built-in ids and the model names they cover
// are listed in assets/tokenizers/README.md.
//
// The zero value is the out-of-the-box default: the download hubs are
// HuggingFace then ModelScope, the endpoints are their public URLs, and the
// default tokenizer is gpt-o200k.
type TokenEstimateConfig struct {
	// Hubs is the download order; each entry is "huggingface" or "modelscope".
	// Empty means both, in that order.
	Hubs []string `yaml:"hubs,omitempty" json:"hubs,omitempty"`
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
// Tokenizer (a built-in id) or Path (a local tokenizer.json) is set.
type TokenizerOverride struct {
	Model     string `yaml:"model" json:"model"`                             // model-name prefix, case-insensitive
	Tokenizer string `yaml:"tokenizer,omitempty" json:"tokenizer,omitempty"` // a built-in tokenizer id
	Path      string `yaml:"path,omitempty" json:"path,omitempty"`           // a local tokenizer.json
}

type ContextInjectConfig struct {
	MaxPreHookRulesChars int `yaml:"max_pre_hook_rules_chars,omitempty" json:"max_pre_hook_rules_chars,omitempty"`
	ModelContextTokens   int `yaml:"model_context_tokens,omitempty" json:"model_context_tokens,omitempty"`
	WarnRemainingTokens  int `yaml:"warn_remaining_tokens,omitempty" json:"warn_remaining_tokens,omitempty"`
	BlockRemainingTokens int `yaml:"block_remaining_tokens,omitempty" json:"block_remaining_tokens,omitempty"`
}

func (c ContextInjectConfig) MaxPreHookRulesCharsOr(defaultN int) int {
	if c.MaxPreHookRulesChars > 0 {
		return c.MaxPreHookRulesChars
	}
	return defaultN
}
