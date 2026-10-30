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
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// The three kinds a subagent row can be.
const (
	// subagentKindBuiltin is a built-in the model may dispatch: this agent may
	// override its model, its reasoning effort and its prompt, and may switch
	// it off.
	subagentKindBuiltin = "builtin"
	// subagentKindCustom is a subagent this primary agent defines itself.
	subagentKindCustom = "custom"
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
	// follows the conversation's model.
	ModelProvider   string `json:"model_provider"`
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoning_effort"`
	// ModelSource says where the model above came from: "agent" for this
	// agent's own subagent configuration, "global" for the legacy per-type
	// agents.definitions.<type>.llm_providers entry, empty for the
	// conversation's model.
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
// custom subagents in configuration order, then the built-ins the model may
// dispatch, then the ones the system dispatches.
func subagentNames(def config.AgentDefinition) []string {
	out := make([]string, 0, len(def.Subagents)+len(agent.PublicSubagentNames)+len(agent.SystemSubagentNames))
	for _, entry := range def.Subagents {
		name := subagentKey(entry.Name)
		if slices.Contains(agent.PublicSubagentNames, name) || slices.Contains(agent.SystemSubagentNames, name) {
			continue
		}
		out = append(out, name)
	}
	out = append(out, agent.PublicSubagentNames...)
	return append(out, agent.SystemSubagentNames...)
}

// subagentRows renders the page's rows in subagentNames order.
func subagentRows(cfg *config.Root, agentID string) []subagentRow {
	def := cfg.Agents.Definitions[agentID]
	names := subagentNames(def)
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
// entry, this agent's override of a built-in, or the built-in definition.
func subagentRowFor(cfg *config.Root, agentID, name string) (subagentRow, bool) {
	def := cfg.Agents.Definitions[agentID]
	key := subagentKey(name)
	switch subagentKindFor(def, key) {
	case subagentKindSystem:
		return systemSubagentRow(key), true
	case subagentKindBuiltin:
		return builtinSubagentRow(cfg, key, subagentEntry(def, key)), true
	case subagentKindCustom:
		return customSubagentRow(cfg, key, subagentEntry(def, key)), true
	}
	return subagentRow{}, false
}

// builtinSubagentRow describes a built-in the model may dispatch. Everything
// but the model, the effort, the prompt and the switch is the definition's:
// a built-in's description and tools are its type, not its configuration.
func builtinSubagentRow(cfg *config.Root, name string, entry config.SubagentConfig) subagentRow {
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
	row.ModelProvider, row.Model, row.ReasoningEffort, row.ModelSource = subagentRowModel(cfg, name, entry)
	return row
}

// customSubagentRow describes one of this agent's own subagents.
func customSubagentRow(cfg *config.Root, name string, entry config.SubagentConfig) subagentRow {
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
	row.ModelProvider, row.Model, row.ReasoningEffort, row.ModelSource = subagentRowModel(cfg, name, entry)
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
// one.
func systemSubagentRow(name string) subagentRow {
	// The name comes from the list this walks, so the definition is there.
	def, _ := agent.ResolveSubtype(name)
	return subagentRow{
		Name:         name,
		Kind:         subagentKindSystem,
		Description:  strings.TrimSpace(def.Description),
		Tools:        tool.BuiltinSubagentToolsTier(name),
		AllowedTools: []string{},
		Continuable:  def.Continuable,
	}
}

// subagentRowModel resolves the model a subagent runs on, in the order
// dispatch uses it: this agent's own entry for it, then — for a built-in the
// model may dispatch — the legacy per-type definition
// agents.definitions.<type>.llm_providers, and otherwise the conversation's
// model. pkg/run's SubagentOwnModel is the same order; a row that described a
// different one would be telling the user the wrong thing about what will run.
func subagentRowModel(cfg *config.Root, name string, entry config.SubagentConfig) (provider, model, effort, source string) {
	if configured := strings.TrimSpace(entry.Model); configured != "" {
		provider, model, _ = strings.Cut(configured, "/")
		return strings.TrimSpace(provider), strings.TrimSpace(model), strings.TrimSpace(entry.ReasoningEffort), subagentModelFromAgent
	}
	if !slices.Contains(agent.PublicSubagentNames, name) {
		return "", "", "", ""
	}
	def, defined := cfg.Agents.Definitions[name]
	if !defined {
		return "", "", "", ""
	}
	// The same resolution run.SubagentOwnModel answers with: the effective
	// default (default_model when set, otherwise the first entry), never a
	// bare first-entry read.
	defEntry := config.EffectiveDefaultLLM(def)
	if defEntry == nil {
		return "", "", "", ""
	}
	return strings.TrimSpace(defEntry.Provider),
		strings.TrimSpace(defEntry.Model),
		config.ReasoningEffort(defEntry.Params),
		subagentModelFromGlobal
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
	// rewrite of a built-in, else the built-in's.
	if prompt, found := run.SubagentCatalogFor(cfg, agentID).Prompt(row.Name); found {
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

// subagentKindFor names what a subagent name is for this agent.
func subagentKindFor(def config.AgentDefinition, name string) string {
	key := subagentKey(name)
	switch {
	case slices.Contains(agent.SystemSubagentNames, key):
		return subagentKindSystem
	case slices.Contains(agent.PublicSubagentNames, key):
		return subagentKindBuiltin
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

// subagentAllRowsDisabled reports whether every row that has a switch — the
// built-ins the model may dispatch and this agent's own subagents — is off.
// This is the other half of the linkage rule: the agent's switch says the
// feature is on, and a list with nothing enabled says otherwise.
func subagentAllRowsDisabled(def config.AgentDefinition) bool {
	for _, name := range agent.PublicSubagentNames {
		if subagentEnabled(subagentEntry(def, name)) {
			return false
		}
	}
	for _, entry := range def.Subagents {
		if slices.Contains(agent.PublicSubagentNames, subagentKey(entry.Name)) {
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
	// Resolving the route's agent reads liveCfg (Env.Deps.AppCfg), which the
	// next turn's sync replaces — resolving it before the write is the same
	// snapshot the write itself works from.
	id, err := s.subagentAgentID(pathParam(r, "id"))
	if err != nil {
		failSubagentWrite(w, err)
		return
	}
	var payload any
	err = s.withConfigSection(r.Context(), "agents", func(cfg *config.Root) error {
		if cfg.Agents.Definitions == nil {
			cfg.Agents.Definitions = map[string]config.AgentDefinition{}
		}
		write := &subagentWrite{cfg: *cfg, agentID: id, def: cfg.Agents.Definitions[id]}
		var err error
		payload, err = mutate(write)
		if err != nil {
			return err
		}
		write.store()
		// The validator is the loader's own: a configuration the CLI would
		// reject must not reach the section, and its sentence names the entry
		// and the rule.
		if err := config.ValidateAgentSubagents(id, write.def, agent.PublicSubagentNames, agent.SystemSubagentNames); err != nil {
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
	case "", subagentKindBuiltin, subagentKindCustom, subagentKindSystem:
	default:
		failSubagentWrite(w, subagentBadRequest("kind must be builtin, custom or system, not %q", kind))
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
	cfg := s.liveCfg()
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
		if kind := subagentKindFor(write.def, name); kind != "" && kind != subagentKindCustom {
			return nil, subagentConflict("%q is a built-in subagent and cannot be redefined; override it or give yours another name", name)
		}
		if subagentEntryIndex(write.def.Subagents, name) >= 0 {
			return nil, subagentConflict("a subagent named %q is already configured for this agent", name)
		}
		// A new subagent goes to the front: the list is what the user just
		// changed, and the row they created is the row they want to look at.
		write.def.Subagents = append([]config.SubagentConfig{subagentEntryFrom(name, in, nil)}, write.def.Subagents...)
		return map[string]any{"name": name}, nil
	})
}

// subagentEntryFrom builds a custom entry from a request, with the route's name
// and the stored switch carried forward.
func subagentEntryFrom(name string, in config.SubagentConfig, stored *bool) config.SubagentConfig {
	entry := config.SubagentConfig{
		Name:            subagentKey(name),
		Description:     strings.TrimSpace(in.Description),
		SystemPrompt:    strings.TrimSpace(in.SystemPrompt),
		Model:           strings.TrimSpace(in.Model),
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
// it can be configured with; a system subagent is refused.
func (s *Server) handleAgentSubagentUpdate(w http.ResponseWriter, r *http.Request) {
	name := subagentKey(pathParam(r, "name"))
	s.writeAgentSubagents(w, r, func(write *subagentWrite) (any, error) {
		switch subagentKindFor(write.def, name) {
		case subagentKindCustom:
			var in config.SubagentConfig
			if err := decodeSubagentBody(r, &in); err != nil {
				return nil, err
			}
			// The name is the identity of the entry the route addressed; it
			// never moves here, and the stored switch stands unless the request
			// carried one.
			index := subagentEntryIndex(write.def.Subagents, name)
			write.def.Subagents[index] = subagentEntryFrom(name, in, write.def.Subagents[index].Enabled)
			return map[string]any{"name": name}, nil
		case subagentKindBuiltin:
			var in subagentBuiltinWrite
			if err := decodeSubagentBody(r, &in); err != nil {
				return nil, err
			}
			stored := subagentEntry(write.def, name)
			entry := config.SubagentConfig{
				Name:            name,
				Model:           strings.TrimSpace(in.Model),
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
// Only a custom subagent can be deleted: a built-in is switched off instead, and
// a system subagent was never this agent's to begin with.
func (s *Server) handleAgentSubagentDelete(w http.ResponseWriter, r *http.Request) {
	name := subagentKey(pathParam(r, "name"))
	s.writeAgentSubagents(w, r, func(write *subagentWrite) (any, error) {
		switch subagentKindFor(write.def, name) {
		case subagentKindCustom:
			index := subagentEntryIndex(write.def.Subagents, name)
			write.def.Subagents = append(write.def.Subagents[:index], write.def.Subagents[index+1:]...)
			return map[string]any{"name": name}, nil
		case subagentKindBuiltin:
			return nil, subagentBadRequest("%q is a built-in subagent: switch it off instead of deleting it", name)
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
		switch subagentKindFor(write.def, name) {
		case subagentKindBuiltin, subagentKindCustom:
			setSubagentRowEnabled(&write.def, name, enabled)
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
		case !enabled && subagentAllRowsDisabled(write.def):
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
		custom := make([]string, 0, len(write.def.Subagents))
		for _, entry := range write.def.Subagents {
			if name := subagentKey(entry.Name); !slices.Contains(agent.PublicSubagentNames, name) {
				custom = append(custom, name)
			}
		}
		for _, name := range agent.PublicSubagentNames {
			setSubagentRowEnabled(&write.def, name, enabled)
		}
		for _, name := range custom {
			setSubagentRowEnabled(&write.def, name, enabled)
		}
		return map[string]any{"switch": write.switchAfter()}, nil
	})
}
