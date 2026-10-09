// Compaction: proactive trigger, chain, and adoption back into the session.
package run

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"github.com/forebrain-harness/forebrain-harness/pkg/assembly"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

// compactModelLimit resolves the budget independently of the optional context
// snapshot. Resume and worker runs may never execute the interactive assembly
// hook, and a snapshot can describe a previously selected model.
func compactModelLimit(snapshotRaw json.RawMessage, provider, model string) int {
	limits, known := llm.Lookup(provider, model)
	window := limits.Limits().EffectiveInputLimit()
	if !known || window <= 0 {
		window = modelContextLimitFromSnapshot(snapshotRaw)
	}
	if window <= 0 {
		// Match the pre-turn path's fallback for uncatalogued models, including
		// explicit thresholds; a missing snapshot must not disable compaction.
		window = state.CalculateTokenBudget(0, model, limits).EffectiveContextWindow
	}
	return llm.EffectiveWindow(provider, model, window)
}

const (
	// proactiveCompactThreshold is the fraction of the model's effective context
	// window at which proactive compaction triggers — same fraction used by
	// session.CalculateTokenBudget (ProactiveCompactFraction).
	proactiveCompactThreshold = 0.90
)

// estimateToolsTokens estimates the tool-schema block sent with every request.
// This is part of the model's input on each call and is often the largest fixed
// cost in the request, so occupancy that omits it systematically reads low.
func estimateToolsTokens(tools []*llm.Tool) int {
	total := 0
	for _, tool := range tools {
		if tool == nil {
			continue
		}
		total += llm.EstimateText(tool.Name())
		total += llm.EstimateText(tool.Description())
		if schema := tool.InputSchema(); len(schema) > 0 {
			if raw, err := json.Marshal(schema); err == nil {
				total += llm.EstimateText(string(raw))
			}
		}
		total += 8
	}
	return total
}

// modelContextLimitFromSnapshot extracts the effective input window size used
// for compaction decisions. Older snapshots only carry the total context
// window, so keep that as a compatibility fallback.
func modelContextLimitFromSnapshot(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var snap struct {
		EffectiveInputTokens int `json:"effective_input_tokens"`
		ModelContextTokens   int `json:"model_context_tokens"`
	}
	if err := json.Unmarshal(raw, &snap); err != nil {
		return 0
	}
	if snap.EffectiveInputTokens > 0 {
		return snap.EffectiveInputTokens
	}
	return snap.ModelContextTokens
}

type CompactChainDeps struct {
	// TryCompact checkpoints the live history — messages and tools are the
	// request the agent is about to make. Reactive marks a compaction forced by
	// the provider rejecting that request as too large.
	TryCompact    func(ctx context.Context, messages []llm.Message, tools []*llm.Tool, reactive bool) ([]llm.Message, bool, error)
	ExplicitLimit int
	LimitScope    string
	// ActiveModel identifies the model the next call will use, so observed
	// context-window behaviour can be attributed to it. It takes the calling
	// context because a shared runner may serve conversations on different
	// models.
	ActiveModel func(ctx context.Context) (provider string, model string)
}

func (d *CompactChainDeps) activeModel(ctx context.Context) (string, string) {
	if d == nil || d.ActiveModel == nil {
		return "", ""
	}
	return d.ActiveModel(ctx)
}

func (d *CompactChainDeps) tryCompact(ctx context.Context, messages []llm.Message, tools []*llm.Tool, reactive bool) ([]llm.Message, bool) {
	if d == nil || d.TryCompact == nil {
		return nil, false
	}
	// A failed compaction has already reported itself through its lifecycle
	// events; the call it was made for goes ahead with the history unchanged.
	compacted, ok, err := d.TryCompact(llm.WithMutedForegroundStream(ctx), messages, tools, reactive)
	return compacted, ok && err == nil
}

// compactIfNeeded implements the single checkpoint path. Crossing 90%
// replaces the active history with one compact checkpoint.
func compactIfNeeded(
	ctx context.Context,
	messages []llm.Message,
	snapshotRaw json.RawMessage,
	deps *CompactChainDeps,
	bodyPrefixTokens int,
	tools []*llm.Tool,
) ([]llm.Message, bool) {
	provider, model := deps.activeModel(ctx)
	modelLimit := compactModelLimit(snapshotRaw, provider, model)
	if modelLimit <= 0 || deps == nil || deps.TryCompact == nil || !shouldCompactMessages(messages, modelLimit, deps, bodyPrefixTokens, tools, provider, model) {
		return messages, false
	}
	compacted, ok := deps.tryCompact(ctx, messages, tools, false)
	if !ok {
		return messages, false
	}
	return compacted, true
}

