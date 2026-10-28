// Batch task storage: fb_tasks and fb_task_executions.
//
// A task is a piece of business work a batch project has to do. It is not a
// conversation: every execution of a task is its own conversation with its own
// workspace (<project root>/tasks/<task id>/<seq>), so re-running a task opens
// another execution instead of reusing a conversation, and a stopped execution
// continues in the conversation it stopped in. The primary agent is the tenant
// (agent_id), exactly like projects: every read and write stays inside that
// boundary, and deleting a project or a conversation takes its tasks and
// executions with it. The directories they ran in are never touched — the same
// rule that keeps a deleted project's files on disk.
package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Task is one row of fb_tasks: the business task a batch project runs.
type Task struct {
	ID, AgentID, ProjectID string
	Title, InputText       string
	// InputSet reports whether InputText is the task's fixed input. It is
	// recorded once, by the first turn that starts the task, so a later
	// execution inherits the input verbatim instead of restating it.
	InputSet    bool
	ExternalRef string
	// Origin says which door the task came in by (api | web | cron |
	// channel); OriginRef names the caller's own record of it — a cron job
	// id, a channel message id — for the integrating system to correlate.
	Origin, OriginRef string
	CreatedAt         int64
	UpdatedAt         int64
}

// TaskExecution is one row of fb_task_executions: one run of a task, which is
// exactly one conversation and one task workspace.
type TaskExecution struct {
	SessionID, TaskID string
	Seq               int
	Workspace, Status string
	ErrorCode         string
	ErrorText         string
	// StartedAt and FinishedAt are unix seconds; zero means "not yet".
	StartedAt, FinishedAt int64
	CreatedAt             int64
}

// TaskRow is one task with the execution that speaks for it — its latest —
// and how many executions it has, which is what a task list draws.
type TaskRow struct {
	Task
	Latest     TaskExecution
	Executions int
}

// Execution statuses. pending is an execution whose turn has not started yet
// (the input may still be recorded by that first turn); stopped is one that
// was cancelled and may continue in its own conversation; completed and
// failed are final, and only a new execution runs the task again.
const (
	TaskStatusPending   = "pending"
	TaskStatusRunning   = "running"
	TaskStatusStopped   = "stopped"
	TaskStatusCompleted = "completed"
	TaskStatusFailed    = "failed"
)

// Task origins: which door the task came in by.
const (
	TaskOriginAPI     = "api"
	TaskOriginWeb     = "web"
	TaskOriginCron    = "cron"
	TaskOriginChannel = "channel"
)

var (
	// ErrTaskNotFound reports a missing or cross-tenant task id.
	ErrTaskNotFound = errors.New("task not found")
	// ErrNotBatchProject reports a task asked of a project that is not a
	// batch project, or one this agent does not own.
	ErrNotBatchProject = errors.New("project is not a batch project")
	// ErrTaskExecutionState is the sentinel behind TaskExecutionStateError:
	// an execution is not in the state the caller expected, either because
	// another turn started it first or because it has already ended.
	ErrTaskExecutionState = errors.New("task execution is not in the expected state")
	// ErrTaskInputFixed reports a first turn that wanted to record the task's
	// input while another one already had. The task's input is written once.
	ErrTaskInputFixed = errors.New("task input is already fixed")
)

// TaskExecutionStateError carries the status the execution actually holds, so
// the caller can say which transition lost the race.
type TaskExecutionStateError struct {
	SessionID string
	Status    string
	Want      string
}

func (e *TaskExecutionStateError) Error() string {
	return fmt.Sprintf("task execution %s is %s, want %s", e.SessionID, e.Status, e.Want)
}

func (e *TaskExecutionStateError) Unwrap() error { return ErrTaskExecutionState }

// TaskWorkspaceDir is where one execution runs: <root>/tasks/<task id>/<seq>.
// It is the directory the turn's tools act in, holding inputs/ (what the
// business system attached), outputs/ (deliverables) and .work/ (scratch).
func TaskWorkspaceDir(projectRoot, taskID string, seq int) string {
	return filepath.Join(strings.TrimSpace(projectRoot), "tasks", strings.TrimSpace(taskID), strconv.Itoa(seq))
}

const taskColumns = `id, agent_id, project_id, title, input_text, input_set, external_ref, origin, origin_ref, created_at, updated_at`

