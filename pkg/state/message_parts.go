package state

import (
	"encoding/json"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

const (
	PartTypeToolCalls      = "tool_calls"
	PartTypeToolResultMeta = "tool_result_meta"
	PartTypeFileReference  = "file_reference"
	PartTypeIsMeta         = "is_meta" // marks a user message as meta/attachment (IsMeta=true)
	PartTypeToolDisplay    = "tool_display"
	PartTypeCompaction     = "compaction"
	PartTypeMemoryCitation = "memory_citation"
	// PartTypeAttachment records a file a user message attached that the
	// model is told about in the message's own text rather than shown. It is
	// display-only: it adds nothing to what the model is sent.
	PartTypeAttachment = "attachment"
)

func stringField(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, _ := m[key].(string)
	return v
}

type toolCallPart struct {
	Type      string           `json:"type"`
	ToolCalls []storedToolCall `json:"tool_calls,omitempty"`
}

type storedToolCall struct {
	ID       string             `json:"id,omitempty"`
	Type     string             `json:"type,omitempty"`
	Function storedFunctionCall `json:"function,omitempty"`
}

type storedFunctionCall struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type toolResultMetaPart struct {
	Type       string `json:"type"`
	ToolCallID string `json:"tool_call_id,omitempty"`
}

type toolDisplayPart struct {
	Type         string `json:"type"`
	Body         string `json:"body,omitempty"`
	Summary      string `json:"summary,omitempty"`
	ToolMetaJSON string `json:"tool_meta_json,omitempty"`
}

type attachmentParsePart struct {
	Type      string `json:"type"`
	FileID    string `json:"file_id,omitempty"`
	Label     string `json:"label,omitempty"`
	MIMEType  string `json:"mime_type,omitempty"`
	Parser    string `json:"parser,omitempty"`
	TextBytes int    `json:"text_bytes,omitempty"`
	Summary   string `json:"summary,omitempty"`
}

// ExtractReasoningText scans a message list for reasoning/thinking content.
// It checks each assistant message's Name for reasoningcarry-encoded data
// (OpenAI/DeepSeek reasoning summaries).  Anthropic thinking blocks are
// converted to fenced text parts upstream and appear as normal text content
// rather than a separate field.
func ExtractReasoningText(messages []llm.Message) string {
	var chunks []string
	for _, msg := range messages {
		if strings.TrimSpace(msg.Role) != llm.RoleAssistant {
			continue
		}
		if t := llm.ExtractSummary(msg.Name); t != "" {
			chunks = append(chunks, t)
		}
	}
	return strings.TrimSpace(strings.Join(chunks, "\n"))
}

func FileReferencePartJSON(fileID string, label string, mimeType string) string {
	return fileEntryPartJSON(PartTypeFileReference, fileID, label, mimeType)
}

// AttachmentPartJSON is the display-only record of a file a user message
// attached and names in its text.
func AttachmentPartJSON(fileID string, label string, mimeType string) string {
	return fileEntryPartJSON(PartTypeAttachment, fileID, label, mimeType)
}

func fileEntryPartJSON(partType, fileID, label, mimeType string) string {
	part := map[string]any{
		"type":    partType,
		"file_id": strings.TrimSpace(fileID),
	}
	if v := strings.TrimSpace(label); v != "" {
		part["label"] = v
	}
	if v := strings.TrimSpace(mimeType); v != "" {
		part["mime_type"] = v
	}
	b, err := json.Marshal(part)
	if err != nil {
		return ""
	}
	return string(b)
}

func MessagePartsJSON(msg llm.Message, fallbackText string) string {
	if msg.Compaction != nil {
		wireType := strings.TrimSpace(msg.Compaction.Type)
		if wireType == "" {
			wireType = PartTypeCompaction
		}
		b, err := json.Marshal([]map[string]any{{
			"type":              wireType,
			"id":                strings.TrimSpace(msg.Compaction.ID),
			"encrypted_content": msg.Compaction.EncryptedContent,
		}})
		if err == nil {
			return string(b)
		}
	}
	parts := contentBlocksJSON(msg.Parts, fallbackText)
	if msg.IsMeta {
		parts = append(parts, map[string]any{"type": PartTypeIsMeta})
	}
	switch strings.TrimSpace(msg.Role) {
	case llm.RoleAssistant:
		if citation := msg.MemoryCitation; citation != nil && (len(citation.Entries) > 0 || len(citation.RolloutIDs) > 0) {
			parts = append(parts, map[string]any{
				"type":        PartTypeMemoryCitation,
				"entries":     citation.Entries,
				"rollout_ids": citation.RolloutIDs,
			})
		}
		if len(msg.ToolCalls) > 0 {
			stored := make([]storedToolCall, 0, len(msg.ToolCalls))
			for _, tc := range msg.ToolCalls {
				name := strings.TrimSpace(tc.Function.Name)
				if name == "" {
					continue
				}
				callType := strings.TrimSpace(tc.Type)
				if callType == "" {
					callType = llm.ToolTypeFunction
				}
				stored = append(stored, storedToolCall{
					ID:   strings.TrimSpace(tc.ID),
					Type: callType,
					Function: storedFunctionCall{
						Name:      name,
						Arguments: strings.TrimSpace(tc.Function.Arguments),
					},
				})
			}
			if len(stored) > 0 {
				parts = append(parts, map[string]any{
					"type":       PartTypeToolCalls,
					"tool_calls": stored,
				})
			}
		}
	case llm.RoleTool:
		if toolCallID := strings.TrimSpace(msg.ToolCallID); toolCallID != "" {
			parts = append(parts, map[string]any{
				"type":         PartTypeToolResultMeta,
				"tool_call_id": toolCallID,
			})
		}
		if display := msg.ToolDisplay; display != nil {
			parts = append(parts, map[string]any{
				"type":           PartTypeToolDisplay,
				"body":           display.Body,
				"summary":        strings.TrimSpace(display.Summary),
				"tool_meta_json": strings.TrimSpace(display.ToolMetaJSON),
			})
		}
	}
	b, err := json.Marshal(parts)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func ParseMessage(role string, content string, partsJSON string) (llm.Message, bool) {
	if state, ok := parseCompactionState(partsJSON); ok {
		return llm.Message{Compaction: &state}, true
	}
	role = strings.TrimSpace(strings.ToLower(role))
	if role == "" {
		return llm.Message{}, false
	}
	parts, toolCalls, toolCallID, isMeta := ParseMessageParts(partsJSON, content)
	switch role {
	case llm.RoleUser:
		msg := llm.UserMessage(parts...)
		msg.IsMeta = isMeta
		return msg, true
	case llm.RoleAssistant:
		msg := llm.AssistantMessage(parts, toolCalls...)
		if citation, found := ParseMemoryCitationPart(partsJSON); found {
			msg.MemoryCitation = citation
		}
		return msg, true
	case llm.RoleTool:
		toolCallID = strings.TrimSpace(toolCallID)
		if toolCallID == "" {
			return llm.Message{}, false
		}
		msg := llm.ToolResultMessage(toolCallID, parts...)
		if display, found := ParseToolDisplayPart(partsJSON); found {
			msg.ToolDisplay = &display
		}
		return msg, true
	case llm.RoleSystem:
		return llm.SystemMessage(llm.TextContent(parts...)), true
	default:
		return llm.Message{}, false
	}
}

// ParseMemoryCitationPart returns persisted assistant memory provenance.
func ParseMemoryCitationPart(partsJSON string) (*llm.MemoryCitation, bool) {
	raw := strings.TrimSpace(partsJSON)
	if raw == "" || raw == "[]" {
		return nil, false
	}
	var parts []json.RawMessage
	if err := json.Unmarshal([]byte(raw), &parts); err != nil {
		return nil, false
	}
	for _, part := range parts {
		var header struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(part, &header); err != nil || strings.TrimSpace(header.Type) != PartTypeMemoryCitation {
			continue
		}
		var stored struct {
			Entries    []llm.MemoryCitationEntry `json:"entries"`
			RolloutIDs []string                  `json:"rollout_ids"`
		}
		if err := json.Unmarshal(part, &stored); err != nil || (len(stored.Entries) == 0 && len(stored.RolloutIDs) == 0) {
			return nil, false
		}
		return &llm.MemoryCitation{Entries: stored.Entries, RolloutIDs: stored.RolloutIDs}, true
	}
	return nil, false
}

// ParseToolDisplayPart extracts the provider-invisible live rendering payload
// stored alongside a tool result. Rows without this payload return false; this
// path does not attempt to reconstruct missing historical display data.
func ParseToolDisplayPart(partsJSON string) (llm.ToolDisplayState, bool) {
	raw := strings.TrimSpace(partsJSON)
	if raw == "" || raw == "[]" {
		return llm.ToolDisplayState{}, false
	}
	var parts []json.RawMessage
	if err := json.Unmarshal([]byte(raw), &parts); err != nil {
		return llm.ToolDisplayState{}, false
	}
	for _, part := range parts {
		var header struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(part, &header); err != nil || strings.TrimSpace(header.Type) != PartTypeToolDisplay {
			continue
		}
		var stored toolDisplayPart
		if err := json.Unmarshal(part, &stored); err != nil {
			return llm.ToolDisplayState{}, false
		}
		return llm.ToolDisplayState{
			Body:         stored.Body,
			Summary:      strings.TrimSpace(stored.Summary),
			ToolMetaJSON: strings.TrimSpace(stored.ToolMetaJSON),
		}, true
	}
	return llm.ToolDisplayState{}, false
}

func parseCompactionState(partsJSON string) (llm.CompactionState, bool) {
	var parts []map[string]any
	if json.Unmarshal([]byte(strings.TrimSpace(partsJSON)), &parts) != nil {
		return llm.CompactionState{}, false
	}
	for _, part := range parts {
		wireType := strings.TrimSpace(stringField(part, "type"))
		if wireType != PartTypeCompaction && wireType != "compaction_summary" {
			continue
		}
		state := llm.CompactionState{
			Type:             wireType,
			ID:               strings.TrimSpace(stringField(part, "id")),
			EncryptedContent: stringField(part, "encrypted_content"),
		}
		return state, strings.TrimSpace(state.EncryptedContent) != ""
	}
	return llm.CompactionState{}, false
}

// ParseMessageParts decodes a stored parts JSON blob back into content parts,
// tool calls, the tool_call_id (for tool-role rows) and whether the message
// was marked as meta/attachment (IsMeta=true). The IsMeta marker is a
// structural part (PartTypeIsMeta) that carries no content; it is consumed
// here and not emitted as a ContentPart.
func ParseMessageParts(partsJSON string, fallbackText string) ([]llm.ContentPart, []llm.ToolCall, string, bool) {
	raw := strings.TrimSpace(partsJSON)
	if raw == "" || raw == "[]" {
		if text := strings.TrimSpace(fallbackText); text != "" {
			return []llm.ContentPart{llm.Text(fallbackText)}, nil, "", false
		}
		return nil, nil, "", false
	}
	var parts []map[string]any
	if err := json.Unmarshal([]byte(raw), &parts); err != nil {
		if text := strings.TrimSpace(fallbackText); text != "" {
			return []llm.ContentPart{llm.Text(fallbackText)}, nil, "", false
		}
		return nil, nil, "", false
	}
	content := make([]llm.ContentPart, 0, len(parts))
	toolCalls := make([]llm.ToolCall, 0, 2)
	toolCallID := ""
	isMeta := false
	for _, part := range parts {
		switch strings.TrimSpace(stringField(part, "type")) {
		case PartTypeIsMeta:
			// Structural marker (no content): the carrying user message was
			// injected as a meta/attachment (e.g. plan-mode reminder).
			isMeta = true
		case "text":
			if text := stringField(part, "text"); strings.TrimSpace(text) != "" {
				content = append(content, llm.Text(text))
			}
		case "image":
			source, _ := part["source"].(map[string]any)
			switch strings.TrimSpace(stringField(source, "type")) {
			case "url":
				if url := strings.TrimSpace(stringField(source, "url")); url != "" {
					content = append(content, llm.ImageURL(url))
				}
			case "base64":
				data := strings.TrimSpace(stringField(source, "data"))
				mimeType := strings.TrimSpace(stringField(source, "media_type"))
				if data != "" || mimeType != "" {
					content = append(content, llm.ImageBase64(mimeType, data))
				}
			}
		case PartTypeFileReference:
			label := strings.TrimSpace(stringField(part, "label"))
			fileID := strings.TrimSpace(stringField(part, "file_id"))
			mimeType := strings.TrimSpace(stringField(part, "mime_type"))
			text := strings.TrimSpace(label)
			if text == "" {
				text = fileID
			}
			if mimeType != "" {
				text = strings.TrimSpace(text + " (" + mimeType + ")")
			}
			if text != "" {
				content = append(content, llm.Text("[attachment] "+text))
			}
		case PartTypeToolCalls:
			rawCalls, _ := part["tool_calls"].([]any)
			for _, rawCall := range rawCalls {
				callMap, _ := rawCall.(map[string]any)
				fnMap, _ := callMap["function"].(map[string]any)
				name := strings.TrimSpace(stringField(fnMap, "name"))
				if name == "" {
					continue
				}
				callType := strings.TrimSpace(stringField(callMap, "type"))
				if callType == "" {
					callType = llm.ToolTypeFunction
				}
				toolCalls = append(toolCalls, llm.ToolCall{
					ID:   strings.TrimSpace(stringField(callMap, "id")),
					Type: callType,
					Function: llm.FunctionCall{
						Name:      name,
						Arguments: strings.TrimSpace(stringField(fnMap, "arguments")),
					},
				})
			}
		case PartTypeToolResultMeta:
			toolCallID = strings.TrimSpace(stringField(part, "tool_call_id"))
		}
	}
	if len(content) == 0 {
		if text := strings.TrimSpace(fallbackText); text != "" {
			content = append(content, llm.Text(fallbackText))
		}
	}
	return content, toolCalls, toolCallID, isMeta
}

// StripUnansweredToolCalls rewrites a stored assistant parts JSON, removing any
// tool_calls whose IDs are not present in answered. Answered tool_calls and all
// non-tool_calls parts (text, images) are preserved. A tool_calls part is
// dropped entirely when none of its calls survive. It returns the rewritten JSON
// and whether anything changed; on any parse/marshal failure it returns the
// original JSON unchanged.
//
// This is the surgery behind (*SessionStore).RepairDanglingToolResults: a cancelled run
// or dismissed tool approval can leave an assistant message carrying tool_calls
// with no following tool-result message, which makes providers like
// OpenAI/DeepSeek reject the next request ("An assistant message with
// 'tool_calls' must be followed by tool messages responding to each
// 'tool_call_id'."). Stripping the unanswered calls is position-independent, so
// it heals a dangling row whether it sits at the transcript tail or mid-history.
func StripUnansweredToolCalls(partsJSON string, answered map[string]struct{}) (string, bool) {
	raw := strings.TrimSpace(partsJSON)
	if raw == "" || raw == "[]" {
		return partsJSON, false
	}
	var parts []map[string]any
	if err := json.Unmarshal([]byte(raw), &parts); err != nil {
		return partsJSON, false
	}
	changed := false
	out := make([]map[string]any, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(stringField(part, "type")) != PartTypeToolCalls {
			out = append(out, part)
			continue
		}
		rawCalls, _ := part["tool_calls"].([]any)
		kept := make([]any, 0, len(rawCalls))
		for _, rc := range rawCalls {
			callMap, _ := rc.(map[string]any)
			id := strings.TrimSpace(stringField(callMap, "id"))
			if _, ok := answered[id]; id != "" && ok {
				kept = append(kept, rc)
				continue
			}
			changed = true
		}
		if len(kept) == 0 {
			// Every call in this part was unanswered: drop the part.
			continue
		}
		if len(kept) != len(rawCalls) {
			part["tool_calls"] = kept
		}
		out = append(out, part)
	}
	if !changed {
		return partsJSON, false
	}
	b, err := json.Marshal(out)
	if err != nil {
		return partsJSON, false
	}
	return string(b), true
}
