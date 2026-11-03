// The gateway's console log, written to the terminal and to a file at the
// same time.
package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/home"
	"github.com/forebrain-harness/forebrain-harness/pkg/telemetry"
)

// gatewayLogFile is the prefix the gateway's console log is written under: its
// file is gateway-<replica>.log.
const gatewayLogFile = "gateway"

// gatewayLogPath is the log file this replica writes (plan 213 §3.6). Each
// replica gets its own, named for the machine it runs on — a pod's name is its
// hostname — because the file rotates by copy-and-truncate: two replicas
// truncating one file lose whichever records the other copied, and no line of a
// shared file says which replica wrote it.
func gatewayLogPath(root string) string {
	return filepath.Join(root, home.LogsDir, gatewayLogFile+"-"+replicaLogName()+".log")
}

// replicaLogName is this process's place among the replicas: its hostname,
// reduced to the characters a file name keeps on every platform, or "local" when
// the machine cannot say.
func replicaLogName() string {
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

// installGatewayConsoleLog sends every record the gateway logs to stderr and,
// at the same time, to this replica's gateway-<host>.log with the same bounded,
// rotating file debug.log uses. The file mirrors the console: the compact format
// with no SGR bytes on a terminal, logfmt off one.
//
// The returned func restores the previous default logger and closes the file.
// A log file that cannot be opened leaves the terminal output untouched: a
// gateway nobody can read a log from is still a gateway that serves.
func installGatewayConsoleLog(root string) func() {
	file, err := telemetry.Open(gatewayLogPath(root), telemetry.Options{
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
