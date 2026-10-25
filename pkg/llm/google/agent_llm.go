package google

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

const (
	// APIBase is Google's own generative-language endpoint. An OpenCode
	// deployment passes the gateway's /google base instead; the client appends
	// /v1beta/models/{model}:generateContent either way.
	APIBase = "https://generativelanguage.googleapis.com"

	// maxResponseBytes bounds a response body. A thinking model's answer with
	// full reasoning is large but nowhere near this.
	maxResponseBytes = 16 << 20

	// roleUser and roleModel are generateContent's only two roles. Everything
	// the model is told and everything it produced arrive as one of them;
	// instructions live in systemInstruction instead.
	roleUser  = "user"
	roleModel = "model"
)

// agentLLM is one configured Gemini client.
type agentLLM struct {
	apiKey    string
	baseURL   string
	model     string
	maxTokens int64
	client    *http.Client
}

// NewAgentLLM builds the Gemini client used by the agent runtime.
//
// apiKey is only a fallback credential for Google's own API; the OpenCode
// gateway authenticates through transport, which may be nil to use this
// package's normal HTTP client.
func NewAgentLLM(apiKey, baseURL, model string, maxTokens int64, transport http.RoundTripper) llm.LLM {
	c := &agentLLM{
		apiKey:    strings.TrimSpace(apiKey),
		baseURL:   strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		model:     strings.TrimSpace(model),
		maxTokens: maxTokens,
	}
	if c.baseURL == "" {
		c.baseURL = APIBase
	}
	if transport == nil {
		c.client = llm.NewHTTPClient()
	} else {
		c.client = &http.Client{Transport: transport}
	}
	return c
}

// Execute runs one streamGenerateContent call, streaming the answer to the
// stream sink in ctx as it arrives.
func (c *agentLLM) Execute(ctx context.Context, messages []llm.Message, tools []*llm.Tool) (*llm.Result, error) {
	if c.model == "" {
		return nil, errors.New("google: no model configured")
	}
	body, err := buildRequest(messages, tools, c.maxTokens)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("google: encode request: %w", err)
	}
	return c.executeStreaming(ctx, raw)
}

// executeUnary runs the same request in its non-streaming form: one
// generateContent call that returns the assembled response. It is kept for the
// non-streaming resend path (plans/126) and has no streaming caller.
func (c *agentLLM) executeUnary(ctx context.Context, raw []byte) (*llm.Result, error) {
	endpoint := c.baseURL + "/v1beta/models/" + url.PathEscape(c.model) + ":generateContent"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("google: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("google: send request: %w", err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("google: read response: %w", err)
	}
	if len(payload) > maxResponseBytes {
		return nil, fmt.Errorf("google: response exceeds %d bytes", maxResponseBytes)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, newAPIError(resp.StatusCode, payload, resp.Header.Get("Retry-After"))
	}
	return parseResponse(resp.StatusCode, payload, resp.Header.Get("Retry-After"))
}

// generateContentRequest is the request body. Only the fields this client
// populates are modelled; generateContent ignores the rest.
type generateContentRequest struct {
	SystemInstruction *content          `json:"systemInstruction,omitempty"`
	Contents          []content         `json:"contents"`
	Tools             []toolGroup       `json:"tools,omitempty"`
	GenerationConfig  *generationConfig `json:"generationConfig,omitempty"`
}

// content is one turn: a role and its parts.
type content struct {
	Role  string `json:"role,omitempty"`
	Parts []part `json:"parts"`
}

// part is one piece of a turn: text, an image, a call, or a call's result.
type part struct {
	Text             string            `json:"text,omitempty"`
	InlineData       *inlineData       `json:"inlineData,omitempty"`
	FunctionCall     *functionCall     `json:"functionCall,omitempty"`
	FunctionResponse *functionResponse `json:"functionResponse,omitempty"`
}

type inlineData struct {
	MIMEType string `json:"mimeType"`
	Data     string `json:"data"`
}

type functionCall struct {
	Name string `json:"name"`
	Args any    `json:"args,omitempty"`
}

type functionResponse struct {
	Name     string `json:"name"`
	Response any    `json:"response"`
}

type toolGroup struct {
	FunctionDeclarations []functionDeclaration `json:"functionDeclarations"`
}

type functionDeclaration struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type generationConfig struct {
	MaxOutputTokens int64 `json:"maxOutputTokens,omitempty"`
}

