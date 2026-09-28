// Rate-limit and quota detail lifted out of a provider's throttling response.
package llm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// RateLimit is a provider's refusal to serve a request because an allowance is
// spent, in the vendor-neutral form pkg/llm classifies.
//
// Providers describe the same event in incompatible payloads: the ChatGPT
// subscription endpoint answers with a plan name plus resets_at/
// resets_in_seconds, the OpenAI platform with an insufficient_quota code, and
// others with nothing but a Retry-After header. Each provider package parses
// the body it received at the boundary -- the only place that knows its own
// shape -- and hands the result here, so surfaces have a fact to render ("the
// allowance returns at 15:30") instead of a transport dump.
type RateLimit struct {
	// Quota is true when a plan allowance is exhausted until a stated reset,
	// false when the provider is only asking for a slower request rate.
	Quota bool
	// Plan names the subscription tier the limit belongs to ("plus", "pro"),
	// when the provider says which.
	Plan string
	// Reason is the provider's machine-readable classifier, such as
	// "usage_limit_reached" or "insufficient_quota".
	Reason string
	// Message is the provider's own human sentence, when it wrote one.
	Message string
	// ResetAt is when the allowance returns, zero when the provider said
	// nothing about it.
	ResetAt time.Time
	// RetryAfter is how long that wait was when the response arrived, zero when
	// unknown. It is recorded rather than recomputed so a delay between the
	// response and the render cannot turn a wait into a negative number.
	RetryAfter time.Duration
}

const (
	// maxRateLimitPayloadDepth bounds the walk over a provider body. The
	// depth is needed because these payloads nest: a body arrives as
	// {"error":{...}} and, once the provider-error normalizer has run, the
	// original body is carried as a JSON string inside the message field.
	maxRateLimitPayloadDepth = 5
	// quotaRetryThreshold separates "slow down" from "your allowance is gone".
	// A provider that asks for a wait measured in hours is not throttling the
	// request rate, whatever it calls the error.
	quotaRetryThreshold = 10 * time.Minute
	// maxProviderProse caps how much provider text a surface will show, so an
	// error page or an HTML body cannot flood the transcript.
	maxProviderProse = 400
)

// ParseRateLimit reads the throttling detail out of a provider's error
// response. It returns nil for any status that is not a throttle, and a
// RateLimit with whatever could be recovered otherwise -- an opaque 429 still
// deserves a sentence saying the provider is refusing requests.
//
// payload is the provider's error body as the provider package received it,
// retryAfterHeader the response's Retry-After value (empty when absent), and
// receivedAt the moment the response arrived.
func ParseRateLimit(status int, payload, retryAfterHeader string, receivedAt time.Time) *RateLimit {
	if status != http.StatusTooManyRequests {
		return nil
	}
	var fields rateLimitFields
	collectRateLimitFields([]byte(payload), maxRateLimitPayloadDepth, &fields)

	limit := &RateLimit{Plan: fields.plan, Reason: fields.reason, Message: fields.message}
	if fields.resetAtUnix > 0 {
		limit.ResetAt = time.Unix(fields.resetAtUnix, 0)
	} else {
		limit.ResetAt = resetAtFromProse(fields.message, receivedAt)
	}
	switch {
	case fields.resetInSeconds > 0:
		limit.RetryAfter = secondsToDuration(fields.resetInSeconds)
	case strings.TrimSpace(retryAfterHeader) != "":
		limit.RetryAfter = parseRetryAfter(retryAfterHeader, receivedAt)
	}
	if limit.RetryAfter <= 0 && !limit.ResetAt.IsZero() {
		limit.RetryAfter = limit.ResetAt.Sub(receivedAt)
	}
	if limit.ResetAt.IsZero() && limit.RetryAfter > 0 {
		limit.ResetAt = receivedAt.Add(limit.RetryAfter)
	}
	if limit.RetryAfter < 0 {
		limit.RetryAfter = 0
	}
	limit.Quota = isQuotaReason(fields.reason) ||
		isQuotaReason(fields.message) ||
		limit.RetryAfter > quotaRetryThreshold
	return limit
}

