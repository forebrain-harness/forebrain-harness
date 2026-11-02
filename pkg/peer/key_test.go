package peer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestNewToken(t *testing.T) {
	token := NewToken()
	if len(token) != 32 || strings.Trim(token, "0123456789abcdef") != "" {
		t.Fatalf("NewToken = %q, want 32 lowercase hex digits", token)
	}
	if again := NewToken(); again == token {
		t.Fatal("two tokens are equal")
	}
}

func TestWriteKeyAndFindToken(t *testing.T) {
	dir := t.TempDir()
	socket := filepath.Join(t.TempDir(), "peer.sock")
	token := NewToken()

	if err := WriteKey(dir, os.Getpid(), socket, token); err != nil {
		t.Fatalf("WriteKey: %v", err)
	}

	canonical, ok := canonicalSocketPath(socket)
	if !ok {
		t.Fatal("the socket path did not canonicalize")
	}
	name := strconv.Itoa(os.Getpid()) + "." + socketPathHash(canonical) + ".key"
	path := filepath.Join(dir, name)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("the key file is not named for the canonical socket path: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("key mode = %v, want 0600", info.Mode().Perm())
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var content KeyFile
	if err := json.Unmarshal(data, &content); err != nil {
		t.Fatalf("the key file is not JSON: %v", err)
	}
	if content.PeerToken != token {
		t.Fatalf("token = %q, want %q", content.PeerToken, token)
	}
	if content.PIDDomain != PIDDomain() {
		t.Fatalf("pid domain = %q", content.PIDDomain)
	}
	if content.ProcStart == "" {
		t.Fatal("the key file carries no start time")
	}

	found, ok := FindToken(socket, dir)
	if !ok || found != token {
		t.Fatalf("FindToken = %q, %v; want the written token", found, ok)
	}
	if _, ok := FindToken(filepath.Join(t.TempDir(), "other.sock"), dir); ok {
		t.Fatal("a key was found for a socket that has none")
	}
}

func TestFindTokenSkipsDeadAndStaleKeys(t *testing.T) {
	dir := t.TempDir()
	socket := filepath.Join(t.TempDir(), "peer.sock")
	canonical, ok := canonicalSocketPath(socket)
	if !ok {
		t.Fatal("the socket path did not canonicalize")
	}

	// A key file whose pid is gone.
	dead := filepath.Join(dir, "999999."+socketPathHash(canonical)+".key")
	if err := os.WriteFile(dead, []byte(`{"peerToken":"`+NewToken()+`","procStart":"Fri Oct 30 06:47:30 2026","pidDomain":"darwin"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := FindToken(socket, dir); ok {
		t.Fatal("a dead process's key was used")
	}

	// A key file for this process whose start time does not match: the pid was
	// reused.
	os.Remove(dead)
	stale := filepath.Join(dir, strconv.Itoa(os.Getpid())+"."+socketPathHash(canonical)+".key")
	if err := os.WriteFile(stale, []byte(`{"peerToken":"`+NewToken()+`","procStart":"Fri Oct 30 06:47:30 2026","pidDomain":"darwin"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := FindToken(socket, dir); ok {
		t.Fatal("a reused pid's key was used")
	}
}
