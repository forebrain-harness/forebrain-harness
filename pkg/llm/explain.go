// Classification and user-facing rendering of provider failures.
package llm

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ExplanationCode names the kind of provider failure a surface is describing.
// It is the stable half of an explanation: the wording may be translated or
// reworded per surface, the code may not change meaning.
type ExplanationCode string

const (
	// ExplainRateLimitQuota is a plan allowance spent until a stated reset.
	ExplainRateLimitQuota ExplanationCode = "rate_limit_quota"
	// ExplainRateLimitThrottle is the provider asking for a slower request rate.
	ExplainRateLimitThrottle ExplanationCode = "rate_limit_throttle"
	// ExplainContextWindow is a request larger than the model's context window.
	ExplainContextWindow ExplanationCode = "context_window"
	// ExplainCredentials is a rejected key or an expired sign-in.
	ExplainCredentials ExplanationCode = "credentials"
	// ExplainBilling is a refusal over the account's balance or subscription.
	ExplainBilling ExplanationCode = "billing"
	// ExplainUnknownModel is an unrecognised model name or endpoint.
	ExplainUnknownModel ExplanationCode = "unknown_model"
	// ExplainTimeout is the provider failing to answer in time.
	ExplainTimeout ExplanationCode = "timeout"
	// ExplainProviderDown is a fault on the provider's side.
	ExplainProviderDown ExplanationCode = "provider_down"
	// ExplainRejected is any other refusal of the request as sent.
	ExplainRejected ExplanationCode = "rejected"
)

// ErrorExplanation is a provider failure reduced to a code plus the facts
// needed to phrase it.
//
// The facts are carried separately rather than baked into a sentence at the
// boundary because the surfaces do not share a language: the terminal renders
// the English wording from ExplainError, while the web UI renders the same
// facts in the viewer's locale and re-renders them when the viewer switches
// language. A sentence assembled in the runtime could do neither.
type ErrorExplanation struct {
	// Code says which failure this is.
	Code ExplanationCode
	// Status is the HTTP status the provider answered with, 0 when none.
	Status int
	// Plan names the subscription tier a spent allowance belongs to, when the
	// provider says which.
	Plan string
	// ProviderMessage is the provider's own sentence, already unwrapped from
	// its JSON envelope and capped in length. Empty when it wrote none.
	ProviderMessage string
	// ResetAt is when a spent allowance returns, zero when unknown.
	ResetAt time.Time
	// RetryAfter is how long that wait was when the response arrived, zero when
	// unknown. Surfaces that render long after the fact should prefer ResetAt.
	RetryAfter time.Duration
}

