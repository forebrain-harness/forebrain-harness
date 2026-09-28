package state

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/require"
)

func TestExpirePendingExpiresOldActionsOnly(t *testing.T) {
	ctx := context.Background()
	db := openActionsDB(t)
	svc := &ActionService{DB: db}

	oldAct, err := svc.CreatePending(ctx, "conv", "approve", nil)
	if err != nil {
		t.Fatalf("CreatePending old: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE fb_actions SET created_at=? WHERE id=?`, time.Now().Add(-10*time.Minute).Unix(), oldAct.ID); err != nil {
		t.Fatalf("backdate: %v", err)
	}
	freshAct, err := svc.CreatePending(ctx, "conv", "approve", nil)
	if err != nil {
		t.Fatalf("CreatePending fresh: %v", err)
	}

	expired, err := svc.ExpirePending(ctx, time.Minute, "")
	if err != nil {
		t.Fatalf("ExpirePending: %v", err)
	}
	if len(expired) != 1 || expired[0] != oldAct.ID {
		t.Fatalf("expected [%s], got %+v", oldAct.ID, expired)
	}

	got, err := svc.Get(ctx, oldAct.ID)
	if err != nil {
		t.Fatalf("Get old: %v", err)
	}
	if got.Status != ActionExpired || got.Error != "approval_ttl_expired" {
		t.Fatalf("old not expired: %+v", got)
	}
	got2, err := svc.Get(ctx, freshAct.ID)
	if err != nil {
		t.Fatalf("Get fresh: %v", err)
	}
	if got2.Status != ActionPending {
		t.Fatalf("fresh changed: %+v", got2)
	}
}

func TestExpirePendingNoOpWhenTTLZero(t *testing.T) {
	ctx := context.Background()
	svc := &ActionService{DB: openActionsDB(t)}
	if _, err := svc.CreatePending(ctx, "conv", "k", nil); err != nil {
		t.Fatalf("create: %v", err)
	}
	expired, err := svc.ExpirePending(ctx, 0, "")
	if err != nil {
		t.Fatalf("ExpirePending: %v", err)
	}
	if expired != nil {
		t.Fatalf("expected nil ids, got %+v", expired)
	}
}

func TestExpirePendingCustomReason(t *testing.T) {
	ctx := context.Background()
	db := openActionsDB(t)
	svc := &ActionService{DB: db}
	act, _ := svc.CreatePending(ctx, "conv", "approve", nil)
	_, _ = db.ExecContext(ctx, `UPDATE fb_actions SET created_at=? WHERE id=?`, time.Now().Add(-time.Hour).Unix(), act.ID)
	if _, err := svc.ExpirePending(ctx, time.Minute, " custom_reason "); err != nil {
		t.Fatalf("ExpirePending: %v", err)
	}
	got, _ := svc.Get(ctx, act.ID)
	if got.Error != "custom_reason" {
		t.Fatalf("expected custom_reason, got %q", got.Error)
	}
}

func TestStartExpireSweeperFiresOnExpiredCallback(t *testing.T) {
	ctx := context.Background()
	db := openActionsDB(t)
	svc := &ActionService{DB: db}
	act, _ := svc.CreatePending(ctx, "conv", "approve", nil)
	_, _ = db.ExecContext(ctx, `UPDATE fb_actions SET created_at=? WHERE id=?`, time.Now().Add(-time.Hour).Unix(), act.ID)

	var (
		mu   sync.Mutex
		seen []string
		done = make(chan struct{})
	)
	stop := StartExpireSweeper(ctx, svc, ExpireSweeperConfig{
		TTL:      time.Minute,
		Interval: 20 * time.Millisecond,
		OnExpired: func(_ context.Context, id string) {
			mu.Lock()
			seen = append(seen, id)
			if len(seen) == 1 {
				close(done)
			}
			mu.Unlock()
		},
	})
	defer stop()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("OnExpired never fired")
	}
	mu.Lock()
	defer mu.Unlock()
	if seen[0] != act.ID {
		t.Fatalf("expected %s, got %+v", act.ID, seen)
	}
}

func TestStartExpireSweeperNoOpWhenDisabled(t *testing.T) {
	ctx := context.Background()
	svc := &ActionService{DB: openActionsDB(t)}
	stop := StartExpireSweeper(ctx, svc, ExpireSweeperConfig{TTL: 0})
	stop()

	stop = StartExpireSweeper(ctx, nil, ExpireSweeperConfig{TTL: time.Second})
	stop()
}

func TestAskAnswerSelectFirstOptionEachQuestion(t *testing.T) {
	got, err := AskAnswerSelectFirstOptionEachQuestion(`{
		"questions": [
			{"id":" q1 ","options":[{"id":" a ","label":"A"},{"id":"b","label":"B"}]},
			{"id":"","options":[{"id":"skip","label":"Skip"}]},
			{"id":"q2","options":[]},
			{"id":"q3","options":[{"id":" ","label":"Blank"}]}
		]
	}`)
	if err != nil {
		t.Fatalf("AskAnswerSelectFirstOptionEachQuestion error: %v", err)
	}
	if len(got.Answers) != 1 || got.Answers[0].QuestionID != "q1" || got.Answers[0].OptionIDs[0] != "a" {
		t.Fatalf("answer=%+v", got)
	}

	if _, err := AskAnswerSelectFirstOptionEachQuestion("{"); err == nil {
		t.Fatal("expected invalid json error")
	}
	if _, err := AskAnswerSelectFirstOptionEachQuestion(`{"questions":[{"id":"q","options":[]}]}`); err == nil || !strings.Contains(err.Error(), "no answerable") {
		t.Fatalf("expected no answerable error, got %v", err)
	}
}

func TestServiceLifecycle(t *testing.T) {
	ctx := context.Background()
	svc := &ActionService{DB: openActionsDB(t)}

	pending, err := svc.CreatePending(ctx, "conv", " approve ", map[string]string{"x": "y"})
	if err != nil {
		t.Fatalf("CreatePending error: %v", err)
	}
	if pending.ID == "" || pending.Kind != "approve" || pending.Status != ActionPending || !strings.Contains(pending.PayloadJSON, `"x":"y"`) {
		t.Fatalf("pending=%+v", pending)
	}

	form := validAskForm()
	ask, err := svc.CreateAsk(ctx, form)
	if err != nil {
		t.Fatalf("CreateAsk error: %v", err)
	}
	if ask.Kind != "user_interaction" || ask.Status != ActionPending {
		t.Fatalf("ask=%+v", ask)
	}

	answered, err := svc.AnswerAsk(ctx, ask.ID, AskAnswer{Answers: []AskAnswerItem{{QuestionID: "q1", OptionIDs: []string{"a"}}}})
	if err != nil {
		t.Fatalf("AnswerAsk error: %v", err)
	}
	if answered.Status != ActionAnswered || !strings.Contains(answered.AnswerJSON, `"q1"`) {
		t.Fatalf("answered=%+v", answered)
	}
	if _, err := svc.AnswerAsk(ctx, ask.ID, AskAnswer{}); !errors.Is(err, ErrNotPending) {
		t.Fatalf("expected ask ErrNotPending, got %v", err)
	}

	approved, err := svc.ApproveWithAnswer(ctx, pending.ID, " yes ", `{"scope":"session"}`)
	if err != nil {
		t.Fatalf("ApproveWithAnswer error: %v", err)
	}
	if approved.Status != ActionApproved || approved.Error != "yes" || approved.AnswerJSON != `{"scope":"session"}` {
		t.Fatalf("approved=%+v", approved)
	}
	if _, err := svc.Approve(ctx, pending.ID, "again"); !errors.Is(err, ErrNotPending) {
		t.Fatalf("expected ErrNotPending, got %v", err)
	}

	denyTarget, err := svc.CreatePending(ctx, "conv", "deny", nil)
	if err != nil {
		t.Fatalf("CreatePending deny error: %v", err)
	}
	denied, err := svc.Deny(ctx, denyTarget.ID, " no ")
	if err != nil {
		t.Fatalf("Deny error: %v", err)
	}
	if denied.Status != ActionDenied || denied.Error != "no" {
		t.Fatalf("denied=%+v", denied)
	}
	if _, err := svc.Deny(ctx, denyTarget.ID, "again"); !errors.Is(err, ErrNotPending) {
		t.Fatalf("expected deny ErrNotPending, got %v", err)
	}

	cancelTarget, err := svc.CreatePending(ctx, "conv", "cancel", nil)
	if err != nil {
		t.Fatalf("CreatePending cancel error: %v", err)
	}
	cancelled, err := svc.Cancel(ctx, cancelTarget.ID, "")
	if err != nil {
		t.Fatalf("Cancel error: %v", err)
	}
	if cancelled.Status != ActionCancelled || cancelled.Error != "cancelled" {
		t.Fatalf("cancelled=%+v", cancelled)
	}
	if _, err := svc.Cancel(ctx, cancelTarget.ID, "again"); !errors.Is(err, ErrNotPending) {
		t.Fatalf("expected cancel ErrNotPending, got %v", err)
	}

	denyIfTarget, err := svc.CreatePending(ctx, "conv", "deny-if", nil)
	if err != nil {
		t.Fatalf("CreatePending deny-if error: %v", err)
	}
	ok, err := svc.DenyIfPending(ctx, denyIfTarget.ID, "no")
	if err != nil || !ok {
		t.Fatalf("DenyIfPending ok=%v err=%v", ok, err)
	}
	ok, err = svc.DenyIfPending(ctx, denyIfTarget.ID, "again")
	if err != nil || ok {
		t.Fatalf("DenyIfPending second ok=%v err=%v", ok, err)
	}

	all, err := svc.List(ctx, "main", ActionFilter{}, 0)
	if err != nil {
		t.Fatalf("List all error: %v", err)
	}
	if len(all) != 5 {
		t.Fatalf("all len=%d actions=%+v", len(all), all)
	}
	answeredOnly, err := svc.List(ctx, "main", ActionFilter{Status: string(ActionAnswered)}, 10)
	if err != nil {
		t.Fatalf("List answered error: %v", err)
	}
	if len(answeredOnly) != 1 || answeredOnly[0].ID != ask.ID {
		t.Fatalf("answeredOnly=%+v", answeredOnly)
	}

	got, err := svc.Get(ctx, " "+ask.ID+" ")
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if got.ID != ask.ID {
		t.Fatalf("got=%+v", got)
	}
}

