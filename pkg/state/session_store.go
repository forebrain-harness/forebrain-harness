package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

type SessionStore struct {
	db           *sql.DB
	defaultsMu   sync.RWMutex
	memoryMode   string
	memorySource string
	cwd          string
	gitBranch    string

	agentMu sync.RWMutex
	agentID string
}

var lastInsertID = func(res sql.Result) (int64, error) {
	return res.LastInsertId()
}

// New opens the session store on behalf of one primary agent. agentID is
// stamped on every session the store creates, which is what lets the memory
// pipeline tell whose conversations it may consolidate; it is a constructor
// argument rather than a setter so a surface cannot start recording sessions
// before it has resolved the tenant they belong to.
func NewSessionStore(db *sql.DB, agentID string) *SessionStore {
	if db == nil {
		return nil
	}
	return &SessionStore{db: db, agentID: strings.TrimSpace(agentID)}
}

// BindPrimaryAgent re-points the store at the tenant that is now active, so
// sessions opened after a primary-agent switch are recorded against it.
func (s *SessionStore) BindPrimaryAgent(agentID string) {
	if s == nil {
		return
	}
	s.agentMu.Lock()
	defer s.agentMu.Unlock()
	s.agentID = strings.TrimSpace(agentID)
}

// AgentID returns the primary agent new sessions are recorded against.
func (s *SessionStore) AgentID() string {
	if s == nil {
		return ""
	}
	s.agentMu.RLock()
	defer s.agentMu.RUnlock()
	return s.agentID
}

func (s *SessionStore) DB() *sql.DB {
	if s == nil {
		return nil
	}
	return s.db
}

// SessionBirth is what a session is born with and keeps: the directory it
// is about. The memory pipeline files the session's memories under that
// directory's project, so it is decided by whoever creates the session —
// a project's session is born in the project root — and never by a
// default some other request changed. A zero Cwd is the process's launch
// directory, the default every ordinary session is born in.
type SessionBirth struct {
	Cwd       string
	GitBranch string
	// Source is what the session is for (SessionSource*); born with it,
	// never changed by later writes.
	Source string
}

// ConfigureMemoryDefaults stamps the process-wide values a session row is
// created with when its creator gave it no directory of its own.
//
// cwd is the session's project identity: the memory pipeline resolves a
// thread's project scope from it (memory.ProjectScopeForCwd), so it must be the
// directory the conversation is about. It is never the agent's workspace root:
// that is Forebrain Harness's own state directory, not a project, and filing a
// session under it writes every memory the session produces into a scope no
// session ever reads back.
//
// It is called once at process startup by the composition root and names the
// launch directory. A session that is about some other directory is born with
// that directory through EnsureAt, not by rewriting this default; a caller
// that merely reacts to a configuration change uses SetMemoryMode instead.
func (s *SessionStore) ConfigureMemoryDefaults(memoryMode, memorySource, cwd, gitBranch string) {
	if s == nil {
		return
	}
	s.defaultsMu.Lock()
	defer s.defaultsMu.Unlock()
	s.memoryMode = strings.TrimSpace(memoryMode)
	s.memorySource = strings.TrimSpace(memorySource)
	s.cwd = strings.TrimSpace(cwd)
	s.gitBranch = strings.TrimSpace(gitBranch)
}

// SetMemoryMode updates only the memory mode new session rows are stamped with.
//
// A configuration reload can turn memory on or off, but it cannot move the
// session to another project. Re-supplying the identity fields on a reload is
// what let a reload site pass a value it did not own, so the mode is the only
// thing this exposes.
func (s *SessionStore) SetMemoryMode(memoryMode string) {
	if s == nil {
		return
	}
	s.defaultsMu.Lock()
	defer s.defaultsMu.Unlock()
	s.memoryMode = strings.TrimSpace(memoryMode)
}

