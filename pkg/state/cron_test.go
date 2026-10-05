package state

import (
	"context"
	"testing"
	"time"
)

// TestSaveHeartbeatKeepsWhenItLastFired pins that replacing a heartbeat's
// setting — its interval, prompt or pause — leaves the scheduler's record of
// when it last fired alone: that is a fact about the conversation, not part of
// the setting the user just edited.
func TestSaveHeartbeatKeepsWhenItLastFired(t *testing.T) {
	ctx := context.Background()
	db := openSessionDB(t)
	if err := NewSessionStore(db, "main").Ensure(ctx, "s", "s"); err != nil {
		t.Fatal(err)
	}
	store := &CronStore{DB: db}
	next := int64(2000)
	if err := store.SaveHeartbeat(ctx, Heartbeat{SessionID: "s", IntervalSec: 600, Prompt: "check", NextRunAt: &next}); err != nil {
		t.Fatal(err)
	}
	fired := int64(1500)
	if _, err := store.ReanchorHeartbeat(ctx, "s", 2100, &fired); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveHeartbeat(ctx, Heartbeat{SessionID: "s", IntervalSec: 900, Prompt: "check again"}); err != nil {
		t.Fatal(err)
	}
	hb, err := store.GetHeartbeat(ctx, "s")
	if err != nil || hb == nil {
		t.Fatalf("GetHeartbeat = %v, %v", hb, err)
	}
	if hb.LastFiredAt == nil || *hb.LastFiredAt != fired {
		t.Fatalf("last fired after an edit = %v, want %d", hb.LastFiredAt, fired)
	}
	if hb.IntervalSec != 900 || hb.Prompt != "check again" || hb.NextRunAt != nil {
		t.Fatalf("edited heartbeat = %+v, want the new setting, paused", hb)
	}
}

// TestListJobsForProjectReadsUnboundJobsAsNoProject pins that the empty
// project names the agent's jobs bound to no project, which the table records
// as NULL rather than as an empty string.
func TestListJobsForProjectReadsUnboundJobsAsNoProject(t *testing.T) {
	ctx := context.Background()
	store := &CronStore{DB: openSessionDB(t)}
	if err := store.InsertJob(ctx, CronJob{ID: "job-free", Ag: "main", Sched: "every 1h"}); err != nil {
		t.Fatal(err)
	}
	jobs, err := store.ListJobsForProject(ctx, "main", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].ID != "job-free" {
		t.Fatalf("unbound jobs = %+v, want job-free", jobs)
	}
}

// TestFinishRunClosesAFireOnce pins the compare-and-swap at the heart of
// settling: a fire closes exactly once, so whichever closer gets there first —
// one scheduler's fast path, its next tick, or another replica — delivers the
// answer, and every other one writes nothing.
func TestFinishRunClosesAFireOnce(t *testing.T) {
	ctx := context.Background()
	store := &CronStore{DB: openSessionDB(t)}
	id, err := store.StartRun(ctx, CronRun{JobID: "job-1", AgentID: "main", SessionID: "cron-job-1-1790000000", Trigger: "schedule"})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	won, err := store.FinishRun(ctx, id, CronStatusOK, "the answer", "", "")
	if err != nil || !won {
		t.Fatalf("first close = %v (%v), want the winner", won, err)
	}
	won, err = store.FinishRun(ctx, id, CronStatusFailed, "a later pass", "", "")
	if err != nil {
		t.Fatalf("second close: %v", err)
	}
	if won {
		t.Fatal("a second close of the same fire must lose the compare-and-swap")
	}
	runs, err := store.ListRuns(ctx, "job-1", 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = %v (%v)", runs, err)
	}
	if runs[0].Status != CronStatusOK || runs[0].Output != "the answer" {
		t.Fatalf("the losing close overwrote the fire's ending: %+v", runs[0])
	}
}

