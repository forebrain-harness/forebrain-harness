package google

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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
}

func newRecordingSink() *recordingSink {
	return &recordingSink{streamed: new(bool)}
}

func (r *recordingSink) sink() *llm.StreamSink {
	return &llm.StreamSink{
		OnResponseStarted: func() { r.trace = append(r.trace, "started") },
		OnDelta:           func(s string) { r.trace = append(r.trace, "delta:"+s) },
		OnTextBlockDone:   func() { r.trace = append(r.trace, "text-block-done") },
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
	result, err := client.executeStreaming(ctx, []byte(`{}`))
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
