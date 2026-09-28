package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	anthropicapi "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/google/uuid"
)

const (
	// anthropicDefaultModel is the model used when a provider entry names none.
	anthropicDefaultModel = "claude-sonnet-4-6"

	// anthropicDefaultMaxTokens is the output cap used when a provider entry
	// sets none.
	anthropicDefaultMaxTokens int64 = 2048
)

// errAnthropicToolMessageMissingToolCallID is returned when an OpenAI-shaped
// tool message carries no ToolCallID, which Anthropic requires as tool_use_id.
var errAnthropicToolMessageMissingToolCallID = errors.New("anthropic: tool message missing tool_call_id")

// anthropicUnsupportedRoleError is returned when converting an OpenAI-shaped
// message whose role has no Anthropic equivalent. The roles that convert are
// system, developer, user, assistant, and tool.
type anthropicUnsupportedRoleError struct {
	Role string
}

func (e *anthropicUnsupportedRoleError) Error() string {
	return fmt.Sprintf("anthropic: unsupported role %q", e.Role)
}

// anthropicNilResponseError is returned when the SDK yields no message.
type anthropicNilResponseError struct{}

func (e *anthropicNilResponseError) Error() string {
	return "anthropic: nil response"
}

type anthropicAgentLLM struct {
	client    anthropicapi.Client
	model     string
	maxTokens int64
	effort    anthropicapi.OutputConfigEffort
}

// NewAgentLLM builds the Anthropic client used by the agent runtime.
//
// There is deliberately no temperature parameter. Every current Anthropic model
// from Opus 4.7 on rejects temperature/top_p/top_k outright, and the models that
// still accept them reject any non-default value, so a sampling knob here can
// only produce request errors; behavior is steered through the prompt and the
// effort level instead.
func NewAgentLLM(apiKey, baseURL, model string, maxTokens int64, effort ...string) llm.LLM {
	c := &anthropicAgentLLM{
		model:     strings.TrimSpace(model),
		maxTokens: anthropicDefaultMaxTokens,
	}
	if c.model == "" {
		c.model = anthropicDefaultModel
	}
	if maxTokens > 0 {
		c.maxTokens = maxTokens
	}
	if len(effort) > 0 {
		switch strings.ToLower(strings.TrimSpace(effort[0])) {
		case "low", "medium", "high", "max":
			c.effort = anthropicapi.OutputConfigEffort(strings.ToLower(strings.TrimSpace(effort[0])))
		}
	}
	clientOpts := []option.RequestOption{}
	if strings.TrimSpace(apiKey) != "" {
		clientOpts = append(clientOpts, option.WithAPIKey(strings.TrimSpace(apiKey)))
	}
	if strings.TrimSpace(baseURL) != "" {
		clientOpts = append(clientOpts, option.WithBaseURL(strings.TrimSpace(baseURL)))
	}
	clientOpts = append(clientOpts, option.WithHTTPClient(llm.NewHTTPClient()))
	c.client = anthropicapi.NewClient(clientOpts...)
	return c
}

