package assembly

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// recordingSink keeps every event a compaction published, in order.
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

func (s *recordingSink) all() []event.RunEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]event.RunEvent(nil), s.events...)
}

// streamingSummaryLLM writes its summary through the stream sink one word at a
// time, the way a provider streams, so progress has something to measure.
type streamingSummaryLLM struct {
	words []string
	err   error
}

func (f streamingSummaryLLM) Execute(ctx context.Context, _ []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	if f.err != nil {
		return nil, f.err
	}
	sink := llm.StreamSinkFrom(ctx)
	for _, word := range f.words {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if sink != nil && sink.OnDelta != nil {
			sink.OnDelta(word + " ")
		}
	}
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text(strings.Join(f.words, " "))})
	return &llm.Result{Message: &msg}, nil
}

// cancellingLLM stands for the user pressing Esc mid-summary.
type cancellingLLM struct{ cancel context.CancelFunc }

func (f cancellingLLM) Execute(ctx context.Context, _ []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	f.cancel()
	return nil, ctx.Err()
}

func seedCompactSession(t *testing.T, sid string) (*recordingSink, Service) {
	t.Helper()
	ctx, sess := context.Background(), newCompactTestStore(t)
	for i := 0; i < 3; i++ {
		_, _ = sess.Append(ctx, sid, "user", strings.Repeat("requirement detail. ", 40))
		_, _ = sess.Append(ctx, sid, "assistant", "answer")
	}
	sink := &recordingSink{}
	words := make([]string, 400)
	for i := range words {
		words[i] = "summary"
	}
	return sink, Service{Sessions: sess, Events: sink, CompactLLM: func(context.Context) llm.LLM { return streamingSummaryLLM{words: words} }}
}

// TestManualCompactionPublishesItsLifecycle pins the one lifecycle every
// surface draws a compaction from: started, a rising percentage that never
// claims completion, and one terminal event — all under one compaction id and
// addressed to the conversation.
func TestManualCompactionPublishesItsLifecycle(t *testing.T) {
	sink, svc := seedCompactSession(t, "s1")
	res, err := svc.ManualCompactSession(context.Background(), "s1", "manual")
	if err != nil {
		t.Fatal(err)
	}
	events := sink.all()
	if len(events) < 4 {
		t.Fatalf("events = %d, want started, progress and the end", len(events))
	}
	var started event.ContextCompactingPayload
	if events[0].Type != event.RunEventContextCompacting || json.Unmarshal(events[0].Payload, &started) != nil {
		t.Fatalf("first event = %s", events[0].Type)
	}
	if started.CompactionID == "" || started.Trigger != "manual" || started.TokensBefore <= 0 || started.AgentID != "" {
		t.Fatalf("started = %+v", started)
	}
	last := -1
	sawSaving := false
	for _, evt := range events[1 : len(events)-1] {
		if evt.Type != event.RunEventContextCompactProgress {
			t.Fatalf("mid-lifecycle event %s", evt.Type)
		}
		var p event.ContextCompactProgressPayload
		if json.Unmarshal(evt.Payload, &p) != nil || p.CompactionID != started.CompactionID {
			t.Fatalf("progress %+v belongs to another compaction", p)
		}
		if p.Percent < last || p.Percent >= 100 {
			t.Fatalf("progress went %d → %d", last, p.Percent)
		}
		last = p.Percent
		sawSaving = sawSaving || p.Phase == event.CompactPhaseSaving
	}
	if last < 90 || !sawSaving {
		t.Fatalf("progress ended at %d (saving seen: %v), want the saving phase near the end", last, sawSaving)
	}
	end := events[len(events)-1]
	var done event.ContextCompactedPayload
	if end.Type != event.RunEventContextCompacted || json.Unmarshal(end.Payload, &done) != nil {
		t.Fatalf("last event = %s", end.Type)
	}
	if done.CompactionID != started.CompactionID || done.BoundaryID != res.BoundaryID || done.TokensBefore != started.TokensBefore || done.Duration == "" {
		t.Fatalf("compacted = %+v, result = %+v", done, res)
	}
	for _, evt := range events {
		if evt.SessionID != "s1" || evt.RunID != "" {
			t.Fatalf("event %s addressed to session %q run %q", evt.Type, evt.SessionID, evt.RunID)
		}
	}
}

