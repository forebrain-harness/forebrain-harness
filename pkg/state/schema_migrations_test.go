package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

// buildLegacyV0State creates a state file in the v0 shape — the schema.sql
// released binaries declared, plus the rows the fixture pins — and returns its
// path. It writes through a plain driver connection so nothing about the
// current Open runs against it.
func buildLegacyV0State(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "legacy_v0.sqlite")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, name := range []string{"legacy_v0_schema.sql", "legacy_v0_fixture.sql"} {
		script, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(script)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	return path
}

// sqliteMasterRows reads the schema objects a comparison must cover, in a
// stable order, with the SQL text that says how each was created.
func sqliteMasterRows(ctx context.Context, t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.QueryContext(ctx, `SELECT type, name, tbl_name, sql FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY type, name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var objects []string
	for rows.Next() {
		var typ, name, tbl, ddl string
		if err := rows.Scan(&typ, &name, &tbl, &ddl); err != nil {
			t.Fatal(err)
		}
		objects = append(objects, typ+" "+name+" "+tbl+" "+ddl)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return objects
}

func requireSchemaVersion(ctx context.Context, t *testing.T, db *sql.DB, want int) {
	t.Helper()
	var version int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != want {
		t.Fatalf("user_version = %d, want %d", version, want)
	}
}

func requireForeignKeysClean(ctx context.Context, t *testing.T, db *sql.DB) {
	t.Helper()
	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("PRAGMA foreign_key_check reported violations after migration")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

// TestStateMigrationMatchesFreshSchema pins the owner's rule that a migrated
// database and a freshly created one are the same database: the same schema
// objects, byte for byte, with the residual tables older binaries left behind
// gone. §4.5 checks 1 and 2.
func TestStateMigrationMatchesFreshSchema(t *testing.T) {
	ctx := context.Background()
	migrated, err := Open(ctx, buildLegacyV0State(t, t.TempDir()), nil)
	if err != nil {
		t.Fatalf("open a v0 state file: %v", err)
	}
	defer migrated.Close()

	fresh, err := Open(ctx, filepath.Join(t.TempDir(), "fresh.sqlite"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()

	requireSchemaVersion(ctx, t, migrated, stateSchemaVersion)
	requireForeignKeysClean(ctx, t, migrated)

	got := sqliteMasterRows(ctx, t, migrated)
	want := sqliteMasterRows(ctx, t, fresh)
	if len(got) != len(want) {
		t.Fatalf("migrated database holds %d objects, fresh one holds %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("schema object %d differs:\nmigrated: %s\nfresh:    %s", i, got[i], want[i])
		}
	}

	for _, residual := range []string{"fb_jobs", "fb_work_items", "fb_todos", "fb_intel_audit"} {
		var count int
		if err := migrated.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE name=?`, residual).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("residual table %s survived the migration", residual)
		}
	}

	// T4: the run-level window moved off the rows and onto the run they name;
	// a turn the eta rows describe (no run id) got a run created for it, and
	// its tool row was bound to that run; the `!cmd` row keeps its own
	// execution window in exec_*.
	var rs, rf, rw int64
	if err := migrated.QueryRowContext(ctx, `SELECT started_at_ms, finished_at_ms, worked_ms FROM fb_runs WHERE session_id='zeta' AND parent_run_id IS NULL AND worked_ms=120000`).Scan(&rs, &rf, &rw); err != nil {
		t.Fatalf("zeta run timing: %v", err)
	}
	if rs == 0 || rf == 0 || rw != 120000 {
		t.Fatalf("zeta run timing = %d/%d/%d, want the 120000ms window", rs, rf, rw)
	}
	var etaRunID string
	if err := migrated.QueryRowContext(ctx, `SELECT id FROM fb_runs WHERE session_id='eta' AND worked_ms=90000`).Scan(&etaRunID); err != nil {
		t.Fatalf("eta run timing: %v", err)
	}
	if !strings.HasPrefix(etaRunID, "legacy-run-eta-") {
		t.Fatalf("eta run id = %q, want the created legacy run", etaRunID)
	}
	var bound, execTool, execCmd int
	if err := migrated.QueryRowContext(ctx, `SELECT COUNT(*) FROM fb_messages WHERE session_id='eta' AND run_id=?`, etaRunID).Scan(&bound); err != nil {
		t.Fatal(err)
	}
	// The assistant rows bracket the turn; the tool row between them is part
	// of it. The `!cmd` row is its own thing — a command the user ran, with no
	// run — so it stays unbound.
	if bound != 3 {
		t.Fatalf("eta rows bound to the created run = %d, want the two assistant rows and the tool row between them", bound)
	}
	if err := migrated.QueryRowContext(ctx, `SELECT COUNT(*) FROM fb_messages WHERE message_id='m-eta-2' AND exec_started_at_ms IS NOT NULL AND exec_duration_ms=5000`).Scan(&execTool); err != nil {
		t.Fatal(err)
	}
	if execTool != 1 {
		t.Fatal("tool row lost its exec timing")
	}
	if err := migrated.QueryRowContext(ctx, `SELECT COUNT(*) FROM fb_messages WHERE message_id='m-eta-3' AND exec_duration_ms=2000`).Scan(&execCmd); err != nil {
		t.Fatal(err)
	}
	if execCmd != 1 {
		t.Fatal("!cmd user row lost its exec timing")
	}
	// An ordinary user message that carried the run's window keeps none of it:
	// the window is the run's, and a user row with exec timing reads as a
	// shell command.
	var plainUserExec sql.NullInt64
	if err := migrated.QueryRowContext(ctx, `SELECT exec_started_at_ms FROM fb_messages WHERE message_id='m-eta-5'`).Scan(&plainUserExec); err != nil {
		t.Fatal(err)
	}
	if plainUserExec.Valid {
		t.Fatal("an ordinary user message must not take the run's window as its own exec timing")
	}
	// A call that finished inside a millisecond keeps its window with a zero
	// duration instead of failing the triple's all-or-nothing CHECK.
	var fastStart, fastFinish, fastDuration sql.NullInt64
	if err := migrated.QueryRowContext(ctx, `SELECT exec_started_at_ms, exec_finished_at_ms, exec_duration_ms FROM fb_messages WHERE message_id='m-eta-6'`).Scan(&fastStart, &fastFinish, &fastDuration); err != nil {
		t.Fatal(err)
	}
	if !fastStart.Valid || fastStart.Int64 != 1790503230123 || fastFinish.Int64 != fastStart.Int64 || !fastDuration.Valid || fastDuration.Int64 != 0 {
		t.Fatalf("instant tool window = %v/%v/%v, want 1790503230123/1790503230123/0", fastStart, fastFinish, fastDuration)
	}
	// The assistant row of a timed run carries no exec timing of its own.
	var assistantExec sql.NullInt64
	if err := migrated.QueryRowContext(ctx, `SELECT exec_started_at_ms FROM fb_messages WHERE message_id='m-eta-1'`).Scan(&assistantExec); err != nil {
		t.Fatal(err)
	}
	if assistantExec.Valid {
		t.Fatal("assistant row must not carry row-level exec timing")
	}

	// T3: one wait per run (the long-run switch beats the newer shell wait),
	// its source blob split into typed columns; the orphan run's wait and the
	// unresolvable actions are gone; actions carry the session their payload
	// recorded or their wait's run resolved to.
	var waitAction, waitTool, waitOwner string
	var waitClaimed sql.NullInt64
	var elevation int
	if err := migrated.QueryRowContext(ctx, `SELECT action_id, tool_name, resume_owner, resume_claimed_at_ms, profile_elevation FROM fb_run_waits WHERE run_id='rz-top'`).Scan(&waitAction, &waitTool, &waitOwner, &waitClaimed, &elevation); err != nil {
		t.Fatal(err)
	}
	if waitAction != "a-z1" || waitTool != "long_run_async_switch" || waitOwner != "proc-1" || !waitClaimed.Valid || waitClaimed.Int64 != 1690000000123 || elevation != 1 {
		t.Fatalf("picked wait = (%s,%s,%s,%v,%d), want a-z1's typed columns", waitAction, waitTool, waitOwner, waitClaimed, elevation)
	}
	var subagentRun sql.NullString
	if err := migrated.QueryRowContext(ctx, `SELECT subagent_run_id FROM fb_run_waits WHERE run_id='rz-top'`).Scan(&subagentRun); err != nil {
		t.Fatal(err)
	}
	if !subagentRun.Valid || subagentRun.String != "rz-sub" {
		t.Fatalf("subagent_run_id = %v, want rz-sub", subagentRun)
	}
	var waitCount int
	if err := migrated.QueryRowContext(ctx, `SELECT COUNT(*) FROM fb_run_waits`).Scan(&waitCount); err != nil {
		t.Fatal(err)
	}
	if waitCount != 2 { // rz-top's pick + rz-sub's wait; the orphan run's wait dropped
		t.Fatalf("waits after migration = %d, want 2", waitCount)
	}
	for _, action := range []struct{ id, session string }{
		{"a-z1", "zeta"}, {"a-z2", "zeta"}, {"a-z3", "zeta"},
	} {
		var session string
		if err := migrated.QueryRowContext(ctx, `SELECT session_id FROM fb_actions WHERE id=?`, action.id).Scan(&session); err != nil {
			t.Fatalf("action %s: %v", action.id, err)
		}
		if session != action.session {
			t.Fatalf("action %s session = %q, want %q", action.id, session, action.session)
		}
	}
	for _, dropped := range []string{"a-lost", "a-ghost"} {
		var n int
		if err := migrated.QueryRowContext(ctx, `SELECT COUNT(*) FROM fb_actions WHERE id=?`, dropped).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("action %s survived but belongs to no conversation", dropped)
		}
	}
	var orphanRun int
	if err := migrated.QueryRowContext(ctx, `SELECT COUNT(*) FROM fb_runs WHERE id='rz-orphan'`).Scan(&orphanRun); err != nil {
		t.Fatal(err)
	}
	if orphanRun != 0 {
		t.Fatal("run of a session that does not exist survived")
	}

	// Rows the fixture pinned survive the rebuild byte for byte, with the
	// sentinel shapes normalized: a NULL/blank title names the session by its
	// id, and the legacy 'tui' source never had a reader, so its rows are
	// dropped rather than carried as unreachable ones.
	var alphaTitle string
	if err := migrated.QueryRowContext(ctx, `SELECT title FROM fb_sessions WHERE id='alpha'`).Scan(&alphaTitle); err != nil {
		t.Fatal(err)
	}
	if alphaTitle != "alpha" {
		t.Fatalf("alpha title = %q, want the id the NULL v0 title normalizes to", alphaTitle)
	}
	var tuiRows int
	if err := migrated.QueryRowContext(ctx, `SELECT COUNT(*) FROM fb_messages WHERE content='beta tui echo' OR content='delta tui row'`).Scan(&tuiRows); err != nil {
		t.Fatal(err)
	}
	if tuiRows != 0 {
		t.Fatalf("legacy 'tui' rows after migration = %d, want them dropped (no reader ever saw them)", tuiRows)
	}
	var maxID int
	if err := migrated.QueryRowContext(ctx, `SELECT MAX(id) FROM fb_messages`).Scan(&maxID); err != nil {
		t.Fatal(err)
	}
	// The fixture's last message id is 18 (T2 added ids 9-11, T3 added 12-13,
	// T4 added 14-18); the dropped tui/out-of-enum/orphan rows do not shift
	// the survivors, which keep their ids verbatim.
	if maxID != 18 {
		t.Fatalf("max message id after migration = %d, want the ids the v0 rows carried", maxID)
	}

	// T5: the run-step ledger is folded into the one event log. The events keep
	// the order their cursor gave them, and the migrated steps land where the
	// conversation drew them: seq 1 before the turn's start, seq 2 between the
	// start and the child's delta, the main plan update after the child's
	// delta. Everything else the ledger held is gone — a step an event already
	// stood for, and every step kind that has a canonical event or no reader.
	type storedEvent struct{ id, runID, typ string }
	var merged []storedEvent
	eventRows, err := migrated.QueryContext(ctx, `
SELECT event_id, IFNULL(run_id,''), event_type FROM fb_session_events ORDER BY sequence`)
	if err != nil {
		t.Fatal(err)
	}
	for eventRows.Next() {
		var evt storedEvent
		if err := eventRows.Scan(&evt.id, &evt.runID, &evt.typ); err != nil {
			eventRows.Close()
			t.Fatal(err)
		}
		merged = append(merged, evt)
	}
	eventRows.Close()
	if err := eventRows.Err(); err != nil {
		t.Fatal(err)
	}
	wantOrder := []storedEvent{
		{"evt-rz-top-1", "rz-top", event.RunEventToolStarted},
		{"evt-turn-start", "rz-top", event.RunEventTurnStarted},
		{"evt-rz-top-2", "rz-top", event.RunEventToolCompleted},
		{"evt-child-delta", "rz-sub", event.RunEventAssistantDelta},
		{"evt-twin", "rz-top", event.RunEventToolCompleted},
		{"evt-rz-top-7", "rz-top", event.RunEventPlanUpdated},
		{"evt-main-done", "rz-top", event.RunEventTurnCompleted},
		{"evt-norun", "", event.RunEventAssistantDelta},
	}
	if len(merged) != len(wantOrder) {
		t.Fatalf("merged events = %+v, want %d rows", merged, len(wantOrder))
	}
	for i, want := range wantOrder {
		if merged[i] != want {
			t.Fatalf("merged event %d = %+v, want %+v (all: %+v)", i, merged[i], want, merged)
		}
	}

	// The migrated tool steps carry the canonical payload shape, not the
	// ledger's, and the plan update is the plan itself rather than the step
	// envelope that held it.
	var stepID, toolName, filePath string
	if err := migrated.QueryRowContext(ctx, `
SELECT payload_json->>'$.step_id', payload_json->>'$.tool_name', payload_json->>'$.tool_meta.input.file_path'
FROM fb_session_events WHERE event_id='evt-rz-top-1'`).Scan(&stepID, &toolName, &filePath); err != nil {
		t.Fatal(err)
	}
	if stepID != "call-migrate" || toolName != "read_file" || filePath != "/repo/a.go" {
		t.Fatalf("migrated started payload = (%q,%q,%q), want the canonical tool shape", stepID, toolName, filePath)
	}
	var duration float64
	var body string
	if err := migrated.QueryRowContext(ctx, `
SELECT payload_json->>'$.duration_seconds', payload_json->>'$.output.content'
FROM fb_session_events WHERE event_id='evt-rz-top-2'`).Scan(&duration, &body); err != nil {
		t.Fatal(err)
	}
	if duration != 2.5 || body != "body" {
		t.Fatalf("migrated completed payload = (%v,%q), want the 2.5s window and its output", duration, body)
	}
	var planTitle string
	var nested sql.NullString
	if err := migrated.QueryRowContext(ctx, `
SELECT payload_json->>'$.title', payload_json->'$.plan_update'
FROM fb_session_events WHERE event_id='evt-rz-top-7'`).Scan(&planTitle, &nested); err != nil {
		t.Fatal(err)
	}
	if planTitle != "Main plan" || nested.Valid {
		t.Fatalf("migrated plan update = (%q,%v), want the plan itself with no step envelope", planTitle, nested)
	}

	// T6: a file's two location columns flatten into one storage_key — the
	// local one from storage_relpath, the s3 one from oss_key, with its bucket
	// and parse error carried to their current names. The workspace_file row
	// (a shape no writer produced) and the row whose session never existed are
	// gone.
	var localKey, localBucket, localParsed, localErr string
	if err := migrated.QueryRowContext(ctx, `
SELECT storage_key, storage_bucket, parsed_text_path, parse_error FROM fb_files WHERE id='f-local'`).
		Scan(&localKey, &localBucket, &localParsed, &localErr); err != nil {
		t.Fatal(err)
	}
	if localKey != "ab/f-local.txt" || localBucket != "" || localParsed != "f-local.txt" || localErr != "" {
		t.Fatalf("local file = (%q,%q,%q,%q), want storage_key from storage_relpath", localKey, localBucket, localParsed, localErr)
	}
	var s3Key, s3Bucket, s3Err string
	if err := migrated.QueryRowContext(ctx, `
SELECT storage_key, storage_bucket, parse_error FROM fb_files WHERE id='f-s3'`).
		Scan(&s3Key, &s3Bucket, &s3Err); err != nil {
		t.Fatal(err)
	}
	if s3Key != "ab/f-s3.pdf" || s3Bucket != "bucket-1" || s3Err != "parse boom" {
		t.Fatalf("s3 file = (%q,%q,%q), want storage_key from oss_key and its bucket", s3Key, s3Bucket, s3Err)
	}
	var fileCount int
	if err := migrated.QueryRowContext(ctx, `SELECT COUNT(*) FROM fb_files`).Scan(&fileCount); err != nil {
		t.Fatal(err)
	}
	if fileCount != 2 {
		t.Fatalf("files after migration = %d, want the two attachment rows", fileCount)
	}

	// T7: a job keeps its project link only when the project survived, and a
	// last run of 0 becomes NULL; a fire's tenant falls back to its job's and a
	// fire resolvable neither way is dropped; a paused heartbeat is a NULL next
	// fire, and a beat of a missing session is gone.
	var liveProject sql.NullString
	var liveLastRun, danglingLastRun sql.NullInt64
	if err := migrated.QueryRowContext(ctx, `SELECT project_id, last_run_at FROM fb_cron_jobs WHERE id='job-live'`).Scan(&liveProject, &liveLastRun); err != nil {
		t.Fatal(err)
	}
	if !liveProject.Valid || liveProject.String != "prj-live" || !liveLastRun.Valid || liveLastRun.Int64 != 7100 {
		t.Fatalf("live job = (%v,%v), want its project and last run", liveProject, liveLastRun)
	}
	var danglingProject sql.NullString
	if err := migrated.QueryRowContext(ctx, `SELECT project_id, last_run_at FROM fb_cron_jobs WHERE id='job-dangling'`).Scan(&danglingProject, &danglingLastRun); err != nil {
		t.Fatal(err)
	}
	if danglingProject.Valid || danglingLastRun.Valid {
		t.Fatalf("dangling job = (%v,%v), want a NULL project and no last run", danglingProject, danglingLastRun)
	}
	var runCount int
	if err := migrated.QueryRowContext(ctx, `SELECT COUNT(*) FROM fb_cron_runs`).Scan(&runCount); err != nil {
		t.Fatal(err)
	}
	if runCount != 2 {
		t.Fatalf("cron runs after migration = %d, want the resolvable pair", runCount)
	}
	var fallbackAgent string
	if err := migrated.QueryRowContext(ctx, `SELECT agent_id FROM fb_cron_runs WHERE session_id='cron-s2'`).Scan(&fallbackAgent); err != nil {
		t.Fatal(err)
	}
	if fallbackAgent != "main" {
		t.Fatalf("fire agent = %q, want its job's tenant", fallbackAgent)
	}
	var runningFinished sql.NullInt64
	if err := migrated.QueryRowContext(ctx, `SELECT finished_at FROM fb_cron_runs WHERE session_id='cron-s3'`).Scan(&runningFinished); err != sql.ErrNoRows {
		t.Fatalf("a fire with no resolvable tenant survived: %v", err)
	}
	var beatCount int
	if err := migrated.QueryRowContext(ctx, `SELECT COUNT(*) FROM fb_heartbeats`).Scan(&beatCount); err != nil {
		t.Fatal(err)
	}
	if beatCount != 2 {
		t.Fatalf("heartbeats after migration = %d, want the two live sessions", beatCount)
	}
	var activeNext, pausedNext sql.NullInt64
	if err := migrated.QueryRowContext(ctx, `SELECT next_run_at FROM fb_heartbeats WHERE session_id='alpha'`).Scan(&activeNext); err != nil {
		t.Fatal(err)
	}
	if err := migrated.QueryRowContext(ctx, `SELECT next_run_at FROM fb_heartbeats WHERE session_id='beta'`).Scan(&pausedNext); err != nil {
		t.Fatal(err)
	}
	if !activeNext.Valid || activeNext.Int64 != 7400 || pausedNext.Valid {
		t.Fatalf("heartbeat schedule = (active=%v, paused=%v), want the paused one NULL", activeNext, pausedNext)
	}

	// T8: a stage-one output survives only when its thread and tenant both
	// match a live session; a NULL usage_count becomes 0 and a non-zero
	// phase-2 flag collapses to 1. The search index is derived, so the
	// migration clears it and the next search rebuilds it.
	var stage1Count int
	if err := migrated.QueryRowContext(ctx, `SELECT COUNT(*) FROM fb_memory_stage1_outputs`).Scan(&stage1Count); err != nil {
		t.Fatal(err)
	}
	if stage1Count != 2 {
		t.Fatalf("stage-one rows after migration = %d, want the two matching threads", stage1Count)
	}
	var alphaUsage, alphaSelected int
	if err := migrated.QueryRowContext(ctx, `SELECT usage_count, selected_for_phase2 FROM fb_memory_stage1_outputs WHERE thread_id='alpha'`).Scan(&alphaUsage, &alphaSelected); err != nil {
		t.Fatal(err)
	}
	if alphaUsage != 0 || alphaSelected != 1 {
		t.Fatalf("alpha stage-one = (usage=%d, selected=%d), want 0 and 1", alphaUsage, alphaSelected)
	}
	var jobCount, indexCount, ftsCount int
	for _, q := range []struct {
		sql string
		out *int
	}{
		{"SELECT COUNT(*) FROM fb_memory_jobs", &jobCount},
		{"SELECT COUNT(*) FROM fb_memory_index_files", &indexCount},
		{"SELECT COUNT(*) FROM fb_memory_fts", &ftsCount},
	} {
		if err := migrated.QueryRowContext(ctx, q.sql).Scan(q.out); err != nil {
			t.Fatal(err)
		}
	}
	if jobCount != 2 {
		t.Fatalf("memory jobs after migration = %d, want the two ledger rows", jobCount)
	}
	if indexCount != 0 || ftsCount != 0 {
		t.Fatalf("search index after migration = (files=%d, fts=%d), want it cleared", indexCount, ftsCount)
	}
}

