package tui

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// The reducer half of the transport retry: the dead attempt's buffered text
// goes, and the error/retry card pair is emitted with the payload's progress.
func TestTransportRetryMsgClearsBuffersAndEmitsCardPair(t *testing.T) {
	var r Reducer
	// Seed the primary agent buffer with the dead attempt's in-flight text:
	// an assistant tail and a reasoning block that never ended.
	buf := r.agentBuf("")
	buf.assistant.WriteString("half an answer")
	buf.reasoning.WriteString("late thought")

	ev := r.Reduce(TransportRetryMsg{
		Retry: 1,
		Total: 10,
		Wait:  2 * time.Second,
		Err:   errors.New("read tcp 10.0.0.1:1->10.0.0.2:2: read: operation timed out"),
	})
	if len(ev.Frames) != 2 {
		t.Fatalf("frames = %d, want the error/retry pair", len(ev.Frames))
	}
	errCard, retryCard := ev.Frames[0], ev.Frames[1]
	if errCard.Kind != FrameError || errCard.Title != "Network error" ||
		!strings.Contains(errCard.Content, "operation timed out") {
		t.Fatalf("error card = %+v, want the transport failure verbatim", errCard)
	}
	if retryCard.Kind != FrameRetrying || retryCard.Final {
		t.Fatalf("retry card = %+v, want a live FrameRetrying", retryCard)
	}
	if retryCard.Retry != 1 || retryCard.RetryTotal != 10 {
		t.Fatalf("retry card progress = %d/%d, want 1/10 from the payload", retryCard.Retry, retryCard.RetryTotal)
	}
	if retryCard.RetryDeadline.IsZero() {
		t.Fatal("retry card carries no deadline for the countdown")
	}
	buf = r.agentBuf("")
	if buf == nil || buf.assistant.Len() != 0 || buf.reasoning.Len() != 0 {
		t.Fatal("the dead attempt's buffered text survived the retry")
	}
}

// The exhausted notification freezes the retry card: same StepID, Final, and
// the t/total wording the renderer derives from the payload.
func TestTransportRetryExhaustedFreezesTheCard(t *testing.T) {
	var r Reducer
	ev := r.Reduce(TransportRetryMsg{Retry: 3, Total: 3, Err: errors.New("boom"), Exhausted: true})
	if len(ev.Frames) != 2 || ev.Frames[1].Kind != FrameRetrying {
		t.Fatalf("frames = %+v, want the pair with a retry card", ev.Frames)
	}
	card := ev.Frames[1]
	if !card.Final {
		t.Fatal("the exhausted card must be Final (frozen)")
	}
	if card.RetryDeadline.After(time.Now()) {
		t.Fatal("an exhausted card carries no wait")
	}
	if got := retryCardLine(card, 0); !strings.Contains(got, "Could not continue, tried 3/3 times") {
		t.Fatalf("frozen line = %q, want Could not continue, tried 3/3 times", got)
	}
}

// The live card's wording walks the countdown and the in-flight state, with
// the progress straight from the payload (no hard-coded denominator).
func TestRetryCardLineWordingAndCountdownTiers(t *testing.T) {
	live := Frame{Kind: FrameRetrying, Retry: 2, RetryTotal: 3, RetryDeadline: time.Now().Add(90 * time.Second)}
	if got := retryCardLine(live, 0); !strings.Contains(got, "continuing in 2m (2/3)") {
		t.Fatalf("live line = %q, want a 2m countdown with 2/3", got)
	}
	inFlight := live
	inFlight.RetryDeadline = time.Now().Add(-time.Second)
	if got := retryCardLine(inFlight, 0); !strings.Contains(got, "Continuing (2/3)") {
		t.Fatalf("in-flight line = %q, want Continuing (2/3)", got)
	}
	hours := Frame{Kind: FrameRetrying, Retry: 9, RetryTotal: 20, RetryDeadline: time.Now().Add(25 * time.Hour)}
	if got := retryCardLine(hours, 0); !strings.Contains(got, "continuing in 2d (9/20)") {
		t.Fatalf("day-tier line = %q, want a 2d countdown with 9/20", got)
	}
	secs := Frame{Kind: FrameRetrying, Retry: 1, RetryTotal: 10, RetryDeadline: time.Now().Add(1500 * time.Millisecond)}
	if got := retryCardLine(secs, 0); !strings.Contains(got, "continuing in 2s (1/10)") {
		t.Fatalf("second-tier line = %q, want a ceiling 2s countdown", got)
	}
}

