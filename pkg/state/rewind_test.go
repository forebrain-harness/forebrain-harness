package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state/dbx"
	"github.com/stretchr/testify/require"
)

// fbMessagesDDLV9 is fb_messages as v9 declared it, before 'rewound' was added
// to the visibility CHECK. The rebuild test uses it to build a genuinely old
// table rather than a fixture from the current schema.sql.
const fbMessagesDDLV9 = `
CREATE TABLE fb_messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id TEXT NOT NULL REFERENCES fb_sessions(id) ON DELETE CASCADE,
  run_id TEXT REFERENCES fb_runs(id) ON DELETE SET NULL,
  role TEXT NOT NULL CHECK (role IN ('user', 'assistant', 'tool', 'reasoning', 'system')),
  visibility TEXT NOT NULL DEFAULT 'visible' CHECK (visibility IN ('visible', 'repaired', 'withdrawn')),
  content TEXT NOT NULL,
  parts TEXT NOT NULL DEFAULT '[]',
  message_id TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL DEFAULT '',
  usage_json TEXT NOT NULL DEFAULT '',
  tool_step_id TEXT NOT NULL DEFAULT '',
  tool_meta_json TEXT NOT NULL DEFAULT '',
  exec_started_at_ms INTEGER,
  exec_finished_at_ms INTEGER,
  exec_duration_ms INTEGER,
  created_at INTEGER NOT NULL,
  CHECK (exec_started_at_ms IS NULL OR role IN ('tool', 'user')),
  CHECK ((exec_started_at_ms IS NULL) = (exec_finished_at_ms IS NULL)
     AND (exec_finished_at_ms IS NULL) = (exec_duration_ms IS NULL))
) STRICT`

func insertRun(t *testing.T, db *sql.DB, id, sessionID string) {
	t.Helper()
	if _, err := db.Exec(
		dbx.Rebind(dbx.DialectOf(db), `INSERT INTO fb_runs(id, session_id, input_text, status, created_at, updated_at) VALUES(?,?,?,'done',1,1)`),
		id, sessionID, id); err != nil {
		t.Fatalf("insert run %s: %v", id, err)
	}
}

// TestRewindMessagesAndEventsHideFromEveryReader pins the reader contract: a
// rewind's rows leave the visible transcript and model history, and the events
// of the turn it rewound to leave every event reader, until the rewind is
// fixed and a genuinely new turn follows it.
func TestRewindMessagesAndEventsHideFromEveryReader(t *testing.T) {
	ctx := context.Background()
	db := openSessionDB(t)
	store := NewSessionStore(db, "main")
	runs := &RunStore{DB: db}
	const sid = "s-rewind"
	require.NoError(t, store.Ensure(ctx, sid, sid))
	insertRun(t, db, "r1", sid)
	insertRun(t, db, "r2", sid)
	insertRun(t, db, "r3", sid)

	rowID := func(runID, role, content string) int64 {
		id, err := store.AppendMessageForRun(ctx, sid, runID, role, content, "", "", MessageExecTiming{})
		require.NoError(t, err)
		return id
	}
	u1 := rowID("r1", "user", "first question")
	_ = rowID("r1", "assistant", "first answer")
	_ = rowID("r2", "user", "second question")

	seqOf := func(runID string) int64 {
		evt, err := runs.AppendSessionEvent(ctx, SessionEvent{ID: "evt-" + runID + "-" + fmt.Sprint(len(runID)), SessionID: sid, RunID: runID, Type: "tool_call_completed", Payload: json.RawMessage(`{"tool_name":"read_file"}`)})
		require.NoError(t, err)
		return evt.Sequence
	}
	s1 := seqOf("r1")
	s2 := seqOf("r2")
	require.Less(t, s1, s2)

	// The rewind to before u1 hides every message from it on and every event
	// from its turn's first event on, while pending.
	start, err := store.RewindEventStart(ctx, sid, u1)
	require.NoError(t, err)
	require.Equal(t, s1, start)
	moved, err := store.RewindMessagesFrom(ctx, sid, u1)
	require.NoError(t, err)
	require.Equal(t, 3, moved)
	require.NoError(t, store.InsertRewind(ctx, RewindRecord{
		ID: "rw-1", SessionID: sid, AnchorMessageID: u1, Mode: "both",
		HiddenEventSeqFrom: start, State: "pending", CreatedAtMs: 1,
	}))

	msgs, err := store.ListRecentMessages(ctx, sid, 50)
	require.NoError(t, err)
	require.Empty(t, msgs, "every rewound row left the visible transcript")

	page, err := runs.ListSessionEvents(ctx, sid, 0, 0, 50)
	require.NoError(t, err)
	require.Empty(t, page.Events, "a pending rewind hides every event from its turn on")

	// A new user message fixes the rewind in place, and the rewind's hidden
	// range now ends at the new turn's first event.
	require.NoError(t, store.FixPendingRewinds(ctx, sid))
	_ = rowID("r3", "user", "a fresh question")
	s3 := seqOf("r3")
	require.Less(t, s2, s3)
	page, err = runs.ListSessionEvents(ctx, sid, 0, 0, 50)
	require.NoError(t, err)
	require.Len(t, page.Events, 1)
	require.Equal(t, s3, page.Events[0].Sequence, "round two stays hidden; only the new turn's events show")
}

