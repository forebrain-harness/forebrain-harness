// Copyright 2026 Simone Vellei
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package llm

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// ContentType identifies the kind of content within a ContentPart.
type ContentType string

const (
	// ContentTypeText is a plain-text content part.
	ContentTypeText ContentType = "text"
	// ContentTypeImageURL is an image referenced by URL.
	ContentTypeImageURL ContentType = "image_url"
	// ContentTypeImageBase64 is an image provided as raw base64-encoded bytes.
	// Use ImageBase64 or ImageFile to create parts of this type.
	ContentTypeImageBase64 ContentType = "image_base64"
)

// ContentPart is a single piece of content within a message — either text or an image.
type ContentPart struct {
	// Type identifies the kind of content.
	Type ContentType
	// Text holds the text content when Type is ContentTypeText.
	Text string
	// ImageURL holds the image URL when Type is ContentTypeImageURL.
	ImageURL string
	// ImageBase64 holds the base64-encoded image bytes when Type is ContentTypeImageBase64.
	ImageBase64 string
	// MIMEType is the MIME type of the image (e.g. "image/png") when Type is ContentTypeImageBase64.
	MIMEType string
}

// CompactionState is an opaque server-generated Responses API compaction item.
// EncryptedContent must be replayed verbatim on subsequent requests.
type CompactionState struct {
	// Type is the provider wire item type. Older checkpoints omit it and should
	// replay as the standard "compaction" item.
	Type             string `json:"type,omitempty"`
	ID               string `json:"id,omitempty"`
	EncryptedContent string `json:"encrypted_content"`
}

// Text returns a ContentPart containing the given plain text.
func Text(s string) ContentPart {
	return ContentPart{Type: ContentTypeText, Text: s}
}

// ImageURL returns a ContentPart referencing an image at the given URL.
func ImageURL(url string) ContentPart {
	return ContentPart{Type: ContentTypeImageURL, ImageURL: url}
}

// ImageBase64 returns a ContentPart carrying a base64-encoded image.
//
// mimeType must be one of: "image/jpeg", "image/png", "image/gif", "image/webp".
// data must be the standard base64 encoding of the raw image bytes.
func ImageBase64(mimeType, data string) ContentPart {
	return ContentPart{Type: ContentTypeImageBase64, MIMEType: mimeType, ImageBase64: data}
}

// ImageFile reads the image at path and returns a ContentPart with its
// base64-encoded content.
//
// The MIME type is detected from the file contents (falling back to the file
// extension when detection is ambiguous). Supported types are the same as
// those accepted by OpenAI and Anthropic: image/jpeg, image/png, image/gif,
// and image/webp.
func ImageFile(path string) (ContentPart, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ContentPart{}, fmt.Errorf("ImageFile: read %q: %w", path, err)
	}

	mimeType := detectImageMIMEType(data, path)
	switch mimeType {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		// accepted
	default:
		return ContentPart{}, fmt.Errorf("ImageFile: unsupported image type %q for %q", mimeType, path)
	}

	b64 := base64.StdEncoding.EncodeToString(data)
	return ImageBase64(mimeType, b64), nil
}

// detectImageMIMEType sniffs the MIME type from the first 512 bytes, then
// falls back to the file extension.
func detectImageMIMEType(data []byte, path string) string {
	sniff := data
	if len(sniff) > 512 {
		sniff = sniff[:512]
	}
	mt := http.DetectContentType(sniff)
	// http.DetectContentType returns "image/jpeg", "image/png", "image/gif",
	// "image/webp" for the respective formats.
	if strings.HasPrefix(mt, "image/") {
		return mt
	}
	// Fall back to extension.
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	}
	return mt
}

// FunctionCall holds the name and JSON-encoded arguments of a tool invocation.
type FunctionCall struct {
	// Name is the name of the tool being called.
	Name string
	// Arguments is the raw JSON-encoded argument object.
	Arguments string
}

// ToolCall represents a single tool invocation requested by the model.
type ToolCall struct {
	// ID is the opaque identifier assigned by the model to correlate this call with its result.
	ID string
	// Type is the tool type (always ToolTypeFunction for function tools).
	Type string
	// Function holds the name and arguments for this call.
	Function FunctionCall
}

