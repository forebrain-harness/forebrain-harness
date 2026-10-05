package turn

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/stretchr/testify/require"
)

type stubSessionStore struct {
	turns   []state.Message
	created []state.SessionSummary
	births  []state.SessionBirth
}

func (s *stubSessionStore) Ensure(ctx context.Context, id string, title string) error {
	s.created = append(s.created, state.SessionSummary{ID: id, Title: title})
	s.births = append(s.births, state.SessionBirth{})
	return nil
}

func (s *stubSessionStore) EnsureAt(ctx context.Context, id, title string, birth state.SessionBirth) error {
	s.created = append(s.created, state.SessionSummary{ID: id, Title: title})
	s.births = append(s.births, birth)
	return nil
}

func (s *stubSessionStore) SetTitle(ctx context.Context, id string, title string) error {
	s.created = append(s.created, state.SessionSummary{ID: id, Title: title})
	return nil
}

func (s stubSessionStore) ListRecentMessages(ctx context.Context, sessionID string, limit int) ([]state.Message, error) {
	if limit <= 0 || limit >= len(s.turns) {
		return append([]state.Message(nil), s.turns...), nil
	}
	return append([]state.Message(nil), s.turns[:limit]...), nil
}

func TestListSessionMessagesUsesSessionStore(t *testing.T) {
	svc := New(WithSessionStore(&stubSessionStore{
		turns: []state.Message{
			{Role: "user", Content: "hello"},
			{Role: "assistant", Content: "world"},
		},
	}))

	got, err := svc.ListSessionMessages(context.Background(), "s1", 10)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "assistant", got[1].Role)
	require.Equal(t, "world", got[1].Content)
}

func TestCreateAndRenameSessionUseSessionStore(t *testing.T) {
	store := &stubSessionStore{}
	svc := New(WithSessionStore(store))

	created, err := svc.CreateSession(context.Background(), " Draft ")
	require.NoError(t, err)
	require.NotEmpty(t, created.ID)
	require.Contains(t, created.ID, "web-")
	require.Equal(t, "Draft", created.Title)
	require.Len(t, store.created, 1)
	require.Equal(t, "Draft", store.created[0].Title)

	require.NoError(t, svc.RenameSession(context.Background(), created.ID, "Renamed"))
	require.Equal(t, created.ID, store.created[1].ID)
	require.Equal(t, "Renamed", store.created[1].Title)
}

// TestCreateSessionWithoutATitleIsUnnamed pins that a session nobody named is
// stored titled with its own id — the store's "unnamed" — so its first user
// message names it, instead of a placeholder that would outrank that message.
func TestCreateSessionWithoutATitleIsUnnamed(t *testing.T) {
	store := &stubSessionStore{}
	svc := New(WithSessionStore(store))

	created, err := svc.CreateSession(context.Background(), "  ")
	require.NoError(t, err)
	require.Empty(t, created.Title)
	require.Len(t, store.created, 1)
	require.Equal(t, created.ID, store.created[0].ID)
	require.Equal(t, created.ID, store.created[0].Title)
}

// TestCreateSessionCarriesItsBirthToTheStore pins that a session's directory
// identity travels with its creation: CreateSessionAt hands the store exactly
// the birth its caller chose, and CreateSession — the ordinary path — hands
// over the zero birth, the process default.
func TestCreateSessionCarriesItsBirthToTheStore(t *testing.T) {
	store := &stubSessionStore{}
	svc := New(WithSessionStore(store))

	birth := state.SessionBirth{Cwd: "/proj", GitBranch: "feat"}
	if _, err := svc.CreateSessionAt(context.Background(), "In project", birth); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateSession(context.Background(), "Ordinary"); err != nil {
		t.Fatal(err)
	}

	require.Len(t, store.births, 2)
	require.Equal(t, birth, store.births[0])
	require.Equal(t, state.SessionBirth{}, store.births[1])
}

