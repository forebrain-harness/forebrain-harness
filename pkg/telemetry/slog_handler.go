package telemetry

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/home"
)

type teeHandler struct {
	root  string
	inner slog.Handler
}

func NewSlogTeeHandler(forebrainRoot string, inner slog.Handler) slog.Handler {
	r := strings.TrimSpace(forebrainRoot)
	if r == "" {
		if x, err := home.Root(); err == nil {
			r = x
		}
	}
	return &teeHandler{root: r, inner: inner}
}

func (h *teeHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *teeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &teeHandler{root: h.root, inner: h.inner.WithAttrs(attrs)}
}

func (h *teeHandler) WithGroup(name string) slog.Handler {
	return &teeHandler{root: h.root, inner: h.inner.WithGroup(name)}
}

func (h *teeHandler) Handle(ctx context.Context, r slog.Record) error {
	innerErr := h.inner.Handle(ctx, r)
	if h.root == "" {
		return innerErr
	}
	if r.Level < slog.LevelError {
		return innerErr
	}
	writeMu.Lock()
	defer writeMu.Unlock()
	path := filepath.Join(h.root, home.LogsDir, "error.log")
	if e := os.MkdirAll(filepath.Dir(path), 0o755); e != nil {
		return innerErr
	}
	f, e := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if e != nil {
		return innerErr
	}
	defer f.Close()
	var b strings.Builder
	fmt.Fprintf(&b, "=== [SLOG %s] %s %s\n", r.Level.String(), time.Now().Format(time.RFC3339Nano), r.Message)
	if src := r.Source(); src != nil {
		fmt.Fprintf(&b, "location: %s:%d\n", src.File, src.Line)
		if src.Function != "" {
			fmt.Fprintf(&b, "function: %s\n", src.Function)
		}
	}
	r.Attrs(func(a slog.Attr) bool {
		if a.Value.Kind() == slog.KindAny {
			if e2, ok := a.Value.Any().(error); ok && e2 != nil {
				fmt.Fprintf(&b, "attr %s:\n%s", a.Key, formatErrorDetails(e2))
				return true
			}
		}
		fmt.Fprintf(&b, "attr %s\n", a.String())
		return true
	})
	b.WriteString("---\n\n")
	_, _ = f.WriteString(b.String())
	return innerErr
}