func shouldCompactMessages(messages []llm.Message, modelLimit int, deps *CompactChainDeps, bodyPrefixTokens int, tools []*llm.Tool, provider, model string) bool {
	if modelLimit <= 0 {
		return false
	}
	// Tool schemas are part of every request's input and can run to tens of
	// thousands of tokens, so occupancy that ignores them reads far below the
	// truth and the threshold never fires. The rest of what this layer cannot
	// see -- the prefix the inner wrappers prepend, the provider's own framing
	// -- is added back as the token overhead those same providers have reported.
	total := llm.Project(provider, model, llm.EstimateMessages(messages)+estimateToolsTokens(tools))
	if total >= modelLimit {
		return true
	}
	threshold := int(float64(modelLimit) * proactiveCompactThreshold)
	if deps != nil && deps.ExplicitLimit > 0 && deps.ExplicitLimit < threshold {
		threshold = deps.ExplicitLimit
	}
	used := total
	if deps != nil && deps.LimitScope == "body_after_prefix" {
		if bodyPrefixTokens >= 0 {
			used = max(0, total-bodyPrefixTokens)
		} else {
			used = bodyAfterPrefixTokens(messages, total)
		}
	}
	return used >= threshold
}

func bodyAfterPrefixTokens(messages []llm.Message, total int) int {
	checkpoint := -1
	for i, msg := range messages {
		if msg.Compaction != nil || (msg.Role == llm.RoleUser && strings.HasPrefix(strings.TrimSpace(msg.TextContent()), assembly.SummaryPrefix+"\n")) {
			checkpoint = i
		}
	}
	prefixEnd := checkpoint
	if prefixEnd < 0 {
		for i, msg := range messages {
			if msg.Role != llm.RoleSystem {
				break
			}
			prefixEnd = i
		}
	}
	if prefixEnd < 0 {
		return total
	}
	return max(0, total-llm.EstimateMessages(messages[:prefixEnd+1]))
}

// compactionAdoptionSink is a context-scoped, one-shot channel that lets an
// inner LLM wrapper (recoverableLLM) hand the compacted replacement history back
// to the enclosing tool-orchestration loop.
//
// A compaction must replace the live history, not just the request: when a
// mid-turn compaction replaces the model context, the LIVE session must adopt the
// compacted messages, otherwise the orchestration keeps growing an uncompacted
// slice that is later persisted verbatim — re-appending the pre-compaction
// history (and its summary) after every new checkpoint boundary and blowing the
// projected context back up on the next round.
//
// A context value is used instead of a Result field because several passthrough
// wrappers (guardrails, fork capture) sit between the orchestration loop and
// recoverableLLM; a context holder survives them all without each wrapper
// having to forward a new Result field.
type compactionAdoptionSink struct {
	mu       sync.Mutex
	replaced []llm.Message
	adopted  bool
}

func (s *compactionAdoptionSink) record(messages []llm.Message) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replaced = append([]llm.Message(nil), messages...)
	s.adopted = true
}

