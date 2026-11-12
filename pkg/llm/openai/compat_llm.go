package openai

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	openaiclient "github.com/sashabaranov/go-openai"
)

const (
	defaultOpenAIChatModel           = "gpt-4o-mini"
	DefaultMaxCompletionTokens       = 8192
	defaultOpenAIChatCompletionsPath = "/chat/completions"
	openAIReasoningCarryPrefix       = "!!FOREBRAIN_RCv1!!"
	providerErrorBodyLimit           = 1 << 20
)

var errOpenAINoChoices = errors.New("openai: response contained no choices")

// publishReasoningCarry hands the reply's reasoning, as far as it is complete
// and safe to replay, to the sink. Both of this package's adapters call it at
// every point where a reasoning unit closes, so a reply that is cut off is
// finished from a message that still carries it. It does nothing when nobody
// listens or there is no carry yet.
func publishReasoningCarry(sink *llm.StreamSink, carry, model string) {
	if sink == nil || sink.OnReasoningCarry == nil || carry == "" {
		return
	}
	sink.OnReasoningCarry(carry, model)
}

type openAICompatLLM struct {
	client      *openaiclient.Client
	model       string
	temperature float32
	maxTokens   int
	paramsJSON  []byte
}

func NewCompatLLMWithPromptCaching(apiKey, baseURL, model string, temperature float32, maxTokens int, apiPath string, paramsJSON []byte, promptCaching bool) llm.LLM {
	return newCompatLLM(apiKey, baseURL, model, temperature, maxTokens, apiPath, paramsJSON, promptCaching, nil)
}

// NewCompatLLMWithTransport is NewCompatLLMWithPromptCaching with prompt
// caching off and a caller-supplied transport injected outermost, which is
// where a provider that authenticates outside the apiKey argument (OpenCode's
// gateway) stamps its bearer token, client identity and session header.
//
// extra must accept this client's own wrapper chain through SetBase, the way
// openai.Transport does; a transport without that method is used on its own,
// dropping the reasoning-normalizing and usage-accounting wrappers this client
// installs. The key handed to the client itself is never the real credential
// on that path — the transport overwrites the Authorization header.
func NewCompatLLMWithTransport(apiKey, baseURL, model string, temperature float32, maxTokens int, apiPath string, paramsJSON []byte, extra http.RoundTripper) llm.LLM {
	return newCompatLLM(apiKey, baseURL, model, temperature, maxTokens, apiPath, paramsJSON, false, extra)
}

