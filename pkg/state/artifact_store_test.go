package state

import (
	"context"
	"path/filepath"
	"testing"
)

// newArtifactStoreForTest opens one store pair the artifact tests share: a
// session store under its own agent plus, where a test needs another tenant,
// a second one over the same database.
func newArtifactStoreForTest(t *testing.T, agent string) *SessionStore {
	t.Helper()
	db, err := OpenStateForTest(context.Background(), filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewSessionStore(db, agent)
}

func TestAddArtifactRequiresOwnedSession(t *testing.T) {
	ctx := context.Background()
	mine := newArtifactStoreForTest(t, "main")
	theirs := newArtifactStoreForTest(t, "other")
	if err := theirs.Ensure(ctx, "s-other", "their conversation"); err != nil {
		t.Fatal(err)
	}
	if _, err := mine.AddArtifact(ctx, Artifact{SessionID: "s-other", Path: "/tmp/a.docx", Name: "a.docx", MIME: "text/plain", Kind: "document"}); err == nil {
		t.Fatal("an artifact under another agent's session was written")
	}
	rows, total, err := mine.ListArtifactsPage(ctx, "s-other", 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 || total != 0 {
		t.Fatalf("other agent's session holds %d rows (total %d), want none", len(rows), total)
	}
}

func TestListArtifactsPageOrdersAndCounts(t *testing.T) {
	ctx := context.Background()
	s := newArtifactStoreForTest(t, "main")
	if err := s.Ensure(ctx, "s", "conversation"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.docx", "b.pptx", "c.pdf"} {
		if _, err := s.AddArtifact(ctx, Artifact{SessionID: "s", Path: "/tmp/" + name, RelPath: name, Name: name, MIME: "application/octet-stream", Kind: "document"}); err != nil {
			t.Fatal(err)
		}
	}
	page, total, err := s.ListArtifactsPage(ctx, "s", 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 2 || total != 3 {
		t.Fatalf("page = %d rows (total %d), want 2 of 3", len(page), total)
	}
	if page[0].Name != "a.docx" || page[1].Name != "b.pptx" {
		t.Fatalf("page order = %s, %s; want delivery order a.docx, b.pptx", page[0].Name, page[1].Name)
	}
	// An offset past the end reads the last page, not an empty list.
	last, total, err := s.ListArtifactsPage(ctx, "s", 2, 100)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || len(last) != 1 || last[0].Name != "c.pdf" {
		t.Fatalf("offset 100 read %d rows (total %d), want the last page holding c.pdf", len(last), total)
	}
}

func TestListArtifactsForRun(t *testing.T) {
	ctx := context.Background()
	s := newArtifactStoreForTest(t, "main")
	if err := s.Ensure(ctx, "s", "conversation"); err != nil {
		t.Fatal(err)
	}
	for _, row := range []Artifact{
		{SessionID: "s", RunID: "run-1", Path: "/tmp/a.docx", Name: "a.docx", Kind: "document"},
		{SessionID: "s", RunID: "run-2", Path: "/tmp/b.pptx", Name: "b.pptx", Kind: "presentation"},
	} {
		if _, err := s.AddArtifact(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.ListArtifactsForRun(ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].RunID != "run-1" || got[0].Name != "a.docx" {
		t.Fatalf("run-1 artifacts = %+v, want only a.docx", got)
	}
	if empty, err := s.ListArtifactsForRun(ctx, ""); err != nil || len(empty) != 0 {
		t.Fatalf("empty run id = %d rows, err %v; want none", len(empty), err)
	}
}

// TestLatestArtifactByPath pins the read the batch outputs scan's version
// check makes: the newest row a path already holds (same-second deliveries
// ordered by row, so the version the caller compares against is the one just
// written).
func TestLatestArtifactByPath(t *testing.T) {
	ctx := context.Background()
	s := newArtifactStoreForTest(t, "main")
	if err := s.Ensure(ctx, "s", "conversation"); err != nil {
		t.Fatal(err)
	}
	first, err := s.AddArtifact(ctx, Artifact{SessionID: "s", RunID: "run-1", Path: "/tmp/report.pdf", Name: "report.pdf", Kind: "document", SHA256: "one"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddArtifact(ctx, Artifact{SessionID: "s", RunID: "run-1", Path: "/tmp/scratch.zip", Name: "scratch.zip", Kind: "archive", Origin: ArtifactOriginFallback}); err != nil {
		t.Fatal(err)
	}
	// A second version of the same path, same second: row order decides.
	second, err := s.AddArtifact(ctx, Artifact{SessionID: "s", RunID: "run-2", Path: "/tmp/report.pdf", Name: "report.pdf", Kind: "document", SHA256: "two"})
	if err != nil {
		t.Fatal(err)
	}
	latest, found, err := s.LatestArtifactByPath(ctx, "s", "/tmp/report.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("a delivered path must read as found")
	}
	if latest.ID != second.ID || latest.SHA256 != "two" {
		t.Fatalf("latest = id %s sha %s; want the second version (id %s, sha two)", latest.ID, latest.SHA256, second.ID)
	}
	if latest.ID == first.ID {
		t.Fatal("the same-second tie must be broken by row order, not by id text")
	}
	if _, found, err := s.LatestArtifactByPath(ctx, "s", "/tmp/never-delivered"); err != nil || found {
		t.Fatalf("undelivered path found = %v, err %v; want not found", found, err)
	}
}

func TestArtifactsGoWithTheirSession(t *testing.T) {
	ctx := context.Background()
	s := newArtifactStoreForTest(t, "main")
	if err := s.Ensure(ctx, "s", "conversation"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddArtifact(ctx, Artifact{SessionID: "s", Path: "/tmp/a.docx", Name: "a.docx", Kind: "document"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteSessions(ctx, []string{"s"}); err != nil {
		t.Fatal(err)
	}
	rows, total, err := s.ListArtifactsPage(ctx, "s", 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 || total != 0 {
		t.Fatalf("artifacts survived their session: %d rows (total %d)", len(rows), total)
	}
	if _, err := s.GetArtifact(ctx, "s", "whatever"); err != ErrArtifactNotFound {
		t.Fatalf("get after session delete = %v, want ErrArtifactNotFound", err)
	}
}

func TestSessionProjectScope(t *testing.T) {
	ctx := context.Background()
	db, err := OpenStateForTest(context.Background(), filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	sessions := NewSessionStore(db, "main")
	projects := NewProjectStore(db, "main")
	root := t.TempDir()
	p, err := projects.Create(ctx, CreateProjectInput{Name: "office", Root: root, ProjectType: ProjectTypeOffice})
	if err != nil {
		t.Fatal(err)
	}
	if err := sessions.EnsureAt(ctx, "bound", "bound", SessionBirth{ProjectID: p.ID}); err != nil {
		t.Fatal(err)
	}
	if err := sessions.Ensure(ctx, "loose", "loose"); err != nil {
		t.Fatal(err)
	}
	pt, gotRoot, err := sessions.SessionProjectScope(ctx, "bound")
	if err != nil {
		t.Fatal(err)
	}
	if pt != ProjectTypeOffice || gotRoot != root {
		t.Fatalf("bound session scope = (%q, %q); want (office, %s)", pt, gotRoot, root)
	}
	pt, gotRoot, err = sessions.SessionProjectScope(ctx, "loose")
	if err != nil {
		t.Fatal(err)
	}
	if pt != ProjectTypeCoding || gotRoot != "" {
		t.Fatalf("project-less session scope = (%q, %q); want (coding, \"\")", pt, gotRoot)
	}
	pt, gotRoot, err = sessions.SessionProjectScope(ctx, "unknown")
	if err != nil {
		t.Fatal(err)
	}
	if pt != ProjectTypeCoding || gotRoot != "" {
		t.Fatalf("unknown session scope = (%q, %q); want the coding default", pt, gotRoot)
	}
}
