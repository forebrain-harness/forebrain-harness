// Retry and rate-limiting middleware, and the reasoning carry: the formats a
// reply's reasoning is stored in, who may be given it back, and the fence that
// stands in for a reply that was only reasoning.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand"
	"strings"
	"sync/atomic"
	"time"
)

const (
	defaultInitialBackoff = 500 * time.Millisecond
	defaultMaxBackoff     = 30 * time.Second
)

// RetryOption configures a Retry middleware.
type RetryOption func(*retryConfig)

// retryConfig holds the configuration for the Retry middleware.
type retryConfig struct {
	maxAttempts    int
	initialBackoff time.Duration
	maxBackoff     time.Duration
	// upwardJitter replaces the default ±25% jitter with an upward-only one:
	// each wait is the back-off step plus 0–25% of it, so a wait never
	// undershoots the step it was computed from.
	upwardJitter bool
	shouldRetry  func(error) bool
	// retryAfter reads the wait the server asked for off an error, and
	// maxRetryAfter is the longest such wait this loop will honour: each
	// wait becomes at least the server's, and a request for longer than the
	// limit ends the retries and returns the error instead of parking the
	// turn.
	retryAfter    func(error) time.Duration
	maxRetryAfter time.Duration
	// notify, when set, is called before every retry sleep and once more
	// (Cleared=true) when that retry is over: the retried request started
	// producing output, or the retry loop returned. It is how the
	// network-retry middleware surfaces discard/retry cards; retry chains
	// that keep no visible state (blank-output retries) leave it unset.
	notify func(ctx context.Context, tr TransportRetry)
	// resendGuard refuses a retry the request's stream progress says would
	// duplicate what the user already has (a finished output block, a
	// dispatched tool), and resets that progress before each retry it does
	// make. Layers whose resend re-asks the same question set it.
	resendGuard bool
}

// WithShouldRetry replaces the default retry predicate. By default every
// non-nil error triggers a retry.
func WithShouldRetry(fn func(error) bool) RetryOption {
	return func(c *retryConfig) { c.shouldRetry = fn }
}

// WithMaxBackoff caps each back-off step before jitter is added.
func WithMaxBackoff(d time.Duration) RetryOption {
	return func(c *retryConfig) { c.maxBackoff = d }
}

// WithUpwardJitter replaces the default ±25% jitter with an upward-only one:
// each wait is the back-off step plus 0–25% of it.
func WithUpwardJitter() RetryOption {
	return func(c *retryConfig) { c.upwardJitter = true }
}

// WithRetryAfter makes each wait at least the server-requested one; a request
// for longer than limit ends the retries and returns the error.
func WithRetryAfter(of func(error) time.Duration, limit time.Duration) RetryOption {
	return func(c *retryConfig) { c.retryAfter = of; c.maxRetryAfter = limit }
}

// WithRetryNotify registers a callback fired before each retry sleep, with the
// 1-based retry number, the retry cap, the wait ahead and the error that
// caused it; and once more with Cleared=true when that retry is over — the
// retried request's first output, or the loop's return. Errors the predicate
// rejected never produce a notification.
func WithRetryNotify(fn func(ctx context.Context, tr TransportRetry)) RetryOption {
	return func(c *retryConfig) { c.notify = fn }
}

// WithResendGuard makes the retry loop ask the request's stream progress
// before re-sending: once the request finished an output block or dispatched
// a tool, the error is returned instead of retried, and every retry it does
// make starts from fresh progress.
func WithResendGuard() RetryOption {
	return func(c *retryConfig) { c.resendGuard = true }
}

// waitFor computes the wait before the next attempt from the back-off step
// and the error that caused the retry: jitter on the step, and the server's
// own Retry-After when it asked for more — never an unbounded one, which
// ends the retries instead of parking the turn.
func (c *retryConfig) waitFor(backoff time.Duration, err error) (sleep time.Duration, giveUp bool) {
	sleep = backoff
	if c.upwardJitter {
		// 0–25% on top of the step: a wait never undershoots it.
		if quarter := int64(backoff) / 4; quarter > 0 {
			sleep += time.Duration(rand.Int63n(quarter + 1)) //nolint:gosec
		}
	} else if half := int64(backoff) / 2; half > 0 {
		jitter := time.Duration(rand.Int63n(half)) - backoff/4 //nolint:gosec
		sleep += jitter
		if sleep < 0 {
			sleep = 0
		}
	}
	// The server's own wait wins whenever it asked for more than the
	// back-off computed — but never an unbounded one: past the limit the
	// provider is not asking for patience, it is saying not now, and the
	// caller gets the error instead of a parked turn.
	if c.retryAfter != nil {
		if ra := c.retryAfter(err); ra > 0 {
			if c.maxRetryAfter > 0 && ra > c.maxRetryAfter {
				return 0, true
			}
			if ra > sleep {
				sleep = ra
			}
		}
	}
	return sleep, false
}

