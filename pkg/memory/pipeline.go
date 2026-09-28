package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

type TranscriptSource interface {
	ListMemoryTranscriptMessages(ctx context.Context, threadID string, limit int) ([]llm.Message, error)
}

type ConsolidationRunner interface {
	RunMemoryConsolidation(ctx context.Context, root Root, prompt ConsolidationPrompt) error
}

type Pipeline struct {
	Store *Store
	Roots Roots
	// SessionProjectKey is the current session's project scope
	// (ProjectKey of its launch cwd), empty when the session has no
	// project identity. It selects which project's phase-2 pass this run
	// consolidates, alongside the agent's single global scope — never every
	// project the agent has ever seen, which is what keeps one startup's cost
	// independent of how many projects this agent has touched.
	SessionProjectKey       string
	Cfg                     *appcfg.Root
	Transcripts             TranscriptSource
	ExtractLLM              llm.LLM
	Consolidator            ConsolidationRunner
	RateLimitGuard          func(context.Context, int) bool
	Stage1RolloutTokenLimit int
}

// phase2Scopes lists the scopes this run's phase-2 step consolidates: the
// session's own project (when it has one) and the agent's global scope.
func (p *Pipeline) phase2Scopes() []Scope {
	scopes := make([]Scope, 0, 2)
	if scope, ok := ProjectScope(p.SessionProjectKey); ok {
		scopes = append(scopes, scope)
	}
	scopes = append(scopes, GlobalScope())
	return scopes
}

func (p *Pipeline) LaunchAsync(currentThreadID string) {
	if p == nil || p.Store == nil || p.Transcripts == nil || !Enabled(p.Cfg) {
		return
	}
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.Error("memory pipeline panic", "error", recovered)
			}
		}()
		if err := p.Run(context.Background(), strings.TrimSpace(currentThreadID)); err != nil {
			slog.Debug("memory startup ended", "error", err)
		}
	}()
}

func (p *Pipeline) Run(ctx context.Context, currentThreadID string) error {
	if p == nil || p.Store == nil || !Enabled(p.Cfg) {
		return nil
	}
	scopes := p.phase2Scopes()
	for _, scope := range scopes {
		root := p.Roots.Scope(scope)
		if err := os.MkdirAll(root.MemoryRoot, 0o755); err != nil {
			return fmt.Errorf("create memories root: %w", err)
		}
		if err := seedAdHocInstructions(root.MemoryRoot); err != nil {
			slog.Warn("seed memory extension instructions", "scope", scope.ID(), "error", err)
		}
	}
	settings := p.Cfg.EffectiveMemories()
	if _, err := p.Store.PruneStage1Outputs(ctx, settings.MaxUnusedDays, stage1PruneBatchSize); err != nil {
		slog.Warn("prune unused raw memories", "error", err)
	}
	if versions, err := p.Store.ListStage1Versions(ctx); err == nil {
		pruneRolloutEvidence(p.Roots, versions)
	} else {
		slog.Warn("prune rollout evidence", "error", err)
	}
	if p.RateLimitGuard != nil && !p.RateLimitGuard(ctx, settings.MinRateLimitRemainingPercent) {
		return nil
	}
	claims, err := p.Store.ClaimStage1JobsForStartup(ctx, currentThreadID,
		settings.MaxRolloutAgeDays, settings.MinRolloutIdleHours, settings.MaxRolloutsPerStartup)
	if err != nil {
		return fmt.Errorf("claim stage-one jobs: %w", err)
	}
	p.runStage1Jobs(ctx, claims)
	for _, scope := range scopes {
		if err := p.runStage2(ctx, scope, settings); err != nil {
			slog.Debug("memory consolidation ended", "scope", scope.ID(), "error", err)
		}
	}
	return nil
}

