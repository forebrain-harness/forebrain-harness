// The process tool: a command that outlives the call that started it, the log
// it writes, the readiness wait, and the kill.
package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
)

// ProcessInput is one action of the process tool. One tool with five actions,
// not five tools: the tool array renders ahead of the system prompt and sits in
// the cached prefix, so every entry costs every request.
type ProcessInput struct {
	Action       string `json:"action" jsonschema_description:"start a command in the background, read its log, wait for it to be ready, kill it, or list what is running."`
	ID           int    `json:"id,omitempty" jsonschema_description:"The id start returned; required by log, wait and kill."`
	Command      string `json:"command,omitempty" jsonschema:"description=start: the command to run."`
	WorkDir      string `json:"work_dir,omitempty" jsonschema:"description=start: the directory to run it in. Defaults to the session's project directory."`
	TimeoutMs    int    `json:"timeout_ms,omitempty" jsonschema:"description=start with wait: give up on readiness after this long. wait: give up after this long. Default 120000."`
	Wait         string `json:"wait,omitempty" jsonschema:"enum=tcp,enum=http,enum=log,description=start: optionally wait for readiness before returning. wait: how readiness is probed."`
	Address      string `json:"address,omitempty" jsonschema:"description=wait tcp: host:port. wait http: a URL."`
	ExpectStatus int    `json:"expect_status,omitempty" jsonschema:"description=wait http: the status that means ready. Default 200."`
	Pattern      string `json:"pattern,omitempty" jsonschema:"description=wait log: a regex in the output that means ready."`
	Tail         int    `json:"tail,omitempty" jsonschema:"description=log: last N lines. Default 200."`
}

const processToolDescription = "Run a command that outlives this call, so a server or watcher can be started and driven: `start` returns immediately with an id and pid, `wait` blocks until a port, an HTTP status or a log line says it is ready, `log` reads what it printed, `kill` ends it, `list` shows what is still running. A process lives until it is killed, the runner that started it is closed, or the harness exits - NOT until the session ends, and several conversations share one runner, so a process you started is visible to (and killable from) another one. Its output goes to <state root>/state/process/<id>.log. Prefer this over `shell` for anything that does not finish: `shell` kills a command at its timeout (default 120s), which is a build or a test but never a dev server. A failed `wait` returns the log tail - read it first, it is the only evidence of why the server did not come up; a taken port shows up there as `address already in use`."

// defaultProcessWaitTimeout bounds a readiness wait when the caller names none,
// and defaultProcessTail bounds the log lines a failed wait carries.
const (
	defaultProcessWaitTimeout = 120 * time.Second
	defaultProcessTail        = 200
	processProbeTimeout       = 2 * time.Second
	processKillSettleTimeout  = 5 * time.Second
)

// processStateDir is where a process's log and the PID ledger live, mirroring
// the shell tool's state/tool-output convention.
func processStateDir(stateRoot string) string {
	root := strings.TrimSpace(stateRoot)
	if root == "" {
		root = os.TempDir()
	}
	return filepath.Join(root, "state", "process")
}

// processRecord is one process this State started and has not forgotten.
// settled closes once the exit watcher has run: the process is completely over,
// its log closed and its ledger entry gone.
type processRecord struct {
	id        int
	handle    *safety.ProcessHandle
	command   string
	workDir   string
	sessionID string
	logPath   string
	startedAt time.Time
	settled   chan struct{}
}

// processRegistry is the per-State list of processes, so the tool can name them
// by id and a runner's teardown can kill what is left. One mutex guards it: a
// wait polls while a kill may land.
type processRegistry struct {
	mu      sync.Mutex
	next    int
	records map[int]*processRecord
	logs    map[int]*os.File
}

func newProcessRegistry() *processRegistry {
	return &processRegistry{records: map[int]*processRecord{}, logs: map[int]*os.File{}}
}

// reserve hands out the next id and the log file it owns. The id is taken
// before the process exists so the log has a name the moment output arrives.
func (r *processRegistry) reserve(dir string) (int, *os.File, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.next == 0 {
		r.next = 1
	}
	id := r.next
	r.next++
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return 0, nil, "", err
	}
	logPath := filepath.Join(dir, fmt.Sprintf("%d.log", id))
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return 0, nil, "", err
	}
	r.logs[id] = file
	return id, file, logPath, nil
}

