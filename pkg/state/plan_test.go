package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPlanPlanDir_DefaultGatewayScope(t *testing.T) {
	tmpDir := t.TempDir()
	d := PlanDirForProject(tmpDir, "")
	if d != filepath.Join(tmpDir, "plans") {
		t.Errorf("PlanDir=%q want %q", d, filepath.Join(tmpDir, "plans"))
	}
}

// TestPlanDirForProject_UsesProjectKeyVerbatim pins the one identity every
// per-project directory in the workspace shares: the project root with its
// separators turned into dashes, as produced by memories.ProjectKey. The plan
// directory must use that key as-is, or plans and memories would disagree about
// what "the current project" is.
func TestPlanPlanDirForProject_UsesProjectKeyVerbatim(t *testing.T) {
	tmpDir := t.TempDir()
	const key = "-Users-ada-work-unionj-cloud-forebrain-harness"
	d := PlanDirForProject(tmpDir, " "+key+" ")
	want := filepath.Join(tmpDir, "plans", key)
	if d != want {
		t.Errorf("PlanDirForProject=%q want %q", d, want)
	}
}

func TestPlanPlanDirForProject_EmptyProjectUsesPlansRoot(t *testing.T) {
	tmpDir := t.TempDir()
	d := PlanDirForProject(tmpDir, "")
	want := filepath.Join(tmpDir, "plans")
	if d != want {
		t.Errorf("PlanDirForProject empty=%q want %q", d, want)
	}
}

func TestPlanPlanPath_EmptyDirReturnsDefault(t *testing.T) {
	tmpDir := t.TempDir()
	p := PlanPathForSession(tmpDir, "", "s1")
	if filepath.Base(p) != "plan.md" {
		t.Errorf("expected default 'plan.md', got %q", filepath.Base(p))
	}
	if filepath.Dir(p) != PlanDirForSession(tmpDir, "", "s1") {
		t.Errorf("default should live in PlanDir, got %q", filepath.Dir(p))
	}
}

func TestPlanGet_NonExistentFile(t *testing.T) {
	tmpDir := t.TempDir()
	content, err := GetPlanForSession(tmpDir, "", "s1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if content != "" {
		t.Errorf("expected empty string for non-existent file, got %q", content)
	}
}

func TestPlanSetAndGet(t *testing.T) {
	tmpDir := t.TempDir()
	err := SetPlanForSession(tmpDir, "", "s1", `# My Plan

Step 1: Do something
Step 2: Do another thing`)
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}
	content, err := GetPlanForSession(tmpDir, "", "s1")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	expected := `# My Plan

Step 1: Do something
Step 2: Do another thing`
	if content != expected {
		t.Errorf("expected %q, got %q", expected, content)
	}
}

func TestPlanSetAndGetEmptyContent(t *testing.T) {
	tmpDir := t.TempDir()
	err := SetPlanForSession(tmpDir, "", "s1", "")
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}
	content, err := GetPlanForSession(tmpDir, "", "s1")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if content != "" {
		t.Errorf("expected empty string, got %q", content)
	}
}

func TestPlanSet_TrimsContent(t *testing.T) {
	tmpDir := t.TempDir()
	err := SetPlanForSession(tmpDir, "", "s1", "  hello world  ")
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}
	content, err := GetPlanForSession(tmpDir, "", "s1")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if content != "hello world" {
		t.Errorf("expected trimmed 'hello world', got %q", content)
	}
}

func TestPlanSet_PersistsToFile(t *testing.T) {
	tmpDir := t.TempDir()
	err := SetPlanForSession(tmpDir, "", "s1", "persistent content")
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}
	p := PlanPathForSession(tmpDir, "", "s1")
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if !strings.Contains(string(data), "persistent content") {
		t.Error("expected file to contain persistent content")
	}
}

