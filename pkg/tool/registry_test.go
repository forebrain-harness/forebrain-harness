package tool

import (
	"context"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
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

func TestLSPToolRegisteredOnlyWhenFrozenOn(t *testing.T) {
	for _, tc := range []struct {
		name           string
		rt             *AgentToolRuntime
		wantRegistered bool
	}{
		{name: "no runtime code intel", rt: &AgentToolRuntime{}, wantRegistered: false},
		{name: "code intel but tool frozen off", rt: &AgentToolRuntime{CodeIntel: nopCodeIntel{}, CodeIntelTool: false}, wantRegistered: false},
		{name: "frozen decision on", rt: &AgentToolRuntime{CodeIntel: nopCodeIntel{}, CodeIntelTool: true}, wantRegistered: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := NewState(t.TempDir())
			a, err := agent.New(registryNoopLLM{}, "main", "test")
			if err != nil {
				t.Fatalf("agent.New: %v", err)
			}
			if err := RegisterDefaultTools(a, st, tc.rt); err != nil {
				t.Fatalf("RegisterDefaultTools: %v", err)
			}
			registered := false
			for _, tool := range a.Tools() {
				if tool.Name() == "lsp" {
					registered = true
				}
			}
			if registered != tc.wantRegistered {
				t.Fatalf("lsp registered = %v, want %v", registered, tc.wantRegistered)
			}
			meta, ok := st.ToolMetaByName("lsp")
			if tc.wantRegistered {
				if !ok {
					t.Fatal("missing ToolMeta for lsp")
				}
				if !meta.ReadOnly || !meta.ConcurrencySafe {
					t.Fatalf("lsp meta flags = %+v, want ReadOnly and ConcurrencySafe", meta)
				}
				if meta.Category != "code" {
					t.Fatalf("lsp meta category = %q, want %q", meta.Category, "code")
				}
				if meta.Description != lspToolDescription {
					t.Fatalf("lsp meta description drifted from appendix B.1")
				}
				return
			}
			if ok {
				t.Fatalf("unexpected ToolMeta for unregistered lsp: %+v", meta)
			}
		})
	}
}

func TestLSPToolRegistrationOrder(t *testing.T) {
	toolNames := func(rt *AgentToolRuntime) []string {
		st := NewState(t.TempDir())
		a, err := agent.New(registryNoopLLM{}, "main", "test")
		if err != nil {
			t.Fatalf("agent.New: %v", err)
		}
		if err := RegisterDefaultTools(a, st, rt); err != nil {
			t.Fatalf("RegisterDefaultTools: %v", err)
		}
		names := make([]string, 0, len(a.Tools()))
		for _, tool := range a.Tools() {
			names = append(names, tool.Name())
		}
		return names
	}
	position := func(names []string, name string) int {
		for i, n := range names {
			if n == name {
				return i
			}
		}
		return -1
	}

	// Without request_permissions the lsp tool follows web_search.
	names := toolNames(&AgentToolRuntime{CodeIntel: nopCodeIntel{}, CodeIntelTool: true})
	if got := position(names, "lsp"); got != position(names, "web_search")+1 {
		t.Fatalf("lsp position = %d in %v, want directly after web_search", got, names)
	}

	// With request_permissions enabled it follows that tool instead.
	cfg := &appcfg.Root{Features: appcfg.FeaturesSection{RequestPermissionsTool: appcfg.BoolPtr(true)}}
	names = toolNames(&AgentToolRuntime{CodeIntel: nopCodeIntel{}, CodeIntelTool: true, Cfg: cfg})
	if got := position(names, "lsp"); got != position(names, "request_permissions")+1 {
		t.Fatalf("lsp position = %d in %v, want directly after request_permissions", got, names)
	}
}
