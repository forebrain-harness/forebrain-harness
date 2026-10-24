package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mattn/go-runewidth"
)

// The reducer half of the transport retry: the dead attempt's buffered text
// goes, no frame is emitted, and the tracker holds the payload's progress for
// the Working line.
func TestTransportRetryMsgSetsTrackerAndEmitsNoFrames(t *testing.T) {
	tracker := NewTracker()
	var r Reducer
	r.WithTracker(tracker)
	// Seed the primary agent buffer with the dead attempt's in-flight text:
	// an assistant tail and a reasoning block that never ended.
	buf := r.agentBuf("")
	buf.assistant.WriteString("half an answer")
	buf.reasoning.WriteString("late thought")

	ev := r.Reduce(TransportRetryMsg{
		Retry: 1,
		Total: 10,
		Wait:  8 * time.Second,
		Err:   errors.New("read tcp 10.0.0.1:1->10.0.0.2:2: read: operation timed out"),
	})
	if len(ev.Frames) != 0 {
		t.Fatalf("frames = %+v, want none — the retry lives on the Working line", ev.Frames)
	}
	buf = r.agentBuf("")
	if buf == nil || buf.assistant.Len() != 0 || buf.reasoning.Len() != 0 {
		t.Fatal("the dead attempt's buffered text survived the retry")
	}
	snap := tracker.SnapshotActiveRun()
	if snap.Retry.Retry != 1 || snap.Retry.Total != 10 {
		t.Fatalf("tracker retry = %d/%d, want 1/10 from the payload", snap.Retry.Retry, snap.Retry.Total)
	}
	if !snap.Retry.Active() {
		t.Fatal("the recorded retry must read as active")
	}
	if want := "read tcp 10.0.0.1:1->10.0.0.2:2: read: operation timed out"; snap.Retry.Err != want {
		t.Fatalf("tracker retry err = %q, want the transport's own sentence", snap.Retry.Err)
	}
	if remaining := time.Until(snap.Retry.Deadline); remaining <= 0 || remaining > 8*time.Second {
		t.Fatalf("tracker retry deadline = %v away, want about 8s", remaining)
	}
}

// The renderer half of the boundary: the dead stream's trailing live blocks
// are withdrawn (removed), while the finished block below them stays sealed
// as history, and a zero-output death changes nothing.
func TestWithdrawDeadStreamBlocksRemovesOnlyLiveBlocks(t *testing.T) {
	r := &Renderer{}
	r.vm.append(Frame{Kind: FrameThinking, Content: "sealed thought", Final: true})
	r.vm.append(Frame{Kind: FrameAssistant, Content: "dead half-answer"})
	r.WithdrawDeadStreamBlocks("")
	if len(r.vm.blocks) != 1 {
		t.Fatalf("blocks after withdrawal = %d, want only the finished thinking block", len(r.vm.blocks))
	}
	if got := r.vm.blocks[0].frame; got.Kind != FrameThinking || !got.Final || got.Content != "sealed thought" {
		t.Fatalf("surviving block = %+v, want the sealed thinking kept", got)
	}
	// A zero-output death has no live blocks: no-op.
	r.WithdrawDeadStreamBlocks("")
	if len(r.vm.blocks) != 1 {
		t.Fatalf("repeat withdrawal changed the transcript: %d blocks", len(r.vm.blocks))
	}
	// The retried attempt's first delta opens its own block.
	r.RenderFrame(Frame{Kind: FrameAssistant, Content: "fresh answer"})
	if len(r.vm.blocks) != 2 {
		t.Fatalf("blocks = %d, want the sealed one plus the fresh live one", len(r.vm.blocks))
	}
	fresh := r.vm.blocks[1].frame
	if fresh.Content != "fresh answer" || fresh.Final {
		t.Fatalf("the retried delta must start its own live block, got %+v", fresh)
	}
}

