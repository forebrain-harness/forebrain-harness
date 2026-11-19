package config

import "testing"

// TestNormalizeLLMModelIdentity pins the one identity form the table's unique
// index and every lookup share, including the protocol default an empty
// api_path fills in.
func TestNormalizeLLMModelIdentity(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, provider, model, baseURL, apiPath string
		wantProvider, wantModel, wantBase, want string
	}{
		{"provider case-folded", "OpenAI", "gpt-4o", "https://a.test", "/responses", "openai", "gpt-4o", "https://a.test", "/responses"},
		{"trailing slashes", "openai", " gpt-4o ", " https://a.test///", "/responses/", "openai", "gpt-4o", "https://a.test", "/responses"},
		{"leading slash added", "openai", "gpt-4o", "https://a.test", "responses", "openai", "gpt-4o", "https://a.test", "/responses"},
		{"model case kept", "openai", "GPT-4o", "", "", "openai", "GPT-4o", "", "/chat/completions"},
		{"api-key provider default", "deepseek", "deepseek-chat", "https://d.test", "", "deepseek", "deepseek-chat", "https://d.test", "/chat/completions"},
		{"anthropic default", "anthropic", "claude-a", "https://x.test", "", "anthropic", "claude-a", "https://x.test", "/v1/messages"},
		{"minimax default", "minimax", "m1", "https://x.test", "", "minimax", "m1", "https://x.test", "/v1/messages"},
		{"chatgpt keeps empty", "chatgpt", "gpt-5", "https://x.test", "", "chatgpt", "gpt-5", "https://x.test", ""},
		{"opencode-go keeps empty", "opencode-go", "m1", "https://x.test", "", "opencode-go", "m1", "https://x.test", ""},
		{"empty provider stays empty", "", "", "", "", "", "", "", ""},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			provider, model, baseURL, apiPath := NormalizeLLMModelIdentity(c.provider, c.model, c.baseURL, c.apiPath)
			if provider != c.wantProvider || model != c.wantModel || baseURL != c.wantBase || apiPath != c.want {
				t.Fatalf("NormalizeLLMModelIdentity(%q, %q, %q, %q) = (%q, %q, %q, %q), want (%q, %q, %q, %q)",
					c.provider, c.model, c.baseURL, c.apiPath,
					provider, model, baseURL, apiPath,
					c.wantProvider, c.wantModel, c.wantBase, c.want)
			}
		})
	}
}

// modelViewFixture is the install two table rows and the per-type rows compose
// to: rows llm-1/llm-2, a definition naming one of them, and a built-in
// subagent type with a global row of its own.
func modelViewFixture() Root {
	return Root{
		LLMModels: []LLMModelConfig{
			{ID: "llm-1", Provider: "openai", Model: "gpt-4o", BaseURL: "https://table.test", APIKey: "table-key"},
			{ID: "llm-2", Provider: "anthropic", Model: "claude-a", BaseURL: "https://table.test", APIKey: "table-key-2"},
			{ID: "llm-3", Provider: "deepseek", Model: "deepseek-chat", BaseURL: "https://deepseek.test"},
		},
		Agents: AgentsSection{
			Definitions: map[string]AgentDefinition{
				"main":   {},
				"worker": {DefaultModel: "llm-2"},
			},
			SubagentModels: map[string]SubagentConfig{
				"explore": {Model: "llm-3", ReasoningEffort: "high"},
			},
		},
	}
}

// TestConfiguredLLMs pins the view: the table's rows in stored order, one entry
// per identity, and a copy whose mutation leaves the root alone.
func TestConfiguredLLMs(t *testing.T) {
	t.Parallel()
	cfg := modelViewFixture()
	view := ConfiguredLLMs(&cfg)
	if len(view) != 3 {
		t.Fatalf("ConfiguredLLMs = %d entries (%+v), want 3", len(view), view)
	}
	if view[0].ID != "llm-1" || view[0].Provider != "openai" || view[0].Model != "gpt-4o" || view[0].BaseURL != "https://table.test" {
		t.Fatalf("view[0] = %+v, want the first table row", view[0])
	}
	if view[2].ID != "llm-3" || view[2].Model != "deepseek-chat" || view[2].APIPath != "/chat/completions" {
		t.Fatalf("view[2] = %+v, want the third row with its protocol path", view[2])
	}
	duplicated := cfg
	duplicated.LLMModels = append(append([]LLMModelConfig(nil), cfg.LLMModels...), LLMModelConfig{
		ID: "llm-dup", Provider: "OpenAI", Model: " gpt-4o ", BaseURL: "https://table.test/",
	})
	if got := ConfiguredLLMs(&duplicated); len(got) != 3 {
		t.Fatalf("ConfiguredLLMs with a duplicate identity = %d entries (%+v), want 3", len(got), got)
	}
	if got := ConfiguredLLMs(&Root{}); len(got) != 0 {
		t.Fatalf("ConfiguredLLMs(empty root) = %+v, want none", got)
	}
	// The caller's copy is its own: mutating one entry leaves the root alone.
	view[0].APIKey = "mutated"
	if cfg.LLMModels[0].APIKey != "table-key" {
		t.Fatal("ConfiguredLLMs aliases the root's model rows")
	}
}

