package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
	"github.com/stretchr/testify/require"
)

// The withdrawal window closes at the model's first output-bearing event and at
// nothing else. Each callback the provider can reach first has to close it, and
// the events that carry no model output must leave it open — otherwise Esc
// either withdraws a submission the model has already answered, or refuses to
// withdraw one it has not.
func TestPrepareTUIAgentBaseClosesWithdrawalWindowOnFirstOutput(t *testing.T) {
	closers := map[string]func(sink *llm.StreamSink){
		"response started":                func(sink *llm.StreamSink) { sink.OnResponseStarted() },
		"assistant delta":                 func(sink *llm.StreamSink) { sink.OnDelta("hello") },
		"reasoning delta":                 func(sink *llm.StreamSink) { sink.OnReasoningDelta("thinking") },
		"provider web search":             func(sink *llm.StreamSink) { sink.OnWebSearch("ws-1", "query", false) },
		"non-streamed response completed": func(sink *llm.StreamSink) { sink.OnResponseCompleted() },
	}
	for name, closeWindow := range closers {
		t.Run(name, func(t *testing.T) {
			s, cleanup := newSurfaceTestSession(t)
			defer cleanup()
			foreground := &foregroundTurn{}
			base := context.WithValue(context.Background(), foregroundTurnKey{}, foreground)
			ctx, _, cleanupStream := s.prepareTUIAgentBase("sid", base)
			defer cleanupStream()
			sink := llm.StreamSinkFrom(ctx)
			require.NotNil(t, sink)

			closeWindow(sink)
			require.False(t, foreground.withdraw(), "Esc must fall back to ordinary cancellation once the model has produced output")
		})
	}

	// Events that carry no model output must not close it: they say the request
	// is alive, not that it has been answered.
	for name, nonOutput := range map[string]func(sink *llm.StreamSink){
		"usage delta":    func(sink *llm.StreamSink) { sink.OnUsage(120, 0) },
		"usage snapshot": func(sink *llm.StreamSink) { sink.OnUsageSnapshot(120, 0) },
		"empty delta":    func(sink *llm.StreamSink) { sink.OnDelta("") },
	} {
		t.Run(name, func(t *testing.T) {
			s, cleanup := newSurfaceTestSession(t)
			defer cleanup()
			foreground := &foregroundTurn{}
			base := context.WithValue(context.Background(), foregroundTurnKey{}, foreground)
			ctx, _, cleanupStream := s.prepareTUIAgentBase("sid", base)
			defer cleanupStream()
			sink := llm.StreamSinkFrom(ctx)
			require.NotNil(t, sink)
			if sink.OnUsage == nil || sink.OnUsageSnapshot == nil {
				t.Skip("sink does not expose usage callbacks")
			}
			nonOutput(sink)
			require.True(t, foreground.withdraw(), "a lifecycle/usage event must leave the withdrawal window open")
		})
	}
}

// A nested/internal call (compaction, an evaluator) runs under a muted sink. It
// did not receive this submission and must not acknowledge it on its behalf.
func TestMutedForegroundStreamCannotCloseTheWithdrawalWindow(t *testing.T) {
	s, cleanup := newSurfaceTestSession(t)
	defer cleanup()
	foreground := &foregroundTurn{}
	base := context.WithValue(context.Background(), foregroundTurnKey{}, foreground)
	ctx, _, cleanupStream := s.prepareTUIAgentBase("sid", base)
	defer cleanupStream()

	muted := llm.StreamSinkFrom(llm.WithMutedForegroundStream(ctx))
	require.NotNil(t, muted)
	require.Nil(t, muted.OnResponseStarted)
	require.Nil(t, muted.OnDelta)
	require.True(t, foreground.withdraw(), "a nested call must not commit the foreground submission")
}

// compactionHoldingLLM is the conversation's model. The compaction's summary
// request is sent to it as the conversation's next request, and it holds that
// request open until the request's context ends, reporting that it did.
type compactionHoldingLLM struct {
	entered   chan struct{}
	once      *sync.Once
	cancelled *atomic.Bool
}

func (m compactionHoldingLLM) Execute(ctx context.Context, msgs []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	if n := len(msgs); n > 0 && strings.Contains(msgs[n-1].TextContent(), "CONTEXT CHECKPOINT COMPACTION") {
		m.once.Do(func() { close(m.entered) })
		<-ctx.Done()
		m.cancelled.Store(true)
		return nil, ctx.Err()
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

// Esc landing inside a pre-turn auto-compaction stops it together with the
// turn it was making room for: the compaction's request is cancelled, its
// checkpoint — which commits atomically or not at all — is never written, the
// history is exactly what it was, and the submission leaves no trace.
func TestWithdrawDuringAutoCompactionCancelsItCleanly(t *testing.T) {
	entered := make(chan struct{})
	var cancelled atomic.Bool
	model := compactionHoldingLLM{entered: entered, once: &sync.Once{}, cancelled: &cancelled}

	s := newCharacterizationSession(t, model)
	sessionID := "withdraw-during-compaction"

	// Force the threshold low enough that a short seeded history crosses it.
	// CompactionService reads this config at call time.
	s.runner().AppCfg.Compact.ModelAutoCompactTokenLimit = 40

	store := s.sessStore()
	seedCtx := context.Background()
	for i := 0; i < 8; i++ {
		if _, err := store.Append(seedCtx, sessionID, "user", fmt.Sprintf("earlier question number %d with enough words to count", i)); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Append(seedCtx, sessionID, "assistant", fmt.Sprintf("earlier answer number %d with enough words to count", i)); err != nil {
			t.Fatal(err)
		}
	}
	seeded, err := store.ListRecentMessages(seedCtx, sessionID, 100)
	if err != nil {
		t.Fatal(err)
	}
	seededCount := len(seeded)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	foreground := &foregroundTurn{cancel: cancel}
	ctx = context.WithValue(ctx, foregroundTurnKey{}, foreground)

	done := make(chan error, 1)
	go func() {
		done <- s.DispatchSurfaceTurn(ctx, turn.TurnSubmission{SessionID: sessionID, UserText: "the question that triggers compaction"})
	}()
	select {
	case <-entered:
	case <-time.After(20 * time.Second):
		t.Fatal("auto-compaction never started; the threshold did not trip")
	}

	// Esc, mid-rewrite.
	if !foreground.withdraw() {
		t.Fatal("withdrawal refused while the compaction was still running")
	}

	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("dispatch returned %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("dispatch never returned")
	}
	require.True(t, cancelled.Load(), "the compaction's request outlived the withdrawal")

	// Nothing was rewritten: no checkpoint, the history as it was.
	boundaryID, _, err := store.LatestCompactBoundary(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("LatestCompactBoundary: %v", err)
	}
	if boundaryID != 0 {
		t.Fatalf("a cancelled compaction left a checkpoint behind: boundary=%d", boundaryID)
	}

	// The submission left nothing behind: it never got as far as its own row.
	rows, err := store.ListRecentMessages(context.Background(), sessionID, 200)
	if err != nil {
		t.Fatalf("ListRecentMessages: %v", err)
	}
	for _, row := range rows {
		if strings.Contains(row.Content, "the question that triggers compaction") {
			t.Fatalf("the withdrawn submission survived the compaction boundary: %+v", row)
		}
	}
	if len(rows) != seededCount {
		t.Fatalf("transcript went from %d to %d rows across a withdrawn turn", seededCount, len(rows))
	}
}
