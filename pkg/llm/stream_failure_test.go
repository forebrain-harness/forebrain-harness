package llm

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestClassifyStreamFailure(t *testing.T) {
	t.Parallel()

	opErr := &net.OpError{Op: "read", Net: "tcp", Err: errors.New("connection reset")}
	tests := []struct {
		name string
		err  error
		snap StreamProgressSnapshot
		want StreamFailureCause
	}{
		{name: "529 status is overloaded", err: &APIError{StatusCode: 529, Message: "overloaded"}, want: CauseOverloaded},
		{name: "overloaded_error type", err: &APIError{Type: "Overloaded_Error "}, want: CauseOverloaded},
		{name: "timeout_error type", err: &APIError{Type: "timeout_error"}, want: CauseTimedOut},
		{name: "deadline_exceeded type", err: &APIError{Type: "deadline_exceeded"}, want: CauseTimedOut},
		{name: "503 status", err: &APIError{StatusCode: http.StatusServiceUnavailable}, want: CauseServerError},
		{name: "api_error type with status 0", err: &APIError{Type: "api_error"}, want: CauseServerError},
		{name: "server_error type", err: &APIError{Type: "server_error"}, want: CauseServerError},
		{name: "internal type", err: &APIError{Type: "internal"}, want: CauseServerError},
		{name: "unavailable type", err: &APIError{Type: "unavailable"}, want: CauseServerError},
		{name: "stall", err: &StreamStallError{After: time.Second}, want: CauseStalled},
		{name: "stall wrapped", err: fmt.Errorf("watchdog: %w", &StreamStallError{After: time.Second}), want: CauseStalled},
		{name: "net op error", err: opErr, want: CauseConnectionLost},
		{name: "truncated", err: fmt.Errorf("gpt-main: %w", ErrStreamTruncated), want: CauseTruncated},
		{name: "connection lost after sleep", err: opErr, snap: StreamProgressSnapshot{Slept: 6 * time.Second}, want: CauseSuspended},
		{name: "json syntax error", err: fmt.Errorf("decode: %w", &json.SyntaxError{}), want: CauseMalformed},
		{name: "json type error", err: fmt.Errorf("decode: %w", &json.UnmarshalTypeError{Value: "x"}), want: CauseMalformed},
		{name: "400 api error", err: &APIError{StatusCode: http.StatusBadRequest}, want: CauseBadRequest},
		{name: "unknown error", err: errors.New("x"), want: CauseUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyStreamFailure(tt.err, tt.snap); got != tt.want {
				t.Fatalf("ClassifyStreamFailure(%v) = %q, want %q", tt.err, got, tt.want)
			}
		})
	}
}

