package google

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
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
// stream sink in ctx as it arrives — unless ctx carries the non-streaming
// switch, in which case it runs one bounded generateContent call and replays
// the whole answer through the sink. A round cut by the output budget
// (MAX_TOKENS) is re-asked up to OutputLimitContinuations times, each round
// carrying the accumulated answer and a resume instruction.
func (c *agentLLM) Execute(ctx context.Context, messages []llm.Message, tools []*llm.Tool) (*llm.Result, error) {
	if c.model == "" {
		return nil, errors.New("google: no model configured")
	}
	// The sink's end fires once per Execute, not once per request: a
	// continuation round is the same answer still being produced, and ending
	// between rounds made the UI treat the fragment as a finished reply. The
	// resend a refused request gets is that same answer too.
	endSink := llm.StreamSinkFrom(ctx)
	if endSink != nil && endSink.OnEnd != nil {
		defer endSink.OnEnd()
	}
	res, replayed, err := c.execute(ctx, messages, tools)
	if err != nil && replayed && llm.ReasoningReplayRefused(err, []int{http.StatusBadRequest},
		"thought_signature", "thoughtsignature", "signature") {
		// The signatures of a reply belong to the model that wrote them and to
		// the account that stored them: once Google refuses them, every request
		// of this conversation carries the same ones and fails the same way.
		// The resend without them sends the reply as ordinary history, and the
		// current turn's first call gets the placeholder buildRequest gives a
		// call that lost its signature — so a refusal of the signatures is
		// answered, while anything else it then fails on is returned as it is.
		slog.Warn("google: provider refused the replayed thought signatures; resending without them", "model", c.model, "err", err)
		res, _, err = c.execute(ctx, llm.WithoutReasoningCarries(messages), tools)
	}
	return res, err
}

// execute runs the request loop for Execute, reporting whether the requests it
// sent replayed reasoning — a thought summary or a signature a reply carried —
// which is what makes a refusal worth a resend without it.
func (c *agentLLM) execute(ctx context.Context, messages []llm.Message, tools []*llm.Tool) (*llm.Result, bool, error) {
	nonStreaming := llm.NonStreamingRequestFrom(ctx)
	// conversation is what the next round sends: the caller's messages plus,
	// from the second round on, the recovery's resume pair. It is rebuilt
	// from the original slice each round — the accumulated answer replaces,
	// never stacks on, the previous round's fragment. buildRequest renders
	// the assistant fragment as role "model" and the prompt as role "user".
	conversation := messages
	var recovery llm.OutputLimitRecovery
	var rounds int
	var lastResult *llm.Result
	var answer strings.Builder
	var lastToolCalls []llm.ToolCall
	var stopReason string
	var usage *llm.Usage
	var billedIn, billedOut int
	var settledIn int
	// replayed reports that a request this loop sends includes reasoning a
	// reply carried: a thought summary, or a signature that came from the
	// model rather than from the placeholder. A signature-less projection
	// replays nothing, so a refusal of it is not this fallback's business.
	replayed := false
	for {
		body, err := buildRequest(conversation, tools, c.maxTokens, c.model)
		if err != nil {
			return nil, replayed, err
		}
		if requestReplaysGeminiReasoning(body) {
			replayed = true
		}
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, replayed, fmt.Errorf("google: encode request: %w", err)
		}
		var res *llm.Result
		if nonStreaming {
			res, err = c.executeUnaryRound(ctx, raw)
		} else {
			res, err = c.executeStreaming(ctx, raw)
		}
		if err != nil {
			return nil, replayed, err
		}
		rounds++
		lastResult = res
		// Occupancy is the settled round's prompt; the output is every
		// round's, because the model really did produce it and this Result's
		// answer carries it. The billing view sums both across rounds.
		if res.Usage != nil {
			billedIn += res.Usage.InputTokens
			billedOut += res.Usage.OutputTokens
			settledIn = res.Usage.InputTokens
			usage = &llm.Usage{InputTokens: settledIn, OutputTokens: billedOut}
		}
		if res.Message != nil {
			answer.WriteString(res.Message.TextContent())
			lastToolCalls = res.Message.ToolCalls
		}
		stopReason = res.StopReason
		if !recovery.ShouldContinue(stopReason, answer.Len(), len(lastToolCalls)) {
			break
		}
		conversation = append(append([]llm.Message(nil), messages...), recovery.Resume(answer.String())...)
	}
	if rounds == 1 {
		// No continuation ran: the settled round's own result is this call's.
		return lastResult, replayed, nil
	}
	// One assistant message carries the whole answer: each round was its own
	// request, but this call is one reply. A Gemini carry survives only in part —
	// the merged text is rounds stitched together and no signature was issued
	// for it, so it goes back unsigned, while the last round's calls keep their
	// own signatures because a current-turn call without one is refused. A last
	// round cut by the output limit carries nothing: that answer is unfinished.
	parts := []llm.ContentPart{}
	if answer.Len() > 0 {
		parts = append(parts, llm.Text(answer.String()))
	}
	merged := &llm.Message{Role: llm.RoleAssistant, Parts: parts, ToolCalls: lastToolCalls}
	if lastResult != nil && lastResult.Message != nil && !llm.OutputLimitReached(stopReason) {
		if layout, ok := llm.DecodeGeminiParts(lastResult.Message.Name); ok {
			mergedLayout := make([]llm.GeminiPart, 0, len(layout)+1)
			if answer.Len() > 0 {
				mergedLayout = append(mergedLayout, llm.GeminiPart{Kind: "text", Text: answer.String()})
			}
			for _, p := range layout {
				if p.Kind == "call" {
					mergedLayout = append(mergedLayout, p)
				}
			}
			if len(mergedLayout) > 0 {
				merged.Name = llm.EncodeGeminiParts(mergedLayout)
				merged.ReasoningModel = lastResult.Message.ReasoningModel
			}
		}
	}
	out := &llm.Result{
		Message:    merged,
		StopReason: stopReason,
		Usage:      usage,
	}
	if billedIn != settledIn {
		out.BilledUsage = &llm.Usage{InputTokens: billedIn, OutputTokens: billedOut}
	}
	return out, replayed, nil
}

