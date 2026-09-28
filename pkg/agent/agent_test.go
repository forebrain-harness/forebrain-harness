// Copyright 2026 Simone Vellei
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package agent_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

// -- mocks -------------------------------------------------------------------

// stubLLM returns responses in the order they are given. Successive calls
// beyond the provided list repeat the last element.
type stubLLM struct {
	responses []*llm.Result
	errs      []error
	callIdx   int
	delay     time.Duration
}

func (s *stubLLM) Execute(_ context.Context, _ []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	if s.delay > 0 {
		time.Sleep(s.delay)
	}
	idx := s.callIdx
	if idx >= len(s.responses) {
		idx = len(s.responses) - 1
	}
	s.callIdx++
	return s.responses[idx], s.errs[idx]
}

// makeStub builds a stubLLM from alternating (result, error) pairs.
func makeStub(pairs ...any) *stubLLM {
	s := &stubLLM{}
	for i := 0; i+1 < len(pairs); i += 2 {
		var r *llm.Result
		if pairs[i] != nil {
			r = pairs[i].(*llm.Result)
		}
		var e error
		if pairs[i+1] != nil {
			e = pairs[i+1].(error)
		}
		s.responses = append(s.responses, r)
		s.errs = append(s.errs, e)
	}
	return s
}

// textResult builds a successful text-only LLM result.
func textResult(content string) *llm.Result {
	return &llm.Result{
		Message: &llm.Message{
			Role:  llm.RoleAssistant,
			Parts: []llm.ContentPart{llm.Text(content)},
		},
		Usage: &llm.Usage{InputTokens: 10, OutputTokens: 5},
	}
}

// toolCallResult builds an LLM result with a single tool call.
func toolCallResult(toolName, callID, arguments string) *llm.Result {
	return &llm.Result{
		Message: &llm.Message{
			Role: llm.RoleAssistant,
			ToolCalls: []llm.ToolCall{
				{
					ID:   callID,
					Type: llm.ToolTypeFunction,
					Function: llm.FunctionCall{
						Name:      toolName,
						Arguments: arguments,
					},
				},
			},
		},
	}
}

type recordingTracer struct {
	events []agent.Event
}

func (t *recordingTracer) Trace(event agent.Event) {
	t.events = append(t.events, event)
}

// -- helpers -----------------------------------------------------------------

func mustNew(t *testing.T, client llm.LLM, name, desc string) *agent.Agent {
	t.Helper()
	a, err := agent.New(client, name, desc)
	if err != nil {
		t.Fatalf("agent.New: unexpected error: %v", err)
	}
	return a
}

func mustTool(t *testing.T, name string, fn func(context.Context, *struct{}) (string, error)) *llm.Tool {
	t.Helper()
	tool, err := llm.NewTool(name, "a test tool", fn)
	if err != nil {
		t.Fatalf("llm.NewTool(%q): unexpected error: %v", name, err)
	}
	return tool
}

// -- tests -------------------------------------------------------------------

