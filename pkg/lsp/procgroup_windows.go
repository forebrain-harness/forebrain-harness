//go:build windows

package lsp

import (
	"fmt"
	"os/exec"
	"unsafe"

	"golang.org/x/sys/windows"
)

// prepareCommand is a no-op on Windows: the Job Object created in attachTree
// owns the whole process tree instead.
func prepareCommand(cmd *exec.Cmd) {}

// processTree is a started server's process tree: a Job Object whose
// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE flag kills every assigned process when
// forebrain exits or the handle closes (spec §7.8).
type processTree struct {
	job windows.Handle
}

// attachTree creates the job object and assigns the started process to it.
func attachTree(cmd *exec.Cmd) (*processTree, error) {
	if cmd.Process == nil {
		return nil, fmt.Errorf("lsp: command has not started")
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		windows.CloseHandle(job)
		return nil, err
	}
	proc, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		windows.CloseHandle(job)
		return nil, err
	}
	defer windows.CloseHandle(proc)
	if err := windows.AssignProcessToJobObject(job, proc); err != nil {
		windows.CloseHandle(job)
		return nil, err
	}
	return &processTree{job: job}, nil
}

// pgidOf reports the process-group id; Windows has none, so 0.
func (t *processTree) pgidOf() int { return 0 }

func (t *processTree) terminate() error {
	if t == nil || t.job == 0 {
		return nil
	}
	return windows.TerminateJobObject(t.job, 1)
}

func (t *processTree) kill() error {
	if t == nil || t.job == 0 {
		return nil
	}
	return windows.TerminateJobObject(t.job, 1)
}

// release closes the job handle, so KILL_ON_JOB_CLOSE also takes care of any
// survivor a TerminateJobObject race could have missed.
func (t *processTree) release() {
	if t == nil || t.job == 0 {
		return
	}
	windows.CloseHandle(t.job)
	t.job = 0
}

// pidRecord mirrors the Unix entry of pids.json; Windows never writes one.
type pidRecord struct {
	Server  string `json:"server"`
	PID     int    `json:"pid"`
	PGID    int    `json:"pgid"`
	Started string `json:"started"`
}

// processStarted is "" on Windows: there is no ps to ask, and the job object
// already ties every server's lifetime to forebrain's.
func processStarted(pid int) string { return "" }

// recordPID is a no-op on Windows: job objects leave no orphans.
func recordPID(pidFile string, rec pidRecord) {}

// forgetPID is a no-op on Windows.
func forgetPID(pidFile string, pid int) {}

// sweepOrphans is a no-op on Windows: KILL_ON_JOB_CLOSE killed every server
// tree the moment the previous forebrain exited.
func sweepOrphans(pidFile string) {}
