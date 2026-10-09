package run

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

func TestRetractSteerRemovesThePendingSteerItNames(t *testing.T) {
	rt := NewTurnInputRuntime()
	rt.enqueueEntry(TurnInputEntry{Mode: TurnInputModeSteer, Parts: []llm.ContentPart{llm.Text("first steer")}, Seq: 1})
	rt.enqueueEntry(TurnInputEntry{Mode: TurnInputModeFollowUp, Parts: []llm.ContentPart{llm.Text("a follow up")}, Seq: 2})
	rt.enqueueEntry(TurnInputEntry{Mode: TurnInputModeSteer, Parts: []llm.ContentPart{llm.Text("second steer")}, Seq: 3})

	if !rt.RetractSteer(3) {
		t.Fatalf("expected the named steer to be retracted")
	}

	remaining := rt.Snapshot()
	if len(remaining) != 2 {
		t.Fatalf("expected two entries left, got %#v", remaining)
	}
	if remaining[0].Mode != TurnInputModeSteer || llm.TextContent(remaining[0].Parts...) != "first steer" {
		t.Fatalf("expected earlier steer preserved, got %#v", remaining[0])
	}
	if remaining[1].Mode != TurnInputModeFollowUp || llm.TextContent(remaining[1].Parts...) != "a follow up" {
		t.Fatalf("expected follow-up preserved, got %#v", remaining[1])
	}
}

func TestTurnInputRuntimeNotifiesAllDeliveryHooks(t *testing.T) {
	rt := NewTurnInputRuntime()
	var first, second int
	rt.SetChangeHook(func([]TurnInputEntry) { first++ })
	rt.AddChangeHook(func([]TurnInputEntry) { second++ })
	rt.Enqueue(TurnInputModeSteer, []llm.ContentPart{llm.Text("one")})
	rt.DrainSteers()
	if first != 1 || second != 1 {
		t.Fatalf("hooks = %d, %d", first, second)
	}
}

func TestRetractSteerLeavesFollowUpsAlone(t *testing.T) {
	rt := NewTurnInputRuntime()
	rt.enqueueEntry(TurnInputEntry{Mode: TurnInputModeFollowUp, Parts: []llm.ContentPart{llm.Text("just a follow up")}, Seq: 1})

	if rt.RetractSteer(1) {
		t.Fatalf("a follow-up is not a steer to retract")
	}
	if remaining := rt.Snapshot(); len(remaining) != 1 {
		t.Fatalf("expected follow-up untouched, got %#v", remaining)
	}

	var nilRT *TurnInputRuntime
	if nilRT.RetractSteer(1) {
		t.Fatalf("expected nil runtime to report no steer")
	}
}

func TestTurnInputRuntimeChangeHookFiresWhenPendingChanges(t *testing.T) {
	rt := NewTurnInputRuntime()
	var calls int
	var lastDelivered []TurnInputEntry
	rt.SetChangeHook(func(delivered []TurnInputEntry) {
		calls++
		lastDelivered = delivered
	})

	rt.Enqueue(TurnInputModeSteer, []llm.ContentPart{llm.Text("first")})
	rt.Enqueue(TurnInputModeFollowUp, []llm.ContentPart{llm.Text("later")})
	if calls != 0 {
		t.Fatalf("expected enqueue to be caller-synchronized, got %d hook calls", calls)
	}

	rt.DrainSteers()
	if calls != 1 {
		t.Fatalf("expected drain hook, got %d", calls)
	}
	if len(lastDelivered) != 1 || lastDelivered[0].Mode != TurnInputModeSteer {
		t.Fatalf("expected delivered steer passed to hook, got %#v", lastDelivered)
	}

	if rt.RetractSteer(1) {
		t.Fatalf("expected no steer left to retract")
	}
	if calls != 1 {
		t.Fatalf("expected no hook when retract changes nothing, got %d", calls)
	}

	rt.DrainAll()
	if calls != 2 {
		t.Fatalf("expected drain-all hook, got %d", calls)
	}
}

// Engine-side queue semantics (SUBAGENT_CONVERSATION plan 004, step 2). The
// surface-side halves of Q1–Q12 are pinned by the characterization tests in
// pkg/tui; these pin the engine's own behavior.

