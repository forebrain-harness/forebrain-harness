package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

func assertNoTempLeftovers(t *testing.T, dir, name string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	prefix := "." + name + ".tmp-"
	for _, e := range entries {
		if len(e.Name()) >= len(prefix) && e.Name()[:len(prefix)] == prefix {
			t.Fatalf("temp file %q left behind in %s", e.Name(), dir)
		}
	}
}

func TestWriteFileAtomicReplacesAndKeepsMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "forebrain.yaml")

	if err := writeFileAtomic(path, []byte("first"), 0o600); err != nil {
		t.Fatalf("write new file: %v", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "first" {
		t.Fatalf("new file content: got %q, err %v", got, err)
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("new file mode: got %v, err %v", info.Mode().Perm(), err)
		}
	}

	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatalf("seed existing file: %v", err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatalf("chmod seed: %v", err)
	}
	if err := writeFileAtomic(path, []byte("second"), 0o600); err != nil {
		t.Fatalf("replace existing file: %v", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "second" {
		t.Fatalf("replaced content: got %q, err %v", got, err)
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o640 {
			t.Fatalf("existing file mode must be kept at 0640: got %v, err %v", info.Mode().Perm(), err)
		}
	}
	assertNoTempLeftovers(t, dir, "forebrain.yaml")
}

func TestWriteFileAtomicWritesThroughASymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.yaml")
	if err := os.WriteFile(real, []byte("old"), 0o600); err != nil {
		t.Fatalf("seed real file: %v", err)
	}
	link := filepath.Join(dir, "link.yaml")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	if err := writeFileAtomic(link, []byte("new"), 0o600); err != nil {
		t.Fatalf("write through symlink: %v", err)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("Lstat link: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link must stay a symlink, mode %v", info.Mode())
	}
	if got, err := os.ReadFile(real); err != nil || string(got) != "new" {
		t.Fatalf("real file content: got %q, err %v", got, err)
	}
	assertNoTempLeftovers(t, dir, "real.yaml")
	assertNoTempLeftovers(t, dir, "link.yaml")
}

func TestWriteFileAtomicFallsBackInPlace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "forebrain.yaml")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	orig := renameFile
	renameFile = func(_, _ string) error { return errors.New("rename refused") }
	t.Cleanup(func() { renameFile = orig })

	if err := writeFileAtomic(path, []byte("new"), 0o600); err != nil {
		t.Fatalf("fallback write must succeed, got %v", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "new" {
		t.Fatalf("fallback content: got %q, err %v", got, err)
	}
	assertNoTempLeftovers(t, dir, "forebrain.yaml")
}

// TestWriteFileAtomicNeverExposesAPartialFile has one goroutine read the
// target in a tight loop while another alternates two distinct 256 KiB
// payloads through writeFileAtomic. Every completed read must be exactly one
// of the two payloads: under the truncate-then-write of os.WriteFile a reader
// observes empty or half files; under the temp-file rename it never does.
func TestWriteFileAtomicNeverExposesAPartialFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "forebrain.yaml")
	a := bytes.Repeat([]byte("A"), 256*1024)
	b := bytes.Repeat([]byte("B"), 256*1024)
	if err := writeFileAtomic(path, a, 0o600); err != nil {
		t.Fatalf("seed write: %v", err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	var bad string
	fail := func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		if bad == "" {
			bad = fmt.Sprintf(format, args...)
		}
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			data, err := os.ReadFile(path)
			if err != nil {
				fail("read: %v", err)
				return
			}
			if !bytes.Equal(data, a) && !bytes.Equal(data, b) {
				fail("partial read: %d bytes, head %q", len(data), data[:32])
				return
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(stop)
		for i := 0; i < 200; i++ {
			next := b
			if i%2 == 1 {
				next = a
			}
			if err := writeFileAtomic(path, next, 0o600); err != nil {
				fail("write %d: %v", i, err)
				return
			}
		}
	}()

	wg.Wait()
	if bad != "" {
		t.Fatalf("reader observed a non-atomic state: %s", bad)
	}
	assertNoTempLeftovers(t, dir, "forebrain.yaml")
}
