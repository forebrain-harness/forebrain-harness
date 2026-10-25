package run

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

// transcriptProgressStore is the slice of the session store the progress
// writer needs: the idempotent sequence append for a run's conversation, the
// structured append for a standalone reasoning row, and the row delete that
// withdraws one when the attempt that wrote it is re-sent.
type transcriptProgressStore interface {
	AppendMessageSequenceForRun(ctx context.Context, sessionID, runID string, messages []llm.Message, model, usageJSON string) error
	AppendStructuredMessageForRun(ctx context.Context, sessionID, runID, role, content, messageID, partsJSON, model, usageJSON, toolStepID, toolMetaJSON string, exec state.MessageExecTiming) (int64, error)
	DeleteRunMessageRow(ctx context.Context, sessionID, runID string, rowID int64) error
}

// transcriptProgress writes a run's conversation into its transcript as it
// happens — each text block when it ends — instead of only when the turn ends.
// The transcript is the one place a conversation's words are kept, so a block
// that ended is on disk even if the process stops before the turn does.
//
// It reconciles through AppendMessageSequenceForRun, which writes only the
// suffix the transcript lacks: the surface's own write at the turn's end, an
// approval gate's snapshot and a cancel's partial write stay idempotent with
// it. The same block written twice — a retry, a resumed turn, a cancelled
// turn — therefore lands once.
type transcriptProgress struct {
	store     transcriptProgressStore
	sessionID string
	runID     string

	mu          sync.Mutex
	reasoning   strings.Builder
	reasoningAt time.Time
	// attemptRows are the transcript rows this model call has written so far
	// (one per finished reasoning block). A resend of the call withdraws
	// them; a finish keeps them.
	attemptRows []int64
}

// newTranscriptProgress builds a progress writer for one session's run, or nil
// when there is nothing to write through (no store, no session). A nil writer's
// methods are all no-ops.
func newTranscriptProgress(store transcriptProgressStore, sessionID, runID string) *transcriptProgress {
	sessionID = strings.TrimSpace(sessionID)
	if store == nil || sessionID == "" {
		return nil
	}
	return &transcriptProgress{store: store, sessionID: sessionID, runID: strings.TrimSpace(runID)}
}

// flush writes the part of session the transcript does not hold yet. An error
// is logged, never returned: losing one checkpoint must not abort an answer the
// user is already watching.
func (t *transcriptProgress) flush(ctx context.Context, session []llm.Message, model string, usage *llm.Usage) {
	if t == nil || t.store == nil || len(session) == 0 {
		return
	}
	if err := t.store.AppendMessageSequenceForRun(ctx, t.sessionID, t.runID, session, strings.TrimSpace(model), state.MarshalTokenUsage(usage)); err != nil {
		slog.Warn("transcript progress: append block", "session_id", t.sessionID, "run_id", t.runID, "err", err)
	}
}

// reasoningDone writes one finished reasoning block as its own row, carrying
// how long the model spent on it so a replay can say "Thought for Ns". An empty
// block writes nothing.
func (t *transcriptProgress) reasoningDone(ctx context.Context, text string, startedAt, endedAt time.Time) {
	if t == nil || t.store == nil {
		return
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	spent := endedAt.Sub(startedAt)
	if spent < 0 {
		spent = 0
	}
	rowID, err := t.store.AppendStructuredMessageForRun(ctx, t.sessionID, t.runID, "reasoning", text, "", state.ReasoningPartsJSON(text, spent), "", "", "", "", state.MessageExecTiming{})
	if err != nil {
		slog.Warn("transcript progress: append reasoning", "session_id", t.sessionID, "run_id", t.runID, "err", err)
		return
	}
	t.mu.Lock()
	t.attemptRows = append(t.attemptRows, rowID)
	t.mu.Unlock()
}

// beginAttempt starts a new model call: rows written by earlier calls of this
// run are settled history, and only this call's rows may still be withdrawn.
func (t *transcriptProgress) beginAttempt() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.attemptRows = nil
	t.mu.Unlock()
}

// dropFinishedReasoning deletes the reasoning rows this model call wrote:
// the attempt that produced them is being re-sent, so what it finished
// leaves the transcript with it.
func (t *transcriptProgress) dropFinishedReasoning(ctx context.Context) {
	if t == nil || t.store == nil {
		return
	}
	t.mu.Lock()
	rows := t.attemptRows
	t.attemptRows = nil
	t.mu.Unlock()
	for _, rowID := range rows {
		if err := t.store.DeleteRunMessageRow(ctx, t.sessionID, t.runID, rowID); err != nil {
			slog.Warn("transcript progress: withdraw reasoning row", "session_id", t.sessionID, "run_id", t.runID, "row", rowID, "err", err)
		}
	}
}

// appendReasoning accumulates a reasoning delta into the block being written,
// stamping the block's start on its first delta.
func (t *transcriptProgress) appendReasoning(text string) {
	if t == nil || text == "" {
		return
	}
	t.mu.Lock()
	if t.reasoning.Len() == 0 {
		t.reasoningAt = time.Now()
	}
	t.reasoning.WriteString(text)
	t.mu.Unlock()
}

// takeReasoning returns the accumulated block and its start, clearing the
// buffer. ok is false when nothing accumulated, which is a block to skip.
func (t *transcriptProgress) takeReasoning() (string, time.Time, bool) {
	if t == nil {
		return "", time.Time{}, false
	}
	t.mu.Lock()
	text := t.reasoning.String()
	startedAt := t.reasoningAt
	t.reasoning.Reset()
	t.reasoningAt = time.Time{}
	t.mu.Unlock()
	if strings.TrimSpace(text) == "" {
		return "", time.Time{}, false
	}
	return text, startedAt, true
}

// dropInFlightReasoning discards the reasoning block still being streamed
// without writing it: the attempt that streamed it died on the network and
// the retry re-streams from the first token. Blocks that already ended were
// written when they ended: a finish keeps them; a resend withdraws them
// through dropFinishedReasoning.
func (t *transcriptProgress) dropInFlightReasoning() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.reasoning.Reset()
	t.reasoningAt = time.Time{}
	t.mu.Unlock()
}