const taskColumnCount = 11

func scanTask(scanner interface{ Scan(...any) error }) (Task, error) {
	var t Task
	var inputSet int
	if err := scanner.Scan(&t.ID, &t.AgentID, &t.ProjectID, &t.Title, &t.InputText, &inputSet,
		&t.ExternalRef, &t.Origin, &t.OriginRef, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return Task{}, err
	}
	t.InputSet = inputSet != 0
	return t, nil
}

const executionColumns = `session_id, task_id, seq, workspace, status, error_code, error_text, started_at, finished_at, created_at`

// scanExecution reads one execution row. started_at and finished_at are NULL
// until their moments happen, which reads as zero.
func scanExecution(scanner interface{ Scan(...any) error }) (TaskExecution, error) {
	var e TaskExecution
	var started, finished sql.NullInt64
	if err := scanner.Scan(&e.SessionID, &e.TaskID, &e.Seq, &e.Workspace, &e.Status, &e.ErrorCode,
		&e.ErrorText, &started, &finished, &e.CreatedAt); err != nil {
		return TaskExecution{}, err
	}
	e.StartedAt, e.FinishedAt = started.Int64, finished.Int64
	return e, nil
}

// CreateTask records a business task of a batch project. The project must be
// this agent's and must be a batch project: a task is what a batch project
// exists to run, and an ordinary conversation has no task behind it.
func (s *ProjectStore) CreateTask(ctx context.Context, in Task) (Task, error) {
	if s == nil || s.db == nil {
		return Task{}, fmt.Errorf("project store unavailable")
	}
	if strings.TrimSpace(s.agentID) == "" {
		return Task{}, fmt.Errorf("project store has no agent")
	}
	projectID := strings.TrimSpace(in.ProjectID)
	if projectID == "" {
		return Task{}, fmt.Errorf("task needs a project")
	}
	p, err := s.Get(ctx, projectID)
	if err != nil {
		return Task{}, err
	}
	if p.ProjectType != ProjectTypeBatch {
		return Task{}, ErrNotBatchProject
	}
	origin := strings.TrimSpace(in.Origin)
	if origin == "" {
		origin = TaskOriginAPI
	}
	switch origin {
	case TaskOriginAPI, TaskOriginWeb, TaskOriginCron, TaskOriginChannel:
	default:
		return Task{}, fmt.Errorf("task origin must be api, web, cron or channel")
	}
	now := time.Now().Unix()
	t := Task{
		ID:          NewID("tsk"),
		AgentID:     s.agentID,
		ProjectID:   projectID,
		Title:       strings.TrimSpace(in.Title),
		InputText:   in.InputText,
		InputSet:    in.InputSet,
		ExternalRef: strings.TrimSpace(in.ExternalRef),
		Origin:      origin,
		OriginRef:   strings.TrimSpace(in.OriginRef),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO fb_tasks(`+taskColumns+`)
VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		t.ID, t.AgentID, t.ProjectID, t.Title, t.InputText, boolInt(t.InputSet),
		t.ExternalRef, t.Origin, t.OriginRef, t.CreatedAt, t.UpdatedAt); err != nil {
		return Task{}, err
	}
	return t, nil
}

