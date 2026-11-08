package google

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

// recordingSink turns the sink callbacks into an ordered trace. Every callback
// fires on the goroutine reading the stream, so no locking is needed.
type recordingSink struct {
	trace     []string
	streamed  *bool
	ends      int
	ready     *llm.ToolCall
	usage     [][2]int
	snapshots [][2]int
	// reasoning holds every thinking delta in order, and reasoningDone counts
	// the thinking blocks the adapter declared finished. carries holds every
	// reasoning carry the adapter handed over, in order, with its producer.
	reasoning     []string
	reasoningDone int
	carries       []string
	carryModels   []string
}

func newRecordingSink() *recordingSink {
	return &recordingSink{streamed: new(bool)}
}

func (r *recordingSink) sink() *llm.StreamSink {
	return &llm.StreamSink{
		OnResponseStarted: func() { r.trace = append(r.trace, "started") },
		OnDelta:           func(s string) { r.trace = append(r.trace, "delta:"+s) },
		OnReasoningDelta:  func(s string) { r.reasoning = append(r.reasoning, s) },
		OnReasoningDone:   func() { r.reasoningDone++ },
		OnReasoningCarry: func(carry, model string) {
			r.carries = append(r.carries, carry)
			r.carryModels = append(r.carryModels, model)
		},
		OnTextBlockDone: func() { r.trace = append(r.trace, "text-block-done") },
		OnToolCallReady: func(c llm.ToolCall) {
			r.ready = &c
			r.trace = append(r.trace, "tool-ready:"+c.Function.Name)
		},
		OnStopReason:    func(reason string) { r.trace = append(r.trace, "stop:"+reason) },
		OnMessageStop:   func() { r.trace = append(r.trace, "message-stop") },
		OnUsage:         func(in, out int) { r.usage = append(r.usage, [2]int{in, out}) },
		OnUsageSnapshot: func(in, out int) { r.snapshots = append(r.snapshots, [2]int{in, out}) },
		OnEnd:           func() { r.ends++ },
		Streamed:        r.streamed,
	}
}

// runStream executes one streaming call against a server with a recording
// sink in the context.
func runStream(t *testing.T, handler http.HandlerFunc) (*llm.Result, error, *recordingSink) {
	t.Helper()
	server := httptest.NewServer(handler)
	defer server.Close()
	rec := newRecordingSink()
	ctx := llm.WithStreamSink(context.Background(), rec.sink())
	client := NewAgentLLM("", server.URL, "gemini-3.5-pro", 0, nil).(*agentLLM)
	// Execute, not executeStreaming: the sink's OnEnd contract belongs to
	// Execute now (once per call, over the whole continuation loop), so the
	// recorded behavior is the public one.
	result, err := client.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil)
	return result, err, rec
}

func TestStreamForwardsTextCallsAndFinishInOrder(t *testing.T) {
	result, err, rec := runStream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveSSE(w,
			`{"candidates":[{"content":{"parts":[{"text":"Hel"}]}}]}`,
			`{"candidates":[{"content":{"parts":[{"text":"lo"}]}}]}`,
			`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"weather","args":{"city":"Paris"}}}]}}]}`,
			`{"candidates":[{"content":{"parts":[{"text":"after"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":7}}`,
		)
	}))
	if err != nil {
		t.Fatalf("executeStreaming: %v", err)
	}
	want := []string{
		"started",
		"delta:Hel",
		"delta:lo",
		"text-block-done",
		"tool-ready:weather",
		"delta:after",
		"text-block-done",
		"stop:STOP",
		"message-stop",
	}
	if fmt.Sprint(rec.trace) != fmt.Sprint(want) {
		t.Fatalf("trace = %v\nwant %v", rec.trace, want)
	}
	if result == nil || result.Message == nil {
		t.Fatalf("result = %#v", result)
	}
	if len(result.Message.Parts) != 2 ||
		result.Message.Parts[0].Text != "Hello" || result.Message.Parts[1].Text != "after" {
		t.Fatalf("text parts = %#v, want one part per block", result.Message.Parts)
	}
	if len(result.Message.ToolCalls) != 1 {
		t.Fatalf("tool calls = %#v", result.Message.ToolCalls)
	}
	// The ready call and the final one are the same value, id included.
	if rec.ready == nil || *rec.ready != result.Message.ToolCalls[0] {
		t.Fatalf("ready = %#v, final = %#v, want identical values", rec.ready, result.Message.ToolCalls[0])
	}
	if result.Usage == nil || result.Usage.InputTokens != 10 || result.Usage.OutputTokens != 7 {
		t.Fatalf("usage = %#v, want the last cumulative figure", result.Usage)
	}
}

