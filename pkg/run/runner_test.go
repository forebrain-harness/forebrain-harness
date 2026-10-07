package run

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/mcp"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

func TestAgentIntermediateToolPromptSuffixMentionsHighFrequencyAndReadBack(t *testing.T) {
	if !strings.Contains(agentIntermediateToolPromptSuffix, "high-frequency") {
		t.Fatalf("suffix missing high-frequency guidance: %q", agentIntermediateToolPromptSuffix)
	}
	if !strings.Contains(agentIntermediateToolPromptSuffix, "Before replying to the user") {
		t.Fatalf("suffix missing read-back guidance: %q", agentIntermediateToolPromptSuffix)
	}
	if !strings.Contains(agentIntermediateToolPromptSuffix, "intermediate_tool") {
		t.Fatalf("suffix missing tool name: %q", agentIntermediateToolPromptSuffix)
	}
}

func TestNormalizeMCPDelegatingInputSchemaPreservesRemoteSchema(t *testing.T) {
	raw := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{"type": "string"},
		},
		"required": []any{"query"},
	}

	got := normalizeMCPDelegatingInputSchema(raw)
	if got["type"] != "object" {
		t.Fatalf("type = %#v, want object", got["type"])
	}
	props, ok := got["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties type = %T, want map[string]any", got["properties"])
	}
	if _, ok := props["query"]; !ok {
		t.Fatalf("properties = %#v, want query key", props)
	}
	required, ok := got["required"].([]any)
	if !ok || len(required) != 1 || required[0] != "query" {
		t.Fatalf("required = %#v, want [query]", got["required"])
	}
}

func TestNormalizeMCPDelegatingInputSchemaFillsObjectDefaults(t *testing.T) {
	got := normalizeMCPDelegatingInputSchema(nil)
	if got["type"] != "object" {
		t.Fatalf("type = %#v, want object", got["type"])
	}
	props, ok := got["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties type = %T, want map[string]any", got["properties"])
	}
	if len(props) != 0 {
		t.Fatalf("properties = %#v, want empty", props)
	}
}

// TestNewMCPDelegatingToolAcceptsOpenRemoteSchemas covers a server-authored
// schema that allows extra properties. `additionalProperties: true` is ordinary
// JSON Schema, but the tool pipeline requires closed objects and used to reject
// the schema outright — the tool then vanished from the agent with no
// diagnostic. It must be closed and kept instead, at every nesting level.
func TestNewMCPDelegatingToolAcceptsOpenRemoteSchemas(t *testing.T) {
	for _, test := range []struct {
		name   string
		schema map[string]any
	}{
		{
			name: "open at root",
			schema: map[string]any{
				"type":                 "object",
				"properties":           map[string]any{"q": map[string]any{"type": "string"}},
				"additionalProperties": true,
			},
		},
		{
			name: "open in a nested object",
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"filter": map[string]any{"type": "object", "additionalProperties": true},
				},
			},
		},
		{
			name: "open inside an array item",
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"rows": map[string]any{
						"type":  "array",
						"items": map[string]any{"type": "object", "additionalProperties": true},
					},
				},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			tool, err := newMCPDelegatingTool("srv__tool", "desc", test.schema, func(context.Context, json.RawMessage) (string, error) {
				return "ok", nil
			})
			if err != nil {
				t.Fatalf("open remote schema rejected: %v", err)
			}
			if tool.InputSchema()["additionalProperties"] != false {
				t.Fatalf("root additionalProperties = %#v, want false", tool.InputSchema()["additionalProperties"])
			}
		})
	}
}

// TestNormalizeMCPSchemaDoesNotMutateSource guards the deep copy: the source map
// is the MCP client's cached tool listing, shared across reloads.
func TestNormalizeMCPSchemaDoesNotMutateSource(t *testing.T) {
	nested := map[string]any{"type": "object", "additionalProperties": true}
	source := map[string]any{
		"type":                 "object",
		"properties":           map[string]any{"filter": nested},
		"additionalProperties": true,
	}
	normalizeMCPDelegatingInputSchema(source)
	if source["additionalProperties"] != true {
		t.Fatalf("source root mutated: %#v", source["additionalProperties"])
	}
	if nested["additionalProperties"] != true {
		t.Fatalf("source nested object mutated: %#v", nested["additionalProperties"])
	}
}

func TestNewMCPDelegatingToolUsesRemoteSchemaAndPassesArgumentsThrough(t *testing.T) {
	inputSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"projectPath": map[string]any{"type": "string"},
			"query":       map[string]any{"type": "string"},
		},
		"required": []any{"query"},
	}

	var captured json.RawMessage
	tool, err := newMCPDelegatingTool("mcp__codegraph__codegraph_explore", "desc", inputSchema, func(_ context.Context, arguments json.RawMessage) (string, error) {
		captured = append(json.RawMessage(nil), arguments...)
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("newMCPDelegatingTool: %v", err)
	}

	schema := tool.InputSchema()
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties type = %T, want map[string]any", schema["properties"])
	}
	if _, ok := props["arguments"]; ok {
		t.Fatalf("unexpected wrapper property in schema: %#v", props)
	}
	if _, ok := props["projectPath"]; !ok {
		t.Fatalf("missing projectPath property in schema: %#v", props)
	}
	if _, ok := props["query"]; !ok {
		t.Fatalf("missing query property in schema: %#v", props)
	}

	payload := `{"projectPath":"/repo","query":"auth flow"}`
	if _, err := tool.Handle(context.Background(), payload); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if string(captured) != payload {
		t.Fatalf("captured arguments = %s, want %s", string(captured), payload)
	}
}

type runnerUsageStubLLM struct {
	calls int
}

func (m *runnerUsageStubLLM) Execute(ctx context.Context, messages []llm.Message, tools []*llm.Tool) (*llm.Result, error) {
	m.calls++
	switch m.calls {
	case 1:
		if acc := internalLLMUsageAccumulatorFrom(ctx); acc == nil {
			panic("missing internal usage accumulator")
		}
		if sink := llm.StreamSinkFrom(ctx); sink != nil && sink.OnUsage != nil {
			sink.OnUsage(2, 1)
		}
		toolCall := llm.ToolCall{
			ID:   "call-1",
			Type: llm.ToolTypeFunction,
			Function: llm.FunctionCall{
				Name:      "echo_tool",
				Arguments: `{}`,
			},
		}
		msg := llm.AssistantMessage(nil, toolCall)
		return &llm.Result{Message: &msg, Usage: &llm.Usage{InputTokens: 10, OutputTokens: 3}}, nil
	case 2:
		msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("done")})
		return &llm.Result{Message: &msg, Usage: &llm.Usage{InputTokens: 7, OutputTokens: 4}}, nil
	default:
		msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("extra")})
		return &llm.Result{Message: &msg, Usage: &llm.Usage{InputTokens: 1, OutputTokens: 1}}, nil
	}
}

type runnerErrorUsageStubLLM struct{}

func (m runnerErrorUsageStubLLM) Execute(ctx context.Context, messages []llm.Message, tools []*llm.Tool) (*llm.Result, error) {
	if acc := internalLLMUsageAccumulatorFrom(ctx); acc == nil {
		panic("missing internal usage accumulator")
	}
	return nil, llm.WrapErrorWithUsageForTest(errors.New("boom"), &llm.Usage{InputTokens: 13, OutputTokens: 5})
}

func TestRunnerRunContentOverwritesSummaryUsageWithWholeLoopTotal(t *testing.T) {
	tool, err := llm.NewTool("echo_tool", "echo", func(context.Context, *struct{}) (string, error) {
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("NewTool: %v", err)
	}
	stub := &runnerUsageStubLLM{}
	main, err := agent.New(stub, "main", "desc")
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	if err := main.AddTool(tool); err != nil {
		t.Fatalf("AddTool: %v", err)
	}
	main.SetTracer(agent.Noop)

	r := &Runner{Deps: &Deps{}, main: main}
	res, err := r.RunContent(context.Background(), []llm.ContentPart{llm.Text("hi")})
	if err != nil {
		t.Fatalf("RunContent error: %v", err)
	}
	if res == nil || res.Summary == nil {
		t.Fatal("expected summary")
	}
	if res.Summary.Usage.InputTokens != 17 || res.Summary.Usage.OutputTokens != 7 {
		t.Fatalf("summary usage = %+v, want input=17 output=7", res.Summary.Usage)
	}
}

func TestRunnerRunContentOverwritesErrorUsageWithWholeLoopTotal(t *testing.T) {
	main, err := agent.New(runnerErrorUsageStubLLM{}, "main", "desc")
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	main.SetTracer(agent.Noop)

	r := &Runner{Deps: &Deps{}, main: main}
	_, err = r.RunContent(context.Background(), []llm.ContentPart{llm.Text("hi")})
	if err == nil {
		t.Fatal("expected error")
	}
	usage := llm.UsageFromErrorForSupervisor(err)
	if usage == nil {
		t.Fatal("expected usage on error")
	}
	if usage.InputTokens != 13 || usage.OutputTokens != 5 {
		t.Fatalf("error usage = %+v, want input=13 output=5", *usage)
	}
}

// TestRunnerLoadsPrimaryAgentWithoutItsOwnProvider pins that a primary agent
// which has not been given a provider yet still loads: it inherits main's until
// /connect configures one, instead of failing to start.
//
// The behaviour is the Runner's own, so the test builds one directly rather
// than reaching it through whichever surface happens to assemble runners.
func TestRunnerLoadsPrimaryAgentWithoutItsOwnProvider(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	home := t.TempDir()
	cfg := appcfg.Root{
		Agents: appcfg.AgentsSection{
			Definitions: map[string]appcfg.AgentDefinition{
				"main": {Primary: true, LLMProviders: []appcfg.AgentLLMProviderConfig{{
					Provider: "openai", Model: "gpt-main",
					APIKey: "${OPENAI_API_KEY}", BaseURL: "http://localhost:0/v1",
				}}},
				// No LLMProviders of its own.
				"review": {Primary: true},
			},
		},
	}
	resolver, err := appcfg.NewResolver(home, &cfg)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	if _, err := resolver.Switch("review"); err != nil {
		t.Fatalf("Switch: %v", err)
	}

	r := &Runner{Deps: &Deps{Home: home, AgentName: "review", AppCfg: &cfg}, FileResolver: PathResolver{}}
	if err := r.Load(); err != nil {
		t.Fatalf("Load for a primary agent with no provider of its own = %v, want nil "+
			"(it must inherit main's until /connect configures one)", err)
	}
}

// TestPrimaryModelExpandsTheModelsList pins the bug this function fixes: a
// provider that names several models with the Models list form must resolve to
// its first model, not to an empty id.
//
// The per-surface copies read appcfg.PrimaryLLM, which returns the raw first
// provider entry, so a Models: [a, b] config with no Model field reported an
// empty model id on /status and /model while the runtime was running a. This is
// the single place the two must agree.
func TestPrimaryModelExpandsTheModelsList(t *testing.T) {
	cfg := &appcfg.Root{Agents: appcfg.AgentsSection{Definitions: map[string]appcfg.AgentDefinition{
		"main": {Primary: true, LLMProviders: []appcfg.AgentLLMProviderConfig{{
			Provider: "openai",
			Models:   appcfg.StringList{"gpt-5.4", "gpt-5.5"},
		}}},
	}}}

	provider, model := PrimaryModel(&Runner{Deps: &Deps{AppCfg: cfg, AgentName: "main"}})
	if provider != "openai" || model != "gpt-5.4" {
		t.Fatalf("PrimaryModel = %q/%q, want openai/gpt-5.4 (the first model of the list)", provider, model)
	}
}

func TestPrimaryModelFallsBackToMainAndNil(t *testing.T) {
	cfg := &appcfg.Root{Agents: appcfg.AgentsSection{Definitions: map[string]appcfg.AgentDefinition{
		"main": {Primary: true, LLMProviders: []appcfg.AgentLLMProviderConfig{{Provider: "openai", Model: "gpt-main"}}},
	}}}

	// An empty AgentName resolves to main, the same fallback the runtime uses.
	if provider, model := PrimaryModel(&Runner{Deps: &Deps{AppCfg: cfg}}); provider != "openai" || model != "gpt-main" {
		t.Fatalf("PrimaryModel(empty agent) = %q/%q, want openai/gpt-main", provider, model)
	}
	if p, m := PrimaryModel(nil); p != "" || m != "" {
		t.Fatalf("PrimaryModel(nil) = %q/%q, want empty", p, m)
	}
	if p, m := PrimaryModel(&Runner{Deps: &Deps{AppCfg: &appcfg.Root{}}}); p != "" || m != "" {
		t.Fatalf("PrimaryModel(no providers) = %q/%q, want empty", p, m)
	}
}

// concurrentRunStubLLM drives one tool call and then finishes, so two runs can
// be interleaved deterministically by the tool handler they share.
type concurrentRunStubLLM struct {
	mu    sync.Mutex
	calls map[string]int
}

func (m *concurrentRunStubLLM) Execute(ctx context.Context, _ []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	sid := llm.AgentSessionIDFromContext(ctx)
	m.mu.Lock()
	m.calls[sid]++
	n := m.calls[sid]
	m.mu.Unlock()
	if n == 1 {
		msg := llm.AssistantMessage(nil, llm.ToolCall{
			ID:       "call-" + sid,
			Type:     "function",
			Function: llm.FunctionCall{Name: "probe_tool", Arguments: `{}`},
		})
		return &llm.Result{Message: &msg}, nil
	}
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("done")})
	return &llm.Result{Message: &msg}, nil
}

