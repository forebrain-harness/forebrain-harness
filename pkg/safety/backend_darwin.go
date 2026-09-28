//go:build darwin

package safety

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/home"
)

func runSeatbeltCommand(ctx context.Context, cfg *appcfg.Root, req CommandRequest, workDir string) (CommandResult, error) {
	profile, err := seatbeltProfile(cfg, req, workDir)
	if err != nil {
		return CommandResult{}, err
	}
	profFile, err := os.CreateTemp("", "forebrain-seatbelt-*.sb")
	if err != nil {
		return CommandResult{}, err
	}
	profPath := profFile.Name()
	if _, err := profFile.WriteString(profile); err != nil {
		_ = profFile.Close()
		_ = os.Remove(profPath)
		return CommandResult{}, err
	}
	_ = profFile.Close()
	defer os.Remove(profPath)

	runCtx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()
	cmd := exec.Command("sandbox-exec", "-f", profPath, "/bin/sh", "-lc", req.Command)
	cmd.Dir = workDir
	cmd.Env = home.SafeSubprocessEnv(nil, home.Options{ExplicitEnv: explicitCommandEnv(req.Env, nil)})
	capture := newCommandCapture(req)
	cmd.Stdout = capture.writer(OutputStreamStdout)
	cmd.Stderr = capture.writer(OutputStreamStderr)
	setupProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		_ = capture.result()
		return CommandResult{ExitCode: 1}, err
	}
	err, stopErr := waitForCommandContext(runCtx, cmd)
	res := capture.result()
	res.ExitCode = 0
	if err != nil {
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
	return res, nil
}

func seatbeltProfile(cfg *appcfg.Root, req CommandRequest, workDir string) (string, error) {
	req = constrainRequestToFilesystemProfile(req, workDir)
	allowWrites := append([]string(nil), req.AdditionalWritablePaths...)
	if req.Profile == ProfileWorkspaceWrite {
		if cfg == nil || !cfg.SandboxWorkspaceWrite.ExcludeTmpdirEnvVar {
			allowWrites = append(allowWrites, os.TempDir())
		}
		if cfg == nil || !cfg.SandboxWorkspaceWrite.ExcludeSlashTmp {
			allowWrites = append(allowWrites, "/tmp")
		}
	}
	denyWrites := append([]string{}, req.DeniedWritablePaths...)

	allowReads := append([]string{}, req.AdditionalReadablePaths...)
	denyReads := append([]string{}, req.DeniedReadablePaths...)

	normalizedAllow := normalizeSeatbeltPaths(allowWrites)
	normalizedDeny := normalizeSeatbeltPaths(denyWrites)
	normalizedAllowRead := normalizeSeatbeltPaths(allowReads)
	normalizedDenyRead := normalizeSeatbeltPaths(denyReads)
	for _, path := range normalizedDenyRead {
		normalizedDeny = appendUniquePath(normalizedDeny, path)
	}

	var b strings.Builder
	b.WriteString(seatbeltBasePolicy)
	b.WriteByte('\n')
	for _, p := range normalizedAllowRead {
		writeSeatbeltAccessRule(&b, "file-read* file-test-existence", p, normalizedDenyRead)
	}
	for _, p := range normalizedAllow {
		writeSeatbeltAccessRule(&b, "file-read* file-test-existence", p, normalizedDenyRead)
		writeSeatbeltAccessRule(&b, "file-write*", p, normalizedDeny)
	}
	for _, pattern := range req.DeniedReadablePatterns {
		if regex := seatbeltRegexForUnreadableGlob(pattern); regex != "" {
			quoted := strings.ReplaceAll(regex, `"`, `\"`)
			b.WriteString(`(deny file-read* (regex #"`)
			b.WriteString(quoted)
			b.WriteString(`"))`)
			b.WriteByte('\n')
			b.WriteString(`(deny file-write-unlink (regex #"`)
			b.WriteString(quoted)
			b.WriteString(`"))`)
			b.WriteByte('\n')
		}
	}
	if req.IncludePlatformDefaults {
		writeSeatbeltMinimalPolicy(&b, normalizedDenyRead, normalizedDeny)
	}
	if req.ManagedNetwork != nil {
		writeManagedSeatbeltNetworkPolicy(&b, *req.ManagedNetwork)
	} else if req.AllowNetwork {
		b.WriteString("(allow network-outbound)\n(allow network-inbound)\n")
		b.WriteString(seatbeltNetworkSupportPolicy)
	}
	return b.String(), nil
}

