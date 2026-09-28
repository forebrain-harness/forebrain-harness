package gateway

import (
	"io/fs"
	"testing"
)

func TestFS(t *testing.T) {
	fsys, ok := FS()
	if fsys == nil {
		t.Fatal("FS returned nil filesystem")
	}
	if !ok {
		t.Skip("frontend not built (placeholder only); skipping embedded asset checks")
	}
	// When the frontend is built, index.html and the assets dir must be present.
	if _, err := fs.Stat(fsys, "index.html"); err != nil {
		t.Fatalf("index.html missing from embedded FS: %v", err)
	}
	entries, err := fs.ReadDir(fsys, "assets")
	if err != nil {
		t.Fatalf("assets dir missing from embedded FS: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("embedded assets dir is empty")
	}
}
