package run

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

// netRetryTestLLM streams part of an answer, dies with a transport error on
// the first attempt, and answers normally from the second attempt on — the
// owner's dropped-stream scenario in miniature.
type netRetryTestLLM struct {
	calls   int
	deltas  []string
	failure error
}

func (m *netRetryTestLLM) Execute(ctx context.Context, _ []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.calls++
	sink := llm.StreamSinkFrom(ctx)
	if m.calls == 1 && m.failure != nil {
		if sink != nil && sink.OnDelta != nil {
			for _, d := range m.deltas {
				sink.OnDelta(d)
			}
		}
		return nil, m.failure
	}
	if sink != nil && sink.OnDelta != nil {
		for _, d := range m.deltas {
			sink.OnDelta(d)
		}
	}
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("full answer")})
	return &llm.Result{Message: &msg}, nil
}

// resendCaptureLLM records the messages each attempt received and behaves
// like the owner's dropped-stream scenario: attempt 1 streams part of an
// answer and dies on the network, attempt 2 succeeds.
type resendCaptureLLM struct {
	calls    int
	received [][]llm.Message
}

func (m *resendCaptureLLM) Execute(ctx context.Context, messages []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.calls++
	m.received = append(m.received, append([]llm.Message(nil), messages...))
	sink := llm.StreamSinkFrom(ctx)
	if m.calls == 1 {
		if sink != nil && sink.OnDelta != nil {
			sink.OnDelta("par")
		}
		return nil, &net.OpError{Op: "read", Net: "tcp", Err: errors.New("i/o timeout")}
	}
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("full answer")})
	return &llm.Result{Message: &msg}, nil
}

func TestNetworkRetryResendsTheOriginalRequest(t *testing.T) {
	inner := &resendCaptureLLM{}
	wrapped := wrapNetworkRetryLLM(inner, 3, 0)

	var notes []llm.TransportRetry
	ctx := llm.WithStreamSink(context.Background(), &llm.StreamSink{
		OnTransportRetry: func(tr llm.TransportRetry) { notes = append(notes, tr) },
	})

	res, err := wrapped.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res == nil || res.Message == nil || res.Message.TextContent() != "full answer" {
		t.Fatalf("result = %+v, want the retried attempt's answer", res)
	}
	if inner.calls != 2 {
		t.Fatalf("provider calls = %d, want 2 (dropped + re-sent)", inner.calls)
	}
	// The resend contract: attempt 2 received exactly the request attempt 1
	// did — nothing continued, nothing appended.
	if len(inner.received) != 2 {
		t.Fatalf("captured attempts = %d, want 2", len(inner.received))
	}
	for i, got := range inner.received {
		if len(got) != 1 || got[0].Role != llm.RoleUser || got[0].TextContent() != "hi" {
			t.Fatalf("attempt %d messages = %+v, want the original request unchanged", i+1, got)
		}
	}
	if len(notes) != 2 || notes[0].Retry != 1 || notes[0].Total != 3 || notes[0].Cleared {
		t.Fatalf("notifications = %+v, want retry 1/3 then its cleared notice", notes)
	}
}

func TestNetworkRetryExhaustionMessage(t *testing.T) {
	inner := &alwaysNetFailLLM{}
	wrapped := wrapNetworkRetryLLM(inner, 2, 0)

	_, err := wrapped.Execute(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("expected the exhaustion error")
	}
	if !strings.HasPrefix(err.Error(), "network error, gave up after 2 retries: ") {
		t.Fatalf("error = %v, want the gave-up sentence with the retry count", err)
	}
	var ne net.Error
	if !errors.As(err, &ne) {
		t.Fatalf("error = %T, want it to still unwrap to the net.Error", err)
	}
	// The stream-failure decision table (plan 123): the shared budget pays
	// for the first call plus both streaming retries, then one non-streaming
	// attempt follows before the loop gives up.
	if inner.calls != 4 {
		t.Fatalf("provider calls = %d, want 1 + 2 streaming retries + 1 non-streaming", inner.calls)
	}
}

// alwaysNetFailLLM dies on the network every call: the loop's exhaustion path
// in miniature.
type alwaysNetFailLLM struct{ calls int }

func (m *alwaysNetFailLLM) Execute(context.Context, []llm.Message, []*llm.Tool) (*llm.Result, error) {
	m.calls++
	return nil, &net.OpError{Op: "read", Net: "tcp", Err: errors.New("i/o timeout")}
}