// fakeCancelStore records what a cancelled turn wrote.
type fakeCancelStore struct {
	seqs      [][]llm.Message
	seqRuns   []string
	model     string
	appends   [][2]string
	appendRun []string
	stamps    map[string]state.RunTiming
	repairs   int
	repairErr error
}

func (f *fakeCancelStore) AppendMessageSequenceForRun(_ context.Context, _, runID string, msgs []llm.Message, model, _ string) error {
	f.seqs = append(f.seqs, msgs)
	f.seqRuns = append(f.seqRuns, runID)
	f.model = model
	return nil
}

func (f *fakeCancelStore) AppendMessageForRun(_ context.Context, _, runID, role, content, _, _ string, _ state.MessageExecTiming) (int64, error) {
	f.appends = append(f.appends, [2]string{role, content})
	f.appendRun = append(f.appendRun, runID)
	return 0, nil
}

func (f *fakeCancelStore) StampRunTiming(_ context.Context, runID string, timing state.RunTiming) error {
	if f.stamps == nil {
		f.stamps = map[string]state.RunTiming{}
	}
	f.stamps[runID] = timing
	return nil
}

func (f *fakeCancelStore) RepairDanglingToolResults(context.Context, string) (int, error) {
	f.repairs++
	return 0, f.repairErr
}

func assistantMsg(text string) llm.Message {
	return llm.AssistantMessage([]llm.ContentPart{llm.Text(text)})
}

// TestPersistCancelledTurnDropsADuplicateStreamBuffer pins the de-duplication
// both surfaces now share: when the streaming buffer reproduces exactly the
// assistant text the capture already holds, it adds nothing and is dropped.
//
// Without it, an execution chain that never signals a response boundary leaves
// the buffer spanning the whole turn, and every assistant message the capture
// carries would be stored twice.
func TestPersistCancelledTurnDropsADuplicateStreamBuffer(t *testing.T) {
	t.Parallel()

	captured := []llm.Message{
		llm.UserMessage(llm.Text("do it")),
		assistantMsg("partial answer"),
	}
	store := &fakeCancelStore{}
	PersistCancelledTurn(context.Background(), store, CancelledTurn{
		SessionID:   "s1",
		Captured:    captured,
		PartialText: "partial answer",
	})

	if len(store.seqs) != 1 {
		t.Fatalf("AppendMessageSequence calls = %d, want 1", len(store.seqs))
	}
	if got := len(store.seqs[0]); got != len(captured) {
		t.Fatalf("persisted %d messages, want %d: the duplicate buffer must be dropped", got, len(captured))
	}
	if store.repairs != 1 {
		t.Fatalf("RepairDanglingToolResults calls = %d, want 1", store.repairs)
	}
}

// TestPersistCancelledTurnKeepsADistinctStreamBuffer covers the other side: a
// genuinely unfinished fragment is content the user watched arrive, so it is
// persisted. The comparison is equality, not containment.
func TestPersistCancelledTurnKeepsADistinctStreamBuffer(t *testing.T) {
	t.Parallel()

	captured := []llm.Message{
		llm.UserMessage(llm.Text("do it")),
		assistantMsg("first part"),
	}
	store := &fakeCancelStore{}
	PersistCancelledTurn(context.Background(), store, CancelledTurn{
		SessionID:        "s1",
		Captured:         captured,
		PartialText:      "first part and then some more",
		PartialReasoning: "thinking hard",
		Model:            "gpt-main",
		End:              RunEnd{StartedAt: time.Now().Add(-2 * time.Second), FinishedAt: time.Now(), Worked: 2 * time.Second},
	})

	if len(store.seqs) != 1 || len(store.seqs[0]) != len(captured)+1 {
		t.Fatalf("persisted %v, want the fragment appended", store.seqs)
	}
	if store.model != "gpt-main" {
		t.Fatalf("model = %q", store.model)
	}
	if len(store.appends) != 1 || store.appends[0][0] != "reasoning" {
		t.Fatalf("reasoning append = %v", store.appends)
	}
}

