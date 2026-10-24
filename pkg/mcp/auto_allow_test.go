package mcp

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// No store yet reads as off; turning the switch on writes the file at the
// documented path, and turning it off removes the key again.
func TestProjectAutoAllowOnOff(t *testing.T) {
	ws := t.TempDir()
	if _, err := os.Stat(ProjectAutoAllowPath(ws)); !os.IsNotExist(err) {
		t.Fatalf("a fresh workspace must have no store: %v", err)
	}
	if ProjectAutoAllowed(ws, "proj-a") {
		t.Fatal("no store must read as off")
	}
	if err := SetProjectAutoAllow(ws, "proj-a", true, time.Unix(10, 0)); err != nil {
		t.Fatal(err)
	}
	if !ProjectAutoAllowed(ws, "proj-a") {
		t.Fatal("the switch must be on after SetProjectAutoAllow(on)")
	}
	a, err := LoadProjectAutoAllow(ws)
	if err != nil {
		t.Fatal(err)
	}
	if since := a["proj-a"].Since; since != 10 {
		t.Fatalf("since = %d, want 10", since)
	}
	if err := SetProjectAutoAllow(ws, "proj-a", false, time.Now()); err != nil {
		t.Fatal(err)
	}
	if ProjectAutoAllowed(ws, "proj-a") {
		t.Fatal("the switch must be off after SetProjectAutoAllow(off)")
	}
	b, err := LoadProjectAutoAllow(ws)
	if err != nil {
		t.Fatal(err)
	}
	if _, still := b["proj-a"]; still {
		t.Fatalf("off must delete the key: %+v", b)
	}
}

// Without a workspace there is no store to read or write — and an empty key
// never turns anything on.
func TestProjectAutoAllowNoWorkspaceOrKey(t *testing.T) {
	if p := ProjectAutoAllowPath(""); p != "" {
		t.Fatalf("ProjectAutoAllowPath(\"\") = %q, want \"\"", p)
	}
	if ProjectAutoAllowed("", "proj-a") {
		t.Fatal("no workspace must read as off")
	}
	if err := SetProjectAutoAllow("", "proj-a", true, time.Now()); err != nil {
		t.Fatalf("no workspace must be a no-op: %v", err)
	}
	if err := SetProjectAutoAllow(t.TempDir(), "  ", true, time.Now()); err != nil {
		t.Fatalf("empty key must be a no-op: %v", err)
	}
	if p := ProjectAutoAllowPath(t.TempDir()); p == "" {
		t.Fatal("a workspace must yield a store path")
	}
	ws := t.TempDir()
	if err := SetProjectAutoAllow(ws, "", true, time.Now()); err != nil {
		t.Fatalf("empty key must be a no-op: %v", err)
	}
	if _, err := os.Stat(ProjectAutoAllowPath(ws)); !os.IsNotExist(err) {
		t.Fatalf("a no-op must not write a store: %v", err)
	}
}

// An unparsable store fails closed: the switch reads as off rather than
// crashing every resolution that consults it.
func TestProjectAutoAllowFailsClosedOnBadJSON(t *testing.T) {
	ws := t.TempDir()
	p := ProjectAutoAllowPath(ws)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ProjectAutoAllowed(ws, "proj-a") {
		t.Fatal("a broken store must read as off")
	}
	if _, err := LoadProjectAutoAllow(ws); err != nil {
		t.Fatalf("a broken store loads as empty, not an error: %v", err)
	}
}

// One project's switch says nothing about another project's.
func TestProjectAutoAllowKeysAreIndependent(t *testing.T) {
	ws := t.TempDir()
	if err := SetProjectAutoAllow(ws, "proj-a", true, time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	if !ProjectAutoAllowed(ws, "proj-a") {
		t.Fatal("proj-a must be on")
	}
	if ProjectAutoAllowed(ws, "proj-b") {
		t.Fatal("proj-b must stay off")
	}
	if ProjectAutoAllowed(t.TempDir(), "proj-a") {
		t.Fatal("another agent workspace must stay off")
	}
}
