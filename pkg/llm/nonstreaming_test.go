package llm

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// replayTrace records the sink callbacks ReplayToSink fires, in order.
type replayTrace struct {
	events []string
}

func (r *replayTrace) sink(streamed *bool) *StreamSink {
	return &StreamSink{
		OnResponseStarted: func() { r.events = append(r.events, "started") },
		OnReasoningDelta:  func(s string) { r.events = append(r.events, "reasoning:"+s) },
		OnReasoningDone:   func() { r.events = append(r.events, "reasoning-done") },
		OnDelta:           func(s string) { r.events = append(r.events, "delta:"+s) },
		OnTextBlockDone:   func() { r.events = append(r.events, "text-block-done") },
		OnToolCallReady: func(c ToolCall) {
			r.events = append(r.events, "ready:"+c.Function.Name)
		},
		OnStopReason:  func(reason string) { r.events = append(r.events, "stop:"+reason) },
		OnMessageStop: func() { r.events = append(r.events, "message-stop") },
		OnEnd:         func() { r.events = append(r.events, "end") },
		Streamed:      streamed,
	}
}

func TestReplayToSinkDeliversStreamOrder(t *testing.T) {
	t.Parallel()

	rec := &replayTrace{}
	streamed := new(bool)
	calls := []ToolCall{
		{ID: "call_1", Type: ToolTypeFunction, Function: FunctionCall{Name: "weather", Arguments: `{"city":"Oslo"}`}},
		{ID: "call_2", Type: ToolTypeFunction, Function: FunctionCall{Name: "lookup", Arguments: "{}"}},
	}
	ReplayToSink(rec.sink(streamed), ReplayedResponse{
		Reasoning:  "Think it through",
		Text:       []string{"Answer one.", "Answer two."},
		ToolCalls:  calls,
		StopReason: "tool_use",
	})
	want := []string{
		"started",
		"reasoning:Think it through",
		"reasoning-done",
		"delta:Answer one.",
		"text-block-done",
		"delta:Answer two.",
		"text-block-done",
		"ready:weather",
		"ready:lookup",
		"stop:tool_use",
		"message-stop",
	}
	if len(rec.events) != len(want) {
		t.Fatalf("events = %v\nwant          %v", rec.events, want)
	}
	for i := range want {
		if rec.events[i] != want[i] {
			t.Fatalf("events = %v\nwant          %v", rec.events, want)
		}
	}
	if !*streamed {
		t.Fatal("Streamed was not set")
	}
}

func TestReplayToSinkDefaultsStopReasonAndSkipsEmptyBlocks(t *testing.T) {
	t.Parallel()

	rec := &replayTrace{}
	ReplayToSink(rec.sink(nil), ReplayedResponse{
		Text:       []string{"  ", "Only block."},
		StopReason: "",
	})
	want := []string{"started", "delta:Only block.", "text-block-done", "stop:stop", "message-stop"}
	if len(rec.events) != len(want) {
		t.Fatalf("events = %v\nwant          %v", rec.events, want)
	}
	for i := range want {
		if rec.events[i] != want[i] {
			t.Fatalf("events = %v\nwant          %v", rec.events, want)
		}
	}
}

func TestReplayToSinkToleratesNilSink(t *testing.T) {
	t.Parallel()

	ReplayToSink(nil, ReplayedResponse{Reasoning: "r", Text: []string{"t"}})
}

func TestNonStreamingErrorWrapsTheDeadline(t *testing.T) {
	t.Parallel()

	orig := NonStreamingTimeout
	NonStreamingTimeout = 10 * time.Millisecond
	t.Cleanup(func() { NonStreamingTimeout = orig })

	ctx, cancel := WithNonStreamingDeadline(context.Background())
	defer cancel()
	<-ctx.Done()

	err := NonStreamingError(ctx, context.DeadlineExceeded)
	if !errors.Is(err, ErrNonStreamingTimeout) {
		t.Fatalf("NonStreamingError = %v, want ErrNonStreamingTimeout", err)
	}
	if !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("error text = %q, want the original failure preserved", err)
	}
}

func TestNonStreamingErrorLeavesOtherErrorsAlone(t *testing.T) {
	t.Parallel()

	plain := errors.New("provider rejected the shape")
	if got := NonStreamingError(context.Background(), plain); got != plain {
		t.Fatalf("NonStreamingError = %v, want the original error", got)
	}

	// A deadline that is not the non-streaming one must not be rewrapped
	// either: only WithNonStreamingDeadline's cause turns into the timeout.
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	if got := NonStreamingError(ctx, plain); got != plain {
		t.Fatalf("NonStreamingError = %v, want the original error", got)
	}
	if got := NonStreamingError(ctx, nil); got != nil {
		t.Fatalf("NonStreamingError(nil) = %v, want nil", got)
	}
}

func TestNonStreamingRequestContextSwitch(t *testing.T) {
	t.Parallel()

	if NonStreamingRequestFrom(context.Background()) {
		t.Fatal("background context reports the non-streaming switch")
	}
	ctx := WithNonStreamingRequest(context.Background())
	if !NonStreamingRequestFrom(ctx) {
		t.Fatal("switched context does not report the non-streaming switch")
	}
}
