package state

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state/dbx"
)

// TestTenantRowsCannotBeRecordedWithoutAnOwningAgent pins the invariant the
// whole memory tenancy rests on: a conversation, the memories extracted from it
// and the jobs that produce them each belong to exactly one primary agent, and
// a row naming none cannot exist. The storage layer refuses it rather than
// leaving the pipeline to decide whose it is.
func TestTenantRowsCannotBeRecordedWithoutAnOwningAgent(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	for _, tc := range []struct {
		name string
		stmt string
	}{
		{"session with no agent", `INSERT INTO fb_sessions(id, title, updated_at, created_at) VALUES('s','s',1,1)`},
		{"session with a blank agent", `INSERT INTO fb_sessions(id, agent_id, title, updated_at, created_at) VALUES('s','  ','s',1,1)`},
		{"stage1 output with no agent", `INSERT INTO fb_memory_stage1_outputs(thread_id, source_updated_at, raw_memory, rollout_summary, generated_at) VALUES('t',1,'raw','summary',1)`},
		{"stage1 output with a blank agent", `INSERT INTO fb_memory_stage1_outputs(thread_id, agent_id, source_updated_at, raw_memory, rollout_summary, generated_at) VALUES('t','  ',1,'raw','summary',1)`},
		{"job with no agent", `INSERT INTO fb_memory_jobs(kind, job_key, status, retry_remaining) VALUES('memory_stage1','t','pending',3)`},
		{"job with a blank agent", `INSERT INTO fb_memory_jobs(kind, job_key, agent_id, status, retry_remaining) VALUES('memory_stage1','t','  ','pending',3)`},
	} {
		if _, err := db.ExecContext(ctx, tc.stmt); err == nil {
			t.Errorf("%s: recorded a row nobody owns", tc.name)
		}
	}

	// A message cannot exist without its session row: the foreign key refuses
	// the insert outright, so no write path can record a transcript row into a
	// conversation nobody owns.
	if _, err := db.ExecContext(ctx, `INSERT INTO fb_messages(session_id, role, content, created_at) VALUES('unknown-session','user','hi',1)`); err == nil {
		t.Fatal("message insert for a session that does not exist should violate the foreign key")
	}
	var sessions int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM fb_sessions WHERE id='unknown-session'`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if sessions != 0 {
		t.Fatalf("%d sessions nobody owns exist", sessions)
	}
}

// Cron was removed from forebrain once, and this test used to assert its tables
// stayed gone. The feature is back by an explicit decision, in a different
// shape: jobs belong to a primary agent, and every fire is recorded. The guard
// therefore now asserts what the schema must have — a tenant column on the job
// table, so one agent's standing work can never be listed or fired under
// another's.
func TestCronSchemaIsScopedToAPrimaryAgent(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	for _, table := range []string{"fb_cron_jobs", "fb_cron_runs", "fb_heartbeats"} {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE name=?`, table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("table %q is missing from the fresh schema", table)
		}
	}

	if _, err := db.ExecContext(ctx, `INSERT INTO fb_cron_jobs (id, agent_id, schedule, created_at, updated_at) VALUES ('j1', '', 'every 1h', 1, 1)`); err == nil {
		t.Fatal("a cron job with no owning agent must be refused: an unowned job would run under whichever tenant happened to be active")
	}
}