func TestStreamUsageIsReportedAsDeltas(t *testing.T) {
	_, err, rec := runStream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveSSE(w,
			`{"candidates":[{"content":{"parts":[{"text":"a"}]}}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5}}`,
			`{"candidates":[{"content":{"parts":[{"text":"b"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":12}}`,
		)
	}))
	if err != nil {
		t.Fatalf("executeStreaming: %v", err)
	}
	if fmt.Sprint(rec.usage) != fmt.Sprint([][2]int{{10, 5}, {0, 7}}) {
		t.Fatalf("usage deltas = %v, want the increments of the cumulative figures", rec.usage)
	}
	in, out := 0, 0
	for _, d := range rec.usage {
		in += d[0]
		out += d[1]
	}
	if last := rec.snapshots[len(rec.snapshots)-1]; in != last[0] || out != last[1] {
		t.Fatalf("deltas sum to %d/%d, want the final snapshot %d/%d", in, out, last[0], last[1])
	}
}

func TestStreamWithoutFinishAfterOutputIsTruncated(t *testing.T) {
	_, err, _ := runStream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveSSE(w,
			`{"candidates":[{"content":{"parts":[{"text":"Hel"}]}}]}`,
			`{"candidates":[{"content":{"parts":[{"text":"lo"}]}}]}`,
		)
	}))
	if !errors.Is(err, llm.ErrStreamTruncated) {
		t.Fatalf("error = %v, want ErrStreamTruncated", err)
	}
}

func TestStreamInBandErrorIsNormalized(t *testing.T) {
	_, err, _ := runStream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveSSE(w, `{"error":{"code":503,"status":"UNAVAILABLE","message":"overloaded"}}`)
	}))
	var apiErr *llm.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want a *llm.APIError", err)
	}
	if apiErr.Type != "UNAVAILABLE" {
		t.Errorf("type = %q, want UNAVAILABLE", apiErr.Type)
	}
	if apiErr.Message != "overloaded" {
		t.Errorf("message = %q, want overloaded", apiErr.Message)
	}
}

func TestStreamMalformedEventIsReported(t *testing.T) {
	_, err, _ := runStream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveSSE(w, `{not json`)
	}))
	var syntaxErr *json.SyntaxError
	if !errors.As(err, &syntaxErr) {
		t.Fatalf("error = %v, want a *json.SyntaxError inside", err)
	}
}

// A connection cut mid-event must surface its read failure, not a decoded
// half-event, so the network-retry predicate can act on it.
func TestStreamCutMidBodyKeepsTheReadError(t *testing.T) {
	_, err, _ := runStream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("the httptest writer must support hijacking")
			return
		}
		conn, brw, err := hj.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		defer conn.Close()
		_, _ = brw.WriteString("HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\n\r\n")
		_, _ = brw.WriteString("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"par\"}]}}]}\n\n")
		_, _ = brw.WriteString("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":")
		_ = brw.Flush()
	}))
	if err == nil {
		t.Fatal("a cut stream must fail")
	}
	if !llm.IsRetryableNetworkError(err) {
		t.Fatalf("error = %v, want a retryable network failure", err)
	}
}

// A stream that already reported usageMetadata and then dies mid-event must
// leave that usage readable off the returned error: the retry loop adds it to
// the request's total, so a retried attempt's tokens are not silently lost.
func TestStreamFailedAttemptCarriesItsUsage(t *testing.T) {
	_, err, _ := runStream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("the httptest writer must support hijacking")
			return
		}
		conn, brw, err := hj.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		defer conn.Close()
		_, _ = brw.WriteString("HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\n\r\n")
		_, _ = brw.WriteString("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Hel\"}]}}],\"usageMetadata\":{\"promptTokenCount\":210,\"candidatesTokenCount\":9}}\n\n")
		_, _ = brw.WriteString("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":")
		_ = brw.Flush()
	}))
	if err == nil {
		t.Fatal("a cut stream must fail")
	}
	usage := llm.UsageFromError(err)
	if usage == nil {
		t.Fatalf("error = %v, want the attempt's usage readable off it", err)
	}
	if usage.InputTokens != 210 || usage.OutputTokens != 9 {
		t.Fatalf("usage = %+v, want the usageMetadata the stream reported", usage)
	}
}

func TestStreamEmptyAnswerKeepsTheEmptyError(t *testing.T) {
	_, err, _ := runStream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveSSE(w, `{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"SAFETY"}]}`)
	}))
	if err == nil || !strings.Contains(err.Error(), `response was empty (finish reason "SAFETY")`) {
		t.Fatalf("error = %v, want the refusal's finish reason", err)
	}
}

