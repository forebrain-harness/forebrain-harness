package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	JobKindStage1      = "memory_stage1"
	JobKindConsolidate = "memory_consolidate_global"

	jobStatusPending = "pending"
	jobStatusRunning = "running"
	jobStatusDone    = "done"
	jobStatusError   = "error"

	defaultRetryRemaining  = 3
	stage1LeaseSeconds     = 3600
	stage1RetryDelay       = 3600
	phase2LeaseSeconds     = 3600
	phase2RetryDelay       = 3600
	phase2CooldownSeconds  = 6 * 60 * 60
	phase2HeartbeatSeconds = 90
	stage1ConcurrencyLimit = 8
	stage1ThreadScanLimit  = 5000
	stage1PruneBatchSize   = 200

	SessionSourceTUI     = "tui"
	SessionSourceWebchat = "webchat"
)

// Stage1RolloutAbsoluteCap bounds a single stage-1 extraction input regardless
// of the extraction model's context window.
//
// Stage-1 extraction is a one-shot call: nothing reuses its prompt prefix, so
// every input token is structurally a cache miss. Sizing the rollout budget by
// window fraction made that miss grow with the model — a 1,000,000-token
// window like GLM-5.3's allowed a single 665,000-token, 0%-cacheable call.
// The extraction task does not need that much transcript; 32k is the ceiling.
const Stage1RolloutAbsoluteCap = 32_000

// consolidateJobKey names the consolidation job of one scope within one
// primary agent. The job table is keyed by (kind, job_key), so folding both
// the tenant and the scope into the key is what stops two agents — or two
// scopes of the same agent — from sharing, and fighting over, a single
// consolidation row: a project's phase-2 pass has its own lease, cooldown, and
// retry budget independent of every other project and of the global scope.
func consolidateJobKey(agentID string, scope Scope) string {
	return strings.TrimSpace(agentID) + "|" + scope.ID()
}

type SessionCandidate struct {
	ThreadID string
	// ProjectKey is the project scope this thread's memory belongs to,
	// resolved from Cwd once when the thread is claimed. Everything
	// downstream — the scope its rollout evidence is written under, the
	// project_key its stage-1 row carries, the phase-2 job it enqueues — uses
	// this one value rather than re-deriving it from Cwd, so no two layers can
	// disagree about which project a thread belongs to.
	ProjectKey string
	UpdatedAt  int64
	CreatedAt  int64
	Cwd        string
	GitBranch  string
	Source     string
}

type Stage1JobClaim struct {
	Thread         SessionCandidate
	OwnershipToken string
}

type Stage1ClaimOutcome string

const (
	Stage1Claimed        Stage1ClaimOutcome = "claimed"
	Stage1UpToDate       Stage1ClaimOutcome = "up_to_date"
	Stage1Running        Stage1ClaimOutcome = "running"
	Stage1RetryBackoff   Stage1ClaimOutcome = "retry_backoff"
	Stage1RetryExhausted Stage1ClaimOutcome = "retry_exhausted"
)

type Phase2ClaimOutcome string

const (
	Phase2Claimed          Phase2ClaimOutcome = "claimed"
	Phase2RetryUnavailable Phase2ClaimOutcome = "retry_unavailable"
	Phase2Cooldown         Phase2ClaimOutcome = "cooldown"
	Phase2Running          Phase2ClaimOutcome = "running"
)

type Phase2Claim struct {
	Outcome        Phase2ClaimOutcome
	OwnershipToken string
	InputWatermark int64
}