func (p *Pipeline) runStage1Jobs(ctx context.Context, claims []Stage1JobClaim) {
	if len(claims) == 0 {
		return
	}
	// Run the first claim alone before fanning out. Every stage-1 request
	// shares one byte-identical system prefix and differs only in its
	// transcript tail, so the first completed call writes that shared prefix
	// into the provider's cache; the concurrent workers that follow start
	// warm instead of each paying their own cold miss on the same bytes.
	if err := p.runStage1(ctx, claims[0]); err != nil {
		slog.Debug("memory extraction failed", "thread", claims[0].Thread.ThreadID, "error", err)
	}
	claims = claims[1:]
	if len(claims) == 0 {
		return
	}
	jobs := make(chan Stage1JobClaim)
	var workers sync.WaitGroup
	workerCount := len(claims)
	if workerCount > stage1ConcurrencyLimit {
		workerCount = stage1ConcurrencyLimit
	}
	for range workerCount {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for claim := range jobs {
				if err := p.runStage1(ctx, claim); err != nil {
					slog.Debug("memory extraction failed", "thread", claim.Thread.ThreadID, "error", err)
				}
			}
		}()
	}
	for _, claim := range claims {
		select {
		case jobs <- claim:
		case <-ctx.Done():
			close(jobs)
			workers.Wait()
			return
		}
	}
	close(jobs)
	workers.Wait()
}

type stage1Result struct {
	RawMemory      string  `json:"raw_memory"`
	RolloutSummary string  `json:"rollout_summary"`
	RolloutSlug    *string `json:"rollout_slug"`
}

func stage1OutputSpec() llm.StructuredOutputSpec {
	return llm.StructuredOutputSpec{
		Name: "memory_stage1_output", Strict: true,
		Description: "Raw memory and rollout summary extracted from one thread.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"rollout_summary": map[string]any{"type": "string"},
				"rollout_slug":    map[string]any{"type": []any{"string", "null"}},
				"raw_memory":      map[string]any{"type": "string"},
			},
			"required": []any{"rollout_summary", "rollout_slug", "raw_memory"}, "additionalProperties": false,
		},
	}
}

// runStage1 extracts one thread's raw memory. Threads claimed in the same
// batch can belong to different projects — ClaimStage1JobsForStartup scans
// this agent's idle conversations regardless of which project they were
// checked out in — so the thread's own project scope (decided when it was
// claimed, not the current session's) is what everything here, including where
// its rollout evidence lives, resolves against.
func (p *Pipeline) runStage1(ctx context.Context, claim Stage1JobClaim) error {
	scope, ok := ProjectScope(claim.Thread.ProjectKey)
	if !ok {
		return fmt.Errorf("stage-one claim for thread %s carries no project scope", claim.Thread.ThreadID)
	}
	projectKey := scope.Key
	root := p.Roots.Scope(scope)
	if p.ExtractLLM == nil {
		err := fmt.Errorf("extraction model unavailable")
		_, _ = p.Store.MarkStage1Failed(ctx, claim.Thread.ThreadID, claim.OwnershipToken, err.Error())
		return err
	}
	messages, err := p.Transcripts.ListMemoryTranscriptMessages(ctx, claim.Thread.ThreadID, 0)
	if err != nil {
		_, _ = p.Store.MarkStage1Failed(ctx, claim.Thread.ThreadID, claim.OwnershipToken, err.Error())
		return err
	}
	serialized, err := serializeTranscriptForMemories(messages)
	if err != nil {
		_, _ = p.Store.MarkStage1Failed(ctx, claim.Thread.ThreadID, claim.OwnershipToken, err.Error())
		return err
	}
	rolloutLimit := p.Stage1RolloutTokenLimit
	if rolloutLimit < 1 {
		rolloutLimit = Stage1RolloutAbsoluteCap
	}
	rolloutPath := root.rolloutEvidencePath(claim.Thread.ThreadID, claim.Thread.UpdatedAt)
	input := renderStageOneInput(claim.Thread, rolloutPath, truncateTokens(serialized, rolloutLimit))
	structured, ok := p.ExtractLLM.(llm.StructuredOutputLLM)
	if !ok {
		err = fmt.Errorf("extraction model does not support strict structured output")
		_, _ = p.Store.MarkStage1Failed(ctx, claim.Thread.ThreadID, claim.OwnershipToken, err.Error())
		return err
	}
	result, err := structured.ExecuteStructured(ctx, []llm.Message{
		llm.SystemMessage(stageOneSystem),
		{Role: llm.RoleUser, Parts: []llm.ContentPart{llm.Text(input)}},
	}, stage1OutputSpec())
	if err != nil || result == nil || result.Message == nil {
		if err == nil {
			err = fmt.Errorf("empty extraction result")
		}
		_, _ = p.Store.MarkStage1Failed(ctx, claim.Thread.ThreadID, claim.OwnershipToken, err.Error())
		return err
	}
	parsed, err := parseStage1(result.Message.TextContent())
	if err != nil {
		_, _ = p.Store.MarkStage1Failed(ctx, claim.Thread.ThreadID, claim.OwnershipToken, err.Error())
		return err
	}
	parsed.RawMemory = redactSecrets(parsed.RawMemory)
	parsed.RolloutSummary = redactSecrets(parsed.RolloutSummary)
	if parsed.RolloutSlug != nil {
		redacted := redactSecrets(*parsed.RolloutSlug)
		parsed.RolloutSlug = &redacted
	}
	if parsed.RawMemory == "" || parsed.RolloutSummary == "" {
		var updated bool
		updated, err = p.Store.MarkStage1SucceededNoOutput(ctx, claim.Thread.ThreadID, claim.OwnershipToken)
		if err == nil && updated {
			removeThreadEvidence(root, claim.Thread.ThreadID, "")
		}
		return err
	}
	stagedPath, canonicalPath, err := stageRolloutEvidence(root, claim.Thread, claim.OwnershipToken, messages)
	if err != nil {
		_, _ = p.Store.MarkStage1Failed(ctx, claim.Thread.ThreadID, claim.OwnershipToken, err.Error())
		return err
	}
	owned, err := p.Store.CompleteStage1WithEvidence(ctx, claim.Thread.ThreadID, projectKey, claim.OwnershipToken,
		claim.Thread.UpdatedAt, parsed.RawMemory, parsed.RolloutSummary, parsed.RolloutSlug, stagedPath, canonicalPath)
	if err != nil {
		return err
	}
	if !owned {
		return nil
	}
	removeThreadEvidence(root, claim.Thread.ThreadID, canonicalPath)
	return nil
}

