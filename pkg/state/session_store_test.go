package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

func TestWithdrawUserTurn(t *testing.T) {
	db := openStateDB(t)
	store := NewSessionStore(db, "owner")
	ctx := context.Background()
	appendRow := func(session, role, text string) int64 {
		t.Helper()
		id, err := store.Append(ctx, session, role, text)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	appendRow("s", "user", "before")
	id := appendRow("s", "user", "mistake")
	assistant := appendRow("s", "assistant", "unrelated later write")
	other := appendRow("other", "user", "mistake")
	for _, tc := range []struct {
		session string
		id      int64
	}{
		{"other", id}, {"s", other}, {"s", assistant}, {"", id}, {"s", 0}, {"s", id + 100},
	} {
		if err := store.WithdrawUserTurn(ctx, tc.session, tc.id); err == nil {
			t.Fatalf("accepted wrong identity: %+v", tc)
		}
	}
	if err := NewSessionStore(db, "stranger").WithdrawUserTurn(ctx, "s", id); !errors.Is(err, ErrSessionNotOwned) {
		t.Fatalf("tenant gate: %v", err)
	}
	for range 2 {
		if err := store.WithdrawUserTurn(ctx, "s", id); err != nil {
			t.Fatal(err)
		}
	}
	// Reopening the store must use the same exclusion in both readers.
	reopened := NewSessionStore(db, "owner")
	visible, err := reopened.ListRecentMessages(ctx, "s", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(visible) != 2 || visible[0].Content != "before" || visible[1].Content != "unrelated later write" {
		t.Fatalf("visible transcript: %+v", visible)
	}
	model, err := reopened.ListTranscriptMessages(ctx, "s", 20)
	if err != nil || len(model) != 2 {
		t.Fatalf("model transcript: %+v, %v", model, err)
	}
	var visibility string
	if err := db.QueryRow("SELECT visibility FROM fb_messages WHERE id=?", id).Scan(&visibility); err != nil || visibility != "withdrawn" {
		t.Fatalf("audit row: %q, %v", visibility, err)
	}
	rows, err := reopened.ListRecentMessages(ctx, "other", 20)
	if err != nil || len(rows) != 1 {
		t.Fatalf("other session changed: %+v, %v", rows, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := store.WithdrawUserTurn(cancelled, "s", id); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled storage operation: %v", err)
	}
}

// TestWithdrawUserTurnReleasesSessionTitle covers the residue that survives
// outside the transcript: a session is named after its first visible user
// message, so withdrawing that message must take the name back. Otherwise the
// text the user retracted keeps naming the session in /resume, in the session
// list and in the terminal window title — permanently, if they never send
// another message.
func TestWithdrawUserTurnReleasesSessionTitle(t *testing.T) {
	ctx := context.Background()
	titleOf := func(store *SessionStore, session string) string {
		t.Helper()
		rows, err := store.ListSessionsRecent(ctx, 50)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.ID == session {
				return row.Title
			}
		}
		t.Fatalf("session %q not listed", session)
		return ""
	}

	t.Run("only message", func(t *testing.T) {
		store := NewSessionStore(openStateDB(t), "owner")
		id, err := store.Append(ctx, "s", "user", "send the wrong thing")
		if err != nil {
			t.Fatal(err)
		}
		if got := titleOf(store, "s"); got != "send the wrong thing" {
			t.Fatalf("title before withdrawal = %q", got)
		}
		if err := store.WithdrawUserTurn(ctx, "s", id); err != nil {
			t.Fatal(err)
		}
		if got := titleOf(store, "s"); got != "s" {
			t.Fatalf("withdrawn text still names the session: %q", got)
		}
	})

	t.Run("re-derives from the next visible message", func(t *testing.T) {
		store := NewSessionStore(openStateDB(t), "owner")
		id, err := store.Append(ctx, "s", "user", "send the wrong thing")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Append(ctx, "s", "user", "the real question"); err != nil {
			t.Fatal(err)
		}
		if err := store.WithdrawUserTurn(ctx, "s", id); err != nil {
			t.Fatal(err)
		}
		if got := titleOf(store, "s"); got != "the real question" {
			t.Fatalf("title = %q, want the first surviving user message", got)
		}
	})

	t.Run("leaves a title it did not generate", func(t *testing.T) {
		store := NewSessionStore(openStateDB(t), "owner")
		id, err := store.Append(ctx, "s", "user", "send the wrong thing")
		if err != nil {
			t.Fatal(err)
		}
		if err := store.SetTitle(ctx, "s", "named by hand"); err != nil {
			t.Fatal(err)
		}
		if err := store.WithdrawUserTurn(ctx, "s", id); err != nil {
			t.Fatal(err)
		}
		if got := titleOf(store, "s"); got != "named by hand" {
			t.Fatalf("clobbered a title the withdrawn message did not set: %q", got)
		}
	})
}

// TestWithdrawUserTurnKeepsPromptPrefixStable is the prompt-cache guard. The
// withdrawn row is the last block of the rendered prefix, so withdrawing it must
// leave every earlier block byte-identical: the next request then still matches
// the provider's cached prefix all the way up to the withdrawn message and forks
// only at the final message block. A withdrawal that renumbered, reordered or
// rewrote surrounding history would fork it much earlier and re-bill the whole
// conversation.
func TestWithdrawUserTurnKeepsPromptPrefixStable(t *testing.T) {
	ctx := context.Background()
	db := openStateDB(t)
	store := NewSessionStore(db, "owner")
	for _, row := range []struct{ role, text string }{
		{"user", "first question"},
		{"assistant", "first answer"},
		{"user", "second question"},
		{"assistant", "second answer"},
	} {
		if _, err := store.Append(ctx, "s", row.role, row.text); err != nil {
			t.Fatal(err)
		}
	}
	before, err := store.ListTranscriptMessages(ctx, "s", 100)
	if err != nil {
		t.Fatal(err)
	}
	id, err := store.Append(ctx, "s", "user", "the message taken back")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WithdrawUserTurn(ctx, "s", id); err != nil {
		t.Fatal(err)
	}
	after, err := store.ListTranscriptMessages(ctx, "s", 100)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("prompt prefix changed across a withdrawal:\nbefore=%+v\nafter =%+v", before, after)
	}
	// Row identity of the surviving history is part of the same invariant: the
	// rows the next request renders must be the same rows, not rewritten copies.
	rows, err := db.Query(`SELECT id, content FROM fb_messages
		WHERE session_id='s' AND visibility='visible' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var surviving []string
	for rows.Next() {
		var rowID int64
		var content string
		if err := rows.Scan(&rowID, &content); err != nil {
			t.Fatal(err)
		}
		surviving = append(surviving, fmt.Sprintf("%d:%s", rowID, content))
	}
	want := []string{"1:first question", "2:first answer", "3:second question", "4:second answer"}
	if !reflect.DeepEqual(surviving, want) {
		t.Fatalf("surviving rows = %v, want %v", surviving, want)
	}
}

// TestListSessionsRecentPaged verifies the LIMIT/OFFSET contract the /resume
// picker relies on: pages tile the history newest-first without gaps or
// duplicates, a short page marks the end, an offset past the end is empty,
// and another primary agent's sessions never leak in.
func TestListSessionsRecentPaged(t *testing.T) {
	db := openStateDB(t)
	acme := NewSessionStore(db, "acme")
	globex := NewSessionStore(db, "globex")
	ctx := context.Background()

	const total = 45
	for i := 0; i < total; i++ {
		id := fmt.Sprintf("s-%02d", i)
		if err := acme.Ensure(ctx, id, "session "+id); err != nil {
			t.Fatal(err)
		}
		// Distinct, descending timestamps so the newest-first order is
		// deterministic: s-00 is the most recent session.
		if _, err := db.Exec("UPDATE fb_sessions SET updated_at = ? WHERE id = ?", int64(10_000-i), id); err != nil {
			t.Fatal(err)
		}
	}

	var wantIDs []string
	for i := 0; i < total; i++ {
		wantIDs = append(wantIDs, fmt.Sprintf("s-%02d", i))
	}

	collect := func(limit int) []string {
		var ids []string
		for offset := 0; ; offset += limit {
			page, err := acme.ListSessionsRecentPaged(ctx, limit, offset)
			if err != nil {
				t.Fatal(err)
			}
			if len(page) == 0 {
				break
			}
			for _, row := range page {
				ids = append(ids, row.ID)
			}
		}
		return ids
	}

	got := collect(20)
	if len(got) != total {
		t.Fatalf("paged through %d sessions, want %d", len(got), total)
	}
	for i := range wantIDs {
		if got[i] != wantIDs[i] {
			t.Fatalf("row %d = %q, want %q", i, got[i], wantIDs[i])
		}
	}

	// A page boundary that is not a multiple of the page size must still
	// line up with the fixed-size pages.
	mixed := collect(13)
	for i := range wantIDs {
		if mixed[i] != wantIDs[i] {
			t.Fatalf("mixed page size row %d = %q, want %q", i, mixed[i], wantIDs[i])
		}
	}

	// Paged listing honors primary-agent tenancy exactly like the flat list.
	leaked, err := globex.ListSessionsRecentPaged(ctx, 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(leaked) != 0 {
		t.Fatalf("globex paged listing returned %d of acme's sessions", len(leaked))
	}
}

// TestConversationListsAreFilteredInTheQuery pins that the purpose filter is
// part of the query itself: however many scheduled-task fires or project
// sessions pile on top of the agent's own conversation, none of them can
// crowd it out of a list.
func TestConversationListsAreFilteredInTheQuery(t *testing.T) {
	ctx := context.Background()
	db := openStateDB(t)
	store := NewSessionStore(db, "main")

	if err := store.Ensure(ctx, "ordinary", "the conversation"); err != nil {
		t.Fatal(err)
	}
	// The ordinary conversation is the oldest row of all.
	if _, err := db.Exec("UPDATE fb_sessions SET updated_at = 1 WHERE id = 'ordinary'"); err != nil {
		t.Fatal(err)
	}
	projects := NewProjectStore(db, "main")
	p, err := projects.Create(ctx, CreateProjectInput{Name: "proj", Root: "/tmp/proj"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 250; i++ {
		cronID := fmt.Sprintf("cron-%03d", i)
		projID := fmt.Sprintf("inproj-%03d", i)
		if err := store.Ensure(ctx, cronID, cronID); err != nil {
			t.Fatal(err)
		}
		if err := store.SetSessionSource(ctx, cronID, SessionSourceCron); err != nil {
			t.Fatal(err)
		}
		if err := store.Ensure(ctx, projID, projID); err != nil {
			t.Fatal(err)
		}
		if err := projects.BindSession(ctx, projID, p.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("UPDATE fb_sessions SET updated_at = ? WHERE id IN (?, ?)", int64(100+i), cronID, projID); err != nil {
			t.Fatal(err)
		}
	}

	own, err := store.ListSessionsOfSource(ctx, SessionSourceConversation, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(own) != 1 || own[0].ID != "ordinary" {
		t.Fatalf("purpose-filtered list = %+v, want only the ordinary conversation", own)
	}

	recent, err := store.ListSessionsRecent(ctx, 500)
	if err != nil {
		t.Fatal(err)
	}
	paged, err := store.ListSessionsRecentPaged(ctx, 500, 0)
	if err != nil {
		t.Fatal(err)
	}
	for name, list := range map[string][]SessionSummary{
		"ListSessionsRecent":      recent,
		"ListSessionsRecentPaged": paged,
	} {
		var saw bool
		for _, row := range list {
			if row.Source == SessionSourceCron {
				t.Fatalf("%s returned a scheduled-task fire: %+v", name, row)
			}
			if row.ID == "ordinary" {
				saw = true
			}
		}
		if !saw {
			t.Fatalf("%s lost the ordinary conversation under newer rows", name)
		}
	}
}

// TestProjectSessionListListsOnlyConversations pins the project list to
// conversations: a scheduled task's fire bound to the project is reached
// from the project's scheduled-tasks tab, not from its conversation list.
func TestProjectSessionListListsOnlyConversations(t *testing.T) {
	ctx := context.Background()
	projects, sessions := newProjectStoreForTest(t, "main")
	p, err := projects.Create(ctx, CreateProjectInput{Name: "proj", Root: "/tmp/proj"})
	if err != nil {
		t.Fatal(err)
	}
	if err := sessions.Ensure(ctx, "talk", "a conversation"); err != nil {
		t.Fatal(err)
	}
	if err := sessions.Ensure(ctx, "fire", "one fire"); err != nil {
		t.Fatal(err)
	}
	if err := sessions.SetSessionSource(ctx, "fire", SessionSourceCron); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"talk", "fire"} {
		if err := projects.BindSession(ctx, id, p.ID); err != nil {
			t.Fatal(err)
		}
	}

	got, err := sessions.ListSessionsForProject(ctx, p.ID, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "talk" {
		t.Fatalf("project session list = %+v, want only the conversation", got)
	}
}

func newPromptStateTestStore(t *testing.T) *SessionStore {
	t.Helper()
	return NewSessionStore(openStateDB(t), "main")
}

// Prompt state is what a session injects ahead of its conversation, so the
// first value written is the one the provider cached: a later render loses.
func TestSessionPromptStateFreezesTheFirstValue(t *testing.T) {
	ctx := context.Background()
	store := newPromptStateTestStore(t)
	if err := store.Ensure(ctx, "s1", "first"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.SessionPromptState(ctx, "s1", "skill_catalog"); err != nil || ok {
		t.Fatalf("unfrozen state reported as frozen: ok=%v err=%v", ok, err)
	}
	frozen, ok, err := store.FreezeSessionPromptState(ctx, "s1", "skill_catalog", "catalog v1")
	if err != nil {
		t.Fatal(err)
	}
	if frozen != "catalog v1" || !ok {
		t.Fatalf("froze %q ok=%v", frozen, ok)
	}
	again, ok, err := store.FreezeSessionPromptState(ctx, "s1", "skill_catalog", "catalog v2")
	if err != nil {
		t.Fatal(err)
	}
	if again != "catalog v1" || !ok {
		t.Fatalf("a later render replaced the frozen value: %q ok=%v", again, ok)
	}
	value, ok, err := store.SessionPromptState(ctx, "s1", "skill_catalog")
	if err != nil || !ok || value != "catalog v1" {
		t.Fatalf("read back %q ok=%v err=%v", value, ok, err)
	}

	// An empty value is a real frozen value — a session with no skills must not
	// re-render a catalog on every request.
	if _, ok, err := store.FreezeSessionPromptState(ctx, "s1", "memory_instruction", ""); err != nil || !ok {
		t.Fatalf("empty value not frozen: ok=%v err=%v", ok, err)
	}
	if value, ok, err := store.SessionPromptState(ctx, "s1", "memory_instruction"); err != nil || !ok || value != "" {
		t.Fatalf("empty frozen value read back as %q ok=%v err=%v", value, ok, err)
	}
}

// Prompt state belongs to a session. A run with no session row has nothing to
// stay identical across, and must not leave rows behind for one.
func TestSessionPromptStateNeedsASession(t *testing.T) {
	ctx := context.Background()
	store := newPromptStateTestStore(t)
	frozen, ok, err := store.FreezeSessionPromptState(ctx, "ghost", "skill_catalog", "catalog v1")
	if err != nil {
		t.Fatal(err)
	}
	if frozen != "catalog v1" {
		t.Fatalf("caller's value not returned: %q", frozen)
	}
	// The caller must learn that nothing was frozen, or it renders again on
	// every request and re-bills the whole prefix each time.
	if ok {
		t.Fatal("a session that does not exist reported a frozen value")
	}
	if _, ok, err := store.SessionPromptState(ctx, "ghost", "skill_catalog"); err != nil || ok {
		t.Fatalf("stored state for a session that does not exist: ok=%v err=%v", ok, err)
	}
}

// TestLastActiveByIDs pins the shape a teardown decision depends on: exact ids
// in (never a table scan), rows for this agent's sessions only, ids without a
// row absent rather than zero — an absent conversation and a never-created one
// read the same, and the caller treats both as "cannot hold a runner open".
func TestLastActiveByIDs(t *testing.T) {
	db := openStateDB(t)
	ours := NewSessionStore(db, "owner")
	theirs := NewSessionStore(db, "other")
	ctx := context.Background()
	if _, err := ours.Append(ctx, "live", "user", "hello"); err != nil {
		t.Fatal(err)
	}
	if _, err := ours.Append(ctx, "older", "user", "hi"); err != nil {
		t.Fatal(err)
	}
	if _, err := theirs.Append(ctx, "foreign", "user", "not ours"); err != nil {
		t.Fatal(err)
	}

	got, err := ours.LastActiveByIDs(ctx, []string{"live", "older", "foreign", "never-created", "  "})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("LastActiveByIDs = %v, want exactly this agent's two sessions", got)
	}
	live, older := got["live"], got["older"]
	if live == 0 || older == 0 {
		t.Fatalf("timestamps = %v, want real updated_at values", got)
	}
	if live < older {
		t.Fatalf("a later write must not read as older: live=%d older=%d", live, older)
	}
	if _, ok := got["foreign"]; ok {
		t.Fatal("another agent's session leaked through the agent boundary")
	}
	if _, ok := got["never-created"]; ok {
		t.Fatal("an id with no row must be absent, not zero")
	}

	// The chunk boundary is part of the contract: one caller's id list works
	// past the query's IN-list cap without error or loss.
	many := make([]string, 0, sessionLastActiveChunk+50)
	for i := 0; i < sessionLastActiveChunk+50; i++ {
		many = append(many, fmt.Sprintf("bulk-%d", i))
	}
	if _, err := ours.Append(ctx, "bulk-0", "user", "x"); err != nil {
		t.Fatal(err)
	}
	got, err = ours.LastActiveByIDs(ctx, many)
	if err != nil {
		t.Fatalf("chunked query: %v", err)
	}
	if len(got) != 1 || got["bulk-0"] == 0 {
		t.Fatalf("chunked query lost rows across the cap: %v", got)
	}

	if got, err := ours.LastActiveByIDs(ctx, nil); err != nil || got == nil {
		t.Fatalf("empty input = %v, %v; want an empty map and no error", got, err)
	}
}

// A fork is the same conversation as stored: the model is sent exactly what it
// was sent from the source — the compaction checkpoint and what follows it,
// tool calls and their results — the recorded prompt state comes along so the
// prefix stays byte-identical, and the source is left untouched.
func TestForkIntoCopiesTheConversationAsStored(t *testing.T) {
	db := openStateDB(t)
	store := NewSessionStore(db, "owner")
	ctx := context.Background()
	must := func(_ int64, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(store.Append(ctx, "src", "user", "before the checkpoint"))
	if err := store.AppendCompactCheckpoint(ctx, "src", "summary", CompactBoundaryPart{
		ReplacementHistory: []llm.Message{llm.UserMessage(llm.Text("summary of what came before"))},
		WindowNumber:       1,
		WindowID:           "w1",
	}); err != nil {
		t.Fatal(err)
	}
	must(store.Append(ctx, "src", "user", "run the tests"))
	must(store.AppendStructuredMessage(ctx, "src", "assistant", "", "m-1", `[{"type":"tool_call","id":"call-1","name":"shell","arguments":"{\"cmd\":\"go test\"}"}]`, "gpt", "", "call-1", `{"tool_name":"shell"}`, MessageExecTiming{}))
	must(store.AppendStructuredMessage(ctx, "src", "tool", "ok", "m-2", `[{"type":"tool_result","tool_call_id":"call-1","text":"ok"}]`, "", "", "call-1", "", MessageExecTiming{}))
	withdrawnRow, err := store.Append(ctx, "src", "user", "withdrawn")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WithdrawUserTurn(ctx, "src", withdrawnRow); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.FreezeSessionPromptState(ctx, "src", "developer", "stable prefix"); err != nil {
		t.Fatal(err)
	}
	if err := store.Ensure(ctx, "fork", "Fork of src"); err != nil {
		t.Fatal(err)
	}

	if err := store.ForkInto(ctx, "src", "fork"); err != nil {
		t.Fatal(err)
	}

	want, err := store.ListTranscriptMessages(ctx, "src", 0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.ListTranscriptMessages(ctx, "fork", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(want) == 0 || !reflect.DeepEqual(got, want) {
		t.Fatalf("fork sends\n%+v\nsource sends\n%+v", got, want)
	}
	_, part, err := store.LatestCompactBoundary(ctx, "fork")
	if err != nil || part.WindowID != "w1" {
		t.Fatalf("fork's checkpoint = %+v, %v", part, err)
	}
	if value, ok, err := store.SessionPromptState(ctx, "fork", "developer"); err != nil || !ok || value != "stable prefix" {
		t.Fatalf("fork's prompt state = %q, %v, %v", value, ok, err)
	}
	var srcRows, forkRows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM fb_messages WHERE session_id='src'`).Scan(&srcRows); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM fb_messages WHERE session_id='fork'`).Scan(&forkRows); err != nil {
		t.Fatal(err)
	}
	if srcRows != forkRows {
		t.Fatalf("fork holds %d rows, source %d", forkRows, srcRows)
	}
	if err := NewSessionStore(db, "stranger").ForkInto(ctx, "src", "theirs"); !errors.Is(err, ErrSessionNotOwned) {
		t.Fatalf("another agent forked this one's conversation: %v", err)
	}
}

// buildSessionIDWithLatestFixture builds the healthy shape the picker's choice
// rests on — sessions whose updated_at tracks their newest message, one with
// no messages at all, and one hidden (withdrawn) message that must not count —
// in the v0 shape, then returns its path.
func buildSessionIDWithLatestFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "picker.sqlite")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	schema, err := os.ReadFile(filepath.Join("testdata", "legacy_v0_schema.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`INSERT INTO fb_sessions(id, agent_id, title, updated_at, created_at) VALUES('s-old','main','old',1500,1000)`,
		`INSERT INTO fb_sessions(id, agent_id, title, updated_at, created_at) VALUES('s-new','main','new',3000,2500)`,
		`INSERT INTO fb_sessions(id, agent_id, title, updated_at, created_at) VALUES('s-empty','main','empty',4000,3500)`,
		`INSERT INTO fb_messages(session_id, run_id, role, content, created_at, source) VALUES('s-old','','user','old question',1500,'transcript')`,
		`INSERT INTO fb_messages(session_id, run_id, role, content, created_at, source) VALUES('s-new','','user','new question',2900,'transcript')`,
		`INSERT INTO fb_messages(session_id, run_id, role, content, created_at, source) VALUES('s-new','','user','taken back',3000,'transcript_withdrawn')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("fixture %q: %v", stmt, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestSessionIDWithLatestMessageMatchesRowScan pins that the indexed rewrite
// of SessionIDWithLatestMessage (§2.5 S1) picks the same session the row-scan
// semantics did: the conversation holding the agent's newest visible message.
// The withdrawn row and the empty session never win.
func TestSessionIDWithLatestMessageMatchesRowScan(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, buildSessionIDWithLatestFixture(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// The row scan the rewrite replaced, spelled for whichever visibility
	// column this database shape carries.
	var visible string
	if err := db.QueryRowContext(ctx,
		`SELECT CASE WHEN EXISTS(SELECT 1 FROM pragma_table_info('fb_messages') WHERE name='visibility') THEN 'visibility' ELSE 'source' END`).
		Scan(&visible); err != nil {
		t.Fatal(err)
	}
	equivalent := "transcript"
	if visible == "visibility" {
		equivalent = "visible"
	}
	byRowScan := func() string {
		var sid sql.NullString
		err := db.QueryRowContext(ctx,
			`SELECT m.session_id FROM fb_messages m
			 JOIN fb_sessions s ON s.id = m.session_id AND s.agent_id = ?
			 WHERE IFNULL(TRIM(m.session_id), '') != ''
			   AND m.`+visible+` = ?
			 GROUP BY m.session_id
			 ORDER BY MAX(m.created_at) DESC
			 LIMIT 1`, "main", equivalent).Scan(&sid)
		if err == sql.ErrNoRows {
			return ""
		}
		if err != nil {
			t.Fatal(err)
		}
		return sid.String
	}

	got, err := NewSessionStore(db, "main").SessionIDWithLatestMessage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != "s-new" {
		t.Fatalf("SessionIDWithLatestMessage = %q, want s-new", got)
	}
	if want := byRowScan(); got != want {
		t.Fatalf("picker = %q, the row-scan semantics it replaced picked %q", got, want)
	}
}

func TestSessionModelSelectionRoundTripAndOverwrite(t *testing.T) {
	ctx := context.Background()
	store := NewSessionStore(openStateDB(t), "owner")

	if stored := saveModelSelectionWithSession(t, store, "s", SessionModelSelection{Provider: " openai ", Model: " gpt-a ", Effort: " "}); !stored {
		t.Fatal("first save reported it stored nothing")
	}
	got, ok, err := store.SessionModelSelection(ctx, "s")
	if err != nil || !ok {
		t.Fatalf("read back: ok=%v err=%v", ok, err)
	}
	// Values are trimmed; an empty effort is stored as a concrete empty value.
	if got.Provider != "openai" || got.Model != "gpt-a" || got.Effort != "" {
		t.Fatalf("round trip = %+v", got)
	}

	if stored := saveModelSelectionWithSession(t, store, "s", SessionModelSelection{Provider: "anthropic", Model: "claude-b", Effort: "high"}); !stored {
		t.Fatal("overwrite reported it stored nothing")
	}
	got, ok, err = store.SessionModelSelection(ctx, "s")
	if err != nil || !ok {
		t.Fatalf("read overwrite: ok=%v err=%v", ok, err)
	}
	if got.Provider != "anthropic" || got.Model != "claude-b" || got.Effort != "high" {
		t.Fatalf("overwrite = %+v", got)
	}

	if _, ok, err := store.SessionModelSelection(ctx, "no-row"); err != nil || ok {
		t.Fatalf("session without selection: ok=%v err=%v", ok, err)
	}
}

func TestSessionModelSelectionSaveNeedsASessionRow(t *testing.T) {
	ctx := context.Background()
	store := NewSessionStore(openStateDB(t), "owner")
	stored, err := store.SaveSessionModelSelection(ctx, "ghost", SessionModelSelection{Provider: "openai", Model: "gpt-a"})
	if err != nil {
		t.Fatalf("save without session row: %v", err)
	}
	if stored {
		t.Fatal("a session that does not exist reported a stored selection")
	}
	if _, err := store.SaveSessionModelSelection(ctx, "s", SessionModelSelection{Provider: "", Model: "m"}); err == nil {
		t.Fatal("accepted an empty provider")
	}
	if _, err := store.SaveSessionModelSelection(ctx, "s", SessionModelSelection{Provider: "p", Model: " "}); err == nil {
		t.Fatal("accepted an empty model")
	}
}

func TestSessionModelSelectionIsTenantData(t *testing.T) {
	ctx := context.Background()
	db := openStateDB(t)
	ours := NewSessionStore(db, "owner")
	theirs := NewSessionStore(db, "other")
	saveModelSelectionWithSession(t, ours, "s", SessionModelSelection{Provider: "openai", Model: "gpt-a", Effort: "low"})
	if err := theirs.Ensure(ctx, "their-session", "their-session"); err != nil {
		t.Fatal(err)
	}

	if _, err := theirs.SaveSessionModelSelection(ctx, "s", SessionModelSelection{Provider: "x", Model: "y"}); err == nil {
		t.Fatal("another agent wrote our session's model state")
	}
	if _, _, err := theirs.SessionModelSelection(ctx, "s"); err == nil {
		t.Fatal("another agent read our session's model state")
	}
	if _, err := theirs.CopySessionModelSelection(ctx, "s", "their-session"); err == nil {
		t.Fatal("another agent copied from our session")
	}
	if _, err := ours.CopySessionModelSelection(ctx, "s", "their-session"); err == nil {
		t.Fatal("copy into another agent's session was not rejected by policy")
	}
	// The guard on the copy is ownership, not the absence of a row: our own
	// missing target is rejected the same way.
	if _, err := ours.CopySessionModelSelection(ctx, "s", "ghost"); err == nil {
		t.Fatal("copy into a missing session was not rejected by policy")
	}
}

func TestSessionModelSelectionCopyAndCascade(t *testing.T) {
	ctx := context.Background()
	store := NewSessionStore(openStateDB(t), "owner")
	saveModelSelectionWithSession(t, store, "src", SessionModelSelection{Provider: "openai", Model: "gpt-a", Effort: ""})
	saveModelSelectionWithSession(t, store, "dst", SessionModelSelection{Provider: "old", Model: "stale", Effort: "low"})

	copied, err := store.CopySessionModelSelection(ctx, "src", "dst")
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if !copied {
		t.Fatal("copy reported nothing copied")
	}
	got, ok, err := store.SessionModelSelection(ctx, "dst")
	if err != nil || !ok {
		t.Fatalf("read copied selection: ok=%v err=%v", ok, err)
	}
	if got.Provider != "openai" || got.Model != "gpt-a" || got.Effort != "" {
		t.Fatalf("copied selection = %+v", got)
	}

	// A source without a model row copies nothing and is not an error.
	if err := store.Ensure(ctx, "empty-src", "empty-src"); err != nil {
		t.Fatal(err)
	}
	copied, err = store.CopySessionModelSelection(ctx, "empty-src", "dst")
	if err != nil {
		t.Fatalf("copy from a session with no selection: %v", err)
	}
	if copied {
		t.Fatal("copy from a session with no selection reported a copy")
	}
	// The failed copy must not have clobbered the target's row.
	if got, ok, _ := store.SessionModelSelection(ctx, "dst"); !ok || got.Model != "gpt-a" {
		t.Fatalf("target row after a no-op copy = %+v ok=%v", got, ok)
	}

	// Deleting a session takes its model state with it.
	if _, err := store.db.ExecContext(ctx, `DELETE FROM fb_sessions WHERE id='dst'`); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.SessionModelSelection(ctx, "dst"); err != nil || ok {
		t.Fatalf("model state survived its session: ok=%v err=%v", ok, err)
	}
}

func saveModelSelectionWithSession(t *testing.T, store *SessionStore, session string, sel SessionModelSelection) (stored bool) {
	t.Helper()
	ctx := context.Background()
	if err := store.Ensure(ctx, session, session); err != nil {
		t.Fatal(err)
	}
	stored, err := store.SaveSessionModelSelection(ctx, session, sel)
	if err != nil {
		t.Fatalf("SaveSessionModelSelection(%s): %v", session, err)
	}
	return stored
}

// TestSessionBirthIsTheCreatorsAndNeverMovesTheDefault pins the fix for the
// defect where opening a project session rewrote the store-wide default cwd:
// the project's session is born in the project root, and the next ordinary
// session is still born in the process's launch directory.
func TestSessionBirthIsTheCreatorsAndNeverMovesTheDefault(t *testing.T) {
	db := openStateDB(t)
	store := NewSessionStore(db, "owner")
	ctx := context.Background()
	store.ConfigureMemoryDefaults("disabled", "webchat", "/launch", "main")

	if err := store.EnsureAt(ctx, "p1", "p1", SessionBirth{Cwd: "/proj", GitBranch: "feat"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Ensure(ctx, "a1", "a1"); err != nil {
		t.Fatal(err)
	}

	birthOf := func(id string) (cwd, branch string) {
		t.Helper()
		if err := db.QueryRow("SELECT cwd, git_branch FROM fb_sessions WHERE id=?", id).Scan(&cwd, &branch); err != nil {
			t.Fatal(err)
		}
		return cwd, branch
	}
	if cwd, branch := birthOf("p1"); cwd != "/proj" || branch != "feat" {
		t.Fatalf("project session born at %q on %q, want /proj on feat", cwd, branch)
	}
	if cwd, branch := birthOf("a1"); cwd != "/launch" || branch != "main" {
		t.Fatalf("later session born at %q on %q, want the untouched default /launch on main", cwd, branch)
	}
}

// TestSessionBirthKeepsCwdAndBranchTogether pins that Cwd and GitBranch are
// decided as a pair: a session born without a directory takes both process
// defaults, never a default directory paired with a caller-supplied branch.
func TestSessionBirthKeepsCwdAndBranchTogether(t *testing.T) {
	db := openStateDB(t)
	store := NewSessionStore(db, "owner")
	ctx := context.Background()
	store.ConfigureMemoryDefaults("disabled", "webchat", "/launch", "main")

	if err := store.EnsureAt(ctx, "p1", "p1", SessionBirth{}); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureAt(ctx, "p2", "p2", SessionBirth{GitBranch: "feat"}); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"p1", "p2"} {
		var cwd, branch string
		if err := db.QueryRow("SELECT cwd, git_branch FROM fb_sessions WHERE id=?", id).Scan(&cwd, &branch); err != nil {
			t.Fatal(err)
		}
		if cwd != "/launch" || branch != "main" {
			t.Fatalf("session %s born at %q on %q, want the pair /launch on main", id, cwd, branch)
		}
	}
}

// TestSessionBirthNamesWhatTheSessionIsFor pins that Source is a birth fact
// like the owning agent: a session born a cron session is one for good — a
// later write that re-ensures the row does not turn it back into an ordinary
// conversation.
func TestSessionBirthNamesWhatTheSessionIsFor(t *testing.T) {
	db := openStateDB(t)
	store := NewSessionStore(db, "owner")
	ctx := context.Background()

	if err := store.EnsureAt(ctx, "cron-job-1-1", "cron-job-1-1", SessionBirth{Source: SessionSourceCron}); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureAt(ctx, "plain", "plain", SessionBirth{}); err != nil {
		t.Fatal(err)
	}
	// Whatever re-ensures the row later, the session stays what it was born as.
	if err := store.Ensure(ctx, "cron-job-1-1", "renamed"); err != nil {
		t.Fatal(err)
	}

	sourceOf := func(id string) string {
		t.Helper()
		var source string
		if err := db.QueryRow("SELECT source FROM fb_sessions WHERE id=?", id).Scan(&source); err != nil {
			t.Fatal(err)
		}
		return source
	}
	if got := sourceOf("cron-job-1-1"); got != SessionSourceCron {
		t.Fatalf("fire session source = %q, want %q", got, SessionSourceCron)
	}
	if got := sourceOf("plain"); got != "" {
		t.Fatalf("an ordinary session born without a source = %q, want the default", got)
	}
}

// TestSessionDefaultsAreSafeToChangeWhileSessionsAreBorn runs a configuration
// reload against concurrent session creation. The defaults were plain fields
// written from one goroutine and read from another; the lock makes the race
// impossible (verified under -race).
func TestSessionDefaultsAreSafeToChangeWhileSessionsAreBorn(t *testing.T) {
	store := NewSessionStore(openStateDB(t), "owner")
	ctx := context.Background()
	store.ConfigureMemoryDefaults("disabled", "webchat", "/launch", "main")

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := range 200 {
			if i%2 == 0 {
				store.SetMemoryMode("off")
			} else {
				store.SetMemoryMode("enabled")
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := range 200 {
			if err := store.Ensure(ctx, fmt.Sprintf("born-%d", i), "born"); err != nil {
				t.Errorf("Ensure: %v", err)
				return
			}
		}
	}()
	wg.Wait()
}

// TestDeleteSessionsCascadesAndReportsLeftovers pins deletion as one whole
// event: the rows go by cascade in one transaction, a conversation forked
// from the deleted session keeps its own life with its parent link cleared,
// another agent's session is untouched even when named in the same call, and
// everything the conversations left on disk — the uploaded files and the
// spilled tool outputs their transcripts point at — comes back to the caller
// so it can be removed once the rows are gone.
func TestDeleteSessionsCascadesAndReportsLeftovers(t *testing.T) {
	db := openStateDB(t)
	ctx := context.Background()
	mine := NewSessionStore(db, "main")
	if err := mine.Ensure(ctx, "cron-job-9-old", "old fire"); err != nil {
		t.Fatal(err)
	}
	if err := mine.Ensure(ctx, "cron-job-9-fresh", "fresh fire"); err != nil {
		t.Fatal(err)
	}
	if err := mine.Ensure(ctx, "forked-1", "a conversation forked away"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE fb_sessions SET parent_session_id='cron-job-9-old' WHERE id='forked-1'`); err != nil {
		t.Fatal(err)
	}
	other := NewSessionStore(db, "second")
	if err := other.Ensure(ctx, "second-own-1", "another tenant's conversation"); err != nil {
		t.Fatal(err)
	}

	spill := "/ws/state/tmp/tool-outputs/shell-call-1-1700000000000.txt"
	if _, err := db.Exec(`INSERT INTO fb_messages(session_id, role, content, tool_meta_json, created_at)
		VALUES('cron-job-9-old', 'tool', 'done', '{"full_path":"/ws/state/tmp/tool-outputs/shell-call-1-1700000000000.txt"}', 1700000000)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO fb_messages(session_id, role, content, created_at)
		VALUES('cron-job-9-old', 'user', 'the prompt', 1700000000)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO fb_runs(id, session_id, input_text, status, created_at, updated_at)
		VALUES('run-1', 'cron-job-9-old', '', 'done', 1700000000, 1700000000)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO fb_session_events(session_id, run_id, event_id, event_type, payload_json, occurred_at_ms)
		VALUES('cron-job-9-old', 'run-1', 'evt-1', 'turn.completed', '{}', 1700000000000)`); err != nil {
		t.Fatal(err)
	}
	if err := (&CronStore{DB: db}).SaveHeartbeat(ctx, Heartbeat{SessionID: "cron-job-9-old", IntervalSec: 600, Prompt: "beat"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO fb_files(id, session_id, original_name, media_type, size_bytes, sha256,
		storage_backend, storage_bucket, storage_key, parse_status, parsed_text_path, parse_error, created_at, updated_at)
		VALUES('file-1', 'cron-job-9-old', 'report.pdf', 'application/pdf', 3, 'abc', 'local', '', 'ab/file-1.pdf', 'done', 'file-1.txt', '', 1700000000, 1700000000)`); err != nil {
		t.Fatal(err)
	}

	left, err := mine.DeleteSessions(ctx, []string{"cron-job-9-old", "second-own-1"})
	if err != nil {
		t.Fatalf("DeleteSessions: %v", err)
	}

	for _, table := range []string{"fb_messages", "fb_runs", "fb_session_events", "fb_heartbeats", "fb_files"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table + ` WHERE session_id='cron-job-9-old'`).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != 0 {
			t.Fatalf("%s still holds %d rows of the deleted session", table, n)
		}
	}
	var gone int
	if err := db.QueryRow(`SELECT COUNT(*) FROM fb_sessions WHERE id='cron-job-9-old'`).Scan(&gone); err != nil {
		t.Fatal(err)
	}
	if gone != 0 {
		t.Fatal("the deleted session's row is still there")
	}

	var parent sql.NullString
	if err := db.QueryRow(`SELECT parent_session_id FROM fb_sessions WHERE id='forked-1'`).Scan(&parent); err != nil {
		t.Fatalf("forked session: %v", err)
	}
	if parent.Valid && parent.String != "" {
		t.Fatalf("fork's parent link = %q, want cleared", parent.String)
	}
	var alive int
	if err := db.QueryRow(`SELECT COUNT(*) FROM fb_sessions WHERE id IN ('second-own-1', 'cron-job-9-fresh')`).Scan(&alive); err != nil {
		t.Fatal(err)
	}
	if alive != 2 {
		t.Fatalf("sessions the call had no business deleting: %d left, want 2", alive)
	}

	if len(left.Files) != 1 || left.Files[0].ID != "file-1" {
		t.Fatalf("leftover files = %+v, want exactly file-1", left.Files)
	}
	if len(left.SpillPaths) != 1 || left.SpillPaths[0] != spill {
		t.Fatalf("leftover spill paths = %v, want exactly %s", left.SpillPaths, spill)
	}

	// Idempotent on a second pass: everything is already gone.
	if left2, err := mine.DeleteSessions(ctx, []string{"cron-job-9-old"}); err != nil || len(left2.Files) != 0 || len(left2.SpillPaths) != 0 {
		t.Fatalf("second delete = %+v, %v, want no error and no leftovers", left2, err)
	}
}
