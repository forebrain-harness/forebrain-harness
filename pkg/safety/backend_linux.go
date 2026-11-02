//go:build linux

package safety

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/home"
)

func runBubblewrapCommand(ctx context.Context, cfg *appcfg.Root, req CommandRequest, workDir string) (CommandResult, error) {
	bwrapPath, err := lookBubblewrap()
	if err != nil {
		return CommandResult{}, &UnavailableError{Reason: "bubblewrap_not_found"}
	}
	markers, err := prepareLinuxMissingDenyMarkers(req)
	if err != nil {
		return CommandResult{}, err
	}
	defer cleanupLinuxMissingDenyMarkers(markers)
	for _, marker := range markers {
		req.DeniedReadablePaths = appendUniquePath(req.DeniedReadablePaths, marker.path)
	}
	cmd := exec.Command(bwrapPath, buildBubblewrapArgs(cfg, req, workDir)...)
	cmd.Env = home.SafeSubprocessEnv(nil, home.Options{ExplicitEnv: explicitCommandEnv(req.Env, nil)})
	capture := newCommandCapture(req)
	cmd.Stdout = capture.writer(OutputStreamStdout)
	cmd.Stderr = capture.writer(OutputStreamStderr)
	setupProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		_ = capture.result()
		return CommandResult{ExitCode: 1}, err
	}
	err, stopErr := waitForCommandContext(ctx, cmd)
	return finalizeExec(capture.result(), err, stopErr)
}

func finalizeExec(res CommandResult, err, stopErr error) (CommandResult, error) {
	res.ExitCode = 0
	if err == nil {
		return res, nil
	}
	if errors.Is(stopErr, context.Canceled) || errors.Is(stopErr, context.DeadlineExceeded) {
		if errors.Is(stopErr, context.DeadlineExceeded) {
			res.ExitCode = 124
			return res, &TimeoutError{Result: res}
		}
		res.ExitCode = 1
		return res, context.Canceled
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			signal := int(status.Signal())
			res.ExitCode = 128 + signal
			return res, &SignalError{Signal: signal}
		}
		res.ExitCode = exitErr.ExitCode()
		return res, nil
	}
	res.ExitCode = 1
	return res, err
}

func buildBubblewrapArgs(cfg *appcfg.Root, req CommandRequest, workDir string) []string {
	args := bubblewrapBaseArgs(cfg, req, workDir)
	return append(args, "/bin/sh", "-lc", req.Command)
}

func bubblewrapBaseArgs(cfg *appcfg.Root, req CommandRequest, workDir string) []string {
	req = constrainRequestToFilesystemProfile(req, workDir)
	args := []string{
		"--die-with-parent",
		"--new-session",
		"--unshare-pid",
		"--unshare-uts",
		"--unshare-ipc",
	}
	if !req.AllowNetwork {
		args = append(args, "--unshare-net")
	}
	if filesystemRulesContainRootRead(req) {
		args = append(args, "--ro-bind", "/", "/")
	} else {
		args = append(args, "--tmpfs", "/")
	}
	args = append(args, "--proc", "/proc", "--dev", "/dev")
	args = appendFilesystemPolicyArgs(args, req)
	return append(args, "--chdir", workDir)
}

type filesystemMountAccess uint8

const (
	filesystemMountRead filesystemMountAccess = iota
	filesystemMountWrite
	filesystemMountDeny
)

type filesystemMountRule struct {
	path     string
	access   filesystemMountAccess
	priority uint8
}

var linuxPlatformDefaultReadRoots = []string{
	"/bin", "/sbin", "/usr", "/etc", "/lib", "/lib64", "/nix/store", "/run/current-system/sw",
}

func filesystemRulesContainRootRead(req CommandRequest) bool {
	for _, paths := range [][]string{req.AdditionalReadablePaths, req.AdditionalWritablePaths} {
		for _, raw := range paths {
			if filepath.Clean(strings.TrimSpace(raw)) == string(filepath.Separator) {
				return true
			}
		}
	}
	return false
}

