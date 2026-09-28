package state

import (
	"context"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

func (s *SessionStore) AppendMessageSequence(ctx context.Context, sessionID string, messages []llm.Message, model string, usageJSON string) error {
	return s.AppendMessageSequenceForRun(ctx, sessionID, "", messages, model, usageJSON, RunTiming{})
}

// AppendMessageSequenceForRun writes the part of a run's message list the
// transcript does not have yet.
//
// A run's list always starts with the history it loaded from this store, so the
// stored transcript is a prefix of it and the only question is where that prefix
// ends. That boundary is found from the tail: the last stored row the run still
// carries marks it, and everything the run produced after that row is new.
//
// It is deliberately not found from the head. Comparing the two sequences
// forward and stopping at the first difference made any single divergence
// re-append the whole remaining conversation, and divergences are normal: a
// partial writer (an approval gate, a cancelled turn) stores an assistant row
// before the tool calls it was still executing were known, a mid-turn
// compaction replaces the head of the list outright, a repair rewrites a row,
// and out-of-band writers (the plan-mode reminder) add rows the run never sees.
// Each of those duplicated the entire tail, and because the duplicate then sat
// in the stored transcript forever, every later turn re-appended the tail again
// - which is how a session ends up with the same user message and the same
// answer replayed several times over.
func (s *SessionStore) AppendMessageSequenceForRun(ctx context.Context, sessionID, runID string, messages []llm.Message, model string, usageJSON string, timing RunTiming) error {
	if s == nil || s.db == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	canonical := stripUnpersistedMessages(messages)
	if sessionID == "" || len(canonical) == 0 {
		return nil
	}
	// Reading the stored tail, planning the append, and writing it happen in
	// one IMMEDIATE transaction: two concurrent writers reconciling the same
	// run would otherwise both compute the same suffix and append it twice.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := s.listTranscriptStoredMessages(ctx, tx, sessionID, 5000)
	if err != nil {
		return err
	}
	plan := planTranscriptAppend(storedTranscriptMessages(rows), canonical)
	for _, completion := range plan.completions {
		text := completion.message.TextContent()
		if err := s.UpdateMessageParts(ctx, tx, completion.rowID, MessagePartsJSON(completion.message, text)); err != nil {
			return err
		}
	}
	for i := plan.start; i < len(canonical); i++ {
		if err := s.appendMessage(ctx, tx, sessionID, runID, canonical[i], model, usageJSON); err != nil {
			return err
		}
	}
	if timing.valid() {
		// The run's clock is stamped by the surface that measured it, once,
		// with the rows that carry the run id.
		if _, err := tx.ExecContext(ctx, `UPDATE fb_runs SET started_at_ms=?, finished_at_ms=?, worked_ms=?, updated_at=? WHERE id=?`,
			timing.StartedAt.UnixMilli(), timing.FinishedAt.UnixMilli(), timing.Worked.Milliseconds(), time.Now().Unix(), strings.TrimSpace(runID)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// AppendNewMessages writes messages the caller knows the transcript does not
// have: a replayed tool result, a mode reminder. They are not a continuation of
// the conversation the store holds - a reminder is the same text every time
// plan mode is entered, a replayed result answers a call stored earlier - so
// reconciling them against it would either drop them or attach them to the
// wrong row. The caller owns that decision, and this writes what it hands over.
func (s *SessionStore) AppendNewMessages(ctx context.Context, sessionID, runID string, messages []llm.Message, model string, usageJSON string) error {
	if s == nil || s.db == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, msg := range stripUnpersistedMessages(messages) {
		if err := s.appendMessage(ctx, tx, sessionID, runID, msg, model, usageJSON); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SessionStore) appendMessage(ctx context.Context, q dbtx, sessionID, runID string, msg llm.Message, model string, usageJSON string) error {
	role := strings.TrimSpace(msg.Role)
	if role == "" || role == llm.RoleSystem {
		return nil
	}
	// Ephemeral injections are rebuilt per request and must never be stored.
	if msg.Ephemeral {
		return nil
	}
	// A compaction handoff summary only ever belongs inside a checkpoint's
	// ReplacementHistory (persisted as a boundary part). Writing it as a
	// standalone transcript row is always the symptom of a compacted prefix
	// leaking into the append path; if that ever happens the summary would be
	// re-read by the projection and stack on top of the boundary's own summary.
	// Refuse it at the source so summaries can never accumulate.
	if IsCompactSummaryMessage(msg) {
		return nil
	}
	content := strings.TrimSpace(msg.TextContent())
	switch role {
	case llm.RoleUser, llm.RoleAssistant, llm.RoleTool, "reasoning":
	default:
		return nil
	}
	var exec messageExecTiming
	toolStepID := ""
	if role == llm.RoleTool {
		toolStepID = strings.TrimSpace(msg.ToolCallID)
		if toolStepID == "" {
			return nil
		}
		exec = toolMessageExecutionTiming(msg)
	}
	_, err := s.appendRowInTx(
		ctx,
		q,
		messageRow{
			SessionID:  sessionID,
			RunID:      strings.TrimSpace(runID),
			Role:       role,
			Visibility: messageVisible,
			Content:    content,
			PartsJSON:  MessagePartsJSON(msg, content),
			Model:      strings.TrimSpace(model),
			UsageJSON:  strings.TrimSpace(usageJSON),
			ToolStepID: toolStepID,
			CreatedAt:  time.Now().Unix(),
			Exec:       exec,
		},
	)
	return err
}

// toolMessageExecutionTiming reads a tool result's own execution window into
// the row's exec_* columns.
func toolMessageExecutionTiming(msg llm.Message) messageExecTiming {
	if msg.ToolExecutionTiming == nil || !msg.ToolExecutionTiming.Valid() {
		return messageExecTiming{}
	}
	return messageExecTiming{
		StartedAtMs:  msg.ToolExecutionTiming.StartedAt.UnixMilli(),
		FinishedAtMs: msg.ToolExecutionTiming.CompletedAt.UnixMilli(),
		DurationMs:   msg.ToolExecutionTiming.Duration.Milliseconds(),
	}
}

// storedTranscriptMessage is one persisted row paired with the row id needed to
// finish it in place.
type storedTranscriptMessage struct {
	rowID   int64
	message llm.Message
}

// storedTranscriptMessages parses the rows the model context is built from.
// System rows (compact-boundary markers and their detail log) are dropped
// because a run's message list carries no system message either; comparing the
// two sides on different alphabets is what an alignment must never do.
func storedTranscriptMessages(rows []storedMessage) []storedTranscriptMessage {
	out := make([]storedTranscriptMessage, 0, len(rows))
	for _, row := range rows {
		if strings.TrimSpace(row.Role) == llm.RoleSystem {
			continue
		}
		if _, isBoundary := ParseCompactBoundaryPart(row.PartsJSON); isBoundary {
			continue
		}
		message, ok := ParseMessage(row.Role, row.Content, row.PartsJSON)
		if !ok {
			continue
		}
		out = append(out, storedTranscriptMessage{rowID: row.ID, message: message})
	}
	return out
}

// transcriptAppendPlan is the reconciliation of one run's message list with the
// stored transcript: where the new messages start, and which already-stored
// rows the run has since completed.
type transcriptAppendPlan struct {
	start       int
	completions []transcriptCompletion
}

// transcriptCompletion is a stored row that holds an earlier, partial capture of
// a message the run has now finished. The row is rewritten in place instead of
// the finished message being appended, because appending it would show the same
// assistant text twice on replay and leave the model context with a tool result
// that answers no visible call.
type transcriptCompletion struct {
	rowID   int64
	message llm.Message
}

func planTranscriptAppend(stored []storedTranscriptMessage, canonical []llm.Message) transcriptAppendPlan {
	if len(stored) == 0 || len(canonical) == 0 {
		return transcriptAppendPlan{}
	}
	index := newCanonicalMessageIndex(canonical)
	storedIdx, canonicalIdx := -1, -1
	for i := len(stored) - 1; i >= 0; i-- {
		if pos, ok := index.lastMatch(stored[i].message); ok {
			storedIdx, canonicalIdx = i, pos
			break
		}
	}
	// The run carries none of the stored conversation: it is an anchor written
	// after a context clear, or the first turn of a session. All of it is new.
	if storedIdx < 0 {
		return transcriptAppendPlan{}
	}
	plan := transcriptAppendPlan{start: canonicalIdx + 1}
	for storedIdx >= 0 && canonicalIdx >= 0 {
		row := stored[storedIdx]
		message := canonical[canonicalIdx]
		if messagesAlign(row.message, message) {
			if row.rowID > 0 && assistantGainedToolCalls(row.message, message) {
				plan.completions = append(plan.completions, transcriptCompletion{rowID: row.rowID, message: message})
			}
			storedIdx--
			canonicalIdx--
			continue
		}
		// A row the run never had: an out-of-band writer put it between two of
		// the run's own messages. Step over it and keep aligning.
		if _, ok := index.lastMatch(row.message); !ok {
			storedIdx--
			continue
		}
		break
	}
	return plan
}

// assistantGainedToolCalls reports an assistant row stored before the calls it
// was already making were known - the shape every partial writer produces.
func assistantGainedToolCalls(stored, finished llm.Message) bool {
	return strings.TrimSpace(stored.Role) == llm.RoleAssistant &&
		len(stored.ToolCalls) == 0 && len(finished.ToolCalls) > 0
}

// messagesAlign reports whether a stored row and a message in a run's list are
// the same message, matched on what cannot change once written: the tool call a
// result answers, the id of the call an assistant message made, otherwise the
// text. Identity, not equality: a row stored by a partial writer is never an
// identical copy of the message the finished run reports.
func messagesAlign(stored, message llm.Message) bool {
	role := strings.TrimSpace(stored.Role)
	if role != strings.TrimSpace(message.Role) {
		return false
	}
	switch role {
	case llm.RoleTool:
		id := strings.TrimSpace(stored.ToolCallID)
		return id != "" && id == strings.TrimSpace(message.ToolCallID)
	case llm.RoleAssistant:
		if len(stored.ToolCalls) > 0 && len(message.ToolCalls) > 0 {
			return strings.TrimSpace(stored.ToolCalls[0].ID) == strings.TrimSpace(message.ToolCalls[0].ID)
		}
		text := messageText(stored)
		return text != "" && text == messageText(message)
	case llm.RoleUser:
		text := messageText(stored)
		if text != "" && text == messageText(message) {
			return true
		}
		return fuzzyUserMatch(stored, message)
	default:
		text := messageText(stored)
		return text != "" && text == messageText(message)
	}
}

func messageText(msg llm.Message) string {
	return strings.TrimSpace(llm.TextContent(msg.Parts...))
}

// canonicalMessageIndex finds a stored row's counterpart in a run's message
// list. Later occurrences win: the transcript is append-only, so when a message
// repeats, the boundary between what is stored and what is new is the last one.
type canonicalMessageIndex struct {
	byKey     map[string]int
	canonical []llm.Message
}

func newCanonicalMessageIndex(canonical []llm.Message) canonicalMessageIndex {
	index := canonicalMessageIndex{byKey: make(map[string]int, len(canonical)*2), canonical: canonical}
	for i, msg := range canonical {
		for _, key := range messageMatchKeys(msg) {
			index.byKey[key] = i
		}
	}
	return index
}

func (c canonicalMessageIndex) lastMatch(stored llm.Message) (int, bool) {
	for _, key := range messageMatchKeys(stored) {
		if i, ok := c.byKey[key]; ok && messagesAlign(stored, c.canonical[i]) {
			return i, true
		}
	}
	// A user row is stored as the user typed it while the run carries the
	// pipeline-enriched copy, so the two are equivalent without being equal.
	if strings.TrimSpace(stored.Role) != llm.RoleUser {
		return 0, false
	}
	for i := len(c.canonical) - 1; i >= 0; i-- {
		if fuzzyUserMatch(stored, c.canonical[i]) {
			return i, true
		}
	}
	return 0, false
}

// messageMatchKeys lists the identities a message can be recognised by, most
// specific first. An assistant message that made tool calls also answers to its
// text, which is how the partial row a gate stored - text, no calls yet - finds
// the finished message it became.
func messageMatchKeys(msg llm.Message) []string {
	role := strings.TrimSpace(msg.Role)
	text := messageText(msg)
	switch role {
	case llm.RoleTool:
		if id := strings.TrimSpace(msg.ToolCallID); id != "" {
			return []string{"tool:" + id}
		}
		return nil
	case llm.RoleAssistant:
		keys := make([]string, 0, 2)
		if len(msg.ToolCalls) > 0 {
			if id := strings.TrimSpace(msg.ToolCalls[0].ID); id != "" {
				keys = append(keys, "call:"+id)
			}
		}
		if text != "" {
			keys = append(keys, "assistant:"+text)
		}
		return keys
	default:
		if text == "" {
			return nil
		}
		return []string{role + ":" + text}
	}
}

// fuzzyUserMatch detects equivalent user messages when one copy also contains
// pipeline annotations such as attachment metadata. This prevents duplicate
// persistence when the stored and in-flight representations differ slightly.
func fuzzyUserMatch(a, b llm.Message) bool {
	if strings.TrimSpace(a.Role) != llm.RoleUser || strings.TrimSpace(b.Role) != llm.RoleUser {
		return false
	}
	aText := messageText(a)
	bText := messageText(b)
	if aText == "" || bText == "" {
		return false
	}
	if aText == bText {
		return true
	}
	return strings.Contains(bText, aText) || strings.Contains(aText, bText)
}

// stripUnpersistedMessages removes every message that never reaches the
// transcript: system rows and per-request ephemeral injections.
func stripUnpersistedMessages(messages []llm.Message) []llm.Message {
	if len(messages) == 0 {
		return nil
	}
	out := make([]llm.Message, 0, len(messages))
	for _, msg := range messages {
		if strings.TrimSpace(msg.Role) == llm.RoleSystem || msg.Ephemeral {
			continue
		}
		out = append(out, msg)
	}
	return out
}
