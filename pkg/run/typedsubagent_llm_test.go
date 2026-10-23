package run

import (
	"context"
	"errors"
	"strings"
	"testing"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	toolpkg "github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// recordingLLM captures the calls made to it so tests can assert which
// underlying LLM received the Execute call.
type recordingLLM struct {
	name     string
	calls    int
	execErr  error
	messages []llm.Message
	tools    []*llm.Tool
}

func (r *recordingLLM) Execute(_ context.Context, messages []llm.Message, tools []*llm.Tool) (*llm.Result, error) {
	if r.execErr != nil {
		return nil, r.execErr
	}
	r.calls++
	r.messages = append([]llm.Message(nil), messages...)
	r.tools = append([]*llm.Tool(nil), tools...)
	return &llm.Result{Message: &llm.Message{Role: llm.RoleAssistant, Parts: []llm.ContentPart{llm.Text(r.name)}}}, nil
}

func TestTypedSubagentProviderRoutesToTypedLLM(t *testing.T) {
	inner := &recordingLLM{name: "inner"}
	exploreLLM := &recordingLLM{name: "explore-llm"}
	typeLLMs := map[string]llm.LLM{"explore": exploreLLM}

	wrapped := wrapTypedSubagentProviderLLM(inner, typeLLMs)
	ctx := toolpkg.WithSubagentType(context.Background(), "explore")

	res, err := wrapped.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("find x"))}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if exploreLLM.calls != 1 {
		t.Fatalf("explore LLM should have been called once, got %d", exploreLLM.calls)
	}
	if inner.calls != 0 {
		t.Fatalf("inner LLM should not have been called, got %d", inner.calls)
	}
	if res == nil || res.Message == nil {
		t.Fatal("nil result")
	}
	if got := res.Message.TextContent(); got != "explore-llm" {
		t.Fatalf("expected response from explore LLM, got %q", got)
	}
}

func TestTypedSubagentProviderFallsBackToInnerForUnknownType(t *testing.T) {
	inner := &recordingLLM{name: "inner"}
	exploreLLM := &recordingLLM{name: "explore-llm"}
	typeLLMs := map[string]llm.LLM{"explore": exploreLLM}

	wrapped := wrapTypedSubagentProviderLLM(inner, typeLLMs)
	// "plan" is not in the typeLLMs map, so it should fall back to inner.
	ctx := toolpkg.WithSubagentType(context.Background(), "plan")

	_, err := wrapped.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("plan x"))}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if inner.calls != 1 {
		t.Fatalf("inner LLM should have been called once, got %d", inner.calls)
	}
	if exploreLLM.calls != 0 {
		t.Fatalf("explore LLM should not have been called, got %d", exploreLLM.calls)
	}
}

func TestTypedSubagentProviderFallsBackToInnerForNoType(t *testing.T) {
	inner := &recordingLLM{name: "inner"}
	typeLLMs := map[string]llm.LLM{"explore": &recordingLLM{name: "explore-llm"}}

	wrapped := wrapTypedSubagentProviderLLM(inner, typeLLMs)
	// No subagent type in context (fork subagent path).
	_, err := wrapped.Execute(context.Background(), []llm.Message{llm.UserMessage(llm.Text("work"))}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if inner.calls != 1 {
		t.Fatalf("inner LLM should have been called once, got %d", inner.calls)
	}
}

func TestTypedSubagentProviderUsesInnerForMainThread(t *testing.T) {
	inner := &recordingLLM{name: "inner"}
	exploreLLM := &recordingLLM{name: "explore-llm"}
	typeLLMs := map[string]llm.LLM{"explore": exploreLLM}

	wrapped := wrapTypedSubagentProviderLLM(inner, typeLLMs)
	// Main-thread queries should always use inner, even when a subagent type is set.
	ctx := WithQuerySource(context.Background(), "repl_main_thread")
	ctx = toolpkg.WithSubagentType(ctx, "explore")

	_, err := wrapped.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("main work"))}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if inner.calls != 1 {
		t.Fatalf("inner LLM should have been called for main thread, got %d", inner.calls)
	}
	if exploreLLM.calls != 0 {
		t.Fatalf("explore LLM should not have been called for main thread, got %d", exploreLLM.calls)
	}
}

