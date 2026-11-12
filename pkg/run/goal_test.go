package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/hook"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// stubEvaluator returns scripted verdicts, one per call, then repeats the last.
type stubEvaluator struct {
	verdicts []Verdict
	err      error
	calls    int
	rounds   [][]RoundOutcome
}

func (s *stubEvaluator) Evaluate(_ context.Context, _ string, rounds []RoundOutcome) (Verdict, error) {
	s.rounds = append(s.rounds, append([]RoundOutcome(nil), rounds...))
	if s.err != nil {
		return Verdict{CheckAgentID: "check-failed"}, s.err
	}
	i := s.calls
	s.calls++
	if i >= len(s.verdicts) {
		i = len(s.verdicts) - 1
	}
	return s.verdicts[i], nil
}

// goalEventProbe keeps every goal event the runner publishes, in order, and
// writes them into the ledger the way the surface's own sink does.
type goalEventProbe struct {
	mu     sync.Mutex
	events []event.RunEvent
	log    *state.RunStore
}

func (p *goalEventProbe) Publish(ctx context.Context, evt event.RunEvent) error {
	p.mu.Lock()
	switch evt.Type {
	case event.RunEventGoalStarted, event.RunEventGoalRoundStarted, event.RunEventGoalCompleted:
		p.events = append(p.events, evt)
	}
	log := p.log
	p.mu.Unlock()
	if log == nil {
		return nil
	}
	return runEventLogSink(log, "sid").Publish(ctx, evt)
}

func (p *goalEventProbe) types() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, 0, len(p.events))
	for _, evt := range p.events {
		out = append(out, evt.Type)
	}
	return out
}

func newResult(text string) *agent.Result {
	return &agent.Result{
		Parts:   []llm.ContentPart{llm.Text(text)},
		Summary: &agent.RunSummary{},
	}
}

func activeGoal(rounds int) goalState {
	return goalState{objective: "ship", rounds: rounds, started: time.Now()}
}

// runEventLogSink writes a runner's published events into the conversation log,
// the way every surface wires its runner. The log is where a resumed run reads
// its goal back from.
func runEventLogSink(rt *state.RunStore, fallbackSessionID string) event.Sink {
	return event.SinkFunc(func(_ context.Context, evt event.RunEvent) error {
		sessionID := strings.TrimSpace(evt.SessionID)
		if sessionID == "" {
			sessionID = fallbackSessionID
		}
		_, err := rt.AppendSessionEvent(context.Background(), state.SessionEvent{
			ID: evt.ID, RunID: evt.RunID, SessionID: sessionID, Type: evt.Type,
			Payload: append(json.RawMessage(nil), evt.Payload...), CreatedAt: evt.CreatedAt,
		})
		return err
	})
}

// goalLedger is a run with a ledger the goal records its events into.
type goalLedger struct {
	rt    *state.RunStore
	runID string
}

func newGoalLedger(t *testing.T) goalLedger {
	t.Helper()
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("OpenStateForTest error: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	rt := &state.RunStore{DB: db}
	if err := state.NewSessionStore(db, "main").Ensure(ctx, "sid", "sid"); err != nil {
		t.Fatal(err)
	}
	rr, err := rt.CreateRun(ctx, "sid", "goal")
	if err != nil {
		t.Fatalf("CreateRun error: %v", err)
	}
	return goalLedger{rt: rt, runID: rr.ID}
}

// probe returns an event sink that both observes the goal events and writes
// them into the ledger, the way the surface's own sink does.
func (l goalLedger) probe() *goalEventProbe {
	return &goalEventProbe{log: l.rt}
}

func (l goalLedger) input(runner *Runner, goal goalState, eval Evaluator, result *agent.Result) ctxGoalContinuationInput {
	if runner.Events == nil {
		runner.Events = runEventLogSink(l.rt, "sid")
	}
	return ctxGoalContinuationInput{
		ctx:       context.Background(),
		runner:    runner,
		runID:     l.runID,
		sessionID: "sid",
		goal:      goal,
		eval:      eval,
		result:    result,
	}
}

// completed is the goal_completed step the ledger holds; the test fails when
// there is not exactly one.
func (l goalLedger) completed(t *testing.T) event.GoalCompletedPayload {
	t.Helper()
	steps := l.steps(t, event.RunEventGoalCompleted)
	if len(steps) != 1 {
		t.Fatalf("goal_completed steps = %d, want 1", len(steps))
	}
	var p event.GoalCompletedPayload
	decodeTestPayload(t, steps[0].Payload, &p)
	return p
}

func (l goalLedger) rounds(t *testing.T) []event.GoalRoundStartedPayload {
	t.Helper()
	var out []event.GoalRoundStartedPayload
	for _, step := range l.steps(t, event.RunEventGoalRoundStarted) {
		var p event.GoalRoundStartedPayload
		decodeTestPayload(t, step.Payload, &p)
		out = append(out, p)
	}
	return out
}

func (l goalLedger) steps(t *testing.T, types ...string) []state.SessionEvent {
	t.Helper()
	events, err := l.rt.ListRunEventsOfTypes(context.Background(), l.runID, types...)
	if err != nil {
		t.Fatalf("ListRunEventsOfTypes error: %v", err)
	}
	return events
}

func decodeTestPayload(t *testing.T, payload any, out any) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
}

