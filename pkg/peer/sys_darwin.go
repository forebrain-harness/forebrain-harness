//go:build darwin

package peer

import (
	"errors"
	"net"
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

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

// procStartSys reads a process start time from the kernel, formatted exactly
// like `LC_ALL=C TZ=UTC ps -o lstart= -p <pid>` trims to — the string Claude
// Code puts in procStart. Nothing is executed: the protocol may not depend on
// another process or on PATH.
func procStartSys(pid int) (string, bool) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || info == nil {
		return "", false
	}
	return time.Unix(int64(info.Proc.P_starttime.Sec), 0).UTC().Format(procStartLayout), true
}

// peerCreds asks the kernel who is on the other end of a local socket.
func peerCreds(conn *net.UnixConn) (pid, uid int, ok bool) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, 0, false
	}
	var (
		cred            *unix.Xucred
		peerID          int
		credErr, pidErr error
	)
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		peerID, pidErr = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	}); err != nil {
		return 0, 0, false
	}
	if credErr != nil || cred == nil {
		return 0, 0, false
	}
	if pidErr != nil {
		peerID = 0
	}
	return peerID, int(cred.Uid), true
}
