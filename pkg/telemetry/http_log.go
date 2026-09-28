package telemetry

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
)

func newHTTPTraceRoundTripper(next http.RoundTripper) http.RoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}
	return &httpTraceRoundTripper{next: next}
}

func WrapLLMHTTPTransport(next http.RoundTripper) http.RoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}
	transport := newHTTPTraceRoundTripper(next)
	if debugFileLogEnabled() {
		transport = newHTTPDebugFileLogRoundTripper(transport)
	}
	return transport
}

func newHTTPDebugFileLogRoundTripper(next http.RoundTripper) http.RoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}
	return &httpDebugFileLogRoundTripper{next: next}
}

type httpTraceRoundTripper struct {
	next http.RoundTripper
}

func (t *httpTraceRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := context.Background()
	if req != nil {
		ctx = req.Context()
	}
	var reqBody []byte
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return nil, err
		}
		reqBody = b
		req.Body = io.NopCloser(bytes.NewReader(reqBody))
	}
	method := ""
	urlText := ""
	reqHeaders := ""
	if req != nil {
		method = strings.TrimSpace(req.Method)
		if req.URL != nil {
			urlText = redactURL(req.URL)
		}
		reqHeaders = formatRedactedHeaderLines(req.Header)
	}
	ctx, step := startHTTPTraceStep(ctx,
		attribute.String("forebrain.http.method", method),
		attribute.String("forebrain.http.url", urlText),
		attribute.String("forebrain.http.request_headers", reqHeaders),
		attribute.String("forebrain.http.request_body", redactHTTPBody(reqBody)),
	)
	if req != nil {
		req = req.Clone(ctx)
		req.Body = io.NopCloser(bytes.NewReader(reqBody))
	}
	resp, err := t.next.RoundTrip(req)
	if err != nil {
		step.End(err.Error())
		return nil, err
	}
	var respBody []byte
	if resp.Body != nil {
		if isEventStreamResponse(resp) {
			// The response continues through the transport chain after RoundTrip
			// returns. Snapshot metadata before installing the asynchronous body
			// observer so outer transports can safely normalize the response.
			statusCode := resp.StatusCode
			respHeaders := formatRedactedHeaderLines(resp.Header)
			resp.Body = newObservingReadCloser(resp.Body, func(body []byte, readErr error) {
				errText := ""
				if readErr != nil && readErr != io.EOF {
					errText = readErr.Error()
				}
				step.End(errText,
					attribute.Int("forebrain.http.status_code", statusCode),
					attribute.String("forebrain.http.response_headers", respHeaders),
					attribute.String("forebrain.http.response_body", redactHTTPBody(body)),
				)
			})
			return resp, nil
		}
		b, rerr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if rerr != nil {
			step.End(rerr.Error(),
				attribute.Int("forebrain.http.status_code", resp.StatusCode),
				attribute.String("forebrain.http.response_headers", formatRedactedHeaderLines(resp.Header)),
			)
			return nil, rerr
		}
		respBody = b
		resp.Body = io.NopCloser(bytes.NewReader(respBody))
	}
	step.End("",
		attribute.Int("forebrain.http.status_code", resp.StatusCode),
		attribute.String("forebrain.http.response_headers", formatRedactedHeaderLines(resp.Header)),
		attribute.String("forebrain.http.response_body", redactHTTPBody(respBody)),
	)
	resp.Body = io.NopCloser(bytes.NewReader(respBody))
	return resp, nil
}

type httpDebugFileLogRoundTripper struct {
	next http.RoundTripper
}

func (t *httpDebugFileLogRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	var reqBody []byte
	if req != nil && req.Body != nil {
		b, err := io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return nil, err
		}
		reqBody = b
		req.Body = io.NopCloser(bytes.NewReader(reqBody))
	}
	writeDebugFileLog("llm_http", formatHTTPRequestBlock(req, reqBody))
	resp, err := t.next.RoundTrip(req)
	if err != nil {
		writeDebugFileLog("llm_http", fmt.Sprintf("llm_http response_error method=%s url=%s err=%v", req.Method, redactURL(req.URL), err))
		return nil, err
	}
	var respBody []byte
	if resp.Body != nil {
		if isEventStreamResponse(resp) {
			respSnapshot := snapshotHTTPResponseMetadata(resp)
			resp.Body = newObservingReadCloser(resp.Body, func(body []byte, readErr error) {
				if readErr != nil && readErr != io.EOF {
					writeDebugFileLog("llm_http", fmt.Sprintf("llm_http response_read_body_error status=%d err=%v", respSnapshot.StatusCode, readErr))
				}
				writeDebugFileLog("llm_http", formatHTTPResponseBlock(respSnapshot, body))
			})
			return resp, nil
		}
		b, rerr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if rerr != nil {
			writeDebugFileLog("llm_http", fmt.Sprintf("llm_http response_read_body_error status=%d err=%v", resp.StatusCode, rerr))
			return nil, rerr
		}
		respBody = b
		resp.Body = io.NopCloser(bytes.NewReader(respBody))
	}
	writeDebugFileLog("llm_http", formatHTTPResponseBlock(resp, respBody))
	return resp, nil
}

func snapshotHTTPResponseMetadata(resp *http.Response) *http.Response {
	if resp == nil {
		return nil
	}
	snapshot := *resp
	snapshot.Header = resp.Header.Clone()
	if resp.Request != nil {
		snapshot.Request = resp.Request.Clone(resp.Request.Context())
	}
	return &snapshot
}

