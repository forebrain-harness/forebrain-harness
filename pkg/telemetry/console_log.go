// The compact console log format for a terminal: what an operator watching
// `forebrain gateway start` reads, in the shape zerolog and zap console
// handlers made familiar across the Go ecosystem.
package telemetry

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ConsoleSink is one destination of a console log line. Color decides whether
// that destination receives SGR sequences: a terminal gets them, a log file
// never does.
type ConsoleSink struct {
	W     io.Writer
	Color bool
}

// fanoutWriter adapts a write function to io.Writer.
type fanoutWriter func([]byte) (int, error)

func (f fanoutWriter) Write(p []byte) (int, error) { return f(p) }

// MultiWriter duplicates each write to every writer, and does so even when one
// of them fails: the stdlib MultiWriter stops at the first error, which would
// let a broken terminal silence the log file on a headless or piped run. The
// errors are joined so a caller that inspects them still sees every failure.
func MultiWriter(writers ...io.Writer) io.Writer {
	return fanoutWriter(func(p []byte) (int, error) {
		n := len(p)
		var errs []error
		for _, w := range writers {
			if _, err := w.Write(p); err != nil {
				errs = append(errs, err)
			}
		}
		return n, errors.Join(errs...)
	})
}

// NewConsoleHandler returns a handler printing one line per record:
//
//	16:40:05.131 INF request method=GET path=/ status=200 … · serve_run.go:203
//
// Every record goes to each sink in turn, so one handler can serve the
// terminal and the log file at once. addSource keeps the process's AddSource
// semantics, with the file shortened to its basename at the end of the line.
func NewConsoleHandler(level slog.Level, addSource bool, sinks ...ConsoleSink) slog.Handler {
	return &consoleLogHandler{
		sinks:     sinks,
		level:     level,
		addSource: addSource,
		mu:        &sync.Mutex{},
	}
}

type consoleLogHandler struct {
	sinks     []ConsoleSink
	level     slog.Level
	addSource bool
	// mu serializes writes across every clone; the line is built under it so
	// records never interleave within or across sinks.
	mu *sync.Mutex
	// attrs are the values pre-bound with WithAttrs, already prefixed with
	// whatever group was open when they were bound.
	attrs []consoleAttr
	// group is the dotted prefix record attributes print under after
	// WithGroup.
	group string
}

// consoleAttr is one attribute of the record's fixed context, bound before a
// group could still change under it.
type consoleAttr struct {
	key string
	val slog.Value
}

