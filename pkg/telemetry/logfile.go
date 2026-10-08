// Package logfile owns Forebrain Harness's bounded, rotating file-log primitives.
//
// Callers decide what to log and which severity/file receives it. This package
// is the single owner of file creation, record bounds, runtime rotation for
// redirected descriptors, and closing. Records reach the descriptor unbuffered
// and unsynced by design (see File.Write); File.Sync is the explicit,
// on-demand durability call.
package telemetry

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	DefaultFileMaxBytes = int64(20 * 1024 * 1024)
	DefaultBackups      = 3
	DefaultRecordBytes  = 128 * 1024
)

// Options controls one rotating log file. Zero values use package defaults.
type Options struct {
	MaxBytes           int64
	Backups            int
	Perm               os.FileMode
	ExistingParentOnly bool
}

func (o Options) normalized() Options {
	if o.MaxBytes <= 0 {
		o.MaxBytes = DefaultFileMaxBytes
	}
	if o.Backups <= 0 {
		o.Backups = DefaultBackups
	}
	if o.Perm == 0 {
		o.Perm = 0o644
	}
	return o
}

// File is an append-only log whose writes and lifecycle rotation are bounded.
type File struct {
	raw     *os.File
	options Options
	// mu serializes writes and rotation for THIS file. Rotation needs the
	// package-level rotateMu as well, but that lock is taken once per rotation
	// (every MaxBytes), not once per record: a global lock on the write path
	// would serialize every log file in the process behind one another.
	mu sync.Mutex
	// size is the number of bytes this File has written to raw's path. It
	// replaces an fstat per record, so it is a hint, not the truth: it counts
	// only what this File wrote, and another writer touching the same path
	// (the TUI's StartPeriodicRotation does) can make it drift. The drift is
	// bounded either way. A writer that truncates — the TUI's rotation is
	// copy-and-truncate on this same inode — makes size an over-estimate, so
	// at worst this File rotates early. A writer that appends makes size an
	// under-estimate, so the active file can exceed MaxBytes until that
	// writer's own 2-second rotation brings it back. writeLocked re-asks the
	// filesystem only when the counter says a rotation may be due, so no
	// periodic stat timer is needed.
	size int64
}

// Open creates the parent directory, performs startup rotation, and opens an
// append-only bounded log.
func Open(path string, options Options) (*File, error) {
	options = options.normalized()
	raw, err := OpenRaw(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, options.Perm, options)
	if err != nil {
		return nil, err
	}
	f := &File{raw: raw, options: options}
	if info, statErr := raw.Stat(); statErr == nil {
		f.size = info.Size()
	}
	return f, nil
}

