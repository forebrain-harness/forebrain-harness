// The gateway's console log: the terminal an operator watches, and the event
// log beside it.
package main

import (
	"log/slog"

	"github.com/forebrain-harness/forebrain-harness/pkg/telemetry"
)

// installGatewayConsoleLog points the process at the gateway's own event log
// for as long as the gateway serves. Each replica writes its own file — the
// name carries the source, the machine (a pod's name is its hostname), the
// process id and a per-process suffix — so two replicas never open the same
// path, and every record's file says which replica wrote it (plan 284).
//
// The terminal keeps the console format it always had: the event log is a
// second destination, never a replacement for what an operator reads live.
//
// The returned func puts the previous logger and event log back and closes the
// gateway's. Nothing here can fail the start: OpenEventLog creates its file on
// the first record, so a home whose logs directory cannot be written costs
// records, not the gateway.
func installGatewayConsoleLog(root string) func() {
	prev := slog.Default()
	prevLog := telemetry.DefaultEventLog()
	// The command's own log is this home's too; closing it here is what keeps
	// one sweeper per process instead of two over the same tree.
	_ = prevLog.Close()
	gatewayLog := telemetry.OpenEventLog(root, "gateway")
	telemetry.SetDefaultEventLog(gatewayLog)
	slog.SetDefault(slog.New(telemetry.NewFanoutHandler(
		stderrLogHandler(defaultSlogLevel(), true),
		telemetry.NewEventHandler(gatewayLog, defaultSlogLevel()),
	)))
	return func() {
		slog.SetDefault(prev)
		telemetry.SetDefaultEventLog(prevLog)
		_ = gatewayLog.Close()
	}
}