// One Runner executes several runs at once: subagent_fanout dispatches its
// children onto the parent's Runner and agent, and every child's tools run
// there while the parent's own turn is still in flight. The run a tool call
// belongs to is therefore per-goroutine, and the only place that carries it is
// the executing call's context.
//
// The regression this pins: the run id was resolved from Runner-wide state
// instead, so a tool call reported whichever run had started most recently. An
// approval raised by one child then wrote its wait row under a sibling's run,
// and SetWaitingAction — which keeps one wait per run — deleted the sibling's
// real continuation, leaving both children unable to claim their own approval
// ("approval continuation is owned by another live process").
func TestPolicyRunIDForToolResolvesTheExecutingRunNotTheLastStarted(t *testing.T) {
	firstInTool := make(chan struct{})
	secondObserved := make(chan struct{})
	var mu sync.Mutex
	seen := map[string]string{}

	main, err := agent.New(&concurrentRunStubLLM{calls: map[string]int{}}, "main", "desc")
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	main.SetTracer(agent.Noop)
	r := &Runner{Deps: &Deps{}, main: main}

	// The handler asks the runner the same question the action hook asks in
	// production, from inside a tool call that a specific run is executing.
	probe, err := llm.NewTool("probe_tool", "probe", func(ctx context.Context, _ *struct{}) (string, error) {
		sid := llm.AgentSessionIDFromContext(ctx)
		if sid == "child-a" {
			// Hold the first run inside its tool until the second run is also
			// in flight; that overlap is the whole point.
			close(firstInTool)
			<-secondObserved
		} else {
			<-firstInTool
		}
		mu.Lock()
		seen[sid] = r.policyRunIDForTool(ctx)
		mu.Unlock()
		if sid != "child-a" {
			close(secondObserved)
		}
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("NewTool: %v", err)
	}
	if err := main.AddTool(probe); err != nil {
		t.Fatalf("AddTool: %v", err)
	}

	var wg sync.WaitGroup
	for _, child := range []struct{ session, runID string }{
		{"child-a", "run-a"}, {"child-b", "run-b"},
	} {
		wg.Add(1)
		go func(session, runID string) {
			defer wg.Done()
			ctx := tool.WithRunID(llm.WithAgentSessionID(context.Background(), session), runID)
			if _, err := r.RunContent(ctx, []llm.ContentPart{llm.Text("go")}); err != nil {
				t.Errorf("RunContent(%s): %v", session, err)
			}
		}(child.session, child.runID)
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if got := seen["child-a"]; got != "run-a" {
		t.Fatalf("child-a's tool resolved run %q, want run-a", got)
	}
	if got := seen["child-b"]; got != "run-b" {
		t.Fatalf("child-b's tool resolved run %q, want run-b", got)
	}
}

func TestMCPServerWorkingDirUsesProjectRootForProjectScope(t *testing.T) {
	r := &Runner{Deps: &Deps{MCPProject: "/proj/root"}}
	project := appcfg.MCPServerConfig{Name: "p", Scope: appcfg.MCPServerScopeProject, ProjectKey: "pk"}
	if got := r.mcpServerWorkingDir(project, "/fallback"); got != "/proj/root" {
		t.Fatalf("project entry cwd=%q, want the project root", got)
	}
	global := appcfg.MCPServerConfig{Name: "g"}
	if got := r.mcpServerWorkingDir(global, "/fallback"); got != "/fallback" {
		t.Fatalf("global entry cwd=%q, want the process working directory (unchanged behavior)", got)
	}
	// A project entry without a project root falls back rather than guessing.
	bare := &Runner{Deps: &Deps{}}
	if got := bare.mcpServerWorkingDir(project, "/fallback"); got != "/fallback" {
		t.Fatalf("project entry without MCPProject cwd=%q", got)
	}
}

func TestMCPSegmentFingerprintCoversListAndProjectDir(t *testing.T) {
	servers := []appcfg.MCPServerConfig{{Name: "n", Command: "/bin/x"}}
	base := mcpSegmentFingerprint(servers, "/proj")
	if base != mcpSegmentFingerprint(servers, "/proj") {
		t.Fatalf("fingerprint must be deterministic")
	}
	if base == mcpSegmentFingerprint(servers, "/other") {
		t.Fatalf("project directory must participate in the fingerprint")
	}
	changed := []appcfg.MCPServerConfig{{Name: "n", Command: "/bin/y"}}
	if base == mcpSegmentFingerprint(changed, "/proj") {
		t.Fatalf("server list must participate in the fingerprint")
	}
}

// fakeAgentForMCPReplay builds a real agent for the loader to register into.
func fakeAgentForMCPReplay(t *testing.T) *agent.Agent {
	t.Helper()
	a, err := agent.New(noopLLM{}, "main", "desc")
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// startOrReplayMCPSegmentLockedUnderTest calls the load entry point the way
// loadLocked does: holding the load lock, as the Locked suffix requires.
func (r *Runner) startOrReplayMCPSegmentLockedUnderTest(a *agent.Agent) *mcpLoad {
	r.mu.load.Lock()
	defer r.mu.load.Unlock()
	return r.startOrReplayMCPSegmentLocked(a)
}

// waitMCPLoad waits for one generation's startup barrier to lift.
func waitMCPLoad(t *testing.T, r *Runner, ld *mcpLoad) {
	t.Helper()
	if ld == nil {
		return
	}
	select {
	case <-ld.done:
	case <-time.After(30 * time.Second):
		t.Fatal("mcp load did not settle")
	}
}

// mcpTestRunner builds a Runner whose MCP half is wired the way process.Open
// wires it: a tool state to register into and the server list under test.
func mcpTestRunner(t *testing.T, servers ...appcfg.MCPServerConfig) *Runner {
	t.Helper()
	r := &Runner{Deps: &Deps{Home: t.TempDir(), MCPServers: servers}}
	r.tools = tool.NewState()
	r.mcpReg = mcp.NewRegistry()
	return r
}

// TestLoadMCPSegmentReplaysCachedToolsWhenFingerprintUnchanged pins the
// reload contract: a Load whose effective MCP list and project directory did
// not change must neither tear down the live registry nor contact any server —
// it re-registers the cached tool definitions into the new agent.
func TestLoadMCPSegmentReplaysCachedToolsWhenFingerprintUnchanged(t *testing.T) {
	r := &Runner{Deps: &Deps{Home: t.TempDir()}}
	r.tools = tool.NewState()
	r.mcpReg = mcp.NewRegistry()
	// A live entry in the registry: a rebuild path would close it.
	r.mcpReg.Apply(mcp.StatusUpdate{Name: "marker", Transport: "stdio", Status: mcp.ConnStatusConnected,
		AuthStatus: mcp.AuthStatusAuthenticated, ToolCount: 1})

	tt, err := llm.NewTool("mcp__cached__tool", "cached", func(ctx context.Context, in *struct{}) (string, error) {
		return "ok", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	fp := mcpSegmentFingerprint(r.MCPServers, r.mcpProjectDir())
	r.mcpSeg = &mcpSegment{
		fingerprint: fp,
		servers: []mcpSegmentServer{{
			name:      "cached",
			transport: "stdio",
			connected: true,
			tools:     []*llm.Tool{tt},
		}},
	}

	a := fakeAgentForMCPReplay(t)
	// A replay is synchronous: no load is handed back, and the cached
	// definition is already registered on the new agent.
	if ld := r.startOrReplayMCPSegmentLockedUnderTest(a); ld != nil {
		t.Fatalf("unchanged fingerprint must replay instead of starting servers: %+v", ld)
	}
	if n := len(debuglogToolsView(a)); n != 1 {
		t.Fatalf("cached tool not replayed into the new agent: tools=%d", n)
	}
	statuses := r.mcpReg.ListStatus()
	if len(statuses) != 1 || statuses[0].Name != "marker" {
		t.Fatalf("registry was rebuilt despite unchanged fingerprint: %+v", statuses)
	}

	// A changed list must invalidate the cache: the new generation starts its
	// own servers and the old registry is retired.
	r.MCPServers = []appcfg.MCPServerConfig{{Name: "unreachable", Transport: "stdio", Command: "/nonexistent/mcp-server-binary"}}
	a2 := fakeAgentForMCPReplay(t)
	ld := r.startOrReplayMCPSegmentLockedUnderTest(a2)
	if ld == nil {
		t.Fatal("changed fingerprint must start a new generation")
	}
	waitMCPLoad(t, r, ld)
	statuses = r.mcpReg.ListStatus()
	if len(statuses) != 1 || statuses[0].Name != "unreachable" {
		t.Fatalf("changed fingerprint must rebuild the registry: %+v", statuses)
	}
	if statuses[0].ConnStatus != mcp.ConnStatusError {
		t.Fatalf("unreachable server must report its failure: %+v", statuses[0])
	}
}

// TestRunnerMCPProjectStatusWithoutHook covers a Runner assembled without a
// diagnostics hook (tests, subagents built by hand): it must report an empty
// view rather than panic.
func TestRunnerMCPProjectStatusWithoutHook(t *testing.T) {
	var r *Runner
	if st := r.MCPProjectStatus(); st.ProjectRoot != "" || st.PendingReload {
		t.Fatalf("nil runner should report an empty view")
	}
	r = &Runner{Deps: &Deps{}}
	if st := r.MCPProjectStatus(); len(st.NotApplied) != 0 {
		t.Fatalf("runner without hook should report an empty view")
	}
}

// TestRunnerMCPProjectStatusReadsInstalledHook verifies the hook flows through.
func TestRunnerMCPProjectStatusReadsInstalledHook(t *testing.T) {
	r := &Runner{Deps: &Deps{MCPDiagnostics: func() mcp.ProjectMCPScopeSummary {
		return mcp.ProjectMCPScopeSummary{ProjectRoot: "/proj", PendingReload: true}
	}}}
	st := r.MCPProjectStatus()
	if st.ProjectRoot != "/proj" || !st.PendingReload {
		t.Fatalf("status=%+v", st)
	}
}

// TestProjectApprovalClampAppliesAtLoadScope verifies the defensive clamp the
// loader applies to project-scope entries that reached the frozen list
// unclamped.
func TestProjectApprovalClampAppliesAtLoadScope(t *testing.T) {
	srv := appcfg.MCPServerConfig{Name: "p", Scope: appcfg.MCPServerScopeProject, DefaultToolsApprovalMode: appcfg.MCPToolApprovalAuto}
	mcp.ClampProjectRuntimePolicy(&srv)
	if srv.DefaultToolsApprovalMode != appcfg.MCPToolApprovalPrompt {
		t.Fatalf("clamp failed: %q", srv.DefaultToolsApprovalMode)
	}
}

// --- MCP startup: real stdio servers, exercised through the load barrier ---
//
// Everything below runs the load machinery against real MCP child processes,
// because the properties under test are about a generation of external I/O:
// which server finishes first, what a late or failing one leaves behind, and
// what a second Load of the same configuration must reproduce byte for byte.
// An in-process fake could not express any of them.

const (
	fixtureRunDelayEnv      = "FOREBRAIN_RUN_FIXTURE_DELAY_MS"
	fixtureRunToolsEnv      = "FOREBRAIN_RUN_FIXTURE_TOOLS"
	fixtureRunBrokenEnv     = "FOREBRAIN_RUN_FIXTURE_BROKEN_TOOL"
	fixtureRunCapsEnv       = "FOREBRAIN_RUN_FIXTURE_CAPS"
	fixtureRunSilentEnv     = "FOREBRAIN_RUN_FIXTURE_SILENT"
	fixtureRunPidEnv        = "FOREBRAIN_RUN_FIXTURE_PID_FILE"
	fixtureRunTimelineEnv   = "FOREBRAIN_RUN_FIXTURE_TIMELINE_FILE"
	fixtureRunFailToolsEnv  = "FOREBRAIN_RUN_FIXTURE_FAIL_TOOLS"
	fixtureRunBadProtoEnv   = "FOREBRAIN_RUN_FIXTURE_BAD_PROTOCOL"
	fixtureRunFixtureMarker = "forebrain-run-mcp-fixture"
)

// buildRunMCPFixture compiles the fixture server once for one test.
func buildRunMCPFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "main.go")
	if err := os.WriteFile(mainPath, []byte(runMCPFixtureSource), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	exe := filepath.Join(dir, fixtureRunFixtureMarker)
	cmd := exec.Command("go", "build", "-o", exe, mainPath)
	cmd.Dir = repoRootForRunTest(t)
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v\n%s", err, out)
	}
	return exe
}

func repoRootForRunTest(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

// runMCPConfig is one fixture server entry.
type runMCPFixtureEntry struct {
	name      string
	required  bool
	timeout   float64
	delayMS   int
	tools     []string
	broken    bool
	caps      string
	silent    bool
	failTools bool
	badProto  bool
	pidFile   string
	timeline  string
}

func runMCPFixtureConfig(exe string, entry runMCPFixtureEntry) appcfg.MCPServerConfig {
	env := map[string]string{
		fixtureRunDelayEnv: strconv.Itoa(entry.delayMS),
		fixtureRunToolsEnv: strings.Join(entry.tools, ","),
		fixtureRunCapsEnv:  entry.caps,
	}
	if entry.broken {
		env[fixtureRunBrokenEnv] = "1"
	}
	if entry.silent {
		env[fixtureRunSilentEnv] = "1"
	}
	if entry.failTools {
		env[fixtureRunFailToolsEnv] = "1"
	}
	if entry.badProto {
		env[fixtureRunBadProtoEnv] = "1"
	}
	if entry.pidFile != "" {
		env[fixtureRunPidEnv] = entry.pidFile
	}
	if entry.timeline != "" {
		env[fixtureRunTimelineEnv] = entry.timeline
	}
	return appcfg.MCPServerConfig{
		Name:           entry.name,
		Transport:      "stdio",
		Command:        exe,
		Required:       entry.required,
		StartupTimeout: entry.timeout,
		Env:            env,
	}
}

// mcpLoadTestRunner assembles a Runner whose MCP half is wired the way
// process.Open wires it: tool state to register into, and the server list.
func mcpLoadTestRunner(t *testing.T, servers ...appcfg.MCPServerConfig) *Runner {
	t.Helper()
	r := &Runner{Deps: &Deps{Home: t.TempDir(), MCPServers: servers}}
	r.tools = tool.NewState()
	r.mcpReg = mcp.NewRegistry()
	return r
}

// startMCPLoad runs the load entry point and returns the generation that is
// starting, or nil when the segment was replayed synchronously.
func startMCPLoad(t *testing.T, r *Runner, a *agent.Agent) *mcpLoad {
	t.Helper()
	r.mu.load.Lock()
	ld := r.startOrReplayMCPSegmentLocked(a)
	r.mu.load.Unlock()
	return ld
}

// awaitMCPLoad waits for one generation to settle, failing the test rather than
// hanging if it does not.
func awaitMCPLoad(t *testing.T, ld *mcpLoad) {
	t.Helper()
	if ld == nil {
		return
	}
	select {
	case <-ld.done:
	case <-time.After(60 * time.Second):
		t.Fatal("mcp load did not settle")
	}
}

// agentToolNames is the agent's MCP half of the tool table, in registration
// order. It reads the agent's own slice rather than tool.State's metas, because
// the metas are sorted: the order the provider receives the definitions in is
// the order the agent holds them, and that is what the prompt prefix is built
// from.
func agentToolNames(a *agent.Agent) []string {
	var out []string
	for _, tt := range debuglogToolsView(a) {
		out = append(out, tt.Name())
	}
	return out
}

func agentToolsJSON(t *testing.T, a *agent.Agent) string {
	t.Helper()
	type toolView struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Schema      map[string]any `json:"schema"`
	}
	views := make([]toolView, 0, len(debuglogToolsView(a)))
	for _, tt := range debuglogToolsView(a) {
		views = append(views, toolView{Name: tt.Name(), Description: tt.Description(), Schema: tt.InputSchema()})
	}
	body, err := json.Marshal(views)
	if err != nil {
		t.Fatalf("marshal tools: %v", err)
	}
	return string(body)
}

// TestMCPLoadStartsInParallelAndRegistersInConfigurationOrder pins the two
// properties the asynchronous load exists for: the first frame does not wait for
// the servers, and which server answers first does not decide the tool table.
//
// Parallelism is proven by overlap, not by a stopwatch: each fixture records when
// it started and when it had answered, and the two intervals must overlap. A
// serial load cannot produce overlap, while a slow machine only widens both
// intervals — so the assertion says the same thing on an idle laptop and on a
// loaded CI box.
//
// The first server is deliberately the slower one. A load that appended results
// as they arrived would put the second server's tools first, and the model would
// see a different tool list from one session to the next depending on how busy
// the machine was.
func TestMCPLoadStartsInParallelAndRegistersInConfigurationOrder(t *testing.T) {
	exe := buildRunMCPFixture(t)
	slowTimeline := filepath.Join(t.TempDir(), "slow.timeline")
	fastTimeline := filepath.Join(t.TempDir(), "fast.timeline")
	slow := appcfg.MCPServerConfig{Name: "slow", Transport: "stdio", Command: exe}
	fast := appcfg.MCPServerConfig{Name: "fast", Transport: "stdio", Command: exe}
	slow.Env = map[string]string{fixtureRunDelayEnv: "500", fixtureRunToolsEnv: "alpha,beta", fixtureRunTimelineEnv: slowTimeline}
	fast.Env = map[string]string{fixtureRunDelayEnv: "600", fixtureRunToolsEnv: "gamma", fixtureRunTimelineEnv: fastTimeline}

	r := mcpLoadTestRunner(t, slow, fast)
	a := fakeAgentForMCPReplay(t)
	started := time.Now()
	ld := startMCPLoad(t, r, a)
	if ld == nil {
		t.Fatal("a fresh server list must start a generation")
	}
	// Nothing is published before the barrier lifts: an agent without its MCP
	// tools is not a state any reader may observe.
	if got := r.Agent(); got != nil {
		t.Fatal("the agent was published before the MCP generation settled")
	}
	progress := r.MCPStartup().Progress()
	if progress.Total != 2 || progress.InFlight != 2 {
		t.Fatalf("startup progress = %+v, want both servers in flight", progress)
	}
	awaitMCPLoad(t, ld)
	elapsed := time.Since(started)

	if r.Agent() == nil {
		t.Fatal("the agent was not published after the generation settled")
	}
	names := agentToolNames(a)
	want := []string{"mcp__slow__alpha", "mcp__slow__beta", "mcp__fast__gamma"}
	if len(names) != len(want) {
		t.Fatalf("tools = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("tools = %v, want configuration order %v", names, want)
		}
	}
	// The two startups overlapped. A serial load cannot make this true: the
	// second server would not begin until the first had finished, so its start
	// would be at or after the first's end.
	slowStart, slowEnd := readFixtureTimeline(t, slowTimeline)
	fastStart, fastEnd := readFixtureTimeline(t, fastTimeline)
	latestStart := slowStart
	if fastStart.After(latestStart) {
		latestStart = fastStart
	}
	earliestEnd := slowEnd
	if fastEnd.Before(earliestEnd) {
		earliestEnd = fastEnd
	}
	if !latestStart.Before(earliestEnd) {
		t.Fatalf("the two servers did not overlap (slow [%s,%s], fast [%s,%s]): they did not start in parallel",
			slowEnd.Sub(slowStart), slowEnd.Sub(slowStart), fastEnd.Sub(fastStart), fastEnd.Sub(fastStart))
	}
	// A loose ceiling on the whole thing, so a load that serialized the servers
	// while still overlapping by accident is caught too.
	if elapsed > 10*time.Second {
		t.Fatalf("two short startups took %s", elapsed)
	}
	progress = r.MCPStartup().Progress()
	if progress.InFlight != 0 || progress.Connected != 2 || progress.Settled() != 2 {
		t.Fatalf("settled progress = %+v", progress)
	}
}

// readFixtureTimeline reads one fixture's recorded interval: the moment it
// started and the moment it had answered the handshake.
func readFixtureTimeline(t *testing.T, path string) (time.Time, time.Time) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(path)
		if err == nil {
			fields := strings.Fields(strings.TrimSpace(string(raw)))
			if len(fields) == 2 {
				start, errStart := strconv.ParseInt(fields[0], 10, 64)
				end, errEnd := strconv.ParseInt(fields[1], 10, 64)
				if errStart == nil && errEnd == nil {
					return time.Unix(0, start), time.Unix(0, end)
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("fixture never recorded its timeline in %s", path)
	return time.Time{}, time.Time{}
}

// TestMCPReplayAfterSettleIsByteStableAndKeepsTheAdvertisedCount is the cache
// guard, and with it the F1/F2 regression: the tool definitions a reload
// rebuilds must be byte-identical to the ones the session's prompt prefix was
// built from, and the tool count a status surface reports must be the count the
// server advertised rather than the number of tools that survived construction.
//
// The broken schema is what makes the two counts differ, so it is the case that
// would have drifted.
func TestMCPReplayAfterSettleIsByteStableAndKeepsTheAdvertisedCount(t *testing.T) {
	exe := buildRunMCPFixture(t)
	server := appcfg.MCPServerConfig{
		Name: "docs", Transport: "stdio", Command: exe,
		Env: map[string]string{
			fixtureRunToolsEnv:  "one,two",
			fixtureRunBrokenEnv: "1",
			fixtureRunCapsEnv:   "prompts",
		},
	}
	r := mcpLoadTestRunner(t, server)
	first := fakeAgentForMCPReplay(t)
	awaitMCPLoad(t, startMCPLoad(t, r, first))

	firstJSON := agentToolsJSON(t, first)
	firstNames := agentToolNames(first)
	// one, two, the broken one skipped, and the derived prompt tools.
	if len(firstNames) != 4 {
		t.Fatalf("first load tools = %v", firstNames)
	}
	rec, ok := r.MCPStartup().Snapshot().Servers[0], true
	if !ok || rec.ToolCount != 3 {
		t.Fatalf("tool_count = %d, want the 3 tools the server advertised", rec.ToolCount)
	}
	generation := r.MCPStartup().Progress().Generation
	if generation == "" {
		t.Fatal("a settled generation must be named")
	}

	// A second Load with the same configuration replays the cache: no new
	// process, and the same definitions.
	second := fakeAgentForMCPReplay(t)
	if ld := startMCPLoad(t, r, second); ld != nil {
		t.Fatal("an unchanged fingerprint must replay instead of starting servers")
	}
	if got := agentToolsJSON(t, second); got != firstJSON {
		t.Fatalf("replayed tools differ from the first load:\n first=%s\nsecond=%s", firstJSON, got)
	}
	rec = r.MCPStartup().Snapshot().Servers[0]
	if rec.ToolCount != 3 {
		t.Fatalf("tool_count after replay = %d, want it unchanged at 3", rec.ToolCount)
	}
	if got := r.MCPStartup().Progress().Generation; got != generation {
		t.Fatalf("generation changed across a replay: %q -> %q", generation, got)
	}

	// A policy-only change is a new generation — the operator widening a
	// timeout is asking for the server that timed out to be tried again — and it
	// must still produce the same tool table.
	restarted := server
	restarted.StartupTimeout = 45
	r.Deps.MCPServers = []appcfg.MCPServerConfig{restarted}
	third := fakeAgentForMCPReplay(t)
	ld := startMCPLoad(t, r, third)
	if ld == nil {
		t.Fatal("a changed startup policy must start a new generation")
	}
	awaitMCPLoad(t, ld)
	if got := agentToolsJSON(t, third); got != firstJSON {
		t.Fatalf("a restarted generation produced different tools:\n first=%s\nrestarted=%s", firstJSON, got)
	}
	if got := r.MCPStartup().Progress().Generation; got == generation {
		t.Fatal("a restarted generation must be a new generation")
	}
}

// TestLoadedToolsWaitsForTheBarrier pins F7: the tool table a fork or hook
// agent is built from must never be the one that is still missing its MCP half.
func TestLoadedToolsWaitsForTheBarrier(t *testing.T) {
	exe := buildRunMCPFixture(t)
	server := appcfg.MCPServerConfig{Name: "slow", Transport: "stdio", Command: exe}
	server.Env = map[string]string{fixtureRunDelayEnv: "400", fixtureRunToolsEnv: "alpha"}

	r := mcpLoadTestRunner(t, server)
	a := fakeAgentForMCPReplay(t)
	ld := startMCPLoad(t, r, a)
	if ld == nil {
		t.Fatal("expected a generation")
	}
	type result struct {
		names []string
		took  time.Duration
	}
	done := make(chan result, 1)
	started := time.Now()
	go func() {
		tools := r.LoadedTools()
		names := make([]string, 0, len(tools))
		for _, tt := range tools {
			names = append(names, tt.Name())
		}
		done <- result{names: names, took: time.Since(started)}
	}()
	select {
	case got := <-done:
		t.Fatalf("LoadedTools returned before the generation settled: %v", got.names)
	case <-time.After(150 * time.Millisecond):
	}
	awaitMCPLoad(t, ld)
	got := <-done
	if got.took < 300*time.Millisecond {
		t.Fatalf("LoadedTools returned after %s, want it to wait for the barrier", got.took)
	}
	if len(got.names) != 1 || got.names[0] != "mcp__slow__alpha" {
		t.Fatalf("LoadedTools = %v, want the settled tool table", got.names)
	}
}

// TestMCPOptionalSkipLeavesRequiredServersAlone pins the two-layer
// cancellation: an interactive skip is exactly the optional servers, and a
// required one is not something the operator may silently do without.
func TestMCPOptionalSkipLeavesRequiredServersAlone(t *testing.T) {
	exe := buildRunMCPFixture(t)
	optional := appcfg.MCPServerConfig{Name: "optional", Transport: "stdio", Command: exe}
	optional.Env = map[string]string{fixtureRunDelayEnv: "30000", fixtureRunToolsEnv: "later"}
	required := appcfg.MCPServerConfig{Name: "required", Transport: "stdio", Command: exe, Required: true}
	required.Env = map[string]string{fixtureRunDelayEnv: "300", fixtureRunToolsEnv: "needed"}

	r := mcpLoadTestRunner(t, optional, required)
	a := fakeAgentForMCPReplay(t)
	ld := startMCPLoad(t, r, a)
	if ld == nil {
		t.Fatal("expected a generation")
	}
	progress := r.MCPStartup().Progress()
	if !progress.CanSkipOptional() || progress.OptionalInFlight != 1 || progress.RequiredInFlight != 1 {
		t.Fatalf("progress = %+v, want one optional and one required in flight", progress)
	}
	if !r.MCPStartup().CancelOptional() {
		t.Fatal("a skip with an optional server in flight must report that it skipped one")
	}
	if r.MCPStartup().CancelOptional() {
		t.Fatal("a repeated skip has nothing left to cancel")
	}
	awaitMCPLoad(t, ld)

	snapshot := r.MCPStartup().Snapshot()
	if len(snapshot.Servers) != 2 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if snapshot.Servers[0].Name != "optional" || snapshot.Servers[0].ConnStatus != mcp.ConnStatusCancelled {
		t.Fatalf("the optional server = %+v, want it cancelled", snapshot.Servers[0])
	}
	if snapshot.Servers[1].ConnStatus != mcp.ConnStatusConnected {
		t.Fatalf("the required server = %+v, want it connected", snapshot.Servers[1])
	}
	// A skipped server contributes no tools, and its absence is not a failure.
	if names := agentToolNames(a); len(names) != 1 || names[0] != "mcp__required__needed" {
		t.Fatalf("tools = %v, want only the required server's tool", names)
	}
	if failures := r.MCPStartup().RequiredFailures(); len(failures) != 0 {
		t.Fatalf("skipping an optional server reported a required failure: %+v", failures)
	}
}

// TestMCPStartupTimeoutFailsOneServerWithoutHoldingTheOthers pins the per-server
// deadline: a server that never answers costs its own timeout and nothing else,
// and the failure keeps the text the runtime produced.
func TestMCPStartupTimeoutFailsOneServerWithoutHoldingTheOthers(t *testing.T) {
	exe := buildRunMCPFixture(t)
	silent := appcfg.MCPServerConfig{Name: "silent", Transport: "stdio", Command: exe, StartupTimeout: 0.4}
	silent.Env = map[string]string{fixtureRunSilentEnv: "1", fixtureRunToolsEnv: "never"}
	healthy := appcfg.MCPServerConfig{Name: "healthy", Transport: "stdio", Command: exe}
	healthy.Env = map[string]string{fixtureRunToolsEnv: "works"}

	r := mcpLoadTestRunner(t, silent, healthy)
	a := fakeAgentForMCPReplay(t)
	started := time.Now()
	ld := startMCPLoad(t, r, a)
	if ld == nil {
		t.Fatal("expected a generation")
	}
	awaitMCPLoad(t, ld)
	elapsed := time.Since(started)
	if elapsed > 5*time.Second {
		t.Fatalf("a 400ms deadline took %s: the failure held up the generation", elapsed)
	}
	snapshot := r.MCPStartup().Snapshot()
	if snapshot.Servers[0].ConnStatus != mcp.ConnStatusError {
		t.Fatalf("the silent server = %+v, want an error", snapshot.Servers[0])
	}
	text := snapshot.Servers[0].Error
	if !strings.Contains(text, "startup timed out after 400ms") || !strings.Contains(text, "context deadline exceeded") {
		t.Fatalf("error text = %q, want the deadline and the underlying error", text)
	}
	if snapshot.Servers[1].ConnStatus != mcp.ConnStatusConnected {
		t.Fatalf("the healthy server = %+v, want it connected", snapshot.Servers[1])
	}
	if names := agentToolNames(a); len(names) != 1 || names[0] != "mcp__healthy__works" {
		t.Fatalf("tools = %v, want the healthy server's tool", names)
	}
	if failures := r.MCPStartup().RequiredFailures(); len(failures) != 0 {
		t.Fatalf("an optional failure was reported as required: %+v", failures)
	}
}

// TestRequiredMCPFailureAggregatesEveryServerInConfigurationOrder pins the
// unattended-run contract: a required server that did not come up fails the run,
// and every failure is reported at once with the server's own text, in the order
// the operator wrote them.
func TestRequiredMCPFailureAggregatesEveryServerInConfigurationOrder(t *testing.T) {
	exe := buildRunMCPFixture(t)
	first := appcfg.MCPServerConfig{Name: "first-required", Transport: "stdio", Command: exe, Required: true, StartupTimeout: 0.3}
	first.Env = map[string]string{fixtureRunSilentEnv: "1"}
	second := appcfg.MCPServerConfig{Name: "second-required", Transport: "stdio", Command: exe, Required: true}
	second.Env = map[string]string{fixtureRunFailToolsEnv: "1"}

	r := mcpLoadTestRunner(t, first, second)
	a := fakeAgentForMCPReplay(t)
	ld := startMCPLoad(t, r, a)
	if ld == nil {
		t.Fatal("expected a generation")
	}
	if r.MCPStartup().CancelOptional() {
		t.Fatal("a generation with no optional servers has nothing to skip")
	}
	awaitMCPLoad(t, ld)

	err := r.MCPStartup().Wait(context.Background())
	if err == nil {
		t.Fatal("a failed required server must fail the barrier for an unattended run")
	}
	var aggregate *MCPRequiredStartupError
	if !errors.As(err, &aggregate) {
		t.Fatalf("error = %T %v, want the aggregate", err, err)
	}
	if len(aggregate.Failures) != 2 {
		t.Fatalf("aggregate = %+v, want both required servers", aggregate.Failures)
	}
	if aggregate.Failures[0].Server != "first-required" || aggregate.Failures[1].Server != "second-required" {
		t.Fatalf("failures = %+v, want configuration order", aggregate.Failures)
	}
	for _, failure := range aggregate.Failures {
		if failure.State != mcp.ConnStatusError || strings.TrimSpace(failure.Error) == "" {
			t.Fatalf("failure = %+v, want its state and its own text", failure)
		}
	}
	if !strings.Contains(err.Error(), "first-required") || !strings.Contains(err.Error(), "second-required") {
		t.Fatalf("error text %q does not name both servers", err.Error())
	}

	// The required server that never answered is not free to be skipped, and the
	// generation that failed stays visible rather than being retried silently.
	if progress := r.MCPStartup().Progress(); progress.RequiredInFlight != 0 || progress.Errored != 2 {
		t.Fatalf("progress = %+v", progress)
	}
}

// TestMCPStartupCancellationIsBoundedAndReleasesChildren pins the Runner's own
// shutdown: it takes no new load, stops the one in flight, and leaves nothing
// behind — no snapshot, no registry, no child process.
func TestMCPStartupCancellationIsBoundedAndReleasesChildren(t *testing.T) {
	exe := buildRunMCPFixture(t)
	pidFile := filepath.Join(t.TempDir(), "pid")
	slow := appcfg.MCPServerConfig{Name: "slow", Transport: "stdio", Command: exe, StartupTimeout: 60}
	slow.Env = map[string]string{
		fixtureRunDelayEnv: "30000",
		fixtureRunPidEnv:   pidFile,
	}

	r := mcpLoadTestRunner(t, slow)
	a := fakeAgentForMCPReplay(t)
	ld := startMCPLoad(t, r, a)
	if ld == nil {
		t.Fatal("expected a generation")
	}
	pid := waitRunFixturePid(t, pidFile)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = r.Close()
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("Runner.Close did not return: a cancelled startup was not bounded")
	}
	if got := r.Agent(); got != nil {
		t.Fatal("a closed Runner must not keep serving its agent")
	}
	if got := r.MCPStartup().Snapshot().Servers; len(got) != 0 {
		t.Fatalf("a closed Runner still reports servers: %+v", got)
	}
	if got := r.MCPRegistry(); got != nil {
		t.Fatal("a closed Runner must release its MCP registry")
	}
	// Closing is idempotent, and a closed Runner refuses to load again rather
	// than starting a generation nothing will shut down.
	if err := r.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if err := r.loadLocked(nil); err == nil {
		t.Fatal("a closed Runner must refuse a new load")
	}
	waitForRunProcessGone(t, pid)
}

// waitRunFixturePid waits for the fixture to record its pid.
func waitRunFixturePid(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(path)
		if err == nil {
			pid, convErr := strconv.Atoi(strings.TrimSpace(string(raw)))
			if convErr == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("fixture never wrote its pid to %s", path)
	return 0
}

// waitForRunProcessGone waits until the pid is no longer running. A zombie
// counts as gone only when its parent reaped it, which is why the state is
// checked rather than just the exit: a killed-but-unreaped child is exactly the
// leak this asserts against.
func waitForRunProcessGone(t *testing.T, pid int) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("process state is asserted through ps, which Windows does not have")
	}
	deadline := time.Now().Add(30 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		out, err := exec.Command("ps", "-o", "state=", "-p", strconv.Itoa(pid)).Output()
		state := strings.TrimSpace(string(out))
		if err != nil || state == "" {
			return
		}
		last = state
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("fixture process %d is still %s after its Runner closed", pid, last)
}

// runMCPFixtureSource is a raw JSON-RPC MCP server. It answers by hand rather
// than through the SDK because every property under test is a scheduling one:
// how long a server takes, in what order two of them finish, and what a server
// that never answers or refuses tools/list leaves behind.
const runMCPFixtureSource = `package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

func main() {
	if path := os.Getenv("FOREBRAIN_RUN_FIXTURE_PID_FILE"); path != "" {
		_ = os.WriteFile(path, []byte(fmt.Sprint(os.Getpid())), 0o600)
	}
	// The timeline records when this process started and when it had answered,
	// which is how a test proves two servers overlapped without timing anything.
	timeline := os.Getenv("FOREBRAIN_RUN_FIXTURE_TIMELINE_FILE")
	startedAt := time.Now().UnixNano()
	delay := 0
	if raw := os.Getenv("FOREBRAIN_RUN_FIXTURE_DELAY_MS"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			delay = parsed
		}
	}
	var tools []map[string]any
	for _, name := range strings.Split(os.Getenv("FOREBRAIN_RUN_FIXTURE_TOOLS"), ",") {
		if name = strings.TrimSpace(name); name == "" {
			continue
		}
		tools = append(tools, map[string]any{
			"name":        name,
			"description": "fixture tool " + name,
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}},
		})
	}
	if os.Getenv("FOREBRAIN_RUN_FIXTURE_BROKEN_TOOL") == "1" {
		// A schema whose $ref the server never defined: the tool is advertised
		// in tools/list and cannot be constructed, which is what makes the
		// advertised count differ from the registered one.
		tools = append(tools, map[string]any{
			"name":        "broken",
			"description": "advertised but unusable",
			"inputSchema": map[string]any{
				"type":       "object",
				"properties": map[string]any{"a": map[string]any{"$ref": "#/$defs/never_defined"}},
			},
		})
	}
	caps := map[string]any{}
	for _, cap := range strings.Split(os.Getenv("FOREBRAIN_RUN_FIXTURE_CAPS"), ",") {
		switch strings.TrimSpace(cap) {
		case "prompts":
			caps["prompts"] = map[string]any{}
		case "resources":
			caps["resources"] = map[string]any{}
		}
	}

	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 1<<20), 1<<20)
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	for in.Scan() {
		line := in.Bytes()
		if len(line) == 0 {
			continue
		}
		var req struct {
			ID     any    ` + "`json:\"id\"`" + `
			Method string ` + "`json:\"method\"`" + `
		}
		if json.Unmarshal(line, &req) != nil || req.Method == "" || req.ID == nil {
			continue
		}
		switch req.Method {
		case "initialize":
			if delay > 0 {
				time.Sleep(time.Duration(delay) * time.Millisecond)
			}
			if os.Getenv("FOREBRAIN_RUN_FIXTURE_SILENT") == "1" {
				continue
			}
			version := "2025-06-18"
			if os.Getenv("FOREBRAIN_RUN_FIXTURE_BAD_PROTOCOL") == "1" {
				version = "1999-01-01"
			}
			if timeline != "" {
				_ = os.WriteFile(timeline, []byte(fmt.Sprintf("%d %d", startedAt, time.Now().UnixNano())), 0o600)
			}
			write(out, map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{
				"protocolVersion": version,
				"capabilities":    caps,
				"serverInfo":      map[string]any{"name": "run-fixture", "version": "1.0.0"},
			}})
		case "tools/list":
			if os.Getenv("FOREBRAIN_RUN_FIXTURE_FAIL_TOOLS") == "1" {
				write(out, map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32601, "message": "tools/list refused"}})
				continue
			}
			write(out, map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"tools": tools}})
		case "prompts/list":
			write(out, map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"prompts": []any{}}})
		case "resources/list":
			write(out, map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"resources": []any{}}})
		default:
			write(out, map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32601, "message": "method not found"}})
		}
	}
}

func write(out *bufio.Writer, message map[string]any) {
	body, err := json.Marshal(message)
	if err != nil {
		return
	}
	_, _ = out.Write(append(body, '\n'))
	_ = out.Flush()
}
`

// TestRunContentWaitsForTheStartingGenerationInsteadOfLoadingAgain pins what
// "not published yet" means to the turn that arrives during startup.
//
// r.main stays nil until the MCP half of the tool table is in place, so a first
// turn cannot tell "still connecting" from "never loaded" by that field alone.
// Treating them the same made the first turn rebuild the whole agent — skills,
// permissions, tool table — for a generation that was already on its way to
// publishing one. The Runner here has no configuration to load from, so a
// rebuild is not merely wasteful: it fails, which is what makes this assertion
// about the condition and not about timing.
func TestRunContentWaitsForTheStartingGenerationInsteadOfLoadingAgain(t *testing.T) {
	exe := buildRunMCPFixture(t)
	server := appcfg.MCPServerConfig{Name: "slow", Transport: "stdio", Command: exe}
	server.Env = map[string]string{fixtureRunDelayEnv: "400", fixtureRunToolsEnv: "alpha"}

	r := mcpLoadTestRunner(t, server)
	a := fakeAgentForMCPReplay(t)
	a.SetTracer(agent.Noop)
	ld := startMCPLoad(t, r, a)
	if ld == nil {
		t.Fatal("expected a generation")
	}
	started := time.Now()
	res, err := r.RunContent(context.Background(), []llm.ContentPart{llm.Text("hi")})
	if err != nil {
		t.Fatalf("RunContent during startup: %v", err)
	}
	if res == nil {
		t.Fatal("RunContent returned no result")
	}
	if took := time.Since(started); took < 300*time.Millisecond {
		t.Fatalf("RunContent returned after %s, want it to wait on the barrier", took)
	}
	// One generation, and the turn ran on the agent it published.
	if r.Agent() != a {
		t.Fatal("RunContent ran on an agent the starting generation did not publish")
	}
	if names := agentToolNames(a); len(names) != 1 || names[0] != "mcp__slow__alpha" {
		t.Fatalf("tools = %v, want the settled tool table", names)
	}
}

// TestASecondLoadDuringStartupPublishesTheAgentItsToolsWereBuiltInto pins the
// publication invariant when two Loads overlap one generation.
//
// A Load that arrives while the servers are still connecting joins the
// generation rather than starting another, and the generation publishes exactly
// one agent: the one its MCP tools were registered into. Letting a late Load
// swap the agent out after the generation had begun building the table would
// publish an agent whose MCP half went somewhere else.
func TestASecondLoadDuringStartupPublishesTheAgentItsToolsWereBuiltInto(t *testing.T) {
	exe := buildRunMCPFixture(t)
	server := appcfg.MCPServerConfig{Name: "slow", Transport: "stdio", Command: exe}
	server.Env = map[string]string{fixtureRunDelayEnv: "300", fixtureRunToolsEnv: "alpha"}

	r := mcpLoadTestRunner(t, server)
	first := fakeAgentForMCPReplay(t)
	ld := startMCPLoad(t, r, first)
	if ld == nil {
		t.Fatal("expected a generation")
	}
	second := fakeAgentForMCPReplay(t)
	if again := startMCPLoad(t, r, second); again != ld {
		t.Fatal("a Load during startup must join the generation rather than start another")
	}
	awaitMCPLoad(t, ld)

	if r.Agent() != second {
		t.Fatal("the generation published an agent other than the one the last Load built")
	}
	if names := agentToolNames(second); len(names) != 1 || names[0] != "mcp__slow__alpha" {
		t.Fatalf("published agent tools = %v, want the MCP table", names)
	}
	if names := agentToolNames(first); len(names) != 0 {
		t.Fatalf("the dropped agent was given tools too: %v", names)
	}
	// Once the generation has claimed its agent there is nothing left to point
	// it at: a later Load builds its own generation instead.
	if ld.attachAgent(fakeAgentForMCPReplay(t)) {
		t.Fatal("a settled generation accepted a new agent")
	}
}

// TestConnectedServersAreVisibleBeforeTheGenerationSettles pins that the
// startup state a surface reads is the state right now.
//
// The progress a status line shows is the answer to "is it worth waiting", and
// it is useless if it only moves when there is nothing left to wait for. A
// server that has answered is connected, is counted as settled, and is no
// longer named as one the session is waiting on — while the slow one still is.
func TestConnectedServersAreVisibleBeforeTheGenerationSettles(t *testing.T) {
	exe := buildRunMCPFixture(t)
	fast := appcfg.MCPServerConfig{Name: "fast", Transport: "stdio", Command: exe}
	fast.Env = map[string]string{fixtureRunToolsEnv: "alpha"}
	slow := appcfg.MCPServerConfig{Name: "slow", Transport: "stdio", Command: exe}
	slow.Env = map[string]string{fixtureRunDelayEnv: "30000", fixtureRunToolsEnv: "later"}

	r := mcpLoadTestRunner(t, fast, slow)
	a := fakeAgentForMCPReplay(t)
	ld := startMCPLoad(t, r, a)
	if ld == nil {
		t.Fatal("expected a generation")
	}
	defer func() {
		r.MCPStartup().CancelOptional()
		awaitMCPLoad(t, ld)
	}()

	deadline := time.Now().Add(30 * time.Second)
	for {
		progress := r.MCPStartup().Progress()
		if progress.Connected == 1 && progress.InFlight == 1 && progress.Settled() == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("progress = %+v, want the connected server counted before the generation settled", progress)
		}
		time.Sleep(20 * time.Millisecond)
	}
	snapshot := r.MCPStartup().Snapshot()
	if len(snapshot.Servers) != 2 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if snapshot.Servers[0].ConnStatus != mcp.ConnStatusConnected {
		t.Fatalf("the fast server = %+v, want it connected", snapshot.Servers[0])
	}
	if snapshot.Servers[1].ConnStatus != mcp.ConnStatusConnecting {
		t.Fatalf("the slow server = %+v, want it still connecting", snapshot.Servers[1])
	}
	// Nothing is published yet: the tool table is still waiting on the slow
	// server, which is the whole point of the barrier.
	if r.Agent() != nil {
		t.Fatal("the agent was published before the generation settled")
	}
}

// TestReleaseIdleConnectionsKeepsTheToolTableAndReconnectsOnUse is the freeze
// half of the idle release: taking an idle Runner's MCP connections away must
// not take anything the conversation was promised.
//
// The tool table stays byte for byte (the segment's cached definitions are what
// the prompt prefix was built from), a required server that was released is not
// a required failure (it came up; it will come back), and the first request
// through the released server grows the connection again on a new child —
// which is the whole difference between releasing a resource and breaking one.
func TestReleaseIdleConnectionsKeepsTheToolTableAndReconnectsOnUse(t *testing.T) {
	exe := buildRunMCPFixture(t)
	pidFile := filepath.Join(t.TempDir(), "fixture.pid")
	server := runMCPFixtureConfig(exe, runMCPFixtureEntry{
		name: "docs", required: true, tools: []string{"alpha"}, pidFile: pidFile,
	})

	r := mcpLoadTestRunner(t, server)
	a := fakeAgentForMCPReplay(t)
	ld := startMCPLoad(t, r, a)
	awaitMCPLoad(t, ld)

	before := agentToolsJSON(t, a)
	firstPid := readFixturePid(t, pidFile)

	// A foreground turn in flight vetoes the release, whatever the clock says:
	// the connections belong to the turn that is using them.
	r.foreground.enter()
	if r.MCPStartup().ReleaseIdleConnections(0) {
		t.Fatal("a release with a foreground turn running must be refused")
	}
	r.foreground.leave()
	if r.MCPStartup().ReleaseIdleConnections(time.Minute) {
		t.Fatal("a release inside the settle window must be refused")
	}

	if !r.MCPStartup().ReleaseIdleConnections(0) {
		t.Fatal("an idle, settled runner must release its connections")
	}
	waitRunFixturePidGone(t, firstPid)

	// The freeze: same tools, same order, same bytes.
	if after := agentToolsJSON(t, a); after != before {
		t.Fatalf("the tool table changed across a release:\nbefore %s\nafter  %s", before, after)
	}
	// A released required server is not a failure — not for the barrier's
	// aggregate, not for the surface that shows failures.
	if failures := r.MCPStartup().RequiredFailures(); len(failures) != 0 {
		t.Fatalf("a released required server reported a failure: %+v", failures)
	}
	snapshot := r.MCPStartup().Snapshot()
	if len(snapshot.Servers) != 1 || snapshot.Servers[0].ConnStatus != mcp.ConnStatusDisconnected {
		t.Fatalf("snapshot after release = %+v, want one disconnected server", snapshot.Servers)
	}
	if snapshot.Pending || snapshot.Progress.InFlight != 0 || snapshot.Progress.Released != 1 {
		t.Fatalf("progress after release = %+v, want it released and not pending", snapshot.Progress)
	}

	// The handle survived, and using it brings the connection back on a new
	// child. ListToolMetas goes through the same ensureSession path a tool call
	// does, so this is the reconnect the model's next tool call would get.
	reg := r.MCPRegistry()
	sess, ok := reg.GetSession("docs")
	if !ok {
		t.Fatal("the released server lost its session handle")
	}
	tools, err := sess.ListToolMetas(context.Background())
	if err != nil {
		t.Fatalf("ListToolMetas after release: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("tools after reconnect = %d, want the advertised one", len(tools))
	}
	if secondPid := readFixturePid(t, pidFile); secondPid == firstPid {
		t.Fatalf("the reconnected child reuses the released pid %d", secondPid)
	}
}

// TestReleaseIdleConnectionsRefusesAGenerationStillStarting pins the barrier's
// side of the contract: a generation whose servers have not settled is the one
// thing every surface is waiting on, and it is not idle no matter what the
// clock says.
func TestReleaseIdleConnectionsRefusesAGenerationStillStarting(t *testing.T) {
	exe := buildRunMCPFixture(t)
	server := appcfg.MCPServerConfig{Name: "slow", Transport: "stdio", Command: exe}
	server.Env = map[string]string{fixtureRunDelayEnv: "400", fixtureRunToolsEnv: "alpha"}

	r := mcpLoadTestRunner(t, server)
	a := fakeAgentForMCPReplay(t)
	ld := startMCPLoad(t, r, a)
	defer awaitMCPLoad(t, ld)
	if r.MCPStartup().ReleaseIdleConnections(0) {
		t.Fatal("a release while the generation is starting must be refused")
	}
}

// readFixturePid reads the pid the run fixture wrote for its current process.
func readFixturePid(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(path)
		if err == nil {
			if pid, convErr := strconv.Atoi(strings.TrimSpace(string(raw))); convErr == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("fixture never wrote its pid to %s", path)
	return 0
}

// waitRunFixturePidGone waits until the fixture's process is neither running
// nor a zombie: a zombie is exactly "signalled but never reaped", so it counts
// as still here.
func waitRunFixturePidGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		out, err := exec.Command("ps", "-o", "state=", "-p", strconv.Itoa(pid)).Output()
		if err != nil {
			return
		}
		if last = strings.TrimSpace(string(out)); last == "" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("fixture process %d is still %s after its session was released", pid, last)
}

// TestMCPStartupIdleForReportsWhatIsRunning pins the probe a teardown decision
// relies on: idle means no generation in flight AND no foreground turn that
// finished within the settle window — the same facts the release acts on, as an
// answer. A runner mid-turn must read as busy however stale the clock is.
func TestMCPStartupIdleForReportsWhatIsRunning(t *testing.T) {
	exe := buildRunMCPFixture(t)
	server := appcfg.MCPServerConfig{Name: "slow", Transport: "stdio", Command: exe}
	server.Env = map[string]string{fixtureRunDelayEnv: "400", fixtureRunToolsEnv: "alpha"}

	r := mcpLoadTestRunner(t, server)
	a := fakeAgentForMCPReplay(t)
	ld := startMCPLoad(t, r, a)
	defer awaitMCPLoad(t, ld)
	if r.MCPStartup().IdleFor(0) {
		t.Fatal("a generation still starting must read as busy")
	}
	awaitMCPLoad(t, ld)
	if !r.MCPStartup().IdleFor(0) {
		t.Fatal("a settled, untouched runner must read as idle")
	}
	// A foreground turn in flight vetoes the probe outright, settle time zero
	// notwithstanding: the connections belong to the turn using them.
	r.foreground.enter()
	if r.MCPStartup().IdleFor(0) {
		t.Fatal("a runner with a foreground turn must read as busy")
	}
	r.foreground.leave()
	if r.MCPStartup().IdleFor(time.Hour) {
		t.Fatal("a turn that just ended is inside the settle window")
	}
	if !r.MCPStartup().IdleFor(0) {
		t.Fatal("a turn that just ended with no settle window must read as idle")
	}
}

// primaryModelTestKeyEnv is the env var the fixtures below expand api_key from.
const primaryModelTestKeyEnv = "FOREBRAIN_PRIMARY_MODEL_TEST_KEY"

// newPrimaryModelRoot builds an in-memory config whose main agent runs the
// given provider entries in order.
func newPrimaryModelRoot(entries ...appcfg.AgentLLMProviderConfig) *appcfg.Root {
	return &appcfg.Root{Agents: appcfg.AgentsSection{Definitions: map[string]appcfg.AgentDefinition{
		"main": {Primary: true, LLMProviders: entries},
	}}}
}

func primaryModelEntry(provider, model, apiKey, baseURL string, params appcfg.LLMRequestParams) appcfg.AgentLLMProviderConfig {
	return appcfg.AgentLLMProviderConfig{Provider: provider, Model: model, APIKey: apiKey, BaseURL: baseURL, Params: params}
}

// loadPrimaryModelRunner builds a runner over cfg and loads it once.
func loadPrimaryModelRunner(t *testing.T, cfg *appcfg.Root) *Runner {
	t.Helper()
	t.Setenv(primaryModelTestKeyEnv, "test-key")
	r := &Runner{Deps: &Deps{Home: t.TempDir(), AgentName: "main", AppCfg: cfg}}
	if err := r.Load(); err != nil {
		t.Fatalf("Runner.Load: %v", err)
	}
	return r
}

func requireSelection(t *testing.T, r *Runner, provider, model, effort string) {
	t.Helper()
	got := PrimaryModelSelection(r)
	if !got.Set || got.Provider != provider || got.Model != model || got.Effort != effort {
		t.Fatalf("selection = %+v, want %s/%s effort %q", got, provider, model, effort)
	}
}

// stubPrimaryModelLLM answers one plain text result, so a recovered runner can
// serve a turn offline.
type stubPrimaryModelLLM struct{}

func (stubPrimaryModelLLM) Execute(context.Context, []llm.Message, []*llm.Tool) (*llm.Result, error) {
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("ok")})
	return &llm.Result{Message: &msg}, nil
}

func TestFirstLoadPinsFirstResolvedEntry(t *testing.T) {
	cfg := newPrimaryModelRoot(appcfg.AgentLLMProviderConfig{
		Provider: "openai",
		Models:   appcfg.StringList{"gpt-m1", "gpt-m2"},
		APIKey:   "${" + primaryModelTestKeyEnv + "}",
		BaseURL:  "http://localhost:0/v1",
		Params:   appcfg.LLMRequestParams(`{"reasoning":{"effort":"low"}}`),
	})
	r := loadPrimaryModelRunner(t, cfg)
	// The Models list form resolves to one entry per model; the first is
	// pinned with its concrete effort.
	requireSelection(t, r, "openai", "gpt-m1", "low")
	if provider, model := PrimaryModel(r); provider != "openai" || model != "gpt-m1" {
		t.Fatalf("PrimaryModel = %s/%s, want openai/gpt-m1", provider, model)
	}
}

func TestLoadKeepsPinnedModelWhenConfigReordersProviders(t *testing.T) {
	key := "${" + primaryModelTestKeyEnv + "}"
	cfg := newPrimaryModelRoot(
		primaryModelEntry("openai", "gpt-a", key, "http://a.test/v1", nil),
		primaryModelEntry("openai", "gpt-b", key, "http://b.test/v1", nil),
	)
	r := loadPrimaryModelRunner(t, cfg)
	requireSelection(t, r, "openai", "gpt-a", "")

	reordered := newPrimaryModelRoot(
		primaryModelEntry("openai", "gpt-b", key, "http://b.test/v1", nil),
		primaryModelEntry("openai", "gpt-a", key, "http://a.test/v1", nil),
	)
	if err := r.LoadConfig(reordered); err != nil {
		t.Fatalf("LoadConfig(reordered): %v", err)
	}
	requireSelection(t, r, "openai", "gpt-a", "")
}

func TestLoadKeepsConcreteEffortAcrossReload(t *testing.T) {
	key := "${" + primaryModelTestKeyEnv + "}"
	t.Run("empty effort stays empty", func(t *testing.T) {
		cfg := newPrimaryModelRoot(primaryModelEntry("openai", "gpt-a", key, "http://a.test/v1", nil))
		r := loadPrimaryModelRunner(t, cfg)
		requireSelection(t, r, "openai", "gpt-a", "")

		withFileEffort := newPrimaryModelRoot(primaryModelEntry("openai", "gpt-a", key, "http://a.test/v1",
			appcfg.LLMRequestParams(`{"reasoning":{"effort":"high"}}`)))
		if err := r.LoadConfig(withFileEffort); err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		requireSelection(t, r, "openai", "gpt-a", "")
	})
	t.Run("pinned effort survives a file change", func(t *testing.T) {
		cfg := newPrimaryModelRoot(primaryModelEntry("openai", "gpt-a", key, "http://a.test/v1",
			appcfg.LLMRequestParams(`{"reasoning":{"effort":"medium"}}`)))
		r := loadPrimaryModelRunner(t, cfg)
		requireSelection(t, r, "openai", "gpt-a", "medium")

		changed := newPrimaryModelRoot(primaryModelEntry("openai", "gpt-a", key, "http://a.test/v1",
			appcfg.LLMRequestParams(`{"reasoning":{"effort":"high"}}`)))
		if err := r.LoadConfig(changed); err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		requireSelection(t, r, "openai", "gpt-a", "medium")
	})
}

func TestLoadRefreshesPinnedProviderDetails(t *testing.T) {
	key := "${" + primaryModelTestKeyEnv + "}"
	cfg := newPrimaryModelRoot(primaryModelEntry("openai", "gpt-a", key, "http://old.test/v1", nil))
	r := loadPrimaryModelRunner(t, cfg)
	if got := PrimaryEndpoint(r); got != "http://old.test/v1" {
		t.Fatalf("endpoint before reload = %q", got)
	}

	rotated := newPrimaryModelRoot(primaryModelEntry("openai", "gpt-a", "rotated-key", "http://new.test/v1",
		appcfg.LLMRequestParams(`{"top_p":0.7,"reasoning":{"effort":"high"}}`)))
	if err := r.LoadConfig(rotated); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	requireSelection(t, r, "openai", "gpt-a", "")
	if got := PrimaryEndpoint(r); got != "http://new.test/v1" {
		t.Fatalf("endpoint after reload = %q, want the refreshed base URL", got)
	}
	snap := r.primaryState.snapshot.Load()
	if snap == nil || snap.provider.APIKey != "rotated-key" {
		t.Fatalf("snapshot credentials did not refresh: %+v", snap)
	}
	// The config itself is not rewritten to the effective entry: the pinned
	// empty effort overlay must not clear the file's effort.
	if got := appcfg.ReasoningEffort(rotated.Agents.Definitions["main"].LLMProviders[0].Params); got != "high" {
		t.Fatalf("reloaded config entry's effort was cleared: %q", got)
	}
}

func TestLoadFallsBackWhenPinnedModelRemoved(t *testing.T) {
	key := "${" + primaryModelTestKeyEnv + "}"
	cfg := newPrimaryModelRoot(
		primaryModelEntry("openai", "gpt-a", key, "http://a.test/v1", nil),
		primaryModelEntry("openai", "gpt-b", key, "http://b.test/v1", nil),
	)
	r := loadPrimaryModelRunner(t, cfg)

	withoutPin := newPrimaryModelRoot(primaryModelEntry("openai", "gpt-b", key, "http://b.test/v1", nil))
	if err := r.LoadConfig(withoutPin); err != nil {
		t.Fatalf("LoadConfig without the pinned pair: %v", err)
	}
	requireSelection(t, r, "openai", "gpt-b", "")
}

func TestSetPrimaryModelRequiresExactPair(t *testing.T) {
	key := "${" + primaryModelTestKeyEnv + "}"
	cfg := newPrimaryModelRoot(
		primaryModelEntry("openai", "gpt-a", key, "http://a.test/v1", nil),
		primaryModelEntry("openai", "gpt-b", key, "http://b.test/v1", nil),
	)
	r := loadPrimaryModelRunner(t, cfg)

	err := r.SetPrimaryModel("openai", "gone-model", nil)
	if !IsPrimaryModelNotConfigured(err) {
		t.Fatalf("missing pair error = %v, want not-configured", err)
	}
	requireSelection(t, r, "openai", "gpt-a", "")
	if r.Agent() == nil {
		t.Fatal("agent lost after a refused selection")
	}

	// An explicit effort, including "", is overlaid exactly.
	if err := r.SetPrimaryModel("openai", "gpt-b", strPtr("high")); err != nil {
		t.Fatalf("SetPrimaryModel with effort: %v", err)
	}
	requireSelection(t, r, "openai", "gpt-b", "high")
	empty := ""
	if err := r.SetPrimaryModel("openai", "gpt-b", &empty); err != nil {
		t.Fatalf("SetPrimaryModel with empty effort: %v", err)
	}
	requireSelection(t, r, "openai", "gpt-b", "")
	// nil effort derives the entry's configured effort once.
	if err := r.SetPrimaryModel("openai", "gpt-a", nil); err != nil {
		t.Fatalf("SetPrimaryModel without effort: %v", err)
	}
	requireSelection(t, r, "openai", "gpt-a", "")
}

func TestSetPrimaryModelNoOpForSameSelection(t *testing.T) {
	key := "${" + primaryModelTestKeyEnv + "}"
	cfg := newPrimaryModelRoot(
		primaryModelEntry("openai", "gpt-a", key, "http://a.test/v1", nil),
		primaryModelEntry("openai", "gpt-b", key, "http://b.test/v1", nil),
	)
	r := loadPrimaryModelRunner(t, cfg)
	agentBefore := r.Agent()
	if err := r.SetPrimaryModel("openai", "gpt-a", strPtr("")); err != nil {
		t.Fatalf("no-op SetPrimaryModel: %v", err)
	}
	if r.Agent() != agentBefore {
		t.Fatal("a no-op selection rebuilt the agent")
	}
}

func TestSetPrimaryModelRollsBackOnLoadFailure(t *testing.T) {
	key := "${" + primaryModelTestKeyEnv + "}"
	// gpt-b resolves but cannot build (a provider no catalog knows, so no
	// environment variable can rescue it), while the pinned gpt-a stays
	// buildable.
	cfg := newPrimaryModelRoot(
		primaryModelEntry("openai", "gpt-a", key, "http://a.test/v1", nil),
		primaryModelEntry("unknownprov", "gpt-b", "k", "http://b.test/v1", nil),
	)
	r := loadPrimaryModelRunner(t, cfg)

	err := r.SetPrimaryModel("unknownprov", "gpt-b", nil)
	if err == nil {
		t.Fatal("unbuildable selection returned nil")
	}
	if IsPrimaryModelRuntimeUncertain(err) {
		t.Fatalf("rollback succeeded but the error is classified uncertain: %v", err)
	}
	requireSelection(t, r, "openai", "gpt-a", "")
	// A rollback rebuilds the runtime; the contract is a published agent on
	// the previous selection, not pointer identity with the old one.
	if r.Agent() == nil {
		t.Fatal("rollback did not restore a published agent")
	}
	if r.runtimeHealthErr() != nil {
		t.Fatalf("runner unhealthy after a successful rollback: %v", r.runtimeHealthErr())
	}
}

// TestSetPrimaryModelRuntimeUncertainFailsClosed injects a build failure the
// rollback cannot escape: with auto_review on, the guardian reviewer resolves
// config order, so a config whose first entry cannot build fails every load —
// the candidate's and the snapshot rebuild's alike.
func TestSetPrimaryModelRuntimeUncertainFailsClosed(t *testing.T) {
	t.Setenv(primaryModelTestKeyEnv, "test-key")
	restore := SetLLMOverrideForTest(stubPrimaryModelLLM{})
	defer restore()
	key := "${" + primaryModelTestKeyEnv + "}"
	good := newPrimaryModelRoot(
		primaryModelEntry("openai", "gpt-a", key, "http://a.test/v1", nil),
		primaryModelEntry("openai", "gpt-b", key, "http://b.test/v1", nil),
	)
	r := &Runner{Deps: &Deps{Home: t.TempDir(), AgentName: "main", AppCfg: good}}
	if err := r.Load(); err != nil {
		t.Fatalf("Runner.Load: %v", err)
	}

	broken := newPrimaryModelRoot(
		primaryModelEntry("unknownprov", "x", "k", "http://x.test/v1", nil),
		primaryModelEntry("openai", "gpt-a", key, "http://a.test/v1", nil),
		primaryModelEntry("openai", "gpt-b", key, "http://b.test/v1", nil),
	)
	broken.ApprovalsReviewer = "auto_review"
	// The broken pointer is what the switch must build against; writing it
	// directly is the injection, LoadConfig is not used here because its own
	// rollback would swap the pointer back before the switch runs.
	r.mu.load.Lock()
	r.AppCfg = broken
	switchErr := r.loadWithIntentLocked(primaryModelExplicit, "openai", "gpt-b", nil)
	r.mu.load.Unlock()

	if !IsPrimaryModelRuntimeUncertain(switchErr) {
		t.Fatalf("double failure classified as %v, want runtime-uncertain", switchErr)
	}
	if err := r.runtimeHealthErr(); err == nil || !errors.Is(err, errRuntimeNeedsReload) {
		t.Fatalf("unhealthy runner gate = %v", err)
	}
	if _, err := r.RunContent(context.Background(), nil); !errors.Is(err, errRuntimeNeedsReload) {
		t.Fatalf("RunContent on an unhealthy runner = %v, want the reload error", err)
	}
	// A complete successful build clears the flag and restores service.
	r.mu.load.Lock()
	r.AppCfg = good
	recoverErr := r.loadWithIntentLocked(primaryModelOrdinary, "", "", nil)
	r.mu.load.Unlock()
	if recoverErr != nil {
		t.Fatalf("recovery load: %v", recoverErr)
	}
	if r.runtimeHealthErr() != nil {
		t.Fatal("runner still unhealthy after a successful rebuild")
	}
	requireSelection(t, r, "openai", "gpt-a", "")
}

// TestLoadConfigRollbackFailureIsVisible injects an initial load failure and
// a rollback failure: the config object the rollback would restore is mutated
// in place to be unloadable first (the shared file going bad between the
// original load and the reload), so both builds fail and neither error may be
// hidden — LoadConfig returns the runtime-uncertain error, the old pointer is
// restored, and the runner refuses work.
func TestLoadConfigRollbackFailureIsVisible(t *testing.T) {
	t.Setenv(primaryModelTestKeyEnv, "test-key")
	restore := SetLLMOverrideForTest(stubPrimaryModelLLM{})
	defer restore()
	key := "${" + primaryModelTestKeyEnv + "}"
	good := newPrimaryModelRoot(primaryModelEntry("openai", "gpt-a", key, "http://a.test/v1", nil))
	r := &Runner{Deps: &Deps{Home: t.TempDir(), AgentName: "main", AppCfg: good}}
	if err := r.Load(); err != nil {
		t.Fatalf("Runner.Load: %v", err)
	}
	// Mutate the rollback target in place: auto_review plus an unloadable
	// first entry fails every later build of this object, including the one
	// LoadConfig's rollback would run.
	good.Agents.Definitions["main"] = appcfg.AgentDefinition{Primary: true, LLMProviders: []appcfg.AgentLLMProviderConfig{
		primaryModelEntry("unknownprov", "x", "k", "http://x.test/v1", nil),
		primaryModelEntry("openai", "gpt-a", key, "http://a.test/v1", nil),
	}}
	good.ApprovalsReviewer = "auto_review"

	broken := newPrimaryModelRoot(
		primaryModelEntry("unknownprov", "y", "k", "http://y.test/v1", nil),
		primaryModelEntry("openai", "gpt-a", key, "http://a.test/v1", nil),
	)
	broken.ApprovalsReviewer = "auto_review"
	err := r.LoadConfig(broken)
	if !IsPrimaryModelRuntimeUncertain(err) {
		t.Fatalf("LoadConfig double failure = %v, want runtime-uncertain", err)
	}
	if r.AppCfg != good {
		t.Fatal("config pointer not restored after a failed reload")
	}
	if _, err := r.RunContent(context.Background(), nil); !errors.Is(err, errRuntimeNeedsReload) {
		t.Fatalf("RunContent after an uncertain reload = %v, want the reload error", err)
	}
}

// TestLoadConfigRollbackRecoversAndReportsPlainError pins the other half: when
// the rollback rebuild succeeds, the returned error is the load failure (not
// an uncertainty the recovery resolved), the old pointer is back, and the
// runner keeps serving on the previous selection.
func TestLoadConfigRollbackRecoversAndReportsPlainError(t *testing.T) {
	key := "${" + primaryModelTestKeyEnv + "}"
	good := newPrimaryModelRoot(primaryModelEntry("openai", "gpt-a", key, "http://a.test/v1", nil))
	r := loadPrimaryModelRunner(t, good)

	broken := newPrimaryModelRoot(
		primaryModelEntry("unknownprov", "x", "k", "http://x.test/v1", nil),
		primaryModelEntry("openai", "gpt-a", key, "http://a.test/v1", nil),
	)
	broken.ApprovalsReviewer = "auto_review"
	err := r.LoadConfig(broken)
	if err == nil {
		t.Fatal("LoadConfig with an unloadable config = nil, want an error")
	}
	if IsPrimaryModelRuntimeUncertain(err) {
		t.Fatalf("successful rollback classified uncertain: %v", err)
	}
	if r.AppCfg != good {
		t.Fatal("config pointer not restored")
	}
	requireSelection(t, r, "openai", "gpt-a", "")
	if r.Agent() == nil {
		t.Fatal("rollback did not restore a published agent")
	}
	if r.runtimeHealthErr() != nil {
		t.Fatalf("runner unhealthy after a successful rollback: %v", r.runtimeHealthErr())
	}
}

func TestLoadResetsPinWhenAgentChanges(t *testing.T) {
	key := "${" + primaryModelTestKeyEnv + "}"
	cfg := &appcfg.Root{Agents: appcfg.AgentsSection{Definitions: map[string]appcfg.AgentDefinition{
		"main":   {Primary: true, LLMProviders: []appcfg.AgentLLMProviderConfig{primaryModelEntry("openai", "gpt-main", key, "http://main.test/v1", nil)}},
		"review": {LLMProviders: []appcfg.AgentLLMProviderConfig{primaryModelEntry("anthropic", "claude-r", key, "http://r.test/v1", nil)}},
	}}}
	t.Setenv(primaryModelTestKeyEnv, "test-key")
	r := &Runner{Deps: &Deps{Home: t.TempDir(), AgentName: "main", AppCfg: cfg}}
	if err := r.Load(); err != nil {
		t.Fatalf("Runner.Load: %v", err)
	}
	requireSelection(t, r, "openai", "gpt-main", "")

	r.AgentName = "review"
	if err := r.Load(); err != nil {
		t.Fatalf("Load after agent switch: %v", err)
	}
	requireSelection(t, r, "anthropic", "claude-r", "")

	// A failed agent-boundary rebuild leaves the runner fail-closed rather
	// than serving the old agent's runtime under the new tenant stores. The
	// broken entry uses a provider no catalog knows, so no environment
	// variable can make it build.
	cfg.Agents.Definitions["broken-agent"] = appcfg.AgentDefinition{
		LLMProviders: []appcfg.AgentLLMProviderConfig{primaryModelEntry("unknownprov", "gpt-broken", "k", "http://b.test/v1", nil)},
	}
	r.AgentName = "broken-agent"
	err := r.Load()
	if !IsPrimaryModelRuntimeUncertain(err) {
		t.Fatalf("failed agent-boundary rebuild = %v, want runtime-uncertain", err)
	}
	if r.runtimeHealthErr() == nil {
		t.Fatal("runner serving after a failed agent-boundary rebuild")
	}
}

func TestSetPrimaryModelConcurrentWithLoadConfig(t *testing.T) {
	key := "${" + primaryModelTestKeyEnv + "}"
	cfg := newPrimaryModelRoot(
		primaryModelEntry("openai", "gpt-a", key, "http://a.test/v1", nil),
		primaryModelEntry("openai", "gpt-b", key, "http://b.test/v1", nil),
	)
	r := loadPrimaryModelRunner(t, cfg)

	var wg sync.WaitGroup
	const rounds = 8
	for i := range rounds {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = r.SetPrimaryModel("openai", "gpt-b", nil)
		}()
		go func(i int) {
			defer wg.Done()
			reordered := newPrimaryModelRoot(
				primaryModelEntry("openai", "gpt-b", key, "http://b.test/v1", nil),
				primaryModelEntry("openai", "gpt-a", key, "http://a.test/v1", nil),
			)
			_ = r.LoadConfig(reordered)
		}(i)
	}
	wg.Wait()
	if err := r.runtimeHealthErr(); err != nil {
		t.Fatalf("runner unhealthy after concurrent switch/reload: %v", err)
	}
	sel := PrimaryModelSelection(r)
	// The switch may have won or the reload may have kept the pin; both are
	// coherent outcomes. A snapshot on any other identity is corruption.
	if !sel.Set || sel.Provider != "openai" || (sel.Model != "gpt-a" && sel.Model != "gpt-b") {
		t.Fatalf("selection after concurrency = %+v", sel)
	}
	if r.Agent() == nil {
		t.Fatal("no published agent after concurrent switch/reload")
	}
}

func TestPrimaryModelReadersAgreeAfterSelection(t *testing.T) {
	key := "${" + primaryModelTestKeyEnv + "}"
	cfg := newPrimaryModelRoot(
		primaryModelEntry("openai", "gpt-a", key, "http://a.test/v1", nil),
		primaryModelEntry("anthropic", "claude-b", key, "http://b.test/v1",
			appcfg.LLMRequestParams(`{"reasoning":{"effort":"high"}}`)),
	)
	r := loadPrimaryModelRunner(t, cfg)
	if err := r.SetPrimaryModel("anthropic", "claude-b", nil); err != nil {
		t.Fatalf("SetPrimaryModel: %v", err)
	}
	if provider, model := PrimaryModel(r); provider != "anthropic" || model != "claude-b" {
		t.Fatalf("PrimaryModel = %s/%s", provider, model)
	}
	if got := PrimaryEndpoint(r); got != "http://b.test/v1" {
		t.Fatalf("PrimaryEndpoint = %q", got)
	}
	if got := PrimaryReasoningEffort(r); got != "high" {
		t.Fatalf("PrimaryReasoningEffort = %q", got)
	}
	requireSelection(t, r, "anthropic", "claude-b", "high")
	if got := r.approvalHookModel(context.Background()); got != "claude-b" {
		t.Fatalf("approvalHookModel = %q", got)
	}
	if r.ContextCompactLLM(context.Background()) == nil {
		t.Fatal("ContextCompactLLM is nil after a selection")
	}
}

func strPtr(s string) *string { return &s }

// TestPrimaryModelStateUnloadedFallback keeps the unloaded-fixture contract:
// a runner that never loaded answers from config order with Set=false.
func TestPrimaryModelStateUnloadedFallback(t *testing.T) {
	cfg := newPrimaryModelRoot(primaryModelEntry("openai", "gpt-a", "k", "http://a.test/v1",
		appcfg.LLMRequestParams(`{"reasoning":{"effort":"low"}}`)))
	state := PrimaryModelSelection(&Runner{Deps: &Deps{AppCfg: cfg, AgentName: "main"}})
	if state.Set || state.Provider != "openai" || state.Model != "gpt-a" || state.Effort != "low" {
		t.Fatalf("unloaded selection = %+v", state)
	}
	if strings.TrimSpace(PrimaryReasoningEffort(nil)) != "" {
		t.Fatal("nil runner effort is non-empty")
	}
}

// recordingSessionLLM records the session model selection each Execute call
// saw in its context, and answers one plain text result.
type recordingSessionLLM struct {
	mu    sync.Mutex
	calls []string
}

func (l *recordingSessionLLM) Execute(ctx context.Context, _ []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	sel, ok := SessionModelSelectionFromContext(ctx)
	l.mu.Lock()
	if ok {
		l.calls = append(l.calls, sel.Provider+"/"+sel.Model+"/"+sel.Effort)
	} else {
		l.calls = append(l.calls, "-")
	}
	l.mu.Unlock()
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("ok")})
	return &llm.Result{Message: &msg}, nil
}