func writeManagedSeatbeltNetworkPolicy(b *strings.Builder, network ManagedNetworkCommand) {
	if b == nil {
		return
	}
	if network.AllowLocalBinding {
		b.WriteString("; allow local binding and loopback traffic\n")
		b.WriteString("(allow network-bind (local ip \"*:*\"))\n")
		b.WriteString("(allow network-inbound (local ip \"localhost:*\"))\n")
		b.WriteString("(allow network-outbound (remote ip \"localhost:*\"))\n")
		if network.HTTPPort > 0 || network.SOCKSPort > 0 || network.SOCKSUDPPort > 0 {
			b.WriteString("(allow network-outbound (remote ip \"*:53\"))\n")
		}
	}
	seen := map[int]struct{}{}
	for _, port := range []int{network.HTTPPort, network.SOCKSPort, network.SOCKSUDPPort} {
		if port <= 0 || port > 65535 {
			continue
		}
		if _, ok := seen[port]; ok {
			continue
		}
		seen[port] = struct{}{}
		b.WriteString("(allow network-outbound (remote ip \"localhost:")
		b.WriteString(strconv.Itoa(port))
		b.WriteString("\"))\n")
	}
	if network.AllowAllUnixSockets || len(network.AllowedUnixSockets) > 0 {
		b.WriteString("(allow system-socket (socket-domain AF_UNIX))\n")
		if network.AllowAllUnixSockets {
			b.WriteString("(allow network-bind (local unix-socket))\n")
			b.WriteString("(allow network-outbound (remote unix-socket))\n")
		} else {
			for _, path := range normalizeSeatbeltPaths(network.AllowedUnixSockets) {
				b.WriteString("(allow network-bind (local unix-socket (subpath ")
				b.WriteString(strconv.Quote(path))
				b.WriteString(")))\n")
				b.WriteString("(allow network-outbound (remote unix-socket (subpath ")
				b.WriteString(strconv.Quote(path))
				b.WriteString(")))\n")
			}
		}
	}
	b.WriteString(seatbeltNetworkSupportPolicy)
}

func writeSeatbeltAccessRule(b *strings.Builder, operations, root string, denied []string) {
	if b == nil || strings.TrimSpace(root) == "" {
		return
	}
	b.WriteString("(allow ")
	b.WriteString(operations)
	b.WriteString(" (require-all (subpath ")
	b.WriteString(strconv.Quote(root))
	b.WriteByte(')')
	for _, path := range denied {
		if !seatbeltPathWithin(root, path) {
			continue
		}
		b.WriteString(" (require-not (literal ")
		b.WriteString(strconv.Quote(path))
		b.WriteString(")) (require-not (subpath ")
		b.WriteString(strconv.Quote(path))
		b.WriteString("))")
	}
	b.WriteString("))\n")
}

