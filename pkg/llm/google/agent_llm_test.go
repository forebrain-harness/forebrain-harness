package google

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

type weatherArgs struct {
	City string `json:"city"`
}

type weatherOut struct {
	TempC int `json:"temp_c"`
}

func weatherTool(t *testing.T) *llm.Tool {
	t.Helper()
	tool, err := llm.NewTool("weather", "report the current weather for a city",
		func(_ context.Context, args weatherArgs) (weatherOut, error) {
			return weatherOut{TempC: 21}, nil
		})
	if err != nil {
		t.Fatalf("NewTool: %v", err)
	}
	return tool
}

// serveSSE answers one streamGenerateContent call with the given events, each
// written as a single data line so the client's SSE reader can decode it.
func serveSSE(w http.ResponseWriter, events ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, event := range events {
		_, _ = io.WriteString(w, "data: "+event+"\n\n")
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// The projection in both directions, plus the response assembly: mixed text and
// functionCall parts, usage with thinking tokens, and the tool declaration.
func TestExecuteProjectsMessagesAndParsesMixedParts(t *testing.T) {
	type captured struct {
		path          string
		alt           string
		accept        string
		authorization string
		body          map[string]any
	}
	requests := make(chan captured, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		requests <- captured{
			path:          r.URL.Path,
			alt:           r.URL.Query().Get("alt"),
			accept:        r.Header.Get("Accept"),
			authorization: r.Header.Get("Authorization"),
			body:          body,
		}
		serveSSE(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"Hello"},{"functionCall":{"name":"weather","args":{"city":"Paris"}}},{"text":"world"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":7,"thoughtsTokenCount":3}}`)
	}))
	defer server.Close()

	client := NewAgentLLM("test-key", server.URL, "gemini-3.5-pro", 4096, nil)
	messages := []llm.Message{
		llm.SystemMessage("sys one"),
		llm.SystemMessage("sys two"),
		llm.UserMessage(llm.Text("what is the weather?"), llm.ImageBase64("image/png", "aGk=")),
		llm.AssistantMessage([]llm.ContentPart{llm.Text("let me check")},
			llm.ToolCall{ID: "call-1", Type: llm.ToolTypeFunction,
				Function: llm.FunctionCall{Name: "weather", Arguments: `{"city":"Paris"}`}}),
		{Role: llm.RoleTool, ToolCallID: "call-1", Parts: []llm.ContentPart{llm.Text(`{"temp_c":21}`)}},
	}
	result, err := client.Execute(context.Background(), messages, []*llm.Tool{weatherTool(t)})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var req captured
	select {
	case req = <-requests:
	default:
		t.Fatal("no request reached the server")
	}
	if want := "/v1beta/models/gemini-3.5-pro:streamGenerateContent"; req.path != want {
		t.Fatalf("path = %q, want %q", req.path, want)
	}
	if req.alt != "sse" {
		t.Fatalf("alt = %q, want sse", req.alt)
	}
	if req.accept != "text/event-stream" {
		t.Fatalf("Accept = %q, want text/event-stream", req.accept)
	}
	// Bearer, never ?key=: the gateway rejects the query form.
	if req.authorization != "Bearer test-key" {
		t.Fatalf("Authorization = %q", req.authorization)
	}

	system, _ := req.body["systemInstruction"].(map[string]any)
	systemParts, _ := system["parts"].([]any)
	if len(systemParts) != 1 {
		t.Fatalf("systemInstruction.parts = %#v, want one merged part", systemParts)
	}
	if text, _ := systemParts[0].(map[string]any)["text"].(string); text != "sys one\n\nsys two" {
		t.Fatalf("systemInstruction text = %q", text)
	}

	contents, _ := req.body["contents"].([]any)
	if len(contents) != 3 {
		t.Fatalf("contents = %d turns, want 3: %#v", len(contents), contents)
	}
	first, _ := contents[0].(map[string]any)
	if first["role"] != "user" {
		t.Fatalf("contents[0].role = %v", first["role"])
	}
	firstParts, _ := first["parts"].([]any)
	if len(firstParts) != 2 {
		t.Fatalf("contents[0].parts = %#v, want text and inline data", firstParts)
	}
	if text, _ := firstParts[0].(map[string]any)["text"].(string); text != "what is the weather?" {
		t.Fatalf("contents[0].parts[0] = %#v", firstParts[0])
	}
	inline, _ := firstParts[1].(map[string]any)["inlineData"].(map[string]any)
	if inline["mimeType"] != "image/png" || inline["data"] != "aGk=" {
		t.Fatalf("inlineData = %#v", inline)
	}

	second, _ := contents[1].(map[string]any)
	if second["role"] != "model" {
		t.Fatalf("contents[1].role = %v, want the assistant's model role", second["role"])
	}
	secondParts, _ := second["parts"].([]any)
	if len(secondParts) != 2 {
		t.Fatalf("contents[1].parts = %#v, want text and functionCall", secondParts)
	}
	call, _ := secondParts[1].(map[string]any)["functionCall"].(map[string]any)
	if call["name"] != "weather" {
		t.Fatalf("functionCall = %#v", call)
	}
	args, _ := call["args"].(map[string]any)
	if args["city"] != "Paris" {
		t.Fatalf("functionCall.args = %#v, want the decoded arguments", args)
	}

	third, _ := contents[2].(map[string]any)
	if third["role"] != "user" {
		t.Fatalf("contents[2].role = %v, want a tool result to be a user turn", third["role"])
	}
	thirdParts, _ := third["parts"].([]any)
	response, _ := thirdParts[0].(map[string]any)["functionResponse"].(map[string]any)
	// The name comes from the call the assistant made; the result carries only
	// its id.
	if response["name"] != "weather" {
		t.Fatalf("functionResponse = %#v", response)
	}
	payload, _ := response["response"].(map[string]any)
	if payload["temp_c"] != float64(21) {
		t.Fatalf("functionResponse.response = %#v, want the tool's JSON", payload)
	}

	tools, _ := req.body["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %#v", req.body["tools"])
	}
	declarations, _ := tools[0].(map[string]any)["functionDeclarations"].([]any)
	if len(declarations) != 1 {
		t.Fatalf("functionDeclarations = %#v", declarations)
	}
	declaration, _ := declarations[0].(map[string]any)
	if declaration["name"] != "weather" || declaration["description"] != "report the current weather for a city" {
		t.Fatalf("declaration = %#v", declaration)
	}
	parameters, _ := declaration["parameters"].(map[string]any)
	if parameters["type"] != "object" {
		t.Fatalf("declaration.parameters = %#v, want the input schema", parameters)
	}

	config, _ := req.body["generationConfig"].(map[string]any)
	if config["maxOutputTokens"] != float64(4096) {
		t.Fatalf("generationConfig = %#v, want maxOutputTokens 4096", config)
	}

	if result == nil || result.Message == nil {
		t.Fatalf("result = %#v", result)
	}
	if len(result.Message.Parts) != 2 {
		t.Fatalf("text parts = %#v, want both text parts in order", result.Message.Parts)
	}
	if result.Message.Parts[0].Text != "Hello" || result.Message.Parts[1].Text != "world" {
		t.Fatalf("text parts = %#v, want the response's order", result.Message.Parts)
	}
	if len(result.Message.ToolCalls) != 1 {
		t.Fatalf("tool calls = %#v", result.Message.ToolCalls)
	}
	toolCall := result.Message.ToolCalls[0]
	if toolCall.Function.Name != "weather" || toolCall.Function.Arguments != `{"city":"Paris"}` {
		t.Fatalf("tool call = %#v", toolCall)
	}
	if toolCall.ID == "" || toolCall.Type != llm.ToolTypeFunction {
		t.Fatalf("tool call id/type = %q/%q, want a minted id and the function type", toolCall.ID, toolCall.Type)
	}
	if result.Usage == nil || result.Usage.InputTokens != 10 || result.Usage.OutputTokens != 10 {
		t.Fatalf("usage = %#v, want prompt 10 and candidates+thoughts 10", result.Usage)
	}
	if result.Usage.CacheReadInputTokens != 0 {
		t.Fatalf("cache read = %d, want 0: generateContent reports no cache reads here", result.Usage.CacheReadInputTokens)
	}
}

