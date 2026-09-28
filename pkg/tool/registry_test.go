package tool

import (
	"context"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

type registryNoopLLM struct{}

func (registryNoopLLM) Execute(context.Context, []llm.Message, []*llm.Tool) (*llm.Result, error) {
	return &llm.Result{Message: &llm.Message{Role: llm.RoleAssistant, Parts: []llm.ContentPart{llm.Text("ok")}}}, nil
}

func TestRegisterRejectsNilInputs(t *testing.T) {
	if err := NewState(t.TempDir()).Register(nil, nil); err == nil || !strings.Contains(err.Error(), "nil agent") {
		t.Fatalf("Register(nil,nil) error = %v", err)
	}
	a, err := agent.New(registryNoopLLM{}, "main", "desc")
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	if err := NewState(t.TempDir()).Register(a, nil); err == nil || !strings.Contains(err.Error(), "nil tool") {
		t.Fatalf("Register(agent,nil) error = %v", err)
	}
}

// A tool name outside ^[a-zA-Z0-9_-]+$ makes the OpenAI Responses API reject
// the entire request with invalid_request_error, killing the turn rather than
// just that tool, so registration has to fail loudly instead.
func TestRegisterRejectsProviderInvalidNames(t *testing.T) {
	for _, name := range []string{"memories/list", "memories.list", "memories list"} {
		a, err := agent.New(registryNoopLLM{}, "main", "desc")
		if err != nil {
			t.Fatalf("agent.New: %v", err)
		}
		tool, err := llm.NewTool(name, "desc", func(context.Context, *struct{}) (string, error) {
			return "ok", nil
		})
		if err != nil {
			t.Fatalf("NewTool(%q): %v", name, err)
		}
		err = NewState(t.TempDir()).Register(a, tool)
		if err == nil || !strings.Contains(err.Error(), "invalid tool name") {
			t.Fatalf("Register(%q) error = %v, want invalid tool name", name, err)
		}
	}
}

func TestRegisterAcceptsProviderValidNames(t *testing.T) {
	for _, name := range []string{"memories_list", "memories_add_ad_hoc_note", "mcp__codegraph__codegraph_explore", "read-file"} {
		a, err := agent.New(registryNoopLLM{}, "main", "desc")
		if err != nil {
			t.Fatalf("agent.New: %v", err)
		}
		tool, err := llm.NewTool(name, "desc", func(context.Context, *struct{}) (string, error) {
			return "ok", nil
		})
		if err != nil {
			t.Fatalf("NewTool(%q): %v", name, err)
		}
		if err := NewState(t.TempDir()).Register(a, tool); err != nil {
			t.Fatalf("Register(%q) error = %v", name, err)
		}
	}
}

func TestRegisterRejectsDuplicateNames(t *testing.T) {
	a, err := agent.New(registryNoopLLM{}, "main", "desc")
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	tool, err := llm.NewTool("dup_tool", "desc", func(context.Context, *struct{}) (string, error) {
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("NewTool: %v", err)
	}
	if err := add(a, tool); err != nil {
		t.Fatalf("Register: %v", err)
	}
	tool2, err := llm.NewTool("dup_tool", "desc", func(context.Context, *struct{}) (string, error) {
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("NewTool2: %v", err)
	}
	if err := add(a, tool2); err == nil {
		t.Fatal("duplicate Register error = nil")
	}
}

// A state's chain is its own middlewares wrapped in the telemetry every tool
// call in the process is observed by. Nothing has to be reset between tests:
// the middlewares belong to the state, and each test builds its own.
func TestStateToolMiddlewaresApplyItsOwnMiddlewareAndTelemetry(t *testing.T) {
	t.Setenv("FOREBRAIN_LLM_HTTP_DEBUG", "0")
	st := NewState(t.TempDir())

	var extraCalled bool
	st.AddToolMiddleware(func(tool *llm.Tool, next llm.ToolHandler) llm.ToolHandler {
		return func(ctx context.Context, arguments string) (any, error) {
			extraCalled = true
			return next(ctx, arguments)
		}
	})

	tool, err := llm.NewTool("registered_tool", "desc", func(context.Context, *struct{}) (string, error) {
		return "registered", nil
	})
	if err != nil {
		t.Fatalf("NewTool: %v", err)
	}
	for _, mw := range st.ToolMiddlewares() {
		tool.Use(mw)
	}

	out, err := tool.Handle(context.Background(), `{}`)
	if err != nil || out != "registered" {
		t.Fatalf("tool.Handle = %#v err=%v", out, err)
	}
	if !extraCalled {
		t.Fatal("registered middleware was not called")
	}
}