type observingReadCloser struct {
	body     io.ReadCloser
	buf      boundedObservationBuffer
	finalize func([]byte, error)
	once     sync.Once
}

// boundedObservationBuffer keeps HTTP telemetry observational: an arbitrarily
// long event stream must not become an arbitrarily large in-process buffer.
// The wire response is never altered; only the diagnostic snapshot is capped.
type boundedObservationBuffer struct {
	buf     bytes.Buffer
	omitted int64
}

func (b *boundedObservationBuffer) Write(p []byte) {
	remaining := DefaultRecordBytes - b.buf.Len()
	if remaining > 0 {
		keep := len(p)
		if keep > remaining {
			keep = remaining
		}
		_, _ = b.buf.Write(p[:keep])
		p = p[keep:]
	}
	b.omitted += int64(len(p))
}

func (b *boundedObservationBuffer) Bytes() []byte {
	result := append([]byte(nil), b.buf.Bytes()...)
	if b.omitted > 0 {
		result = append(result, []byte(fmt.Sprintf("\n[telemetry omitted %d bytes]\n", b.omitted))...)
	}
	return result
}

func newObservingReadCloser(body io.ReadCloser, finalize func([]byte, error)) io.ReadCloser {
	return &observingReadCloser{body: body, finalize: finalize}
}

func (r *observingReadCloser) Read(p []byte) (int, error) {
	n, err := r.body.Read(p)
	if n > 0 {
		r.buf.Write(p[:n])
	}
	if err != nil {
		r.finish(err)
	}
	return n, err
}

func (r *observingReadCloser) Close() error {
	err := r.body.Close()
	r.finish(err)
	return err
}

func (r *observingReadCloser) finish(err error) {
	r.once.Do(func() {
		if r.finalize != nil {
			r.finalize(r.buf.Bytes(), err)
		}
	})
}

func isEventStreamResponse(resp *http.Response) bool {
	if resp == nil {
		return false
	}
	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		mediaType = strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0])
	}
	return strings.EqualFold(mediaType, "text/event-stream")
}

func formatHTTPRequestBlock(req *http.Request, body []byte) string {
	var b strings.Builder
	ts := time.Now().Format(time.RFC3339Nano)
	fmt.Fprintf(&b, "llm_http request ts=%s\n", ts)
	reqLine := ""
	if req.URL != nil {
		reqLine = redactURL(req.URL)
	}
	fmt.Fprintf(&b, "%s %s %s\n", req.Method, reqLine, req.Proto)
	fmt.Fprintf(&b, "host: %s\n", req.Host)
	if req.URL != nil {
		fmt.Fprintf(&b, "url_full: %s\n", redactURL(req.URL))
	}
	b.WriteString(formatHeaders(req.Header))
	b.WriteString("body:\n")
	b.WriteString(BoundText(RedactLogLine(string(body)), DefaultRecordBytes))
	if len(body) > 0 && body[len(body)-1] != '\n' {
		b.WriteByte('\n')
	}
	return b.String()
}

func formatHTTPResponseBlock(resp *http.Response, body []byte) string {
	var b strings.Builder
	ts := time.Now().Format(time.RFC3339Nano)
	fmt.Fprintf(&b, "llm_http response ts=%s\n", ts)
	if resp.Request != nil {
		fmt.Fprintf(&b, "for_request: %s %s\n", resp.Request.Method, redactURL(resp.Request.URL))
	}
	fmt.Fprintf(&b, "%s status=%d\n", resp.Proto, resp.StatusCode)
	fmt.Fprintf(&b, "status_line: %s\n", resp.Status)
	b.WriteString(formatHeaders(resp.Header))
	b.WriteString("body:\n")
	b.WriteString(BoundText(RedactLogLine(string(body)), DefaultRecordBytes))
	if len(body) > 0 && body[len(body)-1] != '\n' {
		b.WriteByte('\n')
	}
	return b.String()
}

func formatHeaders(h http.Header) string {
	if len(h) == 0 {
		return "headers:\n"
	}
	return "headers:\n" + formatRedactedHeaderLines(h)
}

func formatRedactedHeaderLines(h http.Header) string {
	if len(h) == 0 {
		return ""
	}
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		for _, v := range h[k] {
			fmt.Fprintf(&b, "%s: %s\n", k, redactHeaderValue(k, v))
		}
	}
	return b.String()
}

func redactHTTPBody(body []byte) string {
	return RedactLogLine(string(body))
}

func redactHeaderValue(name, value string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "authorization", "proxy-authorization", "x-api-key", "api-key", "x-auth-token", "cookie", "set-cookie":
		return "[REDACTED]"
	default:
		return RedactLogLine(value)
	}
}

func redactURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	uc := *u
	if uc.User != nil {
		uc.User = url.UserPassword("[REDACTED]", "[REDACTED]")
	}
	q := uc.Query()
	for k := range q {
		lk := strings.ToLower(strings.TrimSpace(k))
		if strings.Contains(lk, "token") ||
			strings.Contains(lk, "key") ||
			strings.Contains(lk, "secret") ||
			strings.Contains(lk, "password") ||
			strings.Contains(lk, "auth") ||
			strings.Contains(lk, "signature") {
			q.Set(k, "[REDACTED]")
		}
	}
	uc.RawQuery = q.Encode()
	return uc.String()
}
