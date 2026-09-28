package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	p := PlanPathForProject(tmpDir, "")
	if filepath.Base(p) != "plan.md" {
		t.Errorf("expected default 'plan.md', got %q", filepath.Base(p))
	}
	if filepath.Dir(p) != PlanDirForProject(tmpDir, "") {
		t.Errorf("default should live in PlanDir, got %q", filepath.Dir(p))
	}
}

func TestPlanGet_NonExistentFile(t *testing.T) {
	tmpDir := t.TempDir()
	content, err := GetPlan(tmpDir, "nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if content != "" {
		t.Errorf("expected empty string for non-existent file, got %q", content)
	}
}

func TestPlanSetAndGet(t *testing.T) {
	tmpDir := t.TempDir()
	err := SetPlanForProject(tmpDir, "", `# My Plan

Step 1: Do something
Step 2: Do another thing`)
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}
	content, err := GetPlan(tmpDir, "test1")
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
	err := SetPlanForProject(tmpDir, "", "")
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}
	content, err := GetPlan(tmpDir, "empty")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if content != "" {
		t.Errorf("expected empty string, got %q", content)
	}
}

func TestPlanSet_TrimsContent(t *testing.T) {
	tmpDir := t.TempDir()
	err := SetPlanForProject(tmpDir, "", "  hello world  ")
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}
	content, err := GetPlan(tmpDir, "trim")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if content != "hello world" {
		t.Errorf("expected trimmed 'hello world', got %q", content)
	}
}

func TestPlanSet_PersistsToFile(t *testing.T) {
	tmpDir := t.TempDir()
	err := SetPlanForProject(tmpDir, "", "persistent content")
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}
	p := PlanPathForProject(tmpDir, "")
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
	dir := PlanDirForProject(tmpDir, "")

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
	if got := PlanPathForProject(tmpDir, ""); got != second {
		t.Errorf("PlanPath=%q want %q", got, second)
	}
	got, err := GetPlan(tmpDir, "hist")
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
	dir := PlanDirForProject(tmpDir, "")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write txt: %v", err)
	}
	// No .md -> default path.
	if got := PlanPathForProject(tmpDir, ""); filepath.Base(got) != "plan.md" {
		t.Errorf("expected default plan.md, got %q", got)
	}
}

func TestPlanGet_EmptySessionID(t *testing.T) {
	tmpDir := t.TempDir()
	err := SetPlanForProject(tmpDir, "", "default plan")
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}
	content, err := GetPlan(tmpDir, "")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if content != "default plan" {
		t.Errorf("expected 'default plan', got %q", content)
	}
}

// The helpers below were production functions that only the tests in this
// package ever called. They live here so the production files carry no
// unused code while the tests keep exercising the live code underneath.

// Get reads the current default gateway/channel plan file. A missing file
// yields ("", nil). Prefer GetForProject when projectKey is available.
func GetPlan(workspaceRoot, _ string) (string, error) {
	return GetPlanForProject(workspaceRoot, "")
}
