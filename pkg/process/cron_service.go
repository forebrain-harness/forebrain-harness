package process

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/channel"
	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/hook"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

// CronService is the runtime's standing-work surface: cron jobs, which fire a
// prompt in a fresh session on a schedule, and heartbeats, which fire one back
// into an existing conversation while it is idle.
//
// It lives here rather than in a surface because both surfaces need the same
// thing. The terminal and the web must agree on what a job is, when it fires,
// and what happened on its last run, so the rules are written once at the level
// that owns the runtime and both call in.
type CronService struct {
	env *Environment

	mu        sync.Mutex
	sched     *turn.Scheduler
	stop      func()
	agentID   string
	lastSweep time.Time
}

// Cron returns the standing-work service, created on first use.
func (env *Environment) Cron() *CronService {
	if env == nil {
		return nil
	}
	env.cronOnce.Do(func() { env.cron = &CronService{env: env} })
	return env.cron
}

func (c *CronService) store() *state.CronStore {
	if c == nil || c.env == nil || c.env.SQL == nil {
		return nil
	}
	return &state.CronStore{DB: c.env.SQL}
}

// ScheduledTurns is how the surface hosting the scheduler runs scheduled
// work as turns of a conversation. Only the gateway hosts one.
type ScheduledTurns struct {
	StartHeartbeat func(ctx context.Context, sessionID, prompt string) error
	StartFire      func(ctx context.Context, fire turn.CronFire) error
}

// Bind points the scheduler at one primary agent and starts it, stopping
// whatever was bound before, and hands the scheduler the surface's way of
// running a scheduled beat or a fire as a turn of its conversation.
//
// Standing work is tenant data. Rebinding on a switch is what keeps a
// switched-away agent's jobs from firing against a runtime that now belongs to
// a different agent — the same rule the channel registry follows.
//
// While holding c.mu, never wait on a goroutine that may itself take c.mu —
// the tick goroutine takes it in Maintain. All waiting happens outside the
// lock: this is the rule SettleFire and FireParked follow by copying under
// the lock and calling after it.
func (c *CronService) Bind(ctx context.Context, agentID string, turns ScheduledTurns) {
	if c == nil || c.store() == nil {
		return
	}
	id := strings.TrimSpace(agentID)
	if id == "" {
		return
	}
	c.mu.Lock()
	old := c.stop
	c.stop = nil
	next := &turn.Scheduler{
		Store:   c.store(),
		AgentID: id,
		Deps: turn.SchedulerDeps{
			StartFire:      turns.StartFire,
			FireRunState:   c.fireRunState,
			DeliverText:    c.deliver,
			StartHeartbeat: turns.StartHeartbeat,
			Maintain:       c.Maintain,
		},
	}
	c.sched = next
	c.agentID = id
	// A fresh bind sweeps on its first pass: a restart or an agent switch is
	// exactly when the install last had nobody watching the retention.
	c.lastSweep = time.Time{}
	c.stop = next.Start(ctx)
	c.mu.Unlock()
	if old != nil {
		// The old tick goroutine may be waiting on c.mu right now; waiting
		// for it to exit never happens under the lock.
		old()
	}
}

// Stop ends the scheduler loop. The jobs stay on disk; nothing fires until
// something binds again.
func (c *CronService) Stop() {
	if c == nil {
		return
	}
	c.mu.Lock()
	old := c.stop
	c.stop = nil
	c.sched = nil
	c.agentID = ""
	c.mu.Unlock()
	if old != nil {
		old()
	}
}

// AgentID is the tenant whose standing work is currently running.
func (c *CronService) AgentID() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.agentID
}

