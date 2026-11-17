package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/stretchr/testify/require"
)

// sharedTypeRowByName finds one built-in type's default row in the listing.
func sharedTypeRowByName(t *testing.T, rows []sharedTypeRow, name string) sharedTypeRow {
	t.Helper()
	for _, row := range rows {
		if row.Type == name {
			return row
		}
	}
	t.Fatalf("no per-type row named %q in %+v", name, rows)
	return sharedTypeRow{}
}

// decodeSharedRegistry reads one registry answer: the rows the settings page
// renders and the per-type default rows beside them.
func decodeSharedRegistry(t *testing.T, rec *httptest.ResponseRecorder) sharedSubagentListing {
	t.Helper()
	var listing sharedSubagentListing
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &listing))
	return listing
}

// TestSharedSubagentCreatePersistsARowModel pins the create (plan 314): one
// definition lands in agents.shared_subagents, its model is stored as the id of
// the row it named — the form that resolves for every agent that enables it —
// and the answer is the registry the page then renders.
func TestSharedSubagentCreatePersistsARowModel(t *testing.T) {
	f := newSubagentRootFixture(t, nil)
	rowID := seedGlobalModelRow(t, f, "deepseek", "deepseek-v4-flash")

	rec := f.do(t, http.MethodPost, "/api/agents/subagents/shared", map[string]any{
		"name":             "repo-auditor",
		"description":      "audits one repository area",
		"system_prompt":    "You audit repositories.",
		"model":            rowID,
		"reasoning_effort": "high",
		"tools":            "pick",
		"allowed_tools":    []string{"read_file", "code_search"},
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	stored := f.config(t).Agents.SharedSubagents
	require.Len(t, stored, 1)
	require.Equal(t, "repo-auditor", stored[0].Name)
	require.Equal(t, rowID, stored[0].Model, "the model is the row id, not the label it was sent as")
	require.Equal(t, "high", stored[0].ReasoningEffort)
	require.Equal(t, appcfg.SubagentToolsPick, stored[0].Tools)
	require.Equal(t, []string{"read_file", "code_search"}, stored[0].AllowedTools)
	require.Nil(t, stored[0].Enabled, "a shared definition carries no switch of its own")

	listing := decodeSharedRegistry(t, rec)
	require.Len(t, listing.Rows, 1)
	require.Equal(t, subagentKindShared, listing.Rows[0].Kind)
	require.Equal(t, "audits one repository area", listing.Rows[0].Description)
	require.Nil(t, listing.Rows[0].Enabled, "the registry list carries no per-agent switch")
	// The four public types are listed beside the registry, each with the row it
	// names — none, on a fresh install.
	require.Len(t, listing.Types, 4)
	for _, row := range listing.Types {
		require.NotEmpty(t, row.Type)
		require.Empty(t, row.Model)
	}

	// A legacy "provider/model" label is translated to the row it names, so the
	// stored reference survives an endpoint change.
	rec = f.do(t, http.MethodPut, "/api/agents/subagents/shared/repo-auditor", map[string]any{
		"description":   "audits one repository area",
		"system_prompt": "You audit repositories.",
		"model":         "deepseek/deepseek-v4-flash",
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, rowID, f.config(t).Agents.SharedSubagents[0].Model)
	require.Empty(t, f.config(t).Agents.SharedSubagents[0].Tools, "the update replaces the definition whole")
}

// TestSharedSubagentCreateRefusals pins what a create may not do (plan 314): a
// built-in type's name belongs to that type, a duplicate is one definition too
// many, the definition needs its description and its prompt, and enabled is the
// agent's business rather than the registry's.
func TestSharedSubagentCreateRefusals(t *testing.T) {
	f := newSubagentRootFixture(t, func(root *appcfg.Root) {
		root.Agents.SharedSubagents = []appcfg.SubagentConfig{{
			Name: "repo-auditor", Description: "audits one repository area", SystemPrompt: "You audit repositories.",
		}}
	})
	cases := []struct {
		name   string
		body   map[string]any
		status int
		want   string
	}{
		{
			name:   "a built-in type name",
			body:   map[string]any{"name": "explore", "description": "d", "system_prompt": "p"},
			status: http.StatusConflict,
			want:   `"explore" is a built-in subagent type and cannot be a shared custom subagent`,
		},
		{
			name:   "a system type name",
			body:   map[string]any{"name": "plan-reviewer", "description": "d", "system_prompt": "p"},
			status: http.StatusConflict,
			want:   `"plan-reviewer" is a built-in subagent type`,
		},
		{
			name:   "a duplicate",
			body:   map[string]any{"name": "repo-auditor", "description": "d", "system_prompt": "p"},
			status: http.StatusConflict,
			want:   `a shared subagent named "repo-auditor" already exists`,
		},
		{
			name:   "no description",
			body:   map[string]any{"name": "repo-scout", "system_prompt": "p"},
			status: http.StatusBadRequest,
			want:   "description required",
		},
		{
			name:   "no system prompt",
			body:   map[string]any{"name": "repo-scout", "description": "d"},
			status: http.StatusBadRequest,
			want:   "system_prompt required",
		},
		{
			name:   "a name the loader rejects",
			body:   map[string]any{"name": "repo_scout", "description": "d", "system_prompt": "p"},
			status: http.StatusBadRequest,
			want:   "invalid name",
		},
		{
			name:   "enabled",
			body:   map[string]any{"name": "repo-scout", "description": "d", "system_prompt": "p", "enabled": true},
			status: http.StatusBadRequest,
			want:   "a shared subagent's enablement is per-agent; remove enabled",
		},
		{
			name:   "a model that names no row",
			body:   map[string]any{"name": "repo-scout", "description": "d", "system_prompt": "p", "model": "glm-5.3-flash"},
			status: http.StatusBadRequest,
			want:   `model "glm-5.3-flash" must be provider/model`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := f.do(t, http.MethodPost, "/api/agents/subagents/shared", tc.body)
			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			require.Contains(t, rec.Body.String(), tc.want)
		})
	}
	// Every refusal left the registry as it was.
	require.Len(t, f.config(t).Agents.SharedSubagents, 1)
	require.Equal(t, "repo-auditor", f.config(t).Agents.SharedSubagents[0].Name)
}

// TestSharedSubagentDeleteDropsEveryReference pins the one cross-agent write
// (plan 314): a deleted definition takes every primary agent's reference entry
// with it, because a reference must name a shared subagent and one left behind
// would make the configuration unloadable.
func TestSharedSubagentDeleteDropsEveryReference(t *testing.T) {
	f := newSubagentRootFixture(t, func(root *appcfg.Root) {
		root.Agents.SharedSubagents = []appcfg.SubagentConfig{
			{Name: "repo-auditor", Description: "audits one repository area", SystemPrompt: "You audit repositories."},
			{Name: "note-taker", Description: "writes one note", SystemPrompt: "You take notes."},
		}
		// Two agents, both with a reference entry for the definition under test
		// plus one of their own subagents that must survive the delete.
		for _, id := range []string{"main", "helper"} {
			root.Agents.Definitions[id] = appcfg.AgentDefinition{
				Primary: id == "main", DefaultModel: "llm-1",
				Subagents: []appcfg.SubagentConfig{
					{Name: "repo-auditor", Enabled: appcfg.BoolPtr(true)},
					{Name: "note-taker", Enabled: appcfg.BoolPtr(true)},
					{Name: "db-migrator", Description: "runs migrations", SystemPrompt: "You migrate."},
				},
			}
		}
	})

	rec := f.do(t, http.MethodDelete, "/api/agents/subagents/shared/repo-auditor", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	cfg := f.config(t)
	require.Len(t, cfg.Agents.SharedSubagents, 1)
	require.Equal(t, "note-taker", cfg.Agents.SharedSubagents[0].Name)
	for _, id := range []string{"main", "helper"} {
		names := make([]string, 0, len(cfg.Agents.Definitions[id].Subagents))
		for _, entry := range cfg.Agents.Definitions[id].Subagents {
			names = append(names, entry.Name)
		}
		require.Equal(t, []string{"note-taker", "db-migrator"}, names, "agent %q keeps its other entries", id)
	}
	// The deleted name no longer answers, and a second delete is a 404.
	rec = f.do(t, http.MethodDelete, "/api/agents/subagents/shared/repo-auditor", nil)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	// The agent page no longer lists it.
	rec = f.do(t, http.MethodGet, "/api/agents/primary/main/subagents", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	for _, row := range decodeSubagentPage(t, rec).Rows {
		require.NotEqual(t, "repo-auditor", row.Name)
	}
}

// TestSharedSubagentListKeepsConfigOrder pins the list's order (plan 314): the
// registry in configuration order, whatever order the settings page wrote it in,
// with each row's model named rather than identified.
func TestSharedSubagentListKeepsConfigOrder(t *testing.T) {
	f := newSubagentRootFixture(t, func(root *appcfg.Root) {
		root.Agents.SharedSubagents = []appcfg.SubagentConfig{
			{Name: "note-taker", Description: "writes one note", SystemPrompt: "You take notes."},
			{Name: "repo-auditor", Description: "audits one repository area", SystemPrompt: "You audit repositories.", Model: "openai/gpt-main", ReasoningEffort: "low"},
		}
	})
	rec := f.do(t, http.MethodGet, "/api/agents/subagents/shared", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	listing := decodeSharedRegistry(t, rec)
	require.Len(t, listing.Rows, 2)
	require.Equal(t, "note-taker", listing.Rows[0].Name)
	require.Equal(t, "repo-auditor", listing.Rows[1].Name)
	require.Equal(t, "low", listing.Rows[1].ReasoningEffort)
	require.Equal(t, "openai/gpt-main", listing.Rows[1].ModelLabel)
	require.Equal(t, subagentModelFromGlobal, listing.Rows[1].ModelSource)
}

// TestSharedSubagentTypeDefaultRows pins the per-type global default rows the
// same group edits (plans 314, 315): the four public types are listed, a write
// stores the row id, the level rides beside it, and an empty choice removes the
// row — which is what "no global default" means.
func TestSharedSubagentTypeDefaultRows(t *testing.T) {
	f := newSubagentRootFixture(t, nil)
	rowID := seedGlobalModelRow(t, f, "deepseek", "deepseek-v4-pro")

	rec := f.do(t, http.MethodPut, "/api/agents/subagents/shared/types/explore", map[string]any{
		"model": rowID, "reasoning_effort": "high",
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	row, ok := f.config(t).Agents.SubagentModels["explore"]
	require.True(t, ok)
	require.Equal(t, rowID, row.Model)
	require.Equal(t, "high", row.ReasoningEffort)

	listing := decodeSharedRegistry(t, rec)
	require.Len(t, listing.Types, 4)
	edited := sharedTypeRowByName(t, listing.Types, "explore")
	require.Equal(t, rowID, edited.Model)
	require.Equal(t, "high", edited.ReasoningEffort)

	// A type with no global row keeps the empty reading on the way out.
	untouched := sharedTypeRowByName(t, listing.Types, "verification")
	require.Empty(t, untouched.Model)
	require.Empty(t, untouched.ReasoningEffort)

	// The level needs a model beside it, and only a built-in type has a row.
	rec = f.do(t, http.MethodPut, "/api/agents/subagents/shared/types/plan", map[string]any{"reasoning_effort": "low"})
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "reasoning_effort requires model")
	rec = f.do(t, http.MethodPut, "/api/agents/subagents/shared/types/repo-auditor", map[string]any{"model": rowID})
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "only a built-in subagent has an install-wide model row")

	// Clearing both halves removes the row: absent is "no global default".
	rec = f.do(t, http.MethodPut, "/api/agents/subagents/shared/types/explore", map[string]any{})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	_, ok = f.config(t).Agents.SubagentModels["explore"]
	require.False(t, ok)
}