// ClaimStage1JobsForStartup picks the owning agent's idle conversations to
// extract memories from.
//
// The agent_id filter is the isolation boundary of the whole pipeline: this is
// where another tenant's conversations would otherwise enter, and no per-agent
// memory directory downstream can undo that — the extracted text would already
// be in this agent's own files.
//
// A session that resolves to no project scope (the gateway/channel case with
// no launch directory) is skipped rather than claimed: there is nowhere for
// its extracted memory to belong. Extracting it anyway would force a choice
// between inventing a shared bucket — the cross-project mixing this design
// removes — or throwing the extraction away after paying for it; skipping it
// before the claim does neither, and leaves no claimed job that can never
// complete.
//
// This is also the single place a thread's project scope is decided. The SQL
// below can only approximate it (a non-blank cwd), so the real resolution
// happens here in Go and travels with the claim.
func (s *Store) ClaimStage1JobsForStartup(ctx context.Context, currentThreadID string, maxAgeDays, minIdleHours, maxClaimed int) ([]Stage1JobClaim, error) {
	if s == nil || s.DB == nil || maxClaimed < 1 {
		return nil, nil
	}
	agentID, err := s.agent()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	rows, err := s.DB.QueryContext(ctx, `SELECT id, updated_at, created_at, cwd, git_branch, memory_source
		FROM fb_sessions
		WHERE agent_id=? AND memory_mode=? AND id<>? AND memory_source IN (?,?)
		  AND TRIM(cwd)<>'' AND updated_at>=? AND updated_at<=?
		ORDER BY updated_at DESC, id DESC LIMIT ?`, agentID, ThreadMemoryEnabled, strings.TrimSpace(currentThreadID),
		SessionSourceTUI, SessionSourceWebchat,
		now.Add(-time.Duration(maxAgeDays)*24*time.Hour).Unix(),
		now.Add(-time.Duration(minIdleHours)*time.Hour).Unix(), stage1ThreadScanLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	candidates := make([]SessionCandidate, 0, maxClaimed)
	for rows.Next() {
		var candidate SessionCandidate
		if err := rows.Scan(&candidate.ThreadID, &candidate.UpdatedAt, &candidate.CreatedAt, &candidate.Cwd, &candidate.GitBranch, &candidate.Source); err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	claims := make([]Stage1JobClaim, 0, maxClaimed)
	for _, candidate := range candidates {
		if len(claims) >= maxClaimed {
			break
		}
		scope, ok := ProjectScopeForCwd(candidate.Cwd)
		if !ok {
			continue
		}
		candidate.ProjectKey = scope.Key
		outcome, token, err := s.TryClaimStage1Job(ctx, candidate.ThreadID, candidate.UpdatedAt, stage1LeaseSeconds, maxClaimed)
		if err != nil {
			return nil, err
		}
		if outcome == Stage1Claimed {
			claims = append(claims, Stage1JobClaim{Thread: candidate, OwnershipToken: token})
		}
	}
	return claims, nil
}

func (s *Store) TryClaimStage1Job(ctx context.Context, threadID string, sourceUpdatedAt int64, leaseSeconds, maxRunning int) (outcome Stage1ClaimOutcome, token string, err error) {
	if s == nil || s.DB == nil {
		return Stage1Running, "", nil
	}
	agentID, err := s.agent()
	if err != nil {
		return Stage1Running, "", err
	}
	threadID = strings.TrimSpace(threadID)
	now := time.Now().Unix()
	token = uuid.NewString()
	err = s.withImmediate(ctx, func(tx *sql.Tx) error {
		var source int64
		queryErr := tx.QueryRowContext(ctx, `SELECT source_updated_at FROM fb_memory_stage1_outputs WHERE thread_id=? AND agent_id=?`, threadID, agentID).Scan(&source)
		if queryErr == nil && source >= sourceUpdatedAt {
			outcome = Stage1UpToDate
			return nil
		}
		if queryErr != nil && !errors.Is(queryErr, sql.ErrNoRows) {
			return queryErr
		}
		var success sql.NullInt64
		queryErr = tx.QueryRowContext(ctx, `SELECT last_success_watermark FROM fb_memory_jobs WHERE kind=? AND job_key=? AND agent_id=?`, JobKindStage1, threadID, agentID).Scan(&success)
		if queryErr == nil && success.Valid && success.Int64 >= sourceUpdatedAt {
			outcome = Stage1UpToDate
			return nil
		}
		if queryErr != nil && !errors.Is(queryErr, sql.ErrNoRows) {
			return queryErr
		}

		// Both running-job counts are per tenant: the limit exists to cap this
		// agent's extraction work, and counting another agent's jobs would let
		// one tenant's backlog starve every other tenant's memory.
		result, execErr := tx.ExecContext(ctx, `INSERT INTO fb_memory_jobs(
			kind,job_key,agent_id,status,ownership_token,started_at,finished_at,lease_until,retry_at,
			retry_remaining,last_error,input_watermark,last_success_watermark)
		SELECT ?,?,?,?, ?,?,NULL,?,NULL,?,NULL,?,NULL
		WHERE (SELECT COUNT(*) FROM fb_memory_jobs WHERE agent_id=? AND kind=? AND status=? AND lease_until IS NOT NULL AND lease_until>?)<?
		ON CONFLICT(kind,job_key) DO UPDATE SET
			status=excluded.status,ownership_token=excluded.ownership_token,
			started_at=excluded.started_at,finished_at=NULL,lease_until=excluded.lease_until,retry_at=NULL,
			retry_remaining=CASE WHEN excluded.input_watermark>COALESCE(fb_memory_jobs.input_watermark,-1) THEN ? ELSE fb_memory_jobs.retry_remaining END,
			last_error=NULL,input_watermark=excluded.input_watermark
		WHERE fb_memory_jobs.agent_id=excluded.agent_id
		  AND (fb_memory_jobs.status<>? OR fb_memory_jobs.lease_until IS NULL OR fb_memory_jobs.lease_until<=excluded.started_at)
		  AND (fb_memory_jobs.retry_at IS NULL OR fb_memory_jobs.retry_at<=excluded.started_at OR excluded.input_watermark>COALESCE(fb_memory_jobs.input_watermark,-1))
		  AND (fb_memory_jobs.retry_remaining>0 OR excluded.input_watermark>COALESCE(fb_memory_jobs.input_watermark,-1))
		  AND (SELECT COUNT(*) FROM fb_memory_jobs AS running_jobs
		       WHERE running_jobs.agent_id=excluded.agent_id AND running_jobs.kind=excluded.kind AND running_jobs.status=?
		         AND running_jobs.lease_until IS NOT NULL AND running_jobs.lease_until>excluded.started_at
		         AND running_jobs.job_key<>excluded.job_key)<?`,
			JobKindStage1, threadID, agentID, jobStatusRunning, token, now, now+max64(int64(leaseSeconds), 0),
			defaultRetryRemaining, sourceUpdatedAt, agentID, JobKindStage1, jobStatusRunning, now, maxRunning,
			defaultRetryRemaining, jobStatusRunning, jobStatusRunning, maxRunning)
		if execErr != nil {
			return execErr
		}
		affected, execErr := result.RowsAffected()
		if execErr != nil {
			return execErr
		}
		if affected > 0 {
			outcome = Stage1Claimed
			return nil
		}
		var status string
		var leaseUntil, retryAt sql.NullInt64
		var retryRemaining int
		queryErr = tx.QueryRowContext(ctx, `SELECT status,lease_until,retry_at,retry_remaining FROM fb_memory_jobs WHERE kind=? AND job_key=? AND agent_id=?`, JobKindStage1, threadID, agentID).Scan(&status, &leaseUntil, &retryAt, &retryRemaining)
		if queryErr != nil && !errors.Is(queryErr, sql.ErrNoRows) {
			return queryErr
		}
		switch {
		case retryRemaining <= 0:
			outcome = Stage1RetryExhausted
		case retryAt.Valid && retryAt.Int64 > now:
			outcome = Stage1RetryBackoff
		default:
			outcome = Stage1Running
		}
		return nil
	})
	if err != nil {
		return "", "", err
	}
	if outcome != Stage1Claimed {
		token = ""
	}
	return outcome, token, nil
}

func (s *Store) MarkStage1Succeeded(ctx context.Context, threadID, projectKey, ownershipToken string, sourceUpdatedAt int64, rawMemory, rolloutSummary string, rolloutSlug *string) (bool, error) {
	if s == nil || s.DB == nil {
		return false, nil
	}
	agentID, err := s.agent()
	if err != nil {
		return false, err
	}
	owned := false
	err = s.withImmediate(ctx, func(tx *sql.Tx) error {
		var err error
		owned, err = markStage1SucceededOn(ctx, tx, agentID, threadID, projectKey, ownershipToken, sourceUpdatedAt, rawMemory, rolloutSummary, rolloutSlug)
		return err
	})
	return owned, err
}

// markStage1SucceededOn records one thread's extracted memory and enqueues
// that thread's project scope for its next phase-2 pass. projectKey must be
// the project scope the thread's cwd resolved to at claim time — every row in
// fb_memory_stage1_outputs is required to carry one (see the schema comment),
// so a caller that has none has nothing valid to write.
func markStage1SucceededOn(ctx context.Context, tx *sql.Tx, agentID, threadID, projectKey, ownershipToken string, sourceUpdatedAt int64, rawMemory, rolloutSummary string, rolloutSlug *string) (bool, error) {
	projectKey = strings.TrimSpace(projectKey)
	if projectKey == "" {
		return false, fmt.Errorf("project_key is required")
	}
	now := time.Now().Unix()
	result, err := tx.ExecContext(ctx, `UPDATE fb_memory_jobs SET status=?,finished_at=?,lease_until=NULL,last_error=NULL,last_success_watermark=input_watermark
		WHERE kind=? AND job_key=? AND agent_id=? AND status=? AND ownership_token=?`, jobStatusDone, now, JobKindStage1, strings.TrimSpace(threadID), agentID, jobStatusRunning, ownershipToken)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil || affected == 0 {
		return false, err
	}
	var slug any
	if rolloutSlug != nil {
		slug = *rolloutSlug
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO fb_memory_stage1_outputs(thread_id,agent_id,project_key,source_updated_at,raw_memory,rollout_summary,rollout_slug,generated_at)
		VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(thread_id) DO UPDATE SET project_key=excluded.project_key,source_updated_at=excluded.source_updated_at,
		raw_memory=excluded.raw_memory,rollout_summary=excluded.rollout_summary,rollout_slug=excluded.rollout_slug,generated_at=excluded.generated_at
		WHERE excluded.source_updated_at>=fb_memory_stage1_outputs.source_updated_at
		  AND fb_memory_stage1_outputs.agent_id=excluded.agent_id`, strings.TrimSpace(threadID), agentID, projectKey, sourceUpdatedAt, rawMemory, rolloutSummary, slug, now); err != nil {
		return false, err
	}
	scope, ok := ProjectScope(projectKey)
	if !ok {
		return false, fmt.Errorf("project_key is required")
	}
	if err := enqueueGlobalPhase2On(ctx, tx, agentID, scope, sourceUpdatedAt); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) CompleteStage1WithEvidence(ctx context.Context, threadID, projectKey, ownershipToken string, sourceUpdatedAt int64, rawMemory, rolloutSummary string, rolloutSlug *string, stagedPath, canonicalPath string) (bool, error) {
	if s == nil || s.DB == nil {
		_ = os.Remove(stagedPath)
		return false, nil
	}
	defer os.Remove(stagedPath)
	agentID, err := s.agent()
	if err != nil {
		return false, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	owned, err := markStage1SucceededOn(ctx, tx, agentID, threadID, projectKey, ownershipToken, sourceUpdatedAt, rawMemory, rolloutSummary, rolloutSlug)
	if err != nil {
		return false, err
	}
	if !owned {
		return false, nil
	}
	restore, finalize, err := publishStagedRolloutEvidence(stagedPath, canonicalPath)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		if restoreErr := restore(); restoreErr != nil {
			return false, fmt.Errorf("commit stage-one completion: %v; restore rollout evidence: %w", err, restoreErr)
		}
		return false, err
	}
	finalize()
	return true, nil
}

func (s *Store) MarkStage1SucceededNoOutput(ctx context.Context, threadID, ownershipToken string) (bool, error) {
	if s == nil || s.DB == nil {
		return false, nil
	}
	agentID, err := s.agent()
	if err != nil {
		return false, err
	}
	owned := false
	err = s.withImmediate(ctx, func(tx *sql.Tx) error {
		now := time.Now().Unix()
		result, err := tx.ExecContext(ctx, `UPDATE fb_memory_jobs SET status=?,finished_at=?,lease_until=NULL,last_error=NULL,last_success_watermark=input_watermark
			WHERE kind=? AND job_key=? AND agent_id=? AND status=? AND ownership_token=?`, jobStatusDone, now, JobKindStage1, strings.TrimSpace(threadID), agentID, jobStatusRunning, ownershipToken)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil || affected == 0 {
			return err
		}
		owned = true
		var watermark int64
		if err := tx.QueryRowContext(ctx, `SELECT input_watermark FROM fb_memory_jobs WHERE kind=? AND job_key=? AND agent_id=? AND ownership_token=?`, JobKindStage1, strings.TrimSpace(threadID), agentID, ownershipToken).Scan(&watermark); err != nil {
			return err
		}
		var projectKey string
		queryErr := tx.QueryRowContext(ctx, `SELECT project_key FROM fb_memory_stage1_outputs WHERE thread_id=? AND agent_id=?`, strings.TrimSpace(threadID), agentID).Scan(&projectKey)
		if queryErr != nil && !errors.Is(queryErr, sql.ErrNoRows) {
			return queryErr
		}
		result, err = tx.ExecContext(ctx, `DELETE FROM fb_memory_stage1_outputs WHERE thread_id=? AND agent_id=?`, strings.TrimSpace(threadID), agentID)
		if err != nil {
			return err
		}
		deleted, err := result.RowsAffected()
		if err != nil || deleted == 0 {
			return err
		}
		scope, ok := ProjectScope(projectKey)
		if !ok {
			return fmt.Errorf("stage-one row for thread %s carries no project scope", strings.TrimSpace(threadID))
		}
		return enqueueGlobalPhase2On(ctx, tx, agentID, scope, watermark)
	})
	return owned, err
}

func (s *Store) MarkStage1Failed(ctx context.Context, threadID, ownershipToken, reason string) (bool, error) {
	if s == nil || s.DB == nil {
		return false, nil
	}
	agentID, err := s.agent()
	if err != nil {
		return false, err
	}
	now := time.Now().Unix()
	result, err := s.DB.ExecContext(ctx, `UPDATE fb_memory_jobs SET status=?,finished_at=?,lease_until=NULL,retry_at=?,retry_remaining=retry_remaining-1,last_error=?
		WHERE kind=? AND job_key=? AND agent_id=? AND status=? AND ownership_token=?`, jobStatusError, now, now+stage1RetryDelay, reason, JobKindStage1, strings.TrimSpace(threadID), agentID, jobStatusRunning, ownershipToken)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected > 0, err
}

// EnqueueGlobalPhase2 schedules a scope's next consolidation pass. Despite the
// name it is not agent-global: scope selects exactly one project's pass or the
// agent's single global pass, each with its own lease, cooldown, and retry
// budget (see consolidateJobKey).
func (s *Store) EnqueueGlobalPhase2(ctx context.Context, scope Scope, inputWatermark int64) error {
	if s == nil || s.DB == nil {
		return nil
	}
	agentID, err := s.agent()
	if err != nil {
		return err
	}
	return enqueueGlobalPhase2On(ctx, s.DB, agentID, scope, inputWatermark)
}

type contextExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func enqueueGlobalPhase2On(ctx context.Context, executor contextExecutor, agentID string, scope Scope, inputWatermark int64) error {
	_, err := executor.ExecContext(ctx, `INSERT INTO fb_memory_jobs(kind,job_key,agent_id,status,retry_remaining,input_watermark,last_success_watermark)
		VALUES(?,?,?,?,?,?,0) ON CONFLICT(kind,job_key) DO UPDATE SET
		status=CASE WHEN fb_memory_jobs.status=? THEN ? ELSE ? END,
		retry_at=CASE WHEN fb_memory_jobs.status=? THEN fb_memory_jobs.retry_at ELSE NULL END,
		retry_remaining=MAX(fb_memory_jobs.retry_remaining,excluded.retry_remaining),
		input_watermark=CASE WHEN excluded.input_watermark>COALESCE(fb_memory_jobs.input_watermark,0)
			THEN excluded.input_watermark ELSE COALESCE(fb_memory_jobs.input_watermark,0)+1 END
		WHERE fb_memory_jobs.agent_id=excluded.agent_id`,
		JobKindConsolidate, consolidateJobKey(agentID, scope), agentID, jobStatusPending, defaultRetryRemaining, inputWatermark,
		jobStatusRunning, jobStatusRunning, jobStatusPending, jobStatusRunning)
	return err
}

func (s *Store) TryClaimGlobalPhase2(ctx context.Context, scope Scope) (claim Phase2Claim, err error) {
	if s == nil || s.DB == nil {
		return Phase2Claim{Outcome: Phase2Running}, nil
	}
	agentID, err := s.agent()
	if err != nil {
		return Phase2Claim{Outcome: Phase2Running}, err
	}
	jobKey := consolidateJobKey(agentID, scope)
	now := time.Now().Unix()
	token := uuid.NewString()
	err = s.withImmediate(ctx, func(tx *sql.Tx) error {
		var status string
		var leaseUntil, retryAt, inputWatermark, finishedAt sql.NullInt64
		var lastError sql.NullString
		queryErr := tx.QueryRowContext(ctx, `SELECT status,lease_until,retry_at,input_watermark,finished_at,last_error
			FROM fb_memory_jobs WHERE kind=? AND job_key=? AND agent_id=?`, JobKindConsolidate, jobKey, agentID).Scan(&status, &leaseUntil, &retryAt, &inputWatermark, &finishedAt, &lastError)
		if errors.Is(queryErr, sql.ErrNoRows) {
			_, insertErr := tx.ExecContext(ctx, `INSERT INTO fb_memory_jobs(kind,job_key,agent_id,status,ownership_token,started_at,lease_until,retry_remaining,input_watermark,last_success_watermark)
				VALUES(?,?,?,?,?,?,?,?,0,0)`, JobKindConsolidate, jobKey, agentID, jobStatusRunning, token, now, now+phase2LeaseSeconds, defaultRetryRemaining)
			if insertErr == nil {
				claim = Phase2Claim{Outcome: Phase2Claimed, OwnershipToken: token}
			}
			return insertErr
		}
		if queryErr != nil {
			return queryErr
		}
		claim.InputWatermark = inputWatermark.Int64
		switch {
		case retryAt.Valid && retryAt.Int64 > now:
			claim.Outcome = Phase2RetryUnavailable
			return nil
		case status == jobStatusRunning && leaseUntil.Valid && leaseUntil.Int64 > now:
			claim.Outcome = Phase2Running
			return nil
		case !lastError.Valid && finishedAt.Valid && finishedAt.Int64 > now-phase2CooldownSeconds:
			claim.Outcome = Phase2Cooldown
			return nil
		}
		result, updateErr := tx.ExecContext(ctx, `UPDATE fb_memory_jobs SET status=?,ownership_token=?,started_at=?,finished_at=NULL,lease_until=?,retry_at=NULL,last_error=NULL
			WHERE kind=? AND job_key=? AND agent_id=? AND (status<>? OR lease_until IS NULL OR lease_until<=?)
			AND (retry_at IS NULL OR retry_at<=?) AND (last_error IS NOT NULL OR finished_at IS NULL OR finished_at<=?)`,
			jobStatusRunning, token, now, now+phase2LeaseSeconds,
			JobKindConsolidate, jobKey, agentID, jobStatusRunning, now, now, now-phase2CooldownSeconds)
		if updateErr != nil {
			return updateErr
		}
		affected, updateErr := result.RowsAffected()
		if updateErr != nil {
			return updateErr
		}
		if affected == 0 {
			claim.Outcome = Phase2Running
		} else {
			claim.Outcome = Phase2Claimed
			claim.OwnershipToken = token
		}
		return nil
	})
	return claim, err
}

func (s *Store) HeartbeatGlobalPhase2(ctx context.Context, scope Scope, ownershipToken string) (bool, error) {
	if s == nil || s.DB == nil {
		return false, nil
	}
	agentID, err := s.agent()
	if err != nil {
		return false, err
	}
	result, err := s.DB.ExecContext(ctx, `UPDATE fb_memory_jobs SET lease_until=? WHERE kind=? AND job_key=? AND agent_id=? AND status=? AND ownership_token=?`, time.Now().Unix()+phase2LeaseSeconds, JobKindConsolidate, consolidateJobKey(agentID, scope), agentID, jobStatusRunning, ownershipToken)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected > 0, err
}

// MarkGlobalPhase2Succeeded records one scope's consolidation pass as done.
// For a project scope, selected replaces that project's phase-2 selection —
// scoped to the same project_key, so consolidating project A can never clear
// or overwrite project B's in-flight selection. The global scope has no
// stage-1 selection of its own (its input is project scopes' promoted
// candidates, not raw session transcripts), so selected must be empty there.
func (s *Store) MarkGlobalPhase2Succeeded(ctx context.Context, scope Scope, ownershipToken string, watermark int64, selected []Stage1Output) (bool, error) {
	if s == nil || s.DB == nil {
		return false, nil
	}
	agentID, err := s.agent()
	if err != nil {
		return false, err
	}
	owned := false
	err = s.withImmediate(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE fb_memory_jobs SET status=?,finished_at=?,lease_until=NULL,last_error=NULL,
			last_success_watermark=MAX(COALESCE(last_success_watermark,0),?)
			WHERE kind=? AND job_key=? AND agent_id=? AND status=? AND ownership_token=?`, jobStatusDone, time.Now().Unix(), watermark, JobKindConsolidate, consolidateJobKey(agentID, scope), agentID, jobStatusRunning, ownershipToken)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil || affected == 0 {
			return err
		}
		owned = true
		if scope.Kind != ScopeProject {
			return nil
		}
		// Clearing the previous selection is scoped to this tenant AND this
		// project: clearing every project's selection here would drop the
		// selection another project's consolidation is currently working from.
		if _, err := tx.ExecContext(ctx, `UPDATE fb_memory_stage1_outputs SET selected_for_phase2=0,selected_for_phase2_source_updated_at=NULL
			WHERE agent_id=? AND project_key=? AND (selected_for_phase2<>0 OR selected_for_phase2_source_updated_at IS NOT NULL)`, agentID, scope.Key); err != nil {
			return err
		}
		for _, output := range selected {
			if _, err := tx.ExecContext(ctx, `UPDATE fb_memory_stage1_outputs SET selected_for_phase2=1,selected_for_phase2_source_updated_at=?
				WHERE thread_id=? AND agent_id=? AND project_key=? AND source_updated_at=?`, output.SourceUpdatedAt, output.ThreadID, agentID, scope.Key, output.SourceUpdatedAt); err != nil {
				return err
			}
		}
		return nil
	})
	return owned, err
}

func (s *Store) MarkGlobalPhase2Failed(ctx context.Context, scope Scope, ownershipToken, reason string, allowUnowned bool) (bool, error) {
	if s == nil || s.DB == nil {
		return false, nil
	}
	agentID, err := s.agent()
	if err != nil {
		return false, err
	}
	ownershipClause := `ownership_token=?`
	if allowUnowned {
		ownershipClause = `(ownership_token=? OR ownership_token IS NULL)`
	}
	now := time.Now().Unix()
	result, err := s.DB.ExecContext(ctx, `UPDATE fb_memory_jobs SET status=?,finished_at=?,lease_until=NULL,retry_at=?,
		retry_remaining=MAX(retry_remaining-1,0),last_error=? WHERE kind=? AND job_key=? AND agent_id=? AND status=? AND `+ownershipClause,
		jobStatusError, now, now+phase2RetryDelay, reason, JobKindConsolidate, consolidateJobKey(agentID, scope), agentID, jobStatusRunning, ownershipToken)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected > 0, err
}

func max64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}
