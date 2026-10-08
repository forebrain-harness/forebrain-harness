package telemetry

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
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

func TestWriteIsVisibleToAnotherReaderBeforeClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "debug.log")
	file, err := Open(path, Options{Perm: 0o600})
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.Write([]byte("visible without Close or Sync")); err != nil {
		t.Fatal(err)
	}
	// No Close and no Sync: the record must already be on the filesystem for
	// another reader. This pins the decision to stay unbuffered — a
	// bufio-style wrapper would delay tail -f and drop the tail on a crash.
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "visible without Close or Sync") {
		t.Fatalf("record not visible before Close: %q", body)
	}
}

func TestWriteRotatesUsingTheByteCounter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "debug.log")
	file, err := Open(path, Options{MaxBytes: 1024, Backups: 2, Perm: 0o600})
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	record := bytes.Repeat([]byte("r"), 256)
	for i := 0; i < 10; i++ {
		n, err := file.Write(record)
		if err != nil || n != len(record) {
			t.Fatalf("write %d=(%d,%v) want=(%d,nil)", i, n, err, len(record))
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > 1024 {
		t.Fatalf("active log size=%d want <= 1024", info.Size())
	}
	backup, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatal(err)
	}
	if len(backup) == 0 {
		t.Fatal("rotation never produced a backup")
	}
}

func TestWriteReturnsOriginalLengthForOversizedRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "debug.log")
	file, err := Open(path, Options{MaxBytes: 1024, Backups: 2, Perm: 0o600})
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	payload := bytes.Repeat([]byte("x"), 4096)
	n, err := file.Write(payload)
	if err != nil || n != len(payload) {
		t.Fatalf("write=(%d,%v) want=(%d,nil)", n, err, len(payload))
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > 1024 {
		t.Fatalf("active log size=%d want <= 1024", info.Size())
	}
}

// TestWriteThroughputIsNotFsyncBound is a barrier detector, not a precise
// performance assertion: 2,000 small records took ~7.4 s (2,000 × 3.7 ms) when
// every record paid an fsync and ~10 ms without one, so the 2 s budget is
// deliberately loose — roughly 200× headroom — to avoid flakes on slower
// disks. If it fails, a per-record disk barrier is probably back.
func TestWriteThroughputIsNotFsyncBound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.log")
	file, err := Open(path, Options{Perm: 0o600})
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	record := bytes.Repeat([]byte("g"), 120)
	start := time.Now()
	for i := 0; i < 2000; i++ {
		if _, err := file.Write(record); err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := time.Since(start); elapsed >= 2*time.Second {
		t.Fatalf("2,000 records took %v; a per-record disk barrier is probably back", elapsed)
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

// benchmarkFileWrite exercises the real Open→Write path with a payload of the
// given size. MaxBytes is set far above what the benchmark can write so the
// measurement isolates the per-record write cost from rotation work.
func benchmarkFileWrite(b *testing.B, payload []byte) {
	f, err := Open(filepath.Join(b.TempDir(), "debug.log"), Options{MaxBytes: 1 << 30, Backups: 2, Perm: 0o600})
	if err != nil {
		b.Fatal(err)
	}
	defer f.Close()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := f.Write(payload); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkFileWrite120B models the gateway.log access-log record size.
func BenchmarkFileWrite120B(b *testing.B) {
	benchmarkFileWrite(b, bytes.Repeat([]byte("g"), 120))
}

// BenchmarkFileWrite37KB models a typical debug.log provider-request block.
func BenchmarkFileWrite37KB(b *testing.B) {
	benchmarkFileWrite(b, bytes.Repeat([]byte("g"), 37*1024))
}

// BenchmarkFileWriteParallel8 drives 8 concurrent writers through one File,
// the shape gateway.log takes under concurrent HTTP handlers.
func BenchmarkFileWriteParallel8(b *testing.B) {
	f, err := Open(filepath.Join(b.TempDir(), "debug.log"), Options{MaxBytes: 1 << 30, Backups: 2, Perm: 0o600})
	if err != nil {
		b.Fatal(err)
	}
	defer f.Close()
	payload := bytes.Repeat([]byte("g"), 120)
	b.ResetTimer()
	var wg sync.WaitGroup
	per, rem := b.N/8, b.N%8
	for w := 0; w < 8; w++ {
		count := per
		if w < rem {
			count++
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < count; i++ {
				if _, err := f.Write(payload); err != nil {
					b.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
}
