package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	openaiclient "github.com/sashabaranov/go-openai"
)

func TestOpenAIPromptCacheKeyRoundTripperInjectsContextKey(t *testing.T) {
	var received map[string]any
	rt := &openAIPromptCacheKeyRoundTripper{inner: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(req.Body).Decode(&received); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		return openAICompatTestHTTPResponse(t), nil
	})}
	req, err := http.NewRequest(http.MethodPost, "https://api.openai.com/v1/chat/completions", strings.NewReader(`{"model":"gpt-test"}`))
	if err != nil {
		t.Fatal(err)
	}
	req = req.WithContext(llm.WithPromptCacheKey(req.Context(), "root-session"))
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if got := received["prompt_cache_key"]; got != "root-session" {
		t.Fatalf("prompt_cache_key=%v", got)
	}
}

func TestCompatMessageToOpenAIExcludesInternalToolTiming(t *testing.T) {
	timing := llm.NewExecutionTiming(time.Now(), time.Now().Add(time.Second))
	msg := llm.ToolResultMessage("call-1", llm.Text("done"))
	msg.ToolExecutionTiming = &timing

	projected := compatMessageToOpenAI(msg)
	raw, err := json.Marshal(projected)
	if err != nil {
		t.Fatalf("marshal projected message: %v", err)
	}
	if strings.Contains(string(raw), "tool_execution_timing") || strings.Contains(string(raw), "started_at") {
		t.Fatalf("internal timing leaked into provider payload: %s", raw)
	}
	if projected.ToolCallID != "call-1" {
		t.Fatalf("tool_call_id=%q want call-1", projected.ToolCallID)
	}
}

func TestOpenAICompatLLMUsesDefaultChatCompletionsPath(t *testing.T) {
	t.Parallel()

	seen := make(chan string, 1)
	client := newTestOpenAICompatLLMWithParams(t, "/v1", "", []byte(`{"stream":false}`), func(r *http.Request) (*http.Response, error) {
		seen <- r.URL.Path
		return openAICompatTestHTTPResponse(t), nil
	})
	res, err := client.Execute(context.Background(), []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if got := res.Message.TextContent(); got != "ok" {
		t.Fatalf("message content = %q, want ok", got)
	}
	if got := <-seen; got != "/v1/chat/completions" {
		t.Fatalf("request path = %q, want /v1/chat/completions", got)
	}
}

func TestOpenAICompatLLMRewritesChatCompletionsPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		basePath     string
		apiPath      string
		wantPath     string
		wantRawQuery string
	}{
		{
			name:     "base URL version prefix",
			basePath: "/v1",
			apiPath:  "/responses",
			wantPath: "/v1/responses",
		},
		{
			name:     "api path owns version prefix",
			basePath: "",
			apiPath:  "/v1/responses",
			wantPath: "/v1/responses",
		},
		{
			name:         "api path query",
			basePath:     "/openai",
			apiPath:      "/deployments/test/chat/completions?api-version=2026-05-08",
			wantPath:     "/openai/deployments/test/chat/completions",
			wantRawQuery: "api-version=2026-05-08",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			seen := make(chan *http.Request, 1)
			client := newTestOpenAICompatLLMWithParams(t, tt.basePath, tt.apiPath, []byte(`{"stream":false}`), func(r *http.Request) (*http.Response, error) {
				seen <- r.Clone(r.Context())
				return openAICompatTestHTTPResponse(t), nil
			})
			res, err := client.Execute(context.Background(), []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil)
			if err != nil {
				t.Fatalf("Execute returned error: %v", err)
			}
			if got := res.Message.TextContent(); got != "ok" {
				t.Fatalf("message content = %q, want ok", got)
			}
			req := <-seen
			if got := req.URL.Path; got != tt.wantPath {
				t.Fatalf("request path = %q, want %q", got, tt.wantPath)
			}
			if got := req.URL.RawQuery; got != tt.wantRawQuery {
				t.Fatalf("request raw query = %q, want %q", got, tt.wantRawQuery)
			}
		})
	}
}

func TestOpenAIChatCompletionRequestMergedUsesMaxCompletionTokensForGPT5(t *testing.T) {
	t.Parallel()

	req, err := openAIChatCompletionRequestMerged("gpt-5.5", 1, 2048, nil, nil, nil)
	if err != nil {
		t.Fatalf("openAIChatCompletionRequestMerged returned error: %v", err)
	}
	if req.MaxCompletionTokens != 2048 {
		t.Fatalf("MaxCompletionTokens = %d, want 2048", req.MaxCompletionTokens)
	}
	if req.MaxTokens != 0 {
		t.Fatalf("MaxTokens = %d, want 0", req.MaxTokens)
	}
}

