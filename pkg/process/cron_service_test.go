package process

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/channel"
	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

// TestBindHandsTheSchedulerTheSurfacesHeartbeatStarter pins the wiring: after
// Bind, a due beat reaches the surface's StartHeartbeat — the gateway's turn
// starter — rather than a one-shot runner of the service's own.
func TestBindHandsTheSchedulerTheSurfacesHeartbeatStarter(t *testing.T) {
	env, _ := envWithMCPServers(t, "")
	ctx := context.Background()
	store := &state.CronStore{DB: env.SQL}
	if err := state.NewSessionStore(env.SQL, "main").Ensure(ctx, "s-1", "s-1"); err != nil {
		t.Fatalf("ensure session: %v", err)
	}
	now := time.Now()
	hb := state.Heartbeat{SessionID: "s-1", IntervalSec: 300, Prompt: "anything new?"}
	if err := turn.ApplyHeartbeatInterval(&hb, now.Add(-10*time.Minute), false); err != nil {
		t.Fatalf("ApplyHeartbeatInterval: %v", err)
	}
	if err := store.SaveHeartbeat(ctx, hb); err != nil {
		t.Fatalf("SaveHeartbeat: %v", err)
	}

	var (
		mu    sync.Mutex
		calls [][2]string
	)
	c := env.Cron()
	c.Bind(ctx, "main", ScheduledTurns{StartHeartbeat: func(_ context.Context, sessionID, prompt string) error {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, [2]string{sessionID, prompt})
		return nil
	}})
	defer c.Stop()

	c.mu.Lock()
	sched := c.sched
	c.mu.Unlock()
	sched.RunDue(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(calls)
		mu.Unlock()
		if n == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 || calls[0][0] != "s-1" || calls[0][1] != "anything new?" {
		t.Fatalf("starter calls = %#v, want exactly one beat of s-1", calls)
	}
}

// fakeOutboundChannel is a delivery target that records what it is told to
// send, so a test can see the answer a fire delivered.
type fakeOutboundChannel struct {
	mu   sync.Mutex
	sent []string
}

func (f *fakeOutboundChannel) ID() string { return "fake" }

func (f *fakeOutboundChannel) Start(context.Context, channel.RouteAdder, channel.Bus) error {
	return nil
}

func (f *fakeOutboundChannel) Stop(context.Context) error { return nil }

func (f *fakeOutboundChannel) DeliverOutbound(_ context.Context, o channel.Outbound) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, o.Text)
	return nil
}

func (f *fakeOutboundChannel) delivered() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.sent...)
}

