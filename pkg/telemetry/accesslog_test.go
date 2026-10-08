package telemetry

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// captureHandler keeps every record logged through the default logger, so a
// test can assert on the access log without reading the terminal.
type captureHandler struct {
	mu   sync.Mutex
	recs []slog.Record
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.recs = append(h.recs, r.Clone())
	return nil
}

func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

func (h *captureHandler) snapshot() []slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]slog.Record(nil), h.recs...)
}

// captureAccessLog installs a capturing default logger for one test.
func captureAccessLog(t *testing.T) *captureHandler {
	t.Helper()
	h := &captureHandler{}
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return h
}

// requestRecords returns the access-log records the middleware emitted.
func requestRecords(h *captureHandler) []slog.Record {
	var out []slog.Record
	for _, r := range h.snapshot() {
		if r.Message == "request" {
			out = append(out, r)
		}
	}
	return out
}

func recordAttrs(r slog.Record) map[string]string {
	out := map[string]string{}
	r.Attrs(func(a slog.Attr) bool {
		out[a.Key] = a.Value.String()
		return true
	})
	return out
}

func serveOnce(t *testing.T, method, target string, inner http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	req.RemoteAddr = "203.0.113.7:41234"
	rec := httptest.NewRecorder()
	AccessLogMiddleware(inner).ServeHTTP(rec, req)
	return rec
}

// Every request is reported exactly once, with the nginx-style fields: the
// status the handler answered, the bytes it wrote, and where it came from.
func TestAccessLogMiddlewareReportsOneRecordPerRequest(t *testing.T) {
	tests := []struct {
		name    string
		inner   http.HandlerFunc
		status  string
		written string
	}{
		{
			name:    "ok",
			inner:   func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("hello")) },
			status:  "200",
			written: "5",
		},
		{
			name:    "not found",
			inner:   func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) },
			status:  "404",
			written: fmt.Sprint(len("404 page not found\n")),
		},
		{
			name: "rate limited",
			inner: func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "too many requests", http.StatusTooManyRequests)
			},
			status:  "429",
			written: fmt.Sprint(len("too many requests\n")),
		},
		{
			name:    "handler writes nothing",
			inner:   func(http.ResponseWriter, *http.Request) {},
			status:  "200",
			written: "0",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := captureAccessLog(t)
			serveOnce(t, http.MethodGet, "/api/runs", tc.inner)

			recs := requestRecords(h)
			if len(recs) != 1 {
				t.Fatalf("records = %d, want exactly one", len(recs))
			}
			rec := recs[0]
			if rec.Level != slog.LevelInfo {
				t.Fatalf("level = %v, want INFO", rec.Level)
			}
			got := recordAttrs(rec)
			if got["method"] != http.MethodGet {
				t.Errorf("method = %q", got["method"])
			}
			if got["path"] != "/api/runs" {
				t.Errorf("path = %q", got["path"])
			}
			if got["status"] != tc.status {
				t.Errorf("status = %q, want %q", got["status"], tc.status)
			}
			if got["bytes"] != tc.written {
				t.Errorf("bytes = %q, want %q", got["bytes"], tc.written)
			}
			if got["remote"] != "203.0.113.7" {
				t.Errorf("remote = %q, want the client IP without its port", got["remote"])
			}
			if !regexp.MustCompile(`^\d+(\.\d+)?(ms|s)$`).MatchString(got["duration"]) {
				t.Errorf("duration = %q, want a formatted duration", got["duration"])
			}
		})
	}
}

// A query string is free text from the caller and routinely carries
// credentials, so only the path is recorded.
func TestAccessLogMiddlewareNeverRecordsTheQueryString(t *testing.T) {
	h := captureAccessLog(t)
	req := httptest.NewRequest(http.MethodGet, "/login?token=supersecret&next=/", nil)
	rec := httptest.NewRecorder()
	AccessLogMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})).ServeHTTP(rec, req)

	recs := requestRecords(h)
	if len(recs) != 1 {
		t.Fatalf("records = %d, want exactly one", len(recs))
	}
	if got := recordAttrs(recs[0])["path"]; got != "/login" {
		t.Fatalf("path = %q, want the path alone", got)
	}
	var line strings.Builder
	recs[0].Attrs(func(a slog.Attr) bool {
		line.WriteString(a.Key)
		line.WriteByte('=')
		line.WriteString(a.Value.String())
		line.WriteByte(' ')
		return true
	})
	if strings.Contains(line.String(), "supersecret") {
		t.Fatalf("access log leaked the query string: %s", line.String())
	}
}