// GetTask reads one task of this agent.
func (s *ProjectStore) GetTask(ctx context.Context, id string) (Task, error) {
	if s == nil || s.db == nil {
		return Task{}, ErrTaskNotFound
	}
	t, err := scanTask(s.db.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM fb_tasks WHERE id=? AND agent_id=?`,
		strings.TrimSpace(id), s.agentID))
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrTaskNotFound
	}
	if err != nil {
		return Task{}, err
	}
	return t, nil
}

// AddTaskExecution opens the task's next execution onto a conversation. The
// sequence number and the workspace it runs in are decided here, in one
// transaction, so two callers adding at the same moment get 1 and 2 rather
// than both computing the same seq: the write lock the state database takes at
// the start of every transaction is what serializes them.
func (s *ProjectStore) AddTaskExecution(ctx context.Context, taskID, sessionID, projectRoot string) (TaskExecution, error) {
	if s == nil || s.db == nil {
		return TaskExecution{}, fmt.Errorf("project store unavailable")
	}
	taskID = strings.TrimSpace(taskID)
	sessionID = strings.TrimSpace(sessionID)
	if taskID == "" || sessionID == "" {
		return TaskExecution{}, fmt.Errorf("task execution needs a task and a session")
	}
	if err := s.requireBatchTask(ctx, taskID); err != nil {
		return TaskExecution{}, err
	}
	root := strings.TrimSpace(projectRoot)
	if root == "" {
		return TaskExecution{}, fmt.Errorf("task execution needs the project root")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TaskExecution{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var next int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM fb_task_executions WHERE task_id=?`, taskID).Scan(&next); err != nil {
		return TaskExecution{}, err
	}
	now := time.Now().Unix()
	exec := TaskExecution{
		SessionID: sessionID,
		TaskID:    taskID,
		Seq:       next,
		Workspace: TaskWorkspaceDir(root, taskID, next),
		Status:    TaskStatusPending,
		CreatedAt: now,
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO fb_task_executions(session_id, task_id, seq, workspace, status, created_at)
VALUES(?,?,?,?,?,?)`, exec.SessionID, exec.TaskID, exec.Seq, exec.Workspace, exec.Status, exec.CreatedAt); err != nil {
		return TaskExecution{}, err
	}
	// A new execution is activity on the task: the task list orders by it.
	if _, err := tx.ExecContext(ctx, `UPDATE fb_tasks SET updated_at=? WHERE id=?`, now, taskID); err != nil {
		return TaskExecution{}, err
	}
	if err := tx.Commit(); err != nil {
		return TaskExecution{}, err
	}
	return exec, nil
}

// ExecutionForSession returns the execution a conversation is, when it is one,
// together with its task. ok is false for an ordinary conversation.
func (s *ProjectStore) ExecutionForSession(ctx context.Context, sessionID string) (TaskExecution, Task, bool, error) {
	if s == nil || s.db == nil {
		return TaskExecution{}, Task{}, false, nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return TaskExecution{}, Task{}, false, nil
	}
	var e TaskExecution
	var started, finished sql.NullInt64
	var t Task
	var inputSet int
	err := s.db.QueryRowContext(ctx, `
SELECT e.session_id, e.task_id, e.seq, e.workspace, e.status, e.error_code, e.error_text, e.started_at, e.finished_at, e.created_at,
       t.id, t.agent_id, t.project_id, t.title, t.input_text, t.input_set, t.external_ref, t.origin, t.origin_ref, t.created_at, t.updated_at
  FROM fb_task_executions e
  JOIN fb_tasks t ON t.id = e.task_id
 WHERE e.session_id=? AND t.agent_id=?`, sessionID, s.agentID).Scan(
		&e.SessionID, &e.TaskID, &e.Seq, &e.Workspace, &e.Status, &e.ErrorCode, &e.ErrorText, &started, &finished, &e.CreatedAt,
		&t.ID, &t.AgentID, &t.ProjectID, &t.Title, &t.InputText, &inputSet, &t.ExternalRef, &t.Origin, &t.OriginRef, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return TaskExecution{}, Task{}, false, nil
	}
	if err != nil {
		return TaskExecution{}, Task{}, false, err
	}
	e.StartedAt, e.FinishedAt = started.Int64, finished.Int64
	t.InputSet = inputSet != 0
	return e, t, true, nil
}

// StartTaskExecution moves an execution from expectedStatus (pending for a
// first turn, stopped for a continuation) to running, recording the task's
// input on the way when this is the turn that fixes it.
//
// All of it is one transaction: the status it read is the status it changes,
// the input is recorded only when nothing recorded it before, and a rollback
// leaves neither the input nor the status behind. Two turns racing to start
// the same execution therefore end with exactly one running.
func (s *ProjectStore) StartTaskExecution(ctx context.Context, sessionID, expectedStatus, inputText string, recordInput bool) (TaskExecution, error) {
	if s == nil || s.db == nil {
		return TaskExecution{}, fmt.Errorf("project store unavailable")
	}
	sessionID = strings.TrimSpace(sessionID)
	expectedStatus = strings.TrimSpace(expectedStatus)
	switch expectedStatus {
	case TaskStatusPending, TaskStatusStopped:
	default:
		return TaskExecution{}, fmt.Errorf("task execution can only start from pending or stopped, not %q", expectedStatus)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TaskExecution{}, err
	}
	defer func() { _ = tx.Rollback() }()
	exec, task, err := executionForSessionTx(ctx, tx, sessionID, s.agentID)
	if err != nil {
		return TaskExecution{}, err
	}
	if exec.Status != expectedStatus {
		return TaskExecution{}, &TaskExecutionStateError{SessionID: sessionID, Status: exec.Status, Want: expectedStatus}
	}
	now := time.Now().Unix()
	// recordInput is the caller saying "this turn is the one that fixes the
	// task's input". It is not a hint: a turn that reached here believing the
	// input was still open and finds it taken must not run on an input it did
	// not record — it is refused, and its own status change rolls back with
	// the rest of the transaction.
	if recordInput {
		res, err := tx.ExecContext(ctx, `UPDATE fb_tasks SET input_text=?, input_set=1, updated_at=? WHERE id=? AND input_set=0`,
			inputText, now, task.ID)
		if err != nil {
			return TaskExecution{}, err
		}
		if n, err := res.RowsAffected(); err == nil && n == 0 {
			return TaskExecution{}, ErrTaskInputFixed
		}
	}
	res, err := tx.ExecContext(ctx, `UPDATE fb_task_executions
   SET status=?, started_at=?, finished_at=NULL, error_code='', error_text=''
 WHERE session_id=? AND status=?`, TaskStatusRunning, now, sessionID, expectedStatus)
	if err != nil {
		return TaskExecution{}, err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return TaskExecution{}, &TaskExecutionStateError{SessionID: sessionID, Status: exec.Status, Want: expectedStatus}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE fb_tasks SET updated_at=? WHERE id=?`, now, task.ID); err != nil {
		return TaskExecution{}, err
	}
	if err := tx.Commit(); err != nil {
		return TaskExecution{}, err
	}
	exec.Status = TaskStatusRunning
	exec.StartedAt, exec.FinishedAt = now, 0
	exec.ErrorCode, exec.ErrorText = "", ""
	task.UpdatedAt = now
	return exec, nil
}

