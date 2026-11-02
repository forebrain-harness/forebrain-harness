package config

type Gateway struct {
	HTTPAddr string      `yaml:"http_addr,omitempty" json:"http_addr,omitempty"`
	Auth     GatewayAuth `yaml:"auth,omitempty" json:"auth,omitempty"`
}

// GatewayAuth holds the token every control-plane request has to carry. Token
// is the only authentication the gateway has (owner 2026-10-31, plan 206): the
// "no authentication" mode is gone, so an empty token is a misconfiguration
// rather than a way to turn authentication off.
type GatewayAuth struct {
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
	NetworkAccess *bool `yaml:"network_access,omitempty" json:"network_access,omitempty"`
}

// EffectiveNetworkAccess resolves whether sandboxed workspace-write commands
// may reach the network. Unset (nil) defaults to true.
func (s SandboxWorkspaceWrite) EffectiveNetworkAccess() bool {
	return boolValue(s.NetworkAccess, true)
}

// Auto-compaction triggers at a share of the model's effective input window.
// The share is the only tunable; the range is what a stored value is held to.
const (
	MinAutoCompactPercent     = 50
	MaxAutoCompactPercent     = 90
	DefaultAutoCompactPercent = 90
)

type CompactSection struct {
	// Prompt overrides the local compaction handoff prompt.
	Prompt string `yaml:"prompt,omitempty" json:"prompt,omitempty"`
	// AutoCompactPercent is the share of the effective input window at which
	// automatic compaction starts. Unset (0) selects DefaultAutoCompactPercent;
	// a stored value is clamped to MinAutoCompactPercent–MaxAutoCompactPercent.
	AutoCompactPercent int `yaml:"auto_compact_percent,omitempty" json:"auto_compact_percent,omitempty"`
}

// EffectiveAutoCompactPercent is the share of the effective input window at
// which automatic compaction starts: the configured percent, or the default
// when it is unset.
func (c CompactSection) EffectiveAutoCompactPercent() int {
	if c.AutoCompactPercent > 0 {
		return c.AutoCompactPercent
	}
	return DefaultAutoCompactPercent
}

// CronSection configures scheduled tasks for the whole install.
type CronSection struct {
	// RetentionDays is how long a scheduled run's conversation and history
	// record are kept after their last activity. Unset means the default.
	RetentionDays *int `yaml:"retention_days,omitempty" json:"retention_days,omitempty"`
}

const (
	DefaultCronRetentionDays = 30
	MinCronRetentionDays     = 1
	MaxCronRetentionDays     = 3650
)

// CronRetentionDays is the retention in force: the configured value, or the
// default when none is set.
func (r *Root) CronRetentionDays() int {
	if r != nil && r.Cron.RetentionDays != nil {
		return *r.Cron.RetentionDays
	}
	return DefaultCronRetentionDays
}

// PeersSection holds the cross-process peer coordination's one switch. The
// whole section lives in the state database (fb_config) only: forebrain.yaml
// never sets it — a file copy is neither imported nor effective (owner
// 2026-10-30, plan 195 §3.1).
type PeersSection struct {
	// ClaudeCode moves this install's peer registration into Claude Code's
	// session directory and socket namespace, so Claude Code sessions and
	// forebrain can message each other; forebrain's own directory is then
	// left alone. It is shown, and takes effect, only while this machine
	// has Claude Code's session directory. nil or false — the default —
	// keeps peer messaging among forebrain processes only.
	ClaudeCode *bool `yaml:"claude_code,omitempty" json:"claude_code,omitempty"`
}

type Root struct {
	// SourceFiles lists the files this configuration was read from, in read
	// order: the config file itself, then the forebrain home's .env when one was
	// applied. Runtime-only — set by Load, never persisted.
	SourceFiles []string `yaml:"-" json:"-"`
	// ApprovalPreset is the one approval setting a user chooses (plan 203).
	// The approval policy and the sandbox mode below are derived from it by
	// safety.EffectiveConfig and never persisted: writing one back would pin
	// the derivation and the project's trust decision could no longer move it.
	ApprovalPreset ApprovalPresetID `yaml:"approval_preset,omitempty" json:"approval_preset,omitempty"`
	// ApprovalPolicy is the ask-before-acting mode in force. Derived from
	// ApprovalPreset at runtime.
	ApprovalPolicy ApprovalPolicyConfig `yaml:"-" json:"-"`
	// SandboxMode is the sandbox commands run in. Derived from ApprovalPreset
	// at runtime.
	SandboxMode           SandboxMode           `yaml:"-" json:"-"`
	SandboxWorkspaceWrite SandboxWorkspaceWrite `yaml:"sandbox_workspace_write,omitempty" json:"sandbox_workspace_write,omitempty"`
	Compact               CompactSection        `yaml:"compact,omitempty" json:"compact,omitempty"`
	Cron                  CronSection           `yaml:"cron,omitempty" json:"cron,omitempty"`
	Gateway               Gateway               `yaml:"gateway,omitempty" json:"gateway,omitempty"`
	Agents                AgentsSection         `yaml:"agents,omitempty" json:"agents,omitempty"`
	Hooks                 HooksSettings         `yaml:"hooks,omitempty" json:"hooks,omitempty"`
	Memories              MemorySection         `yaml:"memories,omitempty" json:"memories,omitempty"`
	Batch                 BatchSection          `yaml:"batch,omitempty" json:"batch,omitempty"`
	Features              FeaturesSection       `yaml:"features,omitempty" json:"features,omitempty"`
	LSP                   LSPSection            `yaml:"lsp,omitempty" json:"lsp,omitempty"`
	Peers                 PeersSection          `yaml:"peers,omitempty" json:"peers,omitempty"`
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
	Memories   *bool `yaml:"memories,omitempty" json:"memories,omitempty"`
	SkillOffer *bool `yaml:"skill_offer,omitempty" json:"skill_offer,omitempty"`
	LSP        *bool `yaml:"lsp,omitempty" json:"lsp,omitempty"`
}

