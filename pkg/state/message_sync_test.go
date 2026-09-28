package state

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
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
