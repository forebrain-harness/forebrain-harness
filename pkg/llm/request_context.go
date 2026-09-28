// Per-request context values and provider observability ports.
package llm

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
)

// The execution-scope keys a provider client needs on every request.
//
// These lived in pkg/tool, which put a Layer 2 dependency in the middle of
// every provider client and blocked C10 from extracting them into
// llm/anthropic and llm/openai: those are Layer 0, so they may depend on
// nothing above llm. They belong here on their own merits too — the prompt
// cache key is literally a field of an OpenAI request, and the session id is
// what scopes it.
//
// The two are defined together because they cannot be separated:
// PromptCacheKeyFromContext falls back to the session id, and dropping that
// fallback would silently stop sending a cache key on every path that never
// set one explicitly — a cache-hit-rate regression with no failing test and
// no error, which is the one class of change this project treats as
// unacceptable.
type requestCtxKey string

const (
	ctxKeyAgentSessionID  requestCtxKey = "forebrain_agent_session_id"
	ctxKeyPromptCacheKey  requestCtxKey = "forebrain_prompt_cache_key"
	ctxKeyExternalContext requestCtxKey = "forebrain_external_context_observer"
)

// WithAgentSessionID scopes ctx to one agent session.
func WithAgentSessionID(ctx context.Context, sessionID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, ctxKeyAgentSessionID, strings.TrimSpace(sessionID))
}

// AgentSessionIDFromContext returns the active agent session ID.
func AgentSessionIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(ctxKeyAgentSessionID).(string)
	return v
}

// WithPromptCacheKey pins provider-side prompt caching to a logical root
// session. Subagents deliberately inherit the parent's value even though they
// have distinct transcript/session IDs.
func WithPromptCacheKey(ctx context.Context, key string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, ctxKeyPromptCacheKey, strings.TrimSpace(key))
}

// PromptCacheKeyFromContext returns an explicit root-session key when present,
// otherwise the active agent session ID. The fallback keeps ordinary callers
// cache-aware without requiring every execution seam to be updated.
func PromptCacheKeyFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, _ := ctx.Value(ctxKeyPromptCacheKey).(string); strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return strings.TrimSpace(AgentSessionIDFromContext(ctx))
}

// WithExternalContextObserver installs a callback fired when the execution
// consults external context. A provider client raises it for a built-in
// server-side tool (OpenAI's web_search), which is why it lives here rather
// than in pkg/tool: a Layer 0 provider package cannot reach Layer 2.
func WithExternalContextObserver(ctx context.Context, observer func()) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if observer == nil {
		return ctx
	}
	return context.WithValue(ctx, ctxKeyExternalContext, observer)
}

// NotifyExternalContext fires the observer installed by
// WithExternalContextObserver, if any.
func NotifyExternalContext(ctx context.Context) {
	if ctx == nil {
		return
	}
	if observer, ok := ctx.Value(ctxKeyExternalContext).(func()); ok && observer != nil {
		observer()
	}
}

// fastCtxKey is separate from requestCtxKey so the two cannot collide.
type fastCtxKey struct{}

// WithFast threads the per-turn /fast toggle through context. A surface sets
// it before invoking the runner; the Anthropic client reads it inside Execute
// to send service_tier=auto. Other providers ignore it, having no equivalent
// knob.
//
// It lives here rather than in pkg/agent because it is a property of the LLM
// request, and because a Layer 0 provider package cannot import agent: agent
// already depends on llm, so the edge would close a cycle.
func WithFast(ctx context.Context, enabled bool) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, fastCtxKey{}, enabled)
}

// Fast reports whether the current turn asked for fast mode.
func Fast(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(fastCtxKey{}).(bool)
	return v
}

// Observability ports for provider clients.
//
// A provider client has to wrap its HTTP transport for tracing and emit the
// occasional debug line, but the implementations of both live in pkg/telemetry
// (Layer 1) while the clients are Layer 0. C10 moves those clients into
// llm/anthropic and llm/openai, so the dependency has to be inverted: llm
// declares the ports, and process installs telemetry's implementations at
// startup — the same shape as the other ports process installs there.
//
// Both are stored atomically. They are installed once during process.Open and
// read on every request from whatever goroutine is running a turn, so a plain
// variable would be a data race the moment a second Open happened in the same
// process (which tests do).

var (
	httpTransportWrapper atomic.Pointer[func(http.RoundTripper) http.RoundTripper]
	debugLogger          atomic.Pointer[func(topic, message string)]
)

// SetHTTPTransportWrapper installs the tracing wrapper applied to every
// provider client's transport. Passing nil clears it.
func SetHTTPTransportWrapper(wrap func(http.RoundTripper) http.RoundTripper) {
	if wrap == nil {
		httpTransportWrapper.Store(nil)
		return
	}
	httpTransportWrapper.Store(&wrap)
}

// WrapHTTPTransport applies the installed wrapper, or returns next unchanged
// when none is installed. A nil next means http.DefaultTransport, matching
// what the http package itself does.
func WrapHTTPTransport(next http.RoundTripper) http.RoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}
	if w := httpTransportWrapper.Load(); w != nil {
		return (*w)(next)
	}
	return next
}

// NewHTTPClient builds a provider HTTP client with the installed wrapper and
// no timeout: streaming responses are long-lived, and a client-level timeout
// would cut them off mid-stream.
func NewHTTPClient() *http.Client {
	return &http.Client{Timeout: 0, Transport: WrapHTTPTransport(http.DefaultTransport)}
}

// SetDebugLogger installs the sink for provider debug lines. Passing nil
// clears it, which is the normal state: debug logging is opt-in.
func SetDebugLogger(log func(topic, message string)) {
	if log == nil {
		debugLogger.Store(nil)
		return
	}
	debugLogger.Store(&log)
}

// LogDebug emits one provider debug line if a logger is installed.
func LogDebug(topic, message string) {
	if l := debugLogger.Load(); l != nil {
		(*l)(topic, message)
	}
}
