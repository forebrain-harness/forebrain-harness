// Memory middleware: startup pipeline, injected instruction, and citations.
package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm/openai"
	"github.com/forebrain-harness/forebrain-harness/pkg/memory"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// buildMemoryPipeline assembles this Runner's background memory pipeline.
//
// The assembly itself lives in pkg/memory (memory.NewPipeline); what stays here
// is only what this layer knows: how to construct an LLM client for a model,
// and how to derive the rollout limit and rate-limit guard from the provider
// that model resolves to. pkg/memory cannot do that itself without importing
// pkg/run, which would be a cycle.
func (r *Runner) buildMemoryPipeline() *memory.Pipeline {
	if r == nil {
		return nil
	}
	cfg := r.AppCfg
	agentType := r.activeAgentNameForModel()
	return memory.NewPipeline(memory.PipelineDeps{
		Store:         r.MemoryStore,
		Transcripts:   r.SessionStore,
		Cfg:           cfg,
		WorkspaceRoot: r.workspaceRoot(),
		ProjectKey:    r.ProjectKey,
		ExtractLLM: func(settings appcfg.MemoriesConfig) llm.LLM {
			return r.deferToForeground(buildMemoryLLM(cfg, agentType, settings.ExtractModel, "low"))
		},
		ConsolidateRunner: func(settings appcfg.MemoriesConfig) memory.ConsolidationRunner {
			return &memoryConsolidationRunner{client: r.deferToForeground(buildMemoryLLM(cfg, agentType, settings.ConsolidationModel, "medium"))}
		},
		Stage1RolloutTokenLimit: func(settings appcfg.MemoriesConfig) int {
			return memoryStage1RolloutTokenLimit(cfg, agentType, settings.ExtractModel)
		},
		RateLimitGuard: func(appcfg.MemoriesConfig) func(context.Context, int) bool {
			return memoryRateLimitGuard(r.Home, cfg, agentType)
		},
	})
}

// The memory pipeline's requests carry prompt prefixes that have nothing to do
// with the user's conversation — a 30KB extraction prompt, a 55KB consolidation
// prompt — and a provider's implicit prefix cache is a shared, finite resource
// per account. Firing them while the user's own turn is mid-flight is what puts
// two unrelated large prefixes in the cache at the same time, and the requests
// that came back with the session's prefix gone were the ones issued next to
// that traffic.
//
// So a background memory request waits for the user's turn to finish and for
// the foreground to stay quiet for a moment. The wait is bounded: a session
// that never goes quiet must not stop memory from ever being written, and past
// the bound the request goes out anyway.
const (
	memoryForegroundSettle  = 3 * time.Second
	memoryForegroundPoll    = time.Second
	memoryForegroundMaxWait = 2 * time.Minute
)

// foregroundFlight counts the user-facing turns a runtime has in flight and
// remembers when the last one ended.
type foregroundFlight struct {
	active   atomic.Int64
	lastBusy atomic.Int64
}

func (f *foregroundFlight) enter() {
	f.active.Add(1)
}

func (f *foregroundFlight) leave() {
	f.lastBusy.Store(time.Now().UnixNano())
	f.active.Add(-1)
}

// idleFor reports that no turn is running and none has ended within settle.
func (f *foregroundFlight) idleFor(settle time.Duration) bool {
	if f == nil {
		return true
	}
	if f.active.Load() > 0 {
		return false
	}
	last := f.lastBusy.Load()
	return last == 0 || time.Since(time.Unix(0, last)) >= settle
}

// deferToForeground wraps a background memory client so its requests wait for
// the foreground to be quiet. A nil client stays nil: the pipeline reads that
// as "this stage has no model" and skips the stage.
func (r *Runner) deferToForeground(inner llm.LLM) llm.LLM {
	if r == nil || inner == nil {
		return inner
	}
	return &backgroundMemoryLLM{inner: inner, idle: func() bool { return r.foreground.idleFor(memoryForegroundSettle) }}
}

type backgroundMemoryLLM struct {
	inner llm.LLM
	idle  func() bool
}

func (w *backgroundMemoryLLM) Execute(ctx context.Context, messages []llm.Message, tools []*llm.Tool) (*llm.Result, error) {
	if w == nil || w.inner == nil {
		return nil, nil
	}
	w.waitForQuiet(ctx)
	return w.inner.Execute(ctx, messages, tools)
}

