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
	// configFieldEffort is a thinking level. It has no option list of its own:
	// what a level may be is a property of the model row beside it, so the
	// picker is built from that row's levels and the row's own default, and
	// free text can never store a level the model cannot be given (plan 319).
	configFieldEffort
	// configFieldRows is a table or list — a map of profiles, of MCP
	// servers, of hook events — edited as rows: open one to edit its own
	// fields, add a row, delete a row.
	configFieldRows
	// configFieldModel is a reference to one row of the install's global
	// model table. The global table is the only place a provider or an
	// endpoint is configured, so every model choice picks a row by id
	// (plan 324): the picker lists the table's rows, and free text can
	// never name a provider the install does not carry.
	configFieldModel
	// configFieldBuiltinSubagents is the one entry that opens the six
	// built-in subagent types' install-wide model rows (P4, the prototype's
	// builtinSubagents kind): the page is rendered by the panel, not from
	// the descriptor's path, because the six rows live under three
	// different agents-section keys.
	configFieldBuiltinSubagents
)

// configModelFlavor is which empty a model picker offers and what picking it
// stores (plan 324 D1) — the four shapes the prototype fixes. global and
// subagent draw the same list (the empty option's own label is the field's
// metadata); model is the goal check's, whose empty option carries the
// conversation sentence and keeps no stored reference that resolves to no
// row; memory stores the explicit "current".
type configModelFlavor int

const (
	// configModelFlavorGlobal is an install-wide row: a definition's
	// default model, a built-in type's pin, the plan reviewer's row.
	configModelFlavorGlobal configModelFlavor = iota
	// configModelFlavorSubagent is one agent's own override on a type.
	configModelFlavorSubagent
	// configModelFlavorModel follows the conversation's model: the goal
	// check's own row.
	configModelFlavorModel
	// configModelFlavorMemory names the model memory work runs on, with
	// "current" as its explicit empty value.
	configModelFlavorMemory
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

	// The template layer's own metadata (plan 329). Everything below is read
	// by the nested page renderer, never by the section pages above.

	// Group names the fixed page section the field renders under, mirroring
	// the prototype's group labels — a template field's Group (plan 329), a
	// section field's (plan 325 D2). Fields of one group sit together in the
	// order the prototype fixes; an empty Group renders under no heading.
	Group string
	// Multiline marks a text field whose value holds lines (prompts,
	// descriptions); the single-line editor stays until plan 328 wires the
	// multiline one, and the flag is what tells it apart.
	Multiline bool
	// Secret marks a text field whose value must never be shown as it is
	// (plan 328 wires the masked editor; the flag is the contract).
	Secret bool
	// CustomOnly hides the field on a row that names a built-in or
	// system-dispatched subagent type: those entries take a model, a level,
	// a prompt and a switch, nothing else — the loader's own rule, read
	// ahead of the save.
	CustomOnly bool
	// HideOnBuiltin hides the field on an agent's entry naming one of the
	// four public built-in types (P5): such an entry is a model-and-level
	// pin, so the prompt rewrite and the switch it once carried are retired
	// — hidden here, and dropped from the stored row on load.
	HideOnBuiltin bool
	// The unified agent definition's role flags (P3, the prototype's agent
	// template). The two are mutually exclusive — a page renders one role's
	// fields, never both — and only the agent template carries them.
	//
	// PrimaryOnly hides the field unless the definition plays the primary
	// role (primary is true or unset): the connections and delegation a
	// primary agent owns, and the model its turns run on.
	PrimaryOnly bool
	// SubagentOnly hides the field unless the definition plays the subagent
	// role (primary false): the body an install-wide subagent runs on.
	SubagentOnly bool
	// KeyName marks the field that names the definition itself (the map
	// key): editable while a create has not landed the definition, read-only
	// afterwards — the prototype's keyName createOnly field, whose opening
	// on a saved page is refused with the created-fields sentence.
	KeyName bool
	// CreateEditable marks a field the create form lets the user set and a
	// saved definition locks (the prototype's createEditable flag). Opening
	// it on a saved page is refused with the created-fields sentence.
	CreateEditable bool
	// ShowFor lists the sibling `type` values a field belongs to, in the
	// hook template's own terms: a command hook shows its command and shell,
	// an HTTP hook its URL and headers.
	ShowFor []string
	// Entry marks the rows of a removable collection (a path row, a string
	// row): plan 326's footer removal is offered on these alone.
	Entry bool
	// Requires names the sibling key this field cannot be set without — a
	// thinking level names no model of its own.
	Requires string
	// Unit names the number's unit in the row's value ("seconds", "items").
	Unit string
	// ModelEmpty and ModelEmptyValue are the model picker's empty option
	// (plan 324): the label the empty value reads as, and the value that
	// stores it ("" unsets the key). Set on fields that hold a model row id.
	ModelEmpty      string
	ModelEmptyValue string
	// ModelEmptyDesc is the empty option's own description, where the
	// prototype gives the sentence a page of its own (P4): the type row's
	// and the system row's empty choices each say what an unset pin means.
	ModelEmptyDesc string
	// ModelFlavor is which model picker the field opens (plan 324 D1):
	// which empty its rows offer and what picking the empty stores.
	ModelFlavor configModelFlavor
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
	// StoreSection, when non-empty, marks this section a PROJECTION: it presents
	// a curated view of another stored section's document and saves back to that
	// section. The panel reads and writes p.raw[StoreSection]; Name is only what
	// the user sees and types. "Subagents" projects the agents store
	// (plan 314). A projection is not itself a config.Root section.
	StoreSection string
	// Fields are the editable settings, in the order the section page shows
	// them. A YAML-only section has none.
	Fields []configField
}