// newSessionRoutingRunner builds a loaded base runner over a shared store
// with two configured providers, the shape a gateway's shared runner has. The
// override must be installed before Load: the base client is captured into
// the chain at load time, and a stub installed later would leave a real
// provider client as the router's inner.
func newSessionRoutingRunner(t *testing.T, override llm.LLM) (*Runner, *state.SessionStore) {
	t.Helper()
	t.Setenv("FOREBRAIN_SESSION_ROUTING_TEST_KEY", "test-key")
	home := t.TempDir()
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(home, "state.sqlite"))
	if err != nil {
		t.Fatalf("OpenStateForTest: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := state.NewSessionStore(db, "main")
	cfg := &appcfg.Root{Agents: appcfg.AgentsSection{Definitions: map[string]appcfg.AgentDefinition{
		"main": {Primary: true, LLMProviders: []appcfg.AgentLLMProviderConfig{
			{Provider: "openai", Model: "gpt-a", APIKey: "${FOREBRAIN_SESSION_ROUTING_TEST_KEY}", BaseURL: "http://a.test/v1"},
			{Provider: "openai", Model: "gpt-b", APIKey: "${FOREBRAIN_SESSION_ROUTING_TEST_KEY}", BaseURL: "http://b.test/v1"},
		}},
	}}}
	if override != nil {
		restore := SetLLMOverrideForTest(override)
		t.Cleanup(restore)
	}
	r := &Runner{Deps: &Deps{Home: home, AgentName: "main", AppCfg: cfg, SessionStore: store}}
	if err := r.Load(); err != nil {
		t.Fatalf("Runner.Load: %v", err)
	}
	return r, store
}

// TestSessionModelRouterRoutesOnContext pins the routing wrapper in
// isolation: a context selection routes to the builder's client, no selection
// falls to the inner client, and a pair the builder cannot resolve falls back
// to the inner client (the ordinary-reload semantics) instead of failing the
// turn.
func TestSessionModelRouterRoutesOnContext(t *testing.T) {
	inner := &recordingSessionLLM{}
	built := &recordingSessionLLM{}
	var buildErr error
	router := wrapSessionModelLLM(inner, func(sel PrimaryModelState) (llm.LLM, error) {
		if buildErr != nil {
			return nil, buildErr
		}
		return built, nil
	})
	ctx := context.Background()

	// No selection: the inner client answers.
	if _, err := router.Execute(ctx, nil, nil); err != nil {
		t.Fatalf("inner execute: %v", err)
	}
	if len(builtSnapshot(built)) != 0 || len(builtSnapshot(inner)) != 1 {
		t.Fatalf("no-selection call did not reach the inner client")
	}

	// A selection routes to the built client.
	selCtx := WithSessionModelSelection(ctx, PrimaryModelState{Provider: "openai", Model: "gpt-b", Effort: "high"})
	if _, err := router.Execute(selCtx, nil, nil); err != nil {
		t.Fatalf("routed execute: %v", err)
	}
	if got := builtSnapshot(built); len(got) != 1 || got[0] != "openai/gpt-b/high" {
		t.Fatalf("routed call observations = %v", got)
	}
	if len(builtSnapshot(inner)) != 1 {
		t.Fatalf("a selected call must not reach the inner client")
	}

	// The builder caches per selection: two calls, one build observation
	// added.
	if _, err := router.Execute(selCtx, nil, nil); err != nil {
		t.Fatalf("second routed execute: %v", err)
	}
	if got := builtSnapshot(built); len(got) != 2 {
		t.Fatalf("cached client not reused: %v", got)
	}

	// A pair the builder cannot resolve falls to the inner client. The key
	// must be one the cache has never built — a cached client is legitimately
	// reused without consulting the builder again.
	buildErr = errors.New("model is not configured for this agent")
	unbuiltCtx := WithSessionModelSelection(ctx, PrimaryModelState{Provider: "openai", Model: "gpt-c"})
	if _, err := router.Execute(unbuiltCtx, nil, nil); err != nil {
		t.Fatalf("unresolvable selection must not fail the turn: %v", err)
	}
	if len(builtSnapshot(inner)) != 2 {
		t.Fatalf("unresolvable selection did not fall back to the inner client")
	}
}

func builtSnapshot(l *recordingSessionLLM) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.calls...)
}

