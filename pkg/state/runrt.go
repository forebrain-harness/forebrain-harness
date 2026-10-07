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
	"github.com/google/uuid"
)

var ErrRunNotFound = errors.New("run not found")

// ErrNoWaitContinuation reports that the durable continuation a caller tried to
// claim does not exist: the run is no longer parked at that approval, because
// it finished, was cancelled, or the wait was already resolved. It is a
// different fact from losing the fence to a live owner, and saying so matters —
// a caller that conflates the two reports a contended process where there is
// none.
var ErrNoWaitContinuation = errors.New("the approval this continuation belongs to is no longer pending")

// ErrSessionBusy is the family of refusals to start a turn in a session that
// already has one alive; errors.Is matches both members below.
var ErrSessionBusy = errors.New("session busy")

// sessionBusyError is one member of the ErrSessionBusy family. It matches the
// family sentinel and its own member value, so a caller can ask "was the
// session busy" and "which way" with the same errors.Is.
type sessionBusyError struct{ message string }

func (e sessionBusyError) Error() string { return e.message }

func (e sessionBusyError) Is(target error) bool { return target == ErrSessionBusy }

var (
	// ErrSessionRunning: another live run is driving the conversation.
	ErrSessionRunning = sessionBusyError{"This conversation is already running a turn; send again when it finishes."}
	// ErrSessionAwaitingApproval: a run in it is parked on an approval.
	ErrSessionAwaitingApproval = sessionBusyError{"This conversation is waiting for an approval; answer it before sending another message."}
)

// SessionBusyCode is the stable wire code of a session-busy refusal, "" for
// anything else. A surface that localises the sentence for its viewer keys
// off the code, not the English text.
func SessionBusyCode(err error) string {
	switch {
	case errors.Is(err, ErrSessionAwaitingApproval):
		return "session_awaiting_approval"
	case errors.Is(err, ErrSessionRunning):
		return "session_running"
	default:
		return ""
	}
}

// RunOwnerLease is how long a process may go without renewing its
// registration before its running runs read as abandoned. It is generous on
// purpose: a process starved of the write lock for a few seconds is alive,
// and reaping a live run is far worse than reaping a dead one a minute late.
const RunOwnerLease = 60 * time.Second

type RunStatus string

const (
	RunStatusRunning       RunStatus = "running"
	RunStatusWaitingAction RunStatus = "waiting_action"
	RunStatusDone          RunStatus = "done"
	RunStatusFailed        RunStatus = "failed"
	RunStatusCancelled     RunStatus = "cancelled"
)

type Run struct {
	ID                    string    `json:"id"`
	SessionID             string    `json:"session_id"`
	InputText             string    `json:"input_text"`
	Status                RunStatus `json:"status"`
	UsagePromptTokens     int       `json:"usage_prompt_tokens,omitempty"`
	UsageCompletionTokens int       `json:"usage_completion_tokens,omitempty"`
	ParentRunID           string    `json:"parent_run_id,omitempty"`
	CreatedAt             int64     `json:"created_at"`
	UpdatedAt             int64     `json:"updated_at"`
}

// runColumns is the one fb_runs column list; scanRun is its one reader.
// parent_run_id is the only nullable column in it and reads as "".
const runColumns = `id, session_id, parent_run_id, input_text, status, usage_prompt_tokens, usage_completion_tokens, usage_cache_read_tokens, usage_cache_creation_tokens, usage_llm_calls, created_at, updated_at`

// prefixedRunColumns is runColumns under one table alias, for the queries
// that join fb_sessions.
func prefixedRunColumns(alias string) string {
	parts := strings.Split(runColumns, ", ")
	for i, part := range parts {
		parts[i] = alias + "." + part
	}
	return strings.Join(parts, ", ")
}

func scanRun(scanner interface{ Scan(...any) error }) (Run, error) {
	var r Run
	var parent sql.NullString
	var cacheRead, cacheWrite, llmCalls int
	if err := scanner.Scan(&r.ID, &r.SessionID, &parent, &r.InputText, &r.Status, &r.UsagePromptTokens, &r.UsageCompletionTokens, &cacheRead, &cacheWrite, &llmCalls, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return Run{}, err
	}
	r.ParentRunID = parent.String
	return r, nil
}

