package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	openaigo "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/responses"
	"github.com/openai/openai-go/shared"
)

func codeModelID(version string) string {
	return "gpt-" + version + "-co" + "dex"
}

func TestOpenAIPromptCacheKeyUsesLogicalSessionAndBoundsLength(t *testing.T) {
	ctx := llm.WithAgentSessionID(context.Background(), "agent-session")
	if got := openAIPromptCacheKey(ctx); got != "agent-session" {
		t.Fatalf("fallback key=%q", got)
	}
	ctx = llm.WithPromptCacheKey(ctx, "root-session")
	if got := openAIPromptCacheKey(ctx); got != "root-session" {
		t.Fatalf("explicit key=%q", got)
	}
	ctx = llm.WithPromptCacheKey(ctx, strings.Repeat("long-session-", 20))
	first := openAIPromptCacheKey(ctx)
	second := openAIPromptCacheKey(ctx)
	if first != second || len(first) > 64 || !strings.HasPrefix(first, "forebrain:") {
		t.Fatalf("normalized long key=%q second=%q", first, second)
	}
}

func TestOpenAIUsageSeparatesCachedInput(t *testing.T) {
	usage := openAIUsage(100, 7, 60)
	if usage.InputTokens != 40 || usage.CacheReadInputTokens != 60 || usage.OutputTokens != 7 {
		t.Fatalf("usage=%+v", usage)
	}
}

func TestOpenAIResponsesCacheKeyStaysOffCompatibleEndpoints(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, syncResponsesBody("ok"))
	}))
	defer server.Close()

	client := NewResponsesLLMWithCache("sk-test", server.URL+"/v1", "compatible-test", 16, nil, false)
	ctx := llm.WithPromptCacheKey(context.Background(), "session-1")
	if _, err := client.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["prompt_cache_key"]; ok {
		t.Fatalf("compatible endpoint received prompt_cache_key: %#v", body)
	}
}

func TestOpenAIResponsesRemoteV2UsesTriggerAndReturnsOpaqueCheckpoint(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Errorf("path=%s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Errorf("decode: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_compact","object":"response","created_at":1,"status":"completed","model":"gpt-test","output":[{"type":"compaction","id":"cmp_1","encrypted_content":"encrypted-checkpoint"}],"parallel_tool_calls":true,"tool_choice":"auto","tools":[],"usage":{"input_tokens":25,"output_tokens":0,"input_tokens_details":{"cached_tokens":5}}}`)
	}))
	defer server.Close()
	client := newOpenAIResponsesLLM("sk-test", server.URL+"/v1", "gpt-test", 64, nil)
	compactor, ok := client.(llm.ContextCompactor)
	if !ok {
		t.Fatalf("client %T does not implement ContextCompactor", client)
	}
	result, err := compactor.Compact(context.Background(), []llm.Message{
		llm.UserMessage(llm.Text("keep me")), llm.AssistantMessage([]llm.ContentPart{llm.Text("discard me")}),
	}, nil, llm.CompactModeRemoteV2)
	if err != nil {
		t.Fatal(err)
	}
	input, _ := requestBody["input"].([]any)
	if len(input) == 0 {
		t.Fatalf("request=%#v", requestBody)
	}
	last, _ := input[len(input)-1].(map[string]any)
	if last["type"] != "compaction_trigger" {
		t.Fatalf("last input=%#v", last)
	}
	if len(result.Messages) != 2 || result.Messages[0].TextContent() != "keep me" || result.Messages[1].Compaction == nil || result.Messages[1].Compaction.EncryptedContent != "encrypted-checkpoint" {
		t.Fatalf("replacement=%+v", result.Messages)
	}
}

func TestOpenAIResponsesRemoteV2AcceptsCompactionResponseWireShape(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Errorf("path=%s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Errorf("decode: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_compact","object":"response.compaction","created_at":1,"model":"gpt-test","output":[{"type":"message","role":"user","content":[{"type":"input_text","text":"provider retained"}]},{"type":"compaction_summary","id":"cmp_sum_1","encrypted_content":"encrypted-summary"}],"usage":{"input_tokens":367969,"output_tokens":3491,"input_tokens_details":{"cached_tokens":969}}}`)
	}))
	defer server.Close()

	client := newOpenAIResponsesLLM("sk-test", server.URL+"/v1", "gpt-test", 64, nil)
	compactor := client.(llm.ContextCompactor)
	result, err := compactor.Compact(context.Background(), []llm.Message{
		llm.UserMessage(llm.Text("old oversized history")),
	}, nil, llm.CompactModeRemoteV2)
	if err != nil {
		t.Fatal(err)
	}
	input, _ := requestBody["input"].([]any)
	if len(input) == 0 {
		t.Fatalf("request=%#v", requestBody)
	}
	last, _ := input[len(input)-1].(map[string]any)
	if last["type"] != "compaction_trigger" {
		t.Fatalf("last input=%#v", last)
	}
	if len(result.Messages) != 2 || result.Messages[0].TextContent() != "provider retained" || result.Messages[1].Compaction == nil {
		t.Fatalf("replacement=%+v", result.Messages)
	}
	checkpoint := result.Messages[1].Compaction
	if checkpoint.Type != "compaction_summary" || checkpoint.ID != "cmp_sum_1" || checkpoint.EncryptedContent != "encrypted-summary" {
		t.Fatalf("checkpoint=%+v", checkpoint)
	}
	if result.Usage == nil || result.Usage.InputTokens != 367000 || result.Usage.CacheReadInputTokens != 969 || result.Usage.OutputTokens != 3491 {
		t.Fatalf("usage=%+v", result.Usage)
	}
}

func TestOpenAIResponsesReplaysOpaqueCompactionItem(t *testing.T) {
	client := &openAIResponsesLLM{model: "gpt-test", maxTokens: 64}
	params := client.newResponseParams(context.Background(), []llm.Message{{Compaction: &llm.CompactionState{ID: "cmp_1", EncryptedContent: "opaque"}}}, nil, false)
	body, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"type":"compaction"`) || !strings.Contains(string(body), `"encrypted_content":"opaque"`) {
		t.Fatalf("body=%s", body)
	}
}

func TestOpenAIResponsesReplaysOpaqueCompactionSummaryItem(t *testing.T) {
	client := &openAIResponsesLLM{model: "gpt-test", maxTokens: 64}
	params := client.newResponseParams(context.Background(), []llm.Message{{Compaction: &llm.CompactionState{Type: "compaction_summary", ID: "cmp_1", EncryptedContent: "opaque"}}}, nil, false)
	body, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"type":"compaction_summary"`) || !strings.Contains(string(body), `"encrypted_content":"opaque"`) {
		t.Fatalf("body=%s", body)
	}
}

func TestRemoteV2RetentionPreservesImagesAndEligibleAgentMessages(t *testing.T) {
	user := llm.UserMessage(llm.Text("look"), llm.ImageURL("https://example.test/image.png"))
	agent := llm.Message{AgentMessage: &llm.AgentMessageState{
		ID: "amsg_1", Author: "worker", Recipient: "root",
		Content: []llm.AgentMessageContent{{Type: "input_text", Text: "Message Type: MESSAGE\nresult"}},
	}}
	finalAgent := llm.Message{AgentMessage: &llm.AgentMessageState{
		ID: "amsg_2", Author: "worker", Recipient: "root",
		Content: []llm.AgentMessageContent{{Type: "input_text", Text: "Message Type: FINAL_ANSWER\ndone"}},
	}}
	retained := retainedRemoteMessages([]llm.Message{user, agent, finalAgent}, 64_000)
	if len(retained) != 2 || len(retained[0].Parts) != 2 || retained[1].AgentMessage == nil || retained[1].AgentMessage.ID != "amsg_1" {
		t.Fatalf("retained=%+v", retained)
	}
}