// NewRetry returns an LLMMiddleware that automatically retries failed
// Execute calls up to maxAttempts times using exponential back-off with jitter.
//
// Context cancellation is honoured between retries: if ctx is cancelled while
// sleeping before a retry, the middleware returns immediately.
//
//	base := someLLM()                       // the LLM being wrapped
//	mw, err := NewRetry(3, WithMaxBackoff(8*time.Second))
//	if err != nil {
//		return err
//	}
//	client := Use(base, mw)                 // every Execute now retries
//
// The first wait is fixed at 500ms and doubles per attempt (capped by
// WithMaxBackoff); it is deliberately not configurable — an option nothing
// sets is an option nobody tests. WithRetryNotify reports each wait to the
// caller, which is how the surfaces draw the retry line.
func NewRetry(maxAttempts int, opts ...RetryOption) (LLMMiddleware, error) {
	if maxAttempts < 1 {
		return nil, ErrInvalidMaxAttempts
	}

	cfg := &retryConfig{
		maxAttempts:    maxAttempts,
		initialBackoff: defaultInitialBackoff,
		maxBackoff:     defaultMaxBackoff,
		shouldRetry:    func(error) bool { return true },
	}
	for _, o := range opts {
		o(cfg)
	}

	return func(next LLM) LLM {
		return &retryLLM{inner: next, cfg: cfg}
	}, nil
}

// retryLLM is the concrete LLM produced by the Retry middleware.
type retryLLM struct {
	inner LLM
	cfg   *retryConfig
}

