package telemetry

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/home"
)

// exchangeLog installs one event log with payload capture on and returns its
// home: where a call's records and its payload files both land.
func exchangeLog(t *testing.T) string {
	t.Helper()
	t.Setenv("FOREBRAIN_LLM_HTTP_DEBUG", "1")
	return withEventLog(t)
}

// sessionKey carries the session an exchange test's request belongs to, which
// is the identity the registered Scope reads back out.
type sessionKey struct{}

// registerSessions installs a Scope that reads the session off the context, the
// way pkg/process does for real, and puts the previous one back.
func registerSessions(t *testing.T) {
	t.Helper()
	RegisterScope(func(ctx context.Context) Scope {
		session, _ := ctx.Value(sessionKey{}).(string)
		return Scope{Session: session}
	})
	t.Cleanup(func() { RegisterScope(nil) })
}

// recordsWithMsg is recordsRead restricted to one message: "llm response".
func recordsWithMsg(t *testing.T, root, msg string) []Record {
	t.Helper()
	var kept []Record
	for _, rec := range recordsRead(t, root) {
		if rec.Msg == msg {
			kept = append(kept, rec)
		}
	}
	return kept
}

// onlyResponse is the one response record of a single call: the half that says
// how it went.
func onlyResponse(t *testing.T, root string) Record {
	t.Helper()
	records := recordsWithMsg(t, root, "llm response")
	if len(records) != 1 {
		t.Fatalf("response records = %+v, want exactly one", records)
	}
	return records[0]
}

// onlyRequest is the one request record of a single call.
func onlyRequest(t *testing.T, root string) Record {
	t.Helper()
	records := recordsWithMsg(t, root, "llm request")
	if len(records) != 1 {
		t.Fatalf("request records = %+v, want exactly one", records)
	}
	return records[0]
}

// payloadPath locates one payload file of the home's day directories.
func payloadPath(t *testing.T, root, name string) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(root, home.LogsDir, "*", blobDirName, name))
	if err != nil {
		t.Fatalf("glob %s: %v", name, err)
	}
	if len(matches) != 1 {
		t.Fatalf("payload %q = %v, want exactly one file", name, matches)
	}
	return matches[0]
}

// gunzipPayload decompresses one payload file, and tolerates a stream that has
// not ended yet: the point of those tests is that the bytes already arrived.
func gunzipPayload(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("gzip %s: %v", path, err)
	}
	body, readErr := io.ReadAll(reader)
	if readErr != nil && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		t.Fatalf("gunzip %s: %v", path, readErr)
	}
	return string(body)
}

// respPayloadName turns a request payload's name into its call's response one.
func respPayloadName(reqName string) string {
	return strings.TrimSuffix(reqName, ".req.gz") + ".resp.gz"
}

// postBody makes one request through rt and reads the whole body back.
func postBody(t *testing.T, rt http.RoundTripper, ctx context.Context, url, body string) (string, *http.Response) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	read, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(read), resp
}

