package main

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/telemetry"
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

// --- the gateway's log wiring ---

// gatewayRecordFiles lists the gateway's record files for one home.
func gatewayRecordFiles(t *testing.T, root string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(root, "logs", "*", "gateway-*.jsonl"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	return files
}

// gatewayRecords decodes every record the gateway wrote for one home.
func gatewayRecords(t *testing.T, root string) []map[string]any {
	t.Helper()
	var records []map[string]any
	for _, path := range gatewayRecordFiles(t, root) {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
			if line == "" {
				continue
			}
			var rec map[string]any
			if err := json.Unmarshal([]byte(line), &rec); err != nil {
				t.Fatalf("line is not JSON: %v\n%s", err, line)
			}
			records = append(records, rec)
		}
	}
	return records
}

// installGateway points the process at one home's gateway log for one test and
// restores everything it touched afterwards.
func installGateway(t *testing.T, root string) func() {
	t.Helper()
	prev := slog.Default()
	prevLog := telemetry.DefaultEventLog()
	t.Cleanup(func() {
		slog.SetDefault(prev)
		telemetry.SetDefaultEventLog(prevLog)
	})
	return installGatewayConsoleLog(root)
}

// On a terminal the gateway keeps its console format on stderr and writes the
// same record to its event log: one JSON line per record, no SGR byte.
func TestInstallGatewayConsoleLogWritesTheEventLog(t *testing.T) {
	root := t.TempDir()
	fakeTerminal(t)
	t.Setenv("NO_COLOR", "")
	_, read := captureStderr(t)

	restore := installGateway(t, root)
	slog.Info("gateway started", "port", 6060)
	restore()

	terminal := read(t)
	if !strings.Contains(terminal, "\x1b[36mINF\x1b[0m gateway started") {
		t.Fatalf("terminal output = %q", terminal)
	}
	records := gatewayRecords(t, root)
	if len(records) != 1 {
		t.Fatalf("records = %v, want one", records)
	}
	rec := records[0]
	if rec["msg"] != "gateway started" || rec["lvl"] != "INFO" || rec["kind"] != "log" {
		t.Fatalf("record = %v", rec)
	}
	if attrs, _ := rec["attrs"].(map[string]any); attrs["port"] != float64(6060) {
		t.Fatalf("record attrs = %v, want the console's own attributes", rec["attrs"])
	}
	for _, path := range gatewayRecordFiles(t, root) {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if strings.ContainsRune(string(data), 0x1b) {
			t.Fatalf("%s must carry no escape sequence: %q", path, string(data))
		}
	}
}

// Off a terminal — systemd, docker, a redirected descriptor — the gateway
// keeps the logfmt format machine consumers parse, and the event log gets the
// same record.
func TestInstallGatewayConsoleLogKeepsLogfmtOffATerminal(t *testing.T) {
	root := t.TempDir()
	_, read := captureStderr(t)

	restore := installGateway(t, root)
	slog.Info("gateway started", "port", 6060)
	restore()

	if got := read(t); !strings.Contains(got, "level=INFO") || !strings.Contains(got, `msg="gateway started"`) {
		t.Fatalf("stderr = %q, want logfmt", got)
	}
	records := gatewayRecords(t, root)
	if len(records) != 1 || records[0]["msg"] != "gateway started" {
		t.Fatalf("records = %v, want the same record in the event log", records)
	}
}

// A home with no logs directory is not a reason to lose the terminal output or
// the start: the event log creates its directory with the first record.
func TestInstallGatewayConsoleLogSurvivesMissingLogsDir(t *testing.T) {
	root := t.TempDir() // no logs/ subdirectory
	_, read := captureStderr(t)

	restore := installGateway(t, root)
	slog.Info("still logging")
	restore()

	if got := read(t); !strings.Contains(got, "still logging") {
		t.Fatalf("terminal output = %q", got)
	}
	if records := gatewayRecords(t, root); len(records) != 1 {
		t.Fatalf("records = %v, want the record written into the new directory", records)
	}
}

// The gateway's log file name is the event log's: source, host, pid and a
// per-process suffix, so two replicas — or two starts — never share a file.
func TestGatewayLogFileNamesTheProcess(t *testing.T) {
	root := t.TempDir()
	restore := installGateway(t, root)
	slog.Info("first")
	restore()

	files := gatewayRecordFiles(t, root)
	if len(files) != 1 {
		t.Fatalf("files = %v, want one", files)
	}
	base := filepath.Base(files[0])
	want := "gateway-" + telemetry.HostLabel() + "-"
	if !strings.HasPrefix(base, want) || !regexp.MustCompile(`-\d+-[0-9a-f]{6}\.jsonl$`).MatchString(base) {
		t.Fatalf("log file = %q, want %s<pid>-<proc>.jsonl", base, want)
	}
}

// A record's file lives in the day directory of its own time, so `ls logs`
// answers "what happened today" without opening anything.
func TestGatewayRecordsLandInTodaysDirectory(t *testing.T) {
	root := t.TempDir()
	restore := installGateway(t, root)
	slog.Info("dated")
	restore()

	day := time.Now().Format("2006-01-02")
	if _, err := os.Stat(filepath.Join(root, "logs", day)); err != nil {
		t.Fatalf("day directory: %v", err)
	}
}
