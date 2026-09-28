// Goal-driven continuation: the objective, the check that judges it, and the loop.
package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// MaxGoalRounds is the hard safety cap on rounds for one /goal, reached
// regardless of what the check finds.
const MaxGoalRounds = 100

// goalCheckRecentRounds is how many of the latest rounds the check is told
// about. The check reads the workspace itself; the rounds are only a lead, so
// the oldest are left out rather than growing its prompt with every round.
const goalCheckRecentRounds = 3

// Status is the check's verdict on the round just completed.
type Status string

const (
	StatusContinue Status = "continue" // objective not yet met, keep going
	StatusDone     Status = "done"     // objective met, stop (success)
	StatusStuck    Status = "stuck"    // no progress toward objective, stop
)

// Verdict is the check's judgment for the round just completed. CheckAgentID
// is the roster key of the check that gave it, so a surface can open its view.
type Verdict struct {
	Status       Status
	Why          string
	CheckAgentID string
}

// Decision is what the loop should do next. When Continue is false, Reason is
// how the goal ends: done, stuck or capped.
type Decision struct {
	Continue bool
	Reason   string
}

// Decide combines the completed-round count and the verdict into a loop
// action. rounds is the number of rounds already completed (1 after the first
// run). Done and stuck are honoured before the cap so a verdict on the final
// allowed round reports its true reason.
func Decide(rounds int, v Verdict) Decision {
	switch v.Status {
	case StatusDone:
		return Decision{Continue: false, Reason: event.GoalStatusDone}
	case StatusStuck:
		return Decision{Continue: false, Reason: event.GoalStatusStuck}
	}
	if rounds >= MaxGoalRounds {
		return Decision{Continue: false, Reason: event.GoalStatusCapped}
	}
	return Decision{Continue: true}
}

// RoundOutcome is the bounded summary of one completed round the check is
// given as a lead. No raw transcript, no token counts.
type RoundOutcome struct {
	Index      int    `json:"index"`
	Text       string `json:"text"`
	ToolCalls  int    `json:"tool_calls"`
	ToolErrors int    `json:"tool_errors"`
}

// Evaluator judges whether an objective has been met. The production one is
// the goal check subagent; tests script verdicts.
type Evaluator interface {
	Evaluate(ctx context.Context, objective string, rounds []RoundOutcome) (Verdict, error)
}

// ContinuationPrompt builds the goal-framed nudge for the next round. It
// carries the objective and the check's reasoning, never a token count.
func ContinuationPrompt(objective, why string) string {
	var b strings.Builder
	b.WriteString("Continue working toward this objective:\n")
	b.WriteString(strings.TrimSpace(objective))
	if w := strings.TrimSpace(why); w != "" {
		b.WriteString("\n\nAssessment of progress so far: ")
		b.WriteString(w)
	}
	b.WriteString("\n\nIf the objective is already fully met, say so and stop. Otherwise continue from where you left off without repeating completed work.")
	return b.String()
}

// GoalCheckSubagentType is the reserved definition the goal check runs as.
const GoalCheckSubagentType = "goal-evaluator"

// goalCheck judges a goal by dispatching the goal check subagent. The check
// reads the workspace with read-only tools rather than taking the agent's
// word for it; it is a subagent of the turn working toward the goal, so it is
// on the roster with a view of its own while it runs, and any tool it wants
// approved is put to the user in place.
type goalCheck struct {
	runner *Runner
}

func (c goalCheck) Evaluate(ctx context.Context, objective string, rounds []RoundOutcome) (Verdict, error) {
	if c.runner == nil {
		return Verdict{}, errors.New("the goal check needs a runner")
	}
	output, agentID, err := c.runner.runGoalCheck(ctx, goalCheckTask(objective, rounds))
	if err != nil {
		return Verdict{CheckAgentID: agentID}, err
	}
	verdict, err := parseGoalVerdict(output)
	verdict.CheckAgentID = agentID
	return verdict, err
}

// goalCheckTask is what the check is asked: the objective, and the latest
// rounds as a lead to what changed.
func goalCheckTask(objective string, rounds []RoundOutcome) string {
	if len(rounds) > goalCheckRecentRounds {
		rounds = rounds[len(rounds)-goalCheckRecentRounds:]
	}
	var b strings.Builder
	b.WriteString("Objective:\n")
	b.WriteString(strings.TrimSpace(objective))
	b.WriteString("\n\nThe agent's latest rounds, as it reported them:\n")
	for _, round := range rounds {
		fmt.Fprintf(&b, "\nRound %d (%d tool calls, %d failed):\n%s\n", round.Index+1, round.ToolCalls, round.ToolErrors, strings.TrimSpace(round.Text))
	}
	b.WriteString("\nCheck the workspace yourself and judge whether the objective is met. End with the JSON verdict only.")
	return b.String()
}