func serializeTranscriptForMemories(messages []llm.Message) (string, error) {
	filtered := make([]llm.Message, 0, len(messages))
	for _, message := range messages {
		if message.Role == llm.RoleDeveloper || message.Compaction != nil {
			continue
		}
		if message.Role == llm.RoleUser {
			parts := message.Parts[:0:0]
			for _, part := range message.Parts {
				text := strings.TrimSpace(part.Text)
				lower := strings.ToLower(text)
				if (strings.HasPrefix(lower, "# agents.md instructions") && strings.HasSuffix(lower, "</instructions>")) ||
					(strings.HasPrefix(lower, "<skill>") && strings.HasSuffix(lower, "</skill>")) {
					continue
				}
				parts = append(parts, part)
			}
			message.Parts = parts
			if len(parts) == 0 && strings.TrimSpace(message.TextContent()) == "" {
				continue
			}
		}
		message.IsMeta = false
		message.Ephemeral = false
		message.ToolExecutionTiming = nil
		message.ToolDisplay = nil
		message.MemoryCitation = nil
		filtered = append(filtered, message)
	}
	data, err := json.Marshal(filtered)
	if err != nil {
		return "", fmt.Errorf("serialize thread memory: %w", err)
	}
	return redactSecrets(string(data)), nil
}

func renderStageOneInput(thread SessionCandidate, rolloutPath, transcript string) string {
	return renderTemplate(stageOneInput, map[string]string{
		"rollout_path":     rolloutPath,
		"rollout_cwd":      thread.Cwd,
		"rollout_contents": transcript,
	})
}

func truncateTokens(value string, limit int) string {
	return MiddleTokens(value, limit)
}

func parseStage1(text string) (stage1Result, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return stage1Result{}, fmt.Errorf("parse stage-one output: empty result")
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &keys); err != nil {
		return stage1Result{}, fmt.Errorf("parse stage-one output: %w", err)
	}
	for _, key := range []string{"raw_memory", "rollout_summary", "rollout_slug"} {
		if _, ok := keys[key]; !ok {
			return stage1Result{}, fmt.Errorf("parse stage-one output: missing %s", key)
		}
	}
	var output stage1Result
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&output); err != nil {
		return stage1Result{}, fmt.Errorf("parse stage-one output: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return stage1Result{}, fmt.Errorf("parse stage-one output: trailing content")
	}
	return output, nil
}