func TestDecideStreamFailure(t *testing.T) {
	t.Parallel()

	policy := streamRetryPolicy{maxRetries: 10, maxSleptThrough: 10, maxOverloaded: 3, allowNonStreaming: true}
	oneRetry := streamRetryPolicy{maxRetries: 1, maxSleptThrough: 1, maxOverloaded: 3, allowNonStreaming: true}
	noFallback := streamRetryPolicy{maxRetries: 10, maxSleptThrough: 10, maxOverloaded: 3, allowNonStreaming: false}

	nothing := StreamProgressSnapshot{Kind: ProgressNothing}
	started := StreamProgressSnapshot{Kind: ProgressStarted}
	partial := StreamProgressSnapshot{Kind: ProgressPartialOutput}
	thinking := StreamProgressSnapshot{Kind: ProgressThinkingOnly}
	output := StreamProgressSnapshot{Kind: ProgressOutput}

	tests := []struct {
		name      string
		cause     StreamFailureCause
		snap      StreamProgressSnapshot
		outlasted bool
		counts    streamRetryCounts
		policy    streamRetryPolicy
		want      streamDecision
	}{
		// ProgressOutput: bad request and unknown fail, everything else
		// finishes from what completed.
		{name: "output bad request fails", cause: CauseBadRequest, snap: output, policy: policy, want: failWith(streamRetryCounts{})},
		{name: "output unknown fails", cause: CauseUnknown, snap: output, policy: policy, want: failWith(streamRetryCounts{})},
		{name: "output connection lost keeps partial", cause: CauseConnectionLost, snap: output, policy: policy, want: keepWith(streamRetryCounts{})},
		{name: "output server error keeps partial", cause: CauseServerError, snap: output, policy: policy, want: keepWith(streamRetryCounts{})},
		{name: "output stalled keeps partial", cause: CauseStalled, snap: output, policy: policy, want: keepWith(streamRetryCounts{})},

		// ProgressThinkingOnly.
		{name: "thinking stalled with stop reason keeps partial", cause: CauseStalled, snap: StreamProgressSnapshot{Kind: ProgressThinkingOnly, StopReasonReceived: true}, policy: policy, want: keepWith(streamRetryCounts{})},
		{name: "thinking stalled retries once", cause: CauseStalled, snap: thinking, policy: policy, want: streamDecision{DecideRetry, streamRetryCounts{retries: 1, stalls: 1}}},
		{name: "thinking stalled twice keeps partial", cause: CauseStalled, snap: thinking, counts: streamRetryCounts{stalls: 1}, policy: policy, want: keepWith(streamRetryCounts{stalls: 1})},
		{name: "thinking connection lost with stop reason keeps partial", cause: CauseConnectionLost, snap: StreamProgressSnapshot{Kind: ProgressThinkingOnly, StopReasonReceived: true}, policy: policy, want: keepWith(streamRetryCounts{})},
		{name: "thinking connection lost retries", cause: CauseConnectionLost, snap: thinking, policy: policy, want: streamDecision{DecideRetry, streamRetryCounts{retries: 1, afterThinkingOnly: 1}}},
		{name: "thinking truncated retries on the same counter", cause: CauseTruncated, snap: thinking, policy: policy, want: streamDecision{DecideRetry, streamRetryCounts{retries: 1, afterThinkingOnly: 1}}},
		{name: "thinking malformed retries on the same counter", cause: CauseMalformed, snap: thinking, policy: policy, want: streamDecision{DecideRetry, streamRetryCounts{retries: 1, afterThinkingOnly: 1}}},
		{name: "thinking connection lost twice keeps partial", cause: CauseConnectionLost, snap: thinking, counts: streamRetryCounts{afterThinkingOnly: 2}, policy: policy, want: keepWith(streamRetryCounts{afterThinkingOnly: 2})},
		{name: "thinking server error with stop reason fails", cause: CauseServerError, snap: StreamProgressSnapshot{Kind: ProgressThinkingOnly, StopReasonReceived: true}, policy: policy, want: failWith(streamRetryCounts{})},
		{name: "thinking server error retries twice", cause: CauseServerError, snap: thinking, policy: policy, want: streamDecision{DecideRetry, streamRetryCounts{retries: 1, afterThinkingOnly: 1}}},
		{name: "thinking server error three times fails", cause: CauseServerError, snap: thinking, counts: streamRetryCounts{afterThinkingOnly: 2}, policy: policy, want: failWith(streamRetryCounts{afterThinkingOnly: 2})},
		{name: "thinking timed out retries once", cause: CauseTimedOut, snap: thinking, policy: policy, want: streamDecision{DecideRetry, streamRetryCounts{retries: 1, afterThinkingOnly: 1}}},
		{name: "thinking timed out twice fails", cause: CauseTimedOut, snap: thinking, counts: streamRetryCounts{afterThinkingOnly: 1}, policy: policy, want: failWith(streamRetryCounts{afterThinkingOnly: 1})},
		{name: "thinking overloaded with stop reason fails", cause: CauseOverloaded, snap: StreamProgressSnapshot{Kind: ProgressThinkingOnly, StopReasonReceived: true}, policy: policy, want: failWith(streamRetryCounts{})},
		{name: "thinking overloaded retries", cause: CauseOverloaded, snap: thinking, policy: policy, want: streamDecision{DecideRetry, streamRetryCounts{retries: 1, overloaded: 1}}},
		{name: "thinking bad request fails", cause: CauseBadRequest, snap: thinking, policy: policy, want: failWith(streamRetryCounts{})},

		// Overload on nothing/started/partial.
		{name: "overloaded on partial output falls back without streaming", cause: CauseOverloaded, snap: partial, policy: policy, want: streamDecision{DecideRetryWithoutStreaming, streamRetryCounts{retries: 1, overloaded: 1, triedWithoutStreaming: true}}},
		{name: "overloaded on nothing retries", cause: CauseOverloaded, snap: nothing, policy: policy, want: streamDecision{DecideRetry, streamRetryCounts{retries: 1, overloaded: 1}}},
		{name: "overloaded three times falls back", cause: CauseOverloaded, snap: nothing, counts: streamRetryCounts{overloaded: 2}, policy: policy, want: streamDecision{DecideRetryWithoutStreaming, streamRetryCounts{retries: 1, overloaded: 3, triedWithoutStreaming: true}}},

		// Stalled below thinking.
		{name: "stalled on nothing retries once", cause: CauseStalled, snap: nothing, policy: policy, want: streamDecision{DecideRetry, streamRetryCounts{retries: 1, stalls: 1}}},
		{name: "stalled twice on nothing falls back", cause: CauseStalled, snap: nothing, counts: streamRetryCounts{stalls: 1}, policy: policy, want: streamDecision{DecideRetryWithoutStreaming, streamRetryCounts{retries: 1, stalls: 1, triedWithoutStreaming: true}}},
		{name: "stalled on started falls back", cause: CauseStalled, snap: started, policy: policy, want: streamDecision{DecideRetryWithoutStreaming, streamRetryCounts{retries: 1, triedWithoutStreaming: true}}},

		// Truncated.
		{name: "truncated with stop reason falls back", cause: CauseTruncated, snap: StreamProgressSnapshot{Kind: ProgressStarted, StopReasonReceived: true}, policy: policy, want: streamDecision{DecideRetryWithoutStreaming, streamRetryCounts{retries: 1, triedWithoutStreaming: true}}},
		{name: "truncated retries once", cause: CauseTruncated, snap: started, policy: policy, want: streamDecision{DecideRetry, streamRetryCounts{retries: 1, truncations: 1}}},
		{name: "truncated twice falls back", cause: CauseTruncated, snap: started, counts: streamRetryCounts{truncations: 1}, policy: policy, want: streamDecision{DecideRetryWithoutStreaming, streamRetryCounts{retries: 1, truncations: 1, triedWithoutStreaming: true}}},

		// Suspended.
		{name: "suspended with stop reason falls back", cause: CauseSuspended, snap: StreamProgressSnapshot{Kind: ProgressStarted, StopReasonReceived: true}, policy: policy, want: streamDecision{DecideRetryWithoutStreaming, streamRetryCounts{retries: 1, triedWithoutStreaming: true}}},
		{name: "suspended on partial output retries", cause: CauseSuspended, snap: partial, policy: policy, want: streamDecision{DecideRetry, streamRetryCounts{retries: 1}}},
		{name: "suspended on partial output spends the shared budget", cause: CauseSuspended, snap: partial, counts: streamRetryCounts{retries: 10}, policy: policy, want: streamDecision{DecideRetryWithoutStreaming, streamRetryCounts{retries: 10, triedWithoutStreaming: true}}},
		{name: "suspended on nothing sleeps through", cause: CauseSuspended, snap: nothing, policy: policy, want: streamDecision{DecideRetry, streamRetryCounts{sleptThrough: 1}}},
		{name: "slept through enough retries on the shared budget", cause: CauseSuspended, snap: nothing, counts: streamRetryCounts{sleptThrough: 10}, policy: policy, want: streamDecision{DecideRetry, streamRetryCounts{retries: 1, sleptThrough: 10}}},
		{name: "slept through with the shared budget spent falls back", cause: CauseSuspended, snap: nothing, counts: streamRetryCounts{sleptThrough: 10, retries: 10}, policy: policy, want: streamDecision{DecideRetryWithoutStreaming, streamRetryCounts{retries: 10, sleptThrough: 10, triedWithoutStreaming: true}}},

		// Connection lost below thinking.
		{name: "connection lost with stop reason falls back", cause: CauseConnectionLost, snap: StreamProgressSnapshot{Kind: ProgressStarted, StopReasonReceived: true}, policy: policy, want: streamDecision{DecideRetryWithoutStreaming, streamRetryCounts{retries: 1, triedWithoutStreaming: true}}},
		{name: "connection lost retries on the shared budget", cause: CauseConnectionLost, snap: started, policy: policy, want: streamDecision{DecideRetry, streamRetryCounts{retries: 1}}},
		{name: "connection lost with the shared budget spent falls back", cause: CauseConnectionLost, snap: started, counts: streamRetryCounts{retries: 10}, policy: policy, want: streamDecision{DecideRetryWithoutStreaming, streamRetryCounts{retries: 10, triedWithoutStreaming: true}}},

		// Server error below thinking.
		{name: "server error outlasted retries", cause: CauseServerError, snap: nothing, outlasted: true, policy: policy, want: streamDecision{DecideRetry, streamRetryCounts{retries: 1, afterThinkingOnly: 1}}},
		{name: "server error outlasted twice falls back", cause: CauseServerError, snap: nothing, outlasted: true, counts: streamRetryCounts{afterThinkingOnly: 2}, policy: policy, want: streamDecision{DecideRetryWithoutStreaming, streamRetryCounts{retries: 1, afterThinkingOnly: 2, triedWithoutStreaming: true}}},
		{name: "server error not outlasted falls back", cause: CauseServerError, snap: nothing, policy: policy, want: streamDecision{DecideRetryWithoutStreaming, streamRetryCounts{retries: 1, triedWithoutStreaming: true}}},
		{name: "server error with stop reason falls back", cause: CauseServerError, snap: StreamProgressSnapshot{Kind: ProgressNothing, StopReasonReceived: true}, outlasted: true, policy: policy, want: streamDecision{DecideRetryWithoutStreaming, streamRetryCounts{retries: 1, triedWithoutStreaming: true}}},
		{name: "server error on partial output falls back", cause: CauseServerError, snap: partial, outlasted: true, policy: policy, want: streamDecision{DecideRetryWithoutStreaming, streamRetryCounts{retries: 1, triedWithoutStreaming: true}}},

		// Timed out, malformed, bad request, unknown below thinking.
		{name: "timed out falls back", cause: CauseTimedOut, snap: started, policy: policy, want: streamDecision{DecideRetryWithoutStreaming, streamRetryCounts{retries: 1, triedWithoutStreaming: true}}},
		{name: "malformed falls back", cause: CauseMalformed, snap: started, policy: policy, want: streamDecision{DecideRetryWithoutStreaming, streamRetryCounts{retries: 1, triedWithoutStreaming: true}}},
		{name: "bad request falls back", cause: CauseBadRequest, snap: started, policy: policy, want: streamDecision{DecideRetryWithoutStreaming, streamRetryCounts{retries: 1, triedWithoutStreaming: true}}},
		{name: "unknown falls back", cause: CauseUnknown, snap: started, policy: policy, want: streamDecision{DecideRetryWithoutStreaming, streamRetryCounts{retries: 1, triedWithoutStreaming: true}}},

		// The shared budget spent AND the non-streaming fallback already
		// tried: the decision is to fail.
		{name: "connection lost after everything was tried fails", cause: CauseConnectionLost, snap: started, counts: streamRetryCounts{retries: 10, triedWithoutStreaming: true}, policy: policy, want: failWith(streamRetryCounts{retries: 10, triedWithoutStreaming: true})},
		{name: "exhausted small budget fails after the non-streaming attempt", cause: CauseConnectionLost, snap: started, counts: streamRetryCounts{retries: 1, triedWithoutStreaming: true}, policy: oneRetry, want: failWith(streamRetryCounts{retries: 1, triedWithoutStreaming: true})},

		// Non-streaming resend not allowed: a retry the budget still owes is
		// spent as a stream; the fallback the table would reach is failure.
		{name: "no non-streaming fallback still retries connection lost", cause: CauseConnectionLost, snap: started, policy: noFallback, want: streamDecision{DecideRetry, streamRetryCounts{retries: 1}}},
		{name: "no non-streaming fallback fails connection lost with the budget spent", cause: CauseConnectionLost, snap: started, counts: streamRetryCounts{retries: 10}, policy: noFallback, want: failWith(streamRetryCounts{retries: 10})},
		{name: "no non-streaming fallback fails truncated at its limit", cause: CauseTruncated, snap: started, counts: streamRetryCounts{truncations: 1}, policy: noFallback, want: failWith(streamRetryCounts{truncations: 1})},
		{name: "no non-streaming fallback fails server error at its limit", cause: CauseServerError, snap: nothing, outlasted: true, counts: streamRetryCounts{afterThinkingOnly: 2}, policy: noFallback, want: failWith(streamRetryCounts{afterThinkingOnly: 2})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decideStreamFailure(tt.cause, tt.snap, tt.outlasted, tt.counts, tt.policy)
			if got != tt.want {
				t.Fatalf("decideStreamFailure(%s, %+v, outlasted=%v, counts=%+v) = %+v, want %+v", tt.cause, tt.snap, tt.outlasted, tt.counts, got, tt.want)
			}
		})
	}
}

