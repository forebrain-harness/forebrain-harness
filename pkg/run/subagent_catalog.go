// Subagent catalog: one primary agent's view of the subagent types.
package run

import (
	"fmt"
	"slices"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// SubagentSourceCustom is the agent.Definition.Source of a subagent this
// primary agent defines itself, as opposed to "built-in".
const SubagentSourceCustom = tool.SubagentDefinitionSourceCustom

// publicSubagentEnumSentences is the one-sentence description each public
// built-in carries in the subagent_type schema: what the model reads when it
// chooses a type.
var publicSubagentEnumSentences = map[string]string{
	"general-purpose": "bounded independent implementation or general delegated work",
	"explore":         "read-only investigation and locating facts",
	"plan":            "independent read-only critique of a design or plan, and read-only implementation design proposals",
	"verification":    "falsify or verify an implementation with concrete checks",
}

// SubagentCatalog is one primary agent's view of the subagent types: the
// built-ins it keeps enabled and its overrides of them, and its own custom
// subagents.
//
// It is the whole of what makes subagents per-agent. The definitions
// themselves stay static in pkg/agent — a system subagent is the same
// everywhere — and this is the layer that says which of them this agent's model
// may dispatch, under which prompt and with which model.
type SubagentCatalog struct {
	// cfg and agentName are the configuration this catalog was read from and
	// the primary agent that owns it: a configured subagent's model is a
	// reference into that agent's model view, and only the pair resolves it.
	cfg       *appcfg.Root
	agentName string
	// entries are this agent's configured subagents by name: overrides of the
	// public built-ins and its own custom subagents alike.
	entries map[string]appcfg.SubagentConfig
	// customs are the custom entries in configuration order, which is the order
	// the subagent page lists them in.
	customs []appcfg.SubagentConfig
	// projectType is the conversation type this catalog was narrowed to with
	// ForProjectType; "" means the coding default.
	projectType string
	// goalEvaluator is the settings page's global row for the /goal evaluator (plan
	// 299). It is kept out of entries on purpose: entries is what ResolvePublic
	// and ToolPolicy read as "this agent configures that type", and the goal
	// check must stay undispatchable from a primary agent.
	goalEvaluator appcfg.SubagentConfig
}

// SubagentCatalogFor reads one primary agent's subagent configuration. An
// unknown or unnamed agent has none: the catalog then offers exactly the
// built-in types, all of them enabled.
//
// The install-wide subagent definitions (agents.definitions, a key playing the
// subagent role) are merged in here rather than stored per agent (plan 314, P3):
// an agent's entry naming one is its enable marker — the definition is the
// install's — and such a subagent runs nowhere until that marker says enabled:
// true. That is the opposite of a per-agent custom subagent's absent-means-on
// switch, which is why the two are read by different rules rather than one. An
// entry of that name carrying a body of its own is a custom subagent instead
// (pkg/config's ValidateAgentSubagents draws the same line), and the agent's own
// definition wins at dispatch the way an override of a built-in does.
func SubagentCatalogFor(cfg *appcfg.Root, agentName string) SubagentCatalog {
	catalog := SubagentCatalog{cfg: cfg, agentName: agentName, entries: map[string]appcfg.SubagentConfig{}}
	if cfg == nil {
		return catalog
	}
	// The goal evaluator's row is global, so it is carried before — and
	// independently of — the per-agent definitions below: a primary agent with
	// no entry of its own (a fresh install's shape) still sees it.
	catalog.goalEvaluator = cfg.Agents.GoalEvaluator
	def, ok := cfg.Agents.Definitions[subagentKey(agentName)]
	if !ok {
		return catalog
	}
	shared := appcfg.SubagentDefinitionNames(cfg.Agents.Definitions)
	enabledShared := make(map[string]struct{}, len(shared))
	for _, entry := range def.Subagents {
		entry.Name = subagentKey(entry.Name)
		if slices.Contains(shared, entry.Name) && !appcfg.SubagentEntryCarriesBody(entry) {
			// A reference entry is the enable marker, never a definition.
			if entry.Enabled != nil && *entry.Enabled {
				enabledShared[entry.Name] = struct{}{}
			}
			continue
		}
		catalog.entries[entry.Name] = entry
		if !isPublicSubagentName(entry.Name) {
			catalog.customs = append(catalog.customs, entry)
		}
	}
	// The definitions' own order decides where the merged ones sit: after this
	// agent's own subagents, and only the ones it enabled. The entry carries no
	// Enabled of its own: the marker above was the switch, and
	// subagentConfigEnabled reads absent as on.
	for _, name := range shared {
		if _, on := enabledShared[name]; !on {
			continue
		}
		if _, own := catalog.entries[name]; own {
			continue
		}
		entry, found := appcfg.SubagentDefinitionFor(cfg.Agents.Definitions, name)
		if !found {
			continue
		}
		catalog.entries[entry.Name] = entry
		catalog.customs = append(catalog.customs, entry)
	}
	return catalog
}

// ForProjectType is this catalog as a conversation of the given type sees
// it: office and batch conversations dispatch only the built-ins whose
// work is not about code (agent.OfficeSubagentNames), under their office
// wording, plus the agent's own custom subagents.
func (c SubagentCatalog) ForProjectType(projectType string) SubagentCatalog {
	c.projectType = state.NormalizeProjectType(projectType)
	return c
}

// office reports whether this catalog was narrowed to an office or batch
// conversation, the two types that share the not-about-code built-in set.
func (c SubagentCatalog) office() bool {
	return c.projectType == state.ProjectTypeOffice || c.projectType == state.ProjectTypeBatch
}

// Public lists what the model may dispatch: enabled built-ins (in
// agent.PublicSubagentNames order), then enabled custom subagents (in config
// order).
func (c SubagentCatalog) Public() []agent.Definition {
	out := make([]agent.Definition, 0, len(agent.PublicSubagentNames)+len(c.customs))
	for _, name := range agent.PublicSubagentNames {
		if !c.builtinEnabled(name) {
			continue
		}
		// The name comes from the list the resolution walks, so the definition
		// is always there.
		def, _ := agent.ResolvePublicSubtype(name)
		out = append(out, def)
	}
	for _, entry := range c.customs {
		if subagentConfigEnabled(entry) {
			out = append(out, c.customDefinition(entry))
		}
	}
	return out
}

// ResolvePublic is ResolvePublicSubtype for this agent: a built-in this agent
// disabled and a custom subagent it disabled are both refused with the reason,
// and an unknown name answers with the names that are available.
func (c SubagentCatalog) ResolvePublic(name string) (agent.Definition, error) {
	key := subagentKey(name)
	if key == "" {
		return agent.Definition{}, fmt.Errorf("subagent_type required")
	}
	if isPublicSubagentName(key) {
		if !c.builtinEnabled(key) {
			if c.office() {
				return agent.Definition{}, fmt.Errorf("subagent_type %q is not available in %s projects", key, c.projectType)
			}
			return agent.Definition{}, fmt.Errorf("subagent_type %q is disabled for this agent", key)
		}
		return agent.ResolvePublicSubtype(key)
	}
	entry, ok := c.entries[key]
	if !ok {
		return agent.Definition{}, fmt.Errorf("unknown subagent_type %q; available: %s", name, strings.Join(c.publicNames(), ", "))
	}
	if !subagentConfigEnabled(entry) {
		return agent.Definition{}, fmt.Errorf("subagent_type %q is disabled for this agent", key)
	}
	return c.customDefinition(entry), nil
}

// Prompt is the system prompt a typed run of name uses: a custom subagent's
// own, this agent's rewrite of a built-in, else the built-in's. A built-in
// with a project-type wording (office and batch workers, the coding plan
// reviewer) reads that wording.
func (c SubagentCatalog) Prompt(name string) (string, bool) {
	// A public built-in's entry is a model-and-level pin (P5): it carries no
	// prompt of its own, and the custom ones beside it in this map do — so
	// the read skips the built-ins rather than the whole map.
	if entry, ok := c.entries[subagentKey(name)]; ok && !isPublicSubagentName(entry.Name) {
		if prompt := strings.TrimSpace(entry.SystemPrompt); prompt != "" {
			return prompt, true
		}
	}
	if prompt, ok := agent.ProjectTypeSubagentPrompt(name, state.NormalizeProjectType(c.projectType)); ok {
		return prompt, true
	}
	return agent.SystemPromptForSubtype(name)
}

// Model is the model this agent pins name to; ok false when it follows the
// conversation. The configured value is a model reference — a configured row
// id, or the legacy "provider/model" label — so a value that names no
// configured model pins nothing: the type follows the conversation rather than
// pinning a run to a model this agent cannot build a client for. The id comes
// back with the pair, because the same pair may be configured more than once.
//
// The /goal evaluator is the one type whose row does not come from this agent's
// entries but from the settings page's global field (plan 299): its model is
// the same for every primary agent, and no primary agent configures it.
func (c SubagentCatalog) Model(name string) (modelID, provider, model, effort string, ok bool) {
	entry, found := c.entries[subagentKey(name)]
	if !found && subagentKey(name) == GoalEvaluatorSubagentType {
		entry, found = c.goalEvaluator, true
	}
	if !found {
		return "", "", "", "", false
	}
	configured := strings.TrimSpace(entry.Model)
	if configured == "" {
		return "", "", "", "", false
	}
	resolved, found := appcfg.ResolveModelRef(c.cfg, c.agentName, configured)
	if !found {
		return "", "", "", "", false
	}
	return strings.TrimSpace(resolved.ID),
		strings.TrimSpace(resolved.Provider),
		strings.TrimSpace(resolved.Model),
		strings.TrimSpace(entry.ReasoningEffort), true
}

// ToolPolicy is a custom subagent's tool policy; ok false for built-ins (their
// policy stays the static table).
func (c SubagentCatalog) ToolPolicy(name string) (tool.SubagentToolPolicy, bool) {
	entry, ok := c.entries[subagentKey(name)]
	if !ok || isPublicSubagentName(entry.Name) {
		return tool.SubagentToolPolicy{}, false
	}
	mode := strings.TrimSpace(entry.Tools)
	if mode == "" {
		// Empty is Edit (owner 2026-11-18): a custom subagent may change the
		// workspace unless it is told otherwise. An explicit read_only stays
		// read-only — the choice, not the absence of one, is what restricts.
		mode = appcfg.SubagentToolsEdit
	}
	policy := tool.SubagentToolPolicy{Mode: mode}
	if mode == appcfg.SubagentToolsPick {
		policy.Allowed = make(map[string]struct{}, len(entry.AllowedTools))
		for _, allowed := range entry.AllowedTools {
			if key := subagentKey(allowed); key != "" {
				policy.Allowed[key] = struct{}{}
			}
		}
	}
	return policy, true
}

// EnumDescription is the subagent_type schema description: the built-in
// sentences for the enabled built-ins, then "<name>: <description>." for each
// enabled custom one, then how to omit the field. Office and batch read their
// own built-in sentences.
func (c SubagentCatalog) EnumDescription() string {
	var b strings.Builder
	b.WriteString("Optional public typed subagent.")
	for _, name := range agent.PublicSubagentNames {
		if !c.builtinEnabled(name) {
			continue
		}
		sentence := publicSubagentEnumSentences[name]
		switch c.projectType {
		case state.ProjectTypeOffice:
			sentence = agent.OfficeSubagentEnumSentences[name]
		case state.ProjectTypeBatch:
			sentence = agent.BatchSubagentEnumSentences[name]
		}
		fmt.Fprintf(&b, " %s: %s.", name, sentence)
	}
	for _, entry := range c.customs {
		if !subagentConfigEnabled(entry) {
			continue
		}
		// A description is optional (P5): a subagent without one is
		// dispatched by its name alone, so the sentence names the name and
		// nothing else rather than reading "name: ."
		if description := strings.TrimSpace(entry.Description); description != "" {
			fmt.Fprintf(&b, " %s: %s.", entry.Name, description)
		} else {
			fmt.Fprintf(&b, " %s.", entry.Name)
		}
	}
	b.WriteString(" Omit subagent_type to fork the current agent. Fork inherits the parent conversation context, runs in the background, and keeps intermediate tool output out of the main context.")
	return b.String()
}

// publicNames lists the dispatchable names in Public() order, for the error
// that answers an unknown subagent_type.
func (c SubagentCatalog) publicNames() []string {
	defs := c.Public()
	names := make([]string, 0, len(defs))
	for _, def := range defs {
		names = append(names, def.Name)
	}
	return names
}

// configures reports whether this agent's configuration names name at all,
// enabled or not. A subagent the agent disabled still has a definition to say
// it is disabled; one it deleted has nothing, and callers that hold a record
// need to tell those two apart to describe them truthfully.
func (c SubagentCatalog) configures(name string) bool {
	key := subagentKey(name)
	if key == "" {
		return false
	}
	if isPublicSubagentName(key) {
		return true
	}
	_, ok := c.entries[key]
	return ok
}

func (c SubagentCatalog) builtinEnabled(name string) bool {
	// A built-in is always on (P5): the entry's switch is retired, so the
	// only gate left is office's — a batch project runs the office workers
	// alone.
	if c.office() && !slices.Contains(agent.OfficeSubagentNames, name) {
		return false
	}
	return true
}

// customDefinition is the agent.Definition a custom subagent runs under. Its
// prompt is the configured one: a custom subagent has no built-in text to fall
// back to.
func (c SubagentCatalog) customDefinition(entry appcfg.SubagentConfig) agent.Definition {
	continuable := entry.Continuable == nil || *entry.Continuable
	return agent.Definition{
		Name:         entry.Name,
		Description:  strings.TrimSpace(entry.Description),
		Source:       SubagentSourceCustom,
		OneShot:      !continuable,
		Continuable:  continuable,
		SystemPrompt: strings.TrimSpace(entry.SystemPrompt),
	}
}

// subagentKey is the spelling subagent names are compared in: trimmed and
// lower-case, the same normalisation agent.ResolveSubtype applies.
func subagentKey(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func isPublicSubagentName(name string) bool {
	for _, public := range agent.PublicSubagentNames {
		if subagentKey(name) == public {
			return true
		}
	}
	return false
}

// subagentConfigEnabled reports whether an entry may be dispatched. Absent
// means enabled: a configuration written before the switch existed describes
// subagents that all run.
func subagentConfigEnabled(entry appcfg.SubagentConfig) bool {
	return entry.Enabled == nil || *entry.Enabled
}
