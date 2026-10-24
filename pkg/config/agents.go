package config

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"runtime"
	"strings"
	"time"
)

type AgentsSection struct {
	Defaults    AgentDefaults              `yaml:"defaults,omitempty" json:"defaults,omitempty"`
	Definitions map[string]AgentDefinition `yaml:"definitions,omitempty" json:"definitions,omitempty"`
}

// MCPServersFor returns the MCP servers one primary agent owns, in
// configuration order. An unknown agent owns none: callers fall back to the
// shared list rather than the agent's own, which is what "this agent has no
// entries of its own" means everywhere else.
func (a AgentsSection) MCPServersFor(agentID string) []MCPServerConfig {
	// Definition keys are validated as lower-case ids (primaryAgentIDPattern),
	// so lowering the caller's id is the whole of the case-insensitivity here.
	def, ok := a.Definitions[strings.ToLower(strings.TrimSpace(agentID))]
	if !ok {
		return nil
	}
	return def.MCPServers
}

type AgentDefinition struct {
	Primary     bool   `yaml:"primary" json:"primary"`
	DisplayName string `yaml:"display_name,omitempty" json:"display_name,omitempty"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	// MCPServers are the MCP servers this primary agent owns. They are added
	// to the shared list (agents.defaults.mcp_servers) rather than replacing
	// it — an agent without its own entries uses every shared one — and a
	// same-named entry here replaces the shared one whole.
	MCPServers   []MCPServerConfig        `yaml:"mcp_servers,omitempty" json:"mcp_servers,omitempty"`
	LLMProviders []AgentLLMProviderConfig `yaml:"llm_providers,omitempty" json:"llm_providers,omitempty"`
	// DefaultModel is "provider/model" — this agent's default model. Empty
	// (or absent) means the first llm_providers entry. It must name an entry
	// on this agent's list; the loader and Save both reject a value nothing
	// resolves to. The list's order is pure storage: nothing reorders it.
	DefaultModel string `yaml:"default_model,omitempty" json:"default_model,omitempty"`
	// EnableSubagent is this primary agent's own subagent switch. Absent
	// follows agents.defaults.enable_subagent; set, it decides on its own
	// whether the model of this agent sees the subagent_* tools.
	EnableSubagent *bool `yaml:"enable_subagent,omitempty" json:"enable_subagent,omitempty"`
	// Subagents are the subagent types of this primary agent: entries naming a
	// public built-in override it for this agent, any other name declares a
	// custom subagent. See SubagentConfig.
	Subagents []SubagentConfig `yaml:"subagents,omitempty" json:"subagents,omitempty"`
}

// SubagentConfig is one entry of a primary agent's subagent configuration.
// Name a built-in the model may dispatch (general-purpose, explore, plan,
// verification) and the entry overrides that built-in for this agent: only
// Model, ReasoningEffort, SystemPrompt and Enabled apply. Any other name is a
// custom subagent of this agent.
type SubagentConfig struct {
	Name string `yaml:"name" json:"name"`
	// Description and SystemPrompt are a custom subagent's own text: the model
	// reads the description when choosing a type, and the prompt replaces the
	// built-in one whole.
	Description  string `yaml:"description,omitempty" json:"description,omitempty"`
	SystemPrompt string `yaml:"system_prompt,omitempty" json:"system_prompt,omitempty"`
	// Model is "provider/model" from this agent's own model list; empty follows
	// the conversation.
	Model string `yaml:"model,omitempty" json:"model,omitempty"`
	// ReasoningEffort applies only with Model set.
	ReasoningEffort string `yaml:"reasoning_effort,omitempty" json:"reasoning_effort,omitempty"`
	// Tools is read_only | edit | pick (custom only; empty means read_only).
	Tools string `yaml:"tools,omitempty" json:"tools,omitempty"`
	// AllowedTools is the chosen tool set for Tools: pick.
	AllowedTools []string `yaml:"allowed_tools,omitempty" json:"allowed_tools,omitempty"`
	// Continuable false makes a custom subagent one-shot; nil means continuable.
	Continuable *bool `yaml:"continuable,omitempty" json:"continuable,omitempty"`
	// Enabled false keeps the model from dispatching it; nil means enabled.
	Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
}

// SubagentConfig.Tools values. A built-in's tool set is fixed by its type, so
// these apply to custom subagents only.
const (
	SubagentToolsReadOnly = "read_only"
	SubagentToolsEdit     = "edit"
	SubagentToolsPick     = "pick"
)

type AgentLLMProviderConfig struct {
	Provider string           `yaml:"provider,omitempty" json:"provider,omitempty"`
	Model    string           `yaml:"model,omitempty" json:"model,omitempty"`
	APIKey   string           `yaml:"api_key,omitempty" json:"api_key,omitempty"`
	BaseURL  string           `yaml:"base_url,omitempty" json:"base_url,omitempty"`
	APIPath  string           `yaml:"api_path,omitempty" json:"api_path,omitempty"`
	Params   LLMRequestParams `yaml:"params,omitempty" json:"params,omitempty"`
	Models   StringList       `yaml:"-" json:"-"`
	// APIKeyCleared is a write-path marker, never configuration: the caller
	// removed this entry's api_key on purpose — /connect configuring the
	// provider from a login, where a key on the entry would outrank it — so
	// Save must not restore the value still on disk. Runtime-only; it
	// disappears with the next load.
	APIKeyCleared bool `yaml:"-" json:"-"`
}

type AgentDefaults struct {
	ContextInject ContextInjectConfig `yaml:"context_inject,omitempty" json:"context_inject,omitempty"`
	Execution     ExecutionConfig     `yaml:"execution,omitempty" json:"execution,omitempty"`
	MCPServers    []MCPServerConfig   `yaml:"mcp_servers,omitempty" json:"mcp_servers,omitempty"`

	Guardrails     GuardrailsConfig    `yaml:"guardrails,omitempty" json:"guardrails,omitempty"`
	TokenEstimate  TokenEstimateConfig `yaml:"token_estimate,omitempty" json:"token_estimate,omitempty"`
	WebSearch      WebSearchConfig     `yaml:"web_search,omitempty" json:"web_search"`
	WebFetch       WebFetchConfig      `yaml:"web_fetch,omitempty" json:"web_fetch"`
	EnableSubagent *bool               `yaml:"enable_subagent,omitempty" json:"enable_subagent,omitempty"`
	// NetworkRetryMax caps LLM transport-failure retries (default 10). Nil
	// means the default; 0 disables network retries; load rejects negative
	// values; values above MaxNetworkRetryMax run at MaxNetworkRetryMax.
	NetworkRetryMax *int `yaml:"network_retry_max,omitempty" json:"network_retry_max,omitempty"`
	// StreamIdleTimeoutSeconds bounds how long a streaming LLM request may
	// stay silent — no response headers, or no new data — before it is
	// treated as a dropped connection and handed to the network retry layer.
	// Nil means the default 300; 0 disables the watchdog; load rejects
	// negative values; other values clamp to the [10, 1800] range.
	StreamIdleTimeoutSeconds *int `yaml:"stream_idle_timeout_seconds,omitempty" json:"stream_idle_timeout_seconds,omitempty"`
}

// DefaultNetworkRetryMax is the network-retry cap when the configuration
// leaves network_retry_max unset. It lives here, next to the field it
// defaults, so no other package restates it.
const DefaultNetworkRetryMax = 10

// MaxNetworkRetryMax is the ceiling a configured network_retry_max runs at:
// values above it run at it. The retry waits double up to 32s, so a larger
// cap would only stretch the same ceiling across more half-minute waits.
const MaxNetworkRetryMax = 15

// NetworkRetryMaxOrDefault resolves the network-retry cap: nil → the default;
// 0 disables; set values pass through as-is unless they exceed
// MaxNetworkRetryMax, which they run at instead (the loader already rejected
// negative ones, so there is nothing else to clamp here).
func (d AgentDefaults) NetworkRetryMaxOrDefault() int {
	if d.NetworkRetryMax == nil {
		return DefaultNetworkRetryMax
	}
	return min(*d.NetworkRetryMax, MaxNetworkRetryMax)
}

// DefaultStreamIdleTimeoutSeconds is the streaming silence window when the
// configuration leaves stream_idle_timeout_seconds unset.
const DefaultStreamIdleTimeoutSeconds = 300

// MinStreamIdleTimeoutSeconds and MaxStreamIdleTimeoutSeconds bound a
// configured stream_idle_timeout_seconds: shorter than the minimum would kill
// reasoning models mid-thought, longer than the maximum re-introduces the
// stuck-turn the watchdog exists to end. Configured values outside the range
// run at its edge, not as written.
const (
	MinStreamIdleTimeoutSeconds = 10
	MaxStreamIdleTimeoutSeconds = 1800
)

// StreamIdleTimeout resolves the streaming silence window: nil → the default;
// 0 disables the watchdog; other values clamp to the [Min, Max] range (the
// loader already rejected negative ones, so there is nothing else to exclude).
func (d AgentDefaults) StreamIdleTimeout() time.Duration {
	if d.StreamIdleTimeoutSeconds == nil {
		return DefaultStreamIdleTimeoutSeconds * time.Second
	}
	v := *d.StreamIdleTimeoutSeconds
	if v == 0 {
		return 0
	}
	return time.Duration(min(max(v, MinStreamIdleTimeoutSeconds), MaxStreamIdleTimeoutSeconds)) * time.Second
}

// ExecutionConfig controls coordinated execution capacity for agent mode.
type ExecutionConfig struct {
	MaxParallelSubagents *int `yaml:"max_parallel_subagents,omitempty" json:"max_parallel_subagents,omitempty"`
}

// DefaultMaxParallelSubagents returns max(0, logical CPU cores - 1).
// SubagentsEnabled reports whether the model may spawn subagents. It is opt-in
// (agents.defaults.enable_subagent, absent means off) and governs the
// subagent_* tools the model sees. Features the user invokes directly, such as
// asking another model to review a plan from the exit-plan approval overlay,
// are not subject to it even though they run on the subagent machinery.
func SubagentsEnabled(cfg *Root) bool {
	return cfg != nil && cfg.Agents.Defaults.EnableSubagent != nil && *cfg.Agents.Defaults.EnableSubagent
}

// SubagentsEnabledFor reports whether one primary agent's model may spawn
// subagents. The agent's own switch wins when it is set; an agent that never
// touched it follows the global one.
func SubagentsEnabledFor(cfg *Root, agentName string) bool {
	if cfg != nil {
		if def, ok := cfg.Agents.Definitions[strings.ToLower(strings.TrimSpace(agentName))]; ok && def.EnableSubagent != nil {
			return *def.EnableSubagent
		}
	}
	return SubagentsEnabled(cfg)
}

// subagentConfigNamePattern is the name a subagent entry may carry. Names are
// what the model passes as subagent_type, so they are lower-case and never
// collide with the built-in spelling.
var subagentConfigNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,62}$`)

