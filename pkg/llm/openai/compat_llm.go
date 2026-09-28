package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	openaiclient "github.com/sashabaranov/go-openai"
)

const (
	defaultOpenAIChatModel           = "gpt-4o-mini"
	DefaultMaxCompletionTokens       = 8192
	openAILengthContinueMax          = 16
	defaultOpenAIChatCompletionsPath = "/chat/completions"
	openAIReasoningCarryPrefix       = "!!FOREBRAIN_RCv1!!"
	providerErrorBodyLimit           = 1 << 20
)

var errOpenAINoChoices = errors.New("openai: response contained no choices")

func isOpenAILengthTruncatedFinish(fr openaiclient.FinishReason) bool {
	s := strings.ToLower(strings.TrimSpace(string(fr)))
	switch s {
	case string(openaiclient.FinishReasonLength), "max_tokens":
		return true
	default:
		return false
	}
}

func openAIReasoningLengthShouldContinue(fr openaiclient.FinishReason, reasoningLen, contentLen, toolCalls int) bool {
	if toolCalls > 0 {
		return false
	}
	if !isOpenAILengthTruncatedFinish(fr) {
		return false
	}
	return reasoningLen > 0 || contentLen > 0
}

type openAICompatLLM struct {
	client      *openaiclient.Client
	model       string
	temperature float32
	maxTokens   int
	paramsJSON  []byte
}

func NewCompatLLMWithPromptCaching(apiKey, baseURL, model string, temperature float32, maxTokens int, apiPath string, paramsJSON []byte, promptCaching bool) llm.LLM {
	if maxTokens <= 0 {
		maxTokens = DefaultMaxCompletionTokens
	}
	if strings.TrimSpace(model) == "" {
		model = defaultOpenAIChatModel
	}
	topVal, topOK := topLevelEnableThinkingFromParamsJSON(paramsJSON)
	cfg := openaiclient.DefaultConfig(apiKey)
	if baseURL != "" {
		cfg.BaseURL = baseURL
	}
	if hc, ok := cfg.HTTPClient.(*http.Client); ok {
		if hc.Transport == nil {
			hc.Transport = http.DefaultTransport
		}
		hc.Transport = llm.WrapHTTPTransport(hc.Transport)
		hc.Transport = &normalizeOpenAICompatReasoningRoundTripper{inner: hc.Transport}
		if normalized := normalizeOpenAIChatAPIPath(apiPath); normalized != "" && normalized != defaultOpenAIChatCompletionsPath {
			hc.Transport = &rewriteOpenAIChatPathRoundTripper{inner: hc.Transport, apiPath: normalized}
		}
		hc.Transport = &enableThinkingChatRoundTripper{inner: hc.Transport, staticVal: topVal, staticOK: topOK}
		if promptCaching {
			hc.Transport = &openAIPromptCacheKeyRoundTripper{inner: hc.Transport}
		}
		hc.Transport = &llm.CacheUsageTransport{Next: hc.Transport}
	}
	return &openAICompatLLM{
		client:      openaiclient.NewClientWithConfig(cfg),
		model:       model,
		temperature: temperature,
		maxTokens:   maxTokens,
		paramsJSON:  append([]byte(nil), paramsJSON...),
	}
}

// openAIPromptCacheKeyRoundTripper fills a field missing from the legacy
// go-openai Chat Completions request type. It is enabled only for the OpenAI
// provider so compatible third-party endpoints never receive an unknown field.
type openAIPromptCacheKeyRoundTripper struct {
	inner http.RoundTripper
}

func (t *openAIPromptCacheKeyRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	inner := t.inner
	if inner == nil {
		inner = http.DefaultTransport
	}
	if req == nil || req.Method != http.MethodPost || req.URL == nil ||
		!strings.Contains(req.URL.Path, "chat/completions") || req.Body == nil {
		return inner.RoundTrip(req)
	}
	key := openAIPromptCacheKey(req.Context())
	if key == "" {
		return inner.RoundTrip(req)
	}
	body, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	if err != nil {
		return nil, err
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.ContentLength = int64(len(body))
		return inner.RoundTrip(req)
	}
	payload["prompt_cache_key"] = key
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req2 := req.Clone(req.Context())
	req2.Body = io.NopCloser(bytes.NewReader(encoded))
	req2.ContentLength = int64(len(encoded))
	req2.Header.Set("Content-Type", "application/json")
	req2.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(encoded)), nil
	}
	return inner.RoundTrip(req2)
}

func normalizeOpenAIChatAPIPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return path
}

func splitOpenAIChatAPIPath(path string) (string, string) {
	path = normalizeOpenAIChatAPIPath(path)
	if path == "" {
		return "", ""
	}
	if strings.Contains(path, "?") {
		u, err := url.Parse(path)
		if err == nil && u.Path != "" {
			return normalizeOpenAIChatAPIPath(u.Path), u.RawQuery
		}
	}
	return path, ""
}

type rewriteOpenAIChatPathRoundTripper struct {
	inner   http.RoundTripper
	apiPath string
}

func (t *rewriteOpenAIChatPathRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	inner := t.inner
	if inner == nil {
		inner = http.DefaultTransport
	}
	if req == nil || req.URL == nil || req.Method != http.MethodPost {
		return inner.RoundTrip(req)
	}
	apiPath, rawQuery := splitOpenAIChatAPIPath(t.apiPath)
	if apiPath == "" || apiPath == defaultOpenAIChatCompletionsPath || !strings.HasSuffix(req.URL.Path, defaultOpenAIChatCompletionsPath) {
		return inner.RoundTrip(req)
	}
	req2 := req.Clone(req.Context())
	if req2 == nil {
		req2 = req
	}
	u := *req.URL
	prefix := strings.TrimSuffix(u.Path, defaultOpenAIChatCompletionsPath)
	u.Path = strings.TrimRight(prefix, "/") + apiPath
	u.RawPath = ""
	if rawQuery != "" {
		u.RawQuery = rawQuery
	}
	req2.URL = &u
	return inner.RoundTrip(req2)
}

type normalizeOpenAICompatReasoningRoundTripper struct {
	inner http.RoundTripper
}

func (t *normalizeOpenAICompatReasoningRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	inner := t.inner
	if inner == nil {
		inner = http.DefaultTransport
	}
	resp, err := inner.RoundTrip(req)
	if err != nil || resp == nil || resp.Body == nil {
		return resp, err
	}
	contentType := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type")))
	isEventStream := strings.Contains(contentType, "event-stream")

	// Some gateway proxies (e.g. Spring Cloud Gateway) may return a non-2xx
	// status with an inline error JSON prefix followed by otherwise-valid SSE
	// chunks from the upstream model. When we detect this pattern, strip the
	// error prefix and rewrite the status so the SDK processes the remaining
	// valid stream data instead of discarding it as a hard error.
	if isEventStream && resp.StatusCode >= 400 {
		if nextBody, ok := salvageSSEBodyAfterErrorPrefix(resp.Body); nextBody != nil {
			resp.Body = nextBody
			if ok {
				resp.StatusCode = http.StatusOK
				resp.Status = "200 OK"
			}
		}
	}
	if resp.StatusCode >= 400 {
		return normalizeRawProviderErrorResponse(resp)
	}

	if !strings.Contains(contentType, "json") && !isEventStream {
		return resp, nil
	}
	if isEventStream {
		resp.Body = normalizeOpenAICompatReasoningEventStreamBody(resp.Body)
		resp.ContentLength = -1
		resp.Header.Del("Content-Length")
		return resp, nil
	}
	body, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if readErr != nil {
		return nil, readErr
	}
	normalized := normalizeOpenAICompatReasoningJSON(body)
	resp.Body = io.NopCloser(bytes.NewReader(normalized))
	resp.ContentLength = int64(len(normalized))
	resp.Header.Set("Content-Length", strconv.Itoa(len(normalized)))
	return resp, nil
}

func normalizeRawProviderErrorResponse(resp *http.Response) (*http.Response, error) {
	if resp == nil || resp.Body == nil || resp.StatusCode < 400 {
		return resp, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, providerErrorBodyLimit+1))
	_ = resp.Body.Close()
	if err != nil {
		return nil, err
	}
	truncated := len(body) > providerErrorBodyLimit
	if truncated {
		body = body[:providerErrorBodyLimit]
	}
	if normalized, ok := normalizeOpenAIProviderErrorForSDK(body); ok {
		setHTTPResponseBody(resp, normalized, "application/json")
		return resp, nil
	}
	message := rawProviderErrorMessage(resp, body, truncated)
	normalized, err := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "provider_error",
			"code":    resp.StatusCode,
		},
	})
	if err != nil {
		return nil, err
	}
	setHTTPResponseBody(resp, normalized, "application/json")
	return resp, nil
}

func setHTTPResponseBody(resp *http.Response, body []byte, contentType string) {
	if resp == nil {
		return
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	if strings.TrimSpace(contentType) != "" {
		resp.Header.Set("Content-Type", contentType)
	}
	resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
}

func normalizeOpenAIProviderErrorForSDK(body []byte) ([]byte, bool) {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return nil, false
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, false
	}
	rawErr := obj["error"]
	if len(rawErr) == 0 {
		return nil, false
	}
	var errObj map[string]json.RawMessage
	if err := json.Unmarshal(rawErr, &errObj); err != nil || len(errObj) == 0 {
		return nil, false
	}

	compact := compactJSONBytes(body)
	normalizedErr := make(map[string]any, len(errObj)+1)
	normalizedErr["message"] = string(compact)
	for key, raw := range errObj {
		if key == "message" || len(raw) == 0 {
			continue
		}
		var value any
		if err := json.Unmarshal(raw, &value); err == nil {
			normalizedErr[key] = value
		}
	}
	normalized, err := json.Marshal(map[string]any{"error": normalizedErr})
	if err != nil {
		return nil, false
	}
	return normalized, true
}

func compactJSONBytes(body []byte) []byte {
	var buf bytes.Buffer
	trimmed := bytes.TrimSpace(body)
	if err := json.Compact(&buf, trimmed); err != nil {
		return trimmed
	}
	return buf.Bytes()
}

func rawProviderErrorMessage(resp *http.Response, body []byte, truncated bool) string {
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		msg = strings.TrimSpace(resp.Status)
		if msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
	}
	if msg == "" {
		msg = "provider returned an empty error response"
	}
	if truncated {
		msg += "\n[truncated]"
	}
	return msg
}

