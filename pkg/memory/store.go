package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	ThreadMemoryEnabled  = "enabled"
	ThreadMemoryDisabled = "disabled"
	threadMemoryPolluted = "polluted"
)

// Store is the memory pipeline's view of the state database, bound to exactly
// one primary agent.
//
// A primary agent is a tenant, so every row the pipeline reads or writes is
// tenant data: the sessions it may consolidate, the extracted stage-one
// outputs, and the jobs that produce them. The tenant lives on the store rather
// than in each method's arguments so a query cannot be written that forgets it,
// and an unbound store refuses to touch the database at all rather than fall
// back to seeing everyone's rows.
type Store struct {
	DB *sql.DB

	mu      sync.RWMutex
	agentID string
}

// ErrUnboundStore reports a memory operation attempted without an owning
// primary agent. It is a programming error, not a runtime condition: it means
// a seam built a store without resolving the active agent first.
var ErrUnboundStore = errors.New("memories: store is not bound to a primary agent")

type Stage1Output struct {
	ThreadID                         string
	ProjectKey                       string
	SourceUpdatedAt                  int64
	RawMemory                        string
	RolloutSummary                   string
	RolloutSlug                      sql.NullString
	GeneratedAt                      int64
	UsageCount                       sql.NullInt64
	LastUsage                        sql.NullInt64
	SelectedForPhase2                bool
	SelectedForPhase2SourceUpdatedAt sql.NullInt64
	Cwd                              string
	RolloutPath                      string
	GitBranch                        string
}

type Status struct {
	Stage1Count   int `json:"stage1_count"`
	JobCount      int `json:"job_count"`
	SelectedCount int `json:"selected_count"`
}

type Stage1Version struct {
	ThreadID        string
	ProjectKey      string
	SourceUpdatedAt int64
}

// NewStore binds the memory tables to the primary agent that owns them.
// agentID is the active primary agent's id (primaryagent.Summary.ID).
func NewStore(db *sql.DB, agentID string) *Store {
	return &Store{DB: db, agentID: strings.TrimSpace(agentID)}
}

// BindPrimaryAgent re-points the store at another tenant. Switching primary
// agents rebinds the whole process, so the surfaces that share this store all
// follow the new tenant at once instead of each holding a stale one.
func (s *Store) BindPrimaryAgent(agentID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.agentID = strings.TrimSpace(agentID)
}

// AgentID returns the primary agent whose memories this store reads and writes.
func (s *Store) AgentID() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.agentID
}

// agent returns the owning tenant, or ErrUnboundStore when there is none.
func (s *Store) agent() (string, error) {
	id := s.AgentID()
	if id == "" {
		return "", ErrUnboundStore
	}
	return id, nil
}

func ValidThreadMemoryMode(mode string) bool {
	mode = strings.TrimSpace(mode)
	return mode == ThreadMemoryEnabled || mode == ThreadMemoryDisabled
}

func (s *Store) SetThreadMemoryMode(ctx context.Context, threadID, mode string) error {
	if s == nil || s.DB == nil {
		return nil
	}
	threadID = strings.TrimSpace(threadID)
	mode = strings.TrimSpace(mode)
	if threadID == "" {
		return fmt.Errorf("thread_id is required")
	}
	if !ValidThreadMemoryMode(mode) {
		return fmt.Errorf("invalid memory mode %q", mode)
	}
	agentID, err := s.agent()
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, `UPDATE fb_sessions SET memory_mode=? WHERE id=? AND agent_id=?`, mode, threadID, agentID)
	return err
}

func (s *Store) markThreadPolluted(ctx context.Context, threadID string) error {
	if s == nil || s.DB == nil {
		return nil
	}
	agentID, err := s.agent()
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, `UPDATE fb_sessions SET memory_mode=? WHERE id=? AND agent_id=? AND memory_mode<>?`, threadMemoryPolluted, strings.TrimSpace(threadID), agentID, threadMemoryPolluted)
	return err
}