func newCompatLLM(apiKey, baseURL, model string, temperature float32, maxTokens int, apiPath string, paramsJSON []byte, promptCaching bool, extra http.RoundTripper) llm.LLM {
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
		hc.Transport = &compatNativeMediaRoundTripper{inner: hc.Transport}
		if normalized := normalizeOpenAIChatAPIPath(apiPath); normalized != "" && normalized != defaultOpenAIChatCompletionsPath {
			hc.Transport = &rewriteOpenAIChatPathRoundTripper{inner: hc.Transport, apiPath: normalized}
		}
		hc.Transport = &enableThinkingChatRoundTripper{inner: hc.Transport, staticVal: topVal, staticOK: topOK}
		if promptCaching {
			hc.Transport = &openAIPromptCacheKeyRoundTripper{inner: hc.Transport}
		}
		hc.Transport = &llm.CacheUsageTransport{Next: hc.Transport}
		if extra != nil {
			hc.Transport = roundTripperWithBase(extra, hc.Transport)
		}
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

// compatReasoningExchange is what one request learns from the bytes on the
// wire. field is the name the provider returned reasoning under, empty while
// nothing was observed and for the providers that use reasoning_content;
// renames are the message indices whose reasoning has to go out under another
// name, because the provider named the field itself and does not read
// reasoning_content at all.
//
// It lives in the request context so both halves reach the one place that can
// act on them: the round tripper below renames the inbound field of every
// response, and is the only layer that can rewrite the serialized request,
// which go-openai builds from a struct spelled reasoning_content.
type compatReasoningExchange struct {
	mu      sync.Mutex
	field   string
	renames map[int]string
}

// requestField is the field name the provider used for this request's
// reasoning, "" when it used the default.
func (e *compatReasoningExchange) requestField() string {
	if e == nil {
		return ""
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.field
}

// observeField records the field name a response returned reasoning under.
func (e *compatReasoningExchange) observeField(field string) {
	if e == nil || field == "" {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.field = field
}

// rename records that message index of the request's messages array carries its
// reasoning under field rather than reasoning_content.
func (e *compatReasoningExchange) rename(index int, field string) {
	if e == nil || field == "" {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.renames == nil {
		e.renames = map[int]string{}
	}
	e.renames[index] = field
}

// outboundRenames returns the renames recorded so far, nil when there are none
// — the common case, where the request is sent exactly as go-openai wrote it.
func (e *compatReasoningExchange) outboundRenames() map[int]string {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.renames) == 0 {
		return nil
	}
	out := make(map[int]string, len(e.renames))
	for index, field := range e.renames {
		out[index] = field
	}
	return out
}

type compatReasoningExchangeKey struct{}

// withCompatReasoningExchange gives one request — one Execute attempt — its own
// exchange. A resend starts a fresh one, since it carries no reasoning to
// rename.
func withCompatReasoningExchange(ctx context.Context, ex *compatReasoningExchange) context.Context {
	return context.WithValue(ctx, compatReasoningExchangeKey{}, ex)
}

func compatReasoningExchangeFrom(ctx context.Context) *compatReasoningExchange {
	ex, _ := ctx.Value(compatReasoningExchangeKey{}).(*compatReasoningExchange)
	return ex
}

// compatReasoningRequestWithFields renames the reasoning field of the messages
// the projection marked, and leaves every other request untouched: the rename
// exists only for a provider that reads reasoning under a field of its own, so
// the body of every request to every other provider stays exactly the bytes
// go-openai wrote.
func compatReasoningRequestWithFields(req *http.Request, ex *compatReasoningExchange) (*http.Request, bool) {
	renames := ex.outboundRenames()
	if len(renames) == 0 || req == nil || req.Method != http.MethodPost || req.Body == nil || req.URL == nil ||
		!strings.Contains(req.URL.Path, "chat/completions") {
		return nil, false
	}
	body, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	// The request is sent as it was when nothing below matches, so its body is
	// put back before any of the ways out.
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	if err != nil {
		return nil, false
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, false
	}
	messages, ok := payload["messages"].([]any)
	if !ok {
		return nil, false
	}
	renamed := false
	for index, field := range renames {
		if index < 0 || index >= len(messages) {
			continue
		}
		message, ok := messages[index].(map[string]any)
		if !ok {
			continue
		}
		reasoning, present := message[llm.CompatReasoningField]
		if !present {
			continue
		}
		delete(message, llm.CompatReasoningField)
		message[field] = reasoning
		renamed = true
	}
	if !renamed {
		return nil, false
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, false
	}
	clone := req.Clone(req.Context())
	clone.Body = io.NopCloser(bytes.NewReader(encoded))
	clone.ContentLength = int64(len(encoded))
	clone.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(encoded)), nil
	}
	return clone, true
}

func (t *normalizeOpenAICompatReasoningRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	inner := t.inner
	if inner == nil {
		inner = http.DefaultTransport
	}
	ex := compatReasoningExchangeFrom(req.Context())
	if renamed, ok := compatReasoningRequestWithFields(req, ex); ok {
		req = renamed
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
		resp.Body = normalizeOpenAICompatReasoningEventStreamBody(resp.Body, ex)
		resp.ContentLength = -1
		resp.Header.Del("Content-Length")
		return resp, nil
	}
	body, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if readErr != nil {
		return nil, readErr
	}
	normalized := normalizeOpenAICompatReasoningJSON(body, ex)
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

func normalizeOpenAICompatReasoningEventStreamBody(body io.ReadCloser, ex *compatReasoningExchange) io.ReadCloser {
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
				out := normalizeOpenAICompatReasoningEventStreamLine(line, ex)
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

func normalizeOpenAICompatReasoningEventStreamLine(line []byte, ex *compatReasoningExchange) []byte {
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
		normalized = normalizeOpenAICompatReasoningJSON(normalized, ex)
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

func normalizeOpenAICompatReasoningJSON(body []byte, ex *compatReasoningExchange) []byte {
	var raw any
	if err := json.Unmarshal(body, &raw); err != nil {
		return body
	}
	if !normalizeReasoningAliases(raw, ex) {
		return body
	}
	normalized, err := json.Marshal(raw)
	if err != nil {
		return body
	}
	return normalized
}

func normalizeReasoningAliases(v any, ex *compatReasoningExchange) bool {
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
					// The provider calls its reasoning field "reasoning", so a
					// replay has to use that name: it is the only one this
					// endpoint reads.
					ex.observeField("reasoning")
					changed = true
				}
			}
		}
		for _, child := range x {
			if normalizeReasoningAliases(child, ex) {
				changed = true
			}
		}
	case []any:
		for _, child := range x {
			if normalizeReasoningAliases(child, ex) {
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

// compatToolCallFromOpenAI converts one streamed or accumulated SDK tool call
// into the engine's representation. The mid-stream OnToolCallReady callbacks
// and the final message both go through it, so a call reported ready is
// byte-identical to the one the Result carries.
func compatToolCallFromOpenAI(tc openaiclient.ToolCall) llm.ToolCall {
	return llm.ToolCall{
		ID:   tc.ID,
		Type: string(tc.Type),
		Function: llm.FunctionCall{
			Name:      tc.Function.Name,
			Arguments: tc.Function.Arguments,
		},
	}
}

// emitCompatToolReady fires OnToolCallReady for every accumulated call with
// index < upTo that has not been reported yet. A call the provider left
// without an ID gets one assigned in place, so the final message carries the
// same ID the ready callback reported.
func emitCompatToolReady(sink *llm.StreamSink, toolBuf *[]openaiclient.ToolCall, readyCount *int, upTo int) {
	if sink == nil || sink.OnToolCallReady == nil {
		return
	}
	limit := upTo
	if limit > len(*toolBuf) {
		limit = len(*toolBuf)
	}
	for i := *readyCount; i < limit; i++ {
		tc := &(*toolBuf)[i]
		if tc.ID == "" {
			tc.ID = fmt.Sprintf("call_%d_%s", i, compatStreamIDSuffix())
		}
		sink.OnToolCallReady(compatToolCallFromOpenAI(*tc))
	}
	if limit > *readyCount {
		*readyCount = limit
	}
}

// compatStreamIDSuffix returns 8 random hex characters for a synthesized
// call ID.
func compatStreamIDSuffix() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "00000000"
	}
	return hex.EncodeToString(b[:])
}

func (c *openAICompatLLM) executeChatCompletionStream(ctx context.Context, req openaiclient.ChatCompletionRequest) (*llm.Result, error) {
	sink := llm.StreamSinkFrom(ctx)
	// ex is this request's reasoning exchange, installed by execute: the field
	// the provider names its reasoning with, and the messages to rename.
	ex := compatReasoningExchangeFrom(ctx)
	baseMsgs := append([]openaiclient.ChatCompletionMessage(nil), req.Messages...)
	// roundMsgs is what one round sends: the caller's messages, plus — from
	// the second round on — the recovery's resume pair (the accumulated
	// answer as an assistant turn, the resume instruction as a user turn).
	// contentAcc already holds every round so far, so each round replaces
	// that pair rather than appending another: an appended copy per round
	// sent round 1's output twice by round 3 and grew the prompt with the
	// square of the rounds.
	roundMsgs := baseMsgs
	var reasoningAcc, contentAcc strings.Builder
	// reasoningCarry is the carry this reply would hold if it ended right now:
	// the thinking accumulated so far, stamped exactly as
	// compatMessageFromOpenAI stamps the finished message. The compat stream has
	// no thinking-end frame of its own, so every place that decides the thinking
	// ended is also a place the carry is complete.
	reasoningCarry := func() string {
		if reasoningAcc.Len() == 0 {
			return ""
		}
		// The same carry the finished message holds, field name included: a
		// reply that is cut off is finished from it, and it has to go back
		// under the name the provider used.
		return llm.EncodeCompatReasoning(ex.requestField(), reasoningAcc.String())
	}
	// carryPublished is the reasoning already handed to the sink: a later
	// boundary that found nothing new does not publish the same carry twice.
	carryPublished := ""
	carryReasoning := func() {
		carry := reasoningCarry()
		if carry == "" || carry == carryPublished {
			return
		}
		carryPublished = carry
		publishReasoningCarry(sink, carry, c.model)
	}
	// The compat stream has no thinking-end frame: the thinking stops being
	// extendable at the first answer text or tool fragment, which is where the
	// carry is complete. It is published there, before that output's own
	// callbacks — the tool call it precedes is what starts the provisional
	// message written from the sink.
	reasoningEnded := false
	carryFinishedThinking := func() {
		if reasoningEnded || reasoningAcc.Len() == 0 {
			return
		}
		reasoningEnded = true
		carryReasoning()
	}
	var toolBuf []openaiclient.ToolCall
	var role string
	var lastFinish openaiclient.FinishReason
	// readyCount is the number of leading toolBuf entries whose OnToolCallReady
	// already fired; textDone guards the single OnTextBlockDone that seals the
	// answer text before the first tool fragment. Both survive length-continue
	// rounds: a round never re-fires what an earlier round already reported.
	readyCount := 0
	textDone := false
	var tokIn, tokOut, tokCached int
	// settledIn/Out/Cached are the last round's own usage: the request this
	// conversation is now standing at. tokIn/tokOut/tokCached are every round
	// summed — the billing view, since a length-continue re-sends the whole
	// prompt and each round is its own billed request.
	var settledIn, settledOut, settledCached int
	// lastAttemptUsage is the settled round's own usage: nil until a round
	// completed, and never a sum. It covers the failures raised after a round
	// settled (ErrStreamTruncated, ErrStreamNoEvents), where the round the
	// conversation stands at is also the request that failed.
	lastAttemptUsage := func() *llm.Usage {
		return openAIUsage(int64(settledIn), int64(settledOut), int64(settledCached))
	}
	// refusedRoundUsage is the size of the request the current round sent, for a
	// round that failed: its own reported usage when the provider sent some, or
	// else what it re-sent — the previous round's prompt plus the answer that
	// round produced, which this round carries. Nil on a first round that reported
	// nothing, so overflow calibration falls back to its estimate. It is never the
	// previous round's own usage: that request was accepted, and recording it as
	// refused would cap the window below a size that fit.
	refusedRoundUsage := func(roundIn, roundOut, roundCached int) *llm.Usage {
		if roundIn > 0 {
			return openAIUsage(int64(roundIn), int64(roundOut), int64(roundCached))
		}
		return openAIUsage(int64(settledIn+settledOut), 0, int64(settledCached))
	}
	var reportedIn, reportedOut int
	responseStarted := false
	// The output-limit recovery: at most OutputLimitContinuations re-asks,
	// each carrying the one accumulated answer plus the resume instruction.
	// The fragment is contentAcc — raw answer text, never the
	// compatMessageFromOpenAI rendering, which wraps thinking-only output in
	// a ```thinking fence and would send the reasoning back as answer text.
	var recovery llm.OutputLimitRecovery
	round := 0
	for {
		// Each round is its own request, and Chat Completions reports usage as
		// that request's running total rather than as a per-chunk increment:
		// the same object can arrive on more than one chunk (stepfun /
		// opencode-go sends it on every one). Snapshot inside the round and sum
		// across rounds — adding each chunk up reported N x prompt.
		roundIn, roundOut, roundCached := 0, 0, 0
		req.Messages = roundMsgs
		ctx = llm.WithStreamingRequest(ctx)
		stream, err := c.client.CreateChatCompletionStream(ctx, req)
		if err != nil {
			// Earlier length-continue rounds were already billed; that
			// billing rides the error into the retry loop's accounting.
			return nil, llm.NewExecuteErrorWithLastAttempt(err, openAIUsage(int64(tokIn), int64(tokOut), int64(tokCached)), refusedRoundUsage(roundIn, roundOut, roundCached))
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
				// The stream died mid-read (a reset, an unexpected EOF): the
				// thinking that streamed and never turned into answer text
				// is a finished block at this death — the compat stream has
				// no boundary of its own, so this is it. Calls stay as the
				// in-flight boundaries left them: a later index confirmed
				// what completed, and nothing else is ready.
				_ = stream.Close()
				// The death of the stream is the boundary the compat stream
				// lacks in flight: the thinking that streamed and never turned
				// into answer text is complete at this death, so the carry goes
				// out beside the thinking-done notice.
				carryReasoning()
				if reasoningAcc.Len() > 0 && sink != nil && sink.OnReasoningDone != nil {
					sink.OnReasoningDone()
				}
				// The usage chunks that arrived before the cut were billed;
				// that billing rides the error into the retry loop's
				// accounting.
				return nil, llm.NewExecuteErrorWithLastAttempt(err, openAIUsage(int64(tokIn+roundIn), int64(tokOut+roundOut), int64(tokCached+roundCached)), refusedRoundUsage(roundIn, roundOut, roundCached))
			}
			if len(resp.Choices) > 0 {
				// A tool-call fragment is model output like a word is: a
				// response that answers with calls alone has started the
				// moment its first fragment arrives.
				delta := resp.Choices[0].Delta
				if delta.ReasoningContent != "" || delta.Content != "" || len(delta.ToolCalls) > 0 {
					llm.NotifyResponseStarted(sink, &responseStarted)
				}
			}
			if resp.Usage != nil {
				// Usage is the request's total, not this chunk's increment, so
				// it replaces the round's snapshot instead of adding to it.
				roundIn = resp.Usage.PromptTokens
				roundOut = resp.Usage.CompletionTokens
				if resp.Usage.PromptTokensDetails != nil {
					roundCached = resp.Usage.PromptTokensDetails.CachedTokens
				}
				totalIn, totalOut := tokIn+roundIn, tokOut+roundOut
				// OnUsage stays incremental (its consumers add the deltas up):
				// the difference against the cross-round total keeps that sum
				// right while the absolute numbers stop compounding.
				if sink != nil && sink.OnUsage != nil {
					deltaIn := totalIn - reportedIn
					deltaOut := totalOut - reportedOut
					if deltaIn > 0 || deltaOut > 0 {
						sink.OnUsage(deltaIn, deltaOut)
						reportedIn = totalIn
						reportedOut = totalOut
					}
				}
				if sink != nil && sink.OnUsageSnapshot != nil {
					sink.OnUsageSnapshot(totalIn, totalOut)
				}
			}
			if len(resp.Choices) == 0 {
				continue
			}
			ch := resp.Choices[0]
			finishBefore := lastFinish
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
				// Answer text is where the thinking ended, so its carry is
				// complete here.
				carryFinishedThinking()
				contentAcc.WriteString(d.Content)
				if sink != nil && sink.OnDelta != nil {
					sink.OnDelta(d.Content)
				}
			}
			if len(d.ToolCalls) > 0 {
				// A tool fragment is the thinking's other end, and the call it
				// belongs to may be reported ready below.
				carryFinishedThinking()
				// The answer text is finished the moment the first tool
				// fragment arrives: the compat stream has no text boundary
				// of its own.
				if contentAcc.Len() > 0 && !textDone {
					textDone = true
					if sink != nil && sink.OnTextBlockDone != nil {
						sink.OnTextBlockDone()
					}
				}
				for _, tc := range d.ToolCalls {
					j := 0
					if tc.Index != nil {
						j = *tc.Index
					}
					// Index j appearing for the first time means every
					// smaller index already streamed all of its fragments.
					if j >= len(toolBuf) {
						emitCompatToolReady(sink, &toolBuf, &readyCount, j)
					}
				}
				mergeStreamToolCalls(&toolBuf, d.ToolCalls)
			}
			// The finish-reason frame is the compat stream's terminal frame.
			// It is checked after the delta merge so a provider that packs
			// the last fragment and the finish reason into one chunk still
			// reports complete arguments.
			if lastFinish != "" && finishBefore == "" {
				emitCompatToolReady(sink, &toolBuf, &readyCount, len(toolBuf))
				if sink != nil && sink.OnStopReason != nil {
					sink.OnStopReason(string(lastFinish))
				}
				if sink != nil && sink.OnMessageStop != nil {
					sink.OnMessageStop()
				}
			}
		}
		_ = stream.Close()

		// The round is over: fold its own total into the cross-round sum.
		// Length-continue re-sends the request, so each round is its own
		// billed request with its own usage and the sum across rounds is what
		// this call is billed for. Only the settled round is the request the
		// conversation now stands at, and that — not the sum — is the
		// occupancy every window reader measures.
		settledIn, settledOut, settledCached = roundIn, roundOut, roundCached
		tokIn += roundIn
		tokOut += roundOut
		tokCached += roundCached

		// A clean close with no finish frame after output means the stream
		// was cut mid-answer (most often a proxy dropping the SSE
		// connection): report it instead of returning the partial text as a
		// success, so the retry layers get a chance to re-send. Zero output
		// with no finish frame either is not an answer at all — the stream
		// closed before producing a single event, and the request is re-sent
		// without streaming.
		//
		// The death of the stream is also the boundary the compat stream
		// lacks in flight: the thinking that streamed and never turned into
		// text is a finished block now, and so is every call a later index
		// already confirmed complete — a call with no such confirmation keeps
		// its half-merged arguments to itself instead of being reported
		// ready at its own death.
		if lastFinish == "" && (contentAcc.Len() > 0 || reasoningAcc.Len() > 0 || len(toolBuf) > 0) {
			// The cut ends the thinking as much as it ends the answer, so the
			// carry goes out beside the thinking-done notice.
			carryReasoning()
			if reasoningAcc.Len() > 0 && sink != nil && sink.OnReasoningDone != nil {
				sink.OnReasoningDone()
			}
			return nil, llm.NewExecuteErrorWithLastAttempt(fmt.Errorf("%s: %w", c.model, llm.ErrStreamTruncated), openAIUsage(int64(tokIn), int64(tokOut), int64(tokCached)), lastAttemptUsage())
		}
		if lastFinish == "" {
			return nil, llm.NewExecuteErrorWithLastAttempt(fmt.Errorf("%s: %w", c.model, llm.ErrStreamNoEvents), openAIUsage(int64(tokIn), int64(tokOut), int64(tokCached)), lastAttemptUsage())
		}
		// Clean exit without truncation: any call the finish frame did not
		// cover (a provider that ends [DONE] with zero output) is ready now.
		emitCompatToolReady(sink, &toolBuf, &readyCount, len(toolBuf))

		if !recovery.ShouldContinue(llm.NormalizeStopReason(string(lastFinish)), contentAcc.Len(), len(toolBuf)) {
			break
		}
		roundMsgs = append(append([]openaiclient.ChatCompletionMessage(nil), baseMsgs...), compatMessagesToOpenAI(recovery.Resume(contentAcc.String()), c.model, ex)...)
		round++
	}

	// Fire OnReasoningDone so the TUI reducer can finalize the thinking block
	// (emit FrameThinking{Final:true}).  The Responses API path fires this
	// from a dedicated SSE event, but the Chat Completions API has no such
	// event — reasoning deltas just stop arriving when the model switches to
	// content output.  Without this the thinking block stays in the streaming
	// (▸) state until the next assistant-text or tool frame arrives, which
	// could be much later.  The thinking is as complete as it will get for this
	// reply here, so the carry goes out beside that notice.
	carryReasoning()
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
	msg := compatMessageFromOpenAI(openMsg, c.model, ex.requestField())
	out := &llm.Result{Message: &msg, StopReason: llm.NormalizeStopReason(string(lastFinish))}
	// Occupancy is the settled round's prompt; the output is every round's,
	// because the model really did produce it and this Result's answer carries
	// it.
	out.Usage = openAIUsage(int64(settledIn), int64(tokOut), int64(settledCached))
	if settledIn != tokIn || settledOut != tokOut || settledCached != tokCached {
		out.BilledUsage = openAIUsage(int64(tokIn), int64(tokOut), int64(tokCached))
	}
	return out, nil
}

