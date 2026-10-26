package agent

import (
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

// LLMCalls counts physical model requests: a call whose Result says it took
// three attempts counts as three, and a failed call with no attempt count on
// its error counts as one.
func TestRecordLLMCountsPhysicalAttempts(t *testing.T) {
	stats := newRunStats("test")
	stats.recordLLM(0, &llm.Usage{InputTokens: 10, OutputTokens: 2}, 3)
	stats.recordLLM(0, nil, 0)
	summary := stats.summary(1, nil, nil)
	if summary.LLMCalls != 4 {
		t.Fatalf("LLMCalls = %d, want 3 + 1 (unknown attempts count as one)", summary.LLMCalls)
	}
}
