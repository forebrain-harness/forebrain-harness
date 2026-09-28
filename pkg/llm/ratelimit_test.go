package llm

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// codexUsageLimitBody is a verbatim ChatGPT-subscription 429 body as it reaches
// APIError: the provider answered with {"error":{...}}, the provider-error
// normalizer lifted that object to the top level and kept the original body as
// the message string, and the SDK unwrapped the "error" key.
const codexUsageLimitBody = `{"eligible_promo":null,"message":"{\"error\":{\"type\":\"usage_limit_reached\",\"message\":\"The usage limit has been reached\",\"plan_type\":\"plus\",\"resets_at\":1788543035,\"eligible_promo\":null,\"resets_in_seconds\":8899}}","plan_type":"plus","resets_at":1788543035,"resets_in_seconds":8899,"type":"usage_limit_reached"}`

func TestParseRateLimitCodexUsageLimit(t *testing.T) {
	received := time.Unix(1788534136, 0)
	limit := ParseRateLimit(http.StatusTooManyRequests, codexUsageLimitBody, "", received)
	if limit == nil {
		t.Fatal("expected a rate limit for a 429 response")
	}
	if !limit.Quota {
		t.Error("a spent subscription allowance is a quota, not a throttle")
	}
	if limit.Plan != "plus" {
		t.Errorf("plan = %q, want %q", limit.Plan, "plus")
	}
	if limit.Reason != "usage_limit_reached" {
		t.Errorf("reason = %q, want %q", limit.Reason, "usage_limit_reached")
	}
	if limit.Message != "The usage limit has been reached" {
		t.Errorf("message = %q, want the provider's own sentence", limit.Message)
	}
	if want := time.Unix(1788543035, 0); !limit.ResetAt.Equal(want) {
		t.Errorf("reset at = %v, want %v", limit.ResetAt, want)
	}
	if want := 8899 * time.Second; limit.RetryAfter != want {
		t.Errorf("retry after = %v, want %v", limit.RetryAfter, want)
	}
}

// proseOnlyUsageLimitBody is a verbatim 429 from a provider that states the
// whole refusal in prose: no reset field, a numeric error code that classifies
// nothing, and the sentence written in the account holder's language.
const proseOnlyUsageLimitBody = `{"error":{"code":"1308","message":"已达到 5 小时的使用上限。您的限额将在 2026-09-10 19:16:37 重置。"}}`

func TestParseRateLimitReadsAResetStatedOnlyInProse(t *testing.T) {
	reset := time.Date(2026, 9, 10, 19, 16, 37, 0, time.Local)
	received := reset.Add(-92 * time.Minute)
	limit := ParseRateLimit(http.StatusTooManyRequests, proseOnlyUsageLimitBody, "", received)
	if limit == nil {
		t.Fatal("expected a rate limit for a 429 response")
	}
	if !limit.Quota {
		t.Error("a spent five-hour allowance is a quota, not a throttle")
	}
	if !limit.ResetAt.Equal(reset) {
		t.Errorf("reset at = %v, want the moment named in the sentence %v", limit.ResetAt, reset)
	}
	if limit.RetryAfter != 92*time.Minute {
		t.Errorf("retry after = %v, want 1h32m", limit.RetryAfter)
	}
}

