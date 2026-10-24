package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// ErrStreamStalled reports a streaming request that went silent: no response
// headers, or no body bytes, for the watchdog's window.
var ErrStreamStalled = errors.New("stream stalled")

// StreamStallError says which wait ran out: Headers is true when the response
// never arrived, false when the body stopped producing bytes.
type StreamStallError struct {
	Headers bool          // true: no response headers; false: no body bytes
	After   time.Duration // the silence window that ran out
}

func (e *StreamStallError) Error() string {
	if e.Headers {
		return fmt.Sprintf("stream stalled: no response within %s", e.After)
	}
	return fmt.Sprintf("stream stalled: no data for %s", e.After)
}

func (e *StreamStallError) Is(target error) bool { return target == ErrStreamStalled }

type streamWatchdogKey struct{}

// WithStreamWatchdog sets the silence window for streaming requests made under
// ctx; 0 disables the watchdog for those requests.
func WithStreamWatchdog(ctx context.Context, window time.Duration) context.Context {
	return context.WithValue(ctx, streamWatchdogKey{}, window)
}

// streamWatchdogFrom returns the configured silence window, 0 when none is.
func streamWatchdogFrom(ctx context.Context) time.Duration {
	if ctx == nil {
		return 0
	}
	window, _ := ctx.Value(streamWatchdogKey{}).(time.Duration)
	return window
}

type streamingRequestKey struct{}

// WithStreamingRequest marks the request made under ctx as a streaming one;
// adapters call it right before their streaming SDK call, so the watchdog
// (installed inside every provider transport by WrapHTTPTransport) can tell
// streaming requests from ordinary JSON ones.
func WithStreamingRequest(ctx context.Context) context.Context {
	return context.WithValue(ctx, streamingRequestKey{}, true)
}

// isStreamingRequest reports whether ctx carries the streaming mark.
func isStreamingRequest(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	streaming, _ := ctx.Value(streamingRequestKey{}).(bool)
	return streaming
}

// watchdogTransport aborts a streaming request that goes silent for longer
// than the window in the request's context: one window for the response
// headers, then one window per body read. The request is aborted by canceling
// a context derived from the request's own — never the request's context
// itself — and the caller sees a *StreamStallError, which the network-retry
// layer treats as a dropped connection.
type watchdogTransport struct{ next http.RoundTripper }

func (t watchdogTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	window := streamWatchdogFrom(req.Context())
	if window <= 0 || !isStreamingRequest(req.Context()) {
		return t.next.RoundTrip(req)
	}
	ctx, cancel := context.WithCancelCause(req.Context())
	headerTimer := time.AfterFunc(window, func() {
		cancel(&StreamStallError{Headers: true, After: window})
	})
	resp, err := t.next.RoundTrip(req.WithContext(ctx))
	headerTimer.Stop()
	if err != nil {
		// A watchdog abort is reported as the stall itself; every other
		// failure — a user cancellation included — passes through as it is.
		if stall, ok := context.Cause(ctx).(*StreamStallError); ok {
			cancel(nil)
			return nil, stall
		}
		cancel(nil)
		return nil, err
	}
	resp.Body = &watchdogBody{rc: resp.Body, window: window, cancel: cancel, ctx: ctx}
	return resp, nil
}

// watchdogBody watches the body half of a streaming response: every Read
// re-arms the idle timer, and a read that errors after the timer fired is
// reported as the stall instead of the transport's cancellation noise.
type watchdogBody struct {
	rc     io.ReadCloser
	window time.Duration
	cancel context.CancelCauseFunc
	ctx    context.Context

	// mu guards timer, which the Read and Close entry points and the timer's
	// own replacement touch from different goroutines.
	mu    sync.Mutex
	timer *time.Timer
}

func (b *watchdogBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	if b.timer == nil {
		b.timer = time.AfterFunc(b.window, func() {
			b.cancel(&StreamStallError{After: b.window})
		})
	} else {
		b.timer.Reset(b.window)
	}
	b.mu.Unlock()
	n, err := b.rc.Read(p)
	if err != nil {
		if stall, ok := context.Cause(b.ctx).(*StreamStallError); ok {
			return n, stall
		}
	}
	return n, err
}

func (b *watchdogBody) Close() error {
	b.mu.Lock()
	if b.timer != nil {
		b.timer.Stop()
	}
	b.mu.Unlock()
	b.cancel(nil)
	return b.rc.Close()
}