type Wait struct {
	RunID           string        `json:"run_id"`
	ActionID        string        `json:"action_id"`
	ToolName        string        `json:"tool_name"`
	ToolInputJSON   string        `json:"tool_input_json"`
	SessionSnapshot []llm.Message `json:"session_snapshot,omitempty"`
	// SessionSnapshotJSON is the stored form of SessionSnapshot; the scanner
	// fills it and the parse helper decodes it, so a reader never marshals.
	SessionSnapshotJSON string `json:"-"`
	// Source metadata keeps an approval associated with the child VM that
	// requested it, instead of assuming every pending action belongs to main.
	AgentID      string `json:"agent_id,omitempty"`
	SubagentType string `json:"subagent_type,omitempty"`
	// SubagentRunID is the run ID of the subagent whose own tool call raised
	// this gate, when it originated inside one (empty for a gate the
	// top-level agent raised itself). It survives a process restart because
	// it is persisted here, unlike the in-memory agent.Registry: resuming
	// this wait must look the subagent's WorkerSessionID back up (via
	// agent.GetMerged, which also reads the durable subagent-history ledger)
	// and continue under that session instead of the top-level chat one, or
	// everything the subagent recorded under its own session before the gate
	// fired - notes, todos, mode - becomes unreachable to it afterward.
	SubagentRunID    string `json:"subagent_run_id,omitempty"`
	SandboxProfile   string `json:"sandbox_profile,omitempty"`
	RequestedProfile string `json:"requested_profile,omitempty"`
	ProfileElevation bool   `json:"profile_elevation,omitempty"`
	ResumeOwner      string `json:"resume_owner,omitempty"`
	ResumeClaimedAt  int64  `json:"resume_claimed_at,omitempty"`
	// ResumePhase is empty/claimed until the continuation crosses its durable
	// execution fence. Once execution_started is committed, another process
	// must never replay the tool automatically: a crash can no longer prove
	// whether an external side effect happened.
	ResumePhase string `json:"resume_phase,omitempty"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

const (
	WaitResumePhaseClaimed          = "claimed"
	WaitResumePhaseExecutionStarted = "execution_started"
	WaitResumePhaseUncertain        = "outcome_uncertain"
)

// resumeLeaseFresh reports whether the owner named on this wait is still
// renewing it. A row with no timestamp predates any live owner and is stale by
// definition.
func (w Wait) resumeLeaseFresh(now time.Time) bool {
	return w.ResumeClaimedAt > 0 && now.Sub(time.UnixMilli(w.ResumeClaimedAt)) < WaitResumeLease
}

// WaitResumeLease is the fencing window for a continuation owner. Live
// surfaces refresh it while presenting an approval; another process may take
// over only after the lease is stale.
const WaitResumeLease = 5 * time.Second

func MarshalWaitSessionSnapshot(messages []llm.Message) string {
	if len(messages) == 0 {
		return ""
	}
	b, err := json.Marshal(messages)
	if err != nil {
		return ""
	}
	return string(b)
}

func ParseWaitSessionSnapshot(raw string) []llm.Message {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var out []llm.Message
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

type RunStore struct {
	DB *sql.DB
	// Owner is the process this store writes runs for. Every run it creates,
	// and every run it moves back to running, is stamped with it; a store
	// without one creates runs no live process vouches for.
	Owner string
}

// livePrimaryRunCondition is the one definition of "this session has a live
// primary run": one parked on an approval, or one running under an owner
// whose lease is still fresh. CreateRun's atomic INSERT and
// SessionHasLiveRun read the same predicate, so the writer's rule and the
// reader's rule cannot drift apart. %s takes the fb_runs alias.
const livePrimaryRunCondition = `(status = ? OR (status = ? AND EXISTS (
  SELECT 1 FROM fb_run_owners o WHERE o.owner = %s.owner AND o.heartbeat_at_ms >= ?)))`

// liveRunArgs binds the condition's parameters in order, with the lease
// cutoff computed from now.
func liveRunArgs(now time.Time) []any {
	return []any{string(RunStatusWaitingAction), string(RunStatusRunning),
		now.UnixMilli() - RunOwnerLease.Milliseconds()}
}

func (s *RunStore) CreateRun(ctx context.Context, sessionID, inputText string) (*Run, error) {
	if s == nil || s.DB == nil {
		return nil, fmt.Errorf("nil db")
	}
	now := time.Now()
	r := &Run{
		ID:        uuid.NewString(),
		SessionID: strings.TrimSpace(sessionID),
		InputText: inputText,
		Status:    RunStatusRunning,
		CreatedAt: now.Unix(),
		UpdatedAt: now.Unix(),
	}
	if r.SessionID == "" {
		return nil, fmt.Errorf("a run belongs to a session; create the session first")
	}
	// One statement decides and inserts: a session may hold at most one live
	// primary run — one parked on an approval, or one whose owner still
	// renews its lease. SQLite allows a single writer at a time, so the check
	// and the insert are atomic against every other process sharing this
	// database. A running row whose owner's lease has lapsed does not block:
	// the reaper is on its way to end it.
	args := append([]any{r.ID, r.SessionID, r.InputText, string(r.Status), strings.TrimSpace(s.Owner), r.CreatedAt, r.UpdatedAt,
		r.SessionID}, liveRunArgs(now)...)
	res, err := s.DB.ExecContext(ctx, `
INSERT INTO fb_runs(id, session_id, input_text, status, owner, created_at, updated_at)
SELECT ?, ?, ?, ?, ?, ?, ?
WHERE NOT EXISTS (
  SELECT 1 FROM fb_runs r
  WHERE r.session_id = ? AND r.parent_run_id IS NULL
    AND `+fmt.Sprintf(livePrimaryRunCondition, "r")+`)`, args...)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Nothing was inserted: name the blocker for the person who asked.
		var parked int
		if err := s.DB.QueryRowContext(ctx, `
SELECT EXISTS(SELECT 1 FROM fb_runs WHERE session_id=? AND parent_run_id IS NULL AND status=?)`,
			r.SessionID, string(RunStatusWaitingAction)).Scan(&parked); err != nil {
			return nil, err
		}
		if parked != 0 {
			return nil, ErrSessionAwaitingApproval
		}
		return nil, ErrSessionRunning
	}
	return r, nil
}

func (s *RunStore) CreateSubagentRun(ctx context.Context, parentRunID, sessionID, inputPreview string) (*Run, error) {
	if s == nil || s.DB == nil {
		return nil, fmt.Errorf("nil db")
	}
	parentRunID = strings.TrimSpace(parentRunID)
	if parentRunID == "" {
		return nil, fmt.Errorf("empty parent run id")
	}
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		return nil, fmt.Errorf("a subagent run belongs to the session of the run that dispatched it")
	}
	preview := strings.TrimSpace(inputPreview)
	if preview == "" {
		preview = "(subagent)"
	}
	now := time.Now().Unix()
	r := &Run{
		ID:          uuid.NewString(),
		SessionID:   sid,
		InputText:   preview,
		Status:      RunStatusRunning,
		ParentRunID: parentRunID,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	// No session-exclusivity check: a subagent run belongs to a parent run
	// that is alive, and several of them may share one session.
	_, err := s.DB.ExecContext(ctx, `
INSERT INTO fb_runs(id, session_id, parent_run_id, input_text, status, owner, created_at, updated_at)
VALUES(?,?,?,?,?,?,?,?)`,
		r.ID, r.SessionID, r.ParentRunID, r.InputText, string(r.Status), strings.TrimSpace(s.Owner), r.CreatedAt, r.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return r, nil
}

func (s *RunStore) FailRunningDescendants(ctx context.Context, parentRunID string) error {
	return s.setRunningDescendantsStatus(ctx, parentRunID, RunStatusFailed)
}

// CancelRunningDescendants preserves the difference between a user-directed
// cancellation and an execution failure in durable run history.
func (s *RunStore) CancelRunningDescendants(ctx context.Context, parentRunID string) error {
	return s.setRunningDescendantsStatus(ctx, parentRunID, RunStatusCancelled)
}

func (s *RunStore) setRunningDescendantsStatus(ctx context.Context, parentRunID string, status RunStatus) error {
	if s == nil || s.DB == nil {
		return fmt.Errorf("nil db")
	}
	parentRunID = strings.TrimSpace(parentRunID)
	if parentRunID == "" {
		return nil
	}
	now := time.Now().Unix()
	_, err := s.DB.ExecContext(ctx, `
WITH RECURSIVE descendants(id) AS (
	SELECT id FROM fb_runs WHERE parent_run_id=?
	UNION ALL
	SELECT child.id FROM fb_runs child JOIN descendants parent ON child.parent_run_id=parent.id
)
UPDATE fb_runs SET status=?, updated_at=?
WHERE id IN (SELECT id FROM descendants) AND (status=? OR status=?)`,
		parentRunID, string(status), now, string(RunStatusRunning), string(RunStatusWaitingAction))
	return err
}

func (s *RunStore) GetRun(ctx context.Context, id string) (*Run, error) {
	if s == nil || s.DB == nil {
		return nil, fmt.Errorf("nil db")
	}
	r, err := scanRun(s.DB.QueryRowContext(ctx, `
SELECT `+runColumns+`
FROM fb_runs WHERE id=?`, id))
	if err == sql.ErrNoRows {
		return nil, ErrRunNotFound
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// SessionUsage totals one session's conversation consumption over its run
// tree: the session's own runs and every descendant subagent run. A child run
// carries its parent's id in parent_run_id, and a continued subagent's run is
// recorded under its worker session rather than this one, so the tree is
// walked by parent link rather than filtered by session id. Every row holds
// only the calls its own run made (a subagent's calls never roll up into the
// run that dispatched it), so the sum counts each model call exactly once.
// Cache read/creation tokens and model-call counts ride in the same rows.
type SessionUsage struct {
	PromptTokens     int // uncached input: cache reads and writes are separate
	CompletionTokens int
	CacheReadTokens  int
	CacheWriteTokens int
	LLMCalls         int
}

// LastRunUsage is one run's own usage. PromptTokens is the uncached input
// only — the provider's cache reads and writes are reported beside it — so
// what the run cost to send is the sum of the three.
type LastRunUsage struct {
	PromptTokens     int
	CompletionTokens int
	CacheReadTokens  int
	CacheWriteTokens int
	LLMCalls         int
}

// SessionUsageForSession sums usage over the session's run tree. Every run
// counts whatever its status: a failed or cancelled run spent the tokens its
// row records just the same, and a run still in flight has not written its
// totals yet.
func (s *RunStore) SessionUsageForSession(ctx context.Context, sessionID string) (SessionUsage, error) {
	out := SessionUsage{}
	if s == nil || s.DB == nil {
		return out, fmt.Errorf("nil db")
	}
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		return out, fmt.Errorf("empty session id")
	}
	err := s.DB.QueryRowContext(ctx, `
WITH RECURSIVE run_tree(id) AS (
  SELECT id FROM fb_runs WHERE session_id=?
  UNION
  SELECT r.id FROM fb_runs r JOIN run_tree t ON r.parent_run_id=t.id
)
SELECT IFNULL(SUM(usage_prompt_tokens),0), IFNULL(SUM(usage_completion_tokens),0),
       IFNULL(SUM(usage_cache_read_tokens),0), IFNULL(SUM(usage_cache_creation_tokens),0),
       IFNULL(SUM(usage_llm_calls),0)
FROM fb_runs WHERE id IN (SELECT id FROM run_tree)`,
		sid).Scan(
		&out.PromptTokens, &out.CompletionTokens,
		&out.CacheReadTokens, &out.CacheWriteTokens,
		&out.LLMCalls)
	return out, err
}

// LastRunUsageForSession reads the session's most recently completed turn: a
// top-level run of the session. A subagent run shares the session id of the
// run that dispatched it, and one finishing after its parent must not stand in
// for the user's last turn.
func (s *RunStore) LastRunUsageForSession(ctx context.Context, sessionID string) (LastRunUsage, error) {
	out := LastRunUsage{}
	if s == nil || s.DB == nil {
		return out, fmt.Errorf("nil db")
	}
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		return out, fmt.Errorf("empty session id")
	}
	err := s.DB.QueryRowContext(ctx, `
SELECT usage_prompt_tokens, usage_completion_tokens,
       usage_cache_read_tokens, usage_cache_creation_tokens,
       usage_llm_calls
FROM fb_runs WHERE session_id=? AND status=? AND parent_run_id IS NULL
ORDER BY updated_at DESC LIMIT 1`,
		sid, string(RunStatusDone)).Scan(
		&out.PromptTokens, &out.CompletionTokens,
		&out.CacheReadTokens, &out.CacheWriteTokens,
		&out.LLMCalls)
	return out, err
}

// SetRunUsage persists a run's aggregated provider usage, cache traffic
// included. The figures come from the run's own usage accumulator, so every
// model call the run made — the loop's requests and any local compaction — is
// already summed in, and no call a subagent run made is.
func (s *RunStore) SetRunUsage(ctx context.Context, runID string, usage LastRunUsage) error {
	if s == nil || s.DB == nil {
		return fmt.Errorf("nil db")
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return fmt.Errorf("empty run id")
	}
	if usage.PromptTokens < 0 {
		usage.PromptTokens = 0
	}
	if usage.CompletionTokens < 0 {
		usage.CompletionTokens = 0
	}
	if usage.CacheReadTokens < 0 {
		usage.CacheReadTokens = 0
	}
	if usage.CacheWriteTokens < 0 {
		usage.CacheWriteTokens = 0
	}
	if usage.LLMCalls < 0 {
		usage.LLMCalls = 0
	}
	now := time.Now().Unix()
	_, err := s.DB.ExecContext(ctx, `
UPDATE fb_runs SET usage_prompt_tokens=?, usage_completion_tokens=?,
  usage_cache_read_tokens=?, usage_cache_creation_tokens=?, usage_llm_calls=?,
  updated_at=? WHERE id=?`,
		usage.PromptTokens, usage.CompletionTokens,
		usage.CacheReadTokens, usage.CacheWriteTokens, usage.LLMCalls,
		now, runID)
	return err
}

// SetWaitingAction parks the run at one approval. run_id is the primary key,
// so the upsert replaces whatever the run was waiting on before — the old row
// is gone in the same statement that writes the new one, and no DELETE is
// needed to keep one wait per run.
func (s *RunStore) SetWaitingAction(ctx context.Context, runID string, w Wait) error {
	if s == nil || s.DB == nil {
		return fmt.Errorf("nil db")
	}
	now := time.Now().Unix()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `
INSERT INTO fb_run_waits(run_id, action_id, tool_name, tool_input_json, session_snapshot_json,
  agent_id, subagent_type, subagent_run_id, sandbox_profile, requested_profile, profile_elevation,
  resume_owner, resume_claimed_at_ms, resume_phase, created_at, updated_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(run_id) DO UPDATE SET
  action_id=excluded.action_id,
  tool_name=excluded.tool_name,
  tool_input_json=excluded.tool_input_json,
  session_snapshot_json=excluded.session_snapshot_json,
  agent_id=excluded.agent_id,
  subagent_type=excluded.subagent_type,
  subagent_run_id=excluded.subagent_run_id,
  sandbox_profile=excluded.sandbox_profile,
  requested_profile=excluded.requested_profile,
  profile_elevation=excluded.profile_elevation,
  resume_owner=excluded.resume_owner,
  resume_claimed_at_ms=excluded.resume_claimed_at_ms,
  resume_phase=excluded.resume_phase,
  updated_at=excluded.updated_at`,
		runID, w.ActionID, w.ToolName, w.ToolInputJSON, MarshalWaitSessionSnapshot(w.SessionSnapshot),
		w.AgentID, w.SubagentType, nullIfEmpty(w.SubagentRunID), w.SandboxProfile, w.RequestedProfile, boolInt(w.ProfileElevation),
		w.ResumeOwner, waitClaimedAtValue(w.ResumeClaimedAt), strings.TrimSpace(w.ResumePhase), now, now); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE fb_runs SET status=?, updated_at=? WHERE id=?`, string(RunStatusWaitingAction), now, runID); err != nil {
		return err
	}
	return tx.Commit()
}

