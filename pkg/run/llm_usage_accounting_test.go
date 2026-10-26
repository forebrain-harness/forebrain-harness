package run

import (
	"context"
	"errors"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
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
