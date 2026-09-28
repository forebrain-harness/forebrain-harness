package migrate

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

// codex_parse.go maps one Codex rollout jsonl onto forebrain rows (§4.2).
//
// response_item is the spine: it is complete and ordered under both
// history_modes, and its call_id pairs calls with outputs. event_msg is the
// UI-side line and duplicates the spine (C4), so it contributes exactly
// three things: agent_reasoning (legacy sessions' plaintext thinking),
// turn_aborted (folded into the turn's last assistant row), and token_count
// (usage fallback). No cross-stream joins (§2.3).

// codexSourceStats counts what the parser dropped or synthesized, so the
// report can state it instead of leaving the reader to guess.
type codexSourceStats struct {
	// EncryptedReasoning counts reasoning items dropped because the source
	// stored only encrypted_content (C3: plaintext is the only migratable
	// form; reasoning never reaches the model context anyway).
	EncryptedReasoning int
	// DeveloperMessages counts role=developer items dropped (§1.2 invariant
	// 2: the model-context rebuild has no developer branch).
	DeveloperMessages int
	// CancelledResults is parsedSession.cancelledResults, repeated here for
	// the report's per-session line.
	CancelledResults int
	// DroppedEvents counts event_msg records with no migratable content.
	DroppedEvents int
}

// codexRecord is the union of rollout line shapes; unknown payload types are
// skipped, unknown fields ignored.
type codexRecord struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

// codexSessionMeta is the session_meta payload: the first one in a file is
// the session's identity; a second one (a mid-session reopen) only refreshes
// cwd/git (§4.1).
type codexSessionMeta struct {
	SessionID    string `json:"session_id"`
	ID           string `json:"id"`
	Timestamp    string `json:"timestamp"`
	Cwd          string `json:"cwd"`
	HistoryMode  string `json:"history_mode"`
	ContextWidow struct {
		WindowID string `json:"window_id"`
	} `json:"context_window"`
	Git struct {
		Branch string `json:"branch"`
	} `json:"git"`
}

// codexMessageItem is a response_item of payload.type "message".
type codexMessageItem struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	Role    string `json:"role"`
	Content []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL string `json:"image_url"`
	} `json:"content"`
}

// codexToolCallItem covers custom_tool_call, function_call and
// tool_search_call: call_id pairs them with their output; input is the
// custom-tool JS script text, arguments the function-call JSON string.
type codexToolCallItem struct {
	Type       string `json:"type"`
	ID         string `json:"id"`
	CallID     string `json:"call_id"`
	Name       string `json:"name"`
	Namespace  string `json:"namespace"`
	Status     string `json:"status"`
	Input      string `json:"input"`
	Arguments  string `json:"arguments"`
	SearchCall struct {
		Action string `json:"action"`
	} `json:"search_call"`
}

// codexToolOutputItem covers the three output forms. The output field is
// either a content array (custom tools) or a plain string (function calls),
// so it stays raw and codexOutputBody decodes both.
type codexToolOutputItem struct {
	Type   string `json:"type"`
	ID     string `json:"id"`
	CallID string `json:"call_id"`
	Output json.RawMessage
}

