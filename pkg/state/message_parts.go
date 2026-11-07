package state

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state/dbx"
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
	// PartTypeOrigin names who wrote a user message on the person's behalf —
	// a heartbeat, a scheduled task. It is display-only: ParseMessageParts
	// ignores it, so the model is sent exactly what the same text typed by
	// hand would send, and the cached prefix is unchanged.
	PartTypeOrigin = "origin"
	// PartTypeReasoningTiming records how long the model spent on a reasoning
	// ("thinking") block, on the reasoning row that block produced. It is
	// display-only: a reasoning row is not part of the model history
	// (ParseMessage returns false for the role), so this part only lets a
	// replay draw "Thought for Ns" the way the live surface did.
	PartTypeReasoningTiming = "reasoning_timing"
	// PartTypeReasoningCarry keeps the reasoning an assistant message carries
	// for the model (llm.Message.Name's carry, and the model that wrote it),
	// so the next turn's history sends the model its own thinking back
	// exactly as the turn that produced it did. Without it every turn
	// rebuilt the history with that thinking stripped: a model that keeps
	// its reasoning across turns read a conversation in which it had never
	// thought and stopped thinking, and the prefix the provider had cached
	// forked at the first reasoning message of the previous turn. It never
	// reaches a surface.
	PartTypeReasoningCarry = "reasoning_carry"
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
//
// Only the current turn is read — the assistant messages after the last user
// message — because the history before it carries every earlier turn's
// reasoning back to the model too (PartTypeReasoningCarry), and that is not
// this turn's thinking.
func ExtractReasoningText(messages []llm.Message) string {
	start := 0
	for i := len(messages) - 1; i >= 0; i-- {
		if strings.TrimSpace(messages[i].Role) == llm.RoleUser && !messages[i].IsMeta {
			start = i + 1
			break
		}
	}
	var chunks []string
	for _, msg := range messages[start:] {
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

// Who wrote a user message on the person's behalf, carried by PartTypeOrigin.
const (
	MessageOriginHeartbeat = "heartbeat"
	MessageOriginCron      = "cron"
	// MessageOriginPeer is a message another local session sent this one over
	// the peer protocol (plans 194-197). Its text is the peer envelope exactly
	// as it arrived, and the row is meta: it is context, not the person typing.
	MessageOriginPeer = "peer"
	// MessageOriginPeerStatus is a peer message's receipt summary, delivered to
	// the sending conversation as a meta message (plan 197).
	MessageOriginPeerStatus = "peer_status"
)

// IsPeerOrigin reports whether an origin marks a message another session wrote
// over the peer protocol. Those rows are meta: nobody at this terminal typed
// them, so a model reading the conversation reads them as context the way it
// reads every other injected note.
func IsPeerOrigin(origin string) bool {
	switch strings.TrimSpace(origin) {
	case MessageOriginPeer, MessageOriginPeerStatus:
		return true
	}
	return false
}

// MessageDrawnOnSurface reports whether a stored row is one a surface draws.
//
// The meta marker means "no surface drew this row": runtime-authored context
// such as the environment block, a plan-mode reminder, or the context-clear
// anchor's copy of a gated assistant row. A replay skips those, because showing
// them would put words on screen that nobody typed.
//
// A peer message is the exception. It is meta for the model — it is context,
// not the person at this terminal typing — and it is drawn for the person, as
// the letter card the delivering turn drew live (plans 199, 200). The marker
// answers a model-facing question; whether a row has a card is a different
// question, and this is where both are answered together. Every transcript
// reader asks this one function, so the letter cannot go missing on one surface
// only.
func MessageDrawnOnSurface(partsJSON string) bool {
	if _, _, _, isMeta := ParseMessageParts(partsJSON, ""); !isMeta {
		return true
	}
	return IsPeerOrigin(MessageOrigin(partsJSON))
}

// WithMessageOrigin returns partsJSON with the origin part appended.
func WithMessageOrigin(partsJSON, origin string) string {
	var parts []map[string]any
	if err := json.Unmarshal([]byte(partsJSON), &parts); err != nil {
		return partsJSON
	}
	parts = append(parts, map[string]any{
		"type": PartTypeOrigin,
		"kind": strings.TrimSpace(origin),
	})
	b, err := json.Marshal(parts)
	if err != nil {
		return partsJSON
	}
	return string(b)
}

// MessageOrigin reads the origin part back; "" for a message the person wrote.
func MessageOrigin(partsJSON string) string {
	raw := strings.TrimSpace(partsJSON)
	if raw == "" || raw == "[]" {
		return ""
	}
	var parts []map[string]any
	if err := json.Unmarshal([]byte(raw), &parts); err != nil {
		return ""
	}
	for _, part := range parts {
		if strings.TrimSpace(stringField(part, "type")) == PartTypeOrigin {
			return strings.TrimSpace(stringField(part, "kind"))
		}
	}
	return ""
}

// ReasoningPartsJSON is a reasoning row's parts: its text and how long the
// model spent thinking it, so a replay says "Thought for 12s" as live did.
// The duration is a part rather than an exec_* column because the table's
// CHECK lets those columns carry only a tool or user row.
func ReasoningPartsJSON(text string, spent time.Duration) string {
	parts := make([]map[string]any, 0, 2)
	if body := strings.TrimSpace(text); body != "" {
		parts = append(parts, map[string]any{"type": "text", "text": body})
	}
	if spent < 0 {
		spent = 0
	}
	parts = append(parts, map[string]any{
		"type":        PartTypeReasoningTiming,
		"duration_ms": spent.Milliseconds(),
	})
	b, err := json.Marshal(parts)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// ParseReasoningTiming reads a reasoning row's thinking duration back; false
// when the row carries none (an older row, or one with no timing part).
func ParseReasoningTiming(partsJSON string) (time.Duration, bool) {
	raw := strings.TrimSpace(partsJSON)
	if raw == "" || raw == "[]" {
		return 0, false
	}
	var parts []map[string]any
	if err := json.Unmarshal([]byte(raw), &parts); err != nil {
		return 0, false
	}
	for _, part := range parts {
		if strings.TrimSpace(stringField(part, "type")) != PartTypeReasoningTiming {
			continue
		}
		ms, ok := numericField(part["duration_ms"])
		if !ok {
			return 0, false
		}
		return time.Duration(ms) * time.Millisecond, true
	}
	return 0, false
}

// numericField reads a JSON number that may decode as float64 or, when the
// decoder was configured with UseNumber, json.Number.
func numericField(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return i, true
		}
	case int64:
		return n, true
	case int:
		return int64(n), true
	}
	return 0, false
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
	// The origin part is display-only (ParseMessageParts ignores it), so what
	// the model is sent is the same whether the message was typed by hand or
	// written on the person's behalf.
	if origin := strings.TrimSpace(msg.Origin); origin != "" {
		parts = append(parts, map[string]any{"type": PartTypeOrigin, "kind": origin})
	}
	switch strings.TrimSpace(msg.Role) {
	case llm.RoleAssistant:
		if strings.HasPrefix(msg.Name, llm.Prefix) {
			carry := map[string]any{"type": PartTypeReasoningCarry, "carry": msg.Name}
			if model := strings.TrimSpace(msg.ReasoningModel); model != "" {
				carry["model"] = model
			}
			parts = append(parts, carry)
		}
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
	// One call covers the parts and the tool_meta_json a display part carries
	// inside them: both are this document's bytes, and a NUL in either arrives
	// as the `\u0000` escape PostgreSQL refuses to parse out of a jsonb cast.
	return string(dbx.ValidJSONText(b))
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
		msg.Name, msg.ReasoningModel = parseReasoningCarryPart(partsJSON)
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

// parseReasoningCarryPart returns the reasoning carry an assistant row keeps
// for the model and the model that wrote it; both empty when the row keeps
// none (every row stored before the part existed).
func parseReasoningCarryPart(partsJSON string) (carry, model string) {
	raw := strings.TrimSpace(partsJSON)
	if raw == "" || raw == "[]" || !strings.Contains(raw, PartTypeReasoningCarry) {
		return "", ""
	}
	var parts []json.RawMessage
	if err := json.Unmarshal([]byte(raw), &parts); err != nil {
		return "", ""
	}
	for _, part := range parts {
		var stored struct {
			Type  string `json:"type"`
			Carry string `json:"carry"`
			Model string `json:"model"`
		}
		if err := json.Unmarshal(part, &stored); err != nil || strings.TrimSpace(stored.Type) != PartTypeReasoningCarry {
			continue
		}
		if !strings.HasPrefix(stored.Carry, llm.Prefix) {
			return "", ""
		}
		return stored.Carry, strings.TrimSpace(stored.Model)
	}
	return "", ""
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
