// The read side of the event log (plans/287): the records under <home>/logs as
// one filterable, time-ordered page, one model call's request and response as
// one object, and one payload as a stream. Nothing in this file writes.
//
// The tree it reads is the one plans 284–286 write: a directory per local day,
// one JSON record per line per process, and the bulky payloads under the day's
// x/. That format is append-only and never renamed, so a reader can hold an
// offset across a scan and come back for the line it wants; it must never assume
// the file it scanned is still there — the retention sweep deletes whole days
// underneath it. Every read below therefore skips what has gone instead of
// failing the query.
//
// The query is a file scan, deliberately: the owner settled that logs do not go
// into the state database, so the cost of a wide range is bounded by bytes read
// (eventScanMaxBytes) rather than by an index.
package telemetry

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/home"
)

// ErrBadLogRequest reports a log request the reader refuses before it touches
// the filesystem: a day that is not YYYY-MM-DD, or a blob name that could leave
// the day's x/ directory. The gateway answers it with 400.
var ErrBadLogRequest = errors.New("invalid log request")

const (
	// eventBucketCount is how many bars the log page's histogram draws over the
	// requested range.
	eventBucketCount = 60
	// eventLineMaxBytes bounds one line this reader holds. The writer's own
	// ceiling is recordMaxBytes (64 KiB), so this only ever catches a line that
	// is not one of ours — a torn write, a foreign file — and it is skipped
	// whole rather than read into memory.
	eventLineMaxBytes = 128 * 1024
	// eventDefaultPageSize and eventMaxPageSize clamp the page cut here. The
	// gateway's own listMaxPageSize is 100, so a caller that asks for more than
	// that gets 100 rows: this reader serves a page, never a whole day.
	eventDefaultPageSize = 50
	eventMaxPageSize     = 100
	// stderrTailBytes is how much of a process's raw stderr a synthesized record
	// carries. The end is where a crash says what happened.
	stderrTailBytes = 4 * 1024
	// exchangeConcurrentMax is how many overlapping calls the exchange view
	// lists: a page draws them, it does not tabulate them.
	exchangeConcurrentMax = 8
	// exchangeCallsMax bounds the calls one day's exchange scan remembers while
	// pairing requests with responses (each call is two records).
	exchangeCallsMax = 50_000
	// exchangeSummaryMaxBytes bounds the payload a summary is read from. A
	// summary is a display of what the call produced; it is not a second copy of
	// the body.
	exchangeSummaryMaxBytes = 8 << 20
	// stderrRecordMsg names the one record a process's raw stderr becomes.
	stderrRecordMsg = "process stderr output"
	// stderrKind is that record's kind, which no file-backed record uses.
	stderrKind = "stderr"
)

// The scan ceilings are variables so a test can drive the truncation without
// writing half a gigabyte. Production reads them unchanged.
var (
	eventScanMaxBytes = int64(512) << 20
	eventScanMaxHits  = 500_000
)

