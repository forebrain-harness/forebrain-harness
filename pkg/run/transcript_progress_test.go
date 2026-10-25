package run

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

// A finished thinking block is its own transcript row, and the notification
// that withdraws a resend's finished thinking deletes it — while a
// notification after the next model call began never touches the settled
// rows of the call before it.
func TestTranscriptProgressWithdrawsFinishedReasoningOnResend(t *testing.T) {
	ctx := context.Background()
	const sid = "progress-withdraw-reasoning"
	store, runID := newProgressStore(t, sid)
	tp := newTranscriptProgress(store, sid, runID)
	if tp == nil {
		t.Fatal("no progress writer")
	}

	tp.appendReasoning("first thought")
	tp.reasoningDone(ctx, "first thought", time.Now().Add(-2*time.Second), time.Now())
	rows := reasoningRowContents(t, store, sid)
	if len(rows) != 1 || !strings.Contains(rows[0], "first thought") {
		t.Fatalf("reasoning rows = %v, want the finished block written", rows)
	}

	// A resend that withdraws finished thinking deletes the row it wrote.
	tp.dropFinishedReasoning(ctx)
	if rows = reasoningRowContents(t, store, sid); len(rows) != 0 {
		t.Fatalf("reasoning rows = %v, want the withdrawn row deleted", rows)
	}

	// The next model call begins: its rows are the only ones a notice may
	// still withdraw.
	tp.appendReasoning("second thought")
	tp.reasoningDone(ctx, "second thought", time.Now().Add(-time.Second), time.Now())
	tp.beginAttempt()
	tp.appendReasoning("third thought")
	tp.reasoningDone(ctx, "third thought", time.Now().Add(-time.Second), time.Now())
	tp.dropFinishedReasoning(ctx)

	rows = reasoningRowContents(t, store, sid)
	if len(rows) != 1 || !strings.Contains(rows[0], "second thought") {
		t.Fatalf("reasoning rows = %v, want the settled row kept and the new one withdrawn", rows)
	}
}

func reasoningRowContents(t *testing.T, store *state.SessionStore, sid string) []string {
	t.Helper()
	rows, err := store.ListAllMessages(context.Background(), sid, 0)
	if err != nil {
		t.Fatalf("list rows: %v", err)
	}
	var out []string
	for _, row := range rows {
		if row.Role == "reasoning" {
			out = append(out, row.Content)
		}
	}
	return out
}
