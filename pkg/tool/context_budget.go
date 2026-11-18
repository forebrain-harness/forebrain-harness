// The context_budget tool: the calling agent's own context gauge, on demand.
package tool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

// contextBudgetDescription is written for the model that is about to guess how
// full its context is: the gauge is here, ask it. It teaches the one
// derivation the two reported numbers allow — absolute headroom from the
// budget and the share of it left — instead of sending the result.
const contextBudgetDescription = "Use when you need to know how full your context window is — before deciding to continue, wrap up, compact, or skip work. Reports how much of this request's auto-compaction budget is left, as a percent, and the budget in tokens that percent is measured against, so absolute headroom = budget × percent / 100 (this agent's own context, main or subagent). Never estimate context fullness from how long the conversation feels; query this tool for the actual numbers. Read-only, no arguments."

// contextBudgetMeta is the one-line description the tool tables and cards show.
const contextBudgetMeta = "Use when you need to know how full your context window is before deciding to continue, wrap up, or compact"

// contextBudgetInput is empty on purpose: the gauge is the caller's own.
type contextBudgetInput struct{}

// contextBudgetOutput is the gauge as the two numbers a decision needs: the
// share of the auto-compaction budget still free — the proximity signal every
// continue/wrap/compact choice is made on — and that budget in tokens, the
// scale it is measured against. Absolute headroom (budget × percent / 100)
// derives from the pair, and the rest of the gauge's readings are either
// derivations of it or identities the caller already owns, so they are not
// sent. The footer and the compactor read the event payload, not this.
type contextBudgetOutput struct {
	PercentLeftBeforeAutoCompact int `json:"percent_left_before_auto_compact"`
	AutoCompactAtTokens          int `json:"auto_compact_at_tokens"`
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
	return contextBudgetOutput{
		PercentLeftBeforeAutoCompact: p.PercentLeft,
		AutoCompactAtTokens:          p.AutoCompactThreshold,
	}
}