func normalizeOpenAICompatReasoningEventStreamBody(body io.ReadCloser) io.ReadCloser {
	pr, pw := io.Pipe()
	go func() {
		defer func() {
			_ = body.Close()
		}()
		const maxLineSize = 1 << 20 // 1 MiB — reasoning content can exceed the default 4 KiB bufio buffer.
		reader := bufio.NewReaderSize(body, maxLineSize)
		for {
			line, err := reader.ReadBytes('\n')
			if len(line) > 0 {
				if errors.Is(err, io.EOF) && !bytes.HasSuffix(line, []byte("\n")) {
					line = append(line, '\n')
				}
				out := normalizeOpenAICompatReasoningEventStreamLine(line)
				if _, writeErr := pw.Write(out); writeErr != nil {
					_ = pw.CloseWithError(writeErr)
					return
				}
			}
			if errors.Is(err, io.EOF) {
				_ = pw.Close()
				return
			}
			if err != nil {
				_ = pw.CloseWithError(err)
				return
			}
		}
	}()
	return pr
}

// salvageSSEBodyAfterErrorPrefix detects and strips an inline error JSON
// prefix that some gateway proxies inject into streaming SSE error responses.
//
// The typical pattern is: {"error":{...}}data:{...}\n...
// The gateway writes a raw JSON error (without SSE framing), immediately
// followed by otherwise-valid SSE "data:" frames from the upstream model.
// This function strips the error prefix so the SDK can process the remaining
// valid stream.
//
// Returns the replacement body and true when a valid stream is salvaged. When
// no salvageable SSE data is found, it returns the original bytes as a fresh
// body and false so callers can still surface the provider error body.
func salvageSSEBodyAfterErrorPrefix(body io.ReadCloser) (io.ReadCloser, bool) {
	data, err := io.ReadAll(body)
	_ = body.Close()
	if err != nil || len(data) == 0 {
		return nil, false
	}
	idx := bytes.Index(data, []byte("data:"))
	if idx <= 0 {
		return io.NopCloser(bytes.NewReader(data)), false
	}
	// The prefix should look like a JSON error object.
	prefix := bytes.TrimSpace(data[:idx])
	if len(prefix) == 0 || prefix[0] != '{' || !bytes.Contains(prefix, []byte(`"error"`)) {
		return io.NopCloser(bytes.NewReader(data)), false
	}
	return io.NopCloser(bytes.NewReader(data[idx:])), true
}

func normalizeOpenAICompatReasoningEventStreamLine(line []byte) []byte {
	trimmed := strings.TrimSpace(string(line))
	if !strings.HasPrefix(trimmed, "data:") {
		return line
	}
	payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
	if payload == "" || payload == "[DONE]" {
		return line
	}
	normalized := []byte(payload)
	if errPayload, ok := normalizeOpenAIProviderErrorForSDK(normalized); ok {
		normalized = errPayload
	} else {
		normalized = normalizeOpenAICompatReasoningJSON(normalized)
	}
	if bytes.Equal(normalized, []byte(payload)) {
		return line
	}
	prefix := ""
	if idx := strings.Index(string(line), "data:"); idx > 0 {
		prefix = string(line[:idx])
	}
	suffix := ""
	if strings.HasSuffix(string(line), "\r\n") {
		suffix = "\r\n"
	} else if strings.HasSuffix(string(line), "\n") {
		suffix = "\n"
	}
	return []byte(prefix + "data: " + string(normalized) + suffix)
}

func normalizeOpenAICompatReasoningJSON(body []byte) []byte {
	var raw any
	if err := json.Unmarshal(body, &raw); err != nil {
		return body
	}
	if !normalizeReasoningAliases(raw) {
		return body
	}
	normalized, err := json.Marshal(raw)
	if err != nil {
		return body
	}
	return normalized
}

func normalizeReasoningAliases(v any) bool {
	changed := false
	switch x := v.(type) {
	case map[string]any:
		if reasoning, ok := x["reasoning"]; ok {
			if _, exists := x["reasoning_content"]; !exists {
				// Preserve the value verbatim — streaming SSE delivers reasoning
				// per token chunk, and chunks frequently carry a leading or
				// trailing space (" let me", "more ", etc). Trimming each chunk
				// strips inter-chunk spaces and the accumulated text becomes
				// jammed ("Letme continue moreofthefile..."). Only skip the
				// remap when the value is structurally empty.
				raw := anyToString(reasoning)
				if strings.TrimSpace(raw) != "" {
					x["reasoning_content"] = raw
					changed = true
				}
			}
		}
		for _, child := range x {
			if normalizeReasoningAliases(child) {
				changed = true
			}
		}
	case []any:
		for _, child := range x {
			if normalizeReasoningAliases(child) {
				changed = true
			}
		}
	}
	return changed
}

func anyToString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	default:
		return ""
	}
}

func openAILLMInjectedRequestKey(k string) bool {
	switch strings.ToLower(strings.TrimSpace(k)) {
	case "messages", "tools", "tool_choice", "functions", "function_call", "prompt_cache_key":
		return true
	default:
		return false
	}
}

