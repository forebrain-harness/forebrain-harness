// The one-time move of the install-wide shared subagent registry into the
// agent definitions (P3).
package process

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/state/dbx"
)

// subagentMergeBackupSection is the inert fb_config row holding the agents
// section as it stood before the merge: rollback is copying it back. An unknown
// section is never composed (rootConfigSections), so nothing reads it, and it is
// written once — an install that already carries one keeps it.
const subagentMergeBackupSection = "agents.pre-merge"

// preMergeAgentsSection is the agents section as a pre-merge install stored it:
// the configuration's own section, plus the shared-subagent registry key the
// configuration no longer has a field for. Written back without the registry, it
// is exactly the section the configuration reads.
type preMergeAgentsSection struct {
	appcfg.AgentsSection
	// SharedSubagents is the retired install-wide custom subagent registry (plan
	// 314): enablement was per agent — a reference entry in that agent's
	// Subagents — so the entries carry no Enabled of their own.
	SharedSubagents []appcfg.SubagentConfig `json:"shared_subagents,omitempty"`
}

// migrateSharedSubagents folds agents.shared_subagents into
// agents.definitions as subagent-role definitions (P3 ruling #6): every shared
// custom subagent becomes the definition of that subagent, keeping the order
// the registry configured it in through Order. It is a content migration, not a
// schema one: the gate is the registry's own content, so an install that has
// none — a fresh one, or one this has already run for — is a no-op.
//
// Nothing is lost or renamed. A name a definition already carries is a
// collision: the registry entry is skipped so the definition keeps winning, and
// both sides are reported. The pre-merge section is written verbatim into
// subagentMergeBackupSection in the same transaction as the merge, so an
// install can be restored by copying that row back.
//
// It runs before migrateLegacyLLMModels on purpose: a merged definition's model
// reference is rewritten into a model row id by that pass, which is the same
// treatment every other stored reference gets.
func migrateSharedSubagents(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("no state database")
	}
	// One process migrates at a time, the way the model migration does: two
	// replicas sharing a database would otherwise both read the registry and
	// race to merge it.
	release, err := dbx.AcquireMigrationLock(ctx, db)
	if err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer release()

	store := &state.ConfigStore{DB: db}
	sections, err := store.LoadSections(ctx)
	if err != nil {
		return fmt.Errorf("read stored configuration: %w", err)
	}
	if len(sections["agents"]) == 0 {
		return nil
	}
	var stored preMergeAgentsSection
	if err := json.Unmarshal(sections["agents"], &stored); err != nil {
		return fmt.Errorf("read agents section: %w", err)
	}
	if len(stored.SharedSubagents) == 0 {
		return nil
	}
	backupKept := len(sections[subagentMergeBackupSection]) > 0

	var report subagentMergeReport
	err = store.UpdateSections(ctx, []string{"agents", subagentMergeBackupSection}, func(cur map[string]json.RawMessage) (map[string]json.RawMessage, error) {
		raw := cur["agents"]
		if len(raw) == 0 {
			// The registry this pass was gated on is gone by the time the
			// transaction read it. Nothing to merge, and nothing to back up: a
			// backup of an absent section would be a lie about what it holds.
			return map[string]json.RawMessage{}, nil
		}
		var agents preMergeAgentsSection
		if err := json.Unmarshal(raw, &agents); err != nil {
			return nil, fmt.Errorf("read agents section: %w", err)
		}
		report = mergeSharedSubagents(&agents)
		out, err := json.Marshal(agents)
		if err != nil {
			return nil, fmt.Errorf("write agents section: %w", err)
		}
		next := map[string]json.RawMessage{"agents": out}
		if len(cur[subagentMergeBackupSection]) == 0 {
			// Verbatim: the row holds the section as it was stored, not a
			// re-encoding of it, so a rollback restores exactly what was there.
			next[subagentMergeBackupSection] = raw
		}
		return next, nil
	})
	if err != nil {
		return fmt.Errorf("merge shared subagents into definitions: %w", err)
	}
	if len(report.merged) == 0 && len(report.collisions) == 0 && !report.changedShells() {
		return nil
	}
	slog.Info("merged shared subagents into definitions",
		"entries", len(report.merged), "skipped", len(report.collisions),
		"backup_section", subagentMergeBackupSection, "backup_kept", backupKept)
	for _, name := range report.collisions {
		slog.Warn("a subagent definition already carries a shared subagent's name; the shared entry was not merged",
			"name", name, "kept", "definition")
	}
	for _, name := range report.movedShellModels {
		slog.Info("moved a built-in type's default model from its definition shell into its install-wide row", "type", name)
	}
	for _, name := range report.keptShells {
		slog.Info("kept a built-in type's definition entry for the presentation it carries", "type", name)
	}
	for _, name := range report.droppedShells {
		slog.Info("dropped an empty built-in type definition shell", "type", name)
	}
	return nil
}

// subagentMergeReport is what the merge did, for the log lines that name it.
type subagentMergeReport struct {
	// merged are the registry entries that became definitions, in registry
	// order; collisions are the ones a definition already carried the name of.
	merged     []string
	collisions []string
	// A built-in type's definition key is a shell: the migration moves the
	// model it carries into that type's install-wide row, keeps its
	// presentation, and drops it only when it holds nothing at all.
	movedShellModels []string
	keptShells       []string
	droppedShells    []string
}

