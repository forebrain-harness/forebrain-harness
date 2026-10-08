// The access-log middleware: one record per answered request, shared by every
// HTTP server in the process.
package telemetry

import (
	"bufio"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
)

// AccessLogMiddleware reports every request the handler it wraps answers, one
// record each, with nginx access-log semantics: method, target, status,
// duration, bytes and remote address. It is meant to sit outermost in a server's
// chain, so nothing is skipped — including the rate limiter's own rejections and
// a hijacked websocket upgrade (reported as 101) — and it reports a panicking
// request at Error level with the panic value before letting the panic continue.
//
// Only the request target is recorded, never a query string: a query is free
// text from the caller and routinely carries credentials. The target is the
// path, or the authority for the forms a forward proxy sees (CONNECT and
// absolute-URI requests), which have no path.
func AccessLogMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		// The record is written from a defer so a panicking handler still leaves
		// one: a request that crashed an endpoint is the request an operator most
		// needs to see, and it is the one an emit-on-return path loses. The panic
		// is read with recover() only to name it in the record and is then
		// re-panicked, so net/http still closes the connection and keeps serving
		// exactly as it would have.
		defer func() {
			recovered := recover()
			if recovered != nil {
				rec.panicked = true
			}
			attrs := []any{
				"method", r.Method,
				// The target alone: a query string is free text from the caller and
				// routinely carries credentials. The field keeps the name "path"
				// even when it carries an authority instead, because the field
				// name is part of the shipped format.
				"path", requestTarget(r),
				"status", rec.status(),
				"duration", formatAccessDuration(time.Since(start)),
				"bytes", rec.written,
				"remote", ClientIP(r),
			}
			if recovered != nil {
				slog.Error("request panicked", append(attrs, "panic", recovered)...)
				panic(recovered)
			}
			slog.Info("request", attrs...)
		}()
		next.ServeHTTP(rec, r)
	})
}

// requestTarget is what the record reports for a request. An origin server's
// requests always have a path; a forward proxy sees CONNECT and absolute-URI
// forms whose path is empty and whose authority is the interesting part.
func requestTarget(r *http.Request) string {
	if r.URL == nil {
		return ""
	}
	if r.URL.Path != "" {
		return r.URL.Path
	}
	return r.URL.Host
}

// statusRecorder remembers what a handler answered, since net/http gives no
// way to ask afterwards. It keeps the wrapped writer's own capabilities
// reachable: the websocket upgrade hijacks the connection, and a streaming
// handler flushes.
type statusRecorder struct {
	http.ResponseWriter
	code     int
	written  int64
	hijacked bool
	// panicked records that the handler crashed before a response was sent; the
	// access log reports 500 for it rather than the 200 an untouched recorder
	// would imply, because that is the outcome the client experienced.
	panicked bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.code == 0 {
		r.code = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.code == 0 {
		r.code = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.written += int64(n)
	return n, err
}

func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	conn, rw, err := h.Hijack()
	if err == nil {
		r.hijacked = true
	}
	return conn, rw, err
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// status is what the access log reports. A handler that wrote nothing still
// answered 200, and a hijacked connection is no longer an HTTP response at
// all — the rule reports 101, the status a websocket upgrade answers. A
// handler that panicked before writing anything answered nothing.
func (r *statusRecorder) status() int {
	if r.hijacked {
		return http.StatusSwitchingProtocols
	}
	if r.panicked && r.code == 0 {
		return http.StatusInternalServerError
	}
	if r.code == 0 {
		return http.StatusOK
	}
	return r.code
}

// formatAccessDuration renders a request's duration the way an operator scans
// it: milliseconds below a second, seconds above.
func formatAccessDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%.1fms", float64(d)/float64(time.Millisecond))
	}
	return fmt.Sprintf("%.2fs", d.Seconds())
}

// ClientIP is who a request came from, for the access log's "remote" field and
// any limiter that keys on the caller: the first X-Forwarded-For entry when
// one is present (the address that made the hop the proxy saw), otherwise the
// host of RemoteAddr without its port. A RemoteAddr that does not parse as
// host:port is returned as-is rather than dropped.
func ClientIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For"))
	if xff != "" {
		parts := strings.Split(xff, ",")
		if len(parts) > 0 {
			return strings.TrimSpace(parts[0])
		}
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil {
		return host
	}
	return strings.TrimSpace(r.RemoteAddr)
}

// SlogErrorLog routes an HTTP server's own error reports — a panicking
// handler's stack, TLS handshake failures, a superfluous WriteHeader — through
// slog, so they reach the console and the log files instead of the standard
// library's raw stderr.
func SlogErrorLog() *log.Logger {
	return slog.NewLogLogger(slog.Default().Handler(), slog.LevelError)
}