// Q1: steer admission follows the TUI rule — any message with parts steers,
// attachments included, because Payload carries what it attached back whole.
// A queue with no attached runtime has no turn to deliver into: the message
// is kept as a rejected steer instead of waiting for the next turn.
func TestInputQueueSteerAdmission(t *testing.T) {
	q := NewInputQueue()
	rt := NewTurnInputRuntime()
	q.Attach(rt)
	if !q.Steer(Input{Text: "look", Attachments: []string{"file-1"}, MentionImages: []string{"shots/a.png"}, Payload: "surface record"}) {
		t.Fatal("an attached message must steer")
	}
	if got := rt.Snapshot(); len(got) != 1 || got[0].Mode != TurnInputModeSteer {
		t.Fatalf("steer did not reach the runtime: %#v", got)
	}
	send, _ := q.Next(BoundaryCompleted)
	if len(send) != 1 || send[0].Payload != "surface record" || send[0].Attachments[0] != "file-1" {
		t.Fatalf("payload did not travel with the steer: %#v", send)
	}

	q.Detach()
	if q.Steer(Input{Text: "missed the turn"}) {
		t.Fatal("a detached queue must not accept a steer")
	}
	send, restore := q.Next(BoundaryInterrupted)
	if len(send) != 0 || len(restore) != 1 || !restore[0].Rejected || restore[0].Text != "missed the turn" {
		t.Fatalf("detached steer = %#v, %#v", send, restore)
	}
}

// Q4: recall picks the newest message by the shared enqueue clock, whichever
// lane it landed in. A steer the model is already answering is skipped —
// and because the runtime delivers FIFO, so is the whole steer lane.
func TestInputQueueRecallPicksNewestByLane(t *testing.T) {
	t.Run("newest is a pending steer", func(t *testing.T) {
		q := NewInputQueue()
		rt := NewTurnInputRuntime()
		q.Attach(rt)
		q.FollowUp(Input{Text: "older follow-up"})
		q.Steer(Input{Text: "newest steer"})
		in, ok := q.Recall()
		if !ok || in.Text != "newest steer" {
			t.Fatalf("recall = %#v, %v", in, ok)
		}
		if got := rt.Snapshot(); len(got) != 0 {
			t.Fatalf("retracted steer still in the runtime: %#v", got)
		}
	})
	t.Run("newest lands in the rejected lane", func(t *testing.T) {
		q := NewInputQueue()
		rt := NewTurnInputRuntime()
		q.Attach(rt)
		q.Steer(Input{Text: "older steer"})
		q.FollowUp(Input{Text: "newest rejected", Rejected: true})
		in, ok := q.Recall()
		if !ok || in.Text != "newest rejected" {
			t.Fatalf("recall = %#v, %v", in, ok)
		}
		if preview := q.Preview(); len(preview.Steers) != 1 {
			t.Fatalf("older steer must stay queued: %#v", preview)
		}
	})
	t.Run("in-flight steer is retracted from its delivery", func(t *testing.T) {
		q := NewInputQueue()
		rt := NewTurnInputRuntime()
		q.Attach(rt)
		q.FollowUp(Input{Text: "older follow-up"})
		q.Steer(Input{Text: "in flight"})
		// The runtime took the steer for a model call that has not begun
		// answering: it still shows in the queue, so it is still the user's.
		delivery := rt.BeginSteerDelivery(context.Background())
		if delivery == nil {
			t.Fatal("expected an in-flight delivery")
		}
		in, ok := q.Recall()
		if !ok || in.Text != "in flight" {
			t.Fatalf("recall = %#v, %v", in, ok)
		}
		if delivery.Commit() {
			t.Fatal("the call that carried a recalled steer must not commit it")
		}
	})
	t.Run("steer the model is answering is skipped", func(t *testing.T) {
		q := NewInputQueue()
		rt := NewTurnInputRuntime()
		q.Attach(rt)
		q.FollowUp(Input{Text: "older follow-up"})
		q.Steer(Input{Text: "being answered"})
		// Stand in for the instant between the commit and the queue hearing
		// of it: the steer still shows, but the model is answering it.
		rt.SetChangeHook(nil)
		delivery := rt.BeginSteerDelivery(context.Background())
		if delivery == nil || !delivery.Commit() {
			t.Fatal("expected a committed delivery")
		}
		in, ok := q.Recall()
		if !ok || in.Text != "older follow-up" {
			t.Fatalf("recall = %#v, %v", in, ok)
		}
	})
	t.Run("empty queue", func(t *testing.T) {
		if _, ok := (&InputQueue{}).Recall(); ok {
			t.Fatal("an empty queue recalls nothing")
		}
	})
}

