package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
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
func newSubagentFixture(t *testing.T, mutate func(*appcfg.AgentDefinition), extra ...appcfg.AgentLLMProviderConfig) *subagentFixture {
	t.Helper()
	f := newMCPWriteFixture(t, func(root *appcfg.Root) {
		root.LLMModels = modelRowsForTest(append(mcpWriteProvider(), extra...)...)
		def := appcfg.AgentDefinition{Primary: true, DefaultModel: "llm-1"}
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

// newSubagentRootFixture is newSubagentFixture's sibling for configuration that
// is not one agent's definition: the install-wide rows — agents.plan_reviewer
// and agents.subagent_models — hang off the section itself (plans 313, 315).
func newSubagentRootFixture(t *testing.T, mutate func(*appcfg.Root)) *subagentFixture {
	t.Helper()
	f := newMCPWriteFixture(t, mutate, "main", nil)
	routes := NewRestServer("127.0.0.1:0")
	f.server.AttachExtraRoutes(routes)
	return &subagentFixture{mcpWriteFixture: f, routes: routes}
}

// seedGlobalModelRow adds one row to the fixture's install-wide model table —
// state, not a file section (plan 315) — and returns its id: the reference a
// global row stores. The file cannot carry it: the loader resolves references
// against the table, and the table is born in the database the fixture opens
// after the file is saved.
func seedGlobalModelRow(t *testing.T, f *subagentFixture, provider, model string) string {
	t.Helper()
	row, err := (&state.LLMModelStore{DB: f.server.Env.SQL}).Create(context.Background(), state.LLMModel{
		Provider: provider, Model: model,
		BaseURL: "http://127.0.0.1:9/v1", APIPath: "/chat/completions", APIKey: "sk-subagent-global",
	})
	require.NoError(t, err)
	return row.ID
}

// seedAgentsSection stores the fixture's agents section as the endpoints then
// read it: the composed persisted configuration, changed and written back.
func seedAgentsSection(t *testing.T, f *subagentFixture, mutate func(*appcfg.AgentsSection)) {
	t.Helper()
	root, err := f.server.Env.ComposedPersistedConfig(context.Background())
	require.NoError(t, err)
	mutate(&root.Agents)
	seedConfigRoot(t, f.server.Env.SQL, root)
}

// TestSubagentPlanReviewerGlobalRow pins the one system row with an
// install-wide model of its own (plan 313): the plan reviewer reports
// agents.plan_reviewer as a label beside its identity, no other system row
// carries one, and the row keeps the shape a system row has — no model of this
// agent's, no switch.
func TestSubagentPlanReviewerGlobalRow(t *testing.T) {
	f := newSubagentRootFixture(t, nil)
	rowID := seedGlobalModelRow(t, f, "openai", "gpt-main")
	seedAgentsSection(t, f, func(agents *appcfg.AgentsSection) {
		agents.PlanReviewer = appcfg.SubagentConfig{Model: rowID, ReasoningEffort: "high"}
	})

	rec := f.do(t, http.MethodGet, "/api/agents/primary/main/subagents", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rows := decodeSubagentPage(t, rec).Rows

	reviewer := subagentRowByName(t, rows, "plan-reviewer")
	require.Equal(t, subagentKindSystem, reviewer.Kind)
	require.Equal(t, llm.FormatProviderModelDisplay("openai", "gpt-main"), reviewer.GlobalModelLabel)
	require.Equal(t, "high", reviewer.GlobalReasoningEffort)
	require.Empty(t, reviewer.Model, "a system row still carries no model of this agent's")
	require.Nil(t, reviewer.Enabled, "a system row still has no switch")

	// Only the reviewer has such a row.
	for _, name := range append([]string{"goal-evaluator", "explore"}, agent.SystemSubagentNames...) {
		if name == "plan-reviewer" {
			continue
		}
		row := subagentRowByName(t, rows, name)
		require.Empty(t, row.GlobalModelLabel, "%s names no install-wide row", name)
		require.Empty(t, row.GlobalReasoningEffort, "%s names no install-wide row", name)
	}

	// No configured row: both fields are absent, and the row reads as before.
	bare := newSubagentRootFixture(t, nil)
	rec = bare.do(t, http.MethodGet, "/api/agents/primary/main/subagents", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	unconfigured := subagentRowByName(t, decodeSubagentPage(t, rec).Rows, "plan-reviewer")
	require.Empty(t, unconfigured.GlobalModelLabel)
	require.Empty(t, unconfigured.GlobalReasoningEffort)
	require.NotContains(t, rec.Body.String(), "global_model_label",
		"an unset row adds no field to the wire")

	// A reference the table does not carry is never claimed: the loader refuses
	// such a file, so this is the shape a hand-built root can still have, and
	// the row must read as unconfigured rather than name a model nothing can
	// run on.
	root := &appcfg.Root{
		LLMModels: []appcfg.LLMModelConfig{{ID: "llm-1", Provider: "openai", Model: "gpt-main"}},
		Agents: appcfg.AgentsSection{
			PlanReviewer: appcfg.SubagentConfig{Model: "llm-no-such-row", ReasoningEffort: "low"},
		},
	}
	stray := systemSubagentRow(root, "plan-reviewer")
	require.Empty(t, stray.GlobalModelLabel)
	require.Empty(t, stray.GlobalReasoningEffort)
}

// TestSubagentBuiltinModelSourcePrecedence pins the order dispatch uses for a
// built-in the model may dispatch: this agent's own entry, then the
// install-wide per-type row (agents.subagent_models, plan 315), then the
// conversation's model — the order subagentRowModel and run.SubagentOwnModel
// share (plan 313).
func TestSubagentBuiltinModelSourcePrecedence(t *testing.T) {
	// One fixture, three configurations of it: the row is seeded once, and each
	// step rewrites the agents section the endpoints read.
	f := newSubagentRootFixture(t, nil)
	rowID := seedGlobalModelRow(t, f, "openai", "gpt-main")
	exploreRow := func(t *testing.T) subagentRow {
		t.Helper()
		rec := f.do(t, http.MethodGet, "/api/agents/primary/main/subagents", nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		return subagentRowByName(t, decodeSubagentPage(t, rec).Rows, "explore")
	}

	// No entry of this agent's: the install-wide row is the model, and it is
	// reported as such — with the row's own effort, which is what the run uses.
	seedAgentsSection(t, f, func(agents *appcfg.AgentsSection) {
		agents.SubagentModels = map[string]appcfg.SubagentConfig{
			"explore": {Model: rowID, ReasoningEffort: "low"},
		}
	})
	inherited := exploreRow(t)
	require.Equal(t, subagentModelFromGlobal, inherited.ModelSource)
	require.Equal(t, rowID, inherited.ModelID)
	require.Equal(t, llm.FormatProviderModelDisplay("openai", "gpt-main"), inherited.ModelLabel)
	require.Equal(t, "low", inherited.ReasoningEffort)

	// An entry of its own wins over the install-wide row.
	seedAgentsSection(t, f, func(agents *appcfg.AgentsSection) {
		def := agents.Definitions["main"]
		def.Subagents = []appcfg.SubagentConfig{{Name: "explore", Model: "openai/gpt-sub", ReasoningEffort: "high"}}
		agents.Definitions["main"] = def
	})
	own := exploreRow(t)
	require.Equal(t, subagentModelFromAgent, own.ModelSource)
	require.Equal(t, "gpt-sub", own.Model)
	require.Equal(t, "high", own.ReasoningEffort)
	require.Equal(t, llm.FormatProviderModelDisplay("openai", "gpt-sub"), own.ModelLabel)

	// Neither: the conversation's model, which is no source at all.
	seedAgentsSection(t, f, func(agents *appcfg.AgentsSection) {
		def := agents.Definitions["main"]
		def.Subagents = nil
		agents.Definitions["main"] = def
		agents.SubagentModels = nil
	})
	neither := exploreRow(t)
	require.Empty(t, neither.ModelSource)
	require.Empty(t, neither.ModelLabel)
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
			want: http.StatusBadRequest, text: "thinking level requires a model",
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

// The drawer's model menu and the subagent rows show the vendor listing's
// display name: a configured row through the shared choice projection, and a
// reference nothing resolves through the label fallback — both halves of
// subagentRowModel. The wire id stays the identity a write stores.
func TestSubagentModelSurfacesShowVendorListingDisplayName(t *testing.T) {
	llm.SetBuiltinModels("qodercn", []llm.Model{
		{ID: "gfmodel", Name: "GLM-5.3-Flash"},
		{ID: "dmodel", Name: "DeepSeek-V4-Pro"},
	})
	t.Cleanup(func() { llm.SetBuiltinModels("qodercn", nil) })
	f := newSubagentFixture(t, func(def *appcfg.AgentDefinition) {
		def.DefaultModel = "llm-1"
		def.Subagents = []appcfg.SubagentConfig{
			{Name: "solo", Description: "d", SystemPrompt: "p", Model: "qodercn/gfmodel"},
			{Name: "duo", Description: "d", SystemPrompt: "p", Model: "qodercn/dmodel"},
		}
	}, appcfg.AgentLLMProviderConfig{Provider: "qodercn", Model: "gfmodel"})

	rec := f.do(t, http.MethodGet, "/api/agents/primary/main/model-options", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body struct {
		Options []sessionModelOption `json:"options"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	var qoder *sessionModelOption
	for i := range body.Options {
		if body.Options[i].Provider == "qodercn" {
			qoder = &body.Options[i]
		}
	}
	require.NotNil(t, qoder, "the configured qodercn row is offered")
	require.Equal(t, "gfmodel", qoder.Model)
	require.Equal(t, "GLM-5.3-Flash", qoder.ModelName)
	require.Equal(t, "qodercn/GLM-5.3-Flash", qoder.Label)

	rec = f.do(t, http.MethodGet, "/api/agents/primary/main/subagents", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	page := decodeSubagentPage(t, rec)
	require.Equal(t, "qodercn/GLM-5.3-Flash", subagentRowByName(t, page.Rows, "solo").ModelLabel,
		"a resolved row projects through the shared choice label")
	require.Equal(t, "qodercn/DeepSeek-V4-Pro", subagentRowByName(t, page.Rows, "duo").ModelLabel,
		"an unresolved reference still shows the listing's name")
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

// TestSubagentModelIsStoredAsARowID pins plan 265's Step 2: a subagent's model
// choice is a configured row, so the write accepts the legacy "provider/model"
// label and stores the id, and the row reports the id, the label and the
// endpoint its requests go to.
func TestSubagentModelIsStoredAsARowID(t *testing.T) {
	f := newSubagentFixture(t, nil)
	id, err := process.UpsertLLMModel(context.Background(), f.server.Env.SQL, process.LLMModelInput{
		Provider: "openai", Model: "gpt-4o", BaseURL: "https://api.openai.com/v1",
		APIPath: appcfg.APIPathChatCompletions, APIKey: stringPtr("sk-subagent"),
	})
	require.NoError(t, err)

	rec := f.do(t, http.MethodPut, "/api/agents/primary/main/subagents/explore", map[string]any{
		"model": "openai/gpt-4o", "reasoning_effort": "high",
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	entry := findSubagentEntry(t, f.subagentEntries(t), "explore")
	require.Equal(t, id, entry.Model, "the stored reference is the row id")

	rec = f.do(t, http.MethodGet, "/api/agents/primary/main/subagents", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	row := subagentRowByName(t, decodeSubagentPage(t, rec).Rows, "explore")
	require.Equal(t, id, row.ModelID)
	require.Equal(t, "openai", row.ModelProvider)
	require.Equal(t, "gpt-4o", row.Model)
	require.Equal(t, "openai/gpt-4o", row.ModelLabel)
	require.Equal(t, "https://api.openai.com/v1/chat/completions", row.ModelEndpoint)
	require.Equal(t, "high", row.ReasoningEffort)
}

// A subagent's thinking level must be one its own model row can send (plan 318
// F9). The row holds both facts — the protocol it speaks and the levels that
// protocol carries — so a level the client would drop is refused at the write
// instead of being stored and silently dropped at dispatch. A model that names
// no row keeps today's behaviour (the dispatch client is the only judge), and an
// empty level is never a level.
func TestSubagentEffortMustBeOneItsModelRowCanSend(t *testing.T) {
	f := newSubagentFixture(t, nil)
	// A custom Messages row: the Anthropic client sends neither none nor
	// minimal, and takes low..max.
	messagesRow, err := process.UpsertLLMModel(context.Background(), f.server.Env.SQL, process.LLMModelInput{
		Provider: appcfg.CustomProvider, Model: "vendor-messages", BaseURL: "https://llm.example.com",
		APIPath: appcfg.APIPathAnthropicMessages, APIKey: stringPtr("sk-messages"),
	})
	require.NoError(t, err)

	// The built-in rewrite path: minimal is a level this row cannot send.
	rec := f.do(t, http.MethodPut, "/api/agents/primary/main/subagents/explore", map[string]any{
		"model": messagesRow, "reasoning_effort": "minimal",
	})
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "发不出这个思考强度")
	require.Empty(t, f.subagentEntries(t), "a refused write stores nothing")

	// The level it does send is stored.
	rec = f.do(t, http.MethodPut, "/api/agents/primary/main/subagents/explore", map[string]any{
		"model": messagesRow, "reasoning_effort": "high",
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "high", findSubagentEntry(t, f.subagentEntries(t), "explore").ReasoningEffort)

	// The custom-subagent path answers the same way: none is off, which this
	// protocol cannot say either.
	rec = f.do(t, http.MethodPost, "/api/agents/primary/main/subagents", map[string]any{
		"name": "alpha", "description": "handles alpha", "system_prompt": "You handle alpha.",
		"model": messagesRow, "reasoning_effort": "none",
	})
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())

	// Nothing named is the row's own default, which is always allowed.
	rec = f.do(t, http.MethodPost, "/api/agents/primary/main/subagents", map[string]any{
		"name": "alpha", "description": "handles alpha", "system_prompt": "You handle alpha.",
		"model": messagesRow, "reasoning_effort": "",
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	// A model no row carries is not judged here: its levels are unknown to this
	// process, and the dispatch client is the only thing that could know them.
	rec = f.do(t, http.MethodPut, "/api/agents/primary/main/subagents/explore", map[string]any{
		"model": "someone/vendor-model", "reasoning_effort": "minimal",
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	// The install-wide per-type row goes through the same rule.
	rec = f.do(t, http.MethodPut, "/api/agents/subagents/shared/types/explore", map[string]any{
		"model": messagesRow, "reasoning_effort": "minimal",
	})
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

// sharedSubagentFixtureConfig gives the fixture one install-wide shared custom
// subagent plus one primary agent that defines a custom subagent of its own
// (plan 314): the two kinds the page has to tell apart.
func sharedSubagentFixtureConfig(def *appcfg.AgentDefinition) func(*appcfg.Root) {
	return func(root *appcfg.Root) {
		root.Agents.SharedSubagents = []appcfg.SubagentConfig{{
			Name:            "repo-auditor",
			Description:     "audits one repository area",
			SystemPrompt:    "You audit repositories.",
			Model:           "openai/gpt-main",
			ReasoningEffort: "high",
			Tools:           appcfg.SubagentToolsReadOnly,
		}}
		*def = appcfg.AgentDefinition{
			Primary:      true,
			DefaultModel: "llm-1",
			Subagents: []appcfg.SubagentConfig{{
				Name:         "db-migrator",
				Description:  "runs the database migrations",
				SystemPrompt: "You migrate the database.",
			}},
		}
		root.Agents.Definitions["main"] = *def
	}
}

// TestSubagentSharedRowIsDefaultDisabledAndEnableable pins the owner's rule on
// the agent page (plan 314): a shared custom subagent is listed for every
// primary agent with its switch OFF, enabling writes the reference entry — the
// name and enabled: true, nothing else — and disabling removes it, because
// absent is what disabled means.
func TestSubagentSharedRowIsDefaultDisabledAndEnableable(t *testing.T) {
	var def appcfg.AgentDefinition
	f := newSubagentRootFixture(t, sharedSubagentFixtureConfig(&def))

	rec := f.do(t, http.MethodGet, "/api/agents/primary/main/subagents", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rows := decodeSubagentPage(t, rec).Rows
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		names = append(names, row.Name)
	}
	// The agent's own entries first — its custom subagent and any shared
	// reference — then the shared registry's names it has not referenced, then
	// the built-ins, then the system's (the order subagentNames pins).
	want := append([]string{"db-migrator", "repo-auditor"}, agent.PublicSubagentNames...)
	want = append(want, agent.SystemSubagentNames...)
	require.Equal(t, want, names)

	shared := subagentRowByName(t, rows, "repo-auditor")
	require.Equal(t, subagentKindShared, shared.Kind)
	require.NotNil(t, shared.Enabled)
	require.False(t, *shared.Enabled, "a shared subagent runs nowhere until this agent enables it")
	require.Equal(t, "audits one repository area", shared.Description)
	require.Equal(t, appcfg.SubagentToolsReadOnly, shared.Tools)
	require.True(t, shared.Continuable)
	require.Equal(t, subagentModelFromGlobal, shared.ModelSource)
	require.Equal(t, "high", shared.ReasoningEffort)

	// Not enabled means nothing is stored for it.
	for _, entry := range f.subagentEntries(t) {
		require.NotEqual(t, "repo-auditor", entry.Name)
	}
	// The detail says what the registry says, so the drawer can show the
	// definition the page does not let anyone edit.
	rec = f.do(t, http.MethodGet, "/api/agents/primary/main/subagents/repo-auditor", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var detail subagentDetail
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &detail))
	require.Equal(t, "You audit repositories.", detail.SystemPrompt)
	require.Equal(t, "repo-auditor", detail.Name)

	// Enabling writes the reference entry, and the row reads it back.
	rec = f.do(t, http.MethodPut, "/api/agents/primary/main/subagents/repo-auditor/enabled", map[string]any{"enabled": true})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	reference := findSubagentEntry(t, f.subagentEntries(t), "repo-auditor")
	require.NotNil(t, reference.Enabled)
	require.True(t, *reference.Enabled, "a shared reference carries enabled: true, never the absent-means-on default")
	require.Empty(t, reference.Description)
	require.Empty(t, reference.SystemPrompt)
	require.Empty(t, reference.Model)
	require.Empty(t, reference.Tools)

	rec = f.do(t, http.MethodGet, "/api/agents/primary/main/subagents", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rows = decodeSubagentPage(t, rec).Rows
	require.True(t, *subagentRowByName(t, rows, "repo-auditor").Enabled)
	// The name is listed once: the reference entry and the registry entry are
	// the same row.
	count := 0
	for _, row := range rows {
		if row.Name == "repo-auditor" {
			count++
		}
	}
	require.Equal(t, 1, count)

	// The kind filter answers with it too.
	rec = f.do(t, http.MethodGet, "/api/agents/primary/main/subagents?kind=shared", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	filtered := decodeSubagentPage(t, rec)
	require.Len(t, filtered.Rows, 1)
	require.Equal(t, "repo-auditor", filtered.Rows[0].Name)

	// Disabling removes the entry: absence is what disabled means.
	rec = f.do(t, http.MethodPut, "/api/agents/primary/main/subagents/repo-auditor/enabled", map[string]any{"enabled": false})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	for _, entry := range f.subagentEntries(t) {
		require.NotEqual(t, "repo-auditor", entry.Name)
	}
	rec = f.do(t, http.MethodGet, "/api/agents/primary/main/subagents", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.False(t, *subagentRowByName(t, decodeSubagentPage(t, rec).Rows, "repo-auditor").Enabled)
}

// TestSubagentSharedRowWritesAreRefused pins the read-only half (plan 314): the
// definition belongs to the install-wide registry, so this page edits none of it
// — no update, no delete — while a per-agent custom subagent and a built-in
// override are still written here (ruling A).
func TestSubagentSharedRowWritesAreRefused(t *testing.T) {
	var def appcfg.AgentDefinition
	sharedRegistry := sharedSubagentFixtureConfig(&def)
	f := newSubagentRootFixture(t, func(root *appcfg.Root) {
		sharedRegistry(root)
		// A reference that enables it from the start, so the refusals are proved
		// on an enabled row rather than on the absence of one.
		main := root.Agents.Definitions["main"]
		main.Subagents = append(main.Subagents, appcfg.SubagentConfig{Name: "repo-auditor", Enabled: appcfg.BoolPtr(true)})
		root.Agents.Definitions["main"] = main
	})

	rec := f.do(t, http.MethodPut, "/api/agents/primary/main/subagents/repo-auditor", map[string]any{
		"model": "llm-1", "reasoning_effort": "low", "system_prompt": "mine now",
	})
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "is a shared subagent; edit it in Settings → Subagents, only enable/disable here")
	// Nothing was written by the refusal: the registry still carries the
	// definition the settings page owns, and the reference entry is untouched.
	auditor, ok := appcfg.SharedSubagentFor(f.config(t).Agents.SharedSubagents, "repo-auditor")
	require.True(t, ok)
	require.Equal(t, "high", auditor.ReasoningEffort)
	require.Equal(t, "You audit repositories.", auditor.SystemPrompt)
	require.True(t, *findSubagentEntry(t, f.subagentEntries(t), "repo-auditor").Enabled)

	rec = f.do(t, http.MethodDelete, "/api/agents/primary/main/subagents/repo-auditor", nil)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "is a shared subagent; delete it in Settings → Subagents")
	require.NotEmpty(t, findSubagentEntry(t, f.subagentEntries(t), "repo-auditor"))

	// A per-agent subagent of a shared name is refused too: the registry owns
	// the definition, so a second one here would be a duplicate nobody edits.
	rec = f.do(t, http.MethodPost, "/api/agents/primary/main/subagents", map[string]any{
		"name": "repo-auditor", "description": "mine", "system_prompt": "mine",
	})
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "is a shared subagent")

	// Ruling A regression: a built-in keeps its override path and a per-agent
	// custom subagent stays editable, with the shared registry present. The
	// model is a row of the install-wide table, as every pin is since plan 315.
	overrideRowID := seedGlobalModelRow(t, f, "openai", "gpt-override")
	rec = f.do(t, http.MethodPut, "/api/agents/primary/main/subagents/explore", map[string]any{
		"model": overrideRowID, "reasoning_effort": "low", "system_prompt": "search here",
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	override := findSubagentEntry(t, f.subagentEntries(t), "explore")
	require.Equal(t, overrideRowID, override.Model)
	require.Equal(t, "low", override.ReasoningEffort)
	require.Equal(t, "search here", override.SystemPrompt)

	rec = f.do(t, http.MethodPut, "/api/agents/primary/main/subagents/db-migrator", map[string]any{
		"description": "runs migrations and seeds", "system_prompt": "You migrate and seed.",
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "runs migrations and seeds", findSubagentEntry(t, f.subagentEntries(t), "db-migrator").Description)
}

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
	// The definition's prompt travels with the row: the settings page edits it,
	// and a form that opened without it would save an empty one over a real one.
	require.Equal(t, "You audit repositories.", listing.Rows[1].SystemPrompt)
	require.Nil(t, listing.Rows[1].DefaultPrompt, "a shared subagent is custom: it has no original to restore")
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
	require.Contains(t, rec.Body.String(), "thinking level requires a model")
	rec = f.do(t, http.MethodPut, "/api/agents/subagents/shared/types/repo-auditor", map[string]any{"model": rowID})
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "only a built-in subagent has an install-wide model row")

	// Clearing both halves removes the row: absent is "no global default".
	rec = f.do(t, http.MethodPut, "/api/agents/subagents/shared/types/explore", map[string]any{})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	_, ok = f.config(t).Agents.SubagentModels["explore"]
	require.False(t, ok)
}
