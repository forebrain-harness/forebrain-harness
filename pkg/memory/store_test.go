package memory

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	state "github.com/forebrain-harness/forebrain-harness/pkg/state"
)

const testAgentID = "main"

// testCwd is the launch directory every test thread is inserted with, so
// every thread inserted through insertMemoryThread(ForAgent) belongs to the
// same project scope by default — the common case these tests exercise.
// Project-isolation tests that specifically need two projects construct
// distinct cwds of their own instead of using this helper.
const testCwd = "/project"

// testProjectKey is the project scope every thread inserted through
// insertMemoryThread(ForAgent) belongs to.
var testProjectKey = ProjectKey(testCwd)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return newTestStoreForAgent(t, testAgentID)
}

func newTestStoreForAgent(t *testing.T, agentID string) *Store {
	t.Helper()
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("open test state: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewStore(db, agentID)
}

func insertMemoryThread(t *testing.T, store *Store, id, mode, source string, updatedAt int64) {
	t.Helper()
	insertMemoryThreadForAgent(t, store, store.AgentID(), id, mode, source, updatedAt)
}

func insertMemoryThreadForAgent(t *testing.T, store *Store, agentID, id, mode, source string, updatedAt int64) {
	t.Helper()
	_, err := store.DB.ExecContext(context.Background(), `INSERT INTO fb_sessions(
		id,agent_id,title,updated_at,created_at,memory_mode,memory_source,cwd,git_branch
	) VALUES(?,?,?,?,?,?,?,?,?)`, id, agentID, id, updatedAt, updatedAt-1, mode, source, testCwd, "branch/"+id)
	if err != nil {
		t.Fatalf("insert thread %s: %v", id, err)
	}
}

func TestStage1OutputSelectionAndUsage(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().Unix()
	insertMemoryThread(t, store, "thread-a", ThreadMemoryEnabled, SessionSourceTUI, now-7200)
	if _, err := store.DB.ExecContext(ctx, `UPDATE fb_sessions SET git_branch='feature/memory' WHERE id='thread-a'`); err != nil {
		t.Fatal(err)
	}
	out := Stage1Output{ThreadID: "thread-a", ProjectKey: testProjectKey, SourceUpdatedAt: now - 7200, RawMemory: "raw", RolloutSummary: "summary", GeneratedAt: now - 7100}
	if err := store.UpsertStage1Output(ctx, out); err != nil {
		t.Fatal(err)
	}
	rows, err := store.SelectStage1ForPhase2(ctx, testProjectKey, 10, 30)
	if err != nil || len(rows) != 1 || rows[0].ThreadID != "thread-a" {
		t.Fatalf("rows=%#v err=%v", rows, err)
	}
	if rows[0].RolloutPath != "" || rows[0].Cwd != testCwd || rows[0].GitBranch != "feature/memory" {
		t.Fatalf("metadata = %#v", rows[0])
	}
	if err := store.UpdateUsage(ctx, []string{"thread-a"}, now); err != nil {
		t.Fatal(err)
	}
	rows, _ = store.SelectStage1ForPhase2(ctx, testProjectKey, 10, 30)
	if !rows[0].UsageCount.Valid || rows[0].UsageCount.Int64 != 1 || rows[0].LastUsage.Int64 != now {
		t.Fatalf("usage = %#v", rows[0])
	}

	out.SourceUpdatedAt = now - 8000
	out.RawMemory = "stale"
	if err := store.UpsertStage1Output(ctx, out); err != nil {
		t.Fatal(err)
	}
	rows, _ = store.SelectStage1ForPhase2(ctx, testProjectKey, 10, 30)
	if rows[0].RawMemory != "raw" {
		t.Fatalf("older output replaced current output: %#v", rows[0])
	}
}

func TestSelectStage1RanksThenReturnsStableThreadOrder(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().Unix()
	for _, id := range []string{"c", "a", "b", "disabled", "empty"} {
		mode := ThreadMemoryEnabled
		if id == "disabled" {
			mode = ThreadMemoryDisabled
		}
		insertMemoryThread(t, store, id, mode, SessionSourceTUI, now-100)
		raw := id
		if id == "empty" {
			raw = ""
		}
		if err := store.UpsertStage1Output(ctx, Stage1Output{ThreadID: id, ProjectKey: testProjectKey, SourceUpdatedAt: now - 100, RawMemory: raw}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.DB.ExecContext(ctx, `UPDATE fb_memory_stage1_outputs SET usage_count=5 WHERE thread_id IN ('b','c')`); err != nil {
		t.Fatal(err)
	}
	rows, err := store.SelectStage1ForPhase2(ctx, testProjectKey, 2, 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].ThreadID != "b" || rows[1].ThreadID != "c" {
		t.Fatalf("rows = %#v", rows)
	}
}

func TestClaimStage1FiltersThreadsAndHonorsOwnership(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().Unix()
	insertMemoryThread(t, store, "eligible-new", ThreadMemoryEnabled, SessionSourceTUI, now-7*3600)
	insertMemoryThread(t, store, "eligible-old", ThreadMemoryEnabled, SessionSourceWebchat, now-8*3600)
	insertMemoryThread(t, store, "current", ThreadMemoryEnabled, SessionSourceTUI, now-9*3600)
	insertMemoryThread(t, store, "disabled", ThreadMemoryDisabled, SessionSourceTUI, now-10*3600)
	insertMemoryThread(t, store, "unsupported", ThreadMemoryEnabled, "external", now-11*3600)
	insertMemoryThread(t, store, "recent", ThreadMemoryEnabled, SessionSourceTUI, now-60)
	insertMemoryThread(t, store, "expired", ThreadMemoryEnabled, SessionSourceTUI, now-11*24*3600)

	claims, err := store.ClaimStage1JobsForStartup(ctx, "current", 10, 6, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 2 || claims[0].Thread.ThreadID != "eligible-new" || claims[1].Thread.ThreadID != "eligible-old" {
		t.Fatalf("claims = %#v", claims)
	}
	if claims[0].Thread.GitBranch != "branch/eligible-new" || claims[1].Thread.GitBranch != "branch/eligible-old" {
		t.Fatalf("git branches = %#v", claims)
	}
	if updated, err := store.MarkStage1Succeeded(ctx, "eligible-new", testProjectKey, "wrong-token", now-7*3600, "raw", "summary", nil); err != nil || updated {
		t.Fatalf("wrong ownership updated=%v err=%v", updated, err)
	}
	if updated, err := store.MarkStage1Succeeded(ctx, "eligible-new", testProjectKey, claims[0].OwnershipToken, now-7*3600, "raw", "summary", nil); err != nil || !updated {
		t.Fatalf("owned success updated=%v err=%v", updated, err)
	}
	outcome, _, err := store.TryClaimStage1Job(ctx, "eligible-new", now-7*3600, 3600, 2)
	if err != nil || outcome != Stage1UpToDate {
		t.Fatalf("second claim outcome=%q err=%v", outcome, err)
	}
}

func TestNoOutputDeletesPriorOutputAndEnqueuesConsolidation(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().Unix()
	insertMemoryThread(t, store, "thread", ThreadMemoryEnabled, SessionSourceTUI, now-100)
	if err := store.UpsertStage1Output(ctx, Stage1Output{ThreadID: "thread", ProjectKey: testProjectKey, SourceUpdatedAt: now - 200, RawMemory: "old"}); err != nil {
		t.Fatal(err)
	}
	outcome, token, err := store.TryClaimStage1Job(ctx, "thread", now-100, 3600, 2)
	if err != nil || outcome != Stage1Claimed {
		t.Fatalf("claim=%q err=%v", outcome, err)
	}
	updated, err := store.MarkStage1SucceededNoOutput(ctx, "thread", token)
	if err != nil || !updated {
		t.Fatalf("no output updated=%v err=%v", updated, err)
	}
	var count int
	if err := store.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM fb_memory_stage1_outputs WHERE thread_id='thread'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("output count=%d err=%v", count, err)
	}
	if err := store.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM fb_memory_jobs WHERE kind=? AND job_key=?`, JobKindConsolidate, consolidateJobKey(store.AgentID(), Scope{Kind: ScopeProject, Key: testProjectKey})).Scan(&count); err != nil || count != 1 {
		t.Fatalf("consolidation jobs=%d err=%v", count, err)
	}
}

func TestGlobalConsolidationLockAndExactSelectionSnapshot(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().Unix()
	for _, id := range []string{"a", "b"} {
		insertMemoryThread(t, store, id, ThreadMemoryEnabled, SessionSourceTUI, now-100)
		if err := store.UpsertStage1Output(ctx, Stage1Output{ThreadID: id, ProjectKey: testProjectKey, SourceUpdatedAt: now - 100, RawMemory: id}); err != nil {
			t.Fatal(err)
		}
	}
	scope := Scope{Kind: ScopeProject, Key: testProjectKey}
	if err := store.EnqueueGlobalPhase2(ctx, scope, now); err != nil {
		t.Fatal(err)
	}
	claim, err := store.TryClaimGlobalPhase2(ctx, scope)
	if err != nil || claim.Outcome != Phase2Claimed {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}
	second, err := store.TryClaimGlobalPhase2(ctx, scope)
	if err != nil || second.Outcome != Phase2Running {
		t.Fatalf("second=%#v err=%v", second, err)
	}
	selected := []Stage1Output{{ThreadID: "b", ProjectKey: testProjectKey, SourceUpdatedAt: now - 100}}
	updated, err := store.MarkGlobalPhase2Succeeded(ctx, scope, claim.OwnershipToken, now, selected)
	if err != nil || !updated {
		t.Fatalf("success updated=%v err=%v", updated, err)
	}
	var selectedA, selectedB int
	if err := store.DB.QueryRowContext(ctx, `SELECT selected_for_phase2 FROM fb_memory_stage1_outputs WHERE thread_id='a'`).Scan(&selectedA); err != nil {
		t.Fatal(err)
	}
	if err := store.DB.QueryRowContext(ctx, `SELECT selected_for_phase2 FROM fb_memory_stage1_outputs WHERE thread_id='b'`).Scan(&selectedB); err != nil {
		t.Fatal(err)
	}
	if selectedA != 0 || selectedB != 1 {
		t.Fatalf("selected a=%d b=%d", selectedA, selectedB)
	}
}

func TestResetLeavesThreadsIntact(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().Unix()
	insertMemoryThread(t, store, "parent", ThreadMemoryDisabled, SessionSourceTUI, now)
	insertMemoryThread(t, store, "thread", threadMemoryPolluted, SessionSourceTUI, now)
	if _, err := store.DB.ExecContext(ctx, `UPDATE fb_sessions SET title='Preserved title',parent_session_id='parent',initial_window_id='window',cwd='/preserved',git_branch='feature/reset' WHERE id='thread'`); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertStage1Output(ctx, Stage1Output{ThreadID: "thread", ProjectKey: testProjectKey, SourceUpdatedAt: now, RawMemory: "raw"}); err != nil {
		t.Fatal(err)
	}
	if err := store.EnqueueGlobalPhase2(ctx, Scope{Kind: ScopeProject, Key: testProjectKey}, now); err != nil {
		t.Fatal(err)
	}
	if err := store.Reset(ctx); err != nil {
		t.Fatal(err)
	}
	var threadCount, outputCount, jobCount int
	for query, destination := range map[string]*int{
		`SELECT COUNT(*) FROM fb_sessions WHERE id='thread'`: &threadCount,
		`SELECT COUNT(*) FROM fb_memory_stage1_outputs`:      &outputCount,
		`SELECT COUNT(*) FROM fb_memory_jobs`:                &jobCount,
	} {
		if err := store.DB.QueryRowContext(ctx, query).Scan(destination); err != nil {
			t.Fatal(err)
		}
	}
	if threadCount != 1 || outputCount != 0 || jobCount != 0 {
		t.Fatalf("threads=%d outputs=%d jobs=%d", threadCount, outputCount, jobCount)
	}
	var title, parent, window, mode, source, cwd, branch string
	if err := store.DB.QueryRowContext(ctx, `SELECT title,parent_session_id,initial_window_id,memory_mode,memory_source,cwd,git_branch FROM fb_sessions WHERE id='thread'`).Scan(
		&title, &parent, &window, &mode, &source, &cwd, &branch,
	); err != nil {
		t.Fatal(err)
	}
	if title != "Preserved title" || parent != "parent" || window != "window" || mode != threadMemoryPolluted || source != SessionSourceTUI || cwd != "/preserved" || branch != "feature/reset" {
		t.Fatalf("reset changed session metadata: title=%q parent=%q window=%q mode=%q source=%q cwd=%q branch=%q", title, parent, window, mode, source, cwd, branch)
	}
}
