// transport.go is the disguise itself: it stands outermost in the Chat
// Completions client's HTTP chain and rewrites every request into the
// RemoteChatAsk the official CLI sends (Encode=1 body, COSY bearer, Cosy-*
// headers, X-Model-Key), and unwraps the SSE envelopes the gateway answers
// with back into the plain OpenAI chunks the client above it parses.
package qodercn

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

// inferPath is the algo endpoint inference streams through; signPath is what
// the COSY signature covers (the query string is not part of the preimage).
const (
	inferPath     = "/api/v2/service/pro/sse/agent_chat_generation?FetchKeys=llm_model_result&AgentId=agent_common&Encode=1"
	inferSignPath = "/api/v2/service/pro/sse/agent_chat_generation"
)

// Transport rewrites OpenAI Chat Completions traffic into Qoder CN's
// RemoteChatAsk protocol. Home and DB say where the credential lives; a nil DB
// falls back to the credential file under Home.
type Transport struct {
	Home string
	DB   *sql.DB

	base http.RoundTripper
}

// SetBase receives the client's own transport chain (reasoning normalization,
// usage accounting, …); the plugin sits outside all of it.
func (t *Transport) SetBase(base http.RoundTripper) { t.base = base }

// OwnsUserAgent says the transport installs the client's User-Agent itself.
func (t *Transport) OwnsUserAgent() bool { return true }

func (t *Transport) roundTripper() http.RoundTripper {
	if t.base != nil {
		return t.base
	}
	return http.DefaultTransport
}

// RoundTrip reads the Chat Completions request the client built, dresses it as
// the official CLI would, sends it, and — for the SSE answer — unwraps the
// gateway's envelopes in flight.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(io.LimitReader(req.Body, 32<<20))
	if req.Body != nil {
		_ = req.Body.Close()
	}
	if err != nil {
		return nil, err
	}
	var chat openAIChatRequest
	if err := json.Unmarshal(body, &chat); err != nil {
		return nil, fmt.Errorf("qodercn: read the chat request: %w", err)
	}
	model := strings.TrimSpace(chat.Model)
	if model == "" {
		model = "auto"
	}

	resp, err := t.send(req.Context(), &chat, model)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		// The token went stale between the freshness check and the call:
		// force one refresh, re-sign, and send again.
		resp.Body.Close()
		if _, refreshErr := ForceRefreshCredentials(req.Context(), t.DB, t.Home); refreshErr == nil {
			resp, err = t.send(req.Context(), &chat, model)
			if err != nil {
				return nil, err
			}
		}
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		// A non-stream answer (usually an error): let the client read the
		// status and body as-is.
		return resp, nil
	}
	if !chat.Stream {
		// The gateway answers in SSE whatever the request asked for, so the
		// one JSON document a non-streaming client wants is the stream
		// folded into one.
		return assembleQoderCompletion(resp)
	}
	resp.Body = unwrapQoderSSEStream(resp.Body)
	return resp, nil
}

// send signs and performs one inference request.
func (t *Transport) send(ctx context.Context, chat *openAIChatRequest, model string) (*http.Response, error) {
	creds, err := EnsureFreshCredentials(ctx, t.DB, t.Home)
	if err != nil {
		return nil, err
	}
	ask, err := buildRemoteChatAsk(ctx, chat, model, creds)
	if err != nil {
		return nil, err
	}
	encoded := EncodePayload(ask)
	url, headers, body, err := PrepareRequestHeaders(creds, LoadOrCreateMachineIdentity(t.Home), PrepareSpec{
		Path:        inferPath,
		EncodedBody: encoded,
		SignPath:    inferSignPath,
		ModelKey:    model,
		ModelSource: "system",
	})
	if err != nil {
		return nil, err
	}
	outReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	outReq.Header = headers
	return t.roundTripper().RoundTrip(outReq)
}

// openAIChatRequest is the subset of the Chat Completions request the rewrite
// reads; messages and tools pass through verbatim.
type openAIChatRequest struct {
	Model               string          `json:"model"`
	Messages            json.RawMessage `json:"messages"`
	Tools               json.RawMessage `json:"tools"`
	ToolChoice          json.RawMessage `json:"tool_choice"`
	MaxTokens           int             `json:"max_tokens"`
	MaxCompletionTokens int             `json:"max_completion_tokens"`
	Temperature         *float64        `json:"temperature"`
	TopP                *float64        `json:"top_p"`
	Stop                []string        `json:"stop"`
	ReasoningEffort     string          `json:"reasoning_effort"`
	EnableThinking      *bool           `json:"enable_thinking"`
	Stream              bool            `json:"stream"`
}

