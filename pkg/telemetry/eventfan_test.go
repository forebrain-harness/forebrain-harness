package telemetry

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// fanoutRecorder keeps the lines a fanout delivered, so a test can see which
// destinations received the record and with what context.
type fanoutRecorder struct {
	mu    sync.Mutex
	msgs  []string
	err   error
	level slog.Level
}

func (h *fanoutRecorder) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

func (h *fanoutRecorder) Handle(_ context.Context, r slog.Record) error {
	line := r.Message
	r.Attrs(func(attr slog.Attr) bool {
		line += " " + attr.Key + "=" + attr.Value.String()
		return true
	})
	h.mu.Lock()
	defer h.mu.Unlock()
	h.msgs = append(h.msgs, line)
	return h.err
}

// WithAttrs and WithGroup are marked as a suffix, which is all these tests
// need to know: the derivation reached this destination.
func (h *fanoutRecorder) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &fanoutDerived{fanoutRecorder: h, bound: attrs}
}

func (h *fanoutRecorder) WithGroup(name string) slog.Handler {
	return &fanoutDerived{fanoutRecorder: h, group: name}
}

func (h *fanoutRecorder) got() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string{}, h.msgs...)
}

// fanoutDerived is fanoutRecorder after WithAttrs or WithGroup: it prefixes
// the group and appends one marker per bound attribute.
type fanoutDerived struct {
	*fanoutRecorder
	bound []slog.Attr
	group string
}

func (h *fanoutDerived) Handle(ctx context.Context, r slog.Record) error {
	msg := r.Message
	if h.group != "" {
		msg = h.group + "." + msg
	}
	for range h.bound {
		msg += " bound"
	}
	return h.fanoutRecorder.Handle(ctx, slog.NewRecord(r.Time, r.Level, msg, r.PC))
}

func (h *fanoutDerived) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &fanoutDerived{fanoutRecorder: h.fanoutRecorder, bound: append(h.bound, attrs...), group: h.group}
}

func (h *fanoutDerived) WithGroup(name string) slog.Handler {
	return &fanoutDerived{fanoutRecorder: h.fanoutRecorder, bound: h.bound, group: h.group + name}
}

// A handler that fails must not stop the ones beside it: the terminal and the
// event log are independent destinations, and one broken sink cannot cost the
// other its record.
func TestFanoutKeepsGoingAfterAFailure(t *testing.T) {
	broken := &fanoutRecorder{err: errors.New("sink is broken")}
	second := &fanoutRecorder{}
	third := &fanoutRecorder{}

	err := NewFanoutHandler(broken, second, third).Handle(context.Background(),
		slog.NewRecord(time.Now(), slog.LevelInfo, "keep going", 0))
	if err == nil || !strings.Contains(err.Error(), "sink is broken") {
		t.Fatalf("the failed sink's error must ride out, got %v", err)
	}
	if got := second.got(); len(got) != 1 {
		t.Fatalf("handler after the broken one = %v, want the record", got)
	}
	if got := third.got(); len(got) != 1 {
		t.Fatalf("last handler = %v, want the record", got)
	}
}

// Enabled is true when any destination wants the record, so a level only one
// of them keeps still reaches that one.
func TestFanoutEnabledIfAnyHandlerWantsIt(t *testing.T) {
	verbose := &fanoutRecorder{level: slog.LevelDebug}
	quiet := &fanoutRecorder{level: slog.LevelError}
	h := NewFanoutHandler(quiet, verbose)
	if !h.Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("a debug record one destination keeps must be enabled")
	}
	if NewFanoutHandler(quiet).Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("no destination keeps debug, so the fanout must not")
	}
	if err := h.Handle(context.Background(), slog.NewRecord(time.Now(), slog.LevelDebug, "dbg", 0)); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if got := verbose.got(); len(got) != 1 {
		t.Fatalf("verbose handler = %v, want the debug record", got)
	}
	if got := quiet.got(); len(got) != 0 {
		t.Fatalf("quiet handler = %v, want nothing below its level", got)
	}
}

// WithAttrs and WithGroup apply to every destination: a logger bound once is
// bound for both, which is what keeps the terminal line and the event record
// carrying the same context.
func TestFanoutWithAttrsAndGroupReachEveryHandler(t *testing.T) {
	first := &fanoutRecorder{}
	second := &fanoutRecorder{}
	h := NewFanoutHandler(first, second).WithGroup("g").WithAttrs([]slog.Attr{slog.String("k", "v")})
	if err := h.Handle(context.Background(),
		slog.NewRecord(time.Now(), slog.LevelInfo, "msg", 0)); err != nil {
		t.Fatalf("handle: %v", err)
	}
	for name, got := range map[string][]string{"first": first.got(), "second": second.got()} {
		if len(got) != 1 {
			t.Fatalf("%s = %v, want one record", name, got)
		}
		if got[0] != "g.msg bound" {
			t.Fatalf("%s record = %q, want the group prefix and the bound attribute", name, got[0])
		}
	}
}

// A nil handler in the list is skipped rather than panicking on every record.
func TestFanoutSkipsNilHandlers(t *testing.T) {
	kept := &fanoutRecorder{}
	h := NewFanoutHandler(nil, kept)
	if err := h.Handle(context.Background(),
		slog.NewRecord(time.Now(), slog.LevelInfo, "msg", 0)); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if got := kept.got(); len(got) != 1 {
		t.Fatalf("kept handler = %v, want the record", got)
	}
}
