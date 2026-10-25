// Per-request stream progress recording for the resend/finish decisions.
package llm

import (
	"context"
	"sync"
	"time"
)

type StreamProgressKind int

const (
	ProgressNothing StreamProgressKind = iota
	ProgressStarted
	ProgressPartialOutput
	ProgressThinkingOnly
	ProgressOutput
)

// StreamProgressSnapshot is what one request had produced when it was read.
type StreamProgressSnapshot struct {
	Kind               StreamProgressKind
	StopReasonReceived bool
	// Complete: the provider's terminal frame arrived (OnMessageStop).
	Complete       bool
	ToolDispatched bool
	// Elapsed is the monotonic time since this attempt started.
	Elapsed time.Duration
	// Slept is how much longer the wall clock ran than the monotonic clock
	// over this attempt: the time the machine spent asleep.
	Slept time.Duration
}

// ResendSafe reports whether re-sending the whole request cannot duplicate
// anything the user already has: no tool started and no finished block shown.
func (s StreamProgressSnapshot) ResendSafe() bool {
	return !s.ToolDispatched && s.Kind != ProgressOutput
}

// StreamProgress records one model request's progress. Safe for concurrent use.
type StreamProgress struct {
	mu               sync.Mutex
	anyEvent         bool
	sawReasoning     bool
	anyBlockFinished bool
	anyOutputShown   bool
	stopReason       bool
	complete         bool
	toolDispatched   bool
	startedAt        time.Time
}

func (p *StreamProgress) Snapshot() StreamProgressSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	elapsed := now.Sub(p.startedAt)
	slept := now.Round(0).Sub(p.startedAt.Round(0)) - elapsed
	if slept < 0 {
		slept = 0
	}
	snap := StreamProgressSnapshot{
		StopReasonReceived: p.stopReason,
		Complete:           p.complete,
		ToolDispatched:     p.toolDispatched,
		Elapsed:            elapsed,
		Slept:              slept,
	}
	switch {
	case p.anyBlockFinished:
		if p.anyOutputShown {
			snap.Kind = ProgressOutput
		} else {
			snap.Kind = ProgressThinkingOnly
		}
	case p.anyOutputShown:
		snap.Kind = ProgressPartialOutput
	case p.anyEvent:
		snap.Kind = ProgressStarted
	}
	return snap
}

// Reset starts a new attempt: every flag is cleared and the clock restarts.
func (p *StreamProgress) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.anyEvent = false
	p.sawReasoning = false
	p.anyBlockFinished = false
	p.anyOutputShown = false
	p.stopReason = false
	p.complete = false
	p.toolDispatched = false
	p.startedAt = time.Now()
}

// MarkToolDispatched records that a tool call from this request started
// executing.
func (p *StreamProgress) MarkToolDispatched() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.toolDispatched = true
}

// Recording entry points, called by the sink wrappers WithStreamProgress
// installs. Each one takes the lock once.

func (p *StreamProgress) noteEvent() {
	p.mu.Lock()
	p.anyEvent = true
	p.mu.Unlock()
}

func (p *StreamProgress) noteReasoningDelta() {
	p.mu.Lock()
	p.anyEvent = true
	p.sawReasoning = true
	p.mu.Unlock()
}

func (p *StreamProgress) noteOutputDelta() {
	p.mu.Lock()
	p.anyEvent = true
	// Thinking before the first answer text is finished the moment that text
	// starts: the compat stream has no separate thinking-done frame, so every
	// provider is recorded with the same rule.
	if p.sawReasoning {
		p.anyBlockFinished = true
	}
	p.anyOutputShown = true
	p.mu.Unlock()
}

func (p *StreamProgress) noteReasoningDone() {
	p.mu.Lock()
	p.anyBlockFinished = true
	p.mu.Unlock()
}

func (p *StreamProgress) noteTextBlockDone() {
	p.mu.Lock()
	p.anyBlockFinished = true
	p.anyOutputShown = true
	p.mu.Unlock()
}