// OpenRaw applies the same directory creation and startup rotation policy when
// a caller needs the underlying descriptor for slog or process redirection.
func OpenRaw(path string, flags int, perm os.FileMode, options Options) (*os.File, error) {
	options = options.normalized()
	path = filepath.Clean(path)
	if options.ExistingParentOnly {
		if info, err := os.Stat(filepath.Dir(path)); err != nil || !info.IsDir() {
			if err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("log parent is not a directory: %s", filepath.Dir(path))
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
	}
	RotateOnce(path, options.MaxBytes, options.Backups)
	if perm == 0 {
		perm = options.Perm
	}
	return os.OpenFile(path, flags, perm)
}

// Write appends one bounded record.
//
// Records are written straight to the descriptor: no userspace buffering, and
// deliberately no fsync. Buffering would delay `tail -f` and lose the tail on a
// crash; an fsync per record buys only power-loss durability, which a diagnostic
// log does not need, at the cost of a disk barrier on the caller's thread (the
// LLM request path and the HTTP response path both call this). The package's own
// error.log has always written this way. write(2) already survives process death.
func (f *File) Write(p []byte) (int, error) {
	if f == nil {
		return 0, os.ErrInvalid
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.writeLocked(p)
}

// writeLocked is Write's body, called with f.mu held.
func (f *File) writeLocked(p []byte) (int, error) {
	originalLen := len(p)
	if int64(len(p)) > f.options.MaxBytes {
		budget := int(f.options.MaxBytes)
		if budget > 256 {
			budget -= 128
		}
		p = []byte(BoundText(string(p), budget))
	}
	if f.size+int64(len(p)) > f.options.MaxBytes {
		// f.size counts only what this File wrote, so it can drift when another
		// writer touches the same path (the TUI's StartPeriodicRotation does).
		// Ask the filesystem once, but only when the counter says a rotation may
		// be due — not on every record.
		if info, err := f.raw.Stat(); err == nil {
			f.size = info.Size()
		}
		if f.size+int64(len(p)) > f.options.MaxBytes {
			rotateMu.Lock()
			rotateActiveLocked(f.raw, f.options.MaxBytes, f.options.Backups)
			rotateMu.Unlock()
			f.size = 0
		}
	}
	n, err := f.raw.Write(p)
	f.size += int64(n)
	if err == nil && originalLen != len(p) {
		return originalLen, nil
	}
	return n, err
}

// WriteSection writes one bounded diagnostic block using a consistent format.
func (f *File) WriteSection(section, text string) error {
	_, err := fmt.Fprintf(f, "%s\n==== %s ====\n", BoundText(text, DefaultRecordBytes), section)
	return err
}

func (f *File) Sync() error {
	if f == nil || f.raw == nil {
		return nil
	}
	return f.raw.Sync()
}

func (f *File) Close() error {
	if f == nil || f.raw == nil {
		return nil
	}
	err := f.raw.Close()
	f.raw = nil
	return err
}

// Raw returns the underlying descriptor for APIs such as dup2. Writes made
// through it should use WriteRaw or be covered by StartPeriodicRotation.
func (f *File) Raw() *os.File {
	if f == nil {
		return nil
	}
	return f.raw
}

var (
	rotateMu sync.Mutex
	rotated  = map[string]struct{}{}
)

// RotateOnce performs bounded startup rotation before a process opens a log.
// The per-process guard prevents independent users from renaming a file that
// another Forebrain Harness component has already opened.
func RotateOnce(path string, maxBytes int64, backups int) {
	path = filepath.Clean(path)
	options := (Options{MaxBytes: maxBytes, Backups: backups}).normalized()
	rotateMu.Lock()
	defer rotateMu.Unlock()
	if _, ok := rotated[path]; ok {
		return
	}
	rotated[path] = struct{}{}
	info, err := os.Stat(path)
	if err != nil || info.Size() < options.MaxBytes {
		return
	}
	_ = os.Remove(fmt.Sprintf("%s.%d", path, options.Backups))
	for i := options.Backups - 1; i >= 1; i-- {
		_ = os.Rename(fmt.Sprintf("%s.%d", path, i), fmt.Sprintf("%s.%d", path, i+1))
	}
	_ = os.Rename(path, path+".1")
}

// RawWriter adapts an already-open file to the bounded writer contract.
type RawWriter struct {
	File     *os.File
	MaxBytes int64
	Backups  int
}

func (w RawWriter) Write(p []byte) (int, error) {
	return WriteRaw(w.File, p, w.MaxBytes, w.Backups)
}

func SyncRaw(file *os.File) error {
	if file == nil {
		return nil
	}
	return file.Sync()
}

func CloseRaw(file *os.File) error {
	if file == nil {
		return nil
	}
	return file.Close()
}

// WriteRaw writes one record and rotates before it can push the active file
// over its limit. A pathological single record is bounded as well.
func WriteRaw(file *os.File, p []byte, maxBytes int64, backups int) (int, error) {
	if file == nil {
		return 0, os.ErrInvalid
	}
	options := (Options{MaxBytes: maxBytes, Backups: backups}).normalized()
	originalLen := len(p)
	if int64(len(p)) > options.MaxBytes {
		budget := int(options.MaxBytes)
		if budget > 256 {
			budget -= 128
		}
		p = []byte(BoundText(string(p), budget))
	}
	rotateMu.Lock()
	defer rotateMu.Unlock()
	if info, err := file.Stat(); err == nil && info.Size()+int64(len(p)) > options.MaxBytes {
		rotateActiveLocked(file, options.MaxBytes, options.Backups)
	}
	n, err := file.Write(p)
	if err == nil && originalLen != len(p) {
		return originalLen, nil
	}
	return n, err
}

// RotateActive rotates an open log using copy-and-truncate. It is safe to call
// periodically for output that bypasses Go writers, such as redirected stderr.
func RotateActive(path string, maxBytes int64, backups int) {
	options := (Options{MaxBytes: maxBytes, Backups: backups}).normalized()
	file, err := os.OpenFile(filepath.Clean(path), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer file.Close()
	rotateMu.Lock()
	defer rotateMu.Unlock()
	if info, statErr := file.Stat(); statErr == nil && info.Size() >= options.MaxBytes {
		rotateActiveLocked(file, options.MaxBytes, options.Backups)
	}
}

func rotateActiveLocked(file *os.File, maxBytes int64, backups int) {
	if file == nil {
		return
	}
	path := filepath.Clean(file.Name())
	_ = file.Sync()
	_ = os.Remove(fmt.Sprintf("%s.%d", path, backups))
	for i := backups - 1; i >= 1; i-- {
		_ = os.Rename(fmt.Sprintf("%s.%d", path, i), fmt.Sprintf("%s.%d", path, i+1))
	}
	src, err := os.Open(path)
	if err == nil {
		if info, statErr := src.Stat(); statErr == nil {
			start := info.Size() - maxBytes
			if start < 0 {
				start = 0
			}
			_, _ = src.Seek(start, io.SeekStart)
			if backup, createErr := os.OpenFile(path+".1", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600); createErr == nil {
				_, _ = io.CopyN(backup, src, info.Size()-start)
				_ = backup.Close()
			}
		}
		_ = src.Close()
	}
	_ = file.Truncate(0)
}

// StartPeriodicRotation covers writes that bypass File.Write. The returned
// stop function is idempotent.
func StartPeriodicRotation(paths []string, interval time.Duration) func() {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				for _, path := range paths {
					RotateActive(path, DefaultFileMaxBytes, DefaultBackups)
				}
			case <-stop:
				return
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() { close(stop) })
		<-done
	}
}

// BoundText retains the beginning and end of one record with an explicit
// omission marker.
func BoundText(text string, maxBytes int) string {
	if maxBytes <= 0 {
		maxBytes = DefaultRecordBytes
	}
	if len(text) <= maxBytes {
		return text
	}
	head := maxBytes / 2
	tail := maxBytes - head
	omitted := len(text) - maxBytes
	return text[:head] + fmt.Sprintf("\n... [%d log bytes omitted] ...\n", omitted) + text[len(text)-tail:]
}
