package event

import (
	"encoding/json"
	"time"
)

const DiffEventTurnUpdated = "turn_diff_updated"

type DiffEvent struct {
	RunID     string          `json:"run_id,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

// DiffHunk is a contiguous block of changes in the wire payload.
type DiffHunk struct {
	OldStart int        `json:"old_start"`
	NewStart int        `json:"new_start"`
	Lines    []DiffLine `json:"lines"`
}

// TurnDiffFile describes one file's changes in the wire payload.
type TurnDiffFile struct {
	Path    string     `json:"path,omitempty"`
	OldPath string     `json:"old_path,omitempty"`
	Status  string     `json:"status,omitempty"`
	Added   int        `json:"added,omitempty"`
	Deleted int        `json:"deleted,omitempty"`
	Binary  bool       `json:"binary,omitempty"`
	Hunks   []DiffHunk `json:"hunks,omitempty"`
}

// TurnDiffUpdatedPayload is sent as a turn_diff_updated WebSocket event.
// It carries structured hunk data; the raw diff string has been removed to
// avoid duplicating bytes now that frontends render from Hunks directly.
type TurnDiffUpdatedPayload struct {
	Files   []TurnDiffFile `json:"files,omitempty"`
	Summary string         `json:"summary,omitempty"`
}
