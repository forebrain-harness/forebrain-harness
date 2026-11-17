// Subagent configuration from the web surface (plan 079).
//
// A primary agent's subagents are one list in the agents section of the state
// database — its overrides of the built-ins the model may dispatch, and the
// custom subagents it defines.
// The page this file serves is a configuration list, not a roster: what runs
// right now is on the conversation card, in the subagent drawer and on the
// workbench, and none of that is written here.
//
// Three invariants run through the file:
//
//   - A row is built from the configuration and nothing else, so the table and
//     the drawer describe the same subagent. Whether it runs is a runtime fact,
//     not this surface's business.
//   - A system subagent is visible and read-only. The system dispatches it at
//     its own occasions (plan review, /goal checks, automatic approval), so a
//     switch or a prompt there would be a promise the runtime does not keep.
//   - A write validates the definition the way the loader would, stores the
//     section and rebinds the agent's runners. Rebuilding is what "the next
//     session uses it" means for a process that already has runners.
package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	"github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

// The three kinds a subagent row can be.
const (
	// subagentKindBuiltin is a built-in the model may dispatch: this agent may
	// override its model, its reasoning effort and its prompt, and may switch
	// it off.
	subagentKindBuiltin = "builtin"
	// subagentKindCustom is a subagent this primary agent defines itself.
	subagentKindCustom = "custom"
	// subagentKindShared is an install-wide custom subagent (plan 314): one
	// definition every primary agent may consume, edited in the settings page's
	// shared registry and, on this page, only switched on or off.
	subagentKindShared = "shared"
	// subagentKindSystem is a built-in the system dispatches at its own
	// occasions. The page shows it; nothing here changes it.
	subagentKindSystem = "system"
)

// Where a row's model came from.
const (
	subagentModelFromAgent  = "agent"
	subagentModelFromGlobal = "global"
)

// subagentAPIError is a refused request with the status the client should read.
// Everything else reaching a handler is a server-side failure and answers 500.
type subagentAPIError struct {
	Status int
	Msg    string
}

func (e *subagentAPIError) Error() string { return e.Msg }

func subagentBadRequest(format string, a ...any) error {
	return &subagentAPIError{Status: http.StatusBadRequest, Msg: fmt.Sprintf(format, a...)}
}

func subagentNotFound(format string, a ...any) error {
	return &subagentAPIError{Status: http.StatusNotFound, Msg: fmt.Sprintf(format, a...)}
}

func subagentConflict(format string, a ...any) error {
	return &subagentAPIError{Status: http.StatusConflict, Msg: fmt.Sprintf(format, a...)}
}

