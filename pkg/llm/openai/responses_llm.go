package openai

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	openaigo "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/responses"
	"github.com/openai/openai-go/shared"
)

// openAIResponsesLLM implements llm.LLM using the official OpenAI SDK's Responses API.
// This is used when the API path is set to /v1/responses or /responses.
type openAIResponsesLLM struct {
	client        openaigo.Client
	model         shared.ResponsesModel
	maxTokens     int64
	stream        bool
	paramsOverlay map[string]any
	webSearch     nativeWebSearchConfig
	cacheKey      bool
	// codex marks the ChatGPT/Codex backend, which accepts a narrower
	// parameter set than api.openai.com and rejects the whole request rather
	// than ignoring an unknown field.
	codex bool
}

func NewResponsesLLMWithCache(apiKey, baseURL, model string, maxTokens int, paramsJSON []byte, cacheKey bool) llm.LLM {
	return NewResponsesLLMWithWebSearchCache(apiKey, baseURL, model, maxTokens, paramsJSON, "", nil, cacheKey)
}

func NewResponsesLLMWithWebSearch(apiKey, baseURL, model string, maxTokens int, paramsJSON []byte, defaultWebSearchMode string, authTransport http.RoundTripper) llm.LLM {
	return NewResponsesLLMWithWebSearchCache(apiKey, baseURL, model, maxTokens, paramsJSON, defaultWebSearchMode, authTransport, true)
}

func NewResponsesLLMWithWebSearchCache(apiKey, baseURL, model string, maxTokens int, paramsJSON []byte, defaultWebSearchMode string, authTransport http.RoundTripper, cacheKey bool) llm.LLM {
	if maxTokens <= 0 {
		maxTokens = 8192
	}
	if strings.TrimSpace(model) == "" {
		model = "gpt-4o-mini"
	}

	effectiveMaxTokens := int64(maxTokens)
	codex := strings.HasPrefix(strings.TrimSpace(baseURL), CodexBaseURL)
	stream := true
	var paramsOverlay map[string]any

	if len(paramsJSON) > 0 {
		var overlay map[string]interface{}
		if json.Unmarshal(paramsJSON, &overlay) == nil {
			if v, ok := overlay["max_output_tokens"]; ok {
				if f, ok := v.(float64); ok && f > 0 {
					effectiveMaxTokens = int64(f)
				}
			}
			if v, ok := overlay["stream"]; ok {
				if b, ok := v.(bool); ok {
					stream = b
				}
			}
			paramsOverlay = normalizeOpenAIResponsesParamsOverlay(overlay)
		}
	}
	webSearch := parseNativeWebSearchConfig(paramsJSON, defaultWebSearchMode)
	webSearch.codexAccessControls = authTransport != nil

	var transport http.RoundTripper = http.DefaultTransport
	transport = &responsesSSEFrameFilter{next: transport}
	transport = &responsesContentTypeFixer{next: transport}
	transport = llm.WrapHTTPTransport(transport)
	transport = &suppressUserAgentRoundTripper{next: transport}
	if authTransport != nil {
		transport = roundTripperWithBase(authTransport, transport)
	}
	if webSearch.enabled() {
		transport = &nativeWebSearchRequestRewriter{next: transport, config: webSearch}
	}
	transport = &llm.CacheUsageTransport{Next: transport}

	opts := []option.RequestOption{
		option.WithAPIKey(apiKey),
		option.WithHTTPClient(&http.Client{Transport: transport}),
	}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}

	client := openaigo.NewClient(opts...)

	return &openAIResponsesLLM{
		client:        client,
		model:         shared.ResponsesModel(model),
		maxTokens:     effectiveMaxTokens,
		stream:        stream,
		paramsOverlay: paramsOverlay,
		webSearch:     webSearch,
		cacheKey:      cacheKey,
		codex:         codex,
	}
}

type nativeWebSearchConfig struct {
	mode                string
	searchContext       string
	allowedDomains      []string
	userLocation        map[string]string
	codexAccessControls bool
}

func (c nativeWebSearchConfig) enabled() bool {
	switch strings.ToLower(strings.TrimSpace(c.mode)) {
	case "cached", "indexed", "live":
		return true
	default:
		return false
	}
}

func parseNativeWebSearchConfig(paramsJSON []byte, defaultMode string) nativeWebSearchConfig {
	cfg := nativeWebSearchConfig{mode: strings.ToLower(strings.TrimSpace(defaultMode))}
	var overlay map[string]any
	if json.Unmarshal(paramsJSON, &overlay) != nil {
		return cfg
	}
	value, ok := overlay["web_search"]
	if !ok {
		return cfg
	}
	if enabled, ok := value.(bool); ok {
		if enabled && cfg.mode == "" {
			cfg.mode = "live"
		} else if !enabled {
			cfg.mode = ""
		}
		return cfg
	}
	if mode, ok := value.(string); ok {
		cfg.mode = strings.ToLower(strings.TrimSpace(mode))
		return cfg
	}
	obj, ok := value.(map[string]any)
	if !ok {
		return cfg
	}
	if mode, ok := obj["mode"].(string); ok {
		cfg.mode = strings.ToLower(strings.TrimSpace(mode))
	}
	if v, ok := obj["search_context_size"].(string); ok {
		cfg.searchContext = strings.TrimSpace(v)
	}
	if domains, ok := obj["allowed_domains"].([]any); ok {
		for _, domain := range domains {
			if s, ok := domain.(string); ok && strings.TrimSpace(s) != "" {
				cfg.allowedDomains = append(cfg.allowedDomains, strings.TrimSpace(s))
			}
		}
	}
	if loc, ok := obj["user_location"].(map[string]any); ok {
		cfg.userLocation = make(map[string]string)
		for _, key := range []string{"country", "region", "city", "timezone"} {
			if s, ok := loc[key].(string); ok && strings.TrimSpace(s) != "" {
				cfg.userLocation[key] = strings.TrimSpace(s)
			}
		}
	}
	return cfg
}

type nativeWebSearchRequestRewriter struct {
	next   http.RoundTripper
	config nativeWebSearchConfig
}