func TestOpenAIChatCompletionRequestMergedIgnoresOverlayMaxTokensForReasoningModel(t *testing.T) {
	t.Parallel()

	req, err := openAIChatCompletionRequestMerged(
		"gpt-5.5",
		1,
		1024,
		[]byte(`{"max_tokens":4096}`),
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("openAIChatCompletionRequestMerged returned error: %v", err)
	}
	if req.MaxCompletionTokens != 1024 {
		t.Fatalf("MaxCompletionTokens = %d, want 1024", req.MaxCompletionTokens)
	}
	if req.MaxTokens != 0 {
		t.Fatalf("MaxTokens = %d, want 0", req.MaxTokens)
	}
}

func TestOpenAIChatCompletionRequestMergedKeepsMaxTokensForNonReasoningModel(t *testing.T) {
	t.Parallel()

	req, err := openAIChatCompletionRequestMerged("gpt-4o-mini", 1, 512, nil, nil, nil)
	if err != nil {
		t.Fatalf("openAIChatCompletionRequestMerged returned error: %v", err)
	}
	if req.MaxTokens != 512 {
		t.Fatalf("MaxTokens = %d, want 512", req.MaxTokens)
	}
	if req.MaxCompletionTokens != 0 {
		t.Fatalf("MaxCompletionTokens = %d, want 0", req.MaxCompletionTokens)
	}
}

func TestOpenAICompatLLMPreservesRawProviderErrorBody(t *testing.T) {
	t.Parallel()

	client := newTestOpenAICompatLLMWithParams(t, "/v1", "", []byte(`{"stream":false}`), func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusBadGateway,
			Status:     http.StatusText(http.StatusBadGateway),
			Header:     http.Header{"Content-Type": []string{"text/plain"}},
			Body:       io.NopCloser(strings.NewReader("upstream JSON decoder failed")),
		}, nil
	})

	_, err := client.Execute(context.Background(), []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "upstream JSON decoder failed") {
		t.Fatalf("error = %q, want raw provider body", msg)
	}
	if strings.Contains(msg, "unexpected end of JSON input") {
		t.Fatalf("error = %q, want raw provider body without JSON decoder noise", msg)
	}
}

