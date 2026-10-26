package llm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"reflect"
	"testing"
	"time"
)

// streamRetryRecorder collects what the retry loop announced on the sink.
type streamRetryRecorder struct {
	retries []TransportRetry
	notices []ResponseNotice
}

func (r *streamRetryRecorder) cleared() int {
	n := 0
	for _, tr := range r.retries {
		if tr.Cleared {
			n++
		}
	}
	return n
}

func (r *streamRetryRecorder) live() []TransportRetry {
	var out []TransportRetry
	for _, tr := range r.retries {
		if !tr.Cleared {
			out = append(out, tr)
		}
	}
	return out
}

func (r *streamRetryRecorder) droppedThinking() int {
	n := 0
	for _, tr := range r.retries {
		if tr.DropFinishedThinking {
			n++
		}
	}
	return n
}

// scriptedStreamLLM runs one scripted attempt per Execute call, recording
// what the attempt's context carried.
type scriptedStreamLLM struct {
	attempts       []func(ctx context.Context) (*Result, error)
	calls          int
	nonStreaming   []bool
	progressAtCall []StreamProgressKind
}

func (s *scriptedStreamLLM) Execute(ctx context.Context, messages []Message, tools []*Tool) (*Result, error) {
	call := s.calls
	s.calls++
	s.nonStreaming = append(s.nonStreaming, NonStreamingRequestFrom(ctx))
	kind := ProgressNothing
	if p := StreamProgressFrom(ctx); p != nil {
		kind = p.Snapshot().Kind
	}
	s.progressAtCall = append(s.progressAtCall, kind)
	if call >= len(s.attempts) {
		return nil, errors.New("script exhausted")
	}
	return s.attempts[call](ctx)
}

// runStreamRetry drives one streamRetry Execute over the scripted attempts.
func runStreamRetry(t *testing.T, maxRetries int, attempts ...func(ctx context.Context) (*Result, error)) (*Result, error, *scriptedStreamLLM, *streamRetryRecorder) {
	t.Helper()
	rec := &streamRetryRecorder{}
	sink := &StreamSink{
		OnDelta:          func(string) {},
		OnReasoningDelta: func(string) {},
		OnReasoningDone:  func() {},
		OnTextBlockDone:  func() {},
		OnToolCallReady:  func(ToolCall) {},
		OnMessageStop:    func() {},
		OnTransportRetry: func(tr TransportRetry) { rec.retries = append(rec.retries, tr) },
		OnResponseNotice: func(n ResponseNotice) { rec.notices = append(rec.notices, n) },
	}
	ctx, _ := WithStreamProgress(WithStreamSink(context.Background(), sink))
	inner := &scriptedStreamLLM{attempts: attempts}
	mw := NewStreamRetry(maxRetries, WithRetryNotify(func(ctx context.Context, tr TransportRetry) {
		if s := StreamSinkFrom(ctx); s != nil && s.OnTransportRetry != nil {
			s.OnTransportRetry(tr)
		}
	}))
	result, err := Use(inner, mw).Execute(ctx, []Message{UserMessage(Text("hi"))}, nil)
	return result, err, inner, rec
}

func connDropped() error {
	return &net.OpError{Op: "read", Net: "tcp", Err: errors.New("connection reset by peer")}
}

func okResult(text string) (*Result, error) {
	msg := AssistantMessage([]ContentPart{Text(text)})
	return &Result{Message: &msg}, nil
}

func streamFire(ctx context.Context, fire func(sink *StreamSink)) {
	if sink := StreamSinkFrom(ctx); sink != nil {
		fire(sink)
	}
}

