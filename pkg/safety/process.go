// Sandboxed process supervision: output, waiting, violations, and reports.
package safety

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

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
// recycled process-group ID after Wait has already returned: reaped is closed by
// the waiting goroutine as soon as Wait returns, and that is what withholds the
// second group signal killCommandGroup sends.
func waitForCommandContext(ctx context.Context, cmd *exec.Cmd) (processErr, stopErr error) {
	reaped := make(chan struct{})
	waitCh := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		close(reaped)
		waitCh <- err
	}()
	select {
	case processErr = <-waitCh:
		return processErr, nil
	case <-ctx.Done():
		stopErr = ctx.Err()
		killCommandGroup(cmd, reaped)
		return <-waitCh, stopErr
	}
}

// startedCommand is a command a backend has spawned and nobody has waited for
// yet. It exists because spawn and wait were one function body in every
// backend: everything the defers owned there — the seatbelt profile on disk,
// the Linux deny markers, the Windows token and pipe handles, the context that
// bounded the command — has to stay alive for as long as the command does once
// the wait is a separate call.
type startedCommand struct {
	ctx     context.Context
	cancel  context.CancelFunc
	cmd     *exec.Cmd
	capture *commandCapture
	pid     int
	// waitFn lets a backend that does not spawn a *exec.Cmd own its wait: the
	// Windows sandbox creates its process through CreateProcessAsUser and
	// returns the exit code the OS gave it. A nil waitFn is the ordinary path —
	// wait on cmd, finalize the capture.
	waitFn func(ctx context.Context) (CommandResult, error)
	// killFn is how a backend without an *exec.Cmd ends its process tree.
	killFn  func()
	closers []func()
	once    sync.Once
}

// deferClose records cleanup to run when the command is done with. It stands
// in for a defer in a spawn body: close runs the recorded functions
// last-in-first-out, the order the defers they replace would have fired in.
func (s *startedCommand) deferClose(f func()) {
	if s == nil || f == nil {
		return
	}
	s.closers = append(s.closers, f)
}

// close releases what the spawn half opened for this command, once. The
// process must already have been waited for: the seatbelt profile and the deny
// markers are what the running sandbox is still reading.
func (s *startedCommand) close() {
	if s == nil {
		return
	}
	s.once.Do(func() {
		for i := len(s.closers) - 1; i >= 0; i-- {
			s.closers[i]()
		}
		if s.cancel != nil {
			s.cancel()
		}
	})
}

// wait waits for the command's exit and finalizes its result, once. ctx ending
// first kills the process group and still reports a cancelled or timed-out
// command, which is what the waiting path has always done.
func (s *startedCommand) wait(ctx context.Context) (CommandResult, error) {
	if s == nil {
		return CommandResult{ExitCode: 1}, nil
	}
	if s.waitFn != nil {
		res, err := s.waitFn(ctx)
		s.close()
		return res, err
	}
	var waitErr, stopErr error
	if s.cmd != nil && s.cmd.Process != nil {
		waitErr, stopErr = waitForCommandContext(ctx, s.cmd)
	}
	res := CommandResult{ExitCode: 1}
	if s.capture != nil {
		res = s.capture.result()
	}
	s.close()
	return finalizeExec(res, waitErr, stopErr)
}

// kill ends the command's process tree. reaped is closed once the command has
// been waited for, which is what stops the second signal from reaching a
// process-group ID the kernel has already recycled (see killCommandGroup).
func (s *startedCommand) kill(reaped <-chan struct{}) {
	if s == nil {
		return
	}
	if s.killFn != nil {
		s.killFn()
		return
	}
	if s.cmd == nil {
		return
	}
	killCommandGroup(s.cmd, reaped)
}

// ProcessHandle is a command that was started and not waited for. Holding it is
// what lets a long-lived process outlive the call that started it: the handle
// owns the process, the one wait that reaps it, and the kill.
type ProcessHandle struct {
	started *startedCommand
	pid     int
	done    chan struct{}

	mu      sync.Mutex
	exited  bool
	result  CommandResult
	waitErr error
}

func newProcessHandle(sc *startedCommand) *ProcessHandle {
	h := &ProcessHandle{started: sc, done: make(chan struct{})}
	if sc != nil {
		h.pid = sc.pid
	}
	go h.reap()
	return h
}

