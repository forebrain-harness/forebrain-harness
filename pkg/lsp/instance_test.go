package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestMain removes the shared fake-server build directory once every test in
// this package (including the task 07–09 files that reuse the helpers
// below) has run.
func TestMain(m *testing.M) {
	code := m.Run()
	if fakeServerDir != "" {
		_ = os.RemoveAll(fakeServerDir)
	}
	os.Exit(code)
}

var (
	fakeServerOnce sync.Once
	fakeServerDir  string
	fakeServerExe  string
	fakeServerErr  error
	fakeServerOut  string
)

// buildFakeServer compiles fakeServerSource once per test binary, the same
// way pkg/mcp's bridge tests build their fixture.
func buildFakeServer(t *testing.T) string {
	t.Helper()
	fakeServerOnce.Do(func() {
		dir, err := os.MkdirTemp("", "lsp-fake-server-")
		if err != nil {
			fakeServerErr = err
			return
		}
		fakeServerDir = dir
		mainPath := filepath.Join(dir, "main.go")
		if err := os.WriteFile(mainPath, []byte(fakeServerSource), 0o600); err != nil {
			fakeServerErr = err
			return
		}
		exe := filepath.Join(dir, "fake-lsp-server")
		if runtime.GOOS == "windows" {
			exe += ".exe"
		}
		root, err := repoRoot()
		if err != nil {
			fakeServerErr = err
			return
		}
		cmd := exec.Command("go", "build", "-tags", "fts5", "-o", exe, mainPath)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "CGO_ENABLED=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			fakeServerErr = err
			fakeServerOut = string(out)
			return
		}
		fakeServerExe = exe
	})
	if fakeServerErr != nil {
		t.Fatalf("build fake server: %v\n%s", fakeServerErr, fakeServerOut)
	}
	return fakeServerExe
}

// repoRoot walks up from the working directory to the module root.
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found")
		}
		dir = parent
	}
}

// fakeSpec builds the InstanceSpec for one fake-server run, injecting the
// record path into the script.
func fakeSpec(t *testing.T, script map[string]any, mutate func(*InstanceSpec)) InstanceSpec {
	t.Helper()
	exe := buildFakeServer(t)
	record := filepath.Join(t.TempDir(), "record.jsonl")
	if script == nil {
		script = map[string]any{}
	}
	script["record"] = record
	blob, err := json.Marshal(script)
	if err != nil {
		t.Fatalf("marshal script: %v", err)
	}
	spec := InstanceSpec{
		ServerID:       "fake",
		Command:        exe,
		Env:            append(os.Environ(), "FAKE_LSP_SCRIPT="+string(blob)),
		Root:           t.TempDir(),
		StartupTimeout: 5 * time.Second,
		LogPath:        filepath.Join(t.TempDir(), "server.log"),
	}
	if mutate != nil {
		mutate(&spec)
	}
	return spec
}

// startFake starts one fake server and shuts it down at test end. The
// tests of tasks 07–09 build on it.
func startFake(t *testing.T, script map[string]any, mutate func(*InstanceSpec)) (*Instance, string) {
	t.Helper()
	spec := fakeSpec(t, script, mutate)
	inst, err := StartInstance(context.Background(), spec)
	if err != nil {
		t.Fatalf("StartInstance: %v", err)
	}
	t.Cleanup(func() { _ = inst.Shutdown(context.Background()) })
	return inst, script["record"].(string)
}

// readRecord reads the fake server's record file, one JSON object per line.
// A file that does not exist yet is no records.
func readRecord(t *testing.T, path string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read record: %v", err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("record line %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

// pollRecord waits until cond holds on the record file.
func pollRecord(t *testing.T, path string, cond func([]map[string]any) bool, what string) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		recs := readRecord(t, path)
		if cond(recs) {
			return recs
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s in %s", what, path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// findRecv returns the params of the first recorded message with method.
func findRecv(recs []map[string]any, method string) map[string]any {
	for _, rec := range recs {
		recv, ok := rec["recv"].(map[string]any)
		if !ok || recv["method"] != method {
			continue
		}
		params, _ := recv["params"].(map[string]any)
		return params
	}
	return nil
}

// findResp returns the recorded reply the client gave to the server request
// with method.
func findResp(recs []map[string]any, method string) map[string]any {
	for _, rec := range recs {
		resp, ok := rec["resp"].(map[string]any)
		if !ok || resp["method"] != method {
			continue
		}
		return resp
	}
	return nil
}

// recordInt returns a top-level integer field of a record line (pid,
// child_pid).
func recordInt(recs []map[string]any, key string) (int, bool) {
	for _, rec := range recs {
		v, ok := rec[key].(float64)
		if !ok {
			continue
		}
		return int(v), true
	}
	return 0, false
}

// waitForProcessGone waits until the pid is neither running nor a zombie,
// the same way pkg/mcp's bridge tests assert a reaped child.
func waitForProcessGone(t *testing.T, pid int) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("process state is asserted through ps, which Windows does not have")
	}
	deadline := time.Now().Add(20 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		state := processState(pid)
		last = state
		if state == "" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process %d is still %s", pid, last)
}

func processState(pid int) string {
	out, err := exec.Command("ps", "-o", "state=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func TestBuildEnvPassthroughAndSecrets(t *testing.T) {
	t.Setenv("GOPATH", "/x")
	t.Setenv("MY_API_TOKEN", "s")
	env, missing := BuildEnv(EnvSpec{
		Passthrough: []string{"GOPATH", "MY_API_TOKEN"},
		Env:         map[string]string{"A": "${GOPATH}/bin", "B": "${NOPE}"},
	})
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "GOPATH=/x") {
		t.Errorf("env missing GOPATH=/x:\n%s", joined)
	}
	if strings.Contains(joined, "MY_API_TOKEN") {
		t.Errorf("secret-looking variable leaked:\n%s", joined)
	}
	if !strings.Contains(joined, "A=/x/bin") {
		t.Errorf("env missing A=/x/bin:\n%s", joined)
	}
	found := false
	for _, m := range missing {
		if m == "NOPE" {
			found = true
		}
	}
	if !found {
		t.Errorf("missing = %v, want NOPE", missing)
	}
}

func TestBuildEnvProjectScopedReadsOnlyDotEnv(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".env"), []byte("FOO=file\n"), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	t.Setenv("FOO", "proc")
	env, missing := BuildEnv(EnvSpec{
		FromProject: true,
		Home:        home,
		Env:         map[string]string{"X": "${FOO}"},
	})
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "X=file") {
		t.Errorf("env missing X=file:\n%s", joined)
	}
	if strings.Contains(joined, "X=proc") {
		t.Errorf("project entry resolved from the process environment:\n%s", joined)
	}
	if len(missing) != 0 {
		t.Errorf("missing = %v, want empty", missing)
	}
}