// runStage2 runs one scope's consolidation pass. For a project scope the
// input is that project's selected stage-1 outputs; the global scope has no
// stage-1 outputs of its own (its input is every project's promoted
// candidates — see the global-candidates sync, phase P5), so rows stays empty
// and the pass only picks up whatever ad-hoc global notes or promoted
// candidate files already sit in the global workspace.
func (p *Pipeline) runStage2(ctx context.Context, scope Scope, settings appcfg.MemoriesConfig) error {
	claim, err := p.Store.TryClaimGlobalPhase2(ctx, scope)
	if err != nil || claim.Outcome != Phase2Claimed {
		return err
	}
	root := p.Roots.Scope(scope)
	fail := func(reason string, cause error) error {
		updated, _ := p.Store.MarkGlobalPhase2Failed(ctx, scope, claim.OwnershipToken, reason, false)
		if !updated {
			_, _ = p.Store.MarkGlobalPhase2Failed(ctx, scope, claim.OwnershipToken, reason, true)
		}
		return cause
	}
	if err := prepareMemoryWorkspace(ctx, root.MemoryRoot); err != nil {
		return fail("failed_prepare_workspace", err)
	}
	var rows []Stage1Output
	watermark := claim.InputWatermark
	hasInput := false
	if scope.Kind == ScopeProject {
		rows, err = p.Store.SelectStage1ForPhase2(ctx, scope.Key, settings.MaxRawMemoriesForConsolidation, settings.MaxUnusedDays)
		if err != nil {
			return fail("failed_load_stage1_outputs", err)
		}
		for index := range rows {
			rows[index].RolloutPath = root.rolloutEvidencePath(rows[index].ThreadID, rows[index].SourceUpdatedAt)
		}
		for _, row := range rows {
			if row.SourceUpdatedAt > watermark {
				watermark = row.SourceUpdatedAt
			}
		}
		if err := syncPhase2WorkspaceInputs(root.MemoryRoot, rows); err != nil {
			return fail("failed_sync_workspace_inputs", err)
		}
		hasInput = len(rows) > 0
	} else {
		hasInput, err = syncGlobalPhase2Inputs(p.Roots, root.MemoryRoot)
		if err != nil {
			return fail("failed_sync_workspace_inputs", err)
		}
	}
	// A scope with no input and nothing already stored has nothing for the
	// agent to do, and the generated input files alone ("No raw memories yet.")
	// are enough of a diff to look like it does. Spending a consolidation call
	// on that would also leave behind a summary of nothing — which is then
	// injected into every turn of that project forever, and would make a brand
	// new project's first session look like it had recalled memory. A scope
	// that already holds artifacts is not skipped even with no input: the pass
	// is how stored memory gets pruned once its evidence goes away.
	//
	// The consolidation watermark is deliberately not advanced here. It records
	// which notes a pass folded in, and this pass folded in nothing; moving it
	// would mark a note written in the same instant as already consolidated.
	if !hasInput && !hasExtensionInput(root.MemoryRoot) && !consolidationArtifactsExist(root.MemoryRoot) {
		_, err := p.Store.MarkGlobalPhase2Succeeded(ctx, scope, claim.OwnershipToken, watermark, rows)
		return err
	}
	// Everything the pass consolidates is decided here, by this diff. Notes
	// written from a turn after this instant are not in it, so this — not the
	// finish time — is what tells a later turn which notes are still pending.
	//
	// The second of slack absorbs filesystem mtime granularity: comparing a
	// note's mtime against a same-second snapshot can only err in one direction,
	// and re-consolidating a note is harmless where losing one is not.
	snapshot := time.Now().Add(-time.Second)
	diff, changed, err := memoryWorkspaceDiff(ctx, root.MemoryRoot)
	if err != nil {
		return fail("failed_workspace_status", err)
	}
	if !changed && validateConsolidationArtifacts(root.MemoryRoot) == nil {
		if err := markConsolidationStart(root, snapshot); err != nil {
			slog.Warn("record memory consolidation marker", "error", err)
		}
		_, err := p.Store.MarkGlobalPhase2Succeeded(ctx, scope, claim.OwnershipToken, watermark, rows)
		return err
	}
	if err := writeMemoryWorkspaceDiff(root.MemoryRoot, diff); err != nil {
		return fail("failed_workspace_diff_file", err)
	}
	if p.Consolidator == nil {
		return fail("failed_spawn_agent", fmt.Errorf("consolidation model unavailable"))
	}
	agentCtx, cancel := context.WithCancel(ctx)
	heartbeatDone := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(phase2HeartbeatSeconds * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-agentCtx.Done():
				heartbeatDone <- nil
				return
			case <-ticker.C:
				owned, heartbeatErr := p.Store.HeartbeatGlobalPhase2(agentCtx, scope, claim.OwnershipToken)
				if heartbeatErr != nil || !owned {
					if heartbeatErr == nil {
						heartbeatErr = fmt.Errorf("lost consolidation ownership")
					}
					heartbeatDone <- heartbeatErr
					cancel()
					return
				}
			}
		}
	}()
	prompt := buildConsolidationPrompt(root.MemoryRoot, scope)
	agentErr := p.Consolidator.RunMemoryConsolidation(agentCtx, root, prompt)
	cancel()
	heartbeatErr := <-heartbeatDone
	if agentErr != nil {
		return fail("failed_agent", agentErr)
	}
	if heartbeatErr != nil {
		return fail("failed_confirm_ownership", heartbeatErr)
	}
	if err := validateConsolidationArtifacts(root.MemoryRoot); err != nil {
		return fail("failed_invalid_artifacts", err)
	}
	owned, err := p.Store.HeartbeatGlobalPhase2(ctx, scope, claim.OwnershipToken)
	if err != nil || !owned {
		if err == nil {
			err = fmt.Errorf("lost consolidation ownership")
		}
		return fail("failed_confirm_ownership", err)
	}
	if err := resetMemoryWorkspaceBaseline(ctx, root.MemoryRoot, snapshot); err != nil {
		return fail("failed_workspace_commit", err)
	}
	if err := markConsolidationStart(root, snapshot); err != nil {
		slog.Warn("record memory consolidation marker", "error", err)
	}
	_, err = p.Store.MarkGlobalPhase2Succeeded(ctx, scope, claim.OwnershipToken, watermark, rows)
	return err
}

