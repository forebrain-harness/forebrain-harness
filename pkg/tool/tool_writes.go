// Tool writes: the ledger that lets a shell card say what ITS command changed.
//
// A shell card's "files it changed" is a snapshot pair — the work tree before
// the command and after it — and a snapshot pair cannot tell the command's own
// writes from a sibling tool's. Tool calls do run together (plan 124 runs the
// concurrency-safe ones in one batch), so a write_file landing inside a
// two-minute `go test` used to be reported as that command's creation. Every
// file-writing tool therefore announces the path it just wrote, and a shell
// card subtracts anything announced inside its own window.
package tool

import (
	"sort"
	"sync"
	"time"
)

type toolWriteLedger struct {
	mu     sync.Mutex
	byPath map[string]time.Time // resolved path → when a tool last wrote it
}

var toolWrites = &toolWriteLedger{byPath: map[string]time.Time{}}

const (
	// toolWriteRetention is how long an entry is kept: long enough to cover the
	// longest shell command a reader would wait for, short enough that the map
	// cannot grow without bound.
	toolWriteRetention  = 30 * time.Minute
	toolWriteMaxEntries = 4096
)

// noteToolFileWrite records that a tool (not a shell command) wrote absPath at
// at. Called from the one place every file-writing tool passes through.
func noteToolFileWrite(absPath string, at time.Time) {
	resolved := resolveForLedger(absPath)
	if resolved == "" {
		return
	}
	l := toolWrites
	l.mu.Lock()
	defer l.mu.Unlock()
	l.byPath[resolved] = at
	l.pruneLocked(at)
}

// toolWritesBetween returns the resolved paths a tool wrote in (from, to].
func toolWritesBetween(from, to time.Time) map[string]struct{} {
	l := toolWrites
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make(map[string]struct{}, len(l.byPath))
	for path, at := range l.byPath {
		if at.After(from) && !at.After(to) {
			out[path] = struct{}{}
		}
	}
	return out
}

// pruneLocked drops expired entries and, while still over the cap, the oldest
// ones. Callers hold the ledger mutex.
func (l *toolWriteLedger) pruneLocked(now time.Time) {
	for path, at := range l.byPath {
		if now.Sub(at) > toolWriteRetention {
			delete(l.byPath, path)
		}
	}
	if len(l.byPath) <= toolWriteMaxEntries {
		return
	}
	type aged struct {
		path string
		at   time.Time
	}
	entries := make([]aged, 0, len(l.byPath))
	for path, at := range l.byPath {
		entries = append(entries, aged{path: path, at: at})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].at.Before(entries[j].at) })
	for _, e := range entries[:len(entries)-toolWriteMaxEntries] {
		delete(l.byPath, e.path)
	}
}

// resolveForLedger resolves the PARENT directory's symlinks (the file itself may
// not exist after a delete) so the ledger and the shell snapshot name a path the
// same way: this repository has been bitten by /var vs /private/var before.
// normalizeRootPath is exactly that resolution — whole path when it exists,
// nearest existing ancestor otherwise.
func resolveForLedger(absPath string) string {
	return normalizeRootPath(absPath)
}