// abandon drops the id reserved for a start that failed.
func (r *processRegistry) abandon(id int) {
	r.mu.Lock()
	file := r.logs[id]
	delete(r.logs, id)
	r.mu.Unlock()
	if file != nil {
		_ = file.Close()
	}
}

func (r *processRegistry) add(rec *processRecord) {
	r.mu.Lock()
	r.records[rec.id] = rec
	r.mu.Unlock()
}

func (r *processRegistry) get(id int) (*processRecord, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.records[id]
	return rec, ok
}

// release closes the log of a process that has exited. The record stays, so
// `list` can still say what ran and where its log is.
func (r *processRegistry) release(id int) {
	r.mu.Lock()
	file := r.logs[id]
	delete(r.logs, id)
	r.mu.Unlock()
	if file != nil {
		_ = file.Close()
	}
}

// running is every process the registry holds that has not exited, newest
// first: teardown kills in reverse start order.
func (r *processRegistry) running() []*processRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*processRecord, 0, len(r.records))
	for _, rec := range r.records {
		if rec.handle != nil && !rec.handle.Exited() {
			out = append(out, rec)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id > out[j].id })
	return out
}

// all is every record, oldest first, for `list`.
func (r *processRegistry) all() []*processRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*processRecord, 0, len(r.records))
	for _, rec := range r.records {
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

// processRegistryFor returns this State's registry, creating it on first use.
func (s *State) processRegistryFor() *processRegistry {
	if s == nil {
		return newProcessRegistry()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.processes == nil {
		s.processes = newProcessRegistry()
	}
	return s.processes
}

// KillProcesses ends every process this State started that is still running,
// newest first: a runner's teardown owns the background processes its sessions
// started, because nothing else in the product does (owner 2026-11-23,
// plans/358). Killing what has already exited is a no-op.
func (s *State) KillProcesses() {
	if s == nil || s.processes == nil {
		return
	}
	registry := s.processRegistryFor()
	for _, rec := range registry.running() {
		if err := rec.handle.Kill(); err != nil {
			slog.Warn("background process kill failed", "id", rec.id, "pid", rec.handle.PID(), "err", err)
			continue
		}
		slog.Info("killed background process on teardown", "id", rec.id, "pid", rec.handle.PID(), "command", redactForLog(rec.command))
		removeLedgerRecord(processStateDir(s.stateRootForProcess()), rec.id)
		registry.release(rec.id)
		awaitProcessSettled(rec, processKillSettleTimeout)
	}
}

// stateRootForProcess is the root the process tool writes under.
func (s *State) stateRootForProcess() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.processStateRoot
}

func (s *State) setProcessStateRoot(root string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.processStateRoot = strings.TrimSpace(root)
	s.mu.Unlock()
}

// NewProcessTool builds the process tool. It is registered unconditionally,
// like retrieve_output: a tool array whose shape depends on the project type
// re-bills every cached prefix.
func NewProcessTool(st *State, rt *AgentToolRuntime) (*llm.Tool, error) {
	if st != nil && rt != nil {
		st.setProcessStateRoot(rt.StateRoot())
	}
	tool, err := llm.NewTool(
		"process",
		processToolDescription,
		func(ctx context.Context, in *ProcessInput) (string, error) {
			if st == nil {
				return "", fmt.Errorf("nil state")
			}
			if err := st.GuardTool(ctx, "process"); err != nil {
				return "", err
			}
			if in == nil {
				return "", fmt.Errorf("input required")
			}
			switch strings.ToLower(strings.TrimSpace(in.Action)) {
			case "start":
				return processStart(ctx, st, rt, in)
			case "log":
				return processLog(st, in)
			case "wait":
				return processWait(ctx, st, in)
			case "kill":
				return processKill(st, in)
			case "list":
				return processList(st)
			case "":
				return "", fmt.Errorf("action required: start, log, wait, kill or list")
			default:
				return "", fmt.Errorf("action %q is not start, log, wait, kill or list", in.Action)
			}
		},
	)
	if err != nil {
		return nil, err
	}
	return tool, nil
}

func processStart(ctx context.Context, st *State, rt *AgentToolRuntime, in *ProcessInput) (string, error) {
	command := strings.TrimSpace(in.Command)
	if command == "" {
		return "", fmt.Errorf("command is required for action start")
	}
	workDir := strings.TrimSpace(in.WorkDir)
	if workDir == "" {
		wd, err := WorkingDir(ctx)
		if err != nil {
			return "", err
		}
		workDir = wd
	}
	dir := processStateDir(st.stateRootForProcess())
	registry := st.processRegistryFor()
	id, logFile, logPath, err := registry.reserve(dir)
	if err != nil {
		return "", err
	}
	// The command is sandboxed exactly like a shell command: same config, same
	// manager, same request. A background server is not a reason to weaken
	// containment.
	cfg := sessionConfig(ctx, st, rt)
	snap := permissionSnapshotForRun(permissionSnapshotForContext(ctx, st, rt), RunIDFromContext(ctx))
	manager := safety.NewManager()
	defer manager.Close()
	runtimeCfg := manager.UpdateConfig(cfg, shellToolSettingsSources(rt, cfg), snap, workDir, workDir, os.TempDir(), sandboxAdditionalDirs(ctx, st))
	env := append(planModeShellEnv(ctx, rt), agentRuntimeEnv(rt)...)
	// Writes go straight to the spool file: the callback runs on the command's
	// own pipe readers and must return quickly, so it formats nothing.
	onOutput := func(_ safety.OutputStream, chunk []byte) {
		_, _ = logFile.Write(chunk)
	}
	req := safety.CommandRequest{
		ToolKind:       safety.ToolKindShell,
		Command:        command,
		WorkDir:        workDir,
		Profile:        safety.ProfileForConfig(cfg),
		AllowNetwork:   runtimeCfg.NetworkEnabled,
		AllowLoopback:  runtimeCfg.NetworkLoopback,
		Env:            env,
		OnOutput:       onOutput,
		OutputSpoolDir: dir,
	}
	handle, err := manager.StartCommand(ctx, cfg, req)
	if err != nil {
		registry.abandon(id)
		return "", err
	}
	sessionID := ConversationSessionIDFromContext(ctx)
	rec := &processRecord{id: id, handle: handle, command: command, workDir: workDir, sessionID: sessionID, logPath: logPath, startedAt: time.Now(), settled: make(chan struct{})}
	registry.add(rec)
	addLedgerRecord(dir, processLedgerRecord{
		ID: id, PID: handle.PID(), Command: command, SessionID: sessionID,
		HarnessPID: os.Getpid(), StartToken: processStartToken(handle.PID()),
	})
	// The log closes and the ledger entry goes when the process exits, whether
	// or not anybody is still watching it.
	go func() {
		<-handle.Done()
		registry.release(id)
		removeLedgerRecord(dir, id)
		close(rec.settled)
	}()

	payload := map[string]any{"id": id, "pid": handle.PID(), "log": logPath, "command": command, "running": !handle.Exited()}
	if strings.TrimSpace(in.Wait) == "" {
		return marshalProcessResult(payload)
	}
	readiness, err := processReadiness(ctx, rec, in)
	if err != nil {
		return "", err
	}
	for key, value := range readiness {
		payload[key] = value
	}
	// The wait can end on the process's exit; running was snapshotted before it.
	payload["running"] = !handle.Exited()
	return marshalProcessResult(payload)
}

func processLog(st *State, in *ProcessInput) (string, error) {
	rec, err := processRecordFor(st, in)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(rec.logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return marshalProcessResult(map[string]any{"id": rec.id, "log": rec.logPath, "bytes": 0, "tail": ""})
		}
		return "", err
	}
	tail := in.Tail
	if tail <= 0 {
		tail = defaultProcessTail
	}
	lines, shown := tailLines(string(data), tail)
	payload := map[string]any{
		"id": rec.id, "command": rec.command, "log": rec.logPath,
		"bytes": len(data), "running": !rec.handle.Exited(),
		"tail": lines, "lines": shown,
	}
	if shown < countLines(string(data)) {
		payload["omitted_lines"] = countLines(string(data)) - shown
		payload["note"] = "this is the tail; read the log file at `log` for the rest"
	}
	return marshalProcessResult(payload)
}