func TestServiceValidationAndErrors(t *testing.T) {
	ctx := context.Background()
	var nilSvc *ActionService
	if _, err := nilSvc.CreatePending(ctx, "conv", "kind", nil); err == nil {
		t.Fatal("expected nil db CreatePending error")
	}
	if _, err := nilSvc.CreateAsk(ctx, validAskForm()); err == nil {
		t.Fatal("expected nil db CreateAsk error")
	}
	if _, err := nilSvc.List(ctx, "main", ActionFilter{}, 1); err == nil {
		t.Fatal("expected nil db List error")
	}
	if _, err := nilSvc.Get(ctx, "id"); err == nil {
		t.Fatal("expected nil db Get error")
	}
	if _, err := nilSvc.AnswerAsk(ctx, "id", AskAnswer{}); err == nil {
		t.Fatal("expected nil db AnswerAsk error")
	}
	if _, err := nilSvc.Approve(ctx, "id", ""); err == nil {
		t.Fatal("expected nil db Approve error")
	}
	if _, err := nilSvc.Deny(ctx, "id", ""); err == nil {
		t.Fatal("expected nil db Deny error")
	}
	if _, err := nilSvc.DenyIfPending(ctx, "id", ""); err == nil {
		t.Fatal("expected nil db DenyIfPending error")
	}

	svc := &ActionService{DB: openActionsDB(t)}
	if _, err := svc.CreatePending(ctx, "conv", " ", nil); err == nil {
		t.Fatal("expected kind required error")
	}
	if _, err := svc.CreatePending(ctx, "conv", "kind", func() {}); err == nil {
		t.Fatal("expected marshal error")
	}
	if _, err := svc.CreateAsk(ctx, AskForm{}); err == nil {
		t.Fatal("expected questions required error")
	}
	badForms := []AskForm{
		{Questions: []AskQuestion{{ID: "", Prompt: "p", Options: []AskOption{{ID: "a", Label: "A"}, {ID: "b", Label: "B"}}}}},
		{Questions: []AskQuestion{{ID: "q", Prompt: "", Options: []AskOption{{ID: "a", Label: "A"}, {ID: "b", Label: "B"}}}}},
		{Questions: []AskQuestion{{ID: "q", Prompt: "p", Options: []AskOption{{ID: "a", Label: "A"}}}}},
		{Questions: []AskQuestion{{ID: "q", Prompt: "p", Options: []AskOption{{ID: "", Label: "A"}, {ID: "b", Label: "B"}}}}},
		{Questions: []AskQuestion{{ID: "q", Prompt: "p", Options: []AskOption{{ID: "a", Label: ""}, {ID: "b", Label: "B"}}}}},
	}
	for _, form := range badForms {
		if _, err := svc.CreateAsk(ctx, form); err == nil {
			t.Fatalf("expected invalid form error for %+v", form)
		}
	}

	if _, err := svc.Get(ctx, " "); !errors.Is(err, ErrActionNotFound) {
		t.Fatalf("blank get err=%v", err)
	}
	if _, err := svc.Get(ctx, "missing"); !errors.Is(err, ErrActionNotFound) {
		t.Fatalf("missing get err=%v", err)
	}
	if _, err := svc.AnswerAsk(ctx, " ", AskAnswer{}); !errors.Is(err, ErrActionNotFound) {
		t.Fatalf("blank answer err=%v", err)
	}
	if _, err := svc.Approve(ctx, " ", ""); !errors.Is(err, ErrActionNotFound) {
		t.Fatalf("blank approve err=%v", err)
	}
	if _, err := svc.Deny(ctx, " ", ""); !errors.Is(err, ErrActionNotFound) {
		t.Fatalf("blank deny err=%v", err)
	}
	if _, err := svc.DenyIfPending(ctx, " ", ""); !errors.Is(err, ErrActionNotFound) {
		t.Fatalf("blank deny-if err=%v", err)
	}
}

func TestServiceDatabaseErrors(t *testing.T) {
	ctx := context.Background()
	db := openActionsDB(t)
	svc := &ActionService{DB: db}
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	if _, err := svc.CreatePending(ctx, "conv", "kind", nil); err == nil {
		t.Fatal("expected CreatePending db error")
	}
	if _, err := svc.CreateAsk(ctx, validAskForm()); err == nil {
		t.Fatal("expected CreateAsk db error")
	}
	if _, err := svc.List(ctx, "main", ActionFilter{}, 1); err == nil {
		t.Fatal("expected List db error")
	}
	if _, err := svc.List(ctx, "main", ActionFilter{Status: "pending"}, 1); err == nil {
		t.Fatal("expected filtered List db error")
	}
	if _, err := svc.Get(ctx, "id"); err == nil {
		t.Fatal("expected Get db error")
	}
	if _, err := svc.AnswerAsk(ctx, "id", AskAnswer{}); err == nil {
		t.Fatal("expected AnswerAsk db error")
	}
	if _, err := svc.Approve(ctx, "id", ""); err == nil {
		t.Fatal("expected Approve db error")
	}
	if _, err := svc.Deny(ctx, "id", ""); err == nil {
		t.Fatal("expected Deny db error")
	}
	if _, err := svc.DenyIfPending(ctx, "id", ""); err == nil {
		t.Fatal("expected DenyIfPending db error")
	}
}

func validAskForm() AskForm {
	return AskForm{
		SessionID: "conv",
		Questions: []AskQuestion{{
			ID:     "q1",
			Prompt: "Choose",
			Options: []AskOption{
				{ID: "a", Label: "A"},
				{ID: "b", Label: "B"},
			},
		}},
	}
}

func openActionsDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := OpenStateForTest(context.Background(), filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ensureSessionsForRuns(t, db, "conv")
	return db
}