// The core regression: sixteen model calls streaming at once, each with its own
// session and its own marker, must end up in sixteen files and sixteen records
// that pair up one to one. The debug.log this replaced interleaved all of them.
func TestExchangeConcurrentStreamsStayInTheirOwnFiles(t *testing.T) {
	root := exchangeLog(t)
	registerSessions(t)

	const calls = 16
	const events = 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		marker := r.Header.Get("X-Marker")
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		for i := 0; i < events; i++ {
			fmt.Fprintf(w, "data: {\"marker\":%q,\"i\":%d}\n\n", marker, i)
			flusher.Flush()
			time.Sleep(time.Millisecond)
		}
	}))
	defer srv.Close()

	rt := newExchangeRoundTripper(nil)
	var wg sync.WaitGroup
	for i := 0; i < calls; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			marker := fmt.Sprintf("MARKER%02d", i)
			ctx := context.WithValue(context.Background(), sessionKey{}, "session-"+marker)
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL, strings.NewReader(`{"stream":true}`))
			if err != nil {
				t.Errorf("NewRequest: %v", err)
				return
			}
			req.Header.Set("X-Marker", marker)
			resp, err := rt.RoundTrip(req)
			if err != nil {
				t.Errorf("RoundTrip %s: %v", marker, err)
				return
			}
			if _, err := io.Copy(io.Discard, resp.Body); err != nil {
				t.Errorf("drain %s: %v", marker, err)
			}
			_ = resp.Body.Close()
		}(i)
	}
	wg.Wait()

	responseRecords := recordsWithMsg(t, root, "llm response")
	if len(responseRecords) != calls {
		t.Fatalf("response records = %d, want %d", len(responseRecords), calls)
	}
	seen := map[string]bool{}
	for _, rec := range responseRecords {
		if rec.Session == "" {
			t.Fatalf("record %+v has no session", rec)
		}
		if seen[rec.Session] {
			t.Fatalf("session %q recorded twice", rec.Session)
		}
		seen[rec.Session] = true
		marker := strings.TrimPrefix(rec.Session, "session-")
		if rec.Attrs["outcome"] != outcomeOK {
			t.Fatalf("%s outcome = %v, want %s", marker, rec.Attrs["outcome"], outcomeOK)
		}
		ref, ok := rec.Blobs["resp"]
		if !ok {
			t.Fatalf("%s has no response payload", marker)
		}
		text := gunzipPayload(t, payloadPath(t, root, ref.Name))
		if got := strings.Count(text, "data: "); got != events {
			t.Fatalf("%s holds %d events, want %d", marker, got, events)
		}
		if !strings.Contains(text, marker) {
			t.Fatalf("%s holds none of its own marker", marker)
		}
		for other := 0; other < calls; other++ {
			foreign := fmt.Sprintf("MARKER%02d", other)
			if foreign == marker {
				continue
			}
			if strings.Contains(text, foreign) {
				t.Fatalf("%s holds another call's marker %s", marker, foreign)
			}
		}
	}
	if got := len(recordsWithMsg(t, root, "llm request")); got != calls {
		t.Fatalf("request records = %d, want %d", got, calls)
	}
}

// markers around a 300 KiB request body: one at the head, one in the middle,
// one at the tail, all of them unique to this test.
func largeRequestBody() (body, head, middle, tail string) {
	head = "HEAD_MARKER_14000"
	middle = "MIDDLE_MARKER_14000"
	tail = "TAIL_MARKER_14000"
	var b strings.Builder
	b.WriteString(`{"input":"`)
	b.WriteString(head)
	for b.Len() < 150*1024 {
		b.WriteString(strings.Repeat("q", 1024))
	}
	b.WriteString(middle)
	for b.Len() < 300*1024 {
		b.WriteString(strings.Repeat("q", 1024))
	}
	b.WriteString(tail)
	b.WriteString(`"}`)
	return b.String(), head, middle, tail
}

