package migrate

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// parsedSession is one source transcript mapped onto forebrain rows. It carries
// everything sessions.go needs to write the conversation in one transaction.
type parsedSession struct {
	SessionID string
	Rows      []parsedRow
	Aggregates
	// cancelledResults counts the synthetic cancelled tool answers the
	// invariant pass appended (§1.2); the report surfaces it so the number is
	// never silently invented.
	cancelledResults int
	// toolCalls indexes tool_use ids to their source identity (name and
	// arguments) so a result row can carry the same tool metadata its call
	// row had.
	toolCalls map[string]toolCallIdent
	// initialWindowID is the session's initial window id: a source truth when
	// the transcript carries one (Codex), synthesized by applyWindows when it
	// has at least one boundary and no truth (Claude).
	initialWindowID string
	// boundaries indexes into Rows for every compact boundary, in order.
	boundaries []int
	// byUUID indexes parsed rows by their source uuid for preserved-segment
	// assembly.
	byUUID map[string]int
	// stats carries the source-specific drop/synthesis counters the report
	// states (Codex: encrypted reasoning, developer rows, cancelled answers).
	stats codexSourceStats
}

// Aggregates are the fb_sessions summary columns derived while parsing.
type Aggregates struct {
	Title         string
	FirstSeen     int64
	LastSeen      int64
	Cwd           string
	GitBranch     string
	PromptTok     int64
	CompletionTok int64
	CostUSD       float64
}

// parsedRow is one mapped transcript row before persistence. PartsJSON is
// pre-rendered with state's encoders so replay and model-context rebuild see
// exactly the shapes native sessions write.
type parsedRow struct {
	Role         string // user | assistant | reasoning | tool | system(boundary)
	Content      string
	MessageID    string // source uuid
	PartsJSON    string
	Model        string
	UsageJSON    string
	ToolStepID   string
	ToolMetaJSON string
	CreatedAt    int64
	// SourceUUID preserves the row's source identity for preserved-segment
	// assembly; identical to MessageID.
	SourceUUID string
	// IsBoundary marks the compact-boundary row; Rows[i].boundary carries the
	// decoded part when set.
	IsBoundary bool
	boundary   state.CompactBoundaryPart
	// toolIdent carries the call's source identity so the invariant pass can
	// rebuild a cancelled answer with the same tool name and arguments.
	toolIdent toolCallIdent
}

// claudeRecord is the union of record shapes a transcript line may take.
// Unknown types are skipped by the parser; unknown fields are ignored.
type claudeRecord struct {
	Type        string `json:"type"`
	Subtype     string `json:"subtype"`
	UUID        string `json:"uuid"`
	Timestamp   string `json:"timestamp"`
	Cwd         string `json:"cwd"`
	GitBranch   string `json:"gitBranch"`
	IsSidechain bool   `json:"isSidechain"`
	Message     struct {
		Role    string          `json:"role"`
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
		Usage   *claudeUsage    `json:"usage"`
	} `json:"message"`
	IsCompactSummary bool                       `json:"isCompactSummary"`
	IsMeta           bool                       `json:"isMeta"`
	ToolUseResult    *claudeToolUseResult       `json:"toolUseResult"`
	CompactMetadata  *claudeCompactMetadata     `json:"compactMetadata"`
	AITitle          string                     `json:"aiTitle"`
	TotalCostUSD     *float64                   `json:"totalCostUSD"`
	Raw              map[string]json.RawMessage `json:"-"`
	AgentName        string                     `json:"agentName"`
	DurationMs       int64                      `json:"durationMs"`
}

type claudeUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
}

type claudeToolUseResult struct {
	Stdout              string `json:"stdout"`
	Stderr              string `json:"stderr"`
	Interrupted         bool   `json:"interrupted"`
	IsImage             bool   `json:"isImage"`
	NoOutputExpected    bool   `json:"noOutputExpected"`
	PersistedOutputPath string `json:"persistedOutputPath"`
	PersistedOutputSize int64  `json:"persistedOutputSize"`
}

type claudeCompactMetadata struct {
	Trigger          string `json:"trigger"`
	PreTokens        int64  `json:"preTokens"`
	PostTokens       int64  `json:"postTokens"`
	DurationMs       int64  `json:"durationMs"`
	PreservedSegment *struct {
		HeadUUID   string `json:"headUuid"`
		AnchorUUID string `json:"anchorUuid"`
		TailUUID   string `json:"tailUuid"`
	} `json:"preservedSegment"`
}

// contentBlock is one entry of message.content when it is an array.
type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
	Source    *struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
	} `json:"source"`
}