// blobNameRe is the only shape of payload name this reader opens: one file name
// under the day's x/, gzipped. It cannot carry a separator or a parent
// reference, so validation can happen before any filesystem call.
var blobNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*\.gz$`)

// EventQuery is one page request over the event log. A zero From or To means
// "the last hour"; an empty slice or string means "every value".
type EventQuery struct {
	From, To time.Time
	Levels   []string // empty = every level
	Sources  []string // empty = every source (tui | gateway | cli)
	Project  string   // project KEY (state.ProjectKeyForRoot), not the project id
	Session  string
	Run      string
	Kind     string
	Text     string // case-insensitive substring of the whole line
	Page     int
	PageSize int
}

// EventRow is one record of the event log with the facts its file name carried
// restored: which process wrote it, from which surface, on which day.
type EventRow struct {
	ID     string `json:"id"` // <day>/<file name>:<line offset>, stable across queries
	Day    string `json:"day"`
	Source string `json:"source"`
	Host   string `json:"host"`
	PID    int    `json:"pid"`
	Proc   string `json:"proc,omitempty"`
	Record
}

// EventBucket is one bar of the log page's histogram: the records of a slice of
// the requested range, by level.
type EventBucket struct {
	T     time.Time `json:"t"`
	Debug int       `json:"debug"`
	Info  int       `json:"info"`
	Warn  int       `json:"warn"`
	Error int       `json:"error"`
}

// EventPage is one page of the event log plus the numbers the page draws around
// it. Total counts the rows that match every condition including the level
// filter; LevelCounts counts the same rows without it, so the level buttons can
// say how many more each one would add. Truncated says the scan stopped at its
// ceiling, which makes Total a lower bound.
type EventPage struct {
	Rows        []EventRow
	Total       int
	Page        int
	LevelCounts map[string]int
	Buckets     []EventBucket
	Truncated   bool
}

// eventLevels is the vocabulary of Record.Level, in the one spelling slog.Level
// writes and the writer stores.
var eventLevels = []string{
	slog.LevelDebug.String(),
	slog.LevelInfo.String(),
	slog.LevelWarn.String(),
	slog.LevelError.String(),
}

// FormatEventTime renders a record's time the way the file writes it: RFC3339
// with milliseconds and an offset. The layout lives in this package, so the
// gateway formats a row's "t" with it rather than repeating the layout.
func FormatEventTime(t time.Time) string { return t.Format(eventTimeLayout) }

// QueryEvents reads the records under <root>/logs and returns one page of them,
// newest first.
//
// The scan is bounded three ways: by the retention window (a day directory the
// sweep keeps no longer exists to read), by each file's modification time (a
// file only grows, so its mtime is its newest record), and by eventScanMaxBytes
// / eventScanMaxHits. Only the rows of the served page are decoded in full: the
// scan itself reads a time, a level and the byte-level filters.
func QueryEvents(ctx context.Context, root string, q EventQuery) (EventPage, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	from, to := q.From, q.To
	if to.IsZero() {
		to = time.Now()
	}
	if from.IsZero() {
		from = to.Add(-time.Hour)
	}
	if !from.Before(to) {
		return EventPage{}, fmt.Errorf("event query: from %s is not before to %s",
			FormatEventTime(from), FormatEventTime(to))
	}
	page := max(q.Page, 1)
	pageSize := q.PageSize
	if pageSize < 1 {
		pageSize = eventDefaultPageSize
	}
	pageSize = min(pageSize, eventMaxPageSize)

	out := EventPage{
		Page:        page,
		LevelCounts: map[string]int{},
		Buckets:     newEventBuckets(from, to),
	}
	for _, level := range eventLevels {
		out.LevelCounts[level] = 0
	}

	filters := newEventFilters(q)
	files := eventFilesFor(resolveRoot(root), from, to, filters.sources)
	hits := make([]eventHit, 0, 256)
	truncated := false
	var scanned int64
	scanner := newLogLineScanner(eventLineMaxBytes)
	for index := range files {
		// The scan is bounded by bytes, but a client that walked away should not
		// keep it reading: the request's context is checked once per file, which
		// is where a day's 32 MiB part (tens of milliseconds) ends.
		if err := ctx.Err(); err != nil {
			return EventPage{}, err
		}
		file := &files[index]
		if file.stderr != nil {
			// A process's raw stderr is one record, made here rather than read
			// back: it has no line to seek to.
			record := *file.stderr
			if !filters.recordPasses(&record, from, to) {
				continue
			}
			out.countLevel(record.Level)
			if !filters.levelPasses(record.Level) {
				continue
			}
			out.countBucket(record.Time, record.Level, from, to)
			hits = append(hits, eventHit{t: record.Time.UnixNano(), file: int32(index), off: 0})
			continue
		}
		read := scanEventFile(file, scanner, filters, from, to, &out, int32(index), &hits)
		scanned += read
		if len(hits) > eventScanMaxHits || scanned > eventScanMaxBytes {
			// The ceiling on this scan: the page says the count it gives is a
			// floor, and the reader stops rather than reading a whole week to
			// answer a query nobody can page through. A file is at most
			// eventFileMaxBytes, so the overshoot is one file.
			truncated = true
			break
		}
	}

	// Newest first, and inside one millisecond the file then the offset, both
	// descending: records written in the same millisecond keep one order across
	// pages. files is ordered by day then name (os.ReadDir sorts, and the days
	// are walked oldest first), so its index is that order.
	sort.Slice(hits, func(i, j int) bool {
		a, b := hits[i], hits[j]
		if a.t != b.t {
			return a.t > b.t
		}
		if a.file != b.file {
			return a.file > b.file
		}
		return a.off > b.off
	})

	out.Total = len(hits)
	out.Truncated = truncated
	start, end, served := eventPageWindow(len(hits), page, pageSize)
	out.Page = served
	out.Rows = make([]EventRow, 0, end-start)
	for _, hit := range hits[start:end] {
		row, ok := files[hit.file].rowAt(hit)
		if !ok {
			// The sweep deleted the day between the scan and this read: one
			// missing row must not fail the page.
			continue
		}
		out.Rows = append(out.Rows, row)
	}
	return out, nil
}

// eventPageWindow clamps the requested page to the pages that exist and returns
// the half-open window it serves: a page past the end serves the last one, so
// deleting the last row of the last page lands on the page before it instead of
// an empty table (the paging contract every list endpoint follows).
func eventPageWindow(total, page, pageSize int) (start, end, served int) {
	pages := (total + pageSize - 1) / pageSize
	if pages < 1 {
		pages = 1
	}
	served = min(page, pages)
	start = (served - 1) * pageSize
	if start > total {
		start = total
	}
	return start, min(start+pageSize, total), served
}

// countLevel records one row in the level tally, which ignores the level filter.
func (p *EventPage) countLevel(level string) {
	if _, known := p.LevelCounts[level]; known {
		p.LevelCounts[level]++
	}
}

// countBucket records one row in the histogram of the requested range.
func (p *EventPage) countBucket(t time.Time, level string, from, to time.Time) {
	bucket := &p.Buckets[eventBucketIndex(t, from, to)]
	switch level {
	case "DEBUG":
		bucket.Debug++
	case "INFO":
		bucket.Info++
	case "WARN":
		bucket.Warn++
	case "ERROR":
		bucket.Error++
	}
}

// newEventBuckets splits [from, to] into eventBucketCount equal slices, each
// starting at its own instant.
func newEventBuckets(from, to time.Time) []EventBucket {
	span := to.Sub(from)
	buckets := make([]EventBucket, eventBucketCount)
	for i := range buckets {
		buckets[i].T = from.Add(time.Duration(int64(span) * int64(i) / eventBucketCount))
	}
	return buckets
}

// eventBucketIndex is the slice a record at t falls in, clamped to the range.
func eventBucketIndex(t, from, to time.Time) int {
	span := int64(to.Sub(from))
	index := 0
	if span > 0 {
		index = int(int64(t.Sub(from)) * eventBucketCount / span)
	}
	return min(max(index, 0), eventBucketCount-1)
}

// eventFilters is the cheap half of every condition: byte patterns matched
// against the line before anything is decoded. A JSON string value written by
// the encoder appears in the line exactly as Quote spells it, so an exact
// pattern answers the same question the decoded value would, at a fraction of
// the cost.
type eventFilters struct {
	sources   []string
	levels    map[string]bool
	session   []byte
	run       []byte
	kind      []byte
	kindValue string
	project   []byte
	text      []byte // already lowercased
}

func newEventFilters(q EventQuery) eventFilters {
	f := eventFilters{}
	for _, source := range q.Sources {
		if source = strings.TrimSpace(source); source != "" {
			f.sources = append(f.sources, source)
		}
	}
	if len(q.Levels) > 0 {
		f.levels = make(map[string]bool, len(q.Levels))
		for _, level := range q.Levels {
			f.levels[strings.ToUpper(strings.TrimSpace(level))] = true
		}
	}
	f.session = fieldNeedle("session", q.Session)
	f.run = fieldNeedle("run", q.Run)
	f.kindValue = strings.TrimSpace(q.Kind)
	f.kind = fieldNeedle("kind", f.kindValue)
	f.project = fieldNeedle("project", q.Project)
	if text := strings.TrimSpace(q.Text); text != "" {
		f.text = bytes.ToLower([]byte(text))
	}
	return f
}

// fieldNeedle is the byte pattern one string field takes in a written line, or
// nil when the filter is off.
func fieldNeedle(key, value string) []byte {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return []byte(`"` + key + `":` + strconv.Quote(value))
}

// linePasses is every condition that needs no decode.
func (f *eventFilters) linePasses(line []byte) bool {
	if f.session != nil && !bytes.Contains(line, f.session) {
		return false
	}
	if f.run != nil && !bytes.Contains(line, f.run) {
		return false
	}
	if f.kind != nil && !bytes.Contains(line, f.kind) {
		return false
	}
	if f.project != nil && !bytes.Contains(line, f.project) {
		return false
	}
	if f.text != nil && !containsFold(line, f.text) {
		return false
	}
	return true
}

// levelPasses applies the level filter. A row that passes it is the only kind
// that reaches the histogram and the page, but every row in the time window was
// counted by level first.
func (f *eventFilters) levelPasses(level string) bool {
	if f.levels == nil {
		return true
	}
	return f.levels[level]
}

// recordPasses is linePasses for the record that was never a line: the raw
// stderr record. It carries no session, run or project, so any of those filters
// excludes it — it belongs to the process, not to a conversation.
func (f *eventFilters) recordPasses(rec *Record, from, to time.Time) bool {
	if rec.Time.Before(from) || rec.Time.After(to) {
		return false
	}
	if f.session != nil || f.run != nil || f.project != nil {
		return false
	}
	if f.kindValue != "" && rec.Kind != f.kindValue {
		return false
	}
	if f.text != nil {
		text := rec.Msg
		if tail, ok := rec.Attrs["tail"].(string); ok {
			text += tail
		}
		if !containsFold([]byte(text), f.text) {
			return false
		}
	}
	return true
}

// containsFold reports whether haystack contains needle, comparing ASCII letters
// without regard to case. needle is already lowercased (newEventFilters does
// that), so the fold is one comparison per candidate position. It allocates
// nothing: the text filter runs on every line of a scan.
func containsFold(haystack, needle []byte) bool {
	if len(needle) == 0 {
		return true
	}
	if len(needle) > len(haystack) {
		return false
	}
	first := needle[0]
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if lowerASCII(haystack[i]) != first {
			continue
		}
		if equalFold(haystack[i:i+len(needle)], needle) {
			return true
		}
	}
	return false
}

func equalFold(a, b []byte) bool {
	for i := range a {
		if lowerASCII(a[i]) != lowerASCII(b[i]) {
			return false
		}
	}
	return true
}

func lowerASCII(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + ('a' - 'A')
	}
	return c
}

// eventSource is one file the query reads: a record file, or a process's raw
// stderr already turned into the one record it contributes.
type eventSource struct {
	path   string
	day    string
	name   string
	source string
	host   string
	proc   string
	pid    int
	stderr *Record // non-nil: this source has no line to seek to
}

// rowAt rebuilds one row from the file the scan saw it in.
func (s *eventSource) rowAt(hit eventHit) (EventRow, bool) {
	row := EventRow{Day: s.day, Source: s.source, Host: s.host, PID: s.pid, Proc: s.proc}
	if s.stderr != nil {
		row.ID = s.day + "/" + s.name + ":0"
		row.Record = cloneRecord(*s.stderr)
		return row, true
	}
	record, err := readRecordAt(s.path, hit.off)
	if err != nil {
		return EventRow{}, false
	}
	row.ID = s.day + "/" + s.name + ":" + strconv.FormatInt(hit.off, 10)
	row.Record = record
	return row, true
}

// eventHit is one matching record, kept as a position rather than as a line: a
// nanosecond time, a file index and a byte offset — 24 bytes per hit, and the
// only thing a scan of half a gigabyte holds.
type eventHit struct {
	t    int64
	file int32
	off  int64
}

// eventDayWindow is the first and last day directory a query walks. The tree
// keeps LogRetentionDays days, so a request for a decade is answered from the
// days that can still exist: the walk is bounded by retention, not by the
// request.
func eventDayWindow(from, to time.Time) (first, last time.Time) {
	first = startOfLocalDay(from).AddDate(0, 0, -1)
	last = startOfLocalDay(to).AddDate(0, 0, 1)
	// The sweep runs hourly, so a day just outside the window may still be on
	// disk; one extra day sees it without walking a swept tree.
	floor := startOfLocalDay(time.Now()).AddDate(0, 0, -(LogRetentionDays + 1))
	if first.Before(floor) {
		first = floor
	}
	if last.Before(first) {
		last = first
	}
	return first, last
}

func startOfLocalDay(t time.Time) time.Time {
	local := t.In(time.Local)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.Local)
}

// eventFilesFor lists the files of the day directories a query covers: the
// record files whose source the query wants and whose mtime says they hold
// something new enough, plus one synthesized record per non-empty stderr file.
// A directory or file that cannot be read is skipped: the sweep may be deleting
// it, and that is not a reason to fail the query.
func eventFilesFor(root string, from, to time.Time, sources []string) []eventSource {
	logsDir := filepath.Join(root, home.LogsDir)
	first, last := eventDayWindow(from, to)
	var files []eventSource
	for day := first; !day.After(last); day = day.AddDate(0, 0, 1) {
		name := day.Format(dayLayout)
		entries, err := os.ReadDir(filepath.Join(logsDir, name))
		if err != nil {
			continue // a day the sweep already took
		}
		// os.ReadDir sorts by name, so a day's files are in one order on every
		// machine — the tie-break the page order relies on.
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			source, host, proc, pid, ok := parseLogFileName(entry.Name())
			if !ok {
				continue
			}
			if len(sources) > 0 && !containsString(sources, source) {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				continue
			}
			if info.ModTime().Before(from) {
				// Append-only, so mtime is the newest record: nothing in this
				// file can be inside the range.
				continue
			}
			file := eventSource{
				path:   filepath.Join(logsDir, name, entry.Name()),
				day:    name,
				name:   entry.Name(),
				source: source,
				host:   host,
				proc:   proc,
				pid:    pid,
			}
			if isStderrLogName(entry.Name()) {
				file.stderr = stderrRecord(file.path, info.Size(), info.ModTime())
				if file.stderr == nil {
					continue
				}
			}
			files = append(files, file)
		}
	}
	return files
}

// stderrRecord turns one process's raw stderr into the record the page shows for
// it: a crash's last words are otherwise the only output with no record at all.
// An empty file contributes nothing.
func stderrRecord(path string, size int64, modTime time.Time) *Record {
	if size <= 0 {
		return nil
	}
	tail, err := readFileTail(path, stderrTailBytes)
	if err != nil {
		return nil
	}
	return &Record{
		Time:  modTime,
		Level: slog.LevelError.String(),
		Kind:  stderrKind,
		Msg:   stderrRecordMsg,
		Attrs: map[string]any{"bytes": size, "tail": string(tail)},
	}
}

// readFileTail reads at most limit bytes from the end of a file.
func readFileTail(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if _, err := file.Seek(max(info.Size()-limit, 0), io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(file)
}

// scanEventFile reads one record file line by line, appending its hits and
// returning how many bytes it read. offset is the position of a line's first
// byte, which is what the row's id names.
func scanEventFile(file *eventSource, scanner *logLineScanner, filters eventFilters, from, to time.Time, page *EventPage, index int32, hits *[]eventHit) int64 {
	handle, err := os.Open(file.path)
	if err != nil {
		return 0 // deleted under us: not this query's failure
	}
	defer func() { _ = handle.Close() }()
	scanner.reset(handle)

	var offset int64
	for {
		line, read, overlong, readErr := scanner.next()
		start := offset
		offset += read
		if overlong {
			// Not a line this reader will hold: skipped whole, but its bytes
			// still count against the ceiling.
			if readErr != nil {
				return offset
			}
			continue
		}
		if len(line) > 0 {
			if t, hit := scanEventLine(line, filters, from, to, page); hit {
				*hits = append(*hits, eventHit{t: t.UnixNano(), file: index, off: start})
			}
		}
		if readErr != nil {
			return offset
		}
	}
}

// scanEventLine applies the conditions in the order that costs least: the
// byte-level filters, then the time, then the level that splits "counted" from
// "served". It reports the record's time and whether the line is a hit.
func scanEventLine(line []byte, filters eventFilters, from, to time.Time, page *EventPage) (time.Time, bool) {
	if !bytes.HasPrefix(line, []byte(`{"t":"`)) {
		return time.Time{}, false // not a record of ours
	}
	if !filters.linePasses(line) {
		return time.Time{}, false
	}
	t, ok := recordTime(line)
	if !ok {
		return time.Time{}, false
	}
	if t.Before(from) || t.After(to) {
		return time.Time{}, false
	}
	level := recordLevel(line)
	page.countLevel(level)
	if !filters.levelPasses(level) {
		return time.Time{}, false
	}
	page.countBucket(t, level, from, to)
	return t, true
}

