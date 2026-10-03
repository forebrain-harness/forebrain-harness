package process

import (
	"context"
	"errors"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/hook"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

// runExecutor adapts the Environment to turn.RunExecutor, which is the port
// turn.Service.Submit executes through.
//
// This is the piece the design's canonical turn path was missing: Submit,
// TurnRequest and RunExecutor were all defined, but nothing implemented the
// port, so every surface kept assembling run.Options itself. The adapter
// belongs in process rather than in either surface because process is the
// composition root — it is the only package that already holds the runner,
// the hook pipeline, the run store and the controller together, and the only
// one permitted to import concrete implementations.
//
// What it deliberately does NOT do:
//
//   - It does not take the foreground lock. Submit already serializes turns
//     per session through its ForegroundLocker, and session.Locker is a
//     per-id channel semaphore rather than a reentrant mutex, so locking here
//     as well would deadlock the second acquisition against the first.
//   - It does not call Controller.Finish on a run that stopped at an approval
//     gate. Such a run is paused, not finished, and the resume path claims it
//     later; finishing it would let a concurrent turn start against a session
//     that still has an open gate.
type runExecutor struct {
	env     *Environment
	surface string
	opts    RunExecutorOptions
}

// RunExecutorOptions carries the per-surface extension points that genuinely
// cannot be derived from a TurnRequest. Everything else the executor needs is
// either on the Environment or in the request itself; anything added here
// should be something one surface does and another legitimately does not.
type RunExecutorOptions struct {
	// PreviewMax bounds the stored input preview. Zero means run's default.
	PreviewMax int
	// BeforeAgent runs after the run row exists and before the agent starts.
	// The TUI uses it to attach its event mirror to the new run ID.
	BeforeAgent func(agentCtx context.Context, runID, sessionID, input string) error
	// Finish releases the run once it is no longer executing. A surface that
	// hangs its own bookkeeping off the controller's finished-transition
	// supplies it here, because that transition fires exactly once: the TUI
	// applies a config reload deferred during the run from it, and a second
	// Finish issued by the executor would consume the transition and strand
	// the reload forever. Nil releases through the environment's controller,
	// which is what a surface with no such bookkeeping wants.
	Finish func(runID string)
	// FailOnRequiredMCP makes a run whose MCP generation has a failed required
	// server fail before the agent starts, with every required failure
	// aggregated. It is what a non-interactive surface sets: nobody is watching
	// the status line there, so a required server that did not come up must stop
	// the run instead of quietly leaving its tools out. An interactive surface
	// leaves it false and reports the failure in its own status view, because
	// refusing every turn for the rest of the session would make that session
	// unusable rather than informative.
	FailOnRequiredMCP bool
}

// NewRunExecutor builds the turn.RunExecutor for this environment. surface
// names the caller for hook and telemetry purposes ("tui", "webchat",
// a channel id); it becomes HookContext.Channel, which hooks branch on.
func (env *Environment) NewRunExecutor(surface string, opts RunExecutorOptions) turn.RunExecutor {
	return &runExecutor{env: env, surface: strings.TrimSpace(surface), opts: opts}
}

func (x *runExecutor) Run(ctx context.Context, req turn.TurnRequest, started func(runID string)) (*agent.Result, error) {
	// A nil Runner or Hooks is deliberately NOT an error here. run.Run treats
	// that as "nothing is wired to run, so this turn is a no-op" and returns a
	// nil result with a nil error; a session with no agent behind it still
	// persists the user's message and reports success. Only a missing executor
	// is a wiring fault, so that is all this guards.
	if x == nil || x.env == nil {
		return nil, turn.ErrNoRunExecutor
	}
	sid := strings.TrimSpace(req.SessionID)
	if sid == "" {
		return nil, turn.ErrSessionID
	}
	// A run belongs to a conversation, and the run row references the session
	// row: the conversation is recorded for the bound primary agent before its
	// run is. A surface that opened the session already has it; the first
	// message of a channel conversation has not. The upsert is also the tenant
	// check — a session another agent owns is refused before any work.
	if err := x.env.Deps.SessionStore.Ensure(ctx, sid, sid); err != nil {
		return nil, err
	}

	channel := strings.TrimSpace(req.Origin.ChannelID)
	if channel == "" {
		channel = x.surface
	}
	trigger := strings.TrimSpace(req.Trigger)
	if trigger == "" {
		trigger = "user"
	}
	hc := hook.HookContext{
		SessionID: sid,
		Channel:   channel,
		Trigger:   trigger,
		RawInput:  req.RawInput,
	}

	// One shared context builder for every surface. A hand-rolled variant of
	// this is what once let one surface write plans to a directory other than
	// the one the plan-mode reminder names, so this must stay the only way an
	// agent context is seeded here.
	agBase := AgentContextForProject(ctx, x.env.stateRoot(), sid, x.env.projectScope())
	if req.SkillName != "" || req.SkillPath != "" {
		agBase = run.WithExplicitSkillSelection(agBase, req.SkillName, req.SkillPath)
	}
	// A cancelled or failed turn must still persist the tool calls and results
	// it already completed, otherwise the next turn rebuilds context from a
	// transcript that is missing work the user watched happen.
	//
	// A caller that installed its own capture keeps it. The TUI does exactly
	// that — it holds the capture in order to render and persist the partial
	// turn itself — and overwriting the context value here would leave that
	// one empty, so a cancelled turn would silently lose everything the user
	// had already watched happen.
	capture := run.PartialSessionCaptureFromContext(agBase)
	ownsCapture := capture == nil
	if ownsCapture {
		capture = run.NewPartialSessionCapture()
		agBase = run.WithPartialSessionCapture(agBase, capture)
	}

	var runID string

	// One resolution, one fact source: the barrier and the required-server check
	// below must be about the same Runner the turn executes on. The pooled
	// Runner is shared by every session in this project, so its generation is a
	// shared resource — this waits for it, and never cancels it.
	runner := x.env.RunnerForSession(ctx, sid)
	if x.opts.FailOnRequiredMCP {
		if err := awaitRequiredMCP(ctx, runner); err != nil {
			return nil, err
		}
	}
	res, _, err := run.Run(run.Options{
		RunRT:                  x.env.Deps.RunRT,
		Runner:                 runner,
		Hooks:                  x.env.Hooks,
		AgBase:                 agBase,
		HC:                     hc,
		Input:                  req.UserText,
		InputParts:             req.Parts,
		PreviewMax:             x.opts.PreviewMax,
		RunIDOut:               &runID,
		SuperviseExistingRunID: req.ExistingRunID,
		AgBaseIsRunContext:     req.AgentContextIsRunContext,
		ParentRunID:            req.ParentRunID,
		Control:                x.env.Control,
		InputRuntime:           run.TurnInputRuntimeFromContext(agBase),
		GoalObjective:          strings.TrimSpace(req.GoalObjective),
		BeforeAgent: func(agentCtx context.Context, id, sessionID, input string) error {
			// The run row exists by now, so this is the first moment the real
			// run ID is knowable. Submit publishes turn_started from here.
			if started != nil {
				started(id)
			}
			if x.opts.BeforeAgent != nil {
				return x.opts.BeforeAgent(agentCtx, id, sessionID, input)
			}
			return nil
		},
	})

	// A run that stopped at an approval gate is paused, not finished: the
	// resume path owns it from here and will finish it. Finishing it now would
	// free the session for a concurrent turn while a gate is still open.
	//
	// A caller that supplied its own run id owns that run's lifecycle and
	// finishes it itself. That is not a preference: the gateway's websocket
	// turn finalises its queued input before finishing, and Controller.Finish
	// clears the queue, so finishing here would make that finalisation a no-op
	// and silently drop the pending-input report.
	if !run.IsRequiresAction(err) && strings.TrimSpace(req.ExistingRunID) == "" {
		if x.opts.Finish != nil {
			x.opts.Finish(runID)
		} else {
			x.env.Control.Finish(runID)
		}
	}
	// Only persist what this executor owns. A caller that supplied its own
	// capture is persisting from it too, and doing both would write the same
	// partial turn to the transcript twice.
	if err != nil && ownsCapture {
		x.env.persistPartialTurn(sid, runID, capture)
	}
	// run and turn describe an approval gate with different types, and the
	// adapter is the seam that owns the translation: run raises
	// tool.RequiresActionError, while Submit recognises only turn.WaitingError
	// and would otherwise classify a perfectly normal approval pause as a
	// failed turn. Handing the error straight through is what made a gated
	// turn report status "failed" the first time this path was ever executed.
	if waiting := x.waitingError(sid, err); waiting != nil {
		return res, waiting
	}
	return res, err
}

// waitingError converts run's approval-gate error into turn's, projecting the
// typed approval request the surfaces render from. It returns nil when err is
// not an approval gate.
func (x *runExecutor) waitingError(sessionID string, err error) *turn.WaitingError {
	var rae *tool.RequiresActionError
	if !errors.As(err, &rae) || rae == nil {
		return nil
	}
	request, _, _ := turn.BuildToolApprovalRequest(turn.ApprovalSource{
		SessionID:    sessionID,
		ActionID:     rae.ActionID,
		RunID:        rae.RunID,
		ToolName:     rae.ToolName,
		ActionKind:   rae.ActionKind,
		ToolInput:    rae.ToolInput,
		AgentID:      rae.AgentID,
		SubagentType: rae.SubagentType,
	}, x.env.Runner)
	return &turn.WaitingError{
		Request: &request,
		Resume: &turn.ApprovalResumeState{
			ActionID:        rae.ActionID,
			ToolName:        rae.ToolName,
			SessionSnapshot: rae.SessionSnapshot,
		},
		Cause: err,
	}
}

// stateRoot is the active primary agent's per-agent state root. Both surfaces
// derive it the same way; the executor uses the one on the Environment so a
// surface cannot supply a different one.
func (env *Environment) stateRoot() string {
	if env == nil {
		return ""
	}
	return appcfg.ActiveStateRoot(strings.TrimSpace(env.Root), env.Deps.AppCfg)
}

// projectScope is the runner's launch-project key. Plan files live under
// <stateRoot>/plans/<key>, so a context wired with a different key lets the
// agent write somewhere other than the directory the plan-mode reminder names
// and every plan write is then rejected.
func (env *Environment) projectScope() string {
	if env == nil || env.Runner == nil {
		return ""
	}
	return strings.TrimSpace(env.Runner.ProjectKey)
}

// persistPartialTurn writes the tool calls and results a failed or cancelled
// turn already completed. Without it a transient LLM error drops everything
// the assistant produced this turn: the session store holds only the user
// message, and the next turn rebuilds context from scratch, losing work the
// user watched happen.
func (env *Environment) persistPartialTurn(sessionID, runID string, capture *run.PartialSessionCapture) {
	if env == nil || env.Deps.SessionStore == nil || capture == nil {
		return
	}
	pending := capture.Snapshot()
	if len(pending) == 0 {
		return
	}
	// What the stopped run wrote is the run's. The model is unknown at cancel
	// time; the store tolerates it.
	_ = env.Deps.SessionStore.AppendMessageSequenceForRun(context.Background(), sessionID, runID, pending, "", "")
}

// Tools is the active primary agent's tool state.
//
// R4 makes the Environment the place callers ask for it, rather than reaching
// through the Runner. The Runner still builds it during Load — registering
// tools, MCP servers and skills is execution setup — but "which tool state
// does this process use" is a composition question, and routing every caller
// through the composition root is what lets the Runner eventually stop being
// the answer.
//
// Subagents are deliberately not served from here: Factory.NewIsolatedRunner
// gives each one its own state, because a subagent that shared the primary
// agent's would inherit approvals and filesystem policy scoped to a different
// workspace.
func (env *Environment) Tools() *tool.State {
	if env == nil || env.Runner == nil {
		return nil
	}
	return env.Runner.Tools()
}
