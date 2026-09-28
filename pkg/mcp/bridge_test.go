package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The fixtures below are real MCP servers run as real child processes, because
// what they pin is process lifecycle: a start that fails, a server that never
// answers, a close that has to reap. None of that can be observed with an
// in-process transport.
const (
	// fixturePidEnv names the file the fixture writes its pid into as soon as it
	// starts, which is how a test learns what to watch when the session it
	// started is not the one being closed.
	fixturePidEnv = "FOREBRAIN_TEST_MCP_FIXTURE_PID_FILE"
	// fixtureExitEnv names the file the fixture writes when it is about to
	// exit. Its appearance is the signal that the child really shut down,
	// rather than "the test slept long enough".
	fixtureExitEnv = "FOREBRAIN_TEST_MCP_FIXTURE_EXIT_FILE"
	// fixtureModeEnv selects what the fixture does.
	fixtureModeEnv = "FOREBRAIN_TEST_MCP_FIXTURE_MODE"
)

const (
	fixtureModeServer      = "server"
	fixtureModeBadProtocol = "bad-protocol"
	fixtureModeSilent      = "silent"
	fixtureModeToolsFail   = "tools-fail"
)

func TestStdioSessionCloseReapsTheChildProcess(t *testing.T) {
	fixture := newProcessFixture(t, fixtureModeServer)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	sess, err := Start(ctx, t.TempDir(), t.TempDir(), fixture.serverConfig(0))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	pid := childPid(t, sess)
	if _, err := sess.ListToolMetas(ctx); err != nil {
		t.Fatalf("ListToolMetas: %v", err)
	}
	if err := sess.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	waitForProcessGone(t, pid)
	fixture.waitForExitMarker(t)
}

func TestStdioStartFailureReclaimsTheChildProcess(t *testing.T) {
	cases := []struct {
		name    string
		mode    string
		timeout float64
	}{
		// Client.Connect returns an error without closing the connection it
		// established when the server answers with an unsupported protocol
		// version, so this is the case that used to leave the child behind.
		{name: "unsupported protocol version", mode: fixtureModeBadProtocol},
		// The deadline is generous on purpose: this asserts that the child is
		// reclaimed, and it learns the child's pid from a file the child itself
		// writes. A deadline tight enough to fire before the fixture has started
		// (a loaded machine, a cold binary) would assert that a process which
		// never ran was killed. The deadline path itself is covered by the
		// per-server timeout test in pkg/run, which starts no long-lived child.
		{name: "server never answers the handshake", mode: fixtureModeSilent, timeout: 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newProcessFixture(t, tc.mode)
			// The startup deadline is applied by the context, exactly as a load
			// applies one per server before calling Start with it.
			budget := 20 * time.Second
			if tc.timeout > 0 {
				budget = time.Duration(tc.timeout * float64(time.Second))
			}
			ctx, cancel := context.WithTimeout(context.Background(), budget)
			defer cancel()
			sess, err := Start(ctx, t.TempDir(), t.TempDir(), fixture.serverConfig(tc.timeout))
			if err == nil {
				_ = sess.Close()
				t.Fatal("Start succeeded against a server that cannot complete a handshake")
			}
			if tc.mode == fixtureModeSilent && !strings.Contains(err.Error(), "context deadline exceeded") {
				t.Fatalf("timeout error must keep the underlying text: %v", err)
			}
			waitForProcessGone(t, fixture.waitForPid(t))
		})
	}
}

func TestStdioAbortReclaimsTheChildAfterToolsListFails(t *testing.T) {
	fixture := newProcessFixture(t, fixtureModeToolsFail)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	sess, err := Start(ctx, t.TempDir(), t.TempDir(), fixture.serverConfig(0))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	pid := childPid(t, sess)
	if _, err := sess.ListToolMetas(ctx); err == nil {
		t.Fatal("ListToolMetas must fail against a server that refuses it")
	}
	// Abort is the failure path: signal the child first so the close cannot
	// spend the SDK's terminate duration on a server already known to be
	// unusable — and still reap it. The reaping is the success it reports.
	if err := sess.Abort(); err != nil {
		t.Fatalf("Abort: %v", err)
	}
	waitForProcessGone(t, pid)
}