func TestTypedSubagentProviderNoopWhenNoTypeLLMs(t *testing.T) {
	inner := &recordingLLM{name: "inner"}
	// Empty map -> wrapper should be a no-op and return inner directly.
	wrapped := wrapTypedSubagentProviderLLM(inner, nil)
	if wrapped != inner {
		t.Fatalf("expected nil typeLLMs to return inner directly, got %T", wrapped)
	}

	// Non-empty map but untyped context still falls through to inner.
	exploreLLM := &recordingLLM{name: "explore-llm"}
	wrapped = wrapTypedSubagentProviderLLM(inner, map[string]llm.LLM{"explore": exploreLLM})
	ctx := toolpkg.WithSubagentType(context.Background(), "explore")
	_, err := wrapped.Execute(ctx, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if exploreLLM.calls != 1 {
		t.Fatalf("explore LLM should have been called, got %d", exploreLLM.calls)
	}
}

func TestTypedSubagentProviderPropagatesTypedLLMError(t *testing.T) {
	inner := &recordingLLM{name: "inner"}
	testErr := errors.New("provider unavailable")
	exploreLLM := &recordingLLM{name: "explore-llm", execErr: testErr}
	typeLLMs := map[string]llm.LLM{"explore": exploreLLM}

	wrapped := wrapTypedSubagentProviderLLM(inner, typeLLMs)
	ctx := toolpkg.WithSubagentType(context.Background(), "explore")

	_, err := wrapped.Execute(ctx, nil, nil)
	if !errors.Is(err, testErr) {
		t.Fatalf("expected error %v, got %v", testErr, err)
	}
	if inner.calls != 0 {
		t.Fatalf("inner LLM should not have been called on error, got %d", inner.calls)
	}
}

func TestTypedSubagentProviderRoutesMultipleTypes(t *testing.T) {
	inner := &recordingLLM{name: "inner"}
	exploreLLM := &recordingLLM{name: "explore-llm"}
	planLLM := &recordingLLM{name: "plan-llm"}
	verificationLLM := &recordingLLM{name: "verification-llm"}
	generalLLM := &recordingLLM{name: "general-purpose-llm"}

	typeLLMs := map[string]llm.LLM{
		"explore":         exploreLLM,
		"plan":            planLLM,
		"verification":    verificationLLM,
		"general-purpose": generalLLM,
	}
	wrapped := wrapTypedSubagentProviderLLM(inner, typeLLMs)

	for _, tc := range []struct {
		subtype string
		target  *recordingLLM
	}{
		{"explore", exploreLLM},
		{"plan", planLLM},
		{"verification", verificationLLM},
		{"general-purpose", generalLLM},
	} {
		ctx := toolpkg.WithSubagentType(context.Background(), tc.subtype)
		_, err := wrapped.Execute(ctx, nil, nil)
		if err != nil {
			t.Fatalf("Execute for %s: %v", tc.subtype, err)
		}
		if tc.target.calls != 1 {
			t.Fatalf("%s LLM should have been called once, got %d", tc.subtype, tc.target.calls)
		}
	}
	if inner.calls != 0 {
		t.Fatalf("inner LLM should not have been called, got %d", inner.calls)
	}
}

func TestTypedSubagentProviderNilInner(t *testing.T) {
	wrapped := wrapTypedSubagentProviderLLM(nil, map[string]llm.LLM{"explore": &recordingLLM{name: "e"}})
	if wrapped != nil {
		t.Fatalf("nil inner should return nil, got %T", wrapped)
	}
}

// TestTypedSubagentProviderMessagesPassedThrough verifies that messages and
// tools are forwarded unmodified to the per-type LLM (the prompt/tool-filter
// wrappers apply on top, not inside).
func TestTypedSubagentProviderMessagesPassedThrough(t *testing.T) {
	inner := &recordingLLM{name: "inner"}
	exploreLLM := &recordingLLM{name: "explore-llm"}
	typeLLMs := map[string]llm.LLM{"explore": exploreLLM}

	wrapped := wrapTypedSubagentProviderLLM(inner, typeLLMs)
	msgs := []llm.Message{
		llm.SystemMessage("custom system"),
		llm.UserMessage(llm.Text("find the bug")),
	}
	tool := mustNamedTool(t, "read_file")
	tools := []*llm.Tool{tool}

	ctx := toolpkg.WithSubagentType(context.Background(), "explore")
	_, err := wrapped.Execute(ctx, msgs, tools)
	if err != nil {
		t.Fatal(err)
	}
	if len(exploreLLM.messages) != 2 {
		t.Fatalf("expected 2 messages forwarded, got %d", len(exploreLLM.messages))
	}
	if got := exploreLLM.messages[0].TextContent(); !strings.Contains(got, "custom system") {
		t.Fatalf("system prompt not forwarded: %q", got)
	}
	if len(exploreLLM.tools) != 1 {
		t.Fatalf("expected 1 tool forwarded, got %d", len(exploreLLM.tools))
	}
}

type captureToolsLLM struct{ tools []*llm.Tool }

func (c *captureToolsLLM) Execute(_ context.Context, _ []llm.Message, tools []*llm.Tool) (*llm.Result, error) {
	c.tools = append([]*llm.Tool(nil), tools...)
	return &llm.Result{Message: &llm.Message{Role: llm.RoleAssistant}}, nil
}

func TestTypedSubagentToolFilterExploreUsesSingleShellTool(t *testing.T) {
	inner := &captureToolsLLM{}
	wrapped := wrapTypedSubagentToolFilterLLM(inner)
	ctx := toolpkg.WithSubagentType(context.Background(), "explore")
	tools := []*llm.Tool{mustNamedTool(t, "read_file"), mustNamedTool(t, "write_file"), mustNamedTool(t, "shell")}
	if _, err := wrapped.Execute(ctx, nil, tools); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	names := toolNamesOnly(inner.tools)
	if containsName(names, "write_file") || !containsName(names, "read_file") || !containsName(names, "shell") {
		t.Fatalf("explore visible tools mismatch: %v", names)
	}
}

func TestTypedSubagentToolFilterPlanUsesSingleShellTool(t *testing.T) {
	inner := &captureToolsLLM{}
	wrapped := wrapTypedSubagentToolFilterLLM(inner)
	ctx := toolpkg.WithSubagentType(context.Background(), "plan")
	tools := []*llm.Tool{mustNamedTool(t, "read_file"), mustNamedTool(t, "shell")}
	if _, err := wrapped.Execute(ctx, nil, tools); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	names := toolNamesOnly(inner.tools)
	if !containsName(names, "shell") || !containsName(names, "read_file") {
		t.Fatalf("plan tool pool: %v", names)
	}
}

func TestEffectiveToolsForContextAppliesSubtypeAndKeepsMainThreadTools(t *testing.T) {
	allTools := []*llm.Tool{mustNamedTool(t, "read_file"), mustNamedTool(t, "shell"), mustNamedTool(t, "write_file"), mustNamedTool(t, "subagent_send")}
	explore := effectiveToolsForContext(toolpkg.WithSubagentType(context.Background(), "explore"), allTools)
	if !containsName(toolNamesOnly(explore), "shell") || containsName(toolNamesOnly(explore), "write_file") {
		t.Fatalf("explore effective pool: %v", toolNamesOnly(explore))
	}
	main := effectiveToolsForContext(WithQuerySource(context.Background(), "repl_main_thread"), allTools)
	for _, want := range []string{"read_file", "shell", "write_file", "subagent_send"} {
		if !containsName(toolNamesOnly(main), want) {
			t.Fatalf("main thread missing %s: %v", want, toolNamesOnly(main))
		}
	}
}

func mustNamedTool(t *testing.T, name string) *llm.Tool {
	t.Helper()
	tool, err := llm.NewTool(name, "test tool", func(context.Context, *struct{}) (string, error) { return "ok", nil })
	if err != nil {
		t.Fatalf("NewTool(%s): %v", name, err)
	}
	return tool
}
func toolNamesOnly(in []*llm.Tool) []string {
	out := make([]string, 0, len(in))
	for _, tool := range in {
		if tool != nil {
			out = append(out, tool.Name())
		}
	}
	return out
}
func containsName(in []string, want string) bool {
	for _, name := range in {
		if name == want {
			return true
		}
	}
	return false
}

func TestTypedSubagentPromptReplacesSystemPrompt(t *testing.T) {
	inner := &captureLLM{}
	wrapped := wrapTypedSubagentPromptLLM(inner, nil)
	ctx := toolpkg.WithSubagentType(context.Background(), "plan")
	_, err := wrapped.Execute(ctx, []llm.Message{
		llm.SystemMessage("default system"),
		llm.UserMessage(llm.Text("work")),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(inner.messages) == 0 {
		t.Fatal("no messages captured")
	}
	got := inner.messages[0].TextContent()
	if !strings.Contains(strings.ToLower(got), "plan subagent") {
		t.Fatalf("typed prompt not applied: %q", got)
	}
}

func TestTypedSubagentPromptNoopForMainContext(t *testing.T) {
	inner := &captureLLM{}
	wrapped := wrapTypedSubagentPromptLLM(inner, nil)
	_, err := wrapped.Execute(WithQuerySource(context.Background(), "repl_main_thread"), []llm.Message{
		llm.SystemMessage("default system"),
		llm.UserMessage(llm.Text("work")),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := inner.messages[0].TextContent(); got != "default system" {
		t.Fatalf("unexpected replacement in main context: %q", got)
	}
}

func TestTypedSubagentPromptDoesNotOverrideMainThreadPrompt(t *testing.T) {
	inner := &captureLLM{}
	wrapped := wrapTypedSubagentPromptLLM(inner, nil)
	ctx := WithQuerySource(context.Background(), "repl_main_thread")
	ctx = toolpkg.WithSubagentType(ctx, "plan")
	_, err := wrapped.Execute(ctx, []llm.Message{
		llm.SystemMessage("default system"),
		llm.UserMessage(llm.Text("work")),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := inner.messages[0].TextContent(); got != "default system" {
		t.Fatalf("main thread system prompt unexpectedly overridden: %q", got)
	}
}

// A surface that draws "which model is this subagent on" must resolve it the
// same way the runtime routes the call, or the footer claims a model nothing is
// running. SubagentOwnModel answers for the types the runtime builds a client
// for — the ones with their own agents.definitions[<type>].llm_providers chain —
// and reports false for every other type, which falls through to the primary.
func TestSubagentOwnModelMatchesWhatTheRuntimeRoutes(t *testing.T) {
	cfg := &appcfg.Root{}
	cfg.Agents.Definitions = map[string]appcfg.AgentDefinition{
		"main": {LLMProviders: []appcfg.AgentLLMProviderConfig{{
			Provider: "deepseek", Model: "deepseek-v4-flash",
			Params: appcfg.LLMRequestParams(`{"reasoning":{"effort":"medium"}}`),
		}}},
		"explore": {LLMProviders: []appcfg.AgentLLMProviderConfig{{
			Provider: "openai", Model: "gpt-4.1-mini",
			Params: appcfg.LLMRequestParams(`{"reasoning":{"effort":"low"}}`),
		}}},
		// Configured but not a built-in type: the runtime never builds a client
		// for it, so neither may the display.
		"reviewer": {LLMProviders: []appcfg.AgentLLMProviderConfig{{
			Provider: "openai", Model: "gpt-4.1",
		}}},
	}
	r := &Runner{Deps: &Deps{AppCfg: cfg}}

	provider, model, effort, ok := SubagentOwnModel(r, "explore")
	if !ok || provider != "openai" || model != "gpt-4.1-mini" || effort != "low" {
		t.Fatalf("explore = (%q, %q, %q, %v)", provider, model, effort, ok)
	}
	// plan is a built-in type with no chain of its own: it runs on the primary
	// agent's model, which the caller already knows.
	if _, _, _, ok := SubagentOwnModel(r, "plan"); ok {
		t.Fatal("a built-in type with no providers must report no model of its own")
	}
	if _, _, _, ok := SubagentOwnModel(r, "reviewer"); ok {
		t.Fatal("a non-built-in definition must not be presented as a subagent model")
	}
	if _, _, _, ok := SubagentOwnModel(r, ""); ok {
		t.Fatal("an unnamed type has no model of its own")
	}
	if _, _, _, ok := SubagentOwnModel(nil, "explore"); ok {
		t.Fatal("a nil runner has no model of its own")
	}
}

// typedPromptTestContext is a typed run's context with both roots known, so the
// prompt wrapper appends the file access scope the way a real run does.
func typedPromptTestContext(subtype string) context.Context {
	ctx := toolpkg.WithSubagentType(context.Background(), subtype)
	ctx = toolpkg.WithProjectRoot(ctx, "/work/project")
	return toolpkg.WithWorkspaceRoot(ctx, "/home/user/.forebrain/workspace")
}

// A rewrite of a built-in subagent's prompt is what the model actually receives:
// it replaces the built-in text whole, and the engine still appends the file
// access scope after it.
func TestTypedSubagentPromptUsesTheAgentsRewrite(t *testing.T) {
	inner := &captureLLM{}
	wrapped := wrapTypedSubagentPromptLLM(inner, SubagentCatalogFor(catalogTestConfig(), "main").Prompt)
	_, err := wrapped.Execute(typedPromptTestContext("explore"), []llm.Message{
		llm.SystemMessage("default system"),
		llm.UserMessage(llm.Text("find x")),
	}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(inner.messages) == 0 {
		t.Fatal("no messages captured")
	}
	got := inner.messages[0].TextContent()
	if !strings.HasPrefix(got, "Search only what the task names.") {
		t.Fatalf("the rewrite did not reach the model: %q", got)
	}
	if !strings.Contains(got, "FILE ACCESS SCOPE") {
		t.Fatalf("the file access scope was dropped from the rewritten prompt: %q", got)
	}
}

// A custom subagent's own prompt is the one it runs under, by the same path.
func TestTypedSubagentPromptUsesCustomPrompt(t *testing.T) {
	inner := &captureLLM{}
	wrapped := wrapTypedSubagentPromptLLM(inner, SubagentCatalogFor(catalogTestConfig(), "main").Prompt)
	_, err := wrapped.Execute(typedPromptTestContext("repo-scout"), []llm.Message{
		llm.SystemMessage("default system"),
		llm.UserMessage(llm.Text("find the files")),
	}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(inner.messages) == 0 {
		t.Fatal("no messages captured")
	}
	got := inner.messages[0].TextContent()
	if !strings.HasPrefix(got, "You are repo-scout.") {
		t.Fatalf("the custom prompt did not reach the model: %q", got)
	}
	if !strings.Contains(got, "FILE ACCESS SCOPE") {
		t.Fatalf("the file access scope was dropped from the custom prompt: %q", got)
	}
}

// A read-only custom subagent is held to its policy on both paths that matter:
// the write tools are refused when something calls them anyway, and they are
// never offered to the model in the first place.
func TestCustomReadOnlySubagentCannotWrite(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	ctx := toolpkg.WithSubagentType(context.Background(), "repo-scout")
	ctx = toolpkg.WithSubagentToolPolicy(ctx, toolpkg.SubagentToolPolicy{Mode: appcfg.SubagentToolsReadOnly})

	err := st.GuardTool(ctx, "write_file")
	if err == nil {
		t.Fatal("a read-only custom subagent may call write_file")
	}
	if !strings.Contains(err.Error(), "blocked by policy") {
		t.Fatalf("refusal = %v", err)
	}
	if err := st.GuardTool(ctx, "read_file"); err != nil {
		t.Fatalf("read_file refused: %v", err)
	}
	tools := []*llm.Tool{testTool(t, "read_file"), testTool(t, "write_file"), testTool(t, "edit_file")}
	for _, candidate := range filterToolsForSubagentSubtype(ctx, "repo-scout", tools) {
		if name := candidate.Name(); name == "write_file" || name == "edit_file" {
			t.Fatalf("%s was offered to a read-only custom subagent", name)
		}
	}
}

// A pick-mode custom subagent sees exactly the tools it was configured with, and
// the subagent_* family is never one of them: a delegated task must not become a
// tree of them.
func TestCustomPickSubagentSeesOnlyChosenTools(t *testing.T) {
	ctx := toolpkg.WithSubagentType(context.Background(), "repo-scout")
	ctx = toolpkg.WithSubagentToolPolicy(ctx, toolpkg.SubagentToolPolicy{
		Mode:    appcfg.SubagentToolsPick,
		Allowed: map[string]struct{}{"read_file": {}, "code_search": {}},
	})
	tools := []*llm.Tool{
		testTool(t, "read_file"), testTool(t, "code_search"), testTool(t, "write_file"), testTool(t, "subagent_run"),
	}
	var names []string
	for _, candidate := range filterToolsForSubagentSubtype(ctx, "repo-scout", tools) {
		names = append(names, candidate.Name())
	}
	if got := strings.Join(names, ","); got != "read_file,code_search" {
		t.Fatalf("visible tools = %q, want the chosen two", got)
	}
}

// A custom run that lost its policy is offered no tool at all.
func TestCustomSubagentWithoutPolicySeesNoTools(t *testing.T) {
	ctx := toolpkg.WithSubagentType(context.Background(), "repo-scout")
	ctx = toolpkg.WithSubagentDefinitionSource(ctx, toolpkg.SubagentDefinitionSourceCustom)
	tools := []*llm.Tool{testTool(t, "read_file"), testTool(t, "write_file"), testTool(t, "subagent_run")}
	if got := filterToolsForSubagentSubtype(ctx, "repo-scout", tools); len(got) != 0 {
		t.Fatalf("a custom subagent with no policy was offered %d tools", len(got))
	}
}

// A user-session subagent whose type has no LLM chain of its own, and whose
// caller pinned no model, runs on the conversation's model: the session-model
// wrapper sits below the typed wrapper, so an unknown-type call lands on the
// session client, not on some config-level default. This is the product rule
// "subagents in a user session follow the session's model".
func TestTypedSubagentSessionModelWinsBelowTypedWrapper(t *testing.T) {
	baseLLM := &recordingLLM{name: "base"}
	sessionLLM := &recordingLLM{name: "session"}
	inner := wrapSessionModelLLM(baseLLM, func(PrimaryModelState) (llm.LLM, error) {
		return sessionLLM, nil
	})
	wrapped := wrapTypedSubagentProviderLLM(inner, nil) // no typed chains

	ctx := toolpkg.WithSubagentType(context.Background(), "verification")
	ctx = WithSessionModelSelection(ctx, PrimaryModelState{Provider: "deepseek", Model: "deepseek-v4"})
	res, err := wrapped.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("check x"))}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sessionLLM.calls != 1 {
		t.Fatalf("session LLM calls = %d, want 1", sessionLLM.calls)
	}
	if baseLLM.calls != 0 {
		t.Fatalf("base LLM calls = %d, want 0 — the session model must win", baseLLM.calls)
	}
	if got := res.Message.TextContent(); got != "session" {
		t.Fatalf("answer came from %q, want session", got)
	}
}
