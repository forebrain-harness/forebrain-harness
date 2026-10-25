// The defer signal for tool calls started while their response streams.
package tool

import (
	"context"
	"errors"
)

// ErrDeferToStreamEnd is returned instead of asking anyone when a tool call
// started while its response was still streaming reaches an approval or a
// question. Nothing was created; the call runs again once the stream ends.
var ErrDeferToStreamEnd = errors.New("tool call deferred to the end of the stream")

type streamingDispatchCtxKey struct{}

// WithStreamingDispatch marks a context as executing a tool call that began
// before its response finished streaming: a call under it never asks anyone
// mid-stream, it defers to the end of the stream (see ErrDeferToStreamEnd).
func WithStreamingDispatch(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, streamingDispatchCtxKey{}, true)
}

// StreamingDispatchFromContext reports whether ctx carries the streaming
// dispatch mark installed by WithStreamingDispatch.
func StreamingDispatchFromContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(streamingDispatchCtxKey{}).(bool)
	return v
}
