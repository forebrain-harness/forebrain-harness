package process

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/stretchr/testify/require"
)

// migratedAgentsForTest reads the stored agents section as the configuration
// reads it, plus the raw document the assertion about a key's disappearance
// needs.
func migratedAgentsForTest(t *testing.T, env *Environment, ctx context.Context) (appcfg.AgentsSection, map[string]any) {
	t.Helper()
	raw := storedSectionForTest(t, env, ctx, "agents")
	var section appcfg.AgentsSection
	require.NoError(t, json.Unmarshal([]byte(raw), &section))
	var doc map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &doc))
	return section, doc
}

// The merge itself (P3 ruling #6): every shared custom subagent becomes the
// definition of that subagent, in the order the registry listed it, with the
// body it carried and nothing invented; the registry key is gone from the stored
// section, and the pre-merge section is kept verbatim in the inert backup row.
func TestSubagentMigrationMergesTheRegistryInOrder(t *testing.T) {
	const registry = `{
		"definitions":{"main":{"primary":true}},
		"shared_subagents":[
			{"name":"note-taker","description":"writes one note","system_prompt":"You take notes.","tools":"edit"},
			{"name":"repo-auditor","description":"audits one repository area","system_prompt":"You audit repositories.","model":"deepseek/deepseek-v4-flash","reasoning_effort":"high","tools":"pick","allowed_tools":["read_file","code_search"],"continuable":false}
		]
	}`
	env, ctx := migrateEnvForTest(t, registry, "")

	require.NoError(t, migrateSharedSubagents(ctx, env.SQL))

	section, doc := migratedAgentsForTest(t, env, ctx)
	if _, stale := doc["shared_subagents"]; stale {
		t.Fatalf("the stored section still carries shared_subagents: %v", doc["shared_subagents"])
	}
	for _, name := range []string{"note-taker", "repo-auditor"} {
		def, ok := section.Definitions[name]
		require.True(t, ok, "definition %q was not created", name)
		require.False(t, def.Primary, "a merged definition plays the subagent role")
	}
	require.Equal(t, 1, section.Definitions["note-taker"].Order)
	require.Equal(t, 2, section.Definitions["repo-auditor"].Order)
	require.Equal(t, "You take notes.", section.Definitions["note-taker"].SystemPrompt)
	require.Equal(t, appcfg.SubagentToolsEdit, section.Definitions["note-taker"].Tools)

	auditor := section.Definitions["repo-auditor"]
	require.Equal(t, "audits one repository area", auditor.Description)
	require.Equal(t, "You audit repositories.", auditor.SystemPrompt)
	require.Equal(t, "deepseek/deepseek-v4-flash", auditor.Model)
	require.Equal(t, "high", auditor.ReasoningEffort)
	require.Equal(t, appcfg.SubagentToolsPick, auditor.Tools)
	require.Equal(t, []string{"read_file", "code_search"}, auditor.AllowedTools)
	require.NotNil(t, auditor.Continuable)
	require.False(t, *auditor.Continuable)

	// The order the registry had is the order the definitions list in.
	require.Equal(t, []string{"note-taker", "repo-auditor"}, appcfg.SubagentDefinitionNames(section.Definitions))

	// The backup is the pre-merge section verbatim, in the inert section name.
	require.Equal(t, registry, strings.TrimSpace(storedSectionForTest(t, env, ctx, subagentMergeBackupSection)))
}

