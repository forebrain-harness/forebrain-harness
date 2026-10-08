package telemetry

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

var consoleTestTime = time.Date(2026, 10, 8, 16, 40, 5, 131_000_000, time.Local)

// consoleRecord builds a record with a frozen timestamp and no source, so a
// golden line is byte-stable.
func consoleRecord(level slog.Level, msg string, attrs ...slog.Attr) slog.Record {
	r := slog.NewRecord(consoleTestTime, level, msg, 0)
	r.AddAttrs(attrs...)
	return r
}

func renderConsole(t *testing.T, color bool, r slog.Record) string {
	t.Helper()
	var buf bytes.Buffer
	h := NewConsoleHandler(slog.LevelDebug, false, ConsoleSink{W: &buf, Color: color})
	if err := h.Handle(context.Background(), r); err != nil {
		t.Fatalf("handle: %v", err)
	}
	return buf.String()
}

// The colored line is the format an operator reads, asserted to the byte:
// dim time, three-character level tag, plain message, dim keys, semantic
// values.
func TestConsoleLogHandlerColorGolden(t *testing.T) {
	got := renderConsole(t, true, consoleRecord(slog.LevelInfo, "agent load",
		slog.Int("skills", 18),
		slog.String("mcp_generation", "9f3c2a71"),
		slog.Bool("mcp_pending", true),
	))
	want := "\x1b[90m16:40:05.131\x1b[0m \x1b[36mINF\x1b[0m agent load " +
		"\x1b[90mskills=\x1b[0m18 \x1b[90mmcp_generation=\x1b[0m9f3c2a71 \x1b[90mmcp_pending=\x1b[0mtrue\n"
	if got != want {
		t.Fatalf("line mismatch:\n--- got ---\n%q\n--- want ---\n%q", got, want)
	}
}

// The same record without color is what the log file receives: identical
// text, zero SGR bytes.
func TestConsoleLogHandlerPlainGolden(t *testing.T) {
	got := renderConsole(t, false, consoleRecord(slog.LevelInfo, "agent load",
		slog.Int("skills", 18),
		slog.String("mcp_generation", "9f3c2a71"),
		slog.Bool("mcp_pending", true),
	))
	want := "16:40:05.131 INF agent load skills=18 mcp_generation=9f3c2a71 mcp_pending=true\n"
	if got != want {
		t.Fatalf("line mismatch:\n--- got ---\n%q\n--- want ---\n%q", got, want)
	}
	if strings.ContainsRune(got, 0x1b) {
		t.Fatalf("plain line carries an escape sequence: %q", got)
	}
}

func TestConsoleLogHandlerLevelColors(t *testing.T) {
	tests := []struct {
		name  string
		level slog.Level
		want  string
	}{
		{"debug", slog.LevelDebug, "\x1b[90mDBG\x1b[0m"},
		{"info", slog.LevelInfo, "\x1b[36mINF\x1b[0m"},
		{"warn", slog.LevelWarn, "\x1b[33mWRN\x1b[0m"},
		{"error", slog.LevelError, "\x1b[1;31mERR\x1b[0m"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := renderConsole(t, true, consoleRecord(tc.level, "message"))
			if !strings.Contains(got, tc.want) {
				t.Fatalf("line = %q, want the tag %q", got, tc.want)
			}
		})
	}
}

// An error record's message is red like its level, and an err value is red
// whatever the record's own level.
func TestConsoleLogHandlerErrorValuesAreRed(t *testing.T) {
	got := renderConsole(t, true, consoleRecord(slog.LevelError, "channel outbound",
		slog.Any("err", errors.New("context deadline exceeded")),
	))
	want := "\x1b[90m16:40:05.131\x1b[0m \x1b[1;31mERR\x1b[0m \x1b[31mchannel outbound\x1b[0m " +
		"\x1b[90merr=\x1b[0m\x1b[31m\"context deadline exceeded\"\x1b[0m\n"
	if got != want {
		t.Fatalf("line mismatch:\n--- got ---\n%q\n--- want ---\n%q", got, want)
	}

	warn := renderConsole(t, true, consoleRecord(slog.LevelWarn, "mcp disable store unreadable",
		slog.Any("err", errors.New("open /Users/doudou/.forebrain/mcp_disable.json: no such file")),
	))
	if !strings.Contains(warn, "\x1b[31m\"open /Users/doudou/.forebrain/mcp_disable.json: no such file\"\x1b[0m") {
		t.Fatalf("warn err value must be red and quoted:\n%q", warn)
	}
}

