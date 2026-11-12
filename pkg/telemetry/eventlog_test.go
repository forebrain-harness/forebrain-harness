package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/home"
)

// newTestLog opens one event log in a throwaway home and closes it after the test.
func newTestLog(t *testing.T, source string) (*EventLog, string) {
	t.Helper()
	root := t.TempDir()
	log := OpenEventLog(root, source)
	t.Cleanup(func() { _ = log.Close() })
	return log, root
}

// dayFiles lists the record files of one day directory, oldest part first.
func dayFiles(t *testing.T, root, day string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(root, home.LogsDir, day, "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// onlyFile is dayFiles for the tests that expect a single record file.
func onlyFile(t *testing.T, files []string) string {
	t.Helper()
	if len(files) != 1 {
		t.Fatalf("want exactly one record file, got %v", files)
	}
	return files[0]
}

// dayDirs lists the day directories under one logs directory, oldest first.
func dayDirs(t *testing.T, logsDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(logsDir)
	if err != nil {
		t.Fatal(err)
	}
	var days []string
	for _, entry := range entries {
		if _, ok := parseDayName(entry.Name()); entry.IsDir() && ok {
			days = append(days, entry.Name())
		}
	}
	sort.Strings(days)
	return days
}

// readLines decodes every line of one record file.
func readLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("line is not JSON: %v\n%s", err, line)
		}
		records = append(records, record)
	}
	return records
}

func recordAt(t *testing.T, records []map[string]any, index int) map[string]any {
	t.Helper()
	if len(records) <= index {
		t.Fatalf("want at least %d records, got %d", index+1, len(records))
	}
	return records[index]
}

// attrsOf reads a record's attrs map, which is absent when the record carried none.
func attrsOf(t *testing.T, record map[string]any) map[string]any {
	t.Helper()
	raw, ok := record["attrs"]
	if !ok {
		return nil
	}
	attrs, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("attrs is %T, want an object", raw)
	}
	return attrs
}

func TestEventLogWritesOneJSONLinePerRecord(t *testing.T) {
	log, root := newTestLog(t, "tui")
	for i := 0; i < 3; i++ {
		log.Write(Record{Level: "INFO", Kind: "log", Msg: fmt.Sprintf("record %d", i)})
	}
	day := localDay(time.Now())
	files := dayFiles(t, root, day)
	path := onlyFile(t, files)

	// <source>-<host>-<pid>-<six hex>.jsonl
	name := filepath.Base(path)
	pattern := fmt.Sprintf(`^tui-%s-%d-[0-9a-f]{6}\.jsonl$`, regexp.QuoteMeta(HostLabel()), os.Getpid())
	if !regexp.MustCompile(pattern).MatchString(name) {
		t.Fatalf("file name %q does not match %s", name, pattern)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("want 3 lines, got %d: %q", len(lines), lines)
	}
	for _, line := range lines {
		if !json.Valid([]byte(line)) {
			t.Fatalf("line is not valid JSON: %q", line)
		}
		if !strings.HasPrefix(line, `{"t":"`) {
			t.Fatalf("line does not start with the time key: %q", line)
		}
		lvl := strings.Index(line, `"lvl":`)
		kind := strings.Index(line, `"kind":`)
		msg := strings.Index(line, `"msg":`)
		if lvl < 0 || kind < 0 || msg < 0 || !(lvl < kind && kind < msg) {
			t.Fatalf("key order is not t,lvl,kind,msg: %q", line)
		}
	}
}

func TestEventLogConcurrentWritersNeverInterleave(t *testing.T) {
	const writers, perWriter = 32, 500
	log, root := newTestLog(t, "tui")

	var wg sync.WaitGroup
	for writer := 0; writer < writers; writer++ {
		wg.Add(1)
		go func(writer int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				log.Write(Record{Level: "INFO", Kind: "log", Msg: fmt.Sprintf("g%d-%d", writer, i)})
			}
		}(writer)
	}
	wg.Wait()

	records := readLines(t, onlyFile(t, dayFiles(t, root, localDay(time.Now()))))
	if len(records) != writers*perWriter {
		t.Fatalf("want %d records, got %d", writers*perWriter, len(records))
	}
	counts := map[string]int{}
	for _, record := range records {
		msg, ok := record["msg"].(string)
		if !ok {
			t.Fatalf("record has no msg: %v", record)
		}
		counts[strings.SplitN(msg, "-", 2)[0]]++
	}
	for writer := 0; writer < writers; writer++ {
		if got := counts[fmt.Sprintf("g%d", writer)]; got != perWriter {
			t.Fatalf("writer %d wrote %d records, want %d", writer, got, perWriter)
		}
	}
}

