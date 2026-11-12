package telemetry

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestLogPanicRecovered(t *testing.T) {
	root := withEventLog(t)
	Log(root, "nil", nil)
	Log(root, "string panic", "boom")
	Log(root, "error panic", errors.New("broken"))

	records := recordsRead(t, root)
	if len(records) != 2 {
		t.Fatalf("records = %+v, want one per non-nil panic", records)
	}
	for i, want := range []struct{ msg, err string }{
		{"recovered: string panic", "boom"},
		{"recovered: error panic", "broken"},
	} {
		rec := records[i]
		if rec.Kind != "panic" || rec.Level != "ERROR" || rec.Msg != want.msg {
			t.Fatalf("record %d = %+v, want an ERROR panic record for %q", i, rec, want.msg)
		}
		if rec.Err != want.err {
			t.Fatalf("record %d err = %q, want %q", i, rec.Err, want.err)
		}
		stack, _ := rec.Attrs["stack"].(string)
		if !strings.Contains(stack, "goroutine") || !strings.Contains(stack, "runtime/debug.Stack") {
			t.Fatalf("record %d must carry the recovered stack, got %q", i, stack)
		}
		if rec.At == "" || strings.HasPrefix(rec.At, "?") {
			t.Fatalf("record %d must name where the panic was recovered, got %q", i, rec.At)
		}
	}
}

// errorSkipFromWrapper is one frame above ErrorSkip, the position a real
// caller sits at: the location a record carries must then be the caller's, not
// this helper's.
func errorSkipFromWrapper(root string, err error) {
	ErrorSkip(root, err, "turn dispatch", 0)
}

// The location a record carries is the code that failed, not the helper that
// wrote it down: a reader searches the file it names for the bug.
func TestErrorSkipRecordsTheCallSite(t *testing.T) {
	root := withEventLog(t)
	errorSkipFromWrapper(root, fmt.Errorf("wrapped: %w", errors.New("base")))

	rec := onlyRecord(t, root)
	if rec.Kind != "log" || rec.Level != "ERROR" || rec.Msg != "turn dispatch" {
		t.Fatalf("record = %+v, want an ERROR log record named for its context", rec)
	}
	if rec.Err != "wrapped: base" {
		t.Fatalf("err = %q, want the error's own text", rec.Err)
	}
	chain, ok := rec.Attrs["err_chain"].([]any)
	if !ok || len(chain) != 2 {
		t.Fatalf("err_chain = %v, want the two layers this error has", rec.Attrs["err_chain"])
	}
	if !strings.HasPrefix(rec.At, "pkg/telemetry/paniclog_test.go:") {
		t.Fatalf("at = %q, want this test file's line", rec.At)
	}
}

// A blank context names the record "error" rather than leaving it unnamed.
func TestErrorSkipNamesABlankContext(t *testing.T) {
	root := withEventLog(t)
	ErrorSkip(root, errors.New("boom"), "  ", 0)
	if rec := onlyRecord(t, root); rec.Msg != "error" {
		t.Fatalf("msg = %q, want error", rec.Msg)
	}
}

// ErrorThenFprintln prints the error and records it once; a nil error does
// neither.
func TestErrorThenFprintlnRecordsAndPrints(t *testing.T) {
	root := withEventLog(t)
	err := errors.New("boom")
	var out strings.Builder
	ErrorThenFprintln(root, &out, err, "command exit")
	if strings.TrimSpace(out.String()) != err.Error() {
		t.Fatalf("printed %q, want the error", out.String())
	}
	if rec := onlyRecord(t, root); rec.Msg != "command exit" || rec.Err != "boom" {
		t.Fatalf("record = %+v", rec)
	}

	before := len(recordsRead(t, root))
	ErrorThenFprintln(root, &out, nil, "ignored")
	ErrorSkip(root, nil, "ignored", 0)
	if after := len(recordsRead(t, root)); after != before {
		t.Fatalf("a nil error wrote %d record(s)", after-before)
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
	t.Setenv("FOREBRAIN_HOME", root)
	if got := resolveRoot(""); got != root {
		t.Fatalf("env resolveRoot=%q", got)
	}

	origRoot := datadirRoot
	t.Cleanup(func() { datadirRoot = origRoot })
	datadirRoot = func() (string, error) {
		return "", errors.New("no home")
	}
	if got := resolveRoot(""); got != "" {
		t.Fatalf("resolveRoot failure=%q", got)
	}
}

// A caller frame that cannot be read still names the record, with the "?"
// fallbacks, rather than losing it.
func TestInjectedRuntimeFailureKeepsTheRecord(t *testing.T) {
	root := withEventLog(t)
	origCaller := runtimeCaller
	t.Cleanup(func() { runtimeCaller = origCaller })
	runtimeCaller = func(int) (uintptr, string, int, bool) {
		return 0, "", 0, false
	}

	ErrorSkip(root, errors.New("caller failed"), "caller", 0)
	Log(root, "caller-panic", "panic")

	records := recordsRead(t, root)
	if len(records) != 2 {
		t.Fatalf("records = %+v, want both", records)
	}
	for _, rec := range records {
		if rec.At != "?:0" {
			t.Fatalf("at = %q, want the ?:0 fallback", rec.At)
		}
	}
}

// A negative extraFrames is clamped instead of producing a nonsense skip.
func TestErrorSkipClampsNegativeFrames(t *testing.T) {
	root := withEventLog(t)
	ErrorSkip(root, errors.New("boom"), "clamped", -3)
	if rec := onlyRecord(t, root); rec.Err != "boom" {
		t.Fatalf("record = %+v", rec)
	}
}
