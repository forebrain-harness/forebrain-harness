package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

func TestAnthropicAgentLLMAlwaysStreamsAndAggregates(t *testing.T) {
	t.Setenv("FOREBRAIN_LLM_HTTP_DEBUG", "0")

	type capturedRequest struct {
		path string
		body map[string]any
	}
	requests := make(chan capturedRequest, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseServer := func() { releaseOnce.Do(func() { close(release) }) }

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		requests <- capturedRequest{path: r.URL.Path, body: body}

		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		writeAnthropicSSEEvent(t, w, "message_start", `{"type":"message_start","message":{"id":"msg_test","type":"message","role":"assistant","model":"claude-opus-4-6","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"cache_creation_input_tokens":3,"cache_read_input_tokens":2,"output_tokens":1}}}`)
		writeAnthropicSSEEvent(t, w, "content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"Hel"}}`)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}

		<-release
		writeAnthropicSSEEvent(t, w, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}`)
		writeAnthropicSSEEvent(t, w, "content_block_stop", `{"type":"content_block_stop","index":0}`)
		writeAnthropicSSEEvent(t, w, "content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"thinking","thinking":"Think"}}`)
		writeAnthropicSSEEvent(t, w, "content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"thinking_delta","thinking":"ing"}}`)
		writeAnthropicSSEEvent(t, w, "content_block_stop", `{"type":"content_block_stop","index":1}`)
		writeAnthropicSSEEvent(t, w, "content_block_start", `{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_test","name":"weather","input":{}}}`)
		writeAnthropicSSEEvent(t, w, "content_block_delta", `{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"city\":"}}`)
		writeAnthropicSSEEvent(t, w, "content_block_delta", `{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"\"Paris\"}"}}`)
		writeAnthropicSSEEvent(t, w, "content_block_stop", `{"type":"content_block_stop","index":2}`)
		writeAnthropicSSEEvent(t, w, "message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"input_tokens":10,"cache_creation_input_tokens":3,"cache_read_input_tokens":2,"output_tokens":9}}`)
		writeAnthropicSSEEvent(t, w, "message_stop", `{"type":"message_stop"}`)
	}))
	t.Cleanup(func() {
		releaseServer()
		server.Close()
	})

	client := NewAgentLLM("test-key", server.URL, "claude-opus-4-6", 128_000)
	var streamed bool
	var textDeltas, reasoningDeltas []string
	var usageInput, usageOutput int
	var usageSnapshots [][2]int
	var reasoningDone, ended int
	responseStarts := 0
	var callbackOrder []string
	firstDelta := make(chan struct{})
	var firstDeltaOnce sync.Once
	ctx := llm.WithStreamSink(context.Background(), &llm.StreamSink{
		Streamed: &streamed,
		OnResponseStarted: func() {
			responseStarts++
			callbackOrder = append(callbackOrder, "started")
		},
		OnDelta: func(text string) {
			callbackOrder = append(callbackOrder, "text")
			textDeltas = append(textDeltas, text)
			firstDeltaOnce.Do(func() { close(firstDelta) })
		},
		OnReasoningDelta: func(text string) {
			reasoningDeltas = append(reasoningDeltas, text)
		},
		OnReasoningDone: func() { reasoningDone++ },
		OnUsage: func(inputTokens, outputTokens int) {
			usageInput += inputTokens
			usageOutput += outputTokens
		},
		OnUsageSnapshot: func(inputTokens, outputTokens int) {
			usageSnapshots = append(usageSnapshots, [2]int{inputTokens, outputTokens})
		},
		OnEnd: func() { ended++ },
	})

	type executeResult struct {
		result *llm.Result
		err    error
	}
	done := make(chan executeResult, 1)
	go func() {
		result, err := client.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil)
		done <- executeResult{result: result, err: err}
	}()

	var req capturedRequest
	select {
	case req = <-requests:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Anthropic request")
	}
	if req.path != "/v1/messages" {
		t.Fatalf("request path = %q, want /v1/messages", req.path)
	}
	if stream, ok := req.body["stream"].(bool); !ok || !stream {
		t.Fatalf("request stream = %#v, want true", req.body["stream"])
	}
	if maxTokens, ok := req.body["max_tokens"].(float64); !ok || maxTokens != 128_000 {
		t.Fatalf("request max_tokens = %#v, want 128000", req.body["max_tokens"])
	}

	select {
	case <-firstDelta:
	case <-time.After(5 * time.Second):
		t.Fatal("first Anthropic text delta did not arrive while server was still streaming")
	}
	select {
	case got := <-done:
		t.Fatalf("Execute completed before server released: result=%#v err=%v", got.result, got.err)
	default:
	}
	releaseServer()

	var got executeResult
	select {
	case got = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Anthropic stream completion")
	}
	if got.err != nil {
		t.Fatalf("Execute returned error: %v", got.err)
	}
	if got.result == nil || got.result.Message == nil || got.result.Usage == nil {
		t.Fatalf("incomplete result: %#v", got.result)
	}
	if text := got.result.Message.TextContent(); text != "Hello" {
		t.Fatalf("message text = %q, want Hello", text)
	}
	if len(got.result.Message.ToolCalls) != 1 {
		t.Fatalf("tool calls = %#v, want one", got.result.Message.ToolCalls)
	}
	toolCall := got.result.Message.ToolCalls[0]
	if toolCall.ID != "toolu_test" || toolCall.Function.Name != "weather" || toolCall.Function.Arguments != `{"city":"Paris"}` {
		t.Fatalf("tool call = %#v", toolCall)
	}
	if got.result.Usage.InputTokens != 10 || got.result.Usage.OutputTokens != 9 ||
		got.result.Usage.CacheCreationInputTokens != 3 || got.result.Usage.CacheReadInputTokens != 2 {
		t.Fatalf("usage = %#v", got.result.Usage)
	}
	if !streamed {
		t.Fatal("Streamed was not marked")
	}
	if strings.Join(textDeltas, "") != "Hello" {
		t.Fatalf("text deltas = %#v", textDeltas)
	}
	if strings.Join(reasoningDeltas, "") != "Thinking" || reasoningDone != 1 {
		t.Fatalf("reasoning deltas/done = %#v/%d", reasoningDeltas, reasoningDone)
	}
	if usageInput != 15 || usageOutput != 9 {
		t.Fatalf("stream usage = %d/%d, want 15/9", usageInput, usageOutput)
	}
	wantSnapshots := [][2]int{{15, 1}, {15, 9}}
	if !reflect.DeepEqual(usageSnapshots, wantSnapshots) {
		t.Fatalf("usage snapshots = %#v, want %#v", usageSnapshots, wantSnapshots)
	}
	if ended != 1 {
		t.Fatalf("OnEnd calls = %d, want 1", ended)
	}
	if responseStarts != 1 || len(callbackOrder) == 0 || callbackOrder[0] != "started" {
		t.Fatalf("response-start callbacks=%d order=%v", responseStarts, callbackOrder)
	}
}

