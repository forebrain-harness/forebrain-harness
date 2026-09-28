package turn

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

func testStore(t *testing.T) *state.CronStore {
	t.Helper()
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("OpenStateForTest: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &state.CronStore{DB: db}
}

type recorder struct {
	mu         sync.Mutex
	prompts    []string
	sessions   []string
	delivered  []string
	output     string
	errText    string
	deliverErr error
}

func (r *recorder) run(_ context.Context, sessionID, _, prompt string) (string, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prompts = append(r.prompts, prompt)
	r.sessions = append(r.sessions, sessionID)
	return r.output, r.errText
}

func (r *recorder) deliver(_ context.Context, channelID, _, text string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.deliverErr != nil {
		return r.deliverErr
	}
	r.delivered = append(r.delivered, channelID+":"+text)
	return nil
}

func (r *recorder) snapshot() ([]string, []string, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.prompts...), append([]string(nil), r.sessions...), append([]string(nil), r.delivered...)
}

// A due job runs in a session of its own, delivers where it was told, and is
// rescheduled — the whole lifecycle of one recurring job.
func TestSchedulerFiresDueJobInItsOwnSessionAndReschedules(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	rec := &recorder{output: "three callers"}
	now := time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC)

	job := state.CronJob{ID: "job-1", Ag: "main", Name: "hourly", Sched: "every 1h", Prompt: "check the queue", Deliver: "telegram", Enabled: true}
	if err := ApplySchedule(&job, now.Add(-2*time.Hour)); err != nil {
		t.Fatalf("ApplySchedule: %v", err)
	}
	if err := store.InsertJob(ctx, job); err != nil {
		t.Fatalf("SaveJob: %v", err)
	}

	s := &Scheduler{Store: store, AgentID: "main", Deps: SchedulerDeps{
		RunPrompt: rec.run, DeliverText: rec.deliver, Now: func() time.Time { return now },
	}}
	s.RunDue(ctx)
	waitIdle(t, s)

	prompts, sessions, delivered := rec.snapshot()
	if len(prompts) != 1 || prompts[0] != "check the queue" {
		t.Fatalf("prompts = %#v", prompts)
	}
	if len(sessions) != 1 || sessions[0] == "" || sessions[0] == "main" {
		t.Fatalf("a job must run in a session of its own, got %#v", sessions)
	}
	if len(delivered) != 1 || delivered[0] != "telegram:three callers" {
		t.Fatalf("delivered = %#v", delivered)
	}

	saved, err := store.GetJob(ctx, "job-1")
	if err != nil || saved == nil {
		t.Fatalf("GetJob: %v", err)
	}
	if saved.LastStatus != state.CronStatusOK || saved.RunCount != 1 || saved.FailureStreak != 0 {
		t.Fatalf("job after fire = %#v", saved)
	}
	if saved.NextRunAt == nil || *saved.NextRunAt != now.Add(time.Hour).Unix() {
		t.Fatalf("next run = %v, want an hour after the fire", saved.NextRunAt)
	}

	runs, err := store.ListRuns(ctx, "job-1", 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = %#v (%v)", runs, err)
	}
	if runs[0].Status != state.CronStatusOK || runs[0].Output != "three callers" || runs[0].DeliveredTo != "telegram" {
		t.Fatalf("run record = %#v", runs[0])
	}
}

// A one-shot is consumed by its fire: the record stays readable, but nothing
// schedules it again.
func TestSchedulerConsumesAOneShotJob(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	rec := &recorder{output: "done"}
	now := time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC)

	job := state.CronJob{ID: "job-once", Ag: "main", Sched: "in 1m", Prompt: "remind me", Enabled: true}
	if err := ApplySchedule(&job, now.Add(-2*time.Minute)); err != nil {
		t.Fatalf("ApplySchedule: %v", err)
	}
	if job.RepeatLimit != 1 {
		t.Fatalf("a one-shot must default to a single fire, got %d", job.RepeatLimit)
	}
	_ = store.InsertJob(ctx, job)

	s := &Scheduler{Store: store, AgentID: "main", Deps: SchedulerDeps{RunPrompt: rec.run, Now: func() time.Time { return now }}}
	s.RunDue(ctx)
	waitIdle(t, s)

	saved, _ := store.GetJob(ctx, "job-once")
	if saved.NextRunAt != nil {
		t.Fatalf("a spent one-shot must not be scheduled again: %v", *saved.NextRunAt)
	}
	if saved.RunCount != 1 || saved.LastOutput != "done" {
		t.Fatalf("job after fire = %#v", saved)
	}
}

