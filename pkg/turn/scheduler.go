package turn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

// DefaultSchedulerTick is how often the scheduler looks for work. The floor on a
// recurring schedule is a minute, so this is fine enough that a job fires
// within half a tick of its time without the loop becoming a poller.
const DefaultSchedulerTick = 30 * time.Second

// Deps are the seams the scheduler needs from the runtime above it. They are
// functions rather than interfaces on concrete types so this package stays
// below the orchestration layers: it decides when work runs and what happens to
// the answer, and knows nothing about how a turn is executed.
type SchedulerDeps struct {
	// StartFire opens one fire as a conversation of its own and returns as
	// soon as its run has started. How the fire ends is read back through
	// FireRunState, never reported by StartFire.
	StartFire func(ctx context.Context, fire CronFire) error
	// FireRunState reports the run of the fire that ran in sessionID: still
	// alive, or ended and how.
	FireRunState func(ctx context.Context, sessionID string) (FireRun, error)
	// DeliverText sends a finished job's answer to a channel. Nil means this
	// deployment has no channels, which makes every job local-only.
	DeliverText func(ctx context.Context, channelID, sessionID, text string) error
	// StartHeartbeat starts a heartbeat as a turn of its session and returns
	// once the turn has started. A session that is busy, or parked on an
	// approval, is reported as an error wrapping state.ErrSessionBusy.
	StartHeartbeat func(ctx context.Context, sessionID, prompt string) error
	// Maintain is the standing maintenance pass — today, deleting what the
	// retention has expired. Nil means the runtime has no maintenance to run.
	// It runs after settling, so what it reads has already been closed.
	Maintain func(ctx context.Context, now time.Time)
	// Now is the clock, injectable so a test can drive the scheduler without
	// waiting for wall time.
	Now func() time.Time
}

// CronFire is one fire of a job: the session it runs in, named for the job
// and the moment, and the job as it was when it fired.
type CronFire struct {
	SessionID string
	Title     string
	Job       state.CronJob
}

// FireEnding is how a fire's run ended: its answer, or the reason it gave
// none, already explained for a person to read.
type FireEnding struct {
	Output string
	// ErrCode classifies the failure for a surface to word in its own
	// language; ErrText is the English sentence kept as the fallback.
	ErrCode string
	ErrText string
}

// FireRun is where a fire's run stands. Alive covers a run parked on an
// approval: it has not ended, and the fire is not over.
type FireRun struct {
	Alive  bool
	Ended  bool
	Ending FireEnding
}

// FireEndingFromRunEvent reads how a run ended from its ending event. It
// returns false for any event that is not a run's end.
func FireEndingFromRunEvent(evt event.RunEvent) (FireEnding, bool) {
	switch evt.Type {
	case event.RunEventTurnCompleted:
		var payload event.TurnCompletedPayload
		if err := json.Unmarshal(evt.Payload, &payload); err != nil {
			return FireEnding{}, false
		}
		return FireEnding{Output: payload.Text}, true
	case event.RunEventTurnError:
		var payload event.TurnErrorPayload
		if err := json.Unmarshal(evt.Payload, &payload); err != nil {
			return FireEnding{}, false
		}
		ending := FireEnding{ErrCode: "run_failed"}
		ending.ErrText = payload.Message
		if strings.TrimSpace(ending.ErrText) == "" {
			ending.ErrText = payload.Error
		}
		if payload.Detail != nil {
			ending.ErrCode = payload.Detail.Code
		}
		return ending, true
	case event.RunEventTurnCancelled:
		return FireEnding{ErrCode: "run_stopped", ErrText: "The run was stopped before it finished."}, true
	default:
		return FireEnding{}, false
	}
}