// The color policy keys off the attribute name, so any log source benefits:
// status by response class, duration by slowness, bytes humanized.
func TestConsoleLogHandlerValuePolicyByKey(t *testing.T) {
	tests := []struct {
		name string
		attr slog.Attr
		want string
	}{
		{"2xx green", slog.Int("status", 200), "\x1b[32m200\x1b[0m"},
		{"1xx cyan", slog.Int("status", 101), "\x1b[36m101\x1b[0m"},
		{"3xx cyan", slog.Int("status", 302), "\x1b[36m302\x1b[0m"},
		{"4xx yellow", slog.Int("status", 404), "\x1b[33m404\x1b[0m"},
		{"5xx red", slog.Int("status", 503), "\x1b[1;31m503\x1b[0m"},
		{"fast duration uncolored", slog.String("duration", "3.4ms"), "3.4ms"},
		{"slow duration yellow", slog.String("duration", "240ms"), "\x1b[33m240ms\x1b[0m"},
		{"very slow duration red", slog.String("duration", "1.62s"), "\x1b[31m1.62s\x1b[0m"},
		{"bytes humanized", slog.Int64("bytes", 8420), "8.2KB"},
		{"small bytes humanized", slog.Int64("bytes", 156), "156B"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := renderConsole(t, true, consoleRecord(slog.LevelInfo, "m", tc.attr))
			if !strings.Contains(got, tc.want) {
				t.Fatalf("line = %q, want it to contain %q", got, tc.want)
			}
		})
	}
}

// logfmt quoting keeps a value with spaces, quotes or '=' on one line.
func TestConsoleLogHandlerQuotesValuesThatNeedIt(t *testing.T) {
	got := renderConsole(t, false, consoleRecord(slog.LevelInfo, "request",
		slog.String("path", "/a b"),
		slog.String("plain", "value"),
	))
	if !strings.Contains(got, `path="/a b"`) {
		t.Fatalf("value with a space must be quoted: %q", got)
	}
	if !strings.Contains(got, "plain=value") {
		t.Fatalf("a plain value must not be quoted: %q", got)
	}
}

// redactedToken is a LogValuer deciding its own rendering: what the log shows
// is its LogValue, never the struct.
type redactedToken struct{}

func (redactedToken) LogValue() slog.Value { return slog.StringValue("redacted") }

// A LogValuer resolves at render time on every path: as a record attribute
// and as one bound with With, matching what the stdlib handlers do.
func TestConsoleLogHandlerResolvesLogValuer(t *testing.T) {
	got := renderConsole(t, false, consoleRecord(slog.LevelInfo, "token accepted",
		slog.Any("token", redactedToken{}),
	))
	if !strings.Contains(got, "token=redacted") {
		t.Fatalf("record attribute must resolve through LogValue: %q", got)
	}
	if strings.Contains(got, "&{") {
		t.Fatalf("an unresolved LogValuer leaks the struct: %q", got)
	}

	var buf bytes.Buffer
	logger := slog.New(NewConsoleHandler(slog.LevelDebug, false, ConsoleSink{W: &buf}))
	logger.With("k", redactedToken{}).Info("m")
	if !strings.Contains(buf.String(), "k=redacted") {
		t.Fatalf("bound attribute must resolve through LogValue: %q", buf.String())
	}
}

// A message carrying a control character stays one physical line — escaped,
// so the record can still be grepped and parsed back.
func TestConsoleLogHandlerKeepsOneLinePerRecord(t *testing.T) {
	tests := []struct {
		name string
		msg  string
		esc  string
	}{
		{"newline", "request failed\nbody unread", `\n`},
		{"crlf", "crashed\r\nhard", `\r`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := renderConsole(t, false, consoleRecord(slog.LevelError, tc.msg))
			if n := strings.Count(got, "\n"); n != 1 {
				t.Fatalf("message %q rendered as %d lines: %q", tc.msg, n, got)
			}
			if !strings.Contains(got, tc.esc) {
				t.Fatalf("control character must be escaped: %q", got)
			}
		})
	}
}