func (c *openAICompatLLM) Execute(ctx context.Context, messages []llm.Message, tools []*llm.Tool) (outResult *llm.Result, outErr error) {
	// Every error leaving this package is normalized here rather than at each
	// return, so a path added later cannot leak a raw SDK error to Layer 0.
	defer func() { outErr = normalizeError(outErr) }()
	// The sink's end fires once per Execute, not once per request: a
	// length-continue round, and the resend a refused request gets, are the
	// same answer still being produced.
	endSink := llm.StreamSinkFrom(ctx)
	if endSink != nil && endSink.OnEnd != nil {
		defer endSink.OnEnd()
	}
	res, replayed, err := c.execute(ctx, messages, tools)
	if err != nil && replayed && llm.ReasoningReplayRefused(normalizeError(err), []int{http.StatusBadRequest}, "reasoning") {
		// A provider that refuses the replayed reasoning refuses it on every
		// request of this conversation — the same reasoning goes back each
		// time — so one resend without it is what keeps the turn alive. A
		// refusal after that is some other cause and is returned as it is.
		slog.Warn("openai compat: provider refused the replayed reasoning; resending without it", "model", c.model, "err", err)
		res, _, err = c.execute(ctx, llm.WithoutReasoningCarries(messages), tools)
	}
	return res, err
}

