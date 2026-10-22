package llm

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// A spent allowance is the provider's own sentence, with the status that
// carried it; the runtime does not restate it in words of its own.
func TestExplainErrorCodexUsageLimitQuotesTheProvider(t *testing.T) {
	received := time.Unix(1788534136, 0)
	err := &APIError{
		StatusCode: http.StatusTooManyRequests,
		Type:       "usage_limit_reached",
		Message:    codexUsageLimitBody,
		Err:        errors.New(`POST "https://chatgpt.com/backend-api/codex/responses": 429 Too Many Requests ` + codexUsageLimitBody),
		RateLimit:  ParseRateLimit(http.StatusTooManyRequests, codexUsageLimitBody, "", received),
	}

	got := ExplainError(err)
	want := "The usage limit has been reached (HTTP 429 Too Many Requests)"
	if got != want {
		t.Errorf("explanation =\n%s\nwant:\n%s", got, want)
	}
	for _, unwanted := range []string{"{", "}", "https://"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("explanation leaks transport detail %q:\n%s", unwanted, got)
		}
	}
}

// A provider that writes the whole refusal in prose used to produce an
// explanation that argued with itself: an English shell around the
// untranslated sentence, a line claiming the provider never said when the
// limit resets, and an invitation to retry in a few minutes -- under a
// sentence naming a five-hour allowance and the minute it comes back. It now
// reads as the provider's sentence and the status, quoted verbatim.
func TestExplainErrorProseOnlyUsageLimitAgreesWithTheProvider(t *testing.T) {
	reset := time.Date(2026, 9, 10, 19, 16, 37, 0, time.Local)
	received := reset.Add(-92 * time.Minute)
	err := &APIError{
		StatusCode: http.StatusTooManyRequests,
		Code:       "1308",
		Message:    proseOnlyUsageLimitBody,
		Err:        errors.New("error, status code: 429, status: 429 Too Many Requests, message: " + proseOnlyUsageLimitBody),
		RateLimit:  ParseRateLimit(http.StatusTooManyRequests, proseOnlyUsageLimitBody, "", received),
	}

	got := ExplainError(err)
	want := "已达到 5 小时的使用上限。您的限额将在 2026-09-10 19:16:37 重置。 (HTTP 429 Too Many Requests)"
	if got != want {
		t.Errorf("explanation =\n%s\nwant:\n%s", got, want)
	}
}

// A gateway writes where the request died in front of the provider's sentence.
// That hop is not part of the refusal, so it comes off -- in both the plain and
// the JSON-enveloped body -- and a body that is only the prefix says nothing at
// all, leaving forebrain's own sentence to state the failure.
func TestExplainErrorDropsTheGatewayPrefix(t *testing.T) {
	const sentence = "Invalid credential"
	cases := []struct {
		name    string
		message string
		want    string
	}{
		{
			name:    "plain body",
			message: "Upstream request failed: " + sentence,
			want:    sentence + " (HTTP 401 Unauthorized)",
		},
		{
			name:    "json envelope",
			message: `{"error":{"message":"Upstream request failed: ` + sentence + `","type":"invalid_request_error"}}`,
			want:    sentence + " (HTTP 401 Unauthorized)",
		},
		{
			name:    "prefix without a sentence",
			message: "Upstream request failed:",
			want:    "The provider rejected this request's credentials (HTTP 401 Unauthorized).",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExplainError(&APIError{
				StatusCode: http.StatusUnauthorized,
				Message:    tc.message,
				Err:        errors.New("raw dump"),
			})
			if got != tc.want {
				t.Errorf("explanation =\n%s\nwant:\n%s", got, tc.want)
			}
			if strings.Contains(got, "Upstream request failed") {
				t.Errorf("explanation still names the gateway hop:\n%s", got)
			}
		})
	}
}

func TestExplainErrorLooksThroughRetryWrapper(t *testing.T) {
	inner := &APIError{
		StatusCode: http.StatusTooManyRequests,
		Message:    codexUsageLimitBody,
		Err:        errors.New("raw transport dump"),
		RateLimit:  ParseRateLimit(http.StatusTooManyRequests, codexUsageLimitBody, "", time.Unix(1788534136, 0)),
	}
	wrapped := &MaxAttemptsExceededError{Attempts: 3, Err: inner}
	if got := ExplainError(wrapped); !strings.HasPrefix(got, "The usage limit has been reached") {
		t.Errorf("a retried failure must still explain the provider's refusal, got:\n%s", got)
	}
}

func TestExplainErrorByStatus(t *testing.T) {
	cases := []struct {
		name    string
		err     *APIError
		want    string
		unwants []string
	}{
		{
			name: "unauthorized quotes the provider's refusal",
			err: &APIError{
				StatusCode: http.StatusUnauthorized,
				Message:    `{"error":{"message":"Incorrect API key provided","type":"invalid_request_error"}}`,
				Err:        errors.New("raw dump"),
			},
			want:    "Incorrect API key provided (HTTP 401 Unauthorized)",
			unwants: []string{"raw dump", "sign-in may have expired"},
		},
		{
			name: "server error is described as the provider's problem",
			err: &APIError{
				StatusCode: http.StatusBadGateway,
				Err:        errors.New("raw dump"),
			},
			want:    "having trouble on its side (HTTP 502 Bad Gateway).",
			unwants: []string{"raw dump", "should be retried"},
		},
		{
			name: "unclassified 4xx leads with the provider's sentence",
			err: &APIError{
				StatusCode: http.StatusUnprocessableEntity,
				Message:    `{"error":{"message":"tool_choice is not supported for this model"}}`,
				Err:        errors.New("raw dump"),
			},
			want:    "tool_choice is not supported for this model (HTTP 422 Unprocessable Entity)",
			unwants: []string{"raw dump"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExplainError(tc.err)
			if !strings.Contains(got, tc.want) {
				t.Errorf("explanation is missing %q:\n%s", tc.want, got)
			}
			for _, unwant := range tc.unwants {
				if strings.Contains(got, unwant) {
					t.Errorf("explanation leaks %q:\n%s", unwant, got)
				}
			}
		})
	}
}