// changedShells reports whether the shell pass touched anything.
func (r subagentMergeReport) changedShells() bool {
	return len(r.movedShellModels) > 0 || len(r.droppedShells) > 0
}

// mergeSharedSubagents folds the registry into the definitions map and tidies
// the built-in type keys, reporting what it did. It is the one implementation
// of the merge: the migration runs it inside the store's transaction, against
// the section as stored at that moment.
func mergeSharedSubagents(agents *preMergeAgentsSection) subagentMergeReport {
	report := subagentMergeReport{}
	if agents == nil {
		return report
	}
	if len(agents.SharedSubagents) > 0 && agents.Definitions == nil {
		agents.Definitions = make(map[string]appcfg.AgentDefinition, len(agents.SharedSubagents))
	}
	for i, entry := range agents.SharedSubagents {
		name := strings.ToLower(strings.TrimSpace(entry.Name))
		if name == "" {
			// A nameless entry has no key to merge under, so it cannot become a
			// definition. The registry's own validator never let one be stored;
			// a hand-written row is reported rather than silently dropped.
			slog.Warn("a stored shared subagent has no name; it was not merged", "index", i)
			continue
		}
		if _, exists := agents.Definitions[name]; exists {
			report.collisions = append(report.collisions, name)
			continue
		}
		agents.Definitions[name] = appcfg.AgentDefinition{
			// A definition whose key plays the subagent role: every primary
			// agent's reference entry names it to enable it.
			Primary: false,
			// Order keeps the registry's place, one-based: 0 is "unordered" (a
			// definition nobody positioned), so the first entry carries 1 and
			// the list reads back in the order the registry had.
			Order:           i + 1,
			Description:     entry.Description,
			SystemPrompt:    entry.SystemPrompt,
			Model:           entry.Model,
			ReasoningEffort: entry.ReasoningEffort,
			Tools:           entry.Tools,
			AllowedTools:    entry.AllowedTools,
			Continuable:     entry.Continuable,
		}
		report.merged = append(report.merged, name)
	}
	agents.SharedSubagents = nil
	report.movedShellModels, report.keptShells, report.droppedShells = mergeDefinitionShells(agents)
	return report
}

// mergeDefinitionShells tidies the built-in type keys a definitions map may
// carry (P3 ruling #7: the shells are checked, never blindly dropped). A
// built-in key is not a definition — IsEffectivePrimaryAgent never treats one as
// a primary agent, and the entry under it exists for the model it names:
//
//   - a non-empty default_model moves into agents.subagent_models.<type> when
//     that row names no model of its own, which is the live fallback the shell
//     supplied (ownModelChoice); both set is reported and left as it is, because
//     choosing between two configured models would be inventing a rule;
//   - a display name or a description is the shell's own text, so it keeps the
//     entry and is reported;
//   - only an entry holding nothing at all is dropped.
func mergeDefinitionShells(agents *preMergeAgentsSection) (moved, kept, dropped []string) {
	if agents == nil || len(agents.Definitions) == 0 {
		return nil, nil, nil
	}
	shells := make([]string, 0, len(agents.Definitions))
	for id := range agents.Definitions {
		if appcfg.IsReservedDefinitionID(id) {
			shells = append(shells, id)
		}
	}
	sort.Strings(shells)
	for _, name := range shells {
		def := agents.Definitions[name]
		movedHere := false
		if ref := strings.TrimSpace(def.DefaultModel); ref != "" {
			row := agents.SubagentModels[name]
			if strings.TrimSpace(row.Model) == "" {
				row.Model = ref
				if agents.SubagentModels == nil {
					agents.SubagentModels = map[string]appcfg.SubagentConfig{}
				}
				agents.SubagentModels[name] = row
				def.DefaultModel = ""
				movedHere = true
				moved = append(moved, name)
			} else {
				slog.Warn("a built-in type's definition shell and its install-wide row both name a model; the row is in force and both were kept",
					"type", name, "shell", ref, "row", strings.TrimSpace(row.Model))
			}
		}
		if strings.TrimSpace(def.DisplayName) != "" || strings.TrimSpace(def.Description) != "" {
			agents.Definitions[name] = def
			kept = append(kept, name)
			continue
		}
		if agentDefinitionIsEmpty(def) {
			delete(agents.Definitions, name)
			dropped = append(dropped, name)
			continue
		}
		if movedHere {
			agents.Definitions[name] = def
		}
	}
	return moved, kept, dropped
}

// agentDefinitionIsEmpty reports whether a definitions entry holds nothing but
// the role flag: it is the shape a person never wrote, and the only shape the
// migration may drop.
func agentDefinitionIsEmpty(def appcfg.AgentDefinition) bool {
	return strings.TrimSpace(def.DisplayName) == "" &&
		strings.TrimSpace(def.Description) == "" &&
		len(def.MCPServers) == 0 &&
		strings.TrimSpace(def.DefaultModel) == "" &&
		def.EnableSubagent == nil &&
		len(def.Subagents) == 0 &&
		strings.TrimSpace(def.Model) == "" &&
		strings.TrimSpace(def.ReasoningEffort) == "" &&
		strings.TrimSpace(def.SystemPrompt) == "" &&
		strings.TrimSpace(def.Tools) == "" &&
		len(def.AllowedTools) == 0 &&
		def.Continuable == nil &&
		def.Order == 0
}