// parseSessionFile streams one transcript jsonl into a parsedSession. Rows
// follow file line order, which is the order forebrain replays by row id.
// Bad lines are counted and skipped rather than failing the session.
func parseSessionFile(ctx context.Context, path, sessionID string, onBadLine func()) (*parsedSession, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	parsed := &parsedSession{SessionID: sessionID, byUUID: map[string]int{}}
	parser := &transcriptParser{session: parsed, sidecarRoot: sidecarRootFor(path)}
	reader := bufio.NewReaderSize(file, 1<<20)
	lastTs := int64(0)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			rec, decodeErr := decodeRecord(line)
			if decodeErr != nil {
				if onBadLine != nil {
					onBadLine()
				}
			} else {
				ts := parser.record(rec, lastTs)
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
	parser.session.finalizeInvariants()
	parsed.rebuildIndexes()
	parsed.applyWindows()
	return parsed, nil
}

// applyWindows stamps the boundary parts with their window chain. Values the
// source already carries (Codex's compacted records ship the full chain) are
// source truth and pass through untouched; only missing ones are synthesized
// — numbers from 1, deterministic window ids, the first window id shared with
// the session's initial_window_id, and each boundary linking to its
// predecessor — once, deterministically, rather than at write time, keeping
// the persisted parts self-consistent with the session row.
func (s *parsedSession) applyWindows() {
	if len(s.boundaries) == 0 {
		return
	}
	plan := planWindows(s.SessionID, s.boundaries, s.Rows)
	if s.initialWindowID == "" {
		s.initialWindowID = plan.Initial
	}
	for _, entry := range plan.Windows {
		part := s.Rows[entry.Row].boundary
		if part.WindowNumber == 0 {
			part.WindowNumber = entry.Number
		}
		if strings.TrimSpace(part.FirstWindowID) == "" {
			part.FirstWindowID = s.initialWindowID
		}
		if strings.TrimSpace(part.PreviousWindowID) == "" {
			part.PreviousWindowID = entry.Previous
		}
		if strings.TrimSpace(part.WindowID) == "" {
			part.WindowID = entry.WindowID
		}
		s.Rows[entry.Row].boundary = part
		s.Rows[entry.Row].PartsJSON = state.PartsJSONWithCompactPart(
			s.Rows[entry.Row].Content, state.EncodeCompactBoundaryPart(part))
	}
}

// sidecarRootFor derives the <projects>/<encoded>/<sessionId> directory a
// transcript's tool-results sidecars live under.
func sidecarRootFor(path string) string {
	return filepath.Dir(strings.TrimSpace(path))
}

func decodeRecord(line []byte) (*claudeRecord, error) {
	var rec claudeRecord
	if err := json.Unmarshal(line, &rec); err != nil {
		return nil, err
	}
	if strings.TrimSpace(rec.Type) == "" {
		return nil, fmt.Errorf("record has no type")
	}
	rec.Raw = nil
	return &rec, nil
}

// parseTimestamp converts the source RFC3339 stamp, returning 0 when absent
// or unparsable.
func parseTimestamp(value string) int64 {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if ts, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return ts.Unix()
	}
	if ts, err := time.Parse(time.RFC3339, value); err == nil {
		return ts.Unix()
	}
	return 0
}

// transcriptParser holds the state one pass needs: the rows produced so far,
// the pending boundary waiting for its summary row, and where sidecar files
// resolve.
type transcriptParser struct {
	session     *parsedSession
	sidecarRoot string
	// pendingBoundary holds the boundary row whose summary user row has not
	// arrived yet; source files always place it immediately after.
	pendingBoundary *pendingBoundary
	// firstUserText feeds the fallback title.
	firstUserText string
}

type pendingBoundary struct {
	row      parsedRow
	meta     *claudeCompactMetadata
	recordTs int64
}

// record folds one decoded source record into rows. It returns the record's
// unix timestamp (after monotonic fix-up) so the caller can carry the clock
// forward for records without one.
func (p *transcriptParser) record(rec *claudeRecord, lastTs int64) int64 {
	ts := parseTimestamp(rec.Timestamp)
	if ts <= 0 {
		ts = lastTs + 1
	}
	p.observeAggregate(rec, ts)
	switch strings.TrimSpace(rec.Type) {
	case "user":
		p.userRecord(rec, ts)
	case "assistant":
		p.assistantRecord(rec, ts)
	case "system":
		p.systemRecord(rec, ts)
	case "ai-title":
		if title := strings.TrimSpace(rec.AITitle); title != "" {
			p.session.Title = title
		}
	case "cost-state":
		p.costRecord(rec)
	default:
		// mode, permission-mode, attachment, file-history, queue-operation,
		// last-prompt, agent-name, frame-link, atis-latch, bridge-session:
		// runtime state with no transcript meaning.
	}
	return ts
}

