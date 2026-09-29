package turn

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
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
	// Any turn reaching the session supersedes a continuation still waiting
	// on a usage limit: the conversation has moved on without it.
	s.autoContinue.turnStarting(ctx, req.SessionID)
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
		s.autoContinue.turnEnded(ctx, req, runID, nil)
		out.Status = TurnWaitingApproval
		out.Approval = waiting.Request
		out.Resume = waiting.Resume
		s.publish(ctx, sink, event.RunEvent{RunID: runID, SessionID: req.SessionID, Type: event.RunEventApprovalReq})
		return out, nil
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		s.autoContinue.turnEnded(ctx, req, runID, nil)
		out.Status = TurnCancelled
		out.Error = err
		s.publish(ctx, sink, event.RunEvent{RunID: runID, SessionID: req.SessionID, Type: event.RunEventTurnCancelled})
		return out, err
	case err != nil:
		out.Status = TurnFailed
		out.Error = err
		s.publish(ctx, sink, event.RunEvent{RunID: runID, SessionID: req.SessionID, Type: event.RunEventTurnError})
		// A turn stopped by a spent usage allowance is continued by the
		// runtime once the allowance returns. It is decided here, below every
		// surface, so the terminal and the web schedule, show and cancel the
		// same continuation.
		s.autoContinue.turnEnded(ctx, req, runID, err)
		return out, err
	default:
		s.autoContinue.turnEnded(ctx, req, runID, nil)
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

// Auto-continue: a session stopped by a spent usage allowance resumes by itself
// once the allowance returns.

// AutoContinuePrompt is the user turn the runtime submits when the wait ends.
//
// It is a real turn in the transcript rather than a silent retry of the failed
// request: the failure already persisted whatever the stopped turn produced,
// and a turn of its own is what the reader sees start, what a resume replays,
// and what tells the model why the conversation picks up without a new
// request. It is a constant, provider-neutral and byte-stable, like every other
// runtime text that reaches the model.
const AutoContinuePrompt = "The previous response was interrupted by a usage limit that has since reset. Continue from where you left off."

// Reasons a pending continuation ends without firing, carried on
// auto_continue_cancelled.
const (
	// AutoContinueCancelledByUser is an explicit cancel: Esc, typing into the
	// composer, or the web's cancel control.
	AutoContinueCancelledByUser = "user"
	// AutoContinueSuperseded is a new turn reaching the session first. The
	// conversation has moved on, so continuing the old one would be wrong.
	AutoContinueSuperseded = "superseded"
	// AutoContinueUnavailable is a surface that could not start the turn when
	// the wait ended — it was closed, or it had switched to another session.
	AutoContinueUnavailable = "unavailable"
)

const (
	// defaultAutoContinueGrace is how long after the provider's stated reset
	// the continuation is sent. A clock a few seconds ahead of the provider's
	// would otherwise spend the continuation on a request refused again.
	defaultAutoContinueGrace = 5 * time.Second
	// defaultAutoContinueMinDelay floors the wait. A reset already in the past
	// — a stale stamp, a misread zone — must not turn into a request loop.
	defaultAutoContinueMinDelay = 10 * time.Second
	// defaultAutoContinueMaxAttempts caps continuations in a row that keep
	// being refused. Past it the provider's reset times are not to be trusted,
	// and the error on screen is left for the person to act on.
	defaultAutoContinueMaxAttempts = 5
	// autoContinueCheckInterval bounds each timer slice. The wait is checked
	// against the wall clock rather than trusted to one long timer, whose
	// monotonic clock stops while a laptop sleeps: a machine that wakes after
	// the reset continues within this interval instead of hours late.
	autoContinueCheckInterval = 30 * time.Second
)

// ErrAutoContinueUnavailable is what a surface returns when the wait ended but
// it cannot run the continuation; the engine reports it as a cancellation.
var ErrAutoContinueUnavailable = errors.New("turn: auto-continue unavailable")

// AutoContinuePlan is one scheduled continuation: which session stopped, why,
// and when it picks up again.
type AutoContinuePlan struct {
	SessionID string
	// RunID is the run the usage limit stopped. The lifecycle events are
	// filed under it, since the continuation's own run does not exist yet.
	RunID  string
	Origin Origin
	// Code is the llm explanation code of the failure.
	Code string
	Plan string
	// ResetAt is the provider's stated reset; ContinueAt is when the
	// continuation is submitted, a grace period after it.
	ResetAt    time.Time
	ContinueAt time.Time
	// Attempt counts the continuations in a row this one would be, from 1.
	Attempt int
}

