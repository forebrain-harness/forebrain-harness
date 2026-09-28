// Usage accounting: accumulation, timing, and provider cache-usage capture.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type UsageAccumulator struct {
	mu      sync.Mutex
	sum     Usage
	last    Usage
	hasLast bool
	parent  *UsageAccumulator
}

func NewUsageAccumulator() *UsageAccumulator {
	return &UsageAccumulator{}
}

func (a *UsageAccumulator) Observe(inputTokens int, outputTokens int) {
	a.ObserveUsage(Usage{InputTokens: inputTokens, OutputTokens: outputTokens})
}

func (a *UsageAccumulator) ObserveUsage(usage Usage) {
	if a == nil {
		return
	}
	if usage.InputTokens < 0 {
		usage.InputTokens = 0
	}
	if usage.OutputTokens < 0 {
		usage.OutputTokens = 0
	}
	if usage.CacheCreationInputTokens < 0 {
		usage.CacheCreationInputTokens = 0
	}
	if usage.CacheReadInputTokens < 0 {
		usage.CacheReadInputTokens = 0
	}
	if usage.InputTokens == 0 && usage.OutputTokens == 0 &&
		usage.CacheCreationInputTokens == 0 && usage.CacheReadInputTokens == 0 {
		return
	}
	a.mu.Lock()
	a.sum.InputTokens += usage.InputTokens
	a.sum.OutputTokens += usage.OutputTokens
	a.sum.CacheCreationInputTokens += usage.CacheCreationInputTokens
	a.sum.CacheReadInputTokens += usage.CacheReadInputTokens
	a.last = usage
	a.hasLast = true
	parent := a.parent
	a.mu.Unlock()
	if parent != nil {
		parent.ObserveUsage(usage)
	}
}

