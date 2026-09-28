package openai

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	openaigo "github.com/openai/openai-go"
	openaiclient "github.com/sashabaranov/go-openai"
)

// wrappedErr unwraps to a typed-nil error, reproducing what go-openai's stream
// reader can surface when an endpoint returns a non-SSE body: errors.As matches
// the concrete pointer type but leaves the target nil.
type wrappedErr struct{ inner error }

func (w wrappedErr) Error() string { return "wrapped" }
func (w wrappedErr) Unwrap() error { return w.inner }

func TestNormalizeErrorMapsCompatAPIError(t *testing.T) {
	t.Parallel()

	in := &openaiclient.APIError{
		HTTPStatusCode: http.StatusRequestEntityTooLarge,
		Code:           "context_length_exceeded",
		Type:           "invalid_request_error",
		Message:        "This model's maximum context length is 8192 tokens",
	}
	var got *llm.APIError
	if !errors.As(normalizeError(in), &got) || got == nil {
		t.Fatal("a go-openai APIError must normalize to *llm.APIError")
	}
	if got.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("StatusCode = %d, want 413", got.StatusCode)
	}
	if got.Code != "context_length_exceeded" {
		t.Fatalf("Code = %q", got.Code)
	}
	if got.Type != "invalid_request_error" {
		t.Fatalf("Type = %q", got.Type)
	}
	if got.Message != in.Message {
		t.Fatalf("Message = %q", got.Message)
	}
	// The classifier this feeds must reach the same verdict it did when it
	// type-asserted the SDK error directly.
	if !llm.IsExceeded(normalizeError(in)) {
		t.Fatal("a normalized context-length error must still classify as exceeded")
	}
}

func TestNormalizeErrorMapsResponsesError(t *testing.T) {
	t.Parallel()

	in := &openaigo.Error{
		StatusCode: http.StatusBadRequest,
		Code:       "context_length_exceeded",
		Type:       "invalid_request_error",
		Message:    "reduce the length of the messages",
	}
	var got *llm.APIError
	if !errors.As(normalizeError(in), &got) || got == nil {
		t.Fatal("an openai-go Error must normalize to *llm.APIError")
	}
	if got.StatusCode != http.StatusBadRequest {
		t.Fatalf("StatusCode = %d, want 400", got.StatusCode)
	}
	if got.Code != "context_length_exceeded" || got.Message != in.Message {
		t.Fatalf("Code = %q, Message = %q", got.Code, got.Message)
	}
	if !llm.IsExceeded(normalizeError(in)) {
		t.Fatal("a normalized context-length error must still classify as exceeded")
	}
}

func TestNormalizeErrorLeavesForeignErrorsAlone(t *testing.T) {
	t.Parallel()

	if normalizeError(nil) != nil {
		t.Fatal("nil must stay nil")
	}
	plain := errors.New("connection refused")
	if got := normalizeError(plain); got != plain {
		t.Fatalf("a non-SDK error must pass through untouched, got %#v", got)
	}
	// errors.As matches the SDK pointer type but leaves it nil; normalizing
	// must not dereference it or claim a status it never saw.
	var typedNil *openaiclient.APIError
	in := wrappedErr{inner: typedNil}
	if got := normalizeError(in); got != error(in) {
		t.Fatalf("a typed-nil SDK error must pass through untouched, got %#v", got)
	}
}

func TestCodeStringOnlyKeepsRealCodes(t *testing.T) {
	t.Parallel()

	if got := codeString("context_length_exceeded"); got != "context_length_exceeded" {
		t.Fatalf("codeString(string) = %q", got)
	}
	// go-openai types Code as any, so a numeric code is possible; it carries no
	// code semantics to preserve.
	if got := codeString(429); got != "" {
		t.Fatalf("codeString(int) = %q, want empty", got)
	}
	if got := codeString(nil); got != "" {
		t.Fatalf("codeString(nil) = %q, want empty", got)
	}
}