func TestStreamCleanCloseZeroEventsIsNoEvents(t *testing.T) {
	// A 200 with the event-stream content type and an immediate clean close:
	// no part, no finish frame. The request is re-sent without streaming.
	_, err, rec := runStream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
	}))
	if !errors.Is(err, llm.ErrStreamNoEvents) {
		t.Fatalf("error = %v, want ErrStreamNoEvents", err)
	}
	if !*rec.streamed {
		t.Error("Streamed was not marked")
	}
}

func TestStreamSetsStreamedAndEnds(t *testing.T) {
	success := func(t *testing.T) {
		_, err, rec := runStream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			serveSSE(w, `{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`)
		}))
		if err != nil {
			t.Fatalf("executeStreaming: %v", err)
		}
		if !*rec.streamed {
			t.Error("Streamed was not set")
		}
		if rec.ends != 1 {
			t.Errorf("OnEnd fired %d times, want 1", rec.ends)
		}
	}
	failure := func(t *testing.T) {
		_, err, rec := runStream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			serveSSE(w, `{"candidates":[{"content":{"parts":[{"text":"cut"}]}}]}`)
		}))
		if err == nil {
			t.Fatal("a stream without a finish frame must fail")
		}
		if !*rec.streamed {
			t.Error("Streamed was not set")
		}
		if rec.ends != 1 {
			t.Errorf("OnEnd fired %d times, want 1", rec.ends)
		}
	}
	t.Run("success", success)
	t.Run("failure", failure)
}

// A streaming round that ends MAX_TOKENS with no parts is a cut before any
// answer (the thinking spent the budget), not an empty-response error: it is
// re-asked with the thinking-only prompt, and once the recovery budget is
// spent the accumulated fragment comes back with the output-limit reason.
func TestGoogleStreamingMaxTokensContinues(t *testing.T) {
	type wireContent struct {
		Role  string `json:"role"`
		Parts []struct {
			Text string `json:"text"`
		} `json:"parts"`
	}
	type wireReq struct {
		Contents []wireContent `json:"contents"`
	}
	var mu sync.Mutex
	var bodies []wireReq
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req wireReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		round := len(bodies) + 1
		bodies = append(bodies, req)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		switch round {
		case 1:
			serveSSE(w, `{"candidates":[{"finishReason":"MAX_TOKENS"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":0,"thoughtsTokenCount":64}}`)
		case 2:
			serveSSE(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"part2 "}]},"finishReason":"MAX_TOKENS"}]}`, `{"candidates":[{"finishReason":"MAX_TOKENS"}],"usageMetadata":{"promptTokenCount":20,"candidatesTokenCount":5,"thoughtsTokenCount":0}}`)
		case 3:
			serveSSE(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"part3 "}]},"finishReason":"MAX_TOKENS"}]}`, `{"candidates":[{"finishReason":"MAX_TOKENS"}],"usageMetadata":{"promptTokenCount":30,"candidatesTokenCount":5,"thoughtsTokenCount":0}}`)
		default:
			serveSSE(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"part4 "}]},"finishReason":"MAX_TOKENS"}]}`, `{"candidates":[{"finishReason":"MAX_TOKENS"}],"usageMetadata":{"promptTokenCount":40,"candidatesTokenCount":5,"thoughtsTokenCount":0}}`)
		}
	}))
	defer server.Close()

	rec := newRecordingSink()
	ctx := llm.WithStreamSink(context.Background(), rec.sink())
	client := NewAgentLLM("", server.URL, "gemini-3.5-pro", 0, nil).(*agentLLM)
	res, err := client.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 1+llm.OutputLimitContinuations {
		t.Fatalf("requests = %d, want %d (first round plus three continuations)", len(bodies), 1+llm.OutputLimitContinuations)
	}
	// The first round produced no text, so the second request carries only
	// the caller's turn and the thinking-only resume prompt — no model turn.
	if n := len(bodies[1].Contents); n != 2 {
		t.Fatalf("second request carried %d contents, want 2 (caller, thinking-only prompt)", n)
	}
	for i, c := range bodies[1].Contents {
		if c.Role == "model" {
			t.Fatalf("second request content %d is a model turn; the thinking-only resume carries none", i)
		}
	}
	if text := res.Message.TextContent(); text != "part2 part3 part4 " {
		t.Fatalf("fragment = %q", text)
	}
	if res.StopReason != llm.StopReasonOutputLimit {
		t.Fatalf("StopReason = %q, want %q", res.StopReason, llm.StopReasonOutputLimit)
	}
}

// A STOP that billed output tokens (here the thinking) yet delivered nothing
// is a blank output to re-send (plans/273), not a refusal: the error carries
// the sentinel under the same "response was empty" wording.
func TestGoogleStreamingStopEmptyIsBlankOutput(t *testing.T) {
	_, err, _ := runStream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveSSE(w, `{"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":0,"thoughtsTokenCount":688}}`)
	}))
	if !errors.Is(err, llm.ErrBlankModelOutput) {
		t.Fatalf("error = %v, want the blank-output sentinel", err)
	}
	if !strings.Contains(err.Error(), "response was empty") {
		t.Fatalf("error = %v, want the empty-response wording kept", err)
	}
}

// An empty SAFETY turn stays a plain refusal error even when it billed output:
// re-sending the same request would only be refused again.
func TestGoogleStreamingSafetyEmptyIsNotBlankOutput(t *testing.T) {
	_, err, _ := runStream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveSSE(w, `{"candidates":[{"finishReason":"SAFETY"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":0,"thoughtsTokenCount":688}}`)
	}))
	if err == nil {
		t.Fatal("an empty SAFETY response must stay an error")
	}
	if errors.Is(err, llm.ErrBlankModelOutput) {
		t.Fatalf("error = %v, a refusal must not read as a blank output", err)
	}
}