func processKill(st *State, in *ProcessInput) (string, error) {
	rec, err := processRecordFor(st, in)
	if err != nil {
		return "", err
	}
	// Idempotent by design: killing a process that has already exited is the
	// state the caller asked for, not an error.
	alreadyExited := rec.handle.Exited()
	if err := rec.handle.Kill(); err != nil {
		return "", err
	}
	deadline := time.Now().Add(processKillSettleTimeout)
	for !rec.handle.Exited() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	awaitProcessSettled(rec, processKillSettleTimeout)
	res, _ := rec.handle.Result()
	return marshalProcessResult(map[string]any{
		"id": rec.id, "pid": rec.handle.PID(), "killed": !alreadyExited,
		"running": !rec.handle.Exited(), "exit_code": res.ExitCode,
	})
}

// awaitProcessSettled waits until the exit watcher of rec has run, so a caller
// that ended the process returns with the ledger already free of it. The bound
// keeps a process that ignores its kill from hanging the caller: the watcher
// only takes the registry mutex and the ledger mutex and never waits on us.
func awaitProcessSettled(rec *processRecord, timeout time.Duration) {
	select {
	case <-rec.settled:
	case <-time.After(timeout):
	}
}

func processList(st *State) (string, error) {
	registry := st.processRegistryFor()
	entries := make([]map[string]any, 0, len(registry.all()))
	for _, rec := range registry.all() {
		res, _ := rec.handle.Result()
		entry := map[string]any{
			"id": rec.id, "pid": rec.handle.PID(), "command": rec.command,
			"work_dir": rec.workDir, "session": rec.sessionID, "log": rec.logPath,
			"running": !rec.handle.Exited(), "started_ms": rec.startedAt.UnixMilli(),
		}
		if rec.handle.Exited() {
			entry["exit_code"] = res.ExitCode
		}
		if info, err := os.Stat(rec.logPath); err == nil {
			entry["log_bytes"] = info.Size()
		}
		entries = append(entries, entry)
	}
	return marshalProcessResult(map[string]any{"processes": entries, "count": len(entries)})
}