func TestCompleteDespiteError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		cause StreamFailureCause
		snap  StreamProgressSnapshot
		want  bool
	}{
		{name: "incomplete connection lost", cause: CauseConnectionLost, snap: StreamProgressSnapshot{Kind: ProgressOutput}, want: false},
		{name: "complete connection lost", cause: CauseConnectionLost, snap: StreamProgressSnapshot{Kind: ProgressOutput, Complete: true}, want: true},
		{name: "complete suspended", cause: CauseSuspended, snap: StreamProgressSnapshot{Kind: ProgressStarted, Complete: true}, want: true},
		{name: "complete truncated", cause: CauseTruncated, snap: StreamProgressSnapshot{Kind: ProgressPartialOutput, Complete: true}, want: true},
		{name: "complete stalled", cause: CauseStalled, snap: StreamProgressSnapshot{Kind: ProgressThinkingOnly, Complete: true}, want: true},
		{name: "complete server error after output", cause: CauseServerError, snap: StreamProgressSnapshot{Kind: ProgressOutput, Complete: true}, want: true},
		{name: "complete server error below output", cause: CauseServerError, snap: StreamProgressSnapshot{Kind: ProgressStarted, Complete: true}, want: false},
		{name: "complete overload after output", cause: CauseOverloaded, snap: StreamProgressSnapshot{Kind: ProgressOutput, Complete: true}, want: true},
		{name: "complete bad request", cause: CauseBadRequest, snap: StreamProgressSnapshot{Kind: ProgressOutput, Complete: true}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := completeDespiteError(tt.cause, tt.snap); got != tt.want {
				t.Fatalf("completeDespiteError(%s, %+v) = %v, want %v", tt.cause, tt.snap, got, tt.want)
			}
		})
	}
}