// fireRunState reads a fire's run from the shared run store: alive while it
// runs under a live owner or waits on an approval; otherwise ended, with the
// ending its run recorded. The store is the only source — a crash, another
// replica's tick, and this process's own bus hook all read the same facts.
func (c *CronService) fireRunState(ctx context.Context, sessionID string) (turn.FireRun, error) {
	runs := c.env.Deps.RunRT
	alive, err := runs.SessionHasLiveRun(ctx, sessionID)
	if err != nil {
		return turn.FireRun{}, err
	}
	if alive {
		return turn.FireRun{Alive: true}, nil
	}
	id, err := runs.FirstPrimaryRunID(ctx, sessionID)
	if err != nil {
		return turn.FireRun{}, err
	}
	if id == "" {
		// The run has not been created yet; nothing has ended.
		return turn.FireRun{}, nil
	}
	evts, err := runs.ListRunEventsOfTypes(ctx, id,
		event.RunEventTurnCompleted, event.RunEventTurnError, event.RunEventTurnCancelled)
	if err != nil {
		return turn.FireRun{}, err
	}
	if len(evts) == 0 {
		// The run is no longer alive but its ending has not been written yet;
		// the next pass reads it back.
		return turn.FireRun{}, nil
	}
	ending, ok := turn.FireEndingFromRunEvent(turn.RunEventFromRecord(evts[0]))
	if !ok {
		return turn.FireRun{}, nil
	}
	return turn.FireRun{Ended: true, Ending: ending}, nil
}

// SettleFire asks the bound scheduler to look at the fire that ran in
// sessionID right now, instead of on its next tick. With no scheduler bound
// it does nothing: the first pass after the next bind settles the fire from
// the same persisted facts.
func (c *CronService) SettleFire(ctx context.Context, sessionID string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	sched := c.sched
	c.mu.Unlock()
	if sched != nil {
		sched.SettleFires(ctx, sessionID)
	}
}

// FireParked tells the fire's job that its run is waiting for an approval in
// its conversation. With no scheduler bound it does nothing; the conversation
// itself shows the approval either way.
func (c *CronService) FireParked(ctx context.Context, sessionID, notice string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	sched := c.sched
	c.mu.Unlock()
	if sched != nil {
		sched.NotifyFireParked(ctx, sessionID, notice)
	}
}

func (c *CronService) deliver(ctx context.Context, channelID, sessionID, text string) error {
	if c == nil || c.env == nil || c.env.Channels == nil {
		return fmt.Errorf("no channels are bound")
	}
	return c.env.Channels.Deliver(ctx, channel.Outbound{
		ChannelID: strings.TrimSpace(channelID),
		SessionID: strings.TrimSpace(sessionID),
		Text:      text,
	})
}

// cronRetentionSweepEvery is how often expired fires are looked for. The
// retention is counted in days, so an hour of slack costs nothing.
const cronRetentionSweepEvery = time.Hour

// Maintain is the scheduler's maintenance pass: at most one retention sweep
// an hour, and one on the first pass after every bind — a restart or an
// agent switch is exactly when the install last had nobody watching.
func (c *CronService) Maintain(ctx context.Context, now time.Time) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if !c.lastSweep.IsZero() && now.Sub(c.lastSweep) < cronRetentionSweepEvery {
		c.mu.Unlock()
		return
	}
	c.lastSweep = now
	c.mu.Unlock()
	if err := c.pruneExpiredFires(ctx, now); err != nil {
		slog.Error("cron: retention sweep", "err", err)
	}
}

