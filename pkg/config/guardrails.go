package config

// DefaultGuardrailsMaxRunes is the input rail's size limit when the
// configuration leaves max_runes unset. It lives here, next to the field it
// defaults, so no other package restates it.
const DefaultGuardrailsMaxRunes = 200000

type GuardrailsConfig struct {
	Input GuardrailsInputConfig `yaml:"input,omitempty" json:"input,omitempty"`
}

// GuardrailsInputConfig configures the input rail. The rail itself is always
// on; only its tunables (size limit and hard-block substrings) are
// configurable.
type GuardrailsInputConfig struct {
	MaxRunes        int      `yaml:"max_runes,omitempty" json:"max_runes,omitempty"`
	BlockSubstrings []string `yaml:"block_substrings,omitempty" json:"block_substrings,omitempty"`
}
