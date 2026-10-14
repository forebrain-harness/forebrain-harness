package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
)

// TestTestConnectionConnectsListsToolsAndLetsGo is the check behind the button:
// a real server is started, its tools are listed, and the connection — with the
// child process behind it — is gone by the time the answer is returned.
func TestTestConnectionConnectsListsToolsAndLetsGo(t *testing.T) {
	fixture := newProcessFixture(t, fixtureModeServer)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	res, err := TestConnection(ctx, t.TempDir(), t.TempDir(), fixture.serverConfig(0))
	if err != nil {
		t.Fatalf("TestConnection: %v", err)
	}
	if !res.OK {
		t.Fatalf("result = %+v, want a connection", res)
	}
	if res.ToolCount < 1 || res.ToolCount != len(res.Tools) {
		t.Fatalf("result = %+v", res)
	}
	found := false
	for _, tool := range res.Tools {
		if tool.Name == "echo" {
			found = true
		}
	}
	if !found {
		t.Fatalf("tools = %+v, want the fixture's echo", res.Tools)
	}
	// The test leaves nothing running: the fixture writes its own pid, and the
	// connection has to take that process down with it.
	waitForProcessGone(t, fixture.waitForPid(t))
}

// TestTestConnectionReportsAServerThatWillNotStart pins that "it did not
// connect" is an answer to show, not an error to log: the server's own text is
// the only clue the user has.
func TestTestConnectionReportsAServerThatWillNotStart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	missing := filepath.Join(t.TempDir(), "not-a-command")
	res, err := TestConnection(ctx, t.TempDir(), t.TempDir(), appcfg.MCPServerConfig{
		Name:      "broken",
		Transport: "stdio",
		Command:   missing,
	})
	if err != nil {
		t.Fatalf("a server that cannot start is a result, not a caller error: %v", err)
	}
	if res.OK {
		t.Fatalf("result = %+v", res)
	}
	if strings.TrimSpace(res.Error) == "" {
		t.Fatalf("the server's own failure text must travel back: %+v", res)
	}
}

// TestTestConnectionReclaimsTheChildWhenTheListingFails pins the failure path
// that leaves processes behind if it is not handled: the handshake succeeded, the
// tool listing did not, and the child is still reclaimable from here.
func TestTestConnectionReclaimsTheChildWhenTheListingFails(t *testing.T) {
	fixture := newProcessFixture(t, fixtureModeToolsFail)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	res, err := TestConnection(ctx, t.TempDir(), t.TempDir(), fixture.serverConfig(0))
	if err != nil {
		t.Fatalf("TestConnection: %v", err)
	}
	if res.OK || strings.TrimSpace(res.Error) == "" {
		t.Fatalf("result = %+v, want the server's own refusal", res)
	}
	waitForProcessGone(t, fixture.waitForPid(t))
}

// TestTestConnectionRejectsAnEntryThatIsNotAConnection pins the one case that is
// the caller's to fix rather than the server's to report.
func TestTestConnectionRejectsAnEntryThatIsNotAConnection(t *testing.T) {
	ctx := context.Background()
	cases := []appcfg.MCPServerConfig{
		{Name: "emptiness", Transport: "stdio"},
		{Name: "nonsense", Transport: "carrier-pigeon", Command: "/bin/true"},
		{Name: "no-url", Transport: "streamable_http"},
	}
	for _, srv := range cases {
		res, err := TestConnection(ctx, t.TempDir(), t.TempDir(), srv)
		if err == nil || !IsTestConnectionInputError(err) {
			t.Fatalf("%s: err = %v, want an input error", srv.Name, err)
		}
		if res.OK {
			t.Fatalf("%s: result = %+v", srv.Name, res)
		}
	}
}

// TestRequestTimeoutBoundsOneToolCall pins the field that did not exist before:
// an entry that says how long a tool call may take gets exactly that, while the
// startup listing keeps the patience the startup timeout gives it.
func TestRequestTimeoutBoundsOneToolCall(t *testing.T) {
	fixture := newProcessFixture(t, fixtureModeHangingCall)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	srv := fixture.serverConfig(0)
	srv.RequestTimeout = 0.3
	sess, err := Start(ctx, t.TempDir(), t.TempDir(), srv)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	pid := childPid(t, sess)
	defer func() {
		_ = sess.Close()
		waitForProcessGone(t, pid)
	}()

	// The listing is bounded by startup_timeout, not by the per-call bound: a
	// 0.3s call deadline must not cut the handshake's tool listing off.
	if _, err := sess.ListToolMetas(ctx); err != nil {
		t.Fatalf("request_timeout must not govern the startup listing: %v", err)
	}

	started := time.Now()
	_, callErr := sess.CallToolJSON(ctx, "hang", json.RawMessage(`{}`))
	elapsed := time.Since(started)
	if callErr == nil {
		t.Fatal("a call the server never answers must fail")
	}
	if !errors.Is(callErr, context.DeadlineExceeded) && !strings.Contains(callErr.Error(), "deadline") {
		t.Fatalf("the failure must be the call's own deadline, got %v", callErr)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("the call must end at request_timeout, took %s", elapsed)
	}
}

// TestNoRequestTimeoutKeepsWaiting is the other half of that contract: an entry
// that says nothing about the bound is not given one, so a long-running tool is
// still only ended by whoever asked for it.
func TestNoRequestTimeoutKeepsWaiting(t *testing.T) {
	fixture := newProcessFixture(t, fixtureModeHangingCall)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	sess, err := Start(ctx, t.TempDir(), t.TempDir(), fixture.serverConfig(0))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	pid := childPid(t, sess)
	defer func() {
		_ = sess.Close()
		waitForProcessGone(t, pid)
	}()
	if _, err := sess.ListToolMetas(ctx); err != nil {
		t.Fatalf("ListToolMetas: %v", err)
	}

	callCtx, callCancel := context.WithCancel(ctx)
	defer callCancel()
	done := make(chan error, 1)
	go func() {
		_, err := sess.CallToolJSON(callCtx, "hang", json.RawMessage(`{}`))
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("an entry with no request_timeout must not be cut off on its own: %v", err)
	case <-time.After(time.Second):
	}
	// The caller's own cancellation is the only thing that ends it.
	callCancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a cancelled call must report the cancellation")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a cancelled call never returned")
	}
}

// TestResolveRequestTimeout pins the resolution itself, including the boundary
// that keeps a typo from looking like a working configuration.
func TestResolveRequestTimeout(t *testing.T) {
	if d, err := ResolveRequestTimeout(0); err != nil || d != DefaultRequestTimeout {
		t.Fatalf("zero = %s, %v", d, err)
	}
	if d, err := ResolveRequestTimeout(30); err != nil || d != 30*time.Second {
		t.Fatalf("30 = %s, %v", d, err)
	}
	if _, err := ResolveRequestTimeout(-1); err == nil {
		t.Fatal("a negative bound must be rejected")
	}
	srv := appcfg.MCPServerConfig{RequestTimeout: 0.5}
	if RequestTimeoutFor(srv) != 500*time.Millisecond {
		t.Fatalf("RequestTimeoutFor = %s", RequestTimeoutFor(srv))
	}
	if RequestTimeoutFor(appcfg.MCPServerConfig{}) != DefaultRequestTimeout {
		t.Fatalf("an entry that says nothing gets no bound, got %s", RequestTimeoutFor(appcfg.MCPServerConfig{}))
	}
}