// executeUnaryRound runs one non-streaming round under the non-streaming
// deadline, reports its usage to the sink and replays the whole answer in
// stream order, so every consumer sees it as it would a streamed one. The
// sink's OnEnd is Execute's to fire, once per call.
func (c *agentLLM) executeUnaryRound(ctx context.Context, raw []byte) (*llm.Result, error) {
	// The request context switched this call to one non-streaming attempt:
	// run it under the non-streaming deadline (scoped to this round, so a
	// later round gets its own) and replay the whole answer to the sink in
	// stream order.
	ctx, cancel := llm.WithNonStreamingDeadline(ctx)
	defer cancel()
	sink := llm.StreamSinkFrom(ctx)
	result, finishReason, err := c.executeUnary(ctx, raw)
	if err != nil {
		return nil, llm.NonStreamingError(ctx, err)
	}
	if sink != nil && result.Usage != nil &&
		(result.Usage.InputTokens > 0 || result.Usage.OutputTokens > 0) {
		if sink.OnUsage != nil {
			sink.OnUsage(result.Usage.InputTokens, result.Usage.OutputTokens)
		}
		if sink.OnUsageSnapshot != nil {
			sink.OnUsageSnapshot(result.Usage.InputTokens, result.Usage.OutputTokens)
		}
	}
	var texts []string
	for _, p := range result.Message.Parts {
		if p.Type == llm.ContentTypeText {
			texts = append(texts, p.Text)
		}
	}
	// The thought summaries the response carried are the thinking the person
	// sees; the layout is the one place they are kept, so the replay reads them
	// from there rather than from a second copy.
	reasoning := ""
	if layout, ok := llm.DecodeGeminiParts(result.Message.Name); ok {
		reasoning = geminiThoughtText(layout)
	}
	// The whole reply is finished here, so its carry is complete: hand it over
	// before the replay, whose tool-call callbacks are what can start a
	// provisional message for this reply.
	if sink != nil && sink.OnReasoningCarry != nil && result.Message.Name != "" {
		sink.OnReasoningCarry(result.Message.Name, result.Message.ReasoningModel)
	}
	llm.ReplayToSink(sink, llm.ReplayedResponse{
		Reasoning:  reasoning,
		Text:       texts,
		ToolCalls:  result.Message.ToolCalls,
		StopReason: finishReason,
	})
	return result, nil
}

