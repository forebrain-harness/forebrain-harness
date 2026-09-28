package run

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/mcp"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// mcpDiscoveryKind names one derived tool: the tools a server's advertised
// capabilities imply rather than the ones its tools/list reported.
type mcpDiscoveryKind string

const (
	mcpDiscoveryPromptsList          mcpDiscoveryKind = "prompts_list"
	mcpDiscoveryPromptGet            mcpDiscoveryKind = "prompt_get"
	mcpDiscoveryResourcesList        mcpDiscoveryKind = "resources_list"
	mcpDiscoveryResourceRead         mcpDiscoveryKind = "resource_read"
	mcpDiscoveryResourceTemplatesGet mcpDiscoveryKind = "resource_templates_list"
)

// mcpDiscoveryTool is the cached definition of one derived tool. Only the kind
// is cached: the name, description and schema are static functions of it, so a
// reloaded segment rebuilds byte-identical definitions without asking the
// server anything — which is the fix for a replay that used to re-derive them
// from a live session and silently dropped them when the session could not be
// reached.
type mcpDiscoveryTool struct {
	Kind mcpDiscoveryKind `json:"kind"`
}

// mcpDiscoveryToolsFor reports the derived tools one server's advertised
// capabilities imply, in a stable order.
func mcpDiscoveryToolsFor(caps mcp.ServerCapabilities) []mcpDiscoveryTool {
	var out []mcpDiscoveryTool
	if caps.Prompts {
		out = append(out,
			mcpDiscoveryTool{Kind: mcpDiscoveryPromptsList},
			mcpDiscoveryTool{Kind: mcpDiscoveryPromptGet},
		)
	}
	if caps.Resources {
		out = append(out,
			mcpDiscoveryTool{Kind: mcpDiscoveryResourcesList},
			mcpDiscoveryTool{Kind: mcpDiscoveryResourceRead},
			mcpDiscoveryTool{Kind: mcpDiscoveryResourceTemplatesGet},
		)
	}
	return out
}

// mcpDiscoveryInvoker performs one derived tool's call on the server it belongs
// to. It is a function rather than a captured session so a replayed definition
// stays callable without the connection the definition was first seen on.
type mcpDiscoveryInvoker func(ctx context.Context, kind mcpDiscoveryKind, arguments string) (string, error)

// registerMCPDiscoveryTools registers this server's derived tools from cached
// definitions.
//
// st is the registering runtime's tool state: the middlewares an MCP tool runs
// in are that runtime's, the same as for any other tool.
func registerMCPDiscoveryTools(a *agent.Agent, st *tool.State, serverName string, defs []mcpDiscoveryTool, call mcpDiscoveryInvoker) int {
	if a == nil || call == nil {
		return 0
	}
	n := 0
	for _, def := range defs {
		tt, err := newMCPDiscoveryTool(def, serverName, call)
		if err != nil {
			continue
		}
		if st.Register(a, tt) == nil {
			n++
		}
	}
	return n
}

func newMCPDiscoveryTool(def mcpDiscoveryTool, serverName string, call mcpDiscoveryInvoker) (*llm.Tool, error) {
	name := mcp.BuildToolName(serverName, string(def.Kind))
	switch def.Kind {
	case mcpDiscoveryPromptsList:
		return llm.NewTool(name, "List MCP prompts from server "+serverName+".", func(ctx context.Context, in *struct{}) (string, error) {
			_ = in
			return call(ctx, def.Kind, "")
		})
	case mcpDiscoveryPromptGet:
		return llm.NewTool(name, "Get MCP prompt by name from server "+serverName+".", func(ctx context.Context, in *struct {
			Name      string `json:"name" jsonschema:"description=Prompt name"`
			Arguments string `json:"arguments,omitempty" jsonschema_description:"Optional JSON object of string key-value template arguments, as a JSON string. Omit when the prompt takes no arguments."`
		}) (string, error) {
			args, err := json.Marshal(map[string]any{"name": strings.TrimSpace(in.Name), "arguments": strings.TrimSpace(in.Arguments)})
			if err != nil {
				return "", err
			}
			return call(ctx, def.Kind, string(args))
		})
	case mcpDiscoveryResourcesList:
		return llm.NewTool(name, "List MCP resources from server "+serverName+".", func(ctx context.Context, in *struct{}) (string, error) {
			_ = in
			return call(ctx, def.Kind, "")
		})
	case mcpDiscoveryResourceRead:
		return llm.NewTool(name, "Read MCP resource by URI from server "+serverName+".", func(ctx context.Context, in *struct {
			URI string `json:"uri" jsonschema:"description=Resource URI"`
		}) (string, error) {
			args, err := json.Marshal(map[string]any{"uri": strings.TrimSpace(in.URI)})
			if err != nil {
				return "", err
			}
			return call(ctx, def.Kind, string(args))
		})
	case mcpDiscoveryResourceTemplatesGet:
		return llm.NewTool(name, "List MCP resource URI templates from server "+serverName+".", func(ctx context.Context, in *struct{}) (string, error) {
			_ = in
			return call(ctx, def.Kind, "")
		})
	default:
		return nil, fmt.Errorf("unknown mcp discovery tool kind %q", def.Kind)
	}
}

// mcpToolCaller binds one server's tools to the registry that owns its
// connection, resolving the live session at call time.
//
// Resolving late is what lets a reload replay the cached tool definitions
// without contacting the server, and it keeps a replayed tool callable after
// the connection was replaced underneath it.
func mcpToolCaller(reg *mcp.Registry, serverName string) func(context.Context, string, json.RawMessage) (string, error) {
	return func(ctx context.Context, toolName string, arguments json.RawMessage) (string, error) {
		sess, ok := reg.GetSession(serverName)
		if !ok {
			return "", fmt.Errorf("mcp server %q is not connected", serverName)
		}
		return sess.CallToolJSON(ctx, toolName, arguments)
	}
}

// mcpDiscoveryCaller is mcpToolCaller for the derived tools: it decodes the
// per-kind arguments and performs the matching session call.
func mcpDiscoveryCaller(reg *mcp.Registry, serverName string) mcpDiscoveryInvoker {
	return func(ctx context.Context, kind mcpDiscoveryKind, arguments string) (string, error) {
		sess, ok := reg.GetSession(serverName)
		if !ok {
			return "", fmt.Errorf("mcp server %q is not connected", serverName)
		}
		switch kind {
		case mcpDiscoveryPromptsList:
			return sess.ListPromptsJSON(ctx)
		case mcpDiscoveryPromptGet:
			var in struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}
			if err := json.Unmarshal([]byte(arguments), &in); err != nil {
				return "", err
			}
			args := map[string]string{}
			if strings.TrimSpace(in.Arguments) != "" {
				if err := json.Unmarshal([]byte(in.Arguments), &args); err != nil {
					return "", err
				}
			}
			return sess.GetPromptJSON(ctx, strings.TrimSpace(in.Name), args)
		case mcpDiscoveryResourcesList:
			return sess.ListResourcesJSON(ctx)
		case mcpDiscoveryResourceRead:
			var in struct {
				URI string `json:"uri"`
			}
			if err := json.Unmarshal([]byte(arguments), &in); err != nil {
				return "", err
			}
			return sess.ReadResourceJSON(ctx, strings.TrimSpace(in.URI))
		case mcpDiscoveryResourceTemplatesGet:
			return sess.ListResourceTemplatesJSON(ctx)
		default:
			return "", fmt.Errorf("unknown mcp discovery tool kind %q", kind)
		}
	}
}