func (w *backgroundMemoryLLM) ExecuteStructured(ctx context.Context, messages []llm.Message, spec llm.StructuredOutputSpec) (*llm.Result, error) {
	if w == nil || w.inner == nil {
		return nil, nil
	}
	structured, ok := w.inner.(llm.StructuredOutputLLM)
	if !ok {
		return nil, fmt.Errorf("extraction model does not support strict structured output")
	}
	w.waitForQuiet(ctx)
	return structured.ExecuteStructured(ctx, messages, spec)
}

func (w *backgroundMemoryLLM) waitForQuiet(ctx context.Context) {
	if w.idle == nil || w.idle() {
		return
	}
	deadline := time.NewTimer(memoryForegroundMaxWait)
	defer deadline.Stop()
	poll := time.NewTicker(memoryForegroundPoll)
	defer poll.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-deadline.C:
			return
		case <-poll.C:
			if w.idle() {
				return
			}
		}
	}
}

func memoryRateLimitGuard(home string, cfg *appcfg.Root, agentType string) func(context.Context, int) bool {
	provider := resolveMemoryLLMProvider(cfg, agentType, "")
	if provider == nil || !strings.EqualFold(strings.TrimSpace(provider.Provider), "chatgpt") {
		return nil
	}
	return func(ctx context.Context, minRemaining int) bool {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, openai.UsageURL, nil)
		if err != nil {
			return true
		}
		client := &http.Client{Transport: &openai.Transport{Path: openai.CredentialsPath(home)}, Timeout: 10 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return true
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return true
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			return true
		}
		var usage struct {
			RateLimitReachedType json.RawMessage `json:"rate_limit_reached_type"`
			RateLimit            *struct {
				Primary *struct {
					UsedPercent float64 `json:"used_percent"`
				} `json:"primary_window"`
				Secondary *struct {
					UsedPercent float64 `json:"used_percent"`
				} `json:"secondary_window"`
			} `json:"rate_limit"`
		}
		if json.Unmarshal(body, &usage) != nil {
			return true
		}
		reached := strings.TrimSpace(string(usage.RateLimitReachedType))
		if reached != "" && reached != "null" {
			return false
		}
		maxUsed := float64(100 - max(0, min(100, minRemaining)))
		return usage.RateLimit == nil ||
			(usage.RateLimit.Primary == nil || usage.RateLimit.Primary.UsedPercent <= maxUsed) &&
				(usage.RateLimit.Secondary == nil || usage.RateLimit.Secondary.UsedPercent <= maxUsed)
	}
}

// buildMemoryLLM resolves the LLM client for an internal memory operation.
// An explicit model is used when configured; otherwise it falls back to the
// default provider chain for agentType. Internal memory
// agents do NOT go through the normal LLM wrapper chain, so the memory read-path
// and citation wrappers are never applied, preventing recursion.
func buildMemoryLLM(cfg *appcfg.Root, agentType, model, effort string) llm.LLM {
	provider := resolveMemoryLLMProvider(cfg, agentType, model)
	if provider == nil {
		return nil
	}
	provider.Params = memoryReasoningParams(provider.Params, effort)
	client, err := NewLLMFromYAML(provider)
	if err != nil {
		slog.Debug("memory llm unavailable", "model", provider.Model, "err", err)
		return nil
	}
	return client
}

func resolveMemoryLLMProvider(cfg *appcfg.Root, agentType, model string) *LLMProviderYAML {
	providers := resolvedAgentProviders(cfg, agentType)
	if len(providers) == 0 {
		return nil
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return llmYAMLsFromResolved(providers)[0]
	}
	for _, provider := range providers {
		if strings.EqualFold(strings.TrimSpace(provider.Model), model) {
			return llmYAMLsFromResolved([]appcfg.AgentLLMProviderConfig{provider})[0]
		}
	}
	providerName := ProviderForAgentModel(cfg, agentType, model)
	for _, provider := range providers {
		if strings.EqualFold(strings.TrimSpace(provider.Provider), providerName) {
			provider.Model = model
			return llmYAMLsFromResolved([]appcfg.AgentLLMProviderConfig{provider})[0]
		}
	}
	return nil
}