func TestExecuteSendsThroughTheInjectedTransport(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); got != "custom-agent/9" {
			t.Errorf("User-Agent = %q, want the injected transport's identity", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer gateway-token" {
			t.Errorf("Authorization = %q, want the transport's bearer to win", got)
		}
		serveSSE(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`)
	}))
	defer server.Close()

	// A transport standing in for the gateway's bearer injector: it overrides the
	// identity headers and delegates to the real network.
	transport := &headerTransport{base: http.DefaultTransport}
	client := NewAgentLLM("", server.URL, "gemini-3.5-pro", 0, transport)
	if _, err := client.Execute(context.Background(), []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if transport.seen != 1 {
		t.Fatalf("transport saw %d requests, want 1", transport.seen)
	}
}

// headerTransport injects the two headers an OpenCode transport sets and records
// that it ran.
type headerTransport struct {
	base http.RoundTripper
	mu   sync.Mutex
	seen int
}

func (h *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	h.mu.Lock()
	h.seen++
	h.mu.Unlock()
	clone := req.Clone(req.Context())
	clone.Header.Set("User-Agent", "custom-agent/9")
	clone.Header.Set("Authorization", "Bearer gateway-token")
	return h.base.RoundTrip(clone)
}

// Both error shapes, plus an error delivered with a 200, must produce one
// normalized error the surfaces can act on.
func TestExecuteNormalizesProviderErrors(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		body     string
		wantType string
		wantCode string
		wantMsg  string
	}{
		{
			name:    "gateway envelope",
			status:  http.StatusUnauthorized,
			body:    `{"type":"error","error":{"type":"AuthError","message":"Invalid credential"}}`,
			wantMsg: "Invalid credential",
		},
		{
			name:     "upstream passthrough",
			status:   http.StatusTooManyRequests,
			body:     `{"error":{"code":429,"status":"UNKNOWN","message":"quota exceeded"}}`,
			wantType: "UNKNOWN",
			wantCode: "429",
			wantMsg:  "quota exceeded",
		},
		{
			name:    "error with a 200 status",
			status:  http.StatusOK,
			body:    `{"error":{"code":500,"status":"INTERNAL","message":"upstream broke"}}`,
			wantMsg: "upstream broke",
		},
		{
			name:    "plain body",
			status:  http.StatusBadGateway,
			body:    "bad gateway",
			wantMsg: "bad gateway",
		},
	}
	for _, tc := range cases {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// A 200 streams: the in-band error arrives as one SSE event. Any
			// other status is read as a plain error body.
			if tc.status == http.StatusOK {
				serveSSE(w, tc.body)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))
		client := NewAgentLLM("test-key", server.URL, "gemini-3.5-pro", 0, nil)
		_, err := client.Execute(context.Background(), []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil)
		server.Close()
		if err == nil {
			t.Fatalf("%s: Execute succeeded", tc.name)
		}
		var apiErr *llm.APIError
		if !errors.As(err, &apiErr) {
			t.Fatalf("%s: error = %v, want a *llm.APIError", tc.name, err)
		}
		if apiErr.StatusCode != tc.status {
			t.Errorf("%s: status = %d, want %d", tc.name, apiErr.StatusCode, tc.status)
		}
		if !strings.Contains(apiErr.Message, tc.wantMsg) {
			t.Errorf("%s: message = %q, want it to contain %q", tc.name, apiErr.Message, tc.wantMsg)
		}
		if tc.wantType != "" && apiErr.Type != tc.wantType {
			t.Errorf("%s: type = %q, want %q", tc.name, apiErr.Type, tc.wantType)
		}
		if tc.wantCode != "" && apiErr.Code != tc.wantCode {
			t.Errorf("%s: code = %q, want %q", tc.name, apiErr.Code, tc.wantCode)
		}
	}
}

func TestExecuteRateLimitIsCarried(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"rate limited"}}`))
	}))
	defer server.Close()

	client := NewAgentLLM("", server.URL, "gemini-3.5-pro", 0, nil)
	_, err := client.Execute(context.Background(), []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil)
	var apiErr *llm.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want a *llm.APIError", err)
	}
	if apiErr.RateLimit == nil {
		t.Fatal("a 429 must carry its throttle detail")
	}
}

func TestExecuteRefusesImageURLs(t *testing.T) {
	client := NewAgentLLM("test-key", "http://127.0.0.1:1", "gemini-3.5-pro", 0, nil)
	_, err := client.Execute(context.Background(),
		[]llm.Message{llm.UserMessage(llm.ImageURL("https://example.com/shot.png"))}, nil)
	if err == nil || !strings.Contains(err.Error(), "image URLs are not supported") {
		t.Fatalf("error = %v, want a refusal rather than a dropped image", err)
	}
}

func TestExecuteFailsOnAnEmptyResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveSSE(w, `{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"SAFETY"}]}`)
	}))
	defer server.Close()

	client := NewAgentLLM("", server.URL, "gemini-3.5-pro", 0, nil)
	_, err := client.Execute(context.Background(), []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil)
	if err == nil || !strings.Contains(err.Error(), "SAFETY") {
		t.Fatalf("error = %v, want the refusal's finish reason", err)
	}
}