// A steer still queued when the turn's runtime detached was never
// delivered — the run's deliveries settle before it can end — so it must
// stay editable, and once recalled the boundary must not send it either.
func TestRecallSteerAfterRuntimeDetach(t *testing.T) {
	q := NewInputQueue()
	rt := NewTurnInputRuntime()
	q.Attach(rt)
	if !q.Steer(Input{Text: "test message", Parts: []llm.ContentPart{llm.Text("test message")}}) {
		t.Fatal("Steer failed")
	}

	q.Detach()
	if q.Runtime() != nil {
		t.Fatal("expected runtime to be nil after Detach")
	}

	in, ok := q.Recall()
	if !ok {
		t.Fatal("Recall failed after runtime detach, but the steer was never delivered")
	}
	if in.Text != "test message" {
		t.Fatalf("recalled %q, want %q", in.Text, "test message")
	}
	if preview := q.Preview(); len(preview.Steers) != 0 {
		t.Fatalf("expected 0 steers after recall, got %d", len(preview.Steers))
	}
	if send, _ := q.Next(BoundaryCompleted); len(send) != 0 {
		t.Fatalf("a recalled steer must not be sent by the boundary: %#v", send)
	}
}

// A steer taken for a model call that has not committed is still the user's:
// retracting it shrinks the delivery, aborts the call carrying it, and keeps
// that call from committing; rolling back returns only what is left.
func TestRetractSteerTakesItBackFromAnInFlightDelivery(t *testing.T) {
	q := NewInputQueue()
	rt := NewTurnInputRuntime()
	q.Attach(rt)
	q.Steer(Input{Text: "first"})
	q.Steer(Input{Text: "second"})
	delivery := rt.BeginSteerDelivery(context.Background())
	if delivery == nil || len(delivery.Entries()) != 2 {
		t.Fatalf("expected both steers in flight, got %#v", delivery.Entries())
	}
	aborted := 0
	delivery.bindAbort(func() { aborted++ })
	if !rt.RetractSteer(2) {
		t.Fatal("a steer whose call has not begun answering must be retractable")
	}
	if aborted != 1 {
		t.Fatalf("aborts=%d want 1: the call carrying the steer must be abandoned", aborted)
	}
	if got := delivery.Entries(); len(got) != 1 || got[0].Seq != 1 {
		t.Fatalf("delivery still carries %#v, want only the first steer", got)
	}
	if !delivery.Retracted() {
		t.Fatal("the delivery must report the retraction")
	}
	if delivery.Commit() {
		t.Fatal("a delivery a steer was retracted from must not commit")
	}
	if rt.RetractSteer(2) {
		t.Fatal("the same steer was retracted twice")
	}
	if !delivery.Rollback() {
		t.Fatal("rollback must settle the abandoned delivery")
	}
	if got := rt.Snapshot(); len(got) != 1 || got[0].Seq != 1 {
		t.Fatalf("rollback restored %#v, want only the steer that is left", got)
	}
	if delivery.Retracted() {
		t.Fatal("a settled delivery is no longer in flight")
	}
}

// A retraction can land between the tool boundary that took the steer and the
// start of the call that will carry it; binding the call's abort then fires it
// at once, so that call is never made with the steer.
func TestRetractSteerBeforeTheCallStartsAbortsItOnBind(t *testing.T) {
	q := NewInputQueue()
	rt := NewTurnInputRuntime()
	q.Attach(rt)
	q.Steer(Input{Text: "only"})
	delivery := rt.BeginSteerDelivery(context.Background())
	if delivery == nil {
		t.Fatal("expected a delivery")
	}
	if !rt.RetractSteer(1) {
		t.Fatal("expected the in-flight steer to be retractable")
	}
	aborted := 0
	delivery.bindAbort(func() { aborted++ })
	if aborted != 1 {
		t.Fatalf("aborts=%d want 1", aborted)
	}
}

// Once the model is answering a steer it is out of reach.
func TestRetractSteerFailsOnceCommitted(t *testing.T) {
	q := NewInputQueue()
	rt := NewTurnInputRuntime()
	q.Attach(rt)
	q.Steer(Input{Text: "answered"})
	delivery := rt.BeginSteerDelivery(context.Background())
	if delivery == nil || !delivery.Commit() {
		t.Fatal("expected a committed delivery")
	}
	if rt.RetractSteer(1) {
		t.Fatal("a committed steer was retracted")
	}
	if rt.RetractSteer(0) {
		t.Fatal("seq 0 never names a queued steer")
	}
}

