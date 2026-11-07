package telemetry

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	tracetest "go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestRedactHeaderValue(t *testing.T) {
	if got := redactHeaderValue("Authorization", "Bearer abc123"); got != "[REDACTED]" {
		t.Fatalf("authorization not redacted: %q", got)
	}
	if got := redactHeaderValue("X-API-Key", "sk-live-xxx"); got != "[REDACTED]" {
		t.Fatalf("x-api-key not redacted: %q", got)
	}
	if got := redactHeaderValue("X-Test", "Bearer abc123"); strings.Contains(got, "abc123") {
		t.Fatalf("bearer value should be redacted: %q", got)
	}
}

func TestRedactURL(t *testing.T) {
	u, err := url.Parse("https://user:pass@example.com/v1/chat?api_key=abc&foo=bar&token=xyz")
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	got := redactURL(u)
	if strings.Contains(got, "abc") || strings.Contains(got, "xyz") || strings.Contains(got, "user") || strings.Contains(got, "pass") {
		t.Fatalf("url contains secret: %q", got)
	}
	if !strings.Contains(got, "foo=bar") {
		t.Fatalf("non-sensitive query param should remain: %q", got)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestHTTPTraceRoundTripperRedactsHTTPSpanPayload(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider()
	tp.RegisterSpanProcessor(sr)
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	defer func() {
		_ = tp.Shutdown(context.Background())
		otel.SetTracerProvider(prev)
	}()

	ctx, endRun := StartRunSpan(context.Background(), "run-http", "session-http", "test")
	defer endRun()

	rt := newHTTPTraceRoundTripper(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Proto:      "HTTP/1.1",
			Header: http.Header{
				"Authorization": []string{"Bearer response-secret"},
				"Content-Type":  []string{"application/json"},
				"X-Test":        []string{"Bearer response-test-secret"},
			},
			Body:    io.NopCloser(strings.NewReader(`{"ok":true,"token":"Bearer response-body-secret"}`)),
			Request: req,
		}, nil
	}))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://user:pass@example.com/v1/responses?api_key=req-query-secret&safe=value", strings.NewReader(`{"input":"Bearer request-body-secret"}`))
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}
	req.Header.Set("Authorization", "Bearer request-secret")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", "request-api-key-secret")
	req.Header.Set("X-Test", "Bearer request-test-secret")

	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if resp == nil {
		t.Fatal("expected response")
	}

	for _, sp := range sr.Ended() {
		if sp.Name() != "tool.llm_http" {
			continue
		}
		var reqHeaders string
		var reqBody string
		var respHeaders string
		var respBody string
		var urlText string
		for _, attr := range sp.Attributes() {
			switch string(attr.Key) {
			case "forebrain.http.url":
				urlText = attr.Value.AsString()
			case "forebrain.http.request_headers":
				reqHeaders = attr.Value.AsString()
			case "forebrain.http.request_body":
				reqBody = attr.Value.AsString()
			case "forebrain.http.response_headers":
				respHeaders = attr.Value.AsString()
			case "forebrain.http.response_body":
				respBody = attr.Value.AsString()
			}
		}
		for _, forbidden := range []string{
			"user",
			"pass",
			"req-query-secret",
			"request-secret",
			"request-api-key-secret",
			"request-test-secret",
			"request-body-secret",
			"response-secret",
			"response-test-secret",
			"response-body-secret",
		} {
			if strings.Contains(urlText, forbidden) ||
				strings.Contains(reqHeaders, forbidden) ||
				strings.Contains(reqBody, forbidden) ||
				strings.Contains(respHeaders, forbidden) ||
				strings.Contains(respBody, forbidden) {
				t.Fatalf("secret %q leaked into span attrs: url=%q reqHeaders=%q reqBody=%q respHeaders=%q respBody=%q", forbidden, urlText, reqHeaders, reqBody, respHeaders, respBody)
			}
		}
		if !strings.Contains(urlText, "safe=value") {
			t.Fatalf("non-sensitive query param should remain in url attr: %q", urlText)
		}
		if !strings.Contains(reqHeaders, "Authorization: [REDACTED]") ||
			!strings.Contains(reqHeaders, "X-Api-Key: [REDACTED]") ||
			!strings.Contains(reqHeaders, "X-Test: Bearer [REDACTED]") ||
			!strings.Contains(reqHeaders, "Content-Type: application/json") {
			t.Fatalf("unexpected request headers attr: %q", reqHeaders)
		}
		if !strings.Contains(reqBody, `Bearer [REDACTED]`) {
			t.Fatalf("unexpected request body attr: %q", reqBody)
		}
		if !strings.Contains(respHeaders, "Authorization: [REDACTED]") ||
			!strings.Contains(respHeaders, "X-Test: Bearer [REDACTED]") ||
			!strings.Contains(respHeaders, "Content-Type: application/json") {
			t.Fatalf("unexpected response headers attr: %q", respHeaders)
		}
		if !strings.Contains(respBody, `Bearer [REDACTED]`) {
			t.Fatalf("unexpected response body attr: %q", respBody)
		}
		return
	}
	t.Fatal("llm_http span not found")
}

