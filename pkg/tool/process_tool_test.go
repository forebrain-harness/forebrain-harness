package tool

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
)

// newProcessToolForTest builds the tool over a throwaway state root. The config
// is danger-full-access so these tests pin the tool, not the sandbox — the
// sandboxed start is pinned in pkg/safety's handle tests.
func newProcessToolForTest(t *testing.T) (*State, *llm.Tool, *AgentToolRuntime, string) {
	t.Helper()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	projectDir := filepath.Join(root, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rt := &AgentToolRuntime{Home: root, WorkspaceRoot: workspace, Cfg: &appcfg.Root{SandboxMode: appcfg.SandboxModeDangerFullAccess}}
	st := NewState(projectDir)
	t.Cleanup(st.KillProcesses)
	tl, err := NewProcessTool(st, rt)
	if err != nil {
		t.Fatal(err)
	}
	return st, tl, rt, projectDir
}

func processTestContext(projectDir string) context.Context {
	return WithProjectRoot(WithConversationSessionID(context.Background(), "sess-1"), projectDir)
}

func callProcess(t *testing.T, tl *llm.Tool, ctx context.Context, in ProcessInput) (map[string]any, error) {
	t.Helper()
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	result, err := tl.Handle(ctx, string(raw))
	if err != nil {
		return nil, err
	}
	text, _ := result.(string)
	var payload map[string]any
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		t.Fatalf("result %q is not JSON: %v", text, err)
	}
	return payload, nil
}

func processTestID(t *testing.T, payload map[string]any) int {
	t.Helper()
	value, ok := payload["id"].(float64)
	if !ok || value <= 0 {
		t.Fatalf("result has no id: %+v", payload)
	}
	return int(value)
}

func processRecordForTest(t *testing.T, st *State, id int) *processRecord {
	t.Helper()
	rec, ok := st.processRegistryFor().get(id)
	if !ok {
		t.Fatalf("no process %d in the registry", id)
	}
	return rec
}

// waitForProcessSettled blocks until the process's exit watcher has run: its log
// is closed and its ledger entry gone. Waiting for handle.Done() is not enough,
// because the watcher writes the ledger after it, and that write landing during
// t.TempDir's RemoveAll is what made this file flaky under load.
//
// The tests whose process is still running when they end kill it with the
// tool's own `kill` instead: that wait is bounded and belongs to the product
// (processKill), so the assertion here stays about a process that already
// exited.
func waitForProcessSettled(t *testing.T, rec *processRecord) {
	t.Helper()
	select {
	case <-rec.settled:
	case <-time.After(10 * time.Second):
		t.Fatalf("process %d never settled: its exit watcher has not written the ledger", rec.id)
	}
}

// 1. A trivial command runs to completion and its output is the log.
func TestProcessStartWaitsForExitAndLogs(t *testing.T) {
	st, tl, _, projectDir := newProcessToolForTest(t)
	ctx := processTestContext(projectDir)
	started, err := callProcess(t, tl, ctx, ProcessInput{Action: "start", Command: "echo hi", WorkDir: projectDir})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	id := processTestID(t, started)
	rec := processRecordForTest(t, st, id)
	select {
	case <-rec.handle.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the command never exited")
	}
	waitForProcessSettled(t, rec)
	logged, err := callProcess(t, tl, ctx, ProcessInput{Action: "log", ID: id})
	if err != nil {
		t.Fatalf("log: %v", err)
	}
	if tail, _ := logged["tail"].(string); !strings.Contains(tail, "hi") {
		t.Fatalf("log tail = %q, want the command's output", tail)
	}
	if running, _ := logged["running"].(bool); running {
		t.Fatalf("a finished command still reports running: %+v", logged)
	}
}

