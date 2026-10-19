// Per-type subagent LLM wrappers: provider selection, prompt, and tool filter.
package run

import (
	"context"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// typedSubagentProviderLLM routes LLM Execute calls to a type-specific LLM
// client when the context carries a subagent type that has a dedicated provider
// configured (agents.definitions[<type>].llm_providers in forebrain.yaml). When no
// dedicated LLM exists for the resolved type, the call falls through to the
// inner (main agent) LLM, preserving the default behaviour.
//
// This wrapper sits below typedSubagentPromptLLM and
// typedSubagentToolFilterLLM in the chain so that system-prompt replacement and
// tool filtering still apply on top of the per-type provider. Main-thread
// queries (REPL / SDK) always use the inner LLM regardless of context.
type typedSubagentProviderLLM struct {
	inner    llm.LLM
	typeLLMs map[string]llm.LLM
}

// wrapTypedSubagentProviderLLM wraps inner with per-type LLM routing. If
// typeLLMs is empty or inner is nil the wrapper is a no-op (returns inner or
// nil), avoiding overhead when no per-type providers are configured.
func wrapTypedSubagentProviderLLM(inner llm.LLM, typeLLMs map[string]llm.LLM) llm.LLM {
	if inner == nil {
		return nil
	}
	if len(typeLLMs) == 0 {
		return inner
	}
	return typedSubagentProviderLLM{inner: inner, typeLLMs: typeLLMs}
}

func (w typedSubagentProviderLLM) Execute(ctx context.Context, messages []llm.Message, tools []*llm.Tool) (*llm.Result, error) {
	if !explicitMainThreadQuerySource(QuerySourceFromContext(ctx)) {
		subtype := strings.TrimSpace(tool.SubagentTypeFromContext(ctx))
		if subtype != "" {
			if typed, ok := w.typeLLMs[subtype]; ok && typed != nil {
				return typed.Execute(ctx, messages, tools)
			}
		}
	}
	return w.inner.Execute(ctx, messages, tools)
}

type typedSubagentPromptLLM struct {
	inner llm.LLM
	// prompt answers the system prompt a type runs under. The runtime passes
	// the dispatching primary agent's catalog, so a rewritten built-in or a
	// custom subagent is what actually reaches the model; a nil prompt keeps
	// the built-in definitions.
	prompt func(subtype string) (string, bool)
}

func wrapTypedSubagentPromptLLM(inner llm.LLM, prompt func(subtype string) (string, bool)) llm.LLM {
	if inner == nil {
		return nil
	}
	return typedSubagentPromptLLM{inner: inner, prompt: prompt}
}

func (w typedSubagentPromptLLM) Execute(ctx context.Context, messages []llm.Message, tools []*llm.Tool) (*llm.Result, error) {
	if explicitMainThreadQuerySource(QuerySourceFromContext(ctx)) {
		return w.inner.Execute(ctx, messages, tools)
	}
	subtype := strings.TrimSpace(tool.SubagentTypeFromContext(ctx))
	if subtype != "" {
		if prompt, ok := w.promptFor(subtype); ok {
			// State the file access scope when both roots are known.
			if projectRoot := tool.ProjectRootFromContext(ctx); projectRoot != "" {
				if wsRoot := tool.WorkspaceRootFromContext(ctx); wsRoot != "" {
					prompt = prompt + "\n\n" + agent.FileAccessScopeSystemPrompt(projectRoot, wsRoot)
				}
			}
			messages = replaceSystemPrompt(messages, prompt)
		}
	}
	return w.inner.Execute(ctx, messages, tools)
}

// promptFor answers the system prompt one type runs under: the dispatcher's
// catalog when the wrapper was given one, else the built-in definition.
func (w typedSubagentPromptLLM) promptFor(subtype string) (string, bool) {
	if w.prompt != nil {
		return w.prompt(subtype)
	}
	return agent.SystemPromptForSubtype(subtype)
}

func explicitMainThreadQuerySource(source string) bool {
	source = strings.ToLower(strings.TrimSpace(source))
	return source == "sdk" || strings.HasPrefix(source, "repl_main_thread")
}

func replaceSystemPrompt(messages []llm.Message, prompt string) []llm.Message {
	out := append([]llm.Message(nil), messages...)
	if len(out) == 0 {
		return []llm.Message{llm.SystemMessage(prompt)}
	}
	if out[0].Role == llm.RoleSystem {
		out[0] = llm.SystemMessage(prompt)
		return out
	}
	return append([]llm.Message{llm.SystemMessage(prompt)}, out...)
}

type typedSubagentToolFilterLLM struct {
	inner llm.LLM
}

func wrapTypedSubagentToolFilterLLM(inner llm.LLM) llm.LLM {
	if inner == nil {
		return nil
	}
	return typedSubagentToolFilterLLM{inner: inner}
}

func (w typedSubagentToolFilterLLM) Execute(ctx context.Context, messages []llm.Message, tools []*llm.Tool) (*llm.Result, error) {
	if explicitMainThreadQuerySource(QuerySourceFromContext(ctx)) {
		return w.inner.Execute(ctx, messages, tools)
	}
	subtype := strings.TrimSpace(tool.SubagentTypeFromContext(ctx))
	if subtype != "" {
		tools = filterToolsForSubagentSubtype(ctx, subtype, tools)
	}
	return w.inner.Execute(ctx, messages, tools)
}

func filterToolsForSubagentSubtype(ctx context.Context, subtype string, tools []*llm.Tool) []*llm.Tool {
	if len(tools) == 0 {
		return nil
	}
	if tool.CustomSubagentPolicyMissing(ctx) {
		return nil
	}
	normalized := strings.ToLower(strings.TrimSpace(subtype))
	inputNames := make([]string, 0, len(tools))
	toolByName := make(map[string]*llm.Tool, len(tools))
	for _, tool := range tools {
		if tool == nil {
			continue
		}
		name := strings.TrimSpace(tool.Name())
		if name == "" {
			continue
		}
		inputNames = append(inputNames, name)
		toolByName[name] = tool
	}
	var visible []string
	if policy, ok := tool.SubagentToolPolicyFromContext(ctx); ok {
		// A custom subagent's set is configuration: offer exactly what its
		// policy admits, which never includes the subagent_* family.
		for _, name := range inputNames {
			if !policy.Blocks(name) {
				visible = append(visible, name)
			}
		}
	} else {
		visible = tool.VisibleToolsForSubagentSubtype(normalized, inputNames)
	}
	out := make([]*llm.Tool, 0, len(tools))
	for _, name := range visible {
		if tool := toolByName[name]; tool != nil {
			out = append(out, tool)
		}
	}
	return out
}
