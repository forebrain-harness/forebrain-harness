package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
	"github.com/stretchr/testify/require"
)

// The subagent configuration surface edits forebrain.yaml, so every test below
// runs against a real file: what the write path promises is a field in that
// file, and reading it back through the loader is the only proof of it.

// subagentFixture is the MCP write fixture — a gateway server over a real
// forebrain.yaml — plus the production route table the subagent endpoints are
// registered on.
//
// The routes come from AttachExtraRoutes rather than a hand-written mirror in
// the test, because the thing worth proving is that they coexist: the static
// /defaults segment sits in the same group as the /:agentId/cancel parameter
// route, and a copy registered by the test would prove nothing about that.
type subagentFixture struct {
	*mcpWriteFixture
	routes *RestServer
}

// newSubagentFixture builds the gateway over a forebrain.yaml whose "main" agent
// carries the subagent configuration under test.
func newSubagentFixture(t *testing.T, mutate func(*appcfg.AgentDefinition)) *subagentFixture {
	t.Helper()
	f := newMCPWriteFixture(t, func(root *appcfg.Root) {
		def := appcfg.AgentDefinition{Primary: true, LLMProviders: mcpWriteProvider()}
		if mutate != nil {
			mutate(&def)
		}
		root.Agents.Definitions["main"] = def
	}, "main", nil)
	routes := NewRestServer("127.0.0.1:0")
	f.server.AttachExtraRoutes(routes)
	return &subagentFixture{mcpWriteFixture: f, routes: routes}
}

// do answers one request through the production handler.
func (f *subagentFixture) do(t *testing.T, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	reader := strings.NewReader("")
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		reader = strings.NewReader(string(raw))
	}
	req := httptest.NewRequest(method, target, reader)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.routes.Server.Handler.ServeHTTP(rec, req)
	return rec
}

// subagents is the "main" agent's subagent entries as they stand on disk.
func (f *mcpWriteFixture) subagentEntries(t *testing.T) []appcfg.SubagentConfig {
	t.Helper()
	return f.config(t).Agents.Definitions[f.agentName].Subagents
}

// mainDefinition is the "main" agent's definition as it stands on disk.
func (f *mcpWriteFixture) mainDefinition(t *testing.T) appcfg.AgentDefinition {
	t.Helper()
	return f.config(t).Agents.Definitions[f.agentName]
}

func findSubagentEntry(t *testing.T, entries []appcfg.SubagentConfig, name string) appcfg.SubagentConfig {
	t.Helper()
	for _, entry := range entries {
		if entry.Name == name {
			return entry
		}
	}
	t.Fatalf("no subagent entry named %q in %+v", name, entries)
	return appcfg.SubagentConfig{}
}

// subagentPageResponse is the list endpoint's answer, decoded into the types the
// server builds it from so an assertion reads the same row the table does.
type subagentPageResponse struct {
	Rows     []subagentRow `json:"rows"`
	Total    int           `json:"total"`
	Page     int           `json:"page"`
	PageSize int           `json:"page_size"`
	Switch   bool          `json:"switch"`
	Global   bool          `json:"global"`
}

func decodeSubagentPage(t *testing.T, rec *httptest.ResponseRecorder) subagentPageResponse {
	t.Helper()
	var page subagentPageResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
	return page
}

func subagentRowByName(t *testing.T, rows []subagentRow, name string) subagentRow {
	t.Helper()
	for _, row := range rows {
		if row.Name == name {
			return row
		}
	}
	t.Fatalf("no subagent row named %q in %+v", name, rows)
	return subagentRow{}
}