func (h *consoleLogHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

func (h *consoleLogHandler) Handle(_ context.Context, r slog.Record) error {
	if len(h.sinks) == 0 {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	// Every destination is attempted, always. A dead terminal (EPIPE from a
	// closed `tee`, a vanished tmux pane) must not cost the operator the record
	// that survives the session, and slog discards this error entirely, so
	// returning early would lose it silently.
	var errs []error
	for _, s := range h.sinks {
		var b strings.Builder
		h.appendLine(&b, r, s.Color)
		if _, err := io.WriteString(s.W, b.String()); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (h *consoleLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	bound := make([]consoleAttr, 0, len(h.attrs)+len(attrs))
	bound = append(bound, h.attrs...)
	c := h.clone()
	c.attrs = appendConsoleAttrs(bound, attrs, h.group)
	return c
}

func (h *consoleLogHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	c := h.clone()
	if c.group == "" {
		c.group = name
	} else {
		c.group += "." + name
	}
	return c
}

func (h *consoleLogHandler) clone() *consoleLogHandler {
	return &consoleLogHandler{
		sinks:     h.sinks,
		level:     h.level,
		addSource: h.addSource,
		mu:        h.mu,
		attrs:     h.attrs,
		group:     h.group,
	}
}

// messageText renders the record's message as a single physical line. The
// message is not a key=value value, so spaces stay as they are — prose reads
// badly quoted. Control characters do get escaped: a newline in a message
// (multi-line errors, embedded bodies) would otherwise turn one record into
// several lines and break both the file's one-record-per-line shape and any
// grep-by-record.
func messageText(msg string) string {
	if !strings.ContainsFunc(msg, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return msg
	}
	return strconv.Quote(msg)
}

func (h *consoleLogHandler) appendLine(b *strings.Builder, r slog.Record, color bool) {
	if color {
		b.WriteString("\x1b[90m")
	}
	b.WriteString(r.Time.Format("15:04:05.000"))
	if color {
		b.WriteString("\x1b[0m")
	}
	b.WriteByte(' ')
	tag, code := levelTag(r.Level)
	if color {
		b.WriteString(code)
	}
	b.WriteString(tag)
	if color {
		b.WriteString("\x1b[0m")
	}
	b.WriteByte(' ')
	if color && r.Level >= slog.LevelError {
		b.WriteString("\x1b[31m")
		b.WriteString(messageText(r.Message))
		b.WriteString("\x1b[0m")
	} else {
		b.WriteString(messageText(r.Message))
	}
	for _, a := range h.attrs {
		b.WriteByte(' ')
		writeAttr(b, a.key, a.val.Resolve(), color)
	}
	r.Attrs(func(a slog.Attr) bool {
		appendAttrLine(b, a, h.group, color)
		return true
	})
	if h.addSource {
		if src := r.Source(); src != nil {
			b.WriteByte(' ')
			if color {
				b.WriteString("\x1b[90m")
			}
			b.WriteString("· ")
			b.WriteString(filepath.Base(src.File))
			b.WriteByte(':')
			b.WriteString(strconv.Itoa(src.Line))
			if color {
				b.WriteString("\x1b[0m")
			}
		}
	}
	b.WriteByte('\n')
}

// appendConsoleAttrs flattens attrs bound with WithAttrs into key/value pairs,
// prefixing each with the group that was open at bind time.
func appendConsoleAttrs(dst []consoleAttr, attrs []slog.Attr, group string) []consoleAttr {
	for _, a := range attrs {
		if a.Equal(slog.Attr{}) {
			continue
		}
		if a.Value.Kind() == slog.KindGroup {
			inner := a.Value.Group()
			if len(inner) == 0 {
				continue
			}
			g := a.Key
			if group != "" {
				g = group + "." + a.Key
			}
			dst = appendConsoleAttrs(dst, inner, g)
			continue
		}
		key := a.Key
		if group != "" {
			key = group + "." + key
		}
		dst = append(dst, consoleAttr{key: key, val: a.Value})
	}
	return dst
}

func appendAttrLine(b *strings.Builder, a slog.Attr, group string, color bool) {
	if a.Equal(slog.Attr{}) {
		return
	}
	// Resolve before inspecting the kind: a LogValuer may produce a group, and
	// the stdlib resolves at render time for exactly this reason.
	a.Value = a.Value.Resolve()
	if a.Value.Kind() == slog.KindGroup {
		g := a.Key
		if group != "" {
			g = group + "." + a.Key
		}
		for _, ia := range a.Value.Group() {
			appendAttrLine(b, ia, g, color)
		}
		return
	}
	key := a.Key
	if group != "" {
		key = group + "." + key
	}
	b.WriteByte(' ')
	writeAttr(b, key, a.Value, color)
}

func writeAttr(b *strings.Builder, key string, v slog.Value, color bool) {
	if color {
		b.WriteString("\x1b[90m")
	}
	b.WriteString(key)
	b.WriteString("=")
	if color {
		b.WriteString("\x1b[0m")
	}
	b.WriteString(attrValue(key, v, color))
}

// attrValue renders a value with logfmt quoting and, keyed by attribute
// name, the console color policy: err/error red, status by response class,
// duration by slowness, bytes humanized. It is a policy over names, not an
// access-log branch, so every log source gets it.
func attrValue(key string, v slog.Value, color bool) string {
	s := v.Resolve().String()
	code := ""
	switch key {
	case "err", "error":
		code = "\x1b[31m"
	case "status":
		code = statusColor(s)
	case "duration":
		if d, err := time.ParseDuration(s); err == nil {
			code = durationColor(d)
		}
	case "bytes":
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			s = humanBytes(n)
		}
	}
	if s == "" || strings.ContainsAny(s, " =\"\t\r\n") {
		s = strconv.Quote(s)
	}
	if code != "" && color {
		return code + s + "\x1b[0m"
	}
	return s
}

func statusColor(s string) string {
	code, err := strconv.Atoi(s)
	if err != nil {
		return ""
	}
	switch {
	case code >= 200 && code < 300:
		return "\x1b[32m"
	case code >= 100 && code < 200, code >= 300 && code < 400:
		return "\x1b[36m"
	case code >= 400 && code < 500:
		return "\x1b[33m"
	case code >= 500:
		return "\x1b[1;31m"
	}
	return ""
}

func durationColor(d time.Duration) string {
	switch {
	case d >= time.Second:
		return "\x1b[31m"
	case d >= 100*time.Millisecond:
		return "\x1b[33m"
	}
	return ""
}

// humanBytes renders byte counts the way an operator reads them: 156B,
// 8.4KB, 1.2MB.
func humanBytes(n int64) string {
	if n < 1024 {
		return strconv.FormatInt(n, 10) + "B"
	}
	f := float64(n)
	for _, unit := range []string{"KB", "MB", "GB", "TB", "PB"} {
		f /= 1024
		if f < 1024 {
			return strconv.FormatFloat(f, 'f', 1, 64) + unit
		}
	}
	return strconv.FormatFloat(f, 'f', 1, 64) + "EB"
}

func levelTag(l slog.Level) (tag, code string) {
	switch {
	case l < slog.LevelInfo:
		return "DBG", "\x1b[90m"
	case l < slog.LevelWarn:
		return "INF", "\x1b[36m"
	case l < slog.LevelError:
		return "WRN", "\x1b[33m"
	default:
		return "ERR", "\x1b[1;31m"
	}
}
