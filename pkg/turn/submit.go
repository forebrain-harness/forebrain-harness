package turn

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
)

var (
	ErrSessionID     = errors.New("turn: empty session ID")
	ErrNoRunExecutor = errors.New("turn: no run executor")
)

// RunExecutor executes one normalized turn without knowing its surface.
//
// started, when non-nil, must be called with the run's ID as soon as that ID
// exists and before the agent runs. The run ID is assigned by whatever the
// executor persists into, not by Submit: an executor backed by the run store
// only learns it once the row is created. Submit needs it before it publishes
// anything, because every event it emits is keyed by run ID and a consumer
// that saw turn_started under one ID and turn_completed under another would
// treat them as two different runs.
type RunExecutor interface {
	Run(ctx context.Context, req TurnRequest, started func(runID string)) (*agent.Result, error)
}

// ForegroundLocker serializes foreground turns for one session.
type ForegroundLocker interface {
	Lock(context.Context, string) (func(), error)
}

// ApprovalGate reports an unresolved approval for a session.
type ApprovalGate interface {
	Pending(context.Context, string) (*ToolApprovalRequest, error)
}

// WaitingError reports an executor transition to approval wait.
//
// Cause keeps whatever the executor actually raised. An executor translating
// its own gate error into this one would otherwise destroy the execution state
// a surface needs to resume past the gate — the session snapshot in
// tool.RequiresActionError above all, which Request deliberately does not
// carry because Request is what surfaces render, not how a run resumes.
// Preserving it means errors.As still reaches the original.
type WaitingError struct {
	Request *ToolApprovalRequest
	// Resume is the execution state the surface needs to continue past the
	// gate. The executor fills it in, since only it has the snapshot.
	Resume *ApprovalResumeState
	Cause  error
}

func (e *WaitingError) Error() string { return "turn: waiting for approval" }

func (e *WaitingError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// Submit runs the canonical normal-turn path.
func (s *Service) Submit(ctx context.Context, req TurnRequest, sink EventSink) (TurnOutcome, error) {
	if req.SessionID == "" {
		return TurnOutcome{Status: TurnFailed, Error: ErrSessionID}, ErrSessionID
	}
	if s == nil || s.runner == nil {
		return TurnOutcome{SessionID: req.SessionID, Status: TurnFailed, Error: ErrNoRunExecutor}, ErrNoRunExecutor
	}
	if s.approval != nil {
		pending, err := s.approval.Pending(ctx, req.SessionID)
		if err != nil {
			return TurnOutcome{SessionID: req.SessionID, Status: TurnFailed, Error: err}, err
		}
		if pending != nil {
			out := TurnOutcome{SessionID: req.SessionID, RunID: pending.RunID, Status: TurnWaitingApproval, Approval: pending}
			s.publish(ctx, sink, event.RunEvent{RunID: out.RunID, SessionID: req.SessionID, Type: event.RunEventApprovalReq})
			return out, nil
		}
	}
	unlock := func() {}
	if s.locker != nil {
		var err error
		unlock, err = s.locker.Lock(ctx, req.SessionID)
		if err != nil {
			return TurnOutcome{SessionID: req.SessionID, Status: TurnFailed, Error: err}, err
		}
	}
	defer unlock()

	runID := req.ExistingRunID
	startedAt := time.Now()
	// turn_started is published from inside the executor's callback rather than
	// before the call, so it carries the run ID the executor actually persisted
	// under. An executor that failed before it created a run reports none, and
	// the turn has none: an invented ID would name a run no store holds, which
	// every reference to it — an event, a transcript row — is refused for.
	var announced bool
	announce := func(id string) {
		if announced {
			return
		}
		announced = true
		if id = strings.TrimSpace(id); id != "" {
			runID = id
		}
		s.publish(ctx, sink, event.RunEvent{RunID: runID, SessionID: req.SessionID, Type: event.RunEventTurnStarted})
	}
	result, err := s.runner.Run(ctx, req, announce)
	// An executor that failed before it ever had a run ID still owes the
	// caller a turn_started, so the event stream stays well-formed.
	announce("")
	out := TurnOutcome{SessionID: req.SessionID, RunID: runID, Duration: time.Since(startedAt)}
	if result != nil && result.LastResponseUsage != nil {
		out.Usage = *result.LastResponseUsage
	}
	var waiting *WaitingError
	switch {
	case errors.As(err, &waiting):
		out.Status = TurnWaitingApproval
		out.Approval = waiting.Request
		out.Resume = waiting.Resume
		s.publish(ctx, sink, event.RunEvent{RunID: runID, SessionID: req.SessionID, Type: event.RunEventApprovalReq})
		return out, nil
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		out.Status = TurnCancelled
		out.Error = err
		s.publish(ctx, sink, event.RunEvent{RunID: runID, SessionID: req.SessionID, Type: event.RunEventTurnCancelled})
		return out, err
	case err != nil:
		out.Status = TurnFailed
		out.Error = err
		s.publish(ctx, sink, event.RunEvent{RunID: runID, SessionID: req.SessionID, Type: event.RunEventTurnError})
		return out, err
	default:
		out.Status = TurnCompleted
		out.Result = result
		s.publish(ctx, sink, event.RunEvent{RunID: runID, SessionID: req.SessionID, Type: event.RunEventTurnCompleted})
		return out, nil
	}
}

func (s *Service) publish(ctx context.Context, sink EventSink, evt event.RunEvent) {
	if sink != nil {
		_ = sink.Publish(ctx, evt)
	}
}