// executeUnary runs the same request in its non-streaming form: one
// generateContent call that returns the assembled response, plus the
// candidate's finish reason for the sink replay. It is the non-streaming
// resend path (plans/126): Execute picks it when the request context carries
// the non-streaming switch.
func (c *agentLLM) executeUnary(ctx context.Context, raw []byte) (*llm.Result, string, error) {
	endpoint := c.baseURL + "/v1beta/models/" + url.PathEscape(c.model) + ":generateContent"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, "", fmt.Errorf("google: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("google: send request: %w", err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("google: read response: %w", err)
	}
	if len(payload) > maxResponseBytes {
		return nil, "", fmt.Errorf("google: response exceeds %d bytes", maxResponseBytes)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", newAPIError(resp.StatusCode, payload, resp.Header.Get("Retry-After"))
	}
	return parseResponse(resp.StatusCode, payload, resp.Header.Get("Retry-After"), c.model)
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
	// Thought marks a thought-summary part: the model's reasoning, never its answer.
	Thought bool `json:"thought,omitempty"`
	// ThoughtSignature is the encrypted reasoning state the model attached to
	// this part. It goes back in this same part, to the model that wrote it.
	ThoughtSignature string `json:"thoughtSignature,omitempty"`
}

// partJSON is part without its MarshalJSON method, so every part that is not
// signature-only serializes exactly as the struct did before the method
// existed.
type partJSON part