func TestExpandCacheDirJSON(t *testing.T) {
	in := json.RawMessage(`{"storagePath": "${LSP_CACHE_DIR}", "nested": {"list": ["${LSP_CACHE_DIR}/global"]}}`)
	out := ExpandCacheDirJSON(in, "/cache/x")
	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc["storagePath"] != "/cache/x" {
		t.Errorf("storagePath = %v", doc["storagePath"])
	}
	nested, _ := doc["nested"].(map[string]any)
	list, _ := nested["list"].([]any)
	if len(list) != 1 || list[0] != "/cache/x/global" {
		t.Errorf("nested list = %v", nested["list"])
	}
	bad := json.RawMessage(`{nope`)
	if got := ExpandCacheDirJSON(bad, "/cache/x"); string(got) != string(bad) {
		t.Errorf("invalid input changed: %s", got)
	}
}

func TestClientCapabilitiesMatchSpec(t *testing.T) {
	if !json.Valid([]byte(clientCapabilitiesJSON)) {
		t.Fatalf("clientCapabilitiesJSON is not valid JSON")
	}
	var caps map[string]any
	if err := json.Unmarshal([]byte(clientCapabilitiesJSON), &caps); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	workspace, _ := caps["workspace"].(map[string]any)
	if workspace["applyEdit"] != false {
		t.Errorf("workspace.applyEdit = %v, want false", workspace["applyEdit"])
	}
	general, _ := caps["general"].(map[string]any)
	encodings, _ := general["positionEncodings"].([]any)
	if len(encodings) != 2 || encodings[0] != "utf-8" || encodings[1] != "utf-16" {
		t.Errorf("general.positionEncodings = %v, want [utf-8 utf-16]", encodings)
	}
}

func TestStartInstanceHandshake(t *testing.T) {
	inst, record := startFake(t, map[string]any{
		"capabilities": map[string]any{"positionEncoding": "utf-8", "definitionProvider": true},
	}, nil)
	if inst.Encoding() != EncodingUTF8 {
		t.Errorf("Encoding() = %q, want utf-8", inst.Encoding())
	}
	if !inst.Capabilities().Supports("definitionProvider") {
		t.Errorf("definitionProvider not reported as supported")
	}
	recs := pollRecord(t, record, func(recs []map[string]any) bool {
		return findRecv(recs, "initialized") != nil
	}, "initialize and initialized")
	params := findRecv(recs, "initialize")
	if params == nil {
		t.Fatalf("no initialize request recorded")
	}
	if pid, ok := params["processId"].(float64); !ok || int(pid) != os.Getpid() {
		t.Errorf("processId = %v, want %d", params["processId"], os.Getpid())
	}
	rootURI, _ := params["rootUri"].(string)
	if !strings.HasPrefix(rootURI, "file://") {
		t.Errorf("rootUri = %q, want a file:// URI", rootURI)
	}
}

func TestStartInstanceMissingCommand(t *testing.T) {
	_, err := StartInstance(context.Background(), InstanceSpec{
		ServerID: "fake",
		Command:  "definitely-not-a-server",
		Env:      os.Environ(),
		Root:     t.TempDir(),
	})
	if err == nil {
		t.Fatal("StartInstance succeeded for a missing command")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want it to contain %q", err, "not found")
	}
}

