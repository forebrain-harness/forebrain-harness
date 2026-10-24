package run

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

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

// continuationCaptureLLM records the messages each attempt received and
// behaves like the owner's dropped-stream scenario: attempt 1 streams part of
// an answer and dies on the network, attempt 2 succeeds.
type continuationCaptureLLM struct {
	calls    int
	received [][]llm.Message
}

func (m *continuationCaptureLLM) Execute(ctx context.Context, messages []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.calls++
	m.received = append(m.received, append([]llm.Message(nil), messages...))
	sink := llm.StreamSinkFrom(ctx)
	if m.calls == 1 {
		if sink != nil && sink.OnDelta != nil {
			sink.OnDelta("par")
			sink.OnDelta("tial")
		}
		if sink != nil && sink.OnReasoningDelta != nil {
			sink.OnReasoningDelta("half a thought")
		}
		return nil, &net.OpError{Op: "read", Net: "tcp", Err: errors.New("i/o timeout")}
	}
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text(" continued")})
	return &llm.Result{Message: &msg}, nil
}

func TestNetworkRetryContinuesFromTheStreamedText(t *testing.T) {
	inner := &continuationCaptureLLM{}
	// The mounted shape: the prefill wrapper innermost, the network retry
	// around it.
	wrapped := wrapNetworkRetryLLM(wrapContinuationPrefillLLM(inner), 3)

	var sinkDeltas []string
	var notes []llm.TransportRetry
	ctx := llm.WithStreamSink(context.Background(), &llm.StreamSink{
		OnDelta:          func(s string) { sinkDeltas = append(sinkDeltas, s) },
		OnTransportRetry: func(tr llm.TransportRetry) { notes = append(notes, tr) },
	})

	res, err := wrapped.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res == nil || res.Message == nil || res.Message.TextContent() != " continued" {
		t.Fatalf("result = %+v, want the continued attempt's answer", res)
	}
	if inner.calls != 2 {
		t.Fatalf("provider calls = %d, want 2 (dropped + continued)", inner.calls)
	}
	if len(sinkDeltas) != 2 {
		t.Fatalf("sink deltas = %v, want the dead attempt's deltas", sinkDeltas)
	}
	if len(notes) != 1 || notes[0].Retry != 1 || notes[0].Total != 3 {
		t.Fatalf("notifications = %+v, want retry 1/3", notes)
	}
	// The continuation contract: attempt 1 received the plain request; the
	// retry received it plus the assistant message carrying what the dead
	// attempt had already streamed — answer text and thinking both.
	if len(inner.received) != 2 {
		t.Fatalf("captured attempts = %d, want 2", len(inner.received))
	}
	if len(inner.received[0]) != 1 || inner.received[0][0].Role != llm.RoleUser {
		t.Fatalf("attempt 1 messages = %+v, want the plain request", inner.received[0])
	}
	second := inner.received[1]
	if len(second) != 2 || second[0].Role != llm.RoleUser {
		t.Fatalf("attempt 2 messages = %+v, want the request plus the prefill", second)
	}
	prefill := second[1]
	if prefill.Role != llm.RoleAssistant || prefill.TextContent() != "partial" {
		t.Fatalf("prefill = %+v, want the streamed 'partial' as an assistant message", prefill)
	}
	if !strings.HasPrefix(prefill.Name, llm.Prefix) || !strings.Contains(prefill.Name, "half a thought") {
		t.Fatalf("prefill Name = %q, want the streamed thinking on the carry", prefill.Name)
	}
}

func TestNetworkRetryHonoursContextCancellationDuringBackoff(t *testing.T) {
	inner := &netRetryTestLLM{failure: &net.OpError{Op: "read", Err: errors.New("i/o timeout")}}
	wrapped := wrapNetworkRetryLLM(inner, 5)

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
	wrapped := wrapNetworkRetryLLM(inner, 4)

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
	wrapped := wrapNetworkRetryLLM(inner, 0)
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
	wrapped := wrapNetworkRetryLLM(inner, 3)

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
	}, nil, &CompactChainDeps{}, 3)

	var notes []llm.TransportRetry
	ctx := llm.WithStreamSink(context.Background(), &llm.StreamSink{
		OnTransportRetry: func(tr llm.TransportRetry) { notes = append(notes, tr) },
	})
	if _, err := recoverable.Execute(ctx, nil, nil); err != nil {
		t.Fatalf("Execute through WrapRecoverableLLM: %v", err)
	}
	if len(notes) != 1 || notes[0].Total != 3 {
		t.Fatalf("notifications through the mounted chain = %+v, want one retry with total 3", notes)
	}

	summary := wrapNetworkRetryLLM(&netRetryTestLLM{
		failure: &net.OpError{Op: "read", Err: errors.New("i/o timeout")},
	}, 3)
	notes = nil
	if _, err := summary.Execute(llm.WithStreamSink(context.Background(), &llm.StreamSink{
		OnTransportRetry: func(tr llm.TransportRetry) { notes = append(notes, tr) },
	}), nil, nil); err != nil {
		t.Fatalf("Execute through the summary chain: %v", err)
	}
	if len(notes) != 1 {
		t.Fatalf("summary-chain notifications = %+v, want the network retry mounted", notes)
	}
}