func (p *transcriptParser) observeAggregate(rec *claudeRecord, ts int64) {
	agg := &p.session.Aggregates
	if agg.FirstSeen == 0 || (ts > 0 && ts < agg.FirstSeen) {
		agg.FirstSeen = ts
	}
	if ts > agg.LastSeen {
		agg.LastSeen = ts
	}
	if cwd := strings.TrimSpace(rec.Cwd); cwd != "" {
		agg.Cwd = cwd
	}
	if branch := strings.TrimSpace(rec.GitBranch); branch != "" {
		agg.GitBranch = branch
	}
	if usage := rec.Message.Usage; usage != nil {
		agg.PromptTok += usage.InputTokens + usage.CacheCreationInputTokens + usage.CacheReadInputTokens
		agg.CompletionTok += usage.OutputTokens
	}
}

func (p *transcriptParser) costRecord(rec *claudeRecord) {
	if rec.TotalCostUSD != nil {
		p.session.Aggregates.CostUSD = *rec.TotalCostUSD
	}
}

// userRecord maps a source user row. A compact-summary row is folded into the
// pending boundary instead of becoming its own row (B1): a standalone summary
// row would be dropped by the model-context rebuild and stack summaries.
func (p *transcriptParser) userRecord(rec *claudeRecord, ts int64) {
	if rec.IsCompactSummary {
		text := plainContent(rec.Message.Content)
		if p.pendingBoundary != nil {
			p.flushBoundary(text)
		}
		return
	}
	if isToolResultContent(rec.Message.Content) {
		p.toolResultRow(rec, ts)
		return
	}
	msg := llm.UserMessage()
	msg.IsMeta = rec.IsMeta
	blocks := decodeBlocks(rec.Message.Content)
	if len(blocks) == 0 {
		// String-shaped content is the common user row.
		if text := plainContent(rec.Message.Content); strings.TrimSpace(text) != "" {
			msg.Parts = append(msg.Parts, llm.Text(text))
		}
	}
	for _, block := range blocks {
		switch block.Type {
		case "text":
			msg.Parts = append(msg.Parts, llm.Text(block.Text))
		case "image":
			if block.Source != nil && strings.EqualFold(block.Source.Type, "base64") {
				msg.Parts = append(msg.Parts, llm.ImageBase64(block.Source.MediaType, block.Source.Data))
			}
		}
	}
	content := msg.TextContent()
	if p.firstUserText == "" && strings.TrimSpace(content) != "" {
		p.firstUserText = content
	}
	row := parsedRow{
		Role: llm.RoleUser, Content: content, MessageID: rec.UUID, SourceUUID: rec.UUID,
		PartsJSON: state.MessagePartsJSON(msg, content), CreatedAt: ts,
	}
	p.append(row)
}

// toolResultRow maps the tool_result blocks of a user row into tool rows.
func (p *transcriptParser) toolResultRow(rec *claudeRecord, ts int64) {
	for _, block := range decodeBlocks(rec.Message.Content) {
		if block.Type != "tool_result" {
			continue
		}
		body := blockText(block, p.sidecarRoot)
		display := body
		if result := rec.ToolUseResult; result != nil {
			if combined := toolUseResultBody(result, body); combined != "" {
				display = combined
			}
		}
		stepID := strings.TrimSpace(block.ToolUseID)
		if stepID == "" {
			continue
		}
		ident := p.toolCallForStep(stepID)
		meta, _ := buildToolMeta(ident.Name, ident.Arguments, "")
		msg := llm.ToolResultMessage(stepID, llm.Text(body))
		msg.ToolDisplay = &llm.ToolDisplayState{
			Body: display, Summary: firstLine(display), ToolMetaJSON: marshalToolMeta(meta),
		}
		row := parsedRow{
			Role: llm.RoleTool, Content: body, MessageID: rec.UUID, SourceUUID: rec.UUID,
			PartsJSON:    state.MessagePartsJSON(msg, body),
			ToolStepID:   stepID,
			ToolMetaJSON: marshalToolMeta(meta),
			CreatedAt:    ts,
		}
		p.append(row)
	}
}

// toolCallIdent is one tool_use's source identity.
type toolCallIdent struct {
	Name      string
	Arguments json.RawMessage
}

// toolCallForStep looks up a tool_use's source identity.
func (p *transcriptParser) toolCallForStep(stepID string) toolCallIdent {
	if p.session.toolCalls == nil {
		return toolCallIdent{}
	}
	return p.session.toolCalls[stepID]
}

