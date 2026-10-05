package process

import (
	"context"
	"errors"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	"github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/hook"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

func RunSubagentSupervised(ctx context.Context, env *Environment, task string, childRunID string, parentRunID string, sessionID string, workerSessionID string, subagentType string) (*agent.Result, hook.PreHookResult, error) {
	if env == nil {
		return nil, hook.PreHookResult{}, errors.New("nil environment")
	}
	// A child reuses the primary Runner (see the scope note on h.Runner), so its
	// MCP generation is the parent's: this waits for that one generation to
	// settle rather than starting another, and a required server that did not
	// come up stops the child the same way it stops any unattended run.
	if err := awaitRequiredMCP(ctx, env.Runner); err != nil {
		return nil, hook.PreHookResult{}, err
	}
	sid := strings.TrimSpace(workerSessionID)
	if sid == "" {
		sid = strings.TrimSpace(sessionID)
	}
	if sid == "" {
		sid = "default"
	}
	agBase := AgentContext(ctx, config.ActiveStateRoot(env.Root, env.Deps.AppCfg), sid)
	if parentKey := strings.TrimSpace(llm.PromptCacheKeyFromContext(ctx)); parentKey != "" {
		agBase = llm.WithPromptCacheKey(agBase, parentKey)
	} else if parentKey := strings.TrimSpace(sessionID); parentKey != "" {
		agBase = llm.WithPromptCacheKey(agBase, parentKey)
	}
	// Children inherit the parent's mode (plan/agent) from the worker state.
	// Only typed workers whose own policy already forbids file writes are forced
	// to agent mode; a write-capable type such as general-purpose keeps the
	// inherited plan mode so it cannot be used to edit code mid-planning.
	if st := strings.TrimSpace(tool.SubagentTypeFromContext(ctx)); st != "" && tool.TypedSubagentBlocksFileWrites(st) {
		agBase = tool.WithMode(agBase, "agent")
	}
	if tool.IsForkChildFromContext(ctx) {
		agBase = tool.WithForkChild(agBase, true)
	}
	if st := strings.TrimSpace(tool.SubagentTypeFromContext(ctx)); st != "" {
		agBase = tool.WithSubagentType(agBase, st)
		if src := strings.TrimSpace(tool.SubagentDefinitionSourceFromContext(ctx)); src != "" {
			agBase = tool.WithSubagentDefinitionSource(agBase, src)
		}
	}
	channel := "subagent_fork"
	if strings.TrimSpace(subagentType) != "" {
		channel = "subagent_typed"
	}
	hc := hook.HookContext{SessionID: sid, Channel: channel, Trigger: "subagent_run"}
	opts := run.Options{
		RunRT:        env.Deps.RunRT,
		Runner:       env.Runner,
		Hooks:        env.Hooks,
		AgBase:       agBase,
		HC:           hc,
		Input:        strings.TrimSpace(task),
		PreviewMax:   4096,
		CreateRunCtx: ctx,
	}
	if ch := strings.TrimSpace(childRunID); ch != "" {
		opts.SuperviseExistingRunID = ch
	} else if p := strings.TrimSpace(parentRunID); p != "" {
		opts.ParentRunID = p
	}
	return run.Run(opts)
}

// awaitRequiredMCP waits for the runner's MCP generation to settle and reports
// the required servers that did not come up, aggregated in configuration order.
//
// It is the non-interactive half of the ready barrier: the agent's first request
// waits for the tool table either way, but only an unattended run treats a
// missing required server as fatal. A nil runner waits for nothing — a
// composition root with no agent has no MCP configuration either.
func awaitRequiredMCP(ctx context.Context, runner *run.Runner) error {
	if runner == nil {
		return nil
	}
	return runner.MCPStartup().Wait(ctx)
}