// TestRewoundVisibilityIsAccepted pins the fourth visibility value the
// migration adds to the CHECK.
func TestRewoundVisibilityIsAccepted(t *testing.T) {
	ctx := context.Background()
	db := openSessionDB(t)
	store := NewSessionStore(db, "main")
	require.NoError(t, store.Ensure(ctx, "s", "s"))
	id, err := store.Append(ctx, "s", "user", "hi")
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, dbx.Rebind(dbx.DialectOf(db), `UPDATE fb_messages SET visibility='rewound' WHERE id=?`), id)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, dbx.Rebind(dbx.DialectOf(db), `UPDATE fb_messages SET visibility='visible' WHERE id=?`), id)
	require.NoError(t, err)
}

func TestRecordFileChangeLedger(t *testing.T) {
	ctx := context.Background()
	db := openSessionDB(t)
	store := NewSessionStore(db, "main")
	require.NoError(t, store.Ensure(ctx, "s", "s"))
	require.NoError(t, store.RecordFileChange(ctx, FileChange{
		SessionID: "s", RunID: "r", StepID: "st", AnchorMessageID: 7,
		Path: "/repo/a.go", ExistedBefore: true, BeforeSnapshot: "/snap/a", AfterSHA256: "deadbeef",
	}))
	require.NoError(t, store.RecordFileChange(ctx, FileChange{
		SessionID: "s", RunID: "r", StepID: "st", AnchorMessageID: 9,
		Path: "/repo/b.go", ExistedBefore: false, AfterSHA256: "cafe",
	}))
	since, err := store.FileChangesSince(ctx, "s", 8)
	require.NoError(t, err)
	require.Len(t, since, 1)
	require.Equal(t, "/repo/b.go", since[0].Path)
	all, err := store.AllFileChanges(ctx, "s")
	require.NoError(t, err)
	require.Len(t, all, 2)
	require.NoError(t, store.DeleteFileChangePath(ctx, "s", "/repo/a.go"))
	all, err = store.AllFileChanges(ctx, "s")
	require.NoError(t, err)
	require.Len(t, all, 1)
}

// insertChildRun adds a run that hangs off a parent, the way a subagent
// execution does.
func insertChildRun(t *testing.T, db *sql.DB, id, sessionID, parentID string) {
	t.Helper()
	if _, err := db.Exec(
		dbx.Rebind(dbx.DialectOf(db), `INSERT INTO fb_runs(id, session_id, parent_run_id, input_text, status, created_at, updated_at) VALUES(?,?,?,?,'done',1,1)`),
		id, sessionID, parentID, id); err != nil {
		t.Fatalf("insert child run %s: %v", id, err)
	}
}