func TestOpenMigratesMessageRunIdentityAndPersistsExactAssociation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	legacy, err := sql.Open(sqliteDriverName(), sqliteDataSourceForStateFile(path))
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.ExecContext(ctx, `
CREATE TABLE fb_sessions (
  id TEXT PRIMARY KEY, agent_id TEXT NOT NULL, title TEXT, updated_at INTEGER NOT NULL,
  created_at INTEGER NOT NULL DEFAULT 0, cwd TEXT NOT NULL DEFAULT ''
);
INSERT INTO fb_sessions(id, agent_id, title, updated_at, created_at, cwd) VALUES('legacy','main','legacy',2,1,'/repo/legacy');
CREATE TABLE fb_messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT NOT NULL,
  role TEXT NOT NULL, content TEXT NOT NULL, created_at INTEGER NOT NULL,
  message_id TEXT NOT NULL DEFAULT '', parts TEXT NOT NULL DEFAULT '[]', model TEXT,
  updated_at INTEGER NOT NULL DEFAULT 0, finished_at INTEGER, usage_json TEXT NOT NULL DEFAULT '',
  run_started_at TEXT NOT NULL DEFAULT '', run_finished_at TEXT NOT NULL DEFAULT '',
  worked_duration_ms INTEGER NOT NULL DEFAULT 0, source TEXT NOT NULL DEFAULT 'transcript',
  tool_step_id TEXT NOT NULL DEFAULT '', tool_meta_json TEXT NOT NULL DEFAULT ''
);
INSERT INTO fb_messages(session_id,role,content,created_at) VALUES('legacy','assistant','old',1);
`)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := Open(ctx, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var legacyRunID sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT run_id FROM fb_messages WHERE session_id='legacy'`).Scan(&legacyRunID); err != nil {
		t.Fatal(err)
	}
	if legacyRunID.Valid {
		t.Fatalf("legacy run id = %q, want NULL for a row no run was ever bound to", legacyRunID.String)
	}

	store := NewSessionStore(db, "main")
	if _, err := store.Append(ctx, "new-session", "user", "question"); err != nil {
		t.Fatal(err)
	}
	// A message row may name a run only if that run exists: the reference is a
	// foreign key now, so the run is created before the rows that cite it.
	run, err := (&RunStore{DB: db}).CreateRun(ctx, "new-session", "question")
	if err != nil {
		t.Fatal(err)
	}
	sequence := []llm.Message{
		llm.UserMessage(llm.Text("question")),
		llm.AssistantMessage([]llm.ContentPart{llm.Text("answer")}),
	}
	if err := store.AppendMessageSequenceForRun(ctx, "new-session", run.ID, sequence, "model", ""); err != nil {
		t.Fatal(err)
	}
	rows, err := store.ListAllMessages(ctx, "new-session", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].RunID != "" || rows[1].RunID != run.ID {
		t.Fatalf("message run ids = %#v, want user unbound and assistant %s", rows, run.ID)
	}
}

func TestSessionUIStateIsDurableAndOwnerScoped(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	owner := NewSessionStore(db, "main")
	if err := owner.Ensure(ctx, "s1", "s1"); err != nil {
		t.Fatal(err)
	}
	want := []byte(`{"active_view":"agent-1","views":{"agent-1":{"scroll_offset":9,"follow":false}}}`)
	if err := owner.SaveSessionUIState(ctx, "s1", "tui", want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := owner.LoadSessionUIState(ctx, "s1", "tui")
	if err != nil || !ok || string(got) != string(want) {
		t.Fatalf("LoadSessionUIState = %s, %v, %v", got, ok, err)
	}
	other := NewSessionStore(db, "other")
	if _, _, err := other.LoadSessionUIState(ctx, "s1", "tui"); !errors.Is(err, ErrSessionNotOwned) {
		t.Fatalf("cross-owner load error = %v, want ErrSessionNotOwned", err)
	}
	if err := owner.SaveSessionUIState(ctx, "missing", "tui", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	var orphan int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM fb_session_ui_state WHERE session_id='missing'`).Scan(&orphan); err != nil {
		t.Fatal(err)
	}
	if orphan != 0 {
		t.Fatal("browsing state created an orphan session record")
	}
}