func (s *SessionStore) Ensure(ctx context.Context, id string, title string) error {
	if s == nil || s.db == nil {
		return nil
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	return s.ensureSession(ctx, s.db, id, title, SessionBirth{})
}

// EnsureAt opens a session born with the identity its creator chose: the row's
// cwd and git_branch come from birth and are written only at creation, exactly
// as in Ensure.
func (s *SessionStore) EnsureAt(ctx context.Context, id, title string, birth SessionBirth) error {
	if s == nil || s.db == nil {
		return nil
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	return s.ensureSession(ctx, s.db, id, title, birth)
}

// SetSessionSource marks what a session is for. The value set is a gateway
// whitelist decision; the store only writes it for a session this agent owns.
func (s *SessionStore) SetSessionSource(ctx context.Context, sessionID, source string) error {
	if s == nil || s.db == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return fmt.Errorf("session id required")
	}
	if err := s.requireOwned(ctx, s.db, sessionID); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE fb_sessions SET source=?, updated_at=updated_at WHERE id=? AND agent_id=?`,
		strings.TrimSpace(source), sessionID, s.AgentID())
	if err != nil {
		return err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return fmt.Errorf("%w: %s", ErrSessionNotOwned, sessionID)
	}
	return nil
}

// ensureSession is the one session upsert: it creates the row (owner recorded
// at birth) or, on conflict, updates updated_at for this owner only. Sharing
// it is what lets every append path run it inside its own transaction.
func (s *SessionStore) ensureSession(ctx context.Context, q dbtx, id string, title string, birth SessionBirth) error {
	now := time.Now().Unix()
	// The defaults are read as one snapshot under the lock: an HTTP request
	// creating a project session races a configuration reload, so neither
	// read may see a half-updated set.
	s.defaultsMu.RLock()
	mode := strings.TrimSpace(s.memoryMode)
	source := strings.TrimSpace(s.memorySource)
	cwd := strings.TrimSpace(s.cwd)
	branch := strings.TrimSpace(s.gitBranch)
	s.defaultsMu.RUnlock()
	if mode == "" {
		mode = "disabled"
	}
	// Cwd and GitBranch are a pair. A session born with a directory carries
	// that directory's branch too; a session born without one takes both
	// process defaults — never the given directory with another request's
	// branch, nor the reverse.
	if strings.TrimSpace(birth.Cwd) != "" {
		cwd = strings.TrimSpace(birth.Cwd)
		branch = strings.TrimSpace(birth.GitBranch)
	}
	// agent_id is written when the row is created and never afterwards: a
	// session's owning primary agent is fixed at birth. The conflict clause
	// updates only rows this agent owns, so touching another agent's
	// conversation fails here instead of quietly writing into it — every append
	// path runs this first, which is what keeps writes inside the boundary.
	// Source is the same kind of fact, so it too is written at birth only.
	res, err := q.ExecContext(ctx,
		`INSERT INTO fb_sessions(id, title, updated_at, created_at, memory_mode, memory_source, cwd, git_branch, agent_id, source)
		VALUES(?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET updated_at=excluded.updated_at
		WHERE fb_sessions.agent_id=excluded.agent_id`,
		id, title, now, now, mode, source, cwd, branch, s.AgentID(), strings.TrimSpace(birth.Source))
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("%w: %s", ErrSessionNotOwned, id)
	}
	return nil
}

// ErrSessionNotOwned reports an attempt to reach a conversation that belongs to
// another primary agent.
var ErrSessionNotOwned = errors.New("session belongs to another primary agent")

// requireOwned is the gate every by-id read and write passes through. A primary
// agent is a tenant, so its conversations are tenant data: listing hides other
// agents' sessions, and this stops an id learned some other way — resumed from
// a command, posted by a client — from reaching one.
//
// An id with no session row is allowed: there is no conversation to protect yet,
// and callers read empty history or create the session under their own name.
func (s *SessionStore) requireOwned(ctx context.Context, q dbtx, sessionID string) error {
	if s == nil || s.db == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	var owner string
	err := q.QueryRowContext(ctx, `SELECT agent_id FROM fb_sessions WHERE id=?`, sessionID).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if strings.TrimSpace(owner) != s.AgentID() {
		return fmt.Errorf("%w: %s", ErrSessionNotOwned, sessionID)
	}
	return nil
}

func (s *SessionStore) HasSession(ctx context.Context, id string) (bool, error) {
	if s == nil || s.db == nil {
		return false, nil
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return false, nil
	}
	// Only this agent's conversations exist as far as callers are concerned:
	// answering "yes" for another agent's session is what lets a resume reach it.
	var exists int
	err := s.db.QueryRowContext(ctx, `
SELECT CASE WHEN EXISTS(
	SELECT 1 FROM fb_sessions WHERE id=? AND agent_id=?
) THEN 1 ELSE 0 END`,
		id, s.AgentID()).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists == 1, nil
}

// sessionLastActiveChunk caps one query's IN list. SQLite's default bound
// parameter limit is 999; staying well under it keeps one caller's id list
// working regardless of how the install tuned the driver.
const sessionLastActiveChunk = 400

// LastActiveByIDs returns the last-updated timestamp of each id that has a
// session row, keyed by id. Ids with no row are absent from the map: to the
// caller an absent conversation and a never-created one read the same, and
// which of the two it is the database cannot say either.
//
// The query is by exact id in bounded chunks — never a scan of the session
// table — so a pool asking about the conversations it holds costs work
// proportional to what it holds, not to how many sessions exist.
func (s *SessionStore) LastActiveByIDs(ctx context.Context, ids []string) (map[string]int64, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	cleaned := make([]string, 0, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			cleaned = append(cleaned, id)
		}
	}
	out := make(map[string]int64, len(cleaned))
	for start := 0; start < len(cleaned); start += sessionLastActiveChunk {
		end := start + sessionLastActiveChunk
		if end > len(cleaned) {
			end = len(cleaned)
		}
		chunk := cleaned[start:end]
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
		args := make([]any, 0, len(chunk)+1)
		for _, id := range chunk {
			args = append(args, id)
		}
		args = append(args, s.AgentID())
		rows, err := s.db.QueryContext(ctx,
			`SELECT id, updated_at FROM fb_sessions WHERE id IN (`+placeholders+`) AND agent_id=?`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			var updatedAt int64
			if err := rows.Scan(&id, &updatedAt); err != nil {
				_ = rows.Close()
				return nil, err
			}
			out[id] = updatedAt
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		_ = rows.Close()
	}
	return out, nil
}

// SessionLeftovers is what deleting sessions leaves outside the database:
// the uploaded files' stored bytes and the spilled tool outputs their
// transcripts point at. The caller removes them once the rows are gone.
// Survived lists the sessions the delete left in place because a live
// primary run still holds them; the caller leaves those entirely alone and
// a later pass settles them.
type SessionLeftovers struct {
	Files      []File
	SpillPaths []string
	Survived   []string
}

// DeleteSessions deletes this agent's sessions with the given ids and every
// row that hangs off them (the foreign keys cascade), in one transaction,
// and returns what they leave on disk. A session another agent owns is not
// touched, and one a live primary run still holds is not touched either:
// the sweep that read it as expired may have raced a conversation coming
// back to life, so the delete re-checks liveness inside the transaction and
// reports what it spared in Survived. A conversation forked from one of
// them keeps its own life: its parent link is cleared, not followed.
func (s *SessionStore) DeleteSessions(ctx context.Context, ids []string) (SessionLeftovers, error) {
	if s == nil || s.db == nil {
		return SessionLeftovers{}, fmt.Errorf("session store unavailable")
	}
	cleaned := make([]string, 0, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			cleaned = append(cleaned, id)
		}
	}
	if len(cleaned) == 0 {
		return SessionLeftovers{}, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SessionLeftovers{}, err
	}
	defer func() { _ = tx.Rollback() }()
	out := SessionLeftovers{}
	agentID := s.AgentID()
	// One clock for the whole transaction: the liveness the spare reads and
	// the liveness the delete vetoes are the same facts, so what was spared
	// here cannot be deleted three statements later.
	liveArgs := liveRunArgs(time.Now())
	for start := 0; start < len(cleaned); start += sessionLastActiveChunk {
		end := start + sessionLastActiveChunk
		if end > len(cleaned) {
			end = len(cleaned)
		}
		chunk := cleaned[start:end]
		survivors, err := deleteSessionsSurvivors(ctx, tx, chunk, agentID, liveArgs)
		if err != nil {
			return SessionLeftovers{}, err
		}
		if len(survivors) > 0 {
			out.Survived = append(out.Survived, survivors...)
			if len(survivors) == len(chunk) {
				continue
			}
			surviving := make(map[string]bool, len(survivors))
			for _, id := range survivors {
				surviving[id] = true
			}
			deletable := make([]string, 0, len(chunk)-len(survivors))
			for _, id := range chunk {
				if !surviving[id] {
					deletable = append(deletable, id)
				}
			}
			chunk = deletable
		}
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
		args := make([]any, 0, len(chunk)+1)
		args = append(args, agentID)
		for _, id := range chunk {
			args = append(args, id)
		}
		files, err := deleteSessionsFiles(ctx, tx, placeholders, args)
		if err != nil {
			return SessionLeftovers{}, err
		}
		out.Files = append(out.Files, files...)
		paths, err := deleteSessionsSpillPaths(ctx, tx, placeholders, args)
		if err != nil {
			return SessionLeftovers{}, err
		}
		out.SpillPaths = append(out.SpillPaths, paths...)
		deleteArgs := make([]any, 0, len(args)+len(liveArgs))
		deleteArgs = append(deleteArgs, args...)
		deleteArgs = append(deleteArgs, liveArgs...)
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM fb_sessions WHERE agent_id = ? AND id IN (`+placeholders+`)
  AND NOT EXISTS (
    SELECT 1 FROM fb_runs rr
    WHERE rr.session_id = fb_sessions.id AND rr.parent_run_id IS NULL
      AND `+fmt.Sprintf(livePrimaryRunCondition, "rr")+`)`, deleteArgs...); err != nil {
			return SessionLeftovers{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return SessionLeftovers{}, err
	}
	return out, nil
}

// deleteSessionsSurvivors returns which of the agent's sessions in ids a
// live primary run still holds. DeleteSessions asks it inside the delete's
// own transaction and with the delete's own clock, so the two never disagree.
func deleteSessionsSurvivors(ctx context.Context, q dbtx, ids []string, agentID string, liveArgs []any) ([]string, error) {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids)+1+len(liveArgs))
	args = append(args, agentID)
	for _, id := range ids {
		args = append(args, id)
	}
	args = append(args, liveArgs...)
	rows, err := q.QueryContext(ctx, `
SELECT id FROM fb_sessions
WHERE agent_id = ? AND id IN (`+placeholders+`)
  AND EXISTS (
    SELECT 1 FROM fb_runs rr
    WHERE rr.session_id = fb_sessions.id AND rr.parent_run_id IS NULL
      AND `+fmt.Sprintf(livePrimaryRunCondition, "rr")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func deleteSessionsFiles(ctx context.Context, q dbtx, placeholders string, args []any) ([]File, error) {
	rows, err := q.QueryContext(ctx, `
SELECT f.id, f.session_id, f.original_name, f.media_type, f.size_bytes, f.sha256,
       f.storage_backend, f.storage_bucket, f.storage_key,
       f.parse_status, f.parsed_text_path, f.parse_error, f.created_at, f.updated_at
FROM fb_files f
JOIN fb_sessions s ON s.id = f.session_id AND s.agent_id = ?
WHERE f.session_id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []File
	for rows.Next() {
		var f File
		if err := rows.Scan(&f.ID, &f.SessionID, &f.OriginalName, &f.MediaType, &f.SizeBytes, &f.SHA256,
			&f.StorageBackend, &f.StorageBucket, &f.StorageKey,
			&f.ParseStatus, &f.ParsedTextPath, &f.ParseError, &f.CreatedAt, &f.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// deleteSessionsSpillPaths reads the paths a session's transcripts point at
// for spilled tool output: the governor records where it stored the full bytes
// in the tool row's metadata, so the files can be found again from the
// database alone once the rows are about to go.
func deleteSessionsSpillPaths(ctx context.Context, q dbtx, placeholders string, args []any) ([]string, error) {
	rows, err := q.QueryContext(ctx, `
SELECT DISTINCT json_extract(m.tool_meta_json, '$.full_path')
FROM fb_messages m
JOIN fb_sessions s ON s.id = m.session_id AND s.agent_id = ?
WHERE m.session_id IN (`+placeholders+`)
  AND json_valid(m.tool_meta_json)
  AND json_extract(m.tool_meta_json, '$.full_path') IS NOT NULL
  AND TRIM(json_extract(m.tool_meta_json, '$.full_path')) <> ''`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *SessionStore) SetTitle(ctx context.Context, id string, title string) error {
	if s == nil || s.db == nil {
		return nil
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = id
	}
	now := time.Now().Unix()
	if err := s.Ensure(ctx, id, title); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE fb_sessions SET title=?, updated_at=? WHERE id=? AND agent_id=?`, title, now, id, s.AgentID())
	return err
}

// SessionTitle returns the session's own title, "" when it has none. A
// session that was never named stores its id as its title; that is not a
// name, so it reads as none.
func (s *SessionStore) SessionTitle(ctx context.Context, id string) (string, error) {
	if s == nil || s.db == nil {
		return "", nil
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return "", nil
	}
	var title string
	err := s.db.QueryRowContext(ctx, `SELECT title FROM fb_sessions WHERE id=? AND agent_id=?`, id, s.AgentID()).Scan(&title)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if title = strings.TrimSpace(title); title == id {
		return "", nil
	}
	return title, nil
}

func (s *SessionStore) SetParentSessionID(ctx context.Context, id string, parentSessionID string) error {
	if s == nil || s.db == nil {
		return nil
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	parentSessionID = strings.TrimSpace(parentSessionID)
	now := time.Now().Unix()
	if err := s.Ensure(ctx, id, id); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE fb_sessions SET parent_session_id=NULLIF(?,''), updated_at=? WHERE id=? AND agent_id=?`, parentSessionID, now, id, s.AgentID())
	return err
}

type Message struct {
	RowID            int64
	RunID            string
	Role             string
	Content          string
	MessageID        string
	PartsJSON        string
	Model            string
	UsageJSON        string
	ToolStepID       string
	ToolMetaJSON     string
	ExecStartedAtMs  int64
	ExecFinishedAtMs int64
	ExecDurationMs   int64
	// Run-level timing, read from fb_runs through the run_id this row carries:
	// the window and worked time of the run that produced the row, shared by
	// every row of that run.
	RunStartedAtMs  int64
	RunFinishedAtMs int64
	RunWorkedMs     int64
	CreatedAt       int64
}

type MessageHit struct {
	MessageID int64   `json:"message_id"`
	SessionID string  `json:"session_id"`
	Role      string  `json:"role"`
	Content   string  `json:"content"`
	CreatedAt int64   `json:"created_at"`
	Score     float64 `json:"score,omitempty"`
}

// What a session is for, stored in fb_sessions.source. It is a different
// fact from memory.SessionSource* (which surface wrote the session, in
// fb_sessions.memory_source).
const (
	SessionSourceConversation = ""         // an ordinary conversation
	SessionSourceWorkshop     = "workshop" // a skill-workshop task
	SessionSourceCron         = "cron"     // one fire of a scheduled task
)

type SessionSummary struct {
	ID        string
	Title     string
	UpdatedAt int64
	ProjectID string
	// Source is what the session is for: SessionSourceConversation,
	// SessionSourceWorkshop, or SessionSourceCron. The chat drawer filters
	// on it.
	Source string
}

func (s *SessionStore) ListChildSessionsRecent(ctx context.Context, parentSessionID string, limit int) ([]SessionSummary, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	parentSessionID = strings.TrimSpace(parentSessionID)
	if parentSessionID == "" {
		return []SessionSummary{}, nil
	}
	if limit <= 0 {
		limit = 80
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, title, updated_at, source
		 FROM fb_sessions
		 WHERE parent_session_id = ? AND agent_id = ?
		 ORDER BY updated_at DESC
		 LIMIT ?`,
		parentSessionID, s.AgentID(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]SessionSummary, 0, limit)
	for rows.Next() {
		var r SessionSummary
		if err := rows.Scan(&r.ID, &r.Title, &r.UpdatedAt, &r.Source); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListSessionsRecent is what a person may want back, newest first: every
// purpose but a scheduled task's fire, which is reached from its task's run
// record instead (decision D3).
func (s *SessionStore) ListSessionsRecent(ctx context.Context, limit int) ([]SessionSummary, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 80
	}
	if limit > 500 {
		limit = 500
	}
	// project_id rides along so a surface can tell a project's sessions from
	// the agent's own conversations without a second lookup per row.
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, title, updated_at, source, COALESCE(project_id, '')
		 FROM fb_sessions
		 WHERE agent_id = ? AND source <> ?
		 ORDER BY updated_at DESC
		 LIMIT ?`,
		s.AgentID(), SessionSourceCron, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionSummary
	for rows.Next() {
		var r SessionSummary
		if err := rows.Scan(&r.ID, &r.Title, &r.UpdatedAt, &r.Source, &r.ProjectID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListSessionsOfSource returns the agent's own sessions — not bound to a
// project — that are for one purpose, newest first. The filter is in the
// query, so sessions of another purpose never take a row of the limit.
func (s *SessionStore) ListSessionsOfSource(ctx context.Context, source string, limit int) ([]SessionSummary, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	source = strings.TrimSpace(source)
	if limit <= 0 {
		limit = 80
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, title, updated_at, source
		 FROM fb_sessions
		 WHERE agent_id = ? AND project_id IS NULL AND source = ?
		 ORDER BY updated_at DESC, id ASC
		 LIMIT ?`,
		s.AgentID(), source, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]SessionSummary, 0, limit)
	for rows.Next() {
		var r SessionSummary
		if err := rows.Scan(&r.ID, &r.Title, &r.UpdatedAt, &r.Source); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListSessionsForProject returns one page of the conversations bound to one
// project, newest first. Paged for the same reason every session list is: a
// long history must not be loaded whole. Only conversations are listed: a
// project's scheduled-task fires are reached from the project's
// scheduled-tasks tab.
func (s *SessionStore) ListSessionsForProject(ctx context.Context, projectID string, limit, offset int) ([]SessionSummary, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return []SessionSummary{}, nil
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, title, updated_at, project_id
		 FROM fb_sessions
		 WHERE agent_id = ? AND project_id = ? AND source = ?
		 ORDER BY updated_at DESC, id
		 LIMIT ? OFFSET ?`,
		s.AgentID(), projectID, SessionSourceConversation, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SessionSummary{}
	for rows.Next() {
		var item SessionSummary
		if err := rows.Scan(&item.ID, &item.Title, &item.UpdatedAt, &item.ProjectID); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// ListSessionsRecentPaged returns one page of the agent's sessions ordered by
// most recently updated. Callers browsing a long history must page through
// with limit/offset instead of requesting the whole list: the picker keeps
// only the pages the user actually visited in memory. Ordering adds id as a
// deterministic tie-breaker so equal updated_at values cannot shuffle rows
// between pages. The list is what a person may want back, like
// ListSessionsRecent: a scheduled task's fire is reached from its task's run
// record instead (decision D3).
func (s *SessionStore) ListSessionsRecentPaged(ctx context.Context, limit, offset int) ([]SessionSummary, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 500 {
		limit = 500
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, title, updated_at, source
		 FROM fb_sessions
		 WHERE agent_id = ? AND source <> ?
		 ORDER BY updated_at DESC, id ASC
		 LIMIT ? OFFSET ?`,
		s.AgentID(), SessionSourceCron, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]SessionSummary, 0, limit)
	for rows.Next() {
		var r SessionSummary
		if err := rows.Scan(&r.ID, &r.Title, &r.UpdatedAt, &r.Source); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *SessionStore) SessionIDWithLatestMessage(ctx context.Context) (string, error) {
	if s == nil || s.db == nil {
		return "", nil
	}
	// Walks the agent's sessions newest-first and returns the first one that
	// has a visible message: an index-range answer to what the message row
	// scan used to compute over every row the tenant ever wrote.
	var sid sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT s.id FROM fb_sessions s
		 WHERE s.agent_id = ?
		   AND EXISTS (SELECT 1 FROM fb_messages m WHERE m.session_id = s.id AND m.visibility = 'visible')
		 ORDER BY s.updated_at DESC, s.id DESC
		 LIMIT 1`, s.AgentID()).Scan(&sid)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	out := strings.TrimSpace(sid.String)
	return out, nil
}

type TranscriptTurn struct {
	ID         int64
	Role       string
	Content    string
	MessageID  string
	PartsJSON  string
	ToolStepID string
	CreatedAt  int64
}

func (s *SessionStore) ListTranscriptRecent(ctx context.Context, sessionID string, limit int) ([]TranscriptTurn, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	if err := s.requireOwned(ctx, s.db, sessionID); err != nil {
		return nil, err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return []TranscriptTurn{}, nil
	}
	floor, boundaryID, useProjection, err := s.modelContextFloor(ctx, s.db, sessionID)
	if err != nil {
		return nil, err
	}
	if useProjection {
		projected, ok, err := s.listProjectedMessageRows(ctx, s.db, sessionID, boundaryID, limit)
		if err != nil {
			return nil, err
		}
		if ok {
			out := make([]TranscriptTurn, 0, len(projected))
			for _, row := range projected {
				out = append(out, transcriptTurnFromStoredMessage(row))
			}
			return out, nil
		}
	}
	query := `SELECT id, role, content, message_id, parts, tool_step_id, created_at
		 FROM fb_messages
		 WHERE session_id = ?
		   AND visibility = 'visible'`
	args := []any{sessionID}
	if useProjection && boundaryID > 0 {
		query += ` AND id >= ?`
		args = append(args, boundaryID)
	} else if floor > 0 {
		query += ` AND id > ?`
		args = append(args, floor)
	}
	query += ` ORDER BY id DESC`
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var rev []TranscriptTurn
	for rows.Next() {
		var t TranscriptTurn
		if err := rows.Scan(&t.ID, &t.Role, &t.Content, &t.MessageID, &t.PartsJSON, &t.ToolStepID, &t.CreatedAt); err != nil {
			return nil, err
		}
		rev = append(rev, t)
	}
	out := make([]TranscriptTurn, 0, len(rev))
	for i := len(rev) - 1; i >= 0; i-- {
		out = append(out, rev[i])
	}
	return out, rows.Err()
}

func (s *SessionStore) ListRecentMessages(ctx context.Context, sessionID string, limit int) ([]Message, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	if err := s.requireOwned(ctx, s.db, sessionID); err != nil {
		return nil, err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return []Message{}, nil
	}
	floor, boundaryID, useProjection, err := s.modelContextFloor(ctx, s.db, sessionID)
	if err != nil {
		return nil, err
	}
	if useProjection {
		projected, ok, err := s.listProjectedMessageRows(ctx, s.db, sessionID, boundaryID, limit)
		if err != nil {
			return nil, err
		}
		if ok {
			out := make([]Message, 0, len(projected))
			for _, row := range projected {
				out = append(out, messageFromStored(row))
			}
			return out, nil
		}
	}
	query := `SELECT ` + messageColumnsJoined + `
		 FROM fb_messages m LEFT JOIN fb_runs r ON r.id = m.run_id
		 WHERE m.session_id = ?
		   AND m.visibility = 'visible'`
	args := []any{sessionID}
	if useProjection && boundaryID > 0 {
		query += ` AND m.id >= ?`
		args = append(args, boundaryID)
	} else if floor > 0 {
		query += ` AND m.id > ?`
		args = append(args, floor)
	}
	query += ` ORDER BY m.id DESC`
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var rev []Message
	for rows.Next() {
		row, err := scanMessageWithRun(rows)
		if err != nil {
			return nil, err
		}
		rev = append(rev, messageFromStored(row))
	}
	out := make([]Message, 0, len(rev))
	for i := len(rev) - 1; i >= 0; i-- {
		out = append(out, rev[i])
	}
	return out, rows.Err()
}

type storedMessage struct {
	ID               int64
	RunID            string
	Role             string
	Content          string
	MessageID        string
	PartsJSON        string
	Model            string
	CreatedAt        int64
	UsageJSON        string
	ToolStepID       string
	ToolMetaJSON     string
	ExecStartedAtMs  int64
	ExecFinishedAtMs int64
	ExecDurationMs   int64
	// Run timing, read through the LEFT JOIN on fb_runs; zero when the row's
	// run never completed a timed turn.
	RunStartedAtMs  int64
	RunFinishedAtMs int64
	RunWorkedMs     int64
}

// messageColumns is the one stored-transcript column list; scanMessage is its
// one reader. run_id is the only nullable column in it and reads as "".
const messageColumns = `id, run_id, role, content, message_id, parts, model, created_at, usage_json, tool_step_id, tool_meta_json, exec_started_at_ms, exec_finished_at_ms, exec_duration_ms`

// messageColumnsJoined is messageColumns plus the run's timing tail, every
// column qualified for the `fb_messages m LEFT JOIN fb_runs r` shape every
// message reader uses: a row reports its run's clock without storing a copy.
const messageColumnsJoined = `m.id, m.run_id, m.role, m.content, m.message_id, m.parts, m.model, m.created_at, m.usage_json, m.tool_step_id, m.tool_meta_json, m.exec_started_at_ms, m.exec_finished_at_ms, m.exec_duration_ms, r.started_at_ms, r.finished_at_ms, r.worked_ms`

// scanMessageWithRun scans the joined form and fills the run timing tail.
func scanMessageWithRun(scanner interface{ Scan(...any) error }) (storedMessage, error) {
	var row storedMessage
	var runID sql.NullString
	var execStart, execFinish, execDuration sql.NullInt64
	// The run tail is NULL whenever the row has no run (LEFT JOIN misses) or
	// that run was never timed; both read as zero.
	var runStart, runFinish, runWorked sql.NullInt64
	if err := scanner.Scan(&row.ID, &runID, &row.Role, &row.Content, &row.MessageID, &row.PartsJSON, &row.Model, &row.CreatedAt, &row.UsageJSON, &row.ToolStepID, &row.ToolMetaJSON, &execStart, &execFinish, &execDuration,
		&runStart, &runFinish, &runWorked); err != nil {
		return storedMessage{}, err
	}
	row.RunID = runID.String
	exec := execTimingFromColumns(execStart, execFinish, execDuration)
	row.ExecStartedAtMs, row.ExecFinishedAtMs, row.ExecDurationMs = exec.StartedAtMs, exec.FinishedAtMs, exec.DurationMs
	row.RunStartedAtMs, row.RunFinishedAtMs, row.RunWorkedMs = runStart.Int64, runFinish.Int64, runWorked.Int64
	return row, nil
}

func messageFromStored(row storedMessage) Message {
	return Message{
		RowID:            row.ID,
		RunID:            row.RunID,
		Role:             row.Role,
		Content:          row.Content,
		MessageID:        row.MessageID,
		PartsJSON:        row.PartsJSON,
		Model:            row.Model,
		UsageJSON:        row.UsageJSON,
		ToolStepID:       row.ToolStepID,
		ToolMetaJSON:     row.ToolMetaJSON,
		ExecStartedAtMs:  row.ExecStartedAtMs,
		ExecFinishedAtMs: row.ExecFinishedAtMs,
		ExecDurationMs:   row.ExecDurationMs,
		RunStartedAtMs:   row.RunStartedAtMs,
		RunFinishedAtMs:  row.RunFinishedAtMs,
		RunWorkedMs:      row.RunWorkedMs,
		CreatedAt:        row.CreatedAt,
	}
}

func transcriptTurnFromStoredMessage(row storedMessage) TranscriptTurn {
	return TranscriptTurn{
		ID:         row.ID,
		Role:       row.Role,
		Content:    row.Content,
		MessageID:  row.MessageID,
		PartsJSON:  row.PartsJSON,
		ToolStepID: row.ToolStepID,
		CreatedAt:  row.CreatedAt,
	}
}

func (s *SessionStore) ListTranscriptMessages(ctx context.Context, sessionID string, limit int) ([]llm.Message, error) {
	entries, err := s.ListTranscriptMessagesWithRefs(ctx, sessionID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]llm.Message, len(entries))
	for i, e := range entries {
		out[i] = e.Message
	}
	return out, nil
}

// ListMemoryTranscriptMessages returns the append-only transcript without
// applying model-context compaction or clear cursors. Memory extraction needs
// the complete thread evidence, not only the current inference window.
func (s *SessionStore) ListMemoryTranscriptMessages(ctx context.Context, sessionID string, limit int) ([]llm.Message, error) {
	rows, err := s.listAllStoredMessages(ctx, sessionID, limit)
	if err != nil {
		return nil, err
	}
	return messagesFromStoredRows(rows), nil
}

func messagesFromStoredRows(rows []storedMessage) []llm.Message {
	out := make([]llm.Message, 0, len(rows))
	for _, row := range rows {
		if _, isBoundary := ParseCompactBoundaryPart(row.PartsJSON); isBoundary {
			continue
		}
		message, ok := ParseMessage(row.Role, row.Content, row.PartsJSON)
		if ok {
			out = append(out, message)
		}
	}
	return out
}

func (s *SessionStore) listTranscriptStoredMessages(ctx context.Context, q dbtx, sessionID string, limit int) ([]storedMessage, error) {
	if err := s.requireOwned(ctx, q, sessionID); err != nil {
		return nil, err
	}
	if s == nil || s.db == nil {
		return nil, nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return []storedMessage{}, nil
	}
	if limit <= 0 {
		limit = 400
	}
	if limit > 5000 {
		limit = 5000
	}
	floor, boundaryID, useProjection, err := s.modelContextFloor(ctx, q, sessionID)
	if err != nil {
		return nil, err
	}
	if useProjection {
		projected, ok, err := s.listProjectedMessageRows(ctx, q, sessionID, boundaryID, limit)
		if err != nil {
			return nil, err
		}
		if ok {
			return projected, nil
		}
	}
	// No active compaction projection: read the transcript after the model
	// context floor. floor==0 (never cleared, never compacted) reads all rows.
	return s.listStoredMessagesSince(ctx, q, sessionID, floor, limit)
}

// listAllStoredMessages reads the full stored transcript in chronological order,
// ignoring any compaction boundary projection AND any context-reset cursor. This
// is the raw on-disk history (what the user actually saw) used by resume replay,
// as distinct from the compacted/cleared model-context window.
func (s *SessionStore) listAllStoredMessages(ctx context.Context, sessionID string, limit int) ([]storedMessage, error) {
	if err := s.requireOwned(ctx, s.db, sessionID); err != nil {
		return nil, err
	}
	return s.listStoredMessagesSince(ctx, s.db, sessionID, 0, limit)
}

// LastTranscriptRowID is the id of the newest visible transcript row of the
// session, 0 when it has none: the anchor a display event recorded after it is
// drawn after.
func (s *SessionStore) LastTranscriptRowID(ctx context.Context, sessionID string) (int64, error) {
	if s == nil || s.db == nil {
		return 0, nil
	}
	if err := s.requireOwned(ctx, s.db, sessionID); err != nil {
		return 0, err
	}
	// MAX over a session with no visible rows is NULL: there is no anchor yet.
	var id sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		SELECT MAX(id) FROM fb_messages
		WHERE session_id = ? AND visibility = 'visible'`, strings.TrimSpace(sessionID)).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id.Int64, nil
}

// listStoredMessagesSince reads stored transcript rows with id > sinceRowID in
// chronological order. sinceRowID==0 returns the entire transcript.
func (s *SessionStore) listStoredMessagesSince(ctx context.Context, q dbtx, sessionID string, sinceRowID int64, limit int) ([]storedMessage, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return []storedMessage{}, nil
	}
	query := `SELECT ` + messageColumnsJoined + `
		FROM fb_messages m LEFT JOIN fb_runs r ON r.id = m.run_id
		WHERE m.session_id = ?
		  AND m.visibility = 'visible'`
	args := []any{sessionID}
	if sinceRowID > 0 {
		query += ` AND m.id > ?`
		args = append(args, sinceRowID)
	}
	query += ` ORDER BY m.id DESC`
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	rev := make([]storedMessage, 0, 32)
	for rows.Next() {
		row, err := scanMessageWithRun(rows)
		if err != nil {
			return nil, err
		}
		rev = append(rev, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]storedMessage, 0, len(rev))
	for i := len(rev) - 1; i >= 0; i-- {
		out = append(out, rev[i])
	}
	return out, nil
}