// Message is a single turn in a conversation between a user, assistant, or tool.
type Message struct {
	// Role is the participant role: one of RoleSystem, RoleUser, RoleAssistant, RoleTool.
	Role string
	// Parts holds the multimodal content of the message.
	// For simple text messages this will be a single ContentPart with Type ContentTypeText.
	Parts []ContentPart
	// ToolCalls holds tool invocations requested by the assistant.
	// Only populated on assistant messages.
	ToolCalls []ToolCall
	// ToolCallID is the ID of the ToolCall this message is a response to.
	// Only set on tool-result messages (Role == RoleTool).
	ToolCallID string
	// Name is an optional participant name, used by some providers.
	Name string
	// IsMeta marks a user-role message as a meta/attachment message (e.g.
	// plan-mode reminders, project-context addenda) rather than genuine user
	// input. Meta messages are persisted to the transcript and treated as context, but are not
	// real user turns. LLM adapters serialize meta messages as ordinary user
	// messages - IsMeta is an internal-only marker that never reaches the API.
	IsMeta bool
	// Ephemeral marks a message that is re-derived for every request and must
	// never be written to the session transcript. It is sent to the model but
	// excluded from persistence, so a turn that is retried (or fails and is
	// re-sent) cannot accumulate copies of it in the stored history. Used for
	// per-turn injections such as the explicit skill activation block, which is
	// rebuilt from the slash-command context on each request.
	Ephemeral bool
	// Compaction is set instead of Role/Parts for an opaque remote-compaction
	// checkpoint item. Providers that do not support remote compaction ignore it.
	Compaction *CompactionState `json:"compaction,omitempty"`
	// AgentMessage preserves a Responses API multi-agent message as a native
	// input item across remote compaction and checkpoint resume.
	AgentMessage *AgentMessageState `json:"agent_message,omitempty"`
	// ToolExecutionTiming is internal metadata for tool-result messages. Provider
	// adapters project only model-visible fields and must not serialize it into
	// model requests.
	ToolExecutionTiming *ExecutionTiming `json:"tool_execution_timing,omitempty"`
	// ToolDisplay preserves the exact user-visible completion card separately
	// from the model-facing tool result. Write tools, for example, return only
	// "ok" to the model while their live card contains a diff. Session
	// persistence stores this metadata in PartsJSON; providers must never see it.
	ToolDisplay *ToolDisplayState `json:"-"`
	// MemoryCitation preserves provider-invisible provenance parsed from hidden
	// assistant citation markup. Provider adapters must never serialize it.
	MemoryCitation *MemoryCitation `json:"-"`
}

// MemoryCitation identifies the memory excerpts and source conversations used
// by an assistant response.
type MemoryCitation struct {
	Entries    []MemoryCitationEntry `json:"entries"`
	RolloutIDs []string              `json:"rollout_ids"`
}

type MemoryCitationEntry struct {
	Path      string `json:"path"`
	LineStart int    `json:"line_start"`
	LineEnd   int    `json:"line_end"`
	Note      string `json:"note"`
}

type ToolDisplayState struct {
	Body         string
	Summary      string
	ToolMetaJSON string
}

type AgentMessageState struct {
	ID        string                `json:"id,omitempty"`
	Author    string                `json:"author"`
	Recipient string                `json:"recipient"`
	Content   []AgentMessageContent `json:"content"`
}

type AgentMessageContent struct {
	Type             string `json:"type"`
	Text             string `json:"text,omitempty"`
	EncryptedContent string `json:"encrypted_content,omitempty"`
}

// TextContent returns the concatenation of all text parts in the message.
func (m Message) TextContent() string {
	return TextContent(m.Parts...)
}

// TextContent returns the concatenation of all text parts across the given ContentParts.
func TextContent(parts ...ContentPart) string {
	var sb strings.Builder
	for i, p := range parts {
		if p.Type == ContentTypeText {
			if i > 0 && sb.Len() > 0 {
				sb.WriteByte('\n')
			}
			sb.WriteString(p.Text)
		}
	}
	return sb.String()
}

// Message role constants.
const (
	// RoleSystem is the role for system (instruction) messages.
	RoleSystem = "system"
	// RoleDeveloper is the role for developer instructions. Providers that do not
	// support a developer role must map it to their closest instruction role.
	RoleDeveloper = "developer"
	// RoleUser is the role for user messages.
	RoleUser = "user"
	// RoleAssistant is the role for assistant (model) messages.
	RoleAssistant = "assistant"
	// RoleTool is the role for tool-result messages.
	RoleTool = "tool"
	// ToolTypeFunction is the tool type string for function-style tools.
	ToolTypeFunction = "function"
)