func TestRunGoalContinuationWithoutObjectiveRunsNoCheck(t *testing.T) {
	ledger := newGoalLedger(t)
	runner := newLoadedRunnerForSupervisorTest(t, &supervisorScriptLLM{reply: "x"})
	eval := &stubEvaluator{verdicts: []Verdict{{Status: StatusContinue}}}
	got, err := runGoalContinuation(ledger.input(runner, goalState{}, eval, newResult("done")))
	if err != nil || got.TextContent() != "done" {
		t.Fatalf("result = %q, %v", got.TextContent(), err)
	}
	if eval.calls != 0 {
		t.Fatalf("check ran %d times without a goal", eval.calls)
	}
	if steps := ledger.steps(t, event.RunEventGoalCompleted); len(steps) != 0 {
		t.Fatalf("a turn without a goal recorded %d goal endings", len(steps))
	}
}

func TestRunGoalContinuationDoneAfterFirstRoundRecordsTheCheck(t *testing.T) {
	ledger := newGoalLedger(t)
	probe := ledger.probe()
	runner := newLoadedRunnerForSupervisorTest(t, &supervisorScriptLLM{reply: "more"})
	runner.Events = probe
	eval := &stubEvaluator{verdicts: []Verdict{{Status: StatusDone, Why: "tests pass", CheckAgentID: "check-1"}}}
	got, err := runGoalContinuation(ledger.input(runner, activeGoal(1), eval, newResult("round1")))
	if err != nil || got.TextContent() != "round1" {
		t.Fatalf("result = %q, %v; no further round may run", got.TextContent(), err)
	}
	done := ledger.completed(t)
	if done.Status != event.GoalStatusDone || done.Rounds != 1 || done.Why != "tests pass" || done.CheckAgentID != "check-1" || done.Objective != "ship" {
		t.Fatalf("goal_completed = %+v", done)
	}
	if got := probe.types(); len(got) != 1 || got[0] != event.RunEventGoalCompleted {
		t.Fatalf("published goal events = %v, want the ending only", got)
	}
}

func TestRunGoalContinuationRunsAnotherRoundUntilDone(t *testing.T) {
	ledger := newGoalLedger(t)
	probe := ledger.probe()
	runner := newLoadedRunnerForSupervisorTest(t, &supervisorScriptLLM{reply: "round2"})
	runner.Events = probe
	eval := &stubEvaluator{verdicts: []Verdict{
		{Status: StatusContinue, Why: "two tests still fail", CheckAgentID: "check-1"},
		{Status: StatusDone, Why: "all pass", CheckAgentID: "check-2"},
	}}
	got, err := runGoalContinuation(ledger.input(runner, activeGoal(1), eval, newResult("round1")))
	if err != nil {
		t.Fatalf("runGoalContinuation error: %v", err)
	}
	if got.TextContent() != "round1\nround2" {
		t.Fatalf("text = %q, want both rounds", got.TextContent())
	}
	if eval.calls != 2 {
		t.Fatalf("check calls = %d, want one per round", eval.calls)
	}
	// The second check is told about both rounds, the latest last.
	if last := eval.rounds[1]; len(last) != 2 || last[1].Index != 1 || last[1].Text != "round2" {
		t.Fatalf("second check was told %+v", last)
	}
	rounds := ledger.rounds(t)
	if len(rounds) != 1 || rounds[0].Round != 2 || rounds[0].Why != "two tests still fail" || rounds[0].CheckAgentID != "check-1" {
		t.Fatalf("goal_round_started = %+v", rounds)
	}
	done := ledger.completed(t)
	if done.Status != event.GoalStatusDone || done.Rounds != 2 || done.CheckAgentID != "check-2" {
		t.Fatalf("goal_completed = %+v", done)
	}
	if got := probe.types(); strings.Join(got, ",") != event.RunEventGoalRoundStarted+","+event.RunEventGoalCompleted {
		t.Fatalf("published goal events = %v", got)
	}
}

func TestRunGoalContinuationStopsWhenStuck(t *testing.T) {
	ledger := newGoalLedger(t)
	runner := newLoadedRunnerForSupervisorTest(t, &supervisorScriptLLM{reply: "x"})
	eval := &stubEvaluator{verdicts: []Verdict{{Status: StatusStuck, Why: "the same edit twice"}}}
	got, err := runGoalContinuation(ledger.input(runner, activeGoal(1), eval, newResult("round1")))
	if err != nil || got.TextContent() != "round1" {
		t.Fatalf("result = %q, %v", got.TextContent(), err)
	}
	if done := ledger.completed(t); done.Status != event.GoalStatusStuck || done.Why != "the same edit twice" {
		t.Fatalf("goal_completed = %+v", done)
	}
}

// A check that cannot give a verdict ends the goal with its own error as the
// reason; the round's work stands and the turn does not fail for it.
func TestRunGoalContinuationCheckErrorEndsGoalWithTheError(t *testing.T) {
	ledger := newGoalLedger(t)
	runner := newLoadedRunnerForSupervisorTest(t, &supervisorScriptLLM{reply: "x"})
	eval := &stubEvaluator{err: errors.New("provider returned 529 overloaded")}
	got, err := runGoalContinuation(ledger.input(runner, activeGoal(1), eval, newResult("round1")))
	if err != nil || got.TextContent() != "round1" {
		t.Fatalf("result = %q, %v", got.TextContent(), err)
	}
	done := ledger.completed(t)
	if done.Status != event.GoalStatusFailed || done.Why != "provider returned 529 overloaded" || done.CheckAgentID != "check-failed" {
		t.Fatalf("goal_completed = %+v", done)
	}
}

func TestRunGoalContinuationCancelledCheckInterruptsGoal(t *testing.T) {
	ledger := newGoalLedger(t)
	runner := newLoadedRunnerForSupervisorTest(t, &supervisorScriptLLM{reply: "x"})
	eval := &stubEvaluator{err: context.Canceled}
	_, err := runGoalContinuation(ledger.input(runner, activeGoal(2), eval, newResult("round2")))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want the cancellation", err)
	}
	if done := ledger.completed(t); done.Status != event.GoalStatusInterrupted || done.Rounds != 2 || done.Why != "" {
		t.Fatalf("goal_completed = %+v", done)
	}
}