func TestAnthropicAgentLLMPropagatesStreamingError(t *testing.T) {
	t.Setenv("FOREBRAIN_LLM_HTTP_DEBUG", "0")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		writeAnthropicSSEEvent(t, w, "message_start", `{"type":"message_start","message":{"id":"msg_test","type":"message","role":"assistant","model":"claude-opus-4-6","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":4,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":1}}}`)
		writeAnthropicSSEEvent(t, w, "error", `{"type":"error","error":{"type":"overloaded_error","message":"try again"}}`)
	}))
	defer server.Close()

	client := NewAgentLLM("test-key", server.URL, "claude-opus-4-6", 128_000)
	var streamed bool
	ended := 0
	ctx := llm.WithStreamSink(context.Background(), &llm.StreamSink{
		Streamed: &streamed,
		OnEnd:    func() { ended++ },
	})
	result, err := client.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil)
	if err == nil || !strings.Contains(err.Error(), "overloaded_error") {
		t.Fatalf("Execute error = %v, want streaming overloaded_error", err)
	}
	if result != nil {
		t.Fatalf("result = %#v, want nil", result)
	}
	if !streamed {
		t.Fatal("Streamed was not marked")
	}
	if ended != 1 {
		t.Fatalf("OnEnd calls = %d, want 1", ended)
	}
}

func TestAnthropicAgentLLMStreamCleanCloseZeroEventsIsNoEvents(t *testing.T) {
	t.Setenv("FOREBRAIN_LLM_HTTP_DEBUG", "0")

	// message_start and a clean close, no content block and no stop reason:
	// the stream produced nothing at all and is re-sent without streaming.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		writeAnthropicSSEEvent(t, w, "message_start", `{"type":"message_start","message":{"id":"msg_test","type":"message","role":"assistant","model":"claude-opus-4-6","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":4,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":1}}}`)
	}))
	defer server.Close()

	client := NewAgentLLM("test-key", server.URL, "claude-opus-4-6", 128_000)
	var streamed bool
	ended := 0
	ctx := llm.WithStreamSink(context.Background(), &llm.StreamSink{
		Streamed: &streamed,
		OnEnd:    func() { ended++ },
	})
	result, err := client.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil)
	if !errors.Is(err, llm.ErrStreamNoEvents) {
		t.Fatalf("Execute error = %v, want ErrStreamNoEvents", err)
	}
	if result != nil {
		t.Fatalf("result = %#v, want nil", result)
	}
	if !streamed {
		t.Fatal("Streamed was not marked")
	}
	if ended != 1 {
		t.Fatalf("OnEnd calls = %d, want 1", ended)
	}
}