// TestStateOpenIsIdempotentAtCurrentVersion pins §4.5 check 5: opening a
// database already at the current version runs no DDL at all.
func TestStateOpenIsIdempotentAtCurrentVersion(t *testing.T) {
	ctx := context.Background()
	path := buildLegacyV0State(t, t.TempDir())
	migrated, err := Open(ctx, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	before := sqliteMasterRows(ctx, t, migrated)
	if err := migrated.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(ctx, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	after := sqliteMasterRows(ctx, t, reopened)
	requireSchemaVersion(ctx, t, reopened, stateSchemaVersion)
	if len(before) != len(after) {
		t.Fatalf("reopen changed the schema: %d objects before, %d after", len(before), len(after))
	}
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("reopen changed schema object %d:\nbefore: %s\nafter:  %s", i, before[i], after[i])
		}
	}
}

// TestStateMigrationRepairsPrematureV1Version reproduces databases opened by
// an intermediate migration build: user_version said v1 while the tables were
// still in the legacy shape. Open must rebuild that schema instead of trusting
// the stale version and later failing on compact_boundary_message_id.
func TestStateMigrationRepairsPrematureV1Version(t *testing.T) {
	ctx := context.Background()
	path := buildLegacyV0StateWith(t, `
INSERT INTO fb_sessions(id, agent_id, title, updated_at, created_at, compact_boundary_id)
VALUES('s','main','session',10,10,'');
INSERT INTO fb_messages(session_id, role, content, created_at)
VALUES('s','user','before upgrade',10);`)
	raw, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`PRAGMA user_version = 1`); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := Open(ctx, path, nil)
	if err != nil {
		t.Fatalf("open prematurely versioned state file: %v", err)
	}
	defer db.Close()
	requireSchemaVersion(ctx, t, db, stateSchemaVersion)

	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	columns, err := connTableColumnNames(ctx, conn, "fb_sessions")
	if closeErr := conn.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	if !columns["compact_boundary_message_id"] || columns["compact_boundary_id"] {
		t.Fatalf("session columns after repair = %v", columns)
	}
	var content string
	if err := db.QueryRowContext(ctx, `SELECT content FROM fb_messages WHERE session_id='s'`).Scan(&content); err != nil {
		t.Fatal(err)
	}
	if content != "before upgrade" {
		t.Fatalf("message after repair = %q", content)
	}
	store := NewSessionStore(db, "main")
	boundaryID, err := store.compactBoundaryRowID(ctx, db, "s")
	if err != nil {
		t.Fatalf("read compact boundary after repair: %v", err)
	}
	if boundaryID != 0 {
		t.Fatalf("compact boundary after repair = %d, want none", boundaryID)
	}
	if _, err := store.Append(ctx, "s", "user", "after upgrade"); err != nil {
		t.Fatalf("append after repair: %v", err)
	}
}

