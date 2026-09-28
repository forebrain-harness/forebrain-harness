package anthropic

import (
	"errors"
	"net/http"
	"time"

	anthropicapi "github.com/anthropics/anthropic-sdk-go"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

// normalizeError converts an anthropic-sdk-go error into the vendor-neutral
// shape pkg/llm classifies, so that Layer 0 never has to import this SDK.
//
// Message is not taken from the SDK's own rendering: that renders from a live
// *http.Request and *http.Response and panics when either is missing, so the
// string is produced lazily through llm.APIError.Error() -- which delegates to
// the wrapped error -- rather than eagerly here, where every failed call would
// pay for it whether or not anything ever reads it. The response body the SDK
// already holds as a string is free to read, and is what a surface needs to
// tell the user what Anthropic refused and why.
//
// Errors that are not from the SDK pass through untouched; wrapping them would
// claim an HTTP status this layer never saw.
func normalizeError(err error) error {
	if err == nil {
		return nil
	}
	var apiErr *anthropicapi.Error
	if !errors.As(err, &apiErr) || apiErr == nil {
		return err
	}
	body := apiErr.RawJSON()
	return &llm.APIError{
		StatusCode: apiErr.StatusCode,
		Message:    body,
		Err:        err,
		RateLimit: llm.ParseRateLimit(
			apiErr.StatusCode,
			body,
			retryAfterHeader(apiErr.Response),
			time.Now(),
		),
	}
}

// retryAfterHeader reads Retry-After off a response the SDK kept. The response
// is the SDK's to populate, so it may legitimately be absent on an error the
// SDK built by hand.
func retryAfterHeader(resp *http.Response) string {
	if resp == nil {
		return ""
	}
	return resp.Header.Get("Retry-After")
}