// TestPersistCancelledTurnRepairsEvenWithNothingToWrite pins that a cancel with
// no captured content still repairs dangling tool_calls: the tool that was
// interrupted may have left one behind regardless.
func TestPersistCancelledTurnRepairsEvenWithNothingToWrite(t *testing.T) {
	t.Parallel()

	store := &fakeCancelStore{}
	PersistCancelledTurn(context.Background(), store, CancelledTurn{SessionID: "s1"})
	if len(store.seqs) != 0 {
		t.Fatalf("nothing to persist should write nothing, got %v", store.seqs)
	}
	if store.repairs != 1 {
		t.Fatalf("RepairDanglingToolResults calls = %d, want 1", store.repairs)
	}
	// A nil store is a no-op rather than a panic.
	PersistCancelledTurn(context.Background(), nil, CancelledTurn{SessionID: "s1"})
}

// TestPersistCancelledTurnReportsARepairFailure pins a real regression: the
// TUI's cancel path used to log a RepairDanglingToolResults failure
// (chat_session.go's cancel handler called s.chatLog.Debugf on error) before
// this logic was shared into PersistCancelledTurn. The shared version dropped
// the error on the floor with a bare `_, _ =`, so a repair failure on a
// cancelled turn left no trace anywhere -- worse than before the sharing, not
// neutral to it. OnRepairError restores the observability without hard-coding
// either surface's logger.
func TestPersistCancelledTurnReportsARepairFailure(t *testing.T) {
	t.Parallel()

	store := &fakeCancelStore{repairErr: errors.New("dangling repair boom")}
	var reported error
	PersistCancelledTurn(context.Background(), store, CancelledTurn{
		SessionID:     "s1",
		OnRepairError: func(err error) { reported = err },
	})
	if store.repairs != 1 {
		t.Fatalf("RepairDanglingToolResults calls = %d, want 1", store.repairs)
	}
	if reported == nil || reported.Error() != "dangling repair boom" {
		t.Fatalf("OnRepairError got %v, want the repair error surfaced", reported)
	}

	// A nil OnRepairError must not panic -- most callers in tests supply none.
	PersistCancelledTurn(context.Background(), &fakeCancelStore{repairErr: errors.New("boom")}, CancelledTurn{SessionID: "s1"})
}

// A stopped or failed run binds everything it wrote to itself and stamps its
// clock, so a replay closes it with the "Worked for" line it closed with live.
func TestPersistCancelledTurnBindsItsRowsAndStampsTheRunsClock(t *testing.T) {
	t.Parallel()

	store := &fakeCancelStore{}
	end := RunEnd{StartedAt: time.UnixMilli(1_000), FinishedAt: time.UnixMilli(4_000), Worked: 3 * time.Second}
	PersistCancelledTurn(context.Background(), store, CancelledTurn{
		SessionID:        "s1",
		RunID:            "run-1",
		Captured:         []llm.Message{llm.UserMessage(llm.Text("do it")), assistantMsg("half")},
		PartialReasoning: "thinking",
		End:              end,
	})
	if len(store.seqRuns) != 1 || store.seqRuns[0] != "run-1" {
		t.Fatalf("sequence runs = %v, want the run that wrote it", store.seqRuns)
	}
	if len(store.appendRun) != 1 || store.appendRun[0] != "run-1" {
		t.Fatalf("reasoning runs = %v, want the run that wrote it", store.appendRun)
	}
	if got := store.stamps["run-1"]; got != (state.RunTiming{StartedAt: end.StartedAt, FinishedAt: end.FinishedAt, Worked: end.Worked}) {
		t.Fatalf("stamped %+v, want the run's own clock", got)
	}

	// A run that failed before writing anything still ended, and still has
	// its clock: the user's message bound to it is the row its line closes.
	silent := &fakeCancelStore{}
	PersistCancelledTurn(context.Background(), silent, CancelledTurn{SessionID: "s1", RunID: "run-2", End: end})
	if len(silent.seqs) != 0 || len(silent.appends) != 0 {
		t.Fatalf("nothing to write wrote %v %v", silent.seqs, silent.appends)
	}
	if _, ok := silent.stamps["run-2"]; !ok {
		t.Fatal("a silent run must still have its clock stamped")
	}
}