// RateLimitFromError returns the throttling detail a provider attached to err,
// or nil when err is not a throttled provider call. It looks through wrappers,
// so a failure that exhausted the retry middleware still reports what the
// provider said.
func RateLimitFromError(err error) *RateLimit {
	if err == nil {
		return nil
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr == nil {
		return nil
	}
	return apiErr.RateLimit
}

// rateLimitFields is the raw harvest from a provider body, before any of it is
// turned into time.Time or time.Duration.
type rateLimitFields struct {
	reason         string
	plan           string
	message        string
	resetAtUnix    int64
	resetInSeconds float64
}

// collectRateLimitFields walks a JSON provider body and fills out with the
// first value it finds for each field. Keys are visited in sorted order so the
// harvest does not depend on Go's randomized map iteration: with two keys
// carrying the same field, the same one always wins.
func collectRateLimitFields(raw []byte, depth int, out *rateLimitFields) {
	if depth <= 0 || out == nil {
		return
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || (trimmed[0] != '{' && trimmed[0] != '[') {
		return
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return
	}
	walkRateLimitValue(doc, depth, out)
}

func walkRateLimitValue(value any, depth int, out *rateLimitFields) {
	if depth <= 0 {
		return
	}
	switch node := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(node))
		for key := range node {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			assignRateLimitField(strings.ToLower(strings.TrimSpace(key)), node[key], depth, out)
		}
	case []any:
		for _, item := range node {
			walkRateLimitValue(item, depth-1, out)
		}
	}
}

func assignRateLimitField(key string, value any, depth int, out *rateLimitFields) {
	switch typed := value.(type) {
	case string:
		if assignRateLimitText(key, typed, depth, out) {
			return
		}
		// Some providers send their timings as quoted numbers.
		if n, err := strconv.ParseFloat(strings.TrimSpace(typed), 64); err == nil {
			assignRateLimitNumber(key, n, out)
		}
	case json.Number:
		if n, err := typed.Float64(); err == nil {
			assignRateLimitNumber(key, n, out)
		}
	case map[string]any, []any:
		walkRateLimitValue(value, depth-1, out)
	}
}

// assignRateLimitText reports whether key names a text field, so the caller
// knows not to also read the value as a number.
func assignRateLimitText(key, value string, depth int, out *rateLimitFields) bool {
	text := strings.TrimSpace(value)
	switch key {
	case "type", "code", "error_type", "reason":
		if out.reason == "" {
			out.reason = text
		}
		return true
	case "plan_type", "plan", "plan_name":
		if out.plan == "" {
			out.plan = text
		}
		return true
	case "message", "detail", "description", "error_description":
		// A normalized provider error carries the original body as a JSON
		// string here; the sentence a human should read is inside it.
		if looksLikeJSONDocument(text) {
			collectRateLimitFields([]byte(text), depth-1, out)
			return true
		}
		if out.message == "" {
			out.message = text
		}
		return true
	default:
		return false
	}
}

func assignRateLimitNumber(key string, n float64, out *rateLimitFields) {
	switch key {
	case "resets_at", "reset_at", "reset_time", "resets_at_seconds":
		if out.resetAtUnix == 0 {
			out.resetAtUnix = unixSecondsFrom(n)
		}
	case "resets_in_seconds", "reset_in_seconds", "reset_after_seconds", "retry_after", "retry_after_seconds":
		if out.resetInSeconds == 0 && n > 0 {
			out.resetInSeconds = n
		}
	case "retry_after_ms", "reset_after_ms", "resets_in_ms":
		if out.resetInSeconds == 0 && n > 0 {
			out.resetInSeconds = n / 1000
		}
	}
}

// proseTimestamp matches an absolute date-and-clock stamp written into a
// sentence. It is deliberately narrow: a four-digit year, a calendar date and
// at least hours and minutes, in the two separators these payloads use. A
// looser pattern would start reading version numbers and request ids as times.
var proseTimestamp = regexp.MustCompile(`(\d{4})[-/](\d{1,2})[-/](\d{1,2})[ T](\d{1,2}):(\d{2})(?::(\d{2}))?\s*(Z|z|[+-]\d{2}:?\d{2})?`)