func TestEventLogTwoProcessesWriteTwoFiles(t *testing.T) {
	root := t.TempDir()
	first := OpenEventLog(root, "tui")
	defer first.Close()
	second := OpenEventLog(root, "gateway")
	defer second.Close()

	if first.Proc() == second.Proc() {
		t.Fatalf("two logs share the process id %q", first.Proc())
	}
	for i := 0; i < 5; i++ {
		first.Write(Record{Level: "INFO", Kind: "log", Msg: fmt.Sprintf("tui-%d", i)})
		second.Write(Record{Level: "INFO", Kind: "log", Msg: fmt.Sprintf("gateway-%d", i)})
	}

	files := dayFiles(t, root, localDay(time.Now()))
	if len(files) != 2 {
		t.Fatalf("want two record files, got %v", files)
	}
	seen := map[string]string{}
	for _, path := range files {
		records := readLines(t, path)
		if len(records) != 5 {
			t.Fatalf("%s holds %d records, want 5", filepath.Base(path), len(records))
		}
		prefix := strings.SplitN(records[0]["msg"].(string), "-", 2)[0]
		for _, record := range records {
			if !strings.HasPrefix(record["msg"].(string), prefix+"-") {
				t.Fatalf("%s mixes writers: %q beside %q", filepath.Base(path), record["msg"], records[0]["msg"])
			}
		}
		seen[prefix] = path
	}
	if seen["tui"] == "" || seen["gateway"] == "" {
		t.Fatalf("records were not split by writer: %v", seen)
	}
}

func TestEventLogRollsToNextDayDirectory(t *testing.T) {
	log, root := newTestLog(t, "tui")
	now := time.Now()
	lastNight := time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 59, 0, time.Local).AddDate(0, 0, -1)
	thisMorning := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 1, 0, time.Local)

	log.Write(Record{Time: lastNight, Level: "INFO", Kind: "log", Msg: "before midnight"})
	log.Write(Record{Time: thisMorning, Level: "INFO", Kind: "log", Msg: "after midnight"})

	yesterday := onlyFile(t, dayFiles(t, root, localDay(lastNight)))
	today := onlyFile(t, dayFiles(t, root, localDay(thisMorning)))
	if yesterday == today {
		t.Fatalf("both records landed in %s", today)
	}
	if got := readLines(t, yesterday)[0]["msg"]; got != "before midnight" {
		t.Fatalf("yesterday holds %q", got)
	}
	if got := readLines(t, today)[0]["msg"]; got != "after midnight" {
		t.Fatalf("today holds %q", got)
	}
}

func TestEventLogStartsNewPartPastFileLimit(t *testing.T) {
	previous := eventFileMaxBytes
	eventFileMaxBytes = 4096
	t.Cleanup(func() { eventFileMaxBytes = previous })

	log, root := newTestLog(t, "tui")
	payload := strings.Repeat("p", 800)
	for i := 0; i < 10; i++ {
		log.Write(Record{Level: "INFO", Kind: "log", Msg: fmt.Sprintf("%d %s", i, payload)})
	}

	day := localDay(time.Now())
	files := dayFiles(t, root, day)
	if len(files) < 2 {
		t.Fatalf("want a continued part beside the first file, got %v", files)
	}
	var first, part string
	for _, path := range files {
		switch {
		case strings.HasSuffix(path, ".1.jsonl"):
			part = path
		case !regexp.MustCompile(`\.\d+\.jsonl$`).MatchString(path):
			first = path
		}
	}
	if first == "" || part == "" {
		t.Fatalf("want a first file and a .1 part, got %v", files)
	}
	before, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	if len(readLines(t, first)) == 0 || len(readLines(t, part)) == 0 {
		t.Fatalf("first=%d lines part=%d lines", len(readLines(t, first)), len(readLines(t, part)))
	}
	// The first file keeps every byte it had: a part is a new file, not a rename
	// or a truncation of the old one.
	log.Write(Record{Level: "INFO", Kind: "log", Msg: "later"})
	after, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("the first file changed after the part roll: %d then %d bytes", len(before), len(after))
	}
}