// TestSubagentListOrderKindsAndPaging pins the list: the three kinds in the
// owner's order (custom, then the dispatchable built-ins, then the system's), the
// kind filter and the search, server paging under the 071 contract, and the two
// switch fields the toolbar reads.
func TestSubagentListOrderKindsAndPaging(t *testing.T) {
	f := newSubagentFixture(t, func(def *appcfg.AgentDefinition) {
		def.Subagents = []appcfg.SubagentConfig{{
			Name:         "db-migrator",
			Description:  "runs the database migrations",
			SystemPrompt: "You migrate the database.",
			Tools:        appcfg.SubagentToolsPick,
			AllowedTools: []string{"read_file", "bash"},
		}}
	})

	rec := f.do(t, http.MethodGet, "/api/agents/primary/main/subagents", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	page := decodeSubagentPage(t, rec)
	names := make([]string, 0, len(page.Rows))
	for _, row := range page.Rows {
		names = append(names, row.Name)
	}
	want := append([]string{"db-migrator"}, agent.PublicSubagentNames...)
	want = append(want, agent.SystemSubagentNames...)
	require.Equal(t, want, names)
	require.False(t, page.Switch, "an agent that never touched the switch follows the global one")
	require.False(t, page.Global)

	custom := subagentRowByName(t, page.Rows, "db-migrator")
	require.Equal(t, subagentKindCustom, custom.Kind)
	require.Equal(t, appcfg.SubagentToolsPick, custom.Tools)
	require.Equal(t, []string{"read_file", "bash"}, custom.AllowedTools)
	require.True(t, custom.Continuable)
	require.NotNil(t, custom.Enabled)
	require.True(t, *custom.Enabled)
	require.False(t, custom.ModelProvider != "" || custom.Model != "", "no model means the conversation's")

	builtin := subagentRowByName(t, page.Rows, "explore")
	require.Equal(t, subagentKindBuiltin, builtin.Kind)
	require.Equal(t, appcfg.SubagentToolsReadOnly, builtin.Tools)
	require.False(t, builtin.Continuable)

	system := subagentRowByName(t, page.Rows, "guardian")
	require.Equal(t, subagentKindSystem, system.Kind)
	require.Equal(t, tool.SubagentToolsNone, system.Tools)
	require.Nil(t, system.Enabled, "a system subagent has no switch")

	// The kind filter narrows the same rows.
	rec = f.do(t, http.MethodGet, "/api/agents/primary/main/subagents?kind=builtin&page=1", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	builtins := decodeSubagentPage(t, rec)
	require.Len(t, builtins.Rows, len(agent.PublicSubagentNames))
	require.Equal(t, len(agent.PublicSubagentNames), builtins.Total)

	rec = f.do(t, http.MethodGet, "/api/agents/primary/main/subagents?kind=system", nil)
	require.Len(t, decodeSubagentPage(t, rec).Rows, len(agent.SystemSubagentNames))

	// The search reads the name and the description.
	rec = f.do(t, http.MethodGet, "/api/agents/primary/main/subagents?q=migrations", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	found := decodeSubagentPage(t, rec)
	require.Len(t, found.Rows, 1)
	require.Equal(t, "db-migrator", found.Rows[0].Name)

	// A page past the end serves the last one; total counts every matching row.
	rec = f.do(t, http.MethodGet, "/api/agents/primary/main/subagents?page=99&page_size=5", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	paged := decodeSubagentPage(t, rec)
	require.Equal(t, len(want), paged.Total)
	require.Equal(t, 3, paged.Page)
	require.Len(t, paged.Rows, len(want)-2*5)

	// A kind the endpoint does not know is refused rather than answered empty.
	rec = f.do(t, http.MethodGet, "/api/agents/primary/main/subagents?kind=elsewhere", nil)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())

	// The static /defaults route and the /:agentId param route coexist: the
	// router ranks the literal segment higher, and the older route still
	// answers.
	rec = f.do(t, http.MethodGet, "/api/agents/defaults/subagents", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = f.do(t, http.MethodPost, "/api/agents/main/cancel", nil)
	require.NotEqual(t, http.StatusNotFound, rec.Code, "the sibling param route must keep answering")
}

// TestSubagentDetailShowsThePromptInForce pins the drawer's read: the effective
// prompt, the harness's original word for word, and no original for a subagent
// this agent wrote itself.
func TestSubagentDetailShowsThePromptInForce(t *testing.T) {
	f := newSubagentFixture(t, func(def *appcfg.AgentDefinition) {
		def.Subagents = []appcfg.SubagentConfig{{
			Name: "db-migrator", Description: "runs the migrations", SystemPrompt: "You migrate the database.",
		}}
	})

	rec := f.do(t, http.MethodGet, "/api/agents/primary/main/subagents/explore", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var detail subagentDetail
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &detail))
	original, ok := agent.SystemPromptForSubtype("explore")
	require.True(t, ok)
	require.Equal(t, original, detail.SystemPrompt)
	require.NotNil(t, detail.DefaultPrompt)
	require.Equal(t, original, *detail.DefaultPrompt)
	require.False(t, detail.PromptOverridden)

	// A system subagent is readable for the same reason a built-in is: a person
	// cannot judge what the system does with it from the code.
	rec = f.do(t, http.MethodGet, "/api/agents/primary/main/subagents/guardian", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &detail))
	original, ok = agent.SystemPromptForSubtype("guardian")
	require.True(t, ok)
	require.Equal(t, original, detail.SystemPrompt)
	require.NotNil(t, detail.DefaultPrompt)
	require.Equal(t, original, *detail.DefaultPrompt)
	require.Nil(t, detail.Enabled)

	// A custom subagent is its own text; there is no original to restore.
	rec = f.do(t, http.MethodGet, "/api/agents/primary/main/subagents/db-migrator", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var custom subagentDetail
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &custom))
	require.Equal(t, "You migrate the database.", custom.SystemPrompt)
	require.Nil(t, custom.DefaultPrompt)

	rec = f.do(t, http.MethodGet, "/api/agents/primary/main/subagents/nope", nil)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}

// TestSubagentCreatePrependsAndRefusesCollisions pins the create path: a new
// subagent is first in the list, and every name the file cannot take is refused
// with the status that says why.
func TestSubagentCreatePrependsAndRefusesCollisions(t *testing.T) {
	f := newSubagentFixture(t, nil)

	rec := f.do(t, http.MethodPost, "/api/agents/primary/main/subagents", map[string]any{
		"name":          "alpha",
		"description":   "handles alpha",
		"system_prompt": "You handle alpha.",
		"tools":         "pick",
		"allowed_tools": []string{"read_file", "bash"},
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	entries := f.subagentEntries(t)
	require.Len(t, entries, 1)
	require.Equal(t, "alpha", entries[0].Name)
	require.Equal(t, appcfg.SubagentToolsPick, entries[0].Tools)
	require.Equal(t, []string{"read_file", "bash"}, entries[0].AllowedTools)

	rec = f.do(t, http.MethodPost, "/api/agents/primary/main/subagents", map[string]any{
		"name": "beta", "description": "handles beta", "system_prompt": "You handle beta.",
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	entries = f.subagentEntries(t)
	require.Equal(t, []string{"beta", "alpha"}, []string{entries[0].Name, entries[1].Name})

	cases := []struct {
		name string
		body map[string]any
		want int
		text string
	}{
		{
			name: "a name already configured",
			body: map[string]any{"name": "alpha", "description": "d", "system_prompt": "p"},
			want: http.StatusConflict, text: "already configured",
		},
		{
			name: "a built-in the model may dispatch",
			body: map[string]any{"name": "explore", "description": "d", "system_prompt": "p"},
			want: http.StatusConflict, text: "built-in",
		},
		{
			name: "a subagent the system dispatches",
			body: map[string]any{"name": "plan-reviewer", "description": "d", "system_prompt": "p"},
			want: http.StatusConflict, text: "built-in",
		},
		{
			name: "a name the loader would reject",
			body: map[string]any{"name": "Not A Name", "description": "d", "system_prompt": "p"},
			want: http.StatusBadRequest, text: "invalid name",
		},
		{
			name: "a description nobody wrote",
			body: map[string]any{"name": "gamma", "system_prompt": "p"},
			want: http.StatusBadRequest, text: "description required",
		},
		{
			name: "a subagent that would dispatch subagents",
			body: map[string]any{
				"name": "delta", "description": "d", "system_prompt": "p",
				"tools": "pick", "allowed_tools": []string{"subagent_run"},
			},
			want: http.StatusBadRequest, text: "cannot dispatch subagents",
		},
		{
			name: "an effort without a model",
			body: map[string]any{"name": "epsilon", "description": "d", "system_prompt": "p", "reasoning_effort": "high"},
			want: http.StatusBadRequest, text: "reasoning_effort requires model",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := len(f.subagentEntries(t))
			rec := f.do(t, http.MethodPost, "/api/agents/primary/main/subagents", tc.body)
			require.Equal(t, tc.want, rec.Code, rec.Body.String())
			require.Contains(t, rec.Body.String(), tc.text)
			require.Len(t, f.subagentEntries(t), before, "a refused create changes nothing")
		})
	}
}

// TestSubagentBuiltinRewriteAndRestoreDefault pins the two halves of the prompt
// override: the rewrite is what the row and the drawer then read, and restoring
// the default drops the override entry rather than storing an empty one.
func TestSubagentBuiltinRewriteAndRestoreDefault(t *testing.T) {
	f := newSubagentFixture(t, nil)

	rec := f.do(t, http.MethodPut, "/api/agents/primary/main/subagents/explore", map[string]any{
		"system_prompt":    "You are my explorer.",
		"model":            "openai/gpt-sub",
		"reasoning_effort": "low",
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	entry := findSubagentEntry(t, f.subagentEntries(t), "explore")
	require.Equal(t, "You are my explorer.", entry.SystemPrompt)
	require.Equal(t, "openai/gpt-sub", entry.Model)
	require.Equal(t, "low", entry.ReasoningEffort)

	rec = f.do(t, http.MethodGet, "/api/agents/primary/main/subagents/explore", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var detail subagentDetail
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &detail))
	require.True(t, detail.PromptOverridden)
	require.Equal(t, "You are my explorer.", detail.SystemPrompt)
	require.NotNil(t, detail.DefaultPrompt)
	original, _ := agent.SystemPromptForSubtype("explore")
	require.Equal(t, original, *detail.DefaultPrompt)

	// The row says the same thing about the model, and where it came from.
	rec = f.do(t, http.MethodGet, "/api/agents/primary/main/subagents", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	row := subagentRowByName(t, decodeSubagentPage(t, rec).Rows, "explore")
	require.Equal(t, "openai", row.ModelProvider)
	require.Equal(t, "gpt-sub", row.Model)
	require.Equal(t, "low", row.ReasoningEffort)
	require.Equal(t, subagentModelFromAgent, row.ModelSource)
	require.True(t, row.PromptOverridden)

	rec = f.do(t, http.MethodPut, "/api/agents/primary/main/subagents/explore", map[string]any{"reset_prompt": true})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Empty(t, f.subagentEntries(t), "an override that only repeats the default is dropped")

	rec = f.do(t, http.MethodGet, "/api/agents/primary/main/subagents/explore", nil)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &detail))
	require.False(t, detail.PromptOverridden)
	require.Equal(t, original, detail.SystemPrompt)
}

// TestSubagentBuiltinSwitchAndLinkage pins one row's switch and the rule that
// ties it to the agent's own: the last row switched off switches the feature off
// here, and switching any row on switches it on.
func TestSubagentBuiltinSwitchAndLinkage(t *testing.T) {
	f := newSubagentFixture(t, nil)

	rec := f.do(t, http.MethodPut, "/api/agents/primary/main/subagents/plan/enabled", map[string]any{"enabled": false})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var ack struct {
		Switch bool `json:"switch"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ack))
	require.False(t, ack.Switch, "three other rows are still on")
	entry := findSubagentEntry(t, f.subagentEntries(t), "plan")
	require.NotNil(t, entry.Enabled)
	require.False(t, *entry.Enabled)

	// Switching a row on can only mean the feature is on for this agent: the
	// agent's own switch follows, without the user having to find it.
	rec = f.do(t, http.MethodPut, "/api/agents/primary/main/subagents/plan/enabled", map[string]any{"enabled": true})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ack))
	require.True(t, ack.Switch)
	def := f.mainDefinition(t)
	require.NotNil(t, def.EnableSubagent)
	require.True(t, *def.EnableSubagent)
	require.Empty(t, f.subagentEntries(t), "a built-in back at its default needs no entry")

	rec = f.do(t, http.MethodPut, "/api/agents/primary/main/subagents/plan/enabled", map[string]any{})
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())

	rec = f.do(t, http.MethodPut, "/api/agents/primary/main/subagents/guardian/enabled", map[string]any{"enabled": false})
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

// TestSubagentRowSwitchLinkageWhenEveryRowIsOff pins the other half: a list with
// nothing enabled says the feature is off for this agent.
func TestSubagentRowSwitchLinkageWhenEveryRowIsOff(t *testing.T) {
	f := newSubagentFixture(t, func(def *appcfg.AgentDefinition) {
		def.EnableSubagent = appcfg.BoolPtr(true)
		for _, name := range agent.PublicSubagentNames {
			def.Subagents = append(def.Subagents, appcfg.SubagentConfig{Name: name, Enabled: appcfg.BoolPtr(false)})
		}
		def.Subagents = append(def.Subagents, appcfg.SubagentConfig{
			Name: "solo", Description: "the only one on", SystemPrompt: "You are solo.",
		})
	})

	rec := f.do(t, http.MethodPut, "/api/agents/primary/main/subagents/solo/enabled", map[string]any{"enabled": false})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var ack struct {
		Switch bool `json:"switch"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ack))
	require.False(t, ack.Switch)
	def := f.mainDefinition(t)
	require.NotNil(t, def.EnableSubagent)
	require.False(t, *def.EnableSubagent)

	rec = f.do(t, http.MethodGet, "/api/agents/primary/main/subagents", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.False(t, decodeSubagentPage(t, rec).Switch)

	rec = f.do(t, http.MethodPut, "/api/agents/primary/main/subagents/solo/enabled", map[string]any{"enabled": true})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ack))
	require.True(t, ack.Switch)
	def = f.mainDefinition(t)
	require.NotNil(t, def.EnableSubagent)
	require.True(t, *def.EnableSubagent)
}

// TestSubagentAgentSwitchRewritesEveryRow pins the toolbar switch: it writes the
// agent's own switch and brings every row with it, in both directions.
func TestSubagentAgentSwitchRewritesEveryRow(t *testing.T) {
	f := newSubagentFixture(t, func(def *appcfg.AgentDefinition) {
		def.Subagents = []appcfg.SubagentConfig{{Name: "solo", Description: "d", SystemPrompt: "p"}}
	})

	rec := f.do(t, http.MethodPut, "/api/agents/primary/main/subagent-switch", map[string]any{"enabled": false})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	def := f.mainDefinition(t)
	require.NotNil(t, def.EnableSubagent)
	require.False(t, *def.EnableSubagent)
	for _, name := range agent.PublicSubagentNames {
		entry := findSubagentEntry(t, def.Subagents, name)
		require.NotNil(t, entry.Enabled, "%s must carry the switch", name)
		require.False(t, *entry.Enabled)
	}
	solo := findSubagentEntry(t, def.Subagents, "solo")
	require.NotNil(t, solo.Enabled)
	require.False(t, *solo.Enabled)

	rec = f.do(t, http.MethodGet, "/api/agents/primary/main/subagents", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	for _, row := range decodeSubagentPage(t, rec).Rows {
		if row.Enabled == nil {
			require.Equal(t, subagentKindSystem, row.Kind, "%s: only a system row has no switch", row.Name)
			continue
		}
		require.False(t, *row.Enabled, "%s is off", row.Name)
	}

	rec = f.do(t, http.MethodPut, "/api/agents/primary/main/subagent-switch", map[string]any{"enabled": true})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	def = f.mainDefinition(t)
	require.NotNil(t, def.EnableSubagent)
	require.True(t, *def.EnableSubagent)
	require.Len(t, def.Subagents, 1, "a built-in back at its default needs no entry: %+v", def.Subagents)
	require.Equal(t, "solo", def.Subagents[0].Name)
	require.Nil(t, def.Subagents[0].Enabled)
}

// TestSubagentDeleteRefusesBuiltins pins what cannot be deleted: only a subagent
// this agent defined itself is its to remove.
func TestSubagentDeleteRefusesBuiltins(t *testing.T) {
	f := newSubagentFixture(t, func(def *appcfg.AgentDefinition) {
		def.Subagents = []appcfg.SubagentConfig{{Name: "solo", Description: "d", SystemPrompt: "p"}}
	})

	rec := f.do(t, http.MethodDelete, "/api/agents/primary/main/subagents/explore", nil)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "switch it off")

	rec = f.do(t, http.MethodDelete, "/api/agents/primary/main/subagents/goal-evaluator", nil)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())

	rec = f.do(t, http.MethodDelete, "/api/agents/primary/main/subagents/nope", nil)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())

	rec = f.do(t, http.MethodDelete, "/api/agents/primary/main/subagents/solo", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Empty(t, f.subagentEntries(t))
}

// TestSubagentSystemUpdateIsRefused pins that a system subagent takes no write at
// all: the system dispatches it, so a stored prompt here would be a promise the
// runtime does not keep.
func TestSubagentSystemUpdateIsRefused(t *testing.T) {
	f := newSubagentFixture(t, nil)
	for _, name := range agent.SystemSubagentNames {
		rec := f.do(t, http.MethodPut, "/api/agents/primary/main/subagents/"+name, map[string]any{
			"system_prompt": "rewritten", "model": "openai/gpt-sub",
		})
		require.Equal(t, http.StatusBadRequest, rec.Code, "%s: %s", name, rec.Body.String())
	}
	require.Empty(t, f.subagentEntries(t), "a refused write changes nothing")
}

// TestSubagentsDefaultSwitch pins the settings page's global switch: it writes
// agents.defaults.enable_subagent and leaves every agent's own override alone.
func TestSubagentsDefaultSwitch(t *testing.T) {
	f := newSubagentFixture(t, func(def *appcfg.AgentDefinition) {
		def.EnableSubagent = appcfg.BoolPtr(false)
	})

	rec := f.do(t, http.MethodPut, "/api/agents/defaults/subagents", map[string]any{"enabled": true})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	cfg := f.config(t)
	require.NotNil(t, cfg.Agents.Defaults.EnableSubagent)
	require.True(t, *cfg.Agents.Defaults.EnableSubagent)
	require.NotNil(t, cfg.Agents.Definitions["main"].EnableSubagent)
	require.False(t, *cfg.Agents.Definitions["main"].EnableSubagent, "an agent's own switch is not the global one's to move")

	rec = f.do(t, http.MethodGet, "/api/agents/defaults/subagents", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var ack struct {
		Enabled bool `json:"enabled"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ack))
	require.True(t, ack.Enabled)

	// The page shows both switches, and they disagree for this agent.
	rec = f.do(t, http.MethodGet, "/api/agents/primary/main/subagents", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	page := decodeSubagentPage(t, rec)
	require.False(t, page.Switch)
	require.True(t, page.Global)

	rec = f.do(t, http.MethodPut, "/api/agents/defaults/subagents", map[string]any{})
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

// TestSubagentModelOptions pins the drawer's model list: the same configured
// models the conversation's own model menu offers this agent.
func TestSubagentModelOptions(t *testing.T) {
	f := newSubagentFixture(t, nil)
	rec := f.do(t, http.MethodGet, "/api/agents/primary/main/model-options", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body struct {
		Options []sessionModelOption `json:"options"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.NotEmpty(t, body.Options)
	providers := make([]string, 0, len(body.Options))
	for _, option := range body.Options {
		providers = append(providers, option.Provider+"/"+option.Model)
	}
	require.True(t, slices.Contains(providers, "openai/gpt-main"), providers)

	rec = f.do(t, http.MethodGet, "/api/agents/primary/nope/model-options", nil)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}

// TestSubagentRowSwitchesDoNotLoseUpdates fires all four built-in row switches
// at once. Each PUT is its own read-modify-write of forebrain.yaml; without
// withConfigWrite serialising them, a writer that read the file before an
// earlier save lands would write that stale file back and the rows it switched
// off would come back on. Status codes are collected and asserted on the main
// goroutine only.
func TestSubagentRowSwitchesDoNotLoseUpdates(t *testing.T) {
	f := newSubagentFixture(t, nil)

	names := []string{"general-purpose", "explore", "plan", "verification"}
	codes := make([]int, len(names))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			<-start
			rec := f.do(t, http.MethodPut, "/api/agents/primary/main/subagents/"+name+"/enabled", map[string]any{"enabled": false})
			codes[i] = rec.Code
		}(i, name)
	}
	close(start)
	wg.Wait()

	for i, name := range names {
		require.Equal(t, http.StatusOK, codes[i], "%s: status %d, want 200", name, codes[i])
	}
	entries := f.subagentEntries(t)
	for _, name := range names {
		entry := findSubagentEntry(t, entries, name)
		require.NotNil(t, entry.Enabled, "%s must carry the switch", name)
		require.False(t, *entry.Enabled, "%s was switched off; a racing write restored it", name)
	}
	def := f.mainDefinition(t)
	require.NotNil(t, def.EnableSubagent)
	require.False(t, *def.EnableSubagent, "every public row off means the feature is off for the agent")
}