// The retry's Cleared notice drops the status AND withdraws whatever the
// final dead attempt left behind. At exhaustion no further non-Cleared notice
// comes — the final attempt dies after the last announcement, and the only
// notice that follows is the deferred Cleared — so it must clear the buffer
// too. On a successful retry the notice lands before the retried stream's
// first token, when the buffer is already empty: a no-op there.
func TestTransportRetryClearedClearsTheStatus(t *testing.T) {
	tracker := NewTracker()
	var r Reducer
	r.WithTracker(tracker)
	r.Reduce(TransportRetryMsg{Retry: 2, Total: 2, Wait: time.Second, Err: errors.New("x")})
	if !tracker.SnapshotActiveRun().Retry.Active() {
		t.Fatal("sanity: the retry must be pending before the clear")
	}
	// The final dead attempt streamed after the last retry was announced;
	// its words are still buffered when the Cleared notice arrives.
	buf := r.agentBuf("")
	buf.assistant.WriteString("partial answer that never")
	buf.reasoning.WriteString("dead thought")

	ev := r.Reduce(TransportRetryMsg{Retry: 2, Total: 2, Cleared: true})
	if len(ev.Frames) != 0 {
		t.Fatalf("frames = %+v, want none", ev.Frames)
	}
	if tracker.SnapshotActiveRun().Retry.Active() {
		t.Fatal("the cleared notice left the retry status pending")
	}
	buf = r.agentBuf("")
	if buf == nil || buf.assistant.Len() != 0 || buf.reasoning.Len() != 0 {
		t.Fatal("the cleared notice left the final dead attempt's text buffered")
	}
}

// run.go's TransportRetryMsg branch withdraws the dead stream's live blocks
// for BOTH notices, so the exhaustion timeline — whose only notice after the
// final dead attempt is the Cleared one — leaves the sealed history alone in
// the view, the same way a mid-turn retry does.
func TestClearedTransportRetryWithdrawsTheFinalDeadAttempt(t *testing.T) {
	tracker := NewTracker()
	var reducer Reducer
	reducer.WithTracker(tracker)
	renderer := &Renderer{}
	renderer.vm.append(Frame{Kind: FrameThinking, Content: "sealed thought", Final: true})
	// The final dead attempt's partial, streamed after the last retry was
	// announced; only the deferred Cleared notice follows it.
	renderer.vm.append(Frame{Kind: FrameAssistant, Content: "partial answer that never"})

	m := TransportRetryMsg{Retry: 2, Total: 2, Cleared: true}
	// The run.go loop, gate for gate: reduce first, then withdraw.
	reducer.Reduce(m)
	if !m.turn.isWithdrawn() && renderer != nil {
		renderer.WithdrawDeadStreamBlocks(m.AgentID)
	}

	if len(renderer.vm.blocks) != 1 || renderer.vm.blocks[0].frame.Content != "sealed thought" {
		t.Fatalf("view = %+v, want only the sealed block after the cleared notice", renderer.vm.blocks)
	}
	if tracker.SnapshotActiveRun().Retry.Active() {
		t.Fatal("the cleared notice left the retry status pending")
	}
}