// Thinking turned into answer text, then the connection dropped: the answer
// that happened is finished, never re-sent.
func TestStreamRetryKeepsPartialAfterThinkingAndText(t *testing.T) {
	result, err, inner, rec := runStreamRetry(t, 10, func(ctx context.Context) (*Result, error) {
		streamFire(ctx, func(sink *StreamSink) {
			sink.OnReasoningDelta("thinking")
			sink.OnDelta("answer text")
		})
		return nil, connDropped()
	})
	var partial *PartialResponseError
	if !errors.As(err, &partial) {
		t.Fatalf("error = %v, want a PartialResponseError", err)
	}
	if partial.Complete || partial.Failed {
		t.Fatalf("partial = complete:%v failed:%v, want plain keep-partial", partial.Complete, partial.Failed)
	}
	if partial.Cause != CauseConnectionLost {
		t.Fatalf("cause = %q, want connection_lost", partial.Cause)
	}
	if inner.calls != 1 {
		t.Fatalf("inner calls = %d, want 1 (no resend)", inner.calls)
	}
	if rec.cleared() != 1 || rec.droppedThinking() != 0 {
		t.Fatalf("notices = %+v, want exactly one Cleared without DropFinishedThinking", rec.retries)
	}
	if result != nil {
		t.Fatalf("result = %#v, want nil", result)
	}
}

// Only answer text was streaming when the connection dropped: the request is
// re-sent, and the second attempt succeeds.
func TestStreamFailureWithOnlyTextIsResent(t *testing.T) {
	result, err, inner, rec := runStreamRetry(t, 10,
		func(ctx context.Context) (*Result, error) {
			streamFire(ctx, func(sink *StreamSink) { sink.OnDelta("partial an") })
			return nil, connDropped()
		},
		func(ctx context.Context) (*Result, error) { return okResult("Worked for the resend") },
	)
	if err != nil {
		t.Fatalf("Execute error = %v, want success on the second attempt", err)
	}
	if result == nil || result.Message.TextContent() != "Worked for the resend" {
		t.Fatalf("result = %#v, want the second attempt's answer", result)
	}
	if inner.calls != 2 {
		t.Fatalf("inner calls = %d, want 2", inner.calls)
	}
	if live := rec.live(); len(live) != 1 || live[0].Retry != 1 || live[0].Total != 10 {
		t.Fatalf("non-Cleared notices = %+v, want one retry 1/10", rec.live())
	}
	// The resend began from no progress.
	if inner.progressAtCall[1] != ProgressNothing {
		t.Fatalf("second attempt progress = %v, want Nothing (reset before the resend)", inner.progressAtCall[1])
	}
}

// Only thinking finished, three times: two resends withdraw the finished
// thinking, the third finishes the turn from nothing.
func TestThinkingOnlyFailureRetriesTwiceWithdrawingThinkingThenEnds(t *testing.T) {
	attempt := func(ctx context.Context) (*Result, error) {
		streamFire(ctx, func(sink *StreamSink) {
			sink.OnReasoningDelta("thinking")
			sink.OnReasoningDone()
		})
		return nil, connDropped()
	}
	result, err, inner, rec := runStreamRetry(t, 10, attempt, attempt, attempt)
	var partial *PartialResponseError
	if !errors.As(err, &partial) {
		t.Fatalf("error = %v, want a PartialResponseError", err)
	}
	if partial.Complete || partial.Failed {
		t.Fatalf("partial = complete:%v failed:%v, want keep-partial", partial.Complete, partial.Failed)
	}
	if inner.calls != 3 {
		t.Fatalf("inner calls = %d, want 3", inner.calls)
	}
	if len(rec.live()) != 0 {
		t.Fatalf("non-Cleared notices = %+v, want none (no status line for thinking-only resends)", rec.live())
	}
	if rec.droppedThinking() != 2 {
		t.Fatalf("DropFinishedThinking notices = %d, want 2", rec.droppedThinking())
	}
	// Two withdraw announcements plus the finish's catch-up Cleared.
	if rec.cleared() != 3 {
		t.Fatalf("Cleared notices = %d, want 3", rec.cleared())
	}
	if result != nil {
		t.Fatalf("result = %#v, want nil", result)
	}
}

