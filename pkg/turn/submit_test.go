package turn

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
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

// fakeClock drives the auto-continue timers without waiting for wall time.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	at      time.Time
	f       func()
	stopped bool
	fired   bool
}

func newFakeClock(now time.Time) *fakeClock { return &fakeClock{now: now} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) AfterFunc(d time.Duration, f func()) func() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := &fakeTimer{at: c.now.Add(d), f: f}
	c.timers = append(c.timers, timer)
	return func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		was := !timer.stopped && !timer.fired
		timer.stopped = true
		return was
	}
}

// Advance moves the clock and runs every timer that comes due, including the
// ones a firing timer arms, in time order.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
	c.runDue()
}

// Jump moves the wall clock without letting intermediate timer slices run
// first, the way a machine that slept through a wait wakes up.
func (c *fakeClock) Jump(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// FireNext runs the earliest live timer whatever its time, the way a timer
// whose monotonic clock paused during sleep fires once the machine wakes.
func (c *fakeClock) FireNext() bool {
	c.mu.Lock()
	var next *fakeTimer
	for _, timer := range c.timers {
		if timer.stopped || timer.fired {
			continue
		}
		if next == nil || timer.at.Before(next.at) {
			next = timer
		}
	}
	if next == nil {
		c.mu.Unlock()
		return false
	}
	next.fired = true
	c.mu.Unlock()
	next.f()
	return true
}

func (c *fakeClock) runDue() {
	for {
		c.mu.Lock()
		due := make([]*fakeTimer, 0)
		for _, timer := range c.timers {
			if !timer.stopped && !timer.fired && !timer.at.After(c.now) {
				due = append(due, timer)
			}
		}
		sort.Slice(due, func(i, j int) bool { return due[i].at.Before(due[j].at) })
		if len(due) == 0 {
			c.mu.Unlock()
			return
		}
		due[0].fired = true
		c.mu.Unlock()
		due[0].f()
	}
}

type recordingSink struct {
	mu     sync.Mutex
	events []event.RunEvent
}

func (s *recordingSink) Publish(_ context.Context, evt event.RunEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, evt)
	return nil
}

func (s *recordingSink) types() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.events))
	for _, evt := range s.events {
		out = append(out, evt.Type)
	}
	return out
}

func (s *recordingSink) last(t *testing.T, eventType string, into any) event.RunEvent {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.events) - 1; i >= 0; i-- {
		if s.events[i].Type == eventType {
			if into != nil {
				if err := json.Unmarshal(s.events[i].Payload, into); err != nil {
					t.Fatalf("decode %s payload: %v", eventType, err)
				}
			}
			return s.events[i]
		}
	}
	t.Fatalf("no %s event in %v", eventType, s.events)
	return event.RunEvent{}
}

type continuation struct {
	plan   AutoContinuePlan
	prompt string
}

type autoContinueHarness struct {
	clock     *fakeClock
	sink      *recordingSink
	svc       *Service
	mu        sync.Mutex
	continued []continuation
	runErr    error
	runID     string
	contErr   error
}

func newAutoContinueHarness(t *testing.T, surfaces ...Surface) *autoContinueHarness {
	t.Helper()
	h := &autoContinueHarness{
		clock: newFakeClock(time.Date(2026, 9, 29, 23, 0, 0, 0, time.UTC)),
		sink:  &recordingSink{},
		runID: "run-1",
	}
	cfg := AutoContinueConfig{
		Events:   h.sink,
		Surfaces: surfaces,
		Continue: func(_ context.Context, plan AutoContinuePlan, prompt string) error {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.continued = append(h.continued, continuation{plan: plan, prompt: prompt})
			return h.contErr
		},
		now:       h.clock.Now,
		afterFunc: h.clock.AfterFunc,
	}
	h.svc = New(
		WithRunExecutor(runFunc(func(_ context.Context, _ TurnRequest, started func(string)) (*agent.Result, error) {
			h.mu.Lock()
			err, id := h.runErr, h.runID
			h.mu.Unlock()
			started(id)
			return &agent.Result{}, err
		})),
		WithAutoContinue(cfg),
	)
	return h
}

func (h *autoContinueHarness) submit(t *testing.T, err error, origin Surface) {
	t.Helper()
	h.mu.Lock()
	h.runErr = err
	h.mu.Unlock()
	_, _ = h.svc.Submit(context.Background(), TurnRequest{SessionID: "s1", Origin: Origin{Surface: origin}}, nil)
}

