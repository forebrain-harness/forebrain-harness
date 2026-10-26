package google

import (
	"context"
	"encoding/json"
	"errors"
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
