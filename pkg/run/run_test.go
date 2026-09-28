package run

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

type semanticCaptureLLM struct {
	messages []llm.Message
	tools    []*llm.Tool
}

func (c *semanticCaptureLLM) Execute(ctx context.Context, messages []llm.Message, tools []*llm.Tool) (*llm.Result, error) {
	c.messages = append([]llm.Message(nil), messages...)
	c.tools = append([]*llm.Tool(nil), tools...)
	return &llm.Result{
		Message: &llm.Message{
			Role:  llm.RoleAssistant,
			Parts: []llm.ContentPart{llm.Text("done")},
		},
		Usage: &llm.Usage{InputTokens: 11, OutputTokens: 7},
	}, nil
}

func TestCreateSubagentContextClonesMapsAndMessages(t *testing.T) {
	parent := &CacheSafeParams{
		SystemPrompt:         "sys",
		RenderedSystemPrompt: "rendered sys",
		UserContext:          map[string]string{"u": "1"},
		SystemContext:        map[string]string{"s": "1"},
		ToolUseContext:       map[string]string{"tool": "yes"},
		ForkContextMessages: []llm.Message{
			llm.UserMessage(llm.Text("ctx")),
		},
		ParentMessages: []llm.Message{
			llm.AssistantMessage([]llm.ContentPart{llm.Text("parent assistant")}),
		},
	}
	prompt := []llm.Message{llm.UserMessage(llm.Text("prompt"))}

	ctx, err := CreateSubagentContext(CreateSubagentContextParams{
		CacheSafe:      parent,
		PromptMessages: prompt,
	})
	if err != nil {
		t.Fatalf("CreateSubagentContext: %v", err)
	}

	ctx.UserContext["u"] = "changed"
	ctx.SystemContext["s"] = "changed"
	ctx.ToolUseContext["tool"] = "changed"
	ctx.InitialMessages[0] = llm.AssistantMessage([]llm.ContentPart{llm.Text("mutated")})

	if parent.UserContext["u"] != "1" {
		t.Fatalf("parent user context mutated: %#v", parent.UserContext)
	}
	if parent.SystemContext["s"] != "1" {
		t.Fatalf("parent system context mutated: %#v", parent.SystemContext)
	}
	if parent.ToolUseContext["tool"] != "yes" {
		t.Fatalf("parent tool use context mutated: %#v", parent.ToolUseContext)
	}
	if got := parent.ForkContextMessages[0].TextContent(); got != "ctx" {
		t.Fatalf("parent fork messages mutated: %q", got)
	}
	if got := parent.ParentMessages[0].TextContent(); got != "parent assistant" {
		t.Fatalf("parent messages mutated: %q", got)
	}
	if len(ctx.InitialMessages) != 2 {
		t.Fatalf("initial messages len=%d", len(ctx.InitialMessages))
	}
	if ctx.SystemPrompt != "rendered sys" {
		t.Fatalf("system prompt=%q", ctx.SystemPrompt)
	}
}

