package state

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
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
  created_at INTEGER NOT NULL DEFAULT 0
);
INSERT INTO fb_sessions(id, agent_id, title, updated_at, created_at) VALUES('legacy','main','legacy',2,1);
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
	if err := store.AppendMessageSequenceForRun(ctx, "new-session", run.ID, sequence, "model", "", RunTiming{}); err != nil {
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
	if _, err := db.ExecContext(ctx, `UPDATE fb_runs SET updated_at=updated_at+100 WHERE id=?`, child.ID); err != nil {
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
