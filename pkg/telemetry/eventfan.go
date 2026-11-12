// One slog handler that fans a record out to several destinations: the
// terminal an operator watches and the event log a reader queries.
package telemetry

import (
	"context"
	"errors"
	"log/slog"
)

// fanoutHandler sends every enabled record to each of its handlers in turn.
//
// A handler that fails does not stop the ones after it, for the reason
// MultiWriter exists: the fragile destination is the terminal, and a broken
// terminal must not silence the log file (or the reverse). The errors are
// joined, so a caller that inspects them still sees every failure.
type fanoutHandler struct {
	handlers []slog.Handler
}

// NewFanoutHandler returns a handler that writes each record to every handler
// it was given. A nil handler in the list is skipped.
func NewFanoutHandler(handlers ...slog.Handler) slog.Handler {
	kept := make([]slog.Handler, 0, len(handlers))
	for _, h := range handlers {
		if h != nil {
			kept = append(kept, h)
		}
	}
	return &fanoutHandler{handlers: kept}
}

func (h *fanoutHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, inner := range h.handlers {
		if inner.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (h *fanoutHandler) Handle(ctx context.Context, r slog.Record) error {
	var errs []error
	for _, inner := range h.handlers {
		// Enabled is checked per handler: the terminal keeps the process's
		// level while the event log may keep everything, and a record only
		// one of them wants must still reach that one.
		if !inner.Enabled(ctx, r.Level) {
			continue
		}
		// The record is cloned for every handler but the first: a handler
		// that adds attributes (a bound logger, a shim) would otherwise
		// change what the handlers after it see.
		rec := r.Clone()
		if err := inner.Handle(ctx, rec); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (h *fanoutHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	next := &fanoutHandler{handlers: make([]slog.Handler, 0, len(h.handlers))}
	for _, inner := range h.handlers {
		next.handlers = append(next.handlers, inner.WithAttrs(attrs))
	}
	return next
}

func (h *fanoutHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	next := &fanoutHandler{handlers: make([]slog.Handler, 0, len(h.handlers))}
	for _, inner := range h.handlers {
		next.handlers = append(next.handlers, inner.WithGroup(name))
	}
	return next
}
