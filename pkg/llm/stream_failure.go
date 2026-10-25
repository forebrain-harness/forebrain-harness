// Stream-failure classification and the resend/finish decision table,
// transplanted line by line from Claude Code's stream failure handling.
package llm

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type StreamFailureCause string

const (
	CauseOverloaded     StreamFailureCause = "overloaded"
	CauseServerError    StreamFailureCause = "server_error"
	CauseTimedOut       StreamFailureCause = "timed_out"
	CauseStalled        StreamFailureCause = "stalled"
	CauseTruncated      StreamFailureCause = "truncated"
	CauseSuspended      StreamFailureCause = "suspended"
	CauseConnectionLost StreamFailureCause = "connection_lost"
	CauseMalformed      StreamFailureCause = "malformed"
	CauseBadRequest     StreamFailureCause = "bad_request"
	CauseUnknown        StreamFailureCause = "unknown"
)

// suspendThreshold is how far the wall clock must outrun the monotonic one
// during an attempt before a dropped connection is put down to sleep.
const suspendThreshold = 5 * time.Second

func ClassifyStreamFailure(err error, snap StreamProgressSnapshot) StreamFailureCause {
	var aerr *APIError
	isAPI := errors.As(err, &aerr) && aerr != nil
	typ := ""
	if isAPI {
		typ = strings.ToLower(strings.TrimSpace(aerr.Type))
	}
	switch {
	case isAPI && (aerr.StatusCode == 529 || typ == "overloaded_error"):
		return CauseOverloaded
	case isAPI && (typ == "timeout_error" || typ == "deadline_exceeded"):
		return CauseTimedOut
	case isAPI && (aerr.StatusCode >= 500 || typ == "api_error" || typ == "server_error" || typ == "internal" || typ == "unavailable"):
		return CauseServerError
	case errors.Is(err, ErrStreamStalled):
		return CauseStalled
	case isTransportFailure(err):
		if errors.Is(err, ErrStreamTruncated) {
			return CauseTruncated
		}
		if snap.Slept >= suspendThreshold {
			return CauseSuspended
		}
		return CauseConnectionLost
	}
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &syntaxErr) || errors.As(err, &typeErr) {
		return CauseMalformed
	}
	if isAPI {
		return CauseBadRequest
	}
	return CauseUnknown
}

type StreamDecision int

const (
	DecideFail StreamDecision = iota
	DecideRetry
	DecideRetryWithoutStreaming
	DecideKeepPartial
)

type streamRetryCounts struct {
	retries, sleptThrough, overloaded, stalls, truncations, afterThinkingOnly int
	triedWithoutStreaming                                                     bool
}

type streamRetryPolicy struct {
	maxRetries        int  // network_retry_max
	maxSleptThrough   int  // min(maxRetries, 10)
	maxOverloaded     int  // 3
	allowNonStreaming bool // true
}

type streamDecision struct {
	d StreamDecision
	c streamRetryCounts
}

func failWith(c streamRetryCounts) streamDecision { return streamDecision{DecideFail, c} }
func keepWith(c streamRetryCounts) streamDecision { return streamDecision{DecideKeepPartial, c} }

// retryOr spends one retry from the shared budget, or falls back.
func retryOr(c streamRetryCounts, p streamRetryPolicy, fallback streamDecision) streamDecision {
	if c.retries < p.maxRetries {
		c.retries++
		return streamDecision{DecideRetry, c}
	}
	return fallback
}

type retryCounter int

const (
	counterStalls retryCounter = iota
	counterTruncations
	counterAfterThinkingOnly
)

func (c streamRetryCounts) get(k retryCounter) int {
	switch k {
	case counterStalls:
		return c.stalls
	case counterTruncations:
		return c.truncations
	default:
		return c.afterThinkingOnly
	}
}

func (c streamRetryCounts) inc(k retryCounter) streamRetryCounts {
	switch k {
	case counterStalls:
		c.stalls++
	case counterTruncations:
		c.truncations++
	default:
		c.afterThinkingOnly++
	}
	return c
}

// limitedRetry retries while this kind of failure has happened fewer than
// limit times (and the shared budget allows), then falls back.
func limitedRetry(c streamRetryCounts, k retryCounter, limit int, p streamRetryPolicy, fallback streamDecision) streamDecision {
	if c.get(k) < limit {
		return retryOr(c.inc(k), p, fallback)
	}
	return fallback
}

func withoutStreaming(c streamRetryCounts, p streamRetryPolicy) streamDecision {
	if !p.allowNonStreaming {
		return failWith(c)
	}
	if c.triedWithoutStreaming && c.retries >= p.maxRetries {
		return failWith(c)
	}
	c.retries = min(c.retries+1, p.maxRetries)
	c.triedWithoutStreaming = true
	return streamDecision{DecideRetryWithoutStreaming, c}
}

