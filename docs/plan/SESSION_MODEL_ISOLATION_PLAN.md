# Plan: 隔离运行时模型选择，并完整恢复 TUI 会话选择

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. All code comments and user-facing strings must stay
> English to match the repository; implementation reports may be Chinese.
>
> **Drift check (run first)**: this worktree has no commit (`git rev-parse HEAD`
> fails) and every repository file is untracked, so `git diff` cannot establish
> a baseline. Re-read the cited symbols before every step. Also copy
> `pkg/architecture/testdata/graph.json` to a `mktemp` path before editing; it is
> needed later to prove that package edges did not change. Any mismatch with the
> “Current state” section is a STOP condition.

## Status

- **Priority**: P1
- **Effort**: L — the fix crosses config, state, run, turn, TUI and gateway, and
  must cover every TUI session-transition path rather than startup resume only.
- **Risk**: HIGH — this changes LLM-client construction, config reload, session
  activation and state-schema migration. The steps below make Runner publication
  transactional, keep prompt-shaping wrappers unchanged, and require targeted
  race/regression tests before the full suite.
- **Depends on**: none
- **Category**: bug
- **Planned at**: no repository commit exists; reviewed against the worktree on
  2026-09-28, with a second source-verified review pass the same day
  (findings 31-35) and a third the same day (findings 36-40), both re-checking
  every cited file:line against the live tree.
- **Issue**: not published

## Outcome

After this plan is implemented:

- `/model` and `/connect` still affect the current TUI session immediately;
- reordering the shared `forebrain.yaml` in one process no longer changes the
  provider, model, or reasoning effort already pinned by another running process;
- API key, base URL, API path and non-effort request parameters still refresh
  from a valid config reload on every Runner that participates in that reload;
- a TUI session restores its own provider/model/effort on startup **and** on an
  interactive `/resume`/session switch;
- `/new` starts from the latest file default, while `/fork` inherits the source
  session’s model choice;
- `/agent` cannot carry the previous agent’s pin into the new agent;
- if a pinned model was genuinely removed from the config, reload succeeds by
  falling back to the new configured default and records an explicit warning;
- gateway runners are protected from other processes/config reorder, but web
  sessions that share one runner still share one runtime selection. True
  per-web-session routing remains a separate follow-up.

The shared file remains the default for sessions that have no stored choice.
Changing that default is intentionally last-writer-wins; the file is no longer
the live-selection channel for already running sessions.

## Strict-review findings incorporated into this revision

| # | Defect in the previous plan | Impact | Correction in this plan |
|---|---|---|---|
| 1 | `effort == ""` meant “follow the entry default” | another process changing the same model’s effort still changed this process | pin the **concrete effective effort**, including an explicitly empty value, and overlay it on every reload |
| 2 | `modelSel.Store` happened before `Load()` | readers could report the new model while the old client was active; failed loads left a bad pin | resolve/build first under `r.mu.load`, publish one immutable effective snapshot only after success, and rebuild the previous snapshot after a partial failure |
| 3 | a missing pinned model made all config reloads fail | deleting a model could permanently block unrelated config changes and breaks `TestCharacterizationPrefixSurvivesConfigReload` | ordinary reload falls back to the new file default with a warning; an explicit user selection still fails rather than silently substituting |
| 4 | the pin did not include the active agent | `ApplyAgent` reuses the same Runner, so `/agent` would carry or reject the previous agent’s model | include normalized agent identity; agent mismatch derives and pins the new agent’s default |
| 5 | only `ChatSession.ResumeSession` restored state | interactive `/resume`, `/new`, `/fork`, subagent/session notifications use `switchStreamSession` and bypass that method | activate model state before every TUI session switch; abort the switch on a fatal activation error |
| 6 | no-state sessions did nothing | `/new` retained the previous session’s pin | no stored row means `ResetPrimaryModel()` to the latest live-config default |
| 7 | fork behavior was unspecified, and copying only an existing row is insufficient | a source that never ran `/model` can have no row even though its Runner is pinned, so the fork could unexpectedly use a newer file default | materialize the active source snapshot through `SelectModel`, then copy its row in `copySlashSessionState` |
| 8 | startup footer was drawn before restore | the resumed session could run one model while the footer displayed another | activate first, then refresh footer/model budget; render non-fatal fallback warning |
| 9 | state read/write ownership was incomplete | a SessionStore rebound to another primary agent could read a row by leaked session id | gate reads, writes and copies by `agent_id`, matching `requireOwned`/`HasSession` conventions |
| 10 | saving assumed the session row already existed | `/model` or `/connect` used as the first command could silently fail to persist | TUI ensures the session row first; state save reports whether a row was actually written |
| 11 | the v2→v3 migration used `return execCopy(...)` | it does not compile because `execCopy` returns `(int64, error)` | use `ExecContext`/proper arity |
| 12 | v2→v3 used unconditional `CREATE TABLE` | v0/v1 migrations rebuild from current `schema.sql`, then later migration steps would create the table again | make the v2→v3 create idempotent and test v0, premature-v1 and v2 upgrade paths |
| 13 | `SelectModel` had no context and `/connect` used undefined placeholders | persistence had no valid `ctx`; `handleConnect` did not receive a session id | pass `context.Context` and the active session id through the real seams |
| 14 | a proposed run helper returned a `turn` type | `run` cannot import `turn` without violating package ownership/cycles | `run` exports its own read-only selection value; TUI/gateway map it to `turn.ModelSelectionState` |
| 15 | several effective-model readers were missed | approval-hook metadata, explicit compaction, effort footer and compaction clients could disagree with the actual LLM | route every primary-model reader/client through the published effective snapshot |
| 16 | file write preceded live application | a failed live switch could still change the global default; replies could lie about partial success | apply+persist the session first, then save the independent new-session default and report partial success precisely |
| 17 | `ReloadModelSettings` mixed “select this live model” with “refresh the in-memory catalog/default” | deleting it entirely leaves `AppCfg` on the old order after a successful file write, so an immediate `/new` can pick the wrong default | replace it with separate `SelectModel` and `ReloadModelCatalog` operations; ordinary catalog reload preserves the live pin |
| 18 | scope omitted `pkg/config`, `pkg/tui/run.go` and `pkg/tui/notify.go` | the written steps could not compile within their own scope | scope below includes every required production file and its tests |
| 19 | build/graph verification was incorrect | build omitted required `fts5`; graph was expected unchanged even though it stores LOC and the repo has no git baseline | build with `-tags fts5`; regenerate graph, expect LOC changes, and compare saved `.edges` arrays |
| 20 | config-reload rollback was assumed to restore the Runner | TUI restores only the config pointer after a failed `Load`; process reload attempts a rebuild but discards its error, so the live Runner may remain partially mutated | centralize pointer swap, build and rollback in Runner `LoadConfig`; both reload owners must report rollback failure |
| 21 | the activation helper was private but `switchStreamSession` only owns a `Session` interface | the central switch path had no callable seam and the plan could not compile as written | add `ActivateSessionModel` to `Session`; make both startup resume and interactive switching use it |
| 22 | provider-detail refresh was stated as universal | existing gateway project-pool runners hold their own old `AppCfg` pointers and `Environment.ReloadConfig` reloads only the base Runner | test the selected gateway runner boundary and record pool-wide reload propagation as a follow-up instead of claiming it here |
| 23 | a boolean “model applied” error contract treated failed rollback as a healthy new runtime | if both the requested restore and Runner's internal rollback fail, the published identity may be new while load-owned clients/tools are inconsistent | expose a typed runtime-uncertain error and use a three-state surface outcome; tell the user to restart/reload instead of asserting a model is active |
| 24 | effort persistence read params from config index zero | when a session is pinned to m1 but the shared default is m2, changing m1 effort could relabel m2's params/credentials as m1 | update the exact provider+model entry, preserve its complete config, then promote that entry atomically |
| 25 | `ChooseModel` fell back to `ApplyLLMUpdates` after exact promotion failed | a stale picker or concurrent file edit could relabel index zero while retaining the wrong provider's credentials; a second item in `models: [...]` also relied on this unsafe path | normalize/expand entries before exact promotion and fail without saving when the pair disappeared; remove the fallback |
| 26 | asynchronous session-change replies were rendered before `switchStreamSession` | `/new` or `/fork` could say the user entered the target even when model activation later failed | carry the success reply with the switch event, activate/switch first, and render success only afterward; on failure keep the old session and report the created target as recoverable |
| 27 | config reload writes `Runner.AppCfg` before taking `r.mu.load` | concurrent `SetPrimaryModel` can resolve against a pointer being replaced even though both builds later take the load lock | make Runner own a `LoadConfig(next)` transaction that swaps the pointer only while holding `r.mu.load`; remove direct pre-lock writes from process/TUI reload paths |
| 28 | fallback activation changed Runner before rewriting the stale DB row | if that write failed and the UI switch was aborted, the outgoing session silently kept the target's fallback runtime | capture the outgoing snapshot and restore it on every post-apply fatal error; fail closed if restoration fails |
| 29 | runtime-uncertain was only a message classification | after a double build/rollback failure, another turn could still use a half-mutated Runner | mark the Runner unhealthy, make run entry points reject work, and clear the flag only after a complete successful rebuild |
| 30 | `/agent` mutates agent/workspace/stores before reloading the shared Runner | if the new agent load fails, rebuilding the old model alone cannot restore the old tenant boundary | treat agent-mismatch load failure as unhealthy/fail-closed; surface the switch failure and serve no further turn until the target boundary rebuilds successfully |
| 31 | the fork step told `copySlashSessionState` to call `CopySessionModelSelection`, but turn has no seam for it | the step could not compile: `copySlashSessionState` (executor.go:908-941) holds no store, and turn's only session-state seam is the `turn.SessionStore` interface (model.go:273-281) | add `CopySessionModelSelection(ctx, source, target) (bool, error)` to `turn.SessionStore`; `*state.SessionStore` satisfies it from Step 2; update turn fakes; run the copy under `ctx.commandContext()` |
| 32 | outcome constants were used as error values ("wrap it as `turn.ModelRuntimeUncertain`") | Steps 6/7 could not compile: Step 5 defined `ModelNotApplied`/`ModelAppliedNotDurable`/`ModelRuntimeUncertain` as `ModelApplyOutcome` constants plus an interface, not error types | define one exported concrete error type `ModelSelectionError{Outcome, Err}` implementing `ModelApplyOutcome() ModelApplyOutcome` and `Unwrap()`; classify through an unexported interface; construct via `NewModelSelectionError(outcome, err)` |
| 33 | the new Runner transaction APIs never stated the lock-reentrancy rule | `LoadConfig`/`SetPrimaryModel` calling the public `Load` while holding `r.mu.load` self-deadlocks: it is a non-reentrant `sync.RWMutex` (runner.go:193-204; the permRuntimeOnce comment at 163-169 records the same hazard) | all three APIs build through the internal `loadLocked` path while holding the lock, and refresh filesystem policy only after unlocking, exactly as `Load` does today (runner.go:345-357) |
| 34 | `pkg/tui/reducer.go` was out of scope | the switch reply is not atomic without it: today the reply frame (chat_surface.go:100-106) and the reducer's "session switched" frame (reducer.go:1029-1037) render before run.go:365 performs the switch | add `pkg/tui/reducer.go` to scope; hold a switched result's frames until `switchStreamSession` succeeds, then render them in the target session |
| 35 | existing regression tests that pin the reload/rollback contract were never named | Step 3 could silently weaken `TestReloadConfigRollsBackEveryFieldWhenRunnerLoadFails`, `TestReloadConfigLeavesMCPServersFrozen`, `TestPrimaryModelExpandsTheModelsList`, and `TestPrimaryModelFallsBackToMainAndNil` | name all four as gates that must stay green (adapting internals only); model Step 9's two-runner fixture on the config_reload_test.go:104-115 pattern |
| 36 | the fork copy named `INSERT ... SELECT ... ON CONFLICT DO UPDATE` with no WHERE on the SELECT | SQLite's parser binds `ON` to the SELECT and rejects the statement — Step 2 fails with a confusing syntax error, and a missing target row surfaces as a raw foreign-key error | require `SELECT ... WHERE true` before the upsert clause, and an agent-scoped target-existence check so a missing target is rejected by policy (see Step 2) |
| 37 | Step 5 widens `ModelSlashHandler` but never states that `pkg/tui` and `pkg/gateway` stop compiling until Steps 6-8 | an executor "checking the build" between steps sees breakage and improvises stub implementations | declare the expected intermediate breakage; per-package verifies only until Step 10's full build; stubs are forbidden (see Step 5) |
| 38 | `TestModelChoiceDoesNotLeakToSecondProcess` was listed under Step 6, but its two-Environment fixture is built in Step 9 | a weak executor writes a hollow duplicate of the test in Step 6 | the name belongs to Step 9's cross-process matrix; Step 6 owns single-process cases only |
| 39 | signature ripples into direct test callers were left implicit | unexplained compile errors at `switchStreamSession`/`ResumeSession` test call sites | name them: `chat_surface_test.go:740` (`ResumeSession`), `chat_session_test.go:14392/14414/14431` and `run_test.go:2778` (`switchStreamSession`) |
| 40 | gateway `ModelSettings` keeps reading the picker catalog from `s.liveCfg()` while the current marker becomes snapshot-backed | on a project-pool runner whose `AppCfg` predates a reload, the catalog can list models the marker says are not current — untested disagreement | document the boundary and pin it with a gateway test; pool-wide reload propagation stays the recorded follow-up (see Step 8) |