func TestEventLogBoundsOversizedFieldsAndKeepsValidJSON(t *testing.T) {
	log, root := newTestLog(t, "tui")
	huge := strings.Repeat("x", 100*1024)
	log.Write(Record{
		Level: "INFO",
		Kind:  "llm",
		Msg:   huge,
		Attrs: map[string]any{"payload": huge},
	})

	data, err := os.ReadFile(onlyFile(t, dayFiles(t, root, localDay(time.Now()))))
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSuffix(string(data), "\n")
	if len(line) > recordMaxBytes {
		t.Fatalf("line is %d bytes, over the %d-byte ceiling", len(line), recordMaxBytes)
	}
	if !json.Valid([]byte(line)) {
		t.Fatalf("bounded line is not valid JSON: %q", line)
	}
	if !strings.Contains(line, "log bytes omitted") {
		t.Fatalf("bounded line carries no omission marker: %q", line)
	}
}

func TestEventLogRedactsBearer(t *testing.T) {
	log, root := newTestLog(t, "tui")
	log.Write(Record{Level: "INFO", Kind: "llm", Msg: "request sent with Bearer abc123 here"})

	data, err := os.ReadFile(onlyFile(t, dayFiles(t, root, localDay(time.Now()))))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "abc123") {
		t.Fatalf("the bearer token reached the file: %s", data)
	}
	if !strings.Contains(string(data), "Bearer [REDACTED]") {
		t.Fatalf("no redaction marker in the record: %s", data)
	}
}

func TestEmitTakesScopeFromContextAndExplicitAttrsWin(t *testing.T) {
	log, root := newTestLog(t, "tui")
	SetDefaultEventLog(log)
	t.Cleanup(func() { SetDefaultEventLog(nil) })
	RegisterScope(func(context.Context) Scope { return Scope{Project: "p-ctx", Session: "s-ctx"} })
	t.Cleanup(func() { RegisterScope(nil) })

	ctx := context.Background()
	Emit(ctx, slog.LevelInfo, "turn", "no attributes")
	Emit(ctx, slog.LevelInfo, "turn", "explicit wins", slog.String("session", "s-attr"))
	Emit(ctx, slog.LevelInfo, "turn", "underscored", slog.String("session_id", "s-id"), slog.String("run_id", "r-1"))

	records := readLines(t, onlyFile(t, dayFiles(t, root, localDay(time.Now()))))
	scoped := recordAt(t, records, 0)
	if scoped["session"] != "s-ctx" || scoped["project"] != "p-ctx" {
		t.Fatalf("scope did not reach the record: %v", scoped)
	}
	if attrs := attrsOf(t, scoped); attrs != nil {
		if _, ok := attrs["session"]; ok {
			t.Fatalf("identity attribute stayed in attrs: %v", attrs)
		}
	}
	explicit := recordAt(t, records, 1)
	if explicit["session"] != "s-attr" {
		t.Fatalf("explicit attribute did not win: %v", explicit)
	}
	if _, ok := attrsOf(t, explicit)["session"]; ok {
		t.Fatalf("identity attribute stayed in attrs: %v", attrsOf(t, explicit))
	}
	underscored := recordAt(t, records, 2)
	if underscored["session"] != "s-id" || underscored["run"] != "r-1" {
		t.Fatalf("underscored spellings were not promoted: %v", underscored)
	}
	if attrs := attrsOf(t, underscored); attrs != nil {
		if _, ok := attrs["session_id"]; ok {
			t.Fatalf("session_id stayed in attrs: %v", attrs)
		}
	}
	if got := scoped["kind"]; got != "turn" {
		t.Fatalf("kind is %v, want turn", got)
	}
	if at, _ := scoped["at"].(string); !strings.HasPrefix(at, "pkg/telemetry/eventlog_test.go:") {
		t.Fatalf("at is %q, want the test file", at)
	}
}

func TestEventHandlerMapsSlogRecord(t *testing.T) {
	log, root := newTestLog(t, "tui")
	logger := slog.New(NewEventHandler(log, slog.LevelDebug)).
		With("project", "p-1", "session", "s-1", "worker", "w-1", "run", "r-1", "agent", "a-1")
	logger.Warn("m", "err", fmt.Errorf("wrap: %w", io.EOF), "n", 3)

	record := recordAt(t, readLines(t, onlyFile(t, dayFiles(t, root, localDay(time.Now())))), 0)
	if record["lvl"] != "WARN" || record["kind"] != "log" || record["msg"] != "m" {
		t.Fatalf("record head is wrong: %v", record)
	}
	if record["err"] != "wrap: EOF" {
		t.Fatalf("err is %v, want wrap: EOF", record["err"])
	}
	for key, want := range map[string]string{"project": "p-1", "session": "s-1", "worker": "w-1", "run": "r-1", "agent": "a-1"} {
		if record[key] != want {
			t.Fatalf("%s is %v, want %s (bound attribute did not promote)", key, record[key], want)
		}
	}
	attrs := attrsOf(t, record)
	chain, ok := attrs["err_chain"].([]any)
	if !ok || len(chain) != 2 {
		t.Fatalf("err_chain is %v, want two layers", attrs["err_chain"])
	}
	if chain[0] != "*fmt.wrapError" || chain[1] != "*errors.errorString" {
		t.Fatalf("err_chain types are %v", chain)
	}
	if attrs["n"] != float64(3) {
		t.Fatalf("n is %v, want 3", attrs["n"])
	}
	if at, _ := record["at"].(string); !strings.HasPrefix(at, "pkg/telemetry/eventlog_test.go:") {
		t.Fatalf("at is %q, want the test file", at)
	}
}