func waitClaimedAtValue(ms int64) any {
	if ms <= 0 {
		return nil
	}
	return ms
}

// waitColumns is the one fb_run_waits column list; scanWait is its one reader.
// subagent_run_id is the only nullable column in it and reads as "".
const waitColumns = `run_id, action_id, tool_name, tool_input_json, session_snapshot_json,
  agent_id, subagent_type, subagent_run_id, sandbox_profile, requested_profile, profile_elevation,
  resume_owner, resume_claimed_at_ms, resume_phase, created_at, updated_at`

func scanWait(scanner interface{ Scan(...any) error }) (Wait, error) {
	var w Wait
	var subagentRunID sql.NullString
	var claimedAt sql.NullInt64
	var elevation int
	if err := scanner.Scan(&w.RunID, &w.ActionID, &w.ToolName, &w.ToolInputJSON, &w.SessionSnapshotJSON,
		&w.AgentID, &w.SubagentType, &subagentRunID, &w.SandboxProfile, &w.RequestedProfile, &elevation,
		&w.ResumeOwner, &claimedAt, &w.ResumePhase, &w.CreatedAt, &w.UpdatedAt); err != nil {
		return Wait{}, err
	}
	w.SubagentRunID = subagentRunID.String
	if claimedAt.Valid {
		w.ResumeClaimedAt = claimedAt.Int64
	}
	w.ProfileElevation = elevation != 0
	return w, nil
}