## Fixed semantics

These rules are part of the implementation contract, not suggestions:

1. **One published effective snapshot.** Runner owns an immutable snapshot with
   normalized agent, provider, model, concrete effort, and a deep-cloned resolved
   provider config. Provider secrets stay internal; exported accessors expose
   only provider/model/effort.
2. **Concrete effort.** On first load/default reset, read the resolved entry’s
   current effort and pin that string. Empty is a concrete “no effort” value,
   never “re-read the file next time.” Reload overlays the pinned effort while
   refreshing all other provider fields.
3. **Three load intents.** Ordinary `Load` keeps the current snapshot when its
   agent and exact provider/model still resolve; `SetPrimaryModel` requires the
   exact explicit pair and never falls back; `ResetPrimaryModel` intentionally
   adopts the config’s first resolved entry.
4. **Removed-model fallback.** Only ordinary reload may fall back when the pin is
   gone. It logs the old and new identities without credentials. Explicit user
   selection must return a typed/sentinel “not configured” error.
5. **Transactional publication.** Resolve the candidate and complete the Runner
   rebuild under `r.mu.load`; store the snapshot only on success. If rebuilding
   fails after mutating load-owned fields, rebuild the previous effective
   snapshot before returning. Join rollback failure into the returned error and
   make that condition detectable as “runtime uncertain”; a snapshot value alone
   is not proof that all load-owned fields were restored. Config-pointer swaps
   use the same transaction/lock; callers never write `Runner.AppCfg` first. A
   rollback failure marks Runner unhealthy; `Run`/`RunContent` reject work until
   a later complete `Load`, `LoadConfig`, `SetPrimaryModel`, or
   `ResetPrimaryModel` succeeds and clears the flag. A failed ordinary `Load`
   after agent identity changed is also unhealthy: the surrounding tenant/store
   mutations cannot be repaired by rebuilding only the old model snapshot.
   `r.mu.load` is a non-reentrant `sync.RWMutex` (runner.go:193-204): the new
   Runner APIs (`LoadConfig`, `SetPrimaryModel`, `ResetPrimaryModel`) must build
   through the internal `loadLocked` path while holding the lock — calling the
   public `Load` from inside would self-deadlock — and may refresh filesystem
   policy only after unlocking, exactly as `Load` does today (runner.go:345-357;
   see the permRuntimeOnce comment at runner.go:163-169 for the same hazard).
6. **File and session are separate outcomes.** TUI live apply + DB persistence is
   one operation. Saving `forebrain.yaml` afterward only changes the default.
   A default-save failure must not undo a successfully applied current-session
   choice and must be reported as such. After a successful save, synchronously
   reload the in-memory catalog/default without changing the live pin; report a
   catalog-reload failure as a third, distinct partial-success outcome.