func memoryReasoningParams(params appcfg.LLMRequestParams, effort string) appcfg.LLMRequestParams {
	values := map[string]any{}
	if len(params) > 0 {
		_ = json.Unmarshal(params.Bytes(), &values)
	}
	reasoning, _ := values["reasoning"].(map[string]any)
	if reasoning == nil {
		reasoning = map[string]any{}
	}
	reasoning["effort"] = strings.TrimSpace(effort)
	values["reasoning"] = reasoning
	encoded, _ := json.Marshal(values)
	return appcfg.LLMRequestParams(encoded)
}

// memoryStage1RolloutTokenLimit keeps the stage-1 extraction input under
// memory.Stage1RolloutAbsoluteCap. See that constant for why a window-fraction
// budget (95% × 70%) is no longer allowed to size this one-shot, structurally
// uncacheable call by the model's context window.
func memoryStage1RolloutTokenLimit(cfg *appcfg.Root, agentType, model string) int {
	provider := resolveMemoryLLMProvider(cfg, agentType, model)
	if provider == nil {
		return memory.Stage1RolloutAbsoluteCap
	}
	info, ok := llm.Lookup(provider.Provider, provider.Model)
	if !ok || info.ContextWindow <= 0 {
		return memory.Stage1RolloutAbsoluteCap
	}
	limit := info.ContextWindow * 95 / 100 * 70 / 100
	if limit < 1 {
		return 1
	}
	return int(min(memory.Stage1RolloutAbsoluteCap, limit))
}

type memoryConsolidationRunner struct {
	client llm.LLM
}

func (r *memoryConsolidationRunner) RunMemoryConsolidation(ctx context.Context, root memory.Root, prompt memory.ConsolidationPrompt) error {
	if r == nil || r.client == nil {
		return fmt.Errorf("consolidation llm unavailable")
	}
	worker, err := agent.New(r.client, "memory-consolidation", strings.TrimSpace(prompt.Instruction))
	if err != nil {
		return fmt.Errorf("create consolidation agent: %w", err)
	}
	state := tool.NewState(root.MemoryRoot)
	// The prompt names the artifacts by their path inside the memory folder
	// (MEMORY.md, skills/<name>/SKILL.md), and this run is policy-approved with
	// no operator to ask, so the file tools must treat that folder as the whole
	// filesystem. Without confinement those names resolve against the CLI's
	// working directory and the consolidation output lands in whatever repo the
	// session was started in.
	state.ConfineToRoot(root.MemoryRoot)
	readTool, err := tool.NewFileReadTool(state)
	if err != nil {
		return err
	}
	writeTool, err := tool.NewFileWriteTool(state, &tool.AgentToolRuntime{WorkspaceRoot: root.MemoryRoot, YOLO: true})
	if err != nil {
		return err
	}
	editTool, err := tool.NewFileEditTool(state)
	if err != nil {
		return err
	}
	for _, tool := range []*llm.Tool{readTool, writeTool, editTool} {
		if err := worker.AddTool(tool); err != nil {
			return err
		}
	}
	workerCtx := tool.WithPolicyApproved(ctx, true)
	// The pass's own values ride in the opening user message, behind the
	// instruction, so the instruction stays byte-identical across projects and
	// passes and keeps being served from the provider's cached prefix.
	opening := strings.TrimSpace(prompt.Context)
	if opening != "" {
		opening += "\n\n"
	}
	opening += "Consolidate the changed memory workspace now."
	result, err := worker.Run(workerCtx, llm.Text(opening))
	if err != nil {
		return err
	}
	if result == nil {
		return fmt.Errorf("consolidation agent returned no result")
	}
	return nil
}

// LaunchMemoryStartup starts the background memory pipeline for the given
// session asynchronously. It is a no-op when the memory feature is disabled,
// a pipeline cannot be built, or a pipeline pass is already running.
func (r *Runner) LaunchMemoryStartup(sessionID string) {
	if r == nil {
		return
	}
	if !memory.Enabled(r.AppCfg) {
		return
	}
	p := r.memPipeline.Get(r.buildMemoryPipeline)
	if p == nil {
		return
	}
	p.LaunchAsync(strings.TrimSpace(sessionID))
}

