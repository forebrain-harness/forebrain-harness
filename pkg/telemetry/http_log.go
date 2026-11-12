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

	"go.opentelemetry.io/otel/attribute"
)

func newHTTPTraceRoundTripper(next http.RoundTripper) http.RoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}
	return &httpTraceRoundTripper{next: next}
}

// WrapLLMHTTPTransport is what a provider client's requests pass through: the
// OTel span, and — outside it — the per-call exchange records and payload files
// (plans/286).
//
// The exchange transport is always installed, and whether a call's bodies are
// kept is decided inside it, per call, by payloadCaptureEnabled. Installing it
// only when the payload switch was on would have meant no record at all for a
// call made with the switch off, when the metadata (who, which model, status,
// outcome, timings) is exactly what still makes that call diagnosable.
func WrapLLMHTTPTransport(next http.RoundTripper) http.RoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}
	return newExchangeRoundTripper(newHTTPTraceRoundTripper(next))
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
			resp.Body = newObservingReadCloser(resp.Body, DefaultRecordBytes, func(body []byte, readErr error) {
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
	buf      observationBuffer
	finalize func([]byte, error)
	once     sync.Once
}

// observationBuffer snapshots a response body for diagnostics; the wire
// response is never altered, and the snapshot is capped at limit — the OTel
// span's attributes go to external exporters, so they carry a bounded copy.
// The whole body is the exchange payload's job now (exchange.go, plan 286):
// that one is written to its own file as it arrives instead of being held in
// memory until the stream ends.
type observationBuffer struct {
	buf     bytes.Buffer
	omitted int64
	limit   int
}

func (b *observationBuffer) Write(p []byte) {
	remaining := b.limit - b.buf.Len()
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

func (b *observationBuffer) Bytes() []byte {
	result := append([]byte(nil), b.buf.Bytes()...)
	if b.omitted > 0 {
		result = append(result, []byte(fmt.Sprintf("\n[telemetry omitted %d bytes]\n", b.omitted))...)
	}
	return result
}

func newObservingReadCloser(body io.ReadCloser, limit int, finalize func([]byte, error)) io.ReadCloser {
	return &observingReadCloser{body: body, buf: observationBuffer{limit: limit}, finalize: finalize}
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

// redactHeaderValue hides a header whose name says it carries a credential.
//
// The rule is the one redactURL applies to a query parameter — a name that
// contains one of these words is a secret — rather than the seven-name list
// this used to be: a custom access point's header (x-upstream-token, or
// X-Custom-Api-Key) was not on that list and went to the file in the clear.
// Every other value is still passed through RedactLogLine, so a "Bearer …" in
// an ordinary header is redacted too. x-opencode-session contains none of the
// words and is shown as it is.
func redactHeaderValue(name, value string) string {
	lower := strings.ToLower(strings.TrimSpace(name))
	for _, word := range []string{"auth", "key", "token", "secret", "password", "cookie", "credential", "signature"} {
		if strings.Contains(lower, word) {
			return "[REDACTED]"
		}
	}
	return RedactLogLine(value)
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