// The renderer half of the boundary: the dead stream's trailing live blocks
// are sealed in place (kept, finalized) — what the user watched appear stays
// and the engine persisted it — while sealed blocks below them stay sealed
// and a zero-output death changes nothing.
func TestSealDeadStreamBlocksKeepsTrailingLiveBlocksSealed(t *testing.T) {
	r := &Renderer{}
	r.vm.append(Frame{Kind: FrameThinking, Content: "sealed thought", Final: true})
	r.vm.append(Frame{Kind: FrameAssistant, Content: "sealed answer", Final: true})
	r.vm.append(Frame{Kind: FrameThinking, Content: "live thought"})
	r.vm.append(Frame{Kind: FrameAssistant, Content: "dead half-answer"})
	r.SealDeadStreamBlocks("")
	var kinds []string
	var finals []bool
	for _, b := range r.vm.blocks {
		kinds = append(kinds, string(b.frame.Kind))
		finals = append(finals, b.frame.Final)
	}
	if strings.Join(kinds, "|") != "thinking|assistant|thinking|assistant" || len(r.vm.blocks) != 4 {
		t.Fatalf("blocks after sealing = %v, want all four kept", kinds)
	}
	if !finals[0] || !finals[1] || !finals[2] || !finals[3] {
		t.Fatalf("finals after sealing = %v, want every dead-stream block sealed", finals)
	}
	// A zero-output death has no live blocks: no-op.
	r.SealDeadStreamBlocks("")
	if len(r.vm.blocks) != 4 {
		t.Fatalf("repeat sealing changed the transcript: %d blocks", len(r.vm.blocks))
	}
	// The retried attempt's first delta opens its own block below the kept
	// words instead of growing the sealed one.
	r.RenderFrame(Frame{Kind: FrameAssistant, Content: "fresh answer"})
	if len(r.vm.blocks) != 5 {
		t.Fatalf("blocks = %d, want the sealed four plus the fresh live one", len(r.vm.blocks))
	}
	fresh := r.vm.blocks[4].frame
	if fresh.Content != "fresh answer" || fresh.Final {
		t.Fatalf("the retried delta must start its own live block, got %+v", fresh)
	}
}

// Consecutive failures update the same two blocks in place — no per-failure
// stacking — and the exhausted frame seals the very same card.
func TestRetryCardsReplaceInPlace(t *testing.T) {
	r := &Renderer{}
	r.RenderFrame(Frame{Kind: FrameRetrying, StepID: transportRetryStepIDFor(""), Retry: 1, RetryTotal: 3, RetryDeadline: time.Now().Add(time.Second)})
	r.RenderFrame(Frame{Kind: FrameError, Title: "Network error", StepID: transportRetryErrorStepIDFor(""), Content: "first", Final: true})
	r.RenderFrame(Frame{Kind: FrameRetrying, StepID: transportRetryStepIDFor(""), Retry: 2, RetryTotal: 3, RetryDeadline: time.Now().Add(2 * time.Second)})
	r.RenderFrame(Frame{Kind: FrameError, Title: "Network error", StepID: transportRetryErrorStepIDFor(""), Content: "second", Final: true})
	r.RenderFrame(Frame{Kind: FrameRetrying, StepID: transportRetryStepIDFor(""), Retry: 3, RetryTotal: 3, Final: true})
	if len(r.vm.blocks) != 2 {
		t.Fatalf("blocks = %d, want one error card and one retry card", len(r.vm.blocks))
	}
	var errCard, retryCard Frame
	for _, b := range r.vm.blocks {
		switch b.frame.Kind {
		case FrameError:
			errCard = b.frame
		case FrameRetrying:
			retryCard = b.frame
		}
	}
	if errCard.Content != "second" {
		t.Fatalf("error card = %+v, want the latest failure in place", errCard)
	}
	if !retryCard.Final || retryCard.Retry != 3 {
		t.Fatalf("retry card = %+v, want the frozen 3/3 card", retryCard)
	}
}