// TestFileChangesForRunTree pins the turn-end top-up's tracked write set: a
// primary run's own writes plus every descendant's (subagent runs share the
// conversation), never a sibling's, never another conversation's, and a
// deleted write is still a row the caller filters itself.
func TestFileChangesForRunTree(t *testing.T) {
	ctx := context.Background()
	db := openSessionDB(t)
	store := NewSessionStore(db, "main")
	require.NoError(t, store.Ensure(ctx, "s-tree", "s-tree"))
	require.NoError(t, store.Ensure(ctx, "s-other", "s-other"))
	insertRun(t, db, "root", "s-tree")
	insertChildRun(t, db, "child", "s-tree", "root")
	insertChildRun(t, db, "grandchild", "s-tree", "child")
	insertRun(t, db, "sibling", "s-tree")
	insertRun(t, db, "foreign", "s-other")
	for _, row := range []FileChange{
		{SessionID: "s-tree", RunID: "root", Path: "/repo/root.go"},
		{SessionID: "s-tree", RunID: "child", Path: "/repo/child.go", Deleted: true},
		{SessionID: "s-tree", RunID: "grandchild", Path: "/repo/grandchild.go"},
		{SessionID: "s-tree", RunID: "sibling", Path: "/repo/sibling.go"},
		{SessionID: "s-other", RunID: "foreign", Path: "/repo/foreign.go"},
	} {
		require.NoError(t, store.RecordFileChange(ctx, row))
	}
	rows, err := store.FileChangesForRunTree(ctx, "s-tree", "root")
	require.NoError(t, err)
	require.Len(t, rows, 3)
	require.Equal(t, "/repo/root.go", rows[0].Path)
	require.Equal(t, "/repo/child.go", rows[1].Path)
	require.True(t, rows[1].Deleted, "a deleted write is a row the caller filters")
	require.Equal(t, "/repo/grandchild.go", rows[2].Path)
	// A mid-tree run sees its own subtree only.
	sub, err := store.FileChangesForRunTree(ctx, "s-tree", "child")
	require.NoError(t, err)
	require.Len(t, sub, 2)
	// An unknown run seeds an empty tree.
	none, err := store.FileChangesForRunTree(ctx, "s-tree", "nope")
	require.NoError(t, err)
	require.Empty(t, none)
}

// TestRunTreeIDs pins the id list the top-up reads shell steps for: the run,
// its descendants, and nothing outside the conversation.
func TestRunTreeIDs(t *testing.T) {
	ctx := context.Background()
	db := openSessionDB(t)
	store := NewSessionStore(db, "main")
	require.NoError(t, store.Ensure(ctx, "s-tree", "s-tree"))
	require.NoError(t, store.Ensure(ctx, "s-other", "s-other"))
	insertRun(t, db, "root", "s-tree")
	insertChildRun(t, db, "child", "s-tree", "root")
	insertChildRun(t, db, "grandchild", "s-tree", "child")
	insertRun(t, db, "sibling", "s-tree")
	insertRun(t, db, "foreign", "s-other")
	ids, err := store.RunTreeIDs(ctx, "s-tree", "root")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"root", "child", "grandchild"}, ids)
	// A run id that belongs to another conversation grows no tree here.
	foreign, err := store.RunTreeIDs(ctx, "s-other", "root")
	require.NoError(t, err)
	require.Empty(t, foreign)
	empty, err := store.RunTreeIDs(ctx, "s-tree", "")
	require.NoError(t, err)
	require.Empty(t, empty)
}