// A truncation twice in a row falls back to a non-streaming resend, and the
// fallback warning is shown once per process.
func TestTruncatedTwiceFallsBackToNonStreaming(t *testing.T) {
	truncated := func(ctx context.Context) (*Result, error) {
		streamFire(ctx, func(sink *StreamSink) { sink.OnDelta("partial an") })
		return nil, ErrStreamTruncated
	}
	result, err, inner, rec := runStreamRetry(t, 10,
		truncated, truncated,
		func(ctx context.Context) (*Result, error) { return okResult("after fallback") },
	)
	if err != nil {
		t.Fatalf("Execute error = %v, want the non-streaming attempt to succeed", err)
	}
	if result == nil || result.Message.TextContent() != "after fallback" {
		t.Fatalf("result = %#v, want the non-streaming answer", result)
	}
	if inner.calls != 3 || !inner.nonStreaming[2] {
		t.Fatalf("calls = %d nonStreaming = %v, want the third attempt non-streaming", inner.calls, inner.nonStreaming)
	}
	fallbacks := 0
	for _, n := range rec.notices {
		if n.Kind == ResponseNoticeStreamingFallback {
			fallbacks++
		}
	}
	if fallbacks != 1 {
		t.Fatalf("streaming_fallback notices = %d, want exactly 1", fallbacks)
	}

	// The same scenario again in the same process stays quiet.
	_, _, _, rec2 := runStreamRetry(t, 10,
		truncated, truncated,
		func(ctx context.Context) (*Result, error) { return okResult("after fallback") },
	)
	for _, n := range rec2.notices {
		if n.Kind == ResponseNoticeStreamingFallback {
			t.Fatal("the streaming fallback warning fired a second time in the same process")
		}
	}
}

// A stream that closed before any event is re-sent without streaming on the
// very next attempt.
func TestStreamNoEventsFallsBackToNonStreamingImmediately(t *testing.T) {
	result, err, inner, _ := runStreamRetry(t, 10,
		func(ctx context.Context) (*Result, error) {
			return nil, ErrStreamNoEvents
		},
		func(ctx context.Context) (*Result, error) { return okResult("after fallback") },
	)
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	if result == nil || result.Message.TextContent() != "after fallback" {
		t.Fatalf("result = %#v", result)
	}
	if inner.calls != 2 || !inner.nonStreaming[1] {
		t.Fatalf("calls = %d nonStreaming = %v, want the second attempt non-streaming", inner.calls, inner.nonStreaming)
	}
}

func TestRequestLevelStatusesAtNothing(t *testing.T) {
	t.Run("503 retries", func(t *testing.T) {
		srvErr := func(ctx context.Context) (*Result, error) {
			return nil, &APIError{StatusCode: http.StatusServiceUnavailable, Message: "try later"}
		}
		_, err, inner, rec := runStreamRetry(t, 10, srvErr, func(ctx context.Context) (*Result, error) { return okResult("ok") })
		if err != nil {
			t.Fatalf("Execute error = %v", err)
		}
		if inner.calls != 2 {
			t.Fatalf("inner calls = %d, want 2", inner.calls)
		}
		if live := rec.live(); len(live) != 1 || live[0].Status != http.StatusServiceUnavailable {
			t.Fatalf("non-Cleared notices = %+v, want one 503 retry", rec.live())
		}
	})
	t.Run("404 falls back without streaming", func(t *testing.T) {
		_, err, inner, _ := runStreamRetry(t, 10,
			func(ctx context.Context) (*Result, error) {
				return nil, &APIError{StatusCode: http.StatusNotFound, Message: "no such model"}
			},
			func(ctx context.Context) (*Result, error) { return okResult("ok") },
		)
		if err != nil {
			t.Fatalf("Execute error = %v", err)
		}
		if inner.calls != 2 || !inner.nonStreaming[1] {
			t.Fatalf("calls = %d nonStreaming = %v, want the second attempt non-streaming", inner.calls, inner.nonStreaming)
		}
	})
	t.Run("400 is returned untouched", func(t *testing.T) {
		bad := &APIError{StatusCode: http.StatusBadRequest, Message: "unexpected role"}
		_, err, inner, _ := runStreamRetry(t, 10, func(ctx context.Context) (*Result, error) { return nil, bad })
		if !errors.Is(err, bad) || errors.As(err, &bad) == false {
			t.Fatalf("error = %v, want the original 400", err)
		}
		if inner.calls != 1 {
			t.Fatalf("inner calls = %d, want 1", inner.calls)
		}
	})
	t.Run("529 is returned untouched", func(t *testing.T) {
		overloaded := &APIError{StatusCode: 529, Message: "overloaded"}
		_, err, inner, _ := runStreamRetry(t, 10, func(ctx context.Context) (*Result, error) { return nil, overloaded })
		var got *APIError
		if !errors.As(err, &got) || got.StatusCode != 529 {
			t.Fatalf("error = %v, want the original 529", err)
		}
		if inner.calls != 1 {
			t.Fatalf("inner calls = %d, want 1", inner.calls)
		}
	})
}