func (rt *nativeWebSearchRequestRewriter) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil || req.Body == nil {
		return rt.next.RoundTrip(req)
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	_ = req.Body.Close()
	var payload map[string]any
	if json.Unmarshal(body, &payload) == nil {
		if tools, ok := payload["tools"].([]any); ok {
			for _, raw := range tools {
				tool, ok := raw.(map[string]any)
				if !ok || tool["type"] != "web_search_preview" {
					continue
				}
				tool["type"] = "web_search"
				if rt.config.codexAccessControls {
					switch rt.config.mode {
					case "cached":
						tool["external_web_access"] = false
					case "indexed":
						tool["external_web_access"] = true
						tool["indexed_web_access"] = true
					case "live":
						tool["external_web_access"] = true
					}
				}
				if rt.config.searchContext != "" {
					tool["search_context_size"] = rt.config.searchContext
				}
				if len(rt.config.allowedDomains) > 0 {
					tool["filters"] = map[string]any{"allowed_domains": rt.config.allowedDomains}
				}
				if len(rt.config.userLocation) > 0 {
					location := map[string]any{"type": "approximate"}
					for key, value := range rt.config.userLocation {
						location[key] = value
					}
					tool["user_location"] = location
				}
			}
			if rewritten, marshalErr := json.Marshal(payload); marshalErr == nil {
				body = rewritten
			}
		}
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	return rt.next.RoundTrip(req)
}

type baseTransportSetter interface {
	SetBase(http.RoundTripper)
}

func roundTripperWithBase(auth, base http.RoundTripper) http.RoundTripper {
	if setter, ok := auth.(baseTransportSetter); ok {
		setter.SetBase(base)
	}
	return auth
}

func normalizeOpenAIResponsesParamsOverlay(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := cloneJSONMap(in)
	for k := range out {
		if openAIResponsesInjectedRequestKey(k) {
			delete(out, k)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func cloneJSONMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func openAIResponsesInjectedRequestKey(k string) bool {
	switch strings.ToLower(strings.TrimSpace(k)) {
	case "input", "tools", "prompt_cache_key", "web_search":
		return true
	default:
		return false
	}
}

func openAIPromptCacheKey(ctx context.Context) string {
	key := strings.TrimSpace(llm.PromptCacheKeyFromContext(ctx))
	if key == "" {
		return ""
	}
	// OpenAI accepts at most 64 characters. Preserve normal UUID/session IDs
	// verbatim; hash longer product IDs deterministically.
	if len(key) <= 64 {
		return key
	}
	// "forebrain:" plus 27 hashed bytes in hex is exactly 64 characters.
	sum := sha256.Sum256([]byte(key))
	return fmt.Sprintf("forebrain:%x", sum[:27])
}

func openAIUsage(input, output, cached int64) *llm.Usage {
	if input <= 0 && output <= 0 && cached <= 0 {
		return nil
	}
	if cached < 0 {
		cached = 0
	}
	if cached > input {
		cached = input
	}
	// llm.Usage models cached input as a disjoint bucket (as Anthropic does).
	// OpenAI reports it as a subset of input_tokens, so subtract it here to
	// prevent Forebrain Harness's aggregate accounting from double-counting.
	return &llm.Usage{
		InputTokens:          int(input - cached),
		OutputTokens:         int(output),
		CacheReadInputTokens: int(cached),
	}
}

type suppressUserAgentRoundTripper struct {
	next http.RoundTripper
}

func (rt *suppressUserAgentRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	next := rt.next
	if next == nil {
		next = http.DefaultTransport
	}
	if req != nil {
		req.Header.Set("User-Agent", "")
	}
	return next.RoundTrip(req)
}

type openAIResponsesStreamingContextKey struct{}

func withOpenAIResponsesStreaming(ctx context.Context) context.Context {
	return context.WithValue(ctx, openAIResponsesStreamingContextKey{}, true)
}

// responsesContentTypeFixer rewrites Content-Type from text/event-stream to
// application/json for non-streaming responses. Some proxy servers incorrectly
// return text/event-stream even for synchronous JSON responses, which causes
// the OpenAI SDK to fail deserialization. Streaming requests (which set
// "stream":true in the body) are left untouched.
type responsesContentTypeFixer struct {
	next http.RoundTripper
}

func (rt *responsesContentTypeFixer) RoundTrip(req *http.Request) (*http.Response, error) {
	streamingRequest := responseRequestWantsStream(req)
	resp, err := rt.next.RoundTrip(req)
	if err != nil {
		return resp, err
	}
	ct := resp.Header.Get("Content-Type")
	if !streamingRequest && strings.HasPrefix(ct, "text/event-stream") && resp.ContentLength > 0 && resp.ContentLength < 1<<20 {
		resp.Header.Set("Content-Type", "application/json")
	}
	return resp, err
}

type responsesSSEFrameFilter struct {
	next http.RoundTripper
}

func (rt *responsesSSEFrameFilter) RoundTrip(req *http.Request) (*http.Response, error) {
	next := rt.next
	if next == nil {
		next = http.DefaultTransport
	}
	streamingRequest := responseRequestWantsStream(req)
	resp, err := next.RoundTrip(req)
	if err != nil || resp == nil || resp.Body == nil {
		return resp, err
	}
	if streamingRequest && strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		// Gateway error salvage: same pattern as openai_compat_llm.go.
		if resp.StatusCode >= 400 {
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
		resp.Body = newEmptySSEFrameFilter(resp.Body)
		resp.ContentLength = -1
		resp.Header.Del("Content-Length")
		return resp, nil
	}
	if resp.StatusCode >= 400 {
		return normalizeRawProviderErrorResponse(resp)
	}
	return resp, nil
}

func responseRequestWantsStream(req *http.Request) bool {
	if req == nil {
		return false
	}
	if streaming, _ := req.Context().Value(openAIResponsesStreamingContextKey{}).(bool); streaming {
		return true
	}
	if req.GetBody == nil {
		return false
	}
	body, err := req.GetBody()
	if err != nil {
		return false
	}
	defer body.Close()

	var payload map[string]json.RawMessage
	if err := json.NewDecoder(io.LimitReader(body, 1<<20)).Decode(&payload); err != nil {
		return false
	}
	var stream bool
	if raw, ok := payload["stream"]; ok {
		_ = json.Unmarshal(raw, &stream)
	}
	return stream
}

type emptySSEFrameFilter struct {
	rc     io.ReadCloser
	reader *bufio.Reader
	out    bytes.Buffer
	frame  bytes.Buffer
	data   bytes.Buffer
	eof    bool
}

func newEmptySSEFrameFilter(rc io.ReadCloser) io.ReadCloser {
	return &emptySSEFrameFilter{
		rc:     rc,
		reader: bufio.NewReaderSize(rc, 1<<20),
	}
}

func (f *emptySSEFrameFilter) Read(p []byte) (int, error) {
	for f.out.Len() == 0 && !f.eof {
		if err := f.readNextLine(); err != nil {
			if err == io.EOF {
				f.flushBufferedFrameAtEOF()
				f.eof = true
				break
			}
			return 0, err
		}
	}
	if f.out.Len() > 0 {
		return f.out.Read(p)
	}
	return 0, io.EOF
}

func (f *emptySSEFrameFilter) readNextLine() error {
	line, err := f.reader.ReadBytes('\n')
	if len(line) > 0 {
		f.consumeSSELine(line)
	}
	return err
}

func (f *emptySSEFrameFilter) consumeSSELine(line []byte) {
	trimmedLine := strings.TrimRight(string(line), "\r\n")
	if trimmedLine == "" {
		f.flushBufferedFrame(line)
		return
	}

	f.frame.Write(line)
	name, value, _ := strings.Cut(trimmedLine, ":")
	if name != "data" {
		return
	}
	if strings.HasPrefix(value, " ") {
		value = value[1:]
	}
	f.data.WriteString(value)
	f.data.WriteByte('\n')
}

func (f *emptySSEFrameFilter) flushBufferedFrame(terminator []byte) {
	if f.shouldForwardBufferedFrame() {
		f.out.Write(f.frame.Bytes())
		f.out.Write(terminator)
	}
	f.frame.Reset()
	f.data.Reset()
}

func (f *emptySSEFrameFilter) shouldForwardBufferedFrame() bool {
	data := bytes.TrimSpace(f.data.Bytes())
	if len(data) == 0 {
		if eventName := sseFrameEventName(f.frame.Bytes()); eventName != "" {
			slog.Error("openai responses SSE frame ignored", "event", eventName, "data_bytes", 0, "err", "missing data")
		}
		return false
	}
	if isCodexSSEMetadataFrame(f.frame.Bytes(), data) {
		return false
	}
	if bytes.HasPrefix(data, []byte("[DONE]")) {
		return true
	}
	if !json.Valid(data) {
		logIgnoredOpenAIResponsesSSEFrame(f.frame.Bytes(), data, fmt.Errorf("invalid JSON"))
		return false
	}
	if hasOpenAIResponsesProviderError(data) {
		return true
	}
	var event responses.ResponseStreamEventUnion
	if err := json.Unmarshal(data, &event); err != nil {
		logIgnoredOpenAIResponsesSSEFrame(f.frame.Bytes(), data, err)
		return false
	}
	return true
}

func logIgnoredOpenAIResponsesSSEFrame(frame, data []byte, err error) {
	eventName := sseFrameEventName(frame)
	if eventName == "" {
		var envelope struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(data, &envelope) == nil {
			eventName = strings.TrimSpace(envelope.Type)
		}
	}
	if eventName == "" {
		eventName = "(unnamed)"
	}
	slog.Error("openai responses SSE frame ignored", "event", eventName, "data_bytes", len(data), "err", err)
}

func sseFrameEventName(frame []byte) string {
	for _, line := range bytes.Split(frame, []byte{'\n'}) {
		name, value, _ := bytes.Cut(bytes.TrimSpace(line), []byte{':'})
		if string(name) == "event" {
			return strings.TrimSpace(string(value))
		}
	}
	return ""
}

func hasOpenAIResponsesProviderError(data []byte) bool {
	var envelope map[string]json.RawMessage
	if json.Unmarshal(data, &envelope) != nil {
		return false
	}
	_, ok := envelope["error"]
	return ok
}

func isCodexSSEMetadataFrame(frame, data []byte) bool {
	if strings.HasPrefix(sseFrameEventName(frame), "codex.") {
		return true
	}

	var envelope struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(bytes.TrimSpace(data), &envelope) == nil && strings.HasPrefix(envelope.Type, "codex.")
}

func (f *emptySSEFrameFilter) flushBufferedFrameAtEOF() {
	terminator := []byte("\n")
	if !bytes.HasSuffix(f.frame.Bytes(), []byte("\n")) {
		terminator = []byte("\n\n")
	}
	// The OpenAI SDK's SSE decoder only dispatches an event after a blank line.
	// Providers can interrupt a stream after writing the final data line, so add
	// that terminator instead of dropping the provider error frame at EOF.
	f.flushBufferedFrame(terminator)
}

func (f *emptySSEFrameFilter) Close() error {
	return f.rc.Close()
}

func (c *openAIResponsesLLM) Execute(ctx context.Context, messages []llm.Message, tools []*llm.Tool) (outResult *llm.Result, outErr error) {
	// Every error leaving this package is normalized here rather than at each
	// return, so a path added later cannot leak a raw SDK error to Layer 0.
	defer func() { outErr = normalizeError(outErr) }()
	ctx, finish := llm.WithRawCacheUsage(ctx)
	params := c.newResponseParams(ctx, messages, tools, false)
	if c.stream {
		result, err := c.executeStreaming(ctx, params)
		return finish(result), err
	}
	result, err := c.executeSync(ctx, params)
	return finish(result), err
}

func (c *openAIResponsesLLM) ExecuteStructured(ctx context.Context, messages []llm.Message, spec llm.StructuredOutputSpec) (outResult *llm.Result, outErr error) {
	defer func() { outErr = normalizeError(outErr) }()
	if c == nil {
		return nil, fmt.Errorf("nil OpenAI Responses client")
	}
	if !spec.Strict {
		return nil, fmt.Errorf("structured output must be strict")
	}
	if strings.TrimSpace(spec.Name) == "" || len(spec.Schema) == 0 {
		return nil, fmt.Errorf("structured output name and schema are required")
	}
	ctx, finish := llm.WithRawCacheUsage(ctx)
	params := c.newResponseParams(ctx, messages, nil, false)
	params.Text = responses.ResponseTextConfigParam{
		Format: responses.ResponseFormatTextConfigUnionParam{
			OfJSONSchema: &responses.ResponseFormatTextJSONSchemaConfigParam{
				Name:        strings.TrimSpace(spec.Name),
				Description: openaigo.String(strings.TrimSpace(spec.Description)),
				Schema:      spec.Schema,
				Strict:      openaigo.Bool(true),
			},
		},
	}
	// Internal structured calls must not emit deltas into the user-facing stream.
	result, err := c.executeSync(ctx, params)
	return finish(result), err
}

func (c *openAIResponsesLLM) newResponseParams(ctx context.Context, messages []llm.Message, tools []*llm.Tool, compactionTrigger bool) responses.ResponseNewParams {
	input := make(responses.ResponseInputParam, 0, len(messages))
	rawInput := make([]any, 0, len(messages)+1)
	hasOpaqueInput := compactionTrigger
	for _, msg := range messages {
		if msg.AgentMessage != nil {
			rawInput = append(rawInput, agentMessageRawInput(msg.AgentMessage))
			hasOpaqueInput = true
			continue
		}
		if msg.Compaction != nil {
			wireType := strings.TrimSpace(msg.Compaction.Type)
			if wireType == "" {
				wireType = "compaction"
			}
			rawInput = append(rawInput, map[string]any{
				"type":              wireType,
				"id":                strings.TrimSpace(msg.Compaction.ID),
				"encrypted_content": msg.Compaction.EncryptedContent,
			})
			hasOpaqueInput = true
			continue
		}
		before := len(input)
		switch msg.Role {
		case llm.RoleSystem:
			content := msg.TextContent()
			if content == "" {
				continue
			}
			input = append(input, responses.ResponseInputItemParamOfMessage(content, responses.EasyInputMessageRoleSystem))
		case llm.RoleDeveloper:
			content := msg.TextContent()
			if content == "" {
				continue
			}
			input = append(input, responses.ResponseInputItemParamOfMessage(content, responses.EasyInputMessageRoleDeveloper))
		case llm.RoleAssistant:
			if len(msg.ToolCalls) > 0 {
				for _, tc := range msg.ToolCalls {
					if strings.TrimSpace(tc.Function.Name) == "" || strings.TrimSpace(tc.ID) == "" {
						continue
					}
					input = append(input, responses.ResponseInputItemParamOfFunctionCall(llm.SanitizeToolCallArguments(tc.Function.Arguments), tc.ID, tc.Function.Name))
				}
			} else if items := responseReasoningInputItemsFromMessage(msg); len(items) > 0 {
				input = append(input, items...)
			} else if content := msg.TextContent(); content != "" {
				input = append(input, responses.ResponseInputItemParamOfMessage(content, responses.EasyInputMessageRoleAssistant))
			}
		case llm.RoleTool:
			content := msg.TextContent()
			callID := strings.TrimSpace(msg.ToolCallID)
			if content == "" || callID == "" {
				continue
			}
			input = append(input, responses.ResponseInputItemParamOfFunctionCallOutput(callID, content))
		default:
			content := openAIResponsesUserContent(msg.Parts)
			if len(content) == 0 {
				continue
			}
			input = append(input, responses.ResponseInputItemParamOfMessage(content, responses.EasyInputMessageRoleUser))
		}
		for _, item := range input[before:] {
			var raw any
			if b, err := json.Marshal(item); err == nil && json.Unmarshal(b, &raw) == nil {
				rawInput = append(rawInput, raw)
			}
		}
	}
	if compactionTrigger {
		rawInput = append(rawInput, map[string]any{"type": "compaction_trigger"})
	}

	params := responses.ResponseNewParams{
		Model: c.model,
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: input,
		},
		Reasoning: shared.ReasoningParam{
			Effort:  shared.ReasoningEffortMedium,
			Summary: shared.ReasoningSummaryAuto,
		},
		// The Responses API stores responses server-side by default. Forebrain Harness
		// never references a stored response — it resends the full transcript
		// every turn and uses no previous_response_id — so storing them buys
		// nothing and leaves user transcripts on the provider for no reason.
		// The ChatGPT/Codex backend goes further and rejects the request
		// outright with "Store must be set to false", which made that whole
		// provider unusable.
		Store: openaigo.Bool(false),
	}
	// The Codex backend rejects max_output_tokens outright ("Unsupported
	// parameter"), so it is only sent to endpoints that accept it.
	if !c.codex {
		params.MaxOutputTokens = openaigo.Int(c.maxTokens)
	}
	if key := openAIPromptCacheKey(ctx); c.cacheKey && key != "" {
		params.PromptCacheKey = openaigo.String(key)
	}
	extra := cloneJSONMap(c.paramsOverlay)
	if hasOpaqueInput {
		if extra == nil {
			extra = map[string]any{}
		}
		extra["input"] = rawInput
	}
	if len(extra) > 0 {
		params.SetExtraFields(extra)
	}
	ensureReasoningEncryptedContentIncluded(&params)

	if len(tools) > 0 {
		toolParams := make([]responses.ToolUnionParam, 0, len(tools))
		for _, tool := range tools {
			if tool == nil {
				continue
			}
			// The provider's built-in tool replaces Forebrain Harness's provider-backed web_search
			// function. Advertising both makes the model choose unpredictably
			// and can route a ChatGPT-authenticated search through an unconfigured
			// Tavily/Brave/Baidu provider.
			if c.webSearch.enabled() && tool.Name() == "web_search" {
				continue
			}
			toolParams = append(toolParams, responses.ToolUnionParam{
				OfFunction: &responses.FunctionToolParam{
					Name:        tool.Name(),
					Description: openaigo.String(tool.Description()),
					Parameters:  tool.InputSchema(),
					// Tool arguments stay non-strict (see compatOpenAITools).
					Strict: openaigo.Bool(false),
				},
			})
		}
		params.Tools = toolParams
	}
	if c.webSearch.enabled() {
		params.Tools = append([]responses.ToolUnionParam{
			responses.ToolParamOfWebSearchPreview(responses.WebSearchToolTypeWebSearchPreview),
		}, params.Tools...)
	}

	return params
}

func agentMessageRawInput(message *llm.AgentMessageState) map[string]any {
	content := make([]map[string]any, 0, len(message.Content))
	for _, item := range message.Content {
		switch item.Type {
		case "input_text":
			content = append(content, map[string]any{"type": "input_text", "text": item.Text})
		case "encrypted_content":
			content = append(content, map[string]any{"type": "encrypted_content", "encrypted_content": item.EncryptedContent})
		}
	}
	out := map[string]any{"type": "agent_message", "author": message.Author, "recipient": message.Recipient, "content": content}
	if id := strings.TrimSpace(message.ID); id != "" {
		out["id"] = id
	}
	return out
}

func (c *openAIResponsesLLM) Compact(ctx context.Context, messages []llm.Message, tools []*llm.Tool, mode llm.CompactMode) (*llm.CompactResult, error) {
	switch mode {
	case llm.CompactModeRemoteV2:
		params := c.newResponseParams(ctx, messages, tools, true)
		// A synchronous request gives the same replacement semantics and avoids
		// surfacing compaction bytes through the ordinary assistant stream.
		resp, err := c.remoteV2CompactResponse(ctx, params)
		if err != nil {
			return nil, err
		}
		if err := validateRemoteV2CompactionStatus(resp.Object, resp.Status); err != nil {
			return nil, err
		}
		items := remoteCompactionItems(resp.Output)
		if len(items) != 1 {
			return nil, fmt.Errorf("openai responses remote v2: expected exactly one compaction item, got %d", len(items))
		}
		item := items[0]
		if strings.TrimSpace(item.EncryptedContent) == "" {
			return nil, errors.New("openai responses remote v2: compaction checkpoint missing encrypted_content")
		}
		replacement := compactWireOutputToReplacement(resp.Output)
		if len(replacement) == 1 && replacement[0].Compaction != nil {
			replacement = nil
		}
		if len(replacement) == 0 {
			replacement = retainedRemoteMessages(messages, 64_000)
			replacement = append(replacement, llm.Message{Compaction: &llm.CompactionState{
				Type: item.Type, ID: item.ID, EncryptedContent: item.EncryptedContent,
			}})
		}
		return &llm.CompactResult{Messages: replacement, Usage: openAIUsage(resp.Usage.InputTokens, resp.Usage.OutputTokens, resp.Usage.InputTokensDetails.CachedTokens)}, nil
	case llm.CompactModeRemoteV1:
		params := c.newResponseParams(ctx, messages, tools, false)
		var response struct {
			Output []json.RawMessage `json:"output"`
			Usage  struct {
				InputTokens        int `json:"input_tokens"`
				OutputTokens       int `json:"output_tokens"`
				InputTokensDetails struct {
					CachedTokens int `json:"cached_tokens"`
				} `json:"input_tokens_details"`
			} `json:"usage"`
		}
		if err := c.client.Post(ctx, "responses/compact", params, &response); err != nil {
			return nil, err
		}
		replacement := compactWireOutputToReplacement(response.Output)
		if len(replacement) == 0 {
			return nil, errors.New("openai responses remote v1: empty replacement history")
		}
		return &llm.CompactResult{Messages: replacement, Usage: openAIUsage(int64(response.Usage.InputTokens), int64(response.Usage.OutputTokens), int64(response.Usage.InputTokensDetails.CachedTokens))}, nil
	default:
		return nil, fmt.Errorf("unsupported remote compaction mode %q", mode)
	}
}

type remoteV2CompactResponse struct {
	Object string            `json:"object"`
	Status string            `json:"status"`
	Output []json.RawMessage `json:"output"`
	Usage  struct {
		InputTokens        int64 `json:"input_tokens"`
		OutputTokens       int64 `json:"output_tokens"`
		InputTokensDetails struct {
			CachedTokens int64 `json:"cached_tokens"`
		} `json:"input_tokens_details"`
	} `json:"usage"`
}

type remoteCompactionItem struct {
	Type             string `json:"type"`
	ID               string `json:"id"`
	EncryptedContent string `json:"encrypted_content"`
}

func (c *openAIResponsesLLM) remoteV2CompactResponse(ctx context.Context, params responses.ResponseNewParams) (*remoteV2CompactResponse, error) {
	var resp remoteV2CompactResponse
	if err := c.client.Post(ctx, "responses", params, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func validateRemoteV2CompactionStatus(object, status string) error {
	object = strings.TrimSpace(object)
	status = strings.TrimSpace(status)
	if status == "" {
		if object == "response.compaction" {
			return nil
		}
		return fmt.Errorf("openai responses remote v2: response ended with status %q", status)
	}
	if status != string(responses.ResponseStatusCompleted) {
		return fmt.Errorf("openai responses remote v2: response ended with status %q", status)
	}
	return nil
}

func remoteCompactionItems(rawItems []json.RawMessage) []remoteCompactionItem {
	items := make([]remoteCompactionItem, 0, 1)
	for _, raw := range rawItems {
		var item remoteCompactionItem
		if json.Unmarshal(raw, &item) != nil {
			continue
		}
		item.Type = strings.TrimSpace(item.Type)
		switch item.Type {
		case "compaction", "compaction_summary":
			item.ID = strings.TrimSpace(item.ID)
			items = append(items, item)
		}
	}
	return items
}

func compactWireOutputToReplacement(items []json.RawMessage) []llm.Message {
	out := make([]llm.Message, 0, len(items))
	for _, raw := range items {
		var item struct {
			Type             string `json:"type"`
			ID               string `json:"id"`
			Role             string `json:"role"`
			Author           string `json:"author"`
			Recipient        string `json:"recipient"`
			EncryptedContent string `json:"encrypted_content"`
			Content          []struct {
				Type             string `json:"type"`
				Text             string `json:"text"`
				ImageURL         string `json:"image_url"`
				EncryptedContent string `json:"encrypted_content"`
			} `json:"content"`
		}
		if json.Unmarshal(raw, &item) != nil {
			continue
		}
		switch item.Type {
		case "compaction", "compaction_summary":
			if strings.TrimSpace(item.EncryptedContent) == "" {
				continue
			}
			out = append(out, llm.Message{Compaction: &llm.CompactionState{Type: item.Type, ID: item.ID, EncryptedContent: item.EncryptedContent}})
		case "message":
			role := strings.TrimSpace(item.Role)
			if role != llm.RoleUser && role != llm.RoleAssistant {
				continue
			}
			parts := make([]llm.ContentPart, 0, len(item.Content))
			for _, content := range item.Content {
				switch content.Type {
				case "input_text", "output_text":
					parts = append(parts, llm.Text(content.Text))
				case "input_image":
					if content.ImageURL != "" {
						parts = append(parts, llm.ImageURL(content.ImageURL))
					}
				}
			}
			out = append(out, llm.Message{Role: role, Parts: parts})
		case "agent_message":
			content := make([]llm.AgentMessageContent, 0, len(item.Content))
			for _, part := range item.Content {
				if part.Type == "input_text" || part.Type == "encrypted_content" {
					content = append(content, llm.AgentMessageContent{Type: part.Type, Text: part.Text, EncryptedContent: part.EncryptedContent})
				}
			}
			out = append(out, llm.Message{AgentMessage: &llm.AgentMessageState{
				ID: item.ID, Author: item.Author, Recipient: item.Recipient, Content: content,
			}})
		}
	}
	return out
}

func retainedRemoteMessages(messages []llm.Message, budget int) []llm.Message {
	selected, remaining := make([]llm.Message, 0, 8), budget
	for i := len(messages) - 1; i >= 0; i-- {
		if remaining <= 0 {
			continue
		}
		msg := messages[i]
		if msg.AgentMessage != nil {
			if agentMessageIsFinalAnswer(msg.AgentMessage) {
				continue
			}
			cost := max(1, agentMessageTokenCount(msg.AgentMessage))
			if cost > 10_000 || cost > remaining {
				continue
			}
			remaining -= cost
			selected = append(selected, cloneRemoteCompactMessage(msg))
			continue
		}
		if msg.Role != llm.RoleUser || msg.IsMeta {
			continue
		}
		cost := max(1, remoteMessageTextTokenCount(msg))
		if cost > remaining {
			selected = append(selected, truncateRemoteUserMessage(msg, remaining))
			break
		}
		remaining -= cost
		selected = append(selected, cloneRemoteCompactMessage(msg))
	}
	for i, j := 0, len(selected)-1; i < j; i, j = i+1, j-1 {
		selected[i], selected[j] = selected[j], selected[i]
	}
	return selected
}

func remoteMessageTextTokenCount(message llm.Message) int {
	total := 0
	for _, part := range message.Parts {
		if part.Type == llm.ContentTypeText {
			total += llm.EstimateText(part.Text)
		}
	}
	return total
}

func truncateRemoteUserMessage(message llm.Message, budget int) llm.Message {
	remaining := max(0, budget)
	parts := make([]llm.ContentPart, 0, len(message.Parts))
	for _, part := range message.Parts {
		if part.Type != llm.ContentTypeText {
			parts = append(parts, part)
			continue
		}
		if remaining <= 0 {
			continue
		}
		cost := llm.EstimateText(part.Text)
		if cost <= remaining {
			remaining -= cost
			parts = append(parts, part)
			continue
		}
		part.Text = truncateRemoteText(part.Text, remaining)
		parts = append(parts, part)
		remaining = 0
	}
	message.Parts = parts
	return cloneRemoteCompactMessage(message)
}

func agentMessageIsFinalAnswer(message *llm.AgentMessageState) bool {
	return message != nil && len(message.Content) > 0 && message.Content[0].Type == "input_text" && strings.HasPrefix(message.Content[0].Text, "Message Type: FINAL_ANSWER\n")
}

func agentMessageTokenCount(message *llm.AgentMessageState) int {
	if message == nil {
		return 0
	}
	total := 0
	for _, item := range message.Content {
		total += llm.EstimateText(item.Text + item.EncryptedContent)
	}
	return total
}

func cloneRemoteCompactMessage(message llm.Message) llm.Message {
	message.Parts = append([]llm.ContentPart(nil), message.Parts...)
	message.ToolCalls = append([]llm.ToolCall(nil), message.ToolCalls...)
	if message.AgentMessage != nil {
		state := *message.AgentMessage
		state.Content = append([]llm.AgentMessageContent(nil), message.AgentMessage.Content...)
		message.AgentMessage = &state
	}
	return message
}

func truncateRemoteText(text string, budget int) string {
	maxBytes := max(0, budget*4)
	if maxBytes > 0 && len(text) <= maxBytes {
		return text
	}
	leftEnd := compactUTF8PrefixBytes(text, maxBytes/2)
	rightStart := compactUTF8SuffixStart(text, maxBytes-maxBytes/2)
	if rightStart < leftEnd {
		rightStart = leftEnd
	}
	removedTokens := max(0, (len(text)-maxBytes+3)/4)
	return text[:leftEnd] + fmt.Sprintf("…%d tokens truncated…", removedTokens) + text[rightStart:]
}

func compactUTF8PrefixBytes(text string, budget int) int {
	if budget <= 0 {
		return 0
	}
	if len(text) <= budget {
		return len(text)
	}
	end := 0
	for i := range text {
		if i > budget {
			break
		}
		end = i
	}
	return end
}

func compactUTF8SuffixStart(text string, budget int) int {
	if budget <= 0 {
		return len(text)
	}
	target := len(text) - budget
	if target <= 0 {
		return 0
	}
	for i := range text {
		if i >= target {
			return i
		}
	}
	return len(text)
}

func (c *openAIResponsesLLM) executeSync(ctx context.Context, params responses.ResponseNewParams) (*llm.Result, error) {
	resp, err := c.client.Responses.New(ctx, params)
	if err != nil {
		return nil, err
	}

	var content string
	var reasoningItems []llm.Item
	var toolCalls []llm.ToolCall
	if len(resp.Output) > 0 {
		for _, item := range resp.Output {
			// Extract text content
			if text := extractTextFromOutputItem(item); text != "" && content == "" {
				content = text
			}
			if reasoningItem, ok := extractReasoningCarryItem(item); ok {
				reasoningItems = appendOrReplaceReasoningItem(reasoningItems, reasoningItem)
			}
			// Extract function calls
			if item.Type == "function_call" {
				toolCalls = append(toolCalls, llm.ToolCall{
					ID:   item.CallID,
					Type: "function",
					Function: llm.FunctionCall{
						Name:      item.Name,
						Arguments: item.Arguments,
					},
				})
			}
		}
	}
	reasoning := llm.SummaryText(reasoningItems)

	parts := []llm.ContentPart{}
	if content != "" {
		parts = append(parts, llm.Text(content))
	} else if len(toolCalls) == 0 && strings.TrimSpace(reasoning) != "" {
		parts = append(parts, llm.Text("```thinking\n"+reasoning+"\n```"))
	}

	msg := llm.Message{
		Role:      llm.RoleAssistant,
		Parts:     parts,
		ToolCalls: toolCalls,
	}
	if carry := llm.Encode(reasoningItems); carry != "" {
		msg.Name = carry
	}
	result := &llm.Result{Message: &msg}
	result.Usage = openAIUsage(
		resp.Usage.InputTokens,
		resp.Usage.OutputTokens,
		resp.Usage.InputTokensDetails.CachedTokens,
	)
	if resp.Status == responses.ResponseStatusCompleted && len(parts) == 0 && len(toolCalls) == 0 {
		return nil, llm.WithUsageError(fmt.Errorf(
			"openai responses completed without output: id=%s model=%s status=%s input_tokens=%d output_tokens=%d",
			strings.TrimSpace(resp.ID),
			strings.TrimSpace(string(resp.Model)),
			strings.TrimSpace(string(resp.Status)),
			resp.Usage.InputTokens,
			resp.Usage.OutputTokens,
		), result.Usage)
	}
	return result, nil
}

func openAIResponsesUserContent(parts []llm.ContentPart) responses.ResponseInputMessageContentListParam {
	content := make(responses.ResponseInputMessageContentListParam, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case llm.ContentTypeText:
			if strings.TrimSpace(part.Text) != "" {
				content = append(content, responses.ResponseInputContentParamOfInputText(part.Text))
			}
		case llm.ContentTypeImageURL:
			imageURL := strings.TrimSpace(part.ImageURL)
			if imageURL == "" {
				continue
			}
			image := responses.ResponseInputContentParamOfInputImage(responses.ResponseInputImageDetailAuto)
			image.OfInputImage.ImageURL = openaigo.String(imageURL)
			content = append(content, image)
		case llm.ContentTypeImageBase64:
			mimeType := strings.TrimSpace(part.MIMEType)
			data := strings.TrimSpace(part.ImageBase64)
			if mimeType == "" || data == "" {
				continue
			}
			image := responses.ResponseInputContentParamOfInputImage(responses.ResponseInputImageDetailAuto)
			image.OfInputImage.ImageURL = openaigo.String("data:" + mimeType + ";base64," + data)
			content = append(content, image)
		}
	}
	return content
}

func (c *openAIResponsesLLM) executeStreaming(ctx context.Context, params responses.ResponseNewParams) (*llm.Result, error) {
	sink := llm.StreamSinkFrom(ctx)
	ctx = withOpenAIResponsesStreaming(ctx)
	stream := c.client.Responses.NewStreaming(ctx, params)
	defer stream.Close()

	var textBuf strings.Builder
	var reasoningItems []llm.Item
	streamedReasoningByItem := make(map[string]*streamedReasoningState)
	var usage *llm.Usage
	var reportedIn, reportedOut int
	var toolCalls []llm.ToolCall
	toolCallsMap := make(map[string]*llm.ToolCall)
	var toolCallOrder []string
	toolCallIDByItemID := make(map[string]string)
	webSearchIDs := make(map[string]bool)
	responseStarted := false

	if sink != nil && sink.Streamed != nil {
		*sink.Streamed = true
	}
	defer func() {
		if sink != nil && sink.OnEnd != nil {
			sink.OnEnd()
		}
	}()

	for stream.Next() {
		evt := stream.Current()
		switch evt.Type {
		case "response.output_text.delta":
			delta := evt.Delta.OfString
			if delta != "" {
				llm.NotifyResponseStarted(sink, &responseStarted)
			}
			textBuf.WriteString(delta)
			if sink != nil && sink.OnDelta != nil && delta != "" {
				sink.OnDelta(delta)
			}
		case "response.reasoning_summary_text.delta":
			deltaEvt := evt.AsResponseReasoningSummaryTextDelta()
			delta := deltaEvt.Delta
			if delta != "" {
				llm.NotifyResponseStarted(sink, &responseStarted)
			}
			appendReasoningSummaryDelta(streamedReasoningByItem, deltaEvt.ItemID, int(deltaEvt.SummaryIndex), delta)
			if sink != nil && sink.OnReasoningDelta != nil && delta != "" {
				sink.OnReasoningDelta(delta)
			}
		case "response.reasoning_summary_text.done":
			done := evt.AsResponseReasoningSummaryTextDone()
			state := ensureStreamedReasoningState(streamedReasoningByItem, done.ItemID)
			ensureReasoningSummaryIndex(state, int(done.SummaryIndex))
			summaryIdx := int(done.SummaryIndex)
			wasEmpty := state.Summary[summaryIdx] == ""
			wasDone := state.Done[summaryIdx]
			if done.Text != "" {
				if wasEmpty {
					llm.NotifyResponseStarted(sink, &responseStarted)
				}
				state.Summary[summaryIdx] = done.Text
				if wasEmpty && sink != nil && sink.OnReasoningDelta != nil {
					sink.OnReasoningDelta(done.Text)
				}
			}
			if !wasDone {
				state.Done[summaryIdx] = true
			}
			if !wasDone && sink != nil && sink.OnReasoningDone != nil {
				sink.OnReasoningDone()
			}
		case "response.output_item.added":
			added := evt.AsResponseOutputItemAdded()
			if added.Item.Type == "web_search_call" {
				llm.NotifyResponseStarted(sink, &responseStarted)
				llm.NotifyExternalContext(ctx)
				webSearchIDs[added.Item.ID] = true
				if sink != nil && sink.OnWebSearch != nil {
					sink.OnWebSearch(added.Item.ID, "", false)
				}
				continue
			}
			if added.Item.Type == "function_call" {
				tc := llm.ToolCall{
					ID:   added.Item.CallID,
					Type: "function",
					Function: llm.FunctionCall{
						Name:      added.Item.Name,
						Arguments: added.Item.Arguments,
					},
				}
				toolCallsMap[added.Item.CallID] = &tc
				toolCallOrder = append(toolCallOrder, added.Item.CallID)
				if added.Item.ID != "" {
					toolCallIDByItemID[added.Item.ID] = added.Item.CallID
				}
				continue
			}
			if reasoningItem, ok := extractReasoningCarryItem(added.Item); ok {
				reasoningItems = appendOrReplaceReasoningItem(reasoningItems, reasoningItem)
				mergeReasoningSummaryState(streamedReasoningByItem, reasoningItem)
			}
		case "response.function_call_arguments.delta":
			delta := evt.AsResponseFunctionCallArgumentsDelta()
			callID := toolCallIDByItemID[delta.ItemID]
			if callID == "" {
				continue
			}
			if tc, ok := toolCallsMap[callID]; ok {
				tc.Function.Arguments += delta.Delta
			}
		case "response.function_call_arguments.done":
			done := evt.AsResponseFunctionCallArgumentsDone()
			callID := toolCallIDByItemID[done.ItemID]
			if callID == "" {
				continue
			}
			if tc, ok := toolCallsMap[callID]; ok {
				tc.Function.Arguments = done.Arguments
			}
		case "response.output_item.done":
			done := evt.AsResponseOutputItemDone()
			if done.Item.Type == "web_search_call" && webSearchIDs[done.Item.ID] {
				if sink != nil && sink.OnWebSearch != nil {
					sink.OnWebSearch(done.Item.ID, webSearchActionDetail(done.Item.RawJSON()), true)
				}
				continue
			}
			if reasoningItem, ok := extractReasoningCarryItem(done.Item); ok {
				reasoningItems = appendOrReplaceReasoningItem(reasoningItems, reasoningItem)
				mergeReasoningSummaryState(streamedReasoningByItem, reasoningItem)
			}
		case "error":
			streamErr := evt.AsError()
			return nil, newOpenAIResponsesStreamError(evt.Type, streamErr.Code, streamErr.Message)
		case "response.failed":
			failed := evt.AsResponseFailed()
			return nil, newOpenAIResponsesStreamError(evt.Type, string(failed.Response.Error.Code), failed.Response.Error.Message)
		case "response.completed":
			if evt.Response.Usage.InputTokens > 0 || evt.Response.Usage.OutputTokens > 0 {
				usage = openAIUsage(
					evt.Response.Usage.InputTokens,
					evt.Response.Usage.OutputTokens,
					evt.Response.Usage.InputTokensDetails.CachedTokens,
				)
				if sink != nil && sink.OnUsage != nil {
					totalInput := usage.InputTokens + usage.CacheReadInputTokens
					deltaIn := totalInput - reportedIn
					deltaOut := usage.OutputTokens - reportedOut
					if deltaIn > 0 || deltaOut > 0 {
						sink.OnUsage(deltaIn, deltaOut)
						reportedIn = totalInput
						reportedOut = usage.OutputTokens
					}
				}
				if sink != nil && sink.OnUsageSnapshot != nil {
					totalInput := usage.InputTokens + usage.CacheReadInputTokens
					sink.OnUsageSnapshot(totalInput, usage.OutputTokens)
				}
			}
			// Extract final output if text buffer is empty
			if textBuf.Len() == 0 {
				for _, item := range evt.Response.Output {
					if content := extractTextFromOutputItem(item); content != "" {
						llm.NotifyResponseStarted(sink, &responseStarted)
						textBuf.WriteString(content)
						if sink != nil && sink.OnDelta != nil {
							sink.OnDelta(content)
						}
						break
					}
				}
			}
			for _, item := range evt.Response.Output {
				if reasoningItem, ok := extractReasoningCarryItem(item); ok {
					reasoningItems = appendOrReplaceReasoningItem(reasoningItems, reasoningItem)
					mergeReasoningSummaryState(streamedReasoningByItem, reasoningItem)
				}
			}
			// Collect final function calls from response
			for _, item := range evt.Response.Output {
				if item.Type == "function_call" {
					if _, exists := toolCallsMap[item.CallID]; !exists {
						toolCallsMap[item.CallID] = &llm.ToolCall{
							ID:   item.CallID,
							Type: "function",
							Function: llm.FunctionCall{
								Name:      item.Name,
								Arguments: item.Arguments,
							},
						}
						toolCallOrder = append(toolCallOrder, item.CallID)
					}
				}
			}
		}
	}
	if err := stream.Err(); err != nil {
		return nil, err
	}
	reasoningItems = mergeStreamedReasoningItems(reasoningItems, streamedReasoningByItem)
	reasoningText := llm.SummaryText(reasoningItems)

	// Preserve provider emission order. Iterating the map here used to
	// randomize function-call order in the persisted assistant message, which
	// made the following request's otherwise-identical prefix byte-unstable.
	for _, callID := range toolCallOrder {
		if tc := toolCallsMap[callID]; tc != nil {
			toolCalls = append(toolCalls, *tc)
		}
	}

	parts := []llm.ContentPart{}
	if textBuf.Len() > 0 {
		parts = append(parts, llm.Text(textBuf.String()))
	} else if len(toolCalls) == 0 && strings.TrimSpace(reasoningText) != "" {
		parts = append(parts, llm.Text("```thinking\n"+reasoningText+"\n```"))
	}

	msg := llm.Message{
		Role:      llm.RoleAssistant,
		Parts:     parts,
		ToolCalls: toolCalls,
	}
	if carry := llm.Encode(reasoningItems); carry != "" {
		msg.Name = carry
	}
	return &llm.Result{Message: &msg, Usage: usage}, nil
}

func webSearchActionDetail(raw string) string {
	var item struct {
		Action struct {
			Query   string   `json:"query"`
			Queries []string `json:"queries"`
			URL     string   `json:"url"`
			Pattern string   `json:"pattern"`
			Type    string   `json:"type"`
		} `json:"action"`
	}
	if json.Unmarshal([]byte(raw), &item) != nil {
		return ""
	}
	if item.Action.Query != "" {
		return item.Action.Query
	}
	if len(item.Action.Queries) > 0 {
		detail := item.Action.Queries[0]
		if len(item.Action.Queries) > 1 {
			detail += " ..."
		}
		return detail
	}
	if item.Action.Type == "open_page" || item.Action.Type == "find" || item.Action.Type == "find_in_page" {
		if item.Action.Pattern != "" {
			return fmt.Sprintf("'%s' in %s", item.Action.Pattern, item.Action.URL)
		}
		return item.Action.URL
	}
	return ""
}

func newOpenAIResponsesStreamError(eventType, code, message string) error {
	eventType = strings.TrimSpace(eventType)
	if eventType == "" {
		eventType = "error"
	}
	code = strings.TrimSpace(code)
	message = strings.TrimSpace(message)
	switch {
	case code != "" && message != "":
		return fmt.Errorf("openai responses stream %s (%s): %s", eventType, code, message)
	case message != "":
		return fmt.Errorf("openai responses stream %s: %s", eventType, message)
	case code != "":
		return fmt.Errorf("openai responses stream %s (%s)", eventType, code)
	default:
		return fmt.Errorf("openai responses stream %s", eventType)
	}
}

func extractTextFromOutputItem(item responses.ResponseOutputItemUnion) string {
	// Check if this is a message type output
	if item.Type == "message" && len(item.Content) > 0 {
		// Extract text from the first content item
		for _, content := range item.Content {
			// Content is also a union type, check for text or output_text
			llm.LogDebug("openai_responses", fmt.Sprintf("content.Type=%s text_length=%d", content.Type, len(content.Text)))
			if content.Type == "text" || content.Type == "output_text" {
				return content.Text
			}
		}
	}
	return ""
}

type streamedReasoningState struct {
	Summary []string
	Done    []bool
}

func ensureReasoningEncryptedContentIncluded(params *responses.ResponseNewParams) {
	if params == nil {
		return
	}
	if slices.Contains(params.Include, responses.ResponseIncludableReasoningEncryptedContent) {
		return
	}
	params.Include = append(params.Include, responses.ResponseIncludableReasoningEncryptedContent)
}

func responseReasoningInputItemsFromMessage(msg llm.Message) []responses.ResponseInputItemUnionParam {
	payload, ok := llm.Decode(msg.Name)
	if !ok {
		return nil
	}
	items := make([]responses.ResponseInputItemUnionParam, 0, len(payload.Items))
	for _, item := range payload.Items {
		summaryParts := make([]responses.ResponseReasoningItemSummaryParam, 0, len(item.Summary))
		for _, text := range item.Summary {
			if text == "" {
				continue
			}
			summaryParts = append(summaryParts, responses.ResponseReasoningItemSummaryParam{Text: text})
		}
		if len(summaryParts) == 0 && strings.TrimSpace(item.EncryptedContent) == "" {
			continue
		}
		param := responses.ResponseInputItemParamOfReasoning(item.ID, summaryParts)
		if param.OfReasoning != nil && strings.TrimSpace(item.EncryptedContent) != "" {
			param.OfReasoning.EncryptedContent = openaigo.String(strings.TrimSpace(item.EncryptedContent))
		}
		items = append(items, param)
	}
	return items
}

func extractReasoningCarryItem(item responses.ResponseOutputItemUnion) (llm.Item, bool) {
	if item.Type != "reasoning" {
		return llm.Item{}, false
	}
	summary := make([]string, 0, len(item.Summary))
	for _, part := range item.Summary {
		if part.Text == "" {
			continue
		}
		summary = append(summary, part.Text)
	}
	encrypted := strings.TrimSpace(item.EncryptedContent)
	if len(summary) == 0 && encrypted == "" {
		return llm.Item{}, false
	}
	return llm.Item{
		ID:               strings.TrimSpace(item.ID),
		Summary:          summary,
		EncryptedContent: encrypted,
	}, true
}

func appendOrReplaceReasoningItem(items []llm.Item, next llm.Item) []llm.Item {
	if next.ID != "" {
		for i := range items {
			if items[i].ID == next.ID {
				items[i] = mergeReasoningItems(items[i], next)
				return items
			}
		}
	}
	return append(items, next)
}

func mergeReasoningItems(base, next llm.Item) llm.Item {
	if next.ID != "" {
		base.ID = next.ID
	}
	if len(next.Summary) > 0 {
		base.Summary = append([]string(nil), next.Summary...)
	}
	if strings.TrimSpace(next.EncryptedContent) != "" {
		base.EncryptedContent = strings.TrimSpace(next.EncryptedContent)
	}
	return base
}

func ensureStreamedReasoningState(states map[string]*streamedReasoningState, itemID string) *streamedReasoningState {
	if states == nil {
		return nil
	}
	key := strings.TrimSpace(itemID)
	if key == "" {
		key = "__anon__"
	}
	if state, ok := states[key]; ok {
		return state
	}
	state := &streamedReasoningState{}
	states[key] = state
	return state
}

func ensureReasoningSummaryIndex(state *streamedReasoningState, idx int) {
	if state == nil || idx < 0 {
		return
	}
	for len(state.Summary) <= idx {
		state.Summary = append(state.Summary, "")
	}
	for len(state.Done) <= idx {
		state.Done = append(state.Done, false)
	}
}

func appendReasoningSummaryDelta(states map[string]*streamedReasoningState, itemID string, idx int, delta string) {
	state := ensureStreamedReasoningState(states, itemID)
	ensureReasoningSummaryIndex(state, idx)
	if state == nil || idx < 0 {
		return
	}
	state.Summary[idx] += delta
}

func mergeReasoningSummaryState(states map[string]*streamedReasoningState, item llm.Item) {
	if len(item.Summary) == 0 {
		return
	}
	state := ensureStreamedReasoningState(states, item.ID)
	if state == nil {
		return
	}
	if len(state.Summary) == 0 {
		state.Summary = append([]string(nil), item.Summary...)
	}
}

func mergeStreamedReasoningItems(items []llm.Item, states map[string]*streamedReasoningState) []llm.Item {
	if len(states) == 0 {
		return items
	}
	for id, state := range states {
		if state == nil || len(state.Summary) == 0 {
			continue
		}
		if id == "__anon__" {
			items = appendOrReplaceReasoningItem(items, llm.Item{Summary: append([]string(nil), state.Summary...)})
			continue
		}
		found := false
		for i := range items {
			if items[i].ID != id {
				continue
			}
			found = true
			if len(items[i].Summary) == 0 {
				items[i].Summary = append([]string(nil), state.Summary...)
			}
			break
		}
		if !found {
			items = append(items, llm.Item{
				ID:      id,
				Summary: append([]string(nil), state.Summary...),
			})
		}
	}
	return items
}
