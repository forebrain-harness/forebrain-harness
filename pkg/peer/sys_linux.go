//go:build linux

package peer

import (
	"bytes"
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// userHZ is the unit of /proc/<pid>/stat's starttime, fixed at 100 on Linux.
const userHZ = 100

// selfUID is this process's user id, the only user whose connections and
// directories the protocol serves.
func selfUID() int { return os.Getuid() }

// fileOwnerUID reads the owner of a directory entry.
func fileOwnerUID(info os.FileInfo) (int, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(stat.Uid), true
}

// processExists reports whether pid names a live process; a process we may not
// signal (EPERM) exists all the same.
func processExists(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// procStartSys reads a process start time from /proc, formatted exactly like
// `LC_ALL=C TZ=UTC ps -o lstart= -p <pid>` trims to — the string Claude Code
// puts in procStart. Nothing is executed: the protocol may not depend on
// another process or on PATH.
func procStartSys(pid int) (string, bool) {
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return "", false
	}
	// Field 2 is the executable name in parentheses and may itself contain
	// spaces, so the fields after the last ')' are the ones that count.
	end := bytes.LastIndexByte(stat, ')')
	if end < 0 {
		return "", false
	}
	fields := strings.Fields(string(stat[end+1:]))
	// fields[0] is field 3 (state), so field 22 (starttime) is fields[19].
	if len(fields) < 20 {
		return "", false
	}
	start, err := strconv.ParseInt(fields[19], 10, 64)
	if err != nil {
		return "", false
	}
	boot, ok := bootTime()
	if !ok {
		return "", false
	}
	return time.Unix(boot+start/userHZ, 0).UTC().Format(procStartLayout), true
}

// bootTime is the kernel boot time in seconds since the epoch.
func bootTime() (int64, bool) {
	stat, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(stat), "\n") {
		rest, ok := strings.CutPrefix(line, "btime ")
		if !ok {
			continue
		}
		boot, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64)
		if err != nil {
			return 0, false
		}
		return boot, true
	}
	return 0, false
}

// peerCreds asks the kernel who is on the other end of a local socket.
func peerCreds(conn *net.UnixConn) (pid, uid int, ok bool) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, 0, false
	}
	var (
		cred    *unix.Ucred
		credErr error
	)
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return 0, 0, false
	}
	if credErr != nil || cred == nil {
		return 0, 0, false
	}
	return int(cred.Pid), int(cred.Uid), true
}
