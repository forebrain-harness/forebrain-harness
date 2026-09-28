// Sandboxed process supervision: output, waiting, violations, and reports.
package safety

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
)

const (
	commandPreviewHeadBytes = 32 * 1024
	commandPreviewTailBytes = 96 * 1024
	commandSpoolMaxBytes    = 64 * 1024 * 1024
)

// boundedOutput keeps a useful head/tail preview while accounting for every
// byte. Process output is adversarial input: retaining it in bytes.Buffer made
// one noisy command capable of exhausting the TUI process.
type boundedOutput struct {
	mu    sync.Mutex
	head  []byte
	tail  []byte
	total int64
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.total += int64(len(p))
	rest := p
	if len(b.head) < commandPreviewHeadBytes {
		take := minInt(len(rest), commandPreviewHeadBytes-len(b.head))
		b.head = append(b.head, rest[:take]...)
		rest = rest[take:]
	}
	if len(rest) > 0 {
		b.tail = append(b.tail, rest...)
		if len(b.tail) > commandPreviewTailBytes {
			copy(b.tail, b.tail[len(b.tail)-commandPreviewTailBytes:])
			b.tail = b.tail[:commandPreviewTailBytes]
		}
	}
	return len(p), nil
}

func (b *boundedOutput) snapshot() (text string, total, omitted int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	total = b.total
	kept := int64(len(b.head) + len(b.tail))
	if kept >= total {
		return string(append(append([]byte(nil), b.head...), b.tail...)), total, 0
	}
	omitted = total - kept
	marker := fmt.Sprintf("\n... [%d output bytes omitted] ...\n", omitted)
	preview := make([]byte, 0, len(b.head)+len(marker)+len(b.tail))
	preview = append(preview, b.head...)
	preview = append(preview, marker...)
	preview = append(preview, b.tail...)
	return string(preview), total, omitted
}

// boundedSpool preserves complete output up to a disk quota. It is written by
// the parent Forebrain Harness process, not the sandboxed child, and is always mode 0600.
type boundedSpool struct {
	mu      sync.Mutex
	file    *os.File
	path    string
	written int64
	total   int64
}

func newBoundedSpool(dir string) *boundedSpool {
	dir = filepath.Clean(dir)
	if dir == "." || dir == "" {
		dir = os.TempDir()
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return &boundedSpool{}
	}
	f, err := os.CreateTemp(dir, "forebrain-shell-*.output")
	if err != nil {
		return &boundedSpool{}
	}
	_ = f.Chmod(0o600)
	return &boundedSpool{file: f, path: f.Name()}
}

func (s *boundedSpool) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.total += int64(len(p))
	if s.file == nil || s.written >= commandSpoolMaxBytes {
		return len(p), nil
	}
	remaining := commandSpoolMaxBytes - s.written
	chunk := p
	if int64(len(chunk)) > remaining {
		chunk = chunk[:remaining]
	}
	n, err := s.file.Write(chunk)
	s.written += int64(n)
	// Capturing output must never make the command itself fail. A broken spool
	// degrades to the bounded in-memory preview.
	if err != nil {
		_ = s.file.Close()
		s.file = nil
		return len(p), nil
	}
	return len(p), nil
}

func (s *boundedSpool) finish(keep bool) (path string, written, omitted int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file != nil {
		_ = s.file.Close()
		s.file = nil
	}
	written = s.written
	if s.total > s.written {
		omitted = s.total - s.written
	}
	if keep && s.path != "" {
		return s.path, written, omitted
	}
	if s.path != "" {
		_ = os.Remove(s.path)
	}
	return "", written, omitted
}

type commandCapture struct {
	stdout    boundedOutput
	stderr    boundedOutput
	aggregate boundedOutput
	spool     *boundedSpool
	req       CommandRequest
}

func newCommandCapture(req CommandRequest) *commandCapture {
	return &commandCapture{req: req, spool: newBoundedSpool(req.OutputSpoolDir)}
}

type commandCaptureStreamWriter struct {
	capture *commandCapture
	stream  OutputStream
}

func (w commandCaptureStreamWriter) Write(p []byte) (int, error) {
	if len(p) == 0 || w.capture == nil {
		return len(p), nil
	}
	var target *boundedOutput
	if w.stream == OutputStreamStderr {
		target = &w.capture.stderr
	} else {
		target = &w.capture.stdout
	}
	_, _ = target.Write(p)
	_, _ = w.capture.aggregate.Write(p)
	if w.capture.spool != nil {
		_, _ = w.capture.spool.Write(p)
	}
	if w.capture.req.OnOutput != nil {
		chunk := append([]byte(nil), p...)
		w.capture.req.OnOutput(w.stream, chunk)
	}
	return len(p), nil
}