// errScriptLLM fails every Execute with err.
type errScriptLLM struct{ err error }

func (e errScriptLLM) Execute(context.Context, []llm.Message, []*llm.Tool) (*llm.Result, error) {
	return nil, e.err
}

// A round that fails ends the turn with its error, which the turn reports; the
// goal records only that it ended there.
func TestRunGoalContinuationFailedRoundEndsGoalAndTurn(t *testing.T) {
	ledger := newGoalLedger(t)
	runner := newLoadedRunnerForSupervisorTest(t, errScriptLLM{err: errors.New("run ended")})
	eval := &stubEvaluator{verdicts: []Verdict{{Status: StatusContinue}}}
	got, err := runGoalContinuation(ledger.input(runner, activeGoal(1), eval, newResult("round1")))
	if err == nil || !strings.Contains(err.Error(), "run ended") {
		t.Fatalf("error = %v, want the round's failure", err)
	}
	if got.TextContent() != "round1" {
		t.Fatalf("text = %q, want the rounds that ran", got.TextContent())
	}
	if eval.calls != 1 {
		t.Fatalf("check calls = %d, want 1", eval.calls)
	}
	if done := ledger.completed(t); done.Status != event.GoalStatusFailed || done.Rounds != 2 || done.Why != "" {
		t.Fatalf("goal_completed = %+v", done)
	}
}

func TestRunGoalContinuationCancelledRoundInterruptsGoal(t *testing.T) {
	ledger := newGoalLedger(t)
	runner := newLoadedRunnerForSupervisorTest(t, errScriptLLM{err: context.Canceled})
	eval := &stubEvaluator{verdicts: []Verdict{{Status: StatusContinue}}}
	_, err := runGoalContinuation(ledger.input(runner, activeGoal(1), eval, newResult("round1")))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want the cancellation", err)
	}
	if done := ledger.completed(t); done.Status != event.GoalStatusInterrupted || done.Rounds != 2 {
		t.Fatalf("goal_completed = %+v", done)
	}
}

// A round stopped at an approval suspends the run like any turn would; the
// goal is still on, so nothing records its end.
func TestRunGoalContinuationRoundWaitingOnApprovalKeepsGoal(t *testing.T) {
	ledger := newGoalLedger(t)
	runner := newLoadedRunnerForSupervisorTest(t, errScriptLLM{err: &tool.RequiresActionError{RunID: "run-1", ActionID: "action-1"}})
	eval := &stubEvaluator{verdicts: []Verdict{{Status: StatusContinue}}}
	_, err := runGoalContinuation(ledger.input(runner, activeGoal(1), eval, newResult("round1")))
	var rae *tool.RequiresActionError
	if !errors.As(err, &rae) {
		t.Fatalf("error = %v, want the approval request", err)
	}
	if steps := ledger.steps(t, event.RunEventGoalCompleted); len(steps) != 0 {
		t.Fatalf("a goal waiting on approval recorded %d endings", len(steps))
	}
	if rounds := ledger.rounds(t); len(rounds) != 1 || rounds[0].Round != 2 {
		t.Fatalf("goal_round_started = %+v", rounds)
	}
}

func TestRunGoalContinuationStopsAtRoundLimit(t *testing.T) {
	ledger := newGoalLedger(t)
	runner := newLoadedRunnerForSupervisorTest(t, &supervisorScriptLLM{reply: "again"})
	eval := &stubEvaluator{verdicts: []Verdict{{Status: StatusContinue}}}
	if _, err := runGoalContinuation(ledger.input(runner, activeGoal(1), eval, newResult("round1"))); err != nil {
		t.Fatalf("runGoalContinuation error: %v", err)
	}
	if eval.calls != MaxGoalRounds {
		t.Fatalf("check calls = %d, want %d", eval.calls, MaxGoalRounds)
	}
	if done := ledger.completed(t); done.Status != event.GoalStatusCapped || done.Rounds != MaxGoalRounds {
		t.Fatalf("goal_completed = %+v", done)
	}
}

// runGoalTurn runs one supervised /goal turn the way a surface does, with the
// check answering verdict, and returns its run id and error.
func runGoalTurn(t *testing.T, rt *state.RunStore, script llm.LLM, verdict string, opts Options) (string, error) {
	t.Helper()
	runner := newLoadedRunnerForSupervisorTest(t, script)
	runner.Events = runEventLogSink(rt, "sid")
	runner.SubagentExecutor = &fixedSubagentExecutor{output: verdict}
	opts.RunRT = rt
	opts.Runner = runner
	opts.Hooks = hook.NewAgentPipeline()
	opts.AgBase = context.Background()
	opts.HC = hook.HookContext{SessionID: "sid", Trigger: "user"}
	if opts.Input == "" {
		opts.Input = "ship"
	}
	var runID string
	opts.RunIDOut = &runID
	_, _, err := Run(opts)
	return runID, err
}