func (s *RunStore) SessionHasRunWaitingOnToolApproval(ctx context.Context, sessionID string) (bool, error) {
	if s == nil || s.DB == nil {
		return false, nil
	}
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		return false, nil
	}
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM fb_runs WHERE session_id=? AND status=?)`, sid, string(RunStatusWaitingAction)).Scan(&n)
	if err != nil {
		return false, err
	}
	return n != 0, nil
}

func (s *RunStore) FirstWaitingRunIDForSession(ctx context.Context, sessionID string) (string, error) {
	if s == nil || s.DB == nil {
		return "", nil
	}
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		return "", nil
	}
	var rid string
	err := s.DB.QueryRowContext(ctx, `
SELECT id FROM fb_runs WHERE session_id=? AND status=? ORDER BY updated_at ASC LIMIT 1`,
		sid, string(RunStatusWaitingAction)).Scan(&rid)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(rid), nil
}

func (s *RunStore) FindRunByAction(ctx context.Context, actionID string) (string, *Wait, error) {
	if s == nil || s.DB == nil {
		return "", nil, fmt.Errorf("nil db")
	}
	w, err := scanWait(s.DB.QueryRowContext(ctx, `
SELECT `+waitColumns+`
FROM fb_run_waits WHERE action_id=?`, actionID))
	if err == sql.ErrNoRows {
		return "", nil, ErrRunNotFound
	}
	if err != nil {
		return "", nil, err
	}
	w.SessionSnapshot = ParseWaitSessionSnapshot(w.SessionSnapshotJSON)
	return w.RunID, &w, nil
}

// waitRecoveryScan builds the recovery scan over waits whose action has been
// decided, oldest first, optionally restricted to one conversation.
//
// The restriction is what keeps a report where it belongs. Recovery does not
// only tidy rows: it fails the run it recovers and tells a user their approval
// was interrupted. A surface that speaks for one conversation therefore passes
// that conversation's id, and continuations belonging to other conversations
// stay in the outbox until the surface that owns them opens. The empty string
// scans every session and belongs to a multi-session server, which publishes
// each report onto the event stream of the session that owns it instead.
//
// Ancestry, not session_id alone, is the boundary: a subagent's run records the
// agent session that dispatched it, so a nested subagent's run carries its
// parent's worker session rather than the conversation. Walking parent_run_id
// down from the conversation's own runs reaches every one of them.
func waitRecoveryScan(columns, sessionID string, limit int) (string, []any) {
	sessionID = strings.TrimSpace(sessionID)
	if limit > 5000 {
		limit = 5000
	}
	query, args := "", []any{}
	if sessionID != "" {
		query = `
WITH RECURSIVE conversation_runs(id) AS (
	SELECT id FROM fb_runs WHERE session_id=?
	UNION
	SELECT r.id FROM fb_runs r JOIN conversation_runs c ON r.parent_run_id=c.id
)`
		args = append(args, sessionID)
	}
	query += "\nSELECT " + columns + `