// A non-streaming response that arrived with usageMetadata but no answer the
// client can use (an empty turn) must leave that usage readable off the
// returned error: the failed attempt's tokens still belong on the bill.
func TestExecuteFailedAttemptCarriesItsUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The non-streaming endpoint: one JSON body, empty turn with billing.
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"SAFETY"}],"usageMetadata":{"promptTokenCount":140,"candidatesTokenCount":2}}`)
	}))
	defer server.Close()

	client := NewAgentLLM("", server.URL, "gemini-3.5-pro", 0, nil)
	ctx := llm.WithNonStreamingRequest(context.Background())
	_, err := client.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil)
	if err == nil {
		t.Fatal("an empty turn must fail")
	}
	usage := llm.UsageFromError(err)
	if usage == nil {
		t.Fatalf("error = %v, want the attempt's usage readable off it", err)
	}
	if usage.InputTokens != 140 || usage.OutputTokens != 2 {
		t.Fatalf("usage = %+v, want the usageMetadata the response carried", usage)
	}
}

func TestExecuteRequiresAModelAndAKnownRole(t *testing.T) {
	if _, err := NewAgentLLM("", "", "", 0, nil).Execute(context.Background(), nil, nil); err == nil {
		t.Fatal("Execute accepted an empty model")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("a malformed conversation must not reach the provider")
	}))
	defer server.Close()
	client := NewAgentLLM("", server.URL, "gemini-3.5-pro", 0, nil)
	if _, err := client.Execute(context.Background(), []llm.Message{{Role: "narrator"}}, nil); err == nil {
		t.Fatal("Execute accepted an unsupported role")
	}
}

// A tool that is missing its call id cannot be paired with anything, so it must
// be reported rather than sent as an unpaired response.
func TestToolResultRequiresACallID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("a malformed tool result must not reach the provider")
	}))
	defer server.Close()
	client := NewAgentLLM("", server.URL, "gemini-3.5-pro", 0, nil)
	_, err := client.Execute(context.Background(),
		[]llm.Message{{Role: llm.RoleTool, Parts: []llm.ContentPart{llm.Text("ok")}}}, nil)
	if err == nil || !strings.Contains(err.Error(), "tool_call_id") {
		t.Fatalf("error = %v, want a missing tool_call_id", err)
	}
}

// executeUnary keeps the non-streaming shape working on its own: the one-shot
// JSON answer parses into the same result the streaming path builds.
func TestExecuteUnaryParsesTheJSONBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if want := "/v1beta/models/gemini-3.5-pro:generateContent"; r.URL.Path != want {
			t.Errorf("path = %q, want %q", r.URL.Path, want)
		}
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q, want application/json", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"candidates": [{
				"content": {"role": "model", "parts": [
					{"text": "Hello"},
					{"functionCall": {"name": "weather", "args": {"city": "Paris"}}},
					{"text": "world"}
				]},
				"finishReason": "STOP"
			}],
			"usageMetadata": {"promptTokenCount": 10, "candidatesTokenCount": 7, "thoughtsTokenCount": 3}
		}`))
	}))
	defer server.Close()

	client := NewAgentLLM("test-key", server.URL, "gemini-3.5-pro", 4096, nil).(*agentLLM)
	result, finish, err := client.executeUnary(context.Background(), []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`))
	if err != nil {
		t.Fatalf("executeUnary: %v", err)
	}
	if finish != "STOP" {
		t.Fatalf("finish reason = %q, want STOP", finish)
	}
	if len(result.Message.Parts) != 2 ||
		result.Message.Parts[0].Text != "Hello" || result.Message.Parts[1].Text != "world" {
		t.Fatalf("text parts = %#v, want both in order", result.Message.Parts)
	}
	if len(result.Message.ToolCalls) != 1 {
		t.Fatalf("tool calls = %#v", result.Message.ToolCalls)
	}
	call := result.Message.ToolCalls[0]
	if call.Function.Name != "weather" || call.Function.Arguments != `{"city":"Paris"}` || call.ID == "" {
		t.Fatalf("tool call = %#v", call)
	}
	if result.Usage == nil || result.Usage.InputTokens != 10 || result.Usage.OutputTokens != 10 {
		t.Fatalf("usage = %#v, want prompt 10 and candidates+thoughts 10", result.Usage)
	}
}

// googleUnaryResponseBody is one generateContent answer carrying every piece
// a replay must deliver: two text parts around a function call, usage with
// thinking tokens and the finish reason.
const googleUnaryResponseBody = `{
	"candidates": [{
		"content": {"role": "model", "parts": [
			{"text": "Hello"},
			{"functionCall": {"name": "weather", "args": {"city": "Paris"}}},
			{"text": "world"}
		]},
		"finishReason": "STOP"
	}],
	"usageMetadata": {"promptTokenCount": 10, "candidatesTokenCount": 7, "thoughtsTokenCount": 3}
}`

// The non-streaming switch sends the request to :generateContent without the
// SSE query, and the result parses exactly as the streaming path's would.
func TestExecuteNonStreamingRequestUsesTheUnaryEndpoint(t *testing.T) {
	type captured struct {
		path string
		alt  string
	}
	requests := make(chan captured, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- captured{path: r.URL.Path, alt: r.URL.Query().Get("alt")}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(googleUnaryResponseBody))
	}))
	defer server.Close()

	client := NewAgentLLM("test-key", server.URL, "gemini-3.5-pro", 4096, nil)
	ctx := llm.WithNonStreamingRequest(context.Background())
	result, err := client.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var req captured
	select {
	case req = <-requests:
	default:
		t.Fatal("no request reached the server")
	}
	if !strings.HasSuffix(req.path, ":generateContent") {
		t.Fatalf("path = %q, want it to end with :generateContent", req.path)
	}
	if req.alt != "" {
		t.Fatalf("alt = %q, want no alt=sse on a non-streaming request", req.alt)
	}
	if len(result.Message.Parts) != 2 ||
		result.Message.Parts[0].Text != "Hello" || result.Message.Parts[1].Text != "world" {
		t.Fatalf("text parts = %#v, want both in order", result.Message.Parts)
	}
	if len(result.Message.ToolCalls) != 1 ||
		result.Message.ToolCalls[0].Function.Name != "weather" ||
		result.Message.ToolCalls[0].Function.Arguments != `{"city":"Paris"}` {
		t.Fatalf("tool calls = %#v", result.Message.ToolCalls)
	}
	if result.Usage == nil || result.Usage.InputTokens != 10 || result.Usage.OutputTokens != 10 {
		t.Fatalf("usage = %#v, want prompt 10 and candidates+thoughts 10", result.Usage)
	}
}

func TestExecuteNonStreamingResultIsReplayedToTheSink(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(googleUnaryResponseBody))
	}))
	defer server.Close()

	client := NewAgentLLM("test-key", server.URL, "gemini-3.5-pro", 4096, nil)
	rec := newRecordingSink()
	ctx := llm.WithNonStreamingRequest(llm.WithStreamSink(context.Background(), rec.sink()))
	result, err := client.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	want := []string{
		"started",
		"delta:Hello",
		"text-block-done",
		"delta:world",
		"text-block-done",
		"tool-ready:weather",
		"stop:STOP",
		"message-stop",
	}
	if !reflect.DeepEqual(rec.trace, want) {
		t.Fatalf("sink trace = %v\nwant           %v", rec.trace, want)
	}
	if len(rec.usage) != 1 || rec.usage[0] != [2]int{10, 10} {
		t.Fatalf("usage deltas = %v, want one 10/10", rec.usage)
	}
	if len(rec.snapshots) != 1 || rec.snapshots[0] != [2]int{10, 10} {
		t.Fatalf("usage snapshots = %v, want one 10/10", rec.snapshots)
	}
	if rec.ready == nil || len(result.Message.ToolCalls) != 1 ||
		!reflect.DeepEqual(*rec.ready, result.Message.ToolCalls[0]) {
		t.Fatalf("ready = %+v, final = %+v, want identical", rec.ready, result.Message.ToolCalls)
	}
	if !*rec.streamed {
		t.Fatal("Streamed was not set")
	}
	if rec.ends != 1 {
		t.Fatalf("OnEnd calls = %d, want 1", rec.ends)
	}
}

func TestExecuteNonStreamingTimeoutIsRetryable(t *testing.T) {
	orig := llm.NonStreamingTimeout
	llm.NonStreamingTimeout = 50 * time.Millisecond
	t.Cleanup(func() { llm.NonStreamingTimeout = orig })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(1 * time.Second)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(googleUnaryResponseBody))
	}))
	defer server.Close()

	client := NewAgentLLM("test-key", server.URL, "gemini-3.5-pro", 4096, nil)
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

// The non-streaming switch follows the same recovery: MAX_TOKENS empty rounds
// are re-asked, and the exhausted budget hands back the fragment.
func TestGoogleMaxTokensContinues(t *testing.T) {
	var mu sync.Mutex
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		round := requests
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch round {
		case 1:
			_, _ = w.Write([]byte(`{"candidates":[{"finishReason":"MAX_TOKENS"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":0,"thoughtsTokenCount":64}}`))
		case 2:
			_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"part2 "}]},"finishReason":"MAX_TOKENS"}],"usageMetadata":{"promptTokenCount":20,"candidatesTokenCount":5,"thoughtsTokenCount":0}}`))
		case 3:
			_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"part3 "}]},"finishReason":"MAX_TOKENS"}],"usageMetadata":{"promptTokenCount":30,"candidatesTokenCount":5,"thoughtsTokenCount":0}}`))
		default:
			_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"part4 "}]},"finishReason":"MAX_TOKENS"}],"usageMetadata":{"promptTokenCount":40,"candidatesTokenCount":5,"thoughtsTokenCount":0}}`))
		}
	}))
	defer server.Close()

	client := NewAgentLLM("", server.URL, "gemini-3.5-pro", 0, nil).(*agentLLM)
	ctx := llm.WithNonStreamingRequest(context.Background())
	res, err := client.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if requests != 1+llm.OutputLimitContinuations {
		t.Fatalf("requests = %d, want %d", requests, 1+llm.OutputLimitContinuations)
	}
	if text := res.Message.TextContent(); text != "part2 part3 part4 " {
		t.Fatalf("fragment = %q", text)
	}
	if res.StopReason != llm.StopReasonOutputLimit {
		t.Fatalf("StopReason = %q, want %q", res.StopReason, llm.StopReasonOutputLimit)
	}
}

// An empty turn from any finish reason other than MAX_TOKENS is still a
// refusal to answer, and stays an error after exactly one request.
func TestGoogleSafetyEmptyStillErrors(t *testing.T) {
	var mu sync.Mutex
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"finishReason":"SAFETY"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":0,"thoughtsTokenCount":0}}`))
	}))
	defer server.Close()

	client := NewAgentLLM("", server.URL, "gemini-3.5-pro", 0, nil).(*agentLLM)
	ctx := llm.WithNonStreamingRequest(context.Background())
	_, err := client.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil)
	if err == nil {
		t.Fatal("an empty SAFETY response must stay an error")
	}
	if !strings.Contains(err.Error(), `google: response was empty (finish reason "SAFETY")`) {
		t.Fatalf("error = %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if requests != 1 {
		t.Fatalf("requests = %d, want exactly 1", requests)
	}
}