// ListAllMessages returns the full stored transcript as Turns in chronological
// order, ignoring any compaction boundary. Resume replay uses this so the user
// sees their entire conversation history to scroll through — not the compacted
// model-context window that ListRecentMessages returns for the next prompt.
func (s *SessionStore) ListAllMessages(ctx context.Context, sessionID string, limit int) ([]Message, error) {
	rows, err := s.listAllStoredMessages(ctx, sessionID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]Message, 0, len(rows))
	for _, row := range rows {
		out = append(out, messageFromStored(row))
	}
	return out, nil
}

func (s *SessionStore) listProjectedMessageRows(ctx context.Context, q dbtx, sessionID string, boundaryID int64, limit int) ([]storedMessage, bool, error) {
	boundaryRow, ok, err := s.loadStoredMessageByRowID(ctx, boundaryID)
	if err != nil || !ok {
		return nil, false, err
	}
	meta, ok := ParseCompactBoundaryPart(boundaryRow.PartsJSON)
	if !ok || len(meta.ReplacementHistory) == 0 {
		return nil, false, nil
	}

	// A compact checkpoint is a replacement, not a synthetic summary followed
	// by selected old rows. Rebuild exactly the persisted replacement history.
	out := make([]storedMessage, 0, len(meta.ReplacementHistory)+8)
	for i, msg := range meta.ReplacementHistory {
		role := strings.TrimSpace(msg.Role)
		if msg.Compaction != nil {
			role = "compaction"
		}
		out = append(out, storedMessage{
			ID:        -int64(len(meta.ReplacementHistory) - i),
			Role:      role,
			Content:   msg.TextContent(),
			PartsJSON: MessagePartsJSON(msg, msg.TextContent()),
			CreatedAt: boundaryRow.CreatedAt,
		})
	}

	futureRows, err := s.listStoredMessagesAfterRowID(ctx, q, sessionID, boundaryID)
	if err != nil {
		return nil, false, err
	}
	for _, row := range futureRows {
		if _, isBoundary := ParseCompactBoundaryPart(row.PartsJSON); isBoundary {
			continue
		}
		// The authoritative summary already lives in ReplacementHistory above.
		// Any summary sitting in the post-boundary rows is stale duplication from
		// an earlier compacted-prefix leak; skipping it here keeps the projected
		// context from stacking handoff summaries and lets already-corrupted
		// sessions recover on the next read.
		if msg, ok := ParseMessage(row.Role, row.Content, row.PartsJSON); ok && IsCompactSummaryMessage(msg) {
			continue
		}
		out = append(out, row)
	}

	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, true, nil
}