// The built-in type keys are shells, and the migration checks them instead of
// dropping them (P3 ruling #7): a shell's default_model is the type's live
// install-wide model, so it moves into that type's row when the row names none;
// a display string keeps the entry; a row that already names a model wins and
// both are kept; only an entry holding nothing at all is dropped.
func TestSubagentMigrationChecksTheBuiltinShells(t *testing.T) {
	const agents = `{
		"definitions":{
			"main":{"primary":true},
			"explore":{"primary":true,"default_model":"openai/gpt-x","description":"searches the tree"},
			"plan":{"primary":true},
			"verification":{"primary":true,"default_model":"openai/gpt-shell"}
		},
		"subagent_models":{"verification":{"model":"openai/gpt-row"}},
		"shared_subagents":[{"name":"repo-auditor","description":"d","system_prompt":"You audit repositories."}]
	}`
	env, ctx := migrateEnvForTest(t, agents, "")

	require.NoError(t, migrateSharedSubagents(ctx, env.SQL))

	section, _ := migratedAgentsForTest(t, env, ctx)
	// The model moved out of the shell into the type's install-wide row...
	require.Equal(t, "openai/gpt-x", section.SubagentModels["explore"].Model)
	// ...and the presentation it carried kept the entry.
	require.Empty(t, section.Definitions["explore"].DefaultModel)
	require.Equal(t, "searches the tree", section.Definitions["explore"].Description)

	// A shell that held nothing is dropped.
	_, kept := section.Definitions["plan"]
	require.False(t, kept, "an empty built-in type shell must not survive")

	// Both set: the row is in force and neither side is destroyed.
	require.Equal(t, "openai/gpt-shell", section.Definitions["verification"].DefaultModel)
	require.Equal(t, "openai/gpt-row", section.SubagentModels["verification"].Model)

	// The registry entry the pass was gated on still merged.
	_, merged := section.Definitions["repo-auditor"]
	require.True(t, merged)
}

// A registry entry whose name a definition already carries is skipped: the
// definition is in force and wins, nothing is renamed, and the other entries
// merge as usual (P3 ruling #8).
func TestSubagentMigrationSkipsACollision(t *testing.T) {
	const agents = `{
		"definitions":{"main":{"primary":true},"repo-auditor":{"primary":true,"display_name":"Repo auditor"}},
		"shared_subagents":[
			{"name":"repo-auditor","description":"the registry's","system_prompt":"You audit repositories."},
			{"name":"note-taker","description":"writes one note","system_prompt":"You take notes."}
		]
	}`
	env, ctx := migrateEnvForTest(t, agents, "")

	require.NoError(t, migrateSharedSubagents(ctx, env.SQL))

	section, doc := migratedAgentsForTest(t, env, ctx)
	collided := section.Definitions["repo-auditor"]
	require.True(t, collided.Primary, "the definition the key already carried kept its role")
	require.Equal(t, "Repo auditor", collided.DisplayName)
	require.Empty(t, collided.SystemPrompt, "the registry's body was not merged over the definition")
	require.Empty(t, collided.Order)
	_, merged := section.Definitions["note-taker"]
	require.True(t, merged)
	require.NotContains(t, doc, "shared_subagents")
}

// The migration is gated on the registry's content, so a second start is a
// no-op: the section stamp does not move, and the backup row is never rewritten
// — including on an install that already carries one.
func TestSubagentMigrationIsIdempotent(t *testing.T) {
	const agents = `{
		"definitions":{"main":{"primary":true}},
		"shared_subagents":[{"name":"note-taker","description":"writes one note","system_prompt":"You take notes."}]
	}`
	env, ctx := migrateEnvForTest(t, agents, "")

	require.NoError(t, migrateSharedSubagents(ctx, env.SQL))
	stamp := storedSectionStampForTest(t, env, ctx, "agents")
	backup := storedSectionForTest(t, env, ctx, subagentMergeBackupSection)

	require.NoError(t, migrateSharedSubagents(ctx, env.SQL))
	require.Equal(t, stamp, storedSectionStampForTest(t, env, ctx, "agents"), "a second run rewrote the agents section")
	require.Equal(t, backup, storedSectionForTest(t, env, ctx, subagentMergeBackupSection))

	// An install that already carries a backup keeps it, whatever the registry
	// says: the row is the state before a merge that already happened.
	env2, ctx2 := migrateEnvForTest(t, agents, "")
	storeSectionForTest(t, env2, ctx2, subagentMergeBackupSection, `{"definitions":{"older":"state"}}`)
	require.NoError(t, migrateSharedSubagents(ctx2, env2.SQL))
	require.Equal(t, `{"definitions":{"older":"state"}}`, storedSectionForTest(t, env2, ctx2, subagentMergeBackupSection))
	section, _ := migratedAgentsForTest(t, env2, ctx2)
	_, merged := section.Definitions["note-taker"]
	require.True(t, merged, "the merge still ran; only the backup was kept")
}

