package llm

import "testing"

func resetObserved() {
	observedMu.Lock()
	observed = map[string]*Observation{}
	observedMu.Unlock()
}

func TestSuccessProvesLowerBoundAndProjectsEstimate(t *testing.T) {
	resetObserved()
	// Local estimator said 100k; the provider actually counted 150k.
	RecordSuccess("openai", "gpt-5", 150_000, 100_000)
	if got := Project("openai", "gpt-5", 100_000); got != 150_000 {
		t.Fatalf("projected=%d want 150000", got)
	}
	// Catalog under-reports; a proven larger prompt wins.
	if got := EffectiveWindow("openai", "gpt-5", 128_000); got != 150_000 {
		t.Fatalf("window=%d want 150000", got)
	}
}

func TestOverflowCapsWindowBelowRejectedSize(t *testing.T) {
	resetObserved()
	// Catalog claims 1M, provider rejected a 400k prompt.
	RecordOverflow("openai", "gpt-5", 400_000, 0)
	if got := EffectiveWindow("openai", "gpt-5", 1_000_000); got != 399_999 {
		t.Fatalf("window=%d want 399999", got)
	}
}

func TestOverflowWithoutUsageFallsBackToEstimate(t *testing.T) {
	resetObserved()
	// The reported case: the error carries no numbers and no usage.
	RecordOverflow("openai", "gpt-5", 0, 250_000)
	obs, ok := Get("openai", "gpt-5")
	if !ok || obs.RejectedInput != 250_000 {
		t.Fatalf("obs=%+v ok=%v", obs, ok)
	}
	if got := EffectiveWindow("openai", "gpt-5", 900_000); got != 249_999 {
		t.Fatalf("window=%d want 249999", got)
	}
}

func TestLaterSuccessClearsStaleRejection(t *testing.T) {
	resetObserved()
	RecordOverflow("anthropic", "claude", 200_000, 0)
	// Model switched to a larger window: a bigger prompt now succeeds.
	RecordSuccess("anthropic", "claude", 500_000, 400_000)
	obs, _ := Get("anthropic", "claude")
	if obs.RejectedInput != 0 {
		t.Fatalf("stale rejection kept: %+v", obs)
	}
	if got := EffectiveWindow("anthropic", "claude", 200_000); got != 500_000 {
		t.Fatalf("window=%d want 500000", got)
	}
}

func TestRejectionOutranksStaleProvenValue(t *testing.T) {
	resetObserved()
	RecordSuccess("openai", "gpt-5", 800_000, 800_000)
	RecordOverflow("openai", "gpt-5", 300_000, 0)
	if got := EffectiveWindow("openai", "gpt-5", 1_000_000); got != 299_999 {
		t.Fatalf("window=%d want 299999", got)
	}
}

func TestProjectionNeverShrinksTheEstimate(t *testing.T) {
	resetObserved()
	// Provider counted fewer tokens than estimated (heavy prompt caching).
	RecordSuccess("openai", "gpt-5", 50_000, 100_000)
	if got := Project("openai", "gpt-5", 100_000); got != 100_000 {
		t.Fatalf("projected=%d want 100000 (must not shrink)", got)
	}
}

// The overhead the estimate misses is a fixed block of prefix, so it must be
// added back, never multiplied in.
//
// Every request in a session carries the same invisible prefix: the skills
// catalogue, the memory instruction and the mode reminders are prepended below
// the layer that takes the estimate, and are frozen for the session so the
// prompt cache can hit. Read as a ratio, the first and smallest request of a
// session (a ~14k prefix behind an ~8k estimate) says "multiply everything by
// 2.6", and a 300k conversation then projects as 800k+: the 90% threshold fires
// at a third of the real window, compaction rewrites history, and the next turn
// crosses it again.
func TestOverheadIsAddedNotScaled(t *testing.T) {
	resetObserved()
	// First call of a session: a 14k invisible prefix behind an 8k estimate.
	RecordSuccess("deepseek", "deepseek-v4-flash", 22_000, 8_000)

	if got := Project("deepseek", "deepseek-v4-flash", 8_000); got != 22_000 {
		t.Fatalf("projected=%d want 22000 for the request the overhead was measured on", got)
	}
	// Same session, 300k in. The prefix has not grown, so neither may the
	// correction: a ratio would have projected 825_000 here.
	if got := Project("deepseek", "deepseek-v4-flash", 300_000); got != 314_000 {
		t.Fatalf("projected=%d want 314000; a %dx scale factor would read %d", got, 22_000/8_000, 300_000*22_000/8_000)
	}
	// A later, larger measurement raises the overhead; it never compounds.
	RecordSuccess("deepseek", "deepseek-v4-flash", 348_000, 300_000)
	if got := Project("deepseek", "deepseek-v4-flash", 300_000); got != 348_000 {
		t.Fatalf("projected=%d want 348000", got)
	}
}

func TestUnknownModelPassesEstimateAndCatalogThrough(t *testing.T) {
	resetObserved()
	if got := Project("x", "y", 1234); got != 1234 {
		t.Fatalf("projected=%d", got)
	}
	if got := EffectiveWindow("x", "y", 4321); got != 4321 {
		t.Fatalf("window=%d", got)
	}
}

// An observation belongs to one model, and a caller that names none has stated
// nothing about any of them.
//
// While a blank provider and model shared a key, every caller that could not
// name its model wrote into one bucket and read every other such caller's
// findings back: one runtime's refusal capped an unrelated runtime's window,
// and one provider's token accounting rescaled another's estimates. In tests
// the same bucket carried a rejection from one case into the next, which is how
// a compaction that should have waited for a tool result fired on the first
// message instead.
func TestObservationsAreNotSharedByCallersThatNameNoModel(t *testing.T) {
	ResetObservedForTest()
	t.Cleanup(ResetObservedForTest)

	// A refusal reported without a model must not become everyone's refusal.
	RecordOverflow("", "", 9_000, 9_000)
	if _, ok := Get("", ""); ok {
		t.Fatal("an unnamed model must carry no observation")
	}
	if got := EffectiveWindow("", "", 200_000); got != 200_000 {
		t.Fatalf("window = %d, want the catalog's %d: an unnamed refusal capped it", got, 200_000)
	}
	if got := Project("", "", 100); got != 100 {
		t.Fatalf("estimate = %d, want it unchanged by another caller's overhead", got)
	}

	// A named model still learns, and only for itself.
	RecordSuccess("openai", "gpt-x", 300, 100)
	if _, ok := Get("openai", "gpt-x"); !ok {
		t.Fatal("a named model must carry its own observation")
	}
	if _, ok := Get("openai", "gpt-y"); ok {
		t.Fatal("one model's observation must not answer for another's")
	}
	if got := Project("openai", "gpt-x", 100); got != 300 {
		t.Fatalf("projected = %d, want 300 from the overhead this model itself demonstrated", got)
	}
}