// Output that arrived, then a request-level rejection: the text stays and the
// turn's error is the provider's own.
func TestBadRequestAfterOutputFailsButKeepsTheText(t *testing.T) {
	bad := &APIError{StatusCode: http.StatusBadRequest, Message: "unexpected role"}
	_, err, _, rec := runStreamRetry(t, 10, func(ctx context.Context) (*Result, error) {
		streamFire(ctx, func(sink *StreamSink) {
			sink.OnDelta("the answer that arrived")
			sink.OnTextBlockDone()
		})
		return nil, bad
	})
	var partial *PartialResponseError
	if !errors.As(err, &partial) || !partial.Failed {
		t.Fatalf("error = %v, want a failed PartialResponseError", err)
	}
	if !errors.Is(partial.Err, bad) {
		t.Fatalf("partial.Err = %v, want the original 400", partial.Err)
	}
	if len(rec.notices) != 0 {
		t.Fatalf("notices = %+v, want none (the error itself is the notice)", rec.notices)
	}
}

// The response had fully arrived before the connection dropped: it finishes
// as a success, with no notice.
func TestCompleteResponseSurvivesAConnectionError(t *testing.T) {
	_, err, inner, rec := runStreamRetry(t, 10, func(ctx context.Context) (*Result, error) {
		streamFire(ctx, func(sink *StreamSink) {
			sink.OnDelta("complete answer")
			sink.OnTextBlockDone()
			sink.OnMessageStop()
		})
		return nil, connDropped()
	})
	var partial *PartialResponseError
	if !errors.As(err, &partial) || !partial.Complete {
		t.Fatalf("error = %v, want a complete PartialResponseError", err)
	}
	if inner.calls != 1 {
		t.Fatalf("inner calls = %d, want 1", inner.calls)
	}
	if len(rec.notices) != 0 {
		t.Fatalf("notices = %+v, want none", rec.notices)
	}
}

// Cancellation is returned exactly as it arrived, even when a tool call had
// already been dispatched.
func TestStreamRetryCancellationIsReturnedAsIs(t *testing.T) {
	sink := &StreamSink{
		OnDelta:          func(string) {},
		OnReasoningDelta: func(string) {},
		OnTextBlockDone:  func() {},
		OnToolCallReady:  func(ToolCall) {},
		OnMessageStop:    func() {},
	}
	base, cancel := context.WithCancel(context.Background())
	ctx, _ := WithStreamProgress(WithStreamSink(base, sink))
	inner := &scriptedStreamLLM{attempts: []func(ctx context.Context) (*Result, error){func(ctx context.Context) (*Result, error) {
		streamFire(ctx, func(sink *StreamSink) {
			sink.OnDelta("started answering")
			sink.OnTextBlockDone()
		})
		if p := StreamProgressFrom(ctx); p != nil {
			p.MarkToolDispatched()
		}
		cancel()
		return nil, context.Canceled
	}}}
	mw := NewStreamRetry(10, WithRetryNotify(func(ctx context.Context, tr TransportRetry) {
		if s := StreamSinkFrom(ctx); s != nil && s.OnTransportRetry != nil {
			s.OnTransportRetry(tr)
		}
	}))
	_, err := Use(inner, mw).Execute(ctx, []Message{UserMessage(Text("hi"))}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want the cancellation as it arrived", err)
	}
	if IsPartialResponse(err) {
		t.Fatal("cancellation must not be wrapped as a partial response")
	}
	var exhausted *MaxAttemptsExceededError
	if errors.As(err, &exhausted) {
		t.Fatal("cancellation must not be wrapped as exhaustion")
	}
}

