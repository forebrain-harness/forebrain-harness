// One-shot and batch execution entry points.
package process

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/assembly"
	"github.com/forebrain-harness/forebrain-harness/pkg/hook"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
	"github.com/google/uuid"
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
// scheduler, if one is installed, and then delivers the execution's report to
// the conversation that dispatched it (plan 306). The forward is what lets a
// surface arm the subagent's own continuation, so it keeps running first and
// unchanged; the delivery is the engine's own, so a surface that installed no
// hook — or a process where the peer protocol cannot register — still reports.
func (env *Environment) SubagentExecutionEnded(_ context.Context, end run.SubagentExecutionEnd) {
	if env == nil {
		return
	}
	if env.OnSubagentExecution != nil && env.OnSubagentExecution.Ended != nil {
		env.OnSubagentExecution.Ended(end)
	}
	env.deliverSubagentReport(end)
}

// deliverSubagentReport hands a fire-and-forget subagent's final report to the
// conversation that dispatched it: into the dispatcher's running turn when it
// is still working, or as a turn of its own when it is idle. It is how an owner
// who dispatched with subagent_send and ended their turn still hears the
// result and can finish the job without being prompted.
//
// Three ends are left alone. An awaited execution's dispatcher received the
// report from the blocking call that was waiting for it, so a delivery would be
// the same words twice. A user's cancellation is the user's decision: waking
// the dispatcher would undo it. And a usage limit with a stated reset is the
// subagent's own continuation to run — waking the dispatcher would report a
// failure that is about to be resumed.
func (env *Environment) deliverSubagentReport(end run.SubagentExecutionEnd) {
	sessionID := strings.TrimSpace(end.ConversationSessionID)
	agentKey := strings.TrimSpace(end.AgentKey)
	switch {
	case end.AwaitedByDispatcher, sessionID == "":
		return
	case errors.Is(end.Err, context.Canceled):
		return
	case subagentEndWaitsForItsContinuation(end.Err):
		return
	}
	// The inbox is the one delivery table a conversation's own agent is reached
	// through, whatever the message is; it is nil only before StartPeers has
	// run, where there is no surface to deliver to either.
	if env.peerDelivery == nil {
		slog.Debug("subagent report has no delivery table", "session_id", sessionID, "agent", agentKey)
		return
	}
	prompt := PeerPrompt{
		SessionID: sessionID,
		Text:      subagentReportEnvelope(end),
		Origin:    state.MessageOriginPeer,
		MsgID:     uuid.NewString(),
		FromName:  agentKey,
	}
	// A detached context: the report outlives the execution's own, which is
	// already cancelled when the execution failed.
	if refusal := env.peerDelivery.deliverPrimary(context.Background(), sessionID, prompt); refusal != "" {
		// One attempt, no retry: the conversation may have moved on, and the
		// report is a courtesy to a dispatcher that is no longer waiting.
		slog.Debug("subagent report was not delivered", "session_id", sessionID, "agent", agentKey, "reason", refusal)
	}
}

// subagentEndWaitsForItsContinuation reports whether err is a usage limit whose
// reset time the subagent's own continuation is already waiting for. The
// predicate is the one continuationFor decides with (pkg/turn/submit.go), kept
// literal here so a failure that is about to resume never wakes a dispatcher
// with news that the subagent has not finished.
func subagentEndWaitsForItsContinuation(err error) bool {
	explanation, ok := llm.Explain(err)
	if !ok || explanation.ResetAt.IsZero() {
		return false
	}
	switch explanation.Code {
	case llm.ExplainRateLimitQuota, llm.ExplainRateLimitThrottle:
		return true
	default:
		return false
	}
}

// subagentReportEnvelope renders one finished execution as the message its
// dispatcher reads: the <agent-message> shape a subagent's own peer_send writes
// (pkg/run's peerMessageText), so the surface draws one kind of card for both
// and the name on the card is the roster key of the subagent that finished. A
// failure carries the report the execution managed — empty when it produced
// none — and asks the dispatcher to decide, since only the dispatcher knows
// whether to continue the subagent, replace it, or finish another way.
func subagentReportEnvelope(end run.SubagentExecutionEnd) string {
	agentKey := strings.TrimSpace(end.AgentKey)
	report := strings.TrimSpace(end.Output)
	var b strings.Builder
	if end.Err == nil {
		fmt.Fprintf(&b, "Your subagent %s finished successfully.", agentKey)
		if report != "" {
			b.WriteString("\n\nIts final report:\n\n")
			b.WriteString(report)
		}
		b.WriteString("\n\nContinue the task you dispatched it for: integrate this result, " +
			"complete the remaining work, and deliver the final result.")
	} else {
		fmt.Fprintf(&b, "Your subagent %s finished with an error: %s", agentKey, llm.ExplainError(end.Err))
		if report != "" {
			b.WriteString("\n\nIts partial report:\n\n")
			b.WriteString(report)
		}
		b.WriteString("\n\nDecide how to proceed — continue the subagent, dispatch a replacement, " +
			"or finish the task another way — and deliver the final result.")
	}
	return `<agent-message from="` + agentKey + `">` + b.String() + `</agent-message>`
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
	if err := run.StartSubagentAutoContinue(context.Background(), r, surface, sessionID, agentKey,
		run.Input{Text: text, Parts: []llm.ContentPart{llm.Text(text)}}); err != nil {
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
	if env == nil || env.deps().SessionStore == nil {
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
		turn.PersistAssistantTurn(bg, env.deps().SessionStore, turn.AssistantTurn{
			SessionID: t.WorkerSessionID,
			RunID:     t.RunID,
			Result:    t.Result,
			Model:     t.Model,
			End:       endc,
		})
	case run.IsRequiresAction(t.Err):
		return
	default:
		turn.PersistCancelledTurn(bg, env.deps().SessionStore, turn.CancelledTurn{
			SessionID: t.WorkerSessionID,
			RunID:     t.RunID,
			Captured:  t.Partial,
			Model:     t.Model,
			End:       endc,
		})
	}
}

func (env *Environment) AssembleContextSnapshot(ctx context.Context, hc hook.HookContext, text string) (assembly.AssemblyResult, bool, error) {
	if env == nil || env.ctxHook == nil {
		return assembly.AssemblyResult{}, false, nil
	}
	return env.ctxHook.Assemble(ctx, hc, text)
}
