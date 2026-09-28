package turn

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

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
	// RunPrompt executes prompt as one agent turn in the named session and
	// returns the assistant's final text. A non-empty error string is the
	// runtime's own failure text.
	RunPrompt func(ctx context.Context, sessionID, channelID, prompt string) (output string, errText string)
	// DeliverText sends a finished job's answer to a channel. Nil means this
	// deployment has no channels, which makes every job local-only.
	DeliverText func(ctx context.Context, channelID, sessionID, text string) error
	// SessionBusy reports whether a session has a turn in flight. A heartbeat
	// never interrupts one.
	SessionBusy func(sessionID string) bool
	// Now is the clock, injectable so a test can drive the scheduler without
	// waiting for wall time.
	Now func() time.Time
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

// RunDue fires everything whose time has arrived. It is exported so a surface
// (and a test) can drive one pass without waiting for the ticker.
func (s *Scheduler) RunDue(ctx context.Context) {
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
		if !s.claim(job.ID) {
			// The previous fire of this job is still going. Skipping keeps a
			// slow job from stacking copies of itself.
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
	if !s.claim(job.ID) {
		return fmt.Errorf("cron job %q is already running", jobID)
	}
	go func() {
		defer s.release(job.ID)
		s.fire(ctx, *job, "manual")
	}()
	return nil
}

// fire runs one job and records what happened.
//
// The run gets a session of its own, named after the job and the moment: a job
// must not inherit the state of its previous fire, or a daily report would
// slowly turn into one very long conversation.
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
		s.logger().Error("cron: open run record", "job", job.ID, "err", err)
	}

	output, errText := "", ""
	if s.Deps.RunPrompt == nil {
		errText = "no runtime is bound to the scheduler"
	} else {
		output, errText = s.Deps.RunPrompt(ctx, sessionID, cronChannelID, job.Prompt)
	}

	status := state.CronStatusOK
	deliveredTo := ""
	if strings.TrimSpace(errText) != "" {
		status = state.CronStatusFailed
	} else if target := strings.TrimSpace(job.Deliver); target != "" {
		if s.Deps.DeliverText == nil {
			status = state.CronStatusDeliveryError
			errText = "no delivery channel is bound to the scheduler"
		} else if derr := s.Deps.DeliverText(ctx, target, sessionID, output); derr != nil {
			// The work succeeded and only the delivery failed. That is its own
			// status because it is a different thing to fix, and it must not
			// count against the job's failure streak.
			status = state.CronStatusDeliveryError
			errText = derr.Error()
		} else {
			deliveredTo = target
		}
	}

	if runRowID > 0 {
		if ferr := s.Store.FinishRun(ctx, runRowID, status, output, errText, deliveredTo); ferr != nil {
			s.logger().Error("cron: close run record", "job", job.ID, "err", ferr)
		}
	}
	s.recordOutcome(ctx, job, trigger, status, output, errText)
}

// cronChannelID labels the turn's origin. It is not a delivery target: where a
// job's answer goes is the job's own Deliver field.
const cronChannelID = "cron"

// recordOutcome writes the job's own summary of the fire. The schedule position
// (next_run_at, run_count) is not touched: it was claimed before the fire, and
// rewriting it here is what used to undo a pause or an edit made while the job
// ran — and what resurrected a job deleted mid-fire.
func (s *Scheduler) recordOutcome(ctx context.Context, job state.CronJob, trigger, status, output, errText string) {
	if err := s.Store.RecordOutcome(ctx, job.ID, status, output, errText, s.Deps.now().Unix()); err != nil {
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

// runDueHeartbeats fires the recurring instruction of every idle session whose
// interval has elapsed.
//
// Two rules from the feature's own definition are enforced here rather than in
// the store: a heartbeat never interrupts a running turn, and a session that
// was busy across several intervals gets one beat when it goes quiet, not a
// backlog of them. Both fall out of re-anchoring the timer on every pass.
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
		if s.Deps.SessionBusy != nil && s.Deps.SessionBusy(hb.SessionID) {
			s.reanchor(ctx, hb, now, false)
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
	if s.Deps.RunPrompt == nil {
		return
	}
	// The prompt arrives as an ordinary turn in the session it belongs to, so
	// it sees that conversation's history — that is the whole point of a
	// heartbeat over a cron job.
	if _, errText := s.Deps.RunPrompt(ctx, hb.SessionID, heartbeatChannelID, hb.Prompt); errText != "" {
		s.logger().Error("heartbeat: run", "session", hb.SessionID, "err", errText)
	}
	s.reanchor(ctx, hb, s.Deps.now(), true)
}

const heartbeatChannelID = "heartbeat"

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