// The message position is prose, not a key=value value: spaces stay bare, and
// quoting is reserved for control characters.
func TestConsoleLogHandlerDoesNotQuoteProseMessages(t *testing.T) {
	got := renderConsole(t, false, consoleRecord(slog.LevelInfo, "agent load"))
	if !strings.Contains(got, "INF agent load\n") {
		t.Fatalf("prose with spaces must render unquoted: %q", got)
	}
	got = renderConsole(t, false, consoleRecord(slog.LevelInfo, "agent\tload"))
	if !strings.Contains(got, `"agent\tload"`) {
		t.Fatalf("a message with a control character must be quoted: %q", got)
	}
}

// WithAttrs binds context onto the line, and a group is a dotted prefix —
// the group open when an attribute was bound, not the one open at log time.
func TestConsoleLogHandlerWithAttrsAndGroup(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(NewConsoleHandler(slog.LevelDebug, false, ConsoleSink{W: &buf})).
		With("session", "s-8f21").
		WithGroup("mcp").
		With("server", "codegraph")
	logger.Info("agent tools", "connected", 3)

	got := buf.String()
	if !strings.Contains(got, "agent tools session=s-8f21 mcp.server=codegraph mcp.connected=3") {
		t.Fatalf("line = %q", got)
	}
}

func TestConsoleLogHandlerRespectsLevel(t *testing.T) {
	h := NewConsoleHandler(slog.LevelWarn, false, ConsoleSink{W: &bytes.Buffer{}})
	if h.Enabled(context.Background(), slog.LevelInfo) {
		t.Fatal("INFO must be filtered out above a warn threshold")
	}
	if !h.Enabled(context.Background(), slog.LevelError) {
		t.Fatal("ERROR must pass a warn threshold")
	}
}

// One handler serves the terminal and the log file at once: the file's sink
// gets the same text without a single SGR byte.
func TestConsoleLogHandlerMultipleSinks(t *testing.T) {
	var term, file bytes.Buffer
	h := NewConsoleHandler(slog.LevelDebug, false,
		ConsoleSink{W: &term, Color: true},
		ConsoleSink{W: &file},
	)
	if err := h.Handle(context.Background(), consoleRecord(slog.LevelInfo, "request", slog.Int("status", 200))); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if !strings.Contains(term.String(), "\x1b[32m200\x1b[0m") {
		t.Fatalf("terminal sink must be colored: %q", term.String())
	}
	if strings.ContainsRune(file.String(), 0x1b) {
		t.Fatalf("file sink must carry no escape sequence: %q", file.String())
	}
	if strings.TrimPrefix(term.String(), "\x1b") == "" || file.String() == "" {
		t.Fatalf("both sinks must receive the line: term=%q file=%q", term.String(), file.String())
	}
}

// A dead first destination must not cost the remaining one its record: every
// sink is attempted, and the failures come back joined for whoever inspects
// them (slog itself discards the error).
func TestConsoleLogHandlerWritesEverySinkWhenOneFails(t *testing.T) {
	var file bytes.Buffer
	h := NewConsoleHandler(slog.LevelDebug, false,
		ConsoleSink{W: writerFunc(func([]byte) (int, error) { return 0, errors.New("broken pipe") })},
		ConsoleSink{W: &file},
	)
	if err := h.Handle(context.Background(), consoleRecord(slog.LevelInfo, "request")); err == nil {
		t.Fatal("Handle must report the failed sink")
	}
	if !strings.Contains(file.String(), "INF request") {
		t.Fatalf("the healthy sink must still receive the line: %q", file.String())
	}
}

// The durable destination is attempted before the terminal one: if the
// process dies between the two writes, the record meant to outlive the
// session is the one already on disk. The wiring is pinned too — the file
// sink is colorless, the terminal sink colored, so their order in the slice
// is observable.
func TestConsoleLogHandlerWritesDurableSinkFirst(t *testing.T) {
	var order []string
	sink := func(name string) ConsoleSink {
		return ConsoleSink{W: writerFunc(func(p []byte) (int, error) {
			order = append(order, name)
			return len(p), nil
		})}
	}
	h := NewConsoleHandler(slog.LevelDebug, false, sink("durable"), sink("terminal"))
	if err := h.Handle(context.Background(), consoleRecord(slog.LevelInfo, "ordered")); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if len(order) != 2 || order[0] != "durable" || order[1] != "terminal" {
		t.Fatalf("write order = %v, want the durable sink first", order)
	}
}

