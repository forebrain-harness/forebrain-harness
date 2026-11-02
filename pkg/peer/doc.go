// Package peer implements forebrain's local peer-to-peer messaging protocol:
// the rendezvous directory, key files, Unix domain sockets, frames, envelopes,
// hop chains, admission limits, and the sending and receiving ends. Two
// forebrain processes on one machine — and, once the user turns Claude Code
// interop on, a Claude Code session — find each other through this package and
// exchange messages directly, with no broker, daemon or helper process in
// between.
//
// The wire protocol is Claude Code 2.1.301's local peer messaging
// (peerProtocol 1); the specification is in
// docs/plan/CROSS_PROCESS_PEER_COORDINATION_DESIGN.md §3.
//
// A process uses exactly one registration mode at a time: FB mode
// (forebrain's own rendezvous directory and fb-socks, the default) or CC mode
// (Claude Code's directory and cc-socks, which requires the user's interop
// switch and an existing Claude Code directory). The two modes are separate
// address spaces — a process in one mode never sees the other — so every path
// this package touches comes from one Layout.
//
// The package is self-contained: it imports no other pkg/... package, and it
// never runs an external command (process start times are read from the
// kernel in-process). It never creates Claude Code's rendezvous directory: a
// record is written there only when Claude Code itself already made it.
//
// Files: paths.go (directories, the two layouts, socket paths, socket-directory
// vetting), record.go (the <pid>.json registration record and reading a
// directory of them), key.go (peer tokens and their key files), liveness.go
// (process start times, pid domains, the online test), address.go (uds:
// addressing and reply-target vetting), envelope.go (the
// <cross-session-message> envelope, its escaping and its parser), frame.go (the
// NDJSON frames in both directions), hop.go (the anti-loop hop chain), guard.go
// (admission limits, the per-sender token bucket and the outbound pacer),
// names.go (agent names and local refs), server.go (listening, peer
// credentials, framing and the serial queue), client.go (dialling,
// authentication and error classification), sys_darwin.go, sys_linux.go and
// sys_other.go (peer credentials and process start times per platform).
package peer
