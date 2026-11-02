package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// FileReadState is one conversation's read baseline for one path: the version of
// the file read_file last put in front of the model. It carries the facts the
// overwrite check compares against — mtime, size and the SHA-256 of the whole
// file — and never the file's bytes.
//
// The record is per conversation because it is a fact about what that model saw,
// not a property of the file: two conversations each owe their own read, and a
// baseline inherited from someone else would authorize an overwrite nobody
// looked at.
type FileReadState struct {
	SessionID string
	AgentID   string
	Path      string
	ModTimeNS int64
	Size      int64
	SHA256    string
	ReadAtNS  int64
	TouchKind string
}

// fileReadStateColumns is the one column list the reader and the writer share.
const fileReadStateColumns = `session_id, agent_id, path, mod_time_ns, size, sha256, read_at_ns, touch_kind`

// SaveFileReadState records that read_file showed this conversation the whole
// file at Path. A repeated read of the same path overwrites the row: the newest
// baseline is the one the model last saw.
//
// The row is always filed under this store's own agent — the tenant is the
// store's, never the caller's say-so — and a session that belongs to another
// primary agent is refused rather than written. Invalid input is an error and
// writes nothing: a row the overwrite check cannot trust is worse than no row.
func (s *SessionStore) SaveFileReadState(ctx context.Context, in FileReadState) error {
	if s == nil || s.db == nil {
		return nil
	}
	sessionID := strings.TrimSpace(in.SessionID)
	if sessionID == "" {
		return fmt.Errorf("file read state needs a session id")
	}
	path := strings.TrimSpace(in.Path)
	if path == "" {
		return fmt.Errorf("file read state needs a path")
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("file read state needs an absolute path: %q", in.Path)
	}
	path = filepath.Clean(path)
	sha := strings.TrimSpace(in.SHA256)
	if !isSHA256Hex(sha) {
		return fmt.Errorf("file read state needs a 64-character lowercase hex sha256: %q", in.SHA256)
	}
	kind := strings.TrimSpace(in.TouchKind)
	if kind == "" {
		kind = "read"
	}
	if kind != "read" {
		return fmt.Errorf("file read state only records reads, not %q", kind)
	}
	if err := s.requireOwned(ctx, s.db, sessionID); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `
INSERT INTO fb_file_read_states(`+fileReadStateColumns+`)
VALUES(?,?,?,?,?,?,?,?)
ON CONFLICT(session_id, path) DO UPDATE SET
  agent_id=excluded.agent_id,
  mod_time_ns=excluded.mod_time_ns,
  size=excluded.size,
  sha256=excluded.sha256,
  read_at_ns=excluded.read_at_ns,
  touch_kind=excluded.touch_kind`,
		sessionID, s.AgentID(), path, in.ModTimeNS, in.Size, sha, in.ReadAtNS, kind); err != nil {
		return fmt.Errorf("save file read state for %s: %w", path, err)
	}
	return nil
}

// FileReadStateForSession returns the baseline this conversation holds for path,
// if it holds one. The lookup names the tenant as well as the conversation and
// the path, so a baseline filed by another primary agent reads as "none" — the
// caller then asks for the read it owes instead of borrowing someone else's.
//
// A database failure is returned as an error and must never be read as "not
// read": the difference decides whether the model is told to re-read a file or
// whether a broken state file silently drops the overwrite guard's baseline.
func (s *SessionStore) FileReadStateForSession(ctx context.Context, sessionID, path string) (FileReadState, bool, error) {
	if s == nil || s.db == nil {
		return FileReadState{}, false, nil
	}
	sessionID = strings.TrimSpace(sessionID)
	path = strings.TrimSpace(path)
	if sessionID == "" || path == "" {
		return FileReadState{}, false, nil
	}
	path = filepath.Clean(path)
	var out FileReadState
	err := s.db.QueryRowContext(ctx,
		`SELECT `+fileReadStateColumns+` FROM fb_file_read_states WHERE session_id=? AND path=? AND agent_id=?`,
		sessionID, path, s.AgentID()).Scan(
		&out.SessionID, &out.AgentID, &out.Path, &out.ModTimeNS,
		&out.Size, &out.SHA256, &out.ReadAtNS, &out.TouchKind)
	if errors.Is(err, sql.ErrNoRows) {
		return FileReadState{}, false, nil
	}
	if err != nil {
		return FileReadState{}, false, fmt.Errorf("read file read state for %s: %w", path, err)
	}
	return out, true, nil
}

// isSHA256Hex reports whether s is exactly one lowercase hex SHA-256 digest.
func isSHA256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f':
		default:
			return false
		}
	}
	return true
}
