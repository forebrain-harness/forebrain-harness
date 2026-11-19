package agent

import (
	"reflect"
	"strings"
	"testing"
)

func TestResolveSubtypeBuiltins(t *testing.T) {
	for _, name := range []string{"general-purpose", "explore", "plan", "verification"} {
		def, err := ResolveSubtype(name)
		if err != nil {
			t.Fatalf("resolve %s: %v", name, err)
		}
		if def.Source != "built-in" {
			t.Fatalf("source=%q want built-in", def.Source)
		}
		if def.SystemPrompt == "" {
			t.Fatalf("system prompt empty for %s", name)
		}
	}

	def, err := ResolveSubtype("  PLAN ")
	if err != nil {
		t.Fatalf("resolve normalized plan: %v", err)
	}
	if def.Name != "plan" {
		t.Fatalf("normalized subtype name = %q, want plan", def.Name)
	}
}

func TestResolveSubtypeUnknown(t *testing.T) {
	_, err := ResolveSubtype("missing-type")
	if err == nil {
		t.Fatal("expected error")
	}

	_, err = ResolveSubtype(" ")
	if err == nil {
		t.Fatal("expected empty subtype error")
	}
}

func TestSystemPromptForSubtype(t *testing.T) {
	if got, ok := SystemPromptForSubtype("plan"); !ok || got == "" {
		t.Fatalf("expected plan system prompt, ok=%v got=%q", ok, got)
	}
	if got, ok := SystemPromptForSubtype("missing"); ok || got != "" {
		t.Fatalf("expected missing subtype to fail, ok=%v got=%q", ok, got)
	}
}

func TestPlanSubagentPromptCarriesTheHandoffBarAndReviewRule(t *testing.T) {
	got, ok := SystemPromptForSubtype("plan")
	if !ok {
		t.Fatal("plan system prompt missing")
	}
	if !strings.Contains(got, PlanHandoffQualityPrompt) {
		t.Fatal("plan subagent must carry the handoff quality bar")
	}
	if !strings.Contains(got, codingReviewPrompt) {
		t.Fatal("plan subagent must carry the plan-review discipline")
	}
	if !strings.Contains(got, "pressure-test it against the real sources") {
		t.Fatal("plan subagent must state its independent-critique role")
	}
}

// Every built-in belongs to exactly one of the two lists the configuration and
// the subagent page work from. A new built-in that lands in neither would be
// invisible to both, and one listed in both would be offered to the model as
// configurable while the system owns it.
func TestSubagentNameListsCoverEveryBuiltin(t *testing.T) {
	seen := map[string]string{}
	for _, name := range PublicSubagentNames {
		if prev, dup := seen[name]; dup {
			t.Fatalf("%q is listed twice (in %s and PublicSubagentNames)", name, prev)
		}
		seen[name] = "PublicSubagentNames"
	}
	for _, name := range SystemSubagentNames {
		if prev, dup := seen[name]; dup {
			t.Fatalf("%q is listed twice (in %s and SystemSubagentNames)", name, prev)
		}
		seen[name] = "SystemSubagentNames"
	}
	active := ActiveTypeNames()
	for _, name := range active {
		if _, ok := seen[name]; !ok {
			t.Fatalf("built-in %q is in neither PublicSubagentNames nor SystemSubagentNames", name)
		}
	}
	if len(active) != len(seen) {
		t.Fatalf("the two lists name %d types, but %d built-ins exist: %#v", len(seen), len(active), seen)
	}
	// PublicTypeNames is the model's dispatchable set, so it has to be the
	// public list and not a slice of the shared backing array.
	names := PublicTypeNames()
	names[0] = "mutated"
	if PublicTypeNames()[0] != PublicSubagentNames[0] {
		t.Fatalf("PublicTypeNames shares its backing array with PublicSubagentNames")
	}
}

func TestPublicAndSystemTypeNamesAreSeparated(t *testing.T) {
	if got, want := PublicTypeNames(), []string{"general-purpose", "explore", "plan", "verification"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("PublicTypeNames = %#v, want %#v", got, want)
	}
	public := map[string]struct{}{}
	for _, name := range PublicTypeNames() {
		public[name] = struct{}{}
	}
	// The system-dispatched subtypes are spelled out here rather than read
	// from a production list: the assertion is that the two sets stay
	// disjoint, so reading the same list twice would prove nothing.
	for _, name := range []string{"plan-reviewer", "goal-evaluator"} {
		if _, ok := public[name]; ok {
			t.Fatalf("system subtype %q is public", name)
		}
		if got, ok := SystemPromptForSubtype(name); !ok || got == "" {
			t.Fatalf("%s system prompt missing", name)
		}
	}
}

// The paragraph a subagent is given has to match what the runtime does. When it
// claimed an outside path "will be rejected by the system", a subagent sent to
// analyse a directory outside the project never issued a call at that path — so
// no approval was raised for the user to answer — and reported on a lookalike
// file inside the project instead.
func TestFileAccessScopeSystemPromptOffersEscalationInsteadOfRefusal(t *testing.T) {
	prompt := FileAccessScopeSystemPrompt("/srv/project", "/home/u/.forebrain/workspace")

	if !strings.Contains(prompt, "/srv/project") || !strings.Contains(prompt, "/home/u/.forebrain/workspace") {
		t.Fatalf("both roots must be named: %s", prompt)
	}
	for _, forbidden := range []string{
		"will be rejected by the system",
		"Do not attempt to access files elsewhere",
		"You may ONLY read and write files under",
	} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("prompt still refuses outside paths outright (%q): %s", forbidden, prompt)
		}
	}
	for _, required := range []string{
		"gated, not forbidden",
		"raises an approval prompt",
		"require_escalated",
		"Never retarget the task",
	} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("prompt is missing %q: %s", required, prompt)
		}
	}
}