// TestAnthropicAgentLLMUsesCacheFieldsFromMessageDelta covers a real bug
// found via a live third-party Anthropic-compatible endpoint (an Alibaba
// MaaS gateway proxying Qwen): anthropic-sdk-go's Message.Accumulate only
// copies OutputTokens out of a MessageDeltaEvent into the accumulated
// Message.Usage (see messageutil.go's MessageDeltaEvent case), silently
// dropping InputTokens/CacheCreationInputTokens/CacheReadInputTokens even
// when the SDK's own parsed event.Usage for that event carries them
// correctly. TestAnthropicAgentLLMAlwaysStreamsAndAggregates above didn't
// catch this because its mock message_start already carries the same cache
// numbers as message_delta, so Accumulate's message_start branch (a full
// struct overwrite) already set the right values before the broken
// message_delta branch ran — this test's mock instead puts zero cache
// counters on message_start and the real numbers only on message_delta,
// matching what the live gateway actually does (cache accounting isn't
// known until the response is scored, not at message_start), which is the
// shape that exposed the bug. Execute must read Usage from the raw
// per-event fields itself rather than trusting the SDK's accumulated
// Message.Usage.
func TestAnthropicAgentLLMUsesCacheFieldsFromMessageDelta(t *testing.T) {
	t.Setenv("FOREBRAIN_LLM_HTTP_DEBUG", "0")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		writeAnthropicSSEEvent(t, w, "message_start", `{"type":"message_start","message":{"id":"msg_test","type":"message","role":"assistant","model":"claude-opus-4-6","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":3800,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":0}}}`)
		writeAnthropicSSEEvent(t, w, "content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
		writeAnthropicSSEEvent(t, w, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi"}}`)
		writeAnthropicSSEEvent(t, w, "content_block_stop", `{"type":"content_block_stop","index":0}`)
		writeAnthropicSSEEvent(t, w, "message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":6,"cache_creation_input_tokens":0,"cache_read_input_tokens":3822,"output_tokens":81}}`)
		writeAnthropicSSEEvent(t, w, "message_stop", `{"type":"message_stop"}`)
	}))
	defer server.Close()

	client := NewAgentLLM("test-key", server.URL, "claude-opus-4-6", 128_000)
	result, err := client.Execute(context.Background(), []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result == nil || result.Usage == nil {
		t.Fatalf("incomplete result: %#v", result)
	}
	if result.Usage.CacheReadInputTokens != 3822 {
		t.Fatalf("Usage.CacheReadInputTokens = %d, want 3822 (from message_delta, not the zero on message_start)", result.Usage.CacheReadInputTokens)
	}
	if result.Usage.InputTokens != 6 {
		t.Fatalf("Usage.InputTokens = %d, want 6 (message_delta's final accounting, not message_start's pre-cache-split total)", result.Usage.InputTokens)
	}
	if result.Usage.OutputTokens != 81 {
		t.Fatalf("Usage.OutputTokens = %d, want 81", result.Usage.OutputTokens)
	}
}

func writeAnthropicSSEEvent(t *testing.T, w http.ResponseWriter, event, data string) {
	t.Helper()
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
		t.Errorf("write SSE event %s: %v", event, err)
	}
}

// anthropicBlockSignalServer streams one text block, one tool_use block, the
// terminal message_delta and (unless cutAfterMessageDelta) message_stop.
func anthropicBlockSignalServer(t *testing.T, cutAfterMessageDelta bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		writeAnthropicSSEEvent(t, w, "message_start", `{"type":"message_start","message":{"id":"msg_test","type":"message","role":"assistant","model":"claude-opus-4-6","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":1}}}`)
		writeAnthropicSSEEvent(t, w, "content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"Answ"}}`)
		writeAnthropicSSEEvent(t, w, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"er here."}}`)
		writeAnthropicSSEEvent(t, w, "content_block_stop", `{"type":"content_block_stop","index":0}`)
		writeAnthropicSSEEvent(t, w, "content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_ready","name":"weather","input":{}}}`)
		writeAnthropicSSEEvent(t, w, "content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"city\":"}}`)
		writeAnthropicSSEEvent(t, w, "content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"Oslo\"}"}}`)
		writeAnthropicSSEEvent(t, w, "content_block_stop", `{"type":"content_block_stop","index":1}`)
		writeAnthropicSSEEvent(t, w, "message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"input_tokens":10,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":12}}`)
		if !cutAfterMessageDelta {
			writeAnthropicSSEEvent(t, w, "message_stop", `{"type":"message_stop"}`)
		}
	}))
}

func TestAnthropicStreamBlockSignals(t *testing.T) {
	t.Setenv("FOREBRAIN_LLM_HTTP_DEBUG", "0")

	server := anthropicBlockSignalServer(t, false)
	defer server.Close()

	client := NewAgentLLM("test-key", server.URL, "claude-opus-4-6", 128_000)
	var order []string
	var ready []llm.ToolCall
	ctx := llm.WithStreamSink(context.Background(), &llm.StreamSink{
		OnTextBlockDone: func() { order = append(order, "text_block_done") },
		OnToolCallReady: func(call llm.ToolCall) {
			order = append(order, "ready")
			ready = append(ready, call)
		},
		OnStopReason:  func(reason string) { order = append(order, "stop_reason:"+reason) },
		OnMessageStop: func() { order = append(order, "message_stop") },
	})
	res, err := client.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	want := []string{"text_block_done", "ready", "stop_reason:tool_use", "message_stop"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("callback order = %v\nwant             %v", order, want)
	}
	if len(ready) != 1 || len(res.Message.ToolCalls) != 1 {
		t.Fatalf("ready=%d final=%d, want 1 and 1", len(ready), len(res.Message.ToolCalls))
	}
	if !reflect.DeepEqual(ready[0], res.Message.ToolCalls[0]) {
		t.Fatalf("ready = %+v, final = %+v, want identical", ready[0], res.Message.ToolCalls[0])
	}
	if ready[0].ID != "toolu_ready" || ready[0].Function.Name != "weather" || ready[0].Function.Arguments != `{"city":"Oslo"}` {
		t.Fatalf("ready call = %+v", ready[0])
	}
}

// A reply made only of a tool call has started at its tool_use block: the
// response start fires there, once, before the call's arguments stream — not
// at some later response's first word, which is what the turn's first-token
// wait and a provisional steer's delivery are measured against.
func TestAnthropicStreamToolOnlyReplyStartsAtItsCall(t *testing.T) {
	t.Setenv("FOREBRAIN_LLM_HTTP_DEBUG", "0")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		writeAnthropicSSEEvent(t, w, "message_start", `{"type":"message_start","message":{"id":"msg_test","type":"message","role":"assistant","model":"claude-opus-4-6","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":1}}}`)
		writeAnthropicSSEEvent(t, w, "content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_only","name":"weather","input":{}}}`)
		writeAnthropicSSEEvent(t, w, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\":\"Oslo\"}"}}`)
		writeAnthropicSSEEvent(t, w, "content_block_stop", `{"type":"content_block_stop","index":0}`)
		writeAnthropicSSEEvent(t, w, "message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"input_tokens":10,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":12}}`)
		writeAnthropicSSEEvent(t, w, "message_stop", `{"type":"message_stop"}`)
	}))
	defer server.Close()

	client := NewAgentLLM("test-key", server.URL, "claude-opus-4-6", 128_000)
	trace := newAnthropicReplayTrace()
	ctx := llm.WithStreamSink(context.Background(), trace.sink())
	if _, err := client.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	want := []string{"started", "ready:weather", "stop:tool_use", "message-stop"}
	if !reflect.DeepEqual(trace.trace, want) {
		t.Fatalf("sink trace = %v\nwant           %v", trace.trace, want)
	}
}