// recordTime reads the "t" the writer put first in the line. It is inside the
// prefix scanEventLine already checked, so this is a scan to the closing quote,
// not a decode.
func recordTime(line []byte) (time.Time, bool) {
	const prefix = `{"t":"`
	rest := line[len(prefix):]
	end := bytes.IndexByte(rest, '"')
	if end < 0 {
		return time.Time{}, false
	}
	return parseEventTime(string(rest[:end]))
}

// recordLevel reads the "lvl" value without decoding the line.
func recordLevel(line []byte) string {
	const prefix = `"lvl":"`
	start := bytes.Index(line, []byte(prefix))
	if start < 0 {
		return ""
	}
	rest := line[start+len(prefix):]
	end := bytes.IndexByte(rest, '"')
	if end < 0 {
		return ""
	}
	return string(rest[:end])
}

// readRecordAt decodes the line that starts at offset. A line that no longer
// parses (a torn tail written after the scan) is an error the caller skips.
func readRecordAt(path string, offset int64) (Record, error) {
	file, err := os.Open(path)
	if err != nil {
		return Record{}, err
	}
	defer func() { _ = file.Close() }()
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return Record{}, err
	}
	scanner := newLogLineScanner(eventLineMaxBytes)
	scanner.reset(file)
	line, _, overlong, err := scanner.next()
	if overlong {
		return Record{}, errors.New("event record is longer than this reader holds")
	}
	if err != nil && len(line) == 0 {
		return Record{}, err
	}
	// The record's own time is written by hand as "t" (Record.Time has no JSON
	// name), so it is decoded here beside the fields that do have one.
	var stored struct {
		T string `json:"t"`
		Record
	}
	if err := json.Unmarshal(line, &stored); err != nil {
		return Record{}, err
	}
	if t, ok := parseEventTime(stored.T); ok {
		stored.Record.Time = t
	}
	return stored.Record, nil
}

