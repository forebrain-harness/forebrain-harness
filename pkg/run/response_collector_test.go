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

// The reasoning a response completed before it was cut rides the message the
// collector builds: every complete unit replaces the one before it, and the
// collected state has to be complete even when nobody downstream listens.
func TestResponseCollectorKeepsTheReasoningCarry(t *testing.T) {
	t.Parallel()

	first := llm.EncodeThinking([]llm.ThinkingBlock{{Thinking: "step one", Signature: "sig-1"}})
	second := llm.EncodeThinking([]llm.ThinkingBlock{
		{Thinking: "step one", Signature: "sig-1"},
		{Thinking: "step two", Signature: "sig-2"},
	})

	ctx, collector := withResponseCollector(llm.WithStreamSink(context.Background(), &llm.StreamSink{
		OnDelta:          func(string) {},
		OnReasoningCarry: func(string, string) {},
	}))
	sink := llm.StreamSinkFrom(ctx)
	sink.OnDelta("half an answer")
	sink.OnTextBlockDone()
	sink.OnReasoningCarry(first, "claude-opus-4-6")
	sink.OnReasoningCarry(second, "claude-opus-4-6")

	res := collector.result()
	if res.Message.Name != second {
		t.Fatalf("carry = %q, want the last complete unit %q", res.Message.Name, second)
	}
	if res.Message.ReasoningModel != "claude-opus-4-6" {
		t.Fatalf("ReasoningModel = %q, want the producing model", res.Message.ReasoningModel)
	}
	// The text and the reasoning are one reply: the carry does not disturb the
	// parts the collector rebuilt.
	if got := res.Message.TextContent(); got != "half an answer" {
		t.Fatalf("text = %q, want the finished block", got)
	}

	// A sinkless context still collects: nothing downstream listens, but the
	// message the collector finishes a request from must carry its reasoning.
	ctx, collector = withResponseCollector(llm.WithStreamSink(context.Background(), &llm.StreamSink{}))
	sink = llm.StreamSinkFrom(ctx)
	sink.OnReasoningCarry(first, "claude-opus-4-6")
	if got, model := collector.reasoningCarry(); got != first || model != "claude-opus-4-6" {
		t.Fatalf("carry = %q/%q, want %q recorded without a listener", got, model, first)
	}
}

func TestResponseCollectorTransportRetryDropsTheCarry(t *testing.T) {
	t.Parallel()

	carry := llm.EncodeThinking([]llm.ThinkingBlock{{Thinking: "step one", Signature: "sig-1"}})
	ctx, collector := withResponseCollector(llm.WithStreamSink(context.Background(), &llm.StreamSink{
		OnDelta:          func(string) {},
		OnReasoningCarry: func(string, string) {},
	}))
	sink := llm.StreamSinkFrom(ctx)
	sink.OnReasoningCarry(carry, "claude-opus-4-6")

	// A resend re-streams the reply, publishing its own carry as it goes: the
	// carry of the dead attempt must not end up on the new one.
	sink.OnTransportRetry(llm.TransportRetry{Retry: 1, Total: 3})
	if got, model := collector.reasoningCarry(); got != "" || model != "" {
		t.Fatalf("carry after a resend notice = %q/%q, want it dropped", got, model)
	}

	// The catch-up Cleared only drops the block still being streamed: it does
	// not precede a resend, so the reasoning that completed stays.
	sink.OnReasoningCarry(carry, "claude-opus-4-6")
	sink.OnTransportRetry(llm.TransportRetry{Cleared: true})
	if got, _ := collector.reasoningCarry(); got != carry {
		t.Fatalf("carry after a cleared notice = %q, want %q kept", got, carry)
	}
}
