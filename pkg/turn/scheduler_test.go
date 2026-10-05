package turn

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
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
	startErr   error
}

func (r *recorder) startHeartbeat(_ context.Context, sessionID, prompt string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prompts = append(r.prompts, prompt)
	r.sessions = append(r.sessions, sessionID)
	return r.startErr
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

func (r *recorder) deliveries() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.delivered...)
}

// fireStub is the StartFire/FireRunState pair the scheduler tests run on: it
// records the fires it is asked to open, and the test says how each session's
// run stands — running, parked on an approval, or ended with an ending.
type fireStub struct {
	mu       sync.Mutex
	fires    []CronFire
	startErr error
	runs     map[string]FireRun
}

func (f *fireStub) startFire(_ context.Context, fire CronFire) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fires = append(f.fires, fire)
	return f.startErr
}

func (f *fireStub) runState(_ context.Context, sessionID string) (FireRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runs[sessionID], nil
}

// setRun says how the run in sessionID stands. A run the map has nothing for
// has not ended: FireRunState reports the zero FireRun, which a settle pass
// leaves for a later one.
func (f *fireStub) setRun(sessionID string, run FireRun) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.runs == nil {
		f.runs = map[string]FireRun{}
	}
	f.runs[sessionID] = run
}

func (f *fireStub) fired() []CronFire {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]CronFire(nil), f.fires...)
}

// fireDeps wires a stub into scheduler deps around the delivery double.
func fireDeps(f *fireStub, deliver func(ctx context.Context, channelID, sessionID, text string) error, now func() time.Time) SchedulerDeps {
	return SchedulerDeps{StartFire: f.startFire, FireRunState: f.runState, DeliverText: deliver, Now: now}
}

// A due job fires in a conversation of its own, named for the job and the
// moment; when its run ends the fire is settled, the answer delivered where
// the job was told, and the job rescheduled — the whole lifecycle of one
// recurring job.
func TestSchedulerFiresDueJobInItsOwnSessionAndReschedules(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	rec := &recorder{}
	stub := &fireStub{}
	now := time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC)

	job := state.CronJob{ID: "job-1", Ag: "main", Name: "hourly", Sched: "every 1h", Prompt: "check the queue", Deliver: "telegram", Enabled: true}
	if err := ApplySchedule(&job, now.Add(-2*time.Hour)); err != nil {
		t.Fatalf("ApplySchedule: %v", err)
	}
	if err := store.InsertJob(ctx, job); err != nil {
		t.Fatalf("SaveJob: %v", err)
	}

	s := &Scheduler{Store: store, AgentID: "main", Deps: fireDeps(stub, rec.deliver, func() time.Time { return now })}
	s.RunDue(ctx)
	waitIdle(t, s)

	fires := stub.fired()
	if len(fires) != 1 {
		t.Fatalf("fires = %d, want one", len(fires))
	}
	if !strings.HasPrefix(fires[0].SessionID, "cron-job-1-") {
		t.Fatalf("a job must fire in a session of its own, got %q", fires[0].SessionID)
	}
	if fires[0].SessionID == "main" {
		t.Fatal("the fire's session must not be the agent's conversation")
	}
	if !strings.HasPrefix(fires[0].Title, "hourly") {
		t.Fatalf("the fire's title = %q, want the job's name first", fires[0].Title)
	}
	if fires[0].Job.Prompt != "check the queue" {
		t.Fatalf("the fire carries the job's prompt, got %q", fires[0].Job.Prompt)
	}

	// The run ends; the next pass settles the fire and delivers the answer.
	stub.setRun(fires[0].SessionID, FireRun{Ended: true, Ending: FireEnding{Output: "three callers"}})
	s.SettleFires(ctx)

	if delivered := rec.deliveries(); len(delivered) != 1 || delivered[0] != "telegram:three callers" {
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
	stub := &fireStub{}
	now := time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC)

	job := state.CronJob{ID: "job-once", Ag: "main", Sched: "in 1m", Prompt: "remind me", Enabled: true}
	if err := ApplySchedule(&job, now.Add(-2*time.Minute)); err != nil {
		t.Fatalf("ApplySchedule: %v", err)
	}
	if job.RepeatLimit != 1 {
		t.Fatalf("a one-shot must default to a single fire, got %d", job.RepeatLimit)
	}
	_ = store.InsertJob(ctx, job)

	s := &Scheduler{Store: store, AgentID: "main", Deps: fireDeps(stub, nil, func() time.Time { return now })}
	s.RunDue(ctx)
	waitIdle(t, s)
	fires := stub.fired()
	if len(fires) != 1 {
		t.Fatalf("fires = %d, want one", len(fires))
	}
	stub.setRun(fires[0].SessionID, FireRun{Ended: true, Ending: FireEnding{Output: "done"}})
	s.SettleFires(ctx)

	saved, _ := store.GetJob(ctx, "job-once")
	if saved.NextRunAt != nil {
		t.Fatalf("a spent one-shot must not be scheduled again: %v", *saved.NextRunAt)
	}
	if saved.RunCount != 1 || saved.LastOutput != "done" {
		t.Fatalf("job after fire = %#v", saved)
	}
}

