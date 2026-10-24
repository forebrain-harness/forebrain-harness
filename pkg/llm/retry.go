// Retry, rate limiting, and reasoning-content carry-over middleware.
package llm

import (
	"context"
	"encoding/json"
	"math/rand"
	"strings"
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
	// unboundedBackoff keeps the exponential growth uncapped (each wait is
	// double the previous one, however large that gets). The default stays
	// capped at maxBackoff for the existing callers.
	unboundedBackoff bool
	shouldRetry      func(error) bool
	// notify, when set, is called before every retry sleep and once more
	// (Exhausted=true) after the last retryable failure. It is how the
	// network-retry middleware surfaces discard/retry cards; retry chains
	// that keep no visible state (blank-output retries) leave it unset.
	notify func(ctx context.Context, tr TransportRetry)
}

// WithShouldRetry replaces the default retry predicate. By default every
// non-nil error triggers a retry.
func WithShouldRetry(fn func(error) bool) RetryOption {
	return func(c *retryConfig) { c.shouldRetry = fn }
}

// WithUnboundedBackoff removes the back-off ceiling: waits keep doubling for
// as many retries as the cap allows. Callers whose retry counts are
// user-configured and unbounded themselves need this so a large cap is not
// silently pinned to 30-second waits.
func WithUnboundedBackoff() RetryOption {
	return func(c *retryConfig) { c.unboundedBackoff = true }
}

// WithRetryNotify registers a callback fired before each retry sleep, with the
// 1-based retry number, the retry cap, the wait ahead and the error that
// caused it; and once more with Exhausted=true after the final retryable
// failure, before MaxAttemptsExceededError is returned. Errors the predicate
// rejected never produce a notification.
func WithRetryNotify(fn func(ctx context.Context, tr TransportRetry)) RetryOption {
	return func(c *retryConfig) { c.notify = fn }
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

// Execute calls inner.Execute up to maxAttempts times, sleeping with
// exponential back-off and +/-25% jitter between each attempt. A retry is a
// continuation, not a resend: the answer text and thinking each dead attempt
// had already streamed accumulate across attempts and travel to the next one
// through the continuation prefill, so the model picks up from its own words.
func (r *retryLLM) Execute(ctx context.Context, messages []Message, tools []*Tool) (*Result, error) {
	var lastErr error
	backoff := r.cfg.initialBackoff
	maxRetries := r.cfg.maxAttempts - 1

	// Capture what the attempt in flight streams, by wrapping this call's own
	// view of the sink: every delta is chained downstream untouched and also
	// accumulated, so the continuation prefill after a transport death holds
	// the words the user already watched appear. The builders live across
	// attempts on purpose — attempt N continues what attempts 1..N-1 wrote.
	var continuation ContinuationPrefill
	if sink := StreamSinkFrom(ctx); sink != nil {
		wrapped := *sink
		downstreamDelta := wrapped.OnDelta
		wrapped.OnDelta = func(text string) {
			continuation.Text += text
			if downstreamDelta != nil {
				downstreamDelta(text)
			}
		}
		downstreamReasoning := wrapped.OnReasoningDelta
		wrapped.OnReasoningDelta = func(text string) {
			continuation.Reasoning += text
			if downstreamReasoning != nil {
				downstreamReasoning(text)
			}
		}
		ctx = WithStreamSink(ctx, &wrapped)
	}

	for attempt := 0; attempt < r.cfg.maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		attemptCtx := ctx
		if attempt > 0 && !continuation.Blank() {
			attemptCtx = WithContinuationPrefill(ctx, continuation)
		}
		result, err := r.inner.Execute(attemptCtx, messages, tools)
		if err == nil {
			return result, nil
		}
		lastErr = err

		if !r.cfg.shouldRetry(err) {
			return nil, err
		}

		if attempt == r.cfg.maxAttempts-1 {
			break
		}

		sleep := backoff
		if half := int64(backoff) / 2; half > 0 {
			jitter := time.Duration(rand.Int63n(half)) - backoff/4 //nolint:gosec
			sleep += jitter
			if sleep < 0 {
				sleep = 0
			}
		}

		if r.cfg.notify != nil {
			r.cfg.notify(ctx, TransportRetry{
				Retry: attempt + 1,
				Total: maxRetries,
				Wait:  sleep,
				Err:   err,
			})
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(sleep):
		}

		backoff *= 2
		if !r.cfg.unboundedBackoff && backoff > r.cfg.maxBackoff {
			backoff = r.cfg.maxBackoff
		}
	}

	// The last failure was retryable but no retries remain: surfaces the
	// frozen final retry card. A predicate rejection returned earlier, so a
	// non-retryable error never reaches a fake exhausted notification.
	if r.cfg.notify != nil && lastErr != nil && r.cfg.shouldRetry(lastErr) {
		r.cfg.notify(ctx, TransportRetry{
			Retry:     maxRetries,
			Total:     maxRetries,
			Wait:      0,
			Err:       lastErr,
			Exhausted: true,
		})
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