// ErrMemoriesDisabled reports a consolidation attempt while the memory feature
// is switched off. Callers surface it verbatim: it distinguishes "nothing was
// consolidated and that is the configuration's answer" from a failed pass.
var ErrMemoriesDisabled = errors.New("memories are disabled")

// ConsolidateMemoriesNow runs one synchronous memory-consolidation pass for
// sessionID on this runner's pipeline (its project scope plus the global
// scope), so a caller that just wrote notes to disk can report the real
// outcome instead of "triggered". It returns ErrMemoriesDisabled when the
// feature is off and an error when no pipeline can be built; a nil return
// means the pass ran to completion.
func (r *Runner) ConsolidateMemoriesNow(ctx context.Context, sessionID string) error {
	if r == nil {
		return errors.New("runner unavailable")
	}
	if !memory.Enabled(r.AppCfg) {
		return ErrMemoriesDisabled
	}
	p := r.memPipeline.Get(r.buildMemoryPipeline)
	if p == nil {
		return errors.New("memory pipeline unavailable")
	}
	return p.Run(ctx, strings.TrimSpace(sessionID))
}

// memoryInstructionLLM injects the per-turn memory instruction: what the store
// already knows and how to use it, plus which user instructions this turn must
// be written back as new memory.
type memoryInstructionLLM struct {
	inner llm.LLM
	// workspaceRoot is the primary agent's own root. The injected summary is
	// tenant data, so it must be resolved per agent, never from the shared home.
	workspaceRoot string
	// projectKey is this runtime's project scope (memory.ProjectKey), empty
	// when it has none. It selects which project's memory is recalled and
	// captured, alongside the agent's single global scope.
	projectKey string
	// projectOnly reports the launch project's memory scope: true means the
	// session recalls and captures nothing outside its own project scope. It
	// participates in the freeze key below, so flipping it requires a new
	// session — the recall section sits in the cached prefix.
	projectOnly bool
	cfg         *appcfg.Root

	// sessions is where the rendered instruction is frozen for the life of a
	// session. See instructionForSession for why it must never be re-rendered
	// mid-session, and why the freeze belongs to the session rather than to
	// this process.
	sessions *state.SessionStore

	// mu guards byVariant, the freeze used by a runtime that keeps no session
	// state at all — a hook, an isolated run. It holds one entry per
	// instruction variant rather than one per session, so it is bounded by the
	// kinds of run a runtime can make instead of by how many it has made.
	mu        sync.Mutex
	byVariant map[string]string
}

func wrapMemoryInstructionLLM(inner llm.LLM, workspaceRoot, projectKey string, cfg *appcfg.Root, sessions *state.SessionStore, projectOnly bool) llm.LLM {
	if inner == nil || memory.InstructionOptionsFromConfig(cfg).Empty() {
		return inner
	}
	return &memoryInstructionLLM{inner: inner, workspaceRoot: workspaceRoot, projectKey: strings.TrimSpace(projectKey), projectOnly: projectOnly, cfg: cfg, sessions: sessions}
}

// memoryCaptureAllowedForSource reports whether a run of this kind can hold the
// user instruction that capture is for.
//
// A subagent's "user" messages are prompts its parent wrote, and compaction and
// hook runs carry no user turn at all. Asking those runs to capture durable
// user rules can only produce notes about task text — pollution the store has
// no way to tell apart from the real thing. An unrecognized source is treated
// as a user-facing turn: the cost of a stray note is far smaller than silently
// never capturing on a surface this list does not know about.
func memoryCaptureAllowedForSource(source string) bool {
	source = strings.ToLower(strings.TrimSpace(source))
	if strings.HasPrefix(source, "agent:") {
		return false
	}
	switch source {
	case "compact", "hook_agent":
		return false
	default:
		return true
	}
}