func TestHTTPTraceRoundTripperDoesNotDependOnDebugFileLogFlag(t *testing.T) {
	t.Setenv("FOREBRAIN_LLM_HTTP_DEBUG", "0")

	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider()
	tp.RegisterSpanProcessor(sr)
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	defer func() {
		_ = tp.Shutdown(context.Background())
		otel.SetTracerProvider(prev)
	}()

	ctx, endRun := StartRunSpan(context.Background(), "run-http-no-debug", "session-http", "test")
	defer endRun()

	rt := newHTTPTraceRoundTripper(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 204,
			Status:     "204 No Content",
			Proto:      "HTTP/1.1",
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    req,
		}, nil
	}))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.com/ping", nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}

	for _, sp := range sr.Ended() {
		if sp.Name() == "tool.llm_http" {
			return
		}
	}
	t.Fatal("llm_http span not found when debug file log disabled")
}

func TestHTTPTraceRoundTripperPassesThroughEventStreamBeforeEOF(t *testing.T) {
	t.Setenv("FOREBRAIN_LLM_HTTP_DEBUG", "0")

	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider()
	tp.RegisterSpanProcessor(sr)
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	defer func() {
		_ = tp.Shutdown(context.Background())
		otel.SetTracerProvider(prev)
	}()

	ctx, endRun := StartRunSpan(context.Background(), "run-http-sse", "session-http", "test")
	defer endRun()

	reader, writer := io.Pipe()
	rt := newHTTPTraceRoundTripper(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Proto:      "HTTP/1.1",
			Header: http.Header{
				"Content-Type": []string{"text/event-stream; charset=utf-8"},
				"X-Trace":      []string{"original"},
			},
			Body:    reader,
			Request: req,
		}, nil
	}))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.com/v1/messages", nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}

	type roundTripResult struct {
		resp *http.Response
		err  error
	}
	resultCh := make(chan roundTripResult, 1)
	go func() {
		resp, err := rt.RoundTrip(req)
		resultCh <- roundTripResult{resp: resp, err: err}
	}()
	var result roundTripResult
	select {
	case result = <-resultCh:
	case <-time.After(time.Second):
		t.Fatal("SSE RoundTrip blocked waiting for response EOF")
	}
	if result.err != nil || result.resp == nil {
		t.Fatalf("RoundTrip result = %#v err=%v", result.resp, result.err)
	}
	if got := countEndedSpans(sr, "tool.llm_http"); got != 0 {
		t.Fatalf("ended spans before body consumption = %d, want 0", got)
	}
	// Outer response-normalizing transports may mutate these fields while the
	// stream is consumed. The trace observer must use the metadata snapshot it
	// captured before returning the response.
	result.resp.StatusCode = http.StatusNoContent
	result.resp.Header.Set("X-Trace", "mutated")

	first := []byte("data: first\n\n")
	writeDone := make(chan error, 1)
	go func() {
		_, err := writer.Write(first)
		writeDone <- err
	}()
	buf := make([]byte, len(first))
	if _, err := io.ReadFull(result.resp.Body, buf); err != nil {
		t.Fatalf("read first SSE chunk: %v", err)
	}
	if string(buf) != string(first) {
		t.Fatalf("first chunk = %q, want %q", buf, first)
	}
	if err := <-writeDone; err != nil {
		t.Fatalf("write first SSE chunk: %v", err)
	}
	if got := countEndedSpans(sr, "tool.llm_http"); got != 0 {
		t.Fatalf("ended spans before EOF = %d, want 0", got)
	}

	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	if _, err := io.ReadAll(result.resp.Body); err != nil {
		t.Fatalf("drain body: %v", err)
	}
	if err := result.resp.Body.Close(); err != nil {
		t.Fatalf("close body: %v", err)
	}
	if got := countEndedSpans(sr, "tool.llm_http"); got != 1 {
		t.Fatalf("ended spans after EOF and close = %d, want 1", got)
	}
	for _, sp := range sr.Ended() {
		if sp.Name() != "tool.llm_http" {
			continue
		}
		for _, attr := range sp.Attributes() {
			switch string(attr.Key) {
			case "forebrain.http.status_code":
				if got := attr.Value.AsInt64(); got != http.StatusOK {
					t.Fatalf("recorded status = %d, want %d", got, http.StatusOK)
				}
			case "forebrain.http.response_headers":
				got := attr.Value.AsString()
				if !strings.Contains(got, "X-Trace: original") || strings.Contains(got, "mutated") {
					t.Fatalf("recorded response headers = %q, want original snapshot", got)
				}
			}
		}
	}
}