// TestActivePrimaryRunIsTenantScoped pins D13's replacement: the newest
// top-level run of this agent that is still running or waiting on an action —
// found by tenant index, not by scanning every tenant's recent runs and
// truncating.
func TestActivePrimaryRunIsTenantScoped(t *testing.T) {
	ctx := context.Background()
	db, err := OpenStateForTest(ctx, filepath.Join(t.TempDir(), "s.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	sessions := NewSessionStore(db, "main")
	must(sessions.Ensure(ctx, "s1", "s1"))
	other := NewSessionStore(db, "other")
	must(other.Ensure(ctx, "s-other", "s-other"))
	svc := &RunStore{DB: db}
	ensureSessionsForRuns(t, db, "s1")
	done, err := svc.CreateRun(ctx, "s1", "finished")
	must(err)
	must(svc.SetStatus(ctx, done.ID, RunStatusDone))
	// Another tenant's run is newer and still active; it must never surface as
	// this agent's cancellable run.
	foreign, err := svc.CreateRun(ctx, "s-other", "foreign active")
	must(err)
	// A subagent run under the old run is active but not top-level.
	_, err = svc.CreateSubagentRun(ctx, done.ID, "s1", "child")
	must(err)
	mine, err := svc.CreateRun(ctx, "s1", "waiting on approval")
	must(err)
	must(svc.SetStatus(ctx, mine.ID, RunStatusWaitingAction))

	got, ok, err := svc.ActivePrimaryRun(ctx, "main")
	if err != nil || !ok {
		t.Fatalf("ActivePrimaryRun = %v, %v; want the waiting run", got, err)
	}
	if got.ID != mine.ID {
		t.Fatalf("ActivePrimaryRun = %s, want %s (not the done run %s, the foreign run %s, or a subagent)", got.ID, mine.ID, done.ID, foreign.ID)
	}
	if _, ok, err := svc.ActivePrimaryRun(ctx, "nobody"); err != nil || ok {
		t.Fatalf("ActivePrimaryRun for an agent with no runs = %v, %v; want none", ok, err)
	}
}

func TestListRunsBySession(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "s.sqlite")
	db, err := OpenStateForTest(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc := &RunStore{DB: db}
	ensureSessionsForRuns(t, db, "s1", "s2")
	r1, err := svc.CreateRun(ctx, "s1", "hello")
	if err != nil {
		t.Fatal(err)
	}
	r2, err := svc.CreateRun(ctx, "s2", "world")
	if err != nil {
		t.Fatal(err)
	}
	list, err := svc.ListRunsBySession(ctx, "s1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("len=%d want 1", len(list))
	}
	if list[0].ID != r1.ID {
		t.Fatalf("id=%s want %s", list[0].ID, r1.ID)
	}
	if list[0].ID == r2.ID {
		t.Fatalf("unexpected run from other session: %s", r2.ID)
	}
}

// appendMsg persists one llm.Message as a transcript row the same way the run
// loop does, returning the row ID.
func appendMsg(t *testing.T, s *SessionStore, sessionID string, msg llm.Message) int64 {
	t.Helper()
	ctx := context.Background()
	content := msg.TextContent()
	toolStepID := ""
	if msg.Role == llm.RoleTool {
		toolStepID = msg.ToolCallID
	}
	id, err := s.AppendStructuredMessage(ctx, sessionID, msg.Role, content, "", MessagePartsJSON(msg, content), "", "", toolStepID, "", MessageExecTiming{})
	if err != nil {
		t.Fatalf("AppendStructuredMessage(%s): %v", msg.Role, err)
	}
	return id
}

func assistantToolCall(text, callID, name, args string) llm.Message {
	var parts []llm.ContentPart
	if text != "" {
		parts = []llm.ContentPart{llm.Text(text)}
	}
	return llm.AssistantMessage(parts, llm.ToolCall{
		ID:       callID,
		Type:     llm.ToolTypeFunction,
		Function: llm.FunctionCall{Name: name, Arguments: args},
	})
}

func transcriptToolCallIDs(t *testing.T, s *SessionStore, sessionID string) (assistantCalls, toolResults int) {
	t.Helper()
	msgs, err := s.ListTranscriptMessages(context.Background(), sessionID, 5000)
	if err != nil {
		t.Fatalf("ListTranscriptMessages: %v", err)
	}
	for _, m := range msgs {
		if m.Role == llm.RoleAssistant {
			assistantCalls += len(m.ToolCalls)
		}
		if m.Role == llm.RoleTool {
			toolResults++
		}
	}
	return assistantCalls, toolResults
}

// TestRepairDanglingToolResultsStripsUnansweredCalls reproduces the DeepSeek 400
// ("An assistant message with 'tool_calls' must be followed by tool messages"):
// a dismissed approval leaves an assistant tool_calls row mid-history with user
// turns after it and no tool-result row. After repair, no assistant tool_call is
// left unanswered.
func TestRepairDanglingToolResultsStripsUnansweredCalls(t *testing.T) {
	ctx := context.Background()
	s := NewSessionStore(openSessionDB(t), "main")
	if err := s.Ensure(ctx, "sess", "sess"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	appendMsg(t, s, "sess", llm.UserMessage(llm.Text("delete renderStep")))
	// Assistant requested an edit that needed approval; the snapshot persisted
	// the tool_calls row but the user dismissed approval -> no tool result.
	appendMsg(t, s, "sess", assistantToolCall("I need to edit wizard.go", "call_dangling", "edit_file", `{"path":"wizard.go"}`))
	// User redirected with a new message (mid-history dangling row).
	appendMsg(t, s, "sess", llm.UserMessage(llm.Text("keep renderTimeline")))

	if calls, results := transcriptToolCallIDs(t, s, "sess"); calls != 1 || results != 0 {
		t.Fatalf("pre-repair: assistantCalls=%d toolResults=%d, want 1/0", calls, results)
	}

	repaired, err := s.RepairDanglingToolResults(ctx, "sess")
	if err != nil {
		t.Fatalf("RepairDanglingToolResults: %v", err)
	}
	if repaired != 1 {
		t.Fatalf("repaired=%d, want 1", repaired)
	}

	// The assistant row had text, so it is kept but its dangling call stripped.
	msgs, err := s.ListTranscriptMessages(ctx, "sess", 5000)
	if err != nil {
		t.Fatalf("ListTranscriptMessages: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("messages=%d, want 3 (rows preserved)", len(msgs))
	}
	for i, m := range msgs {
		if m.Role == llm.RoleAssistant && len(m.ToolCalls) != 0 {
			t.Fatalf("msg[%d] still has %d unanswered tool_calls", i, len(m.ToolCalls))
		}
	}
	if got := msgs[1].TextContent(); got != "I need to edit wizard.go" {
		t.Fatalf("assistant text not preserved: %q", got)
	}
}

// TestRepairDanglingToolResultsPreservesAnsweredCalls verifies a complete
// assistant->tool cycle is left untouched.
func TestRepairDanglingToolResultsPreservesAnsweredCalls(t *testing.T) {
	ctx := context.Background()
	s := NewSessionStore(openSessionDB(t), "main")
	if err := s.Ensure(ctx, "sess", "sess"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	appendMsg(t, s, "sess", llm.UserMessage(llm.Text("read the file")))
	appendMsg(t, s, "sess", assistantToolCall("", "call_ok", "read_file", `{"path":"a.go"}`))
	appendMsg(t, s, "sess", llm.ToolResultMessage("call_ok", llm.Text("file contents")))
	appendMsg(t, s, "sess", llm.AssistantMessage([]llm.ContentPart{llm.Text("done")}))

	repaired, err := s.RepairDanglingToolResults(ctx, "sess")
	if err != nil {
		t.Fatalf("RepairDanglingToolResults: %v", err)
	}
	if repaired != 0 {
		t.Fatalf("repaired=%d, want 0 (nothing dangling)", repaired)
	}
	if calls, results := transcriptToolCallIDs(t, s, "sess"); calls != 1 || results != 1 {
		t.Fatalf("post-repair: assistantCalls=%d toolResults=%d, want 1/1", calls, results)
	}
}

// TestRepairDanglingToolResultsDropsEmptyAssistant verifies an assistant row
// whose only content was an unanswered tool_call (no text) is excluded from the
// transcript entirely, since stripping leaves nothing valid to send.
func TestRepairDanglingToolResultsDropsEmptyAssistant(t *testing.T) {
	ctx := context.Background()
	s := NewSessionStore(openSessionDB(t), "main")
	if err := s.Ensure(ctx, "sess", "sess"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	appendMsg(t, s, "sess", llm.UserMessage(llm.Text("go")))
	appendMsg(t, s, "sess", assistantToolCall("", "call_empty", "example_tool", `{"q":"x"}`))

	repaired, err := s.RepairDanglingToolResults(ctx, "sess")
	if err != nil {
		t.Fatalf("RepairDanglingToolResults: %v", err)
	}
	if repaired != 1 {
		t.Fatalf("repaired=%d, want 1", repaired)
	}
	msgs, err := s.ListTranscriptMessages(ctx, "sess", 5000)
	if err != nil {
		t.Fatalf("ListTranscriptMessages: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Role != llm.RoleUser {
		t.Fatalf("messages=%+v, want only the user row", msgs)
	}
}

// assertTranscriptToolPairing fails if the transcript is not valid to re-send to
// an OpenAI-compatible provider: every role:tool row must answer a tool_call
// declared by the most recent preceding assistant batch (and only once), and
// every assistant tool_call must be answered. These are the two reciprocal
// invariants DeepSeek enforces ("Messages with role 'tool' must be a response to
// a preceding message with 'tool_calls'" and its dual).
func assertTranscriptToolPairing(t *testing.T, s *SessionStore, sessionID string) {
	t.Helper()
	msgs, err := s.ListTranscriptMessages(context.Background(), sessionID, 5000)
	if err != nil {
		t.Fatalf("ListTranscriptMessages: %v", err)
	}
	open := map[string]struct{}{}
	for i, m := range msgs {
		switch m.Role {
		case llm.RoleAssistant:
			if len(open) != 0 {
				t.Fatalf("msg[%d]: assistant batch left %d unanswered tool_calls before next assistant", i, len(open))
			}
			open = map[string]struct{}{}
			for _, c := range m.ToolCalls {
				open[c.ID] = struct{}{}
			}
		case llm.RoleTool:
			if _, ok := open[m.ToolCallID]; !ok {
				t.Fatalf("msg[%d]: orphan tool result %q does not answer the preceding assistant batch", i, m.ToolCallID)
			}
			delete(open, m.ToolCallID)
		default:
			if len(open) != 0 {
				t.Fatalf("msg[%d]: %s message with %d unanswered tool_calls still open", i, m.Role, len(open))
			}
		}
	}
	if len(open) != 0 {
		t.Fatalf("transcript ends with %d unanswered tool_calls", len(open))
	}
}

// TestRepairDanglingToolResultsDropsOrphanAndDuplicateRows reproduces the
// DeepSeek 400 "Messages with role 'tool' must be a response to a preceding
// message with 'tool_calls'". Across a parallel-approval resume, the persisted
// transcript accumulates a duplicated assistant tool_calls row and tool-result
// rows stitched after the WRONG assistant (the divergent-suffix append in
// AppendMessageSequence). The orphan rows and the empty duplicate assistant must
// be excluded so the transcript is valid to re-send.
func TestRepairDanglingToolResultsDropsOrphanAndDuplicateRows(t *testing.T) {
	ctx := context.Background()
	s := NewSessionStore(openSessionDB(t), "main")
	if err := s.Ensure(ctx, "sess", "sess"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	appendMsg(t, s, "sess", llm.UserMessage(llm.Text("coverage please")))
	// Batch 1 [B,C], results persisted in completion order then re-stitched.
	appendMsg(t, s, "sess", llm.AssistantMessage(nil,
		llm.ToolCall{ID: "B", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "shell", Arguments: "{}"}},
		llm.ToolCall{ID: "C", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "example_tool", Arguments: "{}"}},
	))
	appendMsg(t, s, "sess", llm.ToolResultMessage("C", llm.Text("c-out")))
	appendMsg(t, s, "sess", llm.ToolResultMessage("B", llm.Text("b-out")))
	appendMsg(t, s, "sess", llm.ToolResultMessage("C", llm.Text("c-out"))) // duplicate answer
	// Empty duplicate assistant [D,E] with B,C results misattached after it.
	appendMsg(t, s, "sess", llm.AssistantMessage(nil,
		llm.ToolCall{ID: "D", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "shell", Arguments: "{}"}},
		llm.ToolCall{ID: "E", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "shell", Arguments: "{}"}},
	))
	appendMsg(t, s, "sess", llm.ToolResultMessage("B", llm.Text("b-out"))) // orphan (B not in D,E)
	appendMsg(t, s, "sess", llm.ToolResultMessage("C", llm.Text("c-out"))) // orphan
	// The real [D,E] batch with its results.
	appendMsg(t, s, "sess", llm.AssistantMessage(nil,
		llm.ToolCall{ID: "D", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "shell", Arguments: "{}"}},
		llm.ToolCall{ID: "E", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "shell", Arguments: "{}"}},
	))
	appendMsg(t, s, "sess", llm.ToolResultMessage("D", llm.Text("d-out")))
	appendMsg(t, s, "sess", llm.ToolResultMessage("E", llm.Text("e-out")))

	if _, err := s.RepairDanglingToolResults(ctx, "sess"); err != nil {
		t.Fatalf("RepairDanglingToolResults: %v", err)
	}

	assertTranscriptToolPairing(t, s, "sess")

	// Repair must be idempotent: a second pass changes nothing.
	if repaired, err := s.RepairDanglingToolResults(ctx, "sess"); err != nil {
		t.Fatalf("RepairDanglingToolResults (2nd): %v", err)
	} else if repaired != 0 {
		t.Fatalf("second repair changed %d rows, want 0 (not idempotent)", repaired)
	}
}

// TestRepairDanglingToolResultsKeepsParallelResultsRegardlessOfOrder verifies a
// valid parallel batch whose results are stored out of assistant order is left
// intact (order among sibling tool results is not significant to the provider).
func TestRepairDanglingToolResultsKeepsParallelResultsRegardlessOfOrder(t *testing.T) {
	ctx := context.Background()
	s := NewSessionStore(openSessionDB(t), "main")
	if err := s.Ensure(ctx, "sess", "sess"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	appendMsg(t, s, "sess", llm.UserMessage(llm.Text("go")))
	appendMsg(t, s, "sess", llm.AssistantMessage([]llm.ContentPart{llm.Text("batch")},
		llm.ToolCall{ID: "x", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "read_file", Arguments: "{}"}},
		llm.ToolCall{ID: "y", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "example_tool", Arguments: "{}"}},
	))
	appendMsg(t, s, "sess", llm.ToolResultMessage("y", llm.Text("y")))
	appendMsg(t, s, "sess", llm.ToolResultMessage("x", llm.Text("x")))

	repaired, err := s.RepairDanglingToolResults(ctx, "sess")
	if err != nil {
		t.Fatalf("RepairDanglingToolResults: %v", err)
	}
	if repaired != 0 {
		t.Fatalf("repaired=%d, want 0 (valid out-of-order batch)", repaired)
	}
	if calls, results := transcriptToolCallIDs(t, s, "sess"); calls != 2 || results != 2 {
		t.Fatalf("post-repair: assistantCalls=%d toolResults=%d, want 2/2", calls, results)
	}
}

// TestRepairDanglingToolResultsPartialBatch verifies that when an assistant
// issues two calls but only one was answered, the answered call and its result
// survive while the unanswered one is stripped.
func TestRepairDanglingToolResultsPartialBatch(t *testing.T) {
	ctx := context.Background()
	s := NewSessionStore(openSessionDB(t), "main")
	if err := s.Ensure(ctx, "sess", "sess"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	appendMsg(t, s, "sess", llm.UserMessage(llm.Text("go")))
	twoCalls := llm.AssistantMessage(
		[]llm.ContentPart{llm.Text("batch")},
		llm.ToolCall{ID: "call_a", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "read_file", Arguments: "{}"}},
		llm.ToolCall{ID: "call_b", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "example_tool", Arguments: "{}"}},
	)
	appendMsg(t, s, "sess", twoCalls)
	appendMsg(t, s, "sess", llm.ToolResultMessage("call_a", llm.Text("ok")))
	appendMsg(t, s, "sess", llm.UserMessage(llm.Text("next")))

	repaired, err := s.RepairDanglingToolResults(ctx, "sess")
	if err != nil {
		t.Fatalf("RepairDanglingToolResults: %v", err)
	}
	if repaired != 1 {
		t.Fatalf("repaired=%d, want 1", repaired)
	}
	msgs, err := s.ListTranscriptMessages(ctx, "sess", 5000)
	if err != nil {
		t.Fatalf("ListTranscriptMessages: %v", err)
	}
	var asst llm.Message
	for _, m := range msgs {
		if m.Role == llm.RoleAssistant {
			asst = m
		}
	}
	if len(asst.ToolCalls) != 1 || asst.ToolCalls[0].ID != "call_a" {
		t.Fatalf("assistant tool_calls=%+v, want only call_a", asst.ToolCalls)
	}
}