// A connection that keeps dying before any output spends the whole budget as
// streams, then gets exactly one non-streaming attempt, then gives up.
func TestConnectionLostAtNothingSpendsTheBudgetThenFallsBack(t *testing.T) {
	drop := func(ctx context.Context) (*Result, error) { return nil, connDropped() }
	attempts := []func(ctx context.Context) (*Result, error){drop, drop, drop} // maxRetries=1: first call + 1 retry + 1 non-streaming
	_, err, inner, _ := runStreamRetry(t, 1, attempts...)
	var exhausted *MaxAttemptsExceededError
	if !errors.As(err, &exhausted) {
		t.Fatalf("error = %v, want MaxAttemptsExceededError", err)
	}
	if inner.calls != 3 {
		t.Fatalf("inner calls = %d, want 3 (first + 1 streaming retry + 1 non-streaming)", inner.calls)
	}
	if !inner.nonStreaming[2] || inner.nonStreaming[0] || inner.nonStreaming[1] {
		t.Fatalf("nonStreaming per call = %v, want [false false true]", inner.nonStreaming)
	}
}

// After the fallback attempt fails, the next attempt streams again: a
// non-streaming request has no watchdog, so staying on it multiplies a
// 5-minute blind wait by the retry budget.
func TestNonStreamingFallbackReturnsToStreamingAfterOneAttempt(t *testing.T) {
	_, err, inner, _ := runStreamRetry(t, 10,
		func(ctx context.Context) (*Result, error) {
			return nil, &APIError{StatusCode: http.StatusNotFound} // picks the fallback
		},
		func(ctx context.Context) (*Result, error) {
			return nil, fmt.Errorf("%w: %v", ErrNonStreamingTimeout, context.DeadlineExceeded)
		},
		func(ctx context.Context) (*Result, error) { return okResult("streamed again") },
	)
	if err != nil {
		t.Fatalf("Execute: %v (a streaming attempt after the fallback should succeed)", err)
	}
	want := []bool{false, true, false}
	if !reflect.DeepEqual(inner.nonStreaming, want) {
		t.Fatalf("non-streaming per attempt = %v, want %v", inner.nonStreaming, want)
	}
}

