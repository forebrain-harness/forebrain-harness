package config

import "strings"

type Gateway struct {
	HTTPAddr string      `yaml:"http_addr,omitempty" json:"http_addr,omitempty"`
	Auth     GatewayAuth `yaml:"auth,omitempty" json:"auth,omitempty"`
}

type GatewayAuth struct {
	Mode  string `yaml:"mode,omitempty" json:"mode,omitempty"`
	Token string `yaml:"token,omitempty" json:"token,omitempty"`
}

type SandboxMode string

const (
	SandboxModeReadOnly         SandboxMode = "read-only"
	SandboxModeWorkspaceWrite   SandboxMode = "workspace-write"
	SandboxModeDangerFullAccess SandboxMode = "danger-full-access"
)

type SandboxWorkspaceWrite struct {
	WritableRoots []string `yaml:"writable_roots,omitempty" json:"writable_roots,omitempty"`
	// NetworkAccess controls whether sandboxed workspace-write commands may
	// reach the network. It is an optional boolean: nil means "unset" and
	// selects the default (true) rather than false. Read the resolved value
	// through EffectiveNetworkAccess.
	NetworkAccess       *bool `yaml:"network_access,omitempty" json:"network_access,omitempty"`
	ExcludeTmpdirEnvVar bool  `yaml:"exclude_tmpdir_env_var" json:"exclude_tmpdir_env_var"`
	ExcludeSlashTmp     bool  `yaml:"exclude_slash_tmp" json:"exclude_slash_tmp"`
	// GoCacheMode controls automatic cache plumbing for trusted Go projects.
	// Empty defaults to isolated. shared is an explicit performance-oriented
	// opt-in that gives sandboxed commands write access to host Go caches.
	GoCacheMode string `yaml:"go_cache_mode,omitempty" json:"go_cache_mode,omitempty"`
}

const (
	GoCacheModeIsolated = "isolated"
	GoCacheModeShared   = "shared"
	GoCacheModeDisabled = "disabled"
)

// EffectiveNetworkAccess resolves whether sandboxed workspace-write commands
// may reach the network. Unset (nil) defaults to true.
func (s SandboxWorkspaceWrite) EffectiveNetworkAccess() bool {
	return boolValue(s.NetworkAccess, true)
}

func (s SandboxWorkspaceWrite) EffectiveGoCacheMode() string {
	mode := strings.ToLower(strings.TrimSpace(s.GoCacheMode))
	if mode == "" {
		return GoCacheModeIsolated
	}
	return mode
}

type AutoReview struct {
	Policy string `yaml:"policy,omitempty" json:"policy,omitempty"`
}

type CompactSection struct {
	// Prompt overrides the local compaction handoff prompt.
	Prompt string `yaml:"prompt,omitempty" json:"prompt,omitempty"`
	// ModelAutoCompactTokenLimit is clamped to 90% of the model context window.
	ModelAutoCompactTokenLimit int `yaml:"model_auto_compact_token_limit,omitempty" json:"model_auto_compact_token_limit,omitempty"`
	// ModelAutoCompactTokenLimitScope is "total" (default) or "body_after_prefix".
	ModelAutoCompactTokenLimitScope string `yaml:"model_auto_compact_token_limit_scope,omitempty" json:"model_auto_compact_token_limit_scope,omitempty"`
	// RemoteCompaction opts in to provider-native compaction. It defaults to
	// false: a remote checkpoint stores an opaque, provider-specific item that
	// only the issuing provider can replay, so a session that later switches to
	// another provider is left holding a checkpoint it cannot use. Local
	// summarization produces a portable checkpoint and is the safe default.
	// Enable this only when the session stays on one provider.
	RemoteCompaction *bool `yaml:"remote_compaction,omitempty" json:"remote_compaction,omitempty"`
	// RemoteCompactionV2 defaults to true; false uses /responses/assembly.
	// Only consulted when RemoteCompaction is enabled.
	RemoteCompactionV2 *bool `yaml:"remote_compaction_v2,omitempty" json:"remote_compaction_v2,omitempty"`
}

// UseRemoteCompaction reports whether provider-native compaction is enabled.
// Disabled unless explicitly turned on.
func (c CompactSection) UseRemoteCompaction() bool {
	return c.RemoteCompaction != nil && *c.RemoteCompaction
}

func (c CompactSection) UseRemoteV2() bool {
	return c.RemoteCompactionV2 == nil || *c.RemoteCompactionV2
}