// A refused call keeps its request whole. This is the evidence the 2026-11-08
// Gemini 400 lost: the middle of the request was the part that explained it.
func TestExchangeKeepsWholeRequestWhenRefused(t *testing.T) {
	root := exchangeLog(t)
	body, head, middle, tail := largeRequestBody()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"message":"Request contains an invalid argument."}}`)
	}))
	defer srv.Close()

	postBody(t, newExchangeRoundTripper(nil), context.Background(), srv.URL, body)

	rec := onlyResponse(t, root)
	if rec.Msg != "llm response" || rec.Level != "WARN" {
		t.Fatalf("record = %s %s, want a WARN llm response", rec.Level, rec.Msg)
	}
	if got := rec.Attrs["outcome"]; got != outcomeHTTPError {
		t.Fatalf("outcome = %v, want %s", got, outcomeHTTPError)
	}
	ref := rec.Blobs["req"]
	if !ref.Whole || ref.Omitted != 0 {
		t.Fatalf("blob.req = %+v, want the whole request", ref)
	}
	text := gunzipPayload(t, payloadPath(t, root, ref.Name))
	for _, marker := range []string{head, middle, tail} {
		if !strings.Contains(text, marker) {
			t.Fatalf("request file lost %s", marker)
		}
	}
	if strings.Contains(text, "log bytes omitted") {
		t.Fatal("a refused call's request was trimmed")
	}
}

// A successful call keeps the head and the tail of its request and says how
// much it left out.
func TestExchangeBoundsRequestAfterSuccess(t *testing.T) {
	root := exchangeLog(t)
	body, head, middle, tail := largeRequestBody()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer srv.Close()

	postBody(t, newExchangeRoundTripper(nil), context.Background(), srv.URL, body)

	rec := onlyResponse(t, root)
	if got := rec.Attrs["outcome"]; got != outcomeOK {
		t.Fatalf("outcome = %v, want %s", got, outcomeOK)
	}
	ref := rec.Blobs["req"]
	if ref.Whole || ref.Omitted == 0 {
		t.Fatalf("blob.req = %+v, want a trimmed request with an omitted count", ref)
	}
	if want := ref.Bytes - requestKeepBytes; ref.Omitted != want {
		t.Fatalf("omitted = %d, want %d (bytes %d minus the %d kept)", ref.Omitted, want, ref.Bytes, int64(requestKeepBytes))
	}
	if reqBytes, _ := rec.Attrs["req_bytes"].(float64); int64(reqBytes) != int64(len(body)) {
		t.Fatalf("req_bytes = %v, want the body's own %d bytes", rec.Attrs["req_bytes"], len(body))
	}
	text := gunzipPayload(t, payloadPath(t, root, ref.Name))
	if !strings.Contains(text, head) || !strings.Contains(text, tail) {
		t.Fatal("trimmed request lost its head or its tail")
	}
	if strings.Contains(text, middle) {
		t.Fatal("trimmed request kept its middle")
	}
	if got := strings.Count(text, "log bytes omitted"); got != 1 {
		t.Fatalf("omission markers = %d, want exactly 1", got)
	}
}

// A request under the cap is left exactly as it was written.
func TestExchangeSmallRequestStaysWhole(t *testing.T) {
	root := exchangeLog(t)
	body := `{"input":"` + strings.Repeat("s", 10*1024) + `SMALL_TAIL_MARKER"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer srv.Close()

	postBody(t, newExchangeRoundTripper(nil), context.Background(), srv.URL, body)

	rec := onlyResponse(t, root)
	ref := rec.Blobs["req"]
	if !ref.Whole || ref.Omitted != 0 {
		t.Fatalf("blob.req = %+v, want the whole request", ref)
	}
	text := gunzipPayload(t, payloadPath(t, root, ref.Name))
	if !strings.Contains(text, "SMALL_TAIL_MARKER") || strings.Contains(text, "log bytes omitted") {
		t.Fatalf("small request file = %q…, want it untouched", text[max(0, len(text)-120):])
	}
}

