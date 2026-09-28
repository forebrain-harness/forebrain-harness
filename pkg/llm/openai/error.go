package openai

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	openaigo "github.com/openai/openai-go"
	openaiclient "github.com/sashabaranov/go-openai"
)

// normalizeError converts an OpenAI SDK error into the vendor-neutral shape
// pkg/llm classifies, so that Layer 0 never has to import either SDK.
//
// Two SDKs are in play: go-openai for chat-completions-compatible endpoints
// and openai-go for the Responses API. Both are handled here because both are
// this package's business and neither is anyone else's.
//
// Errors that are not from either SDK pass through untouched; wrapping them
// would claim an HTTP status this layer never saw.
func normalizeError(err error) error {
	if err == nil {
		return nil
	}
	receivedAt := time.Now()
	var compatErr *openaiclient.APIError
	if errors.As(err, &compatErr) && compatErr != nil {
		// go-openai keeps no copy of the response, so the body this package
		// folded into Message is the only payload left to read.
		return &llm.APIError{
			StatusCode: compatErr.HTTPStatusCode,
			Code:       codeString(compatErr.Code),
			Type:       compatErr.Type,
			Message:    compatErr.Message,
			Err:        err,
			RateLimit:  llm.ParseRateLimit(compatErr.HTTPStatusCode, compatErr.Message, "", receivedAt),
		}
	}
	var respErr *openaigo.Error
	if errors.As(err, &respErr) && respErr != nil {
		return &llm.APIError{
			StatusCode: respErr.StatusCode,
			Code:       respErr.Code,
			Type:       respErr.Type,
			Message:    respErr.Message,
			Err:        err,
			RateLimit: llm.ParseRateLimit(
				respErr.StatusCode,
				respErr.RawJSON(),
				retryAfterHeader(respErr.Response),
				receivedAt,
			),
		}
	}
	return err
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

// codeString renders go-openai's untyped Code field. Only a string or a
// Stringer can carry a provider error code; anything else (a number, say) has
// no code semantics to preserve and normalizes to empty.
func codeString(code any) string {
	switch v := code.(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	default:
		return ""
	}
}
