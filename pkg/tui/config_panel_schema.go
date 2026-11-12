// The /config panel's section table (plan 190 §3.1): which configuration
// sections the panel lists, how each one reads in Chinese, and which widget
// each field gets. Pure data — no rendering, no session, no imports — so the
// coverage test can hold it against config.Root and against the store's
// section list without a terminal or a database.
//
// This table is the TUI side's one "add a new section, remember to change
// here" list: TestConfigPanelSchemaCoversEveryRootSection fails the moment a
// Root section has no descriptor, which is what keeps a new section from
// reaching the panel's users as nothing at all.
package tui

import "strings"

// configFieldKind is the widget a field is edited with.
type configFieldKind int

const (
	// configFieldBool3 is an optional boolean, three states: on, off, and the
	// setting's own default.
	// The third state is not decoration — nil is what selects the setting's
	// default (see config.BoolPtr), so a two-state switch would silently
	// turn "unset" into an explicit choice.
	configFieldBool3 configFieldKind = iota
	// configFieldEnum is one of a fixed set the validator accepts; the
	// picker shows the section's own option list, so no free text can reach
	// the store with a value the loader rejects.
	configFieldEnum
	// configFieldNumber is an integer with the bounds the loader applies.
	configFieldNumber
	// configFieldText is free text: a model name, a path, a command.
	configFieldText
	// configFieldRows is a table or list — a map of profiles, of MCP
	// servers, of hook events — edited as rows: open one to edit its own
	// fields, add a row, delete a row.
	configFieldRows
)

// configField describes one editable field of one section. Path is the
// field's place inside the section's own JSON (dot-separated); the empty path
// means the section's value itself, which is how a section that is a bare
// string — approval_preset — carries its one setting.
type configField struct {
	Path    string
	Label   string
	Kind    configFieldKind
	Options []string // configFieldEnum: the values the loader accepts
	// OptionLabels names an enum option in Chinese on the picker's rows. The
	// stored value stays the one the loader accepts; only the wording changes,
	// so a reader of this panel chooses a behavior rather than an english
	// configuration key (plan 203 §0.8).
	OptionLabels map[string]string
	// DefaultValue is the option to preselect when the field has no value yet,
	// for a setting whose empty value means one of its own options.
	DefaultValue string
	Min          int // configFieldNumber: the smallest accepted value
	Max          int // configFieldNumber: the largest accepted value, 0 = none
	// Clamp makes an over-Max value land on Max instead of being refused,
	// the way the loader treats a limit it can simply honour: the panel
	// shows the clamped value it stored, never a number that did not land.
	Clamp bool
	// List marks a configFieldRows field whose rows are list elements rather
	// than a map's keys — writable_roots is a list, hooks a map. An unset
	// field has no value to read the shape from, so the descriptor carries
	// it.
	List bool
	// ClaudeCodeOnly hides the field unless this machine has Claude Code
	// installed: the interop switch has nothing to act on without it (owner
	// 2026-10-30). A hidden field's stored value stays as it is; the panel
	// writes it back untouched when it saves the section.
	ClaudeCodeOnly bool
	// Hint is the one line the row carries under its value: what the field
	// means, and what leaving it unset does.
	Hint string
}

// configSectionSchema is one section as the panel presents it.
type configSectionSchema struct {
	// Name is the section name a reader sees in forebrain.yaml and in
	// fb_config.
	Name string
	// Title is the name in the picker, in English.
	Title string
	// Summary is the one-line "what this is for" the picker shows beside it.
	Summary string
	// Tab groups the sections the picker lists, in the tabs' own order.
	Tab string
	// YAMLOnly marks a section the panel shows but never writes: it is a
	// startup section of forebrain.yaml, and it takes effect when the
	// process restarts (plan 187 §1.1).
	YAMLOnly bool
	// Fields are the editable settings, in the order the section page shows
	// them. A YAML-only section has none.
	Fields []configField
}

// The picker's tabs. A terminal is wide enough for four short headings, and
// four is what keeps the 11 stored sections reachable without a filter.
const (
	configTabApproval = "Approval & sandbox"
	configTabSession  = "Session & memory"
	configTabAgents   = "Agents & tools"
	configTabFile     = "File only"
)