// TestEffectiveDefaultLLMFor pins the whole fall-back chain an empty model
// selection walks for a primary agent and for a built-in subagent type.
func TestEffectiveDefaultLLMFor(t *testing.T) {
	t.Parallel()
	// A definition naming a row id.
	fixture := modelViewFixture()
	if got := EffectiveDefaultLLMFor(&fixture, "worker"); got == nil || got.ID != "llm-2" || got.Model != "claude-a" {
		t.Fatalf("default_model naming a row = %+v, want llm-2 anthropic/claude-a", got)
	}

	// A built-in subagent type reads its global row, with the type's own
	// reasoning effort applied on top of the row.
	if got := EffectiveDefaultLLMFor(&fixture, "explore"); got == nil || got.ID != "llm-3" {
		t.Fatalf("subagent type row = %+v, want llm-3", got)
	}
	if got := ReasoningEffort(EffectiveDefaultLLMFor(&fixture, "explore").Params); got != "high" {
		t.Fatalf("subagent type effort = %q, want the type's own high", got)
	}

	// Nothing of its own: main's choice.
	fromMain := modelViewFixture()
	fromMain.Agents.Definitions["main"] = AgentDefinition{DefaultModel: "llm-1"}
	fromMain.Agents.Definitions["worker"] = AgentDefinition{}
	if got := EffectiveDefaultLLMFor(&fromMain, "worker"); got == nil || got.ID != "llm-1" {
		t.Fatalf("worker inheriting main's choice = %+v, want llm-1", got)
	}
	// An unknown agent answers the same way: main's choice decides.
	if got := EffectiveDefaultLLMFor(&fromMain, "nobody"); got == nil || got.ID != "llm-1" {
		t.Fatalf("undefined agent = %+v, want main's choice llm-1", got)
	}

	// Nothing configured anywhere but rows: the view's first row.
	rowsOnly := modelViewFixture()
	rowsOnly.Agents.Definitions["worker"] = AgentDefinition{}
	if got := EffectiveDefaultLLMFor(&rowsOnly, "worker"); got == nil || got.ID != "llm-1" {
		t.Fatalf("nothing named anywhere but rows = %+v, want the view's first row llm-1", got)
	}

	// A row id nobody carries is skipped (the model page may delete it), and
	// the next choice stands.
	danglingID := modelViewFixture()
	danglingID.Agents.Definitions["worker"] = AgentDefinition{DefaultModel: "llm-gone"}
	danglingID.Agents.Definitions["main"] = AgentDefinition{DefaultModel: "llm-3"}
	if got := EffectiveDefaultLLMFor(&danglingID, "worker"); got == nil || got.ID != "llm-3" {
		t.Fatalf("dangling row id = %+v, want the fall-back to main's llm-3", got)
	}

	// A legacy label nothing carries means "unset": the agent follows the
	// global default instead of refusing to run (owner 2026-11-16).
	danglingLabel := modelViewFixture()
	danglingLabel.Agents.Definitions["worker"] = AgentDefinition{DefaultModel: "openai/not-configured"}
	if got := EffectiveDefaultLLMFor(&danglingLabel, "worker"); got == nil || got.ID != "llm-1" {
		t.Fatalf("dangling label = %+v, want the view's first row llm-1", got)
	}

	// A label a row still carries keeps naming that row.
	label := modelViewFixture()
	label.Agents.Definitions["worker"] = AgentDefinition{DefaultModel: "Anthropic/claude-a"}
	if got := EffectiveDefaultLLMFor(&label, "worker"); got == nil || got.ID != "llm-2" {
		t.Fatalf("resolvable label = %+v, want llm-2", got)
	}

	// An install with no models configured and nothing named.
	if got := EffectiveDefaultLLMFor(&Root{}, "main"); got != nil {
		t.Fatalf("empty install = %+v, want nil", got)
	}
}

// TestHasOwnModelChoice pins what makes a key "have a choice of its own": a
// default_model reference, or a subagent type's global row.
func TestHasOwnModelChoice(t *testing.T) {
	t.Parallel()
	cfg := modelViewFixture()
	if !HasOwnModelChoice(&cfg, "worker") {
		t.Fatal("worker names a default_model but reports no own choice")
	}
	if !HasOwnModelChoice(&cfg, "explore") {
		t.Fatal("explore names a global row but reports no own choice")
	}
	if HasOwnModelChoice(&cfg, "main") {
		t.Fatal("main names nothing but reports an own choice")
	}
	empty := Root{Agents: AgentsSection{
		Definitions:    map[string]AgentDefinition{"explore": {}},
		SubagentModels: map[string]SubagentConfig{"explore": {}},
	}}
	if HasOwnModelChoice(&empty, "explore") {
		t.Fatal("a subagent type with no global row reports an own choice")
	}
}