func TestNetworkRetryCapsAtFifteen(t *testing.T) {
	wrapped := wrapNetworkRetryLLM(&netRetryTestLLM{}, 99, 0)
	outer, ok := wrapped.(networkRetryLLM)
	if !ok {
		t.Fatalf("wrapNetworkRetryLLM returned %T, want networkRetryLLM", wrapped)
	}
	if outer.maxRetries != appcfg.MaxNetworkRetryMax {
		t.Fatalf("maxRetries = %d, want the cap %d", outer.maxRetries, appcfg.MaxNetworkRetryMax)
	}
}

func TestNetworkRetryHonoursContextCancellationDuringBackoff(t *testing.T) {
	inner := &netRetryTestLLM{failure: &net.OpError{Op: "read", Err: errors.New("i/o timeout")}}
	wrapped := wrapNetworkRetryLLM(inner, 5, 0)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	// The back-off grows past the cancel point, so the loop must observe the
	// cancellation and return it instead of sleeping on.
	start := time.Now()
	_, err := wrapped.Execute(ctx, nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Execute error = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("cancellation took %v, want an immediate return", elapsed)
	}
}

func TestNetworkRetryIgnoresNonTransportErrors(t *testing.T) {
	structural := &llm.APIError{StatusCode: 400, Message: "bad shape"}
	inner := &netRetryTestLLM{failure: structural}
	wrapped := wrapNetworkRetryLLM(inner, 4, 0)

	var notes []llm.TransportRetry
	ctx := llm.WithStreamSink(context.Background(), &llm.StreamSink{
		OnTransportRetry: func(tr llm.TransportRetry) { notes = append(notes, tr) },
	})
	_, err := wrapped.Execute(ctx, nil, nil)
	if err == nil {
		t.Fatal("expected the structural error to surface")
	}
	if inner.calls != 1 {
		t.Fatalf("provider calls = %d, want 1 (no retry)", inner.calls)
	}
	if len(notes) != 0 {
		t.Fatalf("notifications = %+v, want none", notes)
	}
}

func TestNetworkRetryDisabledAtZero(t *testing.T) {
	inner := &netRetryTestLLM{failure: &net.OpError{Op: "read", Err: errors.New("i/o timeout")}}
	wrapped := wrapNetworkRetryLLM(inner, 0, 0)
	_, err := wrapped.Execute(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("expected the transport error to surface immediately")
	}
	if inner.calls != 1 {
		t.Fatalf("provider calls = %d, want 1 (fail fast)", inner.calls)
	}
}

