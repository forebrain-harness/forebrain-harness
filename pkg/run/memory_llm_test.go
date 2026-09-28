package run

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/hook"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/memory"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/skill"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

// instructionCaptureLLM records the messages a wrapper hands down.
type instructionCaptureLLM struct{ messages []llm.Message }

func (c *instructionCaptureLLM) Execute(_ context.Context, messages []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	c.messages = messages
	return &llm.Result{}, nil
}

func (c *instructionCaptureLLM) developerText() string {
	var b strings.Builder
	for _, message := range c.messages {
		if message.Role == llm.RoleDeveloper {
			b.WriteString(message.TextContent())
		}
	}
	return b.String()
}

const memoryTestProjectKey = "-Test-project"

func memoryTestConfig(use, generate bool) *appcfg.Root {
	cfg := &appcfg.Root{}
	cfg.Features.Memories = boolPtr(true)
	cfg.Memories.UseMemories = boolPtr(use)
	cfg.Memories.GenerateMemories = boolPtr(generate)
	return cfg
}

// A workspace whose memory store is still empty is exactly where the capture
// rules matter: with no summary to inject, an instruction gated on stored
// memory would never reach the model, and the store would stay empty forever.
func TestMemoryInstructionInjectsCaptureRulesWithoutStoredMemory(t *testing.T) {
	inner := &instructionCaptureLLM{}
	wrapped := wrapMemoryInstructionLLM(inner, t.TempDir(), memoryTestProjectKey, memoryTestConfig(true, true), nil, false)
	if _, err := wrapped.Execute(context.Background(), []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(inner.developerText(), "Capturing memory") {
		t.Fatalf("capture rules not injected:\n%s", inner.developerText())
	}
}

// The instruction is injected ahead of the entire conversation, so a single
// changed byte invalidates the cached system prompt, tool definitions, and
// every prior turn. Capturing a note mid-session — or a consolidation pass
// landing — must therefore not change what this turn injects: the session can
// already see the note in its own transcript, and the next session picks it up
// from a freshly rendered instruction.
func TestMemoryInstructionStaysByteStableWithinASession(t *testing.T) {
	workspace := t.TempDir()
	roots, err := memory.ResolveRootsForAgent(workspace)
	if err != nil {
		t.Fatal(err)
	}
	root := roots.Scope(memory.Scope{Kind: memory.ScopeProject, Key: memoryTestProjectKey})
	if err := os.MkdirAll(root.MemoryRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root.MemoryRoot, "memory_summary.md"), []byte("v1\n\n- prefers concise updates"), 0o600); err != nil {
		t.Fatal(err)
	}

	inner := &instructionCaptureLLM{}
	sessions := newPromptStateStore(t)
	for _, id := range []string{"session-1", "session-2"} {
		if err := sessions.Ensure(context.Background(), id, id); err != nil {
			t.Fatal(err)
		}
	}
	wrapped := wrapMemoryInstructionLLM(inner, workspace, memoryTestProjectKey, memoryTestConfig(true, true), sessions, false)
	ctx := llm.WithAgentSessionID(WithQuerySource(context.Background(), "repl_main_thread"), "session-1")
	if _, err := wrapped.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil); err != nil {
		t.Fatal(err)
	}
	first := inner.developerText()
	if !strings.Contains(first, "prefers concise updates") {
		t.Fatalf("summary missing:\n%s", first)
	}

	// The store changes underneath the session: a note is captured and a
	// consolidation pass rewrites the summary.
	notes := filepath.Join(root.MemoryRoot, "extensions", "ad_hoc", "notes")
	if err := os.MkdirAll(notes, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(notes, "2026-08-20T09-00-00-root-cause.md"), []byte("# Root cause only\n\nbody"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root.MemoryRoot, "memory_summary.md"), []byte("v1\n\n- rewritten by consolidation"), 0o600); err != nil {
		t.Fatal(err)
	}

	inner.messages = nil
	if _, err := wrapped.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("hi again"))}, nil); err != nil {
		t.Fatal(err)
	}
	if second := inner.developerText(); second != first {
		t.Fatalf("injected prefix changed mid-session, invalidating the prompt cache:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}

	// A different session renders fresh, so nothing captured is stranded.
	next := &instructionCaptureLLM{}
	fresh := wrapMemoryInstructionLLM(next, workspace, memoryTestProjectKey, memoryTestConfig(true, true), sessions, false)
	nextCtx := llm.WithAgentSessionID(WithQuerySource(context.Background(), "repl_main_thread"), "session-2")
	if _, err := fresh.Execute(nextCtx, []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(next.developerText(), "Root cause only") {
		t.Fatalf("note captured earlier never reached the next session:\n%s", next.developerText())
	}
}

// The session store can refuse the freeze — there is no session row yet, or the
// run belongs to another agent. The value still has to stop changing: this
// message is injected ahead of the whole conversation, so a fresh render per
// request re-bills the tool definitions, the system prompt and every prior turn
// each time a note is captured.
func TestMemoryInstructionStaysByteStableWhenTheFreezeIsRefused(t *testing.T) {
	workspace := t.TempDir()
	roots, err := memory.ResolveRootsForAgent(workspace)
	if err != nil {
		t.Fatal(err)
	}
	root := roots.Scope(memory.Scope{Kind: memory.ScopeProject, Key: memoryTestProjectKey})
	if err := os.MkdirAll(root.MemoryRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root.MemoryRoot, "memory_summary.md"), []byte("v1\n\n- prefers concise updates"), 0o600); err != nil {
		t.Fatal(err)
	}

	inner := &instructionCaptureLLM{}
	// A store with no row for this session: FreezeSessionPromptState stores
	// nothing and reports that nothing was frozen.
	sessions := newPromptStateStore(t)
	wrapped := wrapMemoryInstructionLLM(inner, workspace, memoryTestProjectKey, memoryTestConfig(true, true), sessions, false)
	ctx := llm.WithAgentSessionID(WithQuerySource(context.Background(), "repl_main_thread"), "ghost-session")
	if _, err := wrapped.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil); err != nil {
		t.Fatal(err)
	}
	first := inner.developerText()
	if !strings.Contains(first, "prefers concise updates") {
		t.Fatalf("summary missing:\n%s", first)
	}

	if err := os.WriteFile(filepath.Join(root.MemoryRoot, "memory_summary.md"), []byte("v1\n\n- rewritten by consolidation"), 0o600); err != nil {
		t.Fatal(err)
	}
	inner.messages = nil
	if _, err := wrapped.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("hi again"))}, nil); err != nil {
		t.Fatal(err)
	}
	if second := inner.developerText(); second != first {
		t.Fatalf("injected prefix changed between requests, invalidating the prompt cache:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
}

// A subagent's user messages are prompts written by its parent, and compaction
// and hook runs have no user turn at all. Capture rules there can only produce
// notes about task text, which the store cannot tell apart from a real user
// rule.
func TestMemoryInstructionSkipsCaptureOffTheMainThread(t *testing.T) {
	for _, source := range []string{"agent:builtin:explore", "agent:custom", "agent:default", "compact", "hook_agent"} {
		inner := &instructionCaptureLLM{}
		wrapped := wrapMemoryInstructionLLM(inner, t.TempDir(), memoryTestProjectKey, memoryTestConfig(true, true), nil, false)
		ctx := WithQuerySource(context.Background(), source)
		if _, err := wrapped.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(inner.developerText(), "Capturing memory") {
			t.Fatalf("capture rules injected for %q:\n%s", source, inner.developerText())
		}
	}
	// An unknown source must keep capturing: a surface this list does not name
	// is still likely to be carrying the user's own words.
	for _, source := range []string{"repl_main_thread", "sdk", "side_question", ""} {
		inner := &instructionCaptureLLM{}
		wrapped := wrapMemoryInstructionLLM(inner, t.TempDir(), memoryTestProjectKey, memoryTestConfig(true, true), nil, false)
		ctx := WithQuerySource(context.Background(), source)
		if _, err := wrapped.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(inner.developerText(), "Capturing memory") {
			t.Fatalf("capture rules missing for %q:\n%s", source, inner.developerText())
		}
	}
}

// Generation off means the user does not want new memory written, so the turn
// must not be told to capture; memory off entirely means no wrapper at all.
func TestMemoryInstructionFollowsSettings(t *testing.T) {
	inner := &instructionCaptureLLM{}
	wrapped := wrapMemoryInstructionLLM(inner, t.TempDir(), memoryTestProjectKey, memoryTestConfig(true, false), nil, false)
	if _, err := wrapped.Execute(context.Background(), []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(inner.developerText(), "Capturing memory") {
		t.Fatalf("capture rules injected with generation off:\n%s", inner.developerText())
	}

	off := &appcfg.Root{}
	off.Features.Memories = boolPtr(false)
	plain := &instructionCaptureLLM{}
	if wrapMemoryInstructionLLM(plain, t.TempDir(), memoryTestProjectKey, off, nil, false) != llm.LLM(plain) {
		t.Fatal("wrapper applied with memories disabled")
	}
}

// citationStubLLM returns a canned result for testing wrappers.
type citationStubLLM struct {
	result *llm.Result
}

func (s *citationStubLLM) Execute(_ context.Context, _ []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	return s.result, nil
}

type citationStreamingStubLLM struct {
	chunks []string
	result *llm.Result
}

func (s *citationStreamingStubLLM) Execute(ctx context.Context, _ []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	if sink := llm.StreamSinkFrom(ctx); sink != nil && sink.OnDelta != nil {
		for _, chunk := range s.chunks {
			sink.OnDelta(chunk)
		}
	}
	return s.result, nil
}

func TestWrapMemoryCitationLLM_FiltersStreamingChunks(t *testing.T) {
	cfg := &appcfg.Root{}
	cfg.Features.Memories = boolPtr(true)
	cfg.Memories.UseMemories = boolPtr(true)
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("Hello <oai-mem-citation><rollout_ids>\ns1\n</rollout_ids></oai-mem-citation> world")})
	inner := &citationStreamingStubLLM{
		chunks: []string{"Hello <oai-mem-", "citation><rollout_ids>\ns1\n</rollout_ids></oai-mem-", "citation> world"},
		result: &llm.Result{Message: &msg},
	}
	var streamed strings.Builder
	ctx := llm.WithStreamSink(context.Background(), &llm.StreamSink{OnDelta: func(delta string) { streamed.WriteString(delta) }})
	wrapped := wrapMemoryCitationLLM(inner, cfg, newCitationTestStore(t))
	res, err := wrapped.Execute(ctx, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if streamed.String() != "Hello  world" {
		t.Fatalf("stream leaked citation: %q", streamed.String())
	}
	if res.Message.TextContent() != "Hello  world" {
		t.Fatalf("final output leaked citation: %q", res.Message.TextContent())
	}
}

func newCitationTestStore(t *testing.T) *memory.Store {
	t.Helper()
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return memory.NewStore(db, "main")
}

func TestWrapMemoryCitationLLM_StripsCitationsFromMessage(t *testing.T) {
	cfg := &appcfg.Root{}
	cfg.Features.Memories = boolPtr(true)
	cfg.Memories.UseMemories = boolPtr(true)

	result := &llm.Result{
		Message: &llm.Message{
			Role:  llm.RoleAssistant,
			Parts: []llm.ContentPart{llm.Text("answer<oai-mem-citation><citation_entries>\nMEMORY.md:4-8|note=[build settings]\n</citation_entries><rollout_ids>\ns1\n</rollout_ids></oai-mem-citation> done")},
		},
	}
	inner := &citationStubLLM{result: result}
	ms := newCitationTestStore(t)
	// Upsert a stage1 row so the usage update has something to update.
	_ = ms.UpsertStage1Output(context.Background(), memory.Stage1Output{
		ThreadID:        "s1",
		ProjectKey:      "-Test-project",
		SourceUpdatedAt: 1,
		RawMemory:       "mem",
	})

	wrapped := wrapMemoryCitationLLM(inner, cfg, ms)
	res, err := wrapped.Execute(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	got := res.Message.TextContent()
	if got != "answer done" {
		t.Fatalf("citation not stripped: %q", got)
	}
	if citation := res.Message.MemoryCitation; citation == nil || len(citation.Entries) != 1 ||
		citation.Entries[0].Path != "MEMORY.md" || citation.Entries[0].LineStart != 4 ||
		len(citation.RolloutIDs) != 1 || citation.RolloutIDs[0] != "s1" {
		t.Fatalf("citation metadata = %#v", citation)
	}
}

func TestWrapMemoryCitationLLM_UsageAccountingAcceptsOnlyCanonicalUUIDs(t *testing.T) {
	cfg := &appcfg.Root{}
	cfg.Features.Memories = boolPtr(true)
	cfg.Memories.UseMemories = boolPtr(true)
	const canonical = "550e8400-e29b-41d4-a716-446655440000"
	message := llm.AssistantMessage([]llm.ContentPart{llm.Text("answer<oai-mem-citation><rollout_ids>\nnot-a-uuid\n550E8400-E29B-41D4-A716-446655440000\n" + canonical + "\n</rollout_ids></oai-mem-citation>")})
	ms := newCitationTestStore(t)
	if _, err := ms.DB.ExecContext(context.Background(), `INSERT INTO fb_sessions(id,agent_id,title,updated_at,created_at,memory_mode) VALUES(?,?,?,?,?,?)`, canonical, "main", canonical, 1, 1, memory.ThreadMemoryEnabled); err != nil {
		t.Fatal(err)
	}
	if err := ms.UpsertStage1Output(context.Background(), memory.Stage1Output{ThreadID: canonical, ProjectKey: "-Test-project", SourceUpdatedAt: 1, RawMemory: "m"}); err != nil {
		t.Fatal(err)
	}
	wrapped := wrapMemoryCitationLLM(&citationStubLLM{result: &llm.Result{Message: &message}}, cfg, ms)
	res, err := wrapped.Execute(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if citation := res.Message.MemoryCitation; citation == nil || len(citation.RolloutIDs) != 3 || citation.RolloutIDs[0] != "not-a-uuid" {
		t.Fatalf("citation metadata=%#v", citation)
	}
	var usage int
	if err := ms.DB.QueryRowContext(context.Background(), `SELECT COALESCE(usage_count,0) FROM fb_memory_stage1_outputs WHERE thread_id=?`, canonical).Scan(&usage); err != nil {
		t.Fatal(err)
	}
	if usage != 1 {
		t.Fatalf("usage=%d want 1", usage)
	}
}

func TestWrapMemoryCitationLLM_PassthroughWhenDisabled(t *testing.T) {
	// Memories default to on, so the disabled case has to say so explicitly.
	cfg := &appcfg.Root{}
	cfg.Features.Memories = boolPtr(false)

	inner := &citationStubLLM{result: &llm.Result{
		Message: &llm.Message{
			Role:  llm.RoleAssistant,
			Parts: []llm.ContentPart{llm.Text("text<oai-mem-citation>x</oai-mem-citation>")},
		},
	}}
	ms := newCitationTestStore(t)
	wrapped := wrapMemoryCitationLLM(inner, cfg, ms)
	// Should be the unwrapped inner since memory is disabled.
	if wrapped != inner {
		t.Fatalf("expected passthrough inner when memory disabled")
	}
}

func TestWrapMemoryCitationLLM_NilStorePassthrough(t *testing.T) {
	cfg := &appcfg.Root{}
	cfg.Features.Memories = boolPtr(true)
	cfg.Memories.UseMemories = boolPtr(true)

	inner := &citationStubLLM{}
	wrapped := wrapMemoryCitationLLM(inner, cfg, nil)
	if wrapped != inner {
		t.Fatalf("expected passthrough when store nil")
	}
}

func TestWrapMemoryCitationLLM_StripsFromSessionMessages(t *testing.T) {
	cfg := &appcfg.Root{}
	cfg.Features.Memories = boolPtr(true)
	cfg.Memories.UseMemories = boolPtr(true)

	result := &llm.Result{
		Message: &llm.Message{Role: llm.RoleAssistant, Parts: []llm.ContentPart{llm.Text("final")}},
		Session: []llm.Message{
			{Role: llm.RoleUser, Parts: []llm.ContentPart{llm.Text("q")}},
			{Role: llm.RoleAssistant, Parts: []llm.ContentPart{llm.Text("mid<oai-mem-citation><rollout_ids>\ns2\n</rollout_ids></oai-mem-citation>")}},
		},
	}
	inner := &citationStubLLM{result: result}
	ms := newCitationTestStore(t)
	_ = ms.UpsertStage1Output(context.Background(), memory.Stage1Output{
		ThreadID: "s2", ProjectKey: "-Test-project", SourceUpdatedAt: 1, RawMemory: "m",
	})

	wrapped := wrapMemoryCitationLLM(inner, cfg, ms)
	res, err := wrapped.Execute(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Session[1].TextContent() != "mid" {
		t.Fatalf("session citation not stripped: %q", res.Session[1].TextContent())
	}
}

func TestIsRootInteractiveSource(t *testing.T) {
	cases := map[string]bool{
		"repl_main_thread":   true,
		"sdk":                true,
		"agent:default":      false,
		"agent:builtin:fork": false,
		"agent:custom":       false,
		"compact":            false,
		"hook_agent":         false,
		"side_question":      false,
	}
	for src, want := range cases {
		if got := isRootInteractiveSource(src); got != want {
			t.Fatalf("isRootInteractiveSource(%q)=%v want %v", src, got, want)
		}
	}
}

func TestMaybeLaunchMemoryStartupGating(t *testing.T) {
	// Subagent runs (ParentRunID set) must never trigger startup, and a nil
	// runner must be a safe no-op. These assert the gating helpers do not
	// panic and short-circuit before touching the runner.
	maybeLaunchMemoryStartup(Options{}, "sess") // nil runner -> no-op

	// A child run source is not root-interactive.
	childSource := querySourceForRun(Options{
		ParentRunID: "run-parent",
		HC:          hook.HookContext{Trigger: "user"},
	})
	if isRootInteractiveSource(childSource) {
		t.Fatalf("child run source %q should not be root-interactive", childSource)
	}

	// A default TUI turn resolves to a root-interactive source.
	rootSource := querySourceForRun(Options{HC: hook.HookContext{Trigger: "user", Channel: "tui"}})
	if !isRootInteractiveSource(rootSource) {
		t.Fatalf("tui turn source %q should be root-interactive", rootSource)
	}
}

// A reload must drop the cached memory pipeline. The pipeline is built from
// the config in force when it was first launched, so keeping it across a Load
// would leave a session running against configuration the user has already
// replaced — including the case where memories were switched off entirely.
//
// The holder now lives in pkg/memory (the assembly moved there so that package
// owns building its own pipeline); this asserts the caching contract the Runner
// depends on.
func TestMemPipelineHolderResetDropsTheCachedPipeline(t *testing.T) {
	var h memory.PipelineHolder
	built := 0
	build := func() *memory.Pipeline { built++; return &memory.Pipeline{} }

	if h.Get(build) == nil || built != 1 {
		t.Fatalf("first Get must build once, built=%d", built)
	}
	if h.Get(build) == nil || built != 1 {
		t.Fatalf("second Get must reuse the cache, built=%d", built)
	}
	h.Reset()
	if h.Get(build) == nil || built != 2 {
		t.Fatalf("Get after Reset must rebuild, built=%d", built)
	}

	// A build that yields nil is cached as nil, so a disabled configuration is
	// not re-examined on every launch.
	var disabled memory.PipelineHolder
	nilBuilds := 0
	nilBuild := func() *memory.Pipeline { nilBuilds++; return nil }
	if got := disabled.Get(nilBuild); got != nil || nilBuilds != 1 {
		t.Fatalf("nil build = %v builds=%d", got, nilBuilds)
	}
	if got := disabled.Get(nilBuild); got != nil || nilBuilds != 1 {
		t.Fatalf("nil result must be cached, builds=%d", nilBuilds)
	}

	// Idempotent, and nil-safe: Load calls Reset unconditionally.
	h.Reset()
	(*memory.PipelineHolder)(nil).Reset()
	if got := (*memory.PipelineHolder)(nil).Get(build); got != nil {
		t.Fatalf("Get on a nil holder = %v, want nil", got)
	}
}

// The prompt prefix this project serves to providers is
//
//	tools → system(agent prompt) → developer(skills catalog) → developer(memory instruction) → conversation
//
// in that stability order: the most volatile block (the memory instruction,
// whose bytes depend on the store's contents) must sit as late as possible so
// a change to it cannot invalidate blocks ahead of it. This golden pins the
// wrapper-chain order runner.Load composes (memory instruction outermost,
// skills catalog inside it, both injecting behind the system message) and the
// per-session byte stability every request of one session depends on.
func TestPromptPrefixBlockOrderStable(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()

	skillDir := filepath.Join(workspace, "skills", "demo")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: demo\ndescription: A demo skill for ordering tests.\n---\n\nDemo body.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	inner := &instructionCaptureLLM{}
	sessions := newPromptStateStore(t)
	if err := sessions.Ensure(context.Background(), "prefix-order", "prefix-order"); err != nil {
		t.Fatal(err)
	}
	// Same composition order as runner.Load: skills catalog inside, memory
	// instruction outside it.
	wrapped := wrapMemoryInstructionLLM(
		wrapSkillCatalogLLM(inner, skill.AgentSkillRoots(home, workspace, safety.ProjectContext{}), workspace, sessions, nil),
		workspace, memoryTestProjectKey, memoryTestConfig(true, true), sessions, false)

	ctx := llm.WithAgentSessionID(context.Background(), "prefix-order")
	base := []llm.Message{
		llm.SystemMessage("fixed agent prompt"),
		llm.UserMessage(llm.Text("first turn")),
	}
	if _, err := wrapped.Execute(ctx, base, nil); err != nil {
		t.Fatal(err)
	}
	msgs := inner.messages
	if len(msgs) < 4 {
		t.Fatalf("expected system + skills + memory + conversation, got %d messages", len(msgs))
	}
	if msgs[0].Role != llm.RoleSystem {
		t.Fatalf("messages[0].Role=%s, want system", msgs[0].Role)
	}
	if msgs[1].Role != llm.RoleDeveloper || !strings.Contains(msgs[1].TextContent(), "demo") {
		t.Fatalf("messages[1] is not the skills catalog: role=%s text=%.80s", msgs[1].Role, msgs[1].TextContent())
	}
	if msgs[2].Role != llm.RoleDeveloper {
		t.Fatalf("messages[2].Role=%s, want developer (memory instruction)", msgs[2].Role)
	}
	if msgs[1].TextContent() == msgs[2].TextContent() {
		t.Fatal("skills catalog and memory instruction collapsed into one block")
	}
	if msgs[3].TextContent() != "first turn" {
		t.Fatalf("conversation displaced: messages[3]=%.80s", msgs[3].TextContent())
	}

	// The same session must render byte-identical prefix blocks on every
	// request: one changed byte invalidates tools, system, and every prior
	// turn on a prefix-caching provider.
	inner.messages = nil
	second := []llm.Message{
		llm.SystemMessage("fixed agent prompt"),
		llm.UserMessage(llm.Text("first turn")),
		llm.UserMessage(llm.Text("second turn")),
	}
	if _, err := wrapped.Execute(ctx, second, nil); err != nil {
		t.Fatal(err)
	}
	if len(inner.messages) < len(second)+2 {
		t.Fatalf("second render lost blocks: %d", len(inner.messages))
	}
	for i := 0; i < 3; i++ {
		a, _ := json.Marshal(msgs[i])
		b, _ := json.Marshal(inner.messages[i])
		if string(a) != string(b) {
			t.Fatalf("prefix block %d changed mid-session:\n%.200s\n%.200s", i, a, b)
		}
	}
}

// One-shot extraction calls are structurally 100% cache misses, so the amount
// of transcript a single stage-1 call may carry is bounded by an absolute
// budget rather than by the extraction model's context window: sizing it as a
// fraction of the window let a large-window model (GLM-5.3's 1,000,000) send a
// single 665,000-token, entirely uncacheable request.
func TestMemoryStage1RolloutLimitIsBoundedByAbsoluteCap(t *testing.T) {
	cfgFor := func(provider, model string) *appcfg.Root {
		return &appcfg.Root{
			Agents: appcfg.AgentsSection{
				Definitions: map[string]appcfg.AgentDefinition{
					"main": {LLMProviders: []appcfg.AgentLLMProviderConfig{{Provider: provider, Model: model}}},
				},
			},
		}
	}
	for _, tc := range []struct {
		name     string
		provider string
		model    string
	}{
		{"glm-5.3 (1M window)", "zhipuai", "glm-5.3"},
		{"glm-5.3-flash (1M window)", "zhipuai", "glm-5.3-flash"},
		{"unknown provider falls back to the cap", "not-a-provider", "not-a-model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := memoryStage1RolloutTokenLimit(cfgFor(tc.provider, tc.model), "main", tc.model)
			if got > memory.Stage1RolloutAbsoluteCap {
				t.Fatalf("stage-1 rollout limit = %d, want <= %d", got, memory.Stage1RolloutAbsoluteCap)
			}
			if got < 1 {
				t.Fatalf("stage-1 rollout limit = %d, want a positive budget", got)
			}
		})
	}
	if memory.Stage1RolloutAbsoluteCap != 32_000 {
		t.Fatalf("Stage1RolloutAbsoluteCap = %d, want the 32k budget the plan specifies", memory.Stage1RolloutAbsoluteCap)
	}
}

// Background memory requests carry prompt prefixes that have nothing to do with
// the user's conversation. The provider's implicit cache is one shared resource
// per account, so firing them next to an in-flight turn is what puts two
// unrelated large prefixes in it at once — and the requests that came back with
// the session's prefix gone were the ones issued next to that traffic.
func TestBackgroundMemoryRequestWaitsForTheForegroundToGoQuiet(t *testing.T) {
	var flight foregroundFlight
	inner := &instructionCaptureLLM{}
	gated := &backgroundMemoryLLM{inner: inner, idle: func() bool { return flight.idleFor(20 * time.Millisecond) }}

	flight.enter()
	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		close(started)
		if _, err := gated.Execute(context.Background(), []llm.Message{llm.UserMessage(llm.Text("extract"))}, nil); err != nil {
			t.Error(err)
		}
		close(done)
	}()

	<-started
	select {
	case <-done:
		t.Fatal("a background memory request went out while a turn was in flight")
	case <-time.After(50 * time.Millisecond):
	}

	flight.leave()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a background memory request never went out after the turn finished")
	}
}

// A session that never goes quiet must not stop memory from ever being written.
func TestBackgroundMemoryRequestGivesUpWaitingEventually(t *testing.T) {
	inner := &instructionCaptureLLM{}
	gated := &backgroundMemoryLLM{inner: inner, idle: func() bool { return false }}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := gated.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("extract"))}, nil); err != nil {
		t.Fatal(err)
	}
	if len(inner.messages) == 0 {
		t.Fatal("the request never went out")
	}
}
