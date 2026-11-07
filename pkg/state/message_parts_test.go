package state

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state/dbx"
)

func TestMessageOriginRoundTrip(t *testing.T) {
	original := llm.UserMessage(llm.Text("anything new?"))
	partsJSON := MessagePartsJSON(original, "anything new?")
	if got := MessageOrigin(partsJSON); got != "" {
		t.Fatalf("plain parts origin=%q, want empty", got)
	}
	marked := WithMessageOrigin(partsJSON, MessageOriginHeartbeat)
	if got := MessageOrigin(marked); got != "heartbeat" {
		t.Fatalf("marked parts origin=%q, want heartbeat", got)
	}
}

// TestWithMessageOriginLeavesTheParsedMessageUnchanged pins the cache
// invariant: the origin marker is display-only, so parsing a marked message
// yields exactly what parsing the unmarked one does — the model is sent the
// same bytes either way.
func TestWithMessageOriginLeavesTheParsedMessageUnchanged(t *testing.T) {
	p := MessagePartsJSON(llm.UserMessage(llm.Text("anything new?")), "anything new?")
	wantParts, wantCalls, wantCallID, wantIsMeta := ParseMessageParts(p, "")
	gotParts, gotCalls, gotCallID, gotIsMeta := ParseMessageParts(WithMessageOrigin(p, "heartbeat"), "")
	if !reflect.DeepEqual(gotParts, wantParts) {
		t.Fatalf("content parts changed by origin marker: %+v vs %+v", gotParts, wantParts)
	}
	if !reflect.DeepEqual(gotCalls, wantCalls) {
		t.Fatalf("tool calls changed by origin marker: %+v vs %+v", gotCalls, wantCalls)
	}
	if gotCallID != wantCallID {
		t.Fatalf("tool_call_id changed by origin marker: %q vs %q", gotCallID, wantCallID)
	}
	if gotIsMeta != wantIsMeta {
		t.Fatalf("isMeta changed by origin marker: %v vs %v", gotIsMeta, wantIsMeta)
	}
}

// TestPeerMessagePartsCarryTheOriginAndNothingElse pins what a message written
// on the person's behalf by another session persists (plan 196 §4.1): the row
// carries both markers, the origin reads back for the surfaces, and the message
// the model is sent is exactly the message an unmarked row would send.
func TestPeerMessagePartsCarryTheOriginAndNothingElse(t *testing.T) {
	envelope := "<cross-session-message from=\"uds:/tmp/fb-socks-1/7.sock\">\nhi\n</cross-session-message>"
	msg := llm.UserMessage(llm.Text(envelope))
	msg.IsMeta = true
	msg.Origin = MessageOriginPeer
	partsJSON := MessagePartsJSON(msg, envelope)

	if !strings.Contains(partsJSON, `"type":"`+PartTypeIsMeta+`"`) {
		t.Fatalf("parts missing the meta marker: %s", partsJSON)
	}
	if !strings.Contains(partsJSON, `"kind":"`+MessageOriginPeer+`"`) || !strings.Contains(partsJSON, `"type":"`+PartTypeOrigin+`"`) {
		t.Fatalf("parts missing the peer origin: %s", partsJSON)
	}
	if got := MessageOrigin(partsJSON); got != MessageOriginPeer {
		t.Fatalf("MessageOrigin = %q, want %q", got, MessageOriginPeer)
	}
	parsed, ok := ParseMessage(llm.RoleUser, envelope, partsJSON)
	if !ok {
		t.Fatal("the marked message did not parse")
	}
	if !parsed.IsMeta {
		t.Fatal("the parsed message lost IsMeta")
	}
	if parsed.Origin != "" {
		t.Fatalf("parsed origin = %q, want it left to persistence", parsed.Origin)
	}
	plain := MessagePartsJSON(llm.UserMessage(llm.Text(envelope)), envelope)
	want, _, _, _ := ParseMessageParts(plain, envelope)
	got, _, _, _ := ParseMessageParts(partsJSON, envelope)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("model-visible parts changed by the peer markers: %+v vs %+v", got, want)
	}
}