func openGoalRunStore(t *testing.T) *state.RunStore {
	t.Helper()
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("OpenStateForTest error: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// The goal tests' runs live under the "sid" conversation their hook
	// context names; the run row's foreign key requires the session.
	if err := state.NewSessionStore(db, "main").Ensure(context.Background(), "sid", "sid"); err != nil {
		t.Fatal(err)
	}
	return &state.RunStore{DB: db}
}

// A goal whose very first round is cancelled or fails ends there, with the
// same record a later round's ending leaves, so no surface is left showing a
// goal that never ended.
func TestGoalWhoseFirstRoundEndsEarlyIsRecordedEnded(t *testing.T) {
	for _, tt := range []struct {
		name   string
		err    error
		status string
	}{
		{"cancelled", context.Canceled, event.GoalStatusInterrupted},
		{"failed", errors.New("provider unreachable"), event.GoalStatusFailed},
		{"refused without words", ErrTurnStoppedByRejection, event.GoalStatusInterrupted},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rt := openGoalRunStore(t)
			runID, err := runGoalTurn(t, rt, errScriptLLM{err: tt.err}, `{"status":"done"}`, Options{GoalObjective: "ship"})
			if err == nil {
				t.Fatal("the round's error must end the turn")
			}
			ledger := goalLedger{rt: rt, runID: runID}
			if started := ledger.steps(t, event.RunEventGoalStarted); len(started) != 1 {
				t.Fatalf("goal_started steps = %d", len(started))
			}
			if done := ledger.completed(t); done.Status != tt.status || done.Rounds != 1 || done.Why != "" {
				t.Fatalf("goal_completed = %+v", done)
			}
		})
	}
}

// A goal whose first round stops at an approval is suspended with it, still
// on; the run resumed with the decision picks it up from its ledger, without
// starting it again, and the check decides how it ends.
func TestGoalWaitingOnApprovalIsResumedFromItsRun(t *testing.T) {
	rt := openGoalRunStore(t)
	rae := &tool.RequiresActionError{ActionID: "action-1"}
	runID, err := runGoalTurn(t, rt, errScriptLLM{err: rae}, "", Options{GoalObjective: "ship"})
	if !errors.As(err, &rae) {
		t.Fatalf("error = %v, want the approval request", err)
	}
	ledger := goalLedger{rt: rt, runID: runID}
	if done := ledger.steps(t, event.RunEventGoalCompleted); len(done) != 0 {
		t.Fatalf("a goal waiting on approval recorded %d endings", len(done))
	}

	_, err = runGoalTurn(t, rt, &supervisorScriptLLM{reply: "approved and done"}, `{"status":"done","why":"shipped"}`, Options{SuperviseExistingRunID: runID})
	if err != nil {
		t.Fatalf("resumed run error: %v", err)
	}
	if started := ledger.steps(t, event.RunEventGoalStarted); len(started) != 1 {
		t.Fatalf("goal_started steps = %d, want the one the goal began with", len(started))
	}
	if done := ledger.completed(t); done.Status != event.GoalStatusDone || done.Rounds != 1 || done.Why != "shipped" || done.Objective != "ship" {
		t.Fatalf("goal_completed = %+v", done)
	}
}

// A run resumed after an approval learns from its ledger that it was working
// toward a goal and in which round, and goes on counting from there.
func TestGoalOfRunResumesFromTheLedger(t *testing.T) {
	ledger := newGoalLedger(t)
	ctx := context.Background()
	if got := goalOfRun(ctx, ledger.rt, ledger.runID); got.active() {
		t.Fatalf("a run without a goal resumed %+v", got)
	}
	appendGoalEvent := func(id, typ string, payload any) {
		raw, marshalErr := json.Marshal(payload)
		if marshalErr != nil {
			t.Fatalf("marshal %s: %v", typ, marshalErr)
		}
		if _, appendErr := ledger.rt.AppendSessionEvent(ctx, state.SessionEvent{
			ID: id, SessionID: "sid", RunID: ledger.runID, Type: typ, Payload: raw, CreatedAt: time.Now(),
		}); appendErr != nil {
			t.Fatalf("append %s: %v", typ, appendErr)
		}
	}
	appendGoalEvent("goal-started", event.RunEventGoalStarted, event.GoalStartedPayload{Objective: "ship", MaxRounds: MaxGoalRounds})
	if got := goalOfRun(ctx, ledger.rt, ledger.runID); got.objective != "ship" || got.rounds != 1 {
		t.Fatalf("goal after its start = %+v", got)
	}
	appendGoalEvent("goal-round:3", event.RunEventGoalRoundStarted, event.GoalRoundStartedPayload{Round: 3})
	resumed := goalOfRun(ctx, ledger.rt, ledger.runID)
	if resumed.objective != "ship" || resumed.rounds != 3 {
		t.Fatalf("goal after round 3 = %+v", resumed)
	}

	runner := newLoadedRunnerForSupervisorTest(t, &supervisorScriptLLM{reply: "round4"})
	eval := &stubEvaluator{verdicts: []Verdict{{Status: StatusContinue}, {Status: StatusDone}}}
	if _, err := runGoalContinuation(ledger.input(runner, resumed, eval, newResult("round3"))); err != nil {
		t.Fatalf("runGoalContinuation error: %v", err)
	}
	if first := eval.rounds[0]; len(first) != 1 || first[0].Index != 2 {
		t.Fatalf("the resumed goal's first check was told %+v, want round 3", first)
	}
	rounds := ledger.rounds(t)
	if last := rounds[len(rounds)-1]; last.Round != 4 {
		t.Fatalf("the resumed goal's next round = %d, want 4", last.Round)
	}
	if done := ledger.completed(t); done.Rounds != 4 {
		t.Fatalf("goal_completed = %+v", done)
	}
	if got := goalOfRun(ctx, ledger.rt, ledger.runID); got.active() {
		t.Fatalf("a goal that ended resumed %+v", got)
	}
}