func (s *Store) UpsertStage1Output(ctx context.Context, out Stage1Output) error {
	if s == nil || s.DB == nil {
		return nil
	}
	threadID := strings.TrimSpace(out.ThreadID)
	if threadID == "" {
		return fmt.Errorf("thread_id is required")
	}
	projectKey := strings.TrimSpace(out.ProjectKey)
	if projectKey == "" {
		return fmt.Errorf("project_key is required")
	}
	agentID, err := s.agent()
	if err != nil {
		return err
	}
	generatedAt := out.GeneratedAt
	if generatedAt <= 0 {
		generatedAt = time.Now().Unix()
	}
	_, err = s.DB.ExecContext(ctx, `INSERT INTO fb_memory_stage1_outputs(
		thread_id, agent_id, project_key, source_updated_at, raw_memory, rollout_summary, rollout_slug, generated_at,
		usage_count, last_usage, selected_for_phase2, selected_for_phase2_source_updated_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
	ON CONFLICT(thread_id) DO UPDATE SET
		agent_id=excluded.agent_id,
		project_key=excluded.project_key,
		source_updated_at=excluded.source_updated_at,
		raw_memory=excluded.raw_memory,
		rollout_summary=excluded.rollout_summary,
		rollout_slug=excluded.rollout_slug,
		generated_at=excluded.generated_at
	WHERE excluded.source_updated_at>=fb_memory_stage1_outputs.source_updated_at
	  AND fb_memory_stage1_outputs.agent_id=excluded.agent_id`,
		threadID, agentID, projectKey, out.SourceUpdatedAt, out.RawMemory, out.RolloutSummary, nullStringValue(out.RolloutSlug), generatedAt,
		out.UsageCount.Int64, nullIntValue(out.LastUsage), boolInt(out.SelectedForPhase2), nullIntValue(out.SelectedForPhase2SourceUpdatedAt))
	return err
}

func (s *Store) DeleteThreadMemory(ctx context.Context, threadID string) error {
	if s == nil || s.DB == nil {
		return nil
	}
	threadID = strings.TrimSpace(threadID)
	agentID, err := s.agent()
	if err != nil {
		return err
	}
	return s.withImmediate(ctx, func(tx *sql.Tx) error {
		var selected int
		var projectKey string
		err := tx.QueryRowContext(ctx, `SELECT selected_for_phase2, project_key FROM fb_memory_stage1_outputs WHERE thread_id=? AND agent_id=?`, threadID, agentID).Scan(&selected, &projectKey)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM fb_memory_stage1_outputs WHERE thread_id=? AND agent_id=?`, threadID, agentID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM fb_memory_jobs WHERE kind=? AND job_key=? AND agent_id=?`, JobKindStage1, threadID, agentID); err != nil {
			return err
		}
		if selected != 0 {
			scope, ok := ProjectScope(projectKey)
			if !ok {
				return fmt.Errorf("stage-one row for thread %s carries no project scope", threadID)
			}
			return enqueueGlobalPhase2On(ctx, tx, agentID, scope, time.Now().Unix())
		}
		return nil
	})
}

// HasSelectedStage1Output reports whether threadID's stage-1 output is
// currently selected as phase-2 input for its project scope, returning that
// scope alongside the flag. The scope comes from the row itself rather than
// the caller's own cwd, so a caller who only knows the thread ID (not which
// project it belonged to) can still learn which scope's consolidation to
// re-trigger.
func (s *Store) HasSelectedStage1Output(ctx context.Context, threadID string) (Scope, bool, error) {
	if s == nil || s.DB == nil {
		return Scope{}, false, nil
	}
	agentID, err := s.agent()
	if err != nil {
		return Scope{}, false, err
	}
	var selected int
	var projectKey string
	err = s.DB.QueryRowContext(ctx, `SELECT selected_for_phase2, project_key FROM fb_memory_stage1_outputs WHERE thread_id=? AND agent_id=?`, strings.TrimSpace(threadID), agentID).Scan(&selected, &projectKey)
	if errors.Is(err, sql.ErrNoRows) {
		return Scope{}, false, nil
	}
	if err != nil {
		return Scope{}, false, err
	}
	return Scope{Kind: ScopeProject, Key: projectKey}, selected != 0, nil
}

