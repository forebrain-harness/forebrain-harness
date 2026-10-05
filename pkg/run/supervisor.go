// Supervised execution: run lifecycle, hooks, and the test LLM override.
package run

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/hook"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/telemetry"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
	"github.com/google/uuid"
)

type Options struct {
	RunRT                  *state.RunStore
	Runner                 *Runner
	Hooks                  *hook.AgentPipeline
	AgBase                 context.Context
	HC                     hook.HookContext
	Input                  string
	InputParts             []llm.ContentPart
	PreviewMax             int
	UseRunID               string
	SuperviseExistingRunID string
	ParentRunID            string
	AgBaseIsRunContext     bool
	CreateRunCtx           context.Context
	Control                *Controller
	InputRuntime           *TurnInputRuntime
	BeforeAgent            func(agentCtx context.Context, runID, sessionID, input string) error
	AfterSuccess           func(ctx context.Context, res *agent.Result) error
	WallStartedAt          time.Time
	WallPollInterval       time.Duration
	OnWallExceeded         func(ctx context.Context, runID, sessionID string, elapsed time.Duration, cancelCause context.CancelCauseFunc)
	RunIDOut               *string
	// GoalObjective, when non-empty, starts a goal: after each round the goal
	// check reads the workspace and judges whether the objective is met, and
	// another round runs until it is, no progress is being made, the round cap
	// is hit, or the user stops the turn. A run resumed past an approval needs
	// none: it picks its goal up from its own ledger.
	GoalObjective string
}

// goalForRun is the goal the run works toward. A goal this turn starts is
// announced before its first round, so every surface shows it from the
// start; a run resumed past an approval carries on with the goal its ledger
// records, from the round it had reached.
func (o Options) goalForRun(ctx context.Context, runID, sessionID string) goalState {
	if objective := strings.TrimSpace(o.GoalObjective); objective != "" {
		recordGoalEvent(ctx, o.Runner, runID, sessionID, event.RunEventGoalStarted,
			"goal-started:"+runID, event.GoalStartedPayload{Objective: objective, MaxRounds: MaxGoalRounds, AfterRowID: goalAnchorRow(ctx, o.Runner, sessionID)})
		return goalState{objective: objective, rounds: 1, started: time.Now()}
	}
	return goalOfRun(ctx, o.RunRT, runID)
}

// settleRunError records how a run that returned err ended, and returns err.
// A run stopped at an approval is suspended until the decision; a cancelled
// one is recorded cancelled, anything else failed.
func (o Options) settleRunError(runCtx context.Context, runID string, err error) error {
	bg := context.Background()
	var rae *tool.RequiresActionError
	if errors.As(err, &rae) {
		o.suspendRun(runID)
		return err
	}
	_ = o.RunRT.ClearWait(bg, runID)
	if errors.Is(err, context.Canceled) {
		_ = o.RunRT.SetStatus(bg, runID, state.RunStatusCancelled)
		_ = o.RunRT.CancelRunningDescendants(bg, runID)
		return err
	}
	_ = o.RunRT.SetStatus(bg, runID, state.RunStatusFailed)
	_ = o.RunRT.FailRunningDescendants(bg, runID)
	return err
}

// finishSupervisedRun is everything after a run's pipeline returned: the
// success hook, the goal's further rounds when the run works toward one, and
// the record of how the run ended.
func (o Options) finishSupervisedRun(runCtx context.Context, runID, sid string, goal goalState, res *agent.Result, pre hook.PreHookResult, err error) (*agent.Result, hook.PreHookResult, error) {
	continuation := ctxGoalContinuationInput{
		ctx:       runCtx,
		runner:    o.Runner,
		runID:     runID,
		sessionID: sid,
		goal:      goal,
		eval:      goalCheck{runner: o.Runner},
		result:    res,
	}
	tagRAEWithRunID(err, runID)
	if err != nil {
		PersistRunUsageFromError(o.RunRT, runID, err)
		continuation.roundEnded(goal.rounds, err)
		return res, pre, o.settleRunError(runCtx, runID, err)
	}
	bg := context.Background()
	if o.AfterSuccess != nil {
		if err := o.AfterSuccess(bg, res); err != nil {
			continuation.roundEnded(goal.rounds, err)
			return res, pre, o.settleRunError(runCtx, runID, err)
		}
	}
	res, err = runGoalContinuation(continuation)
	persistRunUsage(o.RunRT, runID, res)
	if err != nil {
		tagRAEWithRunID(err, runID)
		return res, pre, o.settleRunError(runCtx, runID, err)
	}
	_ = o.RunRT.ClearWait(bg, runID)
	_ = o.RunRT.SetStatus(bg, runID, state.RunStatusDone)
	return res, pre, nil
}