// Execute calls inner.Execute up to maxAttempts times with exponential
// back-off and jitter between attempts. A retry re-sends the original request
// unchanged — the dead attempt's half-streamed output is never continued,
// its holders are told to discard it through the notification. With notify
// set, one Cleared notification fires per retry: when the retried request
// first produces a non-empty delta, or when the loop returns (success,
// exhaustion, cancellation), whichever comes first.
func (r *retryLLM) Execute(ctx context.Context, messages []Message, tools []*Tool) (*Result, error) {
	var lastErr error
	backoff := r.cfg.initialBackoff
	maxRetries := r.cfg.maxAttempts - 1
	// A retried attempt's tokens are still spent: the failure returns carry
	// every attempt's usage and the physical attempt count out to the
	// accounting layers.
	acct := attemptAccounting{}

	// Notifications ride the caller's own context: the callback must never
	// observe the wrapped sink installed below.
	notifyCtx := ctx
	var pendingRetry atomic.Int64 // retry number awaiting its Cleared notice, 0 = none
	clearPending := func() {
		if n := pendingRetry.Swap(0); n > 0 {
			r.cfg.notify(notifyCtx, TransportRetry{Retry: int(n), Total: maxRetries, Cleared: true})
		}
	}
	if r.cfg.notify != nil {
		// Every return path ends the retry in flight, if one is: the defer
		// covers success, a rejected error, exhaustion and cancellation.
		defer clearPending()
		// The retry is over the moment the retried request produces anything:
		// the first non-empty delta — or the response start, which a reply
		// made only of tool calls fires without any delta — clears the pending
		// notice early, so a surface waiting on it recovers at the first
		// token, not at the response's end. Only callbacks that were non-nil
		// are wrapped — a muted context holds nil OnDelta/OnReasoningDelta and
		// keeps them.
		if sink := StreamSinkFrom(ctx); sink != nil {
			wrapped := *sink
			if down := wrapped.OnResponseStarted; down != nil {
				wrapped.OnResponseStarted = func() {
					clearPending()
					down()
				}
			}
			if down := wrapped.OnDelta; down != nil {
				wrapped.OnDelta = func(text string) {
					if text != "" {
						clearPending()
					}
					down(text)
				}
			}
			if down := wrapped.OnReasoningDelta; down != nil {
				wrapped.OnReasoningDelta = func(text string) {
					if text != "" {
						clearPending()
					}
					down(text)
				}
			}
			ctx = WithStreamSink(ctx, &wrapped)
		}
	}

	for attempt := 0; attempt < r.cfg.maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, acct.fail(err)
		}

		acct.attempts++
		result, err := r.inner.Execute(ctx, messages, tools)
		if err == nil {
			// The settled attempt's own usage stays as Result.Usage; what
			// the caller learns here is how many physical requests it took.
			result.Attempts = acct.attempts
			return result, nil
		}
		acct.note(err)
		lastErr = err

		if r.cfg.resendGuard && !ResendSafe(ctx) {
			// Re-sending would duplicate what the user already has; the
			// caller owns finishing from what completed.
			return nil, acct.fail(err)
		}
		if !r.cfg.shouldRetry(err) {
			return nil, acct.fail(err)
		}

		if attempt == r.cfg.maxAttempts-1 {
			break
		}

		sleep, giveUp := r.cfg.waitFor(backoff, err)
		if giveUp {
			return nil, acct.fail(err)
		}

		if r.cfg.notify != nil {
			r.cfg.notify(notifyCtx, TransportRetry{
				Retry:        attempt + 1,
				Total:        maxRetries,
				Wait:         sleep,
				Err:          err,
				Status:       HTTPStatusOf(err),
				NonStreaming: NonStreamingRequestFrom(notifyCtx),
			})
			pendingRetry.Store(int64(attempt + 1))
		}

		select {
		case <-ctx.Done():
			return nil, acct.fail(ctx.Err())
		case <-time.After(sleep):
		}

		if r.cfg.resendGuard {
			// The next attempt is a fresh one: what the dead attempt
			// produced must not decide anything about it.
			ResetStreamProgress(ctx)
		}

		backoff *= 2
		if backoff > r.cfg.maxBackoff {
			backoff = r.cfg.maxBackoff
		}
	}

	// The exhaustion return is the final dead attempt's withdraw point. Its
	// own first token already consumed the pending notice, and every holder
	// of the text it streamed learns of the death only here — so when the
	// pending is spent, the catch-up Cleared below is the only announcement
	// that final attempt ever gets. clearPending covers the other shape, a
	// final attempt that died before producing a single byte; both paths
	// together guarantee exactly one Cleared per exhaustion.
	if r.cfg.notify != nil && maxRetries > 0 {
		spent := pendingRetry.Load() == 0
		clearPending()
		if spent {
			r.cfg.notify(notifyCtx, TransportRetry{Retry: maxRetries, Total: maxRetries, Cleared: true})
		}
	}
	return nil, &MaxAttemptsExceededError{Attempts: r.cfg.maxAttempts, Err: acct.fail(lastErr)}
}

const Prefix = "!!FOREBRAIN_RCv1!!"

const payloadVersion = "openai_responses_reasoning_v1"

type Item struct {
	ID               string   `json:"id,omitempty"`
	Summary          []string `json:"summary,omitempty"`
	EncryptedContent string   `json:"encrypted_content,omitempty"`
}

type Payload struct {
	Version string `json:"version"`
	Items   []Item `json:"items"`
}

func Encode(items []Item) string {
	items = normalizeItems(items)
	if len(items) == 0 {
		return ""
	}
	data, err := json.Marshal(Payload{
		Version: payloadVersion,
		Items:   items,
	})
	if err != nil {
		return Prefix + SummaryText(items)
	}
	return Prefix + string(data)
}

func Decode(name string) (Payload, bool) {
	if !strings.HasPrefix(name, Prefix) {
		return Payload{}, false
	}
	raw := strings.TrimPrefix(name, Prefix)
	if raw == "" {
		return Payload{}, false
	}
	var payload Payload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return Payload{}, false
	}
	if payload.Version != payloadVersion {
		return Payload{}, false
	}
	payload.Items = normalizeItems(payload.Items)
	if len(payload.Items) == 0 {
		return Payload{}, false
	}
	return payload, true
}

// thinkingPayloadVersion marks a carry holding Anthropic-protocol thinking
// blocks. It is its own payload rather than an Item list: a thinking block's
// text and signature must go back byte for byte, and the Responses items'
// normalization trims the fields an Item would hold them in.
const thinkingPayloadVersion = "anthropic_thinking_v1"