func (c *commandCapture) writer(stream OutputStream) io.Writer {
	return commandCaptureStreamWriter{capture: c, stream: stream}
}

func (c *commandCapture) result() CommandResult {
	stdout, stdoutBytes, stdoutOmitted := c.stdout.snapshot()
	stderr, stderrBytes, stderrOmitted := c.stderr.snapshot()
	aggregated, _, aggregateOmitted := c.aggregate.snapshot()
	keepSpool := stdoutOmitted > 0 || stderrOmitted > 0 || aggregateOmitted > 0
	spoolPath, spoolBytes, spoolOmitted := c.spool.finish(keepSpool)
	return CommandResult{
		Stdout: stdout, Stderr: stderr, AggregatedOutput: aggregated,
		StdoutBytes: stdoutBytes, StderrBytes: stderrBytes,
		StdoutOmittedBytes: stdoutOmitted, StderrOmittedBytes: stderrOmitted,
		OutputSpoolPath: spoolPath, OutputSpoolBytes: spoolBytes, OutputSpoolOmittedBytes: spoolOmitted,
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// waitForCommandContext makes process completion and cancellation mutually
// exclusive. In particular, it prevents a cancellation watcher from killing a
// recycled process-group ID after Wait has already returned.
func waitForCommandContext(ctx context.Context, cmd *exec.Cmd) (processErr, stopErr error) {
	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()
	select {
	case processErr = <-waitCh:
		return processErr, nil
	case <-ctx.Done():
		stopErr = ctx.Err()
		killProcessGroup(cmd)
		return <-waitCh, stopErr
	}
}

var sandboxDeniedKeywords = []string{
	"operation not permitted",
	"permission denied",
	"read-only file system",
	"seccomp",
	"sandbox",
	"landlock",
	"failed to write file",
}

// benignNoiseSubstrings lists substrings that identify lines emitted by macOS
// host tooling (xcrun, confstr) that happen to contain denial-like phrases but
// are unrelated to the sandbox policy.  These lines must be stripped before the
// keyword scan to avoid false positives such as misclassifying a normal git
// failure as a sandbox denial just because macOS xcrun emitted
// "errno=Operation not permitted" while trying to create its own cache file.
//
// Only whole-line matches are suppressed; real denial messages never arrive on
// the same line as these markers.
var benignNoiseSubstrings = []string{
	"xcrun_db",
	"darwin_user_temp_dir",
}

// stripBenignNoise returns a copy of output with known-benign macOS host-tool
// noise lines removed.  The original output is never mutated; nil is returned
// when nothing remains after stripping so callers can short-circuit cheaply.
func stripBenignNoise(output string) string {
	if output == "" {
		return ""
	}
	lower := strings.ToLower(output)
	// Fast path: none of the noise substrings appear at all.
	hasNoise := false
	for _, marker := range benignNoiseSubstrings {
		if strings.Contains(lower, marker) {
			hasNoise = true
			break
		}
	}
	if !hasNoise {
		return output
	}
	lines := strings.Split(output, "\n")
	filtered := lines[:0]
	for _, line := range lines {
		ll := strings.ToLower(line)
		noisy := false
		for _, marker := range benignNoiseSubstrings {
			if strings.Contains(ll, marker) {
				noisy = true
				break
			}
		}
		if !noisy {
			filtered = append(filtered, line)
		}
	}
	return strings.Join(filtered, "\n")
}

// IsLikelySandboxDenied classifies failed sandboxed executions. Ordinary
// command failures are not treated as sandbox denials unless the output
// contains a denial marker. A marker-free Linux SIGSYS exit is the exception.
func IsLikelySandboxDenied(decision SandboxDecision, result CommandResult) bool {
	if !decision.UseSandbox {
		return false
	}
	if result.NetworkDenial != nil {
		return true
	}
	if result.ExitCode == 0 {
		return false
	}
	if SandboxDenialDetail(result) != "" {
		return true
	}

	// These exit codes are normally shell or exec errors rather than evidence
	// that the sandbox denied the command.
	switch result.ExitCode {
	case 2, 126, 127:
		return false
	}

	// Linux seccomp reports SIGSYS as 128 + SIGSYS in shell exit status.
	return decision.SandboxType == SandboxTypeLinuxSeccomp && result.ExitCode == sandboxSIGSYSExitCode()
}

// SandboxDenialDetail returns the first concrete output line that explains why
// the sandbox classified an execution as denied. It is suitable for an
// approval prompt: unlike the full stderr stream, it keeps the reason concise
// while preserving the OS error the user needs to make a decision.
func SandboxDenialDetail(result CommandResult) string {
	for _, section := range []string{result.Stderr, result.Stdout, result.AggregatedOutput} {
		// Strip known-benign macOS host-tool noise before scanning for denial
		// keywords. Real denial messages never share those lines.
		for _, line := range strings.Split(stripBenignNoise(section), "\n") {
			lower := strings.ToLower(line)
			for _, keyword := range sandboxDeniedKeywords {
				if strings.Contains(lower, keyword) {
					return strings.TrimSpace(line)
				}
			}
		}
	}
	return ""
}

// SandboxModeLabel is the sandbox mode the configuration selects: a named
// permission profile outranks sandbox_mode, exactly as /sandbox reports it.
func SandboxModeLabel(cfg *appcfg.Root) string {
	if cfg == nil {
		return ""
	}
	if selected := strings.TrimSpace(cfg.DefaultPermissions); selected != "" {
		return selected
	}
	return strings.TrimSpace(string(cfg.SandboxMode))
}

// SandboxStatusLine is the one-line sandbox summary /status shows, read from
// the same status /sandbox reports in full: the selected mode, then the
// backend that is enforcing it — or, when nothing is, the reason.
func SandboxStatusLine(cfg *appcfg.Root) string {
	st := NewManager().Status(cfg)
	parts := make([]string, 0, 2)
	if mode := SandboxModeLabel(cfg); mode != "" {
		parts = append(parts, mode)
	}
	switch {
	case st.EffectiveEnabled:
		parts = append(parts, string(st.Backend))
	case strings.TrimSpace(st.UnavailableReason) != "":
		parts = append(parts, "not enforced: "+SandboxReasonInWords(st.UnavailableReason))
	default:
		parts = append(parts, "not enforced")
	}
	return strings.Join(parts, " · ")
}

// FormatSandboxReport says in words what the sandbox does to the commands
// Forebrain Harness runs: which files they may change, whether they reach the network,
// what enforces it — and, when nothing can, that they are refused — plus
// anything the platform is missing, in the tools' own words.
func FormatSandboxReport(cfg *appcfg.Root, manager *Manager) string {
	if cfg == nil {
		return "The sandbox settings are unavailable here."
	}
	if manager == nil {
		manager = NewManager()
	}
	if cfg.DangerFullAccessEnabled() {
		return "No sandbox: commands run with your own access, so they can change any file and reach the network."
	}
	status := manager.Status(cfg)
	doctor := manager.Doctor(cfg)
	lines := []string{
		"Files: " + sandboxFilesInWords(cfg),
		"Network: " + SandboxNetworkInWords(cfg),
	}
	reason := strings.TrimSpace(status.UnavailableReason)
	if reason != "" {
		lines = append(lines, "Enforcement: nothing can enforce it here ("+SandboxReasonInWords(reason)+"), so commands that need it are refused.")
	} else {
		lines = append(lines, "Enforcement: "+backendInWords(status.Backend)+" enforces it.")
	}
	if status.SettingsLocked {
		lines = append(lines, "These settings are locked by policy and cannot be changed from here.")
	}
	for _, missing := range doctor.DependencyErrors {
		if missing = strings.TrimSpace(missing); missing != "" && missing != reason {
			lines = append(lines, "Missing: "+SandboxReasonInWords(missing)+".")
		}
	}
	for _, note := range doctor.DependencyWarning {
		if note = strings.TrimSpace(note); note != "" {
			lines = append(lines, "Note: "+SandboxReasonInWords(note)+".")
		}
	}
	return strings.Join(lines, "\n")
}

// SandboxReasonInWords says what a sandbox diagnostic code means; a code it
// does not know is shown as it is.
func SandboxReasonInWords(code string) string {
	switch strings.TrimSpace(code) {
	case "seatbelt_not_found":
		return "sandbox-exec is not on PATH"
	case "bubblewrap_not_found":
		return "bubblewrap (bwrap) is not installed"
	case "wsl1_unsupported":
		return "WSL 1 cannot run the sandbox; WSL 2 can"
	case "platform_unsupported":
		return "this platform has no sandbox Forebrain Harness can use"
	case "missing_config":
		return "there is no sandbox configuration"
	case "linux_glob_limits_apply":
		return "on Linux, path patterns in the rules match only what exists when a command starts"
	case "windows_sandbox_disabled":
		return "the Windows sandbox is turned off (windows.sandbox)"
	case "windows_sandbox_missing_config":
		return "the Windows sandbox is not configured"
	case "windows_sandbox_wrong_platform":
		return "the Windows sandbox runs only on Windows"
	case "windows_sandbox_api_unavailable":
		return "the Windows sandbox API is not available on this system"
	case "windows_firewall_powershell_not_found":
		return "PowerShell, which sets the sandbox's firewall rules, was not found"
	case "windows_managed_network_requires_elevated":
		return "network rules need the elevated Windows sandbox"
	case "windows_unelevated_restricted_read_requires_elevated":
		return "restricting reads needs the elevated Windows sandbox"
	}
	return code
}

// sandboxFilesInWords is what the sandbox lets commands do with files.
func sandboxFilesInWords(cfg *appcfg.Root) string {
	if profile := strings.TrimSpace(cfg.DefaultPermissions); profile != "" && profileForSandboxMode(cfg) == ProfileManaged {
		return "the " + profile + " permission profile decides which files commands can read and change."
	}
	if profileForSandboxMode(cfg) != ProfileWorkspaceWrite {
		return "commands can read files but not change them."
	}
	places := []string{"the workspace"}
	if !cfg.SandboxWorkspaceWrite.ExcludeTmpdirEnvVar {
		places = append(places, "$TMPDIR")
	}
	if !cfg.SandboxWorkspaceWrite.ExcludeSlashTmp {
		places = append(places, "/tmp")
	}
	places = append(places, cfg.SandboxWorkspaceWrite.WritableRoots...)
	return "commands can read files and change them only in " + strings.Join(places, ", ") + "."
}

// SandboxNetworkInWords is what commands in the sandbox can reach on the
// network, which the sandbox's network settings decide.
func SandboxNetworkInWords(cfg *appcfg.Root) string {
	if cfg == nil {
		cfg = &appcfg.Root{}
	}
	switch {
	case !cfg.SandboxWorkspaceWrite.EffectiveNetworkAccess():
		return "commands cannot reach the internet; approval is required to."
	case cfg.Features.NetworkProxy.EnabledValue():
		return "commands reach the internet through Forebrain Harness's proxy, which asks before contacting a site it has not been allowed."
	default:
		return "commands can reach the internet."
	}
}

func backendInWords(backend BackendName) string {
	switch backend {
	case BackendSeatbelt:
		return "macOS Seatbelt"
	case BackendBubblewrap:
		return "bubblewrap"
	case BackendWindows:
		return "the Windows restricted-token sandbox"
	}
	return string(backend)
}

func detectPlatform() Platform {
	switch runtime.GOOS {
	case "darwin":
		return PlatformDarwin
	case "linux":
		if isWSL() {
			return PlatformWSL
		}
		return PlatformLinux
	case "windows":
		return PlatformWindows
	default:
		return PlatformUnsupported
	}
}

func isWSL() bool {
	if strings.TrimSpace(os.Getenv("WSL_DISTRO_NAME")) != "" {
		return true
	}
	if strings.TrimSpace(os.Getenv("WSL_INTEROP")) != "" {
		return true
	}
	b, err := os.ReadFile("/proc/version")
	if err != nil {
		return false
	}
	t := strings.ToLower(string(b))
	return strings.Contains(t, "microsoft") || strings.Contains(t, "wsl")
}

func isWSL1() bool {
	if !isWSL() {
		return false
	}
	b, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return false
	}
	t := strings.ToLower(string(b))
	return strings.Contains(t, "microsoft") && !strings.Contains(t, "wsl2")
}

func backendUnavailableReason(platform Platform) string {
	switch platform {
	case PlatformDarwin:
		if _, err := exec.LookPath("sandbox-exec"); err != nil {
			return "seatbelt_not_found"
		}
		return ""
	case PlatformLinux:
		if _, err := exec.LookPath("bwrap"); err != nil {
			if _, err2 := exec.LookPath("bubblewrap"); err2 != nil {
				return "bubblewrap_not_found"
			}
		}
		return ""
	case PlatformWSL:
		if isWSL1() {
			return "wsl1_unsupported"
		}
		if _, err := exec.LookPath("bwrap"); err != nil {
			if _, err2 := exec.LookPath("bubblewrap"); err2 != nil {
				return "bubblewrap_not_found"
			}
		}
		return ""
	case PlatformWindows:
		return windowsSandboxUnavailableReason()
	default:
		return "platform_unsupported"
	}
}

func backendForPlatform(platform Platform) BackendName {
	switch platform {
	case PlatformDarwin:
		return BackendSeatbelt
	case PlatformLinux, PlatformWSL:
		return BackendBubblewrap
	case PlatformWindows:
		return BackendWindows
	default:
		return BackendHost
	}
}
