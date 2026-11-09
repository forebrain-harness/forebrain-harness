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

// TestLegacyLLMModelID pins that the id a pre-table entry gets is stable
// across spellings of one identity — plan 262's migration writes the same id
// into fb_llm_models, so a reference recorded now must not drift.
func TestLegacyLLMModelID(t *testing.T) {
	t.Parallel()
	same := []struct{ provider, model, baseURL, apiPath string }{
		{"openai", "gpt-4o", "https://a.test", "/chat/completions"},
		{"OpenAI", " gpt-4o ", "https://a.test/", ""},
		{"openai", "gpt-4o", "https://a.test", "chat/completions/"},
	}
	want := LegacyLLMModelID(same[0].provider, same[0].model, same[0].baseURL, same[0].apiPath)
	if len(want) != len(LegacyLLMModelIDPrefix)+16 || want[:len(LegacyLLMModelIDPrefix)] != LegacyLLMModelIDPrefix {
		t.Fatalf("LegacyLLMModelID = %q, want %q followed by 16 hex digits", want, LegacyLLMModelIDPrefix)
	}
	for _, c := range same[1:] {
		if got := LegacyLLMModelID(c.provider, c.model, c.baseURL, c.apiPath); got != want {
			t.Fatalf("same identity spelled (%q, %q, %q, %q) = %q, want %q", c.provider, c.model, c.baseURL, c.apiPath, got, want)
		}
	}
	if other := LegacyLLMModelID("openai", "gpt-4o", "https://b.test", "/chat/completions"); other == want {
		t.Fatal("a different base_url got the same id")
	}
}

// modelViewFixture is the install two table rows and two legacy lists compose
// to: rows llm-1/llm-2, main's list (an empty placeholder, an entry naming
// llm-1's endpoint, and a two-model entry), and worker's own entry.
func modelViewFixture() Root {
	return Root{
		LLMModels: []LLMModelConfig{
			{ID: "llm-1", Provider: "openai", Model: "gpt-4o", BaseURL: "https://table.test", APIKey: "table-key"},
			{ID: "llm-2", Provider: "anthropic", Model: "claude-a", BaseURL: "https://table.test", APIKey: "table-key-2"},
		},
		Agents: AgentsSection{Definitions: map[string]AgentDefinition{
			"main": {
				LLMProviders: []AgentLLMProviderConfig{
					{},
					{Provider: "openai", Model: "gpt-4o", BaseURL: "https://table.test", APIKey: "legacy-same-endpoint"},
					{Provider: "deepseek", Models: StringList{"deepseek-chat", "deepseek-reasoner"}, BaseURL: "https://deepseek.test"},
				},
			},
			"worker": {
				LLMProviders: []AgentLLMProviderConfig{
					{Provider: "openai", Model: "gpt-4o", BaseURL: "https://worker.test", APIKey: "worker-key"},
				},
			},
		}},
	}
}