// pruneExpiredFires deletes every scheduled-task fire that has been quiet
// longer than the configured retention: the conversation with everything it
// left in the database and on disk, then its history record. Retention is a
// property of the install, so it covers every agent's fires, not only the
// bound agent's.
func (c *CronService) pruneExpiredFires(ctx context.Context, now time.Time) error {
	if c == nil || c.store() == nil {
		return nil
	}
	cfg := c.env.Deps.AppCfg
	cutoff := now.Add(-time.Duration(cfg.CronRetentionDays()) * 24 * time.Hour)
	fires, err := c.store().ExpiredFires(ctx, cutoff)
	if err != nil {
		return err
	}
	if len(fires) == 0 {
		return nil
	}

	// Each agent's conversation files live under its own state root; the
	// resolver is the one source of those roots. An agent no longer in the
	// configuration has no root to clean under — its rows still go.
	roots := map[string]string{}
	if resolver, rerr := appcfg.NewResolver(c.env.Root, cfg); rerr == nil {
		for _, sum := range resolver.All() {
			roots[sum.ID] = sum.WorkspaceRoot
		}
	}

	byAgent := map[string][]string{}
	recordIDs := make([]int64, 0, len(fires))
	for _, f := range fires {
		recordIDs = append(recordIDs, f.RecordID)
		if f.CronSession {
			byAgent[f.AgentID] = append(byAgent[f.AgentID], f.SessionID)
		}
	}
	// A session the delete spared because a live run holds it keeps its
	// whole life this round: its files on disk and its fire record wait for
	// the sweep that finds it quiet and ended.
	survived := map[string]bool{}
	for agentID, ids := range byAgent {
		left, derr := state.NewSessionStore(c.env.SQL, agentID).DeleteSessions(ctx, ids)
		if derr != nil {
			return derr
		}
		for _, id := range left.Survived {
			survived[id] = true
		}
		root, known := roots[agentID]
		if !known {
			slog.Warn("cron: retention deleted an unconfigured agent's rows; its files stay", "agent", agentID)
			continue
		}
		for _, id := range ids {
			if survived[id] {
				continue
			}
			if err := state.RemoveSessionStateFiles(root, id); err != nil {
				slog.Warn("cron: retention remove session state files", "session", id, "err", err)
			}
			if err := hook.RemoveSessionArtifacts(root, id); err != nil {
				slog.Warn("cron: retention remove session artifacts", "session", id, "err", err)
			}
		}
		for _, p := range left.SpillPaths {
			// A path from the database is untrusted until it names a plain
			// file right inside a tool-outputs directory — that is where the
			// governor spills. Nothing else is deleted on the database's word.
			if filepath.Base(filepath.Dir(p)) != tool.SpillDir {
				continue
			}
			info, serr := os.Stat(p)
			if serr != nil || !info.Mode().IsRegular() {
				continue
			}
			if err := os.Remove(p); err != nil {
				slog.Warn("cron: retention remove spilled output", "path", p, "err", err)
			}
		}
		for _, f := range left.Files {
			if err := c.env.Files.RemoveStored(ctx, f); err != nil {
				slog.Warn("cron: retention remove stored file", "file", f.ID, "err", err)
			}
		}
	}
	dropped := make([]int64, 0, len(recordIDs))
	for _, f := range fires {
		if survived[f.SessionID] {
			continue
		}
		dropped = append(dropped, f.RecordID)
	}
	return c.store().DeleteFireRecords(ctx, dropped)
}

// CronJobInput is what a surface supplies to create or edit a job. The pointer
// fields are the ones an edit may leave alone.
type CronJobInput struct {
	Name        string
	Schedule    string
	Prompt      string
	Deliver     string
	Enabled     *bool
	RepeatLimit int
	// ProjectID binds the job to one project; empty is agent-wide.
	ProjectID string
}

// ListJobs returns the bound agent's jobs, newest first.
func (c *CronService) ListJobs(ctx context.Context, agentID string) ([]state.CronJob, error) {
	store := c.store()
	if store == nil {
		return nil, fmt.Errorf("cron storage unavailable")
	}
	return store.ListJobs(ctx, agentID)
}

// CreateJob validates the schedule before the job is written, so a job that
// could never fire is refused at the moment it is created rather than sitting
// in the list looking scheduled.
func (c *CronService) CreateJob(ctx context.Context, agentID string, in CronJobInput) (state.CronJob, error) {
	store := c.store()
	if store == nil {
		return state.CronJob{}, fmt.Errorf("cron storage unavailable")
	}
	if strings.TrimSpace(in.Prompt) == "" {
		return state.CronJob{}, fmt.Errorf("prompt is required")
	}
	// A job binds to one of this agent's projects or to none: the binding is
	// tenant data like the job, so a project id learned elsewhere is refused
	// as not found rather than tying the job to another agent's project.
	if projectID := strings.TrimSpace(in.ProjectID); projectID != "" {
		if _, err := state.NewProjectStore(c.env.SQL, agentID).Get(ctx, projectID); err != nil {
			return state.CronJob{}, err
		}
	}
	job := state.CronJob{
		ID:          state.NewID("cron"),
		Ag:          strings.TrimSpace(agentID),
		ProjectID:   strings.TrimSpace(in.ProjectID),
		Name:        strings.TrimSpace(in.Name),
		Sched:       strings.TrimSpace(in.Schedule),
		Prompt:      in.Prompt,
		Deliver:     strings.TrimSpace(in.Deliver),
		Enabled:     in.Enabled == nil || *in.Enabled,
		RepeatLimit: in.RepeatLimit,
	}
	if err := turn.ApplySchedule(&job, time.Now()); err != nil {
		return state.CronJob{}, err
	}
	if err := store.InsertJob(ctx, job); err != nil {
		return state.CronJob{}, err
	}
	return job, nil
}

