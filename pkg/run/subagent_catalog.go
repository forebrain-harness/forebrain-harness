// Subagent catalog: one primary agent's view of the subagent types.
package run

import (
	"fmt"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
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
	"plan":            "read-only implementation design, sequencing, risks, and verification planning",
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
	// entries are this agent's configured subagents by name: overrides of the
	// public built-ins and its own custom subagents alike.
	entries map[string]appcfg.SubagentConfig
	// customs are the custom entries in configuration order, which is the order
	// the subagent page lists them in.
	customs []appcfg.SubagentConfig
}

// SubagentCatalogFor reads one primary agent's subagent configuration. An
// unknown or unnamed agent has none: the catalog then offers exactly the
// built-in types, all of them enabled.
func SubagentCatalogFor(cfg *appcfg.Root, agentName string) SubagentCatalog {
	catalog := SubagentCatalog{entries: map[string]appcfg.SubagentConfig{}}
	if cfg == nil {
		return catalog
	}
	def, ok := cfg.Agents.Definitions[subagentKey(agentName)]
	if !ok {
		return catalog
	}
	for _, entry := range def.Subagents {
		entry.Name = subagentKey(entry.Name)
		catalog.entries[entry.Name] = entry
		if !isPublicSubagentName(entry.Name) {
			catalog.customs = append(catalog.customs, entry)
		}
	}
	return catalog
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
// own, this agent's rewrite of a built-in, else the built-in's.
func (c SubagentCatalog) Prompt(name string) (string, bool) {
	if entry, ok := c.entries[subagentKey(name)]; ok {
		if prompt := strings.TrimSpace(entry.SystemPrompt); prompt != "" {
			return prompt, true
		}
	}
	return agent.SystemPromptForSubtype(name)
}

// Model is the model this agent pins name to; ok false when it follows the
// conversation.
func (c SubagentCatalog) Model(name string) (provider, model, effort string, ok bool) {
	entry, found := c.entries[subagentKey(name)]
	if !found {
		return "", "", "", false
	}
	configured := strings.TrimSpace(entry.Model)
	if configured == "" {
		return "", "", "", false
	}
	provider, model, _ = strings.Cut(configured, "/")
	return provider, model, strings.TrimSpace(entry.ReasoningEffort), true
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
		mode = appcfg.SubagentToolsReadOnly
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
// enabled custom one, then how to omit the field.
func (c SubagentCatalog) EnumDescription() string {
	var b strings.Builder
	b.WriteString("Optional public typed subagent.")
	for _, name := range agent.PublicSubagentNames {
		if !c.builtinEnabled(name) {
			continue
		}
		fmt.Fprintf(&b, " %s: %s.", name, publicSubagentEnumSentences[name])
	}
	for _, entry := range c.customs {
		if !subagentConfigEnabled(entry) {
			continue
		}
		fmt.Fprintf(&b, " %s: %s.", entry.Name, strings.TrimSpace(entry.Description))
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
	entry, ok := c.entries[name]
	return !ok || subagentConfigEnabled(entry)
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