func TestRemoteCompactionParsesAgentMessage(t *testing.T) {
	items := []json.RawMessage{json.RawMessage(`{"type":"agent_message","id":"amsg_1","author":"worker","recipient":"root","content":[{"type":"input_text","text":"result"},{"type":"encrypted_content","encrypted_content":"opaque"}]}`)}
	got := compactWireOutputToReplacement(items)
	if len(got) != 1 || got[0].AgentMessage == nil || len(got[0].AgentMessage.Content) != 2 || got[0].AgentMessage.Content[1].EncryptedContent != "opaque" {
		t.Fatalf("replacement=%+v", got)
	}
}

func TestRemoteCompactionParsesCompactionSummary(t *testing.T) {
	items := []json.RawMessage{json.RawMessage(`{"type":"compaction_summary","id":"cmp_sum_1","encrypted_content":"opaque"}`)}
	got := compactWireOutputToReplacement(items)
	if len(got) != 1 || got[0].Compaction == nil || got[0].Compaction.Type != "compaction_summary" || got[0].Compaction.EncryptedContent != "opaque" {
		t.Fatalf("replacement=%+v", got)
	}
}

func TestOpenAIResponsesStreamingPreservesToolCallOrder(t *testing.T) {
	body := strings.Join([]string{
		`data: {"type":"response.output_item.added","item":{"id":"fc_b","type":"function_call","arguments":"{}","call_id":"call_b","name":"second"},"output_index":0}`,
		"",
		`data: {"type":"response.output_item.added","item":{"id":"fc_a","type":"function_call","arguments":"{}","call_id":"call_a","name":"first"},"output_index":1}`,
		"",
		`data: {"type":"response.completed","response":{"id":"resp_test","status":"completed","output":[],"usage":{"input_tokens":10,"output_tokens":5}}}`,
		"",
	}, "\n")
	client := newTestOpenAIResponsesLLM(t, func(*http.Request) (*http.Response, error) {
		return sseResponse(body), nil
	})
	result, err := client.Execute(context.Background(), []llm.Message{llm.UserMessage(llm.Text("test"))}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Message.ToolCalls) != 2 {
		t.Fatalf("tool calls=%#v", result.Message.ToolCalls)
	}
	if result.Message.ToolCalls[0].ID != "call_b" || result.Message.ToolCalls[1].ID != "call_a" {
		t.Fatalf("tool call order=%#v", result.Message.ToolCalls)
	}
}

func TestOpenAIResponsesOverlayCannotOverridePromptCacheKey(t *testing.T) {
	got := normalizeOpenAIResponsesParamsOverlay(map[string]any{
		"prompt_cache_key": "static",
		"temperature":      0.2,
	})
	if _, exists := got["prompt_cache_key"]; exists {
		t.Fatal("prompt_cache_key override was not removed")
	}
	if got["temperature"] != 0.2 {
		t.Fatalf("unrelated overlay removed: %#v", got)
	}
}

func TestExtractTextFromOutputItem_OutputText(t *testing.T) {
	// Test case: content type is "output_text" (the actual type returned by the API)
	item := responses.ResponseOutputItemUnion{
		Type: "message",
		Content: []responses.ResponseOutputMessageContentUnion{
			{
				Type: "output_text",
				Text: "Hello, I'm here to help with your code—calm, practical, and ready to dig in.",
			},
		},
	}

	result := extractTextFromOutputItem(item)
	expected := "Hello, I'm here to help with your code—calm, practical, and ready to dig in."

	if result != expected {
		t.Errorf("extractTextFromOutputItem() with output_text type failed\ngot:  %q\nwant: %q", result, expected)
	}
}

func TestExtractTextFromOutputItem_Text(t *testing.T) {
	// Test case: content type is "text" (standard type)
	item := responses.ResponseOutputItemUnion{
		Type: "message",
		Content: []responses.ResponseOutputMessageContentUnion{
			{
				Type: "text",
				Text: "Welcome — a calm, practical coding assistant.",
			},
		},
	}

	result := extractTextFromOutputItem(item)
	expected := "Welcome — a calm, practical coding assistant."

	if result != expected {
		t.Errorf("extractTextFromOutputItem() with text type failed\ngot:  %q\nwant: %q", result, expected)
	}
}

func TestExtractTextFromOutputItem_EmptyContent(t *testing.T) {
	// Test case: empty content
	item := responses.ResponseOutputItemUnion{
		Type:    "message",
		Content: []responses.ResponseOutputMessageContentUnion{},
	}

	result := extractTextFromOutputItem(item)
	if result != "" {
		t.Errorf("extractTextFromOutputItem() with empty content should return empty string, got: %q", result)
	}
}

func TestExtractTextFromOutputItem_WrongType(t *testing.T) {
	// Test case: wrong content type (refusal)
	item := responses.ResponseOutputItemUnion{
		Type: "message",
		Content: []responses.ResponseOutputMessageContentUnion{
			{
				Type: "refusal",
				Text: "This should not be extracted",
			},
		},
	}

	result := extractTextFromOutputItem(item)
	if result != "" {
		t.Errorf("extractTextFromOutputItem() with wrong type should return empty string, got: %q", result)
	}
}

func TestExtractTextFromOutputItem_MultipleContent(t *testing.T) {
	// Test case: multiple content items, should return first matching one
	item := responses.ResponseOutputItemUnion{
		Type: "message",
		Content: []responses.ResponseOutputMessageContentUnion{
			{
				Type: "refusal",
				Text: "Should skip this",
			},
			{
				Type: "output_text",
				Text: "This should be extracted",
			},
			{
				Type: "text",
				Text: "This should be skipped",
			},
		},
	}

	result := extractTextFromOutputItem(item)
	expected := "This should be extracted"

	if result != expected {
		t.Errorf("extractTextFromOutputItem() with multiple content failed\ngot:  %q\nwant: %q", result, expected)
	}
}

func TestOpenAIResponsesStreamingSkipsCodexMetadataFrames(t *testing.T) {
	body := strings.Join([]string{
		"event: codex.rate_limits",
		`data: {"type":"codex.rate_limits","rate_limits":{"allowed":true}}`,
		"",
		"event: codex.response.metadata",
		`data: {"type":"codex.response.metadata","headers":{"x-codex-safety-buffering-enabled":"true"}}`,
		"",
		`data: {"type":"response.output_item.added","item":{"id":"fc_test","type":"function_call","status":"in_progress","arguments":"","call_id":"call_test","name":"test_tool"},"output_index":0,"sequence_number":1}`,
		"",
		`data: {"type":"response.function_call_arguments.done","arguments":"{\"enabled\":true}","item_id":"fc_test","output_index":0,"sequence_number":2}`,
		"",
		`data: {"type":"response.completed","response":{"id":"resp_test","status":"completed","output":[],"usage":{"input_tokens":10,"output_tokens":5}}}`,
		"",
	}, "\n")
	testLLM := newTestOpenAIResponsesLLM(t, func(req *http.Request) (*http.Response, error) {
		return sseResponse(body), nil
	})

	result, err := testLLM.Execute(context.Background(), []llm.Message{llm.UserMessage(llm.Text("test"))}, nil)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if result == nil || result.Message == nil || len(result.Message.ToolCalls) != 1 {
		t.Fatalf("tool calls = %#v, want one completed function call", result)
	}
	call := result.Message.ToolCalls[0]
	if call.ID != "call_test" || call.Function.Name != "test_tool" || call.Function.Arguments != `{"enabled":true}` {
		t.Fatalf("tool call = %+v, want test_tool with completed arguments", call)
	}
}