// codexUsageLimitResponse is the body ChatGPT's Codex endpoint returns once a
// subscription's allowance is spent.
const codexUsageLimitResponse = `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","plan_type":"plus","resets_at":1788543035,"eligible_promo":null,"resets_in_seconds":8899}}`

// TestResponsesUsageLimitExplainsInsteadOfDumpingJSON walks the whole path a
// subscription quota failure takes -- the provider's 429 body, the transport's
// error normalizer, the SDK's error type, this package's normalizeError -- and
// asserts the surfaces get a sentence rather than the transport dump they used
// to print.
func TestResponsesUsageLimitExplainsInsteadOfDumpingJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, codexUsageLimitResponse)
	}))
	defer server.Close()

	client := newOpenAIResponsesLLM("sk-test", server.URL+"/v1", "gpt-test", 64, nil)
	_, err := client.Execute(context.Background(), []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil)
	if err == nil {
		t.Fatal("a 429 response must fail the call")
	}

	limit := llm.RateLimitFromError(err)
	if limit == nil {
		t.Fatalf("the 429 carried no rate limit detail: %v", err)
	}
	if !limit.Quota || limit.Plan != "plus" {
		t.Errorf("limit = %+v, want a spent quota on the plus plan", limit)
	}
	if want := time.Unix(1788543035, 0); !limit.ResetAt.Equal(want) {
		t.Errorf("reset at = %v, want %v", limit.ResetAt, want)
	}

	explained := llm.ExplainError(err)
	if !strings.Contains(explained, "Usage limit reached on your plus plan") {
		t.Errorf("explanation does not say what happened:\n%s", explained)
	}
	if !strings.Contains(explained, "available again in") {
		t.Errorf("explanation does not say when it recovers:\n%s", explained)
	}
	if strings.Contains(explained, "usage_limit_reached") || strings.Contains(explained, "{") {
		t.Errorf("explanation still leaks the raw payload:\n%s", explained)
	}
}

// TestResponsesRateLimitUsesRetryAfterHeader covers the providers that say
// nothing useful in the body and put the whole answer in the header.
func TestResponsesRateLimitUsesRetryAfterHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Kept small: the SDK honours Retry-After between its own retries, so
		// a larger value would only make this test sleep.
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"Rate limit reached for gpt-test in organization org-1 on requests per min","type":"requests"}}`)
	}))
	defer server.Close()

	client := newOpenAIResponsesLLM("sk-test", server.URL+"/v1", "gpt-test", 64, nil)
	_, err := client.Execute(context.Background(), []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil)
	if err == nil {
		t.Fatal("a 429 response must fail the call")
	}
	limit := llm.RateLimitFromError(err)
	if limit == nil {
		t.Fatalf("the 429 carried no rate limit detail: %v", err)
	}
	if limit.Quota {
		t.Errorf("a 20 second wait is a throttle, not a spent quota: %+v", limit)
	}
	if limit.RetryAfter != time.Second {
		t.Errorf("retry after = %v, want 1s", limit.RetryAfter)
	}
	explained := llm.ExplainError(err)
	if !strings.Contains(explained, "available again in 1s") {
		t.Errorf("explanation does not carry the header's wait:\n%s", explained)
	}
}

func TestNormalizeErrorAttachesRateLimitFromCompatError(t *testing.T) {
	t.Parallel()

	in := &openaiclient.APIError{
		HTTPStatusCode: http.StatusTooManyRequests,
		Type:           "insufficient_quota",
		Message:        `{"error":{"type":"insufficient_quota","message":"You exceeded your current quota"}}`,
	}
	limit := llm.RateLimitFromError(normalizeError(in))
	if limit == nil {
		t.Fatal("a compat 429 must carry rate limit detail")
	}
	if !limit.Quota {
		t.Errorf("an exhausted quota must be classified as one: %+v", limit)
	}
	if limit.Message != "You exceeded your current quota" {
		t.Errorf("message = %q, want the provider's sentence", limit.Message)
	}
}