// suspendRun marks a run as waiting at an approval gate.
func (o Options) suspendRun(runID string) {
	if o.Control != nil {
		o.Control.WaitApproval(runID)
	}
}

func (o Options) trackRun(runID, sessionID string, cancel context.CancelCauseFunc) {
	if cancel == nil {
		return
	}
	stop := context.CancelFunc(func() { cancel(context.Canceled) })
	if o.Control != nil {
		o.Control.TrackRuntime(runID, sessionID, stop, o.InputRuntime)
	}
}

func tagRAEWithRunID(err error, runID string) {
	var rae *tool.RequiresActionError
	if err == nil || !errors.As(err, &rae) || rae == nil {
		return
	}
	rid := strings.TrimSpace(runID)
	if rid == "" {
		return
	}
	if strings.TrimSpace(rae.RunID) == "" {
		rae.RunID = rid
	}
}

func Run(o Options) (*agent.Result, hook.PreHookResult, error) {
	var zeroPre hook.PreHookResult
	if o.Runner == nil || o.Hooks == nil {
		return nil, zeroPre, nil
	}
	sid := strings.TrimSpace(o.HC.SessionID)
	if sid == "" {
		sid = "default"
	}
	o.HC.SessionID = sid
	if ur := strings.TrimSpace(o.UseRunID); ur != "" {
		o.HC.RunID = ur
		agCtx := llm.WithAgentSessionID(o.AgBase, sid)
		agCtx = tool.WithRunID(agCtx, ur)
		agCtx = WithQuerySource(agCtx, querySourceForRun(o))
		agCtx, endRun := telemetry.StartRunSpan(agCtx, ur, sid, o.HC.Trigger)
		defer endRun()
		res, pre, err := RunPipeline(agCtx, o.Runner, o.Hooks, o.HC, o.Input, o.InputParts)
		tagRAEWithRunID(err, ur)
		return res, pre, err
	}
	if o.RunRT == nil {
		agCtx := llm.WithAgentSessionID(o.AgBase, sid)
		agCtx = WithQuerySource(agCtx, querySourceForRun(o))
		res, pre, err := RunPipeline(agCtx, o.Runner, o.Hooks, o.HC, o.Input, o.InputParts)
		return res, pre, err
	}
	crCtx := o.CreateRunCtx
	if crCtx == nil {
		crCtx = context.Background()
	}
	max := o.PreviewMax
	if max <= 0 {
		max = 4096
	}
	preview := InputPreview(o.Input, max)
	existing := strings.TrimSpace(o.SuperviseExistingRunID)
	if existing != "" {
		return runExistingSupervised(o, sid, existing)
	}
	var rr *state.Run
	var err error
	if strings.TrimSpace(o.ParentRunID) != "" {
		rr, err = o.RunRT.CreateSubagentRun(crCtx, o.ParentRunID, sid, preview)
	} else {
		rr, err = o.RunRT.CreateRun(crCtx, sid, preview)
	}
	if err != nil {
		if errors.Is(err, state.ErrSessionBusy) {
			// The sentence is for the person who asked, and the code the
			// surface localises it by; wrapping would only bury both.
			return nil, zeroPre, err
		}
		return nil, zeroPre, fmt.Errorf("create run: %w", err)
	}
	if rr == nil {
		return nil, zeroPre, fmt.Errorf("create run: nil run")
	}
	runID := rr.ID
	if o.RunIDOut != nil {
		*o.RunIDOut = runID
	}
	o.HC.RunID = runID
	var runCtx context.Context
	var cancel context.CancelCauseFunc
	if o.AgBaseIsRunContext {
		runCtx = o.AgBase
	} else {
		runCtx, cancel = context.WithCancelCause(o.AgBase)
		runCtx = llm.WithAgentSessionID(runCtx, sid)
	}
	// Every tool this run executes resolves its approval queue and its durable
	// wait row from the run id in its own context, so the identity is attached
	// on both paths. A caller-built run context is spared the cancel wrapper --
	// a second cancel would orphan the one its controller tracks -- not the
	// identity.
	runCtx = tool.WithRunID(runCtx, runID)
	runCtx = WithQuerySource(runCtx, querySourceForRun(o))
	o.trackRun(runID, sid, cancel)
	started := o.WallStartedAt
	if started.IsZero() {
		started = time.Now()
	}
	if o.WallPollInterval > 0 && o.OnWallExceeded != nil && cancel != nil {
		go wallPollLoop(runCtx, runID, sid, started, o.WallPollInterval, o.OnWallExceeded, cancel)
	}
	defer func() {
		if cancel != nil {
			cancel(context.Canceled)
		}
	}()
	if o.BeforeAgent != nil {
		_ = o.BeforeAgent(runCtx, runID, sid, o.Input)
	}
	runCtx, endRun := telemetry.StartRunSpan(runCtx, runID, sid, o.HC.Trigger)
	defer endRun()
	maybeLaunchMemoryStartup(o, sid)
	goal := o.goalForRun(runCtx, runID, sid)
	res, pre, err := RunPipeline(runCtx, o.Runner, o.Hooks, o.HC, o.Input, o.InputParts)
	return o.finishSupervisedRun(runCtx, runID, sid, goal, res, pre, err)
}

