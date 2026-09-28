# FOREBRAIN.md

Local AI coding agent. Single Go module (`github.com/forebrain-harness/forebrain-harness`), Go 1.26, CGO mandatory.

## Commands

```bash
make build        # build binary with embedded UI -> build/bin/forebrain (runs make ui first)
make test         # CGO_ENABLED=1 go test ./...
make ui           # build Vue frontend into the Go embed package (pkg/gateway/dist)
make docker       # container image
make release      # per-platform npm packages (npm/scripts/build-platform-packages.sh)
make clean

# Day-to-day (fastest, no frontend rebuild):
go build -o ./build/bin/forebrain ./cmd/forebrain

# Lint / vet (no golangci-lint; this is what CI runs):
go vet ./...

# Tests: CGO is required (silk voice decoder, sqlite) and CI uses -tags fts5:
CGO_ENABLED=1 go test -tags fts5 ./... -count=1
CGO_ENABLED=1 go test -tags fts5 ./pkg/turn -run TestName   # single package/test

# Frontend (Node 22, pnpm 10):
cd frontend && pnpm install && pnpm build
```

Packaging flows (only when touching the npm layer): `npm/scripts/build-platform-packages.sh` builds the host triple by default; override with `FOREBRAIN_BUILD_TARGETS="darwin/arm64 linux/amd64"`. `cd npm && npm link` to test the launcher flow.

## Architecture

Two runtime surfaces share one core:

- `cmd/forebrain` — thin Cobra entrypoint (`gateway`, `interactive`, `resume`, `serve`); keep CLI wiring here, push behavior down into `pkg/...`.
- `pkg/gateway` — HTTP/WS server and REST API; embeds the Vue build output (`pkg/gateway/dist`) via go:embed. Also binds channel integrations for the active agent.
- `pkg/tui` — terminal application and its view projection (the interactive TUI).

Core runtime (surface-agnostic):

- `pkg/run` — lifecycle and interactive control of one agent run: controller, compaction, subagents, permissions, skills.
- `pkg/turn` — one turn: executor, scheduler, session state, slash commands, approvals, and the `@` file-mention engine shared by both the terminal and web composers.
- `pkg/agent` — the agent chat loop around an `llm.LLM` (call LLM → run tool calls → repeat), plus the tracer contract (typed events via context) and subagent registries.
- `pkg/assembly` — context assembly: canonical payload, planner, rules, git sources.
- `pkg/tool` — tool registry and built-ins (file, shell, web, MCP, session tools) with request permissions.
- Supporting: `pkg/llm`, `pkg/mcp`, `pkg/memory`, `pkg/skill`, `pkg/state` (SQLite), `pkg/event` (typed events/sinks), `pkg/hook` (hook pipeline, plan mode), `pkg/safety`, `pkg/session`, `pkg/process`, `pkg/telemetry`, `pkg/home` (workspace bootstrap, version).

Integrations:

- `pkg/channel` — per-integration adapters (telegram, discord, slack, feishu, weixin, …). `silk_cgo.go` requires CGO.
- `pkg/config` — YAML config contracts (agents, sandbox, hooks, memory, gateway, channels, permission profiles).

## Project-level MCP and projects

Projects (web UI) and the project-level MCP files (`<root>/.forebrain/mcp_servers.yaml`, `<root>/.mcp.json`) are documented in the docs site: `docs/config/projects-and-mcp.md` (中文镜像 `docs/zh/config/`).

## Migrating from another agent

`/migrate` (terminal only) imports Claude Code's or Codex's on-disk history — sessions, subagent transcripts, memories, skills, plans, MCP entries and input history — into this install; sessions land with `origin='migrated'` and the run is idempotent. Inline form: `/migrate claude --dry-run`, `/migrate codex --home /Volumes/backup/.codex`, `/migrate --only sessions,memories,skills,plans,mcp,history --project`. Project-level configuration (`.mcp.json`, `.claude/settings*.json` permissions, `.codex/config.toml` `[mcp_servers]`) is migrated into Forebrain Harness's own project files (`.forebrain/mcp_servers.yaml`, `.forebrain/safety.json`); Forebrain Harness never reads another agent's project files. Design: `docs/plan/CLAUDE_CODE_MIGRATION_PLAN.md` + `docs/plan/CODEX_MIGRATION_PLAN.md`; implementation: `pkg/migrate`.

## House rules

- **CGO is mandatory on every platform** (Windows included): silk voice decoder, sqlite, fts5. Always build/test with `CGO_ENABLED=1`; CI also uses `-tags fts5`.
- **Keep the package graph green**: CI runs `scripts/package-graph.sh` and fails if `pkg/architecture/testdata/graph.json` changes. After adding/removing packages or changing `pkg/...` imports, regenerate and commit the graph.
- **TUI must not import core runtime internals** — enforced by `pkg/architecture` test (`pkg/tui` may not import gateway/agentrun/clifacade/forkagent/supervisorrun). Communicate through events/types instead.
- **LLM-facing prompt text must be provider-neutral and byte-stable**: system-prompt constants (see `pkg/agent/scope_discipline.go`) contain no model/tool/vendor names and are compile-time constants so the cached prompt prefix never shifts.
- Every package carries a `doc.go`; keep it accurate when you change a package's role.
- Keep package boundaries intact: entrypoints in `cmd/forebrain`, serving in `pkg/gateway`, run lifecycle in `pkg/run`, turn execution in `pkg/turn`, agent loop in `pkg/agent`, context assembly in `pkg/assembly`, tools in `pkg/tool`.
