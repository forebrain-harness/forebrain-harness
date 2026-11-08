// Copyright 2026 Simone Vellei
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package llm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestNewRetry_Validation checks that zero/negative maxAttempts is rejected.
func TestNewRetry_Validation(t *testing.T) {
	for _, n := range []int{0, -1} {
		_, err := NewRetry(n)
		if !errors.Is(err, ErrInvalidMaxAttempts) {
			t.Errorf("maxAttempts=%d: got %v, want ErrInvalidMaxAttempts", n, err)
		}
	}
}

// TestNewRetry_SuccessOnFirstAttempt checks that a successful call is returned immediately.
func TestNewRetry_SuccessOnFirstAttempt(t *testing.T) {
	mw, err := NewRetry(3, WithInitialBackoff(0))
	if err != nil {
		t.Fatalf("NewRetry: %v", err)
	}

	client := Use(okLLM("ok"), mw)
	result, err := client.Execute(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Message.TextContent() != "ok" {
		t.Fatalf("got %q, want %q", result.Message.TextContent(), "ok")
	}
}

// TestNewRetry_RetriesAndSucceeds verifies that a transient error is retried
// and the eventual success is returned.
func TestNewRetry_RetriesAndSucceeds(t *testing.T) {
	callCount := 0
	transient := errors.New("transient")

	inner := &stubLLM{fn: func(_ context.Context, _ []Message, _ []*Tool) (*Result, error) {
		callCount++
		if callCount < 3 {
			return nil, transient
		}
		return &Result{Message: &Message{Parts: []ContentPart{Text("success")}}}, nil
	}}

	mw, err := NewRetry(5, WithInitialBackoff(0))
	if err != nil {
		t.Fatalf("NewRetry: %v", err)
	}

	client := Use(inner, mw)
	result, execErr := client.Execute(context.Background(), nil, nil)
	if execErr != nil {
		t.Fatalf("Execute: %v", execErr)
	}
	if callCount != 3 {
		t.Fatalf("callCount=%d, want 3", callCount)
	}
	if result.Message.TextContent() != "success" {
		t.Fatalf("got %q, want %q", result.Message.TextContent(), "success")
	}
}

// TestNewRetry_ExhaustsAttempts checks that MaxAttemptsExceededError is returned
// when all retries fail.
func TestNewRetry_ExhaustsAttempts(t *testing.T) {
	sentinel := errors.New("always fails")

	mw, err := NewRetry(3, WithInitialBackoff(0))
	if err != nil {
		t.Fatalf("NewRetry: %v", err)
	}

	client := Use(errLLM(sentinel), mw)
	_, execErr := client.Execute(context.Background(), nil, nil)

	var maxErr *MaxAttemptsExceededError
	if !errors.As(execErr, &maxErr) {
		t.Fatalf("got %T %v, want MaxAttemptsExceededError", execErr, execErr)
	}
	if maxErr.Attempts != 3 {
		t.Fatalf("Attempts=%d, want 3", maxErr.Attempts)
	}
	if !errors.Is(execErr, sentinel) {
		t.Fatalf("Unwrap should yield sentinel error")
	}
}

// TestNewRetry_ShouldRetryFalse verifies that when the predicate returns false
// the error is returned immediately without further attempts.
func TestNewRetry_ShouldRetryFalse(t *testing.T) {
	callCount := 0
	fatal := errors.New("fatal")

	inner := &stubLLM{fn: func(_ context.Context, _ []Message, _ []*Tool) (*Result, error) {
		callCount++
		return nil, fatal
	}}

	mw, err := NewRetry(5,
		WithInitialBackoff(0),
		WithShouldRetry(func(err error) bool { return !errors.Is(err, fatal) }),
	)
	if err != nil {
		t.Fatalf("NewRetry: %v", err)
	}

	client := Use(inner, mw)
	_, execErr := client.Execute(context.Background(), nil, nil)
	if !errors.Is(execErr, fatal) {
		t.Fatalf("got %v, want fatal error", execErr)
	}
	if callCount != 1 {
		t.Fatalf("callCount=%d, want 1 (no retry)", callCount)
	}
}

// TestNewRetry_ContextCancellation verifies that a cancelled context is
// honoured between retry attempts.
func TestNewRetry_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	callCount := 0
	inner := &stubLLM{fn: func(_ context.Context, _ []Message, _ []*Tool) (*Result, error) {
		callCount++
		cancel() // cancel after first attempt
		return nil, errors.New("transient")
	}}

	mw, err := NewRetry(5, WithInitialBackoff(10*time.Millisecond))
	if err != nil {
		t.Fatalf("NewRetry: %v", err)
	}

	client := Use(inner, mw)
	_, execErr := client.Execute(ctx, nil, nil)
	if !errors.Is(execErr, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", execErr)
	}
	if callCount != 1 {
		t.Fatalf("callCount=%d, want 1", callCount)
	}
}

// The helpers below were production functions that only the tests in this
// package ever called. They live here so the production files carry no
// unused code while the tests keep exercising the live code underneath.

// WithInitialBackoff sets the initial back-off duration before the first retry.
// It doubles on each subsequent attempt (capped by WithMaxBackoff). Default: 500ms.
func WithInitialBackoff(d time.Duration) RetryOption {
	return func(c *retryConfig) { c.initialBackoff = d }
}

func TestNewRetry_NotifyPayloadAndCleared(t *testing.T) {
	calls := 0
	boom := &net.OpError{Op: "read", Net: "tcp", Err: errors.New("i/o timeout")}
	inner := &stubLLM{fn: func(_ context.Context, _ []Message, _ []*Tool) (*Result, error) {
		calls++
		return nil, boom
	}}

	var got []TransportRetry
	mw, err := NewRetry(4, // 1 first call + 3 retries
		WithInitialBackoff(0),
		WithShouldRetry(IsRetryableNetworkError),
		WithRetryNotify(func(_ context.Context, tr TransportRetry) { got = append(got, tr) }),
	)
	if err != nil {
		t.Fatalf("NewRetry: %v", err)
	}
	_, execErr := Use(inner, mw).Execute(context.Background(), nil, nil)
	if execErr == nil {
		t.Fatal("expected exhaustion error")
	}

	// Three pre-sleep notifications (retry 1, 2, 3) plus one cleared notice
	// from the loop's return.
	if len(got) != 4 {
		t.Fatalf("notifications = %d, want 4: %+v", len(got), got)
	}
	for i, tr := range got[:3] {
		if tr.Retry != i+1 || tr.Total != 3 {
			t.Fatalf("notification %d: Retry=%d Total=%d, want %d/3", i, tr.Retry, tr.Total, i+1)
		}
		if tr.Cleared {
			t.Fatalf("notification %d must not be Cleared", i)
		}
		if tr.Err != boom {
			t.Fatalf("notification %d: Err = %v, want the failure", i, tr.Err)
		}
	}
	last := got[3]
	if !last.Cleared || last.Retry != 3 || last.Total != 3 {
		t.Fatalf("cleared notification = %+v, want Cleared 3/3", last)
	}
	if last.Err != nil || last.Wait != 0 {
		t.Fatalf("cleared notification = %+v, want no error and no wait", last)
	}
	if calls != 4 {
		t.Fatalf("callCount=%d, want 4", calls)
	}
}

