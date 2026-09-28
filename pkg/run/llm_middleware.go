// LLM middleware chain: retry, fallback, recovery, guardrails, and usage.
package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
// an agent run. The orchestration loop adopts each replacement through the
// compaction sink. Later calls still check the growing history and may compact
// again when the next threshold is reached.
type compactRunGuard struct {
	mu            sync.Mutex
	runID         string
	compactedBase []llm.Message
	baseLen       int
	prefillTokens int
	prefillSet    bool
}

func newCompactRunGuard() *compactRunGuard {
	return &compactRunGuard{}
}

// get returns the cached compacted base for the given run ID, along with the
// original message-count at compaction time. Returns ok=false when no
// compaction has been cached for this run (or when runID is empty).
func (g *compactRunGuard) get(runID string) (base []llm.Message, baseLen int, ok bool) {
	if g == nil || runID == "" {
		return nil, 0, false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.runID != runID || len(g.compactedBase) == 0 {
		return nil, 0, false
	}
	return g.compactedBase, g.baseLen, true
}

// set caches the compacted base for the given run ID, replacing any previous
// entry (which would be from a different run).
//
// baseLen is stored as the compacted length because the orchestration loop
// adopts the compacted history (see compactionAdoptionSink): the next call's
// message slice therefore starts with exactly this base, so combining
// base + messages[baseLen:] is an identity that appends only genuinely-new
// messages. Anchoring on the pre-compaction length instead would, on an
// unusually long turn, slice past newly produced messages and drop them.
func (g *compactRunGuard) set(runID string, compacted []llm.Message) {
	if g == nil || runID == "" || len(compacted) == 0 {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.runID = runID
	g.compactedBase = compacted
	g.baseLen = len(compacted)
	g.prefillTokens = llm.EstimateMessages(compacted)
	g.prefillSet = true
}

func (g *compactRunGuard) prefill(runID string, currentTokens int) int {
	if g == nil || runID == "" {
		return -1
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.runID != runID {
		g.runID = runID
		g.compactedBase = nil
		g.baseLen = 0
		g.prefillSet = false
	}
	if !g.prefillSet {
		g.prefillTokens = max(0, currentTokens)
		g.prefillSet = true
	}
	return g.prefillTokens
}

func WrapRecoverableLLM(
	inner llm.LLM,
	snapshot func(context.Context) (json.RawMessage, bool),
	compactDeps *CompactChainDeps,
) llm.LLM {
	if inner == nil {
		return nil
	}
	inner = wrapBlankOutputRetryLLM(inner)
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
	)
	if err != nil {
		return inner
	}
	return llm.Use(inner, retryMW)
}

func (r recoverableLLM) Execute(ctx context.Context, messages []llm.Message, tools []*llm.Tool) (*llm.Result, error) {
	// Each LLM call is a pre-turn or mid-turn auto-compact checkpoint.
	messages, compacted := r.applyRunScopedCompact(ctx, messages, tools)
	if compacted {
		// A mid-turn compaction just replaced the model context. Hand the
		// compacted history back to the enclosing orchestration loop so its live
		// session adopts it. Without this the
		// orchestration keeps growing an uncompacted slice that is persisted
		// verbatim, re-appending the pre-compaction history after the new
		// checkpoint boundary and re-inflating the next round's context.
		recordCompactionAdoption(ctx, messages)
	}

	sent := llm.EstimateMessages(messages) + estimateToolsTokens(tools)
	res, err := r.inner.Execute(ctx, messages, tools)
	if err == nil {
		r.observeSuccess(ctx, res, sent)
		return res, nil
	}
	if errors.Is(err, context.Canceled) {
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
		if compacted, ok := r.forceCompact(ctx, messages, tools); ok {
			recordCompactionAdoption(ctx, compacted)
			if res2, err2 := r.inner.Execute(ctx, compacted, tools); err2 == nil {
				r.observeSuccess(ctx, res2, llm.EstimateMessages(compacted)+estimateToolsTokens(tools))
				return res2, nil
			} else {
				lastErr = err2
			}
		}
	}

	if llm.APIErrorIsBlankModelOutput(lastErr) {
		return nil, blankOutputError(lastErr)
	}
	if isStatus529(lastErr) && ShouldRetryStatus529(QuerySourceFromContext(ctx)) {
		for attempt := 1; attempt <= Foreground529RetryCap(); attempt++ {
			timer := time.NewTimer(RetryDelay(attempt))
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
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

// applyRunScopedCompact keeps a compacted checkpoint as the base for later LLM
// calls in the same run, then appends messages produced after that checkpoint.
// It reports whether a new compaction happened on this call so the caller can
// propagate the replacement history to the orchestration loop.
func (r recoverableLLM) applyRunScopedCompact(ctx context.Context, messages []llm.Message, tools []*llm.Tool) ([]llm.Message, bool) {
	runID := strings.TrimSpace(tool.RunIDFromContext(ctx))
	prefillTokens := r.runGuard.prefill(runID, llm.EstimateMessages(messages))
	if base, baseLen, ok := r.runGuard.get(runID); ok && baseLen <= len(messages) {
		newMsgs := messages[baseLen:]
		combined := make([]llm.Message, 0, len(base)+len(newMsgs))
		combined = append(combined, base...)
		combined = append(combined, newMsgs...)
		messages = combined
	}
	var snapshotRaw json.RawMessage
	if r.snapshot != nil {
		if raw, ok := r.snapshot(ctx); ok {
			snapshotRaw = raw
		}
	}
	messages, compacted := compactIfNeeded(ctx, messages, snapshotRaw, r.compactDeps, prefillTokens, tools)
	if compacted {
		r.runGuard.set(runID, messages)
	}
	return messages, compacted
}

// observeSuccess records the provider's own prompt size for an accepted call.
// This is the ground truth the local estimator is calibrated against, and it
// proves a lower bound on the real window.
func (r recoverableLLM) observeSuccess(ctx context.Context, res *llm.Result, estimated int) {
	if res == nil || res.Usage == nil {
		return
	}
	provider, model := r.compactDeps.activeModel(ctx)
	realInput := res.Usage.InputTokens + res.Usage.CacheReadInputTokens + res.Usage.CacheCreationInputTokens
	llm.RecordSuccess(provider, model, realInput, estimated)
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

func (r recoverableLLM) forceCompact(ctx context.Context, messages []llm.Message, tools []*llm.Tool) ([]llm.Message, bool) {
	compacted, ok := r.compactDeps.tryCompact(ctx, messages, tools, true)
	if !ok {
		return messages, false
	}
	r.runGuard.set(strings.TrimSpace(tool.RunIDFromContext(ctx)), compacted)
	return compacted, true
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