func parseEventTime(text string) (time.Time, bool) {
	t, err := time.Parse(eventTimeLayout, text)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// logLineScanner cuts one file into lines without ever holding more than limit
// bytes of one. The buffer it hands out is reused across lines: a scan of half a
// gigabyte must not allocate a line at a time.
type logLineScanner struct {
	reader *bufio.Reader
	buffer []byte
	limit  int
}

func newLogLineScanner(limit int) *logLineScanner {
	return &logLineScanner{limit: limit}
}

func (s *logLineScanner) reset(r io.Reader) {
	s.reader = bufio.NewReaderSize(r, 64*1024)
	s.buffer = s.buffer[:0]
}

// next returns the line without its newline, how many bytes it occupied
// (newline included, so the caller's offsets stay exact), whether it exceeded
// the limit, and the read error that ended it (io.EOF for the last line).
func (s *logLineScanner) next() (line []byte, read int64, overlong bool, err error) {
	s.buffer = s.buffer[:0]
	for {
		chunk, chunkErr := s.reader.ReadSlice('\n')
		read += int64(len(chunk))
		if !overlong {
			if len(s.buffer)+len(chunk) > s.limit {
				overlong = true
				s.buffer = s.buffer[:0]
			} else {
				s.buffer = append(s.buffer, chunk...)
			}
		}
		if errors.Is(chunkErr, bufio.ErrBufferFull) {
			continue
		}
		return bytes.TrimSuffix(s.buffer, []byte("\n")), read, overlong, chunkErr
	}
}

// parseLogFileName splits <source>-<host>-<pid>-<proc>[.<n>].jsonl from the right,
// and the same shape for a stderr file. A host name may contain dashes (and
// dots, which HostLabel keeps), so the pid is what anchors the split: the last
// field is the proc, and the one before it must be all digits.
func parseLogFileName(name string) (source, host, proc string, pid int, ok bool) {
	var base string
	switch {
	case strings.HasSuffix(name, ".jsonl"):
		base = strings.TrimSuffix(name, ".jsonl")
	case isStderrLogName(name):
		base = strings.TrimSuffix(strings.TrimPrefix(name, "stderr-"), ".log")
	default:
		return "", "", "", 0, false
	}
	// A rolled-over part is <base>.<n>: anything after the last dot that is all
	// digits is that number, never part of a host name (a host's own dots are
	// always followed by more name, and the proc follows them).
	if dot := strings.LastIndex(base, "."); dot >= 0 && dot+1 < len(base) {
		if _, err := strconv.Atoi(base[dot+1:]); err == nil {
			base = base[:dot]
		}
	}
	fields := strings.Split(base, "-")
	if len(fields) < 4 {
		return "", "", "", 0, false
	}
	value, err := strconv.Atoi(fields[len(fields)-2])
	if err != nil {
		return "", "", "", 0, false
	}
	return fields[0], strings.Join(fields[1:len(fields)-2], "-"), fields[len(fields)-1], value, true
}

func isStderrLogName(name string) bool {
	return strings.HasPrefix(name, "stderr-") && strings.HasSuffix(name, ".log")
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// ---- one call: its request, its response and what it produced -------------

// Exchange is one model call as the log page opens it: the two records 286
// wrote, the conversation they belong to, a summary of what came back, and the
// calls that were in flight at the same time. The project's and the session's
// names are the gateway's to add; nothing here reads the state database.
type Exchange struct {
	ID         string           `json:"id"`
	Day        string           `json:"day"`
	Request    *ExchangePart    `json:"request"`
	Response   *ExchangeReply   `json:"response"`
	Scope      ExchangeScope    `json:"scope"`
	Summary    *ResponseSummary `json:"summary"`
	Concurrent []ExchangeCall   `json:"concurrent"`
}

// ExchangePart is the request half of a call: what was sent, to which endpoint
// and with which model, and where its payload file is.
type ExchangePart struct {
	T       time.Time `json:"t"`
	Method  string    `json:"method,omitempty"`
	URL     string    `json:"url,omitempty"`
	Model   string    `json:"model,omitempty"`
	Bytes   int64     `json:"bytes"`
	Whole   bool      `json:"whole"`
	Omitted int64     `json:"omitted"`
	Blob    string    `json:"blob,omitempty"`
}

// ExchangeReply is the response half: how the call ended, how long it took, and
// where its payload file is.
type ExchangeReply struct {
	T        time.Time `json:"t"`
	Status   int       `json:"status"`
	Outcome  string    `json:"outcome,omitempty"`
	DurMs    int64     `json:"dur_ms"`
	TTFBMs   int64     `json:"ttfb_ms"`
	MaxGapMs int64     `json:"max_gap_ms"`
	Bytes    int64     `json:"bytes"`
	Blob     string    `json:"blob,omitempty"`
}

// ExchangeScope is who made the call: the fields every record carries, plus what
// the file name carried.
type ExchangeScope struct {
	Source  string `json:"source"`
	Host    string `json:"host"`
	PID     int    `json:"pid"`
	Proc    string `json:"proc,omitempty"`
	Project string `json:"project,omitempty"`
	Session string `json:"session,omitempty"`
	Worker  string `json:"worker,omitempty"`
	Run     string `json:"run,omitempty"`
	Agent   string `json:"agent,omitempty"`
}

// ExchangeCall is another call of the same day that was in flight while this one
// was.
type ExchangeCall struct {
	ID      string    `json:"id"`
	Day     string    `json:"day"`
	Session string    `json:"session,omitempty"`
	Agent   string    `json:"agent,omitempty"`
	T0      time.Time `json:"t0"`
	T1      time.Time `json:"t1"`
	Status  int       `json:"status"`
}

// ResponseSummary is what a response body carried, for display: the text, the
// reasoning, the tool calls, the finish reason and the usage, gathered from
// whichever protocol's event shapes the body used.
type ResponseSummary struct {
	Text         string            `json:"text"`
	Reasoning    string            `json:"reasoning"`
	ToolCalls    []SummaryToolCall `json:"tool_calls,omitempty"`
	FinishReason string            `json:"finish_reason,omitempty"`
	Usage        *SummaryUsage     `json:"usage,omitempty"`
	Events       int               `json:"events"`
}

// SummaryToolCall is one tool call a response asked for, with its arguments as
// the protocol sent them: a JSON string.
type SummaryToolCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// SummaryUsage is a response's token counts.
type SummaryUsage struct {
	Input  int `json:"input"`
	Output int `json:"output"`
}

// exchangePair is the pairing table's entry: what a scan needs from a call
// before the page asks for the one it opened.
type exchangePair struct {
	id             string
	day            string
	source, host   string
	proc           string
	pid            int
	session, agent string
	status         int
	t0, t1         time.Time
	request        *Record
	response       *Record
}

// ReadExchange gathers one model call out of the day it ran on. The request may
// sit in the previous day's directory (a call that began before midnight), so
// both are scanned; the response is in the day it ended.
//
// A missing call is fs.ErrNotExist; a day that is not a day is ErrBadLogRequest.
func ReadExchange(ctx context.Context, root, day, id string) (Exchange, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	start, ok := parseDayName(day)
	if !ok {
		return Exchange{}, fmt.Errorf("%w: day %q is not a day", ErrBadLogRequest, day)
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return Exchange{}, fmt.Errorf("%w: no call id", ErrBadLogRequest)
	}
	logsDir := filepath.Join(resolveRoot(root), home.LogsDir)
	pairs := make(map[string]*exchangePair)
	scanner := newLogLineScanner(eventLineMaxBytes)
	for offset := 0; offset <= 1; offset++ {
		if err := ctx.Err(); err != nil {
			return Exchange{}, err
		}
		name := start.AddDate(0, 0, -offset).Format(dayLayout)
		entries, err := os.ReadDir(filepath.Join(logsDir, name))
		if err != nil {
			continue // a day the sweep already took
		}
		for _, entry := range entries {
			if entry.IsDir() || isStderrLogName(entry.Name()) {
				continue
			}
			source, host, proc, pid, ok := parseLogFileName(entry.Name())
			if !ok {
				continue
			}
			file := eventSource{
				path:   filepath.Join(logsDir, name, entry.Name()),
				day:    name,
				name:   entry.Name(),
				source: source,
				host:   host,
				proc:   proc,
				pid:    pid,
			}
			if err := ctx.Err(); err != nil {
				return Exchange{}, err
			}
			scanExchangeFile(&file, scanner, pairs)
			if len(pairs) >= exchangeCallsMax {
				break
			}
		}
	}
	target, found := pairs[id]
	if !found {
		return Exchange{}, fmt.Errorf("no call %q on %s: %w", id, day, fs.ErrNotExist)
	}
	return buildExchange(target, pairs, root), nil
}

// scanExchangeFile pairs one file's llm records by call id. It reads every llm
// record of the file, not only the call asked for: the concurrency list needs
// the calls around it, and they are in the same records.
func scanExchangeFile(file *eventSource, scanner *logLineScanner, pairs map[string]*exchangePair) {
	handle, err := os.Open(file.path)
	if err != nil {
		return
	}
	defer func() { _ = handle.Close() }()
	scanner.reset(handle)
	marker := []byte(`"kind":"` + kindLLM + `"`)
	for {
		line, _, overlong, readErr := scanner.next()
		if !overlong && len(line) > 0 && bytes.Contains(line, marker) {
			if record, ok := decodeExchangeLine(line); ok {
				addExchangeRecord(file, record, pairs)
			}
		}
		if readErr != nil || len(pairs) >= exchangeCallsMax {
			return
		}
	}
}

// decodeExchangeLine decodes one line that says it is an llm record.
func decodeExchangeLine(line []byte) (Record, bool) {
	var stored struct {
		T string `json:"t"`
		Record
	}
	if err := json.Unmarshal(line, &stored); err != nil {
		return Record{}, false
	}
	if stored.Kind != kindLLM {
		return Record{}, false
	}
	if t, ok := parseEventTime(stored.T); ok {
		stored.Record.Time = t
	}
	return stored.Record, true
}

// addExchangeRecord files one record into the pairing table by its call id.
func addExchangeRecord(file *eventSource, record Record, pairs map[string]*exchangePair) {
	id, _ := record.Attrs["x"].(string)
	if id == "" {
		return
	}
	pair, seen := pairs[id]
	if !seen {
		pair = &exchangePair{
			id: id, day: file.day, source: file.source, host: file.host,
			proc: file.proc, pid: file.pid,
		}
		pairs[id] = pair
	}
	if pair.session == "" {
		pair.session = record.Session
	}
	if pair.agent == "" {
		pair.agent = record.Agent
	}
	switch record.Msg {
	case "llm request":
		pair.t0 = record.Time
		if pair.request == nil {
			pair.request = &record
		}
	case "llm response":
		pair.t1 = record.Time
		if pair.response == nil {
			pair.response = &record
		}
		if status, ok := attrInt(record.Attrs["status"]); ok {
			pair.status = status
		}
	}
}

// buildExchange renders the call the page opened, plus the calls around it.
func buildExchange(target *exchangePair, pairs map[string]*exchangePair, root string) Exchange {
	out := Exchange{
		ID: target.id, Day: target.day,
		Scope: ExchangeScope{
			Source: target.source, Host: target.host, PID: target.pid, Proc: target.proc,
			Session: target.session, Agent: target.agent,
		},
	}
	lead := target.request
	if lead == nil {
		lead = target.response
	}
	if lead != nil {
		out.Scope.Project, out.Scope.Worker, out.Scope.Run = lead.Project, lead.Worker, lead.Run
		if out.Scope.Session == "" {
			out.Scope.Session = lead.Session
		}
		if out.Scope.Agent == "" {
			out.Scope.Agent = lead.Agent
		}
	}
	if target.request != nil {
		record := target.request
		out.Request = &ExchangePart{
			T:      record.Time,
			Method: attrText(record.Attrs["method"]),
			URL:    attrText(record.Attrs["url"]),
			Model:  attrText(record.Attrs["model"]),
			Bytes:  attrInt64Or(record.Attrs["req_bytes"]),
		}
		if ref, ok := record.Blobs["req"]; ok {
			out.Request.Blob, out.Request.Whole, out.Request.Omitted = ref.Name, ref.Whole, ref.Omitted
		}
	}
	var responseBlob BlobRef
	if target.response != nil {
		record := target.response
		status, _ := attrInt(record.Attrs["status"])
		out.Response = &ExchangeReply{
			T:        record.Time,
			Status:   status,
			Outcome:  attrText(record.Attrs["outcome"]),
			DurMs:    attrInt64Or(record.Attrs["dur_ms"]),
			TTFBMs:   attrInt64Or(record.Attrs["ttfb_ms"]),
			MaxGapMs: attrInt64Or(record.Attrs["max_gap_ms"]),
			Bytes:    attrInt64Or(record.Attrs["resp_bytes"]),
		}
		if ref, ok := record.Blobs["resp"]; ok {
			out.Response.Blob = ref.Name
			responseBlob = ref
		}
	}
	// A summary is what a reader wants from a call that succeeded: an error's
	// body is already in the reply's status and outcome.
	if out.Response != nil && out.Response.Status >= 200 && out.Response.Status < 300 && responseBlob.Name != "" {
		if body, err := readExchangeBody(root, target.day, responseBlob.Name); err == nil {
			out.Summary = SummarizeResponse(body)
		}
	}
	out.Concurrent = concurrentCalls(target, pairs)
	return out
}

// concurrentCalls lists the calls whose time span overlaps this one's, oldest
// first: the page draws what else was in flight, which is how a slow call is
// told from a busy one.
func concurrentCalls(target *exchangePair, pairs map[string]*exchangePair) []ExchangeCall {
	start, end := target.t0, target.t1
	if start.IsZero() {
		start = end
	}
	if end.IsZero() {
		// A call with no response record is still running, as far as the log
		// knows: it overlaps everything up to now.
		end = time.Now()
	}
	if start.IsZero() {
		return []ExchangeCall{}
	}
	overlaps := make([]ExchangeCall, 0, exchangeConcurrentMax)
	for id, other := range pairs {
		if id == target.id {
			continue
		}
		otherStart, otherEnd := other.t0, other.t1
		if otherStart.IsZero() {
			otherStart = otherEnd
		}
		if otherEnd.IsZero() {
			otherEnd = end
		}
		if otherStart.IsZero() {
			continue
		}
		if !otherStart.Before(end) || !start.Before(otherEnd) {
			continue
		}
		overlaps = append(overlaps, ExchangeCall{
			ID: other.id, Day: other.day, Session: other.session, Agent: other.agent,
			T0: otherStart, T1: otherEnd, Status: other.status,
		})
	}
	sort.Slice(overlaps, func(i, j int) bool {
		if !overlaps[i].T0.Equal(overlaps[j].T0) {
			return overlaps[i].T0.Before(overlaps[j].T0)
		}
		return overlaps[i].ID < overlaps[j].ID
	})
	return overlaps[:min(len(overlaps), exchangeConcurrentMax)]
}

// ---- one payload, as a stream --------------------------------------------

// OpenBlob opens one payload file of a day's x/ and returns it as a stream,
// together with the number of bytes it holds once decompressed.
//
// The name and the day are validated before anything touches the filesystem: the
// day must be a day directory's name, the name must be one file name ending in
// .gz with no parent reference, and the path that results must still be inside
// that day's x/. A symlink where the payload should be is refused rather than
// followed: what a request names is a file 286 wrote, not a door.
func OpenBlob(root, day, name string) (io.ReadCloser, int64, error) {
	path, err := blobFilePath(root, day, name)
	if err != nil {
		return nil, 0, err
	}
	total, err := countBlobBytes(path)
	if err != nil {
		return nil, 0, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	reader, err := gzip.NewReader(file)
	if err != nil {
		_ = file.Close()
		return nil, 0, err
	}
	return &blobStream{reader: reader, file: file}, total, nil
}

// blobFilePath is the whole of OpenBlob's validation, and it runs before any
// filesystem call.
func blobFilePath(root, day, name string) (string, error) {
	if _, ok := parseDayName(day); !ok {
		return "", fmt.Errorf("%w: day %q is not a day", ErrBadLogRequest, day)
	}
	if !blobNameRe.MatchString(name) || strings.Contains(name, "..") {
		return "", fmt.Errorf("%w: name %q is not a payload file", ErrBadLogRequest, name)
	}
	dir := filepath.Join(resolveRoot(root), home.LogsDir, day, blobDirName)
	path := filepath.Clean(filepath.Join(dir, name))
	if !strings.HasPrefix(path, dir+string(os.PathSeparator)) {
		return "", fmt.Errorf("%w: name %q leaves its day", ErrBadLogRequest, name)
	}
	// Lstat, not Stat: a symlink under x/ is refused instead of resolved, so a
	// name can only ever name the file 286 wrote there.
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%w: %q is not a regular file", ErrBadLogRequest, name)
	}
	return path, nil
}

// countBlobBytes is the payload's decompressed size. A stream that is still
// being written ends in io.ErrUnexpectedEOF, and the bytes before it are the
// answer: that is the payload as it stands now, which is exactly what a reader
// asking where a stuck stream stopped needs.
func countBlobBytes(path string) (int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = file.Close() }()
	reader, err := gzip.NewReader(file)
	if err != nil {
		return 0, err
	}
	defer func() { _ = reader.Close() }()
	total, err := io.Copy(io.Discard, reader)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return 0, err
	}
	return total, nil
}

