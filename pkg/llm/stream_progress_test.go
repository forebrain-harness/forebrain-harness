package llm

import (
	"context"
	"testing"
	"time"
)

// progressTestSink has every boundary callback set, so WithStreamProgress
// chains recorders behind live downstreams for all of them.
func progressTestSink() *StreamSink {
	return &StreamSink{
		OnDelta:           func(string) {},
		OnReasoningDelta:  func(string) {},
		OnReasoningDone:   func() {},
		OnResponseStarted: func() {},
		OnTextBlockDone:   func() {},
		OnToolCallReady:   func(ToolCall) {},
		OnStopReason:      func(string) {},
		OnMessageStop:     func() {},
	}
}

func TestStreamProgressKind(t *testing.T) {
	tests := []struct {
		name  string
		drive func(sink *StreamSink)
		want  StreamProgressKind
	}{
		{"no events at all", func(sink *StreamSink) {}, ProgressNothing},
		{"response started only", func(sink *StreamSink) {
			sink.OnResponseStarted()
		}, ProgressStarted},
		{"reasoning delta only", func(sink *StreamSink) {
			sink.OnReasoningDelta("hm")
		}, ProgressStarted},
		{"text delta only", func(sink *StreamSink) {
			sink.OnDelta("he")
		}, ProgressPartialOutput},
		{"reasoning then reasoning done", func(sink *StreamSink) {
			sink.OnReasoningDelta("hm")
			sink.OnReasoningDone()
		}, ProgressThinkingOnly},
		{"reasoning delta then text delta", func(sink *StreamSink) {
			sink.OnReasoningDelta("hm")
			sink.OnDelta("he")
		}, ProgressOutput},
		{"text block done", func(sink *StreamSink) {
			sink.OnDelta("he")
			sink.OnTextBlockDone()
		}, ProgressOutput},
		{"tool call ready", func(sink *StreamSink) {
			sink.OnToolCallReady(ToolCall{ID: "call_1"})
		}, ProgressOutput},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, progress := WithStreamProgress(WithStreamSink(context.Background(), progressTestSink()))
			tt.drive(StreamSinkFrom(ctx))
			if got := progress.Snapshot().Kind; got != tt.want {
				t.Fatalf("Kind = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStreamProgressStopReasonAndComplete(t *testing.T) {
	ctx, progress := WithStreamProgress(WithStreamSink(context.Background(), progressTestSink()))
	sink := StreamSinkFrom(ctx)
	sink.OnStopReason("tool_calls")
	sink.OnMessageStop()
	snap := progress.Snapshot()
	if !snap.StopReasonReceived {
		t.Fatal("StopReasonReceived = false after OnStopReason")
	}
	if !snap.Complete {
		t.Fatal("Complete = false after OnMessageStop")
	}
}

func TestStreamProgressReset(t *testing.T) {
	ctx, progress := WithStreamProgress(WithStreamSink(context.Background(), progressTestSink()))
	sink := StreamSinkFrom(ctx)
	sink.OnReasoningDelta("hm")
	sink.OnDelta("he")
	sink.OnStopReason("stop")
	sink.OnMessageStop()
	progress.MarkToolDispatched()
	if before := progress.Snapshot(); before.Kind != ProgressOutput || !before.Complete || !before.ToolDispatched {
		t.Fatalf("pre-reset snapshot = %+v", before)
	}
	time.Sleep(2 * time.Millisecond)
	progress.Reset()
	after := progress.Snapshot()
	if after.Kind != ProgressNothing {
		t.Fatalf("Kind after Reset = %v, want ProgressNothing", after.Kind)
	}
	if after.StopReasonReceived || after.Complete || after.ToolDispatched {
		t.Fatalf("flags after Reset = %+v", after)
	}
	if after.Elapsed >= time.Millisecond {
		t.Fatalf("Elapsed after Reset = %v, want restarted from ~0", after.Elapsed)
	}
}

func TestStreamProgressMarkToolDispatchedBlocksResend(t *testing.T) {
	_, progress := WithStreamProgress(context.Background())
	if snap := progress.Snapshot(); !snap.ResendSafe() {
		t.Fatal("fresh record must be resend-safe")
	}
	progress.MarkToolDispatched()
	if snap := progress.Snapshot(); snap.ResendSafe() {
		t.Fatal("ResendSafe = true after MarkToolDispatched")
	}
}

func TestStreamProgressResendSafeByKind(t *testing.T) {
	tests := []struct {
		kind StreamProgressKind
		want bool
	}{
		{ProgressNothing, true},
		{ProgressStarted, true},
		{ProgressPartialOutput, true},
		{ProgressThinkingOnly, true},
		{ProgressOutput, false},
	}
	for _, tt := range tests {
		snap := StreamProgressSnapshot{Kind: tt.kind}
		if got := snap.ResendSafe(); got != tt.want {
			t.Fatalf("ResendSafe(Kind=%v) = %v, want %v", tt.kind, got, tt.want)
		}
	}
}

func TestStreamProgressElapsedMonotonicAndSleptZero(t *testing.T) {
	ctx, progress := WithStreamProgress(WithStreamSink(context.Background(), progressTestSink()))
	StreamSinkFrom(ctx).OnDelta("he")
	first := progress.Snapshot()
	time.Sleep(2 * time.Millisecond)
	second := progress.Snapshot()
	if second.Elapsed <= first.Elapsed {
		t.Fatalf("Elapsed did not grow: %v then %v", first.Elapsed, second.Elapsed)
	}
	for _, snap := range []StreamProgressSnapshot{first, second} {
		if snap.Slept < 0 {
			t.Fatalf("Slept = %v, want non-negative", snap.Slept)
		}
		// Wall and monotonic clocks are read separately, so NTP slew shows up
		// as a few hundred nanoseconds without any sleep. A real sleep is
		// milliseconds at minimum; anything below this bound counts as zero.
		if snap.Slept >= time.Millisecond {
			t.Fatalf("Slept = %v without sleeping, want ~0", snap.Slept)
		}
	}
}

func TestStreamProgressWithoutSinkStaysNothing(t *testing.T) {
	ctx, progress := WithStreamProgress(context.Background())
	if progress == nil {
		t.Fatal("progress record is nil")
	}
	if snap := progress.Snapshot(); snap.Kind != ProgressNothing {
		t.Fatalf("Kind = %v, want ProgressNothing", snap.Kind)
	}
	if StreamSinkFrom(ctx) != nil {
		t.Fatal("context without a sink gained one")
	}
}

func TestStreamProgressKeepsNilDeltaMuted(t *testing.T) {
	base := &StreamSink{OnUsage: func(int, int) {}}
	ctx, _ := WithStreamProgress(WithStreamSink(context.Background(), base))
	if sink := StreamSinkFrom(ctx); sink.OnDelta != nil {
		t.Fatal("OnDelta installed on a sink whose OnDelta was nil")
	}
}

func TestMutedForegroundStreamDropsProgress(t *testing.T) {
	base := &StreamSink{OnDelta: func(string) {}}
	ctxWithProgress, _ := WithStreamProgress(WithStreamSink(context.Background(), base))
	muted := WithMutedForegroundStream(ctxWithProgress)
	if got := StreamProgressFrom(muted); got != nil {
		t.Fatalf("StreamProgressFrom(muted ctx) = %v, want nil", got)
	}
	if sink := StreamSinkFrom(muted); sink.OnDelta != nil {
		t.Fatal("muted sink still has OnDelta")
	}
}