// Recovery removes the live card on the retried attempt's first streamed
// words; the turn's closing frames remove whatever is still waiting; the
// frozen exhausted card is history and survives both.
func TestLiveRetryCardDismissal(t *testing.T) {
	// Recovery: the first assistant delta of the retried attempt.
	r := &Renderer{}
	r.RenderFrame(Frame{Kind: FrameRetrying, StepID: transportRetryStepIDFor(""), Retry: 1, RetryTotal: 3, RetryDeadline: time.Now().Add(time.Hour)})
	r.RenderFrame(Frame{Kind: FrameAssistant, Content: "the answer arrives"})
	if len(r.vm.blocks) != 1 || r.vm.blocks[0].frame.Kind != FrameAssistant {
		t.Fatalf("blocks after recovery = %+v, want the retry card gone", r.vm.blocks)
	}
	if r.liveRetryAgents[""] {
		t.Fatal("the live-card tracker still names the conversation")
	}

	// Turn close: a status frame sweeps every view's still-live card.
	r2 := &Renderer{}
	r2.RenderFrame(Frame{Kind: FrameRetrying, StepID: transportRetryStepIDFor(""), Retry: 2, RetryTotal: 3, RetryDeadline: time.Now().Add(time.Hour)})
	r2.RenderFrame(Frame{Kind: FrameStatus, Title: "Worked for 4s", Final: true})
	if len(r2.vm.blocks) != 1 || r2.vm.blocks[0].frame.Kind != FrameStatus {
		t.Fatalf("blocks after turn close = %+v, want only the status", r2.vm.blocks)
	}

	// A frozen card is history: neither recovery nor close removes it.
	r3 := &Renderer{}
	r3.RenderFrame(Frame{Kind: FrameRetrying, StepID: transportRetryStepIDFor(""), Retry: 3, RetryTotal: 3, Final: true})
	r3.RenderFrame(Frame{Kind: FrameAssistant, Content: "late words"})
	r3.RenderFrame(Frame{Kind: FrameStatus, Title: "Worked for 4s", Final: true})
	if len(r3.vm.blocks) != 3 {
		t.Fatalf("blocks = %d, want frozen card, assistant and status kept", len(r3.vm.blocks))
	}
}

// A subagent's retry card lives in its own view; its recovery is scoped to
// that view and never touches the conversation.
func TestSubagentRetryCardIsScopedToItsView(t *testing.T) {
	r := &Renderer{}
	r.RenderFrame(Frame{Kind: FrameRetrying, StepID: transportRetryStepIDFor("task-1"), AgentID: "task-1", Retry: 1, RetryTotal: 2, RetryDeadline: time.Now().Add(time.Hour)})
	if len(r.vm.blocks) != 0 {
		t.Fatalf("primary transcript = %d blocks, want none", len(r.vm.blocks))
	}
	vm := r.perAgentVM["task-1"]
	if vm == nil || len(vm.blocks) != 1 {
		t.Fatalf("subagent view = %+v, want its retry card", vm)
	}
	r.SealDeadStreamBlocks("task-1")
	if len(vm.blocks) != 1 {
		t.Fatalf("the retry card itself must survive the dead-stream withdrawal, got %d blocks", len(vm.blocks))
	}
	r.RenderFrame(Frame{Kind: FrameAssistant, AgentID: "task-1", Content: "recovered"})
	if len(vm.blocks) != 1 || vm.blocks[0].frame.Kind != FrameAssistant {
		t.Fatalf("subagent view after recovery = %+v, want the card replaced by the answer", vm.blocks)
	}
}

// The three animation gates: without them the spinner never turns and the
// countdown never walks.
func TestRetryCardAnimationGating(t *testing.T) {
	live := &viewBlock{frame: Frame{Kind: FrameRetrying, Retry: 1, RetryTotal: 3, RetryDeadline: time.Now().Add(time.Hour)}}
	if !hasLiveRetryFrame(live.frame) {
		t.Fatal("sanity: the frame under test must be a live retry card")
	}
	r := &Renderer{}
	r.vm.append(live.frame)
	r.mu.Lock()
	liveBlock := r.hasLiveBlockLocked()
	r.mu.Unlock()
	if !liveBlock {
		t.Fatal("a live retry card must drive the animation ticker")
	}
	// The frozen card does not.
	r.mu.Lock()
	r.vm.blocks[0].frame.Final = true
	frozen := r.hasLiveBlockLocked()
	r.mu.Unlock()
	if frozen {
		t.Fatal("a frozen retry card must stop the animation ticker")
	}
	// The rendered line advances with the spinner phase (cache invalidation
	// is keyed on it via the animated flag in renderBlockFull).
	phase0 := retryCardLine(Frame{Kind: FrameRetrying, Retry: 1, RetryTotal: 3, RetryDeadline: time.Now().Add(time.Hour)}, 0)
	phase1 := retryCardLine(Frame{Kind: FrameRetrying, Retry: 1, RetryTotal: 3, RetryDeadline: time.Now().Add(time.Hour)}, 1)
	if phase0 == phase1 {
		t.Fatalf("the marker must rotate with the phase: %q vs %q", phase0, phase1)
	}
}

func hasLiveRetryFrame(f Frame) bool {
	return f.Kind == FrameRetrying && !f.Final
}
