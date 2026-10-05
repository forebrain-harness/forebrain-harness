package hook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSessionPathsShareOneSidechainDir pins that the hook transcript and every
// sidechain record of one session hang off the one SessionSidechainDir
// definition: deletion walks that directory, so a path spelled any other way
// would leave a file behind.
func TestSessionPathsShareOneSidechainDir(t *testing.T) {
	root := t.TempDir()
	dir := SessionSidechainDir(root, "cron-job-1-1790000000")
	if !strings.HasPrefix(dir, filepath.Join(root, "state", "fork-sidechain")) {
		t.Fatalf("sidechain dir = %q, want it under state/fork-sidechain", dir)
	}
	transcript := SidechainTranscriptPath(root, "cron-job-1-1790000000", "reviewer")
	if transcript == "" {
		t.Fatal("SidechainTranscriptPath returned empty")
	}
	if dir != filepath.Dir(transcript) {
		t.Fatalf("subagent transcript %q is not inside the session's sidechain dir %q", transcript, dir)
	}
	if got := SessionTranscriptPath(root, "cron-job-1-1790000000"); !strings.HasPrefix(filepath.Base(got), "cron-job-1-1790000000") {
		t.Fatalf("hook transcript path = %q, want it named for the session", got)
	}

	// An id with characters the filesystem would rather not see is sanitised
	// in place, never passed through: the separator becomes '_' and the two
	// dots stay — allowed characters — but as part of one segment, not a
	// traversal.
	if rough := SessionSidechainDir(root, "../escape/at/tempt"); filepath.Base(rough) != ".._escape_at_tempt" {
		t.Fatalf("unsanitised sidechain dir = %q, want the segment escaped in place", rough)
	}
}

// TestRemoveSessionArtifacts deletes both artifacts a session leaves for the
// hooks: the transcript file and the whole sidechain directory. A session
// that wrote neither removes cleanly.
func TestRemoveSessionArtifacts(t *testing.T) {
	root := t.TempDir()
	sid := "cron-job-2-1790000123"

	if err := os.MkdirAll(SessionSidechainDir(root, sid), 0o755); err != nil {
		t.Fatal(err)
	}
	subagent := SidechainTranscriptPath(root, sid, "investigator")
	if err := os.MkdirAll(filepath.Dir(subagent), 0o755); err != nil {
		t.Fatal(err)
	}
	transcript := SessionTranscriptPath(root, sid)
	if err := os.MkdirAll(filepath.Dir(transcript), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{subagent, transcript} {
		if err := os.WriteFile(p, []byte("line\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := RemoveSessionArtifacts(root, sid); err != nil {
		t.Fatalf("RemoveSessionArtifacts: %v", err)
	}
	if _, err := os.Stat(SessionSidechainDir(root, sid)); !os.IsNotExist(err) {
		t.Fatal("sidechain dir survived removal")
	}
	if _, err := os.Stat(SessionTranscriptPath(root, sid)); !os.IsNotExist(err) {
		t.Fatal("hook transcript survived removal")
	}

	if err := RemoveSessionArtifacts(root, "never-wrote-anything"); err != nil {
		t.Fatalf("removing a session that wrote nothing = %v, want nil", err)
	}
}
