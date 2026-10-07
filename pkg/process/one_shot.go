// One-shot and batch execution entry points.
package process

import (
	"context"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/assembly"
	"github.com/forebrain-harness/forebrain-harness/pkg/hook"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

type BatchReporter interface {
	OnWorkItemTerminal(ctx context.Context, runID, workItemID, sessionID, channelID string, failed bool, errMsg string)
}

func (env *Environment) RunSubagentExec(ctx context.Context, req run.SubagentExecRequest) (string, error) {
	res, _, err := RunSubagentSupervised(ctx, env, req)
	if err != nil {
		return "", err
	}
	if res == nil {
		return "", nil
	}
	return res.TextContent(), nil
}

// SubagentExecutionHooks report one subagent execution's start and end to the
// surface that opened the session. A surface installs them once, when it opens
// the session; a process with none installed schedules no subagent
// continuations.
type SubagentExecutionHooks struct {
	// Starting reports that an execution of the subagent whose worker session
	// is named has begun.
	Starting func(workerSessionID string)
	// Ended reports how that execution finished; end.Err is the execution's
	// own error.
	Ended func(end run.SubagentExecutionEnd)
}

// SubagentExecutionStarting forwards a subagent execution's start to the
// surface's scheduler, if one is installed.
func (env *Environment) SubagentExecutionStarting(_ context.Context, workerSessionID string) {
	if env == nil || env.OnSubagentExecution == nil || env.OnSubagentExecution.Starting == nil {
		return
	}
	env.OnSubagentExecution.Starting(workerSessionID)
}

// SubagentExecutionEnded forwards a subagent execution's end to the surface's
// scheduler, if one is installed.
func (env *Environment) SubagentExecutionEnded(_ context.Context, end run.SubagentExecutionEnd) {
	if env == nil || env.OnSubagentExecution == nil || env.OnSubagentExecution.Ended == nil {
		return
	}
	env.OnSubagentExecution.Ended(end)
}

// ContinueSubagent sends an auto-continue prompt to one subagent from a
// surface, so the terminal and the gateway share one delivery instead of each
// writing it. It answers ErrAutoContinueUnavailable when the subagent took
// another execution in the meantime — the continuation would then land behind
// that work rather than resume the stopped one.
func (env *Environment) ContinueSubagent(r *run.Runner, surface run.SubagentSurface, plan turn.AutoContinuePlan, prompt string) error {
	if env == nil || r == nil {
		return turn.ErrAutoContinueUnavailable
	}
	sessionID := strings.TrimSpace(plan.SessionID)
	agentKey := strings.TrimSpace(plan.AgentKey)
	text := strings.TrimSpace(prompt)
	if sessionID == "" || agentKey == "" || text == "" {
		return turn.ErrAutoContinueUnavailable
	}
	delivery, err := run.SendToSubagent(context.Background(), r, surface, sessionID, agentKey,
		run.Input{Text: text, Parts: []llm.ContentPart{llm.Text(text)}}, run.TurnInputModeSteer)
	if err != nil || delivery != run.SubagentDeliveryStarted {
		return turn.ErrAutoContinueUnavailable
	}
	return nil
}

// PersistSubagentTurn writes one subagent execution's own conversation into
// its worker session, through the same turn semantics the primary session's
// surfaces use. It is the only subagent persistence implementation: run.Run
// assembles the conversation, this records it.
//
// A finished execution is written exactly like a primary turn — the store's
// own tail-compare appends only what the session does not hold yet, so the
// replay across an approval gate never duplicates what the first attempt
// already stored. A failed or cancelled one writes the partial session the
// execution captured, so the subagent keeps what it did. A run parked at an
// approval gate writes nothing: the gate's replay runs to a finish and lands
// in one of the branches above, and writing the gate-time list here would
// leave tool_calls rows that the replayed run's own list never contains.
func (env *Environment) PersistSubagentTurn(ctx context.Context, t run.SubagentTurn) {
	if env == nil || env.Deps.SessionStore == nil {
		return
	}
	end := time.Now()
	started := t.Started
	if started.IsZero() {
		started = end
	}
	endc := turn.RunEnd{StartedAt: started, FinishedAt: end, Worked: end.Sub(started)}
	// ctx may already be cancelled — a cancelled execution is exactly the
	// interesting one — so the writes run on a detached context, the way a
	// surface's own persist does.
	bg := context.Background()
	switch {
	case t.Err == nil:
		turn.PersistAssistantTurn(bg, env.Deps.SessionStore, turn.AssistantTurn{
			SessionID: t.WorkerSessionID,
			RunID:     t.RunID,
			Result:    t.Result,
			Model:     t.Model,
			End:       endc,
		})
	case run.IsRequiresAction(t.Err):
		return
	default:
		turn.PersistCancelledTurn(bg, env.Deps.SessionStore, turn.CancelledTurn{
			SessionID: t.WorkerSessionID,
			RunID:     t.RunID,
			Captured:  t.Partial,
			Model:     t.Model,
			End:       endc,
		})
	}
}

func (env *Environment) AssembleContextSnapshot(hc hook.HookContext, text string) (assembly.AssemblyResult, bool, error) {
	if env == nil || env.ctxHook == nil {
		return assembly.AssemblyResult{}, false, nil
	}
	return env.ctxHook.Assemble(hc, text)
}
