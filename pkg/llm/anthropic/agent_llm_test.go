package anthropic

import (
	"context"
	"encoding/json"
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
