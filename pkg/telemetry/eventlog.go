// The structured event log: one JSON record per line, one file per process, one
// directory per local day.
//
// Every invariant below exists because the text logs this replaces broke it, and
// each break cost a record nobody could attribute or read (plans/284):
//
//   - One writer per file. The name carries the source, the host, the process id
//     and a random suffix drawn at startup, so two processes never open the same
//     path. Without that, four processes interleaved their records in one debug.log
//     and nothing in a block said which process wrote it.
//   - One record, one write(2). A whole line reaches the descriptor in a single
//     unbuffered write, and there is deliberately no fsync — the reasoning is
//     File.Write's: a diagnostic log buys nothing from a disk barrier, and
//     write(2) already survives process death.
//   - Append only, never renamed. A file past eventFileMaxBytes continues in the
//     next numbered part; the day directory keeps every part. Nothing is renamed
//     or truncated underneath a reader, so the copy-and-truncate rotation that
//     moved two backups at once and lost 20MB cannot happen here.
package telemetry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/home"
)

const (
	// LogRetentionDays is how many days of event logs a home keeps.
	LogRetentionDays = 7
	// LogBudgetBytes caps the whole log tree. Past it SweepLogs deletes payload
	// files first (the bulky part) and only then whole days.
	LogBudgetBytes = int64(1) << 30

	// recordMaxBytes is the ceiling for one encoded line. A record that would
	// cross it loses its Attrs — replaced by a marker — never its validity: a
	// line cut mid-JSON is a record no query can read back.
	recordMaxBytes = 64 * 1024
	msgMaxBytes    = 8 * 1024
	errMaxBytes    = 16 * 1024
	attrMaxBytes   = 8 * 1024
	// stackAttrMaxBytes is the one attribute allowed to be longer: a panic's
	// call stack is the part of that record worth reading whole.
	stackAttrMaxBytes = 32 * 1024

	// eventTimeLayout is RFC3339 with milliseconds: sortable as text, readable by
	// every log tool, and precise enough to order records from two processes.
	eventTimeLayout = "2006-01-02T15:04:05.000Z07:00"
	dayLayout       = "2006-01-02"
)

// The two limits a test lowers are variables, so a test can drive the rollover
// and the budget without writing 32MB or 1GiB. Production reads them unchanged.
//
// They are unexported on purpose: the retention policy is fixed (owner ruling
// 2026-11-11 — no configuration for a consumer product). LogRetentionDays and
// LogBudgetBytes are the published numbers.
var (
	eventFileMaxBytes = int64(32) << 20
	logBudgetBytes    = LogBudgetBytes
)

// Record is one line of the event log. Field order is the on-disk key order:
// time, level, kind and message come first so a plain `tail -f` still reads.
//
// The source, host, process id and per-process random number are NOT here: they
// are in the file name, which saves ~60 bytes per record and lets a reader
// restore them once per file instead of per line.
type Record struct {
	Time    time.Time          `json:"-"`    // written as "t", RFC3339 with milliseconds and an offset
	Level   string             `json:"lvl"`  // DEBUG | INFO | WARN | ERROR
	Kind    string             `json:"kind"` // log | turn | llm | tool | http | setup | panic
	Msg     string             `json:"msg"`
	Project string             `json:"project,omitempty"` // project key
	Session string             `json:"session,omitempty"` // the user-visible session
	Worker  string             `json:"worker,omitempty"`  // the subagent's own transcript session, when it differs from Session
	Run     string             `json:"run,omitempty"`
	Agent   string             `json:"agent,omitempty"`
	At      string             `json:"at,omitempty"` // repo-relative path:line, e.g. pkg/run/runner.go:1356
	Err     string             `json:"err,omitempty"`
	Attrs   map[string]any     `json:"attrs,omitempty"`
	Blobs   map[string]BlobRef `json:"blobs,omitempty"`
}

// BlobRef points at one payload file under the same day directory's x/.
type BlobRef struct {
	Name    string `json:"name"`              // file name, e.g. a3f09c-000391.req.gz
	Bytes   int64  `json:"bytes"`             // uncompressed size
	Whole   bool   `json:"whole"`             // false = only the head and tail were kept
	Omitted int64  `json:"omitted,omitempty"` // bytes left out
}