func topLevelEnableThinkingFromParamsJSON(paramsJSON []byte) (any, bool) {
	if len(paramsJSON) == 0 {
		return nil, false
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(paramsJSON, &m); err != nil {
		return nil, false
	}
	raw, ok := m["enable_thinking"]
	if !ok {
		return nil, false
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, false
	}
	return v, true
}

type enableThinkingChatRoundTripper struct {
	inner     http.RoundTripper
	staticVal any
	staticOK  bool
}

func (t *enableThinkingChatRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	inner := t.inner
	if inner == nil {
		inner = http.DefaultTransport
	}
	if req.Method != http.MethodPost || req.URL == nil || !strings.Contains(req.URL.Path, "chat/completions") {
		return inner.RoundTrip(req)
	}
	if req.Body == nil {
		return inner.RoundTrip(req)
	}
	b, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	if err != nil {
		return nil, err
	}
	var bodyMap map[string]any
	if err := json.Unmarshal(b, &bodyMap); err != nil {
		req.Body = io.NopCloser(bytes.NewReader(b))
		req.ContentLength = int64(len(b))
		return inner.RoundTrip(req)
	}
	if !t.staticOK {
		req.Body = io.NopCloser(bytes.NewReader(b))
		req.ContentLength = int64(len(b))
		return inner.RoundTrip(req)
	}
	chosen := t.staticVal
	bodyMap["enable_thinking"] = chosen
	if ctk, ok := bodyMap["chat_template_kwargs"].(map[string]any); ok && ctk != nil {
		ctk["enable_thinking"] = chosen
	} else {
		bodyMap["chat_template_kwargs"] = map[string]any{"enable_thinking": chosen}
	}
	newB, err := json.Marshal(bodyMap)
	if err != nil {
		return nil, err
	}
	newBCopy := append([]byte(nil), newB...)
	req2 := req.Clone(req.Context())
	if req2 == nil {
		req2 = req
	}
	req2.Body = io.NopCloser(bytes.NewReader(newBCopy))
	req2.ContentLength = int64(len(newBCopy))
	req2.Header.Set("Content-Type", "application/json")
	req2.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(newBCopy)), nil
	}
	return inner.RoundTrip(req2)
}

func hoistEnableThinkingIntoChatTemplateKwargs(m map[string]interface{}) {
	raw, ok := m["enable_thinking"]
	if !ok {
		return
	}
	delete(m, "enable_thinking")
	var ctk map[string]interface{}
	if existing, ok := m["chat_template_kwargs"]; ok && existing != nil {
		if mm, ok := existing.(map[string]interface{}); ok {
			ctk = mm
		}
	}
	if ctk == nil {
		ctk = make(map[string]interface{})
		m["chat_template_kwargs"] = ctk
	}
	if _, has := ctk["enable_thinking"]; !has {
		ctk["enable_thinking"] = raw
	}
}

// hoistReasoningEffort translates the nested params format {"reasoning":{"effort":"xhigh"}}
// to the flat OpenAI API key {"reasoning_effort":"xhigh"}. The go-openai SDK struct
// has ReasoningEffort with JSON tag "reasoning_effort" but no "reasoning" field, so
// the nested form would be silently dropped during the marshal→unmarshal roundtrip
// in openAIChatCompletionRequestMerged.
func hoistReasoningEffort(m map[string]interface{}) {
	reasoning, ok := m["reasoning"].(map[string]interface{})
	if !ok || reasoning == nil {
		return
	}
	effort, ok := reasoning["effort"]
	if !ok {
		return
	}
	if _, has := m["reasoning_effort"]; !has {
		m["reasoning_effort"] = effort
	}
	delete(reasoning, "effort")
	if len(reasoning) == 0 {
		delete(m, "reasoning")
	}
}

func openAIChatCompletionRequestMerged(model string, temperature float32, maxTokens int, paramsJSON []byte, messages []openaiclient.ChatCompletionMessage, tools []openaiclient.Tool) (openaiclient.ChatCompletionRequest, error) {
	base := map[string]interface{}{
		"model":       model,
		"temperature": float64(temperature),
		"stream":      true,
	}
	if isReasoningStyleOpenAIModel(model) {
		base["max_completion_tokens"] = maxTokens
	} else {
		base["max_tokens"] = maxTokens
	}
	if len(paramsJSON) > 0 {
		var overlay map[string]interface{}
		if err := json.Unmarshal(paramsJSON, &overlay); err != nil {
			return openaiclient.ChatCompletionRequest{}, err
		}
		for k, v := range overlay {
			if openAILLMInjectedRequestKey(k) {
				continue
			}
			base[k] = v
		}
	}
	normalizeOpenAITokenLimitFields(base)
	normalizeOpenAIStreamUsage(base)
	hoistEnableThinkingIntoChatTemplateKwargs(base)
	hoistReasoningEffort(base)
	blob, err := json.Marshal(base)
	if err != nil {
		return openaiclient.ChatCompletionRequest{}, err
	}
	var req openaiclient.ChatCompletionRequest
	if err := json.Unmarshal(blob, &req); err != nil {
		return openaiclient.ChatCompletionRequest{}, err
	}
	req.Messages = messages
	if len(tools) > 0 {
		req.Tools = tools
	} else {
		req.Tools = nil
	}
	return req, nil
}

// normalizeOpenAIStreamUsage keeps stream_options consistent with stream.
// Streaming requests opt into usage reporting; non-streaming requests must not
// carry stream_options at all, because OpenAI-compatible servers reject the
// pair with a 400. Operator params are merged verbatim (stream_options is not
// an injected key), so "stream": false alongside an explicit stream_options
// block reaches here and has to be cleaned up rather than forwarded.
func normalizeOpenAIStreamUsage(body map[string]interface{}) {
	if body == nil {
		return
	}
	stream, ok := body["stream"].(bool)
	if !ok || !stream {
		delete(body, "stream_options")
		return
	}
	raw, ok := body["stream_options"]
	if !ok || raw == nil {
		body["stream_options"] = map[string]interface{}{"include_usage": true}
		return
	}
	if opts, ok := raw.(map[string]interface{}); ok {
		if _, exists := opts["include_usage"]; !exists {
			opts["include_usage"] = true
		}
		return
	}
	body["stream_options"] = map[string]interface{}{"include_usage": true}
}

func isReasoningStyleOpenAIModel(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(m, "o1") ||
		strings.HasPrefix(m, "o3") ||
		strings.HasPrefix(m, "o4") ||
		strings.HasPrefix(m, "gpt-5")
}

