package state

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state/dbx"
)

func TestAppendMessageSequenceAppendsOnlyNewSuffix(t *testing.T) {
	ctx := context.Background()
	s := NewSessionStore(openSessionDB(t), "main")
	_, _ = s.Append(ctx, "s", "user", "hello")
	messages := []llm.Message{llm.SystemMessage("instructions"), llm.UserMessage(llm.Text("hello")), llm.AssistantMessage([]llm.ContentPart{llm.Text("answer")})}
	if err := s.AppendMessageSequence(ctx, "s", messages, "model", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendMessageSequence(ctx, "s", messages, "model", ""); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListTranscriptMessages(ctx, "s", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1].TextContent() != "answer" {
		t.Fatalf("messages=%+v", got)
	}
}

func TestAppendMessageSequenceUnderstandsOpaqueCheckpoint(t *testing.T) {
	ctx := context.Background()
	s := NewSessionStore(openSessionDB(t), "main")
	text := "Session compacted"
	replacement := []llm.Message{llm.UserMessage(llm.Text("summary")), {Compaction: &llm.CompactionState{ID: "cmp", EncryptedContent: "opaque"}}}
	part := CompactBoundaryPart{Trigger: "auto", Strategy: "remote_v2", ReplacementHistory: replacement, WindowNumber: 1, FirstWindowID: "w", WindowID: "w"}
	if err := s.AppendCompactCheckpoint(ctx, "s", text, part); err != nil {
		t.Fatal(err)
	}
	run := append([]llm.Message{llm.SystemMessage("instructions")}, replacement...)
	run = append(run, llm.AssistantMessage([]llm.ContentPart{llm.Text("continued")}))
	if err := s.AppendMessageSequence(ctx, "s", run, "model", ""); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListTranscriptMessages(ctx, "s", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[1].Compaction == nil || got[2].TextContent() != "continued" {
		t.Fatalf("messages=%+v", got)
	}
}

func TestCompactionSummaryCheckpointRoundTripsThroughMessageParts(t *testing.T) {
	msg := llm.Message{Compaction: &llm.CompactionState{Type: "compaction_summary", ID: "cmp", EncryptedContent: "opaque"}}
	parts := MessagePartsJSON(msg, "")
	if !strings.Contains(parts, `"type":"compaction_summary"`) {
		t.Fatalf("parts=%s", parts)
	}
	parsed, ok := ParseMessage("compaction", "", parts)
	if !ok || parsed.Compaction == nil || parsed.Compaction.Type != "compaction_summary" || parsed.Compaction.EncryptedContent != "opaque" {
		t.Fatalf("parsed=%+v ok=%v", parsed, ok)
	}
	legacy := MessagePartsJSON(llm.Message{Compaction: &llm.CompactionState{ID: "cmp", EncryptedContent: "opaque"}}, "")
	parsedLegacy, ok := ParseMessage("compaction", "", legacy)
	if !ok || parsedLegacy.Compaction == nil || parsedLegacy.Compaction.Type != PartTypeCompaction {
		t.Fatalf("legacy=%+v ok=%v parts=%s", parsedLegacy, ok, legacy)
	}
}

// assistantCall is the assistant message a model emits when it answers with
// text and a tool call in the same response.
func assistantCall(text, callID, toolName string) llm.Message {
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text(text)})
	msg.ToolCalls = []llm.ToolCall{{ID: callID, Type: "function", Function: llm.FunctionCall{Name: toolName, Arguments: "{}"}}}
	return msg
}

