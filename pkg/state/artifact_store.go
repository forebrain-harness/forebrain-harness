// Artifact storage: the fb_artifacts table and its read/write surface.
//
// An artifact is a file a conversation delivered — the model presented it with
// present_files, a batch task's output folder was collected as a fallback, or
// an office plan was kept as the deliverable. The row registers the file; it
// never copies it, so a read later in the lifetime can report the file as
// changed or missing. The primary agent is the tenant (agent_id), exactly like
// sessions: every read and write stays inside that boundary, and an artifact
// goes with its conversation when the conversation is deleted.
package state

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Artifact is one row of fb_artifacts.
type Artifact struct {
	ID, AgentID, SessionID, RunID, ToolCallID string
	Path, RelPath, Name, Title, Description   string
	MIME, Kind, Preview                       string
	Size, MTimeNS                             int64
	SHA256, Origin                            string
	CreatedAt                                 int64
}

// Artifact origins. The model presented the file ('model'), a batch task's
// output folder was collected because the task presented nothing
// ('fallback'), or an office plan was kept as the deliverable ('plan').
const (
	ArtifactOriginModel    = "model"
	ArtifactOriginFallback = "fallback"
	ArtifactOriginPlan     = "plan"
)

// ErrArtifactNotFound reports a read for an artifact this conversation does
// not hold: a missing id and another agent's id read the same, because
// neither is the caller's business.
var ErrArtifactNotFound = errors.New("artifact not found")

// artifactColumns is the one column list every artifact read scans, in the
// order scanArtifact reads them.
const artifactColumns = `id, agent_id, session_id, run_id, tool_call_id, path, rel_path,
  name, title, description, mime, kind, preview, size, mtime_ns, sha256, origin, created_at`

// AddArtifact registers one delivered file under this agent's conversation.
// The session must be this primary agent's; the id, agent and timestamp the
// caller does not carry are filled here so every row agrees on who wrote it
// and when.
func (s *SessionStore) AddArtifact(ctx context.Context, a Artifact) (Artifact, error) {
	if s == nil || s.db == nil {
		return a, errors.New("session store unavailable")
	}
	a.SessionID = strings.TrimSpace(a.SessionID)
	if a.SessionID == "" {
		return a, errors.New("artifact needs a session")
	}
	if err := s.requireOwned(ctx, s.db, a.SessionID); err != nil {
		return a, err
	}
	a.ID = strings.TrimSpace(a.ID)
	if a.ID == "" {
		a.ID = NewID("art")
	}
	a.AgentID = s.AgentID()
	a.Origin = strings.TrimSpace(a.Origin)
	if a.Origin == "" {
		a.Origin = ArtifactOriginModel
	}
	a.CreatedAt = time.Now().Unix()
	res, err := s.db.ExecContext(ctx, `
INSERT INTO fb_artifacts (id, agent_id, session_id, run_id, tool_call_id, path, rel_path,
  name, title, description, mime, kind, preview, size, mtime_ns, sha256, origin, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.AgentID, a.SessionID, a.RunID, a.ToolCallID, a.Path, a.RelPath,
		a.Name, a.Title, a.Description, a.MIME, a.Kind, a.Preview, a.Size, a.MTimeNS, a.SHA256, a.Origin, a.CreatedAt)
	if err != nil {
		return a, err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return a, errors.New("artifact was not written")
	}
	return a, nil
}

// GetArtifact reads one artifact of this agent's conversation. Another
// agent's row and a missing id are both ErrArtifactNotFound.
func (s *SessionStore) GetArtifact(ctx context.Context, sessionID, id string) (Artifact, error) {
	if s == nil || s.db == nil {
		return Artifact{}, ErrArtifactNotFound
	}
	sessionID = strings.TrimSpace(sessionID)
	id = strings.TrimSpace(id)
	if sessionID == "" || id == "" {
		return Artifact{}, ErrArtifactNotFound
	}
	var a Artifact
	err := s.db.QueryRowContext(ctx, `
SELECT `+artifactColumns+`
  FROM fb_artifacts
 WHERE id=? AND session_id=? AND agent_id=?`, id, sessionID, s.AgentID()).Scan(&a.ID, &a.AgentID, &a.SessionID, &a.RunID, &a.ToolCallID,
		&a.Path, &a.RelPath, &a.Name, &a.Title, &a.Description, &a.MIME, &a.Kind, &a.Preview,
		&a.Size, &a.MTimeNS, &a.SHA256, &a.Origin, &a.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Artifact{}, ErrArtifactNotFound
	}
	if err != nil {
		return Artifact{}, err
	}
	return a, nil
}

// ListArtifactsPage returns one page of a conversation's delivered files, in
// delivery order, and how many rows the conversation holds, so a list and its
// pager come from one round trip. An offset past the end reads the last page
// rather than an empty one — the paging contract every list endpoint follows.
func (s *SessionStore) ListArtifactsPage(ctx context.Context, sessionID string, limit, offset int) ([]Artifact, int, error) {
	if s == nil || s.db == nil {
		return []Artifact{}, 0, nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return []Artifact{}, 0, nil
	}
	limit, offset = projectPageWindow(limit, offset)
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM fb_artifacts WHERE session_id=? AND agent_id=?`,
		sessionID, s.AgentID()).Scan(&total); err != nil {
		return nil, 0, err
	}
	if offset >= total {
		offset = max(total-1, 0) / limit * limit
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT `+artifactColumns+`
  FROM fb_artifacts
 WHERE session_id=? AND agent_id=?
 ORDER BY created_at ASC, rowid ASC
 LIMIT ? OFFSET ?`, sessionID, s.AgentID(), limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Artifact{}
	for rows.Next() {
		a, err := scanArtifact(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, a)
	}
	return out, total, rows.Err()
}

// ListArtifactsForRun returns every file one run delivered, in delivery
// order. It is what a run's end carries: the run's structured result. An
// empty run id reads as no rows.
func (s *SessionStore) ListArtifactsForRun(ctx context.Context, runID string) ([]Artifact, error) {
	if s == nil || s.db == nil {
		return []Artifact{}, nil
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return []Artifact{}, nil
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT `+artifactColumns+`
  FROM fb_artifacts
 WHERE run_id=? AND agent_id=?
 ORDER BY created_at, rowid`, runID, s.AgentID())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Artifact{}
	for rows.Next() {
		a, err := scanArtifact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// scanArtifact reads one row in artifactColumns order.
func scanArtifact(rows *sql.Rows) (Artifact, error) {
	var a Artifact
	err := rows.Scan(&a.ID, &a.AgentID, &a.SessionID, &a.RunID, &a.ToolCallID,
		&a.Path, &a.RelPath, &a.Name, &a.Title, &a.Description, &a.MIME, &a.Kind, &a.Preview,
		&a.Size, &a.MTimeNS, &a.SHA256, &a.Origin, &a.CreatedAt)
	return a, err
}
