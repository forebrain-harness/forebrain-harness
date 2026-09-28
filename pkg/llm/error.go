package llm

import (
	"errors"
	"net/http"
	"strings"
)

const lengthErrorCode = "context_length_exceeded"

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

func isContextLengthCode(code string) bool {
	return strings.EqualFold(strings.TrimSpace(code), lengthErrorCode)
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
