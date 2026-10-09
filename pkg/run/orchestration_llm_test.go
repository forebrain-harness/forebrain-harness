package run

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/hook"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	toolpkg "github.com/forebrain-harness/forebrain-harness/pkg/tool"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

func TestCodegraphPromptLLMInjectsInstructionWhenToolAvailable(t *testing.T) {
	inner := &captureLLM{}
	wrapped := wrapCodegraphPromptLLM(inner)

	_, err := wrapped.Execute(context.Background(), []llm.Message{
		llm.SystemMessage("base system"),
		llm.UserMessage(llm.Text("inspect the repo")),
	}, []*llm.Tool{
		testTool(t, codegraphExploreToolName),
	})
	if err != nil {
		t.Fatal(err)
	}
	got := inner.messages[0].TextContent()
	if !strings.Contains(got, "base system") || !strings.Contains(got, codegraphExploreToolName) {
		t.Fatalf("system prompt missing codegraph instruction: %q", got)
	}
}

func TestCodegraphPromptLLMLeavesMessagesUntouchedWhenToolMissing(t *testing.T) {
	inner := &captureLLM{}
	wrapped := wrapCodegraphPromptLLM(inner)

	_, err := wrapped.Execute(context.Background(), []llm.Message{
		llm.SystemMessage("base system"),
		llm.UserMessage(llm.Text("inspect the repo")),
	}, []*llm.Tool{
		testTool(t, "web_search"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := inner.messages[0].TextContent(); got != "base system" {
		t.Fatalf("system prompt mutated unexpectedly: %q", got)
	}
}

// testTool builds a trivial tool for tests that only care about a tool's name.
func testTool(t *testing.T, name string) *llm.Tool {
	t.Helper()
	tool, err := llm.NewTool(name, "test tool", func(context.Context, *struct{}) (string, error) {
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("NewTool(%q): %v", name, err)
	}
	return tool
}

// streamOverflowLLM reproduces the provider behaviour from the reported bug: the
// openai-go SSE layer surfaces an overflow as a plain fmt.Errorf carrying the
// raw error frame, with no typed API error and no limit number anywhere in it.
type streamOverflowLLM struct {
	limit    int
	calls    int
	accepted []int
}

func (m *streamOverflowLLM) Execute(_ context.Context, messages []llm.Message, tools []*llm.Tool) (*llm.Result, error) {
	m.calls++
	size := llm.EstimateMessages(messages) + estimateToolsTokens(tools)
	if size > m.limit {
		return nil, fmt.Errorf(
			`received error while streaming: {"type":"invalid_request_error","code":"context_length_exceeded","message":"Your input exceeds the context window of this model. Please adjust your input and try again.","param":"input"}`,
		)
	}
	m.accepted = append(m.accepted, size)
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("answered")})
	// Report a prompt size well above the local estimate, as a real tokenizer
	// does for CJK text and dense tool output.
	return &llm.Result{Message: &msg, Usage: &llm.Usage{InputTokens: size * 2, OutputTokens: 12}}, nil
}

func overflowTestHistory(pairs int) []llm.Message {
	msgs := []llm.Message{llm.SystemMessage("system prompt"), llm.UserMessage(llm.Text("find the root cause"))}
	for i := range pairs {
		id := fmt.Sprintf("call_%d", i)
		msgs = append(msgs, llm.AssistantMessage(nil, llm.ToolCall{
			ID: id, Type: "function",
			Function: llm.FunctionCall{Name: "Bash", Arguments: `{"command":"grep -R pattern ."}`},
		}))
		msgs = append(msgs, llm.ToolResultMessage(id, llm.Text(strings.Repeat("match line\n", 60))))
	}
	return msgs
}

// TestStreamingOverflowRecoversAndContinues is the regression test for the
// reported bug: a context_length_exceeded arriving mid-run must compact and let
// the turn continue, not surface the provider error and end the turn.
func TestStreamingOverflowRecoversAndContinues(t *testing.T) {
	llm.ResetObservedForTest()
	history := overflowTestHistory(40)
	inner := &streamOverflowLLM{limit: llm.EstimateMessages(history) / 3}

	compacted := 0
	var reactive []bool
	deps := &CompactChainDeps{
		ActiveModel: func(context.Context) (string, string) { return "openai", "gpt-5.1-codex" },
		TryCompact: func(_ context.Context, msgs []llm.Message, _ []*llm.Tool, isReactive bool) ([]llm.Message, bool, error) {
			compacted++
			reactive = append(reactive, isReactive)
			// Stand in for the real replacement checkpoint: system prefix, the
			// user's request, and one summary turn.
			return []llm.Message{
				llm.SystemMessage("system prompt"),
				llm.UserMessage(llm.Text("find the root cause")),
				llm.UserMessage(llm.Text("summary of prior work")),
			}, true, nil
		},
	}
	wrapped := WrapRecoverableLLM(inner, nil, deps)

	ctx := toolpkg.WithRunID(context.Background(), "run-overflow")
	res, err := wrapped.Execute(ctx, history, nil)
	if err != nil {
		t.Fatalf("overflow was not recovered: %v", err)
	}
	if res == nil || res.Message == nil || res.Message.TextContent() != "answered" {
		t.Fatalf("result=%+v", res)
	}
	if compacted != 1 {
		t.Fatalf("compactions=%d want 1", compacted)
	}
	if len(reactive) != 1 || !reactive[0] {
		t.Fatalf("the overflow compaction must be requested as reactive, got %v", reactive)
	}
}

// The reactive compaction a provider overflow forces must keep the steers
// riding on the call out of its checkpoint, exactly like the proactive one.
func TestOverflowCompactionKeepsProvisionalSteersOutOfTheCheckpoint(t *testing.T) {
	llm.ResetObservedForTest()
	history := append(overflowTestHistory(40), llm.UserMessage(llm.Text("steer riding on the call")))
	inner := &streamOverflowLLM{limit: llm.EstimateMessages(history) / 3}
	var compactedInput []llm.Message
	deps := &CompactChainDeps{
		ActiveModel: func(context.Context) (string, string) { return "openai", "gpt-5.1-codex" },
		TryCompact: func(_ context.Context, msgs []llm.Message, _ []*llm.Tool, _ bool) ([]llm.Message, bool, error) {
			compactedInput = append([]llm.Message(nil), msgs...)
			return []llm.Message{
				llm.SystemMessage("system prompt"),
				llm.UserMessage(llm.Text("summary of prior work")),
			}, true, nil
		},
	}
	adoption := &compactionAdoptionSink{}
	ctx := withCompactionAdoptionSink(toolpkg.WithRunID(context.Background(), "run-overflow-steer"), adoption)
	ctx = withProvisionalTail(ctx, ctx, len(history), 1)
	if _, err := WrapRecoverableLLM(inner, nil, deps).Execute(ctx, history, nil); err != nil {
		t.Fatalf("overflow was not recovered: %v", err)
	}
	if len(compactedInput) != len(history)-1 {
		t.Fatalf("compaction saw %d messages, want the %d before the steer", len(compactedInput), len(history)-1)
	}
	replaced, ok := adoption.take()
	if !ok || replaced[len(replaced)-1].TextContent() != "steer riding on the call" {
		t.Fatalf("the request after the checkpoint must still end with the steer, got %v", roles(replaced))
	}
}

// TestOverflowTeachesWindowSoNextRunCompactsProactively asserts the learning
// loop: after one rejection, the same history must be compacted *before* being
// sent rather than rejected again.
func TestOverflowTeachesWindowSoNextRunCompactsProactively(t *testing.T) {
	llm.ResetObservedForTest()
	history := overflowTestHistory(40)
	realLimit := llm.EstimateMessages(history) / 3
	inner := &streamOverflowLLM{limit: realLimit}

	// The catalog claims a window far larger than the truth, so the proactive
	// threshold cannot fire on the catalog value alone.
	snapshot := json.RawMessage(`{"effective_input_tokens":1000000}`)
	compactCalls := 0
	deps := &CompactChainDeps{
		ActiveModel: func(context.Context) (string, string) { return "openai", "wrong-catalog-model" },
		TryCompact: func(context.Context, []llm.Message, []*llm.Tool, bool) ([]llm.Message, bool, error) {
			compactCalls++
			return []llm.Message{
				llm.SystemMessage("system prompt"),
				llm.UserMessage(llm.Text("find the root cause")),
				llm.UserMessage(llm.Text("summary of prior work")),
			}, true, nil
		},
	}
	wrapped := WrapRecoverableLLM(
		inner,
		func(context.Context) (json.RawMessage, bool) { return snapshot, true },
		deps,
	)

	// First run: rejected once, recovered reactively.
	if _, err := wrapped.Execute(toolpkg.WithRunID(context.Background(), "run-1"), history, nil); err != nil {
		t.Fatalf("first run: %v", err)
	}
	firstRunCalls := inner.calls
	if firstRunCalls != 2 {
		t.Fatalf("first run provider calls=%d want 2 (reject + retry)", firstRunCalls)
	}

	// Second run with the same oversized history: the learned ceiling must make
	// the threshold fire before the call, so the provider is never rejected again.
	if _, err := wrapped.Execute(toolpkg.WithRunID(context.Background(), "run-2"), history, nil); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if got := inner.calls - firstRunCalls; got != 1 {
		t.Fatalf("second run provider calls=%d want 1 (no rejection); the window was not learned", got)
	}
	if compactCalls != 2 {
		t.Fatalf("compactCalls=%d want 2 (one reactive, one proactive)", compactCalls)
	}
}

// TestOverflowWithoutRecoveryStillReportsProviderError guards against masking a
// genuine failure: if compaction cannot produce a checkpoint, the caller must
// still see the provider error.
func TestOverflowWithoutRecoveryStillReportsProviderError(t *testing.T) {
	llm.ResetObservedForTest()
	history := overflowTestHistory(10)
	inner := &streamOverflowLLM{limit: 1}
	attempted := 0
	wrapped := WrapRecoverableLLM(
		inner,
		nil,
		&CompactChainDeps{
			ActiveModel: func(context.Context) (string, string) { return "openai", "m" },
			TryCompact: func(context.Context, []llm.Message, []*llm.Tool, bool) ([]llm.Message, bool, error) {
				attempted++
				return nil, false, errors.New("compact backend down")
			},
		},
	)
	_, err := wrapped.Execute(toolpkg.WithRunID(context.Background(), "run-fail"), history, nil)
	if err == nil || !strings.Contains(err.Error(), "context_length_exceeded") {
		t.Fatalf("err=%v want the provider overflow error", err)
	}
	if attempted != 1 {
		t.Fatalf("compaction attempts=%d want 1", attempted)
	}
}

func TestToolSchemasCountTowardOccupancy(t *testing.T) {
	type bigToolInput struct {
		// Description repeated to inflate the schema token count.
		Command string `json:"command" jsonschema_description:"the shell command to execute. this description is intentionally long to push the schema token estimate above the compaction threshold for the test"`
	}
	tool, err := llm.NewTool("Bash", strings.Repeat("run a shell command. ", 60), func(_ context.Context, _ *bigToolInput) (string, error) { return "", nil })
	if err != nil {
		t.Fatal(err)
	}
	if got := estimateToolsTokens([]*llm.Tool{tool}); got < 300 {
		t.Fatalf("tool schema tokens=%d; schemas must count toward occupancy", got)
	}
	schemaTokens := estimateToolsTokens([]*llm.Tool{tool})
	// modelLimit is chosen so the schema alone exceeds 90% of the window,
	// but the tiny message alone does not.
	modelLimit := int(float64(schemaTokens) * 1.05)
	msgs := []llm.Message{llm.UserMessage(llm.Text("hi"))}
	if shouldCompactMessages(msgs, modelLimit, nil, -1, nil, "", "") {
		t.Fatal("tiny history without tools must not trigger compaction")
	}
	if !shouldCompactMessages(msgs, modelLimit, nil, -1, []*llm.Tool{tool}, "", "") {
		t.Fatalf("tool schema (%d tokens) must push occupancy over the threshold (limit=%d)", schemaTokens, modelLimit)
	}
}

func TestMergeAgentResultsCombinesTextAndUsage(t *testing.T) {
	a := &agent.Result{
		Parts:   []llm.ContentPart{llm.Text("a")},
		Summary: &agent.RunSummary{},
	}
	a.Summary.Usage.InputTokens = 1
	b := &agent.Result{
		Parts:   []llm.ContentPart{llm.Text("b")},
		Summary: &agent.RunSummary{},
	}
	b.Summary.Usage.OutputTokens = 2
	got := mergeAgentResults(a, b)
	if got.TextContent() != "a\nb" {
		t.Fatalf("text=%q", got.TextContent())
	}
	if got.Summary.Usage.InputTokens != 1 || got.Summary.Usage.OutputTokens != 2 {
		t.Fatalf("usage=%+v", got.Summary.Usage)
	}
}

func TestQuerySourceForRun(t *testing.T) {
	tests := []struct {
		name string
		in   Options
		want string
	}{
		{
			name: "default interactive",
			in:   Options{HC: hook.HookContext{Trigger: "user", Channel: "tui"}},
			want: "repl_main_thread",
		},
		{
			name: "sdk channel",
			in:   Options{HC: hook.HookContext{Trigger: "user", Channel: "sdk"}},
			want: "sdk",
		},
		{
			name: "child run",
			in:   Options{ParentRunID: "run-parent", HC: hook.HookContext{Trigger: "user"}},
			want: "agent:default",
		},
		{
			name: "fork subagent channel",
			in: Options{
				AgBase: toolpkg.WithForkChild(context.Background(), true),
				HC:     hook.HookContext{Trigger: "subagent_run", Channel: "subagent_fork"},
			},
			want: "agent:builtin:fork",
		},
		{
			name: "typed subagent channel",
			in: Options{
				AgBase: toolpkg.WithSubagentDefinitionSource(toolpkg.WithSubagentType(context.Background(), "verification"), "built-in"),
				HC:     hook.HookContext{Trigger: "subagent_run", Channel: "subagent_typed"},
			},
			want: "agent:builtin:verification",
		},
		{
			name: "custom typed subagent channel",
			in: Options{
				AgBase: toolpkg.WithSubagentDefinitionSource(toolpkg.WithSubagentType(context.Background(), "test-runner"), "user"),
				HC:     hook.HookContext{Trigger: "subagent_run", Channel: "subagent_typed"},
			},
			want: "agent:custom",
		},
		{
			name: "compact trigger",
			in:   Options{HC: hook.HookContext{Trigger: "compact"}},
			want: "compact",
		},
		{
			name: "hook trigger",
			in:   Options{HC: hook.HookContext{Trigger: "post_hook"}},
			want: "hook_agent",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := querySourceForRun(tt.in); got != tt.want {
				t.Fatalf("querySourceForRun()=%q want %q", got, tt.want)
			}
		})
	}
}

func TestPersistRunUsageFromErrorStoresWrappedUsage(t *testing.T) {
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, t.TempDir()+"/state.db")
	if err != nil {
		t.Fatalf("OpenStateForTest error: %v", err)
	}
	defer db.Close()

	rt := &state.RunStore{DB: db}
	ensureRunSessions(t, db, "sid")
	rr, err := rt.CreateRun(ctx, "sid", "review")
	if err != nil {
		t.Fatalf("CreateRun error: %v", err)
	}

	PersistRunUsageFromError(rt, rr.ID, llm.WrapErrorWithUsageForTest(errors.New("blank"), &llm.Usage{
		InputTokens:  123,
		OutputTokens: 45,
	}))

	rn, err := rt.GetRun(ctx, rr.ID)
	if err != nil {
		t.Fatalf("GetRun error: %v", err)
	}
	if rn.UsagePromptTokens != 123 || rn.UsageCompletionTokens != 45 {
		t.Fatalf("unexpected usage persisted: %+v", rn)
	}
}

func TestRunPassesCurrentRunIDToPreHooks(t *testing.T) {
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, t.TempDir()+"/state.db")
	if err != nil {
		t.Fatalf("OpenStateForTest error: %v", err)
	}
	defer db.Close()

	rt := &state.RunStore{DB: db}
	ensureRunSessions(t, db, "s1")
	runner := newLoadedRunnerForSupervisorTest(t, &supervisorScriptLLM{reply: "done"})
	pipe := hook.NewAgentPipeline()
	var hookRunID string
	pipe.Add("context", func(_ context.Context, phase string, hc hook.HookContext, text string) (hook.PreHookResult, error) {
		if phase != "pre" {
			t.Fatalf("phase=%q want pre", phase)
		}
		hookRunID = strings.TrimSpace(hc.RunID)
		return hook.PreHookResult{Text: text}, nil
	})

	var runID string
	res, _, err := Run(Options{
		RunRT:    rt,
		Runner:   runner,
		Hooks:    pipe,
		AgBase:   context.Background(),
		HC:       hook.HookContext{SessionID: "s1", Trigger: "user"},
		Input:    "remember context",
		RunIDOut: &runID,
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if res == nil || res.TextContent() != "done" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if runID == "" {
		t.Fatalf("expected run id")
	}
	if hookRunID != runID {
		t.Fatalf("hook run id=%q want %q", hookRunID, runID)
	}
}

type supervisorScriptLLM struct {
	reply string
}

func (s *supervisorScriptLLM) Execute(context.Context, []llm.Message, []*llm.Tool) (*llm.Result, error) {
	reply := strings.TrimSpace(s.reply)
	if reply == "" {
		reply = "ok"
	}
	return &llm.Result{Message: &llm.Message{Role: llm.RoleAssistant, Parts: []llm.ContentPart{llm.Text(reply)}}}, nil
}

type supervisorScriptedLLM struct {
	results []*llm.Result
	errs    []error
	callIdx int
}

func (s *supervisorScriptedLLM) Execute(context.Context, []llm.Message, []*llm.Tool) (*llm.Result, error) {
	idx := s.callIdx
	s.callIdx++
	if idx >= len(s.results) {
		idx = len(s.results) - 1
	}
	return s.results[idx], s.errs[idx]
}

func supervisorToolCallResult(toolName, callID string, usage *llm.Usage) *llm.Result {
	msg := llm.AssistantMessage(nil, llm.ToolCall{
		ID:   callID,
		Type: llm.ToolTypeFunction,
		Function: llm.FunctionCall{
			Name:      toolName,
			Arguments: `{}`,
		},
	})
	return &llm.Result{Message: &msg, Usage: usage}
}

func supervisorTextResult(text string, usage *llm.Usage) *llm.Result {
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text(text)})
	return &llm.Result{Message: &msg, Usage: usage}
}

func newLoadedRunnerForSupervisorTest(t *testing.T, script llm.LLM) *Runner {
	t.Helper()
	a, err := agent.New(script, "main", "desc")
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	r := &Runner{Deps: &Deps{Home: t.TempDir(), AgentName: "main", AppCfg: &appcfg.Root{Agents: appcfg.AgentsSection{Definitions: map[string]appcfg.AgentDefinition{
		"main": {},
	}}}}}
	setUnexportedRunnerFieldForSupervisorTest(t, r, "main", a)
	setUnexportedRunnerFieldForSupervisorTest(t, r, "mainCfg", AgentConfigYAML{Description: "desc"})
	return r
}

func newLoadedRunnerWithAgentForSupervisorTest(t *testing.T, a *agent.Agent) *Runner {
	t.Helper()
	r := &Runner{Deps: &Deps{Home: t.TempDir(), AgentName: "main", AppCfg: &appcfg.Root{Agents: appcfg.AgentsSection{Definitions: map[string]appcfg.AgentDefinition{
		"main": {},
	}}}}}
	setUnexportedRunnerFieldForSupervisorTest(t, r, "main", a)
	setUnexportedRunnerFieldForSupervisorTest(t, r, "mainCfg", AgentConfigYAML{Description: "desc"})
	return r
}

func setUnexportedRunnerFieldForSupervisorTest(t *testing.T, r *Runner, name string, value any) {
	t.Helper()
	field := reflect.ValueOf(r).Elem().FieldByName(name)
	reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem().Set(reflect.ValueOf(value))
}

func TestRunPersistsWholeLoopUsageOnSuccess(t *testing.T) {
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, t.TempDir()+"/state.db")
	if err != nil {
		t.Fatalf("OpenStateForTest error: %v", err)
	}
	defer db.Close()

	rt := &state.RunStore{DB: db}
	ensureRunSessions(t, db, "sid")
	script := &supervisorScriptedLLM{
		results: []*llm.Result{
			supervisorToolCallResult("echo_tool", "call-1", &llm.Usage{InputTokens: 10, OutputTokens: 3}),
			supervisorTextResult("done", &llm.Usage{InputTokens: 7, OutputTokens: 4}),
		},
		errs: []error{nil, nil},
	}
	main, err := agent.New(WrapUsageAccountingLLMForTest(script), "main", "desc")
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	tool, err := llm.NewTool("echo_tool", "echo", func(context.Context, *struct{}) (string, error) {
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("NewTool: %v", err)
	}
	if err := main.AddTool(tool); err != nil {
		t.Fatalf("AddTool: %v", err)
	}
	runner := newLoadedRunnerWithAgentForSupervisorTest(t, main)
	pipe := hook.NewAgentPipeline()

	var runID string
	res, _, err := Run(Options{
		RunRT:    rt,
		Runner:   runner,
		Hooks:    pipe,
		AgBase:   context.Background(),
		HC:       hook.HookContext{SessionID: "sid", Trigger: "user"},
		Input:    "review",
		RunIDOut: &runID,
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if res == nil || res.Summary == nil {
		t.Fatalf("expected summary, got %+v", res)
	}
	if res.Summary.Usage.InputTokens != 17 || res.Summary.Usage.OutputTokens != 7 {
		t.Fatalf("summary usage = %+v, want input=17 output=7", res.Summary.Usage)
	}

	rn, err := rt.GetRun(ctx, runID)
	if err != nil {
		t.Fatalf("GetRun error: %v", err)
	}
	if rn.UsagePromptTokens != 17 || rn.UsageCompletionTokens != 7 {
		t.Fatalf("persisted usage = %d/%d, want 17/7", rn.UsagePromptTokens, rn.UsageCompletionTokens)
	}
	if rn.Status != state.RunStatusDone {
		t.Fatalf("status = %s, want %s", rn.Status, state.RunStatusDone)
	}
}

func TestRunPersistsWholeLoopUsageOnFailure(t *testing.T) {
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, t.TempDir()+"/state.db")
	if err != nil {
		t.Fatalf("OpenStateForTest error: %v", err)
	}
	defer db.Close()

	rt := &state.RunStore{DB: db}
	ensureRunSessions(t, db, "sid")
	script := &supervisorScriptedLLM{
		results: []*llm.Result{
			supervisorToolCallResult("echo_tool", "call-1", &llm.Usage{InputTokens: 10, OutputTokens: 3}),
			nil,
		},
		errs: []error{
			nil,
			llm.WrapErrorWithUsageForTest(errors.New("boom"), &llm.Usage{InputTokens: 7, OutputTokens: 4}),
		},
	}
	main, err := agent.New(WrapUsageAccountingLLMForTest(script), "main", "desc")
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	tool, err := llm.NewTool("echo_tool", "echo", func(context.Context, *struct{}) (string, error) {
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("NewTool: %v", err)
	}
	if err := main.AddTool(tool); err != nil {
		t.Fatalf("AddTool: %v", err)
	}
	runner := newLoadedRunnerWithAgentForSupervisorTest(t, main)
	pipe := hook.NewAgentPipeline()

	var runID string
	_, _, err = Run(Options{
		RunRT:    rt,
		Runner:   runner,
		Hooks:    pipe,
		AgBase:   context.Background(),
		HC:       hook.HookContext{SessionID: "sid", Trigger: "user"},
		Input:    "review",
		RunIDOut: &runID,
	})
	if err == nil {
		t.Fatal("expected run error")
	}

	rn, err := rt.GetRun(ctx, runID)
	if err != nil {
		t.Fatalf("GetRun error: %v", err)
	}
	if rn.UsagePromptTokens != 17 || rn.UsageCompletionTokens != 7 {
		t.Fatalf("persisted usage = %d/%d, want 17/7", rn.UsagePromptTokens, rn.UsageCompletionTokens)
	}
	if rn.Status != state.RunStatusFailed {
		t.Fatalf("status = %s, want %s", rn.Status, state.RunStatusFailed)
	}
}

type coordinatedExecutionProductPathLLM struct {
	mu          sync.Mutex
	calls       int
	toolsByCall [][]string
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func (m *coordinatedExecutionProductPathLLM) Execute(_ context.Context, _ []llm.Message, tools []*llm.Tool) (*llm.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		if tool != nil {
			names = append(names, tool.Name())
		}
	}
	m.toolsByCall = append(m.toolsByCall, names)
	m.calls++
	switch m.calls {
	case 1:
		msg := llm.AssistantMessage(nil,
			llm.ToolCall{
				ID:   "call-todo",
				Type: llm.ToolTypeFunction,
				Function: llm.FunctionCall{
					Name:      "session_todo",
					Arguments: `{"action":"set","items":[{"id":"1","content":"Inspect code","status":"in_progress"}]}`,
				},
			},
		)
		return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
	default:
		msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("done")})
		return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
	}
}

func TestCoordinatedExecutionProductPathPreservesToolOrchestration(t *testing.T) {
	home := t.TempDir()
	st := toolpkg.NewState(home)
	var planEvents int
	st.SetStepHook(func(_ context.Context, evt toolpkg.StepEvent) {
		if evt.Kind == event.RunEventPlanUpdated && evt.PlanUpdate != nil {
			planEvents++
		}
	})

	mainAgent, err := agent.New(noopLLM{}, "main", "main")
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	if err := toolpkg.RegisterDefaultTools(mainAgent, st, &toolpkg.AgentToolRuntime{
		Home: home,
		Cfg:  &appcfg.Root{},
	}); err != nil {
		t.Fatalf("RegisterDefaultTools: %v", err)
	}
	tools := debuglogToolsView(mainAgent)
	if len(tools) == 0 {
		t.Fatal("expected registered tools")
	}

	inner := &coordinatedExecutionProductPathLLM{}
	runner := &Runner{Deps: &Deps{Home: home, AppCfg: &appcfg.Root{}}, tools: st}
	wrapped := runner.wrapExecutionLLM(inner, true)
	ctx := llm.WithAgentSessionID(context.Background(), "s1")
	ctx = toolpkg.WithMode(ctx, "agent")
	ctx = WithQuerySource(ctx, "repl_main_thread")
	ctx = WithTurnInputRuntime(ctx, NewTurnInputRuntime())
	res, err := wrapped.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("inspect and update code"))}, tools)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res == nil || res.Message == nil || res.Message.TextContent() != "done" {
		t.Fatalf("unexpected result: %#v", res)
	}
	if planEvents == 0 {
		t.Fatal("expected plan_updated event from session_todo in product wrapper chain")
	}
	if len(inner.toolsByCall) == 0 || !containsString(inner.toolsByCall[0], "session_todo") {
		t.Fatalf("expected session_todo always visible in agent mode, got %#v", inner.toolsByCall)
	}
	if got := strings.Join(inner.toolsByCall[0], ","); strings.Contains(got, "mcp__") {
		t.Fatalf("agent mode should not expose unrelated MCP tools, got %q", got)
	}
}

func TestExitPlanModeActionHookAlwaysRequiresApprovalDespiteAllowRule(t *testing.T) {
	ctx := llm.WithAgentSessionID(context.Background(), "session-1")
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("OpenStateForTest: %v", err)
	}
	defer db.Close()
	ensureRunSessions(t, db, "session-1")

	payload := map[string]any{"session_id": "session-1", "force_tool_approval": true}
	permissionsStore := safety.NewStore()
	permissionsStore.AddRule(safety.SourceSession, safety.BehaviorAllow, safety.PermissionRuleValue{ToolName: "exit_plan_mode", SessionID: "session-1"})
	r := &Runner{Deps: &Deps{Actions: &state.ActionService{DB: db}}, permRuntime: safety.NewRuntimeWithStore(permissionsStore)}
	id, pending, err := r.actionHook(ctx, "exit_plan_mode", payload)
	if err != nil {
		t.Fatalf("actionHook: %v", err)
	}
	if !pending || id == "" {
		t.Fatalf("exit must create a pending action: id=%q pending=%v", id, pending)
	}
	action, err := r.Actions.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get pending action: %v", err)
	}
	if action.Kind != "exit_plan_mode" || action.Status != state.ActionPending {
		t.Fatalf("created action=%+v", action)
	}
}

func TestExitPlanModeActionHookAlwaysRequiresApprovalUnderYOLO(t *testing.T) {
	t.Setenv(safety.EnvYOLO, "1")
	ctx := llm.WithAgentSessionID(context.Background(), "session-1")
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("OpenStateForTest: %v", err)
	}
	defer db.Close()
	ensureRunSessions(t, db, "session-1")

	r := &Runner{Deps: &Deps{Actions: &state.ActionService{DB: db}}, permRuntime: safety.NewRuntimeWithStore(safety.NewStore())}
	id, pending, err := r.actionHook(ctx, "exit_plan_mode", map[string]any{"session_id": "session-1", "force_tool_approval": true})
	if err != nil {
		t.Fatalf("actionHook: %v", err)
	}
	if !pending || id == "" {
		t.Fatalf("YOLO must not answer for exit_plan_mode: id=%q pending=%v", id, pending)
	}
	action, err := r.Actions.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get pending action: %v", err)
	}
	if action.Kind != "exit_plan_mode" || action.Status != state.ActionPending {
		t.Fatalf("created action=%+v", action)
	}
}

func TestForcedSandboxEscalationCannotOverrideDenyRule(t *testing.T) {
	ctx := llm.WithAgentSessionID(context.Background(), "session-1")
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ensureRunSessions(t, db, "session-1")
	permissionStore := safety.NewStore()
	permissionStore.AddRule(safety.SourceSession, safety.BehaviorDeny, safety.PermissionRuleValue{
		ToolName: "Bash", RuleContent: "rm -rf:*", SessionID: "session-1",
	})
	runner := &Runner{Deps: &Deps{Actions: &state.ActionService{DB: db}}, permRuntime: safety.NewRuntimeWithStore(permissionStore)}
	_, pending, err := runner.actionHook(ctx, "shell", map[string]any{
		"command": "rm -rf tmp", "force_tool_approval": true, "sandbox_permissions": "require_escalated",
	})
	if err == nil || pending {
		t.Fatalf("hard deny must win over forced sandbox escalation: pending=%v err=%v", pending, err)
	}
}

func TestForcedSandboxEscalationAllowsPersistedPrefixRule(t *testing.T) {
	permissionStore := safety.NewStore()
	permissionStore.AddRule(safety.SourceSession, safety.BehaviorAllow, safety.PermissionRuleValue{
		ToolName: "Bash", RuleContent: "go test:*", SessionID: "session-1",
	})
	runner := &Runner{Deps: &Deps{},
		permRuntime: safety.NewRuntimeWithStore(permissionStore),
	}
	ctx := llm.WithAgentSessionID(context.Background(), "session-1")
	_, pending, err := runner.actionHook(ctx, "shell", map[string]any{
		"command": "go test ./...", "force_tool_approval": true, "sandbox_permissions": "require_escalated",
	})
	if err != nil || pending {
		t.Fatalf("persisted prefix allow must suppress forced sandbox re-approval: pending=%v err=%v", pending, err)
	}
}

func TestUnlessTrustedDoesNotUseWorkspaceTrustToSkipPrompt(t *testing.T) {
	permissionStore := safety.NewStore()
	permissionStore.SetApprovalPolicy(safety.ApprovalPolicy{Mode: safety.ApprovalUnlessTrusted})
	runner := &Runner{Deps: &Deps{}, permRuntime: safety.NewRuntimeWithStore(permissionStore)}
	if _, _, err := runner.actionHook(context.Background(), "write_file", map[string]any{"file_path": "/workspace/a.go"}); err == nil {
		t.Fatal("workspace write must still require approval")
	}
	if _, _, err := runner.actionHook(context.Background(), "write_file", map[string]any{"file_path": "/outside/a.go"}); err == nil {
		t.Fatal("untrusted workspace must require approval")
	}
}

func TestApprovedSandboxEscalationIsScopedToItsAction(t *testing.T) {
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	actions := &state.ActionService{DB: db}
	runner := &Runner{Deps: &Deps{Actions: actions}}
	if err := state.NewSessionStore(db, "main").Ensure(ctx, "conv", "conv"); err != nil {
		t.Fatal(err)
	}
	action, err := actions.CreatePending(ctx, "conv", "shell", map[string]any{
		"command": "git commit", "sandbox_permissions": "require_escalated",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := actions.Approve(ctx, action.ID, "approved"); err != nil {
		t.Fatal(err)
	}
	if !runner.approvedActionBypassesSandbox(toolpkg.WithApprovedActionID(ctx, action.ID)) {
		t.Fatal("approved sandbox escalation was not recognized")
	}
	ordinary, err := actions.CreatePending(ctx, "conv", "shell", map[string]any{
		"command": "git status", "sandbox_permissions": "use_default",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := actions.Approve(ctx, ordinary.ID, "approved"); err != nil {
		t.Fatal(err)
	}
	if runner.approvedActionBypassesSandbox(toolpkg.WithApprovedActionID(ctx, ordinary.ID)) {
		t.Fatal("ordinary approval leaked sandbox bypass")
	}
}

func TestExitPlanModeActionHookScopesApprovedActionToExitAndSession(t *testing.T) {
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("OpenStateForTest: %v", err)
	}
	defer db.Close()
	ensureRunSessions(t, db, "session-1")

	actions := &state.ActionService{DB: db}
	sessions := state.NewSessionStore(db, "main")
	for _, sid := range []string{"session-1", "session-2"} {
		if err := sessions.Ensure(ctx, sid, sid); err != nil {
			t.Fatal(err)
		}
	}
	r := &Runner{Deps: &Deps{Actions: actions}}
	payload := map[string]any{"session_id": "session-1", "force_tool_approval": true}

	cases := []struct {
		name       string
		kind       string
		sessionID  string
		status     state.ActionStatus
		wantReplay bool
	}{
		{name: "wrong kind", kind: "enter_plan_mode", sessionID: "session-1", status: state.ActionApproved},
		{name: "not approved", kind: "exit_plan_mode", sessionID: "session-1", status: state.ActionPending},
		{name: "denied", kind: "exit_plan_mode", sessionID: "session-1", status: state.ActionDenied},
		{name: "different session", kind: "exit_plan_mode", sessionID: "session-2", status: state.ActionApproved},
		{name: "matching approved action", kind: "EXIT_PLAN_MODE", sessionID: "session-1", status: state.ActionApproved, wantReplay: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			action, err := actions.CreatePending(ctx, tc.sessionID, tc.kind, map[string]any{"session_id": tc.sessionID, "force_tool_approval": true})
			if err != nil {
				t.Fatalf("CreatePending: %v", err)
			}
			switch tc.status {
			case state.ActionApproved:
				if _, err := actions.Approve(ctx, action.ID, "approved"); err != nil {
					t.Fatalf("Approve: %v", err)
				}
			case state.ActionDenied:
				if _, err := actions.Deny(ctx, action.ID, "denied"); err != nil {
					t.Fatalf("Deny: %v", err)
				}
			}
			callCtx := toolpkg.WithApprovedActionID(llm.WithAgentSessionID(ctx, tc.sessionID), action.ID)
			id, pending, err := r.actionHook(callCtx, "exit_plan_mode", payload)
			if err != nil {
				t.Fatalf("actionHook: %v", err)
			}
			if tc.wantReplay {
				if pending || id != action.ID {
					t.Fatalf("matching action must replay: id=%q pending=%v", id, pending)
				}
				return
			}
			if !pending || id == "" || id == action.ID {
				t.Fatalf("invalid action must create a fresh pending exit action: id=%q pending=%v", id, pending)
			}
			fresh, err := actions.Get(ctx, id)
			if err != nil {
				t.Fatalf("Get fresh action: %v", err)
			}
			if fresh.Kind != "exit_plan_mode" || fresh.Status != state.ActionPending {
				t.Fatalf("fresh action=%+v", fresh)
			}
		})
	}

	missingCtx := toolpkg.WithApprovedActionID(llm.WithAgentSessionID(ctx, "session-1"), "missing-action")
	id, pending, err := r.actionHook(missingCtx, "exit_plan_mode", payload)
	if err != nil {
		t.Fatalf("missing action: %v", err)
	}
	if !pending || id == "" || id == "missing-action" {
		t.Fatalf("missing action must create a fresh pending exit action: id=%q pending=%v", id, pending)
	}
}

func TestExitPlanModeToolRejectsInvalidApprovalAndReplaysMatchingApproval(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.sqlite"))
	if err != nil {
		t.Fatalf("OpenStateForTest: %v", err)
	}
	defer db.Close()
	ensureRunSessions(t, db, "session-1")

	actions := &state.ActionService{DB: db}
	r := &Runner{Deps: &Deps{Actions: actions}}
	st := toolpkg.NewState()
	st.SetActionHook(r.actionHook)
	_, exitTool, err := toolpkg.NewPlanModeTools(st, home)
	if err != nil {
		t.Fatalf("NewPlanModeTools: %v", err)
	}

	const sid = "session-1"
	if err := state.NewSessionStore(db, "main").Ensure(ctx, sid, sid); err != nil {
		t.Fatal(err)
	}
	if err := state.Set(home, sid, state.State{Mode: state.ModePlan, PrePlanMode: state.ModeAgent}); err != nil {
		t.Fatalf("seed plan mode: %v", err)
	}
	wrong, err := actions.CreatePending(ctx, sid, "enter_plan_mode", map[string]any{"session_id": sid})
	if err != nil {
		t.Fatalf("CreatePending wrong action: %v", err)
	}
	if _, err := actions.Approve(ctx, wrong.ID, "approved"); err != nil {
		t.Fatalf("Approve wrong action: %v", err)
	}
	wrongCtx := toolpkg.WithApprovedActionID(llm.WithAgentSessionID(ctx, sid), wrong.ID)
	_, err = exitTool.Handle(wrongCtx, `{}`)
	var req *toolpkg.RequiresActionError
	if !errors.As(err, &req) || req == nil || req.ActionID == wrong.ID {
		t.Fatalf("invalid approval must produce a fresh RequiresActionError, got %T %#v", err, req)
	}
	current, _ := state.Get(home, sid)
	if current.Mode != state.ModePlan {
		t.Fatalf("invalid approval changed mode to %v", current.Mode)
	}

	valid, err := actions.CreatePending(ctx, sid, "exit_plan_mode", map[string]any{"session_id": sid})
	if err != nil {
		t.Fatalf("CreatePending valid action: %v", err)
	}
	if _, err := actions.Approve(ctx, valid.ID, "approved"); err != nil {
		t.Fatalf("Approve valid action: %v", err)
	}
	validCtx := toolpkg.WithApprovedActionID(llm.WithAgentSessionID(ctx, sid), valid.ID)
	if _, err := exitTool.Handle(validCtx, `{}`); err != nil {
		t.Fatalf("matching approved action did not replay: %v", err)
	}
	current, _ = state.Get(home, sid)
	if current.Mode != state.ModeAgent {
		t.Fatalf("matching approval did not exit plan mode: %v", current.Mode)
	}
}

func TestPreloadExplicitSkillEmitsRealLifecycleAndSeedsContent(t *testing.T) {
	dir := t.TempDir()
	skillPath := filepath.Join(dir, "skills", "review", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skillPath, []byte("---\nname: review\ndescription: review changes\n---\nReview carefully."), 0o644); err != nil {
		t.Fatal(err)
	}
	state := toolpkg.NewState(t.TempDir())
	var got []toolpkg.StepEvent
	state.SetStepHook(func(_ context.Context, evt toolpkg.StepEvent) {
		got = append(got, evt)
	})
	runner := &Runner{Deps: &Deps{}, tools: state}
	ctx := WithExplicitSkillSelection(context.Background(), "review", skillPath)

	seeded, err := runner.preloadExplicitSkill(ctx)
	if err != nil {
		t.Fatalf("preload failed: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("steps=%+v", got)
	}
	started, completed := got[0], got[1]
	if started.Kind != toolpkg.StepKindToolStarted || completed.Kind != toolpkg.StepKindToolCompleted {
		t.Fatalf("kinds=%+v", got)
	}
	if started.StepID == "" || started.StepID != completed.StepID {
		t.Fatalf("step ids=%q/%q, want one stable identity", started.StepID, completed.StepID)
	}
	for _, evt := range got {
		if evt.ToolName != "skill" || evt.Category != "skill" || evt.SkillName != "review" ||
			evt.SkillPath != skillPath || evt.Origin != "explicit" {
			t.Fatalf("identity=%+v", evt)
		}
		if evt.Input != nil || evt.Output != nil {
			t.Fatalf("step must not carry an activation payload: %+v", evt)
		}
	}
	if completed.Duration < 0 {
		t.Fatalf("duration=%v", completed.Duration)
	}
	activation, ok := explicitSkillActivationFromContext(seeded)
	if !ok || !strings.Contains(activation.Content, "Review carefully.") {
		t.Fatalf("activation not seeded: %+v ok=%v", activation, ok)
	}
}

func TestPreloadExplicitSkillFailsBeforeLLMWithTypedError(t *testing.T) {
	state := toolpkg.NewState(t.TempDir())
	var got []toolpkg.StepEvent
	state.SetStepHook(func(_ context.Context, evt toolpkg.StepEvent) {
		got = append(got, evt)
	})
	runner := &Runner{Deps: &Deps{}, tools: state}
	missing := filepath.Join(t.TempDir(), "skills", "gone", "SKILL.md")
	ctx := WithExplicitSkillSelection(context.Background(), "gone", missing)

	_, err := runner.preloadExplicitSkill(ctx)
	var loadErr *ExplicitSkillLoadError
	if !errors.As(err, &loadErr) {
		t.Fatalf("err=%v, want *ExplicitSkillLoadError", err)
	}
	if loadErr.SkillName != "gone" || loadErr.SkillPath != missing {
		t.Fatalf("load error=%+v", loadErr)
	}
	if len(got) != 2 {
		t.Fatalf("steps=%+v", got)
	}
	failed := got[1]
	if failed.Kind != toolpkg.StepKindToolCompleted || failed.Error == "" {
		t.Fatalf("failure step=%+v", failed)
	}
	if failed.SkillName != "gone" || failed.Category != "skill" || failed.Origin != "explicit" {
		t.Fatalf("failure identity=%+v", failed)
	}
}

func TestPreloadExplicitSkillRejectsRenamedSkillFile(t *testing.T) {
	dir := t.TempDir()
	skillPath := filepath.Join(dir, "skills", "review", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skillPath, []byte("---\nname: something-else\ndescription: moved\n---\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}
	state := toolpkg.NewState(t.TempDir())
	var got []toolpkg.StepEvent
	state.SetStepHook(func(_ context.Context, evt toolpkg.StepEvent) {
		got = append(got, evt)
	})
	runner := &Runner{Deps: &Deps{}, tools: state}

	_, err := runner.preloadExplicitSkill(WithExplicitSkillSelection(context.Background(), "review", skillPath))
	if err == nil {
		t.Fatal("expected the renamed skill file to fail the preload")
	}
	var loadErr *ExplicitSkillLoadError
	if !errors.As(err, &loadErr) {
		t.Fatalf("err=%v, want *ExplicitSkillLoadError", err)
	}
	if len(got) != 2 || got[1].Error == "" {
		t.Fatalf("steps=%+v", got)
	}
}

func TestExplicitSkillActivationMessageIsEphemeral(t *testing.T) {
	activation, ok := explicitSkillActivationFromContext(
		WithExplicitSkillActivation(context.Background(), "context-restore", "<skill_instructions>restore</skill_instructions>"),
	)
	if !ok {
		t.Fatal("expected activation in context")
	}
	msg := explicitSkillActivationMessage(activation)
	if !msg.Ephemeral {
		t.Fatal("explicit skill activation message must be Ephemeral so it is never persisted to the transcript")
	}
	if !msg.IsMeta {
		t.Fatal("explicit skill activation message must remain IsMeta so it is excluded from real user-turn detection")
	}
}

// outsideWorkspaceRunner wires a runner the way the live pipeline does: a
// workspace-write sandbox whose writable roots are pushed into the file tools,
// approvals decided by the shared tool permission middleware.

// ensureRunSessions creates the session rows a test's runs and approvals
// reference; the foreign keys enforce that both belong to a conversation.
func ensureRunSessions(t *testing.T, db *sql.DB, sids ...string) {
	t.Helper()
	store := state.NewSessionStore(db, "main")
	for _, sid := range sids {
		if err := store.Ensure(context.Background(), sid, sid); err != nil {
			t.Fatal(err)
		}
	}
}

func outsideWorkspaceRunner(t *testing.T, projectRoot string, mode safety.AskForApproval) (*Runner, *toolpkg.State) {
	t.Helper()
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ensureRunSessions(t, db, "session-1")
	permissionStore := safety.NewStore()
	permissionStore.SetApprovalPolicy(safety.ApprovalPolicy{Mode: mode})
	cfg := &appcfg.Root{
		SandboxMode: appcfg.SandboxModeWorkspaceWrite,
		SandboxWorkspaceWrite: appcfg.SandboxWorkspaceWrite{
			ExcludeTmpdirEnvVar: true,
			ExcludeSlashTmp:     true,
		},
	}
	st := toolpkg.NewState(projectRoot)
	r := &Runner{Deps: &Deps{Actions: &state.ActionService{DB: db}, AppCfg: cfg, ProjectRoot: projectRoot}, permRuntime: safety.NewRuntimeWithStore(permissionStore), tools: st}
	toolpkg.ApplyFilesystemPolicy(st, t.TempDir(), projectRoot, cfg, safety.Snapshot{})
	st.SetActionHook(r.actionHook)
	return r, st
}

func outsideWorkspaceFile(t *testing.T) (projectRoot, target string) {
	t.Helper()
	projectRoot = t.TempDir()
	outside := filepath.Join(t.TempDir(), "other-project")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	target = filepath.Join(outside, "Service.java")
	if err := os.WriteFile(target, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return projectRoot, target
}

func editOutsideWorkspace(t *testing.T, r *Runner, st *toolpkg.State, ctx context.Context, target string) (any, error) {
	t.Helper()
	editTool, err := toolpkg.NewFileEditTool(st)
	if err != nil {
		t.Fatal(err)
	}
	readTool, err := toolpkg.NewFileReadTool(st)
	if err != nil {
		t.Fatal(err)
	}
	// edit_file requires a prior read of the same file.
	if _, err := readTool.Handle(toolpkg.WithPolicyApproved(ctx, true), `{"file_path":"`+target+`"}`); err != nil {
		t.Fatalf("read before edit: %v", err)
	}
	wrapped := r.newToolPermissionMiddleware()(editTool, func(ctx context.Context, arguments string) (any, error) {
		return editTool.Handle(ctx, arguments)
	})
	return wrapped(ctx, `{"file_path":"`+target+`","old_string":"hello","new_string":"bye"}`)
}

// A file outside the workspace roots is asked about once and then written.
// Refusing it after the operator approved would strand the approval: the roots
// the file tools resolve against are never widened by an approval, and the
// model reaches the same file through apply_patch anyway.
func TestEditOutsideWorkspaceAppliesAfterApproval(t *testing.T) {
	project, target := outsideWorkspaceFile(t)
	r, st := outsideWorkspaceRunner(t, project, safety.ApprovalOnRequest)
	ctx := llm.WithAgentSessionID(context.Background(), "session-1")

	_, err := editOutsideWorkspace(t, r, st, ctx, target)
	var req *toolpkg.RequiresActionError
	if !errors.As(err, &req) {
		t.Fatalf("an edit outside the workspace must ask first, got %v", err)
	}
	if req.ActionKind != "edit_file" {
		t.Fatalf("unexpected approval kind %q", req.ActionKind)
	}
	if _, err := r.Actions.Approve(context.Background(), req.ActionID, "approved"); err != nil {
		t.Fatal(err)
	}

	if _, err := editOutsideWorkspace(t, r, st, toolpkg.WithApprovedActionID(ctx, req.ActionID), target); err != nil {
		t.Fatalf("approved edit must apply: %v", err)
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "bye\n" {
		t.Fatalf("file content = %q, want %q", string(raw), "bye\n")
	}
}

// With approvals turned off there is no prompt to answer, so the write is
// refused rather than performed unattended.
func TestEditOutsideWorkspaceDeniedWhenApprovalsAreOff(t *testing.T) {
	project, target := outsideWorkspaceFile(t)
	r, st := outsideWorkspaceRunner(t, project, safety.ApprovalNever)
	ctx := llm.WithAgentSessionID(context.Background(), "session-1")

	_, err := editOutsideWorkspace(t, r, st, ctx, target)
	if err == nil {
		t.Fatal("approvals=never must not write outside the workspace")
	}
	var req *toolpkg.RequiresActionError
	if errors.As(err, &req) {
		t.Fatalf("approvals=never must not create a pending action: %+v", req)
	}
	raw, _ := os.ReadFile(target)
	if string(raw) != "hello\n" {
		t.Fatalf("file was modified: %q", string(raw))
	}
}

// A write the sandbox already covers still runs unattended.
func TestEditInsideWorkspaceNeedsNoApproval(t *testing.T) {
	project := t.TempDir()
	target := filepath.Join(project, "main.go")
	if err := os.WriteFile(target, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, st := outsideWorkspaceRunner(t, project, safety.ApprovalOnRequest)
	ctx := llm.WithAgentSessionID(context.Background(), "session-1")

	if _, err := editOutsideWorkspace(t, r, st, ctx, target); err != nil {
		t.Fatalf("in-workspace edit must not require approval: %v", err)
	}
	raw, _ := os.ReadFile(target)
	if string(raw) != "bye\n" {
		t.Fatalf("file content = %q, want %q", string(raw), "bye\n")
	}
}

// The isolation boundaries are not approvable. The middleware knows nothing
// about them and still raises its prompt, so the property that matters is the
// one asserted here: approving it changes nothing, the write stays refused.
func TestEditInsideAnotherPrimaryWorkspaceStaysRefused(t *testing.T) {
	home := t.TempDir()
	activeWS := filepath.Join(home, "workspaces", "review")
	siblingWS := filepath.Join(home, "workspace")
	target := filepath.Join(siblingWS, "notes.md")
	if err := os.MkdirAll(siblingWS, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, st := outsideWorkspaceRunner(t, activeWS, safety.ApprovalOnRequest)
	st.SetPrimaryWorkspaceBoundary(home, activeWS, []string{siblingWS})
	ctx := llm.WithAgentSessionID(context.Background(), "session-1")

	editTool, err := toolpkg.NewFileEditTool(st)
	if err != nil {
		t.Fatal(err)
	}
	wrapped := r.newToolPermissionMiddleware()(editTool, func(ctx context.Context, arguments string) (any, error) {
		return editTool.Handle(ctx, arguments)
	})
	arguments := `{"file_path":"` + target + `","old_string":"hello","new_string":"bye"}`
	_, err = wrapped(ctx, arguments)
	var req *toolpkg.RequiresActionError
	if errors.As(err, &req) {
		if _, aerr := r.Actions.Approve(context.Background(), req.ActionID, "approved"); aerr != nil {
			t.Fatal(aerr)
		}
		_, err = wrapped(toolpkg.WithApprovedActionID(ctx, req.ActionID), arguments)
	}
	if err == nil || !strings.Contains(err.Error(), "primary agent workspace isolation") {
		t.Fatalf("sibling workspace must stay refused, got %v", err)
	}
	raw, _ := os.ReadFile(target)
	if string(raw) != "hello\n" {
		t.Fatalf("file was modified: %q", string(raw))
	}
}

// The same holds for the rest of FOREBRAIN_HOME outside the active workspace.
// A protected edit asks once. The read it owes is part of that same decision,
// so it must not raise a second prompt about a change the user already approved.
func TestEditInsideForebrainHomeAsksOnceAndLetsItsReadThrough(t *testing.T) {
	home := t.TempDir()
	activeWS := filepath.Join(home, "workspace")
	target := filepath.Join(home, "forebrain.yaml")
	if err := os.MkdirAll(activeWS, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, st := outsideWorkspaceRunner(t, activeWS, safety.ApprovalOnRequest)
	st.SetPrimaryWorkspaceBoundary(home, activeWS, nil)
	ctx := llm.WithAgentSessionID(context.Background(), "session-1")

	editTool, err := toolpkg.NewFileEditTool(st)
	if err != nil {
		t.Fatal(err)
	}
	wrapped := r.newToolPermissionMiddleware()(editTool, func(ctx context.Context, arguments string) (any, error) {
		return editTool.Handle(ctx, arguments)
	})
	arguments := `{"file_path":"` + target + `","old_string":"hello","new_string":"bye"}`
	_, err = wrapped(ctx, arguments)
	var req *toolpkg.RequiresActionError
	if !errors.As(err, &req) {
		t.Fatalf("FOREBRAIN_HOME edit must ask before it is allowed, got %v", err)
	}
	raw, _ := os.ReadFile(target)
	if string(raw) != "hello\n" {
		t.Fatalf("file was modified before the approval was answered: %q", string(raw))
	}
	if _, aerr := r.Actions.Approve(context.Background(), req.ActionID, "approved"); aerr != nil {
		t.Fatal(aerr)
	}

	// Replaying the approved edit reveals the read it still owes.
	_, err = wrapped(toolpkg.WithApprovedActionID(ctx, req.ActionID), arguments)
	if err == nil || !strings.Contains(err.Error(), "read_file") {
		t.Fatalf("approved edit error=%v want the read-before-edit requirement", err)
	}

	// That read is not a second question: the user already agreed to have this
	// file changed, so it runs unasked.
	readTool, err := toolpkg.NewFileReadTool(st)
	if err != nil {
		t.Fatal(err)
	}
	if _, readErr := readTool.Handle(ctx, `{"file_path":"`+target+`"}`); readErr != nil {
		t.Fatalf("read of an already-approved write target asked again or failed: %v", readErr)
	}

	if _, err = wrapped(toolpkg.WithApprovedActionID(ctx, req.ActionID), arguments); err != nil {
		t.Fatalf("approved FOREBRAIN_HOME edit failed: %v", err)
	}
	raw, _ = os.ReadFile(target)
	if string(raw) != "bye\n" {
		t.Fatalf("approved edit did not land: %q", string(raw))
	}
}

// A subagent that needs approval raises the same prompt the primary agent
// does: its run suspends on the shared action queue and the operator answers
// in the approval overlay, which labels the requesting subagent.
func TestEditOutsideWorkspaceFromSubagentAsksForApproval(t *testing.T) {
	project, target := outsideWorkspaceFile(t)
	r, st := outsideWorkspaceRunner(t, project, safety.ApprovalOnRequest)
	for name, base := range map[string]context.Context{
		"typed subagent": toolpkg.WithSubagentType(context.Background(), "general-purpose"),
		"fork child":     toolpkg.WithForkChild(context.Background(), true),
	} {
		if err := os.WriteFile(target, []byte("hello\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		ctx := llm.WithAgentSessionID(base, "session-1")

		_, err := editOutsideWorkspace(t, r, st, ctx, target)
		var req *toolpkg.RequiresActionError
		if !errors.As(err, &req) {
			t.Fatalf("%s: an edit outside the workspace must ask, got %v", name, err)
		}
		action, err := r.Actions.Get(context.Background(), req.ActionID)
		if err != nil || action == nil {
			t.Fatalf("%s: pending action: %v", name, err)
		}
		if _, err := r.Actions.Approve(context.Background(), req.ActionID, "approved"); err != nil {
			t.Fatal(err)
		}
		if _, err := editOutsideWorkspace(t, r, st, toolpkg.WithApprovedActionID(ctx, req.ActionID), target); err != nil {
			t.Fatalf("%s: approved edit must apply: %v", name, err)
		}
		raw, _ := os.ReadFile(target)
		if string(raw) != "bye\n" {
			t.Fatalf("%s: file content = %q, want %q", name, string(raw), "bye\n")
		}
	}
}

// request_permissions used to be refused outright for any child run, so a
// subagent that needed access it did not have was told the question could not
// be asked. It now suspends on the shared action queue like any other gate, and
// the action names the worker so the operator knows who is waiting.
func TestRequestPermissionsFromSubagentAsksForApproval(t *testing.T) {
	project, target := outsideWorkspaceFile(t)
	r, st := outsideWorkspaceRunner(t, project, safety.ApprovalOnRequest)
	permTool, err := toolpkg.NewRequestPermissionsTool(st, &toolpkg.AgentToolRuntime{Actions: r.Actions})
	if err != nil {
		t.Fatal(err)
	}
	args := `{"reason":"read the source the task named","permissions":{"file_system":{"read":["` +
		strings.ReplaceAll(filepath.Dir(target), `\`, `\\`) + `"]}}}`

	for name, base := range map[string]context.Context{
		"typed subagent": toolpkg.WithSubagentType(context.Background(), "explore"),
		"fork child":     toolpkg.WithForkChild(context.Background(), true),
	} {
		ctx := toolpkg.WithProjectRoot(llm.WithAgentSessionID(base, "session-1"), project)
		_, err := permTool.Handle(ctx, args)
		var req *toolpkg.RequiresActionError
		if !errors.As(err, &req) {
			t.Fatalf("%s: request_permissions must ask, got %v", name, err)
		}
		action, err := r.Actions.Get(context.Background(), req.ActionID)
		if err != nil || action == nil {
			t.Fatalf("%s: pending action: %v", name, err)
		}
		if action.Kind != "request_permissions" {
			t.Fatalf("%s: action kind = %q", name, action.Kind)
		}
	}
}

// A read-only subagent type is blocked from editing at all, by tool policy
// rather than by a path boundary — that refusal is not an approval question.
func TestEditFromReadOnlySubagentStaysBlocked(t *testing.T) {
	project, target := outsideWorkspaceFile(t)
	r, st := outsideWorkspaceRunner(t, project, safety.ApprovalOnRequest)
	ctx := llm.WithAgentSessionID(toolpkg.WithSubagentType(context.Background(), "explore"), "session-1")

	_, err := editOutsideWorkspace(t, r, st, ctx, target)
	if err == nil || !strings.Contains(err.Error(), "blocked by policy") {
		t.Fatalf("read-only subagent must stay blocked, got %v", err)
	}
	raw, _ := os.ReadFile(target)
	if string(raw) != "hello\n" {
		t.Fatalf("file was modified: %q", string(raw))
	}
}

func TestSanitizePathSegment(t *testing.T) {
	if sanitizePathSegment("ab/cd") != "ab_cd" {
		t.Fatalf("got %q", sanitizePathSegment("ab/cd"))
	}
}

// guardianScriptLLM replays scripted guardian verdicts. pkg/safety has its own
// copy for the reviewer's own tests; this one serves the Runner-level tests
// that check how the guardian is wired in, and the two packages' test binaries
// are independent.
type guardianScriptLLM struct {
	outputs []string
	err     error
	calls   int
	tools   [][]*llm.Tool
	prompts []string
}

func (g *guardianScriptLLM) Execute(_ context.Context, messages []llm.Message, tools []*llm.Tool) (*llm.Result, error) {
	g.calls++
	g.tools = append(g.tools, tools)
	for _, m := range messages {
		g.prompts = append(g.prompts, m.TextContent())
	}
	if g.err != nil {
		return nil, g.err
	}
	out := ""
	if len(g.outputs) > 0 {
		out = g.outputs[0]
		if len(g.outputs) > 1 {
			g.outputs = g.outputs[1:]
		}
	}
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text(out)})
	return &llm.Result{Message: &msg}, nil
}

func TestActionHookUsesGuardianForOnRequestButNotExitPlan(t *testing.T) {
	client := &guardianScriptLLM{outputs: []string{`{"risk_level":"low","user_authorization":"high","outcome":"allow","rationale":"requested"}`}}
	store := safety.NewStore()
	store.SetApprovalPolicy(safety.ApprovalPolicy{Mode: safety.ApprovalOnRequest})
	permRT := safety.NewRuntimeWithStore(store)
	permRT.SetGuardian(client, "")
	runner := &Runner{Deps: &Deps{AppCfg: &appcfg.Root{ApprovalsReviewer: "auto_review"}}, permRuntime: permRT}
	_, pending, err := runner.actionHook(context.Background(), "shell", map[string]any{
		"command":             "make release",
		"force_tool_approval": true,
		"sandbox_permissions": "require_escalated",
	})
	if err != nil || pending {
		t.Fatalf("guardian allow should proceed: pending=%v err=%v", pending, err)
	}
	if client.calls != 1 {
		t.Fatalf("guardian calls=%d want=1", client.calls)
	}
	_, _, err = runner.actionHook(context.Background(), "exit_plan_mode", map[string]any{"session_id": "s1"})
	if err == nil || !strings.Contains(err.Error(), "actions service is unavailable") {
		t.Fatalf("exit_plan_mode must retain user approval path, got %v", err)
	}
	if client.calls != 1 {
		t.Fatal("guardian must not review exit_plan_mode")
	}
}

func TestRequestPermissionsGuardianDenialUsesEmptyGrantSignal(t *testing.T) {
	client := &guardianScriptLLM{outputs: []string{`{"risk_level":"high","user_authorization":"unknown","outcome":"deny","rationale":"not authorized"}`}}
	store := safety.NewStore()
	store.SetApprovalPolicy(safety.ApprovalPolicy{Mode: safety.ApprovalOnRequest})
	permRT := safety.NewRuntimeWithStore(store)
	permRT.SetGuardian(client, "")
	runner := &Runner{Deps: &Deps{AppCfg: &appcfg.Root{ApprovalsReviewer: "auto_review"}}, permRuntime: permRT}
	_, pending, err := runner.actionHook(context.Background(), "request_permissions", map[string]any{
		"request_permissions": true,
		"permissions":         map[string]any{"file_system": map[string]any{"read": []string{"/tmp/external"}}},
	})
	if pending || !errors.Is(err, toolpkg.ErrRequestPermissionsDenied) {
		t.Fatalf("guardian denial must resolve to the dedicated empty-grant signal: pending=%v err=%v", pending, err)
	}
}

func TestStrictAutoReviewForcesGuardianUnderNeverPolicy(t *testing.T) {
	client := &guardianScriptLLM{outputs: []string{`{"risk_level":"low","user_authorization":"high","outcome":"allow","rationale":"approved for this turn"}`}}
	store := safety.NewStore()
	store.SetApprovalPolicy(safety.ApprovalPolicy{Mode: safety.ApprovalNever})
	store.EnableStrictAutoReview("run-strict")
	store.SetSandboxAvailable(true)
	permRT := safety.NewRuntimeWithStore(store)
	permRT.SetGuardian(client, "")
	runner := &Runner{Deps: &Deps{AppCfg: &appcfg.Root{ApprovalsReviewer: "user"}}, permRuntime: permRT}
	ctx := toolpkg.WithRunID(context.Background(), "run-strict")
	_, pending, err := runner.actionHook(ctx, "shell", map[string]any{"command": "make release"})
	if err != nil || pending {
		t.Fatalf("strict review should allow the reviewed command: pending=%v err=%v", pending, err)
	}
	if client.calls != 1 {
		t.Fatalf("guardian calls=%d want=1", client.calls)
	}

	_, pending, err = runner.actionHook(toolpkg.WithRunID(context.Background(), "another-run"), "shell", map[string]any{"command": "make release"})
	if err != nil || pending {
		t.Fatalf("never policy should run sandboxed shell without prompting: pending=%v err=%v", pending, err)
	}
	if client.calls != 1 {
		t.Fatal("strict review must remain scoped to its run")
	}
}

// A subagent's sidechain log is written under the factory's workspace root, and
// the SubagentStop hook is later handed a path rebuilt from the hook runtime's
// root. Both are seeded from Runner.workspaceRoot, and Factory has its own
// fallback chain — if the two ever drift apart the hook is handed a path to a
// file that does not exist, silently and without an error anywhere.
func TestSubagentSidechainWriterAndHookReaderAgree(t *testing.T) {
	home := t.TempDir()
	r := &Runner{Deps: &Deps{Home: home, WorkspaceRoot: filepath.Join(home, "workspaces", "acme")}}

	// Writer: Run(RunParams{WorkspaceRoot: fac.workspaceRoot()}).
	writer := SidechainFilePath(r.subagentFactory().workspaceRoot(), "s1", "subagent", "agent-7")
	// Reader: hook.SidechainTranscriptPath(rt.StateRoot(), ...), where the
	// runtime is built with WorkspaceRoot: r.workspaceRoot().
	rt := &hook.Runtime{Home: r.Home, WorkspaceRoot: r.workspaceRoot()}
	reader := hook.SidechainTranscriptPath(rt.StateRoot(), "s1", "agent-7")

	if writer != reader {
		t.Fatalf("sidechain writer wrote %s but the hook is told %s", writer, reader)
	}
	if strings.HasPrefix(filepath.Clean(writer), filepath.Join(home, "state")) {
		t.Fatalf("sidechain %s leaks into the shared home state tree", writer)
	}
	if !strings.HasPrefix(filepath.Clean(writer), filepath.Clean(r.WorkspaceRoot)) {
		t.Fatalf("sidechain %s is outside the agent workspace %s", writer, r.WorkspaceRoot)
	}
}

func TestRehydrateImageReferences_NoResolver(t *testing.T) {
	entries := []state.TranscriptEntry{{
		Message:  llm.UserMessage(llm.Text("[attachment] a.png (image/png)")),
		FileRefs: []state.FileRefInfo{{FileID: "/tmp/a.png", Label: "a.png", MIMEType: "image/png"}},
	}}
	msgs := rehydrateImageReferences(context.Background(), nil, entries)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	// nil resolver: keep text placeholder
	if msgs[0].TextContent() != "[attachment] a.png (image/png)" {
		t.Fatalf("expected text placeholder, got %q", msgs[0].TextContent())
	}
}

func TestRehydrateImageReferences_ImageReplaced(t *testing.T) {
	// Create a tiny valid PNG: 1x1 pixel
	imgPath := filepath.Join(t.TempDir(), "test.png")
	// Minimal PNG: 1x1 red pixel
	png := []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, // signature
		0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52, // IHDR chunk
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53,
		0xDE, 0x00, 0x00, 0x00, 0x0C, 0x49, 0x44, 0x41, // IDAT chunk
		0x54, 0x08, 0xD7, 0x63, 0xF8, 0xCF, 0xC0, 0x00,
		0x00, 0x00, 0x03, 0x00, 0x01, 0x47, 0x6D, 0x54,
		0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, 0x44, // IEND chunk
		0xAE, 0x42, 0x60, 0x82,
	}
	if err := os.WriteFile(imgPath, png, 0o644); err != nil {
		t.Fatal(err)
	}

	entries := []state.TranscriptEntry{{
		Message:  llm.UserMessage(llm.Text("[attachment] test.png (image/png)")),
		FileRefs: []state.FileRefInfo{{FileID: imgPath, Label: "test.png", MIMEType: "image/png"}},
	}}
	msgs := rehydrateImageReferences(context.Background(), PathResolver{}, entries)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	// The text placeholder should be replaced by an ImageBase64 part
	parts := msgs[0].Parts
	if len(parts) != 1 {
		t.Fatalf("expected 1 part, got %d: %v", len(parts), parts)
	}
	if parts[0].Type != llm.ContentTypeImageBase64 {
		t.Fatalf("expected ImageBase64 part, got type=%s", parts[0].Type)
	}
	if parts[0].MIMEType != "image/png" {
		t.Fatalf("expected image/png, got %s", parts[0].MIMEType)
	}
	if parts[0].ImageBase64 == "" {
		t.Fatal("expected non-empty base64 data")
	}
}

func TestRehydrateImageReferences_NonImageMIME(t *testing.T) {
	entries := []state.TranscriptEntry{{
		Message:  llm.UserMessage(llm.Text("[attachment] doc.pdf (application/pdf)")),
		FileRefs: []state.FileRefInfo{{FileID: "/tmp/doc.pdf", Label: "doc.pdf", MIMEType: "application/pdf"}},
	}}
	msgs := rehydrateImageReferences(context.Background(), PathResolver{}, entries)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	// Non-image: placeholder preserved
	if msgs[0].TextContent() != "[attachment] doc.pdf (application/pdf)" {
		t.Fatalf("expected text placeholder, got %q", msgs[0].TextContent())
	}
}

func TestRehydrateImageReferences_MissingFile(t *testing.T) {
	imgPath := filepath.Join(t.TempDir(), "nonexistent.png")
	entries := []state.TranscriptEntry{{
		Message:  llm.UserMessage(llm.Text("[attachment] nonexistent.png (image/png)")),
		FileRefs: []state.FileRefInfo{{FileID: imgPath, Label: "nonexistent.png", MIMEType: "image/png"}},
	}}
	msgs := rehydrateImageReferences(context.Background(), PathResolver{}, entries)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	// Missing file: placeholder preserved
	if !strings.Contains(msgs[0].TextContent(), "[attachment]") {
		t.Fatalf("expected placeholder preserved, got %q", msgs[0].TextContent())
	}
}

func TestRehydrateImageReferences_MultipleImages(t *testing.T) {
	dir := t.TempDir()
	// Create two tiny PNG files
	png := []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53,
		0xDE, 0x00, 0x00, 0x00, 0x0C, 0x49, 0x44, 0x41, 0x54, 0x08, 0xD7, 0x63, 0xF8, 0xCF, 0xC0, 0x00,
		0x00, 0x00, 0x03, 0x00, 0x01, 0x47, 0x6D, 0x54, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, 0x44,
		0xAE, 0x42, 0x60, 0x82,
	}
	img1 := filepath.Join(dir, "a.png")
	img2 := filepath.Join(dir, "b.png")
	os.WriteFile(img1, png, 0o644)
	os.WriteFile(img2, png, 0o644)

	entries := []state.TranscriptEntry{{
		Message: llm.UserMessage(
			llm.Text("[attachment] a.png (image/png)"),
			llm.Text("[attachment] b.png (image/png)"),
		),
		FileRefs: []state.FileRefInfo{
			{FileID: img1, Label: "a.png", MIMEType: "image/png"},
			{FileID: img2, Label: "b.png", MIMEType: "image/png"},
		},
	}}
	msgs := rehydrateImageReferences(context.Background(), PathResolver{}, entries)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	parts := msgs[0].Parts
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts, got %d: %v", len(parts), parts)
	}
	if parts[0].Type != llm.ContentTypeImageBase64 {
		t.Fatalf("part 0: expected ImageBase64, got %s", parts[0].Type)
	}
	if parts[1].Type != llm.ContentTypeImageBase64 {
		t.Fatalf("part 1: expected ImageBase64, got %s", parts[1].Type)
	}
}

func TestRehydrateImageReferences_RelativePath(t *testing.T) {
	entries := []state.TranscriptEntry{{
		Message:  llm.UserMessage(llm.Text("[attachment] img.png (image/png)")),
		FileRefs: []state.FileRefInfo{{FileID: "img.png", Label: "img.png", MIMEType: "image/png"}},
	}}
	msgs := rehydrateImageReferences(context.Background(), PathResolver{}, entries)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	// Relative path: PathResolver rejects (not absolute), placeholder preserved
	if !strings.Contains(msgs[0].TextContent(), "[attachment]") {
		t.Fatalf("expected placeholder preserved for relative path, got %q", msgs[0].TextContent())
	}
}

func TestFileReferencePlaceholder(t *testing.T) {
	ref := state.FileRefInfo{FileID: "/tmp/a.png", Label: "a.png", MIMEType: "image/png"}
	got := fileReferencePlaceholder(ref)
	want := "[attachment] a.png (image/png)"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}

	ref2 := state.FileRefInfo{FileID: "/tmp/nolabel", Label: "", MIMEType: "image/png"}
	got2 := fileReferencePlaceholder(ref2)
	want2 := "[attachment] /tmp/nolabel (image/png)"
	if got2 != want2 {
		t.Fatalf("expected %q, got %q", want2, got2)
	}
}

func TestInputQueuePreservesQueueOrder(t *testing.T) {
	q := NewInputQueue()
	q.Attach(NewTurnInputRuntime())
	if !q.Steer(Input{Text: "first"}) || !q.FollowUp(Input{Text: "next"}) {
		t.Fatal("queue input")
	}
	if got := q.Preview(); len(got.Steers) != 1 || len(got.FollowUp) != 1 {
		t.Fatalf("preview = %#v", got)
	}
	q.Detach()
	send, _ := q.Next(BoundaryCompleted)
	if len(send) != 1 || send[0].Text != "first" || send[0].Rejected {
		t.Fatalf("steer sent after its turn = %#v", send)
	}
	send, _ = q.Next(BoundaryCompleted)
	if len(send) != 1 || send[0].Text != "next" || send[0].Rejected {
		t.Fatalf("follow-up = %#v", send)
	}
}

func TestInputQueueRecallPicksNewestByEnqueueClock(t *testing.T) {
	q := NewInputQueue()
	q.Attach(NewTurnInputRuntime())
	if !q.Steer(Input{Parts: []llm.ContentPart{llm.Text("steer")}}) {
		t.Fatal("steer")
	}
	if !q.FollowUp(Input{Attachments: []string{" /tmp/shot.png "}}) {
		t.Fatal("follow-up")
	}
	// The follow-up was queued after the steer, so it is the newest — recall
	// picks it even though a steer is also waiting.
	in, ok := q.Recall()
	if !ok || len(in.Attachments) != 1 || in.Attachments[0] != "/tmp/shot.png" {
		t.Fatalf("attachment = %#v, %v", in, ok)
	}
	in, ok = q.Recall()
	if !ok || in.Text != "steer" {
		t.Fatalf("steer = %#v, %v", in, ok)
	}
}

func TestInputQueueIgnoresEmptyAttachments(t *testing.T) {
	q := NewInputQueue()
	q.Attach(NewTurnInputRuntime())
	if !q.Steer(Input{Text: "steer", Attachments: []string{" "}}) {
		t.Fatal("empty attachment must not reject steer")
	}
	if q.FollowUp(Input{Attachments: []string{" "}}) {
		t.Fatal("empty attachment must not create follow-up")
	}
}

func TestInputQueueSteerKeepsRichParts(t *testing.T) {
	q := NewInputQueue()
	q.Attach(NewTurnInputRuntime())
	if !q.Steer(Input{Parts: []llm.ContentPart{llm.ImageURL("https://example.com/image.png")}}) {
		t.Fatal("rich steer")
	}
	if got := q.Runtime().Snapshot(); len(got) != 1 || got[0].Parts[0].ImageURL == "" {
		t.Fatalf("runtime = %#v", got)
	}
}

type scriptedUsageLLM struct {
	result *llm.Result
	err    error
}

func (m scriptedUsageLLM) Execute(context.Context, []llm.Message, []*llm.Tool) (*llm.Result, error) {
	return m.result, m.err
}

func TestUsageAccountingLLMReportsUnaryUsageThroughSinkAndAccumulator(t *testing.T) {
	acc := &internalLLMUsageAccumulator{}
	ctx := WithInternalLLMUsageAccumulator(context.Background(), acc)
	var gotIn, gotOut int
	ctx = llm.WithStreamSink(ctx, &llm.StreamSink{
		OnUsage: func(in, out int) {
			gotIn += in
			gotOut += out
		},
	})
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("ok")})
	inner := wrapUsageAccountingLLM(scriptedUsageLLM{
		result: &llm.Result{
			Message: &msg,
			Usage:   &llm.Usage{InputTokens: 11, OutputTokens: 7},
		},
	})
	res, err := inner.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if res == nil || res.Usage == nil {
		t.Fatal("expected usage on result")
	}
	if gotIn != 11 || gotOut != 7 {
		t.Fatalf("sink usage = %d/%d, want 11/7", gotIn, gotOut)
	}
	if snap := acc.Snapshot(); snap.InputTokens != 11 || snap.OutputTokens != 7 {
		t.Fatalf("accumulator = %+v, want input=11 output=7", snap)
	}
}

func TestUsageAccountingLLMIncludesCacheTokensInSinkContextUsage(t *testing.T) {
	acc := &internalLLMUsageAccumulator{}
	ctx := WithInternalLLMUsageAccumulator(context.Background(), acc)
	var gotIn, gotOut int
	var snapshotIn, snapshotOut int
	ctx = llm.WithStreamSink(ctx, &llm.StreamSink{
		OnUsage: func(in, out int) {
			gotIn += in
			gotOut += out
		},
		OnUsageSnapshot: func(in, out int) {
			snapshotIn = in
			snapshotOut = out
		},
	})
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("ok")})
	inner := wrapUsageAccountingLLM(scriptedUsageLLM{
		result: &llm.Result{
			Message: &msg,
			Usage: &llm.Usage{
				InputTokens:              46,
				CacheCreationInputTokens: 5517,
				CacheReadInputTokens:     30849,
				OutputTokens:             222,
			},
		},
	})
	if _, err := inner.Execute(ctx, nil, nil); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gotIn != 36412 || gotOut != 222 {
		t.Fatalf("sink usage = %d/%d, want 36412/222", gotIn, gotOut)
	}
	if snapshotIn != 36412 || snapshotOut != 222 {
		t.Fatalf("sink snapshot = %d/%d, want 36412/222", snapshotIn, snapshotOut)
	}
	if snap := acc.Snapshot(); snap.InputTokens != 46 || snap.CacheCreationInputTokens != 5517 ||
		snap.CacheReadInputTokens != 30849 || snap.OutputTokens != 222 {
		t.Fatalf("accumulator = %+v", snap)
	}
}

func TestUsageAccountingLLMAttachesErrorUsageToAccumulator(t *testing.T) {
	acc := &internalLLMUsageAccumulator{}
	ctx := WithInternalLLMUsageAccumulator(context.Background(), acc)
	var gotIn, gotOut int
	ctx = llm.WithStreamSink(ctx, &llm.StreamSink{
		OnUsage: func(in, out int) {
			gotIn += in
			gotOut += out
		},
	})
	wantErr := llm.WrapErrorWithUsageForTest(errors.New("boom"), &llm.Usage{InputTokens: 5, OutputTokens: 3})
	inner := wrapUsageAccountingLLM(scriptedUsageLLM{err: wantErr})
	_, err := inner.Execute(ctx, nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if gotIn != 5 || gotOut != 3 {
		t.Fatalf("sink usage = %d/%d, want 5/3", gotIn, gotOut)
	}
	if snap := acc.Snapshot(); snap.InputTokens != 5 || snap.OutputTokens != 3 {
		t.Fatalf("accumulator = %+v, want input=5 output=3", snap)
	}
}

func TestNestedUsageAccumulatorPropagatesToOuterAccumulator(t *testing.T) {
	outer := llm.NewUsageAccumulator()
	ctx := llm.WithUsageAccumulator(context.Background(), outer)
	inner := llm.NewUsageAccumulator()
	ctx = llm.WithUsageAccumulator(ctx, inner)

	inner.Observe(8, 6)

	if snap := inner.Snapshot(); snap.InputTokens != 8 || snap.OutputTokens != 6 {
		t.Fatalf("inner accumulator = %+v, want input=8 output=6", snap)
	}
	if snap := outer.Snapshot(); snap.InputTokens != 8 || snap.OutputTokens != 6 {
		t.Fatalf("outer accumulator = %+v, want input=8 output=6", snap)
	}
}

// TestUsageAccountingLLMSkipsSinkWhenNoAccumulator verifies that an LLM call
// running with a TUI sink but no run accumulator (e.g. the goal-continuation
// evaluator in the supervisor runCtx) does NOT forward usage to the sink. Such
// calls have no accumulator to reach res.Summary.Usage, so forwarding to the
// sink would leak into the live token stream (Working line / footer) without
// reaching the authoritative final, making Working overshoot Worked-for and -
// after end-of-run reconciliation - collapsing the cumulative footer onto the
// single-run final.
func TestUsageAccountingLLMSkipsSinkWhenNoAccumulator(t *testing.T) {
	var gotIn, gotOut int
	ctx := llm.WithStreamSink(context.Background(), &llm.StreamSink{
		OnUsage: func(in, out int) {
			gotIn += in
			gotOut += out
		},
	})
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("ok")})
	inner := wrapUsageAccountingLLM(scriptedUsageLLM{
		result: &llm.Result{
			Message: &msg,
			Usage:   &llm.Usage{InputTokens: 11, OutputTokens: 7},
		},
	})
	res, err := inner.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if res == nil || res.Usage == nil {
		t.Fatal("expected usage on result")
	}
	if gotIn != 0 || gotOut != 0 {
		t.Fatalf("sink usage = %d/%d, want 0/0 (no accumulator => no sink report)", gotIn, gotOut)
	}
}

func TestUsageAccountingWrapperDoesNotAdvertiseRemoteCompactionForLocalClient(t *testing.T) {
	wrapped := wrapUsageAccountingLLM(scriptedUsageLLM{})
	if _, ok := wrapped.(llm.ContextCompactor); ok {
		t.Fatalf("local client wrapper %T incorrectly advertises remote compaction", wrapped)
	}
}

// TestConcurrentLoadAndPermissionEvaluationDoNotRace pins a property the R2
// and R3 lock splits both had to preserve: Load's reassignment of the
// permission runtime must stay mutually exclusive with a concurrent
// permission evaluation reading it. Before R2 a single r.mu covered both for
// free; R2 (briefly) restored that by nesting a perm sub-lock inside
// loadLocked; R3 moved permissionStore/permissionEngine into safety.Runtime,
// which now guards them with its own internal lock instead, so Runner no
// longer needs to borrow any lock of its own for this. This test's job is to
// run Load and permission evaluation concurrently under -race so a future
// regression in whichever mechanism is current fails loudly instead of
// merely by inspection.
func TestConcurrentLoadAndPermissionEvaluationDoNotRace(t *testing.T) {
	home := t.TempDir()
	r := &Runner{Deps: &Deps{Home: home, AppCfg: &appcfg.Root{
		Agents: appcfg.AgentsSection{
			Definitions: map[string]appcfg.AgentDefinition{
				"main": {
					LLMProviders: []appcfg.AgentLLMProviderConfig{{
						Provider: "openai",
						Model:    "qwen-test",
						APIKey:   "test-key",
						BaseURL:  "http://127.0.0.1:9/v1",
					}},
				},
			},
		},
	}}}
	// Deliberately do not pre-Load: the permission runtime's store/engine
	// start nil, so the first concurrent Load and the first concurrent
	// permission evaluation race to initialize them via safety.Runtime's
	// check-then-create. A pre-populated store would make every later Load a
	// no-op reassignment and hide exactly the race this test exists to catch.
	const iterations = 50
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			_ = r.Load()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			_ = r.EvaluatePermissionForSession("session-a", "shell", "go test ./...")
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			_ = r.ExplainPermissionForSession("session-b", "edit_file", "/repo/main.go")
			_ = r.PermissionSnapshot()
		}
	}()
	wg.Wait()
}

func TestMain(m *testing.M) {
	if cat, err := llm.Parse(llm.EmbeddedModelsJSON()); err == nil {
		llm.SetGlobalCatalogForTest(cat)
	}
	os.Exit(m.Run())
}

func TestMCPAnnotationsDefaultConservatively(t *testing.T) {
	missing := inferMCPToolMeta("server", "tool", map[string]any{})
	if !missing.Destructive || missing.ReadOnly || missing.DestructiveHint != nil {
		t.Fatalf("missing annotations must default destructive: %+v", missing)
	}

	readOnly := inferMCPToolMeta("server", "tool", map[string]any{
		"annotations": map[string]any{"readOnlyHint": true, "openWorldHint": false},
	})
	if readOnly.Destructive || !readOnly.ReadOnly || readOnly.ReadOnlyHint == nil || !*readOnly.ReadOnlyHint {
		t.Fatalf("read-only annotation not honored: %+v", readOnly)
	}
	if readOnly.OpenWorldHint == nil || *readOnly.OpenWorldHint {
		t.Fatalf("open-world hint was not preserved: %+v", readOnly)
	}

	destructiveWins := inferMCPToolMeta("server", "tool", map[string]any{
		"annotations": map[string]any{"readOnlyHint": true, "destructiveHint": true},
	})
	if !destructiveWins.Destructive || destructiveWins.ReadOnly {
		t.Fatalf("destructive=true must win: %+v", destructiveWins)
	}

	explicitNonDestructive := inferMCPToolMeta("server", "tool", map[string]any{
		"annotations": map[string]any{"destructiveHint": false, "idempotentHint": true},
	})
	if !explicitNonDestructive.Destructive || explicitNonDestructive.ReadOnly || explicitNonDestructive.IdempotentHint == nil || !*explicitNonDestructive.IdempotentHint {
		t.Fatalf("missing open-world annotation must remain approval-gated: %+v", explicitNonDestructive)
	}

	explicitClosedWorld := inferMCPToolMeta("server", "tool", map[string]any{
		"annotations": map[string]any{"destructiveHint": false, "openWorldHint": false, "idempotentHint": true},
	})
	if explicitClosedWorld.Destructive || !explicitClosedWorld.ReadOnly || !explicitClosedWorld.ConcurrencySafe {
		t.Fatalf("closed-world non-destructive annotations not honored: %+v", explicitClosedWorld)
	}
}

func TestMCPAnnotations_ConcurrencySafeOnlyWhenIdempotentAndReadOnly(t *testing.T) {
	// readOnlyHint=true but no idempotentHint → not concurrency-safe
	readOnlyOnly := inferMCPToolMeta("server", "tool", map[string]any{
		"annotations": map[string]any{"readOnlyHint": true},
	})
	if readOnlyOnly.ConcurrencySafe {
		t.Fatal("read-only without idempotent should not be concurrency-safe")
	}

	// idempotentHint=true but not readOnly → not concurrency-safe
	idempotentOnly := inferMCPToolMeta("server", "tool", map[string]any{
		"annotations": map[string]any{"idempotentHint": true},
	})
	if idempotentOnly.ConcurrencySafe {
		t.Fatal("idempotent without read-only should not be concurrency-safe")
	}

	// both readOnlyHint=true and idempotentHint=true → concurrency-safe
	readOnlyAndIdempotent := inferMCPToolMeta("server", "tool", map[string]any{
		"annotations": map[string]any{"readOnlyHint": true, "idempotentHint": true},
	})
	if !readOnlyAndIdempotent.ConcurrencySafe {
		t.Fatal("read-only + idempotent should be concurrency-safe")
	}

	// A non-destructive hint still needs an explicit closed-world hint.
	nonDestructiveAndIdempotent := inferMCPToolMeta("server", "tool", map[string]any{
		"annotations": map[string]any{"destructiveHint": false, "idempotentHint": true},
	})
	if nonDestructiveAndIdempotent.ConcurrencySafe {
		t.Fatal("missing open-world hint must not be concurrency-safe")
	}
}

type noopLLM struct{}

func (noopLLM) Execute(context.Context, []llm.Message, []*llm.Tool) (*llm.Result, error) {
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("ok")})
	return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
}

func TestPipelineParts(t *testing.T) {
	orig := []llm.ContentPart{llm.ImageURL("https://example.test/a.png"), llm.Text("old")}
	got := pipelineParts("new", orig)
	if got[1].Text != "new" || orig[1].Text != "old" {
		t.Fatalf("parts=%#v original=%#v", got, orig)
	}
	got = pipelineParts("first", []llm.ContentPart{llm.ImageBase64("image/png", "AAAA")})
	if len(got) != 2 || got[0].Type != llm.ContentTypeText || got[0].Text != "first" {
		t.Fatalf("parts=%#v", got)
	}
}

type pipelineLLM struct {
	replies []string
	seen    [][]llm.ContentPart
}

func (s *pipelineLLM) Execute(_ context.Context, msgs []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	if len(msgs) > 0 {
		s.seen = append(s.seen, append([]llm.ContentPart(nil), msgs[len(msgs)-1].Parts...))
	}
	reply := "ok"
	if len(s.replies) > 0 {
		reply, s.replies = s.replies[0], s.replies[1:]
	}
	return &llm.Result{Message: &llm.Message{Role: llm.RoleAssistant, Parts: []llm.ContentPart{llm.Text(reply)}}}, nil
}

func pipelineRunner(t *testing.T, l *pipelineLLM) *Runner {
	t.Helper()
	a, err := agent.New(l, "main", "desc")
	if err != nil {
		t.Fatal(err)
	}
	return &Runner{Deps: &Deps{Home: t.TempDir(), AgentName: "main", AppCfg: &appcfg.Root{Agents: appcfg.AgentsSection{Definitions: map[string]appcfg.AgentDefinition{"main": {}}}}}, main: a, mainCfg: AgentConfigYAML{Description: "desc"}}
}

func TestRunPipeline(t *testing.T) {
	if _, _, err := RunPipeline(context.Background(), nil, nil, hook.HookContext{}, "x", nil); err == nil || !strings.Contains(err.Error(), "nil runner") {
		t.Fatalf("err=%v", err)
	}
	l := &pipelineLLM{replies: []string{"plain"}}
	p := hook.NewAgentPipeline()
	p.Add("pre", func(_ context.Context, phase string, hc hook.HookContext, text string) (hook.PreHookResult, error) {
		if phase != "pre" || hc.SessionID != "s1" {
			t.Fatalf("phase=%q context=%+v", phase, hc)
		}
		return hook.PreHookResult{Text: text + " hooked"}, nil
	})
	res, pre, err := RunPipeline(context.Background(), pipelineRunner(t, l), p, hook.HookContext{SessionID: "s1"}, "input", nil)
	if err != nil || res.TextContent() != "plain" || pre.Text != "input hooked" || len(l.seen) == 0 || llm.TextContent(l.seen[0]...) != "input hooked" {
		t.Fatalf("res=%v pre=%+v seen=%#v err=%v", res, pre, l.seen, err)
	}

	l = &pipelineLLM{replies: []string{"content"}}
	res, pre, err = RunPipeline(context.Background(), pipelineRunner(t, l), nil, hook.HookContext{}, "content text", []llm.ContentPart{llm.ImageURL("https://example.test/i.png")})
	if err != nil || res.TextContent() != "content" || pre.Text != "content text" || len(l.seen) == 0 || len(l.seen[0]) != 2 || l.seen[0][0].Text != "content text" {
		t.Fatalf("res=%v pre=%+v seen=%#v err=%v", res, pre, l.seen, err)
	}
}

func TestRunPipelinePreHookError(t *testing.T) {
	want := errors.New("pre failed")
	p := hook.NewAgentPipeline()
	p.Add("pre", func(context.Context, string, hook.HookContext, string) (hook.PreHookResult, error) {
		return hook.PreHookResult{}, want
	})
	if _, _, err := RunPipeline(context.Background(), pipelineRunner(t, &pipelineLLM{}), p, hook.HookContext{}, "x", nil); !errors.Is(err, want) {
		t.Fatalf("err=%v", err)
	}
}

// TestPlanModeE2EFullCycle exercises the cross-package plan-mode lifecycle:
// enter_plan_mode tool → ActionHook approval → modestore stash →
// planModeLLM injects per-turn reminder + AllowedPlanPath ctx →
// GuardWrite blocks non-plan paths but allows the plan file →
// exit_plan_mode tool → ActionHook approval → restored mode + prompt-rules persisted.
func TestPlanModeE2EFullCycle(t *testing.T) {
	home := t.TempDir()
	sid := "e2e-plan-cycle"
	baseCtx := llm.WithAgentSessionID(context.Background(), sid)

	st := toolpkg.NewState()
	hookCalls := map[string]int{}
	st.SetActionHook(func(ctx context.Context, kind string, payload any) (string, bool, error) {
		hookCalls[kind]++
		if kind == "exit_plan_mode" && toolpkg.ApprovedActionIDFromContext(ctx) != "" {
			return toolpkg.ApprovedActionIDFromContext(ctx), false, nil
		}
		return "act-" + kind, true, nil
	})

	enterTool, exitTool, err := toolpkg.NewPlanModeTools(st, home)
	if err != nil {
		t.Fatalf("ctor: %v", err)
	}

	// --- Step 1: enter_plan_mode without prior approval → RequiresActionError ---
	if _, err := enterTool.Handle(baseCtx, `{}`); err == nil {
		t.Fatal("expected RequiresActionError for enter_plan_mode")
	} else {
		var rae *toolpkg.RequiresActionError
		if !errors.As(err, &rae) || rae.ActionKind != "enter_plan_mode" {
			t.Fatalf("expected RequiresActionError, got %T %v", err, err)
		}
	}
	if hookCalls["enter_plan_mode"] != 1 {
		t.Fatalf("enter hook calls=%d want 1", hookCalls["enter_plan_mode"])
	}

	// --- Step 2: approved enter_plan_mode mutates modestore and returns plan_file ---
	enterApproved := toolpkg.WithApprovedActionID(baseCtx, "act-enter_plan_mode")
	rawEnter, err := enterTool.Handle(enterApproved, `{}`)
	if err != nil {
		t.Fatalf("approved enter handle: %v", err)
	}
	var enterResp map[string]any
	if err := json.Unmarshal([]byte(rawEnter.(string)), &enterResp); err != nil {
		t.Fatalf("decode enter response: %v", err)
	}
	// When no plan exists yet, plan_file should be empty so the LLM must
	// choose a descriptive name instead of overwriting plan.md.
	planFile, _ := enterResp["plan_file"].(string)
	if planFile != "" {
		t.Fatalf("plan_file should be empty when no plan exists, got %q", planFile)
	}
	stPost, _ := state.Get(home, sid)
	if stPost.Mode != state.ModePlan {
		t.Fatalf("Mode=%v want plan", stPost.Mode)
	}
	if stPost.PrePlanMode != state.ModeAgent {
		t.Fatalf("PrePlanMode=%v want agent", stPost.PrePlanMode)
	}
	if stPost.PlanTurnCount != 0 {
		t.Fatalf("PlanTurnCount=%d want 0", stPost.PlanTurnCount)
	}

	// --- Step 3: planModeLLM injects full reminder + AllowedPlanPath on turn 0 ---
	innerLLM := &capturingLLM{}
	wrapped := wrapPlanModeLLM(innerLLM, home)
	if _, err := wrapped.Execute(baseCtx, []llm.Message{llm.UserMessage(llm.Text("design"))}, nil); err != nil {
		t.Fatalf("planModeLLM execute: %v", err)
	}
	if innerLLM.calls != 1 {
		t.Fatalf("inner not called: %d", innerLLM.calls)
	}
	body := innerLLM.gotMsgs[len(innerLLM.gotMsgs)-1].Parts[0].Text
	if !strings.Contains(body, "<system-reminder>") {
		t.Fatalf("missing reminder envelope: %q", body)
	}
	if !strings.Contains(body, "PLAN MODE ACTIVE") {
		t.Fatalf("turn-0 should get full reminder: %q", body)
	}
	allowed := toolpkg.AllowedPlanPathFromContext(innerLLM.gotCtx)
	wantDir := state.PlanDirForSession(home, "", sid)
	if allowed == "" || allowed != wantDir {
		t.Fatalf("AllowedPlanPath not wired: got %q want %q", allowed, wantDir)
	}
	// PlanTurnCount is no longer incremented by planModeLLM (deprecated in
	// favor of stateless transcript-derived counting). It stays at 0.
	stAfterTurn, _ := state.Get(home, sid)
	if stAfterTurn.PlanTurnCount != 0 {
		t.Fatalf("PlanTurnCount=%d want 0 (deprecated, not incremented)", stAfterTurn.PlanTurnCount)
	}

	// --- Step 4: write a descriptive-named plan file (as the LLM should) ---
	planDir := state.PlanDirForSession(home, "", sid)
	planFile = filepath.Join(planDir, "fix-cache-hit-rate.md")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatalf("mkdir plan dir: %v", err)
	}
	if err := os.WriteFile(planFile, []byte("# Plan\n- step 1\n"), 0o600); err != nil {
		t.Fatalf("write plan file: %v", err)
	}
	// PlanPath should now return the descriptive-named file, not plan.md.
	if got := state.PlanPathForSession(home, "", sid); got != planFile {
		t.Fatalf("PlanPath=%q want %q after writing descriptive plan", got, planFile)
	}

	// --- Step 5: GuardWrite blocks non-plan paths, allows plan file ---
	planCtx := toolpkg.WithMode(toolpkg.WithAllowedPlanPath(baseCtx, planDir), "plan")
	nonPlan := filepath.Join(home, "evil.txt")
	if err := st.GuardWrite(planCtx, nonPlan); err == nil || !strings.Contains(err.Error(), "plan mode: write blocked") {
		t.Fatalf("expected GuardWrite to block non-plan path, got %v", err)
	}
	if err := st.GuardWrite(planCtx, planFile); err != nil {
		t.Fatalf("GuardWrite blocked plan file: %v", err)
	}
	// A second, semantically-named plan file inside the dir is also writable.
	altPlan := filepath.Join(planDir, "revised-approach.md")
	if err := st.GuardWrite(planCtx, altPlan); err != nil {
		t.Fatalf("GuardWrite blocked new plan file in dir: %v", err)
	}
	// The generic plan.md name must also be blocked to prevent overwrites.
	genericPlan := filepath.Join(planDir, "plan.md")
	if err := st.GuardWrite(planCtx, genericPlan); err != nil {
		t.Fatalf("GuardWrite blocked generic plan.md (it should be allowed in plan dir): %v", err)
	}

	// --- Step 6: exit_plan_mode without approval → RequiresActionError ---
	if _, err := exitTool.Handle(baseCtx, `{}`); err == nil {
		t.Fatal("expected RequiresActionError for exit_plan_mode")
	} else {
		var rae2 *toolpkg.RequiresActionError
		if !errors.As(err, &rae2) || rae2.ActionKind != "exit_plan_mode" {
			t.Fatalf("expected RequiresActionError, got %T %v", err, err)
		}
	}
	if hookCalls["exit_plan_mode"] != 1 {
		t.Fatalf("exit hook calls=%d want 1", hookCalls["exit_plan_mode"])
	}

	// --- Step 7: approved exit_plan_mode restores PrePlanMode ---
	exitApproved := toolpkg.WithApprovedActionID(baseCtx, "act-exit_plan_mode")
	rawExit, err := exitTool.Handle(exitApproved, `{}`)
	if err != nil {
		t.Fatalf("approved exit handle: %v", err)
	}
	var exitResp map[string]any
	if err := json.Unmarshal([]byte(rawExit.(string)), &exitResp); err != nil {
		t.Fatalf("decode exit response: %v", err)
	}
	if exitResp["mode"] != "agent" {
		t.Fatalf("exit mode=%v want agent", exitResp["mode"])
	}
	stFinal, _ := state.Get(home, sid)
	if stFinal.Mode != state.ModeAgent {
		t.Fatalf("final Mode=%v want agent", stFinal.Mode)
	}
	if string(stFinal.PrePlanMode) != "" {
		t.Fatalf("PrePlanMode not cleared: %v", stFinal.PrePlanMode)
	}
	if stFinal.PlanTurnCount != 0 {
		t.Fatalf("PlanTurnCount not cleared: %d", stFinal.PlanTurnCount)
	}

	// --- Step 8: after exit the loop moves to its implementation half ---
	// Plan mode itself is over (no PLAN MODE reminder, writes are unrestricted
	// again), but the agent is now told to record the implementation status
	// back into the plan file, and the plan file stays writable so it can.
	innerLLM2 := &capturingLLM{}
	wrapped2 := wrapPlanModeLLM(innerLLM2, home)
	if _, err := wrapped2.Execute(baseCtx, []llm.Message{llm.UserMessage(llm.Text("plain"))}, nil); err != nil {
		t.Fatalf("post-exit execute: %v", err)
	}
	body2 := innerLLM2.gotMsgs[len(innerLLM2.gotMsgs)-1].Parts[0].Text
	if strings.Contains(body2, "PLAN MODE ACTIVE") {
		t.Fatalf("plan-mode reminder leaked after exit: %q", body2)
	}
	if !strings.Contains(body2, planImplementationReminderMarker) {
		t.Fatalf("implementation reminder missing after exit: %q", body2)
	}
	if err := st.GuardWrite(toolpkg.WithAllowedPlanPath(baseCtx, planDir), planFile); err != nil {
		t.Fatalf("plan file must stay writable after exit: %v", err)
	}

	// --- Step 9: recording the implementation closes the loop ---
	content, err := state.GetPlanForSession(home, "", sid)
	if err != nil {
		t.Fatalf("read plan: %v", err)
	}
	if err := state.SetPlanForSession(home, "", sid, content+"\n\n"+state.ImplementationHeading+"\n\nLanded in foo.go; go test ./... passes.\n"); err != nil {
		t.Fatalf("record implementation: %v", err)
	}
	innerLLM3 := &capturingLLM{}
	wrapped3 := wrapPlanModeLLM(innerLLM3, home)
	if _, err := wrapped3.Execute(baseCtx, []llm.Message{llm.UserMessage(llm.Text("plain"))}, nil); err != nil {
		t.Fatalf("post-record execute: %v", err)
	}
	body3 := innerLLM3.gotMsgs[len(innerLLM3.gotMsgs)-1].Parts[0].Text
	if strings.Contains(body3, "system-reminder") {
		t.Fatalf("reminder should stop once the plan records its implementation: %q", body3)
	}
}

func exitedPlanState(t *testing.T, home, sid string) {
	t.Helper()
	if err := state.Set(home, sid, state.State{
		Mode:          state.ModeAgent,
		HasExitedPlan: true,
	}); err != nil {
		t.Fatalf("state.Set: %v", err)
	}
}

func writePlan(t *testing.T, home, sid, content string) {
	t.Helper()
	if err := state.SetPlanForSession(home, "", sid, content); err != nil {
		t.Fatalf("planstore.SetPlanForSession: %v", err)
	}
}

func lastReminderBody(msgs []llm.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].IsMeta {
			return llm.TextContent(msgs[i].Parts...)
		}
	}
	return ""
}

func TestImplementationReminderInjectedAfterExitingPlanMode(t *testing.T) {
	home := t.TempDir()
	sid := "impl-1"
	exitedPlanState(t, home, sid)
	writePlan(t, home, sid, "# Plan\n\n- [ ] do the thing\n")

	inner := &capturingLLM{}
	w := wrapPlanModeLLM(inner, home)
	ctx := llm.WithAgentSessionID(context.Background(), sid)

	if _, err := w.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("go implement it"))}, nil); err != nil {
		t.Fatalf("execute: %v", err)
	}
	body := lastReminderBody(inner.gotMsgs)
	if !strings.Contains(body, planImplementationReminderMarker) {
		t.Fatalf("implementation reminder missing: %q", body)
	}
	if !strings.Contains(body, state.ImplementationHeading) {
		t.Fatalf("reminder must name the canonical heading: %q", body)
	}
	if toolpkg.AllowedPlanPathFromContext(inner.gotCtx) == "" {
		t.Fatalf("plan directory must stay writable while implementing")
	}
}

func TestImplementationReminderStopsOnceRecorded(t *testing.T) {
	home := t.TempDir()
	sid := "impl-2"
	exitedPlanState(t, home, sid)
	writePlan(t, home, sid, "# Plan\n\n- [x] do the thing\n\n"+state.ImplementationHeading+"\n\nDone in foo.go.\n")

	inner := &capturingLLM{}
	w := wrapPlanModeLLM(inner, home)
	ctx := llm.WithAgentSessionID(context.Background(), sid)

	if _, err := w.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("anything else?"))}, nil); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if body := lastReminderBody(inner.gotMsgs); body != "" {
		t.Fatalf("loop already closed, reminder should not appear: %q", body)
	}
}

func TestImplementationReminderSkippedWithoutPlanOrExit(t *testing.T) {
	home := t.TempDir()
	sid := "impl-3"
	exitedPlanState(t, home, sid)

	inner := &capturingLLM{}
	w := wrapPlanModeLLM(inner, home)
	ctx := llm.WithAgentSessionID(context.Background(), sid)
	if _, err := w.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if body := lastReminderBody(inner.gotMsgs); body != "" {
		t.Fatalf("no plan file: reminder should not appear: %q", body)
	}

	sid2 := "impl-4"
	setMode(t, home, sid2, state.ModeAgent, 0)
	writePlan(t, home, sid, "# Plan\n")
	inner2 := &capturingLLM{}
	w2 := wrapPlanModeLLM(inner2, home)
	ctx2 := llm.WithAgentSessionID(context.Background(), sid2)
	if _, err := w2.Execute(ctx2, []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if body := lastReminderBody(inner2.gotMsgs); body != "" {
		t.Fatalf("never planned: reminder should not appear: %q", body)
	}
}

func TestImplementationReminderThrottledBetweenTurns(t *testing.T) {
	home := t.TempDir()
	sid := "impl-5"
	exitedPlanState(t, home, sid)
	writePlan(t, home, sid, "# Plan\n")

	prior := llm.Message{
		Role:   llm.RoleUser,
		Parts:  []llm.ContentPart{llm.Text("<system-reminder>\n" + planImplementationReminderMarker + ": update the plan file.\n</system-reminder>")},
		IsMeta: true,
	}
	inner := &capturingLLM{}
	w := wrapPlanModeLLM(inner, home)
	ctx := llm.WithAgentSessionID(context.Background(), sid)
	msgs := []llm.Message{prior, llm.UserMessage(llm.Text("next"))}
	if _, err := w.Execute(ctx, msgs, nil); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(inner.gotMsgs) != len(msgs) {
		t.Fatalf("recent reminder should throttle the next injection: %+v", inner.gotMsgs)
	}
}

func TestImplementationReminderSkippedForSubagents(t *testing.T) {
	home := t.TempDir()
	sid := "impl-6"
	exitedPlanState(t, home, sid)
	writePlan(t, home, sid, "# Plan\n")

	inner := &capturingLLM{}
	w := wrapPlanModeLLM(inner, home)
	ctx := toolpkg.WithSubagentType(llm.WithAgentSessionID(context.Background(), sid), "general-purpose")
	if _, err := w.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("research"))}, nil); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if body := lastReminderBody(inner.gotMsgs); body != "" {
		t.Fatalf("subagent must not own the parent's plan loop: %q", body)
	}
}

func TestHasImplementationRecord(t *testing.T) {
	if state.HasImplementationRecord("# Plan\n\nmentions " + state.ImplementationHeading + " inline\n") {
		t.Fatalf("prose mention must not count as a record")
	}
	if !state.HasImplementationRecord("# Plan\n\n" + state.ImplementationHeading + "\n\nlanded\n") {
		t.Fatalf("heading line must count as a record")
	}
}

type capturingLLM struct {
	gotMsgs  []llm.Message
	gotCtx   context.Context
	gotTools []*llm.Tool
	calls    int
}

type planWriteThenDoneLLM struct {
	captured [][]llm.Message
}

func (m *planWriteThenDoneLLM) Execute(_ context.Context, msgs []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	m.captured = append(m.captured, append([]llm.Message(nil), msgs...))
	if len(m.captured) == 1 {
		msg := llm.AssistantMessage(nil, llm.ToolCall{
			ID:       "call-write",
			Type:     llm.ToolTypeFunction,
			Function: llm.FunctionCall{Name: "write_file", Arguments: `{}`},
		})
		return &llm.Result{Message: &msg}, nil
	}
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("plan complete")})
	return &llm.Result{Message: &msg}, nil
}

func (c *capturingLLM) Execute(ctx context.Context, msgs []llm.Message, tools []*llm.Tool) (*llm.Result, error) {
	c.calls++
	c.gotCtx = ctx
	c.gotMsgs = append([]llm.Message(nil), msgs...)
	c.gotTools = tools
	return &llm.Result{Message: &llm.Message{Role: llm.RoleAssistant}}, nil
}

func setMode(t *testing.T, home, sid string, mode state.Mode, turn int) {
	t.Helper()
	if err := state.Set(home, sid, state.State{
		Mode:          mode,
		Phase:         "plan",
		PrePlanMode:   state.ModeAgent,
		PlanTurnCount: turn,
	}); err != nil {
		t.Fatalf("state.Set: %v", err)
	}
}

// planReminderMetaMsg builds a minimal IsMeta user message that resembles a
// persisted plan-mode reminder, for use in transcript-derived test fixtures.
func planReminderMetaMsg(kind string) llm.Message {
	text := "Plan mode active. sparse reminder."
	if kind == "full" {
		text = "PLAN MODE ACTIVE - read carefully."
	}
	return llm.Message{
		Role:   llm.RoleUser,
		Parts:  []llm.ContentPart{llm.Text("<system-reminder>\n" + text + "\n</system-reminder>")},
		IsMeta: true,
	}
}

func TestPlanModeLLMPassThroughWhenNotPlan(t *testing.T) {
	home := t.TempDir()
	sid := "s1"
	setMode(t, home, sid, state.ModeAgent, 0)

	inner := &capturingLLM{}
	w := wrapPlanModeLLM(inner, home)
	ctx := llm.WithAgentSessionID(context.Background(), sid)
	msgs := []llm.Message{llm.UserMessage(llm.Text("hello"))}

	if _, err := w.Execute(ctx, msgs, nil); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(inner.gotMsgs) != 1 || !strings.Contains(inner.gotMsgs[0].Parts[0].Text, "hello") {
		t.Fatalf("msgs mutated when not plan mode: %+v", inner.gotMsgs)
	}
	if strings.Contains(inner.gotMsgs[0].Parts[0].Text, "system-reminder") {
		t.Fatalf("reminder injected in agent mode")
	}
}

func TestPlanModeLLMInjectsReminderInPlanMode(t *testing.T) {
	home := t.TempDir()
	sid := "s2"
	setMode(t, home, sid, state.ModePlan, 0)

	inner := &capturingLLM{}
	w := wrapPlanModeLLM(inner, home)
	ctx := llm.WithAgentSessionID(context.Background(), sid)
	msgs := []llm.Message{llm.UserMessage(llm.Text("draft a plan"))}

	if _, err := w.Execute(ctx, msgs, nil); err != nil {
		t.Fatalf("execute: %v", err)
	}
	// Reminder is now an independent IsMeta message at the end.
	last := inner.gotMsgs[len(inner.gotMsgs)-1]
	body := last.Parts[0].Text
	if !strings.Contains(body, "<system-reminder>") {
		t.Fatalf("missing reminder envelope: %q", body)
	}
	if !strings.Contains(body, "PLAN MODE ACTIVE") {
		t.Fatalf("full reminder expected on turn 0: %q", body)
	}
	if !last.IsMeta {
		t.Fatalf("reminder message should have IsMeta=true")
	}
}

func TestPlanModeLLMReminderKeepsStablePrefixAcrossToolIterations(t *testing.T) {
	home := t.TempDir()
	sid := "stable-tool-prefix"
	setMode(t, home, sid, state.ModePlan, 0)

	inner := &multiCaptureLLM{}
	w := wrapPlanModeLLM(inner, home)
	ctx := toolpkg.WithRunID(llm.WithAgentSessionID(context.Background(), sid), "run-stable-tools")

	enterCall := llm.AssistantMessage(nil, llm.ToolCall{
		ID:       "call-enter",
		Type:     llm.ToolTypeFunction,
		Function: llm.FunctionCall{Name: "enter_plan_mode", Arguments: `{}`},
	})
	firstSession := []llm.Message{
		llm.UserMessage(llm.Text("repair the plan")),
		enterCall,
		llm.ToolResultMessage("call-enter", llm.Text(`{"mode":"plan"}`)),
	}
	if _, err := w.Execute(ctx, firstSession, nil); err != nil {
		t.Fatalf("first execute: %v", err)
	}
	firstRequest := inner.captured[0]
	if got := firstRequest[len(firstRequest)-1]; !isPlanReminderMessage(got) {
		t.Fatalf("first request must end with the newly active reminder, got: %+v", got)
	}
	// Reproduces write_file creating the plan between requests. The reminder
	// must not be rebuilt with a different "plan exists" variant afterward.
	if err := state.SetPlanForSession(home, "", sid, "# Newly written plan"); err != nil {
		t.Fatalf("planstore.Set: %v", err)
	}

	editCall := llm.AssistantMessage(nil, llm.ToolCall{
		ID:       "call-edit",
		Type:     llm.ToolTypeFunction,
		Function: llm.FunctionCall{Name: "edit_file", Arguments: `{"file_path":"plan.md"}`},
	})
	secondSession := append(append([]llm.Message(nil), firstSession...),
		editCall,
		llm.ToolResultMessage("call-edit", llm.Text("ok")),
	)
	if _, err := w.Execute(ctx, secondSession, nil); err != nil {
		t.Fatalf("second execute: %v", err)
	}
	secondRequest := inner.captured[1]

	// Everything sent in request 1 must remain byte-for-byte/order stable at
	// the front of request 2. This is both the Responses tool-history contract
	// and the condition needed for the provider's prompt cache to keep hitting.
	if len(secondRequest) != len(firstRequest)+2 {
		t.Fatalf("second request has %d messages, want %d", len(secondRequest), len(firstRequest)+2)
	}
	if !reflect.DeepEqual(secondRequest[:len(firstRequest)], firstRequest) {
		t.Fatalf("plan reminder moved across tool iterations:\nfirst:  %+v\nsecond: %+v", firstRequest, secondRequest)
	}
	if got := secondRequest[len(secondRequest)-1]; got.Role != llm.RoleTool || got.ToolCallID != "call-edit" {
		t.Fatalf("tool result must remain the final input item, got: %+v", got)
	}

	// A steer is another suffix item; it must not move the already-visible
	// reminder or invalidate the cached prefix either.
	thirdSession := append(append([]llm.Message(nil), secondSession...), llm.UserMessage(llm.Text("also cover stale plans")))
	if _, err := w.Execute(ctx, thirdSession, nil); err != nil {
		t.Fatalf("third execute: %v", err)
	}
	thirdRequest := inner.captured[2]
	if len(thirdRequest) != len(secondRequest)+1 {
		t.Fatalf("third request has %d messages, want %d", len(thirdRequest), len(secondRequest)+1)
	}
	if !reflect.DeepEqual(thirdRequest[:len(secondRequest)], secondRequest) {
		t.Fatalf("prior request is not an exact prefix after steer:\nsecond: %+v\nthird:  %+v", secondRequest, thirdRequest)
	}
}

func TestPlanModeToolOrchestrationKeepsWriteFileHistoryAsExactPrefix(t *testing.T) {
	home := t.TempDir()
	sid := "active-tool-prefix"
	setMode(t, home, sid, state.ModePlan, 0)

	inner := &planWriteThenDoneLLM{}
	tools := toolpkg.NewState(home)
	writeTool, err := llm.NewTool("write_file", "write the plan", func(context.Context, *struct{}) (string, error) {
		if err := state.SetPlanForSession(home, "", sid, "# Written by tool orchestration"); err != nil {
			return "", err
		}
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("new write_file tool: %v", err)
	}
	w := wrapToolOrchestrationLLM(wrapPlanModeLLM(inner, home), tools)
	ctx := toolpkg.WithRunID(llm.WithAgentSessionID(context.Background(), sid), "run-write-prefix")
	if _, err := w.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("write the plan"))}, []*llm.Tool{writeTool}); err != nil {
		t.Fatalf("orchestrated execute: %v", err)
	}
	if len(inner.captured) != 2 {
		t.Fatalf("captured %d LLM requests, want 2", len(inner.captured))
	}
	firstRequest, secondRequest := inner.captured[0], inner.captured[1]
	if len(secondRequest) != len(firstRequest)+2 {
		t.Fatalf("second request has %d messages, want %d", len(secondRequest), len(firstRequest)+2)
	}
	if !reflect.DeepEqual(secondRequest[:len(firstRequest)], firstRequest) {
		t.Fatalf("write_file continuation changed request prefix:\nfirst:  %+v\nsecond: %+v", firstRequest, secondRequest)
	}
	if got := secondRequest[len(secondRequest)-1]; got.Role != llm.RoleTool || got.ToolCallID != "call-write" {
		t.Fatalf("tool result must remain final, got: %+v", got)
	}
}

func TestPlanModeLLMWiresAllowedPlanPathCtx(t *testing.T) {
	home := t.TempDir()
	sid := "s3"
	setMode(t, home, sid, state.ModePlan, 0)

	inner := &capturingLLM{}
	w := wrapPlanModeLLM(inner, home)
	ctx := llm.WithAgentSessionID(context.Background(), sid)

	if _, err := w.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("x"))}, nil); err != nil {
		t.Fatalf("execute: %v", err)
	}
	allowed := toolpkg.AllowedPlanPathFromContext(inner.gotCtx)
	if allowed == "" {
		t.Fatalf("AllowedPlanPath not wired into downstream ctx")
	}
	if got, want := allowed, state.PlanDirForSession(home, "", sid); got != want {
		t.Fatalf("allowed path %q want %q", got, want)
	}
}

// TestPlanModeReminderNamesOnlyTheConversationsPlan pins the reminder side of
// the cross-conversation leak: the per-turn plan-mode reminder must advertise
// THIS conversation's plan directory and writable region even when another
// conversation in the same project wrote a plan more recently.
func TestPlanModeReminderNamesOnlyTheConversationsPlan(t *testing.T) {
	home := t.TempDir()
	setMode(t, home, "sid-a", state.ModePlan, 0)

	mineDir := state.PlanDirForSession(home, "", "sid-a")
	if err := os.MkdirAll(mineDir, 0o755); err != nil {
		t.Fatalf("mkdir mine dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mineDir, "mine.md"), []byte("# Mine\n"), 0o600); err != nil {
		t.Fatalf("write mine: %v", err)
	}
	theirsDir := state.PlanDirForSession(home, "", "sid-b")
	if err := os.MkdirAll(theirsDir, 0o755); err != nil {
		t.Fatalf("mkdir theirs dir: %v", err)
	}
	theirs := filepath.Join(theirsDir, "theirs.md")
	if err := os.WriteFile(theirs, []byte("# Theirs\n"), 0o600); err != nil {
		t.Fatalf("write theirs: %v", err)
	}
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(theirs, future, future); err != nil {
		t.Fatalf("chtimes theirs: %v", err)
	}

	inner := &capturingLLM{}
	w := wrapPlanModeLLM(inner, home)
	ctx := llm.WithAgentSessionID(context.Background(), "sid-a")

	if _, err := w.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("design"))}, nil); err != nil {
		t.Fatalf("execute: %v", err)
	}
	body := lastReminderBody(inner.gotMsgs)
	if !strings.Contains(body, mineDir) {
		t.Fatalf("reminder must name this conversation's plan dir %q: %q", mineDir, body)
	}
	if strings.Contains(body, "theirs.md") {
		t.Fatalf("reminder must not name another conversation's plan: %q", body)
	}
	if got, want := toolpkg.AllowedPlanPathFromContext(inner.gotCtx), mineDir; got != want {
		t.Fatalf("AllowedPlanPath=%q want %q", got, want)
	}
}

func TestPlanModeLLMThrottlesWithinWindow(t *testing.T) {
	home := t.TempDir()
	sid := "s4"
	setMode(t, home, sid, state.ModePlan, 0)

	inner := &capturingLLM{}
	w := wrapPlanModeLLM(inner, home)
	ctx := llm.WithAgentSessionID(context.Background(), sid)

	// A persisted plan-reminder from a previous turn is present in the
	// transcript-derived msgs, followed by a single human turn (< 5). The
	// throttle must skip re-injection and pass the msgs through unchanged.
	msgs := []llm.Message{
		llm.UserMessage(llm.Text("first turn")),
		planReminderMetaMsg("full"),
		llm.AssistantMessage([]llm.ContentPart{llm.Text("ok")}),
		llm.UserMessage(llm.Text("second turn")), // 1 human turn since reminder
	}
	if _, err := w.Execute(ctx, msgs, nil); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(inner.gotMsgs) != len(msgs) {
		t.Fatalf("throttle should pass msgs through unchanged: got %d msgs want %d", len(inner.gotMsgs), len(msgs))
	}
	last := inner.gotMsgs[len(inner.gotMsgs)-1]
	if last.IsMeta {
		t.Fatalf("throttle should NOT inject a new reminder, but last msg is IsMeta")
	}
}

func TestPlanModeLLMInjectsAfterThrottleWindow(t *testing.T) {
	home := t.TempDir()
	sid := "s4b"
	setMode(t, home, sid, state.ModePlan, 0)

	inner := &capturingLLM{}
	w := wrapPlanModeLLM(inner, home)
	ctx := llm.WithAgentSessionID(context.Background(), sid)

	// 5 human turns since the last reminder -> throttle window expired, inject.
	msgs := []llm.Message{
		llm.UserMessage(llm.Text("t0")),
		planReminderMetaMsg("full"),
	}
	for i := 1; i <= planModeTurnsBetweenReminders; i++ {
		msgs = append(msgs,
			llm.AssistantMessage([]llm.ContentPart{llm.Text("a")}),
			llm.UserMessage(llm.Text("u")),
		)
	}
	if _, err := w.Execute(ctx, msgs, nil); err != nil {
		t.Fatalf("execute: %v", err)
	}
	last := inner.gotMsgs[len(inner.gotMsgs)-1]
	if !last.IsMeta || !strings.Contains(last.Parts[0].Text, "system-reminder") {
		t.Fatalf("expected injected reminder after throttle window, got: %+v", last)
	}
}

func TestPlanModeLLMStatelessKind(t *testing.T) {
	home := t.TempDir()
	sid := "s5"
	setMode(t, home, sid, state.ModePlan, 0)

	inner := &capturingLLM{}
	w := wrapPlanModeLLM(inner, home)
	ctx := llm.WithAgentSessionID(context.Background(), sid)

	// reminderCount (prior reminders in session) -> expected kind.
	// full when count%5==0 (1st, 6th, 11th reminder), sparse otherwise.
	cases := []struct{ count, wantFull int }{
		{0, 1},  // 1st reminder -> full
		{1, 0},  // 2nd -> sparse
		{4, 0},  // 5th -> sparse
		{5, 1},  // 6th -> full
		{10, 1}, // 11th -> full
	}
	for _, c := range cases {
		inner.gotMsgs = nil
		// Place c.count prior reminders, then enough human turns (>= 5) after
		// the last one so the throttle window has expired and injection is
		// allowed. The kind is derived from reminderCount alone.
		msgs := []llm.Message{llm.UserMessage(llm.Text("seed"))}
		for i := 0; i < c.count; i++ {
			msgs = append(msgs, planReminderMetaMsg("sparse"))
		}
		for i := 0; i < planModeTurnsBetweenReminders; i++ {
			msgs = append(msgs,
				llm.AssistantMessage([]llm.ContentPart{llm.Text("a")}),
				llm.UserMessage(llm.Text("u")),
			)
		}
		if _, err := w.Execute(ctx, msgs, nil); err != nil {
			t.Fatalf("execute count=%d: %v", c.count, err)
		}
		last := inner.gotMsgs[len(inner.gotMsgs)-1]
		body := last.Parts[0].Text
		isFull := strings.Contains(body, "PLAN MODE ACTIVE")
		if c.wantFull == 1 && !isFull {
			t.Fatalf("count=%d: expected full reminder, got sparse: %q", c.count, body)
		}
		if c.wantFull == 0 && isFull {
			t.Fatalf("count=%d: expected sparse reminder, got full", c.count)
		}
	}
}

func TestPlanModeLLMReentryRestartsCount(t *testing.T) {
	home := t.TempDir()
	sid := "s6"
	setMode(t, home, sid, state.ModePlan, 0)
	// A plan file exists from the previous (exited) plan session, so the
	// re-entry section (which guides reading the existing plan) is emitted.
	if err := state.SetPlanForSession(home, "", sid, "# Previous Plan\nold content"); err != nil {
		t.Fatalf("planstore.Set: %v", err)
	}

	inner := &capturingLLM{}
	w := wrapPlanModeLLM(inner, home)
	ctx := llm.WithAgentSessionID(context.Background(), sid)

	// Prior plan session had several reminders, then exit_plan_mode happened.
	// On re-entry (no reminders in the new session), counting restarts and the
	// first injected reminder is FULL and carries the re-entry section.
	exitCall := llm.AssistantMessage(nil, llm.ToolCall{
		ID:       "call-exit",
		Type:     llm.ToolTypeFunction,
		Function: llm.FunctionCall{Name: "exit_plan_mode", Arguments: "{}"},
	})
	msgs := []llm.Message{
		llm.UserMessage(llm.Text("old plan turn")),
		planReminderMetaMsg("sparse"),
		llm.AssistantMessage([]llm.ContentPart{llm.Text("done")}),
		exitCall, // boundary: counting restarts after this
		llm.ToolResultMessage("call-exit", llm.Text("Exited plan mode.")),
		llm.UserMessage(llm.Text("re-enter: let's plan again")),
	}
	if _, err := w.Execute(ctx, msgs, nil); err != nil {
		t.Fatalf("execute: %v", err)
	}
	last := inner.gotMsgs[len(inner.gotMsgs)-1]
	body := last.Parts[0].Text
	if !strings.Contains(body, "PLAN MODE ACTIVE") {
		t.Fatalf("re-entry first reminder should be FULL, got: %q", body)
	}
	if !strings.Contains(body, "Re-entering Plan Mode") {
		t.Fatalf("re-entry reminder should carry the re-entry section: %q", body)
	}
}

func TestPlanModeLLMHandlesEmptyMessages(t *testing.T) {
	home := t.TempDir()
	sid := "s7"
	setMode(t, home, sid, state.ModePlan, 0)

	inner := &capturingLLM{}
	w := wrapPlanModeLLM(inner, home)
	ctx := llm.WithAgentSessionID(context.Background(), sid)

	if _, err := w.Execute(ctx, nil, nil); err != nil {
		t.Fatalf("execute empty: %v", err)
	}
	if len(inner.gotMsgs) != 1 {
		t.Fatalf("expected synthetic reminder msg, got %d", len(inner.gotMsgs))
	}
	// The reminder is an independent IsMeta
	// user message (not a system message).
	if inner.gotMsgs[0].Role != llm.RoleUser {
		t.Fatalf("expected meta user msg, got role=%s", inner.gotMsgs[0].Role)
	}
	if !inner.gotMsgs[0].IsMeta {
		t.Fatalf("expected IsMeta=true on reminder msg")
	}
}

func TestPlanModeLLMNoHomeBypasses(t *testing.T) {
	inner := &capturingLLM{}
	w := wrapPlanModeLLM(inner, "")
	ctx := llm.WithAgentSessionID(context.Background(), "s")
	if _, err := w.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("x"))}, nil); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if inner.calls != 1 {
		t.Fatalf("inner not called: %d", inner.calls)
	}
}

func TestPlanModeLLMNoSessionBypasses(t *testing.T) {
	inner := &capturingLLM{}
	w := wrapPlanModeLLM(inner, t.TempDir())
	if _, err := w.Execute(context.Background(), []llm.Message{llm.UserMessage(llm.Text("x"))}, nil); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if inner.calls != 1 {
		t.Fatalf("inner not called: %d", inner.calls)
	}
}

// multiCaptureLLM records the messages from every Execute call so tests can
// compare reminder content across iterations within a single run.
type multiCaptureLLM struct {
	captured [][]llm.Message
}

func (m *multiCaptureLLM) Execute(ctx context.Context, msgs []llm.Message, tools []*llm.Tool) (*llm.Result, error) {
	m.captured = append(m.captured, append([]llm.Message(nil), msgs...))
	return &llm.Result{Message: &llm.Message{Role: llm.RoleAssistant}}, nil
}

// TestPlanModeLLMStableKindWithinRun verifies that all iterations within a
// single run (same runID) receive the SAME plan-mode reminder. The kind is
// derived statelessly from the transcript-visible msgs, which do not change
// across iterations within a run, so the reminder is naturally stable and the
// KV-cache prefix is preserved.
func TestPlanModeLLMStableKindWithinRun(t *testing.T) {
	home := t.TempDir()
	sid := "stable-run"
	// No prior reminders -> first (and every) iteration derives kind="full".
	setMode(t, home, sid, state.ModePlan, 0)

	inner := &multiCaptureLLM{}
	w := wrapPlanModeLLM(inner, home)
	ctx := toolpkg.WithRunID(llm.WithAgentSessionID(context.Background(), sid), "run-stable")

	for i := 0; i < 3; i++ {
		if _, err := w.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("x"))}, nil); err != nil {
			t.Fatalf("execute %d: %v", i, err)
		}
	}

	if len(inner.captured) != 3 {
		t.Fatalf("expected 3 captured calls, got %d", len(inner.captured))
	}
	// All 3 iterations must have the identical reminder content.
	// Reminder is the last message (independent IsMeta message).
	b0 := inner.captured[0][len(inner.captured[0])-1].Parts[0].Text
	b1 := inner.captured[1][len(inner.captured[1])-1].Parts[0].Text
	b2 := inner.captured[2][len(inner.captured[2])-1].Parts[0].Text
	if b0 != b1 || b0 != b2 {
		t.Fatalf("reminder changed across iterations:\n  iter0: %q\n  iter1: %q\n  iter2: %q", b0, b1, b2)
	}
	// No prior reminders -> all iterations are full.
	if !strings.Contains(b0, "PLAN MODE ACTIVE") {
		t.Fatalf("expected full reminder, got: %q", b0)
	}
}

// TestPlanModeLLMRecordsReminderForAdoption verifies that the injected reminder
// is published to the enclosing orchestration loop instead of being written to
// the transcript from inside the call: the sink receives the exact message the
// model was sent, at the index it was sent at.
func TestPlanModeLLMRecordsReminderForAdoption(t *testing.T) {
	home := t.TempDir()
	sid := "adopt-once"
	setMode(t, home, sid, state.ModePlan, 0)

	inner := &capturingLLM{}
	w := wrapPlanModeLLM(inner, home)
	sink := &reminderAdoptionSink{}
	ctx := withReminderAdoptionSink(
		toolpkg.WithRunID(llm.WithAgentSessionID(context.Background(), sid), "run-adopt"),
		sink,
	)

	msgs := []llm.Message{llm.UserMessage(llm.Text("x"))}
	if _, err := w.Execute(ctx, msgs, nil); err != nil {
		t.Fatalf("execute: %v", err)
	}
	adopted := sink.take()
	if len(adopted) != 1 {
		t.Fatalf("expected exactly one published reminder, got %d", len(adopted))
	}
	recorded, insertAt := adopted[0].message, adopted[0].insertAt
	if insertAt != len(msgs) {
		t.Fatalf("insertAt=%d want %d (reminder is appended at the tail)", insertAt, len(msgs))
	}
	if !recorded.IsMeta || recorded.Role != llm.RoleUser {
		t.Fatalf("recorded reminder must be an IsMeta user message: %#v", recorded)
	}
	sent := inner.gotMsgs[len(inner.gotMsgs)-1]
	if sent.Parts[0].Text != recorded.Parts[0].Text {
		t.Fatalf("recorded reminder differs from the one sent:\n  sent: %q\n  recorded: %q",
			sent.Parts[0].Text, recorded.Parts[0].Text)
	}
	// PlanTurnCount is no longer incremented by planModeLLM.
	st, _ := state.Get(home, sid)
	if st.PlanTurnCount != 0 {
		t.Fatalf("PlanTurnCount should stay 0 (deprecated), got %d", st.PlanTurnCount)
	}
}

// TestPlanModeLLMSkipsReminderOnceAdopted verifies that once the orchestration
// has adopted the reminder into the live session, later iterations of the same
// run neither re-inject nor re-publish it: the throttle reads it back out of the
// session it now belongs to.
func TestPlanModeLLMSkipsReminderOnceAdopted(t *testing.T) {
	home := t.TempDir()
	sid := "adopt-idempotent"
	setMode(t, home, sid, state.ModePlan, 0)

	inner := &capturingLLM{}
	w := wrapPlanModeLLM(inner, home)
	sink := &reminderAdoptionSink{}
	ctx := withReminderAdoptionSink(
		toolpkg.WithRunID(llm.WithAgentSessionID(context.Background(), sid), "run-adopt-2"),
		sink,
	)

	// Iteration 1: the wrapper injects and publishes; the loop adopts.
	session := []llm.Message{llm.UserMessage(llm.Text("x"))}
	if _, err := w.Execute(ctx, session, nil); err != nil {
		t.Fatalf("execute 1: %v", err)
	}
	adopted := sink.take()
	if len(adopted) != 1 {
		t.Fatal("iteration 1 published no reminder")
	}
	session = insertPlanReminderMessage(session, adopted[0].message, adopted[0].insertAt)
	session = append(session, llm.AssistantMessage([]llm.ContentPart{llm.Text("ok")}))

	// Iteration 2: the session already carries it.
	if _, err := w.Execute(ctx, session, nil); err != nil {
		t.Fatalf("execute 2: %v", err)
	}
	if remaining := sink.take(); len(remaining) != 0 {
		t.Fatal("reminder was published twice for the same run")
	}
	if got := len(inner.gotMsgs); got != len(session) {
		t.Fatalf("iteration 2 sent %d messages, want %d (no second reminder)", got, len(session))
	}
}

// TestPlanModeLLMPublishesPerRunAcrossRuns verifies that different runs
// (different runIDs) each publish their own reminder, so the cadence tracks
// turns across runs.
func TestPlanModeLLMPublishesPerRunAcrossRuns(t *testing.T) {
	home := t.TempDir()
	sid := "multi-run"
	setMode(t, home, sid, state.ModePlan, 0)

	inner := &capturingLLM{}
	w := wrapPlanModeLLM(inner, home)
	sink := &reminderAdoptionSink{}
	baseCtx := withReminderAdoptionSink(llm.WithAgentSessionID(context.Background(), sid), sink)

	ctx1 := toolpkg.WithRunID(baseCtx, "run-A")
	if _, err := w.Execute(ctx1, []llm.Message{llm.UserMessage(llm.Text("x"))}, nil); err != nil {
		t.Fatalf("execute run1: %v", err)
	}
	if len(sink.take()) == 0 {
		t.Fatal("run1 published no reminder")
	}

	ctx2 := toolpkg.WithRunID(baseCtx, "run-B")
	if _, err := w.Execute(ctx2, []llm.Message{llm.UserMessage(llm.Text("y"))}, nil); err != nil {
		t.Fatalf("execute run2: %v", err)
	}
	if len(sink.take()) == 0 {
		t.Fatal("run2 published no reminder")
	}
}

// TestReminderSinkAdoptsTwoRecordedRemindersInRecordOrder covers the generalized
// adoption sink: two wrappers can record one reminder each during a single
// inner call, and the loop drains both, in record order, so each insertion is
// replayed in the coordinate space its recorded index was computed against.
func TestReminderSinkAdoptsTwoRecordedRemindersInRecordOrder(t *testing.T) {
	sink := &reminderAdoptionSink{}
	ctx := withReminderAdoptionSink(context.Background(), sink)

	first := planReminderMessage("first reminder")
	second := planReminderMessage("second reminder")
	recordReminderAdoption(ctx, first, 3)
	recordReminderAdoption(ctx, second, 4)

	// Replay what the orchestration loop does: adopt in record order, each
	// insertion into the session the previous one just amended.
	session := []llm.Message{
		llm.UserMessage(llm.Text("h1")),
		llm.UserMessage(llm.Text("h2")),
		llm.UserMessage(llm.Text("h3")),
	}
	adopted := sink.take()
	if len(adopted) != 2 {
		t.Fatalf("expected two recorded reminders, got %d", len(adopted))
	}
	if llm.TextContent(adopted[0].message.Parts...) != llm.TextContent(first.Parts...) ||
		llm.TextContent(adopted[1].message.Parts...) != llm.TextContent(second.Parts...) {
		t.Fatalf("adopted reminders out of record order: %q then %q",
			llm.TextContent(adopted[0].message.Parts...), llm.TextContent(adopted[1].message.Parts...))
	}
	for _, rec := range adopted {
		session = insertPlanReminderMessage(session, rec.message, rec.insertAt)
	}
	// 3 history + 2 reminders; first reminder at index 3, second at 4.
	if got := llm.TextContent(session[3].Parts...); got != llm.TextContent(first.Parts...) {
		t.Fatalf("session[3] = %q, want the first reminder", got)
	}
	if got := llm.TextContent(session[4].Parts...); got != llm.TextContent(second.Parts...) {
		t.Fatalf("session[4] = %q, want the second reminder", got)
	}
	if again := sink.take(); len(again) != 0 {
		t.Fatalf("take must drain the sink, got %d more", len(again))
	}

	// discard clears a pending reminder without adopting it (mid-turn
	// compaction replaces history with what the inner call already sent).
	recordReminderAdoption(ctx, first, 0)
	reminderSinkFromContextForTest(ctx).discard()
	if left := sink.take(); len(left) != 0 {
		t.Fatalf("discard left %d reminders pending", len(left))
	}
}

func reminderSinkFromContextForTest(ctx context.Context) *reminderAdoptionSink {
	return reminderAdoptionSinkFromContext(ctx)
}

func TestPlanModeLLMSubagentVariant(t *testing.T) {
	home := t.TempDir()
	sid := "subagent-plan"
	setMode(t, home, sid, state.ModePlan, 0)

	inner := &capturingLLM{}
	w := wrapPlanModeLLM(inner, home)
	ctx := toolpkg.WithForkChild(llm.WithAgentSessionID(context.Background(), sid), true)

	msgs := []llm.Message{llm.UserMessage(llm.Text("research this"))}
	if _, err := w.Execute(ctx, msgs, nil); err != nil {
		t.Fatalf("execute: %v", err)
	}
	last := inner.gotMsgs[len(inner.gotMsgs)-1]
	body := last.Parts[0].Text
	if !last.IsMeta {
		t.Fatalf("expected IsMeta=true on subagent reminder")
	}
	if !strings.Contains(body, "subagent") {
		t.Fatalf("subagent reminder should contain 'subagent', got: %q", body)
	}
	if !strings.Contains(body, "PLAN MODE ACTIVE") {
		t.Fatalf("subagent reminder should contain 'PLAN MODE ACTIVE', got: %q", body)
	}
	if strings.Contains(body, "Phase 5") {
		t.Fatalf("subagent reminder should not reference Phase 5 workflow: %q", body)
	}
}

func TestPlanModeLLMSubagentVariantSparse(t *testing.T) {
	home := t.TempDir()
	sid := "subagent-sparse"
	setMode(t, home, sid, state.ModePlan, 0)

	inner := &capturingLLM{}
	w := wrapPlanModeLLM(inner, home)
	ctx := toolpkg.WithForkChild(llm.WithAgentSessionID(context.Background(), sid), true)

	// With a prior reminder present, the throttle blocks injection and the
	// subagent never exercises the sparse path. Force sparse by providing
	// enough prior reminders that kind is "sparse" (count%5 != 0).
	msgs := []llm.Message{
		llm.UserMessage(llm.Text("seed")),
		planReminderMetaMsg("sparse"),
		llm.AssistantMessage([]llm.ContentPart{llm.Text("a")}),
		llm.UserMessage(llm.Text("u1")),
		llm.AssistantMessage([]llm.ContentPart{llm.Text("a")}),
		llm.UserMessage(llm.Text("u2")),
		llm.AssistantMessage([]llm.ContentPart{llm.Text("a")}),
		llm.UserMessage(llm.Text("u3")),
		llm.AssistantMessage([]llm.ContentPart{llm.Text("a")}),
		llm.UserMessage(llm.Text("u4")),
		llm.AssistantMessage([]llm.ContentPart{llm.Text("a")}),
		llm.UserMessage(llm.Text("u5")),
	}
	if _, err := w.Execute(ctx, msgs, nil); err != nil {
		t.Fatalf("execute: %v", err)
	}
	last := inner.gotMsgs[len(inner.gotMsgs)-1]
	body := last.Parts[0].Text
	if !last.IsMeta {
		t.Fatalf("expected IsMeta=true on injected sparse reminder")
	}
	if !strings.Contains(body, "subagent") {
		t.Fatalf("sparse subagent reminder should contain 'subagent', got: %q", body)
	}
	if strings.Contains(body, "PLAN MODE ACTIVE") {
		t.Fatalf("sparse subagent reminder should NOT be full, got: %q", body)
	}
}

func planModeSubagentFactory(t *testing.T) (Factory, string) {
	t.Helper()
	home := t.TempDir()
	return Factory{
		Home:          home,
		WorkspaceRoot: filepath.Join(home, "workspace"),
		Owner:         &Runner{Deps: &Deps{}, SubagentExecutor: &fixedSubagentExecutor{output: "unused"}},
	}, home
}

func planModeParentCtx() context.Context {
	ctx := llm.WithAgentSessionID(context.Background(), "sid-plan-parent")
	return toolpkg.WithMode(ctx, string(state.ModePlan))
}

// The worker mode entry must land in the workspace root, the state root every
// reader resolves (Runner.StateRoot / primaryagent.ActiveStateRoot). Writing it
// under Home instead leaves the child in agent mode, free to edit the codebase
// while the parent is still planning.
func TestPlanModeForkChildInheritsPlanModeAtWorkspaceRoot(t *testing.T) {
	fac, home := planModeSubagentFactory(t)
	prep, err := prepareSubagentExecution(planModeParentCtx(), fac, "task-fork", "investigate", "")
	if err != nil {
		t.Fatalf("prepareSubagentExecution: %v", err)
	}
	got, err := state.Get(fac.workspaceRoot(), prep.workerSessionID)
	if err != nil {
		t.Fatalf("state.Get: %v", err)
	}
	if got.Mode != state.ModePlan {
		t.Fatalf("fork child mode=%q want plan (entry not readable at the workspace state root)", got.Mode)
	}
	if _, err := os.Stat(filepath.Join(home, "state", "modes")); err == nil {
		t.Fatal("mode entry written under Home; no reader resolves that root")
	}
}

// A typed worker that can write files is a plan-mode escape unless it inherits
// the restriction; one that cannot write files keeps its own stricter policy.
func TestPlanModeInheritanceCoversWriteCapableTypedSubagents(t *testing.T) {
	for _, tc := range []struct {
		subtype string
		want    state.Mode
	}{
		{subtype: "general-purpose", want: state.ModePlan},
		{subtype: "explore", want: state.ModeAgent},
	} {
		t.Run(tc.subtype, func(t *testing.T) {
			fac, _ := planModeSubagentFactory(t)
			prep, err := prepareSubagentExecution(planModeParentCtx(), fac, "task-"+tc.subtype, "investigate", tc.subtype)
			if err != nil {
				t.Fatalf("prepareSubagentExecution: %v", err)
			}
			got, err := state.Get(fac.workspaceRoot(), prep.workerSessionID)
			if err != nil {
				t.Fatalf("state.Get: %v", err)
			}
			if got.Mode != tc.want {
				t.Fatalf("%s child mode=%q want %q", tc.subtype, got.Mode, tc.want)
			}
		})
	}
}

func TestInputPreview(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		max     int
		want    string
		wantErr bool
	}{
		{
			name:  "empty string",
			input: "",
			max:   100,
			want:  "",
		},
		{
			name:  "zero max returns empty",
			input: "hello world",
			max:   0,
			want:  "",
		},
		{
			name:  "negative max returns empty",
			input: "hello world",
			max:   -1,
			want:  "",
		},
		{
			name:  "input shorter than max returns full input",
			input: "hi",
			max:   100,
			want:  "hi",
		},
		{
			name:  "input exactly at max returns full input",
			input: "hello",
			max:   5,
			want:  "hello",
		},
		{
			name:  "input longer than max truncates",
			input: "hello world this is a long string",
			max:   5,
			want:  "hello",
		},
		{
			name:  "whitespace trimming leading and trailing",
			input: "  hello world  ",
			max:   100,
			want:  "hello world",
		},
		{
			name:  "only whitespace becomes empty after trim",
			input: "   \t\n  ",
			max:   100,
			want:  "",
		},
		{
			name:  "unicode characters preserved when within max",
			input: "ＡＢＣＤ",
			max:   100,
			want:  "ＡＢＣＤ",
		},
		{
			name:  "unicode characters truncated by bytes when exceeding max",
			input: "ＡＢＣＤ",
			max:   6,
			want:  "ＡＢ",
		},
		{
			name:  "single char input",
			input: "a",
			max:   1,
			want:  "a",
		},
		{
			name:  "single char input with larger max",
			input: "x",
			max:   100,
			want:  "x",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := InputPreview(tt.input, tt.max)
			if got != tt.want {
				t.Errorf("InputPreview(%q, %d) = %q, want %q", tt.input, tt.max, got, tt.want)
			}
		})
	}
}

func TestExplicitLimitIsClampedBelowDefault(t *testing.T) {
	msgs := []llm.Message{llm.UserMessage(llm.Text(strings.Repeat("x", 1200)))}
	if !shouldCompactMessages(msgs, 1000, &CompactChainDeps{ExplicitLimit: 100}, -1, nil, "", "") {
		t.Fatal("explicit lower threshold ignored")
	}
	if shouldCompactMessages(msgs, 10000, &CompactChainDeps{ExplicitLimit: 9000}, -1, nil, "", "") {
		t.Fatal("explicit limit above 90% must not raise the default threshold")
	}
}

func TestModelContextLimitPrefersEffectiveInputFromSnapshot(t *testing.T) {
	if got := modelContextLimitFromSnapshot(json.RawMessage(`{"model_context_tokens":1050000,"effective_input_tokens":922000}`)); got != 922000 {
		t.Fatalf("limit=%d want 922000", got)
	}
	if got := modelContextLimitFromSnapshot(json.RawMessage(`{"model_context_tokens":1050000}`)); got != 1050000 {
		t.Fatalf("fallback limit=%d want 1050000", got)
	}
}

func TestBodyAfterPrefixScopeStillHonorsHardWindow(t *testing.T) {
	msgs := []llm.Message{llm.SystemMessage(strings.Repeat("s", 12000)), llm.UserMessage(llm.Text("short"))}
	deps := &CompactChainDeps{LimitScope: "body_after_prefix"}
	if shouldCompactMessages(msgs, 2000, deps, -1, nil, "", "") != true {
		t.Fatal("hard full-window cap must trigger")
	}
	if shouldCompactMessages(msgs, 10000, deps, -1, nil, "", "") {
		t.Fatal("prefix should be excluded below hard cap")
	}
}

func TestCompactIfNeededUsesSingleCheckpointPath(t *testing.T) {
	msgs := []llm.Message{llm.UserMessage(llm.Text(strings.Repeat("x", 4000)))}
	called := 0
	ctx, sinkState := newCompactionStreamTestContext()
	readFile, err := llm.NewRawTool("read_file", "read", nil, func(context.Context, string) (any, error) { return "", nil })
	if err != nil {
		t.Fatal(err)
	}
	callTools := []*llm.Tool{readFile}
	deps := &CompactChainDeps{TryCompact: func(ctx context.Context, _ []llm.Message, tools []*llm.Tool, reactive bool) ([]llm.Message, bool, error) {
		called++
		if reactive {
			t.Fatal("a threshold compaction is not reactive")
		}
		// The summary request is the call the agent was about to make, so it
		// needs that call's tools.
		if len(tools) != 1 || tools[0] != callTools[0] {
			t.Fatalf("compaction got tools %v, want the call's own", toolNames(tools))
		}
		assertAndExerciseMutedCompactionSink(t, ctx)
		return []llm.Message{llm.UserMessage(llm.Text("replacement"))}, true, nil
	}}
	out, did := compactIfNeeded(ctx, msgs, json.RawMessage(`{"model_context_tokens":100}`), deps, -1, callTools)
	if !did || called != 1 || len(out) != 1 || out[0].TextContent() != "replacement" {
		t.Fatalf("did=%v called=%d out=%+v", did, called, out)
	}
	sinkState.assertPreservedLifecycleOnly(t)
}

// TestCompactIfNeededKeepsTheHistoryWhenCompactionFails pins that the call a
// compaction was made for goes ahead with the history unchanged; how the
// compaction ended is reported by its own lifecycle, not here.
func TestCompactIfNeededKeepsTheHistoryWhenCompactionFails(t *testing.T) {
	msgs := []llm.Message{llm.UserMessage(llm.Text(strings.Repeat("x", 4000)))}
	deps := &CompactChainDeps{TryCompact: func(context.Context, []llm.Message, []*llm.Tool, bool) ([]llm.Message, bool, error) {
		return nil, false, errors.New("provider compact failed")
	}}
	out, did := compactIfNeeded(context.Background(), msgs, json.RawMessage(`{"model_context_tokens":100}`), deps, -1, nil)
	if did || len(out) != len(msgs) || out[0].TextContent() != msgs[0].TextContent() {
		t.Fatalf("did=%v out=%+v", did, out)
	}
}

type compactionStreamTestState struct {
	visualCallbacks int
	responseStarts  int
	usageInput      int
	usageOutput     int
	snapshotInput   int
	snapshotOutput  int
	endCallbacks    int
	streamed        bool
}

func newCompactionStreamTestContext() (context.Context, *compactionStreamTestState) {
	state := &compactionStreamTestState{}
	sink := &llm.StreamSink{
		OnDelta:          func(string) { state.visualCallbacks++ },
		OnReasoningDelta: func(string) { state.visualCallbacks++ },
		OnReasoningDone:  func() { state.visualCallbacks++ },
		OnWebSearch:      func(string, string, bool) { state.visualCallbacks++ },
		OnResponseStarted: func() {
			state.responseStarts++
		},
		OnUsage: func(inputTokens, outputTokens int) {
			state.usageInput += inputTokens
			state.usageOutput += outputTokens
		},
		OnUsageSnapshot: func(inputTokens, outputTokens int) {
			state.snapshotInput = inputTokens
			state.snapshotOutput = outputTokens
		},
		OnEnd:    func() { state.endCallbacks++ },
		Streamed: &state.streamed,
	}
	return llm.WithStreamSink(context.Background(), sink), state
}

func assertAndExerciseMutedCompactionSink(t *testing.T, ctx context.Context) {
	t.Helper()
	sink := llm.StreamSinkFrom(ctx)
	if sink == nil {
		t.Fatal("compaction context has no LLM stream sink")
	}
	if sink.OnDelta != nil || sink.OnReasoningDelta != nil || sink.OnReasoningDone != nil || sink.OnWebSearch != nil || sink.OnResponseStarted != nil || sink.Streamed != nil {
		t.Fatalf("foreground callbacks were not muted: %+v", sink)
	}
	if sink.OnUsage == nil || sink.OnUsageSnapshot == nil || sink.OnEnd == nil {
		t.Fatalf("non-visual callbacks were not preserved: %+v", sink)
	}
	sink.OnUsage(3, 5)
	sink.OnUsageSnapshot(7, 11)
	sink.OnEnd()
}

func (s *compactionStreamTestState) assertPreservedLifecycleOnly(t *testing.T) {
	t.Helper()
	if s.visualCallbacks != 0 || s.responseStarts != 0 || s.streamed {
		t.Fatalf("foreground stream was touched: visual_callbacks=%d response_starts=%d streamed=%v", s.visualCallbacks, s.responseStarts, s.streamed)
	}
	if s.usageInput != 3 || s.usageOutput != 5 || s.snapshotInput != 7 || s.snapshotOutput != 11 || s.endCallbacks != 1 {
		t.Fatalf("non-visual callbacks were not preserved: %+v", s)
	}
}

type recoverableScriptLLM struct {
	calls int
	seen  [][]llm.Message
}

func (m *recoverableScriptLLM) Execute(_ context.Context, messages []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	m.calls++
	m.seen = append(m.seen, cloneRecoverableTestMessages(messages))
	if m.calls == 1 {
		return nil, errors.New("context_length_exceeded: input exceeds context window")
	}
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("retried successfully")})
	return &llm.Result{Message: &msg}, nil
}

func TestRecoverableLLMCompactsAndRetriesAfterContextOverflow(t *testing.T) {
	inner := &recoverableScriptLLM{}
	var reactive []bool
	replacement := []llm.Message{{Compaction: &llm.CompactionState{Type: "compaction_summary", ID: "cmp_1", EncryptedContent: "opaque"}}}
	wrapped := WrapRecoverableLLM(
		inner,
		nil,
		&CompactChainDeps{TryCompact: func(ctx context.Context, messages []llm.Message, _ []*llm.Tool, isReactive bool) ([]llm.Message, bool, error) {
			assertAndExerciseMutedCompactionSink(t, ctx)
			if len(messages) != 1 || messages[0].TextContent() != "oversized history" {
				t.Fatalf("unexpected compact input=%+v", messages)
			}
			reactive = append(reactive, isReactive)
			return replacement, true, nil
		}},
	)
	ctx, sinkState := newCompactionStreamTestContext()
	ctx = toolpkg.WithRunID(ctx, "run-reactive")
	res, err := wrapped.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("oversized history"))}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || res.Message == nil || res.Message.TextContent() != "retried successfully" {
		t.Fatalf("result=%+v", res)
	}
	if len(reactive) != 1 || !reactive[0] {
		t.Fatalf("compactions=%v, want one reactive", reactive)
	}
	if inner.calls != 2 || len(inner.seen) != 2 {
		t.Fatalf("calls=%d seen=%+v", inner.calls, inner.seen)
	}
	if inner.seen[0][0].Compaction != nil || inner.seen[0][0].TextContent() != "oversized history" {
		t.Fatalf("first call input=%+v", inner.seen[0])
	}
	if len(inner.seen[1]) != 1 || inner.seen[1][0].Compaction == nil || inner.seen[1][0].Compaction.Type != "compaction_summary" {
		t.Fatalf("retry input=%+v", inner.seen[1])
	}
	sinkState.assertPreservedLifecycleOnly(t)
}

func cloneRecoverableTestMessages(messages []llm.Message) []llm.Message {
	out := make([]llm.Message, len(messages))
	for i := range messages {
		out[i] = messages[i]
		out[i].Parts = append([]llm.ContentPart(nil), messages[i].Parts...)
		if messages[i].Compaction != nil {
			state := *messages[i].Compaction
			out[i].Compaction = &state
		}
	}
	return out
}

// runnerWithLocalSettings builds a runner whose local settings hold rules, the
// way one lands there after "don't ask again for commands that start with ...".
func runnerWithLocalSettings(t *testing.T, body string) (*Runner, string) {
	t.Helper()
	home := t.TempDir()
	path := filepath.Join(home, "workspace", "state", "permissions", "local_settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &Runner{Deps: &Deps{Home: home}}
	r.permRuntimeGet().LoadFromDisk(r.AppCfg, r.permPaths())
	return r, path
}

const goTestPrefixSettings = `{"rules":{"allow":[
  {"tool_name":"Bash","command_prefix":["go","test"],"bypass_sandbox":true}
],"ask":[],"deny":[]}}`

// The sandbox-denied escalation prompt is forced (force_tool_approval), so the
// only thing that can stop it repeating is the remembered rule being matched.
func TestSandboxEscalationDoesNotRepromptForARememberedPrefix(t *testing.T) {
	r, _ := runnerWithLocalSettings(t, goTestPrefixSettings)
	payload := map[string]any{
		"command":               "go test ./internal/agentrun ./internal/tui ./internal/clifacade",
		"sandbox_permissions":   "require_escalated",
		"justification":         "command failed; retry without sandbox?",
		"force_tool_approval":   true,
		"approval_reason":       "sandbox_denied",
		"sandbox_denial_reason": "command failed; retry without sandbox?",
	}
	id, pending, err := r.actionHook(context.Background(), "shell", payload)
	if err != nil || pending || id != "" {
		t.Fatalf("remembered `go test` approval still prompted: id=%q pending=%v err=%v", id, pending, err)
	}
}

// A compound command derives no prefix, so it is remembered as itself. That
// rule has to end the same escalation prompt a prefix rule does — otherwise
// remembering it changed nothing the user can see.
func TestSandboxEscalationDoesNotRepromptForARememberedExactCommand(t *testing.T) {
	command := "gofmt -w internal/a.go && go mod tidy"
	r, _ := runnerWithLocalSettings(t, `{"rules":{"allow":[
      {"tool_name":"Bash","rule_content":"gofmt -w internal/a.go && go mod tidy","bypass_sandbox":true}
    ],"ask":[],"deny":[]}}`)
	id, pending, err := r.actionHook(context.Background(), "shell", map[string]any{
		"command":               command,
		"sandbox_permissions":   "require_escalated",
		"justification":         "command failed; retry without sandbox?",
		"force_tool_approval":   true,
		"approval_reason":       "sandbox_denied",
		"sandbox_denial_reason": "command failed; retry without sandbox?",
	})
	if err != nil || pending || id != "" {
		t.Fatalf("remembered exact command still prompted: id=%q pending=%v err=%v", id, pending, err)
	}
	// The rule names one command; anything else is still a fresh decision.
	if d := r.evaluateToolPermission("", "shell", map[string]any{"command": "gofmt -w internal/b.go"}); d.Matched != nil {
		t.Fatalf("exact rule leaked to another command: %+v", d)
	}
}

// The rule a "don't ask again" approval persists is derived from the command
// the shell tool actually runs, which for git is the rewritten form. Evaluating
// only what the model typed can never match it.
func TestRewrittenShellCommandMatchesTheRememberedRule(t *testing.T) {
	r, _ := runnerWithLocalSettings(t, `{"rules":{"allow":[
      {"tool_name":"Bash","command_prefix":["git","--no-pager","add"],"bypass_sandbox":true}
    ],"ask":[],"deny":[]}}`)
	decision := r.evaluateToolPermission("", "shell", map[string]any{"command": "git add -A"})
	if decision.Behavior != safety.BehaviorAllow || decision.Matched == nil {
		t.Fatalf("`git add -A` did not match the remembered `git --no-pager add` rule: %+v", decision)
	}
	if !decision.BypassSandbox {
		t.Fatal("matched rule did not carry its sandbox bypass")
	}
}

// A restriction written against the command as typed must not be sidesteppable
// by the rewrite.
func TestRewriteFallbackNeverOverridesAMatchedRestriction(t *testing.T) {
	r, _ := runnerWithLocalSettings(t, `{"rules":{"allow":[
      {"tool_name":"Bash","command_prefix":["git","--no-pager","add"],"bypass_sandbox":true}
    ],"ask":[],"deny":[{"tool_name":"Bash","rule_content":"git add:*"}]}}`)
	decision := r.evaluateToolPermission("", "shell", map[string]any{"command": "git add -A"})
	if decision.Behavior != safety.BehaviorDeny {
		t.Fatalf("deny rule for `git add` was bypassed by the rewrite: %+v", decision)
	}
}

// Replacing loaded rules from a file that could not be read discards approvals
// the user granted moments earlier, and they are asked for them again for the
// rest of the session.
func TestUnreadableSettingsFileKeepsLoadedRules(t *testing.T) {
	r, path := runnerWithLocalSettings(t, goTestPrefixSettings)
	if d := r.EvaluatePermissionForSession("", "Bash", "go test ./..."); d.Matched == nil {
		t.Fatal("rule was not loaded to begin with")
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.permRuntimeGet().LoadFromDisk(r.AppCfg, r.permPaths())
	if d := r.EvaluatePermissionForSession("", "Bash", "go test ./..."); d.Matched == nil {
		t.Fatalf("an unreadable settings file dropped the loaded rules: %+v", d)
	}
}

// An absent file still means "this agent has no rules for that scope", which is
// what keeps one agent's approvals out of another's store.
func TestAbsentSettingsFileClearsRules(t *testing.T) {
	r, path := runnerWithLocalSettings(t, goTestPrefixSettings)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	r.permRuntimeGet().LoadFromDisk(r.AppCfg, r.permPaths())
	if d := r.EvaluatePermissionForSession("", "Bash", "go test ./..."); d.Matched != nil {
		t.Fatalf("rules survived a scope with no settings file: %+v", d)
	}
}

// The grant a "don't ask again" approval persists has to apply on the first
// attempt, the way it does upstream, not only after the sandbox has already
// refused the command. With a sandbox backing shell commands, an unmatched
// command reaches the "run it, the sandbox contains it" default, which is an
// allow decision carrying no rule and no bypass. Stopping the evaluation there
// hid the remembered rule: the command was sandboxed, denied, and the model
// answered the denial by asking for escalated permissions itself.
func TestRememberedRuleAppliesOnTheFirstAttemptWithASandbox(t *testing.T) {
	r, _ := runnerWithLocalSettings(t, `{"rules":{"allow":[
      {"tool_name":"Bash","command_prefix":["git","--no-pager","add"],"bypass_sandbox":true}
    ],"ask":[],"deny":[]}}`)
	r.permRuntimeGet().Store(r.AppCfg).SetSandboxAvailable(true)

	for _, command := range []string{
		"git add -A",
		"git add -A && git status --short",
	} {
		decision := r.evaluateToolPermission("", "shell", map[string]any{"command": command})
		if decision.Behavior != safety.BehaviorAllow || decision.Matched == nil {
			t.Fatalf("%q did not match the remembered rule: %+v", command, decision)
		}
		if !decision.BypassSandbox {
			t.Fatalf("%q was allowed without the sandbox bypass its rule carries: %+v", command, decision)
		}
	}
}

// The rewritten form is the command that actually runs, so a restriction naming
// it applies too — the fallback may not be an allow-only lookup that lets a
// denied command through on the default.
func TestRewriteFallbackAppliesARestrictionOnTheRewrittenCommand(t *testing.T) {
	r, _ := runnerWithLocalSettings(t, `{"rules":{"allow":[],"ask":[],"deny":[
      {"tool_name":"Bash","rule_content":"git --no-pager push:*"}
    ]}}`)
	r.permRuntimeGet().Store(r.AppCfg).SetSandboxAvailable(true)
	decision := r.evaluateToolPermission("", "shell", map[string]any{"command": "git push origin main"})
	if decision.Behavior != safety.BehaviorDeny {
		t.Fatalf("deny rule written against the rewritten command did not apply: %+v", decision)
	}
}

func TestShouldRetryStatus529MatchesForegroundSources(t *testing.T) {
	for _, source := range []string{
		"repl_main_thread",
		"sdk",
		"agent:custom",
		"agent:default",
		"agent:builtin",
		"agent:builtin:fork",
		"agent:builtin:verification",
		"hook_agent",
		"hook_prompt",
		"verification_agent",
		"side_question",
		"auto_mode",
	} {
		if !ShouldRetryStatus529(source) {
			t.Fatalf("source %q must retry", source)
		}
	}
	if ShouldRetryStatus529("summary") {
		t.Fatalf("background source must not retry")
	}
	if !ShouldRetryStatus529("") {
		t.Fatalf("empty source must retry conservatively")
	}
}

func TestRetryDelayBaseAndCap(t *testing.T) {
	if got := RetryDelay(1); got != 500*time.Millisecond {
		t.Fatalf("attempt 1 delay=%s", got)
	}
	if got := RetryDelay(20); got > 5*time.Minute {
		t.Fatalf("retry delay exceeded cap: %s", got)
	}
	if got := Foreground529RetryCap(); got != 2 {
		t.Fatalf("foreground 529 retry cap=%d", got)
	}
}

func TestStatus529Detection(t *testing.T) {
	if !isStatus529(errors.New("HTTP 529 overloaded")) {
		t.Fatalf("expected 529 detection")
	}
	if isStatus529(errors.New("HTTP 500")) {
		t.Fatalf("unexpected 529 detection")
	}
}

// cancelDuringToolLLM reproduces the model side of an esc interrupt: the first
// sampling asks for a tool call, and any later sampling observes the cancelled
// run context and fails, exactly as a provider call does once the run is
// cancelled.
type cancelDuringToolLLM struct {
	mu    sync.Mutex
	calls int
}

func (m *cancelDuringToolLLM) Execute(ctx context.Context, _ []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.calls++
	n := m.calls
	m.mu.Unlock()
	if n == 1 {
		msg := llm.AssistantMessage(nil, llm.ToolCall{
			ID:       "call-read",
			Type:     llm.ToolTypeFunction,
			Function: llm.FunctionCall{Name: "read_file", Arguments: `{"file_path":"server.go"}`},
		})
		return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
	}
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("unreachable after cancel")})
	return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
}

func (m *cancelDuringToolLLM) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// twoToolTurnsLLM calls one named tool per model turn, in order, then answers.
type twoToolTurnsLLM struct {
	mu    sync.Mutex
	calls int
	tools []llm.ToolCall
}

func (m *twoToolTurnsLLM) Execute(context.Context, []llm.Message, []*llm.Tool) (*llm.Result, error) {
	m.mu.Lock()
	n := m.calls
	m.calls++
	m.mu.Unlock()
	if n < len(m.tools) {
		msg := llm.AssistantMessage(nil, m.tools[n])
		return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
	}
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("done")})
	return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
}

// A tool reports its result through the capture slot its own call opened. A
// subagent's whole run executes inside the parent's subagent_run call, so its
// tools run in a context that already carries a slot; when that slot was
// reused instead of replaced, whatever the child ran last and captured stayed
// in it, and the next child tool that captures nothing of its own adopted that
// result. Reading back saved notes returned a shell's exit_code/stdout map
// with no notes in it, and the card reported there were none.
func TestToolResultsAreNotAdoptedFromAnEarlierCallsCapture(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "capturing"})
	st.RegisterToolMeta(event.ToolMeta{Name: "reporting"})

	type emptyInput struct{}
	capturing, err := llm.NewTool("capturing", "captures its own output", func(ctx context.Context, _ *emptyInput) (string, error) {
		toolpkg.CaptureToolOutput(ctx, map[string]any{"exit_code": 0, "stdout": "ran a command"})
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("capturing tool: %v", err)
	}
	reporting, err := llm.NewRawTool("reporting", "returns its result directly", map[string]any{"type": "object"},
		func(context.Context, string) (any, error) {
			return map[string]any{"action": "read", "content": "the saved notes"}, nil
		})
	if err != nil {
		t.Fatalf("reporting tool: %v", err)
	}

	completed := map[string]map[string]any{}
	var mu sync.Mutex
	st.SetStepHook(func(_ context.Context, evt toolpkg.StepEvent) {
		if evt.Kind != event.RunEventToolCompleted {
			return
		}
		mu.Lock()
		completed[evt.ToolName] = evt.Output
		mu.Unlock()
	})

	inner := &twoToolTurnsLLM{tools: []llm.ToolCall{
		{ID: "call-1", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "capturing", Arguments: `{}`}},
		{ID: "call-2", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "reporting", Arguments: `{}`}},
	}}
	// The context a subagent's tools run in: the parent's subagent_run call has
	// already opened a capture slot here.
	ctx := toolpkg.WithToolCompletionCapture(context.Background())
	if _, err := wrapToolOrchestrationLLM(inner, st).Execute(
		ctx, []llm.Message{llm.UserMessage(llm.Text("investigate"))}, []*llm.Tool{capturing, reporting},
	); err != nil {
		t.Fatalf("execute: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	out := completed["reporting"]
	if got, _ := out["content"].(string); got != "the saved notes" {
		t.Fatalf("reporting tool lost its own result: %#v", out)
	}
	if _, leaked := out["exit_code"]; leaked {
		t.Fatalf("reporting tool adopted the earlier call's captured output: %#v", out)
	}
	if _, ok := completed["capturing"]["stdout"]; !ok {
		t.Fatalf("capturing tool lost its captured output: %#v", completed["capturing"])
	}
}

// Esc cancels the run while its tools are still executing. The tools return
// their context errors and the loop reaches the tool-boundary drain before the
// next sampling observes the cancellation. Draining there would report the
// queued message as delivered - the surface moves it out of the queue and into
// the transcript as a sent user message - while the model call that was to
// carry it is abandoned. The message must stay queued so the interrupted
// boundary can resubmit it.
func TestCancelledRunKeepsQueuedSteerInsteadOfDrainingAtToolBoundary(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "read_file", ReadOnly: true, ConcurrencySafe: true})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type readInput struct {
		FilePath string `json:"file_path"`
	}
	// The user presses esc while this tool runs, so it returns the run's
	// context error just like a cancelled shell command does.
	readTool, err := llm.NewTool("read_file", "read", func(_ context.Context, _ *readInput) (string, error) {
		cancel()
		return "", context.Canceled
	})
	if err != nil {
		t.Fatalf("read_file tool: %v", err)
	}

	rt := NewTurnInputRuntime()
	rt.Enqueue(TurnInputModeSteer, []llm.ContentPart{llm.Text("不需要重新构建dist，我手动构建")})
	delivered := 0
	rt.SetChangeHook(func(entries []TurnInputEntry) { delivered += len(entries) })

	inner := &cancelDuringToolLLM{}
	_, execErr := wrapToolOrchestrationLLM(inner, st).Execute(
		WithTurnInputRuntime(ctx, rt),
		[]llm.Message{llm.UserMessage(llm.Text("run the build"))},
		[]*llm.Tool{readTool},
	)

	if !errors.Is(execErr, context.Canceled) {
		t.Fatalf("expected the cancelled run to fail with context.Canceled, got %v", execErr)
	}
	if !rt.HasSteers() {
		t.Fatal("queued steer was drained into a cancelled turn: it can never reach the model, and the surface has already dropped it from the queue")
	}
	if delivered != 0 {
		t.Fatalf("change hook reported %d delivered steers on a cancelled turn; the surface renders those into the transcript as sent messages", delivered)
	}
	if got := inner.callCount(); got != 1 {
		t.Fatalf("model sampling calls=%d want 1 (the post-cancel sampling must not carry the steer)", got)
	}
}

// cancelAtFinalAnswerLLM cancels the run as it returns a tool-call-free answer,
// reproducing an esc that lands just as the turn was about to end.
type cancelAtFinalAnswerLLM struct {
	cancel context.CancelFunc
	mu     sync.Mutex
	calls  int
}

func (m *cancelAtFinalAnswerLLM) Execute(ctx context.Context, _ []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.calls++
	m.mu.Unlock()
	m.cancel()
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("first answer")})
	return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
}

func (m *cancelAtFinalAnswerLLM) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// The same guarantee on the tool-call-free path, where the drain exists to keep
// the turn going for one more sampling. A cancelled run never makes that
// sampling, so the steer must remain queued rather than be reported delivered.
func TestCancelledRunKeepsQueuedSteerInsteadOfDrainingAtFinalAnswer(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rt := NewTurnInputRuntime()
	rt.Enqueue(TurnInputModeSteer, []llm.ContentPart{llm.Text("actually, also do X")})
	delivered := 0
	rt.SetChangeHook(func(entries []TurnInputEntry) { delivered += len(entries) })

	inner := &cancelAtFinalAnswerLLM{cancel: cancel}
	_, err := wrapToolOrchestrationLLM(inner, st).Execute(
		WithTurnInputRuntime(ctx, rt),
		[]llm.Message{llm.UserMessage(llm.Text("go"))},
		nil,
	)
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("unexpected error: %v", err)
	}
	if !rt.HasSteers() {
		t.Fatal("queued steer was drained into a cancelled turn and can no longer be resubmitted")
	}
	if delivered != 0 {
		t.Fatalf("change hook reported %d delivered steers on a cancelled turn", delivered)
	}
	if got := inner.callCount(); got != 1 {
		t.Fatalf("model sampling calls=%d want 1", got)
	}
}

var errProviderFailed = errors.New("upstream provider error: 429 rate limited")

// failAfterToolCallLLM reproduces a provider failure on the sampling that was
// meant to carry a steer drained at the preceding tool boundary: the first call
// asks for a tool, the second (post-drain) call fails.
type failAfterToolCallLLM struct {
	mu    sync.Mutex
	calls int
}

func (m *failAfterToolCallLLM) Execute(_ context.Context, _ []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	m.mu.Lock()
	m.calls++
	n := m.calls
	m.mu.Unlock()
	if n == 1 {
		msg := llm.AssistantMessage(nil, llm.ToolCall{
			ID:       "call-read",
			Type:     llm.ToolTypeFunction,
			Function: llm.FunctionCall{Name: "read_file", Arguments: `{"file_path":"server.go"}`},
		})
		return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
	}
	return nil, errProviderFailed
}

func (m *failAfterToolCallLLM) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// The queue's delivery is only real once the model call carrying it returns.
// Draining at the tool boundary reports delivery immediately, so a provider
// error on the very next sampling strands the message: the surface has already
// moved it out of the queue preview and into the transcript as a sent user
// message, the failed turn is never answered, and no boundary resubmits it.
// The steer must come back to the queue so the turn boundary can resend it.
func TestFailedSamplingReturnsDrainedSteerToQueue(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "read_file", ReadOnly: true, ConcurrencySafe: true})

	type readInput struct {
		FilePath string `json:"file_path"`
	}
	readTool, err := llm.NewTool("read_file", "read", func(_ context.Context, _ *readInput) (string, error) {
		return "package main", nil
	})
	if err != nil {
		t.Fatalf("read_file tool: %v", err)
	}

	rt := NewTurnInputRuntime()
	rt.Enqueue(TurnInputModeSteer, []llm.ContentPart{llm.Text("also update the changelog")})
	delivered := 0
	rt.SetChangeHook(func(entries []TurnInputEntry) { delivered += len(entries) })

	capture := NewPartialSessionCapture()
	inner := &failAfterToolCallLLM{}
	ctx := WithPartialSessionCapture(WithTurnInputRuntime(context.Background(), rt), capture)
	_, execErr := wrapToolOrchestrationLLM(inner, st).Execute(
		ctx,
		[]llm.Message{llm.UserMessage(llm.Text("run the build"))},
		[]*llm.Tool{readTool},
	)

	if !errors.Is(execErr, errProviderFailed) {
		t.Fatalf("expected the provider error to propagate, got %v", execErr)
	}
	if got := inner.callCount(); got != 2 {
		t.Fatalf("model sampling calls=%d want 2 (drain happens at the tool boundary before the failing call)", got)
	}
	if !rt.HasSteers() {
		t.Fatal("queued steer was consumed by a sampling that failed: the model never saw it and the surface has already dropped it from the queue, so nothing resubmits it")
	}
	if delivered != 0 {
		t.Fatalf("change hook reported %d delivered steers for a failed sampling; the surface renders those into the transcript as sent messages", delivered)
	}
	for _, msg := range capture.Snapshot() {
		if msg.Role == llm.RoleUser && llm.TextContent(msg.Parts...) == "also update the changelog" {
			t.Fatal("the failed turn persisted the steer as a sent user message; it is going back to the queue, so persisting it here duplicates it on the retry")
		}
	}
}

// failAtFinalAnswerLLM answers without tool calls, which is the branch that
// injects a queued steer as user input to keep the turn going. The sampling
// that would answer the injected steer then fails.
type failAtFinalAnswerLLM struct {
	mu    sync.Mutex
	calls int
}

func (m *failAtFinalAnswerLLM) Execute(_ context.Context, _ []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	m.mu.Lock()
	m.calls++
	n := m.calls
	m.mu.Unlock()
	if n == 1 {
		msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("first answer")})
		return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
	}
	return nil, errProviderFailed
}

func (m *failAtFinalAnswerLLM) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// The same guarantee on the tool-call-free path: the injected steer only counts
// as delivered once the extra sampling it triggers actually returns.
func TestFailedSamplingReturnsInjectedSteerToQueue(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())

	rt := NewTurnInputRuntime()
	rt.Enqueue(TurnInputModeSteer, []llm.ContentPart{llm.Text("actually, also do X")})
	delivered := 0
	rt.SetChangeHook(func(entries []TurnInputEntry) { delivered += len(entries) })

	inner := &failAtFinalAnswerLLM{}
	_, err := wrapToolOrchestrationLLM(inner, st).Execute(
		WithTurnInputRuntime(context.Background(), rt),
		[]llm.Message{llm.UserMessage(llm.Text("go"))},
		nil,
	)
	if !errors.Is(err, errProviderFailed) {
		t.Fatalf("expected the provider error to propagate, got %v", err)
	}
	if got := inner.callCount(); got != 2 {
		t.Fatalf("model sampling calls=%d want 2", got)
	}
	if !rt.HasSteers() {
		t.Fatal("injected steer was lost to a failed sampling and can no longer be resubmitted")
	}
	if delivered != 0 {
		t.Fatalf("change hook reported %d delivered steers for a failed sampling", delivered)
	}
}

// A committed delivery must stay committed: once the sampling that carried the
// steer returns, the model has answered it and a later failure in the same turn
// must not push it back into the queue, where the surface would resend it.
func TestCommittedSteerIsNotReturnedByALaterFailure(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "read_file", ReadOnly: true, ConcurrencySafe: true})

	type readInput struct {
		FilePath string `json:"file_path"`
	}
	readTool, err := llm.NewTool("read_file", "read", func(_ context.Context, _ *readInput) (string, error) {
		return "package main", nil
	})
	if err != nil {
		t.Fatalf("read_file tool: %v", err)
	}

	rt := NewTurnInputRuntime()
	rt.Enqueue(TurnInputModeSteer, []llm.ContentPart{llm.Text("also update the changelog")})
	delivered := 0
	rt.SetChangeHook(func(entries []TurnInputEntry) { delivered += len(entries) })

	inner := &failOnThirdSamplingLLM{}
	_, execErr := wrapToolOrchestrationLLM(inner, st).Execute(
		WithTurnInputRuntime(context.Background(), rt),
		[]llm.Message{llm.UserMessage(llm.Text("run the build"))},
		[]*llm.Tool{readTool},
	)
	if !errors.Is(execErr, errProviderFailed) {
		t.Fatalf("expected the provider error to propagate, got %v", execErr)
	}
	if rt.HasSteers() {
		t.Fatal("a steer the model already answered was pushed back into the queue; the surface would send it a second time")
	}
	if delivered != 1 {
		t.Fatalf("change hook reported %d delivered steers, want 1 (the answered steer belongs in the transcript)", delivered)
	}
}

// failOnThirdSamplingLLM answers the drained steer on the second sampling (a
// further tool call) and only fails on the third.
type failOnThirdSamplingLLM struct {
	mu    sync.Mutex
	calls int
}

func (m *failOnThirdSamplingLLM) Execute(_ context.Context, _ []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	m.mu.Lock()
	m.calls++
	n := m.calls
	m.mu.Unlock()
	if n <= 2 {
		msg := llm.AssistantMessage(nil, llm.ToolCall{
			ID:       "call-read",
			Type:     llm.ToolTypeFunction,
			Function: llm.FunctionCall{Name: "read_file", Arguments: `{"file_path":"server.go"}`},
		})
		return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
	}
	return nil, errProviderFailed
}

func boolPtr(value bool) *bool { return &value }

type captureLLM struct {
	messages []llm.Message
	tools    []*llm.Tool
}

func (c *captureLLM) Execute(_ context.Context, messages []llm.Message, tools []*llm.Tool) (*llm.Result, error) {
	c.messages = append([]llm.Message(nil), messages...)
	c.tools = append([]*llm.Tool(nil), tools...)
	message := llm.AssistantMessage([]llm.ContentPart{llm.Text("ok")})
	return &llm.Result{Message: &message}, nil
}

// TestBuildDenialMessageIncludesFeedback verifies that buildDenialMessage
// appends the user's feedback when non-empty.
func TestBuildDenialMessageIncludesFeedback(t *testing.T) {
	withFeedback := buildDenialMessage("exit_plan_mode", "Please focus on the API design instead")
	if !strings.Contains(withFeedback, "User feedback: Please focus on the API design instead") {
		t.Fatalf("expected denial message to include feedback, got: %s", withFeedback)
	}
	if !strings.Contains(withFeedback, "exit_plan_mode") {
		t.Fatalf("expected denial message to include tool name, got: %s", withFeedback)
	}

	withoutFeedback := buildDenialMessage("write_file", "")
	if strings.Contains(withoutFeedback, "User feedback:") {
		t.Fatalf("expected denial message without feedback to not include feedback section, got: %s", withoutFeedback)
	}
}

// TestToolOrchestrationResumeDeniedIncludesFeedback verifies that when a tool
// call is denied with a DenyReason, the denial tool result message sent to the
// LLM includes the user's feedback text.
func TestToolOrchestrationResumeDeniedIncludesFeedback(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "gated", Destructive: true})
	st.SetActionHook(func(context.Context, string, any) (string, bool, error) {
		return "act-1", true, nil
	})

	gated, err := llm.NewTool("gated", "gated tool", func(ctx context.Context, _ *struct{}) (string, error) {
		if toolpkg.ApprovedActionIDFromContext(ctx) == "" {
			return "", &toolpkg.RequiresActionError{
				ActionID:   "act-1",
				ActionKind: "gated",
				ToolName:   "gated",
				ToolInput:  map[string]any{},
			}
		}
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("tool: %v", err)
	}

	inner := &approvalGatedSingleToolLLM{}
	wrapped := wrapToolOrchestrationLLM(inner, st)

	// First call: RAE fires.
	_, err = wrapped.Execute(
		context.Background(),
		[]llm.Message{llm.UserMessage(llm.Text("hi"))},
		[]*llm.Tool{gated},
	)
	var rae *toolpkg.RequiresActionError
	if !errors.As(err, &rae) || rae == nil {
		t.Fatalf("expected RequiresActionError, got %T %v", err, err)
	}

	// Resume with Denied=true and a DenyReason (user feedback).
	feedback := "The plan needs more detail on error handling"
	ctx := toolpkg.WithToolApprovalResume(context.Background(), &toolpkg.ToolApprovalResumeState{
		Session:    rae.SessionSnapshot,
		Denied:     true,
		DenyReason: feedback,
	})

	_, rerr := wrapped.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("ignored"))}, []*llm.Tool{gated})
	if rerr != nil {
		t.Fatalf("resume Execute err=%v", rerr)
	}

	// The second LLM call should have received the denial message with feedback.
	if inner.calls < 2 {
		t.Fatalf("expected at least 2 LLM calls, got %d", inner.calls)
	}
	resumeSession := inner.sessions[1]
	var foundDenial bool
	for _, msg := range resumeSession {
		if msg.Role == llm.RoleTool {
			for _, part := range msg.Parts {
				if part.Type == llm.ContentTypeText {
					if strings.Contains(part.Text, "denied by user") && strings.Contains(part.Text, feedback) {
						foundDenial = true
					}
				}
			}
		}
	}
	if !foundDenial {
		t.Fatalf("denial message with feedback not found in resume session messages")
	}
}

// exitPlanCallLLM emits one exit_plan_mode tool call, then a plain answer —
// the shape of a model that asks to leave plan mode and is told no.
type exitPlanCallLLM struct {
	calls    int
	sessions [][]llm.Message
}

func (m *exitPlanCallLLM) Execute(_ context.Context, messages []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	m.calls++
	m.sessions = append(m.sessions, append([]llm.Message(nil), messages...))
	if m.calls == 1 {
		msg := llm.AssistantMessage(nil,
			llm.ToolCall{ID: "call-1", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "exit_plan_mode", Arguments: `{}`}},
		)
		return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
	}
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("answer after the refusal")})
	return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
}

// The denial tool result carries its display half: the model still receives
// the instruction text buildDenialMessage writes, while the card a replay
// draws from the persisted row shows the one sentence the live card showed —
// the user's own words — not the text written for the model.
func TestDeniedToolResultCarriesItsDisplay(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "exit_plan_mode", Destructive: true})
	st.SetActionHook(func(context.Context, string, any) (string, bool, error) {
		return "act-1", true, nil
	})

	gated, err := llm.NewTool("exit_plan_mode", "gated tool", func(ctx context.Context, _ *struct{}) (string, error) {
		if toolpkg.ApprovedActionIDFromContext(ctx) == "" {
			return "", &toolpkg.RequiresActionError{
				ActionID:   "act-1",
				ActionKind: "exit_plan_mode",
				ToolName:   "exit_plan_mode",
				ToolInput:  map[string]any{},
			}
		}
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("tool: %v", err)
	}

	inner := &exitPlanCallLLM{}
	wrapped := wrapToolOrchestrationLLM(inner, st)

	_, err = wrapped.Execute(
		context.Background(),
		[]llm.Message{llm.UserMessage(llm.Text("hi"))},
		[]*llm.Tool{gated},
	)
	var rae *toolpkg.RequiresActionError
	if !errors.As(err, &rae) || rae == nil {
		t.Fatalf("expected RequiresActionError, got %T %v", err, err)
	}

	composed := turn.ComposeDenyFeedback(nil, "改成先写测试")
	ctx := toolpkg.WithToolApprovalResume(context.Background(), &toolpkg.ToolApprovalResumeState{
		Session:      rae.SessionSnapshot,
		Denied:       true,
		DenyReason:   composed,
		DenyFeedback: "改成先写测试",
	})
	if _, rerr := wrapped.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("ignored"))}, []*llm.Tool{gated}); rerr != nil {
		t.Fatalf("resume Execute err=%v", rerr)
	}

	var denial *llm.Message
	for i := range inner.sessions[1] {
		msg := inner.sessions[1][i]
		if msg.Role != llm.RoleTool {
			continue
		}
		for _, part := range msg.Parts {
			if part.Type == llm.ContentTypeText && strings.Contains(part.Text, "denied by user") {
				denial = &msg
			}
		}
	}
	if denial == nil {
		t.Fatal("the denial tool result is missing from the resumed session")
	}
	if denial.ToolDisplay == nil {
		t.Fatal("the denial tool result must carry the display the replay draws from")
	}
	if got := denial.ToolDisplay.Body; got != toolpkg.DeniedToolDisplayBody("exit_plan_mode", "改成先写测试") {
		t.Fatalf("denial display body = %q, want the sentence the live card shows", got)
	}
	if body := denial.ToolDisplay.Body; strings.Contains(body, "Tool approval denied by user") {
		t.Fatalf("the display half must not show the model-facing instruction: %q", body)
	}
	var meta struct {
		ToolName string `json:"tool_name"`
		Status   string `json:"status"`
	}
	if err := json.Unmarshal([]byte(denial.ToolDisplay.ToolMetaJSON), &meta); err != nil {
		t.Fatalf("denial display meta = %q: %v", denial.ToolDisplay.ToolMetaJSON, err)
	}
	if meta.ToolName != "exit_plan_mode" || meta.Status != "denied" {
		t.Fatalf("denial display meta = %#v, want the refused call named as denied", meta)
	}
}

// A delivered review closed the gate, not the user: the denial result's
// display half is the handoff line — the same sentence every other display of
// the delivery says — never the model-facing guidance and never "(no output)".
func TestDeliveredReviewDenialCarriesTheHandoffLine(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "exit_plan_mode", Destructive: true})
	st.SetActionHook(func(context.Context, string, any) (string, bool, error) {
		return "act-1", true, nil
	})

	gated, err := llm.NewTool("exit_plan_mode", "gated tool", func(ctx context.Context, _ *struct{}) (string, error) {
		if toolpkg.ApprovedActionIDFromContext(ctx) == "" {
			return "", &toolpkg.RequiresActionError{
				ActionID:   "act-1",
				ActionKind: "exit_plan_mode",
				ToolName:   "exit_plan_mode",
				ToolInput:  map[string]any{},
			}
		}
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("tool: %v", err)
	}

	inner := &exitPlanCallLLM{}
	wrapped := wrapToolOrchestrationLLM(inner, st)

	_, err = wrapped.Execute(
		context.Background(),
		[]llm.Message{llm.UserMessage(llm.Text("hi"))},
		[]*llm.Tool{gated},
	)
	var rae *toolpkg.RequiresActionError
	if !errors.As(err, &rae) || rae == nil {
		t.Fatalf("expected RequiresActionError, got %T %v", err, err)
	}

	ctx := toolpkg.WithToolApprovalResume(context.Background(), &toolpkg.ToolApprovalResumeState{
		Session: rae.SessionSnapshot,
		Denied:  true,
		// The resume a delivered review produces: the delivery guidance for
		// the model, no user words, the delivery flag for the display.
		DenyReason:      turn.ComposeReviewDeliveryGuidance(nil),
		DeliveredReview: true,
	})
	if _, rerr := wrapped.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("ignored"))}, []*llm.Tool{gated}); rerr != nil {
		t.Fatalf("resume Execute err=%v", rerr)
	}

	var denial *llm.Message
	for i := range inner.sessions[1] {
		msg := inner.sessions[1][i]
		if msg.Role != llm.RoleTool {
			continue
		}
		for _, part := range msg.Parts {
			if part.Type == llm.ContentTypeText && strings.Contains(part.Text, "denied by user") {
				denial = &msg
			}
		}
	}
	if denial == nil {
		t.Fatal("the denial tool result is missing from the resumed session")
	}
	if denial.ToolDisplay == nil {
		t.Fatal("the denial tool result must carry the display the replay draws from")
	}
	if got := denial.ToolDisplay.Body; got != toolpkg.PlanReviewDeliveredDisplayKey {
		t.Fatalf("denial display body = %q, want the delivery drop key", got)
	}
	if body := denial.ToolDisplay.Body; strings.Contains(body, "(no output)") || strings.Contains(body, "The plan review you asked for has returned") {
		t.Fatalf("the display half must not show the empty-refusal or model-facing text: %q", body)
	}
}

// TestToolOrchestrationResumeDeniedWithoutFeedback verifies that denying
// without a DenyReason still produces a valid denial message (backward compat).
func TestToolOrchestrationResumeDeniedWithoutFeedback(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "gated", Destructive: true})
	st.SetActionHook(func(context.Context, string, any) (string, bool, error) {
		return "act-1", true, nil
	})

	gated, err := llm.NewTool("gated", "gated tool", func(ctx context.Context, _ *struct{}) (string, error) {
		if toolpkg.ApprovedActionIDFromContext(ctx) == "" {
			return "", &toolpkg.RequiresActionError{
				ActionID:   "act-1",
				ActionKind: "gated",
				ToolName:   "gated",
				ToolInput:  map[string]any{},
			}
		}
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("tool: %v", err)
	}

	inner := &approvalGatedSingleToolLLM{}
	wrapped := wrapToolOrchestrationLLM(inner, st)

	// First call: RAE fires.
	_, err = wrapped.Execute(
		context.Background(),
		[]llm.Message{llm.UserMessage(llm.Text("hi"))},
		[]*llm.Tool{gated},
	)
	var rae *toolpkg.RequiresActionError
	if !errors.As(err, &rae) || rae == nil {
		t.Fatalf("expected RequiresActionError, got %T %v", err, err)
	}

	// Resume with Denied=true and NO DenyReason.
	ctx := toolpkg.WithToolApprovalResume(context.Background(), &toolpkg.ToolApprovalResumeState{
		Session: rae.SessionSnapshot,
		Denied:  true,
	})

	_, rerr := wrapped.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("ignored"))}, []*llm.Tool{gated})
	if rerr != nil {
		t.Fatalf("resume Execute err=%v", rerr)
	}

	// Verify the denial message exists but does NOT include "User feedback:".
	if inner.calls < 2 {
		t.Fatalf("expected at least 2 LLM calls, got %d", inner.calls)
	}
	resumeSession := inner.sessions[1]
	var foundDenial bool
	for _, msg := range resumeSession {
		if msg.Role == llm.RoleTool {
			for _, part := range msg.Parts {
				if part.Type == llm.ContentTypeText {
					if strings.Contains(part.Text, "denied by user") {
						foundDenial = true
						if strings.Contains(part.Text, "User feedback:") {
							t.Fatalf("denial message should not include feedback section when DenyReason is empty")
						}
					}
				}
			}
		}
	}
	if !foundDenial {
		t.Fatalf("denial message not found in resume session messages")
	}
}

type orchestrationScriptLLM struct {
	mu       sync.Mutex
	messages [][]llm.Message
}

type singleSubagentFanoutLLM struct {
	calls int
}

func (m *singleSubagentFanoutLLM) Execute(context.Context, []llm.Message, []*llm.Tool) (*llm.Result, error) {
	m.calls++
	if m.calls == 1 {
		msg := llm.AssistantMessage(nil, llm.ToolCall{
			ID:       "call-subagent-fanout",
			Type:     llm.ToolTypeFunction,
			Function: llm.FunctionCall{Name: "subagent_fanout", Arguments: `{}`},
		})
		return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
	}
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("done")})
	return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
}

func TestToolOrchestrationDoesNotPromoteCoordinatorModeAfterFanoutOnly(t *testing.T) {
	home := t.TempDir()
	st := toolpkg.NewState(home)
	st.RegisterToolMeta(event.ToolMeta{Name: "subagent_fanout", ReadOnly: true})
	tool, err := llm.NewTool("subagent_fanout", "fanout", func(context.Context, *struct{}) (string, error) {
		return `{"ok":true}`, nil
	})
	if err != nil {
		t.Fatalf("subagent_fanout tool: %v", err)
	}
	ctx := llm.WithAgentSessionID(context.Background(), "sid-no-promote")
	ctx = WithQuerySource(ctx, "repl_main_thread")
	ctx = WithTurnInputRuntime(ctx, NewTurnInputRuntime())
	if _, err := wrapToolOrchestrationLLMWithHome(&singleSubagentFanoutLLM{}, st, home).Execute(
		ctx,
		[]llm.Message{llm.UserMessage(llm.Text("research in parallel"))},
		[]*llm.Tool{tool},
	); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := toolpkg.ModeFromContext(st.ContextWithRuntimeSessionMode(llm.WithAgentSessionID(context.Background(), "sid-no-promote"))); got != "" {
		t.Fatalf("runtime session mode=%q want empty", got)
	}
	modeState, err := state.Get(home, "sid-no-promote")
	if err != nil {
		t.Fatalf("state.Get: %v", err)
	}
	if modeState.Mode != state.ModeAgent {
		t.Fatalf("persisted mode=%q want agent", modeState.Mode)
	}
}

func (s *orchestrationScriptLLM) Execute(_ context.Context, messages []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := append([]llm.Message(nil), messages...)
	s.messages = append(s.messages, cp)
	switch len(s.messages) {
	case 1:
		msg := llm.AssistantMessage(nil,
			llm.ToolCall{ID: "call-read-1", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "read_one", Arguments: `{}`}},
			llm.ToolCall{ID: "call-read-2", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "read_two", Arguments: `{}`}},
			llm.ToolCall{ID: "call-write", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "write_one", Arguments: `{}`}},
		)
		return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
	default:
		msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("done")})
		return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
	}
}

type usageAccumulatingLLM struct {
	calls int
}

func (m *usageAccumulatingLLM) Execute(_ context.Context, _ []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	m.calls++
	switch m.calls {
	case 1:
		msg := llm.AssistantMessage(nil,
			llm.ToolCall{ID: "call-read", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "read_file", Arguments: `{"file_path":"a.go"}`}},
		)
		return &llm.Result{Message: &msg, Usage: &llm.Usage{InputTokens: 100, OutputTokens: 20}}, nil
	default:
		msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("done")})
		return &llm.Result{Message: &msg, Usage: &llm.Usage{InputTokens: 200, OutputTokens: 30}}, nil
	}
}

type orchestrationTraceRecorder struct {
	mu     sync.Mutex
	events []agent.Event
}

func (r *orchestrationTraceRecorder) Trace(event agent.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func TestToolOrchestrationRunsSafeBatchBeforeSerialWriteAndReplaysOrder(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "read_one", ReadOnly: true, ConcurrencySafe: true})
	st.RegisterToolMeta(event.ToolMeta{Name: "read_two", ReadOnly: true, ConcurrencySafe: true})
	st.RegisterToolMeta(event.ToolMeta{Name: "write_one", Destructive: true})

	readStarted := make(chan string, 2)
	releaseReads := make(chan struct{})
	var writeStartedAt time.Time
	var readDoneAt time.Time
	var mu sync.Mutex

	readTool := func(name string) *llm.Tool {
		t.Helper()
		tool, err := llm.NewTool(name, "read", func(context.Context, *struct{}) (string, error) {
			readStarted <- name
			<-releaseReads
			mu.Lock()
			readDoneAt = time.Now()
			mu.Unlock()
			return name + "-out", nil
		})
		if err != nil {
			t.Fatalf("tool %s: %v", name, err)
		}
		return tool
	}
	writeTool, err := llm.NewTool("write_one", "write", func(context.Context, *struct{}) (string, error) {
		mu.Lock()
		writeStartedAt = time.Now()
		mu.Unlock()
		return "write-out", nil
	})
	if err != nil {
		t.Fatalf("write tool: %v", err)
	}

	inner := &orchestrationScriptLLM{}
	wrapped := wrapToolOrchestrationLLM(inner, st)
	rec := &orchestrationTraceRecorder{}
	ctx := agent.WithTracer(context.Background(), rec)

	done := make(chan error, 1)
	go func() {
		_, err := wrapped.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("go"))}, []*llm.Tool{
			readTool("read_one"),
			readTool("read_two"),
			writeTool,
		})
		done <- err
	}()

	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case name := <-readStarted:
			seen[name] = true
		case <-time.After(time.Second):
			t.Fatalf("safe read batch did not start concurrently; seen=%v", seen)
		}
	}
	inProgress := st.InProgressToolIDs()
	if strings.Join(inProgress, ",") != "call-read-1,call-read-2" {
		t.Fatalf("in-progress ids while read batch running=%v", inProgress)
	}
	close(releaseReads)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("orchestrated execution did not finish")
	}

	mu.Lock()
	if writeStartedAt.IsZero() || readDoneAt.IsZero() || writeStartedAt.Before(readDoneAt) {
		t.Fatalf("write started before read batch finished: write=%s readDone=%s", writeStartedAt, readDoneAt)
	}
	mu.Unlock()

	if len(inner.messages) != 2 {
		t.Fatalf("model calls=%d want 2", len(inner.messages))
	}
	replay := inner.messages[1]
	if len(replay) < 4 {
		t.Fatalf("expected assistant tool call plus three tool results, got %d messages", len(replay))
	}
	var ids []string
	for _, msg := range replay {
		if msg.Role == llm.RoleTool {
			ids = append(ids, msg.ToolCallID)
		}
	}
	if strings.Join(ids, ",") != "call-read-1,call-read-2,call-write" {
		t.Fatalf("tool result replay order=%v", ids)
	}
	if got := st.InProgressToolIDs(); len(got) != 0 {
		t.Fatalf("in-progress ids leaked after completion=%v", got)
	}
}

func TestToolOrchestrationAggregatesUsageAcrossInternalLLMCalls(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "read_file", ReadOnly: true, ConcurrencySafe: true})

	type readInput struct {
		FilePath string `json:"file_path"`
	}
	readTool, err := llm.NewTool("read_file", "read", func(_ context.Context, in *readInput) (string, error) {
		return in.FilePath, nil
	})
	if err != nil {
		t.Fatalf("read_file tool: %v", err)
	}

	res, err := wrapToolOrchestrationLLM(&usageAccumulatingLLM{}, st).Execute(
		context.Background(),
		[]llm.Message{llm.UserMessage(llm.Text("go"))},
		[]*llm.Tool{readTool},
	)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res == nil || res.Usage == nil {
		t.Fatalf("expected non-nil usage")
	}
	if res.Usage.InputTokens != 300 || res.Usage.OutputTokens != 50 {
		t.Fatalf("usage=%+v want input=300 output=50", *res.Usage)
	}
}

type steeringScriptLLM struct {
	mu       sync.Mutex
	messages [][]llm.Message
}

func (s *steeringScriptLLM) Execute(_ context.Context, messages []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := append([]llm.Message(nil), messages...)
	s.messages = append(s.messages, cp)
	switch len(s.messages) {
	case 1:
		msg := llm.AssistantMessage(nil,
			llm.ToolCall{ID: "call-read", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "read_file", Arguments: `{"file_path":"server.go"}`}},
		)
		return &llm.Result{Message: &msg, Usage: &llm.Usage{InputTokens: 10, OutputTokens: 2}}, nil
	default:
		msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("done after steer")})
		return &llm.Result{Message: &msg, Usage: &llm.Usage{InputTokens: 20, OutputTokens: 4}}, nil
	}
}

func TestToolOrchestrationConsumesPendingSteerBeforeNextSampling(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "read_file", ReadOnly: true, ConcurrencySafe: true})

	type readInput struct {
		FilePath string `json:"file_path"`
	}
	readTool, err := llm.NewTool("read_file", "read", func(_ context.Context, in *readInput) (string, error) {
		return in.FilePath, nil
	})
	if err != nil {
		t.Fatalf("read_file tool: %v", err)
	}

	inner := &steeringScriptLLM{}
	rt := NewTurnInputRuntime()
	rt.Enqueue(TurnInputModeSteer, []llm.ContentPart{llm.Text("please adjust approach")})
	ctx := WithTurnInputRuntime(context.Background(), rt)
	res, err := wrapToolOrchestrationLLM(inner, st).Execute(
		ctx,
		[]llm.Message{llm.UserMessage(llm.Text("go"))},
		[]*llm.Tool{readTool},
	)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res == nil || res.Message == nil || res.Message.TextContent() != "done after steer" {
		t.Fatalf("unexpected result: %#v", res)
	}
	if len(inner.messages) != 2 {
		t.Fatalf("model calls=%d want 2", len(inner.messages))
	}
	second := inner.messages[1]
	if len(second) < 4 {
		t.Fatalf("expected assistant tool call, tool result, and steered user message before second sample; got %d messages", len(second))
	}
	last := second[len(second)-1]
	if last.Role != llm.RoleUser {
		t.Fatalf("expected steered user message before second sample, got role=%q", last.Role)
	}
	if got := last.TextContent(); got != "please adjust approach" {
		t.Fatalf("steered user content=%q want %q", got, "please adjust approach")
	}
	if rt.HasSteers() {
		t.Fatalf("expected steer runtime queue drained after sampling")
	}
}

type responseBoundarySteeringLLM struct {
	calls          int
	failAfterStart bool
}

func (m *responseBoundarySteeringLLM) Execute(ctx context.Context, _ []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	m.calls++
	if m.calls == 1 {
		msg := llm.AssistantMessage(nil,
			llm.ToolCall{ID: "call-read", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "read_file", Arguments: `{"file_path":"server.go"}`}},
		)
		return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
	}
	sink := llm.StreamSinkFrom(ctx)
	if sink != nil && sink.OnResponseStarted != nil {
		sink.OnResponseStarted()
	}
	if sink != nil && sink.OnReasoningDelta != nil {
		sink.OnReasoningDelta("reasoning after steer")
	}
	if m.failAfterStart {
		return nil, errors.New("stream failed after response start")
	}
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("done")})
	return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
}

func responseBoundarySteerFixture(t *testing.T, failAfterStart bool) (*toolpkg.State, *TurnInputRuntime, *llm.Tool, *responseBoundarySteeringLLM) {
	t.Helper()
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "read_file", ReadOnly: true, ConcurrencySafe: true})
	tool, err := llm.NewTool("read_file", "read", func(context.Context, *struct {
		FilePath string `json:"file_path"`
	}) (string, error) {
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("read_file tool: %v", err)
	}
	rt := NewTurnInputRuntime()
	rt.Enqueue(TurnInputModeSteer, []llm.ContentPart{llm.Text("steer before reasoning")})
	return st, rt, tool, &responseBoundarySteeringLLM{failAfterStart: failAfterStart}
}

// A streamed model response is emitted while Execute is still running. The
// delivery acknowledgement must therefore happen at the first output event,
// not after Execute returns, or the transcript shows the resulting reasoning
// before the user steer that caused it.
func TestSteerDeliveryCommitsBeforeFirstStreamEvent(t *testing.T) {
	st, rt, tool, inner := responseBoundarySteerFixture(t, false)
	var events []string
	rt.SetChangeHook(func([]TurnInputEntry) { events = append(events, "steer delivered") })
	ctx := WithTurnInputRuntime(context.Background(), rt)
	ctx = llm.WithStreamSink(ctx, &llm.StreamSink{
		OnReasoningDelta: func(string) { events = append(events, "reasoning") },
	})

	_, err := wrapToolOrchestrationLLM(inner, st).Execute(
		ctx,
		[]llm.Message{llm.UserMessage(llm.Text("go"))},
		[]*llm.Tool{tool},
	)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got, want := strings.Join(events, ","), "steer delivered,reasoning"; got != want {
		t.Fatalf("event order = %q, want %q", got, want)
	}
	if rt.HasSteers() {
		t.Fatal("steer remained queued after the provider began responding")
	}
}

// Once any output event has arrived, the model demonstrably received the
// steer. A later stream error must keep it committed; rolling it back would both
// duplicate it on retry and move the user row behind already-rendered output.
func TestSteerDeliveryStaysCommittedWhenStreamFailsAfterResponseStart(t *testing.T) {
	st, rt, tool, inner := responseBoundarySteerFixture(t, true)
	var events []string
	rt.SetChangeHook(func([]TurnInputEntry) { events = append(events, "steer delivered") })
	capture := NewPartialSessionCapture()
	ctx := WithTurnInputRuntime(context.Background(), rt)
	ctx = WithPartialSessionCapture(ctx, capture)
	ctx = llm.WithStreamSink(ctx, &llm.StreamSink{
		OnReasoningDelta: func(string) { events = append(events, "reasoning") },
	})

	_, err := wrapToolOrchestrationLLM(inner, st).Execute(
		ctx,
		[]llm.Message{llm.UserMessage(llm.Text("go"))},
		[]*llm.Tool{tool},
	)
	if err == nil || !strings.Contains(err.Error(), "stream failed after response start") {
		t.Fatalf("Execute error = %v", err)
	}
	if got, want := strings.Join(events, ","), "steer delivered,reasoning"; got != want {
		t.Fatalf("event order = %q, want %q", got, want)
	}
	if rt.HasSteers() {
		t.Fatal("already-delivered steer was incorrectly rolled back after a partial response")
	}
	snapshot := capture.Snapshot()
	if len(snapshot) == 0 || snapshot[len(snapshot)-1].Role != llm.RoleUser || snapshot[len(snapshot)-1].TextContent() != "steer before reasoning" {
		t.Fatalf("partial session lost committed steer: %#v", snapshot)
	}
}

type steerWithoutToolCallLLM struct {
	mu       sync.Mutex
	messages [][]llm.Message
}

func (s *steerWithoutToolCallLLM) Execute(_ context.Context, messages []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := append([]llm.Message(nil), messages...)
	s.messages = append(s.messages, cp)
	switch len(s.messages) {
	case 1:
		// Final response with no tool calls — the turn would normally end here.
		msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("first answer")})
		return &llm.Result{Message: &msg, Usage: &llm.Usage{InputTokens: 10, OutputTokens: 2}}, nil
	default:
		msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("addressed steer")})
		return &llm.Result{Message: &msg, Usage: &llm.Usage{InputTokens: 20, OutputTokens: 4}}, nil
	}
}

// A steer enqueued during a tool-call-free response must still be delivered even
// though the turn was about to complete — regression for queued messages that
// were silently dropped when no further tool call followed.
func TestToolOrchestrationConsumesPendingSteerWhenTurnHasNoToolCall(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())

	inner := &steerWithoutToolCallLLM{}
	rt := NewTurnInputRuntime()
	rt.Enqueue(TurnInputModeSteer, []llm.ContentPart{llm.Text("actually, also do X")})
	ctx := WithTurnInputRuntime(context.Background(), rt)
	res, err := wrapToolOrchestrationLLM(inner, st).Execute(
		ctx,
		[]llm.Message{llm.UserMessage(llm.Text("go"))},
		nil,
	)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res == nil || res.Message == nil || res.Message.TextContent() != "addressed steer" {
		t.Fatalf("expected the model to respond to the steer, got %#v", res)
	}
	if len(inner.messages) != 2 {
		t.Fatalf("model calls=%d want 2 (steer should re-sample)", len(inner.messages))
	}
	second := inner.messages[1]
	last := second[len(second)-1]
	if last.Role != llm.RoleUser {
		t.Fatalf("expected steered user message before second sample, got role=%q", last.Role)
	}
	if got := last.TextContent(); got != "actually, also do X" {
		t.Fatalf("steered user content=%q want %q", got, "actually, also do X")
	}
	prev := second[len(second)-2]
	if prev.Role != llm.RoleAssistant || prev.TextContent() != "first answer" {
		t.Fatalf("expected prior assistant answer retained before steer, got role=%q text=%q", prev.Role, prev.TextContent())
	}
	if rt.HasSteers() {
		t.Fatalf("expected steer runtime queue drained")
	}
}

// retractDuringCallLLM answers the first sampling with a tool call. On the
// second — the one carrying the queued steers — it stands in for the user
// pressing the recall key while the request waits on the provider: it recalls
// from the conversation queue, then waits for the abort that recall triggers.
// Every later sampling finishes the turn.
type retractDuringCallLLM struct {
	queue    *InputQueue
	recalled []Input
	calls    [][]llm.Message
}

func (m *retractDuringCallLLM) Execute(ctx context.Context, messages []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	m.calls = append(m.calls, append([]llm.Message(nil), messages...))
	switch len(m.calls) {
	case 1:
		msg := llm.AssistantMessage(nil, llm.ToolCall{ID: "call-read", Type: llm.ToolTypeFunction,
			Function: llm.FunctionCall{Name: "read_file", Arguments: `{"file_path":"server.go"}`}})
		return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
	case 2:
		in, ok := m.queue.Recall()
		if !ok {
			return nil, errors.New("recall refused a steer that is still in the queue")
		}
		m.recalled = append(m.recalled, in)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Second):
			return nil, errors.New("recalling an in-flight steer did not abort the call carrying it")
		}
	default:
		msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("done")})
		return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
	}
}

func readFileTestTool(t *testing.T) *llm.Tool {
	t.Helper()
	type readInput struct {
		FilePath string `json:"file_path"`
	}
	readTool, err := llm.NewTool("read_file", "read", func(_ context.Context, _ *readInput) (string, error) {
		return "package main", nil
	})
	if err != nil {
		t.Fatalf("read_file tool: %v", err)
	}
	return readTool
}

// A steer taken for a model call that has not begun answering still shows in
// the queue, so recall must hand it back — and the model must never see it:
// the call carrying it is abandoned and re-sent with only the steers left.
func TestRetractedInFlightSteerIsResentWithoutIt(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "read_file", ReadOnly: true, ConcurrencySafe: true})
	q := NewInputQueue()
	rt := NewTurnInputRuntime()
	q.Attach(rt)
	q.Steer(Input{Text: "keep the old API", Parts: []llm.ContentPart{llm.Text("keep the old API")}})
	q.Steer(Input{Text: "never mind", Parts: []llm.ContentPart{llm.Text("never mind")}})

	inner := &retractDuringCallLLM{queue: q}
	res, err := wrapToolOrchestrationLLM(inner, st).Execute(
		WithTurnInputRuntime(context.Background(), rt),
		[]llm.Message{llm.UserMessage(llm.Text("go"))},
		[]*llm.Tool{readFileTestTool(t)},
	)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res == nil || res.Message == nil || res.Message.TextContent() != "done" {
		t.Fatalf("unexpected result: %#v", res)
	}
	if len(inner.recalled) != 1 || inner.recalled[0].Text != "never mind" {
		t.Fatalf("recalled %#v, want the newest steer", inner.recalled)
	}
	if len(inner.calls) != 3 {
		t.Fatalf("model calls=%d want 3 (tool call, abandoned call, re-sent call)", len(inner.calls))
	}
	third := inner.calls[2]
	if last := third[len(third)-1]; last.Role != llm.RoleUser || last.TextContent() != "keep the old API" {
		t.Fatalf("re-sent call must end with the steer left, got %v", roles(third))
	}
	for _, msg := range append(append([]llm.Message(nil), third...), res.Session...) {
		if msg.TextContent() == "never mind" {
			t.Fatal("the retracted steer reached the model or the persisted session")
		}
	}
	if delivered := q.TakeDelivered(); len(delivered) != 1 || delivered[0].Text != "keep the old API" {
		t.Fatalf("delivered = %#v, want only the steer the model answered", delivered)
	}
	if preview := q.Preview(); preview.Visible() {
		t.Fatalf("queue not empty: %#v", preview)
	}
}

// retractAtAnswerLLM ends the turn without a tool call — the branch that
// reopens it for a queued steer — and then, on the sampling that carries the
// steer, recalls it the way the user's recall key would.
type retractAtAnswerLLM struct {
	queue *InputQueue
	calls int
}

func (m *retractAtAnswerLLM) Execute(ctx context.Context, _ []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	m.calls++
	if m.calls == 1 {
		msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("first answer")})
		return &llm.Result{Message: &msg, Usage: &llm.Usage{InputTokens: 10, OutputTokens: 2}}, nil
	}
	if _, ok := m.queue.Recall(); !ok {
		return nil, errors.New("recall refused a steer that is still in the queue")
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(5 * time.Second):
		return nil, errors.New("recalling an in-flight steer did not abort the call carrying it")
	}
}

// When every steer that reopened a finished turn is retracted, the turn ends
// on the answer it had already reached — once, and counted once.
func TestRetractingEveryReopeningSteerEndsTurnOnTheAnswer(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	q := NewInputQueue()
	rt := NewTurnInputRuntime()
	q.Attach(rt)
	q.Steer(Input{Text: "actually, also do X", Parts: []llm.ContentPart{llm.Text("actually, also do X")}})

	inner := &retractAtAnswerLLM{queue: q}
	res, err := wrapToolOrchestrationLLM(inner, st).Execute(
		WithTurnInputRuntime(context.Background(), rt),
		[]llm.Message{llm.UserMessage(llm.Text("go"))},
		nil,
	)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if inner.calls != 2 {
		t.Fatalf("model calls=%d want 2 (the answer, then the abandoned call)", inner.calls)
	}
	if res == nil || res.Message == nil || res.Message.TextContent() != "first answer" {
		t.Fatalf("the turn must end on the answer it had reached, got %#v", res)
	}
	if len(res.Session) != 2 || res.Session[1].TextContent() != "first answer" {
		t.Fatalf("session = %v, want the request and the answer once", roles(res.Session))
	}
	if res.Usage == nil || res.Usage.InputTokens != 10 {
		t.Fatalf("usage = %#v, want the answer counted once", res.Usage)
	}
	if delivered := q.TakeDelivered(); len(delivered) != 0 {
		t.Fatalf("a retracted steer was rendered as sent: %#v", delivered)
	}
	if rt.HasSteers() || q.Preview().Visible() {
		t.Fatal("the retracted steer is still queued")
	}
}

// answerThenRecallLLM begins answering the call that carries the steer — the
// response-start boundary — and only then tries to recall it.
type answerThenRecallLLM struct {
	queue      *InputQueue
	calls      int
	recalledOK bool
}

func (m *answerThenRecallLLM) Execute(ctx context.Context, _ []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	m.calls++
	if m.calls == 1 {
		msg := llm.AssistantMessage(nil, llm.ToolCall{ID: "call-read", Type: llm.ToolTypeFunction,
			Function: llm.FunctionCall{Name: "read_file", Arguments: `{"file_path":"server.go"}`}})
		return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
	}
	if sink := llm.StreamSinkFrom(ctx); sink != nil && sink.OnResponseStarted != nil {
		sink.OnResponseStarted()
	}
	_, m.recalledOK = m.queue.Recall()
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("done")})
	return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
}

// Once the model has begun answering a steer it has left the queue and can no
// longer be recalled: the user sees it as a sent message instead.
func TestSteerCannotBeRecalledOnceTheModelAnswersIt(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "read_file", ReadOnly: true, ConcurrencySafe: true})
	q := NewInputQueue()
	rt := NewTurnInputRuntime()
	q.Attach(rt)
	q.Steer(Input{Text: "too late", Parts: []llm.ContentPart{llm.Text("too late")}})
	inner := &answerThenRecallLLM{queue: q}
	ctx := llm.WithStreamSink(WithTurnInputRuntime(context.Background(), rt), &llm.StreamSink{})
	if _, err := wrapToolOrchestrationLLM(inner, st).Execute(ctx,
		[]llm.Message{llm.UserMessage(llm.Text("go"))}, []*llm.Tool{readFileTestTool(t)}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if inner.recalledOK {
		t.Fatal("a steer the model is answering was handed back for editing")
	}
	if delivered := q.TakeDelivered(); len(delivered) != 1 || delivered[0].Text != "too late" {
		t.Fatalf("delivered = %#v, want the answered steer", delivered)
	}
}

// recallThenLeakLLM recalls the in-flight steer, then behaves like a provider
// whose first output event raced the abort: it fires the response-start
// boundary and a delta, and even returns a complete answer. None of that may
// reach the surface or the session.
type recallThenLeakLLM struct {
	queue *InputQueue
	calls int
}

func (m *recallThenLeakLLM) Execute(ctx context.Context, _ []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	m.calls++
	sink := llm.StreamSinkFrom(ctx)
	switch m.calls {
	case 1:
		msg := llm.AssistantMessage(nil, llm.ToolCall{ID: "call-read", Type: llm.ToolTypeFunction,
			Function: llm.FunctionCall{Name: "read_file", Arguments: `{"file_path":"server.go"}`}})
		return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
	case 2:
		if _, ok := m.queue.Recall(); !ok {
			return nil, errors.New("recall refused a steer that is still in the queue")
		}
		sink.OnResponseStarted()
		sink.OnDelta("answer to the retracted steer")
		msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("answer to the retracted steer")})
		return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
	default:
		sink.OnResponseStarted()
		sink.OnDelta("done")
		msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("done")})
		return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
	}
}

func TestAbandonedCallOutputNeverReachesTheSurface(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "read_file", ReadOnly: true, ConcurrencySafe: true})
	q := NewInputQueue()
	rt := NewTurnInputRuntime()
	q.Attach(rt)
	q.Steer(Input{Text: "retract me", Parts: []llm.ContentPart{llm.Text("retract me")}})
	var events []string
	ctx := llm.WithStreamSink(WithTurnInputRuntime(context.Background(), rt), &llm.StreamSink{
		OnResponseStarted: func() { events = append(events, "started") },
		OnDelta:           func(text string) { events = append(events, text) },
	})
	inner := &recallThenLeakLLM{queue: q}
	res, err := wrapToolOrchestrationLLM(inner, st).Execute(ctx,
		[]llm.Message{llm.UserMessage(llm.Text("go"))}, []*llm.Tool{readFileTestTool(t)})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got, want := strings.Join(events, ","), "started,done"; got != want {
		t.Fatalf("surface saw %q, want %q: the abandoned call's output leaked", got, want)
	}
	if inner.calls != 3 || res == nil || res.Message == nil || res.Message.TextContent() != "done" {
		t.Fatalf("calls=%d result=%#v, want the turn finished by a fresh call", inner.calls, res)
	}
	for _, msg := range res.Session {
		if strings.Contains(msg.TextContent(), "retract") {
			t.Fatalf("the abandoned exchange reached the session: %v", roles(res.Session))
		}
	}
}

// discardDuringCallLLM answers the first sampling with a tool call. On the
// second — the one carrying the queued steer — the user leaves the
// conversation, which discards its queue, and the call waits for the abort
// that triggers. Every later sampling finishes the turn.
type discardDuringCallLLM struct {
	queue   *InputQueue
	dropped int
	calls   [][]llm.Message
}

func (m *discardDuringCallLLM) Execute(ctx context.Context, messages []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	m.calls = append(m.calls, append([]llm.Message(nil), messages...))
	switch len(m.calls) {
	case 1:
		msg := llm.AssistantMessage(nil, llm.ToolCall{ID: "call-read", Type: llm.ToolTypeFunction,
			Function: llm.FunctionCall{Name: "read_file", Arguments: `{"file_path":"server.go"}`}})
		return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
	case 2:
		m.dropped = m.queue.Discard()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Second):
			return nil, errors.New("discarding an in-flight steer did not abort the call carrying it")
		}
	default:
		msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("done")})
		return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
	}
}

// Leaving a conversation discards its queue. A steer already taken for a model
// call that has not begun answering must go with it — otherwise the outgoing
// run hands it to the model with nothing left to render it into the transcript.
func TestDiscardRetractsTheInFlightSteer(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "read_file", ReadOnly: true, ConcurrencySafe: true})
	q := NewInputQueue()
	rt := NewTurnInputRuntime()
	q.Attach(rt)
	q.Steer(Input{Text: "left behind", Parts: []llm.ContentPart{llm.Text("left behind")}})

	inner := &discardDuringCallLLM{queue: q}
	res, err := wrapToolOrchestrationLLM(inner, st).Execute(
		WithTurnInputRuntime(context.Background(), rt),
		[]llm.Message{llm.UserMessage(llm.Text("go"))},
		[]*llm.Tool{readFileTestTool(t)},
	)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if inner.dropped != 1 {
		t.Fatalf("dropped = %d, want 1: the in-flight steer was never answered", inner.dropped)
	}
	if len(inner.calls) != 3 {
		t.Fatalf("model calls=%d want 3 (tool call, abandoned call, re-sent call)", len(inner.calls))
	}
	if third := inner.calls[2]; third[len(third)-1].Role != llm.RoleTool {
		t.Fatalf("re-sent call must end at the tool result, got %v", roles(third))
	}
	if res == nil || res.Message == nil || res.Message.TextContent() != "done" {
		t.Fatalf("unexpected result: %#v", res)
	}
	for _, msg := range res.Session {
		if msg.TextContent() == "left behind" {
			t.Fatal("the discarded steer reached the persisted session")
		}
	}
	if delivered := q.TakeDelivered(); len(delivered) != 0 {
		t.Fatalf("a discarded steer was rendered as sent: %#v", delivered)
	}
}

type parallelReadFileBatchLLM struct {
	calls int
}

type requestPermissionsThenDoneLLM struct {
	calls int
}

func (m *requestPermissionsThenDoneLLM) Execute(context.Context, []llm.Message, []*llm.Tool) (*llm.Result, error) {
	m.calls++
	if m.calls == 1 {
		msg := llm.AssistantMessage(nil, llm.ToolCall{
			ID:   "call-permissions",
			Type: llm.ToolTypeFunction,
			Function: llm.FunctionCall{
				Name:      "request_permissions",
				Arguments: `{}`,
			},
		})
		return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
	}
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("done")})
	return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
}

func TestToolOrchestrationSurfacesRequestPermissionsLifecycle(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "request_permissions", ReadOnly: true})
	tool, err := llm.NewTool("request_permissions", "request", func(ctx context.Context, _ *struct{}) (string, error) {
		toolpkg.CaptureToolOutput(ctx, map[string]any{"permissions": map[string]any{}, "scope": "turn"})
		return `{"permissions":{},"scope":"turn"}`, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var events []toolpkg.StepEvent
	st.SetStepHook(func(_ context.Context, evt toolpkg.StepEvent) {
		if evt.ToolName == "request_permissions" {
			events = append(events, evt)
		}
	})
	res, err := wrapToolOrchestrationLLM(&requestPermissionsThenDoneLLM{}, st).Execute(
		context.Background(), []llm.Message{llm.UserMessage(llm.Text("go"))}, []*llm.Tool{tool},
	)
	if err != nil || res == nil || res.Message == nil || res.Message.TextContent() != "done" {
		t.Fatalf("tool result did not continue to model: res=%#v err=%v", res, err)
	}
	if len(events) != 2 {
		t.Fatalf("lifecycle events=%d want 2", len(events))
	}
	for _, evt := range events {
		if evt.SuppressUI {
			t.Fatalf("request_permissions lifecycle must stay user-visible: %+v", evt)
		}
	}
}

func (m *parallelReadFileBatchLLM) Execute(context.Context, []llm.Message, []*llm.Tool) (*llm.Result, error) {
	m.calls++
	if m.calls == 1 {
		msg := llm.AssistantMessage(nil,
			llm.ToolCall{ID: "call-1", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "read_file", Arguments: `{"file_path":"server.go"}`}},
			llm.ToolCall{ID: "call-2", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "read_file", Arguments: `{"file_path":"load.go"}`}},
			llm.ToolCall{ID: "call-3", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "read_file", Arguments: `{"file_path":"status.go"}`}},
		)
		return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
	}
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("done")})
	return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
}

func TestToolOrchestrationEmitsGroupedLifecycleForSameParallelTool(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "read_file", ReadOnly: true, ConcurrencySafe: true})

	var events []toolpkg.StepEvent
	var mu sync.Mutex
	st.SetStepHook(func(_ context.Context, evt toolpkg.StepEvent) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, evt)
	})

	type readInput struct {
		FilePath string `json:"file_path"`
	}
	readTool, err := llm.NewTool("read_file", "read", func(_ context.Context, in *readInput) (string, error) {
		return in.FilePath, nil
	})
	if err != nil {
		t.Fatalf("read_file tool: %v", err)
	}

	_, err = wrapToolOrchestrationLLM(&parallelReadFileBatchLLM{}, st).Execute(
		context.Background(),
		[]llm.Message{llm.UserMessage(llm.Text("go"))},
		[]*llm.Tool{readTool},
	)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	var startedSummaries []toolpkg.StepEvent
	var completedSummaries []toolpkg.StepEvent
	var readCompleted []toolpkg.StepEvent
	started := 0
	for _, evt := range events {
		switch {
		case evt.Kind == toolpkg.StepKindToolStarted && evt.ToolName == "read_file":
			started++
		case evt.Kind == toolpkg.StepKindToolCompleted && evt.ToolName == "read_file":
			readCompleted = append(readCompleted, evt)
		case evt.Kind == toolpkg.StepKindToolParallelStarted:
			startedSummaries = append(startedSummaries, evt)
		case evt.Kind == toolpkg.StepKindToolParallelCompleted:
			completedSummaries = append(completedSummaries, evt)
		}
	}
	if len(startedSummaries) != 1 || len(completedSummaries) != 1 {
		t.Fatalf("batch lifecycle started=%d completed=%d want 1 each; events=%+v", len(startedSummaries), len(completedSummaries), events)
	}
	if strings.TrimSpace(startedSummaries[0].StepID) == "" || completedSummaries[0].StepID != startedSummaries[0].StepID {
		t.Fatalf("batch StepID mismatch started=%q completed=%q", startedSummaries[0].StepID, completedSummaries[0].StepID)
	}
	if got := startedSummaries[0].ToolName; got != "read_file" {
		t.Fatalf("batch summary tool=%q want read_file", got)
	}
	if got := startedSummaries[0].Output["summary"]; got != "running read server.go, load.go, status.go" {
		t.Fatalf("batch summary=%v", got)
	}
	if completedSummaries[0].Duration <= 0 {
		t.Fatalf("completed batch missing aggregated executor duration: %+v", completedSummaries[0])
	}
	if started != 3 || len(readCompleted) != 3 {
		t.Fatalf("per-tool events should still be emitted for audit; started=%d completed=%d", started, len(readCompleted))
	}
	for _, evt := range readCompleted {
		if evt.SuppressUI {
			t.Fatalf("read_file completed events must be visible; got %+v", evt)
		}
	}
}

type endlessToolLLM struct{}

func (endlessToolLLM) Execute(context.Context, []llm.Message, []*llm.Tool) (*llm.Result, error) {
	msg := llm.AssistantMessage(nil,
		llm.ToolCall{ID: "call-read", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "read_one", Arguments: `{}`}},
	)
	return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
}

type singleWriteToolLLM struct{}

func (singleWriteToolLLM) Execute(context.Context, []llm.Message, []*llm.Tool) (*llm.Result, error) {
	msg := llm.AssistantMessage(nil,
		llm.ToolCall{ID: "call-write", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "write_one", Arguments: `{}`}},
	)
	return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
}

func TestToolOrchestrationPropagatesRequiresActionError(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "write_one", Destructive: true})
	tool, err := llm.NewTool("write_one", "write", func(context.Context, *struct{}) (string, error) {
		return "", &toolpkg.RequiresActionError{
			ActionID:   "act-1",
			ActionKind: "write_one",
			ToolName:   "write_one",
			ToolInput:  map[string]any{"file_path": "a.txt"},
		}
	})
	if err != nil {
		t.Fatalf("tool: %v", err)
	}
	res, err := wrapToolOrchestrationLLM(singleWriteToolLLM{}, st).Execute(
		context.Background(),
		[]llm.Message{llm.UserMessage(llm.Text("go"))},
		[]*llm.Tool{tool},
	)
	if res != nil {
		t.Fatalf("expected nil result, got %#v", res)
	}
	var req *toolpkg.RequiresActionError
	if !errors.As(err, &req) {
		t.Fatalf("expected RequiresActionError, got %T %v", err, err)
	}
	if req.ActionID != "act-1" || req.ToolName != "write_one" {
		t.Fatalf("unexpected requires action payload: %+v", req)
	}
}

type emptyFinalAfterToolLLM struct {
	calls int
}

func (m *emptyFinalAfterToolLLM) Execute(context.Context, []llm.Message, []*llm.Tool) (*llm.Result, error) {
	m.calls++
	if m.calls == 1 {
		msg := llm.AssistantMessage(nil,
			llm.ToolCall{ID: "call-probe", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "probe", Arguments: `{}`}},
		)
		return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
	}
	msg := llm.AssistantMessage(nil)
	return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
}

func TestToolOrchestrationRejectsEmptyFinalAssistantAfterToolUse(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "probe", ReadOnly: true, ConcurrencySafe: true})
	// Any registered tool works here; the assertion is about the orchestrator
	// rejecting an empty final assistant message after a tool call.
	tool, err := llm.NewTool("probe", "probe", func(context.Context, *struct{}) (string, error) {
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("tool: %v", err)
	}
	inner := &emptyFinalAfterToolLLM{}
	res, err := wrapToolOrchestrationLLM(inner, st).Execute(
		context.Background(),
		[]llm.Message{llm.UserMessage(llm.Text("find skill"))},
		[]*llm.Tool{tool},
	)
	if err == nil {
		t.Fatalf("expected empty-final-response error, got nil result=%#v", res)
	}
	if !strings.Contains(err.Error(), "empty assistant response") {
		t.Fatalf("unexpected error: %v", err)
	}
}

type finalAfterReadFileToolLLM struct {
	calls int
	path  string
}

func (m *finalAfterReadFileToolLLM) Execute(context.Context, []llm.Message, []*llm.Tool) (*llm.Result, error) {
	m.calls++
	if m.calls == 1 {
		msg := llm.AssistantMessage(nil,
			llm.ToolCall{
				ID:   "call-read-file",
				Type: llm.ToolTypeFunction,
				Function: llm.FunctionCall{
					Name:      "read_file",
					Arguments: `{"file_path":"` + m.path + `"}`,
				},
			},
		)
		return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
	}
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("done")})
	return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
}

func TestToolOrchestrationEmitsSingleToolStepPairForSelfReportingTool(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "hello.go")
	if err := os.WriteFile(filePath, []byte("package hello\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	st := toolpkg.NewState(root)
	st.RegisterToolMeta(event.ToolMeta{Name: "read_file", ReadOnly: true, ConcurrencySafe: true})
	readTool, err := toolpkg.NewFileReadTool(st)
	if err != nil {
		t.Fatalf("read tool: %v", err)
	}
	var (
		mu        sync.Mutex
		started   []toolpkg.StepEvent
		completed []toolpkg.StepEvent
	)
	st.SetStepHook(func(_ context.Context, evt toolpkg.StepEvent) {
		mu.Lock()
		defer mu.Unlock()
		switch evt.Kind {
		case event.RunEventToolStarted:
			if evt.ToolName == "read_file" {
				started = append(started, evt)
			}
		case event.RunEventToolCompleted:
			if evt.ToolName == "read_file" {
				completed = append(completed, evt)
			}
		}
	})
	inner := &finalAfterReadFileToolLLM{path: filePath}
	_, err = wrapToolOrchestrationLLM(inner, st).Execute(
		context.Background(),
		[]llm.Message{llm.UserMessage(llm.Text("go"))},
		[]*llm.Tool{readTool},
	)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(started) != 1 {
		t.Fatalf("tool started events=%d want 1 (%+v)", len(started), started)
	}
	if len(completed) != 1 {
		t.Fatalf("tool completed events=%d want 1 (%+v)", len(completed), completed)
	}
	if completed[0].Output == nil || completed[0].Output["preview_text"] == nil {
		t.Fatalf("expected rich tool output, got %+v", completed[0])
	}
	if completed[0].Output["result_lines"] != 1 {
		t.Fatalf("expected result_lines=1, got %+v", completed[0].Output)
	}
}

func TestToolOrchestrationUsesCapturedToolCompletionPayload(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "capture_one", ReadOnly: true, ConcurrencySafe: true})
	var completed []toolpkg.StepEvent
	st.SetStepHook(func(_ context.Context, evt toolpkg.StepEvent) {
		if evt.Kind == event.RunEventToolCompleted && evt.ToolName == "capture_one" {
			completed = append(completed, evt)
		}
	})
	tool, err := llm.NewTool("capture_one", "capture", func(ctx context.Context, _ *struct{}) (string, error) {
		toolpkg.CaptureToolOutput(ctx, map[string]any{
			"summary": "captured",
			"count":   2,
		})
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("tool: %v", err)
	}
	inner := &finalAfterSingleToolLLM{name: "capture_one"}
	_, _ = wrapToolOrchestrationLLM(inner, st).Execute(
		context.Background(),
		[]llm.Message{llm.UserMessage(llm.Text("go"))},
		[]*llm.Tool{tool},
	)
	if len(completed) != 1 {
		t.Fatalf("completed events=%d want 1", len(completed))
	}
	if got := completed[0].Output["summary"]; got != "captured" {
		t.Fatalf("unexpected captured output: %+v", completed[0].Output)
	}
	if got := completed[0].Output["count"]; got != 2 {
		t.Fatalf("unexpected captured output: %+v", completed[0].Output)
	}
}

func TestToolOrchestrationCarriesLiveDiffDisplayIntoToolResult(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "edit_file", ReadOnly: false})
	tool, err := llm.NewTool("edit_file", "edit", func(ctx context.Context, _ *struct{}) (string, error) {
		toolpkg.CaptureToolOutput(ctx, map[string]any{
			"status": "ok",
			"turn_diff": map[string]any{
				"path":         "internal/demo.go",
				"added":        1,
				"deleted":      1,
				"unified_diff": "-old\n+new\n",
			},
		})
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("tool: %v", err)
	}
	inner := &finalAfterSingleToolLLM{name: "edit_file"}
	res, err := wrapToolOrchestrationLLM(inner, st).Execute(
		context.Background(),
		[]llm.Message{llm.UserMessage(llm.Text("edit"))},
		[]*llm.Tool{tool},
	)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var display *llm.ToolDisplayState
	for i := range res.Session {
		if res.Session[i].Role == llm.RoleTool {
			display = res.Session[i].ToolDisplay
			break
		}
	}
	if display == nil {
		t.Fatal("tool result missing persisted live display")
	}
	for _, want := range []string{"turn diff:", "internal/demo.go", "-old", "+new"} {
		if !strings.Contains(display.Body, want) {
			t.Fatalf("display body missing %q:\n%s", want, display.Body)
		}
	}
	if strings.TrimSpace(display.ToolMetaJSON) == "" || strings.TrimSpace(display.Summary) == "" {
		t.Fatalf("incomplete display metadata: %+v", display)
	}
}

func TestToolOrchestrationPreservesStructuredMapResultWithoutExplicitCapture(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "structured_one", ReadOnly: true, ConcurrencySafe: true})
	var completed []toolpkg.StepEvent
	st.SetStepHook(func(_ context.Context, evt toolpkg.StepEvent) {
		if evt.Kind == event.RunEventToolCompleted && evt.ToolName == "structured_one" {
			completed = append(completed, evt)
		}
	})
	tool, err := llm.NewTool("structured_one", "structured", func(context.Context, *struct{}) (map[string]any, error) {
		return map[string]any{
			"action":  "read",
			"content": "note one\nnote two",
		}, nil
	})
	if err != nil {
		t.Fatalf("tool: %v", err)
	}
	inner := &finalAfterSingleToolLLM{name: "structured_one"}
	_, _ = wrapToolOrchestrationLLM(inner, st).Execute(
		context.Background(),
		[]llm.Message{llm.UserMessage(llm.Text("go"))},
		[]*llm.Tool{tool},
	)
	if len(completed) != 1 {
		t.Fatalf("completed events=%d want 1", len(completed))
	}
	if got := completed[0].Output["action"]; got != "read" {
		t.Fatalf("unexpected structured output: %+v", completed[0].Output)
	}
	if got := completed[0].Output["content"]; got != "note one\nnote two" {
		t.Fatalf("unexpected structured output: %+v", completed[0].Output)
	}
}

func TestToolOrchestrationMergesGovernedFullPathIntoCapturedToolCompletionPayload(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "capture_large", ReadOnly: true, ConcurrencySafe: true})
	var completed []toolpkg.StepEvent
	st.SetStepHook(func(_ context.Context, evt toolpkg.StepEvent) {
		if evt.Kind == event.RunEventToolCompleted && evt.ToolName == "capture_large" {
			completed = append(completed, evt)
		}
	})
	tool, err := llm.NewTool("capture_large", "capture", func(ctx context.Context, _ *struct{}) (string, error) {
		toolpkg.CaptureToolOutput(ctx, map[string]any{
			"summary": "captured",
		})
		return strings.Repeat("x\n", toolpkg.SpillThresholdBytes), nil
	})
	if err != nil {
		t.Fatalf("tool: %v", err)
	}
	inner := &finalAfterSingleToolLLM{name: "capture_large"}
	_, _ = wrapToolOrchestrationLLM(inner, st).Execute(
		toolpkg.WithRunID(context.Background(), "run-1"),
		[]llm.Message{llm.UserMessage(llm.Text("go"))},
		[]*llm.Tool{tool},
	)
	if len(completed) != 1 {
		t.Fatalf("completed events=%d want 1", len(completed))
	}
	if got := completed[0].Output["summary"]; got != "captured" {
		t.Fatalf("unexpected captured output: %+v", completed[0].Output)
	}
	fullPath, _ := completed[0].Output["full_path"].(string)
	if strings.TrimSpace(fullPath) == "" {
		t.Fatalf("expected governed full_path, got %+v", completed[0].Output)
	}
	if omitted, ok := completed[0].Output["omitted_bytes"].(int); !ok || omitted <= 0 {
		t.Fatalf("expected omitted_bytes metadata, got %+v", completed[0].Output)
	}
	spills := st.ToolResultSpills("", "run-1")
	if len(spills) != 1 {
		t.Fatalf("tool result spills=%d want 1: %+v", len(spills), spills)
	}
	if spills[0].ToolName != "capture_large" || spills[0].CallID != "call-single" || spills[0].Path != fullPath {
		t.Fatalf("unexpected spill metadata: %+v fullPath=%q", spills[0], fullPath)
	}
	if spills[0].OriginalBytes <= 0 || spills[0].OmittedBytes <= 0 || spills[0].TotalLines <= 0 {
		t.Fatalf("expected spill size metadata: %+v", spills[0])
	}
}

type finalAfterSingleToolLLM struct {
	name  string
	calls int
}

func (m *finalAfterSingleToolLLM) Execute(context.Context, []llm.Message, []*llm.Tool) (*llm.Result, error) {
	m.calls++
	if m.calls == 1 {
		msg := llm.AssistantMessage(nil,
			llm.ToolCall{ID: "call-single", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: m.name, Arguments: `{}`}},
		)
		return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
	}
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("done")})
	return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
}

func TestToolOrchestrationEmitsStartedBeforeToolExecution(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "read_one", ReadOnly: true, ConcurrencySafe: true})
	startedSeen := make(chan struct{}, 1)
	enteredHandler := make(chan bool, 1)
	st.SetStepHook(func(_ context.Context, evt toolpkg.StepEvent) {
		if evt.Kind == event.RunEventToolStarted && evt.ToolName == "read_one" {
			select {
			case startedSeen <- struct{}{}:
			default:
			}
		}
	})
	tool, err := llm.NewTool("read_one", "read", func(context.Context, *struct{}) (string, error) {
		select {
		case <-startedSeen:
			enteredHandler <- true
		default:
			enteredHandler <- false
		}
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("tool: %v", err)
	}
	inner := &finalAfterSingleToolLLM{name: "read_one"}
	_, _ = wrapToolOrchestrationLLM(inner, st).Execute(
		context.Background(),
		[]llm.Message{llm.UserMessage(llm.Text("go"))},
		[]*llm.Tool{tool},
	)
	select {
	case ok := <-enteredHandler:
		if !ok {
			t.Fatal("tool handler entered before StepStarted was emitted")
		}
	case <-time.After(time.Second):
		t.Fatal("tool handler did not run")
	}
}

func TestToolOrchestrationPropagatesRequiresActionIntoCompletedStep(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "write_one", Destructive: true})
	var completed []toolpkg.StepEvent
	st.SetStepHook(func(_ context.Context, evt toolpkg.StepEvent) {
		if evt.Kind == event.RunEventToolCompleted && evt.ToolName == "write_one" {
			completed = append(completed, evt)
		}
	})
	tool, err := llm.NewTool("write_one", "write", func(context.Context, *struct{}) (string, error) {
		return "", &toolpkg.RequiresActionError{
			ActionID:   "act-1",
			ActionKind: "write_one",
			ToolName:   "write_one",
			ToolInput:  map[string]any{"file_path": "a.txt"},
		}
	})
	if err != nil {
		t.Fatalf("tool: %v", err)
	}
	_, _ = wrapToolOrchestrationLLM(singleWriteToolLLM{}, st).Execute(
		context.Background(),
		[]llm.Message{llm.UserMessage(llm.Text("go"))},
		[]*llm.Tool{tool},
	)
	if len(completed) != 1 {
		t.Fatalf("completed events=%d want 1", len(completed))
	}
	if completed[0].ActionID != "act-1" || completed[0].ActionKind != "write_one" {
		t.Fatalf("unexpected action metadata: %+v", completed[0])
	}
	if completed[0].Output == nil || completed[0].Output["requires_action"] != true {
		t.Fatalf("expected requires_action output, got %+v", completed[0].Output)
	}
}

func TestToolOrchestrationEmitsCompletedAttemptBeforeApprovalWait(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "write_one", Destructive: true})
	var completed []toolpkg.StepEvent
	st.SetStepHook(func(_ context.Context, evt toolpkg.StepEvent) {
		if evt.Kind == event.RunEventToolCompleted && evt.ToolName == "write_one" {
			completed = append(completed, evt)
		}
	})
	tool, err := llm.NewTool("write_one", "write", func(ctx context.Context, _ *struct{}) (string, error) {
		toolpkg.CaptureToolAttempt(ctx, toolpkg.ToolCompletionPayload{
			Output: map[string]any{
				"stderr":    "listen tcp 127.0.0.1:0: bind: operation not permitted",
				"exit_code": 1,
			},
		})
		toolpkg.CaptureToolRequiresAction(ctx, "act-1", "write_one", map[string]any{"requires_action": true})
		return "", &toolpkg.RequiresActionError{
			ActionID: "act-1", ActionKind: "write_one", ToolName: "write_one",
			ToolInput: map[string]any{"file_path": "a.txt"},
		}
	})
	if err != nil {
		t.Fatalf("tool: %v", err)
	}
	_, _ = wrapToolOrchestrationLLM(singleWriteToolLLM{}, st).Execute(
		context.Background(),
		[]llm.Message{llm.UserMessage(llm.Text("go"))},
		[]*llm.Tool{tool},
	)
	if len(completed) != 2 {
		t.Fatalf("completed events=%d want attempt + approval wait: %+v", len(completed), completed)
	}
	if !completed[0].RetainAsHistory || completed[0].Output["exit_code"] != 1 || completed[0].ActionID != "" {
		t.Fatalf("unexpected retained attempt: %+v", completed[0])
	}
	if completed[1].RetainAsHistory || completed[1].ActionID != "act-1" || completed[1].Output["requires_action"] != true {
		t.Fatalf("unexpected approval-wait completion: %+v", completed[1])
	}
	if completed[0].StepID == "" || completed[0].StepID != completed[1].StepID {
		t.Fatalf("attempt and retry must share the logical StepID: %+v", completed)
	}
}

func TestCoordinatedExecutionPreservesApprovalGate(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "shell", Destructive: true})
	var completed []toolpkg.StepEvent
	st.SetStepHook(func(_ context.Context, evt toolpkg.StepEvent) {
		if evt.Kind == event.RunEventToolCompleted && evt.ToolName == "shell" {
			completed = append(completed, evt)
		}
	})
	tool, err := llm.NewTool("shell", "approval gated shell", func(context.Context, *struct{}) (string, error) {
		return "", &toolpkg.RequiresActionError{
			ActionID:   "act-shell",
			ActionKind: "shell",
			ToolName:   "shell",
			ToolInput:  map[string]any{},
		}
	})
	if err != nil {
		t.Fatalf("shell tool: %v", err)
	}
	ctx := context.Background()
	_, err = wrapToolOrchestrationLLM(&recordingFinalAfterSingleToolLLM{name: "shell"}, st).Execute(
		ctx,
		[]llm.Message{llm.UserMessage(llm.Text("run a command"))},
		[]*llm.Tool{tool},
	)
	var rae *toolpkg.RequiresActionError
	if !errors.As(err, &rae) {
		t.Fatalf("shell should still require action, got %T %v", err, err)
	}
	if rae.ActionID != "act-shell" || rae.ToolName != "shell" {
		t.Fatalf("unexpected requires-action payload: %+v", rae)
	}
	if len(completed) != 1 {
		t.Fatalf("completed events=%d want 1", len(completed))
	}
	if completed[0].ActionID != "act-shell" || completed[0].ActionKind != "shell" {
		t.Fatalf("unexpected requires-action step event: %+v", completed[0])
	}
	if completed[0].Output == nil || completed[0].Output["requires_action"] != true {
		t.Fatalf("expected requires_action output, got %+v", completed[0].Output)
	}
}

type filePathRetryLLM struct {
	calls []llm.ToolCall
	seen  [][]llm.Message
}

func (m *filePathRetryLLM) Execute(_ context.Context, messages []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	index := len(m.seen)
	m.seen = append(m.seen, append([]llm.Message(nil), messages...))
	message := llm.AssistantMessage([]llm.ContentPart{llm.Text("File updated successfully.")})
	if index < len(m.calls) {
		message = llm.AssistantMessage(nil, m.calls[index])
	}
	return &llm.Result{Message: &message}, nil
}

func TestToolOrchestrationRetriesFileMutationAfterMissingPath(t *testing.T) {
	for _, name := range []string{"edit_file", "write_file"} {
		t.Run(name, func(t *testing.T) {
			project := t.TempDir()
			target := filepath.Join(project, "file.txt")
			if err := os.WriteFile(target, []byte("before"), 0o600); err != nil {
				t.Fatal(err)
			}
			runner, st := outsideWorkspaceRunner(t, project, safety.ApprovalOnRequest)
			readTool, err := toolpkg.NewFileReadTool(st)
			if err != nil {
				t.Fatal(err)
			}
			var mutationTool *llm.Tool
			input := map[string]any{"content": "after"}
			if name == "edit_file" {
				mutationTool, err = toolpkg.NewFileEditTool(st)
				input = map[string]any{"old_string": "before", "new_string": "after"}
			} else {
				mutationTool, err = toolpkg.NewFileWriteTool(st, nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, fileTool := range []*llm.Tool{readTool, mutationTool} {
				fileTool.Use(runner.newToolPermissionMiddleware())
			}
			readInput, _ := json.Marshal(map[string]any{"file_path": target})
			invalidInput, _ := json.Marshal(input)
			input["file_path"] = target
			correctedInput, _ := json.Marshal(input)
			inner := &filePathRetryLLM{calls: []llm.ToolCall{
				{ID: "read", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "read_file", Arguments: string(readInput)}},
				{ID: "missing-path", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: name, Arguments: string(invalidInput)}},
				{ID: "corrected-path", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: name, Arguments: string(correctedInput)}},
			}}
			ctx := llm.WithAgentSessionID(context.Background(), "file-path-retry")
			result, err := wrapToolOrchestrationLLM(inner, st).Execute(ctx,
				[]llm.Message{llm.UserMessage(llm.Text("Change before to after in " + target))},
				[]*llm.Tool{readTool, mutationTool})
			if err != nil {
				t.Fatalf("recoverable input error interrupted the conversation: %v", err)
			}
			if len(inner.seen) != 4 {
				t.Fatalf("model calls=%d, want read, invalid mutation, retry, final reply", len(inner.seen))
			}
			feedback := inner.seen[2][len(inner.seen[2])-1]
			if feedback.Role != llm.RoleTool || feedback.ToolCallID != "missing-path" {
				t.Fatalf("error must reach the model as the failed call's tool result: %+v", feedback)
			}
			for _, text := range []string{name, "file_path is required", "Retry this tool call", "absolute or workspace-relative path", "keeping the other arguments unchanged"} {
				if !strings.Contains(feedback.TextContent(), text) {
					t.Fatalf("model feedback lacks %q: %s", text, feedback.TextContent())
				}
			}
			retried := inner.seen[3][len(inner.seen[3])-1]
			if retried.ToolCallID != "corrected-path" || retried.TextContent() != "ok" {
				t.Fatalf("corrected call did not succeed: %+v", retried)
			}
			if result.Message == nil || result.Message.TextContent() != "File updated successfully." {
				t.Fatalf("conversation did not reach the final reply: %+v", result)
			}
			content, err := os.ReadFile(target)
			if err != nil || string(content) != "after" {
				t.Fatalf("corrected call did not update the file: content=%q err=%v", content, err)
			}
			pending, err := runner.Actions.List(ctx, "main", state.ActionFilter{Status: string(state.ActionPending)}, 10)
			if err != nil || len(pending) != 0 {
				t.Fatalf("input correction must not require user approval: pending=%+v err=%v", pending, err)
			}
		})
	}
}

type singleHallucinatedSkillLLM struct {
	calls int
}

func (m *singleHallucinatedSkillLLM) Execute(context.Context, []llm.Message, []*llm.Tool) (*llm.Result, error) {
	m.calls++
	if m.calls == 1 {
		msg := llm.AssistantMessage(nil,
			llm.ToolCall{ID: "call-skill-1", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "caveman-commit", Arguments: `{}`}},
		)
		return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
	}
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("done")})
	return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
}

func TestToolOrchestrationKeepsRealToolRoutingWhenConcreteSkillToolsExist(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "caveman-commit", ReadOnly: true, ConcurrencySafe: true})
	st.RegisterToolMeta(event.ToolMeta{Name: "shell", ReadOnly: false})

	skillTool, err := llm.NewTool("caveman-commit", "commit skill", func(context.Context, *struct{}) (string, error) {
		return "skill-dispatched", nil
	})
	if err != nil {
		t.Fatalf("concrete skill: %v", err)
	}
	shellTool, err := llm.NewTool("shell", "shell", func(context.Context, *struct{}) (string, error) {
		return "diff", nil
	})
	if err != nil {
		t.Fatalf("shell tool: %v", err)
	}

	inner := &finalAfterSingleToolLLM{name: "shell"}
	_, err = wrapToolOrchestrationLLM(inner, st).Execute(
		toolpkg.WithRunID(context.Background(), "run-real-tool"),
		[]llm.Message{llm.UserMessage(llm.Text("diff"))},
		[]*llm.Tool{skillTool, shellTool},
	)
	if err != nil {
		t.Fatalf("Execute err=%v", err)
	}
}

func TestToolOrchestrationUnknownToolWithoutSkillStillErrors(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "real_tool", ReadOnly: true, ConcurrencySafe: true})

	realTool, err := llm.NewTool("real_tool", "real", func(context.Context, *struct{}) (string, error) {
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("tool: %v", err)
	}

	inner := &singleHallucinatedSkillLLM{}
	res, err := wrapToolOrchestrationLLM(inner, st).Execute(
		context.Background(),
		[]llm.Message{llm.UserMessage(llm.Text("commit"))},
		[]*llm.Tool{realTool},
	)
	if err != nil {
		t.Fatalf("Execute err=%v", err)
	}
	if res == nil || res.Message == nil {
		t.Fatalf("expected final assistant message, got %#v", res)
	}
	if inner.calls < 2 {
		t.Fatalf("expected orchestration to continue past unknown-tool error, got calls=%d", inner.calls)
	}
}

type approvalGatedSingleToolLLM struct {
	mu       sync.Mutex
	calls    int
	sessions [][]llm.Message
}

func (m *approvalGatedSingleToolLLM) Execute(_ context.Context, messages []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	m.sessions = append(m.sessions, append([]llm.Message(nil), messages...))
	switch m.calls {
	case 1:
		msg := llm.AssistantMessage(nil,
			llm.ToolCall{ID: "call-1", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "gated", Arguments: `{}`}},
		)
		return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
	default:
		msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("answer after approval")})
		return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
	}
}

type recordingFinalAfterSingleToolLLM struct {
	name     string
	calls    int
	sessions [][]llm.Message
}

func (m *recordingFinalAfterSingleToolLLM) Execute(_ context.Context, messages []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	cp := make([]llm.Message, len(messages))
	copy(cp, messages)
	m.sessions = append(m.sessions, cp)
	m.calls++
	if m.calls == 1 {
		msg := llm.AssistantMessage(nil,
			llm.ToolCall{ID: "call-1", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: m.name, Arguments: `{}`}},
		)
		return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
	}
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("done")})
	return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
}

func TestToolOrchestrationSnapshotsSessionOnRequiresAction(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "gated", Destructive: true})

	gated, err := llm.NewTool("gated", "approval-gated tool", func(ctx context.Context, _ *struct{}) (string, error) {
		if toolpkg.ApprovedActionIDFromContext(ctx) == "" {
			return "", &toolpkg.RequiresActionError{
				ActionID:   "act-1",
				ActionKind: "gated",
				ToolName:   "gated",
				ToolInput:  map[string]any{},
			}
		}
		return "tool-output", nil
	})
	if err != nil {
		t.Fatalf("tool: %v", err)
	}

	inner := &approvalGatedSingleToolLLM{}
	wrapped := wrapToolOrchestrationLLM(inner, st)

	// First call: RAE fires and session snapshot must be attached.
	_, err = wrapped.Execute(
		context.Background(),
		[]llm.Message{llm.UserMessage(llm.Text("hi"))},
		[]*llm.Tool{gated},
	)
	var rae *toolpkg.RequiresActionError
	if !errors.As(err, &rae) || rae == nil {
		t.Fatalf("expected RequiresActionError, got %T %v", err, err)
	}
	if len(rae.SessionSnapshot) == 0 {
		t.Fatalf("expected SessionSnapshot to be populated on RAE")
	}
	foundAssistant := false
	for _, msg := range rae.SessionSnapshot {
		if msg.Role == llm.RoleAssistant && len(msg.ToolCalls) > 0 {
			foundAssistant = true
			break
		}
	}
	if !foundAssistant {
		t.Fatalf("snapshot must include assistant tool_use, got %#v", rae.SessionSnapshot)
	}

	// Resume: inject approved action + resume state. Tool now runs for real,
	// loop continues, LLM emits final assistant text.
	ctx := toolpkg.WithApprovedActionID(context.Background(), rae.ActionID)
	ctx = toolpkg.WithToolApprovalResume(ctx, &toolpkg.ToolApprovalResumeState{Session: rae.SessionSnapshot})

	res, rerr := wrapped.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("ignored"))}, []*llm.Tool{gated})
	if rerr != nil {
		t.Fatalf("resume Execute err=%v", rerr)
	}
	if res == nil || res.Message == nil {
		t.Fatalf("expected non-nil result on resume")
	}
	if text := res.Message.TextContent(); text != "answer after approval" {
		t.Fatalf("unexpected final text: %q", text)
	}
	if inner.calls != 2 {
		t.Fatalf("expected exactly 1 LLM call after resume (skipping first one), total=%d", inner.calls)
	}
	resumedSession := inner.sessions[1]
	if len(resumedSession) == 0 {
		t.Fatalf("resumed LLM call session was empty")
	}
	tail := resumedSession[len(resumedSession)-1]
	if tail.Role != llm.RoleTool {
		t.Fatalf("resumed session tail must be tool result, got role=%s", tail.Role)
	}
}

func TestToolOrchestrationClearContextResumeUsesCurrentEnvironment(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "gated", Destructive: true})

	gated, err := llm.NewTool("gated", "approval-gated tool", func(ctx context.Context, _ *struct{}) (string, error) {
		if toolpkg.ApprovedActionIDFromContext(ctx) == "" {
			return "", &toolpkg.RequiresActionError{
				ActionID:   "act-1",
				ActionKind: "gated",
				ToolName:   "gated",
				ToolInput:  map[string]any{},
			}
		}
		return "tool-output", nil
	})
	if err != nil {
		t.Fatalf("tool: %v", err)
	}

	inner := &approvalGatedSingleToolLLM{}
	wrapped := wrapToolOrchestrationLLM(inner, st)
	oldEnvironment := promptCacheEnvironmentMessage("<project_root>/old/project</project_root>", false)
	_, err = wrapped.Execute(
		context.Background(),
		[]llm.Message{
			llm.SystemMessage("system"),
			llm.UserMessage(llm.Text("implement the plan")),
			oldEnvironment,
		},
		[]*llm.Tool{gated},
	)
	var rae *toolpkg.RequiresActionError
	if !errors.As(err, &rae) || rae == nil {
		t.Fatalf("expected RequiresActionError, got %T %v", err, err)
	}

	resumeState := &toolpkg.ToolApprovalResumeState{
		Session: turn.MinimalClearedResumeSnapshot(rae.SessionSnapshot, "gated"),
	}
	currentEnvironment := promptCacheEnvironmentMessage("<project_root>/current/project</project_root>\n<current_working_directory>/current/project</current_working_directory>", false)
	currentSession := []llm.Message{
		llm.SystemMessage("system"),
		oldEnvironment,
		currentEnvironment,
	}
	ctx := toolpkg.WithApprovedActionID(context.Background(), rae.ActionID)
	ctx = toolpkg.WithToolApprovalResume(ctx, resumeState)
	if _, err := wrapped.Execute(ctx, currentSession, []*llm.Tool{gated}); err != nil {
		t.Fatalf("resume Execute: %v", err)
	}

	if len(inner.sessions) != 2 {
		t.Fatalf("LLM calls=%d want 2", len(inner.sessions))
	}
	resumed := inner.sessions[1]
	environmentCount := 0
	environmentIdx := -1
	pendingIdx := -1
	resultIdx := -1
	for i, message := range resumed {
		text := message.TextContent()
		if strings.Contains(text, "/old/project") {
			t.Fatalf("resumed session retained stale environment: %#v", resumed)
		}
		if isPromptCacheEnvironmentMessage(message) {
			environmentCount++
			environmentIdx = i
			if text != currentEnvironment.TextContent() {
				t.Fatalf("environment=%q want %q", text, currentEnvironment.TextContent())
			}
		}
		if message.Role == llm.RoleAssistant && len(message.ToolCalls) > 0 {
			pendingIdx = i
		}
		if message.Role == llm.RoleTool && message.ToolCallID == "call-1" {
			resultIdx = i
		}
	}
	if environmentCount != 1 {
		t.Fatalf("environment message count=%d want 1: %#v", environmentCount, resumed)
	}
	if environmentIdx < 0 || pendingIdx < 0 || resultIdx < 0 || !(environmentIdx < pendingIdx && pendingIdx < resultIdx) {
		t.Fatalf("unexpected resume ordering: environment=%d pending=%d result=%d", environmentIdx, pendingIdx, resultIdx)
	}
}

// An ordinary approval resume replays the conversation it interrupted. Every
// message in that snapshot has already been sent at its exact index, and a
// provider serving an implicit prefix cache keeps serving only while those
// indexes hold — so the resume must not lift the environment message out of
// history and re-append it at the tail. Doing that forks the prefix at the
// index the message used to occupy, re-bills the whole conversation after it,
// and forks it straight back on the next ordinary turn, which rebuilds from
// the stored transcript where the message still sits at its original place.
func TestToolOrchestrationResumeReplaysHistoryVerbatim(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "gated", Destructive: true})

	gated, err := llm.NewTool("gated", "approval-gated tool", func(ctx context.Context, _ *struct{}) (string, error) {
		if toolpkg.ApprovedActionIDFromContext(ctx) == "" {
			return "", &toolpkg.RequiresActionError{
				ActionID:   "act-1",
				ActionKind: "gated",
				ToolName:   "gated",
				ToolInput:  map[string]any{},
			}
		}
		return "tool-output", nil
	})
	if err != nil {
		t.Fatalf("tool: %v", err)
	}

	inner := &approvalGatedSingleToolLLM{}
	wrapped := wrapToolOrchestrationLLM(inner, st)
	environment := promptCacheEnvironmentMessage("<project_root>/project</project_root>", false)
	session := []llm.Message{
		llm.SystemMessage("system"),
		llm.UserMessage(llm.Text("implement the plan")),
		environment,
		llm.AssistantMessage([]llm.ContentPart{llm.Text("working on it")}),
	}
	_, err = wrapped.Execute(context.Background(), session, []*llm.Tool{gated})
	var rae *toolpkg.RequiresActionError
	if !errors.As(err, &rae) || rae == nil {
		t.Fatalf("expected RequiresActionError, got %T %v", err, err)
	}

	resumeState := &toolpkg.ToolApprovalResumeState{Session: rae.SessionSnapshot}
	ctx := toolpkg.WithApprovedActionID(context.Background(), rae.ActionID)
	ctx = toolpkg.WithToolApprovalResume(ctx, resumeState)
	// The environment has not changed, so the rebuilt current session carries
	// the same message at the same place the snapshot has it.
	if _, err := wrapped.Execute(ctx, session, []*llm.Tool{gated}); err != nil {
		t.Fatalf("resume Execute: %v", err)
	}

	if len(inner.sessions) != 2 {
		t.Fatalf("LLM calls=%d want 2", len(inner.sessions))
	}
	resumed := inner.sessions[1]
	for i := range session {
		if i >= len(resumed) {
			t.Fatalf("resumed session truncated at %d: %#v", i, resumed)
		}
		if resumed[i].Role != session[i].Role || resumed[i].TextContent() != session[i].TextContent() {
			t.Fatalf("message %d changed on resume: %#v want %#v", i, resumed[i], session[i])
		}
	}
	environmentCount := 0
	for _, message := range resumed {
		if isPromptCacheEnvironmentMessage(message) {
			environmentCount++
		}
	}
	if environmentCount != 1 {
		t.Fatalf("environment message count=%d want 1: %#v", environmentCount, resumed)
	}
}

// TestToolOrchestrationResumeCapturesReplayResult verifies that when a resume
// replay executes an approved tool, the result is captured in the
// ReplayResultCapture so the caller can persist it to the session state.
// Without this, the replayed tool result lives only in the orchestration LLM's
// internal session and is discarded — leaving a dangling tool_calls row that
// RepairDanglingToolResults strips, causing the LLM to lose knowledge that the
// tool was called and approved (e.g. exit_plan_mode).
func TestToolOrchestrationResumeCapturesReplayResult(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "gated", Destructive: true})
	st.SetActionHook(func(context.Context, string, any) (string, bool, error) {
		return "act-1", true, nil
	})

	gated, err := llm.NewTool("gated", "gated tool", func(ctx context.Context, _ *struct{}) (string, error) {
		if toolpkg.ApprovedActionIDFromContext(ctx) == "" {
			return "", &toolpkg.RequiresActionError{
				ActionID:   "act-1",
				ActionKind: "gated",
				ToolName:   "gated",
				ToolInput:  map[string]any{},
			}
		}
		return "tool-output", nil
	})
	if err != nil {
		t.Fatalf("tool: %v", err)
	}

	inner := &approvalGatedSingleToolLLM{}
	wrapped := wrapToolOrchestrationLLM(inner, st)

	// First call: RAE fires.
	_, err = wrapped.Execute(
		context.Background(),
		[]llm.Message{llm.UserMessage(llm.Text("hi"))},
		[]*llm.Tool{gated},
	)
	var rae *toolpkg.RequiresActionError
	if !errors.As(err, &rae) || rae == nil {
		t.Fatalf("expected RequiresActionError, got %T %v", err, err)
	}

	// Resume with approved action + resume state + replay capture.
	capture := toolpkg.NewReplayResultCapture()
	ctx := toolpkg.WithApprovedActionID(context.Background(), rae.ActionID)
	ctx = toolpkg.WithToolApprovalResume(ctx, &toolpkg.ToolApprovalResumeState{Session: rae.SessionSnapshot})
	ctx = toolpkg.WithReplayResultCapture(ctx, capture)

	_, rerr := wrapped.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("ignored"))}, []*llm.Tool{gated})
	if rerr != nil {
		t.Fatalf("resume Execute err=%v", rerr)
	}

	entries := capture.Entries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 captured replay result, got %d", len(entries))
	}
	if entries[0].ToolName != "gated" {
		t.Fatalf("expected tool name 'gated', got %q", entries[0].ToolName)
	}
	if entries[0].Content != "tool-output" {
		t.Fatalf("expected content 'tool-output', got %q", entries[0].Content)
	}
	if entries[0].ToolCallID == "" {
		t.Fatalf("expected non-empty ToolCallID")
	}
	if !entries[0].Timing.Valid() || entries[0].Timing.Duration <= 0 {
		t.Fatalf("captured replay result missing executor timing: %#v", entries[0].Timing)
	}
	if entries[0].ToolDisplay == nil {
		t.Fatal("captured replay result missing live tool display metadata")
	}
}

// approvalGatedThenCancelLLM emits a single approval-gated tool call on its
// first invocation, then returns context.Canceled on the resume invocation
// (simulating the user pressing Esc immediately after the approved tool
// replays).
type approvalGatedThenCancelLLM struct {
	mu    sync.Mutex
	calls int
}

func (m *approvalGatedThenCancelLLM) Execute(_ context.Context, _ []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if m.calls == 1 {
		msg := llm.AssistantMessage(nil,
			llm.ToolCall{ID: "call-1", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "gated", Arguments: `{}`}},
		)
		return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
	}
	return nil, context.Canceled
}

// TestToolOrchestrationResumeCapturesReplayResultEvenWhenCancelled verifies
// that a replayed (approved) tool result is captured even when the resume run
// is cancelled (the inner LLM returns context.Canceled, simulating the user
// pressing Esc). replayPendingToolCalls runs before the first LLM iteration,
// so the capture is populated before the cancel surfaces. This is the
// precondition for runResumeApprovedTurn's cancel branch to persist the
// result; without it the LLM loses knowledge that the tool was called/approved
// (e.g. exit_plan_mode) and re-invokes it on the next turn.
func TestToolOrchestrationResumeCapturesReplayResultEvenWhenCancelled(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "gated", Destructive: true})
	st.SetActionHook(func(context.Context, string, any) (string, bool, error) {
		return "act-1", true, nil
	})

	gated, err := llm.NewTool("gated", "gated tool", func(ctx context.Context, _ *struct{}) (string, error) {
		if toolpkg.ApprovedActionIDFromContext(ctx) == "" {
			return "", &toolpkg.RequiresActionError{
				ActionID:   "act-1",
				ActionKind: "gated",
				ToolName:   "gated",
				ToolInput:  map[string]any{},
			}
		}
		return "tool-output", nil
	})
	if err != nil {
		t.Fatalf("tool: %v", err)
	}

	inner := &approvalGatedThenCancelLLM{}
	wrapped := wrapToolOrchestrationLLM(inner, st)

	// First call: RAE fires and the session snapshot is captured.
	_, err = wrapped.Execute(
		context.Background(),
		[]llm.Message{llm.UserMessage(llm.Text("hi"))},
		[]*llm.Tool{gated},
	)
	var rae *toolpkg.RequiresActionError
	if !errors.As(err, &rae) || rae == nil {
		t.Fatalf("expected RequiresActionError, got %T %v", err, err)
	}

	// Resume: approved action + resume state + replay capture. The inner LLM
	// returns context.Canceled on its first resume iteration, but only after
	// replayPendingToolCalls replayed the approved tool and captured its result.
	capture := toolpkg.NewReplayResultCapture()
	ctx := toolpkg.WithApprovedActionID(context.Background(), rae.ActionID)
	ctx = toolpkg.WithToolApprovalResume(ctx, &toolpkg.ToolApprovalResumeState{Session: rae.SessionSnapshot})
	ctx = toolpkg.WithReplayResultCapture(ctx, capture)

	_, rerr := wrapped.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("ignored"))}, []*llm.Tool{gated})
	if !errors.Is(rerr, context.Canceled) {
		t.Fatalf("expected context.Canceled from cancelled resume, got %v", rerr)
	}

	entries := capture.Entries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 captured replay result even after cancel, got %d", len(entries))
	}
	if entries[0].ToolName != "gated" {
		t.Fatalf("expected tool name 'gated', got %q", entries[0].ToolName)
	}
	if entries[0].Content != "tool-output" {
		t.Fatalf("expected content 'tool-output', got %q", entries[0].Content)
	}
}

type skillThenGatedApprovalLLM struct {
	mu       sync.Mutex
	calls    int
	sessions [][]llm.Message
}

func (m *skillThenGatedApprovalLLM) Execute(_ context.Context, messages []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	m.sessions = append(m.sessions, append([]llm.Message(nil), messages...))
	switch m.calls {
	case 1:
		msg := llm.AssistantMessage(nil,
			llm.ToolCall{ID: "call-review", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "review", Arguments: `{}`}},
			llm.ToolCall{ID: "call-gated", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "gated", Arguments: `{}`}},
		)
		return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
	default:
		msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("answer after approval")})
		return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
	}
}

func TestToolOrchestrationResumeDoesNotReplayCompletedSkillBeforeApproval(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "review", ReadOnly: true, ConcurrencySafe: true})
	st.RegisterToolMeta(event.ToolMeta{Name: "gated", Destructive: true})

	var skillRuns int
	skillTool, err := llm.NewTool("review", "review skill", func(context.Context, *struct{}) (string, error) {
		skillRuns++
		return "review skill instructions", nil
	})
	if err != nil {
		t.Fatalf("review skill: %v", err)
	}
	var gatedRuns int
	gated, err := llm.NewTool("gated", "approval-gated tool", func(ctx context.Context, _ *struct{}) (string, error) {
		gatedRuns++
		if toolpkg.ApprovedActionIDFromContext(ctx) == "" {
			return "", &toolpkg.RequiresActionError{
				ActionID:   "act-1",
				ActionKind: "gated",
				ToolName:   "gated",
				ToolInput:  map[string]any{},
			}
		}
		return "gated-output", nil
	})
	if err != nil {
		t.Fatalf("gated tool: %v", err)
	}

	inner := &skillThenGatedApprovalLLM{}
	wrapped := wrapToolOrchestrationLLM(inner, st)

	_, err = wrapped.Execute(
		context.Background(),
		[]llm.Message{llm.UserMessage(llm.Text("review"))},
		[]*llm.Tool{skillTool, gated},
	)
	var rae *toolpkg.RequiresActionError
	if !errors.As(err, &rae) || rae == nil {
		t.Fatalf("expected RequiresActionError, got %T %v", err, err)
	}
	if skillRuns != 1 || gatedRuns != 1 {
		t.Fatalf("first run counts skill=%d gated=%d, want 1/1", skillRuns, gatedRuns)
	}

	ctx := toolpkg.WithApprovedActionID(context.Background(), rae.ActionID)
	ctx = toolpkg.WithToolApprovalResume(ctx, &toolpkg.ToolApprovalResumeState{Session: rae.SessionSnapshot})
	res, rerr := wrapped.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("ignored"))}, []*llm.Tool{skillTool, gated})
	if rerr != nil {
		t.Fatalf("resume Execute err=%v", rerr)
	}
	if res == nil || res.Message == nil || res.Message.TextContent() != "answer after approval" {
		t.Fatalf("unexpected resume result: %#v", res)
	}
	if skillRuns != 1 {
		t.Fatalf("completed skill was replayed on approval resume; runs=%d want 1", skillRuns)
	}
	if gatedRuns != 2 {
		t.Fatalf("gated tool runs=%d want 2", gatedRuns)
	}
	if len(inner.sessions) < 2 {
		t.Fatalf("expected resumed LLM session to be captured")
	}
	var resultIDs []string
	for _, msg := range inner.sessions[1] {
		if msg.Role == llm.RoleTool {
			resultIDs = append(resultIDs, msg.ToolCallID)
		}
	}
	if strings.Join(resultIDs, ",") != "call-review,call-gated" {
		t.Fatalf("resume context tool results=%v want call-review,call-gated", resultIDs)
	}
}

type skillThenThreeApprovalToolsLLM struct {
	stage    int
	sessions [][]llm.Message
}

func (m *skillThenThreeApprovalToolsLLM) Execute(_ context.Context, messages []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	cp := make([]llm.Message, len(messages))
	copy(cp, messages)
	m.sessions = append(m.sessions, cp)
	switch m.stage {
	case 0:
		m.stage++
		msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("run review helpers")})
		msg.ToolCalls = []llm.ToolCall{
			{ID: "call-review", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "review", Arguments: `{}`}},
			{ID: "call-status", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "approval_status", Arguments: `{}`}},
			{ID: "call-diff", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "approval_diff", Arguments: `{}`}},
			{ID: "call-branch", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "approval_branch", Arguments: `{}`}},
		}
		return &llm.Result{Message: &msg}, nil
	default:
		msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("answer after approval")})
		return &llm.Result{Message: &msg}, nil
	}
}

func TestToolOrchestrationApprovalBatchStopsAtFirstRequiresAction(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "review", ReadOnly: true, ConcurrencySafe: true})
	st.RegisterToolMeta(event.ToolMeta{Name: "approval_status", ReadOnly: true, ConcurrencySafe: true})
	st.RegisterToolMeta(event.ToolMeta{Name: "approval_diff", ReadOnly: true, ConcurrencySafe: true})
	st.RegisterToolMeta(event.ToolMeta{Name: "approval_branch", ReadOnly: true, ConcurrencySafe: true})

	nextID := 0
	st.SetActionHook(func(context.Context, string, any) (string, bool, error) {
		nextID++
		return fmt.Sprintf("act-%d", nextID), true, nil
	})

	var skillRuns, statusRuns, diffRuns, branchRuns int
	skillTool, _ := llm.NewTool("review", "review skill", func(context.Context, *struct{}) (string, error) {
		skillRuns++
		return "review skill instructions", nil
	})
	statusTool, _ := llm.NewTool("approval_status", "approval status", func(ctx context.Context, _ *struct{}) (string, error) {
		statusRuns++
		if toolpkg.ApprovedActionIDFromContext(ctx) == "" {
			return "", &toolpkg.RequiresActionError{ActionID: "act-1", ActionKind: "approval_status", ToolName: "approval_status", ToolInput: map[string]any{}}
		}
		return "status-output", nil
	})
	diffTool, _ := llm.NewTool("approval_diff", "approval diff", func(ctx context.Context, _ *struct{}) (string, error) {
		diffRuns++
		if toolpkg.ApprovedActionIDFromContext(ctx) == "" {
			return "", &toolpkg.RequiresActionError{ActionID: "act-2", ActionKind: "approval_diff", ToolName: "approval_diff", ToolInput: map[string]any{}}
		}
		return "diff-output", nil
	})
	branchTool, _ := llm.NewTool("approval_branch", "approval branch", func(ctx context.Context, _ *struct{}) (string, error) {
		branchRuns++
		if toolpkg.ApprovedActionIDFromContext(ctx) == "" {
			return "", &toolpkg.RequiresActionError{ActionID: "act-3", ActionKind: "approval_branch", ToolName: "approval_branch", ToolInput: map[string]any{}}
		}
		return "branch-output", nil
	})

	inner := &skillThenThreeApprovalToolsLLM{}
	wrapped := wrapToolOrchestrationLLM(inner, st)

	_, err := wrapped.Execute(context.Background(), []llm.Message{llm.UserMessage(llm.Text("review"))}, []*llm.Tool{skillTool, statusTool, diffTool, branchTool})
	var rae *toolpkg.RequiresActionError
	if !errors.As(err, &rae) || rae == nil {
		t.Fatalf("expected RequiresActionError, got %T %v", err, err)
	}
	if rae.ActionID != "act-1" || rae.ActionKind != "approval_status" {
		t.Fatalf("unexpected first approval gate: %+v", rae)
	}
	if skillRuns != 1 || statusRuns != 1 || diffRuns != 0 || branchRuns != 0 {
		t.Fatalf("unexpected pre-approval runs skill=%d status=%d diff=%d branch=%d", skillRuns, statusRuns, diffRuns, branchRuns)
	}
}

func TestToolOrchestrationResumeRequiresFreshApprovalForLaterPendingToolCalls(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "review", ReadOnly: true, ConcurrencySafe: true})
	st.RegisterToolMeta(event.ToolMeta{Name: "approval_status", ReadOnly: true, ConcurrencySafe: true})
	st.RegisterToolMeta(event.ToolMeta{Name: "approval_diff", ReadOnly: true, ConcurrencySafe: true})
	st.RegisterToolMeta(event.ToolMeta{Name: "approval_branch", ReadOnly: true, ConcurrencySafe: true})

	nextID := 0
	st.SetActionHook(func(context.Context, string, any) (string, bool, error) {
		nextID++
		return fmt.Sprintf("act-%d", nextID), true, nil
	})

	var skillRuns, statusRuns, diffRuns, branchRuns int
	skillTool, _ := llm.NewTool("review", "review skill", func(context.Context, *struct{}) (string, error) {
		skillRuns++
		return "review skill instructions", nil
	})
	statusTool, _ := llm.NewTool("approval_status", "approval status", func(ctx context.Context, _ *struct{}) (string, error) {
		statusRuns++
		if toolpkg.ApprovedActionIDFromContext(ctx) == "act-1" {
			return "status-output", nil
		}
		return "", &toolpkg.RequiresActionError{ActionID: "act-1", ActionKind: "approval_status", ToolName: "approval_status", ToolInput: map[string]any{}}
	})
	diffTool, _ := llm.NewTool("approval_diff", "approval diff", func(ctx context.Context, _ *struct{}) (string, error) {
		diffRuns++
		if toolpkg.ApprovedActionIDFromContext(ctx) == "act-2" {
			return "diff-output", nil
		}
		return "", &toolpkg.RequiresActionError{ActionID: "act-2", ActionKind: "approval_diff", ToolName: "approval_diff", ToolInput: map[string]any{}}
	})
	branchTool, _ := llm.NewTool("approval_branch", "approval branch", func(ctx context.Context, _ *struct{}) (string, error) {
		branchRuns++
		if toolpkg.ApprovedActionIDFromContext(ctx) == "act-3" {
			return "branch-output", nil
		}
		return "", &toolpkg.RequiresActionError{ActionID: "act-3", ActionKind: "approval_branch", ToolName: "approval_branch", ToolInput: map[string]any{}}
	})

	inner := &skillThenThreeApprovalToolsLLM{}
	wrapped := wrapToolOrchestrationLLM(inner, st)

	_, err := wrapped.Execute(context.Background(), []llm.Message{llm.UserMessage(llm.Text("review"))}, []*llm.Tool{skillTool, statusTool, diffTool, branchTool})
	var rae *toolpkg.RequiresActionError
	if !errors.As(err, &rae) || rae == nil {
		t.Fatalf("expected first RequiresActionError, got %T %v", err, err)
	}
	if rae.ActionID != "act-1" {
		t.Fatalf("first approval action_id=%q want act-1", rae.ActionID)
	}
	if skillRuns != 1 || statusRuns != 1 || diffRuns != 0 || branchRuns != 0 {
		t.Fatalf("unexpected first-run counts skill=%d status=%d diff=%d branch=%d", skillRuns, statusRuns, diffRuns, branchRuns)
	}

	resumeCtx := toolpkg.WithApprovedActionID(context.Background(), "act-1")
	resumeCtx = toolpkg.WithToolApprovalResume(resumeCtx, &toolpkg.ToolApprovalResumeState{Session: rae.SessionSnapshot})
	_, err = wrapped.Execute(resumeCtx, []llm.Message{llm.UserMessage(llm.Text("ignored"))}, []*llm.Tool{skillTool, statusTool, diffTool, branchTool})
	var rae2 *toolpkg.RequiresActionError
	if !errors.As(err, &rae2) || rae2 == nil {
		t.Fatalf("expected second RequiresActionError on resume, got %T %v", err, err)
	}
	if rae2.ActionID != "act-2" || rae2.ActionKind != "approval_diff" {
		t.Fatalf("unexpected second approval gate: %+v", rae2)
	}
	if skillRuns != 1 {
		t.Fatalf("review skill replayed on resume; skillRuns=%d want 1", skillRuns)
	}
	if statusRuns != 2 || diffRuns != 1 || branchRuns != 0 {
		t.Fatalf("unexpected resume counts skill=%d status=%d diff=%d branch=%d", skillRuns, statusRuns, diffRuns, branchRuns)
	}
}

type twoSerialApprovalToolsLLM struct {
	calls int
}

func (m *twoSerialApprovalToolsLLM) Execute(_ context.Context, _ []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	m.calls++
	if m.calls == 1 {
		msg := llm.AssistantMessage(nil,
			llm.ToolCall{ID: "call-shell", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "approval_shell", Arguments: `{}`}},
			llm.ToolCall{ID: "call-root", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "approval_root", Arguments: `{}`}},
		)
		return &llm.Result{Message: &msg}, nil
	}
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("done")})
	return &llm.Result{Message: &msg}, nil
}

func TestToolOrchestrationStopsBatchAfterFirstRequiresAction(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "approval_shell", Destructive: true})
	st.RegisterToolMeta(event.ToolMeta{Name: "approval_root", Destructive: true})

	var shellRuns, rootRuns int
	shellTool, _ := llm.NewTool("approval_shell", "approval shell", func(_ context.Context, _ *struct{}) (string, error) {
		shellRuns++
		return "", &toolpkg.RequiresActionError{ActionID: "act-shell", ActionKind: "approval_shell", ToolName: "approval_shell", ToolInput: map[string]any{}}
	})
	rootTool, _ := llm.NewTool("approval_root", "approval root", func(_ context.Context, _ *struct{}) (string, error) {
		rootRuns++
		return "", &toolpkg.RequiresActionError{ActionID: "act-root", ActionKind: "approval_root", ToolName: "approval_root", ToolInput: map[string]any{}}
	})

	_, err := wrapToolOrchestrationLLM(&twoSerialApprovalToolsLLM{}, st).Execute(
		context.Background(),
		[]llm.Message{llm.UserMessage(llm.Text("run both"))},
		[]*llm.Tool{shellTool, rootTool},
	)
	var rae *toolpkg.RequiresActionError
	if !errors.As(err, &rae) || rae == nil {
		t.Fatalf("expected first RequiresActionError, got %T %v", err, err)
	}
	if rae.ActionID != "act-shell" {
		t.Fatalf("action_id=%q want act-shell", rae.ActionID)
	}
	if shellRuns != 1 || rootRuns != 0 {
		t.Fatalf("unexpected runs shell=%d root=%d; second approval tool must not run before first action resolves", shellRuns, rootRuns)
	}
}

func TestToolOrchestrationResumeApprovalDoesNotAuthorizeSecondMissingToolCall(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "approval_shell", Destructive: true})
	st.RegisterToolMeta(event.ToolMeta{Name: "approval_root", Destructive: true})

	snapshot := []llm.Message{
		llm.UserMessage(llm.Text("run both")),
		llm.AssistantMessage(nil,
			llm.ToolCall{ID: "call-shell", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "approval_shell", Arguments: `{}`}},
			llm.ToolCall{ID: "call-root", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "approval_root", Arguments: `{}`}},
		),
	}
	var shellRuns, rootRuns int
	shellTool, _ := llm.NewTool("approval_shell", "approval shell", func(ctx context.Context, _ *struct{}) (string, error) {
		shellRuns++
		if toolpkg.ApprovedActionIDFromContext(ctx) == "act-shell" {
			return "shell-output", nil
		}
		return "", &toolpkg.RequiresActionError{ActionID: "act-shell", ActionKind: "approval_shell", ToolName: "approval_shell", ToolInput: map[string]any{}}
	})
	rootTool, _ := llm.NewTool("approval_root", "approval root", func(ctx context.Context, _ *struct{}) (string, error) {
		rootRuns++
		if toolpkg.ApprovedActionIDFromContext(ctx) != "" {
			t.Fatalf("second missing tool call inherited approved action id %q", toolpkg.ApprovedActionIDFromContext(ctx))
		}
		return "", &toolpkg.RequiresActionError{ActionID: "act-root", ActionKind: "approval_root", ToolName: "approval_root", ToolInput: map[string]any{}}
	})

	ctx := toolpkg.WithApprovedActionID(context.Background(), "act-shell")
	ctx = toolpkg.WithToolApprovalResume(ctx, &toolpkg.ToolApprovalResumeState{Session: snapshot})
	_, err := wrapToolOrchestrationLLM(&twoSerialApprovalToolsLLM{}, st).Execute(
		ctx,
		[]llm.Message{llm.UserMessage(llm.Text("ignored"))},
		[]*llm.Tool{shellTool, rootTool},
	)
	var rae *toolpkg.RequiresActionError
	if !errors.As(err, &rae) || rae == nil {
		t.Fatalf("expected second RequiresActionError, got %T %v", err, err)
	}
	if rae.ActionID != "act-root" {
		t.Fatalf("action_id=%q want act-root", rae.ActionID)
	}
	if shellRuns != 1 || rootRuns != 1 {
		t.Fatalf("unexpected resume runs shell=%d root=%d", shellRuns, rootRuns)
	}
}

// enterThenExitPlanLLM models the plan-mode lifecycle across iterations: it emits
// an approval-gated tool A (enter_plan), and once that is approved and the turn
// resumes, emits a different approval-gated tool B (exit_plan) in a *later* loop
// iteration, then finishes with text.
type enterThenExitPlanLLM struct {
	stage int
}

func (m *enterThenExitPlanLLM) Execute(_ context.Context, _ []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	switch m.stage {
	case 0:
		m.stage++
		msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("entering plan mode")})
		msg.ToolCalls = []llm.ToolCall{
			{ID: "call-enter", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "enter_plan", Arguments: `{}`}},
		}
		return &llm.Result{Message: &msg}, nil
	case 1:
		m.stage++
		msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("submitting plan for approval")})
		msg.ToolCalls = []llm.ToolCall{
			{ID: "call-exit", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "exit_plan", Arguments: `{}`}},
		}
		return &llm.Result{Message: &msg}, nil
	default:
		msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("done")})
		return &llm.Result{Message: &msg}, nil
	}
}

// TestToolOrchestrationResumeDoesNotLeakApprovalToLaterTurnToolCalls guards the
// fix for an approval-bypass bug: when a turn resumes after an approved tool
// call, the approved action ID must authorize *only* the replayed call. A
// distinct approval-gated tool the model emits in a subsequent loop iteration
// (the exit_plan_mode-after-enter_plan_mode case reported in the TUI) must
// trigger its own approval instead of silently inheriting the prior approval.
func TestToolOrchestrationResumeDoesNotLeakApprovalToLaterTurnToolCalls(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "enter_plan"})
	st.RegisterToolMeta(event.ToolMeta{Name: "exit_plan"})
	st.SetActionHook(func(context.Context, string, any) (string, bool, error) {
		return "act", true, nil
	})

	var enterRuns, exitRuns int
	enterTool, _ := llm.NewTool("enter_plan", "enter plan mode", func(ctx context.Context, _ *struct{}) (string, error) {
		enterRuns++
		if toolpkg.ApprovedActionIDFromContext(ctx) == "" {
			return "", &toolpkg.RequiresActionError{ActionID: "act-enter", ActionKind: "enter_plan", ToolName: "enter_plan", ToolInput: map[string]any{}}
		}
		return "entered plan mode", nil
	})
	exitTool, _ := llm.NewTool("exit_plan", "exit plan mode", func(ctx context.Context, _ *struct{}) (string, error) {
		exitRuns++
		if toolpkg.ApprovedActionIDFromContext(ctx) == "" {
			return "", &toolpkg.RequiresActionError{ActionID: "act-exit", ActionKind: "exit_plan", ToolName: "exit_plan", ToolInput: map[string]any{}}
		}
		return "exited plan mode", nil
	})

	inner := &enterThenExitPlanLLM{}
	wrapped := wrapToolOrchestrationLLM(inner, st)

	// First turn: enter_plan gates for approval.
	_, err := wrapped.Execute(context.Background(), []llm.Message{llm.UserMessage(llm.Text("plan this"))}, []*llm.Tool{enterTool, exitTool})
	var rae *toolpkg.RequiresActionError
	if !errors.As(err, &rae) || rae == nil || rae.ActionKind != "enter_plan" {
		t.Fatalf("expected enter_plan RequiresActionError, got %T %v", err, err)
	}
	if enterRuns != 1 || exitRuns != 0 {
		t.Fatalf("unexpected first-turn runs enter=%d exit=%d", enterRuns, exitRuns)
	}

	// Resume with enter_plan approved. The model then emits exit_plan in a later
	// iteration; it must gate for its own approval rather than inherit act-enter.
	resumeCtx := toolpkg.WithApprovedActionID(context.Background(), "act-enter")
	resumeCtx = toolpkg.WithToolApprovalResume(resumeCtx, &toolpkg.ToolApprovalResumeState{Session: rae.SessionSnapshot})
	_, err = wrapped.Execute(resumeCtx, []llm.Message{llm.UserMessage(llm.Text("ignored"))}, []*llm.Tool{enterTool, exitTool})
	var rae2 *toolpkg.RequiresActionError
	if !errors.As(err, &rae2) || rae2 == nil {
		t.Fatalf("expected exit_plan to require fresh approval on resume, got %T %v", err, err)
	}
	if rae2.ActionKind != "exit_plan" {
		t.Fatalf("resume approval gate kind=%q want exit_plan", rae2.ActionKind)
	}
	if enterRuns != 2 {
		t.Fatalf("enter_plan should replay once on resume; enterRuns=%d want 2", enterRuns)
	}
	if exitRuns != 1 {
		t.Fatalf("exit_plan should run once and gate; exitRuns=%d want 1", exitRuns)
	}
}

func TestToolOrchestrationTruncatesLargeToolResultBeforeContextReplay(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "shell"})

	const large = 164800
	largeDiff := "diff --git a/a.go b/a.go\n" + strings.Repeat("+x\n", large/2)

	shellTool, err := llm.NewTool("shell", "shell", func(context.Context, *struct{}) (string, error) {
		return largeDiff, nil
	})
	if err != nil {
		t.Fatalf("shell tool: %v", err)
	}

	inner := &recordingFinalAfterSingleToolLLM{name: "shell"}
	_, err = wrapToolOrchestrationLLM(inner, st).Execute(
		context.Background(),
		[]llm.Message{llm.UserMessage(llm.Text("diff"))},
		[]*llm.Tool{shellTool},
	)
	if err != nil {
		t.Fatalf("Execute err=%v", err)
	}
	if len(inner.sessions) < 2 {
		t.Fatalf("expected replay session, got %d", len(inner.sessions))
	}
	var toolText string
	for _, msg := range inner.sessions[1] {
		if msg.Role == llm.RoleTool && msg.ToolCallID == "call-1" {
			toolText = msg.TextContent()
			break
		}
	}
	if toolText == "" {
		t.Fatal("expected tool result message in replay session")
	}
	if len(toolText) > toolpkg.SpillThresholdBytes+512 {
		t.Fatalf("tool result still too large for context: %d bytes", len(toolText))
	}
	if !strings.Contains(toolText, "[tool output truncated for context:") {
		t.Fatalf("expected truncation marker, got %q", toolText)
	}
	if !strings.Contains(toolText, "full output available via read_file:") {
		t.Fatalf("expected persisted output path hint, got %q", toolText)
	}
	if !strings.Contains(toolText, "diff --git a/a.go b/a.go") {
		t.Fatalf("expected diff header preserved, got %q", toolText)
	}
}

func TestToolOrchestrationTruncatesLargeReadFileResultBeforeContextReplay(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "read_file", ReadOnly: true, ConcurrencySafe: true})

	largeFile := "1|package main\n" + strings.Repeat("2|xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\n", 1200)
	readTool, err := llm.NewTool("read_file", "read file", func(context.Context, *struct{}) (string, error) {
		return largeFile, nil
	})
	if err != nil {
		t.Fatalf("read_file tool: %v", err)
	}

	inner := &recordingFinalAfterSingleToolLLM{name: "read_file"}
	_, err = wrapToolOrchestrationLLM(inner, st).Execute(
		context.Background(),
		[]llm.Message{llm.UserMessage(llm.Text("read the file"))},
		[]*llm.Tool{readTool},
	)
	if err != nil {
		t.Fatalf("Execute err=%v", err)
	}
	if len(inner.sessions) < 2 {
		t.Fatalf("expected replay session, got %d", len(inner.sessions))
	}
	var toolText string
	for _, msg := range inner.sessions[1] {
		if msg.Role == llm.RoleTool && msg.ToolCallID == "call-1" {
			toolText = msg.TextContent()
			break
		}
	}
	if toolText == "" {
		t.Fatal("expected tool result message in replay session")
	}
	if len(toolText) > toolpkg.SpillThresholdBytes+512 {
		t.Fatalf("tool result still too large for context: %d bytes", len(toolText))
	}
	if !strings.Contains(toolText, "[tool output truncated for context:") {
		t.Fatalf("expected truncation marker, got %q", toolText)
	}
	if !strings.Contains(toolText, "full output available via read_file:") {
		t.Fatalf("expected persisted output path hint, got %q", toolText)
	}
	if !strings.Contains(toolText, "1|package main") {
		t.Fatalf("expected file header preserved, got %q", toolText)
	}
}

func TestForkExecutionChainAlsoTruncatesLargeToolResultBeforeContextReplay(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "shell"})

	const large = 164800
	largeDiff := "diff --git a/a.go b/a.go\n" + strings.Repeat("+x\n", large/2)

	shellTool, err := llm.NewTool("shell", "shell", func(context.Context, *struct{}) (string, error) {
		return largeDiff, nil
	})
	if err != nil {
		t.Fatalf("shell tool: %v", err)
	}

	inner := &recordingFinalAfterSingleToolLLM{name: "shell"}
	runner := &Runner{Deps: &Deps{}, tools: st}
	_, err = runner.wrapExecutionLLM(inner, false).Execute(
		context.Background(),
		[]llm.Message{llm.UserMessage(llm.Text("diff"))},
		[]*llm.Tool{shellTool},
	)
	if err != nil {
		t.Fatalf("Execute err=%v", err)
	}
	if len(inner.sessions) < 2 {
		t.Fatalf("expected replay session, got %d", len(inner.sessions))
	}
	var toolText string
	for _, msg := range inner.sessions[1] {
		if msg.Role == llm.RoleTool && msg.ToolCallID == "call-1" {
			toolText = msg.TextContent()
			break
		}
	}
	if toolText == "" {
		t.Fatal("expected tool result message in replay session")
	}
	if len(toolText) > toolpkg.SpillThresholdBytes+512 {
		t.Fatalf("tool result still too large for context: %d bytes", len(toolText))
	}
	if !strings.Contains(toolText, "[tool output truncated for context:") {
		t.Fatalf("expected truncation marker, got %q", toolText)
	}
	if !strings.Contains(toolText, "full output available via read_file:") {
		t.Fatalf("expected persisted output path hint, got %q", toolText)
	}
}

// finalAssistantOnlyLLM returns a single no-tool-call assistant response.
type finalAssistantOnlyLLM struct{}

func (m *finalAssistantOnlyLLM) Execute(_ context.Context, _ []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("the answer is 42")})
	return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
}

// TestExecuteFinalAssistantIncludedInSession verifies Fix A: when the inner
// LLM returns a final (no-tool-call) assistant message, Result.Session must
// end with that message so appendAssistantOutcome persists it. Without the
// fix, the final assistant answer is streamed to the UI but never written to
// the transcript DB - the next round and /resume replay lose it.
func TestExecuteFinalAssistantIncludedInSession(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	ctx := llm.WithAgentSessionID(context.Background(), "sid-fixa")
	ctx = WithTurnInputRuntime(ctx, NewTurnInputRuntime())

	wrapped := wrapToolOrchestrationLLM(&finalAssistantOnlyLLM{}, st)
	res, err := wrapped.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("what is the answer"))}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res == nil || len(res.Session) == 0 {
		t.Fatal("expected non-empty Result.Session")
	}
	last := res.Session[len(res.Session)-1]
	if last.Role != llm.RoleAssistant {
		t.Fatalf("last session message role=%q want assistant", last.Role)
	}
	if got := strings.TrimSpace(last.TextContent()); got != "the answer is 42" {
		t.Fatalf("last session message text=%q want %q", got, "the answer is 42")
	}
}

// toolCallThenFinalLLM issues one tool call, then a final text answer.
type toolCallThenFinalLLM struct {
	calls int
}

func (m *toolCallThenFinalLLM) Execute(_ context.Context, _ []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	m.calls++
	if m.calls == 1 {
		msg := llm.AssistantMessage(nil,
			llm.ToolCall{ID: "call-1", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "read_file", Arguments: `{"file_path":"a.txt"}`}},
		)
		return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
	}
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("file contents are: hello")})
	return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
}

// TestExecuteToolCallThenFinalAssistantSessionComplete verifies Fix A: after a
// tool call round + final answer, Result.Session must be
// [assistant(tool_calls), tool_result, final-assistant]. Without the fix the
// final assistant is missing.
func TestExecuteToolCallThenFinalAssistantSessionComplete(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "read_file", ReadOnly: true})
	tool, err := llm.NewTool("read_file", "read", func(context.Context, *struct{}) (string, error) {
		return "hello", nil
	})
	if err != nil {
		t.Fatalf("tool: %v", err)
	}
	ctx := llm.WithAgentSessionID(context.Background(), "sid-fixa-tool")
	ctx = WithTurnInputRuntime(ctx, NewTurnInputRuntime())

	wrapped := wrapToolOrchestrationLLM(&toolCallThenFinalLLM{}, st)
	res, err := wrapped.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("read a.txt"))}, []*llm.Tool{tool})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res == nil || len(res.Session) < 4 {
		t.Fatalf("expected at least 4 session messages (user,asst,tool,asst), got %d", len(res.Session))
	}
	// Strip system messages for role check.
	var roles []string
	for _, m := range res.Session {
		if m.Role == llm.RoleSystem {
			continue
		}
		roles = append(roles, m.Role)
	}
	// Expected: user, assistant, tool, assistant
	wantRoles := []string{llm.RoleUser, llm.RoleAssistant, llm.RoleTool, llm.RoleAssistant}
	if len(roles) != len(wantRoles) {
		t.Fatalf("session roles=%v want %v", roles, wantRoles)
	}
	for i, r := range roles {
		if r != wantRoles[i] {
			t.Fatalf("session role[%d]=%q want %q (full=%v)", i, r, wantRoles[i], roles)
		}
	}
	last := res.Session[len(res.Session)-1]
	if got := strings.TrimSpace(last.TextContent()); got != "file contents are: hello" {
		t.Fatalf("final session message text=%q want %q", got, "file contents are: hello")
	}
}

// cancelAfterToolLLM does one tool call, then returns context.Canceled on the
// second LLM call (simulating Esc during the model's follow-up response).
type cancelAfterToolLLM struct {
	calls int
}

func (m *cancelAfterToolLLM) Execute(_ context.Context, _ []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	m.calls++
	if m.calls == 1 {
		msg := llm.AssistantMessage(nil,
			llm.ToolCall{ID: "call-1", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "read_file", Arguments: `{"file_path":"a.txt"}`}},
		)
		return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
	}
	return nil, context.Canceled
}

// TestPartialSessionCaptureOnCancel verifies Fix B1: when the inner LLM is
// cancelled after a completed tool call round, the PartialSessionCapture must
// contain the completed assistant tool_calls + tool result so the dispatcher
// can persist them.
func TestPartialSessionCaptureOnCancel(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "read_file", ReadOnly: true})
	tool, err := llm.NewTool("read_file", "read", func(context.Context, *struct{}) (string, error) {
		return "hello", nil
	})
	if err != nil {
		t.Fatalf("tool: %v", err)
	}
	capture := NewPartialSessionCapture()
	ctx := llm.WithAgentSessionID(context.Background(), "sid-fixb")
	ctx = WithTurnInputRuntime(ctx, NewTurnInputRuntime())
	ctx = WithPartialSessionCapture(ctx, capture)

	wrapped := wrapToolOrchestrationLLM(&cancelAfterToolLLM{}, st)
	_, err = wrapped.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("read a.txt"))}, []*llm.Tool{tool})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	snap := capture.Snapshot()
	if len(snap) == 0 {
		t.Fatal("expected non-empty partial session capture after cancel")
	}
	// Snapshot should contain the user message, the assistant tool_calls, and
	// the tool result (completed before cancellation).
	var hasAssistantToolCalls, hasToolResult bool
	for _, m := range snap {
		if m.Role == llm.RoleAssistant && len(m.ToolCalls) > 0 {
			hasAssistantToolCalls = true
		}
		if m.Role == llm.RoleTool {
			hasToolResult = true
		}
	}
	if !hasAssistantToolCalls {
		t.Fatal("partial capture missing assistant tool_calls message")
	}
	if !hasToolResult {
		t.Fatal("partial capture missing tool result message")
	}
}

// TestPartialSessionCaptureNilSafe verifies the capture is a no-op when nil
// (subagent/gateway paths do not inject one).
func TestPartialSessionCaptureNilSafe(t *testing.T) {
	var c *PartialSessionCapture
	c.Set(nil)   // must not panic
	c.Snapshot() // must not panic
	if got := c.Snapshot(); got != nil {
		t.Fatalf("nil capture Snapshot=%v want nil", got)
	}
}

// streamingCancelLLM streams assistant text on the first call (alongside a tool
// call) and then cancels mid-stream on the second, mimicking Esc pressed while
// the model is composing its follow-up.
type streamingCancelLLM struct {
	calls int
}

func (m *streamingCancelLLM) Execute(ctx context.Context, _ []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	m.calls++
	sink := llm.StreamSinkFrom(ctx)
	if m.calls == 1 {
		if sink != nil && sink.OnDelta != nil {
			sink.OnDelta("first response\n\n")
		}
		msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("first response\n\n")},
			llm.ToolCall{ID: "call-1", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "read_file", Arguments: `{"file_path":"a.txt"}`}},
		)
		return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
	}
	if sink != nil && sink.OnDelta != nil {
		sink.OnDelta("interrupted tail")
	}
	return nil, context.Canceled
}

// TestResponseCompletedBoundaryClearsStreamedText is the regression guard for
// duplicated messages on resume replay. The surface buffers streamed assistant
// deltas for the whole turn; once a finished response is appended to the
// session the capture owns that text, so the orchestration loop must signal the
// boundary and let the surface drop what it buffered. Without the signal the
// buffer still holds "first response" at cancel time and the cancel path writes
// it a second time, next to the captured assistant message.
func TestResponseCompletedBoundaryClearsStreamedText(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "read_file", ReadOnly: true})
	tool, err := llm.NewTool("read_file", "read", func(context.Context, *struct{}) (string, error) {
		return "hello", nil
	})
	if err != nil {
		t.Fatalf("tool: %v", err)
	}

	var buf strings.Builder
	capture := NewPartialSessionCapture()
	ctx := llm.WithAgentSessionID(context.Background(), "sid-boundary")
	ctx = WithTurnInputRuntime(ctx, NewTurnInputRuntime())
	ctx = WithPartialSessionCapture(ctx, capture)
	ctx = llm.WithStreamSink(ctx, &llm.StreamSink{
		OnDelta:             func(s string) { buf.WriteString(s) },
		OnResponseCompleted: func() { buf.Reset() },
	})

	wrapped := wrapToolOrchestrationLLM(&streamingCancelLLM{}, st)
	_, err = wrapped.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("read a.txt"))}, []*llm.Tool{tool})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}

	if got := buf.String(); got != "interrupted tail" {
		t.Fatalf("streamed buffer = %q, want only the interrupted call's text %q", got, "interrupted tail")
	}
	// The completed response's text is not lost: it lives in the capture.
	var capturedText string
	for _, m := range capture.Snapshot() {
		if m.Role == llm.RoleAssistant {
			capturedText += llm.TextContent(m.Parts...)
		}
	}
	if !strings.Contains(capturedText, "first response") {
		t.Fatalf("capture missing the completed response text, got %q", capturedText)
	}
}

// TestMutedStreamDropsResponseCompleted keeps a nested/internal LLM call from
// clearing the foreground turn's buffered text: it contributes no deltas, so
// signalling a boundary could only discard text the parent still needs.
func TestMutedStreamDropsResponseCompleted(t *testing.T) {
	ctx := llm.WithStreamSink(context.Background(), &llm.StreamSink{
		OnResponseStarted:   func() {},
		OnDelta:             func(string) {},
		OnResponseCompleted: func() {},
	})
	muted := llm.StreamSinkFrom(llm.WithMutedForegroundStream(ctx))
	if muted == nil {
		t.Fatal("expected a muted sink")
	}
	if muted.OnResponseCompleted != nil {
		t.Fatal("muted sink must not signal response boundaries")
	}
	if muted.OnResponseStarted != nil {
		t.Fatal("muted nested calls must not acknowledge foreground input")
	}
}

func TestToolOwnsApprovalProtocol(t *testing.T) {
	for _, name := range []string{"request_permissions", "user_interaction", "enter_plan_mode", "exit_plan_mode"} {
		want := name != "request_permissions"
		if got := toolOwnsApprovalProtocol(name); got != want {
			t.Fatalf("toolOwnsApprovalProtocol(%q)=%t want %t", name, got, want)
		}
	}
	if toolOwnsApprovalProtocol("custom_read_only_tool") {
		t.Fatal("metadata-only custom tools must not own an approval protocol")
	}
}

func TestToolPermissionMiddlewareUsesMCPApprovalAnnotations(t *testing.T) {
	t.Run("read only", func(t *testing.T) {
		r := &Runner{Deps: &Deps{}}
		r.tools = toolpkg.NewState(t.TempDir())
		readOnly := true
		r.tools.RegisterToolMeta(event.ToolMeta{
			Name: "custom_reader", Category: "mcp", ReadOnly: true,
			ReadOnlyHint: &readOnly, MCPApprovalMode: string(appcfg.MCPToolApprovalAuto),
		})

		tool, err := llm.NewRawTool("custom_reader", "custom reader", map[string]any{"type": "object"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		called := false
		wrapped := r.newToolPermissionMiddleware()(tool, func(context.Context, string) (any, error) {
			called = true
			return "ok", nil
		})
		if _, err := wrapped(context.Background(), `{}`); err != nil || !called {
			t.Fatalf("read-only MCP tool should execute: called=%t err=%v", called, err)
		}
	})

	t.Run("missing annotations", func(t *testing.T) {
		r := &Runner{Deps: &Deps{}}
		r.tools = toolpkg.NewState(t.TempDir())
		r.tools.RegisterToolMeta(event.ToolMeta{Name: "custom_reader", Category: "mcp", MCPApprovalMode: string(appcfg.MCPToolApprovalAuto)})

		tool, err := llm.NewRawTool("custom_reader", "custom reader", map[string]any{"type": "object"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		called := false
		wrapped := r.newToolPermissionMiddleware()(tool, func(context.Context, string) (any, error) {
			called = true
			return "unexpected", nil
		})
		_, err = wrapped(context.Background(), `{}`)
		if err == nil || called {
			t.Fatalf("missing annotations authorized custom tool: called=%t err=%v", called, err)
		}
	})
}

func TestMCPToolApprovalModes(t *testing.T) {
	readOnly := true
	destructive := true
	for _, tt := range []struct {
		name     string
		mode     appcfg.MCPToolApprovalMode
		readOnly *bool
		destroy  *bool
		wantAsk  bool
	}{
		{name: "auto read", mode: appcfg.MCPToolApprovalAuto, readOnly: &readOnly},
		{name: "auto write", mode: appcfg.MCPToolApprovalAuto, destroy: &destructive, wantAsk: true},
		{name: "prompt read", mode: appcfg.MCPToolApprovalPrompt, readOnly: &readOnly, wantAsk: true},
		{name: "writes read", mode: appcfg.MCPToolApprovalWrites, readOnly: &readOnly},
		{name: "writes unknown", mode: appcfg.MCPToolApprovalWrites, wantAsk: true},
		{name: "approve unknown", mode: appcfg.MCPToolApprovalApprove},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := &Runner{Deps: &Deps{}}
			r.tools = toolpkg.NewState(t.TempDir())
			r.tools.RegisterToolMeta(event.ToolMeta{
				Name: "mcp__server__tool", Category: "mcp", ReadOnlyHint: tt.readOnly,
				DestructiveHint: tt.destroy, MCPApprovalMode: string(tt.mode),
			})
			decision := r.applyMCPToolApprovalPolicy(context.Background(), "mcp__server__tool", safety.Decision{
				Behavior: safety.BehaviorAsk, Mode: safety.ModeOnRequest, Reason: "default_ask",
			})
			if gotAsk := decision.Behavior == safety.BehaviorAsk; gotAsk != tt.wantAsk {
				t.Fatalf("behavior=%s reason=%s wantAsk=%t", decision.Behavior, decision.Reason, tt.wantAsk)
			}
		})
	}
}

// The MCP hints add an approval requirement. Once an operator escape has
// settled the call there is no approval left to require, and under the session
// mode Full Access sets there is no prompt to answer either — so re-asking is a
// refusal in the one mode whose point is not to ask.
func TestMCPToolApprovalHonorsOperatorOverrides(t *testing.T) {
	const sessionID = "session-full-access-mcp"
	destructive := true
	for _, tt := range []struct {
		name   string
		reason string
	}{
		{name: "yolo", reason: safety.ReasonYOLO},
		{name: "full access preset", reason: safety.ReasonDangerFullAccess},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// Full Access picked in the session: the mode is session-scoped, so
			// the configured approval policy is still on-request.
			r := &Runner{Deps: &Deps{AppCfg: &appcfg.Root{SandboxMode: appcfg.SandboxModeDangerFullAccess}}}
			r.permRuntimeGet().Store(r.AppCfg).SetSessionRuntimeMode(sessionID, safety.ModeNever)
			r.tools = toolpkg.NewState(t.TempDir())
			r.tools.RegisterToolMeta(event.ToolMeta{
				Name: "mcp__server__tool", Category: "mcp", DestructiveHint: &destructive,
				MCPApprovalMode: string(appcfg.MCPToolApprovalAuto),
			})

			ctx := llm.WithAgentSessionID(context.Background(), sessionID)
			decision := r.applyMCPToolApprovalPolicy(ctx, "mcp__server__tool", safety.Decision{
				Behavior: safety.BehaviorAllow, Mode: safety.ModeNever,
				Reason: tt.reason, BypassSandbox: true,
			})
			if decision.Behavior != safety.BehaviorAllow || decision.Reason != tt.reason {
				t.Fatalf("override was re-gated by the MCP policy: %+v", decision)
			}
			if !decision.BypassSandbox {
				t.Fatalf("override lost its sandbox bypass: %+v", decision)
			}
		})
	}

	// A deny rule still wins: an override may skip approvals, never a rule that
	// forbids the call.
	r := &Runner{Deps: &Deps{AppCfg: &appcfg.Root{SandboxMode: appcfg.SandboxModeDangerFullAccess}}}
	r.tools = toolpkg.NewState(t.TempDir())
	r.tools.RegisterToolMeta(event.ToolMeta{
		Name: "mcp__server__tool", Category: "mcp", DestructiveHint: &destructive,
		MCPApprovalMode: string(appcfg.MCPToolApprovalAuto),
	})
	denied := r.applyMCPToolApprovalPolicy(context.Background(), "mcp__server__tool", safety.Decision{
		Behavior: safety.BehaviorDeny, Reason: "matched_deny_rule",
	})
	if denied.Behavior != safety.BehaviorDeny {
		t.Fatalf("deny rule was bypassed: %+v", denied)
	}
}

func TestGranularMCPToolApprovalIsIndependentFromElicitations(t *testing.T) {
	cfg := safety.GranularApprovalConfig{}
	decision := safety.Decision{Behavior: safety.BehaviorAsk, Reason: "mcp_tool_requires_approval"}
	if !granularApprovalPromptEnabled(cfg, "mcp__server__tool", map[string]any{"category": "mcp"}, decision) {
		t.Fatal("MCP tool-call approval must remain available")
	}
	if granularApprovalPromptEnabled(cfg, "mcp__server__tool", map[string]any{"category": "mcp", "mcp_elicitation": true}, decision) {
		t.Fatal("MCP elicitation must honor the granular elicitation setting")
	}
	matched := safety.PermissionRule{Behavior: safety.BehaviorAsk}
	decision.Matched = &matched
	if granularApprovalPromptEnabled(cfg, "mcp__server__tool", map[string]any{"category": "mcp"}, decision) {
		t.Fatal("rule-driven approval must honor the granular rules setting")
	}
}

func TestMCPToolApprovalCanPromptUnderNeverWithRestrictedFilesystem(t *testing.T) {
	r := &Runner{Deps: &Deps{AppCfg: &appcfg.Root{SandboxMode: appcfg.SandboxModeReadOnly}}}
	r.permRuntimeGet().Store(r.AppCfg).SetApprovalPolicy(safety.ApprovalPolicy{Mode: safety.ApprovalNever})
	r.tools = toolpkg.NewState(t.TempDir())
	r.tools.RegisterToolMeta(event.ToolMeta{
		Name: "mcp__server__write", Category: "mcp",
		MCPApprovalMode: string(appcfg.MCPToolApprovalPrompt),
	})
	_, pending, err := r.actionHook(context.Background(), "mcp__server__write", map[string]any{
		"category":          "mcp",
		"mcp_approval_mode": "prompt",
	})
	if pending || err == nil || !strings.Contains(err.Error(), "actions service is unavailable") {
		t.Fatalf("pending=%t err=%v", pending, err)
	}
}

func TestMCPPromptAutoApprovedForRestrictedRootWrite(t *testing.T) {
	const sessionID = "session-root-write"
	r := &Runner{Deps: &Deps{AppCfg: &appcfg.Root{SandboxMode: appcfg.SandboxModeReadOnly}}}
	r.permRuntimeGet().Store(r.AppCfg).SetApprovalPolicy(safety.ApprovalPolicy{Mode: safety.ApprovalNever})
	rootWrite := safety.FileSystemPermissionEntry{
		Path: safety.FileSystemPermissionPath{
			Type:  safety.FileSystemPermissionPathTypeSpecial,
			Value: &safety.FileSystemSpecialPath{Kind: safety.FileSystemSpecialPathRoot},
		},
		Access: safety.FileSystemAccessWrite,
	}
	safety.ApplyUpdate(r.permRuntimeGet().Store(r.AppCfg), safety.PermissionUpdate{
		Type: safety.UpdateAddPermissionGrants, Destination: safety.DestinationSession, SessionID: sessionID,
		FileSystemGrants: []safety.FileSystemPermissionGrant{{
			Entry: rootWrite, Scope: safety.GrantScopeSession,
		}},
	})
	ctx := llm.WithAgentSessionID(context.Background(), sessionID)
	if !r.mcpPermissionPromptAutoApproved(ctx) {
		t.Fatal("root-write profile must auto-approve MCP permission prompts under never")
	}
	safety.ApplyUpdate(r.permRuntimeGet().Store(r.AppCfg), safety.PermissionUpdate{
		Type: safety.UpdateAddPermissionGrants, Destination: safety.DestinationSession, SessionID: sessionID,
		FileSystemGrants: []safety.FileSystemPermissionGrant{{
			Entry: safety.FileSystemPermissionEntry{
				Path:   safety.NewFileSystemPermissionPath(filepath.Join(t.TempDir(), "private")),
				Access: safety.FileSystemAccessDeny,
			},
			Scope: safety.GrantScopeSession,
		}},
	})
	if r.mcpPermissionPromptAutoApproved(ctx) {
		t.Fatal("a narrower deny must prevent full-disk-write classification")
	}
}

func TestToolPermissionMiddlewareRequestPermissionsCreatesDedicatedAction(t *testing.T) {
	root := t.TempDir()
	r := &Runner{Deps: &Deps{}}
	r.tools = toolpkg.NewState(root)
	r.tools.RegisterToolMeta(event.ToolMeta{Name: "request_permissions", Category: "permissions", ReadOnly: true})
	r.tools.SetActionHook(func(_ context.Context, kind string, payload any) (string, bool, error) {
		if kind != "request_permissions" {
			t.Fatalf("kind=%q", kind)
		}
		return "act-permissions", true, nil
	})
	permissionTool, err := toolpkg.NewRequestPermissionsTool(r.tools, &toolpkg.AgentToolRuntime{})
	if err != nil {
		t.Fatal(err)
	}
	wrapped := r.newToolPermissionMiddleware()(permissionTool, permissionTool.Handle)
	outside := filepath.Join(filepath.Dir(root), "external")
	_, err = wrapped(context.Background(), `{"reason":"inspect","permissions":{"file_system":{"read":["`+outside+`"]}}}`)
	var req *toolpkg.RequiresActionError
	if !errors.As(err, &req) || req.ActionID != "act-permissions" || req.ActionKind != "request_permissions" {
		t.Fatalf("expected dedicated request_permissions action, got %T %v", err, err)
	}
}

func TestToolPermissionMiddleware_YOLOModeAutoAllows(t *testing.T) {
	// YOLO is read from the environment, not carried on the Runner.
	t.Setenv(safety.EnvYOLO, "1")
	// Set up a minimal runner with YOLO enabled
	r := &Runner{Deps: &Deps{}}

	// Create a tool that would normally require approval
	tool, err := llm.NewRawTool("shell", "Execute shell command", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{"type": "string"},
		},
	}, nil)
	if err != nil {
		t.Fatalf("failed to create tool: %v", err)
	}

	arguments := `{"command": "rm -rf /tmp/test"}`

	called := false
	handler := func(ctx context.Context, args string) (any, error) {
		called = true
		return "executed", nil
	}

	// Apply middleware
	middleware := r.newToolPermissionMiddleware()
	wrappedHandler := middleware(tool, handler)

	// Execute - should auto-allow without approval
	result, err := wrappedHandler(context.Background(), arguments)
	if err != nil {
		t.Fatalf("YOLO mode should auto-allow tool: %v", err)
	}
	if !called {
		t.Fatal("handler should have been called in YOLO mode")
	}
	if result != "executed" {
		t.Fatalf("expected result 'executed', got %v", result)
	}
}

func TestToolPermissionMiddleware_YOLOModeRespectsHardDeny(t *testing.T) {
	// YOLO is read from the environment, not carried on the Runner.
	t.Setenv(safety.EnvYOLO, "1")
	// Set up runner with YOLO enabled and a hard deny rule
	r := &Runner{Deps: &Deps{}}

	// Add a hard deny rule for shell tools
	r.permRuntimeGet().Store(r.AppCfg).AddRule(
		safety.SourceLocalSettings,
		safety.BehaviorDeny,
		safety.PermissionRuleValue{ToolName: "shell"},
	)

	// Create a denied tool
	tool, err := llm.NewRawTool("shell", "Execute shell command", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{"type": "string"},
		},
	}, nil)
	if err != nil {
		t.Fatalf("failed to create tool: %v", err)
	}

	arguments := `{"command": "rm -rf /"}`

	called := false
	handler := func(ctx context.Context, args string) (any, error) {
		called = true
		return "executed", nil
	}

	// Apply middleware
	middleware := r.newToolPermissionMiddleware()
	wrappedHandler := middleware(tool, handler)

	// Execute - should deny even in YOLO mode
	_, err = wrappedHandler(context.Background(), arguments)
	if err == nil {
		t.Fatal("YOLO mode should still respect hard deny rules")
	}
	if called {
		t.Fatal("handler should not be called when denied")
	}
}

func TestActionHookYOLOAllowsForceToolApproval(t *testing.T) {
	// YOLO is read from the environment, not carried on the Runner.
	t.Setenv(safety.EnvYOLO, "1")
	r := &Runner{Deps: &Deps{}}
	r.permRuntimeGet().Store(r.AppCfg).SetApprovalPolicy(safety.ApprovalPolicy{Mode: safety.ApprovalNever})

	payload := map[string]any{
		"command":             "cd /some/path && for f in $(find . -name '*.java'); do cat \"$f\"; done",
		"sandbox_permissions": "require_escalated",
		"force_tool_approval": true,
		"approval_reason":     "dangerous_shell_syntax",
	}
	id, pending, err := r.actionHook(context.Background(), "shell", payload)
	if err != nil {
		t.Fatalf("YOLO mode should allow force_tool_approval commands: %v", err)
	}
	if pending || id != "" {
		t.Fatalf("expected immediate allow, got id=%q pending=%v", id, pending)
	}
}

func TestToolPermissionMiddlewareRequestPermissionsCapturesExemptionReason(t *testing.T) {
	requestPermissionsTool := func(t *testing.T) *llm.Tool {
		t.Helper()
		tool, err := llm.NewRawTool("request_permissions", "request permissions", map[string]any{
			"type":       "object",
			"properties": map[string]any{"reason": map[string]any{"type": "string"}},
		}, nil)
		if err != nil {
			t.Fatalf("failed to create tool: %v", err)
		}
		return tool
	}

	t.Run("matched allow rule", func(t *testing.T) {
		r := &Runner{Deps: &Deps{}}
		r.permRuntimeGet().Store(r.AppCfg).AddRule(
			safety.SourceLocalSettings,
			safety.BehaviorAllow,
			safety.PermissionRuleValue{ToolName: "request_permissions"},
		)

		var gotReason string
		handler := func(ctx context.Context, args string) (any, error) {
			gotReason = toolpkg.PolicyApprovalReasonFromContext(ctx)
			return "ok", nil
		}
		ctx := toolpkg.WithPolicyApprovalReasonCapture(context.Background())
		if _, err := r.newToolPermissionMiddleware()(requestPermissionsTool(t), handler)(ctx, `{}`); err != nil {
			t.Fatalf("allow rule should allow request_permissions: %v", err)
		}
		if gotReason == "" || !strings.Contains(gotReason, "allow rule") {
			t.Fatalf("captured reason=%q want allow-rule explanation", gotReason)
		}
	})
}

func TestToolPermissionMiddlewareDefaultShellMarksPolicyApproved(t *testing.T) {
	r := &Runner{Deps: &Deps{AppCfg: &appcfg.Root{}}}
	r.AppCfg.SandboxMode = appcfg.SandboxModeWorkspaceWrite
	// The unattended shell default exists because a sandbox contains the
	// command; this store is built directly, so it has to be told.
	r.permRuntimeGet().Store(r.AppCfg).SetSandboxAvailable(true)

	tool, err := llm.NewRawTool("shell", "Execute shell command", map[string]any{
		"type":       "object",
		"properties": map[string]any{"command": map[string]any{"type": "string"}},
	}, nil)
	if err != nil {
		t.Fatalf("failed to create tool: %v", err)
	}

	called := false
	handler := func(ctx context.Context, args string) (any, error) {
		called = true
		if !toolpkg.PolicyApprovedFromContext(ctx) {
			t.Fatal("default shell allow must mark policy approved")
		}
		return "executed", nil
	}

	wrappedHandler := r.newToolPermissionMiddleware()(tool, handler)
	result, err := wrappedHandler(context.Background(), `{"command": "go test ./..."}`)
	if err != nil {
		t.Fatalf("sandboxed shell should auto-allow: %v", err)
	}
	if !called || result != "executed" {
		t.Fatalf("handler result=%v called=%t", result, called)
	}
}

func TestToolPermissionMiddlewareAllowRuleCanApproveSandboxBypass(t *testing.T) {
	const sessionID = "session-1"
	r := &Runner{Deps: &Deps{}}
	r.ApplyPermissionUpdate(safety.PermissionUpdate{
		Type:        safety.UpdateAddRules,
		Destination: safety.DestinationSession,
		SessionID:   sessionID,
		Behavior:    safety.BehaviorAllow,
		Rules: []safety.PermissionRuleValue{{
			ToolName:      "Bash",
			RuleContent:   "go test:*",
			BypassSandbox: true,
		}},
	})

	tool, err := llm.NewRawTool("shell", "Execute shell command", map[string]any{
		"type":       "object",
		"properties": map[string]any{"command": map[string]any{"type": "string"}},
	}, nil)
	if err != nil {
		t.Fatalf("failed to create tool: %v", err)
	}

	handler := func(ctx context.Context, args string) (any, error) {
		if !toolpkg.PolicyApprovedFromContext(ctx) {
			t.Fatal("allow rule must mark policy approved")
		}
		if !toolpkg.SandboxBypassApprovedFromContext(ctx) {
			t.Fatal("bypass_sandbox allow rule must mark sandbox bypass approved")
		}
		return "executed", nil
	}

	wrappedHandler := r.newToolPermissionMiddleware()(tool, handler)
	ctx := llm.WithAgentSessionID(context.Background(), sessionID)
	if _, err := wrappedHandler(ctx, `{"command": "go test ./..."}`); err != nil {
		t.Fatalf("allow rule should run without further approval: %v", err)
	}
}

// Under unless-trusted a known-safe command skips the prompt, and it must do so
// for every default reason — not only the one the middleware used to match by
// name. A no-sandbox environment reaches a different default reason, which
// silently disabled this path once.
func TestUnlessTrustedKnownSafeSkipsPromptRegardlessOfDefaultReason(t *testing.T) {
	for _, sandbox := range []bool{true, false} {
		name := "sandbox"
		if !sandbox {
			name = "no-sandbox"
		}
		t.Run(name, func(t *testing.T) {
			r := &Runner{Deps: &Deps{}}
			r.permRuntimeGet().Store(r.AppCfg).SetApprovalPolicy(safety.ApprovalPolicy{
				Mode: safety.ApprovalUnlessTrusted,
			})
			r.permRuntimeGet().Store(r.AppCfg).SetSandboxAvailable(sandbox)

			tool, err := llm.NewRawTool("shell", "Execute shell command", map[string]any{
				"type":       "object",
				"properties": map[string]any{"command": map[string]any{"type": "string"}},
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			called := false
			handler := func(ctx context.Context, args string) (any, error) {
				called = true
				return "ok", nil
			}
			if _, err := r.newToolPermissionMiddleware()(tool, handler)(
				context.Background(), `{"command":"git status"}`,
			); err != nil {
				t.Fatalf("known-safe command should not require approval: %v", err)
			}
			if !called {
				t.Fatal("handler was not reached")
			}
		})
	}
}

// A replay that hits a second gate must suspend again, for a child as much as
// for the primary agent. The child's gate used to be rewritten into
// "operation requires approval which is not available in subagent context" —
// a refusal that stopped being true once children began suspending on the same
// action queue, and that fired on the replay a granted approval had just
// started, so a worker was told its request was impossible immediately after
// the user had answered one.
func TestSubagentReplaySecondGateSuspendsInsteadOfRefusing(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "gated", Destructive: true})
	st.SetActionHook(func(context.Context, string, any) (string, bool, error) {
		return "act", true, nil
	})
	type gatedInput struct {
		N int `json:"n"`
	}
	gated, err := llm.NewTool("gated", "gated tool", func(ctx context.Context, in *gatedInput) (string, error) {
		actionID := fmt.Sprintf("act-%d", in.N)
		if toolpkg.ApprovedActionIDFromContext(ctx) == actionID {
			return "ok", nil
		}
		return "", &toolpkg.RequiresActionError{
			ActionID: actionID, ActionKind: "gated", ToolName: "gated", ToolInput: in,
		}
	})
	if err != nil {
		t.Fatalf("tool: %v", err)
	}

	assistant := llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
		{ID: "call-1", Function: llm.FunctionCall{Name: "gated", Arguments: `{"n":1}`}},
		{ID: "call-2", Function: llm.FunctionCall{Name: "gated", Arguments: `{"n":2}`}},
	}}
	snapshot := []llm.Message{
		llm.UserMessage(llm.Text("investigate")),
		assistant,
		llm.ToolResultMessage("call-1", llm.Text("ok")),
	}

	for name, base := range map[string]context.Context{
		"typed subagent": toolpkg.WithSubagentType(context.Background(), "plan"),
		"fork child":     toolpkg.WithForkChild(context.Background(), true),
	} {
		ctx := toolpkg.WithApprovedActionID(base, "act-1")
		ctx = toolpkg.WithToolApprovalResume(ctx, &toolpkg.ToolApprovalResumeState{Session: snapshot})

		wrapped := wrapToolOrchestrationLLM(noopLLM{}, st)
		_, err := wrapped.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("ignored"))}, []*llm.Tool{gated})
		var rae *toolpkg.RequiresActionError
		if !errors.As(err, &rae) || rae == nil {
			t.Fatalf("%s: the second gate must suspend the run, got %v", name, err)
		}
		if rae.ActionID != "act-2" {
			t.Fatalf("%s: action id = %q, want the second call's", name, rae.ActionID)
		}
		if len(rae.SessionSnapshot) == 0 {
			t.Fatalf("%s: the resume needs the child's session snapshot", name)
		}
	}
}

// The action row is the only thing a web user answering an approval sees, and
// three fanout children can be waiting on them at once. The identity of the
// worker asking therefore travels on the action itself, not only on the wait
// row — which a surface answering the gate in place releases before asking.
func TestActionHookRecordsTheRequestingSubagentOnTheAction(t *testing.T) {
	project, target := outsideWorkspaceFile(t)
	r, st := outsideWorkspaceRunner(t, project, safety.ApprovalOnRequest)
	ctx := llm.WithAgentSessionID(
		toolpkg.WithHookAgentID(toolpkg.WithSubagentType(context.Background(), "general-purpose"), "task-1"),
		"session-1",
	)

	_, err := editOutsideWorkspace(t, r, st, ctx, target)
	var req *toolpkg.RequiresActionError
	if !errors.As(err, &req) {
		t.Fatalf("an edit outside the workspace must ask, got %v", err)
	}
	action, err := r.Actions.Get(context.Background(), req.ActionID)
	if err != nil || action == nil {
		t.Fatalf("pending action: %v", err)
	}
	agentID, subagentType := turn.ActionSubagent(action)
	if agentID != "task-1" || subagentType != "general-purpose" {
		t.Fatalf("action must name the worker asking, got agent=%q type=%q", agentID, subagentType)
	}
	if sid := action.SessionID; sid != "session-1" {
		t.Fatalf("action session = %q, want the dispatching conversation", sid)
	}
}

// gatedThenPlainToolLLM asks for the gated tool, and once the resume has
// replayed it, asks for one ordinary tool before answering. The second tool is
// what makes the fence's closing point observable: it is work the turn does
// after the continuation is over.
type gatedThenPlainToolLLM struct {
	calls int
}

func (m *gatedThenPlainToolLLM) Execute(_ context.Context, _ []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	m.calls++
	switch m.calls {
	case 1:
		msg := llm.AssistantMessage(nil,
			llm.ToolCall{ID: "call-1", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "gated", Arguments: `{}`}},
		)
		return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
	case 2:
		msg := llm.AssistantMessage(nil,
			llm.ToolCall{ID: "call-2", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "later", Arguments: `{}`}},
		)
		return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
	default:
		msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("answer after approval")})
		return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
	}
}

// The durable fence answers exactly one question — "is the approved tool call
// running right now?" — so it must open immediately before the replay and close
// immediately after it. Held open across the rest of the turn, every later exit
// looked like a tool whose outcome nobody could know, and the next session was
// greeted with an interrupted approval that had in fact completed.
func TestApprovalContinuationFenceBracketsOnlyTheReplay(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "gated", Destructive: true})
	st.SetActionHook(func(context.Context, string, any) (string, bool, error) { return "act-1", true, nil })

	var events []string
	gated, err := llm.NewTool("gated", "gated tool", func(ctx context.Context, _ *struct{}) (string, error) {
		if toolpkg.ApprovedActionIDFromContext(ctx) == "" {
			return "", &toolpkg.RequiresActionError{ActionID: "act-1", ActionKind: "gated", ToolName: "gated", ToolInput: map[string]any{}}
		}
		events = append(events, "gated")
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("gated tool: %v", err)
	}
	later, err := llm.NewTool("later", "ordinary tool", func(context.Context, *struct{}) (string, error) {
		events = append(events, "later")
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("later tool: %v", err)
	}

	inner := &gatedThenPlainToolLLM{}
	wrapped := wrapToolOrchestrationLLM(inner, st)
	_, err = wrapped.Execute(context.Background(), []llm.Message{llm.UserMessage(llm.Text("hi"))}, []*llm.Tool{gated, later})
	var rae *toolpkg.RequiresActionError
	if !errors.As(err, &rae) || rae == nil {
		t.Fatalf("expected the gate to fire, got %T %v", err, err)
	}

	ctx := toolpkg.WithApprovedActionID(context.Background(), "act-1")
	ctx = toolpkg.WithToolApprovalResume(ctx, &toolpkg.ToolApprovalResumeState{
		Session:           rae.SessionSnapshot,
		BeginContinuation: func(context.Context) error { events = append(events, "begin"); return nil },
		EndContinuation:   func(context.Context) { events = append(events, "end") },
	})
	if _, rerr := wrapped.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("ignored"))}, []*llm.Tool{gated, later}); rerr != nil {
		t.Fatalf("resume Execute: %v", rerr)
	}

	want := []string{"begin", "gated", "end", "later"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("fence window = %v, want %v", events, want)
	}
}

// A denial replays nothing, so the fence is never crossed — but the
// continuation is just as over, and the wait row it stands for must go.
func TestDeniedApprovalClosesTheContinuationWithoutCrossingTheFence(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	st.RegisterToolMeta(event.ToolMeta{Name: "gated", Destructive: true})
	st.SetActionHook(func(context.Context, string, any) (string, bool, error) { return "act-1", true, nil })

	gated, err := llm.NewTool("gated", "gated tool", func(ctx context.Context, _ *struct{}) (string, error) {
		if toolpkg.ApprovedActionIDFromContext(ctx) == "" {
			return "", &toolpkg.RequiresActionError{ActionID: "act-1", ActionKind: "gated", ToolName: "gated", ToolInput: map[string]any{}}
		}
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("gated tool: %v", err)
	}
	inner := &approvalGatedSingleToolLLM{}
	wrapped := wrapToolOrchestrationLLM(inner, st)
	_, err = wrapped.Execute(context.Background(), []llm.Message{llm.UserMessage(llm.Text("hi"))}, []*llm.Tool{gated})
	var rae *toolpkg.RequiresActionError
	if !errors.As(err, &rae) || rae == nil {
		t.Fatalf("expected the gate to fire, got %T %v", err, err)
	}

	began, ended := false, false
	ctx := toolpkg.WithToolApprovalResume(context.Background(), &toolpkg.ToolApprovalResumeState{
		Session:           rae.SessionSnapshot,
		Denied:            true,
		BeginContinuation: func(context.Context) error { began = true; return nil },
		EndContinuation:   func(context.Context) { ended = true },
	})
	if _, rerr := wrapped.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("ignored"))}, []*llm.Tool{gated}); rerr != nil {
		t.Fatalf("resume Execute: %v", rerr)
	}
	if began {
		t.Fatal("a denial executes nothing, so it must not cross the execution fence")
	}
	if !ended {
		t.Fatal("a denial ends the continuation and must release its wait")
	}
}

// planModeCancelledDenialLLM answers the gate-raising call, then cancels the
// resume call the way pressing esc does while the model is still thinking.
type planModeCancelledDenialLLM struct {
	mu       sync.Mutex
	calls    int
	sessions [][]llm.Message
}

func (m *planModeCancelledDenialLLM) Execute(_ context.Context, messages []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	m.sessions = append(m.sessions, append([]llm.Message(nil), messages...))
	if m.calls == 1 {
		msg := llm.AssistantMessage(nil,
			llm.ToolCall{ID: "call-1", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "exit_plan_mode", Arguments: `{}`}},
		)
		return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
	}
	return nil, context.Canceled
}

// TestCancelledDeniedResumeKeepsFeedbackNextToItsCall is the regression for a
// denied exit_plan_mode whose resume the user cancels immediately: the feedback
// they typed into the denial must reach the transcript directly behind the
// assistant tool_calls row it answers.
//
// A plan-mode reminder is injected into that resume call. It used to be written
// to the transcript from inside the call, landing between the gate's stored
// assistant tool_calls row and the denial result the cancel path stores
// afterwards; RepairDanglingToolResults then read it as the end of an
// unanswered batch and excluded both rows, so the next turn's context lost the
// user's feedback entirely. The reminder now travels in the session instead,
// and a cancelled call contributes none of it.
func TestCancelledDeniedResumeKeepsFeedbackNextToItsCall(t *testing.T) {
	stateRoot := t.TempDir()
	sid := "denied-cancel"
	setMode(t, stateRoot, sid, state.ModePlan, 0)

	st := toolpkg.NewState(t.TempDir())
	st.SetActionHook(func(context.Context, string, any) (string, bool, error) {
		return "act-1", true, nil
	})
	gated, err := llm.NewTool("exit_plan_mode", "exit plan mode", func(ctx context.Context, _ *struct{}) (string, error) {
		if toolpkg.ApprovedActionIDFromContext(ctx) == "" {
			return "", &toolpkg.RequiresActionError{
				ActionID:   "act-1",
				ActionKind: "exit_plan_mode",
				ToolName:   "exit_plan_mode",
				ToolInput:  map[string]any{},
			}
		}
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("tool: %v", err)
	}

	inner := &planModeCancelledDenialLLM{}
	wrapped := wrapToolOrchestrationLLM(wrapPlanModeLLM(inner, stateRoot), st)
	baseCtx := toolpkg.WithRunID(llm.WithAgentSessionID(context.Background(), sid), "run-denied")

	_, err = wrapped.Execute(baseCtx, []llm.Message{llm.UserMessage(llm.Text("plan it"))}, []*llm.Tool{gated})
	var rae *toolpkg.RequiresActionError
	if !errors.As(err, &rae) || rae == nil {
		t.Fatalf("expected RequiresActionError, got %T %v", err, err)
	}

	feedback := "不需要另外部署 redis 和 postgres 组件"
	capture := NewPartialSessionCapture()
	resumeCtx := WithPartialSessionCapture(
		toolpkg.WithToolApprovalResume(baseCtx, &toolpkg.ToolApprovalResumeState{
			Session:    rae.SessionSnapshot,
			Denied:     true,
			DenyReason: feedback,
		}),
		capture,
	)
	if _, rerr := wrapped.Execute(resumeCtx, []llm.Message{llm.UserMessage(llm.Text("plan it"))}, []*llm.Tool{gated}); !errors.Is(rerr, context.Canceled) {
		t.Fatalf("resume Execute err=%v want context.Canceled", rerr)
	}

	// The model did see the reminder, after the denial result.
	if inner.calls != 2 {
		t.Fatalf("expected 2 LLM calls, got %d", inner.calls)
	}
	reminders := 0
	for _, msg := range inner.sessions[1] {
		if msg.IsMeta && strings.Contains(llm.TextContent(msg.Parts...), "PLAN MODE ACTIVE") {
			reminders++
		}
	}
	if reminders != 1 {
		t.Fatalf("resume request carried %d plan reminders, want exactly 1", reminders)
	}

	// What a cancel persists must pair the call with its answer, with nothing
	// in between and no reminder trailing it.
	snapshot := capture.Snapshot()
	if len(snapshot) < 2 {
		t.Fatalf("partial capture too short: %#v", snapshot)
	}
	answer := snapshot[len(snapshot)-1]
	call := snapshot[len(snapshot)-2]
	if answer.Role != llm.RoleTool || answer.ToolCallID != "call-1" {
		t.Fatalf("last captured message must be the denial result, got %#v", answer)
	}
	if text := llm.TextContent(answer.Parts...); !strings.Contains(text, feedback) {
		t.Fatalf("denial result lost the user's feedback: %q", text)
	}
	if call.Role != llm.RoleAssistant || len(call.ToolCalls) == 0 || call.ToolCalls[0].ID != "call-1" {
		t.Fatalf("denial result must sit directly behind its assistant tool_calls row, got %#v", call)
	}
	// The reminder the first (completed) call injected is persisted where the
	// model saw it - ahead of the assistant row - and the cancelled call
	// contributed no second copy behind it.
	captured := 0
	for i, msg := range snapshot {
		if !msg.IsMeta || !strings.Contains(llm.TextContent(msg.Parts...), "PLAN MODE ACTIVE") {
			continue
		}
		captured++
		if i >= len(snapshot)-2 {
			t.Fatalf("a reminder must never sit between a tool call and its result: index %d of %d", i, len(snapshot))
		}
	}
	if captured != 1 {
		t.Fatalf("captured %d plan reminders, want exactly 1", captured)
	}
}

// skillLoadingLLM asks for one skill by name, then finishes.
type skillLoadingLLM struct {
	mu        sync.Mutex
	calls     int
	skillName string
	results   []string
}

func (m *skillLoadingLLM) Execute(_ context.Context, messages []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, msg := range messages {
		if msg.Role == llm.RoleTool {
			m.results = append(m.results, msg.TextContent())
		}
	}
	m.calls++
	if m.calls == 1 {
		msg := llm.AssistantMessage(nil, llm.ToolCall{
			ID:   "call-skill",
			Type: llm.ToolTypeFunction,
			Function: llm.FunctionCall{
				Name:      "skill",
				Arguments: `{"name":"` + m.skillName + `"}`,
			},
		})
		return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
	}
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("done")})
	return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
}

// TestSkillToolLoadIsAnUnpromptedSkillStepEndToEnd walks the whole path the
// catalog now promises: the model names a skill, the load runs without an
// approval gate, the instructions come back in the tool result, and the step
// reports the same skill identity an explicit invocation does — differing only
// in the origin the ledger records.
func TestSkillToolLoadIsAnUnpromptedSkillStepEndToEnd(t *testing.T) {
	home := t.TempDir()
	skillRoot := filepath.Join(home, "skills", "golang-testing")
	if err := os.MkdirAll(skillRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillRoot, "SKILL.md"), []byte("---\nname: golang-testing\ndescription: Write Go tests\n---\nUse table-driven tests."), 0o644); err != nil {
		t.Fatal(err)
	}
	// Skill paths are canonical everywhere they are compared — a skill reached
	// by two spellings is still one skill — so the expectation resolves too.
	if resolved, err := filepath.EvalSymlinks(skillRoot); err == nil {
		skillRoot = resolved
	}
	skillFile := filepath.Join(skillRoot, "SKILL.md")
	st := toolpkg.NewState(home)
	st.ReplaceLoadedSkillCatalog([]toolpkg.LoadedSkill{{Name: "golang-testing", RootDir: skillRoot}})
	var steps []toolpkg.StepEvent
	st.SetStepHook(func(_ context.Context, evt toolpkg.StepEvent) {
		if evt.ToolName == "skill" {
			steps = append(steps, evt)
		}
	})

	mainAgent, err := agent.New(noopLLM{}, "main", "main")
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterSkillTool(mainAgent, st); err != nil {
		t.Fatal(err)
	}
	tools := debuglogToolsView(mainAgent)

	inner := &skillLoadingLLM{skillName: "golang-testing"}
	runner := &Runner{Deps: &Deps{Home: home, AppCfg: &appcfg.Root{}}, tools: st}
	wrapped := runner.wrapExecutionLLM(inner, false)
	ctx := llm.WithAgentSessionID(context.Background(), "s-skill")
	ctx = toolpkg.WithMode(ctx, "agent")
	ctx = WithTurnInputRuntime(ctx, NewTurnInputRuntime())
	res, err := wrapped.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("write a test"))}, tools)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res == nil || res.Message == nil || res.Message.TextContent() != "done" {
		t.Fatalf("unexpected result: %#v", res)
	}
	// The instructions themselves reach the model, not a description of them.
	if len(inner.results) == 0 {
		t.Fatal("the skill load produced no tool result")
	}
	loaded := strings.Join(inner.results, "\n")
	for _, want := range []string{"<name>golang-testing</name>", skillFile, "Use table-driven tests."} {
		if !strings.Contains(loaded, want) {
			t.Fatalf("tool result is missing %q:\n%s", want, loaded)
		}
	}
	if len(steps) != 2 {
		t.Fatalf("steps=%+v, want a started and a completed", steps)
	}
	for _, evt := range steps {
		if evt.Category != "skill" || evt.SkillName != "golang-testing" || evt.SkillPath != skillFile {
			t.Fatalf("step is not a skill step: %+v", evt)
		}
		if evt.Origin != "llm-load" {
			t.Fatalf("origin=%q, want the model's own load", evt.Origin)
		}
		if strings.TrimSpace(evt.ActionID) != "" {
			t.Fatalf("loading a listed skill must not raise an approval gate: %+v", evt)
		}
	}
	if strings.TrimSpace(steps[1].Error) != "" {
		t.Fatalf("load failed: %s", steps[1].Error)
	}
}