// execute runs one request for Execute: the projection, the request loop and
// the sink callbacks. replayed reports whether that request sent reasoning back
// to the model, which is what makes a refusal worth a resend without it.
func (c *openAICompatLLM) execute(ctx context.Context, messages []llm.Message, tools []*llm.Tool) (*llm.Result, bool, error) {
	// One request gets one exchange: what the provider called its reasoning
	// field, and which messages must go back under that name. A resend starts a
	// fresh one — it carries no reasoning to rename.
	ctx = withCompatReasoningExchange(ctx, &compatReasoningExchange{})
	ex := compatReasoningExchangeFrom(ctx)
	ctx, finish := llm.WithRawCacheUsage(ctx)
	// Fingerprint the exact prompt shape this call renders so the cache usage
	// that comes back can be attributed: fork (our prefix changed) vs
	// anomaly (provider-side miss). See pkg/llm/prefix_fingerprint.go.
	prefix := llm.BeginPrefixObservation(ctx, c.model, tools, messages)
	msgs := compatMessagesToOpenAI(messages, c.model, ex)
	replayed := false
	for _, msg := range msgs {
		if msg.ReasoningContent != "" {
			replayed = true
			break
		}
	}
	var otools []openaiclient.Tool
	if len(tools) > 0 {
		otools = compatOpenAITools(tools)
	}
	req, err := openAIChatCompletionRequestMerged(c.model, c.temperature, c.maxTokens, c.paramsJSON, msgs, otools)
	if err != nil {
		return nil, replayed, err
	}
	if llm.NonStreamingRequestFrom(ctx) {
		// The request context switched this call to one non-streaming
		// attempt: run it under the non-streaming deadline and replay the
		// whole answer to the sink in stream order.
		req.Stream = false
		req.StreamOptions = nil
		ctx, cancel := llm.WithNonStreamingDeadline(ctx)
		defer cancel()
		sink := llm.StreamSinkFrom(ctx)
		openMsg, finishReason, usage, billed, err := c.executeChatCompletionUnaryParts(ctx, req, true)
		if err != nil {
			err = llm.NonStreamingError(ctx, err)
			result := finish(nil)
			prefix.Finish(nil)
			return result, replayed, err
		}
		msg := compatMessageFromOpenAI(openMsg, c.model, ex.requestField())
		result := &llm.Result{Message: &msg, StopReason: llm.NormalizeStopReason(string(finishReason)), Usage: usage, BilledUsage: billed}
		result = finishUsage(result, finish)
		prefix.Finish(resultBilledUsage(result))
		if sink != nil && result.Usage != nil && (result.Usage.InputTokens > 0 || result.Usage.OutputTokens > 0) {
			if sink.OnUsage != nil {
				// OnUsage feeds the run's additive counters, which are a bill: a
				// length-continue's every round. The snapshot below is the request the
				// conversation stands at.
				billed := result.Billed()
				sink.OnUsage(billed.InputTokens, billed.OutputTokens)
			}
			if sink.OnUsageSnapshot != nil {
				sink.OnUsageSnapshot(result.Usage.InputTokens, result.Usage.OutputTokens)
			}
		}
		// The whole reply is finished here, so its carry is complete: hand it
		// over before the replay, whose tool-call callbacks are what can start a
		// provisional message for this reply.
		if strings.HasPrefix(msg.Name, openAIReasoningCarryPrefix) {
			publishReasoningCarry(sink, msg.Name, msg.ReasoningModel)
		}
		llm.ReplayToSink(sink, llm.ReplayedResponse{
			Reasoning:  openMsg.ReasoningContent,
			Text:       []string{openMsg.Content},
			ToolCalls:  result.Message.ToolCalls,
			StopReason: string(finishReason),
		})
		return result, replayed, nil
	}
	if req.Stream {
		result, err := c.executeChatCompletionStream(ctx, req)
		result = finishUsage(result, finish)
		prefix.Finish(resultBilledUsage(result))
		return result, replayed, err
	}
	result, err := c.executeChatCompletionUnary(ctx, req, true)
	result = finishUsage(result, finish)
	prefix.Finish(resultBilledUsage(result))
	return result, replayed, err
}

