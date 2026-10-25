package run

import (
	"context"
	"strings"
	"sync"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

// responseIncompleteError ends a turn whose response was cut: what the user
// reads is the sentence about it, while the failure that caused it stays
// reachable for the surfaces that show errors.
type responseIncompleteError struct {
	sentence string
	err      error
}

func (e *responseIncompleteError) Error() string { return e.sentence }
func (e *responseIncompleteError) Unwrap() error { return e.err }

// responseCollector keeps what one model request has completed so far: the
// finished answer-text blocks and the tool calls whose arguments are complete.
// It is what a request that failed mid-stream is finished from.
type responseCollector struct {
	mu        sync.Mutex
	current   strings.Builder // the text block being streamed
	completed []string        // finished text blocks, in order
	ready     []llm.ToolCall  // finished tool calls, in order
}

type responseCollectorCtxKey struct{}

// withResponseCollector installs a collector for the request made under the
// returned context, observing the context's stream sink. OnDelta is chained
// only when the sink already had one — a muted stream must not start
// producing output — while the block boundaries (OnTextBlockDone,
// OnToolCallReady) and the transport-retry notices get record-only versions
// when they were nil, so the collected state is complete even when nobody
// listens.
func withResponseCollector(ctx context.Context) (context.Context, *responseCollector) {
	if ctx == nil {
		ctx = context.Background()
	}
	collector := &responseCollector{}
	sink := llm.StreamSinkFrom(ctx)
	if sink == nil {
		return context.WithValue(ctx, responseCollectorCtxKey{}, collector), collector
	}
	wrapped := *sink
	if downstream := sink.OnDelta; downstream != nil {
		wrapped.OnDelta = func(text string) {
			collector.noteDelta(text)
			downstream(text)
		}
	}
	wrapped.OnTextBlockDone = func() {
		collector.sealCurrent()
		if sink.OnTextBlockDone != nil {
			sink.OnTextBlockDone()
		}
	}
	wrapped.OnToolCallReady = func(call llm.ToolCall) {
		collector.noteToolCall(call)
		if sink.OnToolCallReady != nil {
			sink.OnToolCallReady(call)
		}
	}
	wrapped.OnTransportRetry = func(tr llm.TransportRetry) {
		collector.noteTransportRetry(tr)
		if sink.OnTransportRetry != nil {
			sink.OnTransportRetry(tr)
		}
	}
	return context.WithValue(llm.WithStreamSink(ctx, &wrapped), responseCollectorCtxKey{}, collector), collector
}

// noteDelta accumulates the text block still being streamed.
func (c *responseCollector) noteDelta(text string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.current.WriteString(text)
}

// sealCurrent moves the streamed text block into the finished ones, if it
// holds anything but whitespace.
func (c *responseCollector) sealCurrent() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sealLocked()
}

func (c *responseCollector) sealLocked() {
	if strings.TrimSpace(c.current.String()) == "" {
		c.current.Reset()
		return
	}
	c.completed = append(c.completed, c.current.String())
	c.current.Reset()
}

// noteToolCall seals the text block the call ended and records the call: its
// arguments were complete inside the stream.
func (c *responseCollector) noteToolCall(call llm.ToolCall) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sealLocked()
	c.ready = append(c.ready, call)
}

// noteTransportRetry takes back what the dead attempt produced: a resend
// (non-Cleared, or one that withdraws finished thinking) clears everything;
// the catch-up Cleared only drops the block still being streamed, which is
// all an announcement that does not precede a resend can mean here.
func (c *responseCollector) noteTransportRetry(tr llm.TransportRetry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !tr.Cleared || tr.DropFinishedThinking {
		c.current.Reset()
		c.completed = nil
		c.ready = nil
		return
	}
	c.current.Reset()
}

// result is the response built from what completed: one text part per
// finished block, plus the finished tool calls in order.
func (c *responseCollector) result() *llm.Result {
	c.mu.Lock()
	defer c.mu.Unlock()
	parts := make([]llm.ContentPart, 0, len(c.completed))
	for _, block := range c.completed {
		parts = append(parts, llm.Text(block))
	}
	calls := append([]llm.ToolCall(nil), c.ready...)
	return &llm.Result{Message: &llm.Message{Role: llm.RoleAssistant, Parts: parts, ToolCalls: calls}}
}

// completedParts returns the finished answer-text blocks, in order: the body a
// provisional assistant message written while the stream still runs may carry.
func (c *responseCollector) completedParts() []llm.ContentPart {
	c.mu.Lock()
	defer c.mu.Unlock()
	parts := make([]llm.ContentPart, 0, len(c.completed))
	for _, block := range c.completed {
		parts = append(parts, llm.Text(block))
	}
	return parts
}