FROM fb_run_waits w
JOIN fb_actions a ON a.id=w.action_id`
	if sessionID != "" {
		query += `
JOIN conversation_runs c ON c.id=w.run_id`
	}
	query += `
WHERE a.status<>?
	ORDER BY w.updated_at ASC, w.action_id ASC`
	args = append(args, string(ActionPending))
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	return query, args
}

// ListResolvedWaitActionIDs finds decisions that were committed while their
// continuation snapshot still exists, and that no live process is already
// driving. A fresh process uses this as its durable approval outbox:
// ClaimResume supplies the unique execution claim and the wait is deleted only
// after the resumed run settles.
//
// The lease test belongs here rather than in each caller: every forebrain on this
// machine shares one state database, and recovery reaches decisions this scan
// does not resume but ends — a cancelled, expired or errored action fails its
// run outright. Without it, a second instance ends runs the first is still
// driving.
//
// sessionID is the conversation boundary; see waitRecoveryScan.
func (s *RunStore) ListResolvedWaitActionIDs(ctx context.Context, sessionID string, limit int) ([]string, error) {
	if s == nil || s.DB == nil {
		return nil, fmt.Errorf("nil db")
	}
	query, args := waitRecoveryScan(`DISTINCT w.action_id, w.resume_owner, w.resume_claimed_at_ms, w.resume_phase`, sessionID, limit)
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := time.Now().UTC()
	var ids []string
	for rows.Next() {
		var id string
		var wait Wait
		var claimedAt sql.NullInt64
		if err := rows.Scan(&id, &wait.ResumeOwner, &claimedAt, &wait.ResumePhase); err != nil {
			return nil, err
		}
		if claimedAt.Valid {
			wait.ResumeClaimedAt = claimedAt.Int64
		}
		if wait.ResumePhase == WaitResumePhaseExecutionStarted || wait.ResumePhase == WaitResumePhaseUncertain {
			continue
		}
		if wait.resumeLeaseFresh(now) {
			continue
		}
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	return ids, rows.Err()
}

// ListUncertainResolvedWaits returns continuations whose durable execution
// fence was crossed but whose wait was never cleared, and whose owner has
// stopped renewing its lease. They may have performed an external side effect,
// so startup recovery must surface them as interrupted instead of invoking the
// tool again.
//
// The lease is what separates an abandoned continuation from a live one. Every
// forebrain on this machine shares one state database, so without that test a
// second instance reads the first instance's running tool as a dead process's
// leftovers: it reports an interrupted approval the user never had, and fails a
// run the other process is still driving.
//
// sessionID is the conversation boundary; see waitRecoveryScan.
func (s *RunStore) ListUncertainResolvedWaits(ctx context.Context, sessionID string, limit int) ([]Wait, error) {
	if s == nil || s.DB == nil {
		return nil, fmt.Errorf("nil db")
	}
	query, args := waitRecoveryScan(`w.`+strings.Join(strings.Split(waitColumns, ", "), ", w."), sessionID, limit)
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := time.Now().UTC()
	var out []Wait
	for rows.Next() {
		wait, err := scanWait(rows)
		if err != nil {
			return nil, err
		}
		wait.SessionSnapshot = ParseWaitSessionSnapshot(wait.SessionSnapshotJSON)
		if wait.ResumePhase == WaitResumePhaseExecutionStarted && !wait.resumeLeaseFresh(now) {
			out = append(out, wait)
		}
	}
	return out, rows.Err()
}

// MarkWaitResumeOwner fences the generic resume path while an in-process
// subagent loop owns the continuation. The marker remains durable; a new
// process has a different owner and can recover a resolved wait.
func (s *RunStore) MarkWaitResumeOwner(ctx context.Context, runID, actionID, owner string) error {
	_, owned, found, err := s.claimWaitResume(ctx, runID, actionID, owner, true)
	if err != nil {
		return err
	}
	if !found {
		return ErrNoWaitContinuation
	}
	if !owned {
		return fmt.Errorf("approval continuation is owned by another live process")
	}
	return nil
}

// ClaimWaitResume atomically acquires an unowned or stale durable continuation
// lease. It returns false while another live owner holds the row, when this
// owner already claimed it, or when the row changed before the compare-and-swap.
// Callers must acquire this claim before executing a recovery continuation;
// in-memory run-controller claims alone cannot fence multiple processes that
// share one state database.
func (s *RunStore) ClaimWaitResume(ctx context.Context, runID, actionID, owner string) (bool, error) {
	claimed, _, _, err := s.claimWaitResume(ctx, runID, actionID, owner, false)
	return claimed, err
}

// WaitResumeOwnedBy verifies the durable fence immediately before an owner
// crosses the approval gate. It intentionally does not refresh the lease.
func (s *RunStore) WaitResumeOwnedBy(ctx context.Context, runID, actionID, owner string) (bool, error) {
	if s == nil || s.DB == nil {
		return false, fmt.Errorf("nil db")
	}
	var owned bool
	err := s.DB.QueryRowContext(ctx,
		`SELECT resume_owner = ? FROM fb_run_waits WHERE run_id=? AND action_id=?`,
		strings.TrimSpace(owner), strings.TrimSpace(runID), strings.TrimSpace(actionID)).Scan(&owned)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return owned, nil
}

// ReleaseWaitResumeOwner relinquishes a claim that failed before execution
// started. The owner and phase predicates are the whole decision in one
// UPDATE: a row another process owns, or one that crossed its execution
// fence, does not move. Zero rows then distinguish "not this owner's row"
// (nothing to do) from the fenced refusal.
func (s *RunStore) ReleaseWaitResumeOwner(ctx context.Context, runID, actionID, owner string) error {
	if s == nil || s.DB == nil {
		return fmt.Errorf("nil db")
	}
	owner = strings.TrimSpace(owner)
	res, err := s.DB.ExecContext(ctx, `