func TestOpenAICompatLLMParsesReasoningAliasToolCallUnary(t *testing.T) {
	t.Parallel()

	client := newTestOpenAICompatLLMWithParams(t, "/v1", "", []byte(`{"stream":false}`), func(r *http.Request) (*http.Response, error) {
		rec := httptestResponseRecorder{header: http.Header{}}
		rec.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"id":      "chatcmpl-test",
			"object":  "chat.completion",
			"created": 1,
			"model":   "test",
			"choices": []map[string]any{{
				"index": 0,
				"message": map[string]any{
					"role":       "assistant",
					"content":    nil,
					"tool_calls": []any{},
					"reasoning":  "<tool_call>\n<function=find-skills>\n</function>\n</tool_call>",
				},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{
				"prompt_tokens":     3,
				"completion_tokens": 2,
				"total_tokens":      5,
			},
		}
		if err := json.NewEncoder(&rec).Encode(resp); err != nil {
			t.Fatalf("encode response: %v", err)
		}
		return rec.response(), nil
	})

	res, err := client.Execute(context.Background(), []llm.Message{llm.UserMessage(llm.Text("find skill"))}, nil)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if res == nil || res.Message == nil {
		t.Fatalf("expected non-nil result message")
	}
	if got := len(res.Message.ToolCalls); got != 1 {
		t.Fatalf("tool call count = %d, want 1", got)
	}
	call := res.Message.ToolCalls[0]
	if call.Type != llm.ToolTypeFunction {
		t.Fatalf("tool call type = %q, want %q", call.Type, llm.ToolTypeFunction)
	}
	if call.Function.Name != "find-skills" {
		t.Fatalf("tool name = %q, want find-skills", call.Function.Name)
	}
	if call.Function.Arguments != `{}` {
		t.Fatalf("tool arguments = %q, want {}", call.Function.Arguments)
	}
}

func TestOpenAICompatLLMSurfacesReasoningOnlyResponseAsContent(t *testing.T) {
	t.Parallel()

	// Reasoning-only response: model emitted thinking content but no real
	// content and no parseable tool call (XML brackets missing). Without
	// surfacing, the orchestrator rejects this turn as "empty assistant
	// response after tool execution".
	reasoningText := "function=read_file>\nparameter=file_path>\ninternal/tui/composer.go\nparameter>\n"
	client := newTestOpenAICompatLLMWithParams(t, "/v1", "", []byte(`{"stream":false}`), func(r *http.Request) (*http.Response, error) {
		rec := httptestResponseRecorder{header: http.Header{}}
		rec.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"id":      "chatcmpl-test",
			"object":  "chat.completion",
			"created": 1,
			"model":   "test",
			"choices": []map[string]any{{
				"index": 0,
				"message": map[string]any{
					"role":      "assistant",
					"content":   nil,
					"reasoning": reasoningText,
				},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{
				"prompt_tokens":     1,
				"completion_tokens": 1,
				"total_tokens":      2,
			},
		}
		if err := json.NewEncoder(&rec).Encode(resp); err != nil {
			t.Fatalf("encode response: %v", err)
		}
		return rec.response(), nil
	})

	res, err := client.Execute(context.Background(), []llm.Message{llm.UserMessage(llm.Text("go"))}, nil)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if res == nil || res.Message == nil {
		t.Fatalf("expected non-nil result message")
	}
	if got := len(res.Message.ToolCalls); got != 0 {
		t.Fatalf("tool call count = %d, want 0", got)
	}
	text := strings.TrimSpace(res.Message.TextContent())
	if text == "" {
		t.Fatalf("expected reasoning text surfaced as visible content, got empty")
	}
	if !strings.Contains(text, "internal/tui/composer.go") {
		t.Fatalf("expected surfaced text to contain reasoning, got %q", text)
	}
	if !strings.HasPrefix(text, "```thinking") || !strings.HasSuffix(text, "```") {
		t.Fatalf("expected reasoning wrapped in fenced code block, got %q", text)
	}
	if strings.HasPrefix(res.Message.Name, openAIReasoningCarryPrefix) {
		t.Fatalf("expected reasoning carry cleared once promoted, got %q", res.Message.Name)
	}
}

func TestOpenAICompatLLMParsesReasoningAliasToolCallStream(t *testing.T) {
	t.Parallel()

	client := newTestOpenAICompatLLMWithParams(t, "/v1", "", []byte(`{"stream":true}`), func(r *http.Request) (*http.Response, error) {
		rec := httptestResponseRecorder{header: http.Header{}}
		rec.Header().Set("Content-Type", "text/event-stream")
		var body bytes.Buffer
		body.WriteString("data: ")
		body.WriteString(`{"id":"1","object":"chat.completion.chunk","created":1,"model":"test","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`)
		body.WriteString("\n\n")
		body.WriteString("data: ")
		body.WriteString("{\"id\":\"2\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"test\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning\":\"<tool_call>\\n<function=find-skills>\\n</function>\\n</tool_call>\"},\"finish_reason\":null}]}")
		body.WriteString("\n\n")
		body.WriteString("data: ")
		body.WriteString(`{"id":"3","object":"chat.completion.chunk","created":1,"model":"test","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
		body.WriteString("\n\n")
		body.WriteString("data: [DONE]\n\n")
		_, _ = rec.Write(body.Bytes())
		return rec.response(), nil
	})

	res, err := client.Execute(context.Background(), []llm.Message{llm.UserMessage(llm.Text("find skill"))}, nil)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if res == nil || res.Message == nil {
		t.Fatalf("expected non-nil result message")
	}
	if got := len(res.Message.ToolCalls); got != 1 {
		t.Fatalf("tool call count = %d, want 1", got)
	}
	call := res.Message.ToolCalls[0]
	if call.Function.Name != "find-skills" {
		t.Fatalf("tool name = %q, want find-skills", call.Function.Name)
	}
	if call.Function.Arguments != `{}` {
		t.Fatalf("tool arguments = %q, want {}", call.Function.Arguments)
	}
}

func TestOpenAICompatLLMStreamEnablesIncludeUsage(t *testing.T) {
	t.Parallel()

	var includeUsage bool
	client := newTestOpenAICompatLLMWithParams(t, "/v1", "", []byte(`{"stream":true}`), func(r *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if opts, ok := body["stream_options"].(map[string]any); ok {
			if v, ok := opts["include_usage"].(bool); ok {
				includeUsage = v
			}
		}
		rec := httptestResponseRecorder{header: http.Header{}}
		rec.Header().Set("Content-Type", "text/event-stream")
		_, _ = rec.Write([]byte("data: [DONE]\n\n"))
		return rec.response(), nil
	})

	_, err := client.Execute(context.Background(), []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !includeUsage {
		t.Fatal("expected stream_options.include_usage=true")
	}
}

func TestOpenAICompatLLMStreamReportsUsageImmediately(t *testing.T) {
	t.Parallel()

	client := newTestOpenAICompatLLMWithParams(t, "/v1", "", []byte(`{"stream":true}`), func(r *http.Request) (*http.Response, error) {
		rec := httptestResponseRecorder{header: http.Header{}}
		rec.Header().Set("Content-Type", "text/event-stream")
		var body bytes.Buffer
		body.WriteString(`data: {"id":"1","object":"chat.completion.chunk","created":1,"model":"test","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}` + "\n\n")
		body.WriteString(`data: {"id":"2","object":"chat.completion.chunk","created":1,"model":"test","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":null}],"usage":{"prompt_tokens":1499,"completion_tokens":75,"total_tokens":1574}}` + "\n\n")
		body.WriteString("data: [DONE]\n\n")
		_, _ = rec.Write(body.Bytes())
		return rec.response(), nil
	})

	var usageCalls [][2]int
	var callbackOrder []string
	responseStarts := 0
	ctx := llm.WithStreamSink(context.Background(), &llm.StreamSink{
		OnResponseStarted: func() {
			responseStarts++
			callbackOrder = append(callbackOrder, "started")
		},
		OnUsage: func(in, out int) {
			callbackOrder = append(callbackOrder, "usage")
			usageCalls = append(usageCalls, [2]int{in, out})
		},
	})
	res, err := client.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if res == nil || res.Usage == nil {
		t.Fatalf("expected non-nil usage")
	}
	if len(usageCalls) != 1 {
		t.Fatalf("usage callbacks=%d want 1", len(usageCalls))
	}
	if usageCalls[0] != [2]int{1499, 75} {
		t.Fatalf("usage callback=%v want [1499 75]", usageCalls[0])
	}
	if responseStarts != 1 || len(callbackOrder) == 0 || callbackOrder[0] != "started" {
		t.Fatalf("response-start callbacks=%d order=%v", responseStarts, callbackOrder)
	}
}

func TestOpenAICompatLLMStreamsThroughTelemetryBeforeServerCompletes(t *testing.T) {
	t.Setenv("FOREBRAIN_LLM_HTTP_DEBUG", "0")

	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseServer := func() { releaseOnce.Do(func() { close(release) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `data: {"id":"1","object":"chat.completion.chunk","created":1,"model":"test","choices":[{"index":0,"delta":{"role":"assistant","content":"first"},"finish_reason":null}]}`+"\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-release
		_, _ = io.WriteString(w, `data: {"id":"2","object":"chat.completion.chunk","created":1,"model":"test","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(func() {
		releaseServer()
		server.Close()
	})

	client := newOpenAICompatLLM("sk-test", server.URL+"/v1", "gpt-test", 0, 16, "", []byte(`{"stream":true}`))
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
		t.Fatal("chat completions delta did not arrive while server was still streaming")
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
		t.Fatal("timed out waiting for chat completions stream")
	}
}

func TestOpenAICompatLLMStreamPreservesRawProviderErrorEvent(t *testing.T) {
	t.Parallel()

	client := newTestOpenAICompatLLMWithParams(t, "/v1", "", []byte(`{"stream":true}`), func(r *http.Request) (*http.Response, error) {
		rec := httptestResponseRecorder{header: http.Header{}}
		rec.Header().Set("Content-Type", "text/event-stream")
		_, _ = rec.Write([]byte(`data: {"error":{"code":"upstream_stream_read_error","message":"Upstream response stream was interrupted","type":"upstream_error"}}`))
		return rec.response(), nil
	})

	_, err := client.Execute(context.Background(), []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	for _, want := range []string{
		`"code":"upstream_stream_read_error"`,
		`"message":"Upstream response stream was interrupted"`,
		`"type":"upstream_error"`,
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error = %q, want %q", msg, want)
		}
	}
	if strings.Contains(msg, "unexpected end of JSON input") {
		t.Fatalf("error = %q, want raw provider error without JSON decoder noise", msg)
	}
}

func TestIsReasoningStyleOpenAIModel(t *testing.T) {
	t.Parallel()

	cases := []struct {
		model string
		want  bool
	}{
		{model: "gpt-5", want: true},
		{model: "gpt-5.5", want: true},
		{model: "o3-mini", want: true},
		{model: "o4-mini", want: true},
		{model: "gpt-4o", want: false},
		{model: "gpt-4o-mini", want: false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(strings.ReplaceAll(tc.model, ".", "_"), func(t *testing.T) {
			t.Parallel()
			if got := isReasoningStyleOpenAIModel(tc.model); got != tc.want {
				t.Fatalf("isReasoningStyleOpenAIModel(%q)=%v want=%v", tc.model, got, tc.want)
			}
		})
	}
}

func writeOpenAICompatTestResponse(t *testing.T, w http.ResponseWriter) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	resp := map[string]any{
		"id":      "chatcmpl-test",
		"object":  "chat.completion",
		"created": 1,
		"model":   "gpt-test",
		"choices": []map[string]any{{
			"index": 0,
			"message": map[string]any{
				"role":    "assistant",
				"content": "ok",
			},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{
			"prompt_tokens":     3,
			"completion_tokens": 2,
			"total_tokens":      5,
		},
	}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

func newTestOpenAICompatLLMWithParams(t *testing.T, basePath, apiPath string, paramsJSON []byte, fn func(*http.Request) (*http.Response, error)) llm.LLM {
	t.Helper()
	cfg := openaiclient.DefaultConfig("sk-test")
	cfg.BaseURL = "http://example.invalid" + basePath
	baseTransport := roundTripperFunc(fn)
	transport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return (&normalizeOpenAICompatReasoningRoundTripper{inner: roundTripperFunc(fn)}).RoundTrip(r)
	})
	_ = baseTransport
	if normalized := normalizeOpenAIChatAPIPath(apiPath); normalized != "" && normalized != defaultOpenAIChatCompletionsPath {
		prevTransport := transport
		transport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			return (&rewriteOpenAIChatPathRoundTripper{inner: prevTransport, apiPath: normalized}).RoundTrip(r)
		})
	}
	cfg.HTTPClient = &http.Client{Transport: transport}
	return &openAICompatLLM{
		client:      openaiclient.NewClientWithConfig(cfg),
		model:       "gpt-test",
		temperature: 0,
		maxTokens:   16,
		paramsJSON:  append([]byte(nil), paramsJSON...),
	}
}

func openAICompatTestHTTPResponse(t *testing.T) *http.Response {
	t.Helper()
	rec := httptestResponseRecorder{header: http.Header{}}
	writeOpenAICompatTestResponse(t, &rec)
	return rec.response()
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

type httptestResponseRecorder struct {
	header http.Header
	body   strings.Builder
	code   int
}

func (r *httptestResponseRecorder) Header() http.Header { return r.header }

func (r *httptestResponseRecorder) WriteHeader(statusCode int) { r.code = statusCode }

func (r *httptestResponseRecorder) Write(p []byte) (int, error) {
	return r.body.Write(p)
}

func (r *httptestResponseRecorder) response() *http.Response {
	if r.code == 0 {
		r.code = http.StatusOK
	}
	return &http.Response{
		StatusCode: r.code,
		Status:     http.StatusText(r.code),
		Header:     r.header,
		Body:       io.NopCloser(strings.NewReader(r.body.String())),
	}
}

// TestNormalizeOpenAIStreamUsage pins the stream/stream_options invariant.
// OpenAI-compatible servers reject stream_options on a non-streaming request,
// and operator params are merged verbatim, so a hand-written "stream": false
// next to a stream_options block must be cleaned up rather than forwarded.
func TestNormalizeOpenAIStreamUsage(t *testing.T) {
	t.Run("streaming opts into usage", func(t *testing.T) {
		body := map[string]interface{}{"stream": true}
		normalizeOpenAIStreamUsage(body)
		opts, ok := body["stream_options"].(map[string]interface{})
		if !ok || opts["include_usage"] != true {
			t.Fatalf("stream_options = %#v, want include_usage:true", body["stream_options"])
		}
	})
	t.Run("streaming preserves an explicit block", func(t *testing.T) {
		body := map[string]interface{}{"stream": true, "stream_options": map[string]interface{}{"include_usage": false}}
		normalizeOpenAIStreamUsage(body)
		opts := body["stream_options"].(map[string]interface{})
		if opts["include_usage"] != false {
			t.Fatalf("include_usage = %v, want the operator's false to survive", opts["include_usage"])
		}
	})
	t.Run("non-streaming drops stream_options", func(t *testing.T) {
		body := map[string]interface{}{"stream": false, "stream_options": map[string]interface{}{"include_usage": true}}
		normalizeOpenAIStreamUsage(body)
		if _, present := body["stream_options"]; present {
			t.Fatalf("stream_options must not survive stream:false, got %#v", body["stream_options"])
		}
	})
	t.Run("absent stream drops stream_options", func(t *testing.T) {
		body := map[string]interface{}{"stream_options": map[string]interface{}{"include_usage": true}}
		normalizeOpenAIStreamUsage(body)
		if _, present := body["stream_options"]; present {
			t.Fatalf("stream_options must not survive a missing stream flag, got %#v", body["stream_options"])
		}
	})
}

// TestMergedRequestDropsStreamOptionsWhenNotStreaming covers the same invariant
// through the real merge path an operator's params travel.
func TestMergedRequestDropsStreamOptionsWhenNotStreaming(t *testing.T) {
	params := []byte(`{"stream":false,"stream_options":{"include_usage":true}}`)
	req, err := openAIChatCompletionRequestMerged("gpt-4o", 0.2, 256, params, nil, nil)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if req.Stream {
		t.Fatal("operator params set stream:false")
	}
	if req.StreamOptions != nil {
		t.Fatalf("StreamOptions = %#v, want nil for a non-streaming request", req.StreamOptions)
	}
}

// TestExecuteNormalizesProviderErrorsForLayer0 proves the Execute funnel
// actually applies normalizeError, not just that normalizeError is correct.
//
// The server answers 413 with prose that says nothing about context length, so
// the only evidence of a size rejection is the HTTP status. llm.IsExceeded can
// only see that status through a normalized *llm.APIError; if the funnel
// stopped normalizing, the classifier would fall back to matching the message
// text and would call this a plain failure. That is what makes this test
// sensitive to the wiring rather than to the conversion alone.
func TestExecuteNormalizesProviderErrorsForLayer0(t *testing.T) {
	t.Setenv("FOREBRAIN_LLM_HTTP_DEBUG", "0")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		_, _ = io.WriteString(w, `{"error":{"message":"request body too large","type":"invalid_request_error"}}`)
	}))
	t.Cleanup(server.Close)

	client := newOpenAICompatLLM("sk-test", server.URL+"/v1", "gpt-test", 0, 16, "", nil)
	_, err := client.Execute(context.Background(), []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil)
	if err == nil {
		t.Fatal("Execute against a 413 must return an error")
	}

	var apiErr *llm.APIError
	if !errors.As(err, &apiErr) || apiErr == nil {
		t.Fatalf("error leaving the provider package must be a *llm.APIError, got %T: %v", err, err)
	}
	if apiErr.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("StatusCode = %d, want 413", apiErr.StatusCode)
	}
	if !llm.IsExceeded(err) {
		t.Fatal("a 413 must classify as context-exceeded once normalized")
	}
	// The original SDK error stays reachable for anything that needs it.
	var sdkErr *openaiclient.APIError
	if !errors.As(err, &sdkErr) || sdkErr == nil {
		t.Fatal("normalizing must keep the underlying SDK error reachable")
	}
}

// The helpers below were production functions that only the tests in this
// package ever called. They live here so the production files carry no
// unused code while the tests keep exercising the live code underneath.

func newOpenAICompatLLM(apiKey, baseURL, model string, temperature float32, maxTokens int, apiPath string, paramsJSON []byte) llm.LLM {
	return NewCompatLLMWithPromptCaching(apiKey, baseURL, model, temperature, maxTokens, apiPath, paramsJSON, false)
}

// A message's wire form must depend on that message alone. When it depends on
// what comes after it, appending a turn rewrites an earlier message, and a
// prefix-caching provider re-bills every token from that message on — once per
// step of every tool loop. This pins the invariant on the one field that was
// projected from the tail: the carried reasoning content.
func TestCompatMessageProjectionDoesNotDependOnLaterMessages(t *testing.T) {
	carry := func(text string) llm.Message {
		m := llm.Message{Role: llm.RoleAssistant, Name: openAIReasoningCarryPrefix + text}
		m.Parts = []llm.ContentPart{llm.Text("answer " + text)}
		return m
	}
	messages := []llm.Message{
		llm.SystemMessage("sys"),
		carry("thinking one"),
		llm.UserMessage(llm.Text("next")),
		carry("thinking two"),
		llm.UserMessage(llm.Text("go on")),
	}

	projected := compatMessagesToOpenAI(messages)
	if len(projected) != len(messages) {
		t.Fatalf("projected %d messages, want %d", len(projected), len(messages))
	}
	for i, m := range projected {
		if m.Name != "" {
			t.Fatalf("projected[%d].Name=%q, want empty", i, m.Name)
		}
	}
	if projected[1].ReasoningContent != "thinking one" || projected[3].ReasoningContent != "thinking two" {
		t.Fatalf("carried reasoning not projected per message: %q / %q",
			projected[1].ReasoningContent, projected[3].ReasoningContent)
	}

	// Growing the conversation must leave every earlier message byte-identical.
	grown := append(append([]llm.Message{}, messages...), carry("thinking three"))
	after := compatMessagesToOpenAI(grown)
	for i := range projected {
		before, err := json.Marshal(projected[i])
		if err != nil {
			t.Fatal(err)
		}
		now, err := json.Marshal(after[i])
		if err != nil {
			t.Fatal(err)
		}
		if string(before) != string(now) {
			t.Fatalf("message %d re-rendered after a later turn was appended:\n%s\n%s", i, before, now)
		}
	}
}

func stableTestTool(t *testing.T, name, description string) *llm.Tool {
	t.Helper()
	tool, err := llm.NewRawTool(name, description,
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{"type": "string", "description": "command to run"},
			},
			"required": []string{"command"},
		},
		func(ctx context.Context, arguments string) (any, error) { return "", nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	return tool
}

// The tools table is the first thing a prefix-caching provider hashes, and
// its serialization order IS part of the cached prefix bytes: reordering the
// same tools is a full cache miss. This golden pins that (a) the same tool
// slice serializes to byte-identical JSON every time, (b) schema maps — the
// one part that could otherwise come from map iteration — serialize sorted,
// and (c) a reordered slice produces different bytes, so any future change
// that silently sorts or shuffles the table fails here instead of quietly
// re-billing every cached prefix.
func TestToolsSerializationIsDeterministicAndOrderSensitive(t *testing.T) {
	tools := []*llm.Tool{
		stableTestTool(t, "shell", "Run a shell command."),
		stableTestTool(t, "read_file", "Read a file."),
		stableTestTool(t, "write_file", "Write a file."),
	}
	first, err := json.Marshal(compatOpenAITools(tools))
	if err != nil {
		t.Fatal(err)
	}
	second, err := json.Marshal(compatOpenAITools(tools))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("same tool slice serialized differently:\n%s\n%s", first, second)
	}

	reordered := []*llm.Tool{tools[1], tools[0], tools[2]}
	other, err := json.Marshal(compatOpenAITools(reordered))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) == string(other) {
		t.Fatal("reordered tools serialized identically; order must be part of the bytes")
	}
	if !strings.Contains(string(first), `"name":"shell"`) {
		t.Fatalf("serialized tools missing shell: %s", first)
	}

	// Schema maps must not depend on Go map iteration order.
	schemaA := map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "string"}, "b": map[string]any{"type": "string"}}}
	schemaB := map[string]any{"properties": map[string]any{"b": map[string]any{"type": "string"}, "a": map[string]any{"type": "string"}}, "type": "object"}
	ta, err := llm.NewRawTool("x", "d", schemaA, func(ctx context.Context, arguments string) (any, error) { return "", nil })
	if err != nil {
		t.Fatal(err)
	}
	tb, err := llm.NewRawTool("x", "d", schemaB, func(ctx context.Context, arguments string) (any, error) { return "", nil })
	if err != nil {
		t.Fatal(err)
	}
	ja, err := json.Marshal(compatOpenAITools([]*llm.Tool{ta}))
	if err != nil {
		t.Fatal(err)
	}
	jb, err := json.Marshal(compatOpenAITools([]*llm.Tool{tb}))
	if err != nil {
		t.Fatal(err)
	}
	if string(ja) != string(jb) {
		t.Fatalf("equal schemas serialized differently (map order leaked):\n%s\n%s", ja, jb)
	}
}