// fakeUserTurnStore records what a user turn wrote.
type fakeUserTurnStore struct {
	rowID     int64
	err       error
	ensureErr error
	ensured   []string
	content   string
	parts     string
	role      string
	runID     string
	appended  int
}

func (f *fakeUserTurnStore) Ensure(_ context.Context, id, _ string) error {
	f.ensured = append(f.ensured, id)
	return f.ensureErr
}

func (f *fakeUserTurnStore) AppendStructuredMessageForRun(_ context.Context, _, runID, role, content, _, partsJSON, _ string, _, _, _ string, _ state.MessageExecTiming) (int64, error) {
	f.role, f.content, f.parts, f.runID = role, content, partsJSON, runID
	f.appended++
	return f.rowID, f.err
}

func TestPersistUserTurnReturnsIdentityAndError(t *testing.T) {
	boom := errors.New("write failed")
	for _, tc := range []struct {
		name    string
		store   fakeUserTurnStore
		wantID  int64
		wantErr error
	}{
		{"identity", fakeUserTurnStore{rowID: 42}, 42, nil},
		{"append failure", fakeUserTurnStore{err: boom}, 0, boom},
		{"ensure failure", fakeUserTurnStore{ensureErr: boom}, 0, boom},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id, err := PersistUserTurn(context.Background(), &tc.store, UserTurn{SessionID: "s", ModelInput: "hi", EnsureSession: true})
			if id != tc.wantID || !errors.Is(err, tc.wantErr) {
				t.Fatalf("got (%d, %v), want (%d, %v)", id, err, tc.wantID, tc.wantErr)
			}
			if tc.store.ensureErr != nil && tc.store.appended != 0 {
				t.Fatal("appended after ensure failed")
			}
		})
	}
}

// TestPersistUserTurnStoresTheTypedCommandAsContent pins the display rule both
// surfaces now share: the row's content is what the user typed, while PartsJSON
// carries the prompt the model actually saw.
//
// The model context is rebuilt from PartsJSON, which is preferred over content,
// so this is what lets a resume replay show the command the user entered rather
// than the prompt it expanded into.
func TestPersistUserTurnStoresTheTypedCommandAsContent(t *testing.T) {
	t.Parallel()

	store := &fakeUserTurnStore{}
	PersistUserTurn(context.Background(), store, UserTurn{
		SessionID:  "s1",
		ModelInput: "the long expanded prompt",
		RawInput:   "/init",
	})
	if store.content != "/init" {
		t.Fatalf("content = %q, want the typed command", store.content)
	}
	if !strings.Contains(store.parts, "the long expanded prompt") {
		t.Fatalf("partsJSON = %q, want the expanded prompt", store.parts)
	}
	if store.role != "user" {
		t.Fatalf("role = %q", store.role)
	}

	// A surface that already created the run stores the message bound to it.
	bound := &fakeUserTurnStore{}
	PersistUserTurn(context.Background(), bound, UserTurn{SessionID: "s1", RunID: " run-7 ", ModelInput: "go"})
	if bound.runID != "run-7" {
		t.Fatalf("runID = %q, want the run the message starts", bound.runID)
	}
}

