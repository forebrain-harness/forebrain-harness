package event

import (
	"context"
)

// Sink receives canonical run events.
type Sink interface {
	Publish(context.Context, RunEvent) error
}

// SinkFunc adapts a function to Sink.
type SinkFunc func(context.Context, RunEvent) error

func (f SinkFunc) Publish(ctx context.Context, evt RunEvent) error {
	return f(ctx, evt)
}