func (d SchedulerDeps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// Scheduler runs one primary agent's standing work. It is bound to a tenant:
// switching the active primary agent stops this scheduler and starts another,
// the same way the channel registry is rebound, so one agent's jobs never fire
// while a different agent owns the runtime.
type Scheduler struct {
	Store   *state.CronStore
	AgentID string
	Deps    SchedulerDeps
	Tick    time.Duration
	Log     *slog.Logger

	mu      sync.Mutex
	running map[string]bool
}

func (s *Scheduler) logger() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// Start runs the tick loop until the context is cancelled, and returns a stop
// function that waits for the loop to exit.
func (s *Scheduler) Start(ctx context.Context) func() {
	if s == nil || s.Store == nil || strings.TrimSpace(s.AgentID) == "" {
		return func() {}
	}
	interval := s.Tick
	if interval <= 0 {
		interval = DefaultSchedulerTick
	}
	loopCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-loopCtx.Done():
				return
			case <-ticker.C:
				s.RunDue(loopCtx)
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

// RunDue settles what has ended, runs the standing maintenance, and fires
// everything whose time has arrived. It is exported so a surface (and a test)
// can drive one pass without waiting for the ticker.
func (s *Scheduler) RunDue(ctx context.Context) {
	s.SettleFires(ctx)
	if s.Deps.Maintain != nil {
		s.Deps.Maintain(ctx, s.Deps.now())
	}
	s.runDueJobs(ctx)
	s.runDueHeartbeats(ctx)
}

func (s *Scheduler) runDueJobs(ctx context.Context) {
	now := s.Deps.now()
	jobs, err := s.Store.DueJobs(ctx, s.AgentID, now)
	if err != nil {
		s.logger().Error("cron: list due jobs", "agent", s.AgentID, "err", err)
		return
	}
	for _, job := range jobs {
		// Whether the job may fire again is read from the database, not from
		// this process: its previous fire's run must have ended. A run still
		// going — including one parked on an approval — keeps the job from
		// firing, and the occurrence stays due rather than being stacked: the
		// pass after the fire settles fires once more, never a pile of them.
		skip, err := s.fireAlive(ctx, job.ID)
		if err != nil {
			s.logger().Error("cron: read the job's open fire", "job", job.ID, "err", err)
			continue
		}
		if skip {
			continue
		}
		if !s.claim(job.ID) {
			// The previous fire of this job is still being opened here. The
			// claim covers exactly that span: from the record being written to
			// StartFire returning.
			continue
		}
		// Claim the occurrence before firing: the CAS advances next_run_at and
		// run_count only for the process that moved the value the due query
		// read, so a second scheduler fires nothing. next is computed as if
		// this fire had already counted, which is what the old outcome write
		// did once the run finished.
		claimed := job
		claimed.RunCount++
		next, ok := s.nextFire(claimed, now)
		var nextPtr *int64
		if ok {
			v := next
			nextPtr = &v
		}
		won, claimErr := s.Store.ClaimDueJob(ctx, job, *job.NextRunAt, nextPtr)
		if claimErr != nil {
			s.logger().Error("cron: claim due job", "job", job.ID, "err", claimErr)
			s.release(job.ID)
			continue
		}
		if !won {
			s.release(job.ID)
			continue
		}
		go func(j state.CronJob) {
			defer s.release(j.ID)
			s.fire(ctx, j, "schedule")
		}(job)
	}
}

// fireAlive reports whether the job's previous fire is still going, which
// keeps the job from firing again.
func (s *Scheduler) fireAlive(ctx context.Context, jobID string) (bool, error) {
	rec, err := s.Store.LatestOpenFire(ctx, jobID)
	if err != nil {
		return false, err
	}
	if rec == nil {
		return false, nil
	}
	run, err := s.Deps.FireRunState(ctx, rec.SessionID)
	if err != nil {
		return false, err
	}
	return run.Alive, nil
}

// RunNow fires a job immediately, without disturbing its schedule. This is the
// "run it and show me" path a surface offers next to a job.
func (s *Scheduler) RunNow(ctx context.Context, jobID string) error {
	if s == nil || s.Store == nil {
		return fmt.Errorf("scheduler unavailable")
	}
	job, err := s.Store.GetJob(ctx, jobID)
	if err != nil {
		return err
	}
	if job == nil {
		return fmt.Errorf("cron job %q not found", jobID)
	}
	if alive, err := s.fireAlive(ctx, job.ID); err == nil && alive {
		return fmt.Errorf("cron job %q is already running", jobID)
	}
	if !s.claim(job.ID) {
		return fmt.Errorf("cron job %q is already running", jobID)
	}
	go func() {
		defer s.release(job.ID)
		s.fire(ctx, *job, "manual")
	}()
	return nil
}

// fire opens one fire of a job and starts it, then returns: whether the fire
// ended — minutes later, or by a crash — is read back through FireRunState
// and settled by SettleFires, never reported by the fire itself.
//
// The fire's conversation gets a session of its own, named after the job and
// the moment: a job must not inherit the state of its previous fire, or a
// daily report would slowly turn into one very long conversation.
func (s *Scheduler) fire(ctx context.Context, job state.CronJob, trigger string) {
	started := s.Deps.now()
	sessionID := fmt.Sprintf("cron-%s-%d", strings.TrimSpace(job.ID), started.Unix())
	runRowID, err := s.Store.StartRun(ctx, state.CronRun{
		JobID:     job.ID,
		AgentID:   job.Ag,
		SessionID: sessionID,
		Trigger:   trigger,
		StartedAt: started.Unix(),
	})
	if err != nil {
		// The record is this fire's identity — settling finds the fire by it —
		// so a fire that cannot be recorded does not happen.
		s.logger().Error("cron: open run record", "job", job.ID, "err", err)
		return
	}
	rec := state.CronRun{
		ID: runRowID, JobID: job.ID, AgentID: job.Ag,
		SessionID: sessionID, Trigger: trigger, Status: state.CronStatusRunning, StartedAt: started.Unix(),
	}
	if s.Deps.StartFire == nil {
		s.settle(ctx, rec, FireEnding{ErrCode: "no_runtime", ErrText: "no runtime is bound to the scheduler"})
		return
	}
	err = s.Deps.StartFire(ctx, CronFire{SessionID: sessionID, Title: fireTitle(job, started), Job: job})
	if err != nil {
		// No run was created, so no ending event will ever arrive; the fire is
		// settled here or it stays open forever.
		s.logger().Error("cron: start fire", "job", job.ID, "err", err)
		s.settle(ctx, rec, FireEnding{ErrCode: "fire_start_failed", ErrText: err.Error()})
	}
}

// fireTitle is what a fire's conversation is called: the job's name — or its
// prompt, when the job was never named — and the moment it fired.
func fireTitle(job state.CronJob, started time.Time) string {
	name := strings.TrimSpace(job.Name)
	if name == "" {
		name = state.SessionTitleFromContent(job.Prompt)
	}
	return name + " · " + started.Local().Format("2006-01-02 15:04")
}

// SettleFires closes every fire of this scheduler's tenant whose run has
// ended — or only the fire that ran in sessionID, when one is named. A fire
// whose run is alive, or has not left an ending yet, is left for a later
// pass. Closing is a compare-and-swap on the record, so whichever pass gets
// there first closes it and delivers its answer; every other pass does
// nothing.
func (s *Scheduler) SettleFires(ctx context.Context, sessionID ...string) {
	var records []state.CronRun
	if len(sessionID) > 0 && strings.TrimSpace(sessionID[0]) != "" {
		rec, err := s.Store.OpenFireForSession(ctx, sessionID[0])
		if err != nil {
			s.logger().Error("cron: read open fire", "session", sessionID[0], "err", err)
			return
		}
		if rec != nil {
			records = []state.CronRun{*rec}
		}
	} else {
		var err error
		records, err = s.Store.OpenFires(ctx, s.AgentID)
		if err != nil {
			s.logger().Error("cron: list open fires", "agent", s.AgentID, "err", err)
			return
		}
	}
	for _, rec := range records {
		// Only this scheduler's tenant's fires: another primary agent's
		// scheduler settles its own.
		if rec.AgentID != s.AgentID {
			continue
		}
		run, err := s.Deps.FireRunState(ctx, rec.SessionID)
		if err != nil {
			s.logger().Error("cron: read fire run state", "session", rec.SessionID, "err", err)
			continue
		}
		if run.Alive || !run.Ended {
			continue
		}
		s.settle(ctx, rec, run.Ending)
	}
}

// settle closes one fire and delivers its answer. The close is a CAS on the
// record, so exactly one closer delivers — and a closer that loses it does
// nothing. Delivery is recorded after the close, never before it: that is
// what keeps two closers from delivering twice.
func (s *Scheduler) settle(ctx context.Context, rec state.CronRun, ending FireEnding) {
	status := state.CronStatusOK
	if strings.TrimSpace(ending.ErrText) != "" {
		status = state.CronStatusFailed
	}
	won, err := s.Store.FinishRun(ctx, rec.ID, status, ending.Output, ending.ErrText, ending.ErrCode)
	if err != nil {
		s.logger().Error("cron: close run record", "job", rec.JobID, "err", err)
		return
	}
	if !won {
		return
	}
	job, err := s.Store.GetJob(ctx, rec.JobID)
	if err != nil {
		s.logger().Error("cron: read job at settle", "job", rec.JobID, "err", err)
		return
	}
	if job == nil {
		// The job was deleted while its fire ran. The record stays readable,
		// but there is nowhere to deliver and nothing to write an outcome to.
		return
	}
	errCode, errText := ending.ErrCode, ending.ErrText
	if status == state.CronStatusOK {
		if target := strings.TrimSpace(job.Deliver); target != "" {
			if s.Deps.DeliverText == nil {
				status = state.CronStatusDeliveryError
				errCode = "no_delivery_channel"
				errText = "no delivery channel is bound to the scheduler"
			} else if derr := s.Deps.DeliverText(ctx, target, rec.SessionID, ending.Output); derr != nil {
				// The work succeeded and only the delivery failed. That is its
				// own status because it is a different thing to fix, and it
				// must not count against the job's failure streak. The
				// channel's own sentence is kept as the fallback and shown as
				// the quoted original under a localized wording.
				status = state.CronStatusDeliveryError
				errCode = "delivery_failed"
				errText = derr.Error()
			} else {
				if merr := s.Store.MarkFireDelivered(ctx, rec.ID, target); merr != nil {
					s.logger().Error("cron: mark fire delivered", "job", rec.JobID, "err", merr)
				}
			}
			if status == state.CronStatusDeliveryError {
				if merr := s.Store.MarkFireDeliveryFailed(ctx, rec.ID, errCode, errText); merr != nil {
					s.logger().Error("cron: mark fire delivery failed", "job", rec.JobID, "err", merr)
				}
			}
		}
	}
	s.recordOutcome(ctx, *job, rec.Trigger, status, ending.Output, errCode, errText)
}

// NotifyFireParked tells the job's delivery target that its fire is waiting
// for an approval in its conversation. A job with nowhere to deliver hears
// nothing; the conversation itself shows the approval.
func (s *Scheduler) NotifyFireParked(ctx context.Context, sessionID, notice string) {
	rec, err := s.Store.OpenFireForSession(ctx, sessionID)
	if err != nil {
		s.logger().Error("cron: read open fire", "session", sessionID, "err", err)
		return
	}
	if rec == nil || rec.AgentID != s.AgentID {
		return
	}
	job, err := s.Store.GetJob(ctx, rec.JobID)
	if err != nil || job == nil {
		return
	}
	target := strings.TrimSpace(job.Deliver)
	if target == "" || s.Deps.DeliverText == nil {
		return
	}
	if derr := s.Deps.DeliverText(ctx, target, rec.SessionID, notice); derr != nil {
		s.logger().Error("cron: notify fire parked", "job", rec.JobID, "err", derr)
	}
}

// recordOutcome writes the job's own summary of the fire. The schedule position
// (next_run_at, run_count) is not touched: it was claimed before the fire, and
// rewriting it here is what used to undo a pause or an edit made while the job
// ran — and what resurrected a job deleted mid-fire.
func (s *Scheduler) recordOutcome(ctx context.Context, job state.CronJob, trigger, status, output, errCode, errText string) {
	if err := s.Store.RecordOutcome(ctx, job.ID, status, output, errText, errCode, s.Deps.now().Unix()); err != nil {
		s.logger().Error("cron: record outcome", "job", job.ID, "err", err)
	}
}

func (s *Scheduler) nextFire(job state.CronJob, after time.Time) (int64, bool) {
	if job.RepeatLimit > 0 && job.RunCount >= job.RepeatLimit {
		return 0, false
	}
	sched, err := ParseSchedule(job.Sched, after)
	if err != nil {
		return 0, false
	}
	next, ok := sched.Next(after)
	if !ok {
		return 0, false
	}
	return next.Unix(), true
}

// runDueHeartbeats fires the recurring instruction of every session whose
// interval has elapsed.
//
// Whether the session can start a turn is the starter's call, not this
// loop's: it owns the same rule a page sending a message is held to (one turn
// at a time, none beside a parked approval), so there is a single source of
// truth for "busy". The interval is measured from the moment a beat starts
// (decision D2): one that found the session busy is skipped — re-anchored
// from now, not from whenever it would have ended — so beats never pile up
// behind a long turn.
func (s *Scheduler) runDueHeartbeats(ctx context.Context) {
	now := s.Deps.now()
	beats, err := s.Store.DueHeartbeats(ctx, s.AgentID, now)
	if err != nil {
		s.logger().Error("heartbeat: list due", "err", err)
		return
	}
	for _, hb := range beats {
		if hb.NextRunAt == nil {
			continue
		}
		if !s.claim("hb:" + hb.SessionID) {
			continue
		}
		// Claim the beat before firing so a second scheduler cannot fire the
		// same occurrence; a beat cleared or paused in the meantime loses the
		// CAS and is left alone.
		next := now.Add(heartbeatInterval(hb)).Unix()
		won, claimErr := s.Store.ClaimHeartbeat(ctx, hb.SessionID, *hb.NextRunAt, next)
		if claimErr != nil {
			s.logger().Error("heartbeat: claim", "session", hb.SessionID, "err", claimErr)
			s.release("hb:" + hb.SessionID)
			continue
		}
		if !won {
			s.release("hb:" + hb.SessionID)
			continue
		}
		go func(beat state.Heartbeat) {
			defer s.release("hb:" + beat.SessionID)
			s.fireHeartbeat(ctx, beat)
		}(hb)
	}
}

func (s *Scheduler) fireHeartbeat(ctx context.Context, hb state.Heartbeat) {
	if s.Deps.StartHeartbeat == nil {
		s.logger().Error("heartbeat: no runtime is bound to the scheduler", "session", hb.SessionID)
		s.reanchor(ctx, hb, s.Deps.now(), false)
		return
	}
	err := s.Deps.StartHeartbeat(ctx, hb.SessionID, hb.Prompt)
	switch {
	case err == nil:
		s.reanchor(ctx, hb, s.Deps.now(), true)
	case errors.Is(err, state.ErrSessionBusy):
		// A heartbeat never interrupts a turn: this beat is skipped, and the
		// next one comes an interval from now rather than piling up.
		s.reanchor(ctx, hb, s.Deps.now(), false)
	default:
		s.logger().Error("heartbeat: start", "session", hb.SessionID, "err", err)
		s.reanchor(ctx, hb, s.Deps.now(), false)
	}
}

// heartbeatInterval is the beat's period, floored at the shortest recurring
// schedule the feature allows.
func heartbeatInterval(hb state.Heartbeat) time.Duration {
	interval := time.Duration(hb.IntervalSec) * time.Second
	if interval < MinScheduleInterval {
		interval = MinScheduleInterval
	}
	return interval
}

// reanchor moves the beat's next fire to one interval after now and, when the
// beat actually fired, records when. The store's update only touches a beat
// that is still scheduled, so one cleared or paused during the turn is not
// resurrected.
func (s *Scheduler) reanchor(ctx context.Context, hb state.Heartbeat, now time.Time, fired bool) {
	next := now.Add(heartbeatInterval(hb)).Unix()
	var firedAt *int64
	if fired {
		v := now.Unix()
		firedAt = &v
	}
	if _, err := s.Store.ReanchorHeartbeat(ctx, hb.SessionID, next, firedAt); err != nil {
		s.logger().Error("heartbeat: reanchor", "session", hb.SessionID, "err", err)
	}
}

// claim marks a key as in flight and reports whether the caller got it.
func (s *Scheduler) claim(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running == nil {
		s.running = map[string]bool{}
	}
	if s.running[key] {
		return false
	}
	s.running[key] = true
	return true
}

func (s *Scheduler) release(key string) {
	s.mu.Lock()
	delete(s.running, key)
	s.mu.Unlock()
}

// ApplySchedule validates a job's schedule text and sets the fields derived
// from it: the repeat limit a bare schedule implies, and when it first fires.
// A disabled job keeps its text but is given no next fire, which is what
// "paused" means to the store's due query.
func ApplySchedule(job *state.CronJob, now time.Time) error {
	if job == nil {
		return fmt.Errorf("no job")
	}
	sched, err := ParseSchedule(job.Sched, now)
	if err != nil {
		return err
	}
	if job.RepeatLimit == 0 {
		job.RepeatLimit = sched.DefaultRepeatLimit()
	}
	if !job.Enabled {
		job.NextRunAt = nil
		return nil
	}
	next, ok := sched.Next(now)
	if !ok {
		job.NextRunAt = nil
		return nil
	}
	at := next.Unix()
	job.NextRunAt = &at
	return nil
}

// ApplyHeartbeatInterval validates an interval and anchors the first beat. A
// paused heartbeat is given no next fire, which is the one representation the
// due query reads.
func ApplyHeartbeatInterval(hb *state.Heartbeat, now time.Time, paused bool) error {
	if hb == nil {
		return fmt.Errorf("no heartbeat")
	}
	if strings.TrimSpace(hb.Prompt) == "" {
		return fmt.Errorf("heartbeat prompt is empty")
	}
	interval := time.Duration(hb.IntervalSec) * time.Second
	if interval < MinScheduleInterval {
		return fmt.Errorf("heartbeat interval must be at least %s", MinScheduleInterval)
	}
	if paused {
		hb.NextRunAt = nil
		return nil
	}
	next := now.Add(interval).Unix()
	hb.NextRunAt = &next
	return nil
}