// wireMessage is one entry of the messages array, in the shapes go-openai
// writes (string content, or an array of typed parts).
type wireMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// textOf reads a message's content as text when it is text (a string, or an
// array of {type:"text", text} parts); image parts answer "".
func textOf(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var out strings.Builder
	for _, part := range parts {
		if part.Type == "text" {
			if out.Len() > 0 {
				out.WriteString("\n")
			}
			out.WriteString(part.Text)
		}
	}
	return out.String()
}

// remoteModelConfig is the model_config the official client sends.
type remoteModelConfig struct {
	Key            string `json:"key"`
	DisplayName    string `json:"display_name"`
	Model          string `json:"model"`
	IsVL           bool   `json:"is_vl"`
	IsReasoning    bool   `json:"is_reasoning"`
	MaxInputTokens int64  `json:"max_input_tokens"`
	Source         string `json:"source"`
	Format         string `json:"format"`
	APIKey         string `json:"api_key"`
	URL            string `json:"url"`
}

// remoteChatContext is the chat_context the official client sends: the last
// user turn, repeated under the model it runs on.
type remoteChatContext struct {
	Text       string                 `json:"text"`
	Features   []string               `json:"features"`
	Extra      remoteChatContextExtra `json:"extra"`
	ChatPrompt string                 `json:"chatPrompt"`
	ImageUrls  any                    `json:"imageUrls"`
}

type remoteChatContextExtra struct {
	Context     []string `json:"context"`
	ModelConfig struct {
		Key         string `json:"key"`
		IsReasoning bool   `json:"is_reasoning"`
	} `json:"modelConfig"`
	OriginalContent string `json:"originalContent"`
}

// remoteParameters is the parameters block: the sampling and thinking knobs,
// with the fields the client omits when unset.
type remoteParameters struct {
	Temperature           *float64        `json:"temperature,omitempty"`
	MaxTokens             int             `json:"max_tokens"`
	Stop                  []string        `json:"stop,omitempty"`
	TopP                  *float64        `json:"top_p,omitempty"`
	ReasoningEffort       string          `json:"reasoning_effort,omitempty"`
	EnableThinking        *bool           `json:"enable_thinking,omitempty"`
	ReasoningBudgetTokens int             `json:"reasoning_budget_tokens,omitempty"`
	ContextLength         int64           `json:"context_length,omitempty"`
	ToolChoice            json.RawMessage `json:"tool_choice,omitempty"`
}

// remoteChatAsk is the wire request, in the field order the official client
// serializes.
type remoteChatAsk struct {
	RequestID      string            `json:"request_id"`
	ChatRecordID   string            `json:"chat_record_id"`
	RequestSetID   string            `json:"request_set_id"`
	SessionID      string            `json:"session_id"`
	Stream         bool              `json:"stream"`
	ChatTask       string            `json:"chat_task"`
	ChatContext    remoteChatContext `json:"chat_context"`
	IsReply        bool              `json:"is_reply"`
	IsRetry        bool              `json:"is_retry"`
	Source         int               `json:"source"`
	Version        string            `json:"version"`
	AgentID        string            `json:"agent_id"`
	TaskID         string            `json:"task_id"`
	SessionType    string            `json:"session_type"`
	AliyunUserType string            `json:"aliyun_user_type"`
	ModelConfig    remoteModelConfig `json:"model_config"`
	System         string            `json:"system,omitempty"`
	Messages       json.RawMessage   `json:"messages"`
	Tools          json.RawMessage   `json:"tools,omitempty"`
	Parameters     remoteParameters  `json:"parameters"`
}

// sessionType is the CN CLI's session_type token (the runtime's own constant).
const sessionType = "qoderclicn"

