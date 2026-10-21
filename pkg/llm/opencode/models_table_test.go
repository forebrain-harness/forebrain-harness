package opencode

import "testing"

// The table stores each model's protocol redundantly with ProtocolFor. This is
// the test that keeps the two in agreement: an edit to one that misses the other
// would otherwise hard-code a wrong base URL for that model.
func TestTableProtocolsMatchProtocolFor(t *testing.T) {
	for _, tc := range []struct {
		provider string
		models   []ModelMeta
	}{
		{provider: "opencode", models: ConsoleModels},
		{provider: "opencode-go", models: GoModels},
	} {
		if len(tc.models) == 0 {
			t.Fatalf("%s table is empty", tc.provider)
		}
		for _, m := range tc.models {
			got, err := ProtocolFor(tc.provider, m.ID)
			if err != nil {
				t.Errorf("ProtocolFor(%q, %q) error: %v", tc.provider, m.ID, err)
				continue
			}
			if got != m.Protocol {
				t.Errorf("%s: %s table protocol = %q, ProtocolFor says %q", tc.provider, m.ID, m.Protocol, got)
			}
			if m.ContextWindow <= 0 {
				t.Errorf("%s: %s has no context window", tc.provider, m.ID)
			}
		}
	}
}

func TestTableIDsAreUnique(t *testing.T) {
	for _, tc := range []struct {
		provider string
		models   []ModelMeta
	}{
		{provider: "opencode", models: ConsoleModels},
		{provider: "opencode-go", models: GoModels},
	} {
		seen := map[string]bool{}
		for _, m := range tc.models {
			if seen[m.ID] {
				t.Errorf("%s table lists %q twice", tc.provider, m.ID)
			}
			seen[m.ID] = true
		}
	}
}

// The free family is what a user with no OpenCode account can run, so its
// membership is a product fact worth pinning: a listed model that needs a
// credential would send every one of those users into a 401.
func TestFreeConsoleModels(t *testing.T) {
	want := []string{
		"big-pickle",
		"beam-free",
		"glm-5.3-flash-free",
		"deepseek-v4.1-flash-free",
		"minimax-m3.1-flash-free",
		"ll-alpha-free",
		"longcat-2.5-preview-free",
		"exo-free",
		"mimo-v2.6-flash-free",
		"mimo-v2.5-free",
		"nemotron-3-ultra-free",
		"nemotron-3.5-lightning-free",
		"nemotron-3.5-super-vl-free",
	}
	got := FreeConsoleModels()
	if len(got) != len(want) {
		t.Fatalf("FreeConsoleModels returned %d models, want %d: %+v", len(got), len(want), got)
	}
	seen := map[string]bool{}
	for _, m := range got {
		if !m.Free {
			t.Errorf("%s is in the free list but not marked Free", m.ID)
		}
		seen[m.ID] = true
	}
	for _, id := range want {
		if !seen[id] {
			t.Errorf("free model %q is missing from FreeConsoleModels", id)
		}
	}
	// The list is a copy: mutating it must not reach the table.
	got[0].ID = "mutated"
	if _, ok := LookupModelMeta("opencode", "big-pickle"); !ok {
		t.Fatal("FreeConsoleModels returned the table itself; a caller's edit reached package state")
	}
}

func TestGoModelsCoverTheDocumentedSubscriptionSet(t *testing.T) {
	byProtocol := map[Protocol][]string{
		ProtocolResponses: {
			"grok-4.7", "gpt-6-luna", "gpt-5.6-luna",
			"muse-spark-1.3-contributor", "muse-spark-1.2-contributor",
		},
		ProtocolAnthropic: {
			"claude-haiku-5-5", "qwen3.8-max", "qwen3.8-flash", "qwen3.7-plus", "minimax-m3",
		},
		ProtocolGoogle: {"gemini-4-argon-contributor"},
		ProtocolChatCompletions: {
			"glm-5.3-flash", "glm-5.3", "glm-5.2", "kimi-k3", "kimi-k2.7-code",
			"longcat-2.0", "deepseek-v4.1-flash", "deepseek-v4-pro", "mimo-v2.6-flash",
			"mimo-v2.6-pro", "mimo-v2.5", "mimo-v2.5-pro", "hy4-preview", "hy3",
			"step-5-preview", "minimax-m3.1-flash-free", "longcat-2.5-preview-free", "beam-free",
		},
	}
	total := 0
	for protocol, ids := range byProtocol {
		for _, id := range ids {
			total++
			meta, ok := LookupModelMeta("opencode-go", id)
			if !ok {
				t.Errorf("Go table is missing %q", id)
				continue
			}
			if meta.Protocol != protocol {
				t.Errorf("Go table %q protocol = %q, want %q", id, meta.Protocol, protocol)
			}
		}
	}
	if len(GoModels) != total {
		t.Errorf("GoModels has %d entries, want %d (the documented subscription set)", len(GoModels), total)
	}
}

func TestLookupModelMeta(t *testing.T) {
	console, ok := LookupModelMeta("opencode", "qwen3.8-max")
	if !ok {
		t.Fatal("Console table is missing qwen3.8-max")
	}
	if console.Protocol != ProtocolChatCompletions || !console.Reasoning {
		t.Errorf("Console qwen3.8-max meta = %+v", console)
	}
	goSub, ok := LookupModelMeta("opencode-go", "qwen3.8-max")
	if !ok {
		t.Fatal("Go table is missing qwen3.8-max")
	}
	if goSub.Protocol != ProtocolAnthropic {
		t.Errorf("Go qwen3.8-max protocol = %q, want %q", goSub.Protocol, ProtocolAnthropic)
	}
	// A surface does not answer for the other surface's table.
	if meta, ok := LookupModelMeta("opencode-go", "big-pickle"); ok {
		t.Errorf("Go table answered for the Console free model: %+v", meta)
	}
	if _, ok := LookupModelMeta("opencode", "  "); ok {
		t.Error("LookupModelMeta accepted a blank id")
	}
	if _, ok := LookupModelMeta("opencode", "not-a-model"); ok {
		t.Error("LookupModelMeta invented an entry for an unknown id")
	}
	// Picker and config input arrive in mixed case.
	if _, ok := LookupModelMeta("opencode", "Big-Pickle"); !ok {
		t.Error("LookupModelMeta did not normalize case")
	}
}

func TestDefaultModelMeta(t *testing.T) {
	meta := DefaultModelMeta(" brand-new-model ")
	if meta.ID != "brand-new-model" {
		t.Errorf("DefaultModelMeta id = %q", meta.ID)
	}
	if meta.Protocol != ProtocolChatCompletions {
		t.Errorf("DefaultModelMeta protocol = %q, want %q", meta.Protocol, ProtocolChatCompletions)
	}
	if meta.ContextWindow != defaultContextWindow {
		t.Errorf("DefaultModelMeta window = %d, want %d", meta.ContextWindow, defaultContextWindow)
	}
	if meta.Free || meta.Reasoning || meta.Attachments {
		t.Errorf("DefaultModelMeta promises capabilities it cannot know: %+v", meta)
	}
}
