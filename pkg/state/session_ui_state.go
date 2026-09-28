package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// SaveSessionUIState persists browsing-only state independently of messages.
// The owning primary agent is checked on every access just like transcript
// history, so a guessed session id cannot expose another agent's viewport.
func (s *SessionStore) SaveSessionUIState(ctx context.Context, sessionID, surface string, raw json.RawMessage) error {
	if s == nil || s.db == nil {
		return nil
	}
	sessionID, surface = strings.TrimSpace(sessionID), strings.TrimSpace(surface)
	if sessionID == "" || surface == "" {
		return fmt.Errorf("session id and surface are required")
	}
	if err := s.requireOwned(ctx, s.db, sessionID); err != nil {
		return err
	}
	owned, err := s.HasSession(ctx, sessionID)
	if err != nil {
		return err
	}
	if !owned {
		// A fresh TUI that exits before its first turn has no durable session;
		// browsing state must not create an orphan conversation by itself.
		return nil
	}
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if !json.Valid(raw) {
		return fmt.Errorf("invalid session ui state json")
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO fb_session_ui_state(session_id,surface,state_json,updated_at)
VALUES(?,?,?,?)
ON CONFLICT(session_id,surface) DO UPDATE SET state_json=excluded.state_json,updated_at=excluded.updated_at`,
		sessionID, surface, string(raw), time.Now().Unix())
	return err
}

func (s *SessionStore) LoadSessionUIState(ctx context.Context, sessionID, surface string) (json.RawMessage, bool, error) {
	if s == nil || s.db == nil {
		return nil, false, nil
	}
	sessionID, surface = strings.TrimSpace(sessionID), strings.TrimSpace(surface)
	if sessionID == "" || surface == "" {
		return nil, false, nil
	}
	if err := s.requireOwned(ctx, s.db, sessionID); err != nil {
		return nil, false, err
	}
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT state_json FROM fb_session_ui_state WHERE session_id=? AND surface=?`, sessionID, surface).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !json.Valid([]byte(raw)) {
		return nil, false, fmt.Errorf("invalid stored session ui state json")
	}
	return json.RawMessage(raw), true, nil
}