7. **Session activation.** Stored choice → restore exact choice; no row → reset to
   file default; unavailable stored choice → reset to file default, overwrite
   the stale row with the fallback, continue, and show a warning; DB/client errors
   other than “not configured” restore the outgoing Runner snapshot before
   aborting the session switch. Failed restoration leaves Runner fail-closed.
8. **Session creation/fork.** `/new` has no row and therefore gets the default.
   Before `/fork` copies state, it persists the active source Runner snapshot
   (without writing the default file), then copies that row before target
   activation. Session ids remain globally keyed; all state APIs still verify
   current primary-agent ownership.
9. **Gateway boundary.** `Server.SelectModel` applies to `runnerFor(sessionID)`.
   That runner can serve multiple web sessions, so the selection is runner-level
   and is not written to `fb_session_model_state` in this plan.
10. **Exact effort persistence.** A file-default effort update locates the exact
    provider+model entry, changes only `reasoning.effort`, then promotes that
    whole entry. It never borrows params or credentials from index zero.
11. **No stale-choice synthesis.** File persistence accepts only an exact pair
    that still exists after reloading the persisted config. It expands
    `models: [...]` before matching; a disappeared pair is a conflict/error, not
    permission to synthesize a new entry from index zero.

## Current state

The following was read directly from the 2026-09-28 worktree and re-verified
line-by-line in the second review pass.

### Shared-file pollution

- `pkg/turn/executor.go:769-841`: `chooseModel`/`chooseReasoningEffort` first write
  `forebrain.yaml`, then call `ReloadModelSettings`.
- `pkg/turn/models.go:389-423`: `ChooseModel` promotes a provider entry to index
  zero; `ChooseReasoningEffort` writes `params.reasoning.effort` into that entry.
- `pkg/process/config_reload.go:21-111`: all processes watch the shared config;
  every Write/Create/Rename requests a reload.
- `pkg/process/config_reload.go:192-269` and
  `pkg/tui/chat_session.go:1863-1908`: reload swaps `AppCfg` and calls
  `Runner.Load`. Process reload resets the pointer and attempts the old load but
  discards its error; TUI resets only the pointer and does not rebuild the old
  Runner. The pointer write also happens outside `r.mu.load`. Both paths need one
  Runner-owned, observable transaction.
- `pkg/run/llm_middleware.go:93-105`: `NewLLMForAgentConfig` constructs only
  `cfg.LLMChain[0]`.

### Runner ownership and readers

- `pkg/run/runner.go:114-178`: Runner has no current-model field today.
- `pkg/run/runner.go:180-204`: `r.mu.load` guards the load-owned agent/client/tool
  cluster.
- `pkg/run/runner.go:345-357`: `Load` holds that lock around `loadLocked` and
  refreshes filesystem policy after unlocking.
- `pkg/run/runner.go:804-1175`: `loadLocked` builds wrappers and tools and publishes
  `mainCfg`/`main` only near the end. It must receive the candidate effective
  provider; do not store a public selection before this build succeeds.
- `pkg/run/config.go:118-197`: `modelSelector`, `resolveAgentProvider` and
  `resolveAgentLLM` already define exact-pair resolution. Reuse them; do not
  invent model-only fallback for an explicit session choice.
- Primary-chain reads that must be reconciled:
  - `approvalHookModel` at `pkg/run/runner.go:700-708`;
  - automatic compaction closures at `runner.go:920-961`;
  - telemetry fallback at `runner.go:1113-1114` (it is correct only if the local
    `cfg.LLMChain[0]` was rewritten to the effective provider);
  - `ContextCompactLLM` at `runner.go:1411-1418`;
  - `PrimaryModel`/`PrimaryEndpoint` at `runner.go:1438-1460`;
  - compact-hook metadata at `runner.go:1534-1547`.
- `SubagentOwnModel` at `runner.go:1475-1494` is intentionally about a typed
  subagent’s own chain; do **not** redirect it to the primary snapshot.
- `PrimaryModel` currently supports unloaded test Runners by resolving config
  directly (`runner_test.go:320-356`). Preserve that fallback when no effective
  snapshot has ever been published.
- `pkg/process/agent_switch.go:60-92` mutates and reloads the **same** Runner;
  a model pin therefore must carry/check agent identity.

### Session switching and persistence

- `pkg/tui/chat_session.go:274-304`: `ResumeSession` is used for CLI startup and
  currently only validates/ensures the row.
- `pkg/tui/run.go:106-123`: startup draws the footer before calling
  `ResumeSession`.
- `pkg/tui/run.go:1455-1490`: every in-process switch funnels through
  `switchStreamSession`, which currently changes only UI/session id state.
- `pkg/turn/executor.go:274-299`: `/new` ensures a new row.
- `pkg/turn/executor.go:350-389` plus `copySlashSessionState`: `/fork` copies
  conversation-side state but not a model choice.
- `pkg/state/session_store.go:151-203`: session ids are tenant data;
  `requireOwned` and `HasSession` are the ownership patterns to follow.
- `pkg/state/session_store.go:1986-2019`: prompt state demonstrates an
  INSERT-guarded child table, but is first-write-wins and must not be reused for
  mutable model state.

### Schema migration constraints

- `pkg/state/schema_migrations.go:16-39`: current version is 2; migrations run
  sequentially.
- `migrateStateV0ToV1` at `schema_migrations.go:238-299` rebuilds from the
  **current embedded `schema.sql`**. Therefore a later “create new table” step
  must tolerate the table already existing.
- `execCopy` at `schema_migrations.go:442` returns `(int64, error)`, not `error`.
- Existing required regression paths are
  `TestStateMigrationMatchesFreshSchema`,
  `TestStateMigrationRepairsPrematureV1Version`, and
  `TestStateOpenIsIdempotentAtCurrentVersion`.

### Surface seams

- `pkg/turn/model.go:206-219`: `ModelSlashHandler` currently exposes
  `ModelSettings` + `ReloadModelSettings` only.
- `pkg/tui/chat_slash.go:669-685` and
  `pkg/gateway/slash_handlers.go:211-232` implement that seam.
- `pkg/tui/setup.go:1371-1446`: `handleConnect` receives only `ctx`, although the
  call site at `pkg/tui/run.go:1054-1056` has the active `state.sessionID`.
- `pkg/tui/chat_slash.go:232-233` reads effort from config order rather than from
  the effective runtime; it must use the Runner snapshot.
- `pkg/turn/model.go:273-281`: `turn.SessionStore` is the only session-state seam
  turn owns (`Ensure`, `ListSessionsRecent`, `ForkInto`, `SetTitle`,
  `SetParentSessionID`, `ListChildSessionsRecent`); `copySlashSessionState`
  (executor.go:908-941) holds no store of its own, so any fork copy of model
  state must go through this interface.
- `pkg/tui/chat_surface.go:88-118`: a handled slash result emits its reply as a
  standalone `NewMessageMsg` first, then `SessionSwitchedMsg{Reason: Reply}`;
  `pkg/tui/reducer.go:1029-1037` turns that into `EventResult{Frames,
  SwitchSessionID}`, and `pkg/tui/run.go:365-367` performs the switch — i.e.
  the success frames render in the outgoing session before any switch happens.
- `pkg/tui/commands.go:42-52`: interactive `/resume` only prompts for a choice;
  its callers (`run.go:383`, `run.go:1035`, `run.go:3961`) switch directly
  through `switchStreamSession` and never call `ResumeSession`. Startup
  (`run.go:109`) is the only `ResumeSession` caller, so session-model
  activation must live in the shared switch path, not only in `ResumeSession`.

## Commands