func TestMessagePartsRoundTripPreservesToolDisplay(t *testing.T) {
	original := llm.ToolResultMessage("call-edit", llm.Text("ok"))
	original.ToolDisplay = &llm.ToolDisplayState{
		Body:         "turn diff: `internal/demo.go` (+1/-1)\n\n```diff\n-old\n+new\n```",
		Summary:      "edited internal/demo.go",
		ToolMetaJSON: `{"tool_name":"edit_file","status":"completed"}`,
	}
	partsJSON := MessagePartsJSON(original, original.TextContent())
	if !strings.Contains(partsJSON, PartTypeToolDisplay) {
		t.Fatalf("parts missing tool display: %s", partsJSON)
	}
	parsed, ok := ParseMessage(original.Role, original.TextContent(), partsJSON)
	if !ok || parsed.ToolDisplay == nil {
		t.Fatal("tool display did not survive persistence round trip")
	}
	if *parsed.ToolDisplay != *original.ToolDisplay {
		t.Fatalf("tool display=%+v want %+v", *parsed.ToolDisplay, *original.ToolDisplay)
	}
}

func TestMessagePartsRoundTripPreservesIsMeta(t *testing.T) {
	// A plan-mode reminder meta message
	original := llm.Message{
		Role:   llm.RoleUser,
		Parts:  []llm.ContentPart{llm.Text("<system-reminder>\nPLAN MODE ACTIVE\n</system-reminder>")},
		IsMeta: true,
	}
	json := MessagePartsJSON(original, original.TextContent())
	parsed, ok := ParseMessage(original.Role, original.TextContent(), json)
	if !ok {
		t.Fatalf("ParseMessage failed")
	}
	if parsed.Role != llm.RoleUser {
		t.Fatalf("role=%s want user", parsed.Role)
	}
	if !parsed.IsMeta {
		t.Fatalf("IsMeta not preserved through round-trip")
	}
	if parsed.TextContent() != original.TextContent() {
		t.Fatalf("content changed: %q vs %q", parsed.TextContent(), original.TextContent())
	}
}

func TestMessagePartsRoundTripNonMeta(t *testing.T) {
	original := llm.UserMessage(llm.Text("hello world"))
	json := MessagePartsJSON(original, original.TextContent())
	parsed, ok := ParseMessage(original.Role, original.TextContent(), json)
	if !ok {
		t.Fatalf("ParseMessage failed")
	}
	if parsed.IsMeta {
		t.Fatalf("IsMeta should be false for a non-meta message")
	}
	if parsed.TextContent() != original.TextContent() {
		t.Fatalf("content changed: %q vs %q", parsed.TextContent(), original.TextContent())
	}
}

// TestMessagePartsRoundTripPreservesImageBase64Data verifies that a
// ContentTypeImageBase64 part survives a full serialize -> parse round-trip
// with its pixel data intact. This is a regression test for a bug where
// contentBlocksJSON omitted the "data" field, causing the image to be stored
// with an empty base64 payload. On read-back the empty data produced an
// image_url of "data:image/png;base64," (no data), which providers like
// Volcengine Kimi reject with 400 InvalidParameter.
func TestMessagePartsRoundTripPreservesImageBase64Data(t *testing.T) {
	original := llm.UserMessage(
		llm.Text("Here is a screenshot"),
		llm.ImageBase64("image/png", "iVBORw0KGgoAAAANSUhEUg=="),
	)
	partsJSON := MessagePartsJSON(original, original.TextContent())
	parsed, ok := ParseMessage(original.Role, original.TextContent(), partsJSON)
	if !ok {
		t.Fatalf("ParseMessage failed")
	}
	if len(parsed.Parts) != 2 {
		t.Fatalf("expected 2 parts, got %d", len(parsed.Parts))
	}
	img, ok := findImageBase64Part(parsed.Parts)
	if !ok {
		t.Fatalf("no ContentTypeImageBase64 part found after round-trip; parts=%v", parsed.Parts)
	}
	if img.ImageBase64 != "iVBORw0KGgoAAAANSUhEUg==" {
		t.Fatalf("base64 data lost in round-trip: got %q, want %q", img.ImageBase64, "iVBORw0KGgoAAAANSUhEUg==")
	}
	if img.MIMEType != "image/png" {
		t.Fatalf("mime type changed: got %q, want %q", img.MIMEType, "image/png")
	}
}

func TestMessagePartsRoundTripPreservesMemoryCitation(t *testing.T) {
	original := llm.AssistantMessage([]llm.ContentPart{llm.Text("answer")})
	original.MemoryCitation = &llm.MemoryCitation{
		Entries:    []llm.MemoryCitationEntry{{Path: "MEMORY.md", LineStart: 2, LineEnd: 5, Note: "preferences"}},
		RolloutIDs: []string{"thread-1"},
	}
	partsJSON := MessagePartsJSON(original, original.TextContent())
	parsed, ok := ParseMessage(original.Role, original.TextContent(), partsJSON)
	if !ok || parsed.MemoryCitation == nil {
		t.Fatalf("memory citation missing after round trip: %s", partsJSON)
	}
	if got := parsed.MemoryCitation; len(got.Entries) != 1 || got.Entries[0].LineEnd != 5 ||
		len(got.RolloutIDs) != 1 || got.RolloutIDs[0] != "thread-1" {
		t.Fatalf("memory citation = %#v", got)
	}
}