// A websocket upgrade hijacks the connection and answers 101 without ever
// going through WriteHeader; the websocket's own duration is the time the
// connection was held, since the handler returns when it closes.
func TestAccessLogMiddlewareReportsHijackedUpgradeAs101(t *testing.T) {
	h := captureAccessLog(t)
	srv := httptest.NewServer(AccessLogMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("the wrapped writer must still support hijacking")
			return
		}
		conn, brw, err := hj.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		defer conn.Close()
		_, _ = brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		_ = brw.Flush()
	})))
	defer srv.Close()

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if _, err := fmt.Fprintf(conn, "GET /ws/chat HTTP/1.1\r\nHost: %s\r\n\r\n", srv.Listener.Addr().String()); err != nil {
		t.Fatalf("write request: %v", err)
	}
	buf := make([]byte, 256)
	if _, err := conn.Read(buf); err != nil {
		t.Fatalf("read handshake: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		recs := requestRecords(h)
		if len(recs) == 1 {
			got := recordAttrs(recs[0])
			if got["status"] != "101" {
				t.Fatalf("status = %q, want 101 for a hijacked upgrade", got["status"])
			}
			if got["path"] != "/ws/chat" {
				t.Fatalf("path = %q", got["path"])
			}
			if got["bytes"] != "0" {
				t.Fatalf("bytes = %q, want %q", got["bytes"], "0")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the websocket request was never reported")
}

// The forms a forward proxy sees — CONNECT above all — have no path, and the
// record still reports something a reader can act on: the authority. The field
// keeps the name "path" because the record's shape is a shipped format.
func TestRequestTargetUsesTheAuthorityWhenThereIsNoPath(t *testing.T) {
	tests := []struct {
		name string
		req  *http.Request
		want string
	}{
		{
			name: "connect carries the authority",
			req: &http.Request{
				Method:     http.MethodConnect,
				URL:        &url.URL{Host: "example.com:443"},
				RemoteAddr: "203.0.113.7:41234",
			},
			want: "example.com:443",
		},
		{
			name: "origin request carries the path",
			req: &http.Request{
				Method:     http.MethodGet,
				URL:        &url.URL{Path: "/api/runs"},
				RemoteAddr: "203.0.113.7:41234",
			},
			want: "/api/runs",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := captureAccessLog(t)
			rec := httptest.NewRecorder()
			AccessLogMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(rec, tc.req)

			recs := requestRecords(h)
			if len(recs) != 1 {
				t.Fatalf("records = %d, want exactly one", len(recs))
			}
			if got := recordAttrs(recs[0])["path"]; got != tc.want {
				t.Fatalf("path = %q, want %q", got, tc.want)
			}
		})
	}
}

// A panicking handler is the request an operator most needs to see: the record
// is still written (from the defer, at ERROR level, naming the panic), and the
// panic itself is re-raised unchanged for net/http to handle as it always has.
func TestAccessLogMiddlewareReportsPanickingRequest(t *testing.T) {
	h := captureAccessLog(t)
	var got any
	func() {
		defer func() { got = recover() }()
		serveOnce(t, http.MethodGet, "/api/runs", func(http.ResponseWriter, *http.Request) {
			panic("boom: the disk is on fire")
		})
	}()
	if got != "boom: the disk is on fire" {
		t.Fatalf("recovered = %v, want the panic re-raised unchanged", got)
	}

	recs := h.snapshot()
	if len(recs) != 1 {
		t.Fatalf("records = %d, want exactly one — no normal request record on top", len(recs))
	}
	if recs[0].Message != "request panicked" {
		t.Fatalf("message = %q, want %q", recs[0].Message, "request panicked")
	}
	if recs[0].Level != slog.LevelError {
		t.Fatalf("level = %v, want ERROR", recs[0].Level)
	}
	attrs := recordAttrs(recs[0])
	if attrs["method"] != http.MethodGet {
		t.Errorf("method = %q", attrs["method"])
	}
	if attrs["path"] != "/api/runs" {
		t.Errorf("path = %q", attrs["path"])
	}
	if attrs["remote"] != "203.0.113.7" {
		t.Errorf("remote = %q, want the client IP without its port", attrs["remote"])
	}
	if attrs["status"] != "500" {
		t.Errorf("status = %q, want 500 — the client got a reset connection, not a 200", attrs["status"])
	}
	if !strings.Contains(attrs["panic"], "boom") {
		t.Errorf("panic = %q, want the panic value recorded", attrs["panic"])
	}
}

// A handler that had already sent its response and then panicked is reported
// with the status it actually answered, not the panic fallback.
func TestAccessLogMiddlewarePanicAfterWriteKeepsTheRealStatus(t *testing.T) {
	h := captureAccessLog(t)
	var got any
	func() {
		defer func() { got = recover() }()
		serveOnce(t, http.MethodPost, "/api/runs", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "short and stout", http.StatusTeapot)
			panic("boom after write")
		})
	}()
	if got != "boom after write" {
		t.Fatalf("recovered = %v, want the panic re-raised unchanged", got)
	}

	recs := h.snapshot()
	if len(recs) != 1 {
		t.Fatalf("records = %d, want exactly one", len(recs))
	}
	if recs[0].Message != "request panicked" {
		t.Fatalf("message = %q, want %q", recs[0].Message, "request panicked")
	}
	if attrs := recordAttrs(recs[0]); attrs["status"] != "418" {
		t.Errorf("status = %q, want 418 — the response that was actually sent", attrs["status"])
	}
}