| Purpose | Command | Expected |
|---|---|---|
| Config tests | `CGO_ENABLED=1 go test -tags fts5 ./pkg/config -count=1` | ok |
| State tests | `CGO_ENABLED=1 go test -tags fts5 ./pkg/state -count=1` | ok |
| Runner tests | `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -count=1` | ok |
| Process tests | `CGO_ENABLED=1 go test -tags fts5 ./pkg/process -count=1` | ok |
| Turn tests | `CGO_ENABLED=1 go test -tags fts5 ./pkg/turn -count=1` | ok |
| TUI tests | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` | ok |
| Gateway tests | `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -count=1` | ok |
| Full tests (CI shape) | `CGO_ENABLED=1 go test -tags fts5 ./... -count=1 -timeout 20m` | all pass |
| Vet (CI shape) | `go vet ./...` | no output |
| Build (CI shape) | `CGO_ENABLED=1 go build -tags fts5 ./...` | exit 0 |
| Architecture | `scripts/package-graph.sh && CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` | graph regenerated; tests pass |

## Scope

**Production files in scope**:

- `pkg/config/agent_llm.go` — shared reasoning-effort overlay helper.
- `pkg/state/schema.sql`, `pkg/state/schema_migrations.go`,
  `pkg/state/session_store.go` — durable model state and migration.
- `pkg/run/runner.go`, `pkg/run/config.go` — effective snapshot, resolution,
  transactional reload/switch/reset and all primary-model readers.
- `pkg/process/config_reload.go`, `pkg/process/agent_switch.go` — make reload
  rollback observable and make a failed agent-boundary rebuild fail closed;
  watcher/debounce behavior stays unchanged.
- `pkg/turn/model.go`, `pkg/turn/models.go`, `pkg/turn/executor.go` — live
  selection seam, truthful result handling and fork-state copy.
- `pkg/tui/chat_slash.go`, `pkg/tui/chat_session.go`, `pkg/tui/chat_surface.go`,
  `pkg/tui/setup.go`, `pkg/tui/run.go`, `pkg/tui/notify.go` — selection
  apply/persist, connect, resume result, truthful switch notification and
  centralized session activation.
- `pkg/tui/reducer.go` — the `SessionSwitchedMsg` case (1029-1037) builds the
  switched result's frames and `SwitchSessionID`; making switch replies atomic
  (render only after a successful switch) runs through it.
- `pkg/gateway/slash_handlers.go` — runner-level gateway selection.
- Corresponding `_test.go` files.
- `pkg/architecture/testdata/graph.json` — regenerate because it stores LOC;
  package edges should remain unchanged.

**Out of scope**:

- changing `pkg/process/config_reload.go` watcher/debounce behavior;
- removing shared-file default writes from `ChooseModel`,
  `ChooseReasoningEffort`, or `/connect`;
- true per-session gateway routing or gateway model-state persistence;
- pool-wide propagation of config/provider-detail reloads to already-created
  gateway project runners;
- `/fast`, typed-subagent models, subagent review overrides, MCP freeze rules,
  permissions, or prompt-shaping wrapper behavior;
- changing session ownership or primary-agent switching semantics beyond making
  the model snapshot agent-aware;
- adding file locks around last-writer-wins default updates.

## Git/worktree workflow

- Work directly in the current worktree only when instructed to execute this
  plan. Do not commit, push, or open a PR unless the operator explicitly asks.
- Because there is no HEAD, create a before-edit checksum manifest for `pkg/`
  and record the temporary path. At completion, compare it and account for every
  changed path against the in-scope list. `git status` alone is not a scope check
  in this repository.
- Save the original package graph separately. After regeneration, compare
  `jq -S '.edges' <before>` with `jq -S '.edges'
  pkg/architecture/testdata/graph.json`; edges must be identical. LOC changes are
  expected and must be committed with an implementation when the repository has
  a real git baseline.

## Steps

### Step 1: Add a reusable reasoning-effort overlay

In `pkg/config/agent_llm.go`, add an exported pure helper with this contract:

```go
// WithReasoningEffort returns a deep-owned copy of params with
// reasoning.effort set to the normalized non-empty effort, or removes only
// reasoning.effort when effort is empty. Other params and other reasoning keys
// are preserved.
func WithReasoningEffort(params LLMRequestParams, effort string) LLMRequestParams

// ReasoningEffort returns the normalized concrete reasoning.effort in params,
// or "" when it is absent.
func ReasoningEffort(params LLMRequestParams) string

// SetLLMProviderReasoningEffort updates only the exact provider+model entry's
// reasoning effort and promotes that complete entry to the default position.
func SetLLMProviderReasoningEffort(
    cfg *Root, agent, provider, model, effort string,
) error
```

- Preserve the current `withReasoningEffort` behavior for valid JSON.
- Empty effort is an explicit removal, not “leave the old value.”
- In `pkg/turn/models.go`, delete the duplicate JSON mutation. For a model that
  cannot reason, pass `""`; otherwise pass the chosen effort.
- Rewrite `ChooseReasoningEffort` to call the exact-entry helper. It must not
  read params from `PrimaryLLM` and then call `ApplyLLMUpdates`, because the live
  session choice and the shared file default can legitimately differ.
- Make `PromoteMatchingLLMProvider` normalize/expand the definition before its
  exact lookup, then change `ChooseModel` to return that lookup error directly.
  Remove its `ApplyLLMUpdates` fallback: a picker choice missing from the newly
  loaded persisted file is a concurrent-change error and must leave the file
  untouched.
- Make `turn.CurrentReasoningEffort` delegate to the config helper; Runner uses
  the same helper when it captures an entry's concrete effort.
- Add config unit tests: read, set, replace, clear while preserving sibling
  keys, empty input, and deep ownership (mutating returned bytes cannot mutate
  input). Add a two-entry mutation test with distinct credentials/params: update
  the non-primary exact entry, verify it is promoted intact, and verify the old
  primary is otherwise unchanged. Add `models: [...]` second-item promotion and
  stale/missing exact-pair no-write tests.

**Verify**: config and turn package tests pass.

### Step 2: Add durable, tenant-guarded session model state

Add after `fb_session_ui_state` in `pkg/state/schema.sql`:

```sql
-- One session's own effective primary-model choice. It is restored whenever
-- that session becomes active; updates are last-write-wins.
CREATE TABLE fb_session_model_state (
  session_id TEXT NOT NULL REFERENCES fb_sessions(id) ON DELETE CASCADE,
  provider TEXT NOT NULL,
  model TEXT NOT NULL,
  effort TEXT NOT NULL DEFAULT '',
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (session_id)
) STRICT, WITHOUT ROWID;
```

In `schema_migrations.go`:

- bump `stateSchemaVersion` from 2 to 3;
- append `{version: 3, apply: migrateStateV2ToV3}`;
- make `migrateStateV2ToV3` execute an idempotent
  `CREATE TABLE IF NOT EXISTS ...` with the same columns/constraints. This is
  required because v0/v1 rebuilding uses current `schemaSQL` before the v3 step;
- use `ExecContext` or correctly discard `execCopy`’s count. Do not write the
  old non-compiling `return execCopy(...)` form.

In `session_store.go`, add:

```go
type SessionModelSelection struct {
    Provider string
    Model    string
    Effort   string // concrete; empty means explicitly no effort
}

func (s *SessionStore) SaveSessionModelSelection(
    ctx context.Context, sessionID string, sel SessionModelSelection,
) (stored bool, err error)

func (s *SessionStore) SessionModelSelection(
    ctx context.Context, sessionID string,
) (SessionModelSelection, bool, error)