// The reasoning an assistant message carries back to its model round-trips with
// the model that wrote it, and stays out of what the message says: the text and
// the calls read back exactly as they were. A Name that is not a carry is a
// participant name, which the store never kept and still does not.
func TestMessagePartsRoundTripPreservesReasoningCarry(t *testing.T) {
	original := llm.AssistantMessage([]llm.ContentPart{llm.Text("answer")}, llm.ToolCall{ID: "call-1", Type: "function", Function: llm.FunctionCall{Name: "shell", Arguments: "{}"}})
	original.Name = llm.Prefix + " leading space kept\nsecond line "
	original.ReasoningModel = "glm-5.3-flash"
	partsJSON := MessagePartsJSON(original, original.TextContent())
	parsed, ok := ParseMessage(original.Role, original.TextContent(), partsJSON)
	if !ok || parsed.Name != original.Name || parsed.ReasoningModel != "glm-5.3-flash" {
		t.Fatalf("carry after round trip: name=%q model=%q parts=%s", parsed.Name, parsed.ReasoningModel, partsJSON)
	}
	if parsed.TextContent() != "answer" || len(parsed.Parts) != 1 || len(parsed.ToolCalls) != 1 {
		t.Fatalf("carry leaked into the message body: %+v", parsed)
	}

	named := llm.AssistantMessage([]llm.ContentPart{llm.Text("hi")})
	named.Name = "helper"
	partsJSON = MessagePartsJSON(named, named.TextContent())
	if strings.Contains(partsJSON, PartTypeReasoningCarry) {
		t.Fatalf("a participant name was stored as reasoning: %s", partsJSON)
	}
	if parsed, _ := ParseMessage(named.Role, named.TextContent(), partsJSON); parsed.Name != "" {
		t.Fatalf("parsed name = %q, want none", parsed.Name)
	}
}

// The reasoning a turn falls back to reading is that turn's alone: the history
// before its user message carries every earlier turn's reasoning to the model
// too, and that is not what this turn thought.
func TestExtractReasoningTextReadsTheCurrentTurnOnly(t *testing.T) {
	carry := func(text string) llm.Message {
		m := llm.AssistantMessage([]llm.ContentPart{llm.Text("…")})
		m.Name = llm.Prefix + text
		return m
	}
	meta := llm.UserMessage(llm.Text("reminder"))
	meta.IsMeta = true
	messages := []llm.Message{
		llm.UserMessage(llm.Text("first")),
		carry("earlier turn"),
		llm.UserMessage(llm.Text("second")),
		carry("one"),
		llm.ToolResultMessage("call-1", llm.Text("ok")),
		meta,
		carry("two"),
	}
	if got := ExtractReasoningText(messages); got != "one\ntwo" {
		t.Fatalf("reasoning = %q, want this turn's one and two", got)
	}
}

func findImageBase64Part(parts []llm.ContentPart) (llm.ContentPart, bool) {
	for _, p := range parts {
		if p.Type == llm.ContentTypeImageBase64 {
			return p, true
		}
	}
	return llm.ContentPart{}, false
}

// TestReasoningPartsRoundTrip pins the shape a reasoning row's parts carry: the
// block's text and the time the model spent on it. The duration belongs here,
// not in the exec_* columns, whose CHECK admits only a tool or user row.
func TestReasoningPartsRoundTrip(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		text  string
		spent time.Duration
	}{
		{name: "seconds", text: "weighing the options", spent: 12 * time.Second},
		{name: "sub-second", text: "quick", spent: 250 * time.Millisecond},
		{name: "zero", text: "instant", spent: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			parts := ReasoningPartsJSON(tc.text, tc.spent)
			got, ok := ParseReasoningTiming(parts)
			if !ok {
				t.Fatalf("ParseReasoningTiming(%q) = not ok, want the duration back", parts)
			}
			if got != tc.spent {
				t.Fatalf("ParseReasoningTiming = %v, want %v", got, tc.spent)
			}
		})
	}

	// A row without the part (an older row, a plain "[]") has no timing.
	for _, raw := range []string{"", "[]", `[{"type":"text","text":"hi"}]`, "not json"} {
		if _, ok := ParseReasoningTiming(raw); ok {
			t.Fatalf("ParseReasoningTiming(%q) = ok, want no timing", raw)
		}
	}
}

