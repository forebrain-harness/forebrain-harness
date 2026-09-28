package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// SessionEventSchemaVersion is the shape of one stored event's payload as the
// wire carries it. It lives here as the single constant the surfaces stamp
// when they hand an event to a client; the row itself no longer stores it,
// because every row in a migrated database is at this version by construction.
const SessionEventSchemaVersion = 1

// SessionEvent is the storage-layer representation of one immutable,
// conversation-ordered event. Keeping this record free of pkg/event preserves
// the state -> domain layer boundary; surfaces convert at their edge.
type SessionEvent struct {
	ID        string          `json:"id"`
	Sequence  int64           `json:"sequence,omitempty"`
	RunID     string          `json:"run_id,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

type SessionEventPage struct {
	SessionID     string         `json:"session_id"`
	Events        []SessionEvent `json:"events"`
	NextCursor    int64          `json:"next_cursor"`
	HighWater     int64          `json:"high_water"`
	HasMore       bool           `json:"has_more"`
	SchemaVersion int            `json:"schema_version"`
}

// eventColumns is the one fb_session_events column list; scanSessionEvent is
// its one reader. run_id is the only nullable column in it and reads as "".
const eventColumns = `session_id, sequence, event_id, run_id, event_type, payload_json, occurred_at_ms`

func scanSessionEvent(scanner interface{ Scan(...any) error }) (SessionEvent, error) {
	var evt SessionEvent
	var runID sql.NullString
	var occurred int64
	var payload string
	if err := scanner.Scan(&evt.SessionID, &evt.Sequence, &evt.ID, &runID, &evt.Type, &payload, &occurred); err != nil {
		return SessionEvent{}, err
	}
	evt.RunID = runID.String
	evt.Payload = json.RawMessage(payload)
	evt.CreatedAt = time.UnixMilli(occurred).UTC()
	return evt, nil
}

// ErrSessionNotStarted reports an event for a conversation that has no session
// row yet. Such an event — an MCP server failing at startup, a /compact before
// the first message — happened before the conversation existed and belongs to
// no session's history: the surface shows it and records nothing.
var ErrSessionNotStarted = errors.New("the conversation has not started; its event is shown, not recorded")

// AppendSessionEvent durably assigns a conversation cursor before an event is
// shown by a surface. Repeating an event ID is idempotent and returns the
// original event, which makes websocket replay safe.
func (s *RunStore) AppendSessionEvent(ctx context.Context, in SessionEvent) (SessionEvent, error) {
	stored, _, err := s.AppendSessionEventOnce(ctx, in)
	return stored, err
}

// AppendSessionEventOnce is AppendSessionEvent with the one fact a producer
// needs to stay silent on a repeat: whether this call is what stored the event.
//
// A producer that derives its event ID from what the event is about — rather
// than generating one per attempt — repeats itself every time it re-observes
// the same thing: a surface that reconnects and replays its state, or a second
// subscriber asking the same question. The database is the only place that knows
// whether an event is new, so it reports it here rather than leaving each caller
// to keep a memory of what it has already sent, which a restart would lose.
//
// The insert reports the stored row itself, so the common path costs one
// statement; the follow-up read only runs when the insert stored nothing —
// either the row already existed, or the conversation has no session row yet
// (ErrSessionNotStarted).
func (s *RunStore) AppendSessionEventOnce(ctx context.Context, in SessionEvent) (SessionEvent, bool, error) {
	if s == nil || s.DB == nil {
		return SessionEvent{}, false, fmt.Errorf("nil db")
	}
	in.SessionID = strings.TrimSpace(in.SessionID)
	if in.SessionID == "" {
		return SessionEvent{}, false, fmt.Errorf("session event requires session id")
	}
	in.ID = strings.TrimSpace(in.ID)
	if in.ID == "" {
		in.ID = "evt-" + uuid.NewString()
	}
	if in.CreatedAt.IsZero() {
		in.CreatedAt = time.Now().UTC()
	} else {
		in.CreatedAt = in.CreatedAt.UTC()
	}
	payload := in.Payload
	if len(payload) == 0 || !json.Valid(payload) {
		payload = json.RawMessage(`{}`)
	}
	stored, err := scanSessionEvent(s.DB.QueryRowContext(ctx, `
INSERT INTO fb_session_events(session_id, run_id, event_id, event_type, payload_json, occurred_at_ms)
SELECT ?,?,?,?,?,? WHERE EXISTS(SELECT 1 FROM fb_sessions WHERE id=?)
ON CONFLICT(session_id, event_id) DO NOTHING
RETURNING `+eventColumns,
		in.SessionID, nullIfEmpty(in.RunID), in.ID, strings.TrimSpace(in.Type), string(payload), in.CreatedAt.UnixMilli(), in.SessionID))
	if err == nil {
		return stored, true, nil
	}
	if err != sql.ErrNoRows {
		return SessionEvent{}, false, err
	}
	// Nothing was stored: the row already existed, so read it back — or there
	// is no conversation to record it in yet.
	stored, err = scanSessionEvent(s.DB.QueryRowContext(ctx, `
SELECT `+eventColumns+`
FROM fb_session_events WHERE session_id=? AND event_id=?`, in.SessionID, in.ID))
	if err == sql.ErrNoRows {
		return SessionEvent{}, false, ErrSessionNotStarted
	}
	if err != nil {
		return SessionEvent{}, false, err
	}
	return stored, false, nil
}

func (s *RunStore) SessionEventHighWater(ctx context.Context, sessionID string) (int64, error) {
	if s == nil || s.DB == nil {
		return 0, fmt.Errorf("nil db")
	}
	// A conversation with no events yet has no high water: MAX over an empty
	// set is NULL, and the cursor that no events have been written is zero.
	var high int64
	err := s.DB.QueryRowContext(ctx, `SELECT IFNULL(MAX(sequence), 0) FROM fb_session_events WHERE session_id=?`, strings.TrimSpace(sessionID)).Scan(&high)
	return high, err
}

// ListRunEvents returns a run's own events, oldest first: the run-level
// ledger (goal state, tool audit, the run events API) reads the same log the
// conversation pages by, scoped to the run that produced the rows.
func (s *RunStore) ListRunEvents(ctx context.Context, runID string, limit int) ([]SessionEvent, error) {
	if s == nil || s.DB == nil {
		return nil, fmt.Errorf("nil db")
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 500
	}
	if limit > 5000 {
		limit = 5000
	}
	rows, err := s.DB.QueryContext(ctx, `
SELECT `+eventColumns+`
FROM fb_session_events
WHERE run_id=?
ORDER BY sequence ASC LIMIT ?`, runID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionEvent
	for rows.Next() {
		evt, err := scanSessionEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, evt)
	}
	return out, rows.Err()
}

// ListRunEventsOfTypes returns a run's events of the given types, oldest
// first, however many other events the run recorded.
func (s *RunStore) ListRunEventsOfTypes(ctx context.Context, runID string, types ...string) ([]SessionEvent, error) {
	if s == nil || s.DB == nil {
		return nil, fmt.Errorf("nil db")
	}
	runID = strings.TrimSpace(runID)
	if runID == "" || len(types) == 0 {
		return nil, nil
	}
	args := []any{runID}
	marks := make([]string, 0, len(types))
	for _, t := range types {
		marks = append(marks, "?")
		args = append(args, t)
	}
	rows, err := s.DB.QueryContext(ctx, `
SELECT `+eventColumns+`
FROM fb_session_events
WHERE run_id=? AND event_type IN (`+strings.Join(marks, ",")+`)
ORDER BY sequence ASC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionEvent
	for rows.Next() {
		evt, err := scanSessionEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, evt)
	}
	return out, rows.Err()
}