// blobStream is one open payload: the decompressor and the file behind it.
type blobStream struct {
	reader *gzip.Reader
	file   *os.File
}

func (b *blobStream) Read(p []byte) (int, error) { return b.reader.Read(p) }

func (b *blobStream) Close() error {
	_ = b.reader.Close()
	return b.file.Close()
}

// readExchangeBody is the body of one payload file: everything after the blank
// line that separates the status line and headers from the body. It reads at
// most exchangeSummaryMaxBytes, because a summary is a display and the file is
// the record.
func readExchangeBody(root, day, name string) ([]byte, error) {
	if _, ok := parseDayName(day); !ok {
		return nil, fmt.Errorf("%w: day %q is not a day", ErrBadLogRequest, day)
	}
	if !blobNameRe.MatchString(name) || strings.Contains(name, "..") {
		return nil, fmt.Errorf("%w: name %q is not a payload file", ErrBadLogRequest, name)
	}
	path := filepath.Join(resolveRoot(root), home.LogsDir, day, blobDirName, name)
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	reader, err := gzip.NewReader(file)
	if err != nil {
		return nil, err
	}
	defer func() { _ = reader.Close() }()
	payload, err := io.ReadAll(io.LimitReader(reader, exchangeSummaryMaxBytes))
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, err
	}
	if blank := bytes.Index(payload, []byte("\n\n")); blank >= 0 {
		return payload[blank+2:], nil
	}
	return payload, nil
}

