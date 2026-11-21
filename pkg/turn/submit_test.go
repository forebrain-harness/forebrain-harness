package turn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/telemetry"
	"github.com/google/uuid"
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

// firstTokenCall is one StampRunFirstToken the engine made, with the context it
// saw — recorded so a test can prove the stamp outlived the run's own
// cancellation.
type firstTokenCall struct {
	runID  string
	wait   time.Duration
	ctxErr error
}

type fakeFirstTokenStore struct {
	mu    sync.Mutex
	calls []firstTokenCall
}

func (f *fakeFirstTokenStore) StampRunFirstToken(ctx context.Context, runID string, wait time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, firstTokenCall{runID: runID, wait: wait, ctxErr: ctx.Err()})
	return nil
}

func (f *fakeFirstTokenStore) recorded() []firstTokenCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]firstTokenCall(nil), f.calls...)
}

// TestSubmitStampsTheFirstTokenOncePerRun pins the owner's rule that the wait
// is one value per run: the model answering the turn a second time (a tool
// round-trip) does not measure again, and the store is handed the run the
// executor actually persisted under.
func TestSubmitStampsTheFirstTokenOncePerRun(t *testing.T) {
	store := &fakeFirstTokenStore{}
	var gotRunID string
	var gotWait time.Duration
	svc := New(
		WithFirstTokenStore(store),
		WithRunExecutor(runFunc(func(ctx context.Context, _ TurnRequest, started func(string)) (*agent.Result, error) {
			started("run-1")
			sink := llm.StreamSinkFrom(ctx)
			if sink == nil {
				t.Fatal("Submit must install a stream sink for the engine to observe")
			}
			sink.OnResponseStarted() // the first output of this turn
			sink.OnResponseStarted() // a later block: must not be measured again
			return &agent.Result{}, nil
		})),
	)
	out, err := svc.Submit(llm.WithStreamSink(context.Background(), &llm.StreamSink{}), TurnRequest{
		SessionID: "s1",
		OnFirstToken: func(runID string, wait time.Duration) {
			gotRunID, gotWait = runID, wait
		},
	}, &eventSink{})
	if err != nil || out.Status != TurnCompleted {
		t.Fatalf("outcome = %#v, error = %v", out, err)
	}
	calls := store.recorded()
	if len(calls) != 1 {
		t.Fatalf("store received %d stamps, want exactly one per run: %+v", len(calls), calls)
	}
	if calls[0].runID != "run-1" {
		t.Fatalf("stamped run = %q, want the executor's run-1", calls[0].runID)
	}
	if gotRunID != "run-1" || gotWait != calls[0].wait {
		t.Fatalf("OnFirstToken = (%q, %v), want the same as the stamp %+v", gotRunID, gotWait, calls[0])
	}
}