// MarkExecutionEnded records how an execution ended: completed, stopped (it
// was cancelled and may continue) or failed. finished_at is when it happened,
// and the task is touched so the task list shows the activity.
func (s *ProjectStore) MarkExecutionEnded(ctx context.Context, sessionID, status, code, text string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("project store unavailable")
	}
	switch status {
	case TaskStatusStopped, TaskStatusCompleted, TaskStatusFailed:
	default:
		return fmt.Errorf("task execution cannot end as %q", status)
	}
	sessionID = strings.TrimSpace(sessionID)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	exec, task, err := executionForSessionTx(ctx, tx, sessionID, s.agentID)
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	if _, err := tx.ExecContext(ctx, `UPDATE fb_task_executions
   SET status=?, error_code=?, error_text=?, finished_at=?
 WHERE session_id=?`, status, code, text, now, exec.SessionID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE fb_tasks SET updated_at=? WHERE id=?`, now, task.ID); err != nil {
		return err
	}
	return tx.Commit()
}

// SetExecutionNotifyError records why the notification due at the end of an
// execution never arrived. It never touches the execution's status: the task
// finished, the telling failed, and the two are different facts (plan 151).
// A session that is not this agent's task execution is refused with
// ErrTaskNotFound.
func (s *ProjectStore) SetExecutionNotifyError(ctx context.Context, sessionID, text string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("project store unavailable")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ErrTaskNotFound
	}
	res, err := s.db.ExecContext(ctx, `
UPDATE fb_task_executions SET notify_error=?
 WHERE session_id=?
   AND task_id IN (SELECT id FROM fb_tasks WHERE agent_id=?)`,
		strings.TrimSpace(text), sessionID, s.agentID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrTaskNotFound
	}
	return nil
}

// ExecutionNotifyError returns why the notification due at the end of an
// execution never arrived. It is empty when the notification was delivered or
// when none was due.
func (s *ProjectStore) ExecutionNotifyError(ctx context.Context, sessionID string) (string, error) {
	if s == nil || s.db == nil {
		return "", fmt.Errorf("project store unavailable")
	}
	var text string
	err := s.db.QueryRowContext(ctx, `
SELECT COALESCE(e.notify_error, '')
  FROM fb_task_executions e
  JOIN fb_tasks t ON t.id = e.task_id
 WHERE e.session_id=? AND t.agent_id=?`, strings.TrimSpace(sessionID), s.agentID).Scan(&text)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrTaskNotFound
	}
	if err != nil {
		return "", err
	}
	return text, nil
}

// ExecutionStatuses reads the status of every execution among sessionIDs in
// one query, keyed by session id. A conversation that is not an execution of
// this agent's is absent from the map rather than reported with an empty
// status, so a caller listing a page of conversations asks once what the batch
// ones among them are doing.
func (s *ProjectStore) ExecutionStatuses(ctx context.Context, sessionIDs []string) (map[string]string, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	ids := make([]string, 0, len(sessionIDs))
	seen := make(map[string]bool, len(sessionIDs))
	for _, id := range sessionIDs {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids)+1)
	args = append(args, s.agentID)
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT e.session_id, e.status
  FROM fb_task_executions e
  JOIN fb_tasks t ON t.id = e.task_id
 WHERE t.agent_id=? AND e.session_id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]string, len(ids))
	for rows.Next() {
		var sessionID, status string
		if err := rows.Scan(&sessionID, &status); err != nil {
			return nil, err
		}
		out[sessionID] = status
	}
	return out, rows.Err()
}

// executionForSessionTx reads one execution and its task inside a
// transaction, refusing a conversation that is not a task of this agent.
func executionForSessionTx(ctx context.Context, tx *sql.Tx, sessionID, agentID string) (TaskExecution, Task, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return TaskExecution{}, Task{}, ErrTaskNotFound
	}
	var e TaskExecution
	var started, finished sql.NullInt64
	var t Task
	var inputSet int
	err := tx.QueryRowContext(ctx, `
SELECT e.session_id, e.task_id, e.seq, e.workspace, e.status, e.error_code, e.error_text, e.started_at, e.finished_at, e.created_at,
       t.id, t.agent_id, t.project_id, t.title, t.input_text, t.input_set, t.external_ref, t.origin, t.origin_ref, t.created_at, t.updated_at
  FROM fb_task_executions e
  JOIN fb_tasks t ON t.id = e.task_id
 WHERE e.session_id=? AND t.agent_id=?`, sessionID, agentID).Scan(
		&e.SessionID, &e.TaskID, &e.Seq, &e.Workspace, &e.Status, &e.ErrorCode, &e.ErrorText, &started, &finished, &e.CreatedAt,
		&t.ID, &t.AgentID, &t.ProjectID, &t.Title, &t.InputText, &inputSet, &t.ExternalRef, &t.Origin, &t.OriginRef, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return TaskExecution{}, Task{}, ErrTaskNotFound
	}
	if err != nil {
		return TaskExecution{}, Task{}, err
	}
	e.StartedAt, e.FinishedAt = started.Int64, finished.Int64
	t.InputSet = inputSet != 0
	return e, t, nil
}

// requireBatchTask reads one task of this agent and reports it missing when it
// belongs to another agent.
func (s *ProjectStore) requireBatchTask(ctx context.Context, taskID string) error {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM fb_tasks WHERE id=? AND agent_id=?`, taskID, s.agentID).Scan(&count)
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrTaskNotFound
	}
	return nil
}

