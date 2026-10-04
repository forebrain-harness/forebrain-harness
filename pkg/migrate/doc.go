// Package migrate imports another agent's on-disk history into forebrain's
// stores.
//
// The migration is one-way and idempotent: it reads the source application's
// files, writes sessions, memories, skills, MCP entries and input history
// into this install's state database and configuration, and can be re-run
// without duplicating anything. Every imported session carries
// fb_sessions.origin='migrated' so the results stay identifiable and
// reversible, and each session is written in a single transaction so a
// failure loses that session's work only.
//
// Both sources — Claude Code (~/.claude) and Codex (~/.codex) — share one
// contract. The resume invariants (§1.2 of the Codex plan) apply to both:
// every tool call is answered (an interrupted call gets a cancelled result;
// the call row itself is never deleted), no row carries a role the
// model-context rebuild would drop mid-context, each segment opens on a user
// message, and a compacted session's projection stays self-consistent.
// Project-level configuration is always copied into forebrain's own project
// files (<root>/.forebrain/mcp_servers.yaml, <root>/.forebrain/safety.json);
// forebrain never reads another agent's project files (§4.9, 2026-09-17).
//
// Layering: this package is Layer 2 and may not reach pkg/run or
// pkg/process. Memory consolidation after an import is injected as a
// callback by the composition root (see Options.Consolidate), which keeps
// the layer ceiling intact while letting the report state what actually
// happened rather than what was triggered.
//
// Files here:
//
//   - source.go        — source registry, install detection, source-directory
//     validation, the picker's view, the shared Options.
//   - claude.go        — Claude Code discovery: sessions, subagents, memories,
//     MCP entries, plugin skills, input history.
//   - codex.go         — Codex discovery: rollout files, SQLite snapshots
//     (threads, spawn edges, memories), config.toml, input history.
//   - parse.go         — Claude-side streaming jsonl → mapped rows, the
//     compact-boundary mapping, and the shared resume-invariant pass.
//   - codex_parse.go   — Codex rollout jsonl → mapped rows (response_item
//     spine, compacted boundaries with source window chains).
//   - toolmap.go       — source tool names → forebrain tool names, and the durable
//     tool metadata written beside each imported row.
//   - sessions.go      — the source-agnostic unit importer: transactions,
//     window chains, aggregates, idempotency, cross-agent guards.
//   - assets.go        — memories (four-level project path resolution), skills,
//     user-level and project-level MCP writes (both sources), permission
//     rules → safety.json, plan files, input history merge.
//   - lsp.go           — imports the language servers of enabled Claude Code
//     plugins into forebrain.yaml: official plugins enable their built-in
//     catalog server, everything else becomes a custom lsp.servers entry.
//   - codex_assets.go  — Codex memories, global instructions, user MCP,
//     plugin skills, project configuration, and plan-mode extraction.
//   - plan.go          — dry-run plan, the run orchestration for both
//     sources, and the report.
package migrate
