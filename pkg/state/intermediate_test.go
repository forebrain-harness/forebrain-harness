package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAppendReadClear(t *testing.T) {
	home := t.TempDir()
	sessionID := "sess1"

	if err := Append(home, sessionID, "first note"); err != nil {
		t.Fatalf("Append first: %v", err)
	}
	if err := Append(home, sessionID, "second note"); err != nil {
		t.Fatalf("Append second: %v", err)
	}

	got, err := Read(home, sessionID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	want := "first note\n\nsecond note"
	if got != want {
		t.Fatalf("Read = %q, want %q", got, want)
	}

	info, err := os.Stat(filepath.Join(home, "state", "intermediate", sessionID+".md"))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %#o, want 0600", info.Mode().Perm())
	}

	if err := Clear(home, sessionID); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	got, err = Read(home, sessionID)
	if err != nil {
		t.Fatalf("Read after clear: %v", err)
	}
	if got != "" {
		t.Fatalf("Read after clear = %q, want empty", got)
	}
}
