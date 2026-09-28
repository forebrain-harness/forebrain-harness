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
	shouldRetry    func(error) bool
}

// WithShouldRetry replaces the default retry predicate. By default every
// non-nil error triggers a retry.
func WithShouldRetry(fn func(error) bool) RetryOption {
	return func(c *retryConfig) { c.shouldRetry = fn }
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
// exponential back-off and +/-25% jitter between each attempt.
func (r *retryLLM) Execute(ctx context.Context, messages []Message, tools []*Tool) (*Result, error) {
	var lastErr error
	backoff := r.cfg.initialBackoff

	for attempt := 0; attempt < r.cfg.maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		result, err := r.inner.Execute(ctx, messages, tools)
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

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(sleep):
		}

		backoff *= 2
		if backoff > r.cfg.maxBackoff {
			backoff = r.cfg.maxBackoff
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