// SystemMessage constructs a system-role Message containing a single text part.
func SystemMessage(text string) Message {
	return Message{Role: RoleSystem, Parts: []ContentPart{Text(text)}}
}

// UserMessage constructs a user-role Message from the given content parts.
func UserMessage(parts ...ContentPart) Message {
	return Message{Role: RoleUser, Parts: parts}
}

// AssistantMessage constructs an assistant-role Message from the given parts and optional tool calls.
func AssistantMessage(parts []ContentPart, toolCalls ...ToolCall) Message {
	return Message{Role: RoleAssistant, Parts: parts, ToolCalls: toolCalls}
}

// ToolResultMessage constructs a tool-role Message carrying the result of a tool call.
func ToolResultMessage(toolCallID string, parts ...ContentPart) Message {
	return Message{Role: RoleTool, ToolCallID: toolCallID, Parts: parts}
}

// Usage holds token consumption figures from a single LLM call.
type Usage struct {
	// InputTokens is the number of tokens in the prompt sent to the model.
	InputTokens int
	// OutputTokens is the number of tokens produced by the model.
	OutputTokens int
	// CacheCreationInputTokens is the number of input tokens written to the
	// provider prompt cache for this response.
	CacheCreationInputTokens int
	// CacheReadInputTokens is the number of input tokens read from the provider
	// prompt cache for this response.
	CacheReadInputTokens int
}

// Result represents the output of an LLM execution, including the assistant message and any tool calls.
type Result struct {
	Message *Message
	// Usage holds token counts for this call. May be nil when the provider does
	// not return usage information.
	Usage *Usage
	// Session, when non-nil, carries the full message array accumulated by an
	// orchestration wrapper (e.g. toolOrchestrationLLM) that runs its own
	// internal tool-calling loop. The agent uses this to replace its session so
	// that intermediate tool calls and results are included in the final
	// Result.Session and persisted to the session store. When nil, callers fall
	// back to appending just Message to their session (the legacy behavior).
	Session []Message
}

// LLM is the minimal interface implemented by chat-model backends.
//
// Implementations are expected to accept a list of messages and return the next
// assistant message. Tools can be configured via WithTools.
type LLM interface {
	Execute(context.Context, []Message, []*Tool) (*Result, error)
}

// StructuredOutputSpec requests provider-enforced JSON output matching Schema.
// Strict must be honored by implementations; an implementation that cannot
// enforce the schema must return an error instead of prompt-parsing a fallback.
type StructuredOutputSpec struct {
	Name        string
	Description string
	Schema      map[string]any
	Strict      bool
}

// StructuredOutputLLM is implemented only by providers that can enforce a JSON
// schema for the final assistant output.
type StructuredOutputLLM interface {
	ExecuteStructured(context.Context, []Message, StructuredOutputSpec) (*Result, error)
}

// CompactMode selects the OpenAI Responses compaction event.
type CompactMode string

const (
	CompactModeRemoteV1 CompactMode = "remote_v1"
	CompactModeRemoteV2 CompactMode = "remote_v2"
)

// CompactResult is a complete replacement history returned by a provider.
type CompactResult struct {
	Messages []Message
	Usage    *Usage
}

// ContextCompactor is implemented by providers with native context compaction.
// Callers must replace, not append to, their active history with Messages.
type ContextCompactor interface {
	Compact(context.Context, []Message, []*Tool, CompactMode) (*CompactResult, error)
}

// LLMMiddleware wraps an LLM, decorating its Execute method with additional behavior.
//
// Use Use to compose multiple middlewares around a base LLM.
// Middleware order is preserved: Use(base, m1, m2) means m1 is the outermost layer and
// runs first, delegating to m2, which delegates to base.
type LLMMiddleware func(next LLM) LLM

// Use wraps base with the provided middlewares, returning a new LLM.
//
// Middlewares are applied in order: the first middleware listed is the outermost layer.
// For example, Use(base, logging, ratelimit) produces logging(ratelimit(base)).
func Use(base LLM, middlewares ...LLMMiddleware) LLM {
	result := base
	for i := len(middlewares) - 1; i >= 0; i-- {
		result = middlewares[i](result)
	}
	return result
}
