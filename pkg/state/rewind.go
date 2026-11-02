package state

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/state/dbx"
)

// SHA256Hex is the hex sha256 of content — the digest a file change records
// for what a tool wrote and a rewind compares a file against.
func SHA256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// This file is the state layer behind "rewind to before a message" and the
// workbench's changed-files list: the ledger of every file a file tool wrote
// (fb_file_changes), the record of each rewind and how to undo it
// (fb_rewinds), and the message-visibility and event filtering a rewind needs.
// The orchestration that reads and writes them — what "both", "chat" and
// "code" mean — lives in pkg/turn, shared by the gateway and the terminal.

// The ledger's statements. Each SQLite arm is the text this file carried before
// multi-database support; PostgreSQL numbers its parameters and takes COALESCE
// where the others take IFNULL.
var (
	fileChangeInsertSQL = dbx.SQL{
		SQLite: `
INSERT INTO fb_file_changes(session_id, run_id, step_id, anchor_message_id, path, existed_before, before_snapshot, after_sha256, deleted, created_at_ms)
VALUES(?,?,?,?,?,?,?,?,?,?)`,
		MySQL: `
INSERT INTO fb_file_changes(session_id, run_id, step_id, anchor_message_id, path, existed_before, before_snapshot, after_sha256, deleted, created_at_ms)
VALUES(?,?,?,?,?,?,?,?,?,?)`,
		Postgres: `
INSERT INTO fb_file_changes(session_id, run_id, step_id, anchor_message_id, path, existed_before, before_snapshot, after_sha256, deleted, created_at_ms)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
	}
	deleteFileChangePathSQL = dbx.SQL{
		SQLite:   `DELETE FROM fb_file_changes WHERE session_id=? AND path=?`,
		MySQL:    `DELETE FROM fb_file_changes WHERE session_id=? AND path=?`,
		Postgres: `DELETE FROM fb_file_changes WHERE session_id=$1 AND path=$2`,
	}
	latestUserMessageRowSelectSQL = dbx.SQL{
		SQLite:   `SELECT id, parts FROM fb_messages WHERE session_id=? AND role='user' AND visibility=? ORDER BY id DESC LIMIT 40`,
		MySQL:    `SELECT id, parts FROM fb_messages WHERE session_id=? AND role='user' AND visibility=? ORDER BY id DESC LIMIT 40`,
		Postgres: `SELECT id, parts FROM fb_messages WHERE session_id=$1 AND role='user' AND visibility=$2 ORDER BY id DESC LIMIT 40`,
	}
	userMessageContentSQL = dbx.SQL{
		SQLite:   `SELECT content FROM fb_messages WHERE id=? AND session_id=? AND role='user'`,
		MySQL:    `SELECT content FROM fb_messages WHERE id=? AND session_id=? AND role='user'`,
		Postgres: `SELECT content FROM fb_messages WHERE id=$1 AND session_id=$2 AND role='user'`,
	}
	visibleUserMessagesSQL = dbx.SQL{
		SQLite: `
SELECT id, content, parts FROM fb_messages
WHERE session_id=? AND role='user' AND visibility=?
ORDER BY id DESC LIMIT ?`,
		MySQL: `
SELECT id, content, parts FROM fb_messages
WHERE session_id=? AND role='user' AND visibility=?
ORDER BY id DESC LIMIT ?`,
		Postgres: `
SELECT id, content, parts FROM fb_messages
WHERE session_id=$1 AND role='user' AND visibility=$2
ORDER BY id DESC LIMIT $3`,
	}
	insertRewindSQL = dbx.SQL{
		SQLite: `
INSERT INTO fb_rewinds(id, session_id, anchor_message_id, mode, hidden_event_seq_from, files_json, state, created_at_ms)
VALUES(?,?,?,?,?,?,?,?)`,
		MySQL: `
INSERT INTO fb_rewinds(id, session_id, anchor_message_id, mode, hidden_event_seq_from, files_json, state, created_at_ms)
VALUES(?,?,?,?,?,?,?,?)`,
		Postgres: `
INSERT INTO fb_rewinds(id, session_id, anchor_message_id, mode, hidden_event_seq_from, files_json, state, created_at_ms)
VALUES($1,$2,$3,$4,$5,$6,$7,$8)`,
	}
	pendingRewindSQL = dbx.SQL{
		SQLite: `
SELECT id, session_id, anchor_message_id, mode, hidden_event_seq_from, files_json, state, created_at_ms
FROM fb_rewinds WHERE session_id=? AND state='pending' ORDER BY created_at_ms DESC, id DESC LIMIT 1`,
		MySQL: `
SELECT id, session_id, anchor_message_id, mode, hidden_event_seq_from, files_json, state, created_at_ms
FROM fb_rewinds WHERE session_id=? AND state='pending' ORDER BY created_at_ms DESC, id DESC LIMIT 1`,
		Postgres: `
SELECT id, session_id, anchor_message_id, mode, hidden_event_seq_from, files_json, state, created_at_ms
FROM fb_rewinds WHERE session_id=$1 AND state='pending' ORDER BY created_at_ms DESC, id DESC LIMIT 1`,
	}
	fixPendingRewindsSQL = dbx.SQL{
		SQLite:   `UPDATE fb_rewinds SET state='fixed' WHERE session_id=? AND state='pending'`,
		MySQL:    `UPDATE fb_rewinds SET state='fixed' WHERE session_id=? AND state='pending'`,
		Postgres: `UPDATE fb_rewinds SET state='fixed' WHERE session_id=$1 AND state='pending'`,
	}
	deleteRewindSQL = dbx.SQL{
		SQLite:   `DELETE FROM fb_rewinds WHERE id=?`,
		MySQL:    `DELETE FROM fb_rewinds WHERE id=?`,
		Postgres: `DELETE FROM fb_rewinds WHERE id=$1`,
	}
	// One statement serves both directions of a rewind: it moves the anchor and
	// everything after it between two visibility values.
	setMessageVisibilitySQL = dbx.SQL{
		SQLite:   `UPDATE fb_messages SET visibility=? WHERE session_id=? AND id>=? AND visibility=?`,
		MySQL:    `UPDATE fb_messages SET visibility=? WHERE session_id=? AND id>=? AND visibility=?`,
		Postgres: `UPDATE fb_messages SET visibility=$1 WHERE session_id=$2 AND id>=$3 AND visibility=$4`,
	}
	rewindEventStartSQL = dbx.SQL{
		SQLite: `
SELECT IFNULL(MIN(e.sequence), 0)
FROM fb_session_events e
WHERE e.session_id=? AND TRIM(IFNULL(e.run_id,'')) <> ''
  AND e.run_id = (SELECT TRIM(IFNULL(m.run_id,'')) FROM fb_messages m WHERE m.id=? AND m.session_id=?)`,
		MySQL: `
SELECT IFNULL(MIN(e.sequence), 0)
FROM fb_session_events e
WHERE e.session_id=? AND TRIM(IFNULL(e.run_id,'')) <> ''
  AND e.run_id = (SELECT TRIM(IFNULL(m.run_id,'')) FROM fb_messages m WHERE m.id=? AND m.session_id=?)`,
		Postgres: `
SELECT COALESCE(MIN(e.sequence), 0)
FROM fb_session_events e
WHERE e.session_id=$1 AND TRIM(COALESCE(e.run_id,'')) <> ''
  AND e.run_id = (SELECT TRIM(COALESCE(m.run_id,'')) FROM fb_messages m WHERE m.id=$2 AND m.session_id=$3)`,
	}
)

// The two changed-files reads share their column list and their FROM clause;
// only the tail differs. The FROM clause carries the indentation the statement
// has always had, spelled with escapes so it survives as that text: MySQL
// matches SQLite, and PostgreSQL numbers its session parameter.
const (
	changedFileColumns      = `SELECT id, run_id, step_id, anchor_message_id, path, existed_before, before_snapshot, after_sha256, deleted, created_at_ms`
	changedFileFromSQLite   = "\n\t\t FROM fb_file_changes WHERE session_id=?"
	changedFileFromPostgres = "\n\t\t FROM fb_file_changes WHERE session_id=$1"
)

// FileChange is one file write a file tool recorded. It is the value the tool
// layer hands the store's recorder; the store never inspects the file itself.
type FileChange struct {
	SessionID       string
	RunID           string
	StepID          string
	AnchorMessageID int64
	Path            string
	ExistedBefore   bool
	// BeforeSnapshot is the snapshot path written before an overwrite; empty
	// when the file did not exist.
	BeforeSnapshot string
	// AfterSHA256 is the hex sha256 of the bytes written; empty when the write
	// deleted the file.
	AfterSHA256 string
	Deleted     bool
}

// FileChangeRow is a recorded file change read back, with its ledger id.
type FileChangeRow struct {
	ID              int64
	RunID           string
	StepID          string
	AnchorMessageID int64
	Path            string
	ExistedBefore   bool
	BeforeSnapshot  string
	AfterSHA256     string
	Deleted         bool
	CreatedAtMs     int64
}

// RecordFileChange appends one file-tool write to the ledger. It is the
// implementation of tool.FileChangeRecorder; a session with no id is refused
// so a write can never land in the ledger without a conversation to belong to.
func (s *SessionStore) RecordFileChange(ctx context.Context, c FileChange) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("record file change: session store unavailable")
	}
	sessionID := strings.TrimSpace(c.SessionID)
	if sessionID == "" {
		return fmt.Errorf("record file change: empty session id")
	}
	path := strings.TrimSpace(c.Path)
	if path == "" {
		return fmt.Errorf("record file change: empty path")
	}
	_, err := dbx.Exec(ctx, s.db, fileChangeInsertSQL.For(dbx.DialectOf(s.db)),
		sessionID, strings.TrimSpace(c.RunID), strings.TrimSpace(c.StepID), c.AnchorMessageID, path,
		boolToInt(c.ExistedBefore), strings.TrimSpace(c.BeforeSnapshot), strings.TrimSpace(c.AfterSHA256),
		boolToInt(c.Deleted), time.Now().UnixMilli())
	if err != nil {
		return fmt.Errorf("record file change: %w", err)
	}
	return nil
}

// FileChangesSince returns the recorded changes whose turn is at or after
// anchorMessageID, oldest first: rewinding to before that message restores
// everything it returns.
func (s *SessionStore) FileChangesSince(ctx context.Context, sessionID string, anchorMessageID int64) ([]FileChangeRow, error) {
	return s.queryFileChanges(ctx, dbx.SQL{
		SQLite:   changedFileColumns + changedFileFromSQLite + ` AND anchor_message_id>=? ORDER BY id ASC`,
		MySQL:    changedFileColumns + changedFileFromSQLite + ` AND anchor_message_id>=? ORDER BY id ASC`,
		Postgres: changedFileColumns + changedFileFromPostgres + ` AND anchor_message_id>=$2 ORDER BY id ASC`,
	}, strings.TrimSpace(sessionID), anchorMessageID)
}

// AllFileChanges returns every change recorded for a conversation, oldest
// first: the source of the workbench's changed-files list.
func (s *SessionStore) AllFileChanges(ctx context.Context, sessionID string) ([]FileChangeRow, error) {
	return s.queryFileChanges(ctx, dbx.SQL{
		SQLite:   changedFileColumns + changedFileFromSQLite + ` ORDER BY id ASC`,
		MySQL:    changedFileColumns + changedFileFromSQLite + ` ORDER BY id ASC`,
		Postgres: changedFileColumns + changedFileFromPostgres + ` ORDER BY id ASC`,
	}, strings.TrimSpace(sessionID))
}

func (s *SessionStore) queryFileChanges(ctx context.Context, q dbx.SQL, args ...any) ([]FileChangeRow, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("file changes: session store unavailable")
	}
	rows, err := s.db.QueryContext(ctx, q.For(dbx.DialectOf(s.db)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FileChangeRow
	for rows.Next() {
		var r FileChangeRow
		var existed, deleted int
		if err := rows.Scan(&r.ID, &r.RunID, &r.StepID, &r.AnchorMessageID, &r.Path, &existed, &r.BeforeSnapshot, &r.AfterSHA256, &deleted, &r.CreatedAtMs); err != nil {
			return nil, err
		}
		r.ExistedBefore = existed != 0
		r.Deleted = deleted != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteFileChangePath removes the ledger rows for one path, so a
// single-file revert drops it from the changed-files list.
func (s *SessionStore) DeleteFileChangePath(ctx context.Context, sessionID, path string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("delete file change: session store unavailable")
	}
	_, err := dbx.Exec(ctx, s.db, deleteFileChangePathSQL.For(dbx.DialectOf(s.db)),
		strings.TrimSpace(sessionID), strings.TrimSpace(path))
	return err
}

// LatestUserMessageRowID returns the newest user message's row id for a
// conversation: the anchor a turn's file changes are recorded against. A
// runtime-injected user row — the environment-context block the model request
// carries — is meta, not something the person sent, so it is skipped.
func (s *SessionStore) LatestUserMessageRowID(ctx context.Context, sessionID string) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("latest user message: session store unavailable")
	}
	rows, err := s.db.QueryContext(ctx, latestUserMessageRowSelectSQL.For(dbx.DialectOf(s.db)),
		strings.TrimSpace(sessionID), messageVisible)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var parts string
		if err := rows.Scan(&id, &parts); err != nil {
			return 0, err
		}
		if _, _, _, isMeta := ParseMessageParts(parts, ""); isMeta {
			continue
		}
		return id, nil
	}
	return 0, rows.Err()
}

// UserMessageContent returns the display text of one owned user message row.
func (s *SessionStore) UserMessageContent(ctx context.Context, sessionID string, rowID int64) (string, bool, error) {
	if s == nil || s.db == nil {
		return "", false, fmt.Errorf("user message content: session store unavailable")
	}
	var content string
	err := s.db.QueryRowContext(ctx, userMessageContentSQL.For(dbx.DialectOf(s.db)),
		rowID, strings.TrimSpace(sessionID)).Scan(&content)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return content, true, nil
}

// UserMessageRef is one visible user message a rewind can target.
type UserMessageRef struct {
	RowID   int64
	Content string
}

// ListVisibleUserMessages returns a conversation's visible user messages,
// newest first: the rows a "rewind to before a message" picker lists.
func (s *SessionStore) ListVisibleUserMessages(ctx context.Context, sessionID string, limit int) ([]UserMessageRef, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("list user messages: session store unavailable")
	}
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, visibleUserMessagesSQL.For(dbx.DialectOf(s.db)), strings.TrimSpace(sessionID), messageVisible, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UserMessageRef{}
	for rows.Next() {
		var r UserMessageRef
		var parts string
		if err := rows.Scan(&r.RowID, &r.Content, &parts); err != nil {
			return nil, err
		}
		// The runtime-injected environment block is a user row too; it is not
		// something the person sent, so it is not a rewind target.
		if _, _, _, isMeta := ParseMessageParts(parts, ""); isMeta {
			continue
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RewindRecord is one "rewind to before a message" as stored.
type RewindRecord struct {
	ID                 string
	SessionID          string
	AnchorMessageID    int64
	Mode               string
	HiddenEventSeqFrom int64
	FilesJSON          string
	State              string
	CreatedAtMs        int64
}

// InsertRewind records a rewind.
func (s *SessionStore) InsertRewind(ctx context.Context, rec RewindRecord) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("insert rewind: session store unavailable")
	}
	_, err := dbx.Exec(ctx, s.db, insertRewindSQL.For(dbx.DialectOf(s.db)),
		strings.TrimSpace(rec.ID), strings.TrimSpace(rec.SessionID), rec.AnchorMessageID,
		strings.TrimSpace(rec.Mode), rec.HiddenEventSeqFrom, rec.FilesJSON, strings.TrimSpace(rec.State), rec.CreatedAtMs)
	if err != nil {
		return fmt.Errorf("insert rewind: %w", err)
	}
	return nil
}

// PendingRewind returns the conversation's undoable rewind, if any. At most one
// rewind is ever pending: a second rewind fixes the first before it runs.
func (s *SessionStore) PendingRewind(ctx context.Context, sessionID string) (RewindRecord, bool, error) {
	if s == nil || s.db == nil {
		return RewindRecord{}, false, fmt.Errorf("pending rewind: session store unavailable")
	}
	var rec RewindRecord
	err := s.db.QueryRowContext(ctx, pendingRewindSQL.For(dbx.DialectOf(s.db)),
		strings.TrimSpace(sessionID)).
		Scan(&rec.ID, &rec.SessionID, &rec.AnchorMessageID, &rec.Mode, &rec.HiddenEventSeqFrom, &rec.FilesJSON, &rec.State, &rec.CreatedAtMs)
	if errors.Is(err, sql.ErrNoRows) {
		return RewindRecord{}, false, nil
	}
	if err != nil {
		return RewindRecord{}, false, err
	}
	return rec, true, nil
}

// FixPendingRewinds fixes every pending rewind of a conversation in place: a
// new user message has been sent, so the rewind can no longer be undone.
func (s *SessionStore) FixPendingRewinds(ctx context.Context, sessionID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("fix pending rewinds: session store unavailable")
	}
	_, err := dbx.Exec(ctx, s.db, fixPendingRewindsSQL.For(dbx.DialectOf(s.db)),
		strings.TrimSpace(sessionID))
	return err
}

// DeleteRewind removes a rewind record, as undo does once its files and rows
// are back.
func (s *SessionStore) DeleteRewind(ctx context.Context, id string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("delete rewind: session store unavailable")
	}
	_, err := dbx.Exec(ctx, s.db, deleteRewindSQL.For(dbx.DialectOf(s.db)), strings.TrimSpace(id))
	return err
}

// RewindMessagesFrom hides the anchor user message and everything after it
// from the display transcript and the model history, keeping the rows on disk.
// It returns how many rows it moved.
func (s *SessionStore) RewindMessagesFrom(ctx context.Context, sessionID string, anchorMessageID int64) (int, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("rewind messages: session store unavailable")
	}
	res, err := dbx.Exec(ctx, s.db, setMessageVisibilitySQL.For(dbx.DialectOf(s.db)),
		messageRewound, strings.TrimSpace(sessionID), anchorMessageID, messageVisible)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// RestoreRewoundMessages puts the rows a rewind hid back into the transcript.
func (s *SessionStore) RestoreRewoundMessages(ctx context.Context, sessionID string, anchorMessageID int64) (int, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("restore rewound messages: session store unavailable")
	}
	res, err := dbx.Exec(ctx, s.db, setMessageVisibilitySQL.For(dbx.DialectOf(s.db)),
		messageVisible, strings.TrimSpace(sessionID), anchorMessageID, messageRewound)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// RewindEventStart returns the sequence of the first event of the turn the
// anchor user message started, which is where a rewind's hidden event range
// begins. It returns 0 when the anchor's run recorded no events, in which case
// no events are hidden.
func (s *SessionStore) RewindEventStart(ctx context.Context, sessionID string, anchorMessageID int64) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("rewind event start: session store unavailable")
	}
	var seq int64
	err := s.db.QueryRowContext(ctx, rewindEventStartSQL.For(dbx.DialectOf(s.db)),
		strings.TrimSpace(sessionID), anchorMessageID, strings.TrimSpace(sessionID)).Scan(&seq)
	if err != nil {
		return 0, err
	}
	return seq, nil
}

// rewindVisibleEventsPredicate excludes the events a conversation's rewinds
// hide. Every reader of fb_session_events composes it against that table
// unaliased, and the session term carries the table name for a reason: inside
// the NOT EXISTS subquery the FROM list is fb_rewinds, which has a session_id
// of its own, and SQLite resolves an unqualified name in the innermost scope
// first. A bare `session_id` would compare the rewinds row with itself — every
// rewind would then hide events of every conversation, and
// idx_fb_rewinds_session_state could not answer the subquery. A constant, not
// a function of caller-supplied expressions, so there is nothing left to pass
// wrong; a caller that needs an alias has to revisit this.
const rewindVisibleEventsPredicate = `NOT EXISTS (
  SELECT 1 FROM fb_rewinds rw
  WHERE rw.session_id = fb_session_events.session_id
    AND rw.hidden_event_seq_from > 0
    AND fb_session_events.sequence >= rw.hidden_event_seq_from
    AND (rw.state = 'pending' OR fb_session_events.sequence < IFNULL((
          SELECT MIN(e2.sequence)
          FROM fb_session_events e2
          JOIN fb_messages m2 ON m2.run_id = e2.run_id AND m2.session_id = e2.session_id
          WHERE m2.session_id = rw.session_id
            AND m2.role = 'user'
            AND m2.id > rw.anchor_message_id
            AND m2.visibility = 'visible'
            AND TRIM(IFNULL(m2.run_id,'')) <> ''
        ), 9223372036854775807))
)`

// rewindVisibleEventsPredicatePostgres is the same predicate for PostgreSQL:
// COALESCE in place of IFNULL, and a MIN over an empty set falling back to the
// largest BIGINT. The correlation is unchanged — PostgreSQL resolves an
// unqualified name in the innermost scope first too, so the session term keeps
// carrying the table name.
const rewindVisibleEventsPredicatePostgres = `NOT EXISTS (
  SELECT 1 FROM fb_rewinds rw
  WHERE rw.session_id = fb_session_events.session_id
    AND rw.hidden_event_seq_from > 0
    AND fb_session_events.sequence >= rw.hidden_event_seq_from
    AND (rw.state = 'pending' OR fb_session_events.sequence < COALESCE((
          SELECT MIN(e2.sequence)
          FROM fb_session_events e2
          JOIN fb_messages m2 ON m2.run_id = e2.run_id AND m2.session_id = e2.session_id
          WHERE m2.session_id = rw.session_id
            AND m2.role = 'user'
            AND m2.id > rw.anchor_message_id
            AND m2.visibility = 'visible'
            AND TRIM(COALESCE(m2.run_id,'')) <> ''
        ), 9223372036854775807))
)`

// rewindVisibleEventsPredicateFor picks the predicate for d. MySQL takes the
// SQLite text unchanged: IFNULL is MySQL's own function.
func rewindVisibleEventsPredicateFor(d dbx.Dialect) string {
	if d == dbx.Postgres {
		return rewindVisibleEventsPredicatePostgres
	}
	return rewindVisibleEventsPredicate
}