// Payload is the plan as the auto_continue_scheduled event carries it. The
// gateway also sends it when a client binds, so a page opened mid-wait shows
// the same notice a page that saw the event does.
func (p AutoContinuePlan) Payload() event.AutoContinueScheduledPayload {
	out := event.AutoContinueScheduledPayload{
		ContinueAt: p.ContinueAt.UTC().Format(time.RFC3339),
		Code:       p.Code,
		Plan:       p.Plan,
		Attempt:    p.Attempt,
	}
	if !p.ResetAt.IsZero() {
		out.ResetAt = p.ResetAt.UTC().Format(time.RFC3339)
	}
	return out
}

// AutoContinueFunc is a surface's port for running the continuation. It is
// called once the wait ends, off any caller's goroutine, with the prompt to
// submit. The surface runs the turn the way it runs any other — the terminal
// through its own event loop so the turn renders, the gateway as a detached
// run whose events reach every page watching the session — and returns
// ErrAutoContinueUnavailable (or any error) when it cannot.
type AutoContinueFunc func(ctx context.Context, plan AutoContinuePlan, prompt string) error

// AutoContinueConfig installs auto-continue on a Service.
type AutoContinueConfig struct {
	// Continue runs the continuation. Nil disables auto-continue.
	Continue AutoContinueFunc
	// Events receives the auto_continue_* lifecycle. A continuation is armed
	// and fired outside any Submit call, so it cannot use Submit's sink.
	Events EventSink
	// Surfaces limits auto-continue to turns from these surfaces; empty means
	// every surface. A surface with no one to cancel the wait — a channel
	// integration — should not be listed.
	Surfaces []Surface

	// Overridable in tests.
	grace       time.Duration
	minDelay    time.Duration
	maxAttempts int
	now         func() time.Time
	afterFunc   func(time.Duration, func()) func() bool
}

// WithAutoContinue arms a continuation whenever a turn from a listed surface
// fails because a usage allowance is spent and the provider said when it
// returns.
func WithAutoContinue(cfg AutoContinueConfig) Option {
	return func(s *Service) {
		if s == nil || cfg.Continue == nil {
			return
		}
		s.autoContinue = newAutoContinuer(cfg)
	}
}

// SetAutoContinue installs auto-continue after construction, for a surface
// whose continuation port closes over the surface itself (see SetRunExecutor).
func (s *Service) SetAutoContinue(cfg AutoContinueConfig) {
	if s == nil || cfg.Continue == nil {
		return
	}
	s.autoContinue = newAutoContinuer(cfg)
}

// PendingAutoContinue reports the continuation waiting in a session, if any.
func (s *Service) PendingAutoContinue(sessionID string) (AutoContinuePlan, bool) {
	if s == nil || s.autoContinue == nil {
		return AutoContinuePlan{}, false
	}
	return s.autoContinue.pendingPlan(strings.TrimSpace(sessionID))
}

// CancelAutoContinue stops a session's pending continuation and reports
// whether there was one. reason is one of the AutoContinue* reasons.
func (s *Service) CancelAutoContinue(ctx context.Context, sessionID, reason string) bool {
	if s == nil || s.autoContinue == nil {
		return false
	}
	return s.autoContinue.cancel(ctx, strings.TrimSpace(sessionID), reason)
}

// StopAutoContinue drops every pending continuation without firing it. A
// process that is shutting down calls it so no timer outlives the runtime it
// would submit into.
func (s *Service) StopAutoContinue() {
	if s == nil || s.autoContinue == nil {
		return
	}
	s.autoContinue.stop()
}

type autoContinuer struct {
	cfg AutoContinueConfig

	mu      sync.Mutex
	pending map[string]*pendingContinuation
	// fired counts, per session, the continuations fired since a turn last
	// ended in anything but a usage limit. It is what makes Attempt count
	// continuations in a row, and what stops a provider whose reset times are
	// wrong from being asked forever.
	fired  map[string]int
	seq    uint64
	closed bool
}

