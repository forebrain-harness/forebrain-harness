package telemetry

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
)

// Log records a recovered panic: one ERROR record of kind panic, carrying the
// recovered value and the stack it was recovered from.
//
// The stack keeps its own length allowance (plans/284's stackAttrMaxBytes): it
// is the part of this record a reader needs whole.
//
// forebrainRoot is kept for the callers and no longer used: the record goes to
// the process's event log (plan 285).
func Log(forebrainRoot string, label string, recovered any) {
	if recovered == nil {
		return
	}
	_ = forebrainRoot
	_, file, line, ok := runtimeCaller(2)
	if !ok {
		file, line = "?", 0
	}
	emitAt(context.Background(), Record{
		Level: slog.LevelError.String(),
		Kind:  "panic",
		Msg:   "recovered: " + strings.TrimSpace(label),
	}, atLocation(file, line),
		slog.String(attrErr, fmt.Sprint(recovered)),
		slog.String(attrStack, string(debug.Stack())))
}