// SelectStage1ForPhase2 selects the raw memories that make up one project
// scope's next consolidation input. It never crosses scopes: both the output
// row and the session it was extracted from must carry this tenant's agent_id
// (so a mislabelled row or a session that changed hands cannot feed another
// tenant's consolidation) and this project's key (so one project's
// consolidation pass never reads another project's raw memory).
func (s *Store) SelectStage1ForPhase2(ctx context.Context, projectKey string, limit, maxUnusedDays int) ([]Stage1Output, error) {
	if s == nil || s.DB == nil {
		return nil, nil
	}
	projectKey = strings.TrimSpace(projectKey)
	if projectKey == "" {
		return nil, fmt.Errorf("project_key is required")
	}
	if limit < 1 {
		limit = 1
	}
	agentID, err := s.agent()
	if err != nil {
		return nil, err
	}
	cutoff := time.Now().Add(-time.Duration(maxUnusedDays) * 24 * time.Hour).Unix()
	rows, err := s.DB.QueryContext(ctx, `SELECT o.thread_id, o.project_key, o.source_updated_at, o.raw_memory, o.rollout_summary,
		o.rollout_slug, o.generated_at, o.usage_count, o.last_usage, o.selected_for_phase2,
		o.selected_for_phase2_source_updated_at, s.cwd, s.git_branch
	FROM fb_memory_stage1_outputs AS o
	JOIN fb_sessions AS s ON s.id=o.thread_id
	WHERE o.agent_id=? AND s.agent_id=? AND o.project_key=?
	  AND s.memory_mode=? AND (TRIM(o.raw_memory)<>'' OR TRIM(o.rollout_summary)<>'')
	  AND COALESCE(o.last_usage, o.source_updated_at)>=?
	ORDER BY o.usage_count DESC,
		COALESCE(o.last_usage,o.source_updated_at) DESC,
		o.source_updated_at DESC, o.thread_id DESC
	LIMIT ?`, agentID, agentID, projectKey, ThreadMemoryEnabled, cutoff, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Stage1Output, 0, limit)
	for rows.Next() {
		var item Stage1Output
		var selected int
		if err := rows.Scan(&item.ThreadID, &item.ProjectKey, &item.SourceUpdatedAt, &item.RawMemory, &item.RolloutSummary,
			&item.RolloutSlug, &item.GeneratedAt, &item.UsageCount, &item.LastUsage, &selected,
			&item.SelectedForPhase2SourceUpdatedAt, &item.Cwd, &item.GitBranch); err != nil {
			return nil, err
		}
		item.SelectedForPhase2 = selected != 0
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ThreadID < out[j].ThreadID })
	return out, nil
}

func (s *Store) UpdateUsage(ctx context.Context, threadIDs []string, at int64) error {
	if s == nil || s.DB == nil || len(threadIDs) == 0 {
		return nil
	}
	if at <= 0 {
		at = time.Now().Unix()
	}
	agentID, err := s.agent()
	if err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, threadID := range threadIDs {
		threadID = strings.TrimSpace(threadID)
		if threadID == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE fb_memory_stage1_outputs SET usage_count=usage_count+1, last_usage=? WHERE thread_id=? AND agent_id=?`, at, threadID, agentID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) PruneStage1Outputs(ctx context.Context, maxUnusedDays, batchSize int) (int64, error) {
	if s == nil || s.DB == nil {
		return 0, nil
	}
	agentID, err := s.agent()
	if err != nil {
		return 0, err
	}
	cutoff := time.Now().Add(-time.Duration(maxUnusedDays) * 24 * time.Hour).Unix()
	result, err := s.DB.ExecContext(ctx, `DELETE FROM fb_memory_stage1_outputs WHERE agent_id=? AND thread_id IN (
		SELECT thread_id FROM fb_memory_stage1_outputs
		WHERE agent_id=? AND selected_for_phase2=0 AND COALESCE(last_usage,source_updated_at)<?
		ORDER BY COALESCE(last_usage,source_updated_at) ASC, thread_id ASC LIMIT ?
	)`, agentID, agentID, cutoff, batchSize)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *Store) ListStage1Versions(ctx context.Context) ([]Stage1Version, error) {
	if s == nil || s.DB == nil {
		return nil, nil
	}
	agentID, err := s.agent()
	if err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT thread_id, project_key, source_updated_at FROM fb_memory_stage1_outputs WHERE agent_id=?`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Stage1Version
	for rows.Next() {
		var version Stage1Version
		if err := rows.Scan(&version.ThreadID, &version.ProjectKey, &version.SourceUpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, version)
	}
	return out, rows.Err()
}

// Reset clears the memories of the owning primary agent only. Resetting one
// tenant must not disturb another's pipeline, so the deletes are scoped the
// same way every read is.
func (s *Store) Reset(ctx context.Context) error {
	if s == nil || s.DB == nil {
		return nil
	}
	agentID, err := s.agent()
	if err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM fb_memory_stage1_outputs WHERE agent_id=?`, agentID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM fb_memory_jobs WHERE agent_id=? AND kind IN (?,?)`, agentID, JobKindStage1, JobKindConsolidate); err != nil {
		return err
	}
	return tx.Commit()
}