func TestEventHandlerKeepsGroups(t *testing.T) {
	log, root := newTestLog(t, "tui")
	slog.New(NewEventHandler(log, slog.LevelInfo)).WithGroup("http").Info("x", "status", 200, "err", io.ErrUnexpectedEOF)

	record := recordAt(t, readLines(t, onlyFile(t, dayFiles(t, root, localDay(time.Now())))), 0)
	if got := attrsOf(t, record)["http.status"]; got != float64(200) {
		t.Fatalf("group prefix was dropped: attrs=%v", attrsOf(t, record))
	}
	// A grouped error is not the record's own err: it stays an attribute, and it
	// is kept as its message rather than as the {} json makes of an error.
	if got := attrsOf(t, record)["http.err"]; got != io.ErrUnexpectedEOF.Error() {
		t.Fatalf("grouped error is %v, want its message", got)
	}
}

func TestEventHandlerPromotesKindAttr(t *testing.T) {
	log, root := newTestLog(t, "tui")
	slog.New(NewEventHandler(log, slog.LevelInfo)).Info("request", "kind", "http", "status", 200)

	record := recordAt(t, readLines(t, onlyFile(t, dayFiles(t, root, localDay(time.Now())))), 0)
	if record["kind"] != "http" {
		t.Fatalf("kind is %v, want http", record["kind"])
	}
	attrs := attrsOf(t, record)
	if _, ok := attrs["kind"]; ok {
		t.Fatalf("kind stayed in attrs: %v", attrs)
	}
	if attrs["status"] != float64(200) {
		t.Fatalf("status is %v", attrs["status"])
	}
}

func TestEventLogAllowsLongerStackAttr(t *testing.T) {
	log, root := newTestLog(t, "tui")
	stack := strings.Repeat("s", 20*1024)
	other := strings.Repeat("o", 20*1024)
	log.Write(Record{Level: "ERROR", Kind: "panic", Msg: "boom", Attrs: map[string]any{"stack": stack, "note": other}})

	attrs := attrsOf(t, recordAt(t, readLines(t, onlyFile(t, dayFiles(t, root, localDay(time.Now())))), 0))
	if got, _ := attrs["stack"].(string); got != stack {
		t.Fatalf("a 20KB stack was truncated to %d bytes", len(got))
	}
	note, _ := attrs["note"].(string)
	if len(note) >= len(other) || !strings.Contains(note, "log bytes omitted") {
		t.Fatalf("a 20KB non-stack attribute was not bounded: %d bytes", len(note))
	}
	if len(note) > attrMaxBytes+200 {
		t.Fatalf("non-stack attribute was bounded to %d bytes, over %d", len(note), attrMaxBytes)
	}
}