func normalizeOpenAITokenLimitFields(body map[string]interface{}) {
	if body == nil {
		return
	}
	model, _ := body["model"].(string)
	if !isReasoningStyleOpenAIModel(model) {
		return
	}
	if _, hasCompletion := body["max_completion_tokens"]; hasCompletion {
		delete(body, "max_tokens")
		return
	}
	if maxTokens, hasMax := body["max_tokens"]; hasMax {
		body["max_completion_tokens"] = maxTokens
		delete(body, "max_tokens")
	}
}

func mergeStreamToolCalls(acc *[]openaiclient.ToolCall, delta []openaiclient.ToolCall) {
	for _, d := range delta {
		idx := 0
		if d.Index != nil {
			idx = *d.Index
		}
		for len(*acc) <= idx {
			*acc = append(*acc, openaiclient.ToolCall{Type: openaiclient.ToolTypeFunction})
		}
		t := &(*acc)[idx]
		if d.ID != "" {
			t.ID = d.ID
		}
		if d.Type != "" {
			t.Type = d.Type
		}
		if d.Function.Name != "" {
			t.Function.Name += d.Function.Name
		}
		if d.Function.Arguments != "" {
			t.Function.Arguments += d.Function.Arguments
		}
	}
}

func (c *openAICompatLLM) executeChatCompletionStream(ctx context.Context, req openaiclient.ChatCompletionRequest) (*llm.Result, error) {
	sink := llm.StreamSinkFrom(ctx)
	baseMsgs := append([]openaiclient.ChatCompletionMessage(nil), req.Messages...)
	var reasoningAcc, contentAcc strings.Builder
	var toolBuf []openaiclient.ToolCall
	var role string
	var lastFinish openaiclient.FinishReason
	var tokIn, tokOut, tokCached int
	var reportedIn, reportedOut int
	responseStarted := false

	for round := 0; round < openAILengthContinueMax; round++ {
		req.Messages = baseMsgs
		stream, err := c.client.CreateChatCompletionStream(ctx, req)
		if err != nil {
			if sink != nil && sink.OnEnd != nil {
				sink.OnEnd()
			}
			return nil, err
		}
		if sink != nil && sink.Streamed != nil && round == 0 {
			*sink.Streamed = true
		}
		lastFinish = ""
		for {
			resp, err := stream.Recv()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				_ = stream.Close()
				if sink != nil && sink.OnEnd != nil {
					sink.OnEnd()
				}
				return nil, err
			}
			if len(resp.Choices) > 0 {
				delta := resp.Choices[0].Delta
				if delta.ReasoningContent != "" || delta.Content != "" {
					llm.NotifyResponseStarted(sink, &responseStarted)
				}
			}
			if resp.Usage != nil {
				tokIn += resp.Usage.PromptTokens
				tokOut += resp.Usage.CompletionTokens
				if resp.Usage.PromptTokensDetails != nil {
					tokCached += resp.Usage.PromptTokensDetails.CachedTokens
				}
				if sink != nil && sink.OnUsage != nil {
					deltaIn := tokIn - reportedIn
					deltaOut := tokOut - reportedOut
					if deltaIn > 0 || deltaOut > 0 {
						sink.OnUsage(deltaIn, deltaOut)
						reportedIn = tokIn
						reportedOut = tokOut
					}
				}
				if sink != nil && sink.OnUsageSnapshot != nil {
					sink.OnUsageSnapshot(tokIn, tokOut)
				}
			}
			if len(resp.Choices) == 0 {
				continue
			}
			ch := resp.Choices[0]
			if ch.FinishReason != "" && ch.FinishReason != openaiclient.FinishReasonNull {
				lastFinish = ch.FinishReason
			}
			d := ch.Delta
			if strings.TrimSpace(d.Role) != "" {
				role = d.Role
			}
			if d.ReasoningContent != "" {
				reasoningAcc.WriteString(d.ReasoningContent)
				if sink != nil && sink.OnReasoningDelta != nil {
					sink.OnReasoningDelta(d.ReasoningContent)
				}
			}
			if d.Content != "" {
				contentAcc.WriteString(d.Content)
				if sink != nil && sink.OnDelta != nil {
					sink.OnDelta(d.Content)
				}
			}
			if len(d.ToolCalls) > 0 {
				mergeStreamToolCalls(&toolBuf, d.ToolCalls)
			}
		}
		_ = stream.Close()

		if !openAIReasoningLengthShouldContinue(lastFinish, reasoningAcc.Len(), contentAcc.Len(), len(toolBuf)) {
			break
		}
		asst := openaiclient.ChatCompletionMessage{
			Role:             openaiclient.ChatMessageRoleAssistant,
			ReasoningContent: reasoningAcc.String(),
			Content:          contentAcc.String(),
		}
		if len(toolBuf) > 0 {
			asst.ToolCalls = append([]openaiclient.ToolCall(nil), toolBuf...)
		}
		baseMsgs = append(append([]openaiclient.ChatCompletionMessage(nil), baseMsgs...), asst)
	}

	if sink != nil && sink.OnEnd != nil {
		sink.OnEnd()
	}
	// Fire OnReasoningDone so the TUI reducer can finalize the thinking block
	// (emit FrameThinking{Final:true}).  The Responses API path fires this
	// from a dedicated SSE event, but the Chat Completions API has no such
	// event — reasoning deltas just stop arriving when the model switches to
	// content output.  Without this the thinking block stays in the streaming
	// (▸) state until the next assistant-text or tool frame arrives, which
	// could be much later.
	if reasoningAcc.Len() > 0 && sink != nil && sink.OnReasoningDone != nil {
		sink.OnReasoningDone()
	}
	if role == "" {
		role = openaiclient.ChatMessageRoleAssistant
	}
	openMsg := openaiclient.ChatCompletionMessage{
		Role:             role,
		ReasoningContent: reasoningAcc.String(),
		Content:          contentAcc.String(),
	}
	if len(toolBuf) > 0 {
		openMsg.ToolCalls = toolBuf
	}
	msg := compatMessageFromOpenAI(openMsg)
	out := &llm.Result{Message: &msg}
	out.Usage = openAIUsage(int64(tokIn), int64(tokOut), int64(tokCached))
	return out, nil
}