func (s *SessionStore) CopySessionModelSelection(
    ctx context.Context, sourceSessionID, targetSessionID string,
) (copied bool, err error)
```

Requirements:

- trim ids/provider/model; reject an empty provider or model instead of storing
  an unusable row;
- run `requireOwned` for by-id access and ensure SQL also scopes existing session
  rows to `s.AgentID()`;
- save uses UPSERT/last-write-wins and reports `stored=false` when the session row
  does not exist;
- copy checks ownership of both sessions — `requireOwned` for the source, and
  an agent-scoped existence check for the target, so a missing target is
  rejected by policy instead of surfacing as a raw foreign-key error — then
  performs one agent-guarded `INSERT ... SELECT ... ON CONFLICT DO UPDATE` so
  source existence and copied values are one SQLite statement; it returns
  `copied=false` when the source row disappeared or has no model row. SQLite
  parses `ON CONFLICT` after `INSERT ... SELECT` only when the SELECT ends
  with a WHERE clause: write `SELECT ... WHERE true` before the upsert clause
  or the statement is a syntax error. (`FreezeSessionPromptState` dodges the
  same ambiguity with `INSERT OR IGNORE`, which is not available here because
  a copy must overwrite the target's row.);
- read returns `(zero, false, nil)` only for no owned row;
- cascade deletion removes model state.

Tests in `pkg/state/session_model_state_test.go` plus migration tests must cover:

1. save/read/overwrite, including concrete empty effort;
2. missing-session save returns `stored=false`;
3. another primary agent cannot read, write or copy the row;
4. copy and `ON DELETE CASCADE`;
5. fresh schema at v3;
6. a handcrafted v2 file upgrades to v3 with old data intact;
7. existing v0 and premature-v1 fixtures upgrade without duplicate-table errors
   and still match fresh schema;
8. reopening v3 is idempotent.

**Verify**: state package tests pass, including all pre-existing migration tests.

### Step 3: Make Runner publish one effective primary-model snapshot

In `pkg/run/runner.go`, add an immutable internal snapshot held by one
`atomic.Pointer` with this shape:

```go
type primaryModelRuntime struct {
    agent    string
    provider appcfg.AgentLLMProviderConfig // deep-cloned, effective params
}
```

Add an `atomic.Bool` runtime-health flag. It starts healthy, becomes unhealthy
when candidate build **and** rollback build both fail, or when ordinary `Load`
fails after the active agent no longer matches the published snapshot. It
returns to healthy only after one of the four complete build operations
succeeds. Check it at `Run`/`RunContent` entry before using `r.main`; return a
stable typed/wrapped error that tells surfaces a reload is required.

Do not expose API keys/base URLs through the exported selection accessor. Add
this run-owned public value:

```go
type PrimaryModelState struct {
    Provider string
    Model    string
    Effort   string
    Set      bool
}
```

Resolution/build rules:

1. Deep-clone the provider snapshot with the existing
   `appcfg.CloneAgentLLMProviderConfig`; do not hand-copy only the fields used by
   today's clients.
2. First load or `ResetPrimaryModel`: resolve the config’s first provider and
   capture its current concrete effort.
3. Ordinary reload with a same-agent snapshot: exact-match provider+model,
   refresh the entry, and call `appcfg.WithReasoningEffort` with the pinned
   effort **even when it is empty**.
4. Ordinary reload with a missing exact pair: resolve the new default, log a
   credential-free warning, and continue. Do not reject the whole config.
5. Agent mismatch: resolve the new agent’s default; never attempt the old pair.
6. Explicit selection: exact provider+model only. An omitted effort derives that
   entry’s current effort once; an explicit effort pointer, including `""`, is
   overlaid exactly.
7. Rewrite the local in-memory `cfg.LLMChain[0]` to the effective provider before
   client construction/telemetry; do not mutate `AppCfg` or the persisted file.

Refactor `Load`/`loadLocked` so the load intent and candidate are explicit.
Regular `Load`, explicit selection, and default reset must all use the same build
path. Store the snapshot only after `loadLocked` returns success and while
`r.mu.load` is still held. On `SetPrimaryModel` or `ResetPrimaryModel` failure,
rebuild the previous snapshot before returning so fields assigned earlier in
`loadLocked` do not stay half-switched; return
an error that preserves both `original` and `rollback` if rollback also fails.
That joined/wrapped error must satisfy the classifier below; do not make callers
infer runtime health from the still-published snapshot.

Runner owns the config-pointer transaction too. `LoadConfig(next)` acquires
`r.mu.load`, captures the old config pointer/effective snapshot, assigns `next`
only under that lock, performs an ordinary pinned reload, and commits on
success. It rejects `nil` before mutation. On failure it restores the old
pointer and rebuilds the old effective
runtime before unlocking. It classifies a failed rebuild as runtime-uncertain
while preserving both errors. Refresh filesystem policy after unlocking only
for the runtime that ultimately won.

Locking rules that keep this deadlock-free (`r.mu.load` is a non-reentrant
`sync.RWMutex`, runner.go:193-204):

- `LoadConfig`, `SetPrimaryModel`, and `ResetPrimaryModel` must perform their
  build through the internal `loadLocked` path while already holding the lock.
  Calling the public `Load` from inside any of them self-deadlocks — the same
  hazard the `permRuntimeOnce` comment records at runner.go:163-169.
- Filesystem-policy refresh (`RefreshFilesystemPolicy`) takes `r.mu.load.RLock`
  itself, so it may run only after unlocking, the way `Load` does today
  (runner.go:345-357).
- Rewriting the local `cfg.LLMChain[0]` (rule 7 below) is safe by construction:
  `loadLocked`'s `cfg` comes from `LoadAgentConfigsFromRoot` →
  `llmYAMLsFromResolved` (config.go:278-295), which allocates fresh
  `*LLMProviderYAML` values with copied `Params` — it cannot mutate `AppCfg`.
  Do not add a defensive hand-copy.

These existing tests pin the reload/rollback contract and must stay green
(adapting their internals to the new API is expected, weakening what they
assert is not):

- `TestReloadConfigRollsBackEveryFieldWhenRunnerLoadFails`
  (pkg/process/config_reload_test.go:85-149) — pointer, provider entry, MCP
  list restoration and post-rollback usability;
- `TestReloadConfigLeavesMCPServersFrozen` (config_reload_test.go:208);
- `TestPrimaryModelExpandsTheModelsList` and
  `TestPrimaryModelFallsBackToMainAndNil` (pkg/run/runner_test.go:320-356) —
  the unloaded-Runner fallback this step must preserve.

In `pkg/process/config_reload.go`, replace direct `env.Runner.AppCfg = next` plus
`Load`/manual rollback with `env.Runner.LoadConfig(next)`. Publish `next` to
other environment holders only after success, and avoid a redundant write when
`env.Deps` is the Runner's own `Deps`. Tests must inject both an initial load
failure and a rollback failure and assert that neither is hidden. Step 6 applies
the same API to TUI's independent reload path.

Add:

```go
func (r *Runner) SetPrimaryModel(provider, model string, effort *string) error
func (r *Runner) ResetPrimaryModel() error
func (r *Runner) LoadConfig(next *appcfg.Root) error
func PrimaryModelSelection(r *Runner) PrimaryModelState
func IsPrimaryModelNotConfigured(err error) bool
func IsPrimaryModelRuntimeUncertain(err error) bool
```

`IsPrimaryModelNotConfigured` classifies only the explicit exact-pair miss used
by session activation. `IsPrimaryModelRuntimeUncertain` is true only when the
candidate build failed and rebuilding the previously published runtime also
failed. Do not branch on error strings.

No-op only when the effective exact selection (including effort) already matches
and no config refresh is requested. `Load` must still rebuild to refresh provider
details.

Preserve `PrimaryModel`’s current fallback for an unloaded test Runner: when no
snapshot has ever been published, resolve `AppCfg` index zero. Loaded runners
must read the snapshot.

**Verify**: runner package tests pass before changing surfaces.

### Step 4: Route every effective-primary reader/client through the snapshot

Update these sites in `pkg/run/runner.go`:

- `approvalHookModel`;
- automatic-compaction `PrimaryModel` and `ActiveModel` closures;
- automatic-compaction `CompactLLM`;
- telemetry’s local `cfg.LLMChain[0]` (confirm Step 3 rewrites it; no separate
  config-order read may remain);
- `ContextCompactLLM`;
- `PrimaryModel`, `PrimaryEndpoint`, and a new
  `PrimaryReasoningEffort` accessor;
- `runCompactHook` model metadata.

`ContextCompactLLM` must build a fresh client from the effective deep-cloned
provider snapshot, not call `NewLLMForAgentType` (which follows file order).
Leave previous-model compaction lookup and typed-subagent model lookup unchanged.

Search gate:

```bash
rg -n 'resolvedAgentProviders\(r\.AppCfg, r\.activeAgentNameForModel\(\)\)|NewLLMForAgentType\(r\.AppCfg, r\.activeAgentNameForModel\(\)\)' pkg/run/runner.go
```

Expected: no primary-current read remains. Any intentional historical/typed
lookup must be documented at its call site rather than blindly changed.

Runner tests must cover:

- first-load pin and `Models: [...]` expansion;
- config reorder preserves provider/model;
- config effort change preserves a pinned non-empty effort;
- config effort change also preserves a pinned **empty** effort;
- API key/base URL/non-effort params refresh for the same pin;
- removed pin falls back to new default on ordinary reload;
- explicit missing pair fails and leaves model/client/state on the previous pair;
- a forced load failure does not publish candidate state and restores prior
  load-owned state;
- a forced candidate+rollback double failure makes `RunContent` fail closed,
  and a later successful rebuild restores service;
- `/agent`-style `AgentName` change resets to that agent’s default;
- failed `/agent`-style rebuild leaves Runner fail-closed rather than serving the
  old agent/runtime under new tenant stores;
- explicit effort set and explicit empty effort;
- `PrimaryModel`, endpoint, effort, compaction client and hook metadata agree.

Use these stable test names so the race gate in Step 10 is executable:

- `TestLoadKeepsPinnedModelWhenConfigReordersProviders`;
- `TestLoadKeepsConcreteEffortAcrossReload` (subtests for empty and non-empty);
- `TestLoadRefreshesPinnedProviderDetails`;
- `TestLoadFallsBackWhenPinnedModelRemoved`;
- `TestSetPrimaryModelRollsBackOnLoadFailure`;
- `TestSetPrimaryModelConcurrentWithLoadConfig`;
- `TestLoadResetsPinWhenAgentChanges`.

**Verify**: runner tests and a targeted race run of the new tests pass.

### Step 5: Replace the slash reload seam with a live-selection seam

In `pkg/turn/model.go`:

```go
type ModelSlashHandler interface {
    ModelSettings(sessionID string) (ModelSettings, error)
    SelectModel(
        ctx context.Context,
        sessionID string,
        choice ModelChoice,
        effort *string, // nil derives this model's configured effort once
    ) error
    // ReloadModelCatalog synchronizes a successful default-file write into
    // this surface without selecting config index zero as the live model.
    ReloadModelCatalog(ctx context.Context, sessionID string) error
}