// ThinkingBlock is one thinking or redacted_thinking block of an
// Anthropic-protocol response, exactly as the model returned it. Nothing in it
// is ever trimmed or rewritten: the provider verifies the block against its
// signature, and a changed byte is a rejected request.
type ThinkingBlock struct {
	// Redacted marks a redacted_thinking block, whose whole content is Data.
	Redacted  bool   `json:"redacted,omitempty"`
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`
	Data      string `json:"data,omitempty"`
}

type thinkingPayload struct {
	Version string          `json:"version"`
	Blocks  []ThinkingBlock `json:"blocks"`
}

// EncodeThinking packs blocks into a carry, keeping their order. It returns ""
// when no block carries anything, and never falls back to plain text: a plain
// carry is what the OpenAI-compatible client replays as reasoning_content.
func EncodeThinking(blocks []ThinkingBlock) string {
	kept := make([]ThinkingBlock, 0, len(blocks))
	for _, b := range blocks {
		if b.Redacted {
			if b.Data != "" {
				kept = append(kept, ThinkingBlock{Redacted: true, Data: b.Data})
			}
			continue
		}
		if b.Thinking != "" || b.Signature != "" {
			kept = append(kept, ThinkingBlock{Thinking: b.Thinking, Signature: b.Signature})
		}
	}
	if len(kept) == 0 {
		return ""
	}
	data, err := json.Marshal(thinkingPayload{Version: thinkingPayloadVersion, Blocks: kept})
	if err != nil {
		return ""
	}
	return Prefix + string(data)
}

// DecodeThinking reads the blocks EncodeThinking packed. It reports false for
// every other carry, Responses payloads and plain reasoning text included.
func DecodeThinking(name string) ([]ThinkingBlock, bool) {
	if !strings.HasPrefix(name, Prefix) {
		return nil, false
	}
	var payload thinkingPayload
	if err := json.Unmarshal([]byte(strings.TrimPrefix(name, Prefix)), &payload); err != nil {
		return nil, false
	}
	if payload.Version != thinkingPayloadVersion || len(payload.Blocks) == 0 {
		return nil, false
	}
	return payload.Blocks, true
}

// geminiPartsPayloadVersion marks a carry holding a Gemini response's parts
// as the model returned them: their order, the thought-summary parts, and the
// thoughtSignature each part carried. Gemini has no thinking blocks of its
// own; its reasoning rides those signatures, and the API wants every part
// back exactly as it came — the signature in its original part.
const geminiPartsPayloadVersion = "gemini_parts_v1"

// GeminiPart is one part of a Gemini response, as replayed to the model that
// produced it.
type GeminiPart struct {
	// Kind is "text" (answer text, possibly empty when the part only
	// carried a signature), "thought" (a thought summary) or "call".
	Kind string `json:"kind"`
	// Text is the part's text, byte for byte; empty for a call.
	Text string `json:"text,omitempty"`
	// Call indexes the message's ToolCalls for a "call" part.
	Call int `json:"call,omitempty"`
	// Signature is the part's thoughtSignature, byte for byte.
	Signature string `json:"signature,omitempty"`
}

type geminiPartsPayload struct {
	Version string       `json:"version"`
	Parts   []GeminiPart `json:"parts"`
}

// EncodeGeminiParts packs parts into a carry, keeping their order. It returns
// "" when parts is empty. It never falls back to plain text: a plain carry is
// reasoning text, not a part layout, and another protocol would replay it as
// reasoning_content.
func EncodeGeminiParts(parts []GeminiPart) string {
	if len(parts) == 0 {
		return ""
	}
	data, err := json.Marshal(geminiPartsPayload{Version: geminiPartsPayloadVersion, Parts: parts})
	if err != nil {
		return ""
	}
	return Prefix + string(data)
}

// DecodeGeminiParts reads what EncodeGeminiParts packed. It reports false for
// every other carry, Responses payloads, thinking blocks and plain reasoning
// text included.
func DecodeGeminiParts(name string) ([]GeminiPart, bool) {
	if !strings.HasPrefix(name, Prefix) {
		return nil, false
	}
	var payload geminiPartsPayload
	if err := json.Unmarshal([]byte(strings.TrimPrefix(name, Prefix)), &payload); err != nil {
		return nil, false
	}
	if payload.Version != geminiPartsPayloadVersion || len(payload.Parts) == 0 {
		return nil, false
	}
	return payload.Parts, true
}

// StructuredCarry reports whether name holds a protocol's structured reasoning
// payload — Responses items, Anthropic thinking blocks, Gemini part layouts or
// an OpenAI-compatible replay field — rather than plain reasoning text. Only
// plain text is something every protocol can replay.
func StructuredCarry(name string) bool {
	if _, ok := Decode(name); ok {
		return true
	}
	if _, ok := DecodeThinking(name); ok {
		return true
	}
	if _, ok := DecodeGeminiParts(name); ok {
		return true
	}
	_, ok := decodeCompatReasoningPayload(name)
	return ok
}

// ReplaysReasoningTo reports whether the reasoning m carries may be sent back
// to model: m carries some, and either the model that produced it is the one
// being asked or it was recorded without a model. The rule depends on the
// message and the request's model alone, so a conversation renders the same
// bytes on every request to one model and its cached prefix holds.
func ReplaysReasoningTo(m Message, model string) bool {
	if !strings.HasPrefix(m.Name, Prefix) {
		return false
	}
	producer := strings.TrimSpace(m.ReasoningModel)
	return producer == "" || strings.EqualFold(producer, strings.TrimSpace(model))
}

// CompatReasoningField is the field name OpenAI-compatible providers return
// reasoning under by default, and the one go-openai's message struct serializes
// it to. A provider that names the field something else gets its reasoning back
// under that name — see EncodeCompatReasoning.
const CompatReasoningField = "reasoning_content"

// compatReasoningPayloadVersion marks the reasoning of an OpenAI-compatible
// reply whose provider named the field something other than reasoning_content.
// The common case stays a plain carry, byte for byte as before; only another
// field name needs recording, so the reasoning can be replayed under the name
// the provider reads.
const compatReasoningPayloadVersion = "openai_compat_reasoning_v1"

// compatReasoningPayload is one reply's reasoning for a provider that reads it
// under a field of its own.
type compatReasoningPayload struct {
	Version string `json:"version"`
	Field   string `json:"field"`
	Text    string `json:"text"`
}

// EncodeCompatReasoning returns the carry for reasoning an OpenAI-compatible
// provider returned under field: a plain carry for reasoning_content (or ""
// field), a payload naming the field otherwise; "" when text is empty.
//
// The plain carry is what every provider but a few gets, and it is exactly the
// bytes this protocol always wrote — the prefix followed by the reasoning text.
// The payload exists only for a provider that names its field something else:
// go-openai serializes reasoning_content and nothing else, so that name has to
// be recorded here to be replayed under the one the provider reads.
func EncodeCompatReasoning(field, text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	field = strings.TrimSpace(field)
	if field == "" || field == CompatReasoningField {
		return Prefix + text
	}
	data, err := json.Marshal(compatReasoningPayload{Version: compatReasoningPayloadVersion, Field: field, Text: text})
	if err != nil {
		return ""
	}
	return Prefix + string(data)
}

// DecodeCompatReasoning reads an OpenAI-compatible reasoning carry: the payload
// above, or a plain carry, which is reasoning_content. It reports false for
// every other carry — another protocol's blocks are not reasoning this model
// ever said, and a participant name is not a carry at all.
func DecodeCompatReasoning(name string) (field, text string, ok bool) {
	if !strings.HasPrefix(name, Prefix) {
		return "", "", false
	}
	if payload, ok := decodeCompatReasoningPayload(name); ok {
		return payload.Field, payload.Text, true
	}
	// Plain text is the one shape no protocol claims: each of the others writes
	// a payload and checks its own version, so a carry none of them reads as
	// its own is reasoning text written without a field name.
	if _, ok := Decode(name); ok {
		return "", "", false
	}
	if _, ok := DecodeThinking(name); ok {
		return "", "", false
	}
	if _, ok := DecodeGeminiParts(name); ok {
		return "", "", false
	}
	return CompatReasoningField, strings.TrimPrefix(name, Prefix), true
}

// decodeCompatReasoningPayload reads the payload EncodeCompatReasoning writes
// for a provider that names its reasoning field. It reports false for the plain
// carry and for every other protocol's payload.
func decodeCompatReasoningPayload(name string) (compatReasoningPayload, bool) {
	if !strings.HasPrefix(name, Prefix) {
		return compatReasoningPayload{}, false
	}
	var payload compatReasoningPayload
	if err := json.Unmarshal([]byte(strings.TrimPrefix(name, Prefix)), &payload); err != nil {
		return compatReasoningPayload{}, false
	}
	if payload.Version != compatReasoningPayloadVersion || strings.TrimSpace(payload.Field) == "" {
		return compatReasoningPayload{}, false
	}
	return payload, true
}

// CarryEstimateText is the part of a Name that a request can actually carry,
// for estimating its size: a carry's replayable content without its JSON
// envelope, and without answer text a Gemini layout repeats from Parts. A Name
// that is not a carry is returned as it is.
//
// The estimate it feeds decides when a conversation is compacted, so counting
// the envelope and the repeated text read the prompt high and compacted a turn
// early: neither byte is ever sent.
func CarryEstimateText(name string) string {
	if !strings.HasPrefix(name, Prefix) {
		return name
	}
	if payload, ok := Decode(name); ok {
		kept := make([]string, 0, len(payload.Items)*2)
		for _, item := range payload.Items {
			kept = append(kept, item.Summary...)
			if item.EncryptedContent != "" {
				kept = append(kept, item.EncryptedContent)
			}
		}
		return strings.Join(kept, "\n")
	}
	if blocks, ok := DecodeThinking(name); ok {
		kept := make([]string, 0, len(blocks)*2)
		for _, b := range blocks {
			if b.Redacted {
				if b.Data != "" {
					kept = append(kept, b.Data)
				}
				continue
			}
			if b.Thinking != "" {
				kept = append(kept, b.Thinking)
			}
			if b.Signature != "" {
				kept = append(kept, b.Signature)
			}
		}
		return strings.Join(kept, "\n")
	}
	if parts, ok := DecodeGeminiParts(name); ok {
		// A text part's body is the answer text Parts already holds, and the
		// replay sends that body from one place or the other, never both. The
		// thought summaries and the signatures are what the carry adds.
		kept := make([]string, 0, len(parts)*2)
		for _, p := range parts {
			if p.Kind == "thought" && p.Text != "" {
				kept = append(kept, p.Text)
			}
			if p.Signature != "" {
				kept = append(kept, p.Signature)
			}
		}
		return strings.Join(kept, "\n")
	}
	if payload, ok := decodeCompatReasoningPayload(name); ok {
		return payload.Text
	}
	return strings.TrimPrefix(name, Prefix)
}

func ExtractSummary(name string) string {
	if !strings.HasPrefix(name, Prefix) {
		return ""
	}
	if payload, ok := Decode(name); ok {
		return SummaryText(payload.Items)
	}
	if blocks, ok := DecodeThinking(name); ok {
		// A redacted block's data is ciphertext, not something to show; the
		// thinking text of the rest is. Falling through to the plain-text
		// return below would hand a caller this payload's JSON as reasoning.
		texts := make([]string, 0, len(blocks))
		for _, b := range blocks {
			if b.Redacted || b.Thinking == "" {
				continue
			}
			texts = append(texts, b.Thinking)
		}
		return strings.Join(texts, "\n")
	}
	if parts, ok := DecodeGeminiParts(name); ok {
		// A Gemini thought summary is the reasoning the person may see; the
		// signatures on any part are ciphertext, and the payload's own JSON is
		// never text. Falling through to the plain-text return below would
		// hand a caller that JSON as the model's thinking.
		texts := make([]string, 0, len(parts))
		for _, p := range parts {
			if p.Kind != "thought" || p.Text == "" {
				continue
			}
			texts = append(texts, p.Text)
		}
		return strings.Join(texts, "\n")
	}
	if payload, ok := decodeCompatReasoningPayload(name); ok {
		// The compatible provider's own field name is wire detail; the
		// reasoning text beside it is what a surface shows.
		return payload.Text
	}
	return strings.TrimPrefix(name, Prefix)
}

// thinkingFenceOpen and thinkingFenceClose bracket the block a client shows
// when a reply held reasoning and nothing else.
const (
	thinkingFenceOpen  = "```thinking\n"
	thinkingFenceClose = "\n```"
)