// 2. The readiness poll dials the address itself, so the assertion needs no
// loopback from inside the command and does not depend on the sandbox's network
// setting.
func TestProcessWaitTCPReportsReadyWhileRunning(t *testing.T) {
	st, tl, _, projectDir := newProcessToolForTest(t)
	ctx := processTestContext(projectDir)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	started, err := callProcess(t, tl, ctx, ProcessInput{Action: "start", Command: "sleep 30", WorkDir: projectDir})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	id := processTestID(t, started)
	rec := processRecordForTest(t, st, id)
	defer func() { _, _ = callProcess(t, tl, ctx, ProcessInput{Action: "kill", ID: rec.id}) }()
	waited, err := callProcess(t, tl, ctx, ProcessInput{
		Action: "wait", ID: id, Wait: "tcp", Address: listener.Addr().String(), TimeoutMs: 5000,
	})
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if ready, _ := waited["ready"].(bool); !ready {
		t.Fatalf("wait did not see the listening port: %+v", waited)
	}
	if rec.handle.Exited() {
		t.Fatal("the process exited while the wait was ready")
	}
}

// 3. A wait that never becomes ready fails with the log tail: that tail is the
// only evidence of why the server did not come up.
func TestProcessWaitTCPTimesOutWithTheLogTail(t *testing.T) {
	_, tl, _, projectDir := newProcessToolForTest(t)
	ctx := processTestContext(projectDir)
	// A port nothing listens on: bind and release so the number is free.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := probe.Addr().String()
	_ = probe.Close()

	started, err := callProcess(t, tl, ctx, ProcessInput{Action: "start", Command: "echo no-server-here; sleep 30", WorkDir: projectDir})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	id := processTestID(t, started)
	waited, err := callProcess(t, tl, ctx, ProcessInput{Action: "wait", ID: id, Wait: "tcp", Address: address, TimeoutMs: 400})
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if ready, _ := waited["ready"].(bool); ready {
		t.Fatalf("wait reported ready on a closed port: %+v", waited)
	}
	tail, _ := waited["tail"].(string)
	if !strings.Contains(tail, "no-server-here") {
		t.Fatalf("a failed wait must carry the log tail, got %+v", waited)
	}
}

// 4. A process that already exited is reported as exited instead of being
// polled to the timeout.
func TestProcessWaitOnAnExitedProcessReturnsImmediately(t *testing.T) {
	st, tl, _, projectDir := newProcessToolForTest(t)
	ctx := processTestContext(projectDir)
	started, err := callProcess(t, tl, ctx, ProcessInput{Action: "start", Command: "echo done", WorkDir: projectDir})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	id := processTestID(t, started)
	rec := processRecordForTest(t, st, id)
	select {
	case <-rec.handle.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the command never exited")
	}
	waitForProcessSettled(t, rec)
	begin := time.Now()
	waited, err := callProcess(t, tl, ctx, ProcessInput{Action: "wait", ID: id, Wait: "tcp", Address: "127.0.0.1:1", TimeoutMs: 5000})
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if elapsed := time.Since(begin); elapsed > 2*time.Second {
		t.Fatalf("a wait on a dead process took %s", elapsed)
	}
	if exited, _ := waited["exited"].(bool); !exited {
		t.Fatalf("wait did not report the process as exited: %+v", waited)
	}
	if ready, _ := waited["ready"].(bool); ready {
		t.Fatalf("a dead process was reported ready: %+v", waited)
	}
}

// 5. Killing is idempotent: the second kill is the state the caller asked for.
func TestProcessKillIsIdempotent(t *testing.T) {
	_, tl, _, projectDir := newProcessToolForTest(t)
	ctx := processTestContext(projectDir)
	started, err := callProcess(t, tl, ctx, ProcessInput{Action: "start", Command: "sleep 30", WorkDir: projectDir})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	id := processTestID(t, started)
	first, err := callProcess(t, tl, ctx, ProcessInput{Action: "kill", ID: id})
	if err != nil {
		t.Fatalf("first kill: %v", err)
	}
	if killed, _ := first["killed"].(bool); !killed {
		t.Fatalf("the first kill did not report a kill: %+v", first)
	}
	if running, _ := first["running"].(bool); running {
		t.Fatalf("the process is still running after its kill: %+v", first)
	}
	second, err := callProcess(t, tl, ctx, ProcessInput{Action: "kill", ID: id})
	if err != nil {
		t.Fatalf("killing an exited process must succeed, got %v", err)
	}
	if running, _ := second["running"].(bool); running {
		t.Fatalf("the process came back after its second kill: %+v", second)
	}
}