type ModelSelectionState struct {
    Provider string
    Model    string
    Effort   string
    Set      bool
}

type ModelSettings struct {
    Config     *appcfg.Root
    AgentName  string
    ConfigPath string
    Selection  ModelSelectionState
}

type ModelApplyOutcome uint8

const (
    ModelNotApplied ModelApplyOutcome = iota
    ModelAppliedNotDurable
    ModelRuntimeUncertain
)

// modelSelectionOutcome is what the classifier asserts on. Unexported on
// purpose: the exported surface is the concrete error below, and the package
// stays free to add other carriers later.
type modelSelectionOutcome interface {
    ModelApplyOutcome() ModelApplyOutcome
}

// ModelSelectionError reports a partial or uncertain selection outcome without
// making callers parse a surface-specific error message. Err carries the
// underlying cause and stays reachable through Unwrap.
type ModelSelectionError struct {
    Outcome ModelApplyOutcome
    Err     error
}

func (e *ModelSelectionError) Error() string
func (e *ModelSelectionError) Unwrap() error
func (e *ModelSelectionError) ModelApplyOutcome() ModelApplyOutcome

// NewModelSelectionError is the only constructor surfaces use; outcome must be
// ModelAppliedNotDurable or ModelRuntimeUncertain (a plain error already means
// ModelNotApplied).
func NewModelSelectionError(outcome ModelApplyOutcome, err error) error

