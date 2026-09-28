package telemetry

import "testing"

func TestStatsStoreIncrementCountsAndIgnoresBlankNames(t *testing.T) {
	s := NewStatsStore()
	s.Increment("calls")
	s.Increment("calls")
	s.Increment("tokens", 40)
	s.Increment("tokens", 2)
	s.Increment("")
	var nilStore *StatsStore
	nilStore.Increment("calls")

	if got := s.metrics["calls"]; got != 2 {
		t.Fatalf("calls=%v want 2", got)
	}
	if got := s.metrics["tokens"]; got != 42 {
		t.Fatalf("tokens=%v want 42", got)
	}
	if _, ok := s.metrics[""]; ok {
		t.Fatal("a blank metric name must be ignored")
	}
}