// buildRequest projects llm messages and tools onto the generateContent shape.
func buildRequest(messages []llm.Message, tools []*llm.Tool, maxTokens int64) (*generateContentRequest, error) {
	req := &generateContentRequest{}
	// toolNames maps a call id back to its function name. Gemini pairs a
	// functionResponse with its functionCall by name, while an llm tool-result
	// message carries only the id.
	toolNames := map[string]string{}
	var systemParts []string
	for _, m := range messages {
		switch m.Role {
		case llm.RoleSystem, llm.RoleDeveloper:
			if text := m.TextContent(); strings.TrimSpace(text) != "" {
				systemParts = append(systemParts, text)
			}
		case llm.RoleUser:
			parts, err := userParts(m.Parts)
			if err != nil {
				return nil, err
			}
			if len(parts) > 0 {
				req.Contents = append(req.Contents, content{Role: roleUser, Parts: parts})
			}
		case llm.RoleAssistant:
			parts := assistantParts(m, toolNames)
			if len(parts) > 0 {
				req.Contents = append(req.Contents, content{Role: roleModel, Parts: parts})
			}
		case llm.RoleTool:
			resultPart, err := toolResultPart(m, toolNames)
			if err != nil {
				return nil, err
			}
			// A tool result is a user turn in generateContent's model.
			req.Contents = append(req.Contents, content{Role: roleUser, Parts: []part{resultPart}})
		default:
			return nil, fmt.Errorf("google: unsupported role %q", m.Role)
		}
	}
	// generateContent takes one instruction block, so several system messages
	// become one text, the same way the Anthropic projection joins them.
	if joined := strings.TrimSpace(strings.Join(systemParts, "\n\n")); joined != "" {
		req.SystemInstruction = &content{Parts: []part{{Text: joined}}}
	}
	if maxTokens > 0 {
		req.GenerationConfig = &generationConfig{MaxOutputTokens: maxTokens}
	}
	if decls := functionDeclarations(tools); len(decls) > 0 {
		req.Tools = []toolGroup{{FunctionDeclarations: decls}}
	}
	return req, nil
}

// userParts projects user content. Image bytes become inlineData; an image URL
// has no generateContent equivalent and is refused rather than dropped.
func userParts(parts []llm.ContentPart) ([]part, error) {
	out := make([]part, 0, len(parts))
	for _, p := range parts {
		switch p.Type {
		case llm.ContentTypeText:
			if strings.TrimSpace(p.Text) != "" {
				out = append(out, part{Text: p.Text})
			}
		case llm.ContentTypeImageBase64:
			out = append(out, part{InlineData: &inlineData{MIMEType: p.MIMEType, Data: p.ImageBase64}})
		case llm.ContentTypeImageURL:
			return nil, errors.New("google: image URLs are not supported; attach the image bytes instead")
		}
	}
	return out, nil
}

// assistantParts projects an assistant turn: its text, then its tool calls as
// functionCall parts.
func assistantParts(m llm.Message, toolNames map[string]string) []part {
	out := make([]part, 0, len(m.Parts)+len(m.ToolCalls))
	for _, p := range m.Parts {
		if p.Type == llm.ContentTypeText && strings.TrimSpace(p.Text) != "" {
			out = append(out, part{Text: p.Text})
		}
	}
	for _, tc := range m.ToolCalls {
		name := strings.TrimSpace(tc.Function.Name)
		if name == "" {
			// A call with no name cannot be replayed: Gemini would reject it and
			// there is nothing to pair its result with.
			continue
		}
		if id := strings.TrimSpace(tc.ID); id != "" {
			toolNames[id] = name
		}
		out = append(out, part{FunctionCall: &functionCall{Name: name, Args: argumentsValue(tc.Function.Arguments)}})
	}
	return out
}

// toolResultPart projects a tool-result message. Its name is recovered from the
// call the assistant made, because generateContent pairs by name; the id is the
// last resort, and is also what Gemini accepts for an unpaired result.
func toolResultPart(m llm.Message, toolNames map[string]string) (part, error) {
	id := strings.TrimSpace(m.ToolCallID)
	if id == "" {
		return part{}, errors.New("google: tool message missing tool_call_id")
	}
	name := toolNames[id]
	if name == "" {
		name = strings.TrimSpace(m.Name)
	}
	if name == "" {
		name = id
	}
	return part{FunctionResponse: &functionResponse{Name: name, Response: toolResponse(m.Parts)}}, nil
}

// toolResponse turns a tool result into the JSON object generateContent
// requires. A tool that answers with JSON is passed through as it wrote it; any
// other text is wrapped so the object shape holds.
func toolResponse(parts []llm.ContentPart) map[string]any {
	text := llm.TextContent(parts...)
	var obj map[string]any
	if strings.TrimSpace(text) != "" {
		if err := json.Unmarshal([]byte(text), &obj); err == nil && obj != nil {
			return obj
		}
	}
	return map[string]any{"result": text}
}

// argumentsValue decodes a call's arguments for the request body. Arguments that
// are empty or not valid JSON become an empty object: a model that emitted a
// truncated object must not abort the whole request, and the tool handler has
// already reported the parse failure for that call.
func argumentsValue(args string) any {
	args = strings.TrimSpace(args)
	if args == "" || !json.Valid([]byte(args)) {
		return map[string]any{}
	}
	var value any
	if err := json.Unmarshal([]byte(args), &value); err != nil || value == nil {
		return map[string]any{}
	}
	return value
}