// TestStateV9UpgradesToV10AddsRewindLedger builds a database that is really at
// v9 — fb_messages rejects 'rewound', the two new tables are absent — with
// live rows, and pins that opening it adds both tables, rebuilds fb_messages
// with every row and both indexes intact, and leaves foreign keys clean.
func TestStateV9UpgradesToV10AddsRewindLedger(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.sqlite")
	db, err := sql.Open(sqliteDriverName(), path)
	require.NoError(t, err)
	_, err = db.Exec(schemaSQL)
	require.NoError(t, err)
	// Drop back to the v9 shape: fb_messages rejects 'rewound', the two new
	// tables and their indexes are gone, version 9.
	for _, stmt := range []string{
		`DROP TABLE fb_file_changes`,
		`DROP TABLE fb_rewinds`,
		`PRAGMA legacy_alter_table = ON`,
		`ALTER TABLE fb_messages RENAME TO fb_messages_new`,
		fbMessagesDDLV9,
		`INSERT INTO fb_messages SELECT * FROM fb_messages_new`,
		`DROP TABLE fb_messages_new`,
		`CREATE INDEX idx_fb_messages_session_visibility ON fb_messages(session_id, visibility)`,
		`CREATE INDEX idx_fb_messages_run ON fb_messages(run_id) WHERE run_id IS NOT NULL`,
		`PRAGMA legacy_alter_table = OFF`,
		`PRAGMA user_version = 9`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	const sid = "s-v9"
	if _, err := db.Exec(dbx.Rebind(dbx.DialectOf(db), `INSERT INTO fb_sessions(id, agent_id, title, created_at, updated_at, cwd) VALUES(?,?,?,?,?,?)`),
		sid, "main", sid, 1, 1, "/repo/"+sid); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 200; i++ {
		if _, err := db.Exec(dbx.Rebind(dbx.DialectOf(db), `INSERT INTO fb_messages(session_id, role, content, created_at) VALUES(?,?,?,?)`),
			sid, "user", fmt.Sprintf("m-%d", i), 1); err != nil {
			t.Fatal(err)
		}
	}
	require.NoError(t, db.Close())

	migrated, err := Open(ctx, path, nil)
	require.NoError(t, err)
	defer migrated.Close()
	requireSchemaVersion(ctx, t, migrated, stateSchemaVersion)
	requireForeignKeysClean(ctx, t, migrated)

	var n int
	require.NoError(t, migrated.QueryRowContext(ctx, dbx.Rebind(dbx.DialectOf(migrated), `SELECT COUNT(*) FROM fb_messages WHERE session_id=?`), sid).Scan(&n))
	require.Equal(t, 200, n, "the rebuild copied every row")
	for _, idx := range []string{"idx_fb_messages_session_visibility", "idx_fb_messages_run"} {
		var c int
		require.NoError(t, migrated.QueryRowContext(ctx,
			dbx.Rebind(dbx.DialectOf(migrated), `SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`), idx).Scan(&c))
		require.Equal(t, 1, c, "index %s recreated", idx)
	}
	var ddl string
	require.NoError(t, migrated.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='fb_messages'`).Scan(&ddl))
	require.Contains(t, ddl, "'rewound'")
	// The new table accepts 'rewound'.
	_, err = migrated.ExecContext(ctx, dbx.Rebind(dbx.DialectOf(migrated), `UPDATE fb_messages SET visibility='rewound' WHERE session_id=? AND id=(SELECT MIN(id) FROM fb_messages WHERE session_id=?)`), sid, sid)
	require.NoError(t, err)
	for _, table := range []string{"fb_file_changes", "fb_rewinds"} {
		var c int
		require.NoError(t, migrated.QueryRowContext(ctx,
			dbx.Rebind(dbx.DialectOf(migrated), `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`), table).Scan(&c))
		require.Equal(t, 1, c, "table %s created", table)
	}
	require.NoError(t, migrated.Close())

	// A second open is a no-op: the migration tolerates the objects already
	// being final.
	again, err := Open(ctx, path, nil)
	require.NoError(t, err)
	defer again.Close()
	requireSchemaVersion(ctx, t, again, stateSchemaVersion)
	requireForeignKeysClean(ctx, t, again)
	if !strings.Contains(stripWhitespace(fbMessagesDDLV9), "withdrawn')") {
		t.Fatal("fixture drifted")
	}
}

func stripWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// A rewind hides its own conversation's events, and only its own. Sequences
// are global, so a session that keeps writing after another session's rewind
// holds events past that rewind's hidden range; an unqualified session term
// inside the subquery would bind to fb_rewinds itself and hide them too.
func TestRewindHidesNothingInAnotherSession(t *testing.T) {
	ctx := context.Background()
	db := openSessionDB(t)
	store := NewSessionStore(db, "main")
	runs := &RunStore{DB: db}
	require.NoError(t, store.Ensure(ctx, "s-rwd", "s-rwd"))
	require.NoError(t, store.Ensure(ctx, "s-other", "s-other"))
	insertRun(t, db, "r-rwd", "s-rwd")
	insertRun(t, db, "r-other", "s-other")

	// Give r-rwd one event before asking where its hidden range starts:
	// RewindEventStart answers 0 for a run with no events, and a rewind whose
	// range starts at 0 is inert under both the buggy and the fixed predicate
	// — the fixture would pass before the fix and prove nothing.
	_, err := runs.AppendSessionEvent(ctx, SessionEvent{
		ID: "evt-rwd", SessionID: "s-rwd", RunID: "r-rwd", Type: "tool_call_completed",
		Payload: json.RawMessage(`{"tool_name":"read_file"}`),
	})
	require.NoError(t, err)

	// s-rwd rewinds to before its first user message (pending, never fixed).
	anchor, err := store.AppendMessageForRun(ctx, "s-rwd", "r-rwd", "user", "q", "", "", MessageExecTiming{})
	require.NoError(t, err)
	start, err := store.RewindEventStart(ctx, "s-rwd", anchor)
	require.NoError(t, err)
	require.Greater(t, start, int64(0), "a rewind with a zero range hides nothing, in any session")
	require.NoError(t, store.InsertRewind(ctx, RewindRecord{
		ID: "rw-cross", SessionID: "s-rwd", AnchorMessageID: anchor, Mode: "both",
		HiddenEventSeqFrom: start, State: "pending", CreatedAtMs: 1,
	}))

	// s-other writes after that, so its event's sequence is past the range.
	evt, err := runs.AppendSessionEvent(ctx, SessionEvent{
		ID: "evt-other", SessionID: "s-other", RunID: "r-other", Type: "tool_call_completed",
		Payload: json.RawMessage(`{"tool_name":"read_file"}`),
	})
	require.NoError(t, err)

	page, err := runs.ListSessionEvents(ctx, "s-other", 0, 0, 50)
	require.NoError(t, err)
	require.Len(t, page.Events, 1, "another conversation's rewind hid this session's events")
	require.Equal(t, evt.Sequence, page.Events[0].Sequence)

	high, err := runs.SessionEventHighWater(ctx, "s-other")
	require.NoError(t, err)
	require.Equal(t, evt.Sequence, high, "the high-water cursor must count this session's own events")

	byTool, total, err := runs.CountSessionToolCalls(ctx, "s-other")
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Equal(t, 1, byTool["read_file"])
}

// appendUserRow stores one user row, stamps it with createdAt, and returns
// its id.
func appendUserRow(t *testing.T, db *sql.DB, store *SessionStore, sid, text string, meta bool, createdAt int64) int64 {
	t.Helper()
	ctx := context.Background()
	msg := llm.UserMessage(llm.Text(text))
	msg.IsMeta = meta
	require.NoError(t, store.AppendNewMessages(ctx, sid, "", []llm.Message{msg}, "", ""))
	var id int64
	require.NoError(t, db.QueryRowContext(ctx,
		dbx.Rebind(dbx.DialectOf(db), `SELECT MAX(id) FROM fb_messages WHERE session_id=?`), sid).Scan(&id))
	if createdAt > 0 {
		_, err := db.ExecContext(ctx, dbx.Rebind(dbx.DialectOf(db), `UPDATE fb_messages SET created_at=? WHERE id=?`), createdAt, id)
		require.NoError(t, err)
	}
	return id
}

// The runtime injects meta user rows — a turn can inject dozens in a row — and
// the limit must count the person's messages, not rows, or the injected rows
// crowd the messages out of the window.
func TestListVisibleUserMessagesLimitCountsMessagesNotMetaRows(t *testing.T) {
	ctx := context.Background()
	db := openSessionDB(t)
	store := NewSessionStore(db, "main")
	require.NoError(t, store.Ensure(ctx, "s1", "s1"))
	for i := 0; i < 3; i++ {
		appendUserRow(t, db, store, "s1", fmt.Sprintf("message %d", i), false, 0)
		for j := 0; j < 10; j++ {
			appendUserRow(t, db, store, "s1", "<system-reminder>injected</system-reminder>", true, 0)
		}
	}

	two, err := store.ListVisibleUserMessages(ctx, "s1", 2)
	require.NoError(t, err)
	require.Len(t, two, 2)
	require.Equal(t, "message 2", two[0].Content)
	require.Equal(t, "message 1", two[1].Content)

	all, err := store.ListVisibleUserMessages(ctx, "s1", 0)
	require.NoError(t, err)
	require.Len(t, all, 3, "a non-positive limit reads every message")
}

// A turn the runtime started (a reminder, a peer's message, a hook) can stack
// more meta rows after the person's last message than any fixed row window.
func TestLatestUserMessageRowIDSeesPastALongRunOfMetaRows(t *testing.T) {
	ctx := context.Background()
	db := openSessionDB(t)
	store := NewSessionStore(db, "main")
	require.NoError(t, store.Ensure(ctx, "s1", "s1"))
	want := appendUserRow(t, db, store, "s1", "fix the installer", false, 0)
	for i := 0; i < 60; i++ {
		appendUserRow(t, db, store, "s1", "<system-reminder>injected</system-reminder>", true, 0)
	}

	got, err := store.LatestUserMessageRowID(ctx, "s1")
	require.NoError(t, err)
	require.Equal(t, want, got)
}