// finishUsage folds the raw transport's reading into result without letting it
// overwrite the request the conversation is standing at.
//
// The transport reports the total of every response this call read — the
// billing view, and on a single-request call the same number the client
// computed. A length-continue makes several responses, and there the total is
// not the settled round: assigning it to Result.Usage put a 2x prompt in front
// of every occupancy reader (the compaction threshold fired at half the window
// and the footer gauge echoed it). The client's own split wins, and the raw
// total stays what it is: the billing view.
func finishUsage(result *llm.Result, finish func(*llm.Result) *llm.Result) *llm.Result {
	if result == nil {
		return finish(nil)
	}
	settled, billed := result.Usage, result.BilledUsage
	result = finish(result)
	if result == nil || billed == nil {
		// One request: the raw figure is the client's own, and the single
		// request path stays exactly as it was.
		return result
	}
	raw := result.Usage
	result.Usage = settled
	if raw != nil && (settled == nil || *raw != *settled) {
		result.BilledUsage = raw
	}
	return result
}

// resultBilledUsage returns what this call was billed for — every physical
// request it made — tolerating a nil result. Cache hit-rate is a billing
// figure: each length-continue round re-reads the same prefix.
func resultBilledUsage(result *llm.Result) *llm.Usage {
	if result == nil {
		return nil
	}
	return result.Billed()
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
	res, replayed, err := c.executeStructured(ctx, messages, spec)
	if err != nil && replayed && llm.ReasoningReplayRefused(normalizeError(err), []int{http.StatusBadRequest}, "reasoning") {
		// Same one resend Execute makes: see there.
		slog.Warn("openai compat: provider refused the replayed reasoning; resending without it", "model", c.model, "err", err)
		res, _, err = c.executeStructured(ctx, llm.WithoutReasoningCarries(messages), spec)
	}
	return res, err
}