// A failed run and a failed delivery are different problems: only the first
// counts against the streak, because the second means the work was done.
func TestSchedulerSeparatesRunFailureFromDeliveryFailure(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC)

	failing := &recorder{errText: "provider refused"}
	job := state.CronJob{ID: "job-fail", Ag: "main", Sched: "every 1h", Prompt: "x", Enabled: true}
	_ = ApplySchedule(&job, now.Add(-2*time.Hour))
	_ = store.InsertJob(ctx, job)
	s := &Scheduler{Store: store, AgentID: "main", Deps: SchedulerDeps{RunPrompt: failing.run, Now: func() time.Time { return now }}}
	s.RunDue(ctx)
	waitIdle(t, s)
	saved, _ := store.GetJob(ctx, "job-fail")
	if saved.LastStatus != state.CronStatusFailed || saved.FailureStreak != 1 {
		t.Fatalf("failed run = %#v", saved)
	}

	undeliverable := &recorder{output: "ok", deliverErr: errDelivery{}}
	job2 := state.CronJob{ID: "job-undeliverable", Ag: "main", Sched: "every 1h", Prompt: "x", Deliver: "slack", Enabled: true}
	_ = ApplySchedule(&job2, now.Add(-2*time.Hour))
	_ = store.InsertJob(ctx, job2)
	s2 := &Scheduler{Store: store, AgentID: "main", Deps: SchedulerDeps{RunPrompt: undeliverable.run, DeliverText: undeliverable.deliver, Now: func() time.Time { return now }}}
	s2.RunDue(ctx)
	waitIdle(t, s2)
	saved2, _ := store.GetJob(ctx, "job-undeliverable")
	if saved2.LastStatus != state.CronStatusDeliveryError {
		t.Fatalf("delivery failure status = %q", saved2.LastStatus)
	}
	if saved2.FailureStreak != 0 {
		t.Fatalf("a delivery failure must not count against the run streak, got %d", saved2.FailureStreak)
	}
	if saved2.LastOutput != "ok" {
		t.Fatalf("the answer must still be recorded when only delivery failed: %#v", saved2)
	}
}

type errDelivery struct{}

func (errDelivery) Error() string { return "channel unreachable" }

// Only the bound tenant's jobs run: another primary agent's job is not this
// scheduler's to fire.
func TestSchedulerOnlyFiresItsOwnAgentsJobs(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	rec := &recorder{output: "x"}
	now := time.Now()

	other := state.CronJob{ID: "job-other", Ag: "research", Sched: "every 1h", Prompt: "not yours", Enabled: true}
	_ = ApplySchedule(&other, now.Add(-2*time.Hour))
	_ = store.InsertJob(ctx, other)

	s := &Scheduler{Store: store, AgentID: "main", Deps: SchedulerDeps{RunPrompt: rec.run, Now: func() time.Time { return now }}}
	s.RunDue(ctx)
	waitIdle(t, s)

	if prompts, _, _ := rec.snapshot(); len(prompts) != 0 {
		t.Fatalf("another agent's job was fired: %#v", prompts)
	}
}

// A heartbeat fires into its own session, and never while that session is busy.
func TestHeartbeatFiresIntoItsSessionAndYieldsToARunningTurn(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC)
	if err := state.NewSessionStore(store.DB, "main").Ensure(ctx, "s-1", "s-1"); err != nil {
		t.Fatalf("ensure session: %v", err)
	}

	hb := state.Heartbeat{SessionID: "s-1", IntervalSec: 300, Prompt: "anything new?"}
	if err := ApplyHeartbeatInterval(&hb, now.Add(-10*time.Minute), false); err != nil {
		t.Fatalf("ApplyHeartbeatInterval: %v", err)
	}
	_ = store.SaveHeartbeat(ctx, hb)

	busy := &recorder{output: ""}
	sBusy := &Scheduler{Store: store, AgentID: "main", Deps: SchedulerDeps{
		RunPrompt: busy.run, SessionBusy: func(string) bool { return true }, Now: func() time.Time { return now },
	}}
	sBusy.RunDue(ctx)
	waitIdle(t, sBusy)
	if prompts, _, _ := busy.snapshot(); len(prompts) != 0 {
		t.Fatalf("a heartbeat must not interrupt a running turn: %#v", prompts)
	}
	afterSkip, _ := store.GetHeartbeat(ctx, "s-1")
	if afterSkip.NextRunAt == nil || *afterSkip.NextRunAt != now.Add(5*time.Minute).Unix() {
		t.Fatalf("a skipped beat must re-anchor rather than pile up: %v", afterSkip.NextRunAt)
	}
	if afterSkip.LastFiredAt != nil {
		t.Fatal("a skipped beat did not fire and must not be recorded as having fired")
	}

	idle := &recorder{}
	later := now.Add(6 * time.Minute)
	sIdle := &Scheduler{Store: store, AgentID: "main", Deps: SchedulerDeps{
		RunPrompt: idle.run, SessionBusy: func(string) bool { return false }, Now: func() time.Time { return later },
	}}
	sIdle.RunDue(ctx)
	waitIdle(t, sIdle)
	prompts, sessions, _ := idle.snapshot()
	if len(prompts) != 1 || prompts[0] != "anything new?" {
		t.Fatalf("prompts = %#v", prompts)
	}
	if sessions[0] != "s-1" {
		t.Fatalf("a heartbeat must fire into its own session, got %q", sessions[0])
	}
	fired, _ := store.GetHeartbeat(ctx, "s-1")
	if fired.LastFiredAt == nil || *fired.LastFiredAt != later.Unix() {
		t.Fatalf("a fired beat must record when it fired: %#v", fired)
	}
	if fired.NextRunAt == nil || *fired.NextRunAt != later.Add(5*time.Minute).Unix() {
		t.Fatalf("heartbeat after firing = %#v", fired)
	}
}