type Root struct {
	// SourceFiles lists the files this configuration was read from, in read
	// order: the config file itself, then the forebrain home's .env when one was
	// applied. Runtime-only — set by Load, never persisted.
	SourceFiles           []string              `yaml:"-" json:"-"`
	ApprovalPolicy        ApprovalPolicyConfig  `yaml:"approval_policy,omitempty" json:"approval_policy,omitempty"`
	ApprovalsReviewer     string                `yaml:"approvals_reviewer,omitempty" json:"approvals_reviewer,omitempty"`
	AutoReview            AutoReview            `yaml:"auto_review,omitempty" json:"auto_review,omitempty"`
	DefaultPermissions    string                `yaml:"default_permissions,omitempty" json:"default_permissions,omitempty"`
	Permissions           PermissionProfiles    `yaml:"permissions,omitempty" json:"permissions,omitempty"`
	SandboxMode           SandboxMode           `yaml:"sandbox_mode,omitempty" json:"sandbox_mode,omitempty"`
	SandboxWorkspaceWrite SandboxWorkspaceWrite `yaml:"sandbox_workspace_write,omitempty" json:"sandbox_workspace_write,omitempty"`
	Compact               CompactSection        `yaml:"compact,omitempty" json:"compact,omitempty"`
	Gateway               Gateway               `yaml:"gateway,omitempty" json:"gateway,omitempty"`
	Agents                AgentsSection         `yaml:"agents,omitempty" json:"agents,omitempty"`
	Hooks                 HooksSettings         `yaml:"hooks,omitempty" json:"hooks,omitempty"`
	Memories              MemorySection         `yaml:"memories,omitempty" json:"memories,omitempty"`
	Tools                 ToolsSection          `yaml:"tools,omitempty" json:"tools,omitempty"`
	Features              FeaturesSection       `yaml:"features,omitempty" json:"features,omitempty"`
	LSP                   LSPSection            `yaml:"lsp,omitempty" json:"lsp,omitempty"`
	Windows               WindowsSection        `yaml:"windows,omitempty" json:"windows,omitempty"`
	Credentials           CredentialsSection    `yaml:"credentials,omitempty" json:"credentials,omitempty"`
}

// CredentialsSection points at credential files kept outside forebrain.yaml.
// They hold OAuth tokens, so they stay in their own files rather than being
// inlined into a config a user may share or check in.
type CredentialsSection struct {
	// ChatGPT is the path to the ChatGPT/Codex auth.json. Empty means
	// <FOREBRAIN_HOME>/auth.json. A relative path resolves against FOREBRAIN_HOME so
	// the config stays portable; a leading ~ is expanded. The file uses the
	// Codex CLI's format, so pointing this at ~/.codex/auth.json works and one
	// login serves both tools.
	ChatGPT string `yaml:"chatgpt,omitempty" json:"chatgpt,omitempty"`
}

type FeaturesSection struct {
	ExecPermissionApprovals *bool                     `yaml:"exec_permission_approvals,omitempty" json:"exec_permission_approvals,omitempty"`
	RequestPermissionsTool  *bool                     `yaml:"request_permissions_tool,omitempty" json:"request_permissions_tool,omitempty"`
	Memories                *bool                     `yaml:"memories,omitempty" json:"memories,omitempty"`
	SkillOffer              *bool                     `yaml:"skill_offer,omitempty" json:"skill_offer,omitempty"`
	LSP                     *bool                     `yaml:"lsp,omitempty" json:"lsp,omitempty"`
	NetworkProxy            NetworkProxyFeatureConfig `yaml:"network_proxy,omitempty" json:"network_proxy,omitempty"`
}

// EffectiveFeatures returns the resolved boolean values for all feature flags,
// applying fallback defaults when the config does not specify a value.
func (r *Root) EffectiveFeatures() EffectiveFeaturesConfig {
	f := FeaturesSection{}
	if r != nil {
		f = r.Features
	}
	return EffectiveFeaturesConfig{
		ExecPermissionApprovals: boolValue(f.ExecPermissionApprovals, false),
		RequestPermissionsTool:  boolValue(f.RequestPermissionsTool, false),
		Memories:                boolValue(f.Memories, true),
		SkillOffer:              boolValue(f.SkillOffer, true),
		LSP:                     boolValue(f.LSP, true),
	}
}

// EffectiveFeaturesConfig holds resolved feature flag values with defaults applied.
type EffectiveFeaturesConfig struct {
	ExecPermissionApprovals bool
	RequestPermissionsTool  bool
	Memories                bool
	SkillOffer              bool
	LSP                     bool
}

type WindowsSandboxMode string

const (
	WindowsSandboxUnelevated WindowsSandboxMode = "unelevated"
	WindowsSandboxElevated   WindowsSandboxMode = "elevated"
)

type WindowsSection struct {
	Sandbox               WindowsSandboxMode `yaml:"sandbox,omitempty" json:"sandbox,omitempty"`
	SandboxPrivateDesktop *bool              `yaml:"sandbox_private_desktop,omitempty" json:"sandbox_private_desktop,omitempty"`
}

func (w WindowsSection) UseSandboxPrivateDesktop() bool {
	return w.SandboxPrivateDesktop == nil || *w.SandboxPrivateDesktop
}

// ToolsSection holds per-tool behavior settings for the built-in tools.
type ToolsSection struct {
	CommandRewrite CommandRewriteSection `yaml:"command_rewrite,omitempty" json:"command_rewrite,omitempty"`
}

// CommandRewriteSection controls shell command rewriting, which inserts
// presentation-only flags (pager off, color off) before the command is sent
// for approval. Because rewriting precedes approval, the user always approves
// the command that actually runs.
//
// Enabled is a *bool so an unset value is distinguishable from an explicit
// false. Unset means enabled.
type CommandRewriteSection struct {
	Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
}