// TestCompactionFailureAndCancellationEndTheLifecycle pins the two ways a
// compaction ends without a checkpoint: a failure carries the error text, a
// cancellation is reported as the user's decision rather than as an error.
func TestCompactionFailureAndCancellationEndTheLifecycle(t *testing.T) {
	t.Run("failed", func(t *testing.T) {
		sink, svc := seedCompactSession(t, "s1")
		svc.CompactLLM = func(context.Context) llm.LLM { return streamingSummaryLLM{err: errors.New("provider unavailable")} }
		if _, err := svc.ManualCompactSession(context.Background(), "s1", "manual"); err == nil {
			t.Fatal("a failed summary must fail the compaction")
		}
		events := sink.all()
		var failed event.ContextCompactFailedPayload
		if end := events[len(events)-1]; end.Type != event.RunEventContextCompactError || json.Unmarshal(end.Payload, &failed) != nil {
			t.Fatalf("last event = %s", end.Type)
		}
		if failed.Cancelled || !strings.Contains(failed.Error, "provider unavailable") || failed.Trigger != "manual" {
			t.Fatalf("failed = %+v", failed)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		sink, svc := seedCompactSession(t, "s1")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		// The user stops it while the summary is being written.
		svc.CompactLLM = func(context.Context) llm.LLM { return cancellingLLM{cancel: cancel} }
		if _, err := svc.ManualCompactSession(ctx, "s1", "manual"); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want cancellation", err)
		}
		events := sink.all()
		var failed event.ContextCompactFailedPayload
		if end := events[len(events)-1]; end.Type != event.RunEventContextCompactError || json.Unmarshal(end.Payload, &failed) != nil {
			t.Fatalf("last event = %s", end.Type)
		}
		if !failed.Cancelled || failed.Error != "" {
			t.Fatalf("cancelled = %+v", failed)
		}
		if boundary, _, _ := svc.Sessions.LatestCompactBoundary(context.Background(), "s1"); boundary != 0 {
			t.Fatal("a cancelled compaction left a checkpoint behind")
		}
	})
	t.Run("nothing to compact", func(t *testing.T) {
		sink := &recordingSink{}
		svc := Service{Sessions: newCompactTestStore(t), Events: sink, CompactLLM: func(context.Context) llm.LLM { return checkpointTestLLM{summary: "x"} }}
		if _, err := svc.ManualCompactSession(context.Background(), "empty", "manual"); err == nil {
			t.Fatal("an empty session must refuse")
		}
		events := sink.all()
		if len(events) != 2 || events[0].Type != event.RunEventContextCompacting || events[1].Type != event.RunEventContextCompactError {
			t.Fatalf("events = %v, want the card started and then failed with the reason", events)
		}
	})
}

// TestSubagentCompactionIsAddressedToItsView pins where a mid-turn
// compaction of a subagent is shown: the conversation's session, tagged with
// the subagent's roster key and its run, while the checkpoint itself is
// written to the subagent's worker transcript.
func TestSubagentCompactionIsAddressedToItsView(t *testing.T) {
	sink, svc := seedCompactSession(t, "worker")
	ctx := tool.WithConversationSessionID(context.Background(), "conversation")
	ctx = tool.WithHookAgentID(ctx, "explorer-1")
	ctx = tool.WithRunID(ctx, "run-child")
	history, err := svc.Sessions.ListTranscriptMessages(context.Background(), "worker", 100)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := svc.TryCompactOnMessages(ctx, history, "worker", false, nil); err != nil || !ok {
		t.Fatalf("compact: ok=%v err=%v", ok, err)
	}
	for _, evt := range sink.all() {
		if evt.SessionID != "conversation" || evt.RunID != "run-child" {
			t.Fatalf("%s addressed to session %q run %q", evt.Type, evt.SessionID, evt.RunID)
		}
		var tagged struct {
			AgentID string `json:"agent_id"`
		}
		if json.Unmarshal(evt.Payload, &tagged) != nil || tagged.AgentID != "explorer-1" {
			t.Fatalf("%s carries agent %q", evt.Type, tagged.AgentID)
		}
	}
	if boundary, _, _ := svc.Sessions.LatestCompactBoundary(context.Background(), "worker"); boundary == 0 {
		t.Fatal("the checkpoint belongs to the worker transcript")
	}
}