func processRecordFor(st *State, in *ProcessInput) (*processRecord, error) {
	if in == nil || in.ID <= 0 {
		return nil, fmt.Errorf("id is required (the number start returned)")
	}
	rec, ok := st.processRegistryFor().get(in.ID)
	if !ok {
		return nil, fmt.Errorf("no process with id %d in this runner", in.ID)
	}
	return rec, nil
}

// processWait polls readiness until it holds, the deadline passes or the
// process exits. A failed wait carries the log tail: it is the only evidence of
// why the server did not come up.
func processWait(ctx context.Context, st *State, in *ProcessInput) (string, error) {
	rec, err := processRecordFor(st, in)
	if err != nil {
		return "", err
	}
	if rec.handle.Exited() {
		res, _ := rec.handle.Result()
		return marshalProcessResult(map[string]any{
			"id": rec.id, "ready": false, "exited": true, "exit_code": res.ExitCode,
			"tail": logTail(rec.logPath, defaultProcessTail), "elapsed_ms": 0,
		})
	}
	readiness, err := processReadiness(ctx, rec, in)
	if err != nil {
		return "", err
	}
	readiness["id"] = rec.id
	return marshalProcessResult(readiness)
}

// processReadiness runs the readiness poll for one process and reports what it
// found. Every branch that ends not-ready carries the log tail.
func processReadiness(ctx context.Context, rec *processRecord, in *ProcessInput) (map[string]any, error) {
	mode := strings.ToLower(strings.TrimSpace(in.Wait))
	switch mode {
	case "tcp", "http", "log":
	case "":
		return nil, fmt.Errorf("wait is required for a readiness poll: tcp, http or log")
	default:
		return nil, fmt.Errorf("wait %q is not tcp, http or log", in.Wait)
	}
	address := strings.TrimSpace(in.Address)
	pattern := strings.TrimSpace(in.Pattern)
	if mode != "log" && address == "" {
		return nil, fmt.Errorf("address is required for wait %s", mode)
	}
	if mode == "log" {
		if pattern == "" {
			return nil, fmt.Errorf("pattern is required for wait log")
		}
		if _, err := regexp.Compile(pattern); err != nil {
			return nil, fmt.Errorf("pattern: %w", err)
		}
	}
	expected := in.ExpectStatus
	if expected <= 0 {
		expected = 200
	}
	budget := defaultProcessWaitTimeout
	if in.TimeoutMs > 0 {
		budget = time.Duration(in.TimeoutMs) * time.Millisecond
	}
	startedAt := time.Now()
	deadline := startedAt.Add(budget)
	interval := 100 * time.Millisecond
	for {
		ready := false
		switch mode {
		case "tcp":
			ready = probeTCP(address)
		case "http":
			ready = probeHTTP(ctx, address, expected)
		case "log":
			ready = probeLog(rec.logPath, pattern)
		}
		elapsed := time.Since(startedAt)
		if ready {
			return map[string]any{"ready": true, "elapsed_ms": elapsed.Milliseconds()}, nil
		}
		if rec.handle.Exited() {
			res, _ := rec.handle.Result()
			return map[string]any{
				"ready": false, "exited": true, "exit_code": res.ExitCode,
				"elapsed_ms": elapsed.Milliseconds(), "tail": logTail(rec.logPath, defaultProcessTail),
			}, nil
		}
		if ctx != nil && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if time.Now().After(deadline) {
			return map[string]any{
				"ready": false, "exited": false, "elapsed_ms": elapsed.Milliseconds(),
				"timeout_ms": budget.Milliseconds(), "tail": logTail(rec.logPath, defaultProcessTail),
			}, nil
		}
		if elapsed > 2*time.Second {
			interval = 500 * time.Millisecond
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
	}
}

func probeTCP(address string) bool {
	conn, err := net.DialTimeout("tcp", address, processProbeTimeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func probeHTTP(ctx context.Context, address string, expected int) bool {
	url := address
	if !strings.Contains(url, "://") {
		url = "http://" + url
	}
	probeCtx, cancel := context.WithTimeout(ctx, processProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == expected
}

func probeLog(path, pattern string) bool {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return re.Match(data)
}

// logTail is the last n lines of a log file, or the empty string when there is
// nothing to read.
func logTail(path string, n int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	tail, _ := tailLines(string(data), n)
	return tail
}

// tailLines returns the last n lines of text and how many lines that was.
func tailLines(text string, n int) (string, int) {
	if text == "" || n <= 0 {
		return "", 0
	}
	trimmed := strings.TrimSuffix(text, "\n")
	lines := strings.Split(trimmed, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n"), len(lines)
}

func countLines(text string) int {
	if text == "" {
		return 0
	}
	return strings.Count(strings.TrimSuffix(text, "\n"), "\n") + 1
}

func marshalProcessResult(payload map[string]any) (string, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// processLedgerRecord is one live process as a later launch reads it: the PID,
// the OS start time that guards against PID reuse, and who started it.
type processLedgerRecord struct {
	ID         int    `json:"id"`
	PID        int    `json:"pid"`
	StartToken string `json:"start_token,omitempty"`
	Command    string `json:"command"`
	SessionID  string `json:"session_id,omitempty"`
	HarnessPID int    `json:"harness_pid"`
}

// processLedgerFile is the on-disk shape: a crash mid-write must never leave a
// half ledger, so the writer replaces the file whole.
type processLedgerFile struct {
	Processes []processLedgerRecord `json:"processes"`
}

// processLedgerMu serializes the ledger's read-modify-write. Two Runners of one
// project share a state root, so the file has one writer at a time across the
// process.
var processLedgerMu sync.Mutex

func processLedgerPath(dir string) string { return filepath.Join(dir, "ledger.json") }

// readProcessLedger returns the ledger's records. A missing or malformed file is
// no records: the ledger is a kill list, and a launch must never be stopped by
// one it cannot read.
func readProcessLedger(dir string) []processLedgerRecord {
	data, err := os.ReadFile(processLedgerPath(dir))
	if err != nil {
		return nil
	}
	var doc processLedgerFile
	if err := json.Unmarshal(data, &doc); err != nil {
		slog.Warn("process ledger unreadable; ignoring it", "path", processLedgerPath(dir), "err", err)
		return nil
	}
	return doc.Processes
}

// writeProcessLedger replaces the ledger atomically: a temporary file in the
// same directory, then a rename.
func writeProcessLedger(dir string, records []processLedgerRecord) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	encoded, err := json.Marshal(processLedgerFile{Processes: records})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "ledger-*.json")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(encoded); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	_ = tmp.Chmod(0o600)
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, processLedgerPath(dir)); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

func addLedgerRecord(dir string, rec processLedgerRecord) {
	if rec.ID == 0 {
		return
	}
	processLedgerMu.Lock()
	defer processLedgerMu.Unlock()
	records := readProcessLedger(dir)
	for i, existing := range records {
		if existing.ID == rec.ID {
			records[i] = rec
			_ = writeProcessLedger(dir, records)
			return
		}
	}
	_ = writeProcessLedger(dir, append(records, rec))
}

func removeLedgerRecord(dir string, id int) {
	if id == 0 {
		return
	}
	processLedgerMu.Lock()
	defer processLedgerMu.Unlock()
	records := readProcessLedger(dir)
	kept := records[:0]
	found := false
	for _, existing := range records {
		if existing.ID == id {
			found = true
			continue
		}
		kept = append(kept, existing)
	}
	if !found {
		return
	}
	_ = writeProcessLedger(dir, kept)
}

// processStartToken is the OS start time of pid, or the empty string when the
// platform cannot say — a record without one is never killed.
func processStartToken(pid int) string {
	token, ok := safety.ProcessStartToken(pid)
	if !ok {
		return ""
	}
	return token
}

// ReapOrphanProcesses kills the processes a crashed harness left behind, and is
// what closes the window a SIGKILL opens: only Linux's --die-with-parent ties a
// sandboxed child to the harness's death, so the next launch reads the ledger
// and ends what is still running. Windows has no reap — there is no cheap
// start-time check there, and a record without one is never killed.
//
// The OS start time is the guard that makes this safe: a PID the OS has recycled
// belongs to a different process, whose start time does not match the record,
// and that process is never touched.
//
// It is best effort by contract: a missing or malformed ledger, an unreadable
// /proc, or a failed kill must never stop an install from starting.
func ReapOrphanProcesses(stateRoot string) []int {
	dir := processStateDir(stateRoot)
	processLedgerMu.Lock()
	defer processLedgerMu.Unlock()
	records := readProcessLedger(dir)
	if len(records) == 0 {
		return nil
	}
	self := os.Getpid()
	var killed []int
	kept := make([]processLedgerRecord, 0, len(records))
	for _, rec := range records {
		if rec.HarnessPID == self {
			// Nothing of this process is running yet, but a record that names
			// this PID is not ours to judge; leave it where it is.
			kept = append(kept, rec)
			continue
		}
		if rec.PID <= 0 || rec.StartToken == "" {
			continue
		}
		token, ok := safety.ProcessStartToken(rec.PID)
		if !ok || token != rec.StartToken {
			// Gone, or a recycled PID: never kill on the PID alone.
			continue
		}
		if err := safety.KillProcessGroupByPID(rec.PID); err != nil {
			slog.Warn("orphaned process reap failed", "pid", rec.PID, "err", err)
			continue
		}
		killed = append(killed, rec.PID)
		slog.Warn("reaped a background process an earlier harness left behind",
			"pid", rec.PID, "command", redactForLog(rec.Command), "session", rec.SessionID)
	}
	if err := writeProcessLedger(dir, kept); err != nil {
		slog.Warn("process ledger rewrite failed", "path", processLedgerPath(dir), "err", err)
	}
	return killed
}