func TestAnthropicStreamStopReasonWithoutMessageStop(t *testing.T) {
	t.Setenv("FOREBRAIN_LLM_HTTP_DEBUG", "0")

	server := anthropicBlockSignalServer(t, true)
	defer server.Close()

	client := NewAgentLLM("test-key", server.URL, "claude-opus-4-6", 128_000)
	var order []string
	ctx := llm.WithStreamSink(context.Background(), &llm.StreamSink{
		OnTextBlockDone: func() { order = append(order, "text_block_done") },
		OnToolCallReady: func(llm.ToolCall) { order = append(order, "ready") },
		OnStopReason:    func(reason string) { order = append(order, "stop_reason:"+reason) },
		OnMessageStop:   func() { order = append(order, "message_stop") },
	})
	_, err := client.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	want := []string{"text_block_done", "ready", "stop_reason:tool_use"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("callback order = %v\nwant             %v", order, want)
	}
}

// anthropicNonStreamingBody is one whole message carrying every piece a replay
// must deliver: two thinking blocks, a text block, a tool call, the stop
// reason and usage with both cache buckets. The 64k maxTokens used by these
// tests is production-shaped: without an explicit request timeout the SDK
// locally rejects a non-streaming call whose maxTokens implies more than ten
// minutes of work, so these tests only pass with the stated timeout.
const anthropicNonStreamingBody = `{"id":"msg_test","type":"message","role":"assistant","model":"claude-opus-4-6","content":[{"type":"thinking","thinking":"Think it "},{"type":"thinking","thinking":"through"},{"type":"text","text":"Hello there"},{"type":"tool_use","id":"toolu_test","name":"weather","input":{"city":"Paris"}}],"stop_reason":"tool_use","stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":9,"cache_creation_input_tokens":3,"cache_read_input_tokens":2}}`

