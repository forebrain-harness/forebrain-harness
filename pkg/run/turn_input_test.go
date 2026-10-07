package run

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

func TestRetractLastSteerRemovesMostRecentSteer(t *testing.T) {
	rt := NewTurnInputRuntime()
	rt.Enqueue(TurnInputModeSteer, []llm.ContentPart{llm.Text("first steer")})
	rt.Enqueue(TurnInputModeFollowUp, []llm.ContentPart{llm.Text("a follow up")})
	rt.Enqueue(TurnInputModeSteer, []llm.ContentPart{llm.Text("second steer")})

	parts, ok := rt.RetractLastSteer()
	if !ok {
		t.Fatalf("expected a steer to retract")
	}
	if got := llm.TextContent(parts...); got != "second steer" {
		t.Fatalf("expected most recent steer retracted, got %q", got)
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

func TestRetractLastSteerReturnsFalseWhenNoSteers(t *testing.T) {
	rt := NewTurnInputRuntime()
	rt.Enqueue(TurnInputModeFollowUp, []llm.ContentPart{llm.Text("just a follow up")})

	if _, ok := rt.RetractLastSteer(); ok {
		t.Fatalf("expected no steer to retract when only follow-ups are queued")
	}
	if remaining := rt.Snapshot(); len(remaining) != 1 {
		t.Fatalf("expected follow-up untouched, got %#v", remaining)
	}

	var nilRT *TurnInputRuntime
	if _, ok := nilRT.RetractLastSteer(); ok {
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

	if _, ok := rt.RetractLastSteer(); ok {
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
// lane it landed in. A steer the runtime can no longer retract is skipped —
// and because the runtime drains FIFO, so is the whole steer lane.
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
	t.Run("unretractable steer is skipped", func(t *testing.T) {
		q := NewInputQueue()
		rt := NewTurnInputRuntime()
		q.Attach(rt)
		q.FollowUp(Input{Text: "older follow-up"})
		q.Steer(Input{Text: "in flight"})
		// The runtime drained the steer for a model call that has not
		// committed yet: retracting finds nothing, so recall must not hand
		// back a message the model is about to answer.
		if delivery := rt.BeginSteerDelivery(context.Background()); delivery == nil {
			t.Fatal("expected an in-flight delivery")
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

	q2 := NewInputQueue()
	rt2 := NewTurnInputRuntime()
	q2.Attach(rt2)
	q2.Steer(Input{Text: "in flight"})
	if delivery := rt2.BeginSteerDelivery(context.Background()); delivery == nil {
		t.Fatal("expected an in-flight delivery")
	}
	q2.FollowUp(Input{Text: "follow-up"})
	if dropped := q2.Discard(); dropped != 1 {
		t.Fatalf("an undeliverable steer must not be counted as dropped, got %d", dropped)
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