func (c *anthropicAgentLLM) Execute(ctx context.Context, messages []llm.Message, tools []*llm.Tool) (outResult *llm.Result, outErr error) {
	// Every error leaving this package is normalized here rather than at each
	// return, so a path added later cannot leak a raw SDK error to Layer 0.
	defer func() { outErr = normalizeError(outErr) }()
	system, anthropicMessages, err := agentAnthropicMessagesToAPI(messages)
	if err != nil {
		return nil, err
	}
	params := anthropicapi.MessageNewParams{
		Model:     anthropicapi.Model(strings.TrimSpace(c.model)),
		MaxTokens: c.maxTokens,
		Messages:  anthropicMessages,
		System:    system,
	}
	if c.effort != "" {
		params.OutputConfig.Effort = c.effort
	}
	if llm.Fast(ctx) {
		params.ServiceTier = anthropicapi.MessageNewParamsServiceTierAuto
	}
	if len(tools) > 0 {
		params.Tools = agentAnthropicTools(tools)
	}
	// Must run after Tools is populated: the breakpoint on the system block
	// caches the tool definitions with it, so the tools have to be part of the
	// request the markers describe.
	ApplyPromptCache(params.System, params.Messages)
	stream := c.client.Messages.NewStreaming(ctx, params)
	defer func() { _ = stream.Close() }()

	sink := llm.StreamSinkFrom(ctx)
	if sink != nil && sink.OnEnd != nil {
		defer sink.OnEnd()
	}
	if err := stream.Err(); err != nil {
		return nil, err
	}
	if sink != nil && sink.Streamed != nil {
		*sink.Streamed = true
	}

	var res anthropicapi.Message
	thinkingBlocks := make(map[int64]bool)
	var reportedInput, reportedOutput int64
	// Tracked independently of res.Usage: anthropic-sdk-go's Message.Accumulate
	// only copies OutputTokens out of a MessageDeltaEvent (messageutil.go),
	// silently dropping InputTokens/CacheCreationInputTokens/CacheReadInputTokens
	// from res.Usage whenever a provider reports cache accounting there rather
	// than in message_start — confirmed against a real Anthropic-compatible
	// endpoint that only ever populates cache_read/cache_creation on
	// message_delta, never message_start. Providers are free to place these
	// fields on either event, so the fix tracks the latest value seen from the
	// raw event on both message_start and message_delta ourselves, rather than
	// trusting the SDK's accumulated Usage, which real production traffic can
	// silently zero out.
	var latestUsage anthropicapi.Usage
	responseStarted := false
	reportUsage := func(input, output, cacheCreationInput, cacheReadInput int64) {
		if sink == nil {
			return
		}
		// Anthropic reports uncached, cache-write, and cache-read input as
		// disjoint buckets. Context occupancy must include all three; using
		// input_tokens alone makes a heavily cached prompt look nearly empty.
		totalInput := input + cacheCreationInput + cacheReadInput
		var deltaInput, deltaOutput int64
		if totalInput > reportedInput {
			deltaInput = totalInput - reportedInput
			reportedInput = totalInput
		}
		if output > reportedOutput {
			deltaOutput = output - reportedOutput
			reportedOutput = output
		}
		if sink.OnUsage != nil && (deltaInput > 0 || deltaOutput > 0) {
			sink.OnUsage(int(deltaInput), int(deltaOutput))
		}
		if sink.OnUsageSnapshot != nil && (totalInput > 0 || output > 0) {
			sink.OnUsageSnapshot(int(totalInput), int(output))
		}
	}

	for stream.Next() {
		event := stream.Current()
		switch event.Type {
		case "message_start":
			latestUsage.InputTokens = event.Message.Usage.InputTokens
			latestUsage.CacheCreationInputTokens = event.Message.Usage.CacheCreationInputTokens
			latestUsage.CacheReadInputTokens = event.Message.Usage.CacheReadInputTokens
			if event.Message.Usage.OutputTokens > latestUsage.OutputTokens {
				latestUsage.OutputTokens = event.Message.Usage.OutputTokens
			}
			reportUsage(
				event.Message.Usage.InputTokens,
				event.Message.Usage.OutputTokens,
				event.Message.Usage.CacheCreationInputTokens,
				event.Message.Usage.CacheReadInputTokens,
			)
		case "message_delta":
			// message_delta's InputTokens/cache fields, when present, are the
			// authoritative final accounting for this response (input-side
			// stats do not change mid-response) — overwrite rather than add.
			if event.Usage.InputTokens > 0 {
				latestUsage.InputTokens = event.Usage.InputTokens
			}
			if event.Usage.CacheCreationInputTokens > 0 {
				latestUsage.CacheCreationInputTokens = event.Usage.CacheCreationInputTokens
			}
			if event.Usage.CacheReadInputTokens > 0 {
				latestUsage.CacheReadInputTokens = event.Usage.CacheReadInputTokens
			}
			if event.Usage.OutputTokens > latestUsage.OutputTokens {
				latestUsage.OutputTokens = event.Usage.OutputTokens
			}
			reportUsage(
				event.Usage.InputTokens,
				event.Usage.OutputTokens,
				event.Usage.CacheCreationInputTokens,
				event.Usage.CacheReadInputTokens,
			)
		case "content_block_start":
			switch event.ContentBlock.Type {
			case "text":
				if event.ContentBlock.Text != "" {
					llm.NotifyResponseStarted(sink, &responseStarted)
				}
				if event.ContentBlock.Text != "" && sink != nil && sink.OnDelta != nil {
					sink.OnDelta(event.ContentBlock.Text)
				}
			case "thinking":
				thinkingBlocks[event.Index] = true
				if event.ContentBlock.Thinking != "" {
					llm.NotifyResponseStarted(sink, &responseStarted)
				}
				if event.ContentBlock.Thinking != "" && sink != nil && sink.OnReasoningDelta != nil {
					sink.OnReasoningDelta(event.ContentBlock.Thinking)
				}
			}
		case "content_block_delta":
			switch event.Delta.Type {
			case "text_delta":
				if event.Delta.Text != "" {
					llm.NotifyResponseStarted(sink, &responseStarted)
				}
				if event.Delta.Text != "" && sink != nil && sink.OnDelta != nil {
					sink.OnDelta(event.Delta.Text)
				}
			case "thinking_delta":
				if event.Delta.Thinking != "" {
					llm.NotifyResponseStarted(sink, &responseStarted)
				}
				if event.Delta.Thinking != "" && sink != nil && sink.OnReasoningDelta != nil {
					sink.OnReasoningDelta(event.Delta.Thinking)
				}
			}
		case "content_block_stop":
			if thinkingBlocks[event.Index] {
				delete(thinkingBlocks, event.Index)
				if sink != nil && sink.OnReasoningDone != nil {
					sink.OnReasoningDone()
				}
			}
		}
		if err := res.Accumulate(event); err != nil {
			return nil, err
		}
	}
	if err := stream.Err(); err != nil {
		return nil, err
	}
	msg, err := agentAnthropicMessageFromAPI(&res)
	if err != nil {
		return nil, err
	}
	return &llm.Result{
		Message: msg,
		Usage: &llm.Usage{
			InputTokens:              int(latestUsage.InputTokens),
			OutputTokens:             int(latestUsage.OutputTokens),
			CacheCreationInputTokens: int(latestUsage.CacheCreationInputTokens),
			CacheReadInputTokens:     int(latestUsage.CacheReadInputTokens),
		},
	}, nil
}

