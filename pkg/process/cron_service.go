package process

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/channel"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
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

	mu      sync.Mutex
	sched   *turn.Scheduler
	stop    func()
	agentID string
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

// Bind points the scheduler at one primary agent and starts it, stopping
// whatever was bound before.
//
// Standing work is tenant data. Rebinding on a switch is what keeps a
// switched-away agent's jobs from firing against a runtime that now belongs to
// a different agent — the same rule the channel registry follows.
func (c *CronService) Bind(ctx context.Context, agentID string) {
	if c == nil || c.store() == nil {
		return
	}
	id := strings.TrimSpace(agentID)
	if id == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stop != nil {
		c.stop()
		c.stop = nil
	}
	next := &turn.Scheduler{
		Store:   c.store(),
		AgentID: id,
		Deps: turn.SchedulerDeps{
			RunPrompt:   c.runPrompt,
			DeliverText: c.deliver,
			SessionBusy: c.sessionBusy,
		},
	}
	c.sched = next
	c.agentID = id
	c.stop = next.Start(ctx)
}

// Stop ends the scheduler loop. The jobs stay on disk; nothing fires until
// something binds again.
func (c *CronService) Stop() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stop != nil {
		c.stop()
		c.stop = nil
	}
	c.sched = nil
	c.agentID = ""
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

// runPrompt runs a scheduled prompt through the same one-shot entry point a
// channel message uses, so scheduled work is not a second kind of turn with
// rules of its own.
func (c *CronService) runPrompt(ctx context.Context, sessionID, channelID, prompt string) (string, string) {
	if c == nil || c.env == nil {
		return "", "runtime unavailable"
	}
	return c.env.RunAgentOnceExec(ctx, sessionID, channelID, "", prompt)
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

// sessionBusy reports whether a session has a turn in flight, which is what
// stops a heartbeat from interrupting one.
func (c *CronService) sessionBusy(sessionID string) bool {
	if c == nil || c.env == nil || c.env.Control == nil {
		return false
	}
	_, _, ok := c.env.Control.Find(strings.TrimSpace(sessionID))
	return ok
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