func (s *SessionStore) loadStoredMessageByRowID(ctx context.Context, rowID int64) (storedMessage, bool, error) {
	if s == nil || s.db == nil || rowID <= 0 {
		return storedMessage{}, false, nil
	}
	row, err := scanMessageWithRun(s.db.QueryRowContext(ctx, `
		SELECT `+messageColumnsJoined+`
		FROM fb_messages m LEFT JOIN fb_runs r ON r.id = m.run_id
		WHERE m.id = ?`,
		rowID))
	if errors.Is(err, sql.ErrNoRows) {
		return storedMessage{}, false, nil
	}
	if err != nil {
		return storedMessage{}, false, err
	}
	return row, true, nil
}

func (s *SessionStore) listStoredMessagesAfterRowID(ctx context.Context, q dbtx, sessionID string, rowID int64) ([]storedMessage, error) {
	if s == nil || s.db == nil || rowID <= 0 {
		return nil, nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return []storedMessage{}, nil
	}
	rows, err := q.QueryContext(ctx, `
		SELECT `+messageColumnsJoined+`
		FROM fb_messages m LEFT JOIN fb_runs r ON r.id = m.run_id
		WHERE m.session_id = ?
		  AND m.visibility = 'visible'
		  AND m.id > ?
		ORDER BY m.id ASC`,
		sessionID,
		rowID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]storedMessage, 0, 8)
	for rows.Next() {
		row, err := scanMessageWithRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *SessionStore) LatestCompactBoundary(ctx context.Context, sessionID string) (int64, CompactBoundaryPart, error) {
	if s == nil || s.db == nil {
		return 0, CompactBoundaryPart{}, nil
	}
	if err := s.requireOwned(ctx, s.db, sessionID); err != nil {
		return 0, CompactBoundaryPart{}, err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return 0, CompactBoundaryPart{}, nil
	}
	boundaryID, err := s.compactBoundaryRowID(ctx, s.db, sessionID)
	if err != nil || boundaryID <= 0 {
		return boundaryID, CompactBoundaryPart{}, err
	}
	var partsJSON string
	if err := s.db.QueryRowContext(ctx, `SELECT parts FROM fb_messages WHERE id=?`, boundaryID).Scan(&partsJSON); err != nil {
		return 0, CompactBoundaryPart{}, err
	}
	part, ok := ParseCompactBoundaryPart(partsJSON)
	if !ok {
		return boundaryID, CompactBoundaryPart{}, nil
	}
	return boundaryID, part, nil
}