// parseGoalVerdict reads the verdict the check ends with. The check may
// reason before it, so the verdict is the last JSON object in its answer.
func parseGoalVerdict(output string) (Verdict, error) {
	raw, ok := lastJSONObject(output)
	if !ok {
		return Verdict{}, fmt.Errorf("the goal check ended without a verdict: %s", llm.TruncateRunes(strings.TrimSpace(output), 300))
	}
	var verdict struct {
		Status string `json:"status"`
		Why    string `json:"why"`
	}
	if err := json.Unmarshal([]byte(raw), &verdict); err != nil {
		return Verdict{}, fmt.Errorf("the goal check's verdict is not valid JSON: %w", err)
	}
	status := Status(strings.ToLower(strings.TrimSpace(verdict.Status)))
	switch status {
	case StatusDone, StatusContinue, StatusStuck:
	default:
		return Verdict{}, fmt.Errorf("the goal check's verdict has no status it can act on: %q", verdict.Status)
	}
	return Verdict{Status: status, Why: strings.TrimSpace(verdict.Why)}, nil
}

// lastJSONObject finds the last complete JSON object in s.
func lastJSONObject(s string) (string, bool) {
	end := strings.LastIndexByte(s, '}')
	for end >= 0 {
		for start := strings.LastIndexByte(s[:end], '{'); start >= 0; start = strings.LastIndexByte(s[:start], '{') {
			candidate := s[start : end+1]
			if json.Valid([]byte(candidate)) {
				return candidate, true
			}
		}
		end = strings.LastIndexByte(s[:end], '}')
	}
	return "", false
}

const goalOutcomeTextRunes = 2000

// goalState is a goal in progress: its objective, how many rounds have
// started, and when it began.
type goalState struct {
	objective string
	rounds    int
	started   time.Time
}

func (g goalState) active() bool { return strings.TrimSpace(g.objective) != "" }

type ctxGoalContinuationInput struct {
	ctx       context.Context
	runner    *Runner
	runID     string
	sessionID string
	goal      goalState
	eval      Evaluator
	result    *agent.Result
}

// runGoalContinuation drives the rounds after a goal's latest one: it asks the
// check whether the objective is met and runs another round until the check
// finds it done or stuck, or the round limit is hit. Every way the goal ends is
// recorded. A round that stops at an approval, fails or is cancelled returns
// its error, so the run is suspended or ended exactly as it would be had its
// first round done so; a run resumed past an approval picks the goal up again
// from its ledger.
func runGoalContinuation(in ctxGoalContinuationInput) (*agent.Result, error) {
	res := in.result
	if in.runner == nil || !in.goal.active() || in.eval == nil {
		return res, nil
	}
	rounds := max(in.goal.rounds, 1)
	outcomes := []RoundOutcome{roundOutcome(rounds-1, res)}
	for {
		verdict, err := in.eval.Evaluate(in.ctx, in.goal.objective, outcomes)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				in.endGoal(rounds, event.GoalStatusInterrupted, "", verdict.CheckAgentID)
				return res, err
			}
			// The rounds' own work stands; only the goal cannot go on without
			// its check, and the check's error is the reason it ends.
			in.endGoal(rounds, event.GoalStatusFailed, err.Error(), verdict.CheckAgentID)
			return res, nil
		}
		decision := Decide(rounds, verdict)
		if !decision.Continue {
			in.endGoal(rounds, decision.Reason, verdict.Why, verdict.CheckAgentID)
			return res, nil
		}
		rounds++
		recordGoalEvent(in.ctx, in.runner, in.runID, in.sessionID, event.RunEventGoalRoundStarted,
			"goal-round:"+in.runID+":"+strconv.Itoa(rounds), event.GoalRoundStartedPayload{
				Round:        rounds,
				Why:          verdict.Why,
				CheckAgentID: verdict.CheckAgentID,
				AfterRowID:   persistGoalRounds(in.ctx, in.runner, in.sessionID, res),
			})
		next, err := in.runner.Run(withGoalContinuationInput(in.ctx), ContinuationPrompt(in.goal.objective, verdict.Why))
		res = mergeAgentResults(res, next)
		if err != nil {
			in.roundEnded(rounds, err)
			return res, err
		}
		outcomes = append(outcomes, roundOutcome(rounds-1, next))
	}
}

// roundEnded records how a goal ends when the round numbered round returned err
// instead of finishing: cancelled, it was interrupted; failed, it failed, and
// the turn reports the round's error, so the goal says only that it ended
// there. A round stopped at an approval ends nothing: the run is suspended
// with the goal still on, and picks it up again from its ledger when resumed.
func (in ctxGoalContinuationInput) roundEnded(round int, err error) {
	var rae *tool.RequiresActionError
	if !in.goal.active() || errors.As(err, &rae) {
		return
	}
	status := event.GoalStatusFailed
	if errors.Is(err, context.Canceled) {
		status = event.GoalStatusInterrupted
	}
	in.endGoal(round, status, "", "")
}

