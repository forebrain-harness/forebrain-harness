package state

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Cron job statuses. They describe the last fire, not the job's configuration,
// so a paused job keeps the status of the run before it was paused.
const (
	CronStatusOK            = "ok"
	CronStatusFailed        = "failed"
	CronStatusRunning       = "running"
	CronStatusDeliveryError = "delivery_failed"
)

// CronJob is one standing instruction for a primary agent: a schedule, a
// prompt, and where the answer goes. Each fire runs in its own fresh session,
// so a job never inherits the state of the run before it.
type CronJob struct {
	ID string `json:"id"`
	Ag string `json:"-"`
	// ProjectID binds a job to one project; empty is an agent-wide job.
	ProjectID string `json:"project_id,omitempty"`
	Name      string `json:"name"`
	Sched     string `json:"schedule"`
	Prompt    string `json:"prompt"`
	// Deliver names the channel the answer is sent to, empty for local-only:
	// the run is still recorded and readable, it just is not pushed anywhere.
	Deliver string `json:"deliver"`
	Enabled bool   `json:"enabled"`
	// RepeatLimit caps how many times the job may fire, 0 for unlimited. A
	// one-shot schedule ("in 30m") is a limit of one.
	RepeatLimit int `json:"repeat_limit"`
	RunCount    int `json:"run_count"`
	// NextRunAt is nil when the job will not fire again — paused, or a
	// one-shot that has been consumed.
	NextRunAt     *int64 `json:"next_run_at,omitempty"`
	LastRunAt     *int64 `json:"last_run_at,omitempty"`
	LastStatus    string `json:"last_status,omitempty"`
	LastError     string `json:"last_error,omitempty"`
	LastOutput    string `json:"last_output,omitempty"`
	FailureStreak int    `json:"failure_streak"`
	CreatedAt     int64  `json:"created_at"`
	UpdatedAt     int64  `json:"updated_at"`
}

// AgentID is the primary agent (tenant) this job belongs to.
func (j CronJob) AgentID() string { return j.Ag }

// CronRun is one fire of a job, kept after the job itself is edited or removed
// so the history stays readable.
type CronRun struct {
	ID          int64  `json:"id"`
	JobID       string `json:"job_id"`
	AgentID     string `json:"agent_id"`
	SessionID   string `json:"session_id,omitempty"`
	Trigger     string `json:"trigger"`
	Status      string `json:"status"`
	Output      string `json:"output,omitempty"`
	Error       string `json:"error,omitempty"`
	DeliveredTo string `json:"delivered_to,omitempty"`
	StartedAt   int64  `json:"started_at"`
	FinishedAt  *int64 `json:"finished_at,omitempty"`
}

// Heartbeat is a recurring instruction inside one conversation. Unlike a cron
// job it has no session of its own: it fires into the session it belongs to,
// which is what lets it see the context that conversation has built up.
//
// NextRunAt is nil when the heartbeat is paused: the schedule and the pause
// flag are the same fact, so a caller reads one field to know both.
type Heartbeat struct {
	SessionID   string `json:"session_id"`
	IntervalSec int    `json:"interval_seconds"`
	Prompt      string `json:"prompt"`
	LastFiredAt *int64 `json:"last_fired_at,omitempty"`
	NextRunAt   *int64 `json:"next_run_at,omitempty"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

// CronStore persists cron jobs, their run history, and per-session heartbeats.
type CronStore struct {
	DB *sql.DB
}

func (s *CronStore) ok() bool { return s != nil && s.DB != nil }

// InsertJob creates a job. The caller owns the id and the computed NextRunAt:
// the store records what it is told rather than re-deriving a schedule it does
// not parse.
func (s *CronStore) InsertJob(ctx context.Context, job CronJob) error {
	if !s.ok() {
		return fmt.Errorf("cron store unavailable")
	}
	if strings.TrimSpace(job.ID) == "" {
		return fmt.Errorf("cron job id required")
	}
	if strings.TrimSpace(job.Ag) == "" {
		return fmt.Errorf("cron job agent id required")
	}
	now := time.Now().Unix()
	if job.CreatedAt <= 0 {
		job.CreatedAt = now
	}
	job.UpdatedAt = now
	_, err := s.DB.ExecContext(ctx, `
INSERT INTO fb_cron_jobs (id, agent_id, project_id, name, schedule, prompt, deliver, enabled, repeat_limit,
  run_count, next_run_at, last_run_at, last_status, last_error, last_output, failure_streak, created_at, updated_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		strings.TrimSpace(job.ID), strings.TrimSpace(job.Ag), nullIfEmpty(job.ProjectID), strings.TrimSpace(job.Name),
		strings.TrimSpace(job.Sched), job.Prompt, strings.TrimSpace(job.Deliver), boolToInt(job.Enabled),
		job.RepeatLimit, job.RunCount, job.NextRunAt, job.LastRunAt, job.LastStatus, job.LastError,
		job.LastOutput, job.FailureStreak, job.CreatedAt, job.UpdatedAt)
	return err
}

