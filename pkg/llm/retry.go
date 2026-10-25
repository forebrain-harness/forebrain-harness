// Retry and rate-limiting middleware.
package llm

import (
	"context"
	"encoding/json"
	"math/rand"
	"strings"
	"sync/atomic"
	"time"
)

const (
	defaultInitialBackoff = 500 * time.Millisecond
	defaultMaxBackoff     = 30 * time.Second
)

// RetryOption configures a Retry middleware.
type RetryOption func(*retryConfig)

// retryConfig holds the configuration for the Retry middleware.
type retryConfig struct {
	maxAttempts    int
	initialBackoff time.Duration
	maxBackoff     time.Duration
	// upwardJitter replaces the default ±25% jitter with an upward-only one:
	// each wait is the back-off step plus 0–25% of it, so a wait never
	// undershoots the step it was computed from.
	upwardJitter bool
	shouldRetry  func(error) bool
	// retryAfter reads the wait the server asked for off an error, and
	// maxRetryAfter is the longest such wait this loop will honour: each
	// wait becomes at least the server's, and a request for longer than the
	// limit ends the retries and returns the error instead of parking the
	// turn.
	retryAfter    func(error) time.Duration
	maxRetryAfter time.Duration
	// notify, when set, is called before every retry sleep and once more
	// (Cleared=true) when that retry is over: the retried request started
	// producing output, or the retry loop returned. It is how the
	// network-retry middleware surfaces discard/retry cards; retry chains
	// that keep no visible state (blank-output retries) leave it unset.
	notify func(ctx context.Context, tr TransportRetry)
	// resendGuard refuses a retry the request's stream progress says would
	// duplicate what the user already has (a finished output block, a
	// dispatched tool), and resets that progress before each retry it does
	// make. Layers whose resend re-asks the same question set it.
	resendGuard bool
}

// WithShouldRetry replaces the default retry predicate. By default every
// non-nil error triggers a retry.
func WithShouldRetry(fn func(error) bool) RetryOption {
	return func(c *retryConfig) { c.shouldRetry = fn }
}

// WithMaxBackoff caps each back-off step before jitter is added.
func WithMaxBackoff(d time.Duration) RetryOption {
	return func(c *retryConfig) { c.maxBackoff = d }
}

// WithUpwardJitter replaces the default ±25% jitter with an upward-only one:
// each wait is the back-off step plus 0–25% of it.
func WithUpwardJitter() RetryOption {
	return func(c *retryConfig) { c.upwardJitter = true }
}

// WithRetryAfter makes each wait at least the server-requested one; a request
// for longer than limit ends the retries and returns the error.
func WithRetryAfter(of func(error) time.Duration, limit time.Duration) RetryOption {
	return func(c *retryConfig) { c.retryAfter = of; c.maxRetryAfter = limit }
}

// WithRetryNotify registers a callback fired before each retry sleep, with the
// 1-based retry number, the retry cap, the wait ahead and the error that
// caused it; and once more with Cleared=true when that retry is over — the
// retried request's first output, or the loop's return. Errors the predicate
// rejected never produce a notification.
func WithRetryNotify(fn func(ctx context.Context, tr TransportRetry)) RetryOption {
	return func(c *retryConfig) { c.notify = fn }
}

// WithResendGuard makes the retry loop ask the request's stream progress
// before re-sending: once the request finished an output block or dispatched
// a tool, the error is returned instead of retried, and every retry it does
// make starts from fresh progress.
func WithResendGuard() RetryOption {
	return func(c *retryConfig) { c.resendGuard = true }
}