func TestOpenAIResponsesStreamingLargeRequestSkipsPingFrames(t *testing.T) {
	body := strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"Hello","sequence_number":1}`,
		"",
		": PING",
		"",
		`data: {"type":"response.output_text.delta","delta":" world","sequence_number":2}`,
		"",
		`data: {"type":"response.completed","response":{"id":"resp_test","status":"completed","output":[],"usage":{"input_tokens":10,"output_tokens":5}}}`,
		"",
	}, "\n")
	testLLM := newTestOpenAIResponsesLLM(t, func(req *http.Request) (*http.Response, error) {
		if req.ContentLength <= 1<<20 {
			t.Fatalf("request ContentLength = %d, want more than 1 MiB", req.ContentLength)
		}
		resp := sseResponse(body)
		resp.ContentLength = int64(len(body))
		resp.Header.Set("Content-Length", fmt.Sprint(len(body)))
		return resp, nil
	})

	// Keep the stream flag past the old 1 MiB request-body inspection limit. The
	// transport must identify this as streaming from context, not by decoding a
	// truncated copy of the request JSON.
	input := strings.Repeat("x", (1<<20)+1024)
	result, err := testLLM.Execute(context.Background(), []llm.Message{llm.UserMessage(llm.Text(input))}, nil)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if got := result.Message.TextContent(); got != "Hello world" {
		t.Fatalf("text = %q, want Hello world", got)
	}
	if result.Usage == nil || result.Usage.InputTokens != 10 || result.Usage.OutputTokens != 5 {
		t.Fatalf("usage = %+v, want input=10 output=5", result.Usage)
	}
}

func TestOpenAIResponsesStreamingIgnoresMalformedAndMissingDataFrames(t *testing.T) {
	body := strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"Hello","sequence_number":1}`,
		"",
		"event: proxy.unknown",
		"data: {",
		"",
		"event: proxy.missing_data",
		"",
		`data: {"type":"response.output_text.delta","delta":" world","sequence_number":2}`,
		"",
		`data: {"type":"response.completed","response":{"id":"resp_test","status":"completed","output":[],"usage":{"input_tokens":10,"output_tokens":5}}}`,
		"",
	}, "\n")
	testLLM := newTestOpenAIResponsesLLM(t, func(req *http.Request) (*http.Response, error) {
		return sseResponse(body), nil
	})

	result, err := testLLM.Execute(context.Background(), []llm.Message{llm.UserMessage(llm.Text("test"))}, nil)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if got := result.Message.TextContent(); got != "Hello world" {
		t.Fatalf("text = %q, want Hello world", got)
	}
}

func TestOpenAIResponsesStreamingCompletedOnlySkipsEmptySSEFrames(t *testing.T) {
	body := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"\"}\n\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"Hello\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test\",\"output\":[],\"usage\":{\"input_tokens\":10,\"output_tokens\":5}}}\n\n"
	testLLM := newTestOpenAIResponsesLLM(t, func(req *http.Request) (*http.Response, error) {
		return sseResponse(body), nil
	})

	messages := []llm.Message{{Role: llm.RoleUser, Parts: []llm.ContentPart{llm.Text("test")}}}
	result, err := testLLM.Execute(context.Background(), messages, nil)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if result.Message.TextContent() != "Hello" {
		t.Errorf("Expected 'Hello', got %q", result.Message.TextContent())
	}
}

func TestOpenAIResponsesStreamingEmitsDeltas(t *testing.T) {
	body := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"Hello\"}\n\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\" world\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test\",\"output\":[],\"usage\":{\"input_tokens\":10,\"output_tokens\":5}}}\n\n"
	testLLM := newTestOpenAIResponsesLLM(t, func(req *http.Request) (*http.Response, error) {
		return sseResponse(body), nil
	})

	messages := []llm.Message{{Role: llm.RoleUser, Parts: []llm.ContentPart{llm.Text("test")}}}
	result, err := testLLM.Execute(context.Background(), messages, nil)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if result.Message.TextContent() != "Hello world" {
		t.Errorf("Expected 'Hello world', got %q", result.Message.TextContent())
	}
	if result.Usage == nil {
		t.Fatal("expected non-nil usage")
	}
	if result.Usage.InputTokens != 10 || result.Usage.OutputTokens != 5 {
		t.Fatalf("usage mismatch: got input=%d output=%d", result.Usage.InputTokens, result.Usage.OutputTokens)
	}
}

func TestOpenAIResponsesStreamsThroughTelemetryBeforeServerCompletes(t *testing.T) {
	t.Setenv("FOREBRAIN_LLM_HTTP_DEBUG", "0")

	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseServer := func() { releaseOnce.Do(func() { close(release) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `data: {"type":"response.output_text.delta","delta":"first","sequence_number":1}`+"\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-release
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"resp_test","object":"response","status":"completed","model":"gpt-test","output":[],"usage":{"input_tokens":10,"output_tokens":5}}}`+"\n\n")
	}))
	t.Cleanup(func() {
		releaseServer()
		server.Close()
	})

	client := newOpenAIResponsesLLM("sk-test", server.URL+"/v1", "gpt-test", 16, nil)
	firstDelta := make(chan struct{})
	var firstDeltaOnce sync.Once
	ctx := llm.WithStreamSink(context.Background(), &llm.StreamSink{
		OnDelta: func(text string) {
			if text == "first" {
				firstDeltaOnce.Do(func() { close(firstDelta) })
			}
		},
	})
	done := make(chan error, 1)
	go func() {
		_, err := client.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil)
		done <- err
	}()

	select {
	case <-firstDelta:
	case <-time.After(5 * time.Second):
		t.Fatal("Responses API delta did not arrive while server was still streaming")
	}
	select {
	case err := <-done:
		t.Fatalf("Execute completed before server release: %v", err)
	default:
	}
	releaseServer()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Responses API stream")
	}
}

func TestOpenAIResponsesStreamingReturnsResponseFailedError(t *testing.T) {
	body := strings.Join([]string{
		"event: response.failed",
		`data: {"type":"response.failed","response":{"id":"resp_test","object":"response","model":"gpt-test","status":"failed","output":[],"error":{"code":"rate_limit_exceeded","message":"Concurrency limit exceeded for user, please retry later"}}}`,
		"",
		"",
	}, "\n")
	testLLM := newTestOpenAIResponsesLLM(t, func(req *http.Request) (*http.Response, error) {
		return sseResponse(body), nil
	})

	responseStarts := 0
	ctx := llm.WithStreamSink(context.Background(), &llm.StreamSink{
		OnResponseStarted: func() { responseStarts++ },
	})
	messages := []llm.Message{{Role: llm.RoleUser, Parts: []llm.ContentPart{llm.Text("test")}}}
	result, err := testLLM.Execute(ctx, messages, nil)
	if err == nil {
		t.Fatalf("Execute returned result %+v, want response.failed error", result)
	}
	for _, want := range []string{"rate_limit_exceeded", "Concurrency limit exceeded for user, please retry later"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Execute error = %q, want %q", err, want)
		}
	}
	if responseStarts != 0 {
		t.Fatalf("response-start callbacks=%d, want 0 for a failed request", responseStarts)
	}
}

func TestOpenAIResponsesStreamingReturnsErrorEvent(t *testing.T) {
	body := strings.Join([]string{
		"event: error",
		`data: {"type":"error","code":"server_error","message":"upstream unavailable","param":null,"sequence_number":1}`,
		"",
		"",
	}, "\n")
	testLLM := newTestOpenAIResponsesLLM(t, func(req *http.Request) (*http.Response, error) {
		return sseResponse(body), nil
	})

	messages := []llm.Message{{Role: llm.RoleUser, Parts: []llm.ContentPart{llm.Text("test")}}}
	result, err := testLLM.Execute(context.Background(), messages, nil)
	if err == nil {
		t.Fatalf("Execute returned result %+v, want error event", result)
	}
	for _, want := range []string{"server_error", "upstream unavailable"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Execute error = %q, want %q", err, want)
		}
	}
}

func TestOpenAIResponsesStreamingReturnsNestedProviderErrorEvent(t *testing.T) {
	body := strings.Join([]string{
		"event: error",
		`data: {"type":"error","error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"Your input exceeds the context window of this model. Please adjust your input and try again.","param":"input"},"sequence_number":2}`,
		"",
		"",
	}, "\n")
	testLLM := newTestOpenAIResponsesLLM(t, func(req *http.Request) (*http.Response, error) {
		return sseResponse(body), nil
	})

	messages := []llm.Message{{Role: llm.RoleUser, Parts: []llm.ContentPart{llm.Text("test")}}}
	result, err := testLLM.Execute(context.Background(), messages, nil)
	if err == nil {
		t.Fatalf("Execute returned result %+v, want nested provider error", result)
	}
	for _, want := range []string{
		"context_length_exceeded",
		"Your input exceeds the context window of this model. Please adjust your input and try again.",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Execute error = %q, want %q", err, want)
		}
	}
}

