package state

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state/dbx"
	"github.com/stretchr/testify/require"
)

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

func stampActionDecidedAt(t *testing.T, db *sql.DB, id string, at int64) {
	t.Helper()
	_, err := db.ExecContext(context.Background(),
		dbx.Rebind(dbx.DialectOf(db), `UPDATE fb_actions SET updated_at=? WHERE id=?`), at, id)
	require.NoError(t, err)
}

func answeredAsk(t *testing.T, db *sql.DB, sid, prompt, choice string, at int64) *Action {
	t.Helper()
	ctx := context.Background()
	svc := &ActionService{DB: db}
	act, err := svc.CreateAsk(ctx, AskForm{SessionID: sid, Questions: []AskQuestion{{
		ID: "Scope", Prompt: prompt,
		Options: []AskOption{{ID: "A", Label: "A"}, {ID: "B", Label: "B"}},
	}}})
	require.NoError(t, err)
	_, err = svc.AnswerAsk(ctx, act.ID, AskAnswer{Answers: []AskAnswerItem{{QuestionID: "Scope", OptionIDs: []string{choice}}}})
	require.NoError(t, err)
	stampActionDecidedAt(t, db, act.ID, at)
	return act
}

func deniedPlan(t *testing.T, db *sql.DB, sid, feedback string, at int64) *Action {
	t.Helper()
	ctx := context.Background()
	svc := &ActionService{DB: db}
	act, err := svc.CreatePending(ctx, sid, "exit_plan_mode", map[string]any{"action": "exit"})
	require.NoError(t, err)
	_, err = svc.Deny(ctx, act.ID, feedback)
	require.NoError(t, err)
	stampActionDecidedAt(t, db, act.ID, at)
	return act
}

func userInputLabels(inputs []UserInput) []string {
	out := make([]string, 0, len(inputs))
	for _, in := range inputs {
		switch {
		case in.Message != nil:
			out = append(out, "msg:"+in.Message.Content)
		case in.Action != nil:
			out = append(out, in.Action.Kind+":"+string(in.Action.Status))
		}
	}
	return out
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

// The answers to a form and the feedback on a sent-back plan are the person's
// words too; they sit in the conversation where they were decided.
func TestListUserInputsMergesDecidedFormsIntoTheConversationInOrder(t *testing.T) {
	ctx := context.Background()
	db := openSessionDB(t)
	store := NewSessionStore(db, "main")
	require.NoError(t, store.Ensure(ctx, "s1", "s1"))
	svc := &ActionService{DB: db}

	appendUserRow(t, db, store, "s1", "first", false, 100)
	appendUserRow(t, db, store, "s1", "<forebrain_environment_context>", true, 101)
	answeredAsk(t, db, "s1", "Which scope?", "B", 150)
	appendUserRow(t, db, store, "s1", "second", false, 200)
	deniedPlan(t, db, "s1", "keep the old API", 250)

	// Neither a form the user walked away from nor one still waiting is
	// something they said.
	cancelled, err := svc.CreateAsk(ctx, AskForm{SessionID: "s1", Questions: []AskQuestion{{
		ID: "Other", Prompt: "Skip?", Options: []AskOption{{ID: "Y", Label: "Y"}, {ID: "N", Label: "N"}},
	}}})
	require.NoError(t, err)
	_, err = svc.Cancel(ctx, cancelled.ID, "dismissed")
	require.NoError(t, err)
	_, err = svc.CreatePending(ctx, "s1", "exit_plan_mode", map[string]any{"action": "exit"})
	require.NoError(t, err)

	got, err := store.ListUserInputs(ctx, "s1")
	require.NoError(t, err)
	require.Equal(t, []string{
		"msg:first",
		"user_interaction:answered",
		"msg:second",
		"exit_plan_mode:denied",
	}, userInputLabels(got))
	require.Equal(t, "keep the old API", got[3].Action.Error)
}

// A rewind that took the conversation back hides that stretch's messages; the
// forms decided in it go with them. A rewind that only restored files
// discarded nothing anyone said.
func TestListUserInputsLeavesOutFormsARewindTookBack(t *testing.T) {
	ctx := context.Background()
	db := openSessionDB(t)
	store := NewSessionStore(db, "main")
	require.NoError(t, store.Ensure(ctx, "s1", "s1"))

	appendUserRow(t, db, store, "s1", "kept", false, 100)
	anchor := appendUserRow(t, db, store, "s1", "taken back", false, 200)
	answeredAsk(t, db, "s1", "Inside the stretch?", "A", 250)
	_, err := store.RewindMessagesFrom(ctx, "s1", anchor)
	require.NoError(t, err)
	require.NoError(t, store.InsertRewind(ctx, RewindRecord{
		ID: "rw-1", SessionID: "s1", AnchorMessageID: anchor, Mode: "both", State: "pending", CreatedAtMs: 300_000,
	}))
	answeredAsk(t, db, "s1", "After the rewind?", "B", 350)

	codeAnchor := appendUserRow(t, db, store, "s1", "files only", false, 400)
	answeredAsk(t, db, "s1", "Before a code rewind?", "A", 450)
	require.NoError(t, store.InsertRewind(ctx, RewindRecord{
		ID: "rw-2", SessionID: "s1", AnchorMessageID: codeAnchor, Mode: "code", State: "pending", CreatedAtMs: 500_000,
	}))

	got, err := store.ListUserInputs(ctx, "s1")
	require.NoError(t, err)
	require.Equal(t, []string{
		"msg:kept",
		"user_interaction:answered",
		"msg:files only",
		"user_interaction:answered",
	}, userInputLabels(got))
	require.Equal(t, int64(350), got[1].At)
	require.Equal(t, int64(450), got[3].At)
}