// UpdateJobConfig writes a job's user-facing configuration, leaving its
// schedule position (next_run_at, run_count) and last result to the scheduler.
// The agent predicate keeps one tenant from editing another's job.
func (s *CronStore) UpdateJobConfig(ctx context.Context, job CronJob) error {
	if !s.ok() {
		return fmt.Errorf("cron store unavailable")
	}
	if strings.TrimSpace(job.ID) == "" {
		return fmt.Errorf("cron job id required")
	}
	if strings.TrimSpace(job.Ag) == "" {
		return fmt.Errorf("cron job agent id required")
	}
	_, err := s.DB.ExecContext(ctx, `
UPDATE fb_cron_jobs
SET project_id=?, name=?, schedule=?, prompt=?, deliver=?, enabled=?, repeat_limit=?, next_run_at=?, updated_at=?
WHERE id=? AND agent_id=?`,
		nullIfEmpty(job.ProjectID), strings.TrimSpace(job.Name), strings.TrimSpace(job.Sched), job.Prompt,
		strings.TrimSpace(job.Deliver), boolToInt(job.Enabled), job.RepeatLimit, job.NextRunAt, time.Now().Unix(),
		strings.TrimSpace(job.ID), strings.TrimSpace(job.Ag))
	return err
}

// ClaimDueJob advances a due job's schedule before it fires and reports whether
// this caller won the claim. The CAS on next_run_at is what keeps two
// schedulers — a cluster's replicas, or a restart overlapping a running tick —
// from firing the same occurrence: only the process that moves the value the
// due query read from fires. run_count moves with it.
func (s *CronStore) ClaimDueJob(ctx context.Context, job CronJob, dueAt int64, next *int64) (bool, error) {
	if !s.ok() {
		return false, fmt.Errorf("cron store unavailable")
	}
	res, err := s.DB.ExecContext(ctx, `
UPDATE fb_cron_jobs
SET next_run_at=?, run_count=run_count+1, last_status=?, updated_at=?
WHERE id=? AND agent_id=? AND next_run_at=?`,
		next, CronStatusRunning, time.Now().Unix(), strings.TrimSpace(job.ID), strings.TrimSpace(job.Ag), dueAt)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// RecordOutcome writes a fired job's result. It touches only the result columns:
// a pause or an edit made while the job was running stands, and a job deleted
// while it ran updates nothing (0 rows) rather than being recreated.
func (s *CronStore) RecordOutcome(ctx context.Context, jobID, status, output, errText string, at int64) error {
	if !s.ok() {
		return fmt.Errorf("cron store unavailable")
	}
	_, err := s.DB.ExecContext(ctx, `
UPDATE fb_cron_jobs
SET last_run_at=?, last_status=?, last_error=?, last_output=?,
    failure_streak=CASE WHEN ?=? THEN failure_streak+1 WHEN ?=? THEN 0 ELSE failure_streak END,
    updated_at=?
WHERE id=?`,
		at, status, errText, output,
		status, CronStatusFailed, status, CronStatusOK, time.Now().Unix(), strings.TrimSpace(jobID))
	return err
}

func (s *CronStore) GetJob(ctx context.Context, id string) (*CronJob, error) {
	if !s.ok() {
		return nil, fmt.Errorf("cron store unavailable")
	}
	rows, err := s.DB.QueryContext(ctx, cronJobSelect+` WHERE id = ?`, strings.TrimSpace(id))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs, err := scanCronJobs(rows)
	if err != nil {
		return nil, err
	}
	if len(jobs) == 0 {
		return nil, nil
	}
	return &jobs[0], nil
}

// ListJobs returns one agent's jobs, newest first.
func (s *CronStore) ListJobs(ctx context.Context, agentID string) ([]CronJob, error) {
	if !s.ok() {
		return nil, fmt.Errorf("cron store unavailable")
	}
	rows, err := s.DB.QueryContext(ctx, cronJobSelect+` WHERE agent_id = ? ORDER BY created_at DESC`, strings.TrimSpace(agentID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCronJobs(rows)
}

// ListJobsForProject returns one project's jobs for the agent, newest first.
// The empty project returns the agent's unbound jobs, whose project is NULL.
func (s *CronStore) ListJobsForProject(ctx context.Context, agentID, projectID string) ([]CronJob, error) {
	if !s.ok() {
		return nil, fmt.Errorf("cron store unavailable")
	}
	rows, err := s.DB.QueryContext(ctx, cronJobSelect+` WHERE agent_id = ? AND project_id IS NULLIF(?, '') ORDER BY created_at DESC`,
		strings.TrimSpace(agentID), strings.TrimSpace(projectID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCronJobs(rows)
}

// DueJobs returns the agent's jobs whose next fire has arrived. A job with a
// NULL next_run_at is paused or spent and is never returned; enabled is the
// user's intent, not the scheduler's gate, so it is not consulted here.
func (s *CronStore) DueJobs(ctx context.Context, agentID string, at time.Time) ([]CronJob, error) {
	if !s.ok() {
		return nil, fmt.Errorf("cron store unavailable")
	}
	rows, err := s.DB.QueryContext(ctx, cronJobSelect+`
WHERE agent_id = ? AND next_run_at IS NOT NULL AND next_run_at <= ?
ORDER BY next_run_at ASC`, strings.TrimSpace(agentID), at.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCronJobs(rows)
}

func (s *CronStore) DeleteJob(ctx context.Context, id string) error {
	if !s.ok() {
		return fmt.Errorf("cron store unavailable")
	}
	_, err := s.DB.ExecContext(ctx, `DELETE FROM fb_cron_jobs WHERE id = ?`, strings.TrimSpace(id))
	return err
}

// StartRun opens a run record and returns its id, so the fire is visible while
// it is still going rather than only once it finishes.
func (s *CronStore) StartRun(ctx context.Context, run CronRun) (int64, error) {
	if !s.ok() {
		return 0, fmt.Errorf("cron store unavailable")
	}
	if run.StartedAt <= 0 {
		run.StartedAt = time.Now().Unix()
	}
	if strings.TrimSpace(run.Status) == "" {
		run.Status = CronStatusRunning
	}
	res, err := s.DB.ExecContext(ctx, `
INSERT INTO fb_cron_runs (job_id, agent_id, session_id, trigger, status, started_at)
VALUES (?,?,?,?,?,?)`,
		strings.TrimSpace(run.JobID), strings.TrimSpace(run.AgentID),
		strings.TrimSpace(run.SessionID), strings.TrimSpace(run.Trigger), run.Status, run.StartedAt)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *CronStore) FinishRun(ctx context.Context, id int64, status, output, errText, deliveredTo string) error {
	if !s.ok() {
		return fmt.Errorf("cron store unavailable")
	}
	_, err := s.DB.ExecContext(ctx, `
UPDATE fb_cron_runs SET status = ?, output = ?, error = ?, delivered_to = ?, finished_at = ?
WHERE id = ?`, status, output, errText, deliveredTo, time.Now().Unix(), id)
	return err
}

func (s *CronStore) ListRuns(ctx context.Context, jobID string, limit int) ([]CronRun, error) {
	if !s.ok() {
		return nil, fmt.Errorf("cron store unavailable")
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.DB.QueryContext(ctx, `
SELECT id, job_id, agent_id, session_id, trigger, status, output, error, delivered_to, started_at, finished_at
FROM fb_cron_runs WHERE job_id = ? ORDER BY started_at DESC, id DESC LIMIT ?`,
		strings.TrimSpace(jobID), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CronRun{}
	for rows.Next() {
		var r CronRun
		var finished sql.NullInt64
		if err := rows.Scan(&r.ID, &r.JobID, &r.AgentID, &r.SessionID, &r.Trigger,
			&r.Status, &r.Output, &r.Error, &r.DeliveredTo, &r.StartedAt, &finished); err != nil {
			return nil, err
		}
		if finished.Valid {
			v := finished.Int64
			r.FinishedAt = &v
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SaveHeartbeat writes the recurring instruction for one session. It is the
// user's setting — create or replace the interval, prompt and schedule — and is
// not used by the scheduler, which advances the timer with
// ClaimHeartbeat/ReanchorHeartbeat. When the beat last fired is a fact the
// scheduler recorded, not a setting, so replacing the setting keeps it.
func (s *CronStore) SaveHeartbeat(ctx context.Context, hb Heartbeat) error {
	if !s.ok() {
		return fmt.Errorf("cron store unavailable")
	}
	if strings.TrimSpace(hb.SessionID) == "" {
		return fmt.Errorf("heartbeat session id required")
	}
	now := time.Now().Unix()
	if hb.CreatedAt <= 0 {
		hb.CreatedAt = now
	}
	hb.UpdatedAt = now
	_, err := s.DB.ExecContext(ctx, `
INSERT INTO fb_heartbeats (session_id, interval_seconds, prompt, next_run_at, last_fired_at, created_at, updated_at)
VALUES (?,?,?,?,?,?,?)
ON CONFLICT(session_id) DO UPDATE SET
  interval_seconds=excluded.interval_seconds, prompt=excluded.prompt,
  next_run_at=excluded.next_run_at, updated_at=excluded.updated_at`,
		strings.TrimSpace(hb.SessionID), hb.IntervalSec, hb.Prompt, hb.NextRunAt, hb.LastFiredAt, hb.CreatedAt, hb.UpdatedAt)
	return err
}

func (s *CronStore) GetHeartbeat(ctx context.Context, sessionID string) (*Heartbeat, error) {
	if !s.ok() {
		return nil, fmt.Errorf("cron store unavailable")
	}
	rows, err := s.DB.QueryContext(ctx, heartbeatSelect+` WHERE h.session_id = ?`, strings.TrimSpace(sessionID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list, err := scanHeartbeats(rows)
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return &list[0], nil
}

// DueHeartbeats returns one agent's beats whose interval has elapsed. Whether
// the session is actually idle is the scheduler's question, not the store's;
// which tenant a beat belongs to is the store's, resolved through the session
// it hangs off so a scheduler never fires another agent's conversation.
func (s *CronStore) DueHeartbeats(ctx context.Context, agentID string, at time.Time) ([]Heartbeat, error) {
	if !s.ok() {
		return nil, fmt.Errorf("cron store unavailable")
	}
	rows, err := s.DB.QueryContext(ctx, heartbeatSelect+`
WHERE h.next_run_at IS NOT NULL AND h.next_run_at <= ?
  AND EXISTS(SELECT 1 FROM fb_sessions s WHERE s.id = h.session_id AND s.agent_id = ?)
ORDER BY h.next_run_at ASC`, at.Unix(), strings.TrimSpace(agentID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanHeartbeats(rows)
}

// ClaimHeartbeat advances a due beat's next fire before it fires and reports
// whether this caller won the claim, so two schedulers cannot both fire the
// same occurrence. A beat cleared or paused in the meantime matches nothing.
func (s *CronStore) ClaimHeartbeat(ctx context.Context, sessionID string, dueAt, next int64) (bool, error) {
	if !s.ok() {
		return false, fmt.Errorf("cron store unavailable")
	}
	res, err := s.DB.ExecContext(ctx, `
UPDATE fb_heartbeats SET next_run_at=?, updated_at=?
WHERE session_id=? AND next_run_at=?`, next, time.Now().Unix(), strings.TrimSpace(sessionID), dueAt)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// ReanchorHeartbeat re-anchors a beat's next fire and, when it fired, records
// when. The guard is on the beat still being scheduled: one that was cleared or
// paused while its turn ran matches nothing and is not resurrected.
func (s *CronStore) ReanchorHeartbeat(ctx context.Context, sessionID string, next int64, firedAt *int64) (bool, error) {
	if !s.ok() {
		return false, fmt.Errorf("cron store unavailable")
	}
	res, err := s.DB.ExecContext(ctx, `
UPDATE fb_heartbeats
SET next_run_at=?, last_fired_at=COALESCE(?, last_fired_at), updated_at=?
WHERE session_id=? AND next_run_at IS NOT NULL`,
		next, firedAt, time.Now().Unix(), strings.TrimSpace(sessionID))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

func (s *CronStore) DeleteHeartbeat(ctx context.Context, sessionID string) error {
	if !s.ok() {
		return fmt.Errorf("cron store unavailable")
	}
	_, err := s.DB.ExecContext(ctx, `DELETE FROM fb_heartbeats WHERE session_id = ?`, strings.TrimSpace(sessionID))
	return err
}

const cronJobSelect = `
SELECT id, agent_id, project_id, name, schedule, prompt, deliver, enabled, repeat_limit, run_count,
       next_run_at, last_run_at, last_status, last_error, last_output, failure_streak, created_at, updated_at
FROM fb_cron_jobs`

const heartbeatSelect = `
SELECT h.session_id, h.interval_seconds, h.prompt, h.next_run_at, h.last_fired_at, h.created_at, h.updated_at
FROM fb_heartbeats h`

func scanCronJobs(rows *sql.Rows) ([]CronJob, error) {
	out := []CronJob{}
	for rows.Next() {
		var j CronJob
		var enabled int
		var project sql.NullString
		var next, lastRun sql.NullInt64
		if err := rows.Scan(&j.ID, &j.Ag, &project, &j.Name, &j.Sched, &j.Prompt, &j.Deliver, &enabled,
			&j.RepeatLimit, &j.RunCount, &next, &lastRun, &j.LastStatus, &j.LastError,
			&j.LastOutput, &j.FailureStreak, &j.CreatedAt, &j.UpdatedAt); err != nil {
			return nil, err
		}
		j.Enabled = enabled != 0
		j.ProjectID = project.String
		if next.Valid {
			v := next.Int64
			j.NextRunAt = &v
		}
		if lastRun.Valid {
			v := lastRun.Int64
			j.LastRunAt = &v
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

func scanHeartbeats(rows *sql.Rows) ([]Heartbeat, error) {
	out := []Heartbeat{}
	for rows.Next() {
		var hb Heartbeat
		var next, lastFired sql.NullInt64
		if err := rows.Scan(&hb.SessionID, &hb.IntervalSec, &hb.Prompt, &next, &lastFired,
			&hb.CreatedAt, &hb.UpdatedAt); err != nil {
			return nil, err
		}
		if next.Valid {
			v := next.Int64
			hb.NextRunAt = &v
		}
		if lastFired.Valid {
			v := lastFired.Int64
			hb.LastFiredAt = &v
		}
		out = append(out, hb)
	}
	return out, rows.Err()
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
