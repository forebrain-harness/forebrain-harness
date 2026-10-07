package process

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	"github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/hook"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

// mcpServerEntry is a real child process that starts and never answers a
// JSON-RPC handshake.
//
// That is the shape of the problem the required-server check exists for: the
// server is present and running, so it is nothing like a missing binary, and the
// startup ends on its own deadline. A sleeping process is used rather than a fake
// because the contract under test is about a real child that never replies — and
// because the deadline path is also the path that has to reclaim it.
func mcpServerEntry(name string, required bool) string {
	entry := "    mcp_servers:\n" +
		"      - name: " + name + "\n" +
		"        transport: stdio\n" +
		"        command: /bin/sleep\n" +
		"        args: [\"30\"]\n"
	if required {
		entry += "        required: true\n"
	}
	return entry + "        startup_timeout: 0.4\n"
}

// countingLLM counts provider requests, so a test can assert that a
// required-server failure stops the run before the model is asked anything.
type countingLLM struct {
	mu    sync.Mutex
	calls int
}

func (c *countingLLM) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func (c *countingLLM) add() {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
}

// envWithMCPServers opens a real environment around a fake provider, with the
// given MCP section in its configuration.
func envWithMCPServers(t *testing.T, mcpSection string) (*Environment, *countingLLM) {
	t.Helper()
	t.Setenv("FOREBRAIN_REQUIRED_MCP_KEY", "test-key")
	counter := &countingLLM{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		counter.add()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "chatcmpl-test", "object": "chat.completion", "created": 1, "model": "gpt-test",
			"choices": []map[string]any{{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": "the model answered"},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5},
		})
	}))
	t.Cleanup(srv.Close)

	home := t.TempDir()
	cfg := "features:\n" +
		"  memories: false\n" +
		"agents:\n" +
		"  defaults:\n" +
		mcpSection +
		"  definitions:\n" +
		"    main:\n" +
		"      llm_providers:\n" +
		"        - provider: openai\n" +
		"          model: gpt-test\n" +
		"          api_key: ${FOREBRAIN_REQUIRED_MCP_KEY}\n" +
		"          base_url: " + srv.URL + "/v1\n" +
		"          params:\n" +
		"            stream: false\n"
	if err := os.WriteFile(filepath.Join(home, "forebrain.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	env, err := Open(context.Background(), OpenOptions{
		Home:          home,
		ConfigPath:    filepath.Join(home, "forebrain.yaml"),
		LaunchDir:     t.TempDir(),
		SessionSource: "webchat",
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(env.Close)
	return env, counter
}

// TestRunExecutorFailsBeforeTheModelWhenARequiredServerDidNotStart pins the
// unattended-run contract at the entry point the gateway uses.
//
// The run must stop at the barrier: an unattended session has nobody watching a
// status line, so proceeding without the tools the configuration requires would
// produce confident work that is missing its inputs. The error names the server
// and keeps the underlying text, and the provider is never asked.
func TestRunExecutorFailsBeforeTheModelWhenARequiredServerDidNotStart(t *testing.T) {
	env, counter := envWithMCPServers(t, mcpServerEntry("required-docs", true))

	executor := env.NewRunExecutor("webchat", RunExecutorOptions{FailOnRequiredMCP: true})
	_, err := executor.Run(context.Background(), turn.TurnRequest{SessionID: "s-required", UserText: "hello"}, func(string) {})
	if err == nil {
		t.Fatal("a run whose required MCP server never started must fail")
	}
	var aggregate *run.MCPRequiredStartupError
	if !errors.As(err, &aggregate) {
		t.Fatalf("error = %T %v, want the aggregate required-startup error", err, err)
	}
	if len(aggregate.Failures) != 1 || aggregate.Failures[0].Server != "required-docs" {
		t.Fatalf("failures = %+v, want the required server named", aggregate.Failures)
	}
	if !strings.Contains(aggregate.Failures[0].Error, "context deadline exceeded") {
		t.Fatalf("failure text = %q, want the deadline the runtime hit", aggregate.Failures[0].Error)
	}
	if counter.count() != 0 {
		t.Fatalf("the provider was called %d times; a required-server failure must stop before the model", counter.count())
	}
}

// TestRunExecutorRunsWhenOnlyAnOptionalServerFailed pins the other half of the
// contract: only what the configuration requires is fatal, so a server the run
// can do without still leaves a working session.
func TestRunExecutorRunsWhenOnlyAnOptionalServerFailed(t *testing.T) {
	env, counter := envWithMCPServers(t, mcpServerEntry("optional-docs", false))

	executor := env.NewRunExecutor("webchat", RunExecutorOptions{FailOnRequiredMCP: true})
	if err := env.Deps.SessionStore.Ensure(context.Background(), "s-optional", "s-optional"); err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Run(context.Background(), turn.TurnRequest{SessionID: "s-optional", UserText: "hello"}, func(string) {}); err != nil {
		t.Fatalf("an optional server that failed must not stop the run: %v", err)
	}
	if counter.count() == 0 {
		t.Fatal("the model was never asked, so the run did not actually proceed")
	}
}

// TestSubagentFailsWhenThePrimaryRunnersRequiredServerDidNotStart pins that a
// child cannot quietly outlive a required-server failure: a subagent reuses the
// primary Runner's generation, so it meets the same barrier.
func TestSubagentFailsWhenThePrimaryRunnersRequiredServerDidNotStart(t *testing.T) {
	env, counter := envWithMCPServers(t, mcpServerEntry("required-docs", true))

	_, _, err := RunSubagentSupervised(context.Background(), env, run.SubagentExecRequest{Task: "do the thing", SessionID: "s-1", WorkerSessionID: "worker-1"})
	if err == nil {
		t.Fatal("a subagent must not run on a generation whose required server failed")
	}
	if !strings.Contains(err.Error(), "required-docs") {
		t.Fatalf("error %q does not name the required server", err)
	}
	if counter.count() != 0 {
		t.Fatalf("the provider was called %d times for a child that had to stop", counter.count())
	}
}

// scriptedReply is one scripted model turn: a plain assistant text, a single
// tool call, or a hard failure that never resolves.
type scriptedReply struct {
	text     string
	toolName string
	toolArgs string
	fail     bool
}

// scriptedLLMServer answers OpenAI chat/completions from a script, one entry
// per request in order; the last entry repeats once the script runs out, so a
// failure entry is a permanent failure. Every raw request body is kept, which
// is what the byte-for-byte golden test compares.
type scriptedLLMServer struct {
	mu     sync.Mutex
	script []scriptedReply
	calls  int
	bodies []string
	srv    *httptest.Server
}

func newScriptedLLMServer(t *testing.T, script []scriptedReply) *scriptedLLMServer {
	t.Helper()
	s := &scriptedLLMServer{script: script}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		idx := s.calls
		if idx >= len(s.script) {
			idx = len(s.script) - 1
		}
		reply := s.script[idx]
		s.calls++
		s.bodies = append(s.bodies, string(body))
		s.mu.Unlock()
		if reply.fail {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "scripted provider outage"}})
			return
		}
		msg := map[string]any{"role": "assistant", "content": reply.text}
		if reply.toolName != "" {
			msg["content"] = nil
			msg["tool_calls"] = []map[string]any{{
				"id":   "call-" + strconv.Itoa(idx),
				"type": "function",
				"function": map[string]any{
					"name":      reply.toolName,
					"arguments": reply.toolArgs,
				},
			}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "chatcmpl-scripted", "object": "chat.completion", "created": 1, "model": "gpt-test",
			"choices": []map[string]any{{"index": 0, "message": msg, "finish_reason": "stop"}},
			// The usage a provider reports is the ground truth the observed-window
			// tracker and the token budget read; a fake "3 tokens" answer to a
			// many-thousand-token request caps the learned window at 3 and every
			// later compaction sizes itself by that fiction. Report the request's
			// own size, the way a real provider does.
			"usage": map[string]any{"prompt_tokens": len(body)/4 + 1, "completion_tokens": 2, "total_tokens": len(body)/4 + 3},
		})
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *scriptedLLMServer) requestBodies() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.bodies...)
}