// TestPostCompactHookCannotUndoACommittedCheckpoint pins that a failing
// PostCompact hook leaves the compaction a success: the checkpoint is already
// committed, and a mid-turn caller told otherwise would keep the uncompacted
// history and persist the compacted prefix after the new boundary.
func TestPostCompactHookCannotUndoACommittedCheckpoint(t *testing.T) {
	sink, svc := seedCompactSession(t, "s1")
	svc.PostCompact = func(context.Context, string, string) error { return errors.New("hook exploded") }
	history, err := svc.Sessions.ListTranscriptMessages(context.Background(), "s1", 100)
	if err != nil {
		t.Fatal(err)
	}
	replaced, ok, err := svc.TryCompactOnMessages(context.Background(), history, "s1", true, nil)
	if err != nil || !ok || len(replaced) == 0 {
		t.Fatalf("compact: ok=%v err=%v", ok, err)
	}
	events := sink.all()
	var done event.ContextCompactedPayload
	if end := events[len(events)-1]; end.Type != event.RunEventContextCompacted || json.Unmarshal(end.Payload, &done) != nil {
		t.Fatalf("last event = %s", end.Type)
	}
	if !done.Reactive || !strings.Contains(done.Reason, "context window was exceeded") {
		t.Fatalf("a reactive compaction must say so: %+v", done)
	}
	if _, err := svc.ManualCompactSession(context.Background(), "s1", "manual"); err != nil {
		t.Fatalf("manual compaction failed on the hook: %v", err)
	}
}

// TestFormatCompactBannerReadsAsOneLine pins the banner's wording: counts
// without a unit glued to them, the saving as a percentage, and the time.
func TestFormatCompactBannerReadsAsOneLine(t *testing.T) {
	got := FormatCompactBanner(event.ContextCompactedPayload{TokensBefore: 182_400, TokensAfter: 12_300, ReplacedItems: 1}, 14_200_000_000)
	if got != "182.4k → 12.3k tokens (−93%) · 14.2s" {
		t.Fatalf("banner = %q", got)
	}
	if got := FormatCompactBanner(event.ContextCompactedPayload{TokensBefore: 355, TokensAfter: 355}, 0); got != "355 → 355 tokens" {
		t.Fatalf("small banner = %q", got)
	}
	if FormatTokenCount(40_000) != "40k" || FormatTokenCount(1_250_000) != "1.3M" || FormatTokenCount(12_250) != "12.3k" {
		t.Fatalf("token counts = %q %q %q", FormatTokenCount(40_000), FormatTokenCount(1_250_000), FormatTokenCount(12_250))
	}
}

// slowFirstTokenLLM takes a while before its first token, the way a provider
// reads a long history before it answers.
type slowFirstTokenLLM struct {
	wait  time.Duration
	words int
}

func (f slowFirstTokenLLM) Execute(ctx context.Context, _ []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	select {
	case <-time.After(f.wait):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	sink := llm.StreamSinkFrom(ctx)
	for i := 0; i < f.words; i++ {
		if sink != nil && sink.OnDelta != nil {
			sink.OnDelta("summary ")
		}
	}
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text(strings.Repeat("summary ", f.words))})
	return &llm.Result{Message: &msg}, nil
}

// TestCompactionProgressAdvancesWhileTheModelReads pins the reading phase: the
// bar moves with the clock before the first token, then hands over to the
// summary without ever going back, and the phases arrive in order.
func TestCompactionProgressAdvancesWhileTheModelReads(t *testing.T) {
	sink, svc := seedCompactSession(t, "s1")
	svc.CompactLLM = func(context.Context) llm.LLM { return slowFirstTokenLLM{wait: 900 * time.Millisecond, words: 300} }
	if _, err := svc.ManualCompactSession(context.Background(), "s1", "manual"); err != nil {
		t.Fatal(err)
	}
	var phases []string
	last := -1
	readingSteps := 0
	for _, evt := range sink.all() {
		if evt.Type != event.RunEventContextCompactProgress {
			continue
		}
		var p event.ContextCompactProgressPayload
		if json.Unmarshal(evt.Payload, &p) != nil {
			t.Fatal("bad progress payload")
		}
		if p.Percent < last {
			t.Fatalf("progress went back %d → %d", last, p.Percent)
		}
		last = p.Percent
		if len(phases) == 0 || phases[len(phases)-1] != p.Phase {
			phases = append(phases, p.Phase)
		}
		if p.Phase == event.CompactPhaseReading {
			readingSteps++
		}
	}
	want := []string{event.CompactPhaseReading, event.CompactPhaseSummarizing, event.CompactPhaseSaving}
	if strings.Join(phases, ",") != strings.Join(want, ",") {
		t.Fatalf("phases = %v, want %v in order", phases, want)
	}
	if readingSteps < 2 {
		t.Fatalf("the bar did not move while the model read: %d reading reports", readingSteps)
	}
}