// A failed run and a failed delivery are different problems: only the first
// counts against the streak, because the second means the work was done. Each
// failure carries a stable code for a surface to word in its own language,
// recorded on the fire and on the job's own summary.
func TestSchedulerSeparatesRunFailureFromDeliveryFailure(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC)

	failing := &fireStub{}
	job := state.CronJob{ID: "job-fail", Ag: "main", Sched: "every 1h", Prompt: "x", Enabled: true}
	_ = ApplySchedule(&job, now.Add(-2*time.Hour))
	_ = store.InsertJob(ctx, job)
	s := &Scheduler{Store: store, AgentID: "main", Deps: fireDeps(failing, nil, func() time.Time { return now })}
	s.RunDue(ctx)
	waitIdle(t, s)
	fires := failing.fired()
	if len(fires) != 1 {
		t.Fatalf("fires = %d, want one", len(fires))
	}
	failing.setRun(fires[0].SessionID, FireRun{Ended: true, Ending: FireEnding{ErrCode: "run_failed", ErrText: "provider refused"}})
	s.SettleFires(ctx)
	saved, _ := store.GetJob(ctx, "job-fail")
	if saved.LastStatus != state.CronStatusFailed || saved.FailureStreak != 1 {
		t.Fatalf("failed run = %#v", saved)
	}
	if saved.LastErrorCode != "run_failed" {
		t.Fatalf("the job's summary must carry the code, got %q", saved.LastErrorCode)
	}
	runs, _ := store.ListRuns(ctx, "job-fail", 10)
	if len(runs) != 1 || runs[0].Status != state.CronStatusFailed || runs[0].ErrorCode != "run_failed" {
		t.Fatalf("failed fire record = %#v", runs)
	}

	// The work succeeded and only the delivery failed.
	undeliverable := &recorder{deliverErr: errDelivery{}}
	undelivered := &fireStub{}
	job2 := state.CronJob{ID: "job-undeliverable", Ag: "main", Sched: "every 1h", Prompt: "x", Deliver: "slack", Enabled: true}
	_ = ApplySchedule(&job2, now.Add(-2*time.Hour))
	_ = store.InsertJob(ctx, job2)
	s2 := &Scheduler{Store: store, AgentID: "main", Deps: fireDeps(undelivered, undeliverable.deliver, func() time.Time { return now })}
	s2.RunDue(ctx)
	waitIdle(t, s2)
	fires2 := undelivered.fired()
	if len(fires2) != 1 {
		t.Fatalf("fires = %d, want one", len(fires2))
	}
	undelivered.setRun(fires2[0].SessionID, FireRun{Ended: true, Ending: FireEnding{Output: "ok"}})
	s2.SettleFires(ctx)
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
	if saved2.LastErrorCode != "delivery_failed" {
		t.Fatalf("the job's summary must carry the delivery code, got %q", saved2.LastErrorCode)
	}
	runs2, _ := store.ListRuns(ctx, "job-undeliverable", 10)
	if len(runs2) != 1 || runs2[0].Status != state.CronStatusDeliveryError || runs2[0].ErrorCode != "delivery_failed" || runs2[0].Error != "channel unreachable" {
		t.Fatalf("undelivered fire record = %#v", runs2)
	}

	// A job told to deliver where no delivery channel is bound at all records
	// its own code, not the delivery-failed one.
	channelless := &fireStub{}
	job3 := state.CronJob{ID: "job-channelless", Ag: "main", Sched: "every 1h", Prompt: "x", Deliver: "telegram", Enabled: true}
	_ = ApplySchedule(&job3, now.Add(-2*time.Hour))
	_ = store.InsertJob(ctx, job3)
	s3 := &Scheduler{Store: store, AgentID: "main", Deps: fireDeps(channelless, nil, func() time.Time { return now })}

	s3.RunDue(ctx)
	waitIdle(t, s3)
	fires3 := channelless.fired()
	if len(fires3) != 1 {
		t.Fatalf("fires = %d, want one", len(fires3))
	}
	channelless.setRun(fires3[0].SessionID, FireRun{Ended: true, Ending: FireEnding{Output: "ok"}})
	s3.SettleFires(ctx)
	runs3, _ := store.ListRuns(ctx, "job-channelless", 10)
	if len(runs3) != 1 || runs3[0].Status != state.CronStatusDeliveryError || runs3[0].ErrorCode != "no_delivery_channel" {
		t.Fatalf("channelless fire record = %#v", runs3)
	}
	saved3, _ := store.GetJob(ctx, "job-channelless")
	if saved3.LastErrorCode != "no_delivery_channel" {
		t.Fatalf("the job's summary must carry the no-channel code, got %q", saved3.LastErrorCode)
	}
}

