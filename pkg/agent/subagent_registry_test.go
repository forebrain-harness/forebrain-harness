package agent

import (
	"context"
	"path/filepath"
	"testing"
)

func finishRunning(t *testing.T, h *Handle, taskID string) {
	t.Helper()
	t.Cleanup(func() {
		h.Finish(HistoryEntry{TaskID: taskID, Status: StatusCancelled, UpdatedAt: 2})
	})
}

// The live index is process-global storage holding per-agent data. Roster and
// cancel paths issue queries with no session pinned ("cancel all agents" scans
// with Query{Limit: 500}), so scoping cannot rely on the query alone — one
// agent would otherwise both see and CANCEL another agent's in-flight work.
func TestRegistryScopesLiveEntriesByWorkspaceRoot(t *testing.T) {
	acme := RegistryFor(t.TempDir())
	globex := RegistryFor(t.TempDir())

	cancelled := false
	h := acme.Start(HistoryEntry{
		TaskID:    "acme-task",
		RunID:     "acme-run",
		SessionID: "s1",
		Task:      "acme's in-flight work",
		Status:    StatusRunning,
		UpdatedAt: 1,
	}, func() { cancelled = true })
	finishRunning(t, h, "acme-task")

	// The unscoped scan that cancelAllAgents performs.
	if got := globex.List(Query{Limit: 500}); len(got) != 0 {
		t.Fatalf("globex sees acme's in-flight subagents: %+v", got)
	}
	if _, ok := globex.Get(Query{TaskID: "acme-task"}); ok {
		t.Fatalf("globex resolved a handle owned by acme")
	}
	if globex.Cancel(Query{TaskID: "acme-task"}) || cancelled {
		t.Fatalf("globex cancelled acme's subagent")
	}

	// The owner must still reach its own work, or the scoping broke the feature.
	if got := acme.List(Query{Limit: 500}); len(got) != 1 {
		t.Fatalf("acme cannot see its own subagent: %+v", got)
	}
	if !acme.Cancel(Query{TaskID: "acme-task"}) || !cancelled {
		t.Fatalf("acme could not cancel its own subagent")
	}
}