func TestProgressCurvesStayInsideTheirPhases(t *testing.T) {
	if got := readingProgressPercent(0, 100_000); got != readingStartPercent {
		t.Fatalf("reading at 0s = %d", got)
	}
	if got := readingProgressPercent(10*time.Minute, 100_000); got >= readingEndPercent {
		t.Fatalf("reading never completes its phase, got %d", got)
	}
	// A typical checkpoint — a couple of thousand tokens — ends well into the
	// summarizing phase, so the last step to done is short.
	if got := summaryProgressPercent(20, 2000, 150_000); got < 75 || got >= summaryEndPercent {
		t.Fatalf("summary of 2000 tokens = %d%%", got)
	}
	if got := summaryProgressPercent(20, 100_000, 150_000); got >= summaryEndPercent {
		t.Fatalf("summary never completes its phase, got %d", got)
	}
}

// fixedSummaryLLM is the stand-alone summarizer; it counts how often it was
// asked.
type fixedSummaryLLM struct {
	calls *int
	text  string
}

func (f fixedSummaryLLM) Execute(context.Context, []llm.Message, []*llm.Tool) (*llm.Result, error) {
	*f.calls++
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text(f.text)})
	return &llm.Result{Message: &msg}, nil
}

// TestSummaryIsAskedOfTheConversationFirst pins where a compaction's summary
// comes from: the conversation's own request, which reuses its cached prefix,
// whenever that request can carry it; the stand-alone request only when it
// cannot — the model answered with a tool call, or the history no longer
// fits — and never for a compaction forced by an overflow.
func TestSummaryIsAskedOfTheConversationFirst(t *testing.T) {
	answer := func(text string, calls ...llm.ToolCall) *llm.Result {
		msg := llm.AssistantMessage([]llm.ContentPart{llm.Text(text)}, calls...)
		return &llm.Result{Message: &msg}
	}
	cases := []struct {
		name         string
		reply        *llm.Result
		err          error
		wantSummary  string
		wantFallback bool
	}{
		{name: "conversation answers", reply: answer("from the conversation"), wantSummary: "from the conversation"},
		{name: "tool call instead", reply: answer("", llm.ToolCall{ID: "c1", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "read_file", Arguments: "{}"}}), wantSummary: "stand-alone", wantFallback: true},
		{name: "does not fit", err: errors.New("context_length_exceeded: maximum context length"), wantSummary: "stand-alone", wantFallback: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, svc := seedCompactSession(t, "s1")
			fallbackCalls := 0
			svc.CompactLLM = func(context.Context) llm.LLM { return fixedSummaryLLM{calls: &fallbackCalls, text: "stand-alone"} }
			var instruction llm.Message
			svc.ConversationSummary = func(sessionID string) ConversationSummarizer {
				if sessionID != "s1" {
					t.Fatalf("summarizer for %q", sessionID)
				}
				return func(_ context.Context, msg llm.Message) (*llm.Result, error) {
					instruction = msg
					return tc.reply, tc.err
				}
			}
			res, err := svc.ManualCompactSession(context.Background(), "s1", "manual")
			if err != nil {
				t.Fatal(err)
			}
			if res.Summary != tc.wantSummary || (fallbackCalls > 0) != tc.wantFallback {
				t.Fatalf("summary=%q fallback calls=%d", res.Summary, fallbackCalls)
			}
			if !strings.HasSuffix(instruction.TextContent(), summaryReplyInstruction) {
				t.Fatalf("instruction %q does not ask for a plain-text answer", instruction.TextContent())
			}
		})
	}
	t.Run("overflow", func(t *testing.T) {
		_, svc := seedCompactSession(t, "s1")
		history, err := svc.Sessions.ListTranscriptMessages(context.Background(), "s1", 100)
		if err != nil {
			t.Fatal(err)
		}
		conversation := func(context.Context, llm.Message) (*llm.Result, error) {
			t.Fatal("the request the provider refused as too large cannot carry the summary")
			return nil, nil
		}
		if _, ok, err := svc.TryCompactOnMessages(context.Background(), history, "s1", true, conversation); err != nil || !ok {
			t.Fatalf("reactive compaction: ok=%v err=%v", ok, err)
		}
	})
}
