package gateway

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/process"
)

func TestRestServerRunReportsBusyPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy port: %v", err)
	}
	defer ln.Close()

	srv := NewRestServer(ln.Addr().String())
	err = srv.Run(nil)
	if err == nil {
		t.Fatal("Run on a busy port must return an error")
	}
	if !strings.Contains(err.Error(), "gateway listen on") {
		t.Fatalf("error = %q, want it to name the listen failure", err)
	}
}

func bannerAddr(t *testing.T, s string) net.Addr {
	t.Helper()
	addr, err := net.ResolveTCPAddr("tcp", s)
	if err != nil {
		t.Fatalf("resolve %s: %v", s, err)
	}
	return addr
}

func TestWriteStartupBannerGolden(t *testing.T) {
	var buf bytes.Buffer
	writeStartupBanner(&buf, startupBanner{
		Version:     "v0.3.0",
		Addr:        bannerAddr(t, "127.0.0.1:6060"),
		AuthMode:    "token",
		SignInToken: "tok",
		WebUI:       true,
	})

	want := bannerArt +
		"Forebrain Harness Gateway v0.3.0\n" +
		"\n" +
		"Web UI       http://127.0.0.1:6060/\n" +
		"Sign in      http://127.0.0.1:6060/login#token=tok\n" +
		"Auth         token\n" +
		"Listening on http://127.0.0.1:6060\n"
	if buf.String() != want {
		t.Fatalf("banner mismatch:\n--- got ---\n%s\n--- want ---\n%s", buf.String(), want)
	}
}

// A configured token is free text; the link must carry it so the sign-in
// page's decodeURIComponent (which, like PathUnescape, decodes only %XX)
// reads back exactly the token, with nothing cutting the fragment short.
func TestSignInFragmentValueRoundTripsFreeTextTokens(t *testing.T) {
	for _, token := range []string{"tok", "a&b#c", "100% sure", "x+y=z", "日本"} {
		encoded := signInFragmentValue(token)
		if strings.ContainsAny(encoded, "&# +") {
			t.Fatalf("encoded %q = %q still carries a fragment delimiter", token, encoded)
		}
		decoded, err := url.PathUnescape(encoded)
		if err != nil || decoded != token {
			t.Fatalf("round trip %q -> %q -> %q (%v)", token, encoded, decoded, err)
		}
	}
}

func TestWriteStartupBannerOmitsSignInWithoutToken(t *testing.T) {
	var buf bytes.Buffer
	writeStartupBanner(&buf, startupBanner{
		Version:  "v0.3.0",
		Addr:     bannerAddr(t, "127.0.0.1:6060"),
		AuthMode: "none",
	})
	if strings.Contains(buf.String(), "Sign in") {
		t.Fatalf("banner must omit the sign-in line without a token:\n%s", buf.String())
	}
	if strings.Contains(buf.String(), "Web UI") {
		t.Fatalf("banner must omit the Web UI line when no UI is served:\n%s", buf.String())
	}
}

func TestWriteStartupBannerUnspecifiedHost(t *testing.T) {
	var buf bytes.Buffer
	writeStartupBanner(&buf, startupBanner{
		Version:  "v0.3.0",
		Addr:     bannerAddr(t, "0.0.0.0:6060"),
		AuthMode: "token",
		WebUI:    true,
	})
	if !strings.Contains(buf.String(), "Web UI       http://127.0.0.1:6060/") {
		t.Fatalf("clickable URLs must use 127.0.0.1 for an unspecified bind:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "Listening on http://0.0.0.0:6060") {
		t.Fatalf("the listening line must name the actual bind address:\n%s", buf.String())
	}
}

func TestWriteStartupBannerNoVersion(t *testing.T) {
	var buf bytes.Buffer
	writeStartupBanner(&buf, startupBanner{
		Addr:     bannerAddr(t, "127.0.0.1:6060"),
		AuthMode: "token",
	})
	if !strings.Contains(buf.String(), "Forebrain Harness Gateway\n") {
		t.Fatalf("version-less banner must still name the product:\n%s", buf.String())
	}
}

func TestFS(t *testing.T) {
	fsys, ok := FS()
	if fsys == nil {
		t.Fatal("FS returned nil filesystem")
	}
	if !ok {
		t.Skip("frontend not built (placeholder only); skipping embedded asset checks")
	}
	// When the frontend is built, index.html and the assets dir must be present.
	if _, err := fs.Stat(fsys, "index.html"); err != nil {
		t.Fatalf("index.html missing from embedded FS: %v", err)
	}
	entries, err := fs.ReadDir(fsys, "assets")
	if err != nil {
		t.Fatalf("assets dir missing from embedded FS: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("embedded assets dir is empty")
	}
}

// --- gateway status/stop person-readable output ---

// useGatewayTestHome points the process config at an isolated home whose
// forebrain.yaml sets the gateway HTTP address, resetting the process-level
// Resolve cache around the test (it is global state).
func useGatewayTestHome(t *testing.T, addr string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "forebrain.yaml"),
		[]byte("gateway:\n  http_addr: \""+addr+"\"\n"), 0o600); err != nil {
		t.Fatalf("write test config: %v", err)
	}
	t.Setenv("FOREBRAIN_HOME", dir)
	process.ResetResolve()
	t.Cleanup(process.ResetResolve)
}