// reap is the one goroutine that waits for the command, and it belongs to the
// command rather than to the call that started it: the pipe readers feeding the
// capture belong to the command too, so output keeps being recorded after the
// start call has returned.
func (h *ProcessHandle) reap() {
	res, err := h.started.wait(h.started.ctx)
	h.mu.Lock()
	h.waitErr = err
	h.result = res
	h.exited = true
	h.mu.Unlock()
	close(h.done)
}

// Wait blocks until the command exits, or until ctx ends — in which case the
// process group is killed and its exit is still waited for, exactly as the
// waiting path always did.
func (h *ProcessHandle) Wait(ctx context.Context) (CommandResult, error) {
	if h == nil {
		return CommandResult{ExitCode: 1}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-h.done:
	case <-ctx.Done():
		_ = h.Kill()
		<-h.done
	}
	h.mu.Lock()
	res, waitErr := h.result, h.waitErr
	h.mu.Unlock()
	return res, waitErr
}

// Kill terminates the command's whole process group. Killing a process that has
// already exited is a success: the caller asked for a state, not an event.
func (h *ProcessHandle) Kill() error {
	if h == nil || h.started == nil {
		return nil
	}
	select {
	case <-h.done:
		return nil
	default:
	}
	h.started.kill(h.done)
	return nil
}

// PID is the process the sandbox spawned, which is the leader of the group its
// children inherit.
func (h *ProcessHandle) PID() int {
	if h == nil {
		return 0
	}
	return h.pid
}

// Done is closed when the command has exited and been reaped: a watcher that
// only cares that it ended waits on it instead of polling Exited.
func (h *ProcessHandle) Done() <-chan struct{} {
	if h == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return h.done
}

func (h *ProcessHandle) Exited() bool {
	if h == nil {
		return true
	}
	select {
	case <-h.done:
		return true
	default:
		return false
	}
}