func runExistingSupervised(o Options, sid, runID string) (*agent.Result, hook.PreHookResult, error) {
	if o.RunIDOut != nil {
		*o.RunIDOut = runID
	}
	o.HC.RunID = runID
	var runCtx context.Context
	var cancel context.CancelCauseFunc
	if o.AgBaseIsRunContext {
		runCtx = o.AgBase
	} else {
		runCtx, cancel = context.WithCancelCause(o.AgBase)
		runCtx = llm.WithAgentSessionID(runCtx, sid)
	}
	// Every tool this run executes resolves its approval queue and its durable
	// wait row from the run id in its own context, so the identity is attached
	// on both paths. A caller-built run context is spared the cancel wrapper --
	// a second cancel would orphan the one its controller tracks -- not the
	// identity.
	runCtx = tool.WithRunID(runCtx, runID)
	runCtx = WithQuerySource(runCtx, querySourceForRun(o))
	o.trackRun(runID, sid, cancel)
	started := o.WallStartedAt
	if started.IsZero() {
		started = time.Now()
	}
	if o.WallPollInterval > 0 && o.OnWallExceeded != nil && cancel != nil {
		go wallPollLoop(runCtx, runID, sid, started, o.WallPollInterval, o.OnWallExceeded, cancel)
	}
	defer func() {
		if cancel != nil {
			cancel(context.Canceled)
		}
	}()
	if o.BeforeAgent != nil {
		_ = o.BeforeAgent(runCtx, runID, sid, o.Input)
	}
	runCtx, endRun := telemetry.StartRunSpan(runCtx, runID, sid, o.HC.Trigger)
	defer endRun()
	maybeLaunchMemoryStartup(o, sid)
	goal := o.goalForRun(runCtx, runID, sid)
	res, pre, err := RunPipeline(runCtx, o.Runner, o.Hooks, o.HC, o.Input, o.InputParts)
	return o.finishSupervisedRun(runCtx, runID, sid, goal, res, pre, err)
}

func wallPollLoop(runCtx context.Context, runID, sid string, started time.Time, every time.Duration, onEx func(context.Context, string, string, time.Duration, context.CancelCauseFunc), cancelCause context.CancelCauseFunc) {
	if every <= 0 || onEx == nil || cancelCause == nil {
		return
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-runCtx.Done():
			return
		case <-t.C:
			onEx(runCtx, runID, sid, time.Since(started), cancelCause)
		}
	}
}

func persistRunUsage(rt *state.RunStore, runID string, res *agent.Result) {
	if rt == nil || res == nil || res.Summary == nil {
		return
	}
	_ = rt.SetRunUsage(context.Background(), runID, state.LastRunUsage{
		PromptTokens:     res.Summary.Usage.InputTokens,
		CompletionTokens: res.Summary.Usage.OutputTokens,
		CacheReadTokens:  res.Summary.Usage.CacheReadInputTokens,
		CacheWriteTokens: res.Summary.Usage.CacheCreationInputTokens,
		LLMCalls:         res.Summary.LLMCalls,
	})
}

func PersistRunUsageFromError(rt *state.RunStore, runID string, err error) {
	if rt == nil || strings.TrimSpace(runID) == "" {
		return
	}
	usage := llm.UsageFromErrorForSupervisor(err)
	if usage == nil {
		return
	}
	_ = rt.SetRunUsage(context.Background(), runID, state.LastRunUsage{
		PromptTokens:     usage.InputTokens,
		CompletionTokens: usage.OutputTokens,
		CacheReadTokens:  usage.CacheReadInputTokens,
		CacheWriteTokens: usage.CacheCreationInputTokens,
	})
}