// UseRewrite is the single definition of the command-rewrite default. Both the
// runtime and the value persisted to forebrain.yaml resolve through it, so the
// written configuration cannot drift from the behavior it describes.
func (c CommandRewriteSection) UseRewrite() bool {
	return boolValue(c.Enabled, true)
}

// memorySearchDefaultTopK and memorySearchMaxTopK bound how many memories one
// search may return. Recall is wide and the results are ranked, so the first
// few carry nearly all the value while the rest still cost the model context —
// which is why the default is small and the ceiling is low. A configuration
// asking for more is clamped to the ceiling, not rejected.
const (
	memorySearchDefaultTopK = 5
	memorySearchMaxTopK     = 20
)

type MemorySection struct {
	DisableOnExternalContext       *bool `yaml:"disable_on_external_context,omitempty" json:"disable_on_external_context,omitempty"`
	GenerateMemories               *bool `yaml:"generate_memories,omitempty" json:"generate_memories,omitempty"`
	UseMemories                    *bool `yaml:"use_memories,omitempty" json:"use_memories,omitempty"`
	DedicatedTools                 *bool `yaml:"dedicated_tools,omitempty" json:"dedicated_tools,omitempty"`
	MaxRawMemoriesForConsolidation *int  `yaml:"max_raw_memories_for_consolidation,omitempty" json:"max_raw_memories_for_consolidation,omitempty"`
	MaxUnusedDays                  *int  `yaml:"max_unused_days,omitempty" json:"max_unused_days,omitempty"`
	MaxRolloutAgeDays              *int  `yaml:"max_rollout_age_days,omitempty" json:"max_rollout_age_days,omitempty"`
	MaxRolloutsPerStartup          *int  `yaml:"max_rollouts_per_startup,omitempty" json:"max_rollouts_per_startup,omitempty"`
	MinRolloutIdleHours            *int  `yaml:"min_rollout_idle_hours,omitempty" json:"min_rollout_idle_hours,omitempty"`
	MinRateLimitRemainingPercent   *int  `yaml:"min_rate_limit_remaining_percent,omitempty" json:"min_rate_limit_remaining_percent,omitempty"`
	// SearchTopK is how many memories one memories_search call returns. The
	// results are ranked and already filtered by relevance, so this is a budget
	// on the model's context rather than a page size — there is no second page.
	// Values above the ceiling are clamped rather than refused.
	SearchTopK         *int   `yaml:"search_top_k,omitempty" json:"search_top_k,omitempty"`
	ExtractModel       string `yaml:"extract_model,omitempty" json:"extract_model,omitempty"`
	ConsolidationModel string `yaml:"consolidation_model,omitempty" json:"consolidation_model,omitempty"`
}

type MemoriesConfig struct {
	DisableOnExternalContext       bool
	GenerateMemories               bool
	UseMemories                    bool
	DedicatedTools                 bool
	MaxRawMemoriesForConsolidation int
	MaxUnusedDays                  int
	MaxRolloutAgeDays              int
	MaxRolloutsPerStartup          int
	MinRolloutIdleHours            int
	MinRateLimitRemainingPercent   int
	SearchTopK                     int
	ExtractModel                   string
	ConsolidationModel             string
}

func (r *Root) EffectiveMemories() MemoriesConfig {
	m := MemorySection{}
	if r != nil {
		m = r.Memories
	}
	return MemoriesConfig{
		DisableOnExternalContext:       boolValue(m.DisableOnExternalContext, false),
		GenerateMemories:               boolValue(m.GenerateMemories, true),
		UseMemories:                    boolValue(m.UseMemories, true),
		DedicatedTools:                 boolValue(m.DedicatedTools, true),
		MaxRawMemoriesForConsolidation: clampInt(intValue(m.MaxRawMemoriesForConsolidation, 256), 1, 4096),
		MaxUnusedDays:                  clampInt(intValue(m.MaxUnusedDays, 30), 0, 365),
		MaxRolloutAgeDays:              clampInt(intValue(m.MaxRolloutAgeDays, 10), 0, 90),
		MaxRolloutsPerStartup:          clampInt(intValue(m.MaxRolloutsPerStartup, 2), 1, 128),
		MinRolloutIdleHours:            clampInt(intValue(m.MinRolloutIdleHours, 6), 1, 48),
		MinRateLimitRemainingPercent:   clampInt(intValue(m.MinRateLimitRemainingPercent, 25), 0, 100),
		SearchTopK:                     clampInt(intValue(m.SearchTopK, memorySearchDefaultTopK), 1, memorySearchMaxTopK),
		ExtractModel:                   m.ExtractModel,
		ConsolidationModel:             m.ConsolidationModel,
	}
}

// BoolPtr is the constructor for optional boolean settings, whose nil value
// means "unset" and selects the setting's default rather than false.
func BoolPtr(v bool) *bool { return &v }

func boolValue(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}

func intValue(value *int, fallback int) int {
	if value == nil {
		return fallback
	}
	return *value
}

func clampInt(value, minimum, maximum int) int {
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}
