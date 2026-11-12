package telemetry

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"runtime"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/home"
)

var (
	runtimeCaller = runtime.Caller
	datadirRoot   = home.Root
)

// ErrorSkip records one error where it happened. extraFrames walks past the
// wrappers between the failing caller and this function, so At names the code
// that failed rather than the logging helper itself.
//
// label was named context before this package wrote records; the name moved
// because the package that means "context" is now in scope here.
func ErrorSkip(forebrainRoot string, err error, label string, extraFrames int) {
	if err == nil {
		return
	}
	if extraFrames < 0 {
		extraFrames = 0
	}
	skip := 2 + extraFrames
	_, file, line, ok := runtimeCaller(skip)
	if !ok {
		file, line = "?", 0
	}
	msg := strings.TrimSpace(label)
	if msg == "" {
		msg = "error"
	}
	emitAt(context.Background(), Record{Level: slog.LevelError.String(), Kind: kindLog, Msg: msg},
		atLocation(file, line), slog.Any(attrErr, err))
}

// ErrorThenFprintln records the error and prints it: a command that is about
// to exit owed the reader both.
func ErrorThenFprintln(forebrainRoot string, w io.Writer, err error, label string) {
	if err == nil {
		return
	}
	ErrorSkip(forebrainRoot, err, label, 1)
	_, _ = fmt.Fprintln(w, err)
}

func resolveRoot(forebrainRoot string) string {
	root := strings.TrimSpace(forebrainRoot)
	if root != "" {
		return root
	}
	x, e := datadirRoot()
	if e != nil {
		return ""
	}
	return x
}

// emitAt writes one record for ctx's identity at the call site a reader needs.
//
// Emit cannot serve the two writers below it: it takes At from its own caller,
// which for a wrapper inside this package is this package, not the failing
// line. The identity half is the same — the scope off ctx, overridden by any
// explicit attribute — and so is the attribute folding, which is what turns an
// err attribute into the record's Err and its type chain.
func emitAt(ctx context.Context, rec Record, at string, attrs ...slog.Attr) {
	l := DefaultEventLog()
	if l == nil {
		return // no log configured: nothing to write, nothing to compute
	}
	rec.Time = time.Now()
	rec.At = at
	adoptScope(&rec, scopeFor(ctx))
	for _, attr := range attrs {
		applyAttr(&rec, attr, "", false)
	}
	l.Write(rec)
}
