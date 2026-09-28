package telemetry

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/home"
)

func TestLogPanicRecovered(t *testing.T) {
	root := t.TempDir()
	Log(root, "nil", nil)
	Log(root, "string panic", "boom")
	Log(root, "error panic", errors.New("broken"))

	logText := readErrorLog(t, root)
	for _, want := range []string{
		"=== [PANIC-RECOVER]",
		"string panic",
		"error panic",
		"%#v: \"boom\"",
		"Error(): broken",
		"debug.Stack():",
	} {
		if !strings.Contains(logText, want) {
			t.Fatalf("panic log missing %q in:\n%s", want, logText)
		}
	}
}

func TestHelpersAndAppendErrors(t *testing.T) {
	if functionNameForPC(0) != "?" {
		t.Fatal("zero pc should return unknown function")
	}
	pc, _, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime caller failed")
	}
	if got := functionNameForPC(pc); !strings.Contains(got, "TestHelpersAndAppendErrors") {
		t.Fatalf("function name=%q", got)
	}
	if got := functionNameForPC(^uintptr(0)); got != "?" {
		t.Fatalf("invalid function name=%q", got)
	}
	if formatErrorDetails(nil) != "" {
		t.Fatal("nil error details should be empty")
	}
	if err := writeErrorEntry(t.TempDir(), nil, "", "f", 1, "fn"); err != nil {
		t.Fatalf("nil writeErrorEntry error: %v", err)
	}
	if err := appendErrorFile(filepath.Join(t.TempDir(), "error.log"), nil, "", "f", 1, "fn"); err != nil {
		t.Fatalf("nil appendErrorFile error: %v", err)
	}
	if err := appendFile(filepath.Join(t.TempDir(), "error.log"), "label", "panic", "f", 1, "fn"); err != nil {
		t.Fatalf("appendFile error: %v", err)
	}

	blockingRoot := filepath.Join(t.TempDir(), "file-root")
	if err := os.WriteFile(blockingRoot, []byte("x"), 0o644); err != nil {
		t.Fatalf("write blocking root: %v", err)
	}
	if err := writeErrorEntry(blockingRoot, errors.New("x"), "", "f", 1, "fn"); err == nil {
		t.Fatal("expected writeErrorEntry mkdir error")
	}
	if err := appendFile(filepath.Join(blockingRoot, "child", "error.log"), "label", "panic", "f", 1, "fn"); err == nil {
		t.Fatal("expected appendFile mkdir error")
	}

	dirPath := filepath.Join(t.TempDir(), "logs", "error.log")
	if err := os.MkdirAll(dirPath, 0o755); err != nil {
		t.Fatalf("mkdir dir path: %v", err)
	}
	if err := appendErrorFile(dirPath, errors.New("x"), "", "f", 1, "fn"); err == nil {
		t.Fatal("expected appendErrorFile open error")
	}
	if err := appendFile(dirPath, "label", "panic", "f", 1, "fn"); err == nil {
		t.Fatal("expected appendFile open error")
	}
}

func deepErrorSkip(root string, err error) {
	ErrorSkip(root, err, "deep", 10000)
}

func deepLogSkip(root string, r any) {
	appendPCFallback(root, r, 10000)
}

func appendPCFallback(root string, r any, skip int) {
	pc, f, l, ok := runtime.Caller(skip)
	if !ok {
		f, l = "?", 0
		pc = 0
	}
	_ = appendFile(filepath.Join(root, home.LogsDir, "error.log"), "deep-panic", r, f, l, functionNameForPC(pc))
}