// Explain classifies err as a provider failure. ok is false for anything else,
// in which case the error's own text is the best a surface can show.
func Explain(err error) (ErrorExplanation, bool) {
	if err == nil {
		return ErrorExplanation{}, false
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr == nil {
		return ErrorExplanation{}, false
	}

	out := ErrorExplanation{Status: apiErr.StatusCode, ProviderMessage: providerProse(apiErr)}
	if limit := apiErr.RateLimit; limit != nil {
		out.Code = ExplainRateLimitThrottle
		if limit.Quota {
			out.Code = ExplainRateLimitQuota
		}
		out.Plan = limit.Plan
		out.ResetAt = limit.ResetAt
		out.RetryAfter = limit.RetryAfter
		if text := truncateProse(limit.Message); text != "" {
			out.ProviderMessage = text
		}
		return out, true
	}
	if IsExceeded(err) {
		out.Code = ExplainContextWindow
		return out, true
	}
	switch {
	case apiErr.StatusCode == http.StatusUnauthorized, apiErr.StatusCode == http.StatusForbidden:
		out.Code = ExplainCredentials
	case apiErr.StatusCode == http.StatusPaymentRequired:
		out.Code = ExplainBilling
	case apiErr.StatusCode == http.StatusNotFound:
		out.Code = ExplainUnknownModel
	case apiErr.StatusCode == http.StatusRequestTimeout, apiErr.StatusCode == http.StatusGatewayTimeout:
		out.Code = ExplainTimeout
	case apiErr.StatusCode >= 500:
		out.Code = ExplainProviderDown
	case apiErr.StatusCode >= 400:
		out.Code = ExplainRejected
	default:
		// A provider error with no usable status says nothing this package can
		// improve on; let the caller fall back to the error's own text.
		if out.ProviderMessage == "" {
			return ErrorExplanation{}, false
		}
		out.Code = ExplainRejected
	}
	return out, true
}

// ExplainError renders err as English text a person can act on.
//
// A provider refusal reaches this package as a transport error whose string is
// the raw HTTP body -- an escaped JSON blob that says nothing to the person
// waiting on the turn. Every surface that shows a failed turn in English (the
// TUI's error frame, the scheduler's run record) calls this instead of
// err.Error(). The untouched error still reaches the log, which is where the
// raw body belongs.
//
// Errors that are not provider failures pass through with their own text: this
// only replaces a message it can improve on.
func ExplainError(err error) string {
	if err == nil {
		return ""
	}
	explanation, ok := Explain(err)
	if !ok {
		return strings.TrimSpace(err.Error())
	}
	return explanation.English()
}

// English renders the explanation as the wording the terminal shows.
//
// Every code renders as exactly one sentence. A failed turn interrupts what
// the reader was doing, so the wording says what happened and how it recovers
// in a single line and stops there; the advice that used to follow it said
// nothing the sentence did not already imply. The provider's own sentence,
// when it is quoted at all, still gets a line of its own -- that is a
// quotation, not part of the sentence forebrain writes.
func (e ErrorExplanation) English() string {
	switch e.Code {
	case ExplainRateLimitQuota, ExplainRateLimitThrottle:
		return e.englishRateLimit()
	case ExplainContextWindow:
		return "The conversation no longer fits in the model's context window — start a new session, or compact this one, to continue."
	case ExplainCredentials:
		return withDetail(
			"The provider rejected this request's credentials ("+httpLabel(e.Status)+") — the sign-in may have expired, or the API key may no longer be valid.",
			e.ProviderMessage)
	case ExplainBilling:
		return withDetail(
			"The provider refused the request for billing reasons ("+httpLabel(e.Status)+") — check the account's balance or subscription.",
			e.ProviderMessage)
	case ExplainUnknownModel:
		return withDetail(
			"The provider does not recognize this agent's model or endpoint ("+httpLabel(e.Status)+") — check the model name and base URL.",
			e.ProviderMessage)
	case ExplainTimeout:
		return withDetail(
			"The provider did not answer in time ("+httpLabel(e.Status)+") — the request can be retried.",
			e.ProviderMessage)
	case ExplainProviderDown:
		return withDetail(
			"The provider is having trouble on its side ("+httpLabel(e.Status)+") — it should be retried in a moment.",
			e.ProviderMessage)
	default:
		if e.ProviderMessage != "" {
			return e.ProviderMessage + " (" + httpLabel(e.Status) + ")"
		}
		return "The provider rejected the request (" + httpLabel(e.Status) + ")."
	}
}

// englishRateLimit says what ran out and when it comes back, in one sentence.
// The reset is given both as a wait and as a wall-clock time: the wait answers
// "how long do I sit here", the clock time answers "can I come back after
// lunch".
//
// The provider's own sentence is quoted on a line of its own, never spliced
// into this one: providers write in their own language, and a sentence that
// starts in English and finishes in Chinese reads as one claim by the runtime
// rather than as a quotation. It is quoted only for a throttle, where the
// runtime has nothing more specific to say than the provider did; once the
// allowance and its reset are known, the sentence states them outright and
// repeating the original adds nothing.
func (e ErrorExplanation) englishRateLimit() string {
	quota := e.Code == ExplainRateLimitQuota
	sentence := "The provider is rate-limiting this account"
	switch {
	case quota && e.Plan != "":
		sentence = "Usage limit reached on your " + e.Plan + " plan"
	case quota:
		sentence = "Usage limit reached for this account"
	}

	switch {
	case !e.ResetAt.IsZero():
		sentence += " — available again in " + formatWait(e.RetryAfter) + ", at " + formatResetTime(e.ResetAt) + "."
	case e.RetryAfter > 0:
		sentence += " — available again in " + formatWait(e.RetryAfter) + "."
	default:
		sentence += " — no reset time given."
	}

	if quota {
		return sentence
	}
	return withDetail(sentence, e.ProviderMessage)
}

// providerProse recovers the sentence a provider wrote for a human. The
// message field of a normalized provider error carries the original body, so
// the readable text is often nested a level or two inside it.
//
// A body with no sentence in it yields nothing. A gateway or a CDN answers a
// throttle with an error page, and markup is not prose: quoting it would put
// the transport dump back on the screen this package exists to keep it off.
func providerProse(apiErr *APIError) string {
	raw := strings.TrimSpace(apiErr.Message)
	if raw == "" || looksLikeMarkup(raw) {
		return ""
	}
	if !looksLikeJSONDocument(raw) {
		return truncateProse(raw)
	}
	var fields rateLimitFields
	collectRateLimitFields([]byte(raw), maxRateLimitPayloadDepth, &fields)
	return truncateProse(fields.message)
}

func withDetail(headline, detail string) string {
	if detail == "" {
		return headline
	}
	return headline + "\nThe provider said: " + detail
}

func httpLabel(status int) string {
	label := http.StatusText(status)
	if label == "" {
		return "HTTP " + strconv.Itoa(status)
	}
	return "HTTP " + strconv.Itoa(status) + " " + label
}

// formatWait renders a duration the way someone deciding whether to wait reads
// it: coarse, and never with more precision than the number deserves.
func formatWait(d time.Duration) string {
	d = d.Round(time.Second)
	if d <= 0 {
		return "less than a minute"
	}
	switch {
	case d < time.Minute:
		return strconv.Itoa(int(d/time.Second)) + "s"
	case d < time.Hour:
		minutes := int(d / time.Minute)
		if seconds := int((d % time.Minute) / time.Second); seconds > 0 {
			return fmt.Sprintf("%dm %ds", minutes, seconds)
		}
		return fmt.Sprintf("%dm", minutes)
	default:
		hours := int(d / time.Hour)
		if minutes := int((d % time.Hour) / time.Minute); minutes > 0 {
			return fmt.Sprintf("%dh %dm", hours, minutes)
		}
		return fmt.Sprintf("%dh", hours)
	}
}

func formatResetTime(t time.Time) string {
	return t.Local().Format("2006-01-02 15:04 MST")
}

// looksLikeMarkup reports whether text is an HTML or XML document rather than
// something a person wrote.
func looksLikeMarkup(text string) bool {
	return strings.HasPrefix(strings.TrimSpace(text), "<")
}

// truncateProse caps how much provider text a surface will show. The cap is in
// bytes, so the cut is pulled back off a partial rune: a sentence in a script
// that does not use spaces has no word boundary to fall back on, and half a
// rune reaches the terminal as a replacement character.
func truncateProse(text string) string {
	text = strings.TrimSpace(text)
	if len(text) <= maxProviderProse {
		return text
	}
	cut := text[:maxProviderProse]
	if idx := strings.LastIndexAny(cut, " \n\t"); idx > maxProviderProse/2 {
		cut = cut[:idx]
	}
	return strings.TrimSpace(strings.ToValidUTF8(cut, "")) + "…"
}