// TestSessionUsageSumsTheRunTreeOnceAndLastTurnIsTopLevel pins the two usage
// reads /status is built from. The session total walks the run tree by parent
// link — a subagent run under the parent's session, and a continued one
// recorded under its worker session — and counts failed runs too, because the
// tokens they record were spent. The last turn is the newest top-level run: a
// subagent that finishes after its parent is not the user's last turn.
func TestSessionUsageSumsTheRunTreeOnceAndLastTurnIsTopLevel(t *testing.T) {
	ctx := context.Background()
	db, err := OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rt := &RunStore{DB: db}
	ensureSessionsForRuns(t, db, "s1", "worker-session")

	parent, err := rt.CreateRun(ctx, "s1", "turn")
	if err != nil {
		t.Fatal(err)
	}
	child, err := rt.CreateSubagentRun(ctx, parent.ID, "s1", "child")
	if err != nil {
		t.Fatal(err)
	}
	worker, err := rt.CreateSubagentRun(ctx, child.ID, "worker-session", "continued")
	if err != nil {
		t.Fatal(err)
	}
	failed, err := rt.CreateRun(ctx, "s1", "failed turn")
	if err != nil {
		t.Fatal(err)
	}
	for id, u := range map[string]LastRunUsage{
		parent.ID: {PromptTokens: 10, CompletionTokens: 1, CacheReadTokens: 100, CacheWriteTokens: 5, LLMCalls: 2},
		child.ID:  {PromptTokens: 20, CompletionTokens: 2, CacheReadTokens: 200, LLMCalls: 3},
		worker.ID: {PromptTokens: 30, CompletionTokens: 3, LLMCalls: 1},
		failed.ID: {PromptTokens: 40, CompletionTokens: 4, LLMCalls: 1},
	} {
		if err := rt.SetRunUsage(ctx, id, u); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{parent.ID, child.ID, worker.ID} {
		if err := rt.SetStatus(ctx, id, RunStatusDone); err != nil {
			t.Fatal(err)
		}
	}
	if err := rt.SetStatus(ctx, failed.ID, RunStatusFailed); err != nil {
		t.Fatal(err)
	}
	// The child settles last, as a subagent can.
	if _, err := db.ExecContext(ctx, dbx.Rebind(dbx.DialectOf(db), `UPDATE fb_runs SET updated_at=updated_at+100 WHERE id=?`), child.ID); err != nil {
		t.Fatal(err)
	}

	totals, err := rt.SessionUsageForSession(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	want := SessionUsage{PromptTokens: 100, CompletionTokens: 10, CacheReadTokens: 300, CacheWriteTokens: 5, LLMCalls: 7}
	if totals != want {
		t.Fatalf("session totals = %+v, want %+v", totals, want)
	}

	last, err := rt.LastRunUsageForSession(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if last.PromptTokens != 10 || last.CacheReadTokens != 100 {
		t.Fatalf("last turn = %+v, want the parent run", last)
	}
}

// TestRunUsageStoresModelCallTime pins that the run's aggregated usage row
// carries the model-call time beside the tokens: the successful path's
// persistRunUsage writes it from the summary's latency, and both reads (the
// last-turn one and the session total) hand it back.
func TestRunUsageStoresModelCallTime(t *testing.T) {
	ctx := context.Background()
	db, err := OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rt := &RunStore{DB: db}
	ensureSessionsForRuns(t, db, "s1")

	run, err := rt.CreateRun(ctx, "s1", "turn")
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.SetRunUsage(ctx, run.ID, LastRunUsage{
		PromptTokens: 100, CompletionTokens: 42, LLMCalls: 2, LLMMs: 8_500,
	}); err != nil {
		t.Fatal(err)
	}
	if err := rt.SetStatus(ctx, run.ID, RunStatusDone); err != nil {
		t.Fatal(err)
	}

	var raw int
	if err := db.QueryRowContext(ctx, dbx.Rebind(dbx.DialectOf(db), `SELECT usage_llm_ms FROM fb_runs WHERE id=?`), run.ID).Scan(&raw); err != nil {
		t.Fatalf("read usage_llm_ms: %v", err)
	}
	if raw != 8_500 {
		t.Fatalf("usage_llm_ms = %d, want 8500", raw)
	}
	last, err := rt.LastRunUsageForSession(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if last.LLMMs != 8_500 {
		t.Fatalf("last turn LLMMs = %d, want 8500", last.LLMMs)
	}
	totals, err := rt.SessionUsageForSession(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if totals.LLMMs != 8_500 {
		t.Fatalf("session LLMMs = %d, want 8500", totals.LLMMs)
	}
}

// TestSessionOutputRateIgnoresRunsWithoutTime pins the rate query's pairing
// rule: only runs that recorded a model-call time contribute, so a run with
// tokens but no time (a failed one, or a row older than the column) stays out
// of both the numerator and the denominator.
func TestSessionOutputRateIgnoresRunsWithoutTime(t *testing.T) {
	ctx := context.Background()
	db, err := OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rt := &RunStore{DB: db}
	ensureSessionsForRuns(t, db, "s1")

	timed, err := rt.CreateRun(ctx, "s1", "timed turn")
	if err != nil {
		t.Fatal(err)
	}
	untimed, err := rt.CreateRun(ctx, "s1", "untimed turn")
	if err != nil {
		t.Fatal(err)
	}
	for id, u := range map[string]LastRunUsage{
		timed.ID:   {CompletionTokens: 300, LLMCalls: 1, LLMMs: 6_000},
		untimed.ID: {CompletionTokens: 9_999, LLMCalls: 1},
	} {
		if err := rt.SetRunUsage(ctx, id, u); err != nil {
			t.Fatal(err)
		}
	}

	tokens, ms, err := rt.SessionOutputRate(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if tokens != 300 || ms != 6_000 {
		t.Fatalf("SessionOutputRate = %d tokens / %d ms, want 300 / 6000 (only the timed run)", tokens, ms)
	}

	// A session whose no run recorded the time answers zero on both sides:
	// no denominator, no rate.
	if _, err := db.ExecContext(ctx, dbx.Rebind(dbx.DialectOf(db), `UPDATE fb_runs SET usage_llm_ms=0 WHERE id=?`), timed.ID); err != nil {
		t.Fatal(err)
	}
	tokens, ms, err = rt.SessionOutputRate(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if tokens != 0 || ms != 0 {
		t.Fatalf("SessionOutputRate with no timed runs = %d / %d, want 0 / 0", tokens, ms)
	}
}

// TestLatestPrimaryRunTreeAttemptsIncludesSubagentRows pins the reading-end
// contract of plan 182: the attempts of a turn include its subagents' — the
// latest primary run's tree, every row carrying its own run id so a reader
// can tell whose request it was. The latest primary run's choice is
// deterministic even when two turns share updated_at's second: the
// later-inserted run wins.
func TestLatestPrimaryRunTreeAttemptsIncludesSubagentRows(t *testing.T) {
	ctx := context.Background()
	db, err := OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rt := &RunStore{DB: db}
	ensureSessionsForRuns(t, db, "s1", "s2")

	// Two primary runs in s1: r1 (an older turn) and r2 (the latest turn).
	// Direct inserts, not CreateRun — the session-exclusivity check refuses a
	// second live primary run — and updated_at must differ explicitly: both
	// are second-granularity, and at equal values "latest" is the storage
	// engine's coin flip, not a fact.
	seedPrimaryRun := func(sessionID, runID string, updatedAt int64) {
		t.Helper()
		if _, err := db.ExecContext(ctx,
			dbx.Rebind(dbx.DialectOf(db), `INSERT INTO fb_runs(id, session_id, input_text, status, created_at, updated_at) VALUES(?,?,'',?,?,?)`),
			runID, sessionID, string(RunStatusDone), updatedAt, updatedAt); err != nil {
			t.Fatalf("create run %s: %v", runID, err)
		}
	}
	seedPrimaryRun("s1", "r1", 100)
	seedPrimaryRun("s1", "r2", 200)
	sub, err := rt.CreateSubagentRun(ctx, "r2", "s1", "sub")
	if err != nil {
		t.Fatal(err)
	}
	attempt := func(runID string, call int) {
		t.Helper()
		if err := rt.InsertLLMAttempt(ctx, LLMAttempt{
			RunID: runID, Call: call, Attempt: 1, Outcome: LLMAttemptSuccess,
		}); err != nil {
			t.Fatalf("insert attempt for %s call %d: %v", runID, call, err)
		}
	}
	// r1's row lands first so a leak would head the answer; r2's two rows
	// sandwich the subagent's, in the order the requests happened.
	attempt("r1", 1)
	attempt("r2", 1)
	attempt(sub.ID, 1)
	attempt("r2", 2)

	rows, err := rt.LatestPrimaryRunTreeAttempts(ctx, "s1", 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("attempts = %+v, want r2's two rows and the subagent's one, r1's excluded", rows)
	}
	wantRun := []string{"r2", sub.ID, "r2"}
	wantCall := []int{1, 1, 2}
	for i, row := range rows {
		if row.RunID != wantRun[i] || row.Call != wantCall[i] {
			t.Fatalf("row %d = run %s call %d, want run %s call %d", i, row.RunID, row.Call, wantRun[i], wantCall[i])
		}
	}

	// Two primary runs sharing updated_at's second: the later insertion wins,
	// for the id query and the tree query alike — the tiebreak is insertion
	// order, never the storage engine's pick.
	seedPrimaryRun("s2", "rA", 300)
	seedPrimaryRun("s2", "rB", 300)
	attempt("rA", 1)
	attempt("rB", 1)
	latest, err := rt.LatestPrimaryRunIDForSession(ctx, "s2")
	if err != nil {
		t.Fatal(err)
	}
	if latest != "rB" {
		t.Fatalf("latest primary run = %s, want rB: same-second ties break by insertion order", latest)
	}
	rows, err = rt.LatestPrimaryRunTreeAttempts(ctx, "s2", 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].RunID != "rB" {
		t.Fatalf("s2 attempts = %+v, want only the later-inserted rB's row", rows)
	}

	// A session with no runs answers no rows and no error — nil, the
	// ListLLMAttempts convention; the handler's len>0 guard keeps the JSON []
	// contract, so the state layer must not manufacture an empty slice.
	rows, err = rt.LatestPrimaryRunTreeAttempts(ctx, "s3", 200)
	if err != nil {
		t.Fatal(err)
	}
	if rows != nil {
		t.Fatalf("empty session attempts = %+v, want nil", rows)
	}
}

// TestSQLiteBuildServesTheStateSchema pins that the test build (cgo on, fts5
// tag on) satisfies the probe guarding every state open: under the documented
// build the linked SQLite engine reports FTS5 and requireSQLiteBuild passes.
func TestSQLiteBuildServesTheStateSchema(t *testing.T) {
	if err := requireSQLiteBuild(context.Background()); err != nil {
		t.Fatalf("requireSQLiteBuild = %v, want nil for a cgo build with -tags fts5", err)
	}
}

// TestSQLiteBuildErrorIsOneSentenceWithTheInstallCommand pins the user-facing
// contract of a build that cannot open its state: one sentence that names the
// cause and the exact reinstall command.
func TestSQLiteBuildErrorIsOneSentenceWithTheInstallCommand(t *testing.T) {
	msg := errSQLiteBuild.Error()
	const install = "npm install -g @forebrain-harness/forebrain"
	if !strings.Contains(msg, install) {
		t.Fatalf("error %q does not carry the reinstall command %q", msg, install)
	}
	prose := strings.TrimRight(strings.Replace(msg, install, "", 1), " .!?")
	if strings.ContainsAny(prose, "\n") || strings.ContainsAny(prose, ".!?") {
		t.Fatalf("error %q is more than one sentence", msg)
	}
}

// TestOpenFromConfigDefaultsToTheStateFile pins the one case every existing
// install is in: no database.url means the SQLite file under the home's state
// directory, exactly where it always was (plan 209, design §2.1).
func TestOpenFromConfigDefaultsToTheStateFile(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	// The state directory is the caller's to create, as it always was: Open
	// creates the file, not the tree above it.
	if err := os.MkdirAll(filepath.Join(home, "state"), 0o755); err != nil {
		t.Fatalf("prepare state directory: %v", err)
	}
	db, err := OpenFromConfig(ctx, home, "   ", nil)
	if err != nil {
		t.Fatalf("open state database: %v", err)
	}
	defer db.Close()
	if got := dbx.DialectOf(db); got != dbx.SQLite {
		t.Fatalf("an empty database.url opened %s, want sqlite", got)
	}
	var tables int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'fb_sessions'`).Scan(&tables); err != nil {
		t.Fatalf("query state schema: %v", err)
	}
	if tables != 1 {
		t.Fatalf("the state file at %s holds no fb_sessions table", StateDBPath(home))
	}
}

// TestOpenFromConfigRejectsAnUnsupportedScheme fails the open rather than
// silently falling back to SQLite: a database.url the binary cannot serve must
// not look like a brand-new empty state.
func TestOpenFromConfigRejectsAnUnsupportedScheme(t *testing.T) {
	_, err := OpenFromConfig(context.Background(), t.TempDir(), "oracle://user:pass@db:1521/forebrain", nil)
	if err == nil {
		t.Fatal("an unsupported database type opened")
	}
	if !strings.Contains(err.Error(), "unsupported database type") {
		t.Fatalf("error %q does not name the unsupported type", err)
	}
}

// TestOpenFromConfigOpensTheNamedServer opens a state database on the MySQL or
// PostgreSQL server FOREBRAIN_TEST_DATABASE_URL names, proves it is at this
// binary's version, and proves a second open finds it current instead of
// recreating it — the read-only open included, which must not migrate.
func TestOpenFromConfigOpensTheNamedServer(t *testing.T) {
	raw := strings.TrimSpace(os.Getenv(testDatabaseURLVar))
	if raw == "" {
		t.Skipf("%s is not set", testDatabaseURLVar)
	}
	ctx := context.Background()
	scratch := filepath.Join(t.TempDir(), "state.sqlite")
	target, ok, err := testDatabaseURLFor(scratch)
	if err != nil || !ok {
		t.Fatalf("scratch database: %v", err)
	}
	u, err := url.Parse(target)
	if err != nil {
		t.Fatalf("parse scratch url: %v", err)
	}
	want := dbx.MySQL
	if u.Scheme == "postgres" || u.Scheme == "postgresql" {
		want = dbx.Postgres
	}

	db, err := OpenFromConfig(ctx, "", target, nil)
	if err != nil {
		t.Fatalf("open %s state database: %v", want, err)
	}
	if got := dbx.DialectOf(db); got != want {
		t.Fatalf("opened %s, want %s", got, want)
	}
	version, err := readDialectSchemaVersion(ctx, db, want)
	if err != nil {
		t.Fatalf("read version: %v", err)
	}
	if version != stateSchemaVersion {
		t.Fatalf("state database is at v%d, want v%d", version, stateSchemaVersion)
	}
	if want == dbx.MySQL {
		// The session settings every connection has to carry: byte-exact text
		// comparison and READ COMMITTED, which is what the store's lock-based
		// serialisation assumes (plan 209 §3.1).
		var charset, collation, isolation string
		if err := db.QueryRowContext(ctx,
			`SELECT @@character_set_client, @@collation_connection, @@transaction_isolation`).
			Scan(&charset, &collation, &isolation); err != nil {
			t.Fatalf("read session settings: %v", err)
		}
		if charset != mysqlCharset || collation != mysqlCollation {
			t.Fatalf("session is %s / %s, want %s / %s", charset, collation, mysqlCharset, mysqlCollation)
		}
		if isolation != "READ-COMMITTED" {
			t.Fatalf("session isolation is %s, want READ-COMMITTED", isolation)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Opening again meets the schema already at this version: no migration, no
	// DDL, and no error.
	again, err := OpenFromConfig(ctx, "", target, nil)
	if err != nil {
		t.Fatalf("reopen %s state database: %v", want, err)
	}
	defer again.Close()

	readOnly, err := OpenFromConfig(ctx, "", target, &OpenOptions{ReadOnly: true})
	if err != nil {
		t.Fatalf("read-only open of a current %s database: %v", want, err)
	}
	defer readOnly.Close()
	if got := dbx.DialectOf(readOnly); got != want {
		t.Fatalf("read-only open reads as %s, want %s", got, want)
	}
}

// TestMySQLConfigFromURL pins the conversion of a mysql:// URL into the
// driver's configuration (plan 209 §3.1): percent-encoded credentials reach
// the server as written, and the settings the store depends on — utf8mb4_bin,
// no time parsing, one statement per round trip, READ COMMITTED — cannot be
// overridden by the URL.
func TestMySQLConfigFromURL(t *testing.T) {
	u, err := url.Parse("mysql://us%40er:p%3Ass%2Fword@db.internal:3307/forebrain?tls=true&charset=latin1&collation=utf8mb4_general_ci&parseTime=true&multiStatements=true&transaction_isolation=%27READ-UNCOMMITTED%27&timeout=7s")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cfg, err := mysqlConfigFromURL(u)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if cfg.User != "us@er" || cfg.Passwd != "p:ss/word" {
		t.Fatalf("credentials came out as %q / %q", cfg.User, cfg.Passwd)
	}
	if cfg.Addr != "db.internal:3307" || cfg.Net != "tcp" || cfg.DBName != "forebrain" {
		t.Fatalf("target came out as %q %q %q", cfg.Net, cfg.Addr, cfg.DBName)
	}
	if cfg.ParseTime || cfg.MultiStatements {
		t.Fatalf("parseTime=%v multiStatements=%v, want both forced off", cfg.ParseTime, cfg.MultiStatements)
	}
	if cfg.Collation != mysqlCollation {
		t.Fatalf("collation %q, want %q", cfg.Collation, mysqlCollation)
	}
	// The URL's own charset/collation/parseTime/multiStatements are dropped:
	// the store decides them. The charset travels as a SET NAMES the driver
	// issues on every connection, which is why it is not a leftover param.
	for _, key := range []string{"charset", "collation"} {
		if got := cfg.Params[key]; got != "" {
			t.Fatalf("%s survived as %q; the URL may not decide it", key, got)
		}
	}
	if got := cfg.Params["transaction_isolation"]; got != "'READ-COMMITTED'" {
		t.Fatalf("transaction_isolation %q, want the READ COMMITTED setting every write lock assumes", got)
	}
	// A parameter the store does not decide itself is the user's to set.
	if got := cfg.Params["tls"]; got != "true" {
		t.Fatalf("tls %q, want true passed through", got)
	}
	if got := cfg.Params["timeout"]; got != "7s" {
		t.Fatalf("timeout %q, want 7s passed through", got)
	}

	if _, err := mysqlConfigFromURL(mustParseURL(t, "mysql://user:pass@db/forebrain")); err != nil {
		t.Fatalf("a URL without a port: %v", err)
	}
	if _, err := mysqlConfigFromURL(mustParseURL(t, "mysql://user:pass@db:3306/")); err == nil {
		t.Fatal("a mysql URL naming no database converted")
	}
	if _, err := mysqlConfigFromURL(mustParseURL(t, "mysql:///forebrain")); err == nil {
		t.Fatal("a mysql URL naming no host converted")
	}
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %s: %v", raw, err)
	}
	return u
}
