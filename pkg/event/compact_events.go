package event

import (
	"strings"
)

const (
	CompactEventContextCompacted = "context_compacted"
	CompactEventBudgetUpdated    = "token_budget_updated"
)

type ContextCompactedPayload struct {
	CompactionID  string `json:"compaction_id,omitempty"`
	AgentID       string `json:"agent_id,omitempty"`
	CreatedAtUTC  string `json:"created_at_utc,omitempty"`
	Trigger       string `json:"trigger,omitempty"`
	Strategy      string `json:"strategy,omitempty"`
	Reason        string `json:"reason,omitempty"`
	SummarySource string `json:"summary_source,omitempty"`
	Scope         string `json:"scope,omitempty"`
	ReplacedItems int    `json:"replaced_items,omitempty"`
	WindowNumber  int    `json:"window_number,omitempty"`
	Summary       string `json:"summary,omitempty"`
	BoundaryID    string `json:"boundary_id,omitempty"`
	TokensBefore  int    `json:"tokens_before,omitempty"`
	TokensAfter   int    `json:"tokens_after,omitempty"`
	Reactive      bool   `json:"reactive,omitempty"`
	Duration      string `json:"duration,omitempty"`
}

func (p ContextCompactedPayload) Canonicalized() ContextCompactedPayload {
	p.CompactionID = strings.TrimSpace(p.CompactionID)
	p.AgentID = strings.TrimSpace(p.AgentID)
	p.CreatedAtUTC = strings.TrimSpace(p.CreatedAtUTC)
	p.Trigger = strings.TrimSpace(p.Trigger)
	p.Strategy = strings.TrimSpace(p.Strategy)
	p.Reason = strings.TrimSpace(p.Reason)
	p.SummarySource = strings.TrimSpace(p.SummarySource)
	p.Scope = strings.TrimSpace(p.Scope)
	p.Summary = strings.TrimSpace(p.Summary)
	p.BoundaryID = strings.TrimSpace(p.BoundaryID)
	return p
}

type TokenBudgetUpdatedPayload struct {
	// AgentID is the roster key of the agent whose context this budget
	// measures; empty is the primary agent's. A subagent's gauge is that
	// subagent's own, so the event that carries it says whose it is.
	AgentID              string `json:"agent_id,omitempty"`
	Model                string `json:"model,omitempty"`
	TokenUsage           int    `json:"token_usage,omitempty"`
	PercentLeft          int    `json:"percent_left,omitempty"`
	ContextWindow        int    `json:"context_window,omitempty"`
	EffectiveWindow      int    `json:"effective_window,omitempty"`
	AutoCompactThreshold int    `json:"auto_compact_threshold,omitempty"`
}