func (h *autoContinueHarness) continuations() []continuation {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]continuation(nil), h.continued...)
}

func quotaError(resetAt time.Time, plan string) error {
	return &llm.MaxAttemptsExceededError{Attempts: 1, Err: &llm.APIError{
		StatusCode: 429,
		RateLimit:  &llm.RateLimit{Quota: true, Plan: plan, Reason: "usage_limit_reached", ResetAt: resetAt},
	}}
}

func TestAutoContinueFiresAfterTheReset(t *testing.T) {
	h := newAutoContinueHarness(t)
	resetAt := h.clock.Now().Add(3*time.Hour + 10*time.Minute)
	h.submit(t, quotaError(resetAt, "pro"), SurfaceTUI)

	plan, ok := h.svc.PendingAutoContinue("s1")
	if !ok {
		t.Fatal("no continuation pending after a usage-limit failure")
	}
	wantAt := resetAt.Add(defaultAutoContinueGrace)
	if !plan.ContinueAt.Equal(wantAt) || !plan.ResetAt.Equal(resetAt) {
		t.Fatalf("plan times = continue %v reset %v, want continue %v reset %v", plan.ContinueAt, plan.ResetAt, wantAt, resetAt)
	}
	if plan.RunID != "run-1" || plan.Attempt != 1 || plan.Plan != "pro" || plan.Code != string(llm.ExplainRateLimitQuota) {
		t.Fatalf("plan = %+v", plan)
	}
	var scheduled event.AutoContinueScheduledPayload
	evt := h.sink.last(t, event.RunEventAutoContinueScheduled, &scheduled)
	if evt.RunID != "run-1" || evt.SessionID != "s1" {
		t.Fatalf("scheduled event filed under run %q session %q", evt.RunID, evt.SessionID)
	}
	if scheduled.ContinueAt != wantAt.UTC().Format(time.RFC3339) || scheduled.Plan != "pro" || scheduled.Attempt != 1 {
		t.Fatalf("scheduled payload = %+v", scheduled)
	}

	h.clock.Advance(3*time.Hour + 10*time.Minute)
	if got := h.continuations(); len(got) != 0 {
		t.Fatalf("continued before the grace period ended: %+v", got)
	}
	h.clock.Advance(defaultAutoContinueGrace)
	got := h.continuations()
	if len(got) != 1 {
		t.Fatalf("continuations = %d, want 1", len(got))
	}
	if got[0].prompt != AutoContinuePrompt || got[0].plan.SessionID != "s1" || got[0].plan.Origin.Surface != SurfaceTUI {
		t.Fatalf("continuation = %+v", got[0])
	}
	if _, ok := h.svc.PendingAutoContinue("s1"); ok {
		t.Fatal("continuation still pending after it fired")
	}
	var started event.AutoContinueStartedPayload
	h.sink.last(t, event.RunEventAutoContinueStarted, &started)
	if started.Prompt != AutoContinuePrompt || started.Attempt != 1 {
		t.Fatalf("started payload = %+v", started)
	}
}

func TestAutoContinueIgnoresFailuresWithoutAReset(t *testing.T) {
	for name, err := range map[string]error{
		"plain error":         errors.New("boom"),
		"throttle, no reset":  &llm.APIError{StatusCode: 429, RateLimit: &llm.RateLimit{}},
		"server error":        &llm.APIError{StatusCode: 500},
		"credentials refused": &llm.APIError{StatusCode: 401},
	} {
		t.Run(name, func(t *testing.T) {
			h := newAutoContinueHarness(t)
			h.submit(t, err, SurfaceTUI)
			if _, ok := h.svc.PendingAutoContinue("s1"); ok {
				t.Fatal("armed a continuation for a failure with no reset time")
			}
			if len(h.sink.types()) != 0 {
				t.Fatalf("published %v", h.sink.types())
			}
		})
	}
}

func TestAutoContinueArmsAThrottleThatNamedItsWait(t *testing.T) {
	h := newAutoContinueHarness(t)
	h.submit(t, &llm.APIError{StatusCode: 429, RateLimit: &llm.RateLimit{ResetAt: h.clock.Now().Add(2 * time.Minute)}}, SurfaceTUI)
	plan, ok := h.svc.PendingAutoContinue("s1")
	if !ok || plan.Code != string(llm.ExplainRateLimitThrottle) {
		t.Fatalf("plan = %+v, pending %v", plan, ok)
	}
}