func TestStartInstanceTimeout(t *testing.T) {
	script := map[string]any{"never_initialize": true}
	spec := fakeSpec(t, script, func(s *InstanceSpec) {
		s.StartupTimeout = 300 * time.Millisecond
	})
	started := time.Now()
	_, err := StartInstance(context.Background(), spec)
	if err == nil {
		t.Fatal("StartInstance succeeded although initialize never completed")
	}
	if !strings.Contains(err.Error(), "initialize did not complete") {
		t.Errorf("error = %q", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Errorf("StartInstance took %s, want it to give up within 1s", elapsed)
	}
	recs := pollRecord(t, script["record"].(string), func(recs []map[string]any) bool {
		_, ok := recordInt(recs, "pid")
		return ok
	}, "the server pid")
	if pid, ok := recordInt(recs, "pid"); ok {
		waitForProcessGone(t, pid)
	}
}

func TestServerRequestsAnswered(t *testing.T) {
	inst, record := startFake(t, map[string]any{
		"after_initialized": []map[string]any{
			{"request": "workspace/configuration", "params": map[string]any{
				"items": []map[string]any{{"section": "gopls"}, {"section": "nope"}},
			}},
			{"request": "window/showMessageRequest", "params": map[string]any{
				"type":    3,
				"message": "Import build?",
				"actions": []map[string]any{{"title": "Import build"}, {"title": "Not now"}},
			}},
			{"request": "workspace/applyEdit", "params": map[string]any{"edit": map[string]any{}}},
			{"request": "window/showDocument", "params": map[string]any{"uri": "file:///x"}},
			{"request": "fake/unknown", "params": map[string]any{}},
		},
	}, func(s *InstanceSpec) {
		s.Settings = json.RawMessage(`{"gopls": {"hoverKind": "Full"}}`)
		s.AutoAnswers = map[string]string{"Import build": "Import build"}
	})
	if err := inst.WaitReady(context.Background()); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
	recs := pollRecord(t, record, func(recs []map[string]any) bool {
		return findResp(recs, "fake/unknown") != nil
	}, "the five server requests")

	config, _ := findResp(recs, "workspace/configuration")["result"].([]any)
	if len(config) != 2 {
		t.Fatalf("configuration result = %v", findResp(recs, "workspace/configuration")["result"])
	}
	if section := config[0].(map[string]any)["hoverKind"]; section != "Full" {
		t.Errorf("gopls section = %v", config[0])
	}
	if config[1] != nil {
		t.Errorf("nope section = %v, want null", config[1])
	}
	message, _ := findResp(recs, "window/showMessageRequest")["result"].(map[string]any)
	if message["title"] != "Import build" {
		t.Errorf("showMessageRequest result = %v", message)
	}
	apply, _ := findResp(recs, "workspace/applyEdit")["result"].(map[string]any)
	if apply["applied"] != false || apply["failureReason"] != "read-only client" {
		t.Errorf("applyEdit result = %v", apply)
	}
	showDoc, _ := findResp(recs, "window/showDocument")["result"].(map[string]any)
	if showDoc["success"] != false {
		t.Errorf("showDocument result = %v", showDoc)
	}
	unknown, _ := findResp(recs, "fake/unknown")["error"].(map[string]any)
	if code, _ := unknown["code"].(float64); int(code) != -32601 {
		t.Errorf("fake/unknown error = %v, want code -32601", unknown)
	}
}

func TestPythonPathFilled(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fixture builds a Unix .venv layout; Windows uses Scripts\\python.exe")
	}
	root := t.TempDir()
	bin := filepath.Join(root, ".venv", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	python := filepath.Join(bin, "python")
	if err := os.WriteFile(python, nil, 0o755); err != nil {
		t.Fatalf("write python: %v", err)
	}
	_, record := startFake(t, map[string]any{
		"after_initialized": []map[string]any{
			{"request": "workspace/configuration", "params": map[string]any{
				"items": []map[string]any{{"section": "python"}},
			}},
		},
	}, func(s *InstanceSpec) {
		s.Root = root
		s.Settings = json.RawMessage(`{"python": {}}`)
	})
	recs := pollRecord(t, record, func(recs []map[string]any) bool {
		return findResp(recs, "workspace/configuration") != nil
	}, "the python configuration reply")
	section, _ := findResp(recs, "workspace/configuration")["result"].([]any)[0].(map[string]any)
	if section["pythonPath"] != python {
		t.Errorf("pythonPath = %v, want %s", section["pythonPath"], python)
	}
}

func TestReadinessProgress(t *testing.T) {
	previous := quietPeriod
	quietPeriod = 50 * time.Millisecond
	t.Cleanup(func() { quietPeriod = previous })

	inst, _ := startFake(t, map[string]any{
		"after_initialized": []map[string]any{
			{"notify": "$/progress", "params": map[string]any{
				"token": 1,
				"value": map[string]any{"kind": "begin", "percentage": 50},
			}},
			{"notify": "$/progress", "delay_ms": 200, "params": map[string]any{
				"token": 1,
				"value": map[string]any{"kind": "end"},
			}},
		},
	}, func(s *InstanceSpec) { s.Readiness = ReadinessProgress })

	deadline := time.Now().Add(5 * time.Second)
	for inst.Progress() != 50 {
		if time.Now().After(deadline) {
			t.Fatalf("progress stuck at %d, want 50", inst.Progress())
		}
		time.Sleep(10 * time.Millisecond)
	}
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := inst.WaitReady(ctx); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
	if elapsed := time.Since(started); elapsed < 150*time.Millisecond {
		t.Errorf("ready after %s, before the progress ended", elapsed)
	}
}

func TestReadinessStatusNotifications(t *testing.T) {
	t.Run("rust-analyzer quiescent", func(t *testing.T) {
		inst, _ := startFake(t, map[string]any{
			"after_initialized": []map[string]any{
				{"notify": "experimental/serverStatus", "params": map[string]any{
					"health": "ok", "quiescent": true,
				}},
			},
		}, func(s *InstanceSpec) { s.Readiness = ReadinessRustAnalyzerStatus })
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := inst.WaitReady(ctx); err != nil {
			t.Fatalf("WaitReady: %v", err)
		}
	})
	t.Run("jdtls ServiceReady", func(t *testing.T) {
		inst, _ := startFake(t, map[string]any{
			"after_initialized": []map[string]any{
				{"notify": "language/status", "params": map[string]any{
					"type": "ServiceReady", "message": "ready",
				}},
			},
		}, func(s *InstanceSpec) { s.Readiness = ReadinessJDTLSStatus })
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := inst.WaitReady(ctx); err != nil {
			t.Fatalf("WaitReady: %v", err)
		}
	})
}

func TestCrashRestart(t *testing.T) {
	previous := restartBackoff
	restartBackoff = []time.Duration{10 * time.Millisecond}
	t.Cleanup(func() { restartBackoff = previous })

	restarted := make(chan struct{}, 4)
	inst, _ := startFake(t, map[string]any{
		"crash_after_requests": 1,
		"state_dir":            t.TempDir(),
	}, func(s *InstanceSpec) {
		s.RestartOnCrash = true
		s.MaxRestarts = 1
		s.OnRestart = func(ctx context.Context) { restarted <- struct{}{} }
	})

	var out map[string]any
	if err := inst.Call(context.Background(), "fake/env", nil, &out); err == nil || !strings.Contains(err.Error(), "restarted") {
		t.Fatalf("first call error = %v, want it to mention restarted", err)
	}
	select {
	case <-restarted:
	case <-time.After(5 * time.Second):
		t.Fatal("OnRestart was not called")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := inst.WaitReady(ctx); err != nil {
		t.Fatalf("WaitReady after restart: %v", err)
	}
	if inst.State() != StateReady {
		t.Errorf("state = %s after restart, want ready", inst.State())
	}

	// The crash budget allows exactly one restart: the second crash fails
	// the instance for good.
	if err := inst.Call(context.Background(), "fake/env", nil, &out); err == nil || !strings.Contains(err.Error(), "stopped") {
		t.Fatalf("second call error = %v, want it to mention stopped", err)
	}
	if inst.State() != StateFailed {
		t.Errorf("state = %s, want failed", inst.State())
	}
	if msg := inst.LastError(); !strings.Contains(msg, "crashed") {
		t.Errorf("LastError = %q, want it to mention crashed", msg)
	}
	select {
	case <-inst.Done():
	default:
		t.Error("Done is not closed after the final crash")
	}
}

func TestShutdownKillsTree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fixture spawns sleep 600, which Windows does not have")
	}
	pidFile := filepath.Join(t.TempDir(), "pids.json")
	inst, record := startFake(t, map[string]any{"spawn_child": true}, func(s *InstanceSpec) {
		s.PIDFile = pidFile
	})
	recs := pollRecord(t, record, func(recs []map[string]any) bool {
		_, ok := recordInt(recs, "child_pid")
		return ok
	}, "the child pid")
	server, _ := recordInt(recs, "pid")
	child, _ := recordInt(recs, "child_pid")

	if err := inst.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	waitForProcessGone(t, server)
	waitForProcessGone(t, child)

	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read pids.json: %v", err)
	}
	var recsAfter []pidRecord
	if err := json.Unmarshal(b, &recsAfter); err != nil {
		t.Fatalf("pids.json = %s: %v", b, err)
	}
	if len(recsAfter) != 0 {
		t.Errorf("pids.json still records %d processes", len(recsAfter))
	}
}