func TestHTTPDebugFileLogRoundTripperPassesThroughEventStreamBeforeEOF(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "logs"), 0o755); err != nil {
		t.Fatalf("mkdir logs: %v", err)
	}
	t.Setenv("FOREBRAIN_HOME", tmp)
	t.Setenv("FOREBRAIN_LLM_HTTP_DEBUG", "1")
	resetEmitForTest(t)

	reader, writer := io.Pipe()
	rt := newHTTPDebugFileLogRoundTripper(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Proto:      "HTTP/1.1",
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
				"X-Trace":      []string{"original"},
			},
			Body:    reader,
			Request: req,
		}, nil
	}))
	req, err := http.NewRequest(http.MethodPost, "https://example.com/v1/responses", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	resultCh := make(chan *http.Response, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := rt.RoundTrip(req)
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- resp
	}()
	var resp *http.Response
	select {
	case resp = <-resultCh:
	case err := <-errCh:
		t.Fatalf("RoundTrip: %v", err)
	case <-time.After(time.Second):
		t.Fatal("debug SSE RoundTrip blocked waiting for response EOF")
	}
	resp.StatusCode = http.StatusNoContent
	resp.Status = "204 No Content"
	resp.Header.Set("X-Trace", "mutated")

	first := []byte("data: first\n\n")
	go func() { _, _ = writer.Write(first) }()
	buf := make([]byte, len(first))
	if _, err := io.ReadFull(resp.Body, buf); err != nil {
		t.Fatalf("read first SSE chunk: %v", err)
	}
	if string(buf) != string(first) {
		t.Fatalf("first chunk = %q, want %q", buf, first)
	}
	logPath := filepath.Join(tmp, "logs", "debug.log")
	if raw, err := os.ReadFile(logPath); err != nil {
		t.Fatalf("read debug log before EOF: %v", err)
	} else if strings.Contains(string(raw), "llm_http response ts=") {
		t.Fatalf("response logged before SSE body completed: %s", raw)
	}
	_ = writer.Close()
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if raw, err := os.ReadFile(logPath); err != nil {
		t.Fatalf("read debug log after EOF: %v", err)
	} else if text := string(raw); !strings.Contains(text, "llm_http response ts=") ||
		!strings.Contains(text, "data: first") ||
		!strings.Contains(text, "status=200") ||
		!strings.Contains(text, "X-Trace: original") ||
		strings.Contains(text, "X-Trace: mutated") {
		t.Fatalf("completed SSE response missing its original metadata or body: %s", text)
	}
}