// ThinkingFence wraps reasoning as the ```thinking block a client puts in a
// reply's text when the model returned reasoning and nothing else, so the
// person sees the thinking instead of an empty answer. The block is a display
// form of the reasoning, never an answer the model wrote.
func ThinkingFence(reasoning string) string {
	return thinkingFenceOpen + reasoning + thinkingFenceClose
}

// IsThinkingFence reports text that is such a block.
func IsThinkingFence(text string) bool {
	return strings.HasPrefix(text, thinkingFenceOpen) && strings.HasSuffix(text, thinkingFenceClose)
}

// ThinkingFenceOnly reports an assistant message that is a thinking fence and
// nothing else: no tool calls, no non-text parts, and every text part — at
// least one — a fence. It is a reply in which the model only reasoned.
func ThinkingFenceOnly(m Message) bool {
	if m.Role != RoleAssistant || len(m.ToolCalls) > 0 || len(m.Parts) == 0 {
		return false
	}
	for _, p := range m.Parts {
		if p.Type != ContentTypeText || !IsThinkingFence(p.Text) {
			return false
		}
	}
	return true
}

// ThinkingFenceGoesTo reports whether a ThinkingFenceOnly message may be sent
// to model. Only the model recorded as having produced its reasoning gets it:
// to any other model the fence would pass that reasoning off as an answer
// this conversation gave. A message that records no producer — every such
// message stored before producers were kept — goes to no model.
func ThinkingFenceGoesTo(m Message, model string) bool {
	producer := strings.TrimSpace(m.ReasoningModel)
	to := strings.TrimSpace(model)
	return producer != "" && to != "" && strings.EqualFold(producer, to)
}

