package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

// TestStampRunFirstTokenRecordsOneWaitAndReadsItBack pins the owner's rule
// (plan 015): the first-token wait is one value per run — a second stamp, as a
// continuation past an approval would attempt, leaves the measured wait — and
// the transcript reader hands it back on every row of that run.
func TestStampRunFirstTokenRecordsOneWaitAndReadsItBack(t *testing.T) {
	ctx := context.Background()
	db := openStateDB(t)
	store := NewSessionStore(db, "main")
	ensureSessionsForRuns(t, db, "s1")
	runs := &RunStore{DB: db}
	run, err := runs.CreateRun(ctx, "s1", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.StampRunFirstToken(ctx, run.ID, 1200*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	// A continuation is the same run: the second stamp must not overwrite it.
	if err := store.StampRunFirstToken(ctx, run.ID, 5000*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessageForRun(ctx, "s1", run.ID, "assistant", "hi", "", "", messageExecTiming{}); err != nil {
		t.Fatal(err)
	}

	messages, err := store.ListRecentMessages(ctx, "s1", 10)
	if err != nil {
		t.Fatal(err)
	}
	var answer *Message
	for i := range messages {
		if strings.TrimSpace(messages[i].RunID) == run.ID {
			answer = &messages[i]
		}
	}
	if answer == nil {
		t.Fatalf("no transcript row carried run %s: %+v", run.ID, messages)
	}
	if answer.RunFirstTokenMs != 1200 {
		t.Fatalf("RunFirstTokenMs = %d, want the first measured 1200ms", answer.RunFirstTokenMs)
	}
	if answer.RunWorkedMs != 0 {
		t.Fatalf("RunWorkedMs = %d, want the run still untimed", answer.RunWorkedMs)
	}
}

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
	if err := rows.Err(); err != nil {
		t.Fatal(err)
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
		// Each listed session holds a message: a row with no message is not a
		// conversation and no list returns it. The append must come before
		// the timestamp is pinned, because it bumps updated_at through the
		// upsert every write path runs.
		if _, err := acme.Append(ctx, id, "user", "hello "+id); err != nil {
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
	// It is a conversation because it holds a message.
	if _, err := store.Append(ctx, "ordinary", "user", "the conversation"); err != nil {
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

// TestConversationListsHideSessionsWithoutAMessage pins what a conversation
// list means by "a conversation": the session holds a message. A row exists
// from the moment a surface needs the session's identity — /new, /model,
// /migrate and the web's create endpoint all ensure it before the
// conversation has said anything — so a list that went by rows would offer a
// person conversations with nothing to resume, newest first.
func TestConversationListsHideSessionsWithoutAMessage(t *testing.T) {
	ctx := context.Background()
	ids := func(rows []SessionSummary) []string {
		out := make([]string, 0, len(rows))
		for _, row := range rows {
			out = append(out, row.ID)
		}
		return out
	}

	t.Run("every list hides a row that holds no message", func(t *testing.T) {
		db := openStateDB(t)
		store := NewSessionStore(db, "main")
		projects := NewProjectStore(db, "main")
		p, err := projects.Create(ctx, CreateProjectInput{Name: "proj", Root: "/tmp/proj"})
		if err != nil {
			t.Fatal(err)
		}

		if err := store.Ensure(ctx, "empty", "empty"); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Append(ctx, "talked", "user", "hello"); err != nil {
			t.Fatal(err)
		}
		// The project's own pair: a row that never said anything, and a
		// conversation.
		if err := store.Ensure(ctx, "proj-empty", "proj-empty"); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Append(ctx, "proj-talk", "user", "hello"); err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{"proj-empty", "proj-talk"} {
			if err := projects.BindSession(ctx, id, p.ID); err != nil {
				t.Fatal(err)
			}
		}
		// A conversation the user rewound to before its first message keeps
		// its rows, and therefore stays listed: the only place that rewind
		// can be undone is the conversation's own view.
		row, err := store.Append(ctx, "rewound", "user", "the wrong thing")
		if err != nil {
			t.Fatal(err)
		}
		if err := store.WithdrawUserTurn(ctx, "rewound", row); err != nil {
			t.Fatal(err)
		}

		recent, err := store.ListSessionsRecent(ctx, 50)
		if err != nil {
			t.Fatal(err)
		}
		paged, err := store.ListSessionsRecentPaged(ctx, 50, 0)
		if err != nil {
			t.Fatal(err)
		}
		own, err := store.ListSessionsOfSource(ctx, SessionSourceConversation, 50)
		if err != nil {
			t.Fatal(err)
		}
		inProject, err := store.ListSessionsForProject(ctx, p.ID, 50, 0)
		if err != nil {
			t.Fatal(err)
		}

		// Both agent-wide lists hold every conversation of this agent,
		// project-bound or not; ListSessionsOfSource holds the ones with no
		// project; the project's list holds its own.
		for name, list := range map[string][]SessionSummary{
			"ListSessionsRecent":      recent,
			"ListSessionsRecentPaged": paged,
		} {
			got := ids(list)
			for _, want := range []string{"talked", "rewound", "proj-talk"} {
				if !slices.Contains(got, want) {
					t.Fatalf("%s lost %q: %v", name, want, got)
				}
			}
			for _, unwanted := range []string{"empty", "proj-empty"} {
				if slices.Contains(got, unwanted) {
					t.Fatalf("%s listed %q, a session with no message: %v", name, unwanted, got)
				}
			}
		}
		if got := ids(own); len(got) != 2 || !slices.Contains(got, "talked") || !slices.Contains(got, "rewound") {
			t.Fatalf("purpose-filtered list = %v, want the agent's own conversations only", got)
		}
		if got := ids(inProject); !slices.Equal(got, []string{"proj-talk"}) {
			t.Fatalf("project list = %v, want only the project's conversation", got)
		}
	})

	t.Run("pages are not hollowed out by empty rows", func(t *testing.T) {
		db := openStateDB(t)
		store := NewSessionStore(db, "main")
		// Five empty rows among three conversations: the page sequence is the
		// conversations alone, in the list's own newest-first order, because
		// the filter runs inside the query and not over a fetched page.
		for i := 0; i < 8; i++ {
			id := fmt.Sprintf("s-%d", i)
			if err := store.Ensure(ctx, id, id); err != nil {
				t.Fatal(err)
			}
		}
		for _, id := range []string{"s-1", "s-4", "s-6"} {
			if _, err := store.Append(ctx, id, "user", "hello "+id); err != nil {
				t.Fatal(err)
			}
		}
		for i := 0; i < 8; i++ {
			if _, err := db.Exec("UPDATE fb_sessions SET updated_at = ? WHERE id = ?", int64(100-i), fmt.Sprintf("s-%d", i)); err != nil {
				t.Fatal(err)
			}
		}

		var got []string
		for offset := 0; ; offset++ {
			page, err := store.ListSessionsRecentPaged(ctx, 1, offset)
			if err != nil {
				t.Fatal(err)
			}
			if len(page) == 0 {
				break
			}
			got = append(got, ids(page)...)
		}
		if want := []string{"s-1", "s-4", "s-6"}; !slices.Equal(got, want) {
			t.Fatalf("paged ids = %v, want %v", got, want)
		}
	})

	t.Run("another primary agent does not see them", func(t *testing.T) {
		db := openStateDB(t)
		if _, err := NewSessionStore(db, "main").Append(ctx, "mine", "user", "hello"); err != nil {
			t.Fatal(err)
		}
		theirs := NewSessionStore(db, "other")
		recent, err := theirs.ListSessionsRecent(ctx, 50)
		if err != nil {
			t.Fatal(err)
		}
		paged, err := theirs.ListSessionsRecentPaged(ctx, 50, 0)
		if err != nil {
			t.Fatal(err)
		}
		own, err := theirs.ListSessionsOfSource(ctx, SessionSourceConversation, 50)
		if err != nil {
			t.Fatal(err)
		}
		if len(recent)+len(paged)+len(own) != 0 {
			t.Fatalf("another agent's lists = %v %v %v, want nothing", recent, paged, own)
		}
	})
}

// TestListConversationsPagePinsTheDrawerList pins the conversation drawer's
// one list: this agent's conversations across every project plus the primary
// agent's own skill-workshop tasks, while the project-less ordinary rows of
// pre-project versions, a scheduled task's fire, a subagent's private
// conversation, a row that never said anything and another tenant's session
// stay out. Search matches titles, pages walk newest first without overlap,
// and a page past the end reads the last page.
func TestListConversationsPagePinsTheDrawerList(t *testing.T) {
	ctx := context.Background()
	db := openStateDB(t)
	store := NewSessionStore(db, "main")
	projects := NewProjectStore(db, "main")
	p, err := projects.Create(ctx, CreateProjectInput{Name: "drawer", Root: "/tmp/drawer"})
	if err != nil {
		t.Fatal(err)
	}

	ensure := func(id string) {
		t.Helper()
		if err := store.Ensure(ctx, id, id); err != nil {
			t.Fatal(err)
		}
	}
	say := func(id string) {
		t.Helper()
		if _, err := store.Append(ctx, id, "user", "hello "+id); err != nil {
			t.Fatal(err)
		}
	}
	source := func(id, value string) {
		t.Helper()
		if err := store.SetSessionSource(ctx, id, value); err != nil {
			t.Fatal(err)
		}
	}

	// Listed: a project's ordinary conversation, a project's workshop task
	// and the primary agent's own project-less workshop task.
	for _, id := range []string{"proj-talk", "proj-task", "own-task"} {
		ensure(id)
		say(id)
	}
	for _, id := range []string{"proj-talk", "proj-task"} {
		if err := projects.BindSession(ctx, id, p.ID); err != nil {
			t.Fatal(err)
		}
	}
	source("proj-task", SessionSourceWorkshop)
	source("own-task", SessionSourceWorkshop)
	// Left out: a pre-project ordinary conversation, a scheduled task's
	// fire, a subagent's conversation, a row that never said anything, and
	// another tenant's conversation.
	ensure("legacy")
	say("legacy")
	ensure("fire")
	say("fire")
	source("fire", SessionSourceCron)
	if err := projects.BindSession(ctx, "fire", p.ID); err != nil {
		t.Fatal(err)
	}
	ensure("sub")
	say("sub")
	source("sub", SessionSourceSubagent)
	ensure("quiet")
	if err := projects.BindSession(ctx, "quiet", p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSessionStore(db, "other").Append(ctx, "theirs", "user", "hello"); err != nil {
		t.Fatal(err)
	}
	// The newest conversation comes first, and none of the left-out rows —
	// however new — can take a row of a page.
	for id, at := range map[string]int64{
		"own-task": 30, "proj-task": 20, "proj-talk": 10,
		"legacy": 90, "fire": 80, "sub": 70, "quiet": 60, "theirs": 50,
	} {
		if _, err := db.Exec("UPDATE fb_sessions SET updated_at = ? WHERE id = ?", at, id); err != nil {
			t.Fatal(err)
		}
	}

	ids := func(rows []SessionSummary) []string {
		out := make([]string, 0, len(rows))
		for _, row := range rows {
			out = append(out, row.ID)
		}
		return out
	}

	rows, total, err := store.ListConversationsPage(ctx, "", "", 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || !slices.Equal(ids(rows), []string{"own-task", "proj-task", "proj-talk"}) {
		t.Fatalf("drawer list = %v (total %d), want the three conversations newest first", ids(rows), total)
	}
	for _, row := range rows {
		wantProject, wantSource := "", SessionSourceWorkshop
		if row.ID == "proj-talk" {
			wantProject, wantSource = p.ID, SessionSourceConversation
		}
		if row.ID == "proj-task" {
			wantProject = p.ID
		}
		if row.ProjectID != wantProject || row.Source != wantSource {
			t.Fatalf("%s = project %q source %q, want project %q source %q", row.ID, row.ProjectID, row.Source, wantProject, wantSource)
		}
	}

	// Search narrows by title, on the server, before the page is cut.
	rows, total, err = store.ListConversationsPage(ctx, "own-task", "", 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || !slices.Equal(ids(rows), []string{"own-task"}) {
		t.Fatalf("searched list = %v (total %d), want only own-task", ids(rows), total)
	}

	// The project filter is the server's too: only that project's rows
	// (its conversation and its workshop task), counted before the page is
	// cut, and a project nothing belongs to lists nothing.
	rows, total, err = store.ListConversationsPage(ctx, "", p.ID, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || !slices.Equal(ids(rows), []string{"proj-task", "proj-talk"}) {
		t.Fatalf("project list = %v (total %d), want the project's two rows", ids(rows), total)
	}
	rows, total, err = store.ListConversationsPage(ctx, "", "prj_nothing", 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 0 || len(rows) != 0 {
		t.Fatalf("unknown project list = %v (total %d), want nothing", ids(rows), total)
	}

	// Pages walk the same order without overlap.
	first, total, err := store.ListConversationsPage(ctx, "", "", 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	second, total2, err := store.ListConversationsPage(ctx, "", "", 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || total2 != 3 || !slices.Equal(ids(first), []string{"own-task", "proj-task"}) || !slices.Equal(ids(second), []string{"proj-talk"}) {
		t.Fatalf("pages = %v %v (totals %d %d), want the list split newest-first", ids(first), ids(second), total, total2)
	}

	// A page past the end reads the last page rather than an empty one.
	rows, total, err = store.ListConversationsPage(ctx, "", "", 2, 99)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || !slices.Equal(ids(rows), []string{"proj-talk"}) {
		t.Fatalf("past-the-end page = %v (total %d), want the last page", ids(rows), total)
	}
}

// TestListConversationsPageLeavesOutBatchExecutions pins the drawer's other
// exclusion (plan 153): a batch project's conversations are executions of its
// tasks, reached from that project's task page and its execution history, so
// the conversation drawer never lists them — neither in the whole list nor
// when it asks for that project's rows — while a coding and an office
// conversation under the same agent stay.
func TestListConversationsPageLeavesOutBatchExecutions(t *testing.T) {
	ctx := context.Background()
	projects, store := newProjectStoreForTest(t, "main")
	create := func(name, projectType string) Project {
		t.Helper()
		p, err := projects.Create(ctx, CreateProjectInput{Name: name, Root: t.TempDir(), ProjectType: projectType})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	batch := create("batch", ProjectTypeBatch)
	coding := create("coding", ProjectTypeCoding)
	office := create("office", ProjectTypeOffice)
	// The batch execution is the newest row, so a list that forgot the filter
	// cannot hide behind the page's cut.
	for _, row := range []struct {
		id      string
		project Project
		at      int64
	}{
		{"batch-run-1", batch, 30},
		{"coding-talk", coding, 20},
		{"office-talk", office, 10},
	} {
		if err := store.EnsureAt(ctx, row.id, row.id, SessionBirth{ProjectID: row.project.ID}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Append(ctx, row.id, "user", "hello "+row.id); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.Exec(`UPDATE fb_sessions SET updated_at = ? WHERE id = ?`, row.at, row.id); err != nil {
			t.Fatal(err)
		}
	}
	ids := func(rows []SessionSummary) []string {
		out := make([]string, 0, len(rows))
		for _, row := range rows {
			out = append(out, row.ID)
		}
		return out
	}

	rows, total, err := store.ListConversationsPage(ctx, "", "", 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || !slices.Equal(ids(rows), []string{"coding-talk", "office-talk"}) {
		t.Fatalf("drawer list = %v (total %d), want only the coding and office conversations", ids(rows), total)
	}

	// The project filter does not open the door again: a batch project's
	// executions are listed by its task page, never here.
	rows, total, err = store.ListConversationsPage(ctx, "", batch.ID, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 0 || len(rows) != 0 {
		t.Fatalf("batch project's drawer list = %v (total %d), want nothing", ids(rows), total)
	}
}

// TestConversationCountsByProjectCountsOnlyConversationsWithMessages pins the
// count a project table draws per row: it is the same predicate
// ListSessionsForProject lists by, so a row that never said anything and a
// scheduled task's fire are not conversations, and a project with none is
// simply absent from the answer.
func TestConversationCountsByProjectCountsOnlyConversationsWithMessages(t *testing.T) {
	ctx := context.Background()
	db := openStateDB(t)
	store := NewSessionStore(db, "main")
	projects := NewProjectStore(db, "main")
	talked, err := projects.Create(ctx, CreateProjectInput{Name: "talked", Root: "/tmp/count-talked"})
	if err != nil {
		t.Fatal(err)
	}
	quiet, err := projects.Create(ctx, CreateProjectInput{Name: "quiet", Root: "/tmp/count-quiet"})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"c1", "c2", "c-shell", "c-fire"} {
		if err := store.Ensure(ctx, id, id); err != nil {
			t.Fatal(err)
		}
		if err := projects.BindSession(ctx, id, talked.ID); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"c1", "c2", "c-fire"} {
		if _, err := store.Append(ctx, id, "user", "hello"); err != nil {
			t.Fatal(err)
		}
	}
	// A scheduled task's fire holds messages, but its project's sessions tab
	// reaches it through the task, not the conversation list.
	if err := store.SetSessionSource(ctx, "c-fire", SessionSourceCron); err != nil {
		t.Fatal(err)
	}
	// A workshop task is the primary agent's own, never a project's.
	if err := store.Ensure(ctx, "workshop-1", "workshop-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(ctx, "workshop-1", "user", "make a skill"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSessionSource(ctx, "workshop-1", SessionSourceWorkshop); err != nil {
		t.Fatal(err)
	}

	counts, err := store.ConversationCountsByProject(ctx, []string{talked.ID, quiet.ID, "no-such-project"})
	if err != nil {
		t.Fatal(err)
	}
	if got := counts[talked.ID]; got != 2 {
		t.Fatalf("count = %d, want the project's 2 conversations: %v", got, counts)
	}
	for _, id := range []string{quiet.ID, "no-such-project"} {
		if got, seen := counts[id]; seen {
			t.Fatalf("project with no conversation must be absent, got %d for %q", got, id)
		}
	}
	empty, err := store.ConversationCountsByProject(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 {
		t.Fatalf("no project ids must count nothing, got %v", empty)
	}
}

// TestSubagentSessionsAreBornHiddenFromEveryList pins what a subagent's
// worker session is at birth: a private conversation of one subagent, whose
// row carries its conversation's id in parent_session_id and source
// 'subagent' — and is therefore invisible to every conversation list, every
// child listing, while still being a real session the subagent's own view
// reads back. A later plain Ensure must not rewrite either birth column.
func TestSubagentSessionsAreBornHiddenFromEveryList(t *testing.T) {
	ctx := context.Background()
	db := openStateDB(t)
	store := NewSessionStore(db, "main")

	if err := store.Ensure(ctx, "c1", "the conversation"); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureAt(ctx, "w1", "the subagent's own conversation", SessionBirth{
		Source:          SessionSourceSubagent,
		ParentSessionID: "c1",
	}); err != nil {
		t.Fatal(err)
	}

	for name, list := range map[string]func() ([]SessionSummary, error){
		"ListSessionsRecent":      func() ([]SessionSummary, error) { return store.ListSessionsRecent(ctx, 100) },
		"ListSessionsRecentPaged": func() ([]SessionSummary, error) { return store.ListSessionsRecentPaged(ctx, 100, 0) },
		"ListChildSessionsRecent": func() ([]SessionSummary, error) { return store.ListChildSessionsRecent(ctx, "c1", 100) },
	} {
		rows, err := list()
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.ID == "w1" {
				t.Fatalf("%s returned a subagent's worker session: %+v", name, row)
			}
		}
	}

	var parent, source string
	if err := db.QueryRow(`SELECT parent_session_id, source FROM fb_sessions WHERE id='w1'`).Scan(&parent, &source); err != nil {
		t.Fatal(err)
	}
	if parent != "c1" || source != SessionSourceSubagent {
		t.Fatalf("worker session birth = parent %q source %q, want c1/%q", parent, source, SessionSourceSubagent)
	}

	// A later write that merely touches the session (every append path runs
	// the upsert first) must not rewrite what it was born with.
	if err := store.Ensure(ctx, "w1", "renamed"); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT parent_session_id, source FROM fb_sessions WHERE id='w1'`).Scan(&parent, &source); err != nil {
		t.Fatal(err)
	}
	if parent != "c1" || source != SessionSourceSubagent {
		t.Fatalf("after re-ensure = parent %q source %q, want the birth values", parent, source)
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
	if _, err := sessions.Append(ctx, "talk", "user", "hello"); err != nil {
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

// TestListSessionsForProjectPageSortsCountsAndSearches pins the project
// conversation table: the rows the old list gives, searched by title, ordered by
// the time the table asked for, each carrying its own model if it pinned one and
// the number of messages a person actually exchanged in it.
func TestListSessionsForProjectPageSortsCountsAndSearches(t *testing.T) {
	ctx := context.Background()
	db := openStateDB(t)
	projects := NewProjectStore(db, "main")
	sessions := NewSessionStore(db, "main")
	p, err := projects.Create(ctx, CreateProjectInput{Name: "proj", Root: "/tmp/proj"})
	if err != nil {
		t.Fatal(err)
	}
	// "talked" is the newer conversation by update time and the older one by
	// creation time, so the two orders must differ.
	for _, ids := range []string{"talked", "quiet-then-loud"} {
		if err := sessions.Ensure(ctx, ids, ids); err != nil {
			t.Fatal(err)
		}
		if err := projects.BindSession(ctx, ids, p.ID); err != nil {
			t.Fatal(err)
		}
	}
	seed := func(sessionID, role, visibility, content string) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO fb_messages(session_id, role, visibility, content, created_at) VALUES(?,?,?,?,?)`,
			sessionID, role, visibility, content, 1); err != nil {
			t.Fatal(err)
		}
	}
	seed("talked", "user", "visible", "hello")
	seed("talked", "assistant", "visible", "hi there")
	// Work, not conversation: neither tool nor reasoning rows are messages a
	// person exchanged.
	seed("talked", "tool", "visible", "a tool result")
	seed("talked", "reasoning", "visible", "thinking")
	// A hidden row is not shown in the transcript either, so it is not counted.
	seed("talked", "user", "withdrawn", "never mind")
	seed("quiet-then-loud", "user", "visible", "the only thing said")
	if _, err := sessions.SaveSessionModelSelection(ctx, "talked", SessionModelSelection{Provider: "anthropic", Model: "claude-sonnet-4"}); err != nil {
		t.Fatal(err)
	}
	for id, times := range map[string][2]int64{
		"talked":          {100, 300},
		"quiet-then-loud": {200, 200},
	} {
		if _, err := db.Exec(`UPDATE fb_sessions SET created_at = ?, updated_at = ? WHERE id = ?`, times[0], times[1], id); err != nil {
			t.Fatal(err)
		}
	}

	ids := func(rows []ProjectSessionRow) []string {
		out := make([]string, 0, len(rows))
		for _, row := range rows {
			out = append(out, row.ID)
		}
		return out
	}

	newestFirst, total, err := sessions.ListSessionsForProjectPage(ctx, p.ID, ProjectSessionsQuery{Desc: true}, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Fatalf("total = %d, want both conversations", total)
	}
	if got := ids(newestFirst); !slices.Equal(got, []string{"talked", "quiet-then-loud"}) {
		t.Fatalf("default order = %v, want newest update first", got)
	}
	byID := map[string]ProjectSessionRow{}
	for _, row := range newestFirst {
		byID[row.ID] = row
	}
	if talked := byID["talked"]; talked.MessageCount != 2 {
		t.Fatalf("message count = %d, want the two visible user/assistant rows", talked.MessageCount)
	}
	if talked := byID["talked"]; talked.Model != "claude-sonnet-4" {
		t.Fatalf("model = %q, want the session's own choice", talked.Model)
	}
	if pinned := byID["quiet-then-loud"]; pinned.Model != "" {
		t.Fatalf("a session that never pinned a model reports %q", pinned.Model)
	}
	if pinned := byID["quiet-then-loud"]; pinned.MessageCount != 1 {
		t.Fatalf("message count = %d, want the one visible user row", pinned.MessageCount)
	}

	byCreated, _, err := sessions.ListSessionsForProjectPage(ctx, p.ID, ProjectSessionsQuery{Sort: "created", Desc: true}, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(byCreated); !slices.Equal(got, []string{"quiet-then-loud", "talked"}) {
		t.Fatalf("order by creation time = %v, want the newer row first", got)
	}

	// The search box narrows the list, and the total is the count of matches.
	found, total, err := sessions.ListSessionsForProjectPage(ctx, p.ID, ProjectSessionsQuery{Search: "loud"}, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(found); !slices.Equal(got, []string{"quiet-then-loud"}) || total != 1 {
		t.Fatalf("search = %v (total %d), want only the matching conversation", got, total)
	}

	// A page past the end reads the last page rather than an empty one.
	last, _, err := sessions.ListSessionsForProjectPage(ctx, p.ID, ProjectSessionsQuery{Desc: true}, 1, 5)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(last); !slices.Equal(got, []string{"quiet-then-loud"}) {
		t.Fatalf("page past the end = %v, want the last page", got)
	}

	// Counting messages must ride the session's own index, never read the whole
	// message table: the column is drawn for every row of every page.
	query, args := sessions.projectSessionsPageSQL(p.ID, ProjectSessionsQuery{Desc: true}, 10, 0)
	planRows, err := db.Query(`EXPLAIN QUERY PLAN `+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer planRows.Close()
	var plan []string
	for planRows.Next() {
		var id, parent, unused int
		var detail string
		if err := planRows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if err := planRows.Err(); err != nil {
		t.Fatal(err)
	}
	searchedCount := false
	for _, detail := range plan {
		parts := strings.Fields(detail)
		if len(parts) >= 2 && parts[0] == "SCAN" && (parts[1] == "m" || parts[1] == "fb_messages") {
			t.Fatalf("the message count reads the whole table: %q\nplan: %v", detail, plan)
		}
		// The count is the subquery that also narrows on visibility; the bare
		// EXISTS beside it only narrows on the session.
		if strings.Contains(detail, "idx_fb_messages_session_visibility") && strings.Contains(detail, "visibility=?") {
			searchedCount = true
		}
	}
	if !searchedCount {
		t.Fatalf("the message count does not narrow on idx_fb_messages_session_visibility: %v", plan)
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
		`INSERT INTO fb_sessions(id, agent_id, title, updated_at, created_at, cwd) VALUES('s-old','main','old',1500,1000,'/repo/s-old')`,
		`INSERT INTO fb_sessions(id, agent_id, title, updated_at, created_at, cwd) VALUES('s-new','main','new',3000,2500,'/repo/s-new')`,
		`INSERT INTO fb_sessions(id, agent_id, title, updated_at, created_at, cwd) VALUES('s-empty','main','empty',4000,3500,'/repo/s-empty')`,
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

// The "most recently used" model is the newest row among this agent's own
// conversation and workshop sessions only — scheduled fires, channel
// conversations and subagent workers never count, another agent's rows are
// invisible, and a touch refreshes the time without touching the model.
func TestLatestSessionModelSelectionScopesAndTouch(t *testing.T) {
	ctx := context.Background()
	db := openStateDB(t)
	seed := func(id, agent, source string, at int64, provider, model string) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO fb_sessions(id, agent_id, title, updated_at, created_at, cwd, source) VALUES(?,?,?,?,?,?,?)`,
			id, agent, id, at, 1, "/repo/s", source); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
		if provider != "" {
			if _, err := db.Exec(`INSERT INTO fb_session_model_state(session_id, provider, model, effort, updated_at) VALUES(?,?,?,?,?)`,
				id, provider, model, "", at); err != nil {
				t.Fatalf("seed row %s: %v", id, err)
			}
		}
	}
	seed("old-talk", "main", "", 100, "openai", "gpt-old")
	seed("workshop-late", "main", "workshop", 300, "anthropic", "claude-late")
	seed("cron-fire", "main", "cron", 900, "zhipuai", "glm-cron")
	seed("chan", "main", "telegram", 800, "zhipuai", "glm-chan")
	seed("worker", "main", "subagent", 700, "zhipuai", "glm-worker")
	seed("other-agent", "other", "", 950, "openai", "gpt-theirs")

	mine := NewSessionStore(db, "main")
	sel, ok, err := mine.LatestSessionModelSelection(ctx)
	if err != nil || !ok || sel.Provider != "anthropic" || sel.Model != "claude-late" {
		t.Fatalf("latest = %+v ok=%v err=%v, want workshop-late's claude-late", sel, ok, err)
	}

	// Touch makes an ordinary conversation the most recently used, without
	// changing its model; it does not reach another agent's sessions.
	if err := mine.TouchSessionModelSelection(ctx, "old-talk"); err != nil {
		t.Fatalf("touch: %v", err)
	}
	sel, ok, err = mine.LatestSessionModelSelection(ctx)
	if err != nil || !ok || sel.Provider != "openai" || sel.Model != "gpt-old" {
		t.Fatalf("latest after touch = %+v ok=%v err=%v, want old-talk's gpt-old", sel, ok, err)
	}
	var provider, model string
	var at int64
	if err := db.QueryRow(`SELECT provider, model, updated_at FROM fb_session_model_state WHERE session_id='old-talk'`).Scan(&provider, &model, &at); err != nil {
		t.Fatal(err)
	}
	if provider != "openai" || model != "gpt-old" {
		t.Fatalf("touch changed the model: %s/%s", provider, model)
	}
	if at <= 300 {
		t.Fatalf("touch did not move updated_at past the workshop row: %d", at)
	}
	// A ghost session and another agent's session are quiet no-ops.
	if err := mine.TouchSessionModelSelection(ctx, "ghost"); err != nil {
		t.Fatalf("touch ghost: %v", err)
	}
	if err := mine.TouchSessionModelSelection(ctx, "other-agent"); err != nil {
		t.Fatalf("touch another agent's session: %v", err)
	}
	var otherAt int64
	if err := db.QueryRow(`SELECT updated_at FROM fb_session_model_state WHERE session_id='other-agent'`).Scan(&otherAt); err != nil {
		t.Fatal(err)
	}
	if otherAt != 950 {
		t.Fatalf("touch reached another agent's row: %d", otherAt)
	}

	// A fresh agent with no rows answers nothing.
	fresh := NewSessionStore(db, "nobody")
	if _, ok, err := fresh.LatestSessionModelSelection(ctx); err != nil || ok {
		t.Fatalf("fresh agent latest: ok=%v err=%v", ok, err)
	}
}

// TestSessionModelStampIsStrictlyIncreasing pins the order "most recently
// used" depends on: many writes inside one millisecond must still leave
// strictly increasing timestamps, so the newest row is the one just written
// and never the storage engine's pick. fb_session_model_state is STRICT,
// WITHOUT ROWID — there is no rowid to break a tie with — which is why the
// stamp itself has to be total.
func TestSessionModelStampIsStrictlyIncreasing(t *testing.T) {
	ctx := context.Background()
	db := openStateDB(t)
	store := NewSessionStore(db, "main")
	const n = 64
	prev := int64(0)
	for i := 0; i < n; i++ {
		sid := fmt.Sprintf("stamp-%02d", i)
		model := fmt.Sprintf("m-%02d", i)
		if err := store.Ensure(ctx, sid, sid); err != nil {
			t.Fatalf("Ensure %s: %v", sid, err)
		}
		if _, err := store.SaveSessionModelSelection(ctx, sid, SessionModelSelection{Provider: "openai", Model: model}); err != nil {
			t.Fatalf("save %s: %v", sid, err)
		}
		// The reading surfaces share: the row just written is the newest.
		got, ok, err := store.LatestSessionModelSelection(ctx)
		if err != nil || !ok || got.Model != model {
			t.Fatalf("after write %d latest = %+v ok=%v err=%v, want %s", i, got, ok, err, model)
		}
	}
	rows, err := db.Query(`SELECT updated_at FROM fb_session_model_state ORDER BY updated_at ASC`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var at int64
		if err := rows.Scan(&at); err != nil {
			t.Fatal(err)
		}
		if at <= prev {
			t.Fatalf("stamp %d after %d is not strictly increasing", at, prev)
		}
		prev = at
		seen++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if seen != n {
		t.Fatalf("stamps = %d, want %d", seen, n)
	}
}

// TestLatestSessionModelSelectionIsDeterministicOnSharedStamps covers the
// rows this process did not write: a file from a build that stamped in
// seconds, a migrated file, or a hand-seeded one can hold several rows with
// the same updated_at. There is no true order among them, so what the reading
// must guarantee is the same answer on every call — a stable pick, not the
// storage engine's.
func TestLatestSessionModelSelectionIsDeterministicOnSharedStamps(t *testing.T) {
	ctx := context.Background()
	db := openStateDB(t)
	for _, sid := range []string{"tie-a", "tie-b"} {
		if _, err := db.Exec(`INSERT INTO fb_sessions(id, agent_id, title, updated_at, created_at, cwd, source) VALUES(?,?,?,?,?,?,?)`,
			sid, "main", sid, 500, 1, "/repo/s", ""); err != nil {
			t.Fatalf("seed session %s: %v", sid, err)
		}
		if _, err := db.Exec(`INSERT INTO fb_session_model_state(session_id, provider, model, effort, updated_at) VALUES(?,?,?,?,?)`,
			sid, "openai", "gpt-"+sid, "", 500); err != nil {
			t.Fatalf("seed row %s: %v", sid, err)
		}
	}
	store := NewSessionStore(db, "main")
	first, ok, err := store.LatestSessionModelSelection(ctx)
	if err != nil || !ok {
		t.Fatalf("latest: ok=%v err=%v", ok, err)
	}
	for i := 0; i < 8; i++ {
		again, ok, err := store.LatestSessionModelSelection(ctx)
		if err != nil || !ok || again != first {
			t.Fatalf("read %d = %+v ok=%v err=%v, want the same %+v", i, again, ok, err, first)
		}
	}
	// A touch lifts one of them past the other: recency works even when the
	// file arrived full of shared stamps.
	if err := store.TouchSessionModelSelection(ctx, "tie-a"); err != nil {
		t.Fatalf("touch: %v", err)
	}
	if got, ok, err := store.LatestSessionModelSelection(ctx); err != nil || !ok || got.Model != "gpt-tie-a" {
		t.Fatalf("latest after touch = %+v ok=%v err=%v, want gpt-tie-a", got, ok, err)
	}
}

// The permission-preset row follows the model-selection row's rules: write and
// read back, last write wins, another primary agent sees nothing, /fork copies
// and /new (a fresh row) does not, deleting the session takes the row with it,
// and the lazy YOLO observation reads the newest tool result only.
func TestSessionPermissionPresetRoundTripOwnershipAndCascade(t *testing.T) {
	ctx := context.Background()
	db := openStateDB(t)
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	mustExec(`INSERT INTO fb_sessions(id, agent_id, title, updated_at, created_at, cwd) VALUES('s','main','s',10,1,'/repo/s')`)
	mustExec(`INSERT INTO fb_sessions(id, agent_id, title, updated_at, created_at, cwd) VALUES('forked','main','f',11,2,'/repo/s')`)
	mustExec(`INSERT INTO fb_sessions(id, agent_id, title, updated_at, created_at, cwd) VALUES('fresh','main','n',12,3,'/repo/s')`)

	store := NewSessionStore(db, "main")
	stored, err := store.SaveSessionPermissionPreset(ctx, "s", "full-access", SessionPresetSourcePicker)
	if err != nil || !stored {
		t.Fatalf("save: stored=%v err=%v", stored, err)
	}
	// Last write wins, source travels with it.
	if stored, err = store.SaveSessionPermissionPreset(ctx, "s", "auto", SessionPresetSourcePicker); err != nil || !stored {
		t.Fatalf("overwrite: stored=%v err=%v", stored, err)
	}
	preset, source, _, ok, err := store.SessionPermissionPreset(ctx, "s")
	if err != nil || !ok || preset != "auto" || source != SessionPresetSourcePicker {
		t.Fatalf("read back = %q/%q ok=%v err=%v, want auto/picker", preset, source, ok, err)
	}

	// A ghost session stores nothing and reads nothing.
	if stored, err = store.SaveSessionPermissionPreset(ctx, "ghost", "auto", SessionPresetSourcePicker); err != nil || stored {
		t.Fatalf("ghost save: stored=%v err=%v", stored, err)
	}
	if _, _, _, ok, err := store.SessionPermissionPreset(ctx, "ghost"); err != nil || ok {
		t.Fatalf("ghost read: ok=%v err=%v", ok, err)
	}

	// /fork inherits; a fresh target with no source row stays empty.
	copied, err := store.CopySessionPermissionPreset(ctx, "s", "forked")
	if err != nil || !copied {
		t.Fatalf("fork copy: copied=%v err=%v", copied, err)
	}
	if preset, _, _, ok, _ := store.SessionPermissionPreset(ctx, "forked"); !ok || preset != "auto" {
		t.Fatalf("forked preset = %q ok=%v, want auto", preset, ok)
	}
	if copied, err = store.CopySessionPermissionPreset(ctx, "fresh", "forked"); err != nil || copied {
		t.Fatalf("copy from a source with no row: copied=%v err=%v", copied, err)
	}

	// Another primary agent owns neither the row nor the write.
	mine, theirs := NewSessionStore(db, "main"), NewSessionStore(db, "other")
	if _, err = theirs.SaveSessionPermissionPreset(ctx, "s", "read-only", SessionPresetSourcePicker); err == nil {
		t.Fatal("another agent stored a preset on a session it does not own")
	}
	if _, _, _, ok, err := theirs.SessionPermissionPreset(ctx, "s"); err == nil || ok {
		t.Fatalf("another agent read the row: ok=%v err=%v, want the ownership refusal", ok, err)
	}
	if _, _, _, ok, err := mine.SessionPermissionPreset(ctx, "s"); err != nil || !ok {
		t.Fatalf("owning agent lost the row: ok=%v err=%v", ok, err)
	}

	// Lazy YOLO observation: the newest tool result decides, older ones do not.
	mustExec(`INSERT INTO fb_messages(session_id, role, visibility, content, created_at) VALUES('s','tool','visible','{"output":"ok","approval_bypassed_by_yolo":true}',10)`)
	if saw, err := store.SessionLastToolResultBypassedApprovals(ctx, "s"); err != nil || !saw {
		t.Fatalf("yolo observation = %v err=%v, want true", saw, err)
	}
	mustExec(`INSERT INTO fb_messages(session_id, role, visibility, content, created_at) VALUES('s','tool','visible','{"output":"asked this time"}',11)`)
	if saw, err := store.SessionLastToolResultBypassedApprovals(ctx, "s"); err != nil || saw {
		t.Fatalf("yolo observation after a normal tool result = %v err=%v, want false", saw, err)
	}
	if saw, err := store.SessionLastToolResultBypassedApprovals(ctx, "fresh"); err != nil || saw {
		t.Fatalf("yolo observation without tool results = %v err=%v, want false", saw, err)
	}

	// Deleting the session releases the row (ON DELETE CASCADE).
	mustExec(`DELETE FROM fb_sessions WHERE id='s'`)
	if _, _, _, ok, err := store.SessionPermissionPreset(ctx, "s"); err != nil || ok {
		t.Fatalf("preset row survived session delete: ok=%v err=%v", ok, err)
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

// TestImageRefusalRoundTripAndModelScope pins the record that stops a
// conversation from paying for the same provider refusal on every turn: it
// reads back for the exact model that produced it, reads as nothing for any
// other model (a switch is the recovery the refusal sentence points at), and a
// second refusal on the same model refreshes the reason instead of adding a row.
func TestImageRefusalRoundTripAndModelScope(t *testing.T) {
	ctx := context.Background()
	store := NewSessionStore(openStateDB(t), "owner")
	if err := store.Ensure(ctx, "s", "s"); err != nil {
		t.Fatal(err)
	}

	// Nothing recorded yet: the answer is "no record", not an error.
	if reason, ok, err := store.ImageRefusal(ctx, "s", "deepseek", "deepseek-v4-pro"); err != nil || ok {
		t.Fatalf("fresh session: ok=%v reason=%q err=%v", ok, reason, err)
	}

	if err := store.RecordImageRefusal(ctx, "s", "deepseek", "deepseek-v4-pro", "unsupported image"); err != nil {
		t.Fatalf("record: %v", err)
	}
	reason, ok, err := store.ImageRefusal(ctx, "s", "deepseek", "deepseek-v4-pro")
	if err != nil || !ok {
		t.Fatalf("read back: ok=%v err=%v", ok, err)
	}
	if reason != "unsupported image" {
		t.Fatalf("reason = %q", reason)
	}

	// Another model of the same provider, and the same model under another
	// provider, are different questions.
	for _, other := range [][2]string{{"deepseek", "deepseek-chat"}, {"zhipuai", "deepseek-v4-pro"}} {
		if _, ok, err := store.ImageRefusal(ctx, "s", other[0], other[1]); err != nil || ok {
			t.Fatalf("%v: ok=%v err=%v — a record must not follow another model", other, ok, err)
		}
	}

	// A second refusal on the same model is the same row: the reason is
	// refreshed and the count does not grow.
	if err := store.RecordImageRefusal(ctx, "s", "deepseek", "deepseek-v4-pro", "image format not supported"); err != nil {
		t.Fatalf("record again: %v", err)
	}
	reason, ok, err = store.ImageRefusal(ctx, "s", "deepseek", "deepseek-v4-pro")
	if err != nil || !ok {
		t.Fatalf("read after second refusal: ok=%v err=%v", ok, err)
	}
	if reason != "image format not supported" {
		t.Fatalf("reason after refresh = %q, want the latest sentence", reason)
	}
	var rows int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM fb_session_image_refusals WHERE session_id='s'`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("rows = %d, want one row per (session, provider, model)", rows)
	}

	// A reason is folded to one line before it is stored: it rides in a sentence
	// a surface shows, so a multi-line provider error must not become a
	// paragraph.
	if err := store.RecordImageRefusal(ctx, "s", "acme", "m", "  first line\n\nsecond   line  "); err != nil {
		t.Fatal(err)
	}
	if reason, _, err = store.ImageRefusal(ctx, "s", "acme", "m"); err != nil {
		t.Fatal(err)
	}
	if reason != "first line second line" {
		t.Fatalf("stored reason = %q, want one flattened line", reason)
	}
}

// TestImageRefusalNeedsAnIdentityAndFollowsTheSession pins the two boundaries of
// the record: a write with no session, provider or model files nothing (it is an
// optimization, never an error), and the row is released when its session is.
func TestImageRefusalNeedsAnIdentityAndFollowsTheSession(t *testing.T) {
	ctx := context.Background()
	store := NewSessionStore(openStateDB(t), "owner")
	if err := store.Ensure(ctx, "s", "s"); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordImageRefusal(ctx, "", "deepseek", "m", "reason"); err != nil {
		t.Fatalf("an empty session must be a no-op, got %v", err)
	}
	if err := store.RecordImageRefusal(ctx, "s", " ", "m", "reason"); err != nil {
		t.Fatalf("an empty provider must be a no-op, got %v", err)
	}
	if err := store.RecordImageRefusal(ctx, "s", "deepseek", "", "reason"); err != nil {
		t.Fatalf("an empty model must be a no-op, got %v", err)
	}
	var rows int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM fb_session_image_refusals`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("rows = %d, want none without an identity", rows)
	}

	// A session that does not exist files nothing: the insert's guard finds no
	// row to attach to, and the answer is still not an error.
	if err := store.RecordImageRefusal(ctx, "ghost", "deepseek", "m", "reason"); err != nil {
		t.Fatalf("record for a session that does not exist: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM fb_session_image_refusals`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("rows = %d, want none for a session that does not exist", rows)
	}

	if err := store.RecordImageRefusal(ctx, "s", "deepseek", "m", "reason"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.ImageRefusal(ctx, "s", "deepseek", "m"); err != nil || !ok {
		t.Fatalf("record before delete: ok=%v err=%v", ok, err)
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM fb_sessions WHERE id='s'`); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM fb_session_image_refusals WHERE session_id='s'`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("rows after deleting the session = %d, want the record released with it", rows)
	}
}

// TestImageRefusalIsTenantData pins the agent scope: a record filed by one
// primary agent's store is not readable through another's, and asking about a
// session that belongs to someone else is refused the way the model-selection
// read refuses it.
func TestImageRefusalIsTenantData(t *testing.T) {
	ctx := context.Background()
	db := openStateDB(t)
	ours := NewSessionStore(db, "owner")
	theirs := NewSessionStore(db, "other")
	if err := ours.Ensure(ctx, "s", "s"); err != nil {
		t.Fatal(err)
	}
	if err := ours.RecordImageRefusal(ctx, "s", "deepseek", "m", "reason"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := theirs.ImageRefusal(ctx, "s", "deepseek", "m"); !errors.Is(err, ErrSessionNotOwned) || ok {
		t.Fatalf("another agent's read: ok=%v err=%v, want the ownership refusal", ok, err)
	}
	// Their own session never inherits ours: the record is keyed by the session
	// it was observed in.
	if err := theirs.Ensure(ctx, "their-session", "their-session"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := theirs.ImageRefusal(ctx, "their-session", "deepseek", "m"); err != nil || ok {
		t.Fatalf("their own session: ok=%v err=%v", ok, err)
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

// TestSessionBornUnderTheConfiguredProject pins the TUI's launch binding:
// once a surface registers its launch directory as a project and points the
// store at it, every conversation the store creates is filed under that
// project at birth.
func TestSessionBornUnderTheConfiguredProject(t *testing.T) {
	projects, sessions := newProjectStoreForTest(t, "main")
	ctx := context.Background()
	p, err := projects.Create(ctx, CreateProjectInput{Name: "launch", Root: "/tmp/launch"})
	if err != nil {
		t.Fatal(err)
	}
	sessions.ConfigureProjectDefault(p.ID)

	if err := sessions.Ensure(ctx, "talk", "talk"); err != nil {
		t.Fatal(err)
	}
	var projectID sql.NullString
	if err := sessions.db.QueryRowContext(ctx, `SELECT project_id FROM fb_sessions WHERE id='talk'`).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	if !projectID.Valid || projectID.String != p.ID {
		t.Fatalf("session project = %q valid=%v, want the configured project %q", projectID.String, projectID.Valid, p.ID)
	}
}

// TestSessionChildInheritsParentProjectAndDirectory pins the birth rule for a
// child conversation: with no project or directory of its own it takes both
// from its parent, so a subagent's memories land in the project its
// conversation belongs to rather than in the process default.
func TestSessionChildInheritsParentProjectAndDirectory(t *testing.T) {
	projects, sessions := newProjectStoreForTest(t, "main")
	ctx := context.Background()
	parent, err := projects.Create(ctx, CreateProjectInput{Name: "parent", Root: "/tmp/parent-project"})
	if err != nil {
		t.Fatal(err)
	}
	// A process default that points somewhere else: the child must ignore it
	// in favor of its parent.
	other, err := projects.Create(ctx, CreateProjectInput{Name: "other", Root: "/tmp/other-project"})
	if err != nil {
		t.Fatal(err)
	}
	sessions.ConfigureProjectDefault(other.ID)

	if err := sessions.EnsureAt(ctx, "parent-talk", "parent-talk", SessionBirth{Cwd: "/tmp/parent-project", ProjectID: parent.ID}); err != nil {
		t.Fatal(err)
	}
	if err := sessions.EnsureAt(ctx, "child", "child", SessionBirth{ParentSessionID: "parent-talk"}); err != nil {
		t.Fatal(err)
	}

	var projectID, cwd string
	if err := sessions.db.QueryRowContext(ctx, `SELECT COALESCE(project_id,''), cwd FROM fb_sessions WHERE id='child'`).Scan(&projectID, &cwd); err != nil {
		t.Fatal(err)
	}
	if projectID != parent.ID {
		t.Fatalf("child project = %q, want the parent's project %q", projectID, parent.ID)
	}
	if cwd != "/tmp/parent-project" {
		t.Fatalf("child cwd = %q, want the parent's directory %q", cwd, "/tmp/parent-project")
	}
}

// TestSessionProjectIsFixedAtBirth pins that project_id is a birth fact like
// the owning agent: re-ensuring the row does not move it, even after the
// process default changed underneath.
func TestSessionProjectIsFixedAtBirth(t *testing.T) {
	projects, sessions := newProjectStoreForTest(t, "main")
	ctx := context.Background()
	born, err := projects.Create(ctx, CreateProjectInput{Name: "born", Root: "/tmp/born"})
	if err != nil {
		t.Fatal(err)
	}
	later, err := projects.Create(ctx, CreateProjectInput{Name: "later", Root: "/tmp/later"})
	if err != nil {
		t.Fatal(err)
	}
	sessions.ConfigureProjectDefault(born.ID)
	if err := sessions.Ensure(ctx, "s1", "s1"); err != nil {
		t.Fatal(err)
	}

	// The default moves and the row is re-ensured; the birth project stays.
	sessions.ConfigureProjectDefault(later.ID)
	if err := sessions.Ensure(ctx, "s1", "renamed"); err != nil {
		t.Fatal(err)
	}
	var projectID string
	if err := sessions.db.QueryRowContext(ctx, `SELECT COALESCE(project_id,'') FROM fb_sessions WHERE id='s1'`).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	if projectID != born.ID {
		t.Fatalf("session project moved to %q, want it fixed at birth %q", projectID, born.ID)
	}
}

// TestBatchSessionIsBornWithMemoryDisabled pins the owner's rule (2026-10-26):
// a batch conversation never generates memories, so even a process whose
// memory defaults are enabled stamps a batch session disabled at birth. A
// coding session under the same defaults keeps the enabled mode.
func TestBatchSessionIsBornWithMemoryDisabled(t *testing.T) {
	projects, sessions := newProjectStoreForTest(t, "main")
	ctx := context.Background()
	sessions.ConfigureMemoryDefaults("enabled", "", "", "")
	batch, err := projects.Create(ctx, CreateProjectInput{Name: "batch", Root: t.TempDir(), ProjectType: ProjectTypeBatch})
	if err != nil {
		t.Fatal(err)
	}
	coding, err := projects.Create(ctx, CreateProjectInput{Name: "coding", Root: filepath.Join(t.TempDir(), "c"), ProjectType: ProjectTypeCoding})
	if err != nil {
		t.Fatal(err)
	}
	if err := sessions.EnsureAt(ctx, "batch-s1", "batch-s1", SessionBirth{ProjectID: batch.ID}); err != nil {
		t.Fatal(err)
	}
	if err := sessions.EnsureAt(ctx, "coding-s1", "coding-s1", SessionBirth{ProjectID: coding.ID}); err != nil {
		t.Fatal(err)
	}
	var batchMode, codingMode string
	if err := sessions.db.QueryRowContext(ctx, `SELECT memory_mode FROM fb_sessions WHERE id='batch-s1'`).Scan(&batchMode); err != nil {
		t.Fatal(err)
	}
	if batchMode != "disabled" {
		t.Fatalf("a batch session must be born with memory disabled, got %q", batchMode)
	}
	if err := sessions.db.QueryRowContext(ctx, `SELECT memory_mode FROM fb_sessions WHERE id='coding-s1'`).Scan(&codingMode); err != nil {
		t.Fatal(err)
	}
	if codingMode != "enabled" {
		t.Fatalf("a coding session must keep the process default, got %q", codingMode)
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

// TestDeleteSessionsSparesASessionALiveRunHolds pins the re-check inside
// the delete's own transaction: the sweep that listed a session as expired
// may have raced a conversation coming back to life. A session a live
// primary run holds stays whole — rows and leftovers alike — and is
// reported in Survived, while one whose run has ended, or that never had
// one, goes exactly as before.
func TestDeleteSessionsSparesASessionALiveRunHolds(t *testing.T) {
	db := openStateDB(t)
	ctx := context.Background()
	mine := NewSessionStore(db, "main")
	for _, sid := range []string{"cron-live-1", "cron-ended-1", "cron-bare-1"} {
		if err := mine.Ensure(ctx, sid, sid); err != nil {
			t.Fatal(err)
		}
	}
	living := &RunStore{DB: db, Owner: "owner-delete-live"}
	stopLive, err := living.HoldOwnerLease(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer stopLive()
	if _, err := living.CreateRun(ctx, "cron-live-1", "a person resumed it"); err != nil {
		t.Fatal(err)
	}
	ended := &RunStore{DB: db, Owner: "owner-delete-ended"}
	endedRun, err := ended.CreateRun(ctx, "cron-ended-1", "already over")
	if err != nil {
		t.Fatal(err)
	}
	if err := ended.SetStatus(ctx, endedRun.ID, RunStatusDone); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO fb_files(id, session_id, original_name, media_type, size_bytes, sha256,
		storage_backend, storage_bucket, storage_key, parse_status, parsed_text_path, parse_error, created_at, updated_at)
		VALUES('file-live', 'cron-live-1', 'report.pdf', 'application/pdf', 3, 'abc', 'local', '', 'ab/file-live.pdf', 'done', 'file-live.txt', '', 1700000000, 1700000000)`); err != nil {
		t.Fatal(err)
	}

	left, err := mine.DeleteSessions(ctx, []string{"cron-live-1", "cron-ended-1", "cron-bare-1"})
	if err != nil {
		t.Fatalf("DeleteSessions: %v", err)
	}
	if len(left.Survived) != 1 || left.Survived[0] != "cron-live-1" {
		t.Fatalf("survived = %v, want exactly cron-live-1", left.Survived)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM fb_sessions WHERE id='cron-live-1'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("a session a live run holds was deleted")
	}
	for _, sid := range []string{"cron-ended-1", "cron-bare-1"} {
		if err := db.QueryRow(`SELECT COUNT(*) FROM fb_sessions WHERE id=?`, sid).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("session %s, with no live run, was spared", sid)
		}
	}
	for _, f := range left.Files {
		if f.SessionID == "cron-live-1" {
			t.Fatalf("the spared session's file came back as a leftover: %+v", f)
		}
	}

	// The next sweep, once the run is over, takes the session whole.
	if _, err := db.Exec(`UPDATE fb_runs SET status=? WHERE session_id='cron-live-1'`, string(RunStatusDone)); err != nil {
		t.Fatal(err)
	}
	again, err := mine.DeleteSessions(ctx, []string{"cron-live-1"})
	if err != nil {
		t.Fatalf("second DeleteSessions: %v", err)
	}
	if len(again.Survived) != 0 {
		t.Fatalf("a session with no live run survived: %v", again.Survived)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM fb_sessions WHERE id='cron-live-1'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("the ended session was not deleted on the second pass")
	}
}

// LastAssistantTextForRun is the reader a scheduled fire's delivery uses now
// that a run's ending event carries no answer: the transcript is the durable
// record of a conversation's words.
func TestLastAssistantTextForRunReadsTheFinalAnswer(t *testing.T) {
	db := openStateDB(t)
	store := NewSessionStore(db, "owner")
	ctx := context.Background()
	if err := store.Ensure(ctx, "s", "s"); err != nil {
		t.Fatal(err)
	}
	run, err := (&RunStore{DB: db}).CreateRun(ctx, "s", "a turn")
	if err != nil {
		t.Fatal(err)
	}
	if text, err := store.LastAssistantTextForRun(ctx, run.ID); err != nil || text != "" {
		t.Fatalf("a run with no answer = %q (%v), want empty", text, err)
	}
	appendAssistant := func(text string) {
		t.Helper()
		if _, err := store.AppendMessageForRun(ctx, "s", run.ID, "assistant", text, "", "", MessageExecTiming{}); err != nil {
			t.Fatal(err)
		}
	}
	appendAssistant("first block")
	appendAssistant("the final answer")
	if text, err := store.LastAssistantTextForRun(ctx, run.ID); err != nil || text != "the final answer" {
		t.Fatalf("last assistant text = %q (%v)", text, err)
	}
	if text, err := store.LastAssistantTextForRun(ctx, "no-such-run"); err != nil || text != "" {
		t.Fatalf("an unknown run = %q (%v), want empty", text, err)
	}
}

// TestListMessagesBeforePagesWithoutGapsOrRepeats pins the keyset contract of
// plan 173: paging backwards by row id returns every visible row exactly once,
// each page in chronological order, never a hidden one, and the page that
// reaches the start of the transcript says so.
func TestListMessagesBeforePagesWithoutGapsOrRepeats(t *testing.T) {
	ctx := context.Background()
	db := openStateDB(t)
	store := NewSessionStore(db, "main")
	if err := store.Ensure(ctx, "s1", "s1"); err != nil {
		t.Fatal(err)
	}
	seed := func(role, visibility, content string) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO fb_messages(session_id, role, visibility, content, created_at) VALUES(?,?,?,?,?)`,
			"s1", role, visibility, content, 1); err != nil {
			t.Fatal(err)
		}
	}
	// The repaired row sits between visible ones so a page cursor has to step
	// over it, and a broken implementation would either return it or lose the
	// row behind it.
	seed("user", "visible", "one")
	seed("user", "visible", "two")
	seed("user", "repaired", "the hidden repair")
	seed("user", "visible", "three")
	seed("user", "visible", "four")
	seed("user", "visible", "five")

	var collected []string
	seenRowIDs := map[int64]bool{}
	before := int64(0)
	pages := 0
	for {
		page, hasOlder, err := store.ListMessagesBefore(ctx, "s1", before, 2)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		// Pages arrive newest-first; prefixing keeps the whole walk in
		// chronological order so the no-gaps claim reads left to right.
		var pageContents []string
		for i, m := range page {
			if seenRowIDs[m.RowID] {
				t.Fatalf("row %d returned twice across pages", m.RowID)
			}
			seenRowIDs[m.RowID] = true
			if i > 0 && page[i-1].RowID >= m.RowID {
				t.Fatalf("page not in chronological order: %v", page)
			}
			pageContents = append(pageContents, m.Content)
		}
		collected = append(pageContents, collected...)
		if !hasOlder {
			break
		}
		if len(page) == 0 {
			t.Fatal("hasOlder=true on an empty page: paging would never end")
		}
		before = page[0].RowID
		if pages > 10 {
			t.Fatal("paging did not converge")
		}
	}
	want := []string{"one", "two", "three", "four", "five"}
	if !slices.Equal(collected, want) {
		t.Fatalf("pages collected %v, want %v", collected, want)
	}
	if pages != 3 {
		t.Fatalf("pages = %d, want three pages of two at most", pages)
	}
}