// Q5, Q6, Q7: the boundary decides what follows a turn.
func TestInputQueueNextBoundaries(t *testing.T) {
	t.Run("completed drains one batch per turn", func(t *testing.T) {
		q := NewInputQueue()
		rt := NewTurnInputRuntime()
		q.Attach(rt)
		q.FollowUp(Input{Text: "rejected one", Rejected: true})
		q.FollowUp(Input{Text: "rejected two", Rejected: true})
		q.Steer(Input{Text: "undelivered steer"})
		q.FollowUp(Input{Text: "first follow-up"})
		q.FollowUp(Input{Text: "second follow-up"})
		q.Detach()
		var batches []string
		for {
			send, restore := q.Next(BoundaryCompleted)
			if len(restore) != 0 {
				t.Fatalf("a completed boundary restores nothing: %#v", restore)
			}
			if len(send) == 0 {
				break
			}
			texts := make([]string, 0, len(send))
			for _, in := range send {
				texts = append(texts, in.Text)
			}
			batches = append(batches, strings.Join(texts, "|"))
		}
		want := []string{"rejected one|rejected two", "undelivered steer", "first follow-up", "second follow-up"}
		if !reflect.DeepEqual(batches, want) {
			t.Fatalf("batches = %#v, want %#v", batches, want)
		}
	})
	t.Run("interrupt to send flushes the steers", func(t *testing.T) {
		q := NewInputQueue()
		rt := NewTurnInputRuntime()
		q.Attach(rt)
		q.Steer(Input{Text: "flush me"})
		q.FollowUp(Input{Text: "waits instead"})
		send, restore := q.Next(BoundaryInterruptToSend)
		if len(restore) != 0 || len(send) != 1 || send[0].Text != "flush me" {
			t.Fatalf("send = %#v, restore = %#v", send, restore)
		}
		if preview := q.Preview(); len(preview.FollowUp) != 1 {
			t.Fatalf("the follow-up must wait its turn: %#v", preview)
		}
	})
	t.Run("interrupted restores every lane in order", func(t *testing.T) {
		q := NewInputQueue()
		rt := NewTurnInputRuntime()
		q.Attach(rt)
		q.Steer(Input{Text: "a steer"})
		q.FollowUp(Input{Text: "b follow-up"})
		q.FollowUp(Input{Text: "a rejected", Rejected: true})
		send, restore := q.Next(BoundaryInterrupted)
		if len(send) != 0 {
			t.Fatalf("an interrupted boundary sends nothing: %#v", send)
		}
		texts := make([]string, 0, len(restore))
		for _, in := range restore {
			texts = append(texts, in.Text)
		}
		if want := []string{"a rejected", "a steer", "b follow-up"}; !reflect.DeepEqual(texts, want) {
			t.Fatalf("restore order = %#v, want %#v", texts, want)
		}
	})
}

// Q8: TakeAll returns everything in the order it was written, across lanes,
// and leaves nothing queued for the model or the composer.
func TestInputQueueTakeAllWrittenOrder(t *testing.T) {
	q := NewInputQueue()
	rt := NewTurnInputRuntime()
	q.Attach(rt)
	q.FollowUp(Input{Text: "first"})
	q.Steer(Input{Text: "second"})
	q.FollowUp(Input{Text: "third", Rejected: true})
	q.FollowUp(Input{Text: "fourth"})
	all := q.TakeAll()
	texts := make([]string, 0, len(all))
	for _, in := range all {
		texts = append(texts, in.Text)
	}
	if want := []string{"first", "second", "third", "fourth"}; !reflect.DeepEqual(texts, want) {
		t.Fatalf("take-all = %#v, want %#v", texts, want)
	}
	if got := rt.Snapshot(); len(got) != 0 {
		t.Fatalf("steers left in the runtime: %#v", got)
	}
	if preview := q.Preview(); preview.Visible() {
		t.Fatalf("queue not emptied: %#v", preview)
	}
}

