package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
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
	defer func() {
		_, _ = callProcess(t, tl, ctx, ProcessInput{Action: "kill", ID: rec.id, Reason: "test cleanup"})
	}()
	waited, err := callProcess(t, tl, ctx, ProcessInput{
		Action: "wait", ID: id, Wait: "tcp", Address: listener.Addr().String(), TimeoutMs: 5000,
	})
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if ready, _ := waited["ready"].(bool); !ready {
		t.Fatalf("wait did not see the listening port: %+v", waited)
	}
	// The card names the command it waited on, so the head can say what is being
	// waited for instead of only which id (owner 2026-11-24).
	if got, _ := waited["command"].(string); got != "sleep 30" {
		t.Fatalf("a ready wait does not name the command: %+v", waited)
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
	if got, _ := waited["command"].(string); got != "echo no-server-here; sleep 30" {
		t.Fatalf("a timed-out wait does not name the command: %+v", waited)
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
	if got, _ := waited["command"].(string); got != "echo done" {
		t.Fatalf("a wait on an exited process does not name the command: %+v", waited)
	}
}

// A wait, kill or log card names the command before any result exists: the
// engine resolves the id against this state's registry when it dispatches the
// call.
func TestProcessStartedCommandResolvesTheID(t *testing.T) {
	st, tl, _, projectDir := newProcessToolForTest(t)
	ctx := processTestContext(projectDir)
	started, err := callProcess(t, tl, ctx, ProcessInput{Action: "start", Command: "sleep 30", WorkDir: projectDir})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	id := processTestID(t, started)
	for _, action := range []string{"wait", "kill", "log"} {
		if got := st.ProcessStartedCommand("process", map[string]any{"action": action, "id": float64(id)}); got != "sleep 30" {
			t.Fatalf("%s resolved to %q, want the command", action, got)
		}
	}
	if _, err := callProcess(t, tl, ctx, ProcessInput{Action: "kill", ID: id, Reason: "test cleanup"}); err != nil {
		t.Fatalf("kill: %v", err)
	}
	// The record outlives the process (list can still say what ran), so a wait on
	// an exited one still names it.
	if got := st.ProcessStartedCommand("process", map[string]any{"action": "wait", "id": float64(id)}); got != "sleep 30" {
		t.Fatalf("an exited process resolved to %q, want its command", got)
	}
	if got := st.ProcessStartedCommand("process", map[string]any{"action": "start", "command": "x"}); got != "" {
		t.Fatalf("start resolved to %q, want nothing (its command is its own argument)", got)
	}
	if got := st.ProcessStartedCommand("shell", map[string]any{"action": "wait", "id": float64(id)}); got != "" {
		t.Fatalf("another tool resolved to %q", got)
	}
	if got := st.ProcessStartedCommand("process", map[string]any{"action": "wait", "id": float64(999)}); got != "" {
		t.Fatalf("an unknown id resolved to %q", got)
	}
}

// A wait that streams output redraws its card from each delta's own facts: a
// delta carrying only the id left the head saying "Calling process #1".
func TestProcessWaitDeltasCarryTheActionAndCommand(t *testing.T) {
	_, tl, _, projectDir := newProcessToolForTest(t)
	command := "while true; do echo tick; sleep 0.2; done"
	plainCtx := processTestContext(projectDir)
	started, err := callProcess(t, tl, plainCtx, ProcessInput{Action: "start", Command: command, WorkDir: projectDir})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	id := processTestID(t, started)
	defer func() {
		_, _ = callProcess(t, tl, plainCtx, ProcessInput{Action: "kill", ID: id, Reason: "test cleanup"})
	}()
	var mu sync.Mutex
	var deltas []StepEvent
	waitCtx := WithStepHook(plainCtx, func(_ context.Context, evt StepEvent) {
		if evt.Kind != StepKindToolOutputDelta || evt.ToolName != "process" {
			return
		}
		mu.Lock()
		deltas = append(deltas, evt)
		mu.Unlock()
	})
	if _, err := callProcess(t, tl, waitCtx, ProcessInput{Action: "wait", ID: id, Wait: "log", Pattern: "never-matches", TimeoutMs: 600}); err != nil {
		t.Fatalf("wait: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		got := append([]StepEvent(nil), deltas...)
		mu.Unlock()
		if len(got) > 0 {
			for _, evt := range got {
				if action, _ := evt.Input["action"].(string); action != "wait" {
					t.Fatalf("a wait delta carries action %q, want wait: %+v", action, evt.Input)
				}
				if cmd, _ := evt.Input["command"].(string); cmd != command {
					t.Fatalf("a wait delta carries command %q, want %q", cmd, command)
				}
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the wait streamed no output delta to inspect")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A start that streams during its readiness wait keeps its verb: its emitter
// carried the command but never the action, so the head fell to "Calling".
func TestProcessStartDeltasCarryTheAction(t *testing.T) {
	_, tl, _, projectDir := newProcessToolForTest(t)
	command := "while true; do echo tick; sleep 0.2; done"
	var mu sync.Mutex
	var deltas []StepEvent
	ctx := WithStepHook(processTestContext(projectDir), func(_ context.Context, evt StepEvent) {
		if evt.Kind != StepKindToolOutputDelta || evt.ToolName != "process" {
			return
		}
		mu.Lock()
		deltas = append(deltas, evt)
		mu.Unlock()
	})
	started, err := callProcess(t, tl, ctx, ProcessInput{
		Action: "start", Command: command, WorkDir: projectDir,
		Wait: "log", Pattern: "never-matches", TimeoutMs: 700,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	id := processTestID(t, started)
	defer func() {
		_, _ = callProcess(t, tl, ctx, ProcessInput{Action: "kill", ID: id, Reason: "test cleanup"})
	}()
	mu.Lock()
	got := append([]StepEvent(nil), deltas...)
	mu.Unlock()
	if len(got) == 0 {
		t.Fatal("the start streamed no output delta to inspect")
	}
	for _, evt := range got {
		if action, _ := evt.Input["action"].(string); action != "start" {
			t.Fatalf("a start delta carries action %q, want start: %+v", action, evt.Input)
		}
		if cmd, _ := evt.Input["command"].(string); cmd != command {
			t.Fatalf("a start delta carries command %q, want %q", cmd, command)
		}
		// This start waits for readiness, so the call blocks: every delta has to
		// say so, or the card goes back to claiming a background start while the
		// turn is stuck in the wait (owner 2026-11-24).
		if waitMode, _ := evt.Input["wait"].(string); waitMode != "log" {
			t.Fatalf("a waiting start delta carries wait %q, want log: %+v", waitMode, evt.Input)
		}
	}
}

// A plain start returns at once, so its deltas must not claim a wait the call
// does not have.
func TestProcessPlainStartDeltasCarryNoWait(t *testing.T) {
	_, tl, _, projectDir := newProcessToolForTest(t)
	command := "while true; do echo tick; sleep 0.2; done"
	// A bare start returns in milliseconds and the emitter closes with the call,
	// so the first chunk is raced against the shell's startup: retry until one
	// lands instead of asserting on that timing.
	for attempt := 0; attempt < 6; attempt++ {
		var mu sync.Mutex
		var deltas []StepEvent
		ctx := WithStepHook(processTestContext(projectDir), func(_ context.Context, evt StepEvent) {
			if evt.Kind != StepKindToolOutputDelta || evt.ToolName != "process" {
				return
			}
			mu.Lock()
			deltas = append(deltas, evt)
			mu.Unlock()
		})
		started, err := callProcess(t, tl, ctx, ProcessInput{Action: "start", Command: command, WorkDir: projectDir})
		if err != nil {
			t.Fatalf("start: %v", err)
		}
		id := processTestID(t, started)
		deadline := time.Now().Add(400 * time.Millisecond)
		for {
			mu.Lock()
			got := append([]StepEvent(nil), deltas...)
			mu.Unlock()
			if len(got) > 0 {
				for _, evt := range got {
					if waitMode, ok := evt.Input["wait"]; ok {
						t.Fatalf("a plain start delta carries wait %v: %+v", waitMode, evt.Input)
					}
					if cmd, _ := evt.Input["command"].(string); cmd != command {
						t.Fatalf("a plain start delta carries command %q, want %q", cmd, command)
					}
				}
				_, _ = callProcess(t, tl, ctx, ProcessInput{Action: "kill", ID: id, Reason: "test cleanup"})
				return
			}
			if time.Now().After(deadline) {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		_, _ = callProcess(t, tl, ctx, ProcessInput{Action: "kill", ID: id, Reason: "test cleanup"})
	}
	t.Fatal("a plain start streamed no output delta to inspect in six attempts")
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
	first, err := callProcess(t, tl, ctx, ProcessInput{Action: "kill", ID: id, Reason: "test cleanup"})
	if err != nil {
		t.Fatalf("first kill: %v", err)
	}
	if killed, _ := first["killed"].(bool); !killed {
		t.Fatalf("the first kill did not report a kill: %+v", first)
	}
	if running, _ := first["running"].(bool); running {
		t.Fatalf("the process is still running after its kill: %+v", first)
	}
	if got, _ := first["command"].(string); got != "sleep 30" {
		t.Fatalf("a kill card does not name the command it ended: %+v", first)
	}
	if runtime.GOOS != "windows" {
		if got, _ := first["exit_signal"].(float64); int(got) != 9 {
			t.Fatalf("a kill does not record the signal that ended the process: %+v", first)
		}
	}
	second, err := callProcess(t, tl, ctx, ProcessInput{Action: "kill", ID: id, Reason: "test cleanup"})
	if err != nil {
		t.Fatalf("killing an exited process must succeed, got %v", err)
	}
	if running, _ := second["running"].(bool); running {
		t.Fatalf("the process came back after its second kill: %+v", second)
	}
}

// A kill without a reason is refused: the card has to be able to say why.
func TestProcessKillRequiresAReason(t *testing.T) {
	_, tl, _, projectDir := newProcessToolForTest(t)
	ctx := processTestContext(projectDir)
	started, err := callProcess(t, tl, ctx, ProcessInput{Action: "start", Command: "sleep 30", WorkDir: projectDir})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	id := processTestID(t, started)
	if _, err := callProcess(t, tl, ctx, ProcessInput{Action: "kill", ID: id}); err == nil || !strings.Contains(err.Error(), "reason is required for kill") {
		t.Fatalf("a kill without a reason reported %v, want the reason error", err)
	}
	if _, err := callProcess(t, tl, ctx, ProcessInput{Action: "kill", ID: id, Reason: "test cleanup"}); err != nil {
		t.Fatalf("a kill with a reason failed: %v", err)
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

	if _, err := callProcess(t, tl, ctx, ProcessInput{Action: "kill", ID: sleepID, Reason: "test cleanup"}); err != nil {
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
	if _, err := callProcess(t, tl, ctx, ProcessInput{Action: "kill", ID: id, Reason: "test cleanup"}); err != nil {
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
	defer func() {
		_, _ = callProcess(t, tl, ctx, ProcessInput{Action: "kill", ID: rec.id, Reason: "test cleanup"})
	}()
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
	if err := writeProcessLedgerRecord(processStateDir(rt.StateRoot()), processLedgerRecord{
		ID: id, PID: pid, StartToken: token, Command: command, SessionID: "sess-gone", HarnessPID: os.Getpid() + 1,
	}); err != nil {
		t.Fatalf("write the orphan record: %v", err)
	}
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
	defer func() {
		_, _ = callProcess(t, tl, ctx, ProcessInput{Action: "kill", ID: rec.id, Reason: "test cleanup"})
	}()
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

// 12. A ledger that cannot be read must never stop a launch. One bad record now
// costs only its own record: the files are separate, so a torn one is skipped
// and its neighbours are still judged.
func TestReapOrphanProcessesSurvivesAMalformedLedger(t *testing.T) {
	stateRoot := t.TempDir()
	dir := processStateDir(stateRoot)
	ledgerDir := processLedgerDir(dir)
	if err := os.MkdirAll(ledgerDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// A torn record names no process anyone can kill; a file that is not
	// "<digits>.json" is not a record at all.
	if err := os.WriteFile(filepath.Join(ledgerDir, "7.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ledgerDir, "notes.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The record beside them is still judged: this PID is alive but its start
	// time does not match, so it is dropped without a kill.
	if err := writeProcessLedgerRecord(dir, processLedgerRecord{
		ID: 8, PID: os.Getpid(), StartToken: "not-this-process", Command: "sleep 30", HarnessPID: 0,
	}); err != nil {
		t.Fatalf("write the stale record: %v", err)
	}
	if killed := ReapOrphanProcesses(stateRoot); len(killed) != 0 {
		t.Fatalf("a malformed ledger produced kills: %v", killed)
	}
	if _, err := os.Stat(filepath.Join(ledgerDir, "8.json")); !os.IsNotExist(err) {
		t.Fatalf("the stale record survived a reap that dropped it: %v", err)
	}
	// A missing ledger is the ordinary case on a first launch.
	if killed := ReapOrphanProcesses(t.TempDir()); len(killed) != 0 {
		t.Fatalf("a missing ledger produced kills: %v", killed)
	}
	// The pre-<version> document is dropped whole: it names nothing readable.
	legacyRoot := t.TempDir()
	legacyDir := processStateDir(legacyRoot)
	if err := os.MkdirAll(legacyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyProcessLedgerPath(legacyDir), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if killed := ReapOrphanProcesses(legacyRoot); len(killed) != 0 {
		t.Fatalf("a malformed legacy document produced kills: %v", killed)
	}
	if _, err := os.Stat(legacyProcessLedgerPath(legacyDir)); !os.IsNotExist(err) {
		t.Fatalf("the unreadable legacy document was kept: %v", err)
	}
}

// A blocking start+wait must stream the command's output into the live card:
// the owner stared at an empty output area for the whole wait.
func TestProcessStartWithWaitStreamsOutputDeltas(t *testing.T) {
	st, tl, _, projectDir := newProcessToolForTest(t)
	var deltas []StepEvent
	st.SetStepHook(func(_ context.Context, evt StepEvent) {
		if evt.Kind == StepKindToolOutputDelta {
			deltas = append(deltas, evt)
		}
	})
	ctx := WithToolStepID(processTestContext(projectDir), "process-step-1")
	started, err := callProcess(t, tl, ctx, ProcessInput{
		Action: "start", Command: "echo live-process-output", WorkDir: projectDir,
		Wait: "log", Pattern: "NEVER-READY-MARK", TimeoutMs: 5000,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if len(deltas) == 0 {
		t.Fatal("a blocking start+wait emitted no output deltas")
	}
	var combined strings.Builder
	for _, evt := range deltas {
		if evt.StepID != "process-step-1" {
			t.Fatalf("delta StepID = %q, want process-step-1", evt.StepID)
		}
		if evt.ToolName != "process" {
			t.Fatalf("delta ToolName = %q, want process", evt.ToolName)
		}
		chunk, _ := evt.Output["chunk"].(string)
		combined.WriteString(chunk)
	}
	if !strings.Contains(combined.String(), "live-process-output") {
		t.Fatalf("deltas never carried the command's output: %q", combined.String())
	}
	// Await the exit watcher's ledger write so it cannot land during t.TempDir's
	// RemoveAll (the flake waitForProcessSettled's comment documents); the echo
	// already exited via the readiness wait's exit branch.
	waitForProcessSettled(t, processRecordForTest(t, st, processTestID(t, started)))
}

// A blocking wait on a process an earlier call started must stream what the
// process appends to its log while the wait polls.
func TestProcessWaitStreamsLogDeltasWhilePolling(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the ticking command is a POSIX shell loop")
	}
	st, tl, _, projectDir := newProcessToolForTest(t)
	ctx := processTestContext(projectDir)
	started, err := callProcess(t, tl, ctx, ProcessInput{
		Action: "start", WorkDir: projectDir,
		Command: `sh -c 'i=0; while [ $i -lt 20 ]; do echo tick-$i; i=$((i+1)); sleep 0.2; done'`,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	id := processTestID(t, started)
	var deltas []StepEvent
	st.SetStepHook(func(_ context.Context, evt StepEvent) {
		if evt.Kind == StepKindToolOutputDelta {
			deltas = append(deltas, evt)
		}
	})
	waitCtx := WithToolStepID(ctx, "process-step-2")
	if _, err := callProcess(t, tl, waitCtx, ProcessInput{
		Action: "wait", ID: id, Wait: "tcp", Address: "127.0.0.1:1", TimeoutMs: 3000,
	}); err != nil {
		t.Fatalf("wait: %v", err)
	}
	var combined strings.Builder
	for _, evt := range deltas {
		if evt.StepID != "process-step-2" {
			t.Fatalf("delta StepID = %q, want process-step-2", evt.StepID)
		}
		chunk, _ := evt.Output["chunk"].(string)
		combined.WriteString(chunk)
	}
	if !strings.Contains(combined.String(), "tick-") {
		t.Fatalf("the wait streamed none of the log it tailed: %q", combined.String())
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

// TestProcessIDsAndLogsAreUniqueAcrossRunners pins the ledger's primary key:
// several runners of one state root — the real product, where conversations
// share one runner environment — never hand out the same id, never share a log,
// and never overwrite each other's ledger record (owner 2026-11-24).
func TestProcessIDsAndLogsAreUniqueAcrossRunners(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	projectDir := filepath.Join(root, "project")
	for _, dir := range []string{workspace, projectDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	newRunner := func() *llm.Tool {
		rt := &AgentToolRuntime{Home: root, WorkspaceRoot: workspace, Cfg: &appcfg.Root{SandboxMode: appcfg.SandboxModeDangerFullAccess}}
		st := NewState(projectDir)
		t.Cleanup(st.KillProcesses)
		tl, err := NewProcessTool(st, rt)
		if err != nil {
			t.Fatal(err)
		}
		return tl
	}
	start := func(tl *llm.Tool, command string) (int, string) {
		t.Helper()
		payload, err := callProcess(t, tl, processTestContext(projectDir), ProcessInput{Action: "start", Command: command, WorkDir: projectDir})
		if err != nil {
			t.Fatalf("start %q: %v", command, err)
		}
		logPath, _ := payload["log"].(string)
		return processTestID(t, payload), logPath
	}

	firstID, firstLog := start(newRunner(), "sleep 30")
	secondID, secondLog := start(newRunner(), "sleep 30")
	if firstID == secondID {
		t.Fatalf("two runners handed out the same process id %d", firstID)
	}
	if firstLog == secondLog {
		t.Fatalf("two runners share the log file %s", firstLog)
	}
	records := readProcessLedger(filepath.Dir(firstLog))
	if len(records) != 2 {
		t.Fatalf("the ledger holds %d records for two live processes: %+v", len(records), records)
	}

	// A log left by an earlier run is never written into again, and its id is
	// never handed out again.
	stale := filepath.Join(filepath.Dir(firstLog), "7.log")
	if err := os.WriteFile(stale, []byte("an earlier run\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	staleID, staleLog := start(newRunner(), "sleep 30")
	if staleLog == stale {
		t.Fatalf("a new process reused the existing log %s", stale)
	}
	if staleID <= secondID {
		t.Fatalf("id %d was handed out without moving past the ids already in use (%d)", staleID, secondID)
	}
	if data, err := os.ReadFile(staleLog); err != nil || strings.Contains(string(data), "an earlier run") {
		t.Fatalf("the new log %s carries another run's output: %q (%v)", staleLog, data, err)
	}
}

// TestProcessIDsAreUniqueUnderConcurrentStarts pins the mechanism the allocator
// rests on: eight runners starting at the same instant on one state root still
// get eight distinct ids and eight distinct logs, because the candidate id is
// arbitrated by O_EXCL rather than by a counter (owner 2026-11-24).
func TestProcessIDsAreUniqueUnderConcurrentStarts(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	projectDir := filepath.Join(root, "project")
	for _, dir := range []string{workspace, projectDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	const runners = 8
	tools := make([]*llm.Tool, runners)
	for i := range tools {
		rt := &AgentToolRuntime{Home: root, WorkspaceRoot: workspace, Cfg: &appcfg.Root{SandboxMode: appcfg.SandboxModeDangerFullAccess}}
		st := NewState(projectDir)
		t.Cleanup(st.KillProcesses)
		tl, err := NewProcessTool(st, rt)
		if err != nil {
			t.Fatal(err)
		}
		tools[i] = tl
	}
	type outcome struct {
		id  int
		log string
		err error
	}
	results := make([]outcome, runners)
	var wg sync.WaitGroup
	for i := range tools {
		wg.Add(1)
		// No t.Fatalf inside the goroutine: it is not goroutine-safe.
		go func(i int) {
			defer wg.Done()
			raw, _ := json.Marshal(ProcessInput{Action: "start", Command: "sleep 30", WorkDir: projectDir})
			out, err := tools[i].Handle(processTestContext(projectDir), string(raw))
			if err != nil {
				results[i] = outcome{err: err}
				return
			}
			var payload map[string]any
			if err := json.Unmarshal([]byte(out.(string)), &payload); err != nil {
				results[i] = outcome{err: err}
				return
			}
			id, _ := payload["id"].(float64)
			logPath, _ := payload["log"].(string)
			results[i] = outcome{id: int(id), log: logPath}
		}(i)
	}
	wg.Wait()
	ids := make(map[int]bool, runners)
	logs := make(map[string]bool, runners)
	for i, res := range results {
		if res.err != nil {
			t.Fatalf("runner %d: %v", i, res.err)
		}
		if ids[res.id] {
			t.Fatalf("id %d was handed out twice", res.id)
		}
		if logs[res.log] {
			t.Fatalf("log %s was handed out twice", res.log)
		}
		ids[res.id], logs[res.log] = true, true
	}
	if records := readProcessLedger(filepath.Dir(results[0].log)); len(records) != runners {
		t.Fatalf("the ledger holds %d records for %d live processes", len(records), runners)
	}
}

// TestProcessLedgerRecordFileLivesWithTheProcess pins the new on-disk shape
// through the tool's own actions: a started process has its own record file, and
// killing it removes that file. (Before this change the ledger was one shared
// document, so the directory did not exist at all.)
func TestProcessLedgerRecordFileLivesWithTheProcess(t *testing.T) {
	st, tl, rt, projectDir := newProcessToolForTest(t)
	ctx := processTestContext(projectDir)
	started, err := callProcess(t, tl, ctx, ProcessInput{Action: "start", Command: "sleep 30", WorkDir: projectDir})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	id := processTestID(t, started)
	// The path is spelled out, not taken from the new processLedgerDir helper:
	// this test must compile and run on the OLD tree too, so its failure is an
	// assertion (no ledger/ directory) rather than a build error.
	recordPath := filepath.Join(processStateDir(rt.StateRoot()), "ledger", fmt.Sprintf("%d.json", id))
	if _, err := os.Stat(recordPath); err != nil {
		t.Fatalf("the started process has no record file: %v", err)
	}
	rec := processRecordForTest(t, st, id)
	if _, err := callProcess(t, tl, ctx, ProcessInput{Action: "kill", ID: id, Reason: "test cleanup"}); err != nil {
		t.Fatalf("kill: %v", err)
	}
	waitForProcessSettled(t, rec)
	if _, err := os.Stat(recordPath); !os.IsNotExist(err) {
		t.Fatalf("the killed process left its record file behind: %v", err)
	}
}

// processLedgerChildEnv points the child test below at the state directory it
// writes into; without it, that test body does nothing.
const processLedgerChildEnv = "FOREBRAIN_TEST_PROCESS_LEDGER_DIR"

// TestProcessLedgerChildRecords is the second harness process of the
// cross-harness test: its own OS process, writing its own share of the records
// through the product's writer.
func TestProcessLedgerChildRecords(t *testing.T) {
	dir := os.Getenv(processLedgerChildEnv)
	if dir == "" {
		t.Skip("driven by TestProcessLedgerKeepsEveryRecordAcrossHarnesses")
	}
	offset, err := strconv.Atoi(os.Getenv("FOREBRAIN_TEST_PROCESS_LEDGER_OFFSET"))
	if err != nil {
		t.Fatalf("offset: %v", err)
	}
	count, err := strconv.Atoi(os.Getenv("FOREBRAIN_TEST_PROCESS_LEDGER_COUNT"))
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	for i := 0; i < count; i++ {
		rec := processLedgerRecord{
			ID: offset + i, PID: 1, Command: "sleep 1", SessionID: "sess-1", HarnessPID: os.Getpid(),
		}
		if err := writeProcessLedgerRecord(dir, rec); err != nil {
			t.Fatalf("record %d: %v", rec.ID, err)
		}
	}
}

// TestProcessLedgerKeepsEveryRecordAcrossHarnesses is the lost update this
// change removes: harnesses sharing one state root write the ledger at the same
// moment, and every record must survive. With the pre-thread single document the
// last writer replaced the whole file (measured: 84 and 163 of 300 kept).
func TestProcessLedgerKeepsEveryRecordAcrossHarnesses(t *testing.T) {
	dir := processStateDir(t.TempDir())
	const harnesses = 6
	const perHarness = 50
	var wg sync.WaitGroup
	failures := make([]error, harnesses)
	for i := 0; i < harnesses; i++ {
		wg.Add(1)
		// No t.Fatalf inside the goroutine: it is not goroutine-safe.
		go func(i int) {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=^TestProcessLedgerChildRecords$", "-test.timeout=2m")
			cmd.Env = append(os.Environ(),
				processLedgerChildEnv+"="+dir,
				// Ids start at 1: id 0 names no process, so the writer drops it.
				fmt.Sprintf("FOREBRAIN_TEST_PROCESS_LEDGER_OFFSET=%d", 1+i*perHarness),
				fmt.Sprintf("FOREBRAIN_TEST_PROCESS_LEDGER_COUNT=%d", perHarness),
			)
			if out, err := cmd.CombinedOutput(); err != nil {
				failures[i] = fmt.Errorf("harness %d: %v: %s", i, err, out)
			}
		}(i)
	}
	wg.Wait()
	for _, err := range failures {
		if err != nil {
			t.Fatalf("%v", err)
		}
	}
	want := harnesses * perHarness
	if records := readProcessLedger(dir); len(records) != want {
		t.Fatalf("the ledger holds %d records, want %d: lost %d", len(records), want, want-len(records))
	}
}

// legacyProcessLedgerDocument writes the ledger.json an older version left, and
// returns its path.
func legacyProcessLedgerDocument(t *testing.T, dir string, records ...processLedgerRecord) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(processLedgerFile{Processes: records})
	if err != nil {
		t.Fatal(err)
	}
	path := legacyProcessLedgerPath(dir)
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestMigrateLegacyProcessLedgerKeepsEveryRecord covers the upgrade window: the
// one-document ledger becomes one file per process, and the document goes only
// after every record made it.
func TestMigrateLegacyProcessLedgerKeepsEveryRecord(t *testing.T) {
	dir := processStateDir(t.TempDir())
	path := legacyProcessLedgerDocument(t, dir,
		processLedgerRecord{ID: 1, PID: 111, StartToken: "t1", Command: "sleep 1", HarnessPID: 9001},
		processLedgerRecord{ID: 2, PID: 112, StartToken: "t2", Command: "sleep 2", HarnessPID: 9002},
	)
	migrateLegacyProcessLedger(dir)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the legacy document survived a complete migration: %v", err)
	}
	records := readProcessLedger(dir)
	if len(records) != 2 {
		t.Fatalf("the migration left %d records, want 2: %+v", len(records), records)
	}
	byID := map[int]processLedgerRecord{}
	for _, rec := range records {
		byID[rec.ID] = rec
	}
	if got := byID[2]; got.PID != 112 || got.StartToken != "t2" || got.HarnessPID != 9002 {
		t.Fatalf("migrated record 2 = %+v, want it whole", got)
	}
	// Migrating again is a no-op: the document is gone.
	migrateLegacyProcessLedger(dir)
	if records := readProcessLedger(dir); len(records) != 2 {
		t.Fatalf("a second migration changed the ledger: %+v", records)
	}
}

// TestReapOrphanProcessesMigratesTheLegacyDocument pins that the launch path
// itself migrates: the next launch reads per-record files, not the document.
func TestReapOrphanProcessesMigratesTheLegacyDocument(t *testing.T) {
	stateRoot := t.TempDir()
	dir := processStateDir(stateRoot)
	// The record names this process as its harness, so the reap leaves it be:
	// what is under test is the migration, not the kill.
	path := legacyProcessLedgerDocument(t, dir,
		processLedgerRecord{ID: 3, PID: 1, StartToken: "t3", Command: "sleep 3", HarnessPID: os.Getpid()},
	)
	if killed := ReapOrphanProcesses(stateRoot); len(killed) != 0 {
		t.Fatalf("the launch-path migration produced kills: %v", killed)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the launch path kept the legacy document: %v", err)
	}
	records := readProcessLedger(dir)
	if len(records) != 1 || records[0].ID != 3 {
		t.Fatalf("the launch path left %+v, want record 3", records)
	}
}

// TestReapOrphanProcessesSparesALiveHarness is the second half of the fix: a
// harness that is still running owns its background processes, so a new launch
// must not kill them. A record from an older version carries no harness start
// time; a live harness PID is all the evidence it can give, and it is enough.
func TestReapOrphanProcessesSparesALiveHarness(t *testing.T) {
	for _, tc := range []struct {
		name        string
		harnessTime bool
	}{
		{name: "record names the live harness", harnessTime: true},
		{name: "legacy record has no harness start time", harnessTime: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
				t.Skip("this platform reports no process start time, so there is no reap to test")
			}
			// A live process standing in for the other harness.
			other := exec.Command("sleep", "30")
			if err := other.Start(); err != nil {
				t.Fatalf("start the stand-in harness: %v", err)
			}
			t.Cleanup(func() {
				_ = other.Process.Kill()
				_, _ = other.Process.Wait()
			})
			harnessToken, ok := safety.ProcessStartToken(other.Process.Pid)
			if !ok {
				t.Skip("this platform reports no process start time, so there is no reap to test")
			}
			written := processLedgerRecord{ID: rec.id, PID: pid, StartToken: token, Command: "sleep 30", SessionID: "sess-1", HarnessPID: other.Process.Pid}
			if tc.harnessTime {
				written.HarnessStartToken = harnessToken
			}
			if err := writeProcessLedgerRecord(processStateDir(rt.StateRoot()), written); err != nil {
				t.Fatalf("write the record: %v", err)
			}

			if killed := ReapOrphanProcesses(rt.StateRoot()); len(killed) != 0 {
				t.Fatalf("a live harness's process was killed: %v", killed)
			}
			if rec.handle.Exited() {
				t.Fatal("the live harness's process was ended by the reap")
			}
			recordPath := filepath.Join(processLedgerDir(processStateDir(rt.StateRoot())), fmt.Sprintf("%d.json", rec.id))
			if _, err := os.Stat(recordPath); err != nil {
				t.Fatalf("the live harness's record was dropped: %v", err)
			}
		})
	}
}