func (c *openAICompatLLM) Execute(ctx context.Context, messages []llm.Message, tools []*llm.Tool) (outResult *llm.Result, outErr error) {
	// Every error leaving this package is normalized here rather than at each
	// return, so a path added later cannot leak a raw SDK error to Layer 0.
	defer func() { outErr = normalizeError(outErr) }()
	ctx, finish := llm.WithRawCacheUsage(ctx)
	// Fingerprint the exact prompt shape this call renders so the cache usage
	// that comes back can be attributed: fork (our prefix changed) vs
	// anomaly (provider-side miss). See pkg/llm/prefix_fingerprint.go.
	prefix := llm.BeginPrefixObservation(ctx, c.model, tools, messages)
	msgs := compatMessagesToOpenAI(messages)
	var otools []openaiclient.Tool
	if len(tools) > 0 {
		otools = compatOpenAITools(tools)
	}
	req, err := openAIChatCompletionRequestMerged(c.model, c.temperature, c.maxTokens, c.paramsJSON, msgs, otools)
	if err != nil {
		return nil, err
	}
	if req.Stream {
		result, err := c.executeChatCompletionStream(ctx, req)
		result = finish(result)
		prefix.Finish(resultUsage(result))
		return result, err
	}
	result, err := c.executeChatCompletionUnary(ctx, req)
	result = finish(result)
	prefix.Finish(resultUsage(result))
	return result, err
}

// resultUsage returns the usage attached to result, tolerating a nil result.
func resultUsage(result *llm.Result) *llm.Usage {
	if result == nil {
		return nil
	}
	return result.Usage
}

func (c *openAICompatLLM) ExecuteStructured(ctx context.Context, messages []llm.Message, spec llm.StructuredOutputSpec) (outResult *llm.Result, outErr error) {
	defer func() { outErr = normalizeError(outErr) }()
	if c == nil {
		return nil, fmt.Errorf("nil OpenAI-compatible client")
	}
	if !spec.Strict {
		return nil, fmt.Errorf("structured output must be strict")
	}
	if strings.TrimSpace(spec.Name) == "" || len(spec.Schema) == 0 {
		return nil, fmt.Errorf("structured output name and schema are required")
	}
	ctx, finish := llm.WithRawCacheUsage(ctx)
	prefix := llm.BeginPrefixObservation(ctx, c.model, nil, messages)
	req, err := openAIChatCompletionRequestMerged(c.model, c.temperature, c.maxTokens, c.paramsJSON, compatMessagesToOpenAI(messages), nil)
	if err != nil {
		return nil, err
	}
	// Structured output is always a unary call. Stream is forced off after the
	// merge, which means normalizeOpenAIStreamUsage already ran while stream was
	// still true and left stream_options populated; clear it here too or the
	// server rejects stream_options on a non-streaming request.
	req.Stream = false
	req.StreamOptions = nil
	req.ResponseFormat = &openaiclient.ChatCompletionResponseFormat{
		Type: openaiclient.ChatCompletionResponseFormatTypeJSONSchema,
		JSONSchema: &openaiclient.ChatCompletionResponseFormatJSONSchema{
			Name:        strings.TrimSpace(spec.Name),
			Description: strings.TrimSpace(spec.Description),
			Schema:      rawJSONSchema(spec.Schema),
			Strict:      true,
		},
	}
	result, err := c.executeChatCompletionUnary(ctx, req)
	result = finish(result)
	prefix.Finish(resultUsage(result))
	return result, err
}

type rawJSONSchema map[string]any

func (s rawJSONSchema) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any(s))
}

