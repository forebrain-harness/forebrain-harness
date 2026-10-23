package llm

import (
	"path/filepath"
	"testing"
)

// One lock per credentials file, not per client: a refresh token that rotates
// on use can only be exchanged once from a given value, so every holder of the
// same login file has to queue on the same mutex.
func TestCredentialMutexIsSharedPerFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.json")
	first, second := CredentialMutex(path), CredentialMutex(path)
	if first != second {
		t.Fatal("CredentialMutex returned two mutexes for one path; two clients of one login would refresh concurrently")
	}

	// The same file spelled with a parent hop and spelled plainly is one file.
	// Leaving ".." in the key would hand out two locks for it.
	unclean := filepath.Join(dir, "x") + string(filepath.Separator) + ".." + string(filepath.Separator) + "opencode.json"
	if CredentialMutex(unclean) != CredentialMutex(path) {
		t.Fatalf("CredentialMutex(%q) is not the lock of %q", unclean, path)
	}
	if CredentialMutex(filepath.Join(dir, "x", "..", "opencode.json")) != CredentialMutex(path) {
		t.Fatal("CredentialMutex is not stable across equivalent spellings of one file")
	}

	other := filepath.Join(dir, "auth.json")
	if CredentialMutex(other) == CredentialMutex(path) {
		t.Fatal("two different login files share one mutex; unrelated refreshes would serialize")
	}
}