// ListTasksOptions filters a task list.
type ListTasksOptions struct {
	// Search matches the task's title, external reference and id.
	Search string
	// Status keeps only tasks whose latest execution holds this status.
	Status string
	// Origin keeps only tasks that came in by this door (api | web | cron |
	// channel).
	Origin string
}

// taskListWhere is the filter both task-list reads apply, so the page a table
// draws and the total it pages through always count the same rows.
func (s *ProjectStore) taskListWhere(projectID string, opts ListTasksOptions) (string, []any) {
	where := "t.agent_id=? AND t.project_id=?"
	args := []any{s.agentID, strings.TrimSpace(projectID)}
	if q := strings.TrimSpace(opts.Search); q != "" {
		where += " AND (t.title LIKE ? OR t.external_ref LIKE ? OR t.id LIKE ?)"
		like := "%" + q + "%"
		args = append(args, like, like, like)
	}
	if status := strings.TrimSpace(opts.Status); status != "" {
		where += " AND (SELECT st.status FROM fb_task_executions st WHERE st.task_id=t.id ORDER BY st.seq DESC LIMIT 1)=?"
		args = append(args, status)
	}
	if origin := strings.TrimSpace(opts.Origin); origin != "" {
		where += " AND t.origin=?"
		args = append(args, origin)
	}
	return where, args
}