func TestObservingReadCloserPreservesReadAndFinalizesOnce(t *testing.T) {
	wantErr := io.ErrUnexpectedEOF
	body := &scriptedReadCloser{
		reads:    []scriptedRead{{data: "abc", err: wantErr}},
		closeErr: io.ErrClosedPipe,
	}
	var calls atomic.Int32
	var gotBody string
	var gotErr error
	observed := newObservingReadCloser(body, DefaultRecordBytes, func(data []byte, err error) {
		calls.Add(1)
		gotBody = string(data)
		gotErr = err
	})
	buf := make([]byte, 8)
	n, err := observed.Read(buf)
	if n != 3 || err != wantErr || string(buf[:n]) != "abc" {
		t.Fatalf("Read = (%d, %v, %q), want (3, %v, abc)", n, err, buf[:n], wantErr)
	}
	if closeErr := observed.Close(); closeErr != io.ErrClosedPipe {
		t.Fatalf("Close error = %v, want %v", closeErr, io.ErrClosedPipe)
	}
	if calls.Load() != 1 || gotBody != "abc" || gotErr != wantErr {
		t.Fatalf("finalize = calls=%d body=%q err=%v", calls.Load(), gotBody, gotErr)
	}
}

func TestObservingReadCloserBoundsTelemetrySnapshot(t *testing.T) {
	payload := strings.Repeat("x", DefaultRecordBytes+64*1024)
	var got []byte
	observed := newObservingReadCloser(io.NopCloser(strings.NewReader(payload)), DefaultRecordBytes, func(data []byte, _ error) {
		got = append([]byte(nil), data...)
	})
	forwarded, err := io.ReadAll(observed)
	if err != nil {
		t.Fatal(err)
	}
	if string(forwarded) != payload {
		t.Fatal("observer changed the HTTP response body")
	}
	if len(got) > DefaultRecordBytes+128 {
		t.Fatalf("telemetry snapshot remained unbounded: %d bytes", len(got))
	}
	if !strings.Contains(string(got), "telemetry omitted 65536 bytes") {
		t.Fatalf("missing omission accounting: %q", got[len(got)-80:])
	}
}

func TestObservingReadCloserUnboundedKeepsWholeStream(t *testing.T) {
	payload := strings.Repeat("x", DefaultRecordBytes+64*1024)
	var got []byte
	observed := newObservingReadCloser(io.NopCloser(strings.NewReader(payload)), 0, func(data []byte, _ error) {
		got = append([]byte(nil), data...)
	})
	forwarded, err := io.ReadAll(observed)
	if err != nil {
		t.Fatal(err)
	}
	if string(forwarded) != payload {
		t.Fatal("observer changed the HTTP response body")
	}
	if string(got) != payload {
		t.Fatalf("unbounded snapshot = %d bytes, want %d", len(got), len(payload))
	}
	if strings.Contains(string(got), "telemetry omitted") {
		t.Fatalf("unbounded snapshot still reports omissions: %q", got[len(got)-80:])
	}
}

