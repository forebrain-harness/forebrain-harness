package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// --- stderr wiring ---

// A terminal gets the compact colored console format; a redirected stderr
// (systemd, docker, a file) gets logfmt with zero ANSI bytes.
func TestStderrLogHandlerFormatFollowsTTY(t *testing.T) {
	_, read := captureStderr(t)

	t.Run("redirected", func(t *testing.T) {
		h := stderrLogHandler(slog.LevelDebug, true)
		if _, ok := h.(*slog.TextHandler); !ok {
			t.Fatalf("handler = %T, want the logfmt text handler off a terminal", h)
		}
		slog.New(h).Info("hello", "k", "v")
		got := read(t)
		if !strings.Contains(got, "level=INFO") || !strings.Contains(got, "msg=hello k=v") {
			t.Fatalf("redirected output = %q", got)
		}
		if strings.ContainsRune(got, 0x1b) {
			t.Fatalf("redirected output carries an escape sequence: %q", got)
		}
	})

	t.Run("terminal", func(t *testing.T) {
		fakeTerminal(t)
		t.Setenv("NO_COLOR", "")
		h := stderrLogHandler(slog.LevelDebug, true)
		slog.New(h).Info("colored", "k", "v")
		got := read(t)
		if !strings.Contains(got, "\x1b[36mINF\x1b[0m colored") {
			t.Fatalf("terminal output = %q", got)
		}
	})

	t.Run("NO_COLOR", func(t *testing.T) {
		fakeTerminal(t)
		t.Setenv("NO_COLOR", "1")
		h := stderrLogHandler(slog.LevelDebug, true)
		slog.New(h).Info("plaincolor", "k", "v")
		got := read(t)
		if strings.Contains(got, "plaincolor \x1b") || strings.Contains(got, "\x1b[36mINF\x1b[0m plaincolor") {
			t.Fatalf("NO_COLOR output carries an escape sequence: %q", got)
		}
		if !strings.Contains(got, "INF plaincolor") {
			t.Fatalf("NO_COLOR still prints the compact format: %q", got)
		}
	})
}

// On a terminal the wiring puts one handler behind both destinations: the
// file sink is colorless and the terminal sink colored, so the file mirrors
// the console minus every SGR byte. The write order — durable sink first —
// is pinned by pkg/telemetry's TestConsoleLogHandlerWritesDurableSinkFirst.
func TestStderrLogHandlerWiresConsoleSinksOnATerminal(t *testing.T) {
	fakeTerminal(t)
	t.Setenv("NO_COLOR", "")
	_, read := captureStderr(t)
	file, err := os.Create(filepath.Join(t.TempDir(), "mirror.log"))
	if err != nil {
		t.Fatalf("temp mirror: %v", err)
	}
	t.Cleanup(func() { _ = file.Close() })

	slog.New(stderrLogHandlerTo(file, slog.LevelDebug, false)).Info("wired", "k", "v")

	terminal := read(t)
	if !strings.Contains(terminal, "\x1b[36mINF\x1b[0m wired") {
		t.Fatalf("terminal output = %q, want the colored console format", terminal)
	}
	data, err := os.ReadFile(file.Name())
	if err != nil {
		t.Fatalf("read mirror: %v", err)
	}
	if strings.ContainsRune(string(data), 0x1b) {
		t.Fatalf("file sink must carry no escape sequence: %q", string(data))
	}
	if !strings.Contains(string(data), "INF wired k=v") {
		t.Fatalf("file output = %q, want the colorless console format", string(data))
	}
}

// fakeTerminal makes rootIsTerminal report a terminal for one test.
func fakeTerminal(t *testing.T) {
	t.Helper()
	prev := rootIsTerminal
	rootIsTerminal = func(*os.File) bool { return true }
	t.Cleanup(func() { rootIsTerminal = prev })
}