// EventLog is this process's event log: one file per local day, continued into
// numbered parts, and the sweep that keeps the tree within its retention window.
//
// A nil *EventLog accepts every call and records nothing, so a caller that has
// no log configured keeps its hot path free of nil checks.
type EventLog struct {
	root   string
	source string
	proc   string
	pid    int

	mu   sync.Mutex
	day  string
	part int
	file *os.File
	// size counts what this process wrote to the open part. This process is the
	// only writer of that path, so the count is exact and no per-record stat is
	// needed; it is seeded once from the file's own size when the part is opened.
	size int64
	// dropped counts records that never reached the file. Write never returns an
	// error and never panics (a TUI's stderr is redirected, so there is nowhere
	// to complain), so the count rides out on the next record that does land.
	dropped atomic.Int64

	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

// OpenEventLog opens this process's event log under <root>/logs. source is
// "tui", "gateway" or "cli". The file itself is created on the first Write, in
// the directory of that record's local date.
//
// The retention sweep runs once before this returns and then hourly until Close.
func OpenEventLog(root, source string) *EventLog {
	l := &EventLog{
		root:   resolveRoot(root),
		source: source,
		proc:   randomProc(),
		pid:    os.Getpid(),
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	SweepLogs(l.root, time.Now())
	go l.sweepLoop()
	return l
}

// Proc is this process's six hex digits, the value 286 uses to build call ids.
// It is also in every file name, so a payload written by 286 can be tied back
// to the records around it.
func (l *EventLog) Proc() string {
	if l == nil {
		return ""
	}
	return l.proc
}

// Write appends one record.
//
// It never returns an error and never panics: a log directory that cannot be
// written is a diagnostic problem, not a reason to fail the model request or the
// HTTP response on this path. Losses are counted and reported by the next record
// that does land, in Attrs["dropped_before"].
func (l *EventLog) Write(rec Record) {
	if l == nil {
		return
	}
	if rec.Time.IsZero() {
		rec.Time = time.Now()
	}
	// Take the loss count before encoding: that is what lets this record carry it.
	// A record that fails to encode or write puts the count back, plus itself.
	before := l.dropped.Swap(0)
	line, err := l.encode(&rec, before)
	if err == nil {
		l.mu.Lock()
		err = l.writeLocked(rec.Time, line)
		l.mu.Unlock()
	}
	if err != nil {
		l.dropped.Add(before + 1)
	}
}

// BlobPath returns <root>/logs/<day>/x/<name> and creates its directory. It is
// where a payload too big for a record goes (286: model request and response
// bodies), referenced from the record's Blobs.
func (l *EventLog) BlobPath(t time.Time, name string) (string, error) {
	if l == nil {
		return "", errors.New("event log is nil")
	}
	path := filepath.Join(l.root, home.LogsDir, localDay(t), blobDirName, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	return path, nil
}

// Close stops the hourly sweep, waits for it to exit, and closes the open part.
// It is idempotent.
func (l *EventLog) Close() error {
	if l == nil {
		return nil
	}
	// A zero EventLog never started the sweep, so it has both channels nil: there
	// is nothing to stop and nothing to wait for. Closing a nil channel panics and
	// waiting on one never returns.
	if l.stop != nil && l.done != nil {
		l.stopOnce.Do(func() { close(l.stop) })
		<-l.done
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.closeFileLocked()
}

// writeLocked opens the part this record belongs in and appends the encoded line
// in one write(2).
func (l *EventLog) writeLocked(t time.Time, line []byte) error {
	if err := l.rotateLocked(t); err != nil {
		return err
	}
	n, err := l.file.Write(line)
	l.size += int64(n)
	return err
}

// rotateLocked opens a new part when the record belongs in a new day directory or
// the current part has reached eventFileMaxBytes. Records are the only thing that
// moves a part forward, so the file a reader has open keeps growing and is never
// replaced.
func (l *EventLog) rotateLocked(t time.Time) error {
	day := localDay(t)
	if l.file != nil && l.day == day && l.size < eventFileMaxBytes {
		return nil
	}
	if l.file != nil && l.day == day {
		l.part++
	} else {
		l.day = day
		l.part = 0
	}
	_ = l.closeFileLocked()
	dir := filepath.Join(l.root, home.LogsDir, day)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(dir, l.partName()), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	l.file = file
	l.size = 0
	// Seed the count from the file itself: one stat per part, not per record. The
	// name is unique to this process, so the file is normally new and empty.
	if info, statErr := file.Stat(); statErr == nil {
		l.size = info.Size()
	}
	return nil
}

func (l *EventLog) closeFileLocked() error {
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	l.size = 0
	return err
}

// partName is <source>-<host>-<pid>-<proc>.jsonl and, past the size limit,
// the same plus .<n> before the extension.
func (l *EventLog) partName() string {
	base := fmt.Sprintf("%s-%s-%d-%s", l.source, HostLabel(), l.pid, l.proc)
	if l.part == 0 {
		return base + ".jsonl"
	}
	return fmt.Sprintf("%s.%d.jsonl", base, l.part)
}

// sweepLoop keeps the tree inside its retention window for as long as this
// process lives. Cleanup is idempotent and takes no lock, so several processes
// sweeping the same home at once is harmless (plans/284: a shared RWX home).
func (l *EventLog) sweepLoop() {
	defer close(l.done)
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			SweepLogs(l.root, time.Now())
		case <-l.stop:
			return
		}
	}
}

// errUnencodable reports a record json cannot carry, because Attrs holds a value
// with no JSON form (a channel, a function). The record is counted as lost rather
// than written as a line no reader can parse.
var errUnencodable = errors.New("event record is not JSON-encodable")

// encode renders one record as a line, bounded and redacted. droppedBefore is
// folded into Attrs so the record itself reports what was lost before it.
func (l *EventLog) encode(rec *Record, droppedBefore int64) ([]byte, error) {
	attrs := cloneAttrs(rec.Attrs)
	if droppedBefore > 0 {
		if attrs == nil {
			attrs = map[string]any{}
		}
		attrs[attrDroppedBefore] = droppedBefore
	}
	// Bound every string value with BoundText rather than slicing: a value that
	// is cut mid-rune or mid-JSON is unreadable, while BoundText keeps both ends
	// and states how many bytes it left out.
	for key, value := range attrs {
		text, ok := value.(string)
		if !ok {
			continue
		}
		limit := attrMaxBytes
		if key == attrStack {
			limit = stackAttrMaxBytes
		}
		attrs[key] = BoundText(RedactLogLine(text), limit)
	}
	out := Record{
		Time:    rec.Time,
		Level:   rec.Level,
		Kind:    rec.Kind,
		Msg:     BoundText(RedactLogLine(rec.Msg), msgMaxBytes),
		Project: rec.Project,
		Session: rec.Session,
		Worker:  rec.Worker,
		Run:     rec.Run,
		Agent:   rec.Agent,
		At:      rec.At,
		Err:     BoundText(RedactLogLine(rec.Err), errMaxBytes),
		Attrs:   attrs,
		Blobs:   rec.Blobs,
	}
	// "t" is written by hand and the marshalled object's opening brace dropped, so
	// time stays the first key: a record read by eye starts with when it happened.
	head := `{"t":"` + out.Time.Format(eventTimeLayout) + `",`
	body, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errUnencodable, err)
	}
	if len(head)+len(body) > recordMaxBytes {
		// The fields are already bounded, so this is a record with a very wide
		// Attrs map. Its identity and message survive; the map is replaced by a
		// marker rather than cut into invalid JSON.
		out.Attrs = map[string]any{attrTruncated: true, "bytes": len(head) + len(body)}
		if body, err = json.Marshal(out); err != nil {
			return nil, fmt.Errorf("%w: %v", errUnencodable, err)
		}
	}
	line := make([]byte, 0, len(head)+len(body))
	line = append(line, head...)
	line = append(line, body[1:]...) // drop the opening brace json.Marshal added
	return append(line, '\n'), nil
}

