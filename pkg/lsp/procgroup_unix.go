//go:build unix

package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// prepareCommand configures cmd so its whole process tree can be signalled:
// the server becomes its own process-group leader (spec §7.8).
func prepareCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// processTree is a started server's process tree: the server runs in its own
// process group, so signalling -pgid reaches every descendant.
type processTree struct {
	pgid int
}

// attachTree records the started command's process group.
func attachTree(cmd *exec.Cmd) (*processTree, error) {
	if cmd.Process == nil {
		return nil, fmt.Errorf("lsp: command has not started")
	}
	return &processTree{pgid: cmd.Process.Pid}, nil
}

// pgidOf reports the recorded process-group id (0 is "none").
func (t *processTree) pgidOf() int {
	if t == nil {
		return 0
	}
	return t.pgid
}

func (t *processTree) terminate() error { return signalGroup(t.pgid, syscall.SIGTERM) }

func (t *processTree) kill() error { return signalGroup(t.pgid, syscall.SIGKILL) }

// release is a no-op on Unix: there is no job handle to drop.
func (t *processTree) release() {}

// signalGroup signals a whole process group. A group that is already gone
// (ESRCH) is success — the goal is that nothing is left, not that the signal
// landed.
func signalGroup(pgid int, sig syscall.Signal) error {
	if pgid <= 0 {
		return nil
	}
	if err := syscall.Kill(-pgid, sig); err != nil && err != syscall.ESRCH {
		return err
	}
	return nil
}

// processStarted identifies a process start for orphan checks: it is the
// start time ps reports, "" when the process no longer exists. Comparing it
// keeps sweepOrphans from killing an unrelated process that reused the pid.
func processStarted(pid int) string {
	if pid <= 0 {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return ""
	}
	return strings.Join(strings.Fields(string(out)), " ")
}

// pidRecord is one entry of pids.json (spec §5.4, §7.8).
type pidRecord struct {
	Server  string `json:"server"`
	PID     int    `json:"pid"`
	PGID    int    `json:"pgid"`
	Started string `json:"started"`
}

// pidFileMu serializes every pids.json access (spec §5.4: writes are atomic
// temp-file-plus-rename; the mutex keeps concurrent instances in line).
var pidFileMu sync.Mutex

// recordPID appends rec to pidFile, stamping the process start time so a later
// sweep can tell this server from a pid reuser.
func recordPID(pidFile string, rec pidRecord) {
	if pidFile == "" {
		return
	}
	pidFileMu.Lock()
	defer pidFileMu.Unlock()
	if rec.Started == "" {
		rec.Started = processStarted(rec.PID)
	}
	recs := readPIDRecords(pidFile)
	writePIDRecords(pidFile, append(recs, rec))
}

// forgetPID removes the record with pid from pidFile.
func forgetPID(pidFile string, pid int) {
	if pidFile == "" {
		return
	}
	pidFileMu.Lock()
	defer pidFileMu.Unlock()
	recs := readPIDRecords(pidFile)
	kept := make([]pidRecord, 0, len(recs))
	for _, r := range recs {
		if r.PID != pid {
			kept = append(kept, r)
		}
	}
	writePIDRecords(pidFile, kept)
}

// sweepOrphans kills recorded process groups left by a previous forebrain
// that died without shutting its servers down, then removes their records
// (spec §7.8). Orphan bookkeeping is best effort: unreadable records are
// dropped, and only a record whose process still reports the same start time
// is killed.
func sweepOrphans(pidFile string) {
	if pidFile == "" {
		return
	}
	pidFileMu.Lock()
	defer pidFileMu.Unlock()
	for _, rec := range readPIDRecords(pidFile) {
		if rec.Started == "" || rec.PGID <= 0 {
			continue
		}
		if processStarted(rec.PID) != rec.Started {
			continue // the pid now belongs to an unrelated process
		}
		_ = syscall.Kill(-rec.PGID, syscall.SIGKILL)
	}
	writePIDRecords(pidFile, nil)
}

// readPIDRecords reads pidFile; a missing or malformed file is no records.
func readPIDRecords(pidFile string) []pidRecord {
	b, err := os.ReadFile(pidFile)
	if err != nil {
		return nil
	}
	var recs []pidRecord
	if err := json.Unmarshal(b, &recs); err != nil {
		return nil
	}
	return recs
}

// writePIDRecords atomically replaces pidFile with recs.
func writePIDRecords(pidFile string, recs []pidRecord) {
	if recs == nil {
		recs = []pidRecord{}
	}
	b, err := json.Marshal(recs)
	if err != nil {
		return
	}
	dir := filepath.Dir(pidFile)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	tmp, err := os.CreateTemp(dir, ".pids-*")
	if err != nil {
		return
	}
	name := tmp.Name()
	_, writeErr := tmp.Write(b)
	closeErr := tmp.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(name)
		return
	}
	if err := os.Rename(name, pidFile); err != nil {
		_ = os.Remove(name)
	}
}
