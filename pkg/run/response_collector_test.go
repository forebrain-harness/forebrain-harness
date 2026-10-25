package run

import (
	"context"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

func TestResponseCollectorCollectsBlocksAndCalls(t *testing.T) {
	t.Parallel()

	var deltas, blocksDone int
	var calls []llm.ToolCall
	base := &llm.StreamSink{
		OnDelta:         func(string) { deltas++ },
		OnTextBlockDone: func() { blocksDone++ },
		OnToolCallReady: func(c llm.ToolCall) { calls = append(calls, c) },
	}
	ctx, collector := withResponseCollector(llm.WithStreamSink(context.Background(), base))
	sink := llm.StreamSinkFrom(ctx)

	sink.OnDelta("first block")
	sink.OnTextBlockDone()
	sink.OnDelta("second ")
	sink.OnDelta("block")
	call := llm.ToolCall{ID: "call_1", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "shell", Arguments: `{"command":"echo kept"}`}}
	sink.OnToolCallReady(call)

	if deltas != 3 || blocksDone != 1 || len(calls) != 1 {
		t.Fatalf("downstream callbacks: deltas=%d blocks=%d calls=%d", deltas, blocksDone, len(calls))
	}
	res := collector.result()
	if res == nil || res.Message == nil {
		t.Fatalf("result = %#v", res)
	}
	if got := res.Message.TextContent(); got != "first block\nsecond block" {
		t.Fatalf("text = %q, want both finished blocks", got)
	}
	if len(res.Message.Parts) != 2 {
		t.Fatalf("parts = %d, want one per finished block", len(res.Message.Parts))
	}
	if len(res.Message.ToolCalls) != 1 || res.Message.ToolCalls[0].ID != "call_1" {
		t.Fatalf("tool calls = %+v, want the finished call", res.Message.ToolCalls)
	}
}

func TestResponseCollectorSealCurrentCountsTheLastBlock(t *testing.T) {
	t.Parallel()

	ctx, collector := withResponseCollector(llm.WithStreamSink(context.Background(), &llm.StreamSink{OnDelta: func(string) {}}))
	sink := llm.StreamSinkFrom(ctx)
	sink.OnDelta("the answer that fully arrived")
	collector.sealCurrent()
	res := collector.result()
	if got := len(res.Message.Parts); got != 1 {
		t.Fatalf("parts = %d, want the sealed last block", got)
	}
	if res.Message.TextContent() != "the answer that fully arrived" {
		t.Fatalf("text = %q", res.Message.TextContent())
	}
	// A blank block seals to nothing.
	sink.OnDelta("   ")
	collector.sealCurrent()
	if res := collector.result(); len(res.Message.Parts) != 1 {
		t.Fatalf("parts = %d after a blank seal, want unchanged", len(res.Message.Parts))
	}
}

func TestResponseCollectorTransportRetryWithdraws(t *testing.T) {
	t.Parallel()

	ctx, collector := withResponseCollector(llm.WithStreamSink(context.Background(), &llm.StreamSink{OnDelta: func(string) {}}))
	sink := llm.StreamSinkFrom(ctx)
	sink.OnDelta("finished")
	sink.OnTextBlockDone()
	call := llm.ToolCall{ID: "call_1", Type: llm.ToolTypeFunction}
	sink.OnToolCallReady(call)

	// A plain resend withdraws everything the attempt produced.
	sink.OnTransportRetry(llm.TransportRetry{Retry: 1, Total: 3})
	if res := collector.result(); len(res.Message.Parts) != 0 || len(res.Message.ToolCalls) != 0 {
		t.Fatalf("after a resend notice: %+v, want everything withdrawn", res.Message)
	}

	// A Cleared that also withdraws finished thinking clears everything.
	sink.OnDelta("another")
	sink.OnTextBlockDone()
	sink.OnTransportRetry(llm.TransportRetry{Cleared: true, DropFinishedThinking: true})
	if res := collector.result(); len(res.Message.Parts) != 0 {
		t.Fatalf("after a thinking withdraw: %+v, want everything withdrawn", res.Message)
	}

	// The catch-up Cleared drops only the block still being streamed.
	sink.OnDelta("finished block")
	sink.OnTextBlockDone()
	sink.OnDelta("in flight")
	sink.OnTransportRetry(llm.TransportRetry{Cleared: true})
	res := collector.result()
	if len(res.Message.Parts) != 1 || res.Message.TextContent() != "finished block" {
		t.Fatalf("after a cleared notice: %+v, want only the in-flight block dropped", res.Message)
	}
}

func TestResponseCollectorWorksWithoutASink(t *testing.T) {
	t.Parallel()

	ctx, collector := withResponseCollector(context.Background())
	if llm.StreamSinkFrom(ctx) != nil {
		t.Fatal("a sinkless context must not gain one")
	}
	if res := collector.result(); res == nil || res.Message == nil || len(res.Message.Parts) != 0 {
		t.Fatalf("result = %#v, want an empty assistant message", res)
	}
}
