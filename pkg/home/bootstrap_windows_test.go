//go:build windows

package home

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHasMultipleHardLinksWindows(t *testing.T) {
	dir := t.TempDir()

	single := filepath.Join(dir, "single.md")
	if err := os.WriteFile(single, []byte("solo"), 0o644); err != nil {
		t.Fatalf("write single: %v", err)
	}
	if hasMultipleHardLinks(single, nil) {
		t.Fatal("a freshly written file must not report multiple hardlinks")
	}

	original := filepath.Join(dir, "orig.md")
	linked := filepath.Join(dir, "linked.md")
	if err := os.WriteFile(original, []byte("body"), 0o644); err != nil {
		t.Fatalf("write original: %v", err)
	}
	if err := os.Link(original, linked); err != nil {
		t.Skipf("hardlink unavailable: %v", err)
	}
	if !hasMultipleHardLinks(linked, nil) {
		t.Fatal("hard-linked file should report multiple hardlinks")
	}

	// A non-existent path must not panic and must report false.
	if hasMultipleHardLinks(filepath.Join(dir, "missing.md"), nil) {
		t.Fatal("missing file should not report multiple hardlinks")
	}
}