func TestPersistUserTurnFallsBackAndSkipsEmpty(t *testing.T) {
	t.Parallel()

	// No RawInput: content is the model input itself.
	plain := &fakeUserTurnStore{}
	PersistUserTurn(context.Background(), plain, UserTurn{SessionID: "s1", ModelInput: "hello"})
	if plain.content != "hello" {
		t.Fatalf("content = %q", plain.content)
	}
	if len(plain.ensured) != 0 {
		t.Fatalf("Ensure called without EnsureSession: %v", plain.ensured)
	}

	// A surface-rendered PartsJSON wins: it is the one carrying attachments.
	supplied := &fakeUserTurnStore{}
	PersistUserTurn(context.Background(), supplied, UserTurn{
		SessionID: "s1", ModelInput: "hello", PartsJSON: `{"mine":true}`, EnsureSession: true,
	})
	if supplied.parts != `{"mine":true}` {
		t.Fatalf("partsJSON = %q, want the supplied rendering", supplied.parts)
	}
	if len(supplied.ensured) != 1 {
		t.Fatalf("EnsureSession must create the session row, got %v", supplied.ensured)
	}

	// Blank input writes nothing at all.
	blank := &fakeUserTurnStore{}
	PersistUserTurn(context.Background(), blank, UserTurn{SessionID: "s1", ModelInput: "   "})
	if blank.appended != 0 {
		t.Fatalf("blank input appended %d rows, want 0", blank.appended)
	}
	PersistUserTurn(context.Background(), nil, UserTurn{SessionID: "s1", ModelInput: "hi"})
}

// TestPersistUserTurnMarksWhoWroteThePrompt pins the origin rule: a turn
// someone other than the person started — a heartbeat — stores its row with
// the origin part, while the content stays the prompt text itself.
func TestPersistUserTurnMarksWhoWroteThePrompt(t *testing.T) {
	t.Parallel()

	beat := &fakeUserTurnStore{}
	PersistUserTurn(context.Background(), beat, UserTurn{
		SessionID:  "s1",
		RunID:      "run-9",
		ModelInput: "anything new?",
		Origin:     state.MessageOriginHeartbeat,
	})
	if got := state.MessageOrigin(beat.parts); got != "heartbeat" {
		t.Fatalf("origin = %q, want heartbeat; parts = %s", got, beat.parts)
	}
	if beat.content != "anything new?" {
		t.Fatalf("content = %q, want the prompt itself", beat.content)
	}

	plain := &fakeUserTurnStore{}
	PersistUserTurn(context.Background(), plain, UserTurn{SessionID: "s1", ModelInput: "hello"})
	if got := state.MessageOrigin(plain.parts); got != "" {
		t.Fatalf("origin = %q, want empty for the person's own message", got)
	}
}

func newAbandonedCallStore(t *testing.T) (*state.SessionStore, string) {
	t.Helper()
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := state.NewSessionStore(db, "main")
	if err := store.Ensure(ctx, "s1", "s1"); err != nil {
		t.Fatalf("ensure session: %v", err)
	}
	return store, "s1"
}

func gatedShellCall() llm.Message {
	return llm.AssistantMessage([]llm.ContentPart{llm.Text("running it")}, llm.ToolCall{
		ID: "call-1", Type: llm.ToolTypeFunction,
		Function: llm.FunctionCall{Name: "shell", Arguments: `{"command":"rm -rf build"}`},
	})
}