// TestFireDeliveryMarksRecordWhatBecameOfTheAnswer pins that delivery is
// recorded after the fire closed: where the answer went, or why it could not
// be delivered — with a code for a surface to word in its own language — and
// that neither mark touches a fire that is still open.
func TestFireDeliveryMarksRecordWhatBecameOfTheAnswer(t *testing.T) {
	ctx := context.Background()
	store := &CronStore{DB: openSessionDB(t)}
	if err := store.InsertJob(ctx, CronJob{ID: "job-1", Ag: "main", Sched: "every 1h"}); err != nil {
		t.Fatal(err)
	}
	delivered, err := store.StartRun(ctx, CronRun{JobID: "job-1", AgentID: "main", SessionID: "cron-job-1-1", Trigger: "schedule"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishRun(ctx, delivered, CronStatusOK, "the answer", "", ""); err != nil {
		t.Fatal(err)
	}
	undelivered, err := store.StartRun(ctx, CronRun{JobID: "job-1", AgentID: "main", SessionID: "cron-job-1-2", Trigger: "schedule"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishRun(ctx, undelivered, CronStatusOK, "the answer", "", ""); err != nil {
		t.Fatal(err)
	}
	stillOpen, err := store.StartRun(ctx, CronRun{JobID: "job-1", AgentID: "main", SessionID: "cron-job-1-3", Trigger: "schedule"})
	if err != nil {
		t.Fatal(err)
	}

	if err := store.MarkFireDelivered(ctx, delivered, "telegram"); err != nil {
		t.Fatalf("MarkFireDelivered: %v", err)
	}
	if err := store.MarkFireDeliveryFailed(ctx, undelivered, "delivery_failed", "channel unreachable"); err != nil {
		t.Fatalf("MarkFireDeliveryFailed: %v", err)
	}
	// Neither mark may touch a fire that has not closed.
	if err := store.MarkFireDelivered(ctx, stillOpen, "telegram"); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkFireDeliveryFailed(ctx, stillOpen, "delivery_failed", "channel unreachable"); err != nil {
		t.Fatal(err)
	}

	runs, err := store.ListRuns(ctx, "job-1", 10)
	if err != nil || len(runs) != 3 {
		t.Fatalf("runs = %v (%v)", runs, err)
	}
	bySession := map[string]CronRun{}
	for _, r := range runs {
		bySession[r.SessionID] = r
	}
	if r := bySession["cron-job-1-1"]; r.DeliveredTo != "telegram" || r.Status != CronStatusOK {
		t.Fatalf("delivered fire = %+v", r)
	}
	if r := bySession["cron-job-1-2"]; r.Status != CronStatusDeliveryError || r.ErrorCode != "delivery_failed" || r.Error != "channel unreachable" {
		t.Fatalf("undelivered fire = %+v", r)
	}
	if r := bySession["cron-job-1-3"]; r.Status != CronStatusRunning || r.DeliveredTo != "" || r.Error != "" {
		t.Fatalf("an open fire must not take a delivery mark: %+v", r)
	}

	// The job's own summary carries the code beside the sentence, so a surface
	// can word the last failure in the viewer's language.
	if err := store.RecordOutcome(ctx, "job-1", CronStatusDeliveryError, "the answer", "channel unreachable", "delivery_failed", 1); err != nil {
		t.Fatal(err)
	}
	job, err := store.GetJob(ctx, "job-1")
	if err != nil || job == nil {
		t.Fatalf("GetJob: %v (%v)", job, err)
	}
	if job.LastErrorCode != "delivery_failed" || job.LastError != "channel unreachable" {
		t.Fatalf("job's summary = %+v, want the code beside the sentence", job)
	}
}

// TestOpenFireQueriesFindOnlyThisTenantsOpenFires pins the three reads
// settling and anti-stacking rest on: this tenant's open fires, the open fire
// of one session, and a job's open fire — all empty once the fire closed.
func TestOpenFireQueriesFindOnlyThisTenantsOpenFires(t *testing.T) {
	ctx := context.Background()
	store := &CronStore{DB: openSessionDB(t)}
	mine, err := store.StartRun(ctx, CronRun{JobID: "job-1", AgentID: "main", SessionID: "cron-job-1-1", Trigger: "schedule"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.StartRun(ctx, CronRun{JobID: "job-other", AgentID: "research", SessionID: "cron-job-other-1", Trigger: "schedule"}); err != nil {
		t.Fatal(err)
	}

	open, err := store.OpenFires(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 1 || open[0].SessionID != "cron-job-1-1" {
		t.Fatalf("open fires = %+v, want only this tenant's", open)
	}
	bySession, err := store.OpenFireForSession(ctx, "cron-job-1-1")
	if err != nil || bySession == nil || bySession.ID != mine {
		t.Fatalf("open fire for session = %v (%v)", bySession, err)
	}
	latest, err := store.LatestOpenFire(ctx, "job-1")
	if err != nil || latest == nil || latest.ID != mine {
		t.Fatalf("latest open fire = %v (%v)", latest, err)
	}
	// The session-keyed read is not the tenant boundary — the scheduler
	// settles only its own tenant's records — so it finds any session's open
	// fire, including another tenant's.
	if other, err := store.OpenFireForSession(ctx, "cron-job-other-1"); err != nil || other == nil {
		t.Fatalf("the open fire of another tenant's session = %v (%v)", other, err)
	}

	if _, err := store.FinishRun(ctx, mine, CronStatusOK, "done", "", ""); err != nil {
		t.Fatal(err)
	}
	if open, err := store.OpenFires(ctx, "main"); err != nil || len(open) != 0 {
		t.Fatalf("open fires after closing = %v (%v)", open, err)
	}
	if got, err := store.OpenFireForSession(ctx, "cron-job-1-1"); err != nil || got != nil {
		t.Fatalf("fire for session after closing = %v (%v)", got, err)
	}
	if got, err := store.LatestOpenFire(ctx, "job-1"); err != nil || got != nil {
		t.Fatalf("latest open fire after closing = %v (%v)", got, err)
	}
}

// TestListRunsReportsWhetherTheFireHasAConversation pins the flag a surface
// reads to decide whether a fire's record can be opened as a conversation:
// a session with a visible transcript has one, a session without does not.
func TestListRunsReportsWhetherTheFireHasAConversation(t *testing.T) {
	ctx := context.Background()
	db := openSessionDB(t)
	store := &CronStore{DB: db}
	sessions := NewSessionStore(db, "main")
	if err := sessions.Ensure(ctx, "cron-job-1-1", "cron-job-1-1"); err != nil {
		t.Fatal(err)
	}
	if err := sessions.Ensure(ctx, "cron-job-1-2", "cron-job-1-2"); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.Append(ctx, "cron-job-1-1", "user", "nightly summary"); err != nil {
		t.Fatal(err)
	}
	for _, sid := range []string{"cron-job-1-1", "cron-job-1-2"} {
		if _, err := store.StartRun(ctx, CronRun{JobID: "job-1", AgentID: "main", SessionID: sid, Trigger: "schedule"}); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := store.ListRuns(ctx, "job-1", 10)
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs = %v (%v)", runs, err)
	}
	for _, r := range runs {
		if r.SessionID == "cron-job-1-1" && !r.HasConversation {
			t.Fatalf("a session with a visible message must report a conversation: %+v", r)
		}
		if r.SessionID == "cron-job-1-2" && r.HasConversation {
			t.Fatalf("a session with no messages must report no conversation: %+v", r)
		}
	}
}

// TestExpiredFires pins who counts as past the retention: a quiet
// scheduled-task conversation, across every tenant — never one the user just
// talked in, never a fire still running, never one whose conversation a live
// run still holds. A record whose session is gone, or is an ordinary
// conversation, is expired as a record only: only source='cron' sessions are
// ever deleted with their fire.
func TestExpiredFires(t *testing.T) {
	ctx := context.Background()
	db := openSessionDB(t)
	store := &CronStore{DB: db}
	main := NewSessionStore(db, "main")
	second := NewSessionStore(db, "second")
	now := time.Now()
	old := now.Add(-40 * 24 * time.Hour).Unix()
	cutoff := now.Add(-30 * 24 * time.Hour)

	cronBirth := SessionBirth{Source: SessionSourceCron}
	seed := func(st *SessionStore, id, source string) {
		t.Helper()
		if err := st.EnsureAt(ctx, id, id, SessionBirth{Source: source}); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE fb_sessions SET updated_at=? WHERE id=?`, old, id); err != nil {
			t.Fatal(err)
		}
	}
	fire := func(sessionID string, close bool) int64 {
		t.Helper()
		id, err := store.StartRun(ctx, CronRun{JobID: "job", AgentID: "main", SessionID: sessionID, Trigger: "schedule"})
		if err != nil {
			t.Fatal(err)
		}
		if close {
			if _, err := store.FinishRun(ctx, id, CronStatusOK, "done", "", ""); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE fb_cron_runs SET started_at=?, finished_at=? WHERE id=?`, old, old, id); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}

	oldID := fire("cron-old", true)
	seed(main, "cron-old", SessionSourceCron)

	// The user went on talking in this one after the fire.
	fire("cron-recent", true)
	if err := main.EnsureAt(ctx, "cron-recent", "cron-recent", cronBirth); err != nil {
		t.Fatal(err)
	}

	// Still going: the fire has not closed.
	seed(main, "cron-open-fire", SessionSourceCron)
	fire("cron-open-fire", false)

	// The fire closed, but a live run still holds the conversation.
	seed(main, "cron-live-run", SessionSourceCron)
	fire("cron-live-run", true)
	driver := &RunStore{DB: db, Owner: "expired-fires-driver"}
	stopDriver, err := driver.HoldOwnerLease(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer stopDriver()
	if _, err := driver.CreateRun(ctx, "cron-live-run", "a later turn"); err != nil {
		t.Fatal(err)
	}

	// The session is gone; only the record remains.
	goneID := fire("cron-session-gone", true)

	// An ordinary conversation a record happens to point at.
	fire("cron-plain", true)
	seed(main, "cron-plain", SessionSourceConversation)

	// Another tenant's expired fire is swept too.
	otherID, err := store.StartRun(ctx, CronRun{JobID: "job-2", AgentID: "second", SessionID: "cron-other", Trigger: "schedule"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishRun(ctx, otherID, CronStatusOK, "done", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE fb_cron_runs SET started_at=?, finished_at=? WHERE id=?`, old, old, otherID); err != nil {
		t.Fatal(err)
	}
	seed(second, "cron-other", SessionSourceCron)

	fires, err := store.ExpiredFires(ctx, cutoff)
	if err != nil {
		t.Fatalf("ExpiredFires: %v", err)
	}
	bySession := make(map[string]ExpiredFire, len(fires))
	for _, f := range fires {
		bySession[f.SessionID] = f
	}
	want := map[string]bool{
		"cron-old":          true,
		"cron-session-gone": false,
		"cron-plain":        false,
		"cron-other":        true,
	}
	if len(fires) != len(want) {
		t.Fatalf("expired fires = %+v, want exactly %d", fires, len(want))
	}
	for sid, wantCron := range want {
		f, ok := bySession[sid]
		if !ok {
			t.Fatalf("session %s missing from the expired fires: %+v", sid, fires)
		}
		if f.CronSession != wantCron {
			t.Fatalf("session %s CronSession = %v, want %v", sid, f.CronSession, wantCron)
		}
	}
	for _, sid := range []string{"cron-recent", "cron-open-fire", "cron-live-run"} {
		if _, ok := bySession[sid]; ok {
			t.Fatalf("session %s must not be expired", sid)
		}
	}

	// The record ids line up, and deleting them leaves the living fires.
	if got := bySession["cron-old"].RecordID; got != oldID {
		t.Fatalf("cron-old record id = %d, want %d", got, oldID)
	}
	if got := bySession["cron-session-gone"].RecordID; got != goneID {
		t.Fatalf("cron-session-gone record id = %d, want %d", got, goneID)
	}
	ids := make([]int64, 0, len(fires))
	for _, f := range fires {
		ids = append(ids, f.RecordID)
	}
	if err := store.DeleteFireRecords(ctx, ids); err != nil {
		t.Fatalf("DeleteFireRecords: %v", err)
	}
	var left int
	if err := db.QueryRow(`SELECT COUNT(*) FROM fb_cron_runs`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 3 {
		t.Fatalf("records left after the delete = %d, want the 3 living fires", left)
	}
}
