package memory

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPhase2WorkspaceInputsPreserveMetadataAndStableOrder(t *testing.T) {
	root := t.TempDir()
	rows := []Stage1Output{
		{ThreadID: "thread-b", SourceUpdatedAt: 100, Cwd: "/work/b", RolloutPath: "/state/thread-b.jsonl", RawMemory: "raw b", RolloutSummary: "summary b"},
		{ThreadID: "thread-a", SourceUpdatedAt: 50, Cwd: "/work/a", RolloutPath: "/state/thread-a.jsonl", GitBranch: "feature/memory", RawMemory: "raw a", RolloutSummary: "summary a", RolloutSlug: sql.NullString{String: "Useful Task", Valid: true}},
	}
	if err := syncPhase2WorkspaceInputs(root, rows); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "raw_memories.md"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	if strings.Index(body, "thread-a") > strings.Index(body, "thread-b") || !strings.Contains(body, "updated_at: 1970-01-01T00:00:50+00:00") {
		t.Fatalf("raw memories = %q", body)
	}
	files, err := filepath.Glob(filepath.Join(root, rolloutSummariesDir, "*.md"))
	if err != nil || len(files) != 2 {
		t.Fatalf("summary files=%#v err=%v", files, err)
	}
	var foundBranch bool
	for _, path := range files {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(content), "thread_id: thread-a") {
			foundBranch = strings.Contains(string(content), "git_branch: feature/memory") && strings.Contains(string(content), "\n\nsummary a\n")
		}
	}
	if !foundBranch {
		t.Fatal("thread branch metadata missing from rollout summary")
	}
}

func TestPruneOldExtensionResourcesUsesTimestampedMarkdownOnly(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, extensionsDir, "ad_hoc")
	resources := filepath.Join(dir, "resources")
	if err := os.MkdirAll(resources, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "instructions.md"), []byte("instructions"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"2026-01-01T00-00-00-old.md", "2026-01-10T00-00-01-new.md", "untimestamped.md", "2026-01-01T00-00-00-old.txt"} {
		if err := os.WriteFile(filepath.Join(resources, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pruneOldExtensionResources(root, time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC))
	if _, err := os.Stat(filepath.Join(resources, "2026-01-01T00-00-00-old.md")); !os.IsNotExist(err) {
		t.Fatalf("old resource remains: %v", err)
	}
	for _, name := range []string{"2026-01-10T00-00-01-new.md", "untimestamped.md", "2026-01-01T00-00-00-old.txt"} {
		if _, err := os.Stat(filepath.Join(resources, name)); err != nil {
			t.Fatalf("resource %s was removed: %v", name, err)
		}
	}
}