// buildRemoteChatAsk folds one Chat Completions request into the RemoteChatAsk
// the official client sends: system messages are lifted into the top-level
// system field (kept in messages too), the last user turn is repeated as the
// chat_context, and the capability data rides in model_config.
func buildRemoteChatAsk(ctx context.Context, chat *openAIChatRequest, model string, creds Credentials) ([]byte, error) {
	var messages []wireMessage
	if len(chat.Messages) > 0 {
		if err := json.Unmarshal(chat.Messages, &messages); err != nil {
			return nil, fmt.Errorf("qodercn: read the messages: %w", err)
		}
	}
	var system strings.Builder
	lastUser := ""
	for _, message := range messages {
		text := textOf(message.Content)
		if message.Role == "system" {
			if system.Len() > 0 {
				system.WriteString("\n")
			}
			system.WriteString(text)
		}
		if message.Role == "user" && text != "" {
			lastUser = text
		}
	}

	requestID, err := newUUIDv4()
	if err != nil {
		return nil, err
	}
	sessionID := strings.TrimSpace(llm.AgentSessionIDFromContext(ctx))
	if sessionID == "" {
		if sessionID, err = newUUIDv4(); err != nil {
			return nil, err
		}
	}

	// The two numbers come from two places on purpose: max_input_tokens is the
	// input budget the listing declared, and context_length is the window the
	// model serves. The host's ContextWindow carries the second (models.go).
	config := remoteModelConfig{
		Key: model, DisplayName: model, Model: "",
		Source: "system", Format: "openai", APIKey: "", URL: "",
		MaxInputTokens: inputLimitOf(model),
	}
	hit, known := llm.Lookup("qodercn", model)
	if known {
		config.DisplayName = modelDisplayName(model, hit)
		config.IsVL = hit.SupportsAttachments
		config.IsReasoning = hit.CanReason
	}

	parameters := remoteParameters{
		Temperature:     chat.Temperature,
		MaxTokens:       chat.MaxTokens,
		Stop:            chat.Stop,
		TopP:            chat.TopP,
		ReasoningEffort: strings.TrimSpace(chat.ReasoningEffort),
		EnableThinking:  chat.EnableThinking,
		ToolChoice:      chat.ToolChoice,
		ContextLength:   hit.ContextWindow,
	}
	if parameters.MaxTokens == 0 {
		parameters.MaxTokens = chat.MaxCompletionTokens
	}
	if parameters.MaxTokens == 0 {
		parameters.MaxTokens = 32000
	}
	// Thinking is one switch and one effort, sent together or not at all: an
	// effort implies enable_thinking, and turning thinking off takes it out of
	// model_config too. A caller that states the switch itself decides it.
	if effort := parameters.ReasoningEffort; effort != "" {
		on := !strings.EqualFold(effort, "none")
		if chat.EnableThinking != nil {
			on = *chat.EnableThinking
		}
		parameters.EnableThinking = &on
		if !on {
			config.IsReasoning = false
		}
	}

	contextModel := config
	ask := remoteChatAsk{
		RequestID: requestID, ChatRecordID: requestID, RequestSetID: requestID,
		SessionID: sessionID, Stream: true,
		ChatTask: "FREE_INPUT",
		ChatContext: remoteChatContext{
			Text: lastUser, Features: []string{},
			Extra: remoteChatContextExtra{
				Context: []string{},
				ModelConfig: struct {
					Key         string `json:"key"`
					IsReasoning bool   `json:"is_reasoning"`
				}{Key: model, IsReasoning: config.IsReasoning},
				OriginalContent: lastUser,
			},
			ChatPrompt: "",
			ImageUrls:  nil,
		},
		IsReply: true, IsRetry: false, Source: 1, Version: "3",
		AgentID: "agent_common", TaskID: "common",
		SessionType: sessionType, AliyunUserType: "",
		ModelConfig: config,
		System:      system.String(),
		Messages:    ensureToolCallContents(chat.Messages),
		Tools:       chat.Tools,
		Parameters:  parameters,
	}
	_ = contextModel
	return json.Marshal(ask)
}