// A streamed thought summary is thinking, not answer text, and the signature that
// closes it stays on that thought's own part. The answer text and the call are
// recorded in arrival order, and a signature delivered on an empty text part is
// kept too: it belongs to this reply and must come back with it.
func TestGoogleStreamingKeepsThoughtSignatures(t *testing.T) {
	result, err, rec := runStream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveSSE(w,
			`{"candidates":[{"content":{"parts":[{"text":"pla","thought":true}]}}]}`,
			`{"candidates":[{"content":{"parts":[{"text":"n","thought":true,"thoughtSignature":"A"}]}}]}`,
			`{"candidates":[{"content":{"parts":[{"text":"Hel"}]}}]}`,
			`{"candidates":[{"content":{"parts":[{"text":"lo"}]}}]}`,
			`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"weather"},"thoughtSignature":"B"}]}}]}`,
			`{"candidates":[{"content":{"parts":[{"text":"","thoughtSignature":"C"}]},"finishReason":"STOP"}]}`,
		)
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if text := result.Message.TextContent(); text != "Hello" {
		t.Fatalf("answer = %q, want the answer text alone", text)
	}
	if fmt.Sprint(rec.reasoning) != fmt.Sprint([]string{"pla", "n"}) {
		t.Fatalf("reasoning deltas = %v, want the thought summary in two deltas", rec.reasoning)
	}
	if rec.reasoningDone != 1 {
		t.Fatalf("OnReasoningDone fired %d times, want 1", rec.reasoningDone)
	}
	layout, ok := llm.DecodeGeminiParts(result.Message.Name)
	if !ok {
		t.Fatalf("carry = %q, want a Gemini part layout", result.Message.Name)
	}
	want := []llm.GeminiPart{
		{Kind: "thought", Text: "plan", Signature: "A"},
		{Kind: "text", Text: "Hello"},
		{Kind: "call", Call: 0, Signature: "B"},
		{Kind: "text", Signature: "C"},
	}
	if !reflect.DeepEqual(layout, want) {
		t.Fatalf("layout = %#v\nwant      %#v", layout, want)
	}
	if result.Message.ReasoningModel != "gemini-3.5-pro" {
		t.Fatalf("ReasoningModel = %q, want the client's model", result.Message.ReasoningModel)
	}
}

// Two signed text chunks are two parts: Gemini cannot merge two signatures, so
// each keeps its own and the answer still reads as both chunks.
func TestGoogleStreamingNeverMergesSignedParts(t *testing.T) {
	result, err, _ := runStream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveSSE(w,
			`{"candidates":[{"content":{"parts":[{"text":"A","thoughtSignature":"S1"}]}}]}`,
			`{"candidates":[{"content":{"parts":[{"text":"B","thoughtSignature":"S2"}]},"finishReason":"STOP"}]}`,
		)
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	layout, ok := llm.DecodeGeminiParts(result.Message.Name)
	if !ok {
		t.Fatalf("carry = %q, want a Gemini part layout", result.Message.Name)
	}
	want := []llm.GeminiPart{
		{Kind: "text", Text: "A", Signature: "S1"},
		{Kind: "text", Text: "B", Signature: "S2"},
	}
	if !reflect.DeepEqual(layout, want) {
		t.Fatalf("layout = %#v\nwant      %#v", layout, want)
	}
	// Both chunks are answer text, one part each: TextContent joins parts with
	// a newline.
	if text := result.Message.TextContent(); text != "A\nB" {
		t.Fatalf("answer = %q, want both chunks", text)
	}
}

