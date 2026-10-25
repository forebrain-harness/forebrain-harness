package process

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	"github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/hook"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

func RunSubagentSupervised(ctx context.Context, env *Environment, req run.SubagentExecRequest) (*agent.Result, hook.PreHookResult, error) {
	if env == nil {
		return nil, hook.PreHookResult{}, errors.New("nil environment")
	}
	// A child rides the dispatching conversation's runner: every conversation
	// runs on its project's (see RunnerPool), so the child's MCP generation is
	// the parent's — this waits for that one generation to settle rather than
	// starting another, and a required server that did not come up stops the
	// child the same way it stops any unattended run. The base runner is the
	// fallback: it hosts no MCP, so a session with no project runs a child
	// with no MCP rather than borrowing another project's servers.
	runner := env.Runner
	if resolved, err := env.RunnerForTurn(ctx, strings.TrimSpace(req.SessionID)); err == nil && resolved != nil {
		runner = resolved
	}
	if err := awaitRequiredMCP(ctx, runner); err != nil {
		return nil, hook.PreHookResult{}, err
	}
	task := req.Task
	sessionID := req.SessionID
	workerSessionID := req.WorkerSessionID
	subagentType := req.SubagentType
	childRunID := req.SuperviseRunID
	parentRunID := req.ParentRunID
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
	if tool.SubagentBlocksFileWrites(ctx) {
		agBase = tool.WithMode(agBase, "agent")
	}
	if tool.IsForkChildFromContext(ctx) {
		agBase = tool.WithForkChild(agBase, true)
	}
	if st := strings.TrimSpace(tool.SubagentTypeFromContext(ctx)); st != "" {
		agBase = tool.WithSubagentType(agBase, st)
		// A custom subagent's tool policy is configuration, not a type name, so
		// the worker rebuilds it here or the child's GuardTool would fall back
		// to the static table and hand a read-only worker every tool it excluded.
		if policy, ok := tool.SubagentToolPolicyFromContext(ctx); ok {
			agBase = tool.WithSubagentToolPolicy(agBase, policy)
		}
		if src := strings.TrimSpace(tool.SubagentDefinitionSourceFromContext(ctx)); src != "" {
			agBase = tool.WithSubagentDefinitionSource(agBase, src)
		}
	}
	channel := "subagent_fork"
	if strings.TrimSpace(subagentType) != "" {
		channel = "subagent_typed"
	}
	hc := hook.HookContext{SessionID: sid, Channel: channel, Trigger: "subagent_run"}
	started := time.Now()
	runID := strings.TrimSpace(childRunID)
	// The dispatch reaches the worker session the way a surface writes a
	// primary turn's user message: before the run, bound to the run — the
	// session builder reads it back and replaces it with the pipeline-
	// enriched version, so the first request gains no duplicate. An attempt
	// resuming across an approval gate must not write again: the first
	// attempt's row is the one the replay continues from.
	if tool.ToolApprovalResumeFromContext(ctx) == nil && env.Deps.SessionStore != nil {
		// The pre-turn compaction runs before the new user turn is written,
		// the way the conversation's own pre-turn compaction does. A failure
		// only logs — it must not stop the continuation it was making room
		// for, which is how chat_session's maybeAutoCompactBeforeAppend
		// treats a failure too. A first dispatch's empty worker session sits
		// far below any threshold and decides against it on its own.
		if _, _, cerr := run.CompactionService(env.Runner, env.Deps.SessionStore).AutoCompactSession(ctx, sid, ""); cerr != nil {
			slog.Warn("subagent pre-turn compaction skipped", "session_id", sid, "err", cerr)
		}
		rowID, err := turn.PersistUserTurn(ctx, env.Deps.SessionStore, turn.UserTurn{
			SessionID:  sid,
			RunID:      runID,
			ModelInput: strings.TrimSpace(task),
			Parts:      req.Parts,
		})
		if err != nil {
			return nil, hook.PreHookResult{}, err
		}
		// Tell the channel which row this execution's user message is, so a
		// user-driven execution can be withdrawn before the model answers.
		if req.OnUserTurn != nil {
			req.OnUserTurn(rowID)
		}
	}
	// The worker session's own partial capture: a cancelled execution
	// persists what it did. Installing it here also stops the dispatching
	// run's capture from collecting the child's rows.
	capture := run.NewPartialSessionCapture()
	agBase = run.WithPartialSessionCapture(agBase, capture)
	opts := run.Options{
		RunRT:        env.Deps.RunRT,
		Runner:       runner,
		Hooks:        env.Hooks,
		AgBase:       agBase,
		HC:           hc,
		Input:        strings.TrimSpace(task),
		InputParts:   req.Parts,
		PreviewMax:   4096,
		CreateRunCtx: ctx,
		RunIDOut:     &runID,
	}
	if ch := strings.TrimSpace(childRunID); ch != "" {
		opts.SuperviseExistingRunID = ch
	} else if p := strings.TrimSpace(parentRunID); p != "" {
		opts.ParentRunID = p
	}
	res, pre, runErr := run.Run(opts)
	env.PersistSubagentTurn(ctx, run.SubagentTurn{
		WorkerSessionID: sid,
		RunID:           runID,
		Model:           subagentTurnModel(ctx, env, req),
		Result:          res,
		Partial:         capture.Snapshot(),
		Err:             runErr,
		Started:         started,
	})
	return res, pre, runErr
}

// subagentTurnModel names the model this execution ran on: the model its record
// carries, which is the one dispatch decided and the one its requests were
// routed to. A record persisted before dispatches carried their model — a
// continued subagent from an older runtime — has none, and resolves here the
// way its own surfaces did: a dispatch-time override, then a typed subagent's
// own provider chain, then the conversation's model.
func subagentTurnModel(ctx context.Context, env *Environment, req run.SubagentExecRequest) string {
	if model := strings.TrimSpace(req.Model); model != "" {
		return model
	}
	if override, ok := run.SubagentModelOverrideFromContext(ctx); ok {
		return override.Model
	}
	if env == nil || env.Runner == nil {
		return ""
	}
	_, model, _ := run.AgentModel(env.Runner, req.SessionID, &agent.HistoryEntry{AgentType: req.SubagentType})
	return model
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