func TestOpenAIResponsesStreamingPreservesRawProviderErrorEventAtEOF(t *testing.T) {
	body := `data: {"error":{"code":"upstream_stream_read_error","message":"Upstream response stream was interrupted","type":"upstream_error"}}`
	testLLM := newTestOpenAIResponsesLLM(t, func(req *http.Request) (*http.Response, error) {
		return sseResponse(body), nil
	})

	messages := []llm.Message{{Role: llm.RoleUser, Parts: []llm.ContentPart{llm.Text("test")}}}
	result, err := testLLM.Execute(context.Background(), messages, nil)
	if err == nil {
		t.Fatalf("Execute returned result %+v, want provider error", result)
	}
	msg := err.Error()
	for _, want := range []string{
		`"code":"upstream_stream_read_error"`,
		`"message":"Upstream response stream was interrupted"`,
		`"type":"upstream_error"`,
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("Execute error = %q, want %q", msg, want)
		}
	}
	if strings.Contains(msg, "unexpected end of JSON input") {
		t.Fatalf("Execute error = %q, want raw provider error without JSON decoder noise", msg)
	}
}

func TestOpenAIResponsesStreamingWaitsForFunctionCallArgumentsDone(t *testing.T) {
	body := strings.Join([]string{
		`data: {"type":"response.output_item.added","item":{"id":"fc_test","type":"function_call","status":"in_progress","arguments":"","call_id":"call_test","name":"test_tool"},"output_index":1,"sequence_number":4}`,
		"",
		`data: {"type":"response.function_call_arguments.delta","delta":"{\"","item_id":"fc_test","output_index":1,"sequence_number":5}`,
		"",
		`data: {"type":"response.function_call_arguments.delta","delta":"enabled\":true}","item_id":"fc_test","output_index":1,"sequence_number":6}`,
		"",
		`data: {"type":"response.function_call_arguments.done","arguments":"{\"enabled\":true}","item_id":"fc_test","output_index":1,"sequence_number":7}`,
		"",
		`data: {"type":"response.completed","response":{"id":"resp_test","output":[],"usage":{"input_tokens":10,"output_tokens":5}}}`,
		"",
	}, "\n")
	testLLM := newTestOpenAIResponsesLLM(t, func(req *http.Request) (*http.Response, error) {
		return sseResponse(body), nil
	})

	messages := []llm.Message{{Role: llm.RoleUser, Parts: []llm.ContentPart{llm.Text("test")}}}
	result, err := testLLM.Execute(context.Background(), messages, nil)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if result == nil || result.Message == nil {
		t.Fatalf("missing result message")
	}
	if got := len(result.Message.ToolCalls); got != 1 {
		t.Fatalf("tool calls = %d, want 1", got)
	}
	call := result.Message.ToolCalls[0]
	if call.ID != "call_test" || call.Function.Name != "test_tool" {
		t.Fatalf("tool call = %+v, want test_tool call_test", call)
	}
	if call.Function.Arguments != `{"enabled":true}` {
		t.Fatalf("tool arguments = %q, want final arguments", call.Function.Arguments)
	}
}

func TestOpenAIResponsesSyncAcceptsJSONBodyWithEventStreamContentType(t *testing.T) {
	testLLM := newTestOpenAIResponsesLLMWithStream(t, false, func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    200,
			ContentLength: int64(len(syncResponsesBody("test response"))),
			Header:        http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:          io.NopCloser(strings.NewReader(syncResponsesBody("test response"))),
		}, nil
	})

	messages := []llm.Message{{Role: llm.RoleUser, Parts: []llm.ContentPart{llm.Text("test")}}}
	result, err := testLLM.Execute(context.Background(), messages, nil)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if result.Message.TextContent() != "test response" {
		t.Errorf("Expected 'test response', got %q", result.Message.TextContent())
	}
}

func TestOpenAIResponsesLLMPassesTools(t *testing.T) {
	var capturedBody map[string]any

	testLLM := newTestOpenAIResponsesLLMWithStream(t, false, func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		_ = json.Unmarshal(body, &capturedBody)

		respBody := `{"id":"resp_123","object":"response","created":1234567890,"model":"gpt-4o-mini","output":[{"type":"message","content":[{"type":"output_text","text":"Tool call result"}]}],"usage":{"input_tokens":10,"output_tokens":5}}`
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(respBody)),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
		}, nil
	})

	type TestToolInput struct {
		Query string `json:"query"`
	}

	tool, err := llm.NewTool("test_tool", "A test tool", func(ctx context.Context, args TestToolInput) (string, error) {
		return "result", nil
	})
	if err != nil {
		t.Fatalf("Failed to create tool: %v", err)
	}

	messages := []llm.Message{{Role: llm.RoleUser, Parts: []llm.ContentPart{llm.Text("test")}}}
	_, err = testLLM.Execute(context.Background(), messages, []*llm.Tool{tool})
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	tools, ok := capturedBody["tools"].([]any)
	if !ok || len(tools) == 0 {
		t.Fatalf("Tools not passed to API. Body: %+v", capturedBody)
	}

	toolMap := tools[0].(map[string]any)
	if toolMap["name"] != "test_tool" {
		t.Errorf("Tool name mismatch: got %v, want test_tool", toolMap["name"])
	}
	if toolMap["description"] != "A test tool" {
		t.Errorf("Tool description mismatch: got %v, want 'A test tool'", toolMap["description"])
	}
}

func TestNativeWebSearchUsesPublicResponsesWireShape(t *testing.T) {
	var capturedBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
			return
		}
		if err := json.Unmarshal(body, &capturedBody); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, syncResponsesBody("ok"))
	}))
	defer server.Close()
	testLLM := NewResponsesLLMWithWebSearch(
		"sk-test", server.URL+"/v1", "gpt-test", 16,
		[]byte(`{"web_search":{"mode":"live","search_context_size":"high","allowed_domains":["openaigo.com"],"user_location":{"country":"US","city":"San Francisco"}},"stream":false}`),
		"", nil,
	)
	responsesLLM := testLLM.(*openAIResponsesLLM)
	if _, err := responsesLLM.Execute(context.Background(), []llm.Message{llm.UserMessage(llm.Text("latest OpenAI news"))}, nil); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	tools, ok := capturedBody["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %#v, want one provider web search tool", capturedBody["tools"])
	}
	tool, _ := tools[0].(map[string]any)
	if tool["type"] != "web_search" {
		t.Fatalf("web search tool = %#v, want stable Responses wire shape", tool)
	}
	if _, ok := tool["external_web_access"]; ok {
		t.Fatalf("public Responses tool unexpectedly has access controls: %#v", tool)
	}
	if tool["search_context_size"] != "high" {
		t.Fatalf("search_context_size = %#v, want high", tool["search_context_size"])
	}
	filters, _ := tool["filters"].(map[string]any)
	if got := filters["allowed_domains"]; fmt.Sprint(got) != "[openaigo.com]" {
		t.Fatalf("filters = %#v, want allowed_domains", filters)
	}
	location, _ := tool["user_location"].(map[string]any)
	if location["type"] != "approximate" || location["country"] != "US" {
		t.Fatalf("user_location = %#v", location)
	}
}