// MarshalJSON keeps a signature-only part legal. A part carrying nothing but a
// signature has to state the field it belongs to, and text is the field Gemini
// returns such a part under (streaming may deliver a signature in a part with
// empty text); "thoughtSignature" alone is not a part the API accepts. An empty
// thought summary — the model may return one with no text — needs the same,
// because a "thought" marker with no data field of its own is no more a part.
func (p part) MarshalJSON() ([]byte, error) {
	if p.Text != "" || (p.ThoughtSignature == "" && !p.Thought) ||
		p.InlineData != nil || p.FunctionCall != nil || p.FunctionResponse != nil {
		return json.Marshal(partJSON(p))
	}
	return json.Marshal(struct {
		Text             string `json:"text"`
		Thought          bool   `json:"thought,omitempty"`
		ThoughtSignature string `json:"thoughtSignature,omitempty"`
	}{Text: p.Text, Thought: p.Thought, ThoughtSignature: p.ThoughtSignature})
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
// model is the model the request is addressed to, which decides whether a
// reply that was only reasoning may be sent at all.
func buildRequest(messages []llm.Message, tools []*llm.Tool, maxTokens int64, model string) (*generateContentRequest, error) {
	req := &generateContentRequest{}
	// toolNames maps a call id back to its function name. Gemini pairs a
	// functionResponse with its functionCall by name, while an llm tool-result
	// message carries only the id.
	toolNames := map[string]string{}
	var systemParts []string
	// turnStart is where the current turn begins in req.Contents. Google
	// validates thought signatures for the current turn only, and a turn begins
	// with the most recent user message that is not a tool result.
	turnStart := 0
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
			turnStart = len(req.Contents)
		case llm.RoleAssistant:
			// A reply that was only reasoning goes back to the model that
			// produced it, and to no other: the fence would hand that model's
			// reasoning to another one as an answer this conversation gave.
			if llm.ThinkingFenceOnly(m) && !llm.ThinkingFenceGoesTo(m, model) {
				continue
			}
			if layout, ok := geminiReplayLayout(m, model); ok {
				// The model that wrote this reply gets its parts back as it
				// returned them: in their order, thought summaries included,
				// each signature in its own part. The signatures are its
				// reasoning; without them Gemini 3 and later refuse the
				// current turn's calls, and earlier models re-derive it.
				parts, ok := geminiPartsFromLayout(m, layout, toolNames)
				if !ok {
					// The layout names a call the message does not have, so it is
					// damaged: send the ordinary projection rather than a reply
					// with a hole in it.
					llm.LogDebug("google", "gemini carry for an assistant message names a missing tool call; projecting it without the carry")
					parts = assistantParts(m, toolNames)
				}
				if len(parts) > 0 {
					req.Contents = append(req.Contents, content{Role: roleModel, Parts: parts})
				}
				continue
			}
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
	// A current-turn call with no signature at all is a request Gemini 3 and
	// later refuse, and such a call reaches here whenever the reply lost its
	// signature (an older session's call, a call some other producer made). The
	// placeholder below is the last resort: a call carrying its own signature
	// keeps it, and so does every earlier turn.
	for i := turnStart; i < len(req.Contents); i++ {
		c := &req.Contents[i]
		if c.Role != roleModel {
			continue
		}
		for j := range c.Parts {
			if c.Parts[j].FunctionCall == nil {
				continue
			}
			if c.Parts[j].ThoughtSignature == "" {
				c.Parts[j].ThoughtSignature = geminiSkipSignatureValidator
			}
			break
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

// requestReplaysGeminiReasoning reports whether a request sends reasoning back
// to the model: a thought summary, or a thought signature that came from the
// model's own reply. The placeholder buildRequest puts on a current-turn call
// that lost its signature is not reasoning — no reply ever carried it — and a
// request that holds only that one replays nothing worth dropping.
func requestReplaysGeminiReasoning(req *generateContentRequest) bool {
	for _, c := range req.Contents {
		for _, p := range c.Parts {
			if p.Thought {
				return true
			}
			if p.ThoughtSignature != "" && p.ThoughtSignature != geminiSkipSignatureValidator {
				return true
			}
		}
	}
	return false
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

// geminiSkipSignatureValidator is the placeholder signature Google's thought
// signatures guide names for a function call the model did not write itself.
// Gemini 3 and later reject a current-turn call that carries no signature at
// all, so a call that lost its own — an older session's, or one another producer
// made — is given this value instead. It degrades the model's output, so it is a
// fallback only: a call that carries its real signature never gets it.
const geminiSkipSignatureValidator = "skip_thought_signature_validator"

// geminiReplayLayout returns the part layout m carries for replay to model: its
// own reply, for the model that wrote it. A carry recorded without a producer is
// not replayed either, for the same reason a thinking block is not: only the
// model the signatures belong to is given them.
func geminiReplayLayout(m llm.Message, model string) ([]llm.GeminiPart, bool) {
	if strings.TrimSpace(m.ReasoningModel) == "" || !llm.ReplaysReasoningTo(m, model) {
		return nil, false
	}
	return llm.DecodeGeminiParts(m.Name)
}

// geminiPartsFromLayout rebuilds a reply's parts as the model returned them,
// followed by the parts the layout does not cover. ok is false when the layout
// names a call the message does not hold: the layout is then unusable, and the
// caller sends the ordinary projection instead of a reply with a missing call.
func geminiPartsFromLayout(m llm.Message, layout []llm.GeminiPart, toolNames map[string]string) ([]part, bool) {
	out := make([]part, 0, len(layout)+len(m.ToolCalls))
	// coveredText counts the answer-text parts the layout rebuilt, and
	// coveredCall marks the calls it referenced: together they are how much of
	// the reply the layout speaks for.
	coveredText := 0
	coveredCall := make([]bool, len(m.ToolCalls))
	for _, lp := range layout {
		switch lp.Kind {
		case "thought":
			out = append(out, part{Text: lp.Text, Thought: true, ThoughtSignature: lp.Signature})
		case "call":
			if lp.Call < 0 || lp.Call >= len(m.ToolCalls) {
				return nil, false
			}
			coveredCall[lp.Call] = true
			tc := m.ToolCalls[lp.Call]
			name := strings.TrimSpace(tc.Function.Name)
			if name == "" {
				// A call with no name cannot be replayed: Gemini would reject it
				// and there is nothing to pair its result with.
				continue
			}
			if id := strings.TrimSpace(tc.ID); id != "" {
				toolNames[id] = name
			}
			out = append(out, part{
				FunctionCall:     &functionCall{Name: name, Args: argumentsValue(tc.Function.Arguments)},
				ThoughtSignature: lp.Signature,
			})
		default:
			// An empty text part carrying a signature is legal only because
			// part's marshaler states the text field it belongs to. Such a part
			// covers no answer text: it holds none.
			if lp.Text != "" {
				coveredText++
			}
			out = append(out, part{Text: lp.Text, ThoughtSignature: lp.Signature})
		}
	}
	// A reply that was interrupted only recorded its prefix in the layout: the
	// answer text still open at the cut is sealed by the engine afterwards, and
	// a call the adapter never got to append is missing from it entirely. What
	// the layout does not cover goes after it, in the message's own order —
	// answer text first, then calls — without a signature: none ever arrived
	// for those parts, and inventing one would fail the model's own check.
	texts := 0
	for _, p := range m.Parts {
		if p.Type != llm.ContentTypeText {
			continue
		}
		texts++
		if texts <= coveredText || strings.TrimSpace(p.Text) == "" {
			continue
		}
		out = append(out, part{Text: p.Text})
	}
	for i, tc := range m.ToolCalls {
		if coveredCall[i] {
			continue
		}
		name := strings.TrimSpace(tc.Function.Name)
		if name == "" {
			continue
		}
		if id := strings.TrimSpace(tc.ID); id != "" {
			toolNames[id] = name
		}
		out = append(out, part{FunctionCall: &functionCall{Name: name, Args: argumentsValue(tc.Function.Arguments)}})
	}
	return out, true
}

// geminiLayoutHasReasoning reports a layout worth carrying: one that holds a
// thought-signature or a thought summary. Without either, the layout says
// nothing the ordinary projection does not already send.
func geminiLayoutHasReasoning(layout []llm.GeminiPart) bool {
	for _, p := range layout {
		if p.Kind == "thought" || p.Signature != "" {
			return true
		}
	}
	return false
}

// geminiThoughtText returns the reasoning a layout carries: its thought-summary
// texts, in order, as one string. A surface shows this as thinking; it is never
// part of the answer.
func geminiThoughtText(layout []llm.GeminiPart) string {
	var sb strings.Builder
	for _, p := range layout {
		if p.Kind == "thought" {
			sb.WriteString(p.Text)
		}
	}
	return sb.String()
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

// parseResponse turns a generateContent response into a result, also
// returning the candidate's finish reason so the non-streaming replay can
// report it as the stop reason. model is the one the request was addressed to;
// it is recorded as the producer of the reasoning the response carries, which
// is what lets the replay hand that reasoning back to it alone.
func parseResponse(status int, payload []byte, retryAfter, model string) (*llm.Result, string, error) {
	var parsed generateContentResponse
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return nil, "", fmt.Errorf("google: decode response: %w", err)
	}
	// usageMetadata is parsed before the response is judged usable, so the
	// failures below can still carry the billing the attempt already had.
	usage := parsed.UsageMetadata
	var billed *llm.Usage
	if usage.PromptTokenCount > 0 || usage.CandidatesTokenCount > 0 || usage.ThoughtsTokenCount > 0 {
		billed = &llm.Usage{
			InputTokens: usage.PromptTokenCount,
			// Thinking tokens are billed output, so they count towards the
			// output figure.
			OutputTokens: usage.CandidatesTokenCount + usage.ThoughtsTokenCount,
		}
	}
	if len(parsed.Error) > 0 && string(parsed.Error) != "null" {
		return nil, "", newAPIError(status, payload, retryAfter)
	}
	if len(parsed.Candidates) == 0 {
		return nil, "", llm.NewExecuteError(fmt.Errorf("google: response has no candidates: %s", truncateMessage(strings.TrimSpace(string(payload)))), billed)
	}
	candidate := parsed.Candidates[0]
	msg := &llm.Message{Role: llm.RoleAssistant}
	// Text and calls are collected in the order they arrived, so a response that
	// interleaves them keeps its sequence within each of the two shapes. The
	// layout records those same parts in arrival order — thought summaries
	// included, each with the signature it came with — because Gemini wants its
	// own reply replayed exactly as it returned it: the signature inside its
	// original part, and the thought summaries are reasoning, never answer text.
	layout := make([]llm.GeminiPart, 0, len(candidate.Content.Parts))
	for _, p := range candidate.Content.Parts {
		switch {
		case p.FunctionCall != nil:
			msg.ToolCalls = append(msg.ToolCalls, googleToolCallFromPart(p))
			layout = append(layout, llm.GeminiPart{Kind: "call", Call: len(msg.ToolCalls) - 1, Signature: p.ThoughtSignature})
		case p.Thought:
			// A thought summary is the model thinking out loud, not its answer:
			// sending it as answer text would hand the person (and, after a
			// model switch, another model) reasoning as this reply.
			layout = append(layout, llm.GeminiPart{Kind: "thought", Text: p.Text, Signature: p.ThoughtSignature})
		case p.Text != "":
			msg.Parts = append(msg.Parts, llm.Text(p.Text))
			layout = append(layout, llm.GeminiPart{Kind: "text", Text: p.Text, Signature: p.ThoughtSignature})
		case p.ThoughtSignature != "":
			// A part that is only a signature still has to come back: the API may
			// deliver one in an empty text part, and it belongs to this reply.
			layout = append(layout, llm.GeminiPart{Kind: "text", Signature: p.ThoughtSignature})
		}
	}
	if geminiLayoutHasReasoning(layout) {
		// The layout rides the reasoning carry, so the next request gives this
		// model its own signatures and thought summaries back
		// (geminiReplayLayout). Without them Gemini 3 and later refuse the
		// current turn's calls, and earlier models re-derive their plan on every
		// step of a tool loop.
		msg.Name = llm.EncodeGeminiParts(layout)
		msg.ReasoningModel = strings.TrimSpace(model)
	}
	if len(msg.Parts) == 0 && len(msg.ToolCalls) == 0 {
		if llm.OutputLimitReached(candidate.FinishReason) {
			// MAX_TOKENS before any content is a cut, not a refusal: the
			// round produced an output-limit fragment (here an empty one),
			// and Execute's recovery loop decides whether to re-ask. An
			// empty turn from any other finish reason (safety, recitation, a
			// filtered prompt) really is a refusal to answer, and stays an
			// error.
			return &llm.Result{
				Message:    msg,
				StopReason: llm.StopReasonOutputLimit,
				Usage:      billed,
			}, candidate.FinishReason, nil
		}
		// A STOP that billed output tokens yet delivered nothing is a blank
		// output to re-send (plans/273), not a refusal: STOP is the model's
		// own "done", so the provider charged for an answer it never handed
		// over.
		if billed != nil && billed.OutputTokens > 0 && strings.EqualFold(candidate.FinishReason, "STOP") {
			return nil, candidate.FinishReason, llm.NewExecuteError(fmt.Errorf("google: response was empty (finish reason %q): %w", candidate.FinishReason, llm.ErrBlankModelOutput), billed)
		}
		// An empty turn from any other finish reason is a refusal to answer
		// (safety, recitation, a filtered prompt). Returning it as a silent
		// empty message would look like a successful turn that said nothing.
		return nil, candidate.FinishReason, llm.NewExecuteError(fmt.Errorf("google: response was empty (finish reason %q)", candidate.FinishReason), billed)
	}
	result := &llm.Result{Message: msg}
	// Why the candidate stopped, normalized: MAX_TOKENS maps onto the
	// output-limit reason the engine reads.
	result.StopReason = llm.NormalizeStopReason(candidate.FinishReason)
	result.Usage = billed
	return result, candidate.FinishReason, nil
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