// Every round is on the transcript before the next one starts, and the next
// round's line is drawn after it; writing the same rounds again adds nothing.
func TestPersistGoalRoundsWritesRoundsAndAnchorsTheNextLine(t *testing.T) {
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("OpenStateForTest error: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := state.NewSessionStore(db, "main")
	if err := store.Ensure(ctx, "sid", "sid"); err != nil {
		t.Fatalf("Ensure error: %v", err)
	}
	runner := &Runner{Deps: &Deps{SessionStore: store}}
	res := &agent.Result{Session: []llm.Message{
		llm.UserMessage(llm.Text("/goal ship")),
		llm.AssistantMessage([]llm.ContentPart{llm.Text("round 1 done")}),
	}}

	anchor := persistGoalRounds(ctx, runner, "sid", res)
	last, err := store.LastTranscriptRowID(ctx, "sid")
	if err != nil {
		t.Fatalf("LastTranscriptRowID error: %v", err)
	}
	if anchor == 0 || anchor != last {
		t.Fatalf("anchor = %d, want the newest row %d", anchor, last)
	}
	if again := persistGoalRounds(ctx, runner, "sid", res); again != anchor {
		t.Fatalf("writing the same rounds again moved the anchor %d -> %d", anchor, again)
	}
	var rows int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM fb_messages WHERE session_id = 'sid'`).Scan(&rows); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if rows != 2 {
		t.Fatalf("rows = %d, want the two messages once", rows)
	}
}

// The prompt opening a continuation round is sent to the model like any user
// message but carries IsMeta, so no surface shows it as something the user
// said.
func TestGoalContinuationInputIsSentAsMeta(t *testing.T) {
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("OpenStateForTest error: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := state.NewSessionStore(db, "main")
	if err := store.Ensure(ctx, "sid", "sid"); err != nil {
		t.Fatalf("Ensure error: %v", err)
	}
	build := transcriptSession{store: store, systemPrompt: "sys"}.build
	prompt := []llm.ContentPart{llm.Text(ContinuationPrompt("ship", "tests fail"))}

	msgs, _, err := build(withGoalContinuationInput(llm.WithAgentSessionID(ctx, "sid")), prompt)
	if err != nil {
		t.Fatalf("build error: %v", err)
	}
	if last := msgs[len(msgs)-1]; last.Role != llm.RoleUser || !last.IsMeta || !strings.Contains(last.TextContent(), "ship") {
		t.Fatalf("continuation input = %+v", last)
	}
	msgs, _, err = build(llm.WithAgentSessionID(ctx, "sid"), []llm.ContentPart{llm.Text("hello")})
	if err != nil {
		t.Fatalf("build error: %v", err)
	}
	if last := msgs[len(msgs)-1]; last.IsMeta {
		t.Fatalf("an ordinary user message was marked meta")
	}
}

// The check is the read-only goal-evaluator subagent, dispatched even with
// agents.defaults.enable_subagent off because the user asked for the goal; its
// roster key comes back with the verdict so a surface can open its view.
func TestGoalCheckDispatchesTheEvaluatorSubagent(t *testing.T) {
	probe := &dispatchProbe{}
	executor := &fixedSubagentExecutor{output: "I ran the tests; all 42 pass.\n{\"status\": \"done\", \"why\": \"all 42 tests pass\"}"}
	runner := &Runner{Deps: &Deps{Home: t.TempDir()}, Events: probe, SubagentExecutor: executor}

	verdict, err := goalCheck{runner: runner}.Evaluate(dispatchTestCtx(), "make the tests pass", []RoundOutcome{{Index: 0, Text: "fixed the parser"}})
	if err != nil {
		t.Fatalf("Evaluate error: %v", err)
	}
	if verdict.Status != StatusDone || verdict.Why != "all 42 tests pass" {
		t.Fatalf("verdict = %+v", verdict)
	}
	if executor.subagentType != GoalCheckSubagentType {
		t.Fatalf("check ran as %q", executor.subagentType)
	}
	if !strings.Contains(executor.task, "make the tests pass") || !strings.Contains(executor.task, "fixed the parser") {
		t.Fatalf("check task = %q", executor.task)
	}
	if len(probe.spawned) != 1 || probe.spawned[0].AgentType != GoalCheckSubagentType {
		t.Fatalf("spawned = %+v", probe.spawned)
	}
	if verdict.CheckAgentID == "" || verdict.CheckAgentID != probe.spawned[0].AgentID {
		t.Fatalf("verdict names check %q, roster has %+v", verdict.CheckAgentID, probe.spawned)
	}
}

func TestGoalCheckWithoutAVerdictIsAnError(t *testing.T) {
	executor := &fixedSubagentExecutor{output: "I could not decide."}
	runner := &Runner{Deps: &Deps{Home: t.TempDir()}, Events: &dispatchProbe{}, SubagentExecutor: executor}
	verdict, err := goalCheck{runner: runner}.Evaluate(dispatchTestCtx(), "ship", nil)
	if err == nil || !strings.Contains(err.Error(), "I could not decide.") {
		t.Fatalf("error = %v, want the check's own words", err)
	}
	if verdict.CheckAgentID == "" {
		t.Fatal("a failed check must still name its view")
	}
}

func TestGoalCheckTaskTellsOnlyTheLatestRounds(t *testing.T) {
	var rounds []RoundOutcome
	for i := range 5 {
		rounds = append(rounds, RoundOutcome{Index: i, Text: "work of round " + string(rune('A'+i))})
	}
	task := goalCheckTask("ship", rounds)
	for _, gone := range []string{"round A", "round B"} {
		if strings.Contains(task, gone) {
			t.Fatalf("task still tells %q: %s", gone, task)
		}
	}
	for _, kept := range []string{"Round 3", "Round 4", "Round 5", "round E"} {
		if !strings.Contains(task, kept) {
			t.Fatalf("task misses %q: %s", kept, task)
		}
	}
}

func TestParseGoalVerdict(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		want    Verdict
		wantErr string
	}{
		{"verdict alone", `{"status":"stuck","why":"going in circles"}`, Verdict{Status: StatusStuck, Why: "going in circles"}, ""},
		{"reasoning before the verdict", "Checked `go test ./...`: {one} failure.\n\n{\"status\": \"Continue\", \"why\": \" one test fails \"}", Verdict{Status: StatusContinue, Why: "one test fails"}, ""},
		{"braces inside the reason", `{"status":"done","why":"map[string]{} is initialised"}`, Verdict{Status: StatusDone, Why: "map[string]{} is initialised"}, ""},
		{"the last verdict wins", `{"status":"continue","why":"a"} then {"status":"done","why":"b"}`, Verdict{Status: StatusDone, Why: "b"}, ""},
		{"no verdict", "all good I think", Verdict{}, "ended without a verdict"},
		{"unknown status", `{"status":"maybe","why":"x"}`, Verdict{}, `"maybe"`},
		{"no status", `{"why":"x"}`, Verdict{}, "no status"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseGoalVerdict(tt.output)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("verdict = %+v, %v; want %+v", got, err, tt.want)
			}
		})
	}
}

func TestDecide(t *testing.T) {
	tests := []struct {
		name         string
		rounds       int
		verdict      Verdict
		wantContinue bool
		wantReason   string
	}{
		{"done stops", 1, Verdict{Status: StatusDone, Why: "ok"}, false, event.GoalStatusDone},
		{"stuck stops", 3, Verdict{Status: StatusStuck, Why: "circles"}, false, event.GoalStatusStuck},
		{"continue below cap", 5, Verdict{Status: StatusContinue}, true, ""},
		{"continue at cap stops", MaxGoalRounds, Verdict{Status: StatusContinue}, false, event.GoalStatusCapped},
		{"continue past cap stops", MaxGoalRounds + 1, Verdict{Status: StatusContinue}, false, event.GoalStatusCapped},
		{"done wins over cap", MaxGoalRounds, Verdict{Status: StatusDone}, false, event.GoalStatusDone},
		{"stuck wins over cap", MaxGoalRounds, Verdict{Status: StatusStuck, Why: "circles"}, false, event.GoalStatusStuck},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Decide(tt.rounds, tt.verdict)
			if got.Continue != tt.wantContinue {
				t.Fatalf("Continue=%v want %v", got.Continue, tt.wantContinue)
			}
			if !tt.wantContinue && got.Reason != tt.wantReason {
				t.Fatalf("Reason=%q want %q", got.Reason, tt.wantReason)
			}
		})
	}
}

func TestContinuationPromptCarriesObjectiveAndWhy(t *testing.T) {
	got := ContinuationPrompt("ship the feature", "tests still failing")
	if !strings.Contains(got, "ship the feature") {
		t.Fatalf("prompt missing objective: %q", got)
	}
	if !strings.Contains(got, "tests still failing") {
		t.Fatalf("prompt missing why: %q", got)
	}
	if strings.Contains(strings.ToLower(got), "token") {
		t.Fatalf("prompt must not mention tokens: %q", got)
	}
}

func TestContinuationPromptWithoutWhy(t *testing.T) {
	got := ContinuationPrompt("ship it", "")
	if !strings.Contains(got, "ship it") || strings.Contains(got, "Assessment") {
		t.Fatalf("prompt = %q", got)
	}
}

// The /goal check's row in the tests below: one configured model whose own
// default effort is "high", chosen on the settings page for every primary
// agent. The primary agent's own model is a different one on purpose — see
// goalCheckConfig.
const (
	goalCheckRowID       = "llm-goal-check-row"
	goalCheckRowProvider = "deepseek"
	goalCheckRowModel    = "deepseek-v4-flash"
	goalCheckRowEffort   = "high"
)

// goalCheckConfig is an install whose settings page configured the goal
// check's row. The primary agent's own model (glm-5.3) is deliberately
// different, and so is the first row in the install's model table: what the
// check runs on has to come from the settings field, never from the
// conversation and never from "whichever row is first".
func goalCheckConfig() *appcfg.Root {
	return &appcfg.Root{
		LLMModels: []appcfg.LLMModelConfig{
			{
				ID: "llm-session-row", Provider: "openai", Model: "gpt-4o",
				BaseURL: "https://api.openai.invalid/v1", APIPath: "/chat/completions", APIKey: "sk-session-row",
			},
			{
				ID:       goalCheckRowID,
				Provider: goalCheckRowProvider,
				Model:    goalCheckRowModel,
				BaseURL:  "https://api.deepseek.invalid",
				APIPath:  "/chat/completions",
				APIKey:   "sk-goal-check",
				Caps:     appcfg.LLMModelCaps{DefaultEffort: goalCheckRowEffort},
			},
		},
		Agents: appcfg.AgentsSection{
			Definitions: map[string]appcfg.AgentDefinition{
				"main": {
					Primary: true,
					LLMProviders: []appcfg.AgentLLMProviderConfig{
						{Provider: "zhipuai", Model: "glm-5.3", APIKey: "key", BaseURL: "https://example.invalid"},
					},
				},
			},
			GoalEvaluator: appcfg.SubagentConfig{Model: goalCheckRowID},
		},
	}
}

// The goal check's model comes from the settings page and nowhere else: one
// global row for every primary agent, so the check takes the row id, provider
// and model the configuration names (plan 299, two-level semantics).
func TestGoalCheckRunsOnTheConfiguredRow(t *testing.T) {
	probe := &dispatchProbe{}
	executor := &fixedSubagentExecutor{output: `{"status": "done", "why": "configured"}`}
	runner := &Runner{Deps: &Deps{Home: t.TempDir(), AppCfg: goalCheckConfig()}, Events: probe, SubagentExecutor: executor}

	verdict, err := goalCheck{runner: runner}.Evaluate(dispatchTestCtx(), "make the tests pass", []RoundOutcome{{Index: 0, Text: "fixed the parser"}})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if verdict.Status != StatusDone {
		t.Fatalf("verdict = %+v", verdict)
	}

	// fixedSubagentExecutor.RunSubagentExec (pkg/run/subagent_test.go:2267-2283)
	// records two distinct facts: `model` = req.Model (what the record says) and
	// `pinnedModel` = the model on the execution's context (what its requests
	// are routed to). Both are the configured row.
	if executor.pinnedModel != goalCheckRowModel {
		t.Fatalf("goal check pinnedModel = %q, want the configured row %q", executor.pinnedModel, goalCheckRowModel)
	}
	if executor.model != goalCheckRowModel {
		t.Fatalf("goal check req model = %q, want the configured row %q", executor.model, goalCheckRowModel)
	}
	// The spawn announcement carries the same fact, plus the effort the row's
	// own default gives it (the settings page stores "" for "follow the row").
	if len(probe.spawned) != 1 {
		t.Fatalf("spawned = %+v, want exactly one goal check", probe.spawned)
	}
	if got := probe.spawned[0]; got.AgentType != GoalCheckSubagentType ||
		got.Provider != goalCheckRowProvider || got.Model != goalCheckRowModel || got.Effort != goalCheckRowEffort {
		t.Fatalf("spawned %#v, want %s/%s effort %s as %s",
			got, goalCheckRowProvider, goalCheckRowModel, goalCheckRowEffort, GoalCheckSubagentType)
	}
}

// A dispatch-time pin is a caller's choice, and the goal check is not the
// caller's: /goal takes its model from the settings page, so a pin another
// caller put on the context (plan 283's approval-card pick, or anything else)
// reaches no goal check. This is the direct assertion of the owner's two-level
// decision (plan 299 §1).
func TestGoalCheckIgnoresADispatchTimeModelPin(t *testing.T) {
	probe := &dispatchProbe{}
	executor := &fixedSubagentExecutor{output: `{"status": "done", "why": "configured"}`}
	runner := &Runner{Deps: &Deps{Home: t.TempDir(), AppCfg: goalCheckConfig()}, Events: probe, SubagentExecutor: executor}
	// Another configured row, pinned the way the plan review's approval card
	// pins the user's pick.
	ctx := WithSubagentModelOverride(dispatchTestCtx(), SubagentModelOverride{
		ModelID: "llm-approval-card", Provider: "zhipuai", Model: "glm-5.3", Effort: "max",
	})

	if _, err := (goalCheck{runner: runner}).Evaluate(ctx, "make the tests pass", nil); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if executor.pinnedModel != goalCheckRowModel {
		t.Fatalf("goal check pinnedModel = %q, want the configured row %q, not the dispatch-time pin",
			executor.pinnedModel, goalCheckRowModel)
	}
	if executor.model != goalCheckRowModel {
		t.Fatalf("goal check req model = %q, want the configured row %q", executor.model, goalCheckRowModel)
	}
	if len(probe.spawned) != 1 || probe.spawned[0].Model != goalCheckRowModel {
		t.Fatalf("spawned = %+v, want the configured row %q", probe.spawned, goalCheckRowModel)
	}
}

// With nothing configured the check keeps the behaviour it has always had: it
// runs on the conversation's model, resolved through AgentModel ->
// PrimaryModelStateForSession.
func TestGoalCheckFollowsTheSessionModelWhenUnconfigured(t *testing.T) {
	probe := &dispatchProbe{}
	executor := &fixedSubagentExecutor{output: `{"status": "done", "why": "unconfigured"}`}
	cfg := goalCheckConfig()
	cfg.Agents.GoalEvaluator = appcfg.SubagentConfig{}
	runner := &Runner{Deps: &Deps{Home: t.TempDir(), AppCfg: cfg}, Events: probe, SubagentExecutor: executor}

	if _, err := (goalCheck{runner: runner}).Evaluate(dispatchTestCtx(), "ship", nil); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if executor.model != "glm-5.3" {
		t.Fatalf("goal check req model = %q, want the session's own model glm-5.3", executor.model)
	}
	if len(probe.spawned) != 1 || probe.spawned[0].Model != "glm-5.3" {
		t.Fatalf("spawned = %+v, want the session's own model", probe.spawned)
	}
}

// The settings page's row is global, so it must survive the two early returns
// in SubagentCatalogFor: cfg == nil is covered by leaving it nil, and a
// primary agent with no definition of its own — a fresh install's shape — must
// still run its goal check on the configured row (plan 299 §4 pitfall ①).
func TestGoalCheckConfigAppliesToAgentsWithoutDefinitions(t *testing.T) {
	probe := &dispatchProbe{}
	executor := &fixedSubagentExecutor{output: `{"status": "done", "why": "no definitions"}`}
	cfg := goalCheckConfig()
	cfg.Agents.Definitions = nil
	runner := &Runner{Deps: &Deps{Home: t.TempDir(), AppCfg: cfg, AgentName: "main"}, Events: probe, SubagentExecutor: executor}

	if _, err := (goalCheck{runner: runner}).Evaluate(dispatchTestCtx(), "ship", nil); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if executor.model != goalCheckRowModel {
		t.Fatalf("goal check req model = %q, want the configured row %q", executor.model, goalCheckRowModel)
	}
	if len(probe.spawned) != 1 || probe.spawned[0].Model != goalCheckRowModel {
		t.Fatalf("spawned = %+v, want the configured row %q", probe.spawned, goalCheckRowModel)
	}
}

// The settings page stores "" for "follow the model page's thinking level",
// and that has to be a live reading, not a snapshot (plan 299 §6-⑪): a check
// whose row default says "high" sends high, and after the row's default moves
// the next check sends the new level — with nothing of its own to override.
// An effort the page did set is the opposite: it is sent as stored and does
// not move with the row.
//
// The effort is read off the request the run actually sends: the provider
// client's chat-completions body carries "reasoning_effort", which is where
// the engine folds a model's default level into a request that names none.
func TestGoalCheckEffortFollowsTheRowDefaultUntilOverridden(t *testing.T) {
	var mu sync.Mutex
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		rw.Header().Set("Content-Type", "text/event-stream")
		for _, frame := range []string{
			`{"id":"chatcmpl-goal","object":"chat.completion.chunk","created":1,"model":"gpt-5","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"},"finish_reason":null}]}`,
			`{"id":"chatcmpl-goal","object":"chat.completion.chunk","created":1,"model":"gpt-5","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		} {
			_, _ = fmt.Fprintf(rw, "data: %s\n\n", frame)
		}
		_, _ = fmt.Fprint(rw, "data: [DONE]\n\n")
	}))
	defer server.Close()

	cfg := &appcfg.Root{
		LLMModels: []appcfg.LLMModelConfig{{
			ID:       "llm-goal-effort",
			Provider: "openai",
			Model:    "gpt-5",
			BaseURL:  server.URL + "/v1",
			APIKey:   "sk-goal-effort",
			Caps:     appcfg.LLMModelCaps{DefaultEffort: "high"},
		}},
		Agents: appcfg.AgentsSection{
			GoalEvaluator: appcfg.SubagentConfig{Model: "llm-goal-effort"},
		},
	}

	// dispatch runs one real goal-check dispatch and answers the pin it decided
	// on: the one place the configured row becomes the execution's model.
	dispatch := func(t *testing.T) SubagentModelOverride {
		t.Helper()
		home := t.TempDir()
		owner := &Runner{Deps: &Deps{Home: home, AppCfg: cfg, AgentName: "main"},
			SubagentExecutor: &fixedSubagentExecutor{output: `{"status": "done", "why": "ok"}`}}
		var pin SubagentModelOverride
		var pinned bool
		if _, _, err := dispatchSubagent(dispatchTestCtx(), Factory{AgentName: "main", Home: home, AppCfg: cfg, Owner: owner}, subagentDispatch{
			taskID:  "task-goal-effort",
			task:    "judge the objective",
			subtype: GoalCheckSubagentType,
			resolve: agent.ResolveSubtype,
			run: func(_ context.Context, _ Factory, prep preparedSubagent) (string, error) {
				pin, pinned = SubagentModelOverrideFromContext(prep.ctx)
				return "done", nil
			},
		}); err != nil {
			t.Fatalf("dispatchSubagent: %v", err)
		}
		if !pinned {
			t.Fatal("the goal check dispatched without a model pin")
		}
		return pin
	}

	// send builds the client the run's requests go through — the same builder
	// the runtime's override wrapper calls — and answers the effort its request
	// carried.
	send := func(t *testing.T, pin SubagentModelOverride) string {
		t.Helper()
		client, err := ConfiguredModelClientWithEffort(cfg, GoalCheckSubagentType, pin.ModelID, pin.Provider, pin.Model, pin.Effort)
		if err != nil {
			t.Fatalf("ConfiguredModelClientWithEffort: %v", err)
		}
		if _, err := client.Execute(context.Background(), []llm.Message{llm.UserMessage(llm.Text("judge"))}, nil); err != nil {
			t.Fatalf("Execute: %v", err)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(bodies) == 0 {
			t.Fatal("the provider server saw no request")
		}
		body := bodies[len(bodies)-1]
		effort, _ := body["reasoning_effort"].(string)
		return effort
	}

	// The page stored no effort: the check follows the row's own default.
	pin := dispatch(t)
	if pin.ModelID != "llm-goal-effort" || pin.Model != "gpt-5" || strings.TrimSpace(pin.Effort) != "" {
		t.Fatalf("pin = %#v, want the configured row with no effort of its own", pin)
	}
	if got := send(t, pin); got != "high" {
		t.Fatalf("request effort = %q, want the row's default %q", got, "high")
	}

	// The model page moves the row's default, the configuration is adopted
	// again, and the next check carries the new level — not the old one, and
	// not an empty request.
	cfg.LLMModels[0].Caps.DefaultEffort = "low"
	if got := send(t, dispatch(t)); got != "low" {
		t.Fatalf("request effort after the row default moved = %q, want %q", got, "low")
	}

	// An effort the page did set is the opposite: sent as stored, unmoved by
	// the row's default.
	cfg.Agents.GoalEvaluator.ReasoningEffort = "max"
	pin = dispatch(t)
	if pin.Effort != "max" {
		t.Fatalf("pin effort = %q, want the configured override %q", pin.Effort, "max")
	}
	if got := send(t, pin); got != "max" {
		t.Fatalf("request effort = %q, want the configured override %q", got, "max")
	}
	cfg.LLMModels[0].Caps.DefaultEffort = "minimal"
	if got := send(t, dispatch(t)); got != "max" {
		t.Fatalf("request effort after the row default moved = %q, want the override %q to stand", got, "max")
	}
}