// TestMultiplePlansPreservedAndNewestIsCurrent is the core of the new
// semantics: the LLM writes a first plan, then a second plan with a different
// semantic name. Both files are preserved, and the newest one is "current".
func TestPlanMultiplePlansPreservedAndNewestIsCurrent(t *testing.T) {
	tmpDir := t.TempDir()
	dir := PlanDirForSession(tmpDir, "", "s1")

	first := filepath.Join(dir, "skill-archive-s3.md")
	second := filepath.Join(dir, "s3-workspace-sync.md")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(first, []byte("# Plan A\n"), 0o600); err != nil {
		t.Fatalf("write first: %v", err)
	}
	// Second file written later -> newest mtime -> current.
	if err := os.WriteFile(second, []byte("# Plan B\n"), 0o600); err != nil {
		t.Fatalf("write second: %v", err)
	}

	// Both files preserved.
	if _, err := os.Stat(first); err != nil {
		t.Fatalf("first plan should be preserved: %v", err)
	}
	if _, err := os.Stat(second); err != nil {
		t.Fatalf("second plan should be preserved: %v", err)
	}
	// Current = newest = second.
	if got := PlanPathForSession(tmpDir, "", "s1"); got != second {
		t.Errorf("PlanPath=%q want %q", got, second)
	}
	got, err := GetPlanForSession(tmpDir, "", "s1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "# Plan B" {
		t.Errorf("Get=%q want '# Plan B'", got)
	}
}

// TestPlanPath_NonMdFilesIgnored confirms only .md files are considered plans.
func TestPlanPlanPath_NonMdFilesIgnored(t *testing.T) {
	tmpDir := t.TempDir()
	dir := PlanDirForSession(tmpDir, "", "s1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write txt: %v", err)
	}
	// No .md -> default path.
	if got := PlanPathForSession(tmpDir, "", "s1"); filepath.Base(got) != "plan.md" {
		t.Errorf("expected default plan.md, got %q", got)
	}
}

func TestPlanGet_EmptySessionID(t *testing.T) {
	tmpDir := t.TempDir()
	err := SetPlanForSession(tmpDir, "", "", "default plan")
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}
	content, err := GetPlanForSession(tmpDir, "", "")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if content != "default plan" {
		t.Errorf("expected 'default plan', got %q", content)
	}
}

// writePlanFile writes content into dir/name and pins its mtime so tests never
// depend on write ordering or filesystem timestamp granularity.
func writePlanFile(t *testing.T, dir, name, content string, mt time.Time) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	if err := os.Chtimes(p, mt, mt); err != nil {
		t.Fatalf("chtimes %s: %v", p, err)
	}
}

// TestPlanSessionsResolveOnlyTheirOwnPlans is the regression test for the
// cross-conversation plan leak: a plan written by another conversation — or a
// legacy file left flat in the project directory — must never become this
// conversation's current plan, no matter how recently it was modified.
func TestPlanSessionsResolveOnlyTheirOwnPlans(t *testing.T) {
	root := t.TempDir()
	t0 := time.Now().Add(-time.Hour)

	writePlanFile(t, PlanDirForSession(root, "proj", "cli-a"), "a.md", "# A", t0)
	writePlanFile(t, PlanDirForProject(root, "proj"), "legacy.md", "# Legacy", t0.Add(time.Minute))
	writePlanFile(t, PlanDirForSession(root, "proj", "cli-b"), "b.md", "# B", t0.Add(2*time.Minute))

	if got, want := PlanPathForSession(root, "proj", "cli-a"), filepath.Join(PlanDirForSession(root, "proj", "cli-a"), "a.md"); got != want {
		t.Errorf("PlanPathForSession(cli-a)=%q want %q", got, want)
	}
	content, err := GetPlanForSession(root, "proj", "cli-a")
	if err != nil {
		t.Fatalf("GetPlanForSession(cli-a): %v", err)
	}
	if content != "# A" {
		t.Errorf("GetPlanForSession(cli-a)=%q want %q", content, "# A")
	}

	// A conversation that never wrote a plan has no current plan, even though
	// other directories hold fresh ones.
	if got, want := PlanPathForSession(root, "proj", "cli-c"), filepath.Join(PlanDirForSession(root, "proj", "cli-c"), "plan.md"); got != want {
		t.Errorf("PlanPathForSession(cli-c)=%q want %q", got, want)
	}
	content, err = GetPlanForSession(root, "proj", "cli-c")
	if err != nil {
		t.Fatalf("GetPlanForSession(cli-c): %v", err)
	}
	if content != "" {
		t.Errorf("GetPlanForSession(cli-c)=%q want empty", content)
	}
}