// 6. list is the answer to "what did I leave running".
func TestProcessListReflectsStartAndKill(t *testing.T) {
	st, tl, _, projectDir := newProcessToolForTest(t)
	ctx := processTestContext(projectDir)
	sleeping, err := callProcess(t, tl, ctx, ProcessInput{Action: "start", Command: "sleep 30", WorkDir: projectDir})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	sleepID := processTestID(t, sleeping)
	finished, err := callProcess(t, tl, ctx, ProcessInput{Action: "start", Command: "true", WorkDir: projectDir})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	finishedID := processTestID(t, finished)
	rec := processRecordForTest(t, st, finishedID)
	select {
	case <-rec.handle.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the second command never exited")
	}
	waitForProcessSettled(t, rec)

	listed, err := callProcess(t, tl, ctx, ProcessInput{Action: "list"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	entries, _ := listed["processes"].([]any)
	if len(entries) != 2 {
		t.Fatalf("list = %+v, want both processes", listed)
	}
	state := map[int]map[string]any{}
	for _, entry := range entries {
		object, _ := entry.(map[string]any)
		state[processTestID(t, object)] = object
	}
	if running, _ := state[sleepID]["running"].(bool); !running {
		t.Fatalf("the sleeping process is not listed as running: %+v", state[sleepID])
	}
	if running, _ := state[finishedID]["running"].(bool); running {
		t.Fatalf("the finished process is listed as running: %+v", state[finishedID])
	}
	if session, _ := state[sleepID]["session"].(string); session != "sess-1" {
		t.Fatalf("list lost the owning session: %+v", state[sleepID])
	}

	if _, err := callProcess(t, tl, ctx, ProcessInput{Action: "kill", ID: sleepID}); err != nil {
		t.Fatalf("kill: %v", err)
	}
	after, err := callProcess(t, tl, ctx, ProcessInput{Action: "list"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	entries, _ = after["processes"].([]any)
	for _, entry := range entries {
		object, _ := entry.(map[string]any)
		if processTestID(t, object) == sleepID {
			if running, _ := object["running"].(bool); running {
				t.Fatalf("a killed process is still listed as running: %+v", object)
			}
		}
	}
}

// 7. Teardown kills what is left: the runner that started a process owns it,
// because nothing else in the product does.
func TestKillProcessesEndsEveryRunningProcess(t *testing.T) {
	st, tl, _, projectDir := newProcessToolForTest(t)
	ctx := processTestContext(projectDir)
	first, err := callProcess(t, tl, ctx, ProcessInput{Action: "start", Command: "sleep 30", WorkDir: projectDir})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	second, err := callProcess(t, tl, ctx, ProcessInput{Action: "start", Command: "sleep 30", WorkDir: projectDir})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	firstRec := processRecordForTest(t, st, processTestID(t, first))
	secondRec := processRecordForTest(t, st, processTestID(t, second))

	st.KillProcesses()

	for name, rec := range map[string]*processRecord{"first": firstRec, "second": secondRec} {
		select {
		case <-rec.handle.Done():
		case <-time.After(10 * time.Second):
			t.Fatalf("%s process survived teardown", name)
		}
	}
}

// 8. A wait polling while a kill lands must not race: `go test -race -run
// TestProcess` is the assertion, and the poll ends instead of spinning.
func TestProcessWaitAndKillDoNotRace(t *testing.T) {
	st, tl, _, projectDir := newProcessToolForTest(t)
	ctx := processTestContext(projectDir)
	started, err := callProcess(t, tl, ctx, ProcessInput{Action: "start", Command: "sleep 30", WorkDir: projectDir})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	id := processTestID(t, started)
	rec := processRecordForTest(t, st, id)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = callProcess(t, tl, ctx, ProcessInput{Action: "wait", ID: id, Wait: "tcp", Address: "127.0.0.1:1", TimeoutMs: 10000})
	}()
	time.Sleep(100 * time.Millisecond)
	if _, err := callProcess(t, tl, ctx, ProcessInput{Action: "kill", ID: id}); err != nil {
		t.Fatalf("kill: %v", err)
	}
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("the wait did not end when its process was killed")
	}
	if !rec.handle.Exited() {
		t.Fatal("the process survived its kill")
	}
}

// 9. The log lives under the state root's process directory, beside the ledger,
// and it grows while the process runs.
func TestProcessLogSpoolGrowsUnderTheStateRoot(t *testing.T) {
	st, tl, rt, projectDir := newProcessToolForTest(t)
	ctx := processTestContext(projectDir)
	started, err := callProcess(t, tl, ctx, ProcessInput{
		Action:  "start",
		Command: "echo first; sleep 0.4; echo second; sleep 30",
		WorkDir: projectDir,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	id := processTestID(t, started)
	rec := processRecordForTest(t, st, id)
	defer func() { _, _ = callProcess(t, tl, ctx, ProcessInput{Action: "kill", ID: rec.id}) }()
	want := filepath.Join(rt.StateRoot(), "state", "process")
	if filepath.Dir(rec.logPath) != want {
		t.Fatalf("log path = %q, want a file under %q", rec.logPath, want)
	}
	deadline := time.Now().Add(10 * time.Second)
	var firstSize int64
	for time.Now().Before(deadline) {
		info, err := os.Stat(rec.logPath)
		if err == nil && info.Size() > 0 {
			firstSize = info.Size()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if firstSize == 0 {
		t.Fatal("the log stayed empty")
	}
	for time.Now().Before(deadline) {
		info, err := os.Stat(rec.logPath)
		if err == nil && info.Size() > firstSize {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the log never grew after the first line")
}

// orphanLedgerRecord writes the record a crashed harness would have left for
// pid, with the OS start time it really has. ok is false on a platform that
// cannot report one (Windows), where the reap is deliberately absent.
func orphanLedgerRecord(t *testing.T, st *State, rt *AgentToolRuntime, id int, pid int, command string, token string) bool {
	t.Helper()
	if token == "" {
		return false
	}
	addLedgerRecord(processStateDir(rt.StateRoot()), processLedgerRecord{
		ID: id, PID: pid, StartToken: token, Command: command, SessionID: "sess-gone", HarnessPID: os.Getpid() + 1,
	})
	return true
}

// 10. The reap ends a process whose ledger record names a different harness,
// and drops the record.
func TestReapOrphanProcessesKillsAMatchingOrphan(t *testing.T) {
	st, tl, rt, projectDir := newProcessToolForTest(t)
	ctx := processTestContext(projectDir)
	started, err := callProcess(t, tl, ctx, ProcessInput{Action: "start", Command: "sleep 30", WorkDir: projectDir})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	rec := processRecordForTest(t, st, processTestID(t, started))
	pid := rec.handle.PID()
	token, ok := safety.ProcessStartToken(pid)
	if !ok {
		_ = rec.handle.Kill()
		t.Skip("this platform reports no process start time, so there is no reap to test")
	}
	if !orphanLedgerRecord(t, st, rt, 99, pid, "sleep 30", token) {
		t.Fatal("the orphan record was not written")
	}

	killed := ReapOrphanProcesses(rt.StateRoot())
	if len(killed) != 1 || killed[0] != pid {
		t.Fatalf("reap killed %v, want [%d]", killed, pid)
	}
	select {
	case <-rec.handle.Done():
	case <-time.After(10 * time.Second):
		t.Fatalf("process %d survived the reap", pid)
	}
	waitForProcessSettled(t, rec)
	for _, record := range readProcessLedger(processStateDir(rt.StateRoot())) {
		if record.ID == 99 {
			t.Fatalf("the reaped record is still in the ledger: %+v", record)
		}
	}
}

// 11. The PID-reuse guard: a record whose start time does not match the live
// process is never killed, because that PID belongs to somebody else now.
func TestReapOrphanProcessesSparesARecycledPID(t *testing.T) {
	st, tl, rt, projectDir := newProcessToolForTest(t)
	ctx := processTestContext(projectDir)
	started, err := callProcess(t, tl, ctx, ProcessInput{Action: "start", Command: "sleep 30", WorkDir: projectDir})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	rec := processRecordForTest(t, st, processTestID(t, started))
	defer func() { _, _ = callProcess(t, tl, ctx, ProcessInput{Action: "kill", ID: rec.id}) }()
	pid := rec.handle.PID()
	token, ok := safety.ProcessStartToken(pid)
	if !ok {
		t.Skip("this platform reports no process start time, so there is no reap to test")
	}
	if !orphanLedgerRecord(t, st, rt, 98, pid, "sleep 30", token+"-stale") {
		t.Fatal("the stale record was not written")
	}

	if killed := ReapOrphanProcesses(rt.StateRoot()); len(killed) != 0 {
		t.Fatalf("reap killed a recycled PID: %v", killed)
	}
	if rec.handle.Exited() {
		t.Fatal("the process was ended by a reap that should have spared it")
	}
}

// 12. A ledger that cannot be read must never stop a launch.
func TestReapOrphanProcessesSurvivesAMalformedLedger(t *testing.T) {
	stateRoot := t.TempDir()
	dir := processStateDir(stateRoot)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(processLedgerPath(dir), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if killed := ReapOrphanProcesses(stateRoot); len(killed) != 0 {
		t.Fatalf("a malformed ledger produced kills: %v", killed)
	}
	// A missing ledger is the ordinary case on a first launch.
	if killed := ReapOrphanProcesses(t.TempDir()); len(killed) != 0 {
		t.Fatalf("a missing ledger produced kills: %v", killed)
	}
}

// A start whose readiness wait ends on the process's own exit must not carry
// the pre-wait running snapshot: exited and running are mutually exclusive.
func TestProcessStartWithWaitEndingOnExitReportsNotRunning(t *testing.T) {
	st, tl, _, projectDir := newProcessToolForTest(t)
	ctx := processTestContext(projectDir)
	started, err := callProcess(t, tl, ctx, ProcessInput{
		Action: "start", Command: "echo hi", WorkDir: projectDir,
		Wait: "log", Pattern: "NEVER-READY-MARK", TimeoutMs: 15000,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if exited, _ := started["exited"].(bool); !exited {
		t.Fatalf("the readiness wait did not end on the process's exit: %+v", started)
	}
	if ready, _ := started["ready"].(bool); ready {
		t.Fatalf("a process that exited was reported ready: %+v", started)
	}
	if running, _ := started["running"].(bool); running {
		t.Fatalf("an exited process still reports running: %+v", started)
	}
	if code, _ := started["exit_code"].(float64); code != 0 {
		t.Fatalf("exit_code = %v, want 0: %+v", started["exit_code"], started)
	}
	// The exit watcher writes the ledger after handle.Done(); await it so that
	// write cannot land during t.TempDir's RemoveAll (the flake waitForProcessSettled's
	// comment documents). KillProcesses only reaps still-running processes, so an
	// already-exited one must be awaited here, exactly like the :101-126 precedent.
	waitForProcessSettled(t, processRecordForTest(t, st, processTestID(t, started)))
}
