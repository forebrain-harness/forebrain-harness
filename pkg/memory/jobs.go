package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/state/dbx"
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
	// sessionSourceCron is the session-purpose value a fire's conversation is
	// born with. It mirrors state.SessionSourceCron; this package does not
	// import state, so a test pins the two equal.
	sessionSourceCron = "cron"
	// sessionSourceSubagent is the session-purpose value a subagent's private
	// conversation is born with. It mirrors state.SessionSourceSubagent and is
	// pinned equal by the same test: a subagent's work is already represented
	// in its dispatching conversation, so extracting from it would pay a
	// stage-1 model call to relearn what the main session already holds.
	sessionSourceSubagent = "subagent"
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

// The statements the pipeline runs, below the constants they name.
// The memory pipeline's statements: the stage-one claim, the job ledger and
// the phase-2 pass. Every row is tenant data, so every statement carries the
// owning agent.
//
// The two upserts are guarded — a job may not be claimed twice, and a
// stage-one row is not replaced by an older extraction — and MySQL's
// ON DUPLICATE KEY UPDATE has no WHERE, so each guarded column is wrapped in
// the same IF: a false guard assigns every column its own value, which is
// exactly the skipped update the other two engines report as no rows affected.
var (
	memStage1CandidatesSQL = dbx.SQL{
		SQLite: `SELECT id, updated_at, created_at, cwd, git_branch, memory_source
		FROM fb_sessions
		WHERE agent_id=? AND memory_mode=? AND id<>? AND memory_source IN (?,?)
		  AND TRIM(cwd)<>'' AND updated_at>=? AND updated_at<=? AND source NOT IN (?,?)
		ORDER BY updated_at DESC, id DESC LIMIT ?`,
		MySQL: `SELECT id, updated_at, created_at, cwd, git_branch, memory_source
		FROM fb_sessions
		WHERE agent_id=? AND memory_mode=? AND id<>? AND memory_source IN (?,?)
		  AND TRIM(cwd)<>'' AND updated_at>=? AND updated_at<=? AND source NOT IN (?,?)
		ORDER BY updated_at DESC, id DESC LIMIT ?`,
		Postgres: `SELECT id, updated_at, created_at, cwd, git_branch, memory_source
		FROM fb_sessions
		WHERE agent_id=$1 AND memory_mode=$2 AND id<>$3 AND memory_source IN ($4,$5)
		  AND TRIM(cwd)<>'' AND updated_at>=$6 AND updated_at<=$7 AND source NOT IN ($8,$9)
		ORDER BY updated_at DESC, id DESC LIMIT $10`,
	}
	memStage1SourceWatermarkSQL = dbx.SQL{
		SQLite:   `SELECT source_updated_at FROM fb_memory_stage1_outputs WHERE thread_id=? AND agent_id=?`,
		MySQL:    `SELECT source_updated_at FROM fb_memory_stage1_outputs WHERE thread_id=? AND agent_id=?`,
		Postgres: `SELECT source_updated_at FROM fb_memory_stage1_outputs WHERE thread_id=$1 AND agent_id=$2`,
	}
	memJobSuccessWatermarkSQL = dbx.SQL{
		SQLite:   `SELECT last_success_watermark FROM fb_memory_jobs WHERE kind=? AND job_key=? AND agent_id=?`,
		MySQL:    `SELECT last_success_watermark FROM fb_memory_jobs WHERE kind=? AND job_key=? AND agent_id=?`,
		Postgres: `SELECT last_success_watermark FROM fb_memory_jobs WHERE kind=$1 AND job_key=$2 AND agent_id=$3`,
	}
	memJobStateSQL = dbx.SQL{
		SQLite:   `SELECT status,lease_until,retry_at,retry_remaining FROM fb_memory_jobs WHERE kind=? AND job_key=? AND agent_id=?`,
		MySQL:    `SELECT status,lease_until,retry_at,retry_remaining FROM fb_memory_jobs WHERE kind=? AND job_key=? AND agent_id=?`,
		Postgres: `SELECT status,lease_until,retry_at,retry_remaining FROM fb_memory_jobs WHERE kind=$1 AND job_key=$2 AND agent_id=$3`,
	}
	memJobInputWatermarkSQL = dbx.SQL{
		SQLite:   `SELECT input_watermark FROM fb_memory_jobs WHERE kind=? AND job_key=? AND agent_id=? AND ownership_token=?`,
		MySQL:    `SELECT input_watermark FROM fb_memory_jobs WHERE kind=? AND job_key=? AND agent_id=? AND ownership_token=?`,
		Postgres: `SELECT input_watermark FROM fb_memory_jobs WHERE kind=$1 AND job_key=$2 AND agent_id=$3 AND ownership_token=$4`,
	}
	memStage1ProjectKeySQL = dbx.SQL{
		SQLite:   `SELECT project_key FROM fb_memory_stage1_outputs WHERE thread_id=? AND agent_id=?`,
		MySQL:    `SELECT project_key FROM fb_memory_stage1_outputs WHERE thread_id=? AND agent_id=?`,
		Postgres: `SELECT project_key FROM fb_memory_stage1_outputs WHERE thread_id=$1 AND agent_id=$2`,
	}
	memJobDetailSQL = dbx.SQL{
		SQLite: `SELECT status,lease_until,retry_at,input_watermark,finished_at,last_error
			FROM fb_memory_jobs WHERE kind=? AND job_key=? AND agent_id=?`,
		MySQL: `SELECT status,lease_until,retry_at,input_watermark,finished_at,last_error
			FROM fb_memory_jobs WHERE kind=? AND job_key=? AND agent_id=?`,
		Postgres: `SELECT status,lease_until,retry_at,input_watermark,finished_at,last_error
			FROM fb_memory_jobs WHERE kind=$1 AND job_key=$2 AND agent_id=$3`,
	}
	memStage1SuccessUpsertSQL = dbx.SQL{
		SQLite: `INSERT INTO fb_memory_stage1_outputs(thread_id,agent_id,project_key,source_updated_at,raw_memory,rollout_summary,rollout_slug,generated_at,last_message_row_id)
		VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(thread_id) DO UPDATE SET project_key=excluded.project_key,source_updated_at=excluded.source_updated_at,
		raw_memory=excluded.raw_memory,rollout_summary=excluded.rollout_summary,rollout_slug=excluded.rollout_slug,generated_at=excluded.generated_at,
		last_message_row_id=excluded.last_message_row_id
		WHERE excluded.source_updated_at>=fb_memory_stage1_outputs.source_updated_at
		  AND fb_memory_stage1_outputs.agent_id=excluded.agent_id`,
		MySQL: `INSERT INTO fb_memory_stage1_outputs(thread_id,agent_id,project_key,source_updated_at,raw_memory,rollout_summary,rollout_slug,generated_at,last_message_row_id)
		VALUES(?,?,?,?,?,?,?,?,?)
	ON DUPLICATE KEY UPDATE project_key=IF(VALUES(source_updated_at)>=fb_memory_stage1_outputs.source_updated_at AND fb_memory_stage1_outputs.agent_id=VALUES(agent_id), VALUES(project_key), fb_memory_stage1_outputs.project_key),
		source_updated_at=IF(VALUES(source_updated_at)>=fb_memory_stage1_outputs.source_updated_at AND fb_memory_stage1_outputs.agent_id=VALUES(agent_id), VALUES(source_updated_at), fb_memory_stage1_outputs.source_updated_at),
		raw_memory=IF(VALUES(source_updated_at)>=fb_memory_stage1_outputs.source_updated_at AND fb_memory_stage1_outputs.agent_id=VALUES(agent_id), VALUES(raw_memory), fb_memory_stage1_outputs.raw_memory),
		rollout_summary=IF(VALUES(source_updated_at)>=fb_memory_stage1_outputs.source_updated_at AND fb_memory_stage1_outputs.agent_id=VALUES(agent_id), VALUES(rollout_summary), fb_memory_stage1_outputs.rollout_summary),
		rollout_slug=IF(VALUES(source_updated_at)>=fb_memory_stage1_outputs.source_updated_at AND fb_memory_stage1_outputs.agent_id=VALUES(agent_id), VALUES(rollout_slug), fb_memory_stage1_outputs.rollout_slug),
		generated_at=IF(VALUES(source_updated_at)>=fb_memory_stage1_outputs.source_updated_at AND fb_memory_stage1_outputs.agent_id=VALUES(agent_id), VALUES(generated_at), fb_memory_stage1_outputs.generated_at),
		last_message_row_id=IF(VALUES(source_updated_at)>=fb_memory_stage1_outputs.source_updated_at AND fb_memory_stage1_outputs.agent_id=VALUES(agent_id), VALUES(last_message_row_id), fb_memory_stage1_outputs.last_message_row_id)`,
		Postgres: `INSERT INTO fb_memory_stage1_outputs(thread_id,agent_id,project_key,source_updated_at,raw_memory,rollout_summary,rollout_slug,generated_at,last_message_row_id)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(thread_id) DO UPDATE SET project_key=excluded.project_key,source_updated_at=excluded.source_updated_at,
		raw_memory=excluded.raw_memory,rollout_summary=excluded.rollout_summary,rollout_slug=excluded.rollout_slug,generated_at=excluded.generated_at,
		last_message_row_id=excluded.last_message_row_id
		WHERE excluded.source_updated_at>=fb_memory_stage1_outputs.source_updated_at
		  AND fb_memory_stage1_outputs.agent_id=excluded.agent_id`,
	}
	memJobMarkFailedSQL = dbx.SQL{
		SQLite: `UPDATE fb_memory_jobs SET status=?,finished_at=?,lease_until=NULL,retry_at=?,retry_remaining=retry_remaining-1,last_error=?
		WHERE kind=? AND job_key=? AND agent_id=? AND status=? AND ownership_token=?`,
		MySQL: `UPDATE fb_memory_jobs SET status=?,finished_at=?,lease_until=NULL,retry_at=?,retry_remaining=retry_remaining-1,last_error=?
		WHERE kind=? AND job_key=? AND agent_id=? AND status=? AND ownership_token=?`,
		Postgres: `UPDATE fb_memory_jobs SET status=$1,finished_at=$2,lease_until=NULL,retry_at=$3,retry_remaining=retry_remaining-1,last_error=$4
		WHERE kind=$5 AND job_key=$6 AND agent_id=$7 AND status=$8 AND ownership_token=$9`,
	}
	memPhase2EnqueueSQL = dbx.SQL{
		SQLite: `INSERT INTO fb_memory_jobs(kind,job_key,agent_id,status,retry_remaining,input_watermark,last_success_watermark)
		VALUES(?,?,?,?,?,?,0) ON CONFLICT(kind,job_key) DO UPDATE SET
		status=CASE WHEN fb_memory_jobs.status=? THEN ? ELSE ? END,
		retry_at=CASE WHEN fb_memory_jobs.status=? THEN fb_memory_jobs.retry_at ELSE NULL END,
		retry_remaining=MAX(fb_memory_jobs.retry_remaining,excluded.retry_remaining),
		input_watermark=CASE WHEN excluded.input_watermark>COALESCE(fb_memory_jobs.input_watermark,0)
			THEN excluded.input_watermark ELSE COALESCE(fb_memory_jobs.input_watermark,0)+1 END
		WHERE fb_memory_jobs.agent_id=excluded.agent_id`,
		MySQL: `INSERT INTO fb_memory_jobs(kind,job_key,agent_id,status,retry_remaining,input_watermark,last_success_watermark)
		VALUES(?,?,?,?,?,?,0)
	ON DUPLICATE KEY UPDATE
		status=IF(fb_memory_jobs.agent_id=VALUES(agent_id), IF(fb_memory_jobs.status=?, ?, ?), fb_memory_jobs.status),
		retry_at=IF(fb_memory_jobs.agent_id=VALUES(agent_id), IF(fb_memory_jobs.status=?, fb_memory_jobs.retry_at, NULL), fb_memory_jobs.retry_at),
		retry_remaining=GREATEST(fb_memory_jobs.retry_remaining, VALUES(retry_remaining)),
		input_watermark=IF(fb_memory_jobs.agent_id=VALUES(agent_id), IF(VALUES(input_watermark)>COALESCE(fb_memory_jobs.input_watermark,0),
			VALUES(input_watermark), COALESCE(fb_memory_jobs.input_watermark,0)+1), fb_memory_jobs.input_watermark)`,
		Postgres: `INSERT INTO fb_memory_jobs(kind,job_key,agent_id,status,retry_remaining,input_watermark,last_success_watermark)
		VALUES($1,$2,$3,$4,$5,$6,0) ON CONFLICT(kind,job_key) DO UPDATE SET
		status=CASE WHEN fb_memory_jobs.status=$7 THEN $8 ELSE $9 END,
		retry_at=CASE WHEN fb_memory_jobs.status=$10 THEN fb_memory_jobs.retry_at ELSE NULL END,
		retry_remaining=GREATEST(fb_memory_jobs.retry_remaining,excluded.retry_remaining),
		input_watermark=CASE WHEN excluded.input_watermark>COALESCE(fb_memory_jobs.input_watermark,0)
			THEN excluded.input_watermark ELSE COALESCE(fb_memory_jobs.input_watermark,0)+1 END
		WHERE fb_memory_jobs.agent_id=excluded.agent_id`,
	}
	memPhase2InsertSQL = dbx.SQL{
		SQLite: `INSERT INTO fb_memory_jobs(kind,job_key,agent_id,status,ownership_token,started_at,lease_until,retry_remaining,input_watermark,last_success_watermark)
				VALUES(?,?,?,?,?,?,?,?,0,0)`,
		MySQL: `INSERT INTO fb_memory_jobs(kind,job_key,agent_id,status,ownership_token,started_at,lease_until,retry_remaining,input_watermark,last_success_watermark)
				VALUES(?,?,?,?,?,?,?,?,0,0)`,
		Postgres: `INSERT INTO fb_memory_jobs(kind,job_key,agent_id,status,ownership_token,started_at,lease_until,retry_remaining,input_watermark,last_success_watermark)
				VALUES($1,$2,$3,$4,$5,$6,$7,$8,0,0)`,
	}
	memPhase2ClaimUpdateSQL = dbx.SQL{
		SQLite: `UPDATE fb_memory_jobs SET status=?,ownership_token=?,started_at=?,finished_at=NULL,lease_until=?,retry_at=NULL,last_error=NULL
			WHERE kind=? AND job_key=? AND agent_id=? AND (status<>? OR lease_until IS NULL OR lease_until<=?)
			AND (retry_at IS NULL OR retry_at<=?) AND (last_error IS NOT NULL OR finished_at IS NULL OR finished_at<=?)`,
		MySQL: `UPDATE fb_memory_jobs SET status=?,ownership_token=?,started_at=?,finished_at=NULL,lease_until=?,retry_at=NULL,last_error=NULL
			WHERE kind=? AND job_key=? AND agent_id=? AND (status<>? OR lease_until IS NULL OR lease_until<=?)
			AND (retry_at IS NULL OR retry_at<=?) AND (last_error IS NOT NULL OR finished_at IS NULL OR finished_at<=?)`,
		Postgres: `UPDATE fb_memory_jobs SET status=$1,ownership_token=$2,started_at=$3,finished_at=NULL,lease_until=$4,retry_at=NULL,last_error=NULL
			WHERE kind=$5 AND job_key=$6 AND agent_id=$7 AND (status<>$8 OR lease_until IS NULL OR lease_until<=$9)
			AND (retry_at IS NULL OR retry_at<=$10) AND (last_error IS NOT NULL OR finished_at IS NULL OR finished_at<=$11)`,
	}
	memPhase2HeartbeatSQL = dbx.SQL{
		SQLite:   `UPDATE fb_memory_jobs SET lease_until=? WHERE kind=? AND job_key=? AND agent_id=? AND status=? AND ownership_token=?`,
		MySQL:    `UPDATE fb_memory_jobs SET lease_until=? WHERE kind=? AND job_key=? AND agent_id=? AND status=? AND ownership_token=?`,
		Postgres: `UPDATE fb_memory_jobs SET lease_until=$1 WHERE kind=$2 AND job_key=$3 AND agent_id=$4 AND status=$5 AND ownership_token=$6`,
	}
	// memJobCloseStage1SQL is the same close as memJobCloseOwnedSQL with the
	// stage-one statements' own layout: SQLite's text is what it is, and the
	// snapshot holds both spellings.
	memJobCloseStage1SQL = dbx.SQL{
		SQLite: `UPDATE fb_memory_jobs SET status=?,finished_at=?,lease_until=NULL,last_error=NULL,last_success_watermark=input_watermark
			WHERE kind=? AND job_key=? AND agent_id=? AND status=? AND ownership_token=?`,
		MySQL: `UPDATE fb_memory_jobs SET status=?,finished_at=?,lease_until=NULL,last_error=NULL,last_success_watermark=input_watermark
			WHERE kind=? AND job_key=? AND agent_id=? AND status=? AND ownership_token=?`,
		Postgres: `UPDATE fb_memory_jobs SET status=$1,finished_at=$2,lease_until=NULL,last_error=NULL,last_success_watermark=input_watermark
			WHERE kind=$3 AND job_key=$4 AND agent_id=$5 AND status=$6 AND ownership_token=$7`,
	}
	// memPhase2SucceedSQL records a pass as done, keeping the furthest
	// watermark either engine ever reached; MAX(a,b) is scalar here, so MySQL
	// and PostgreSQL spell it GREATEST.
	memPhase2SucceedSQL = dbx.SQL{
		SQLite: `UPDATE fb_memory_jobs SET status=?,finished_at=?,lease_until=NULL,last_error=NULL,
			last_success_watermark=MAX(COALESCE(last_success_watermark,0),?)
			WHERE kind=? AND job_key=? AND agent_id=? AND status=? AND ownership_token=?`,
		MySQL: `UPDATE fb_memory_jobs SET status=?,finished_at=?,lease_until=NULL,last_error=NULL,
			last_success_watermark=GREATEST(COALESCE(last_success_watermark,0),?)
			WHERE kind=? AND job_key=? AND agent_id=? AND status=? AND ownership_token=?`,
		Postgres: `UPDATE fb_memory_jobs SET status=$1,finished_at=$2,lease_until=NULL,last_error=NULL,
			last_success_watermark=GREATEST(COALESCE(last_success_watermark,0),$3)
			WHERE kind=$4 AND job_key=$5 AND agent_id=$6 AND status=$7 AND ownership_token=$8`,
	}
	memPhase2SelectClearSQL = dbx.SQL{
		SQLite: `UPDATE fb_memory_stage1_outputs SET selected_for_phase2=0,selected_for_phase2_source_updated_at=NULL
			WHERE agent_id=? AND project_key=? AND (selected_for_phase2<>0 OR selected_for_phase2_source_updated_at IS NOT NULL)`,
		MySQL: `UPDATE fb_memory_stage1_outputs SET selected_for_phase2=0,selected_for_phase2_source_updated_at=NULL
			WHERE agent_id=? AND project_key=? AND (selected_for_phase2<>0 OR selected_for_phase2_source_updated_at IS NOT NULL)`,
		Postgres: `UPDATE fb_memory_stage1_outputs SET selected_for_phase2=0,selected_for_phase2_source_updated_at=NULL
			WHERE agent_id=$1 AND project_key=$2 AND (selected_for_phase2<>0 OR selected_for_phase2_source_updated_at IS NOT NULL)`,
	}
	memPhase2SelectMarkSQL = dbx.SQL{
		SQLite: `UPDATE fb_memory_stage1_outputs SET selected_for_phase2=1,selected_for_phase2_source_updated_at=?
				WHERE thread_id=? AND agent_id=? AND project_key=? AND source_updated_at=?`,
		MySQL: `UPDATE fb_memory_stage1_outputs SET selected_for_phase2=1,selected_for_phase2_source_updated_at=?
				WHERE thread_id=? AND agent_id=? AND project_key=? AND source_updated_at=?`,
		Postgres: `UPDATE fb_memory_stage1_outputs SET selected_for_phase2=1,selected_for_phase2_source_updated_at=$1
				WHERE thread_id=$2 AND agent_id=$3 AND project_key=$4 AND source_updated_at=$5`,
	}
	memPhase2FailSQL = dbx.SQL{
		SQLite: `UPDATE fb_memory_jobs SET status=?,finished_at=?,lease_until=NULL,retry_at=?,retry_remaining=retry_remaining-1,last_error=?
		WHERE kind=? AND job_key=? AND agent_id=? AND status=? AND ownership_token=?`,
		MySQL: `UPDATE fb_memory_jobs SET status=?,finished_at=?,lease_until=NULL,retry_at=?,retry_remaining=retry_remaining-1,last_error=?
		WHERE kind=? AND job_key=? AND agent_id=? AND status=? AND ownership_token=?`,
		Postgres: `UPDATE fb_memory_jobs SET status=$1,finished_at=$2,lease_until=NULL,retry_at=$3,retry_remaining=retry_remaining-1,last_error=$4
		WHERE kind=$5 AND job_key=$6 AND agent_id=$7 AND status=$8 AND ownership_token=$9`,
	}
	// The claim itself is one statement on SQLite and PostgreSQL, whose upsert
	// can name both its conflict target and the conditions the update needs.
	// MySQL has neither, so it runs the guarded UPDATE and the guarded INSERT
	// below instead (memJobClaimMySQL) and keeps no arm here.
	memJobClaimSQL = dbx.SQL{
		SQLite: `INSERT INTO fb_memory_jobs(
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
		Postgres: `INSERT INTO fb_memory_jobs(
			kind,job_key,agent_id,status,ownership_token,started_at,finished_at,lease_until,retry_at,
			retry_remaining,last_error,input_watermark,last_success_watermark)
		SELECT $1,$2,$3,$4, $5,$6,NULL,$7,NULL,$8,NULL,$9,NULL
		WHERE (SELECT COUNT(*) FROM fb_memory_jobs WHERE agent_id=$10 AND kind=$11 AND status=$12 AND lease_until IS NOT NULL AND lease_until>$13)<$14
		ON CONFLICT(kind,job_key) DO UPDATE SET
			status=excluded.status,ownership_token=excluded.ownership_token,
			started_at=excluded.started_at,finished_at=NULL,lease_until=excluded.lease_until,retry_at=NULL,
			retry_remaining=CASE WHEN excluded.input_watermark>COALESCE(fb_memory_jobs.input_watermark,-1) THEN $15 ELSE fb_memory_jobs.retry_remaining END,
			last_error=NULL,input_watermark=excluded.input_watermark
		WHERE fb_memory_jobs.agent_id=excluded.agent_id
		  AND (fb_memory_jobs.status<>$16 OR fb_memory_jobs.lease_until IS NULL OR fb_memory_jobs.lease_until<=excluded.started_at)
		  AND (fb_memory_jobs.retry_at IS NULL OR fb_memory_jobs.retry_at<=excluded.started_at OR excluded.input_watermark>COALESCE(fb_memory_jobs.input_watermark,-1))
		  AND (fb_memory_jobs.retry_remaining>0 OR excluded.input_watermark>COALESCE(fb_memory_jobs.input_watermark,-1))
		  AND (SELECT COUNT(*) FROM fb_memory_jobs AS running_jobs
		       WHERE running_jobs.agent_id=excluded.agent_id AND running_jobs.kind=excluded.kind AND running_jobs.status=$17
		         AND running_jobs.lease_until IS NOT NULL AND running_jobs.lease_until>excluded.started_at
		         AND running_jobs.job_key<>excluded.job_key)<$18`,
	}
)

// The three statements only MySQL's claim runs, in place of the one upsert.
// They carry no SQLite text because no SQLite statement says this: the other
// engines express it as a single upsert.
//
// The count of the other running claims is read through a derived table:
// MySQL refuses a statement that reads the table it is writing, and
// materialising the rows first is how it is spelled around that.
const (
	memJobExistsMySQL      = `SELECT COUNT(*) FROM fb_memory_jobs WHERE kind=? AND job_key=?`
	memJobClaimInsertMySQL = `INSERT INTO fb_memory_jobs(
	kind,job_key,agent_id,status,ownership_token,started_at,finished_at,lease_until,retry_at,
	retry_remaining,last_error,input_watermark,last_success_watermark)
SELECT ?,?,?,?, ?,?,NULL,?,NULL,?,NULL,?,NULL
WHERE (SELECT COUNT(*) FROM (SELECT agent_id, kind, status, lease_until FROM fb_memory_jobs) AS running_jobs
       WHERE running_jobs.agent_id=? AND running_jobs.kind=? AND running_jobs.status=?
         AND running_jobs.lease_until IS NOT NULL AND running_jobs.lease_until>?)<?`
	// The assignments run left to right, so retry_remaining reads
	// input_watermark's old value before the last line replaces it — the same
	// pair SQLite's excluded.input_watermark and fb_memory_jobs.input_watermark
	// name explicitly.
	memJobClaimUpdateMySQL = `UPDATE fb_memory_jobs SET
	status=?,
	ownership_token=?,
	started_at=?,
	finished_at=NULL,
	lease_until=?,
	retry_at=NULL,
	retry_remaining=IF(? > COALESCE(input_watermark,-1), ?, retry_remaining),
	last_error=NULL,
	input_watermark=?
WHERE kind=? AND job_key=? AND agent_id=?
  AND (status<>? OR lease_until IS NULL OR lease_until<=?)
  AND (retry_at IS NULL OR retry_at<=? OR ?>COALESCE(input_watermark,-1))
  AND (retry_remaining>0 OR ?>COALESCE(input_watermark,-1))
  AND (SELECT COUNT(*) FROM (SELECT agent_id, kind, status, lease_until, job_key FROM fb_memory_jobs) AS running_jobs
       WHERE running_jobs.agent_id=? AND running_jobs.kind=? AND running_jobs.status=?
         AND running_jobs.lease_until IS NOT NULL AND running_jobs.lease_until>?
         AND running_jobs.job_key<>?)<?`
)

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
// A fire's conversation is never a candidate: each one is a fresh, report-
// shaped transcript, so extracting it would pay a stage-1 model call to
// relearn what the last fire already wrote. A subagent's private conversation
// is excluded for the same reason — its work already stands in the
// conversation that dispatched it.
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
	rows, err := s.DB.QueryContext(ctx, memStage1CandidatesSQL.For(dbx.DialectOf(s.DB)), agentID, ThreadMemoryEnabled, strings.TrimSpace(currentThreadID),
		SessionSourceTUI, SessionSourceWebchat,
		now.Add(-time.Duration(maxAgeDays)*24*time.Hour).Unix(),
		now.Add(-time.Duration(minIdleHours)*time.Hour).Unix(), sessionSourceCron, sessionSourceSubagent, stage1ThreadScanLimit)
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
	err = s.withImmediate(ctx, func(tx *dbx.Tx) error {
		var source int64
		queryErr := tx.QueryRowContext(ctx, memStage1SourceWatermarkSQL.For(tx.Dialect()), threadID, agentID).Scan(&source)
		if queryErr == nil && source >= sourceUpdatedAt {
			outcome = Stage1UpToDate
			return nil
		}
		if queryErr != nil && !errors.Is(queryErr, sql.ErrNoRows) {
			return queryErr
		}
		var success sql.NullInt64
		queryErr = tx.QueryRowContext(ctx, memJobSuccessWatermarkSQL.For(tx.Dialect()), JobKindStage1, threadID, agentID).Scan(&success)
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
		claimUntil := now + max64(int64(leaseSeconds), 0)
		var result sql.Result
		var execErr error
		if tx.Dialect() == dbx.MySQL {
			result, execErr = claimStage1JobMySQL(ctx, tx, threadID, agentID, token, now, claimUntil, sourceUpdatedAt, maxRunning)
		} else {
			result, execErr = tx.ExecContext(ctx, memJobClaimSQL.For(tx.Dialect()),
				JobKindStage1, threadID, agentID, jobStatusRunning, token, now, claimUntil,
				defaultRetryRemaining, sourceUpdatedAt, agentID, JobKindStage1, jobStatusRunning, now, maxRunning,
				defaultRetryRemaining, jobStatusRunning, jobStatusRunning, maxRunning)
		}
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
		queryErr = tx.QueryRowContext(ctx, memJobStateSQL.For(tx.Dialect()), JobKindStage1, threadID, agentID).Scan(&status, &leaseUntil, &retryAt, &retryRemaining)
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

// claimStage1JobMySQL takes the one claim statement's place on MySQL: its
// upsert can neither name a conflict target nor carry the guards, so the row's
// presence decides between the guarded update and the guarded insert. Both run
// inside the caller's transaction, which already holds the global write lock,
// so the read between them cannot see another writer's claim.
func claimStage1JobMySQL(ctx context.Context, tx *dbx.Tx, threadID, agentID, token string, now, leaseUntil, inputWatermark int64, maxRunning int) (sql.Result, error) {
	var existing int
	if err := tx.QueryRowContext(ctx, memJobExistsMySQL, JobKindStage1, threadID).Scan(&existing); err != nil {
		return nil, err
	}
	if existing == 0 {
		return tx.ExecContext(ctx, memJobClaimInsertMySQL,
			JobKindStage1, threadID, agentID, jobStatusRunning, token, now, leaseUntil,
			defaultRetryRemaining, inputWatermark,
			agentID, JobKindStage1, jobStatusRunning, now, maxRunning)
	}
	return tx.ExecContext(ctx, memJobClaimUpdateMySQL,
		jobStatusRunning, token, now, leaseUntil,
		inputWatermark, defaultRetryRemaining,
		inputWatermark,
		JobKindStage1, threadID, agentID,
		jobStatusRunning, now,
		now, inputWatermark,
		inputWatermark,
		agentID, JobKindStage1, jobStatusRunning, now, threadID, maxRunning)
}

func (s *Store) MarkStage1Succeeded(ctx context.Context, threadID, projectKey, ownershipToken string, sourceUpdatedAt int64, rawMemory, rolloutSummary string, rolloutSlug *string, lastMessageRowID int64) (bool, error) {
	if s == nil || s.DB == nil {
		return false, nil
	}
	agentID, err := s.agent()
	if err != nil {
		return false, err
	}
	owned := false
	err = s.withImmediate(ctx, func(tx *dbx.Tx) error {
		var err error
		owned, err = markStage1SucceededOn(ctx, tx, agentID, threadID, projectKey, ownershipToken, sourceUpdatedAt, rawMemory, rolloutSummary, rolloutSlug, lastMessageRowID)
		return err
	})
	return owned, err
}

// markStage1SucceededOn records one thread's extracted memory and enqueues
// that thread's project scope for its next phase-2 pass. projectKey must be
// the project scope the thread's cwd resolved to at claim time — every row in
// fb_memory_stage1_outputs is required to carry one (see the schema comment),
// so a caller that has none has nothing valid to write. lastMessageRowID is the
// transcript cursor the extraction ended on: it replaces the row's previous
// one, so the next pass starts after exactly the messages this one consumed.
func markStage1SucceededOn(ctx context.Context, tx *dbx.Tx, agentID, threadID, projectKey, ownershipToken string, sourceUpdatedAt int64, rawMemory, rolloutSummary string, rolloutSlug *string, lastMessageRowID int64) (bool, error) {
	projectKey = strings.TrimSpace(projectKey)
	if projectKey == "" {
		return false, fmt.Errorf("project_key is required")
	}
	now := time.Now().Unix()
	result, err := tx.ExecContext(ctx, memJobCloseStage1SQL.For(tx.Dialect()), jobStatusDone, now, JobKindStage1, strings.TrimSpace(threadID), agentID, jobStatusRunning, ownershipToken)
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
	var cursor any
	if lastMessageRowID > 0 {
		cursor = lastMessageRowID
	}
	if _, err := tx.ExecContext(ctx, memStage1SuccessUpsertSQL.For(tx.Dialect()), strings.TrimSpace(threadID), agentID, projectKey, sourceUpdatedAt, rawMemory, rolloutSummary, slug, now, cursor); err != nil {
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

func (s *Store) CompleteStage1WithEvidence(ctx context.Context, threadID, projectKey, ownershipToken string, sourceUpdatedAt int64, rawMemory, rolloutSummary string, rolloutSlug *string, lastMessageRowID int64, stagedPath, canonicalPath string) (bool, error) {
	if s == nil || s.DB == nil {
		_ = os.Remove(stagedPath)
		return false, nil
	}
	defer os.Remove(stagedPath)
	agentID, err := s.agent()
	if err != nil {
		return false, err
	}
	tx, err := dbx.BeginTx(ctx, s.DB, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	owned, err := markStage1SucceededOn(ctx, tx, agentID, threadID, projectKey, ownershipToken, sourceUpdatedAt, rawMemory, rolloutSummary, rolloutSlug, lastMessageRowID)
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
	err = s.withImmediate(ctx, func(tx *dbx.Tx) error {
		now := time.Now().Unix()
		result, err := tx.ExecContext(ctx, memJobCloseStage1SQL.For(tx.Dialect()), jobStatusDone, now, JobKindStage1, strings.TrimSpace(threadID), agentID, jobStatusRunning, ownershipToken)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil || affected == 0 {
			return err
		}
		owned = true
		var watermark int64
		if err := tx.QueryRowContext(ctx, memJobInputWatermarkSQL.For(tx.Dialect()), JobKindStage1, strings.TrimSpace(threadID), agentID, ownershipToken).Scan(&watermark); err != nil {
			return err
		}
		var projectKey string
		queryErr := tx.QueryRowContext(ctx, memStage1ProjectKeySQL.For(tx.Dialect()), strings.TrimSpace(threadID), agentID).Scan(&projectKey)
		if queryErr != nil && !errors.Is(queryErr, sql.ErrNoRows) {
			return queryErr
		}
		result, err = tx.ExecContext(ctx, memStage1DeleteSQL.For(tx.Dialect()), strings.TrimSpace(threadID), agentID)
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
	result, err := dbx.Exec(ctx, s.DB, memJobMarkFailedSQL.For(dbx.DialectOf(s.DB)), jobStatusError, now, now+stage1RetryDelay, reason, JobKindStage1, strings.TrimSpace(threadID), agentID, jobStatusRunning, ownershipToken)
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
	d := dbx.DialectOf(executor)
	query := memPhase2EnqueueSQL.For(d)
	args := []any{
		JobKindConsolidate, consolidateJobKey(agentID, scope), agentID, jobStatusPending, defaultRetryRemaining, inputWatermark,
		jobStatusRunning, jobStatusRunning, jobStatusPending, jobStatusRunning}
	if d != dbx.SQLite {
		// A caller outside a transaction hands the state database here; MySQL
		// and PostgreSQL take the global write lock through dbx.Exec.
		if sqlDB, ok := executor.(*sql.DB); ok {
			_, err := dbx.Exec(ctx, sqlDB, query, args...)
			return err
		}
	}
	_, err := executor.ExecContext(ctx, query, args...)
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
	err = s.withImmediate(ctx, func(tx *dbx.Tx) error {
		var status string
		var leaseUntil, retryAt, inputWatermark, finishedAt sql.NullInt64
		var lastError sql.NullString
		queryErr := tx.QueryRowContext(ctx, memJobDetailSQL.For(tx.Dialect()), JobKindConsolidate, jobKey, agentID).Scan(&status, &leaseUntil, &retryAt, &inputWatermark, &finishedAt, &lastError)
		if errors.Is(queryErr, sql.ErrNoRows) {
			_, insertErr := tx.ExecContext(ctx, memPhase2InsertSQL.For(tx.Dialect()), JobKindConsolidate, jobKey, agentID, jobStatusRunning, token, now, now+phase2LeaseSeconds, defaultRetryRemaining)
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
		result, updateErr := tx.ExecContext(ctx, memPhase2ClaimUpdateSQL.For(tx.Dialect()),
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
	result, err := dbx.Exec(ctx, s.DB, memPhase2HeartbeatSQL.For(dbx.DialectOf(s.DB)), time.Now().Unix()+phase2LeaseSeconds, JobKindConsolidate, consolidateJobKey(agentID, scope), agentID, jobStatusRunning, ownershipToken)
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
	err = s.withImmediate(ctx, func(tx *dbx.Tx) error {
		result, err := tx.ExecContext(ctx, memPhase2SucceedSQL.For(tx.Dialect()), jobStatusDone, time.Now().Unix(), watermark, JobKindConsolidate, consolidateJobKey(agentID, scope), agentID, jobStatusRunning, ownershipToken)
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
		if _, err := tx.ExecContext(ctx, memPhase2SelectClearSQL.For(tx.Dialect()), agentID, scope.Key); err != nil {
			return err
		}
		for _, output := range selected {
			if _, err := tx.ExecContext(ctx, memPhase2SelectMarkSQL.For(tx.Dialect()), output.SourceUpdatedAt, output.ThreadID, agentID, scope.Key, output.SourceUpdatedAt); err != nil {
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
	d := dbx.DialectOf(s.DB)
	token := dbx.Placeholder(d, 9)
	ownershipClause := `ownership_token=` + token
	if allowUnowned {
		ownershipClause = `(ownership_token=` + token + ` OR ownership_token IS NULL)`
	}
	now := time.Now().Unix()
	result, err := dbx.Exec(ctx, s.DB, memPhase2FailSQL.For(d)+ownershipClause,
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
