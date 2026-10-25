package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// NonStreamingTimeout bounds one non-streaming request. A non-streaming
// request has no idle watchdog, so without a bound a stuck one never returns.
var NonStreamingTimeout = 5 * time.Minute

// ErrNonStreamingTimeout reports a non-streaming request that ran out of
// NonStreamingTimeout. It is retryable like any dropped connection.
var ErrNonStreamingTimeout = errors.New("non-streaming request timed out")

type nonStreamingRequestKey struct{}

// WithNonStreamingRequest asks the adapter serving a request made under ctx to
// send it as one non-streaming call instead of a stream.
func WithNonStreamingRequest(ctx context.Context) context.Context {
	return context.WithValue(ctx, nonStreamingRequestKey{}, true)
}

// NonStreamingRequestFrom reports whether ctx carries the non-streaming
// request switch.
func NonStreamingRequestFrom(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	on, _ := ctx.Value(nonStreamingRequestKey{}).(bool)
	return on
}

// WithNonStreamingDeadline applies NonStreamingTimeout to one non-streaming
// attempt; NonStreamingError turns that deadline into ErrNonStreamingTimeout.
func WithNonStreamingDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeoutCause(ctx, NonStreamingTimeout, ErrNonStreamingTimeout)
}

// NonStreamingError reports err as it is, unless the non-streaming deadline
// placed on ctx by WithNonStreamingDeadline is what caused it: that failure
// becomes ErrNonStreamingTimeout, retryable like any dropped connection.
func NonStreamingError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx != nil && errors.Is(context.Cause(ctx), ErrNonStreamingTimeout) {
		return fmt.Errorf("%w: %v", ErrNonStreamingTimeout, err)
	}
	return err
}

// ReplayedResponse is a whole response, in the order a stream would have
// delivered it.
type ReplayedResponse struct {
	Reasoning  string     // the model's visible thinking text, "" when none
	Text       []string   // answer-text blocks, in order
	ToolCalls  []ToolCall // identical to the Result's calls
	StopReason string     // the provider's finish reason, "" when unknown
}

// ReplayToSink delivers a non-streaming response through the stream sink so
// every consumer sees it exactly as it would a streamed one: response start,
// thinking, answer text (one block each), tool calls, finish reason and the
// terminal frame, in that order. Usage is not replayed here — the adapter
// reports it from the response it holds.
func ReplayToSink(sink *StreamSink, r ReplayedResponse) {
	if sink == nil {
		return
	}
	hasContent := strings.TrimSpace(r.Reasoning) != "" || len(r.ToolCalls) > 0
	for _, text := range r.Text {
		if strings.TrimSpace(text) != "" {
			hasContent = true
			break
		}
	}
	started := false
	if hasContent {
		NotifyResponseStarted(sink, &started)
		if sink.Streamed != nil {
			*sink.Streamed = true
		}
	}
	if strings.TrimSpace(r.Reasoning) != "" {
		if sink.OnReasoningDelta != nil {
			sink.OnReasoningDelta(r.Reasoning)
		}
		if sink.OnReasoningDone != nil {
			sink.OnReasoningDone()
		}
	}
	for _, text := range r.Text {
		if strings.TrimSpace(text) == "" {
			continue
		}
		if sink.OnDelta != nil {
			sink.OnDelta(text)
		}
		if sink.OnTextBlockDone != nil {
			sink.OnTextBlockDone()
		}
	}
	for _, call := range r.ToolCalls {
		if sink.OnToolCallReady != nil {
			sink.OnToolCallReady(call)
		}
	}
	if sink.OnStopReason != nil {
		reason := r.StopReason
		if strings.TrimSpace(reason) == "" {
			reason = "stop"
		}
		sink.OnStopReason(reason)
	}
	if sink.OnMessageStop != nil {
		sink.OnMessageStop()
	}
}