func TestEventLogWriteFailureIsCountedNotReturned(t *testing.T) {
	root := t.TempDir()
	// A regular file where the log directory belongs: every open under it fails.
	if err := os.WriteFile(filepath.Join(root, home.LogsDir), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	log := OpenEventLog(root, "tui")
	defer log.Close()
	log.Write(Record{Level: "INFO", Kind: "log", Msg: "lost"}) // must not panic

	if err := os.Remove(filepath.Join(root, home.LogsDir)); err != nil {
		t.Fatal(err)
	}
	log.Write(Record{Level: "INFO", Kind: "log", Msg: "kept"})

	records := readLines(t, onlyFile(t, dayFiles(t, root, localDay(time.Now()))))
	if len(records) != 1 {
		t.Fatalf("want only the recovered record on disk, got %v", records)
	}
	if got := attrsOf(t, records[0])["dropped_before"]; got != float64(1) {
		t.Fatalf("dropped_before is %v, want 1", got)
	}
}

func TestSweepLogsDropsOldDaysThenOldestPayloads(t *testing.T) {
	previous := logBudgetBytes
	t.Cleanup(func() { logBudgetBytes = previous })

	root := t.TempDir()
	logsDir := filepath.Join(root, home.LogsDir)
	now := time.Now()
	writeDay := func(day string, payload bool) {
		dir := filepath.Join(logsDir, day)
		if err := os.MkdirAll(filepath.Join(dir, "x"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "tui-h-1-abcdef.jsonl"), []byte(strings.Repeat("r", 10)), 0o600); err != nil {
			t.Fatal(err)
		}
		if payload {
			if err := os.WriteFile(filepath.Join(dir, "x", "a3f09c-000391.req.gz"), []byte(strings.Repeat("b", 1000)), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}

	// Ten day directories, of which exactly three are older than the window:
	// D-7, D-8 and D-9. Seven survive (D-6 back to today) — a directory dated D-7
	// starts before now minus LogRetentionDays, so it goes with the older ones.
	kept := make([]string, 0, 7)
	for i := 0; i < 7; i++ {
		day := localDay(now.AddDate(0, 0, -i))
		kept = append(kept, day)
		writeDay(day, true)
	}
	var expired []string
	for i := 7; i <= 9; i++ {
		day := localDay(now.AddDate(0, 0, -i))
		expired = append(expired, day)
		writeDay(day, true)
	}
	// A directory that is not a day directory belongs to someone else.
	foreign := filepath.Join(logsDir, "keep-me")
	if err := os.MkdirAll(foreign, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(foreign, "note.txt"), []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}

	// One payload's worth of headroom: the sweep must delete the oldest payload
	// and stop, leaving every record file in place.
	logBudgetBytes = 6500
	SweepLogs(root, now)

	for _, day := range expired {
		if _, err := os.Stat(filepath.Join(logsDir, day)); !os.IsNotExist(err) {
			t.Fatalf("%s is older than the retention window but survived: %v", day, err)
		}
	}
	if surviving := dayDirs(t, logsDir); len(surviving) != len(kept) ||
		surviving[0] != kept[len(kept)-1] || surviving[len(surviving)-1] != kept[0] {
		t.Fatalf("want %d day directories from %s to %s, got %v",
			len(kept), kept[len(kept)-1], kept[0], surviving)
	}
	if _, err := os.Stat(filepath.Join(foreign, "note.txt")); err != nil {
		t.Fatalf("a directory that is not a day directory was touched: %v", err)
	}
	oldest := kept[len(kept)-1]
	if _, err := os.Stat(filepath.Join(logsDir, oldest, "x")); !os.IsNotExist(err) {
		t.Fatalf("the oldest day still holds its payload: %v", err)
	}
	if _, err := os.Stat(filepath.Join(logsDir, oldest, "tui-h-1-abcdef.jsonl")); err != nil {
		t.Fatalf("the oldest day lost its records: %v", err)
	}
	if _, err := os.Stat(filepath.Join(logsDir, localDay(now), "x")); err != nil {
		t.Fatalf("today's payload was deleted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(logsDir, localDay(now), "tui-h-1-abcdef.jsonl")); err != nil {
		t.Fatalf("today's records were deleted: %v", err)
	}
}

func TestSweepLogsRemovesStaleLegacyFiles(t *testing.T) {
	root := t.TempDir()
	logsDir := filepath.Join(root, home.LogsDir)
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	stale := filepath.Join(logsDir, "debug.log.2")
	fresh := filepath.Join(logsDir, "error.log")
	other := filepath.Join(logsDir, "notes.txt")
	for _, path := range []string{stale, fresh, other} {
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	eightDaysAgo := now.AddDate(0, 0, -8)
	if err := os.Chtimes(stale, eightDaysAgo, eightDaysAgo); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(other, eightDaysAgo, eightDaysAgo); err != nil {
		t.Fatal(err)
	}

	SweepLogs(root, now)

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("a stale debug.log backup survived: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("an error.log inside the retention window was deleted: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("a file that is not an old log format was deleted: %v", err)
	}
}

// BenchmarkEventLogWrite measures one record on the path model requests and HTTP
// responses call: encode, one write(2), no fsync.
func BenchmarkEventLogWrite(b *testing.B) {
	root := b.TempDir()
	log := OpenEventLog(root, "bench")
	defer log.Close()
	record := Record{
		Level: "INFO",
		Kind:  "llm",
		Msg:   strings.Repeat("x", 512),
		Attrs: map[string]any{"model": "claude-sonnet-4", "status": 200, "tokens": 1024},
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		log.Write(record)
	}
}