// A stream's payload is on disk before the stream ends: that is the case a
// process dying mid-answer leaves behind.
func TestExchangeStreamIsOnDiskBeforeItEnds(t *testing.T) {
	root := exchangeLog(t)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		for i := 0; i < 40; i++ {
			fmt.Fprintf(w, "data: %s\n\n", strings.Repeat("s", 1017))
			flusher.Flush()
		}
		<-release
		fmt.Fprint(w, "data: done\n\n")
		flusher.Flush()
	}))
	defer srv.Close()
	defer unblock()

	rt := newExchangeRoundTripper(nil)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, strings.NewReader(`{"stream":true}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	buf := make([]byte, 40*1024)
	if _, err := io.ReadFull(resp.Body, buf); err != nil {
		t.Fatalf("read the first 40 KiB: %v", err)
	}

	// The stream is still open: the request is recorded, the response is not.
	requests := recordsWithMsg(t, root, "llm request")
	if len(requests) != 1 {
		t.Fatalf("request records = %d, want 1", len(requests))
	}
	if got := recordsWithMsg(t, root, "llm response"); len(got) != 0 {
		t.Fatalf("response records before the stream ended = %d, want 0", len(got))
	}
	if ref := requests[0].Blobs["req"]; !ref.Whole {
		t.Fatalf("in-flight request = %+v, want it whole", ref)
	}

	onDisk := gunzipPayload(t, payloadPath(t, root, respPayloadName(requests[0].Blobs["req"].Name)))
	if len(onDisk) < 32*1024 {
		t.Fatalf("response payload on disk = %d bytes while the stream is open, want at least 32 KiB", len(onDisk))
	}
	if !strings.Contains(onDisk, "data: ") {
		t.Fatal("response payload on disk holds no events")
	}

	unblock()
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatalf("drain: %v", err)
	}
	_ = resp.Body.Close()
	if got := recordsWithMsg(t, root, "llm response"); len(got) != 1 {
		t.Fatalf("response records after the stream ended = %d, want 1", len(got))
	}
}

// A connection cut mid-stream is a WARN read_error, the request stays whole,
// and the response file keeps what arrived.
func TestExchangeReportsCutStream(t *testing.T) {
	root := exchangeLog(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n")
		_, _ = buf.WriteString("20\r\ndata: {\"partial\":\"half\"}\n\n\r\n")
		_ = buf.Flush()
	}))
	defer srv.Close()

	rt := newExchangeRoundTripper(nil)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, strings.NewReader(`{"stream":true}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("X-Marker", "CUT")
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	// The chunked body never gets its terminating chunk: the read ends in an
	// error, not an EOF.
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	rec := onlyResponse(t, root)
	if got := rec.Attrs["outcome"]; got != outcomeReadError {
		t.Fatalf("outcome = %v, want %s", got, outcomeReadError)
	}
	if rec.Level != "WARN" {
		t.Fatalf("level = %s, want WARN", rec.Level)
	}
	if readErr, _ := rec.Attrs["read_err"].(string); readErr == "" {
		t.Fatalf("read_err is empty: %+v", rec.Attrs)
	}
	if ref := rec.Blobs["req"]; !ref.Whole {
		t.Fatalf("blob.req = %+v, want the whole request", ref)
	}
	respRef := rec.Blobs["resp"]
	text := gunzipPayload(t, payloadPath(t, root, respRef.Name))
	if !strings.Contains(text, `"partial":"half"`) {
		t.Fatalf("response file lost what arrived: %q", text)
	}
}

// A call that never reached the server has a record and no response payload.
func TestExchangeReportsTransportError(t *testing.T) {
	root := exchangeLog(t)
	wantErr := errors.New("dial tcp 127.0.0.1:1: connect: connection refused")
	rt := newExchangeRoundTripper(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, wantErr
	}))

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://provider.test/v1/responses", strings.NewReader(`{"model":"m"}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if _, err := rt.RoundTrip(req); !errors.Is(err, wantErr) {
		t.Fatalf("RoundTrip error = %v, want it passed through unchanged", err)
	}

	rec := onlyResponse(t, root)
	if got := rec.Attrs["outcome"]; got != outcomeTransportError {
		t.Fatalf("outcome = %v, want %s", got, outcomeTransportError)
	}
	if rec.Level != "WARN" || !strings.Contains(rec.Err, "connection refused") {
		t.Fatalf("record = %s %q, want a WARN record carrying the error", rec.Level, rec.Err)
	}
	if _, ok := rec.Blobs["resp"]; ok {
		t.Fatalf("a call with no response has a response payload: %+v", rec.Blobs)
	}
	if ref := rec.Blobs["req"]; !ref.Whole {
		t.Fatalf("blob.req = %+v, want the whole request", ref)
	}
}