func (a *UsageAccumulator) Snapshot() Usage {
	if a == nil {
		return Usage{}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sum
}

func (a *UsageAccumulator) LastResponse() (Usage, bool) {
	if a == nil {
		return Usage{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.hasLast {
		return Usage{}, false
	}
	return a.last, true
}

type usageAccumulatorCtxKey struct{}

func WithUsageAccumulator(ctx context.Context, acc *UsageAccumulator) context.Context {
	if acc == nil {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if parent := UsageAccumulatorFrom(ctx); parent != nil && parent != acc {
		acc.linkParent(parent)
	}
	return context.WithValue(ctx, usageAccumulatorCtxKey{}, acc)
}

// WithoutUsageAccumulator starts a context whose model calls belong to a run
// of their own. The accumulator chain is how one run's nested scopes (the
// runner's, then the agent's) roll up into that run's total; a subagent is a
// separate run with its own persisted row, so its calls must not also roll up
// into the run that dispatched it — that total is persisted too, and every
// sum over a run tree would count the child twice. Surfaces that show a turn's
// work including its subagents add the subagents' own usage on top.
func WithoutUsageAccumulator(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, usageAccumulatorCtxKey{}, (*UsageAccumulator)(nil))
}

func UsageAccumulatorFrom(ctx context.Context) *UsageAccumulator {
	if ctx == nil {
		return nil
	}
	v, _ := ctx.Value(usageAccumulatorCtxKey{}).(*UsageAccumulator)
	return v
}

func (a *UsageAccumulator) linkParent(parent *UsageAccumulator) {
	if a == nil || parent == nil || parent == a {
		return
	}
	a.mu.Lock()
	if a.parent == nil {
		a.parent = parent
	}
	a.mu.Unlock()
}

// ExecutionTiming is the executor-owned timing for one completed execution.
// StartedAt and CompletedAt retain their monotonic clock readings while the
// value remains in memory; Duration is captured at the same boundary so it can
// be persisted without recomputing it from wall-clock timestamps.
type ExecutionTiming struct {
	StartedAt   time.Time     `json:"started_at"`
	CompletedAt time.Time     `json:"completed_at"`
	Duration    time.Duration `json:"duration"`
}

// NewExecutionTiming builds timing from the timestamps captured by an executor.
func NewExecutionTiming(startedAt, completedAt time.Time) ExecutionTiming {
	if startedAt.IsZero() || completedAt.IsZero() {
		return ExecutionTiming{}
	}
	duration := completedAt.Sub(startedAt)
	if duration < 0 {
		return ExecutionTiming{}
	}
	return ExecutionTiming{
		StartedAt:   startedAt,
		CompletedAt: completedAt,
		Duration:    duration,
	}
}

// Valid reports whether the timing contains a complete, non-negative interval.
func (t ExecutionTiming) Valid() bool {
	return !t.StartedAt.IsZero() && !t.CompletedAt.IsZero() && t.Duration >= 0
}

// AggregateExecutionTimings returns the wall-clock span covered by valid member
// executions. It is intended for parallel batch summaries and never starts an
// independent timer.
func AggregateExecutionTimings(timings ...ExecutionTiming) ExecutionTiming {
	var startedAt, completedAt time.Time
	for _, timing := range timings {
		if !timing.Valid() {
			continue
		}
		if startedAt.IsZero() || timing.StartedAt.Before(startedAt) {
			startedAt = timing.StartedAt
		}
		if completedAt.IsZero() || timing.CompletedAt.After(completedAt) {
			completedAt = timing.CompletedAt
		}
	}
	return NewExecutionTiming(startedAt, completedAt)
}

type cacheUsageKey struct{}

// CacheUsageSink collects provider usage from raw HTTP responses.
type CacheUsageSink struct {
	mu   sync.Mutex
	use  Usage
	seen bool
}

// NewCacheUsageSink creates an empty raw-usage collector.
func NewCacheUsageSink() *CacheUsageSink { return &CacheUsageSink{} }

// WithCacheUsageSink attaches s to outbound LLM requests made with ctx.
func WithCacheUsageSink(ctx context.Context, s *CacheUsageSink) context.Context {
	if s == nil {
		return ctx
	}
	return context.WithValue(ctx, cacheUsageKey{}, s)
}

// Usage returns the cumulative usage observed by s.
func (s *CacheUsageSink) Usage() (Usage, bool) {
	if s == nil {
		return Usage{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.use, s.seen
}

func (s *CacheUsageSink) add(use Usage) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.use.InputTokens += use.InputTokens
	s.use.OutputTokens += use.OutputTokens
	s.use.CacheCreationInputTokens += use.CacheCreationInputTokens
	s.use.CacheReadInputTokens += use.CacheReadInputTokens
	s.seen = true
	s.mu.Unlock()
}

// CacheUsageTransport observes response usage without changing requests.
// It restores non-streaming bodies and transparently inspects SSE data lines.
type CacheUsageTransport struct {
	Next http.RoundTripper
}

// RoundTrip implements http.RoundTripper.
func (t *CacheUsageTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	next := t.Next
	if next == nil {
		next = http.DefaultTransport
	}
	resp, err := next.RoundTrip(req)
	if err != nil || resp == nil || resp.Body == nil || req == nil {
		return resp, err
	}
	s, _ := req.Context().Value(cacheUsageKey{}).(*CacheUsageSink)
	if s == nil {
		return resp, nil
	}
	if isEventStream(resp.Header.Get("Content-Type")) {
		resp.Body = &usageBody{ReadCloser: resp.Body, sink: s}
		return resp, nil
	}
	body, err := io.ReadAll(resp.Body)
	closeErr := resp.Body.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	observeUsage(s, body)
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	return resp, nil
}

func isEventStream(contentType string) bool {
	return strings.Contains(strings.ToLower(contentType), "text/event-stream")
}

type usageBody struct {
	io.ReadCloser
	sink *CacheUsageSink
	line []byte
}

func (r *usageBody) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if n > 0 {
		r.feed(p[:n])
	}
	return n, err
}

// Close flushes a final SSE line that was not terminated by a newline, then
// closes the provider response. Providers are allowed to end a stream this
// way; dropping that line would hide its only usage event.
func (r *usageBody) Close() error {
	if len(r.line) > 0 {
		line := bytes.TrimSuffix(r.line, []byte{'\r'})
		r.line = nil
		if data, ok := bytes.CutPrefix(line, []byte("data:")); ok {
			observeUsage(r.sink, bytes.TrimSpace(data))
		}
	}
	return r.ReadCloser.Close()
}

func (r *usageBody) feed(data []byte) {
	r.line = append(r.line, data...)
	for {
		i := bytes.IndexByte(r.line, '\n')
		if i < 0 {
			return
		}
		line := bytes.TrimSuffix(r.line[:i], []byte{'\r'})
		r.line = r.line[i+1:]
		if data, ok := bytes.CutPrefix(line, []byte("data:")); ok {
			observeUsage(r.sink, bytes.TrimSpace(data))
		}
	}
}

// ParseCacheUsage parses either a provider response or its usage object.
// Cached input is kept separate from uncached input for stable accounting.
func ParseCacheUsage(raw []byte) (Usage, bool) {
	var root map[string]json.RawMessage
	if json.Unmarshal(raw, &root) != nil {
		return Usage{}, false
	}
	use := root
	if nested, ok := root["usage"]; ok {
		if json.Unmarshal(nested, &use) != nil {
			return Usage{}, false
		}
	}
	prompt, hasPrompt := number(use, "prompt_tokens")
	if !hasPrompt {
		prompt, hasPrompt = number(use, "input_tokens")
	}
	output, hasOutput := number(use, "completion_tokens")
	if !hasOutput {
		output, hasOutput = number(use, "output_tokens")
	}
	read, hasRead := firstNumber(use,
		"prompt_cache_hit_tokens",
		"cache_read_input_tokens",
		"cached_tokens",
	)
	if !hasRead {
		var details map[string]json.RawMessage
		if json.Unmarshal(use["prompt_tokens_details"], &details) == nil {
			read, hasRead = number(details, "cached_tokens")
		}
	}
	created, hasCreated := number(use, "cache_creation_input_tokens")
	if !hasPrompt && !hasRead && !hasCreated && !hasOutput {
		return Usage{}, false
	}
	uncached := prompt - read - created
	if uncached < 0 {
		uncached = 0
	}
	return Usage{
		InputTokens:              uncached,
		OutputTokens:             output,
		CacheReadInputTokens:     read,
		CacheCreationInputTokens: created,
	}, true
}

func observeUsage(s *CacheUsageSink, raw []byte) {
	if bytes.Equal(raw, []byte("[DONE]")) {
		return
	}
	if use, ok := ParseCacheUsage(raw); ok {
		s.add(use)
	}
}

func firstNumber(m map[string]json.RawMessage, keys ...string) (int, bool) {
	for _, key := range keys {
		if n, ok := number(m, key); ok {
			return n, true
		}
	}
	return 0, false
}

func number(m map[string]json.RawMessage, key string) (int, bool) {
	raw, ok := m[key]
	if !ok {
		return 0, false
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		i, err := strconv.Atoi(n.String())
		return i, err == nil
	}
	var f float64
	if json.Unmarshal(raw, &f) == nil {
		return int(f), f >= 0
	}
	return 0, false
}

// WithRawCacheUsage installs a sink that captures provider cache usage from
// the raw HTTP response, and returns a function that attaches whatever it
// captured to a Result.
//
// It exists because several provider SDKs drop the cache fields while
// decoding: the Anthropic SDK's Message.Accumulate silently discards the
// cache counters that arrive on message_delta, so a real cache hit is recorded
// as zero. Reading them off the wire is the only reliable source, and every
// provider client has to do it the same way or the hit rate this project
// treats as its top constraint becomes unmeasurable.
func WithRawCacheUsage(ctx context.Context) (context.Context, func(*Result) *Result) {
	sink := NewCacheUsageSink()
	ctx = WithCacheUsageSink(ctx, sink)
	return ctx, func(result *Result) *Result {
		if result == nil {
			return nil
		}
		if use, ok := sink.Usage(); ok {
			result.Usage = &use
		}
		return result
	}
}
