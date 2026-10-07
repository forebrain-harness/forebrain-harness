// Package lsp runs language servers for code intelligence: it owns their
// processes, speaks the Language Server Protocol to them, and implements the
// tool.CodeIntelligence and tool.CodeIntelControl ports that pkg/process
// injects into each runner. See docs/plan/LSP_CODE_INTELLIGENCE_PLAN.md.
//
// Files: pool.go (process-wide instance pool), manager.go (one runner's view,
// the CodeIntelligence port), control.go (the CodeIntelControl port),
// catalog.go and catalog/*.yaml (built-in servers), detect.go (finding,
// versioning and installing server binaries; forebrain lsp doctor), resolve.go
// (merging catalog and configuration,
// file matching, workspace roots, the lsp tool decision, per-agent state
// paths), jsonrpc.go (framing and the JSON-RPC connection), protocol.go
// (the LSP 3.17 types this client uses), position.go (line/column and URI
// mapping), instance.go (one server process: environment, initialize,
// server requests, readiness, restarts), docsync.go (keeping each server's
// open documents in step with the disk), diagnostics.go (the diagnostics
// store, new-problem computation and the model-facing diagnostic text),
// query.go (the lsp tool's lookups and their model-facing text),
// procgroup_*.go (process-tree control and orphan cleanup), project.go
// (project-level servers and their per-entry consent).
//
// The pool's instances are started lazily, shared across runners of one
// project, capped by lsp.max_servers and stopped when idle; DidWrite
// implements the diagnostics wait window.
package lsp