func (w *memoryInstructionLLM) Execute(ctx context.Context, messages []llm.Message, tools []*llm.Tool) (*llm.Result, error) {
	if w == nil || w.inner == nil {
		return nil, nil
	}
	opts := memory.InstructionOptionsFromConfig(w.cfg)
	opts.Capture = opts.Capture && memoryCaptureAllowedForSource(QuerySourceFromContext(ctx))
	opts.ProjectOnly = w.projectOnly
	projectKey := w.projectKey
	if ctxProjectKey := tool.ProjectKeyFromContext(ctx); ctxProjectKey != "" {
		projectKey = ctxProjectKey
	}
	instruction := w.instructionForSession(ctx, projectKey, opts)
	if instruction != "" {
		messages = memory.InjectDeveloperInstruction(messages, instruction)
	}
	return w.inner.Execute(ctx, messages, tools)
}

// instructionForSession renders the instruction once per session and returns
// that exact string for every later call.
//
// This message is injected ahead of the whole conversation, so providers only
// serve a cached prefix while it stays byte-identical. Re-rendering per call
// would read the summary and the notes directory live and change the message
// the moment a note is captured or a consolidation pass lands — invalidating
// the cached system prompt, tool definitions, and every prior turn, for content
// the session can already see in its own transcript. Freshness is worth nothing
// here and costs the entire prefix, so a value that appears mid-session waits
// for the next one.
//
// The options are part of the key because one session can drive runs of
// different kinds: a subagent run under the same session must not be served the
// main thread's capture rules, or vice versa. The project key is part of it for
// the same reason: a subagent pinned to a different launch directory than its
// parent must not be served its parent's project memory.
func (w *memoryInstructionLLM) instructionForSession(ctx context.Context, projectKey string, opts memory.InstructionOptions) string {
	variant := memoryInstructionVariantKey(projectKey, opts)
	sessionID := strings.TrimSpace(llm.AgentSessionIDFromContext(ctx))
	if sessionID == "" || w.sessions == nil {
		return w.instructionForVariant(variant, projectKey, opts)
	}
	key := sessionPromptStateMemoryInstruction + "|" + variant
	if frozen, ok, err := w.sessions.SessionPromptState(ctx, sessionID, key); err == nil && ok {
		return frozen
	} else if err != nil {
		slog.Debug("memory instruction: read frozen instruction", "session", sessionID, "err", err)
	}
	frozen, ok, err := w.sessions.FreezeSessionPromptState(ctx, sessionID, key, w.renderInstruction(projectKey, opts))
	if err != nil {
		slog.Debug("memory instruction: freeze instruction", "session", sessionID, "err", err)
	}
	if err != nil || !ok {
		// Nothing was frozen — there is no session row to hang it on, or the
		// store refused. Returning the render that just happened would render
		// again on the next request, and this message sits ahead of the whole
		// conversation: a value that changes the moment a note is captured
		// re-bills the tool definitions, the system prompt and every prior turn.
		return w.instructionForVariant(variant, projectKey, opts)
	}
	return frozen
}

// instructionForVariant renders once per variant and keeps that answer, for
// runs with no session to freeze against.
func (w *memoryInstructionLLM) instructionForVariant(variant, projectKey string, opts memory.InstructionOptions) string {
	w.mu.Lock()
	if cached, ok := w.byVariant[variant]; ok {
		w.mu.Unlock()
		return cached
	}
	w.mu.Unlock()

	instruction := w.renderInstruction(projectKey, opts)

	w.mu.Lock()
	defer w.mu.Unlock()
	// A concurrent call may have rendered first; its value is the one already
	// on the wire, so keep it rather than replacing it with an equivalent-but-
	// possibly-newer render.
	if cached, ok := w.byVariant[variant]; ok {
		return cached
	}
	if w.byVariant == nil {
		w.byVariant = make(map[string]string)
	}
	w.byVariant[variant] = instruction
	return instruction
}

func (w *memoryInstructionLLM) renderInstruction(projectKey string, opts memory.InstructionOptions) string {
	roots, err := memory.ResolveRootsForAgent(w.workspaceRoot)
	if err != nil {
		slog.Debug("memory instruction root unavailable", "err", err)
		return ""
	}
	instruction, err := memory.RenderTurnInstruction(roots, projectKey, opts)
	if err != nil {
		slog.Debug("memory instruction unavailable", "err", err)
		return ""
	}
	return instruction
}