// ---- what a response carried ---------------------------------------------

// SummarizeResponse reads a response body as the display of what came back: the
// text, the reasoning, the tool calls, the finish reason and the usage, from the
// bodies of the four protocols the adapters speak (OpenAI-compatible, Anthropic,
// Responses and Gemini).
//
// This is a best-effort reading for the page, not a second adapter: every value
// is optional, an event of a shape it does not know is only counted, and nothing
// here returns an error. The adapters remain the only thing that decides what a
// stream means — if a provider changes its stream, this shows less and nothing
// else changes.
func SummarizeResponse(body []byte) *ResponseSummary {
	summary := &ResponseSummary{}
	sawData := false
	for _, raw := range bytes.Split(body, []byte("\n")) {
		line := bytes.TrimSpace(raw)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		sawData = true
		summary.consume(bytes.TrimSpace(line[len("data:"):]))
	}
	if !sawData {
		// Not a server-sent-event stream: a non-streamed reply is one JSON
		// document, which is one event.
		summary.consume(bytes.TrimSpace(body))
	}
	summary.pruneToolCalls()
	if summary.empty() {
		return nil
	}
	return summary
}

// pruneToolCalls drops the placeholders an index-keyed protocol leaves behind.
// Anthropic numbers its content blocks, so a tool_use that starts at block 2
// grows two empty entries below it; the page shows calls, not block numbers.
func (s *ResponseSummary) pruneToolCalls() {
	kept := s.ToolCalls[:0]
	for _, call := range s.ToolCalls {
		if call.Name == "" && call.Arguments == "" {
			continue
		}
		kept = append(kept, call)
	}
	s.ToolCalls = kept
	if len(s.ToolCalls) == 0 {
		s.ToolCalls = nil
	}
}

