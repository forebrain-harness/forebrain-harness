package home

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveWithinRoots_AllowsDirectChild(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "a", "b.txt")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveWithinRoots(file, []string{root})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != filepath.Clean(file) {
		t.Fatalf("got %q want %q", got, filepath.Clean(file))
	}
}

func TestContainsTraversalRejectsDangerousForms(t *testing.T) {
	for _, tc := range []string{"../a", `..\\a`, "C:/x", "//server/share", "a/../b", "a\x00b"} {
		if !ContainsTraversal(tc) {
			t.Fatalf("expected traversal for %q", tc)
		}
	}
	for _, tc := range []string{"safe/path/file.txt", "/safe/abs/path"} {
		if ContainsTraversal(tc) {
			t.Fatalf("unexpected traversal detection for safe path %q", tc)
		}
	}
}

func TestValidateArchiveRelPathRejectsTraversal(t *testing.T) {
	for _, tc := range []string{"../evil", "/abs", `C:\evil`, "//unc/path"} {
		if err := ValidateArchiveRelPath(tc); err == nil {
			t.Fatalf("expected archive path rejection for %q", tc)
		}
	}
	if err := ValidateArchiveRelPath("safe/entry.txt"); err != nil {
		t.Fatalf("unexpected archive path rejection: %v", err)
	}
}

func TestResolveWithinRoots_BlocksOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(filepath.Dir(root), "outside.txt")
	_, err := ResolveWithinRoots(outside, []string{root})
	if err == nil {
		t.Fatalf("expected error")
	}
	if !errors.Is(err, ErrPathNotAllowed) {
		t.Fatalf("err=%v want ErrPathNotAllowed", err)
	}
}

func TestResolveWithinRoots_BlocksSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outsideDir := t.TempDir()
	link := filepath.Join(root, "link")
	if err := os.Symlink(outsideDir, link); err != nil {
		t.Skipf("symlink unsupported on this environment: %v", err)
	}
	target := filepath.Join(link, "secret.txt")
	_, err := ResolveWithinRoots(target, []string{root})
	if err == nil {
		t.Fatalf("expected symlink escape block")
	}
	if !errors.Is(err, ErrPathNotAllowed) {
		t.Fatalf("err=%v want ErrPathNotAllowed", err)
	}
}

func TestResolveWithinRoots_AllowsSymlinkInsideRoot(t *testing.T) {
	root := t.TempDir()
	targetDir := filepath.Join(root, "real")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "alias")
	if err := os.Symlink(targetDir, link); err != nil {
		t.Skipf("symlink unsupported on this environment: %v", err)
	}
	target := filepath.Join(link, "ok.txt")
	got, err := ResolveWithinRoots(target, []string{root})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != filepath.Clean(target) {
		t.Fatalf("got %q want %q", got, filepath.Clean(target))
	}
}