func (c *openAICompatLLM) executeChatCompletionUnary(ctx context.Context, req openaiclient.ChatCompletionRequest) (*llm.Result, error) {
	baseMsgs := append([]openaiclient.ChatCompletionMessage(nil), req.Messages...)
	var reasoningAcc, contentAcc strings.Builder
	var toolBuf []openaiclient.ToolCall
	var role string
	var lastFinish openaiclient.FinishReason
	var tokIn, tokOut, tokCached int

	for round := 0; round < openAILengthContinueMax; round++ {
		req.Messages = baseMsgs
		resp, err := c.client.CreateChatCompletion(ctx, req)
		if err != nil {
			return nil, err
		}
		if len(resp.Choices) == 0 {
			return nil, errOpenAINoChoices
		}
		ch := resp.Choices[0]
		lastFinish = ch.FinishReason
		m := ch.Message
		if strings.TrimSpace(m.Role) != "" {
			role = m.Role
		}
		reasoningAcc.WriteString(m.ReasoningContent)
		contentAcc.WriteString(m.Content)
		if len(m.ToolCalls) > 0 {
			toolBuf = append([]openaiclient.ToolCall(nil), m.ToolCalls...)
		}
		tokIn += resp.Usage.PromptTokens
		tokOut += resp.Usage.CompletionTokens
		if resp.Usage.PromptTokensDetails != nil {
			tokCached += resp.Usage.PromptTokensDetails.CachedTokens
		}

		if !openAIReasoningLengthShouldContinue(lastFinish, reasoningAcc.Len(), contentAcc.Len(), len(toolBuf)) {
			break
		}
		asst := openaiclient.ChatCompletionMessage{
			Role:             openaiclient.ChatMessageRoleAssistant,
			ReasoningContent: reasoningAcc.String(),
			Content:          contentAcc.String(),
		}
		if len(toolBuf) > 0 {
			asst.ToolCalls = append([]openaiclient.ToolCall(nil), toolBuf...)
		}
		baseMsgs = append(append([]openaiclient.ChatCompletionMessage(nil), baseMsgs...), asst)
	}

	if role == "" {
		role = openaiclient.ChatMessageRoleAssistant
	}
	openMsg := openaiclient.ChatCompletionMessage{
		Role:             role,
		ReasoningContent: reasoningAcc.String(),
		Content:          contentAcc.String(),
	}
	if len(toolBuf) > 0 {
		openMsg.ToolCalls = toolBuf
	}
	msg := compatMessageFromOpenAI(openMsg)
	return &llm.Result{
		Message: &msg,
		Usage:   openAIUsage(int64(tokIn), int64(tokOut), int64(tokCached)),
	}, nil
}

// compatMessagesToOpenAI projects the conversation for the wire.
//
// The projection is per message: what message i renders to depends on message
// i alone, never on the messages around it. That is not a style preference —
// providers with implicit prefix caching serve a request only while its
// rendered bytes extend a prefix they have already seen, so a rule that lets a
// later turn change how an earlier message renders forks the prefix at that
// earlier message and re-bills every token after it, on every step of every
// tool loop. A selection like "carry the reasoning of the last assistant
// message that has any" is exactly such a rule: each new reasoning block
// silently rewrites the previous holder.
func compatMessagesToOpenAI(messages []llm.Message) []openaiclient.ChatCompletionMessage {
	out := make([]openaiclient.ChatCompletionMessage, 0, len(messages))
	for _, m := range messages {
		out = append(out, compatMessageToOpenAI(m))
	}
	return out
}

func compatMessageToOpenAI(m llm.Message) openaiclient.ChatCompletionMessage {
	msg := openaiclient.ChatCompletionMessage{
		Role:       compatRoleToOpenAI(m.Role),
		ToolCallID: m.ToolCallID,
	}
	if strings.HasPrefix(m.Name, openAIReasoningCarryPrefix) {
		msg.ReasoningContent = strings.TrimPrefix(m.Name, openAIReasoningCarryPrefix)
		msg.Name = ""
	} else {
		msg.Name = m.Name
	}
	if len(m.ToolCalls) > 0 {
		msg.ToolCalls = make([]openaiclient.ToolCall, len(m.ToolCalls))
		for i, tc := range m.ToolCalls {
			msg.ToolCalls[i] = openaiclient.ToolCall{
				ID:   tc.ID,
				Type: openaiclient.ToolType(tc.Type),
				Function: openaiclient.FunctionCall{
					Name:      tc.Function.Name,
					Arguments: llm.SanitizeToolCallArguments(tc.Function.Arguments),
				},
			}
		}
	}
	if len(m.Parts) == 0 {
		return msg
	}
	if len(m.Parts) == 1 && m.Parts[0].Type == llm.ContentTypeText {
		msg.Content = m.Parts[0].Text
		return msg
	}
	multi := make([]openaiclient.ChatMessagePart, 0, len(m.Parts))
	for _, p := range m.Parts {
		switch p.Type {
		case llm.ContentTypeText:
			if strings.TrimSpace(p.Text) != "" {
				multi = append(multi, openaiclient.ChatMessagePart{
					Type: openaiclient.ChatMessagePartTypeText,
					Text: p.Text,
				})
			}
		case llm.ContentTypeImageURL:
			multi = append(multi, openaiclient.ChatMessagePart{
				Type: openaiclient.ChatMessagePartTypeImageURL,
				ImageURL: &openaiclient.ChatMessageImageURL{
					URL: p.ImageURL,
				},
			})
		case llm.ContentTypeImageBase64:
			dataURI := "data:" + p.MIMEType + ";base64," + p.ImageBase64
			multi = append(multi, openaiclient.ChatMessagePart{
				Type: openaiclient.ChatMessagePartTypeImageURL,
				ImageURL: &openaiclient.ChatMessageImageURL{
					URL: dataURI,
				},
			})
		}
	}
	msg.MultiContent = multi
	return msg
}

func compatRoleToOpenAI(role string) string {
	if role == llm.RoleDeveloper {
		// Map developer to system for OpenAI-compatible providers that don't support
		// the developer role (e.g., DeepSeek). Native OpenAI accepts both roles.
		return llm.RoleSystem
	}
	return role
}

