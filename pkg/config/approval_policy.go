package config

import (
	"encoding/json"
	"fmt"
	"strings"
)

type ApprovalPolicyMode string

const (
	ApprovalPolicyUntrusted ApprovalPolicyMode = "untrusted"
	ApprovalPolicyOnRequest ApprovalPolicyMode = "on-request"
	ApprovalPolicyGranular  ApprovalPolicyMode = "granular"
	ApprovalPolicyNever     ApprovalPolicyMode = "never"
)

type GranularApprovalConfig struct {
	SandboxApproval    bool `yaml:"sandbox_approval" json:"sandbox_approval"`
	Rules              bool `yaml:"rules" json:"rules"`
	SkillApproval      bool `yaml:"skill_approval,omitempty" json:"skill_approval,omitempty"`
	RequestPermissions bool `yaml:"request_permissions,omitempty" json:"request_permissions,omitempty"`
	MCPElicitations    bool `yaml:"mcp_elicitations" json:"mcp_elicitations"`
}

type ApprovalPolicyConfig struct {
	Mode     ApprovalPolicyMode
	Granular GranularApprovalConfig
}

func NewApprovalPolicy(mode ApprovalPolicyMode) ApprovalPolicyConfig {
	return ApprovalPolicyConfig{Mode: mode}
}

func (p ApprovalPolicyConfig) String() string {
	if p.Mode == "" {
		return string(ApprovalPolicyOnRequest)
	}
	return string(p.Mode)
}

// MarshalYAML persists an unset policy as an empty value so the Root marshaler
// omits the key. Writing String()'s "on-request" fallback would silently turn
// "not configured" into an explicit choice and permanently disable the
// project-trust derivation in sandboxrt.EffectiveConfig.
func (p ApprovalPolicyConfig) MarshalYAML() (any, error) {
	if p.Mode == ApprovalPolicyGranular {
		return map[string]GranularApprovalConfig{"granular": p.Granular}, nil
	}
	if p.Mode == "" {
		return "", nil
	}
	return p.String(), nil
}

func (p *ApprovalPolicyConfig) UnmarshalYAML(unmarshal func(any) error) error {
	var mode string
	if err := unmarshal(&mode); err == nil {
		return p.setString(mode)
	}
	var raw map[string]rawGranularApprovalConfig
	if err := unmarshal(&raw); err != nil {
		return fmt.Errorf("invalid approval_policy: %w", err)
	}
	return p.setGranular(raw)
}

func (p ApprovalPolicyConfig) MarshalJSON() ([]byte, error) {
	if p.Mode == ApprovalPolicyGranular {
		return json.Marshal(map[string]GranularApprovalConfig{"granular": p.Granular})
	}
	return json.Marshal(p.String())
}

func (p *ApprovalPolicyConfig) UnmarshalJSON(data []byte) error {
	var mode string
	if err := json.Unmarshal(data, &mode); err == nil {
		return p.setString(mode)
	}
	var raw map[string]rawGranularApprovalConfig
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("invalid approval_policy: %w", err)
	}
	return p.setGranular(raw)
}

func (p *ApprovalPolicyConfig) setString(raw string) error {
	mode := ApprovalPolicyMode(strings.ToLower(strings.TrimSpace(raw)))
	if mode == "on-failure" {
		mode = ApprovalPolicyOnRequest
	}
	switch mode {
	case ApprovalPolicyUntrusted, ApprovalPolicyOnRequest, ApprovalPolicyNever:
		p.Mode = mode
		p.Granular = GranularApprovalConfig{}
		return nil
	case ApprovalPolicyGranular:
		return fmt.Errorf("granular approval_policy requires a granular configuration object")
	default:
		return fmt.Errorf("approval_policy must be untrusted, on-request, never, or a granular object")
	}
}

type rawGranularApprovalConfig struct {
	SandboxApproval    *bool `yaml:"sandbox_approval" json:"sandbox_approval"`
	Rules              *bool `yaml:"rules" json:"rules"`
	SkillApproval      bool  `yaml:"skill_approval,omitempty" json:"skill_approval,omitempty"`
	RequestPermissions bool  `yaml:"request_permissions,omitempty" json:"request_permissions,omitempty"`
	MCPElicitations    *bool `yaml:"mcp_elicitations" json:"mcp_elicitations"`
}

func (p *ApprovalPolicyConfig) setGranular(raw map[string]rawGranularApprovalConfig) error {
	if len(raw) != 1 {
		return fmt.Errorf("approval_policy granular object must contain only granular")
	}
	cfg, ok := raw["granular"]
	if !ok {
		return fmt.Errorf("approval_policy object must contain granular")
	}
	if cfg.SandboxApproval == nil || cfg.Rules == nil || cfg.MCPElicitations == nil {
		return fmt.Errorf("granular approval_policy requires sandbox_approval, rules, and mcp_elicitations")
	}
	p.Mode = ApprovalPolicyGranular
	p.Granular = GranularApprovalConfig{
		SandboxApproval:    *cfg.SandboxApproval,
		Rules:              *cfg.Rules,
		SkillApproval:      cfg.SkillApproval,
		RequestPermissions: cfg.RequestPermissions,
		MCPElicitations:    *cfg.MCPElicitations,
	}
	return nil
}