const (
	blobDirName       = "x"
	attrDroppedBefore = "dropped_before"
	attrStack         = "stack"
	attrTruncated     = "truncated"
)

// localDay is the day directory one record belongs in: the writer's local date.
func localDay(t time.Time) string {
	if t.IsZero() {
		t = time.Now()
	}
	return t.In(time.Local).Format(dayLayout)
}

// HostLabel is this machine's name, reduced to the characters a file name keeps
// on every platform, or "local" when it has none. It is the same rule the
// gateway's per-replica log name used (cmd/forebrain/gateway_log.go), moved here
// so the event log's file names and the legacy names agree while both exist.
func HostLabel() string {
	host, err := os.Hostname()
	if err != nil {
		return "local"
	}
	var b strings.Builder
	for _, r := range strings.TrimSpace(host) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	name := strings.Trim(b.String(), "-.")
	if name == "" {
		return "local"
	}
	return name
}

// randomProc draws the six hex digits that make this process's file name unique
// even beside another process with the same host and pid (two containers sharing
// one RWX home). crypto/rand keeps them unrelated to the clock; if the system
// cannot supply randomness at all, the clock still separates two start-ups.
func randomProc() string {
	var b [3]byte
	if _, err := rand.Read(b[:]); err != nil {
		n := uint32(time.Now().UnixNano())
		b = [3]byte{byte(n), byte(n >> 8), byte(n >> 16)}
	}
	return hex.EncodeToString(b[:])
}

