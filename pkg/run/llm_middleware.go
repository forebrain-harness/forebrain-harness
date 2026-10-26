// LLM middleware chain: retry, fallback, recovery, guardrails, and usage.
package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

const (
	baseRetryDelay          = 500 * time.Millisecond
	persistentMaxRetryDelay = 5 * time.Minute
	foreground529RetryCap   = 2
)

var foregroundRetrySources = map[string]struct{}{
	"repl_main_thread":                         {},
	"repl_main_thread:outputStyle:custom":      {},
	"repl_main_thread:outputStyle:Explanatory": {},
	"repl_main_thread:outputStyle:Learning":    {},
	"sdk":                                      {},
	"agent:custom":                             {},
	"agent:default":                            {},
	"agent:builtin":                            {},
	"hook_agent":                               {},
	"hook_prompt":                              {},
	"verification_agent":                       {},
	"side_question":                            {},
	"auto_mode":                                {},
}

type querySourceKey struct{}

func WithQuerySource(ctx context.Context, source string) context.Context {
	return context.WithValue(ctx, querySourceKey{}, strings.TrimSpace(source))
}

func QuerySourceFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(querySourceKey{}).(string)
	return strings.TrimSpace(v)
}

func ShouldRetryStatus529(querySource string) bool {
	querySource = strings.TrimSpace(querySource)
	if querySource == "" {
		return true
	}
	if strings.HasPrefix(querySource, "agent:builtin:") {
		return true
	}
	_, ok := foregroundRetrySources[querySource]
	return ok
}

func RetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := baseRetryDelay
	for i := 1; i < attempt; i++ {
		delay *= 2
		if delay >= persistentMaxRetryDelay {
			return persistentMaxRetryDelay
		}
	}
	return delay
}

func Foreground529RetryCap() int {
	return foreground529RetryCap
}

func isStatus529(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "529") || strings.Contains(msg, "overloaded")
}

func NewLLMForAgentConfig(cfg *AgentConfigYAML) (llm.LLM, error) {
	if cfg == nil {
		return nil, fmt.Errorf("nil agent config")
	}
	if len(cfg.LLMChain) == 0 {
		if err := prepareAgentLLMChain(cfg); err != nil {
			return nil, err
		}
	}
	if len(cfg.LLMChain) == 0 {
		return nil, fmt.Errorf("no llm chain")
	}
	return NewLLMFromYAML(cfg.LLMChain[0])
}

// llm.NewRetry counts the first call as an attempt; 4 attempts gives
// the requested initial call plus 3 blank-output retries.
const blankOutputRetryAttempts = 4

type recoverableLLM struct {
	inner       llm.LLM
	snapshot    func(context.Context) (json.RawMessage, bool)
	compactDeps *CompactChainDeps
	runGuard    *compactRunGuard
}

// compactRunGuard caches the latest checkpoint and prefix token baseline for
// one model history. The orchestration loop adopts each replacement through the
// compaction sink. Later calls still check the growing history and may compact
// again when the next threshold is reached.
//
// One entry per history: a Runner's single recoverableLLM serves many histories
// at once, and a run identity is not one of them, so keying by run id alone let
// one history's checkpoint be spliced into another's request. The map is
// bounded; when it is full the oldest entry is dropped, which costs that
// history only its cached base until its next compaction.
type compactRunGuard struct {
	mu      sync.Mutex
	entries map[string]compactRunGuardEntry
	order   []string
}

type compactRunGuardEntry struct {
	compactedBase []llm.Message
	baseLen       int
	prefillTokens int
	prefillSet    bool
}

// compactRunGuardCap bounds the number of histories one Runner's guard
// remembers. A Runner serves its primary conversation plus the subagents
// running on it; the cap is far above that, and a dropped entry is harmless.
const compactRunGuardCap = 64

func newCompactRunGuard() *compactRunGuard {
	return &compactRunGuard{entries: make(map[string]compactRunGuardEntry)}
}