type errDelivery struct{}

func (errDelivery) Error() string { return "channel unreachable" }

// Only the bound tenant's jobs run: another primary agent's job is not this
// scheduler's to fire.
func TestSchedulerOnlyFiresItsOwnAgentsJobs(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	stub := &fireStub{}
	now := time.Now()

	other := state.CronJob{ID: "job-other", Ag: "research", Sched: "every 1h", Prompt: "not yours", Enabled: true}
	_ = ApplySchedule(&other, now.Add(-2*time.Hour))
	_ = store.InsertJob(ctx, other)

	s := &Scheduler{Store: store, AgentID: "main", Deps: fireDeps(stub, nil, func() time.Time { return now })}
	s.RunDue(ctx)
	waitIdle(t, s)

	if fires := stub.fired(); len(fires) != 0 {
		t.Fatalf("another agent's job was fired: %#v", fires)
	}
}

// A heartbeat fires into its own session as a turn, and never while that
// session is busy.
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

	// Busy: the starter reports state.ErrSessionBusy, so this beat is skipped.
	busy := &recorder{startErr: fmt.Errorf("x: %w", state.ErrSessionBusy)}
	sBusy := &Scheduler{Store: store, AgentID: "main", Deps: SchedulerDeps{
		StartHeartbeat: busy.startHeartbeat, Now: func() time.Time { return now },
	}}
	sBusy.RunDue(ctx)
	waitIdle(t, sBusy)
	if prompts, _, _ := busy.snapshot(); len(prompts) != 1 {
		t.Fatalf("the starter owns the busy rule and must be asked: %#v", prompts)
	}
	afterSkip, _ := store.GetHeartbeat(ctx, "s-1")
	if afterSkip.NextRunAt == nil || *afterSkip.NextRunAt != now.Add(5*time.Minute).Unix() {
		t.Fatalf("a skipped beat must re-anchor rather than pile up: %v", afterSkip.NextRunAt)
	}
	if afterSkip.LastFiredAt != nil {
		t.Fatal("a skipped beat did not fire and must not be recorded as having fired")
	}

	// Idle: the beat becomes a turn of its own session.
	idle := &recorder{}
	later := now.Add(6 * time.Minute)
	sIdle := &Scheduler{Store: store, AgentID: "main", Deps: SchedulerDeps{
		StartHeartbeat: idle.startHeartbeat, Now: func() time.Time { return later },
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

	// Any other failure is a real failure, not a fire.
	broken := &recorder{startErr: errors.New("gateway is down")}
	failing := later.Add(6 * time.Minute)
	sFailing := &Scheduler{Store: store, AgentID: "main", Deps: SchedulerDeps{
		StartHeartbeat: broken.startHeartbeat, Now: func() time.Time { return failing },
	}}
	sFailing.RunDue(ctx)
	waitIdle(t, sFailing)
	afterFailure, _ := store.GetHeartbeat(ctx, "s-1")
	if afterFailure.LastFiredAt == nil || *afterFailure.LastFiredAt != later.Unix() {
		t.Fatalf("a failed start must not record a fire: %#v", afterFailure.LastFiredAt)
	}
	if afterFailure.NextRunAt == nil || *afterFailure.NextRunAt != failing.Add(5*time.Minute).Unix() {
		t.Fatalf("a failed start must still re-anchor: %v", afterFailure.NextRunAt)
	}
}

// A pause the user makes while a job's fire is running must survive the job's
// own outcome write: the result still lands, but the schedule stays as the
// user left it rather than being restored from the pre-fire snapshot.
func TestSchedulerKeepsAPauseMadeWhileAJobRuns(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	stub := &fireStub{}
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
		StartFire: func(ctx context.Context, fire CronFire) error {
			if err := stub.startFire(ctx, fire); err != nil {
				return err
			}
			cur, gerr := store.GetJob(ctx, "job-pause")
			if gerr == nil && cur != nil {
				cur.Enabled = false
				cur.NextRunAt = nil
				pauseErr = store.UpdateJobConfig(ctx, *cur)
			}
			return nil
		},
		FireRunState: stub.runState,
		Now:          func() time.Time { return now },
	}}
	s.RunDue(ctx)
	waitIdle(t, s)
	fires := stub.fired()
	if len(fires) != 1 {
		t.Fatalf("fires = %d, want one", len(fires))
	}
	stub.setRun(fires[0].SessionID, FireRun{Ended: true, Ending: FireEnding{Output: "done"}})
	s.SettleFires(ctx)

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