// ValidateAgentSubagents checks one primary agent's subagent entries.
// publicBuiltins are the built-in types an entry of that name overrides;
// systemBuiltins are the types the system dispatches, which a configuration
// must not touch. Names are passed in rather than imported so the loader can
// report the rule without depending on the package that defines the
// definitions.
//
// The model a subagent entry names is deliberately not checked against the
// agent's model list: deleting a provider must not make an existing
// configuration unloadable, and dispatch reports the unknown model when the
// subagent is actually started.
func ValidateAgentSubagents(agentID string, def AgentDefinition, publicBuiltins, systemBuiltins []string) error {
	public := make(map[string]struct{}, len(publicBuiltins))
	for _, name := range publicBuiltins {
		public[name] = struct{}{}
	}
	system := make(map[string]struct{}, len(systemBuiltins))
	for _, name := range systemBuiltins {
		system[name] = struct{}{}
	}
	seen := make(map[string]struct{}, len(def.Subagents))
	for i, entry := range def.Subagents {
		where := fmt.Sprintf("agents.definitions.%s.subagents[%d]", agentID, i)
		name := strings.ToLower(strings.TrimSpace(entry.Name))
		if !subagentConfigNamePattern.MatchString(name) {
			return fmt.Errorf("%s: invalid name %q: use lower-case letters, digits and dashes, starting with a letter", where, entry.Name)
		}
		if _, dup := seen[name]; dup {
			return fmt.Errorf("%s: duplicate subagent name %q", where, name)
		}
		seen[name] = struct{}{}
		if _, ok := system[name]; ok {
			return fmt.Errorf("%s: %q is dispatched by the system and cannot be configured", where, name)
		}
		if _, ok := public[name]; ok {
			if strings.TrimSpace(entry.Description) != "" || strings.TrimSpace(entry.Tools) != "" || len(entry.AllowedTools) > 0 || entry.Continuable != nil {
				return fmt.Errorf("%s: %q is a built-in: it takes a model, a reasoning effort, a system prompt and enabled, nothing else", where, name)
			}
		} else {
			if strings.TrimSpace(entry.Description) == "" {
				return fmt.Errorf("%s: description required", where)
			}
			if strings.TrimSpace(entry.SystemPrompt) == "" {
				return fmt.Errorf("%s: system_prompt required", where)
			}
			switch tools := strings.TrimSpace(entry.Tools); tools {
			case "", SubagentToolsReadOnly, SubagentToolsEdit, SubagentToolsPick:
			default:
				return fmt.Errorf("%s: tools %q must be %s, %s or %s", where, entry.Tools, SubagentToolsReadOnly, SubagentToolsEdit, SubagentToolsPick)
			}
			if strings.TrimSpace(entry.Tools) == SubagentToolsPick {
				if len(entry.AllowedTools) == 0 {
					return fmt.Errorf("%s: allowed_tools requires at least one tool with tools: %s", where, SubagentToolsPick)
				}
				for _, allowed := range entry.AllowedTools {
					if strings.HasPrefix(strings.ToLower(strings.TrimSpace(allowed)), "subagent_") {
						return fmt.Errorf("%s: allowed_tools cannot contain %q: a subagent cannot dispatch subagents", where, allowed)
					}
				}
			}
		}
		if err := validateSubagentModel(where, entry); err != nil {
			return err
		}
	}
	return nil
}