func TestAppendMessageSequenceCompletesPartialAssistantRowInsteadOfDuplicatingTheTurn(t *testing.T) {
	ctx := context.Background()
	s := NewSessionStore(openSessionDB(t), "main")
	_, _ = s.Append(ctx, "s", "user", "hello")
	// An approval gate persists what the run had produced so far, which is the
	// assistant text without the call it was suspended on.
	partial := []llm.Message{
		llm.UserMessage(llm.Text("hello")),
		llm.AssistantMessage([]llm.ContentPart{llm.Text("working on it")}),
	}
	if err := s.AppendMessageSequence(ctx, "s", partial, "model", ""); err != nil {
		t.Fatal(err)
	}
	// The resumed run finishes and reports the same conversation, with that
	// assistant message now carrying its call and result.
	finished := []llm.Message{
		llm.SystemMessage("instructions"),
		llm.UserMessage(llm.Text("hello")),
		assistantCall("working on it", "call-1", "shell"),
		llm.ToolResultMessage("call-1", llm.Text("ok")),
		llm.AssistantMessage([]llm.ContentPart{llm.Text("done")}),
	}
	if err := s.AppendMessageSequence(ctx, "s", finished, "model", ""); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListTranscriptMessages(ctx, "s", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("want the turn stored once, got %d messages: %+v", len(got), got)
	}
	if got[1].TextContent() != "working on it" || len(got[1].ToolCalls) != 1 || got[1].ToolCalls[0].ID != "call-1" {
		t.Fatalf("partial assistant row was not completed in place: %+v", got[1])
	}
	if got[2].ToolCallID != "call-1" || got[3].TextContent() != "done" {
		t.Fatalf("messages=%+v", got)
	}
}

// A provisional assistant row written while its response still streamed - body
// text plus the first of two calls - is completed in place by the finished
// exchange: both calls in one row, its text grown, the results behind it, and
// no duplicate turn.
func TestAppendMessageSequenceCompletesProvisionalAssistantRowWithLaterCallsAndText(t *testing.T) {
	ctx := context.Background()
	s := NewSessionStore(openSessionDB(t), "main")
	_, _ = s.Append(ctx, "s", "user", "hello")
	provisional := []llm.Message{
		llm.UserMessage(llm.Text("hello")),
		assistantCall("checking", "call-1", "shell"),
	}
	if err := s.AppendMessageSequence(ctx, "s", provisional, "model", ""); err != nil {
		t.Fatal(err)
	}
	final := assistantCall("checking both files", "call-1", "shell")
	final.ToolCalls = append(final.ToolCalls, llm.ToolCall{ID: "call-2", Type: "function", Function: llm.FunctionCall{Name: "shell", Arguments: "{}"}})
	finished := []llm.Message{
		llm.SystemMessage("instructions"),
		llm.UserMessage(llm.Text("hello")),
		final,
		llm.ToolResultMessage("call-1", llm.Text("one")),
		llm.ToolResultMessage("call-2", llm.Text("two")),
	}
	if err := s.AppendMessageSequence(ctx, "s", finished, "model", ""); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListTranscriptMessages(ctx, "s", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("want the turn stored once, got %d messages: %+v", len(got), got)
	}
	if got[1].TextContent() != "checking both files" || len(got[1].ToolCalls) != 2 ||
		got[1].ToolCalls[0].ID != "call-1" || got[1].ToolCalls[1].ID != "call-2" {
		t.Fatalf("provisional assistant row was not completed in place: %+v", got[1])
	}
	if got[2].ToolCallID != "call-1" || got[2].TextContent() != "one" {
		t.Fatalf("first result missing: %+v", got[2])
	}
	if got[3].ToolCallID != "call-2" || got[3].TextContent() != "two" {
		t.Fatalf("second result missing: %+v", got[3])
	}
}

// The finished message may differ from the stored row only by text: the same
// call, grown words. The row is rewritten, not duplicated.
func TestAppendMessageSequenceCompletesAssistantRowWithSameCallsAndGrownText(t *testing.T) {
	ctx := context.Background()
	s := NewSessionStore(openSessionDB(t), "main")
	_, _ = s.Append(ctx, "s", "user", "hello")
	provisional := []llm.Message{
		llm.UserMessage(llm.Text("hello")),
		assistantCall("checking", "call-1", "shell"),
	}
	if err := s.AppendMessageSequence(ctx, "s", provisional, "model", ""); err != nil {
		t.Fatal(err)
	}
	finished := []llm.Message{
		llm.SystemMessage("instructions"),
		llm.UserMessage(llm.Text("hello")),
		assistantCall("checking both files", "call-1", "shell"),
		llm.ToolResultMessage("call-1", llm.Text("ok")),
	}
	if err := s.AppendMessageSequence(ctx, "s", finished, "model", ""); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListTranscriptMessages(ctx, "s", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want the turn stored once, got %d messages: %+v", len(got), got)
	}
	if got[1].TextContent() != "checking both files" || len(got[1].ToolCalls) != 1 {
		t.Fatalf("assistant row was not rewritten with the finished text: %+v", got[1])
	}
	if got[2].ToolCallID != "call-1" {
		t.Fatalf("messages=%+v", got)
	}
}

