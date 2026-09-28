package turn

import (
	"context"
	"errors"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
)

func TestSubmitOutcomes(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want TurnStatus
	}{
		{"completed", nil, TurnCompleted},
		{"cancelled", context.Canceled, TurnCancelled},
		{"failed", errors.New("boom"), TurnFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := New(WithRunExecutor(runFunc(func(_ context.Context, _ TurnRequest, started func(string)) (*agent.Result, error) {
				started("run-from-store")
				return &agent.Result{}, tt.err
			})))
			out, err := svc.Submit(context.Background(), TurnRequest{SessionID: "s1"}, &eventSink{})
			if out.Status != tt.want {
				t.Fatalf("status = %q, want %q", out.Status, tt.want)
			}
			if !errors.Is(err, tt.err) {
				t.Fatalf("error = %v, want %v", err, tt.err)
			}
			// The executor's run ID is the turn's: every event Submit
			// publishes is keyed by it.
			if out.RunID != "run-from-store" {
				t.Fatalf("outcome RunID = %q, want the executor's %q", out.RunID, "run-from-store")
			}
		})
	}
}

func TestSubmitWaitsForApproval(t *testing.T) {
	request := &ToolApprovalRequest{RunID: "r1"}
	svc := New(WithRunExecutor(runFunc(func(context.Context, TurnRequest, func(string)) (*agent.Result, error) {
		t.Fatal("runner called")
		return nil, nil
	})), WithApprovalGate(gateFunc(func(context.Context, string) (*ToolApprovalRequest, error) {
		return request, nil
	})))
	out, err := svc.Submit(context.Background(), TurnRequest{SessionID: "s1"}, nil)
	if err != nil || out.Status != TurnWaitingApproval || out.Approval != request {
		t.Fatalf("outcome = %#v, error = %v", out, err)
	}
}

type runFunc func(context.Context, TurnRequest, func(string)) (*agent.Result, error)

func (f runFunc) Run(ctx context.Context, req TurnRequest, started func(string)) (*agent.Result, error) {
	return f(ctx, req, started)
}

type gateFunc func(context.Context, string) (*ToolApprovalRequest, error)

func (f gateFunc) Pending(ctx context.Context, id string) (*ToolApprovalRequest, error) {
	return f(ctx, id)
}

type eventSink struct{ events []event.RunEvent }

func (s *eventSink) Publish(_ context.Context, evt event.RunEvent) error {
	s.events = append(s.events, evt)
	return nil
}

// An executor that translates its own gate error into WaitingError must not
// destroy it. Request is the render-able projection and deliberately carries
// no session snapshot, so a surface that needs to resume past the gate has to
// be able to reach the original error through the chain.
func TestWaitingErrorPreservesItsCause(t *testing.T) {
	cause := errors.New("tool shell: requires action")
	err := error(&WaitingError{Request: &ToolApprovalRequest{ActionID: "a1"}, Cause: cause})
	if !errors.Is(err, cause) {
		t.Fatal("errors.Is could not reach the cause; the executor's gate error was discarded")
	}
	var waiting *WaitingError
	if !errors.As(err, &waiting) || waiting.Request.ActionID != "a1" {
		t.Fatalf("errors.As did not recover the waiting error: %+v", waiting)
	}
	if (&WaitingError{}).Unwrap() != nil {
		t.Fatal("Unwrap on a causeless WaitingError should be nil, not a panic or a stale value")
	}
}

// TestSubmitInventsNoRunForAnExecutorThatNeverCreatedOne pins that a turn whose
// executor failed before it persisted a run has no run ID. An invented one
// names a run no store holds, so every event and transcript row a surface
// writes under it is refused by the reference to fb_runs, and the failure the
// user should see vanishes with them.
func TestSubmitInventsNoRunForAnExecutorThatNeverCreatedOne(t *testing.T) {
	failure := errors.New("create run: database is locked")
	svc := New(WithRunExecutor(runFunc(func(context.Context, TurnRequest, func(string)) (*agent.Result, error) {
		return nil, failure
	})))
	sink := &eventSink{}
	out, err := svc.Submit(context.Background(), TurnRequest{SessionID: "s1"}, sink)
	if !errors.Is(err, failure) || out.Status != TurnFailed {
		t.Fatalf("outcome = %#v, error = %v; want the executor's failure", out, err)
	}
	if out.RunID != "" {
		t.Fatalf("outcome RunID = %q, want none: no run was created", out.RunID)
	}
	if len(sink.events) != 2 || sink.events[0].Type != event.RunEventTurnStarted || sink.events[1].Type != event.RunEventTurnError {
		t.Fatalf("events = %+v, want turn_started then turn_error", sink.events)
	}
	for _, evt := range sink.events {
		if evt.RunID != "" || evt.SessionID != "s1" {
			t.Fatalf("event %s carries run %q session %q, want no run in s1", evt.Type, evt.RunID, evt.SessionID)
		}
	}
}