// TestSubmitFirstTokenOutlivesACancelledTurn pins what a cancelled turn keeps:
// the wait is written through context.WithoutCancel, so a turn the user
// stopped after the model began answering still records the wait it measured.
func TestSubmitFirstTokenOutlivesACancelledTurn(t *testing.T) {
	store := &fakeFirstTokenStore{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc := New(
		WithFirstTokenStore(store),
		WithRunExecutor(runFunc(func(runCtx context.Context, _ TurnRequest, started func(string)) (*agent.Result, error) {
			started("run-cancelled")
			sink := llm.StreamSinkFrom(runCtx)
			if sink == nil {
				t.Fatal("Submit must install a stream sink for the engine to observe")
			}
			sink.OnResponseStarted() // the model began answering
			cancel()                 // then the user stopped the turn
			return nil, context.Canceled
		})),
	)
	out, err := svc.Submit(llm.WithStreamSink(ctx, &llm.StreamSink{}), TurnRequest{SessionID: "s1"}, &eventSink{})
	if out.Status != TurnCancelled || !errors.Is(err, context.Canceled) {
		t.Fatalf("outcome = %#v, error = %v, want a cancelled turn", out, err)
	}
	calls := store.recorded()
	if len(calls) != 1 {
		t.Fatalf("a measured cancelled turn recorded %d stamps, want one: %+v", len(calls), calls)
	}
	if calls[0].ctxErr != nil {
		t.Fatalf("the stamp was handed a cancelled context (%v); it must outlive the run", calls[0].ctxErr)
	}
}

// TestSubmitMeasuresNothingWithoutAStreamSink pins that the engine does not
// invent a measurement: a turn that runs with no streaming sink has no first
// output to observe, so neither the store nor the surface callback fires.
func TestSubmitMeasuresNothingWithoutAStreamSink(t *testing.T) {
	store := &fakeFirstTokenStore{}
	callbackFired := false
	svc := New(
		WithFirstTokenStore(store),
		WithRunExecutor(runFunc(func(ctx context.Context, _ TurnRequest, started func(string)) (*agent.Result, error) {
			started("run-2")
			if llm.StreamSinkFrom(ctx) != nil {
				t.Fatal("no sink was installed; the engine must not add one")
			}
			return &agent.Result{}, nil
		})),
	)
	out, err := svc.Submit(context.Background(), TurnRequest{
		SessionID:    "s1",
		OnFirstToken: func(string, time.Duration) { callbackFired = true },
	}, &eventSink{})
	if err != nil || out.Status != TurnCompleted {
		t.Fatalf("outcome = %#v, error = %v", out, err)
	}
	if calls := store.recorded(); len(calls) != 0 {
		t.Fatalf("store received %+v, want nothing without a sink", calls)
	}
	if callbackFired {
		t.Fatal("OnFirstToken fired with no sink to observe")
	}
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

// TestAutoContinueSkipsUnattendedTurns pins the heartbeat's rule: a turn
// nobody is watching ends for good on a usage limit. The same surface, with
// a person behind it, arms exactly as before.
func TestAutoContinueSkipsUnattendedTurns(t *testing.T) {
	h := newAutoContinueHarness(t, SurfaceWebChat)
	h.mu.Lock()
	h.runErr = quotaError(h.clock.Now().Add(time.Hour), "")
	h.mu.Unlock()
	_, _ = h.svc.Submit(context.Background(), TurnRequest{
		SessionID: "s1", Origin: Origin{Surface: SurfaceWebChat}, Unattended: true,
	}, nil)
	if _, ok := h.svc.PendingAutoContinue("s1"); ok {
		t.Fatal("an unattended turn armed a continuation")
	}
	for _, typ := range h.sink.types() {
		if typ == event.RunEventAutoContinueScheduled {
			t.Fatal("an unattended turn published a continuation schedule")
		}
	}
	h.clock.Advance(2 * time.Hour)
	if got := h.continuations(); len(got) != 0 {
		t.Fatalf("an unattended turn's continuation fired: %+v", got)
	}

	// The same surface, attended, arms as before.
	h.submit(t, quotaError(h.clock.Now().Add(time.Hour), ""), SurfaceWebChat)
	if plan, ok := h.svc.PendingAutoContinue("s1"); !ok || plan.Attempt != 1 {
		t.Fatalf("attended plan = %+v, pending %v; want a first attempt", plan, ok)
	}
}

// TestAutoContinueUnattendedEndingsWithoutAUsageLimitKeepTheirBookkeeping
// pins the guard to the usage-limit arming alone: an unattended turn that
// ends any other way closes the session's run of continuations exactly as
// an attended one does, so the next refusal is a fresh first attempt.
func TestAutoContinueUnattendedEndingsWithoutAUsageLimitKeepTheirBookkeeping(t *testing.T) {
	h := newAutoContinueHarness(t)
	h.submit(t, quotaError(h.clock.Now().Add(time.Hour), ""), SurfaceWebChat)
	h.clock.Advance(time.Hour + defaultAutoContinueGrace)
	if got := h.continuations(); len(got) != 1 {
		t.Fatalf("continuations = %d, want the first one", len(got))
	}
	h.mu.Lock()
	h.runErr = nil
	h.mu.Unlock()
	_, _ = h.svc.Submit(context.Background(), TurnRequest{
		SessionID: "s1", Origin: Origin{Surface: SurfaceWebChat}, Unattended: true,
	}, nil)
	h.submit(t, quotaError(h.clock.Now().Add(time.Hour), ""), SurfaceWebChat)
	if plan, ok := h.svc.PendingAutoContinue("s1"); !ok || plan.Attempt != 1 {
		t.Fatalf("plan = %+v, pending %v; want a fresh first attempt after an unattended ending", plan, ok)
	}
}

// endSubagent reports one finished execution of the harness conversation's
// subagent to the scheduler.
func (h *autoContinueHarness) endSubagent(err error, worker, agentKey string) {
	h.svc.SubagentExecutionEnded(context.Background(), SubagentExecutionEnd{
		ConversationSessionID: "s1",
		WorkerSessionID:       worker,
		AgentKey:              agentKey,
		RunID:                 "subrun-" + worker,
		Origin:                Origin{Surface: SurfaceTUI},
		Err:                   err,
	})
}

// The primary conversation's own continuation behaves exactly as it does when
// no subagent is waiting: a subagent's continuation is a separate plan that
// neither arms nor cancels the conversation's.
func TestPrimaryAutoContinueIsUnchangedBySubagentPlans(t *testing.T) {
	h := newAutoContinueHarness(t)
	resetAt := h.clock.Now().Add(3 * time.Hour)
	h.submit(t, quotaError(resetAt, ""), SurfaceTUI)
	h.endSubagent(quotaError(resetAt, ""), "worker-a", "task-a")

	plan, ok := h.svc.PendingAutoContinue("s1")
	if !ok || plan.Attempt != 1 || plan.AgentKey != "" {
		t.Fatalf("primary plan = %+v, pending %v", plan, ok)
	}
	if want := resetAt.Add(defaultAutoContinueGrace); !plan.ContinueAt.Equal(want) {
		t.Fatalf("primary continue at %v, want %v", plan.ContinueAt, want)
	}
	if plans := h.svc.PendingAutoContinuePlans("s1"); len(plans) != 2 || plans[0].AgentKey != "" || plans[1].AgentKey != "task-a" {
		t.Fatalf("pending plans = %+v", plans)
	}
	if !h.svc.CancelAutoContinue(context.Background(), "s1", AutoContinueCancelledByUser) {
		t.Fatal("primary cancel reported nothing pending")
	}
	if _, ok := h.svc.PendingAutoContinue("s1"); ok {
		t.Fatal("primary still pending after its cancel")
	}
	if _, ok := h.svc.PendingAutoContinue("worker-a"); !ok {
		t.Fatal("cancelling the primary dropped the subagent's continuation")
	}
	h.clock.Advance(3*time.Hour + defaultAutoContinueGrace)
	got := h.continuations()
	if len(got) != 1 || got[0].plan.AgentKey != "task-a" {
		t.Fatalf("continuations = %+v, want only the subagent's", got)
	}
}

func TestSubagentUsageLimitArmsItsOwnContinuation(t *testing.T) {
	h := newAutoContinueHarness(t)
	resetAt := h.clock.Now().Add(3 * time.Hour)
	h.endSubagent(quotaError(resetAt, "pro"), "worker-a", "task-a")

	plan, ok := h.svc.PendingAutoContinue("worker-a")
	if !ok {
		t.Fatal("subagent continuation not armed")
	}
	if plan.SessionID != "s1" || plan.AgentKey != "task-a" || plan.WorkerSessionID != "worker-a" || plan.Attempt != 1 {
		t.Fatalf("plan identity = %+v", plan)
	}
	if want := resetAt.Add(defaultAutoContinueGrace); !plan.ContinueAt.Equal(want) {
		t.Fatalf("continue at %v, want %v", plan.ContinueAt, want)
	}
	if _, ok := h.svc.PendingAutoContinue("s1"); ok {
		t.Fatal("the conversation armed its own continuation for a subagent's failure")
	}
	var scheduled event.AutoContinueScheduledPayload
	evt := h.sink.last(t, event.RunEventAutoContinueScheduled, &scheduled)
	if evt.SessionID != "s1" || scheduled.AgentID != "task-a" {
		t.Fatalf("scheduled event filed session %q agent %q", evt.SessionID, scheduled.AgentID)
	}
	h.clock.Advance(3*time.Hour + defaultAutoContinueGrace)
	got := h.continuations()
	if len(got) != 1 || got[0].plan.AgentKey != "task-a" || got[0].plan.SessionID != "s1" || got[0].prompt != AutoContinuePrompt {
		t.Fatalf("continuations = %+v", got)
	}
	var started event.AutoContinueStartedPayload
	h.sink.last(t, event.RunEventAutoContinueStarted, &started)
	if started.AgentID != "task-a" {
		t.Fatalf("started payload = %+v", started)
	}
}

// An execution its dispatching agent awaited reports its failure to that
// agent, which continues the subagent itself: the scheduler arms nothing for
// the subagent, and the conversation's own plans are untouched.
func TestAwaitedSubagentUsageLimitArmsNoContinuation(t *testing.T) {
	h := newAutoContinueHarness(t)
	resetAt := h.clock.Now().Add(3 * time.Hour)
	h.svc.SubagentExecutionEnded(context.Background(), SubagentExecutionEnd{
		ConversationSessionID: "s1",
		WorkerSessionID:       "worker-a",
		AgentKey:              "task-a",
		RunID:                 "subrun-worker-a",
		Origin:                Origin{Surface: SurfaceTUI},
		Err:                   quotaError(resetAt, "pro"),
		AwaitedByDispatcher:   true,
	})
	if plan, ok := h.svc.PendingAutoContinue("worker-a"); ok {
		t.Fatalf("an awaited subagent armed its own continuation: %+v", plan)
	}
	if plans := h.svc.PendingAutoContinuePlans("s1"); len(plans) != 0 {
		t.Fatalf("pending plans = %+v, want none", plans)
	}
	h.clock.Advance(3*time.Hour + defaultAutoContinueGrace)
	if got := h.continuations(); len(got) != 0 {
		t.Fatalf("continuations = %+v, want none", got)
	}
}

func TestSubagentContinuationIsSupersededByItsNextExecution(t *testing.T) {
	h := newAutoContinueHarness(t)
	h.endSubagent(quotaError(h.clock.Now().Add(time.Hour), ""), "worker-a", "task-a")
	h.svc.SubagentExecutionStarting(context.Background(), "worker-a")
	if _, ok := h.svc.PendingAutoContinue("worker-a"); ok {
		t.Fatal("still pending after the subagent's next execution reached it")
	}
	var cancelled event.AutoContinueCancelledPayload
	h.sink.last(t, event.RunEventAutoContinueCancelled, &cancelled)
	if cancelled.Reason != AutoContinueSuperseded || cancelled.AgentID != "task-a" {
		t.Fatalf("cancelled = %+v", cancelled)
	}
	h.clock.Advance(2 * time.Hour)
	if got := h.continuations(); len(got) != 0 {
		t.Fatalf("a superseded continuation fired: %+v", got)
	}
}

func TestSubagentContinuationsStopAfterFiveInARow(t *testing.T) {
	h := newAutoContinueHarness(t)
	for attempt := 1; attempt <= defaultAutoContinueMaxAttempts; attempt++ {
		h.endSubagent(quotaError(h.clock.Now().Add(time.Hour), ""), "worker-a", "task-a")
		plan, ok := h.svc.PendingAutoContinue("worker-a")
		if !ok || plan.Attempt != attempt {
			t.Fatalf("attempt %d: plan = %+v, pending %v", attempt, plan, ok)
		}
		h.clock.Advance(time.Hour + defaultAutoContinueGrace)
	}
	h.endSubagent(quotaError(h.clock.Now().Add(time.Hour), ""), "worker-a", "task-a")
	if _, ok := h.svc.PendingAutoContinue("worker-a"); ok {
		t.Fatal("kept continuing past the attempt cap")
	}
	h.endSubagent(quotaError(h.clock.Now().Add(time.Hour), ""), "worker-a", "task-a")
	if plan, ok := h.svc.PendingAutoContinue("worker-a"); !ok || plan.Attempt != 1 {
		t.Fatalf("after the cap: plan = %+v, pending %v", plan, ok)
	}
}

func TestSubagentSuccessResetsItsContinuationCount(t *testing.T) {
	h := newAutoContinueHarness(t)
	h.endSubagent(quotaError(h.clock.Now().Add(time.Hour), ""), "worker-a", "task-a")
	h.clock.Advance(time.Hour + defaultAutoContinueGrace)
	h.endSubagent(nil, "worker-a", "task-a")
	h.endSubagent(quotaError(h.clock.Now().Add(time.Hour), ""), "worker-a", "task-a")
	if plan, ok := h.svc.PendingAutoContinue("worker-a"); !ok || plan.Attempt != 1 {
		t.Fatalf("plan = %+v, pending %v; want a fresh first attempt after success", plan, ok)
	}
}

// TestSubmitRunsConfigSyncFirst pins the turn-boundary contract of the
// configuration sync (plan 187): it runs before the executor, exactly once
// per Submit, and its error fails the turn before any run exists.
func TestSubmitRunsConfigSyncFirst(t *testing.T) {
	var order []string
	svc := New(
		WithRunExecutor(runFunc(func(context.Context, TurnRequest, func(string)) (*agent.Result, error) {
			order = append(order, "run")
			return &agent.Result{}, nil
		})),
		WithConfigSync(func(context.Context) error {
			order = append(order, "sync")
			return nil
		}),
	)
	out, err := svc.Submit(context.Background(), TurnRequest{SessionID: "s1"}, nil)
	if err != nil || out.Status != TurnCompleted {
		t.Fatalf("outcome = %#v, error = %v", out, err)
	}
	if len(order) != 2 || order[0] != "sync" || order[1] != "run" {
		t.Fatalf("execution order = %v, want sync before run", order)
	}
}

// TestSubmitFailsWhenConfigSyncFails pins the fail-closed half: a sync error
// is the submitter's error, and the executor never runs.
func TestSubmitFailsWhenConfigSyncFails(t *testing.T) {
	syncErr := errors.New("state database is broken")
	svc := New(
		WithRunExecutor(runFunc(func(context.Context, TurnRequest, func(string)) (*agent.Result, error) {
			t.Fatal("runner called although the config sync failed")
			return nil, nil
		})),
		WithConfigSync(func(context.Context) error { return syncErr }),
	)
	out, err := svc.Submit(context.Background(), TurnRequest{SessionID: "s1"}, nil)
	if !errors.Is(err, syncErr) {
		t.Fatalf("error = %v, want the sync error back", err)
	}
	if out.Status != TurnFailed || out.Error != syncErr {
		t.Fatalf("outcome = %#v, want a failed turn carrying the sync error", out)
	}
}

// TestSubmitWithoutConfigSyncKeepsWorking pins the nil case: a service whose
// runtime has nothing to sync turns exactly as before.
func TestSubmitWithoutConfigSyncKeepsWorking(t *testing.T) {
	svc := New(WithRunExecutor(runFunc(func(_ context.Context, _ TurnRequest, started func(string)) (*agent.Result, error) {
		started("run-plain")
		return &agent.Result{}, nil
	})))
	out, err := svc.Submit(context.Background(), TurnRequest{SessionID: "s1"}, nil)
	if err != nil || out.Status != TurnCompleted || out.RunID != "run-plain" {
		t.Fatalf("outcome = %#v, error = %v", out, err)
	}
}

// openTurnRecordLog installs a real event log on a throwaway home and returns
// a reader for every record Submit wrote, so the engine's own turn records are
// asserted on what landed in the jsonl both surfaces read (the same approach
// as pkg/telemetry's own tests).
func openTurnRecordLog(t *testing.T) func() []map[string]any {
	t.Helper()
	root := t.TempDir()
	log := telemetry.OpenEventLog(root, "test")
	telemetry.SetDefaultEventLog(log)
	t.Cleanup(func() {
		telemetry.SetDefaultEventLog(nil)
		_ = log.Close()
	})
	return func() []map[string]any {
		files, err := filepath.Glob(filepath.Join(root, "logs", "*", "*.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		var records []map[string]any
		for _, path := range files {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
				if line == "" {
					continue
				}
				var rec map[string]any
				if err := json.Unmarshal([]byte(line), &rec); err != nil {
					t.Fatalf("record is not JSON: %q", line)
				}
				if rec["kind"] == "turn" {
					records = append(records, rec)
				}
			}
		}
		return records
	}
}

// TestSubmitWritesTurnRecordsForEveryOutcome pins the engine as the one write
// point for turn records: every outcome writes exactly one record per exit,
// with the message, level and channel both surfaces share, and a failure's
// error reaches the file redacted.
func TestSubmitWritesTurnRecordsForEveryOutcome(t *testing.T) {
	secret := "Bearer sk-live-turn-record-fixture"
	tests := []struct {
		name string
		run  func(context.Context, TurnRequest, func(string)) (*agent.Result, error)
		// want lists every turn record in order as "msg level".
		want  []string
		check func(t *testing.T, records []map[string]any)
	}{
		{
			name: "completed",
			run: func(_ context.Context, _ TurnRequest, started func(string)) (*agent.Result, error) {
				started("run-1")
				return &agent.Result{Parts: []llm.ContentPart{llm.Text("hello answer")}}, nil
			},
			want: []string{"turn started INFO", "turn completed INFO"},
			check: func(t *testing.T, records []map[string]any) {
				completed := records[len(records)-1]
				attrs, _ := completed["attrs"].(map[string]any)
				if attrs == nil || attrs["assistant_bytes"] != float64(len("hello answer")) {
					t.Fatalf("completed record has no assistant_bytes: %v", completed)
				}
				if preview, _ := attrs["preview"].(string); preview != "hello answer" {
					t.Fatalf("completed preview = %q, want the answer", preview)
				}
				if _, has := attrs["elapsed"]; !has {
					t.Fatalf("completed record has no elapsed: %v", completed)
				}
			},
		},
		{
			name: "completed without an answer omits the answer attrs",
			run: func(_ context.Context, _ TurnRequest, started func(string)) (*agent.Result, error) {
				started("run-1")
				return &agent.Result{}, nil
			},
			want: []string{"turn started INFO", "turn completed INFO"},
			check: func(t *testing.T, records []map[string]any) {
				completed := records[len(records)-1]
				attrs, _ := completed["attrs"].(map[string]any)
				if attrs != nil {
					if _, has := attrs["assistant_bytes"]; has {
						t.Fatalf("silent run recorded assistant_bytes: %v", completed)
					}
					if _, has := attrs["preview"]; has {
						t.Fatalf("silent run recorded a preview: %v", completed)
					}
				}
			},
		},
		{
			name: "cancelled",
			run: func(_ context.Context, _ TurnRequest, started func(string)) (*agent.Result, error) {
				started("run-1")
				return nil, context.Canceled
			},
			want: []string{"turn started INFO", "turn cancelled INFO"},
		},
		{
			name: "failed redacts the error",
			run: func(_ context.Context, _ TurnRequest, started func(string)) (*agent.Result, error) {
				started("run-1")
				return nil, errors.New("provider refused: " + secret)
			},
			want: []string{"turn started INFO", "turn failed ERROR"},
			check: func(t *testing.T, records []map[string]any) {
				failed := records[len(records)-1]
				if failed["err"] == nil || !strings.Contains(failed["err"].(string), "provider refused") {
					t.Fatalf("failed record carries no error text: %v", failed)
				}
				if strings.Contains(failed["err"].(string), "sk-live-turn-record-fixture") {
					t.Fatalf("the bearer token reached the record: %v", failed)
				}
				if !strings.Contains(failed["err"].(string), "Bearer [REDACTED]") {
					t.Fatalf("no redaction marker in the record: %v", failed)
				}
			},
		},
		{
			name: "waiting for approval",
			run: func(_ context.Context, _ TurnRequest, started func(string)) (*agent.Result, error) {
				started("run-1")
				return nil, &WaitingError{
					Request: &ToolApprovalRequest{ActionID: "act-1", RunID: "run-1"},
					Resume:  &ApprovalResumeState{ActionID: "act-1"},
				}
			},
			want: []string{"turn started INFO", "turn awaiting approval INFO"},
			check: func(t *testing.T, records []map[string]any) {
				waiting := records[len(records)-1]
				attrs, _ := waiting["attrs"].(map[string]any)
				if attrs == nil || attrs["action_id"] != "act-1" {
					t.Fatalf("approval record has no action_id: %v", waiting)
				}
			},
		},
		{
			name: "executor that never created a run still records a start without one",
			run: func(context.Context, TurnRequest, func(string)) (*agent.Result, error) {
				return nil, errors.New("executor failed before the run existed")
			},
			want: []string{"turn started INFO", "turn failed ERROR"},
			check: func(t *testing.T, records []map[string]any) {
				for _, rec := range records {
					if run, has := rec["run"]; has && run != "" {
						t.Fatalf("a record named a run the executor never created: %v", rec)
					}
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			read := openTurnRecordLog(t)
			svc := New(WithRunExecutor(runFunc(tt.run)))
			out, _ := svc.Submit(context.Background(), TurnRequest{
				SessionID: "sess-1",
				Origin:    Origin{Surface: SurfaceWebChat, ChannelID: "webchat"},
				RawInput:  "the user's own words",
				UserText:  "the user's own words",
			}, nil)
			if out.RunID != "run-1" && tt.want[0] == "turn started INFO" && out.Status != TurnFailed {
				t.Fatalf("outcome = %#v", out)
			}
			records := read()
			var got []string
			for _, rec := range records {
				got = append(got, rec["msg"].(string)+" "+rec["lvl"].(string))
			}
			if strings.Join(got, "|") != strings.Join(tt.want, "|") {
				t.Fatalf("records = %v, want %v", got, tt.want)
			}
			for _, rec := range records {
				if rec["session"] != "sess-1" {
					t.Fatalf("record session = %v, want sess-1: %v", rec["session"], rec)
				}
				attrs, _ := rec["attrs"].(map[string]any)
				channel := ""
				if attrs != nil {
					channel, _ = attrs["channel"].(string)
				}
				if channel != "webchat" {
					t.Fatalf("record channel = %q, want webchat: %v", channel, rec)
				}
			}
			started := records[0]
			attrs, _ := started["attrs"].(map[string]any)
			if attrs == nil || attrs["input_bytes"] != float64(len("the user's own words")) {
				t.Fatalf("started record has no input_bytes: %v", started)
			}
			if preview, _ := attrs["preview"].(string); preview != "the user's own words" {
				t.Fatalf("started preview = %q", preview)
			}
			if tt.check != nil {
				tt.check(t, records)
			}
		})
	}
}

// TestTurnRecordIdentityComesFromTheRequestNotTheScope is the gateway's
// detached-turn shape: the context carries a scope for some other session (or
// none at all), and the record still names the request's own session, run and
// channel, because the explicit attributes win over whatever the scope says.
func TestTurnRecordIdentityComesFromTheRequestNotTheScope(t *testing.T) {
	read := openTurnRecordLog(t)
	telemetry.RegisterScope(func(context.Context) telemetry.Scope {
		return telemetry.Scope{Project: "p-ctx", Session: "s-from-scope", Run: "r-from-scope"}
	})
	t.Cleanup(func() { telemetry.RegisterScope(nil) })

	svc := New(WithRunExecutor(runFunc(func(_ context.Context, _ TurnRequest, started func(string)) (*agent.Result, error) {
		started("run-from-executor")
		return &agent.Result{}, nil
	})))
	if _, err := svc.Submit(context.Background(), TurnRequest{
		SessionID: "sess-req",
		Origin:    Origin{Surface: SurfaceChannel, ChannelID: "feishu"},
		RawInput:  "via a channel",
	}, nil); err != nil {
		t.Fatal(err)
	}
	records := read()
	if len(records) != 2 {
		t.Fatalf("records = %d, want 2 (started + completed): %v", len(records), records)
	}
	for _, rec := range records {
		if rec["session"] != "sess-req" {
			t.Fatalf("record session = %v, want sess-req from the request: %v", rec["session"], rec)
		}
		if rec["run"] != "run-from-executor" {
			t.Fatalf("record run = %v, want the executor's run: %v", rec["run"], rec)
		}
		attrs, _ := rec["attrs"].(map[string]any)
		channel, _ := attrs["channel"].(string)
		if channel != "channel" {
			t.Fatalf("record channel = %q, want the channel surface: %v", channel, rec)
		}
	}
}

// TestStartedRecordFallsBackToUserTextWhenThereIsNoRawInput pins the plain
// message's shape: RawInput only carries the original text of a slash turn
// (the expanded prompt lives in UserText/Parts), so a plain message's start
// record sizes and previews the message itself rather than an empty string.
func TestStartedRecordFallsBackToUserTextWhenThereIsNoRawInput(t *testing.T) {
	read := openTurnRecordLog(t)
	svc := New(WithRunExecutor(runFunc(func(_ context.Context, _ TurnRequest, started func(string)) (*agent.Result, error) {
		started("run-plain")
		return &agent.Result{}, nil
	})))
	if _, err := svc.Submit(context.Background(), TurnRequest{
		SessionID: "sess-plain",
		Origin:    Origin{Surface: SurfaceTUI},
		UserText:  "a plain message, no separate raw form",
	}, nil); err != nil {
		t.Fatal(err)
	}
	records := read()
	if len(records) == 0 {
		t.Fatal("no turn records were written")
	}
	started := records[0]
	attrs, _ := started["attrs"].(map[string]any)
	if attrs == nil || attrs["input_bytes"] != float64(len("a plain message, no separate raw form")) {
		t.Fatalf("started record did not size the message: %v", started)
	}
	if preview, _ := attrs["preview"].(string); preview != "a plain message, no separate raw form" {
		t.Fatalf("started preview = %q, want the message", preview)
	}
}

// deliverableEnv is one project conversation with its real stores, so the
// delivery assertions run against the same reads and writes production uses.
type deliverableEnv struct {
	projects *state.ProjectStore
	sessions *state.SessionStore
	runs     *state.RunStore
	root     string
	session  string
	runID    string
}

func newDeliverableEnv(t *testing.T, projectType string) *deliverableEnv {
	t.Helper()
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	projects := state.NewProjectStore(db, "main")
	sessions := state.NewSessionStore(db, "main")
	root := t.TempDir()
	p, err := projects.Create(ctx, state.CreateProjectInput{Name: "proj", Root: root, ProjectType: projectType})
	if err != nil {
		t.Fatal(err)
	}
	const sid = "s-dl"
	if err := sessions.EnsureAt(ctx, sid, sid, state.SessionBirth{ProjectID: p.ID}); err != nil {
		t.Fatal(err)
	}
	run, err := (&state.RunStore{DB: db}).CreateRun(ctx, sid, "do work")
	if err != nil {
		t.Fatal(err)
	}
	return &deliverableEnv{projects: projects, sessions: sessions, runs: &state.RunStore{DB: db}, root: root, session: sid, runID: run.ID}
}

func (e *deliverableEnv) write(t *testing.T, rel, content string) string {
	t.Helper()
	path := filepath.Join(e.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func (e *deliverableEnv) recordChange(t *testing.T, runID, path string, deleted bool) {
	t.Helper()
	if err := e.sessions.RecordFileChange(context.Background(), state.FileChange{
		SessionID: e.session, RunID: runID, Path: path, Deleted: deleted,
	}); err != nil {
		t.Fatal(err)
	}
}

func (e *deliverableEnv) runArtifacts(t *testing.T, runID string) []state.Artifact {
	t.Helper()
	rows, err := e.sessions.ListArtifactsForRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// TestSubmitOfficeTurnRegistersNoFallbackArtifacts pins the half of the
// owner's 2026-11-19/2026-11-21 contract that stands: an office conversation
// delivers only what the model presents, when the model judges the work
// complete. A helper script the model kept out of the delivery set stays out,
// even though the ledger tracked the write. Coding is the exception — the work
// is the deliverable there — and its turns are pinned by the
// TestSubmitCoding* cases below.
func TestSubmitOfficeTurnRegistersNoFallbackArtifacts(t *testing.T) {
	const pt = state.ProjectTypeOffice
	for _, outcome := range []struct {
		name string
		err  error
	}{
		{"completed", nil},
		{"cancelled", context.Canceled},
		{"failed", errors.New("boom")},
	} {
		t.Run(pt+"/"+outcome.name, func(t *testing.T) {
			env := newDeliverableEnv(t, pt)
			path := env.write(t, ".work/helper.py", "package main\n")
			svc := New(
				WithSessionStore(env.sessions),
				WithRunEventStore(env.runs),
				WithBatchTasks(nil, env.sessions, nil),
				WithRunExecutor(runFunc(func(runCtx context.Context, _ TurnRequest, started func(string)) (*agent.Result, error) {
					run, err := env.runs.CreateRun(runCtx, env.session, "work")
					if err != nil {
						return nil, err
					}
					started(run.ID)
					if err := env.sessions.RecordFileChange(runCtx, state.FileChange{
						SessionID: env.session, RunID: run.ID, Path: path,
					}); err != nil {
						return nil, err
					}
					return &agent.Result{}, outcome.err
				})),
			)
			out, err := svc.Submit(context.Background(), TurnRequest{SessionID: env.session}, nil)
			if err != nil && outcome.err == nil {
				t.Fatalf("submit: %v", err)
			}
			if rows := env.runArtifacts(t, out.RunID); len(rows) != 0 {
				t.Fatalf("%s %s turn registered engine-side artifacts: %+v", pt, outcome.name, rows)
			}
		})
	}
}

// TestSubmitKeepsModelPresentedArtifacts is the positive control: removing
// the engine-side collection must not touch what the model itself delivered.
func TestSubmitKeepsModelPresentedArtifacts(t *testing.T) {
	env := newDeliverableEnv(t, state.ProjectTypeOffice)
	deliverable := env.write(t, "2026Q3-report.docx", "final")
	scratch := env.write(t, ".work/helper.py", "print(1)\n")
	svc := New(
		WithSessionStore(env.sessions),
		WithRunEventStore(env.runs),
		WithBatchTasks(nil, env.sessions, nil),
		WithRunExecutor(runFunc(func(runCtx context.Context, _ TurnRequest, started func(string)) (*agent.Result, error) {
			run, err := env.runs.CreateRun(runCtx, env.session, "work")
			if err != nil {
				return nil, err
			}
			started(run.ID)
			for _, p := range []string{deliverable, scratch} {
				if err := env.sessions.RecordFileChange(runCtx, state.FileChange{
					SessionID: env.session, RunID: run.ID, Path: p,
				}); err != nil {
					return nil, err
				}
			}
			if _, err := env.sessions.AddArtifact(runCtx, state.Artifact{
				SessionID: env.session, RunID: run.ID, Path: deliverable,
				RelPath: "2026Q3-report.docx", Name: "2026Q3-report.docx",
				Kind: "document", SHA256: rowsOf(t, deliverable), Origin: state.ArtifactOriginModel,
			}); err != nil {
				return nil, err
			}
			return &agent.Result{}, nil
		})),
	)
	out, err := svc.Submit(context.Background(), TurnRequest{SessionID: env.session}, nil)
	if err != nil {
		t.Fatal(err)
	}
	rows := env.runArtifacts(t, out.RunID)
	if len(rows) != 1 || rows[0].Origin != state.ArtifactOriginModel || rows[0].Name != "2026Q3-report.docx" {
		t.Fatalf("artifacts = %+v, want exactly the model-presented deliverable", rows)
	}
}

// submitCodingTurn runs one coding conversation's turn whose run already exists
// — so a test can fill the ledger of that run and of the subagent runs it
// spawned before the turn reaches its terminal branch — and returns the rows
// the turn's run holds when Submit comes back, with the outcome.
func (e *deliverableEnv) submitCodingTurn(t *testing.T, outcome error, prepare func(parent *state.Run) error) ([]state.Artifact, TurnOutcome) {
	t.Helper()
	ctx := context.Background()
	parent, err := e.runs.CreateRun(ctx, e.session, "work")
	if err != nil {
		t.Fatal(err)
	}
	if prepare != nil {
		if err := prepare(parent); err != nil {
			t.Fatal(err)
		}
	}
	svc := New(
		WithSessionStore(e.sessions),
		WithRunEventStore(e.runs),
		WithBatchTasks(nil, e.sessions, nil),
		WithRunExecutor(runFunc(func(context.Context, TurnRequest, func(string)) (*agent.Result, error) {
			return &agent.Result{}, outcome
		})),
	)
	// The executor's run is the one the test prepared: announcing its id is
	// what tells the top-up whose tree to read.
	svc.runner = runFunc(func(_ context.Context, _ TurnRequest, started func(string)) (*agent.Result, error) {
		started(parent.ID)
		return &agent.Result{}, outcome
	})
	out, err := svc.Submit(ctx, TurnRequest{SessionID: e.session}, nil)
	if err != nil && outcome == nil {
		t.Fatalf("submit: %v", err)
	}
	return e.runArtifacts(t, out.RunID), out
}

// TestSubmitCodingTurnRegistersUnpresentedWrittenFiles is the defect the plan
// fixes: a coding turn's subagent writes inside a worktree, the model presents
// only the plan files, and the worktree's file never reached the delivery list.
// The turn-end top-up must register it as a fallback row beside what the model
// presented.
func TestSubmitCodingTurnRegistersUnpresentedWrittenFiles(t *testing.T) {
	env := newDeliverableEnv(t, state.ProjectTypeCoding)
	presented := env.write(t, "plans/x.md", "# plan\n")
	worktreeFile := env.write(t, ".worktrees/demo/src/a.go", "package a\n")
	rows, out := env.submitCodingTurn(t, nil, func(parent *state.Run) error {
		env.recordChange(t, parent.ID, presented, false)
		if _, err := env.sessions.AddArtifact(context.Background(), state.Artifact{
			SessionID: env.session, RunID: parent.ID, Path: presented,
			RelPath: "plans/x.md", Name: "x.md", Kind: "document",
			SHA256: rowsOf(t, presented), Origin: state.ArtifactOriginModel,
		}); err != nil {
			return err
		}
		child, err := env.runs.CreateSubagentRun(context.Background(), parent.ID, env.session, "executor")
		if err != nil {
			return err
		}
		env.recordChange(t, child.ID, worktreeFile, false)
		return nil
	})
	if len(rows) != 2 {
		t.Fatalf("artifacts = %+v, want the presented plan and the worktree write", rows)
	}
	var fallback []state.Artifact
	for _, row := range rows {
		if row.Origin == state.ArtifactOriginFallback {
			fallback = append(fallback, row)
		}
	}
	if len(fallback) != 1 {
		t.Fatalf("fallback rows = %+v, want exactly the worktree file", fallback)
	}
	if rows[0].Origin != state.ArtifactOriginModel && rows[1].Origin != state.ArtifactOriginModel {
		t.Fatalf("the model's presentation was replaced: %+v", rows)
	}
	if rel := fallback[0].RelPath; rel != ".worktrees/demo/src/a.go" {
		t.Fatalf("fallback RelPath = %q, want the worktree-relative path", rel)
	}
	if fallback[0].RunID != out.RunID {
		t.Fatalf("fallback row belongs to run %q, want the turn's run %q", fallback[0].RunID, out.RunID)
	}
}

// TestSubmitCodingTurnWithNoPresentationStillListsWrites is the worst case the
// owner chose: a coding turn that presented nothing still delivers the files
// its tools wrote.
func TestSubmitCodingTurnWithNoPresentationStillListsWrites(t *testing.T) {
	env := newDeliverableEnv(t, state.ProjectTypeCoding)
	written := env.write(t, ".worktrees/demo/src/a.go", "package a\n")
	rows, _ := env.submitCodingTurn(t, nil, func(parent *state.Run) error {
		env.recordChange(t, parent.ID, written, false)
		return nil
	})
	if len(rows) != 1 || rows[0].Origin != state.ArtifactOriginFallback || rows[0].RelPath != ".worktrees/demo/src/a.go" {
		t.Fatalf("artifacts = %+v, want exactly the write as a fallback row", rows)
	}
}

// TestSubmitCodingTurnShellStepWorktreePathRegisters pins the second source of
// the tracked write set: a shell step's changed_files, which carries the paths
// the file-change ledger never saw (a command that wrote the file itself).
func TestSubmitCodingTurnShellStepWorktreePathRegisters(t *testing.T) {
	env := newDeliverableEnv(t, state.ProjectTypeCoding)
	written := env.write(t, ".worktrees/demo/pkg/a.go", "package a\n")
	rows, _ := env.submitCodingTurn(t, nil, func(parent *state.Run) error {
		child, err := env.runs.CreateSubagentRun(context.Background(), parent.ID, env.session, "executor")
		if err != nil {
			return err
		}
		return env.appendShellStep(child.ID, written)
	})
	if len(rows) != 1 || rows[0].Origin != state.ArtifactOriginFallback || rows[0].RelPath != ".worktrees/demo/pkg/a.go" {
		t.Fatalf("artifacts = %+v, want the shell step's write as a fallback row", rows)
	}
}

// TestSubmitCodingTurnPresentThenEditRegistersNewVersion pins the version
// comparison: presenting one version does not cover the bytes the turn wrote
// afterwards, and the newer version is registered under the same path.
func TestSubmitCodingTurnPresentThenEditRegistersNewVersion(t *testing.T) {
	env := newDeliverableEnv(t, state.ProjectTypeCoding)
	presented := env.write(t, "plans/x.md", "# plan v1\n")
	rows, _ := env.submitCodingTurn(t, nil, func(parent *state.Run) error {
		env.recordChange(t, parent.ID, presented, false)
		if _, err := env.sessions.AddArtifact(context.Background(), state.Artifact{
			SessionID: env.session, RunID: parent.ID, Path: presented,
			RelPath: "plans/x.md", Name: "x.md", Kind: "document",
			SHA256: rowsOf(t, presented), Origin: state.ArtifactOriginModel,
		}); err != nil {
			return err
		}
		// The same turn edits the file again after presenting it.
		env.write(t, "plans/x.md", "# plan v2\n")
		env.recordChange(t, parent.ID, presented, false)
		return nil
	})
	if len(rows) != 2 {
		t.Fatalf("artifacts = %+v, want the presented version and the edit", rows)
	}
	if rows[0].RelPath != rows[1].RelPath || rows[0].SHA256 == rows[1].SHA256 {
		t.Fatalf("artifacts = %+v, want two versions of one path", rows)
	}
	if rows[1].Origin != state.ArtifactOriginFallback {
		t.Fatalf("the later version's origin = %q, want fallback", rows[1].Origin)
	}
}

// TestSubmitCodingTopUpSkipsDeletedGitAndOutside pins what a deliverable is
// not: a deleted write, git metadata, and anything outside the project.
func TestSubmitCodingTopUpSkipsDeletedGitAndOutside(t *testing.T) {
	env := newDeliverableEnv(t, state.ProjectTypeCoding)
	deleted := env.write(t, "src/gone.go", "package gone\n")
	gitMeta := env.write(t, ".git/x", "ref\n")
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("elsewhere"), 0o644); err != nil {
		t.Fatal(err)
	}
	rows, _ := env.submitCodingTurn(t, nil, func(parent *state.Run) error {
		for _, path := range []string{deleted, gitMeta, outside} {
			env.recordChange(t, parent.ID, path, path == deleted)
		}
		return nil
	})
	if len(rows) != 0 {
		t.Fatalf("artifacts = %+v, want none of the deleted, git or outside paths", rows)
	}
}

// TestSubmitCodingTopUpTruncatesAtLimitSorted pins the cap's reproducibility:
// 250 candidates register exactly the 200 whose paths sort first, so two runs
// over one write set deliver the same rows.
func TestSubmitCodingTopUpTruncatesAtLimitSorted(t *testing.T) {
	env := newDeliverableEnv(t, state.ProjectTypeCoding)
	const candidates = 250
	var all []string
	for i := 0; i < candidates; i++ {
		all = append(all, env.write(t, fmt.Sprintf("pkg/f%03d/a.go", i), "package a\n"))
	}
	rows, _ := env.submitCodingTurn(t, nil, func(parent *state.Run) error {
		for _, path := range all {
			env.recordChange(t, parent.ID, path, false)
		}
		return nil
	})
	if len(rows) != turnDeliverablePathLimit {
		t.Fatalf("registered %d rows, want the cap of %d", len(rows), turnDeliverablePathLimit)
	}
	got := make(map[string]bool, len(rows))
	for _, row := range rows {
		got[row.RelPath] = true
	}
	for i := 0; i < turnDeliverablePathLimit; i++ {
		rel := fmt.Sprintf("pkg/f%03d/a.go", i)
		if !got[rel] {
			t.Fatalf("the sorted prefix is missing %q", rel)
		}
	}
}

// appendShellStep stores one completed shell step whose diff reports the given
// changed files, the way the tool layer leaves it for the top-up to read.
func (e *deliverableEnv) appendShellStep(runID string, changed ...string) error {
	payload, err := json.Marshal(map[string]any{
		"output": map[string]any{
			"shell_edit_diff": map[string]any{"changed_files": changed},
		},
	})
	if err != nil {
		return err
	}
	_, err = e.runs.AppendSessionEvent(context.Background(), state.SessionEvent{
		ID: uuid.NewString(), SessionID: e.session, RunID: runID,
		Type:    event.RunEventToolCompleted,
		Payload: payload,
	})
	return err
}

// rowsOf hashes a file the way the store's rows do.
func rowsOf(t *testing.T, path string) string {
	t.Helper()
	sum, _, _, _, _, _, err := hashArtifactFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sum
}