UPDATE fb_run_waits
SET resume_owner='', resume_claimed_at_ms=NULL, resume_phase='', updated_at=?
WHERE run_id=? AND action_id=? AND resume_owner=? AND resume_phase NOT IN (?, ?)`,
		time.Now().Unix(), strings.TrimSpace(runID), strings.TrimSpace(actionID), owner,
		WaitResumePhaseExecutionStarted, WaitResumePhaseUncertain)
	if err != nil {
		return err
	}
	if changed, _ := res.RowsAffected(); changed == 1 {
		return nil
	}
	var resumeOwner, resumePhase string
	err = s.DB.QueryRowContext(ctx,
		`SELECT resume_owner, resume_phase FROM fb_run_waits WHERE run_id=? AND action_id=?`,
		strings.TrimSpace(runID), strings.TrimSpace(actionID)).Scan(&resumeOwner, &resumePhase)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	if resumeOwner != owner {
		return nil
	}
	return fmt.Errorf("approval continuation already crossed its execution fence")
}

// BeginWaitResumeExecution atomically crosses the point after which automatic
// replay is unsafe. The owner predicate is the fencing token: if a stale
// process lost its lease, it cannot start the tool after the new owner wins.
func (s *RunStore) BeginWaitResumeExecution(ctx context.Context, runID, actionID, owner string) error {
	if s == nil || s.DB == nil {
		return fmt.Errorf("nil db")
	}
	runID, actionID, owner = strings.TrimSpace(runID), strings.TrimSpace(actionID), strings.TrimSpace(owner)
	if runID == "" || actionID == "" || owner == "" {
		return fmt.Errorf("run id, action id, and resume owner are required")
	}
	res, err := s.DB.ExecContext(ctx, `
UPDATE fb_run_waits
SET resume_phase=?, resume_claimed_at_ms=?, updated_at=?
WHERE run_id=? AND action_id=? AND resume_owner=? AND resume_phase NOT IN (?, ?)`,
		WaitResumePhaseExecutionStarted, time.Now().UTC().UnixMilli(), time.Now().Unix(),
		runID, actionID, owner, WaitResumePhaseExecutionStarted, WaitResumePhaseUncertain)
	if err != nil {
		return err
	}
	if changed, _ := res.RowsAffected(); changed == 1 {
		return nil
	}
	var resumeOwner, resumePhase string
	err = s.DB.QueryRowContext(ctx,
		`SELECT resume_owner, resume_phase FROM fb_run_waits WHERE run_id=? AND action_id=?`,
		runID, actionID).Scan(&resumeOwner, &resumePhase)
	if err == sql.ErrNoRows {
		return ErrRunNotFound
	}
	if err != nil {
		return err
	}
	if strings.TrimSpace(resumeOwner) != owner {
		return fmt.Errorf("approval continuation fence is not owned by this process")
	}
	return fmt.Errorf("approval continuation execution already started")
}

// MarkWaitResumeUncertain makes startup classification idempotent while
// retaining the original action, tool input and snapshot for diagnosis. A row
// already marked, or already gone, is success.
func (s *RunStore) MarkWaitResumeUncertain(ctx context.Context, runID, actionID string) error {
	if s == nil || s.DB == nil {
		return fmt.Errorf("nil db")
	}
	res, err := s.DB.ExecContext(ctx, `
UPDATE fb_run_waits SET resume_phase=?, updated_at=?
WHERE run_id=? AND action_id=? AND resume_phase=?`,
		WaitResumePhaseUncertain, time.Now().Unix(), strings.TrimSpace(runID), strings.TrimSpace(actionID), WaitResumePhaseExecutionStarted)
	if err != nil {
		return err
	}
	if changed, _ := res.RowsAffected(); changed == 1 {
		return nil
	}
	var resumePhase string
	err = s.DB.QueryRowContext(ctx,
		`SELECT resume_phase FROM fb_run_waits WHERE run_id=? AND action_id=?`,
		strings.TrimSpace(runID), strings.TrimSpace(actionID)).Scan(&resumePhase)
	if err == sql.ErrNoRows || resumePhase == WaitResumePhaseUncertain {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("approval continuation has not started execution")
}

// claimWaitResume is the durable continuation lease claim. Every fencing
// rule is a predicate of one UPDATE — a protected phase only its own owner
// may refresh, an unowned or timestamp-less or stale lease claimable by
// anyone — so a second process never sees a half-written claim. When the
// statement matches nothing, one SELECT reports whether the continuation
// exists and who holds it, which is the (found, owned) answer the callers
// turn into their own errors.
func (s *RunStore) claimWaitResume(ctx context.Context, runID, actionID, owner string, refreshSame bool) (claimed bool, owned bool, found bool, err error) {
	if s == nil || s.DB == nil {
		return false, false, false, fmt.Errorf("nil db")
	}
	runID = strings.TrimSpace(runID)
	actionID = strings.TrimSpace(actionID)
	owner = strings.TrimSpace(owner)
	if runID == "" || actionID == "" || owner == "" {
		return false, false, false, fmt.Errorf("run id, action id, and resume owner are required")
	}
	now := time.Now().UTC()
	staleBefore := now.UnixMilli() - WaitResumeLease.Milliseconds()
	res, err := s.DB.ExecContext(ctx, `
UPDATE fb_run_waits
SET resume_owner = ?, resume_claimed_at_ms = ?,
    resume_phase = CASE WHEN resume_phase = '' THEN ? ELSE resume_phase END,
    updated_at = ?
WHERE run_id = ? AND action_id = ?
  AND (
    (? AND resume_owner = ?)
    OR (resume_phase NOT IN (?, ?)
        AND (resume_owner = '' OR resume_claimed_at_ms IS NULL OR resume_claimed_at_ms < ?))
  )`,
		owner, now.UnixMilli(), WaitResumePhaseClaimed, now.Unix(),
		runID, actionID,
		refreshSame, owner,
		WaitResumePhaseExecutionStarted, WaitResumePhaseUncertain, staleBefore)
	if err != nil {
		return false, false, true, err
	}
	if changed, _ := res.RowsAffected(); changed == 1 {
		return true, true, true, nil
	}
	var resumeOwner string
	err = s.DB.QueryRowContext(ctx,
		`SELECT resume_owner FROM fb_run_waits WHERE run_id=? AND action_id=?`,
		runID, actionID).Scan(&resumeOwner)
	if err == sql.ErrNoRows {
		return false, false, false, nil
	}
	if err != nil {
		return false, false, true, err
	}
	return false, strings.TrimSpace(resumeOwner) == owner, true, nil
}

func (s *RunStore) ClearWait(ctx context.Context, runID string) error {
	if s == nil || s.DB == nil {
		return fmt.Errorf("nil db")
	}
	_, err := s.DB.ExecContext(ctx, `DELETE FROM fb_run_waits WHERE run_id=?`, runID)
	return err
}

func (s *RunStore) GetWaitForRun(ctx context.Context, runID string) (*Wait, error) {
	if s == nil || s.DB == nil {
		return nil, fmt.Errorf("nil db")
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, nil
	}
	w, err := scanWait(s.DB.QueryRowContext(ctx, `