func TestAutoContinueOnlyForListedSurfaces(t *testing.T) {
	h := newAutoContinueHarness(t, SurfaceWebChat)
	h.submit(t, quotaError(h.clock.Now().Add(time.Hour), ""), SurfaceChannel)
	if _, ok := h.svc.PendingAutoContinue("s1"); ok {
		t.Fatal("armed a continuation for a surface nobody can cancel it from")
	}
	h.submit(t, quotaError(h.clock.Now().Add(time.Hour), ""), SurfaceWebChat)
	if _, ok := h.svc.PendingAutoContinue("s1"); !ok {
		t.Fatal("did not arm for a listed surface")
	}
}

func TestAutoContinueSkipsChildRuns(t *testing.T) {
	h := newAutoContinueHarness(t)
	h.mu.Lock()
	h.runErr = quotaError(h.clock.Now().Add(time.Hour), "")
	h.mu.Unlock()
	_, _ = h.svc.Submit(context.Background(), TurnRequest{SessionID: "s1", ParentRunID: "parent"}, nil)
	if _, ok := h.svc.PendingAutoContinue("s1"); ok {
		t.Fatal("a child run armed its own continuation")
	}
}

func TestAutoContinueCancelledByUser(t *testing.T) {
	h := newAutoContinueHarness(t)
	h.submit(t, quotaError(h.clock.Now().Add(time.Hour), ""), SurfaceTUI)
	if !h.svc.CancelAutoContinue(context.Background(), "s1", AutoContinueCancelledByUser) {
		t.Fatal("cancel reported nothing pending")
	}
	if h.svc.CancelAutoContinue(context.Background(), "s1", AutoContinueCancelledByUser) {
		t.Fatal("a second cancel found the continuation again")
	}
	var cancelled event.AutoContinueCancelledPayload
	h.sink.last(t, event.RunEventAutoContinueCancelled, &cancelled)
	if cancelled.Reason != AutoContinueCancelledByUser {
		t.Fatalf("reason = %q", cancelled.Reason)
	}
	h.clock.Advance(2 * time.Hour)
	if got := h.continuations(); len(got) != 0 {
		t.Fatalf("a cancelled continuation fired: %+v", got)
	}
}

func TestAutoContinueSupersededByANewTurn(t *testing.T) {
	h := newAutoContinueHarness(t)
	h.submit(t, quotaError(h.clock.Now().Add(time.Hour), ""), SurfaceTUI)
	h.submit(t, nil, SurfaceTUI)
	var cancelled event.AutoContinueCancelledPayload
	h.sink.last(t, event.RunEventAutoContinueCancelled, &cancelled)
	if cancelled.Reason != AutoContinueSuperseded {
		t.Fatalf("reason = %q, want superseded", cancelled.Reason)
	}
	h.clock.Advance(2 * time.Hour)
	if got := h.continuations(); len(got) != 0 {
		t.Fatalf("a superseded continuation fired: %+v", got)
	}
}

func TestAutoContinueCountsAttemptsInARowAndStops(t *testing.T) {
	h := newAutoContinueHarness(t)
	for attempt := 1; attempt <= defaultAutoContinueMaxAttempts; attempt++ {
		h.submit(t, quotaError(h.clock.Now().Add(time.Hour), ""), SurfaceTUI)
		plan, ok := h.svc.PendingAutoContinue("s1")
		if !ok || plan.Attempt != attempt {
			t.Fatalf("attempt %d: plan = %+v, pending %v", attempt, plan, ok)
		}
		h.clock.Advance(time.Hour + defaultAutoContinueGrace)
		// The fired continuation is the next turn, and it is refused again.
	}
	h.submit(t, quotaError(h.clock.Now().Add(time.Hour), ""), SurfaceTUI)
	if _, ok := h.svc.PendingAutoContinue("s1"); ok {
		t.Fatal("kept continuing past the attempt cap")
	}
	// The cap reset the count: the person's next turn, refused again, is a
	// fresh first attempt.
	h.submit(t, quotaError(h.clock.Now().Add(time.Hour), ""), SurfaceTUI)
	if plan, ok := h.svc.PendingAutoContinue("s1"); !ok || plan.Attempt != 1 {
		t.Fatalf("after the cap: plan = %+v, pending %v", plan, ok)
	}
}