// codexReasoningItem is a response_item of payload.type "reasoning".
type codexReasoningItem struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	Summary []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"summary"`
	EncryptedContent string `json:"encrypted_content"`
}

// codexEventItem is one event_msg payload; only the three types the spine
// cannot provide are honored.
type codexEventItem struct {
	Type   string `json:"type"`
	Text   string `json:"text"`
	Reason string `json:"reason"`
	Info   struct {
		LastTokenUsage *codexTokenUsage `json:"last_token_usage"`
	} `json:"info"`
}

type codexTokenUsage struct {
	InputTokens        int64 `json:"input_tokens"`
	OutputTokens       int64 `json:"output_tokens"`
	CachedInputTokens  int64 `json:"cached_input_tokens"`
	CacheWriteInput    int64 `json:"cache_write_input_tokens"`
	ReasoningOutputTok int64 `json:"reasoning_output_tokens"`
	TotalTokens        int64 `json:"total_tokens"`
}

// codexUsageRecord is a token_usage_record payload; thread_token_usage of
// the LAST record in a file is the session's token aggregate (§4.1).
type codexUsageRecord struct {
	ThreadTokenUsage *codexTokenUsage `json:"thread_token_usage"`
}

// codexCompacted is the compacted payload: the window chain is source truth
// (C2) and replacement_history carries the opaque compaction item verbatim
// (C1).
type codexCompacted struct {
	Message            string `json:"message"`
	ReplacementHistory []struct {
		Type             string         `json:"type"`
		ID               string         `json:"id"`
		Role             string         `json:"role"`
		Content          []codexContent `json:"content"`
		EncryptedContent string         `json:"encrypted_content"`
	} `json:"replacement_history"`
	WindowNumber     uint64 `json:"window_number"`
	WindowID         string `json:"window_id"`
	PreviousWindowID string `json:"previous_window_id"`
	FirstWindowID    string `json:"first_window_id"`
}

type codexContent struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	ImageURL string `json:"image_url"`
}

// codexTurnContext refreshes the aggregate cwd as the working directory
// changes turn to turn.
type codexTurnContext struct {
	Cwd string `json:"cwd"`
}

// codexInjectionPrefixes mark user messages the source injected rather than
// typed (§4.2): they still reach the model, but replay hides them like any
// other meta/attachment row.
var codexInjectionPrefixes = []string{
	"<environment_context>",
	"<codex_internal_context",
	"<turn_aborted>",
	"<image name=",
	"# AGENTS.md instructions for ",
}

// parseCodexRollout streams one rollout jsonl into a parsedSession. Rows
// follow file line order, which is the order forebrain replays by row id. Bad
// lines are counted and skipped rather than failing the session.
func parseCodexRollout(ctx context.Context, path, sessionID string, onBadLine func()) (*parsedSession, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	parsed := &parsedSession{SessionID: sessionID, byUUID: map[string]int{}}
	parser := &codexParser{session: parsed, stats: &parsed.stats}
	reader := bufio.NewReaderSize(file, 1<<20)
	lastTs := int64(0)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			var record codexRecord
			if decodeErr := json.Unmarshal(line, &record); decodeErr != nil || strings.TrimSpace(record.Type) == "" {
				if onBadLine != nil {
					onBadLine()
				}
			} else {
				ts := parser.record(record, lastTs)
				if ts > 0 {
					lastTs = ts
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
	}
	parser.finish()
	parsed.finalizeInvariants()
	parsed.rebuildIndexes()
	parsed.applyWindows()
	return parsed, nil
}

// codexParser holds the state one rollout pass needs.
type codexParser struct {
	session *parsedSession
	stats   *codexSourceStats
	// metaSeen counts session_meta records: the first fixes identity, a
	// second only refreshes cwd/git.
	metaSeen int
	// lastAssistant indexes the row a turn_aborted event folds into.
	lastAssistant int
	// firstUserText feeds the fallback title.
	firstUserText string
	// usageFallback accumulates token_count.info.last_token_usage while no
	// token_usage_record has arrived; the record, when present, wins whole.
	usageFallback codexTokenUsage
	// threadUsage is the latest token_usage_record.thread_token_usage.
	threadUsage *codexTokenUsage
}

// record folds one rollout line into rows, returning the line's unix
// timestamp (after monotonic fix-up).
func (p *codexParser) record(rec codexRecord, lastTs int64) int64 {
	ts := parseTimestamp(rec.Timestamp)
	if ts <= 0 {
		ts = lastTs + 1
	}
	p.observeTime(ts)
	switch strings.TrimSpace(rec.Type) {
	case "session_meta":
		p.sessionMeta(rec.Payload)
	case "response_item":
		p.responseItem(rec.Payload, ts)
	case "event_msg":
		p.eventItem(rec.Payload, ts)
	case "token_usage_record":
		var record codexUsageRecord
		if json.Unmarshal(rec.Payload, &record) == nil && record.ThreadTokenUsage != nil {
			p.threadUsage = record.ThreadTokenUsage
		}
	case "compacted":
		p.compactedRow(rec.Payload, ts)
	case "turn_context":
		var ctx codexTurnContext
		if json.Unmarshal(rec.Payload, &ctx) == nil {
			if cwd := strings.TrimSpace(ctx.Cwd); cwd != "" {
				p.session.Aggregates.Cwd = cwd
			}
		}
	default:
		// world_state and anything unrecognized: runtime state, no rows.
	}
	return ts
}

func (p *codexParser) observeTime(ts int64) {
	agg := &p.session.Aggregates
	if agg.FirstSeen == 0 || (ts > 0 && ts < agg.FirstSeen) {
		agg.FirstSeen = ts
	}
	if ts > agg.LastSeen {
		agg.LastSeen = ts
	}
}

// sessionMeta fixes identity on the first record and refreshes cwd/git after
// a mid-session reopen; it never splits one file into two sessions (§4.1).
func (p *codexParser) sessionMeta(payload json.RawMessage) {
	var meta codexSessionMeta
	if json.Unmarshal(payload, &meta) != nil {
		return
	}
	p.metaSeen++
	if cwd := strings.TrimSpace(meta.Cwd); cwd != "" {
		p.session.Aggregates.Cwd = cwd
	}
	if branch := strings.TrimSpace(meta.Git.Branch); branch != "" {
		p.session.Aggregates.GitBranch = branch
	}
	if p.metaSeen > 1 {
		return
	}
	if id := strings.TrimSpace(meta.SessionID); id != "" {
		p.session.SessionID = id
	} else if id := strings.TrimSpace(meta.ID); id != "" {
		p.session.SessionID = id
	}
	if windowID := strings.TrimSpace(meta.ContextWidow.WindowID); windowID != "" {
		p.session.initialWindowID = windowID
	}
}

// responseItem dispatches one model-side line by payload.type.
func (p *codexParser) responseItem(payload json.RawMessage, ts int64) {
	var header struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(payload, &header) != nil {
		return
	}
	switch strings.TrimSpace(header.Type) {
	case "message":
		var item codexMessageItem
		if json.Unmarshal(payload, &item) == nil {
			p.messageRow(item, ts)
		}
	case "custom_tool_call", "function_call", "tool_search_call":
		var item codexToolCallItem
		if json.Unmarshal(payload, &item) == nil {
			p.toolCallRow(item, ts)
		}
	case "custom_tool_call_output", "function_call_output", "tool_search_output":
		var item codexToolOutputItem
		if json.Unmarshal(payload, &item) == nil {
			p.toolOutputRow(item, ts)
		}
	case "reasoning":
		var item codexReasoningItem
		if json.Unmarshal(payload, &item) == nil {
			p.reasoningRow(item, ts)
		}
	default:
		// Unknown response_item subtype: no row.
	}
}

// messageRow maps a response_item message. developer items are dropped
// (invariant 2); user items the source injected carry the is_meta marker.
func (p *codexParser) messageRow(item codexMessageItem, ts int64) {
	role := strings.TrimSpace(item.Role)
	switch role {
	case "developer":
		p.stats.DeveloperMessages++
		return
	case "user", "assistant":
	default:
		return
	}
	texts := make([]string, 0, len(item.Content))
	var image llm.ContentPart
	for _, block := range item.Content {
		switch strings.TrimSpace(block.Type) {
		case "input_text", "output_text":
			texts = append(texts, block.Text)
		case "input_image":
			if part, ok := codexImagePart(block.ImageURL); ok && image.Type == "" {
				image = part
			}
		}
	}
	content := strings.Join(texts, "\n")
	if role == "user" {
		msg := llm.UserMessage()
		if isCodexInjectedUser(content) {
			msg.IsMeta = true
		}
		if strings.TrimSpace(content) != "" {
			msg.Parts = append(msg.Parts, llm.Text(content))
		}
		if image.Type != "" {
			msg.Parts = append(msg.Parts, image)
		}
		if p.firstUserText == "" && strings.TrimSpace(content) != "" && !msg.IsMeta {
			p.firstUserText = content
		}
		p.append(parsedRow{
			Role: llm.RoleUser, Content: content, MessageID: item.ID, SourceUUID: item.ID,
			PartsJSON: state.MessagePartsJSON(msg, content), CreatedAt: ts,
		})
		return
	}
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text(content)})
	p.append(parsedRow{
		Role: llm.RoleAssistant, Content: content, MessageID: item.ID, SourceUUID: item.ID,
		PartsJSON: state.MessagePartsJSON(msg, content), CreatedAt: ts,
	})
	p.lastAssistant = len(p.session.Rows) - 1
}

// codexImagePart splits a data:image/...;base64,... URI into forebrain's inline
// image part; a non-data URL yields nothing (the source holds no bytes to
// keep).
func codexImagePart(rawURL string) (llm.ContentPart, bool) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return llm.ContentPart{}, false
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "data" {
		return llm.ContentPart{}, false
	}
	mediaType, data, found := strings.Cut(parsed.Opaque, ",")
	if !found {
		mediaType, data, found = strings.Cut(strings.TrimPrefix(rawURL, "data:"), ",")
		if !found {
			return llm.ContentPart{}, false
		}
	}
	mediaType = strings.TrimSpace(strings.TrimSuffix(mediaType, ";base64"))
	if !strings.HasPrefix(mediaType, "image/") || data == "" {
		return llm.ContentPart{}, false
	}
	return llm.ImageBase64(mediaType, data), true
}

func isCodexInjectedUser(content string) bool {
	trimmed := strings.TrimSpace(content)
	for _, prefix := range codexInjectionPrefixes {
		if strings.HasPrefix(trimmed, prefix) {
			return true
		}
	}
	return false
}

// toolCallRow maps one call item. The pairing key is call_id; the item's own
// id becomes the row's message id (the dedup key). The call's identity is
// registered so its output row can carry the same tool metadata.
func (p *codexParser) toolCallRow(item codexToolCallItem, ts int64) {
	name := strings.TrimSpace(item.Name)
	if ns := strings.TrimSpace(item.Namespace); ns != "" {
		name = ns + "." + name
	}
	stepID := firstNonEmptyStr(strings.TrimSpace(item.CallID), strings.TrimSpace(item.ID))
	if name == "" || stepID == "" {
		return
	}
	arguments := json.RawMessage(codexCallArgumentsJSON(item))
	if p.session.toolCalls == nil {
		p.session.toolCalls = map[string]toolCallIdent{}
	}
	p.session.toolCalls[stepID] = toolCallIdent{Name: name, Arguments: arguments}
	meta, canonicalArgs := buildToolMeta(name, arguments, "")
	if strings.TrimSpace(item.Status) == "failed" {
		meta.Status = "failed"
	}
	msg := llm.AssistantMessage(nil, llm.ToolCall{
		ID: stepID, Type: llm.ToolTypeFunction,
		Function: llm.FunctionCall{Name: mapToolName(name), Arguments: canonicalArgs},
	})
	p.append(parsedRow{
		Role: llm.RoleAssistant, Content: "", MessageID: item.ID, SourceUUID: item.ID,
		PartsJSON:    state.MessagePartsJSON(msg, ""),
		ToolStepID:   stepID,
		ToolMetaJSON: marshalToolMeta(meta),
		CreatedAt:    ts,
		toolIdent:    toolCallIdent{Name: name, Arguments: arguments},
	})
}

// codexCallArgumentsJSON renders the call's input as the JSON the tool-meta
// and cancelled-answer paths re-read: function_call arguments verbatim,
// custom-tool script wrapped as {"script": ...}. No JS is parsed for exec
// scripts (§4.3) — the invocation line is the script's first line, and
// guessing deeper would display a command the user never ran.
func codexCallArgumentsJSON(item codexToolCallItem) string {
	if args := strings.TrimSpace(item.Arguments); args != "" {
		return args
	}
	if script := strings.TrimSpace(item.Input); script != "" {
		if b, err := json.Marshal(map[string]string{"script": script}); err == nil {
			return string(b)
		}
	}
	return "{}"
}

// toolOutputRow maps one output item onto a tool row.
func (p *codexParser) toolOutputRow(item codexToolOutputItem, ts int64) {
	stepID := firstNonEmptyStr(strings.TrimSpace(item.CallID), strings.TrimSpace(item.ID))
	if stepID == "" {
		return
	}
	body := codexOutputBody(item)
	ident := p.toolCallForStep(stepID)
	meta, _ := buildToolMeta(ident.Name, ident.Arguments, "")
	msg := llm.ToolResultMessage(stepID, llm.Text(body))
	msg.ToolDisplay = &llm.ToolDisplayState{
		Body: body, Summary: firstLine(body), ToolMetaJSON: marshalToolMeta(meta),
	}
	p.append(parsedRow{
		Role: llm.RoleTool, Content: body, MessageID: item.ID, SourceUUID: item.ID,
		PartsJSON:    state.MessagePartsJSON(msg, body),
		ToolStepID:   stepID,
		ToolMetaJSON: marshalToolMeta(meta),
		CreatedAt:    ts,
	})
}

// codexOutputBody concatenates an output item's text: custom outputs carry
// a content array, function outputs a plain string.
func codexOutputBody(item codexToolOutputItem) string {
	raw := strings.TrimSpace(string(item.Output))
	if raw == "" || raw == "null" {
		return ""
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(item.Output, &blocks); err == nil && blocks != nil {
		texts := make([]string, 0, len(blocks))
		for _, block := range blocks {
			texts = append(texts, block.Text)
		}
		return strings.Join(texts, "\n")
	}
	var text string
	if err := json.Unmarshal(item.Output, &text); err == nil {
		return text
	}
	return ""
}

// reasoningRow keeps only plaintext summaries: an encrypted-only item is
// dead weight the model context never reads (C3).
func (p *codexParser) reasoningRow(item codexReasoningItem, ts int64) {
	texts := make([]string, 0, len(item.Summary))
	for _, summary := range item.Summary {
		if strings.TrimSpace(summary.Text) != "" {
			texts = append(texts, summary.Text)
		}
	}
	if len(texts) == 0 {
		p.stats.EncryptedReasoning++
		return
	}
	text := strings.Join(texts, "\n")
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text(text)})
	p.append(parsedRow{
		Role: "reasoning", Content: text, MessageID: item.ID, SourceUUID: item.ID,
		PartsJSON: state.MessagePartsJSON(msg, text), CreatedAt: ts,
	})
}

// eventItem honors the three event types the spine cannot provide.
func (p *codexParser) eventItem(payload json.RawMessage, ts int64) {
	var item codexEventItem
	if json.Unmarshal(payload, &item) != nil {
		return
	}
	switch strings.TrimSpace(item.Type) {
	case "agent_reasoning":
		if text := strings.TrimSpace(item.Text); text != "" {
			msg := llm.AssistantMessage([]llm.ContentPart{llm.Text(text)})
			p.append(parsedRow{
				Role: "reasoning", Content: text, MessageID: fmt.Sprintf("reasoning-%d", ts), SourceUUID: "",
				PartsJSON: state.MessagePartsJSON(msg, text), CreatedAt: ts,
			})
		}
	case "turn_aborted":
		p.markTurnAborted(item.Reason)
	case "token_count":
		if usage := item.Info.LastTokenUsage; usage != nil {
			p.usageFallback.InputTokens += usage.InputTokens
			p.usageFallback.OutputTokens += usage.OutputTokens
		}
	default:
		p.stats.DroppedEvents++
	}
}

// markTurnAborted folds the interruption into the turn's last assistant row:
// the marker joins the visible body, both in the content column and in the
// parts JSON, so replay shows it wherever it reads from.
func (p *codexParser) markTurnAborted(reason string) {
	idx := p.lastAssistant
	if idx < 0 || idx >= len(p.session.Rows) {
		return
	}
	row := p.session.Rows[idx]
	if row.Role != llm.RoleAssistant || row.IsBoundary {
		return
	}
	marker := "(turn aborted"
	if r := strings.TrimSpace(reason); r != "" {
		marker += ": " + r
	}
	marker += ")"
	if strings.Contains(row.Content, marker) {
		return
	}
	content := strings.TrimSpace(row.Content)
	if content == "" {
		content = marker
	} else {
		content = content + "\n\n" + marker
	}
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text(content)})
	row.Content = content
	row.PartsJSON = state.MessagePartsJSON(msg, content)
	p.session.Rows[idx] = row
}

// compactedRow maps one compacted record: the window chain is source truth,
// replacement_history converts entry by entry, and the opaque compaction
// item replays verbatim (C1/C2, §4.2).
func (p *codexParser) compactedRow(payload json.RawMessage, ts int64) {
	var item codexCompacted
	if json.Unmarshal(payload, &item) != nil {
		return
	}
	history := make([]llm.Message, 0, len(item.ReplacementHistory))
	for _, entry := range item.ReplacementHistory {
		switch strings.TrimSpace(entry.Type) {
		case "compaction":
			if strings.TrimSpace(entry.EncryptedContent) == "" {
				continue
			}
			history = append(history, llm.Message{Compaction: &llm.CompactionState{
				Type:             "compaction",
				ID:               strings.TrimSpace(entry.ID),
				EncryptedContent: entry.EncryptedContent,
			}})
		case "message":
			if strings.TrimSpace(entry.Role) == "developer" {
				p.stats.DeveloperMessages++
				continue
			}
			msg, ok := codexHistoryMessage(entry.Role, entry.Content)
			if ok {
				history = append(history, msg)
			}
		}
	}
	detail := fmt.Sprintf("compact boundary (migrated from codex) · window %d", item.WindowNumber)
	part := state.CompactBoundaryPart{
		Trigger:            "codex",
		Strategy:           "migrated",
		SummarySource:      "migrated",
		ReplacementHistory: history,
		WindowNumber:       item.WindowNumber,
		FirstWindowID:      strings.TrimSpace(item.FirstWindowID),
		PreviousWindowID:   strings.TrimSpace(item.PreviousWindowID),
		WindowID:           strings.TrimSpace(item.WindowID),
	}
	if part.FirstWindowID != "" && p.session.initialWindowID == "" {
		p.session.initialWindowID = part.FirstWindowID
	}
	row := parsedRow{
		Role: llm.RoleSystem, Content: detail, MessageID: fmt.Sprintf("compacted-%d-%s", item.WindowNumber, item.WindowID),
		SourceUUID: fmt.Sprintf("compacted-%d-%s", item.WindowNumber, item.WindowID),
		PartsJSON:  state.PartsJSONWithCompactPart(detail, state.EncodeCompactBoundaryPart(part)),
		CreatedAt:  ts, IsBoundary: true, boundary: part,
	}
	p.append(row)
}

// codexHistoryMessage converts one replacement_history message entry into an
// llm.Message (images included; developer entries were dropped upstream).
func codexHistoryMessage(role string, content []codexContent) (llm.Message, bool) {
	switch strings.TrimSpace(role) {
	case "user":
		msg := llm.UserMessage()
		for _, block := range content {
			switch strings.TrimSpace(block.Type) {
			case "input_text", "text":
				msg.Parts = append(msg.Parts, llm.Text(block.Text))
			case "input_image":
				if part, ok := codexImagePart(block.ImageURL); ok {
					msg.Parts = append(msg.Parts, part)
				}
			}
		}
		return msg, true
	case "assistant":
		texts := make([]string, 0, len(content))
		for _, block := range content {
			texts = append(texts, block.Text)
		}
		return llm.AssistantMessage([]llm.ContentPart{llm.Text(strings.Join(texts, "\n"))}), true
	default:
		return llm.Message{}, false
	}
}

func (p *codexParser) append(row parsedRow) {
	p.session.Rows = append(p.session.Rows, row)
}

// finish fills the session aggregates only the whole file can answer and the
// fallback title.
func (p *codexParser) finish() {
	if p.threadUsage != nil {
		p.session.Aggregates.PromptTok = p.threadUsage.InputTokens
		p.session.Aggregates.CompletionTok = p.threadUsage.OutputTokens
	} else {
		p.session.Aggregates.PromptTok = p.usageFallback.InputTokens
		p.session.Aggregates.CompletionTok = p.usageFallback.OutputTokens
	}
	// Codex records no dollar cost; inventing one would be a lie (§4.1).
	p.session.Aggregates.CostUSD = 0
	if strings.TrimSpace(p.session.Title) == "" {
		p.session.Title = fallbackTitle(p.firstUserText, p.session.SessionID)
	}
	p.stats.CancelledResults = p.session.cancelledResults
}

func (p *codexParser) toolCallForStep(stepID string) toolCallIdent {
	if p.session.toolCalls == nil {
		return toolCallIdent{}
	}
	return p.session.toolCalls[stepID]
}

func firstNonEmptyStr(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