type pendingContinuation struct {
	plan  AutoContinuePlan
	id    uint64
	timer func() bool
}

func newAutoContinuer(cfg AutoContinueConfig) *autoContinuer {
	if cfg.grace <= 0 {
		cfg.grace = defaultAutoContinueGrace
	}
	if cfg.minDelay <= 0 {
		cfg.minDelay = defaultAutoContinueMinDelay
	}
	if cfg.maxAttempts <= 0 {
		cfg.maxAttempts = defaultAutoContinueMaxAttempts
	}
	if cfg.now == nil {
		// Round(0) strips the monotonic reading, so every comparison below is
		// against the wall clock the provider's reset time is written on.
		cfg.now = func() time.Time { return time.Now().Round(0) }
	}
	if cfg.afterFunc == nil {
		cfg.afterFunc = func(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop }
	}
	return &autoContinuer{
		cfg:     cfg,
		pending: map[string]*pendingContinuation{},
		fired:   map[string]int{},
	}
}

func (a *autoContinuer) accepts(origin Origin) bool {
	if len(a.cfg.Surfaces) == 0 {
		return true
	}
	for _, surface := range a.cfg.Surfaces {
		if surface == origin.Surface {
			return true
		}
	}
	return false
}

// turnStarting is called as a turn is submitted. A pending continuation is
// superseded by any turn that is not the continuation itself: the person moved
// the conversation on, so it must not resume where the failed turn stopped.
// The continuation's own turn finds nothing pending — firing removed it.
func (a *autoContinuer) turnStarting(ctx context.Context, sessionID string) {
	if a == nil {
		return
	}
	if a.cancel(ctx, sessionID, AutoContinueSuperseded) {
		a.mu.Lock()
		delete(a.fired, sessionID)
		a.mu.Unlock()
	}
}

// turnEnded arms a continuation when err is a usage limit with a known reset,
// and otherwise closes the session's run of continuations.
func (a *autoContinuer) turnEnded(ctx context.Context, req TurnRequest, runID string, err error) {
	if a == nil {
		return
	}
	sessionID := strings.TrimSpace(req.SessionID)
	plan, ok := a.planFor(req, runID, err)
	a.mu.Lock()
	if !ok || a.closed {
		delete(a.fired, sessionID)
		a.mu.Unlock()
		return
	}
	plan.Attempt = a.fired[sessionID] + 1
	if plan.Attempt > a.cfg.maxAttempts {
		// Refused again right after every reset: stop here and start the
		// count afresh, so the next failure — necessarily a turn the person
		// sent — is armed as a first attempt.
		delete(a.fired, sessionID)
		a.mu.Unlock()
		return
	}
	if prev := a.pending[sessionID]; prev != nil && prev.timer != nil {
		prev.timer()
	}
	a.seq++
	entry := &pendingContinuation{plan: plan, id: a.seq}
	a.pending[sessionID] = entry
	a.armLocked(entry)
	a.mu.Unlock()
	a.publish(ctx, plan, "scheduled", event.RunEventAutoContinueScheduled, plan.Payload())
}

// planFor decides whether a failed turn gets a continuation, and when.
func (a *autoContinuer) planFor(req TurnRequest, runID string, err error) (AutoContinuePlan, bool) {
	sessionID := strings.TrimSpace(req.SessionID)
	if err == nil || sessionID == "" || strings.TrimSpace(req.ParentRunID) != "" || !a.accepts(req.Origin) {
		return AutoContinuePlan{}, false
	}
	explanation, ok := llm.Explain(err)
	if !ok || explanation.ResetAt.IsZero() {
		return AutoContinuePlan{}, false
	}
	switch explanation.Code {
	case llm.ExplainRateLimitQuota, llm.ExplainRateLimitThrottle:
	default:
		return AutoContinuePlan{}, false
	}
	now := a.cfg.now()
	resetAt := explanation.ResetAt.Round(0)
	continueAt := resetAt.Add(a.cfg.grace)
	if floor := now.Add(a.cfg.minDelay); continueAt.Before(floor) {
		continueAt = floor
	}
	return AutoContinuePlan{
		SessionID:  sessionID,
		RunID:      strings.TrimSpace(runID),
		Origin:     req.Origin,
		Code:       string(explanation.Code),
		Plan:       explanation.Plan,
		ResetAt:    resetAt,
		ContinueAt: continueAt,
	}, true
}