// A configuration with no registry has nothing to move, and the migration
// writes nothing at all — no merged definitions and no backup row.
func TestSubagentMigrationDoesNothingWithoutARegistry(t *testing.T) {
	env, ctx := migrateEnvForTest(t, `{"definitions":{"main":{"primary":true}}}`, "")

	require.NoError(t, migrateSharedSubagents(ctx, env.SQL))

	var backup string
	err := env.SQL.QueryRowContext(ctx, `SELECT value_json FROM fb_config WHERE section = ?`, subagentMergeBackupSection).Scan(&backup)
	require.Error(t, err, "a no-op migration wrote a backup row")
	require.Equal(t, `{"definitions":{"main":{"primary":true}}}`, storedSectionForTest(t, env, ctx, "agents"))
}

// The startup path: Open runs the merge before anything composes the
// configuration, so the definitions a primary agent's reference entry names are
// there for the first turn of the process that finds the registry. The state
// database is planted the way a DB-era install holds it — the file carries only
// its startup sections — and the backup row lands beside it in the same start.
func TestSubagentMigrationRunsAtStartup(t *testing.T) {
	home := importHomeForTest(t)
	cfgPath := filepath.Join(home, "forebrain.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte("gateway:\n  http_addr: 127.0.0.1:6060\n"), 0o600))

	const stored = `{"definitions":{"main":{"primary":true}},"shared_subagents":[{"name":"repo-auditor","description":"audits one repository area","system_prompt":"You audit repositories."}]}`
	ctx := context.Background()
	// No validator on a plant this direct: the point is the bytes an older start
	// left, exactly as the migration has to read them.
	state.SetSectionValidator(nil)
	db, err := state.Open(ctx, filepath.Join(home, "state", "forebrain.state.sqlite"), nil)
	require.NoError(t, err)
	store := &state.ConfigStore{DB: db}
	require.NoError(t, store.UpdateSection(ctx, "agents", func(json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(stored), nil
	}))
	require.NoError(t, db.Close())

	env := importOpenForTest(t, home)

	var raw string
	require.NoError(t, env.SQL.QueryRowContext(ctx,
		`SELECT value_json FROM fb_config WHERE section = ?`, "agents").Scan(&raw))
	var section appcfg.AgentsSection
	require.NoError(t, json.Unmarshal([]byte(raw), &section))
	def, ok := section.Definitions["repo-auditor"]
	require.True(t, ok, "the startup merge did not land the definition: %s", raw)
	require.False(t, def.Primary)
	require.Equal(t, "You audit repositories.", def.SystemPrompt)
	require.NotContains(t, raw, "shared_subagents", "the merged section still carries the retired key")

	// The backup is the section as it stood when the merge ran: the startup's own
	// defaults top-up (which runs before the merge) has already been written into
	// it, and it still carries the registry the merge consumed.
	var backup string
	require.NoError(t, env.SQL.QueryRowContext(ctx,
		`SELECT value_json FROM fb_config WHERE section = ?`, subagentMergeBackupSection).Scan(&backup))
	var backupDoc preMergeAgentsSection
	require.NoError(t, json.Unmarshal([]byte(backup), &backupDoc))
	require.Len(t, backupDoc.SharedSubagents, 1)
	require.Equal(t, "repo-auditor", backupDoc.SharedSubagents[0].Name)
	require.Contains(t, backupDoc.Definitions, "main")
}
