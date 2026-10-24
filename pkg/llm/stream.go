// Streaming sinks and the tool-argument sanitizer applied to streamed calls.
package llm

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

type StreamSink struct {
	OnDelta          func(string)
	OnReasoningDelta func(string)
	OnReasoningDone  func()
	// OnResponseStarted fires before the first output-bearing event from the
	// current provider response is forwarded to the other callbacks. It is the
	// acknowledgement boundary for input provisionally attached to that request:
	// once model output has begun, the model has received the input even if the
	// stream later fails or is cancelled. Lifecycle-only events do not fire it.
	OnResponseStarted func()
	// OnWebSearch reports server-executed Responses web search activity. The
	// callback is deliberately separate from function-tool callbacks: provider
	// web search has no client-side call/result pair.
	OnWebSearch func(id, detail string, completed bool)
	// OnUsage receives additive deltas used by per-run/session token counters.
	OnUsage func(inputTokens int, outputTokens int)
	// OnUsageSnapshot receives the current LLM response's absolute context
	// occupancy. Unlike OnUsage, repeated snapshots replace the composer
	// footer budget instead of being added together.
	OnUsageSnapshot func(inputTokens int, outputTokens int)
	// OnResponseCompleted fires when a completed assistant response has been
	// appended to the orchestration session, meaning its text is now covered by
	// the PartialSessionCapture. Consumers that accumulate streamed assistant
	// deltas across a whole turn use this as the boundary to drop the text they
	// have buffered so far: after this point the buffer holds only deltas from
	// the next (possibly interrupted) LLM call, so a cancelled turn can persist
	// the buffer without duplicating text the capture already carries.
	//
	// It is deliberately NOT a reasoning boundary: reasoning from completed
	// responses is only persisted as a standalone transcript row on the cancel
	// path, so dropping it per iteration would lose it from resume replay.
	OnResponseCompleted func()
	// OnTransportRetry fires when a transport failure killed the current
	// attempt and the request is about to be re-sent. The notification
	// implies discard: every holder of the current response's streamed text
	// must drop it (the next attempt re-streams from the first token), and
	// every surface shows the failure/retry cards. The middlewares copy the
	// sink by value, so each layer that keeps state across attempts chains
	// this callback explicitly (see orchestration's reasoning recorder).
	OnTransportRetry func(TransportRetry)
	OnEnd            func()
	Streamed         *bool
}

// TransportRetry describes one transport failure and the retry about to run.
type TransportRetry struct {
	// Retry is the retry's 1-based sequence number (the numerator of n/total).
	Retry int
	// Total is the retry cap from configuration (default 10).
	Total int
	// Wait is the back-off before the next attempt (0 when Exhausted).
	Wait time.Duration
	// Err is the failure that just happened, rendered on the error card. When
	// Exhausted it is the last real error.
	Err error
	// Exhausted reports that every retry has been used up. It is the only
	// producer of the retry card's frozen final state.
	Exhausted bool
}

// NotifyResponseStarted fires the response acknowledgement exactly once for
// an Execute call. Providers invoke it after receiving their first model-output
// stream event and before forwarding output from that event.
func NotifyResponseStarted(sink *StreamSink, started *bool) {
	if started == nil || *started {
		return
	}
	*started = true
	if sink != nil && sink.OnResponseStarted != nil {
		sink.OnResponseStarted()
	}
}

type llmStreamSinkCtxKey struct{}

func WithStreamSink(ctx context.Context, sink *StreamSink) context.Context {
	if sink == nil {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, llmStreamSinkCtxKey{}, sink)
}

func StreamSinkFrom(ctx context.Context) *StreamSink {
	if ctx == nil {
		return nil
	}
	v, _ := ctx.Value(llmStreamSinkCtxKey{}).(*StreamSink)
	return v
}

// ContinuationPrefill is what a network-interrupted attempt had already
// streamed when it died: the thinking so far and the answer text so far. A
// retry carries it so the model CONTINUES from its own words instead of
// starting over — the continuation message is appended at the innermost layer,
// where every prompt-shaping wrapper has already had its say.
type ContinuationPrefill struct {
	Reasoning string
	Text      string
}

// Blank reports whether there is nothing worth continuing from.
func (p ContinuationPrefill) Blank() bool {
	return strings.TrimSpace(p.Reasoning) == "" && strings.TrimSpace(p.Text) == ""
}

type continuationPrefillCtxKey struct{}

// WithContinuationPrefill attaches the partial output the next attempt must
// continue from.
func WithContinuationPrefill(ctx context.Context, prefill ContinuationPrefill) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, continuationPrefillCtxKey{}, prefill)
}

// ContinuationPrefillFrom returns the continuation carried by this request, or
// the zero value when the request is a fresh one.
func ContinuationPrefillFrom(ctx context.Context) ContinuationPrefill {
	if ctx == nil {
		return ContinuationPrefill{}
	}
	v, _ := ctx.Value(continuationPrefillCtxKey{}).(ContinuationPrefill)
	return v
}

// WithMutedForegroundStream keeps non-visual sink callbacks like usage/end,
// but prevents nested/internal LLM calls from surfacing assistant/reasoning
// deltas or marking the parent run as streamed in the foreground UI.
func WithMutedForegroundStream(ctx context.Context) context.Context {
	sink := StreamSinkFrom(ctx)
	if sink == nil {
		return ctx
	}
	muted := *sink
	muted.OnDelta = nil
	muted.OnReasoningDelta = nil
	muted.OnReasoningDone = nil
	muted.OnWebSearch = nil
	// Response-start acknowledges the foreground request's provisional input.
	// A nested compaction/evaluator response did not receive that input and must
	// not commit it on the foreground request's behalf.
	muted.OnResponseStarted = nil
	// A nested call contributes no deltas to the foreground buffer (OnDelta is
	// muted above), so letting it signal a response boundary would only discard
	// the parent turn's buffered text and lose it from a later cancel.
	muted.OnResponseCompleted = nil
	// A nested call's retries surface no cards and clear no foreground
	// buffers — it has none; it just retries quietly.
	muted.OnTransportRetry = nil
	muted.Streamed = nil
	return WithStreamSink(ctx, &muted)
}

// SanitizeToolCallArguments validates that the given arguments string is a
// single, well-formed JSON value. If it is not (e.g. the model emitted
// malformed JSON such as a premature closing brace), the function returns
// "{}" so that downstream consumers — LLM providers that echo previous
// assistant tool_calls back in the request — never receive invalid JSON and
// reject the entire request with a BadRequest.
//
// The original (possibly malformed) string is only used for local tool
// execution where the handler already reports a descriptive parse error; it
// must never reach a provider API.
func SanitizeToolCallArguments(args string) string {
	s := strings.TrimSpace(args)
	if s == "" {
		return "{}"
	}
	// json.Valid checks that the entire string is a single JSON value with
	// no trailing garbage (e.g. "{}}, extra": ...}").
	if json.Valid([]byte(s)) {
		return s
	}
	return "{}"
}