// take returns the recorded compacted history exactly once, clearing the sink so
// the next inner call starts fresh.
func (s *compactionAdoptionSink) take() ([]llm.Message, bool) {
	if s == nil {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.adopted {
		return nil, false
	}
	out := s.replaced
	s.replaced, s.adopted = nil, false
	return out, true
}

type compactionAdoptionSinkKey struct{}

func withCompactionAdoptionSink(ctx context.Context, sink *compactionAdoptionSink) context.Context {
	if sink == nil {
		return ctx
	}
	return context.WithValue(ctx, compactionAdoptionSinkKey{}, sink)
}

func compactionAdoptionSinkFromContext(ctx context.Context) *compactionAdoptionSink {
	if ctx == nil {
		return nil
	}
	sink, _ := ctx.Value(compactionAdoptionSinkKey{}).(*compactionAdoptionSink)
	return sink
}

// recordCompactionAdoption publishes the compacted replacement history to the
// enclosing orchestration loop, if one installed a sink. No-op otherwise (e.g.
// direct LLM callers that keep no long-lived session).
func recordCompactionAdoption(ctx context.Context, messages []llm.Message) {
	if len(messages) == 0 {
		return
	}
	compactionAdoptionSinkFromContext(ctx).record(messages)
}

// provisionalTail marks the trailing messages of one model call that are not
// settled history yet: the queued steers riding on it — and, when they reopened
// a finished turn, the answer they follow — any of which the user can still
// retract until the call commits. recoverableLLM keeps them out of a
// checkpoint, which is durable: a retracted steer must never survive in it, and
// a steer rolled back after a failed call is sent again, so folding it in would
// send it twice.
type provisionalTail struct {
	sent  int             // length of the message slice the call was handed
	count int             // how many of its trailing messages are provisional
	outer context.Context // the orchestration context, without the call's retraction abort
}

type provisionalTailKey struct{}

// withProvisionalTail marks the last count of the sent messages handed to the
// call made under ctx as provisional. outer is the context the call's own was
// derived from: a compaction runs on its cancellation, not the call's, so a
// retraction that aborts the call does not throw away a checkpoint that never
// included the retracted steer.
func withProvisionalTail(ctx, outer context.Context, sent, count int) context.Context {
	if count <= 0 || count > sent {
		return ctx
	}
	return context.WithValue(ctx, provisionalTailKey{}, provisionalTail{sent: sent, count: count, outer: outer})
}

// provisionalTailLen is how many trailing messages of a sent slice are
// provisional — zero unless ctx describes exactly this slice, so a nested call
// that inherits the context with other messages compacts them whole, as before.
func provisionalTailLen(ctx context.Context, sent int) int {
	if ctx == nil {
		return 0
	}
	pt, ok := ctx.Value(provisionalTailKey{}).(provisionalTail)
	if !ok || pt.sent != sent {
		return 0
	}
	return pt.count
}

// compactionContext is ctx minus the call's retraction abort: it keeps every
// value, but it is cancelled only when the orchestration context is. Without a
// provisional tail there is nothing to retract, and ctx is returned as is.
func compactionContext(ctx context.Context) (context.Context, func()) {
	pt, ok := ctx.Value(provisionalTailKey{}).(provisionalTail)
	if !ok || pt.outer == nil {
		return ctx, func() {}
	}
	detached, cancel := context.WithCancelCause(context.WithoutCancel(ctx))
	stop := context.AfterFunc(pt.outer, func() { cancel(context.Cause(pt.outer)) })
	return detached, func() {
		stop()
		cancel(nil)
	}
}

// joinProvisionalTail appends the provisional messages after a checkpoint, in a
// fresh slice so the checkpoint the run guard caches is never written through.
func joinProvisionalTail(head, tail []llm.Message) []llm.Message {
	if len(tail) == 0 {
		return head
	}
	out := make([]llm.Message, 0, len(head)+len(tail))
	out = append(out, head...)
	return append(out, tail...)
}

// CompactionService assembles the compaction service for an explicit /compact.
//
// It was implemented twice, byte for byte, in the TUI and the gateway, and the
// only difference was how each surface named its session store. The wiring is a
// pure function of the Runner plus the session store, so it lives here rather
// than being duplicated wherever a surface draws a compact command.
func CompactionService(r *Runner, sessions *state.SessionStore) assembly.Service {
	out := assembly.Service{Sessions: sessions}
	if r == nil {
		return out
	}
	out.CompactLLM = r.agentCompactClientFor
	out.ModelProvider = r.ContextCompactModelProvider
	out.CompactLLMForModel = r.ContextCompactLLMForModel
	out.PrimaryModel = func(ctx context.Context) (string, string) { return r.agentModelFor(ctx) }
	if r.AppCfg != nil {
		out.Prompt = r.AppCfg.Compact.Prompt
		out.RemoteCompaction = r.AppCfg.Compact.UseRemoteCompaction()
		out.RemoteV1 = !r.AppCfg.Compact.UseRemoteV2()
		out.ExplicitLimit = r.AppCfg.Compact.ModelAutoCompactTokenLimit
		out.LimitScope = r.AppCfg.Compact.ModelAutoCompactTokenLimitScope
	}
	out.PreCompact = func(ctx context.Context, sid, trigger string) error {
		return r.RunCompactHook(ctx, "PreCompact", sid, trigger)
	}
	out.PostCompact = func(ctx context.Context, sid, trigger string) error {
		return r.RunCompactHook(ctx, "PostCompact", sid, trigger)
	}
	out.Events = event.SinkFunc(r.publishSurfaceEvent)
	out.ConversationSummary = r.conversationSummarizer
	return out
}

// conversationSummarizer sends a stored conversation's summary request as its
// next request would be sent: the conversation's head and history, the
// agent's tools, through the chain that shapes a request's prefix — the same
// bytes every turn of the conversation starts with.
func (r *Runner) conversationSummarizer(sessionID string) assembly.ConversationSummarizer {
	r.mu.load.RLock()
	main, transcript, chain := r.main, r.transcript, r.summaryLLM
	r.mu.load.RUnlock()
	if main == nil || chain == nil || transcript.store == nil {
		// Not loaded yet: there is no conversation request to reproduce, and
		// the summary is asked for on its own.
		return nil
	}
	tools := main.Tools()
	return func(ctx context.Context, instruction llm.Message) (*llm.Result, error) {
		ctx = llm.WithAgentSessionID(ctx, sessionID)
		// A fork runs on the system frozen at its birth (decision D6), so its
		// head is that system and its tools are the fork runtime's — the same
		// prefix its continuation replays. Everything else starts at the
		// conversation's own head, whose system the typed wrappers substitute
		// per request.
		session := transcript
		headTools := tools
		head := transcript.head(ctx)
		if frozen, ok, ferr := transcript.store.SessionPromptState(ctx, sessionID, forkSystemPromptKey); ferr == nil && ok && strings.TrimSpace(frozen) != "" {
			session = transcriptSession{store: transcript.store, systemPrompt: frozen, resolver: r.FileResolver}
			head = []llm.Message{llm.SystemMessage(frozen)}
			headTools = r.LoadedTools()
		}
		history, err := session.history(ctx, sessionID)
		if err != nil {
			return nil, err
		}
		return chain.Execute(ctx, append(append(head, history...), instruction), headTools)
	}
}
