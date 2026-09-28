package config

type GuardrailsConfig struct {
	Input     GuardrailsInputConfig     `yaml:"input,omitempty" json:"input,omitempty"`
	Output    GuardrailsOutputConfig    `yaml:"output,omitempty" json:"output,omitempty"`
	Retrieval GuardrailsRetrievalConfig `yaml:"retrieval,omitempty" json:"retrieval,omitempty"`
}

// GuardrailsOutputConfig configures the output rail. The rail is always on
// unless explicitly disabled via the Enabled field.
type GuardrailsOutputConfig struct {
	Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
}

// GuardrailsInputConfig configures the input rail. The rail itself is always
// on; only its tunables (size limit and hard-block substrings) are
// configurable.
type GuardrailsInputConfig struct {
	MaxRunes        int      `yaml:"max_runes,omitempty" json:"max_runes,omitempty"`
	BlockSubstrings []string `yaml:"block_substrings,omitempty" json:"block_substrings,omitempty"`
}

// GuardrailsRetrievalConfig configures the retrieval rail. The rail itself is
// always on; only the per-chunk size cap is configurable.
type GuardrailsRetrievalConfig struct {
	MaxChunkRunes int `yaml:"max_chunk_runes,omitempty" json:"max_chunk_runes,omitempty"`
}
