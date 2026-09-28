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
// The zero value is the out-of-the-box default and is safe to use without YAML: at Runner load,
// if both Encoding and Model are empty, the first LLM provider model on the active agent is used;
// if that is also empty, the implementation uses the cl100k_base encoding. The rune/4 fallback is
// always used when tiktoken init fails (it is not configurable).
//
// To use a HuggingFace tokenizer (e.g. for DeepSeek models that use LlamaTokenizerFast),
// set TokenizerPath to the path of a tokenizer.json file. When set, it takes precedence over
// Encoding and Model.
type TokenEstimateConfig struct {
	Encoding      string `yaml:"encoding,omitempty" json:"encoding,omitempty"`
	Model         string `yaml:"model,omitempty" json:"model,omitempty"`
	TokenizerPath string `yaml:"tokenizer_path,omitempty" json:"tokenizer_path,omitempty"`
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
