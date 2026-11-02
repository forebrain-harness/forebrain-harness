package config

// ApprovalPolicyMode is the ask-before-acting mode a preset derives.
//
// It is not a configuration key (plan 203): a user picks an approval preset,
// and safety.EffectiveConfig turns it into one of these modes. The type stays
// because the permission runtime, the store and every surface read a mode.
type ApprovalPolicyMode string

const (
	// ApprovalPolicyUntrusted asks before anything that is not provably safe.
	// Derived for a project the user has rejected.
	ApprovalPolicyUntrusted ApprovalPolicyMode = "untrusted"
	// ApprovalPolicyOnRequest asks when work needs to leave the sandbox.
	ApprovalPolicyOnRequest ApprovalPolicyMode = "on-request"
	// ApprovalPolicyNever asks nothing. Derived for the full-access preset.
	ApprovalPolicyNever ApprovalPolicyMode = "never"
)

// ApprovalPolicyConfig is the mode in force. It stays a struct rather than a
// bare mode because the permission snapshot carries it as one field beside the
// sandbox mode.
type ApprovalPolicyConfig struct {
	Mode ApprovalPolicyMode
}

func NewApprovalPolicy(mode ApprovalPolicyMode) ApprovalPolicyConfig {
	return ApprovalPolicyConfig{Mode: mode}
}

// String is the mode, with the on-request default for an unset value. Callers
// that must tell "unset" from "chosen" read Mode directly: an unset mode is
// what tells EffectiveConfig the pair has not been derived yet.
func (p ApprovalPolicyConfig) String() string {
	if p.Mode == "" {
		return string(ApprovalPolicyOnRequest)
	}
	return string(p.Mode)
}