// ReasoningReplayRefused reports a request the provider refused because of
// the reasoning it replayed: an APIError whose status is one of statuses and
// whose message, code or type names one of keywords (case-insensitive). The
// adapter that sent the reasoning decides the statuses and keywords, and
// resends once without it — a refusal that otherwise fails every request of
// the conversation from here on, since the same reasoning is replayed on every
// one of them.
//
// The status and the keyword both have to match: a provider refuses a hundred
// things with a 400, and resending each of them without the reasoning would
// spend a request on a failure that has nothing to do with it.
func ReasoningReplayRefused(err error, statuses []int, keywords ...string) bool {
	if err == nil {
		return false
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr == nil {
		return false
	}
	refused := false
	for _, status := range statuses {
		if apiErr.StatusCode == status {
			refused = true
			break
		}
	}
	if !refused {
		return false
	}
	for _, text := range []string{apiErr.Message, apiErr.Code, apiErr.Type} {
		if text == "" {
			continue
		}
		lower := strings.ToLower(text)
		for _, keyword := range keywords {
			if keyword != "" && strings.Contains(lower, strings.ToLower(keyword)) {
				return true
			}
		}
	}
	return false
}

// WithoutReasoningCarries returns a copy of messages in which no message
// carries reasoning for the model: every Prefix carry in Name is cleared.
// ReasoningModel is kept — it records who produced the message, which rules
// other than reasoning replay still read.
//
// It is the resend a provider that refused the replayed reasoning gets: the
// conversation without the reasoning, not without the turn that held it.
func WithoutReasoningCarries(messages []Message) []Message {
	out := make([]Message, len(messages))
	copy(out, messages)
	for i := range out {
		if strings.HasPrefix(out[i].Name, Prefix) {
			out[i].Name = ""
		}
	}
	return out
}

func SummaryText(items []Item) string {
	items = normalizeItems(items)
	if len(items) == 0 {
		return ""
	}
	blocks := make([]string, 0, len(items))
	for _, item := range items {
		partLines := make([]string, 0, len(item.Summary))
		for _, part := range item.Summary {
			if part == "" {
				continue
			}
			partLines = append(partLines, part)
		}
		if len(partLines) == 0 {
			continue
		}
		blocks = append(blocks, strings.Join(partLines, "\n"))
	}
	return strings.Join(blocks, "\n")
}

func normalizeItems(items []Item) []Item {
	if len(items) == 0 {
		return nil
	}
	out := make([]Item, 0, len(items))
	for _, item := range items {
		summary := make([]string, 0, len(item.Summary))
		for _, part := range item.Summary {
			if part == "" {
				continue
			}
			summary = append(summary, part)
		}
		if len(summary) == 0 && strings.TrimSpace(item.EncryptedContent) == "" {
			continue
		}
		out = append(out, Item{
			ID:               strings.TrimSpace(item.ID),
			Summary:          summary,
			EncryptedContent: strings.TrimSpace(item.EncryptedContent),
		})
	}
	return out
}