func (p *transcriptParser) assistantRecord(rec *claudeRecord, ts int64) {
	blocks := decodeBlocks(rec.Message.Content)
	if len(blocks) == 0 {
		// A string-content assistant row is plain text.
		if text := plainContent(rec.Message.Content); strings.TrimSpace(text) != "" {
			msg := llm.AssistantMessage([]llm.ContentPart{llm.Text(text)})
			p.append(parsedRow{
				Role: llm.RoleAssistant, Content: text, MessageID: rec.UUID, SourceUUID: rec.UUID,
				PartsJSON: state.MessagePartsJSON(msg, text), Model: rec.Message.Model,
				UsageJSON: usageJSON(rec.Message.Usage), CreatedAt: ts,
			})
		}
		return
	}
	for _, block := range blocks {
		switch block.Type {
		case "thinking":
			text := strings.TrimSpace(block.Thinking)
			if text == "" {
				continue
			}
			// The signature is dropped: it authenticates the source's model
			// session, not this one.
			msg := llm.AssistantMessage([]llm.ContentPart{llm.Text(text)})
			p.append(parsedRow{
				Role: "reasoning", Content: text, MessageID: rec.UUID, SourceUUID: rec.UUID,
				PartsJSON: state.MessagePartsJSON(msg, text), Model: rec.Message.Model,
				UsageJSON: usageJSON(rec.Message.Usage), CreatedAt: ts,
			})
		case "text":
			text := strings.TrimSpace(block.Text)
			if text == "" {
				continue
			}
			msg := llm.AssistantMessage([]llm.ContentPart{llm.Text(text)})
			p.append(parsedRow{
				Role: llm.RoleAssistant, Content: text, MessageID: rec.UUID, SourceUUID: rec.UUID,
				PartsJSON: state.MessagePartsJSON(msg, text), Model: rec.Message.Model,
				UsageJSON: usageJSON(rec.Message.Usage), CreatedAt: ts,
			})
		case "tool_use":
			p.toolUseRow(rec, block, ts)
		}
	}
}

func (p *transcriptParser) toolUseRow(rec *claudeRecord, block contentBlock, ts int64) {
	name := strings.TrimSpace(block.Name)
	stepID := strings.TrimSpace(block.ID)
	if name == "" || stepID == "" {
		return
	}
	if p.session.toolCalls == nil {
		p.session.toolCalls = map[string]toolCallIdent{}
	}
	p.session.toolCalls[stepID] = toolCallIdent{Name: name, Arguments: block.Input}
	meta, canonicalArgs := buildToolMeta(name, block.Input, "")
	msg := llm.AssistantMessage(nil, llm.ToolCall{
		ID: stepID, Type: llm.ToolTypeFunction,
		Function: llm.FunctionCall{Name: mapToolName(name), Arguments: canonicalArgs},
	})
	p.append(parsedRow{
		Role: llm.RoleAssistant, Content: "", MessageID: rec.UUID, SourceUUID: rec.UUID,
		PartsJSON:    state.MessagePartsJSON(msg, ""),
		Model:        rec.Message.Model,
		UsageJSON:    usageJSON(rec.Message.Usage),
		ToolStepID:   stepID,
		ToolMetaJSON: marshalToolMeta(meta),
		CreatedAt:    ts,
		toolIdent:    toolCallIdent{Name: name, Arguments: block.Input},
	})
}

// systemRecord maps compact boundaries; every other system subtype is
// dropped. A boundary is held until the summary row that follows it, because
// the summary belongs inside the boundary's ReplacementHistory.
func (p *transcriptParser) systemRecord(rec *claudeRecord, ts int64) {
	if !strings.EqualFold(strings.TrimSpace(rec.Subtype), "compact_boundary") {
		return
	}
	if p.pendingBoundary != nil {
		// A boundary with no summary row behind it still becomes a boundary.
		p.flushBoundary("")
	}
	var meta *claudeCompactMetadata
	if rec.CompactMetadata != nil {
		meta = rec.CompactMetadata
	}
	detail := boundaryDetail(rec)
	p.pendingBoundary = &pendingBoundary{
		row: parsedRow{
			Role: llm.RoleSystem, Content: detail, MessageID: rec.UUID, SourceUUID: rec.UUID,
			PartsJSON: state.ContentPartsJSON(nil, detail), CreatedAt: ts, IsBoundary: true,
		},
		meta:     meta,
		recordTs: ts,
	}
}