func (s *SessionStore) EnsureInitialWindowID(ctx context.Context, sessionID, candidate string) (string, error) {
	if s == nil || s.db == nil {
		return "", errors.New("nil session store")
	}
	if err := s.requireOwned(ctx, s.db, sessionID); err != nil {
		return "", err
	}
	sessionID, candidate = strings.TrimSpace(sessionID), strings.TrimSpace(candidate)
	if sessionID == "" || candidate == "" {
		return "", errors.New("session id and initial window id are required")
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE fb_sessions SET initial_window_id=? WHERE id=? AND agent_id=? AND initial_window_id=''`, candidate, sessionID, s.AgentID()); err != nil {
		return "", err
	}
	var windowID string
	if err := s.db.QueryRowContext(ctx, `SELECT initial_window_id FROM fb_sessions WHERE id=?`, sessionID).Scan(&windowID); err != nil {
		return "", err
	}
	return strings.TrimSpace(windowID), nil
}

func (s *SessionStore) Append(ctx context.Context, sessionID string, role string, content string) (int64, error) {
	return s.AppendMessageForRun(ctx, sessionID, "", role, content, "", "", messageExecTiming{})
}

// RunTiming is a completed run's clock: the window the surface that ran the
// turn measured, and how long it worked. The surface owns this clock — it is
// the same one the "Worked for" line reports live — and records it once, on
// the run row, when the turn ends. The zero value means "not timed".
type RunTiming struct {
	StartedAt  time.Time
	FinishedAt time.Time
	Worked     time.Duration
}

func (t RunTiming) valid() bool {
	return !t.StartedAt.IsZero() && !t.FinishedAt.IsZero()
}

// StampRunTiming records a run's clock when the run ends — however it ends:
// finished, failed or stopped. Every run closes with a "Worked for" line live,
// and this is what gives a replay that same line back, whether or not the run
// wrote a single row of its own. The surface that measured the run stamps it.
//
// A run ends once, so the first stamp is its clock and a later one changes
// nothing: the stamp written where the surface measured the run precisely
// stands, and a surface's catch-all at the very end of a run only fills in
// for an ending that wrote none. An untimed window (the zero value) changes
// nothing either.
func (s *SessionStore) StampRunTiming(ctx context.Context, runID string, timing RunTiming) error {
	if s == nil || s.db == nil {
		return nil
	}
	runID = strings.TrimSpace(runID)
	if runID == "" || !timing.valid() {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `UPDATE fb_runs SET started_at_ms=?, finished_at_ms=?, worked_ms=?, updated_at=? WHERE id=? AND finished_at_ms IS NULL`,
		timing.StartedAt.UnixMilli(), timing.FinishedAt.UnixMilli(), timing.Worked.Milliseconds(), time.Now().Unix(), runID)
	return err
}

// BindMessageToRun records that a row written before its run existed belongs
// to that run: the user message the terminal stores before the engine creates
// the run it starts. A row already bound to a run keeps it.
func (s *SessionStore) BindMessageToRun(ctx context.Context, sessionID string, rowID int64, runID string) error {
	if s == nil || s.db == nil {
		return nil
	}
	sessionID, runID = strings.TrimSpace(sessionID), strings.TrimSpace(runID)
	if sessionID == "" || runID == "" || rowID <= 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `UPDATE fb_messages SET run_id=? WHERE id=? AND session_id=? AND run_id IS NULL`, runID, rowID, sessionID)
	return err
}

// messageExecTiming is the row's own execution window: a tool call, or a
// shell command the user ran. It becomes the exec_* columns.
type messageExecTiming struct {
	StartedAtMs  int64
	FinishedAtMs int64
	DurationMs   int64
}

// columns renders the window as the three exec_* values. They are one fact —
// a row has its own execution window or it does not — so they are NULL
// together or set together, which is what the table's CHECK holds them to. A
// call that finished inside a millisecond has a zero duration, and a zero
// duration is a duration, not the absence of one.
func (t messageExecTiming) columns() (startedAtMs, finishedAtMs, durationMs any) {
	if t.StartedAtMs == 0 {
		return nil, nil, nil
	}
	return t.StartedAtMs, t.FinishedAtMs, t.DurationMs
}

// execTimingFromColumns reads the exec_* columns back; a row without its own
// execution window reads as the zero value.
func execTimingFromColumns(startedAtMs, finishedAtMs, durationMs sql.NullInt64) messageExecTiming {
	if !startedAtMs.Valid {
		return messageExecTiming{}
	}
	return messageExecTiming{StartedAtMs: startedAtMs.Int64, FinishedAtMs: finishedAtMs.Int64, DurationMs: durationMs.Int64}
}

// MessageExecTiming is the exported form a caller passes to the append
// family: the row's own execution window.
type MessageExecTiming = messageExecTiming

// AppendMessageForRun durably associates a display row with the execution that
// produced it: runID empty for rows outside any run.
func (s *SessionStore) AppendMessageForRun(ctx context.Context, sessionID, runID string, role string, content string, toolStepID string, toolMetaJSON string, exec messageExecTiming) (int64, error) {
	if s == nil || s.db == nil {
		return 0, nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return 0, nil
	}
	if toolMetaJSON != "" && !json.Valid([]byte(toolMetaJSON)) {
		return 0, fmt.Errorf("invalid tool_meta_json")
	}
	return s.appendRow(ctx, messageRow{
		SessionID:    sessionID,
		RunID:        strings.TrimSpace(runID),
		Role:         role,
		Visibility:   messageVisible,
		Content:      content,
		PartsJSON:    "[]",
		CreatedAt:    time.Now().Unix(),
		ToolStepID:   strings.TrimSpace(toolStepID),
		ToolMetaJSON: toolMetaJSON,
		Exec:         exec,
	})
}

// AppendShellCommandTurn records a shell command the user ran directly — the
// `!cmd` row — with the command's own execution window and tool metadata.
func (s *SessionStore) AppendShellCommandTurn(ctx context.Context, sessionID, body, toolMetaJSON string, timing llm.ExecutionTiming) (int64, error) {
	startedMs, finishedMs := timing.StartedAt.UnixMilli(), timing.CompletedAt.UnixMilli()
	if timing.StartedAt.IsZero() || timing.CompletedAt.IsZero() {
		startedMs, finishedMs = 0, 0
	}
	return s.AppendMessageForRun(ctx, sessionID, "", "user", body, "", toolMetaJSON, messageExecTiming{
		StartedAtMs:  startedMs,
		FinishedAtMs: finishedMs,
		DurationMs:   timing.Duration.Milliseconds(),
	})
}

// AppendToolTurnWithMeta records a tool result row with its own execution
// window.
func (s *SessionStore) AppendToolTurnWithMeta(ctx context.Context, sessionID string, role string, content string, toolStepID string, toolMetaJSON string, exec messageExecTiming) (int64, error) {
	return s.AppendMessageForRun(ctx, sessionID, "", role, content, toolStepID, toolMetaJSON, exec)
}

func (s *SessionStore) AppendStructuredMessage(ctx context.Context, sessionID string, role string, content string, messageID string, partsJSON string, model string, usageJSON string, toolStepID string, toolMetaJSON string, exec messageExecTiming) (int64, error) {
	return s.AppendStructuredMessageForRun(ctx, sessionID, "", role, content, messageID, partsJSON, model, usageJSON, toolStepID, toolMetaJSON, exec)
}

// AppendStructuredMessageForRun stores a fully described transcript row: the
// rendered content with its structured parts, the model and usage of the call
// that produced it, and the run it belongs to.
func (s *SessionStore) AppendStructuredMessageForRun(ctx context.Context, sessionID, runID string, role string, content string, messageID string, partsJSON string, model string, usageJSON string, toolStepID string, toolMetaJSON string, exec messageExecTiming) (int64, error) {
	if s == nil || s.db == nil {
		return 0, nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return 0, nil
	}
	messageID = strings.TrimSpace(messageID)
	partsJSON = strings.TrimSpace(partsJSON)
	if partsJSON == "" {
		partsJSON = "[]"
	}
	if !json.Valid([]byte(partsJSON)) {
		return 0, fmt.Errorf("invalid parts json")
	}
	usageJSON = strings.TrimSpace(usageJSON)
	if usageJSON != "" && !json.Valid([]byte(usageJSON)) {
		return 0, fmt.Errorf("invalid usage json")
	}
	if toolMetaJSON != "" && !json.Valid([]byte(toolMetaJSON)) {
		return 0, fmt.Errorf("invalid tool_meta_json")
	}
	return s.appendRow(ctx, messageRow{
		SessionID:    sessionID,
		RunID:        strings.TrimSpace(runID),
		Role:         role,
		Visibility:   messageVisible,
		Content:      content,
		MessageID:    messageID,
		PartsJSON:    partsJSON,
		Model:        strings.TrimSpace(model),
		UsageJSON:    usageJSON,
		CreatedAt:    time.Now().Unix(),
		ToolStepID:   strings.TrimSpace(toolStepID),
		ToolMetaJSON: toolMetaJSON,
		Exec:         exec,
	})
}

// messageRow is one transcript row in the shape fb_messages stores it, so a
// single INSERT covers every column no matter which append path builds it.
type messageRow struct {
	SessionID    string
	RunID        string
	Role         string
	Visibility   string
	Content      string
	MessageID    string
	PartsJSON    string
	Model        string
	UsageJSON    string
	ToolStepID   string
	ToolMetaJSON string
	Exec         messageExecTiming
	CreatedAt    int64
}

// insertMessage is the one INSERT onto fb_messages.
func insertMessage(ctx context.Context, q dbtx, row messageRow) (int64, error) {
	execStarted, execFinished, execDuration := row.Exec.columns()
	res, err := q.ExecContext(ctx,
		`INSERT INTO fb_messages(session_id, run_id, role, visibility, content, parts, message_id, model, usage_json, tool_step_id, tool_meta_json, exec_started_at_ms, exec_finished_at_ms, exec_duration_ms, created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		row.SessionID, nullIfEmpty(row.RunID), row.Role, row.Visibility, row.Content, row.PartsJSON, row.MessageID, row.Model, row.UsageJSON, row.ToolStepID, row.ToolMetaJSON,
		execStarted, execFinished, execDuration, row.CreatedAt)
	if err != nil {
		return 0, err
	}
	return lastInsertID(res)
}

func nullIfEmpty(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// appendRow writes one transcript row and the session bookkeeping around it
// in a single transaction: the session row is ensured (its owner recorded at
// creation, never assigned later), the row lands, and the first visible user
// message names the session by an atomic conditional update — the title flips
// only while it still equals the id, so two racing first messages cannot both
// claim the naming and a title a caller set by hand survives.
func (s *SessionStore) appendRow(ctx context.Context, row messageRow) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	rowID, err := s.appendRowInTx(ctx, tx, row)
	if err != nil {
		return 0, err
	}
	return rowID, tx.Commit()
}

// appendRowInTx is appendRow for a caller that owns the transaction (the
// message-sync reconciler batches its rows into one).
func (s *SessionStore) appendRowInTx(ctx context.Context, q dbtx, row messageRow) (int64, error) {
	if err := s.ensureSession(ctx, q, row.SessionID, row.SessionID, SessionBirth{}); err != nil {
		return 0, err
	}
	rowID, err := insertMessage(ctx, q, row)
	if err != nil {
		return 0, err
	}
	if row.Role == "user" && row.Visibility == messageVisible && strings.TrimSpace(row.Content) != "" {
		if _, err := q.ExecContext(ctx,
			`UPDATE fb_sessions SET title=?, updated_at=? WHERE id=? AND agent_id=? AND title=id`,
			generateSessionTitle(row.Content), time.Now().Unix(), row.SessionID, s.AgentID()); err != nil {
			return 0, err
		}
	}
	return rowID, nil
}

// WithdrawUserTurn hides exactly one owned user row from both display and model
// transcripts and releases the session name that row generated. The row stays on
// disk for audit; repeating a withdrawal is harmless, but a mismatched identity
// never counts as success.
//
// Only this one row changes. Every earlier row keeps its id, its order and its
// bytes, so the next request still matches the provider's cached prompt prefix
// everywhere up to the withdrawn message and forks only at the final message
// block. Nothing here may grow into a rewrite of surrounding history.
func (s *SessionStore) WithdrawUserTurn(ctx context.Context, sessionID string, rowID int64) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("withdraw user turn: session store unavailable")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || rowID <= 0 {
		return fmt.Errorf("withdraw user turn: invalid message identity")
	}
	// Read the display content before hiding the row: it is what named the
	// session, and the release below compares against it.
	var content string
	switch err := s.db.QueryRowContext(ctx,
		`SELECT content FROM fb_messages WHERE id=? AND session_id=? AND role='user'`,
		rowID, sessionID).Scan(&content); {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return err
	}
	moved, err := s.setMessageVisibility(ctx, s.db, rowID, messageWithdrawn,
		[]string{messageVisible, messageWithdrawn}, sessionID, "user")
	if err != nil {
		return err
	}
	if !moved {
		if err := s.requireOwned(ctx, s.db, sessionID); err != nil {
			return err
		}
		return fmt.Errorf("withdraw user turn: user transcript row not found")
	}
	return s.releaseWithdrawnSessionTitle(ctx, sessionID, content)
}