// A job deleted while its fire runs stays deleted: the outcome write must not
// recreate it from the pre-fire snapshot, and the fire still closes — with
// nothing delivered, for a job that no longer says where its answers go.
func TestSchedulerDoesNotResurrectAJobDeletedWhileItRuns(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	stub := &fireStub{}
	rec := &recorder{}
	now := time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC)
	job := state.CronJob{ID: "job-del", Ag: "main", Sched: "every 1h", Prompt: "x", Deliver: "telegram", Enabled: true}
	if err := ApplySchedule(&job, now.Add(-2*time.Hour)); err != nil {
		t.Fatalf("ApplySchedule: %v", err)
	}
	if err := store.InsertJob(ctx, job); err != nil {
		t.Fatalf("InsertJob: %v", err)
	}

	s := &Scheduler{Store: store, AgentID: "main", Deps: SchedulerDeps{
		StartFire: func(ctx context.Context, fire CronFire) error {
			if err := stub.startFire(ctx, fire); err != nil {
				return err
			}
			_ = store.DeleteJob(ctx, "job-del")
			return nil
		},
		FireRunState: stub.runState,
		DeliverText:  rec.deliver,
		Now:          func() time.Time { return now },
	}}
	s.RunDue(ctx)
	waitIdle(t, s)
	fires := stub.fired()
	if len(fires) != 1 {
		t.Fatalf("fires = %d, want one", len(fires))
	}
	stub.setRun(fires[0].SessionID, FireRun{Ended: true, Ending: FireEnding{Output: "done"}})
	s.SettleFires(ctx)

	saved, _ := store.GetJob(ctx, "job-del")
	if saved != nil {
		t.Fatalf("a job deleted while it ran was resurrected: %#v", saved)
	}
	if delivered := rec.deliveries(); len(delivered) != 0 {
		t.Fatalf("a deleted job's fire must not deliver: %#v", delivered)
	}
	runs, _ := store.ListRuns(ctx, "job-del", 10)
	if len(runs) != 1 || runs[0].Status != state.CronStatusOK || runs[0].Output != "done" {
		t.Fatalf("the fire of a deleted job must still close with its ending: %#v", runs)
	}
}