// While a retry is pending the Working line is the retry status alone: the
// reason (generic for the first attempts, the transport's own sentence from
// the third on, cut to 60 columns), the countdown, and the attempt counter —
// never the ordinary "Working" segments.
func TestWorkingLineShowsRetryStatus(t *testing.T) {
	formatterFor := func(s TransportRetryStatus) transientStatusFormatter {
		return workingStatusFormatter(func() Counters { return Counters{Retry: s} }, nil)
	}

	// Attempt 2 of 10 names the failure generically.
	got := formatterFor(TransportRetryStatus{Retry: 2, Total: 10, Deadline: time.Now().Add(8 * time.Second), Err: "read tcp x"})(20*time.Second, 0, "")
	if !strings.Contains(got, "Network error · Retrying in 8s · attempt 2/10") {
		t.Fatalf("retry line = %q, want the generic reason with the countdown and progress", got)
	}
	if strings.Contains(got, "Working") || strings.Contains(got, "esc to interrupt") {
		t.Fatalf("retry line = %q, must not carry the ordinary Working segments", got)
	}

	// From the third attempt the transport's own sentence is the reason.
	long := strings.Repeat("a", 80)
	got = formatterFor(TransportRetryStatus{Retry: 3, Total: 10, Deadline: time.Now().Add(8 * time.Second), Err: long})(20*time.Second, 0, "")
	if !strings.Contains(got, "attempt 3/10") {
		t.Fatalf("retry line = %q, want attempt 3/10", got)
	}
	reason := got
	if i := strings.Index(reason, " · Retrying in"); i >= 0 {
		reason = reason[:i]
	}
	reason = strings.TrimPrefix(reason, transientSpinnerFrame(0)+" ")
	if reason != long[:59]+"…" {
		t.Fatalf("reason = %q, want the error cut to 60 columns ending in …", reason)
	}
	if runewidth.StringWidth(reason) != 60 {
		t.Fatalf("reason width = %d, want 60", runewidth.StringWidth(reason))
	}

	// A total under three keeps the generic reason one attempt longer:
	// min(3, 2) = 2, so attempt 1 is generic and attempt 2 is verbatim.
	got = formatterFor(TransportRetryStatus{Retry: 1, Total: 2, Deadline: time.Now().Add(8 * time.Second), Err: "read tcp x"})(20*time.Second, 0, "")
	if !strings.Contains(got, "Network error · Retrying in 8s · attempt 1/2") {
		t.Fatalf("1/2 retry line = %q, want the generic reason", got)
	}
	got = formatterFor(TransportRetryStatus{Retry: 2, Total: 2, Deadline: time.Now().Add(8 * time.Second), Err: "read tcp x"})(20*time.Second, 0, "")
	if !strings.Contains(got, "read tcp x · Retrying in 8s · attempt 2/2") {
		t.Fatalf("2/2 retry line = %q, want the verbatim reason", got)
	}

	// A deadline that has passed reads 0s until the request produces or fails.
	got = formatterFor(TransportRetryStatus{Retry: 3, Total: 10, Deadline: time.Now().Add(-time.Second), Err: "read tcp x"})(20*time.Second, 0, "")
	if !strings.Contains(got, "Retrying in 0s") {
		t.Fatalf("past-deadline retry line = %q, want Retrying in 0s", got)
	}
}

// The countdown's tiers: seconds under a minute (ceilinged), minutes and
// seconds under five, then the largest unit alone.
func TestFormatRetryCountdownTiers(t *testing.T) {
	cases := []struct {
		remaining time.Duration
		want      string
	}{
		{500 * time.Millisecond, "1s"},
		{8 * time.Second, "8s"},
		{59200 * time.Millisecond, "1m 0s"},
		{65 * time.Second, "1m 5s"},
		{6 * time.Minute, "6m"},
		{150 * time.Minute, "2h"},
		{26 * time.Hour, "1d"},
		{-time.Second, "0s"},
	}
	for _, tc := range cases {
		if got := formatRetryCountdown(tc.remaining); got != tc.want {
			t.Fatalf("formatRetryCountdown(%v) = %q, want %q", tc.remaining, got, tc.want)
		}
	}
}

// A subagent's view shows that subagent's own retry; the conversation's
// Working line shows the conversation's — one agent's dead stream is not the
// other's news.
func TestSubagentWorkingLineShowsItsOwnRetry(t *testing.T) {
	tracker := NewTracker()
	tracker.ObserveAgent("task-1", "exec-1", time.Now())
	tracker.ObserveTransportRetry("task-1", 1, 10, 8*time.Second, "x")

	formatter := workingStatusFormatter(tracker.SnapshotActiveRun, tracker.SnapshotAgentRun)
	sub := formatter(time.Second, 0, "task-1")
	if !strings.Contains(sub, "Retrying in 8s · attempt 1/10") {
		t.Fatalf("subagent working line = %q, want its own retry status", sub)
	}
	if strings.Contains(sub, "Working (") {
		t.Fatalf("subagent working line = %q, must be the retry status alone", sub)
	}
	conv := formatter(time.Second, 0, "")
	if !strings.Contains(conv, "Working (") || strings.Contains(conv, "Retrying in") {
		t.Fatalf("conversation working line = %q, want the ordinary line with no retry status", conv)
	}
}

// A new run does not inherit the previous run's retry status.
func TestStartRunClearsAStaleRetry(t *testing.T) {
	tracker := NewTracker()
	tracker.ObserveTransportRetry("", 1, 10, time.Second, "x")
	if !tracker.SnapshotActiveRun().Retry.Active() {
		t.Fatal("sanity: the retry must be pending before the new run")
	}
	tracker.StartRun("run-2")
	if tracker.SnapshotActiveRun().Retry.Active() {
		t.Fatal("StartRun left the previous run's retry status pending")
	}
}