// ListTasksPage returns one page of a project's tasks with the execution that
// speaks for each — its latest — and how many it has, plus how many rows the
// same filter matches. An offset past the end reads the last page rather than
// an empty one, the paging contract every list endpoint follows.
func (s *ProjectStore) ListTasksPage(ctx context.Context, projectID string, opts ListTasksOptions, limit, offset int) ([]TaskRow, int, error) {
	if s == nil || s.db == nil {
		return nil, 0, nil
	}
	limit, offset = projectPageWindow(limit, offset)
	where, args := s.taskListWhere(projectID, opts)
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM fb_tasks t WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if offset >= total {
		offset = max(total-1, 0) / limit * limit
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT t.id, t.agent_id, t.project_id, t.title, t.input_text, t.input_set, t.external_ref, t.origin, t.origin_ref, t.created_at, t.updated_at,
       e.session_id, e.task_id, e.seq, e.workspace, e.status, e.error_code, e.error_text, e.started_at, e.finished_at, e.created_at,
       (SELECT COUNT(*) FROM fb_task_executions c WHERE c.task_id=t.id)
  FROM fb_tasks t
  LEFT JOIN fb_task_executions e
    ON e.task_id=t.id
   AND e.seq=(SELECT MAX(m.seq) FROM fb_task_executions m WHERE m.task_id=t.id)
 WHERE `+where+`
 ORDER BY t.updated_at DESC, t.id
 LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []TaskRow
	for rows.Next() {
		row, err := scanTaskRow(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, row)
	}
	return out, total, rows.Err()
}

// scanTaskRow reads one task with its latest execution. A task always has at
// least the execution it was created for, but the join is a left one so a task
// whose execution row somehow went missing still lists.
func scanTaskRow(scanner interface{ Scan(...any) error }) (TaskRow, error) {
	var row TaskRow
	var inputSet int
	var sessionID, taskID, workspace, status, errCode, errText sql.NullString
	var seq, createdAt sql.NullInt64
	var started, finished sql.NullInt64
	if err := scanner.Scan(&row.ID, &row.AgentID, &row.ProjectID, &row.Title, &row.InputText, &inputSet,
		&row.ExternalRef, &row.Origin, &row.OriginRef, &row.CreatedAt, &row.UpdatedAt,
		&sessionID, &taskID, &seq, &workspace, &status, &errCode, &errText, &started, &finished, &createdAt,
		&row.Executions); err != nil {
		return TaskRow{}, err
	}
	row.InputSet = inputSet != 0
	row.Latest = TaskExecution{
		SessionID:  sessionID.String,
		TaskID:     taskID.String,
		Seq:        int(seq.Int64),
		Workspace:  workspace.String,
		Status:     status.String,
		ErrorCode:  errCode.String,
		ErrorText:  errText.String,
		StartedAt:  started.Int64,
		FinishedAt: finished.Int64,
		CreatedAt:  createdAt.Int64,
	}
	return row, nil
}

// ListTaskExecutionsPage returns one page of a task's executions, newest
// first, and how many it has.
func (s *ProjectStore) ListTaskExecutionsPage(ctx context.Context, taskID string, limit, offset int) ([]TaskExecution, int, error) {
	if s == nil || s.db == nil {
		return nil, 0, nil
	}
	taskID = strings.TrimSpace(taskID)
	if err := s.requireBatchTask(ctx, taskID); err != nil {
		return nil, 0, err
	}
	limit, offset = projectPageWindow(limit, offset)
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM fb_task_executions WHERE task_id=?`, taskID).Scan(&total); err != nil {
		return nil, 0, err
	}
	if offset >= total {
		offset = max(total-1, 0) / limit * limit
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+executionColumns+` FROM fb_task_executions
 WHERE task_id=? ORDER BY seq DESC LIMIT ? OFFSET ?`, taskID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []TaskExecution
	for rows.Next() {
		e, err := scanExecution(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, e)
	}
	return out, total, rows.Err()
}