// Result is the reaped result, and whether the command has been reaped at all.
// A caller that must not kill the process it is watching — a readiness poll —
// reads this instead of calling Wait.
func (h *ProcessHandle) Result() (CommandResult, bool) {
	if h == nil {
		return CommandResult{}, true
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.result, h.exited
}

// finalizeExec turns a finished wait into the result the caller sees: a
// cancelled or timed-out command keeps that shape rather than the signal its
// own kill produced, an abandoned pipe is the success the command had, and an
// exit code is what a plain failure reports.
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
	// ErrWaitDelay is not a failure: it says the command ended and a descendant
	// it left behind still held the output pipes past WaitDelay, so os/exec
	// abandoned them. It is only raised when the command itself exited with
	// status 0, so the command's own outcome is the clean one it had.
	if errors.Is(err, exec.ErrWaitDelay) {
		return res, nil
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

// ProcessStartToken identifies when a process started, in a form a later
// reading can be compared with. It is how the PID ledger tells a live process
// from one whose PID the OS has recycled, and a recycled PID must never be
// killed.
//
// Linux reads field 22 of /proc/<pid>/stat (clock ticks since boot) and macOS
// asks ps for lstart. Windows has no cheap equivalent here, so it reports none
// and the reap is skipped there (plans/358).
func ProcessStartToken(pid int) (string, bool) {
	if pid <= 0 {
		return "", false
	}
	switch runtime.GOOS {
	case "linux":
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			return "", false
		}
		// The command name is parenthesised and may itself hold spaces and
		// parentheses, so the fields are counted from the last ')'.
		end := strings.LastIndex(string(data), ")")
		if end < 0 {
			return "", false
		}
		fields := strings.Fields(string(data[end+1:]))
		// fields[0] is field 3 (state), so the start time (field 22) is fields[19].
		if len(fields) < 20 {
			return "", false
		}
		return fields[19], true
	case "darwin":
		out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "lstart=").Output()
		if err != nil {
			return "", false
		}
		token := strings.TrimSpace(string(out))
		if token == "" {
			return "", false
		}
		return token, true
	}
	return "", false
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

// networkDeniedKeywords are the ways a program reports that it could not reach
// a host: name resolution, routing, and connection setup failures. They are a
// second table, not more entries in sandboxDeniedKeywords, because they say
// something weaker — a host with no network produces them too. They only
// classify a command as denied together with the sandbox's own fact that it
// blocked the network (SandboxDecision.NetworkBlocked).
//
// The wording matters: a kernel denial here is EPERM ("Operation not
// permitted"), but curl, git and the Go toolchain each rewrite it into their
// own sentence, so the errno keyword never appears. These are the sentences
// those tools actually print (measured against a real seatbelt sandbox).
var networkDeniedKeywords = []string{
	"could not resolve host",
	"no such host",
	"nodename nor servname provided",
	"name or service not known",
	"temporary failure in name resolution",
	"failed to connect",
	"couldn't connect to server",
	"network is unreachable",
	"no route to host",
}

// networkDeniedVetoes are the connect failures that mean the opposite: the
// stack reached the peer and was refused, or nothing answered in time. Both
// happen on a host whose network is perfectly fine, so a line carrying one says
// nothing about the sandbox — even though curl prefixes it with "Failed to
// connect", a phrase the markers above do look for.
var networkDeniedVetoes = []string{
	"connection refused",
	"timed out",
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
	// A command that ran in a sandbox with no network path and reported a
	// network failure was denied by the sandbox, whatever its exit status: a
	// shell chain the model wrote itself (`curl … || echo failed`) hands back 0
	// after the failure it just absorbed, and that is exactly the shape this
	// check exists for. Without the decision's own fact this would be a guess,
	// so a host that is simply offline never takes this branch.
	if decision.NetworkBlocked && networkDenialDetail(result) != "" {
		return true
	}
	if result.ExitCode == 0 {
		return false
	}
	if SandboxDenialDetail(decision, result) != "" {
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
//
// The decision is part of the answer because a network failure is only a
// sandbox denial when the sandbox is what removed the network.
func SandboxDenialDetail(decision SandboxDecision, result CommandResult) string {
	if detail := denialDetailIn(result, sandboxDeniedKeywords, nil); detail != "" {
		return detail
	}
	if decision.NetworkBlocked {
		return networkDenialDetail(result)
	}
	return ""
}

// networkDenialDetail is the output line that says the command could not reach
// a host, or "" when the output does not say that.
func networkDenialDetail(result CommandResult) string {
	return denialDetailIn(result, networkDeniedKeywords, networkDeniedVetoes)
}

// denialDetailIn scans every output section for the first line carrying one of
// the markers, in the order the sections are most likely to explain a failure.
func denialDetailIn(result CommandResult, markers, vetoes []string) string {
	for _, section := range []string{result.Stderr, result.Stdout, result.AggregatedOutput} {
		// Strip known-benign macOS host-tool noise before scanning for denial
		// keywords. Real denial messages never share those lines.
		for _, line := range strings.Split(stripBenignNoise(section), "\n") {
			lower := strings.ToLower(line)
			if containsAny(lower, vetoes) {
				continue
			}
			if containsAny(lower, markers) {
				return strings.TrimSpace(line)
			}
		}
	}
	return ""
}

func containsAny(line string, needles []string) bool {
	for _, needle := range needles {
		if strings.Contains(line, needle) {
			return true
		}
	}
	return false
}

// SandboxModeLabel is the sandbox mode the preset derives, as /sandbox
// reports it.
func SandboxModeLabel(cfg *appcfg.Root) string {
	if cfg == nil {
		return ""
	}
	return strings.TrimSpace(string(cfg.SandboxMode))
}

// SandboxStatusLine is the one-line sandbox summary /status shows, read from
// the same status /sandbox reports in full: the selected mode, then the
// backend that is enforcing it — or, when one should be and cannot, the
// reason. A mode that already means "no sandbox" (danger-full-access) stands
// alone; "not enforced" beside it only repeated the mode.
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
	}
	return strings.Join(parts, " ")
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
//
// A probe's failure arrives as the code plus a discriminator
// ("bubblewrap_unusable: exit 1"), so the known codes are matched by prefix and
// the discriminator is worded too — otherwise /status and /sandbox would print
// the raw code, which is what made "unusable" indistinguishable from a probe
// that merely ran out of time.
func SandboxReasonInWords(code string) string {
	code = strings.TrimSpace(code)
	base, detail, hasDetail := strings.Cut(code, ": ")
	var words string
	switch base {
	case "seatbelt_not_found":
		words = "sandbox-exec is not on PATH"
	case "bubblewrap_not_found":
		words = "bubblewrap (bwrap) is not installed"
	case "bubblewrap_unusable":
		words = "bubblewrap is installed but cannot start a sandbox here (the kernel or a security profile refuses its namespaces)"
	case "seatbelt_unusable":
		words = "sandbox-exec is installed but cannot start a sandbox here"
	case "wsl1_unsupported":
		words = "WSL 1 cannot run the sandbox; WSL 2 can"
	case "platform_unsupported":
		words = "this platform has no sandbox Forebrain Harness can use"
	case "missing_config":
		words = "there is no sandbox configuration"
	case "sandbox configuration unavailable":
		words = "no sandbox settings are available here, so nothing can be enforced"
	case "linux_glob_limits_apply":
		words = "on Linux, path patterns in the rules match only what exists when a command starts"
	case "windows_sandbox_wrong_platform":
		words = "the Windows sandbox runs only on Windows"
	case "windows_sandbox_api_unavailable":
		words = "the Windows sandbox API is not available on this system"
	case "windows_batch_reads_not_isolated":
		words = "on Windows a batch task's writes stay inside its own directory, but its reads are not isolated: a restricted token cannot hold an exclusive read set"
	default:
		return code
	}
	if !hasDetail {
		return words
	}
	switch {
	case detail == "timeout":
		return words + " — it timed out after " + sandboxProbeTimeout.String()
	case strings.HasPrefix(detail, "exit "):
		return words + " — it exited with code " + strings.TrimPrefix(detail, "exit ")
	}
	return code
}

// sandboxFilesInWords is what the sandbox lets commands do with files.
func sandboxFilesInWords(cfg *appcfg.Root) string {
	if profileForSandboxMode(cfg) != ProfileWorkspaceWrite {
		return "commands can read files but not change them."
	}
	places := []string{"the workspace", "$TMPDIR"}
	if filepath.Separator == '/' {
		places = append(places, "/tmp")
	}
	places = append(places, cfg.SandboxWorkspaceWrite.WritableRoots...)
	return "commands can read files and change them only in " + strings.Join(places, ", ") + "."
}

// SandboxNetworkInWords is what commands in the sandbox can reach on the
// network, which the sandbox's network settings decide.
//
// Windows is always the open sentence: its restricted token cannot take the
// network away, so the blocked sentence would describe isolation that is not
// there (owner 2026-10-31, plan 206).
func SandboxNetworkInWords(cfg *appcfg.Root) string {
	if cfg == nil {
		cfg = &appcfg.Root{}
	}
	if detectPlatform() != PlatformWindows && !cfg.SandboxWorkspaceWrite.EffectiveNetworkAccess() {
		return "commands cannot reach the internet; approval is required to."
	}
	return "commands can reach the internet."
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

// backendUnavailableReason names why commands cannot be contained here: the
// backend is missing, or it is installed and cannot actually run. The second
// half is what a bare presence check cannot see — a container commonly ships
// bubblewrap and then blocks the namespaces it needs, so "installed" alone
// turned every command into a failure (owner 2026-10-31, plan 215 §3.1).
func backendUnavailableReason(platform Platform) string {
	switch platform {
	case PlatformDarwin:
		if _, err := exec.LookPath("sandbox-exec"); err != nil {
			return "seatbelt_not_found"
		}
	case PlatformLinux:
		if _, err := lookBubblewrap(); err != nil {
			return "bubblewrap_not_found"
		}
	case PlatformWSL:
		if isWSL1() {
			return "wsl1_unsupported"
		}
		if _, err := lookBubblewrap(); err != nil {
			return "bubblewrap_not_found"
		}
	case PlatformWindows:
		return windowsSandboxUnavailableReason()
	default:
		return "platform_unsupported"
	}
	return probeSandboxReason(platform)
}

// lookBubblewrap finds the bubblewrap binary under either of the two names
// distributions ship it as.
func lookBubblewrap() (string, error) {
	if p, err := exec.LookPath("bwrap"); err == nil {
		return p, nil
	}
	return exec.LookPath("bubblewrap")
}

// sandboxProbeTimeout bounds the one real command a probe runs. A backend that
// hangs is as unusable as one that fails, and this answer gates every command
// the process runs.
const sandboxProbeTimeout = 5 * time.Second

// sandboxProbeRetryDelay is how long a failed probe waits before its one retry.
const sandboxProbeRetryDelay = 100 * time.Millisecond

// sandboxProbe caches the probe for the life of the process: the answer is a
// property of this machine, and running it per command would cost a process
// launch each time. forced is what a test pinned it to — nil in the product.
var sandboxProbe struct {
	mu     sync.Mutex
	done   bool
	reason string
	forced *string
}

// probeSandboxReason is the cached probe answer, or "" when the sandbox really
// runs here.
//
// A success is cached at once; a failure is retried once before it is cached.
// The answer is a capability of the machine, but one failure is not always the
// machine: sandboxProbeTimeout under load, a host busy for a moment, or a probe
// that starts before the session's temp directories exist all fail once and
// then pass. Since plan 215 an unusable sandbox is not a degraded answer but a
// permanent one — every command then runs uncontained with full access and the
// approval picker never appears — so a transient failure must not be cached as
// this machine's verdict for the life of the process.
func probeSandboxReason(platform Platform) string {
	sandboxProbe.mu.Lock()
	defer sandboxProbe.mu.Unlock()
	if sandboxProbe.forced != nil {
		return *sandboxProbe.forced
	}
	if !sandboxProbe.done {
		reason := probeSandbox(platform)
		if reason != "" {
			time.Sleep(sandboxProbeRetryDelay)
			reason = probeSandbox(platform)
		}
		sandboxProbe.reason = reason
		sandboxProbe.done = true
	}
	return sandboxProbe.reason
}

// probeSandbox runs the backend once on a command that does nothing and
// reports "" for a sandbox that works. The arguments are the ones a real
// command gets, narrowed to the part a broken container or seccomp profile
// refuses: a namespace to run in, a read-only root to start from, and the two
// filesystems that make it usable.
func probeSandbox(platform Platform) string {
	switch platform {
	case PlatformDarwin:
		return runSandboxProbe("sandbox-exec", "-p", "(version 1)(allow default)", "/usr/bin/true")
	case PlatformLinux, PlatformWSL:
		bwrap, err := lookBubblewrap()
		if err != nil {
			return "bubblewrap_not_found"
		}
		return runSandboxProbe(bwrap,
			"--unshare-user", "--unshare-pid", "--die-with-parent",
			"--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc", "--", "true")
	}
	// Windows keeps its API check (backendUnavailableReason reports it before
	// the probe), and a platform with no backend is already unsupported.
	return ""
}

// runSandboxProbe runs one command under one backend, describing a failure as
// the backend being unusable. No output is kept: the exit status is the whole
// answer, and a probe must not leave a process behind — the deadline kills
// one that hangs, and bubblewrap's own --die-with-parent covers its children.
//
// The failure carries a short discriminator after the code ("timeout", "exit
// N"): "unusable" alone cannot tell a missing kernel capability from a probe
// that ran out of time, and the two want different fixes. The command's own
// output is never quoted — it can contain paths, and this string is shown.
func runSandboxProbe(name string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), sandboxProbeTimeout)
	defer cancel()
	if err := exec.CommandContext(ctx, name, args...).Run(); err != nil {
		reason := "bubblewrap_unusable"
		if strings.Contains(name, "sandbox-exec") {
			reason = "seatbelt_unusable"
		}
		if detail := sandboxProbeFailureDetail(ctx, err); detail != "" {
			return reason + ": " + detail
		}
		return reason
	}
	return ""
}

// sandboxProbeFailureDetail names a failed probe's cause in one word and a
// number: the deadline it hit, or the status the command exited with. A command
// that was killed by a signal, or never started at all, has no exit status to
// report — the bare reason code still says the backend is unusable.
func sandboxProbeFailureDetail(ctx context.Context, err error) string {
	if ctx.Err() == context.DeadlineExceeded {
		return "timeout"
	}
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() >= 0 {
		return fmt.Sprintf("exit %d", exitErr.ExitCode())
	}
	return ""
}

// SandboxAvailable reports whether this machine can actually contain a command
// right now — the backend is installed and a real command under it succeeds.
// It answers about the machine, not about a configuration or a preset: a
// conversation that chose full access runs uncontained on a machine that has a
// sandbox, and the two questions are different (plan 215 §3.1).
func SandboxAvailable() bool {
	return probeSandboxReason(detectPlatform()) == ""
}

// SetSandboxAvailableForTest pins the machine's answer and returns the
// restore. It exists because a "machine without a sandbox" is the one thing a
// test cannot arrange on a machine that has one; nothing in the product calls
// it.
func SetSandboxAvailableForTest(available bool) (restore func()) {
	sandboxProbe.mu.Lock()
	prev := sandboxProbe.forced
	reason := ""
	if !available {
		reason = "bubblewrap_not_found"
	}
	sandboxProbe.forced = &reason
	sandboxProbe.mu.Unlock()
	return func() {
		sandboxProbe.mu.Lock()
		sandboxProbe.forced = prev
		sandboxProbe.mu.Unlock()
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