func TestHTTPDebugFileLogKeepsWholeEventStream(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "logs"), 0o755); err != nil {
		t.Fatalf("mkdir logs: %v", err)
	}
	t.Setenv("FOREBRAIN_HOME", tmp)
	t.Setenv("FOREBRAIN_LLM_HTTP_DEBUG", "1")
	resetEmitForTest(t)

	var payload strings.Builder
	frame := "data: {\"choices\":[{\"delta\":{\"content\":\"abcdefgh\"}}]}\n\n"
	for payload.Len() < 300*1024 {
		payload.WriteString(frame)
	}
	lastFrame := "data: {\"finish_reason\":\"stop\",\"usage\":{\"completion_tokens\":688}}\n\n"
	payload.WriteString(lastFrame)

	rt := newHTTPDebugFileLogRoundTripper(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Proto:      "HTTP/1.1",
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
			},
			Body:    io.NopCloser(strings.NewReader(payload.String())),
			Request: req,
		}, nil
	}))
	req, err := http.NewRequest(http.MethodPost, "https://example.com/v1/responses", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatalf("drain body: %v", err)
	}
	_ = resp.Body.Close()

	raw, err := os.ReadFile(filepath.Join(tmp, "logs", "debug.log"))
	if err != nil {
		t.Fatalf("read debug log: %v", err)
	}
	text := string(raw)
	if !strings.Contains(text, strings.TrimRight(lastFrame, "\n")) {
		t.Fatalf("debug log missing the final SSE frame (finish reason / usage): tail=%q", tailFor(text, 200))
	}
	if strings.Contains(text, "telemetry omitted") || strings.Contains(text, "log bytes omitted") {
		t.Fatalf("SSE response truncated in debug log: %q", tailFor(text, 200))
	}
}

func TestHTTPDebugFileLogKeepsWholeUnaryResponse(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "logs"), 0o755); err != nil {
		t.Fatalf("mkdir logs: %v", err)
	}
	t.Setenv("FOREBRAIN_HOME", tmp)
	t.Setenv("FOREBRAIN_LLM_HTTP_DEBUG", "1")
	resetEmitForTest(t)

	body := `{"content":"` + strings.Repeat("x", 300*1024) + `","marker":"UNIQUE_JSON_TAIL_MARKER"}`
	rt := newHTTPDebugFileLogRoundTripper(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Proto:      "HTTP/1.1",
			Header: http.Header{
				"Content-Type": []string{"application/json"},
			},
			Body:    io.NopCloser(strings.NewReader(body)),
			Request: req,
		}, nil
	}))
	req, err := http.NewRequest(http.MethodPost, "https://example.com/v1/responses", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	raw, err := os.ReadFile(filepath.Join(tmp, "logs", "debug.log"))
	if err != nil {
		t.Fatalf("read debug log: %v", err)
	}
	text := string(raw)
	if !strings.Contains(text, "UNIQUE_JSON_TAIL_MARKER") {
		t.Fatalf("debug log missing the response tail marker: %q", tailFor(text, 200))
	}
	if strings.Contains(text, "log bytes omitted") {
		t.Fatalf("unary response truncated in debug log: %q", tailFor(text, 200))
	}
}

func TestHTTPDebugFileLogBoundsRequestBodyOnce(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "logs"), 0o755); err != nil {
		t.Fatalf("mkdir logs: %v", err)
	}
	t.Setenv("FOREBRAIN_HOME", tmp)
	t.Setenv("FOREBRAIN_LLM_HTTP_DEBUG", "1")
	resetEmitForTest(t)

	reqBody := strings.Repeat("q", 300*1024)
	rt := newHTTPDebugFileLogRoundTripper(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Proto:      "HTTP/1.1",
			Header: http.Header{
				"Content-Type": []string{"application/json"},
			},
			Body:    io.NopCloser(strings.NewReader(`{"ok":true}`)),
			Request: req,
		}, nil
	}))
	req, err := http.NewRequest(http.MethodPost, "https://example.com/v1/responses", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	raw, err := os.ReadFile(filepath.Join(tmp, "logs", "debug.log"))
	if err != nil {
		t.Fatalf("read debug log: %v", err)
	}
	if got := strings.Count(string(raw), "log bytes omitted"); got != 1 {
		t.Fatalf("request body omission markers = %d, want exactly 1 (request body bounded once)", got)
	}
}