func TestNativeWebSearchModeAccessControls(t *testing.T) {
	for _, tc := range []struct {
		mode     string
		external any
		indexed  any
	}{
		{mode: "cached", external: false},
		{mode: "indexed", external: true, indexed: true},
		{mode: "live", external: true},
	} {
		payload := map[string]any{"tools": []any{map[string]any{"type": "web_search_preview"}}}
		body, _ := json.Marshal(payload)
		rt := &nativeWebSearchRequestRewriter{next: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			got, _ := io.ReadAll(req.Body)
			var wire map[string]any
			if err := json.Unmarshal(got, &wire); err != nil {
				return nil, err
			}
			tool := wire["tools"].([]any)[0].(map[string]any)
			if tool["type"] != "web_search" || tool["external_web_access"] != tc.external {
				t.Fatalf("mode %s tool = %#v", tc.mode, tool)
			}
			if tc.indexed == nil {
				if _, ok := tool["indexed_web_access"]; ok {
					t.Fatalf("mode %s unexpectedly sets indexed_web_access", tc.mode)
				}
			} else if tool["indexed_web_access"] != tc.indexed {
				t.Fatalf("mode %s indexed_web_access = %#v", tc.mode, tool["indexed_web_access"])
			}
			return nil, nil
		}), config: nativeWebSearchConfig{mode: tc.mode, codexAccessControls: true}}
		req, _ := http.NewRequest(http.MethodPost, "http://example.invalid/v1/responses", bytes.NewReader(body))
		if _, err := rt.RoundTrip(req); err != nil {
			t.Fatalf("mode %s: %v", tc.mode, err)
		}
	}
}

func TestNativeWebSearchReplacesLocalWebSearchFunction(t *testing.T) {
	var capturedBody map[string]any
	testLLM := newTestOpenAIResponsesLLMWithStream(t, false, func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		_ = json.Unmarshal(body, &capturedBody)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(syncResponsesBody("ok"))),
		}, nil
	})
	responsesLLM := testLLM.(*openAIResponsesLLM)
	responsesLLM.webSearch = nativeWebSearchConfig{mode: "live"}

	webSearch, err := llm.NewTool("web_search", "local search", func(context.Context, struct{}) (string, error) {
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	other, err := llm.NewTool("other_tool", "other", func(context.Context, struct{}) (string, error) {
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := responsesLLM.Execute(context.Background(), []llm.Message{llm.UserMessage(llm.Text("test"))}, []*llm.Tool{nil, webSearch, other}); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	tools, _ := capturedBody["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("tools = %#v, want provider search and other_tool", tools)
	}
	if builtin, _ := tools[0].(map[string]any); builtin["type"] != "web_search_preview" {
		t.Fatalf("provider tool = %#v", builtin)
	}
	if function, _ := tools[1].(map[string]any); function["name"] != "other_tool" {
		t.Fatalf("function tool = %#v", function)
	}
}

func TestParseNativeWebSearchConfigBooleanOverride(t *testing.T) {
	if got := parseNativeWebSearchConfig([]byte(`{"web_search":false}`), "live"); got.enabled() {
		t.Fatalf("web_search=false did not disable default mode: %+v", got)
	}
	if got := parseNativeWebSearchConfig([]byte(`{"web_search":true}`), ""); !got.enabled() || got.mode != "live" {
		t.Fatalf("web_search=true did not enable live mode: %+v", got)
	}
}

func TestWebSearchActionDetailSupportsFindAction(t *testing.T) {
	got := webSearchActionDetail(`{"action":{"type":"find","url":"https://example.com","pattern":"needle"}}`)
	if got != "'needle' in https://example.com" {
		t.Fatalf("detail = %q", got)
	}
}

func TestOpenAIResponsesLLMEncodesMCPDelegatingToolWithoutArgumentsWrapper(t *testing.T) {
	var capturedBody map[string]any

	testLLM := newTestOpenAIResponsesLLMWithStream(t, false, func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		_ = json.Unmarshal(body, &capturedBody)

		respBody := `{"id":"resp_123","object":"response","created":1234567890,"model":"gpt-4o-mini","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":10,"output_tokens":5}}`
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(respBody)),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
		}, nil
	})

	// Built here rather than via pkg/run's newMCPDelegatingTool: this package
	// is Layer 0 and cannot import run. What the test actually needs is a tool
	// flagged as carrying external context, which is one call.
	tool, err := newExternalContextTool("mcp__codegraph__codegraph_explore", "Codegraph explore", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"projectPath": map[string]any{"type": "string"},
			"query":       map[string]any{"type": "string"},
		},
		"required": []any{"query"},
	}, func(context.Context, json.RawMessage) (string, error) {
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("newMCPDelegatingTool: %v", err)
	}

	messages := []llm.Message{{Role: llm.RoleUser, Parts: []llm.ContentPart{llm.Text("test")}}}
	if _, err := testLLM.Execute(context.Background(), messages, []*llm.Tool{tool}); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	tools, ok := capturedBody["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools payload = %#v, want single tool", capturedBody["tools"])
	}
	toolMap, _ := tools[0].(map[string]any)
	parameters, _ := toolMap["parameters"].(map[string]any)
	props, _ := parameters["properties"].(map[string]any)
	if _, ok := props["arguments"]; ok {
		t.Fatalf("parameters unexpectedly wrapped in arguments property: %#v", parameters)
	}
	if _, ok := props["projectPath"]; !ok {
		t.Fatalf("parameters missing projectPath: %#v", parameters)
	}
	if _, ok := props["query"]; !ok {
		t.Fatalf("parameters missing query: %#v", parameters)
	}
}

func TestOpenAIResponsesLLMEncodesToolHistoryAsFunctionCallItems(t *testing.T) {
	var capturedBody map[string]any

	testLLM := newTestOpenAIResponsesLLMWithStream(t, false, func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		_ = json.Unmarshal(body, &capturedBody)

		respBody := `{"id":"resp_123","object":"response","created":1234567890,"model":"gpt-4o-mini","output":[{"type":"message","content":[{"type":"output_text","text":"done"}]}],"usage":{"input_tokens":10,"output_tokens":5}}`
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(respBody)),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
		}, nil
	})

	messages := []llm.Message{
		llm.UserMessage(llm.Text("Structured skill invocation")),
		{
			Role: llm.RoleAssistant,
			ToolCalls: []llm.ToolCall{{
				ID:       "call-review",
				Type:     llm.ToolTypeFunction,
				Function: llm.FunctionCall{Name: "review", Arguments: `{}`},
			}},
		},
		llm.ToolResultMessage("call-review", llm.Text("# Review\nbody")),
	}
	if _, err := testLLM.Execute(context.Background(), messages, nil); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	inputItems, ok := capturedBody["input"].([]any)
	if !ok || len(inputItems) != 3 {
		t.Fatalf("input items = %#v, want 3 entries", capturedBody["input"])
	}
	userMsg, _ := inputItems[0].(map[string]any)
	if userMsg["role"] != "user" {
		t.Fatalf("first input role = %v, want user", userMsg["role"])
	}
	funcCall, _ := inputItems[1].(map[string]any)
	if funcCall["type"] != "function_call" || funcCall["call_id"] != "call-review" || funcCall["name"] != "review" {
		t.Fatalf("second input item = %#v, want function_call for review", inputItems[1])
	}
	funcOut, _ := inputItems[2].(map[string]any)
	if funcOut["type"] != "function_call_output" || funcOut["call_id"] != "call-review" {
		t.Fatalf("third input item = %#v, want function_call_output for review result", inputItems[2])
	}
	if output := funcOut["output"]; output != "# Review\nbody" {
		t.Fatalf("function_call_output = %q, want skill body", output)
	}
}