// flushBoundary assembles the boundary row's ReplacementHistory: the summary
// message the source wrote next, then the preserved segment its metadata
// 圈定 (head..tail inclusive), then finishes the row.
func (p *transcriptParser) flushBoundary(summaryText string) {
	pending := p.pendingBoundary
	p.pendingBoundary = nil
	if pending == nil {
		return
	}
	history := []llm.Message{}
	if strings.TrimSpace(summaryText) != "" {
		history = append(history, llm.UserMessage(llm.Text(state.CompactSummaryPrefix+"\n\n"+strings.TrimSpace(summaryText))))
	}
	if pending.meta != nil && pending.meta.PreservedSegment != nil {
		history = append(history, p.preservedMessages(pending.meta.PreservedSegment.HeadUUID, pending.meta.PreservedSegment.TailUUID)...)
	}
	pending.row.boundary = state.CompactBoundaryPart{
		Trigger:            strings.TrimSpace(triggerOf(pending)),
		Strategy:           "migrated",
		SummarySource:      "migrated",
		ReplacementHistory: history,
	}
	pending.row.PartsJSON = state.PartsJSONWithCompactPart(pending.row.Content, state.EncodeCompactBoundaryPart(pending.row.boundary))
	p.append(pending.row)
}

func triggerOf(pending *pendingBoundary) string {
	if pending.meta == nil {
		return ""
	}
	return pending.meta.Trigger
}

// preservedMessages collects the mapped rows between headUUID and tailUUID
// (inclusive, in row order) as llm messages for the boundary's replacement
// history. System rows and other boundaries never appear: the model context
// rebuild drops them, so storing them would only bloat the checkpoint.
func (p *transcriptParser) preservedMessages(headUUID, tailUUID string) []llm.Message {
	rows := p.session.Rows
	start, end := -1, -1
	for i := range rows {
		if headUUID != "" && rows[i].SourceUUID == headUUID {
			start = i
		}
		if tailUUID != "" && rows[i].SourceUUID == tailUUID {
			end = i
		}
	}
	if start < 0 || end < 0 || end < start {
		return nil
	}
	out := make([]llm.Message, 0, end-start+1)
	for i := start; i <= end; i++ {
		row := rows[i]
		if row.IsBoundary || row.Role == llm.RoleSystem {
			continue
		}
		role := row.Role
		// A reasoning row replays as an assistant message inside the
		// checkpoint: the model-context rebuild parses user/assistant/tool
		// rows, and native sessions carry thinking to the model as
		// assistant text, so the replacement history does the same.
		if role == "reasoning" {
			role = llm.RoleAssistant
		}
		msg, ok := state.ParseMessage(role, row.Content, row.PartsJSON)
		if !ok {
			continue
		}
		out = append(out, msg)
	}
	return out
}

// finish closes a trailing boundary that never met its summary row.
func (p *transcriptParser) finish() {
	if p.pendingBoundary != nil {
		p.flushBoundary("")
	}
	if strings.TrimSpace(p.session.Title) == "" {
		p.session.Title = fallbackTitle(p.firstUserText, p.session.SessionID)
	}
}

// --- Shared resume invariants (§1.2, both sources) ---
//
// "Imported so it can be replayed" and "imported so it can be continued" are
// different claims. Replay only reads rows back; continuing rebuilds them into
// a provider request, whose validators reject: an assistant tool_call nothing
// answers, a tool result with no call ahead of it, or a context that does not
// open on a user message. These repairs run over the parsed rows and every
// boundary's replacement history, for Claude and Codex alike, so neither
// source can import a transcript that its own provider would refuse.

// migratedCancelledNote is the tool result written for a call the source
// never answered — the same sentence live sessions write (turn.CancelledTool-
// CallNote), repeated here because migrate may not import pkg/turn.
const migratedCancelledNote = "The user canceled this call before it ran."

// cancelledStatus matches the one-l spelling the renderer stamps on a live
// cancelled card, so a replayed card and a live one agree.
const cancelledStatus = "canceled"

// finalizeInvariants enforces the four resume invariants on one parsed
// session: first-row-user per segment, no dangling tool results, every call
// answered (orphans get a cancelled result appended — the call row itself is
// never deleted), and no role the model-context rebuild would drop as a
// mid-context row. Boundary rows keep their position; their replacement
// histories are repaired with the same rules.
func (s *parsedSession) finalizeInvariants() {
	var out []parsedRow
	var segment []parsedRow
	seenSegment := false
	flush := func() {
		repaired, added := repairRowSegment(segment, !seenSegment)
		s.cancelledResults += added
		out = append(out, repaired...)
		segment = nil
		seenSegment = true
	}
	for _, row := range s.Rows {
		if row.IsBoundary {
			flush()
			out = append(out, row)
			continue
		}
		segment = append(segment, row)
	}
	flush()
	s.Rows = out
	for i := range s.Rows {
		if !s.Rows[i].IsBoundary {
			continue
		}
		part := s.Rows[i].boundary
		repaired, msgAdded := repairMessageSegment(part.ReplacementHistory)
		if msgAdded > 0 {
			s.cancelledResults += msgAdded
		}
		part.ReplacementHistory = repaired
		s.Rows[i].boundary = part
		s.Rows[i].PartsJSON = state.PartsJSONWithCompactPart(
			s.Rows[i].Content, state.EncodeCompactBoundaryPart(part))
	}
}