// executeStructured runs one structured-output request for ExecuteStructured,
// reporting whether it replayed reasoning the way execute does.
func (c *openAICompatLLM) executeStructured(ctx context.Context, messages []llm.Message, spec llm.StructuredOutputSpec) (*llm.Result, bool, error) {
	// See execute: one request, one exchange.
	ctx = withCompatReasoningExchange(ctx, &compatReasoningExchange{})
	ctx, finish := llm.WithRawCacheUsage(ctx)
	prefix := llm.BeginPrefixObservation(ctx, c.model, nil, messages)
	msgs := compatMessagesToOpenAI(messages, c.model, compatReasoningExchangeFrom(ctx))
	replayed := false
	for _, msg := range msgs {
		if msg.ReasoningContent != "" {
			replayed = true
			break
		}
	}
	req, err := openAIChatCompletionRequestMerged(c.model, c.temperature, c.maxTokens, c.paramsJSON, msgs, nil)
	if err != nil {
		return nil, replayed, err
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
	// Structured output runs exactly one round even when the finish reason
	// says the budget ran out: a JSON document can only be continued by
	// re-sending the same document, so the StopReason is recorded for the
	// caller instead of being recovered here.
	result, err := c.executeChatCompletionUnary(ctx, req, false)
	result = finishUsage(result, finish)
	prefix.Finish(resultBilledUsage(result))
	return result, replayed, err
}

type rawJSONSchema map[string]any

func (s rawJSONSchema) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any(s))
}

