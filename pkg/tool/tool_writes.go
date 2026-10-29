// Tool writes: the ledger that lets a shell card say what ITS command changed.
//
// A shell card's "files it changed" is a snapshot pair — the work tree before
// the command and after it — and a snapshot pair cannot tell the command's own
// writes from anybody else's. Two ordinary situations make that difference
// visible, and only one of them is inside a process: tool calls run together
// (plan 124 batches the concurrency-safe ones), and a second forebrain writes
// the same work tree at the same time — two terminals, or a terminal and the
// gateway, each with its own home (plan 186: a `go vet` card claimed the
// git_install.go edit another session was writing). Every file-writing tool
// therefore announces the path it just wrote, and a shell card subtracts
// whatever was announced inside its own window — whoever announced it.
//
// That "whoever" is why this is a directory rather than a map. The log is
// append-only, one shard per minute, in a directory keyed to the USER and not
// to an installation: two forebrains started with different FOREBRAIN_HOME
// values still share the work tree they write, so they must share the ledger.
// A single line appended with O_APPEND is atomic on POSIX and needs no lock;
// Windows opens the file with FILE_APPEND_DATA, where one WriteFile appends
// without truncating another writer's bytes. A shard whose minute is over is
// never appended to again, so any process may delete it.
//
// Nothing here may fail loudly. A home or cache directory that cannot be
// resolved, a shard that cannot be written or read — each is simply "no
// announcement was seen", which is the behaviour without a ledger at all: the
// card may over-report a change, but the tool's own result is untouched.
package tool

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// toolWriteRetention is how long an announcement is kept: long enough to
	// cover the longest shell command a reader would wait for, short enough
	// that the directory cannot grow without bound.
	toolWriteRetention = 30 * time.Minute
	// toolWriteSweepEvery keeps each process from scanning the directory more
	// than once a minute.
	toolWriteSweepEvery = time.Minute
	// toolWriteShardSuffix names one minute of announcements.
	toolWriteShardSuffix = ".log"
	// toolWriteDirEnv overrides toolWriteLedgerDir. Tests install it so a run
	// neither writes to nor reads from the machine-wide directory.
	toolWriteDirEnv = "FOREBRAIN_TOOL_WRITES_DIR"
)

// toolWriteLedgerDir is the directory every forebrain of this user shares,
// whatever FOREBRAIN_HOME each was started with. os.UserCacheDir picks the
// platform's own per-user cache directory (~/Library/Caches on macOS,
// $XDG_CACHE_HOME or ~/.cache elsewhere, %LocalAppData% on Windows), so the
// ledger is private to the user, exists with no setup, and survives the
// process that wrote it.
func toolWriteLedgerDir() (string, bool) {
	if override := strings.TrimSpace(os.Getenv(toolWriteDirEnv)); override != "" {
		return filepath.Clean(override), true
	}
	cache, err := os.UserCacheDir()
	if err != nil || strings.TrimSpace(cache) == "" {
		return "", false
	}
	return filepath.Join(cache, "forebrain", "tool-writes"), true
}

func toolWriteShardName(minute int64) string {
	return strconv.FormatInt(minute, 10) + toolWriteShardSuffix
}

// toolWriteShardMinute reads the minute a shard file names; anything else in
// the directory is not ours to interpret.
func toolWriteShardMinute(name string) (int64, bool) {
	digits, ok := strings.CutSuffix(name, toolWriteShardSuffix)
	if !ok {
		return 0, false
	}
	minute, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return 0, false
	}
	return minute, true
}

// noteToolFileWrite records that a tool (not a shell command) wrote absPath at
// at. Called from the one place every file-writing tool passes through.
func noteToolFileWrite(absPath string, at time.Time) {
	resolved := resolveForLedger(absPath)
	if resolved == "" {
		return
	}
	dir, ok := toolWriteLedgerDir()
	if !ok {
		return
	}
	appendToolFileWrite(dir, resolved, at)
}

// appendToolFileWrite adds one line — <unixNano>\t<quoted path> — to the shard
// for at's minute, then sweeps expired shards. Quoting keeps a path that holds
// a newline or a tab from breaking the line structure, and a reader that
// catches a torn line simply ignores it.
func appendToolFileWrite(dir, resolved string, at time.Time) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	line := strconv.FormatInt(at.UnixNano(), 10) + "\t" + strconv.Quote(resolved) + "\n"
	f, err := os.OpenFile(filepath.Join(dir, toolWriteShardName(at.Unix()/60)),
		os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	if _, err := f.WriteString(line); err != nil {
		f.Close()
		return
	}
	if err := f.Close(); err != nil {
		return
	}
	sweepToolWriteShardsOnce(dir, at)
}

// The sweep is rate-limited per process: an announcement every few hundred
// milliseconds must not cost a directory scan every time.
var (
	toolWriteSweepMu sync.Mutex
	toolWriteSweptAt time.Time
)

func sweepToolWriteShardsOnce(dir string, now time.Time) {
	toolWriteSweepMu.Lock()
	if !toolWriteSweptAt.IsZero() && now.Sub(toolWriteSweptAt) < toolWriteSweepEvery {
		toolWriteSweepMu.Unlock()
		return
	}
	toolWriteSweptAt = now
	toolWriteSweepMu.Unlock()
	sweepToolWriteShards(dir, now)
}

// sweepToolWriteShards deletes every shard whose minute is past the retention
// window, keeping a minute of slack so a reader that started just before the
// boundary still finds the shards its window covers.
func sweepToolWriteShards(dir string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := now.Add(-toolWriteRetention-time.Minute).Unix() / 60
	for _, entry := range entries {
		minute, ok := toolWriteShardMinute(entry.Name())
		if !ok || minute > cutoff {
			continue
		}
		os.Remove(filepath.Join(dir, entry.Name()))
	}
}

// toolWritesBetween returns the resolved paths a tool wrote in (from, to].
func toolWritesBetween(from, to time.Time) map[string]struct{} {
	out := map[string]struct{}{}
	dir, ok := toolWriteLedgerDir()
	if !ok {
		return out
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	first, last := from.Unix()/60, to.Unix()/60
	for _, entry := range entries {
		minute, ok := toolWriteShardMinute(entry.Name())
		if !ok || minute < first || minute > last {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			nanoText, quoted, ok := strings.Cut(line, "\t")
			if !ok {
				continue
			}
			nano, err := strconv.ParseInt(nanoText, 10, 64)
			if err != nil {
				continue
			}
			path, err := strconv.Unquote(quoted)
			if err != nil || path == "" {
				continue
			}
			if at := time.Unix(0, nano); at.After(from) && !at.After(to) {
				out[path] = struct{}{}
			}
		}
	}
	return out
}

// resolveForLedger resolves the PARENT directory's symlinks (the file itself may
// not exist after a delete) so the ledger and the shell snapshot name a path the
// same way: this repository has been bitten by /var vs /private/var before.
// normalizeRootPath is exactly that resolution — whole path when it exists,
// nearest existing ancestor otherwise.
func resolveForLedger(absPath string) string {
	return normalizeRootPath(absPath)
}