// TestConfiguredLLMs pins the view's order and its two merge rules: a table
// row comes first and beats a legacy entry naming the same endpoint, and the
// empty placeholder entry carries no identity at all.
func TestConfiguredLLMs(t *testing.T) {
	t.Parallel()
	cfg := modelViewFixture()
	view := ConfiguredLLMs(&cfg)
	// Two rows, main's deepseek pair (its openai entry is the same endpoint as
	// llm-1 and its placeholder is no identity), then worker's entry.
	if len(view) != 5 {
		t.Fatalf("ConfiguredLLMs = %d entries (%+v), want 5", len(view), view)
	}
	if view[0].ID != "llm-1" || view[0].Provider != "openai" || view[0].Model != "gpt-4o" || view[0].BaseURL != "https://table.test" {
		t.Fatalf("view[0] = %+v, want the first table row", view[0])
	}
	if view[1].ID != "llm-2" || view[1].Provider != "anthropic" || view[1].Model != "claude-a" {
		t.Fatalf("view[1] = %+v, want the second table row", view[1])
	}
	deepseek := view[2]
	if deepseek.Provider != "deepseek" || deepseek.Model != "deepseek-chat" || deepseek.APIPath != "/chat/completions" {
		t.Fatalf("view[2] = %+v, want main's first expanded deepseek entry", deepseek)
	}
	if deepseek.ID != LegacyLLMModelID("deepseek", "deepseek-chat", "https://deepseek.test", "") {
		t.Fatalf("view[2].ID = %q, want the legacy id of the lifted entry", deepseek.ID)
	}
	if view[3].Provider != "deepseek" || view[3].Model != "deepseek-reasoner" {
		t.Fatalf("view[3] = %+v, want main's second expanded deepseek entry", view[3])
	}
	if view[4].BaseURL != "https://worker.test" || view[4].APIKey != "worker-key" {
		t.Fatalf("view[4] = %+v, want worker's own entry last", view[4])
	}
	for _, entry := range view {
		if entry.Provider == "openai" && entry.BaseURL == "https://table.test" && entry.ID != "llm-1" {
			t.Fatalf("a duplicate endpoint got id %q, want the table row's llm-1", entry.ID)
		}
		if entry.Provider == "" && entry.Model == "" {
			t.Fatalf("the empty placeholder entry reached the view: %+v", entry)
		}
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
// selection walks, and the one case that stops it: a legacy label naming
// nothing.
func TestEffectiveDefaultLLMFor(t *testing.T) {
	t.Parallel()
	// A row id the view carries.
	rowsOnly := modelViewFixture()
	rowsOnly.Agents.Definitions["worker"] = AgentDefinition{DefaultModel: "llm-2"}
	if got := EffectiveDefaultLLMFor(&rowsOnly, "worker"); got == nil || got.ID != "llm-2" || got.Model != "claude-a" {
		t.Fatalf("default_model naming a row = %+v, want llm-2 anthropic/claude-a", got)
	}

	// A legacy label resolves inside the agent's own list, not main's entry
	// naming the same pair on another endpoint.
	ownLabel := modelViewFixture()
	worker := ownLabel.Agents.Definitions["worker"]
	worker.DefaultModel = "openai/gpt-4o"
	ownLabel.Agents.Definitions["worker"] = worker
	if got := EffectiveDefaultLLMFor(&ownLabel, "worker"); got == nil || got.BaseURL != "https://worker.test" || got.APIKey != "worker-key" {
		t.Fatalf("legacy label = %+v, want worker's own entry", got)
	}
	// Same label, no own list: the view answers, so the run falls back to the
	// first model the install has rather than refusing to start.
	noOwnList := modelViewFixture()
	noOwnList.Agents.Definitions["worker"] = AgentDefinition{DefaultModel: "openai/gpt-4o"}
	if got := EffectiveDefaultLLMFor(&noOwnList, "worker"); got == nil || got.ID != "llm-1" {
		t.Fatalf("legacy label without an own list = %+v, want the view's first row llm-1", got)
	}

	// No default_model, an own list: the agent's first entry.
	ownFirst := modelViewFixture()
	ownFirst.Agents.Definitions["worker"] = AgentDefinition{LLMProviders: []AgentLLMProviderConfig{
		{Provider: "minimax", Model: "m1", BaseURL: "https://worker.test"},
	}}
	if got := EffectiveDefaultLLMFor(&ownFirst, "worker"); got == nil || got.Provider != "minimax" {
		t.Fatalf("own list = %+v, want worker's first entry minimax", got)
	}

	// Nothing of its own: main's first entry.
	fromMain := modelViewFixture()
	fromMain.Agents.Definitions["worker"] = AgentDefinition{}
	if got := EffectiveDefaultLLMFor(&fromMain, "worker"); got == nil || got.Model != "gpt-4o" || got.BaseURL != "https://table.test" {
		t.Fatalf("worker inheriting main's list = %+v, want main's first entry openai/gpt-4o", got)
	}

	// Main names a default: it resolves against the whole view.
	mainDefault := Root{
		LLMModels: []LLMModelConfig{
			{ID: "llm-a", Provider: "openai", Model: "gpt-4o", BaseURL: "https://a.test"},
			{ID: "llm-b", Provider: "anthropic", Model: "claude-a", BaseURL: "https://b.test"},
		},
		Agents: AgentsSection{Definitions: map[string]AgentDefinition{
			"main": {DefaultModel: "anthropic/claude-a"},
			// No definition at all: the resolution starts at main's choice.
			"worker": {},
		}},
	}
	if got := EffectiveDefaultLLMFor(&mainDefault, "worker"); got == nil || got.ID != "llm-b" {
		t.Fatalf("worker inheriting main's default_model = %+v, want llm-b", got)
	}
	if got := EffectiveDefaultLLMFor(&mainDefault, "nobody"); got == nil || got.ID != "llm-b" {
		t.Fatalf("undefined agent = %+v, want main's default llm-b", got)
	}

	// Main has nothing but the placeholder: the view's first row.
	rowsOnlyMain := modelViewFixture()
	rowsOnlyMain.Agents.Definitions["main"] = AgentDefinition{LLMProviders: []AgentLLMProviderConfig{{}}}
	rowsOnlyMain.Agents.Definitions["worker"] = AgentDefinition{}
	if got := EffectiveDefaultLLMFor(&rowsOnlyMain, "worker"); got == nil || got.ID != "llm-1" {
		t.Fatalf("nothing configured anywhere but rows = %+v, want the view's first row llm-1", got)
	}

	// A row id nobody carries is skipped, the next choice stands.
	danglingID := modelViewFixture()
	workerDef := danglingID.Agents.Definitions["worker"]
	workerDef.DefaultModel = "llm-gone"
	workerDef.LLMProviders = []AgentLLMProviderConfig{{Provider: "minimax", Model: "m2", BaseURL: "https://worker.test"}}
	danglingID.Agents.Definitions["worker"] = workerDef
	if got := EffectiveDefaultLLMFor(&danglingID, "worker"); got == nil || got.Model != "m2" {
		t.Fatalf("dangling row id = %+v, want the agent's own first entry minimax/m2", got)
	}

	// A legacy label naming nothing is the loader's verdict, not a fall-back.
	danglingLabel := modelViewFixture()
	broken := danglingLabel.Agents.Definitions["worker"]
	broken.DefaultModel = "openai/not-configured"
	danglingLabel.Agents.Definitions["worker"] = broken
	if got := EffectiveDefaultLLMFor(&danglingLabel, "worker"); got != nil {
		t.Fatalf("dangling label = %+v, want nil", got)
	}

	// An install with no models configured and nothing named.
	if got := EffectiveDefaultLLMFor(&Root{}, "main"); got != nil {
		t.Fatalf("empty install = %+v, want nil", got)
	}
}

// TestResolveModelRef pins that a reference names exactly one row: by id when
// two rows share a provider and model, by label inside the list that agent
// wrote it against, and by the view's first match otherwise.
func TestResolveModelRef(t *testing.T) {
	t.Parallel()
	cfg := Root{
		LLMModels: []LLMModelConfig{
			{ID: "llm-a", Provider: "openai", Model: "gpt-4o", BaseURL: "https://a.test"},
			{ID: "llm-b", Provider: "openai", Model: "gpt-4o", BaseURL: "https://b.test"},
		},
		Agents: AgentsSection{Definitions: map[string]AgentDefinition{
			"main": {},
			"worker": {LLMProviders: []AgentLLMProviderConfig{
				{Provider: "openai", Model: "gpt-4o", BaseURL: "https://worker.test"},
			}},
		}},
	}
	if got, ok := ResolveModelRef(&cfg, "worker", "llm-a"); !ok || got.BaseURL != "https://a.test" {
		t.Fatalf("ref llm-a = %+v ok=%v, want the a-endpoint row", got, ok)
	}
	if got, ok := ResolveModelRef(&cfg, "worker", "llm-b"); !ok || got.BaseURL != "https://b.test" {
		t.Fatalf("ref llm-b = %+v ok=%v, want the b-endpoint row", got, ok)
	}
	if got, ok := ResolveModelRef(&cfg, "worker", "OpenAI/gpt-4o"); !ok || got.BaseURL != "https://worker.test" {
		t.Fatalf("label = %+v ok=%v, want worker's own entry", got, ok)
	}
	if got, ok := ResolveModelRef(&cfg, "main", "openai/gpt-4o"); !ok || got.ID != "llm-a" {
		t.Fatalf("label without an own list = %+v ok=%v, want the view's first match llm-a", got, ok)
	}
	if _, ok := ResolveModelRef(&cfg, "main", "llm-gone"); ok {
		t.Fatal("a reference no row carries resolved")
	}
	if _, ok := ResolveModelRef(&cfg, "main", "openai/gone"); ok {
		t.Fatal("a label no entry carries resolved")
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