// anthropicReplayTrace records the sink callbacks in firing order; usage and
// snapshots sit beside the trace because the non-streaming path reports them
// before replaying the answer.
type anthropicReplayTrace struct {
	trace     []string
	streamed  *bool
	ends      int
	ready     []llm.ToolCall
	usage     [][2]int
	snapshots [][2]int
}

func newAnthropicReplayTrace() *anthropicReplayTrace {
	return &anthropicReplayTrace{streamed: new(bool)}
}

func (r *anthropicReplayTrace) sink() *llm.StreamSink {
	return &llm.StreamSink{
		OnResponseStarted: func() { r.trace = append(r.trace, "started") },
		OnReasoningDelta:  func(s string) { r.trace = append(r.trace, "reasoning:"+s) },
		OnReasoningDone:   func() { r.trace = append(r.trace, "reasoning-done") },
		OnDelta:           func(s string) { r.trace = append(r.trace, "delta:"+s) },
		OnTextBlockDone:   func() { r.trace = append(r.trace, "text-block-done") },
		OnToolCallReady: func(c llm.ToolCall) {
			r.ready = append(r.ready, c)
			r.trace = append(r.trace, "ready:"+c.Function.Name)
		},
		OnStopReason:    func(reason string) { r.trace = append(r.trace, "stop:"+reason) },
		OnMessageStop:   func() { r.trace = append(r.trace, "message-stop") },
		OnUsage:         func(in, out int) { r.usage = append(r.usage, [2]int{in, out}) },
		OnUsageSnapshot: func(in, out int) { r.snapshots = append(r.snapshots, [2]int{in, out}) },
		OnEnd:           func() { r.ends++ },
		Streamed:        r.streamed,
	}
}

func TestAnthropicAgentLLMNonStreamingRequestUsesTheUnaryEndpoint(t *testing.T) {
	t.Setenv("FOREBRAIN_LLM_HTTP_DEBUG", "0")

	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(anthropicNonStreamingBody))
	}))
	defer server.Close()

	client := NewAgentLLM("test-key", server.URL, "claude-opus-4-6", 64_000)
	ctx := llm.WithNonStreamingRequest(context.Background())
	result, err := client.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if stream, ok := body["stream"].(bool); ok && stream {
		t.Fatalf("request stream = %#v, want absent or false", body["stream"])
	}
	if got := result.Message.TextContent(); got != "Hello there" {
		t.Fatalf("message text = %q, want Hello there", got)
	}
	if len(result.Message.ToolCalls) != 1 ||
		result.Message.ToolCalls[0].ID != "toolu_test" ||
		result.Message.ToolCalls[0].Function.Name != "weather" ||
		result.Message.ToolCalls[0].Function.Arguments != `{"city":"Paris"}` {
		t.Fatalf("tool calls = %#v", result.Message.ToolCalls)
	}
	if result.Usage.InputTokens != 10 || result.Usage.OutputTokens != 9 ||
		result.Usage.CacheCreationInputTokens != 3 || result.Usage.CacheReadInputTokens != 2 {
		t.Fatalf("usage = %#v", result.Usage)
	}
}

func TestAnthropicAgentLLMNonStreamingResultIsReplayedToTheSink(t *testing.T) {
	t.Setenv("FOREBRAIN_LLM_HTTP_DEBUG", "0")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(anthropicNonStreamingBody))
	}))
	defer server.Close()

	client := NewAgentLLM("test-key", server.URL, "claude-opus-4-6", 64_000)
	rec := newAnthropicReplayTrace()
	ctx := llm.WithNonStreamingRequest(llm.WithStreamSink(context.Background(), rec.sink()))
	result, err := client.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	want := []string{
		"started",
		"reasoning:Think it through",
		"reasoning-done",
		"delta:Hello there",
		"text-block-done",
		"ready:weather",
		"stop:tool_use",
		"message-stop",
	}
	if !reflect.DeepEqual(rec.trace, want) {
		t.Fatalf("sink trace = %v\nwant           %v", rec.trace, want)
	}
	// Usage follows the streaming path's conversion: input plus both cache
	// buckets counts as occupied context.
	if len(rec.usage) != 1 || rec.usage[0] != [2]int{15, 9} {
		t.Fatalf("usage deltas = %v, want one 15/9", rec.usage)
	}
	if len(rec.snapshots) != 1 || rec.snapshots[0] != [2]int{15, 9} {
		t.Fatalf("usage snapshots = %v, want one 15/9", rec.snapshots)
	}
	if len(rec.ready) != 1 || len(result.Message.ToolCalls) != 1 ||
		!reflect.DeepEqual(rec.ready[0], result.Message.ToolCalls[0]) {
		t.Fatalf("ready = %+v, final = %+v, want identical", rec.ready, result.Message.ToolCalls)
	}
	if !*rec.streamed {
		t.Fatal("Streamed was not set")
	}
	if rec.ends != 1 {
		t.Fatalf("OnEnd calls = %d, want 1", rec.ends)
	}
}