func appendFilesystemPolicyArgs(args []string, req CommandRequest) []string {
	var rules []filesystemMountRule
	if req.IncludePlatformDefaults {
		for _, path := range linuxPlatformDefaultReadRoots {
			rules = append(rules, filesystemMountRule{path: path, access: filesystemMountRead, priority: 0})
		}
	}
	for _, path := range req.AdditionalReadablePaths {
		rules = append(rules, filesystemMountRule{path: path, access: filesystemMountRead, priority: 0})
	}
	for _, path := range req.AdditionalWritablePaths {
		rules = append(rules, filesystemMountRule{path: path, access: filesystemMountWrite, priority: 1})
	}
	for _, path := range req.DeniedWritablePaths {
		rules = append(rules, filesystemMountRule{path: path, access: filesystemMountRead, priority: 2})
	}
	for _, path := range req.DeniedReadablePaths {
		rules = append(rules, filesystemMountRule{path: path, access: filesystemMountDeny, priority: 3})
	}
	// A deny-list request's boundary roots are denied the same way as a
	// deny_read entry: a tmpfs over each one. Its readable exceptions are
	// ordinary read rules, and the depth-sorted mount order is what makes them
	// exceptions at all — bubblewrap stacks later mounts on earlier ones, so a
	// bind nested under a tmpfs re-opens exactly that subpath (plans/216).
	for _, path := range req.DeniedReadableRoots {
		rules = append(rules, filesystemMountRule{path: path, access: filesystemMountDeny, priority: 3})
	}
	for _, path := range req.ReadableExceptions {
		rules = append(rules, filesystemMountRule{path: path, access: filesystemMountRead, priority: 0})
	}

	normalized := make([]filesystemMountRule, 0, len(rules))
	seen := map[string]filesystemMountRule{}
	for _, rule := range rules {
		path := strings.TrimSpace(rule.path)
		if path == "" {
			continue
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			continue
		}
		abs = filepath.Clean(abs)
		rule.path = abs
		if current, ok := seen[abs]; ok && current.priority >= rule.priority {
			continue
		}
		seen[abs] = rule
	}
	for _, rule := range seen {
		normalized = append(normalized, rule)
	}
	sort.Slice(normalized, func(i, j int) bool {
		leftDepth := pathComponentCount(normalized[i].path)
		rightDepth := pathComponentCount(normalized[j].path)
		if leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		if normalized[i].path != normalized[j].path {
			return normalized[i].path < normalized[j].path
		}
		return normalized[i].access < normalized[j].access
	})
	var deferredRemounts []string
	for _, rule := range normalized {
		if rule.path == string(filepath.Separator) && rule.access == filesystemMountRead {
			continue
		}
		info, err := os.Stat(rule.path)
		if err != nil {
			continue
		}
		switch rule.access {
		case filesystemMountRead:
			args = append(args, "--ro-bind", rule.path, rule.path)
		case filesystemMountWrite:
			args = append(args, "--bind", rule.path, rule.path)
		case filesystemMountDeny:
			if info.IsDir() {
				args = append(args, "--tmpfs", rule.path)
				if deniedDirHasNestedRule(normalized, rule.path) {
					// bwrap needs a mount point inside the tmpfs for each nested
					// bind, and a read-only mount has none to offer: remounting the
					// parent read-only before its children are mounted makes those
					// mounts fail. Defer the remount until they exist. The nested
					// binds are independent mounts, so making the parent read-only
					// afterwards leaves them writable/readable as each rule asked.
					deferredRemounts = append(deferredRemounts, rule.path)
					continue
				}
				args = append(args, "--remount-ro", rule.path)
			} else {
				args = append(args, "--ro-bind", "/dev/null", rule.path)
			}
		}
	}
	for _, path := range deferredRemounts {
		args = append(args, "--remount-ro", path)
	}
	return args
}

// deniedDirHasNestedRule reports whether another filesystem rule sits strictly
// beneath dir, which is what makes its remount have to wait.
func deniedDirHasNestedRule(rules []filesystemMountRule, dir string) bool {
	for _, other := range rules {
		if other.path == dir {
			continue
		}
		rel, err := filepath.Rel(dir, other.path)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		return true
	}
	return false
}

type linuxMissingDenyMarker struct {
	path  string
	isDir bool
	dev   uint64
	ino   uint64
}

