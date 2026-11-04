package llm

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

// wrappedErr unwraps to a typed-nil error, reproducing what a provider SDK's
// stream reader can surface when an endpoint returns a non-SSE body:
// errors.As matches the concrete pointer type but leaves the target nil.
type wrappedErr struct{ inner error }

func (w wrappedErr) Error() string { return "wrapped" }
func (w wrappedErr) Unwrap() error { return w.inner }

func TestIsExceededNilTargets(t *testing.T) {
	t.Parallel()

	if IsExceeded(nil) {
		t.Fatal("nil error should not be context-exceeded")
	}

	// errors.As matches *APIError but the pointer is nil. The classifier must
	// not dereference it.
	var typedNil *APIError
	err := wrappedErr{inner: typedNil}
	if IsExceeded(err) {
		t.Fatal("typed-nil APIError must classify as not-exceeded, not panic")
	}
}

func TestIsExceededClassifiesNormalizedProviderErrors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "code context_length_exceeded",
			err:  &APIError{Code: "context_length_exceeded"},
			want: true,
		},
		{
			name: "type context_length_exceeded",
			err:  &APIError{Type: "context_length_exceeded"},
			want: true,
		},
		{
			name: "message says maximum context length",
			err:  &APIError{Message: "This model's maximum context length is 8192 tokens"},
			want: true,
		},
		{
			name: "request entity too large",
			err:  &APIError{StatusCode: http.StatusRequestEntityTooLarge},
			want: true,
		},
		{
			// The size rejection is only visible in the body prose, which is
			// what Error() exposes when the SDK offers no Message field. This
			// is the Anthropic shape.
			name: "bad request whose wrapped text says prompt is too long",
			err:  &APIError{StatusCode: http.StatusBadRequest, Err: errors.New("400: prompt is too long")},
			want: true,
		},
		{
			name: "bad request with unrelated text",
			err:  &APIError{StatusCode: http.StatusBadRequest, Err: errors.New("400: unexpected tool_call_id")},
			want: false,
		},
		{
			name: "unrelated error",
			err:  errors.New("connection refused"),
			want: false,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := IsExceeded(tc.err); got != tc.want {
				t.Fatalf("IsExceeded(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestIsStructuralRequestErrorSeparatesShapeFromSize(t *testing.T) {
	t.Parallel()

	shape := &APIError{StatusCode: http.StatusBadRequest, Message: "unexpected tool_call_id"}
	if !IsStructuralRequestError(shape) {
		t.Fatal("a 400 that is not a size rejection must be structural")
	}
	// A size rejection is also a 400 but has its own recovery path, so it must
	// not be reported as structural.
	size := &APIError{StatusCode: http.StatusBadRequest, Message: "prompt is too long"}
	if IsStructuralRequestError(size) {
		t.Fatal("a 400 size rejection must not be classified as structural")
	}
	if IsStructuralRequestError(nil) {
		t.Fatal("nil is not a structural error")
	}
	if IsStructuralRequestError(errors.New("connection refused")) {
		t.Fatal("an error with no status is not structural")
	}
}

func TestAPIErrorPreservesTheOriginalForErrorsAs(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("provider exploded")
	wrapped := &APIError{StatusCode: http.StatusBadGateway, Err: sentinel}
	if !errors.Is(wrapped, sentinel) {
		t.Fatal("normalizing must keep the original error reachable through errors.Is")
	}
	if got := wrapped.Error(); got != "provider exploded" {
		t.Fatalf("Error() = %q, want the wrapped error's text", got)
	}
	// With no wrapped error the message stands in for the text.
	if got := (&APIError{Message: "just a message"}).Error(); got != "just a message" {
		t.Fatalf("Error() = %q, want the message", got)
	}
}

func TestIsRetryableNetworkErrorClassification(t *testing.T) {
	t.Parallel()

	opTimeout := &net.OpError{Op: "read", Net: "tcp", Err: errors.New("i/o timeout")}
	urlErr := &url.Error{Op: "Post", URL: "https://api.example/v1/chat", Err: &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}}
	nxdomain := &net.DNSError{Err: "no such host", Name: "api.example.invalid", IsNotFound: true}
	badStatus := &APIError{StatusCode: http.StatusBadRequest, Message: "unexpected role"}
	rateLimited := &APIError{StatusCode: http.StatusTooManyRequests, Message: "slow down"}
	wrappedTransport := &APIError{Err: opTimeout}
	// The server statuses the network-retry layer now owns: 408/409 and 5xx
	// minus 529 (overload keeps its own recovery layer) and 429 (rate limits
	// have their own path).
	requestTimeout := &APIError{StatusCode: http.StatusRequestTimeout, Message: "try again"}
	conflict := &APIError{StatusCode: http.StatusConflict, Message: "conflict"}
	internalError := &APIError{StatusCode: http.StatusInternalServerError, Message: "boom"}
	badGateway := &APIError{StatusCode: http.StatusBadGateway, Message: "upstream gone"}
	serviceUnavailable := &APIError{StatusCode: http.StatusServiceUnavailable, Message: "unavailable"}
	gatewayTimeout := &APIError{StatusCode: http.StatusGatewayTimeout, Message: "gateway timeout"}
	overloaded := &APIError{StatusCode: 529, Message: "overloaded_error"}
	unauthorized := &APIError{StatusCode: http.StatusUnauthorized, Message: "bad key"}
	notFound := &APIError{StatusCode: http.StatusNotFound, Message: "no such model"}

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "op error timeout", err: opTimeout, want: true},
		{name: "url error refused", err: urlErr, want: true},
		{name: "econnreset", err: syscall.ECONNRESET, want: true},
		{name: "econnaborted", err: syscall.ECONNABORTED, want: true},
		{name: "unexpected eof", err: io.ErrUnexpectedEOF, want: true},
		{name: "bare eof", err: io.EOF, want: true},
		{name: "stream truncated", err: ErrStreamTruncated, want: true},
		{name: "stream stalled on headers", err: &StreamStallError{Headers: true, After: time.Second}, want: true},
		{name: "stream stalled on body", err: fmt.Errorf("wrap: %w", &StreamStallError{After: time.Second}), want: true},
		{name: "non-streaming timeout", err: fmt.Errorf("wrap: %w", ErrNonStreamingTimeout), want: true},
		{name: "wrapped transport inside api error", err: wrappedTransport, want: true},
		{name: "408 request timeout", err: requestTimeout, want: true},
		{name: "409 conflict", err: conflict, want: true},
		{name: "500 internal", err: internalError, want: true},
		{name: "502 bad gateway", err: badGateway, want: true},
		{name: "503 service unavailable", err: serviceUnavailable, want: true},
		{name: "504 gateway timeout", err: gatewayTimeout, want: true},
		{name: "529 overloaded", err: overloaded, want: false},
		{name: "context cancelled", err: context.Canceled, want: false},
		{name: "context deadline", err: context.DeadlineExceeded, want: false},
		{name: "bad request", err: badStatus, want: false},
		{name: "rate limited", err: rateLimited, want: false},
		{name: "unauthorized", err: unauthorized, want: false},
		{name: "not found", err: notFound, want: false},
		{name: "dns nxdomain", err: nxdomain, want: false},
		{name: "x509 unknown authority", err: x509.UnknownAuthorityError{}, want: false},
		{name: "x509 hostname", err: x509.HostnameError{Host: "api.example"}, want: false},
		{name: "x509 invalid certificate", err: x509.CertificateInvalidError{Reason: x509.Expired}, want: false},
		{name: "plain error", err: errors.New("some build failure"), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsRetryableNetworkError(tt.err); got != tt.want {
				t.Fatalf("IsRetryableNetworkError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// realDialTimeout is the error Go's own dialer returns for a dial that ran
// out of its timeout, made by the real dialer: an already-expired timeout
// fails before any packet leaves, with the error a 30s timeout gives — net's
// errTimeout, which deliberately matches context.DeadlineExceeded.
func realDialTimeout(t *testing.T) error {
	t.Helper()
	client := &http.Client{Transport: &http.Transport{DialContext: (&net.Dialer{Timeout: time.Nanosecond}).DialContext}}
	resp, err := client.Post("https://192.0.2.1/v1/chat/completions", "application/json", strings.NewReader("{}"))
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil || !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "dial tcp 192.0.2.1:443: i/o timeout") {
		t.Fatalf("sanity: dial error = %v, want net's errTimeout behind \"dial tcp …: i/o timeout\"", err)
	}
	return err
}

// A dial that times out is the network's failure whichever error net's
// connect race hands back — errTimeout (matches context.DeadlineExceeded) or
// os.ErrDeadlineExceeded; both read "dial tcp …: i/o timeout". Reading only
// the second as transport made one outage retry on some attempts and give up
// on others. The caller's own deadline, with no socket operation behind it,
// still is not a transport failure.
func TestDialTimeoutIsATransportFailureWhicheverWayItSurfaces(t *testing.T) {
	t.Parallel()

	transport := map[string]error{
		"dial context timeout": realDialTimeout(t),
		"dial socket deadline": &url.Error{Op: "Post", URL: "https://api.example/v1/chat/completions",
			Err: &net.OpError{Op: "dial", Net: "tcp", Err: os.ErrDeadlineExceeded}},
		"dns lookup timeout": &url.Error{Op: "Post", URL: "https://api.example/v1/chat/completions",
			Err: &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{Err: "i/o timeout", Name: "api.example", IsTimeout: true, UnwrapErr: context.DeadlineExceeded}}},
	}
	for name, err := range transport {
		if !IsRetryableNetworkError(err) {
			t.Errorf("%s: IsRetryableNetworkError(%v) = false, want true", name, err)
		}
		if got := ClassifyStreamFailure(err, StreamProgressSnapshot{}); got != CauseConnectionLost {
			t.Errorf("%s: ClassifyStreamFailure(%v) = %q, want %q", name, err, got, CauseConnectionLost)
		}
	}

	callerDeadline := map[string]error{
		"bare":           context.DeadlineExceeded,
		"wrapped":        fmt.Errorf("turn: %w", context.DeadlineExceeded),
		"on a request":   &url.Error{Op: "Post", URL: "https://api.example/v1/chat/completions", Err: context.DeadlineExceeded},
		"cancelled dial": &url.Error{Op: "Post", URL: "https://api.example/v1/chat/completions", Err: &net.OpError{Op: "dial", Net: "tcp", Err: context.Canceled}},
	}
	for name, err := range callerDeadline {
		if IsRetryableNetworkError(err) {
			t.Errorf("%s: IsRetryableNetworkError(%v) = true, want false", name, err)
		}
	}
}