func TestAppendMessageSequenceKeepsAlignmentAcrossOutOfBandRow(t *testing.T) {
	ctx := context.Background()
	s := NewSessionStore(openSessionDB(t), "main")
	_, _ = s.Append(ctx, "s", "user", "hello")
	first := []llm.Message{
		llm.UserMessage(llm.Text("hello")),
		llm.AssistantMessage([]llm.ContentPart{llm.Text("first answer")}),
	}
	if err := s.AppendMessageSequence(ctx, "s", first, "model", ""); err != nil {
		t.Fatal(err)
	}
	// A mode reminder is written straight to the transcript: the run that
	// follows never carries it, so it sits between the run's own messages.
	reminder := llm.UserMessage(llm.Text("<system-reminder>plan mode</system-reminder>"))
	reminder.IsMeta = true
	if err := s.AppendNewMessages(ctx, "s", "", []llm.Message{reminder}, "", ""); err != nil {
		t.Fatal(err)
	}
	second := append(append([]llm.Message{llm.SystemMessage("instructions")}, first...),
		llm.UserMessage(llm.Text("again")),
		llm.AssistantMessage([]llm.ContentPart{llm.Text("second answer")}),
	)
	if err := s.AppendMessageSequence(ctx, "s", second, "model", ""); err != nil {
		t.Fatal(err)
	}
	// A third turn now has the reminder row buried mid-history, which is where
	// a forward comparison gave up and re-appended everything after it.
	third := append(append([]llm.Message(nil), second...),
		llm.UserMessage(llm.Text("once more")),
		llm.AssistantMessage([]llm.ContentPart{llm.Text("third answer")}),
	)
	if err := s.AppendMessageSequence(ctx, "s", third, "model", ""); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListTranscriptMessages(ctx, "s", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 7 {
		t.Fatalf("want every turn stored once, got %d messages: %+v", len(got), got)
	}
	if got[2].TextContent() != "<system-reminder>plan mode</system-reminder>" {
		t.Fatalf("reminder row moved or was dropped: %+v", got)
	}
	if got[3].TextContent() != "again" || got[4].TextContent() != "second answer" {
		t.Fatalf("messages=%+v", got)
	}
	if got[5].TextContent() != "once more" || got[6].TextContent() != "third answer" {
		t.Fatalf("messages=%+v", got)
	}
}

func TestAppendMessageSequenceDoesNotRepeatTheTailWhenAnEarlierRowWasRepaired(t *testing.T) {
	ctx := context.Background()
	s := NewSessionStore(openSessionDB(t), "main")
	_, _ = s.Append(ctx, "s", "user", "hello")
	turn := []llm.Message{
		llm.UserMessage(llm.Text("hello")),
		assistantCall("calling", "call-1", "shell"),
		llm.ToolResultMessage("call-1", llm.Text("ok")),
		llm.AssistantMessage([]llm.ContentPart{llm.Text("answer")}),
	}
	if err := s.AppendMessageSequence(ctx, "s", turn, "model", ""); err != nil {
		t.Fatal(err)
	}
	// The next turn reports the same history with a row the store no longer
	// has - a repair excluded it - followed by new work.
	rows, err := s.listTranscriptStoredMessages(ctx, s.db, "s", 100)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.excludeMessageFromTranscript(ctx, s.db, rows[1].ID); err != nil {
		t.Fatal(err)
	}
	next := append(append([]llm.Message{llm.SystemMessage("instructions")}, turn...),
		llm.AssistantMessage([]llm.ContentPart{llm.Text("more")}),
	)
	if err := s.AppendMessageSequence(ctx, "s", next, "model", ""); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListTranscriptMessages(ctx, "s", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("want only the new message appended, got %d messages: %+v", len(got), got)
	}
	if got[3].TextContent() != "more" {
		t.Fatalf("messages=%+v", got)
	}
}

// TestSubMillisecondToolCallKeepsItsWindow pins the exec_* triple as one fact:
// a tool call that finished inside a millisecond has a zero duration, which is
// a duration — writing it as NULL beside a set start broke the table's
// all-or-nothing CHECK and rolled back the whole turn's transcript. A fork
// copies the row as stored, zero duration included.
func TestSubMillisecondToolCallKeepsItsWindow(t *testing.T) {
	ctx := context.Background()
	s := NewSessionStore(openSessionDB(t), "main")
	started := time.UnixMilli(1790503230123)
	timing := llm.NewExecutionTiming(started, started.Add(300*time.Microsecond))
	call := llm.AssistantMessage([]llm.ContentPart{llm.Text("checking")})
	call.ToolCalls = []llm.ToolCall{{ID: "call-fast", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "session_todo", Arguments: "{}"}}}
	result := llm.ToolResultMessage("call-fast", llm.Text("ok"))
	result.ToolExecutionTiming = &timing
	messages := []llm.Message{llm.UserMessage(llm.Text("go")), call, result}
	if err := s.AppendMessageSequence(ctx, "s", messages, "model", ""); err != nil {
		t.Fatalf("append a turn with a sub-millisecond tool call: %v", err)
	}
	if err := s.ForkInto(ctx, "s", "fork"); err != nil {
		t.Fatalf("fork a turn with a sub-millisecond tool call: %v", err)
	}
	for _, sid := range []string{"s", "fork"} {
		rows, err := s.ListAllMessages(ctx, sid, 0)
		if err != nil {
			t.Fatal(err)
		}
		var tool *Message
		for i := range rows {
			if rows[i].Role == llm.RoleTool {
				tool = &rows[i]
			}
		}
		if tool == nil {
			t.Fatalf("%s: tool row missing: %+v", sid, rows)
		}
		if tool.ExecStartedAtMs != 1790503230123 || tool.ExecFinishedAtMs != 1790503230123 || tool.ExecDurationMs != 0 {
			t.Fatalf("%s: tool window = %d/%d/%d, want 1790503230123/1790503230123/0", sid, tool.ExecStartedAtMs, tool.ExecFinishedAtMs, tool.ExecDurationMs)
		}
	}
}