// Two schedulers reading the same due job fire it once: the schedule CAS is
// what decides, so a second replica (or a restart overlapping a tick) does not
// run the same occurrence.
func TestTwoSchedulersFireOneDueJobOnce(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	stub := &fireStub{}
	now := time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC)
	job := state.CronJob{ID: "job-shared", Ag: "main", Sched: "every 1h", Prompt: "x", Enabled: true}
	if err := ApplySchedule(&job, now.Add(-2*time.Hour)); err != nil {
		t.Fatalf("ApplySchedule: %v", err)
	}
	if err := store.InsertJob(ctx, job); err != nil {
		t.Fatalf("InsertJob: %v", err)
	}

	deps := fireDeps(stub, nil, func() time.Time { return now })
	s1 := &Scheduler{Store: store, AgentID: "main", Deps: deps}
	s2 := &Scheduler{Store: store, AgentID: "main", Deps: deps}
	s1.RunDue(ctx)
	waitIdle(t, s1)
	s2.RunDue(ctx)
	waitIdle(t, s2)

	if fires := stub.fired(); len(fires) != 1 {
		t.Fatalf("the same due job fired %d times", len(fires))
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
		StartHeartbeat: func(ctx context.Context, sessionID, _ string) error {
			_ = store.DeleteHeartbeat(ctx, sessionID)
			return nil
		},
		Now: func() time.Time { return now },
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
		StartHeartbeat: rec.startHeartbeat, Now: func() time.Time { return now },
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

// Settling is idempotent: the record's compare-and-swap decides who closes,
// so the same ending arriving three times — the bus fast path, a tick, a
// replica — delivers exactly once.
func TestSettleFiresDeliversOnceHoweverOftenItRuns(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	stub := &fireStub{}
	rec := &recorder{}
	now := time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC)
	job := state.CronJob{ID: "job-1", Ag: "main", Sched: "every 1h", Prompt: "x", Deliver: "telegram", Enabled: true}
	_ = ApplySchedule(&job, now.Add(-2*time.Hour))
	_ = store.InsertJob(ctx, job)

	s := &Scheduler{Store: store, AgentID: "main", Deps: fireDeps(stub, rec.deliver, func() time.Time { return now })}
	s.RunDue(ctx)
	waitIdle(t, s)
	fires := stub.fired()
	if len(fires) != 1 {
		t.Fatalf("fires = %d, want one", len(fires))
	}
	ending := FireRun{Ended: true, Ending: FireEnding{Output: "one answer"}}
	stub.setRun(fires[0].SessionID, ending)
	s.SettleFires(ctx)
	s.SettleFires(ctx, fires[0].SessionID)
	s.SettleFires(ctx)

	if delivered := rec.deliveries(); len(delivered) != 1 || delivered[0] != "telegram:one answer" {
		t.Fatalf("delivered = %#v, want the single delivery", delivered)
	}
	runs, _ := store.ListRuns(ctx, "job-1", 10)
	if len(runs) != 1 || runs[0].Status != state.CronStatusOK || runs[0].Output != "one answer" {
		t.Fatalf("run record after three settles = %#v", runs)
	}
}

// A job whose fire's run is still going — running, or parked on an approval —
// does not fire again: the occurrence stays due rather than stacking, "run it
// now" is refused, and the pass after the run ends fires exactly once more.
func TestSchedulerHoldsAJobWhileItsFireRuns(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	stub := &fireStub{}
	now := time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC)
	clock := now
	job := state.CronJob{ID: "job-1", Ag: "main", Sched: "every 1h", Prompt: "x", Enabled: true}
	_ = ApplySchedule(&job, now.Add(-2*time.Hour))
	_ = store.InsertJob(ctx, job)

	s := &Scheduler{Store: store, AgentID: "main", Deps: fireDeps(stub, nil, func() time.Time { return clock })}
	s.RunDue(ctx)
	waitIdle(t, s)
	if fires := stub.fired(); len(fires) != 1 {
		t.Fatalf("fires = %d, want the first fire", len(fires))
	}
	session := stub.fired()[0].SessionID

	// The run is alive — this is also what a run parked on an approval reads
	// as, which is the same fact to the scheduler.
	stub.setRun(session, FireRun{Alive: true})
	s.SettleFires(ctx)
	runs, _ := store.ListRuns(ctx, "job-1", 10)
	if len(runs) != 1 || runs[0].Status != state.CronStatusRunning {
		t.Fatalf("a live run's fire must stay open: %#v", runs)
	}

	// Due again, but the fire is still going: no second fire, and the
	// occurrence is left due rather than being skipped outright.
	clock = now.Add(2 * time.Hour)
	s.RunDue(ctx)
	waitIdle(t, s)
	if fires := stub.fired(); len(fires) != 1 {
		t.Fatalf("a job whose fire runs fired %d times", len(fires))
	}
	if err := s.RunNow(ctx, "job-1"); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("RunNow while the fire runs = %v, want already running", err)
	}

	// The run ends; the fire settles and the held-over occurrence fires once.
	stub.setRun(session, FireRun{Ended: true, Ending: FireEnding{Output: "done"}})
	s.SettleFires(ctx)
	s.RunDue(ctx)
	waitIdle(t, s)
	if fires := stub.fired(); len(fires) != 2 {
		t.Fatalf("fires after the fire ended = %d, want the single catch-up", len(fires))
	}
	// And then the schedule, not the backlog, decides again.
	s.RunDue(ctx)
	waitIdle(t, s)
	if fires := stub.fired(); len(fires) != 2 {
		t.Fatalf("fires without a backlog = %d, want no extra catch-up", len(fires))
	}
}