// validateSubagentModel enforces the "provider/model" shape and the rule that
// a reasoning effort only means something next to an explicit model.
func validateSubagentModel(where string, entry SubagentConfig) error {
	model := strings.TrimSpace(entry.Model)
	if model == "" {
		if strings.TrimSpace(entry.ReasoningEffort) != "" {
			return fmt.Errorf("%s: reasoning_effort requires model", where)
		}
		return nil
	}
	provider, name, found := strings.Cut(model, "/")
	if !found || strings.TrimSpace(provider) == "" || strings.TrimSpace(name) == "" || strings.Contains(name, "/") {
		return fmt.Errorf("%s: model %q must be provider/model", where, entry.Model)
	}
	return nil
}

func DefaultMaxParallelSubagents() int {
	n := runtime.NumCPU() - 1
	if n < 0 {
		return 0
	}
	return n
}

// MaxParallelSubagentsValue returns the effective subagent capacity.
// When nil (omitted), it uses the dynamic default. Negative values clamp to 0.
func (c ExecutionConfig) MaxParallelSubagentsValue() int {
	if c.MaxParallelSubagents == nil {
		return DefaultMaxParallelSubagents()
	}
	if *c.MaxParallelSubagents < 0 {
		return 0
	}
	return *c.MaxParallelSubagents
}