// TestFireRunStateReadsTheRunsOwnFacts pins the one read a fire's settling
// rests on: the run store says whether the fire's run is alive — running
// under a renewing owner, or parked on an approval even after that owner died
// — and, once it is neither, hands back the ending its run recorded, the same
// ending a reaper writes for a process that stopped mid-run.
func TestFireRunStateReadsTheRunsOwnFacts(t *testing.T) {
	env, _ := envWithMCPServers(t, "")
	ctx := context.Background()
	c := env.Cron()
	sessions := state.NewSessionStore(env.SQL, "main")
	for _, sid := range []string{"cron-park-1", "cron-died-1", "cron-unstarted-1"} {
		if err := sessions.Ensure(ctx, sid, sid); err != nil {
			t.Fatal(err)
		}
	}

	// A second process, sharing the database, drives the fires' runs.
	driver := &state.RunStore{DB: env.SQL, Owner: "proc-fire-driver"}
	stopDriver, err := driver.HoldOwnerLease(ctx)
	if err != nil {
		t.Fatal(err)
	}

	parked, err := driver.CreateRun(ctx, "cron-park-1", "nightly summary")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.SQL.ExecContext(ctx,
		`INSERT INTO fb_actions(id, session_id, kind, status, payload_json, created_at, updated_at)
		VALUES(?,?,?,?,?,?,?)`, "act-park", "cron-park-1", "tool_approval", string(state.ActionPending), "{}", 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := driver.SetWaitingAction(ctx, parked.ID, state.Wait{ActionID: "act-park", ToolName: "shell", ToolInputJSON: "{}"}); err != nil {
		t.Fatal(err)
	}
	died, err := driver.CreateRun(ctx, "cron-died-1", "nightly summary")
	if err != nil {
		t.Fatal(err)
	}

	// Both runs are alive while their owner renews.
	for _, sid := range []string{"cron-park-1", "cron-died-1"} {
		run, err := c.fireRunState(ctx, sid)
		if err != nil {
			t.Fatal(err)
		}
		if !run.Alive || run.Ended {
			t.Fatalf("fire run state of %s = %+v, want alive", sid, run)
		}
	}

	// The driving process stops renewing. The parked run is still alive — the
	// approval is waiting for a person, not for the process.
	stopDriver()
	parkedRun, err := c.fireRunState(ctx, "cron-park-1")
	if err != nil {
		t.Fatal(err)
	}
	if !parkedRun.Alive {
		t.Fatalf("a run parked on an approval must read alive after its owner died: %+v", parkedRun)
	}

	// The other run is reaped as abandoned, and its ending is the reaper's.
	reaped, err := env.Deps.RunRT.ReapAbandonedRuns(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(reaped) != 1 || reaped[0].ID != died.ID {
		t.Fatalf("reap = %+v, want only the abandoned run (not the parked one)", reaped)
	}
	if _, err := env.Deps.RunRT.AppendSessionEvent(ctx, state.SessionEvent{
		ID: "evt-reap-fire", RunID: died.ID, SessionID: "cron-died-1",
		Type: event.RunEventTurnError,
		Payload: event.EncodePayload(event.TurnErrorPayload{
			Error: turn.AbandonedRunReason, Message: turn.AbandonedRunReason,
			Detail: &event.TurnErrorDetail{Code: "run_abandoned"},
		}),
	}); err != nil {
		t.Fatal(err)
	}
	ended, err := c.fireRunState(ctx, "cron-died-1")
	if err != nil {
		t.Fatal(err)
	}
	if ended.Alive || !ended.Ended {
		t.Fatalf("the abandoned run's fire = %+v, want ended", ended)
	}
	if ended.Ending.ErrCode != "run_abandoned" || ended.Ending.ErrText != turn.AbandonedRunReason {
		t.Fatalf("the abandoned ending = %+v", ended.Ending)
	}

	// A fire whose run has not been created yet has ended nothing.
	fresh, err := c.fireRunState(ctx, "cron-unstarted-1")
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Alive || fresh.Ended {
		t.Fatalf("a fire with no run = %+v, want nothing ended", fresh)
	}
}

// TestSettleFireAndFireParkedReachTheBoundScheduler pins the service's two
// hand-offs to the scheduler — and that with no scheduler bound (before a
// bind, or after a stop) neither call is an error: the next bound pass
// settles the same fire from the same persisted facts.
func TestSettleFireAndFireParkedReachTheBoundScheduler(t *testing.T) {
	env, _ := envWithMCPServers(t, "")
	ctx := context.Background()

	fake := &fakeOutboundChannel{}
	env.Channels = channel.NewRegistry()
	if err := env.Channels.Bind(ctx, "main", []channel.Handler{fake}, nil); err != nil {
		t.Fatal(err)
	}

	cronStore := &state.CronStore{DB: env.SQL}
	sessions := state.NewSessionStore(env.SQL, "main")
	for _, sid := range []string{"cron-job-1-1", "cron-job-2-1"} {
		if err := sessions.Ensure(ctx, sid, sid); err != nil {
			t.Fatal(err)
		}
	}
	job := state.CronJob{ID: "job-1", Ag: "main", Sched: "every 1h", Prompt: "x", Deliver: "fake", Enabled: true}
	if err := turn.ApplySchedule(&job, time.Now().Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := cronStore.InsertJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	parked := state.CronJob{ID: "job-2", Ag: "main", Sched: "every 1h", Prompt: "y", Deliver: "fake", Enabled: true}
	if err := turn.ApplySchedule(&parked, time.Now().Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := cronStore.InsertJob(ctx, parked); err != nil {
		t.Fatal(err)
	}

	// One fire whose run already ended with an answer, one still open.
	run, err := env.Deps.RunRT.CreateRun(ctx, "cron-job-1-1", "x")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.Deps.RunRT.AppendSessionEvent(ctx, state.SessionEvent{
		ID: "evt-fire-done", RunID: run.ID, SessionID: "cron-job-1-1",
		Type: event.RunEventTurnCompleted, Payload: event.EncodePayload(event.TurnCompletedPayload{Text: "the answer"}),
	}); err != nil {
		t.Fatal(err)
	}
	if err := env.Deps.RunRT.SetStatus(ctx, run.ID, state.RunStatusDone); err != nil {
		t.Fatal(err)
	}
	if _, err := cronStore.StartRun(ctx, state.CronRun{JobID: "job-1", AgentID: "main", SessionID: "cron-job-1-1", Trigger: "schedule"}); err != nil {
		t.Fatal(err)
	}
	if _, err := cronStore.StartRun(ctx, state.CronRun{JobID: "job-2", AgentID: "main", SessionID: "cron-job-2-1", Trigger: "schedule"}); err != nil {
		t.Fatal(err)
	}

	c := env.Cron()
	// Before a bind: nothing is bound, nothing happens, nothing errors.
	c.SettleFire(ctx, "cron-job-1-1")
	c.FireParked(ctx, "cron-job-2-1", "notice before a bind")

	c.Bind(ctx, "main", ScheduledTurns{
		StartHeartbeat: func(context.Context, string, string) error { return nil },
		StartFire:      func(context.Context, turn.CronFire) error { return nil },
	})

	// SettleFire reaches the scheduler: the ended fire closes and delivers.
	c.SettleFire(ctx, "cron-job-1-1")
	runs, err := cronStore.ListRuns(ctx, "job-1", 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = %v (%v)", runs, err)
	}
	if runs[0].Status != state.CronStatusOK || runs[0].Output != "the answer" || runs[0].DeliveredTo != "fake" {
		t.Fatalf("the settled fire = %#v", runs[0])
	}
	if delivered := fake.delivered(); len(delivered) != 1 || delivered[0] != "the answer" {
		t.Fatalf("delivered = %#v", delivered)
	}

	// FireParked reaches the scheduler: the open fire's job is told.
	notice := "Waiting for approval to run a tool. Approve it in the Forebrain Harness app to continue."
	c.FireParked(ctx, "cron-job-2-1", notice)
	if delivered := fake.delivered(); len(delivered) != 2 || delivered[1] != notice {
		t.Fatalf("delivered after the parked notice = %#v", delivered)
	}

	// After a stop: neither call errors, and neither touches the open fire.
	c.Stop()
	c.SettleFire(ctx, "cron-job-2-1")
	c.FireParked(ctx, "cron-job-2-1", notice)
	open, err := cronStore.LatestOpenFire(ctx, "job-2")
	if err != nil || open == nil {
		t.Fatalf("the open fire after Stop = %v (%v), want it left alone", open, err)
	}
	if delivered := fake.delivered(); len(delivered) != 2 {
		t.Fatalf("deliveries after Stop = %#v", delivered)
	}
}

// envWithTwoPrimaryAgents opens a runtime with two tenants configured, so a
// retention sweep can be proven to cover every agent's fires and clean each
// agent's files under its own state root.
func envWithTwoPrimaryAgents(t *testing.T) *Environment {
	t.Helper()
	t.Setenv("FOREBRAIN_REQUIRED_MCP_KEY", "test-key")
	home := t.TempDir()
	cfg := "cron:\n" +
		"  retention_days: 30\n" +
		"features:\n" +
		"  memories: false\n" +
		"agents:\n" +
		"  definitions:\n" +
		"    main:\n" +
		"      llm_providers:\n" +
		"      - provider: openai\n" +
		"        model: gpt-test\n" +
		"        api_key: ${FOREBRAIN_REQUIRED_MCP_KEY}\n" +
		"        base_url: http://127.0.0.1:1/v1\n" +
		"        params:\n" +
		"          stream: false\n" +
		"    second:\n" +
		"      primary: true\n"
	if err := os.WriteFile(filepath.Join(home, "forebrain.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	env, err := Open(context.Background(), OpenOptions{
		Home:          home,
		ConfigPath:    filepath.Join(home, "forebrain.yaml"),
		LaunchDir:     t.TempDir(),
		SessionSource: "webchat",
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(env.Close)
	return env
}

// TestPruneExpiredFiresRemovesEverything pins the whole deletion: both
// tenants' expired conversations go from the database and from every corner
// of their state roots, the spilled output recorded under tool-outputs goes
// with them, a full_path outside tool-outputs is left strictly alone, and
// everything not yet expired — rows and files — stays.
func TestPruneExpiredFiresRemovesEverything(t *testing.T) {
	env := envWithTwoPrimaryAgents(t)
	ctx := context.Background()
	db := env.SQL
	store := &state.CronStore{DB: db}
	now := time.Now()
	old := now.Add(-40 * 24 * time.Hour).Unix()

	seedFire := func(agentID, sid string, lastActive int64) {
		t.Helper()
		if err := state.NewSessionStore(db, agentID).EnsureAt(ctx, sid, sid,
			state.SessionBirth{Source: state.SessionSourceCron}); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE fb_sessions SET created_at=?, updated_at=? WHERE id=?`, old, lastActive, sid); err != nil {
			t.Fatal(err)
		}
		id, err := store.StartRun(ctx, state.CronRun{JobID: "job-" + agentID, AgentID: agentID, SessionID: sid, Trigger: "schedule"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.FinishRun(ctx, id, state.CronStatusOK, "done", "", ""); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE fb_cron_runs SET started_at=?, finished_at=? WHERE id=?`, old, old, id); err != nil {
			t.Fatal(err)
		}
	}

	agents := []struct {
		id   string
		root string
	}{
		{"main", filepath.Join(env.Root, "workspace")},
		{"second", filepath.Join(env.Root, "workspaces", "second")},
	}
	for _, agent := range agents {
		oldSid := "cron-" + agent.id + "-old"
		freshSid := "cron-" + agent.id + "-fresh"
		seedFire(agent.id, oldSid, old)
		seedFire(agent.id, freshSid, now.Unix())

		artifacts := func(sid string) []string {
			return []string{
				filepath.Join(agent.root, "state", "modes", sid+".json"),
				filepath.Join(agent.root, "state", "todos", sid+".json"),
				filepath.Join(agent.root, "state", "intermediate", sid+".md"),
				filepath.Join(agent.root, "state", "hook-transcripts", sid+".jsonl"),
				filepath.Join(agent.root, "state", "fork-sidechain", sid, "subagent-a.jsonl"),
			}
		}
		writeAll := func(sid string) {
			t.Helper()
			for _, p := range artifacts(sid) {
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
		}
		writeAll(oldSid)
		writeAll(freshSid)

		// One spilled output where the governor spills, one recorded path
		// somewhere else entirely. Both are named by the conversation's tool
		// rows; only the one inside tool-outputs may be deleted.
		spill := filepath.Join(agent.root, "state", "tmp", "tool-outputs", "shell-call-9.txt")
		outside := filepath.Join(agent.root, "keep-me.txt")
		for _, p := range []string{spill, outside} {
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte("bytes"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		for _, p := range []string{spill, outside} {
			if _, err := db.Exec(`INSERT INTO fb_messages(session_id, role, content, tool_meta_json, created_at)
				VALUES(?, 'tool', 'done', ?, ?)`, oldSid,
				`{"full_path":`+strconv.Quote(filepath.ToSlash(p))+`}`, old); err != nil {
				t.Fatal(err)
			}
		}
	}

	if err := env.Cron().pruneExpiredFires(ctx, now); err != nil {
		t.Fatalf("pruneExpiredFires: %v", err)
	}

	for _, agent := range agents {
		oldSid := "cron-" + agent.id + "-old"
		freshSid := "cron-" + agent.id + "-fresh"

		var rows int
		if err := db.QueryRow(`SELECT COUNT(*) FROM fb_messages WHERE session_id=?`, oldSid).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		if rows != 0 {
			t.Fatalf("%s: %d message rows survived the sweep", oldSid, rows)
		}
		for _, p := range []string{
			filepath.Join(agent.root, "state", "modes", oldSid+".json"),
			filepath.Join(agent.root, "state", "todos", oldSid+".json"),
			filepath.Join(agent.root, "state", "intermediate", oldSid+".md"),
			filepath.Join(agent.root, "state", "hook-transcripts", oldSid+".jsonl"),
			filepath.Join(agent.root, "state", "fork-sidechain", oldSid, "subagent-a.jsonl"),
			filepath.Join(agent.root, "state", "tmp", "tool-outputs", "shell-call-9.txt"),
		} {
			if _, err := os.Stat(p); !os.IsNotExist(err) {
				t.Fatalf("%s survived the sweep", p)
			}
		}

		// The untouched half: a path outside tool-outputs, and everything of
		// the not-yet-expired fire.
		if _, err := os.Stat(filepath.Join(agent.root, "keep-me.txt")); err != nil {
			t.Fatalf("a recorded path outside tool-outputs was deleted: %v", err)
		}
		var fresh int
		if err := db.QueryRow(`SELECT COUNT(*) FROM fb_sessions WHERE id=?`, freshSid).Scan(&fresh); err != nil {
			t.Fatal(err)
		}
		if fresh != 1 {
			t.Fatalf("%s did not survive the sweep", freshSid)
		}
		if _, err := os.Stat(filepath.Join(agent.root, "state", "modes", freshSid+".json")); err != nil {
			t.Fatalf("the fresh fire's state file did not survive the sweep: %v", err)
		}
	}
	var fires int
	if err := db.QueryRow(`SELECT COUNT(*) FROM fb_cron_runs`).Scan(&fires); err != nil {
		t.Fatal(err)
	}
	if fires != 2 {
		t.Fatalf("fire records after the sweep = %d, want only the 2 fresh ones", fires)
	}
}

// TestMaintainSweepsAtMostOnceAnHour pins the throttle: two passes closer
// together than the sweep interval clean once, and the next full interval
// later cleans again.
func TestMaintainSweepsAtMostOnceAnHour(t *testing.T) {
	env := envWithTwoPrimaryAgents(t)
	ctx := context.Background()
	db := env.SQL
	store := &state.CronStore{DB: db}
	now := time.Now()
	old := now.Add(-40 * 24 * time.Hour).Unix()

	quietFire := func(sid string, at int64) {
		t.Helper()
		if err := state.NewSessionStore(db, "main").EnsureAt(ctx, sid, sid,
			state.SessionBirth{Source: state.SessionSourceCron}); err != nil {
			t.Fatal(err)
		}
		id, err := store.StartRun(ctx, state.CronRun{JobID: "job-t", AgentID: "main", SessionID: sid, Trigger: "schedule"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.FinishRun(ctx, id, state.CronStatusOK, "done", "", ""); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE fb_sessions SET created_at=?, updated_at=? WHERE id=?`, old, at, sid); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE fb_cron_runs SET started_at=?, finished_at=? WHERE id=?`, old, old, id); err != nil {
			t.Fatal(err)
		}
	}

	c := env.Cron()
	quietFire("cron-throttle-1", old)
	c.Maintain(ctx, now)
	var left int
	if err := db.QueryRow(`SELECT COUNT(*) FROM fb_cron_runs WHERE session_id='cron-throttle-1'`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatal("the first Maintain did not sweep the expired fire")
	}

	// A second expired fire, swept by the next pass — but the pass ten
	// minutes later is throttled away.
	quietFire("cron-throttle-2", old)
	c.Maintain(ctx, now.Add(10*time.Minute))
	if err := db.QueryRow(`SELECT COUNT(*) FROM fb_cron_runs WHERE session_id='cron-throttle-2'`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 1 {
		t.Fatal("a throttled Maintain swept a second time")
	}

	c.Maintain(ctx, now.Add(time.Hour+time.Minute))
	if err := db.QueryRow(`SELECT COUNT(*) FROM fb_cron_runs WHERE session_id='cron-throttle-2'`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatal("the Maintain after a full interval did not sweep")
	}
}

// TestPruneReadsTheLiveRetentionConfig pins that each sweep reads the
// configuration in force right then — the hot-reloaded value — so changing
// the retention on the settings page applies from the next cleanup without a
// restart.
func TestPruneReadsTheLiveRetentionConfig(t *testing.T) {
	env := envWithTwoPrimaryAgents(t)
	ctx := context.Background()
	db := env.SQL
	store := &state.CronStore{DB: db}
	now := time.Now()
	old := now.Add(-40 * 24 * time.Hour).Unix()

	if err := state.NewSessionStore(db, "main").EnsureAt(ctx, "cron-live-cfg", "cron-live-cfg",
		state.SessionBirth{Source: state.SessionSourceCron}); err != nil {
		t.Fatal(err)
	}
	id, err := store.StartRun(ctx, state.CronRun{JobID: "job-c", AgentID: "main", SessionID: "cron-live-cfg", Trigger: "schedule"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishRun(ctx, id, state.CronStatusOK, "done", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE fb_sessions SET created_at=?, updated_at=? WHERE id='cron-live-cfg'`, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE fb_cron_runs SET started_at=?, finished_at=? WHERE id=?`, old, old, id); err != nil {
		t.Fatal(err)
	}

	long := 3650
	env.Deps.AppCfg.Cron = appcfg.CronSection{RetentionDays: &long}
	if err := env.Cron().pruneExpiredFires(ctx, now); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM fb_cron_runs WHERE session_id='cron-live-cfg'`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatal("a sweep under a 3650-day retention deleted a 40-day-old fire")
	}

	one := 1
	env.Deps.AppCfg.Cron = appcfg.CronSection{RetentionDays: &one}
	if err := env.Cron().pruneExpiredFires(ctx, now); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM fb_cron_runs WHERE session_id='cron-live-cfg'`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatal("a sweep under a 1-day retention kept a 40-day-old fire")
	}
}