func overloadedDecision(c streamRetryCounts, p streamRetryPolicy) streamDecision {
	c.overloaded++
	fallback := withoutStreaming(c, p)
	if c.overloaded < p.maxOverloaded {
		return retryOr(c, p, fallback)
	}
	return fallback
}

func sleptThroughRetry(c streamRetryCounts, p streamRetryPolicy, fallback streamDecision) streamDecision {
	if c.sleptThrough < p.maxSleptThrough {
		c.sleptThrough++
		return streamDecision{DecideRetry, c}
	}
	return retryOr(c, p, fallback)
}

// decideStreamFailure is Claude Code's stream-failure table.
func decideStreamFailure(cause StreamFailureCause, snap StreamProgressSnapshot, outlasted bool, c streamRetryCounts, p streamRetryPolicy) streamDecision {
	stop := snap.StopReasonReceived
	q := withoutStreaming(c, p) // the fallback, from the counts on entry
	switch snap.Kind {
	case ProgressOutput:
		if cause == CauseBadRequest || cause == CauseUnknown {
			return failWith(c)
		}
		return keepWith(c)
	case ProgressThinkingOnly:
		switch cause {
		case CauseStalled:
			if stop {
				return keepWith(c)
			}
			return limitedRetry(c, counterStalls, 1, p, keepWith(c))
		case CauseConnectionLost, CauseSuspended, CauseTruncated, CauseMalformed:
			if stop {
				return keepWith(c)
			}
			return limitedRetry(c, counterAfterThinkingOnly, 2, p, keepWith(c))
		case CauseServerError, CauseTimedOut:
			if stop {
				return failWith(c)
			}
			limit := 2
			if cause == CauseTimedOut {
				limit = 1
			}
			return limitedRetry(c, counterAfterThinkingOnly, limit, p, failWith(c))
		case CauseOverloaded:
			if stop {
				return failWith(c)
			}
			return overloadedDecision(c, p)
		default:
			return failWith(c)
		}
	}
	switch cause {
	case CauseOverloaded:
		if snap.Kind == ProgressPartialOutput {
			c.overloaded++
			return withoutStreaming(c, p)
		}
		return overloadedDecision(c, p)
	case CauseStalled:
		if snap.Kind == ProgressNothing {
			return limitedRetry(c, counterStalls, 1, p, q)
		}
		return q
	case CauseTruncated:
		if stop {
			return q
		}
		return limitedRetry(c, counterTruncations, 1, p, q)
	case CauseSuspended:
		if stop {
			return q
		}
		if snap.Kind == ProgressPartialOutput {
			return retryOr(c, p, q)
		}
		return sleptThroughRetry(c, p, q)
	case CauseConnectionLost:
		if stop {
			return q
		}
		return retryOr(c, p, q)
	case CauseServerError:
		if outlasted && !stop && snap.Kind != ProgressPartialOutput {
			return limitedRetry(c, counterAfterThinkingOnly, 2, p, q)
		}
		return q
	default: // timed out, malformed, bad request, unknown
		return q
	}
}

// completeDespiteError: the response had fully arrived before the failure.
func completeDespiteError(cause StreamFailureCause, snap StreamProgressSnapshot) bool {
	if !snap.Complete {
		return false
	}
	switch cause {
	case CauseConnectionLost, CauseSuspended, CauseTruncated, CauseStalled:
		return true
	case CauseOverloaded, CauseServerError, CauseTimedOut:
		return snap.Kind == ProgressOutput
	}
	return false
}

// requestLevelDecision handles a request that failed before it produced
// anything, from its HTTP status. ok is false when the error is not one;
// pass means "return the error untouched": the layers above own it.
func requestLevelDecision(err error, snap StreamProgressSnapshot, c streamRetryCounts, p streamRetryPolicy) (dec streamDecision, pass, ok bool) {
	if snap.Kind != ProgressNothing {
		return streamDecision{}, false, false
	}
	status := statusOf(err)
	// DNS and certificate failures are request-level facts the same way a
	// status is: the request as sent can never be answered.
	if status < 400 && !isNXDOMAIN(err) && !isCertificateFailure(err) {
		return streamDecision{}, false, false
	}
	switch {
	case status == 529:
		// Overload keeps its own recovery layer.
		return streamDecision{}, true, true
	case status == 408 || status == 409 || status >= 500:
		return retryOr(c, p, failWith(c)), false, true
	case status == 404 && !c.triedWithoutStreaming:
		return withoutStreaming(c, p), false, true
	default:
		return streamDecision{}, true, true
	}
}