// TestRunContentInjectsSessionModelSelection is the per-session end-to-end
// proof at the run level: two sessions sharing one runner each carry their
// own stored selection into their turn's LLM calls, and a child context that
// already carries a selection keeps it.
func TestRunContentInjectsSessionModelSelection(t *testing.T) {
	rec := &recordingSessionLLM{}
	r, store := newSessionRoutingRunner(t, rec)
	ctx := context.Background()
	for _, sid := range []string{"s1", "s2"} {
		if err := store.Ensure(ctx, sid, sid); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.SaveSessionModelSelection(ctx, "s1", state.SessionModelSelection{Provider: "openai", Model: "gpt-b", Effort: "high"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveSessionModelSelection(ctx, "s2", state.SessionModelSelection{Provider: "openai", Model: "gpt-a"}); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		sid  string
		want string
	}{
		{"s1", "openai/gpt-b/high"},
		{"s2", "openai/gpt-a/"},
	} {
		turnCtx := llm.WithAgentSessionID(context.Background(), tc.sid)
		if _, err := r.Run(turnCtx, "hello"); err != nil {
			t.Fatalf("Run(%s): %v", tc.sid, err)
		}
	}
	got := builtSnapshot(rec)
	if len(got) != 2 || got[0] != "openai/gpt-b/high" || got[1] != "openai/gpt-a/" {
		t.Fatalf("observed selections = %v, want each session's own row", got)
	}

	// A context that already carries a selection (a child inheriting its
	// parent session's) keeps it: injection never overwrites.
	child := llm.WithAgentSessionID(context.Background(), "s-worker")
	child = WithSessionModelSelection(child, PrimaryModelState{Provider: "openai", Model: "gpt-b", Effort: "high"})
	if _, err := r.Run(child, "child work"); err != nil {
		t.Fatalf("Run(child): %v", err)
	}
	got = builtSnapshot(rec)
	if len(got) != 3 || got[2] != "openai/gpt-b/high" {
		t.Fatalf("child context observation = %v, want the inherited selection kept", got)
	}
}

// TestRunContentHealsStaleSessionModelRow pins the gateway-side fallback: a
// stored pair the config no longer offers runs the turn on the runner's
// default and rewrites the stale row, so the next turn and every marker agree.
func TestRunContentHealsStaleSessionModelRow(t *testing.T) {
	rec := &recordingSessionLLM{}
	r, store := newSessionRoutingRunner(t, rec)
	ctx := context.Background()
	if err := store.Ensure(ctx, "s1", "s1"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveSessionModelSelection(ctx, "s1", state.SessionModelSelection{Provider: "openai", Model: "gpt-gone"}); err != nil {
		t.Fatal(err)
	}

	turnCtx := llm.WithAgentSessionID(ctx, "s1")
	if _, err := r.Run(turnCtx, "hello"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// The turn ran on the runner's default, not the stale pair.
	if got := builtSnapshot(rec); len(got) != 1 || got[0] != "-" {
		t.Fatalf("stale row routed somewhere: %v", got)
	}
	// The row now names the default.
	row, ok, err := store.SessionModelSelection(ctx, "s1")
	if err != nil || !ok {
		t.Fatalf("healed row read: ok=%v err=%v", ok, err)
	}
	if row.Model != "gpt-a" {
		t.Fatalf("healed row = %+v, want the runner's default gpt-a", row)
	}
}

// TestResolveSessionModelChoiceEffortSemantics pins the pointer contract: nil
// derives the entry's configured effort once, an explicit pointer (including
// "") pins that exact value, and a missing pair refuses without side effects.
func TestResolveSessionModelChoiceEffortSemantics(t *testing.T) {
	r, _ := newSessionRoutingRunner(t, nil)

	derived, err := ResolveSessionModelChoice(r, "openai", "gpt-a", nil)
	if err != nil {
		t.Fatalf("nil effort: %v", err)
	}
	if derived.Effort != "" {
		t.Fatalf("derived effort = %q, want the entry's (empty)", derived.Effort)
	}
	high := "high"
	pinned, err := ResolveSessionModelChoice(r, "openai", "gpt-a", &high)
	if err != nil || pinned.Effort != "high" {
		t.Fatalf("pinned effort = %+v err=%v", pinned, err)
	}
	empty := ""
	pinnedEmpty, err := ResolveSessionModelChoice(r, "openai", "gpt-a", &empty)
	if err != nil || pinnedEmpty.Effort != "" {
		t.Fatalf("explicit empty = %+v err=%v", pinnedEmpty, err)
	}
	if _, err := ResolveSessionModelChoice(r, "openai", "gpt-gone", nil); !IsPrimaryModelNotConfigured(err) {
		t.Fatalf("missing pair = %v, want not-configured", err)
	}
}

// serializeToolTable renders the prompt-prefix-relevant bytes of a tool table:
// names, descriptions and schemas, in registration order. Two serializations
// that differ describe a prefix every cached session would have to re-bill.
func serializeToolTable(tools []*llm.Tool) string {
	var sb strings.Builder
	for _, t := range tools {
		if t == nil {
			continue
		}
		sb.WriteString(t.Name())
		sb.WriteString("\x00")
		sb.WriteString(t.Description())
		sb.WriteString("\x00")
		schema, err := json.Marshal(t.InputSchema())
		if err != nil {
			sb.WriteString("marshal-error:" + err.Error())
		} else {
			sb.Write(schema)
		}
		sb.WriteString("\x00")
	}
	return sb.String()
}

// The lsp tool sits in the prompt prefix, so its presence and bytes are frozen
// for the runner's lifetime: a server being enabled or disabled, or a config
// reload over the same configuration, must not reshape the tool table (spec
// §8.5). CodeIntelTool is computed once at composition time, never re-derived
// on reload.
func TestLSPToolTableStableAcrossServerChanges(t *testing.T) {
	cfg := &appcfg.Root{
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
	}
	stub := factoryCodeIntelStub{}
	r := &Runner{Deps: &Deps{
		Home: t.TempDir(), AppCfg: cfg,
		CodeIntel: stub, CodeIntelControl: stub, CodeIntelTool: true,
	}}
	if err := r.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	before := serializeToolTable(r.LoadedTools())
	if !strings.Contains(before, "lsp\x00") {
		t.Fatalf("tool table does not contain the lsp tool:\n%s", before)
	}

	// A server toggling on or off is a snapshot change, not a prefix change.
	if err := stub.SetEnabled("gopls", false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	// A config reload over an equal configuration rebuilds the agent without
	// touching the frozen lsp decision.
	reload := *cfg
	if err := r.LoadConfig(&reload); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	after := serializeToolTable(r.LoadedTools())
	if before != after {
		t.Fatalf("tool table changed across server/reload events:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}
