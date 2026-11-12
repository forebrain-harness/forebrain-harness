package main

import (
	"log/slog"
	"os"

	"github.com/forebrain-harness/forebrain-harness/pkg/telemetry"
)

// stderrColor reports whether the terminal this process logs to should get
// SGR sequences: only a real terminal, and never when NO_COLOR asks for none.
func stderrColor() bool {
	return rootIsTerminal(os.Stderr) && os.Getenv("NO_COLOR") == ""
}

// stderrLogHandler is the handler the process logs to stderr through: the
// compact console format on a terminal — colored unless NO_COLOR — and logfmt
// everywhere else (systemd, docker, a redirected descriptor), so machine
// consumers see one stable format.
//
// It is also what the slog fanout writes to the terminal with; the log file
// beside it is the event log's, which is written once and in one format
// (plan 285), so this handler no longer mirrors anything to a file.
func stderrLogHandler(level slog.Level, addSource bool) slog.Handler {
	if !rootIsTerminal(os.Stderr) {
		return slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level, AddSource: addSource})
	}
	return telemetry.NewConsoleHandler(level, addSource, telemetry.ConsoleSink{W: os.Stderr, Color: stderrColor()})
}
