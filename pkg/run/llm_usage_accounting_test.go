package run

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

// failedUsageLLM fails every call with the usage of that attempt attached,
// the way a provider adapter reports a death after the billing frame.
type failedUsageLLM struct {
	usages []llm.Usage
	calls  int
}

func (f *failedUsageLLM) Execute(context.Context, []llm.Message, []*llm.Tool) (*llm.Result, error) {
	u := f.usages[f.calls%len(f.usages)]
	f.calls++
	return nil, llm.NewExecuteError(errors.New("attempt failed"), &u)
}

// The accounting wrapper sits inside the retry loops, so every physical
// attempt's usage — the dead ones' included — reaches the run's accumulator
// the moment the attempt dies. That is the billing view: a retried attempt's
// tokens are spent whether the request eventually succeeds or not.
func TestFailedAttemptUsageReachesTheAccumulator(t *testing.T) {
	acc := &internalLLMUsageAccumulator{}
	ctx := WithInternalLLMUsageAccumulator(context.Background(), acc)
	retryMW, err := llm.NewRetry(2, llm.WithMaxBackoff(0))
	if err != nil {
		t.Fatalf("NewRetry: %v", err)
	}
	inner := &failedUsageLLM{usages: []llm.Usage{
		{InputTokens: 100, OutputTokens: 10},
		{InputTokens: 110, OutputTokens: 12},
	}}
	wrapped := llm.Use(WrapUsageAccountingLLMForTest(inner), retryMW)
	_, execErr := wrapped.Execute(ctx, nil, nil)
	if execErr == nil {
		t.Fatal("both attempts fail, the call must fail")
	}
	snap := acc.Snapshot()
	if snap.InputTokens != 210 || snap.OutputTokens != 22 {
		t.Fatalf("accumulator = %+v, want both attempts' usage (210/22)", snap)
	}
	// The exhaustion error carries the same total, plus the count and the
	// last attempt's own usage.
	if u := llm.UsageFromError(execErr); u == nil || u.InputTokens != 210 || u.OutputTokens != 22 {
		t.Fatalf("UsageFromError = %+v, want the merged total (210/22)", u)
	}
	if n := llm.AttemptsFromError(execErr); n != 2 {
		t.Fatalf("AttemptsFromError = %d, want 2", n)
	}
	if last := llm.LastAttemptUsageFromError(execErr); last == nil || last.InputTokens != 110 || last.OutputTokens != 12 {
		t.Fatalf("LastAttemptUsageFromError = %+v, want the last attempt's own usage (110/12)", last)
	}
}

// Overflow calibration learns from the rejected attempt's input — what the
// provider actually refused — not from the merged total a retried request's
// error carries, which sums dead attempts and would inflate the bound.
func TestObserveOverflowLearnsTheRejectedAttemptNotTheMergedTotal(t *testing.T) {
	retryMW, err := llm.NewRetry(2, llm.WithMaxBackoff(0))
	if err != nil {
		t.Fatalf("NewRetry: %v", err)
	}
	inner := &failedUsageLLM{usages: []llm.Usage{
		{InputTokens: 120, OutputTokens: 14},
		{InputTokens: 210, OutputTokens: 22},
	}}
	_, execErr := llm.Use(inner, retryMW).Execute(context.Background(), nil, nil)
	if execErr == nil {
		t.Fatal("both attempts fail, the call must fail")
	}
	if u := llm.UsageFromError(execErr); u == nil || u.InputTokens != 330 {
		t.Fatalf("UsageFromError = %+v, want the merged total (330 in) as the precondition", u)
	}
	r := recoverableLLM{compactDeps: &CompactChainDeps{
		ActiveModel: func(context.Context) (string, string) { return "m175prov", "m175model" },
	}}
	r.observeOverflow(context.Background(), execErr, 400_000)
	obs, ok := llm.Get("m175prov", "m175model")
	if !ok {
		t.Fatal("the overflow was not recorded")
	}
	if obs.RejectedInput != 210 {
		t.Fatalf("RejectedInput = %d, want the rejected attempt's own input (210), not the merged total (330)", obs.RejectedInput)
	}
}

// The successful path's persistRunUsage writes the summary's model-call time
// beside the tokens: the denominator of the output rate, from the same run
// the tokens describe. The failed path writes no time (it has no Result),
// which leaves that run on the "no denominator" side of the rate query.
func TestPersistRunUsageWritesTheModelCallTime(t *testing.T) {
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("OpenStateForTest error: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	rt := &state.RunStore{DB: db}
	if err := state.NewSessionStore(db, "main").Ensure(ctx, "sid", "sid"); err != nil {
		t.Fatal(err)
	}
	rr, err := rt.CreateRun(ctx, "sid", "turn")
	if err != nil {
		t.Fatalf("CreateRun error: %v", err)
	}

	persistRunUsage(rt, rr.ID, &agent.Result{Summary: &agent.RunSummary{
		LLMCalls: 3,
		Usage:    agent.UsageSummary{InputTokens: 90, OutputTokens: 120},
		Latency:  agent.LatencySummary{LLM: 12_345 * time.Millisecond},
	}})
	if err := rt.SetStatus(ctx, rr.ID, state.RunStatusDone); err != nil {
		t.Fatal(err)
	}

	last, err := rt.LastRunUsageForSession(ctx, "sid")
	if err != nil {
		t.Fatalf("LastRunUsageForSession error: %v", err)
	}
	if last.LLMMs != 12_345 {
		t.Fatalf("usage_llm_ms = %d, want the summary's Latency.LLM (12345 ms)", last.LLMMs)
	}
	if last.CompletionTokens != 120 || last.LLMCalls != 3 {
		t.Fatalf("tokens beside the time = %+v, want 120 tokens over 3 calls", last)
	}

	// The rate pairs the two: this run alone yields 120 tokens / 12.345 s.
	tokens, ms, err := rt.SessionOutputRate(ctx, "sid")
	if err != nil {
		t.Fatalf("SessionOutputRate error: %v", err)
	}
	if tokens != 120 || ms != 12_345 {
		t.Fatalf("SessionOutputRate = %d / %d, want 120 / 12345", tokens, ms)
	}
}