func TestNewRetry_NotifySilentOnPredicateRejection(t *testing.T) {
	fatal := &APIError{StatusCode: 400, Message: "bad shape"}
	inner := &stubLLM{fn: func(_ context.Context, _ []Message, _ []*Tool) (*Result, error) {
		return nil, fatal
	}}

	var got []TransportRetry
	mw, err := NewRetry(3,
		WithInitialBackoff(0),
		WithShouldRetry(IsRetryableNetworkError),
		WithRetryNotify(func(_ context.Context, tr TransportRetry) { got = append(got, tr) }),
	)
	if err != nil {
		t.Fatalf("NewRetry: %v", err)
	}
	_, execErr := Use(inner, mw).Execute(context.Background(), nil, nil)
	if execErr == nil {
		t.Fatal("expected the fatal error")
	}
	if len(got) != 0 {
		t.Fatalf("a rejected error must produce no notifications, got %+v", got)
	}
}

// The server's Retry-After raises the wait to at least what it asked for,
// even when the back-off step computed is far shorter.
func TestRetryHonoursRetryAfter(t *testing.T) {
	calls := 0
	overloaded := &APIError{StatusCode: 503, Message: "unavailable", RetryAfter: 40 * time.Millisecond}
	inner := &stubLLM{fn: func(_ context.Context, _ []Message, _ []*Tool) (*Result, error) {
		calls++
		if calls == 1 {
			return nil, overloaded
		}
		return &Result{Message: &Message{Parts: []ContentPart{Text("ok")}}}, nil
	}}

	var got []TransportRetry
	mw, err := NewRetry(3,
		WithInitialBackoff(time.Millisecond),
		WithShouldRetry(IsRetryableNetworkError),
		WithRetryAfter(RetryAfterOf, 5*time.Minute),
		WithRetryNotify(func(_ context.Context, tr TransportRetry) { got = append(got, tr) }),
	)
	if err != nil {
		t.Fatalf("NewRetry: %v", err)
	}
	res, execErr := Use(inner, mw).Execute(context.Background(), nil, nil)
	if execErr != nil {
		t.Fatalf("Execute: %v", execErr)
	}
	if res.Message.TextContent() != "ok" {
		t.Fatalf("result = %q, want the retried attempt's answer", res.Message.TextContent())
	}
	if len(got) != 2 || got[0].Cleared || !got[1].Cleared {
		t.Fatalf("notifications = %+v, want retry 1 in flight then its cleared notice", got)
	}
	// The back-off step is 1ms with ±25% jitter; only the server's 40ms can
	// account for a wait this long.
	if got[0].Wait < 40*time.Millisecond {
		t.Fatalf("wait = %v, want at least the server-requested 40ms", got[0].Wait)
	}
}

// A Retry-After longer than the limit is the server saying not now: the loop
// returns the error on the spot — one call, no notification, no parked turn.
func TestRetryStopsOnLongRetryAfter(t *testing.T) {
	calls := 0
	overloaded := &APIError{StatusCode: 503, Message: "unavailable", RetryAfter: time.Minute}
	inner := &stubLLM{fn: func(_ context.Context, _ []Message, _ []*Tool) (*Result, error) {
		calls++
		return nil, overloaded
	}}

	var got []TransportRetry
	mw, err := NewRetry(3,
		WithInitialBackoff(time.Millisecond),
		WithShouldRetry(IsRetryableNetworkError),
		WithRetryAfter(RetryAfterOf, 5*time.Second),
		WithRetryNotify(func(_ context.Context, tr TransportRetry) { got = append(got, tr) }),
	)
	if err != nil {
		t.Fatalf("NewRetry: %v", err)
	}
	_, execErr := Use(inner, mw).Execute(context.Background(), nil, nil)
	if execErr == nil {
		t.Fatal("expected the overload error to surface")
	}
	if !errors.Is(execErr, overloaded) {
		t.Fatalf("error = %v, want the original overload error", execErr)
	}
	if calls != 1 {
		t.Fatalf("callCount=%d, want 1 (no retry)", calls)
	}
	if len(got) != 0 {
		t.Fatalf("notifications = %+v, want none", got)
	}
}

func TestRetryBackoffCapsWithUpwardJitter(t *testing.T) {
	inner := &stubLLM{fn: func(_ context.Context, _ []Message, _ []*Tool) (*Result, error) {
		return nil, errors.New("transient")
	}}
	var notes []TransportRetry
	cfg := &retryConfig{
		maxAttempts:    4,
		initialBackoff: time.Millisecond,
		maxBackoff:     2 * time.Millisecond,
		upwardJitter:   true,
		shouldRetry:    func(error) bool { return true },
		notify:         func(_ context.Context, tr TransportRetry) { notes = append(notes, tr) },
	}
	if _, err := (&retryLLM{inner: inner, cfg: cfg}).Execute(context.Background(), nil, nil); err == nil {
		t.Fatal("expected exhaustion error")
	}

	// Upward jitter keeps each wait within [step, step+25%] of its back-off
	// step, and the cap pins the third step at 2ms instead of letting it
	// double to 4ms. The list ends with the loop's cleared notice.
	if len(notes) != 4 || !notes[3].Cleared {
		t.Fatalf("notifications = %+v, want 3 waits plus the cleared one", notes)
	}
	steps := []time.Duration{time.Millisecond, 2 * time.Millisecond, 2 * time.Millisecond}
	for i, tr := range notes[:3] {
		if tr.Cleared {
			t.Fatalf("notification %d must not be Cleared", i)
		}
		lo, hi := steps[i], steps[i]+steps[i]/4
		if tr.Wait < lo || tr.Wait > hi {
			t.Fatalf("wait %d = %v, want it within [%v, %v]", i, tr.Wait, lo, hi)
		}
	}
}

