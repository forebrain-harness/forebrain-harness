# FOREBRAIN.md

Local AI coding agent. Single Go module (`github.com/forebrain-harness/forebrain-harness`), Go 1.26, CGO mandatory.

## Commands

First build on a fresh checkout: `make build` — web UI + binary + dictionary + `models.json` into `build/bin/`. It also fetches the native dependencies (`scripts/fetch-libtokenizers.sh`, `scripts/fetch-pdfium.sh`, `scripts/fetch-pdfium-fonts.sh` → `third_party/...`); `make build` and `make test` re-check them on every run. Direct `go` commands that compile `pkg/pdf` (build, vet, test) additionally need `PKG_CONFIG_PATH` pointing at the fetched PDFium triple — the Makefile sets it only inside make, so carry it yourself when calling go directly (CI and `CONTRIBUTING.md` do the same); a cold build without it fails in pkg/pdf's cgo step.

```bash
make build        # UI + binary + dict/models.json -> build/bin/forebrain (runs make ui first)
make test         # CGO_ENABLED=1 go test -tags fts5 ./... -count=1 (fetches native deps first)
make ui           # build Vue frontend into the Go embed package (pkg/gateway/dist)
make docker       # container image
make release      # per-platform npm packages (npm/scripts/build-platform-packages.sh)
make clean

export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"

# Day-to-day Go build (fastest, no frontend rebuild):
CGO_ENABLED=1 go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain
scripts/install-dictionary.sh ./build/bin   # dict/ beside the binary (memory search reads it)
scripts/install-assets.sh ./build/bin       # models.json beside the binary (already there after make build)

# Lint / vet (no golangci-lint; this is what CI runs):
gofmt -l cmd pkg third_party                 # must print nothing
CGO_ENABLED=1 go vet -tags fts5 ./...

# Tests (CGO: silk voice decoder + sqlite; -tags fts5: memory-search FTS5 tables):
CGO_ENABLED=1 go test -tags fts5 ./... -count=1
CGO_ENABLED=1 go test -tags fts5 ./pkg/turn -run TestName   # single package/test

# In an agent session, run the suite against a scratch home (see House rules):
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./... -count=1

# Store dialect matrix (when touching pkg/state): a plain go test runs SQLite;
# MySQL 8 + PostgreSQL 14 run in scratch containers:
scripts/test-db.sh up && scripts/test-db.sh all    # also: sqlite | mysql | postgres | check | down

# Frontend (Node 22, pnpm 10):
cd frontend && pnpm install && pnpm typecheck && pnpm test && pnpm build
# browser e2e goes through scripts/acceptance/web_e2e.sh — see House rules
```

Packaging flows (only when touching the npm layer): `npm/scripts/build-platform-packages.sh` builds the host triple by default; override with `FOREBRAIN_BUILD_TARGETS="darwin/arm64 linux/amd64"`. `cd npm && npm link` to test the launcher flow.

## Architecture

Two runtime surfaces share one core:

- `cmd/forebrain` — thin Cobra entrypoint (`gateway`, `interactive`, `resume`, `serve`, `lsp`); keep CLI wiring here, push behavior down into `pkg/...`.
- `pkg/gateway` — HTTP/WS server and REST API; embeds the Vue build output (`pkg/gateway/dist`) via go:embed. Also binds channel integrations for the active agent.
- `pkg/tui` — terminal application and its view projection (the interactive TUI).

Core runtime (surface-agnostic):

