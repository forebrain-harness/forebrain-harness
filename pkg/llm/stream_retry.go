// The streamed-request retry loop: each failure is classified and decided
// from the request's stream progress the way Claude Code decides it.
package llm

import (
	"context"
	"errors"
	"sync/atomic"
	"time"
)

// NewStreamRetry is the network retry for streamed model requests. Each
// failure is classified and decided from the request's StreamProgress the way
// Claude Code decides it: re-send as a stream, re-send without streaming,
// finish from what completed, or fail.
func NewStreamRetry(maxRetries int, opts ...RetryOption) LLMMiddleware {
	if maxRetries < 0 {
		maxRetries = 0
	}
	cfg := &retryConfig{
		maxAttempts:    1 + maxRetries,
		initialBackoff: defaultInitialBackoff,
		maxBackoff:     defaultMaxBackoff,
		shouldRetry:    func(error) bool { return true },
	}
	for _, o := range opts {
		o(cfg)
	}
	return func(next LLM) LLM {
		return &streamRetryLLM{inner: next, cfg: cfg}
	}
}

type streamRetryLLM struct {
	inner LLM
	cfg   *retryConfig
}

// streamingFallbackWarned makes the non-streaming fallback a once-per-process
// warning: the first resend that follows a truncation or a stream that never
// opened tells the user, the rest stay quiet.
var streamingFallbackWarned atomic.Bool

// retryAnnounceNumber is the number a retry announcement carries: the retry's
// own 1-based ordinal, every announced retry counted. It is not the shared
// budget's spend, because the decision table also retries without spending it —
// a machine that slept through the request gets up to maxSleptThrough retries of
// its own — and an announcement numbered 0 is read downstream as no retry at
// all: the pending notice would never be armed, so its Cleared would never fire
// and the status line would outlive the recovery until the turn ended. Clamped
// to the cap the line divides by, which the slept-through retries sit past.
func retryAnnounceNumber(c streamRetryCounts, maxRetries int) int {
	return min(c.retries+c.sleptThrough, maxRetries)
}