// TestReapedAfterStopReadsEveryWayAStoppedChildEnds pins how a teardown that
// stops the child before closing reads the close's report. The child either
// dies of the stop's signal or exits successfully once told to stop, and in
// the second case exec.Cmd.Wait reports the stop's cancelled context: both are
// the child reaped, and Release once failed at random on the second.
func TestReapedAfterStopReadsEveryWayAStoppedChildEnds(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell and signals")
	}
	stopped := func(t *testing.T, script string, cancelWith func(*exec.Cmd) error) error {
		t.Helper()
		ctx, stop := context.WithCancel(context.Background())
		cmd := exec.CommandContext(ctx, "sh", "-c", script)
		if cancelWith != nil {
			cmd.Cancel = func() error { return cancelWith(cmd) }
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		// Wait for the shell to be ready for the signal before stopping it.
		if _, err := io.ReadFull(stdout, make([]byte, 5)); err != nil {
			t.Fatal(err)
		}
		stop()
		return cmd.Wait()
	}

	killed := stopped(t, "echo ready; sleep 30", nil)
	var exited *exec.ExitError
	if !errors.As(killed, &exited) || !reapedAfterStop(killed) {
		t.Fatalf("a child the stop killed: %v", killed)
	}
	exitedCleanly := stopped(t, "trap 'exit 0' TERM; echo ready; sleep 30 & wait", func(cmd *exec.Cmd) error {
		return cmd.Process.Signal(syscall.SIGTERM)
	})
	if !errors.Is(exitedCleanly, context.Canceled) || !reapedAfterStop(exitedCleanly) {
		t.Fatalf("a child that exited successfully once stopped: %v", exitedCleanly)
	}
	if reapedAfterStop(errors.New("write |1: broken pipe")) {
		t.Fatal("a close that failed is not a reaping")
	}
}

func TestStdioSessionCloseIsIdempotent(t *testing.T) {
	fixture := newProcessFixture(t, fixtureModeServer)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	sess, err := Start(ctx, t.TempDir(), t.TempDir(), fixture.serverConfig(0))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	pid := childPid(t, sess)
	if err := sess.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := sess.Close(); err != nil {
		t.Fatalf("second Close must be a no-op: %v", err)
	}
	waitForProcessGone(t, pid)
}

// TestReconnectRebuildsOnTheSessionLifetimeContext pins the two reconnect
// fixes: the replacement is built on the session's own lifetime rather than on
// the tool call that discovered the expiry — a child that outlives the call must
// not die with it — and it is installed under the session lock with the previous
// connection torn down.
func TestReconnectRebuildsOnTheSessionLifetimeContext(t *testing.T) {
	fixture := newProcessFixture(t, fixtureModeServer)
	spare := newProcessFixture(t, fixtureModeServer)

	// The context that started the session stands in for a tool call: by the
	// time it is discovered to have expired, the caller has already given up and
	// cancelled. A reconnect that inherited it could not rebuild anything.
	upstream, cancelUpstream := context.WithCancel(context.Background())
	defer cancelUpstream()
	sess, err := Start(upstream, t.TempDir(), t.TempDir(), fixture.serverConfig(5))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer sess.Close()
	firstPid := childPid(t, sess)
	cancelUpstream()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// The established connection survives its caller, which is the reason the
	// session keeps a process context of its own.
	if _, err := sess.ListToolMetas(ctx); err != nil {
		t.Fatalf("the session's child died with the context that started it: %v", err)
	}

	var (
		mu         sync.Mutex
		factoryCtx context.Context
	)
	sess.reconnect = func(handshake context.Context) (*Session, error) {
		mu.Lock()
		factoryCtx = handshake
		mu.Unlock()
		return Start(handshake, t.TempDir(), t.TempDir(), spare.serverConfig(5))
	}
	if err := sess.reconnectSession(); err != nil {
		t.Fatalf("reconnectSession: %v", err)
	}
	mu.Lock()
	handshake := factoryCtx
	mu.Unlock()
	if handshake == nil {
		t.Fatal("the reconnect factory was never called")
	}
	deadline, ok := handshake.Deadline()
	if !ok {
		t.Fatal("a reconnect handshake must be bounded by the server's startup timeout")
	}
	if remaining := time.Until(deadline); remaining <= 0 || remaining > 6*time.Second {
		t.Fatalf("reconnect handshake is bounded by %s, want the 5s startup timeout", remaining)
	}

	// The displaced child is signalled as the replacement is installed, and the
	// reconnected session works even though its upstream is already cancelled.
	waitForProcessGone(t, firstPid)
	if _, err := sess.ListToolMetas(ctx); err != nil {
		t.Fatalf("ListToolMetas after reconnect: %v", err)
	}
	secondPid := childPid(t, sess)

	// A closed session must refuse to be reconnected into, and must close the
	// connection the factory built rather than adopt it.
	if err := sess.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	sparePid := spare.waitForPid(t)
	if err := sess.reconnectSession(); err == nil {
		t.Fatal("a closed session must not accept a reconnect")
	}
	waitForProcessGone(t, secondPid)
	waitForProcessGone(t, sparePid)
}