// waitFor computes the wait before the next attempt from the back-off step
// and the error that caused the retry: jitter on the step, and the server's
// own Retry-After when it asked for more — never an unbounded one, which
// ends the retries instead of parking the turn.
func (c *retryConfig) waitFor(backoff time.Duration, err error) (sleep time.Duration, giveUp bool) {
	sleep = backoff
	if c.upwardJitter {
		// 0–25% on top of the step: a wait never undershoots it.
		if quarter := int64(backoff) / 4; quarter > 0 {
			sleep += time.Duration(rand.Int63n(quarter + 1)) //nolint:gosec
		}
	} else if half := int64(backoff) / 2; half > 0 {
		jitter := time.Duration(rand.Int63n(half)) - backoff/4 //nolint:gosec
		sleep += jitter
		if sleep < 0 {
			sleep = 0
		}
	}
	// The server's own wait wins whenever it asked for more than the
	// back-off computed — but never an unbounded one: past the limit the
	// provider is not asking for patience, it is saying not now, and the
	// caller gets the error instead of a parked turn.
	if c.retryAfter != nil {
		if ra := c.retryAfter(err); ra > 0 {
			if c.maxRetryAfter > 0 && ra > c.maxRetryAfter {
				return 0, true
			}
			if ra > sleep {
				sleep = ra
			}
		}
	}
	return sleep, false
}

// NewRetry returns an LLMMiddleware that automatically retries failed
// Execute calls up to maxAttempts times using exponential back-off with jitter.
//
// Context cancellation is honoured between retries: if ctx is cancelled while
// sleeping before a retry, the middleware returns immediately.
//
//	mw, err := middleware.NewRetry(3, middleware.WithInitialBackoff(200*time.Millisecond))
//	if err != nil { ... }
//	client := Use(base, mw)
func NewRetry(maxAttempts int, opts ...RetryOption) (LLMMiddleware, error) {
	if maxAttempts < 1 {
		return nil, ErrInvalidMaxAttempts
	}

	cfg := &retryConfig{
		maxAttempts:    maxAttempts,
		initialBackoff: defaultInitialBackoff,
		maxBackoff:     defaultMaxBackoff,
		shouldRetry:    func(error) bool { return true },
	}
	for _, o := range opts {
		o(cfg)
	}

	return func(next LLM) LLM {
		return &retryLLM{inner: next, cfg: cfg}
	}, nil
}

// retryLLM is the concrete LLM produced by the Retry middleware.
type retryLLM struct {
	inner LLM
	cfg   *retryConfig
}

// Execute calls inner.Execute up to maxAttempts times with exponential
// back-off and jitter between attempts. A retry re-sends the original request
// unchanged — the dead attempt's half-streamed output is never continued,
// its holders are told to discard it through the notification. With notify
// set, one Cleared notification fires per retry: when the retried request
// first produces a non-empty delta, or when the loop returns (success,
// exhaustion, cancellation), whichever comes first.
func (r *retryLLM) Execute(ctx context.Context, messages []Message, tools []*Tool) (*Result, error) {
	var lastErr error
	backoff := r.cfg.initialBackoff
	maxRetries := r.cfg.maxAttempts - 1

	// Notifications ride the caller's own context: the callback must never
	// observe the wrapped sink installed below.
	notifyCtx := ctx
	var pendingRetry atomic.Int64 // retry number awaiting its Cleared notice, 0 = none
	clearPending := func() {
		if n := pendingRetry.Swap(0); n > 0 {
			r.cfg.notify(notifyCtx, TransportRetry{Retry: int(n), Total: maxRetries, Cleared: true})
		}
	}
	if r.cfg.notify != nil {
		// Every return path ends the retry in flight, if one is: the defer
		// covers success, a rejected error, exhaustion and cancellation.
		defer clearPending()
		// The retry is over the moment the retried request produces anything:
		// the first non-empty delta clears the pending notice early, so a
		// surface waiting on it recovers at the first token, not at the
		// response's end. Only callbacks that were non-nil are wrapped — a
		// muted context holds nil OnDelta/OnReasoningDelta and keeps them.
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

	for attempt := 0; attempt < r.cfg.maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		result, err := r.inner.Execute(ctx, messages, tools)
		if err == nil {
			return result, nil
		}
		lastErr = err

		if r.cfg.resendGuard && !ResendSafe(ctx) {
			// Re-sending would duplicate what the user already has; the
			// caller owns finishing from what completed.
			return nil, err
		}
		if !r.cfg.shouldRetry(err) {
			return nil, err
		}

		if attempt == r.cfg.maxAttempts-1 {
			break
		}

		sleep, giveUp := r.cfg.waitFor(backoff, err)
		if giveUp {
			return nil, err
		}

		if r.cfg.notify != nil {
			r.cfg.notify(notifyCtx, TransportRetry{
				Retry:  attempt + 1,
				Total:  maxRetries,
				Wait:   sleep,
				Err:    err,
				Status: HTTPStatusOf(err),
			})
			pendingRetry.Store(int64(attempt + 1))
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(sleep):
		}

		if r.cfg.resendGuard {
			// The next attempt is a fresh one: what the dead attempt
			// produced must not decide anything about it.
			ResetStreamProgress(ctx)
		}

		backoff *= 2
		if backoff > r.cfg.maxBackoff {
			backoff = r.cfg.maxBackoff
		}
	}

	// The exhaustion return is the final dead attempt's withdraw point. Its
	// own first token already consumed the pending notice, and every holder
	// of the text it streamed learns of the death only here — so when the
	// pending is spent, the catch-up Cleared below is the only announcement
	// that final attempt ever gets. clearPending covers the other shape, a
	// final attempt that died before producing a single byte; both paths
	// together guarantee exactly one Cleared per exhaustion.
	if r.cfg.notify != nil && maxRetries > 0 {
		spent := pendingRetry.Load() == 0
		clearPending()
		if spent {
			r.cfg.notify(notifyCtx, TransportRetry{Retry: maxRetries, Total: maxRetries, Cleared: true})
		}
	}
	return nil, &MaxAttemptsExceededError{Attempts: r.cfg.maxAttempts, Err: lastErr}
}

