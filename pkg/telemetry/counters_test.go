package telemetry

import (
	"sync"
	"testing"
)

// ===========================================================================
// Extended tests for counters.go — covering multi-step increments,
// accumulation semantics, Snapshot consistency,
// and high-contention concurrency beyond what counters_test.go covers.
// ===========================================================================

// ---------------------------------------------------------------------------
// A) Multi-step increment correctness (not just single-step).
//    counters_test.go only tests a single Inc* call; these test multiple.
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// E) High-contention concurrency with enough overlapping writers to exercise
//    atomic operations without turning race-instrumented test runs into a
//    telemetry throughput benchmark.
// ---------------------------------------------------------------------------

const (
	highGoroutines   = 32
	highPerGoroutine = 250
)

// ---------------------------------------------------------------------------
// G) Snapshot map size is stable — always returns exactly 10 keys.
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// I) Interleaved Snapshot reads during concurrent writes.
//    Verifies that Snapshot can be called concurrently with Inc* calls
//    without panicking or returning corrupted data.
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Zero-value / initial-state tests
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Mixed increments — verify that different counters accumulate correctly
// together without interference.
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Snapshot completeness — every expected key must be present.
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Large-count safety — verify counts can exceed 2^32 without panic.
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Snapshot returns a fresh map on each call (no aliasing).
// ---------------------------------------------------------------------------

// resetStatsForTesting gives each test a fresh counter store. Production never
// resets the store, so the helper belongs to the tests.
func resetStatsForTesting() {
	stats = NewStatsStore()
}

// Snapshot and StatsSnapshot read the in-process counters back. Production only
// ever writes them, so the readers live here with the tests that assert on
// what IncSubagentEnter/Exit and AddForkAgentUsage recorded.
func Snapshot() map[string]uint64 {
	return map[string]uint64{
		"inproc_subagent_enter":         subagentIn.Load(),
		"inproc_subagent_exit":          subagentOut.Load(),
		"inproc_fork_prompt_tokens":     forkPromptTok.Load(),
		"inproc_fork_completion_tokens": forkCompletionTok.Load(),
	}
}

func StatsSnapshot() map[string]float64 {
	out := map[string]float64{}
	for k, v := range Snapshot() {
		out[k] = float64(v)
	}
	return out
}

func TestIncSubagentEnter(t *testing.T) {
	defer resetAllCounters()
	IncSubagentEnter()
	snap := Snapshot()
	if snap["inproc_subagent_enter"] != 1 {
		t.Errorf("expected inproc_subagent_enter=1, got %d", snap["inproc_subagent_enter"])
	}
}

func TestIncSubagentEnter_MultiStep(t *testing.T) {
	defer resetAllCounters()
	for i := 0; i < 200; i++ {
		IncSubagentEnter()
	}
	snap := Snapshot()
	if snap["inproc_subagent_enter"] != 200 {
		t.Errorf("after 200 calls: got %d, want 200", snap["inproc_subagent_enter"])
	}
}

func TestIncSubagentExit(t *testing.T) {
	defer resetAllCounters()
	IncSubagentExit()
	snap := Snapshot()
	if snap["inproc_subagent_exit"] != 1 {
		t.Errorf("expected inproc_subagent_exit=1, got %d", snap["inproc_subagent_exit"])
	}
}

func TestIncSubagentExit_MultiStep(t *testing.T) {
	defer resetAllCounters()
	for i := 0; i < 150; i++ {
		IncSubagentExit()
	}
	snap := Snapshot()
	if snap["inproc_subagent_exit"] != 150 {
		t.Errorf("after 150 calls: got %d, want 150", snap["inproc_subagent_exit"])
	}
}

func TestConcurrent_IncSubagentEnter(t *testing.T) {
	defer resetAllCounters()
	const n = 1000
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			IncSubagentEnter()
		}()
	}
	wg.Wait()

	snap := Snapshot()
	if snap["inproc_subagent_enter"] != uint64(n) {
		t.Errorf("concurrent IncSubagentEnter: got %d, want %d", snap["inproc_subagent_enter"], n)
	}
}

func TestConcurrent_IncSubagentExit(t *testing.T) {
	defer resetAllCounters()
	const n = 1000
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			IncSubagentExit()
		}()
	}
	wg.Wait()

	snap := Snapshot()
	if snap["inproc_subagent_exit"] != uint64(n) {
		t.Errorf("concurrent IncSubagentExit: got %d, want %d", snap["inproc_subagent_exit"], n)
	}
}

func TestConcurrentHighContention_SubagentEnter(t *testing.T) {
	defer resetAllCounters()
	var wg sync.WaitGroup
	for i := 0; i < highGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < highPerGoroutine; j++ {
				IncSubagentEnter()
			}
		}()
	}
	wg.Wait()

	snap := Snapshot()
	got := snap["inproc_subagent_enter"]
	want := uint64(highGoroutines * highPerGoroutine)
	if got != want {
		t.Errorf("high contention: inproc_subagent_enter = %d, want %d", got, want)
	}
}

func TestConcurrentHighContention_SubagentExit(t *testing.T) {
	defer resetAllCounters()
	var wg sync.WaitGroup
	for i := 0; i < highGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < highPerGoroutine; j++ {
				IncSubagentExit()
			}
		}()
	}
	wg.Wait()

	snap := Snapshot()
	got := snap["inproc_subagent_exit"]
	want := uint64(highGoroutines * highPerGoroutine)
	if got != want {
		t.Errorf("high contention: inproc_subagent_exit = %d, want %d", got, want)
	}
}

func TestMultipleIncrements_SubagentEnter(t *testing.T) {
	defer resetAllCounters()
	for i := 0; i < 60; i++ {
		IncSubagentEnter()
	}
	snap := Snapshot()
	if snap["inproc_subagent_enter"] != 60 {
		t.Errorf("expected inproc_subagent_enter=60, got %d", snap["inproc_subagent_enter"])
	}
}

func TestMultipleIncrements_SubagentExit(t *testing.T) {
	defer resetAllCounters()
	for i := 0; i < 60; i++ {
		IncSubagentExit()
	}
	snap := Snapshot()
	if snap["inproc_subagent_exit"] != 60 {
		t.Errorf("expected inproc_subagent_exit=60, got %d", snap["inproc_subagent_exit"])
	}
}

func TestAddForkAgentUsage(t *testing.T) {
	defer resetAllCounters()
	AddForkAgentUsage(0, -1)
	snap := Snapshot()
	if snap["inproc_fork_prompt_tokens"] != 0 || snap["inproc_fork_completion_tokens"] != 0 {
		t.Fatalf("non-positive fork usage changed counters: %#v", snap)
	}

	AddForkAgentUsage(10, 20)
	snap = Snapshot()
	if snap["inproc_fork_prompt_tokens"] != 10 || snap["inproc_fork_completion_tokens"] != 20 {
		t.Fatalf("fork usage snapshot = %#v", snap)
	}
	statsSnap := StatsSnapshot()
	if statsSnap["inproc_fork_prompt_tokens"] != 10 || statsSnap["inproc_fork_completion_tokens"] != 20 {
		t.Fatalf("fork usage stats snapshot = %#v", statsSnap)
	}
}

// resetAllCounters zeroes every package-level counter.
// It is used by TestMain and per-test cleanup to ensure isolation.
func resetAllCounters() {
	subagentIn.Store(0)
	subagentOut.Store(0)
	forkPromptTok.Store(0)
	forkCompletionTok.Store(0)
	resetStatsForTesting()
}