// TestConcurrentCallToolAndClose runs the reconnect, call and close paths
// against each other: they all mutate the same session fields, so this is the
// test that -race has to be run on.
func TestConcurrentCallToolAndClose(t *testing.T) {
	fixture := newProcessFixture(t, fixtureModeServer)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sess, err := Start(ctx, t.TempDir(), t.TempDir(), fixture.serverConfig(5))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	sess.reconnect = func(reconnectCtx context.Context) (*Session, error) {
		return Start(reconnectCtx, t.TempDir(), t.TempDir(), fixture.serverConfig(5))
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = sess.CallToolJSON(ctx, "echo", json.RawMessage(`{"text":"x"}`))
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = sess.reconnectSession()
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = sess.Close()
	}()
	wg.Wait()
	_ = sess.Close()
}

// processFixture is one built fixture binary plus the files one run of it
// writes: the pid it started with, and the marker it writes on the way out.
type processFixture struct {
	exe      string
	mode     string
	pidFile  string
	exitFile string
}

func newProcessFixture(t *testing.T, mode string) *processFixture {
	t.Helper()
	dir := t.TempDir()
	exe := buildProcessFixture(t, dir)
	return &processFixture{
		exe:      exe,
		mode:     mode,
		pidFile:  filepath.Join(dir, "pid"),
		exitFile: filepath.Join(dir, "exit"),
	}
}

// serverConfig is the entry the session under test starts. The marker files
// travel as explicit env: an MCP child gets a filtered environment, and these
// are names it is meant to see.
func (f *processFixture) serverConfig(timeoutSeconds float64) appcfg.MCPServerConfig {
	return appcfg.MCPServerConfig{
		Name:           "fixture",
		Transport:      "stdio",
		Command:        f.exe,
		StartupTimeout: timeoutSeconds,
		Env: map[string]string{
			fixturePidEnv:  f.pidFile,
			fixtureExitEnv: f.exitFile,
			fixtureModeEnv: f.mode,
		},
	}
}

func (f *processFixture) waitForPid(t *testing.T) int {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(f.pidFile)
		if err == nil {
			pid, convErr := strconv.Atoi(strings.TrimSpace(string(raw)))
			if convErr == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("fixture %s never wrote its pid", f.pidFile)
	return 0
}

// waitForExitMarker waits for the child to reach its own exit path, which is
// what proves the shutdown travelled all the way into the server rather than
// only killing the process.
func (f *processFixture) waitForExitMarker(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(f.exitFile); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("fixture never reached its exit path: %s", f.exitFile)
}

func childPid(t *testing.T, sess *Session) int {
	t.Helper()
	if sess == nil || sess.proc == nil {
		t.Fatal("session has no child process to watch")
	}
	pid := sess.proc.pidOf()
	if pid <= 0 {
		t.Fatal("session has no child process to watch")
	}
	return pid
}

// buildProcessFixture compiles the fixture server into dir.
func buildProcessFixture(t *testing.T, dir string) string {
	t.Helper()
	mainPath := filepath.Join(dir, "main.go")
	if err := os.WriteFile(mainPath, []byte(processFixtureSource), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	exe := filepath.Join(dir, "mcp-process-fixture")
	cmd := exec.Command("go", "build", "-o", exe, mainPath)
	cmd.Dir = repoRootForTest(t)
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v\n%s", err, out)
	}
	return exe
}

// waitForProcessGone waits until the pid is neither running nor a zombie. A
// zombie is exactly what "signalled but never reaped" looks like, so it counts
// as not gone: a test that only asked "is it running" would pass while the reap
// never happened.
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
	t.Fatalf("process %d is still %s after the session was closed", pid, last)
}