// empty reports a summary that found nothing to show, which the page renders as
// no summary at all.
func (s *ResponseSummary) empty() bool {
	return s.Text == "" && s.Reasoning == "" && len(s.ToolCalls) == 0 && s.FinishReason == "" && s.Usage == nil
}

// consume reads one event, in whichever protocol's shape it arrived.
func (s *ResponseSummary) consume(payload []byte) {
	if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
		return
	}
	var event map[string]any
	if err := json.Unmarshal(payload, &event); err != nil {
		return
	}
	s.Events++
	s.consumeOpenAI(event)
	s.consumeAnthropic(event)
	s.consumeResponses(event)
	s.consumeGemini(event)
}

// consumeOpenAI reads choices[0]'s delta: content, the reasoning field the
// compatible providers spell two ways, the tool calls that arrive in pieces, the
// finish reason and the usage.
func (s *ResponseSummary) consumeOpenAI(event map[string]any) {
	choice, ok := firstOf(event, "choices")
	if !ok {
		return
	}
	if delta, ok := choice["delta"].(map[string]any); ok {
		s.Text += attrText(delta["content"])
		if reasoning := attrText(delta["reasoning_content"]); reasoning != "" {
			s.Reasoning += reasoning
		} else {
			s.Reasoning += attrText(delta["reasoning"])
		}
		for _, raw := range listOf(delta["tool_calls"]) {
			call, _ := raw.(map[string]any)
			index, _ := attrInt(call["index"])
			function, _ := call["function"].(map[string]any)
			if function == nil {
				continue
			}
			target := s.toolCallAt(index)
			if name := attrText(function["name"]); name != "" {
				target.Name = name
			}
			target.Arguments += attrText(function["arguments"])
		}
	}
	if reason := attrText(choice["finish_reason"]); reason != "" {
		s.FinishReason = reason
	}
	if usage, ok := event["usage"].(map[string]any); ok {
		s.setUsage(usage["prompt_tokens"], usage["completion_tokens"])
	}
}

