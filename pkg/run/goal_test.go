package run

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
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
