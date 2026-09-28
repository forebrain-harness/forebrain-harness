package assembly

import (
	"sync"
	"time"
)

type SourceMetrics struct {
	SourceID     string    `json:"source_id"`
	HitCount     int       `json:"hit_count"`
	MissCount    int       `json:"miss_count"`
	EvictedCount int       `json:"evicted_count"`
	TotalTokens  int       `json:"total_tokens_est"`
	LastHitAt    time.Time `json:"last_hit_at,omitempty"`
}

type ContextMetrics struct {
	mu      sync.RWMutex
	sources map[string]*SourceMetrics
	runs    []RunMetric
}

type RunMetric struct {
	SessionID    string    `json:"session_id"`
	Mode         string    `json:"mode"`
	TotalTokens  int       `json:"total_tokens_est"`
	ItemCount    int       `json:"item_count"`
	EvictedCount int       `json:"evicted_count"`
	AssembledAt  time.Time `json:"assembled_at"`
}

var globalMetrics = &ContextMetrics{
	sources: make(map[string]*SourceMetrics),
}

func RecordHit(sourceID string, tokens int) {
	globalMetrics.mu.Lock()
	defer globalMetrics.mu.Unlock()
	m := getOrCreate(sourceID)
	m.HitCount++
	m.TotalTokens += tokens
	m.LastHitAt = time.Now()
}

func RecordEviction(sourceID string) {
	globalMetrics.mu.Lock()
	defer globalMetrics.mu.Unlock()
	m := getOrCreate(sourceID)
	m.EvictedCount++
}

func RecordRun(sessionID, mode string, result AssemblyResult) {
	globalMetrics.mu.Lock()
	defer globalMetrics.mu.Unlock()
	evicted := result.Budget.TotalItems - result.Budget.UsedItems
	if evicted < 0 {
		evicted = 0
	}
	globalMetrics.runs = append(globalMetrics.runs, RunMetric{
		SessionID:    sessionID,
		Mode:         mode,
		TotalTokens:  result.Budget.UsedTokens,
		ItemCount:    result.Budget.UsedItems,
		EvictedCount: evicted,
		AssembledAt:  time.Now(),
	})
	if len(globalMetrics.runs) > 200 {
		globalMetrics.runs = globalMetrics.runs[len(globalMetrics.runs)-200:]
	}
}

func getOrCreate(sourceID string) *SourceMetrics {
	m, ok := globalMetrics.sources[sourceID]
	if !ok {
		m = &SourceMetrics{SourceID: sourceID}
		globalMetrics.sources[sourceID] = m
	}
	return m
}