func TestAutoContinueSuccessResetsTheCount(t *testing.T) {
	h := newAutoContinueHarness(t)
	h.submit(t, quotaError(h.clock.Now().Add(time.Hour), ""), SurfaceTUI)
	h.clock.Advance(time.Hour + defaultAutoContinueGrace)
	h.submit(t, nil, SurfaceTUI)
	h.submit(t, quotaError(h.clock.Now().Add(time.Hour), ""), SurfaceTUI)
	if plan, ok := h.svc.PendingAutoContinue("s1"); !ok || plan.Attempt != 1 {
		t.Fatalf("plan = %+v, pending %v", plan, ok)
	}
}

func TestAutoContinueFloorsAResetInThePast(t *testing.T) {
	h := newAutoContinueHarness(t)
	h.submit(t, quotaError(h.clock.Now().Add(-time.Hour), ""), SurfaceTUI)
	plan, ok := h.svc.PendingAutoContinue("s1")
	if !ok {
		t.Fatal("not armed")
	}
	if want := h.clock.Now().Add(defaultAutoContinueMinDelay); !plan.ContinueAt.Equal(want) {
		t.Fatalf("continue at %v, want the floor %v", plan.ContinueAt, want)
	}
}

func TestAutoContinueFiresOnWakeAfterSleep(t *testing.T) {
	h := newAutoContinueHarness(t)
	h.submit(t, quotaError(h.clock.Now().Add(5*time.Hour), ""), SurfaceTUI)
	// The machine sleeps through the reset: the wall clock moves on while the
	// pending timer slice has not run. The first slice to run after waking
	// sees the wall clock and fires rather than waiting out the full wait.
	h.clock.Jump(6 * time.Hour)
	if !h.clock.FireNext() {
		t.Fatal("no timer armed")
	}
	if got := h.continuations(); len(got) != 1 {
		t.Fatalf("continuations after waking = %d, want 1", len(got))
	}
}

func TestAutoContinueReportsASurfaceThatCannotContinue(t *testing.T) {
	h := newAutoContinueHarness(t)
	h.contErr = ErrAutoContinueUnavailable
	h.submit(t, quotaError(h.clock.Now().Add(time.Minute), ""), SurfaceTUI)
	h.clock.Advance(time.Hour)
	var cancelled event.AutoContinueCancelledPayload
	h.sink.last(t, event.RunEventAutoContinueCancelled, &cancelled)
	if cancelled.Reason != AutoContinueUnavailable || cancelled.Error != "" {
		t.Fatalf("cancelled = %+v", cancelled)
	}
	want := []string{event.RunEventAutoContinueScheduled, event.RunEventAutoContinueStarted, event.RunEventAutoContinueCancelled}
	if got := h.sink.types(); len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("events = %v, want %v", got, want)
	}
}

func TestStopAutoContinueDropsPendingWaits(t *testing.T) {
	h := newAutoContinueHarness(t)
	h.submit(t, quotaError(h.clock.Now().Add(time.Minute), ""), SurfaceTUI)
	h.svc.StopAutoContinue()
	h.clock.Advance(time.Hour)
	if got := h.continuations(); len(got) != 0 {
		t.Fatalf("a stopped service continued: %+v", got)
	}
	h.submit(t, quotaError(h.clock.Now().Add(time.Minute), ""), SurfaceTUI)
	if _, ok := h.svc.PendingAutoContinue("s1"); ok {
		t.Fatal("a stopped service armed a new continuation")
	}
}

func TestAutoContinueDisabledWithoutAPort(t *testing.T) {
	svc := New(
		WithRunExecutor(runFunc(func(context.Context, TurnRequest, func(string)) (*agent.Result, error) {
			return nil, quotaError(time.Now().Add(time.Hour), "")
		})),
		WithAutoContinue(AutoContinueConfig{}),
	)
	_, _ = svc.Submit(context.Background(), TurnRequest{SessionID: "s1"}, nil)
	if _, ok := svc.PendingAutoContinue("s1"); ok {
		t.Fatal("armed with no surface to continue through")
	}
	if svc.CancelAutoContinue(context.Background(), "s1", AutoContinueCancelledByUser) {
		t.Fatal("cancel reported a continuation that cannot exist")
	}
}