// A stream that already delivered its billing frame (message_start carries
// the attempt's input usage) and then dies mid-event must leave that usage
// readable off the returned error: the retry loop adds it to the request's
// total, so a retried attempt's tokens are not silently lost.
func TestAnthropicAgentLLMFailedAttemptCarriesItsUsage(t *testing.T) {
	t.Setenv("FOREBRAIN_LLM_HTTP_DEBUG", "0")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("the httptest writer must support hijacking")
			return
		}
		conn, brw, err := hj.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		defer conn.Close()
		_, _ = brw.WriteString("HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\n\r\n")
		_, _ = brw.WriteString("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_test\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-opus-4-6\",\"content\":[],\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":120,\"cache_creation_input_tokens\":7,\"cache_read_input_tokens\":30,\"output_tokens\":4}}}\n\n")
		// Half an event, then the connection ends: the client's read fails
		// the way a cut connection looks.
		_, _ = brw.WriteString("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"Answ")
		_ = brw.Flush()
	}))
	defer server.Close()

	client := NewAgentLLM("test-key", server.URL, "claude-opus-4-6", 128_000)
	_, err := client.Execute(context.Background(), []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil)
	if err == nil {
		t.Fatal("a cut stream must fail")
	}
	usage := llm.UsageFromError(err)
	if usage == nil {
		t.Fatalf("error = %v, want the attempt's usage readable off it", err)
	}
	// input stays separate from both cache buckets — fb_runs keeps four columns.
	if usage.InputTokens != 120 || usage.CacheCreationInputTokens != 7 ||
		usage.CacheReadInputTokens != 30 || usage.OutputTokens != 4 {
		t.Fatalf("usage = %+v, want the usage message_start reported", usage)
	}
}

func TestAnthropicAgentLLMNonStreamingTimeoutIsRetryable(t *testing.T) {
	t.Setenv("FOREBRAIN_LLM_HTTP_DEBUG", "0")

	orig := llm.NonStreamingTimeout
	llm.NonStreamingTimeout = 50 * time.Millisecond
	t.Cleanup(func() { llm.NonStreamingTimeout = orig })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(1 * time.Second)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(anthropicNonStreamingBody))
	}))
	defer server.Close()

	client := NewAgentLLM("test-key", server.URL, "claude-opus-4-6", 64_000)
	ctx := llm.WithNonStreamingRequest(context.Background())
	result, err := client.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil)
	if err == nil {
		t.Fatalf("Execute returned result %#v, want the non-streaming timeout", result)
	}
	if !errors.Is(err, llm.ErrNonStreamingTimeout) {
		t.Fatalf("error = %v, want ErrNonStreamingTimeout", err)
	}
	if !llm.IsRetryableNetworkError(err) {
		t.Fatalf("IsRetryableNetworkError(%v) = false, want true", err)
	}
}