func TestTruncateProseCutsOnRuneBoundaries(t *testing.T) {
	long := strings.Repeat("限", maxProviderProse)
	got := truncateProse(long)
	if !utf8.ValidString(got) {
		t.Errorf("truncateProse cut a rune in half: %q", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncateProse = %q, want an ellipsis on a capped sentence", got)
	}
}

func TestExplainErrorPassesThroughNonProviderErrors(t *testing.T) {
	err := fmt.Errorf("resolve workspace: %w", errors.New("permission denied"))
	if got := ExplainError(err); got != err.Error() {
		t.Errorf("ExplainError = %q, want the original text %q", got, err.Error())
	}
	if got := ExplainError(nil); got != "" {
		t.Errorf("ExplainError(nil) = %q, want empty", got)
	}
}

func TestExplainErrorContextWindow(t *testing.T) {
	t.Run("quotes the provider when it said why", func(t *testing.T) {
		err := &APIError{
			StatusCode: http.StatusBadRequest,
			Code:       "context_length_exceeded",
			Message:    "This model's maximum context length is 128000 tokens",
			Err:        errors.New("raw dump"),
		}
		want := "This model's maximum context length is 128000 tokens (HTTP 400 Bad Request)"
		if got := ExplainError(err); got != want {
			t.Errorf("explanation =\n%s\nwant:\n%s", got, want)
		}
	})

	// A size rejection the provider explains no further still names the context
	// window, and stops at what happened: the advice that used to follow it is
	// not part of the sentence.
	t.Run("names the window when the provider said nothing", func(t *testing.T) {
		err := &APIError{
			StatusCode: http.StatusBadRequest,
			Code:       "context_length_exceeded",
			Err:        errors.New("raw dump"),
		}
		got := ExplainError(err)
		want := "The conversation no longer fits in the model's context window."
		if got != want {
			t.Errorf("explanation =\n%s\nwant:\n%s", got, want)
		}
	})
}

func TestFormatWait(t *testing.T) {
	cases := map[time.Duration]string{
		0:                              "less than a minute",
		45 * time.Second:               "45s",
		90 * time.Second:               "1m 30s",
		5 * time.Minute:                "5m",
		8899 * time.Second:             "2h 28m",
		3 * time.Hour:                  "3h",
		time.Hour + 30*time.Minute + 2: "1h 30m",
	}
	for d, want := range cases {
		if got := formatWait(d); got != want {
			t.Errorf("formatWait(%v) = %q, want %q", d, got, want)
		}
	}
}

// The web UI writes its own sentence from these facts, so the classification
// and the numbers behind it are a contract, not an implementation detail.
func TestExplainReturnsTheFactsBehindTheSentence(t *testing.T) {
	received := time.Unix(1788534136, 0)
	got, ok := Explain(&APIError{
		StatusCode: http.StatusTooManyRequests,
		Message:    codexUsageLimitBody,
		Err:        errors.New("raw transport dump"),
		RateLimit:  ParseRateLimit(http.StatusTooManyRequests, codexUsageLimitBody, "", received),
	})
	if !ok {
		t.Fatal("a provider 429 must be classifiable")
	}
	want := ErrorExplanation{
		Code:            ExplainRateLimitQuota,
		Status:          http.StatusTooManyRequests,
		Plan:            "plus",
		ProviderMessage: "The usage limit has been reached",
		ResetAt:         time.Unix(1788543035, 0),
		RetryAfter:      8899 * time.Second,
	}
	if got.Code != want.Code || got.Status != want.Status || got.Plan != want.Plan ||
		got.ProviderMessage != want.ProviderMessage || !got.ResetAt.Equal(want.ResetAt) ||
		got.RetryAfter != want.RetryAfter {
		t.Errorf("Explain = %+v, want %+v", got, want)
	}
}

func TestExplainClassifiesByStatus(t *testing.T) {
	cases := map[int]ExplanationCode{
		http.StatusUnauthorized:        ExplainCredentials,
		http.StatusForbidden:           ExplainCredentials,
		http.StatusPaymentRequired:     ExplainBilling,
		http.StatusNotFound:            ExplainUnknownModel,
		http.StatusGatewayTimeout:      ExplainTimeout,
		http.StatusServiceUnavailable:  ExplainProviderDown,
		http.StatusUnprocessableEntity: ExplainRejected,
	}
	for status, want := range cases {
		got, ok := Explain(&APIError{StatusCode: status, Err: errors.New("dump")})
		if !ok {
			t.Errorf("status %d must be classifiable", status)
			continue
		}
		if got.Code != want {
			t.Errorf("status %d classified as %q, want %q", status, got.Code, want)
		}
	}
}

func TestExplainDeclinesWhatItCannotImproveOn(t *testing.T) {
	if _, ok := Explain(errors.New("user prompt hook failed")); ok {
		t.Error("an ordinary error is not a provider failure")
	}
	if _, ok := Explain(&APIError{Err: errors.New("stream closed")}); ok {
		t.Error("a provider error with no status and no message says nothing extra")
	}
	if _, ok := Explain(nil); ok {
		t.Error("nil is not a failure")
	}
}
