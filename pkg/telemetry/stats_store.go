package telemetry

import (
	"sync"
)

// StatsStore holds the in-process counters the runtime increments while a
// session runs. Only counting is kept: the histogram and set-cardinality
// surfaces this type once carried had no caller.
type StatsStore struct {
	mu      sync.Mutex
	metrics map[string]float64
}

func NewStatsStore() *StatsStore {
	return &StatsStore{metrics: map[string]float64{}}
}

func (s *StatsStore) Increment(name string, value ...float64) {
	if s == nil || name == "" {
		return
	}
	inc := 1.0
	if len(value) > 0 {
		inc = value[0]
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.metrics[name] += inc
}