// ListMerged must read the live index for the same root it reads the ledger
// for; otherwise the on-disk half is isolated and the in-memory half is not.
func TestListMergedReadsTheScopedLiveIndex(t *testing.T) {
	acmeRoot := t.TempDir()
	globexRoot := t.TempDir()

	h := RegistryFor(acmeRoot).Start(HistoryEntry{
		TaskID:    "acme-live",
		SessionID: "s1",
		Task:      "acme's in-flight work",
		Status:    StatusRunning,
		UpdatedAt: 1,
	}, func() {})
	finishRunning(t, h, "acme-live")

	got, err := ListMerged(globexRoot, Query{Limit: 500})
	if err != nil {
		t.Fatalf("ListMerged: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("globex's merged listing includes acme's live entry: %+v", got)
	}

	got, err = ListMerged(acmeRoot, Query{Limit: 500})
	if err != nil {
		t.Fatalf("ListMerged: %v", err)
	}
	if len(got) != 1 || got[0].TaskID != "acme-live" {
		t.Fatalf("owner's merged listing lost the live entry: %+v", got)
	}
}

// Same root must mean the same index, or writers and readers inside one agent
// would silently use different maps.
func TestRegistryForReturnsOneIndexPerRoot(t *testing.T) {
	root := t.TempDir()
	if RegistryFor(root) != RegistryFor(root) {
		t.Fatal("RegistryFor handed out two indexes for one root")
	}
	if RegistryFor(root) == RegistryFor(t.TempDir()) {
		t.Fatal("two roots share one index")
	}
}

// The writer (agentrun) and the readers (the TUI roster, the gateway) each
// resolve the workspace root themselves. Spellings of one root that the ledger
// treats as identical must reach the same live index too, or an agent's roster
// comes up empty while its subagents are running.
func TestRegistryForNormalizesEquivalentRoots(t *testing.T) {
	root := t.TempDir()
	for _, spelling := range []string{root + string(filepath.Separator), filepath.Join(root, "sub", "..")} {
		if RegistryFor(spelling) != RegistryFor(root) {
			t.Fatalf("%q resolved to a different index than %q", spelling, root)
		}
	}
	// AppendHistory refuses a relative root, so it has no ledger of its own and
	// must not mint a private index either.
	if RegistryFor("workspace") != RegistryFor("") {
		t.Fatal("a relative root got its own index instead of the unscoped bucket")
	}
}

func newTestRegistry() *Registry { return NewRegistry() }

// startCancelable registers a running handle with a real cancelable context and
// returns the handle plus a func reporting whether its context was cancelled.
func startCancelable(r *Registry, entry HistoryEntry) (*Handle, func() bool) {
	ctx, cancel := context.WithCancel(context.Background())
	h := r.Start(entry, cancel)
	cancelled := func() bool {
		select {
		case <-ctx.Done():
			return true
		default:
			return false
		}
	}
	return h, cancelled
}

func TestCancelEnforcesSessionScope(t *testing.T) {
	r := newTestRegistry()
	_, cancelled := startCancelable(r, HistoryEntry{
		TaskID:    "task-a",
		SessionID: "sess-1",
		Status:    StatusRunning,
	})

	// A query carrying a different session must not resolve or cancel the handle,
	// even though the task id matches.
	if r.Cancel(Query{TaskID: "task-a", SessionID: "sess-2"}) {
		t.Fatal("cancel succeeded across session boundary")
	}
	if cancelled() {
		t.Fatal("handle was cancelled by a foreign session")
	}

	// The owning session can cancel it.
	if !r.Cancel(Query{TaskID: "task-a", SessionID: "sess-1"}) {
		t.Fatal("owning session failed to cancel")
	}
	if !cancelled() {
		t.Fatal("handle context was not cancelled")
	}
}

func TestCancelEnforcesParentRunScope(t *testing.T) {
	r := newTestRegistry()
	_, cancelled := startCancelable(r, HistoryEntry{
		RunID:       "run-a",
		SessionID:   "sess-1",
		ParentRunID: "parent-1",
		Status:      StatusRunning,
	})

	if r.Cancel(Query{RunID: "run-a", ParentRunID: "parent-2"}) {
		t.Fatal("cancel succeeded across parent-run boundary")
	}
	if cancelled() {
		t.Fatal("handle was cancelled by a foreign parent run")
	}

	if !r.Cancel(Query{RunID: "run-a", ParentRunID: "parent-1"}) {
		t.Fatal("owning parent run failed to cancel")
	}
}

func TestCancelFinishedSubagentReturnsFalse(t *testing.T) {
	r := newTestRegistry()
	h, _ := startCancelable(r, HistoryEntry{
		TaskID:    "task-done",
		SessionID: "sess-1",
		Status:    StatusRunning,
	})

	// Finishing removes the handle from the live index.
	h.Finish(HistoryEntry{TaskID: "task-done", SessionID: "sess-1", Status: StatusOK})

	if r.Cancel(Query{TaskID: "task-done", SessionID: "sess-1"}) {
		t.Fatal("cancel reported success for an already-finished subagent")
	}
	if _, ok := r.Get(Query{TaskID: "task-done", SessionID: "sess-1"}); ok {
		t.Fatal("finished handle is still resolvable in the live index")
	}
}

func TestCancelRequiresExplicitID(t *testing.T) {
	r := newTestRegistry()
	startCancelable(r, HistoryEntry{
		TaskID:    "task-a",
		SessionID: "sess-1",
		Status:    StatusRunning,
	})

	// No task_id / run_id: close must not guess "the latest" subagent.
	if r.Cancel(Query{SessionID: "sess-1"}) {
		t.Fatal("cancel succeeded without an explicit task_id or run_id")
	}
	if _, ok := r.Get(Query{SessionID: "sess-1"}); ok {
		t.Fatal("Get resolved a handle without an explicit id")
	}
}

func TestFinishRemovesAllKeys(t *testing.T) {
	r := newTestRegistry()
	h, _ := startCancelable(r, HistoryEntry{
		TaskID:    "task-a",
		RunID:     "run-a",
		SessionID: "sess-1",
		Status:    StatusRunning,
	})

	h.Finish(HistoryEntry{TaskID: "task-a", RunID: "run-a", SessionID: "sess-1", Status: StatusOK})

	if _, ok := r.Get(Query{TaskID: "task-a", SessionID: "sess-1"}); ok {
		t.Fatal("task key not removed after finish")
	}
	if _, ok := r.Get(Query{RunID: "run-a", SessionID: "sess-1"}); ok {
		t.Fatal("run key not removed after finish")
	}
	r.mu.RLock()
	n := len(r.byKey)
	r.mu.RUnlock()
	if n != 0 {
		t.Fatalf("registry retained %d keys after finish, want 0", n)
	}
}

func TestStartReusedKeyNotClobberedByStaleRemove(t *testing.T) {
	r := newTestRegistry()
	h1, _ := startCancelable(r, HistoryEntry{TaskID: "task-x", SessionID: "sess-1", Status: StatusRunning})

	// A second Start reuses the same task id (e.g. id collision); it must take
	// over the index slot.
	h2, _ := startCancelable(r, HistoryEntry{TaskID: "task-x", SessionID: "sess-1", Status: StatusRunning})

	// Finishing the first handle must not evict the second handle's live entry.
	h1.Finish(HistoryEntry{TaskID: "task-x", SessionID: "sess-1", Status: StatusOK})

	got, ok := r.Get(Query{TaskID: "task-x", SessionID: "sess-1"})
	if !ok || got != h2 {
		t.Fatal("stale finish removed the live handle that reused the key")
	}
}
