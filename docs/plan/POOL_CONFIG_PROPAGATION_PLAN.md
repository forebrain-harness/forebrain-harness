# Plan: 事务性地把配置 reload 传播到 gateway project-pool runner

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. All code comments and user-facing strings must stay
> English to match the repository; implementation reports may be Chinese.
>
> **Drift check (run first)**: this worktree has no commit (`git rev-parse HEAD`
> fails) and every repository file is untracked, so `git diff` cannot establish
> a baseline. Re-read the cited symbols before every step. Any mismatch with
> the "Current state" section is a STOP condition.

## Status

- **Priority**: P1
- **Effort**: M — one pool method, one reload integration, one sentinel error,
  and the test matrix that pins them.
- **Risk**: MED — touches the reload path every surface shares. The base
  runner's transaction is untouched; propagation only adds per-entry
  `LoadConfig` calls after the base succeeded.
- **Depends on**: docs/plan/SESSION_MODEL_ISOLATION_PLAN.md (executed 2026-09-28;
  its `Runner.LoadConfig` transaction is the primitive this plan reuses)
- **Category**: bug
- **Planned at**: no repository commit exists; reviewed against the worktree on
  2026-09-28 after the SESSION_MODEL_ISOLATION_PLAN execution.

## Why this matters

A gateway session bound to a web project runs on a pooled project runner whose
`Deps.AppCfg` pointer was copied when the entry was built
(`runner_pool.go:688`). A config reload only rebinds the base Runner
(`config_reload.go` → `env.Runner.LoadConfig(next)`), so every live pool entry
keeps serving on the old config: rotated credentials never reach it, a removed
provider entry still looks configured, and the /model picker catalog (read from
`s.liveCfg()`) lists models the runner's own snapshot marker does not report as
current — the disagreement SESSION_MODEL_ISOLATION_PLAN recorded as finding 40
and its maintenance notes deferred to "a separate process/RunnerPool design".
After this plan, one reload moves every live runner in the process, each
through its own `LoadConfig` transaction, and the catalog/marker disagreement is
gone.

## Current state

- `pkg/process/runner_pool.go:644-709` — `buildEntryLocked` assembles each
  pooled runner's own `run.Deps` value and sets `AppCfg: baseDeps.AppCfg`
  (line 688): a pointer snapshot at build time. `p.mu` guards `entries`,
  `sessions`, `lru`; `closePoolEntries` closes evicted runners **outside** the
  lock because a close waits on MCP children (`runner_pool.go:604-641`).
- `pkg/process/config_reload.go` — `Environment.reloadConfig` loads and
  validates the file, calls `env.Runner.LoadConfig(next)` (the transactional
  pointer swap + pinned reload + rollback from the previous plan), then writes
  `env.Deps.AppCfg = next` when the env's Deps is not the runner's, and
  continues with sandbox/hook/skill refresh. The pool is never consulted.
  `env.pool` exists (created lazily by `RunnerForSession` / eagerly by
  `EnsureRunnerPool`); `env.Deps.AppCfg` is what new entries copy, so entries
  built *after* a reload already get `next`.
- `pkg/run/runner.go` — `loadLocked` starts with
  `if r.closed { return errors.New("runner is closed") }`. `Runner.Close` and
  `LoadConfig` both take `r.mu.load`, so a propagate racing an eviction
  serializes: either Close wins (LoadConfig returns the closed error) or the
  config lands first and Close tears the runner down afterwards. No corruption
  either way; the closed error just needs to be distinguishable.
- `pkg/gateway/slash_handlers.go:211-235` — `ModelSettings` documents the
  liveCfg-catalog vs snapshot-marker disagreement as the recorded boundary;
  `pkg/gateway/slash_handlers_test.go` pins it with
  `TestGatewayCatalogDisagreesWithStalePooledRunner` (a hand-built pooled
  runner whose `AppCfg` predates a reload).
- `pkg/process/runner_pool_test.go` — `newPoolTestEnv(t)` (lines 18-54) builds
  the env + project-store fixture every pool test uses;
  `ensurePoolSessions(t, env, ...)` creates session rows.

## Fixed semantics

1. **One reload, every live runner.** After the base Runner's `LoadConfig`
   succeeds and the new pointer is published to the environment, every live
   pool entry is offered the same `next` through its own `LoadConfig`. Each
   entry keeps its frozen MCP list, project instructions, and identity —
   `LoadConfig` swaps only the config pointer and rebuilds the chain.
