package llm

import "testing"

// A run's usage and its last response answer two different questions, and one
// call can make them differ: a length-continue re-sends the whole prompt, so
// every round is billed while only the last round is the request the
// conversation continues from. The accumulator must keep them apart — billing
// in the totals, the settled round in LastResponse.
func TestUsageAccumulatorSeparatesBillingFromTheSettledRequest(t *testing.T) {
	acc := NewUsageAccumulator()
	// Two rounds of fifty thousand: billed 100k, settled 50k.
	acc.ObserveUsageWithBilling(
		Usage{InputTokens: 60000, OutputTokens: 200, CacheReadInputTokens: 40000},
		Usage{InputTokens: 30000, OutputTokens: 200, CacheReadInputTokens: 20000},
	)
	acc.ObserveUsage(Usage{InputTokens: 7000, OutputTokens: 80})

	if got := acc.Snapshot(); got.InputTokens != 67000 || got.OutputTokens != 280 || got.CacheReadInputTokens != 40000 {
		t.Fatalf("snapshot = %+v, want the billed total of both calls (67000 prompt / 280 output / 40000 cached)", got)
	}
	last, ok := acc.LastResponse()
	if !ok {
		t.Fatal("expected a last response")
	}
	if last.InputTokens != 7000 || last.CacheReadInputTokens != 0 {
		t.Fatalf("last response = %+v, want the settled request of the last call (7000 uncached), not a sum over rounds", last)
	}
}

// A subagent's accumulator rolls up into its parent's, and the parent has to
// tell the same two figures apart: billing sums on both, the settled round
// survives as LastResponse.
func TestUsageAccumulatorPropagatesBothFiguresToItsParent(t *testing.T) {
	outer := NewUsageAccumulator()
	inner := NewUsageAccumulator()
	inner.linkParent(outer)
	inner.ObserveUsageWithBilling(
		Usage{InputTokens: 9000, OutputTokens: 50},
		Usage{InputTokens: 4500, OutputTokens: 50},
	)
	if got := outer.Snapshot(); got.InputTokens != 9000 || got.OutputTokens != 50 {
		t.Fatalf("outer snapshot = %+v, want the billed total 9000/50", got)
	}
	last, ok := outer.LastResponse()
	if !ok || last.InputTokens != 4500 {
		t.Fatalf("outer last response = %+v (ok=%v), want the settled 4500", last, ok)
	}
}

// An observation that carries nothing must not clear what the accumulator
// already knows: a provider that reports only an empty usage object is not a
// response.
func TestUsageAccumulatorIgnoresEmptyObservations(t *testing.T) {
	acc := NewUsageAccumulator()
	acc.ObserveUsage(Usage{InputTokens: 10, OutputTokens: 2})
	acc.ObserveUsageWithBilling(Usage{}, Usage{})
	acc.ObserveUsage(Usage{InputTokens: -5, OutputTokens: -1})
	if got := acc.Snapshot(); got.InputTokens != 10 || got.OutputTokens != 2 {
		t.Fatalf("snapshot = %+v, want the one real observation", got)
	}
	if last, _ := acc.LastResponse(); last.InputTokens != 10 {
		t.Fatalf("last response = %+v, want the one real observation", last)
	}
}
