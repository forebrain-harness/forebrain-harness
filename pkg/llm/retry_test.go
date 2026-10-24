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