// configSectionSchemas lists every section of the runtime configuration: the
// 11 the state database stores, then the two forebrain.yaml keeps. The order
// inside a tab is the order a user tunes these settings in, roughly: the
// switch that decides everything else first.
var configSectionSchemas = []configSectionSchema{
	{
		Name: "approval_preset", Title: "Approval preset", Tab: configTabApproval,
		Summary: "Default approval mode for new sessions",
		Fields: []configField{{
			Label: "Approval preset", Kind: configFieldEnum,
			Options:      []string{"workspace_write", "full_access"},
			OptionLabels: map[string]string{"workspace_write": "Default", "full_access": "Full access"},
			DefaultValue: "workspace_write",
			Hint:         "Default: reads and writes inside the workspace proceed, anything outside it asks first; full access: nothing asks unless you refuse it. A change here affects new sessions only.",
		}},
	},
	{
		Name: "sandbox_workspace_write", Title: "Sandbox writable roots", Tab: configTabApproval,
		Summary: "Write access and caching for the workspace-write sandbox",
		Fields: []configField{
			{Path: "writable_roots", Label: "Extra writable roots", Kind: configFieldRows, List: true, Hint: "One absolute path per row"},
			{Path: "network_access", Label: "Allow network access", Kind: configFieldBool3, Hint: "On by default"},
		},
	},
	{
		Name: "compact", Title: "Context compaction", Tab: configTabSession,
		Summary: "Auto-compaction threshold and prompt",
		Fields: []configField{
			{Path: "prompt", Label: "Hand-off prompt", Kind: configFieldText, Hint: "Empty uses the built-in prompt"},
			{
				Path: "auto_compact_percent", Label: "Auto-compact threshold (%)", Kind: configFieldNumber,
				Min: 50, Max: 90, Clamp: true,
				Hint: "Default 90; compacts when the window reaches this percentage — range 50–90, past 90 is stored as 90",
			},
		},
	},
	{
		Name: "batch", Title: "Batch projects", Tab: configTabApproval,
		Summary: "Sandbox a new batch project starts with",
		Fields: []configField{{
			Path: "sandbox_default", Label: "Enable sandbox", Kind: configFieldBool3,
			Hint: "On by default; a new batch project starts from this value and manages its own afterwards",
		}},
	},
	{
		Name: "memories", Title: "Memory", Tab: configTabSession,
		Summary: "Memory budgets and models; the switch lives under Feature switches → Memory",
		Fields: []configField{
			{Path: "search_top_k", Label: "Results per search", Kind: configFieldNumber, Min: 1, Max: 20, Clamp: true, Hint: "Default 5; past 20 is stored as 20"},
			{Path: "max_raw_memories_for_consolidation", Label: "Raw memories per consolidation", Kind: configFieldNumber, Min: 1, Max: 4096, Clamp: true, Hint: "Default 256"},
			{Path: "max_unused_days", Label: "Days before an unused memory expires", Kind: configFieldNumber, Min: 0, Max: 365, Clamp: true, Hint: "Default 30"},
			{Path: "max_rollout_age_days", Label: "Days a record is kept", Kind: configFieldNumber, Min: 0, Max: 90, Clamp: true, Hint: "Default 10"},
			{Path: "max_rollouts_per_startup", Label: "Records processed per startup", Kind: configFieldNumber, Min: 1, Max: 128, Clamp: true, Hint: "Default 2"},
			{Path: "min_rollout_idle_hours", Label: "Idle hours before a record is processed", Kind: configFieldNumber, Min: 1, Max: 48, Clamp: true, Hint: "Default 6"},
			// The stored value is a model name or the explicit "current" (the
			// conversation's own model, plan 207 §3.3): the database always
			// holds one, so the row shows what is in force instead of a
			// "leave it empty" placeholder.
			{Path: "extract_model", Label: "Extraction model", Kind: configFieldText, Hint: "current = the conversation's own model; a model name also works"},
			{Path: "consolidation_model", Label: "Consolidation model", Kind: configFieldText, Hint: "current = the conversation's own model; a model name also works"},
		},
	},
	{
		Name: "cron", Title: "Scheduled tasks", Tab: configTabSession,
		Summary: "How long a scheduled task's records are kept",
		Fields: []configField{{
			Path: "retention_days", Label: "Keep days", Kind: configFieldNumber,
			Min: 1, Max: 3650, Hint: "Default 30; range 1–3650",
		}},
	},
	{
		Name: "features", Title: "Feature switches", Tab: configTabSession,
		Summary: "Feature switches",
		Fields: []configField{
			{Path: "memories", Label: "Memory", Kind: configFieldBool3, Hint: "On by default"},
			{Path: "skill_offer", Label: "Skill suggestions", Kind: configFieldBool3, Hint: "On by default"},
			{Path: "lsp", Label: "Language servers", Kind: configFieldBool3, Hint: "On by default"},
		},
	},
	{
		Name: "agents", Title: "Agents", Tab: configTabAgents,
		Summary: "Defaults, agent definitions and their providers, subagents, MCP",
		Fields: []configField{
			{
				Path: "definitions", Label: "Agent definitions", Kind: configFieldRows,
				Hint: "One agent id per row; a row holds its providers, subagents, MCP and default model",
			},
			{
				Path: "defaults.network_retry_max", Label: "Network retry limit", Kind: configFieldNumber,
				Min: 0, Max: 15, Clamp: true, Hint: "Default 10; 0 turns retries off, past 15 runs as 15",
			},
			{
				Path: "defaults.stream_idle_timeout_seconds", Label: "Stream idle timeout (s)", Kind: configFieldNumber,
				Min: 0, Max: 1800, Clamp: true, Hint: "Default 300; 0 turns the watchdog off",
			},
			{Path: "defaults.enable_subagent", Label: "Enable subagents", Kind: configFieldBool3, Hint: "Default: the built-in setting"},
			{
				Path: "defaults.context_inject.warn_remaining_percent", Label: "Warn remaining (%)", Kind: configFieldNumber,
				Min: 1, Max: 50, Clamp: true,
				Hint: "Default 12; warns when the remaining window falls below this percentage, range 1–50",
			},
			{Path: "defaults.mcp_servers", Label: "Shared MCP servers", Kind: configFieldRows, Hint: "One server per row"},
			// The /goal check's global row (plan 299) is two settings, not a
			// whole subagent entry: it is dispatched by the system, so it has
			// no name, description, prompt or tool policy to edit here, and a
			// rows descriptor on the value would offer all of those.
			{
				Path: "goal_evaluator.model", Label: "Goal check model", Kind: configFieldText,
				Hint: "A model row id (the row chosen in Settings → Subagents); empty follows the primary agent",
			},
			{
				Path: "goal_evaluator.reasoning_effort", Label: "Goal check thinking level", Kind: configFieldText,
				Hint: "Empty follows that model row's default level; low, high and so on override it",
			},
		},
	},
	{
		Name: "hooks", Title: "Hooks", Tab: configTabAgents,
		Summary: "Event hooks (command / prompt)",
		Fields: []configField{{
			Label: "Hook events", Kind: configFieldRows,
			Hint: "One event name per row (e.g. pre_tool_use); a row holds its matcher list",
		}},
	},
	{
		Name: "lsp", Title: "Language servers", Tab: configTabAgents,
		Summary: "Language server switches, limits and diagnostics",
		Fields: []configField{
			{Path: "recommendations", Label: "Recommendations", Kind: configFieldBool3, Hint: "On by default"},
			{Path: "servers", Label: "Servers", Kind: configFieldRows, Hint: "One server id per row; a row holds command, args, env and the rest"},
			{Path: "max_servers", Label: "Concurrent servers limit", Kind: configFieldNumber, Min: 0, Max: 32, Hint: "Default 6; range 1–32"},
			{Path: "idle_timeout", Label: "Idle exit (s)", Kind: configFieldNumber, Min: 0, Max: 86400, Hint: "Default 600; range 60–86400 when not 0"},
			{Path: "request_timeout", Label: "Request timeout (s)", Kind: configFieldNumber, Min: 0, Max: 600, Hint: "Default 30; range 1–600"},
			{Path: "diagnostics.after_edit", Label: "Diagnose after edit", Kind: configFieldBool3, Hint: "On by default"},
			{Path: "diagnostics.wait_ms", Label: "Diagnostic wait (ms)", Kind: configFieldNumber, Min: 0, Max: 30000, Hint: "Default 2500; 0 does not wait"},
			{
				Path: "diagnostics.min_severity", Label: "Lowest severity", Kind: configFieldEnum,
				Options: []string{"error", "warning", "information", "hint"}, Hint: "Default warning",
			},
			{Path: "diagnostics.max_per_file", Label: "Limit per file", Kind: configFieldNumber, Min: 0, Max: 200, Hint: "Default 20; range 1–200"},
			{Path: "diagnostics.max_files", Label: "File-count limit", Kind: configFieldNumber, Min: 0, Max: 50, Hint: "Default 10; range 1–50"},
			{Path: "diagnostics.late_delivery", Label: "Late diagnostics", Kind: configFieldBool3, Hint: "On by default"},
		},
	},
	{
		Name: "peers", Title: "Peer messaging", Tab: configTabAgents,
		Summary: "Agents' processes on this machine message each other",
		Fields: []configField{{
			Path: "claude_code", Label: "Interoperate with Claude Code", Kind: configFieldBool3, ClaudeCodeOnly: true,
			Hint: "Off by default; forebrain processes with different switches cannot see each other",
		}},
	},
	{
		Name: "gateway", Title: "Gateway", Tab: configTabFile,
		Summary:  "Listen address, authentication and outward links (read-only)",
		YAMLOnly: true,
	},
	{
		Name: "credentials", Title: "Credential files", Tab: configTabFile,
		Summary:  "Where external credential files live (read-only)",
		YAMLOnly: true,
	},
}

// configSectionSchemaFor returns one section's descriptor.
func configSectionSchemaFor(name string) (configSectionSchema, bool) {
	name = strings.TrimSpace(name)
	for _, schema := range configSectionSchemas {
		if schema.Name == name {
			return schema, true
		}
	}
	return configSectionSchema{}, false
}