// Job returns one job, or an error when it belongs to another tenant. A job id
// learned elsewhere must not read or change a different agent's work.
func (c *CronService) Job(ctx context.Context, agentID, jobID string) (state.CronJob, error) {
	store := c.store()
	if store == nil {
		return state.CronJob{}, fmt.Errorf("cron storage unavailable")
	}
	job, err := store.GetJob(ctx, jobID)
	if err != nil {
		return state.CronJob{}, err
	}
	if job == nil || job.AgentID() != strings.TrimSpace(agentID) {
		return state.CronJob{}, fmt.Errorf("cron job %q not found", jobID)
	}
	return *job, nil
}

// UpdateJob applies an edit and recomputes when the job fires next. The
// schedule is recomputed on every edit, not only when its text changed:
// re-enabling a paused job has to give it a next fire again.
func (c *CronService) UpdateJob(ctx context.Context, agentID, jobID string, in CronJobInput) (state.CronJob, error) {
	job, err := c.Job(ctx, agentID, jobID)
	if err != nil {
		return state.CronJob{}, err
	}
	if v := strings.TrimSpace(in.Name); v != "" {
		job.Name = v
	}
	if v := strings.TrimSpace(in.Prompt); v != "" {
		job.Prompt = v
	}
	if v := strings.TrimSpace(in.Schedule); v != "" {
		job.Sched = v
	}
	job.Deliver = strings.TrimSpace(in.Deliver)
	if in.Enabled != nil {
		job.Enabled = *in.Enabled
	}
	if in.RepeatLimit != 0 {
		job.RepeatLimit = in.RepeatLimit
	}
	if err := turn.ApplySchedule(&job, time.Now()); err != nil {
		return state.CronJob{}, err
	}
	if err := c.store().UpdateJobConfig(ctx, job); err != nil {
		return state.CronJob{}, err
	}
	return job, nil
}

func (c *CronService) DeleteJob(ctx context.Context, agentID, jobID string) error {
	job, err := c.Job(ctx, agentID, jobID)
	if err != nil {
		return err
	}
	return c.store().DeleteJob(ctx, job.ID)
}

// RunJobNow fires a job immediately without disturbing its schedule. The run is
// asynchronous: its result lands in the job's history.
func (c *CronService) RunJobNow(ctx context.Context, agentID, jobID string) error {
	job, err := c.Job(ctx, agentID, jobID)
	if err != nil {
		return err
	}
	c.mu.Lock()
	sched := c.sched
	c.mu.Unlock()
	if sched == nil {
		return fmt.Errorf("no scheduler is bound")
	}
	return sched.RunNow(ctx, job.ID)
}

func (c *CronService) JobRuns(ctx context.Context, agentID, jobID string, limit int) ([]state.CronRun, error) {
	job, err := c.Job(ctx, agentID, jobID)
	if err != nil {
		return nil, err
	}
	return c.store().ListRuns(ctx, job.ID, limit)
}

func (c *CronService) Heartbeat(ctx context.Context, sessionID string) (*state.Heartbeat, error) {
	store := c.store()
	if store == nil {
		return nil, fmt.Errorf("cron storage unavailable")
	}
	return store.GetHeartbeat(ctx, sessionID)
}

// SetHeartbeat installs or replaces a session's recurring instruction. paused
// is the user's intent; it is folded into the stored schedule so "paused" and
// "no next fire" are one fact.
func (c *CronService) SetHeartbeat(ctx context.Context, hb state.Heartbeat, paused bool) (state.Heartbeat, error) {
	store := c.store()
	if store == nil {
		return state.Heartbeat{}, fmt.Errorf("cron storage unavailable")
	}
	if strings.TrimSpace(hb.SessionID) == "" {
		return state.Heartbeat{}, fmt.Errorf("session id is required")
	}
	if err := turn.ApplyHeartbeatInterval(&hb, time.Now(), paused); err != nil {
		return state.Heartbeat{}, err
	}
	if err := store.SaveHeartbeat(ctx, hb); err != nil {
		return state.Heartbeat{}, err
	}
	return hb, nil
}

func (c *CronService) ClearHeartbeat(ctx context.Context, sessionID string) error {
	store := c.store()
	if store == nil {
		return fmt.Errorf("cron storage unavailable")
	}
	return store.DeleteHeartbeat(ctx, sessionID)
}