// Cancelling a stream is information, not a warning.
func TestExchangeCancelIsNotAWarning(t *testing.T) {
	root := exchangeLog(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		for {
			if _, err := fmt.Fprint(w, "data: tick\n\n"); err != nil {
				return
			}
			flusher.Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	rt := newExchangeRoundTripper(nil)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL, strings.NewReader(`{"stream":true}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	buf := make([]byte, 64)
	if _, err := io.ReadFull(resp.Body, buf); err != nil {
		t.Fatalf("read the first bytes: %v", err)
	}
	cancel()
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	rec := onlyResponse(t, root)
	if got := rec.Attrs["outcome"]; got != outcomeCancelled {
		t.Fatalf("outcome = %v, want %s", got, outcomeCancelled)
	}
	if rec.Level != "INFO" {
		t.Fatalf("level = %s, want INFO for a cancellation", rec.Level)
	}
	if ref := rec.Blobs["req"]; !ref.Whole {
		t.Fatalf("blob.req = %+v, want the whole request", ref)
	}
}

// Capture never changes what the caller reads — neither a streamed body nor a
// unary one — and a stream is still readable before it ends.
func TestExchangePassesBodyThroughUnchanged(t *testing.T) {
	exchangeLog(t)
	streamed := "data: first\n\n" + strings.Repeat("data: filler\n\n", 200) + "data: [DONE]\n\n"
	unary := `{"content":"` + strings.Repeat("u", 100*1024) + `","tail":"UNARY_TAIL"}`

	firstSeen := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/unary" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, unary)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		close(firstSeen)
		<-release
		_, _ = fmt.Fprint(w, strings.TrimPrefix(streamed, "data: first\n\n"))
	}))
	defer srv.Close()
	defer unblock()

	rt := newExchangeRoundTripper(nil)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, strings.NewReader(`{"stream":true}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	first := make([]byte, len("data: first\n\n"))
	if _, err := io.ReadFull(resp.Body, first); err != nil {
		t.Fatalf("read first chunk: %v", err)
	}
	if string(first) != "data: first\n\n" {
		t.Fatalf("first chunk = %q", first)
	}
	select {
	case <-firstSeen:
	default:
		t.Fatal("no first chunk arrived before EOF")
	}
	unblock()
	rest, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read the rest: %v", err)
	}
	_ = resp.Body.Close()
	if got := string(first) + string(rest); got != streamed {
		t.Fatalf("streamed body changed (%d bytes, want %d)", len(got), len(streamed))
	}

	if got, _ := postBody(t, rt, context.Background(), srv.URL+"/unary", `{"model":"m"}`); got != unary {
		t.Fatalf("unary body changed (%d bytes, want %d)", len(got), len(unary))
	}
}

// The first byte and the longest silence are on the record: "it stalled for
// most of a minute and then dropped" has to be visible without reading the
// payload.
func TestExchangeMeasuresFirstByteAndLongestGap(t *testing.T) {
	root := exchangeLog(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		flusher.Flush()
		time.Sleep(120 * time.Millisecond)
		_, _ = fmt.Fprint(w, "data: one\n\n")
		flusher.Flush()
		time.Sleep(200 * time.Millisecond)
		_, _ = fmt.Fprint(w, "data: two\n\n")
	}))
	defer srv.Close()

	postBody(t, newExchangeRoundTripper(nil), context.Background(), srv.URL, `{"stream":true}`)

	rec := onlyResponse(t, root)
	ttfb, _ := rec.Attrs["ttfb_ms"].(float64)
	gap, _ := rec.Attrs["max_gap_ms"].(float64)
	if ttfb > 100 {
		t.Fatalf("ttfb_ms = %v, want the headers' own arrival (< 100ms)", ttfb)
	}
	if gap < 180 {
		t.Fatalf("max_gap_ms = %v, want the 200ms silence between two events", gap)
	}
}