func TestRequestLevelDecision(t *testing.T) {
	t.Parallel()

	policy := streamRetryPolicy{maxRetries: 10, maxSleptThrough: 10, maxOverloaded: 3, allowNonStreaming: true}
	nothing := StreamProgressSnapshot{Kind: ProgressNothing}
	nxdomain := &net.DNSError{Err: "no such host", Name: "api.example.invalid", IsNotFound: true}

	tests := []struct {
		name   string
		err    error
		snap   StreamProgressSnapshot
		counts streamRetryCounts
		pass   bool
		ok     bool
		want   streamDecision
	}{
		{name: "529 passes to the overload layer", err: &APIError{StatusCode: 529}, snap: nothing, pass: true, ok: true},
		{name: "408 retries", err: &APIError{StatusCode: http.StatusRequestTimeout}, snap: nothing, ok: true, want: streamDecision{DecideRetry, streamRetryCounts{retries: 1}}},
		{name: "409 retries", err: &APIError{StatusCode: http.StatusConflict}, snap: nothing, ok: true, want: streamDecision{DecideRetry, streamRetryCounts{retries: 1}}},
		{name: "503 retries", err: &APIError{StatusCode: http.StatusServiceUnavailable}, snap: nothing, ok: true, want: streamDecision{DecideRetry, streamRetryCounts{retries: 1}}},
		{name: "503 with the budget spent fails", err: &APIError{StatusCode: http.StatusServiceUnavailable}, snap: nothing, counts: streamRetryCounts{retries: 10}, ok: true, want: failWith(streamRetryCounts{retries: 10})},
		{name: "404 falls back without streaming", err: &APIError{StatusCode: http.StatusNotFound}, snap: nothing, ok: true, want: streamDecision{DecideRetryWithoutStreaming, streamRetryCounts{retries: 1, triedWithoutStreaming: true}}},
		{name: "404 after the non-streaming attempt passes", err: &APIError{StatusCode: http.StatusNotFound}, snap: nothing, counts: streamRetryCounts{triedWithoutStreaming: true}, pass: true, ok: true},
		{name: "400 passes untouched", err: &APIError{StatusCode: http.StatusBadRequest}, snap: nothing, pass: true, ok: true},
		{name: "dns failure passes untouched", err: nxdomain, snap: nothing, pass: true, ok: true},
		{name: "certificate failure passes untouched", err: x509.HostnameError{Host: "api.example"}, snap: nothing, pass: true, ok: true},
		{name: "transport error without a status is not request-level", err: &net.OpError{Op: "read", Net: "tcp", Err: errors.New("reset")}, snap: nothing, ok: false},
		{name: "408 after output is not request-level", err: &APIError{StatusCode: http.StatusRequestTimeout}, snap: StreamProgressSnapshot{Kind: ProgressOutput}, ok: false},
		{name: "408 on started is not request-level", err: &APIError{StatusCode: http.StatusRequestTimeout}, snap: StreamProgressSnapshot{Kind: ProgressStarted}, ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dec, pass, ok := requestLevelDecision(tt.err, tt.snap, tt.counts, policy)
			if ok != tt.ok || pass != tt.pass {
				t.Fatalf("requestLevelDecision(%v) ok/pass = %v/%v, want %v/%v", tt.err, ok, pass, tt.ok, tt.pass)
			}
			if ok && dec != tt.want {
				t.Fatalf("requestLevelDecision(%v) = %+v, want %+v", tt.err, dec, tt.want)
			}
		})
	}
}