// releaseWithdrawnSessionTitle re-derives the session name after a user row
// leaves the transcript. touchSessionFromTurn names a session from its first
// visible user message, so withdrawing that message would otherwise leave the
// text the user took back naming the session in /resume, in the session list and
// in the terminal window title — permanently, if they never send another
// message. A title that does not match the withdrawn text was set from something
// else and is left alone.
func (s *SessionStore) releaseWithdrawnSessionTitle(ctx context.Context, sessionID string, withdrawnContent string) error {
	var current string
	switch err := s.db.QueryRowContext(ctx,
		`SELECT title FROM fb_sessions WHERE id=? AND agent_id=?`,
		sessionID, s.AgentID()).Scan(&current); {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		return err
	}
	if strings.TrimSpace(current) != strings.TrimSpace(generateSessionTitle(withdrawnContent)) {
		return nil
	}
	var next string
	switch err := s.db.QueryRowContext(ctx,
		`SELECT content FROM fb_messages
		  WHERE session_id=? AND role='user' AND visibility=?
		  ORDER BY id LIMIT 1`,
		sessionID, messageVisible).Scan(&next); {
	case errors.Is(err, sql.ErrNoRows):
		// Nothing visible is left to name the session: fall back to the id,
		// which every title reader already treats as "unnamed".
		return s.SetTitle(ctx, sessionID, sessionID)
	case err != nil:
		return err
	}
	return s.SetTitle(ctx, sessionID, generateSessionTitle(next))
}

func (s *SessionStore) UpdateMessageParts(ctx context.Context, q dbtx, rowID int64, partsJSON string) error {
	if s == nil || s.db == nil {
		return nil
	}
	if rowID <= 0 {
		return fmt.Errorf("invalid message row id")
	}
	partsJSON = strings.TrimSpace(partsJSON)
	if partsJSON == "" {
		partsJSON = "[]"
	}
	if !json.Valid([]byte(partsJSON)) {
		return fmt.Errorf("invalid parts json")
	}
	// Row ids reach this from elsewhere in the process (compaction hands back a
	// boundary row it read), so ownership is re-derived from the row's own
	// session rather than trusted: this is the one write in the package that is
	// not keyed by a session id, and it must still stay inside the boundary.
	res, err := q.ExecContext(ctx, `UPDATE fb_messages SET parts=?
		WHERE id=? AND EXISTS (SELECT 1 FROM fb_sessions s WHERE s.id=fb_messages.session_id AND s.agent_id=?)`,
		partsJSON, rowID, s.AgentID())
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil || affected > 0 {
		return err
	}
	// Nothing changed: either the row is gone, which callers have always
	// tolerated, or it belongs to another primary agent, which they must not.
	return s.messageRowOwnershipError(ctx, q, rowID)
}

// messageRowOwnershipError reports ErrSessionNotOwned when rowID exists but
// belongs to another primary agent, and nil when there is no such row.
func (s *SessionStore) messageRowOwnershipError(ctx context.Context, q dbtx, rowID int64) error {
	var sessionID string
	err := q.QueryRowContext(ctx, `SELECT session_id FROM fb_messages WHERE id=?`, rowID).Scan(&sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.requireOwned(ctx, q, sessionID)
}

// Message visibility. Every transcript loader filters on visibility='visible',
// so any other value hides a row from the display transcript and from the next
// model request while keeping it on disk for inspection. The two hidden values
// stay distinct because an audit read has to tell a mechanical transcript
// repair from a message the user deliberately took back.
const (
	messageVisible   = "visible"
	messageRepaired  = "repaired"
	messageWithdrawn = "withdrawn"
)

// setMessageVisibility is the single writer that moves one owned row between
// visibility values, so the reader contract above is enforced by one statement
// instead of one per reason. from restricts which values the move may start
// from (nil means any); sessionID and role, when non-empty, are additional
// identity predicates the caller requires to match. It reports whether a row
// actually moved, leaving each caller to turn a false into the specific
// not-found/not-owned error it owes its own caller.
func (s *SessionStore) setMessageVisibility(ctx context.Context, q dbtx, rowID int64, to string, from []string, sessionID string, role string) (bool, error) {
	stmt := `UPDATE fb_messages SET visibility=? WHERE id=?`
	args := []any{to, rowID}
	if sessionID != "" {
		stmt += ` AND session_id=?`
		args = append(args, sessionID)
	}
	if role != "" {
		stmt += ` AND role=?`
		args = append(args, role)
	}
	if len(from) > 0 {
		stmt += ` AND visibility IN (?` + strings.Repeat(", ?", len(from)-1) + `)`
		for _, src := range from {
			args = append(args, src)
		}
	}
	stmt += ` AND EXISTS (SELECT 1 FROM fb_sessions s WHERE s.id=fb_messages.session_id AND s.agent_id=?)`
	args = append(args, s.AgentID())
	res, err := q.ExecContext(ctx, stmt, args...)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

// excludeMessageFromTranscript moves a row off the 'transcript' source so it is
// no longer loaded into the model-facing message list, marking it as a repair.
func (s *SessionStore) excludeMessageFromTranscript(ctx context.Context, q dbtx, rowID int64) error {
	if s == nil || s.db == nil {
		return nil
	}
	if rowID <= 0 {
		return fmt.Errorf("invalid message row id")
	}
	moved, err := s.setMessageVisibility(ctx, q, rowID, messageRepaired, nil, "", "")
	if err != nil || moved {
		return err
	}
	return s.messageRowOwnershipError(ctx, q, rowID)
}

// RepairDanglingToolResults makes a session transcript valid to re-send to the
// model by reconciling assistant tool_calls against the tool-result rows that
// answer them, in transcript order. Providers (OpenAI/DeepSeek) enforce two
// reciprocal invariants:
//
//   - "An assistant message with 'tool_calls' must be followed by tool messages
//     responding to each 'tool_call_id'." — a cancelled run or dismissed approval
//     can leave an assistant tool_calls row with no following result.
//   - "Messages with role 'tool' must be a response to a preceding message with
//     'tool_calls'." — an approval resume that re-stitches an already-persisted
//     batch in a different order (the divergent-suffix append in
//     AppendMessageSequence) can leave duplicate or misattached tool-result rows
//     buried mid-history, where no preceding assistant opened that id.
//
// A single order-aware walk repairs both. For each assistant batch we record the
// tool_call ids it declares; a following tool-result row is valid only while its
// id is still open in the current batch — an unopened id (orphan / answer to an
// earlier or duplicate batch) or a second answer to the same id (duplicate) is
// excluded from the transcript. Any non-tool message closes the current batch.
// After the walk, each assistant row keeps only the tool_calls actually answered
// by a surviving result; an assistant row left with neither text nor surviving
// tool_calls is excluded. It returns the number of rows changed.
//
// The repair is position-independent and idempotent: it heals a dangling or
// orphaned row whether at the transcript tail or buried mid-history, and a
// second pass over an already-valid transcript changes nothing. Callers should
// run it before reading the transcript for a new turn or a resume.
func (s *SessionStore) RepairDanglingToolResults(ctx context.Context, sessionID string) (int, error) {
	if s == nil || s.db == nil {
		return 0, nil
	}
	if err := s.requireOwned(ctx, s.db, sessionID); err != nil {
		return 0, err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return 0, nil
	}
	// The walk and the repairs it plans run in one transaction, so a second
	// writer appending to the same tail mid-repair cannot strand rows the
	// walk already judged.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := s.listTranscriptStoredMessages(ctx, tx, sessionID, 5000)
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}

	toolResultID := func(row storedMessage) string {
		if id := strings.TrimSpace(row.ToolStepID); id != "" {
			return id
		}
		_, _, id, _ := ParseMessageParts(row.PartsJSON, row.Content)
		return strings.TrimSpace(id)
	}

	// assistantBatch tracks one assistant tool_calls row and which of its ids end
	// up answered by a surviving (valid, deduplicated) tool-result row.
	type assistantBatch struct {
		row      storedMessage
		answered map[string]struct{}
	}
	var batches []*assistantBatch
	var current *assistantBatch
	open := map[string]struct{}{}
	var invalidToolRows []int64

	for _, row := range rows {
		switch strings.TrimSpace(strings.ToLower(row.Role)) {
		case llm.RoleAssistant:
			_, calls, _, _ := ParseMessageParts(row.PartsJSON, row.Content)
			open = make(map[string]struct{}, len(calls))
			for _, c := range calls {
				if id := strings.TrimSpace(c.ID); id != "" {
					open[id] = struct{}{}
				}
			}
			current = &assistantBatch{row: row, answered: make(map[string]struct{}, len(calls))}
			batches = append(batches, current)
		case llm.RoleTool:
			id := toolResultID(row)
			if _, ok := open[id]; id == "" || current == nil || !ok {
				invalidToolRows = append(invalidToolRows, row.ID)
				continue
			}
			// Consume the id so a duplicate answer in the same batch is treated as
			// an orphan on its next occurrence.
			delete(open, id)
			current.answered[id] = struct{}{}
		default:
			// A user/system message closes any open assistant batch: ids still
			// open are dangling and their assistant tool_calls get stripped below.
			open = map[string]struct{}{}
			current = nil
		}
	}

	repaired := 0
	for _, rowID := range invalidToolRows {
		if rowID <= 0 {
			continue
		}
		if err := s.excludeMessageFromTranscript(ctx, tx, rowID); err != nil {
			return repaired, err
		}
		repaired++
	}
	empty := func(s string) bool { t := strings.TrimSpace(s); return t == "" || t == "[]" }
	for _, batch := range batches {
		stripped, changed := StripUnansweredToolCalls(batch.row.PartsJSON, batch.answered)
		if !changed {
			continue
		}
		if !empty(stripped) || strings.TrimSpace(batch.row.Content) != "" {
			if err := s.UpdateMessageParts(ctx, tx, batch.row.ID, stripped); err != nil {
				return repaired, err
			}
		} else if err := s.excludeMessageFromTranscript(ctx, tx, batch.row.ID); err != nil {
			return repaired, err
		}
		repaired++
	}
	if err := tx.Commit(); err != nil {
		return repaired, err
	}
	return repaired, nil
}

// SessionTitleFromContent exposes the session-title derivation rule (first
// line, truncated to 50 runes) for surfaces that need a provisional display
// title before the store has persisted one — e.g. the stream terminal's
// animated window title during a first turn. Reuse this instead of
// re-implementing the truncation rule.
func SessionTitleFromContent(content string) string {
	return generateSessionTitle(content)
}

// generateSessionTitle creates a concise title from user input.
// All sources use the same rule: first line, truncated to 50 runes.
func generateSessionTitle(content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return "New session"
	}

	// Take first line
	if idx := strings.IndexAny(content, "\r\n"); idx > 0 {
		content = content[:idx]
	}

	// Truncate to reasonable length
	const maxLen = 50
	runes := []rune(content)
	if len(runes) > maxLen {
		return string(runes[:maxLen]) + "..."
	}

	return content
}