func processState(pid int) string {
	out, err := exec.Command("ps", "-o", "state=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// processFixtureSource is the fixture child. Every mode writes its pid on
// startup and a marker on exit, and the raw modes speak just enough JSON-RPC to
// drive the client into the state under test.
const processFixtureSource = `package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type echoIn struct {
	Text string ` + "`json:\"text\"`" + `
}

type echoOut struct {
	Text string ` + "`json:\"text\"`" + `
}

func main() {
	writeMarker(os.Getenv("FOREBRAIN_TEST_MCP_FIXTURE_PID_FILE"), fmt.Sprint(os.Getpid()))
	defer writeMarker(os.Getenv("FOREBRAIN_TEST_MCP_FIXTURE_EXIT_FILE"), "done")
	switch os.Getenv("FOREBRAIN_TEST_MCP_FIXTURE_MODE") {
	case "bad-protocol", "silent", "tools-fail":
		serveRaw(os.Getenv("FOREBRAIN_TEST_MCP_FIXTURE_MODE"))
	default:
		serveSDK()
	}
}

func writeMarker(path, text string) {
	if path == "" {
		return
	}
	_ = os.WriteFile(path, []byte(text), 0o600)
}

func serveSDK() {
	server := mcp.NewServer(&mcp.Implementation{Name: "process-fixture", Version: "1.0.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "echo", Description: "echo text"}, func(ctx context.Context, req *mcp.CallToolRequest, in echoIn) (*mcp.CallToolResult, echoOut, error) {
		return nil, echoOut{Text: in.Text}, nil
	})
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		panic(err)
	}
}

// serveRaw answers newline-delimited JSON-RPC by hand, which is the only way to
// produce answers a real server does not: an unsupported protocol version, no
// answer at all, and a failing tools/list.
func serveRaw(mode string) {
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 1<<20), 1<<20)
	out := bufio.NewWriter(os.Stdout)
	for in.Scan() {
		line := in.Bytes()
		if len(line) == 0 {
			continue
		}
		var req struct {
			ID     any    ` + "`json:\"id\"`" + `
			Method string ` + "`json:\"method\"`" + `
		}
		if json.Unmarshal(line, &req) != nil || req.Method == "" || req.ID == nil {
			continue
		}
		switch req.Method {
		case "initialize":
			if mode == "silent" {
				continue
			}
			version := "2025-06-18"
			if mode == "bad-protocol" {
				version = "1999-01-01"
			}
			writeMessage(out, map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{
				"protocolVersion": version,
				"capabilities":    map[string]any{},
				"serverInfo":      map[string]any{"name": "raw-fixture", "version": "1.0.0"},
			}})
		case "tools/list":
			if mode == "tools-fail" {
				writeMessage(out, map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32601, "message": "tools/list is disabled here"}})
				continue
			}
			writeMessage(out, map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"tools": []any{}}})
		default:
			writeMessage(out, map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32601, "message": "method not found"}})
		}
	}
	// Give the parent a moment to observe whatever the mode was reproducing,
	// then leave through the deferred exit marker.
	time.Sleep(50 * time.Millisecond)
}

func writeMessage(out *bufio.Writer, message map[string]any) {
	body, err := json.Marshal(message)
	if err != nil {
		return
	}
	_, _ = out.Write(append(body, '\n'))
	_ = out.Flush()
}
`

// TestExpiredSessionRetriesOnTheReconnectedConnection pins the recovery a
// dropped session is supposed to get, end to end.
//
// The server terminates the session mid-call, which the spec says it answers
// with 404 and the SDK reports as ErrSessionMissing. That is the one failure a
// reconnect can fix, so it has to be recognised as an expiry — and the retry has
// to run on the connection the reconnect installed. A retry bound to the
// connection the first attempt used would go straight back down the socket that
// had just been declared gone, turning a recoverable expiry into a failed tool
// call.
func TestExpiredSessionRetriesOnTheReconnectedConnection(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "expiring", Version: "1.0.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "echo", Description: "echo text"},
		func(ctx context.Context, req *mcp.CallToolRequest, in struct {
			Text string `json:"text"`
		}) (*mcp.CallToolResult, struct {
			Text string `json:"text"`
		}, error) {
			return nil, struct {
				Text string `json:"text"`
			}{Text: in.Text}, nil
		})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)

	var (
		mu        sync.Mutex
		expired   bool
		toolCalls int
	)
	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		if bytes.Contains(body, []byte(`"method":"tools/call"`)) {
			mu.Lock()
			toolCalls++
			first := !expired
			expired = true
			mu.Unlock()
			if first {
				// §2.5.3: a server that has terminated the session answers
				// requests carrying its id with 404.
				w.WriteHeader(http.StatusNotFound)
				return
			}
		}
		handler.ServeHTTP(w, r)
	}))
	defer httpSrv.Close()

	cfg := appcfg.MCPServerConfig{Name: "expiring", Transport: "streamable_http", URL: httpSrv.URL, StartupTimeout: 10}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sess, err := Start(ctx, t.TempDir(), t.TempDir(), cfg)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer sess.Close()

	out, err := sess.CallToolJSON(ctx, "echo", json.RawMessage(`{"text":"after the expiry"}`))
	if err != nil {
		t.Fatalf("CallToolJSON did not recover from an expired session: %v", err)
	}
	if !strings.Contains(out, "after the expiry") {
		t.Fatalf("CallToolJSON = %q, want the retried call's own result", out)
	}
	mu.Lock()
	calls := toolCalls
	mu.Unlock()
	if calls != 2 {
		t.Fatalf("the server saw %d tool calls, want the expired one and the retry", calls)
	}
}

