package state

import (
	"context"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

func summaryMessage(body string) llm.Message {
	return llm.UserMessage(llm.Text(CompactSummaryPrefix + "\n" + body))
}

// TestAppendMessageSequenceRefusesStandaloneSummary guards the source of the
// stacked-handoff-summary bug: a compaction summary must never be written as a
// standalone transcript row (it only ever belongs inside a checkpoint's
// ReplacementHistory).
func TestAppendMessageSequenceRefusesStandaloneSummary(t *testing.T) {
	ctx := context.Background()
	s := NewSessionStore(openSessionDB(t), "main")

	run := []llm.Message{
		llm.SystemMessage("instructions"),
		llm.UserMessage(llm.Text("commit all changes")),
		summaryMessage("prior work"),
		llm.AssistantMessage([]llm.ContentPart{llm.Text("answer")}),
	}
	if err := s.AppendMessageSequence(ctx, "s", run, "model", ""); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListTranscriptMessages(ctx, "s", 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range got {
		if IsCompactSummaryMessage(m) {
			t.Fatalf("handoff summary was persisted as a standalone transcript row: %q", m.TextContent())
		}
	}
	// The genuine user turn and assistant answer are still persisted.
	if len(got) != 2 {
		t.Fatalf("expected 2 rows (user + assistant), got %d: %+v", len(got), got)
	}
}

// TestProjectionSkipsStrayPostBoundarySummary proves an already-corrupted
// session (stray summary rows sitting after the boundary) self-heals on read:
// the projected model context carries exactly one summary — the authoritative
// one from ReplacementHistory.
func TestProjectionSkipsStrayPostBoundarySummary(t *testing.T) {
	ctx := context.Background()
	s := NewSessionStore(openSessionDB(t), "main")

	// Persist a checkpoint boundary whose ReplacementHistory holds the summary.
	text := "checkpoint"
	replacement := []llm.Message{
		llm.UserMessage(llm.Text("commit all changes")),
		summaryMessage("authoritative"),
	}
	part := CompactBoundaryPart{Trigger: "auto", Strategy: "local", ReplacementHistory: replacement, WindowNumber: 1, FirstWindowID: "w", WindowID: "w"}
	if err := s.AppendCompactCheckpoint(ctx, "s", text, part); err != nil {
		t.Fatal(err)
	}

	// Simulate the pre-fix corruption: two stray summary rows persisted after
	// the boundary, plus a legitimate assistant row.
	stray := summaryMessage("stale-1")
	if _, err := s.AppendStructuredMessage(ctx, "s", "user", stray.TextContent(), "", MessagePartsJSON(stray, stray.TextContent()), "", "", "", "", MessageExecTiming{}); err != nil {
		t.Fatal(err)
	}
	stray2 := summaryMessage("stale-2")
	if _, err := s.AppendStructuredMessage(ctx, "s", "user", stray2.TextContent(), "", MessagePartsJSON(stray2, stray2.TextContent()), "", "", "", "", MessageExecTiming{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(ctx, "s", "assistant", "post-compaction reply"); err != nil {
		t.Fatal(err)
	}

	got, err := s.ListTranscriptMessages(ctx, "s", 100)
	if err != nil {
		t.Fatal(err)
	}
	summaries := 0
	for _, m := range got {
		if IsCompactSummaryMessage(m) {
			summaries++
		}
	}
	if summaries != 1 {
		t.Fatalf("expected exactly 1 summary in projected context, got %d: %+v", summaries, previewRoles(got))
	}
	// The post-compaction reply survives.
	found := false
	for _, m := range got {
		if strings.Contains(m.TextContent(), "post-compaction reply") {
			found = true
		}
	}
	if !found {
		t.Fatalf("post-compaction reply missing from projection: %+v", previewRoles(got))
	}
}

func previewRoles(msgs []llm.Message) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		txt := strings.ReplaceAll(m.TextContent(), "\n", " ")
		if len(txt) > 20 {
			txt = txt[:20]
		}
		out = append(out, m.Role+":"+txt)
	}
	return out
}

// TestAppendCompactCheckpointIsOneStateChange pins that a checkpoint row, its
// boundary part and the session's pointer to it land together: the projection
// reads the replacement at once, and a session another primary agent owns is
// refused before anything is written.
func TestAppendCompactCheckpointIsOneStateChange(t *testing.T) {
	ctx := context.Background()
	db := openSessionDB(t)
	s := NewSessionStore(db, "main")
	_, _ = s.Append(ctx, "s", "user", "old history")
	part := CompactBoundaryPart{Trigger: "manual", Strategy: "local", ReplacementHistory: []llm.Message{summaryMessage("handoff")}, WindowNumber: 1, FirstWindowID: "w", WindowID: "w"}
	if err := s.AppendCompactCheckpoint(ctx, "s", "handoff", part); err != nil {
		t.Fatal(err)
	}
	boundary, latest, err := s.LatestCompactBoundary(ctx, "s")
	if err != nil || boundary == 0 || latest.WindowID != "w" {
		t.Fatalf("boundary = %d %+v err=%v", boundary, latest, err)
	}
	got, err := s.ListTranscriptMessages(ctx, "s", 100)
	if err != nil || len(got) != 1 || !IsCompactSummaryMessage(got[0]) {
		t.Fatalf("projection = %+v err=%v", got, err)
	}

	other := NewSessionStore(db, "someone-else")
	if err := other.AppendCompactCheckpoint(ctx, "s", "stolen", part); err == nil {
		t.Fatal("another primary agent's session must be refused")
	}
	all, err := s.ListAllMessages(ctx, "s", 0)
	if err != nil || len(all) != 2 {
		t.Fatalf("rows after the refused write = %d err=%v, want the original two", len(all), err)
	}
}