// ---- retention -----------------------------------------------------------

// dayNameRe is the only thing SweepLogs will delete a whole directory for: a
// day directory this log wrote. Anything else in <root>/logs belongs to someone
// else (a person's notes, the legacy logs) and is never touched.
var dayNameRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// legacyLogPatterns are the file logs this event log replaces. They are deleted
// once stale; after 285/286 nothing writes them.
var legacyLogPatterns = []string{"debug.log*", "info.log*", "error.log*", "gateway.log*", "gateway-*.log*"}

// SweepLogs applies the retention policy to <root>/logs: drop day directories
// older than LogRetentionDays, keep the tree under LogBudgetBytes by deleting
// payload files first and whole days second, and remove stale files of the old
// log format.
//
// Every step is an idempotent delete that ignores what has already gone, so
// several processes may sweep the same home concurrently.
func SweepLogs(root string, now time.Time) {
	logsDir := filepath.Join(resolveRoot(root), home.LogsDir)
	entries, err := os.ReadDir(logsDir)
	if err != nil {
		return // no log tree yet: nothing to keep back
	}
	// Both arms compare against the same instant, now minus LogRetentionDays: a
	// day directory as its own date, a legacy file as its modification time. A
	// directory dated D-7 starts before that instant, so it goes; D-6..D0 stay.
	cutoff := now.AddDate(0, 0, -LogRetentionDays)

	var days []string // oldest first; the day name sorts as its date
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		day, ok := parseDayName(entry.Name())
		if !ok {
			continue // not a day directory: not ours to delete
		}
		path := filepath.Join(logsDir, entry.Name())
		if day.Before(cutoff) {
			_ = os.RemoveAll(path)
			continue
		}
		days = append(days, path)
	}
	sort.Strings(days)

	// Payloads first: they are what fills the disk, and a record with no payload
	// still says what happened.
	sweepPayloads(days)

	// Then whole days, oldest first. Today's records always stay: they are the
	// ones being written right now and the ones a person asks about.
	today := localDay(now)
	for _, path := range days {
		if treeSize(days) <= logBudgetBytes {
			break
		}
		if filepath.Base(path) == today {
			continue
		}
		_ = os.RemoveAll(path)
	}
	sweepLegacyFiles(logsDir, entries, cutoff)
}

// sweepPayloads deletes each day's x/ directory, oldest day first, until the tree
// fits the budget again.
func sweepPayloads(days []string) {
	for _, path := range days {
		if treeSize(days) <= logBudgetBytes {
			return
		}
		payload := filepath.Join(path, blobDirName)
		if dirSize(payload) == 0 {
			continue
		}
		_ = os.RemoveAll(payload)
	}
}

