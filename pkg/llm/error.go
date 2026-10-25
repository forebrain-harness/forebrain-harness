package llm

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"
)

const lengthErrorCode = "context_length_exceeded"

// ErrStreamTruncated reports a stream that ended without a finish frame after
// it had already produced output: the provider (or something between, most
// often a proxy) closed the connection cleanly, and the answer is missing its
// tail. Providers whose wire protocol carries finish frames must raise this
// instead of returning the partial text as a success.
var ErrStreamTruncated = errors.New("stream ended without a finish frame")

// ErrStreamNoEvents reports a stream that closed cleanly before producing a
// single event: no output, no finish frame. It is retried without streaming.
var ErrStreamNoEvents = errors.New("stream closed before any event")

// IsRetryableNetworkError reports whether err is a failure a resend can
// plausibly fix: transport-level ones (timeouts, resets, aborted/dropped
// connections, truncated streams, streams that went silent, non-streaming
// requests that ran out of NonStreamingTimeout) and the server's
// transient statuses (408,
// 409 and 5xx except 529, which has its own recovery layer). It is the retry
// predicate of the network-retry middleware; everything else — cancellation,
// provider rejections, request encoding, DNS NXDOMAIN (a misspelled base_url
// is permanent) and TLS certificate failures (permanent in proxy
// environments) — is excluded so the retry loop never parks a turn on an
// error that cannot clear. 429 stays out too: rate limits and usage caps
// have their own path.
func IsRetryableNetworkError(err error) bool {
	if err == nil {
		return false
	}
	// A stream the watchdog aborted for going silent: resend it before the
	// Canceled check, because the watchdog's abort arrives as a canceled
	// derived context and must not read as the user cancelling.
	if errors.Is(err, ErrStreamStalled) {
		return true
	}
	if isTransportFailure(err) {
		return true
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var aerr *APIError
	if errors.As(err, &aerr) && aerr != nil {
		switch status := aerr.StatusCode; {
		case status == http.StatusRequestTimeout, status == http.StatusConflict:
			return true // the server's own "send it again" statuses
		case status >= 500 && status <= 599 && status != 529:
			return true // server-side trouble that is not the overload path
		}
	}
	return false
}

// isTransportFailure reports whether err is a transport-level failure: a
// network error, an unexpected or bare EOF, a reset or aborted connection, a
// truncated stream, or a non-streaming request that ran out of its deadline.
// It is the transport slice of IsRetryableNetworkError. The failures a resend
// cannot plausibly fix are not transport: cancellation and the caller's own
// deadlines, DNS NXDOMAIN (a misspelled base_url is permanent), TLS
// certificate failures (permanent in proxy environments), and the server's
// own statuses — though a provider error that wraps a transport failure in
// its Err is the transport failure it wraps, exactly as the retry predicate
// reads it.
func isTransportFailure(err error) bool {
	if err == nil {
		return false
	}
	// The non-streaming deadline expiring is the same kind of trouble, and
	// arrives as a DeadlineExceeded, so it also has to be read before the
	// cancellation checks classify it as the user's own deadline.
	if errors.Is(err, ErrNonStreamingTimeout) {
		return true
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if isNXDOMAIN(err) {
		return false
	}
	if isCertificateFailure(err) {
		return false
	}
	var nerr net.Error
	if errors.As(err, &nerr) {
		return true // *net.OpError timeouts, *url.Error (incl. H2 via Do); refused stays retryable (a restarting proxy recovers).
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return true // common shape of an SSE stream cut mid-flight
	}
	if errors.Is(err, io.EOF) {
		return true // provider-level bare EOF on a live stream
	}
	if errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNABORTED) {
		return true
	}
	if errors.Is(err, ErrStreamTruncated) {
		return true
	}
	var aerr *APIError
	if errors.As(err, &aerr) && aerr != nil && aerr.Err != nil {
		return isTransportFailure(aerr.Err) // unwrap provider normalizeError wrappers
	}
	return false
}

// isNXDOMAIN reports whether err is a DNS name-resolution miss: a misspelled
// base_url is permanent, so it is not a failure a resend can fix.
func isNXDOMAIN(err error) bool {
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr) && dnsErr.IsNotFound
}

// isCertificateFailure reports whether err is a TLS certificate failure.
// Such failures are permanent (most common in proxied environments:
// self-signed or mismatched certificates); retrying only means waiting for
// nothing.
func isCertificateFailure(err error) bool {
	var uaErr x509.UnknownAuthorityError
	if errors.As(err, &uaErr) {
		return true
	}
	var hErr x509.HostnameError
	if errors.As(err, &hErr) {
		return true
	}
	var ciErr x509.CertificateInvalidError
	if errors.As(err, &ciErr) {
		return true
	}
	return false
}