// A credential is found by the name of the header that carries it, not by a
// list of the seven headers we happen to know today.
func TestExchangeRedactsHeadersByNameRule(t *testing.T) {
	root := exchangeLog(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer srv.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, strings.NewReader(`{"model":"m"}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer auth-secret-value")
	req.Header.Set("X-Custom-Api-Key", "custom-key-secret-value")
	req.Header.Set("X-Upstream-Token", "upstream-token-secret-value")
	req.Header.Set("X-Opencode-Session", "session-visible-value")
	req.Header.Set("X-Trace", "Bearer trace-secret-value")
	resp, err := newExchangeRoundTripper(nil).RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	rec := onlyResponse(t, root)
	text := gunzipPayload(t, payloadPath(t, root, rec.Blobs["req"].Name))
	for _, secret := range []string{"auth-secret-value", "custom-key-secret-value", "upstream-token-secret-value", "trace-secret-value"} {
		if strings.Contains(text, secret) {
			t.Fatalf("request file leaked %q:\n%s", secret, text)
		}
	}
	if !strings.Contains(text, "session-visible-value") {
		t.Fatalf("request file hid a header that carries no credential:\n%s", text)
	}
}

// The model is a label: taken from the path where Gemini puts it and from the
// body where the OpenAI and Anthropic shapes put it.
func TestExchangeModelFromBodyOrPath(t *testing.T) {
	root := exchangeLog(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer srv.Close()

	rt := newExchangeRoundTripper(nil)
	postBody(t, rt, context.Background(), srv.URL+"/v1/responses", `{"model":"gpt-5-codex","input":"hi"}`)
	postBody(t, rt, context.Background(), srv.URL+"/v1beta/models/gemini-4-argon:streamGenerateContent?alt=sse", `{"contents":[]}`)

	requests := recordsWithMsg(t, root, "llm request")
	if len(requests) != 2 {
		t.Fatalf("request records = %d, want 2", len(requests))
	}
	if got := requests[0].Attrs["model"]; got != "gpt-5-codex" {
		t.Fatalf("body model = %v, want gpt-5-codex", got)
	}
	if got := requests[1].Attrs["model"]; got != "gemini-4-argon" {
		t.Fatalf("path model = %v, want gemini-4-argon", got)
	}
}

// Turning payloads off keeps the metadata: who called, which model, the status
// and the outcome are the half that still explains a call.
func TestExchangeWithoutPayloadCaptureKeepsMetadata(t *testing.T) {
	root := exchangeLog(t)
	t.Setenv("FOREBRAIN_LLM_HTTP_DEBUG", "0")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer srv.Close()

	postBody(t, newExchangeRoundTripper(nil), context.Background(), srv.URL, `{"model":"m"}`)

	records := recordsRead(t, root)
	if len(records) != 2 {
		t.Fatalf("records = %d, want both halves of the call", len(records))
	}
	for _, rec := range records {
		if len(rec.Blobs) != 0 {
			t.Fatalf("%s carries payload references with capture off: %+v", rec.Msg, rec.Blobs)
		}
		if rec.Msg == "llm response" && rec.Attrs["outcome"] != outcomeOK {
			t.Fatalf("outcome = %v, want %s", rec.Attrs["outcome"], outcomeOK)
		}
	}
	if matches, err := filepath.Glob(filepath.Join(root, home.LogsDir, "*", blobDirName)); err != nil || len(matches) != 0 {
		t.Fatalf("payload directory = %v (err %v), want none", matches, err)
	}
}

// A payload directory that cannot be created costs the payloads, never the call.
func TestExchangeSurvivesUnwritablePayloadDir(t *testing.T) {
	t.Setenv("FOREBRAIN_LLM_HTTP_DEBUG", "1")
	root := withEventLog(t)
	// x as a regular file: MkdirAll cannot make it a directory.
	day := filepath.Join(root, home.LogsDir, localDay(time.Now()))
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatalf("mkdir day: %v", err)
	}
	if err := os.WriteFile(filepath.Join(day, blobDirName), []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write x: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer srv.Close()

	if got, _ := postBody(t, newExchangeRoundTripper(nil), context.Background(), srv.URL, `{"model":"m"}`); got != `{"ok":true}` {
		t.Fatalf("body = %q, want the call to succeed anyway", got)
	}

	records := recordsRead(t, root)
	if len(records) != 2 {
		t.Fatalf("records = %d, want both halves of a call whose payloads failed", len(records))
	}
	for _, rec := range records {
		if len(rec.Blobs) != 0 {
			t.Fatalf("%s references a payload it could not write: %+v", rec.Msg, rec.Blobs)
		}
		if blobErr, _ := rec.Attrs[attrBlobError].(string); blobErr == "" {
			t.Fatalf("%s has no %s: %+v", rec.Msg, attrBlobError, rec.Attrs)
		}
	}
}

// BenchmarkExchangeRequestBlob is what the exchange adds to the request path for
// a 1 MiB body: one gzip at BestSpeed, no fsync (plan 286 budgets 30ms).
func BenchmarkExchangeRequestBlob(b *testing.B) {
	b.Setenv("FOREBRAIN_LLM_HTTP_DEBUG", "1")
	log := OpenEventLog(b.TempDir(), "bench")
	SetDefaultEventLog(log)
	defer func() {
		SetDefaultEventLog(nil)
		_ = log.Close()
	}()
	body := strings.Repeat("q", 1<<20)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer srv.Close()
	rt := newExchangeRoundTripper(nil)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, strings.NewReader(body))
		if err != nil {
			b.Fatalf("NewRequest: %v", err)
		}
		resp, err := rt.RoundTrip(req)
		if err != nil {
			b.Fatalf("RoundTrip: %v", err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
}