// The non-streaming switch follows plans/273: a STOP that billed output yet
// delivered nothing carries the blank-output sentinel, so the resend layer
// above re-sends it; the continuation loop does not adopt a STOP.
func TestGoogleStopEmptyIsBlankOutput(t *testing.T) {
	var mu sync.Mutex
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":0,"thoughtsTokenCount":688}}`))
	}))
	defer server.Close()

	client := NewAgentLLM("", server.URL, "gemini-3.5-pro", 0, nil).(*agentLLM)
	ctx := llm.WithNonStreamingRequest(context.Background())
	_, err := client.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil)
	if !errors.Is(err, llm.ErrBlankModelOutput) {
		t.Fatalf("error = %v, want the blank-output sentinel", err)
	}
	if !strings.Contains(err.Error(), "response was empty") {
		t.Fatalf("error = %v, want the empty-response wording kept", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if requests != 1 {
		t.Fatalf("requests = %d, want exactly 1", requests)
	}
}

// A reply that was only reasoning is a fence, and the fence reaches the model
// that produced it as that text. No other model sees it: it would read that
// reasoning as an answer this conversation gave.
func TestGoogleSendsThinkingFenceOnlyToItsProducer(t *testing.T) {
	fenceText := llm.ThinkingFence("weigh A against B")
	fence := llm.Message{
		Role:           llm.RoleAssistant,
		Parts:          []llm.ContentPart{llm.Text(fenceText)},
		ReasoningModel: "gemini-3.5-pro",
	}
	req, err := buildRequest([]llm.Message{llm.UserMessage(llm.Text("hi")), fence}, nil, 0, "gemini-3.5-pro")
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if len(req.Contents) != 2 || req.Contents[1].Role != roleModel {
		t.Fatalf("contents = %#v, want the prompt and the fence", req.Contents)
	}
	if parts := req.Contents[1].Parts; len(parts) != 1 || parts[0].Text != fenceText {
		t.Fatalf("fence parts = %#v, want the fence text alone", parts)
	}

	for _, tc := range []struct {
		name     string
		model    string
		producer string
	}{
		{name: "another model", model: "glm-5.3", producer: "gemini-3.5-pro"},
		{name: "no producer", model: "gemini-3.5-pro"},
		{name: "no requested model", producer: "gemini-3.5-pro"},
	} {
		history := fence
		history.ReasoningModel = tc.producer
		req, err := buildRequest([]llm.Message{llm.UserMessage(llm.Text("hi")), history}, nil, 0, tc.model)
		if err != nil {
			t.Fatalf("%s: buildRequest: %v", tc.name, err)
		}
		if len(req.Contents) != 1 || req.Contents[0].Role != roleUser {
			t.Fatalf("%s: contents = %#v, want the prompt alone", tc.name, req.Contents)
		}
	}
}

// Every part the request builder emits serializes as it did before the
// signature fields existed, except the one shape that had no valid form: a
// part carrying a signature and no other field, which must say it is text.
func TestGooglePartJSON(t *testing.T) {
	for _, tc := range []struct {
		name string
		part part
		want map[string]any
	}{
		{
			name: "signature only",
			part: part{ThoughtSignature: "S"},
			want: map[string]any{"text": "", "thoughtSignature": "S"},
		},
		{
			name: "text",
			part: part{Text: "hi"},
			want: map[string]any{"text": "hi"},
		},
		{
			name: "call with a signature",
			part: part{FunctionCall: &functionCall{Name: "f"}, ThoughtSignature: "S"},
			want: map[string]any{"functionCall": map[string]any{"name": "f"}, "thoughtSignature": "S"},
		},
		{
			name: "thought summary",
			part: part{Text: "t", Thought: true, ThoughtSignature: "S"},
			want: map[string]any{"text": "t", "thought": true, "thoughtSignature": "S"},
		},
		{
			name: "empty thought summary",
			part: part{Thought: true},
			want: map[string]any{"text": "", "thought": true},
		},
	} {
		raw, err := json.Marshal(tc.part)
		if err != nil {
			t.Fatalf("%s: marshal: %v", tc.name, err)
		}
		var got map[string]any
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("%s: unmarshal %s: %v", tc.name, raw, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s: json = %s, want %#v", tc.name, raw, tc.want)
		}
		if tc.name == "signature only" && !strings.Contains(string(raw), `"text":""`) {
			t.Fatalf("signature-only part = %s, want an explicit empty text field", raw)
		}
	}
}

// A reply's thought summaries and signatures are kept in arrival order: the
// summary is reasoning the surface shows as thinking, and every part keeps the
// signature it came with so this reply can be given back to its model intact.
func TestGoogleUnaryKeepsThoughtSignatures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"candidates": [{
				"content": {"role": "model", "parts": [
					{"text": "plan", "thought": true, "thoughtSignature": "A"},
					{"text": "ok"},
					{"functionCall": {"name": "weather", "args": {"city": "Paris"}}, "thoughtSignature": "B"},
					{"functionCall": {"name": "clock"}},
					{"text": "", "thoughtSignature": "C"}
				]},
				"finishReason": "STOP"
			}],
			"usageMetadata": {"promptTokenCount": 10, "candidatesTokenCount": 7}
		}`))
	}))
	defer server.Close()

	client := NewAgentLLM("", server.URL, "gemini-3.5-pro", 0, nil)
	rec := newRecordingSink()
	ctx := llm.WithNonStreamingRequest(llm.WithStreamSink(context.Background(), rec.sink()))
	result, err := client.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if text := result.Message.TextContent(); text != "ok" {
		t.Fatalf("answer = %q, want the answer text alone", text)
	}
	if len(result.Message.ToolCalls) != 2 {
		t.Fatalf("tool calls = %#v, want both", result.Message.ToolCalls)
	}
	if result.Message.ReasoningModel != "gemini-3.5-pro" {
		t.Fatalf("ReasoningModel = %q, want the client's model", result.Message.ReasoningModel)
	}
	layout, ok := llm.DecodeGeminiParts(result.Message.Name)
	if !ok {
		t.Fatalf("carry = %q, want a Gemini part layout", result.Message.Name)
	}
	want := []llm.GeminiPart{
		{Kind: "thought", Text: "plan", Signature: "A"},
		{Kind: "text", Text: "ok"},
		{Kind: "call", Call: 0, Signature: "B"},
		{Kind: "call", Call: 1},
		{Kind: "text", Signature: "C"},
	}
	if !reflect.DeepEqual(layout, want) {
		t.Fatalf("layout = %#v\nwant      %#v", layout, want)
	}
	// The non-streaming replay shows the thought summary as thinking and the
	// answer as the answer.
	if fmt.Sprint(rec.reasoning) != fmt.Sprint([]string{"plan"}) || rec.reasoningDone != 1 {
		t.Fatalf("reasoning = %v (done %d), want the thought summary once", rec.reasoning, rec.reasoningDone)
	}
	if fmt.Sprint(rec.trace) != fmt.Sprint([]string{"started", "delta:ok", "text-block-done", "tool-ready:weather", "tool-ready:clock", "stop:STOP", "message-stop"}) {
		t.Fatalf("trace = %v, want the thought summary outside the answer text", rec.trace)
	}
}

// googleSignedLayout is the reply the replay tests below send back: one thought
// summary, one answer text, two calls (the first signed), and a signature on its
// own empty text part.
func googleSignedLayout() []llm.GeminiPart {
	return []llm.GeminiPart{
		{Kind: "thought", Text: "plan", Signature: "A"},
		{Kind: "text", Text: "ok"},
		{Kind: "call", Call: 0, Signature: "B"},
		{Kind: "call", Call: 1},
		{Kind: "text", Signature: "C"},
	}
}

// googleSignedReply is the assistant message those parts belong to: the answer
// text and the two calls the layout indexes.
func googleSignedReply(producer string) llm.Message {
	return llm.Message{
		Role:  llm.RoleAssistant,
		Parts: []llm.ContentPart{llm.Text("ok")},
		ToolCalls: []llm.ToolCall{
			{ID: "call-a", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "weather", Arguments: `{"city":"Paris"}`}},
			{ID: "call-b", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "clock"}},
		},
		Name:           llm.EncodeGeminiParts(googleSignedLayout()),
		ReasoningModel: producer,
	}
}

// googleSignedHistory is the reply plus the tool results it asked for, which is
// what the next request of the same turn carries.
func googleSignedHistory(producer string) []llm.Message {
	reply := googleSignedReply(producer)
	return []llm.Message{
		llm.UserMessage(llm.Text("hi")),
		reply,
		{Role: llm.RoleTool, ToolCallID: "call-a", Parts: []llm.ContentPart{llm.Text(`{"temp_c":21}`)}},
		{Role: llm.RoleTool, ToolCallID: "call-b", Parts: []llm.ContentPart{llm.Text("12:00")}},
	}
}

// The reply goes back to the model that wrote it exactly as it returned it, and
// to no other model: the signatures are that model's reasoning, and another
// model neither wrote them nor can verify them.
func TestGoogleReplaysLayoutOnlyToItsProducer(t *testing.T) {
	req, err := buildRequest(googleSignedHistory("gemini-3.5-pro"), nil, 0, "gemini-3.5-pro")
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if len(req.Contents) < 2 || req.Contents[1].Role != roleModel {
		t.Fatalf("contents = %#v, want the prompt then the reply", req.Contents)
	}
	wantParts := []part{
		{Text: "plan", Thought: true, ThoughtSignature: "A"},
		{Text: "ok"},
		{FunctionCall: &functionCall{Name: "weather", Args: map[string]any{"city": "Paris"}}, ThoughtSignature: "B"},
		{FunctionCall: &functionCall{Name: "clock", Args: map[string]any{}}},
		{ThoughtSignature: "C"},
	}
	if !reflect.DeepEqual(req.Contents[1].Parts, wantParts) {
		t.Fatalf("producer parts = %#v\nwant            %#v", req.Contents[1].Parts, wantParts)
	}

	// Another model gets the answer and the calls, no reasoning, and the
	// placeholder on the current turn's first call so Gemini 3 and later still
	// accept the request.
	wantOther := []part{
		{Text: "ok"},
		{FunctionCall: &functionCall{Name: "weather", Args: map[string]any{"city": "Paris"}}, ThoughtSignature: geminiSkipSignatureValidator},
		{FunctionCall: &functionCall{Name: "clock", Args: map[string]any{}}},
	}
	for _, tc := range []struct {
		name     string
		model    string
		producer string
	}{
		{name: "another model", model: "glm-5.3", producer: "gemini-3.5-pro"},
		{name: "no producer", model: "gemini-3.5-pro"},
	} {
		req, err := buildRequest(googleSignedHistory(tc.producer), nil, 0, tc.model)
		if err != nil {
			t.Fatalf("%s: buildRequest: %v", tc.name, err)
		}
		if len(req.Contents) < 2 || req.Contents[1].Role != roleModel {
			t.Fatalf("%s: contents = %#v, want the prompt then the reply", tc.name, req.Contents)
		}
		if !reflect.DeepEqual(req.Contents[1].Parts, wantOther) {
			t.Fatalf("%s: parts = %#v\nwant            %#v", tc.name, req.Contents[1].Parts, wantOther)
		}
	}
}

// The placeholder signature covers exactly one case: the current turn's first
// call of a model content that has none. Earlier turns are history Google does
// not validate, and a call with its own signature keeps it.
func TestGoogleCurrentTurnUnsignedCallGetsPlaceholder(t *testing.T) {
	call := func(id, name string) llm.ToolCall {
		return llm.ToolCall{ID: id, Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: name}}
	}
	history := []llm.Message{
		llm.UserMessage(llm.Text("hi")),
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{call("c1", "weather"), call("c2", "clock")}},
		{Role: llm.RoleTool, ToolCallID: "c1", Parts: []llm.ContentPart{llm.Text(`{"temp_c":21}`)}},
		{Role: llm.RoleTool, ToolCallID: "c2", Parts: []llm.ContentPart{llm.Text("12:00")}},
		llm.UserMessage(llm.Text("and now?")),
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{call("c3", "weather"), call("c4", "clock")}},
		{Role: llm.RoleTool, ToolCallID: "c3", Parts: []llm.ContentPart{llm.Text(`{"temp_c":22}`)}},
	}
	req, err := buildRequest(history, nil, 0, "gemini-3.5-pro")
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	var modelContents []content
	for _, c := range req.Contents {
		if c.Role == roleModel {
			modelContents = append(modelContents, c)
		}
	}
	if len(modelContents) != 2 {
		t.Fatalf("model contents = %#v, want the two replies", modelContents)
	}
	for i, p := range modelContents[0].Parts {
		if p.ThoughtSignature != "" {
			t.Fatalf("earlier turn call %d got signature %q, want it untouched", i, p.ThoughtSignature)
		}
	}
	if got := modelContents[1].Parts[0].ThoughtSignature; got != geminiSkipSignatureValidator {
		t.Fatalf("current turn first call signature = %q, want the placeholder", got)
	}
	if got := modelContents[1].Parts[1].ThoughtSignature; got != "" {
		t.Fatalf("current turn second call signature = %q, want it untouched", got)
	}
}

// A tool round is one model content and one user content: Gemini refuses a
// request whose function-call turn does not hold exactly as many
// functionResponse parts as functionCall parts ("Please ensure that the number
// of function response parts is equal to the number of function call parts of
// the function call turn"). Parallel calls arrive here as one result message
// each, so the results must share the user content that answers that turn —
// while a user message with text of its own keeps a content of its own.
func TestGoogleProjectsOneToolRoundIntoOneUserContent(t *testing.T) {
	call := func(id, name string) llm.ToolCall {
		return llm.ToolCall{ID: id, Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: name}}
	}
	history := []llm.Message{
		llm.UserMessage(llm.Text("go")),
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{call("c1", "weather"), call("c2", "clock")}},
		{Role: llm.RoleTool, ToolCallID: "c1", Parts: []llm.ContentPart{llm.Text(`{"temp_c":21}`)}},
		{Role: llm.RoleTool, ToolCallID: "c2", Parts: []llm.ContentPart{llm.Text("12:00")}},
		llm.UserMessage(llm.Text("steer")),
	}
	req, err := buildRequest(history, nil, 0, "gemini-3.5-pro")
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if len(req.Contents) != 4 {
		t.Fatalf("contents = %#v, want the prompt, the reply, one user turn of results and the steer", req.Contents)
	}
	if got, want := []string{req.Contents[0].Role, req.Contents[1].Role, req.Contents[2].Role, req.Contents[3].Role}, []string{roleUser, roleModel, roleUser, roleUser}; !reflect.DeepEqual(got, want) {
		t.Fatalf("roles = %v, want %v", got, want)
	}
	calls, results := req.Contents[1].Parts, req.Contents[2].Parts
	if len(calls) != 2 || len(results) != 2 {
		t.Fatalf("model holds %d parts, the user turn %d; want the two calls answered by the two results", len(calls), len(results))
	}
	for i, wantName := range []string{"weather", "clock"} {
		if calls[i].FunctionCall == nil || calls[i].FunctionCall.Name != wantName {
			t.Fatalf("model part %d = %#v, want the %s call", i, calls[i], wantName)
		}
		if results[i].FunctionResponse == nil || results[i].FunctionResponse.Name != wantName {
			t.Fatalf("user part %d = %#v, want the %s result", i, results[i], wantName)
		}
	}
	// The steer is the conversation's own words: it keeps a content of its
	// own rather than being absorbed into the results.
	var steer []string
	for _, c := range req.Contents {
		if c.Role != roleUser || allToolResults(c.Parts) {
			continue
		}
		for _, p := range c.Parts {
			if p.Text != "" {
				steer = append(steer, p.Text)
			}
		}
	}
	if want := []string{"go", "steer"}; !reflect.DeepEqual(steer, want) {
		t.Fatalf("user text parts = %v, want %v: the conversation's own words keep a content of their own", steer, want)
	}
}

// The gateway's own rule, enforced on the wire: a function-call turn must be
// answered by exactly one functionResponse per functionCall in the content that
// follows it, which is the 400 a parallel round used to earn ("Please ensure
// that the number of function response parts is equal to the number of function
// call parts of the function call turn"). A single-call round keeps its own one
// result — the merge is a round's, not every user content's.
func TestGoogleGatewayAcceptsOneToolRoundAsOneUserContent(t *testing.T) {
	call := func(id, name string) llm.ToolCall {
		return llm.ToolCall{ID: id, Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: name}}
	}
	type wireContent struct {
		Role  string           `json:"role"`
		Parts []map[string]any `json:"parts"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Contents []wireContent `json:"contents"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		for i, c := range body.Contents {
			if c.Role != "model" {
				continue
			}
			calls := 0
			for _, p := range c.Parts {
				if _, ok := p["functionCall"]; ok {
					calls++
				}
			}
			if calls == 0 {
				continue
			}
			responses := 0
			if i+1 < len(body.Contents) && body.Contents[i+1].Role == "user" {
				for _, p := range body.Contents[i+1].Parts {
					if _, ok := p["functionResponse"]; ok {
						responses++
					}
				}
			}
			if responses != calls {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"Please ensure that the number of function response parts is equal to the number of function call parts of the function call turn."}}`)
				return
			}
		}
		serveSSE(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"both"}]},"finishReason":"STOP"}]}`)
	}))
	defer server.Close()

	client := NewAgentLLM("test-key", server.URL, "gemini-3.5-pro", 4096, nil)
	for _, tc := range []struct {
		name    string
		calls   []llm.ToolCall
		results []llm.Message
	}{
		{
			name:  "parallel round",
			calls: []llm.ToolCall{call("c1", "weather"), call("c2", "clock")},
			results: []llm.Message{
				{Role: llm.RoleTool, ToolCallID: "c1", Parts: []llm.ContentPart{llm.Text(`{"temp_c":21}`)}},
				{Role: llm.RoleTool, ToolCallID: "c2", Parts: []llm.ContentPart{llm.Text("12:00")}},
			},
		},
		{
			name:  "single call",
			calls: []llm.ToolCall{call("c1", "weather")},
			results: []llm.Message{
				{Role: llm.RoleTool, ToolCallID: "c1", Parts: []llm.ContentPart{llm.Text(`{"temp_c":21}`)}},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			history := append([]llm.Message{
				llm.UserMessage(llm.Text("go")),
				{Role: llm.RoleAssistant, ToolCalls: tc.calls},
			}, tc.results...)
			res, err := client.Execute(context.Background(), history, nil)
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if got := res.Message.TextContent(); got != "both" {
				t.Fatalf("answer = %q, want the gateway's reply", got)
			}
		})
	}
}

// A thought summary is never an answer: it is not answer text, it is not what a
// surface renders as the reply, and another model never receives it.
func TestGoogleThoughtSummaryIsNeverAnswer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"candidates": [{
				"content": {"role": "model", "parts": [
					{"text": "secret plan", "thought": true, "thoughtSignature": "A"},
					{"functionCall": {"name": "weather", "args": {"city": "Paris"}}}
				]},
				"finishReason": "STOP"
			}]
		}`))
	}))
	defer server.Close()

	client := NewAgentLLM("", server.URL, "gemini-3.5-pro", 0, nil)
	ctx := llm.WithNonStreamingRequest(context.Background())
	result, err := client.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(result.Message.Parts) != 0 || result.Message.TextContent() != "" {
		t.Fatalf("parts = %#v, want no answer text from a thought summary", result.Message.Parts)
	}
	layout, ok := llm.DecodeGeminiParts(result.Message.Name)
	if !ok || len(layout) != 2 || layout[0].Kind != "thought" || layout[0].Text != "secret plan" {
		t.Fatalf("carry = %q, want the thought summary kept for its own model", result.Message.Name)
	}

	history := []llm.Message{llm.UserMessage(llm.Text("hi")), *result.Message}
	req, err := buildRequest(history, nil, 0, "glm-5.3")
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, unwanted := range []string{"secret plan", `"thought":true`, `"signature":"A"`} {
		if strings.Contains(string(raw), unwanted) {
			t.Fatalf("request leaked %q to another model: %s", unwanted, raw)
		}
	}
}