// failSubagentWrite answers with the error's own status and text. The text is
// the validator's own sentence where there is one — it names the entry and the
// rule — because that is the only explanation the form has to show.
func failSubagentWrite(w http.ResponseWriter, err error) {
	var api *subagentAPIError
	if errors.As(err, &api) {
		http.Error(w, api.Msg, api.Status)
		return
	}
	var invalid *state.InvalidSectionError
	if errors.As(err, &invalid) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

// subagentRow is one row of the subagent page. The list and the drawer share
// it, so what the table shows and what the form opens with cannot disagree.
type subagentRow struct {
	Name string `json:"name"`
	// Kind is builtin, custom or system.
	Kind        string `json:"kind"`
	Description string `json:"description"`
	// ModelProvider and Model are the model the subagent runs on, empty when it
	// follows the conversation's model. ModelID is the configured row the
	// subagent is pinned to — what the drawer's picker selects and what a write
	// stores (plan 262) — and ModelLabel/ModelEndpoint are what the form shows
	// without recomputing them.
	ModelProvider   string `json:"model_provider"`
	Model           string `json:"model"`
	ModelID         string `json:"model_id"`
	ModelLabel      string `json:"model_label"`
	ModelEndpoint   string `json:"model_endpoint"`
	ReasoningEffort string `json:"reasoning_effort"`
	// ModelSource says where the model above came from: "agent" for this
	// agent's own subagent configuration, "global" for the install-wide per-type
	// row (agents.subagent_models.<type>), empty for the conversation's model.
	ModelSource string `json:"model_source"`
	// Tools is the row's tool tier: read_only, edit, pick, or none for a type
	// that has no tools at all. A built-in's tier is fixed by its type, so this
	// is the only place it is described.
	Tools        string   `json:"tools"`
	AllowedTools []string `json:"allowed_tools"`
	Continuable  bool     `json:"continuable"`
	// Enabled is this row's own switch. It is null for a system subagent,
	// which nothing here configures.
	Enabled *bool `json:"enabled"`
	// PromptOverridden marks a built-in whose system prompt this agent rewrote.
	PromptOverridden bool `json:"prompt_overridden"`
	// GlobalModelLabel and GlobalReasoningEffort are the install-wide row the
	// system dispatches this type on, when the type has one: today only the
	// plan reviewer, whose row lives in agents.plan_reviewer (plan 313).
	// Display-only — dispatch for a system subagent is unchanged — and both
	// stay empty when no row is configured or the stored reference names no
	// row, because a model the engine cannot resolve is not a model to show.
	GlobalModelLabel      string `json:"global_model_label,omitempty"`
	GlobalReasoningEffort string `json:"global_reasoning_effort,omitempty"`
}

// subagentDetail is one row as the drawer opens it: the row, plus the prompt in
// force and — for a built-in or system subagent — the original it restores to.
type subagentDetail struct {
	subagentRow
	// SystemPrompt is the text that will actually be injected: a custom
	// subagent's own prompt, this agent's rewrite of a built-in, or the
	// built-in's original.
	SystemPrompt string `json:"system_prompt"`
	// DefaultPrompt is the harness's own text, shown word for word (a reader
	// cannot judge a rewrite against "see the source"). Absent for a custom
	// subagent, which has no original.
	DefaultPrompt *string `json:"default_prompt,omitempty"`
}

// subagentNames lists every subagent the page shows, in its order: this agent's
// own entries (its custom subagents and the reference entries that enable a
// shared one) in configuration order, then the shared registry's names it has
// not referenced yet — a shared subagent is on this page whether or not the
// agent enabled it, because that is where it gets enabled (plan 314) — then the
// built-ins the model may dispatch, then the ones the system dispatches.
func subagentNames(cfg *config.Root, def config.AgentDefinition) []string {
	out := make([]string, 0, len(def.Subagents)+len(agent.PublicSubagentNames)+len(agent.SystemSubagentNames))
	for _, entry := range def.Subagents {
		name := subagentKey(entry.Name)
		if slices.Contains(agent.PublicSubagentNames, name) || slices.Contains(agent.SystemSubagentNames, name) {
			continue
		}
		out = append(out, name)
	}
	for _, shared := range sharedSubagentDefinitions(cfg) {
		name := subagentKey(shared.Name)
		if name == "" || slices.Contains(out, name) {
			continue
		}
		out = append(out, name)
	}
	out = append(out, agent.PublicSubagentNames...)
	return append(out, agent.SystemSubagentNames...)
}

// sharedSubagentDefinitions is the install-wide registry, nil when there is no
// configuration to read it from.
func sharedSubagentDefinitions(cfg *config.Root) []config.SubagentConfig {
	if cfg == nil {
		return nil
	}
	return cfg.Agents.SharedSubagents
}

// sharedSubagentName reports whether name is one of the install-wide shared
// custom subagents (plan 314).
func sharedSubagentName(cfg *config.Root, name string) bool {
	_, ok := config.SharedSubagentFor(sharedSubagentDefinitions(cfg), name)
	return ok
}

// subagentRows renders the page's rows in subagentNames order.
func subagentRows(cfg *config.Root, agentID string) []subagentRow {
	def := cfg.Agents.Definitions[agentID]
	names := subagentNames(cfg, def)
	rows := make([]subagentRow, 0, len(names))
	for _, name := range names {
		if row, ok := subagentRowFor(cfg, agentID, name); ok {
			rows = append(rows, row)
		}
	}
	return rows
}

// subagentRowFor renders one row. Which kind it is decides where its
// description, its tools and its switch come from: a custom subagent's own
// entry, this agent's override of a built-in, the install-wide definition of a
// shared subagent, or the built-in definition.
func subagentRowFor(cfg *config.Root, agentID, name string) (subagentRow, bool) {
	def := cfg.Agents.Definitions[agentID]
	key := subagentKey(name)
	switch subagentKindFor(cfg, def, key) {
	case subagentKindSystem:
		return systemSubagentRow(cfg, key), true
	case subagentKindBuiltin:
		return builtinSubagentRow(cfg, agentID, key, subagentEntry(def, key)), true
	case subagentKindCustom:
		return customSubagentRow(cfg, agentID, key, subagentEntry(def, key)), true
	case subagentKindShared:
		return sharedSubagentRow(cfg, key, subagentEntry(def, key)), true
	}
	return subagentRow{}, false
}

// builtinSubagentRow describes a built-in the model may dispatch. Everything
// but the model, the effort, the prompt and the switch is the definition's:
// a built-in's description and tools are its type, not its configuration.
func builtinSubagentRow(cfg *config.Root, agentID, name string, entry config.SubagentConfig) subagentRow {
	// The name comes from the list this walks, so the definition is there.
	def, _ := agent.ResolvePublicSubtype(name)
	row := subagentRow{
		Name:             name,
		Kind:             subagentKindBuiltin,
		Description:      strings.TrimSpace(def.Description),
		Tools:            tool.BuiltinSubagentToolsTier(name),
		AllowedTools:     []string{},
		Continuable:      def.Continuable,
		Enabled:          config.BoolPtr(subagentEnabled(entry)),
		PromptOverridden: strings.TrimSpace(entry.SystemPrompt) != "",
	}
	subagentRowModel(cfg, agentID, name, &row, entry)
	return row
}

// customSubagentRow describes one of this agent's own subagents.
func customSubagentRow(cfg *config.Root, agentID, name string, entry config.SubagentConfig) subagentRow {
	allowed := make([]string, 0, len(entry.AllowedTools))
	if customSubagentTools(entry) == config.SubagentToolsPick {
		allowed = append(allowed, entry.AllowedTools...)
	}
	row := subagentRow{
		Name:         name,
		Kind:         subagentKindCustom,
		Description:  strings.TrimSpace(entry.Description),
		Tools:        customSubagentTools(entry),
		AllowedTools: allowed,
		Continuable:  entry.Continuable == nil || *entry.Continuable,
		Enabled:      config.BoolPtr(subagentEnabled(entry)),
	}
	subagentRowModel(cfg, agentID, name, &row, entry)
	return row
}

// customSubagentTools is a custom subagent's tool tier. Empty is read_only:
// a subagent that may edit the codebase is a deliberate choice, never a default.
func customSubagentTools(entry config.SubagentConfig) string {
	if tools := strings.TrimSpace(entry.Tools); tools != "" {
		return tools
	}
	return config.SubagentToolsReadOnly
}

// systemSubagentRow describes a subagent the system dispatches. It carries no
// switch and no model of this agent's: the system chooses both when it calls
// one. The plan reviewer is the one type with an install-wide row of its own
// (agents.plan_reviewer, plan 313), so that row is reported as a label beside
// the row's identity — display only; nothing here decides what runs.
func systemSubagentRow(cfg *config.Root, name string) subagentRow {
	// The name comes from the list this walks, so the definition is there.
	def, _ := agent.ResolveSubtype(name)
	row := subagentRow{
		Name:         name,
		Kind:         subagentKindSystem,
		Description:  strings.TrimSpace(def.Description),
		Tools:        tool.BuiltinSubagentToolsTier(name),
		AllowedTools: []string{},
		Continuable:  def.Continuable,
	}
	if name == run.PlanReviewSubagentType && cfg != nil {
		entry := cfg.Agents.PlanReviewer
		if ref := strings.TrimSpace(entry.Model); ref != "" {
			// A reference that resolves to no row is not claimed: the engine
			// would not run on it either.
			if resolved, ok := config.ResolveModelRef(cfg, "main", ref); ok {
				row.GlobalModelLabel = llm.FormatProviderModelDisplay(resolved.Provider, resolved.Model)
				row.GlobalReasoningEffort = strings.TrimSpace(entry.ReasoningEffort)
			}
		}
	}
	return row
}

// sharedSubagentRow describes an install-wide shared custom subagent (plan 314).
// Everything a subagent is — description, tools, prompt, model, lifecycle — is
// the registry's definition, never this agent's: the page shows one switch and
// the facts behind it. Enabled comes from this agent's reference entry alone,
// and it is false when there is none — a shared subagent runs nowhere until the
// agent enables it (owner 2026-11-16), the opposite of a custom subagent's
// absent-means-on switch.
func sharedSubagentRow(cfg *config.Root, name string, entry config.SubagentConfig) subagentRow {
	// The name comes from the registry this reads, so the definition is there.
	shared, _ := config.SharedSubagentFor(sharedSubagentDefinitions(cfg), name)
	allowed := make([]string, 0, len(shared.AllowedTools))
	if customSubagentTools(shared) == config.SubagentToolsPick {
		allowed = append(allowed, shared.AllowedTools...)
	}
	row := subagentRow{
		Name:         name,
		Kind:         subagentKindShared,
		Description:  strings.TrimSpace(shared.Description),
		Tools:        customSubagentTools(shared),
		AllowedTools: allowed,
		Continuable:  shared.Continuable == nil || *shared.Continuable,
		Enabled:      config.BoolPtr(entry.Enabled != nil && *entry.Enabled),
		// A shared subagent's model is the install-wide row it stores, so the
		// page says the model came from there rather than from this agent.
		ModelSource: subagentModelFromGlobal,
	}
	if ref := strings.TrimSpace(shared.Model); ref != "" {
		row.ReasoningEffort = strings.TrimSpace(shared.ReasoningEffort)
		// Resolved as main's row: the id branch ignores the agent name, which is
		// exactly what makes a shared subagent's model global (the goal check's
		// precedent, plans 299 and 314).
		if resolved, found := config.ResolveModelRef(cfg, "main", ref); found {
			subagentRowModelFields(&row, resolved)
		} else {
			row.ModelProvider, row.Model, _ = strings.Cut(ref, "/")
			row.ModelProvider = strings.TrimSpace(row.ModelProvider)
			row.Model = strings.TrimSpace(row.Model)
			row.ModelLabel = llm.FormatProviderModelDisplay(row.ModelProvider, row.Model)
		}
	}
	return row
}

// subagentRowModel fills a row's model fields with the model a subagent runs on,
// in the order dispatch uses it: this agent's own entry for it, then — for a
// built-in the model may dispatch — the install-wide per-type row
// agents.subagent_models.<type>, and otherwise the conversation's model. pkg/run's SubagentOwnModel is the same order and resolves the entry the
// same way; a row that described a different one would be telling the user the
// wrong thing about what will run.
//
// The entry names a model row (plan 262) or a legacy "provider/model" label. A
// reference nothing carries has no id and no endpoint to report, and its own
// halves are all there is to show; the runner falls back to the conversation's
// model for it.
func subagentRowModel(cfg *config.Root, agentID, name string, row *subagentRow, entry config.SubagentConfig) {
	if configured := strings.TrimSpace(entry.Model); configured != "" {
		row.ReasoningEffort = strings.TrimSpace(entry.ReasoningEffort)
		row.ModelSource = subagentModelFromAgent
		if resolved, found := config.ResolveModelRef(cfg, agentID, configured); found {
			subagentRowModelFields(row, resolved)
			return
		}
		row.ModelProvider, row.Model, _ = strings.Cut(configured, "/")
		row.ModelProvider = strings.TrimSpace(row.ModelProvider)
		row.Model = strings.TrimSpace(row.Model)
		row.ModelLabel = llm.FormatProviderModelDisplay(row.ModelProvider, row.Model)
		return
	}
	if !slices.Contains(agent.PublicSubagentNames, name) {
		return
	}
	// The same resolution run.SubagentOwnModel answers with: a type that names
	// no model of its own runs on the conversation's model, never on main's
	// default dressed up as the type's own.
	if !config.HasOwnModelChoice(cfg, name) {
		return
	}
	defEntry := config.EffectiveDefaultLLMFor(cfg, name)
	if defEntry == nil {
		return
	}
	row.ReasoningEffort = config.ReasoningEffort(defEntry.Params)
	row.ModelSource = subagentModelFromGlobal
	subagentRowModelFields(row, *defEntry)
}

// subagentRowModelFields names one configured row on the subagent row.
func subagentRowModelFields(row *subagentRow, entry config.AgentLLMProviderConfig) {
	row.ModelID = strings.TrimSpace(entry.ID)
	row.ModelProvider = strings.TrimSpace(entry.Provider)
	row.Model = strings.TrimSpace(entry.Model)
	row.ModelLabel = llm.FormatProviderModelDisplay(entry.Provider, entry.Model)
	row.ModelEndpoint = turn.ModelEndpointLabel(entry.BaseURL, entry.APIPath, entry.URLVerbatim)
}

// subagentRowMatches is the page's search box: the name a person types and the
// description the model reads when it picks a type.
func subagentRowMatches(row subagentRow, q string) bool {
	needle := strings.ToLower(q)
	return strings.Contains(strings.ToLower(row.Name), needle) ||
		strings.Contains(strings.ToLower(row.Description), needle)
}

// subagentDetailFor is one row as the drawer opens it.
func subagentDetailFor(cfg *config.Root, agentID, name string) (subagentDetail, bool) {
	row, ok := subagentRowFor(cfg, agentID, name)
	if !ok {
		return subagentDetail{}, false
	}
	out := subagentDetail{subagentRow: row}
	// Prompt is the effective text: a custom subagent's own, this agent's
	// rewrite of a built-in, else the built-in's. A shared subagent's prompt is
	// the registry's whether or not this agent enabled it — the definition is
	// one text, and the page would otherwise describe a subagent it cannot show
	// (plan 314).
	if row.Kind == subagentKindShared {
		if shared, ok := config.SharedSubagentFor(sharedSubagentDefinitions(cfg), row.Name); ok {
			out.SystemPrompt = strings.TrimSpace(shared.SystemPrompt)
		}
	} else if prompt, found := run.SubagentCatalogFor(cfg, agentID).Prompt(row.Name); found {
		out.SystemPrompt = prompt
	}
	if row.Kind != subagentKindCustom {
		if original, found := agent.SystemPromptForSubtype(row.Name); found {
			out.DefaultPrompt = &original
		}
	}
	return out, true
}

// --- names and entries ------------------------------------------------------

// subagentKey is the spelling subagent names are compared in: trimmed and
// lower-case, the same normalisation subagent_type and the loader apply.
func subagentKey(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// subagentKindFor names what a subagent name is for this agent. The shared
// registry is consulted before this agent's own entries: the loader lets a
// shared name appear in them only as the reference entry that enables it, so an
// entry of that name is a switch, not a definition (plan 314).
func subagentKindFor(cfg *config.Root, def config.AgentDefinition, name string) string {
	key := subagentKey(name)
	switch {
	case slices.Contains(agent.SystemSubagentNames, key):
		return subagentKindSystem
	case slices.Contains(agent.PublicSubagentNames, key):
		return subagentKindBuiltin
	case sharedSubagentName(cfg, key):
		return subagentKindShared
	case subagentEntryIndex(def.Subagents, key) >= 0:
		return subagentKindCustom
	}
	return ""
}

// subagentEntry returns the configured entry for name, zero when there is none.
// A built-in without an entry is a built-in at its defaults.
func subagentEntry(def config.AgentDefinition, name string) config.SubagentConfig {
	if i := subagentEntryIndex(def.Subagents, name); i >= 0 {
		return def.Subagents[i]
	}
	return config.SubagentConfig{Name: subagentKey(name)}
}

// subagentEntryIndex finds one configured entry by name.
func subagentEntryIndex(entries []config.SubagentConfig, name string) int {
	key := subagentKey(name)
	for i, entry := range entries {
		if subagentKey(entry.Name) == key {
			return i
		}
	}
	return -1
}

// subagentEnabled reads an entry's switch: absent means enabled, because a
// configuration written before the switch existed describes subagents that run.
func subagentEnabled(entry config.SubagentConfig) bool {
	return entry.Enabled == nil || *entry.Enabled
}

// subagentEnabledPointer renders a switch for the file: on is the default and
// is written as absent, so a configuration carries only what it decides.
func subagentEnabledPointer(enabled bool) *bool {
	if enabled {
		return nil
	}
	return config.BoolPtr(false)
}

// subagentEnabledAfterUpdate is the switch an update leaves the entry with: the
// value the request carried, else what was stored. The enable switch lives on
// the table row, not in the drawer, so an edit that never mentioned it must not
// turn a disabled subagent back on.
func subagentEnabledAfterUpdate(stored, in *bool) *bool {
	if in != nil {
		return subagentEnabledPointer(*in)
	}
	if stored != nil && !*stored {
		return config.BoolPtr(false)
	}
	return nil
}

// subagentOverrideIsEmpty reports whether a built-in's override entry says
// nothing: every field is at its default, so the entry can be dropped and the
// built-in keeps its own behaviour.
func subagentOverrideIsEmpty(entry config.SubagentConfig) bool {
	return strings.TrimSpace(entry.Model) == "" &&
		strings.TrimSpace(entry.ReasoningEffort) == "" &&
		strings.TrimSpace(entry.SystemPrompt) == "" &&
		subagentEnabled(entry)
}

// storeSubagentOverride puts a built-in's override entry in place, or removes
// it when nothing is left in it: an override that only repeats the default is a
// line of configuration nobody meant to write.
func storeSubagentOverride(def *config.AgentDefinition, entry config.SubagentConfig) {
	key := subagentKey(entry.Name)
	entry.Name = key
	i := subagentEntryIndex(def.Subagents, key)
	if subagentOverrideIsEmpty(entry) {
		if i >= 0 {
			def.Subagents = append(def.Subagents[:i], def.Subagents[i+1:]...)
		}
		return
	}
	if i >= 0 {
		def.Subagents[i] = entry
		return
	}
	def.Subagents = append(def.Subagents, entry)
}

// setSubagentRowEnabled writes one row's switch: a custom subagent carries its
// own, a built-in's lives in its override entry.
func setSubagentRowEnabled(def *config.AgentDefinition, name string, enabled bool) {
	key := subagentKey(name)
	entry := subagentEntry(*def, key)
	entry.Enabled = subagentEnabledPointer(enabled)
	if slices.Contains(agent.PublicSubagentNames, key) {
		storeSubagentOverride(def, entry)
		return
	}
	if i := subagentEntryIndex(def.Subagents, key); i >= 0 {
		def.Subagents[i] = entry
	}
}

// setSharedSubagentEnabled writes one shared subagent's switch for this agent
// (plan 314). Enabling stores the reference entry — the name and enabled: true,
// explicitly, because for a shared subagent an absent meaning "on" would turn
// every one of them on the moment the registry grew one — and disabling removes
// the entry, which is the same thing as absent.
func setSharedSubagentEnabled(def *config.AgentDefinition, name string, enabled bool) {
	key := subagentKey(name)
	index := subagentEntryIndex(def.Subagents, key)
	if !enabled {
		if index >= 0 {
			def.Subagents = append(def.Subagents[:index], def.Subagents[index+1:]...)
		}
		return
	}
	entry := config.SubagentConfig{Name: key, Enabled: config.BoolPtr(true)}
	if index >= 0 {
		def.Subagents[index] = entry
		return
	}
	def.Subagents = append(def.Subagents, entry)
}

// sharedSubagentEnabled reports whether this agent's reference entry switches
// one shared subagent on: only an explicit true does.
func sharedSubagentEnabled(def config.AgentDefinition, name string) bool {
	entry := subagentEntry(def, name)
	return entry.Enabled != nil && *entry.Enabled
}

// subagentAllRowsDisabled reports whether every row that has a switch — the
// built-ins the model may dispatch, this agent's own subagents, and the shared
// subagents it enabled — is off. This is the other half of the linkage rule:
// the agent's switch says the feature is on, and a list with nothing enabled
// says otherwise.
func subagentAllRowsDisabled(cfg *config.Root, def config.AgentDefinition) bool {
	for _, name := range agent.PublicSubagentNames {
		if subagentEnabled(subagentEntry(def, name)) {
			return false
		}
	}
	shared := config.SharedSubagentNames(sharedSubagentDefinitions(cfg))
	for _, entry := range def.Subagents {
		key := subagentKey(entry.Name)
		if slices.Contains(agent.PublicSubagentNames, key) {
			continue
		}
		if slices.Contains(shared, key) {
			// A reference entry's switch is read by its own rule: absent is off.
			if sharedSubagentEnabled(def, key) {
				return false
			}
			continue
		}
		if subagentEnabled(entry) {
			return false
		}
	}
	return true
}

// --- request plumbing -------------------------------------------------------

// subagentAgentID resolves the route's primary agent to an id that is in
// effect, the way every other per-agent route does.
func (s *Server) subagentAgentID(agentID string) (string, error) {
	if strings.TrimSpace(agentID) == "" {
		return "", subagentNotFound("no primary agent named in the route")
	}
	resolver, err := s.primaryAgentResolver()
	if err != nil {
		return "", err
	}
	sum, err := resolver.Resolve(agentID)
	if err != nil {
		return "", subagentNotFound("%v", err)
	}
	return strings.TrimSpace(sum.ID), nil
}

// subagentConfigFor resolves one read: the configuration as written and the
// primary agent the route names.
func (s *Server) subagentConfigFor(r *http.Request) (*config.Root, string, error) {
	id, err := s.subagentAgentID(pathParam(r, "id"))
	if err != nil {
		return nil, "", err
	}
	cfg, err := s.persistedConfig(r.Context())
	if err != nil {
		return nil, "", err
	}
	return &cfg, id, nil
}

// decodeSubagentBody reads one request body into out.
func decodeSubagentBody(r *http.Request, out any) error {
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(out); err != nil {
		return subagentBadRequest("the request body is not a subagent entry: %v", err)
	}
	return nil
}

// decodeSubagentSwitch reads a switch body: {"enabled": bool}. The field is
// required — a body that names nothing would otherwise read as "off".
func decodeSubagentSwitch(r *http.Request) (bool, error) {
	var in struct {
		Enabled *bool `json:"enabled"`
	}
	if err := decodeSubagentBody(r, &in); err != nil {
		return false, err
	}
	if in.Enabled == nil {
		return false, subagentBadRequest("enabled is required")
	}
	return *in.Enabled, nil
}

// subagentWrite is one configuration write in progress: the agents section as
// stored, the primary agent it edits and that agent's definition, which a
// handler changes and the writer stores back before validating and saving.
type subagentWrite struct {
	cfg     config.Root
	agentID string
	def     config.AgentDefinition
	// models is the persisted configuration the model references are resolved
	// against. The section callback's root carries the agents section alone, and
	// a subagent's model names a row of the install-wide table, which that root
	// does not have.
	models *config.Root
}

// store writes the handler's changes back into the configuration, so a mutation
// can be followed by a read of the field it changed.
func (w *subagentWrite) store() {
	w.cfg.Agents.Definitions[w.agentID] = w.def
}

// switchAfter is the agent's subagent switch in effect once this write lands.
func (w *subagentWrite) switchAfter() bool {
	w.store()
	return config.SubagentsEnabledFor(&w.cfg, w.agentID)
}

// writeAgentSubagents is the one write path behind every subagent route (plan
// 188 W7): one read-modify-write of the agents section in the state database,
// with the handler changing this agent's definition and the result checked the
// way the loader would. The store's transaction is the mutual exclusion the
// file lock used to provide.
//
// The rebind is not optional: a session's subagent types are decided when it
// starts, so the pooled runners are rebuilt before the answer goes out.
// Rebuilding the agent's runners is what "the next session uses it" means for
// a process that already has them.
func (s *Server) writeAgentSubagents(w http.ResponseWriter, r *http.Request, mutate func(*subagentWrite) (any, error)) {
	// Resolving the route's agent reads liveCfg (Env.Deps.Config()), which the
	// next turn's sync replaces — resolving it before the write is the same
	// snapshot the write itself works from.
	id, err := s.subagentAgentID(pathParam(r, "id"))
	if err != nil {
		failSubagentWrite(w, err)
		return
	}
	// Read before the write: the resolved references and the validator both need
	// the model table, which only a full persisted read carries.
	persisted, err := s.persistedConfig(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var payload any
	err = s.withConfigSection(r.Context(), "agents", func(cfg *config.Root) error {
		if cfg.Agents.Definitions == nil {
			cfg.Agents.Definitions = map[string]config.AgentDefinition{}
		}
		write := &subagentWrite{cfg: *cfg, agentID: id, def: cfg.Agents.Definitions[id], models: &persisted}
		var err error
		payload, err = mutate(write)
		if err != nil {
			return err
		}
		write.store()
		// The validator is the loader's own: a configuration the CLI would
		// reject must not reach the section, and its sentence names the entry
		// and the rule. A model without the legacy "provider/model" shape is a
		// model reference (a fb_llm_models row id, plan 262), and resolves
		// against the same root the write is landing in.
		if err := config.ValidateAgentSubagents(id, write.def, agent.PublicSubagentNames, agent.SystemSubagentNames, config.SharedSubagentNames(write.cfg.Agents.SharedSubagents), func(ref string) bool {
			_, ok := config.ResolveModelRef(write.models, id, ref)
			return ok
		}); err != nil {
			return subagentBadRequest("%v", err)
		}
		*cfg = write.cfg
		return nil
	})
	if err != nil {
		failSubagentWrite(w, err)
		return
	}
	s.rebindAgentRunners()
	writeAgentsJSON(w, payload)
}

// --- read endpoints ---------------------------------------------------------

// handleSubagentsDefaultGet serves GET /agents/defaults/subagents: the global
// switch the settings page owns.
func (s *Server) handleSubagentsDefaultGet(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.persistedConfig(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeAgentsJSON(w, map[string]any{"enabled": config.SubagentsEnabled(&cfg)})
}

// handleAgentSubagentsList serves GET /agents/primary/:id/subagents: one page of
// this agent's subagents, with the two switches the toolbar shows beside them.
func (s *Server) handleAgentSubagentsList(w http.ResponseWriter, r *http.Request) {
	cfg, agentID, err := s.subagentConfigFor(r)
	if err != nil {
		failSubagentWrite(w, err)
		return
	}
	q := readListQuery(r)
	kind := subagentKey(r.URL.Query().Get("kind"))
	switch kind {
	case "", subagentKindBuiltin, subagentKindCustom, subagentKindShared, subagentKindSystem:
	default:
		failSubagentWrite(w, subagentBadRequest("kind must be builtin, custom, shared or system, not %q", kind))
		return
	}
	extra := map[string]any{
		"switch": config.SubagentsEnabledFor(cfg, agentID),
		"global": config.SubagentsEnabled(cfg),
	}
	matched := make([]subagentRow, 0, len(cfg.Agents.Definitions[agentID].Subagents)+len(agent.PublicSubagentNames)+len(agent.SystemSubagentNames))
	for _, row := range subagentRows(cfg, agentID) {
		if kind != "" && row.Kind != kind {
			continue
		}
		if q.Q != "" && !subagentRowMatches(row, q.Q) {
			continue
		}
		matched = append(matched, row)
	}
	if !q.Paged {
		payload := make(map[string]any, len(extra)+1)
		for field, value := range extra {
			payload[field] = value
		}
		payload["rows"] = matched
		writeAgentsJSON(w, payload)
		return
	}
	page, served := pageOf(matched, q)
	writeListPage(w, "rows", page, len(matched), served, q, extra)
}

// handleAgentSubagentGet serves GET /agents/primary/:id/subagents/:name: one
// subagent with the prompt that will be injected and, where there is one, the
// harness's original.
func (s *Server) handleAgentSubagentGet(w http.ResponseWriter, r *http.Request) {
	cfg, agentID, err := s.subagentConfigFor(r)
	if err != nil {
		failSubagentWrite(w, err)
		return
	}
	name := subagentKey(pathParam(r, "name"))
	detail, found := subagentDetailFor(cfg, agentID, name)
	if !found {
		failSubagentWrite(w, subagentNotFound("no subagent named %q", name))
		return
	}
	writeAgentsJSON(w, detail)
}

// handleAgentModelOptions serves GET /agents/primary/:id/model-options: the
// models this agent may pin a subagent to, which are the same list the
// conversation's own model menu offers.
func (s *Server) handleAgentModelOptions(w http.ResponseWriter, r *http.Request) {
	id, err := s.subagentAgentID(pathParam(r, "id"))
	if err != nil {
		failSubagentWrite(w, err)
		return
	}
	cfg := s.liveCfg() // liveCfg: runtime snapshot (plan 217) — must not be persistedConfig
	if cfg == nil {
		http.Error(w, "no configuration is bound", http.StatusServiceUnavailable)
		return
	}
	writeAgentsJSON(w, map[string]any{"options": s.sessionModelOptions(cfg, id)})
}

// --- write endpoints --------------------------------------------------------

// handleSubagentsDefaultPut serves PUT /agents/defaults/subagents: the global
// switch. No agent's own switch moves: an agent that set one keeps it.
func (s *Server) handleSubagentsDefaultPut(w http.ResponseWriter, r *http.Request) {
	enabled, err := decodeSubagentSwitch(r)
	if err != nil {
		failSubagentWrite(w, err)
		return
	}
	err = s.withConfigSection(r.Context(), "agents", func(cfg *config.Root) error {
		cfg.Agents.Defaults.EnableSubagent = config.BoolPtr(enabled)
		return nil
	})
	if err != nil {
		failSubagentWrite(w, err)
		return
	}
	// Every agent that follows the global switch changed meaning, and a session
	// freezes its subagent types when it starts, so the pooled runners are
	// rebuilt before the answer goes out.
	s.rebindAgentRunners()
	writeAgentsJSON(w, map[string]any{"enabled": enabled})
}

// handleAgentSubagentCreate serves POST /agents/primary/:id/subagents: a new
// custom subagent, first in the list.
func (s *Server) handleAgentSubagentCreate(w http.ResponseWriter, r *http.Request) {
	var in config.SubagentConfig
	if err := decodeSubagentBody(r, &in); err != nil {
		failSubagentWrite(w, err)
		return
	}
	name := subagentKey(in.Name)
	if name == "" {
		failSubagentWrite(w, subagentBadRequest("a subagent needs a name"))
		return
	}
	s.writeAgentSubagents(w, r, func(write *subagentWrite) (any, error) {
		switch kind := subagentKindFor(&write.cfg, write.def, name); kind {
		case "":
		case subagentKindCustom:
		case subagentKindShared:
			// The registry owns the definition, so a per-agent subagent of the
			// same name would be a second one nobody can edit here (plan 314).
			return nil, subagentConflict("%q is a shared subagent: enable it here, or add a differently named one", name)
		default:
			return nil, subagentConflict("%q is a built-in subagent and cannot be redefined; override it or give yours another name", name)
		}
		if subagentEntryIndex(write.def.Subagents, name) >= 0 {
			return nil, subagentConflict("a subagent named %q is already configured for this agent", name)
		}
		// A new subagent goes to the front: the list is what the user just
		// changed, and the row they created is the row they want to look at.
		write.def.Subagents = append([]config.SubagentConfig{subagentEntryFrom(write, name, in, nil)}, write.def.Subagents...)
		return map[string]any{"name": name}, nil
	})
}

// subagentEntryFrom builds a custom entry from a request, with the route's name
// and the stored switch carried forward.
func subagentEntryFrom(write *subagentWrite, name string, in config.SubagentConfig, stored *bool) config.SubagentConfig {
	entry := config.SubagentConfig{
		Name:            subagentKey(name),
		Description:     strings.TrimSpace(in.Description),
		SystemPrompt:    strings.TrimSpace(in.SystemPrompt),
		Model:           subagentModelRef(write, in.Model),
		ReasoningEffort: strings.TrimSpace(in.ReasoningEffort),
		Tools:           strings.TrimSpace(in.Tools),
		Continuable:     in.Continuable,
		Enabled:         subagentEnabledAfterUpdate(stored, in.Enabled),
	}
	if entry.Tools == config.SubagentToolsPick {
		entry.AllowedTools = subagentAllowedTools(in.AllowedTools)
	}
	return entry
}

// subagentModelRef puts a subagent's model choice into the form the
// configuration stores: the id of the model row it names. A page that still
// sends a legacy "provider/model" label is translated, so the stored reference
// survives a row's endpoint changing; a value nothing carries is left as it is
// for the validator to refuse with its own sentence.
func subagentModelRef(write *subagentWrite, ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	if entry, found := config.ResolveModelRef(write.models, write.agentID, ref); found {
		return strings.TrimSpace(entry.ID)
	}
	return ref
}

// subagentAllowedTools normalises a chosen tool set: trimmed, de-duplicated, and
// without the subagent_* tools a subagent can neither call nor dispatch.
func subagentAllowedTools(in []string) []string {
	out := make([]string, 0, len(in))
	for _, name := range in {
		key := subagentKey(name)
		if key == "" || slices.Contains(out, key) {
			continue
		}
		out = append(out, key)
	}
	return out
}

// handleAgentSubagentUpdate serves PUT /agents/primary/:id/subagents/:name.
// A custom subagent is replaced whole; a built-in takes only the four settings
// it can be configured with; a shared subagent's definition is the registry's,
// so only its switch moves here — through the enabled endpoint — and a system
// subagent is refused.
func (s *Server) handleAgentSubagentUpdate(w http.ResponseWriter, r *http.Request) {
	name := subagentKey(pathParam(r, "name"))
	s.writeAgentSubagents(w, r, func(write *subagentWrite) (any, error) {
		switch subagentKindFor(&write.cfg, write.def, name) {
		case subagentKindCustom:
			var in config.SubagentConfig
			if err := decodeSubagentBody(r, &in); err != nil {
				return nil, err
			}
			// The name is the identity of the entry the route addressed; it
			// never moves here, and the stored switch stands unless the request
			// carried one.
			index := subagentEntryIndex(write.def.Subagents, name)
			write.def.Subagents[index] = subagentEntryFrom(write, name, in, write.def.Subagents[index].Enabled)
			return map[string]any{"name": name}, nil
		case subagentKindBuiltin:
			var in subagentBuiltinWrite
			if err := decodeSubagentBody(r, &in); err != nil {
				return nil, err
			}
			stored := subagentEntry(write.def, name)
			entry := config.SubagentConfig{
				Name:            name,
				Model:           subagentModelRef(write, in.Model),
				ReasoningEffort: strings.TrimSpace(in.ReasoningEffort),
				SystemPrompt:    strings.TrimSpace(in.SystemPrompt),
				Enabled:         subagentEnabledAfterUpdate(stored.Enabled, in.Enabled),
			}
			// "Restore the default" clears the rewrite and nothing else: the
			// entry keeps the model this agent pinned, if any.
			if in.ResetPrompt {
				entry.SystemPrompt = ""
			}
			storeSubagentOverride(&write.def, entry)
			return map[string]any{"name": name}, nil
		case subagentKindShared:
			// The definition is install-wide, so this page carries one fact
			// about it: whether this agent may dispatch it. The switch has its
			// own endpoint, and the web uses it (plan 314).
			return nil, subagentBadRequest("%q is a shared subagent; edit it in Settings → Subagents, only enable/disable here", name)
		case subagentKindSystem:
			return nil, subagentBadRequest("%q is dispatched by the system and cannot be configured", name)
		default:
			return nil, subagentNotFound("no subagent named %q", name)
		}
	})
}

// subagentBuiltinWrite is what an update of a built-in carries: the three
// settings this agent may pin, the "restore the default" flag for the prompt,
// and the row's switch when the form carried it.
type subagentBuiltinWrite struct {
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoning_effort"`
	SystemPrompt    string `json:"system_prompt"`
	ResetPrompt     bool   `json:"reset_prompt"`
	Enabled         *bool  `json:"enabled"`
}

// handleAgentSubagentDelete serves DELETE /agents/primary/:id/subagents/:name.
// Only a custom subagent can be deleted: a built-in is switched off instead, a
// shared subagent is deleted in the registry it belongs to, and a system
// subagent was never this agent's to begin with.
func (s *Server) handleAgentSubagentDelete(w http.ResponseWriter, r *http.Request) {
	name := subagentKey(pathParam(r, "name"))
	s.writeAgentSubagents(w, r, func(write *subagentWrite) (any, error) {
		switch subagentKindFor(&write.cfg, write.def, name) {
		case subagentKindCustom:
			index := subagentEntryIndex(write.def.Subagents, name)
			write.def.Subagents = append(write.def.Subagents[:index], write.def.Subagents[index+1:]...)
			return map[string]any{"name": name}, nil
		case subagentKindBuiltin:
			return nil, subagentBadRequest("%q is a built-in subagent: switch it off instead of deleting it", name)
		case subagentKindShared:
			return nil, subagentBadRequest("%q is a shared subagent; delete it in Settings → Subagents", name)
		case subagentKindSystem:
			return nil, subagentBadRequest("%q is dispatched by the system and cannot be deleted", name)
		default:
			return nil, subagentNotFound("no subagent named %q", name)
		}
	})
}

// handleAgentSubagentEnabled serves PUT /agents/primary/:id/subagents/:name/enabled:
// one row's switch, and the agent's own switch with it.
//
// The linkage is the owner's rule and lives here rather than in the page: a row
// switched on can only run with the feature on, so the agent's switch follows it
// on; and a list with every row switched off says the feature is off here, so
// the agent's switch follows it off. Doing this in the browser would leave the
// file describing one thing and the table another the first time a request was
// dropped.
func (s *Server) handleAgentSubagentEnabled(w http.ResponseWriter, r *http.Request) {
	enabled, err := decodeSubagentSwitch(r)
	if err != nil {
		failSubagentWrite(w, err)
		return
	}
	name := subagentKey(pathParam(r, "name"))
	s.writeAgentSubagents(w, r, func(write *subagentWrite) (any, error) {
		switch subagentKindFor(&write.cfg, write.def, name) {
		case subagentKindBuiltin, subagentKindCustom:
			setSubagentRowEnabled(&write.def, name, enabled)
		case subagentKindShared:
			// Absent is disabled, so switching one off removes the reference
			// entry rather than storing a false (plan 314).
			setSharedSubagentEnabled(&write.def, name, enabled)
		case subagentKindSystem:
			return nil, subagentBadRequest("%q is dispatched by the system and has no switch", name)
		default:
			return nil, subagentNotFound("no subagent named %q", name)
		}
		// The two directions of the linkage the doc above describes: the first
		// row switched on turns the feature on here, and the last row switched
		// off turns it off.
		switch {
		case enabled && !write.switchAfter():
			write.def.EnableSubagent = config.BoolPtr(true)
		case !enabled && subagentAllRowsDisabled(&write.cfg, write.def):
			write.def.EnableSubagent = config.BoolPtr(false)
		}
		return map[string]any{"switch": write.switchAfter()}, nil
	})
}

// handleAgentSubagentSwitch serves PUT /agents/primary/:id/subagent-switch: this
// agent's own switch, and every row with it — switching the feature off means
// the model dispatches none of them, and the table has to say so.
func (s *Server) handleAgentSubagentSwitch(w http.ResponseWriter, r *http.Request) {
	enabled, err := decodeSubagentSwitch(r)
	if err != nil {
		failSubagentWrite(w, err)
		return
	}
	s.writeAgentSubagents(w, r, func(write *subagentWrite) (any, error) {
		write.def.EnableSubagent = config.BoolPtr(enabled)
		// The custom names are collected first: setting a row may create or drop
		// an entry, and the list being walked must not move underneath it.
		shared := config.SharedSubagentNames(sharedSubagentDefinitions(&write.cfg))
		custom := make([]string, 0, len(write.def.Subagents))
		reference := make([]string, 0, len(write.def.Subagents))
		for _, entry := range write.def.Subagents {
			name := subagentKey(entry.Name)
			if slices.Contains(agent.PublicSubagentNames, name) {
				continue
			}
			if slices.Contains(shared, name) {
				reference = append(reference, name)
				continue
			}
			custom = append(custom, name)
		}
		for _, name := range agent.PublicSubagentNames {
			setSubagentRowEnabled(&write.def, name, enabled)
		}
		for _, name := range custom {
			setSubagentRowEnabled(&write.def, name, enabled)
		}
		// A shared subagent follows the same switch, but its own rule: enabled
		// stores the reference entry, disabled removes it (plan 314).
		for _, name := range reference {
			setSharedSubagentEnabled(&write.def, name, enabled)
		}
		return map[string]any{"switch": write.switchAfter()}, nil
	})
}

// --- the install-wide shared subagent registry (plan 314) -------------------
//
// One definition every primary agent may consume: the settings page creates,
// edits and deletes it through the routes below, and each agent's own page only
// switches it on or off. That split is the whole shape of this section — a write
// lands in agents.shared_subagents, never in any agent's entry, with the one
// exception of a delete, which also drops the reference entries that would
// otherwise name a shared subagent that no longer exists.
//
// It lives here rather than in a file of its own because it is the same HTTP
// family as the per-agent routes above: the same stored section, the same write
// path and the same validator (pkg/architecture keeps this package to 34 files).
// sharedTypeRow is one public built-in type's install-wide default model row
// (agents.subagent_models.<type>, plan 315), as the settings page edits it: the
// row id it names and the thinking level that overrides the row's own. Both
// empty is "no global default", which is how a fresh install reads.
type sharedTypeRow struct {
	Type            string `json:"type"`
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoning_effort"`
}

// sharedSubagentListing is the registry as the settings page reads it: the
// shared custom subagents in configuration order, and the four per-type global
// defaults beside them. The page renders both in one group (plan 314).
//
// A row is a subagentDetail rather than a plain row because the definition's
// prompt is what the page edits: a list without it would leave the form saving
// an empty prompt over a real one.
type sharedSubagentListing struct {
	Rows  []subagentDetail `json:"rows"`
	Types []sharedTypeRow  `json:"types"`
}

// handleSharedSubagentsList serves GET /agents/subagents/shared: the whole
// registry and the per-type rows.
func (s *Server) handleSharedSubagentsList(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.persistedConfig(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeAgentsJSON(w, sharedSubagentListingOf(&cfg))
}

// sharedSubagentListingOf renders the registry. A registry row carries no
// switch: enablement is per agent, so it is read on that agent's page and
// nowhere else (plan 314).
func sharedSubagentListingOf(cfg *config.Root) sharedSubagentListing {
	out := sharedSubagentListing{
		Rows:  make([]subagentDetail, 0, len(cfg.Agents.SharedSubagents)),
		Types: make([]sharedTypeRow, 0, len(agent.PublicSubagentNames)),
	}
	for _, entry := range cfg.Agents.SharedSubagents {
		row := sharedSubagentRow(cfg, subagentKey(entry.Name), config.SubagentConfig{Name: entry.Name})
		row.Enabled = nil
		// The definition's own prompt, and no original to restore to: a shared
		// subagent is a custom one, so it has no built-in text behind it.
		out.Rows = append(out.Rows, subagentDetail{subagentRow: row, SystemPrompt: strings.TrimSpace(entry.SystemPrompt)})
	}
	for _, name := range agent.PublicSubagentNames {
		row := cfg.Agents.SubagentModels[name]
		out.Types = append(out.Types, sharedTypeRow{
			Type:            name,
			Model:           strings.TrimSpace(row.Model),
			ReasoningEffort: strings.TrimSpace(row.ReasoningEffort),
		})
	}
	return out
}

// decodeSharedSubagentBody reads one registry write. enabled is refused rather
// than dropped: a shared subagent's enablement is per agent, so a client that
// sent one would otherwise be told it was stored.
func decodeSharedSubagentBody(r *http.Request) (config.SubagentConfig, error) {
	var in config.SubagentConfig
	if err := decodeSubagentBody(r, &in); err != nil {
		return in, err
	}
	if in.Enabled != nil {
		return in, subagentBadRequest("a shared subagent's enablement is per-agent; remove enabled")
	}
	return in, nil
}

// sharedSubagentEntry builds a registry definition from a request. The model is
// stored as the id of the row it names (plan 262's rule, kept here because a
// shared subagent's model resolves globally for every agent that enables it); a
// reference nothing carries is left as written for the validator to refuse with
// its own sentence. enabled is never stored, and the handler has already refused
// one that arrived.
func sharedSubagentEntry(models *config.Root, name string, in config.SubagentConfig) config.SubagentConfig {
	entry := config.SubagentConfig{
		Name:            subagentKey(name),
		Description:     strings.TrimSpace(in.Description),
		SystemPrompt:    strings.TrimSpace(in.SystemPrompt),
		ReasoningEffort: strings.TrimSpace(in.ReasoningEffort),
		Tools:           strings.TrimSpace(in.Tools),
		Continuable:     in.Continuable,
	}
	if ref := strings.TrimSpace(in.Model); ref != "" {
		if row, ok := config.ResolveModelRef(models, "main", ref); ok {
			entry.Model = strings.TrimSpace(row.ID)
		} else {
			entry.Model = ref
		}
	}
	if entry.Tools == config.SubagentToolsPick {
		entry.AllowedTools = subagentAllowedTools(in.AllowedTools)
	}
	return entry
}

// writeSharedSubagents is the one write path behind the registry routes: one
// read-modify-write of the agents section in the state database, with the
// loader's verdict on the configuration the change would produce.
//
// The verdict runs on a probe root that carries the model table: the section
// callback's root holds the agents section alone, and both a shared subagent's
// model and a per-type row are references into the install-wide table, which
// that root does not have (the same reason writeAgentSubagents reads the
// persisted configuration first).
func (s *Server) writeSharedSubagents(w http.ResponseWriter, r *http.Request, mutate func(cfg, models *config.Root) error) {
	persisted, err := s.persistedConfig(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	written := persisted
	err = s.withConfigSection(r.Context(), "agents", func(cfg *config.Root) error {
		if err := mutate(cfg, &persisted); err != nil {
			return err
		}
		probe := persisted
		probe.Agents = cfg.Agents
		if err := validateLoadedRootForAgents(&probe); err != nil {
			return subagentBadRequest("%v", err)
		}
		written = probe
		return nil
	})
	if err != nil {
		failSubagentWrite(w, err)
		return
	}
	// A session freezes its subagent types when it starts, so the pooled runners
	// are rebuilt before the answer goes out: a definition added or removed here
	// is what the next session may dispatch.
	s.rebindAgentRunners()
	writeAgentsJSON(w, sharedSubagentListingOf(&written))
}

// sharedSubagentIndex finds one registry entry by name.
func sharedSubagentIndex(entries []config.SubagentConfig, name string) int {
	key := subagentKey(name)
	for i, entry := range entries {
		if subagentKey(entry.Name) == key {
			return i
		}
	}
	return -1
}

// handleSharedSubagentCreate serves POST /agents/subagents/shared: a new shared
// custom subagent, appended in configuration order.
func (s *Server) handleSharedSubagentCreate(w http.ResponseWriter, r *http.Request) {
	in, err := decodeSharedSubagentBody(r)
	if err != nil {
		failSubagentWrite(w, err)
		return
	}
	name := subagentKey(in.Name)
	if name == "" {
		failSubagentWrite(w, subagentBadRequest("a shared subagent needs a name"))
		return
	}
	s.writeSharedSubagents(w, r, func(cfg, models *config.Root) error {
		// The names the loader refuses are the two built-in lists: the four the
		// model may dispatch and the six the system does — the latter includes
		// the system subagent types that are not dispatchable definitions at all
		// (plan-reviewer, goal-evaluator).
		if config.IsBuiltInSubagentDefinition(name) || slices.Contains(agent.SystemSubagentNames, name) {
			return subagentConflict("%q is a built-in subagent type and cannot be a shared custom subagent", name)
		}
		if sharedSubagentIndex(cfg.Agents.SharedSubagents, name) >= 0 {
			return subagentConflict("a shared subagent named %q already exists", name)
		}
		cfg.Agents.SharedSubagents = append(cfg.Agents.SharedSubagents, sharedSubagentEntry(models, name, in))
		return nil
	})
}

// handleSharedSubagentUpdate serves PUT /agents/subagents/shared/:name: the
// definition is replaced whole. The name is the entry's identity and comes from
// the route, so an update never renames.
func (s *Server) handleSharedSubagentUpdate(w http.ResponseWriter, r *http.Request) {
	name := subagentKey(pathParam(r, "name"))
	in, err := decodeSharedSubagentBody(r)
	if err != nil {
		failSubagentWrite(w, err)
		return
	}
	s.writeSharedSubagents(w, r, func(cfg, models *config.Root) error {
		index := sharedSubagentIndex(cfg.Agents.SharedSubagents, name)
		if index < 0 {
			return subagentNotFound("no shared subagent named %q", name)
		}
		cfg.Agents.SharedSubagents[index] = sharedSubagentEntry(models, name, in)
		return nil
	})
}

// handleSharedSubagentDelete serves DELETE /agents/subagents/shared/:name.
//
// The delete is the one cross-agent write in this file: every primary agent's
// reference entry for the name goes with the definition. A reference must name a
// shared subagent (the loader refuses one that does not), so leaving the
// references behind would leave an install that cannot start — and the removal
// lands in the same agents-section transaction as the definition itself.
func (s *Server) handleSharedSubagentDelete(w http.ResponseWriter, r *http.Request) {
	name := subagentKey(pathParam(r, "name"))
	s.writeSharedSubagents(w, r, func(cfg, _ *config.Root) error {
		index := sharedSubagentIndex(cfg.Agents.SharedSubagents, name)
		if index < 0 {
			return subagentNotFound("no shared subagent named %q", name)
		}
		cfg.Agents.SharedSubagents = append(cfg.Agents.SharedSubagents[:index], cfg.Agents.SharedSubagents[index+1:]...)
		for id, def := range cfg.Agents.Definitions {
			kept := make([]config.SubagentConfig, 0, len(def.Subagents))
			for _, entry := range def.Subagents {
				if subagentKey(entry.Name) != name {
					kept = append(kept, entry)
				}
			}
			if len(kept) == len(def.Subagents) {
				continue
			}
			def.Subagents = kept
			cfg.Agents.Definitions[id] = def
		}
		return nil
	})
}

// handleSharedSubagentTypeUpdate serves PUT /agents/subagents/shared/types/:type:
// one public built-in type's install-wide default model row (plan 315). The
// model is stored as the id of the row it names — the only form that resolves
// for every agent — and an empty model with an empty level removes the row,
// which is what "no global default" means.
func (s *Server) handleSharedSubagentTypeUpdate(w http.ResponseWriter, r *http.Request) {
	typeName := subagentKey(pathParam(r, "type"))
	if !slices.Contains(agent.PublicSubagentNames, typeName) {
		failSubagentWrite(w, subagentBadRequest("only a built-in subagent has an install-wide model row (%s)", strings.Join(agent.PublicSubagentNames, ", ")))
		return
	}
	var in struct {
		Model           string `json:"model"`
		ReasoningEffort string `json:"reasoning_effort"`
	}
	if err := decodeSubagentBody(r, &in); err != nil {
		failSubagentWrite(w, err)
		return
	}
	s.writeSharedSubagents(w, r, func(cfg, models *config.Root) error {
		row := config.SubagentConfig{ReasoningEffort: strings.TrimSpace(in.ReasoningEffort)}
		if ref := strings.TrimSpace(in.Model); ref != "" {
			resolved, ok := config.ResolveModelRef(models, "main", ref)
			if !ok {
				return subagentBadRequest("the model %q names no configured model row", ref)
			}
			row.Model = strings.TrimSpace(resolved.ID)
		}
		if row.Model == "" && row.ReasoningEffort == "" {
			delete(cfg.Agents.SubagentModels, typeName)
			return nil
		}
		if cfg.Agents.SubagentModels == nil {
			cfg.Agents.SubagentModels = map[string]config.SubagentConfig{}
		}
		cfg.Agents.SubagentModels[typeName] = row
		return nil
	})
}