// consumeAnthropic reads the content-block events: text and thinking deltas, the
// tool_use blocks and their partial JSON, and the stop reason and usage that
// arrive with message_delta.
func (s *ResponseSummary) consumeAnthropic(event map[string]any) {
	switch attrText(event["type"]) {
	case "content_block_delta":
		delta, _ := event["delta"].(map[string]any)
		if delta == nil {
			return
		}
		switch attrText(delta["type"]) {
		case "text_delta":
			s.Text += attrText(delta["text"])
		case "thinking_delta":
			s.Reasoning += attrText(delta["thinking"])
		case "input_json_delta":
			index, _ := attrInt(event["index"])
			s.toolCallAt(index).Arguments += attrText(delta["partial_json"])
		}
	case "content_block_start":
		block, _ := event["content_block"].(map[string]any)
		if attrText(block["type"]) != "tool_use" {
			return
		}
		index, _ := attrInt(event["index"])
		s.toolCallAt(index).Name = attrText(block["name"])
		if input, ok := block["input"].(map[string]any); ok && len(input) > 0 {
			if raw, err := json.Marshal(input); err == nil {
				s.toolCallAt(index).Arguments += string(raw)
			}
		}
	case "message_delta":
		delta, _ := event["delta"].(map[string]any)
		if reason := attrText(delta["stop_reason"]); reason != "" {
			s.FinishReason = reason
		}
		if usage, ok := event["usage"].(map[string]any); ok {
			s.setUsage(usage["input_tokens"], usage["output_tokens"])
		}
	}
}

// consumeResponses reads the Responses API's events: text and reasoning-summary
// deltas, a finished function_call item, and the completed response's status and
// usage.
func (s *ResponseSummary) consumeResponses(event map[string]any) {
	switch attrText(event["type"]) {
	case "response.output_text.delta":
		s.Text += attrText(event["delta"])
	case "response.reasoning_summary_text.delta":
		s.Reasoning += attrText(event["delta"])
	case "response.output_item.done":
		item, _ := event["item"].(map[string]any)
		if attrText(item["type"]) != "function_call" {
			return
		}
		name := attrText(item["name"])
		if name == "" {
			return
		}
		s.ToolCalls = append(s.ToolCalls, SummaryToolCall{Name: name, Arguments: attrText(item["arguments"])})
	case "response.completed":
		response, _ := event["response"].(map[string]any)
		if status := attrText(response["status"]); status != "" {
			s.FinishReason = status
		}
		if usage, ok := response["usage"].(map[string]any); ok {
			s.setUsage(usage["input_tokens"], usage["output_tokens"])
		}
	}
}

// consumeGemini reads the candidates of one Gemini chunk: its parts (text or
// thought, a function call), the finish reason and the usage metadata.
func (s *ResponseSummary) consumeGemini(event map[string]any) {
	candidate, ok := firstOf(event, "candidates")
	if !ok {
		return
	}
	content, _ := candidate["content"].(map[string]any)
	for _, raw := range listOf(content["parts"]) {
		part, _ := raw.(map[string]any)
		if part == nil {
			continue
		}
		if call, ok := part["functionCall"].(map[string]any); ok {
			name := attrText(call["name"])
			if name == "" {
				continue
			}
			arguments := ""
			if raw, err := json.Marshal(call["args"]); err == nil && string(raw) != "null" {
				arguments = string(raw)
			}
			s.ToolCalls = append(s.ToolCalls, SummaryToolCall{Name: name, Arguments: arguments})
			continue
		}
		text := attrText(part["text"])
		if text == "" {
			continue
		}
		if thought, _ := part["thought"].(bool); thought {
			s.Reasoning += text
		} else {
			s.Text += text
		}
	}
	if reason := attrText(candidate["finishReason"]); reason != "" {
		s.FinishReason = reason
	}
	if usage, ok := event["usageMetadata"].(map[string]any); ok {
		s.setUsage(usage["promptTokenCount"], usage["candidatesTokenCount"])
	}
}

// toolCallAt is the call an event names by index, grown as far as needed: a
// streamed call's name and arguments arrive in separate events and are joined
// here.
func (s *ResponseSummary) toolCallAt(index int) *SummaryToolCall {
	for len(s.ToolCalls) <= index {
		s.ToolCalls = append(s.ToolCalls, SummaryToolCall{})
	}
	return &s.ToolCalls[index]
}

// setUsage records token counts, replacing the previous snapshot rather than
// adding to it: providers repeat the running totals on every event, and an event
// that names only one of the two keeps the other.
func (s *ResponseSummary) setUsage(input, output any) {
	in, hasIn := attrInt(input)
	out, hasOut := attrInt(output)
	if !hasIn && !hasOut {
		return
	}
	if s.Usage == nil {
		s.Usage = &SummaryUsage{}
	}
	if hasIn {
		s.Usage.Input = in
	}
	if hasOut {
		s.Usage.Output = out
	}
}

// attrText reads a JSON string value, "" for anything else.
func attrText(value any) string {
	text, _ := value.(string)
	return text
}

// listOf reads a JSON array, nil for anything else.
func listOf(value any) []any {
	list, _ := value.([]any)
	return list
}

// firstOf reads the first element of a JSON array under key.
func firstOf(event map[string]any, key string) (map[string]any, bool) {
	list := listOf(event[key])
	if len(list) == 0 {
		return nil, false
	}
	first, ok := list[0].(map[string]any)
	return first, ok
}

// attrInt reads a JSON number that is one, whichever of the forms it arrived in:
// an attribute in memory is an int, and the same attribute read back from a line
// is a float64.
func attrInt(value any) (int, bool) {
	switch number := value.(type) {
	case int:
		return number, true
	case int64:
		return int(number), true
	case float64:
		return int(number), true
	case json.Number:
		if parsed, err := number.Int64(); err == nil {
			return int(parsed), true
		}
	}
	return 0, false
}

// attrInt64Or is attrInt for the durations and byte counts, 0 when absent.
func attrInt64Or(value any) int64 {
	number, _ := attrInt(value)
	return int64(number)
}
