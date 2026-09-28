package llm

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"
)

func TestExplainErrorCodexUsageLimitSaysWhatAndWhen(t *testing.T) {
	received := time.Unix(1788534136, 0)
	err := &APIError{
		StatusCode: http.StatusTooManyRequests,
		Type:       "usage_limit_reached",
		Message:    codexUsageLimitBody,
		Err:        errors.New(`POST "https://chatgpt.com/backend-api/codex/responses": 429 Too Many Requests ` + codexUsageLimitBody),
		RateLimit:  ParseRateLimit(http.StatusTooManyRequests, codexUsageLimitBody, "", received),
	}

	got := ExplainError(err)
	for _, want := range []string{
		"Usage limit reached on your plus plan",
		"available again in 2h 28m",
		time.Unix(1788543035, 0).Local().Format("2006-01-02 15:04 MST"),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("explanation is missing %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"{", "}", "429", "https://"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("explanation leaks transport detail %q:\n%s", unwanted, got)
		}
	}
}

// A provider that writes the whole refusal in prose used to produce an
// explanation that argued with itself: an English shell around an untranslated
// sentence, a line claiming the provider never said when the limit resets, and
// an invitation to retry in a few minutes -- under a sentence naming a
// five-hour allowance and the minute it comes back. It now reads as one
// sentence that agrees with the provider.
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
	want := "Usage limit reached for this account — available again in 1h 32m, at " +
		reset.Format("2006-01-02 15:04 MST") + "."
	if got != want {
		t.Errorf("explanation =\n%s\nwant:\n%s", got, want)
	}
	for _, r := range got {
		if unicode.Is(unicode.Han, r) {
			t.Errorf("the terminal wording must be written in one language, got:\n%s", got)
			break
		}
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
	if got := ExplainError(wrapped); !strings.HasPrefix(got, "Usage limit reached") {
		t.Errorf("a retried failure must still explain the provider's refusal, got:\n%s", got)
	}
}

func TestExplainErrorByStatus(t *testing.T) {
	cases := []struct {
		name   string
		err    *APIError
		want   string
		unwant string
	}{
		{
			name: "unauthorized names the credentials",
			err: &APIError{
				StatusCode: http.StatusUnauthorized,
				Message:    `{"error":{"message":"Incorrect API key provided","type":"invalid_request_error"}}`,
				Err:        errors.New("raw dump"),
			},
			want:   "rejected this request's credentials",
			unwant: "raw dump",
		},
		{
			name: "server error is described as the provider's problem",
			err: &APIError{
				StatusCode: http.StatusBadGateway,
				Err:        errors.New("raw dump"),
			},
			want:   "having trouble on its side",
			unwant: "raw dump",
		},
		{
			name: "unclassified 4xx leads with the provider's sentence",
			err: &APIError{
				StatusCode: http.StatusUnprocessableEntity,
				Message:    `{"error":{"message":"tool_choice is not supported for this model"}}`,
				Err:        errors.New("raw dump"),
			},
			want:   "tool_choice is not supported for this model",
			unwant: "raw dump",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExplainError(tc.err)
			if !strings.Contains(got, tc.want) {
				t.Errorf("explanation is missing %q:\n%s", tc.want, got)
			}
			if strings.Contains(got, tc.unwant) {
				t.Errorf("explanation leaks %q:\n%s", tc.unwant, got)
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
	err := &APIError{
		StatusCode: http.StatusBadRequest,
		Code:       "context_length_exceeded",
		Message:    "This model's maximum context length is 128000 tokens",
		Err:        errors.New("raw dump"),
	}
	if got := ExplainError(err); !strings.Contains(got, "context window") {
		t.Errorf("a size rejection should name the context window, got:\n%s", got)
	}
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