const Prefix = "!!FOREBRAIN_RCv1!!"

const payloadVersion = "openai_responses_reasoning_v1"

type Item struct {
	ID               string   `json:"id,omitempty"`
	Summary          []string `json:"summary,omitempty"`
	EncryptedContent string   `json:"encrypted_content,omitempty"`
}

type Payload struct {
	Version string `json:"version"`
	Items   []Item `json:"items"`
}

func Encode(items []Item) string {
	items = normalizeItems(items)
	if len(items) == 0 {
		return ""
	}
	data, err := json.Marshal(Payload{
		Version: payloadVersion,
		Items:   items,
	})
	if err != nil {
		return Prefix + SummaryText(items)
	}
	return Prefix + string(data)
}

func Decode(name string) (Payload, bool) {
	if !strings.HasPrefix(name, Prefix) {
		return Payload{}, false
	}
	raw := strings.TrimPrefix(name, Prefix)
	if raw == "" {
		return Payload{}, false
	}
	var payload Payload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return Payload{}, false
	}
	if payload.Version != payloadVersion {
		return Payload{}, false
	}
	payload.Items = normalizeItems(payload.Items)
	if len(payload.Items) == 0 {
		return Payload{}, false
	}
	return payload, true
}

func ExtractSummary(name string) string {
	if !strings.HasPrefix(name, Prefix) {
		return ""
	}
	if payload, ok := Decode(name); ok {
		return SummaryText(payload.Items)
	}
	return strings.TrimPrefix(name, Prefix)
}

func SummaryText(items []Item) string {
	items = normalizeItems(items)
	if len(items) == 0 {
		return ""
	}
	blocks := make([]string, 0, len(items))
	for _, item := range items {
		partLines := make([]string, 0, len(item.Summary))
		for _, part := range item.Summary {
			if part == "" {
				continue
			}
			partLines = append(partLines, part)
		}
		if len(partLines) == 0 {
			continue
		}
		blocks = append(blocks, strings.Join(partLines, "\n"))
	}
	return strings.Join(blocks, "\n")
}

func normalizeItems(items []Item) []Item {
	if len(items) == 0 {
		return nil
	}
	out := make([]Item, 0, len(items))
	for _, item := range items {
		summary := make([]string, 0, len(item.Summary))
		for _, part := range item.Summary {
			if part == "" {
				continue
			}
			summary = append(summary, part)
		}
		if len(summary) == 0 && strings.TrimSpace(item.EncryptedContent) == "" {
			continue
		}
		out = append(out, Item{
			ID:               strings.TrimSpace(item.ID),
			Summary:          summary,
			EncryptedContent: strings.TrimSpace(item.EncryptedContent),
		})
	}
	return out
}