// repairRowSegment applies the invariants to one boundary-free run of rows
// and reports how many cancelled answers it appended. trimLeading is the
// session's first segment only: §1.2 rule 3 ("the first stored row must be a
// real user message") speaks of the transcript's opening after the injected
// preamble — a boundary's replacement history already opens the projected
// context, so a post-boundary segment may legitimately resume mid-turn.
func repairRowSegment(rows []parsedRow, trimLeading bool) ([]parsedRow, int) {
	if len(rows) == 0 {
		return rows, 0
	}
	// Invariant 3: the session opens on a real user message. Boundary and
	// system rows never start a segment here (the caller splits on them).
	if trimLeading {
		first := 0
		for first < len(rows) && rows[first].Role != llm.RoleUser {
			first++
		}
		rows = rows[first:]
	}
	if len(rows) == 0 {
		return nil, 0
	}
	// Collect the calls this segment actually issued before dropping
	// anything: a result may trail several calls in one assistant batch.
	calls := make(map[string]toolCallIdent, len(rows))
	var callOrder []string
	for _, row := range rows {
		if row.Role == llm.RoleAssistant && strings.TrimSpace(row.ToolStepID) != "" {
			id := strings.TrimSpace(row.ToolStepID)
			if _, seen := calls[id]; !seen {
				callOrder = append(callOrder, id)
			}
			calls[id] = row.toolIdent
		}
	}
	answered := make(map[string]bool, len(rows))
	for _, row := range rows {
		if row.Role == llm.RoleTool {
			answered[strings.TrimSpace(row.ToolStepID)] = true
		}
	}
	out := make([]parsedRow, 0, len(rows)+len(callOrder))
	for _, row := range rows {
		// A tool result whose call is not in this segment answers nothing a
		// provider can see; keeping it would be an invalid context.
		if row.Role == llm.RoleTool && !answeredCallsContains(calls, row.ToolStepID) {
			continue
		}
		out = append(out, row)
	}
	// Invariant 1: answer every unanswered call with a cancelled result. The
	// call row stays — deleting it would tell the model it never made a call
	// it watched itself make.
	var lastTs int64
	for _, row := range out {
		if row.CreatedAt > lastTs {
			lastTs = row.CreatedAt
		}
	}
	added := 0
	for _, id := range callOrder {
		if answered[id] {
			continue
		}
		out = append(out, cancelledResultRow(id, calls[id], lastTs))
		added++
	}
	return out, added
}

func answeredCallsContains(calls map[string]toolCallIdent, stepID string) bool {
	_, ok := calls[strings.TrimSpace(stepID)]
	return ok
}

// cancelledResultRow builds the synthetic answer for one never-run call,
// mirroring what a live stopped run appends (turn.cancelledToolAnswer): the
// model-facing sentence, a display half with no body, and a canceled badge.
func cancelledResultRow(stepID string, ident toolCallIdent, ts int64) parsedRow {
	stepID = strings.TrimSpace(stepID)
	meta := tool.ToolMeta{ToolName: mapToolName(ident.Name), Status: cancelledStatus}
	if args := strings.TrimSpace(string(ident.Arguments)); args != "" && args != "null" {
		var parsed map[string]any
		if json.Unmarshal([]byte(args), &parsed) == nil && parsed != nil {
			meta.Input = parsed
		}
	}
	body := migratedCancelledNote
	msg := llm.ToolResultMessage(stepID, llm.Text(body))
	msg.ToolDisplay = &llm.ToolDisplayState{
		Summary:      firstLine(body),
		ToolMetaJSON: marshalToolMeta(meta),
	}
	return parsedRow{
		Role: llm.RoleTool, Content: body, MessageID: "cancelled-" + stepID, SourceUUID: "cancelled-" + stepID,
		PartsJSON:    state.MessagePartsJSON(msg, body),
		ToolStepID:   stepID,
		ToolMetaJSON: marshalToolMeta(meta),
		CreatedAt:    ts,
	}
}

