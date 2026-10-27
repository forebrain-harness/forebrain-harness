// The attempt ledger: one row per physical model request a run made. It is
// the observation layer of the network retry — plan 165's vertical slice —
// written where the run layer already knows the three facts pkg/llm does not:
// the run id (tool.RunIDFromContext), the attempt's elapsed time
// (llm.StreamProgressFrom) and, on the terminal row, the response's usage.
package run

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// llmAttemptLedger appends a run's attempt rows to the store. One ledger
// serves every chain a Runner builds (the conversation's and the compaction
// summary's), so call numbers order the two together; it is nil-safe — a
// Runner without a RunStore simply records nothing.
type llmAttemptLedger struct {
	store *state.RunStore
	// nextCall numbers the logical requests: one pass through the network
	// retry wrapper each, however many physical attempts it spans.
	nextCall atomic.Int64
}

// newLLMAttemptLedger returns nil when there is no store to write to, which
// leaves every record call a no-op.
func newLLMAttemptLedger(store *state.RunStore) *llmAttemptLedger {
	if store == nil {
		return nil
	}
	return &llmAttemptLedger{store: store}
}

// llmAttemptCall is one logical request's ledger state, per Execute call. It
// rides the context so the construction-time retry-notify hook — which cannot
// hold per-call state itself — reads the same call number the terminal row
// will carry. The retry loop fires it synchronously, so no locking is needed.
type llmAttemptCall struct {
	runID string
	call  int
	// nextAttempt numbers the physical requests of this call, monotonically,
	// whatever layer made them: a transport failure, a blank-output resend or
	// an image-heal discard each take the next number, so (call, attempt)
	// names one physical request with no collisions across the nested retry
	// chains. Every writer runs on the request's own goroutine — the retry
	// loops fire their notifications synchronously — so no locking is needed.
	nextAttempt int
}

// takeAttempt hands out the next physical-request number of this call.
func (c *llmAttemptCall) takeAttempt() int {
	c.nextAttempt++
	return c.nextAttempt
}

type llmAttemptCallKey struct{}

// arm opens the ledger for one logical request. A context with no run id
// (a model call no run owns) opens nothing and records nothing.
func (l *llmAttemptLedger) arm(ctx context.Context) context.Context {
	if l == nil || l.store == nil {
		return ctx
	}
	runID := strings.TrimSpace(tool.RunIDFromContext(ctx))
	if runID == "" {
		return ctx
	}
	return context.WithValue(ctx, llmAttemptCallKey{}, &llmAttemptCall{
		runID: runID,
		call:  int(l.nextCall.Add(1)),
	})
}

func llmAttemptCallFrom(ctx context.Context) *llmAttemptCall {
	if ctx == nil {
		return nil
	}
	call, _ := ctx.Value(llmAttemptCallKey{}).(*llmAttemptCall)
	return call
}

// noteFailure records the attempt that just died and announced itself — the
// retries a user sees as n/total. Withdrawal-only retries (a thinking-only
// or stalled attempt, taken back without a status line) announce no failure,
// so they leave no row here; the prototype accepts the gap rather than
// changing what pkg/llm announces.
func (l *llmAttemptLedger) noteFailure(ctx context.Context, tr llm.TransportRetry) {
	call := llmAttemptCallFrom(ctx)
	if l == nil || l.store == nil || call == nil || tr.Cleared || tr.Retry <= 0 {
		return
	}
	streamed := !tr.NonStreaming
	rec := state.LLMAttempt{
		RunID:     call.runID,
		Call:      call.call,
		Attempt:   call.takeAttempt(),
		Status:    tr.Status,
		WaitMs:    tr.Wait.Milliseconds(),
		Outcome:   state.LLMAttemptFailed,
		Streaming: &streamed,
	}
	if tr.Err != nil {
		rec.Error = tr.Err.Error()
	}
	if p := llm.StreamProgressFrom(ctx); p != nil {
		rec.ElapsedMs = p.Snapshot().Elapsed.Milliseconds()
	}
	l.record(ctx, rec)
}

// noteDiscarded records a request a recovery layer threw away before asking
// again — today an image the provider refused, healed by degrading it. The
// row explains a resend the transport layer never announced: status 0 (no
// HTTP answer to record), the refusal text as the error.
func (l *llmAttemptLedger) noteDiscarded(ctx context.Context, reason string) {
	call := llmAttemptCallFrom(ctx)
	if l == nil || l.store == nil || call == nil || strings.TrimSpace(reason) == "" {
		return
	}
	streamed := !llm.NonStreamingRequestFrom(ctx)
	rec := state.LLMAttempt{
		RunID:     call.runID,
		Call:      call.call,
		Attempt:   call.takeAttempt(),
		Error:     reason,
		Outcome:   state.LLMAttemptFailed,
		Streaming: &streamed,
	}
	if p := llm.StreamProgressFrom(ctx); p != nil {
		rec.ElapsedMs = p.Snapshot().Elapsed.Milliseconds()
	}
	l.record(ctx, rec)
}

// settle records the attempt that ended the request: the one that succeeded,
// finished from what it completed, was cancelled, or failed for good. A
// transport failure reports no usage, and a success whose provider sent none
// records nil tokens — never an estimate. The write runs on an uncancellable
// context so a cancelled request still leaves its last row.
func (l *llmAttemptLedger) settle(ctx context.Context, res *llm.Result, err error) {
	call := llmAttemptCallFrom(ctx)
	if l == nil || l.store == nil || call == nil {
		return
	}
	// The terminal row's streaming reads the request-level switch: the layers
	// below may run one attempt under a derived non-streaming context (the
	// stream-retry fallback), which this boundary cannot see — the failure
	// rows around such an attempt carry the attempt-level bit instead.
	streamed := !llm.NonStreamingRequestFrom(ctx)
	rec := state.LLMAttempt{
		RunID:     call.runID,
		Call:      call.call,
		Attempt:   call.takeAttempt(),
		Streaming: &streamed,
	}
	if p := llm.StreamProgressFrom(ctx); p != nil {
		rec.ElapsedMs = p.Snapshot().Elapsed.Milliseconds()
	}
	if err != nil {
		rec.Outcome = state.LLMAttemptFailed
		rec.Error = err.Error()
		var api *llm.APIError
		if errors.As(err, &api) {
			rec.Status = api.StatusCode
		}
		switch {
		case errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled):
			rec.Outcome = state.LLMAttemptCancelled
		case llm.IsPartialResponse(err):
			rec.Outcome = state.LLMAttemptKeptPartial
		}
	} else {
		rec.Outcome = state.LLMAttemptSuccess
		if res != nil && res.Usage != nil {
			input := int64(res.Usage.InputTokens)
			output := int64(res.Usage.OutputTokens)
			if input > 0 {
				rec.InputTokens = &input
			}
			if output > 0 {
				rec.OutputTokens = &output
			}
		}
	}
	l.record(context.WithoutCancel(ctx), rec)
}

// record is the one write path: the ledger is an observation, so a failed
// write is a warning, never a failed request.
func (l *llmAttemptLedger) record(ctx context.Context, rec state.LLMAttempt) {
	if err := l.store.InsertLLMAttempt(ctx, rec); err != nil {
		slog.Warn("llm attempt ledger write failed", "run_id", rec.RunID, "call", rec.Call, "attempt", rec.Attempt, "error", err)
	}
}