// The cleared notice fires on the retried request's first non-empty delta —
// before its downstream delta, and exactly once even though the loop also
// returns later.
func TestNewRetry_ClearedOnFirstDeltaAfterRetry(t *testing.T) {
	var seq []string
	boom := &net.OpError{Op: "read", Net: "tcp", Err: errors.New("i/o timeout")}
	calls := 0
	inner := &stubLLM{fn: func(ctx context.Context, _ []Message, _ []*Tool) (*Result, error) {
		calls++
		if calls == 1 {
			return nil, boom
		}
		if sink := StreamSinkFrom(ctx); sink != nil && sink.OnDelta != nil {
			sink.OnDelta("hi")
		}
		msg := Message{Parts: []ContentPart{Text("hi")}}
		return &Result{Message: &msg}, nil
	}}

	mw, err := NewRetry(3,
		WithInitialBackoff(0),
		WithShouldRetry(IsRetryableNetworkError),
		WithRetryNotify(func(_ context.Context, tr TransportRetry) {
			if tr.Cleared {
				seq = append(seq, "cleared")
				return
			}
			seq = append(seq, "retry 1")
		}),
	)
	if err != nil {
		t.Fatalf("NewRetry: %v", err)
	}
	sink := &StreamSink{OnDelta: func(text string) { seq = append(seq, "delta "+text) }}
	if _, err := Use(inner, mw).Execute(WithStreamSink(context.Background(), sink), nil, nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := strings.Join(seq, "|"); got != "retry 1|cleared|delta hi" {
		t.Fatalf("sequence = %q, want retry 1, cleared, delta hi and nothing after", got)
	}
}

// A retried request that answers with a tool call alone streams no delta: its
// response start is what ends the retry, so the cleared notice fires there —
// before the downstream start — not when the response returns.
func TestNewRetry_ClearedOnToolOnlyReplyAfterRetry(t *testing.T) {
	var seq []string
	boom := &net.OpError{Op: "read", Net: "tcp", Err: errors.New("i/o timeout")}
	calls := 0
	inner := &stubLLM{fn: func(ctx context.Context, _ []Message, _ []*Tool) (*Result, error) {
		calls++
		if calls == 1 {
			return nil, boom
		}
		call := ToolCall{ID: "call_1", Type: ToolTypeFunction, Function: FunctionCall{Name: "shell", Arguments: "{}"}}
		if sink := StreamSinkFrom(ctx); sink != nil {
			sink.OnResponseStarted()
			sink.OnToolCallReady(call)
		}
		seq = append(seq, "returned")
		msg := AssistantMessage(nil, call)
		return &Result{Message: &msg}, nil
	}}

	mw, err := NewRetry(3,
		WithInitialBackoff(0),
		WithShouldRetry(IsRetryableNetworkError),
		WithRetryNotify(func(_ context.Context, tr TransportRetry) {
			if tr.Cleared {
				seq = append(seq, "cleared")
				return
			}
			seq = append(seq, "retry 1")
		}),
	)
	if err != nil {
		t.Fatalf("NewRetry: %v", err)
	}
	sink := &StreamSink{
		OnResponseStarted: func() { seq = append(seq, "started") },
		OnToolCallReady:   func(ToolCall) { seq = append(seq, "ready") },
	}
	if _, err := Use(inner, mw).Execute(WithStreamSink(context.Background(), sink), nil, nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := strings.Join(seq, "|"); got != "retry 1|cleared|started|ready|returned" {
		t.Fatalf("sequence = %q, want retry 1, cleared, started, ready, returned and nothing after", got)
	}
}

// The streaming shape of exhaustion: every attempt streams one delta before
// dying, so each retry's Cleared fires at the retried attempt's first token
// and the final attempt's own death is announced by nothing else — the
// exhaustion return must emit a catch-up Cleared, or every holder of the
// final attempt's streamed text keeps it forever.
func TestNewRetry_ExhaustionAnnouncesTheFinalStreamingAttempt(t *testing.T) {
	boom := &net.OpError{Op: "read", Net: "tcp", Err: errors.New("i/o timeout")}
	calls := 0
	inner := &stubLLM{fn: func(ctx context.Context, _ []Message, _ []*Tool) (*Result, error) {
		calls++
		if sink := StreamSinkFrom(ctx); sink != nil && sink.OnDelta != nil {
			sink.OnDelta(fmt.Sprintf("attempt %d words", calls))
		}
		return nil, boom
	}}

	var got []TransportRetry
	mw, err := NewRetry(3, // 1 first call + 2 retries
		WithInitialBackoff(0),
		WithShouldRetry(IsRetryableNetworkError),
		WithRetryNotify(func(_ context.Context, tr TransportRetry) { got = append(got, tr) }),
	)
	if err != nil {
		t.Fatalf("NewRetry: %v", err)
	}
	sink := &StreamSink{OnDelta: func(string) {}}
	_, execErr := Use(inner, mw).Execute(WithStreamSink(context.Background(), sink), nil, nil)
	var maxErr *MaxAttemptsExceededError
	if !errors.As(execErr, &maxErr) {
		t.Fatalf("got %T %v, want MaxAttemptsExceededError", execErr, execErr)
	}

	// retry 1 → cleared at attempt 2's first token → retry 2 → cleared at
	// attempt 3's first token → the catch-up Cleared for the final death.
	if len(got) != 5 {
		t.Fatalf("notifications = %+v, want 5", got)
	}
	for i, wantRetry := range []int{1, 1, 2, 2, 2} {
		if got[i].Retry != wantRetry || got[i].Total != 2 {
			t.Fatalf("notification %d = %+v, want Retry %d/2", i, got[i], wantRetry)
		}
	}
	if got[0].Cleared || got[2].Cleared {
		t.Fatalf("the retry announcements must not be Cleared: %+v", got)
	}
	if !got[1].Cleared || !got[3].Cleared {
		t.Fatalf("the token clears must be Cleared: %+v", got)
	}
	// After the second retry announcement only the two Cleared notices
	// follow, and the catch-up is the last word: a bare Cleared 2/2.
	last := got[4]
	if !last.Cleared || last.Err != nil || last.Wait != 0 {
		t.Fatalf("catch-up notification = %+v, want a bare Cleared 2/2", last)
	}
}

// A user cancel keeps the streamed partial on purpose: when the attempt in
// flight has already spent the pending notice on its first token and then
// dies on the cancellation, no Cleared may fire — it would withdraw text the
// user chose to keep.
func TestNewRetry_CancelAfterFirstTokenEmitsNoCleared(t *testing.T) {
	boom := &net.OpError{Op: "read", Net: "tcp", Err: errors.New("i/o timeout")}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	calls := 0
	inner := &stubLLM{fn: func(ctx context.Context, _ []Message, _ []*Tool) (*Result, error) {
		calls++
		if calls == 1 {
			return nil, boom // retryable: retry 1 is announced
		}
		if sink := StreamSinkFrom(ctx); sink != nil && sink.OnDelta != nil {
			sink.OnDelta("kept words") // spends the pending notice
		}
		<-ctx.Done()          // the user hits Esc mid-stream
		return nil, ctx.Err() // non-retryable: the loop returns at once
	}}

	notes := make(chan TransportRetry, 8)
	mw, err := NewRetry(3,
		WithInitialBackoff(0),
		WithShouldRetry(IsRetryableNetworkError),
		WithRetryNotify(func(_ context.Context, tr TransportRetry) { notes <- tr }),
	)
	if err != nil {
		t.Fatalf("NewRetry: %v", err)
	}
	sink := &StreamSink{OnDelta: func(string) {}}
	done := make(chan error, 1)
	go func() {
		_, execErr := Use(inner, mw).Execute(WithStreamSink(ctx, sink), nil, nil)
		done <- execErr
	}()
	// The retry announcement, then its token clear — in that order.
	for i, wantCleared := range []bool{false, true} {
		select {
		case tr := <-notes:
			if tr.Cleared != wantCleared || tr.Retry != 1 || tr.Total != 2 {
				t.Fatalf("notification %d = %+v", i, tr)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("notification %d never arrived", i)
		}
	}
	cancel()
	select {
	case execErr := <-done:
		if !errors.Is(execErr, context.Canceled) {
			t.Fatalf("Execute error = %v, want context.Canceled", execErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Execute never returned after the cancel")
	}
	// Nothing after the token clear: no retry 2, no Cleared.
	select {
	case tr := <-notes:
		t.Fatalf("unexpected notification after the cancel: %+v", tr)
	default:
	}
}

// A fatal first attempt never starts a retry cycle, streamed text or not:
// nothing may be announced — in particular no Cleared, which would withdraw
// text no retry ever owned.
func TestNewRetry_FatalFirstStreamAnnouncesNothing(t *testing.T) {
	fatal := &APIError{StatusCode: 400, Message: "bad shape"}
	inner := &stubLLM{fn: func(ctx context.Context, _ []Message, _ []*Tool) (*Result, error) {
		if sink := StreamSinkFrom(ctx); sink != nil && sink.OnDelta != nil {
			sink.OnDelta("doomed words")
		}
		return nil, fatal
	}}

	var got []TransportRetry
	mw, err := NewRetry(3,
		WithInitialBackoff(0),
		WithShouldRetry(IsRetryableNetworkError),
		WithRetryNotify(func(_ context.Context, tr TransportRetry) { got = append(got, tr) }),
	)
	if err != nil {
		t.Fatalf("NewRetry: %v", err)
	}
	sink := &StreamSink{OnDelta: func(string) {}}
	if _, execErr := Use(inner, mw).Execute(WithStreamSink(context.Background(), sink), nil, nil); !errors.Is(execErr, fatal) {
		t.Fatalf("Execute error = %v, want the fatal error", execErr)
	}
	if len(got) != 0 {
		t.Fatalf("notifications = %+v, want none", got)
	}
}

// Without a notify callback the middleware keeps its hands off the sink: the
// inner LLM sees the caller's sink pointer, identical.
func TestNewRetry_LeavesSinkAloneWithoutNotify(t *testing.T) {
	boom := &net.OpError{Op: "read", Net: "tcp", Err: errors.New("i/o timeout")}
	calls := 0
	var seen *StreamSink
	inner := &stubLLM{fn: func(ctx context.Context, _ []Message, _ []*Tool) (*Result, error) {
		calls++
		seen = StreamSinkFrom(ctx)
		if calls == 1 {
			return nil, boom
		}
		msg := Message{Parts: []ContentPart{Text("ok")}}
		return &Result{Message: &msg}, nil
	}}

	mw, err := NewRetry(2,
		WithInitialBackoff(0),
		WithShouldRetry(IsRetryableNetworkError),
	)
	if err != nil {
		t.Fatalf("NewRetry: %v", err)
	}
	sink := &StreamSink{}
	if _, err := Use(inner, mw).Execute(WithStreamSink(context.Background(), sink), nil, nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	if seen != sink {
		t.Fatal("without a notify callback the sink must pass through untouched")
	}
}

// A retry re-sends the original request: both attempts see the same messages,
// with no continuation appended between them.
func TestNewRetry_ResendsTheSameMessages(t *testing.T) {
	boom := &net.OpError{Op: "read", Net: "tcp", Err: errors.New("i/o timeout")}
	calls := 0
	var received [][]Message
	inner := &stubLLM{fn: func(_ context.Context, messages []Message, _ []*Tool) (*Result, error) {
		calls++
		received = append(received, append([]Message(nil), messages...))
		if calls == 1 {
			return nil, boom
		}
		msg := Message{Parts: []ContentPart{Text("full answer")}}
		return &Result{Message: &msg}, nil
	}}

	mw, err := NewRetry(2,
		WithInitialBackoff(0),
		WithShouldRetry(IsRetryableNetworkError),
	)
	if err != nil {
		t.Fatalf("NewRetry: %v", err)
	}
	messages := []Message{UserMessage(Text("hello"))}
	if _, err := Use(inner, mw).Execute(context.Background(), messages, nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(received) != 2 {
		t.Fatalf("attempts = %d, want 2", len(received))
	}
	for i, got := range received {
		if len(got) != 1 || got[0].Role != RoleUser || got[0].TextContent() != "hello" {
			t.Fatalf("attempt %d messages = %+v, want the original request unchanged", i+1, got)
		}
	}
}

// ExampleNewRetry is the live copy of NewRetry's doc example: it is compiled by
// `go test`, so the API the comment names must exist.
func ExampleNewRetry() {
	inner := &stubLLM{fn: func(context.Context, []Message, []*Tool) (*Result, error) {
		return okResult("hi")
	}}
	mw, err := NewRetry(3, WithMaxBackoff(8*time.Second))
	if err != nil {
		return
	}
	_, _ = Use(inner, mw).Execute(context.Background(), []Message{UserMessage(Text("hi"))}, nil)
}

// The non-streaming loop carries the same attempt accounting as the stream
// retry: a retried call's Result keeps the settled attempt's own usage while
// Attempts counts the physical requests, and an exhausted request's error
// carries the merged usage, the count, and the last attempt's own usage.
func TestRetryAttemptAccounting(t *testing.T) {
	t.Run("counted not merged", func(t *testing.T) {
		calls := 0
		inner := &stubLLM{fn: func(_ context.Context, _ []Message, _ []*Tool) (*Result, error) {
			calls++
			if calls == 1 {
				return nil, NewExecuteError(errors.New("transient"), &Usage{InputTokens: 100, OutputTokens: 10})
			}
			res, _ := okResult("done")
			res.Usage = &Usage{InputTokens: 120, OutputTokens: 8}
			return res, nil
		}}
		mw, err := NewRetry(3, WithMaxBackoff(0))
		if err != nil {
			t.Fatalf("NewRetry: %v", err)
		}
		res, execErr := Use(inner, mw).Execute(context.Background(), nil, nil)
		if execErr != nil {
			t.Fatalf("Execute: %v", execErr)
		}
		if res.Usage == nil || res.Usage.InputTokens != 120 || res.Usage.OutputTokens != 8 {
			t.Fatalf("res.Usage = %+v, want the settled attempt's own usage", res.Usage)
		}
		if res.Attempts != 2 {
			t.Fatalf("res.Attempts = %d, want 2", res.Attempts)
		}
	})
	t.Run("exhausted carries every attempt", func(t *testing.T) {
		usages := []Usage{{InputTokens: 100, OutputTokens: 10}, {InputTokens: 110, OutputTokens: 12}, {InputTokens: 120, OutputTokens: 14}}
		calls := 0
		inner := &stubLLM{fn: func(_ context.Context, _ []Message, _ []*Tool) (*Result, error) {
			u := usages[calls]
			calls++
			return nil, NewExecuteError(errors.New("transient"), &u)
		}}
		mw, err := NewRetry(3, WithMaxBackoff(0))
		if err != nil {
			t.Fatalf("NewRetry: %v", err)
		}
		_, execErr := Use(inner, mw).Execute(context.Background(), nil, nil)
		var exhausted *MaxAttemptsExceededError
		if !errors.As(execErr, &exhausted) {
			t.Fatalf("Execute error = %v, want MaxAttemptsExceededError", execErr)
		}
		usage := UsageFromError(execErr)
		if usage == nil || usage.InputTokens != 330 || usage.OutputTokens != 36 {
			t.Fatalf("UsageFromError = %+v, want the sum of every attempt (330/36)", usage)
		}
		if n := AttemptsFromError(execErr); n != 3 {
			t.Fatalf("AttemptsFromError = %d, want 3", n)
		}
		last := LastAttemptUsageFromError(execErr)
		if last == nil || last.InputTokens != 120 || last.OutputTokens != 14 {
			t.Fatalf("LastAttemptUsageFromError = %+v, want the last attempt's own usage (120/14)", last)
		}
	})
}

// A thinking block's text and signature go back to the provider exactly as it
// returned them: it verifies the block against the signature, so a trimmed
// space or a normalized quote is a rejected request.
func TestThinkingCarryRoundTripsByteForByte(t *testing.T) {
	blocks := []ThinkingBlock{
		{
			Thinking:  "  Plan:\n\tstep \"one\" — back\\slash <>& 中文 🧠  ",
			Signature: "sig-abc+/==",
		},
		{Thinking: "second block", Signature: "sig-def+/=="},
		{Redacted: true, Data: "redacted-opaque-data+/=="},
	}
	carry := EncodeThinking(blocks)
	if !strings.HasPrefix(carry, Prefix) {
		t.Fatalf("carry = %q, want the reasoning carry prefix", carry)
	}
	decoded, ok := DecodeThinking(carry)
	if !ok {
		t.Fatalf("DecodeThinking(%q) = false, want true", carry)
	}
	if !reflect.DeepEqual(decoded, blocks) {
		t.Fatalf("decoded = %#v\nwant      %#v", decoded, blocks)
	}
}

// A block that carries nothing is dropped rather than sent as an empty block;
// a block with a signature and no text is kept, because the provider wrote it
// that way.
func TestEncodeThinkingDropsEmptyBlocks(t *testing.T) {
	if got := EncodeThinking(nil); got != "" {
		t.Fatalf("EncodeThinking(nil) = %q, want empty", got)
	}
	if got := EncodeThinking([]ThinkingBlock{{}, {Redacted: true}, {Thinking: ""}}); got != "" {
		t.Fatalf("EncodeThinking(all empty) = %q, want empty", got)
	}
	signatureOnly := ThinkingBlock{Signature: "sig-only=="}
	got := EncodeThinking([]ThinkingBlock{{}, signatureOnly, {Redacted: true}})
	decoded, ok := DecodeThinking(got)
	if !ok || len(decoded) != 1 || decoded[0] != signatureOnly {
		t.Fatalf("decoded = %#v (ok=%v), want exactly the signature-only block", decoded, ok)
	}
}

// The two payload versions never read as each other, and StructuredCarry is the
// one answer to "is this a protocol's structured payload". Plain text is what
// every protocol can replay.
func TestThinkingCarryIsNotAResponsesPayload(t *testing.T) {
	thinking := EncodeThinking([]ThinkingBlock{{Thinking: "thought", Signature: "sig=="}})
	if _, ok := Decode(thinking); ok {
		t.Fatal("Decode read a thinking carry as a Responses payload")
	}
	responses := Encode([]Item{{ID: "rs_1", EncryptedContent: "x"}})
	if _, ok := DecodeThinking(responses); ok {
		t.Fatal("DecodeThinking read a Responses payload as thinking")
	}
	if _, ok := DecodeThinking(Prefix + "plain"); ok {
		t.Fatal("DecodeThinking read plain reasoning text as thinking")
	}
	for _, name := range []string{thinking, responses} {
		if !StructuredCarry(name) {
			t.Fatalf("StructuredCarry(%q) = false, want true", name)
		}
	}
	for _, name := range []string{Prefix + "plain", ""} {
		if StructuredCarry(name) {
			t.Fatalf("StructuredCarry(%q) = true, want false", name)
		}
	}
}

// What a surface shows of a thinking carry is the reasoning text: no payload
// JSON, no signature and no redacted ciphertext.
func TestExtractSummaryReadsThinkingText(t *testing.T) {
	carry := EncodeThinking([]ThinkingBlock{
		{Thinking: "first thought", Signature: "sig-one=="},
		{Redacted: true, Data: "secret-ciphertext"},
		{Thinking: "second thought", Signature: "sig-two=="},
	})
	got := ExtractSummary(carry)
	if got != "first thought\nsecond thought" {
		t.Fatalf("ExtractSummary = %q, want the thinking text joined by newlines", got)
	}
	for _, unwanted := range []string{"anthropic_thinking_v1", "sig-one==", "sig-two==", "secret-ciphertext"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("ExtractSummary leaked %q: %q", unwanted, got)
		}
	}
}

// The compatible providers that use the default field name keep the bytes this
// protocol has always written: the common case must not move, because every
// provider's cached prefix and every stored session's carry is that shape.
func TestCompatReasoningCarryKeepsTheCommonCaseBytes(t *testing.T) {
	const text = " plan:\n1. look  "
	for _, field := range []string{"", CompatReasoningField} {
		if got, want := EncodeCompatReasoning(field, text), Prefix+text; got != want {
			t.Fatalf("EncodeCompatReasoning(%q, text) = %q, want the plain carry %q", field, got, want)
		}
	}
	// Another name goes into a payload, and comes back byte for byte: the text
	// is what the provider wrote, and a replay is refused for a changed byte as
	// much as for a wrong field.
	carry := EncodeCompatReasoning("reasoning", text)
	field, got, ok := DecodeCompatReasoning(carry)
	if !ok {
		t.Fatalf("DecodeCompatReasoning(%q) = false, want true", carry)
	}
	if field != "reasoning" || got != text {
		t.Fatalf("decoded (%q, %q), want (%q, %q)", field, got, "reasoning", text)
	}
	if came := EncodeCompatReasoning(field, got); came != carry {
		t.Fatalf("re-encoded %q, want %q", came, carry)
	}
	if got := EncodeCompatReasoning("reasoning", " \n\t "); got != "" {
		t.Fatalf("EncodeCompatReasoning of empty reasoning = %q, want empty", got)
	}
}

// The compatible payload is its own shape: another protocol never reads it as
// its blocks, and it is never mistaken for plain reasoning text — which is the
// only shape every protocol may replay.
func TestCompatReasoningPayloadIsItsOwn(t *testing.T) {
	carry := EncodeCompatReasoning("reasoning", "first thought\nsecond thought")
	if !StructuredCarry(carry) {
		t.Fatalf("StructuredCarry(%q) = false, want true", carry)
	}
	if _, ok := Decode(carry); ok {
		t.Fatal("Decode read the compatible payload as a Responses payload")
	}
	if _, ok := DecodeThinking(carry); ok {
		t.Fatal("DecodeThinking read the compatible payload as thinking blocks")
	}
	if _, ok := DecodeGeminiParts(carry); ok {
		t.Fatal("DecodeGeminiParts read the compatible payload as a part layout")
	}
	if got := ExtractSummary(carry); got != "first thought\nsecond thought" {
		t.Fatalf("ExtractSummary = %q, want the reasoning text", got)
	}
	for _, unwanted := range []string{compatReasoningPayloadVersion, "{", "reasoning\\"} {
		if strings.Contains(ExtractSummary(carry), unwanted) {
			t.Fatalf("ExtractSummary leaked %q: %q", unwanted, ExtractSummary(carry))
		}
	}

	plain := Prefix + "just thinking out loud"
	if field, text, ok := DecodeCompatReasoning(plain); !ok || field != CompatReasoningField || text != "just thinking out loud" {
		t.Fatalf("DecodeCompatReasoning(%q) = (%q, %q, %v), want the plain field", plain, field, text, ok)
	}
	others := []string{
		Encode([]Item{{ID: "rs_1", EncryptedContent: "opaque"}}),
		EncodeThinking([]ThinkingBlock{{Thinking: "thought", Signature: "sig=="}}),
		EncodeGeminiParts([]GeminiPart{{Kind: "thought", Text: "plan", Signature: "sig=="}}),
		"",
		"peer-session",
	}
	for _, name := range others {
		if field, text, ok := DecodeCompatReasoning(name); ok {
			t.Fatalf("DecodeCompatReasoning(%q) = (%q, %q, true), want false", name, field, text)
		}
	}
}

// The estimate may only count what a request sends: a carry's JSON envelope
// never reaches the wire, and a Gemini layout repeats answer text that Parts
// already holds.
func TestCarryEstimateText(t *testing.T) {
	const answer = "the answer text"
	gemini := EncodeGeminiParts([]GeminiPart{
		{Kind: "thought", Text: "first thought", Signature: "sig-thought=="},
		{Kind: "text", Text: answer, Signature: "sig-text=="},
		{Kind: "call", Call: 0, Signature: "sig-call=="},
	})
	tests := []struct {
		name     string
		carry    string
		want     []string
		unwanted []string
	}{
		{
			name:     "plain text",
			carry:    Prefix + "plain reasoning",
			want:     []string{"plain reasoning"},
			unwanted: []string{Prefix},
		},
		{
			name:     "compatible field",
			carry:    EncodeCompatReasoning("reasoning", "reasoning body"),
			want:     []string{"reasoning body"},
			unwanted: []string{compatReasoningPayloadVersion, "reasoning_content"},
		},
		{
			name:     "responses items",
			carry:    Encode([]Item{{ID: "rs_1", Summary: []string{"step one"}, EncryptedContent: "enc_one"}}),
			want:     []string{"step one", "enc_one"},
			unwanted: []string{payloadVersion, "rs_1", "items"},
		},
		{
			name:     "thinking blocks",
			carry:    EncodeThinking([]ThinkingBlock{{Thinking: "think", Signature: "sig-one=="}, {Redacted: true, Data: "cipher"}}),
			want:     []string{"think", "sig-one==", "cipher"},
			unwanted: []string{thinkingPayloadVersion, "redacted"},
		},
		{
			name:     "gemini parts",
			carry:    gemini,
			want:     []string{"first thought", "sig-thought==", "sig-text==", "sig-call=="},
			unwanted: []string{geminiPartsPayloadVersion, answer},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := CarryEstimateText(tc.carry)
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Fatalf("CarryEstimateText = %q, want it to contain %q", got, want)
				}
			}
			for _, unwanted := range tc.unwanted {
				if strings.Contains(got, unwanted) {
					t.Fatalf("CarryEstimateText leaked %q: %q", unwanted, got)
				}
			}
		})
	}

	// A Name that is not a carry is a participant name, and a request sends it.
	for _, name := range []string{"peer-session", ""} {
		if got := CarryEstimateText(name); got != name {
			t.Fatalf("CarryEstimateText(%q) = %q, want it unchanged", name, got)
		}
	}
}

func TestGeminiPartsCarryRoundTripsByteForByte(t *testing.T) {
	parts := []GeminiPart{
		{Kind: "thought", Text: "  Plan:\n\tstep \"one\" — back\\slash <>& 中文 🧠  ", Signature: "sig-thought+/=="},
		{Kind: "text", Text: "answer text", Signature: "sig-text+/=="},
		{Kind: "call", Call: 0, Signature: "sig-call-zero+/=="},
		{Kind: "call", Call: 2},
		{Kind: "text", Signature: "sig-only+/=="},
	}
	carry := EncodeGeminiParts(parts)
	if !strings.HasPrefix(carry, Prefix) {
		t.Fatalf("carry = %q, want the reasoning carry prefix", carry)
	}
	decoded, ok := DecodeGeminiParts(carry)
	if !ok {
		t.Fatalf("DecodeGeminiParts(%q) = false, want true", carry)
	}
	if !reflect.DeepEqual(decoded, parts) {
		t.Fatalf("decoded = %#v\nwant      %#v", decoded, parts)
	}
}

// The Gemini payload is a third structured format: never read as another
// protocol's payload, and its summary is the thought text alone.
func TestGeminiPartsCarryIsStructured(t *testing.T) {
	gemini := EncodeGeminiParts([]GeminiPart{
		{Kind: "thought", Text: "first thought", Signature: "sig-thought=="},
		{Kind: "text", Text: "the answer"},
		{Kind: "call", Call: 0, Signature: "sig-call=="},
	})
	if !StructuredCarry(gemini) {
		t.Fatalf("StructuredCarry(%q) = false, want true", gemini)
	}
	if _, ok := Decode(gemini); ok {
		t.Fatal("Decode read a Gemini payload as a Responses payload")
	}
	if _, ok := DecodeThinking(gemini); ok {
		t.Fatal("DecodeThinking read a Gemini payload as thinking blocks")
	}
	responses := Encode([]Item{{ID: "rs_1", EncryptedContent: "x"}})
	thinking := EncodeThinking([]ThinkingBlock{{Thinking: "thought", Signature: "sig=="}})
	for _, name := range []string{responses, thinking, Prefix + "plain", ""} {
		if _, ok := DecodeGeminiParts(name); ok {
			t.Fatalf("DecodeGeminiParts(%q) = true, want false", name)
		}
	}
	if got := ExtractSummary(gemini); got != "first thought" {
		t.Fatalf("ExtractSummary = %q, want the thought text alone", got)
	}
	for _, unwanted := range []string{"gemini_parts_v1", "sig-thought==", "sig-call==", "{", "the answer"} {
		if strings.Contains(ExtractSummary(gemini), unwanted) {
			t.Fatalf("ExtractSummary leaked %q: %q", unwanted, ExtractSummary(gemini))
		}
	}
	if got := ExtractSummary(EncodeGeminiParts([]GeminiPart{{Kind: "text", Text: "only an answer"}})); got != "" {
		t.Fatalf("ExtractSummary without a thought part = %q, want empty", got)
	}
}

// The fence is a stored form: sessions on disk already hold replies whose only
// text part is this exact block, so its bytes are pinned and must keep round
// tripping through the predicate every adapter now shares.
func TestThinkingFenceRoundTrip(t *testing.T) {
	if got, want := ThinkingFence("a"), "```thinking\na\n```"; got != want {
		t.Fatalf("ThinkingFence(\"a\") = %q, want %q", got, want)
	}
	for _, reasoning := range []string{"a", "多行\n思考", ""} {
		if !IsThinkingFence(ThinkingFence(reasoning)) {
			t.Fatalf("IsThinkingFence(ThinkingFence(%q)) = false, want true", reasoning)
		}
	}
	for _, text := range []string{"plain answer", "```thinking\nno close", "```go\nx\n```"} {
		if IsThinkingFence(text) {
			t.Fatalf("IsThinkingFence(%q) = true, want false", text)
		}
	}
}

// Only a reply whose entire content is the fence is the reply the model
// produced by reasoning alone. Anything else it said — a second part, an
// image, a tool call — makes it an answer, and an answer is always sent.
func TestThinkingFenceOnly(t *testing.T) {
	fence := Text(ThinkingFence("weigh A against B"))
	cases := []struct {
		name string
		msg  Message
		want bool
	}{
		{"fence alone", Message{Role: RoleAssistant, Parts: []ContentPart{fence}}, true},
		{"fence with tool call", Message{Role: RoleAssistant, Parts: []ContentPart{fence}, ToolCalls: []ToolCall{{ID: "call-1", Type: ToolTypeFunction}}}, false},
		{"fence with a second part", Message{Role: RoleAssistant, Parts: []ContentPart{fence, Text("so: A")}}, false},
		{"plain answer", Message{Role: RoleAssistant, Parts: []ContentPart{Text("A wins")}}, false},
		{"user wrote a fence", Message{Role: RoleUser, Parts: []ContentPart{fence}}, false},
		{"no parts", Message{Role: RoleAssistant}, false},
		{"fence with an image", Message{Role: RoleAssistant, Parts: []ContentPart{fence, ImageURL("https://example.invalid/x.png")}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ThinkingFenceOnly(tc.msg); got != tc.want {
				t.Fatalf("ThinkingFenceOnly(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// The producer is compared as a model id, so case and surrounding spaces do
// not matter; an absent producer is not a wildcard but the opposite — the
// fence goes to nobody, because nothing proves whose reasoning it is.
func TestThinkingFenceGoesTo(t *testing.T) {
	cases := []struct {
		name     string
		producer string
		model    string
		want     bool
	}{
		{"same model", "glm-5.3", "glm-5.3", true},
		{"same model, other case and spacing", "  GLM-5.3 ", "glm-5.3", true},
		{"another model", "glm-5.3", "deepseek-v4-flash", false},
		{"no producer", "", "glm-5.3", false},
		{"no requested model", "glm-5.3", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := Message{Role: RoleAssistant, Parts: []ContentPart{Text(ThinkingFence("weigh A against B"))}, ReasoningModel: tc.producer}
			if got := ThinkingFenceGoesTo(m, tc.model); got != tc.want {
				t.Fatalf("ThinkingFenceGoesTo(producer %q, model %q) = %v, want %v", tc.producer, tc.model, got, tc.want)
			}
		})
	}
}

// A refusal is recognized by status and keyword together: the status alone
// covers every 400 a provider sends, and the keyword alone covers failures at
// other statuses that happen to mention reasoning. Only both make a resend
// without the reasoning worthwhile.
func TestReasoningReplayRefused(t *testing.T) {
	apiErr := func(status int, message string) error {
		return &APIError{StatusCode: status, Message: message}
	}
	tests := []struct {
		name     string
		err      error
		statuses []int
		keywords []string
		want     bool
	}{
		{
			name:     "400 naming thinking",
			err:      apiErr(http.StatusBadRequest, `{"error":{"message":"messages.1.content.0.thinking: unsupported"}}`),
			statuses: []int{http.StatusBadRequest},
			keywords: []string{"thinking", "signature"},
			want:     true,
		},
		{
			name:     "status not in the set",
			err:      apiErr(http.StatusUnprocessableEntity, "reasoning is not supported"),
			statuses: []int{http.StatusBadRequest},
			keywords: []string{"reasoning"},
			want:     false,
		},
		{
			name:     "no keyword in the message",
			err:      apiErr(http.StatusBadRequest, "unknown field temperature"),
			statuses: []int{http.StatusBadRequest},
			keywords: []string{"reasoning"},
			want:     false,
		},
		{
			name:     "keyword in the code",
			err:      &APIError{StatusCode: http.StatusBadRequest, Code: "Encrypted_Content_Invalid"},
			statuses: []int{http.StatusBadRequest},
			keywords: []string{"encrypted", "rs_"},
			want:     true,
		},
		{
			name:     "keyword in the type",
			err:      &APIError{StatusCode: http.StatusBadRequest, Type: "invalid_request_thought_signature"},
			statuses: []int{http.StatusBadRequest},
			keywords: []string{"thought_signature"},
			want:     true,
		},
		{
			name:     "second status matches",
			err:      apiErr(http.StatusNotFound, "Item with id 'rs_abc' not found"),
			statuses: []int{http.StatusBadRequest, http.StatusNotFound},
			keywords: []string{"rs_"},
			want:     true,
		},
		{
			name:     "not an API error",
			err:      errors.New("400 thinking"),
			statuses: []int{http.StatusBadRequest},
			keywords: []string{"thinking"},
			want:     false,
		},
		{
			name:     "no error",
			err:      nil,
			statuses: []int{http.StatusBadRequest},
			keywords: []string{"thinking"},
			want:     false,
		},
		{
			name:     "wrapped by the execute error",
			err:      NewExecuteError(apiErr(http.StatusBadRequest, "signature mismatch"), &Usage{InputTokens: 1}),
			statuses: []int{http.StatusBadRequest},
			keywords: []string{"signature"},
			want:     true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ReasoningReplayRefused(tc.err, tc.statuses, tc.keywords...); got != tc.want {
				t.Fatalf("ReasoningReplayRefused(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// The resend drops the reasoning and keeps everything else: every protocol's
// carry goes, the producer that other rules read stays, and a participant name
// was never reasoning. The caller's slice is untouched — the request that was
// refused is the one it built, and the retry layers above read it again.
func TestWithoutReasoningCarries(t *testing.T) {
	carries := []string{
		Prefix + "plain reasoning",
		Encode([]Item{{ID: "rs_1", Summary: []string{"step"}, EncryptedContent: "opaque"}}),
		EncodeThinking([]ThinkingBlock{{Thinking: "thought", Signature: "sig=="}}),
		EncodeGeminiParts([]GeminiPart{{Kind: "thought", Text: "plan", Signature: "sig=="}}),
	}
	messages := []Message{
		{Role: RoleSystem, Parts: []ContentPart{Text("be brief")}},
		UserMessage(Text("hi")),
	}
	for i, carry := range carries {
		messages = append(messages, Message{
			Role:           RoleAssistant,
			Parts:          []ContentPart{Text(fmt.Sprintf("answer %d", i))},
			Name:           carry,
			ReasoningModel: "glm-5.3",
		})
	}
	messages = append(messages,
		Message{Role: RoleAssistant, Parts: []ContentPart{Text("named")}, Name: "peer-session"},
		Message{Role: RoleTool, ToolCallID: "call-1", Parts: []ContentPart{Text("ok")}},
	)
	before := make([]Message, len(messages))
	copy(before, messages)

	stripped := WithoutReasoningCarries(messages)
	if len(stripped) != len(messages) {
		t.Fatalf("message count = %d, want %d", len(stripped), len(messages))
	}
	for i, msg := range stripped {
		if !strings.HasPrefix(before[i].Name, Prefix) {
			if msg.Name != before[i].Name {
				t.Fatalf("message %d name = %q, want %q untouched", i, msg.Name, before[i].Name)
			}
			continue
		}
		if msg.Name != "" {
			t.Fatalf("message %d kept carry %q", i, msg.Name)
		}
		if msg.ReasoningModel != "glm-5.3" {
			t.Fatalf("message %d reasoning model = %q, want the producer kept", i, msg.ReasoningModel)
		}
	}
	for i := range messages {
		if messages[i].Name != before[i].Name {
			t.Fatalf("input message %d was modified: %q, want %q", i, messages[i].Name, before[i].Name)
		}
	}
}