func memoryInstructionVariantKey(projectKey string, opts memory.InstructionOptions) string {
	flags := []byte("----")
	if opts.Recall {
		flags[0] = 'r'
	}
	if opts.Capture {
		flags[1] = 'c'
	}
	if opts.DedicatedTools {
		flags[2] = 't'
	}
	if opts.ProjectOnly {
		flags[3] = 'p'
	}
	return string(flags) + "|" + strings.TrimSpace(projectKey)
}

// memoryCitationLLM strips hidden <oai-mem-citation> markup from assistant
// output before it reaches the transcript/UI and records memory usage for the
// cited sessions. It is gated on the read path being enabled: citations only
// appear when memories were surfaced to the model.
type memoryCitationLLM struct {
	inner llm.LLM
	cfg   *appcfg.Root
	store *memory.Store
}

func wrapMemoryCitationLLM(inner llm.LLM, cfg *appcfg.Root, store *memory.Store) llm.LLM {
	if inner == nil || store == nil || !memory.ReadPathEnabled(cfg) {
		return inner
	}
	return &memoryCitationLLM{inner: inner, cfg: cfg, store: store}
}

func (w *memoryCitationLLM) Execute(ctx context.Context, messages []llm.Message, tools []*llm.Tool) (*llm.Result, error) {
	if w == nil || w.inner == nil {
		return nil, nil
	}
	var streamFilter *memory.CitationStreamFilter
	var downstreamDelta func(string)
	if sink := llm.StreamSinkFrom(ctx); sink != nil && sink.OnDelta != nil {
		wrapped := *sink
		streamFilter = &memory.CitationStreamFilter{}
		downstreamDelta = sink.OnDelta
		wrapped.OnDelta = func(delta string) {
			if visible := streamFilter.Push(delta); visible != "" {
				downstreamDelta(visible)
			}
		}
		ctx = llm.WithStreamSink(ctx, &wrapped)
	}
	res, err := w.inner.Execute(ctx, messages, tools)
	if streamFilter != nil && downstreamDelta != nil {
		if visible := streamFilter.Finish(); visible != "" {
			downstreamDelta(visible)
		}
	}
	if err != nil || res == nil {
		return res, err
	}
	ids := map[string]struct{}{}
	if res.Message != nil {
		stripMessageCitations(res.Message, ids)
	}
	for i := range res.Session {
		if res.Session[i].Role == llm.RoleAssistant {
			stripMessageCitations(&res.Session[i], ids)
		}
	}
	if len(ids) > 0 {
		sessionIDs := make([]string, 0, len(ids))
		for id := range ids {
			sessionIDs = append(sessionIDs, id)
		}
		_ = w.store.UpdateUsage(ctx, sessionIDs, 0)
	}
	return res, err
}

// stripMessageCitations removes citation markup from every text part of msg and
// collects the referenced session IDs into out.
func stripMessageCitations(msg *llm.Message, out map[string]struct{}) {
	if msg == nil {
		return
	}
	for i := range msg.Parts {
		if msg.Parts[i].Type != llm.ContentTypeText {
			continue
		}
		stripped, citation := memory.StripAndParseCitations(msg.Parts[i].Text)
		msg.Parts[i].Text = stripped
		if citation != nil {
			mergeMessageCitation(msg, citation)
			for _, id := range memory.ValidMemoryCitationThreadIDs(citation) {
				out[id] = struct{}{}
			}
		}
	}
}

func mergeMessageCitation(msg *llm.Message, citation *memory.MemoryCitation) {
	if msg == nil || citation == nil {
		return
	}
	if msg.MemoryCitation == nil {
		msg.MemoryCitation = &llm.MemoryCitation{}
	}
	for _, entry := range citation.Entries {
		msg.MemoryCitation.Entries = append(msg.MemoryCitation.Entries, llm.MemoryCitationEntry{
			Path: entry.Path, LineStart: entry.LineStart, LineEnd: entry.LineEnd, Note: entry.Note,
		})
	}
	seen := make(map[string]struct{}, len(msg.MemoryCitation.RolloutIDs))
	for _, id := range msg.MemoryCitation.RolloutIDs {
		seen[id] = struct{}{}
	}
	for _, id := range citation.RolloutIDs {
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		msg.MemoryCitation.RolloutIDs = append(msg.MemoryCitation.RolloutIDs, id)
	}
}