func TestSlogTeeHandler(t *testing.T) {
	root := t.TempDir()
	inner := &recordingHandler{enabled: true, handleErr: errors.New("inner failed")}
	h := NewSlogTeeHandler(root, inner)

	if !h.Enabled(context.Background(), slog.LevelInfo) {
		t.Fatal("handler should be enabled")
	}
	_ = h.WithAttrs([]slog.Attr{slog.String("component", "test")})
	_ = h.WithGroup("group")

	info := slog.NewRecord(time.Now(), slog.LevelInfo, "info message", 0)
	if err := h.Handle(context.Background(), info); !errors.Is(err, inner.handleErr) {
		t.Fatalf("info handle err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, home.LogsDir, "error.log")); !os.IsNotExist(err) {
		t.Fatalf("info event should not create error log, stat err=%v", err)
	}

	pc, _, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime caller failed")
	}
	rec := slog.NewRecord(time.Now(), slog.LevelError, "error message", pc)
	rec.AddAttrs(slog.Any("err", fmt.Errorf("wrapped: %w", errors.New("root"))), slog.String("plain", "value"))
	if err := h.Handle(context.Background(), rec); !errors.Is(err, inner.handleErr) {
		t.Fatalf("error handle err=%v", err)
	}
	logText := readErrorLog(t, root)
	for _, want := range []string{
		"=== [SLOG ERROR]",
		"error message",
		"attr err:",
		"unwrap[1]: root",
		"attr plain",
	} {
		if !strings.Contains(logText, want) {
			t.Fatalf("slog log missing %q in:\n%s", want, logText)
		}
	}

	emptyRoot := &teeHandler{root: "", inner: inner}
	if err := emptyRoot.Handle(context.Background(), rec); !errors.Is(err, inner.handleErr) {
		t.Fatalf("empty root handle err=%v", err)
	}
}

func TestSlogTeeHandlerWriteFailuresReturnInnerError(t *testing.T) {
	inner := &recordingHandler{enabled: true, handleErr: errors.New("inner failed")}
	rec := slog.NewRecord(time.Now(), slog.LevelError, "error message", 0)

	blockingRoot := filepath.Join(t.TempDir(), "file-root")
	if err := os.WriteFile(blockingRoot, []byte("x"), 0o644); err != nil {
		t.Fatalf("write blocking root: %v", err)
	}
	if err := (&teeHandler{root: blockingRoot, inner: inner}).Handle(context.Background(), rec); !errors.Is(err, inner.handleErr) {
		t.Fatalf("mkdir failure err=%v", err)
	}

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, home.LogsDir, "error.log"), 0o755); err != nil {
		t.Fatalf("mkdir blocking log path: %v", err)
	}
	if err := (&teeHandler{root: root, inner: inner}).Handle(context.Background(), rec); !errors.Is(err, inner.handleErr) {
		t.Fatalf("open failure err=%v", err)
	}
}

func TestSlogTeeHandlerDefaultRootAndSource(t *testing.T) {
	root := t.TempDir()
	t.Setenv("FOREBRAIN_HOME", root)
	inner := &recordingHandler{enabled: true}
	h := NewSlogTeeHandler("", inner)

	rec := slog.NewRecord(time.Now(), slog.LevelError, "source message", 0)
	rec.PC = 1
	if err := h.Handle(context.Background(), rec); err != nil {
		t.Fatalf("handle source record: %v", err)
	}
	logText := readErrorLog(t, root)
	if !strings.Contains(logText, "source message") {
		t.Fatalf("default source log missing:\n%s", logText)
	}
}

type recordingHandler struct {
	enabled   bool
	handleErr error
}

func (h *recordingHandler) Enabled(context.Context, slog.Level) bool {
	return h.enabled
}

func (h *recordingHandler) Handle(context.Context, slog.Record) error {
	return h.handleErr
}

func (h *recordingHandler) WithAttrs([]slog.Attr) slog.Handler {
	return h
}

func (h *recordingHandler) WithGroup(string) slog.Handler {
	return h
}

func readErrorLog(t *testing.T, root string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, home.LogsDir, "error.log"))
	if err != nil {
		t.Fatalf("read error log: %v", err)
	}
	return string(data)
}

// Error and ErrorThenFprintf were production wrappers over ErrorSkip that
// nothing called. They stay here, verbatim, so the tests that exercise the
// live write path (ErrorSkip, resolveRoot, writeErrorEntry) keep their
// caller frame depth.

func Error(forebrainRoot string, err error, context string) {
	if err == nil {
		return
	}
	pc, file, line, ok := runtimeCaller(2)
	if !ok {
		file, line = "?", 0
		pc = 0
	}
	_ = writeErrorEntry(forebrainRoot, err, context, file, line, functionNameForPC(pc))
}

