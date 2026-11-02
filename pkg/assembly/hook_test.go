package assembly

import (
	"context"
	"strings"
	"testing"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/hook"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

// The context panel's conversation size is counted with the session model's
// estimator when the hook has one, the unit the compaction decision uses.
func TestHookConversationTokensUseSessionEstimator(t *testing.T) {
	ctx := context.Background()
	sess := newCompactTestStore(t)
	for i := 0; i < 3; i++ {
		if _, err := sess.Append(ctx, "s1", "user", strings.Repeat("conversation text ", 40)); err != nil {
			t.Fatal(err)
		}
	}
	assemble := func(t *testing.T, h *Hook) int {
		t.Helper()
		res, ok, err := h.Assemble(ctx, hook.HookContext{Trigger: "user", SessionID: "s1"}, "next question")
		if err != nil || !ok {
			t.Fatalf("Assemble = ok %v, err %v", ok, err)
		}
		return res.ConversationTokens
	}

	byDefault := assemble(t, &Hook{Cfg: &appcfg.Root{}, Store: sess})
	if byDefault <= 0 {
		t.Fatalf("default ConversationTokens = %d, want a positive count", byDefault)
	}
	bySession := assemble(t, &Hook{
		Cfg:               &appcfg.Root{},
		Store:             sess,
		EstimatorProvider: func(string) llm.Estimator { return tenfoldEstimator{} },
	})
	if bySession <= byDefault {
		t.Fatalf("ConversationTokens with the session estimator = %d, want more than the default %d", bySession, byDefault)
	}
}

// Pressure has two levels and they are shares of the effective input window:
// with 12% left configured, 11% left warns and 14% left is fine. Both cases
// also pin that the hook measures against the same window the compaction
// threshold divides by — one denominator, not two.
func TestHookContextPressureIsAShareOfTheEffectiveWindow(t *testing.T) {
	ctx := context.Background()
	store := newCompactTestStore(t)
	est := tenfoldEstimator{}
	const target = 300000
	text := strings.Repeat("word ", 4000)
	tokens := 0
	for tokens < target {
		if _, err := store.Append(ctx, "s1", "user", text); err != nil {
			t.Fatal(err)
		}
		turns, err := store.ListRecentMessages(ctx, "s1", 400)
		if err != nil {
			t.Fatal(err)
		}
		tokens = state.TokenCountWithEstimationFor(turns, est)
	}

	assemble := func(t *testing.T, window int) AssemblyResult {
		t.Helper()
		cfg := &appcfg.Root{}
		cfg.Agents.Defaults.ContextInject.ModelContextTokens = window
		cfg.Agents.Defaults.ContextInject.WarnRemainingPercent = 12
		h := &Hook{Cfg: cfg, Store: store, EstimatorProvider: func(string) llm.Estimator { return est }}
		res, ok, err := h.Assemble(ctx, hook.HookContext{Trigger: "user", SessionID: "s1"}, "next question")
		if err != nil || !ok {
			t.Fatalf("Assemble = ok %v, err %v", ok, err)
		}
		// The compaction threshold divides by this same denominator.
		budget := state.CalculateTokenBudgetWithOptions(0, "uncatalogued-model", llm.Hit{}, state.TokenBudgetOptions{
			FallbackContextTokens: window,
		})
		if res.EffectiveInputTokens != budget.EffectiveContextWindow {
			t.Fatalf("pressure window = %d, compaction window = %d", res.EffectiveInputTokens, budget.EffectiveContextWindow)
		}
		return res
	}

	// ~11% of the window left is below the 12% warning line.
	if got := assemble(t, tokens*100/89); got.ContextPressure != "warn" {
		t.Fatalf("11%% left = %q, want warn (remaining %d of %d)", got.ContextPressure, got.RemainingContextTokens, got.EffectiveInputTokens)
	}
	// ~14% left is above it.
	if got := assemble(t, tokens*100/86); got.ContextPressure != "ok" {
		t.Fatalf("14%% left = %q, want ok (remaining %d of %d)", got.ContextPressure, got.RemainingContextTokens, got.EffectiveInputTokens)
	}
}