type MCPOAuthConfig struct {
	Mode             string   `yaml:"mode,omitempty" json:"mode,omitempty"`
	AccessToken      string   `yaml:"access_token,omitempty" json:"access_token,omitempty"`
	TokenType        string   `yaml:"token_type,omitempty" json:"token_type,omitempty"`
	ClientID         string   `yaml:"client_id,omitempty" json:"client_id,omitempty"`
	ClientSecret     string   `yaml:"client_secret,omitempty" json:"client_secret,omitempty"`
	TokenURL         string   `yaml:"token_url,omitempty" json:"token_url,omitempty"`
	AuthorizationURL string   `yaml:"authorization_url,omitempty" json:"authorization_url,omitempty"`
	RedirectURL      string   `yaml:"redirect_url,omitempty" json:"redirect_url,omitempty"`
	RefreshToken     string   `yaml:"refresh_token,omitempty" json:"refresh_token,omitempty"`
	Scopes           []string `yaml:"scopes,omitempty" json:"scopes,omitempty"`
}

type MCPToolApprovalMode string

const (
	MCPToolApprovalAuto    MCPToolApprovalMode = "auto"
	MCPToolApprovalPrompt  MCPToolApprovalMode = "prompt"
	MCPToolApprovalWrites  MCPToolApprovalMode = "writes"
	MCPToolApprovalApprove MCPToolApprovalMode = "approve"
)