// A continued answer is one reply assembled from several rounds: its text is
// unsigned (no signature was issued for the merge), while the last round's calls
// keep their signatures, because a current-turn call without one is refused. A
// last round cut by the output limit carries nothing at all.
func TestGoogleContinuationKeepsCallSignatures(t *testing.T) {
	serve := func(t *testing.T, lastRound string) *llm.Result {
		t.Helper()
		rounds := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rounds++
			w.Header().Set("Content-Type", "application/json")
			if rounds == 1 {
				_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"Half"}]},"finishReason":"MAX_TOKENS"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2}}`))
				return
			}
			_, _ = w.Write([]byte(lastRound))
		}))
		t.Cleanup(server.Close)
		client := NewAgentLLM("", server.URL, "gemini-3.5-pro", 0, nil)
		ctx := llm.WithNonStreamingRequest(context.Background())
		result, err := client.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil)
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		return result
	}

	t.Run("stop with a signed call", func(t *testing.T) {
		result := serve(t, `{"candidates":[{"content":{"role":"model","parts":[{"text":" rest"},{"functionCall":{"name":"weather"},"thoughtSignature":"S"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":20,"candidatesTokenCount":5}}`)
		if text := result.Message.TextContent(); text != "Half rest" {
			t.Fatalf("answer = %q, want both rounds", text)
		}
		layout, ok := llm.DecodeGeminiParts(result.Message.Name)
		if !ok {
			t.Fatalf("carry = %q, want the merged layout", result.Message.Name)
		}
		want := []llm.GeminiPart{
			{Kind: "text", Text: "Half rest"},
			{Kind: "call", Call: 0, Signature: "S"},
		}
		if !reflect.DeepEqual(layout, want) {
			t.Fatalf("layout = %#v\nwant      %#v", layout, want)
		}
		if result.Message.ReasoningModel != "gemini-3.5-pro" {
			t.Fatalf("ReasoningModel = %q, want the model copied from the last round", result.Message.ReasoningModel)
		}
	})

	t.Run("exhausted on the output limit", func(t *testing.T) {
		result := serve(t, `{"candidates":[{"content":{"role":"model","parts":[{"text":" and more"},{"functionCall":{"name":"weather"},"thoughtSignature":"S"}]},"finishReason":"MAX_TOKENS"}],"usageMetadata":{"promptTokenCount":20,"candidatesTokenCount":5}}`)
		if result.StopReason != llm.StopReasonOutputLimit {
			t.Fatalf("StopReason = %q, want the output limit", result.StopReason)
		}
		if result.Message.Name != "" || result.Message.ReasoningModel != "" {
			t.Fatalf("carry = %q (producer %q), want none on an unfinished answer", result.Message.Name, result.Message.ReasoningModel)
		}
	})
}

// An interrupted reply only recorded the prefix of itself in its layout: the
// answer text still open at the cut was sealed by the engine afterwards, and a
// call the adapter never got to append is missing from it. Both go after the
// layout, in the message's own order, without a signature — none ever arrived
// for them, and the placeholder covers only the current turn's first call.
func TestGoogleReplaysALayoutPrefixAndAppendsTheRest(t *testing.T) {
	layout := []llm.GeminiPart{
		{Kind: "text", Text: "first", Signature: "S1"},
		{Kind: "call", Call: 0, Signature: "S2"},
	}
	reply := llm.Message{
		Role:  llm.RoleAssistant,
		Parts: []llm.ContentPart{llm.Text("first"), llm.Text("second")},
		ToolCalls: []llm.ToolCall{
			{ID: "call-a", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "weather", Arguments: `{"city":"Paris"}`}},
			{ID: "call-b", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "clock"}},
		},
		Name:           llm.EncodeGeminiParts(layout),
		ReasoningModel: "gemini-3.5-pro",
	}
	req, err := buildRequest([]llm.Message{llm.UserMessage(llm.Text("hi")), reply}, nil, 0, "gemini-3.5-pro")
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if len(req.Contents) < 2 || req.Contents[1].Role != roleModel {
		t.Fatalf("contents = %#v, want the prompt then the reply", req.Contents)
	}
	want := []part{
		{Text: "first", ThoughtSignature: "S1"},
		{FunctionCall: &functionCall{Name: "weather", Args: map[string]any{"city": "Paris"}}, ThoughtSignature: "S2"},
		{Text: "second"},
		{FunctionCall: &functionCall{Name: "clock", Args: map[string]any{}}},
	}
	if !reflect.DeepEqual(req.Contents[1].Parts, want) {
		t.Fatalf("parts = %#v\nwant    %#v", req.Contents[1].Parts, want)
	}
}

// googleSignatureRefusal answers the way Google does when it will not accept the
// thought signatures it was given.
func googleSignatureRefusal(message string) string {
	payload, _ := json.Marshal(map[string]any{"error": map[string]any{
		"code":    400,
		"message": message,
		"status":  "INVALID_ARGUMENT",
	}})
	return string(payload)
}

// googleCapturedReasoning reads what a captured request replays of the model's
// reasoning: every thought signature it sends, and whether it sends a thought
// summary.
func googleCapturedReasoning(body map[string]any) (signatures []string, thought bool) {
	contents, _ := body["contents"].([]any)
	for _, c := range contents {
		entry, _ := c.(map[string]any)
		parts, _ := entry["parts"].([]any)
		for _, p := range parts {
			part, _ := p.(map[string]any)
			if signature, ok := part["thoughtSignature"].(string); ok && signature != "" {
				signatures = append(signatures, signature)
			}
			if asBool, ok := part["thought"].(bool); ok && asBool {
				thought = true
			}
		}
	}
	return signatures, thought
}

// The signatures in a reply belong to the model and account that wrote them:
// once Google refuses them, every request of the conversation carries the same
// ones and fails the same way. The refusal buys one resend without them, and the
// current turn's first call still gets the placeholder that keeps a signature-less
// call acceptable — otherwise the retry would fail too.
func TestGoogleResendsWithoutSignaturesWhenRefused(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		bodies = append(bodies, body)
		if len(bodies) == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, googleSignatureRefusal("Thought signature is not valid for this request"))
			return
		}
		serveSSE(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":2}}`)
	}))
	defer server.Close()

	client := NewAgentLLM("", server.URL, "gemini-3.5-pro", 0, nil)
	ends := 0
	ctx := llm.WithStreamSink(context.Background(), &llm.StreamSink{OnEnd: func() { ends++ }})
	result, err := client.Execute(ctx, googleSignedHistory("gemini-3.5-pro"), []*llm.Tool{weatherTool(t)})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(bodies) != 2 {
		t.Fatalf("requests = %d, want the refusal and one resend", len(bodies))
	}
	if signatures, thought := googleCapturedReasoning(bodies[0]); !thought || len(signatures) == 0 {
		t.Fatalf("first request replayed signatures %v (thought %v), want the reply's own layout", signatures, thought)
	}
	signatures, thought := googleCapturedReasoning(bodies[1])
	if thought {
		t.Fatalf("resend still sent a thought summary: %v", signatures)
	}
	for _, unwanted := range []string{"A", "B", "C"} {
		for _, signature := range signatures {
			if signature == unwanted {
				t.Fatalf("resend still sent the refused signature %q: %v", unwanted, signatures)
			}
		}
	}
	placeholders := 0
	for _, signature := range signatures {
		if signature == geminiSkipSignatureValidator {
			placeholders++
		}
	}
	if placeholders != 1 {
		t.Fatalf("resend carried %d placeholder signatures %v, want exactly the current turn's first call", placeholders, signatures)
	}
	if got := result.Message.TextContent(); got != "ok" {
		t.Fatalf("answer = %q, want the resend's own answer", got)
	}
	if ends != 1 {
		t.Fatalf("OnEnd calls = %d, want 1: one Execute is one reply", ends)
	}
}

