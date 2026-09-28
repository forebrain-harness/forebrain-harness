package memory

import (
	"os"
	"path/filepath"
	"testing"
)

func TestClearRemovesMemoryAndRolloutContents(t *testing.T) {
	workspace := t.TempDir()
	roots, err := ResolveRootsForAgent(workspace)
	if err != nil {
		t.Fatal(err)
	}
	root := roots.Scope(Scope{Kind: ScopeProject, Key: "proj"})
	memoryFile := filepath.Join(root.MemoryRoot, "nested", "MEMORY.md")
	evidenceFile := filepath.Join(root.rolloutEvidenceRoot(), "evidence.jsonl")
	for path, body := range map[string]string{memoryFile: "memory", evidenceFile: "evidence"} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := Clear(root); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root.MemoryRoot)
	if err != nil {
		t.Fatalf("memory root should remain available: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("memory root not empty: %#v", entries)
	}
	if _, err := os.Lstat(root.rolloutEvidenceRoot()); !os.IsNotExist(err) {
		t.Fatalf("rollout evidence root should be removed, err=%v", err)
	}
}

func TestClearRejectsSymlinkRootsWithoutTouchingTargets(t *testing.T) {
	for _, test := range []struct {
		name       string
		linkTarget func(root Root) string
		linkPath   func(root Root) string
	}{
		{name: "memory root", linkTarget: func(root Root) string { return filepath.Join(filepath.Dir(root.MemoryRoot), "memory-target") }, linkPath: func(root Root) string { return root.MemoryRoot }},
		{name: "scope state root", linkTarget: func(root Root) string { return filepath.Join(filepath.Dir(root.StateRoot), "state-target") }, linkPath: func(root Root) string { return root.stateScopeRoot() }},
	} {
		t.Run(test.name, func(t *testing.T) {
			roots, err := ResolveRootsForAgent(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			root := roots.Scope(Scope{Kind: ScopeProject, Key: "proj"})
			target := test.linkTarget(root)
			link := test.linkPath(root)
			if err := os.MkdirAll(target, 0o755); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(target, "keep.txt")
			if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			if err := Clear(root); err == nil {
				t.Fatal("symlinked clear root accepted")
			}
			if body, err := os.ReadFile(marker); err != nil || string(body) != "keep" {
				t.Fatalf("symlink target was touched: body=%q err=%v", body, err)
			}
		})
	}
}
