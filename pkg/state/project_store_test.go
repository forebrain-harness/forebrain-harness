package state

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
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

// TestDefaultProjectTypeForRoot pins the launch default for a folder nobody
// has registered yet (owner 2026-10-27): coding anywhere inside a Git
// worktree, office elsewhere. The marker is the filesystem alone — a .git
// directory, or the .git file of a linked worktree — so an empty repository
// with no HEAD and a deep subdirectory both answer coding without ever
// running git.
func TestDefaultProjectTypeForRoot(t *testing.T) {
	plain := t.TempDir()
	if got, err := DefaultProjectTypeForRoot(plain); err != nil || got != ProjectTypeOffice {
		t.Fatalf("a non-Git directory must default to office, got %q err=%v", got, err)
	}
	gitRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(gitRoot, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := DefaultProjectTypeForRoot(gitRoot); err != nil || got != ProjectTypeCoding {
		t.Fatalf("a repository root must default to coding, got %q err=%v", got, err)
	}
	deep := filepath.Join(gitRoot, "a", "b", "c")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := DefaultProjectTypeForRoot(deep); err != nil || got != ProjectTypeCoding {
		t.Fatalf("a subdirectory of a repository must default to coding, got %q err=%v", got, err)
	}
	linked := t.TempDir()
	if err := os.WriteFile(filepath.Join(linked, ".git"), []byte("gitdir: ../elsewhere/.git/worktrees/x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := DefaultProjectTypeForRoot(linked); err != nil || got != ProjectTypeCoding {
		t.Fatalf("a linked worktree (.git file) must default to coding, got %q err=%v", got, err)
	}
	// An empty repository: the .git directory exists but nothing was ever
	// committed, so no HEAD exists to read.
	empty := t.TempDir()
	if err := os.Mkdir(filepath.Join(empty, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := DefaultProjectTypeForRoot(empty); err != nil || got != ProjectTypeCoding {
		t.Fatalf("an empty repository must default to coding, got %q err=%v", got, err)
	}
}

// TestProjectCreateDefaultsFromRootAndHonorsExplicitType pins the creation
// boundary: an omitted type takes the root-aware default, an explicit one
// always wins, and an unknown one is refused rather than quietly stored.
func TestProjectCreateDefaultsFromRootAndHonorsExplicitType(t *testing.T) {
	ctx := context.Background()
	a, _ := newProjectStoreForTest(t, "main")
	plain := t.TempDir()
	p, err := a.Create(ctx, CreateProjectInput{Name: "plain", Root: plain})
	if err != nil || p.ProjectType != ProjectTypeOffice {
		t.Fatalf("a non-Git root with no type must be office, got %q err=%v", p.ProjectType, err)
	}
	gitRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(gitRoot, ".git", "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(gitRoot, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	g, err := a.Create(ctx, CreateProjectInput{Name: "git", Root: sub})
	if err != nil || g.ProjectType != ProjectTypeCoding {
		t.Fatalf("a Git subdirectory with no type must be coding, got %q err=%v", g.ProjectType, err)
	}
	plainCoding, err := a.Create(ctx, CreateProjectInput{Name: "explicit", Root: filepath.Join(plain, "one"), ProjectType: ProjectTypeCoding})
	if err != nil || plainCoding.ProjectType != ProjectTypeCoding {
		t.Fatalf("explicit coding on a non-Git root must win, got %q err=%v", plainCoding.ProjectType, err)
	}
	gitOffice, err := a.Create(ctx, CreateProjectInput{Name: "office-in-git", Root: filepath.Join(gitRoot, "two"), ProjectType: ProjectTypeOffice})
	if err != nil || gitOffice.ProjectType != ProjectTypeOffice {
		t.Fatalf("explicit office on a Git root must win, got %q err=%v", gitOffice.ProjectType, err)
	}
	upper, err := a.Create(ctx, CreateProjectInput{Name: "upper", Root: filepath.Join(plain, "three"), ProjectType: "BATCH"})
	if err != nil || upper.ProjectType != ProjectTypeBatch {
		t.Fatalf("BATCH must normalize to batch, got %q err=%v", upper.ProjectType, err)
	}
	if _, err := a.Create(ctx, CreateProjectInput{Name: "bad", Root: filepath.Join(plain, "four"), ProjectType: "x"}); !errors.Is(err, ErrProjectTypeInvalid) {
		t.Fatalf("an unknown type must be refused with ErrProjectTypeInvalid, got %v", err)
	}
}

// TestFindOrCreateByRootDoesNotReclassifyExistingProject pins that the
// root-aware default runs only when a row is actually being inserted: a
// project registered before the folder grew its .git (or before the caller
// changed its mind) keeps the type it was born with.
func TestFindOrCreateByRootDoesNotReclassifyExistingProject(t *testing.T) {
	ctx := context.Background()
	a, _ := newProjectStoreForTest(t, "main")
	plain := t.TempDir()
	first, created, err := a.FindOrCreateByRoot(ctx, plain, CreateProjectInput{})
	if err != nil || !created || first.ProjectType != ProjectTypeOffice {
		t.Fatalf("first registration on a non-Git root: created=%v type=%q err=%v", created, first.ProjectType, err)
	}
	if err := os.Mkdir(filepath.Join(plain, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	again, created, err := a.FindOrCreateByRoot(ctx, plain, CreateProjectInput{})
	if err != nil || created {
		t.Fatalf("an existing project must be returned as-is: created=%v err=%v", created, err)
	}
	if again.ID != first.ID || again.ProjectType != ProjectTypeOffice {
		t.Fatalf("a grown .git must not reclassify the project: %+v", again)
	}
	explicit, created, err := a.FindOrCreateByRoot(ctx, plain, CreateProjectInput{ProjectType: ProjectTypeCoding})
	if err != nil || created || explicit.ProjectType != ProjectTypeOffice {
		t.Fatalf("an explicit type on a known root must not overwrite the stored one: created=%v type=%q err=%v", created, explicit.ProjectType, err)
	}
}

// TestProjectUpdateChangesType pins the explicit edit path: a stored type
// moves, an illegal value is refused and leaves the row untouched.
func TestProjectUpdateChangesType(t *testing.T) {
	ctx := context.Background()
	a, _ := newProjectStoreForTest(t, "main")
	p, err := a.Create(ctx, CreateProjectInput{Name: "typed", Root: t.TempDir(), ProjectType: ProjectTypeCoding})
	if err != nil {
		t.Fatal(err)
	}
	office := ProjectTypeOffice
	updated, err := a.Update(ctx, p.ID, UpdateProjectInput{ProjectType: &office})
	if err != nil || updated.ProjectType != ProjectTypeOffice {
		t.Fatalf("update must change the type, got %q err=%v", updated.ProjectType, err)
	}
	stored, err := a.Get(ctx, p.ID)
	if err != nil || stored.ProjectType != ProjectTypeOffice {
		t.Fatalf("get must read the changed type, got %q err=%v", stored.ProjectType, err)
	}
	bad := "x"
	if _, err := a.Update(ctx, p.ID, UpdateProjectInput{ProjectType: &bad}); !errors.Is(err, ErrProjectTypeInvalid) {
		t.Fatalf("an unknown type must be refused, got %v", err)
	}
	after, err := a.Get(ctx, p.ID)
	if err != nil || after.ProjectType != ProjectTypeOffice {
		t.Fatalf("a refused update must leave the row alone, got %q err=%v", after.ProjectType, err)
	}
}

// TestSessionFreezesProjectTypeAtBirth pins the owner's rule: a conversation
// copies its project's type when it is born and keeps that copy across later
// project edits and later writes to the session row.
func TestSessionFreezesProjectTypeAtBirth(t *testing.T) {
	ctx := context.Background()
	a, sessions := newProjectStoreForTest(t, "main")
	p, err := a.Create(ctx, CreateProjectInput{Name: "office", Root: t.TempDir(), ProjectType: ProjectTypeOffice})
	if err != nil {
		t.Fatal(err)
	}
	if err := sessions.EnsureAt(ctx, "frozen-1", "frozen-1", SessionBirth{ProjectID: p.ID}); err != nil {
		t.Fatal(err)
	}
	if got, err := a.SessionProjectType(ctx, "frozen-1"); err != nil || got != ProjectTypeOffice {
		t.Fatalf("a session born under an office project must read office, got %q err=%v", got, err)
	}
	coding := ProjectTypeCoding
	if _, err := a.Update(ctx, p.ID, UpdateProjectInput{ProjectType: &coding}); err != nil {
		t.Fatal(err)
	}
	if got, err := a.SessionProjectType(ctx, "frozen-1"); err != nil || got != ProjectTypeOffice {
		t.Fatalf("the type edit must not reach the existing session, got %q err=%v", got, err)
	}
	if err := sessions.EnsureAt(ctx, "frozen-2", "frozen-2", SessionBirth{ProjectID: p.ID}); err != nil {
		t.Fatal(err)
	}
	if got, err := a.SessionProjectType(ctx, "frozen-2"); err != nil || got != ProjectTypeCoding {
		t.Fatalf("a session born after the edit must read coding, got %q err=%v", got, err)
	}
	// A later write to the born row is the upsert's conflict path: it touches
	// updated_at only, never the frozen columns.
	if err := sessions.Ensure(ctx, "frozen-1", "frozen-1"); err != nil {
		t.Fatal(err)
	}
	if got, err := a.SessionProjectType(ctx, "frozen-1"); err != nil || got != ProjectTypeOffice {
		t.Fatalf("a later write must not change the frozen type, got %q err=%v", got, err)
	}
}

// TestSessionProjectTypeKnownReportsAMissingRow pins the one fact the known
// variant adds: a conversation whose row does not exist yet has frozen nothing,
// so a caller that may run before the conversation's first turn can tell the
// two cases apart. The older reader keeps its contract for the same session.
func TestSessionProjectTypeKnownReportsAMissingRow(t *testing.T) {
	ctx := context.Background()
	a, sessions := newProjectStoreForTest(t, "main")
	p, err := a.Create(ctx, CreateProjectInput{Name: "office", Root: t.TempDir(), ProjectType: ProjectTypeOffice})
	if err != nil {
		t.Fatal(err)
	}
	if got, found, err := a.SessionProjectTypeKnown(ctx, "missing"); err != nil || found || got != "" {
		t.Fatalf("a session with no row must read found=false and no type, got %q found=%t err=%v", got, found, err)
	}
	if got, err := a.SessionProjectType(ctx, "missing"); err != nil || got != ProjectTypeCoding {
		t.Fatalf("SessionProjectType still reads coding for a missing row, got %q err=%v", got, err)
	}
	if err := sessions.EnsureAt(ctx, "known-1", "known-1", SessionBirth{ProjectID: p.ID}); err != nil {
		t.Fatal(err)
	}
	if got, found, err := a.SessionProjectTypeKnown(ctx, "known-1"); err != nil || !found || got != ProjectTypeOffice {
		t.Fatalf("an office session must read found=true office, got %q found=%t err=%v", got, found, err)
	}
}

// TestChildSessionInheritsParentProjectType pins the inheritance order: a
// child born under a project conversation takes its parent's frozen type, even
// when its own birth project points somewhere else.
func TestChildSessionInheritsParentProjectType(t *testing.T) {
	ctx := context.Background()
	a, sessions := newProjectStoreForTest(t, "main")
	office, err := a.Create(ctx, CreateProjectInput{Name: "office", Root: t.TempDir(), ProjectType: ProjectTypeOffice})
	if err != nil {
		t.Fatal(err)
	}
	coding, err := a.Create(ctx, CreateProjectInput{Name: "coding", Root: filepath.Join(t.TempDir(), "c"), ProjectType: ProjectTypeCoding})
	if err != nil {
		t.Fatal(err)
	}
	if err := sessions.EnsureAt(ctx, "parent-office", "parent-office", SessionBirth{ProjectID: office.ID}); err != nil {
		t.Fatal(err)
	}
	if err := sessions.EnsureAt(ctx, "child", "child", SessionBirth{ParentSessionID: "parent-office", ProjectID: coding.ID}); err != nil {
		t.Fatal(err)
	}
	if got, err := a.SessionProjectType(ctx, "child"); err != nil || got != ProjectTypeOffice {
		t.Fatalf("a child must inherit its parent's frozen type, got %q err=%v", got, err)
	}
}

// TestBindSessionNeverChangesFrozenProjectType pins the boundary: rebinding
// changes which project a conversation belongs to, never the type it was born
// with.
func TestBindSessionNeverChangesFrozenProjectType(t *testing.T) {
	ctx := context.Background()
	a, sessions := newProjectStoreForTest(t, "main")
	office, err := a.Create(ctx, CreateProjectInput{Name: "office", Root: t.TempDir(), ProjectType: ProjectTypeOffice})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := a.Create(ctx, CreateProjectInput{Name: "batch", Root: filepath.Join(t.TempDir(), "b"), ProjectType: ProjectTypeBatch})
	if err != nil {
		t.Fatal(err)
	}
	if err := sessions.EnsureAt(ctx, "rebind", "rebind", SessionBirth{ProjectID: office.ID}); err != nil {
		t.Fatal(err)
	}
	coding := ProjectTypeCoding
	if _, err := a.Update(ctx, office.ID, UpdateProjectInput{ProjectType: &coding}); err != nil {
		t.Fatal(err)
	}
	if err := a.BindSession(ctx, "rebind", office.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := a.SessionProjectType(ctx, "rebind"); err != nil || got != ProjectTypeOffice {
		t.Fatalf("rebinding to the same project must not change the frozen type, got %q err=%v", got, err)
	}
	if err := a.BindSession(ctx, "rebind", batch.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := a.SessionProjectType(ctx, "rebind"); err != nil || got != ProjectTypeOffice {
		t.Fatalf("rebinding to a batch project must not change the frozen type, got %q err=%v", got, err)
	}
	// The project binding itself did move.
	var bound string
	if err := a.db.QueryRowContext(ctx, `SELECT project_id FROM fb_sessions WHERE id=? AND agent_id=?`, "rebind", "main").Scan(&bound); err != nil {
		t.Fatal(err)
	}
	if bound != batch.ID {
		t.Fatalf("the session must now belong to the batch project, got %q", bound)
	}
}

// TestProjectTaskNotifyRoundTrip pins the notification-target column: what a
// project is told to notify survives a round trip, the same channel and target
// is kept once, a half-filled entry is dropped rather than stored, the list is
// capped, and a column that cannot be read as a list reads as "notifies
// nobody" instead of failing the project load.
func TestProjectTaskNotifyRoundTrip(t *testing.T) {
	ctx := context.Background()
	projects, _ := newProjectStoreForTest(t, "main")
	p, err := projects.Create(ctx, CreateProjectInput{Name: "batch", Root: t.TempDir(), ProjectKey: "k", ProjectType: ProjectTypeBatch})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.TaskNotify) != 0 {
		t.Fatalf("a fresh project notifies %v, want nobody", p.TaskNotify)
	}

	targets := []TaskNotifyTarget{
		{Channel: " Feishu ", Target: " oc_1 "},
		{Channel: "feishu", Target: "oc_1"},
		{Channel: "telegram", Target: "42"},
		{Channel: "feishu", Target: "  "},
		{Channel: "", Target: "nobody"},
	}
	updated, err := projects.Update(ctx, p.ID, UpdateProjectInput{TaskNotify: &targets})
	if err != nil {
		t.Fatal(err)
	}
	want := []TaskNotifyTarget{{Channel: "feishu", Target: "oc_1"}, {Channel: "telegram", Target: "42"}}
	if len(updated.TaskNotify) != len(want) || updated.TaskNotify[0] != want[0] || updated.TaskNotify[1] != want[1] {
		t.Fatalf("stored targets = %v, want %v", updated.TaskNotify, want)
	}
	reloaded, err := projects.Get(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.TaskNotify) != 2 || reloaded.TaskNotify[1] != want[1] {
		t.Fatalf("targets after reload = %v, want %v", reloaded.TaskNotify, want)
	}

	many := make([]TaskNotifyTarget, 0, maxTaskNotifyTargets+5)
	for i := 0; i < maxTaskNotifyTargets+5; i++ {
		many = append(many, TaskNotifyTarget{Channel: "webhook", Target: "user-" + strconv.Itoa(i)})
	}
	updated, err = projects.Update(ctx, p.ID, UpdateProjectInput{TaskNotify: &many})
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.TaskNotify) != maxTaskNotifyTargets {
		t.Fatalf("stored %d targets, want the cap of %d", len(updated.TaskNotify), maxTaskNotifyTargets)
	}

	// A column nobody can read as a list is "no targets", not a broken project.
	if _, err := projects.db.ExecContext(ctx, `UPDATE fb_projects SET task_notify_json='not json' WHERE id=?`, p.ID); err != nil {
		t.Fatal(err)
	}
	broken, err := projects.Get(ctx, p.ID)
	if err != nil {
		t.Fatalf("a damaged target list must not make the project unreadable: %v", err)
	}
	if len(broken.TaskNotify) != 0 {
		t.Fatalf("targets from a damaged column = %v, want none", broken.TaskNotify)
	}

	// Clearing the list is a real setting, and it stays an empty list.
	empty := []TaskNotifyTarget{}
	cleared, err := projects.Update(ctx, p.ID, UpdateProjectInput{TaskNotify: &empty})
	if err != nil {
		t.Fatal(err)
	}
	if cleared.TaskNotify == nil || len(cleared.TaskNotify) != 0 {
		t.Fatalf("cleared targets = %v, want an empty list", cleared.TaskNotify)
	}
}