func (p *StreamProgress) noteToolCallReady() {
	p.mu.Lock()
	p.anyEvent = true
	p.anyBlockFinished = true
	p.anyOutputShown = true
	p.mu.Unlock()
}

func (p *StreamProgress) noteStopReason() {
	p.mu.Lock()
	p.stopReason = true
	p.mu.Unlock()
}

func (p *StreamProgress) noteMessageStop() {
	p.mu.Lock()
	p.complete = true
	p.mu.Unlock()
}

type streamProgressCtxKey struct{}

// WithStreamProgress installs a fresh progress record for the request made
// under the returned context, observing the context's stream sink. Callbacks
// that were already set are chained (record first, then the original); the
// boundary callbacks that were nil get record-only versions so the progress
// is complete even when nobody listens. OnDelta and OnReasoningDelta stay nil
// when they were nil: muted streams must not start producing output.
// Without a sink it returns the context unchanged plus a record no callback
// will ever update (its snapshot reads ProgressNothing).
func WithStreamProgress(ctx context.Context) (context.Context, *StreamProgress) {
	if ctx == nil {
		ctx = context.Background()
	}
	progress := &StreamProgress{startedAt: time.Now()}
	sink := StreamSinkFrom(ctx)
	if sink == nil {
		return ctx, progress
	}
	wrapped := *sink

	wrapped.OnResponseStarted = func() {
		progress.noteEvent()
		if sink.OnResponseStarted != nil {
			sink.OnResponseStarted()
		}
	}
	if downstream := sink.OnReasoningDelta; downstream != nil {
		wrapped.OnReasoningDelta = func(s string) {
			progress.noteReasoningDelta()
			downstream(s)
		}
	}
	if downstream := sink.OnDelta; downstream != nil {
		wrapped.OnDelta = func(s string) {
			progress.noteOutputDelta()
			downstream(s)
		}
	}
	if downstream := sink.OnReasoningDone; downstream != nil {
		wrapped.OnReasoningDone = func() {
			progress.noteReasoningDone()
			downstream()
		}
	}
	wrapped.OnTextBlockDone = func() {
		progress.noteTextBlockDone()
		if sink.OnTextBlockDone != nil {
			sink.OnTextBlockDone()
		}
	}
	wrapped.OnToolCallReady = func(call ToolCall) {
		progress.noteToolCallReady()
		if sink.OnToolCallReady != nil {
			sink.OnToolCallReady(call)
		}
	}
	wrapped.OnStopReason = func(reason string) {
		progress.noteStopReason()
		if sink.OnStopReason != nil {
			sink.OnStopReason(reason)
		}
	}
	wrapped.OnMessageStop = func() {
		progress.noteMessageStop()
		if sink.OnMessageStop != nil {
			sink.OnMessageStop()
		}
	}
	ctx = WithStreamSink(ctx, &wrapped)
	return context.WithValue(ctx, streamProgressCtxKey{}, progress), progress
}

// StreamProgressFrom returns the progress record installed for requests made
// under ctx, or nil — including on contexts derived through
// WithMutedForegroundStream, which deliberately drops the record.
func StreamProgressFrom(ctx context.Context) *StreamProgress {
	if ctx == nil {
		return nil
	}
	v, _ := ctx.Value(streamProgressCtxKey{}).(*StreamProgress)
	return v
}

// ResendSafe reports whether the request recorded for ctx may be re-sent as
// a whole: a context with no record produced nothing, so it may; a muted
// nested request reads as no record for the same reason.
func ResendSafe(ctx context.Context) bool {
	p := StreamProgressFrom(ctx)
	if p == nil {
		return true
	}
	return p.Snapshot().ResendSafe()
}

// ResetStreamProgress starts a fresh attempt on the request recorded for ctx,
// when there is one. A resend begins from no progress: what the dead attempt
// produced must not decide the next failure.
func ResetStreamProgress(ctx context.Context) {
	if p := StreamProgressFrom(ctx); p != nil {
		p.Reset()
	}
}
