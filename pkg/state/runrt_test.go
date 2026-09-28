package state

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