// TestPlanDirForSessionStaysInsideProjectDir pins the sanitiser: any session id
// collapses to a single directory name inside the project's plan directory, and
// an empty id maps to "default" like every other per-session store.
func TestPlanDirForSessionStaysInsideProjectDir(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		id   string
		base string
	}{
		{"cli-1", "cli-1"},
		{"", "default"},
		{"  ", "default"},
		{"..", "default"},
		{"../escape", "-escape"},
		{"a/b", "a-b"},
		{"wx:abc", "wx-abc"},
	}
	for _, tc := range cases {
		got := PlanDirForSession(root, "proj", tc.id)
		if filepath.Dir(got) != PlanDirForProject(root, "proj") {
			t.Errorf("PlanDirForSession(%q)=%q escapes %q", tc.id, got, PlanDirForProject(root, "proj"))
		}
		if filepath.Base(got) != tc.base {
			t.Errorf("PlanDirForSession(%q) base=%q want %q", tc.id, filepath.Base(got), tc.base)
		}
	}
}

// TestCopySessionPlansKeepsTheCurrentPlanCurrent pins /fork semantics: the
// copy carries over the same plan files (only plan markdown, never hidden or
// non-md entries) and the source's current plan stays current because the
// modification times travel with the content.
func TestCopySessionPlansKeepsTheCurrentPlanCurrent(t *testing.T) {
	root := t.TempDir()
	t0 := time.Now().Add(-time.Hour)
	src := PlanDirForSession(root, "proj", "src")

	writePlanFile(t, src, "old.md", "# Old", t0)
	writePlanFile(t, src, "new.md", "# New", t0.Add(time.Minute))
	writePlanFile(t, src, "notes.txt", "not a plan", t0.Add(2*time.Minute))
	writePlanFile(t, src, ".hidden.md", "# Hidden", t0.Add(3*time.Minute))

	if err := CopySessionPlans(root, "proj", "src", "dst"); err != nil {
		t.Fatalf("CopySessionPlans: %v", err)
	}
	dst := PlanDirForSession(root, "proj", "dst")
	entries, err := os.ReadDir(dst)
	if err != nil {
		t.Fatalf("ReadDir(dst): %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if strings.Join(names, ",") != "new.md,old.md" && strings.Join(names, ",") != "old.md,new.md" {
		t.Errorf("dst contains %v, want exactly old.md and new.md", names)
	}
	for name, want := range map[string]string{"old.md": "# Old", "new.md": "# New"} {
		b, err := os.ReadFile(filepath.Join(dst, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if got := strings.TrimSpace(string(b)); got != want {
			t.Errorf("%s content=%q want %q", name, got, want)
		}
	}
	if got := filepath.Base(PlanPathForSession(root, "proj", "dst")); got != "new.md" {
		t.Errorf("current plan in dst=%q want new.md", got)
	}
}

// TestCopySessionPlansWithoutSourceIsNoop: a source that never wrote plans is
// not an error, and copying must not create the target's directory either —
// an empty conversation directory is indistinguishable from "no plans yet".
func TestCopySessionPlansWithoutSourceIsNoop(t *testing.T) {
	root := t.TempDir()
	if err := CopySessionPlans(root, "proj", "src", "dst"); err != nil {
		t.Fatalf("CopySessionPlans: %v", err)
	}
	if _, err := os.Stat(PlanDirForSession(root, "proj", "dst")); !os.IsNotExist(err) {
		t.Errorf("dst directory should not exist, stat err=%v", err)
	}
}

// The helpers below were production functions that only the tests in this
// package ever called. They live here so the production files carry no
// unused code while the tests keep exercising the live code underneath.

// PlansForSession is the history a conversation's plan directory already
// holds: every .md it ever wrote, newest first, so the web can show previous
// versions that PlanPathForSession (newest only) can never name.
func TestPlansForSessionListsHistoryNewestFirst(t *testing.T) {
	root := t.TempDir()
	t0 := time.Now().Add(-time.Hour)
	dir := PlanDirForSession(root, "proj", "s1")

	writePlanFile(t, dir, "first-version.md", "# First\n", t0)
	writePlanFile(t, dir, "second-version.md", "# Second\n", t0.Add(time.Minute))
	writePlanFile(t, dir, "third-version.md", "# Third\n", t0.Add(2*time.Minute))

	plans, err := PlansForSession(root, "proj", "s1")
	if err != nil {
		t.Fatalf("PlansForSession: %v", err)
	}
	if len(plans) != 3 {
		t.Fatalf("got %d plans, want 3: %+v", len(plans), plans)
	}
	wantNames := []string{"third-version.md", "second-version.md", "first-version.md"}
	for i, want := range wantNames {
		if plans[i].Name != want {
			t.Errorf("plans[%d].Name=%q want %q", i, plans[i].Name, want)
		}
		if got, wantPath := plans[i].Path, filepath.Join(dir, want); got != wantPath {
			t.Errorf("plans[%d].Path=%q want %q", i, got, wantPath)
		}
		if plans[i].UpdatedAt.IsZero() {
			t.Errorf("plans[%d].UpdatedAt is zero", i)
		}
	}
	if plans[0].Markdown != "# Third" {
		t.Errorf("plans[0].Markdown=%q want %q", plans[0].Markdown, "# Third")
	}
}

// Ties on modification time order by name so a list never reorders itself
// between two reads of the same directory.
func TestPlansForSessionSameMtimeOrdersByName(t *testing.T) {
	root := t.TempDir()
	mt := time.Now().Add(-time.Hour)
	dir := PlanDirForSession(root, "proj", "s1")

	writePlanFile(t, dir, "b-plan.md", "# B\n", mt)
	writePlanFile(t, dir, "a-plan.md", "# A\n", mt)

	plans, err := PlansForSession(root, "proj", "s1")
	if err != nil {
		t.Fatalf("PlansForSession: %v", err)
	}
	if len(plans) != 2 || plans[0].Name != "a-plan.md" || plans[1].Name != "b-plan.md" {
		t.Fatalf("plans=%+v want [a-plan.md b-plan.md]", plans)
	}
}

// A plan list is the .md files and nothing else: the same isPlanMarkdown
// definition newestMarkdown uses, so the two can never disagree about what
// counts.
func TestPlansForSessionSkipsHiddenAndNonMarkdown(t *testing.T) {
	root := t.TempDir()
	now := time.Now().Add(-time.Hour)
	dir := PlanDirForSession(root, "proj", "s1")

	writePlanFile(t, dir, "real-plan.md", "# Real\n", now)
	writePlanFile(t, dir, ".draft.md", "# Draft\n", now.Add(time.Minute))
	writePlanFile(t, dir, "notes.txt", "not a plan", now.Add(2*time.Minute))
	if err := os.MkdirAll(filepath.Join(dir, "archive.md"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	plans, err := PlansForSession(root, "proj", "s1")
	if err != nil {
		t.Fatalf("PlansForSession: %v", err)
	}
	if len(plans) != 1 || plans[0].Name != "real-plan.md" {
		t.Fatalf("plans=%+v want exactly real-plan.md", plans)
	}
}

// Most conversations never plan; that is an empty list, not an error.
func TestPlansForSessionMissingDirIsEmpty(t *testing.T) {
	root := t.TempDir()
	plans, err := PlansForSession(root, "proj", "never-planned")
	if err != nil {
		t.Fatalf("PlansForSession: %v", err)
	}
	if len(plans) != 0 {
		t.Fatalf("plans=%+v want empty", plans)
	}
}

// A plan too large to belong in a list still appears, without text: dropping
// it would hide that the conversation planned, and carrying it would put a
// huge body in one JSON response.
func TestPlansForSessionOversizedPlanArrivesWithoutText(t *testing.T) {
	root := t.TempDir()
	now := time.Now().Add(-time.Hour)
	dir := PlanDirForSession(root, "proj", "s1")

	writePlanFile(t, dir, "small.md", "# Small\n", now)
	huge := strings.Repeat("x", maxListedPlanBytes+1)
	writePlanFile(t, dir, "huge.md", huge, now.Add(time.Minute))

	plans, err := PlansForSession(root, "proj", "s1")
	if err != nil {
		t.Fatalf("PlansForSession: %v", err)
	}
	if len(plans) != 2 {
		t.Fatalf("got %d plans, want 2", len(plans))
	}
	if plans[0].Name != "huge.md" || plans[0].Markdown != "" {
		t.Errorf("oversized plan=%q markdown len=%d, want huge.md with empty text", plans[0].Name, len(plans[0].Markdown))
	}
	if plans[1].Markdown != "# Small" {
		t.Errorf("small plan markdown=%q want %q", plans[1].Markdown, "# Small")
	}
}