- `pkg/run` — lifecycle and interactive control of one agent run: controller, compaction, subagents, permissions, skills.
- `pkg/turn` — one turn: executor, scheduler, session state, slash commands, approvals, the `@` file-mention engine shared by both the terminal and web composers, and auto-continue (a turn stopped by a usage limit resumes by itself once the limit resets; the terminal and the web only show, cancel and run the engine's continuation).
- `pkg/agent` — the agent chat loop around an `llm.LLM` (call LLM → run tool calls → repeat), plus the tracer contract (typed events via context) and subagent registries.
- `pkg/assembly` — context assembly: canonical payload, planner, rules, git sources.
- `pkg/tool` — tool registry and built-ins (file, shell, web, MCP, session tools) with request permissions.
- `pkg/process` — the composition root: builds each runner and wires the Layer-3 siblings and capability packages together; the one package allowed to know everything (its fan-out is exempt from the budget table).
- Supporting: `pkg/llm`, `pkg/mcp`, `pkg/memory`, `pkg/skill`, `pkg/state` (SQLite by default; MySQL 8 and PostgreSQL 14 dialects, exercised by `scripts/test-db.sh`), `pkg/event` (typed events/sinks), `pkg/hook` (hook pipeline, plan mode), `pkg/safety`, `pkg/session`, `pkg/telemetry`, `pkg/home` (workspace bootstrap, version).
- `pkg/lsp` — language-server runtime (Layer 2 in the table below): the built-in catalog (`pkg/lsp/catalog/*.yaml`), JSON-RPC and protocol types, one process pool per forebrain process, a Manager per runner that implements `tool.CodeIntelligence`/`tool.CodeIntelControl`. Only `pkg/process` and `cmd/forebrain` import it; tools and surfaces go through the ports in `pkg/tool/search.go` and the DTOs in `pkg/event/lsp.go`.

Integrations:

- `pkg/channel` — per-integration adapters (telegram, discord, slack, feishu, weixin, …). `silk_cgo.go` requires CGO.
- `pkg/config` — YAML config contracts (agents, sandbox, hooks, memory, gateway, channels, permission profiles).

### Layering (enforced by `pkg/architecture` tests)

Layer assignment from `docs/plan/TUI_FIRST_RUNTIME_REFACTOR.md` §3.1/§11.1; a package may import only strictly lower layers (the few same-layer exceptions are enumerated in the tests):

| Layer | Packages |
| :--- | :--- |
| 0 | `agent`, `llm` |
| 1 | `state`, `config`, `home`, `telemetry`, `channel`, `peer` |
| 2 | `tool`, `pdf`, `hook`, `mcp`, `event`, `assembly`, `safety`, `skill`, `memory`, `migrate`, `lsp` |
| 3 | `session`, `turn`, `run` |
| 4 | `process` (composition root, no fan-out limit) |
| 5 | `tui`, `gateway` (surfaces) |

- When a package needs a sibling or a lower layer, define the interface on the consumer and have `process` inject the implementation — a new direct import is the last resort, not the default.
- Per-package fan-out (how many `pkg/...` packages it imports) is a ratchet (`fanOutBudgets` in `pkg/architecture/cache_test.go`): a ceiling never grows, and when a real count drops you must lower its constant. `tui`, `gateway`, `run`, `tool`, `assembly` sit above their §3.7 targets today (tracked P4–P7 debt); their enforced ceiling is the measured number.
- Same file also enforces: Layer-3 siblings never import each other; `state` never imports `event` (turn projects step rows into events); `event` never imports `tool`; `tool`/`hook` never import `agent`; `agent` imports only `llm`; the `llm` root imports no provider SDKs (adapters live in `pkg/llm/<provider>`); `gateway` never imports `tui`; no top-level `internal/`.

## Model data

`assets/models.json` beside the binary is the catalog, and it is the only one:
nothing is downloaded at runtime and no copy lives in a forebrain home (owner
2026-11-09). Every process of a build reads the same file, and a build that ships
no catalog reports that and runs with no catalog data instead of fetching one.

The catalog is regenerated offline from a models.dev snapshot, never by the
running product: `scripts/merge-catalog-into-models.sh` takes the snapshot
(`assets/catalog.json`, never committed), calls `scripts/update-models-asset.sh`
and leaves only `assets/models.json` to review and commit
(`npm/scripts/build-platform-packages.sh` runs the same step when the snapshot
is present). The updater produces the canonical `models` table, injects the
`opencode`/`opencode-go` rows from the providers tree, and lifts
`reasoning_options` out of it; its `OVERRIDES` table is the only place a local
correction may differ from upstream — each entry carries a dated reason and a
golden test. A model the snapshot no longer carries keeps the
`reasoning_options` it had, so a provider row disappearing upstream does not
erase levels users can still send; `lost-options` counts what could not be
preserved and fails the run when it is non-zero.

Reasoning-effort levels have exactly one source: the catalog's
`reasoning_options`, trimmed to what the build can send on that model's protocol.
A model with no level there is not offered a picker. Capabilities a configured
model overrides — and the levels a user picks by hand — are that row's data in
the database (web `设置 › 模型`), applied over the catalog at lookup; they are
not written back into `models.json`. The two OpenCode surfaces and ChatGPT are
exceptions only in where their models come from — an account login — so their
rows are reference data and a model listing skips them.

## Project-level MCP and projects

Projects (web UI) and the project-level MCP files (`<root>/.forebrain/mcp_servers.yaml`, `<root>/.mcp.json`) are documented in the docs site: `docs/config/projects-and-mcp.md` (中文镜像 `docs/zh/config/`).

## Language servers

Design and task plans: `docs/plan/LSP_CODE_INTELLIGENCE_PLAN.md` and `docs/plan/lsp/`. Rules that keep it safe to change:

- Never start a server on the `Runner.Load` path or in a first-frame path; everything starts lazily or in the background.
- The `lsp` tool is registered once per session (`Deps.CodeIntelTool`); its description and schema are constants. Enabling, installing or restarting a server never changes the tool table.
- Model-visible texts (tool description, diagnostics block, late-diagnostics reminder, error messages) are verbatim from the spec's appendices B and C and have golden tests.
- Unit tests use the fake server in `pkg/lsp/instance_test.go`; real servers run only with `FOREBRAIN_LSP_INTEGRATION` set (`.github/workflows/lsp-integration.yml`).
- Project entries (`<root>/.forebrain/lsp_servers.yaml`) apply only in trusted, version-controlled projects and only after per-entry consent.

## Migrating from another agent

`/migrate` (terminal only) imports Claude Code's or Codex's on-disk history — sessions, subagent transcripts, memories, skills, plans, MCP entries, language-server plugins and input history — into this install; sessions land with `origin='migrated'` and the run is idempotent. **Runs are incremental**: the state database keeps a fingerprint (size + mtime) per imported transcript, so a rerun does not reparse anything that has not moved, a transcript that grew appends only its new rows (keyed by source message id), and a first sighting of a pre-ledger import keeps the old skip semantics while backfilling the fingerprint. Extracted Codex plans are self-healing: a deleted or hand-edited plan file forces the owning transcript to be parsed again on the next run. Memory consolidation after an import runs only when the import actually wrote notes — a zero-new-note rerun pays no pass — and the pass integrates only the workspace's own diff, never native rollouts' stage-1 extraction. Progress renders as the compact card's percentage bar above the composer. `/migrate` takes no inline arguments: a picker asks for the source, then its data directory, then the session scope, then shows a dry-run preview before anything is written. Categories are not selectable — an import always runs every category the source holds. Enabled Claude Code LSP plugins become enabled catalog servers (official plugins) or custom `lsp.servers` entries (other plugins). Project-level configuration (`.mcp.json`, `.claude/settings*.json` permissions, `.codex/config.toml` `[mcp_servers]`) is migrated into Forebrain Harness's own project files (`.forebrain/mcp_servers.yaml`, `.forebrain/safety.json`); Forebrain Harness never reads another agent's project files. Design: `docs/plan/CLAUDE_CODE_MIGRATION_PLAN.md` + `docs/plan/CODEX_MIGRATION_PLAN.md`; implementation: `pkg/migrate`.

## House rules

- **CGO is mandatory on every platform** (Windows included): silk voice decoder, sqlite, fts5. Always build/test with `CGO_ENABLED=1`; CI also uses `-tags fts5`.
- **A test or acceptance script must never inherit an ambient `FOREBRAIN_HOME`.** It is the one variable that can aim a harness at a real install, and the harness then writes there: a 2026-10-14 acceptance run whose home came from the environment overwrote `~/.forebrain/forebrain.yaml` with a test config and left multi-gigabyte pre-migration backups in `~/.forebrain/state/backups/`. Take the home from a variable the harness itself creates or names (`FOREBRAIN_ACCEPT_HOME` for the memory-search acceptance), put it under `mktemp -d`, and never fall back to `$HOME` or to `FOREBRAIN_HOME`. Any path that writes a user's home needs a proof that on CI and on a developer machine it resolves to a throwaway directory.
- **Run the full suite with an explicit scratch home** — `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./... -count=1`. An agent session exports `FOREBRAIN_HOME` pointing at the real install, and a test that opens the state database migrates it: `cmd/forebrain` (pinned by its own `TestMain`) and `pkg/tui` drive the real terminal entrypoint, so an unpinned home writes a multi-gigabyte pre-migration backup into `~/.forebrain/state/backups/` and then blocks for the ten-minute test timeout on the write lock of the forebrain the user has open. Tests in packages that open state should pin their own home (`t.Setenv`); a package `TestMain` that pins one **must override an inherited value**, not only fill in an unset one.
- **Keep the package graph green**: CI runs `scripts/package-graph.sh` and fails if `pkg/architecture/testdata/graph.json` changes. After adding/removing packages or changing `pkg/...` imports, regenerate and commit the graph.
- **Run only the e2e specs a change touches; the whole browser suite is a release gate, not a per-change loop.** `scripts/acceptance/web_e2e.sh` takes `FOREBRAIN_E2E_SPEC` as one or more spec files, directories or globs (`FOREBRAIN_E2E_SPEC="e2e/subagent-*.spec.ts"`). A task-scoped run is about two minutes including the build; the full 160-case suite is about nine. Choose by what the diff can reach — touching a shared composer, shell or drawer component pulls in every spec that drives it. When a plan's done criteria name a full-suite run, that text is the contract and wins over this default.
- **One engine, two surfaces.** Behavior shared by the terminal and the web belongs in the shared engine (`pkg/turn`, `pkg/run`), not in one surface.
- **TUI must not import core runtime internals** — enforced by `pkg/architecture` test (`pkg/tui` may not import gateway/agentrun/clifacade/forkagent/supervisorrun). Communicate through events/types instead.
- **LLM-facing prompt text must be provider-neutral and byte-stable**: system-prompt constants (see `pkg/agent/scope_discipline.go`) contain no model/tool/vendor names and are compile-time constants so the cached prompt prefix never shifts. The prompt-cache hit rate is a guarded metric: `pkg/architecture/testdata/cache_baseline.json` pins per-provider replay cases, and `scripts/hitrate.sh BASELINE CURRENT` diffs two cache reports — a change that breaks the cached prefix shows up as a falling cache-read rate.
- **Package shape is test-enforced** (`pkg/architecture`): every package carries a `doc.go` — keep it accurate when you change a package's role; at most 30 production files per package (at least 2); test files sit beside and are named after the production file they cover (`X_test.go` for `X.go`); file names carry at most two underscores; imports are grouped stdlib-first with no duplicate imports in a file; generic package names (`utils`, `common`, `core`, …) are rejected; subpackages exist only where enumerated (`pkg/llm/<provider>`, `pkg/state/dbx`).
- **Run `scripts/peer-protocol-check.sh` after upgrading the local Claude Code, and before changing `pkg/peer`.** forebrain's cross-process interop clones Claude Code's private peer-messaging protocol, which moves between releases; the script greps the installed binary for the protocol's stable literals and checks the rendezvous records, so a protocol change is caught at upgrade time instead of as an interop failure. It is read-only, and it exits 0 when at least one Claude Code on the machine carries the protocol — an install that does not is reported as a WARNING, because a machine can hold more than one.

## Development workflow (branch, commit, pull request)

`main` is protected: every change lands through a pull request that passes all eight required checks. Direct pushes to `main` are rejected.

1. Branch from latest `main` with a short slug: `fix/<what>`, `feat/<what>`, `docs/<what>`.
2. Commit with the Conventional Commits header this repo enforces —
   `<type>(<scope>): <subject>` (type: feat fix perf refactor test docs build ci chore revert; subject lowercase, no trailing period, ≤100 chars) —
   and a body following the `.gitmessage` template: `Why:` / `What:` / `Verification:` (only commands actually run). Sign with `git commit -s -F <file>`; the DCO check requires a `Signed-off-by:` matching the author. `make hooks` installs the local hooks that check and auto-sign.
3. Open the PR against `main`. The PR title must pass the same Conventional Commits check (the squash commit inherits title and body from the PR, so the PR description doubles as the commit body). The release automation reads these titles, so a wrong type silently changes what a release would include.
4. All required checks must pass. The full Windows test suite runs as a non-blocking job (`continue-on-error`) until it is Windows-ready; everything else gates.

Community rules, setup, and the DCO text live in `CONTRIBUTING.md`; issue and PR templates in `.github/`.

## Release rules (what an agent may and may not do)

Releases are fully automated; there is no manual publishing step, and hand-running `npm publish` or `gh release create/upload` is forbidden.

- **Versions are derived from commit titles on `main`**, by release-please: `feat:` bumps the minor, `fix:`/`perf:`/`revert:` bump the patch, and a `!` in the title or a `BREAKING CHANGE:` footer marks breaking (still minor-only while on 0.x, to keep the module path un-versioned). `docs:`, `test:`, `ci:`, `chore:`, `build:`, and `refactor:` never trigger a release. Dependabot's `build(deps):` titles do not trigger one either — dependency upgrades ship with the next feat/fix.
- **When `main` gains a releasable commit, the Release workflow runs release-please**, which opens or updates one pull request titled `chore(main): release X.Y.Z` containing the CHANGELOG, the version bumps (`VERSION` plus the six npm version fields), and a rebuilt web UI (`pkg/gateway/dist`) so builds from the tagged source — the release archives, the Docker image, a local `make build` — embed a current frontend. It keeps the PR updated on every later push to `main`.
- **Merging that release PR is the release itself.** The merge creates the `vX.Y.Z` tag and GitHub Release, then the pipeline builds five platforms (darwin/arm64, darwin/amd64 cross-compiled, linux/amd64, linux/arm64, windows/amd64 — all CGO), uploads the archives, the dictionary bundle and SHA256SUMS, publishes the six npm packages with provenance (platform packages before the launcher; an already-published version is skipped).
- **Only the owner merges the release PR** — that click is the approval gate. An agent must prepare and verify it (checks green, CHANGELOG sane) but never merge, never push to `main` directly, never re-tag or delete a published tag (the Go checksum database already records it), and never publish by hand. If a release job fails, rerun that job; the pipeline is safe to re-enter (npm skips published versions, assets upload with `--clobber`).
- To pin a specific version, add a `Release-As: X.Y.Z` footer to a commit body.