func (m MCPToolApprovalMode) Normalized() MCPToolApprovalMode {
	switch MCPToolApprovalMode(strings.ToLower(strings.TrimSpace(string(m)))) {
	case MCPToolApprovalPrompt:
		return MCPToolApprovalPrompt
	case MCPToolApprovalWrites:
		return MCPToolApprovalWrites
	case MCPToolApprovalApprove:
		return MCPToolApprovalApprove
	default:
		return MCPToolApprovalAuto
	}
}

func (m MCPToolApprovalMode) Valid() bool {
	switch MCPToolApprovalMode(strings.ToLower(strings.TrimSpace(string(m)))) {
	case "", MCPToolApprovalAuto, MCPToolApprovalPrompt, MCPToolApprovalWrites, MCPToolApprovalApprove:
		return true
	default:
		return false
	}
}

type MCPServerToolConfig struct {
	ApprovalMode MCPToolApprovalMode `yaml:"approval_mode,omitempty" json:"approval_mode,omitempty"`
}

type MCPServerConfig struct {
	Name                     string                         `yaml:"name,omitempty" json:"name,omitempty"`
	Transport                string                         `yaml:"transport,omitempty" json:"transport,omitempty"`
	URL                      string                         `yaml:"url,omitempty" json:"url,omitempty"`
	Headers                  map[string]string              `yaml:"headers,omitempty" json:"headers,omitempty"`
	OAuth                    MCPOAuthConfig                 `yaml:"oauth,omitempty" json:"oauth,omitempty"`
	DisableStandaloneSSE     *bool                          `yaml:"disable_standalone_sse,omitempty" json:"disable_standalone_sse,omitempty"`
	Command                  string                         `yaml:"command,omitempty" json:"command,omitempty"`
	Args                     []string                       `yaml:"args,omitempty" json:"args,omitempty"`
	Env                      map[string]string              `yaml:"env,omitempty" json:"env,omitempty"`
	InheritParentEnv         *bool                          `yaml:"inherit_parent_env,omitempty" json:"inherit_parent_env,omitempty"`
	ExpectedCommandSHA256    string                         `yaml:"expected_command_sha256,omitempty" json:"expected_command_sha256,omitempty"`
	DefaultToolsApprovalMode MCPToolApprovalMode            `yaml:"default_tools_approval_mode,omitempty" json:"default_tools_approval_mode,omitempty"`
	Tools                    map[string]MCPServerToolConfig `yaml:"tools,omitempty" json:"tools,omitempty"`

	// StartupTimeout bounds how long this server may take to start and list its
	// tools, in seconds. Zero means the runtime default. It is deliberately not
	// part of ServerFingerprint: that hash is the identity a project MCP
	// consent was granted for, and a local patience dial must not read as "this
	// is a different server now, ask again".
	StartupTimeout float64 `yaml:"startup_timeout,omitempty" json:"startup_timeout,omitempty"`
	// RequestTimeout bounds one tool call against this server, in seconds:
	// the answer to a server that accepts a request and then never replies.
	// Zero means no bound — the call runs for as long as its caller allows,
	// which is what every entry does today. Like StartupTimeout it is not part
	// of ServerFingerprint; it is local patience, not identity.
	RequestTimeout float64 `yaml:"request_timeout,omitempty" json:"request_timeout,omitempty"`
	// Enabled is this entry's own switch, for a server a user wants to keep
	// described but out of every new session. Absent means enabled; false
	// keeps the entry in the configuration, out of the effective list, and
	// visible in the panel with the reason. It is not part of
	// ServerFingerprint for the same reason StartupTimeout is not: toggling a
	// server off must not read as "this is a different server, ask again".
	Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	// Required marks a server whose tools a run cannot do without. A required
	// server's failure is a hard failure for a non-interactive run, and an
	// interactive one may not skip it while it waits for the rest to settle.
	Required bool `yaml:"required,omitempty" json:"required,omitempty"`

	// Scope, ProjectKey and AgentKey are runtime stamps, never configuration.
	// They record which source produced this effective entry ("global" from
	// forebrain.yaml's shared list, "agent" from one primary agent's own list,
	// "project" from a project-level file), which project a project entry
	// belongs to, and which primary agent an agent entry belongs to, so
	// credential storage, disable keys and approval clamping can tell the
	// three apart. Serialization ignores them for the same reason
	// AgentLLMProviderConfig.Models does: a runtime stamp that round-tripped
	// through yaml would leak into files users edit by hand. Callers must set
	// them on a deep copy, never on a config struct shared with AppCfg.
	Scope      string `yaml:"-" json:"-"`
	ProjectKey string `yaml:"-" json:"-"`
	AgentKey   string `yaml:"-" json:"-"`
}