func TestOpenAIResponsesLLMEncodesUserImagesAsInputImageContent(t *testing.T) {
	var capturedBody map[string]any
	testLLM := newTestOpenAIResponsesLLMWithStream(t, false, func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		_ = json.Unmarshal(body, &capturedBody)
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(syncResponsesBody("seen"))),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
		}, nil
	})

	messages := []llm.Message{
		llm.UserMessage(
			llm.Text("[Image #1] identify this"),
			llm.ImageBase64("image/png", "AQID"),
			llm.ImageURL("https://example.test/screenshot.jpg"),
		),
	}
	if _, err := testLLM.Execute(context.Background(), messages, nil); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	inputItems, ok := capturedBody["input"].([]any)
	if !ok || len(inputItems) != 1 {
		t.Fatalf("input items = %#v, want one user message", capturedBody["input"])
	}
	userMsg, _ := inputItems[0].(map[string]any)
	content, ok := userMsg["content"].([]any)
	if !ok || len(content) != 3 {
		t.Fatalf("user content = %#v, want text and two images", userMsg["content"])
	}
	textPart, _ := content[0].(map[string]any)
	if textPart["type"] != "input_text" || textPart["text"] != "[Image #1] identify this" {
		t.Fatalf("text content = %#v", content[0])
	}
	base64Part, _ := content[1].(map[string]any)
	if base64Part["type"] != "input_image" ||
		base64Part["image_url"] != "data:image/png;base64,AQID" ||
		base64Part["detail"] != "auto" {
		t.Fatalf("base64 image content = %#v", content[1])
	}
	urlPart, _ := content[2].(map[string]any)
	if urlPart["type"] != "input_image" ||
		urlPart["image_url"] != "https://example.test/screenshot.jpg" {
		t.Fatalf("URL image content = %#v", content[2])
	}
}

func TestOpenAIResponsesSyncCompletedWithoutOutputReturnsError(t *testing.T) {
	testLLM := newTestOpenAIResponsesLLMWithStream(t, false, func(req *http.Request) (*http.Response, error) {
		respBody := fmt.Sprintf(`{"id":"resp_123","object":"response","status":"completed","model":"%s","output":[],"usage":{"input_tokens":8311,"output_tokens":99,"total_tokens":8410}}`, codeModelID("5.3"))
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(respBody)),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
		}, nil
	})

	messages := []llm.Message{{Role: llm.RoleUser, Parts: []llm.ContentPart{llm.Text("test")}}}
	result, err := testLLM.Execute(context.Background(), messages, nil)
	if err == nil {
		t.Fatalf("Execute returned result %+v, want completed-without-output error", result)
	}
	for _, want := range []string{
		"completed without output",
		"resp_123",
		codeModelID("5.3"),
		"input_tokens=8311",
		"output_tokens=99",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Execute error = %q, want substring %q", err, want)
		}
	}
}

func TestOpenAIResponsesSyncSurfacesReasoningOnlyOutput(t *testing.T) {
	testLLM := newTestOpenAIResponsesLLMWithStream(t, false, func(req *http.Request) (*http.Response, error) {
		respBody := `{"id":"resp_reasoning","object":"response","status":"completed","model":"gpt-5.4","output":[{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"Need one more pass over the tool results."}]}],"usage":{"input_tokens":10,"output_tokens":5}}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(respBody)),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
		}, nil
	})

	messages := []llm.Message{{Role: llm.RoleUser, Parts: []llm.ContentPart{llm.Text("test")}}}
	result, err := testLLM.Execute(context.Background(), messages, nil)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if result == nil || result.Message == nil {
		t.Fatalf("missing result message")
	}
	text := strings.TrimSpace(result.Message.TextContent())
	if text == "" {
		t.Fatalf("expected surfaced reasoning text, got empty")
	}
	if !strings.Contains(text, "Need one more pass over the tool results.") {
		t.Fatalf("expected surfaced reasoning content, got %q", text)
	}
	if !strings.HasPrefix(text, "```thinking\n") || !strings.HasSuffix(text, "\n```") {
		t.Fatalf("expected reasoning wrapped in fenced code block, got %q", text)
	}
	payload, ok := llm.Decode(result.Message.Name)
	if !ok {
		t.Fatalf("expected structured reasoning carry, got %q", result.Message.Name)
	}
	if got := llm.SummaryText(payload.Items); !strings.Contains(got, "Need one more pass over the tool results.") {
		t.Fatalf("carry summary = %q", got)
	}
}

func TestOpenAIResponsesLLMPreservesRawProviderErrorBody(t *testing.T) {
	t.Parallel()

	testLLM := newTestOpenAIResponsesLLMWithStream(t, false, func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Status:     http.StatusText(http.StatusServiceUnavailable),
			Header:     http.Header{"Content-Type": []string{"text/plain"}},
			Body:       io.NopCloser(strings.NewReader("provider gateway returned truncated body")),
		}, nil
	})

	_, err := testLLM.Execute(context.Background(), []llm.Message{llm.UserMessage(llm.Text("test"))}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "provider gateway returned truncated body") {
		t.Fatalf("error = %q, want raw provider body", msg)
	}
	if strings.Contains(msg, "unexpected end of JSON input") {
		t.Fatalf("error = %q, want raw provider body without JSON decoder noise", msg)
	}
}

func TestOpenAIResponsesLLMIncludesConfiguredParamsInRequestBody(t *testing.T) {
	var capturedBody map[string]any
	testLLM, ok := newOpenAIResponsesLLM("sk-test", "http://example.invalid", "gpt-test", 16, []byte(`{
		"reasoning": {"effort": "none"},
		"text": {"verbosity": "low"},
		"temperature": 0.2,
		"stream": false,
		"max_output_tokens": 2048,
		"prompt_cache_key": "ignored-static-key",
		"input": "ignored",
		"tools": [{"type": "web_search_preview"}]
	}`)).(*openAIResponsesLLM)
	if !ok {
		t.Fatalf("newOpenAIResponsesLLM returned %T, want *openAIResponsesLLM", testLLM)
	}
	testLLM.client = openaigo.NewClient(
		option.WithAPIKey("sk-test"),
		option.WithBaseURL("http://example.invalid"),
		option.WithHTTPClient(&http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			if !strings.HasSuffix(req.URL.Path, "/responses") {
				t.Errorf("request path = %q, want suffix /responses", req.URL.Path)
			}
			body, err := io.ReadAll(req.Body)
			if err != nil {
				return nil, err
			}
			if err := json.Unmarshal(body, &capturedBody); err != nil {
				return nil, err
			}
			respBody := `{"id":"resp_123","object":"response","created":1234567890,"model":"gpt-test","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":10,"output_tokens":5}}`
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(respBody)),
			}, nil
		})}),
	)
	messages := []llm.Message{{Role: llm.RoleUser, Parts: []llm.ContentPart{llm.Text("test")}}}
	ctx := llm.WithPromptCacheKey(context.Background(), "root-session")
	if _, err := testLLM.Execute(ctx, messages, nil); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	reasoning, ok := capturedBody["reasoning"].(map[string]any)
	if !ok || reasoning["effort"] != "none" {
		t.Fatalf("reasoning = %v, want effort none; body=%+v", capturedBody["reasoning"], capturedBody)
	}
	text, ok := capturedBody["text"].(map[string]any)
	if !ok || text["verbosity"] != "low" {
		t.Fatalf("text = %v, want verbosity low; body=%+v", capturedBody["text"], capturedBody)
	}
	if capturedBody["temperature"] != 0.2 {
		t.Fatalf("temperature = %v, want 0.2; body=%+v", capturedBody["temperature"], capturedBody)
	}
	if capturedBody["stream"] != false {
		t.Fatalf("stream = %v, want false; body=%+v", capturedBody["stream"], capturedBody)
	}
	if capturedBody["max_output_tokens"] != float64(2048) {
		t.Fatalf("max_output_tokens = %v, want 2048; body=%+v", capturedBody["max_output_tokens"], capturedBody)
	}
	if capturedBody["input"] == "ignored" {
		t.Fatalf("params input overrode runtime input; body=%+v", capturedBody)
	}
	if _, ok := capturedBody["tools"]; ok {
		t.Fatalf("params tools should not be sent without runtime tools; body=%+v", capturedBody)
	}
	if capturedBody["prompt_cache_key"] != "root-session" {
		t.Fatalf("prompt_cache_key=%v, want root-session; body=%+v", capturedBody["prompt_cache_key"], capturedBody)
	}
}

func TestOpenAIResponsesStreamingSurfacesReasoningOnlyOutput(t *testing.T) {
	body := strings.Join([]string{
		`data: {"type":"response.reasoning_summary_text.delta","delta":"Need ","item_id":"rs_1","output_index":0,"summary_index":0,"sequence_number":1}`,
		"",
		`data: {"type":"response.reasoning_summary_text.delta","delta":"another tool pass.","item_id":"rs_1","output_index":0,"summary_index":0,"sequence_number":2}`,
		"",
		`data: {"type":"response.completed","response":{"id":"resp_test","output":[{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"Need another tool pass."}]}],"usage":{"input_tokens":10,"output_tokens":5}}}`,
		"",
	}, "\n")
	testLLM := newTestOpenAIResponsesLLM(t, func(req *http.Request) (*http.Response, error) {
		return sseResponse(body), nil
	})

	messages := []llm.Message{{Role: llm.RoleUser, Parts: []llm.ContentPart{llm.Text("test")}}}
	result, err := testLLM.Execute(context.Background(), messages, nil)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if result == nil || result.Message == nil {
		t.Fatalf("missing result message")
	}
	text := strings.TrimSpace(result.Message.TextContent())
	if text == "" {
		t.Fatalf("expected surfaced reasoning text, got empty")
	}
	if !strings.Contains(text, "Need another tool pass.") {
		t.Fatalf("expected surfaced reasoning content, got %q", text)
	}
	if !strings.HasPrefix(text, "```thinking\n") || !strings.HasSuffix(text, "\n```") {
		t.Fatalf("expected reasoning wrapped in fenced code block, got %q", text)
	}
	payload, ok := llm.Decode(result.Message.Name)
	if !ok {
		t.Fatalf("expected structured reasoning carry, got %q", result.Message.Name)
	}
	if got := llm.SummaryText(payload.Items); !strings.Contains(got, "Need another tool pass.") {
		t.Fatalf("carry summary = %q", got)
	}
}

func TestOpenAIResponsesStreamingCallsReasoningDoneOnSummaryDone(t *testing.T) {
	body := strings.Join([]string{
		`data: {"type":"response.reasoning_summary_text.delta","delta":"Need ","item_id":"rs_1","output_index":0,"summary_index":0,"sequence_number":1}`,
		"",
		`data: {"type":"response.reasoning_summary_text.done","text":"Need another pass.","item_id":"rs_1","output_index":0,"summary_index":0,"sequence_number":2}`,
		"",
		`data: {"type":"response.completed","response":{"id":"resp_test","output":[{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"Need another pass."}]}],"usage":{"input_tokens":10,"output_tokens":5}}}`,
		"",
	}, "\n")
	testLLM := newTestOpenAIResponsesLLM(t, func(req *http.Request) (*http.Response, error) {
		return sseResponse(body), nil
	})

	var deltas []string
	reasoningDoneCalls := 0
	responseStarts := 0
	var callbackOrder []string
	ctx := llm.WithStreamSink(context.Background(), &llm.StreamSink{
		OnResponseStarted: func() {
			responseStarts++
			callbackOrder = append(callbackOrder, "started")
		},
		OnReasoningDelta: func(text string) {
			callbackOrder = append(callbackOrder, "reasoning")
			deltas = append(deltas, text)
		},
		OnReasoningDone: func() {
			reasoningDoneCalls++
		},
	})
	_, err := testLLM.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("test"))}, nil)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if strings.Join(deltas, "") != "Need " {
		t.Fatalf("reasoning deltas = %#v", deltas)
	}
	if reasoningDoneCalls != 1 {
		t.Fatalf("reasoning done calls = %d, want 1", reasoningDoneCalls)
	}
	if responseStarts != 1 || len(callbackOrder) == 0 || callbackOrder[0] != "started" {
		t.Fatalf("response-start callbacks=%d order=%v", responseStarts, callbackOrder)
	}
}

func TestOpenAIResponsesSyncCarriesEncryptedReasoningItems(t *testing.T) {
	var capturedBody map[string]any
	testLLM := newTestOpenAIResponsesLLMWithStream(t, false, func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		_ = json.Unmarshal(body, &capturedBody)
		respBody := `{"id":"resp_reasoning","object":"response","status":"completed","model":"gpt-5.4","output":[{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"Need one more pass over the tool results."}],"encrypted_content":"enc_sync"}],"usage":{"input_tokens":10,"output_tokens":5}}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(respBody)),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
		}, nil
	})

	result, err := testLLM.Execute(context.Background(), []llm.Message{llm.UserMessage(llm.Text("test"))}, nil)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	includeVals, _ := capturedBody["include"].([]any)
	if !slices.Contains(includeVals, any("reasoning.encrypted_content")) {
		t.Fatalf("include = %#v, want reasoning.encrypted_content", capturedBody["include"])
	}
	payload, ok := llm.Decode(result.Message.Name)
	if !ok {
		t.Fatalf("expected structured reasoning carry, got %q", result.Message.Name)
	}
	if len(payload.Items) != 1 {
		t.Fatalf("payload items = %#v, want 1", payload.Items)
	}
	if payload.Items[0].EncryptedContent != "enc_sync" {
		t.Fatalf("encrypted_content = %q, want enc_sync", payload.Items[0].EncryptedContent)
	}
}