func SelectionApplyOutcome(err error) ModelApplyOutcome
```

Implement `SelectionApplyOutcome` with `errors.As` against
`modelSelectionOutcome`; a nil error and ordinary errors mean `ModelNotApplied`.
This is a package-owned contract, so the executor does not depend on a
TUI-specific error type or parse message text. When a surface must report a
partial outcome it returns `NewModelSelectionError(outcome, cause)` — the
outcome constants themselves are not error values. A surface maps
`run.IsPrimaryModelRuntimeUncertain(err)` to `ModelRuntimeUncertain`.

Also extend `turn.SessionStore` (model.go:273-281) with the fork seam —
`copySlashSessionState` holds no store of its own:

```go
CopySessionModelSelection(ctx context.Context, source, target string) (copied bool, err error)
```

`*state.SessionStore` already satisfies it after Step 2; update turn's
recording fakes.

Remove `ReloadModelSettings` from the interface and both surface
implementations after `rg` confirms no caller remains. It is replaced, not
merely renamed: `SelectModel` changes live selection, whereas
`ReloadModelCatalog` refreshes config/provider details and relies on ordinary
Runner reload semantics to retain the effective pin.

This step changes an interface `pkg/tui` and `pkg/gateway` implement, so those
two packages stop compiling from here until Steps 6-8 give them the new
methods. That breakage is expected and is not a STOP condition: verify with
the per-package test runs each step directs, and run the first full
`go build ./...` at Step 10. Do not add stub implementations to make the tree
build early — the real implementations land in Steps 6-8, and a stub would
silently satisfy the seam this plan exists to fill.

In `pkg/turn/executor.go`:

1. Mark the current picker item by exact case-insensitive provider+model match
   against `settings.Selection`; only fall back to `i == 0` when `Set` is false.
2. Compare changes against the live selection, not list index.
3. Apply `SelectModel` **before** writing the file default.
4. After successful live apply, call `ChooseModel`. If that write fails, reply
   truthfully: current session switched, but the new-session default was not
   saved. Do not roll back the live session.
5. After a successful file save, call `ReloadModelCatalog`. If reload fails,
   report that the current session switched and the file default was saved, but
   this process could not refresh its catalog/default; do not misreport either
   completed outcome as failed. A reasoning-capable choice must still return its
   effort picker, driven by the live selection.
6. A model selection passes `effort=nil`, causing Runner to capture that entry’s
   configured effort. The effort picker’s current marker reads the returned
   concrete `Selection.Effort`.
7. An effort selection passes `&effort`, then calls `ChooseReasoningEffort` for
   the independent file default, followed by `ReloadModelCatalog`. Distinguish
   live-apply, file-save, and catalog-reload outcomes the same way. The file
   helper must update/promote the exact chosen entry even when a different model
   currently occupies index zero.
8. Interpret `SelectionApplyOutcome`: `ModelAppliedNotDurable` says the new live
   model remains active but its session row was not saved;
   `ModelRuntimeUncertain` says selection/rollback both failed, the Runner must
   not be trusted for another turn, and the user should reload/restart. Never
   claim a definite active model in the uncertain case.
9. After success, re-read `ModelSettings` only to refresh picker state; do not use
   config index zero as current truth.

In `copySlashSessionState`, after the target exists and before copying the row:

1. read `ModelSettings(sourceSessionID)` when `ctx.Model` is available;
2. if its live `Selection.Set` is true, call `SelectModel` for the **source**
   session with the exact provider/model and an explicit pointer to the concrete
   effort. This is a Runner no-op but makes the active source row durable;
3. call `ctx.Sessions.CopySessionModelSelection(ctx.commandContext(),
   sourceSessionID, targetSessionID)` — the new `turn.SessionStore` member from
   above — and require `copied=true` for TUI when a
   live selection was materialized. Gateway's non-persisting surface may return
   `copied=false`; a genuinely absent source selection remains success/no-op.

This path must not call `ChooseModel`, `ChooseReasoningEffort`, or
`ReloadModelCatalog`; forking never changes the shared default.

Update recording fakes and executor tests for ordering, current markers,
nil-versus-explicit effort, partial default-save failure, and fork copy.

**Verify**: turn tests pass.

### Step 6: Implement transactional TUI apply/persist and session activation

In `pkg/tui/chat_slash.go`:

- map `run.PrimaryModelSelection` into `turn.ModelSettings.Selection`;
- change `CurrentModelReasoningEffort` to `run.PrimaryReasoningEffort`;
- implement `ReloadModelCatalog(ctx, sessionID)` through the existing
  `reloadConfigFromDisk` path; the session id is validated/non-empty but does
  not select the Runner's model;
- implement `SelectModel(ctx, sessionID, choice, effort)`:
  1. ensure the session row (`Ensure(ctx, sid, sid)`) so a first-command model
     change is durable;
  2. capture the previous Runner selection;
  3. call `SetPrimaryModel`;
     if it returns a runtime-uncertain error, return
     `turn.NewModelSelectionError(turn.ModelRuntimeUncertain, err)`; any other
     error means not applied;
  4. save the newly published concrete selection and require `stored=true`;
  5. if save fails, restore the previous selection (or reset to default if none);
  6. if restoration succeeds, return a normal not-applied error. If restoration
     fails but the Runner ended up on the requested new runtime, return
     `turn.NewModelSelectionError(turn.ModelAppliedNotDurable, cause)`; if the
     Runner reports runtime uncertainty, return
     `turn.NewModelSelectionError(turn.ModelRuntimeUncertain, err)` and do not
     claim either model is healthy.

Add `ActivateSessionModel(ctx, sessionID) (warning string, err error)` to the
`Session` interface in `pkg/tui/notify.go` and implement it on `ChatSession`
through one private helper shared with `ResumeSession`. Activation must be
idempotent for a session whose stored selection already matches the live
Runner selection (the `SetPrimaryModel` no-op rule covers it), because startup
resume and a switch to the same session can both reach it. Note that
interactive `/resume` never calls `ResumeSession` — `handleResume`
(commands.go:42-52) only prompts and its callers switch directly — which is why
activation is centralized in `switchStreamSession`, with startup resume
activating through `ResumeSession`:

- capture the outgoing Runner selection before reading/applying target state;
- stored row: apply provider/model with an explicit pointer to stored effort;
- no row: `ResetPrimaryModel()`;
- stored exact pair no longer configured: reset to default, save the fallback
  concrete selection over the stale row, and return a non-fatal warning;
- on any fatal error after Runner changed, restore the captured outgoing
  selection (or reset if it was unset) before returning;
- any other DB/client error is fatal and leaves the outgoing session and its
  runtime active. If restoration itself fails, return a runtime-uncertain error;
  Runner's health gate prevents another model turn until recovery.

Change `ResumeSession` and the `Session` interface to return a warning separately
from fatal error as `(id, title, warning string, err error)`. It must call
`ActivateSessionModel` after ownership/existence validation. Update fakes and
callers — including the direct test call sites that take these signatures:
`ResumeSession` at `pkg/tui/chat_surface_test.go:740`, and `switchStreamSession`
at `pkg/tui/chat_session_test.go:14392`, `:14414`, `:14431` and
`pkg/tui/run_test.go:2778`. Do not encode a non-fatal warning as `error`,
because startup currently aborts on every error.

Also fix `ChatSession.applyConfigFromDisk`: pass the validated/effective config
to `Runner.LoadConfig` instead of assigning `deps.AppCfg` and calling `Load`.
Only adopt/publish the new pointer to the remaining session/environment holders
after Runner success; avoid reassigning the same shared `Deps.AppCfg` pointer.
Add characterization tests for successful rollback, visible rollback failure,
and concurrent `SelectModel` versus config reload; mirror the process-reload
assertions from Step 3.

In `pkg/tui/run.go`:

- on startup, construct the renderer, resolve/resume the initial session, then
  initialize footer, subagent models and token budget from the activated Runner;
  retain the returned warning and render it after startup chrome is ready;
- change `switchStreamSession` to accept `context.Context` and return
  `(switched bool, warning string, err error)`. It calls
  `state.session.ActivateSessionModel(ctx, next)` **before** persisting outgoing
  UI state, dropping queued input or mutating `state.sessionID`;
- return the warning/error to the caller; render a fallback warning only after a
  successful switch, and render a fatal activation error in the still-active
  outgoing session;
- return success/failure from `switchStreamSession` and update every call site so
  replay/title/MCP-watch changes happen only after successful activation;
- call `refreshSessionFooter` after every successful switch. This covers direct
  `/resume`, picker outcomes, `/new`, `/fork`, and `SessionSwitchedMsg` paths.

Make switch replies atomic with the switch:

- in `pkg/tui/chat_surface.go`, do not emit the standalone `NewMessageMsg` for a
  handled result where both `SessionChanged` and `SessionSwitched` are true;
  carry its reply only in `SessionSwitchedMsg`;
- `pkg/tui/reducer.go`'s `SessionSwitchedMsg` case (1029-1037) is what turns
  that message into `EventResult{Frames, SwitchSessionID}`: keep the reply
  flowing through the message's `Reason`/`sessionSummary(m)` so exactly one
  frame carries it, with no duplicate for the suppressed `NewMessageMsg`;
- in the main notification loop, if a reducer result (`EventResult`) has
  `SwitchSessionID`, call `switchStreamSession` **before** rendering that
  result's success frames (today run.go:365-367 switches only after they have
  rendered). On success, render them in the target session; on failure, discard
  those frames and render a corrective error in the source session;
- apply the same success-after-switch rule in `applySlashOutcome` and direct
  resume picker paths;
- do not delete a `/new` or `/fork` target on activation failure. Report that it
  was created but not entered and can be resumed after configuration is fixed.

Tests must prove:

- startup resume restores selection and footer effort;
- interactive resume A→B→A restores each stored selection;
- `/new` resets to the latest config default;
- `/fork` inherits the source's live selection even when the source had no model
  row before the fork;
- unavailable stored model falls back, rewrites the row, warns, and continues;
- DB failure aborts the switch without discarding outgoing queued input;
- fallback-row save failure restores the outgoing model; forced restore failure
  leaves Runner fail-closed;
- fatal activation never renders “started/switched” success and leaves the
  created target recoverable;
- using `/model` as the first command creates/persists a usable session row;
- persistence failure rolls the live Runner back; forced rollback failure yields
  the precise applied-not-durable or runtime-uncertain response.

Name the principal tests:

- `TestInteractiveResumeRestoresSessionModelSelection`;
- `TestNewSessionResetsModelToConfigDefault`;
- `TestForkCopiesSessionModelSelection`;
- `TestResumeMissingStoredModelFallsBackWithWarning`;
- `TestModelPersistenceFailureRollsBackRunner`;
- `TestModelChoiceDoesNotLeakToSecondProcess` is only *named* here: it is
  written against Step 9's two-Environment fixture, so implement it there.
  Step 6 owns the single-process cases above it.

**Verify**: TUI tests pass, including
`TestCharacterizationModelSwitchAppliesWithinSession` and
`TestCharacterizationPrefixSurvivesConfigReload`.

### Step 7: Make `/connect` select the active TUI session explicitly

Change `handleConnect` to accept `sessionID`; pass `state.sessionID` from
`pkg/tui/run.go`.

Keep this order:

1. `process.Execute` saves provider configuration/default;
2. the existing `c.session.ReloadConfig()` call (Session interface,
   notify.go:1262) refreshes live catalog/provider details while keeping the old
   valid pin (or applying the documented removed-pin fallback) — after Step 6 it
   routes through the fixed `applyConfigFromDisk` → `Runner.LoadConfig`, so no
   new seam is needed here;
3. `SelectModel(ctx, sessionID, rep.Provider/rep.Model, nil)` applies and persists
   the newly connected model for this session;
4. render success and refresh footer.

If step 3 fails after step 1, say that the config/default was saved but the
active session could not adopt it. Distinguish applied-not-durable from
runtime-uncertain using the turn outcome contract; in the latter case require a
reload/restart and do not name a healthy active model. Do not use undefined
`ctx_sessionless` or infer a session id from the runner.

Tests: connected model is live and persisted; a second process keeps its model
and concrete effort after reloading the file; failure messages distinguish
saved-default, live-apply and persistence outcomes.

**Verify**: TUI tests pass.

### Step 8: Implement the gateway’s documented runner-level boundary

In `pkg/gateway/slash_handlers.go`:

- `ModelSettings(sessionID)` maps the selection from the exact
  `runnerFor(context.Background(), sessionID)`; the picker catalog still comes
  from `s.liveCfg()`, so on a project-pool runner whose `AppCfg` predates a
  reload the catalog can list models the snapshot-backed marker does not
  report as current. That disagreement is accepted and documented here — the
  marker (the runner snapshot) is the truth about what runs; pool-wide reload
  propagation remains the recorded follow-up, not something to fix in this
  plan;
- `SelectModel(ctx, sessionID, choice, effort)` resolves that same runner and
  calls `SetPrimaryModel`; map a Runner runtime-uncertain error to the turn
  outcome contract even though gateway has no DB-persistence failure;
- `ReloadModelCatalog(ctx, sessionID)` calls `Environment.ReloadConfig` so the
  shared catalog/default and base Runner update synchronously while retaining
  their pins. Do not claim this refreshes already-created project-pool Runner
  config pointers;
- do not persist `fb_session_model_state` in gateway;
- remove `ReloadModelSettings`.

Tests must cover base runner, project runner, nil runner, current marker/effort,
and two sessions sharing one runner (last selection wins, documenting the
follow-up boundary). Also prove sessions on different project runners do not
share the selection, that catalog reload retains the base Runner pin, and one
case documenting the `liveCfg`-catalog versus snapshot-marker disagreement on
a stale pooled runner.

**Verify**: gateway tests pass.

### Step 9: Cross-process and regression matrix

Add integration-style tests with two independently loaded Environments/Runners
sharing one home/config. **Load both before process A writes the file**, or B has
no pin and the test does not reproduce the bug. Build the fixture the way
`TestReloadConfigRollsBackEveryFieldWhenRunnerLoadFails` does
(config_reload_test.go:104-115): construct each `run.Runner` directly with its
own `run.Deps` over one shared `home`/`cfgPath`, `Load` both, and wrap each in
its own `Environment` — no second OS process is needed.

Required cases:

1. A switches m1→m2; B reloads reordered config and stays on m1.
2. A changes effort empty/medium→high on the same provider/model; B reloads and
   keeps its concrete previous effort, including the empty-effort case.
3. A’s switch is immediately active and persisted for A’s session.
4. A new session adopts the shared file default m2/high.
5. Resuming A’s old session restores its stored selection even after the file
   default changes again.
6. Provider credentials/base URL changed without removing B’s pair refresh in B.
7. Removing B’s pair causes documented fallback rather than reload rollback.
8. Switching primary agent on a loaded Runner does not reuse the old agent pin.

Use distinct session ids in the two TUI fixtures. Do not reuse the existing
`chooseInSession` helper if it hardcodes `"s1"`; parameterize it or add a helper
that accepts a session id.

**Verify**: targeted config/state/run/turn/TUI/gateway tests pass together.

### Step 10: Full verification and scope proof

Run, in order:

1. `go vet ./...`
2. `CGO_ENABLED=1 go build -tags fts5 ./...`
3. `CGO_ENABLED=1 go test -tags fts5 ./... -count=1 -timeout 20m`
4. `CGO_ENABLED=1 go test -race -tags fts5 ./pkg/run ./pkg/tui -run 'Test(LoadKeepsPinnedModelWhenConfigReordersProviders|LoadKeepsConcreteEffortAcrossReload|SetPrimaryModelRollsBackOnLoadFailure|SetPrimaryModelConcurrentWithLoadConfig|InteractiveResumeRestoresSessionModelSelection|ModelPersistenceFailureRollsBackRunner|ModelChoiceDoesNotLeakToSecondProcess)$' -count=1`
5. `scripts/package-graph.sh`
6. `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1`
7. compare saved and regenerated graph `.edges` arrays — identical; retain the
   regenerated file because LOC changes are expected;
8. compare the before-edit checksum manifest and account for every modified file
   against Scope.

## Test plan summary

- **Config**: exact effort overlay, clear semantics, deep ownership.
- **State**: roundtrip/overwrite/copy/cascade/tenant guards; fresh/v0/v1/v2/v3
  schema paths.
- **Runner**: concrete pin, refreshable details, reorder isolation, missing-model
  fallback, explicit failure rollback, agent reset, all effective readers.
- **Turn**: selection-based current markers, live-before-default ordering,
  synchronous catalog refresh, truthful three-stage partial replies, effort
  pointer semantics, fork copy.
- **TUI**: first-command persistence, startup and interactive resume, new, fork,
  connect, footer refresh, activation warning/failure atomicity.
- **Gateway**: exact runner routing and explicitly documented shared-runner scope.
- **Cross-process**: model and effort do not leak; provider details still refresh.
- **Regression**: existing immediate-switch and prompt-prefix characterization,
  config reload adoption, package architecture, full CI test shape.

## Done criteria

- [ ] Reordering the config in process A cannot change process B’s pinned
      provider/model/effort when B reloads.
- [ ] Empty effort is pinned concretely and cannot inherit a later file effort.
- [ ] API key/base URL/API path/non-effort params refresh for an exact pin.
- [ ] Ordinary reload falls back only when the pin disappeared; explicit
      selection never silently substitutes.
- [ ] Failed explicit switch does not publish candidate identity and restores
      the previous load-owned runtime.
- [ ] Failed process/TUI config reload rebuilds the previous runtime; a failed
      rollback is joined into the returned error rather than discarded.
- [ ] Process/TUI reload paths use Runner `LoadConfig`; no config pointer is
      replaced outside the Runner load lock before a build.
- [ ] Rollback failure is machine-classified as runtime-uncertain; no user reply
      claims that either model is healthy in that state.
- [ ] Startup resume and every interactive TUI session switch activate the
      target session’s selection before UI state changes.
- [ ] A `/new`, `/fork` or `/resume` success frame never renders before its
      session switch succeeded (reducer frames are held until
      `switchStreamSession` returns success).
- [ ] `/new` uses current file default; `/fork` inherits source selection.
- [ ] A successful `/model` default-file write refreshes the in-memory catalog
      synchronously, so an immediate `/new` does not observe stale order.
- [ ] `/agent` uses the new agent’s default rather than the previous pin.
- [ ] TUI state APIs enforce primary-agent ownership and all migration paths pass.
- [ ] `/model` and `/connect` replies distinguish live success, persistence
      failure and default-file failure truthfully.
- [ ] Gateway behavior is tested and documented as runner-level, not falsely
      claimed as per-web-session isolation.
- [ ] `rg -n "ReloadModelSettings" pkg` returns no production references.
- [ ] Vet, fts5 build, full tests, targeted race tests and architecture tests pass.
- [ ] Package graph edges are unchanged; regenerated LOC is retained.
- [ ] Every changed file is in Scope; no commit/push was made implicitly.

## STOP conditions

Stop and report instead of improvising if:

- any cited symbol/path no longer matches current code;
- exact provider+model resolution cannot represent a `/model` choice currently
  offered by `ModelChoices`;
- keeping concrete effort requires freezing all provider params rather than
  overlaying only `reasoning.effort`;
- Runner rollback cannot restore load-owned state without changing the existing
  MCP generation/publication contract;
- a TUI session-switch path cannot be made to activate before queued input is
  discarded;
- the schema migration needs destructive table rebuilding or loses existing
  data;
- implementation requires changing watcher semantics, prompt-shaping wrapper
  output, gateway request-context routing, or another out-of-scope file;
- an existing test encodes a product rule that directly contradicts the Fixed
  semantics above (report the test and contradiction before changing it);
- package graph edges change unexpectedly.

## Maintenance notes

- The next independent design task is true per-web-session model routing for
  gateway shared runners. Prefer a request/session context selector with a
  per-run stable client snapshot; do not reuse this runner-level mutable pin as
  if it were already per-session.
- Gateway project-pool runners currently retain the `AppCfg` pointer from their
  construction. A separate process/RunnerPool design must propagate valid
  config reloads transactionally to every live pool entry if provider-detail
  refresh is required there; this plan deliberately does not imply that
  `Environment.ReloadConfig` already does so.
- A future provider-settings UI that removes entries must preserve the explicit
  removed-pin fallback/warning contract.
- New primary-model consumers must use Runner’s effective snapshot accessors.
  Reading `AppCfg` index zero is correct only for defaults or unloaded fixtures.
- Session model rows contain identity and effort only — never credentials,
  base URLs, or full provider params. Those always refresh from config.
- Shared default-file updates remain last-writer-wins by design. Do not add a
  file lock as part of this plan.