2. **Per-entry transaction, per-entry rollback.** An entry whose load fails
   keeps its previous config and runtime (`LoadConfig`'s own rollback); it is
   never left half-switched, and its failure never reverts the base runner or
   the other entries.
3. **Failures are visible, not fatal.** The base reload succeeded, so
   propagation failures are reported in the returned error with truthful
   wording ("config reloaded, but N project runner(s) kept their previous
   configuration"), never silently dropped, and never presented as a total
   reload failure.
4. **Closed entries are skipped.** An entry evicted concurrently with the
   propagation reports the runner-closed condition, which is classified and
   skipped — it is teardown, not a propagation failure.
5. **The freeze contract is untouched.** Propagation never changes
   `MCPServers`, `MCPProject`, project instructions, or session bindings; a
   reload that changed `agents.defaults.mcp_servers` still lands only in the
   next session, exactly as the base runner behaves.

## Commands

| Purpose | Command | Expected |
|---|---|---|
| Process tests | `CGO_ENABLED=1 go test -tags fts5 ./pkg/process -count=1` | ok |
| Run tests | `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -count=1` | ok |
| Gateway tests | `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -count=1` | ok |
| Full tests (CI shape) | `CGO_ENABLED=1 go test -tags fts5 ./... -count=1 -timeout 20m` | all pass |
| Vet | `go vet ./...` | no output |

## Scope

**In scope**:

- `pkg/run/runner.go` — the closed sentinel and its classifier (two lines).
- `pkg/process/runner_pool.go` — `PropagateConfig`.
- `pkg/process/config_reload.go` — call it from `reloadConfig`.
- `pkg/gateway/slash_handlers.go` — the `ModelSettings` comment that documents
  the now-fixed disagreement.
- Corresponding `_test.go` files (all in the same packages; the repository
  caps each package at 20 production files and requires test files to be named
  after a production file, so tests merge into
  `pkg/process/runner_pool_test.go`, `pkg/process/config_reload_test.go`,
  `pkg/gateway/slash_handlers_test.go`, `pkg/run/runner_test.go`).

**Out of scope**:

- any change to `Runner.LoadConfig` itself or the base reload transaction;
- deferring reloads while pooled runs are active (`SetActiveRunProbe` wiring) —
  the timing model is unchanged: an in-flight turn keeps the agent instance it
  captured at turn start, for the base runner today and for pooled runners
  after this plan;
- per-entry effective-config re-derivation from each project's own trust
  context — pooled runners are *built* from the environment's effective config
  (`buildEntryLocked` copies `baseDeps.AppCfg`), so propagating the same `next`
  matches construction semantics;
- per-session model routing (docs/plan/SESSION_MODEL_ROUTING_PLAN.md).

## Git workflow

- Work directly in the current worktree only when instructed to execute this
  plan. Do not commit, push, or open a PR unless the operator explicitly asks.

## Steps

### Step 1: A closed sentinel the pool can classify

In `pkg/run/runner.go`:

- replace the inline `errors.New("runner is closed")` at the top of
  `loadLocked` with a package-level sentinel:

```go
// errRunnerClosed is what any load on a shut-down Runner returns, so callers
// racing a teardown (the runner pool propagating a config reload past an
// eviction) can classify the answer as "gone" instead of "failed".
var errRunnerClosed = errors.New("runner is closed")
```

- add the classifier next to the other run-owned classifiers in
  `pkg/run/config.go` (beside `IsPrimaryModelNotConfigured`):

```go
// IsRunnerClosed reports whether err is the answer a shut-down Runner gives a
// load. The runner pool treats it as teardown, not a propagation failure.
func IsRunnerClosed(err error) bool { return errors.Is(err, errRunnerClosed) }
```

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -count=1` → ok
(`TestRunnerCloseCancelsStartup`'s "a closed Runner must refuse a new load"
assertion must stay green).

### Step 2: `RunnerPool.PropagateConfig`

In `pkg/process/runner_pool.go`, add:

```go
// PropagateConfig offers next to every live pool entry through its own
// LoadConfig transaction: pointer swap, pinned ordinary reload, rollback. An
// entry whose load fails keeps its previous configuration and runtime and is
// named in the joined error; an entry evicted concurrently (its runner already
// closed) is skipped — teardown is not a propagation failure. The pool lock is
// held only to collect entries, because LoadConfig rebuilds a chain and one
// slow entry must not hold up session resolution for every other project.
func (p *RunnerPool) PropagateConfig(ctx context.Context, next *appcfg.Root) error
```

Implementation rules:

- nil pool / nil next → nil;
- collect the entries' runners under `p.mu`, release, then `LoadConfig` each
  one outside the lock;
- `run.IsRunnerClosed(err)` → skip, no error;
- any other error → `slog.Error("config reload: project runner kept its previous configuration", "err", err)` and collect;
- return `errors.Join` of the collected errors (nil when none).

**Verify**: `CGO_ENABLED=1 go vet ./pkg/process` → no output.

### Step 3: Wire it into `reloadConfig`

In `pkg/process/config_reload.go`, inside `reloadConfig`, immediately after
the block that publishes `next` to `env.Deps.AppCfg` (before the
`SessionStore.SetMemoryMode` refresh):

```go
if p := env.pool; p != nil {
    if err := p.PropagateConfig(context.Background(), next); err != nil {
        // The base runner and the environment already adopted next; the
        // entries named here keep their previous runtime (LoadConfig's own
        // rollback). Truthful wording matters: this is not a failed reload.
        return fmt.Errorf("config reloaded, but some project runners kept their previous configuration: %w", err)
    }
}
```

Notes:

- `env.pool` is nil until the first `RunnerForSession`/`EnsureRunnerPool`; nil
  means nothing to propagate, not an error.
- The gateway builds the pool eagerly at startup, so live deployments always
  propagate.
- Do **not** add propagation to the TUI's `applyConfigFromDisk`: a TUI process
  never binds a session to a project row, so its pool exists only as an empty
  shell (see `RunnerForSession`'s no-project path) and there is nothing to
  propagate — no code for unreachable states.

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/process -count=1` → ok
(the four existing reload gates —
`TestReloadConfigRollsBackEveryFieldWhenRunnerLoadFails`,
`TestReloadConfigLeavesMCPServersFrozen`, and the others in
`config_reload_test.go` — must stay green unchanged).

### Step 4: The test matrix

In `pkg/process/runner_pool_test.go` (fixture: `newPoolTestEnv`), add:

1. `TestRunnerPoolPropagateConfigReachesEveryEntry` — build the pool, bind two
   sessions to two projects, write a reordered config (second provider first,
   rotated api_key), call `PropagateConfig` with the loaded root: every pooled
   runner's `AppCfg` is the new pointer, its published selection survived
   (`run.PrimaryModelSelection`), and `run.PrimaryEndpoint` shows the rotated
   base URL; a session resolved afterwards still lands on the same entry.
2. `TestRunnerPoolPropagateConfigKeepsFailingEntryOnOldConfig` — same setup,
   then propagate a config with **no providers at all** (parses, startup-check
   passes, but no agent definition resolves): `PropagateConfig` returns an
   error naming the entry; the pooled runner's `AppCfg` pointer is unchanged
   and its published selection is unchanged; the base runner (not propagated
   here) is untouched.
3. `TestRunnerPoolPropagateConfigSkipsClosedRunners` — build one entry, close
   the pool (`pool.Close()`), then call `PropagateConfig` on the (now empty)
   pool → nil; and directly `LoadConfig` a closed runner →
   `run.IsRunnerClosed` is true.
4. `TestReloadConfigPropagatesToPoolEntries` in
   `pkg/process/config_reload_test.go` — env with a bound project session,
   write the reordered file, `env.ReloadConfig()` → the pooled runner's
   `AppCfg` equals the new config and `env.Deps.AppCfg` is the same pointer.

In `pkg/gateway/slash_handlers_test.go`, **rewrite**
`TestGatewayCatalogDisagreesWithStalePooledRunner` into
`TestGatewayCatalogAgreesAfterPropagatedReload`: build the env + pool + bound
session, capture the pooled runner, write a reordered config with a rotated
api_key, call `s.ReloadModelCatalog(context.Background(), sid)` (which routes
through `Env.ReloadConfig`), then assert on the pooled runner: `AppCfg` is the
new pointer, the published selection survived, and `ModelSettings`'s catalog
(the reloaded `liveCfg`) and its snapshot marker name the same provider set —
the disagreement this test used to pin is fixed.

Also update the `ModelSettings` comment in
`pkg/gateway/slash_handlers.go` that documents the disagreement: after this
plan, reload propagation keeps pooled runners' configs in step with the
catalog; remove the "recorded follow-up" sentence for it (the per-session
routing follow-up stays — that is the other plan).

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/process ./pkg/gateway -count=1` → ok.

### Step 5: Full verification

1. `go vet ./...` → no output.
2. `CGO_ENABLED=1 go test -tags fts5 ./... -count=1 -timeout 20m` → all pass.
3. `scripts/package-graph.sh && CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` → graph regenerates (LOC may change), tests pass, edges unchanged.

## Done criteria

- [ ] A config reload reaches every live pooled runner: same `AppCfg` pointer,
      retained pin, refreshed provider details.
- [ ] A failing entry keeps its previous config and runtime and is named in
      the returned error; the base reload is not reverted by it.
- [ ] A closed (evicted) entry is skipped, not reported.
- [ ] The gateway catalog/marker disagreement test now asserts agreement
      through `ReloadModelCatalog`.
- [ ] The four pre-existing reload gate tests pass unchanged.
- [ ] Vet, full fts5 test suite, and architecture tests pass; graph edges
      unchanged.

## STOP conditions

- `Runner.LoadConfig` cannot be called on a pooled runner without violating
  the freeze contract (e.g. it turns out to reset `MCPServers`).
- An entry's failure turns out to require reverting the base runner to keep
  any invariant the codebase actually tests.
- The gateway disagreement test cannot be made to agree without touching
  out-of-scope files.

## Maintenance notes

- Reload timing is unchanged: `SetActiveRunProbe` is still unwired, so a
  hot-reload can land while a pooled turn is running; the turn keeps the agent
  it captured at start, the same exposure the base runner has today. Wiring
  the probe (or an idle barrier across pooled runners) is a separate,
  standalone change if the operator wants it.
- Pooled runners still share the *environment's* effective config
  (`EffectiveConfig` for the process launch dir), by construction since
  `buildEntryLocked`. If per-project trust ever needs to shape each entry's
  config, that is a buildEntryLocked change, not a propagation change.
- `docs/plan/SESSION_MODEL_ROUTING_PLAN.md` builds on this: its gateway
  comments should describe a pool whose entries track the live config.