func (c *openAICompatLLM) executeChatCompletionUnary(ctx context.Context, req openaiclient.ChatCompletionRequest, continueOnLimit bool) (*llm.Result, error) {
	ex := compatReasoningExchangeFrom(ctx)
	openMsg, finishReason, usage, billed, err := c.executeChatCompletionUnaryParts(ctx, req, continueOnLimit)
	if err != nil {
		return nil, err
	}
	msg := compatMessageFromOpenAI(openMsg, c.model, ex.requestField())
	return &llm.Result{Message: &msg, StopReason: llm.NormalizeStopReason(string(finishReason)), Usage: usage, BilledUsage: billed}, nil
}

// executeChatCompletionUnaryParts runs the length-continue loop of one
// non-streaming chat completion and returns the assembled OpenAI message, the
// last finish reason and the usage — the pieces a sink replay of the whole
// answer needs on top of the Result. usage is the settled round (what
// occupancy reads), billed every round (what the call was billed for) and nil
// when there was only one round. continueOnLimit is false on the structured
// output path, which must not continue: JSON can only be continued by
// re-sending the same document, not by a resume instruction that restarts it.
func (c *openAICompatLLM) executeChatCompletionUnaryParts(ctx context.Context, req openaiclient.ChatCompletionRequest, continueOnLimit bool) (openaiclient.ChatCompletionMessage, openaiclient.FinishReason, *llm.Usage, *llm.Usage, error) {
	// See executeChatCompletionStream.
	ex := compatReasoningExchangeFrom(ctx)
	baseMsgs := append([]openaiclient.ChatCompletionMessage(nil), req.Messages...)
	// See executeChatCompletionStream: each round replaces the carried answer.
	roundMsgs := baseMsgs
	var reasoningAcc, contentAcc strings.Builder
	var toolBuf []openaiclient.ToolCall
	var role string
	var lastFinish openaiclient.FinishReason
	var tokIn, tokOut, tokCached int
	// settledIn/Out/Cached are the last round's own usage; tokIn/tokOut/
	// tokCached are every round summed (each round is its own billed request).
	var settledIn, settledOut, settledCached int
	// lastAttemptUsage is the settled round's own usage: nil until a round
	// completed, and never a sum.
	lastAttemptUsage := func() *llm.Usage {
		return openAIUsage(int64(settledIn), int64(settledOut), int64(settledCached))
	}
	// refusedRoundUsage is the size of the request the current round sent, for
	// a round that failed. See executeChatCompletionStream.
	refusedRoundUsage := func() *llm.Usage {
		return openAIUsage(int64(settledIn+settledOut), 0, int64(settledCached))
	}

	var recovery llm.OutputLimitRecovery
	for {
		req.Messages = roundMsgs
		resp, err := c.client.CreateChatCompletion(ctx, req)
		if err != nil {
			// Earlier length-continue rounds were already billed; that
			// billing rides the error into the retry loop's accounting.
			return openaiclient.ChatCompletionMessage{}, "", nil, nil, llm.NewExecuteErrorWithLastAttempt(err, openAIUsage(int64(tokIn), int64(tokOut), int64(tokCached)), refusedRoundUsage())
		}
		if len(resp.Choices) == 0 {
			// A 200 with usage but no choice was still billed for its
			// prompt, on this round and any earlier one.
			cached := 0
			if resp.Usage.PromptTokensDetails != nil {
				cached = resp.Usage.PromptTokensDetails.CachedTokens
			}
			settledIn, settledOut, settledCached = resp.Usage.PromptTokens, resp.Usage.CompletionTokens, cached
			return openaiclient.ChatCompletionMessage{}, "", nil, nil, llm.NewExecuteErrorWithLastAttempt(errOpenAINoChoices,
				openAIUsage(int64(tokIn+resp.Usage.PromptTokens), int64(tokOut+resp.Usage.CompletionTokens), int64(tokCached+cached)),
				lastAttemptUsage())
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
		settledIn, settledOut, settledCached = resp.Usage.PromptTokens, resp.Usage.CompletionTokens, 0
		if resp.Usage.PromptTokensDetails != nil {
			settledCached = resp.Usage.PromptTokensDetails.CachedTokens
		}
		tokIn += resp.Usage.PromptTokens
		tokOut += resp.Usage.CompletionTokens
		tokCached += settledCached

		if !continueOnLimit || !recovery.ShouldContinue(llm.NormalizeStopReason(string(lastFinish)), contentAcc.Len(), len(toolBuf)) {
			break
		}
		// The same resume pair the streaming loop sends: the accumulated
		// answer as an assistant turn, the resume instruction as a user turn.
		// Reasoning is never sent back.
		roundMsgs = append(append([]openaiclient.ChatCompletionMessage(nil), baseMsgs...), compatMessagesToOpenAI(recovery.Resume(contentAcc.String()), c.model, ex)...)
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
	settled := openAIUsage(int64(settledIn), int64(tokOut), int64(settledCached))
	var billed *llm.Usage
	if settledIn != tokIn || settledOut != tokOut || settledCached != tokCached {
		billed = openAIUsage(int64(tokIn), int64(tokOut), int64(tokCached))
	}
	return openMsg, lastFinish, settled, billed, nil
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
// silently rewrites the previous holder. The request's model is the one other
// input, and it is the same for every message of a request.
func compatMessagesToOpenAI(messages []llm.Message, model string, ex *compatReasoningExchange) []openaiclient.ChatCompletionMessage {
	out := make([]openaiclient.ChatCompletionMessage, 0, len(messages))
	for _, m := range messages {
		// A reply that was only reasoning goes back to the model that produced
		// it, and to no other: the fence would hand that model's reasoning to
		// another one as an answer this conversation gave.
		if llm.ThinkingFenceOnly(m) && !llm.ThinkingFenceGoesTo(m, model) {
			continue
		}
		msg, field := compatMessageToOpenAI(m, model)
		if field != "" {
			// The index is the message's place in the array actually sent: the
			// loop skips messages, so it is not the message's place in the
			// conversation.
			ex.rename(len(out), field)
		}
		out = append(out, msg)
	}
	return out
}

// compatMessageToOpenAI projects one message. Its second result is the request
// field name the reasoning must go back under when the provider named its own,
// "" when reasoning_content carries it.
func compatMessageToOpenAI(m llm.Message, model string) (openaiclient.ChatCompletionMessage, string) {
	msg := openaiclient.ChatCompletionMessage{
		Role:       compatRoleToOpenAI(m.Role),
		ToolCallID: m.ToolCallID,
	}
	field := ""
	if strings.HasPrefix(m.Name, openAIReasoningCarryPrefix) {
		// The carry is reasoning, never a participant name. It goes back as
		// reasoning_content only to the model that wrote it, and only when the
		// carry is this protocol's own: a Responses payload or an Anthropic
		// thinking payload is another protocol's blocks, not something this
		// model ever said. A fence reply does not take it either — its content
		// already is that reasoning, and repeating it as reasoning_content
		// would send it twice. A provider that named its reasoning field gets
		// the text under that name instead, because reasoning_content is a
		// field it does not read.
		if name, text, ok := llm.DecodeCompatReasoning(m.Name); ok &&
			llm.ReplaysReasoningTo(m, model) && !llm.ThinkingFenceOnly(m) {
			msg.ReasoningContent = text
			if name != llm.CompatReasoningField {
				field = name
			}
		}
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
		return msg, field
	}
	if len(m.Parts) == 1 && m.Parts[0].Type == llm.ContentTypeText {
		msg.Content = m.Parts[0].Text
		return msg, field
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
		case llm.ContentTypeFileBase64:
			if data := strings.TrimSpace(p.FileBase64); data != "" && llm.NativeMediaKindOfMIME(p.MIMEType) != "" {
				multi = append(multi, openaiclient.ChatMessagePart{
					Type:     openaiclient.ChatMessagePartTypeImageURL,
					ImageURL: &openaiclient.ChatMessageImageURL{URL: compatNativeMediaDataURL(p)},
				})
			}
		}
	}
	msg.MultiContent = multi
	return msg, field
}

func compatRoleToOpenAI(role string) string {
	if role == llm.RoleDeveloper {
		// Map developer to system for OpenAI-compatible providers that don't support
		// the developer role (e.g., DeepSeek). Native OpenAI accepts both roles.
		return llm.RoleSystem
	}
	return role
}

// compatMessageFromOpenAI converts model's answer. Reasoning it carries is
// stamped with model, the only model it is ever sent back to, and with field,
// the request field the provider returned it under, so the replay can use that
// name — "" and reasoning_content both mean the plain carry every provider but
// a few reads.
func compatMessageFromOpenAI(m openaiclient.ChatCompletionMessage, model, field string) llm.Message {
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
		msg.Name = llm.EncodeCompatReasoning(field, reasoning)
	} else {
		msg.Name = m.Name
	}
	if len(m.ToolCalls) > 0 {
		msg.ToolCalls = make([]llm.ToolCall, len(m.ToolCalls))
		for i, tc := range m.ToolCalls {
			msg.ToolCalls[i] = compatToolCallFromOpenAI(tc)
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
	// an "empty assistant response" error. Wrap in a fenced code block
	// (language "thinking") so the CLI renderer preserves the original
	// whitespace and newlines instead of collapsing them as Markdown inline
	// text would. The carry stays: it is how this message records the model
	// that produced the reasoning, and the projection above sends the message
	// only to that model — which receives the fence as content and no
	// reasoning_content, so the reasoning is not sent twice.
	if len(msg.Parts) == 0 && len(msg.ToolCalls) == 0 && hasReasoning {
		msg.Parts = []llm.ContentPart{llm.Text(llm.ThinkingFence(reasoning))}
	}
	if strings.HasPrefix(msg.Name, openAIReasoningCarryPrefix) {
		msg.ReasoningModel = model
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
