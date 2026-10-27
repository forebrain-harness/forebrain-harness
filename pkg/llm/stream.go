// Streaming sinks and the tool-argument sanitizer applied to streamed calls.
package llm

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
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
	// footer budget instead of being added together. It is a live gauge,
	// fired once per event that carries usage — a consumer that also files
	// it durably must use UsageSnapshotRecord, or it writes one row per
	// chunk.
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
	// OnToolCallReady fires once per tool call, as soon as that call's
	// arguments are complete inside the stream — before the response ends.
	// The call is identical (ID, name, arguments) to the one the final
	// Result carries.
	OnToolCallReady func(ToolCall)
	// OnTextBlockDone fires when an answer-text block ended inside the
	// response. Providers without an in-stream text boundary fire it when the
	// first tool call begins.
	OnTextBlockDone func()
	// OnStopReason fires when the provider sent the response's finish reason.
	OnStopReason func(reason string)
	// OnMessageStop fires when the provider's terminal frame for the response
	// arrived: nothing of this response is still missing. Providers whose
	// finish reason is itself the terminal frame fire it right after
	// OnStopReason.
	OnMessageStop func()
	// OnTransportRetry fires when a transport failure killed the current
	// attempt and a retry is about to run (Cleared=false), and once more
	// when that retry is over (Cleared=true): the retried request started
	// producing output, or the retry loop returned. A non-Cleared
	// notification implies discard: every holder of the current response's
	// streamed in-flight text must drop it — the next attempt re-streams
	// from the first token — while blocks that already ended stay. The
	// middlewares copy the sink by value, so each layer that keeps state
	// across attempts chains this callback explicitly (see orchestration's
	// reasoning recorder).
	OnTransportRetry func(TransportRetry)
	// OnResponseNotice delivers a line about the response itself — an answer
	// finished from what completed, a stream retried without streaming — that
	// belongs in the conversation rather than on a status line.
	OnResponseNotice func(ResponseNotice)
	OnEnd            func()
	Streamed         *bool
}

// TransportRetry describes one transport failure and the retry around it.
type TransportRetry struct {
	// Retry is the retry's 1-based sequence number (the numerator of n/total).
	Retry int
	// Total is the retry cap from configuration (default 10).
	Total int
	// Wait is the back-off before the next attempt (0 when Cleared).
	Wait time.Duration
	// Err is the failure that just happened, rendered on the error card.
	Err error
	// Status is the HTTP status of the failure, 0 for transport errors. It
	// only picks the status line's generic reason; the retry decision was
	// already made by the predicate.
	Status int
	// Cleared marks the end of a retry: the retried request started producing
	// output, or the retry loop returned (success, exhaustion or cancel). Only
	// Retry and Total are set on it.
	Cleared bool
	// DropFinishedThinking: also withdraw the thinking blocks the dead attempt
	// finished. A resend only ever follows an attempt whose finished blocks
	// are all thinking, and Claude Code withdraws them with the unfinished one.
	DropFinishedThinking bool
	// NonStreaming marks that the attempt this failure came from was sent
	// without streaming (the one-shot fallback plan 156 allows, or a retry
	// chain running under a non-streaming request). Informational only: the
	// retry decisions never read it; the attempt ledger does, to fill its
	// streaming column.
	NonStreaming bool
}

// ResponseNoticeKind names the line a surface draws about the response
// itself, not about a tool or a file.
type ResponseNoticeKind string

const (
	ResponseNoticeIncomplete        ResponseNoticeKind = "incomplete"
	ResponseNoticeStreamingFallback ResponseNoticeKind = "streaming_fallback"
)

const StreamingFallbackWarning = "Streaming response ended before any complete data was received. Retrying without streaming. If this keeps happening, check any proxy or gateway between forebrain and your model provider."

// ResponseNotice is a line about the response itself that belongs in the
// conversation: it is delivered once, as a fact the user reads, and the
// surfaces that persist the conversation keep it there.
type ResponseNotice struct {
	Kind ResponseNoticeKind
	Text string
}

// UsageSnapshotRecord keeps the last absolute usage snapshot of one request,
// so a consumer that also files the gauge durably writes one record per
// request instead of one per callback. OnUsageSnapshot is a live gauge: a
// provider may fire it once per chunk (a compatible stream carrying its
// cumulative usage on every chunk does), and filing each one wrote 209 rows
// for one response. Observe from the snapshot callback, Take from the
// request's end boundary (OnResponseCompleted, and OnEnd so a cancelled
// request is still recorded). Take drains: calling it at both boundaries
// yields one record.
type UsageSnapshotRecord struct {
	mu    sync.Mutex
	in    int
	out   int
	state bool
}

// Observe stores the latest snapshot, replacing any earlier one.
func (r *UsageSnapshotRecord) Observe(inputTokens, outputTokens int) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.in, r.out, r.state = inputTokens, outputTokens, true
	r.mu.Unlock()
}

// Take returns the latest observed snapshot and drains the record; a second
// Take reports ok=false until a new snapshot is observed.
func (r *UsageSnapshotRecord) Take() (int, int, bool) {
	if r == nil {
		return 0, 0, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	in, out, ok := r.in, r.out, r.state
	r.state = false
	return in, out, ok
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
	// A nested call contributes no response notices to the foreground
	// conversation.
	muted.OnResponseNotice = nil
	// Nested internal calls take no part in the foreground request's
	// progress accounting or its future mid-stream tool execution.
	muted.OnToolCallReady = nil
	muted.OnTextBlockDone = nil
	muted.OnStopReason = nil
	muted.OnMessageStop = nil
	muted.Streamed = nil
	ctx = WithStreamSink(ctx, &muted)
	// Nested internal requests share this ctx chain with the foreground
	// request; without an explicit nil record their retry layer would read
	// the foreground request's progress.
	return context.WithValue(ctx, streamProgressCtxKey{}, (*StreamProgress)(nil))
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