// anthropicLimitScriptServer serves the given SSE scripts in request order,
// recording every request body. Each script is the raw event payloads of one
// streaming response, written as message events.
func anthropicLimitScriptServer(t *testing.T, scripts []string, bodies *[]map[string]any) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	next := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		i := next
		next++
		*bodies = append(*bodies, body)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		if i >= len(scripts) {
			writeAnthropicSSEEvent(t, w, "message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":1,"output_tokens":1}}`)
			writeAnthropicSSEEvent(t, w, "message_stop", `{"type":"message_stop"}`)
			return
		}
		for _, line := range strings.Split(scripts[i], "\n") {
			if line == "" {
				continue
			}
			parts := strings.SplitN(line, "|", 2)
			writeAnthropicSSEEvent(t, w, parts[0], parts[1])
		}
	}))
}

// anthropicRoundScript is one SSE response whose text deltas join into text
// and whose terminal stop reason is reason. input/output are the round's own
// usage on message_delta (message_start seeds output_tokens with 1).
func anthropicRoundScript(text, reason string, input, output int) string {
	var b strings.Builder
	start := `message_start|{"type":"message_start","message":{"id":"msg_test","type":"message","role":"assistant","model":"claude-opus-4-6","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":` + fmt.Sprint(input) + `,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":1}}}`
	b.WriteString(start + "\n")
	blockStart := `content_block_start|{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`
	b.WriteString(blockStart + "\n")
	for _, piece := range strings.SplitAfter(text, " ") {
		if piece == "" {
			continue
		}
		delta := fmt.Sprintf(`content_block_delta|{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%q}}`, piece)
		b.WriteString(delta + "\n")
	}
	blockStop := `content_block_stop|{"type":"content_block_stop","index":0}`
	b.WriteString(blockStop + "\n")
	msgDelta := fmt.Sprintf(`message_delta|{"type":"message_delta","delta":{"stop_reason":%q,"stop_sequence":null},"usage":{"input_tokens":%d,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":%d}}`, reason, input, output)
	b.WriteString(msgDelta + "\n")
	b.WriteString(`message_stop|{"type":"message_stop"}`)
	return b.String()
}

// A response cut by the output budget is re-asked once with the fragment as
// the assistant's own turn and the resume instruction as a user turn — no
// assistant prefill tail — and the two rounds' text is returned as one answer.
func TestAnthropicContinuesAfterOutputLimit(t *testing.T) {
	t.Setenv("FOREBRAIN_LLM_HTTP_DEBUG", "0")

	var bodies []map[string]any
	server := anthropicLimitScriptServer(t, []string{
		anthropicRoundScript("Half of a sentence.", "max_tokens", 10, 50),
		anthropicRoundScript("The rest.", "end_turn", 25, 7),
	}, &bodies)
	defer server.Close()

	client := NewAgentLLM("test-key", server.URL, "claude-opus-4-6", 64_000)
	ended := 0
	ctx := llm.WithStreamSink(context.Background(), &llm.StreamSink{OnEnd: func() { ended++ }})
	res, err := client.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if len(bodies) != 2 {
		t.Fatalf("requests = %d, want 2", len(bodies))
	}
	msgs, _ := bodies[1]["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("second request carries %d messages, want 3 (user, assistant fragment, resume prompt)", len(msgs))
	}
	fragment, _ := msgs[1].(map[string]any)
	if fragment["role"] != "assistant" {
		t.Fatalf("second request's middle message role = %v, want assistant", fragment["role"])
	}
	if got := anthropicMessageText(t, fragment); got != "Half of a sentence." {
		t.Fatalf("assistant fragment = %q, want the first round's text", got)
	}
	prompt, _ := msgs[2].(map[string]any)
	if prompt["role"] != "user" || !strings.Contains(anthropicMessageText(t, prompt), "Resume directly") {
		t.Fatalf("last message must be the user resume prompt, got %#v", prompt)
	}
	if text := res.Message.TextContent(); text != "Half of a sentence.The rest." {
		t.Fatalf("joined text = %q", text)
	}
	if res.Usage == nil || res.Usage.InputTokens != 25 || res.Usage.OutputTokens != 57 {
		t.Fatalf("usage = %#v, want the settled round's input (25) and both rounds' output (57)", res.Usage)
	}
	billed := res.Billed()
	if billed.InputTokens != 35 || billed.OutputTokens != 57 {
		t.Fatalf("billed = %#v, want both rounds summed (35/57)", billed)
	}
	if ended != 1 {
		t.Fatalf("OnEnd fired %d times, want exactly 1 over both rounds", ended)
	}
	if res.StopReason != "end_turn" {
		t.Fatalf("StopReason = %q, want end_turn", res.StopReason)
	}
}

// A cut that happened while only thinking had been produced sends the
// thinking-only resume prompt as the sole extra message: there is no fragment
// to continue from, and an empty assistant turn is rejected by the protocol.
func TestAnthropicThinkingOnlyLimitContinues(t *testing.T) {
	t.Setenv("FOREBRAIN_LLM_HTTP_DEBUG", "0")

	thinkingOnly := `message_start|{"type":"message_start","message":{"id":"msg_test","type":"message","role":"assistant","model":"claude-opus-4-6","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":1}}}
content_block_start|{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}
content_block_delta|{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Long reasoning that never turned into text"}}
content_block_stop|{"type":"content_block_stop","index":0}
message_delta|{"type":"message_delta","delta":{"stop_reason":"max_tokens","stop_sequence":null},"usage":{"input_tokens":10,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":64}}
message_stop|{"type":"message_stop"}`
	var bodies []map[string]any
	server := anthropicLimitScriptServer(t, []string{
		thinkingOnly,
		anthropicRoundScript("Brief answer.", "end_turn", 30, 5),
	}, &bodies)
	defer server.Close()

	client := NewAgentLLM("test-key", server.URL, "claude-opus-4-6", 64_000)
	res, err := client.Execute(context.Background(), []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if len(bodies) != 2 {
		t.Fatalf("requests = %d, want 2", len(bodies))
	}
	msgs, _ := bodies[1]["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("second request carries %d messages, want 2 (the caller's user message and the prompt)", len(msgs))
	}
	for i, m := range msgs {
		msg, _ := m.(map[string]any)
		if msg["role"] == "assistant" {
			t.Fatalf("message %d of the second request is an assistant message; the thinking-only resume must carry none", i)
		}
	}
	last, _ := msgs[len(msgs)-1].(map[string]any)
	if !strings.Contains(anthropicMessageText(t, last), "reason much more briefly") {
		t.Fatalf("resume prompt = %.80q, want the thinking-only wording", anthropicMessageText(t, last))
	}
	if text := res.Message.TextContent(); text != "Brief answer." {
		t.Fatalf("answer = %q, want only the second round's text", text)
	}
}

// Three continuations, then the fragment is handed back with the output-limit
// stop reason. Every round's request carries exactly one assistant message —
// the accumulated answer, replaced per round, never stacked.
func TestAnthropicGivesUpAfterThree(t *testing.T) {
	t.Setenv("FOREBRAIN_LLM_HTTP_DEBUG", "0")

	scripts := []string{
		anthropicRoundScript("Part one.", "max_tokens", 10, 50),
		anthropicRoundScript("Part two.", "max_tokens", 20, 50),
		anthropicRoundScript("Part three.", "max_tokens", 30, 50),
		anthropicRoundScript("Part four.", "max_tokens", 40, 50),
	}
	var bodies []map[string]any
	server := anthropicLimitScriptServer(t, scripts, &bodies)
	defer server.Close()

	client := NewAgentLLM("test-key", server.URL, "claude-opus-4-6", 64_000)
	res, err := client.Execute(context.Background(), []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if len(bodies) != 4 {
		t.Fatalf("requests = %d, want 4 (first round plus three continuations)", len(bodies))
	}
	if res.StopReason != llm.StopReasonOutputLimit {
		t.Fatalf("StopReason = %q, want %q", res.StopReason, llm.StopReasonOutputLimit)
	}
	msgs, _ := bodies[3]["messages"].([]any)
	assistant := 0
	for _, m := range msgs {
		msg, _ := m.(map[string]any)
		if msg["role"] == "assistant" {
			assistant++
		}
	}
	if assistant != 1 {
		t.Fatalf("fourth request carries %d assistant messages, want exactly 1 (the accumulated answer)", assistant)
	}
	if text := res.Message.TextContent(); text != "Part one.Part two.Part three.Part four." {
		t.Fatalf("joined text = %q", text)
	}
}

// The continuation loop wraps the non-streaming switch too: executeUnary is a
// round of it like executeStreaming.
func TestAnthropicNonStreamingContinues(t *testing.T) {
	t.Setenv("FOREBRAIN_LLM_HTTP_DEBUG", "0")

	responses := []string{
		`{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-4-6","content":[{"type":"text","text":"Half "}],"stop_reason":"max_tokens","stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":5,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}`,
		`{"id":"msg_2","type":"message","role":"assistant","model":"claude-opus-4-6","content":[{"type":"text","text":"there."}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":20,"output_tokens":3,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}`,
	}
	var bodies []map[string]any
	var mu sync.Mutex
	next := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		i := next
		next++
		bodies = append(bodies, body)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(responses[i]))
	}))
	defer server.Close()

	client := NewAgentLLM("test-key", server.URL, "claude-opus-4-6", 64_000)
	ctx := llm.WithNonStreamingRequest(context.Background())
	res, err := client.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if len(bodies) != 2 {
		t.Fatalf("requests = %d, want 2", len(bodies))
	}
	if text := res.Message.TextContent(); text != "Half there." {
		t.Fatalf("joined text = %q", text)
	}
}

// anthropicMessageText joins the text blocks of one Messages-API message body.
func anthropicMessageText(t *testing.T, msg map[string]any) string {
	t.Helper()
	blocks, _ := msg["content"].([]any)
	var b strings.Builder
	for _, blk := range blocks {
		block, _ := blk.(map[string]any)
		if block["type"] == "text" {
			text, _ := block["text"].(string)
			b.WriteString(text)
		}
	}
	return b.String()
}