// envWithScriptedLLM opens a full Environment whose provider answers from the
// script, with subagents enabled so the nested-dispatch regression can drive
// the real subagent_run tool.
func envWithScriptedLLM(t *testing.T, script []scriptedReply, extraCfg ...string) (*Environment, *scriptedLLMServer) {
	return envWithScriptedLLMOnModel(t, "gpt-test", script, extraCfg...)
}

// envWithScriptedLLMOnModel is envWithScriptedLLM on a model id of the test's
// own. A test that sizes anything by the observed window wants an id no other
// test in the process has used: the observed-window store is keyed by
// provider and model, and every request another test recorded under the id
// would cap this test's window at that size.
func envWithScriptedLLMOnModel(t *testing.T, model string, script []scriptedReply, extraCfg ...string) (*Environment, *scriptedLLMServer) {
	t.Helper()
	t.Setenv("FOREBRAIN_SCRIPTED_LLM_KEY", "test-key")
	srv := newScriptedLLMServer(t, script)
	home := t.TempDir()
	cfg := "features:\n" +
		"  memories: false\n"
	for _, extra := range extraCfg {
		cfg += extra
	}
	cfg += "agents:\n" +
		"  defaults:\n" +
		"    enable_subagent: true\n" +
		"  definitions:\n" +
		"    main:\n" +
		"      llm_providers:\n" +
		"        - provider: openai\n" +
		"          model: " + model + "\n" +
		"          api_key: ${FOREBRAIN_SCRIPTED_LLM_KEY}\n" +
		"          base_url: " + srv.srv.URL + "/v1\n" +
		// The compat client streams by default; the scripted server answers
		// with a plain JSON body, so the run must ask for a non-streamed reply.
		"          params:\n" +
		"            stream: false\n"
	if err := os.WriteFile(filepath.Join(home, "forebrain.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	env, err := Open(context.Background(), OpenOptions{
		Home:          home,
		ConfigPath:    filepath.Join(home, "forebrain.yaml"),
		LaunchDir:     t.TempDir(),
		SessionSource: "webchat",
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(env.Close)
	return env, srv
}

// birthWorkerSession mirrors what a dispatch's prepare does before the
// supervised run: it opens the conversation and the worker session and
// creates the supervised run row. The persistence tests drive
// RunSubagentSupervised directly, so the birth is seeded here instead of
// reached through the tool path.
func birthWorkerSession(t *testing.T, env *Environment, conversationID, workerSessionID string) string {
	t.Helper()
	ctx := context.Background()
	if err := env.Deps.SessionStore.Ensure(ctx, conversationID, conversationID); err != nil {
		t.Fatal(err)
	}
	if err := env.Deps.SessionStore.EnsureAt(ctx, workerSessionID, workerSessionID, state.SessionBirth{
		Source:          state.SessionSourceSubagent,
		ParentSessionID: conversationID,
	}); err != nil {
		t.Fatal(err)
	}
	parent, err := env.Deps.RunRT.CreateRun(ctx, conversationID, "dispatching turn")
	if err != nil {
		t.Fatal(err)
	}
	child, err := env.Deps.RunRT.CreateSubagentRun(ctx, parent.ID, workerSessionID, "subagent task")
	if err != nil {
		t.Fatal(err)
	}
	return child.ID
}

func workerRows(t *testing.T, env *Environment, workerSessionID string) []llm.Message {
	t.Helper()
	rows, err := env.Deps.SessionStore.ListTranscriptMessages(context.Background(), workerSessionID, 200)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// TestTypedSubagentWritesItsConversationToTheWorkerSession is the core of
// the persistence plan: a subagent's model context is written into its own
// worker session, row for row, exactly as a primary turn is written — so a
// continuation, a compaction, and a context read all have the same source.
func TestTypedSubagentWritesItsConversationToTheWorkerSession(t *testing.T) {
	env, _ := envWithScriptedLLM(t, []scriptedReply{
		{toolName: "read_file", toolArgs: `{"file_path": "notes.txt"}`},
		{text: "I read the notes."},
	})
	taskFile := filepath.Join(env.LaunchDir, "notes.txt")
	if err := os.WriteFile(taskFile, []byte("the finding"), 0o600); err != nil {
		t.Fatal(err)
	}
	const (
		conversationID = "conv-persist"
		workerID       = "main:conv-persist:worker:aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	)
	childRunID := birthWorkerSession(t, env, conversationID, workerID)
	res, _, err := RunSubagentSupervised(context.Background(), env, run.SubagentExecRequest{Task: "read the notes", SuperviseRunID: childRunID, SessionID: conversationID, WorkerSessionID: workerID, SubagentType: "general-purpose"})
	if err != nil {
		t.Fatalf("RunSubagentSupervised: %v", err)
	}
	if res == nil || res.TextContent() != "I read the notes." {
		t.Fatalf("result = %v, want the scripted answer", res)
	}
	rows := workerRows(t, env, workerID)
	roles := make([]string, 0, len(rows))
	for _, row := range rows {
		roles = append(roles, row.Role)
	}
	want := []string{llm.RoleUser, llm.RoleAssistant, llm.RoleTool, llm.RoleAssistant}
	if len(roles) != len(want) {
		t.Fatalf("worker session rows = %v, want %v", roles, want)
	}
	for i := range want {
		if roles[i] != want[i] {
			t.Fatalf("worker session rows = %v, want %v", roles, want)
		}
	}
	if rows[0].TextContent() != "read the notes" {
		t.Fatalf("user row = %q, want the task verbatim", rows[0].TextContent())
	}
	if len(rows[1].ToolCalls) != 1 || rows[1].ToolCalls[0].Function.Name != "read_file" {
		t.Fatalf("assistant row carries %+v, want the read_file call", rows[1].ToolCalls)
	}
	if rows[2].TextContent() == "" {
		t.Fatal("tool row is empty")
	}
	if rows[3].TextContent() != "I read the notes." {
		t.Fatalf("final assistant row = %q, want the scripted answer", rows[3].TextContent())
	}
}

// TestFailedTypedSubagentKeepsWhatItDid pins the failure half: a subagent
// whose second model call dies has still done work, and that work stays in
// its worker session — user, assistant tool calls, and the tool result, with
// nothing left dangling for the next request to trip over.
func TestFailedTypedSubagentKeepsWhatItDid(t *testing.T) {
	env, _ := envWithScriptedLLM(t, []scriptedReply{
		{toolName: "read_file", toolArgs: `{"file_path": "notes.txt"}`},
		{fail: true},
	})
	taskFile := filepath.Join(env.LaunchDir, "notes.txt")
	if err := os.WriteFile(taskFile, []byte("the finding"), 0o600); err != nil {
		t.Fatal(err)
	}
	const (
		conversationID = "conv-fail"
		workerID       = "main:conv-fail:worker:11111111-2222-3333-4444-555555555555"
	)
	childRunID := birthWorkerSession(t, env, conversationID, workerID)
	_, _, err := RunSubagentSupervised(context.Background(), env, run.SubagentExecRequest{Task: "read the notes", SuperviseRunID: childRunID, SessionID: conversationID, WorkerSessionID: workerID, SubagentType: "general-purpose"})
	if err == nil {
		t.Fatal("expected the scripted failure")
	}
	rows := workerRows(t, env, workerID)
	roles := make([]string, 0, len(rows))
	for _, row := range rows {
		roles = append(roles, row.Role)
	}
	want := []string{llm.RoleUser, llm.RoleAssistant, llm.RoleTool}
	if len(roles) != len(want) {
		t.Fatalf("worker session rows = %v, want %v (the captured partial)", roles, want)
	}
	if rows[0].TextContent() != "read the notes" {
		t.Fatalf("user row = %q, want the task verbatim", rows[0].TextContent())
	}
	// No dangling tool calls: every tool call the stored assistant made is
	// answered by a stored tool row.
	for _, row := range rows {
		for _, call := range row.ToolCalls {
			answered := false
			for _, candidate := range rows {
				if candidate.Role == llm.RoleTool && candidate.ToolCallID == call.ID {
					answered = true
					break
				}
			}
			if !answered {
				t.Fatalf("stored tool call %q has no tool result row", call.ID)
			}
		}
	}
}

// TestApprovalResumeDoesNotRewriteTheSubagentPrompt pins the gate: the first
// attempt writes the dispatch and stops at the approval; the resumed attempt
// must continue that conversation, not open it a second time — the worker
// session ends with exactly one user row.
func TestApprovalResumeDoesNotRewriteTheSubagentPrompt(t *testing.T) {
	env, _ := envWithScriptedLLM(t, []scriptedReply{
		{toolName: "write_file", toolArgs: `{"file_path": "out.txt", "content": "hi"}`},
		{text: "fine, skipping the write."},
		{text: "fine, skipping the write."},
	})
	const (
		conversationID = "conv-gate"
		workerID       = "main:conv-gate:worker:66666666-7777-8888-9999-000000000000"
	)
	childRunID := birthWorkerSession(t, env, conversationID, workerID)
	firstCtx := tool.WithSubagentType(context.Background(), "general-purpose")
	_, _, gateErr := RunSubagentSupervised(firstCtx, env, run.SubagentExecRequest{Task: "write the file", SuperviseRunID: childRunID, SessionID: conversationID, WorkerSessionID: workerID, SubagentType: "general-purpose"})
	var rae *tool.RequiresActionError
	if !errors.As(gateErr, &rae) || rae == nil {
		t.Fatalf("err = %v, want a tool approval gate", gateErr)
	}
	rows := workerRows(t, env, workerID)
	if len(rows) != 1 || rows[0].Role != llm.RoleUser || rows[0].TextContent() != "write the file" {
		t.Fatalf("worker session after the gate = %+v, want exactly the user row", rows)
	}

	// The resumed attempt replays the parked session — the snapshot the gate
	// attached to the error, exactly what the approval path hands back — with
	// the approval answered (denied, like a user pressing "no").
	if len(rae.SessionSnapshot) == 0 {
		t.Fatal("the gate error carries no session snapshot to resume from")
	}
	resumeCtx := tool.WithToolApprovalResume(tool.WithSubagentType(context.Background(), "general-purpose"), &tool.ToolApprovalResumeState{
		Session:    append([]llm.Message(nil), rae.SessionSnapshot...),
		Denied:     true,
		DenyReason: "do not write anything",
	})
	res, _, err := RunSubagentSupervised(resumeCtx, env, run.SubagentExecRequest{Task: "write the file", SuperviseRunID: childRunID, SessionID: conversationID, WorkerSessionID: workerID, SubagentType: "general-purpose"})
	if err != nil {
		t.Fatalf("resumed run: %v", err)
	}
	if res == nil || res.TextContent() != "fine, skipping the write." {
		t.Fatalf("resumed result = %v, want the scripted answer", res)
	}
	rows = workerRows(t, env, workerID)
	userRows := 0
	for _, row := range rows {
		if row.Role == llm.RoleUser {
			userRows++
		}
	}
	if userRows != 1 {
		t.Fatalf("worker session holds %d user rows, want 1 (rows: %+v)", userRows, rows)
	}
	if len(rows) != 4 {
		t.Fatalf("worker session rows = %+v, want user, assistant, tool, assistant", rows)
	}
}

// TestTypedSubagentFirstRequestIsUnchangedByPersistence is the golden test
// the cache rule demands: writing the dispatch into the worker session may
// not change a single byte of the first request. The before side runs the
// same supervision the persistence replaced — run.Run over the same context
// wiring, with no user row written — against the same task and a fresh
// worker session of its own.
func TestTypedSubagentFirstRequestIsUnchangedByPersistence(t *testing.T) {
	env, srv := envWithScriptedLLM(t, []scriptedReply{
		{text: "before"},
		{text: "after"},
	})
	const (
		conversationID = "conv-golden"
		task           = "report the tally"
	)
	if err := env.Deps.SessionStore.Ensure(context.Background(), conversationID, conversationID); err != nil {
		t.Fatal(err)
	}
	stateRoot := config.ActiveStateRoot(env.Root, env.Deps.AppCfg)
	// Before: the worker session stays empty; nothing is written.
	beforeWorker := "main:conv-golden:worker:before-00000000-0000-0000-0000-000000000000"
	beforeBase := AgentContext(tool.WithSubagentType(context.Background(), "general-purpose"), stateRoot, beforeWorker)
	beforeBase = llm.WithPromptCacheKey(beforeBase, conversationID)
	if _, _, err := run.Run(run.Options{
		Runner:       env.Runner,
		Hooks:        env.Hooks,
		AgBase:       beforeBase,
		HC:           hook.HookContext{SessionID: beforeWorker, Channel: "subagent_typed", Trigger: "subagent_run"},
		Input:        task,
		PreviewMax:   4096,
		CreateRunCtx: context.Background(),
	}); err != nil {
		t.Fatalf("before run: %v", err)
	}

	// After: the full supervised path, birth included.
	afterWorker := "main:conv-golden:worker:after-11111111-1111-1111-1111-111111111111"
	childRunID := birthWorkerSession(t, env, conversationID, afterWorker)
	if _, _, err := RunSubagentSupervised(tool.WithSubagentType(context.Background(), "general-purpose"), env, run.SubagentExecRequest{Task: task, SuperviseRunID: childRunID, SessionID: conversationID, WorkerSessionID: afterWorker, SubagentType: "general-purpose"}); err != nil {
		t.Fatalf("after run: %v", err)
	}

	bodies := srv.requestBodies()
	if len(bodies) < 2 {
		t.Fatalf("captured %d requests, want at least one per side", len(bodies))
	}
	if bodies[0] != bodies[1] {
		t.Fatalf("first request changed byte for byte:\nbefore: %s\nafter:  %s", bodies[0], bodies[1])
	}
}

// TestNestedTypedDispatchPersistsCleanly is the regression for the defect
// the plan-013 real-device run handed over: a typed subagent dispatching
// another typed subagent used to die in prepare with `persist subagent run:
// FOREIGN KEY constraint failed`, because the nested run row named the outer
// subagent's worker session — which no dispatch had ever birthed. Births
// happen at dispatch now, so the nested run persists and both conversations
// land in their own worker sessions.
func TestNestedTypedDispatchPersistsCleanly(t *testing.T) {
	env, _ := envWithScriptedLLM(t, []scriptedReply{
		{toolName: "subagent_run", toolArgs: `{"task": "nested lookup", "subagent_type": "explore"}`},
		{text: "nested answer"},
		{text: "outer answer"},
	})
	const (
		conversationID = "conv-nested"
		workerID       = "main:conv-nested:worker:outer-22222222-2222-2222-2222-222222222222"
	)
	childRunID := birthWorkerSession(t, env, conversationID, workerID)
	res, _, err := RunSubagentSupervised(tool.WithSubagentType(context.Background(), "general-purpose"), env, run.SubagentExecRequest{Task: "coordinate the lookup", SuperviseRunID: childRunID, SessionID: conversationID, WorkerSessionID: workerID, SubagentType: "general-purpose"})
	if err != nil {
		t.Fatalf("nested dispatch: %v", err)
	}
	if res == nil || res.TextContent() != "outer answer" {
		t.Fatalf("result = %v, want the outer answer", res)
	}
	// Both conversations exist as their own sessions, born subagents of the
	// conversation chain, and the nested one holds its own exchange.
	born, err := env.Deps.SessionStore.ListSessionsOfSource(context.Background(), state.SessionSourceSubagent, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(born) != 2 {
		t.Fatalf("subagent sessions = %+v, want the outer and the nested worker session", born)
	}
	nested := born[0]
	if nested.ID == workerID {
		nested = born[1]
	}
	if nested.ID == workerID {
		t.Fatalf("both sessions are the outer worker: %+v", born)
	}
	nestedRows := workerRows(t, env, nested.ID)
	if len(nestedRows) != 2 ||
		nestedRows[0].Role != llm.RoleUser || nestedRows[0].TextContent() != "nested lookup" ||
		nestedRows[1].Role != llm.RoleAssistant || nestedRows[1].TextContent() != "nested answer" {
		t.Fatalf("nested worker session rows = %+v, want the nested exchange", nestedRows)
	}
}

// collectingEvents keeps the run events the runner published so a test can
// assert on the compaction lifecycle's routing.
type collectingEvents struct {
	mu     sync.Mutex
	events []event.RunEvent
}

func (c *collectingEvents) Publish(_ context.Context, evt event.RunEvent) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, evt)
	return nil
}

func (c *collectingEvents) compacted() []event.ContextCompactedPayload {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]event.ContextCompactedPayload, 0, len(c.events))
	for _, evt := range c.events {
		if evt.Type != event.RunEventContextCompacted {
			continue
		}
		var payload event.ContextCompactedPayload
		if json.Unmarshal(evt.Payload, &payload) == nil {
			out = append(out, payload)
		}
	}
	return out
}

// TestTypedSubagentContinuationCompactsFirstWhenItNoLongerFits is the
// pre-turn half of the compaction plan: a continuation whose worker session
// has crossed the auto-compact threshold compacts before the new user turn
// is written, under the context the subagent's own calls run in — so the
// boundary predates the new turn, the new turn lands after it, and the
// compaction reports itself under the subagent's roster key, into its own
// view.
//
// The worker session is seeded past the threshold rather than grown by a
// first execution, and the threshold sits below the seeded history but above
// the compacted request: that keeps the run's own mid-turn checkpoint out of
// the picture, so the one boundary in the session is the pre-turn one.
func TestTypedSubagentContinuationCompactsFirstWhenItNoLongerFits(t *testing.T) {
	env, srv := envWithScriptedLLMOnModel(t, "gpt-compact-003", []scriptedReply{
		{text: "summary of the work so far"},
		{text: "continuation answer"},
	}, "compact:\n  model_auto_compact_token_limit: 100000\n")
	const (
		conversationID = "conv-compact"
		workerID       = "main:conv-compact:worker:11111111-2222-3333-4444-555555555555"
	)
	rosterKey := agent.RosterKey("task-compact", "general-purpose")
	childRunID := birthWorkerSession(t, env, conversationID, workerID)
	// A finished prior execution whose one user turn alone crosses the
	// threshold. The local estimator is calibrated by observations earlier
	// tests in this process recorded, so the seed grows until it measures
	// past the threshold rather than assuming a fixed ratio.
	big := strings.Repeat("prior work ", 40000)
	for llm.EstimateText(big) < 120000 {
		big += big[:len(big)/2]
	}
	if err := env.Deps.SessionStore.AppendMessageSequenceForRun(context.Background(), workerID, childRunID, []llm.Message{
		llm.UserMessage(llm.Text(big)),
		llm.AssistantMessage([]llm.ContentPart{llm.Text("the prior answer")}),
	}, "gpt-compact-003", ""); err != nil {
		t.Fatal(err)
	}

	events := &collectingEvents{}
	env.Runner.Events = events
	ctx := tool.WithHookAgentID(context.Background(), rosterKey)
	res, _, err := RunSubagentSupervised(ctx, env, run.SubagentExecRequest{Task: "continue the work", SuperviseRunID: childRunID, SessionID: conversationID, WorkerSessionID: workerID, SubagentType: "general-purpose"})
	if err != nil {
		t.Fatalf("continuation: %v", err)
	}
	if res == nil || res.TextContent() != "continuation answer" {
		t.Fatalf("result = %v, want the scripted continuation", res)
	}

	// Exactly one compaction — the pre-turn one — filed under the roster key.
	compacted := events.compacted()
	if len(compacted) != 1 {
		t.Fatalf("compaction events = %d, want the single pre-turn compaction", len(compacted))
	}
	if compacted[0].AgentID != rosterKey {
		t.Fatalf("compaction AgentID = %q, want the roster key %q", compacted[0].AgentID, rosterKey)
	}

	// The boundary predates the new user turn: the session's rows are the
	// compacted history, then the new turn, then the answer.
	boundaryID, _, err := env.Deps.SessionStore.LatestCompactBoundary(context.Background(), workerID)
	if err != nil {
		t.Fatal(err)
	}
	if boundaryID <= 0 {
		t.Fatal("the worker session has no compaction boundary")
	}
	rows := workerRows(t, env, workerID)
	want := []string{
		llm.RoleUser, // the retained, truncated prior turn
		llm.RoleUser, // the summary checkpoint
		llm.RoleUser, // the continuation's new turn
		llm.RoleAssistant,
	}
	roles := make([]string, 0, len(rows))
	for _, row := range rows {
		roles = append(roles, row.Role)
	}
	if len(rows) != len(want) {
		t.Fatalf("worker session roles = %v, want %v", roles, want)
	}
	for i, role := range want {
		if rows[i].Role != role {
			t.Fatalf("worker session roles = %v, want %v", roles, want)
		}
	}
	if !strings.Contains(rows[1].TextContent(), "summary of the work so far") {
		t.Fatalf("summary row = %q, want the scripted summary", rows[1].TextContent())
	}
	if rows[2].TextContent() != "continue the work" {
		t.Fatalf("new turn row = %q, want the continuation message after the boundary", rows[2].TextContent())
	}
	if rows[3].TextContent() != "continuation answer" {
		t.Fatalf("answer row = %q, want the scripted answer", rows[3].TextContent())
	}
	// The request the continuation actually sent rides on the boundary: the
	// summary checkpoint is in it, ahead of the new turn.
	bodies := srv.requestBodies()
	if len(bodies) != 2 {
		t.Fatalf("provider requests = %d, want the summary request and the continuation request", len(bodies))
	}
	var sent struct {
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(bodies[1]), &sent); err != nil {
		t.Fatal(err)
	}
	summaryAt, turnAt := -1, -1
	for i, msg := range sent.Messages {
		text, _ := msg.Content.(string)
		if strings.Contains(text, "summary of the work so far") && summaryAt < 0 {
			summaryAt = i
		}
		if strings.Contains(text, "continue the work") && turnAt < 0 {
			turnAt = i
		}
	}
	if summaryAt < 0 || turnAt < 0 || summaryAt > turnAt {
		t.Fatalf("continuation request carries the boundary after the new turn: summary at %d, new turn at %d", summaryAt, turnAt)
	}
}