func seatbeltPathWithin(root, path string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func seatbeltRegexForUnreadableGlob(pattern string) string {
	if pattern == "" {
		return ""
	}
	var b strings.Builder
	b.WriteByte('^')
	sawGlob := false
	runes := []rune(pattern)
	for i := 0; i < len(runes); i++ {
		switch runes[i] {
		case '*':
			sawGlob = true
			if i+1 < len(runes) && runes[i+1] == '*' {
				i++
				if i+1 < len(runes) && runes[i+1] == '/' {
					i++
					b.WriteString("(.*/)?")
				} else {
					b.WriteString(".*")
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			sawGlob = true
			b.WriteString("[^/]")
		case '[':
			sawGlob = true
			end := i + 1
			for end < len(runes) && runes[end] != ']' {
				end++
			}
			if end == len(runes) {
				b.WriteString(`\[`)
				continue
			}
			b.WriteByte('[')
			class := runes[i+1 : end]
			for j, ch := range class {
				switch {
				case j == 0 && ch == '!':
					b.WriteByte('^')
				case j == 0 && ch == '^':
					b.WriteString(`\^`)
				case ch == '\\':
					b.WriteString(`\\`)
				default:
					b.WriteRune(ch)
				}
			}
			b.WriteByte(']')
			i = end
		case ']':
			sawGlob = true
			b.WriteString(`\]`)
		default:
			if strings.ContainsRune(`\.+()|{}^$`, runes[i]) {
				b.WriteByte('\\')
			}
			b.WriteRune(runes[i])
		}
	}
	if !sawGlob {
		b.WriteString("(/.*)?")
	}
	b.WriteByte('$')
	return b.String()
}

func normalizeSeatbeltPaths(paths []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(paths))
	addAbs := func(abs string) {
		abs = filepath.Clean(abs)
		if _, ok := seen[abs]; ok {
			return
		}
		seen[abs] = struct{}{}
		out = append(out, abs)
	}
	add := func(p string) {
		abs, err := filepath.Abs(p)
		if err != nil {
			return
		}
		addAbs(abs)
		if resolved, err := filepath.EvalSymlinks(abs); err == nil {
			addAbs(resolved)
		}
		if strings.HasPrefix(abs, "/var/") {
			addAbs("/private" + abs)
		}
		if strings.HasPrefix(abs, "/private/var/") {
			addAbs(strings.TrimPrefix(abs, "/private"))
		}
	}
	for _, raw := range paths {
		p := strings.TrimSpace(raw)
		if p == "" {
			continue
		}
		if strings.HasPrefix(p, "~") {
			if home, err := os.UserHomeDir(); err == nil {
				p = filepath.Join(home, strings.TrimPrefix(p, "~/"))
			}
		}
		add(p)
	}
	return out
}

const seatbeltMinimalSupportPolicy = `; macOS services required by the minimal runtime profile.
(allow system-mac-syscall (mac-policy-name "vnguard"))
(allow system-mac-syscall
  (require-all
    (mac-policy-name "Sandbox")
    (mac-syscall-number 67)))
(allow system-fsctl (fsctl-command FSIOC_CAS_BSDFLAGS))
(allow iokit-open
  (iokit-registry-entry-class "RootDomainUserClient"))
(allow mach-lookup
  (global-name "com.apple.system.opendirectoryd.libinfo"))
(allow mach-lookup
  (global-name "com.apple.analyticsd")
  (global-name "com.apple.analyticsd.messagetracer")
  (global-name "com.apple.appsleep")
  (global-name "com.apple.bsd.dirhelper")
  (global-name "com.apple.cfprefsd.agent")
  (global-name "com.apple.cfprefsd.daemon")
  (global-name "com.apple.diagnosticd")
  (global-name "com.apple.dt.automationmode.reader")
  (global-name "com.apple.espd")
  (global-name "com.apple.logd")
  (global-name "com.apple.logd.events")
  (global-name "com.apple.runningboard")
  (global-name "com.apple.secinitd")
  (global-name "com.apple.system.DirectoryService.libinfo_v1")
  (global-name "com.apple.system.logger")
  (global-name "com.apple.system.notification_center")
  (global-name "com.apple.system.opendirectoryd.membership")
  (global-name "com.apple.trustd")
  (global-name "com.apple.trustd.agent")
  (global-name "com.apple.xpc.activity.unmanaged")
  (local-name "com.apple.cfprefsd.agent"))
(allow network-outbound (literal "/private/var/run/syslog"))
(allow ipc-posix-shm-read*
  (ipc-posix-name "apple.shm.notification_center"))
(allow mach-lookup (global-name "com.apple.audio.audiohald"))
(allow mach-lookup (global-name "com.apple.audio.AudioComponentRegistrar"))
(allow mach-lookup (global-name "com.apple.PowerManagement.control"))
`

var seatbeltPlatformReadSubpaths = []string{
	"/Library/Apple",
	"/Library/Filesystems/NetFSPlugins",
	"/Library/Preferences/Logging",
	"/private/var/db/DarwinDirectory/local/recordStore.data",
	"/private/var/db/timezone",
	"/usr/lib",
	"/usr/share",
	"/Library/Preferences",
	"/var/db",
	"/private/var/db",
	"/Library/Apple/System/Library/Frameworks",
	"/Library/Apple/System/Library/PrivateFrameworks",
	"/Library/Apple/usr/lib",
	"/System/Library/Frameworks",
	"/System/Library/PrivateFrameworks",
	"/System/Library/SubFrameworks",
	"/System/iOSSupport/System/Library/Frameworks",
	"/System/iOSSupport/System/Library/PrivateFrameworks",
	"/System/iOSSupport/System/Library/SubFrameworks",
	"/etc",
	"/private/etc",
	"/opt/homebrew/lib",
	"/usr/local/lib",
	"/Applications",
}

var seatbeltPlatformExecutableSubpaths = []string{
	"/Library/Apple/System/Library/Frameworks",
	"/Library/Apple/System/Library/PrivateFrameworks",
	"/Library/Apple/usr/lib",
	"/System/Library/Extensions",
	"/System/Library/Frameworks",
	"/System/Library/PrivateFrameworks",
	"/System/Library/SubFrameworks",
	"/System/iOSSupport/System/Library/Frameworks",
	"/System/iOSSupport/System/Library/PrivateFrameworks",
	"/System/iOSSupport/System/Library/SubFrameworks",
	"/usr/lib",
}

func writeSeatbeltMinimalPolicy(b *strings.Builder, deniedRead, deniedWrite []string) {
	if b == nil {
		return
	}
	b.WriteString("; macOS platform defaults included when a split filesystem policy requests :minimal.\n")
	for _, path := range seatbeltPlatformReadSubpaths {
		writeSeatbeltPlatformRule(b, "file-read* file-test-existence", seatbeltSubpathFilter(path), deniedRead)
	}
	for _, path := range seatbeltPlatformExecutableSubpaths {
		writeSeatbeltPlatformRule(b, "file-map-executable", seatbeltSubpathFilter(path), deniedRead)
	}

	for _, path := range []string{"/etc", "/tmp", "/var", "/private/etc/localtime"} {
		writeSeatbeltPlatformRule(b, "file-read-metadata file-test-existence", seatbeltLiteralFilter(path), deniedRead)
	}
	writeSeatbeltPlatformRule(b, "file-read-metadata file-test-existence", `(path-ancestors "/System/Volumes/Data/private")`, deniedRead)
	writeSeatbeltPlatformRule(b, "file-read* file-test-existence", seatbeltLiteralFilter("/"), deniedRead)

	for _, path := range []string{
		"/dev/autofs_nowait", "/dev/random", "/dev/urandom", "/private/etc/master.passwd",
		"/private/etc/passwd", "/private/etc/protocols", "/private/etc/services",
	} {
		writeSeatbeltPlatformRule(b, "file-read* file-test-existence", seatbeltLiteralFilter(path), deniedRead)
	}
	for _, path := range []string{"/dev/null", "/dev/zero"} {
		writeSeatbeltPlatformRule(b, "file-read* file-test-existence", seatbeltLiteralFilter(path), deniedRead)
		writeSeatbeltPlatformRule(b, "file-write-data", seatbeltLiteralFilter(path), deniedWrite)
	}
	writeSeatbeltPlatformRule(b, "file-read-data file-test-existence", seatbeltSubpathFilter("/dev/fd"), deniedRead)
	writeSeatbeltPlatformRule(b, "file-write-data", seatbeltSubpathFilter("/dev/fd"), deniedWrite)
	writeSeatbeltPlatformRule(b, "file-read* file-test-existence", seatbeltLiteralFilter("/dev/dtracehelper"), deniedRead)
	writeSeatbeltPlatformRule(b, "file-write-data file-ioctl", seatbeltLiteralFilter("/dev/dtracehelper"), deniedWrite)

	// The reference minimal profile supplies scratch space independently of
	// workspace-write. Keep that grant, but let explicit deny rules narrow it.
	for _, path := range []string{"/tmp", "/private/tmp", "/var/tmp", "/private/var/tmp"} {
		writeSeatbeltPlatformRule(b, "file-read* file-test-existence", seatbeltSubpathFilter(path), deniedRead)
		writeSeatbeltPlatformRule(b, "file-write*", seatbeltSubpathFilter(path), deniedWrite)
	}

	for _, path := range []string{
		"/System/Library/CoreServices",
		"/System/Library/CoreServices/.SystemVersionPlatform.plist",
		"/System/Library/CoreServices/SystemVersion.plist",
		"/private/var/db/eligibilityd/eligibility.plist",
	} {
		writeSeatbeltPlatformRule(b, "file-read* file-test-existence", seatbeltLiteralFilter(path), deniedRead)
	}
	for _, path := range []string{"/var", "/private/var"} {
		writeSeatbeltPlatformRule(b, "file-read-metadata", seatbeltSubpathFilter(path), deniedRead)
	}
	for _, path := range []string{"/bin", "/sbin", "/usr/bin", "/usr/sbin", "/usr/libexec"} {
		writeSeatbeltPlatformRule(b, "file-read-data file-read-metadata", seatbeltSubpathFilter(path), deniedRead)
	}

	writeSeatbeltPlatformRule(b, "file-read*", `(regex "^/dev/fd/(0|1|2)$")`, deniedRead)
	writeSeatbeltPlatformRule(b, "file-write*", `(regex "^/dev/fd/(1|2)$")`, deniedWrite)
	for _, path := range []string{"/dev/null", "/dev/tty"} {
		writeSeatbeltPlatformRule(b, "file-read*", seatbeltLiteralFilter(path), deniedRead)
		writeSeatbeltPlatformRule(b, "file-write*", seatbeltLiteralFilter(path), deniedWrite)
	}
	for _, filter := range []string{
		seatbeltLiteralFilter("/dev"), `(regex "^/dev/.*$")`, seatbeltLiteralFilter("/dev/stdin"),
		seatbeltLiteralFilter("/dev/stdout"), seatbeltLiteralFilter("/dev/stderr"),
		`(regex "^/dev/tty[^/]*$")`, `(regex "^/dev/pty[^/]*$")`,
	} {
		writeSeatbeltPlatformRule(b, "file-read-metadata", filter, deniedRead)
	}
	for _, filter := range []string{`(regex "^/dev/ttys[0-9]+$")`, seatbeltLiteralFilter("/dev/ptmx")} {
		writeSeatbeltPlatformRule(b, "file-read*", filter, deniedRead)
		writeSeatbeltPlatformRule(b, "file-write*", filter, deniedWrite)
	}
	writeSeatbeltPlatformRule(b, "file-ioctl", `(regex "^/dev/ttys[0-9]+$")`, deniedWrite)

	for _, path := range []string{"/System/Volumes", "/System/Volumes/Data", "/System/Volumes/Data/Users"} {
		filter := "(require-all " + seatbeltLiteralFilter(path) + " (vnode-type DIRECTORY))"
		writeSeatbeltPlatformRule(b, "file-read-metadata", filter, deniedRead)
	}
	writeSeatbeltPlatformRule(b, "file-read*", `(extension "com.apple.app-sandbox.read")`, deniedRead)
	writeSeatbeltPlatformRule(b, "file-read*", `(extension "com.apple.app-sandbox.read-write")`, deniedRead)
	writeSeatbeltPlatformRule(b, "file-write*", `(extension "com.apple.app-sandbox.read-write")`, deniedWrite)
	b.WriteString(seatbeltMinimalSupportPolicy)
}

func writeSeatbeltPlatformRule(b *strings.Builder, operations, filter string, denied []string) {
	b.WriteString("(allow ")
	b.WriteString(operations)
	if len(denied) == 0 {
		b.WriteByte(' ')
		b.WriteString(filter)
		b.WriteString(")\n")
		return
	}
	b.WriteString(" (require-all ")
	b.WriteString(filter)
	for _, path := range denied {
		b.WriteString(" (require-not (literal ")
		b.WriteString(strconv.Quote(path))
		b.WriteString(")) (require-not (subpath ")
		b.WriteString(strconv.Quote(path))
		b.WriteString("))")
	}
	b.WriteString("))\n")
}

func seatbeltSubpathFilter(path string) string {
	return "(subpath " + strconv.Quote(path) + ")"
}

func seatbeltLiteralFilter(path string) string {
	return "(literal " + strconv.Quote(path) + ")"
}

const seatbeltBasePolicy = `(version 1)

; inspired by Chrome's sandbox policy:
; https://source.chromium.org/chromium/chromium/src/+/main:sandbox/policy/mac/common.sb;l=273-319;drc=7b3962fe2e5fc9e2ee58000dc8fbf3429d84d3bd
; https://source.chromium.org/chromium/chromium/src/+/main:sandbox/policy/mac/renderer.sb;l=64;drc=7b3962fe2e5fc9e2ee58000dc8fbf3429d84d3bd

; start with closed-by-default
(deny default)

; child processes inherit the policy of their parent
(allow process-exec)
(allow process-fork)
(allow signal (target same-sandbox))

; process-info
(allow process-info* (target same-sandbox))

(allow file-write-data
  (require-all
    (path "/dev/null")
    (vnode-type CHARACTER-DEVICE)))

; sysctls permitted.
(allow sysctl-read
  (sysctl-name "hw.activecpu")
  (sysctl-name "hw.busfrequency_compat")
  (sysctl-name "hw.byteorder")
  (sysctl-name "hw.cacheconfig")
  (sysctl-name "hw.cachelinesize_compat")
  (sysctl-name "hw.cpufamily")
  (sysctl-name "hw.cpufrequency_compat")
  (sysctl-name "hw.cputype")
  (sysctl-name "hw.l1dcachesize_compat")
  (sysctl-name "hw.l1icachesize_compat")
  (sysctl-name "hw.l2cachesize_compat")
  (sysctl-name "hw.l3cachesize_compat")
  (sysctl-name "hw.logicalcpu_max")
  (sysctl-name "hw.machine")
  (sysctl-name "hw.model")
  (sysctl-name "hw.memsize")
  (sysctl-name "hw.ncpu")
  (sysctl-name "hw.nperflevels")
  ; Chrome locks these CPU feature detection down a bit more tightly,
  ; but mostly for fingerprinting concerns that are not relevant here.
  (sysctl-name-prefix "hw.optional.arm.")
  (sysctl-name-prefix "hw.optional.armv8_")
  (sysctl-name "hw.packages")
  (sysctl-name "hw.pagesize_compat")
  (sysctl-name "hw.pagesize")
  (sysctl-name "hw.physicalcpu")
  (sysctl-name "hw.physicalcpu_max")
  (sysctl-name "hw.logicalcpu")
  (sysctl-name "hw.cpufrequency")
  (sysctl-name "hw.tbfrequency_compat")
  (sysctl-name "hw.vectorunit")
  (sysctl-name "machdep.cpu.brand_string")
  (sysctl-name "kern.argmax")
  (sysctl-name "kern.hostname")
  (sysctl-name "kern.maxfilesperproc")
  (sysctl-name "kern.maxproc")
  (sysctl-name "kern.osproductversion")
  (sysctl-name "kern.osrelease")
  (sysctl-name "kern.ostype")
  (sysctl-name "kern.osvariant_status")
  (sysctl-name "kern.osversion")
  (sysctl-name "kern.secure_kernel")
  (sysctl-name "kern.usrstack64")
  (sysctl-name "kern.version")
  (sysctl-name "sysctl.proc_cputype")
  (sysctl-name "vm.loadavg")
  (sysctl-name-prefix "hw.perflevel")
  (sysctl-name-prefix "kern.proc.pgrp.")
  (sysctl-name-prefix "kern.proc.pid.")
  (sysctl-name-prefix "net.routetable.")
)

; Allow Java to read some CPU info. This is misclassified as a "write" because
; userspace passes a memory buffer to the sysctl, but conceptually it is a read.
(allow sysctl-write
  (sysctl-name "kern.grade_cputype"))

; IOKit
(allow iokit-open
  (iokit-registry-entry-class "RootDomainUserClient")
)

; needed to look up user info, see https://crbug.com/792228
(allow mach-lookup
  (global-name "com.apple.system.opendirectoryd.libinfo")
)

; Needed for python multiprocessing on MacOS for the SemLock
(allow ipc-posix-sem)

; Needed for PyTorch/libomp on macOS to register OpenMP runtimes.
(allow ipc-posix-shm-read-data
  ipc-posix-shm-write-create
  ipc-posix-shm-write-unlink
  (ipc-posix-name-regex #"^/__KMP_REGISTERED_LIB_[0-9]+$"))

(allow mach-lookup
  (global-name "com.apple.PowerManagement.control")
)

; allow openpty()
(allow pseudo-tty)
(allow file-read* file-write* file-ioctl (literal "/dev/ptmx"))
(allow file-read* file-write*
  (require-all
    (regex #"^/dev/ttys[0-9]+")
    (extension "com.apple.sandbox.pty")))
; PTYs created before entering seatbelt may lack the extension; allow ioctl
; on those slave ttys so interactive shells detect a TTY and remain functional.
(allow file-ioctl (regex #"^/dev/ttys[0-9]+"))

; allow readonly user preferences
(allow ipc-posix-shm-read* (ipc-posix-name-prefix "apple.cfprefs."))
(allow mach-lookup
  (global-name "com.apple.cfprefsd.daemon")
  (global-name "com.apple.cfprefsd.agent")
  (local-name "com.apple.cfprefsd.agent"))
(allow user-preference-read)
`

const seatbeltNetworkSupportPolicy = `; platform services required by network clients.
(allow system-socket
  (require-all
    (socket-domain AF_SYSTEM)
    (socket-protocol 2)))
(allow mach-lookup
  (global-name "com.apple.bsd.dirhelper")
  (global-name "com.apple.system.opendirectoryd.membership")
  (global-name "com.apple.SecurityServer")
  (global-name "com.apple.networkd")
  (global-name "com.apple.ocspd")
  (global-name "com.apple.trustd.agent")
  (global-name "com.apple.SystemConfiguration.DNSConfiguration")
  (global-name "com.apple.SystemConfiguration.configd"))
(allow sysctl-read (sysctl-name-regex #"^net.routetable"))
`