func TestResetAtFromProse(t *testing.T) {
	received := time.Date(2026, 9, 10, 17, 44, 0, 0, time.Local)
	cases := []struct {
		name string
		text string
		want time.Time
	}{
		{
			name: "space separated local stamp",
			text: "您的限额将在 2026-09-10 19:16:37 重置。",
			want: time.Date(2026, 9, 10, 19, 16, 37, 0, time.Local),
		},
		{
			name: "slash separated without seconds",
			text: "Quota resets at 2026/09/10 19:16",
			want: time.Date(2026, 9, 10, 19, 16, 0, 0, time.Local),
		},
		{
			name: "explicit zone is honoured over the local clock",
			text: "resets at 2026-09-10T12:00:00Z",
			want: time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC),
		},
		{
			name: "offset without a colon",
			text: "resets at 2026-09-10T20:00:00+0800",
			want: time.Date(2026, 9, 10, 20, 0, 0, 0, time.FixedZone("", 8*60*60)),
		},
		{name: "a stamp already past is not a reset", text: "reset at 2026-09-10 09:00:00"},
		{name: "an impossible stamp is not a time", text: "reset at 2026-02-30 19:16:37"},
		{name: "a version is not a time", text: "model 2026-09-10 is retired"},
		{name: "no stamp at all", text: "请求过于频繁，请稍后再试。"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resetAtFromProse(tc.text, received)
			if !got.Equal(tc.want) {
				t.Errorf("resetAtFromProse = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseRateLimitRetryAfterHeaderOnly(t *testing.T) {
	received := time.Unix(1788534136, 0)
	limit := ParseRateLimit(http.StatusTooManyRequests, `{"error":{"type":"rate_limit_error","message":"Number of requests has exceeded your rate limit"}}`, "30", received)
	if limit == nil {
		t.Fatal("expected a rate limit for a 429 response")
	}
	if limit.Quota {
		t.Error("a 30 second wait is a throttle, not a spent quota")
	}
	if limit.RetryAfter != 30*time.Second {
		t.Errorf("retry after = %v, want 30s", limit.RetryAfter)
	}
	if want := received.Add(30 * time.Second); !limit.ResetAt.Equal(want) {
		t.Errorf("reset at = %v, want %v", limit.ResetAt, want)
	}
	got := ExplainError(&APIError{StatusCode: http.StatusTooManyRequests, RateLimit: limit, Err: errors.New("raw")})
	if !strings.Contains(got, "The provider said: Number of requests has exceeded your rate limit") {
		t.Errorf("a throttle should quote the provider's own sentence as a quotation, got:\n%s", got)
	}
	if !strings.Contains(got, "available again in 30s") {
		t.Errorf("a throttle should say how long to wait, got:\n%s", got)
	}
}

func TestParseRateLimitRetryAfterHTTPDate(t *testing.T) {
	received := time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC)
	header := received.Add(90 * time.Second).UTC().Format(http.TimeFormat)
	limit := ParseRateLimit(http.StatusTooManyRequests, "", header, received)
	if limit == nil {
		t.Fatal("expected a rate limit for a 429 response")
	}
	if limit.RetryAfter != 90*time.Second {
		t.Errorf("retry after = %v, want 90s", limit.RetryAfter)
	}
}

func TestParseRateLimitOpaqueBodyStillExplains(t *testing.T) {
	limit := ParseRateLimit(http.StatusTooManyRequests, "<html>too many requests</html>", "", time.Now())
	if limit == nil {
		t.Fatal("a 429 with an unreadable body is still a rate limit")
	}
	got := ExplainError(&APIError{
		StatusCode: http.StatusTooManyRequests,
		Message:    "<html>too many requests</html>",
		RateLimit:  limit,
		Err:        errors.New("raw"),
	})
	if !strings.Contains(got, "no reset time given") {
		t.Errorf("an opaque 429 should admit the reset is unknown, got:\n%s", got)
	}
	if strings.Contains(got, "<html>") {
		t.Errorf("an error page is not a provider sentence and must not be quoted, got:\n%s", got)
	}
}

func TestParseRateLimitIgnoresOtherStatuses(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusBadRequest, http.StatusUnauthorized, http.StatusInternalServerError} {
		if limit := ParseRateLimit(status, codexUsageLimitBody, "60", time.Now()); limit != nil {
			t.Errorf("status %d must not be read as a rate limit", status)
		}
	}
}

func TestRateLimitFromError(t *testing.T) {
	limit := &RateLimit{Quota: true}
	err := fmt.Errorf("turn failed: %w", &APIError{StatusCode: http.StatusTooManyRequests, RateLimit: limit})
	if got := RateLimitFromError(err); got != limit {
		t.Errorf("RateLimitFromError = %v, want the attached limit", got)
	}
	if got := RateLimitFromError(errors.New("plain")); got != nil {
		t.Errorf("RateLimitFromError = %v, want nil for a non-provider error", got)
	}
}
