package agent

import (
	"reflect"
	"strings"
	"testing"
)

func TestResolveSubtypeBuiltins(t *testing.T) {
	for _, name := range []string{"general-purpose", "explore", "plan", "verification", "cavecrew-investigator", "cavecrew-builder", "cavecrew-reviewer", "guardian"} {
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

func TestPublicAndCavecrewPrivateTypeNamesAreSeparated(t *testing.T) {
	if got, want := PublicTypeNames(), []string{"general-purpose", "explore", "plan", "verification"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("PublicTypeNames = %#v, want %#v", got, want)
	}
	public := map[string]struct{}{}
	for _, name := range PublicTypeNames() {
		public[name] = struct{}{}
	}
	// The reserved subtypes are spelled out here rather than read from a
	// production list: nothing in the runtime enumerates them, so a list living
	// in production would exist only for this assertion.
	for _, name := range cavecrewPrivateTypeNames {
		if _, ok := public[name]; ok {
			t.Fatalf("cavecrew-private subtype %q is public", name)
		}
	}
	if got, ok := SystemPromptForSubtype("goal-evaluator"); !ok || got == "" {
		t.Fatalf("goal-evaluator system prompt missing")
	}
	if got, ok := SystemPromptForSubtype("guardian"); !ok || got == "" {
		t.Fatalf("guardian system prompt missing")
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

// cavecrewPrivateTypeNames are the built-in subtypes reserved for cavecrew.
// They resolve to a definition but must never surface in the public
// subagent_type enum.
var cavecrewPrivateTypeNames = []string{"cavecrew-investigator", "cavecrew-builder", "cavecrew-reviewer"}