// ResetScope clears one scope's memory rows only: for a project scope, its
// stage-1 outputs (and the stage-1 jobs that produced them) plus its
// consolidation job; for the global scope, just its consolidation job, since
// the global scope has no stage-1 outputs of its own — its input is the
// project scopes' promotion candidates, not raw session transcripts.
func (s *Store) ResetScope(ctx context.Context, scope Scope) error {
	if s == nil || s.DB == nil {
		return nil
	}
	agentID, err := s.agent()
	if err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if scope.Kind == ScopeProject {
		threadIDs, err := tx.QueryContext(ctx, `SELECT thread_id FROM fb_memory_stage1_outputs WHERE agent_id=? AND project_key=?`, agentID, scope.Key)
		if err != nil {
			return err
		}
		var ids []string
		for threadIDs.Next() {
			var id string
			if err := threadIDs.Scan(&id); err != nil {
				threadIDs.Close()
				return err
			}
			ids = append(ids, id)
		}
		if err := threadIDs.Err(); err != nil {
			threadIDs.Close()
			return err
		}
		threadIDs.Close()
		if _, err := tx.ExecContext(ctx, `DELETE FROM fb_memory_stage1_outputs WHERE agent_id=? AND project_key=?`, agentID, scope.Key); err != nil {
			return err
		}
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx, `DELETE FROM fb_memory_jobs WHERE kind=? AND job_key=? AND agent_id=?`, JobKindStage1, id, agentID); err != nil {
				return err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM fb_memory_jobs WHERE kind=? AND job_key=? AND agent_id=?`, JobKindConsolidate, consolidateJobKey(agentID, scope), agentID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Status(ctx context.Context) (Status, error) {
	var status Status
	if s == nil || s.DB == nil {
		return status, nil
	}
	agentID, err := s.agent()
	if err != nil {
		return status, err
	}
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM fb_memory_stage1_outputs WHERE agent_id=?`, agentID).Scan(&status.Stage1Count); err != nil {
		return status, err
	}
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM fb_memory_jobs WHERE agent_id=? AND kind IN (?,?)`, agentID, JobKindStage1, JobKindConsolidate).Scan(&status.JobCount); err != nil {
		return status, err
	}
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM fb_memory_stage1_outputs WHERE agent_id=? AND selected_for_phase2<>0`, agentID).Scan(&status.SelectedCount); err != nil {
		return status, err
	}
	return status, nil
}

// withImmediate runs fn in one write transaction. The state database's DSN
// opens every transaction IMMEDIATE, so the write lock is taken before the
// first read and a reader-turned-writer queues behind busy_timeout instead of
// failing mid-transaction with SQLITE_BUSY_SNAPSHOT.
func (s *Store) withImmediate(ctx context.Context, fn func(*sql.Tx) error) (err error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func nullStringValue(value sql.NullString) any {
	if value.Valid {
		return value.String
	}
	return nil
}

func nullIntValue(value sql.NullInt64) any {
	if value.Valid {
		return value.Int64
	}
	return nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