func seedAdHocInstructions(root string) error {
	dir := filepath.Join(root, extensionsDir, "ad_hoc")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(dir, "instructions.md"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return nil
		}
		return err
	}
	defer file.Close()
	_, err = file.WriteString(adHocInstructions)
	return err
}

func renderTemplate(source string, values map[string]string) string {
	for key, value := range values {
		source = strings.ReplaceAll(source, "{{ "+key+" }}", value)
		source = strings.ReplaceAll(source, "{{"+key+"}}", value)
	}
	return source
}

// ConsolidationPrompt is a phase-2 pass split along the line a prefix-caching
// provider bills on.
//
// Instruction is the same bytes for every pass of a scope kind, whichever
// project and whichever machine: it is the system prompt, it is ~55 KB, and
// holding it constant is what lets the second pass — and every pass after it,
// in any project — start against a cached prefix instead of paying that whole
// prompt again. Context carries everything that actually varies (the memory
// folder's absolute path, the extensions this pass has) and is delivered in the
// opening user message, after the instruction, where new bytes cost themselves
// once and invalidate nothing ahead of them.
type ConsolidationPrompt struct {
	Instruction string
	Context     string
}

func buildConsolidationPrompt(root string, scope Scope) ConsolidationPrompt {
	template := consolidationProjectInstruction
	if scope.Kind == ScopeGlobal {
		template = consolidationGlobalInstruction
	}
	instruction := renderTemplate(template, map[string]string{
		"phase2_workspace_diff_file": phase2WorkspaceDiffFile,
	})

	context := "Run context:\n\n- $MEMORY_ROOT = " + root + "\n"
	extensionsRoot := filepath.Join(root, extensionsDir)
	if info, err := os.Stat(extensionsRoot); err == nil && info.IsDir() {
		values := map[string]string{"memory_extensions_root": extensionsRoot}
		context += renderTemplate(memoryExtensionsFolderStructure, values)
		if scope.Kind != ScopeGlobal {
			context += renderTemplate(memoryExtensionsPrimaryInputs, values)
		}
	}
	return ConsolidationPrompt{Instruction: instruction, Context: context}
}