SELECT `+waitColumns+`
FROM fb_run_waits WHERE run_id=?`, runID))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	w.SessionSnapshot = ParseWaitSessionSnapshot(w.SessionSnapshotJSON)
	return &w, nil
}

// SetStatus records where the run stands. A failure's text is not stored with
// it: the failure is an event the surface that ran the turn publishes, exactly
// once, not something the storage layer authors.
//
// A run ends once. The condition below is the same first-writer-wins rule
// StampRunTiming applies to the clock: a terminal row is never moved, not to
// another terminal state and not back to running, and moving a run back to
// running (an approval resume) is only legal from the two alive states. The
// caller's signature is unchanged; a transition the rule refuses simply
// changes nothing.
func (s *RunStore) SetStatus(ctx context.Context, runID string, st RunStatus) error {
	if s == nil || s.DB == nil {
		return fmt.Errorf("nil db")
	}
	switch st {
	case RunStatusRunning:
		// Resuming past an approval re-stamps the driver: the lease that
		// vouches for this run now belongs to whoever continued it.
		_, err := s.DB.ExecContext(ctx, `
UPDATE fb_runs SET status=?, owner=?, updated_at=? WHERE id=? AND status IN (?, ?)`,
			string(st), strings.TrimSpace(s.Owner), time.Now().Unix(), runID,
			string(RunStatusWaitingAction), string(RunStatusRunning))
		return err
	case RunStatusDone, RunStatusFailed, RunStatusCancelled:
		_, err := s.DB.ExecContext(ctx, `
UPDATE fb_runs SET status=?, updated_at=? WHERE id=? AND status IN (?, ?)`,
			string(st), time.Now().Unix(), runID, string(RunStatusRunning), string(RunStatusWaitingAction))
		return err
	case RunStatusWaitingAction:
		_, err := s.DB.ExecContext(ctx, `
UPDATE fb_runs SET status=?, updated_at=? WHERE id=? AND status=?`,
			string(st), time.Now().Unix(), runID, string(RunStatusRunning))
		return err
	default:
		return fmt.Errorf("unknown run status %q", st)
	}
}

// SessionHasLiveRun reports whether the session holds a live primary run —
// the same predicate CreateRun refuses a second one for.
func (s *RunStore) SessionHasLiveRun(ctx context.Context, sessionID string) (bool, error) {
	if s == nil || s.DB == nil {
		return false, fmt.Errorf("nil db")
	}
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		return false, nil
	}
	var live int
	err := s.DB.QueryRowContext(ctx, `
SELECT EXISTS(
  SELECT 1 FROM fb_runs r
  WHERE r.session_id=? AND r.parent_run_id IS NULL
    AND `+fmt.Sprintf(livePrimaryRunCondition, "r")+`)`,
		append([]any{sid}, liveRunArgs(time.Now())...)...).Scan(&live)
	if err != nil {
		return false, err
	}
	return live != 0, nil
}

// FirstPrimaryRunID returns the session's oldest primary run. A fire's run is
// the first primary run of its session — the fire's session is born empty,
// with nothing else in it — which is what makes one session name the fire's
// run from before the run exists until after it ends. Subagent runs are not
// the conversation's; a session with no primary run has none.
func (s *RunStore) FirstPrimaryRunID(ctx context.Context, sessionID string) (string, error) {
	if s == nil || s.DB == nil {
		return "", fmt.Errorf("nil db")
	}
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		return "", nil
	}
	var id string
	err := s.DB.QueryRowContext(ctx, `
SELECT id FROM fb_runs WHERE session_id=? AND parent_run_id IS NULL ORDER BY created_at ASC, rowid ASC LIMIT 1`, sid).Scan(&id)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return id, nil
}

// HoldOwnerLease registers this store's owner and keeps its heartbeat fresh
// until the returned stop is called. Registration happens synchronously — a
// caller that ignored an error here would create runs no lease vouches for.
// Renewal failures are logged and retried on the next beat: one missed write
// must not deregister a live process. stop ends the loop and deletes the
// owner's row, so any run this process failed to settle is immediately
// readable as abandoned.
func (s *RunStore) HoldOwnerLease(ctx context.Context) (stop func(), err error) {
	if s == nil || s.DB == nil {
		return nil, fmt.Errorf("nil db")
	}
	owner := strings.TrimSpace(s.Owner)
	if owner == "" {
		return nil, fmt.Errorf("a run store without an owner cannot hold a lease")
	}
	renew := func(ctx context.Context) error {
		_, err := s.DB.ExecContext(ctx, `
INSERT INTO fb_run_owners(owner, heartbeat_at_ms) VALUES(?, ?)
ON CONFLICT(owner) DO UPDATE SET heartbeat_at_ms=excluded.heartbeat_at_ms`,
			owner, time.Now().UnixMilli())
		return err
	}
	if err := renew(ctx); err != nil {
		return nil, err
	}
	loopCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(RunOwnerLease / 6)
		defer ticker.Stop()
		for {
			select {
			case <-loopCtx.Done():
				return
			case <-ticker.C:
				if err := renew(loopCtx); err != nil {
					slog.Warn("run owner lease renewal failed", "owner", owner, "err", err)
				}
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			<-done
			if _, err := s.DB.ExecContext(context.Background(), `DELETE FROM fb_run_owners WHERE owner=?`, owner); err != nil {
				slog.Warn("run owner lease deregistration failed", "owner", owner, "err", err)
			}
		})
	}, nil
}

// AbandonedRun is a run reaped because the process driving it stopped
// renewing its lease. FinishedAtMs is the clock the reap stamped on it — the
// moment its owner was last seen — which is what a surface computes the
// run's final duration from.
type AbandonedRun struct {
	ID, SessionID, ParentRunID, Owner string
	FinishedAtMs                      int64
}

// ReapAbandonedRuns ends every running run whose owner's lease has lapsed —
// failed, with its clock stamped from when it started to when its owner was
// last seen — and returns them for the caller to report. A run with an
// approval continuation row is skipped: the continuation recovery
// (ListUncertainResolvedWaits) owns it and knows whether its tool ran.
func (s *RunStore) ReapAbandonedRuns(ctx context.Context, now time.Time) ([]AbandonedRun, error) {
	if s == nil || s.DB == nil {
		return nil, fmt.Errorf("nil db")
	}
	staleBefore := now.UnixMilli() - RunOwnerLease.Milliseconds()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `
SELECT id, session_id, parent_run_id, owner FROM fb_runs
WHERE status=?
  AND NOT EXISTS (SELECT 1 FROM fb_run_waits w WHERE w.run_id=fb_runs.id)
  AND (owner=''
       OR NOT EXISTS (SELECT 1 FROM fb_run_owners o WHERE o.owner=fb_runs.owner)
       OR (SELECT o.heartbeat_at_ms FROM fb_run_owners o WHERE o.owner=fb_runs.owner) < ?)
ORDER BY created_at ASC, id ASC`,
		string(RunStatusRunning), staleBefore)
	if err != nil {
		return nil, err
	}
	var candidates []AbandonedRun
	for rows.Next() {
		var r AbandonedRun
		var parent sql.NullString
		if err := rows.Scan(&r.ID, &r.SessionID, &parent, &r.Owner); err != nil {
			rows.Close()
			return nil, err
		}
		r.ParentRunID = parent.String
		candidates = append(candidates, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	// The clock's end is the owner's last heartbeat — when its process was
	// last seen — falling back to the row's own last write when no owner row
	// exists at all. Every expression below reads the pre-update row, so the
	// updated_at this reap writes cannot feed its own arithmetic; COALESCE
	// keeps a clock a surface already stamped, because a stamp is written
	// only once a run has ended, which makes it a better end than the lease
	// fallback. A row like that — clock stamped, status still running — is
	// exactly the process dying between the two writes, and it is reaped
	// like any other dead one. RETURNING yields the stamped end only for
	// the row this compare-and-swap won.
	const clockExpr = `MAX(COALESCE((SELECT o.heartbeat_at_ms FROM fb_run_owners o WHERE o.owner=fb_runs.owner), updated_at*1000), created_at*1000)`
	out := make([]AbandonedRun, 0, len(candidates))
	for _, r := range candidates {
		var finished int64
		err := tx.QueryRowContext(ctx, `
UPDATE fb_runs
SET status=?, updated_at=?,
    started_at_ms=COALESCE(started_at_ms, created_at*1000),
    finished_at_ms=COALESCE(finished_at_ms, `+clockExpr+`),
    worked_ms=COALESCE(worked_ms, `+clockExpr+`-created_at*1000)
WHERE id=? AND status=? AND owner=?
RETURNING finished_at_ms`,
			string(RunStatusFailed), now.Unix(), r.ID, string(RunStatusRunning), r.Owner).Scan(&finished)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return nil, err
		}
		r.FinishedAtMs = finished
		out = append(out, r)
	}
	// A lapsed lease reads as dead already; removing its rows keeps the
	// table from collecting one per process that ever crashed mid-run.
	if _, err := tx.ExecContext(ctx, `DELETE FROM fb_run_owners WHERE heartbeat_at_ms < ?`, staleBefore); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

// ActivePrimaryRun returns the agent's newest top-level run that is still
// running or waiting on an action — the run a cancel/stop command targets.
// The tenant JOIN and the partial index keep it one indexed lookup instead of
// a global recent-runs scan that crosses every tenant and truncates at an
// arbitrary LIMIT.
func (s *RunStore) ActivePrimaryRun(ctx context.Context, agentID string) (Run, bool, error) {
	if s == nil || s.DB == nil {
		return Run{}, false, fmt.Errorf("nil db")
	}
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return Run{}, false, nil
	}
	r, err := scanRun(s.DB.QueryRowContext(ctx, `
SELECT `+prefixedRunColumns("r")+`
FROM fb_runs r JOIN fb_sessions s ON s.id = r.session_id
WHERE s.agent_id = ? AND r.parent_run_id IS NULL AND r.status IN (?, ?)
ORDER BY r.updated_at DESC LIMIT 1`, agentID, string(RunStatusRunning), string(RunStatusWaitingAction)))
	if err == sql.ErrNoRows {
		return Run{}, false, nil
	}
	if err != nil {
		return Run{}, false, err
	}
	return r, true, nil
}

func (s *RunStore) ListRunsBySession(ctx context.Context, sessionID string, limit int) ([]Run, error) {
	if s == nil || s.DB == nil {
		return nil, fmt.Errorf("nil db")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return []Run{}, nil
	}
	if limit <= 0 {
		limit = 30
	}
	if limit > 5000 {
		limit = 5000
	}
	rows, err := s.DB.QueryContext(ctx, `
SELECT `+runColumns+`
FROM fb_runs WHERE session_id=? ORDER BY updated_at DESC, created_at DESC LIMIT ?`, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Run, 0, limit)
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *RunStore) ListChildRuns(ctx context.Context, parentRunID string, limit int, statuses ...RunStatus) ([]Run, error) {
	if s == nil || s.DB == nil {
		return nil, fmt.Errorf("nil db")
	}
	parentRunID = strings.TrimSpace(parentRunID)
	if parentRunID == "" {
		return []Run{}, nil
	}
	if limit <= 0 {
		limit = 30
	}
	if limit > 500 {
		limit = 500
	}
	args := []any{parentRunID}
	var b strings.Builder
	b.WriteString(`
SELECT ` + runColumns + `
FROM fb_runs
WHERE parent_run_id=?`)
	if len(statuses) > 0 {
		b.WriteString(` AND status IN (`)
		for i, st := range statuses {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString("?")
			args = append(args, string(st))
		}
		b.WriteString(")")
	}
	b.WriteString(` ORDER BY updated_at DESC, created_at DESC LIMIT ?`)
	args = append(args, limit)
	rows, err := s.DB.QueryContext(ctx, b.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Run, 0, limit)
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