// armLocked starts the next timer slice for entry. Caller holds a.mu.
func (a *autoContinuer) armLocked(entry *pendingContinuation) {
	wait := entry.plan.ContinueAt.Sub(a.cfg.now())
	if wait > autoContinueCheckInterval {
		wait = autoContinueCheckInterval
	}
	if wait < 0 {
		wait = 0
	}
	sessionID, id := entry.plan.SessionID, entry.id
	entry.timer = a.cfg.afterFunc(wait, func() { a.tick(sessionID, id) })
}

// tick runs when a timer slice ends: it re-arms while the wall clock is short
// of the continuation time, and fires once it is not.
func (a *autoContinuer) tick(sessionID string, id uint64) {
	a.mu.Lock()
	entry := a.pending[sessionID]
	if a.closed || entry == nil || entry.id != id {
		a.mu.Unlock()
		return
	}
	if a.cfg.now().Before(entry.plan.ContinueAt) {
		a.armLocked(entry)
		a.mu.Unlock()
		return
	}
	delete(a.pending, sessionID)
	a.fired[sessionID] = entry.plan.Attempt
	a.mu.Unlock()

	ctx := context.Background()
	plan := entry.plan
	a.publish(ctx, plan, "started", event.RunEventAutoContinueStarted, event.AutoContinueStartedPayload{Attempt: plan.Attempt, Prompt: AutoContinuePrompt})
	if err := a.cfg.Continue(ctx, plan, AutoContinuePrompt); err != nil {
		a.mu.Lock()
		delete(a.fired, sessionID)
		a.mu.Unlock()
		payload := event.AutoContinueCancelledPayload{Reason: AutoContinueUnavailable}
		if !errors.Is(err, ErrAutoContinueUnavailable) {
			payload.Error = err.Error()
		}
		a.publish(ctx, plan, "cancelled", event.RunEventAutoContinueCancelled, payload)
	}
}

func (a *autoContinuer) cancel(ctx context.Context, sessionID, reason string) bool {
	if a == nil || sessionID == "" {
		return false
	}
	a.mu.Lock()
	entry := a.pending[sessionID]
	if entry == nil {
		a.mu.Unlock()
		return false
	}
	delete(a.pending, sessionID)
	if entry.timer != nil {
		entry.timer()
	}
	if reason == AutoContinueCancelledByUser {
		// An explicit cancel ends the run of continuations; the next usage
		// limit is a fresh first attempt.
		delete(a.fired, sessionID)
	}
	a.mu.Unlock()
	if strings.TrimSpace(reason) == "" {
		reason = AutoContinueCancelledByUser
	}
	a.publish(ctx, entry.plan, "cancelled", event.RunEventAutoContinueCancelled, event.AutoContinueCancelledPayload{Reason: reason})
	return true
}

func (a *autoContinuer) pendingPlan(sessionID string) (AutoContinuePlan, bool) {
	if a == nil || sessionID == "" {
		return AutoContinuePlan{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	entry := a.pending[sessionID]
	if entry == nil {
		return AutoContinuePlan{}, false
	}
	return entry.plan, true
}

func (a *autoContinuer) stop() {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closed = true
	for id, entry := range a.pending {
		if entry.timer != nil {
			entry.timer()
		}
		delete(a.pending, id)
	}
}

// publish files one lifecycle event under the stopped run. The event id is
// derived from the plan, so a sink that deduplicates by id (the terminal's
// session log does) sees one event per transition of one continuation.
func (a *autoContinuer) publish(ctx context.Context, plan AutoContinuePlan, phase, eventType string, payload any) {
	if a == nil || a.cfg.Events == nil {
		return
	}
	// The lifecycle outlives the turn that triggered it: a failed or
	// superseding turn's context may already be done, and the event must still
	// reach the session log.
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = context.WithoutCancel(ctx)
	id := "autocontinue-" + plan.SessionID + "-" + plan.RunID + "-" +
		strconv.FormatInt(plan.ContinueAt.Unix(), 10) + "-" + strconv.Itoa(plan.Attempt) + "-" + phase
	_ = a.cfg.Events.Publish(ctx, event.NewRunEvent(id, plan.RunID, plan.SessionID, eventType, payload, a.cfg.now()))
}