func (r *streamRetryLLM) Execute(ctx context.Context, messages []Message, tools []*Tool) (*Result, error) {
	maxRetries := r.cfg.maxAttempts - 1
	if maxRetries < 0 {
		maxRetries = 0
	}
	policy := streamRetryPolicy{
		maxRetries:        maxRetries,
		maxSleptThrough:   min(maxRetries, 10),
		maxOverloaded:     3,
		allowNonStreaming: true,
	}
	counts := streamRetryCounts{}
	nonStreaming := false
	backoff := r.cfg.initialBackoff

	// Notifications ride the caller's own context: the callback must never
	// observe the wrapped sink installed below (kept as retryLLM keeps it).
	notifyCtx := ctx
	var pendingRetry atomic.Int64 // retry number awaiting its Cleared notice, 0 = none
	clearPending := func() {
		if n := pendingRetry.Swap(0); n > 0 {
			r.cfg.notify(notifyCtx, TransportRetry{Retry: int(n), Total: maxRetries, Cleared: true})
		}
	}
	if r.cfg.notify != nil {
		// Every return path ends the retry in flight, if one is.
		defer clearPending()
		// The retry is over the moment the retried request produces anything.
		if sink := StreamSinkFrom(ctx); sink != nil {
			wrapped := *sink
			if down := wrapped.OnDelta; down != nil {
				wrapped.OnDelta = func(text string) {
					if text != "" {
						clearPending()
					}
					down(text)
				}
			}
			if down := wrapped.OnReasoningDelta; down != nil {
				wrapped.OnReasoningDelta = func(text string) {
					if text != "" {
						clearPending()
					}
					down(text)
				}
			}
			ctx = WithStreamSink(ctx, &wrapped)
		}
	}

	// finish hands the caller the completed part of a response that failed
	// mid-flight: what the user keeps is what had finished. The catch-up
	// Cleared withdraws the in-flight blocks of the dead attempt when its
	// own first token never consumed a pending notice. The error carries
	// the whole attempt accounting: the dead attempts' tokens are spent
	// whether the response is kept or not.
	acct := attemptAccounting{}
	finish := func(p *PartialResponseError) (*Result, error) {
		spent := pendingRetry.Load() == 0
		clearPending()
		if r.cfg.notify != nil && spent {
			r.cfg.notify(notifyCtx, TransportRetry{Total: maxRetries, Cleared: true})
		}
		return nil, acct.fail(p)
	}
	// withdraw takes back everything the dead attempt showed before a
	// resend: the block still being streamed, and — when all it finished was
	// thinking — those finished thinking blocks too.
	withdraw := func(snap StreamProgressSnapshot) {
		clearPending()
		if r.cfg.notify != nil {
			r.cfg.notify(notifyCtx, TransportRetry{Total: maxRetries, Cleared: true, DropFinishedThinking: snap.Kind == ProgressThinkingOnly})
		}
	}
	warnOnce := func() {
		// A sink that cannot show the warning does not spend the one
		// warning this process has.
		sink := StreamSinkFrom(ctx)
		if sink == nil || sink.OnResponseNotice == nil {
			return
		}
		if streamingFallbackWarned.CompareAndSwap(false, true) {
			sink.OnResponseNotice(ResponseNotice{Kind: ResponseNoticeStreamingFallback, Text: StreamingFallbackWarning})
		}
	}
	snapshot := func() StreamProgressSnapshot {
		if p := StreamProgressFrom(ctx); p != nil {
			return p.Snapshot()
		}
		return StreamProgressSnapshot{}
	}
	wait := func(d time.Duration) error {
		if d <= 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d):
			return nil
		}
	}

	for {
		if err := ctx.Err(); err != nil {
			return nil, acct.fail(err)
		}
		// Captured before the fallback consumes its flag below: this is the
		// "this attempt goes out without streaming" decision of the attempt
		// about to run, so a later streaming attempt's failure does not
		// announce a stale true.
		attemptNonStreaming := nonStreaming
		attemptCtx := ctx
		if nonStreaming {
			attemptCtx = WithNonStreamingRequest(ctx)
			// One attempt, not a mode: consumed here so the attempt after this
			// one is streamed again — and back under the watchdog.
			nonStreaming = false
		}
		acct.attempts++
		result, err := r.inner.Execute(attemptCtx, messages, tools)
		if err == nil {
			// The settled attempt's own usage stays as Result.Usage; the
			// every-attempt billing lives on the accounting path. What the
			// caller learns here is how many physical requests it took.
			result.Attempts = acct.attempts
			return result, nil
		}
		acct.note(err)
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			return nil, acct.fail(err) // cancellation is returned exactly as it arrived
		}
		if IsPartialResponse(err) {
			return nil, acct.fail(err) // an inner layer already finished this response
		}
		snap := snapshot()

		var cause StreamFailureCause
		var dec streamDecision
		if d, pass, ok := requestLevelDecision(err, snap, counts, policy); ok {
			if pass {
				return nil, acct.fail(err) // the layers above own this error
			}
			dec = d
		} else {
			cause = ClassifyStreamFailure(err, snap)
			if completeDespiteError(cause, snap) {
				// The response had fully arrived; finish as a success.
				return finish(&PartialResponseError{Err: err, Progress: snap, Cause: cause, Complete: true})
			}
			dec = decideStreamFailure(cause, snap, snap.Elapsed >= NonStreamingTimeout, counts, policy)
		}
		counts = dec.c

		switch dec.d {
		case DecideFail:
			if snap.Kind == ProgressOutput || snap.ToolDispatched {
				// The completed part stays; the notice is the error itself.
				return finish(&PartialResponseError{Err: err, Progress: snap, Cause: cause, Failed: true})
			}
			if counts.retries > 0 {
				clearPending()
				return nil, &MaxAttemptsExceededError{Attempts: 1 + counts.retries, Err: acct.fail(err)}
			}
			return nil, acct.fail(err)
		case DecideKeepPartial:
			return finish(&PartialResponseError{Err: err, Progress: snap, Cause: cause})
		case DecideRetryWithoutStreaming:
			withdraw(snap)
			if cause == CauseTruncated || errors.Is(err, ErrStreamNoEvents) {
				warnOnce()
			}
			nonStreaming = true
			ResetStreamProgress(ctx)
			continue
		case DecideRetry:
			switch {
			case snap.Kind == ProgressThinkingOnly:
				// Only thinking finished: the resend withdraws it, and the
				// wait stays out of the status line.
				withdraw(snap)
				switch cause {
				case CauseStalled:
					// The watchdog already waited; go again at once.
				case CauseServerError, CauseTimedOut, CauseOverloaded:
					sleep, giveUp := r.cfg.waitFor(backoff, err)
					if giveUp {
						return nil, acct.fail(err)
					}
					if werr := wait(sleep); werr != nil {
						return nil, acct.fail(werr)
					}
					backoff *= 2
					if backoff > r.cfg.maxBackoff {
						backoff = r.cfg.maxBackoff
					}
				default:
					// Connection-class: a short fixed wait per thinking-only
					// retry, the way Claude Code waits between them.
					if werr := wait(100 * time.Millisecond * time.Duration(counts.afterThinkingOnly)); werr != nil {
						return nil, acct.fail(werr)
					}
				}
				ResetStreamProgress(ctx)
				continue
			case cause == CauseStalled:
				// The watchdog's abort re-sends at any progress, with no
				// wait and no status line.
				withdraw(snap)
				ResetStreamProgress(ctx)
				continue
			default:
				sleep, giveUp := r.cfg.waitFor(backoff, err)
				if giveUp {
					return nil, acct.fail(err)
				}
				if r.cfg.notify != nil {
					retryNo := retryAnnounceNumber(counts, maxRetries)
					r.cfg.notify(notifyCtx, TransportRetry{
						Retry:        retryNo,
						Total:        maxRetries,
						Wait:         sleep,
						Err:          err,
						Status:       statusOf(err),
						NonStreaming: attemptNonStreaming,
					})
					pendingRetry.Store(int64(retryNo))
				}
				if werr := wait(sleep); werr != nil {
					return nil, acct.fail(werr)
				}
				ResetStreamProgress(ctx)
				backoff *= 2
				if backoff > r.cfg.maxBackoff {
					backoff = r.cfg.maxBackoff
				}
				continue
			}
		}
	}
}
