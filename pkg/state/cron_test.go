package state

import (
	"context"
	"testing"
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
