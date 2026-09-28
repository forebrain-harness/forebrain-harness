package run

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

func TestControllerSerializesSessionAndAllowsOtherSessions(t *testing.T) {
	c := NewController()
	if _, err := c.Begin(context.Background(), "r1", "s1"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := c.Begin(ctx, "r2", "s1"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("same session error = %v", err)
	}
	if _, err := c.Begin(context.Background(), "r3", "s2"); err != nil {
		t.Fatalf("other session: %v", err)
	}
	if !c.Finish("r1") || !c.Finish("r3") {
		t.Fatal("finish failed")
	}
}

func TestControllerCancelAndQueue(t *testing.T) {
	c := NewController()
	if _, err := c.Begin(context.Background(), "r1", "s1"); err != nil {
		t.Fatal(err)
	}
	if !c.Steer("r1", Input{Text: "first"}) || !c.FollowUp("r1", Input{Text: "next"}) {
		t.Fatal("queue failed")
	}
	if in, ok := c.Retract("r1"); !ok || in.Text != "first" {
		t.Fatalf("retract = %#v, %v", in, ok)
	}
	if !c.Cancel("r1", context.Canceled) || c.Cancel("r1", context.Canceled) {
		t.Fatal("cancel must be idempotent")
	}
	if c.Steer("r1", Input{Text: "late"}) {
		t.Fatal("steer accepted after cancellation")
	}
}

func TestControllerKeepsQueueAcrossApproval(t *testing.T) {
	c := NewController()
	if !c.Track("r1", "s1", func() {}) {
		t.Fatal("track")
	}
	q, _, ok := c.Queue("r1")
	if !ok || !q.FollowUp(Input{Text: "after approval"}) {
		t.Fatal("queue input")
	}
	if !c.WaitApproval("r1") || !c.Resume("r1") {
		t.Fatal("approval transition")
	}
	q, _, ok = c.Queue("r1")
	if !ok {
		t.Fatal("queue lost")
	}
	in, ok := q.PopNext()
	if !ok || in.Text != "after approval" {
		t.Fatalf("input = %#v, %v", in, ok)
	}
}

// TestControllerPopNextKeepsWhatRejectedInputAttached pins that input the run
// could not take as a steer is merged into one message with everything each
// piece attached, not reduced to its text.
func TestControllerPopNextKeepsWhatRejectedInputAttached(t *testing.T) {
	c := NewController()
	if !c.Track("r1", "s1", func() {}) || !c.WaitApproval("r1") {
		t.Fatal("track")
	}
	c.Steer("r1", Input{Text: "look at this", MentionImages: []string{"shots/a.png"}})
	c.Steer("r1", Input{Attachments: []string{"file-1"}})
	q, _, ok := c.Queue("r1")
	if !ok {
		t.Fatal("queue")
	}
	in, ok := q.PopNext()
	if !ok || !in.Rejected || in.Text != "look at this" {
		t.Fatalf("input = %#v, %v", in, ok)
	}
	if len(in.Attachments) != 1 || in.Attachments[0] != "file-1" || len(in.MentionImages) != 1 || in.MentionImages[0] != "shots/a.png" {
		t.Fatalf("the merged input lost what it attached: %#v", in)
	}
	if _, ok := q.PopNext(); ok {
		t.Fatal("a rejected input was left behind the merge")
	}
}

func TestControllerClaimsResumeOnce(t *testing.T) {
	c := NewController()
	if _, err := c.Begin(context.Background(), "r1", "s1"); err != nil {
		t.Fatal(err)
	}
	if !c.WaitApproval("r1") || !c.ClaimResume("r1", "s1") || c.ClaimResume("r1", "s1") {
		t.Fatal("resume claim must be exclusive")
	}
	if !c.ClaimResume("r2", "s2") {
		t.Fatal("persisted run was not restored")
	}
}

func TestControllerTracksProvidedInputRuntime(t *testing.T) {
	c := NewController()
	rt := NewTurnInputRuntime()
	rt.Enqueue(TurnInputModeSteer, []llm.ContentPart{llm.Text("keep")})
	if !c.TrackRuntime("r1", "s1", func() {}, rt) {
		t.Fatal("track")
	}
	q, _, ok := c.Queue("r1")
	if !ok || q.Runtime() != rt {
		t.Fatal("runtime was replaced")
	}
	if got := q.Runtime().DrainSteers(); len(got) != 1 {
		t.Fatalf("steers = %#v", got)
	}
}

// TestControllerReleasesEverythingStillQueued pins that a finishing run hands
// back every message it never took — the steer that never reached the model
// first, then queued messages in order, each with what it attached — and takes
// nothing more once it has, so no message can be dropped with the run.
func TestControllerReleasesEverythingStillQueued(t *testing.T) {
	c := NewController()
	if !c.Track("r1", "s1", func() {}) {
		t.Fatal("track")
	}
	if !c.Steer("r1", Input{Text: "later"}) {
		t.Fatal("steer")
	}
	if !c.FollowUp("r1", Input{Text: "next", Attachments: []string{"file-1"}, MentionImages: []string{"a.png"}}) || !c.FollowUp("r1", Input{Text: "after that"}) {
		t.Fatal("follow up")
	}
	released := c.Release("r1")
	if len(released) != 3 || released[0].Text != "later" || !released[0].Rejected ||
		released[1].Text != "next" || released[1].Attachments[0] != "file-1" || released[1].MentionImages[0] != "a.png" ||
		released[2].Text != "after that" {
		t.Fatalf("released = %#v", released)
	}
	if c.Steer("r1", Input{Text: "too late"}) || c.FollowUp("r1", Input{Text: "too late"}) {
		t.Fatal("a released run took more input, which would be dropped with it")
	}
	if again := c.Release("r1"); again != nil {
		t.Fatalf("released twice: %#v", again)
	}
}

func TestControllerFindsRunsBySession(t *testing.T) {
	c := NewController()
	if !c.Track("r1", "s1", func() {}) || !c.Track("r2", "s2", func() {}) {
		t.Fatal("track")
	}
	id, phase, ok := c.Find(" s2 ")
	if !ok || id != "r2" || phase != Running {
		t.Fatalf("find = %q, %q, %v", id, phase, ok)
	}
	if _, _, ok := c.Find("missing"); ok {
		t.Fatal("found missing session")
	}
}

func TestOptionsControlKeepsRunUntilCallerFinishes(t *testing.T) {
	ctl := NewController()
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	o := Options{Control: ctl}
	o.trackRun("r1", "s1", cancel)
	if ctx.Err() != nil {
		t.Fatal("track cancelled context")
	}
	if ctl.Active() != 1 {
		t.Fatal("track")
	}
	o.suspendRun("r1")
	state, ok := ctl.Get("r1")
	if !ok || state.Phase() != Waiting {
		t.Fatalf("state = %#v, %v", state, ok)
	}
	if ctl.Active() != 1 {
		t.Fatal("controller run ended before caller finalized input")
	}
	if !ctl.Finish("r1") {
		t.Fatal("finish")
	}
}

func TestControllerConcurrentLifecycle(t *testing.T) {
	c := NewController()
	var calls atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		id := "r" + string(rune('a'+i))
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.Track(id, "s"+id, func() { calls.Add(1) })
			c.Cancel(id, context.Canceled)
			c.Untrack(id)
		}()
	}
	wg.Wait()
	if calls.Load() != 32 {
		t.Fatalf("cancel calls = %d, want 32", calls.Load())
	}
	if got := c.Active(); got != 0 {
		t.Fatalf("active = %d, want 0", got)
	}
}