// Even when everything fails, there is only one non-streaming blind wait:
// the decision table never asks for the fallback twice.
func TestRetryGivesUpAfterOneNonStreamingAttempt(t *testing.T) {
	fail := func(ctx context.Context) (*Result, error) {
		return nil, fmt.Errorf("%w: %v", ErrNonStreamingTimeout, context.DeadlineExceeded)
	}
	_, err, inner, _ := runStreamRetry(t, 10,
		func(ctx context.Context) (*Result, error) {
			return nil, &APIError{StatusCode: http.StatusNotFound}
		}, fail, fail, fail, fail, fail, fail, fail, fail, fail, fail, fail)
	if err == nil {
		t.Fatal("every attempt failed: an error is expected")
	}
	n := 0
	for _, nonStreaming := range inner.nonStreaming {
		if nonStreaming {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("non-streaming attempts = %d, want exactly 1 (each one is a 5-minute blind wait)", n)
	}
}

// A retried logical request reports the settled attempt's own usage as
// Result.Usage — the every-attempt billing is the accumulator's business —
// while Attempts says how many physical requests it took.
func TestRetriedAttemptsAreCountedNotMergedIntoUsage(t *testing.T) {
	result, err, inner, _ := runStreamRetry(t, 10,
		func(ctx context.Context) (*Result, error) {
			streamFire(ctx, func(sink *StreamSink) { sink.OnDelta("partial an") })
			return nil, NewExecuteError(connDropped(), &Usage{InputTokens: 100, OutputTokens: 10})
		},
		func(ctx context.Context) (*Result, error) {
			res, _ := okResult("Worked for the resend")
			res.Usage = &Usage{InputTokens: 120, OutputTokens: 8}
			return res, nil
		},
	)
	if err != nil {
		t.Fatalf("Execute error = %v, want success on the second attempt", err)
	}
	if result == nil || result.Usage == nil {
		t.Fatalf("result = %#v, want the settled attempt's usage", result)
	}
	if result.Usage.InputTokens != 120 || result.Usage.OutputTokens != 8 {
		t.Fatalf("result.Usage = %+v, want the settled attempt's own usage (120/8), not a merged total", result.Usage)
	}
	if result.Attempts != 2 {
		t.Fatalf("result.Attempts = %d, want 2 (the dead attempt still happened)", result.Attempts)
	}
	if inner.calls != 2 {
		t.Fatalf("inner calls = %d, want 2", inner.calls)
	}
}

// Every attempt of an exhausted request rides its error out: the merged
// usage of all of them, the physical count, and the last attempt's own
// usage (which is not the merged total).
func TestExhaustedRetryCarriesEveryAttempt(t *testing.T) {
	usages := []Usage{
		{InputTokens: 100, OutputTokens: 10},
		{InputTokens: 110, OutputTokens: 12},
		{InputTokens: 120, OutputTokens: 14},
	}
	drop := func(i int) func(ctx context.Context) (*Result, error) {
		return func(ctx context.Context) (*Result, error) {
			return nil, NewExecuteError(connDropped(), &usages[i])
		}
	}
	// maxRetries=1: two stream attempts plus the non-streaming fallback.
	_, err, inner, _ := runStreamRetry(t, 1, drop(0), drop(1), drop(2))
	var exhausted *MaxAttemptsExceededError
	if !errors.As(err, &exhausted) {
		t.Fatalf("error = %v, want MaxAttemptsExceededError", err)
	}
	if inner.calls != 3 {
		t.Fatalf("inner calls = %d, want 3", inner.calls)
	}
	usage := UsageFromError(err)
	if usage == nil || usage.InputTokens != 330 || usage.OutputTokens != 36 {
		t.Fatalf("UsageFromError = %+v, want the sum of every attempt (330/36)", usage)
	}
	if n := AttemptsFromError(err); n != 3 {
		t.Fatalf("AttemptsFromError = %d, want 3", n)
	}
	last := LastAttemptUsageFromError(err)
	if last == nil || last.InputTokens != 120 || last.OutputTokens != 14 {
		t.Fatalf("LastAttemptUsageFromError = %+v, want the last attempt's own usage (120/14)", last)
	}
}

// The waitFor computation is shared with the plain retry loop.
func TestWaitForComputesJitterAndRetryAfter(t *testing.T) {
	t.Parallel()

	cfg := &retryConfig{maxBackoff: 30 * time.Second}
	sleep, giveUp := cfg.waitFor(500*time.Millisecond, connDropped())
	if giveUp || sleep < 375*time.Millisecond {
		t.Fatalf("waitFor = %v/%v, want a jittered 500ms step", sleep, giveUp)
	}
	cfg.upwardJitter = true
	sleep, giveUp = cfg.waitFor(500*time.Millisecond, connDropped())
	if giveUp || sleep < 500*time.Millisecond {
		t.Fatalf("upward waitFor = %v/%v, want at least the step", sleep, giveUp)
	}
	cfg.retryAfter = func(error) time.Duration { return 10 * time.Minute }
	cfg.maxRetryAfter = 5 * time.Minute
	if _, giveUp := cfg.waitFor(time.Second, connDropped()); !giveUp {
		t.Fatal("a Retry-After past the limit must give up")
	}
	cfg.maxRetryAfter = 10 * time.Minute
	sleep, giveUp = cfg.waitFor(time.Second, connDropped())
	if giveUp || sleep != 10*time.Minute {
		t.Fatalf("waitFor = %v/%v, want the server's own wait", sleep, giveUp)
	}
}