// resetAtFromProse recovers a reset moment the provider stated only in its
// sentence.
//
// Not every provider sends the fact in a field. Several answer a spent
// allowance with nothing but prose -- {"error":{"code":"1308","message":"已达到
// 5 小时的使用上限。您的限额将在 2026-09-10 19:16:37 重置。"}} -- and if the
// sentence is treated as decoration the runtime learns nothing: it reads a
// multi-hour allowance as a momentary throttle and then tells the reader the
// provider said nothing about a reset, directly under the sentence that said
// exactly when.
//
// A stamp carrying its own zone is honoured. One without a zone is read on the
// caller's clock, which is the clock the account holder compares it against
// and the one the reset is rendered back in. A stamp that is not in the future
// is not a reset -- it is a stale time or a misread zone -- and is ignored.
func resetAtFromProse(text string, receivedAt time.Time) time.Time {
	for _, match := range proseTimestamp.FindAllStringSubmatch(text, -1) {
		when, err := timeFromProseMatch(match, receivedAt.Location())
		if err != nil || !when.After(receivedAt) {
			continue
		}
		return when
	}
	return time.Time{}
}

// timeFromProseMatch rebuilds a submatch into a time. The fields go back
// through the time package rather than through arithmetic here so that an
// impossible stamp -- month 13, or February 30th -- is rejected as the parse
// error it is.
func timeFromProseMatch(match []string, loc *time.Location) (time.Time, error) {
	number := func(text string) int {
		n, _ := strconv.Atoi(text)
		return n
	}
	second := 0
	if match[6] != "" {
		second = number(match[6])
	}
	date := fmt.Sprintf("%04d-%02d-%02dT%02d:%02d:%02d",
		number(match[1]), number(match[2]), number(match[3]),
		number(match[4]), number(match[5]), second)
	zone := strings.ToUpper(match[7])
	if zone == "" {
		return time.ParseInLocation("2006-01-02T15:04:05", date, loc)
	}
	if zone != "Z" && !strings.Contains(zone, ":") {
		zone = zone[:3] + ":" + zone[3:]
	}
	return time.Parse(time.RFC3339, date+zone)
}

// unixSecondsFrom normalizes an epoch stamp that may have been sent in
// milliseconds. Anything at or before the epoch carries no reset information.
func unixSecondsFrom(n float64) int64 {
	if n <= 0 {
		return 0
	}
	if n > 1e12 {
		return int64(n / 1000)
	}
	return int64(n)
}

func secondsToDuration(seconds float64) time.Duration {
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds * float64(time.Second))
}

// parseRetryAfter reads the two forms RFC 9110 allows for Retry-After: a
// delay in seconds, or an HTTP date.
func parseRetryAfter(header string, receivedAt time.Time) time.Duration {
	header = strings.TrimSpace(header)
	if header == "" {
		return 0
	}
	if seconds, err := strconv.ParseFloat(header, 64); err == nil {
		return secondsToDuration(seconds)
	}
	if when, err := http.ParseTime(header); err == nil {
		if d := when.Sub(receivedAt); d > 0 {
			return d
		}
	}
	return 0
}

func isQuotaReason(text string) bool {
	s := strings.ToLower(strings.TrimSpace(text))
	if s == "" {
		return false
	}
	for _, marker := range []string{
		"usage_limit",
		"usage limit",
		"quota",
		"insufficient",
		"billing",
		"credit",
		"plan_limit",
		"plan limit",
		// Providers that answer in Chinese carry no reason code worth
		// matching: the sentence is the only classifier they send, so the
		// wording for a spent allowance is matched here as well. The markers
		// name an allowance, never a request rate, so a throttle ("请求过于
		// 频繁") stays a throttle.
		"使用上限",
		"用量上限",
		"限额",
		"限額",
		"配额",
		"配額",
		"额度",
		"額度",
		"余额不足",
		"餘額不足",
	} {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}

func looksLikeJSONDocument(text string) bool {
	text = strings.TrimSpace(text)
	return len(text) > 1 && (text[0] == '{' || text[0] == '[')
}