func TestHTTPDebugFileLogPairsRequestAndResponse(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "logs"), 0o755); err != nil {
		t.Fatalf("mkdir logs: %v", err)
	}
	t.Setenv("FOREBRAIN_HOME", tmp)
	t.Setenv("FOREBRAIN_LLM_HTTP_DEBUG", "1")
	resetEmitForTest(t)

	roundTrip := func() {
		t.Helper()
		rt := newHTTPDebugFileLogRoundTripper(roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: 200,
				Status:     "200 OK",
				Proto:      "HTTP/1.1",
				Header: http.Header{
					"Content-Type": []string{"application/json"},
				},
				Body:    io.NopCloser(strings.NewReader(`{"ok":true}`)),
				Request: req,
			}, nil
		}))
		req, err := http.NewRequest(http.MethodPost, "https://example.com/v1/responses", strings.NewReader(`{"input":"hi"}`))
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		resp, err := rt.RoundTrip(req)
		if err != nil {
			t.Fatalf("RoundTrip: %v", err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	roundTrip()
	roundTrip()

	raw, err := os.ReadFile(filepath.Join(tmp, "logs", "debug.log"))
	if err != nil {
		t.Fatalf("read debug log: %v", err)
	}
	reqRe := regexp.MustCompile(`llm_http request ts=\S+ id=(\S+)`)
	respRe := regexp.MustCompile(`llm_http response ts=\S+ id=(\S+)`)
	reqIDs := reqRe.FindAllStringSubmatch(string(raw), -1)
	respIDs := respRe.FindAllStringSubmatch(string(raw), -1)
	if len(reqIDs) < 2 || len(respIDs) < 2 {
		t.Fatalf("expected 2 request and 2 response blocks, got %d/%d: %q", len(reqIDs), len(respIDs), tailFor(string(raw), 400))
	}
	first := reqIDs[0][1]
	if first == "" || first != respIDs[0][1] {
		t.Fatalf("first round trip ids do not pair: request=%q response=%q", first, respIDs[0][1])
	}
	second := reqIDs[1][1]
	if second == "" || second != respIDs[1][1] {
		t.Fatalf("second round trip ids do not pair: request=%q response=%q", second, respIDs[1][1])
	}
	if first == second {
		t.Fatalf("two round trips shared one id: %q", first)
	}
}

func TestHTTPTraceSpanStillBoundsResponseBody(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider()
	tp.RegisterSpanProcessor(sr)
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	defer func() {
		_ = tp.Shutdown(context.Background())
		otel.SetTracerProvider(prev)
	}()

	ctx, endRun := StartRunSpan(context.Background(), "run-http-span-bound", "session-http", "test")
	defer endRun()

	payload := strings.Repeat("x", DefaultRecordBytes+64*1024)
	rt := newHTTPTraceRoundTripper(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Proto:      "HTTP/1.1",
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
			},
			Body:    io.NopCloser(strings.NewReader(payload)),
			Request: req,
		}, nil
	}))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.com/v1/messages", nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	forwarded, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("drain body: %v", err)
	}
	_ = resp.Body.Close()
	if string(forwarded) != payload {
		t.Fatal("observer changed the HTTP response body")
	}

	for _, sp := range sr.Ended() {
		if sp.Name() != "tool.llm_http" {
			continue
		}
		for _, attr := range sp.Attributes() {
			if string(attr.Key) != "forebrain.http.response_body" {
				continue
			}
			got := attr.Value.AsString()
			if len(got) > DefaultRecordBytes+128 {
				t.Fatalf("span response_body unbounded: %d bytes", len(got))
			}
			if !strings.Contains(got, "telemetry omitted") {
				t.Fatalf("span response_body missing omission accounting: %q", got[len(got)-80:])
			}
			return
		}
		t.Fatal("llm_http span has no response_body attribute")
	}
	t.Fatal("llm_http span not found")
}

func tailFor(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

type scriptedRead struct {
	data string
	err  error
}

type scriptedReadCloser struct {
	reads    []scriptedRead
	closeErr error
}

func (r *scriptedReadCloser) Read(p []byte) (int, error) {
	if len(r.reads) == 0 {
		return 0, io.EOF
	}
	next := r.reads[0]
	r.reads = r.reads[1:]
	return copy(p, next.data), next.err
}

func (r *scriptedReadCloser) Close() error { return r.closeErr }

func countEndedSpans(sr *tracetest.SpanRecorder, name string) int {
	count := 0
	for _, span := range sr.Ended() {
		if span.Name() == name {
			count++
		}
	}
	return count
}