func compatMessageFromOpenAI(m openaiclient.ChatCompletionMessage) llm.Message {
	msg := llm.Message{
		Role:       m.Role,
		ToolCallID: m.ToolCallID,
	}
	// Preserve the raw reasoning content verbatim — do not trim. Trimming the
	// accumulated text drops a leading or trailing space that was emitted as
	// part of the first/last streaming chunk, which is meaningful for prose.
	// Use a separate TrimSpace only to decide whether reasoning is structurally
	// present.
	reasoning := m.ReasoningContent
	hasReasoning := strings.TrimSpace(reasoning) != ""
	if hasReasoning {
		msg.Name = openAIReasoningCarryPrefix + reasoning
	} else {
		msg.Name = m.Name
	}
	if len(m.ToolCalls) > 0 {
		msg.ToolCalls = make([]llm.ToolCall, len(m.ToolCalls))
		for i, tc := range m.ToolCalls {
			msg.ToolCalls[i] = llm.ToolCall{
				ID:   tc.ID,
				Type: string(tc.Type),
				Function: llm.FunctionCall{
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
				},
			}
		}
	} else if synthesized := synthesizeToolCallsFromReasoning(reasoning); len(synthesized) > 0 {
		msg.ToolCalls = synthesized
	}
	if len(m.MultiContent) > 0 {
		parts := make([]llm.ContentPart, 0, len(m.MultiContent))
		for _, p := range m.MultiContent {
			switch p.Type {
			case openaiclient.ChatMessagePartTypeText:
				parts = append(parts, llm.Text(p.Text))
			case openaiclient.ChatMessagePartTypeImageURL:
				if p.ImageURL != nil {
					parts = append(parts, llm.ImageURL(p.ImageURL.URL))
				}
			}
		}
		msg.Parts = parts
	} else if strings.TrimSpace(m.Content) != "" {
		msg.Parts = []llm.ContentPart{llm.Text(m.Content)}
	}
	// If the model returned only reasoning content — no real content, no native
	// tool calls, and no synthesizable tool calls — surface the reasoning as
	// visible content so the user sees the model's thinking instead of getting
	// an "empty assistant response" error. Clear the carry so this text is not
	// also re-sent as reasoning_content on the next API call. Wrap in a fenced
	// code block (language "thinking") so the CLI renderer preserves the
	// original whitespace and newlines instead of collapsing them as Markdown
	// inline text would.
	if len(msg.Parts) == 0 && len(msg.ToolCalls) == 0 && hasReasoning {
		msg.Parts = []llm.ContentPart{llm.Text("```thinking\n" + reasoning + "\n```")}
		msg.Name = ""
	}
	return msg
}

func synthesizeToolCallsFromReasoning(reasoning string) []llm.ToolCall {
	reasoning = strings.TrimSpace(reasoning)
	if reasoning == "" || !strings.Contains(reasoning, "<tool_call>") {
		return nil
	}
	var out []llm.ToolCall
	rest := reasoning
	for {
		start := strings.Index(rest, "<tool_call>")
		if start < 0 {
			break
		}
		rest = rest[start+len("<tool_call>"):]
		end := strings.Index(rest, "</tool_call>")
		if end < 0 {
			break
		}
		block := strings.TrimSpace(rest[:end])
		rest = rest[end+len("</tool_call>"):]
		call, ok := parseReasoningToolCallBlock(block, len(out))
		if !ok {
			continue
		}
		out = append(out, call)
	}
	return out
}

func parseReasoningToolCallBlock(block string, index int) (llm.ToolCall, bool) {
	fnName, fnBody, ok := extractTaggedBlock(block, "<function=", "</function>")
	if !ok {
		return llm.ToolCall{}, false
	}
	fnName = strings.TrimSpace(fnName)
	if fnName == "" {
		return llm.ToolCall{}, false
	}
	args := map[string]string{}
	rest := fnBody
	for {
		paramName, paramBody, found := extractTaggedBlock(rest, "<parameter=", "</parameter>")
		if !found {
			break
		}
		key := strings.TrimSpace(paramName)
		if key != "" {
			args[key] = strings.TrimSpace(paramBody)
		}
		nextIdx := strings.Index(rest, "</parameter>")
		if nextIdx < 0 {
			break
		}
		rest = rest[nextIdx+len("</parameter>"):]
	}
	argJSON, err := json.Marshal(args)
	if err != nil {
		return llm.ToolCall{}, false
	}
	return llm.ToolCall{
		ID:   "reasoning-tool-call-" + strconv.Itoa(index+1),
		Type: llm.ToolTypeFunction,
		Function: llm.FunctionCall{
			Name:      fnName,
			Arguments: string(argJSON),
		},
	}, true
}

func extractTaggedBlock(s, openPrefix, closeTag string) (name string, body string, ok bool) {
	start := strings.Index(s, openPrefix)
	if start < 0 {
		return "", "", false
	}
	afterOpen := s[start+len(openPrefix):]
	tagEnd := strings.Index(afterOpen, ">")
	if tagEnd < 0 {
		return "", "", false
	}
	name = afterOpen[:tagEnd]
	content := afterOpen[tagEnd+1:]
	closeIdx := strings.Index(content, closeTag)
	if closeIdx < 0 {
		return "", "", false
	}
	return name, content[:closeIdx], true
}

func compatOpenAITools(tools []*llm.Tool) []openaiclient.Tool {
	out := make([]openaiclient.Tool, len(tools))
	for i, tool := range tools {
		out[i] = openaiclient.Tool{
			Type: openaiclient.ToolTypeFunction,
			Function: &openaiclient.FunctionDefinition{
				Name:        tool.Name(),
				Parameters:  tool.InputSchema(),
				Description: tool.Description(),
				// Tool arguments stay non-strict: strict structured outputs
				// force every property to be required and reject optional
				// fields, which weaker models mis-handle (e.g. emitting a
				// string-encoded array for an optional array field).
				Strict: false,
			},
		}
	}
	return out
}