// ListSessionEventsOfType returns the newest limit events of one type, oldest
// first: the record of one kind of fact about the conversation, such as its
// compactions or its plan updates, without paging through everything else.
func (s *RunStore) ListSessionEventsOfType(ctx context.Context, sessionID, eventType string, limit int) ([]SessionEvent, error) {
	if s == nil || s.DB == nil {
		return nil, fmt.Errorf("nil db")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("session id required")
	}
	if limit <= 0 {
		limit = 500
	}
	rows, err := s.DB.QueryContext(ctx, `
SELECT `+eventColumns+`
FROM fb_session_events
WHERE session_id=? AND event_type=?
ORDER BY sequence DESC LIMIT ?`, sessionID, strings.TrimSpace(eventType), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionEvent
	for rows.Next() {
		evt, err := scanSessionEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, evt)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// ListSessionEvents returns events after cursor and at or below highWater.
// highWater <= 0 means the current high water. One extra row determines
// HasMore, so callers can page without guessing from page length.
func (s *RunStore) ListSessionEvents(ctx context.Context, sessionID string, cursor, highWater int64, limit int) (SessionEventPage, error) {
	page := SessionEventPage{SessionID: strings.TrimSpace(sessionID), SchemaVersion: SessionEventSchemaVersion}
	if s == nil || s.DB == nil {
		return page, fmt.Errorf("nil db")
	}
	if page.SessionID == "" {
		return page, fmt.Errorf("session id required")
	}
	if highWater <= 0 {
		var err error
		highWater, err = s.SessionEventHighWater(ctx, page.SessionID)
		if err != nil {
			return page, err
		}
	}
	page.HighWater = highWater
	if limit <= 0 {
		limit = 500
	}
	if limit > 5000 {
		limit = 5000
	}
	rows, err := s.DB.QueryContext(ctx, `
SELECT `+eventColumns+`
FROM fb_session_events
WHERE session_id=? AND sequence>? AND sequence<=?
ORDER BY sequence ASC LIMIT ?`, page.SessionID, cursor, highWater, limit+1)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		evt, err := scanSessionEvent(rows)
		if err != nil {
			return page, err
		}
		if len(page.Events) == limit {
			page.HasMore = true
			break
		}
		page.Events = append(page.Events, evt)
		page.NextCursor = evt.Sequence
	}
	if err := rows.Err(); err != nil && err != sql.ErrNoRows {
		return page, err
	}
	if len(page.Events) == 0 {
		page.NextCursor = cursor
	}
	return page, nil
}

// ToolCallRow is one user-visible tool completion read back from the event log
// under the field names the web audit panel uses. The event log is the only
// source: what ran is one record, and every reader projects the same row.
type ToolCallRow struct {
	ID         string `json:"id"`
	RunID      string `json:"runId"`
	SessionID  string `json:"sessionId"`
	ToolName   string `json:"toolName"`
	DetailJSON string `json:"detailJson"`
	CreatedAt  int64  `json:"createdAt"`
}

// visibleToolCallPredicate selects the tool completions an audit reader shows:
// the ones the surfaces drew as a tool card. A permission request is a gate the
// transcript already renders, and a plan-emitting tool that succeeded speaks
// through its plan card, so neither is a tool call the audit panel lists. A
// plan-emitting call that failed has no plan card to stand for it and keeps
// its own, as the surfaces draw it (event.ToolStepRendersAsPlan).
const visibleToolCallPredicate = `event_type='tool_call_completed'
  AND IFNULL(payload_json->>'$.tool_name','') <> 'request_permissions'
  AND NOT (IFNULL(payload_json->>'$.tool_name','') = 'session_todo' AND TRIM(IFNULL(payload_json->>'$.error','')) = '')`

// ListSessionToolCalls returns the conversation's newest tool completions,
// newest first, narrowed to one run and one tool when those are given. The
// filter runs inside the query, so a narrow request is answered from the
// narrowed set rather than from a page of everything truncated first.
func (s *RunStore) ListSessionToolCalls(ctx context.Context, sessionID, runID, toolName string, limit int) ([]ToolCallRow, error) {
	if s == nil || s.DB == nil {
		return nil, fmt.Errorf("nil db")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("session id required")
	}
	query := `SELECT event_id, IFNULL(run_id,''), session_id, IFNULL(payload_json->>'$.tool_name',''), payload_json, occurred_at_ms
FROM fb_session_events
WHERE session_id=? AND ` + visibleToolCallPredicate
	args := []any{sessionID}
	if runID = strings.TrimSpace(runID); runID != "" {
		query += ` AND run_id=?`
		args = append(args, runID)
	}
	if toolName = strings.TrimSpace(toolName); toolName != "" {
		query += ` AND payload_json->>'$.tool_name'=?`
		args = append(args, toolName)
	}
	if limit <= 0 {
		limit = 200
	}
	if limit > 5000 {
		limit = 5000
	}
	query += ` ORDER BY sequence DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ToolCallRow{}
	for rows.Next() {
		var row ToolCallRow
		var occurred int64
		if err := rows.Scan(&row.ID, &row.RunID, &row.SessionID, &row.ToolName, &row.DetailJSON, &occurred); err != nil {
			return nil, err
		}
		row.CreatedAt = occurred / 1000
		out = append(out, row)
	}
	return out, rows.Err()
}

// CountSessionToolCalls reports how many tool completions the conversation
// recorded, per tool and in total.
func (s *RunStore) CountSessionToolCalls(ctx context.Context, sessionID string) (map[string]int, int, error) {
	if s == nil || s.DB == nil {
		return nil, 0, fmt.Errorf("nil db")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, 0, fmt.Errorf("session id required")
	}
	rows, err := s.DB.QueryContext(ctx, `
SELECT IFNULL(payload_json->>'$.tool_name',''), COUNT(*)
FROM fb_session_events
WHERE session_id=? AND `+visibleToolCallPredicate+`
GROUP BY 1`, sessionID)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	byTool := map[string]int{}
	total := 0
	for rows.Next() {
		var name string
		var count int
		if err := rows.Scan(&name, &count); err != nil {
			return nil, 0, err
		}
		byTool[name] = count
		total += count
	}
	return byTool, total, rows.Err()
}

// LatestRewindableToolCall names the file the conversation's most recent
// successful edit_file or write_file call changed, for a rewind to restore. A
// call that failed changed nothing, so it is not the change a rewind undoes.
func (s *RunStore) LatestRewindableToolCall(ctx context.Context, sessionID string) (string, bool, error) {
	if s == nil || s.DB == nil {
		return "", false, fmt.Errorf("nil db")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", false, fmt.Errorf("session id required")
	}
	var path string
	err := s.DB.QueryRowContext(ctx, `
SELECT IFNULL(payload_json->>'$.tool_meta.input.file_path','')
FROM fb_session_events
WHERE session_id=? AND event_type='tool_call_completed'
  AND IFNULL(payload_json->>'$.tool_name','') IN ('edit_file','write_file')
  AND TRIM(IFNULL(payload_json->>'$.error','')) = ''
ORDER BY sequence DESC LIMIT 1`, sessionID).Scan(&path)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	path = strings.TrimSpace(path)
	return path, path != "", nil
}
