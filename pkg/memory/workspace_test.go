package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMemoryWorkspaceBaselineDiffAndReset(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "MEMORY.md"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "memory_summary.md"), []byte("v1\n\nold\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := prepareMemoryWorkspace(ctx, root); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "MEMORY.md"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "raw_memories.md"), []byte("raw\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	diff, changed, err := memoryWorkspaceDiff(ctx, root)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if !changed || !strings.Contains(diff, "MEMORY.md") || !strings.Contains(diff, "raw_memories.md") {
		t.Fatalf("unexpected diff changed=%v body=%q", changed, diff)
	}
	if err := writeMemoryWorkspaceDiff(root, diff); err != nil {
		t.Fatal(err)
	}
	if err := resetMemoryWorkspaceBaseline(ctx, root, time.Now()); err != nil {
		t.Fatalf("reset baseline: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, phase2WorkspaceDiffFile)); !os.IsNotExist(err) {
		t.Fatalf("workspace diff file still exists: %v", err)
	}
	_, changed, err = memoryWorkspaceDiff(ctx, root)
	if err != nil || changed {
		t.Fatalf("expected clean baseline changed=%v err=%v", changed, err)
	}
}

func TestResetMemoryWorkspaceBaselineDropsOldGitObjects(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	secret := "obsolete-memory-object-that-must-not-remain"
	if err := os.WriteFile(filepath.Join(root, "MEMORY.md"), []byte(secret), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := prepareMemoryWorkspace(ctx, root); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "MEMORY.md"), []byte("current memory"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := resetMemoryWorkspaceBaseline(ctx, root, time.Now()); err != nil {
		t.Fatal(err)
	}
	objects, err := runGit(ctx, root, "rev-list", "--objects", "--all")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(objects), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		content, err := runGit(ctx, root, "cat-file", "-p", fields[0])
		if err == nil && strings.Contains(content, secret) {
			t.Fatal("obsolete memory remained reachable from the fresh baseline")
		}
	}
}

func TestPrepareMemoryWorkspaceRemovesStaleDiff(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, phase2WorkspaceDiffFile), []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := prepareMemoryWorkspace(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, phase2WorkspaceDiffFile)); !os.IsNotExist(err) {
		t.Fatalf("stale diff not removed: %v", err)
	}
}

func TestValidateConsolidationArtifacts(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "MEMORY.md"), []byte("# Memory\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "memory_summary.md"), []byte("wrong\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validateConsolidationArtifacts(root); err == nil {
		t.Fatal("expected invalid summary failure")
	}
	if err := os.WriteFile(filepath.Join(root, "memory_summary.md"), []byte("v1\n\nsummary\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validateConsolidationArtifacts(root); err != nil {
		t.Fatalf("valid artifacts rejected: %v", err)
	}
}