// PipelineDeps is what a caller must supply to build a Pipeline. Everything
// here is either owned by this package already or is a port the caller
// implements, so building a pipeline needs no knowledge of how LLM clients are
// constructed.
//
// The LLM fields are functions rather than values because constructing a client
// is the agent runtime's business (provider resolution, auth, per-model
// parameters) and lives above this package. Passing them in is what lets the
// pipeline's assembly live here without pkg/memory importing pkg/run, which
// would be an import cycle: run already imports memory.
type PipelineDeps struct {
	Store         *Store
	Transcripts   TranscriptSource
	Cfg           *appcfg.Root
	WorkspaceRoot string
	ProjectKey    string
	// ExtractLLM builds the client for the per-session extraction stage,
	// ConsolidateRunner the one that rewrites a scope's memory file.
	ExtractLLM        func(settings appcfg.MemoriesConfig) llm.LLM
	ConsolidateRunner func(settings appcfg.MemoriesConfig) ConsolidationRunner
	// Stage1RolloutTokenLimit and RateLimitGuard depend on which provider the
	// extract model resolves to, which again only the runtime knows.
	Stage1RolloutTokenLimit func(settings appcfg.MemoriesConfig) int
	RateLimitGuard          func(settings appcfg.MemoriesConfig) func(context.Context, int) bool
}

// NewPipeline assembles the background memory pipeline, or returns nil when
// memory is disabled, its stores are missing, or the agent's memory roots
// cannot be resolved. A nil Pipeline is safe to launch; it does nothing.
func NewPipeline(deps PipelineDeps) *Pipeline {
	if !Enabled(deps.Cfg) {
		return nil
	}
	if deps.Store == nil || deps.Transcripts == nil {
		return nil
	}
	roots, err := ResolveRootsForAgent(deps.WorkspaceRoot)
	if err != nil {
		slog.Debug("memory startup: resolve root failed", "err", err)
		return nil
	}
	settings := deps.Cfg.EffectiveMemories()
	p := &Pipeline{
		Store:             deps.Store,
		Roots:             roots,
		SessionProjectKey: strings.TrimSpace(deps.ProjectKey),
		Cfg:               deps.Cfg,
		Transcripts:       deps.Transcripts,
	}
	if deps.ExtractLLM != nil {
		p.ExtractLLM = deps.ExtractLLM(settings)
	}
	if deps.ConsolidateRunner != nil {
		p.Consolidator = deps.ConsolidateRunner(settings)
	}
	if deps.Stage1RolloutTokenLimit != nil {
		p.Stage1RolloutTokenLimit = deps.Stage1RolloutTokenLimit(settings)
	}
	if deps.RateLimitGuard != nil {
		p.RateLimitGuard = deps.RateLimitGuard(settings)
	}
	return p
}

// PipelineHolder lazily builds a Pipeline once and caches it, so a session's
// repeated launches share one. Reset drops the cache, which the agent runtime
// does on every config reload since that can change whether memory is enabled
// at all.
//
// Zero value is ready to use.
type PipelineHolder struct {
	mu       sync.Mutex
	pipeline *Pipeline
	built    bool
}

// Get returns the cached pipeline, building it from build() on first use.
// A nil holder returns nil, and a build that yields nil is cached as nil so a
// disabled configuration is not re-examined on every launch.
func (h *PipelineHolder) Get(build func() *Pipeline) *Pipeline {
	if h == nil || build == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.built {
		return h.pipeline
	}
	h.pipeline = build()
	h.built = true
	return h.pipeline
}

// Reset drops the cached pipeline so the next Get rebuilds it.
func (h *PipelineHolder) Reset() {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.pipeline = nil
	h.built = false
	h.mu.Unlock()
}