func TestNew_Validation(t *testing.T) {
	okLLM := makeStub(textResult("hi"), nil)

	tests := []struct {
		name    string
		client  llm.LLM
		agName  string
		desc    string
		wantErr error
	}{
		{
			name:    "nil LLM",
			client:  nil,
			agName:  "agent",
			desc:    "desc",
			wantErr: agent.ErrUndefinedLLM,
		},
		{
			name:    "empty name",
			client:  okLLM,
			agName:  "",
			desc:    "desc",
			wantErr: agent.ErrNameRequired,
		},
		{
			name:    "empty description",
			client:  okLLM,
			agName:  "agent",
			desc:    "",
			wantErr: agent.ErrDescriptionRequired,
		},
		{
			name:   "valid",
			client: okLLM,
			agName: "agent",
			desc:   "a helpful agent",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := agent.New(tc.client, tc.agName, tc.desc)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("want error %v, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestAddTool_DuplicateReturnsError(t *testing.T) {
	a := mustNew(t, makeStub(textResult("hi"), nil), "agent", "desc")

	tool := mustTool(t, "my_tool", func(_ context.Context, _ *struct{}) (string, error) {
		return "ok", nil
	})

	if err := a.AddTool(tool); err != nil {
		t.Fatalf("first AddTool: unexpected error: %v", err)
	}

	err := a.AddTool(tool)
	var alreadyExists *agent.ToolAlreadyExistsError
	if !errors.As(err, &alreadyExists) {
		t.Fatalf("expected ToolAlreadyExistsError, got %v", err)
	}
	if alreadyExists.Name != "my_tool" {
		t.Fatalf("expected error for tool %q, got %q", "my_tool", alreadyExists.Name)
	}
}

func TestAddHandoff_DuplicateReturnsError(t *testing.T) {
	a := mustNew(t, makeStub(textResult("hi"), nil), "orchestrator", "orchestrates")
	target := mustNew(t, makeStub(textResult("done"), nil), "worker", "does work")

	if err := a.AddHandoff(target); err != nil {
		t.Fatalf("first AddHandoff: unexpected error: %v", err)
	}

	err := a.AddHandoff(target)
	var alreadyExists *agent.ToolAlreadyExistsError
	if !errors.As(err, &alreadyExists) {
		t.Fatalf("expected ToolAlreadyExistsError on second AddHandoff, got %v", err)
	}
}

func TestRun_Simple(t *testing.T) {
	stub := makeStub(textResult("Hello, world!"), nil)
	a := mustNew(t, stub, "agent", "a helpful agent")

	result, err := a.Run(context.Background(), llm.Text("hi"))
	if err != nil {
		t.Fatalf("Run: unexpected error: %v", err)
	}
	if result.TextContent() != "Hello, world!" {
		t.Fatalf("expected %q, got %q", "Hello, world!", result.TextContent())
	}
	if len(result.HandoffAgents) != 0 {
		t.Fatalf("expected no handoff agents, got %v", result.HandoffAgents)
	}
}

func TestRun_LLMError(t *testing.T) {
	stub := makeStub(nil, errors.New("model unavailable"))
	a := mustNew(t, stub, "agent", "a helpful agent")

	_, err := a.Run(context.Background(), llm.Text("hi"))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestRun_ToolCall_Success(t *testing.T) {
	toolInvoked := false

	tool := mustTool(t, "echo_tool", func(_ context.Context, _ *struct{}) (string, error) {
		toolInvoked = true
		return "echo_result", nil
	})

	// First call: tool call. Second call: final text.
	stub := &stubLLM{
		responses: []*llm.Result{
			toolCallResult("echo_tool", "call-1", "{}"),
			textResult("done"),
		},
		errs: []error{nil, nil},
	}

	a := mustNew(t, stub, "agent", "desc")
	if err := a.AddTool(tool); err != nil {
		t.Fatalf("AddTool: %v", err)
	}

	result, err := a.Run(context.Background(), llm.Text("do it"))
	if err != nil {
		t.Fatalf("Run: unexpected error: %v", err)
	}
	if !toolInvoked {
		t.Fatal("expected tool to be invoked")
	}
	if result.TextContent() != "done" {
		t.Fatalf("expected %q, got %q", "done", result.TextContent())
	}
}

func TestRun_ToolCall_ToolErrors_AgentContinues(t *testing.T) {
	// A tool that errors produces an error message in the conversation rather
	// than terminating the agent loop.
	errTool := mustTool(t, "fail_tool", func(_ context.Context, _ *struct{}) (string, error) {
		return "", errors.New("tool broke")
	})

	stub := &stubLLM{
		responses: []*llm.Result{
			toolCallResult("fail_tool", "call-1", "{}"),
			textResult("recovered"),
		},
		errs: []error{nil, nil},
	}

	a := mustNew(t, stub, "agent", "desc")
	if err := a.AddTool(errTool); err != nil {
		t.Fatalf("AddTool: %v", err)
	}

	result, err := a.Run(context.Background(), llm.Text("break it"))
	if err != nil {
		t.Fatalf("Run: unexpected error: %v", err)
	}
	if result.TextContent() != "recovered" {
		t.Fatalf("expected %q, got %q", "recovered", result.TextContent())
	}
}

func TestRun_Handoff(t *testing.T) {
	worker := mustNew(t, makeStub(textResult("worker done"), nil), "worker", "does work")

	// First call returns a handoff tool call; the agent should set HandoffAgent.
	orchestrator := mustNew(t,
		makeStub(
			toolCallResult("handoff_to_worker", "h-1", `{"context":"go work"}`),
			nil,
		),
		"orchestrator", "orchestrates",
	)

	if err := orchestrator.AddHandoff(worker); err != nil {
		t.Fatalf("AddHandoff: %v", err)
	}

	result, err := orchestrator.Run(context.Background(), llm.Text("delegate"))
	if err != nil {
		t.Fatalf("Run: unexpected error: %v", err)
	}
	if len(result.HandoffAgents) == 0 {
		t.Fatal("expected HandoffAgents to be set")
	}
	if result.HandoffAgents[0].Name() != "worker" {
		t.Fatalf("expected handoff to %q, got %q", "worker", result.HandoffAgents[0].Name())
	}
}

func TestRun_EmptyInput(t *testing.T) {
	stub := makeStub(textResult("what can I help with?"), nil)
	a := mustNew(t, stub, "agent", "desc")

	result, err := a.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: unexpected error: %v", err)
	}
	if result.TextContent() == "" {
		t.Fatal("expected non-empty result")
	}
}

// multiToolCallResult builds an LLM result that requests multiple tool calls in one turn.
func multiToolCallResult(calls ...llm.ToolCall) *llm.Result {
	return &llm.Result{
		Message: &llm.Message{
			Role:      llm.RoleAssistant,
			ToolCalls: calls,
		},
	}
}

func toolCall(toolName, callID, arguments string) llm.ToolCall {
	return llm.ToolCall{
		ID:       callID,
		Type:     llm.ToolTypeFunction,
		Function: llm.FunctionCall{Name: toolName, Arguments: arguments},
	}
}

func TestSanitizeToolName(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"my-agent", "my-agent"},
		{"My Agent", "My_Agent"},
		{"agent v2.0!", "agent_v2_0_"},
		{"agent(v2)", "agent_v2_"},
		{strings.Repeat("x", 70), strings.Repeat("x", 64)},
		{"", "agent"},
		{"!@#$", "____"},
		{"already_clean-name", "already_clean-name"},
	}
	for _, tc := range tests {
		got := agent.SanitizeToolName(tc.in)
		if got != tc.want {
			t.Errorf("SanitizeToolName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestRun_ParallelToolCalls verifies that when the LLM requests two tool calls
// in the same turn, both execute and both results appear in the final response.
func TestRun_ParallelToolCalls(t *testing.T) {
	var calls [2]int

	toolA := mustTool(t, "tool_a", func(_ context.Context, _ *struct{}) (string, error) {
		calls[0]++
		return "result_a", nil
	})
	toolB := mustTool(t, "tool_b", func(_ context.Context, _ *struct{}) (string, error) {
		calls[1]++
		return "result_b", nil
	})

	stub := &stubLLM{
		responses: []*llm.Result{
			multiToolCallResult(
				toolCall("tool_a", "c1", "{}"),
				toolCall("tool_b", "c2", "{}"),
			),
			textResult("both done"),
		},
		errs: []error{nil, nil},
	}

	a := mustNew(t, stub, "agent", "desc")
	if err := a.AddTool(toolA); err != nil {
		t.Fatalf("AddTool tool_a: %v", err)
	}
	if err := a.AddTool(toolB); err != nil {
		t.Fatalf("AddTool tool_b: %v", err)
	}

	result, err := a.Run(context.Background(), llm.Text("do both"))
	if err != nil {
		t.Fatalf("Run: unexpected error: %v", err)
	}
	if calls[0] != 1 {
		t.Errorf("tool_a called %d times, want 1", calls[0])
	}
	if calls[1] != 1 {
		t.Errorf("tool_b called %d times, want 1", calls[1])
	}
	if result.TextContent() != "both done" {
		t.Errorf("result = %q, want %q", result.TextContent(), "both done")
	}
}

// TestRun_HandoffWithPrecedingToolCall verifies that when the LLM requests a
// regular tool call followed by a handoff in the same turn, both execute and
// the handoff is honoured (previously the regular tool was abandoned if it
// came after the handoff in the slice).
func TestRun_HandoffWithPrecedingToolCall(t *testing.T) {
	regularInvoked := false

	regular := mustTool(t, "regular_tool", func(_ context.Context, _ *struct{}) (string, error) {
		regularInvoked = true
		return "regular_result", nil
	})

	worker := mustNew(t, makeStub(textResult("worker done"), nil), "worker", "does work")

	orchestrator := mustNew(t,
		makeStub(
			multiToolCallResult(
				toolCall("regular_tool", "c1", "{}"),
				toolCall("handoff_to_worker", "c2", `{"context":"go"}`),
			),
			nil,
		),
		"orchestrator", "orchestrates",
	)

	if err := orchestrator.AddTool(regular); err != nil {
		t.Fatalf("AddTool: %v", err)
	}
	if err := orchestrator.AddHandoff(worker); err != nil {
		t.Fatalf("AddHandoff: %v", err)
	}

	result, err := orchestrator.Run(context.Background(), llm.Text("delegate"))
	if err != nil {
		t.Fatalf("Run: unexpected error: %v", err)
	}
	if !regularInvoked {
		t.Error("regular_tool was not invoked; expected it to run even when a handoff is in the same batch")
	}
	if len(result.HandoffAgents) == 0 {
		t.Fatal("expected HandoffAgents to be set")
	}
	if result.HandoffAgents[0].Name() != "worker" {
		t.Errorf("HandoffAgents[0] = %q, want %q", result.HandoffAgents[0].Name(), "worker")
	}
}