// buildLegacyV0StateWith creates a v0 state file holding only the rows script
// inserts, then applies alter, so a test can pin one migration rule — or a
// still older shape — without the whole fixture.
func buildLegacyV0StateWith(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "legacy_v0.sqlite")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	schema, err := os.ReadFile(filepath.Join("testdata", "legacy_v0_schema.sql"))
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{string(schema), script} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("build legacy state: %v", err)
		}
	}
	return path
}

// TestMigrationLeavesThePoolAtTheOrdinaryBusyTimeout pins that the long lock
// wait a migration takes for itself does not leak: the connection it used goes
// back to the pool, and the next ordinary statement must fail after the DSN's
// five seconds, not wait ten minutes behind another writer.
func TestMigrationLeavesThePoolAtTheOrdinaryBusyTimeout(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, buildLegacyV0State(t, t.TempDir()), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var timeout int
	if err := db.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&timeout); err != nil {
		t.Fatal(err)
	}
	if timeout != stateBusyTimeoutMs {
		t.Fatalf("busy_timeout after migration = %d, want %d", timeout, stateBusyTimeoutMs)
	}
}

// TestStateMigrationKeepsTheMessageIDHighWater pins AUTOINCREMENT across the
// rebuild: an id the v0 table already handed out — to a row deleted since — is
// never issued again.
func TestStateMigrationKeepsTheMessageIDHighWater(t *testing.T) {
	ctx := context.Background()
	path := buildLegacyV0StateWith(t, `
INSERT INTO fb_sessions(id, agent_id, title, updated_at, created_at) VALUES('s','main','s',10,10);
INSERT INTO fb_messages(session_id, role, content, created_at) VALUES('s','user','kept',10),('s','user','deleted later',11);
DELETE FROM fb_messages WHERE content='deleted later';
INSERT INTO fb_cron_jobs(id, agent_id, schedule, created_at, updated_at) VALUES('job','main','every 1h',10,10);
INSERT INTO fb_cron_runs(job_id, agent_id, started_at) VALUES('job','main',10),('job','main',11);
DELETE FROM fb_cron_runs WHERE started_at=11;`)
	db, err := Open(ctx, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	id, err := NewSessionStore(db, "main").Append(ctx, "s", "user", "after the upgrade")
	if err != nil {
		t.Fatal(err)
	}
	if id != 3 {
		t.Fatalf("first message id after migration = %d, want 3 (id 2 was issued before)", id)
	}
	runID, err := (&CronStore{DB: db}).StartRun(ctx, CronRun{JobID: "job", AgentID: "main", Trigger: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if runID != 3 {
		t.Fatalf("first cron run id after migration = %d, want 3 (id 2 was issued before)", runID)
	}
}

// TestStateMigrationDropsRunsWhoseParentIsGone pins that a child run hanging
// off a parent the v0 file no longer holds goes with it — subtree and all —
// the way the parent link's ON DELETE CASCADE would have taken it, instead of
// failing the foreign-key check and leaving the database unopenable.
func TestStateMigrationDropsRunsWhoseParentIsGone(t *testing.T) {
	ctx := context.Background()
	path := buildLegacyV0StateWith(t, `
INSERT INTO fb_sessions(id, agent_id, title, updated_at, created_at) VALUES('s','main','s',10,10);
INSERT INTO fb_runs(id, session_id, input_text, status, parent_run_id, created_at, updated_at) VALUES
 ('top','s','hi','done','',10,10),
 ('child','s','sub','done','top',11,11),
 ('stray','s','sub','done','gone',12,12),
 ('stray-grandchild','s','sub','done','stray',13,13);
INSERT INTO fb_messages(session_id, run_id, role, content, created_at) VALUES('s','stray','assistant','stray answer',12);
INSERT INTO fb_session_events(session_id, event_id, run_id, event_type, payload_json, occurred_at_ms) VALUES('s','evt-stray','stray-grandchild','assistant_delta','{}',13000);`)
	db, err := Open(ctx, path, nil)
	if err != nil {
		t.Fatalf("open a v0 file with a dangling parent run: %v", err)
	}
	defer db.Close()
	requireForeignKeysClean(ctx, t, db)
	rows, err := db.QueryContext(ctx, `SELECT id FROM fb_runs ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if strings.Join(ids, ",") != "child,top" {
		t.Fatalf("runs after migration = %v, want the orphaned subtree gone", ids)
	}
	var runID sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT run_id FROM fb_messages WHERE content='stray answer'`).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	if runID.Valid {
		t.Fatalf("message of a dropped run still names it: %v", runID)
	}
}

// TestStateMigrationToleratesMessagesOlderThanRunTiming pins the "any older
// shape" promise for the timing move: a file whose fb_messages predates the
// run id and the per-row timing columns has no timing to move, and migrates.
func TestStateMigrationToleratesMessagesOlderThanRunTiming(t *testing.T) {
	ctx := context.Background()
	path := buildLegacyV0StateWith(t, `
DROP INDEX idx_fb_messages_session_run;
ALTER TABLE fb_messages DROP COLUMN run_id;
ALTER TABLE fb_messages DROP COLUMN run_started_at;
ALTER TABLE fb_messages DROP COLUMN run_finished_at;
ALTER TABLE fb_messages DROP COLUMN worked_duration_ms;
INSERT INTO fb_sessions(id, agent_id, title, updated_at, created_at) VALUES('s','main','s',10,10);
INSERT INTO fb_messages(session_id, role, content, created_at) VALUES('s','user','hello',10),('s','assistant','hi',11);`)
	db, err := Open(ctx, path, nil)
	if err != nil {
		t.Fatalf("open a v0 file older than run timing: %v", err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM fb_messages WHERE session_id='s' AND run_id IS NULL AND exec_started_at_ms IS NULL`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("untimed rows after migration = %d, want both", n)
	}
}

// TestStateMigrationDedupesStepsWithinTheirOwnConversation pins that a step is
// recognised as already recorded only by an event of its own conversation: a
// provider's call id is unique within a conversation, and another
// conversation that reused it must not swallow this one's tool call.
func TestStateMigrationDedupesStepsWithinTheirOwnConversation(t *testing.T) {
	ctx := context.Background()
	path := buildLegacyV0StateWith(t, `
INSERT INTO fb_sessions(id, agent_id, title, updated_at, created_at) VALUES('a','main','a',10,10),('b','main','b',10,10);
INSERT INTO fb_runs(id, session_id, input_text, status, created_at, updated_at) VALUES('run-a','a','hi','done',10,10),('run-b','b','hi','done',10,10);
INSERT INTO fb_session_events(session_id, event_id, run_id, event_type, payload_json, occurred_at_ms) VALUES
 ('b','evt-b','run-b','tool_call_completed','{"kind":"tool_call_completed","step_id":"call_0","tool_name":"shell"}',10000);
INSERT INTO fb_run_steps(run_id, seq, event_type, payload_json, created_at) VALUES
 ('run-a',1,'tool_call_completed','{"kind":"tool_call_completed","step_id":"call_0","tool_name":"read_file","output":{}}',11),
 ('run-b',1,'tool_call_completed','{"kind":"tool_call_completed","step_id":"call_0","tool_name":"shell","output":{}}',11);`)
	db, err := Open(ctx, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	counts := map[string]int{}
	rows, err := db.QueryContext(ctx, `SELECT session_id, COUNT(*) FROM fb_session_events WHERE event_type='tool_call_completed' GROUP BY session_id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var sid string
		var n int
		if err := rows.Scan(&sid, &n); err != nil {
			t.Fatal(err)
		}
		counts[sid] = n
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if counts["a"] != 1 || counts["b"] != 1 {
		t.Fatalf("tool completions per conversation = %v, want a's step migrated and b's twin kept once", counts)
	}
}

// TestOpenRejectsStateDatabaseFromANewerBinary pins the guard on the other
// side: a file a newer binary wrote is an error, not a downgrade.
func TestOpenRejectsStateDatabaseFromANewerBinary(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "newer.sqlite")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version = ` + "999"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = Open(ctx, path, nil)
	if !errors.Is(err, ErrStateSchemaNewer) {
		t.Fatalf("open error = %v, want ErrStateSchemaNewer", err)
	}
}

// TestReadOnlyOpenRejectsOutdatedSchema pins the read-only gate: without the
// write lock a migration needs, a v0 database is refused rather than read
// partially.
func TestReadOnlyOpenRejectsOutdatedSchema(t *testing.T) {
	ctx := context.Background()
	path := buildLegacyV0State(t, t.TempDir())
	_, err := Open(ctx, path, &OpenOptions{ReadOnly: true})
	if !errors.Is(err, ErrStateSchemaOutdated) {
		t.Fatalf("read-only open error = %v, want ErrStateSchemaOutdated", err)
	}
}

// TestConcurrentSequenceAppendsDoNotDuplicateRows pins R2/D6: two writers
// reconciling the same run's message list against the same session must not
// both append the computed suffix. The read-plan-write reconciliation runs in
// one IMMEDIATE transaction, so the second writer plans against what the first
// committed and appends nothing.
func TestConcurrentSequenceAppendsDoNotDuplicateRows(t *testing.T) {
	ctx := context.Background()
	db, err := OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewSessionStore(db, "main")
	if _, err := store.Append(ctx, "s", "user", "seed question"); err != nil {
		t.Fatal(err)
	}
	run, err := (&RunStore{DB: db}).CreateRun(ctx, "s", "seed question")
	if err != nil {
		t.Fatal(err)
	}
	sequence := []llm.Message{
		llm.UserMessage(llm.Text("seed question")),
		llm.AssistantMessage([]llm.ContentPart{llm.Text("answer")}),
		llm.AssistantMessage([]llm.ContentPart{llm.Text("more")}),
	}

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := store.AppendMessageSequenceForRun(ctx, "s", run.ID, sequence, "m", ""); err != nil {
				t.Errorf("concurrent append: %v", err)
			}
		}()
	}
	wg.Wait()

	var visible int
	if err := db.QueryRow(`SELECT COUNT(*) FROM fb_messages WHERE session_id='s' AND visibility='visible'`).Scan(&visible); err != nil {
		t.Fatal(err)
	}
	// The seed user row plus the two assistant rows the sequence added; the
	// reconciler recognizes its own user message as already stored. A racing
	// second writer that re-appended the suffix would leave 5.
	if visible != 3 {
		t.Fatalf("visible rows after two racing reconcilers = %d, want 3 (no duplicated suffix)", visible)
	}
}

// TestConcurrentFirstMessagesNameTheSessionOnce pins R3/D7: two first user
// messages racing must leave the session named after one of them, never stuck
// on its id. The naming is a conditional update that only fires while the title
// still equals the id, so exactly one writer flips it.
func TestConcurrentFirstMessagesNameTheSessionOnce(t *testing.T) {
	ctx := context.Background()
	db, err := OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewSessionStore(db, "main")

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		content := fmt.Sprintf("first message %d", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := store.Append(ctx, "s", "user", content); err != nil {
				t.Errorf("concurrent first message: %v", err)
			}
		}()
	}
	wg.Wait()

	var title string
	if err := db.QueryRow(`SELECT title FROM fb_sessions WHERE id='s'`).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if title == "s" || title == "" {
		t.Fatalf("session title after two racing first messages = %q, want one of their derived names", title)
	}
	if want := SessionTitleFromContent("first message 0"); title != want && title != SessionTitleFromContent("first message 1") {
		t.Fatalf("session title = %q, want one of the first messages' derived names", title)
	}
}

// TestStateMigrationPreservesModelContextBytes pins the plan's top rule
// (§4.5.4): the v0→v1 migration must not change one byte of what a session
// rebuilds its model context from — every visible transcript row and every
// frozen prompt-state value.
func TestStateMigrationPreservesModelContextBytes(t *testing.T) {
	ctx := context.Background()
	path := buildLegacyV0State(t, t.TempDir())

	// The bytes as the v0 database holds them, read through the plain driver
	// so no state.Open machinery touches the file first.
	raw, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	before := map[string]string{}
	// Rows of a session that does not exist are unreachable in v0 and dropped
	// by the migration; the comparison covers what a real session can rebuild.
	rows, err := raw.QueryContext(ctx, `
		SELECT m.session_id, m.id, m.role, m.content, IFNULL(m.parts,'[]')
		FROM fb_messages m
		WHERE IFNULL(m.source,'transcript')='transcript'
		  AND EXISTS(SELECT 1 FROM fb_sessions s WHERE s.id=m.session_id)
		  AND m.role IN ('user','assistant','tool','reasoning','system')
		ORDER BY m.session_id, m.id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var sid string
		var id int64
		var role, content, parts string
		if err := rows.Scan(&sid, &id, &role, &content, &parts); err != nil {
			t.Fatal(err)
		}
		before[sid] += fmt.Sprintf("%d\x00%s\x00%s\x00%s\x00", id, role, content, parts)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	promptRows, err := raw.QueryContext(ctx, `SELECT session_id, key, value FROM fb_session_prompt_state ORDER BY session_id, key`)
	if err != nil {
		t.Fatal(err)
	}
	for promptRows.Next() {
		var sid, key, value string
		if err := promptRows.Scan(&sid, &key, &value); err != nil {
			t.Fatal(err)
		}
		before["prompt:"+sid] += key + "\x00" + value + "\x00"
	}
	if err := promptRows.Err(); err != nil {
		t.Fatal(err)
	}
	promptRows.Close()
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	if before["alpha"] == "" {
		t.Fatal("fixture precondition: alpha must have visible rows before migration")
	}

	db, err := Open(ctx, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// After migration the same rows are present, visible, id-for-id and
	// byte-for-byte, and the prompt state kept its exact value.
	afterRows, err := db.QueryContext(ctx, `
		SELECT session_id, id, role, content, parts
		FROM fb_messages
		WHERE visibility='visible'
		ORDER BY session_id, id`)
	if err != nil {
		t.Fatal(err)
	}
	after := map[string]string{}
	for afterRows.Next() {
		var sid string
		var id int64
		var role, content, parts string
		if err := afterRows.Scan(&sid, &id, &role, &content, &parts); err != nil {
			t.Fatal(err)
		}
		after[sid] += fmt.Sprintf("%d\x00%s\x00%s\x00%s\x00", id, role, content, parts)
	}
	if err := afterRows.Err(); err != nil {
		t.Fatal(err)
	}
	afterRows.Close()
	sessionsBefore := 0
	for k := range before {
		if !strings.HasPrefix(k, "prompt:") {
			sessionsBefore++
		}
	}
	if len(after) != sessionsBefore {
		t.Fatalf("sessions with visible rows: before=%d after=%d", sessionsBefore, len(after))
	}
	for sid, want := range before {
		if strings.HasPrefix(sid, "prompt:") {
			continue
		}
		if after[sid] != want {
			t.Fatalf("session %s transcript bytes changed:\nbefore %q\nafter  %q", sid, want, after[sid])
		}
	}
	var promptValue string
	if err := db.QueryRowContext(ctx, `SELECT value FROM fb_session_prompt_state WHERE session_id='alpha' AND key='skills'`).Scan(&promptValue); err != nil {
		t.Fatal(err)
	}
	if promptValue != `["exact-bytes"]` || !strings.HasSuffix(before["prompt:alpha"], "\x00"+`["exact-bytes"]`+"\x00") {
		t.Fatalf("prompt state bytes changed: before %q after %q", before["prompt:alpha"], promptValue)
	}
}

func TestFreshStateSchemaIsAtV3(t *testing.T) {
	ctx := context.Background()
	db := openStateDB(t)
	requireSchemaVersion(ctx, t, db, stateSchemaVersion)
	var name string
	if err := db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name='fb_session_model_state'`).Scan(&name); err != nil {
		t.Fatalf("fresh schema has no fb_session_model_state: %v", err)
	}
}

// buildLegacyV2State creates the exact pre-v3 shape: the current schema minus
// fb_session_model_state, stamped user_version=2, with live rows to carry
// forward.

func TestStateV2UpgradesToV3WithDataIntact(t *testing.T) {
	ctx := context.Background()
	path := buildLegacyV2State(t)
	db, err := Open(ctx, path, nil)
	if err != nil {
		t.Fatalf("open a v2 state file: %v", err)
	}
	defer db.Close()
	requireSchemaVersion(ctx, t, db, stateSchemaVersion)
	requireForeignKeysClean(ctx, t, db)

	var title, promptValue string
	if err := db.QueryRowContext(ctx, `SELECT title FROM fb_sessions WHERE id='s'`).Scan(&title); err != nil {
		t.Fatalf("session row after upgrade: %v", err)
	}
	if title != "session" {
		t.Fatalf("session title after upgrade = %q", title)
	}
	if err := db.QueryRowContext(ctx, `SELECT value FROM fb_session_prompt_state WHERE session_id='s' AND key='k'`).Scan(&promptValue); err != nil {
		t.Fatalf("prompt state after upgrade: %v", err)
	}
	if promptValue != "v" {
		t.Fatalf("prompt value after upgrade = %q", promptValue)
	}

	// The upgraded table works, and reopening a v3 file runs no DDL.
	store := NewSessionStore(db, "main")
	stored, err := store.SaveSessionModelSelection(ctx, "s", SessionModelSelection{Provider: "openai", Model: "gpt-a", Effort: "high"})
	if err != nil || !stored {
		t.Fatalf("save on upgraded table: stored=%v err=%v", stored, err)
	}
	before := sqliteMasterRows(ctx, t, db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	after := sqliteMasterRows(ctx, t, reopened)
	if len(before) != len(after) {
		t.Fatalf("reopen changed the schema: %d objects before, %d after", len(before), len(after))
	}
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("reopen changed schema object %d:\nbefore: %s\nafter:  %s", i, before[i], after[i])
		}
	}
	got, ok, err := NewSessionStore(reopened, "main").SessionModelSelection(ctx, "s")
	if err != nil || !ok || got.Model != "gpt-a" {
		t.Fatalf("selection after reopen = %+v ok=%v err=%v", got, ok, err)
	}
}

func TestStateV0AndPrematureV1UpgradeThroughV3(t *testing.T) {
	ctx := context.Background()
	v0Path := buildLegacyV0State(t, t.TempDir())
	db, err := Open(ctx, v0Path, nil)
	if err != nil {
		t.Fatalf("open a v0 state file: %v", err)
	}
	requireSchemaVersion(ctx, t, db, stateSchemaVersion)
	fresh, err := Open(ctx, filepath.Join(t.TempDir(), "fresh.sqlite"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	got, want := sqliteMasterRows(ctx, t, db), sqliteMasterRows(ctx, t, fresh)
	if len(got) != len(want) {
		t.Fatalf("v0-migrated database holds %d objects, fresh one holds %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("v0-migrated schema object %d differs:\nmigrated: %s\nfresh:    %s", i, got[i], want[i])
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	prematurePath := buildLegacyV0StateWith(t, `
INSERT INTO fb_sessions(id, agent_id, title, updated_at, created_at, compact_boundary_id)
VALUES('s','main','session',10,10,'');`)
	raw, err := sql.Open("sqlite3", prematurePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`PRAGMA user_version = 1`); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	premature, err := Open(ctx, prematurePath, nil)
	if err != nil {
		t.Fatalf("open a premature-v1 state file: %v", err)
	}
	defer premature.Close()
	requireSchemaVersion(ctx, t, premature, stateSchemaVersion)
	var tables int
	if err := premature.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='fb_session_model_state'`).Scan(&tables); err != nil || tables != 1 {
		t.Fatalf("fb_session_model_state after premature-v1 repair: count=%d err=%v", tables, err)
	}
}

func buildLegacyV2State(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy_v2.sqlite")
	db, err := Open(ctx, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM fb_sessions`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`DROP TABLE fb_session_model_state`,
		`INSERT INTO fb_sessions(id, agent_id, title, updated_at, created_at) VALUES('s','main','session',20,10)`,
		`INSERT INTO fb_session_prompt_state(session_id, key, value, updated_at) VALUES('s','k','v',20)`,
		`PRAGMA user_version = 2`,
	} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("build legacy v2 state (%s): %v", stmt, err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// buildV1ShapedState writes a file in the finished v1 shape — schema.sql
// without what later versions add — stamped v1 and holding the given rows,
// then applies alter (an intermediate build's leftovers, when a test wants
// one).
func buildV1ShapedState(t *testing.T, rows, alter string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "v1.sqlite")
	raw, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	declared, err := declaredSchemaObjects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, typ := range []string{"table", "index"} {
		for _, object := range declared {
			// The FTS table creates its own shadow tables.
			if object.typ != typ || stateObjectsAddedAfterV2[object.table] || strings.HasPrefix(object.name, "fb_memory_fts_") {
				continue
			}
			if _, err := raw.Exec(object.ddl); err != nil {
				t.Fatalf("build v1 %s: %v", object.name, err)
			}
		}
	}
	for _, stmt := range []string{`PRAGMA user_version = 1`, rows, alter} {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("build v1: %v", err)
		}
	}
	return path
}

// v1Rows are rows only a v1-shaped table can hold: a withdrawn message, a
// compaction pointer, a tool row's own window, a timed run, a flattened file
// key, an action whose session is a column (its payload no longer carries it)
// and a wait whose continuation crossed its execution fence.
const v1Rows = `
INSERT INTO fb_sessions(id, agent_id, title, created_at, updated_at) VALUES('s','main','s',10,10);
INSERT INTO fb_runs(id, session_id, input_text, status, started_at_ms, finished_at_ms, worked_ms, created_at, updated_at)
  VALUES('r','s','hi','waiting_action',1000,2000,1000,10,10);
INSERT INTO fb_messages(id, session_id, run_id, role, visibility, content, created_at) VALUES
  (1,'s','r','user','visible','kept',10),(2,'s',NULL,'user','withdrawn','taken back',11),(3,'s',NULL,'system','visible','checkpoint',12);
INSERT INTO fb_messages(id, session_id, run_id, role, visibility, content, tool_step_id, exec_started_at_ms, exec_finished_at_ms, exec_duration_ms, created_at)
  VALUES(4,'s','r','tool','visible','ok','call-1',1500,1500,0,13);
UPDATE fb_sessions SET compact_boundary_message_id=3 WHERE id='s';
INSERT INTO fb_session_events(session_id, run_id, event_id, event_type, payload_json, occurred_at_ms) VALUES
  ('s','r','evt-1','turn_started','{}',1000),('s','r','evt-2','tool_call_completed','{"tool_name":"shell"}',1500);
INSERT INTO fb_files(id, session_id, original_name, media_type, size_bytes, sha256, storage_backend, storage_key, parse_status, created_at, updated_at)
  VALUES('f','s','a.txt','text/plain',1,'x','local','ab/cd.txt','done',10,10);
INSERT INTO fb_actions(id, session_id, kind, status, payload_json, created_at, updated_at) VALUES('a','s','shell','approved','{}',10,10);
INSERT INTO fb_run_waits(run_id, action_id, tool_name, tool_input_json, resume_owner, resume_claimed_at_ms, resume_phase, created_at, updated_at)
  VALUES('r','a','shell','{}','proc-1',1700,'execution_started',10,10);`

// requireV1RowsCarried asserts every fact in v1Rows survived.
func requireV1RowsCarried(ctx context.Context, t *testing.T, db *sql.DB) {
	t.Helper()
	var visibility, key, phase, actionSession string
	var boundary, execDuration, worked sql.NullInt64
	for _, q := range []struct {
		query string
		dest  []any
	}{
		{`SELECT visibility FROM fb_messages WHERE id=2`, []any{&visibility}},
		{`SELECT compact_boundary_message_id FROM fb_sessions WHERE id='s'`, []any{&boundary}},
		{`SELECT exec_duration_ms FROM fb_messages WHERE id=4`, []any{&execDuration}},
		{`SELECT worked_ms FROM fb_runs WHERE id='r'`, []any{&worked}},
		{`SELECT storage_key FROM fb_files WHERE id='f'`, []any{&key}},
		{`SELECT session_id FROM fb_actions WHERE id='a'`, []any{&actionSession}},
		{`SELECT resume_phase FROM fb_run_waits WHERE run_id='r'`, []any{&phase}},
	} {
		if err := db.QueryRowContext(ctx, q.query).Scan(q.dest...); err != nil {
			t.Fatalf("%s: %v", q.query, err)
		}
	}
	if visibility != "withdrawn" || boundary.Int64 != 3 || !execDuration.Valid || execDuration.Int64 != 0 ||
		worked.Int64 != 1000 || key != "ab/cd.txt" || actionSession != "s" || phase != WaitResumePhaseExecutionStarted {
		t.Fatalf("v1 rows after upgrade: visibility=%q boundary=%v exec=%v worked=%v key=%q action=%q phase=%q",
			visibility, boundary, execDuration, worked, key, actionSession, phase)
	}
}

// TestFinishedV1FileUpgradesWithoutARebuild pins that a file in the finished
// v1 shape is recognised as one: it lacks only what later versions add, so the
// intermediate-v1 repair leaves it alone (its event cursor keeps its numbers,
// which a rebuild would renumber) and v3 adds its table from the declared
// text.
func TestFinishedV1FileUpgradesWithoutARebuild(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, buildV1ShapedState(t, v1Rows, ""), nil)
	if err != nil {
		t.Fatalf("open a finished v1 file: %v", err)
	}
	defer db.Close()
	requireSchemaVersion(ctx, t, db, stateSchemaVersion)
	requireForeignKeysClean(ctx, t, db)
	requireV1RowsCarried(ctx, t, db)
	var firstSequence int64
	if err := db.QueryRowContext(ctx, `SELECT sequence FROM fb_session_events WHERE event_id='evt-2'`).Scan(&firstSequence); err != nil || firstSequence != 2 {
		t.Fatalf("event cursor after upgrade = %d, %v; want it untouched", firstSequence, err)
	}
	fresh, err := Open(ctx, filepath.Join(t.TempDir(), "fresh.sqlite"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	got, want := sqliteMasterRows(ctx, t, db), sqliteMasterRows(ctx, t, fresh)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("upgraded v1 schema differs from fresh:\n%s\n---\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestIntermediateV1FileRebuildKeepsConvertedTables pins the repair of a file
// an intermediate build stamped v1 while some leftover was still in v0 shape:
// the rebuild reads every table in whichever shape it is in, so a table the
// build had already converted keeps its data instead of being read through
// the v0 column names it no longer has.
func TestIntermediateV1FileRebuildKeepsConvertedTables(t *testing.T) {
	ctx := context.Background()
	path := buildV1ShapedState(t, v1Rows, `ALTER TABLE fb_sessions ADD COLUMN todos TEXT NOT NULL DEFAULT ''`)
	db, err := Open(ctx, path, nil)
	if err != nil {
		t.Fatalf("open an intermediate v1 file: %v", err)
	}
	defer db.Close()
	requireSchemaVersion(ctx, t, db, stateSchemaVersion)
	requireForeignKeysClean(ctx, t, db)
	requireV1RowsCarried(ctx, t, db)
	fresh, err := Open(ctx, filepath.Join(t.TempDir(), "fresh.sqlite"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	got, want := sqliteMasterRows(ctx, t, db), sqliteMasterRows(ctx, t, fresh)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatal("rebuilt intermediate v1 schema differs from fresh")
	}
}

// TestStateV3UpgradesToV4WithSessionsIntact builds a database that is really
// at v3 — the sessions table has no source column — with live session rows,
// and pins that opening it adds the column with every row preserved.
func TestStateV3UpgradesToV4WithSessionsIntact(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.sqlite")
	db, err := sql.Open(sqliteDriverName(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		t.Fatal(err)
	}
	// Drop back to the v3 shape: no source column, version 3.
	if _, err := db.Exec(`ALTER TABLE fb_sessions DROP COLUMN source`); err != nil {
		t.Fatalf("drop source: %v", err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 3`); err != nil {
		t.Fatal(err)
	}
	const sid = "s-v3"
	if _, err := db.Exec(`INSERT INTO fb_sessions(id, agent_id, title, created_at, updated_at) VALUES(?,?,?,?,?)`,
		sid, "main", sid, 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	migrated, err := Open(ctx, path, nil)
	if err != nil {
		t.Fatalf("open v3 database: %v", err)
	}
	defer migrated.Close()
	requireSchemaVersion(ctx, t, migrated, stateSchemaVersion)
	var source string
	if err := migrated.QueryRowContext(ctx, `SELECT source FROM fb_sessions WHERE id=?`, sid).Scan(&source); err != nil {
		t.Fatalf("read migrated row: %v", err)
	}
	if source != "" {
		t.Fatalf("migrated session source = %q, want the ordinary-conversation default", source)
	}
	// The column reads as writable through the store's own setter.
	store := NewSessionStore(migrated, "main")
	if err := store.SetSessionSource(ctx, sid, "workshop"); err != nil {
		t.Fatalf("set source: %v", err)
	}
	summaries, err := store.ListSessionsRecent(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 || summaries[0].Source != "workshop" {
		t.Fatalf("summaries after set = %#v", summaries)
	}
}

// TestStateV4UpgradesToV5WithRunsIntact builds a database that is really at
// v4 — fb_runs has no owner column, the owner lease table and the
// session-live index do not exist — with a running row in it, and pins that
// opening it adds all three with the row preserved: the legacy running row
// reads owner = ” (nobody vouches for it) and the first reap ends it as
// abandoned.
func TestStateV4UpgradesToV5WithRunsIntact(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.sqlite")
	db, err := sql.Open(sqliteDriverName(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		t.Fatal(err)
	}
	// Drop back to the v4 shape: no owner column, no lease table, no live
	// index, version 4.
	for _, stmt := range []string{
		`DROP INDEX idx_fb_runs_session_live`,
		`DROP TABLE fb_run_owners`,
		`ALTER TABLE fb_runs DROP COLUMN owner`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if _, err := db.Exec(`PRAGMA user_version = 4`); err != nil {
		t.Fatal(err)
	}
	const sid = "s-v4"
	if _, err := db.Exec(`INSERT INTO fb_sessions(id, agent_id, title, created_at, updated_at) VALUES(?,?,?,?,?)`,
		sid, "main", sid, 1, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO fb_runs(id, session_id, input_text, status, created_at, updated_at) VALUES(?,?,?,?,?,?)`,
		"r-v4", sid, "orphaned running turn", "running", 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	migrated, err := Open(ctx, path, nil)
	if err != nil {
		t.Fatalf("open v4 database: %v", err)
	}
	defer migrated.Close()
	requireSchemaVersion(ctx, t, migrated, stateSchemaVersion)
	var owner string
	if err := migrated.QueryRowContext(ctx, `SELECT owner FROM fb_runs WHERE id='r-v4'`).Scan(&owner); err != nil {
		t.Fatalf("read migrated run: %v", err)
	}
	if owner != "" {
		t.Fatalf("migrated run owner = %q, want the no-owner default", owner)
	}
	// The store's own reap immediately settles the legacy row: no live
	// process vouches for it.
	rt := &RunStore{DB: migrated}
	reaped, err := rt.ReapAbandonedRuns(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(reaped) != 1 || reaped[0].ID != "r-v4" {
		t.Fatalf("reap after migration = %+v, want the legacy running row", reaped)
	}
	var status string
	var started, finished, worked int64
	if err := migrated.QueryRowContext(ctx,
		`SELECT status, started_at_ms, finished_at_ms, worked_ms FROM fb_runs WHERE id='r-v4'`).
		Scan(&status, &started, &finished, &worked); err != nil {
		t.Fatal(err)
	}
	if status != string(RunStatusFailed) || started != 1000 || finished < started || worked != finished-started {
		t.Fatalf("reaped legacy row: status=%q clock=%d/%d/%d", status, started, finished, worked)
	}
}

// TestStateV5UpgradesToV6MarksFireSessions builds a database that is really at
// v5 — fb_cron_runs carries no error_code, fb_cron_jobs no last_error_code,
// the session index is gone — with an ordinary conversation and a session a
// fire recorded, and pins that opening it marks the fire's session as what it
// is while the ordinary one stays an ordinary conversation, and brings the
// three objects v6 declares.
func TestStateV5UpgradesToV6MarksFireSessions(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.sqlite")
	db, err := sql.Open(sqliteDriverName(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		t.Fatal(err)
	}
	// Drop back to the v5 shape: no error-code columns, no session index,
	// version 5.
	for _, stmt := range []string{
		`DROP INDEX idx_fb_cron_runs_session`,
		`ALTER TABLE fb_cron_runs DROP COLUMN error_code`,
		`ALTER TABLE fb_cron_jobs DROP COLUMN last_error_code`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if _, err := db.Exec(`PRAGMA user_version = 5`); err != nil {
		t.Fatal(err)
	}
	for _, sid := range []string{"s-plain", "cron-job-1-1790000000"} {
		if _, err := db.Exec(`INSERT INTO fb_sessions(id, agent_id, title, created_at, updated_at) VALUES(?,?,?,?,?)`,
			sid, "main", sid, 1, 1); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO fb_cron_runs(job_id, agent_id, session_id, trigger, status, started_at)
VALUES('job-1', 'main', 'cron-job-1-1790000000', 'schedule', 'running', 1)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	migrated, err := Open(ctx, path, nil)
	if err != nil {
		t.Fatalf("open v5 database: %v", err)
	}
	defer migrated.Close()
	requireSchemaVersion(ctx, t, migrated, stateSchemaVersion)
	requireForeignKeysClean(ctx, t, migrated)

	var fireSource, plainSource string
	if err := migrated.QueryRowContext(ctx, `SELECT source FROM fb_sessions WHERE id='cron-job-1-1790000000'`).Scan(&fireSource); err != nil {
		t.Fatal(err)
	}
	if fireSource != SessionSourceCron {
		t.Fatalf("fire session source = %q, want %q", fireSource, SessionSourceCron)
	}
	if err := migrated.QueryRowContext(ctx, `SELECT source FROM fb_sessions WHERE id='s-plain'`).Scan(&plainSource); err != nil {
		t.Fatal(err)
	}
	if plainSource != "" {
		t.Fatalf("an ordinary conversation's source = %q, want the default", plainSource)
	}
	var idx int
	if err := migrated.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_fb_cron_runs_session'`).Scan(&idx); err != nil {
		t.Fatal(err)
	}
	if idx != 1 {
		t.Fatal("idx_fb_cron_runs_session must exist after the migration")
	}
	// The two error-code columns are writable through the store's own setters.
	store := &CronStore{DB: migrated}
	var id int64
	if err := migrated.QueryRowContext(ctx, `SELECT id FROM fb_cron_runs WHERE session_id='cron-job-1-1790000000'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if won, err := store.FinishRun(ctx, id, CronStatusOK, "the answer", "", ""); err != nil || !won {
		t.Fatalf("close the migrated fire = %v (%v)", won, err)
	}
	if err := store.MarkFireDeliveryFailed(ctx, id, "delivery_failed", "channel unreachable"); err != nil {
		t.Fatal(err)
	}
	runs, err := store.ListRuns(ctx, "job-1", 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = %v (%v)", runs, err)
	}
	if runs[0].ErrorCode != "delivery_failed" {
		t.Fatalf("error_code after the migration = %q", runs[0].ErrorCode)
	}
}

// TestStateV6UpgradesToV7GivesDeniedToolRowsTheirDisplay builds a database
// that is really at v6 and holds the four shapes a denied approval could
// leave: an exit_plan_mode refusal whose reason wrapped plan reviews, one
// with the user's plain words, a refused request_permissions, a refused
// ordinary tool, and one row that already carries a display part. Opening it
// appends the display half to the first four and leaves the fifth alone, so
// a replay of any of them draws what the live refusal card drew rather than
// the text written for the model (D15, option B).
func TestStateV6UpgradesToV7GivesDeniedToolRowsTheirDisplay(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.sqlite")
	db, err := sql.Open(sqliteDriverName(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 6`); err != nil {
		t.Fatal(err)
	}
	const sid = "s-v6"
	if _, err := db.Exec(`INSERT INTO fb_sessions(id, agent_id, title, created_at, updated_at) VALUES(?,?,?,?,?)`,
		sid, "main", sid, 1, 1); err != nil {
		t.Fatal(err)
	}
	insert := func(content, parts string) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO fb_messages(session_id, role, content, parts, created_at) VALUES(?,?,?,?,1)`,
			sid, "tool", content, parts); err != nil {
			t.Fatal(err)
		}
	}
	withReview := "Tool approval denied by user: the user rejected this exit_plan_mode tool call. Try a different approach or ask the user for guidance.\n\n" +
		"User feedback: The user asked openai / gpt-5.1 to review this plan before approving it.\n" +
		"<review model=\"openai / gpt-5.1\">\nVerdict: rework.\n</review>\n" +
		"The user's own feedback, which outranks the reviews:\n改成先写测试"
	insert(withReview, "[]")
	plainWords := "Tool approval denied by user: the user rejected this exit_plan_mode tool call. Try a different approach or ask the user for guidance.\n\nUser feedback: 改成先写测试"
	insert(plainWords, `[{"type":"tool_result_meta","tool_call_id":"c2"}]`)
	insert("Tool approval denied by user: the user rejected this request_permissions tool call. Try a different approach or ask the user for guidance.", "[]")
	insert("Tool approval denied by user: the user rejected this shell tool call.", "[]")
	alreadyCarried := `[{"type":"tool_display","body":"kept"}]`
	insert("Tool approval denied by user: the user rejected this shell tool call.", alreadyCarried)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	migrated, err := Open(ctx, path, nil)
	if err != nil {
		t.Fatalf("open v6 database: %v", err)
	}
	defer migrated.Close()
	requireSchemaVersion(ctx, t, migrated, stateSchemaVersion)

	display := func(nth int) (string, string) {
		t.Helper()
		var partsJSON string
		if err := migrated.QueryRowContext(ctx,
			`SELECT parts FROM fb_messages WHERE session_id=? AND role='tool' ORDER BY id LIMIT 1 OFFSET ?`, sid, nth).
			Scan(&partsJSON); err != nil {
			t.Fatal(err)
		}
		var parts []map[string]any
		if err := json.Unmarshal([]byte(partsJSON), &parts); err != nil {
			t.Fatalf("parts of row %d: %v", nth, err)
		}
		for _, part := range parts {
			if v, _ := part["type"].(string); v == PartTypeToolDisplay {
				body, _ := part["body"].(string)
				meta, _ := part["tool_meta_json"].(string)
				return body, meta
			}
		}
		return "", ""
	}
	if body, meta := display(0); body != "(no output)" || !strings.Contains(meta, `"tool_name":"exit_plan_mode"`) || !strings.Contains(meta, `"status":"denied"`) {
		t.Fatalf("review-carrying refusal display = %q / %q, want (no output) and denied exit_plan_mode", body, meta)
	}
	if body, meta := display(1); body != "改成先写测试" || !strings.Contains(meta, `"tool_name":"exit_plan_mode"`) {
		t.Fatalf("plain refusal display = %q / %q, want the user's words", body, meta)
	}
	if body, _ := display(2); body != "The user did not approve the requested permissions" {
		t.Fatalf("request_permissions refusal display = %q", body)
	}
	if body, meta := display(3); body != "" || !strings.Contains(meta, `"tool_name":"shell"`) {
		t.Fatalf("ordinary refusal display = %q / %q, want an empty body on a denied shell", body, meta)
	}
	if body, _ := display(4); body != "kept" {
		t.Fatalf("a row that already carried a display part must keep it, got %q", body)
	}
	// The stored model-facing text is untouched: the model's history reads
	// exactly what it read before the migration.
	var withReviewAfter string
	if err := migrated.QueryRowContext(ctx,
		`SELECT content FROM fb_messages WHERE session_id=? AND role='tool' ORDER BY id LIMIT 1`, sid).Scan(&withReviewAfter); err != nil {
		t.Fatal(err)
	}
	if withReviewAfter != withReview {
		t.Fatal("the migration must not rewrite the denial text the model reads")
	}
}

// TestStateV7UpgradesToV8BackfillsTheSendCallOfLegacySubagentEvents builds a
// database that is really at v7 and holds the shapes the backfill has to
// tell apart: a subagent_send call whose result named the execution it
// started, that execution's spawned/ended pair, a goal check's pair no send
// call answers, and an event that already names its call. Opening it links
// the first pair to the send call and touches nothing else — not the goal
// check's empty link, not the already-named one, and not a single other byte
// of any touched payload (decision D10, option A).
func TestStateV7UpgradesToV8BackfillsTheSendCallOfLegacySubagentEvents(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.sqlite")
	db, err := sql.Open(sqliteDriverName(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 7`); err != nil {
		t.Fatal(err)
	}
	const sid = "s-v7"
	if _, err := db.Exec(`INSERT INTO fb_sessions(id, agent_id, title, created_at, updated_at) VALUES(?,?,?,?,?)`,
		sid, "main", sid, 1, 1); err != nil {
		t.Fatal(err)
	}
	payloads := map[string]string{}
	insertEvent := func(eventID, typ, payload string) {
		t.Helper()
		payloads[eventID] = payload
		if _, err := db.Exec(`INSERT INTO fb_session_events(session_id, event_id, event_type, payload_json, occurred_at_ms) VALUES(?,?,?,?,1)`,
			sid, eventID, typ, payload); err != nil {
			t.Fatal(err)
		}
	}
	// The send call's result carries the run_id of the execution it started.
	sendResult := `{"agent_id":"agent-1","task_id":"task-1","run_id":"exec-1","status":"running"}`
	insertEvent("evt-send", "tool_call_completed", fmt.Sprintf(
		`{"kind":"tool_call_completed","step_id":"call-send-1","tool_name":"subagent_send","output":{"output":%s}}`,
		strconv.Quote(sendResult)))
	insertEvent("evt-spawn", "subagent_spawned", `{"agent_id":"task-1","agent_type":"general-purpose","task_id":"task-1","title":"Legacy send","task_index":0,"execution_id":"exec-1"}`)
	insertEvent("evt-end", "subagent_ended", `{"agent_id":"task-1","agent_type":"general-purpose","task_id":"task-1","status":"ok","task_index":0,"execution_id":"exec-1"}`)
	// A goal check's execution: no send call ever answered it.
	insertEvent("evt-goal-spawn", "subagent_spawned", `{"agent_id":"goal-1","task_id":"goal-1","task_index":0,"execution_id":"exec-goal"}`)
	insertEvent("evt-goal-end", "subagent_ended", `{"agent_id":"goal-1","task_id":"goal-1","status":"ok","task_index":0,"execution_id":"exec-goal"}`)
	// An event that already names its call keeps it.
	insertEvent("evt-kept", "subagent_spawned", `{"agent_id":"task-2","task_id":"task-2","task_index":0,"execution_id":"exec-2","parent_tool_call_id":"call-kept"}`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	migrated, err := Open(ctx, path, nil)
	if err != nil {
		t.Fatalf("open v7 database: %v", err)
	}
	defer migrated.Close()
	requireSchemaVersion(ctx, t, migrated, stateSchemaVersion)

	payloadAfter := func(eventID string) string {
		t.Helper()
		var payload string
		if err := migrated.QueryRowContext(ctx, `SELECT payload_json FROM fb_session_events WHERE event_id=?`, eventID).Scan(&payload); err != nil {
			t.Fatalf("read %s: %v", eventID, err)
		}
		return payload
	}
	parentOf := func(payload string) string {
		t.Helper()
		var parent any
		if err := migrated.QueryRowContext(ctx, `SELECT json_extract(?, '$.parent_tool_call_id')`, payload).Scan(&parent); err != nil {
			t.Fatal(err)
		}
		if parent == nil {
			return ""
		}
		s, _ := parent.(string)
		return s
	}
	for _, eventID := range []string{"evt-spawn", "evt-end"} {
		after := payloadAfter(eventID)
		if got := parentOf(after); got != "call-send-1" {
			t.Fatalf("%s parent_tool_call_id = %q, want call-send-1", eventID, got)
		}
		var stripped string
		if err := migrated.QueryRowContext(ctx, `SELECT json_remove(?, '$.parent_tool_call_id')`, after).Scan(&stripped); err != nil {
			t.Fatal(err)
		}
		if stripped != payloads[eventID] {
			t.Fatalf("%s payload changed beyond the backfilled key:\n got %s\nwant %s", eventID, stripped, payloads[eventID])
		}
	}
	for _, eventID := range []string{"evt-goal-spawn", "evt-goal-end"} {
		after := payloadAfter(eventID)
		if got := parentOf(after); got != "" {
			t.Fatalf("%s parent_tool_call_id = %q, want still empty: no send call answers a goal check", eventID, got)
		}
		if after != payloads[eventID] {
			t.Fatalf("%s payload must be byte-identical, got %s want %s", eventID, after, payloads[eventID])
		}
	}
	if after := payloadAfter("evt-kept"); after != payloads["evt-kept"] || parentOf(after) != "call-kept" {
		t.Fatalf("an event that already named its call must be untouched, got %s", after)
	}
}