// A signed function-call part arrives complete, so the layout the stream has
// built is replayable from that moment: the carry has to be handed over before
// the call is reported ready, because that notice starts a provisional message
// written from the sink. The handed-over layout is the one the finished reply
// carries.
func TestGoogleStreamHandsOverSignedParts(t *testing.T) {
	result, err, rec := runStream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveSSE(w,
			`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"weather","args":{"city":"Paris"}},"thoughtSignature":"B"}]}}]}`,
			`{"candidates":[{"content":{"parts":[{"text":"done"}]},"finishReason":"STOP"}]}`,
		)
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(rec.carries) != 1 {
		t.Fatalf("carries = %d, want exactly 1: the signed call is the one complete part", len(rec.carries))
	}
	layout, ok := llm.DecodeGeminiParts(rec.carries[0])
	if !ok || len(layout) != 1 {
		t.Fatalf("handed-over carry = %q, want a layout holding the call", rec.carries[0])
	}
	want := llm.GeminiPart{Kind: "call", Call: 0, Signature: "B"}
	if layout[0] != want {
		t.Fatalf("handed-over part = %#v, want %#v", layout[0], want)
	}
	if rec.carryModels[0] != "gemini-3.5-pro" {
		t.Fatalf("carry model = %q, want the client's model", rec.carryModels[0])
	}
	// The call was reported ready after the carry: the ready entry is in the
	// trace, and the carry was the only one before it.
	if rec.ready == nil || rec.ready.Function.Name != "weather" {
		t.Fatalf("ready = %+v, want the weather call", rec.ready)
	}
	// The finished reply carries the whole layout: the call the stream handed
	// over, then the answer text that followed it.
	finished, ok := llm.DecodeGeminiParts(result.Message.Name)
	if !ok {
		t.Fatalf("finished reply carry = %q, want a layout carry", result.Message.Name)
	}
	wantLayout := []llm.GeminiPart{want, {Kind: "text", Text: "done"}}
	if !reflect.DeepEqual(finished, wantLayout) {
		t.Fatalf("finished layout = %#v\nwant           %#v", finished, wantLayout)
	}
}

// A part that carries no signature and no thought is not reasoning: nothing is
// handed over until the reply really has some.
func TestGoogleStreamHandsOverNothingWithoutSignedParts(t *testing.T) {
	_, err, rec := runStream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveSSE(w,
			`{"candidates":[{"content":{"parts":[{"text":"plain"}]},"finishReason":"STOP"}]}`,
		)
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(rec.carries) != 0 {
		t.Fatalf("carries = %v, want none: the reply held no reasoning", rec.carries)
	}
}

// The non-streaming path replays a whole reply at once, so its layout is
// complete before the first replayed callback: the carry has to be handed over
// ahead of the replay, whose tool call starts a provisional message.
func TestGoogleUnaryHandsOverCarryBeforeReplay(t *testing.T) {
	const body = `{"candidates":[{"content":{"parts":[{"text":"plan","thought":true,"thoughtSignature":"A"},{"functionCall":{"name":"weather","args":{"city":"Paris"}},"thoughtSignature":"B"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	rec := newRecordingSink()
	ctx := llm.WithNonStreamingRequest(llm.WithStreamSink(context.Background(), rec.sink()))
	client := NewAgentLLM("", server.URL, "gemini-3.5-pro", 0, nil)
	if _, err := client.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(rec.carries) != 1 {
		t.Fatalf("carries = %d, want exactly 1", len(rec.carries))
	}
	layout, ok := llm.DecodeGeminiParts(rec.carries[0])
	if !ok {
		t.Fatalf("handed-over carry = %q, want a layout", rec.carries[0])
	}
	want := []llm.GeminiPart{
		{Kind: "thought", Text: "plan", Signature: "A"},
		{Kind: "call", Call: 0, Signature: "B"},
	}
	if !reflect.DeepEqual(layout, want) {
		t.Fatalf("handed-over layout = %#v\nwant                %#v", layout, want)
	}
	if rec.carryModels[0] != "gemini-3.5-pro" {
		t.Fatalf("carry model = %q, want the client's model", rec.carryModels[0])
	}
	// The carry came before the replayed call: the trace holds the ready entry
	// and nothing but the carry preceded it.
	if rec.ready == nil || rec.ready.Function.Name != "weather" {
		t.Fatalf("ready = %+v, want the weather call", rec.ready)
	}
}
