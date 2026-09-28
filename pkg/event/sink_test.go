package event

import (
	"context"
	"errors"
	"testing"
)

func TestSinkFuncAdaptsAFunctionToSink(t *testing.T) {
	var got RunEvent
	want := errors.New("sink failed")
	var sink Sink = SinkFunc(func(_ context.Context, evt RunEvent) error {
		got = evt
		return want
	})

	err := sink.Publish(context.Background(), RunEvent{RunID: "run-1", Type: "turn_started"})
	if !errors.Is(err, want) {
		t.Fatalf("Publish error = %v, want %v", err, want)
	}
	if got.RunID != "run-1" || got.Type != "turn_started" {
		t.Fatalf("sink received %+v", got)
	}
}