// ensureToolCallContents gives a tool-only assistant turn the content string
// the upstream insists on. A turn that carries tool_calls and no content field
// at all (or a null one) is not read as the owner of the calls by the model
// behind the gateway, so the tool message that answers it reads as an orphan
// and the whole round is rejected with
//
//	Messages with role 'tool' must be a response to a preceding message with 'tool_calls'
//
// The host's own wire form is the one that gets refused: go-openai's content
// is omitempty and a turn that only called tools has no text to carry. An
// empty string is accepted upstream, null is not.
func ensureToolCallContents(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var messages []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &messages); err != nil || len(messages) == 0 {
		return raw
	}
	changed := false
	for _, message := range messages {
		var calls []json.RawMessage
		if json.Unmarshal(message["tool_calls"], &calls) != nil || len(calls) == 0 {
			continue
		}
		content, carried := message["content"]
		trimmed := strings.TrimSpace(string(content))
		if carried && trimmed != "" && trimmed != "null" {
			continue
		}
		message["content"] = json.RawMessage(`""`)
		changed = true
	}
	if !changed {
		// Nothing to correct: the request keeps the exact bytes the client
		// wrote (the map round trip above would reorder every message's keys).
		return raw
	}
	fixed, err := json.Marshal(messages)
	if err != nil {
		return raw
	}
	return fixed
}

// modelDisplayName keeps the display name the listing carried, when the
// capability table still knows it.
func modelDisplayName(model string, hit llm.Hit) string {
	records := staticQoderModels()
	for _, record := range records {
		if record.Key == model && record.DisplayName != "" {
			return record.DisplayName
		}
	}
	return model
}

// sseEnvelope is the frame the gateway wraps every chunk in.
type sseEnvelope struct {
	StatusCode      string          `json:"statusCode"`
	StatusCodeValue int             `json:"statusCodeValue"`
	Body            json.RawMessage `json:"body"`
}

// scanQoderFrames reads the gateway's SSE answer and hands every chunk to
// onChunk: envelopes unwrapped, heartbeats dropped, [DONE] taken as the
// terminator. It reports whether that terminator arrived; a provider failure
// inside a frame comes back as the *llm.APIError qoderEnvelopeFailure builds.
// Both consumers — the streaming unwrapper and the non-streaming assembler —
// read frames through here, so the two cannot drift apart.
func scanQoderFrames(upstream io.Reader, onChunk func(chunk string) error) (sawDone bool, err error) {
	scanner := bufio.NewScanner(upstream)
	scanner.Buffer(make([]byte, 0, 64*1024), 16<<20)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		// The markers the gateway keeps the connection warm with, and the
		// stream's own terminator, are not chunks.
		if payload == "[DONE]" {
			return true, nil
		}
		if payload == "[NOT_EXCEED_QUOTA]" || strings.HasPrefix(payload, "[EXCEED_QUOTA]") ||
			strings.HasPrefix(payload, "[NOTIFICATIONS]") {
			continue
		}
		if failure := qoderEnvelopeFailure(payload); failure != nil {
			return false, failure
		}
		chunk := unwrapSSEPayload(payload)
		if chunk == "" {
			continue
		}
		if err := onChunk(chunk); err != nil {
			return false, err
		}
	}
	if err := scanner.Err(); err != nil {
		return false, fmt.Errorf("qodercn: the answer stream ended early: %w", err)
	}
	return false, nil
}

// unwrapQoderSSEStream reads the gateway's SSE stream and writes the plain
// OpenAI stream the client above expects: envelope bodies instead of
// envelopes, heartbeats dropped. The reader runs until the upstream body ends
// or the consumer stops reading; either way the upstream body is closed and
// the goroutine exits.
func unwrapQoderSSEStream(upstream io.ReadCloser) io.ReadCloser {
	pr, pw := io.Pipe()
	go func() {
		defer upstream.Close()
		sawDone, err := scanQoderFrames(upstream, func(chunk string) error {
			_, writeErr := pw.Write([]byte("data: " + chunk + "\n"))
			return writeErr
		})
		if err == nil && !sawDone {
			// The gateway ended the body without its terminator: end the
			// stream the way it would have.
			_, err = pw.Write([]byte("data: [DONE]\n"))
		}
		if err != nil {
			// A provider failure closes the stream with the reason; a
			// consumer that stopped reading gets the closed pipe.
			pw.CloseWithError(err)
			return
		}
		pw.Close()
	}()
	return pr
}