// A pause the user makes while a job is running must survive the job's own
// outcome write: the result still lands, but the schedule stays as the user
// left it rather than being restored from the pre-fire snapshot.
func TestSchedulerKeepsAPauseMadeWhileAJobRuns(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC)
	job := state.CronJob{ID: "job-pause", Ag: "main", Sched: "every 1h", Prompt: "x", Enabled: true}
	if err := ApplySchedule(&job, now.Add(-2*time.Hour)); err != nil {
		t.Fatalf("ApplySchedule: %v", err)
	}
	if err := store.InsertJob(ctx, job); err != nil {
		t.Fatalf("InsertJob: %v", err)
	}

	var pauseErr error
	s := &Scheduler{Store: store, AgentID: "main", Deps: SchedulerDeps{
		RunPrompt: func(ctx context.Context, _, _, _ string) (string, string) {
			cur, gerr := store.GetJob(ctx, "job-pause")
			if gerr == nil && cur != nil {
				cur.Enabled = false
				cur.NextRunAt = nil
				pauseErr = store.UpdateJobConfig(ctx, *cur)
			}
			return "done", ""
		},
		Now: func() time.Time { return now },
	}}
	s.RunDue(ctx)
	waitIdle(t, s)

	if pauseErr != nil {
		t.Fatalf("pause mid-run: %v", pauseErr)
	}
	saved, _ := store.GetJob(ctx, "job-pause")
	if saved.Enabled || saved.NextRunAt != nil {
		t.Fatalf("a pause made mid-run was undone by the outcome write: %#v", saved)
	}
	if saved.LastStatus != state.CronStatusOK || saved.LastOutput != "done" {
		t.Fatalf("the fire's result must still land: %#v", saved)
	}
}

// A job deleted while it runs stays deleted: the outcome write must not
// recreate it from the pre-fire snapshot.
func TestSchedulerDoesNotResurrectAJobDeletedWhileItRuns(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC)
	job := state.CronJob{ID: "job-del", Ag: "main", Sched: "every 1h", Prompt: "x", Enabled: true}
	if err := ApplySchedule(&job, now.Add(-2*time.Hour)); err != nil {
		t.Fatalf("ApplySchedule: %v", err)
	}
	if err := store.InsertJob(ctx, job); err != nil {
		t.Fatalf("InsertJob: %v", err)
	}

	s := &Scheduler{Store: store, AgentID: "main", Deps: SchedulerDeps{
		RunPrompt: func(ctx context.Context, _, _, _ string) (string, string) {
			_ = store.DeleteJob(ctx, "job-del")
			return "done", ""
		},
		Now: func() time.Time { return now },
	}}
	s.RunDue(ctx)
	waitIdle(t, s)

	saved, _ := store.GetJob(ctx, "job-del")
	if saved != nil {
		t.Fatalf("a job deleted while it ran was resurrected: %#v", saved)
	}
}

// Two schedulers reading the same due job fire it once: the schedule CAS is
// what decides, so a second replica (or a restart overlapping a tick) does not
// run the same occurrence.
func TestTwoSchedulersFireOneDueJobOnce(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC)
	job := state.CronJob{ID: "job-shared", Ag: "main", Sched: "every 1h", Prompt: "x", Enabled: true}
	if err := ApplySchedule(&job, now.Add(-2*time.Hour)); err != nil {
		t.Fatalf("ApplySchedule: %v", err)
	}
	if err := store.InsertJob(ctx, job); err != nil {
		t.Fatalf("InsertJob: %v", err)
	}

	rec := &recorder{output: "x"}
	deps := SchedulerDeps{RunPrompt: rec.run, Now: func() time.Time { return now }}
	s1 := &Scheduler{Store: store, AgentID: "main", Deps: deps}
	s2 := &Scheduler{Store: store, AgentID: "main", Deps: deps}
	s1.RunDue(ctx)
	waitIdle(t, s1)
	s2.RunDue(ctx)
	waitIdle(t, s2)

	if prompts, _, _ := rec.snapshot(); len(prompts) != 1 {
		t.Fatalf("the same due job fired %d times", len(prompts))
	}
	saved, _ := store.GetJob(ctx, "job-shared")
	if saved.RunCount != 1 {
		t.Fatalf("run_count = %d, want the single fire", saved.RunCount)
	}
}