func TestNetworkRetryMutedContextStaysSilent(t *testing.T) {
	inner := &netRetryTestLLM{failure: &net.OpError{Op: "read", Err: errors.New("i/o timeout")}}
	wrapped := wrapNetworkRetryLLM(inner, 3, 0)

	var notes []llm.TransportRetry
	ctx := llm.WithMutedForegroundStream(llm.WithStreamSink(context.Background(), &llm.StreamSink{
		OnTransportRetry: func(tr llm.TransportRetry) { notes = append(notes, tr) },
	}))
	if _, err := wrapped.Execute(ctx, nil, nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if inner.calls != 2 {
		t.Fatalf("provider calls = %d, want 2 (the muted chain still retries)", inner.calls)
	}
	if len(notes) != 0 {
		t.Fatalf("muted notifications = %+v, want none", notes)
	}
}

// The mount order is the contract: the network retry sits inside
// WrapRecoverableLLM (a network death retries through it) and the summary
// chain carries it too. Both are asserted on the layer the execute path
// actually walks.
func TestNetworkRetryMountOrder(t *testing.T) {
	recoverable := WrapRecoverableLLM(&netRetryTestLLM{
		failure: &net.OpError{Op: "read", Err: errors.New("i/o timeout")},
	}, nil, &CompactChainDeps{}, 3, 0)

	var notes []llm.TransportRetry
	ctx := llm.WithStreamSink(context.Background(), &llm.StreamSink{
		OnTransportRetry: func(tr llm.TransportRetry) { notes = append(notes, tr) },
	})
	if _, err := recoverable.Execute(ctx, nil, nil); err != nil {
		t.Fatalf("Execute through WrapRecoverableLLM: %v", err)
	}
	if len(notes) != 2 {
		t.Fatalf("notifications through the mounted chain = %+v, want retry 1/3 then its cleared notice", notes)
	}
	if notes[0].Retry != 1 || notes[0].Total != 3 || notes[0].Cleared {
		t.Fatalf("first notification = %+v, want retry 1/3 in flight", notes[0])
	}
	if !notes[1].Cleared {
		t.Fatalf("second notification = %+v, want the cleared notice", notes[1])
	}

	summary := wrapNetworkRetryLLM(&netRetryTestLLM{
		failure: &net.OpError{Op: "read", Err: errors.New("i/o timeout")},
	}, 3, 0)
	notes = nil
	if _, err := summary.Execute(llm.WithStreamSink(context.Background(), &llm.StreamSink{
		OnTransportRetry: func(tr llm.TransportRetry) { notes = append(notes, tr) },
	}), nil, nil); err != nil {
		t.Fatalf("Execute through the summary chain: %v", err)
	}
	if len(notes) != 2 || !notes[1].Cleared {
		t.Fatalf("summary-chain notifications = %+v, want the network retry mounted (retry then cleared)", notes)
	}
}

// A server error the provider normalized with a status is the network retry
// layer's business now: the request is re-sent and the notification carries
// the status so the surfaces can call it an API error.
func TestNetworkRetryRetriesServerErrors(t *testing.T) {
	inner := &netRetryTestLLM{failure: &llm.APIError{StatusCode: 503, Message: "service unavailable"}}
	wrapped := wrapNetworkRetryLLM(inner, 3, 0)

	var notes []llm.TransportRetry
	ctx := llm.WithStreamSink(context.Background(), &llm.StreamSink{
		OnTransportRetry: func(tr llm.TransportRetry) { notes = append(notes, tr) },
	})

	res, err := wrapped.Execute(ctx, nil, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res == nil || res.Message == nil || res.Message.TextContent() != "full answer" {
		t.Fatalf("result = %+v, want the retried attempt's answer", res)
	}
	if inner.calls != 2 {
		t.Fatalf("provider calls = %d, want 2 (503 then re-sent)", inner.calls)
	}
	if len(notes) != 2 || notes[0].Cleared || !notes[1].Cleared {
		t.Fatalf("notifications = %+v, want retry 1 in flight then its cleared notice", notes)
	}
	if notes[0].Status != 503 {
		t.Fatalf("notification status = %d, want 503", notes[0].Status)
	}
}

// A stream the watchdog aborted for going silent reads as a dropped
// connection: the network retry layer re-sends the whole request immediately,
// exactly as it does for a stream cut mid-answer. The stall resend runs with
// no status line (plan 123), so its only announcement is the withdraw
// notification that takes the dead attempt's in-flight text back.
func TestNetworkRetryRetriesAStalledStream(t *testing.T) {
	inner := &netRetryTestLLM{failure: &llm.StreamStallError{After: time.Second}}
	wrapped := wrapNetworkRetryLLM(inner, 3, 0)

	var notes []llm.TransportRetry
	ctx := llm.WithStreamSink(context.Background(), &llm.StreamSink{
		OnTransportRetry: func(tr llm.TransportRetry) { notes = append(notes, tr) },
	})

	res, err := wrapped.Execute(ctx, nil, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res == nil || res.Message == nil || res.Message.TextContent() != "full answer" {
		t.Fatalf("result = %+v, want the re-sent attempt's answer", res)
	}
	if inner.calls != 2 {
		t.Fatalf("provider calls = %d, want 2 (stalled + re-sent)", inner.calls)
	}
	if len(notes) != 1 || !notes[0].Cleared {
		t.Fatalf("notifications = %+v, want the stall resend's withdraw notice", notes)
	}
}

// 529 is the overload path's own failure and stays there: the network retry
// layer rejects it without a word, and the recoverable layer's 529 branch is
// the one that re-enters the stack — so no transport-retry notification ever
// fires for it.
func TestNetworkRetryLeaves529ToRecovery(t *testing.T) {
	inner := &netRetryTestLLM{failure: &llm.APIError{StatusCode: 529, Message: "overloaded"}}
	recoverable := WrapRecoverableLLM(inner, nil, &CompactChainDeps{}, 3, 0)

	var notes []llm.TransportRetry
	ctx := llm.WithStreamSink(context.Background(), &llm.StreamSink{
		OnTransportRetry: func(tr llm.TransportRetry) { notes = append(notes, tr) },
	})

	res, err := recoverable.Execute(ctx, nil, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res == nil || res.Message == nil || res.Message.TextContent() != "full answer" {
		t.Fatalf("result = %+v, want the recovery branch's answer", res)
	}
	if len(notes) != 0 {
		t.Fatalf("transport-retry notifications = %+v, want none — 529 is recovered above this layer", notes)
	}
}

// failingLLM returns the same error from every Execute and counts the calls.
type failingLLM struct {
	err   error
	calls int
}

func (f *failingLLM) Execute(context.Context, []llm.Message, []*llm.Tool) (*llm.Result, error) {
	f.calls++
	return nil, f.err
}

// progressCtx builds a context whose stream progress has already reached the
// given shape.
func progressCtx(t *testing.T, fire func(sink *llm.StreamSink)) context.Context {
	t.Helper()
	sink := &llm.StreamSink{
		OnResponseStarted: func() {},
		OnDelta:           func(string) {},
		OnReasoningDelta:  func(string) {},
		OnTextBlockDone:   func() {},
		OnToolCallReady:   func(llm.ToolCall) {},
		OnMessageStop:     func() {},
	}
	ctx, _ := llm.WithStreamProgress(llm.WithStreamSink(context.Background(), sink))
	fire(llm.StreamSinkFrom(ctx))
	return ctx
}

// A request whose progress already shows finished output is never re-sent by
// the blank-output layer: the error surfaces and the caller finishes from
// what completed.
func TestBlankOutputRetryHonoursTheResendGuard(t *testing.T) {
	inner := &failingLLM{err: &llm.APIError{StatusCode: 200, Message: "completed without output"}}
	wrapped := wrapBlankOutputRetryLLM(inner)

	ctx := progressCtx(t, func(sink *llm.StreamSink) {
		sink.OnDelta("the partial answer")
		sink.OnTextBlockDone()
	})
	_, err := wrapped.Execute(ctx, nil, nil)
	if !errors.Is(err, inner.err) {
		t.Fatalf("Execute error = %v, want the original blank-output error", err)
	}
	if inner.calls != 1 {
		t.Fatalf("inner calls = %d, want 1 (output was already shown)", inner.calls)
	}
}

// A partial response is already a decision about how this response ends: the
// compaction and 529 resends of the recoverable layer must not re-ask the
// question, not even when the wrapped error looks like theirs.
func TestRecoverableLayerPassesPartialResponsesThrough(t *testing.T) {
	partialErr := &llm.PartialResponseError{
		Err:   &llm.APIError{StatusCode: 400, Message: "context window exceeded: maximum context length"},
		Cause: llm.CauseBadRequest,
	}
	inner := &failingLLM{err: partialErr}
	recoverable := WrapRecoverableLLM(inner, nil, &CompactChainDeps{TryCompact: func(context.Context, []llm.Message, []*llm.Tool, bool) ([]llm.Message, bool, error) {
		return []llm.Message{llm.UserMessage(llm.Text("compacted"))}, true, nil
	}}, 3, 0)

	_, err := recoverable.Execute(context.Background(), nil, nil)
	var partial *llm.PartialResponseError
	if !errors.As(err, &partial) || partial != partialErr {
		t.Fatalf("Execute error = %v, want the PartialResponseError untouched", err)
	}
	if inner.calls != 1 {
		t.Fatalf("inner calls = %d, want 1 (no compaction or 529 resend)", inner.calls)
	}
}

// The 529 recovery re-asks only a request that had produced nothing; one that
// already showed anything keeps the error. The stream-retry layer inside owns
// a 529 that arrives mid-flight, so this pins the recoverable layer's own
// gate directly.
func TestStatus529AfterStartedOutputIsReturnedAsIs(t *testing.T) {
	inner := &failingLLM{err: &llm.APIError{StatusCode: 529, Message: "overloaded"}}
	recoverable := recoverableLLM{inner: inner, compactDeps: &CompactChainDeps{}, runGuard: newCompactRunGuard()}

	ctx := progressCtx(t, func(sink *llm.StreamSink) { sink.OnResponseStarted() })
	_, err := recoverable.Execute(ctx, nil, nil)
	var got *llm.APIError
	if !errors.As(err, &got) || got.StatusCode != 529 {
		t.Fatalf("Execute error = %v, want the original 529", err)
	}
	if inner.calls != 1 {
		t.Fatalf("inner calls = %d, want 1 (529 recovery only handles a request that produced nothing)", inner.calls)
	}
}

// A mid-flight compaction resend is refused the same way: a request whose
// context-length rejection arrived after output stays with its caller.
func TestCompactionResendHonoursTheResendGuard(t *testing.T) {
	inner := &failingLLM{err: &llm.APIError{StatusCode: 400, Message: "maximum context length exceeded"}}
	deps := &CompactChainDeps{
		TryCompact: func(context.Context, []llm.Message, []*llm.Tool, bool) ([]llm.Message, bool, error) {
			return []llm.Message{llm.UserMessage(llm.Text("compacted"))}, true, nil
		},
	}
	recoverable := WrapRecoverableLLM(inner, nil, deps, 3, 0)

	ctx := progressCtx(t, func(sink *llm.StreamSink) {
		sink.OnDelta("partial")
		sink.OnTextBlockDone()
	})
	_, err := recoverable.Execute(ctx, nil, nil)
	if !errors.Is(err, inner.err) {
		t.Fatalf("Execute error = %v, want the original overflow error", err)
	}
	if inner.calls != 1 {
		t.Fatalf("inner calls = %d, want 1 (no compacted resend after output)", inner.calls)
	}
}