// AppendCompactCheckpoint records a compaction checkpoint: the system row that
// carries the summary text and its compact-boundary part, and the session's
// pointer to that row. The three facts are one state change — a row without the
// pointer is a stray summary the next request replays as an ordinary system
// message, and a pointer to a row without its part is a checkpoint every reader
// rejects — so they commit in one transaction or not at all.
func (s *SessionStore) AppendCompactCheckpoint(ctx context.Context, sessionID string, text string, part CompactBoundaryPart) error {
	if s == nil || s.db == nil {
		return errors.New("append compact checkpoint: session store unavailable")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return errors.New("append compact checkpoint: empty session id")
	}
	partsJSON := PartsJSONWithCompactPart(text, EncodeCompactBoundaryPart(part))
	if !json.Valid([]byte(partsJSON)) {
		return fmt.Errorf("invalid parts json")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// The upsert is also the ownership check: it refuses a session another
	// primary agent owns before anything is written.
	if err := s.ensureSession(ctx, tx, sessionID, sessionID, SessionBirth{}); err != nil {
		return err
	}
	rowID, err := insertMessage(ctx, tx, messageRow{
		SessionID:  sessionID,
		Role:       "system",
		Visibility: messageVisible,
		Content:    text,
		PartsJSON:  partsJSON,
		CreatedAt:  time.Now().Unix(),
	})
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE fb_sessions SET compact_boundary_message_id=?, updated_at=? WHERE id=? AND agent_id=?`,
		rowID, time.Now().Unix(), sessionID, s.AgentID()); err != nil {
		return err
	}
	return tx.Commit()
}

// ForkInto makes target the same conversation as source, in one transaction:
// every message row as stored — tool calls, attachments, compaction
// checkpoints and hidden rows alike — with the session's pointers into them
// (compaction boundary, context reset, first window) moved to the copies, the
// session's project, directory and memory settings, the recorded prompt state
// that keeps the copy's prompt prefix byte-identical to the source's, and the
// event history its transcript replays from. target is a session this agent
// owns that has no messages yet.
func (s *SessionStore) ForkInto(ctx context.Context, sourceID, targetID string) error {
	if s == nil || s.db == nil {
		return errors.New("nil session store")
	}
	sourceID, targetID = strings.TrimSpace(sourceID), strings.TrimSpace(targetID)
	if sourceID == "" || targetID == "" || sourceID == targetID {
		return errors.New("a fork needs a source session and a different target session")
	}
	if err := s.requireOwned(ctx, s.db, sourceID); err != nil {
		return err
	}
	if err := s.Ensure(ctx, targetID, targetID); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// The fork copies rows as stored — hidden rows, timing and all — through
	// the one insert path, then re-points the copied session's row pointers at
	// the copied rows.
	rows, err := tx.QueryContext(ctx, `SELECT `+messageColumns+`, visibility FROM fb_messages WHERE session_id=? ORDER BY id`, sourceID)
	if err != nil {
		return err
	}
	type sourceRow struct {
		id  int64
		row messageRow
	}
	var copied []sourceRow
	for rows.Next() {
		var item sourceRow
		var runID sql.NullString
		var execStart, execFinish, execDuration sql.NullInt64
		if err := rows.Scan(&item.id, &runID, &item.row.Role, &item.row.Content, &item.row.MessageID, &item.row.PartsJSON, &item.row.Model, &item.row.CreatedAt, &item.row.UsageJSON, &item.row.ToolStepID, &item.row.ToolMetaJSON, &execStart, &execFinish, &execDuration, &item.row.Visibility); err != nil {
			_ = rows.Close()
			return err
		}
		item.row.RunID = runID.String
		item.row.SessionID = targetID
		item.row.Exec = execTimingFromColumns(execStart, execFinish, execDuration)
		copied = append(copied, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	copyOf := make(map[int64]int64, len(copied))
	for _, item := range copied {
		row := item.row
		rowID, err := insertMessage(ctx, tx, row)
		if err != nil {
			return err
		}
		copyOf[item.id] = rowID
	}

	var (
		window, memoryMode, memorySource, cwd, gitBranch string
		projectID                                        sql.NullString
		boundary, resetRow                               sql.NullInt64
	)
	if err := tx.QueryRowContext(ctx,
		`SELECT compact_boundary_message_id, context_reset_message_id, initial_window_id, memory_mode, memory_source, cwd, git_branch, project_id FROM fb_sessions WHERE id=?`,
		sourceID).Scan(&boundary, &resetRow, &window, &memoryMode, &memorySource, &cwd, &gitBranch, &projectID); err != nil {
		return err
	}
	mapRow := func(v sql.NullInt64) any {
		if !v.Valid || v.Int64 <= 0 {
			return nil
		}
		return copyOf[v.Int64]
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE fb_sessions SET compact_boundary_message_id=?, context_reset_message_id=?, initial_window_id=?, memory_mode=?, memory_source=?, cwd=?, git_branch=?, project_id=NULLIF(?,''), updated_at=? WHERE id=? AND agent_id=?`,
		mapRow(boundary), mapRow(resetRow), window, memoryMode, memorySource, cwd, gitBranch, projectID.String, time.Now().Unix(), targetID, s.AgentID()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT OR REPLACE INTO fb_session_prompt_state(session_id, key, value, updated_at) SELECT ?, key, value, updated_at FROM fb_session_prompt_state WHERE session_id=?`,
		targetID, sourceID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO fb_session_events(session_id, event_id, run_id, event_type, payload_json, occurred_at_ms) SELECT ?, event_id, run_id, event_type, payload_json, occurred_at_ms FROM fb_session_events WHERE session_id=? ORDER BY sequence`,
		targetID, sourceID); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteSessionMessages deletes all transcript turns for a
// This is used by the hard clear implementation (matching claude-code's
// clearConversation semantics) and by the /clear slash command.
func (s *SessionStore) DeleteSessionMessages(ctx context.Context, sessionID string) error {
	if s == nil || s.db == nil {
		return nil
	}
	if err := s.requireOwned(ctx, s.db, sessionID); err != nil {
		return err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM fb_messages WHERE session_id=? AND EXISTS (SELECT 1 FROM fb_sessions s WHERE s.id=fb_messages.session_id AND s.agent_id=?)`, sessionID, s.AgentID())
	return err
}

// SetSessionContextResetToLatest advances the session's context-reset cursor to
// the latest transcript row id. Rows with id <= the cursor are retained on disk
// (and remain fully replayable via ListAllMessages) but are excluded from the
// model-context projection readers. This implements "clear context" without
// deleting any history. It Ensures the session row exists first so /resume and
// `forebrain resume <id>` keep finding the session, and returns the new cursor.
func (s *SessionStore) SetSessionContextResetToLatest(ctx context.Context, sessionID string) (int64, error) {
	if s == nil || s.db == nil {
		return 0, nil
	}
	if err := s.requireOwned(ctx, s.db, sessionID); err != nil {
		return 0, err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return 0, nil
	}
	if err := s.Ensure(ctx, sessionID, sessionID); err != nil {
		return 0, err
	}
	latest, err := s.LastTranscriptRowID(ctx, sessionID)
	if err != nil {
		return 0, err
	}
	now := time.Now().Unix()
	if _, err := s.db.ExecContext(ctx,
		`UPDATE fb_sessions SET context_reset_message_id=NULLIF(?,0), updated_at=? WHERE id=? AND agent_id=?`,
		latest, now, sessionID, s.AgentID()); err != nil {
		return 0, err
	}
	return latest, nil
}

// sessionContextResetRowID returns the session's context-reset cursor (0 when
// never cleared). Rows with id <= the cursor are excluded from the model
// context projection.
func (s *SessionStore) sessionContextResetRowID(ctx context.Context, q dbtx, sessionID string) (int64, error) {
	if s == nil || s.db == nil {
		return 0, nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return 0, nil
	}
	var rowID sql.NullInt64
	err := q.QueryRowContext(ctx,
		`SELECT context_reset_message_id FROM fb_sessions WHERE id=?`,
		sessionID).Scan(&rowID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if rowID.Int64 < 0 {
		return 0, nil
	}
	return rowID.Int64, nil
}

// modelContextFloor computes the row-id floor for model-context projection. It
// combines the compaction boundary with the context-reset cursor and returns:
//   - floor: rows with id > floor are the active model context (used when the
//     compaction projection does not apply).
//   - boundaryID: the compaction boundary row id (0 when none).
//   - useProjection: true when the compact checkpoint projection should be
//     applied (i.e. a checkpoint is the active floor and no later clear
//     superseded it). When a context reset happened at or after the compaction
//     boundary, the projection is skipped and simple id > floor filtering is
//     used, yielding an empty active context immediately after a fresh clear.
func (s *SessionStore) modelContextFloor(ctx context.Context, q dbtx, sessionID string) (floor int64, boundaryID int64, useProjection bool, err error) {
	boundaryID, err = s.compactBoundaryRowID(ctx, q, sessionID)
	if err != nil {
		return 0, 0, false, err
	}
	if boundaryID > 0 {
		row, ok, loadErr := s.loadStoredMessageByRowID(ctx, boundaryID)
		if loadErr != nil {
			return 0, 0, false, loadErr
		}
		part, isCheckpoint := ParseCompactBoundaryPart(row.PartsJSON)
		if !ok || !isCheckpoint || len(part.ReplacementHistory) == 0 {
			// An invalid pointer cannot become an active checkpoint. Falling back
			// to the unprojected transcript is the safe choice, but it is also
			// indistinguishable from "never compacted": the session silently
			// re-reads its whole history, and the auto-compact check keeps firing
			// on every turn. Log at ERROR to make a persisted-but-unusable
			// checkpoint diagnosable instead of showing up only as unexplained
			// token spend or repeated compaction triggers.
			slog.Error("compact boundary is not a usable checkpoint; falling back to full transcript",
				"session", sessionID,
				"boundary_row_id", boundaryID,
				"row_found", ok,
				"is_checkpoint", isCheckpoint,
				"replacement_history_len", len(part.ReplacementHistory))
			boundaryID = 0
		}
	}
	resetID, err := s.sessionContextResetRowID(ctx, q, sessionID)
	if err != nil {
		return 0, 0, false, err
	}
	floor = boundaryID
	if resetID > floor {
		floor = resetID
	}
	// The checkpoint projection only applies when the compact boundary is the
	// active floor. A later
	// clear (resetID >= boundaryID) supersedes it: the model context is simply
	// everything after the reset cursor.
	useProjection = boundaryID > 0 && boundaryID > resetID
	return floor, boundaryID, useProjection, nil
}

func (s *SessionStore) compactBoundaryRowID(ctx context.Context, q dbtx, sessionID string) (int64, error) {
	if s == nil || s.db == nil {
		return 0, nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return 0, nil
	}
	var v sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT compact_boundary_message_id FROM fb_sessions WHERE id=?`, sessionID).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if !v.Valid || v.Int64 <= 0 {
		return 0, nil
	}
	return v.Int64, nil
}

// SessionPromptState returns one frozen piece of a session's prompt prefix, and
// whether it has been frozen at all. An empty frozen value is a real value: a
// session whose skill set is empty freezes an empty catalog, and must not
// re-render one on every request.
func (s *SessionStore) SessionPromptState(ctx context.Context, sessionID, key string) (string, bool, error) {
	if s == nil || s.db == nil {
		return "", false, nil
	}
	sessionID, key = strings.TrimSpace(sessionID), strings.TrimSpace(key)
	if sessionID == "" || key == "" {
		return "", false, nil
	}
	var value string
	err := s.db.QueryRowContext(ctx,
		`SELECT value FROM fb_session_prompt_state WHERE session_id=? AND key=?`,
		sessionID, key,
	).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

// FreezeSessionPromptState stores candidate as this session's value for key and
// returns the value that is now frozen — which is the one already there when
// another request of the same session won the race. First write wins, because
// the value that is already on the wire is the one the provider cached.
//
// The second return says whether the value is actually frozen. A session row
// that does not exist stores nothing — prompt state belongs to a session, and a
// run without one has nothing to stay identical across — and the caller is told
// so, because a caller that mistook the returned candidate for a frozen value
// would render a fresh one on every single request and re-bill its whole cached
// prefix each time. Such a caller must fall back to a freeze of its own.
func (s *SessionStore) FreezeSessionPromptState(ctx context.Context, sessionID, key, candidate string) (string, bool, error) {
	if s == nil || s.db == nil {
		return candidate, false, nil
	}
	sessionID, key = strings.TrimSpace(sessionID), strings.TrimSpace(key)
	if sessionID == "" || key == "" {
		return candidate, false, nil
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO fb_session_prompt_state (session_id, key, value, updated_at)
		 SELECT ?, ?, ?, ? WHERE EXISTS (SELECT 1 FROM fb_sessions WHERE id=? AND agent_id=?)`,
		sessionID, key, candidate, time.Now().Unix(), sessionID, s.AgentID(),
	); err != nil {
		return "", false, err
	}
	frozen, ok, err := s.SessionPromptState(ctx, sessionID, key)
	if err != nil {
		return "", false, err
	}
	if !ok {
		return candidate, false, nil
	}
	return frozen, true, nil
}

// SessionModelSelection is one session's own effective primary-model choice.
// Effort is the concrete pinned value; empty means explicitly no effort, never
// "re-read the file next time".
type SessionModelSelection struct {
	Provider string
	Model    string
	Effort   string
}

// SaveSessionModelSelection stores sel as the session's model choice,
// last-write-wins. It reports stored=false, without error, when the session
// row does not exist: model state belongs to a conversation, and there is
// nothing to pin for one that was never created.
func (s *SessionStore) SaveSessionModelSelection(
	ctx context.Context, sessionID string, sel SessionModelSelection,
) (stored bool, err error) {
	if s == nil || s.db == nil {
		return false, nil
	}
	sessionID = strings.TrimSpace(sessionID)
	sel.Provider = strings.TrimSpace(sel.Provider)
	sel.Model = strings.TrimSpace(sel.Model)
	sel.Effort = strings.TrimSpace(sel.Effort)
	if sessionID == "" {
		return false, nil
	}
	if sel.Provider == "" || sel.Model == "" {
		return false, fmt.Errorf("session model selection requires a provider and a model")
	}
	if err := s.requireOwned(ctx, s.db, sessionID); err != nil {
		return false, err
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO fb_session_model_state(session_id, provider, model, effort, updated_at)
		SELECT ?, ?, ?, ?, ?
		WHERE EXISTS (SELECT 1 FROM fb_sessions WHERE id=? AND agent_id=?)
		ON CONFLICT(session_id) DO UPDATE SET
			provider=excluded.provider, model=excluded.model, effort=excluded.effort, updated_at=excluded.updated_at`,
		sessionID, sel.Provider, sel.Model, sel.Effort, time.Now().Unix(), sessionID, s.AgentID(),
	)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

// SessionModelSelection returns the session's stored model choice, and false
// when it has none (or the session belongs to another primary agent, which is
// the same answer as not existing).
func (s *SessionStore) SessionModelSelection(
	ctx context.Context, sessionID string,
) (SessionModelSelection, bool, error) {
	if s == nil || s.db == nil {
		return SessionModelSelection{}, false, nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return SessionModelSelection{}, false, nil
	}
	if err := s.requireOwned(ctx, s.db, sessionID); err != nil {
		return SessionModelSelection{}, false, err
	}
	var sel SessionModelSelection
	err := s.db.QueryRowContext(ctx,
		`SELECT m.provider, m.model, m.effort
		FROM fb_session_model_state m
		JOIN fb_sessions s ON s.id=m.session_id AND s.agent_id=?
		WHERE m.session_id=?`,
		s.AgentID(), sessionID,
	).Scan(&sel.Provider, &sel.Model, &sel.Effort)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionModelSelection{}, false, nil
	}
	if err != nil {
		return SessionModelSelection{}, false, err
	}
	return sel, true, nil
}

// CopySessionModelSelection copies the source session's stored model choice
// onto the target session, overwriting whatever the target held. Both sides
// must belong to this primary agent: the source is checked by ownership, the
// target by an agent-scoped existence check, so a missing target is rejected
// by policy instead of surfacing as a raw foreign-key error. It reports
// copied=false when the source has no model row — a fork of a session that
// never pinned a model inherits nothing, which the caller resolves from the
// live runner before copying.
func (s *SessionStore) CopySessionModelSelection(
	ctx context.Context, sourceSessionID, targetSessionID string,
) (copied bool, err error) {
	if s == nil || s.db == nil {
		return false, nil
	}
	sourceSessionID = strings.TrimSpace(sourceSessionID)
	targetSessionID = strings.TrimSpace(targetSessionID)
	if sourceSessionID == "" || targetSessionID == "" || sourceSessionID == targetSessionID {
		return false, nil
	}
	if err := s.requireOwned(ctx, s.db, sourceSessionID); err != nil {
		return false, err
	}
	var target int
	if err := s.db.QueryRowContext(ctx,
		`SELECT CASE WHEN EXISTS(
			SELECT 1 FROM fb_sessions WHERE id=? AND agent_id=?
		) THEN 1 ELSE 0 END`,
		targetSessionID, s.AgentID(),
	).Scan(&target); err != nil {
		return false, err
	}
	if target != 1 {
		return false, fmt.Errorf("%w: %s", ErrSessionNotOwned, targetSessionID)
	}
	// The SELECT ends with a WHERE clause on purpose: SQLite binds ON
	// CONFLICT to the upsert only then; without it the statement is a syntax
	// error.
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO fb_session_model_state(session_id, provider, model, effort, updated_at)
		SELECT ?, src.provider, src.model, src.effort, ?
		FROM fb_session_model_state src
		WHERE src.session_id=? AND EXISTS (
			SELECT 1 FROM fb_sessions s WHERE s.id=src.session_id AND s.agent_id=?
		)
		ON CONFLICT(session_id) DO UPDATE SET
			provider=excluded.provider, model=excluded.model, effort=excluded.effort, updated_at=excluded.updated_at`,
		targetSessionID, time.Now().Unix(), sourceSessionID, s.AgentID(),
	)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}