// APIError is a provider error after the provider package has normalized it.
//
// It exists so that this package can classify provider failures without
// knowing any provider SDK. pkg/llm is Layer 0: pulling anthropic-sdk-go,
// openai-go and go-openai in here put all three vendor SDKs into the
// dependency closure of the one package everything else builds on. Each
// provider package owns the shape of its own SDK's error and converts it to
// this type at the boundary, which is the only place that knowledge belongs.
//
// Err keeps the original error so that errors.As still finds the SDK type for
// any caller that needs it, and so that Error() can stay lazy -- some SDK
// error types build their string from a live *http.Request and *http.Response
// and panic when those are absent, so the text is never rendered eagerly at
// the point of conversion.
type APIError struct {
	// StatusCode is the HTTP status, or 0 when the error carries none.
	StatusCode int
	// Code and Type are the provider's machine-readable classifiers.
	Code string
	Type string
	// Message is the provider's human-readable text. It is left empty when the
	// SDK offers no cheap field for it; Error() then falls back to Err.
	Message string
	// Err is the original provider error.
	Err error
	// RateLimit carries the throttling detail the provider sent with a 429 --
	// which allowance ran out, on which plan, and when it comes back. The
	// provider package fills it because only it knows the shape of its own
	// body; it is nil for every other failure.
	RateLimit *RateLimit
	// RetryAfter is the wait the provider asked for with Retry-After on any
	// status, zero when it sent none.
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	if e == nil {
		return ""
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return e.Message
}

func (e *APIError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func IsExceeded(err error) bool {
	if err == nil {
		return false
	}

	var aErr *APIError
	if errors.As(err, &aErr) && aErr != nil {
		if isContextLengthCode(aErr.Code) ||
			isContextLengthCode(aErr.Type) ||
			messageSaysContextExceeded(aErr.Message) {
			return true
		}
		if aErr.StatusCode == http.StatusRequestEntityTooLarge {
			return true
		}
		// A size rejection can also arrive as a plain 400 whose only evidence
		// is the prose in the body, which is what Error() exposes.
		if aErr.StatusCode == http.StatusBadRequest && messageSaysContextExceeded(aErr.Error()) {
			return true
		}
	}

	return messageSaysContextExceeded(err.Error())
}

// IsStructuralRequestError reports whether err is the provider rejecting the
// shape of the request rather than its size or the server's own state.
//
// Classification is by HTTP status only. Providers describe these failures in
// prose that differs per vendor and per API version ("unexpected tool_call_id",
// "first message must use the user role", and so on), so matching on message
// text would be both vendor-specific and brittle; 400 is the same signal
// everywhere. A retry that changes the request shape is the only thing that can
// clear a 400, which is exactly what the caller does.
func IsStructuralRequestError(err error) bool {
	if err == nil {
		return false
	}
	// Size rejections also arrive as 400 but have their own recovery path.
	if IsExceeded(err) {
		return false
	}
	return statusOf(err) == http.StatusBadRequest
}

// statusOf extracts the HTTP status from a normalized provider error.
// Returns 0 when the error carries no status.
func statusOf(err error) int {
	var aErr *APIError
	if errors.As(err, &aErr) && aErr != nil {
		return aErr.StatusCode
	}
	return 0
}

// HTTPStatusOf returns the HTTP status of a normalized provider error, 0 when
// it carries none.
func HTTPStatusOf(err error) int { return statusOf(err) }

// RetryAfterOf returns the Retry-After wait a normalized provider error
// carries, 0 when none.
func RetryAfterOf(err error) time.Duration {
	var aErr *APIError
	if errors.As(err, &aErr) && aErr != nil {
		return aErr.RetryAfter
	}
	return 0
}

func isContextLengthCode(code string) bool {
	return strings.EqualFold(strings.TrimSpace(code), lengthErrorCode)
}

// PartialResponseError says a request failed after it had produced something
// that must not be produced twice. The caller finishes from what completed.
type PartialResponseError struct {
	Err      error
	Progress StreamProgressSnapshot
	Cause    StreamFailureCause
	// Complete: the response had fully arrived; finish as a success, no notice.
	Complete bool
	// Failed: Claude Code's "fail" after output. The completed part stays; the
	// notice (or the turn's error) is Err itself, not an incomplete sentence.
	Failed bool
}

func (e *PartialResponseError) Error() string { return e.Err.Error() }
func (e *PartialResponseError) Unwrap() error { return e.Err }

func IsPartialResponse(err error) bool { var p *PartialResponseError; return errors.As(err, &p) }

// IncompleteSentence is what the user reads about a response that was cut.
// shown is true when the user had already seen part of the answer — its text
// or a tool call starting — and picks the sentence that names what the user
// is looking at; otherwise the sentence says no answer was produced at all.
func IncompleteSentence(cause StreamFailureCause, shown bool) string {
	if shown {
		switch cause {
		case CauseStalled:
			return "The response stopped arriving. The response above may be incomplete."
		case CauseServerError, CauseOverloaded, CauseTimedOut:
			return "Server error mid-response. The response above may be incomplete."
		case CauseMalformed:
			return "The response stream was malformed. The response above may be incomplete."
		case CauseSuspended:
			return "Your computer went to sleep mid-response. The response above may be incomplete."
		default:
			return "Connection lost mid-response. The response above may be incomplete."
		}
	}
	switch cause {
	case CauseStalled:
		return "The response stalled before a response was produced. Try again."
	case CauseMalformed:
		return "The response stream was malformed and no response was produced. Try again."
	case CauseSuspended:
		return "Your computer went to sleep before a response was produced. Try again."
	default:
		return "Connection lost before a response was produced. Try again."
	}
}

func messageSaysContextExceeded(msg string) bool {
	s := strings.ToLower(strings.TrimSpace(msg))
	if s == "" {
		return false
	}
	switch {
	case strings.Contains(s, lengthErrorCode):
		return true
	case strings.Contains(s, "prompt is too long"):
		return true
	case strings.Contains(s, "maximum context length"):
		return true
	case strings.Contains(s, "maximum context window"):
		return true
	case strings.Contains(s, "context window"):
		return true
	case strings.Contains(s, "reduce the length") && strings.Contains(s, "messages"):
		return true
	default:
		return false
	}
}