// TestClearingAnEmptySessionHasNoAnchor pins that a session with no visible
// rows has no latest row — MAX over nothing is NULL, not an error — so
// clearing the context of a conversation that has not spoken yet succeeds.
func TestClearingAnEmptySessionHasNoAnchor(t *testing.T) {
	ctx := context.Background()
	s := NewSessionStore(openSessionDB(t), "main")
	if err := s.Ensure(ctx, "empty", "empty"); err != nil {
		t.Fatal(err)
	}
	id, err := s.LastTranscriptRowID(ctx, "empty")
	if err != nil || id != 0 {
		t.Fatalf("LastTranscriptRowID(empty) = %d, %v; want 0, nil", id, err)
	}
	cursor, err := s.SetSessionContextResetToLatest(ctx, "empty")
	if err != nil || cursor != 0 {
		t.Fatalf("SetSessionContextResetToLatest(empty) = %d, %v; want 0, nil", cursor, err)
	}
}

// TestStoreRepairsTextAnEngineWouldReject pins the promise the store boundary
// makes: the text it writes is text every engine accepts.
//
// On SQLite both bad bytes round-trip, which is why this has never been seen on
// the default engine; MySQL and PostgreSQL reject the row server-side instead.
// The assertion is therefore "the stored value is what an engine accepts", not
// "the value is unchanged" — a normalisation that only ran on the two servers
// would make a conversation's stored bytes depend on which one it ran on.
func TestStoreRepairsTextAnEngineWouldReject(t *testing.T) {
	ctx := context.Background()
	db, err := OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("open the state database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	t.Logf("engine under test: %s", dbx.DialectOf(db))
	ensureSessionsForRuns(t, db, "bytes-session")
	store := NewSessionStore(db, "main")

	const bad = "before\xffafter\x00end"
	message := llm.UserMessage(llm.Text(bad))
	if err := store.AppendMessageSequenceForRun(ctx, "bytes-session", "", []llm.Message{message}, "model", ""); err != nil {
		t.Fatalf("a message carrying an invalid byte and a NUL was refused instead of repaired: %v", err)
	}

	content, parts := storedRowText(t, db, "bytes-session")
	if !utf8.ValidString(content) {
		t.Errorf("stored content is not valid UTF-8: %q", content)
	}
	if strings.ContainsRune(content, 0) {
		t.Errorf("stored content carries a NUL: %q", content)
	}
	if !utf8.ValidString(parts) || strings.ContainsRune(parts, 0) {
		t.Errorf("stored parts carry a byte an engine refuses: %q", parts)
	}
	if strings.Contains(parts, `\u0000`) {
		t.Errorf("stored parts carry a NUL escape, which PostgreSQL refuses to parse out of a jsonb cast: %q", parts)
	}
	if !strings.HasPrefix(content, "before") || !strings.HasSuffix(content, "end") {
		t.Errorf("stored content lost the prose around the bad bytes: %q", content)
	}
}

// storedRowText reads the content and parts columns of a conversation's first
// message row back as the engine stored them.
func storedRowText(t *testing.T, db *sql.DB, sessionID string) (content, parts string) {
	t.Helper()
	query := `SELECT content, parts FROM fb_messages WHERE session_id=` +
		dbx.Placeholder(dbx.DialectOf(db), 1) + ` ORDER BY id ASC`
	if err := db.QueryRowContext(context.Background(), query, sessionID).Scan(&content, &parts); err != nil {
		t.Fatalf("read the stored row back: %v", err)
	}
	return content, parts
}

// TestPostgresToolPayloadWithANULStillListsTheToolCall is the engine leg for the
// JSON repair, named for the engine whose jsonb cast is what refused the row: a
// tool completion whose output held a NUL is stored, and the audit reader that
// parses the payload to find the tool's name still returns it.
func TestPostgresToolPayloadWithANULStillListsTheToolCall(t *testing.T) {
	ctx := context.Background()
	db, err := OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("open the state database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	t.Logf("engine under test: %s", dbx.DialectOf(db))
	ensureSessionsForRuns(t, db, "tool-payload-session")
	store := &RunStore{DB: db}

	payload := event.EncodePayload(event.ToolCallCompletedPayload{
		ToolName:    "read_file",
		Description: "read a GBK file",
		Data:        "binary\x00output\xffand prose",
	})
	if !strings.Contains(string(payload), `\u0000`) {
		t.Fatalf("the payload under test carries no NUL escape: %s", payload)
	}
	if _, err := store.AppendSessionEvent(ctx, SessionEvent{
		ID:        "evt-nul-payload",
		SessionID: "tool-payload-session",
		Type:      event.RunEventToolCompleted,
		Payload:   payload,
	}); err != nil {
		t.Fatalf("a tool completion whose output held a NUL was refused instead of repaired: %v", err)
	}

	rows, err := store.ListSessionToolCalls(ctx, "tool-payload-session", "", "", 10)
	if err != nil {
		t.Fatalf("list the conversation's tool calls: %v", err)
	}
	if len(rows) != 1 || rows[0].ToolName != "read_file" {
		t.Fatalf("tool calls = %+v, want the one read_file completion", rows)
	}
	if strings.Contains(rows[0].DetailJSON, `\u0000`) {
		t.Errorf("stored payload still carries the NUL escape: %s", rows[0].DetailJSON)
	}
	if !strings.Contains(rows[0].DetailJSON, dbx.ReplacementText) {
		t.Errorf("stored payload does not carry the replacement where the NUL was: %s", rows[0].DetailJSON)
	}
}
