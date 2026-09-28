package telemetry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBoundTextRetainsHeadTail(t *testing.T) {
	text := strings.Repeat("a", 100) + strings.Repeat("z", 100)
	got := BoundText(text, 40)
	if !strings.HasPrefix(got, strings.Repeat("a", 20)) || !strings.HasSuffix(got, strings.Repeat("z", 20)) || !strings.Contains(got, "log bytes omitted") {
		t.Fatalf("bounded=%q", got)
	}
}

func TestOpenCreatesParentAndRotatesDuringLifetime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "debug.log")
	file, err := Open(path, Options{MaxBytes: 10, Backups: 2, Perm: 0o600})
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.Write([]byte("12345678")); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("abcdefgh")); err != nil {
		t.Fatal(err)
	}
	active, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatal(err)
	}
	if string(active) != "abcdefgh" || string(backup) != "12345678" {
		t.Fatalf("active=%q backup=%q", active, backup)
	}
}

func TestOpenPerformsStartupRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "debug.log")
	if err := os.WriteFile(path, []byte("oversized"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := Open(path, Options{MaxBytes: 4, Backups: 2, Perm: 0o600})
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if backup, err := os.ReadFile(path + ".1"); err != nil || string(backup) != "oversized" {
		t.Fatalf("startup backup=%q err=%v", backup, err)
	}
}

func TestOpenCanRequireExistingParent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "debug.log")
	if _, err := Open(path, Options{ExistingParentOnly: true}); err == nil {
		t.Fatal("missing parent was created despite ExistingParentOnly")
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("parent unexpectedly exists: %v", err)
	}
}

func TestWriteRawBoundsSingleRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "debug.log")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	payload := []byte(strings.Repeat("x", 4096))
	n, err := WriteRaw(file, payload, 1024, 2)
	if err != nil || n != len(payload) {
		t.Fatalf("write=(%d,%v) want=(%d,nil)", n, err, len(payload))
	}
	if info, err := file.Stat(); err != nil || info.Size() > 1024 {
		t.Fatalf("active log size=%v err=%v", info, err)
	}
}

func TestLevelLoggerOwnsInfoAndDebugFiles(t *testing.T) {
	dir := t.TempDir()
	infoPath := filepath.Join(dir, "info.log")
	debugPath := filepath.Join(dir, "debug.log")
	logger, err := OpenLevelLogger(infoPath, debugPath, Options{})
	if err != nil {
		t.Fatal(err)
	}
	logger.Infof("info %d", 1)
	logger.Debugf("debug %d", 2)
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	infoBody, err := os.ReadFile(infoPath)
	if err != nil {
		t.Fatal(err)
	}
	debugBody, err := os.ReadFile(debugPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(infoBody), "info 1") || !strings.Contains(string(debugBody), "debug 2") {
		t.Fatalf("info=%q debug=%q", infoBody, debugBody)
	}
}
