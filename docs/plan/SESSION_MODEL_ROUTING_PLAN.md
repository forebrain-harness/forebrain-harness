# Plan: gateway 共享 runner 上的真 per-web-session 模型路由

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
- **Effort**: L — crosses run, assembly, process and gateway; every effective
  primary-model consumer that can observe a per-session choice becomes
  context-aware.
- **Risk**: HIGH — changes LLM-client routing for every surface. The design
  keeps the TUI byte-equivalent (its runner pin already equals its session
  selection) and keeps the prompt-shaping wrappers above the router so the
  cached prefix never moves.
- **Depends on**: docs/plan/SESSION_MODEL_ISOLATION_PLAN.md (executed 2026-09-28:
  the effective snapshot, `fb_session_model_state`, and the Runner transactions
  this plan builds on). Execute after
  docs/plan/POOL_CONFIG_PROPAGATION_PLAN.md so the gateway comments this plan
  rewrites describe a pool whose entries track the live config.
- **Category**: bug
- **Planned at**: no repository commit exists; reviewed against the worktree on
  2026-09-28 after the SESSION_MODEL_ISOLATION_PLAN execution.

## Why this matters

Every web session without a project row runs on the gateway's one base Runner,
and every session bound to the same project shares that project's pooled
runner. Today `/model` on the web calls `SetPrimaryModel` — the Runner's one
mutable pin — so two web sessions on one runner overwrite each other's model
mid-conversation, and the last writer silently moves the other session's next
turn. SESSION_MODEL_ISOLATION_PLAN deliberately scoped its gateway seam to the
runner level and recorded true per-session routing as the next design:
"a request/session context selector with a per-run stable client snapshot".
This plan implements exactly that: a session's stored choice
(`fb_session_model_state`) is resolved once per turn, carried in the run
context, and every LLM call of that run — including fork and subagent children,
which inherit the context — routes through it, while the Runner's own pin
stays the process default.

## Fixed semantics

1. **The context is the selector.** `RunContent` resolves the session's stored
   choice once at turn entry (one store read per turn, never per LLM call) and
   puts it in the run context. The value is immutable for the run: a
   concurrent `/model` by another session cannot move an in-flight turn.