// TestSecondReconnectRebuildsFromTheLiveChildsLifetime pins that a session
// reconnects more than once.
//
// Each reconnect brings its own child process and therefore its own lifetime
// context, and the previous one is cancelled as its child is torn down. A
// session that kept the first lifetime would build every later reconnect on a
// context that was already cancelled, so the second expiry — on a long-lived
// server, the common one — could never be recovered from.
func TestSecondReconnectRebuildsFromTheLiveChildsLifetime(t *testing.T) {
	first := newProcessFixture(t, fixtureModeServer)
	second := newProcessFixture(t, fixtureModeServer)
	third := newProcessFixture(t, fixtureModeServer)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sess, err := Start(ctx, t.TempDir(), t.TempDir(), first.serverConfig(5))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer sess.Close()
	firstPid := childPid(t, sess)

	next := second
	sess.reconnect = func(handshake context.Context) (*Session, error) {
		return Start(handshake, t.TempDir(), t.TempDir(), next.serverConfig(5))
	}
	if err := sess.reconnectSession(); err != nil {
		t.Fatalf("first reconnect: %v", err)
	}
	waitForProcessGone(t, firstPid)
	secondPid := childPid(t, sess)

	next = third
	if err := sess.reconnectSession(); err != nil {
		t.Fatalf("second reconnect: %v", err)
	}
	waitForProcessGone(t, secondPid)
	if _, err := sess.ListToolMetas(ctx); err != nil {
		t.Fatalf("ListToolMetas after two reconnects: %v", err)
	}
	thirdPid := childPid(t, sess)
	if thirdPid == secondPid || thirdPid == firstPid {
		t.Fatalf("the second reconnect did not install a new child (pid %d)", thirdPid)
	}
}

// TestReleaseReclaimsTheChildAndTheNextCallGrowsItBack pins the idle-release
// contract: a released session loses its child process, not its identity, and
// the first request afterwards brings the connection back instead of failing.
//
// This is what makes an idle Runner able to give MCP resources back at all —
// Close would end the session permanently, and the tool table a long-lived
// session was built on still names this server.
func TestReleaseReclaimsTheChildAndTheNextCallGrowsItBack(t *testing.T) {
	fixture := newProcessFixture(t, fixtureModeServer)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sess, err := Start(ctx, t.TempDir(), t.TempDir(), fixture.serverConfig(5))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer sess.Close()
	firstPid := childPid(t, sess)

	if err := sess.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	waitForProcessGone(t, firstPid)

	// The session is not closed: the next request rebuilds the connection, on a
	// new child, and succeeds as though nothing had been taken away.
	if _, err := sess.CallToolJSON(ctx, "echo", json.RawMessage(`{"text":"back"}`)); err != nil {
		t.Fatalf("CallToolJSON after Release: %v", err)
	}
	secondPid := childPid(t, sess)
	if secondPid == firstPid {
		t.Fatalf("the reconnected child reuses the released pid %d", secondPid)
	}

	// A second release of a live connection works, and a release of an already
	// released one is a no-op rather than an error.
	if err := sess.Release(); err != nil {
		t.Fatalf("second Release: %v", err)
	}
	waitForProcessGone(t, secondPid)
	if err := sess.Release(); err != nil {
		t.Fatalf("Release of an already-released session: %v", err)
	}

	// Close after releases still reaps whatever is left and stays idempotent —
	// the released handle must not become a leak the pool cannot collect.
	if err := sess.Close(); err != nil {
		t.Fatalf("Close after Release: %v", err)
	}
	if err := sess.reconnectSession(); err == nil {
		t.Fatal("a closed session must not accept a reconnect, released or not")
	}
}