func agentAnthropicMessagesToAPI(messages []llm.Message) ([]anthropicapi.TextBlockParam, []anthropicapi.MessageParam, error) {
	var systemParts []string
	out := make([]anthropicapi.MessageParam, 0, len(messages))
	for _, m := range messages {
		switch m.Role {
		case llm.RoleSystem, llm.RoleDeveloper:
			text := m.TextContent()
			if strings.TrimSpace(text) != "" {
				systemParts = append(systemParts, text)
			}
		case llm.RoleUser:
			blocks := agentAnthropicContentBlocks(m.Parts)
			if len(blocks) > 0 {
				out = append(out, anthropicapi.NewUserMessage(blocks...))
			}
		case llm.RoleAssistant:
			blocks := make([]anthropicapi.ContentBlockParamUnion, 0, len(m.Parts)+len(m.ToolCalls))
			for _, p := range m.Parts {
				if p.Type == llm.ContentTypeText && strings.TrimSpace(p.Text) != "" {
					blocks = append(blocks, anthropicapi.NewTextBlock(p.Text))
				}
			}
			for _, tc := range m.ToolCalls {
				id := strings.TrimSpace(tc.ID)
				if id == "" {
					id = uuid.NewString()
				}
				var input any
				args := strings.TrimSpace(tc.Function.Arguments)
				if args == "" || !json.Valid([]byte(args)) {
					// Malformed JSON arguments (e.g. model emitted a premature
					// closing brace) must not abort the entire LLM request.
					// Fall back to an empty object so the provider accepts the
					// message; the tool handler already reported a parse error
					// for this call.
					input = map[string]any{}
				} else {
					if err := json.Unmarshal([]byte(args), &input); err != nil {
						input = map[string]any{}
					}
				}
				blocks = append(blocks, anthropicapi.NewToolUseBlock(id, input, tc.Function.Name))
			}
			out = append(out, anthropicapi.NewAssistantMessage(blocks...))
		case llm.RoleTool:
			toolUseID := strings.TrimSpace(m.ToolCallID)
			if toolUseID == "" {
				return nil, nil, errAnthropicToolMessageMissingToolCallID
			}
			content := agentAnthropicToolResultContent(m.Parts)
			toolBlock := anthropicapi.ToolResultBlockParam{
				ToolUseID: toolUseID,
				Content:   content,
			}
			out = append(out, anthropicapi.NewUserMessage(
				anthropicapi.ContentBlockParamUnion{OfToolResult: &toolBlock},
			))
		default:
			return nil, nil, &anthropicUnsupportedRoleError{Role: m.Role}
		}
	}
	joined := strings.TrimSpace(strings.Join(systemParts, "\n\n"))
	var system []anthropicapi.TextBlockParam
	if joined != "" {
		system = []anthropicapi.TextBlockParam{{Text: joined}}
	}
	return system, out, nil
}