func ErrorThenFprintf(forebrainRoot string, w io.Writer, err error, context string, format string, args ...any) {
	if err == nil {
		return
	}
	ErrorSkip(forebrainRoot, err, context, 1)
	_, _ = fmt.Fprintf(w, format, args...)
}

func TestErrorLogging(t *testing.T) {
	root := t.TempDir()
	base := errors.New("base")
	err := fmt.Errorf("wrapped: %w", base)

	Error(root, nil, "ignored")
	Error(root, err, "context-a")
	ErrorSkip(root, err, "context-b", -1)

	var out bytes.Buffer
	ErrorThenFprintf(root, &out, err, "context-c", "formatted %s", "message")
	if out.String() != "formatted message" {
		t.Fatalf("fprintf output=%q", out.String())
	}
	out.Reset()
	ErrorThenFprintln(root, &out, err, "context-d")
	if strings.TrimSpace(out.String()) != err.Error() {
		t.Fatalf("fprintln output=%q", out.String())
	}
	ErrorThenFprintf(root, &out, nil, "ignored", "x")
	ErrorThenFprintln(root, &out, nil, "ignored")

	logText := readErrorLog(t, root)
	for _, want := range []string{
		"=== [ERROR]",
		"context: context-a",
		"context: context-b",
		"context: context-c",
		"context: context-d",
		"%v: wrapped: base",
		"unwrap[1]: base",
		"function:",
	} {
		if !strings.Contains(logText, want) {
			t.Fatalf("error log missing %q in:\n%s", want, logText)
		}
	}
}

func TestResolveRootAndCallerFallbacks(t *testing.T) {
	root := t.TempDir()
	if got := resolveRoot(root); got != root {
		t.Fatalf("resolveRoot=%q", got)
	}
	if got := resolveRoot("  " + root + "  "); got != root {
		t.Fatalf("trimmed resolveRoot=%q", got)
	}

	deepErrorSkip(root, errors.New("deep"))
	logText := readErrorLog(t, root)
	if !strings.Contains(logText, "location: ?:0") || !strings.Contains(logText, "function: ?") {
		t.Fatalf("caller fallback not logged:\n%s", logText)
	}

	t.Setenv("FOREBRAIN_HOME", root)
	if got := resolveRoot(""); got != root {
		t.Fatalf("env resolveRoot=%q", got)
	}
	Error("", errors.New("default-root-error"), "default-root")
	Log("", "default-root-panic", "panic")
	logText = readErrorLog(t, root)
	if !strings.Contains(logText, "default-root-error") || !strings.Contains(logText, "default-root-panic") {
		t.Fatalf("default root log missing entries:\n%s", logText)
	}
	deepLogSkip(root, "deep-panic")
	logText = readErrorLog(t, root)
	if !strings.Contains(logText, "deep-panic") || !strings.Contains(logText, "location: ?:0") {
		t.Fatalf("deep log fallback not logged:\n%s", logText)
	}
}

func TestInjectedRuntimeAndRootFailures(t *testing.T) {
	root := t.TempDir()
	origCaller := runtimeCaller
	origRoot := datadirRoot
	t.Cleanup(func() {
		runtimeCaller = origCaller
		datadirRoot = origRoot
	})

	runtimeCaller = func(skip int) (uintptr, string, int, bool) {
		return 0, "", 0, false
	}
	Error(root, errors.New("caller failed"), "caller")
	Log(root, "caller-panic", "panic")
	logText := readErrorLog(t, root)
	if strings.Count(logText, "location: ?:0") < 2 {
		t.Fatalf("caller fallback log missing:\n%s", logText)
	}

	var out bytes.Buffer
	ErrorSkip(root, nil, "ignored", 0)
	ErrorThenFprintf(root, &out, nil, "ignored", "x")
	ErrorThenFprintln(root, &out, nil, "ignored")
	if out.String() != "" {
		t.Fatalf("nil error helpers wrote %q", out.String())
	}

	datadirRoot = func() (string, error) {
		return "", errors.New("no home")
	}
	if got := resolveRoot(""); got != "" {
		t.Fatalf("resolveRoot failure=%q", got)
	}
	Log("", "no-root", "panic")
}
