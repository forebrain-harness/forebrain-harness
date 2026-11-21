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

	"github.com/forebrain-harness/forebrain-harness/pkg/state/dbx"
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

// The artifact statements. Each SQLite arm is the text this file carried
// before multi-database support. PostgreSQL numbers its parameters, and the
// other two engines order same-timestamp artifacts by the explicit row_id
// column where SQLite uses its rowid.
var (
	artifactInsertSQL = dbx.SQL{
		SQLite: `
INSERT INTO fb_artifacts (id, agent_id, session_id, run_id, tool_call_id, path, rel_path,
  name, title, description, mime, kind, preview, size, mtime_ns, sha256, origin, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		MySQL: `
INSERT INTO fb_artifacts (id, agent_id, session_id, run_id, tool_call_id, path, rel_path,
  name, title, description, mime, kind, preview, size, mtime_ns, sha256, origin, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		Postgres: `
INSERT INTO fb_artifacts (id, agent_id, session_id, run_id, tool_call_id, path, rel_path,
  name, title, description, mime, kind, preview, size, mtime_ns, sha256, origin, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)`,
	}
	artifactSelectSQL = dbx.SQL{
		SQLite: `
SELECT ` + artifactColumns + `
  FROM fb_artifacts
 WHERE id=? AND session_id=? AND agent_id=?`,
		MySQL: `
SELECT ` + artifactColumns + `
  FROM fb_artifacts
 WHERE id=? AND session_id=? AND agent_id=?`,
		Postgres: `
SELECT ` + artifactColumns + `
  FROM fb_artifacts
 WHERE id=$1 AND session_id=$2 AND agent_id=$3`,
	}
	artifactCountSQL = dbx.SQL{
		SQLite:   `SELECT COUNT(*) FROM fb_artifacts WHERE session_id=? AND agent_id=?`,
		MySQL:    `SELECT COUNT(*) FROM fb_artifacts WHERE session_id=? AND agent_id=?`,
		Postgres: `SELECT COUNT(*) FROM fb_artifacts WHERE session_id=$1 AND agent_id=$2`,
	}
	artifactPageSQL = dbx.SQL{
		SQLite: `
SELECT ` + artifactColumns + `
  FROM fb_artifacts
 WHERE session_id=? AND agent_id=?
 ORDER BY created_at ASC, rowid ASC
 LIMIT ? OFFSET ?`,
		MySQL: `
SELECT ` + artifactColumns + `
  FROM fb_artifacts
 WHERE session_id=? AND agent_id=?
 ORDER BY created_at ASC, row_id ASC
 LIMIT ? OFFSET ?`,
		Postgres: `
SELECT ` + artifactColumns + `
  FROM fb_artifacts
 WHERE session_id=$1 AND agent_id=$2
 ORDER BY created_at ASC, row_id ASC
 LIMIT $3 OFFSET $4`,
	}
	artifactRunSQL = dbx.SQL{
		SQLite: `
SELECT ` + artifactColumns + `
  FROM fb_artifacts
 WHERE run_id=? AND agent_id=?
 ORDER BY created_at, rowid`,
		MySQL: `
SELECT ` + artifactColumns + `
  FROM fb_artifacts
 WHERE run_id=? AND agent_id=?
 ORDER BY created_at, row_id`,
		Postgres: `
SELECT ` + artifactColumns + `
  FROM fb_artifacts
 WHERE run_id=$1 AND agent_id=$2
 ORDER BY created_at, row_id`,
	}
	// artifactLatestByPathSQL is the turn-end top-up's session-level version
	// check: the newest row a path already has, so a file whose bytes did not
	// change since it was delivered is not delivered twice. The id column is
	// a prefixed uuid, not a counter, so "newest" is delivery order — the
	// same created_at/rowid pair every artifact list is ordered by.
	artifactLatestByPathSQL = dbx.SQL{
		SQLite: `
SELECT ` + artifactColumns + `
  FROM fb_artifacts
 WHERE session_id=? AND agent_id=? AND path=?
 ORDER BY created_at DESC, rowid DESC
 LIMIT 1`,
		MySQL: `
SELECT ` + artifactColumns + `
  FROM fb_artifacts
 WHERE session_id=? AND agent_id=? AND path=?
 ORDER BY created_at DESC, row_id DESC
 LIMIT 1`,
		Postgres: `
SELECT ` + artifactColumns + `
  FROM fb_artifacts
 WHERE session_id=$1 AND agent_id=$2 AND path=$3
 ORDER BY created_at DESC, row_id DESC
 LIMIT 1`,
	}
)

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
	res, err := dbx.Exec(ctx, s.db, artifactInsertSQL.For(dbx.DialectOf(s.db)),
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
	err := s.db.QueryRowContext(ctx, artifactSelectSQL.For(dbx.DialectOf(s.db)), id, sessionID, s.AgentID()).Scan(&a.ID, &a.AgentID, &a.SessionID, &a.RunID, &a.ToolCallID,
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
	if err := s.db.QueryRowContext(ctx, artifactCountSQL.For(dbx.DialectOf(s.db)),
		sessionID, s.AgentID()).Scan(&total); err != nil {
		return nil, 0, err
	}
	if offset >= total {
		offset = max(total-1, 0) / limit * limit
	}
	rows, err := s.db.QueryContext(ctx, artifactPageSQL.For(dbx.DialectOf(s.db)), sessionID, s.AgentID(), limit, offset)
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
	rows, err := s.db.QueryContext(ctx, artifactRunSQL.For(dbx.DialectOf(s.db)), runID, s.AgentID())
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

// LatestArtifactByPath is the newest row a path already has in this
// conversation: the version check the batch outputs scan and the coding
// turn-end top-up make before registering a path again, so unchanged bytes are
// not re-delivered under a new run. A path never delivered reads as found=false.
func (s *SessionStore) LatestArtifactByPath(ctx context.Context, sessionID, path string) (Artifact, bool, error) {
	if s == nil || s.db == nil {
		return Artifact{}, false, nil
	}
	sessionID = strings.TrimSpace(sessionID)
	path = strings.TrimSpace(path)
	if sessionID == "" || path == "" {
		return Artifact{}, false, nil
	}
	var a Artifact
	err := s.db.QueryRowContext(ctx, artifactLatestByPathSQL.For(dbx.DialectOf(s.db)),
		sessionID, s.AgentID(), path).Scan(&a.ID, &a.AgentID, &a.SessionID, &a.RunID, &a.ToolCallID,
		&a.Path, &a.RelPath, &a.Name, &a.Title, &a.Description, &a.MIME, &a.Kind, &a.Preview,
		&a.Size, &a.MTimeNS, &a.SHA256, &a.Origin, &a.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Artifact{}, false, nil
	}
	if err != nil {
		return Artifact{}, false, err
	}
	return a, true, nil
}

// SessionProjectScope resolves the delivery scope one conversation froze at
// birth: the project type it belongs to and the root of that project (empty
// for a conversation with no project). It is what the turn-end deliverable
// top-up anchors containment to, so a write outside the project is never
// registered as that conversation's deliverable.
func (s *SessionStore) SessionProjectScope(ctx context.Context, sessionID string) (projectType, projectRoot string, err error) {
	if s == nil || s.db == nil {
		return ProjectTypeCoding, "", nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ProjectTypeCoding, "", nil
	}
	if err := s.db.QueryRowContext(ctx, projectSessionTypeSQL.For(dbx.DialectOf(s.db)),
		sessionID, s.AgentID()).Scan(&projectType); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ProjectTypeCoding, "", nil
		}
		return "", "", err
	}
	if err := s.db.QueryRowContext(ctx, fileProjectRootSQL.For(dbx.DialectOf(s.db)),
		sessionID).Scan(&projectRoot); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// A conversation with no project (a primary-agent workshop task)
			// names no root to deliver inside.
			return NormalizeProjectType(projectType), "", nil
		}
		return "", "", err
	}
	return NormalizeProjectType(projectType), strings.TrimSpace(projectRoot), nil
}