// warmResolve loads the isolated home's config once, before the output under
// test runs, so startup-time resolve work cannot leak into the probe-path
// silence assertions.
func warmResolve(t *testing.T) {
	t.Helper()
	if _, err := process.Resolve(); err != nil {
		t.Fatalf("resolve test home: %v", err)
	}
}

// captureDefaultSlog records everything logged through the default logger,
// restoring the previous one on cleanup.
func captureDefaultSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// refusedAddr returns a loopback address verified to refuse connections.
// Binding :0 and closing only *hopes* the port stays free: under the full
// parallel test run (make test), sibling package binaries churn ephemeral
// ports and can grab the candidate between the close and the probe, turning
// the "not running" fixture into a flake. So each candidate is verified by
// dialing it: only an address whose dial fails with ECONNREFUSED is returned.
func refusedAddr(t *testing.T) string {
	t.Helper()
	const attempts = 8
	for i := 0; i < attempts; i++ {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("open probe port: %v", err)
		}
		addr := ln.Addr().String()
		if err := ln.Close(); err != nil {
			t.Fatalf("close probe port: %v", err)
		}
		conn, err := net.DialTimeout("tcp", addr, 250*time.Millisecond)
		if err == nil {
			// Another process grabbed the port between close and dial.
			_ = conn.Close()
			continue
		}
		if errors.Is(err, syscall.ECONNREFUSED) {
			return addr
		}
		// Unexpected dial failure (e.g. timeout): try a fresh candidate.
	}
	t.Fatalf("no loopback candidate refused a dial in %d attempts", attempts)
	return ""
}

func TestGatewayHealthTextNotRunning(t *testing.T) {
	addr := refusedAddr(t)
	useGatewayTestHome(t, addr)
	warmResolve(t)
	logs := captureDefaultSlog(t)

	var out bytes.Buffer
	if err := GatewayHealthText(context.Background(), &out); err != nil {
		t.Fatalf("GatewayHealthText: %v", err)
	}
	want := "Gateway is not running (optional — the terminal works without it).\n" +
		"  Address    http://" + addr + "\n" +
		"  Reach      nothing is listening on " + addr + "\n" +
		"  Start it with `forebrain gateway start`\n"
	if out.String() != want {
		t.Fatalf("output mismatch:\n--- got ---\n%s\n--- want ---\n%s", out.String(), want)
	}
	if logs.Len() > 0 {
		t.Fatalf("a not-running probe is the command's answer, not a log event; got:\n%s", logs.String())
	}
}

func TestGatewayHealthTextRunning(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	useGatewayTestHome(t, addr)
	warmResolve(t)

	var out bytes.Buffer
	if err := GatewayHealthText(context.Background(), &out); err != nil {
		t.Fatalf("GatewayHealthText: %v", err)
	}
	want := "Gateway is running.\n" +
		"  Address    http://" + addr + "\n" +
		"  Health     ok (HTTP 200)\n" +
		"  Web UI     http://" + addr + "/\n"
	if out.String() != want {
		t.Fatalf("output mismatch:\n--- got ---\n%s\n--- want ---\n%s", out.String(), want)
	}
}

func TestGatewayHealthTextUnhealthyStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	useGatewayTestHome(t, addr)
	warmResolve(t)

	var out bytes.Buffer
	if err := GatewayHealthText(context.Background(), &out); err != nil {
		t.Fatalf("GatewayHealthText: %v", err)
	}
	want := "Gateway is reachable but the health check failed.\n" +
		"  Address    http://" + addr + "\n" +
		"  Health     HTTP 500\n"
	if out.String() != want {
		t.Fatalf("output mismatch:\n--- got ---\n%s\n--- want ---\n%s", out.String(), want)
	}
}

