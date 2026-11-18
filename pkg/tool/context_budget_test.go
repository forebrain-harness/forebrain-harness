package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

// contextBudgetPayloadFixture is a measured 1M-window session: 333079 tokens
// used out of 900000 before auto-compaction (the reading the owner's report
// screen showed as "63%/1M").
func contextBudgetPayloadFixture() event.TokenBudgetUpdatedPayload {
	return event.TokenBudgetUpdatedPayload{
		Model:                "deepseek-v4.1-flash",
		TokenUsage:           333079,
		PercentLeft:          63,
		ContextWindow:        1000000,
		EffectiveWindow:      1000000,
		AutoCompactThreshold: 900000,
	}
}

func contextBudgetTestTool(t *testing.T, gauge func(context.Context) (event.TokenBudgetUpdatedPayload, bool)) *llm.Tool {
	t.Helper()
	tl, err := newContextBudgetTool(&AgentToolRuntime{ContextGauge: gauge})
	if err != nil {
		t.Fatalf("newContextBudgetTool: %v", err)
	}
	return tl
}

// The numbers a model acts on: both denominators stated, in tokens.
func TestContextBudgetReportsAbsoluteTokens(t *testing.T) {
	payload := contextBudgetPayloadFixture()
	tl := contextBudgetTestTool(t, func(context.Context) (event.TokenBudgetUpdatedPayload, bool) {
		return payload, true
	})
	ctx := llm.WithAgentSessionID(context.Background(), "session-1")
	raw, err := tl.Handle(ctx, `{}`)
	if err != nil {
		t.Fatalf("context_budget: %v", err)
	}
	out, ok := raw.(string)
	if !ok {
		t.Fatalf("context_budget result type = %T, want string", raw)
	}
	var got contextBudgetOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("context_budget output %q: %v", out, err)
	}
	want := contextBudgetOutput{
		Model:                        "deepseek-v4.1-flash",
		ContextWindowTokens:          1000000,
		EffectiveInputWindowTokens:   1000000,
		UsedTokens:                   333079,
		UsedPercentOfWindow:          33,
		RemainingTokensToWindow:      666921,
		AutoCompactAtTokens:          900000,
		RemainingTokensToAutoCompact: 566921,
		PercentLeftBeforeAutoCompact: 63,
	}
	if got != want {
		t.Fatalf("context_budget = %+v, want %+v", got, want)
	}
}

// A fresh context is a legitimate reading, not an error: the whole window.
func TestContextBudgetFreshContextShowsWholeWindow(t *testing.T) {
	tl := contextBudgetTestTool(t, func(context.Context) (event.TokenBudgetUpdatedPayload, bool) {
		return event.TokenBudgetUpdatedPayload{
			Model:                "glm-5.3",
			PercentLeft:          100,
			ContextWindow:        200000,
			EffectiveWindow:      200000,
			AutoCompactThreshold: 180000,
		}, true
	})
	raw, err := tl.Handle(llm.WithAgentSessionID(context.Background(), "session-fresh"), `{}`)
	if err != nil {
		t.Fatalf("context_budget: %v", err)
	}
	var got contextBudgetOutput
	if err := json.Unmarshal([]byte(raw.(string)), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.UsedTokens != 0 || got.UsedPercentOfWindow != 0 || got.RemainingTokensToWindow != 200000 {
		t.Fatalf("fresh context = %+v, want 0 used of 200000", got)
	}
	if got.PercentLeftBeforeAutoCompact != 100 || got.RemainingTokensToAutoCompact != 180000 {
		t.Fatalf("fresh context budget = %+v, want the whole 180000 auto-compact budget", got)
	}
}

// A runtime built without a gauge says so, rather than guessing.
func TestContextBudgetWithoutGaugeFails(t *testing.T) {
	tl, err := newContextBudgetTool(&AgentToolRuntime{})
	if err != nil {
		t.Fatalf("newContextBudgetTool: %v", err)
	}
	_, err = tl.Handle(llm.WithAgentSessionID(context.Background(), "session-1"), `{}`)
	if err == nil || !strings.Contains(err.Error(), "context_budget unavailable") {
		t.Fatalf("context_budget without a gauge = %v, want an unavailable error", err)
	}
}

// A call outside an agent session has nobody's context to report.
func TestContextBudgetRequiresBoundSession(t *testing.T) {
	tl := contextBudgetTestTool(t, func(context.Context) (event.TokenBudgetUpdatedPayload, bool) {
		return contextBudgetPayloadFixture(), true
	})
	_, err := tl.Handle(context.Background(), `{}`)
	if err == nil || !strings.Contains(err.Error(), "agent session not bound") {
		t.Fatalf("context_budget without a session = %v, want agent session not bound", err)
	}
}

// The tool reaches the model's tool table as a read-only, concurrency-safe
// entry: without metadata the scheduler would serialize every call beside it.
func TestContextBudgetIsRegisteredReadOnly(t *testing.T) {
	home := t.TempDir()
	st := NewState(home)
	a, err := agent.New(noopLLM{}, "test", "test")
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	rt := &AgentToolRuntime{
		Home: home, WorkspaceRoot: home, Cfg: &appcfg.Root{},
		ContextGauge: func(context.Context) (event.TokenBudgetUpdatedPayload, bool) {
			return contextBudgetPayloadFixture(), true
		},
	}
	if err := RegisterDefaultTools(a, st, rt); err != nil {
		t.Fatalf("RegisterDefaultTools: %v", err)
	}
	if _, ok := registeredTools(t, a)["context_budget"]; !ok {
		t.Fatal("context_budget missing from the tool table")
	}
	for _, meta := range st.ToolMetas() {
		if meta.Name != "context_budget" {
			continue
		}
		if !meta.ReadOnly || !meta.ConcurrencySafe {
			t.Fatalf("context_budget metadata = %+v, want read-only and concurrency-safe", meta)
		}
		return
	}
	t.Fatal("context_budget missing from the tool metadata")
}