func TestRunUsesPromptMessagesFiltersToolsAndRecordsInitialSidechain(t *testing.T) {
	tmp := t.TempDir()
	fake := &semanticCaptureLLM{}
	allowed, err := llm.NewTool("allowed_tool", "allowed", func(ctx context.Context, _ *struct{}) (string, error) {
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("NewTool allowed: %v", err)
	}
	blocked, err := llm.NewTool("blocked_tool", "blocked", func(ctx context.Context, _ *struct{}) (string, error) {
		return "blocked", nil
	})
	if err != nil {
		t.Fatalf("NewTool blocked: %v", err)
	}

	outcome, err := RunFork(context.Background(), RunParams{
		LLM: fake,
		CacheSafe: &CacheSafeParams{
			SystemPrompt:         "fork system",
			RenderedSystemPrompt: "fork system rendered",
			UserContext:          map[string]string{"u": "1"},
			SystemContext:        map[string]string{"s": "1"},
			ToolUseContext:       map[string]string{"tool": "ctx"},
			ForkContextMessages: []llm.Message{
				llm.UserMessage(llm.Text("ctx user")),
			},
			ParentMessages: []llm.Message{
				llm.UserMessage(llm.Text("ctx user")),
			},
		},
		PromptMessages: []llm.Message{
			llm.AssistantMessage([]llm.ContentPart{llm.Text("ctx assistant")}),
			llm.UserMessage(llm.Text("prompt user")),
		},
		CanUseTool:    func(name string) bool { return strings.HasPrefix(name, "allowed") },
		AgentBaseName: "semantic",
		WorkspaceRoot: tmp,
		SessionID:     "session-1",
		ParentRunID:   "parent-1",
		ForkLabel:     "semantic",
		QuerySource:   "semantic",
		RegisterTools: func(reg *ToolRegistry) error {
			if err := reg.Add(allowed); err != nil {
				return err
			}
			return reg.Add(blocked)
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(fake.tools) != 1 || fake.tools[0].Name() != "allowed_tool" {
		t.Fatalf("tools=%v", toolNames(fake.tools))
	}
	if len(fake.messages) != 3 {
		t.Fatalf("messages len=%d", len(fake.messages))
	}
	if fake.messages[0].TextContent() != "ctx user" || fake.messages[1].TextContent() != "ctx assistant" || fake.messages[2].TextContent() != "prompt user" {
		t.Fatalf("unexpected prompt chain: %#v", fake.messages)
	}
	if outcome.RuntimeKind != "forked_subagent" {
		t.Fatalf("runtime kind=%q", outcome.RuntimeKind)
	}
	if outcome.ParentRunID != "parent-1" {
		t.Fatalf("parent run id=%q", outcome.ParentRunID)
	}
	if outcome.ParentSessionID != "session-1" {
		t.Fatalf("parent session id=%q", outcome.ParentSessionID)
	}
	if outcome.QuerySource != "semantic" || outcome.ForkLabel != "semantic" {
		t.Fatalf("observability mismatch: %#v", outcome)
	}

	raw, err := os.ReadFile(outcome.SidechainPath)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", outcome.SidechainPath, err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) < 1 {
		t.Fatalf("sidechain lines=%d", len(lines))
	}
	var first sidechainEnvelope
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("unmarshal first line: %v", err)
	}
	var firstMsgs []llm.Message
	if err := json.Unmarshal(first.Messages, &firstMsgs); err != nil {
		t.Fatalf("unmarshal first messages: %v", err)
	}
	if len(firstMsgs) != 3 {
		t.Fatalf("initial sidechain messages len=%d", len(firstMsgs))
	}
	if firstMsgs[0].TextContent() != "ctx user" || firstMsgs[1].TextContent() != "ctx assistant" || firstMsgs[2].TextContent() != "prompt user" {
		t.Fatalf("unexpected initial sidechain messages: %#v", firstMsgs)
	}
}

func TestRunUsesAgentIDForSubagentSidechainPath(t *testing.T) {
	tmp := t.TempDir()
	fake := &semanticCaptureLLM{}

	outcome, err := RunFork(context.Background(), RunParams{
		LLM: fake,
		CacheSafe: &CacheSafeParams{
			SystemPrompt:         "fork system",
			RenderedSystemPrompt: "fork system",
			ParentMessages: []llm.Message{
				llm.UserMessage(llm.Text("ctx user")),
			},
		},
		UserPrompt:    "inspect auth flow",
		AgentID:       "agent-7",
		AgentType:     "fork",
		WorkspaceRoot: tmp,
		SessionID:     "session-1",
		ForkLabel:     "subagent",
		QuerySource:   "agent:builtin:fork",
		RegisterTools: func(*ToolRegistry) error { return nil },
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	want := SidechainFilePath(tmp, "session-1", "subagent", "agent-7")
	if outcome.SidechainPath != want {
		t.Fatalf("sidechain path=%q want %q", outcome.SidechainPath, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("expected subagent sidechain at agent-id path: %v", err)
	}
}

func TestBuildForkedMessagesPreservesAssistantAndUsesStablePlaceholderPrefix(t *testing.T) {
	parent := llm.AssistantMessage([]llm.ContentPart{llm.Text("previous assistant text")},
		llm.ToolCall{ID: "call-1", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "read_file", Arguments: `{"path":"a.go"}`}},
		llm.ToolCall{ID: "call-2", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "web_search", Arguments: `{"query":"Go files"}`}},
	)
	msgsA, err := BuildForkedMessages("inspect auth flow", parent)
	if err != nil {
		t.Fatalf("BuildForkedMessages A: %v", err)
	}
	msgsB, err := BuildForkedMessages("inspect billing flow", parent)
	if err != nil {
		t.Fatalf("BuildForkedMessages B: %v", err)
	}
	// Expected: assistant(tool_calls) + tool_result per call + user directive
	if len(msgsA) != 4 || len(msgsB) != 4 {
		t.Fatalf("unexpected lengths: %d %d (expected 4: assistant + 2 tool_results + user directive)", len(msgsA), len(msgsB))
	}
	if msgsA[0].Role != llm.RoleAssistant || len(msgsA[0].ToolCalls) != 2 {
		t.Fatalf("assistant tool calls not preserved: %#v", msgsA[0])
	}
	// Tool result messages (one per tool call) with placeholder text.
	for i, callID := range []string{"call-1", "call-2"} {
		if msgsA[1+i].Role != llm.RoleTool || msgsA[1+i].ToolCallID != callID {
			t.Fatalf("tool result %d: role=%s id=%s want role=tool id=%s", i, msgsA[1+i].Role, msgsA[1+i].ToolCallID, callID)
		}
		if got := msgsA[1+i].TextContent(); got != forkPlaceholderResult {
			t.Fatalf("tool result %d content=%q want %q", i, got, forkPlaceholderResult)
		}
	}
	// Last message is the user directive (boilerplate + task, no tool placeholders).
	aDirective := msgsA[3].TextContent()
	bDirective := msgsB[3].TextContent()
	if msgsA[3].Role != llm.RoleUser || msgsB[3].Role != llm.RoleUser {
		t.Fatalf("last message should be user directive: %s %s", msgsA[3].Role, msgsB[3].Role)
	}
	for _, want := range []string{
		"<" + forkBoilerplateTag + ">",
		forkDirectivePrefix + "inspect auth flow",
	} {
		if !strings.Contains(aDirective, want) {
			t.Fatalf("missing %q in directive A", want)
		}
	}
	if strings.Contains(aDirective, "billing") || strings.Contains(bDirective, "auth") {
		t.Fatalf("directive content leaked across sibling-specific suffixes")
	}
	if strings.Split(aDirective, forkDirectivePrefix)[0] != strings.Split(bDirective, forkDirectivePrefix)[0] {
		t.Fatalf("fork prefixes diverged before directive suffix")
	}
}

func TestStripOrphanedToolCalls(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		if got := stripOrphanedToolCalls(nil); got != nil {
			t.Fatalf("nil should stay nil: %#v", got)
		}
	})
	t.Run("no assistant messages", func(t *testing.T) {
		msgs := []llm.Message{
			llm.UserMessage(llm.Text("hello")),
			llm.UserMessage(llm.Text("world")),
		}
		got := stripOrphanedToolCalls(msgs)
		if len(got) != 2 {
			t.Fatalf("len=%d want 2", len(got))
		}
	})
	t.Run("assistant without tool calls untouched", func(t *testing.T) {
		msgs := []llm.Message{
			llm.AssistantMessage([]llm.ContentPart{llm.Text("hello")}),
		}
		got := stripOrphanedToolCalls(msgs)
		if len(got) != 1 || got[0].Role != llm.RoleAssistant {
			t.Fatalf("untouched: %#v", got)
		}
	})
	t.Run("assistant with satisfied tool calls kept", func(t *testing.T) {
		msgs := []llm.Message{
			llm.AssistantMessage(nil,
				llm.ToolCall{ID: "t1", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "f1"}},
			),
			llm.ToolResultMessage("t1", llm.Text("result")),
		}
		got := stripOrphanedToolCalls(msgs)
		if len(got) != 2 {
			t.Fatalf("len=%d want 2", len(got))
		}
		if len(got[0].ToolCalls) != 1 {
			t.Fatalf("tool calls should be preserved when satisfied: %#v", got[0])
		}
	})
	t.Run("orphaned tool calls stripped", func(t *testing.T) {
		// Simulates the fork runtime snapshot: assistant with tool_calls appended,
		// but tool results not yet in the session.
		msgs := []llm.Message{
			llm.UserMessage(llm.Text("before")),
			llm.AssistantMessage(nil,
				llm.ToolCall{ID: "orphan-1", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "subagent"}},
				llm.ToolCall{ID: "orphan-2", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "read"}},
			),
		}
		got := stripOrphanedToolCalls(msgs)
		if len(got) != 2 {
			t.Fatalf("len=%d want 2", len(got))
		}
		if len(got[1].ToolCalls) != 0 {
			t.Fatalf("orphaned tool calls should be stripped: %#v", got[1])
		}
		if got[1].Role != llm.RoleAssistant {
			t.Fatalf("role should stay assistant: %s", got[1].Role)
		}
	})
	t.Run("partial satisfaction — some tool calls orphaned", func(t *testing.T) {
		msgs := []llm.Message{
			llm.AssistantMessage(nil,
				llm.ToolCall{ID: "t1", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "f1"}},
				llm.ToolCall{ID: "t2", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "f2"}},
			),
			llm.ToolResultMessage("t1", llm.Text("done")),
			// t2 has no result — orphaned, so ALL tool_calls should be stripped.
		}
		got := stripOrphanedToolCalls(msgs)
		if len(got[0].ToolCalls) != 0 {
			t.Fatalf("partially satisfied: all tool calls should be stripped: %#v", got[0])
		}
	})
}

func toolNames(in []*llm.Tool) []string {
	out := make([]string, 0, len(in))
	for _, tool := range in {
		if tool == nil {
			out = append(out, "<nil>")
			continue
		}
		out = append(out, tool.Name())
	}
	return out
}