func TestIncompleteSentence(t *testing.T) {
	t.Parallel()

	shown := []struct {
		cause StreamFailureCause
		want  string
	}{
		{CauseStalled, "The response stopped arriving. The response above may be incomplete."},
		{CauseServerError, "Server error mid-response. The response above may be incomplete."},
		{CauseOverloaded, "Server error mid-response. The response above may be incomplete."},
		{CauseTimedOut, "Server error mid-response. The response above may be incomplete."},
		{CauseMalformed, "The response stream was malformed. The response above may be incomplete."},
		{CauseSuspended, "Your computer went to sleep mid-response. The response above may be incomplete."},
		{CauseConnectionLost, "Connection lost mid-response. The response above may be incomplete."},
		{CauseTruncated, "Connection lost mid-response. The response above may be incomplete."},
	}
	for _, tt := range shown {
		if got := IncompleteSentence(tt.cause, true); got != tt.want {
			t.Errorf("IncompleteSentence(%s, shown) = %q, want %q", tt.cause, got, tt.want)
		}
	}
	hidden := []struct {
		cause StreamFailureCause
		want  string
	}{
		{CauseStalled, "The response stalled before a response was produced. Try again."},
		{CauseMalformed, "The response stream was malformed and no response was produced. Try again."},
		{CauseSuspended, "Your computer went to sleep before a response was produced. Try again."},
		{CauseServerError, "Connection lost before a response was produced. Try again."},
		{CauseConnectionLost, "Connection lost before a response was produced. Try again."},
		{CauseTruncated, "Connection lost before a response was produced. Try again."},
	}
	for _, tt := range hidden {
		if got := IncompleteSentence(tt.cause, false); got != tt.want {
			t.Errorf("IncompleteSentence(%s, hidden) = %q, want %q", tt.cause, got, tt.want)
		}
	}
}

func TestResendSafeAndResetFromContext(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	if !ResendSafe(ctx) {
		t.Fatal("a context with no progress record is resend-safe")
	}
	ResetStreamProgress(ctx) // must not panic

	sink := &StreamSink{OnDelta: func(string) {}}
	ctx, _ = WithStreamProgress(WithStreamSink(ctx, sink))
	if !ResendSafe(ctx) {
		t.Fatal("a fresh record is resend-safe")
	}
	// One finished text block makes the request one a resend would duplicate.
	StreamSinkFrom(ctx).OnDelta("answer text")
	StreamSinkFrom(ctx).OnTextBlockDone()
	if ResendSafe(ctx) {
		t.Fatal("a request with a finished output block is not resend-safe")
	}
	ResetStreamProgress(ctx)
	if !ResendSafe(ctx) {
		t.Fatal("reset must make the request resend-safe again")
	}
}