// Q10: Discard retracts pending steers from the runtime before clearing, and
// counts what was actually dropped; delivered steers are not counted.
func TestInputQueueDiscardCountsWhatItDrops(t *testing.T) {
	q := NewInputQueue()
	rt := NewTurnInputRuntime()
	q.Attach(rt)
	q.Steer(Input{Text: "steer"})
	q.FollowUp(Input{Text: "rejected", Rejected: true})
	q.FollowUp(Input{Text: "follow-up"})
	if dropped := q.Discard(); dropped != 3 {
		t.Fatalf("dropped = %d, want 3", dropped)
	}
	if got := rt.Snapshot(); len(got) != 0 {
		t.Fatalf("steer left in the runtime: %#v", got)
	}
	if preview := q.Preview(); preview.Visible() {
		t.Fatalf("queue not emptied: %#v", preview)
	}

	// A steer taken for a model call that has not begun answering is still
	// the user's: discarding retracts it from that call and counts it.
	q2 := NewInputQueue()
	rt2 := NewTurnInputRuntime()
	q2.Attach(rt2)
	q2.Steer(Input{Text: "in flight"})
	delivery := rt2.BeginSteerDelivery(context.Background())
	if delivery == nil {
		t.Fatal("expected an in-flight delivery")
	}
	q2.FollowUp(Input{Text: "follow-up"})
	if dropped := q2.Discard(); dropped != 2 {
		t.Fatalf("dropped = %d, want 2: the in-flight steer was never answered", dropped)
	}
	if delivery.Commit() {
		t.Fatal("the call carrying a discarded steer must not commit it")
	}

	// A steer the model is already answering is out of reach and not counted.
	q3 := NewInputQueue()
	rt3 := NewTurnInputRuntime()
	q3.Attach(rt3)
	q3.Steer(Input{Text: "being answered"})
	// Stand in for the instant between the commit and the queue hearing of it.
	rt3.SetChangeHook(nil)
	if d := rt3.BeginSteerDelivery(context.Background()); d == nil || !d.Commit() {
		t.Fatal("expected a committed delivery")
	}
	q3.FollowUp(Input{Text: "follow-up"})
	if dropped := q3.Discard(); dropped != 1 {
		t.Fatalf("a steer the model is answering must not be counted as dropped, got %d", dropped)
	}

	// With the runtime detached the steers never left the queue: all count.
	q4 := NewInputQueue()
	q4.Attach(NewTurnInputRuntime())
	q4.Steer(Input{Text: "never sent"})
	q4.Detach()
	if dropped := q4.Discard(); dropped != 1 {
		t.Fatalf("dropped = %d, want 1: a steer left behind by a detached runtime was never delivered", dropped)
	}
}

// Q11: the preview shows each lane's entry text.
func TestInputQueuePreviewShowsEntryTexts(t *testing.T) {
	q := NewInputQueue()
	q.Attach(NewTurnInputRuntime())
	q.Steer(Input{Text: "steer preview"})
	q.FollowUp(Input{Text: "rejected preview", Rejected: true})
	q.FollowUp(Input{Text: "follow-up preview"})
	got := q.Preview()
	if !reflect.DeepEqual(got.Steers, []string{"steer preview"}) ||
		!reflect.DeepEqual(got.Rejected, []string{"rejected preview"}) ||
		!reflect.DeepEqual(got.FollowUp, []string{"follow-up preview"}) {
		t.Fatalf("preview = %#v", got)
	}
}

// Q3 (engine half): a delivery the runtime commits leaves the steer lane and
// waits on the delivered list for the surface to render, whole.
func TestInputQueueDeliveryReachesDeliveredList(t *testing.T) {
	q := NewInputQueue()
	rt := NewTurnInputRuntime()
	q.Attach(rt)
	q.Steer(Input{Text: "delivered", Payload: "surface record"})
	delivery := rt.BeginSteerDelivery(context.Background())
	if delivery == nil {
		t.Fatal("expected a delivery")
	}
	if !delivery.Commit() {
		t.Fatal("expected the delivery to commit")
	}
	if preview := q.Preview(); preview.Visible() {
		t.Fatalf("delivered steer still previews as pending: %#v", preview)
	}
	delivered := q.TakeDelivered()
	if len(delivered) != 1 || delivered[0].Text != "delivered" || delivered[0].Payload != "surface record" {
		t.Fatalf("delivered = %#v", delivered)
	}
	if again := q.TakeDelivered(); len(again) != 0 {
		t.Fatalf("delivered list must drain: %#v", again)
	}
}