// functionDeclarations projects the registered tools.
func functionDeclarations(tools []*llm.Tool) []functionDeclaration {
	out := make([]functionDeclaration, 0, len(tools))
	for _, tool := range tools {
		if tool == nil {
			continue
		}
		out = append(out, functionDeclaration{
			Name:        tool.Name(),
			Description: tool.Description(),
			Parameters:  tool.InputSchema(),
		})
	}
	return out
}

// generateContentResponse is the response body.
type generateContentResponse struct {
	Candidates []struct {
		Content      content `json:"content"`
		FinishReason string  `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
		ThoughtsTokenCount   int `json:"thoughtsTokenCount"`
	} `json:"usageMetadata"`
	// Error carries an error body returned with a 200 status, which the gateway
	// does when an upstream reports the failure in band.
	Error json.RawMessage `json:"error"`
}

// parseResponse turns a generateContent response into a result.
func parseResponse(status int, payload []byte, retryAfter string) (*llm.Result, error) {
	var parsed generateContentResponse
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return nil, fmt.Errorf("google: decode response: %w", err)
	}
	if len(parsed.Error) > 0 && string(parsed.Error) != "null" {
		return nil, newAPIError(status, payload, retryAfter)
	}
	if len(parsed.Candidates) == 0 {
		return nil, fmt.Errorf("google: response has no candidates: %s", truncateMessage(strings.TrimSpace(string(payload))))
	}
	candidate := parsed.Candidates[0]
	msg := &llm.Message{Role: llm.RoleAssistant}
	// Text and calls are collected in the order they arrived, so a response that
	// interleaves them keeps its sequence within each of the two shapes.
	for _, p := range candidate.Content.Parts {
		switch {
		case p.FunctionCall != nil:
			msg.ToolCalls = append(msg.ToolCalls, googleToolCallFromPart(p))
		case p.Text != "":
			msg.Parts = append(msg.Parts, llm.Text(p.Text))
		}
	}
	if len(msg.Parts) == 0 && len(msg.ToolCalls) == 0 {
		// An empty turn is a refusal to answer (safety, recitation, a filtered
		// prompt). Returning it as a silent empty message would look like a
		// successful turn that said nothing.
		return nil, fmt.Errorf("google: response was empty (finish reason %q)", candidate.FinishReason)
	}
	result := &llm.Result{Message: msg}
	usage := parsed.UsageMetadata
	if usage.PromptTokenCount > 0 || usage.CandidatesTokenCount > 0 || usage.ThoughtsTokenCount > 0 {
		result.Usage = &llm.Usage{
			InputTokens: usage.PromptTokenCount,
			// Thinking tokens are billed output, so they count towards the
			// output figure.
			OutputTokens: usage.CandidatesTokenCount + usage.ThoughtsTokenCount,
		}
	}
	return result, nil
}

// errorInfo is a provider error after extraction.
type errorInfo struct {
	Code    string
	Type    string
	Message string
}

// newAPIError normalizes a failure. Both error shapes seen on the wire are read:
// the gateway's own {"type":"error","error":{"type":…,"message":…}} and the
// upstream passthrough {"error":{"code":…,"status":…,"message":…}}.
func newAPIError(status int, payload []byte, retryAfter string) error {
	info := parseError(payload)
	message := info.Message
	if message == "" {
		message = truncateMessage(strings.TrimSpace(string(payload)))
	}
	if message == "" {
		message = http.StatusText(status)
	}
	receivedAt := time.Now()
	return &llm.APIError{
		StatusCode: status,
		Code:       info.Code,
		Type:       info.Type,
		Message:    message,
		RateLimit:  llm.ParseRateLimit(status, string(payload), retryAfter, receivedAt),
		RetryAfter: llm.ParseRetryAfter(retryAfter, receivedAt),
	}
}

func parseError(payload []byte) errorInfo {
	var envelope struct {
		Type  string          `json:"type"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil || len(envelope.Error) == 0 {
		return errorInfo{}
	}
	var detail struct {
		Type    string          `json:"type"`
		Status  string          `json:"status"`
		Message string          `json:"message"`
		Code    json.RawMessage `json:"code"`
	}
	if err := json.Unmarshal(envelope.Error, &detail); err != nil {
		var text string
		if json.Unmarshal(envelope.Error, &text) == nil {
			return errorInfo{Type: envelope.Type, Message: strings.TrimSpace(text)}
		}
		return errorInfo{Type: envelope.Type}
	}
	return errorInfo{
		// The gateway names the failure in "type", an upstream in "status"; the
		// envelope's own type is the last resort.
		Type:    firstNonEmpty(strings.TrimSpace(detail.Type), strings.TrimSpace(detail.Status), strings.TrimSpace(envelope.Type)),
		Code:    codeString(detail.Code),
		Message: strings.TrimSpace(detail.Message),
	}
}

// codeString reads a code that providers spell either as a number (401) or as a
// string.
func codeString(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return strings.TrimSpace(text)
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err == nil {
		return number.String()
	}
	return ""
}

func truncateMessage(s string) string {
	const limit = 300
	if len(s) > limit {
		return s[:limit] + "…"
	}
	return s
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
