// The gateway's console log, written to the terminal and to a file at the
// same time.
package main

import (
	"io"
	"log/slog"
	"path/filepath"

	"github.com/forebrain-harness/forebrain-harness/pkg/home"
	"github.com/forebrain-harness/forebrain-harness/pkg/telemetry"
)

// gatewayLogFile is the file the gateway's console log is also written to.
const gatewayLogFile = "gateway.log"

// installGatewayConsoleLog sends every record the gateway logs to stderr and,
// at the same time, to <home>/logs/gateway.log with the same bounded, rotating
// file debug.log uses. The file mirrors the console: the compact format with
// no SGR bytes on a terminal, logfmt off one.
//
// The returned func restores the previous default logger and closes the file.
// A log file that cannot be opened leaves the terminal output untouched: a
// gateway nobody can read a log from is still a gateway that serves.
func installGatewayConsoleLog(root string) func() {
	file, err := telemetry.Open(filepath.Join(root, home.LogsDir, gatewayLogFile), telemetry.Options{
		Perm:               0o600,
		ExistingParentOnly: true,
	})
	if err != nil {
		slog.Warn("gateway log file unavailable; logging to the terminal only", "err", err)
		file = nil
	}
	// A nil *telemetry.File in a non-nil io.Writer would read as a working
	// sink, so the mirror is only passed along when there really is one.
	var sink io.Writer
	if file != nil {
		sink = file
	}
	prev := slog.Default()
	slog.SetDefault(slog.New(telemetry.NewSlogTeeHandler(root, stderrLogHandlerTo(sink, defaultSlogLevel(), true))))
	return func() {
		slog.SetDefault(prev)
		if file != nil {
			_ = file.Close()
		}
	}
}