2. **Injection never overwrites.** A context that already carries a selection
   (a subagent or fork child inheriting its parent's) keeps it. Children run
   on the session's model — the same "runs on whatever the primary runs on"
   rule the typed-subagent fallback already implements.
3. **Routing sits under every prompt-shaping wrapper.** The router wraps only
   the base client, below `wrapTypedSubagentProviderLLM` and far below the
   skill-catalog/memory/plan-mode wrappers, so the request prefix a provider
   has cached is byte-identical whichever model a session picks. Typed
   subagents with their own chains and explicit run-level overrides
   (`WithSubagentModelOverride`) are intercepted above the router and are not
   re-routed.
4. **Runner pin is the default, not the channel.** The gateway never calls
   `SetPrimaryModel`/`ResetPrimaryModel` anymore. A session with no stored
   choice (or one whose pair the config no longer offers) runs on the runner's
   published snapshot — the same ordinary-reload fallback semantics the TUI
   has, applied per turn.
5. **Stale rows heal on the next turn.** When the stored pair no longer
   resolves, the turn runs on the runner's default, the row is rewritten to
   that default (best effort, logged on failure), and a warning is logged —
   the gateway-side equivalent of the TUI's `ActivateSessionModel` fallback,
   minus the interactive warning card (the marker reads the same resolution,
   so the picker never claims a model that is not configured).
6. **Identity readers follow the context.** Every consumer that names "the
   model this conversation runs on" — the assembly compaction budget and
   summary client, the compact-hook metadata, the approval-hook model, the
   runtime hook's model limits, the gateway's /model marker, /status,
   context gauge and persisted assistant `model` field — resolves through the
   same context-first helper. `PrimaryModel(r)` itself is unchanged: for the
   TUI the pin *is* the session truth, and for defaults/unloaded fixtures it
   stays the documented fallback.
7. **Selection is durable and tenant-scoped.** The gateway writes
   `fb_session_model_state` through the same agent-guarded store API the TUI
   uses; validation happens before the write, so an unconfigured pair is
   refused without touching the file default.

## Current state

- `pkg/run/config.go` (merged primary-model code from the previous plan):
  - `runnerPrimaryState` holds the published snapshot + health gate;
    `SetPrimaryModel`/`ResetPrimaryModel`/`LoadConfig` are the transactions;
    `PrimaryModelSelection(r)`/`PrimaryReasoningEffort(r)` are the exported
    reads; `resolveAgentProvider` + `modelSelector` do exact-pair resolution;
    `loadWithIntentLocked`'s explicit branch shows the effort-overlay rule
    (`appcfg.WithReasoningEffort(entry.Params, effort)` — empty is exact).
- `pkg/run/runner.go`:
  - `loadLocked` builds the chain: base client via `NewLLMForAgentConfig(&cfg)`
    (line ~894), then `wrapTypedSubagentProviderLLM` (~933), then
    `wrapSubagentModelOverrideLLM` (~936), then every prompt-shaping wrapper.
  - `RunContent` (~1296) is the single funnel every surface's turns pass
    through (executor `run.Run`, `RunPipeline`, TUI dispatch).
  - `approvalHookModel()` (~700) and `runCompactHook`'s model metadata
    (~1594) read the snapshot; both call sites have a ctx.
  - `compactSvc := assembly.Service{...}` (~974) wires `PrimaryModel` and
    `CompactLLM: r.effectivePrimaryClient`; `compactDeps.ActiveModel` (~1011)
    is `func() (string, string)`.
- `pkg/run/compaction.go`:
  - `CompactionService` (~262) wires `out.CompactLLM = r.ContextCompactLLM`
    and `out.PrimaryModel = func() (string, string) { return PrimaryModel(r) }`;
  - `CompactChainDeps.ActiveModel` (~90) is ctx-free, called via
    `activeModel()` from `pkg/run/llm_middleware.go:332,341` and
    `pkg/run/compaction.go:120` — all three have a ctx.
- `pkg/assembly/service.go`: `Service.PrimaryModel func() (string, string)`
  (line 47) called at 217 (`autoCompactDecision`, has ctx+sessionID), 550
  (`summaryInputBudget`, caller has ctx); `Service.CompactLLM func() llm.LLM`
  (line 48) called at 331 (`compactMessages`, has ctx).
  `pkg/assembly/runtime_hook.go`: `HookParams.PrimaryModel func() (string, string)`
  (line 21) consumed at 36 inside `ModelLimitsProvider = func(sessionID string)`
  — the session id is already in scope there.
- `pkg/process/open.go:342-351` wires `HookParams.PrimaryModel` through a local
  `primaryModel(runner)` helper (line 142: `return run.PrimaryModel(r)`); the
  surrounding scope holds `runner` and `sessStore`.
- `pkg/process/run_executor.go`: `runExecutor.Run` resolves the session's
  runner and funnels into `run.Run` → `RunContent`; the gateway websocket
  handler builds `agCtx` (`pkg/gateway/server.go:1681`) and, for the canonical
  path, hands it to `Core.Submit` with `AgentContextIsRunContext`.
- `pkg/run/subagent.go:662` — fork children build `agentCtx := ctx` (context
  values are inherited); `pkg/run/supervisor.go:165,175` likewise derive from
  `o.AgBase`.
- `pkg/gateway/slash_handlers.go`:
  - `ModelSettings` (~211) maps the runner snapshot into
    `turn.ModelSettings.Selection`;
  - `SelectModel` (~237) calls `r.SetPrimaryModel` — the leak this plan
    removes — and documents itself as runner-level;
  - `/status` and the context report (~65, ~85) read
    `run.PrimaryModel(r)` where `r` is already `runnerFor(ctx, sessionID)`.
- `pkg/gateway/run_control.go:306-308` — `appendTranscriptTurns` sets the
  persisted assistant `model` from `run.PrimaryModel(s.Runner)` (the *base*
  runner, not even the session's runner).
- `pkg/gateway/slash_handlers_test.go` (merged from the previous plan):
  `TestGatewaySelectionIsRunnerLevelNotPerWebSession` pins the runner-level
  boundary this plan replaces; `TestGatewayModelSettingsReadsRunnerSelection`,
  `TestWebModelPickerSwitchesTheModel` cover the current seam.
- `pkg/state/session_store.go` — `SessionModelSelection(ctx, sid)` /
  `SaveSessionModelSelection(ctx, sid, sel) (stored bool, err)` are the
  agent-guarded row APIs; `turn.SessionStore` does not need them (run reads
  the concrete `*state.SessionStore` off `Deps`).

## Commands

| Purpose | Command | Expected |
|---|---|---|
| Run tests | `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -count=1` | ok |
| Assembly tests | `CGO_ENABLED=1 go test -tags fts5 ./pkg/assembly -count=1` | ok |
| Process tests | `CGO_ENABLED=1 go test -tags fts5 ./pkg/process -count=1` | ok |
| Turn tests | `CGO_ENABLED=1 go test -tags fts5 ./pkg/turn -count=1` | ok |
| TUI tests | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` | ok |
| Gateway tests | `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -count=1` | ok |
| Full tests (CI shape) | `CGO_ENABLED=1 go test -tags fts5 ./... -count=1 -timeout 20m` | all pass |
| Race gate | `CGO_ENABLED=1 go test -race -tags fts5 ./pkg/run ./pkg/gateway -run 'TestSessionModel' -count=1` | ok |
| Vet | `go vet ./...` | no output |

## Scope

**In scope** (every package is at the 20-production-file cap; all new code
merges into existing files):

- `pkg/run/config.go` — context carrier, resolver, router wrapper, helpers.
- `pkg/run/runner.go` — chain insertion, `RunContent` injection, ctx-aware
  readers/closures.
- `pkg/run/compaction.go` — ctx-aware `CompactionService` closures,
  `ActiveModel`, `ContextCompactLLM(ctx)`.
- `pkg/run/llm_middleware.go` — pass ctx to `activeModel`.
- `pkg/assembly/service.go`, `pkg/assembly/runtime_hook.go` — ctx-taking
  closure types.
- `pkg/process/open.go` — session-aware `HookParams.PrimaryModel`.
- `pkg/gateway/slash_handlers.go`, `pkg/gateway/run_control.go` —
  session-aware reads; `SelectModel` without runner mutation.
- Corresponding `_test.go` files in those packages.

**Out of scope**:

- any change to the TUI's `SelectModel`/`ActivateSessionModel`/pin semantics —
  the TUI's pin already equals its session selection, so injection is a no-op
  there by construction;
- `run.PrimaryModel`/`PrimaryModelSelection` signatures and their TUI callers;
- typed-subagent own chains, the guardian reviewer client, and the subagent
  run-level override (`WithSubagentModelOverride`) — all intercepted above the
  router by design;
- the prompt-shaping wrappers and the cached-prefix contract (they sit above
  the router and never see the swap);
- channel surfaces gaining `/model` (they do not implement
  `turn.ModelSlashHandler`; sessions without a row simply run the default);
- per-session gateway routing *across processes* — each process resolves the
  row per turn from the shared store, so a cluster stays correct without new
  coordination.

## Git workflow

- Work directly in the current worktree only when instructed to execute this
  plan. Do not commit, push, or open a PR unless the operator explicitly asks.

## Steps

### Step 1: The context carrier, the resolver, the router (pkg/run/config.go)

Add beside the existing primary-model code:

```go
// WithSessionModelSelection carries one session's effective model choice for
// the life of a run. RunContent injects it once per turn from the session's
// stored row; children (forks, subagents) inherit it with the context, which
// is what keeps "runs on whatever the primary runs on" true per session.
func WithSessionModelSelection(ctx context.Context, sel PrimaryModelState) context.Context

// SessionModelSelectionFromContext reports the selection this run carries.
func SessionModelSelectionFromContext(ctx context.Context) (PrimaryModelState, bool)

// ResolveSessionModelChoice validates an exact provider+model pair against
// the runner's config and overlays the concrete effort (empty is exact),
// without touching the runner. It is the one resolution every surface shares:
// gateway selection validates through it, and turn entry resolves through it.
func ResolveSessionModelChoice(r *Runner, provider, model, effort string) (PrimaryModelState, error)
```

`ResolveSessionModelChoice` reuses `resolveAgentProvider(r.AppCfg,
r.activeAgentNameForModel(), modelSelector{provider: provider, model: model})`
and `appcfg.WithReasoningEffort`; a miss wraps `errModelNotConfigured`.

Then the router, modeled on `subagentModelOverrideLLM`
(`pkg/run/subagent.go:1959-1989` — unexported struct, `wrapX` constructor,
per-key client cache under a mutex, `build` injected as a function so tests
can observe routing):

```go
// sessionModelLLM routes a run's LLM calls to the model its context selected.
// It wraps only the base client: every prompt-shaping wrapper above it is
// model-agnostic, so the provider's cached prefix never moves when a session
// picks a different model.
type sessionModelLLM struct {
    inner llm.LLM
    build func(PrimaryModelState) (llm.LLM, error)
    mu       sync.Mutex
    clients  map[string]llm.LLM
}

func wrapSessionModelLLM(inner llm.LLM, build func(PrimaryModelState) (llm.LLM, error)) llm.LLM
```

`Execute` rules:

- no selection in ctx → `inner`;
- selection present → `clientFor(sel)` (cache key
  `strings.ToLower(provider+"/"+model+"/"+effort)`);
- build error (the pair vanished under a mid-run reload) →
  `slog.Warn("session model no longer configured; the call runs on the runner's current model", ...)`
  and fall to `inner` — the ordinary-reload fallback semantics, not a failed
  turn.

The production `build` (a `func (r *Runner) sessionModelClient(sel
PrimaryModelState) (llm.LLM, error)`) resolves the exact pair with the effort
overlay and constructs via the existing `llmYAMLsFromResolved` + `NewLLMFromYAML`
path — the same construction `loadWithIntentLocked`'s explicit branch uses.

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -count=1` → ok (new
unit tests below; nothing else changed yet).

### Step 2: Chain insertion and per-turn injection (pkg/run/runner.go)

- In `loadLocked`, immediately before
  `llmClient = wrapTypedSubagentProviderLLM(llmClient, typeLLMs)` (~line 933):

```go
// A run's context may carry its session's own model choice; route the base
// client on it. It sits below the typed-subagent wrapper so types with their
// own chain keep it, and below every prompt-shaping wrapper so the cached
// prefix is model-agnostic.
llmClient = wrapSessionModelLLM(llmClient, r.sessionModelClient)
```

- At the top of `RunContent` (after the health gate, before the load lock):

```go
ctx = r.injectSessionModelSelection(ctx)
```

where the helper is:

- if `SessionModelSelectionFromContext(ctx)` already has a value → return ctx
  (children keep their inheritance);
- `sid := llm.AgentSessionIDFromContext(ctx)`; empty or
  `r.Deps.SessionStore == nil` → return ctx;
- row, ok := store.`SessionModelSelection(ctx, sid)`; no row → return ctx;
- `ResolveSessionModelChoice(r, row.Provider, row.Model, row.Effort)`:
  - ok → `WithSessionModelSelection(ctx, resolved)`;
  - not-configured → rewrite the row to the runner's current published
    selection (`SaveSessionModelSelection`, best effort, warn log on failure),
    `slog.Warn` the credential-free fallback, return ctx unchanged.

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/tui -count=1` → ok
(the TUI suites are the regression net: its pin equals its row, so injected
value and inner client are the same model).

### Step 3: Context-aware identity readers

- `pkg/assembly/service.go`: change the closure types to
  `PrimaryModel func(ctx context.Context) (string, string)` and
  `CompactLLM func(ctx context.Context) llm.LLM`; thread ctx into
  `summaryInputBudget`. Every call site (217, 331, 550) already has one.
- `pkg/assembly/runtime_hook.go`: `HookParams.PrimaryModel` becomes
  `func(sessionID string) (string, string)`; `ModelLimitsProvider` already
  receives the session id.
- `pkg/run/compaction.go`: `CompactChainDeps.ActiveModel` becomes
  `func(ctx context.Context) (string, string)`; `activeModel(ctx)`;
  `CompactionService` wires `out.PrimaryModel = func(ctx context.Context) (string, string)`
  and `out.CompactLLM = func(ctx context.Context) llm.LLM` through the new
  runner helpers; `ContextCompactLLM` becomes
  `func (r *Runner) ContextCompactLLM(ctx context.Context) llm.LLM`.
- `pkg/run/llm_middleware.go:332,341`: pass the Execute ctx into
  `activeModel(ctx)`.
- `pkg/run/runner.go`: `approvalHookModel(ctx)` (caller `actionHook` has ctx);
  `runCompactHook`'s model read via the same helper; the `compactSvc`
  closures in `loadLocked` become ctx-taking.
- Add the two helpers in `pkg/run/config.go`:

```go
// effectiveModelFor names the model this context's conversation runs on: the
// run's carried selection first, then (for callers outside a turn, e.g. an
// explicit /compact) the session's stored row resolved against the live
// config, then the runner's published snapshot, then the config-order
// fallback an unloaded test fixture uses.
func (r *Runner) effectiveModelFor(ctx context.Context) (provider, model string)

// sessionClientFor builds the client this context's conversation should run
// on, mirroring effectiveModelFor's resolution order.
func (r *Runner) sessionClientFor(ctx context.Context) llm.LLM
```

- `pkg/process/open.go`: `HookParams.PrimaryModel` becomes
  `func(sessionID string) (string, string)` resolving through a new exported
  `run.PrimaryModelForSession(r *Runner, store *state.SessionStore, sessionID string) (string, string)`
  (row → resolve → `PrimaryModel(r)` fallback); the local `primaryModel`
  helper at open.go:142 stays for the ctx-free fallbacks.

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/assembly ./pkg/process -count=1` → ok
(adapt assembly tests that construct `Service`/`HookParams` to the new
signatures; only `pkg/run` constructs them in production).

### Step 4: The gateway seam (pkg/gateway)

- `SelectModel(ctx, sessionID, choice, effort)`:
  1. resolve the runner (`runnerFor`);
  2. `run.ResolveSessionModelChoice(...)` — a miss surfaces the
     not-configured error (the turn layer already words it);
  3. `s.Sessions.Ensure(ctx, sessionID, sessionID)`, then
     `SaveSessionModelSelection(ctx, sessionID, resolved)`; `stored == false`
     → an error saying the session could not be recorded;
  4. no `SetPrimaryModel`, no partial-outcome wrapping — a row write either
     lands or nothing changed, so plain errors are the truthful contract.
  Update the method comment from "runner-level" to per-session.
- `ModelSettings`: resolve the marker session-first —
  `run.ResolveSessionModelChoice` on the stored row; on no row or a
  not-configured pair, fall back to `run.PrimaryModelSelection(r)`. The
  catalog stays `s.liveCfg()` (entries track it after
  POOL_CONFIG_PROPAGATION_PLAN).
- `/status` and the context report (`slash_handlers.go` ~65, ~85): provider,
  model and endpoint through the session-aware resolution (a new small helper
  on Server or direct `run.` calls — both stores and runner are in scope
  there).
- `run_control.go` `appendTranscriptTurns`: the persisted assistant `model`
  comes from the session-aware resolution for `sid` (today it reads the *base*
  runner — wrong even before this plan for pooled sessions).
- Rewrite `TestGatewaySelectionIsRunnerLevelNotPerWebSession` into
  `TestGatewayModelSelectionIsPerSession`: two sessions on the base runner,
  each `SelectModel` a different configured pair, assert each
  `ModelSettings(sid)` marker names its own pair and the runner's published
  snapshot never moved (it stays the loaded default).
- `TestWebModelPickerSwitchesTheModel` asserts the row is written for the
  session (the persisted assistant-model assertion moves to the new e2e test
  below).

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway ./pkg/turn -count=1` → ok.

### Step 5: The test matrix

- `pkg/run` (`runner_test.go` / `config.go`-adjacent tests, merged file):
  1. `TestSessionModelRouterRoutesOnContext` — wrap a recording inner with a
     fake `build`; with a selection in ctx, the fake client is used; without,
     the inner is; a build error falls to the inner;
  2. `TestRunContentInjectsSessionModelSelection` — loaded runner, session
     row pinned to the second provider, a stub LLM override capturing ctx:
     `RunContent` sees the selection in ctx; the row is consulted once per
     turn;
  3. `TestRunContentHealsStaleSessionModelRow` — row names a removed pair;
     after `RunContent` the row equals the runner's current selection and the
     turn ran on the default;
  4. `TestSessionModelSelectionInheritedByChildContexts` — a ctx with a
     selection passed through `WithAgentSessionID` (as `subagent.go:662`
     does) still carries it after `injectSessionModelSelection`;
  5. `TestSessionModelClientCoversEffort` — the built client reflects the
     stored concrete effort (observable via `ResolveSessionModelChoice` +
     builder invocation, no network).
- `pkg/gateway` (`slash_handlers_test.go`):
  6. `TestGatewayModelSelectionIsPerSession` (Step 4);
  7. `TestGatewayTurnRunsSessionModelEndToEnd` — two webchat sessions on the
     base runner, each with its own stored selection, both turns executed
     through `run.Run` with a stub LLM override; assert each turn's persisted
     assistant `model` field names its own session's model and the two
     differ;
  8. the /status and context-gauge reads name the session's model.
- `pkg/tui`: no new tests required by this plan; the full suite is the
  regression net (pin == row == routed model).

**Verify**: all package tests plus the race gate:

```
CGO_ENABLED=1 go test -race -tags fts5 ./pkg/run ./pkg/gateway -run 'TestSessionModel|TestGatewayModelSelection|TestGatewayTurnRuns' -count=1
```

### Step 6: Full verification

1. `go vet ./...` → no output.
2. `CGO_ENABLED=1 go test -tags fts5 ./... -count=1 -timeout 20m` → all pass.
3. `scripts/package-graph.sh && CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` → edges unchanged, LOC retained.
4. Scope audit against a before-edit checksum manifest of `pkg/` — every
   changed file must be in Scope.

## Done criteria

- [ ] Two web sessions sharing one runner keep independent model selections;
      neither `/model` moves the other's next turn or the runner's pin.
- [ ] A turn's LLM calls route on the context selection; fork/subagent
      children inherit it; typed subagents with own chains and explicit run
      overrides do not.
- [ ] The prompt prefix is model-agnostic: the router sits below every
      prompt-shaping wrapper (assert by wrapper-order test or code
      inspection note in the PR).
- [ ] A stale stored pair heals on the next turn: default runs, row
      rewritten, warning logged.
- [ ] /status, the context gauge, the /model marker and the persisted
      assistant `model` all name the session's own model on the gateway.
- [ ] `rg -n "SetPrimaryModel" pkg/gateway` returns no production hits.
- [ ] TUI behavior is unchanged: full suite green, pin semantics untouched.
- [ ] Vet, full fts5 suite, race gate, architecture tests pass; graph edges
      unchanged.

## STOP conditions

- Any prompt-shaping wrapper turns out to read the base client's model
  identity (the router would then change prefix bytes — report before
  proceeding).
- `assembly.Service` has an implementer besides `pkg/run` that cannot supply
  a ctx at its call sites.
- The TUI suites show any behavior change from injection (the pin and the row
  disagreeing anywhere is a design failure, not a test to adapt).
- The gateway e2e test cannot observe the routed model without network
  access.

## Maintenance notes

- The runner's published snapshot is now the *process default* for gateway
  turns; only the TUI moves it (`ActivateSessionModel`/`SelectModel`). New
  surfaces should resolve per session through the context, never by mutating
  the pin.
- The guardian reviewer and typed-subagent clients still build from config
  order: a policy reviewer and per-type chains are process-level by design.
- `te.Model` (token-estimator fallback) stays global — it is only the fallback
  when `agents.defaults.token_estimate` names nothing, and the estimator is
  process-global state regardless.
- Channel surfaces get per-session routing for free the day they implement
  `turn.ModelSlashHandler`; the row APIs are already tenant-guarded.
- A cluster stays correct without coordination: every process resolves the
  row per turn from the shared store.