func TestOpenAIResponsesStreamingCollectsReasoningFromAddedAndDoneEvents(t *testing.T) {
	body := strings.Join([]string{
		`data: {"type":"response.output_item.added","item":{"id":"rs_1","type":"reasoning","encrypted_content":"enc_added","summary":[]},"output_index":0,"sequence_number":1}`,
		"",
		`data: {"type":"response.reasoning_summary_text.delta","delta":"Need ","item_id":"rs_1","output_index":0,"summary_index":0,"sequence_number":2}`,
		"",
		`data: {"type":"response.reasoning_summary_text.done","item_id":"rs_1","output_index":0,"summary_index":0,"text":"Need another pass.","sequence_number":3}`,
		"",
		`data: {"type":"response.output_item.done","item":{"id":"rs_1","type":"reasoning","encrypted_content":"enc_done","summary":[{"type":"summary_text","text":"Need another pass."}],"status":"completed"},"output_index":0,"sequence_number":4}`,
		"",
		`data: {"type":"response.completed","response":{"id":"resp_test","output":[{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"Need another pass."}],"encrypted_content":"enc_done"}],"usage":{"input_tokens":10,"output_tokens":5}}}`,
		"",
	}, "\n")
	testLLM := newTestOpenAIResponsesLLM(t, func(req *http.Request) (*http.Response, error) {
		return sseResponse(body), nil
	})

	result, err := testLLM.Execute(context.Background(), []llm.Message{llm.UserMessage(llm.Text("test"))}, nil)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	text := strings.TrimSpace(result.Message.TextContent())
	if !strings.Contains(text, "Need another pass.") {
		t.Fatalf("visible reasoning = %q", text)
	}
	payload, ok := llm.Decode(result.Message.Name)
	if !ok {
		t.Fatalf("expected structured reasoning carry, got %q", result.Message.Name)
	}
	if len(payload.Items) != 1 {
		t.Fatalf("payload items = %#v, want 1", payload.Items)
	}
	if payload.Items[0].EncryptedContent != "enc_done" {
		t.Fatalf("encrypted_content = %q, want enc_done", payload.Items[0].EncryptedContent)
	}
	if got := llm.SummaryText(payload.Items); got != "Need another pass." {
		t.Fatalf("carry summary = %q, want exact summary", got)
	}
}

