package config

import (
	"errors"
	"fmt"
	"math"
	"runtime"
	"strings"
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
}

type AgentLLMProviderConfig struct {
	Provider string           `yaml:"provider,omitempty" json:"provider,omitempty"`
	Model    string           `yaml:"model,omitempty" json:"model,omitempty"`
	APIKey   string           `yaml:"api_key,omitempty" json:"api_key,omitempty"`
	BaseURL  string           `yaml:"base_url,omitempty" json:"base_url,omitempty"`
	APIPath  string           `yaml:"api_path,omitempty" json:"api_path,omitempty"`
	Params   LLMRequestParams `yaml:"params,omitempty" json:"params,omitempty"`
	Models   StringList       `yaml:"-" json:"-"`
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