// The whole point of answering an abandoned call rather than deleting it: the
// next turn's repair used to strip the call from the stored row, and that row is
// the one a resume replays, so the canceled card the user watched lasted exactly
// one more message.
func TestAnsweredCancelledCallSurvivesTheNextTurnsRepair(t *testing.T) {
	ctx := context.Background()
	store, sid := newAbandonedCallStore(t)
	if err := store.AppendNewMessages(ctx, sid, "", []llm.Message{
		llm.UserMessage(llm.Text("clean up")),
		gatedShellCall(),
	}, "m", ""); err != nil {
		t.Fatalf("append: %v", err)
	}

	if wrote := AnswerAbandonedToolCalls(ctx, store, sid, "m"); wrote != 1 {
		t.Fatalf("answers written = %d, want one for the call the user canceled", wrote)
	}
	if _, err := store.RepairDanglingToolResults(ctx, sid); err != nil {
		t.Fatalf("repair: %v", err)
	}

	rows, err := store.ListAllMessages(ctx, sid, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	calls, answered := 0, 0
	for _, row := range rows {
		_, toolCalls, toolCallID, _ := state.ParseMessageParts(row.PartsJSON, "")
		calls += len(toolCalls)
		if strings.TrimSpace(toolCallID) == "call-1" {
			answered++
		}
	}
	if calls != 1 {
		t.Fatalf("the display transcript kept %d tool calls, want the one the user saw", calls)
	}
	if answered != 1 {
		t.Fatalf("the canceled call is answered %d times, want exactly once", answered)
	}
}

// Answering twice must not stack results: the id is already answered the second
// time round, so there is nothing left to write.
func TestAnsweringAnAbandonedCallIsIdempotent(t *testing.T) {
	ctx := context.Background()
	store, sid := newAbandonedCallStore(t)
	if err := store.AppendNewMessages(ctx, sid, "", []llm.Message{
		llm.UserMessage(llm.Text("clean up")),
		gatedShellCall(),
	}, "m", ""); err != nil {
		t.Fatalf("append: %v", err)
	}
	if wrote := AnswerAbandonedToolCalls(ctx, store, sid, "m"); wrote != 1 {
		t.Fatalf("first pass wrote %d, want 1", wrote)
	}
	if wrote := AnswerAbandonedToolCalls(ctx, store, sid, "m"); wrote != 0 {
		t.Fatalf("second pass wrote %d, want none: the call is already answered", wrote)
	}
}

// A call that ran is left alone, and so is a transcript that ends on ordinary
// prose: neither is an abandoned call.
func TestNothingIsAnsweredWhenNoCallWasAbandoned(t *testing.T) {
	ctx := context.Background()
	store, sid := newAbandonedCallStore(t)
	result := llm.ToolResultMessage("call-1", llm.Text(`{"stdout":""}`))
	if err := store.AppendNewMessages(ctx, sid, "", []llm.Message{
		llm.UserMessage(llm.Text("clean up")),
		gatedShellCall(),
		result,
		llm.AssistantMessage([]llm.ContentPart{llm.Text("done")}),
	}, "m", ""); err != nil {
		t.Fatalf("append: %v", err)
	}
	if wrote := AnswerAbandonedToolCalls(ctx, store, sid, "m"); wrote != 0 {
		t.Fatalf("wrote %d answers for a transcript with nothing abandoned", wrote)
	}
}

// A cancelled turn answers its own interrupted calls inside the sequence it
// writes, ahead of any partial text: a tool result has to follow the assistant
// message that opened it.
func TestCancelledTurnAnswersItsInterruptedCallBeforeItsPartialText(t *testing.T) {
	msgs := []llm.Message{llm.UserMessage(llm.Text("clean up")), gatedShellCall()}
	answers := CancelledToolAnswers(msgs)
	if len(answers) != 1 {
		t.Fatalf("answers = %d, want one", len(answers))
	}
	if answers[0].ToolCallID != "call-1" || answers[0].TextContent() != CancelledToolCallNote {
		t.Fatalf("answer = %+v", answers[0])
	}
	if answers[0].ToolDisplay == nil || strings.TrimSpace(answers[0].ToolDisplay.Body) != "" {
		t.Fatalf("a canceled card claims output it never produced: %+v", answers[0].ToolDisplay)
	}
	if strings.HasPrefix(answers[0].ToolDisplay.Summary, "ran ") {
		t.Fatalf("a canceled card claims the call ran: %q", answers[0].ToolDisplay.Summary)
	}
}

// TestStreamPartialResponseCompleted pins the boundary semantics the
// orchestration loop relies on: answer text is dropped, reasoning is not.
func TestStreamPartialResponseCompleted(t *testing.T) {
	partial := &StreamPartial{}
	partial.AppendContent("completed response text")
	partial.AppendReasoning("thinking from the completed response")
	partial.ResponseCompleted()
	require.Equal(t, "", partial.Content(), "captured assistant text must be dropped at the boundary")
	require.Equal(t, "thinking from the completed response", partial.Reasoning(),
		"reasoning only reaches the transcript via the cancel path and must survive")
	partial.AppendContent("interrupted text")
	require.Equal(t, "interrupted text", partial.Content())
}