// streamedChunk is the part of one streamed chunk the folded answer keeps.
type streamedChunk struct {
	ID      string `json:"id"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Delta struct {
			Role             string `json:"role"`
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage json.RawMessage `json:"usage"`
}

// assembledToolCall is one call of the folded answer: fragments arrive in
// pieces that share an index, and the call is what they add up to.
type assembledToolCall struct {
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// assembledCompletion is the non-streaming answer the stream folds into: the
// document the OpenAI Chat Completions endpoint would have returned.
type assembledCompletion struct {
	ID      string            `json:"id"`
	Object  string            `json:"object"`
	Created int64             `json:"created"`
	Model   string            `json:"model"`
	Choices []assembledChoice `json:"choices"`
	Usage   json.RawMessage   `json:"usage,omitempty"`
}

type assembledChoice struct {
	Index        int              `json:"index"`
	Message      assembledMessage `json:"message"`
	FinishReason string           `json:"finish_reason"`
}

type assembledMessage struct {
	Role             string              `json:"role"`
	Content          string              `json:"content"`
	ReasoningContent string              `json:"reasoning_content,omitempty"`
	ToolCalls        []assembledToolCall `json:"tool_calls,omitempty"`
}

// assembleQoderCompletion folds the gateway's SSE answer into the one
// chat.completion a non-streaming client asked for. The gateway streams
// whatever the request said (measured 2026-11-13: stream:false still answers
// text/event-stream), so without this the client above unmarshals "data: …" as
// one JSON document and reports "invalid character 'd' looking for beginning
// of value" instead of reading the answer.
func assembleQoderCompletion(resp *http.Response) (*http.Response, error) {
	upstream := resp.Body
	defer upstream.Close()
	var (
		id, model, role, finish string
		created                 int64
		content, reasoning      strings.Builder
		usage                   json.RawMessage
		chunks                  int
	)
	calls := make(map[int]*assembledToolCall)
	var indexes []int
	_, err := scanQoderFrames(upstream, func(chunk string) error {
		var streamed streamedChunk
		if err := json.Unmarshal([]byte(chunk), &streamed); err != nil {
			return nil
		}
		chunks++
		if streamed.ID != "" {
			id = streamed.ID
		}
		if streamed.Model != "" {
			model = streamed.Model
		}
		if streamed.Created > 0 {
			created = streamed.Created
		}
		if held := strings.TrimSpace(string(streamed.Usage)); held != "" && held != "null" {
			// Usage is the request's running total, so the last chunk that
			// carries one is the whole measurement. It rides through as the
			// gateway sent it: the standard fields are what the client reads,
			// and its own extras are none of this layer's business.
			usage = streamed.Usage
		}
		for _, choice := range streamed.Choices {
			if name := strings.TrimSpace(choice.Delta.Role); name != "" {
				role = name
			}
			content.WriteString(choice.Delta.Content)
			reasoning.WriteString(choice.Delta.ReasoningContent)
			if reason := choice.FinishReason; reason != "" && reason != "null" {
				finish = reason
			}
			for _, fragment := range choice.Delta.ToolCalls {
				call, started := calls[fragment.Index]
				if !started {
					call = &assembledToolCall{}
					calls[fragment.Index] = call
					indexes = append(indexes, fragment.Index)
				}
				// The first fragment that names the call, its type or its
				// function wins; the arguments are what the rest add up to.
				if call.ID == "" {
					call.ID = fragment.ID
				}
				if call.Type == "" {
					call.Type = fragment.Type
				}
				if call.Function.Name == "" {
					call.Function.Name = fragment.Function.Name
				}
				call.Function.Arguments += fragment.Function.Arguments
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if chunks == 0 {
		return nil, fmt.Errorf("qodercn: the answer stream carried no chunk")
	}
	if role == "" {
		role = "assistant"
	}
	if created == 0 {
		created = time.Now().Unix()
	}
	sort.Ints(indexes)
	ordered := make([]assembledToolCall, 0, len(indexes))
	for _, index := range indexes {
		ordered = append(ordered, *calls[index])
	}
	assembled, err := json.Marshal(assembledCompletion{
		ID: id, Object: "chat.completion", Created: created, Model: model,
		Choices: []assembledChoice{{
			Index: 0,
			Message: assembledMessage{
				Role: role, Content: content.String(),
				ReasoningContent: reasoning.String(), ToolCalls: ordered,
			},
			FinishReason: finish,
		}},
		Usage: usage,
	})
	if err != nil {
		return nil, fmt.Errorf("qodercn: fold the answer stream: %w", err)
	}
	resp.Body = io.NopCloser(bytes.NewReader(assembled))
	resp.ContentLength = int64(len(assembled))
	resp.Header.Set("Content-Type", "application/json")
	resp.Header.Set("Content-Length", strconv.Itoa(len(assembled)))
	return resp, nil
}

// unwrapSSEPayload extracts the chunk JSON one frame carries: an envelope's
// body (a string holding the chunk, or the chunk itself), or a bare chunk.
func unwrapSSEPayload(payload string) string {
	var envelope sseEnvelope
	if err := json.Unmarshal([]byte(payload), &envelope); err == nil && envelope.StatusCodeValue != 0 && len(envelope.Body) > 0 {
		var inner string
		if json.Unmarshal(envelope.Body, &inner) == nil {
			return inner
		}
		return string(envelope.Body)
	}
	// Not an envelope: a plain chunk (or an unencoded error object) passes
	// through.
	trimmed := strings.TrimSpace(payload)
	if !bytes.HasPrefix([]byte(trimmed), []byte("{")) && !bytes.HasPrefix([]byte(trimmed), []byte("[")) {
		return ""
	}
	if !json.Valid([]byte(trimmed)) {
		return ""
	}
	return trimmed
}

// qoderFailure is the failure object the gateway puts in an envelope's body:
// its own classifier, plus the upstream's own error under details.
type qoderFailure struct {
	Code    string          `json:"code"`
	Type    string          `json:"type"`
	Message string          `json:"message"`
	Details json.RawMessage `json:"details"`
}

// qoderFailureDetails is what details holds: the upstream's error, wrapped in
// an error object (and, like the body, as a string holding JSON).
type qoderFailureDetails struct {
	Error struct {
		Code    string `json:"code"`
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// qoderEnvelopeFailure reports the failure an envelope carries, nil when the
// frame is a chunk. The gateway answers a rejected request with HTTP 200 and a
// text/event-stream whose frame holds the real status (400) and the provider's
// error object. That object is valid JSON, so unwrapping it as a chunk hands
// the consumer a frame with no choices: the stream then ends before any answer
// started, and the fallback that follows reports a JSON syntax error instead
// of what the provider said.
func qoderEnvelopeFailure(payload string) error {
	var envelope sseEnvelope
	if err := json.Unmarshal([]byte(payload), &envelope); err != nil {
		return nil
	}
	// A chunk never carries a failure status, so this is the whitelist: no
	// prose is read to tell the two apart.
	if envelope.StatusCodeValue < 400 {
		return nil
	}
	body := unwindJSONString(envelope.Body)
	var failure qoderFailure
	if err := json.Unmarshal(body, &failure); err != nil {
		failure = qoderFailure{Message: strings.TrimSpace(string(body))}
	}
	message := strings.TrimSpace(failure.Message)
	code, kind := strings.TrimSpace(failure.Code), strings.TrimSpace(failure.Type)
	if len(failure.Details) > 0 {
		var details qoderFailureDetails
		if json.Unmarshal(unwindJSONString(failure.Details), &details) == nil {
			if upstream := strings.TrimSpace(details.Error.Message); upstream != "" {
				message = upstream
			}
			if code == "" {
				code = strings.TrimSpace(details.Error.Code)
			}
			if kind == "" {
				kind = strings.TrimSpace(details.Error.Type)
			}
		}
	}
	if message == "" {
		message = strings.TrimSpace(payload)
	}
	return fmt.Errorf("qodercn: %w", &llm.APIError{
		StatusCode: envelope.StatusCodeValue,
		Code:       code,
		Type:       kind,
		Message:    message,
	})
}

// unwindJSONString returns the JSON document raw holds: the gateway wraps an
// envelope's body and the upstream's details in a string, while other frames
// carry the object itself.
func unwindJSONString(raw json.RawMessage) json.RawMessage {
	var text string
	if json.Unmarshal(raw, &text) == nil && strings.HasPrefix(strings.TrimSpace(text), "{") {
		return json.RawMessage(text)
	}
	return raw
}
