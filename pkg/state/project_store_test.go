package state

import (
	"context"
	"path/filepath"
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

func TestProjectArchiveKeepsData(t *testing.T) {
	ctx := context.Background()
	a, _ := newProjectStoreForTest(t, "main")
	p, err := a.Create(ctx, CreateProjectInput{Name: "keep", Root: "/tmp/keep"})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.SetArchived(ctx, p.ID, 1700000000); err != nil {
		t.Fatal(err)
	}
	list, err := a.ListProjects(ctx, ListProjectsOptions{}, 50, 0)
	if err != nil || len(list) != 0 {
		t.Fatalf("archived project hidden by default: %v %+v", err, list)
	}
	list, err = a.ListProjects(ctx, ListProjectsOptions{IncludeArchived: true}, 50, 0)
	if err != nil || len(list) != 1 {
		t.Fatalf("archived project visible with filter: %v %+v", err, list)
	}
	if list[0].ArchivedAt == nil {
		t.Fatalf("archived_at not persisted")
	}
	if err := a.SetArchived(ctx, p.ID, 0); err != nil {
		t.Fatal(err)
	}
	list, _ = a.ListProjects(ctx, ListProjectsOptions{}, 50, 0)
	if len(list) != 1 {
		t.Fatalf("restore failed")
	}
}

func TestProjectDeleteCascadesAndUnbindsSessions(t *testing.T) {
	ctx := context.Background()
	a, sessions := newProjectStoreForTest(t, "main")
	p, err := a.Create(ctx, CreateProjectInput{Name: "gone", Root: "/tmp/gone"})
	if err != nil {
		t.Fatal(err)
	}
	if err := sessions.Ensure(ctx, "sess-1", "title"); err != nil {
		t.Fatal(err)
	}
	if err := a.BindSession(ctx, "sess-1", p.ID); err != nil {
		t.Fatal(err)
	}
	got, ok, err := a.ProjectForSession(ctx, "sess-1")
	if err != nil || !ok || got.ID != p.ID {
		t.Fatalf("bind failed: %v %v %+v", err, ok, got)
	}
	if err := a.Delete(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Get(ctx, p.ID); err == nil {
		t.Fatalf("deleted project still readable")
	}
	if _, ok, _ := a.ProjectForSession(ctx, "sess-1"); ok {
		t.Fatalf("session should be unbound after delete")
	}
	// Session itself survives.
	if exists, _ := sessions.HasSession(ctx, "sess-1"); !exists {
		t.Fatalf("session transcript must survive project deletion")
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