func TestOpenAIResponsesLLMReplaysReasoningCarryAsReasoningInputItems(t *testing.T) {
	var capturedBody map[string]any
	testLLM := newTestOpenAIResponsesLLMWithStream(t, false, func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		_ = json.Unmarshal(body, &capturedBody)
		respBody := `{"id":"resp_123","object":"response","created":1234567890,"model":"gpt-test","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":10,"output_tokens":5}}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(respBody)),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
		}, nil
	})

	carry := llm.Encode([]llm.Item{{
		ID:               "rs_prev",
		Summary:          []string{"Step one", "Step two"},
		EncryptedContent: "enc_prev",
	}})
	messages := []llm.Message{
		llm.UserMessage(llm.Text("test")),
		{Role: llm.RoleAssistant, Name: carry},
	}
	if _, err := testLLM.Execute(context.Background(), messages, nil); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	inputItems, ok := capturedBody["input"].([]any)
	if !ok || len(inputItems) != 2 {
		t.Fatalf("input items = %#v, want 2", capturedBody["input"])
	}
	reasoning, _ := inputItems[1].(map[string]any)
	if reasoning["type"] != "reasoning" || reasoning["id"] != "rs_prev" {
		t.Fatalf("second input item = %#v, want reasoning item", inputItems[1])
	}
	if reasoning["encrypted_content"] != "enc_prev" {
		t.Fatalf("encrypted_content = %v, want enc_prev", reasoning["encrypted_content"])
	}
	summary, ok := reasoning["summary"].([]any)
	if !ok || len(summary) != 2 {
		t.Fatalf("summary = %#v, want 2 items", reasoning["summary"])
	}
}

func TestOpenAIResponsesParamsOverlayDoesNotTranslateLegacyReasoningEffort(t *testing.T) {
	overlay := normalizeOpenAIResponsesParamsOverlay(map[string]any{
		"reasoning_effort": "high",
		"input":            "ignored",
		"tools":            []any{map[string]any{"type": "web_search_preview"}},
	})

	if _, ok := overlay["reasoning"]; ok {
		t.Fatalf("legacy reasoning_effort should not be translated, got overlay=%+v", overlay)
	}
	if overlay["reasoning_effort"] != "high" {
		t.Fatalf("reasoning_effort should remain untouched if configured, got overlay=%+v", overlay)
	}
	if _, ok := overlay["input"]; ok {
		t.Fatalf("input should be reserved for runtime request construction, got overlay=%+v", overlay)
	}
	if _, ok := overlay["tools"]; ok {
		t.Fatalf("tools should be reserved for runtime request construction, got overlay=%+v", overlay)
	}
}

func TestOpenAIResponsesLLMSuppressesUserAgent(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "http://example.invalid/v1/responses", nil)
	if err != nil {
		t.Fatalf("NewRequest failed: %v", err)
	}
	req.Header.Set("User-Agent", "OpenAI/Go 1.0")

	var wire bytes.Buffer
	rt := &suppressUserAgentRoundTripper{next: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if err := req.Write(&wire); err != nil {
			t.Fatalf("Write failed: %v", err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader("{}")),
		}, nil
	})}

	_, err = rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip failed: %v", err)
	}
	if strings.Contains(wire.String(), "\r\nUser-Agent:") {
		t.Fatalf("User-Agent header was sent:\n%s", wire.String())
	}
}

func newTestOpenAIResponsesLLM(t *testing.T, fn func(*http.Request) (*http.Response, error)) llm.LLM {
	t.Helper()
	return newTestOpenAIResponsesLLMWithStream(t, true, fn)
}

func newTestOpenAIResponsesLLMWithStream(t *testing.T, stream bool, fn func(*http.Request) (*http.Response, error)) llm.LLM {
	t.Helper()
	client := openaigo.NewClient(
		option.WithAPIKey("sk-test"),
		option.WithBaseURL("http://example.invalid"),
		option.WithHTTPClient(&http.Client{Transport: &responsesContentTypeFixer{next: &responsesSSEFrameFilter{next: roundTripperFunc(fn)}}}),
	)
	return &openAIResponsesLLM{
		client:    client,
		model:     shared.ResponsesModel("gpt-test"),
		maxTokens: 16,
		stream:    stream,
	}
}

func sseResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     http.StatusText(http.StatusOK),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func syncResponsesBody(text string) string {
	return "{\"id\":\"resp_test\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"gpt-test\",\"output\":[{\"type\":\"message\",\"id\":\"msg_test\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"" + text + "\"}]}]}"
}

// newExternalContextTool builds a raw tool flagged as carrying external
// context, which is the only property of pkg/run's MCP delegating tool that
// the responses client branches on.
func newExternalContextTool(name, description string, schema map[string]any, call func(context.Context, json.RawMessage) (string, error)) (*llm.Tool, error) {
	tool, err := llm.NewRawTool(name, description, schema, func(ctx context.Context, arguments string) (any, error) {
		arguments = strings.TrimSpace(arguments)
		if arguments == "" {
			arguments = "{}"
		}
		return call(ctx, json.RawMessage(arguments))
	})
	if err != nil {
		return nil, err
	}
	return tool.SetContainsExternalContext(true), nil
}

// captureResponsesRequest runs one Execute against a stub and returns the JSON
// body that was sent.
func captureResponsesRequest(t *testing.T, client llm.LLM) map[string]any {
	t.Helper()
	var body map[string]any
	_, err := client.Execute(context.Background(), []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil)
	_ = err // the stub server records the body regardless of how decoding ends
	return body
}

// Both of these were found by running a real turn against the ChatGPT/Codex
// backend, which rejects the request outright rather than ignoring a field it
// does not accept. Before the fixes every single Codex request failed — first
// with "Store must be set to false", then with "Unsupported parameter:
// max_output_tokens" — so the provider was completely unusable and no test
// noticed, because every test in this package points at a stub that accepts
// anything.
func TestResponsesRequestShapeForCodexAndForOpenAI(t *testing.T) {
	newClientAgainst := func(t *testing.T, baseURL string) (llm.LLM, *map[string]any) {
		t.Helper()
		body := map[string]any{}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode request: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, syncResponsesBody("ok"))
		}))
		t.Cleanup(srv.Close)
		// The client keys off the configured base URL, so the Codex case has to
		// be built with the real prefix; the stub is reached by overriding the
		// transport to redirect there.
		effective := srv.URL + "/v1"
		if baseURL == CodexBaseURL {
			effective = CodexBaseURL
		}
		client := NewResponsesLLMWithWebSearchCache("sk-test", effective, "gpt-test", 16,
			[]byte(`{"stream":false}`), "", redirectTo(srv.URL), true)
		return client, &body
	}

	t.Run("codex omits max_output_tokens", func(t *testing.T) {
		client, body := newClientAgainst(t, CodexBaseURL)
		captureResponsesRequest(t, client)
		if _, present := (*body)["max_output_tokens"]; present {
			t.Fatal("max_output_tokens was sent to the Codex backend, which rejects the whole request for it")
		}
		if store, ok := (*body)["store"].(bool); !ok || store {
			t.Fatalf("store = %v, want false: the Codex backend refuses anything else", (*body)["store"])
		}
	})

	t.Run("openai keeps max_output_tokens and still does not store", func(t *testing.T) {
		client, body := newClientAgainst(t, "https://api.openai.com/v1")
		captureResponsesRequest(t, client)
		if _, present := (*body)["max_output_tokens"]; !present {
			t.Fatal("max_output_tokens is missing for a non-Codex endpoint, which supports and needs it")
		}
		if store, ok := (*body)["store"].(bool); !ok || store {
			t.Fatalf("store = %v, want false: forebrain never reads a stored response back", (*body)["store"])
		}
	})
}

// redirectTo sends every request to the stub server regardless of the base URL
// the client was configured with, so the Codex code path can be exercised
// without talking to ChatGPT.
func redirectTo(target string) http.RoundTripper {
	return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		u, err := url.Parse(target)
		if err != nil {
			return nil, err
		}
		clone := req.Clone(req.Context())
		clone.URL.Scheme, clone.URL.Host = u.Scheme, u.Host
		return http.DefaultTransport.RoundTrip(clone)
	})
}

// The helpers below were production functions that only the tests in this
// package ever called. They live here so the production files carry no
// unused code while the tests keep exercising the live code underneath.

func newOpenAIResponsesLLM(apiKey, baseURL, model string, maxTokens int, paramsJSON []byte) llm.LLM {
	return NewResponsesLLMWithCache(apiKey, baseURL, model, maxTokens, paramsJSON, true)
}
