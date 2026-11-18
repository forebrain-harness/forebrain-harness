// The context_budget tool: the calling agent's own context gauge, on demand.
package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"math"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

// contextBudgetDescription is written for the model that is about to guess how
// full its context is: the gauge is here, ask it.
const contextBudgetDescription = "Use when you need to know how full your context window is — before deciding to continue, wrap up, compact, or skip work. Reports the real context window, current occupancy and auto-compact budget of the model this request runs on (this agent's own context, main or subagent). Never estimate context fullness from how long the conversation feels; query this tool for the actual numbers. Read-only, no arguments."

// contextBudgetMeta is the one-line description the tool tables and cards show.
const contextBudgetMeta = "Use when you need to know how full your context window is before deciding to continue, wrap up, or compact"

// contextBudgetInput is empty on purpose: the gauge is the caller's own.
type contextBudgetInput struct{}

// contextBudgetOutput is the gauge as absolute numbers. Every amount names its
// denominator: used_percent_of_window and remaining_tokens_to_window are
// shares of the whole context window, while percent_left_before_auto_compact
// and remaining_tokens_to_auto_compact are measured against the auto-compaction
// budget — the pair the footer shows.
type contextBudgetOutput struct {
	Model                        string `json:"model"`
	ContextWindowTokens          int    `json:"context_window_tokens"`
	EffectiveInputWindowTokens   int    `json:"effective_input_window_tokens"`
	UsedTokens                   int    `json:"used_tokens"`
	UsedPercentOfWindow          int    `json:"used_percent_of_window"`
	RemainingTokensToWindow      int    `json:"remaining_tokens_to_window"`
	AutoCompactAtTokens          int    `json:"auto_compact_at_tokens"`
	RemainingTokensToAutoCompact int    `json:"remaining_tokens_to_auto_compact"`
	PercentLeftBeforeAutoCompact int    `json:"percent_left_before_auto_compact"`
}

// newContextBudgetTool builds context_budget: no arguments, one reading of the
// gauge this runtime carries (rt.ContextGauge), and the numbers a model can act
// on. It never writes anything and never asks for approval.
func newContextBudgetTool(rt *AgentToolRuntime) (*llm.Tool, error) {
	return llm.NewTool(
		"context_budget",
		contextBudgetDescription,
		func(ctx context.Context, _ *contextBudgetInput) (string, error) {
			if _, err := sessionKey(ctx); err != nil {
				return "", err
			}
			if rt == nil || rt.ContextGauge == nil {
				return "", fmt.Errorf("context_budget unavailable: this runtime has no context gauge")
			}
			payload, ok := rt.ContextGauge(ctx)
			if !ok {
				return "", fmt.Errorf("context_budget unavailable: this session has no measured context")
			}
			b, err := json.Marshal(contextBudgetOutputOf(payload))
			if err != nil {
				return "", err
			}
			return string(b), nil
		},
	)
}

func contextBudgetOutputOf(p event.TokenBudgetUpdatedPayload) contextBudgetOutput {
	usedPercent := 0
	if p.ContextWindow > 0 {
		usedPercent = int(math.Round(float64(p.TokenUsage) * 100 / float64(p.ContextWindow)))
	}
	return contextBudgetOutput{
		Model:                        p.Model,
		ContextWindowTokens:          p.ContextWindow,
		EffectiveInputWindowTokens:   p.EffectiveWindow,
		UsedTokens:                   p.TokenUsage,
		UsedPercentOfWindow:          usedPercent,
		RemainingTokensToWindow:      budgetRemaining(p.ContextWindow, p.TokenUsage),
		AutoCompactAtTokens:          p.AutoCompactThreshold,
		RemainingTokensToAutoCompact: budgetRemaining(p.AutoCompactThreshold, p.TokenUsage),
		PercentLeftBeforeAutoCompact: p.PercentLeft,
	}
}

// budgetRemaining is how much of limit is not used yet, never below zero.
func budgetRemaining(limit, used int) int {
	if left := limit - used; left > 0 {
		return left
	}
	return 0
}
