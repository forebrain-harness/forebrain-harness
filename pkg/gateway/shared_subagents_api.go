// The install-wide shared subagent registry from the web surface (plan 314).
//
// One definition every primary agent may consume: the settings page creates,
// edits and deletes it here, and each agent's own page only switches it on or
// off (subagents_api.go). That split is the whole shape of this file — a write
// lands in agents.shared_subagents, never in any agent's entry, with the one
// exception of a delete, which also drops the reference entries that would
// otherwise name a shared subagent that no longer exists.
//
// It sits beside subagents_api.go rather than inside it because its route group
// is a different one: /agents/subagents/shared is install-wide, while
// /agents/primary/:id/subagents belongs to one agent.
package gateway

import (
	"net/http"
	"slices"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	"github.com/forebrain-harness/forebrain-harness/pkg/config"
)

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