// EffectiveFeatures returns the resolved boolean values for all feature flags,
// applying fallback defaults when the config does not specify a value.
func (r *Root) EffectiveFeatures() EffectiveFeaturesConfig {
	f := FeaturesSection{}
	if r != nil {
		f = r.Features
	}
	return EffectiveFeaturesConfig{
		Memories:   boolValue(f.Memories, true),
		SkillOffer: boolValue(f.SkillOffer, true),
		LSP:        boolValue(f.LSP, true),
	}
}

// EffectiveFeaturesConfig holds resolved feature flag values with defaults applied.
type EffectiveFeaturesConfig struct {
	Memories   bool
	SkillOffer bool
	LSP        bool
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

// MemoryModelCurrent is the stored memories.extract_model or
// memories.consolidation_model of an install that runs the memory pipeline on
// the conversation's own model — the meaning the empty value had before plan
// 207 started writing the setting into the database. The memory settings page
// shows it as 「当前模型」.
const MemoryModelCurrent = "current"

// MemorySection holds the memory pipeline's budgets and models. Whether memory
// exists at all is features.memories: one switch for reading, writing, and the
// dedicated tools (owner 2026-10-26, plan 205).
type MemorySection struct {
	MaxRawMemoriesForConsolidation *int `yaml:"max_raw_memories_for_consolidation,omitempty" json:"max_raw_memories_for_consolidation,omitempty"`
	MaxUnusedDays                  *int `yaml:"max_unused_days,omitempty" json:"max_unused_days,omitempty"`
	MaxRolloutAgeDays              *int `yaml:"max_rollout_age_days,omitempty" json:"max_rollout_age_days,omitempty"`
	MaxRolloutsPerStartup          *int `yaml:"max_rollouts_per_startup,omitempty" json:"max_rollouts_per_startup,omitempty"`
	MinRolloutIdleHours            *int `yaml:"min_rollout_idle_hours,omitempty" json:"min_rollout_idle_hours,omitempty"`
	// SearchTopK is how many memories one memories_search call returns. The
	// results are ranked and already filtered by relevance, so this is a budget
	// on the model's context rather than a page size — there is no second page.
	// Values above the ceiling are clamped rather than refused.
	SearchTopK *int `yaml:"search_top_k,omitempty" json:"search_top_k,omitempty"`
	// ExtractModel and ConsolidationModel name the models the two memory
	// stages run on: current (the conversation's own model), or a model name
	// from the agent's list. Empty is the same as current and is only what a
	// configuration written before plan 207 still carries.
	ExtractModel       string `yaml:"extract_model,omitempty" json:"extract_model,omitempty"`
	ConsolidationModel string `yaml:"consolidation_model,omitempty" json:"consolidation_model,omitempty"`
}

type MemoriesConfig struct {
	MaxRawMemoriesForConsolidation int
	MaxUnusedDays                  int
	MaxRolloutAgeDays              int
	MaxRolloutsPerStartup          int
	MinRolloutIdleHours            int
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
		MaxRawMemoriesForConsolidation: clampInt(intValue(m.MaxRawMemoriesForConsolidation, 256), 1, 4096),
		MaxUnusedDays:                  clampInt(intValue(m.MaxUnusedDays, 30), 0, 365),
		MaxRolloutAgeDays:              clampInt(intValue(m.MaxRolloutAgeDays, 10), 0, 90),
		MaxRolloutsPerStartup:          clampInt(intValue(m.MaxRolloutsPerStartup, 2), 1, 128),
		MinRolloutIdleHours:            clampInt(intValue(m.MinRolloutIdleHours, 6), 1, 48),
		SearchTopK:                     clampInt(intValue(m.SearchTopK, memorySearchDefaultTopK), 1, memorySearchMaxTopK),
		ExtractModel:                   m.ExtractModel,
		ConsolidationModel:             m.ConsolidationModel,
	}
}

// BatchSection holds what a batch project starts with (plans/216). A project
// copies the value here when it is created, or when its type is changed to
// batch; changing it afterwards reaches new projects only, because a project
// owns its own switch.
type BatchSection struct {
	// SandboxDefault is the value a new batch project's "enable sandbox"
	// switch starts at, and with it whether its tasks run inside the task
	// boundary at all. Unset means the default (true): a batch task is
	// unattended, so it starts inside the sandbox and somebody has to decide
	// to open it up. Only meaningful where a sandbox exists — a machine with
	// none runs every batch task with full access whatever this says.
	SandboxDefault *bool `yaml:"sandbox_default,omitempty" json:"sandbox_default,omitempty"`
}

// EffectiveSandboxDefault is what a new batch project's sandbox switch starts
// at: the configured value, or the default when none is set.
func (b BatchSection) EffectiveSandboxDefault() bool {
	return boolValue(b.SandboxDefault, true)
}

// BoolPtr is the constructor for optional boolean settings, whose nil value
// means "unset" and selects the setting's default rather than false.
func BoolPtr(v bool) *bool { return &v }

// IntPtr is the constructor for optional integer settings, whose nil value
// means "unset" and selects the setting's default rather than zero.
func IntPtr(v int) *int { return &v }

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
