package peer

import (
	"net"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
)

// procStartLayout is the layout of `ps -o lstart=` in the C locale with TZ=UTC:
// "Fri Oct 30 06:47:30 2026", with the day of month space-padded.
const procStartLayout = "Mon Jan _2 15:04:05 2006"

// How long a process start time is cached. A live process keeps its start time
// for its whole life, but a dead pid can be reused, so a hit is trusted for a
// minute and a failure is retried after five seconds.
const (
	procStartHitTTL  = 60 * time.Second
	procStartMissTTL = 5 * time.Second
)

// aliveDialTimeout is how long the online test waits for a peer's socket.
const aliveDialTimeout = 250 * time.Millisecond

// procStartEntry is one cached start time.
type procStartEntry struct {
	value string
	ok    bool
	at    time.Time
}

var procStartCache struct {
	sync.Mutex
	entries map[int]procStartEntry
}

// ProcStart is the process start time in the exact form Claude Code records —
// "Fri Oct 30 06:47:30 2026", the trimmed output of
// `LC_ALL=C TZ=UTC ps -o lstart= -p <pid>` — read in-process, never by running
// ps. It is empty for a process that no longer exists.
func ProcStart(pid int) (string, bool) {
	if pid <= 0 {
		return "", false
	}
	now := time.Now()
	procStartCache.Lock()
	entry, cached := procStartCache.entries[pid]
	procStartCache.Unlock()
	if cached {
		ttl := procStartHitTTL
		if !entry.ok {
			ttl = procStartMissTTL
		}
		if now.Sub(entry.at) < ttl {
			return entry.value, entry.ok
		}
	}

	value, ok := procStartSys(pid)

	procStartCache.Lock()
	if procStartCache.entries == nil {
		procStartCache.entries = map[int]procStartEntry{}
	}
	procStartCache.entries[pid] = procStartEntry{value: value, ok: ok, at: now}
	procStartCache.Unlock()
	return value, ok
}

// pidDomain is computed once: it identifies "which pid namespace am I in", so
// it cannot change while the process runs.
var pidDomain struct {
	sync.Once
	value string
}

// PIDDomain identifies this process's pid namespace. Two records from different
// domains may carry the same pid and still be different processes, so the online
// test skips the pid check between them.
//
// The Linux form is literally prefixed "darwin" because that is what Claude
// Code writes (a Windows-shaped value behind a shared prefix); it looks like a
// typo and is not one. An unreadable component leaves an empty placeholder, so
// the shape is always two colons.
func PIDDomain() string {
	pidDomain.Do(func() {
		switch runtime.GOOS {
		case "darwin":
			pidDomain.value = "darwin"
		case "linux":
			pidDomain.value = "darwin:" + machineID() + ":" + pidNamespace()
		}
	})
	return pidDomain.value
}

// machineID is /etc/machine-id, trimmed, or empty when unreadable.
func machineID() string {
	data, err := os.ReadFile("/etc/machine-id")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// pidNamespace is the target of this process's pid namespace symlink, or empty
// when unreadable.
func pidNamespace() string {
	target, err := os.Readlink("/proc/self/ns/pid")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(target)
}

// Alive reports whether a registration record still describes a running
// process. A record from another pid domain is taken at its word (its pid says
// nothing about ours); otherwise the pid must exist, the recorded start time
// must still match (a mismatch means the pid was reused), and a recorded
// socket must accept a connection.
func Alive(r Record) bool {
	if r.PIDDomain != "" && r.PIDDomain != PIDDomain() {
		return true
	}
	if !processExists(r.PID) {
		return false
	}
	if r.ProcStart != "" {
		current, ok := ProcStart(r.PID)
		if !ok || current != r.ProcStart {
			return false
		}
	}
	if r.MessagingSocketPath != "" {
		return socketAlive(r.MessagingSocketPath)
	}
	return true
}

// socketAlive dials a peer's socket with the short probe timeout. A busy
// socket (EBUSY) answers the same question as an accepted connection: someone is
// listening.
func socketAlive(path string) bool {
	conn, err := net.DialTimeout("unix", path, aliveDialTimeout)
	if err == nil {
		conn.Close()
		return true
	}
	return isBusy(err)
}