// The fan-out writer keeps writing past a failure — the stdlib MultiWriter's
// documented stop-at-first-error is the exact behaviour this replaces on the
// logfmt path.
func TestMultiWriterKeepsGoingAfterAFailure(t *testing.T) {
	var file bytes.Buffer
	w := MultiWriter(
		writerFunc(func([]byte) (int, error) { return 0, errors.New("eio") }),
		&file,
	)
	n, err := w.Write([]byte("gateway started\n"))
	if err == nil {
		t.Fatal("the fan-out must report the failing writer")
	}
	if n != len("gateway started\n") {
		t.Fatalf("n = %d, want the full payload length", n)
	}
	if file.String() != "gateway started\n" {
		t.Fatalf("the healthy writer must receive the payload: %q", file.String())
	}
}

// errors.Join of no failures is nil: healthy sinks keep Handle's contract of
// returning nil.
func TestConsoleLogHandlerHandleReturnsNoErrorOnSuccess(t *testing.T) {
	h := NewConsoleHandler(slog.LevelDebug, false,
		ConsoleSink{W: &bytes.Buffer{}},
		ConsoleSink{W: &bytes.Buffer{}},
	)
	if err := h.Handle(context.Background(), consoleRecord(slog.LevelInfo, "healthy")); err != nil {
		t.Fatalf("handle: %v", err)
	}
}

// The source is shortened to basename:line at the end of the line, and the
// full path never reaches the terminal.
func TestConsoleLogHandlerShortensSource(t *testing.T) {
	r := consoleRecordWithSource(t, slog.LevelInfo, "with source")
	var buf bytes.Buffer
	h := NewConsoleHandler(slog.LevelDebug, true, ConsoleSink{W: &buf, Color: true})
	if err := h.Handle(context.Background(), r); err != nil {
		t.Fatalf("handle: %v", err)
	}
	got := buf.String()
	_, source, ok := strings.Cut(got, "· ")
	if !ok {
		t.Fatalf("line = %q, want a trailing source", got)
	}
	if !regexp.MustCompile(`^console_log_test\.go:\d+`).MatchString(source) {
		t.Fatalf("source = %q, want the basename and line", source)
	}
	if strings.Contains(strings.TrimSuffix(source, "\n"), "/") {
		t.Fatalf("source = %q, want no directory", source)
	}
	if !strings.HasSuffix(got, "\x1b[0m\n") {
		t.Fatalf("the dimmed source must be the last token: %q", got)
	}
}

// consoleRecordWithSource captures a real program counter, so the record
// carries the source location slog would report for a real call site.
func consoleRecordWithSource(t *testing.T, level slog.Level, msg string) slog.Record {
	t.Helper()
	var pcs [1]uintptr
	runtime.Callers(1, pcs[:])
	return slog.NewRecord(consoleTestTime, level, msg, pcs[0])
}

// Many goroutines through one handler must not interleave lines: every record
// arrives whole. Run with -race to also cover the shared write lock.
func TestConsoleLogHandlerConcurrentWrites(t *testing.T) {
	var mu sync.Mutex
	var buf bytes.Buffer
	h := NewConsoleHandler(slog.LevelDebug, false, ConsoleSink{W: writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return buf.Write(p)
	})})
	logger := slog.New(h)

	const goroutines, each = 16, 64
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			child := logger.With("worker", g)
			for i := 0; i < each; i++ {
				child.Info("work", "i", i)
			}
		}(g)
	}
	wg.Wait()

	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) != goroutines*each {
		t.Fatalf("lines = %d, want %d", len(lines), goroutines*each)
	}
	whole := regexp.MustCompile(`^\d{2}:\d{2}:\d{2}\.\d{3} INF work worker=\d+ i=\d+$`)
	for _, line := range lines {
		if !whole.MatchString(line) {
			t.Fatalf("interleaved or malformed line: %q", line)
		}
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
