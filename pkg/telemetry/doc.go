// Package telemetry provides logs, traces, metrics, and usage records,
// including the compact console log format and the bounded, rotating log files
// the process writes them into.
//
// # Event log
//
// The event log is the only log format the process writes to disk: one JSON
// record per line, one file per process, one directory per local day
// (eventlog.go, plans/284).
//
//	<home>/logs/
//	  2026-11-11/
//	    tui-mbp-dev-89572-a3f09c.jsonl            <source>-<host>-<pid>-<6 hex>.jsonl
//	    tui-mbp-dev-89572-a3f09c.1.jsonl          next part, past eventFileMaxBytes
//	    gateway-gw-0-4117-51be02.jsonl
//	    x/                                        payloads a record points at (Blobs)
//	      a3f09c-000391.req.gz
//
// Three invariants hold the design up, and each one removes a failure the old
// text logs had (records of four processes interleaved in one debug.log, a
// copy-and-truncate rotation that moved two backups at once, timestamps with no
// date):
//
//   - One writer per file. The name carries source, host, process id and a
//     random suffix drawn at startup, so two processes never open one path.
//   - One record, one write(2). No buffering, and no fsync — see File.Write.
//   - Append only, never renamed. A part that fills up is continued in the next
//     numbered part; nothing is renamed or truncated under a reader.
//
// Retention is fixed, not configured (owner ruling 2026-11-11): LogRetentionDays
// days and LogBudgetBytes in total, payload files deleted before records and the
// oldest day before the newest. SweepLogs applies it — synchronously when
// OpenEventLog opens, then hourly — and it is safe to run from several processes
// at once because every step is an idempotent delete.
package telemetry