// TestResolveModelRef pins that a reference names exactly one row: by id when
// two rows share a provider and model, by label against the whole view, and
// never by a value nothing carries.
func TestResolveModelRef(t *testing.T) {
	t.Parallel()
	cfg := Root{
		LLMModels: []LLMModelConfig{
			{ID: "llm-a", Provider: "openai", Model: "gpt-4o", BaseURL: "https://a.test"},
			{ID: "llm-b", Provider: "openai", Model: "gpt-4o", BaseURL: "https://b.test"},
		},
		Agents: AgentsSection{Definitions: map[string]AgentDefinition{"main": {}, "worker": {}}},
	}
	if got, ok := ResolveModelRef(&cfg, "worker", "llm-a"); !ok || got.BaseURL != "https://a.test" {
		t.Fatalf("ref llm-a = %+v ok=%v, want the a-endpoint row", got, ok)
	}
	if got, ok := ResolveModelRef(&cfg, "worker", "llm-b"); !ok || got.BaseURL != "https://b.test" {
		t.Fatalf("ref llm-b = %+v ok=%v, want the b-endpoint row", got, ok)
	}
	// A label is still resolvable, case-insensitively, by the first match.
	if got, ok := ResolveModelRef(&cfg, "main", "OpenAI/gpt-4o"); !ok || got.ID != "llm-a" {
		t.Fatalf("label = %+v ok=%v, want the view's first match llm-a", got, ok)
	}
	if _, ok := ResolveModelRef(&cfg, "main", "llm-gone"); ok {
		t.Fatal("a reference no row carries resolved")
	}
	if _, ok := ResolveModelRef(&cfg, "main", "openai/gone"); ok {
		t.Fatal("a label no entry carries resolved")
	}
}

// TestValidateSubagentModels pins the one verdict the per-type global rows get:
// only a public built-in may carry one, its model has to be a configured model,
// and an effort means nothing without a model.
func TestValidateSubagentModels(t *testing.T) {
	t.Parallel()
	resolves := func(ref string) bool { return ref == "llm-1" }
	cases := []struct {
		name    string
		models  map[string]SubagentConfig
		wantErr bool
	}{
		{"empty section", nil, false},
		{"row naming a configured model", map[string]SubagentConfig{"explore": {Model: "llm-1"}}, false},
		{"row left unset", map[string]SubagentConfig{"explore": {}}, false},
		{"effort with a model", map[string]SubagentConfig{"plan": {Model: "llm-1", ReasoningEffort: "high"}}, false},
		{"unknown model", map[string]SubagentConfig{"explore": {Model: "llm-gone"}}, true},
		{"effort without a model", map[string]SubagentConfig{"explore": {ReasoningEffort: "high"}}, true},
		{"custom name", map[string]SubagentConfig{"my-helper": {Model: "llm-1"}}, true},
		{"system name", map[string]SubagentConfig{"goal-evaluator": {Model: "llm-1"}}, true},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateSubagentModels(c.models, resolves)
			if (err != nil) != c.wantErr {
				t.Fatalf("ValidateSubagentModels(%v) err = %v, want error %v", c.models, err, c.wantErr)
			}
		})
	}
}

// TestConfiguredLLMsAppliesTheRowsOwnCaps pins the two row-level values that
// reach a client: the default reasoning effort (only when the params do not
// already name one) and the exact output limit.
func TestConfiguredLLMsAppliesTheRowsOwnCaps(t *testing.T) {
	maxOutput := int64(20000)
	root := &Root{LLMModels: []LLMModelConfig{
		{
			ID: "llm-effort", Provider: "zhipuai", Model: "glm-5.3", BaseURL: "https://api.z.ai/api/paas/v4",
			Caps: LLMModelCaps{DefaultEffort: "High", MaxOutput: &maxOutput},
		},
		{
			ID: "llm-explicit", Provider: "zhipuai", Model: "glm-5.3-flash", BaseURL: "https://api.z.ai/api/paas/v4",
			Params: WithReasoningEffort(nil, "low"),
			Caps:   LLMModelCaps{DefaultEffort: "high"},
		},
	}}
	view := ConfiguredLLMs(root)
	if len(view) != 2 {
		t.Fatalf("view = %+v", view)
	}
	if got := ReasoningEffort(view[0].Params); got != "high" {
		t.Fatalf("row default effort = %q, want the row's default applied (normalized)", got)
	}
	if view[0].MaxOutputTokens != 20000 {
		t.Fatalf("MaxOutputTokens = %d, want the row's own limit", view[0].MaxOutputTokens)
	}
	// An explicit effort in the params is a concrete choice and wins.
	if got := ReasoningEffort(view[1].Params); got != "low" {
		t.Fatalf("explicit effort = %q, want it untouched by the row's default", got)
	}
	if view[1].MaxOutputTokens != 0 {
		t.Fatalf("MaxOutputTokens = %d, want 0 when the row names none", view[1].MaxOutputTokens)
	}
}