// recordedPIDs returns every pid the fake servers wrote to the record file,
// in order (one per generation that started).
func recordedPIDs(t *testing.T, record string) []int {
	t.Helper()
	var pids []int
	for _, rec := range readRecord(t, record) {
		if v, ok := rec["pid"].(float64); ok {
			pids = append(pids, int(v))
		}
	}
	return pids
}

// TestShutdownDuringRestartReturns covers the deadlock where Shutdown
// arrived while a Restart was closing the old process: Shutdown used to
// wait on Done, which a successful Restart never closes, and blocked
// forever. It must instead wait for the stop phase to clear, take over, and
// leave the instance stopped with no process left.
func TestShutdownDuringRestartReturns(t *testing.T) {
	inst, record := startFake(t, map[string]any{"hold_exit_ms": 2000}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := inst.WaitReady(ctx); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
	restartErr := make(chan error, 1)
	go func() { restartErr <- inst.Restart(context.Background()) }()
	// Catch the restart inside its stop phase: the old process holds the
	// exit notification, so stopping stays set for about a second.
	deadline := time.Now().Add(5 * time.Second)
	for inst.State() != StateStarting {
		if time.Now().After(deadline) {
			t.Fatal("Restart never entered its stop phase")
		}
		time.Sleep(5 * time.Millisecond)
	}
	shutdownErr := make(chan error, 1)
	go func() { shutdownErr <- inst.Shutdown(context.Background()) }()
	select {
	case err := <-shutdownErr:
		if err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Shutdown did not return while a Restart was in flight")
	}
	select {
	case <-restartErr: // Any outcome fits; it must simply finish.
	case <-time.After(15 * time.Second):
		t.Fatal("Restart did not return after Shutdown took over")
	}
	select {
	case <-inst.Done():
	default:
		t.Error("Done is not closed")
	}
	if s := inst.State(); s != StateStopped {
		t.Errorf("state = %s, want stopped", s)
	}
	if runtime.GOOS != "windows" {
		for _, pid := range recordedPIDs(t, record) {
			waitForProcessGone(t, pid)
		}
	}
}

// TestShutdownDuringCrashRestartStaysStopped covers the shutdown-vs-restart
// races: a crash restart that is still initializing when Shutdown wins must
// not rewind the terminal state, resolve its stuck callers with a stopped
// error, and leave no process behind.
func TestShutdownDuringCrashRestartStaysStopped(t *testing.T) {
	previous := restartBackoff
	restartBackoff = []time.Duration{50 * time.Millisecond}
	t.Cleanup(func() { restartBackoff = previous })

	inst, record := startFake(t, map[string]any{
		"crash_after_requests": 1,
		"state_dir":            t.TempDir(),
		"init_delay_ms":        3000,
	}, func(s *InstanceSpec) {
		s.RestartOnCrash = true
		s.MaxRestarts = 3
	})
	callErr := make(chan error, 1)
	go func() {
		var out map[string]any
		callErr <- inst.Call(context.Background(), "fake/env", nil, &out)
	}()
	// Wait until the crash restart started its new process and is sitting in
	// initialize (init_delay_ms), then shut into the middle of it.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if pids := recordedPIDs(t, record); len(pids) >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the crash restart never started a new process (pids: %v)", recordedPIDs(t, record))
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := inst.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	select {
	case err := <-callErr:
		if err == nil || !strings.Contains(err.Error(), "stopped") {
			t.Fatalf("in-flight call error = %v, want it to mention stopped", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the in-flight call never resolved after shutdown")
	}
	if s := inst.State(); s != StateStopped {
		t.Errorf("state = %s after shutdown during a crash restart, want stopped", s)
	}
	select {
	case <-inst.Done():
	default:
		t.Error("Done is not closed")
	}
	if runtime.GOOS != "windows" {
		for _, pid := range recordedPIDs(t, record) {
			waitForProcessGone(t, pid)
		}
	}
}

// TestLaunchRefusesToInstallGenerationDuringStop covers the revival race:
// when a Shutdown wins while a launch is between its abort check and the
// generation hand-off, the launch must stop its own process instead of
// installing a generation nothing will ever clean up.
func TestLaunchRefusesToInstallGenerationDuringStop(t *testing.T) {
	inst, record := startFake(t, map[string]any{}, nil)
	// Simulate that Shutdown state: stopping set, the previous generation
	// already taken by the stopper.
	inst.mu.Lock()
	oldGen := inst.gen
	inst.stopping = true
	inst.gen = nil
	inst.pid = 0
	inst.mu.Unlock()
	t.Cleanup(func() {
		// Hand the live original generation back so the shared shutdown
		// cleanup can stop it.
		inst.mu.Lock()
		inst.stopping = false
		if inst.gen == nil {
			inst.gen = oldGen
			inst.pid = oldGen.cmd.Process.Pid
		}
		inst.mu.Unlock()
	})

	err := inst.launchAndInitialize(context.Background())
	if err == nil || !strings.Contains(err.Error(), "stopped") {
		t.Fatalf("launchAndInitialize error = %v, want it to mention stopped", err)
	}
	inst.mu.Lock()
	gen, pid := inst.gen, inst.pid
	inst.mu.Unlock()
	if gen != nil {
		t.Error("a generation was installed despite the stop in progress")
	}
	if pid != 0 {
		t.Errorf("pid = %d after the refused launch, want 0", pid)
	}
	if runtime.GOOS != "windows" {
		pids := recordedPIDs(t, record)
		if len(pids) != 2 {
			t.Fatalf("recorded %d pids, want 2 (the original and the refused launch)", len(pids))
		}
		waitForProcessGone(t, pids[1])
		if processState(pids[0]) == "" {
			t.Error("the original server process did not survive the refused launch")
		}
	}
}

// TestSetStateFrozenAfterDone pins the single terminal authority: once Done
// closed, restart paths still in flight must not move the state again.
func TestSetStateFrozenAfterDone(t *testing.T) {
	inst, _ := startFake(t, map[string]any{}, nil)
	if err := inst.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	inst.setState(StateStarting)
	inst.setState(StateReady)
	inst.setState(StateFailed)
	if s := inst.State(); s != StateStopped {
		t.Errorf("state = %s after done, want stopped", s)
	}
}

func TestSweepOrphans(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("orphan sweeping relies on Unix process groups")
	}
	pidFile := filepath.Join(t.TempDir(), "pids.json")

	matched := exec.Command("sleep", "600")
	prepareCommand(matched)
	if err := matched.Start(); err != nil {
		t.Fatalf("start matched: %v", err)
	}
	t.Cleanup(func() { _ = matched.Process.Kill(); _, _ = matched.Process.Wait() })
	rec := pidRecord{
		Server:  "test",
		PID:     matched.Process.Pid,
		PGID:    matched.Process.Pid,
		Started: processStarted(matched.Process.Pid),
	}
	if rec.Started == "" {
		t.Fatal("processStarted returned nothing for a live process")
	}
	matchJSON, err := json.Marshal([]pidRecord{rec})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(pidFile, matchJSON, 0o600); err != nil {
		t.Fatalf("write pids.json: %v", err)
	}

	sweepOrphans(pidFile)
	// sweepOrphans killed the group; this test process is the parent, so it
	// reaps the corpse before asserting the pid is gone.
	_ = matched.Wait()
	waitForProcessGone(t, matched.Process.Pid)
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read pids.json: %v", err)
	}
	if strings.TrimSpace(string(b)) != "[]" {
		t.Errorf("pids.json = %s, want []", b)
	}

	// A record whose start time does not match must be dropped, not killed.
	mismatched := exec.Command("sleep", "600")
	prepareCommand(mismatched)
	if err := mismatched.Start(); err != nil {
		t.Fatalf("start mismatched: %v", err)
	}
	t.Cleanup(func() { _ = mismatched.Process.Kill(); _, _ = mismatched.Process.Wait() })
	stale := []pidRecord{{
		Server:  "test",
		PID:     mismatched.Process.Pid,
		PGID:    mismatched.Process.Pid,
		Started: "Mon Jan  1 00:00:00 2001",
	}}
	staleJSON, err := json.Marshal(stale)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(pidFile, staleJSON, 0o600); err != nil {
		t.Fatalf("write pids.json: %v", err)
	}
	sweepOrphans(pidFile)
	if processState(mismatched.Process.Pid) == "" {
		t.Error("mismatched record was killed anyway")
	}
}

func TestStderrGoesToLog(t *testing.T) {
	var logPath string
	_, _ = startFake(t, map[string]any{"stderr": "boom"}, func(s *InstanceSpec) {
		logPath = s.LogPath
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		b, err := os.ReadFile(logPath)
		if err == nil && strings.Contains(string(b), "boom") {
			return
		}
		if time.Now().After(deadline) {
			content, _ := os.ReadFile(logPath)
			t.Fatalf("log never mentioned boom; content:\n%s", content)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestAddFolder(t *testing.T) {
	inst, record := startFake(t, map[string]any{}, nil)
	dir := t.TempDir()
	if err := inst.AddFolder(context.Background(), dir); err != nil {
		t.Fatalf("AddFolder: %v", err)
	}
	recs := pollRecord(t, record, func(recs []map[string]any) bool {
		return findRecv(recs, "workspace/didChangeWorkspaceFolders") != nil
	}, "workspace/didChangeWorkspaceFolders")
	params := findRecv(recs, "workspace/didChangeWorkspaceFolders")
	event, _ := params["event"].(map[string]any)
	added, _ := event["added"].([]any)
	if len(added) != 1 {
		t.Fatalf("added = %v", event["added"])
	}
	folder, _ := added[0].(map[string]any)
	if folder["uri"] != PathToURI(dir) {
		t.Errorf("added uri = %v, want %s", folder["uri"], PathToURI(dir))
	}
	if err := inst.AddFolder(context.Background(), dir); err != nil {
		t.Fatalf("AddFolder twice: %v", err)
	}
	if got := inst.Folders(); !containsString(got, PathToURI(dir)) {
		t.Errorf("Folders() = %v, missing %s", got, PathToURI(dir))
	}
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func TestRegistrations(t *testing.T) {
	inst, _ := startFake(t, map[string]any{
		"after_initialized": []map[string]any{
			{"request": "client/registerCapability", "params": map[string]any{
				"registrations": []map[string]any{{
					"id":              "w1",
					"method":          "workspace/didChangeWatchedFiles",
					"registerOptions": map[string]any{"watchers": []any{"**/*.go"}},
				}},
			}},
			{"request": "client/unregisterCapability", "delay_ms": 500, "params": map[string]any{
				"unregistrations": []map[string]any{{"id": "w1", "method": "workspace/didChangeWatchedFiles"}},
			}},
		},
	}, nil)
	deadline := time.Now().Add(5 * time.Second)
	for len(inst.Registrations("workspace/didChangeWatchedFiles")) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("registration never became live")
		}
		time.Sleep(10 * time.Millisecond)
	}
	regs := inst.Registrations("workspace/didChangeWatchedFiles")
	if len(regs) != 1 {
		t.Fatalf("Registrations = %v", regs)
	}
	var options map[string]any
	if err := json.Unmarshal(regs[0], &options); err != nil {
		t.Fatalf("registerOptions = %s: %v", regs[0], err)
	}
	if watchers, _ := options["watchers"].([]any); len(watchers) != 1 || watchers[0] != "**/*.go" {
		t.Errorf("watchers = %v", options["watchers"])
	}
	deadline = time.Now().Add(5 * time.Second)
	for len(inst.Registrations("workspace/didChangeWatchedFiles")) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("registration never went away")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// fakeServerSource is the fake language server the instance tests drive. It
// is a string constant compiled on demand, never a testdata package (spec
// §3.14). It reads its behavior from the FAKE_LSP_SCRIPT environment
// variable and appends one JSON object per line to the record file: {"pid":
// n} and {"child_pid": n} on startup, then {"recv": <message>} for every
// message it receives and {"resp": {...}} for every reply its own requests
// got. Every script field is optional.
const fakeServerSource = `package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/lsp"
)

var (
	writeMu sync.Mutex // one frame at a time on stdout
	recMu   sync.Mutex
	recFile *os.File

	pendMu  sync.Mutex
	pending = map[int64]chan map[string]json.RawMessage{}
	nextID  = int64(1)

	docsMu sync.Mutex
	docs   = map[string]map[string]any{} // uri → {text, version}
)

func main() {
	var script map[string]json.RawMessage
	if blob := os.Getenv("FAKE_LSP_SCRIPT"); blob != "" {
		if err := json.Unmarshal([]byte(blob), &script); err != nil {
			fmt.Fprintln(os.Stderr, "bad script:", err)
			os.Exit(2)
		}
	}
	if path := textField(script, "record"); path != "" {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			fmt.Fprintln(os.Stderr, "bad record file:", err)
			os.Exit(2)
		}
		recFile = f
	}
	record(map[string]any{"pid": os.Getpid()})
	if s := textField(script, "stderr"); s != "" {
		fmt.Fprintln(os.Stderr, s)
	}
	if boolField(script, "spawn_child") {
		child := exec.Command("sleep", "600")
		if err := child.Start(); err == nil {
			record(map[string]any{"child_pid": child.Process.Pid})
			go func() { _ = child.Wait() }()
		}
	}

	in := bufio.NewReader(os.Stdin)
	for {
		body, err := lsp.ReadMessage(in)
		if err != nil {
			os.Exit(0)
		}
		var msg map[string]json.RawMessage
		if err := json.Unmarshal(body, &msg); err != nil {
			continue
		}
		record(map[string]any{"recv": json.RawMessage(body)})
		method := stringOr(msg["method"])
		_, hasID := msg["id"]
		isRequest := method != "" && hasID && string(msg["id"]) != "null"
		switch {
		case isRequest:
			if crashCheck(script, method) {
				os.Exit(3)
			}
			handleRequest(script, method, msg["id"], msg["params"])
		case method != "":
			handleNotification(script, method, msg["params"])
		case hasID:
			deliverReply(msg)
		}
	}
}

// crashCheck counts a request in the shared count file and reports whether
// the fake should exit instead of answering.
func crashCheck(script map[string]json.RawMessage, method string) bool {
	dir := textField(script, "state_dir")
	limit := intField(script, "crash_after_requests")
	if dir == "" || limit <= 0 || method == "initialize" || method == "shutdown" {
		return false
	}
	count := 0
	if b, err := os.ReadFile(filepath.Join(dir, "count")); err == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
			count = n
		}
	}
	count++
	_ = os.WriteFile(filepath.Join(dir, "count"), []byte(strconv.Itoa(count)), 0o644)
	return count >= limit
}

func handleRequest(script map[string]json.RawMessage, method string, id, params json.RawMessage) {
	switch method {
	case "initialize":
		if ms := intField(script, "init_delay_ms"); ms > 0 {
			time.Sleep(time.Duration(ms) * time.Millisecond)
		}
		if boolField(script, "never_initialize") {
			return
		}
		caps := json.RawMessage("{}")
		if raw, ok := script["capabilities"]; ok && len(raw) > 0 {
			caps = raw
		}
		reply(id, map[string]any{
			"capabilities": caps,
			"serverInfo":   map[string]string{"name": "fake"},
		})
	case "shutdown":
		reply(id, nil)
	case "fake/env":
		values := map[string]string{}
		for _, raw := range listField(script, "echo_env") {
			if name := stringOr(raw); name != "" {
				values[name] = os.Getenv(name)
			}
		}
		reply(id, values)
	case "textDocument/diagnostic":
		if boolField(script, "pull_diagnostics") {
			reply(id, map[string]any{"kind": "full", "items": diagnose(script, uriOf(params))})
			return
		}
		replyError(id, -32601, "method not found")
	default:
		if errSpec := methodEntry(script, "errors", method); errSpec != nil {
			var spec map[string]json.RawMessage
			if json.Unmarshal(errSpec, &spec) == nil {
				var code float64
				_ = json.Unmarshal(spec["code"], &code)
				replyError(id, int64(code), stringOr(spec["message"]))
				return
			}
		}
		for _, raw := range listField(script, "hang") {
			if stringOr(raw) == method {
				return // never answered
			}
		}
		if result := methodEntry(script, "responses", method); result != nil {
			reply(id, json.RawMessage(result))
			return
		}
		replyError(id, -32601, "method not found")
	}
}

func handleNotification(script map[string]json.RawMessage, method string, params json.RawMessage) {
	switch method {
	case "exit":
		if ms := intField(script, "hold_exit_ms"); ms > 0 {
			time.Sleep(time.Duration(ms) * time.Millisecond)
		}
		os.Exit(0)
	case "initialized":
		if raw, ok := script["after_initialized"]; ok && len(raw) > 0 {
			go runActions(raw)
		}
	case "textDocument/didOpen":
		td := textDocumentOf(params)
		if uri, ok := td["uri"].(string); ok {
			text, _ := td["text"].(string)
			version, _ := td["version"].(float64)
			docsMu.Lock()
			docs[uri] = map[string]any{"text": text, "version": version}
			docsMu.Unlock()
			maybeDiagnose(script, "didOpen", uri)
		}
	case "textDocument/didChange":
		td := textDocumentOf(params)
		if uri, ok := td["uri"].(string); ok {
			text := ""
			if changes, ok := paramsOf(params)["contentChanges"].([]any); ok {
				for _, change := range changes {
					if cm, ok := change.(map[string]any); ok {
						if t, ok := cm["text"].(string); ok {
							text = t // full-text sync: the last change wins
						}
					}
				}
			}
			version, _ := td["version"].(float64)
			docsMu.Lock()
			doc := docs[uri]
			if doc == nil {
				doc = map[string]any{}
				docs[uri] = doc
			}
			doc["text"] = text
			doc["version"] = version
			docsMu.Unlock()
			maybeDiagnose(script, "didChange", uri)
		}
	case "textDocument/didSave":
		if td := textDocumentOf(params); td != nil {
			if uri, ok := td["uri"].(string); ok {
				maybeDiagnose(script, "didSave", uri)
			}
		}
	case "textDocument/didClose":
		if td := textDocumentOf(params); td != nil {
			if uri, ok := td["uri"].(string); ok {
				docsMu.Lock()
				delete(docs, uri)
				docsMu.Unlock()
			}
		}
	}
}

// maybeDiagnose publishes diagnostics for uri when the method that changed
// the document is on the script's on-list.
func maybeDiagnose(script map[string]json.RawMessage, on, uri string) {
	diag := rawMap(script["diagnostics"])
	if diag == nil || uri == "" {
		return
	}
	wanted := false
	for _, raw := range rawList(diag["on"]) {
		if stringOr(raw) == on {
			wanted = true
			break
		}
	}
	if !wanted {
		return
	}
	delay := rawInt(diag["delay_ms"])
	go func() {
		if delay > 0 {
			time.Sleep(time.Duration(delay) * time.Millisecond)
		}
		docsMu.Lock()
		doc := docs[uri]
		docsMu.Unlock()
		text, version := "", 0.0
		if doc != nil {
			text, _ = doc["text"].(string)
			version, _ = doc["version"].(float64)
		}
		params := map[string]any{"uri": uri, "diagnostics": matchRules(diag, text)}
		if rawBool(diag["version"]) && version > 0 {
			params["version"] = version
		}
		send(map[string]any{"jsonrpc": "2.0", "method": "textDocument/publishDiagnostics", "params": params})
	}()
}

// diagnose answers textDocument/diagnostic with the same rules.
func diagnose(script map[string]json.RawMessage, uri string) []json.RawMessage {
	items := []json.RawMessage{}
	diag := rawMap(script["diagnostics"])
	if diag == nil {
		return items
	}
	docsMu.Lock()
	doc := docs[uri]
	docsMu.Unlock()
	text := ""
	if doc != nil {
		text, _ = doc["text"].(string)
	}
	return matchRules(diag, text)
}

// matchRules collects the diagnostics of every rule whose substring the
// document text contains.
func matchRules(diag map[string]json.RawMessage, text string) []json.RawMessage {
	items := []json.RawMessage{}
	rules := rawMap(diag["rules"])
	if rules == nil {
		return items
	}
	substrs := make([]string, 0, len(rules))
	for substr := range rules {
		substrs = append(substrs, substr)
	}
	sort.Strings(substrs)
	for _, substr := range substrs {
		if substr != "" && strings.Contains(text, substr) {
			items = append(items, rawList(rules[substr])...)
		}
	}
	return items
}

// runActions replays the after_initialized script: server→client requests
// (recording each reply) and notifications, either with an optional delay.
func runActions(raw json.RawMessage) {
	for _, item := range rawList(raw) {
		act := rawMap(item)
		if act == nil {
			continue
		}
		if ms := rawInt(act["delay_ms"]); ms > 0 {
			time.Sleep(time.Duration(ms) * time.Millisecond)
		}
		if method := stringOr(act["request"]); method != "" {
			pendMu.Lock()
			id := nextID
			nextID++
			ch := make(chan map[string]json.RawMessage, 1)
			pending[id] = ch
			pendMu.Unlock()
			entry := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
			if len(act["params"]) > 0 {
				entry["params"] = json.RawMessage(act["params"])
			}
			send(entry)
			select {
			case resp := <-ch:
				out := map[string]any{"id": id, "method": method}
				if r, ok := resp["result"]; ok {
					out["result"] = json.RawMessage(r)
				}
				if e, ok := resp["error"]; ok {
					out["error"] = json.RawMessage(e)
				}
				record(map[string]any{"resp": out})
			case <-time.After(10 * time.Second):
			}
		} else if method := stringOr(act["notify"]); method != "" {
			entry := map[string]any{"jsonrpc": "2.0", "method": method}
			if len(act["params"]) > 0 {
				entry["params"] = json.RawMessage(act["params"])
			}
			send(entry)
		}
	}
}

// deliverReply routes a client response to the request that waits for it.
func deliverReply(msg map[string]json.RawMessage) {
	var id int64
	if err := json.Unmarshal(msg["id"], &id); err != nil {
		return
	}
	pendMu.Lock()
	ch, ok := pending[id]
	if ok {
		delete(pending, id)
	}
	pendMu.Unlock()
	if ok {
		ch <- msg
	}
}

func paramsOf(params json.RawMessage) map[string]any {
	out := map[string]any{}
	if len(params) > 0 {
		_ = json.Unmarshal(params, &out)
	}
	return out
}

func textDocumentOf(params json.RawMessage) map[string]any {
	td, _ := paramsOf(params)["textDocument"].(map[string]any)
	return td
}

func uriOf(params json.RawMessage) string {
	uri, _ := textDocumentOf(params)["uri"].(string)
	return uri
}

// methodEntry returns the raw value script[key][method], or nil.
func methodEntry(script map[string]json.RawMessage, key, method string) json.RawMessage {
	entries := rawMap(script[key])
	if entries == nil {
		return nil
	}
	raw, ok := entries[method]
	if !ok || len(raw) == 0 {
		return nil
	}
	return raw
}

func textField(script map[string]json.RawMessage, key string) string {
	return stringOr(script[key])
}

func boolField(script map[string]json.RawMessage, key string) bool {
	return rawBool(script[key])
}

func intField(script map[string]json.RawMessage, key string) int {
	return rawInt(script[key])
}

func listField(script map[string]json.RawMessage, key string) []json.RawMessage {
	return rawList(script[key])
}

func stringOr(raw json.RawMessage) string {
	var s string
	if len(raw) == 0 || json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

func rawBool(raw json.RawMessage) bool {
	var b bool
	return len(raw) > 0 && json.Unmarshal(raw, &b) == nil && b
}

func rawInt(raw json.RawMessage) int {
	var n float64
	if len(raw) == 0 || json.Unmarshal(raw, &n) != nil {
		return 0
	}
	return int(n)
}

func rawList(raw json.RawMessage) []json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var out []json.RawMessage
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	return out
}

func rawMap(raw json.RawMessage) map[string]json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var out map[string]json.RawMessage
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	return out
}

func record(entry map[string]any) {
	if recFile == nil {
		return
	}
	b, err := json.Marshal(entry)
	if err != nil {
		return
	}
	recMu.Lock()
	defer recMu.Unlock()
	_, _ = recFile.Write(append(b, '\n'))
}

func send(msg map[string]any) {
	body, err := json.Marshal(msg)
	if err != nil {
		return
	}
	writeMu.Lock()
	defer writeMu.Unlock()
	if err := lsp.WriteMessage(os.Stdout, body); err != nil {
		os.Exit(0)
	}
}

func reply(id json.RawMessage, result any) {
	send(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "result": result})
}

func replyError(id json.RawMessage, code int64, message string) {
	send(map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(id),
		"error":   map[string]any{"code": code, "message": message},
	})
}
`