func (in ctxGoalContinuationInput) endGoal(rounds int, status, why, checkAgentID string) {
	recordGoalEvent(in.ctx, in.runner, in.runID, in.sessionID, event.RunEventGoalCompleted,
		"goal-completed:"+in.runID, event.GoalCompletedPayload{
			Objective:    in.goal.objective,
			Rounds:       rounds,
			Status:       status,
			Why:          strings.TrimSpace(why),
			DurationMs:   time.Since(in.goal.started).Milliseconds(),
			CheckAgentID: checkAgentID,
		})
}

// roundOutcome builds a bounded summary of a completed round for the check.
func roundOutcome(index int, res *agent.Result) RoundOutcome {
	out := RoundOutcome{Index: index}
	if res == nil {
		return out
	}
	out.Text = llm.TruncateRunes(strings.TrimSpace(res.TextContent()), goalOutcomeTextRunes)
	if res.Summary != nil {
		out.ToolCalls = res.Summary.ToolCalls
		out.ToolErrors = res.Summary.ToolErrors
	}
	return out
}

// recordGoalEvent writes one step of a goal's life once, onto the
// conversation's event log every surface draws the goal from, live and on
// replay; a run-level reader pages the same rows by run_id.
func recordGoalEvent(ctx context.Context, runner *Runner, runID, sessionID, eventType, eventID string, payload any) {
	bg := context.WithoutCancel(ctx)
	if runner != nil {
		_ = runner.publishSurfaceEvent(bg, event.NewRunEvent(eventID, strings.TrimSpace(runID), strings.TrimSpace(sessionID), eventType, payload, time.Now()))
	}
}

// goalOfRun reads from a run's events the goal it was working toward and how
// many rounds it had started. A goal that already ended is over.
func goalOfRun(ctx context.Context, runRT *state.RunStore, runID string) goalState {
	if runRT == nil || strings.TrimSpace(runID) == "" {
		return goalState{}
	}
	events, err := runRT.ListRunEventsOfTypes(ctx, runID, event.RunEventGoalStarted, event.RunEventGoalRoundStarted, event.RunEventGoalCompleted)
	if err != nil {
		return goalState{}
	}
	var goal goalState
	for _, evt := range events {
		switch evt.Type {
		case event.RunEventGoalStarted:
			var p event.GoalStartedPayload
			if decodeStepPayload(evt.Payload, &p) {
				goal = goalState{objective: strings.TrimSpace(p.Objective), rounds: 1, started: evt.CreatedAt}
			}
		case event.RunEventGoalRoundStarted:
			var p event.GoalRoundStartedPayload
			if decodeStepPayload(evt.Payload, &p) && p.Round > goal.rounds {
				goal.rounds = p.Round
			}
		case event.RunEventGoalCompleted:
			goal = goalState{}
		}
	}
	return goal
}

func decodeStepPayload(payload any, out any) bool {
	raw, err := json.Marshal(payload)
	return err == nil && json.Unmarshal(raw, out) == nil
}

type goalContinuationInputKey struct{}

// withGoalContinuationInput marks the input of the run under ctx as the
// prompt opening a goal's continuation round: written by the runtime rather
// than the user, so it is sent to the model like any message but never shown
// as something the user said. The round's own line is the goal's event.
func withGoalContinuationInput(ctx context.Context) context.Context {
	return context.WithValue(ctx, goalContinuationInputKey{}, true)
}

func goalContinuationInputFromContext(ctx context.Context) bool {
	marked, _ := ctx.Value(goalContinuationInputKey{}).(bool)
	return marked
}

// persistGoalRounds writes what the goal's rounds have said so far to the
// conversation's transcript before the next round starts, and returns the row
// the next round's line is drawn after. A turn's rows are otherwise written
// when it ends, and a long goal is exactly the turn whose history a mid-turn
// compaction replaces before then: its earlier rounds would never reach the
// transcript at all. The write is the one every surface makes at the end of a
// turn, which appends only what is not stored yet; the model's prompt is
// untouched.
func persistGoalRounds(ctx context.Context, runner *Runner, sessionID string, res *agent.Result) int64 {
	if runner == nil || runner.SessionStore == nil || res == nil || strings.TrimSpace(sessionID) == "" {
		return 0
	}
	bg := context.WithoutCancel(ctx)
	_ = runner.SessionStore.AppendMessageSequence(bg, sessionID, res.Session, "", "")
	return goalAnchorRow(bg, runner, sessionID)
}

// goalAnchorRow is the newest transcript row, the one a goal line recorded
// now is drawn after.
func goalAnchorRow(ctx context.Context, runner *Runner, sessionID string) int64 {
	if runner == nil || runner.SessionStore == nil || strings.TrimSpace(sessionID) == "" {
		return 0
	}
	id, err := runner.SessionStore.LastTranscriptRowID(ctx, sessionID)
	if err != nil {
		return 0
	}
	return id
}