// ClientIP precedence, pinned now that the rule serves the access log and any
// limiter that keys on the caller: the first X-Forwarded-For entry wins, then
// RemoteAddr's host, and a RemoteAddr that does not parse is kept as-is.
func TestClientIPPrecedence(t *testing.T) {
	withXFF := httptest.NewRequest(http.MethodGet, "/api/runs", nil)
	withXFF.RemoteAddr = "10.0.0.1:51234"
	withXFF.Header.Set("X-Forwarded-For", "203.0.113.7, 10.0.0.1")

	noXFF := httptest.NewRequest(http.MethodGet, "/api/runs", nil)
	noXFF.RemoteAddr = "198.51.100.9:41234"

	unparseable := httptest.NewRequest(http.MethodGet, "/api/runs", nil)
	unparseable.RemoteAddr = "203.0.113.7"

	tests := []struct {
		name string
		req  *http.Request
		want string
	}{
		{name: "forwarded for wins", req: withXFF, want: "203.0.113.7"},
		{name: "remote host without port", req: noXFF, want: "198.51.100.9"},
		{name: "unparseable remote addr kept as-is", req: unparseable, want: "203.0.113.7"},
		{name: "nil request", req: nil, want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClientIP(tc.req); got != tc.want {
				t.Fatalf("ClientIP = %q, want %q", got, tc.want)
			}
		})
	}
}

// SlogErrorLog routes a *log.Logger's reports into slog: one ERROR record per
// Print, with the stdlib's trailing-newline trim applied to the message.
func TestSlogErrorLogRoutesToSlog(t *testing.T) {
	h := captureAccessLog(t)

	SlogErrorLog().Print("boom")

	recs := h.snapshot()
	if len(recs) != 1 {
		t.Fatalf("records = %d, want exactly one", len(recs))
	}
	if recs[0].Message != "boom" {
		t.Fatalf("message = %q, want %q — the stdlib trims one trailing newline", recs[0].Message, "boom")
	}
	if recs[0].Level != slog.LevelError {
		t.Fatalf("level = %v, want ERROR", recs[0].Level)
	}
}

// End to end: a panicking handler's report — the panic value plus its stack —
// travels net/http → Server.ErrorLog → slog.NewLogLogger → the default
// handler, and the whole multi-line report stays one record, which is what the
// console handler's message escaping needs to keep it one line per record.
func TestSlogErrorLogCarriesAPanicStackFromNetHTTP(t *testing.T) {
	h := captureAccessLog(t)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom: handler on fire")
	}))
	srv.Config.ErrorLog = SlogErrorLog()
	srv.Start()
	defer srv.Close()

	// The panicking handler makes net/http close the connection without a
	// response, so the client error is the expected outcome; the record is
	// what is under test.
	_, _ = srv.Client().Get(srv.URL)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		recs := h.snapshot()
		if len(recs) > 1 {
			t.Fatalf("records = %d, want exactly one — the report must not be duplicated", len(recs))
		}
		if len(recs) == 1 {
			msg := recs[0].Message
			if !strings.Contains(msg, "boom: handler on fire") {
				t.Fatalf("message does not carry the panic value: %q", msg)
			}
			if !strings.Contains(msg, "goroutine") {
				t.Fatalf("message does not carry the stack header: %q", msg)
			}
			if !strings.Contains(msg, "\n") {
				t.Fatal("message has no interior newline; the stack must arrive intact for one-record escaping to matter")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the panicking handler's report never reached slog")
}
