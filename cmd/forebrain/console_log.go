package main

import (
	"io"
	"log/slog"
	"os"

	"github.com/forebrain-harness/forebrain-harness/pkg/telemetry"
)

// stderrColor reports whether the terminal this process logs to should get
// SGR sequences: only a real terminal, and never when NO_COLOR asks for none.
func stderrColor() bool {
	return rootIsTerminal(os.Stderr) && os.Getenv("NO_COLOR") == ""
}

// stderrLogHandler is the inner handler the process logs to stderr through
// before the telemetry tee wraps it.
func stderrLogHandler(level slog.Level, addSource bool) slog.Handler {
	return stderrLogHandlerTo(nil, level, addSource)
}

// stderrLogHandlerTo builds that handler and, when file is non-nil, mirrors
// every record into it: the compact console format on a terminal — colored
// unless NO_COLOR — with the file receiving the same text without a single
// SGR byte. Everywhere else (systemd, docker, a redirected descriptor) the
// terminal and the file both get logfmt, so machine consumers see one stable
// format.
func stderrLogHandlerTo(file io.Writer, level slog.Level, addSource bool) slog.Handler {
	// The durable destination is written first: if the process dies between the
	// two writes, the record that is supposed to outlive the session is the one
	// already on disk.
	if !rootIsTerminal(os.Stderr) {
		w := io.Writer(os.Stderr)
		if file != nil {
			w = telemetry.MultiWriter(file, os.Stderr)
		}
		return slog.NewTextHandler(w, &slog.HandlerOptions{Level: level, AddSource: addSource})
	}
	sinks := []telemetry.ConsoleSink{{W: os.Stderr, Color: stderrColor()}}
	if file != nil {
		sinks = []telemetry.ConsoleSink{{W: file}, {W: os.Stderr, Color: stderrColor()}}
	}
	return telemetry.NewConsoleHandler(level, addSource, sinks...)
}