// P6-4 requires that a concurrent double cancel produce exactly one cancelled
// event. TestControllerCancelAndQueue only covers the sequential case (cancel,
// then cancel again on the same goroutine), and TestControllerConcurrentLifecycle
// cancels 32 *distinct* runs once each — neither can catch two goroutines both
// passing the phase check on the *same* run and both firing the stop callback.
// Cancel guards that with a phase transition to Finishing under state.mu; this
// pins it, and is meaningful under -race in particular.
func TestControllerConcurrentCancelFiresExactlyOnce(t *testing.T) {
	const racers = 16
	for attempt := 0; attempt < 50; attempt++ {
		c := NewController()
		var fired atomic.Int32
		if !c.Track("r1", "s1", func() { fired.Add(1) }) {
			t.Fatal("Track failed")
		}

		var wonCount atomic.Int32
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := 0; i < racers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				if c.Cancel("r1", context.Canceled) {
					wonCount.Add(1)
				}
			}()
		}
		close(start)
		wg.Wait()

		if got := wonCount.Load(); got != 1 {
			t.Fatalf("attempt %d: %d concurrent Cancel calls reported success, want exactly 1", attempt, got)
		}
		if got := fired.Load(); got != 1 {
			t.Fatalf("attempt %d: the cancel callback fired %d times, want exactly 1 cancelled event", attempt, got)
		}
		c.Untrack("r1")
	}
}

// P7-9 requires a concurrent decision test proving only one resume is started
// for a run awaiting approval. ClaimResume enforces that with a CAS under
// state.mu — only the caller that observes phase == Waiting flips it to
// Running — but TestControllerClaimsResumeOnce exercises it sequentially, so a
// lost guard would still look correct there: the second call arrives after the
// phase is already Running and returns false for the wrong reason. Racing the
// claimants is what distinguishes the two.
func TestControllerConcurrentClaimResumeStartsExactlyOneResume(t *testing.T) {
	const racers = 16
	for attempt := 0; attempt < 50; attempt++ {
		c := NewController()
		if _, err := c.Begin(context.Background(), "r1", "s1"); err != nil {
			t.Fatalf("attempt %d: Begin: %v", attempt, err)
		}
		if !c.WaitApproval("r1") {
			t.Fatalf("attempt %d: WaitApproval did not park the run", attempt)
		}

		var claimed atomic.Int32
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := 0; i < racers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				if c.ClaimResume("r1", "s1") {
					claimed.Add(1)
				}
			}()
		}
		close(start)
		wg.Wait()

		if got := claimed.Load(); got != 1 {
			t.Fatalf("attempt %d: %d concurrent ClaimResume calls succeeded, want exactly 1 resume started", attempt, got)
		}
		c.Untrack("r1")
	}
}

func TestControllerTracksAdapterCancelOnce(t *testing.T) {
	c := NewController()
	var calls int
	if !c.Track("run-1", "session-1", func() { calls++ }) {
		t.Fatal("track")
	}
	if !c.Cancel("run-1", context.Canceled) || c.Cancel("run-1", context.Canceled) {
		t.Fatal("cancel must be idempotent")
	}
	if calls != 1 {
		t.Fatalf("cancel calls = %d", calls)
	}
	if !c.Untrack("run-1") || c.Active() != 0 {
		t.Fatal("untrack")
	}
}
