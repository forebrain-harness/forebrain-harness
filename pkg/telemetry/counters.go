package telemetry

import (
	"sync/atomic"
)

var subagentIn, subagentOut atomic.Uint64
var forkPromptTok, forkCompletionTok atomic.Uint64
var stats = NewStatsStore()

func incCounter(c *atomic.Uint64, name string) {
	c.Add(1)
	stats.Increment("inproc_" + name)
	LogEvent("inproc.counter", CounterMetadata(name))
}

func IncSubagentEnter() { incCounter(&subagentIn, "subagent_enter") }

func IncSubagentExit() { incCounter(&subagentOut, "subagent_exit") }

func AddForkAgentUsage(promptTokens, completionTokens int) {
	if promptTokens > 0 {
		forkPromptTok.Add(uint64(promptTokens))
		stats.Increment("inproc_fork_prompt_tokens", float64(promptTokens))
	}
	if completionTokens > 0 {
		forkCompletionTok.Add(uint64(completionTokens))
		stats.Increment("inproc_fork_completion_tokens", float64(completionTokens))
	}
}