// historyKey names the model history a call is carrying: the run it belongs to
// and the conversation whose history it is. A run identity alone does not name
// one history — one Runner serves the primary chain, the fork chains and every
// typed child with a single recoverableLLM (see Runner.Load), and a child
// dispatched without a run row of its own borrows the dispatching run's id — so
// the guard's key carries the same conversation identity compaction itself is
// scoped to. Empty when the call names no run: such a call caches nothing, as
// before.
func historyKey(ctx context.Context) string {
	runID := strings.TrimSpace(tool.RunIDFromContext(ctx))
	if runID == "" {
		return ""
	}
	return runID + "\x00" + strings.TrimSpace(llm.AgentSessionIDFromContext(ctx))
}

// get returns the cached compacted base for the given history, along with the
// original message-count at compaction time. Returns ok=false when no
// compaction has been cached for this history (or when the key is empty).
func (g *compactRunGuard) get(key string) (base []llm.Message, baseLen int, ok bool) {
	if g == nil || key == "" {
		return nil, 0, false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	entry, ok := g.entries[key]
	if !ok || len(entry.compactedBase) == 0 {
		return nil, 0, false
	}
	return entry.compactedBase, entry.baseLen, true
}

// set caches the compacted base for the given history. prefillTokens is the
// compacted base's token count, computed by the caller with the session's own
// model's estimator so this prefix baseline shares the unit of the compaction
// decision and the observed overhead.
//
// baseLen is stored as the compacted length because the orchestration loop
// adopts the compacted history (see compactionAdoptionSink): the next call's
// message slice therefore starts with exactly this base, so combining
// base + messages[baseLen:] is an identity that appends only genuinely-new
// messages. Anchoring on the pre-compaction length instead would, on an
// unusually long turn, slice past newly produced messages and drop them.
func (g *compactRunGuard) set(key string, compacted []llm.Message, prefillTokens int) {
	if g == nil || key == "" || len(compacted) == 0 {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, exists := g.entries[key]; !exists {
		g.order = append(g.order, key)
		for len(g.order) > compactRunGuardCap {
			oldest := g.order[0]
			g.order = g.order[1:]
			delete(g.entries, oldest)
		}
	}
	g.entries[key] = compactRunGuardEntry{
		compactedBase: compacted,
		baseLen:       len(compacted),
		prefillTokens: max(0, prefillTokens),
		prefillSet:    true,
	}
}

// prefill returns the prefix token baseline recorded for this history's last
// checkpoint, recording the current size on the first call. A history that has
// never compacted answers -1, which tells the caller to derive the baseline
// from the slice it is holding.
//
// current counts the current size. It runs only when no baseline is recorded
// yet — counting a whole history is the expensive part of a model call — and
// outside the lock, so other histories sharing the guard are not held up.
func (g *compactRunGuard) prefill(key string, current func() int) int {
	if g == nil || key == "" {
		return -1
	}
	g.mu.Lock()
	if entry, ok := g.entries[key]; ok && entry.prefillSet {
		g.mu.Unlock()
		return entry.prefillTokens
	}
	g.mu.Unlock()
	currentTokens := current()

	g.mu.Lock()
	defer g.mu.Unlock()
	entry, ok := g.entries[key]
	if !ok {
		entry = compactRunGuardEntry{}
		g.order = append(g.order, key)
		for len(g.order) > compactRunGuardCap {
			oldest := g.order[0]
			g.order = g.order[1:]
			delete(g.entries, oldest)
		}
	}
	if !entry.prefillSet {
		entry.prefillTokens = max(0, currentTokens)
		entry.prefillSet = true
		g.entries[key] = entry
	}
	return entry.prefillTokens
}

// withImageHeal attaches the one retry a refused image gets: when the
// provider rejects a request because of an image, the request is re-sent once
// with every image degraded to a line of text.
//
// Both chains need it. The main chain is obvious; the summary chain is the one
// that turns "a conversation with one unsendable image" into a dead end — a
// compaction whose request is refused has no second way to ask, so the history
// keeps growing until nothing fits and every later turn fails too.
//
// It is safe to attach to the summary chain because it changes nothing on the
// success path: the wrapper calls inner with exactly the messages and tools it
// was given, and only a rejected call is retried, with the degraded copy
// belonging to that call alone. The stored conversation keeps its images.
func withImageHeal(inner llm.LLM) llm.LLM {
	if inner == nil {
		return nil
	}
	return llm.Use(inner, llm.NewImageHeal())
}

func WrapRecoverableLLM(
	inner llm.LLM,
	snapshot func(context.Context) (json.RawMessage, bool),
	compactDeps *CompactChainDeps,
	networkRetryMax int,
	streamIdle time.Duration,
	withLedger ...*llmAttemptLedger,
) llm.LLM {
	if inner == nil {
		return nil
	}
	// Below the blank-output retry and the orchestration wrapper (which is what
	// builds Result.Session): a provider that refuses an image gets one retry
	// with that image degraded to text, and the degraded request is this call
	// only — the transcript keeps its images.
	inner = withImageHeal(inner)
	inner = wrapBlankOutputRetryLLM(inner)
	// The network retry goes on last, i.e. outermost: it is the boundary a
	// request first succeeds or dies on, and it owns the discard/retry
	// notification every surface renders.
	inner = wrapNetworkRetryLLM(inner, networkRetryMax, streamIdle, withLedger...)
	return recoverableLLM{
		inner:       inner,
		snapshot:    snapshot,
		compactDeps: compactDeps,
		runGuard:    newCompactRunGuard(),
	}
}

func wrapBlankOutputRetryLLM(inner llm.LLM) llm.LLM {
	retryMW, err := llm.NewRetry(
		blankOutputRetryAttempts,
		llm.WithShouldRetry(llm.APIErrorIsBlankModelOutput),
		llm.WithResendGuard(),
	)
	if err != nil {
		return inner
	}
	return llm.Use(inner, retryMW)
}

// streamProgressIsNothing reports whether the request recorded for ctx had
// produced nothing when it failed: no record at all — a muted nested
// request — reads as nothing too.
func streamProgressIsNothing(ctx context.Context) bool {
	p := llm.StreamProgressFrom(ctx)
	return p == nil || p.Snapshot().Kind == llm.ProgressNothing
}

// wrapNetworkRetryLLM attaches the stream-failure retry loop. Each failure is
// decided from what the request had already produced, the way Claude Code
// decides it: a stream cut before any answer text or tool call finished is
// re-sent as a stream (exponential back-off capped at 32s plus 0–25% upward
// jitter), a request the stream cannot serve is re-sent without streaming, a
// stream cut after output finished is finished from what completed, and a
// request-level rejection is returned to the layers above. Every retry is
// announced through the stream sink so each surface can drop the dead
// attempt's text and show the retry cards. It is applied last of the chain
// inside WrapRecoverableLLM, making it the outermost retry: transport is the
// first boundary a request succeeds or dies on, and the recovery layers above
// (compaction, blank-output, 529) re-enter the whole inner stack on their own
// retries, inheriting this protection.
//
// maxRetries is the retry cap (agents.defaults.network_retry_max, default
// 10, at most config.MaxNetworkRetryMax). streamIdle is the stream-stall
// watchdog window (agents.defaults.stream_idle_timeout_seconds): when it is
// positive every Execute arms it, so a request that goes silent is aborted
// and reported as a dropped connection. 0 disables the watchdog. maxRetries
// 0 disables the retry loop but keeps the wrapper — the watchdog still fires,
// the stall simply surfaces as the call's error.
func wrapNetworkRetryLLM(inner llm.LLM, maxRetries int, streamIdle time.Duration, withLedger ...*llmAttemptLedger) llm.LLM {
	if inner == nil {
		return nil
	}
	var ledger *llmAttemptLedger
	if len(withLedger) > 0 {
		ledger = withLedger[0]
	}
	if maxRetries < 0 {
		maxRetries = 0
	}
	if maxRetries > appcfg.MaxNetworkRetryMax {
		maxRetries = appcfg.MaxNetworkRetryMax
	}
	retryCore := inner
	if maxRetries > 0 {
		retryMW := llm.NewStreamRetry(
			maxRetries,
			llm.WithMaxBackoff(32*time.Second),
			llm.WithUpwardJitter(),
			// A server that answered with Retry-After sets the wait itself; a
			// request for more than five minutes is not a wait this turn should
			// park on, and the error surfaces instead.
			llm.WithRetryAfter(llm.RetryAfterOf, 5*time.Minute),
			llm.WithRetryNotify(func(ctx context.Context, tr llm.TransportRetry) {
				// The attempt ledger reads the failure before anything else:
				// a muted stream suppresses the cards, not the record.
				if ledger != nil {
					ledger.noteFailure(ctx, tr)
				}
				if sink := llm.StreamSinkFrom(ctx); sink != nil && sink.OnTransportRetry != nil {
					sink.OnTransportRetry(tr)
				}
			}),
		)
		retryCore = llm.Use(inner, retryMW)
	}
	return networkRetryLLM{inner: retryCore, maxRetries: maxRetries, streamIdle: streamIdle, ledger: ledger}
}

// networkRetryLLM arms the stream-stall watchdog on every call and rewrites
// the retry loop's exhaustion into the sentence a user reads when the network
// never came back.
type networkRetryLLM struct {
	inner      llm.LLM
	maxRetries int
	streamIdle time.Duration
	// ledger, when set, records each physical attempt this boundary sees:
	// failures as the retry loop announces them, and the settling attempt on
	// return. Nil leaves the chain exactly as it was.
	ledger *llmAttemptLedger
}

func (n networkRetryLLM) Execute(ctx context.Context, messages []llm.Message, tools []*llm.Tool) (*llm.Result, error) {
	if n.streamIdle > 0 {
		ctx = llm.WithStreamWatchdog(ctx, n.streamIdle)
	}
	ctx = n.ledger.arm(ctx)
	res, err := n.inner.Execute(ctx, messages, tools)
	n.ledger.settle(ctx, res, err)
	if err != nil && llm.IsPartialResponse(err) {
		// The stream-retry layer already decided how this response ends;
		// the orchestration above finishes it from what completed.
		return res, err
	}
	// Only a mounted retry loop can exhaust: with maxRetries 0 the inner
	// chain is the raw LLM, and an exhaustion error bubbling out of it is
	// another layer's business, not a network give-up.
	if n.maxRetries > 0 {
		var exhausted *llm.MaxAttemptsExceededError
		if err != nil && errors.As(err, &exhausted) {
			return nil, fmt.Errorf("network error, gave up after %d retries: %w", n.maxRetries, exhausted.Err)
		}
	}
	return res, err
}

func (r recoverableLLM) Execute(ctx context.Context, messages []llm.Message, tools []*llm.Tool) (*llm.Result, error) {
	// Resolve the estimator once, for this call's own model, and thread it
	// through every occupancy number this call produces: the compaction
	// decision (compactIfNeeded -> shouldCompactMessages), the prefix baseline
	// cached in the run guard, and the estimate the observation is calibrated
	// against (observeSuccess/observeOverflow). The learned overhead is
	// server-real minus local-estimate, so all three must count in the same
	// unit or the overhead is added to a base it was never measured against.
	provider, model := r.compactDeps.activeModel(ctx)
	est := llm.EstimatorFor(provider, model)

	// Each LLM call is a pre-turn or mid-turn auto-compact checkpoint.
	messages, tail, compacted := r.applyRunScopedCompact(ctx, messages, tools, est)
	if compacted {
		// A mid-turn compaction just replaced the model context. Hand the
		// compacted history back to the enclosing orchestration loop so its live
		// session adopts it. Without this the
		// orchestration keeps growing an uncompacted slice that is persisted
		// verbatim, re-appending the pre-compaction history after the new
		// checkpoint boundary and re-inflating the next round's context.
		recordCompactionAdoption(ctx, messages)
	}

	sent := llm.EstimateMessagesWith(est, messages) + estimateToolsTokens(est, tools)
	res, err := r.inner.Execute(ctx, messages, tools)
	if err == nil {
		r.observeSuccess(ctx, res, est, sent)
		return res, nil
	}
	if errors.Is(err, context.Canceled) {
		return nil, err
	}
	if llm.IsPartialResponse(err) {
		// The stream-retry layer already decided how this response ends; the
		// compaction and 529 resends below must not re-ask the question.
		return nil, err
	}
	lastErr := err

	if isContextWindowExceeded(lastErr) {
		// The provider just told us this prompt does not fit. That is the only
		// authoritative statement about the real window we ever get, so record it
		// before recovering: overflow errors carry no limit number, and the
		// catalog's window may be wrong in either direction.
		r.observeOverflow(ctx, lastErr, sent)
		// A provider overflow uses the same replacement-checkpoint operation as
		// proactive compaction. Never recover by silently dropping exchanges.
		if compacted, ok := r.forceCompact(ctx, messages, tail, tools, est); ok {
			recordCompactionAdoption(ctx, compacted)
			if !llm.ResendSafe(ctx) {
				// The failed request had produced something a resend would
				// duplicate; the caller finishes from it.
				return nil, lastErr
			}
			llm.ResetStreamProgress(ctx)
			if res2, err2 := r.inner.Execute(ctx, compacted, tools); err2 == nil {
				r.observeSuccess(ctx, res2, est, llm.EstimateMessagesWith(est, compacted)+estimateToolsTokens(est, tools))
				return res2, nil
			} else {
				lastErr = err2
			}
		}
	}

	if llm.APIErrorIsBlankModelOutput(lastErr) {
		return nil, blankOutputError(lastErr)
	}
	// The 529 recovery re-asks a request that had produced nothing; one that
	// already showed output keeps what it showed (the stream-retry layer
	// decided), and the error goes up as it is.
	if isStatus529(lastErr) && streamProgressIsNothing(ctx) && ShouldRetryStatus529(QuerySourceFromContext(ctx)) {
		for attempt := 1; attempt <= Foreground529RetryCap(); attempt++ {
			timer := time.NewTimer(RetryDelay(attempt))
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
			if !llm.ResendSafe(ctx) {
				return nil, lastErr
			}
			llm.ResetStreamProgress(ctx)
			if res2, err2 := r.inner.Execute(ctx, messages, tools); err2 == nil {
				return res2, nil
			} else {
				lastErr = err2
				if llm.APIErrorIsBlankModelOutput(lastErr) {
					return nil, blankOutputError(lastErr)
				}
			}
			if !isStatus529(lastErr) {
				break
			}
		}
	}
	return nil, lastErr
}

func blankOutputError(err error) error {
	return llm.WithUsageError(
		errors.New("model returned a blank output; this turn was stopped. Please retry or switch to another model."),
		llm.UsageFromError(err),
	)
}

// applyRunScopedCompact keeps the checkpoint this history's later calls start
// from as the base for the model context, then appends messages produced after
// that checkpoint. It reports whether a new compaction happened on this call so
// the caller can propagate the replacement history to the orchestration loop,
// and how many trailing messages are provisional (see provisionalTail): those
// stay out of the checkpoint and follow it verbatim. A negative tail means the
// provisional messages cannot be told apart, and the call must not be compacted
// at all.
func (r recoverableLLM) applyRunScopedCompact(ctx context.Context, messages []llm.Message, tools []*llm.Tool, est llm.Estimator) ([]llm.Message, int, bool) {
	key := historyKey(ctx)
	history := messages
	prefillTokens := r.runGuard.prefill(key, func() int { return llm.EstimateMessagesWith(est, history) })
	tail, separable := provisionalTailLen(ctx, len(messages))
	if base, baseLen, ok := r.runGuard.get(key); ok && baseLen <= len(messages) {
		newMsgs := messages[baseLen:]
		if tail > len(newMsgs) {
			// A cached base reaching into the provisional messages is not the
			// history the orchestration loop described.
			separable = false
		}
		combined := make([]llm.Message, 0, len(base)+len(newMsgs))
		combined = append(combined, base...)
		combined = append(combined, newMsgs...)
		messages = combined
	}
	if !separable {
		// A checkpoint is durable, and a steer riding on this call is still the
		// user's to take back: once its tail cannot be told apart from settled
		// history, compacting would fold it in. This call goes out uncompacted —
		// its provisional messages settle with it, and the next call compacts as
		// usual. A negative tail also tells the overflow path not to compact
		// (see forceCompact): the same call must not fold the steer in through
		// the reactive route either.
		slog.Warn("compaction skipped: provisional messages cannot be told apart",
			"run_id", strings.TrimSpace(tool.RunIDFromContext(ctx)), "messages", len(messages))
		return messages, -1, false
	}
	head, provisional := messages[:len(messages)-tail], messages[len(messages)-tail:]
	var snapshotRaw json.RawMessage
	if r.snapshot != nil {
		if raw, ok := r.snapshot(ctx); ok {
			snapshotRaw = raw
		}
	}
	compactCtx, release := compactionContext(ctx)
	head, compacted := compactIfNeeded(compactCtx, head, snapshotRaw, r.compactDeps, prefillTokens, tools)
	release()
	if !compacted {
		return messages, tail, false
	}
	r.runGuard.set(key, head, llm.EstimateMessagesWith(est, head))
	return joinProvisionalTail(head, provisional), tail, true
}

// observeSuccess records the provider's own prompt size for an accepted call.
// This is the ground truth the local estimator is calibrated against, and it
// proves a lower bound on the real window. estimated is est's size of the
// request, and the overhead is learned under est's name — the estimator the
// compaction decision projects with (shouldCompactMessages).
func (r recoverableLLM) observeSuccess(ctx context.Context, res *llm.Result, est llm.Estimator, estimated int) {
	if res == nil || res.Usage == nil {
		return
	}
	provider, model := r.compactDeps.activeModel(ctx)
	realInput := res.Usage.InputTokens + res.Usage.CacheReadInputTokens + res.Usage.CacheCreationInputTokens
	llm.RecordSuccessFor(provider, model, est.Name(), realInput, estimated)
}

// observeOverflow records a rejected prompt size as an upper bound on the real
// window. Providers report overflow without a limit number, so the size Forebrain Harness
// sent is the only measurement available.
func (r recoverableLLM) observeOverflow(ctx context.Context, err error, estimated int) {
	provider, model := r.compactDeps.activeModel(ctx)
	realInput := 0
	if usage := llm.UsageFromError(err); usage != nil {
		realInput = usage.InputTokens + usage.CacheReadInputTokens + usage.CacheCreationInputTokens
	}
	llm.RecordOverflow(provider, model, realInput, estimated)
}

func (r recoverableLLM) forceCompact(ctx context.Context, messages []llm.Message, tail int, tools []*llm.Tool, est llm.Estimator) ([]llm.Message, bool) {
	if tail < 0 {
		return messages, false
	}
	head, provisional := messages[:len(messages)-tail], messages[len(messages)-tail:]
	compactCtx, release := compactionContext(ctx)
	compacted, ok := r.compactDeps.tryCompact(compactCtx, head, tools, true)
	release()
	if !ok {
		return messages, false
	}
	r.runGuard.set(historyKey(ctx), compacted, llm.EstimateMessagesWith(est, compacted))
	return joinProvisionalTail(compacted, provisional), true
}

// guardrailRunDedup tracks which run IDs have already had their input rails
// evaluated, so the input guard fires once per foreground user turn rather than
// once per agent-loop iteration. Bounded to avoid unbounded growth.
type guardrailRunDedup struct {
	mu    sync.Mutex
	seen  map[string]struct{}
	order []string
}

const guardrailRunDedupCap = 512

func newGuardrailRunDedup() *guardrailRunDedup {
	return &guardrailRunDedup{seen: make(map[string]struct{})}
}

// markChecked returns true the first time it sees a non-empty runID, false on
// repeats. An empty runID always returns true (cannot dedup; check every time).
func (d *guardrailRunDedup) markChecked(runID string) bool {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return true
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.seen[runID]; ok {
		return false
	}
	d.seen[runID] = struct{}{}
	d.order = append(d.order, runID)
	if len(d.order) > guardrailRunDedupCap {
		old := d.order[0]
		d.order = d.order[1:]
		delete(d.seen, old)
	}
	return true
}

// wrapGuardrailsLLM composes the guardrails middleware around the shared agent
// LLM client:
//   - deterministic input rails (length / blocklist) run once
//     per foreground main-thread turn, on the turn-initiating call only.
//   - output rail runs on foreground main-thread final answers (results with no
//     tool calls), never on intermediate tool-calling steps.
//
// Gating applies to explicit foreground main-thread queries and skips empty or
// slash-command input.
// dedup makes the input rail fire once per turn instead of once per loop
// iteration. Returns inner unchanged when guardrails resolve to nil.
func wrapGuardrailsLLM(inner llm.LLM, cfg *appcfg.Root, dedup *guardrailRunDedup) llm.LLM {
	if inner == nil {
		return inner
	}
	r := safety.ResolveFromRoot(cfg)
	if r == nil {
		return inner
	}

	inputGuard := func(ctx context.Context, messages []llm.Message) error {
		if !explicitMainThreadQuerySource(QuerySourceFromContext(ctx)) {
			return nil
		}
		if dedup != nil && !dedup.markChecked(tool.RunIDFromContext(ctx)) {
			return nil
		}
		q := strings.TrimSpace(latestUserText(messages))
		if q == "" || strings.HasPrefix(q, "/") {
			return nil
		}
		if err := safety.RunInputRules(r, q); err != nil {
			return err
		}
		return nil
	}

	outputGuard := func(ctx context.Context, result *llm.Result) error {
		if !r.OutputEnabled {
			return nil
		}
		if result == nil || result.Message == nil {
			return nil
		}
		if len(result.Message.ToolCalls) > 0 {
			return nil // intermediate tool-calling step; not a final answer
		}
		if !explicitMainThreadQuerySource(QuerySourceFromContext(ctx)) {
			return nil
		}
		_, err := safety.ApplyOutputRail(r, result.Message.TextContent())
		return err
	}

	mw := safety.NewGuardrails(
		safety.WithMessageGuard("input-rail", inputGuard),
		safety.WithResultGuard("output-rail", outputGuard),
	)
	return llm.Use(inner, mw)
}

// latestUserText returns the most recent non-empty user message text.
func latestUserText(messages []llm.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == llm.RoleUser {
			if text := strings.TrimSpace(messages[i].TextContent()); text != "" {
				return text
			}
		}
	}
	return ""
}

type internalLLMUsageAccumulator = llm.UsageAccumulator

func WithInternalLLMUsageAccumulator(ctx context.Context, acc *internalLLMUsageAccumulator) context.Context {
	return llm.WithUsageAccumulator(ctx, acc)
}

func internalLLMUsageAccumulatorFrom(ctx context.Context) *internalLLMUsageAccumulator {
	return llm.UsageAccumulatorFrom(ctx)
}

type internalLLMUsageObserver struct {
	sink *llm.StreamSink
	mu   sync.Mutex
	seen bool
}

func newInternalLLMUsageObserver(sink *llm.StreamSink) *internalLLMUsageObserver {
	if sink == nil {
		return nil
	}
	return &internalLLMUsageObserver{sink: sink}
}

func (o *internalLLMUsageObserver) wrapContext(ctx context.Context) context.Context {
	if o == nil || o.sink == nil {
		return ctx
	}
	wrapped := *o.sink
	origOnUsage := wrapped.OnUsage
	wrapped.OnUsage = func(inputTokens int, outputTokens int) {
		if inputTokens > 0 || outputTokens > 0 {
			o.mu.Lock()
			o.seen = true
			o.mu.Unlock()
		}
		if origOnUsage != nil {
			origOnUsage(inputTokens, outputTokens)
		}
	}
	return llm.WithStreamSink(ctx, &wrapped)
}

func (o *internalLLMUsageObserver) reportFinalIfNeeded(usage *llm.Usage) {
	if o == nil || o.sink == nil || usage == nil {
		return
	}
	totalInput := usage.InputTokens + usage.CacheCreationInputTokens + usage.CacheReadInputTokens
	if totalInput <= 0 && usage.OutputTokens <= 0 {
		return
	}
	o.mu.Lock()
	seen := o.seen
	o.mu.Unlock()
	if seen {
		return
	}
	if o.sink.OnUsage != nil {
		o.sink.OnUsage(totalInput, usage.OutputTokens)
	}
	if o.sink.OnUsageSnapshot != nil {
		o.sink.OnUsageSnapshot(totalInput, usage.OutputTokens)
	}
}

func cloneUsage(usage *llm.Usage) *llm.Usage {
	if usage == nil {
		return nil
	}
	cp := *usage
	return &cp
}

func attachUsageToError(err error, usage *llm.Usage) error {
	if err == nil || usage == nil {
		return err
	}
	return llm.NewExecuteError(err, cloneUsage(usage))
}

type usageAccountingLLM struct {
	inner llm.LLM
}

func wrapUsageAccountingLLM(inner llm.LLM) llm.LLM {
	if inner == nil {
		return nil
	}
	accounting := &usageAccountingLLM{inner: inner}
	if _, ok := inner.(llm.ContextCompactor); ok {
		return &usageAccountingCompactor{usageAccountingLLM: accounting}
	}
	return accounting
}

func WrapUsageAccountingLLMForTest(inner llm.LLM) llm.LLM {
	return wrapUsageAccountingLLM(inner)
}

func (w *usageAccountingLLM) Execute(ctx context.Context, messages []llm.Message, tools []*llm.Tool) (*llm.Result, error) {
	if w == nil || w.inner == nil {
		return nil, nil
	}
	return w.executeWithAccounting(ctx, func(callCtx context.Context) (*llm.Result, error) {
		return w.inner.Execute(callCtx, messages, tools)
	})
}

func (w *usageAccountingLLM) ExecuteStructured(ctx context.Context, messages []llm.Message, spec llm.StructuredOutputSpec) (*llm.Result, error) {
	if w == nil || w.inner == nil {
		return nil, nil
	}
	structured, ok := w.inner.(llm.StructuredOutputLLM)
	if !ok {
		return nil, fmt.Errorf("provider does not support strict structured output")
	}
	return w.executeWithAccounting(ctx, func(callCtx context.Context) (*llm.Result, error) {
		return structured.ExecuteStructured(callCtx, messages, spec)
	})
}

func (w *usageAccountingLLM) executeWithAccounting(ctx context.Context, execute func(context.Context) (*llm.Result, error)) (*llm.Result, error) {
	// Usage is forwarded to the TUI live stream (StreamSink.OnUsage, which
	// drives the "Working" line and the cumulative composer footer) only when
	// this Execute runs inside a run's accounting scope - i.e. a
	// UsageAccumulator is present in ctx, so the same usage also reaches
	// res.Summary.Usage (the "Worked for" final). Calls that carry a TUI sink
	// but no accumulator (e.g. the goal-continuation evaluator running in the
	// supervisor runCtx) would otherwise leak into the live token stream
	// without ever reaching the authoritative final, making "Working" overshoot
	// "Worked for" and, after end-of-run reconciliation, collapsing the
	// cumulative footer onto the single-run final. Suppress OnUsage for those
	// so streamed and final stay in lockstep.
	hasAcc := internalLLMUsageAccumulatorFrom(ctx) != nil
	sink := llm.StreamSinkFrom(ctx)
	var observer *internalLLMUsageObserver
	switch {
	case hasAcc && sink != nil:
		observer = newInternalLLMUsageObserver(sink)
		if observer != nil {
			ctx = observer.wrapContext(ctx)
		}
	case !hasAcc && sink != nil:
		muted := *sink
		muted.OnUsage = nil
		ctx = llm.WithStreamSink(ctx, &muted)
	}
	res, err := execute(ctx)
	usage := usageCopy(res)
	if usage == nil {
		usage = llm.UsageFromError(err)
	}
	if usage != nil {
		if acc := internalLLMUsageAccumulatorFrom(ctx); acc != nil {
			acc.ObserveUsage(*usage)
		}
	}
	if observer != nil {
		observer.reportFinalIfNeeded(usage)
	}
	return res, err
}

// usageAccountingCompactor is returned only when the wrapped client really
// supports provider-side compaction. Keeping Compact off the base wrapper is
// important: callers use the ContextCompactor interface as the capability
// check that selects remote versus local compaction.
type usageAccountingCompactor struct {
	*usageAccountingLLM
}

func (w *usageAccountingCompactor) Compact(ctx context.Context, messages []llm.Message, tools []*llm.Tool, mode llm.CompactMode) (*llm.CompactResult, error) {
	if w == nil || w.usageAccountingLLM == nil || w.inner == nil {
		return nil, nil
	}
	compactor := w.inner.(llm.ContextCompactor)
	result, err := compactor.Compact(ctx, messages, tools, mode)
	if result != nil && result.Usage != nil {
		if acc := internalLLMUsageAccumulatorFrom(ctx); acc != nil {
			acc.ObserveUsage(*result.Usage)
		}
	}
	return result, err
}

func isContextWindowExceeded(err error) bool {
	return llm.IsExceeded(err)
}