// The claim itself is one-winner: a second caller moving the same due value
// loses, which is what a second replica experiences.
func TestClaimDueJobLetsOnlyOneProcessWin(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC)
	job := state.CronJob{ID: "job-cas", Ag: "main", Sched: "every 1h", Prompt: "x", Enabled: true}
	if err := ApplySchedule(&job, now.Add(-2*time.Hour)); err != nil {
		t.Fatalf("ApplySchedule: %v", err)
	}
	if err := store.InsertJob(ctx, job); err != nil {
		t.Fatalf("InsertJob: %v", err)
	}
	due := *job.NextRunAt
	next := now.Add(time.Hour).Unix()

	won1, err := store.ClaimDueJob(ctx, job, due, &next)
	if err != nil || !won1 {
		t.Fatalf("first claim = %v (%v), want the winner", won1, err)
	}
	won2, err := store.ClaimDueJob(ctx, job, due, &next)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if won2 {
		t.Fatal("a second claim on the same due value must lose")
	}
	saved, _ := store.GetJob(ctx, "job-cas")
	if saved.RunCount != 1 || saved.NextRunAt == nil || *saved.NextRunAt != next {
		t.Fatalf("job after the single claim = %#v", saved)
	}
}

// A heartbeat cleared while its turn runs is not resurrected by the re-anchor.
func TestHeartbeatClearedWhileItRunsIsNotResurrected(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC)
	if err := state.NewSessionStore(store.DB, "main").Ensure(ctx, "s-clear", "s-clear"); err != nil {
		t.Fatalf("ensure session: %v", err)
	}
	hb := state.Heartbeat{SessionID: "s-clear", IntervalSec: 300, Prompt: "x"}
	if err := ApplyHeartbeatInterval(&hb, now.Add(-10*time.Minute), false); err != nil {
		t.Fatalf("ApplyHeartbeatInterval: %v", err)
	}
	_ = store.SaveHeartbeat(ctx, hb)

	s := &Scheduler{Store: store, AgentID: "main", Deps: SchedulerDeps{
		RunPrompt: func(ctx context.Context, _, _, _ string) (string, string) {
			_ = store.DeleteHeartbeat(ctx, "s-clear")
			return "", ""
		},
		SessionBusy: func(string) bool { return false },
		Now:         func() time.Time { return now },
	}}
	s.RunDue(ctx)
	waitIdle(t, s)

	got, _ := store.GetHeartbeat(ctx, "s-clear")
	if got != nil {
		t.Fatalf("a heartbeat cleared while it ran was resurrected: %#v", got)
	}
}

// Only the bound tenant's heartbeats fire: another primary agent's session is
// not this scheduler's to beat, and its timer is left untouched.
func TestSchedulerDoesNotFireAnotherAgentsHeartbeat(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC)
	if err := state.NewSessionStore(store.DB, "other").Ensure(ctx, "s-other", "s-other"); err != nil {
		t.Fatalf("ensure session: %v", err)
	}
	other := state.Heartbeat{SessionID: "s-other", IntervalSec: 300, Prompt: "not yours"}
	if err := ApplyHeartbeatInterval(&other, now.Add(-10*time.Minute), false); err != nil {
		t.Fatalf("ApplyHeartbeatInterval: %v", err)
	}
	_ = store.SaveHeartbeat(ctx, other)

	rec := &recorder{output: "x"}
	s := &Scheduler{Store: store, AgentID: "main", Deps: SchedulerDeps{
		RunPrompt: rec.run, SessionBusy: func(string) bool { return false }, Now: func() time.Time { return now },
	}}
	s.RunDue(ctx)
	waitIdle(t, s)

	if prompts, _, _ := rec.snapshot(); len(prompts) != 0 {
		t.Fatalf("another agent's heartbeat fired: %#v", prompts)
	}
	got, _ := store.GetHeartbeat(ctx, "s-other")
	if got == nil || got.NextRunAt == nil || *got.NextRunAt != now.Add(-5*time.Minute).Unix() {
		t.Fatalf("another agent's beat must be left untouched: %#v", got)
	}
}

// waitIdle waits for the goroutines a pass started to finish.
func waitIdle(t *testing.T, s *Scheduler) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		n := len(s.running)
		s.mu.Unlock()
		if n == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("scheduler still has work in flight")
}
