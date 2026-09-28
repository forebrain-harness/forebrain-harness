package run

import (
	"context"
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

func TestWithoutTurnInputRuntimeClearsSlot(t *testing.T) {
	rt := NewTurnInputRuntime()
	rt.Enqueue(TurnInputModeSteer, []llm.ContentPart{llm.Text("user steer")})
	ctx := WithTurnInputRuntime(context.Background(), rt)
	if TurnInputRuntimeFromContext(ctx) != rt {
		t.Fatalf("expected runtime present before strip")
	}
	stripped := WithoutTurnInputRuntime(ctx)
	if got := TurnInputRuntimeFromContext(stripped); got != nil {
		t.Fatalf("expected nil runtime after strip, got %#v", got)
	}
	// The original context must still carry the runtime so the primary
	// agent keeps draining steers after the subagent returns.
	if TurnInputRuntimeFromContext(ctx) != rt {
		t.Fatalf("strip must not mutate the parent context")
	}
	// The queued steer must survive the strip — the subagent must not
	// have drained it.
	if !rt.HasSteers() {
		t.Fatalf("expected steer to remain queued after strip")
	}
}