func TestStoreAppendAndReadTranscript(t *testing.T) {
	s := NewSessionStore(openSessionDB(t), "main")
	ctx := context.Background()
	if _, err := s.Append(ctx, "s", "user", "hello"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(ctx, "s", "assistant", "world"); err != nil {
		t.Fatal(err)
	}
	msgs, err := s.ListTranscriptMessages(ctx, "s", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[0].TextContent() != "hello" || msgs[1].TextContent() != "world" {
		t.Fatalf("messages=%+v", msgs)
	}
}

func TestEnsurePersistsConfiguredMemoryMetadata(t *testing.T) {
	s := NewSessionStore(openSessionDB(t), "main")
	s.ConfigureMemoryDefaults("disabled", "tui", "/work/project", "feature/memory")
	if err := s.Ensure(context.Background(), "session-1", "Session 1"); err != nil {
		t.Fatal(err)
	}
	var mode, source, cwd, gitBranch string
	if err := s.db.QueryRow(`SELECT memory_mode, memory_source, cwd, git_branch FROM fb_sessions WHERE id=?`, "session-1").Scan(&mode, &source, &cwd, &gitBranch); err != nil {
		t.Fatal(err)
	}
	if mode != "disabled" || source != "tui" || cwd != "/work/project" || gitBranch != "feature/memory" {
		t.Fatalf("unexpected memory metadata mode=%q source=%q cwd=%q branch=%q", mode, source, cwd, gitBranch)
	}
}

func TestReplacementCheckpointProjectsExactHistoryAndSuffix(t *testing.T) {
	ctx := context.Background()
	s := NewSessionStore(openSessionDB(t), "main")
	_, _ = s.Append(ctx, "checkpoint", "user", "old transcript")
	replacement := []llm.Message{
		llm.UserMessage(llm.Text("retained user")), llm.UserMessage(llm.Text("handoff summary")),
		{Compaction: &llm.CompactionState{ID: "cmp_1", EncryptedContent: "opaque"}},
	}
	text := "Session compacted"
	part := CompactBoundaryPart{Trigger: "manual", Strategy: "remote_v2", ReplacementHistory: replacement, WindowNumber: 1, FirstWindowID: "w1", WindowID: "w1"}
	if err := s.AppendCompactCheckpoint(ctx, "checkpoint", text, part); err != nil {
		t.Fatal(err)
	}
	_, _ = s.Append(ctx, "checkpoint", "assistant", "future answer")
	msgs, err := s.ListTranscriptMessages(ctx, "checkpoint", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 4 || msgs[0].TextContent() != "retained user" || msgs[1].TextContent() != "handoff summary" || msgs[2].Compaction == nil || msgs[3].TextContent() != "future answer" {
		t.Fatalf("projected=%+v", msgs)
	}
	all, err := s.ListAllMessages(ctx, "checkpoint", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || all[0].Content != "old transcript" {
		t.Fatalf("append-only=%+v", all)
	}
}

func TestMemoryTranscriptIgnoresCompactionProjection(t *testing.T) {
	ctx := context.Background()
	s := NewSessionStore(openSessionDB(t), "main")
	_, _ = s.Append(ctx, "memory-history", "user", "old transcript evidence")
	text := "Session compacted"
	part := CompactBoundaryPart{Trigger: "manual", Strategy: "local", ReplacementHistory: []llm.Message{llm.UserMessage(llm.Text("compressed summary"))}, WindowNumber: 1, FirstWindowID: "w1", WindowID: "w1"}
	if err := s.AppendCompactCheckpoint(ctx, "memory-history", text, part); err != nil {
		t.Fatal(err)
	}
	_, _ = s.Append(ctx, "memory-history", "assistant", "new answer")

	messages, err := s.ListMemoryTranscriptMessages(ctx, "memory-history", 5000)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].TextContent() != "old transcript evidence" || messages[1].TextContent() != "new answer" {
		t.Fatalf("memory transcript = %#v", messages)
	}
}

func TestContextResetSupersedesCheckpoint(t *testing.T) {
	ctx := context.Background()
	s := NewSessionStore(openSessionDB(t), "main")
	_, _ = s.Append(ctx, "s", "user", "old")
	text := "Session compacted"
	part := CompactBoundaryPart{Trigger: "manual", Strategy: "local", ReplacementHistory: []llm.Message{llm.UserMessage(llm.Text("summary"))}, WindowNumber: 1, FirstWindowID: "w", WindowID: "w"}
	if err := s.AppendCompactCheckpoint(ctx, "s", text, part); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetSessionContextResetToLatest(ctx, "s"); err != nil {
		t.Fatal(err)
	}
	msgs, err := s.ListTranscriptMessages(ctx, "s", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 0 {
		t.Fatalf("messages=%+v", msgs)
	}
}

func openSessionDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := OpenStateForTest(context.Background(), filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestAutoGenerateTitleFromFirstUserMessage(t *testing.T) {
	s := NewSessionStore(openSessionDB(t), "main")
	ctx := context.Background()

	// First user message should generate title
	_, err := s.Append(ctx, "test-session", "user", "How do I implement authentication?")
	if err != nil {
		t.Fatal(err)
	}

	// Check title was generated
	var title string
	err = s.db.QueryRowContext(ctx, `SELECT title FROM fb_sessions WHERE id=?`, "test-session").Scan(&title)
	if err != nil {
		t.Fatal(err)
	}

	if title == "test-session" {
		t.Errorf("expected auto-generated title, got session ID: %s", title)
	}

	if title != "How do I implement authentication?" {
		t.Errorf("expected title 'How do I implement authentication?', got: %s", title)
	}
}

func TestTitleTruncation(t *testing.T) {
	s := NewSessionStore(openSessionDB(t), "main")
	ctx := context.Background()

	longInput := "This is a very long user message that should be truncated to a reasonable length for display in the session list"
	_, err := s.Append(ctx, "long-session", "user", longInput)
	if err != nil {
		t.Fatal(err)
	}

	var title string
	err = s.db.QueryRowContext(ctx, `SELECT title FROM fb_sessions WHERE id=?`, "long-session").Scan(&title)
	if err != nil {
		t.Fatal(err)
	}

	if len([]rune(title)) > 53 { // 50 + "..."
		t.Errorf("title too long: %d runes, got: %s", len([]rune(title)), title)
	}

	if title[len(title)-3:] != "..." {
		t.Errorf("expected truncated title to end with '...', got: %s", title)
	}
}

func TestTitleFromMultilineInput(t *testing.T) {
	s := NewSessionStore(openSessionDB(t), "main")
	ctx := context.Background()

	multiline := "First line is the title\nSecond line should be ignored\nThird line too"
	_, err := s.Append(ctx, "multiline-session", "user", multiline)
	if err != nil {
		t.Fatal(err)
	}

	var title string
	err = s.db.QueryRowContext(ctx, `SELECT title FROM fb_sessions WHERE id=?`, "multiline-session").Scan(&title)
	if err != nil {
		t.Fatal(err)
	}

	if title != "First line is the title" {
		t.Errorf("expected first line as title, got: %s", title)
	}
}

func TestTitleSkipsSlashCommand(t *testing.T) {
	s := NewSessionStore(openSessionDB(t), "main")
	ctx := context.Background()

	slashInput := "/review Please review this code"
	_, err := s.Append(ctx, "slash-session", "user", slashInput)
	if err != nil {
		t.Fatal(err)
	}

	var title string
	err = s.db.QueryRowContext(ctx, `SELECT title FROM fb_sessions WHERE id=?`, "slash-session").Scan(&title)
	if err != nil {
		t.Fatal(err)
	}

	if title != "/review Please review this code" {
		t.Errorf("expected full first line as title, got: %s", title)
	}
}

func TestSecondUserMessageDoesNotChangeTitle(t *testing.T) {
	s := NewSessionStore(openSessionDB(t), "main")
	ctx := context.Background()

	// First message
	_, err := s.Append(ctx, "stable-session", "user", "First message")
	if err != nil {
		t.Fatal(err)
	}

	var firstTitle string
	err = s.db.QueryRowContext(ctx, `SELECT title FROM fb_sessions WHERE id=?`, "stable-session").Scan(&firstTitle)
	if err != nil {
		t.Fatal(err)
	}

	// Second message
	_, err = s.Append(ctx, "stable-session", "user", "Second message should not change title")
	if err != nil {
		t.Fatal(err)
	}

	var secondTitle string
	err = s.db.QueryRowContext(ctx, `SELECT title FROM fb_sessions WHERE id=?`, "stable-session").Scan(&secondTitle)
	if err != nil {
		t.Fatal(err)
	}

	if firstTitle != secondTitle {
		t.Errorf("title changed from %q to %q", firstTitle, secondTitle)
	}
}

func TestAssistantMessageDoesNotGenerateTitle(t *testing.T) {
	s := NewSessionStore(openSessionDB(t), "main")
	ctx := context.Background()

	// Assistant message first (shouldn't generate title)
	_, err := s.Append(ctx, "assistant-first", "assistant", "Hello, how can I help?")
	if err != nil {
		t.Fatal(err)
	}

	var title string
	err = s.db.QueryRowContext(ctx, `SELECT title FROM fb_sessions WHERE id=?`, "assistant-first").Scan(&title)
	if err != nil {
		t.Fatal(err)
	}

	if title != "assistant-first" {
		t.Errorf("expected session ID as title, got: %s", title)
	}
}

func TestListChildRunsFiltersByParentAndStatus(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := OpenStateForTest(ctx, filepath.Join(dir, "state.sqlite"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	svc := &RunStore{DB: db}
	ensureSessionsForRuns(t, db, "s1")
	parent, err := svc.CreateRun(ctx, "s1", "parent")
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	childA, err := svc.CreateSubagentRun(ctx, parent.ID, "s1", "child-a")
	if err != nil {
		t.Fatalf("create child a: %v", err)
	}
	childB, err := svc.CreateSubagentRun(ctx, parent.ID, "s1", "child-b")
	if err != nil {
		t.Fatalf("create child b: %v", err)
	}
	otherParent, err := svc.CreateRun(ctx, "s1", "other")
	if err != nil {
		t.Fatalf("create other parent: %v", err)
	}
	if _, err := svc.CreateSubagentRun(ctx, otherParent.ID, "s1", "other-child"); err != nil {
		t.Fatalf("create other child: %v", err)
	}
	if err := svc.SetStatus(ctx, childA.ID, RunStatusDone); err != nil {
		t.Fatalf("set child a done: %v", err)
	}

	running, err := svc.ListChildRuns(ctx, parent.ID, 10, RunStatusRunning)
	if err != nil {
		t.Fatalf("list running children: %v", err)
	}
	if len(running) != 1 || running[0].ID != childB.ID {
		t.Fatalf("expected only running child-b, got %#v", running)
	}

	all, err := svc.ListChildRuns(ctx, parent.ID, 10)
	if err != nil {
		t.Fatalf("list all children: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 children, got %d", len(all))
	}
	if all[0].ParentRunID != parent.ID || all[1].ParentRunID != parent.ID {
		t.Fatalf("unexpected parent ids: %#v", all)
	}
}

func TestCancelRunningDescendantsTraversesTheWholeRunTree(t *testing.T) {
	ctx := context.Background()
	db, err := OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	runs := &RunStore{DB: db}
	ensureSessionsForRuns(t, db, "s1")
	parent, err := runs.CreateRun(ctx, "s1", "parent")
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	child, err := runs.CreateSubagentRun(ctx, parent.ID, "s1", "child")
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	grandchild, err := runs.CreateSubagentRun(ctx, child.ID, "s1", "grandchild")
	if err != nil {
		t.Fatalf("create grandchild: %v", err)
	}
	completed, err := runs.CreateSubagentRun(ctx, parent.ID, "s1", "already done")
	if err != nil {
		t.Fatalf("create completed child: %v", err)
	}
	if err := runs.SetStatus(ctx, completed.ID, RunStatusDone); err != nil {
		t.Fatalf("complete child: %v", err)
	}

	if err := runs.CancelRunningDescendants(ctx, parent.ID); err != nil {
		t.Fatalf("cancel descendants: %v", err)
	}
	for _, runID := range []string{child.ID, grandchild.ID} {
		got, getErr := runs.GetRun(ctx, runID)
		if getErr != nil || got.Status != RunStatusCancelled {
			t.Fatalf("run %s = %+v, %v; want cancelled", runID, got, getErr)
		}
	}
	gotCompleted, err := runs.GetRun(ctx, completed.ID)
	if err != nil || gotCompleted.Status != RunStatusDone {
		t.Fatalf("completed run = %+v, %v; cancellation must not overwrite it", gotCompleted, err)
	}
}

func TestCancelledRunStatusDoesNotCreateAnErrorStep(t *testing.T) {
	ctx := context.Background()
	db, err := OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	runs := &RunStore{DB: db}
	ensureSessionsForRuns(t, db, "s1")
	run, err := runs.CreateRun(ctx, "s1", "cancel me")
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := runs.SetStatus(ctx, run.ID, RunStatusCancelled); err != nil {
		t.Fatalf("set cancelled: %v", err)
	}
	events, err := runs.ListRunEvents(ctx, run.ID, 0)
	if err != nil {
		t.Fatalf("list run events: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("cancelled status created failure events: %+v", events)
	}
}

// openStateDB opens the real schema, triggers included. The ownership rules
// below are about who wins the race between the touch-session trigger and
// Ensure, so a hand-rolled fixture without that trigger would test nothing.
func openStateDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := OpenStateForTest(context.Background(), filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// twoTenantStores returns two session stores over one state database, the way
// two primary agents actually share it.
func twoTenantStores(t *testing.T) (*SessionStore, *SessionStore) {
	t.Helper()
	db := openStateDB(t)
	return NewSessionStore(db, "acme"), NewSessionStore(db, "globex")
}

// TestEnsureRecordsOwningPrimaryAgent covers the input side of memory tenancy:
// the pipeline decides whose conversations it may consolidate by reading
// fb_sessions.agent_id, so a session created without one belongs to no tenant
// and is consolidated by none.
func TestEnsureRecordsOwningPrimaryAgent(t *testing.T) {
	s := NewSessionStore(openStateDB(t), "acme")
	if err := s.Ensure(context.Background(), "session-1", "Session 1"); err != nil {
		t.Fatal(err)
	}
	if got := sessionAgentID(t, s, "session-1"); got != "acme" {
		t.Fatalf("agent_id=%q, want acme", got)
	}
}

// TestAppendRecordsOwnerBeforeTheTouchTrigger guards the ordering that makes
// that possible. Appending a TUI message fires a trigger that creates the
// session row, and nothing but the append path itself creates the row — so
// the owner is recorded at creation, never assigned afterwards.
func TestAppendRecordsOwnerOnCreation(t *testing.T) {
	s := NewSessionStore(openStateDB(t), "acme")
	ctx := context.Background()
	if _, err := s.Append(ctx, "tui-session", "user", "hello"); err != nil {
		t.Fatal(err)
	}
	if got := sessionAgentID(t, s, "tui-session"); got != "acme" {
		t.Fatalf("agent_id=%q; the append path created the session row without its owner", got)
	}
}

// TestConversationsAreInvisibleToOtherPrimaryAgents is the tenancy boundary for
// conversations. Isolating the memory pipeline is not enough on its own: a
// session another agent can list, resume, read or write to is that agent's
// conversation in every way that matters.
func TestConversationsAreInvisibleToOtherPrimaryAgents(t *testing.T) {
	acme, globex := twoTenantStores(t)
	ctx := context.Background()
	if _, err := acme.Append(ctx, "acme-session", "user", "acme secret"); err != nil {
		t.Fatal(err)
	}
	if err := acme.Ensure(ctx, "acme-parent", "acme-parent"); err != nil {
		t.Fatal(err)
	}
	if err := acme.SetParentSessionID(ctx, "acme-session", "acme-parent"); err != nil {
		t.Fatal(err)
	}
	if _, err := globex.Append(ctx, "globex-session", "user", "globex work"); err != nil {
		t.Fatal(err)
	}

	t.Run("listing", func(t *testing.T) {
		got, err := globex.ListSessionsRecent(ctx, 50)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range got {
			if row.ID != "globex-session" {
				t.Fatalf("listed %q, which belongs to another primary agent", row.ID)
			}
		}
		if len(got) != 1 {
			t.Fatalf("listed %d sessions, want only its own", len(got))
		}

		children, err := globex.ListChildSessionsRecent(ctx, "acme-parent", 50)
		if err != nil {
			t.Fatal(err)
		}
		if len(children) != 0 {
			t.Fatalf("listed %d children of another agent's session", len(children))
		}

		latest, err := globex.SessionIDWithLatestMessage(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if latest != "globex-session" {
			t.Fatalf("latest session = %q, want its own", latest)
		}
	})

	t.Run("resume", func(t *testing.T) {
		exists, err := globex.HasSession(ctx, "acme-session")
		if err != nil {
			t.Fatal(err)
		}
		if exists {
			t.Fatal("another agent's session reported as resumable")
		}
	})

	t.Run("history", func(t *testing.T) {
		if _, err := globex.ListTranscriptMessages(ctx, "acme-session", 100); !errors.Is(err, ErrSessionNotOwned) {
			t.Fatalf("ListTranscriptMessages err=%v, want ErrSessionNotOwned", err)
		}
		if _, err := globex.ListMemoryTranscriptMessages(ctx, "acme-session", 100); !errors.Is(err, ErrSessionNotOwned) {
			t.Fatalf("ListMemoryTranscriptMessages err=%v, want ErrSessionNotOwned", err)
		}
		if _, err := globex.ListAllMessages(ctx, "acme-session", 100); !errors.Is(err, ErrSessionNotOwned) {
			t.Fatalf("ListAllMessages err=%v, want ErrSessionNotOwned", err)
		}
		if _, err := globex.ListRecentMessages(ctx, "acme-session", 100); !errors.Is(err, ErrSessionNotOwned) {
			t.Fatalf("ListRecentMessages err=%v, want ErrSessionNotOwned", err)
		}
		if _, err := globex.ListTranscriptRecent(ctx, "acme-session", 100); !errors.Is(err, ErrSessionNotOwned) {
			t.Fatalf("ListTranscriptRecent err=%v, want ErrSessionNotOwned", err)
		}
		if _, err := globex.ListTranscriptMessagesWithRefs(ctx, "acme-session", 100); !errors.Is(err, ErrSessionNotOwned) {
			t.Fatalf("ListTranscriptMessagesWithRefs err=%v, want ErrSessionNotOwned", err)
		}
	})

	t.Run("writes", func(t *testing.T) {
		if err := globex.Ensure(ctx, "acme-session", "taken over"); !errors.Is(err, ErrSessionNotOwned) {
			t.Fatalf("Ensure err=%v, want ErrSessionNotOwned", err)
		}
		if _, err := globex.Append(ctx, "acme-session", "user", "injected"); !errors.Is(err, ErrSessionNotOwned) {
			t.Fatalf("Append err=%v, want ErrSessionNotOwned", err)
		}
		if err := globex.SetTitle(ctx, "acme-session", "renamed"); !errors.Is(err, ErrSessionNotOwned) {
			t.Fatalf("SetTitle err=%v, want ErrSessionNotOwned", err)
		}
		if err := globex.DeleteSessionMessages(ctx, "acme-session"); !errors.Is(err, ErrSessionNotOwned) {
			t.Fatalf("DeleteSessionMessages err=%v, want ErrSessionNotOwned", err)
		}

		// The owner's conversation is untouched by any of it.
		if got := sessionAgentID(t, acme, "acme-session"); got != "acme" {
			t.Fatalf("agent_id=%q after the attempts", got)
		}
		msgs, err := acme.ListTranscriptMessages(ctx, "acme-session", 100)
		if err != nil {
			t.Fatal(err)
		}
		if len(msgs) != 1 {
			t.Fatalf("owner sees %d messages, want the 1 it wrote", len(msgs))
		}
	})
}

// TestMessageRowWritesStayInsideTheBoundary covers the one write that is keyed
// by a message row id rather than a session id. Row ids travel between packages
// — compaction reads a boundary row and hands the id back — so ownership is
// re-derived from the row's own session instead of trusted.
func TestMessageRowWritesStayInsideTheBoundary(t *testing.T) {
	acme, globex := twoTenantStores(t)
	ctx := context.Background()
	rowID, err := acme.Append(ctx, "acme-session", "assistant", "acme answer")
	if err != nil {
		t.Fatal(err)
	}

	if err := globex.UpdateMessageParts(ctx, globex.DB(), rowID, `[{"type":"text","data":{"text":"rewritten"}}]`); !errors.Is(err, ErrSessionNotOwned) {
		t.Fatalf("UpdateMessageParts err=%v, want ErrSessionNotOwned", err)
	}
	if err := globex.excludeMessageFromTranscript(ctx, globex.DB(), rowID); !errors.Is(err, ErrSessionNotOwned) {
		t.Fatalf("excludeMessageFromTranscript err=%v, want ErrSessionNotOwned", err)
	}

	msgs, err := acme.ListTranscriptMessages(ctx, "acme-session", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].TextContent() != "acme answer" {
		t.Fatalf("owner's message = %#v after another agent wrote to its row", msgs)
	}

	// The owner still edits its own row, and a row that simply does not exist
	// stays the no-op callers have always relied on.
	if err := acme.UpdateMessageParts(ctx, acme.DB(), rowID, `[{"type":"text","data":{"text":"edited"}}]`); err != nil {
		t.Fatalf("owner UpdateMessageParts: %v", err)
	}
	if err := acme.UpdateMessageParts(ctx, acme.DB(), rowID+9999, `[]`); err != nil {
		t.Fatalf("missing row should stay a no-op, got %v", err)
	}
}

func sessionAgentID(t *testing.T, s *SessionStore, id string) string {
	t.Helper()
	var agentID string
	if err := s.db.QueryRow(`SELECT agent_id FROM fb_sessions WHERE id=?`, id).Scan(&agentID); err != nil {
		t.Fatalf("read agent_id for %s: %v", id, err)
	}
	return agentID
}

func assistantMessage(messageID, usageJSON string) Message {
	return Message{Role: "assistant", MessageID: messageID, UsageJSON: usageJSON}
}

func TestTokenCountFromLastAPIResponseAndTailEstimate(t *testing.T) {
	turns := []Message{
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "anchor", MessageID: "msg-1", UsageJSON: `{"input_tokens":1000,"cache_creation_input_tokens":200,"cache_read_input_tokens":300,"output_tokens":50}`},
		{Role: "tool", Content: stringsOfLen(400)},
		{Role: "user", Content: stringsOfLen(200)},
	}

	if got := TokenCountFromLastAPIResponse(turns); got != 1550 {
		t.Fatalf("TokenCountFromLastAPIResponse=%d want 1550", got)
	}
	withTail := TokenCountWithEstimation(turns)
	if withTail <= 1550 {
		t.Fatalf("TokenCountWithEstimation=%d should include tail estimate after anchor", withTail)
	}
}

func TestTokenCountWithEstimationWalksBackAcrossSplitAssistantResponse(t *testing.T) {
	turns := []Message{
		{Role: "assistant", Content: "first split", MessageID: "msg-1", UsageJSON: `{"input_tokens":1000,"output_tokens":10}`},
		{Role: "tool", Content: stringsOfLen(800), PartsJSON: sessionTestPartsJSON(stringsOfLen(800))},
		{Role: "assistant", Content: "second split", MessageID: "msg-1", UsageJSON: `{"input_tokens":1100,"output_tokens":20}`},
		{Role: "user", Content: stringsOfLen(400), PartsJSON: sessionTestPartsJSON(stringsOfLen(400))},
	}

	fromLastResponse := TokenCountFromLastAPIResponse(turns)
	withTail := TokenCountWithEstimation(turns)
	if fromLastResponse != 1120 {
		t.Fatalf("TokenCountFromLastAPIResponse=%d want 1120", fromLastResponse)
	}
	if withTail <= fromLastResponse {
		t.Fatalf("TokenCountWithEstimation=%d should include interleaved content after first split", withTail)
	}
}

func TestBodyAfterPrefixUsesFirstServerInputAsBaseline(t *testing.T) {
	turns := []Message{
		{Role: "user", Content: stringsOfLen(400)},
		{Role: "assistant", Content: "answer", UsageJSON: `{"input_tokens":100,"output_tokens":25}`},
		{Role: "user", Content: stringsOfLen(80)},
	}
	// Active usage is 125 plus the estimated 24-token suffix (20 tokens of text
	// plus the per-message framing every occupancy estimate carries). The
	// 100-token first request input is the window prefix, leaving 49 body tokens.
	if got := BodyAfterPrefixTokenCount(turns, 9999); got != 49 {
		t.Fatalf("body tokens=%d want 49", got)
	}
}

func TestBodyAfterPrefixUsesEstimatedResumePrefixBeforeServerUsage(t *testing.T) {
	turns := []Message{{Role: "user", Content: stringsOfLen(400)}}
	// 100 tokens of text plus the per-message framing, against a 100-token
	// estimated prefix.
	if got := BodyAfterPrefixTokenCount(turns, 100); got != 4 {
		t.Fatalf("body tokens=%d want 4", got)
	}
}

func TestTokenBudgetMatchesAutoCompactFormula(t *testing.T) {
	b := CalculateTokenBudget(1000, "test-model", llm.Hit{
		ContextWindow:    200000,
		DefaultMaxTokens: 50000,
	})
	if b.EffectiveContextWindow != 200000 {
		t.Fatalf("effective=%d want 200000", b.EffectiveContextWindow)
	}
	// Default threshold = 90% of the full window.
	if b.AutoCompactThreshold != 180000 {
		t.Fatalf("threshold=%d want 180000", b.AutoCompactThreshold)
	}
	// PercentLeft rounds to 99.
	wantPercent := 99
	if b.PercentLeft != wantPercent {
		t.Fatalf("percent=%d want %d", b.PercentLeft, wantPercent)
	}
}

func TestTokenBudgetLargeOutputModelStillUsesFullContext(t *testing.T) {
	b := CalculateTokenBudget(0, "deepseek/deepseek-v4-pro", llm.Hit{
		ContextWindow:    1000000,
		DefaultMaxTokens: 384000,
	})

	if b.EffectiveContextWindow != 1000000 {
		t.Fatalf("effective=%d want 1000000", b.EffectiveContextWindow)
	}
	if b.AutoCompactThreshold != 900000 {
		t.Fatalf("threshold=%d want 900000", b.AutoCompactThreshold)
	}
}

func TestTokenBudgetUsesExplicitInputLimitAndExplicitCompactLimit(t *testing.T) {
	b := CalculateTokenBudgetWithOptions(371460, "gpt-5.6-sol", llm.Hit{
		ContextWindow:    1050000,
		InputTokenLimit:  922000,
		DefaultMaxTokens: 128000,
	}, TokenBudgetOptions{})
	if b.EffectiveContextWindow != 922000 {
		t.Fatalf("effective=%d want 922000", b.EffectiveContextWindow)
	}
	if b.AutoCompactThreshold != 829800 {
		t.Fatalf("threshold=%d want 829800", b.AutoCompactThreshold)
	}
	if b.PercentLeft != 55 {
		t.Fatalf("percent=%d want 55", b.PercentLeft)
	}
	limited := CalculateTokenBudgetWithOptions(75_000, "gpt-5.6-sol", llm.Hit{
		ContextWindow:   1050000,
		InputTokenLimit: 922000,
	}, TokenBudgetOptions{ExplicitLimit: 100000})
	if limited.AutoCompactThreshold != 100000 || limited.PercentLeft != 25 {
		t.Fatalf("limited=%+v", limited)
	}
}

func sessionTestPartsJSON(text string) string {
	return ContentPartsJSON(nil, text)
}

func stringsOfLen(n int) string {
	if n <= 0 {
		return ""
	}
	b := make([]byte, n)
	for i := range b {
		b[i] = 'x'
	}
	return string(b)
}

func TestWaitSessionSnapshotRoundTrip(t *testing.T) {
	ctx := context.Background()
	db, err := OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	svc := &RunStore{DB: db}
	ensureSessionsForRuns(t, db, "s1")
	run, err := svc.CreateRun(ctx, "s1", "review request")
	require.NoError(t, err)

	timing := llm.NewExecutionTiming(time.Now(), time.Now().Add(150*time.Millisecond))
	toolResult := llm.ToolResultMessage("call-earlier", llm.Text("earlier result"))
	toolResult.ToolExecutionTiming = &timing
	snapshot := []llm.Message{
		llm.UserMessage(llm.Text("review request")),
		toolResult,
		llm.AssistantMessage([]llm.ContentPart{llm.Text("checking status")}, llm.ToolCall{
			ID:       "call-1",
			Type:     llm.ToolTypeFunction,
			Function: llm.FunctionCall{Name: "shell", Arguments: `{"command":"git status --short"}`},
		}),
	}
	mustWaitAction(t, db, "s1", "act-1")
	require.NoError(t, svc.SetWaitingAction(ctx, run.ID, Wait{
		RunID:            run.ID,
		ActionID:         "act-1",
		ToolName:         "shell",
		ToolInputJSON:    `{"command":"git status --short"}`,
		SessionSnapshot:  snapshot,
		AgentID:          "subagent-1",
		SubagentType:     "explore",
		SandboxProfile:   "read-only",
		RequestedProfile: "workspace-write",
		ProfileElevation: true,
	}))

	wait, err := svc.GetWaitForRun(ctx, run.ID)
	require.NoError(t, err)
	require.NotNil(t, wait)
	require.Len(t, wait.SessionSnapshot, len(snapshot))
	require.Equal(t, "checking status", wait.SessionSnapshot[2].TextContent())
	require.Len(t, wait.SessionSnapshot[2].ToolCalls, 1)
	require.NotNil(t, wait.SessionSnapshot[1].ToolExecutionTiming)
	require.Equal(t, timing.Duration, wait.SessionSnapshot[1].ToolExecutionTiming.Duration)

	require.Equal(t, "subagent-1", wait.AgentID)
	require.Equal(t, "explore", wait.SubagentType)
	require.Equal(t, "read-only", wait.SandboxProfile)
	require.Equal(t, "workspace-write", wait.RequestedProfile)
	require.True(t, wait.ProfileElevation)

	_, byAction, err := svc.FindRunByAction(ctx, "act-1")
	require.NoError(t, err)
	require.NotNil(t, byAction)
	require.Len(t, byAction.SessionSnapshot, len(snapshot))
	require.Equal(t, "call-1", byAction.SessionSnapshot[2].ToolCalls[0].ID)
	require.NotNil(t, byAction.SessionSnapshot[1].ToolExecutionTiming)
	require.Equal(t, timing.Duration, byAction.SessionSnapshot[1].ToolExecutionTiming.Duration)
	require.Equal(t, "subagent-1", byAction.AgentID)
	require.True(t, byAction.ProfileElevation)
}

func TestRunRuntimeHasNoPlanJSONMetadata(t *testing.T) {
	typ := reflect.TypeOf(Run{})
	if _, ok := typ.FieldByName("PlanJSON"); ok {
		t.Fatal("Run must not expose PlanJSON")
	}
}