// repairMessageSegment applies the same invariants to a boundary's
// replacement history. Compaction items (opaque server state) are never
// dropped and never start the leading-user trim: they must replay verbatim.
func repairMessageSegment(msgs []llm.Message) ([]llm.Message, int) {
	first := 0
	for first < len(msgs) {
		role := strings.TrimSpace(msgs[first].Role)
		if msgs[first].Compaction != nil || role == llm.RoleUser {
			break
		}
		if role == llm.RoleAssistant || role == llm.RoleTool || role == "reasoning" {
			first++
			continue
		}
		break
	}
	msgs = msgs[first:]
	calls := make(map[string]llm.ToolCall, len(msgs))
	var callOrder []string
	answered := make(map[string]bool, len(msgs))
	for _, msg := range msgs {
		switch strings.TrimSpace(msg.Role) {
		case llm.RoleAssistant:
			for _, call := range msg.ToolCalls {
				id := strings.TrimSpace(call.ID)
				if id == "" {
					continue
				}
				if _, seen := calls[id]; !seen {
					callOrder = append(callOrder, id)
				}
				calls[id] = call
			}
		case llm.RoleTool:
			answered[strings.TrimSpace(msg.ToolCallID)] = true
		}
	}
	out := make([]llm.Message, 0, len(msgs)+len(callOrder))
	for _, msg := range msgs {
		if strings.TrimSpace(msg.Role) == llm.RoleTool {
			if _, ok := calls[strings.TrimSpace(msg.ToolCallID)]; !ok {
				continue
			}
		}
		out = append(out, msg)
	}
	added := 0
	for _, id := range callOrder {
		if answered[id] {
			continue
		}
		out = append(out, cancelledResultMessage(calls[id]))
		added++
	}
	return out, added
}

// cancelledResultMessage is the replacement-history form of a cancelled
// answer: parts render it, and the model context rebuild parses it back as a
// tool message answering the orphan call.
func cancelledResultMessage(call llm.ToolCall) llm.Message {
	name := strings.TrimSpace(call.Function.Name)
	var input map[string]any
	if raw := strings.TrimSpace(call.Function.Arguments); raw != "" {
		_ = json.Unmarshal([]byte(raw), &input)
	}
	meta := tool.ToolMeta{ToolName: name, Status: cancelledStatus, Input: input}
	msg := llm.ToolResultMessage(strings.TrimSpace(call.ID), llm.Text(migratedCancelledNote))
	msg.ToolDisplay = &llm.ToolDisplayState{
		Summary:      firstLine(migratedCancelledNote),
		ToolMetaJSON: marshalToolMeta(meta),
	}
	return msg
}

func fallbackTitle(text, sessionID string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return sessionID
	}
	first := firstLine(text)
	runes := []rune(first)
	if len(runes) > 40 {
		first = string(runes[:40])
	}
	return first
}

func (p *transcriptParser) append(row parsedRow) {
	p.session.Rows = append(p.session.Rows, row)
}

func (s *parsedSession) rebuildIndexes() {
	s.byUUID = make(map[string]int, len(s.Rows))
	s.boundaries = nil
	for i := range s.Rows {
		if id := s.Rows[i].SourceUUID; id != "" {
			if _, exists := s.byUUID[id]; !exists {
				s.byUUID[id] = i
			}
		}
		if s.Rows[i].IsBoundary {
			s.boundaries = append(s.boundaries, i)
		}
	}
}

// windowIDs derives the deterministic window chain a session's boundaries
// form. The ids are pure functions of the session and boundary uuids, so a
// re-import that somehow re-derived them would produce the same chain.
type windowPlan struct {
	Initial string
	Windows []windowEntry
}

type windowEntry struct {
	Row      int
	WindowID string
	Previous string
	Number   uint64
}

func planWindows(sessionID string, boundaries []int, rows []parsedRow) windowPlan {
	plan := windowPlan{Initial: stableWindowID(sessionID, "initial")}
	for n, rowIdx := range boundaries {
		boundaryID := rows[rowIdx].SourceUUID
		plan.Windows = append(plan.Windows, windowEntry{
			Row:      rowIdx,
			WindowID: stableWindowID(sessionID, boundaryID),
			Previous: "",
			Number:   uint64(n + 1),
		})
		if n > 0 {
			plan.Windows[n].Previous = plan.Windows[n-1].WindowID
		}
	}
	return plan
}

