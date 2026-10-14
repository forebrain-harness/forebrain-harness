package run

import (
	"context"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

// withSessionModel carries a session model selection on the context, the way
// a turn does once a conversation has a stored (or explicit) choice.
func withSessionModel(provider, model string) context.Context {
	return WithSessionModelSelection(context.Background(), PrimaryModelState{Provider: provider, Model: model})
}

// TestImageInputRefusalRejectsTextOnlyModels pins the gate's reason to exist:
// a genuinely text-only model must answer with an actionable refusal (naming
// the model and pointing at /model) instead of the provider rejecting the turn
// with a 400 once the pixels are already sent.
func TestImageInputRefusalRejectsTextOnlyModels(t *testing.T) {
	// The real case owner confirmed on 2026-10-14: deepseek-v4-pro is a
	// text-only model that is selectable in his provider list. TestMain
	// installs the repository catalog, so this exercises the shipped data.
	got := (&Runner{}).ImageInputRefusal(withSessionModel("deepseek", "deepseek-v4-pro"))
	if got == "" {
		t.Fatal("expected a refusal for the text-only deepseek/deepseek-v4-pro")
	}
	if !strings.Contains(got, "deepseek-v4-pro") || !strings.Contains(got, "/model") {
		t.Fatalf("refusal=%q want it to name the model and point at /model", got)
	}

	// The same judgement on a synthetic text-only entry, so the assertion does
	// not rest on the contents of the data file. The repository catalog is
	// restored afterwards.
	previous, err := llm.RepoModelsJSONForTest()
	if err != nil {
		t.Fatalf("read repo models.json: %v", err)
	}
	synthetic, err := llm.Parse([]byte(`{"acme/text-only":{"id":"acme/text-only","attachment":false,"limit":{"context":1000,"output":100}}}`))
	if err != nil {
		t.Fatalf("parse synthetic catalog: %v", err)
	}
	llm.SetGlobalCatalogForTest(synthetic)
	t.Cleanup(func() {
		if restored, err := llm.Parse(previous); err == nil {
			llm.SetGlobalCatalogForTest(restored)
		}
	})
	if got := (&Runner{}).ImageInputRefusal(withSessionModel("acme", "text-only")); got == "" {
		t.Fatal("expected a refusal for a synthetic attachment:false model")
	}
}

// TestImageInputRefusalAllowsUnknownModels pins the fail-open rule: the
// catalog cannot tell a stale "no" from a correct one, so a model it does not
// know (a custom base_url with a custom name) is allowed through.
func TestImageInputRefusalAllowsUnknownModels(t *testing.T) {
	if got := (&Runner{}).ImageInputRefusal(withSessionModel("custom", "a-model-nobody-tabulated")); got != "" {
		t.Fatalf("unknown model must be allowed, got %q", got)
	}
}

// TestImageInputRefusalFallsBackToPrimaryModel pins the second source of the
// session model: when the context carries no selection (a session with no
// stored row, or a call outside a turn), the gate judges the runner's current
// selection.
func TestImageInputRefusalFallsBackToPrimaryModel(t *testing.T) {
	cfg := newPrimaryModelRoot(primaryModelEntry("deepseek", "deepseek-v4-pro", "k", "https://api.deepseek.com", nil))
	r := &Runner{Deps: &Deps{AppCfg: cfg, AgentName: "main"}}
	if got := r.ImageInputRefusal(context.Background()); got == "" {
		t.Fatal("expected the runner's configured text-only model to be judged without a context selection")
	}
}