func agentAnthropicContentBlocks(parts []llm.ContentPart) []anthropicapi.ContentBlockParamUnion {
	blocks := make([]anthropicapi.ContentBlockParamUnion, 0, len(parts))
	for _, p := range parts {
		switch p.Type {
		case llm.ContentTypeText:
			if strings.TrimSpace(p.Text) != "" {
				blocks = append(blocks, anthropicapi.NewTextBlock(p.Text))
			}
		case llm.ContentTypeImageURL:
			blocks = append(blocks, anthropicapi.NewImageBlock(
				anthropicapi.URLImageSourceParam{URL: p.ImageURL},
			))
		case llm.ContentTypeImageBase64:
			blocks = append(blocks, anthropicapi.NewImageBlock(
				anthropicapi.Base64ImageSourceParam{
					Data:      p.ImageBase64,
					MediaType: anthropicapi.Base64ImageSourceMediaType(p.MIMEType),
				},
			))
		}
	}
	return blocks
}

func agentAnthropicToolResultContent(parts []llm.ContentPart) []anthropicapi.ToolResultBlockParamContentUnion {
	content := make([]anthropicapi.ToolResultBlockParamContentUnion, 0, len(parts))
	for _, p := range parts {
		switch p.Type {
		case llm.ContentTypeText:
			text := p.Text
			content = append(content, anthropicapi.ToolResultBlockParamContentUnion{
				OfText: &anthropicapi.TextBlockParam{Text: text},
			})
		case llm.ContentTypeImageURL:
			imgBlock := anthropicapi.ImageBlockParam{
				Source: anthropicapi.ImageBlockParamSourceUnion{
					OfURL: &anthropicapi.URLImageSourceParam{URL: p.ImageURL},
				},
			}
			content = append(content, anthropicapi.ToolResultBlockParamContentUnion{
				OfImage: &imgBlock,
			})
		case llm.ContentTypeImageBase64:
			imgBlock := anthropicapi.ImageBlockParam{
				Source: anthropicapi.ImageBlockParamSourceUnion{
					OfBase64: &anthropicapi.Base64ImageSourceParam{
						Data:      p.ImageBase64,
						MediaType: anthropicapi.Base64ImageSourceMediaType(p.MIMEType),
					},
				},
			}
			content = append(content, anthropicapi.ToolResultBlockParamContentUnion{
				OfImage: &imgBlock,
			})
		}
	}
	return content
}

func agentAnthropicMessageFromAPI(m *anthropicapi.Message) (*llm.Message, error) {
	if m == nil {
		return nil, &anthropicNilResponseError{}
	}
	parts := make([]llm.ContentPart, 0)
	toolCalls := make([]llm.ToolCall, 0)
	for _, b := range m.Content {
		switch b.Type {
		case "text":
			if strings.TrimSpace(b.Text) != "" {
				parts = append(parts, llm.Text(b.Text))
			}
		case "tool_use":
			args := strings.TrimSpace(string(b.Input))
			if args == "" {
				args = "{}"
			}
			toolCalls = append(toolCalls, llm.ToolCall{
				ID:   b.ID,
				Type: llm.ToolTypeFunction,
				Function: llm.FunctionCall{
					Name:      b.Name,
					Arguments: args,
				},
			})
		}
	}
	msg := &llm.Message{
		Role:      llm.RoleAssistant,
		Parts:     parts,
		ToolCalls: toolCalls,
	}
	return msg, nil
}

func agentAnthropicTools(tools []*llm.Tool) []anthropicapi.ToolUnionParam {
	out := make([]anthropicapi.ToolUnionParam, 0, len(tools))
	for _, t := range tools {
		if t == nil {
			continue
		}
		inputSchema := agentAnthropicToolInputSchema(t.InputSchema())
		tool := anthropicapi.ToolParam{
			Name:        t.Name(),
			Description: anthropicapi.String(t.Description()),
			// Tool arguments stay non-strict so optional fields remain
			// optional (see compatOpenAITools).
			Strict:      anthropicapi.Bool(false),
			InputSchema: inputSchema,
		}
		out = append(out, anthropicapi.ToolUnionParam{OfTool: &tool})
	}
	return out
}

func agentAnthropicToolInputSchema(schema map[string]any) anthropicapi.ToolInputSchemaParam {
	if schema == nil {
		return anthropicapi.ToolInputSchemaParam{Properties: map[string]any{}}
	}
	properties := schema["properties"]
	required := make([]string, 0)
	if raw, ok := schema["required"]; ok {
		switch v := raw.(type) {
		case []string:
			required = append(required, v...)
		case []any:
			for _, it := range v {
				s, ok := it.(string)
				if ok {
					required = append(required, s)
				}
			}
		}
	}
	extra := make(map[string]any)
	for k, v := range schema {
		if k == "type" || k == "properties" || k == "required" {
			continue
		}
		extra[k] = v
	}
	return anthropicapi.ToolInputSchemaParam{
		Properties:  properties,
		Required:    required,
		ExtraFields: extra,
	}
}

var _ llm.LLM = (*anthropicAgentLLM)(nil)