// boundaryDetail renders the boundary row's visible body: what the source
// recorded about the compaction, without inventing part types for it.
func boundaryDetail(rec *claudeRecord) string {
	parts := []string{"compact boundary (migrated)"}
	if rec.CompactMetadata != nil {
		if trigger := strings.TrimSpace(rec.CompactMetadata.Trigger); trigger != "" {
			parts = append(parts, "trigger "+trigger)
		}
		if rec.CompactMetadata.PreTokens > 0 || rec.CompactMetadata.PostTokens > 0 {
			parts = append(parts, fmt.Sprintf("tokens %d → %d", rec.CompactMetadata.PreTokens, rec.CompactMetadata.PostTokens))
		}
		if rec.CompactMetadata.DurationMs > 0 {
			parts = append(parts, "duration "+(time.Duration(rec.CompactMetadata.DurationMs)*time.Millisecond).String())
		}
	}
	return strings.Join(parts, " · ")
}

// decodeBlocks returns the content blocks of a raw content value, or nil when
// the content is a plain string or an empty array.
func decodeBlocks(raw json.RawMessage) []contentBlock {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil || blocks == nil {
		return nil
	}
	return blocks
}

// isToolResultContent reports whether any block is a tool_result.
func isToolResultContent(raw json.RawMessage) bool {
	for _, block := range decodeBlocks(raw) {
		if block.Type == "tool_result" {
			return true
		}
	}
	return false
}

// plainContent returns the text of a string-shaped content value.
func plainContent(raw json.RawMessage) string {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	blocks := decodeBlocks(raw)
	var out []string
	for _, block := range blocks {
		if block.Type == "text" {
			out = append(out, block.Text)
		}
	}
	return strings.Join(out, "\n")
}

// blockText renders a tool_result block's body, inlining the sidecar file the
// source spilled large outputs into so the result is not lost.
func blockText(block contentBlock, sidecarRoot string) string {
	text := plainOrListText(block.Content)
	if marker := sidecarPathIn(text, sidecarRoot); marker != "" {
		if body, err := os.ReadFile(marker); err == nil {
			return string(body)
		}
	}
	if text == "" && strings.TrimSpace(block.ToolUseID) != "" {
		text = "(no output)"
	}
	return text
}

func plainOrListText(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var out []string
		for _, block := range blocks {
			if block.Type == "text" {
				out = append(out, block.Text)
			}
		}
		return strings.Join(out, "\n")
	}
	return ""
}

// sidecarPathIn extracts a tool-results sidecar path mentioned in a result
// body, resolving it against the transcript's session directory.
func sidecarPathIn(text, sidecarRoot string) string {
	const marker = "tool-results/"
	idx := strings.Index(text, marker)
	if idx < 0 {
		return ""
	}
	rest := text[idx+len(marker):]
	end := len(rest)
	if stop := strings.IndexAny(rest, " \t\r\n"); stop >= 0 {
		end = stop
	}
	name := strings.Trim(rest[:end], "`\"'")
	if name == "" || strings.ContainsAny(name, "/\\") {
		return ""
	}
	return filepath.Join(sidecarRoot, "tool-results", name)
}

// toolUseResultBody prefers the structured toolUseResult fields for the
// display card, falling back to the block body.
func toolUseResultBody(result *claudeToolUseResult, fallback string) string {
	var parts []string
	if strings.TrimSpace(result.Stdout) != "" {
		parts = append(parts, result.Stdout)
	}
	if strings.TrimSpace(result.Stderr) != "" {
		parts = append(parts, result.Stderr)
	}
	if result.Interrupted {
		parts = append(parts, "(interrupted)")
	}
	if result.NoOutputExpected && len(parts) == 0 {
		parts = append(parts, "(no output expected)")
	}
	if len(parts) > 0 {
		return strings.Join(parts, "\n")
	}
	if path := strings.TrimSpace(result.PersistedOutputPath); path != "" {
		if body, err := os.ReadFile(path); err == nil {
			return string(body)
		}
	}
	return fallback
}

func usageJSON(usage *claudeUsage) string {
	if usage == nil {
		return ""
	}
	if usage.InputTokens == 0 && usage.OutputTokens == 0 && usage.CacheCreationInputTokens == 0 && usage.CacheReadInputTokens == 0 {
		return ""
	}
	b, err := json.Marshal(map[string]int64{
		"input_tokens":                usage.InputTokens,
		"output_tokens":               usage.OutputTokens,
		"cache_creation_input_tokens": usage.CacheCreationInputTokens,
		"cache_read_input_tokens":     usage.CacheReadInputTokens,
	})
	if err != nil {
		return ""
	}
	return string(b)
}

// stableWindowID derives a window id from the session and boundary identity.
func stableWindowID(sessionID, boundaryID string) string {
	return "mw-" + shortDigest(sessionID+"\x00"+boundaryID)
}

func shortDigest(input string) string {
	sum := sha256.Sum256([]byte(input))
	return hex.EncodeToString(sum[:])[:16]
}
