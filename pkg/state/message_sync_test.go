package state

import (
	"context"
	"database/sql"
	"encoding/json"
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

// The reasoning an assistant message carries back to its model outlives the
// turn: the row written when the response's first call started has none of it,
// the finished message has it, and the next turn's history — read back from
// the store — must carry it exactly as the turn that produced it sent it.
// Before the carry was stored, every turn rebuilt a history in which the model
// had never thought.
func TestAssistantReasoningCarrySurvivesTheTurn(t *testing.T) {
	ctx := context.Background()
	s := NewSessionStore(openSessionDB(t), "main")
	_, _ = s.Append(ctx, "s", "user", "hello")
	provisional := []llm.Message{
		llm.UserMessage(llm.Text("hello")),
		assistantCall("checking", "call-1", "shell"),
	}
	if err := s.AppendMessageSequence(ctx, "s", provisional, "glm-5.3-flash", ""); err != nil {
		t.Fatal(err)
	}
	carried := assistantCall("checking", "call-1", "shell")
	carried.Name = llm.Prefix + "the file list first, then grep"
	carried.ReasoningModel = "glm-5.3-flash"
	unstamped := llm.AssistantMessage([]llm.ContentPart{llm.Text("done")})
	unstamped.Name = llm.Prefix + "summing up"
	finished := []llm.Message{
		llm.SystemMessage("instructions"),
		llm.UserMessage(llm.Text("hello")),
		carried,
		llm.ToolResultMessage("call-1", llm.Text("ok")),
		unstamped,
	}
	for range 2 { // a second reconciliation of the same run changes nothing
		if err := s.AppendMessageSequence(ctx, "s", finished, "glm-5.3-flash", ""); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.ListTranscriptMessages(ctx, "s", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("want the turn stored once, got %d messages: %+v", len(got), got)
	}
	if got[1].Name != carried.Name || got[1].ReasoningModel != "glm-5.3-flash" {
		t.Fatalf("provisional row kept no reasoning: name=%q model=%q", got[1].Name, got[1].ReasoningModel)
	}
	if got[1].TextContent() != "checking" || len(got[1].ToolCalls) != 1 {
		t.Fatalf("completing the reasoning changed the message: %+v", got[1])
	}
	// A carry that arrived without its model is filed under the row's model.
	if got[3].Name != unstamped.Name || got[3].ReasoningModel != "glm-5.3-flash" {
		t.Fatalf("unstamped carry: name=%q model=%q", got[3].Name, got[3].ReasoningModel)
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

// TestAppendMessageForRunRepairsContent is the regression for the main write
// path: AppendMessageForRun hands the row's content and tool metadata to the
// engine unrepaired, so MySQL refuses the row (error 1366) and PostgreSQL
// refuses it too (SQLSTATE 22021). The composer's own turn goes through this
// call, which makes a repaired store the difference between a stored message
// and a lost one.
func TestAppendMessageForRunRepairsContent(t *testing.T) {
	ctx := context.Background()
	db := openStateDBForTest(t)
	t.Logf("engine under test: %s", dbx.DialectOf(db))
	store := NewSessionStore(db, "main")

	// Two invalid bytes in a row, so the test also pins the collapse: the
	// repair writes one replacement per run, not one per byte.
	const bad = "a\xff\xffb"
	// A JSON document can carry a NUL as the escape, which PostgreSQL's JSON
	// lexer refuses to parse; the document itself is otherwise well-formed.
	const toolMeta = `{"tool":"read_file","detail":"nul\u0000here"}`

	if _, err := store.AppendMessageForRun(ctx, "repair-content", "", "user", bad, "", toolMeta, MessageExecTiming{}); err != nil {
		t.Fatalf("a message whose content holds invalid bytes was refused instead of repaired: %v", err)
	}

	content, parts, storedMeta := storedRepairedRow(t, db, "repair-content", "user")
	if want := "a" + dbx.ReplacementText + "b"; content != want {
		t.Fatalf("stored content = %q, want %q", content, want)
	}
	if parts != "[]" {
		t.Errorf("stored parts = %q, want the empty document the append path writes", parts)
	}
	if strings.Contains(storedMeta, `\u0000`) {
		t.Errorf("stored tool metadata still carries the NUL escape: %s", storedMeta)
	}
	if !strings.Contains(storedMeta, `\uFFFD`) {
		t.Errorf("stored tool metadata does not carry the replacement where the NUL was: %s", storedMeta)
	}
	if !json.Valid([]byte(storedMeta)) {
		t.Errorf("repairing the tool metadata broke the document: %s", storedMeta)
	}
	// The name this first turn gives its session is the same prose, written by
	// the naming statement rather than by insertMessage.
	if title := storedSessionTitle(t, db, "repair-content"); title != content {
		t.Errorf("the session named from that turn reads %q, want the repaired content %q", title, content)
	}
}

// TestAppendStructuredMessageRepairsParts is the same promise on the path every
// composer turn takes (pkg/turn/session.go): content and the parts document
// holding the same text must agree after the repair — one text, one repair —
// and the document must not carry the NUL escape PostgreSQL refuses to parse
// out of a jsonb cast.
func TestAppendStructuredMessageRepairsParts(t *testing.T) {
	ctx := context.Background()
	db := openStateDBForTest(t)
	t.Logf("engine under test: %s", dbx.DialectOf(db))
	store := NewSessionStore(db, "main")

	const bad = "sum\xff\xffmary\x00tail"
	want := "sum" + dbx.ReplacementText + "mary" + dbx.ReplacementText + "tail"
	msg := llm.UserMessage(llm.Text(bad))
	if _, err := store.AppendStructuredMessageForRun(ctx, "repair-parts", "", "user", bad, "",
		MessagePartsJSON(msg, bad), "", "", "", "", MessageExecTiming{}); err != nil {
		t.Fatalf("a message whose content and parts hold bytes an engine refuses was refused instead of repaired: %v", err)
	}
	content, parts, _ := storedRepairedRow(t, db, "repair-parts", "user")
	if content != want {
		t.Fatalf("stored content = %q, want %q", content, want)
	}
	if got := storedPartsText(t, parts); got != content {
		t.Fatalf("the row holds two texts: parts say %q, content says %q", got, content)
	}
	if strings.Contains(parts, `\u0000`) {
		t.Errorf("stored parts still carry the NUL escape PostgreSQL refuses: %s", parts)
	}

	// A caller that hands over a document instead of a message list reaches the
	// same choke point, so the escape is repaired there too.
	const nulParts = `[{"type":"text","text":"nul\u0000here"}]`
	if _, err := store.AppendStructuredMessageForRun(ctx, "repair-parts", "", "assistant", "nul\x00here", "",
		nulParts, "", "", "", "", MessageExecTiming{}); err != nil {
		t.Fatalf("a parts document carrying a NUL escape was refused instead of repaired: %v", err)
	}
	content, parts, _ = storedRepairedRow(t, db, "repair-parts", "assistant")
	if strings.Contains(parts, `\u0000`) {
		t.Errorf("the parts document handed over by the caller kept its NUL escape: %s", parts)
	}
	if got := storedPartsText(t, parts); got != content {
		t.Errorf("the caller's document and the content disagree after repair: parts say %q, content says %q", got, content)
	}
}

// TestMessageTextComparesRepairedRows pins what the repair changes for the
// reconciler: a stored row is repaired and the run's canonical message is not,
// so comparing the two on the raw text never matches — the reconciler then
// drops the in-place completion it was asked for and appends the message a
// second time. The comparison repairs both sides, so a row this store wrote
// still aligns with the message that produced it.
func TestMessageTextComparesRepairedRows(t *testing.T) {
	ctx := context.Background()
	db := openStateDBForTest(t)
	t.Logf("engine under test: %s", dbx.DialectOf(db))
	store := NewSessionStore(db, "main")
	const sessionID = "repair-sync"
	const bad = "sum\xff\xffmary"

	partial := []llm.Message{
		llm.UserMessage(llm.Text("hello")),
		llm.AssistantMessage([]llm.ContentPart{llm.Text(bad)}),
	}
	if err := store.AppendMessageSequenceForRun(ctx, sessionID, "", partial, "model", ""); err != nil {
		t.Fatalf("append the partial capture: %v", err)
	}
	if got := countStoredRows(t, db, sessionID); got != 2 {
		t.Fatalf("the first append stored %d rows, want the 2 of its message list", got)
	}

	finished := []llm.Message{
		llm.UserMessage(llm.Text("hello")),
		llm.AssistantMessage(
			[]llm.ContentPart{llm.Text(bad)},
			llm.ToolCall{ID: "call-1", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "read_file", Arguments: `{"path":"a.txt"}`}},
		),
	}
	if err := store.AppendMessageSequenceForRun(ctx, sessionID, "", finished, "model", ""); err != nil {
		t.Fatalf("append the finished run: %v", err)
	}
	if got := countStoredRows(t, db, sessionID); got != 2 {
		t.Fatalf("the reconciler appended the run's tail again: %d rows, want the 2 it already held", got)
	}
	content, parts, _ := storedRepairedRow(t, db, sessionID, "assistant")
	if want := "sum" + dbx.ReplacementText + "mary"; content != want {
		t.Fatalf("completed assistant content = %q, want %q", content, want)
	}
	if !strings.Contains(parts, "call-1") {
		t.Fatalf("the in-place completion did not land the finished message's tool call: %s", parts)
	}
}

// TestOutputHistoryRepairsCommand pins the third text column that carries a
// tool's own words: the command line of a compressed output record. It is
// repaired for the same reason its output already was — a record the engine
// refuses is a record nobody can retrieve.
func TestOutputHistoryRepairsCommand(t *testing.T) {
	store := openOutputHistoryForTest(t)
	t.Logf("engine under test: %s", store.dialect)

	id, err := store.Save(Entry{Command: "grep \xff\xff pattern", OriginalOutput: "before\xffafter", FilteredOutput: "filtered"})
	if err != nil {
		t.Fatalf("a record whose command holds invalid bytes was refused instead of repaired: %v", err)
	}
	entry, err := store.Get(id)
	if err != nil {
		t.Fatalf("read the saved record back: %v", err)
	}
	if want := "grep " + dbx.ReplacementText + " pattern"; entry.Command != want {
		t.Fatalf("stored command = %q, want %q", entry.Command, want)
	}
	if want := "before" + dbx.ReplacementText + "after"; entry.OriginalOutput != want {
		t.Fatalf("stored original output = %q, want %q", entry.OriginalOutput, want)
	}
}

// TestOutputHistoryImportsARepairableLegacyRow pins the legacy import: one
// record whose text an engine refuses used to roll back the whole batch, so a
// single bad byte in an old history file kept every record in it out of the
// database — and the file stayed behind to fail again on the next start.
func TestOutputHistoryImportsARepairableLegacyRow(t *testing.T) {
	ctx := context.Background()
	stateRoot := t.TempDir()
	writeRepairableLegacyHistory(t, stateRoot)
	db := openStateDBForTest(t)
	t.Logf("engine under test: %s", dbx.DialectOf(db))

	store, err := OpenOutputHistory(ctx, db, stateRoot)
	if err != nil {
		t.Fatalf("open the output history: %v", err)
	}
	dirty, err := store.Get(1)
	if err != nil {
		t.Fatalf("the legacy record with a bad byte did not land: %v", err)
	}
	if want := "cat " + dbx.ReplacementText + ".txt"; dirty.Command != want {
		t.Fatalf("imported command = %q, want %q", dirty.Command, want)
	}
	if want := "orig" + dbx.ReplacementText; dirty.OriginalOutput != want {
		t.Fatalf("imported original output = %q, want %q", dirty.OriginalOutput, want)
	}
	// The clean record of the same batch has to land with it: the repair is per
	// row, not a licence to drop the batch around a bad one.
	clean, err := store.Get(2)
	if err != nil {
		t.Fatalf("the clean record of the same batch did not land: %v", err)
	}
	if clean.Command != "make all" || clean.OriginalOutput != "orig-clean" {
		t.Fatalf("imported clean record = %+v, want the seeded command and output unchanged", clean)
	}
}

// TestStoreRepairLeavesCleanTextUnchanged is the control every repair needs:
// ordinary prose, tabs, newlines and non-ASCII text come back byte for byte, so
// the store still writes a person's message exactly as they typed it.
func TestStoreRepairLeavesCleanTextUnchanged(t *testing.T) {
	ctx := context.Background()
	db := openStateDBForTest(t)
	t.Logf("engine under test: %s", dbx.DialectOf(db))
	store := NewSessionStore(db, "main")

	const clean = "a\tb\nnaïve 汉字 🙂 — done"
	partsJSON := MessagePartsJSON(llm.UserMessage(llm.Text(clean)), clean)
	if _, err := store.AppendStructuredMessageForRun(ctx, "repair-clean", "", "user", clean, "",
		partsJSON, "", "", "", "", MessageExecTiming{}); err != nil {
		t.Fatalf("append a clean message: %v", err)
	}
	content, parts, toolMeta := storedRepairedRow(t, db, "repair-clean", "user")
	if content != clean {
		t.Fatalf("clean content came back changed: %q, want %q", content, clean)
	}
	if parts != partsJSON {
		t.Fatalf("clean parts came back changed: %q, want %q", parts, partsJSON)
	}
	if toolMeta != "" {
		t.Fatalf("clean row's tool metadata came back changed: %q, want empty", toolMeta)
	}
	if got := storedPartsText(t, parts); got != clean {
		t.Fatalf("the parts document of a clean row reads back as %q, want %q", got, clean)
	}
}

// storedRepairedRow reads one message row's text columns back as the engine
// stored them.
func storedRepairedRow(t *testing.T, db *sql.DB, sessionID, role string) (content, parts, toolMeta string) {
	t.Helper()
	dialect := dbx.DialectOf(db)
	query := `SELECT content, parts, tool_meta_json FROM fb_messages WHERE session_id=` +
		dbx.Placeholder(dialect, 1) + ` AND role=` + dbx.Placeholder(dialect, 2) + ` ORDER BY id ASC`
	if err := db.QueryRowContext(context.Background(), query, sessionID, role).Scan(&content, &parts, &toolMeta); err != nil {
		t.Fatalf("read the stored %s row of %s back: %v", role, sessionID, err)
	}
	return content, parts, toolMeta
}

// storedPartsText reads the text out of a stored parts document the way the
// transcript projection does: the text blocks, in order, joined by a newline.
func storedPartsText(t *testing.T, partsJSON string) string {
	t.Helper()
	var blocks []map[string]any
	if err := json.Unmarshal([]byte(partsJSON), &blocks); err != nil {
		t.Fatalf("parse the stored parts document %q: %v", partsJSON, err)
	}
	var texts []string
	for _, block := range blocks {
		if block["type"] != "text" {
			continue
		}
		text, _ := block["text"].(string)
		texts = append(texts, text)
	}
	return strings.Join(texts, "\n")
}

// storedSessionTitle reads the name a session carries back from the engine.
func storedSessionTitle(t *testing.T, db *sql.DB, sessionID string) string {
	t.Helper()
	query := `SELECT title FROM fb_sessions WHERE id=` + dbx.Placeholder(dbx.DialectOf(db), 1)
	var title string
	if err := db.QueryRowContext(context.Background(), query, sessionID).Scan(&title); err != nil {
		t.Fatalf("read the stored title of %s back: %v", sessionID, err)
	}
	return title
}

// countStoredRows counts a conversation's transcript rows.
func countStoredRows(t *testing.T, db *sql.DB, sessionID string) int {
	t.Helper()
	query := `SELECT COUNT(*) FROM fb_messages WHERE session_id=` + dbx.Placeholder(dbx.DialectOf(db), 1)
	var n int
	if err := db.QueryRowContext(context.Background(), query, sessionID).Scan(&n); err != nil {
		t.Fatalf("count the stored rows of %s: %v", sessionID, err)
	}
	return n
}

// legacyRepairableTableShape is the pre-migration history file's table, without
// the STRICT keyword: the file under test holds bytes SQLite will store and the
// two server engines refuse, which is the point.
const legacyRepairableTableShape = `
CREATE TABLE output_records (
  id INTEGER PRIMARY KEY,
  kind TEXT NOT NULL,
  command TEXT NOT NULL,
  session_id TEXT NOT NULL DEFAULT '',
  original_output TEXT NOT NULL,
  filtered_output TEXT NOT NULL,
  original_output_bytes INTEGER NOT NULL,
  filtered_output_bytes INTEGER NOT NULL,
  saved_tokens INTEGER NOT NULL,
  capability_id TEXT NOT NULL DEFAULT '',
  capability_version TEXT NOT NULL DEFAULT '',
  retrieve_count INTEGER NOT NULL DEFAULT 0,
  retrieve_reason TEXT NOT NULL DEFAULT '',
  spool_path TEXT NOT NULL DEFAULT '',
  spool_omitted_bytes INTEGER NOT NULL DEFAULT 0,
  created_at_ms INTEGER NOT NULL
);`

// writeRepairableLegacyHistory seeds a history file holding one record whose
// text an engine refuses, followed by a clean one.
func writeRepairableLegacyHistory(t *testing.T, stateRoot string) string {
	t.Helper()
	path := writeLegacyHistoryFile(t, stateRoot, legacyRepairableTableShape)
	db, err := sql.Open("sqlite3", path+"?_busy_timeout=5000")
	if err != nil {
		t.Fatalf("open the legacy history file: %v", err)
	}
	defer func() { _ = db.Close() }()
	insert := dbx.Rebind(dbx.DialectOf(db), `INSERT INTO output_records (id, kind, command, session_id, original_output,
		filtered_output, original_output_bytes, filtered_output_bytes, saved_tokens,
		retrieve_count, created_at_ms) VALUES (?,?,?,?,?,?,?,?,?,?,?)`)
	for _, record := range []struct {
		id      int64
		command string
		output  string
	}{
		{id: 1, command: "cat \xff.txt", output: "orig\xff"},
		{id: 2, command: "make all", output: "orig-clean"},
	} {
		if _, err := db.Exec(insert, record.id, KindShell, record.command, "s-1", record.output,
			"filtered", 10, 5, 1, 0, int64(1767323045000)); err != nil {
			t.Fatalf("seed the legacy history file with record %d: %v", record.id, err)
		}
	}
	return path
}