// TestReasoningRowStaysOutOfTheModelTranscript pins the cache boundary: a
// reasoning row is display-only, so it never reaches the message list the next
// request is built from, even with its timing part present.
func TestReasoningRowStaysOutOfTheModelTranscript(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db, err := OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := NewSessionStore(db, "main")
	const sid = "reasoning-transcript"
	if _, err := store.Append(ctx, sid, "user", "what am I thinking?"); err != nil {
		t.Fatal(err)
	}
	run, err := (&RunStore{DB: db}).CreateRun(ctx, sid, "what am I thinking?")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendStructuredMessageForRun(ctx, sid, run.ID, "reasoning", "a private thought", "", ReasoningPartsJSON("a private thought", 3*time.Second), "", "", "", "", MessageExecTiming{}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(ctx, sid, "assistant", "the answer"); err != nil {
		t.Fatal(err)
	}

	msgs, err := store.ListTranscriptMessages(ctx, sid, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		if strings.EqualFold(strings.TrimSpace(m.Role), "reasoning") {
			t.Fatalf("reasoning row reached the model transcript: %+v", m)
		}
	}
	// The row is on disk all the same — the projection drops it, not the store.
	var reasoningRows int
	if err := store.db.QueryRowContext(ctx,
		dbx.Rebind(dbx.DialectOf(store.db), `SELECT count(*) FROM fb_messages WHERE session_id=? AND role='reasoning'`), sid).Scan(&reasoningRows); err != nil {
		t.Fatal(err)
	}
	if reasoningRows != 1 {
		t.Fatalf("reasoning rows on disk = %d, want 1", reasoningRows)
	}
}

// TestMessagePartsRoundTripToolResultImage pins that an image carried by a
// tool-result message (a read_file that handed the model pixels) survives a
// full serialize -> parse round-trip with its base64 payload and MIME type
// intact, and keeps the tool_call_id that correlates it to the call.
func TestMessagePartsRoundTripToolResultImage(t *testing.T) {
	original := llm.ToolResultMessage("call-1", llm.Text("image 4x4"), llm.ImageBase64("image/png", "iVBORw0KGgo="))
	partsJSON := MessagePartsJSON(original, original.TextContent())
	parsed, ok := ParseMessage(llm.RoleTool, original.TextContent(), partsJSON)
	if !ok {
		t.Fatalf("ParseMessage failed")
	}
	if parsed.ToolCallID != "call-1" {
		t.Fatalf("ToolCallID=%q want call-1", parsed.ToolCallID)
	}
	img, ok := findImageBase64Part(parsed.Parts)
	if !ok {
		t.Fatalf("no ContentTypeImageBase64 part after round-trip; parts=%v", parsed.Parts)
	}
	if img.ImageBase64 != "iVBORw0KGgo=" || img.MIMEType != "image/png" {
		t.Fatalf("image part = %#v, want the original data and mime", img)
	}
}

// TestMessageDrawnOnSurface pins the one rule both transcript readers ask
// (plan 199/200): the meta marker hides a row from every surface, and the peer
// family is the exception — meta for the model, a card for the reader. The
// runtime rows the marker exists for must stay hidden, or a resume would show
// the environment block as though the person had typed it.
func TestMessageDrawnOnSurface(t *testing.T) {
	runtimeRow := llm.UserMessage(llm.Text("<forebrain_environment_context/>"))
	runtimeRow.IsMeta = true
	cases := []struct {
		name  string
		parts string
		want  bool
	}{
		{"a message the person typed", MessagePartsJSON(llm.UserMessage(llm.Text("hello")), "hello"), true},
		{"a runtime meta row", MessagePartsJSON(runtimeRow, runtimeRow.TextContent()), false},
		{"a peer letter", WithMessageOrigin(MessagePartsJSON(runtimeRow, runtimeRow.TextContent()), MessageOriginPeer), true},
		{"a peer delivery note", WithMessageOrigin(MessagePartsJSON(runtimeRow, runtimeRow.TextContent()), MessageOriginPeerStatus), true},
		{"a heartbeat stays hidden", WithMessageOrigin(MessagePartsJSON(runtimeRow, runtimeRow.TextContent()), MessageOriginHeartbeat), false},
		{"empty parts", "", true},
	}
	for _, tc := range cases {
		if got := MessageDrawnOnSurface(tc.parts); got != tc.want {
			t.Fatalf("%s: MessageDrawnOnSurface=%v want %v (parts %s)", tc.name, got, tc.want, tc.parts)
		}
	}
}