// A fire whose conversation could not be opened is closed right there with
// the failure: no run was created, so no ending event will ever arrive. The
// job is not spent — the next pass fires it again.
func TestAStartFireFailureSettlesTheFireAndTheJobFiresAgain(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	stub := &fireStub{startErr: errors.New("no turn runtime")}
	now := time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC)
	clock := now
	job := state.CronJob{ID: "job-1", Ag: "main", Sched: "every 1h", Prompt: "x", Enabled: true}
	_ = ApplySchedule(&job, now.Add(-2*time.Hour))
	_ = store.InsertJob(ctx, job)

	s := &Scheduler{Store: store, AgentID: "main", Deps: fireDeps(stub, nil, func() time.Time { return clock })}
	s.RunDue(ctx)
	waitIdle(t, s)
	runs, _ := store.ListRuns(ctx, "job-1", 10)
	if len(runs) != 1 || runs[0].Status != state.CronStatusFailed {
		t.Fatalf("a fire that could not start must be closed failed: %#v", runs)
	}
	if runs[0].ErrorCode != "fire_start_failed" || runs[0].Error != "no turn runtime" {
		t.Fatalf("the closed fire carries its own code and reason: %#v", runs[0])
	}
	saved, _ := store.GetJob(ctx, "job-1")
	if saved.LastStatus != state.CronStatusFailed || saved.LastErrorCode != "fire_start_failed" {
		t.Fatalf("the job's summary after a failed start = %#v", saved)
	}

	// The failure did not spend the job: with the start working again, the
	// next pass fires once.
	stub.startErr = nil
	clock = now.Add(2 * time.Hour)
	s.RunDue(ctx)
	waitIdle(t, s)
	if fires := stub.fired(); len(fires) != 2 {
		t.Fatalf("fires after recovery = %d, want the one new fire", len(fires))
	}
}

// A scheduler settles only its own tenant's fires: another primary agent's
// open record is left for that agent's scheduler, which is the only place its
// job's answers are configured.
func TestSettleFiresLeavesAnotherTenantsFiresAlone(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	stub := &fireStub{}
	now := time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC)

	other := state.CronJob{ID: "job-other", Ag: "research", Sched: "every 1h", Prompt: "not yours", Deliver: "slack", Enabled: true}
	_ = ApplySchedule(&other, now.Add(-2*time.Hour))
	_ = store.InsertJob(ctx, other)
	if _, err := store.StartRun(ctx, state.CronRun{JobID: "job-other", AgentID: "research", SessionID: "cron-job-other-1", Trigger: "schedule", StartedAt: now.Unix()}); err != nil {
		t.Fatal(err)
	}
	stub.setRun("cron-job-other-1", FireRun{Ended: true, Ending: FireEnding{Output: "not yours"}})

	rec := &recorder{}
	s := &Scheduler{Store: store, AgentID: "main", Deps: fireDeps(stub, rec.deliver, func() time.Time { return now })}
	s.SettleFires(ctx)
	// Even named by session, another tenant's fire is not this scheduler's.
	s.SettleFires(ctx, "cron-job-other-1")

	open, err := store.OpenFireForSession(ctx, "cron-job-other-1")
	if err != nil || open == nil {
		t.Fatalf("another tenant's fire was settled from under it: %v (%v)", open, err)
	}
	if delivered := rec.deliveries(); len(delivered) != 0 {
		t.Fatalf("another tenant's answer was delivered: %#v", delivered)
	}
}