// sweepLegacyFiles deletes the old per-file logs once they are older than the
// retention window. They are not replaced (owner ruling 2026-11-11: no dual
// write), so this is the only thing that ever removes them.
func sweepLegacyFiles(logsDir string, entries []os.DirEntry, cutoff time.Time) {
	for _, entry := range entries {
		if entry.IsDir() || !isLegacyLogName(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(logsDir, entry.Name()))
	}
}

func isLegacyLogName(name string) bool {
	for _, pattern := range legacyLogPatterns {
		if ok, err := filepath.Match(pattern, name); err == nil && ok {
			return true
		}
	}
	return false
}

func parseDayName(name string) (time.Time, bool) {
	if !dayNameRe.MatchString(name) {
		return time.Time{}, false
	}
	day, err := time.ParseInLocation(dayLayout, name, time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return day, true
}

func treeSize(paths []string) int64 {
	var total int64
	for _, path := range paths {
		total += dirSize(path)
	}
	return total
}

// dirSize sums the regular files under path, and reports 0 for a path that has
// already been deleted by this or another process.
func dirSize(path string) int64 {
	var total int64
	_ = filepath.WalkDir(path, func(_ string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		if info, infoErr := entry.Info(); infoErr == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

// ---- identity and the slog adapter ---------------------------------------

// Scope is who a record belongs to. pkg/telemetry cannot import pkg/tool (tool
// imports telemetry), so the composition root registers the function that reads
// it off a context — the same port shape as llm.SetHTTPTransportWrapper.
type Scope struct{ Project, Session, Worker, Run, Agent string }

var (
	scopeFn         atomic.Pointer[func(ctx context.Context) Scope]
	defaultEventLog atomic.Pointer[EventLog]
)

// RegisterScope installs the one function that reads a record's identity off a
// context. Passing nil clears it, which leaves records with only the identity
// their explicit attributes carry.
func RegisterScope(fn func(ctx context.Context) Scope) {
	if fn == nil {
		scopeFn.Store(nil)
		return
	}
	scopeFn.Store(&fn)
}

func scopeFor(ctx context.Context) Scope {
	if fn := scopeFn.Load(); fn != nil {
		return (*fn)(ctx)
	}
	return Scope{}
}

// SetDefaultEventLog installs this process's event log. Passing nil means the
// process records no events; it is a process-wide setting, written once at
// startup and read from every goroutine that logs.
func SetDefaultEventLog(l *EventLog) {
	if l == nil {
		defaultEventLog.Store(nil)
		return
	}
	defaultEventLog.Store(l)
}

// DefaultEventLog is the log Emit and NewEventHandler write through.
func DefaultEventLog() *EventLog {
	return defaultEventLog.Load()
}

// Emit writes one record through the default log. It is the entry for callers
// that are not slog (turn lifecycle, model exchanges, tool calls, setup).
//
// Identity is taken from the registered Scope and then overridden by an explicit
// attribute of the same name, so a call site that knows better than the context
// wins.
func Emit(ctx context.Context, level slog.Level, kind, msg string, attrs ...slog.Attr) {
	l := DefaultEventLog()
	if l == nil {
		return // no log configured: keep the caller's path free of the work below
	}
	rec := Record{Time: time.Now(), Level: level.String(), Kind: kind, Msg: msg}
	adoptScope(&rec, scopeFor(ctx))
	if _, file, line, ok := runtime.Caller(1); ok {
		rec.At = atLocation(file, line)
	}
	for _, attr := range attrs {
		applyAttr(&rec, attr, "", false)
	}
	l.Write(rec)
}

// NewEventHandler adapts slog to the event log: every slog record becomes a
// kind="log" record, or the kind its own "kind" attribute names (285's access
// log tags its records "http" that way).
func NewEventHandler(l *EventLog, level slog.Leveler) slog.Handler {
	if level == nil {
		level = slog.LevelInfo
	}
	return &eventHandler{log: l, level: level}
}

// eventHandler carries the attributes and group prefix a logger bound with
// WithAttrs and WithGroup. Bound attributes are folded into a Record template at
// bind time, which is what freezes the prefix they were bound under: a WithGroup
// after WithAttrs must not re-prefix what came before.
type eventHandler struct {
	log    *EventLog
	level  slog.Leveler
	prefix string // group path in effect, "http." or ""
	bound  Record
}

func (h *eventHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level.Level()
}

func (h *eventHandler) Handle(ctx context.Context, r slog.Record) error {
	if h.log == nil || !h.Enabled(ctx, r.Level) {
		return nil
	}
	rec := Record{Time: r.Time, Level: r.Level.String(), Kind: kindLog, Msg: r.Message}
	adoptScope(&rec, scopeFor(ctx))
	if src := r.Source(); src != nil {
		rec.At = atLocation(src.File, src.Line)
	}
	adoptBound(&rec, &h.bound)
	r.Attrs(func(attr slog.Attr) bool {
		applyAttr(&rec, attr, h.prefix, true)
		return true
	})
	if rec.Kind == "" {
		rec.Kind = kindLog
	}
	h.log.Write(rec)
	return nil
}

// WithAttrs binds attributes for every later record. The copy is what keeps two
// handlers derived from one logger from sharing a map.
func (h *eventHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	next := *h
	next.bound = cloneRecord(h.bound)
	for _, attr := range attrs {
		applyAttr(&next.bound, attr, h.prefix, true)
	}
	return &next
}

// WithGroup prefixes every later attribute key with the group name. Today's
// tuiLogSink.WithGroup drops the group instead (pkg/tui/render.go); a record that
// says which subsystem an attribute came from is the point of having groups.
func (h *eventHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	next := *h
	next.bound = cloneRecord(h.bound)
	next.prefix = h.prefix + name + "."
	return &next
}

const kindLog = "log"

// applyAttr folds one slog attribute into rec. prefix is the group path a nested
// attribute is under. promoteKind is set for the slog adapter, whose "kind"
// attribute names the record type; Emit takes that as an argument instead.
func applyAttr(rec *Record, attr slog.Attr, prefix string, promoteKind bool) {
	attr.Value = attr.Value.Resolve()
	if attr.Value.Kind() == slog.KindGroup {
		// slog.Group("", ...) is an inline group: no prefix, flattened in place.
		groupPrefix := prefix
		if attr.Key != "" {
			groupPrefix = prefix + attr.Key + "."
		}
		for _, inner := range attr.Value.Group() {
			applyAttr(rec, inner, groupPrefix, promoteKind)
		}
		return
	}
	// Only an unprefixed attribute can name an identity field: a key inside a
	// group is a value about that group, not about the record.
	if prefix == "" && promoteAttr(rec, attr, promoteKind) {
		return
	}
	setAttr(rec, prefix+attr.Key, attr.Value)
}

// promoteAttr moves one identity attribute into its field, so it does not also
// land in Attrs. Both spellings of each key are accepted: today's call sites pass
// "session" in 22 places and "session_id"/"run_id" elsewhere.
func promoteAttr(rec *Record, attr slog.Attr, promoteKind bool) bool {
	if attr.Key == attrErr || attr.Key == attrError {
		if err, ok := attr.Value.Any().(error); ok && err != nil {
			rec.Err = err.Error()
			if chain := errorChain(err); len(chain) > 1 {
				setAttr(rec, attrErrChain, slog.AnyValue(chain))
			}
			return true
		}
	}
	value, ok := attrString(attr.Value)
	if !ok {
		return false
	}
	switch attr.Key {
	case attrProject:
		rec.Project = value
	case attrSession, attrSessionID:
		rec.Session = value
	case attrWorker:
		rec.Worker = value
	case attrRun, attrRunID:
		rec.Run = value
	case attrAgent:
		rec.Agent = value
	case attrKind:
		if !promoteKind {
			return false
		}
		rec.Kind = value
	case attrErr, attrError:
		rec.Err = value
	default:
		return false
	}
	return true
}

const (
	attrProject   = "project"
	attrSession   = "session"
	attrSessionID = "session_id"
	attrWorker    = "worker"
	attrRun       = "run"
	attrRunID     = "run_id"
	attrAgent     = "agent"
	attrKind      = "kind"
	attrErr       = "err"
	attrError     = "error"
	attrErrChain  = "err_chain"
)

// errorChain names one type per layer of an error, outermost first, at most eight
// layers deep. Today's formatErrorDetails prints the same message three times
// (%v, %#v and Error()); the type chain says the same thing once and points at
// where the failure came from.
func errorChain(err error) []string {
	var chain []string
	for depth := 0; err != nil && depth < 8; depth++ {
		chain = append(chain, fmt.Sprintf("%T", err))
		err = errors.Unwrap(err)
	}
	return chain
}

// attrString reads a scalar attribute as text. Identity fields are strings in
// practice; a numeric or boolean one is still worth keeping rather than losing.
func attrString(value slog.Value) (string, bool) {
	switch value.Kind() {
	case slog.KindString, slog.KindInt64, slog.KindUint64, slog.KindFloat64, slog.KindBool, slog.KindDuration:
		return value.String(), true
	}
	return "", false
}

// setAttr stores one attribute value as its Go value, so numbers stay numbers in
// JSON rather than being flattened to their text form.
func setAttr(rec *Record, key string, value slog.Value) {
	var stored any
	switch value.Kind() {
	case slog.KindString:
		stored = value.String()
	case slog.KindInt64:
		stored = value.Int64()
	case slog.KindUint64:
		stored = value.Uint64()
	case slog.KindFloat64:
		stored = value.Float64()
	case slog.KindBool:
		stored = value.Bool()
	case slog.KindDuration:
		stored = value.Duration()
	case slog.KindTime:
		stored = value.Time()
	default:
		stored = value.Any()
		// An error has no JSON form — it marshals to {} — so every error that is
		// not the record's own err (a grouped http.err, say) is kept as its
		// message. An attribute that reads {} is a record that says nothing.
		if err, ok := stored.(error); ok && err != nil {
			stored = err.Error()
		}
	}
	if rec.Attrs == nil {
		rec.Attrs = map[string]any{}
	}
	rec.Attrs[key] = stored
}

func adoptScope(rec *Record, scope Scope) {
	rec.Project, rec.Session, rec.Worker, rec.Run, rec.Agent = scope.Project, scope.Session, scope.Worker, scope.Run, scope.Agent
}

// adoptBound merges what a logger bound with WithAttrs. Explicit bindings win
// over the context's scope, and later attributes at the call site win over both.
func adoptBound(rec *Record, bound *Record) {
	if bound.Project != "" {
		rec.Project = bound.Project
	}
	if bound.Session != "" {
		rec.Session = bound.Session
	}
	if bound.Worker != "" {
		rec.Worker = bound.Worker
	}
	if bound.Run != "" {
		rec.Run = bound.Run
	}
	if bound.Agent != "" {
		rec.Agent = bound.Agent
	}
	if bound.Kind != "" {
		rec.Kind = bound.Kind
	}
	if bound.Err != "" {
		rec.Err = bound.Err
	}
	for key, value := range bound.Attrs {
		if rec.Attrs == nil {
			rec.Attrs = map[string]any{}
		}
		rec.Attrs[key] = value
	}
}

func cloneRecord(rec Record) Record {
	rec.Attrs = cloneAttrs(rec.Attrs)
	rec.Blobs = nil
	return rec
}

func cloneAttrs(attrs map[string]any) map[string]any {
	if len(attrs) == 0 {
		return nil
	}
	out := make(map[string]any, len(attrs))
	for key, value := range attrs {
		out[key] = value
	}
	return out
}

// atLocation trims a source location to the repository-relative form a reader can
// search for: pkg/run/runner.go:1356. A path outside this module keeps its base
// name, which is all it can honestly offer.
func atLocation(file string, line int) string {
	path := filepath.ToSlash(file)
	start := -1
	for _, marker := range []string{"/pkg/", "/cmd/"} {
		if index := strings.Index(path, marker); index >= 0 && (start < 0 || index < start) {
			start = index
		}
	}
	if start >= 0 {
		return fmt.Sprintf("%s:%d", path[start+1:], line)
	}
	return fmt.Sprintf("%s:%d", filepath.Base(file), line)
}