func TestGatewayShutdownRequestNotRunning(t *testing.T) {
	addr := refusedAddr(t)
	useGatewayTestHome(t, addr)
	warmResolve(t)
	logs := captureDefaultSlog(t)

	var out bytes.Buffer
	if err := GatewayShutdownRequest(context.Background(), &out); err != nil {
		t.Fatalf("GatewayShutdownRequest: %v", err)
	}
	want := "Gateway is not running — nothing to stop.\n" +
		"  Address    http://" + addr + "\n" +
		"  Reach      nothing is listening on " + addr + "\n"
	if out.String() != want {
		t.Fatalf("output mismatch:\n--- got ---\n%s\n--- want ---\n%s", out.String(), want)
	}
	if logs.Len() > 0 {
		t.Fatalf("a not-running probe is the command's answer, not a log event; got:\n%s", logs.String())
	}
}

func TestGatewayShutdownRequestAccepted(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	useGatewayTestHome(t, addr)
	warmResolve(t)

	var out bytes.Buffer
	if err := GatewayShutdownRequest(context.Background(), &out); err != nil {
		t.Fatalf("GatewayShutdownRequest: %v", err)
	}
	want := "Shutdown requested — the gateway at http://" + addr + " is stopping.\n"
	if out.String() != want {
		t.Fatalf("output mismatch:\n--- got ---\n%s\n--- want ---\n%s", out.String(), want)
	}
	if gotMethod != http.MethodPost || gotPath != "/admin/shutdown" {
		t.Fatalf("probe hit %s %s, want POST /admin/shutdown", gotMethod, gotPath)
	}
}

type fakeTimeoutError struct{}

func (fakeTimeoutError) Error() string   { return "probe timed out" }
func (fakeTimeoutError) Timeout() bool   { return true }
func (fakeTimeoutError) Temporary() bool { return true }

func TestReachOutcomeClassification(t *testing.T) {
	refused, reason := reachOutcome(
		fmt.Errorf("Get %q: %w", "http://127.0.0.1:6060/healthz", syscall.ECONNREFUSED),
		"127.0.0.1:6060")
	if !refused || reason != "nothing is listening on 127.0.0.1:6060" {
		t.Fatalf("ECONNREFUSED → (%v, %q), want (true, %q)", refused, reason, "nothing is listening on 127.0.0.1:6060")
	}

	wantTimeout := fmt.Sprintf("no response within %ds", int(gatewayProbeTimeout.Seconds()))
	refused, reason = reachOutcome(fakeTimeoutError{}, "127.0.0.1:6060")
	if refused || reason != wantTimeout {
		t.Fatalf("timeout → (%v, %q), want (false, %q)", refused, reason, wantTimeout)
	}

	refused, reason = reachOutcome(errors.New("weird probe failure"), "127.0.0.1:6060")
	if refused || reason != "weird probe failure" {
		t.Fatalf("passthrough → (%v, %q), want (false, %q)", refused, reason, "weird probe failure")
	}
}

func TestDisplayBaseURLMatchesDisplayHost(t *testing.T) {
	cases := []struct {
		addr string
		want string
	}{
		{addr: "0.0.0.0:6060", want: "http://127.0.0.1:6060"},
		{addr: ":6060", want: "http://127.0.0.1:6060"},
		{addr: "[::]:6060", want: "http://127.0.0.1:6060"},
		{addr: "127.0.0.1:6060", want: "http://127.0.0.1:6060"},
		{addr: "localhost", want: "http://localhost"}, // no port: passthrough
	}
	for _, tc := range cases {
		if got := displayBaseURL(tc.addr); got != tc.want {
			t.Fatalf("displayBaseURL(%q) = %q, want %q", tc.addr, got, tc.want)
		}
		// displayHost is the single mapping source; the URL must agree with it.
		if got, want := displayBaseURL(tc.addr), "http://"+displayHost(tc.addr); got != want {
			t.Fatalf("displayBaseURL(%q) = %q, want %q (displayHost agreement)", tc.addr, got, want)
		}
	}
}

// The server's own error reports — a panicking handler's stack among them —
// are wired through slog at construction time, not left on raw stderr.
func TestNewRestServerRoutesHTTPErrorsThroughSlog(t *testing.T) {
	if NewRestServer("127.0.0.1:0").ErrorLog == nil {
		t.Fatal("ErrorLog = nil, want the server's error reports routed through slog")
	}
}