func querySourceForRun(o Options) string {
	trigger := strings.ToLower(strings.TrimSpace(o.HC.Trigger))
	channel := strings.ToLower(strings.TrimSpace(o.HC.Channel))
	runKind := strings.ToLower(strings.TrimSpace(o.HC.RunKind))
	if strings.Contains(channel, "subagent") {
		if tool.IsForkChildFromContext(o.AgBase) {
			return "agent:builtin:fork"
		}
		if subtype := strings.ToLower(strings.TrimSpace(tool.SubagentTypeFromContext(o.AgBase))); subtype != "" {
			if src := strings.ToLower(strings.TrimSpace(tool.SubagentDefinitionSourceFromContext(o.AgBase))); src == "built-in" {
				return "agent:builtin:" + subtype
			}
			return "agent:custom"
		}
		return "agent:default"
	}
	switch {
	case strings.TrimSpace(o.ParentRunID) != "":
		return "agent:default"
	case strings.Contains(trigger, "compact") || strings.Contains(runKind, "compact"):
		return "compact"
	case strings.Contains(trigger, "hook"):
		return "hook_agent"
	case channel == "sdk":
		return "sdk"
	case strings.Contains(trigger, "agent-once"):
		return "agent:default"
	case trigger == "side_question":
		return "side_question"
	default:
		return "repl_main_thread"
	}
}

// isRootInteractiveSource reports whether a query source string represents a
// root interactive turn (TUI, gateway web, or SDK) that should trigger the
// background memory pipeline.
func isRootInteractiveSource(source string) bool {
	switch source {
	case "repl_main_thread", "sdk":
		return true
	default:
		return false
	}
}

// maybeLaunchMemoryStartup fires the background memory pipeline for root
// interactive turns. It is called once per run after the run span is started.
func maybeLaunchMemoryStartup(o Options, sessionID string) {
	if o.Runner == nil {
		return
	}
	if strings.TrimSpace(o.ParentRunID) != "" {
		return
	}
	if !isRootInteractiveSource(querySourceForRun(o)) {
		return
	}
	o.Runner.LaunchMemoryStartup(sessionID)
}

// IsRequiresAction reports whether err is the sentinel meaning the run stopped
// at an approval gate rather than finishing. Such a run is paused: the resume
// path owns it and will finish it, so callers must not treat it as terminal.
// Every surface was open-coding this errors.As against an unexported-ish
// error type, which is exactly the check that must not drift between them.
func IsRequiresAction(err error) bool {
	var rae *tool.RequiresActionError
	return errors.As(err, &rae)
}

func RunText(r *Runner, ctx context.Context, input string) (string, error) {
	if r == nil {
		return "", fmt.Errorf("nil runner")
	}
	res, err := r.Run(ctx, input)
	if err != nil {
		return "", err
	}
	if res == nil {
		return "", fmt.Errorf("runner returned nil result")
	}
	return res.TextContent(), nil
}

func (r *Runner) RunAgentHook(ctx context.Context, task string) (string, error) {
	if r == nil {
		return "", fmt.Errorf("nil runner")
	}
	out, err := RunFork(ctx, RunParams{
		LLM:           r.ForkLLM(),
		CacheSafe:     &CacheSafeParams{SystemPrompt: r.MainAgentDescription()},
		UserPrompt:    task,
		Tools:         r.tools,
		AgentBaseName: "hook-agent",
		AgentType:     "hook-agent",
		AgentID:       uuid.NewString(),
		WorkspaceRoot: r.workspaceRoot(),
		SessionID:     "hook",
		QuerySource:   "hook_agent",
		RegisterTools: func(reg *ToolRegistry) error {
			if reg == nil {
				return fmt.Errorf("nil tool registry")
			}
			for _, t := range r.LoadedTools() {
				if t == nil {
					continue
				}
				if err := reg.Add(t.Clone()); err != nil {
					return err
				}
			}
			return nil
		},
	})
	if err != nil {
		return "", err
	}
	if out == nil || out.Result == nil {
		return "", fmt.Errorf("agent hook returned no result")
	}
	return out.Result.TextContent(), nil
}

// llmOverrideForTest replaces the provider client loadLocked would otherwise
// build from config, so a test can drive a real Runner.Load without live
// provider credentials while every wrapping layer loadLocked applies on top
// of the raw client (typed subagent routing, model override, memory
// instruction/citation, plan mode, ...) still runs for real. Package-level
// rather than a Runner field: Runner is already at the field-count ratchet
// TestRunnerOnlyShrinks enforces, and — like skill's SetAuthoredSkillSource
// tests set and restore this sequentially rather than
// running the affected tests in parallel with each other.
var llmOverrideForTest llm.LLM

// SetLLMOverrideForTest installs client as the provider client the next
// Runner.Load call(s) use, and returns a func that restores the previous
// value. Test-only: production code never calls this.
func SetLLMOverrideForTest(client llm.LLM) (restore func()) {
	prev := llmOverrideForTest
	llmOverrideForTest = client
	return func() { llmOverrideForTest = prev }
}