func prepareLinuxMissingDenyMarkers(req CommandRequest) ([]linuxMissingDenyMarker, error) {
	var markers []linuxMissingDenyMarker
	seen := map[string]struct{}{}
	for _, raw := range append(append([]string(nil), req.DeniedWritablePaths...), req.DeniedReadablePaths...) {
		target, err := filepath.Abs(strings.TrimSpace(raw))
		if err != nil || target == string(filepath.Separator) || target == "" {
			continue
		}
		target = filepath.Clean(target)
		if _, ok := seen[target]; ok {
			continue
		}
		seen[target] = struct{}{}
		if _, err := os.Lstat(target); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			cleanupLinuxMissingDenyMarkers(markers)
			return nil, err
		}
		if !linuxPathMayBeWritten(req, target) {
			continue
		}
		missing, final, err := firstMissingLinuxPathComponent(target)
		if err != nil {
			cleanupLinuxMissingDenyMarkers(markers)
			return nil, err
		}
		marker, created, err := createLinuxMissingDenyMarker(missing, !final)
		if err != nil {
			cleanupLinuxMissingDenyMarkers(markers)
			return nil, fmt.Errorf("protect missing denied path %s: %w", target, err)
		}
		if created {
			markers = append(markers, marker)
		}
	}
	return markers, nil
}

func linuxPathMayBeWritten(req CommandRequest, target string) bool {
	if req.Profile.normalized() == ProfileWorkspaceWrite {
		workDir, err := filepath.Abs(strings.TrimSpace(req.WorkDir))
		if err == nil && linuxPathContains(filepath.Clean(workDir), target) {
			return true
		}
	}
	for _, raw := range req.AdditionalWritablePaths {
		root, err := filepath.Abs(strings.TrimSpace(raw))
		if err == nil && linuxPathContains(filepath.Clean(root), target) {
			return true
		}
	}
	return false
}

func linuxPathContains(root, target string) bool {
	if root == target {
		return true
	}
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func firstMissingLinuxPathComponent(target string) (string, bool, error) {
	volume := filepath.VolumeName(target)
	current := volume + string(filepath.Separator)
	components := strings.Split(strings.TrimPrefix(target, current), string(filepath.Separator))
	for index, component := range components {
		if component == "" {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return current, index == len(components)-1, nil
		}
		if err != nil {
			return "", false, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", false, fmt.Errorf("path ancestor is a symbolic link: %s", current)
		}
		if !info.IsDir() && index != len(components)-1 {
			return "", false, fmt.Errorf("path ancestor is not a directory: %s", current)
		}
	}
	return target, true, nil
}

func createLinuxMissingDenyMarker(path string, directory bool) (linuxMissingDenyMarker, bool, error) {
	if directory {
		if err := os.Mkdir(path, 0o000); err != nil {
			if os.IsExist(err) {
				return linuxMissingDenyMarker{}, false, nil
			}
			return linuxMissingDenyMarker{}, false, err
		}
	} else {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o000)
		if err != nil {
			if os.IsExist(err) {
				return linuxMissingDenyMarker{}, false, nil
			}
			return linuxMissingDenyMarker{}, false, err
		}
		if err := file.Close(); err != nil {
			_ = os.Remove(path)
			return linuxMissingDenyMarker{}, false, err
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return linuxMissingDenyMarker{}, false, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		_ = os.Remove(path)
		return linuxMissingDenyMarker{}, false, fmt.Errorf("path identity is unavailable")
	}
	return linuxMissingDenyMarker{path: path, isDir: directory, dev: uint64(stat.Dev), ino: stat.Ino}, true, nil
}

func cleanupLinuxMissingDenyMarkers(markers []linuxMissingDenyMarker) {
	for index := len(markers) - 1; index >= 0; index-- {
		marker := markers[index]
		info, err := os.Lstat(marker.path)
		if err != nil || info.IsDir() != marker.isDir || info.Mode()&os.ModeSymlink != 0 || (!marker.isDir && info.Size() != 0) {
			continue
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || uint64(stat.Dev) != marker.dev || stat.Ino != marker.ino {
			continue
		}
		_ = os.Chmod(marker.path, 0o700)
		_ = os.Remove(marker.path)
	}
}