// storeSection is the configuration section this descriptor reads and writes:
// itself, or the section a projection presents.
func (s configSectionSchema) storeSection() string {
	if s.StoreSection != "" {
		return s.StoreSection
	}
	return s.Name
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
		Summary: "Choose what new sessions may do.",
		Fields: []configField{{
			Label: "Default approval mode", Kind: configFieldEnum,
			Options:      []string{"workspace_write", "full_access"},
			OptionLabels: map[string]string{"workspace_write": "Workspace write", "full_access": "Full access"},
			DefaultValue: "workspace_write",
			Hint:         "This setting affects new sessions, not the current conversation.",
		}},
	},
	{
		Name: "sandbox_workspace_write", Title: "Workspace sandbox", Tab: configTabApproval,
		Summary: "Control writable paths and network access.",
		Fields: []configField{
			{Path: "writable_roots", Label: "Extra writable roots", Kind: configFieldRows, List: true, Hint: "Add absolute paths outside the current workspace."},
			{Path: "network_access", Label: "Network access", Kind: configFieldBool3, DefaultValue: "true", Group: "Network", Hint: "Allow sandboxed commands to access the network."},
		},
	},
	{
		Name: "compact", Title: "Context compaction", Tab: configTabSession,
		Summary: "Keep long conversations within their context window.",
		Fields: []configField{
			{
				Path: "auto_compact_percent", Label: "Auto-compact at", Kind: configFieldNumber,
				Min: 50, Max: 90, Clamp: true, Unit: "%", DefaultValue: "90", Group: "Threshold",
				Hint: "Compact when context usage reaches this percentage.",
			},
			{Path: "prompt", Label: "Hand-off prompt", Kind: configFieldText, Multiline: true, DefaultValue: "Built-in prompt", Group: "Instructions", Hint: "Leave empty to use the built-in compaction prompt."},
		},
	},
	{
		Name: "batch", Title: "Batch projects", Tab: configTabApproval,
		Summary: "Set the starting policy for new batch projects.",
		Fields: []configField{{
			Path: "sandbox_default", Label: "Enable sandbox", Kind: configFieldBool3, DefaultValue: "true",
			Hint: "Existing batch projects keep their own sandbox policy.",
		}},
	},
	{
		Name: "memories", Title: "Memory", Tab: configTabSession,
		Summary: "Tune retrieval, retention and background processing.",
		Fields: []configField{
			{Path: "search_top_k", Label: "Results per search", Kind: configFieldNumber, DefaultValue: "5", Group: "Retrieval", Min: 1, Max: 20, Clamp: true, Unit: "results"},
			{Path: "max_raw_memories_for_consolidation", Label: "Memories per consolidation", Kind: configFieldNumber, DefaultValue: "256", Group: "Retrieval", Min: 1, Max: 4096, Clamp: true, Unit: "memories"},
			{Path: "max_unused_days", Label: "Expire unused memories after", Kind: configFieldNumber, DefaultValue: "30", Group: "Retention", Min: 0, Max: 365, Clamp: true, Unit: "days"},
			{Path: "max_rollout_age_days", Label: "Keep records for", Kind: configFieldNumber, DefaultValue: "10", Group: "Retention", Min: 0, Max: 90, Clamp: true, Unit: "days"},
			{Path: "max_rollouts_per_startup", Label: "Records per startup", Kind: configFieldNumber, DefaultValue: "2", Group: "Processing", Min: 1, Max: 128, Clamp: true, Unit: "records"},
			{Path: "min_rollout_idle_hours", Label: "Minimum idle time", Kind: configFieldNumber, DefaultValue: "6", Group: "Processing", Min: 1, Max: 48, Clamp: true, Unit: "hours"},
			// The stored value is a row of the global model table or the
			// explicit "current" (the conversation's own model, plan 207
			// §3.3): the picker stores one of those two, and the row shows
			// what is in force.
			{Path: "extract_model", Label: "Extraction model", Kind: configFieldModel, Group: "Models", ModelEmpty: "Follow conversation", ModelEmptyValue: "current", ModelFlavor: configModelFlavorMemory},
			{Path: "consolidation_model", Label: "Consolidation model", Kind: configFieldModel, Group: "Models", ModelEmpty: "Follow conversation", ModelEmptyValue: "current", ModelFlavor: configModelFlavorMemory},
		},
	},
	{
		Name: "cron", Title: "Scheduled tasks", Tab: configTabSession,
		Summary: "Choose how long task records are retained.",
		Fields: []configField{{
			Path: "retention_days", Label: "Keep task records for", Kind: configFieldNumber, DefaultValue: "30", Group: "Retention",
			Min: 1, Max: 3650, Unit: "days",
		}},
	},
	{
		Name: "features", Title: "Feature switches", Tab: configTabSession,
		Summary: "Enable memory and language intelligence.",
		Fields: []configField{
			{Path: "memories", Label: "Memory", Kind: configFieldBool3, DefaultValue: "true", Hint: "Remember useful context across conversations."},
			{Path: "lsp", Label: "Language servers", Kind: configFieldBool3, DefaultValue: "true", Hint: "Enable code intelligence and language diagnostics."},
		},
	},
	{
		Name: "agents", Title: "Agents", Tab: configTabAgents,
		Summary: "Manage definitions, shared defaults.",
		Fields: []configField{
			{
				Path: "definitions", Label: "Agent definitions", Kind: configFieldRows, Group: "Definitions",
				// A definition's default_model is picked from the global model
				// table like every model reference (plan 324): the row's page is
				// the agent template, whose Default model field opens the picker.
				Hint: "Manage each primary agent and its own configuration.",
			},
			// The six built-in subagent types' install-wide rows (P4, the
			// prototype's builtinSubagents entry, one row under Defaults):
			// the four public types' per-type rows (agents.subagent_models,
			// plan 315) and the two system types' global rows
			// (agents.plan_reviewer, plan 313; agents.goal_evaluator,
			// plan 299) behind one page. Each carries an install-wide model
			// row and thinking level, which an agent's own override still
			// outranks.
			{
				Path: "subagent_models", Label: "Built-in subagents", Kind: configFieldBuiltinSubagents, Group: "Defaults",
				Hint: "The 6 built-in subagent types, always on. Each carries an install-wide model and thinking level; an agent may override them.",
			},
			{
				Path: "defaults.network_retry_max", Label: "Network retries", Kind: configFieldNumber, DefaultValue: "10", Group: "Defaults",
				Min: 0, Max: 15, Clamp: true, Unit: "attempts", Hint: "Zero disables network retries.",
			},
			{
				Path: "defaults.stream_idle_timeout_seconds", Label: "Stream idle timeout", Kind: configFieldNumber, DefaultValue: "300", Group: "Defaults",
				Min: 0, Max: 1800, Clamp: true, Unit: "seconds", Hint: "Zero disables the watchdog. Positive values below 10 run as 10.",
			},
			{Path: "defaults.enable_subagent", Label: "Enable subagents", Kind: configFieldBool3, DefaultValue: "false", Group: "Defaults", Hint: "Allow the primary agent to delegate work."},
			{
				Path: "defaults.context_inject.warn_remaining_percent", Label: "Warn below remaining context", Kind: configFieldNumber, DefaultValue: "12", Group: "Defaults",
				Min: 1, Max: 50, Clamp: true, Unit: "%",
			},
			{Path: "defaults.mcp_servers", Label: "MCP servers", Kind: configFieldRows, Group: "Connections", Hint: "Shared with all agents. Agent entries with the same name override these."},
		},
	},
	{
		Name: "hooks", Title: "Hooks", Tab: configTabAgents,
		Summary: "Run configured actions at lifecycle events.",
		Fields: []configField{{
			Label: "Hook events", Kind: configFieldRows,
			Hint: "Command, prompt, agent and HTTP hooks are supported.",
		}},
	},
	{
		Name: "lsp", Title: "Language servers", Tab: configTabAgents,
		Summary: "Manage servers, timeouts and diagnostic delivery.",
		Fields: []configField{
			{Path: "recommendations", Label: "Recommend language servers", Kind: configFieldBool3, DefaultValue: "true", Group: "Discovery", Hint: "Suggest useful servers for this project."},
			{Path: "servers", Label: "Configured servers", Kind: configFieldRows, Group: "Discovery", Hint: "Each server maps files and languages to a process."},
			{Path: "max_servers", Label: "Concurrent servers", Kind: configFieldNumber, DefaultValue: "6", Group: "Lifecycle", Min: 0, Max: 32, Unit: "servers"},
			{Path: "idle_timeout", Label: "Exit when idle for", Kind: configFieldNumber, DefaultValue: "600", Group: "Lifecycle", Min: 0, Max: 86400, Unit: "seconds", Hint: "Zero uses the default. Other values must be at least 60 seconds."},
			{Path: "request_timeout", Label: "Request timeout", Kind: configFieldNumber, DefaultValue: "30", Group: "Lifecycle", Min: 0, Max: 600, Unit: "seconds"},
			{Path: "diagnostics.after_edit", Label: "Diagnose after edits", Kind: configFieldBool3, DefaultValue: "true", Group: "Diagnostics", Hint: "Request diagnostics after a file changes."},
			{Path: "diagnostics.wait_ms", Label: "Wait for diagnostics", Kind: configFieldNumber, DefaultValue: "2500", Group: "Diagnostics", Min: 0, Max: 30000, Unit: "ms", Hint: "Zero does not wait."},
			{
				Path: "diagnostics.min_severity", Label: "Minimum severity", Kind: configFieldEnum, DefaultValue: "warning", Group: "Diagnostics",
				Options:      []string{"error", "warning", "information", "hint"},
				OptionLabels: map[string]string{"error": "Error", "warning": "Warning", "information": "Information", "hint": "Hint"},
				Hint:         "Include this severity and all more severe diagnostics.",
			},
			{Path: "diagnostics.max_per_file", Label: "Diagnostics per file", Kind: configFieldNumber, DefaultValue: "20", Group: "Diagnostics", Min: 0, Max: 200, Unit: "items"},
			{Path: "diagnostics.max_files", Label: "Files per report", Kind: configFieldNumber, DefaultValue: "10", Group: "Diagnostics", Min: 0, Max: 50, Unit: "files"},
			{Path: "diagnostics.late_delivery", Label: "Deliver late diagnostics", Kind: configFieldBool3, DefaultValue: "true", Group: "Diagnostics", Hint: "Deliver diagnostics that arrive after the initial wait."},
		},
	},
	{
		Name: "peers", Title: "Peer messaging", Tab: configTabAgents,
		Summary: "Coordinate with other agent processes on this machine.",
		Fields: []configField{{
			Path: "claude_code", Label: "Claude Code interop", Kind: configFieldBool3, DefaultValue: "false", ClaudeCodeOnly: true,
			Hint: "Processes with different interop settings cannot see each other.",
		}},
	},
	{
		Name: "gateway", Title: "Gateway", Tab: configTabFile,
		Summary:  "Inspect the listening address and authentication.",
		YAMLOnly: true,
	},
	{
		Name: "credentials", Title: "Credential files", Tab: configTabFile,
		Summary:  "Inspect external sign-in file locations.",
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

// The nested templates' names (plan 329 D2). The object templates are the
// fixed-field pages a stored row opens as; the three pseudo-templates name a
// scalar row's editor instead of a page of fields — a path row and a string
// row open a text editor, and a matcher list is the hooks section's own tree,
// which keeps its rows page. Resolved from a row's real path shape, never
// from a descriptor's Path (the generic editor hands the same field down at
// every level).
const (
	configTemplateAgent           = "agent"
	configTemplateSubagent        = "subagent"
	configTemplateTypeDef         = "typeDef"
	configTemplateGlobalSystemRow = "globalSystemRow"
	configTemplateMCP             = "mcp"
	configTemplateOAuth           = "oauth"
	configTemplateToolApproval    = "toolApproval"
	configTemplateMatcher         = "matcher"
	configTemplateHook            = "hook"
	configTemplateLSPServer       = "lspServer"
	// configTemplateMatcherList is an event's matcher list under hooks.
	configTemplateMatcherList = "matcherList"
	// configTemplatePath is a writable-roots row: one absolute path.
	configTemplatePath = "path"
	// configTemplateString is one string element of a known collection.
	configTemplateString = "string"
)

// configTemplates is the single source of every nested page's field shape:
// what a stored row holds, in the order the prototype fixes, with the group
// each field renders under and the rules that hide or gate it. A key the
// template does not name still travels: the page lists it under its
// additional-fields group and the save writes the section whole, so nothing
// is lost. A future config field adds a descriptor here, not ad-hoc rows.
var configTemplates = map[string][]configField{
	// The unified agent definition (P3, the prototype's agent template field
	// for field): Name names the definition itself and Primary is decided at
	// create — both read-only on a saved page — and every other field belongs
	// to one of the two roles, shown by the role the definition plays. The
	// system prompt is the one field both roles share, and its help says what
	// each role does with it. Order carries no groups: the prototype draws
	// this page's fields flat.
	configTemplateAgent: {
		{Path: "name", Label: "Name", Kind: configFieldText, KeyName: true,
			Hint: "Lowercase letters, numbers and hyphens. This is the configuration key."},
		{Path: "display_name", Label: "Display name", Kind: configFieldText},
		{Path: "description", Label: "Description", Kind: configFieldText, Multiline: true, Hint: "Describe the role of this agent."},
		{Path: "primary", Label: "Primary", Kind: configFieldBool3, DefaultValue: "false", CreateEditable: true,
			Hint: "Set when the definition was created; it cannot be changed afterwards."},
		// The one field both roles carry, saying something different about
		// each (the prototype's helpPrimary/helpSubagent); the page picks the
		// sentence the definition's role names.
		{Path: "system_prompt", Label: "System prompt", Kind: configFieldText, Multiline: true},
		{
			Path: "default_model", Label: "Model", Kind: configFieldModel, ModelFlavor: configModelFlavorGlobal, PrimaryOnly: true,
			Hint:            "Pick a row from the global model table (/connect or Settings → Models). A primary agent has no provider list of its own.",
			ModelEmpty:      "Follow global default",
			ModelEmptyValue: "",
		},
		{Path: "mcp_servers", Label: "MCP servers", Kind: configFieldRows, List: true, PrimaryOnly: true, Hint: "Agent-specific entries augment or override shared servers."},
		{Path: "enable_subagent", Label: "Enable subagents", Kind: configFieldBool3, PrimaryOnly: true, Hint: "Default inherits the shared subagent setting."},
		{Path: "subagents", Label: "Subagents", Kind: configFieldRows, List: true, PrimaryOnly: true, Hint: "Built-in overrides and custom agent definitions."},
		{
			Path: "model", Label: "Model", Kind: configFieldModel, ModelFlavor: configModelFlavorGlobal, SubagentOnly: true,
			Hint:            "The model this subagent definition runs on; empty follows the conversation.",
			ModelEmpty:      "Follow conversation",
			ModelEmptyValue: "",
		},
		{Path: "reasoning_effort", Label: "Thinking level", Kind: configFieldEffort, Requires: "model", SubagentOnly: true},
		{
			Path: "tools", Label: "Tool access", Kind: configFieldEnum, SubagentOnly: true,
			Options:      []string{"read_only", "edit", "pick"},
			OptionLabels: map[string]string{"read_only": "Read-only", "edit": "Edit", "pick": "Pick"},
			DefaultValue: "edit", Hint: "Empty means Edit.",
		},
		{Path: "allowed_tools", Label: "Allowed tools", Kind: configFieldRows, List: true, SubagentOnly: true, Hint: "Used when tool access is Pick."},
		{Path: "continuable", Label: "Allow continuation", Kind: configFieldBool3, SubagentOnly: true, DefaultValue: "true", Hint: "A subagent that cannot continue is one-shot."},
	},
	configTemplateTypeDef: {
		{
			Path: "model", Label: "Model", Kind: configFieldModel, ModelFlavor: configModelFlavorGlobal,
			Hint:            "The type's install-wide default model row, picked from the global model table. An agent's own override still wins.",
			ModelEmpty:      "Follow conversation",
			ModelEmptyDesc:  "No default; the type follows the conversation model.",
			ModelEmptyValue: "",
		},
		{Path: "reasoning_effort", Label: "Thinking level", Kind: configFieldEffort, Requires: "model"},
	},
	configTemplateGlobalSystemRow: {
		{
			Path: "model", Label: "Model", Kind: configFieldModel, ModelFlavor: configModelFlavorGlobal,
			Hint:            "The global model the system dispatches this type on, picked from the global model table.",
			ModelEmpty:      "Follow conversation",
			ModelEmptyDesc:  "No global pin; the system chooses at dispatch.",
			ModelEmptyValue: "",
		},
		{Path: "reasoning_effort", Label: "Thinking level", Kind: configFieldEffort, Requires: "model"},
	},
	configTemplateSubagent: {
		{Path: "name", Label: "Type name", Kind: configFieldText, Hint: "Built-in overrides: general-purpose, explore, plan, verification."},
		{
			Path: "model", Label: "Model", Kind: configFieldModel, ModelFlavor: configModelFlavorSubagent,
			Hint:            "This agent's override. Empty follows the install-wide type default, then the conversation.",
			ModelEmpty:      "Follow primary agent",
			ModelEmptyValue: "",
		},
		{Path: "reasoning_effort", Label: "Thinking level", Kind: configFieldEffort, Requires: "model"},
		// The prompt rewrite and the switch are the two fields a built-in's
		// pin retires (P5): hidden on a built-in's entry, judged on a custom
		// one.
		{Path: "system_prompt", Label: "System prompt", Kind: configFieldText, Multiline: true, HideOnBuiltin: true, Hint: "Leave empty to use the built-in prompt."},
		{Path: "description", Label: "Description", Kind: configFieldText, Multiline: true, CustomOnly: true, Hint: "Custom subagents only."},
		{
			Path: "tools", Label: "Tool access", Kind: configFieldEnum, CustomOnly: true,
			Options:      []string{"read_only", "edit", "pick"},
			OptionLabels: map[string]string{"read_only": "Read only", "edit": "Edit", "pick": "Pick"},
			DefaultValue: "read_only", Hint: "Custom subagents only.",
		},
		{Path: "allowed_tools", Label: "Allowed tools", Kind: configFieldRows, List: true, CustomOnly: true, Hint: "Used when tool access is Pick."},
		{Path: "continuable", Label: "Allow continuation", Kind: configFieldBool3, CustomOnly: true, Hint: "Custom subagents only."},
		{Path: "enabled", Label: "Enabled", Kind: configFieldBool3, DefaultValue: "true", HideOnBuiltin: true, Hint: "Choose an explicit value or inherit the default."},
	},
	configTemplateMCP: {
		{Path: "name", Label: "Server name", Kind: configFieldText, Group: "Identity", Hint: "Unique within this server list."},
		{Path: "enabled", Label: "Enabled", Kind: configFieldBool3, DefaultValue: "true", Group: "Identity", Hint: "Keep the entry configured while disabling its tools."},
		{Path: "required", Label: "Required", Kind: configFieldBool3, DefaultValue: "false", Group: "Identity", Hint: "A required server cannot be silently skipped."},
		{
			Path: "transport", Label: "Transport", Kind: configFieldEnum, Group: "Connection",
			Options: []string{"stdio", "http", "sse"}, OptionLabels: map[string]string{"stdio": "Stdio", "http": "HTTP", "sse": "SSE"},
			DefaultValue: "stdio", Hint: "Choose a local process or remote endpoint.",
		},
		{Path: "command", Label: "Command", Kind: configFieldText, Group: "Connection", Hint: "Program to start for stdio transport."},
		{Path: "args", Label: "Arguments", Kind: configFieldRows, List: true, Group: "Connection", Hint: "One argument per row."},
		{Path: "url", Label: "Server URL", Kind: configFieldText, Group: "Connection", Hint: "Absolute URL for HTTP or SSE transport."},
		{Path: "headers", Label: "Headers", Kind: configFieldRows, Group: "Connection", Hint: "Header values remain masked."},
		{Path: "oauth", Label: "OAuth", Kind: configFieldRows, Group: "Authentication", Hint: "Configure OAuth fields. No login is performed by this editor."},
		{Path: "env", Label: "Environment", Kind: configFieldRows, Group: "Process", Hint: "Explicit environment variables for the server."},
		{Path: "inherit_parent_env", Label: "Inherit environment", Kind: configFieldBool3, DefaultValue: "true", Group: "Process", Hint: "Include parent-process environment variables."},
		{Path: "disable_standalone_sse", Label: "Disable standalone SSE", Kind: configFieldBool3, DefaultValue: "false", Group: "Process", Hint: "Disable the standalone SSE transport."},
		{Path: "expected_command_sha256", Label: "Expected command SHA-256", Kind: configFieldText, Group: "Process", Hint: "Verify the executable digest."},
		{Path: "startup_timeout", Label: "Startup timeout", Kind: configFieldNumber, Min: 0, Max: 1000000000, DefaultValue: "0", Unit: "seconds", Group: "Timeouts"},
		{Path: "request_timeout", Label: "Request timeout", Kind: configFieldNumber, Min: 0, Max: 1000000000, DefaultValue: "0", Unit: "seconds", Group: "Timeouts"},
		{
			Path: "default_tools_approval_mode", Label: "Default tool approval", Kind: configFieldEnum, Group: "Tool permissions",
			Options: []string{"auto", "prompt", "writes", "approve"}, OptionLabels: map[string]string{"auto": "Auto", "prompt": "Prompt", "writes": "Writes", "approve": "Approve"},
			DefaultValue: "auto", Hint: "Set the approval mode for this server's tools.",
		},
		{Path: "tools", Label: "Tool overrides", Kind: configFieldRows, Group: "Tool permissions", Hint: "Override approval per tool."},
	},
	configTemplateOAuth: {
		{Path: "mode", Label: "OAuth mode", Kind: configFieldText, Hint: "Enter the OAuth mode supported by this server."},
		{Path: "access_token", Label: "Access token", Kind: configFieldText, Secret: true},
		{Path: "token_type", Label: "Token type", Kind: configFieldText},
		{Path: "client_id", Label: "Client ID", Kind: configFieldText},
		{Path: "client_secret", Label: "Client secret", Kind: configFieldText, Secret: true},
		{Path: "token_url", Label: "Token URL", Kind: configFieldText},
		{Path: "authorization_url", Label: "Authorization URL", Kind: configFieldText},
		{Path: "redirect_url", Label: "Redirect URL", Kind: configFieldText},
		{Path: "refresh_token", Label: "Refresh token", Kind: configFieldText, Secret: true},
		{Path: "scopes", Label: "Scopes", Kind: configFieldRows, List: true},
	},
	configTemplateToolApproval: {
		{
			Path: "approval_mode", Label: "Approval mode", Kind: configFieldEnum,
			Options:      []string{"auto", "prompt", "writes", "approve"},
			OptionLabels: map[string]string{"auto": "Auto", "prompt": "Prompt", "writes": "Writes", "approve": "Approve"},
			DefaultValue: "auto", Hint: "Override the server-level tool approval policy.",
		},
	},
	configTemplateMatcher: {
		{Path: "matcher", Label: "Tool matcher", Kind: configFieldText, Hint: "Match tool names, or use * for all tools."},
		{Path: "hooks", Label: "Actions", Kind: configFieldRows, List: true, Hint: "Actions run in their configured order."},
	},
	configTemplateHook: {
		{
			Path: "type", Label: "Action type", Kind: configFieldEnum, Group: "Action",
			Options:      []string{"command", "prompt", "agent", "http"},
			OptionLabels: map[string]string{"command": "Command", "prompt": "Prompt", "agent": "Agent", "http": "HTTP"},
			DefaultValue: "command", Hint: "Choose the action this hook performs.",
		},
		{Path: "command", Label: "Command", Kind: configFieldText, Group: "Action", ShowFor: []string{"command"}, Hint: "Run by the runtime when the hook fires."},
		{Path: "prompt", Label: "Prompt", Kind: configFieldText, Multiline: true, Group: "Action", ShowFor: []string{"prompt", "agent"}, Hint: "Instructions for the model."},
		{Path: "url", Label: "Request URL", Kind: configFieldText, Group: "Action", ShowFor: []string{"http"}, Hint: "Absolute URL for the HTTP hook."},
		{
			Path: "shell", Label: "Shell", Kind: configFieldEnum, Group: "Action", ShowFor: []string{"command"},
			Options: []string{"bash", "powershell"}, OptionLabels: map[string]string{"bash": "Bash", "powershell": "Powershell"},
			DefaultValue: "bash", Hint: "Shell used by a command hook.",
		},
		{Path: "model", Label: "Model", Kind: configFieldText, Group: "Action", ShowFor: []string{"prompt", "agent"}, Hint: "Optional model override."},
		{Path: "headers", Label: "Headers", Kind: configFieldRows, Group: "Action", ShowFor: []string{"http"}, Hint: "Header values stay masked."},
		{Path: "allowed_env_vars", Label: "Allowed environment variables", Kind: configFieldRows, List: true, Group: "Action", ShowFor: []string{"http"}, Hint: "Variables available to an HTTP hook."},
		{Path: "if", Label: "Condition", Kind: configFieldText, Group: "Behavior", Hint: "Use the runtime permission-rule syntax."},
		{Path: "timeout", Label: "Timeout", Kind: configFieldNumber, Min: 0, Max: 86400, DefaultValue: "0", Unit: "seconds", Group: "Behavior"},
		{Path: "status_message", Label: "Status message", Kind: configFieldText, Group: "Behavior", Hint: "Shown while the hook is running."},
		{Path: "once", Label: "Run once", Kind: configFieldBool3, DefaultValue: "false", Group: "Behavior", Hint: "Run this action once per session."},
		{Path: "async", Label: "Run asynchronously", Kind: configFieldBool3, DefaultValue: "false", Group: "Behavior", Hint: "Do not wait for the hook before continuing."},
		{Path: "async_rewake", Label: "Wake after completion", Kind: configFieldBool3, DefaultValue: "false", Group: "Behavior", Hint: "Allow an asynchronous hook to wake the conversation."},
	},
	configTemplateLSPServer: {
		{Path: "enabled", Label: "Enabled", Kind: configFieldBool3, DefaultValue: "true", Group: "Process", Hint: "Enable this configured language server."},
		{Path: "command", Label: "Command", Kind: configFieldText, Group: "Process", Hint: "Executable to start."},
		{Path: "args", Label: "Arguments", Kind: configFieldRows, List: true, Group: "Process", Hint: "One argument per row."},
		{Path: "env", Label: "Environment", Kind: configFieldRows, Group: "Process", Hint: "Server environment variables."},
		{Path: "env_passthrough", Label: "Inherited environment keys", Kind: configFieldRows, List: true, Group: "Workspace", Hint: "Explicit keys to pass through."},
		{Path: "extension_to_language", Label: "File extensions", Kind: configFieldRows, Group: "Workspace", Hint: "Map extensions to language IDs."},
		{Path: "filenames", Label: "File names", Kind: configFieldRows, Group: "Workspace", Hint: "Map exact file names to language IDs."},
		{Path: "root_markers", Label: "Root markers", Kind: configFieldRows, List: true, Group: "Workspace", Hint: "Files used to find the workspace root."},
		{Path: "workspace_folder", Label: "Workspace folder", Kind: configFieldText, Group: "Workspace", Hint: "Optional workspace folder override."},
		{Path: "initialization_options", Label: "Initialization options", Kind: configFieldRows, Group: "Configuration", Hint: "Arbitrary JSON object sent at startup."},
		{Path: "settings", Label: "Server settings", Kind: configFieldRows, Group: "Configuration", Hint: "Arbitrary JSON configuration object."},
		{Path: "startup_timeout", Label: "Startup timeout", Kind: configFieldNumber, Min: 0, Max: 600, DefaultValue: "0", Unit: "seconds", Group: "Lifecycle"},
		{Path: "shutdown_timeout", Label: "Shutdown timeout", Kind: configFieldNumber, Min: 0, Max: 60, DefaultValue: "0", Unit: "seconds", Group: "Lifecycle"},
		{Path: "restart_on_crash", Label: "Restart after crash", Kind: configFieldBool3, DefaultValue: "true", Group: "Lifecycle", Hint: "Restart the server after a crash."},
		{Path: "max_restarts", Label: "Maximum restarts", Kind: configFieldNumber, Min: 0, Max: 20, DefaultValue: "0", Unit: "attempts", Group: "Lifecycle"},
		{Path: "diagnostics", Label: "Diagnostics", Kind: configFieldBool3, DefaultValue: "true", Group: "Capabilities", Hint: "Receive diagnostics from this server."},
		{
			Path: "role", Label: "Role", Kind: configFieldEnum, Group: "Capabilities",
			Options: []string{"primary", "diagnostics"}, OptionLabels: map[string]string{"primary": "Primary", "diagnostics": "Diagnostics"},
			DefaultValue: "primary", Hint: "Choose the server role.",
		},
		{Path: "priority", Label: "Priority", Kind: configFieldNumber, Group: "Capabilities", Hint: "Enter an integer priority."},
		{Path: "prewarm", Label: "Prewarm", Kind: configFieldBool3, DefaultValue: "false", Group: "Capabilities", Hint: "Start this server ahead of its first request."},
	},
	// The path pseudo-template carries the one field its scalar row edits, so
	// the row opens the editor directly instead of asking what type the value
	// is; a string row inherits the parent collection's own descriptor the
	// same way. Entry marks the row as a removable collection item (plan
	// 326's footer).
	configTemplatePath: {
		{Label: "Writable root", Kind: configFieldText, Entry: true, Hint: "Enter an absolute path."},
	},
}

// configTemplateFields is one object template's fixed fields; a
// pseudo-template answers none.
func configTemplateFields(template string) []configField {
	return configTemplates[template]
}

// configTemplateIsObject reports whether a template names a fixed-field page
// rather than a scalar row's editor.
func configTemplateIsObject(template string) bool {
	_, ok := configTemplates[template]
	return ok
}
