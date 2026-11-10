package turn

import (
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

// The gauge reads the configured row when the caller names it: two rows may
// share a provider/model and carry different windows, and each surface reports
// the row the conversation actually runs on (owner 2026-11-10, plan 283 Part E).
func TestContextGaugeOfUsesTheRowsOwnWindow(t *testing.T) {
	wide, narrow := int64(64000), int64(32000)
	llm.SetModelOverrides(
		map[string]llm.ModelOverride{
			"row-wide":   {ContextWindow: &wide},
			"row-narrow": {ContextWindow: &narrow},
		},
		map[string]string{
			"row-wide":   "deepseek/deepseek-v4",
			"row-narrow": "deepseek/deepseek-v4",
		},
	)
	t.Cleanup(func() { llm.SetModelOverrides(nil, nil) })
	opts := state.TokenBudgetOptions{AutoCompactPercent: 80}

	if got := ContextGaugeOf("row-narrow", "deepseek", "deepseek-v4", 0, opts); got.WindowTokens != 32000 {
		t.Fatalf("narrow row window = %d, want 32000", got.WindowTokens)
	}
	if got := ContextGaugeOf("row-wide", "deepseek", "deepseek-v4", 0, opts); got.WindowTokens != 64000 {
		t.Fatalf("wide row window = %d, want 64000", got.WindowTokens)
	}
}