// NotifyFireParked tells the job's delivery target that its fire is waiting
// for an approval; a job with nowhere to deliver hears nothing, the
// conversation itself shows the approval.
func TestNotifyFireParkedTellsTheDeliveryTarget(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	stub := &fireStub{}
	rec := &recorder{}
	now := time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC)
	job := state.CronJob{ID: "job-1", Ag: "main", Sched: "every 1h", Prompt: "x", Deliver: "telegram", Enabled: true}
	_ = ApplySchedule(&job, now.Add(-2*time.Hour))
	_ = store.InsertJob(ctx, job)
	quiet := state.CronJob{ID: "job-quiet", Ag: "main", Sched: "every 1h", Prompt: "y", Enabled: true}
	_ = ApplySchedule(&quiet, now.Add(-2*time.Hour))
	_ = store.InsertJob(ctx, quiet)

	s := &Scheduler{Store: store, AgentID: "main", Deps: fireDeps(stub, rec.deliver, func() time.Time { return now })}
	s.RunDue(ctx)
	waitIdle(t, s)
	fires := stub.fired()
	if len(fires) != 2 {
		t.Fatalf("fires = %d, want both jobs", len(fires))
	}

	notice := "Waiting for approval to run a tool. Approve it in the Forebrain Harness app to continue."
	for _, fire := range fires {
		s.NotifyFireParked(ctx, fire.SessionID, notice)
	}
	delivered := rec.deliveries()
	if len(delivered) != 1 || delivered[0] != "telegram:"+notice {
		t.Fatalf("delivered = %#v, want the parked notice on the one configured target", delivered)
	}
}

// FireEndingFromRunEvent is the one place a run's end becomes a fire's
// ending: the three ways a run ends, and nothing else.
func TestFireEndingFromRunEventMapsTheThreeEndings(t *testing.T) {
	mk := func(kind string, payload any) event.RunEvent {
		return event.NewRunEvent("", "r-1", "cron-job-1-1", kind, payload, time.Now())
	}
	end, ok := FireEndingFromRunEvent(mk(event.RunEventTurnCompleted, event.TurnCompletedPayload{Text: "the answer"}))
	if !ok || end.Output != "the answer" || end.ErrCode != "" || end.ErrText != "" {
		t.Fatalf("completed = %+v %v", end, ok)
	}

	end, ok = FireEndingFromRunEvent(mk(event.RunEventTurnError, event.TurnErrorPayload{
		Error: "fallback text", Message: "the message", Detail: &event.TurnErrorDetail{Code: "rate_limited"},
	}))
	if !ok || end.ErrCode != "rate_limited" || end.ErrText != "the message" {
		t.Fatalf("error with detail = %+v %v", end, ok)
	}
	end, ok = FireEndingFromRunEvent(mk(event.RunEventTurnError, event.TurnErrorPayload{Error: "fallback text"}))
	if !ok || end.ErrCode != "run_failed" || end.ErrText != "fallback text" {
		t.Fatalf("error without detail = %+v %v", end, ok)
	}
	end, ok = FireEndingFromRunEvent(mk(event.RunEventTurnError, event.TurnErrorPayload{}))
	if !ok || end.ErrCode != "run_failed" || end.ErrText != "" {
		t.Fatalf("error with nothing to say = %+v %v", end, ok)
	}

	end, ok = FireEndingFromRunEvent(mk(event.RunEventTurnCancelled, event.TurnCancelledPayload{}))
	if !ok || end.ErrCode != "run_stopped" || end.ErrText != "The run was stopped before it finished." {
		t.Fatalf("cancelled = %+v %v", end, ok)
	}

	if _, ok := FireEndingFromRunEvent(mk(event.RunEventTurnStarted, event.TurnStartedPayload{})); ok {
		t.Fatal("a turn_started event is not an ending")
	}
	if _, ok := FireEndingFromRunEvent(mk(event.RunEventSubagentEnded, nil)); ok {
		t.Fatal("a subagent's end is not a run's end")
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

// TestRunDueMaintainsAfterSettleFires pins the order of one pass: settling
// closes what has ended first, then maintenance runs — it reads the state
// settling leaves behind, so it must never see the pass half-done.
func TestRunDueMaintainsAfterSettleFires(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	if err := store.InsertJob(ctx, state.CronJob{ID: "job-maint", Ag: "main", Sched: "every 1h"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StartRun(ctx, state.CronRun{JobID: "job-maint", AgentID: "main", SessionID: "s-maint", Trigger: "schedule"}); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var order []string
	s := &Scheduler{
		Store:   store,
		AgentID: "main",
		Deps: SchedulerDeps{
			FireRunState: func(context.Context, string) (FireRun, error) {
				mu.Lock()
				order = append(order, "settle")
				mu.Unlock()
				return FireRun{Ended: true, Ending: FireEnding{Output: "done"}}, nil
			},
			Maintain: func(context.Context, time.Time) {
				mu.Lock()
				order = append(order, "maintain")
				mu.Unlock()
			},
		},
	}
	s.RunDue(ctx)

	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != "settle" || order[1] != "maintain" {
		t.Fatalf("order = %v, want settle then maintain", order)
	}
}