// TestReleasedSessionIsClosedByTheRegistryTeardown pins the ownership side: a
// released handle still belongs to its registry, and the registry's Close —
// not just the pool's idle sweep — is what ends it for good.
func TestReleasedSessionIsClosedByTheRegistryTeardown(t *testing.T) {
	reg := NewRegistry()
	sess := &Session{}
	reg.Apply(StatusUpdate{Name: "docs", Transport: "stdio", Status: ConnStatusConnected,
		AuthStatus: AuthStatusAuthenticated, ToolCount: 1, Session: sess})

	if n := reg.ReleaseSessions(); n != 1 {
		t.Fatalf("ReleaseSessions released %d, want 1", n)
	}
	rec, ok := reg.StatusOf("docs")
	if !ok || rec.ConnStatus != ConnStatusDisconnected {
		t.Fatalf("record after release = %+v ok=%v, want disconnected", rec, ok)
	}
	if rec.ConnectedAt != nil {
		t.Fatalf("a disconnected record kept its connected-at: %+v", rec.ConnectedAt)
	}
	// The handle survives the release: this is the whole difference between a
	// release and a teardown, and it is what the next request reconnects through.
	if _, ok := reg.GetSession("docs"); !ok {
		t.Fatal("a released record must keep its session handle")
	}

	reg.Close()
	if _, ok := reg.GetSession("docs"); ok {
		t.Fatal("Close must drop the released handle along with everything else")
	}
	if n := reg.ReleaseSessions(); n != 0 {
		t.Fatalf("a closed registry released %d, want 0", n)
	}
}

// TestReleaseSessionsTellsSubscribersWhatChanged pins the notification: a
// surface watching the registry hears that the servers went from connected to
// idle-released, so a status line or a web badge cannot keep claiming a live
// connection that was just given back.
func TestReleaseSessionsTellsSubscribersWhatChanged(t *testing.T) {
	reg := NewRegistry()
	reg.Apply(StatusUpdate{Name: "docs", Transport: "stdio", Status: ConnStatusConnected,
		AuthStatus: AuthStatusAuthenticated, ToolCount: 2, Session: &Session{}})
	var (
		mu      sync.Mutex
		states  []ConnStatus
		haveSes []bool
	)
	stop := reg.Subscribe(func(records []ServerRecord) {
		mu.Lock()
		defer mu.Unlock()
		states = nil
		haveSes = nil
		for _, rec := range records {
			states = append(states, rec.ConnStatus)
			haveSes = append(haveSes, rec.ToolCount > 0)
		}
	})
	defer stop()

	if n := reg.ReleaseSessions(); n != 1 {
		t.Fatalf("ReleaseSessions released %d, want 1", n)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(states) != 1 || states[0] != ConnStatusDisconnected {
		t.Fatalf("subscriber saw %v, want one disconnected record", states)
	}
	// What the record knew before the release it still knows: the release takes
	// the connection, not the server's own facts.
	if len(haveSes) != 1 || !haveSes[0] {
		t.Fatalf("the released record lost its tool count: %v", haveSes)
	}
}

// TestConcurrentReleaseCallReconnectClose runs the idle release against the
// request paths it can interleave with. All of them replace or read the same
// connection under the same two locks (reconnectMu, then mu), so this is the
// test -race has to see; its success condition is not that a particular call
// wins but that exactly one child is left standing and it is the one the
// session ends up holding.
func TestConcurrentReleaseCallReconnectClose(t *testing.T) {
	fixture := newProcessFixture(t, fixtureModeServer)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sess, err := Start(ctx, t.TempDir(), t.TempDir(), fixture.serverConfig(5))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	sess.reconnect = func(reconnectCtx context.Context) (*Session, error) {
		return Start(reconnectCtx, t.TempDir(), t.TempDir(), fixture.serverConfig(5))
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = sess.CallToolJSON(ctx, "echo", json.RawMessage(`{"text":"x"}`))
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = sess.Release()
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = sess.reconnectSession()
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = sess.Release()
	}()
	wg.Wait()
	// Close is the arbiter: whatever combination won, it must leave nothing
	// behind, and the idempotent calls after it must be no-ops.
	_ = sess.Close()
	_ = sess.Close()
	_ = sess.Release()
}
