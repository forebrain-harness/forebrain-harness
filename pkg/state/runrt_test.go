package state

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
)

// --- Wait-resume lease semantics -------------------------------------------
//
// The table-driven cases below pin every branch of the durable continuation
// lease. They are written against the observable API only, so the storage
// behind them (a JSON blob, typed columns, ...) can change without the
// contract moving.

func newLeaseTestStore(t *testing.T) (*RunStore, string) {
	t.Helper()
	ctx := context.Background()
	db, err := OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := NewSessionStore(db, "main")
	if err := store.Ensure(ctx, "s", "s"); err != nil {
		t.Fatal(err)
	}
	rt := &RunStore{DB: db}
	run, err := rt.CreateRun(ctx, "s", "input")
	if err != nil {
		t.Fatal(err)
	}
	return rt, run.ID
}

func seedWait(t *testing.T, rt *RunStore, sessionID, runID string, w Wait) {
	t.Helper()
	ctx := context.Background()
	if _, err := rt.DB.ExecContext(ctx, `INSERT INTO fb_actions(id, session_id, kind, status, payload_json, created_at, updated_at)
		VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(id) DO NOTHING`, w.ActionID, sessionID, "tool_approval", string(ActionPending), "{}", 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := rt.SetWaitingAction(ctx, runID, w); err != nil {
		t.Fatal(err)
	}
}

// mustWaitAction creates the fb_actions row a wait references; the foreign
// key enforces that every parked run waits on a real action.
func mustWaitAction(t *testing.T, db *sql.DB, sessionID, actionID string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `INSERT INTO fb_actions(id, session_id, kind, status, payload_json, created_at, updated_at)
		VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(id) DO NOTHING`, actionID, sessionID, "tool_approval", string(ActionPending), "{}", 1, 1); err != nil {
		t.Fatal(err)
	}
}

