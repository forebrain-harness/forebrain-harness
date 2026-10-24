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
	"net"
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

func TestNewRetry_NotifyPayloadAndExhausted(t *testing.T) {
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

	// Three pre-sleep notifications (retry 1, 2, 3) plus one exhausted.
	if len(got) != 4 {
		t.Fatalf("notifications = %d, want 4: %+v", len(got), got)
	}
	for i, tr := range got[:3] {
		if tr.Retry != i+1 || tr.Total != 3 {
			t.Fatalf("notification %d: Retry=%d Total=%d, want %d/3", i, tr.Retry, tr.Total, i+1)
		}
		if tr.Exhausted {
			t.Fatalf("notification %d must not be Exhausted", i)
		}
		if tr.Err != boom {
			t.Fatalf("notification %d: Err = %v, want the failure", i, tr.Err)
		}
	}
	last := got[3]
	if !last.Exhausted || last.Retry != 3 || last.Total != 3 || last.Wait != 0 || last.Err != boom {
		t.Fatalf("exhausted notification = %+v, want Exhausted 3/3 wait 0 with the last error", last)
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

func TestRetryBackoffUnboundedVersusCapped(t *testing.T) {
	run := func(unbounded bool) []TransportRetry {
		inner := &stubLLM{fn: func(_ context.Context, _ []Message, _ []*Tool) (*Result, error) {
			return nil, errors.New("transient")
		}}
		var notes []TransportRetry
		cfg := &retryConfig{
			maxAttempts:    4,
			initialBackoff: time.Millisecond,
			maxBackoff:     2 * time.Millisecond,
			shouldRetry:    func(error) bool { return true },
			notify:         func(_ context.Context, tr TransportRetry) { notes = append(notes, tr) },
		}
		if unbounded {
			WithUnboundedBackoff()(cfg)
		}
		if _, err := (&retryLLM{inner: inner, cfg: cfg}).Execute(context.Background(), nil, nil); err == nil {
			t.Fatal("expected exhaustion error")
		}
		return notes
	}

	// Jitter keeps each wait within [75%, 125%] of its back-off step, so the
	// capped run saturates near 2ms while the unbounded run keeps doubling
	// past it (3rd wait ≈4ms). Each list also carries the trailing exhausted
	// notification (Wait=0).
	waits := func(run []TransportRetry) []time.Duration {
		var out []time.Duration
		for _, tr := range run {
			if !tr.Exhausted {
				out = append(out, tr.Wait)
			}
		}
		return out
	}
	cappedAll := run(false)
	if len(cappedAll) != 4 || !cappedAll[3].Exhausted {
		t.Fatalf("capped notifications = %+v, want 3 waits plus the exhausted one", cappedAll)
	}
	capped := waits(cappedAll)
	if capped[2] > 2500*time.Microsecond {
		t.Fatalf("capped waits = %v, want the third one at the 2ms ceiling", capped)
	}
	unbounded := waits(run(true))
	if len(unbounded) != 3 {
		t.Fatalf("unbounded waits = %v, want 3 entries", unbounded)
	}
	if unbounded[2] < 2800*time.Microsecond {
		t.Fatalf("unbounded waits = %v, want the third one doubling past the ceiling (≈4ms)", unbounded)
	}
}