// captureStderr redirects os.Stderr to a temp file for one test and returns a
// reader that returns everything written to it.
func captureStderr(t *testing.T) (restore func(), read func(*testing.T) string) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatalf("temp stderr: %v", err)
	}
	prev := os.Stderr
	os.Stderr = f
	var once sync.Once
	restore = func() { once.Do(func() { os.Stderr = prev }) }
	t.Cleanup(restore)
	read = func(t *testing.T) string {
		t.Helper()
		got, err := os.ReadFile(f.Name())
		if err != nil {
			t.Fatalf("read stderr: %v", err)
		}
		return string(got)
	}
	return restore, read
}

// On a terminal the gateway mirrors its console format into the log file,
// minus every SGR byte.
func TestInstallGatewayConsoleLogMirrorsConsoleFormat(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "logs"), 0o755); err != nil {
		t.Fatalf("mkdir logs: %v", err)
	}
	fakeTerminal(t)
	t.Setenv("NO_COLOR", "")
	_, read := captureStderr(t)
	prev := slog.Default()
	defer slog.SetDefault(prev)

	restore := installGatewayConsoleLog(root)
	slog.Info("gateway started", "port", 6060)
	restore()

	terminal := read(t)
	if !strings.Contains(terminal, "\x1b[36mINF\x1b[0m gateway started") {
		t.Fatalf("terminal output = %q", terminal)
	}
	data, err := os.ReadFile(filepath.Join(root, "logs", gatewayLogFile))
	if err != nil {
		t.Fatalf("read %s: %v", gatewayLogFile, err)
	}
	if strings.ContainsRune(string(data), 0x1b) {
		t.Fatalf("log file must carry no escape sequence: %q", string(data))
	}
	want := stripSGR(terminal)
	if string(data) != want {
		t.Fatalf("log file must mirror the console:\n--- file ---\n%q\n--- console ---\n%q", string(data), want)
	}
	info, err := os.Stat(filepath.Join(root, "logs", gatewayLogFile))
	if err != nil {
		t.Fatalf("stat %s: %v", gatewayLogFile, err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("gateway.log mode = %o, want 0600", got)
	}
	if slog.Default() != prev {
		t.Fatal("restore must put the previous logger back")
	}
}

// Off a terminal — systemd, docker, a redirected descriptor — the gateway
// keeps the logfmt format machine consumers parse, in the file too.
func TestInstallGatewayConsoleLogKeepsLogfmtOffATerminal(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "logs"), 0o755); err != nil {
		t.Fatalf("mkdir logs: %v", err)
	}
	_, read := captureStderr(t)
	prev := slog.Default()
	defer slog.SetDefault(prev)

	restore := installGatewayConsoleLog(root)
	slog.Info("gateway started", "port", 6060)
	restore()

	data, err := os.ReadFile(filepath.Join(root, "logs", gatewayLogFile))
	if err != nil {
		t.Fatalf("read %s: %v", gatewayLogFile, err)
	}
	if !strings.Contains(string(data), "level=INFO") || !strings.Contains(string(data), "msg=\"gateway started\"") {
		t.Fatalf("log file = %q, want logfmt", string(data))
	}
	if !strings.Contains(read(t), "level=INFO") {
		t.Fatalf("stderr = %q, want logfmt", read(t))
	}
}

// stripSGR removes every escape sequence, leaving the text a file should hold.
func stripSGR(s string) string {
	return regexp.MustCompile("\x1b\\[[0-9;]*m").ReplaceAllString(s, "")
}

// A home whose logs directory is missing leaves the terminal output working
// and says so, rather than taking the gateway down.
func TestInstallGatewayConsoleLogSurvivesMissingLogsDir(t *testing.T) {
	root := t.TempDir() // no logs/ subdirectory
	_, read := captureStderr(t)
	prev := slog.Default()
	defer slog.SetDefault(prev)

	restore := installGatewayConsoleLog(root)
	slog.Info("still logging")
	restore()

	if got := read(t); !strings.Contains(got, "still logging") {
		t.Fatalf("terminal output = %q", got)
	}
	if _, err := os.Stat(filepath.Join(root, "logs", gatewayLogFile)); err == nil {
		t.Fatal("no log file should exist when the logs directory is absent")
	}
}