func TestClaimWaitResumeBranches(t *testing.T) {
	nowMs := time.Now().UTC().UnixMilli()
	staleMs := nowMs - int64(WaitResumeLease.Milliseconds()) - 1000
	cases := []struct {
		name    string
		seed    *Wait
		owner   string
		refresh bool
		want    bool // ClaimWaitResume result
	}{
		{name: "no continuation row", seed: nil, owner: "p1", want: false},
		{name: "unowned wait is claimable", seed: &Wait{}, owner: "p1", want: true},
		{name: "legacy owner without timestamp is stale", seed: &Wait{ResumeOwner: "p0"}, owner: "p1", want: true},
		{name: "another live owner blocks the claim", seed: &Wait{ResumeOwner: "p0", ResumeClaimedAt: nowMs}, owner: "p1", want: false},
		{name: "own live lease is not re-claimed", seed: &Wait{ResumeOwner: "p1", ResumeClaimedAt: nowMs}, owner: "p1", want: false},
		{name: "another owner with a stale lease is taken over", seed: &Wait{ResumeOwner: "p0", ResumeClaimedAt: staleMs}, owner: "p1", want: true},
		{name: "own stale lease is re-claimed", seed: &Wait{ResumeOwner: "p1", ResumeClaimedAt: staleMs}, owner: "p1", want: true},
		{name: "execution fence blocks any other owner", seed: &Wait{ResumeOwner: "p0", ResumeClaimedAt: nowMs, ResumePhase: WaitResumePhaseExecutionStarted}, owner: "p1", want: false},
		{name: "execution fence blocks even its own owner without refresh", seed: &Wait{ResumeOwner: "p1", ResumeClaimedAt: nowMs, ResumePhase: WaitResumePhaseExecutionStarted}, owner: "p1", want: false},
		{name: "uncertain outcome blocks the claim", seed: &Wait{ResumeOwner: "p0", ResumeClaimedAt: nowMs, ResumePhase: WaitResumePhaseUncertain}, owner: "p1", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rt, runID := newLeaseTestStore(t)
			if tc.seed != nil {
				w := *tc.seed
				w.ActionID = "act-" + tc.name
				w.ToolName = "shell"
				seedWait(t, rt, "s", runID, w)
			}
			var (
				got bool
				err error
			)
			if tc.refresh {
				err = rt.MarkWaitResumeOwner(context.Background(), runID, "act-"+tc.name, tc.owner)
			} else {
				got, err = rt.ClaimWaitResume(context.Background(), runID, "act-"+tc.name, tc.owner)
			}
			if err != nil {
				t.Fatalf("claim branch error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("claim = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMarkWaitResumeOwnerBranches(t *testing.T) {
	nowMs := time.Now().UTC().UnixMilli()
	cases := []struct {
		name         string
		seed         Wait
		wantErr      string // "" means nil
		wantNotFound bool
	}{
		{name: "own live lease refreshes", seed: Wait{ResumeOwner: "p1", ResumeClaimedAt: nowMs}},
		{name: "own fenced lease still refreshes", seed: Wait{ResumeOwner: "p1", ResumeClaimedAt: nowMs, ResumePhase: WaitResumePhaseExecutionStarted}},
		{name: "another live owner is refused", seed: Wait{ResumeOwner: "p0", ResumeClaimedAt: nowMs}, wantErr: "owned by another live process"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rt, runID := newLeaseTestStore(t)
			w := tc.seed
			w.ActionID, w.ToolName = "act", "shell"
			seedWait(t, rt, "s", runID, w)
			err := rt.MarkWaitResumeOwner(context.Background(), runID, "act", "p1")
			switch {
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("error = %v, want it to mention %q", err, tc.wantErr)
			case tc.wantErr == "" && err != nil:
				t.Fatalf("error = %v, want nil", err)
			}
		})
	}
	t.Run("missing continuation", func(t *testing.T) {
		rt, runID := newLeaseTestStore(t)
		if err := rt.MarkWaitResumeOwner(context.Background(), runID, "missing", "p1"); !errors.Is(err, ErrNoWaitContinuation) {
			t.Fatalf("error = %v, want ErrNoWaitContinuation", err)
		}
	})
}

func TestReleaseWaitResumeOwnerBranches(t *testing.T) {
	nowMs := time.Now().UTC().UnixMilli()
	ctx := context.Background()
	t.Run("missing row is a no-op", func(t *testing.T) {
		rt, runID := newLeaseTestStore(t)
		if err := rt.ReleaseWaitResumeOwner(ctx, runID, "missing", "p1"); err != nil {
			t.Fatalf("error = %v, want nil", err)
		}
	})
	t.Run("another owner's lease is left alone", func(t *testing.T) {
		rt, runID := newLeaseTestStore(t)
		seedWait(t, rt, "s", runID, Wait{ActionID: "act", ToolName: "shell", ResumeOwner: "p0", ResumeClaimedAt: nowMs})
		if err := rt.ReleaseWaitResumeOwner(ctx, runID, "act", "p1"); err != nil {
			t.Fatalf("error = %v, want nil", err)
		}
		owned, err := rt.WaitResumeOwnedBy(ctx, runID, "act", "p0")
		if err != nil || !owned {
			t.Fatalf("p0 still owns = %v, %v; release must not clear another owner's fence", owned, err)
		}
	})
	t.Run("crossed execution fence refuses release", func(t *testing.T) {
		rt, runID := newLeaseTestStore(t)
		seedWait(t, rt, "s", runID, Wait{ActionID: "act", ToolName: "shell", ResumeOwner: "p1", ResumeClaimedAt: nowMs, ResumePhase: WaitResumePhaseExecutionStarted})
		err := rt.ReleaseWaitResumeOwner(ctx, runID, "act", "p1")
		if err == nil || !strings.Contains(err.Error(), "execution fence") {
			t.Fatalf("error = %v, want the fence refusal", err)
		}
	})
	t.Run("own claim is released", func(t *testing.T) {
		rt, runID := newLeaseTestStore(t)
		seedWait(t, rt, "s", runID, Wait{ActionID: "act", ToolName: "shell", ResumeOwner: "p1", ResumeClaimedAt: nowMs})
		if err := rt.ReleaseWaitResumeOwner(ctx, runID, "act", "p1"); err != nil {
			t.Fatalf("error = %v, want nil", err)
		}
		w, err := rt.GetWaitForRun(ctx, runID)
		if err != nil || w == nil {
			t.Fatalf("wait after release = %v, %v", w, err)
		}
		if w.ResumeOwner != "" || w.ResumePhase != "" || w.ResumeClaimedAt != 0 {
			t.Fatalf("released lease = %+v, want it cleared", w)
		}
	})
}

func TestBeginWaitResumeExecutionBranches(t *testing.T) {
	nowMs := time.Now().UTC().UnixMilli()
	ctx := context.Background()
	t.Run("missing run", func(t *testing.T) {
		rt, runID := newLeaseTestStore(t)
		if err := rt.BeginWaitResumeExecution(ctx, runID, "missing", "p1"); !errors.Is(err, ErrRunNotFound) {
			t.Fatalf("error = %v, want ErrRunNotFound", err)
		}
	})
	t.Run("another owner cannot cross the fence", func(t *testing.T) {
		rt, runID := newLeaseTestStore(t)
		seedWait(t, rt, "s", runID, Wait{ActionID: "act", ToolName: "shell", ResumeOwner: "p0", ResumeClaimedAt: nowMs})
		err := rt.BeginWaitResumeExecution(ctx, runID, "act", "p1")
		if err == nil || !strings.Contains(err.Error(), "not owned") {
			t.Fatalf("error = %v, want the ownership refusal", err)
		}
	})
	t.Run("own claim crosses and second crossing refuses", func(t *testing.T) {
		rt, runID := newLeaseTestStore(t)
		seedWait(t, rt, "s", runID, Wait{ActionID: "act", ToolName: "shell", ResumeOwner: "p1", ResumeClaimedAt: nowMs})
		if err := rt.BeginWaitResumeExecution(ctx, runID, "act", "p1"); err != nil {
			t.Fatalf("first crossing: %v", err)
		}
		err := rt.BeginWaitResumeExecution(ctx, runID, "act", "p1")
		if err == nil || !strings.Contains(err.Error(), "already started") {
			t.Fatalf("second crossing error = %v, want the idempotence refusal", err)
		}
	})
}

func TestMarkWaitResumeUncertainBranches(t *testing.T) {
	nowMs := time.Now().UTC().UnixMilli()
	ctx := context.Background()
	t.Run("missing row is a no-op", func(t *testing.T) {
		rt, runID := newLeaseTestStore(t)
		if err := rt.MarkWaitResumeUncertain(ctx, runID, "missing"); err != nil {
			t.Fatalf("error = %v, want nil", err)
		}
	})
	t.Run("unstarted execution refuses", func(t *testing.T) {
		rt, runID := newLeaseTestStore(t)
		seedWait(t, rt, "s", runID, Wait{ActionID: "act", ToolName: "shell", ResumeOwner: "p1", ResumeClaimedAt: nowMs})
		err := rt.MarkWaitResumeUncertain(ctx, runID, "act")
		if err == nil || !strings.Contains(err.Error(), "not started") {
			t.Fatalf("error = %v, want the not-started refusal", err)
		}
	})
	t.Run("started execution becomes uncertain, idempotently", func(t *testing.T) {
		rt, runID := newLeaseTestStore(t)
		seedWait(t, rt, "s", runID, Wait{ActionID: "act", ToolName: "shell", ResumeOwner: "p1", ResumeClaimedAt: nowMs, ResumePhase: WaitResumePhaseExecutionStarted})
		if err := rt.MarkWaitResumeUncertain(ctx, runID, "act"); err != nil {
			t.Fatalf("first marking: %v", err)
		}
		if err := rt.MarkWaitResumeUncertain(ctx, runID, "act"); err != nil {
			t.Fatalf("second marking must be a no-op: %v", err)
		}
		w, err := rt.GetWaitForRun(ctx, runID)
		if err != nil || w == nil || w.ResumePhase != WaitResumePhaseUncertain {
			t.Fatalf("phase after marking = %+v, %v", w, err)
		}
	})
}

// ensureSessionsForRuns creates the session rows a test's runs reference; the
// runs table's foreign key enforces that every run belongs to one.
func ensureSessionsForRuns(t *testing.T, db *sql.DB, sessionIDs ...string) {
	t.Helper()
	store := NewSessionStore(db, "main")
	for _, sid := range sessionIDs {
		if err := store.Ensure(context.Background(), sid, sid); err != nil {
			t.Fatal(err)
		}
	}
}

// --- Session exclusivity, owner leases, and abandonment ---------------------
//
// Two RunStores on one database file are two processes: the exclusivity rule
// must hold across them, not just inside one store.

// newTwoOwnerStores opens one database with two owned stores, the sessions
// named in sids already created.
func newTwoOwnerStores(t *testing.T, sids ...string) (*RunStore, *RunStore, *sql.DB) {
	t.Helper()
	ctx := context.Background()
	db, err := OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ensureSessionsForRuns(t, db, sids...)
	return &RunStore{DB: db, Owner: "owner-a"}, &RunStore{DB: db, Owner: "owner-b"}, db
}

// ageOwnerLease moves an owner's heartbeat far enough back that its runs read
// as abandoned.
func ageOwnerLease(t *testing.T, db *sql.DB, owner string) {
	t.Helper()
	_, err := db.Exec(`UPDATE fb_run_owners SET heartbeat_at_ms=? WHERE owner=?`,
		time.Now().UnixMilli()-RunOwnerLease.Milliseconds()-5000, owner)
	if err != nil {
		t.Fatal(err)
	}
}

func TestCreateRunRefusesASessionWithALiveRun(t *testing.T) {
	ctx := context.Background()
	a, b, _ := newTwoOwnerStores(t, "s1", "s2")
	stop, err := a.HoldOwnerLease(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if _, err := a.CreateRun(ctx, "s1", "hello"); err != nil {
		t.Fatal(err)
	}
	_, err = b.CreateRun(ctx, "s1", "again")
	if !errors.Is(err, ErrSessionBusy) {
		t.Fatalf("second owner's run refused with %v, want the session-busy family", err)
	}
	if !errors.Is(err, ErrSessionRunning) {
		t.Fatalf("refusal = %v, want ErrSessionRunning", err)
	}
	const want = "This conversation is already running a turn; send again when it finishes."
	if err.Error() != want {
		t.Fatalf("refusal text = %q, want %q", err.Error(), want)
	}
	if code := SessionBusyCode(err); code != "session_running" {
		t.Fatalf("SessionBusyCode = %q, want session_running", code)
	}
	// Another session is untouched: the rule is per conversation.
	if _, err := b.CreateRun(ctx, "s2", "fresh conversation"); err != nil {
		t.Fatalf("run in another session: %v", err)
	}
	live, err := b.SessionHasLiveRun(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if !live {
		t.Fatal("SessionHasLiveRun must see the run that blocks s1")
	}
}

func TestCreateRunRefusesASessionParkedOnApproval(t *testing.T) {
	ctx := context.Background()
	a, b, _ := newTwoOwnerStores(t, "s1")
	stop, err := a.HoldOwnerLease(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	run, err := a.CreateRun(ctx, "s1", "parked")
	if err != nil {
		t.Fatal(err)
	}
	mustWaitAction(t, a.DB, "s1", "act-park")
	if err := a.SetWaitingAction(ctx, run.ID, Wait{ActionID: "act-park", ToolName: "shell", ToolInputJSON: "{}"}); err != nil {
		t.Fatal(err)
	}
	_, err = b.CreateRun(ctx, "s1", "while parked")
	if !errors.Is(err, ErrSessionBusy) || !errors.Is(err, ErrSessionAwaitingApproval) {
		t.Fatalf("refusal = %v, want ErrSessionAwaitingApproval", err)
	}
	const want = "This conversation is waiting for an approval; answer it before sending another message."
	if err.Error() != want {
		t.Fatalf("refusal text = %q, want %q", err.Error(), want)
	}
	if code := SessionBusyCode(err); code != "session_awaiting_approval" {
		t.Fatalf("SessionBusyCode = %q, want session_awaiting_approval", code)
	}
}

func TestCreateRunIgnoresARunWhoseOwnerIsGone(t *testing.T) {
	ctx := context.Background()
	a, b, _ := newTwoOwnerStores(t, "s1")
	stop, err := a.HoldOwnerLease(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	stale, err := a.CreateRun(ctx, "s1", "from a process that died")
	if err != nil {
		t.Fatal(err)
	}
	ageOwnerLease(t, a.DB, "owner-a")
	if _, err := b.CreateRun(ctx, "s1", "after the crash"); err != nil {
		t.Fatalf("a run whose owner's lease lapsed must not block: %v", err)
	}
	// The dead run stays running until a reaper ends it.
	r, err := b.GetRun(ctx, stale.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != RunStatusRunning {
		t.Fatalf("stale run status = %q, want running until reaped", r.Status)
	}
}

func TestCreateRunIsAtomicAcrossStores(t *testing.T) {
	ctx := context.Background()
	const attempts = 50
	a, b, _ := newTwoOwnerStores(t, "race")
	for _, s := range []*RunStore{a, b} {
		stop, err := s.HoldOwnerLease(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer stop()
	}
	var mu sync.Mutex
	maxLive := 0
	countLive := func() int {
		var n int
		if err := a.DB.QueryRow(`SELECT COUNT(*) FROM fb_runs WHERE session_id='race' AND parent_run_id IS NULL AND status='running'`).Scan(&n); err != nil {
			// Runs on the worker goroutines: fail the test without the
			// goroutine-unsafe runtime.Goexit of t.Fatal.
			t.Errorf("count live runs: %v", err)
			return 0
		}
		return n
	}
	successes := map[string]int{}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for name, store := range map[string]*RunStore{"a": a, "b": b} {
		wg.Add(1)
		go func(name string, store *RunStore) {
			defer wg.Done()
			<-start
			for range attempts {
				r, err := store.CreateRun(ctx, "race", "concurrent turn")
				if err != nil {
					if !errors.Is(err, ErrSessionBusy) {
						t.Errorf("unexpected create error: %v", err)
					}
					// A tight retry can starve the holder's SetStatus out of
					// the sqlite write lock; yield so it can finish.
					time.Sleep(time.Millisecond)
					continue
				}
				mu.Lock()
				if n := countLive(); n > maxLive {
					maxLive = n
				}
				successes[name]++
				mu.Unlock()
				if err := store.SetStatus(ctx, r.ID, RunStatusDone); err != nil {
					t.Errorf("set done: %v", err)
				}
				// The fairness premise ("both owners must win sometimes")
				// needs the winner to leave a window for the other owner's
				// next CreateRun instead of re-taking the slot immediately.
				time.Sleep(time.Millisecond)
			}
		}(name, store)
	}
	close(start)
	wg.Wait()
	if maxLive != 1 {
		t.Fatalf("session held %d live primary runs at once, want 1", maxLive)
	}
	if successes["a"] == 0 || successes["b"] == 0 {
		t.Fatalf("both owners must win sometimes, got %v", successes)
	}
}

func TestReapAbandonedRunsEndsOnlyTheDead(t *testing.T) {
	ctx := context.Background()
	a, b, db := newTwoOwnerStores(t, "s-alive", "s-stale", "s-stopped", "s-fenced")
	// A third owner keeps the "alive" run's lease independent of owner-a's,
	// which the test ages on purpose.
	c := &RunStore{DB: db, Owner: "owner-c"}
	aliveStop, err := c.HoldOwnerLease(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer aliveStop()
	if _, err := c.CreateRun(ctx, "s-alive", "still driving"); err != nil {
		t.Fatal(err)
	}
	stale, err := a.CreateRun(ctx, "s-stale", "owner vanished")
	if err != nil {
		t.Fatal(err)
	}
	ageOwnerLease(t, db, "owner-a")
	stoppedStop, err := b.HoldOwnerLease(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stopped, err := b.CreateRun(ctx, "s-stopped", "owner deregistered")
	if err != nil {
		t.Fatal(err)
	}
	stoppedStop()
	fenced, err := a.CreateRun(ctx, "s-fenced", "mid continuation")
	if err != nil {
		t.Fatal(err)
	}
	mustWaitAction(t, db, "s-fenced", "act-fence")
	if err := a.SetWaitingAction(ctx, fenced.ID, Wait{ActionID: "act-fence", ToolName: "shell", ToolInputJSON: "{}", ResumeOwner: "owner-a", ResumeClaimedAt: time.Now().UnixMilli(), ResumePhase: WaitResumePhaseExecutionStarted}); err != nil {
		t.Fatal(err)
	}
	// The continuation crossed its fence and the driver died: the run reads
	// running again while its wait row survives.
	if _, err := db.Exec(`UPDATE fb_runs SET status='running' WHERE id=?`, fenced.ID); err != nil {
		t.Fatal(err)
	}
	ageOwnerLease(t, db, "owner-a")

	reaped, err := b.ReapAbandonedRuns(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(reaped) != 2 {
		t.Fatalf("reaped %d runs, want exactly the stale and the deregistered one: %+v", len(reaped), reaped)
	}
	got := map[string]bool{}
	for _, r := range reaped {
		got[r.ID] = true
	}
	if !got[stale.ID] || !got[stopped.ID] || got[fenced.ID] {
		t.Fatalf("reaped the wrong runs: %+v", reaped)
	}
	for _, id := range []string{stale.ID, stopped.ID} {
		var status string
		var started, finished, worked int64
		if err := db.QueryRow(`SELECT status, started_at_ms, finished_at_ms, worked_ms FROM fb_runs WHERE id=?`, id).
			Scan(&status, &started, &finished, &worked); err != nil {
			t.Fatal(err)
		}
		if status != string(RunStatusFailed) {
			t.Fatalf("reaped run %s status = %q, want failed", id, status)
		}
		if started <= 0 || finished < started || worked != finished-started {
			t.Fatalf("reaped run %s clock = started %d finished %d worked %d", id, started, finished, worked)
		}
	}
	for _, id := range []string{fenced.ID} {
		var status string
		if err := db.QueryRow(`SELECT status FROM fb_runs WHERE id=?`, id).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status != string(RunStatusRunning) {
			t.Fatalf("fenced run %s was reaped; the continuation recovery owns it", id)
		}
	}
	// The fenced run itself is untouched, but it no longer vouches for the
	// session either: its owner's lease lapsed, so a fresh turn may start
	// while the continuation recovery decides the fence's outcome.
	if live, err := b.SessionHasLiveRun(ctx, "s-fenced"); err != nil || live {
		t.Fatalf("fenced session with a lapsed owner must not read live: %v %v", live, err)
	}
	// The reap is a compare-and-swap: a second pass finds nothing.
	if again, err := b.ReapAbandonedRuns(ctx, time.Now()); err != nil || len(again) != 0 {
		t.Fatalf("second reap = %+v, %v; want nothing", again, err)
	}
}

// TestReapAbandonedRunsTakesARunWhoseClockWasStampedBeforeTheStatus pins
// the crash window between StampRunTiming and the terminal status write: a
// running row whose clock a surface already stamped is reaped like any
// other dead one, keeping the clock it measured, while an unstamped row
// keeps the lease-fallback clock exactly as before. Both endings flow out
// the one branch the reaper's event publication reads.
func TestReapAbandonedRunsTakesARunWhoseClockWasStampedBeforeTheStatus(t *testing.T) {
	ctx := context.Background()
	a, b, db := newTwoOwnerStores(t, "s-stamped", "s-unstamped")
	stamped, err := a.CreateRun(ctx, "s-stamped", "clock stamped, then the process died")
	if err != nil {
		t.Fatal(err)
	}
	unstamped, err := a.CreateRun(ctx, "s-unstamped", "no clock at all")
	if err != nil {
		t.Fatal(err)
	}
	// The stamp is written when the run ends; the process died between it
	// and the status write, so the row sits there running and timed.
	startedAt := time.Now().Add(-2 * time.Minute).Round(time.Millisecond)
	finishedAt := time.Now().Add(-1 * time.Minute).Round(time.Millisecond)
	if err := NewSessionStore(db, "main").StampRunTiming(ctx, stamped.ID, RunTiming{
		StartedAt: startedAt, FinishedAt: finishedAt, Worked: time.Minute,
	}); err != nil {
		t.Fatal(err)
	}
	ageOwnerLease(t, db, "owner-a")

	reaped, err := b.ReapAbandonedRuns(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	reapedIDs := map[string]bool{}
	for _, r := range reaped {
		reapedIDs[r.ID] = true
	}
	if !reapedIDs[stamped.ID] || !reapedIDs[unstamped.ID] {
		t.Fatalf("reaped = %+v, want both the stamped and the unstamped run", reaped)
	}

	var status string
	var started, finished, worked int64
	if err := db.QueryRow(`SELECT status, started_at_ms, finished_at_ms, worked_ms FROM fb_runs WHERE id=?`, stamped.ID).
		Scan(&status, &started, &finished, &worked); err != nil {
		t.Fatal(err)
	}
	if status != string(RunStatusFailed) {
		t.Fatalf("stamped run status = %q, want failed", status)
	}
	if started != startedAt.UnixMilli() || finished != finishedAt.UnixMilli() || worked != int64(time.Minute/time.Millisecond) {
		t.Fatalf("stamped run clock = started %d finished %d worked %d, want the surface's own %d %d %d",
			started, finished, worked, startedAt.UnixMilli(), finishedAt.UnixMilli(), int64(time.Minute/time.Millisecond))
	}

	if err := db.QueryRow(`SELECT status, started_at_ms, finished_at_ms, worked_ms FROM fb_runs WHERE id=?`, unstamped.ID).
		Scan(&status, &started, &finished, &worked); err != nil {
		t.Fatal(err)
	}
	if status != string(RunStatusFailed) {
		t.Fatalf("unstamped run status = %q, want failed", status)
	}
	if started != unstamped.CreatedAt*1000 || finished < started || worked != finished-started {
		t.Fatalf("unstamped run clock = started %d finished %d worked %d, want the lease fallback from created_at %d",
			started, finished, worked, unstamped.CreatedAt*1000)
	}

	// The ending the reaper publishes for the reaped run reads back through
	// the same listing a fire's settlement reads.
	if _, err := b.AppendSessionEvent(ctx, SessionEvent{
		ID: "evt-reap-stamped", RunID: stamped.ID, SessionID: "s-stamped",
		Type: event.RunEventTurnError,
		Payload: event.EncodePayload(event.TurnErrorPayload{
			Error: "stopped", Message: "stopped", Detail: &event.TurnErrorDetail{Code: "run_abandoned"},
		}),
	}); err != nil {
		t.Fatal(err)
	}
	evts, err := b.ListRunEventsOfTypes(ctx, stamped.ID, event.RunEventTurnError, event.RunEventTurnCancelled)
	if err != nil {
		t.Fatal(err)
	}
	if len(evts) != 1 {
		t.Fatalf("ending events of the reaped run = %d, want the one published ending", len(evts))
	}
}

// TestReapAbandonedRunsCleansLapsedLeaseRows pins the reap's second duty:
// a lease that reads as dead is removed for real, so the table collects no
// one row per process that ever crashed mid-run. A fresh lease — the
// reaper's own — stays, and a voluntarily deregistered lease stays gone.
func TestReapAbandonedRunsCleansLapsedLeaseRows(t *testing.T) {
	ctx := context.Background()
	a, b, db := newTwoOwnerStores(t, "s-lease")

	// Voluntary deregistration keeps removing its row on the spot.
	ghost := &RunStore{DB: db, Owner: "owner-ghost"}
	stopGhost, err := ghost.HoldOwnerLease(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stopGhost()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM fb_run_owners WHERE owner='owner-ghost'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("a voluntarily deregistered lease left its row behind")
	}

	// A lapsed lease no run points at any more: the leftover this reaping is
	// for. Written without a renewal loop so it stays lapsed.
	if _, err := db.Exec(`INSERT INTO fb_run_owners(owner, heartbeat_at_ms) VALUES('owner-lapsed', ?)`,
		time.Now().UnixMilli()-RunOwnerLease.Milliseconds()-5000); err != nil {
		t.Fatal(err)
	}
	run, err := a.CreateRun(ctx, "s-lease", "died mid-run")
	if err != nil {
		t.Fatal(err)
	}
	ageOwnerLease(t, db, "owner-a")
	stopFresh, err := b.HoldOwnerLease(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer stopFresh()

	reaped, err := b.ReapAbandonedRuns(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(reaped) != 1 || reaped[0].ID != run.ID {
		t.Fatalf("reaped = %+v, want exactly the lapsed owner's run", reaped)
	}
	for owner, want := range map[string]int{"owner-a": 0, "owner-lapsed": 0, "owner-b": 1} {
		if err := db.QueryRow(`SELECT COUNT(*) FROM fb_run_owners WHERE owner=?`, owner).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != want {
			t.Fatalf("lease rows of %s = %d, want %d", owner, n, want)
		}
	}
}

func TestRunStatusEndsOnce(t *testing.T) {
	ctx := context.Background()
	a, b, _ := newTwoOwnerStores(t, "s1")
	done, err := a.CreateRun(ctx, "s1", "finished")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.SetStatus(ctx, done.ID, RunStatusDone); err != nil {
		t.Fatal(err)
	}
	if err := a.SetStatus(ctx, done.ID, RunStatusRunning); err != nil {
		t.Fatal(err)
	}
	if err := a.SetStatus(ctx, done.ID, RunStatusFailed); err != nil {
		t.Fatal(err)
	}
	if r, _ := a.GetRun(ctx, done.ID); r.Status != RunStatusDone {
		t.Fatalf("done run moved to %q", r.Status)
	}
	failed, err := a.CreateRun(ctx, "s1", "failed turn")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.SetStatus(ctx, failed.ID, RunStatusFailed); err != nil {
		t.Fatal(err)
	}
	if err := a.SetStatus(ctx, failed.ID, RunStatusDone); err != nil {
		t.Fatal(err)
	}
	if r, _ := a.GetRun(ctx, failed.ID); r.Status != RunStatusFailed {
		t.Fatalf("failed run moved to %q", r.Status)
	}
	parked, err := a.CreateRun(ctx, "s1", "parked turn")
	if err != nil {
		t.Fatal(err)
	}
	mustWaitAction(t, a.DB, "s1", "act-once")
	if err := a.SetWaitingAction(ctx, parked.ID, Wait{ActionID: "act-once", ToolName: "shell", ToolInputJSON: "{}"}); err != nil {
		t.Fatal(err)
	}
	if err := b.SetStatus(ctx, parked.ID, RunStatusRunning); err != nil {
		t.Fatal(err)
	}
	var owner string
	if err := a.DB.QueryRow(`SELECT owner FROM fb_runs WHERE id=?`, parked.ID).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if owner != "owner-b" {
		t.Fatalf("resumed run owner = %q, want the resumer's", owner)
	}
}

func TestOwnerLeaseRegistersAndLeaves(t *testing.T) {
	ctx := context.Background()
	a, _, db := newTwoOwnerStores(t)
	stop, err := a.HoldOwnerLease(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM fb_run_owners WHERE owner=?`, "owner-a").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("owner rows after HoldOwnerLease = %d, want 1", n)
	}
	stop()
	if err := db.QueryRow(`SELECT COUNT(*) FROM fb_run_owners WHERE owner=?`, "owner-a").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("owner rows after stop = %d, want 0", n)
	}
	stop() // idempotent
	bare := &RunStore{DB: db}
	if _, err := bare.HoldOwnerLease(ctx); err == nil {
		t.Fatal("a store without an owner must refuse to hold a lease")
	}
}

// TestFirstPrimaryRunIDReturnsTheEarliestPrimaryRun pins the read settling a
// fire rests on: the first primary run of a fire's session is the fire's run,
// however many runs the conversation later holds and however many subagent
// runs branched off it.
func TestFirstPrimaryRunIDReturnsTheEarliestPrimaryRun(t *testing.T) {
	ctx := context.Background()
	rt, _, _ := newTwoOwnerStores(t, "s-fire")
	stop, err := rt.HoldOwnerLease(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	first, err := rt.CreateRun(ctx, "s-fire", "the fire's prompt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.CreateSubagentRun(ctx, first.ID, "s-fire", "a child's task"); err != nil {
		t.Fatalf("CreateSubagentRun: %v", err)
	}
	if err := rt.SetStatus(ctx, first.ID, RunStatusDone); err != nil {
		t.Fatal(err)
	}
	second, err := rt.CreateRun(ctx, "s-fire", "the person keeps chatting")
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID {
		t.Fatal("the second run must be a different run")
	}

	got, err := rt.FirstPrimaryRunID(ctx, "s-fire")
	if err != nil {
		t.Fatal(err)
	}
	if got != first.ID {
		t.Fatalf("first primary run = %q, want the fire's run %q", got, first.ID)
	}
	if got, err := rt.FirstPrimaryRunID(ctx, "s-no-runs"); err != nil || got != "" {
		t.Fatalf("a session with no primary run = %q (%v), want empty", got, err)
	}
}