// Only a refusal of the replayed signatures buys a resend: any other failure,
// and a signature-shaped failure on a request that replayed no reasoning, is the
// provider's answer as it stands.
func TestGoogleDoesNotResendUnrelatedFailures(t *testing.T) {
	tests := []struct {
		name     string
		messages []llm.Message
		message  string
	}{
		{
			name:     "a 400 that does not name the signature",
			messages: googleSignedHistory("gemini-3.5-pro"),
			message:  "API key not valid. Please pass a valid API key.",
		},
		{
			name:     "a signature 400 on a request that replayed none",
			messages: []llm.Message{llm.UserMessage(llm.Text("hi"))},
			message:  "Thought signature is not valid for this request",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests++
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, googleSignatureRefusal(tc.message))
			}))
			defer server.Close()

			client := NewAgentLLM("", server.URL, "gemini-3.5-pro", 0, nil)
			if _, err := client.Execute(context.Background(), tc.messages, nil); err == nil {
				t.Fatal("Execute succeeded, want the provider's 400")
			}
			if requests != 1 {
				t.Fatalf("requests = %d, want 1: this failure is not the signatures'", requests)
			}
		})
	}
}

// Audio and video travel as inline_data, the same shape an image has:
// mime_type plus the bytes. A PDF is refused, because generateContent's
// document input has no official shape this build could verify.
func TestGoogleUserPartsCarryAudioAndVideoInline(t *testing.T) {
	for _, tc := range []struct{ mime, b64 string }{
		{"audio/mpeg", "SUQzBAAAAA=="},
		{"video/mp4", "AAAAIGZ0eXA="},
	} {
		parts, err := userParts([]llm.ContentPart{llm.Text("look"), llm.FileBase64("clip", tc.mime, tc.b64)})
		if err != nil {
			t.Fatalf("userParts(%s): %v", tc.mime, err)
		}
		if len(parts) != 2 || parts[1].InlineData == nil {
			t.Fatalf("parts = %#v, want text + inline_data", parts)
		}
		if parts[1].InlineData.MIMEType != tc.mime || parts[1].InlineData.Data != tc.b64 {
			t.Fatalf("inline_data = %#v, want %s and its bytes", parts[1].InlineData, tc.mime)
		}
	}
}

func TestGoogleUserPartsRefuseAPDF(t *testing.T) {
	if _, err := userParts([]llm.ContentPart{llm.FileBase64("demo.pdf", "application/pdf", "JVBERi0=")}); err == nil {
		t.Fatal("a PDF must be refused: no official inline shape was verified for it")
	}
}