// MCPServerScope values for MCPServerConfig.Scope.
const (
	MCPServerScopeGlobal  = "global"
	MCPServerScopeAgent   = "agent"
	MCPServerScopeProject = "project"
)

// ServerEnabled reports whether this entry is switched on in the
// configuration. Absent means yes: a configuration written before the switch
// existed describes servers that all run.
func ServerEnabled(srv MCPServerConfig) bool {
	return srv.Enabled == nil || *srv.Enabled
}

// MaxMCPTimeoutSeconds is the largest timeout in seconds the runtime can
// represent as a duration (about 31 years). Anything above it would overflow
// time.Duration, so it is rejected at load instead of silently wrapping into a
// negative deadline that expires immediately.
const MaxMCPTimeoutSeconds = 1e9

// ValidateMCPTimeout rejects a timeout in seconds the runtime cannot honor.
// Startup and per-call bounds share one rule because they share one
// conversion: both become a time.Duration. It lives beside the fields it
// validates so the rule has one definition, and the runtime's resolvers call
// it rather than repeating it.
func ValidateMCPTimeout(seconds float64) error {
	switch {
	case math.IsNaN(seconds):
		return errors.New("must be a number of seconds")
	case math.IsInf(seconds, 0):
		return errors.New("must be a finite number of seconds")
	case seconds < 0:
		return errors.New("must not be negative")
	case seconds > MaxMCPTimeoutSeconds:
		return fmt.Errorf("must not exceed %g seconds", float64(MaxMCPTimeoutSeconds))
	}
	return nil
}

func (c MCPServerConfig) ToolApprovalMode(toolName string) MCPToolApprovalMode {
	if tool, ok := c.Tools[strings.TrimSpace(toolName)]; ok && strings.TrimSpace(string(tool.ApprovalMode)) != "" {
		return tool.ApprovalMode.Normalized()
	}
	return c.DefaultToolsApprovalMode.Normalized()
}
