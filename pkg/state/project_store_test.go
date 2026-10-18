package state

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

func newProjectStoreForTest(t *testing.T, agent string) (*ProjectStore, *SessionStore) {
	t.Helper()
	db, err := OpenStateForTest(context.Background(), filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewProjectStore(db, agent), NewSessionStore(db, agent)
}

func TestProjectStoreTenantIsolation(t *testing.T) {
	ctx := context.Background()
	a, _ := newProjectStoreForTest(t, "agent-a")
	b, _ := newProjectStoreForTest(t, "agent-b")
	// Both stores share one database in production; use A's db for B too.
	*b = *NewProjectStore(a.db, "agent-b")

	p, err := a.Create(ctx, CreateProjectInput{Name: "mine", Root: "/tmp/mine", ProjectKey: "-tmp-mine"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Get(ctx, p.ID); err == nil {
		t.Fatalf("agent B must not see agent A's project")
	}
	list, err := b.ListProjects(ctx, ListProjectsOptions{}, 50, 0)
	if err != nil || len(list) != 0 {
		t.Fatalf("agent B's list must be empty: %v %+v", err, list)
	}
}

func TestProjectCreateRejectsDuplicateRoot(t *testing.T) {
	ctx := context.Background()
	a, _ := newProjectStoreForTest(t, "main")
	first, err := a.Create(ctx, CreateProjectInput{Name: "one", Root: "/tmp/dup-root"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.Create(ctx, CreateProjectInput{Name: "two", Root: "/tmp/dup-root"})
	if !errors.Is(err, ErrProjectRootTaken) {
		t.Fatalf("duplicate root err = %v, want ErrProjectRootTaken", err)
	}
	if second.ID != first.ID {
		t.Fatalf("a conflict must hand back the existing project: %+v vs %+v", second, first)
	}
	list, _ := a.ListProjects(ctx, ListProjectsOptions{}, 50, 0)
	if len(list) != 1 {
		t.Fatalf("one project per root, got %d", len(list))
	}
}

// TestProjectFindOrCreateByRootIsIdempotent pins that two surfaces opening the
// same directory at once converge on a single project row.
func TestProjectFindOrCreateByRootIsIdempotent(t *testing.T) {
	ctx := context.Background()
	a, _ := newProjectStoreForTest(t, "main")
	root := t.TempDir()
	ids := make([]string, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p, _, err := a.FindOrCreateByRoot(ctx, root, CreateProjectInput{Name: "concurrent"})
			ids[i], errs[i] = p.ID, err
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if ids[0] != ids[1] {
		t.Fatalf("concurrent calls must converge on one project: %v vs %v", ids[0], ids[1])
	}
	list, _ := a.ListProjects(ctx, ListProjectsOptions{}, 50, 0)
	if len(list) != 1 {
		t.Fatalf("one row expected, got %d", len(list))
	}
	if _, created, err := a.FindOrCreateByRoot(ctx, root, CreateProjectInput{}); err != nil || created {
		t.Fatalf("a second call on a known root reports created=%v err=%v", created, err)
	}
}

// TestProjectDeleteRemovesItsConversations pins the new delete semantics: the
// project's conversations (and sub-sessions) go with it, not merely unbound.
func TestProjectDeleteRemovesItsConversations(t *testing.T) {
	ctx := context.Background()
	a, sessions := newProjectStoreForTest(t, "main")
	p, err := a.Create(ctx, CreateProjectInput{Name: "gone", Root: "/tmp/gone"})
	if err != nil {
		t.Fatal(err)
	}
	for _, sid := range []string{"sess-1", "child-1"} {
		if err := sessions.Ensure(ctx, sid, "title"); err != nil {
			t.Fatal(err)
		}
		if err := a.BindSession(ctx, sid, p.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := sessions.SetParentSessionID(ctx, "child-1", "sess-1"); err != nil {
		t.Fatal(err)
	}
	if err := a.Delete(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Get(ctx, p.ID); err == nil {
		t.Fatalf("deleted project still readable")
	}
	if exists, _ := sessions.HasSession(ctx, "sess-1"); exists {
		t.Fatalf("project deletion must remove its conversations")
	}
	if exists, _ := sessions.HasSession(ctx, "child-1"); exists {
		t.Fatalf("project deletion must remove its sub-sessions too")
	}
}

// TestProjectDeleteBusyWhileRunInFlight pins that a project with a run in
// flight is refused rather than torn out from under the run.
func TestProjectDeleteBusyWhileRunInFlight(t *testing.T) {
	ctx := context.Background()
	a, sessions := newProjectStoreForTest(t, "main")
	p, err := a.Create(ctx, CreateProjectInput{Name: "busy", Root: "/tmp/busy"})
	if err != nil {
		t.Fatal(err)
	}
	if err := sessions.Ensure(ctx, "sess-busy", "t"); err != nil {
		t.Fatal(err)
	}
	if err := a.BindSession(ctx, "sess-busy", p.ID); err != nil {
		t.Fatal(err)
	}
	rt := &RunStore{DB: a.db}
	if _, err := rt.CreateRun(ctx, "sess-busy", "hello"); err != nil {
		t.Fatal(err)
	}
	if err := a.Delete(ctx, p.ID); !errors.Is(err, ErrProjectBusy) {
		t.Fatalf("delete while a run is in flight = %v, want ErrProjectBusy", err)
	}
	if _, err := a.Get(ctx, p.ID); err != nil {
		t.Fatalf("project must survive a refused delete: %v", err)
	}
}

// TestProjectDeletePreviewCounts pins the counts the confirmation dialog
// shows: DeletePreview counts exactly the conversations the project's
// sessions tab lists (ListSessionsForProject's predicate), so a
// message-holding subagent child counts and an empty shell does not — the
// empty shells are deleted with the project too, but they were never
// conversations to the person reading the count. A scheduled task's fire
// holds messages yet is not a conversation, and its job is counted on its
// own line.
func TestProjectDeletePreviewCounts(t *testing.T) {
	ctx := context.Background()
	a, sessions := newProjectStoreForTest(t, "main")
	p, err := a.Create(ctx, CreateProjectInput{Name: "preview", Root: "/tmp/preview"})
	if err != nil {
		t.Fatal(err)
	}
	bind := func(id string) {
		t.Helper()
		if err := sessions.Ensure(ctx, id, "t"); err != nil {
			t.Fatal(err)
		}
		if err := a.BindSession(ctx, id, p.ID); err != nil {
			t.Fatal(err)
		}
	}
	// Two conversations the list shows: a main one and its subagent child.
	for _, id := range []string{"s1", "s2-child"} {
		bind(id)
		if _, err := sessions.Append(ctx, id, "user", "hello"); err != nil {
			t.Fatal(err)
		}
	}
	if err := sessions.SetParentSessionID(ctx, "s2-child", "s1"); err != nil {
		t.Fatal(err)
	}
	// Three empty shells: rows the surfaces ensure before a conversation
	// says anything. Deleted with the project, never counted.
	for _, id := range []string{"e1", "e2", "e3"} {
		bind(id)
	}
	// A scheduled task's fire: it holds messages, but the sessions tab
	// reaches it from the task's run record, not the conversation list.
	bind("fire")
	if _, err := sessions.Append(ctx, "fire", "user", "tick"); err != nil {
		t.Fatal(err)
	}
	if err := sessions.SetSessionSource(ctx, "fire", SessionSourceCron); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.ExecContext(ctx,
		`INSERT INTO fb_cron_jobs(id, agent_id, project_id, schedule, created_at, updated_at)
		 VALUES('job-1','main',?,'every 1h',1,1)`, p.ID); err != nil {
		t.Fatal(err)
	}
	sessionCount, cronJobs, running, err := a.DeletePreview(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if sessionCount != 2 || cronJobs != 1 || running {
		t.Fatalf("preview = %d sessions, %d jobs, running=%v; want 2, 1, false", sessionCount, cronJobs, running)
	}
}

func TestProjectListSortPinAndSearch(t *testing.T) {
	ctx := context.Background()
	a, _ := newProjectStoreForTest(t, "main")
	first, _ := a.Create(ctx, CreateProjectInput{Name: "alpha", Root: "/tmp/alpha"})
	second, _ := a.Create(ctx, CreateProjectInput{Name: "beta", Root: "/tmp/beta"})
	if err := a.SetPinned(ctx, second.ID, true); err != nil {
		t.Fatal(err)
	}
	list, _ := a.ListProjects(ctx, ListProjectsOptions{Sort: "name"}, 50, 0)
	if len(list) != 2 || list[0].ID != second.ID || list[1].ID != first.ID {
		t.Fatalf("pinned should lead: %+v", list)
	}
	list, _ = a.ListProjects(ctx, ListProjectsOptions{Search: "alp"}, 50, 0)
	if len(list) != 1 || list[0].ID != first.ID {
		t.Fatalf("search by name: %+v", list)
	}
	list, _ = a.ListProjects(ctx, ListProjectsOptions{Search: "/tmp/beta"}, 50, 0)
	if len(list) != 1 || list[0].ID != second.ID {
		t.Fatalf("search by root: %+v", list)
	}
}

// TestListProjectsPageKeepsPinnedFirstAndCountsTotal pins the paging contract
// the project table relies on: the pinned project leads whatever the sort key
// and direction are, the same search decides both the page and the total, and
// a page past the end reads the last page instead of an empty one.
func TestListProjectsPageKeepsPinnedFirstAndCountsTotal(t *testing.T) {
	ctx := context.Background()
	store, _ := newProjectStoreForTest(t, "main")
	alpha, err := store.Create(ctx, CreateProjectInput{Name: "alpha", Root: "/tmp/page-alpha"})
	if err != nil {
		t.Fatal(err)
	}
	beta, err := store.Create(ctx, CreateProjectInput{Name: "beta", Root: "/tmp/page-beta"})
	if err != nil {
		t.Fatal(err)
	}
	gamma, err := store.Create(ctx, CreateProjectInput{Name: "gamma", Root: "/tmp/page-gamma"})
	if err != nil {
		t.Fatal(err)
	}
	for _, stamp := range []struct {
		id      string
		updated int64
	}{{alpha.ID, 300}, {beta.ID, 100}, {gamma.ID, 200}} {
		if _, err := store.db.ExecContext(ctx, `UPDATE fb_projects SET updated_at=? WHERE id=?`, stamp.updated, stamp.id); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SetPinned(ctx, gamma.ID, true); err != nil {
		t.Fatal(err)
	}

	asc, desc := false, true
	for _, tc := range []struct {
		name string
		sort string
		desc *bool
		want []string
	}{
		{"name ascending", "name", &asc, []string{gamma.ID, alpha.ID, beta.ID}},
		{"name descending", "name", &desc, []string{gamma.ID, beta.ID, alpha.ID}},
		{"updated defaults to newest first", "", nil, []string{gamma.ID, alpha.ID, beta.ID}},
		{"updated oldest first", "updated", &asc, []string{gamma.ID, beta.ID, alpha.ID}},
	} {
		rows, total, err := store.ListProjectsPage(ctx, ListProjectsOptions{Sort: tc.sort, Desc: tc.desc}, 10, 0)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if total != 3 {
			t.Fatalf("%s: total = %d, want 3", tc.name, total)
		}
		got := make([]string, 0, len(rows))
		for _, row := range rows {
			got = append(got, row.ID)
		}
		if fmt.Sprint(got) != fmt.Sprint(tc.want) {
			t.Fatalf("%s: order = %v, want %v", tc.name, got, tc.want)
		}
	}

	// The search narrows the page and the total together.
	rows, total, err := store.ListProjectsPage(ctx, ListProjectsOptions{Search: "alp"}, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(rows) != 1 || rows[0].ID != alpha.ID {
		t.Fatalf("search page = %+v total %d, want only alpha", rows, total)
	}

	// A window smaller than the list counts every row, and an offset past the
	// end reads the last page.
	rows, total, err = store.ListProjectsPage(ctx, ListProjectsOptions{}, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || len(rows) != 2 {
		t.Fatalf("first window = %d rows, total %d; want 2 of 3", len(rows), total)
	}
	rows, total, err = store.ListProjectsPage(ctx, ListProjectsOptions{}, 2, 400)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || len(rows) != 1 || rows[0].ID != beta.ID {
		t.Fatalf("offset past the end = %+v total %d, want the last page", rows, total)
	}
}

func TestProjectUpdateEditableFieldsOnly(t *testing.T) {
	ctx := context.Background()
	a, _ := newProjectStoreForTest(t, "main")
	p, _ := a.Create(ctx, CreateProjectInput{Name: "orig", Root: "/tmp/orig", Instructions: "be careful", MemoryScope: "project_only", ResourceAccess: true})
	newName := "renamed"
	newScope := "shared"
	resourceOff := false
	updated, err := a.Update(ctx, p.ID, UpdateProjectInput{Name: &newName, MemoryScope: &newScope, ResourceAccess: &resourceOff})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "renamed" || updated.MemoryScope != "shared" || updated.ResourceAccess {
		t.Fatalf("update not applied: %+v", updated)
	}
	if updated.Root != "/tmp/orig" || updated.ProjectKey != p.ProjectKey {
		t.Fatalf("root and key are not editable: %+v", updated)
	}
	if updated.Instructions != "be careful" {
		t.Fatalf("untouched field changed: %+v", updated)
	}
	empty := ""
	if _, err := a.Update(ctx, p.ID, UpdateProjectInput{Name: &empty}); err == nil {
		t.Fatalf("empty name must be rejected")
	}
}

func TestProjectMemoryScopeNormalization(t *testing.T) {
	if NormalizeProjectMemoryScope("PROJECT_ONLY") != ProjectMemoryProjectOnly {
		t.Fatalf("scope normalization failed")
	}
	if NormalizeProjectMemoryScope("") != ProjectMemoryShared {
		t.Fatalf("empty scope defaults to shared")
	}
	if NormalizeProjectMemoryScope("nonsense") != ProjectMemoryShared {
		t.Fatalf("unknown scope defaults to shared")
	}
}

func TestProjectBindSessionRequiresOwnedProject(t *testing.T) {
	ctx := context.Background()
	a, sessions := newProjectStoreForTest(t, "main")
	other, _ := newProjectStoreForTest(t, "other")
	*other = *NewProjectStore(a.db, "other")
	foreign, _ := other.Create(ctx, CreateProjectInput{Name: "foreign", Root: "/tmp/foreign"})
	if err := sessions.Ensure(ctx, "sess-x", "t"); err != nil {
		t.Fatal(err)
	}
	if err := a.BindSession(ctx, "sess-x", foreign.ID); err == nil {
		t.Fatalf("binding another agent's project must fail")
	}
}

func TestListSessionsForProjectPages(t *testing.T) {
	ctx := context.Background()
	a, sessions := newProjectStoreForTest(t, "main")
	p, _ := a.Create(ctx, CreateProjectInput{Name: "paged", Root: "/tmp/paged"})
	other, _ := a.Create(ctx, CreateProjectInput{Name: "otherproj", Root: "/tmp/otherproj"})
	for i := 0; i < 3; i++ {
		sid := "sess-p" + string(rune('0'+i))
		if err := sessions.Ensure(ctx, sid, "t"); err != nil {
			t.Fatal(err)
		}
		// Both of the paged project's sessions hold a message: the list is of
		// conversations, and a row with no message is not one.
		if _, err := sessions.Append(ctx, sid, "user", "hello"); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.BindSession(ctx, "sess-p0", p.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.BindSession(ctx, "sess-p1", p.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.BindSession(ctx, "sess-p2", other.ID); err != nil {
		t.Fatal(err)
	}
	got, err := sessions.ListSessionsForProject(ctx, p.ID, 1, 0)
	if err != nil || len(got) != 1 {
		t.Fatalf("page 1: %v %+v", err, got)
	}
	page2, _ := sessions.ListSessionsForProject(ctx, p.ID, 1, 1)
	if len(page2) != 1 || page2[0].ID == got[0].ID {
		t.Fatalf("page 2 must differ: %+v vs %+v", got, page2)
	}
}
