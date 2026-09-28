package llm

import (
	"errors"
	"net/http"
	"testing"
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
