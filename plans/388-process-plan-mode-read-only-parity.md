# Plan 388: `process start` obeys the same plan-mode read-only wall as `shell`

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: `git diff --stat 9a3ae0c2 -- pkg/tool/process_tool.go pkg/tool/shell_tool.go pkg/run/permissions.go pkg/run/config.go`
> and `git status --porcelain -- pkg/tool pkg/run`. **This plan depends on
> plans/387 being landed** (it calls `ShellScratchRoots`,
> `ShellWritesConfinedToRoots` and the post-387 `planModeShellGate(ctx, danger,
> mutation, cmd, cwd)` signature). If 387 is not in the tree, STOP and report
> the dependency — do not reimplement 387 here. On any content mismatch in the
> excerpts below, STOP (moved line numbers alone are not drift; re-locate by
> the quoted symbols/text).

## Status

- **Priority**: P1 (owner mandate 2026-11-26: 「process start 用 ProfileForConfig、无 plan 模式只读降级 必须立计划」)
- **Effort**: L
- **Risk**: MED (shared shell/process seam extraction + an approval path)
- **Depends on**: plans/387-plan-mode-scratch-writes-free.md (must be landed first)
- **Category**: bug (plan-mode containment hole)
- **Planned at**: commit `9a3ae0c2` (dev), 2026-11-26
- **Landed**：2026-11-26 owner 指示「合并回主树dev分支」→ 与 387 同一提交 `bfc3ac17`
  ff-only 落 dev（`9a3ae0c2..bfc3ac17`），未 push；落 dev 门禁复跑绿（gofmt 空、vet exit 0、
  `go build -tags fts5 ./cmd/forebrain` ok、四包全量 ok、`pkg/architecture` 仅预存红）。
- **Implementation**: **DONE, review APPROVE（2026-11-26, round 1）** — 与 387 同 worktree
  `.worktrees/plan-mode-scratch-writes`（分支同名）在其**未提交**改动之上追加，同样未提交；
  执行器 `alibaba/deepseek-v4.1-flash-1017`；未 push、未合并。
  - **派发前的独立复现（评审自己做）**：`go test -overlay` 探针在本 worktree 的 387 树上实测
    `process start "echo probe > <projectDir>/probe388.txt"` = `err=<nil>`、`file_created=true`
    （同一命令的 shell 门＝Refuse），`process start "python3 <projectDir>/server.py"` = `err=<nil>`
    （shell 门＝Ask）—— 计划的两条红为真。
  - **修复后评审复跑**：`err=plan mode: "…" modifies files; call exit_plan_mode first`、
    `file_created=false`；agent 模式同命令照常运行（不过度收紧）；`./pkg/tool` 全量 ok；
    `TestProcessStartInPlanMode{RefusesProjectWrites,AllowsScratch,AsksForUnproven,IsReadOnlyUnderTheSandbox}`
    +`TestProcessStartAgentModeUnaffected` 逐个 PASS（Ask 用例装 ActionHook，断言
    `approval_reason=plan_mode_unproven_command` 且命令未在回答前运行）；gofmt 空、vet exit 0、
    四包全量绿、`pkg/architecture` 仅上面两条已知红；`grep ProfileForConfig(cfg) pkg/tool/process_tool.go`
    无命中、`grep '"process"' pkg/run/permissions.go` 命中 defer 条件。
  - **已接受的两处偏差**：①`readOnlyExtraWriteRoots(ctx, st, snap, cwd)` 去掉计划里的 `rt` 形参
    （函数体无用途）；②permissions.go 的 defer **不是**裸 `name=="process"`：`permissionInput` 对
    process 返回整份 payload，裸写法会让 `log/wait/kill/list` 在 plan 模式下也跳过边界自身的常规审批，
    现改为只对**带 command 的** process 调用走门（其余保持原流程）。
  - **已知差异（未在计划内，报告）**：批准回放时 shell 会升到 workspace-write（`profileElevation`），
    process 仍按 plan 模式的 read-only profile 执行，故 sandboxed 配置下「批准后的项目写」会被 OS 拒
    （假阴性，非越权）；YOLO/danger-full-access 无沙箱，manifest 表现一致。`processStart` 仍忽略
    `BatchBoundaryFromContext`（计划已声明延后，另立计划）。

## Why this matters

`process start` runs a background command with `Profile:
safety.ProfileForConfig(cfg)` — the *config-level* profile — and never applies
the per-call read-only downgrade the shell tool applies in plan mode. Its own
comment claims the opposite ("The command is sandboxed exactly like a shell
command… A background server is not a reason to weaken containment",
`process_tool.go:354-356`), but in plan mode `shell "echo x > pkg/tool/f"` is
refused/blocked while `process start "echo x > pkg/tool/f"` writes the file.
The same hole lets a read-only typed worker (explore/plan/verification) edit
the project through `process`. Under danger-full-access (the owner's YOLO
environment) there is no sandbox at all, so `process start` is an unguarded
project-write path in plan mode. This plan gives `process` the **same**
semantics as `shell` after 387: project read-only, OS-temp scratch free,
proven project writes refused, unproven commands asked — by reusing the
shell's machinery, not by adding a parallel one.

## Current state (verified against `9a3ae0c2`)

### The hole

`pkg/tool/process_tool.go` `processStart` (~:335-453):
```go
// The command is sandboxed exactly like a shell command: same config, same
// manager, same request. A background server is not a reason to weaken
// containment.
cfg := sessionConfig(ctx, st, rt)
…
req := safety.CommandRequest{
    ToolKind:       safety.ToolKindShell,
    Command:        command,
    WorkDir:        workDir,
    Profile:        safety.ProfileForConfig(cfg),   // ← config profile, no per-call downgrade
    AllowNetwork:   runtimeCfg.NetworkEnabled,
    AllowLoopback:  runtimeCfg.NetworkLoopback,
    Env:            env,
    OnOutput:       onOutput,
    OutputSpoolDir: dir,
}
handle, err := manager.StartCommand(ctx, cfg, req)
```
There is **no** `ModeFromContext` check, no `shellSandboxProfile`, no plan
gate, no assessment approval anywhere in `process_tool.go` (verified by grep).
`processLog`/`processKill`/`processWait`/`processList` execute no command and
are unaffected.

### The shell machinery to reuse (all in `pkg/tool`, same package)

- Per-call profile selection — `shell_tool.go:121-125`:
  ```go
  cfg := sessionConfig(ctx, st, rt)
  profile, profileElevation := shellSandboxProfile(ctx)
  if profile == safety.ProfileWorkspaceWrite && !profileElevation && cfg != nil {
      profile = safety.ProfileForConfig(cfg)
  }
  ```
  `shellSandboxProfile` (~:1743-1780): plan mode OR a read-only typed worker
  (explore/plan/plan-reviewer/goal-evaluator/verification, or a custom policy
  blocking `write_file`) ⇒ `ProfileReadOnly`; an approved-action replay ⇒
  `ProfileWorkspaceWrite, true`; otherwise `ProfileWorkspaceWrite, false`
  (then the config profile wins via the snippet above).
- Read-only write exceptions — `runSandboxedShellCommand` read-only branch
  (~:357-371): `writablePaths = nil` + structured write grants
  (`snap.FileSystemGrants`, `grant.Entry.Path.Resolve(cwd)`) +
  `ReviewWriteScopeFromContext(ctx).shellWritableRoots()` + (after 387)
  `safety.ShellScratchRoots()` in plan mode. These become `extraBinds`, then
  `allowRead = promoteReadOnlyWriteScope(&req, extraBinds, allowRead)` (~:477).
- `promoteReadOnlyWriteScope` (~:1793-1807): read-only + exceptions ⇒
  `ProfileManaged` + `exclusiveWritablePaths = true`; returns the readable
  paths to re-state. Pure function on `sandboxedShellRequest`.
- `CommandRequest` carries the same shape (`pkg/safety/sandbox_config.go:127-144`):
  `Profile`, `AdditionalWritablePaths`, `ExclusiveWritablePaths` ("this
  command's complete write set… a plan-mode command granted its build cache
  would also gain the ability to edit the project it is planning" — the
  comment names this exact use).
- Go caches — `shellProfilePlumbsGoCache(profile, sandboxApplied,
  writableRoots, cwd)` (~:1727) gates `resolveGoCacheAccess(ctx, rt, cwd)`
  (~:2009-2047) returning `goCacheAccess{Writable, Readable, Env}`; shell
  merges `Writable` into the write set, `Readable` into the read set, `Env`
  via `shellAttemptEnv(base, cache, sandboxed)` (~:1709). The shell merge site
  is ~:472-490 — read it as the exemplar.
- Plan gate (post-387) — `planModeShellGate(ctx, dangerFullAccess, mutation,
  cmd, cwd)`: ReadOnly/scratch-confined ⇒ Allow; proven project write ⇒
  Refuse; unproven ⇒ Ask; approved replay ⇒ Allow. Shell's refusal text
  (~:187): `plan mode: %q modifies files; call exit_plan_mode first`. Shell's
  Ask handling (~:209-246): `markPlanModeApproval(payload)` +
  `st.ActionHook()(ctx, "shell", payload)` ⇒ `RequiresActionError`.
  `markPlanModeApproval` (`permissions.go:178-198`) sets
  `approval_reason = "plan_mode_unproven_command"` and the justification
  sentence.
- Classification roots — `shellAllowedRoots(ctx, st, rt)` (~:1895, post-387
  includes scratch in plan mode); `safety.ClassifyShellCommand(cmd, roots, cwd)`.

### The approval boundary

`pkg/run/permissions.go:104-118`: `toolOwnsApprovalProtocol` (~:438-455)
returns false for `process`, so the generic middleware raises process
approvals via `actionHook(ctx, "process", payload)` — **process approval cards
already render on every surface**. The plan-mode defer special-case at :115
currently names only `shell`:
```go
if strings.EqualFold(name, "shell") &&
    tool.PlanModeShellGateForCommand(ctx, r.sessionConfig(ctx).DangerFullAccessEnabled(), permissionInput(name, payload)) != tool.PlanModeShellAllow {
    return next(ctx, arguments)
}
```
It must also name `process`, or the boundary asks ahead of the tool's gate.

### Test harness (exists, danger-full-access)

`pkg/tool/process_tool_test.go:26-50`: `newProcessToolForTest(t)` builds
`rt.Cfg = &appcfg.Root{SandboxMode: appcfg.SandboxModeDangerFullAccess}`,
`st := NewState(projectDir)`, returns `(st, tl, rt, projectDir)`;
`processTestContext(projectDir)`; `callProcess(t, tl, ctx, in)`. Comment at
:24-25: "danger-full-access so these tests pin the tool, not the sandbox — the
sandboxed start is pinned in pkg/safety's handle tests." 28 tests exist.
Plan-mode ctx: `WithMode(processTestContext(dir), "plan")` (the shell gate
tests use `planModeShellCtx()` / `WithMode`, `shell_output_test.go:404-405`).

### Reproduction (real tool run, danger-full-access + plan mode)

With the harness above, `callProcess(t, tl, WithMode(ctx,"plan"),
ProcessInput{Action:"start", Command:"echo probe > "+filepath.Join(projectDir,"probe.txt")})`
**succeeds today and creates `probe.txt`** — no gate, config profile is
advisory under danger-full-access. The identical `shell` call is refused with
`plan mode: … modifies files; call exit_plan_mode first`. That asymmetry is
the defect; the executor pins it as a failing test in Step 1.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Env (cgo/pdf) | `export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"` | — |
| Focused tests | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/run ./pkg/safety -count=1` | 3× ok |
| Single test | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run TestProcess -count=1 -v` | named tests PASS |
| Format | `gofmt -l pkg/tool pkg/run pkg/safety` | prints nothing |
| Vet | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/tool ./pkg/run ./pkg/safety` | exit 0 |
| Layering | `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` | only the pre-existing `TestTestFilesCorrespondToProductionFiles` red (baseline `80bb73b4`) — do NOT fix |
| Real run | the project's `run-forebrain` skill | Step 7 |

## Scope

**In scope**:
- `pkg/tool/process_tool.go` — profile selection, write scope, go caches, plan gate + Ask approval in `processStart`.
- `pkg/tool/shell_tool.go` — extract two shared helpers (behavior-preserving); shell call sites use them.
- `pkg/tool/process_tool_test.go` — reproduction + parity cases.
- `pkg/tool/shell_tool_test.go` / `shell_output_test.go` — only if an extracted helper needs a direct unit test.
- `pkg/run/permissions.go` — the :115 defer condition names `process` too.
- `pkg/run/config.go` + `pkg/run/config_test.go` — one reminder clause (Step 5).
- `plans/README.md` — append one index row only.

**Out of scope** (do NOT touch):
- `pkg/tool/review_scope.go`, the mode-inheritance condition
  (`pkg/run/subagent.go:2038-2046`), `TypedSubagentBlocksFileWrites` — the
  reviewer contract is unchanged (387 §Relationship verified no interference).
- The batch-task boundary: `processStart` also ignores
  `BatchBoundaryFromContext` (a batch task can escape its workspace through
  `process`). Real, adjacent, but a different contract — report in one line,
  separate plan. Do not fix it here.
- `pkg/safety` internals (387 owns `ShellScratchRoots`/proof/go-tier); no new
  cross-layer import; fan-out budgets unchanged (`process_tool.go` already
  lives in `pkg/tool` beside the helpers).
- `pkg/tui`, `pkg/gateway`, `frontend/` — process approval cards already
  render via the generic middleware; no surface change.
- `processLog`/`processKill`/`processWait`/`processList` — no command runs there.

## Git workflow

Owner standing rule: no commits unless instructed. Work in
`.worktrees/process-plan-mode-parity` (branch of the same name from HEAD);
leave changes uncommitted for review. `plans/` lives in the main checkout;
append-only on `plans/README.md` (parallel agents own the other rows).

## Steps

### Step 1: Pin the defect with failing tests

In `pkg/tool/process_tool_test.go`, model on `newProcessToolForTest` /
`callProcess`:
1. `TestProcessStartInPlanModeRefusesProjectWrites`: danger-full-access
   harness, `WithMode(ctx,"plan")`, `start` with
   `echo probe > <projectDir>/probe.txt` ⇒ want error containing
   `exit_plan_mode`, and `probe.txt` must NOT exist. (Today: succeeds, file
   exists — the reproduction.)
2. `TestProcessStartInPlanModeAllowsScratch`: `start` with
   `mkdir <os.TempDir()>/fb-388-<rand>` (or `echo hi > <temp>/f`) ⇒ succeeds
   (post-387 scratch proof). Regression guard, not red.
3. `TestProcessStartInPlanModeAsksForUnproven`: `start` with
   `python3 <projectDir>/server.py` ⇒ `RequiresActionError` with
   `approval_reason == "plan_mode_unproven_command"` (mirror
   `TestShellToolPlanModeGateAsksOnlyForUnprovenCommands`,
   `shell_output_test.go:445`). Today: runs unasked — red.
4. `TestProcessStartAgentModeUnaffected`: agent-mode ctx, project write under
   danger-full-access ⇒ runs (no gate). Guards against over-restriction.

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run 'TestProcessStartInPlanMode|TestProcessStartAgentMode' -count=1 -v`
→ cases 1 and 3 FAIL (file created / no approval), 2 and 4 PASS. Record output.

### Step 2: Extract two shared helpers (behavior-preserving)

In `pkg/tool/shell_tool.go`:
1. `func callSandboxProfile(ctx context.Context, cfg *appcfg.Root) safety.Profile`
   — exactly the :121-125 selection (shellSandboxProfile + config fallback).
   Replace the shell call site with it.
2. `func readOnlyExtraWriteRoots(ctx context.Context, st *State, rt *AgentToolRuntime, snap safety.Snapshot, cwd string) []string`
   — the read-only branch's `writablePaths` resolution: structured write
   grants + `ReviewWriteScopeFromContext(ctx).shellWritableRoots()` + (plan
   mode) `safety.ShellScratchRoots()`. Replace the inline resolution in the
   read-only branch with a call; keep the surrounding `extraBinds` loop and
   `promoteReadOnlyWriteScope` call exactly as they are.

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -count=1` → ok, all
28 process tests and every shell test unchanged (the extraction must move no
behavior). `gofmt -l pkg/tool` empty.

### Step 3: `processStart` profile + write scope + go caches

In `processStart`, replace `Profile: safety.ProfileForConfig(cfg)` with the
shell-equivalent resolution:
```go
profile := callSandboxProfile(ctx, cfg)
req := sandboxedShellRequest{profile: profile, cwd: workDir}
var extraBinds, allowRead []string
if profile == safety.ProfileReadOnly {
    extraBinds = readOnlyExtraWriteRoots(ctx, st, rt, snap, workDir)
    allowRead = promoteReadOnlyWriteScope(&req, extraBinds, nil)
}
```
Then plumb go caches exactly as shell (~:472-490): `contained :=
manager.DecideShellCommand(cfg, command, safety.SandboxPermissionsUseDefault,
req.profile, false).UseSandbox`; if `shellProfilePlumbsGoCache(req.profile,
contained, runtimeCfg.Filesystem.AllowWrite, workDir)` ⇒
`goCache := resolveGoCacheAccess(ctx, rt, workDir)`; append `goCache.Writable`
to `req`'s write set (re-promote if read-only), `goCache.Readable` to
`allowRead`. Build the `CommandRequest` with `Profile: req.profile`,
`AdditionalWritablePaths: <extraBinds+goCache.Writable>`,
`ExclusiveWritablePaths: req.exclusiveWritablePaths`,
`AdditionalReadablePaths: allowRead`, `Env: shellAttemptEnv(env, goCache.Env,
contained)` (keep `planModeShellEnv` + `agentRuntimeEnv` in `env`). Update the
:false comment at :354-356 to state the per-call downgrade truthfully.

**Verify**: add a sandboxed-config case (workspace-write) asserting a
plan-mode `process start` project write is denied by the OS sandbox (model on
pkg/safety handle tests if a real-sandbox assertion is needed; otherwise pin
`req.profile == ProfileReadOnly` + `exclusiveWritablePaths` via a unit test on
the built request). `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/safety -count=1`
→ ok; agent-mode process behavior unchanged.

### Step 4: Plan gate parity under danger-full-access

In `processStart`, before `manager.StartCommand`:
```go
mutation := safety.ClassifyShellCommand(command, shellAllowedRoots(ctx, st, rt), workDir)
switch planModeShellGate(ctx, cfg != nil && cfg.DangerFullAccessEnabled(), mutation, command, workDir) {
case PlanModeShellRefuse:
    registry.abandon(id)
    return "", fmt.Errorf("plan mode: %q modifies files; call exit_plan_mode first", strings.TrimSpace(command))
case PlanModeShellAsk:
    // mirror shell ~:209-246: markPlanModeApproval(payload) + st.ActionHook()(ctx,"process",payload)
    // payload carries command/work_dir/sandbox_profile; on ok&&id!="" return &RequiresActionError{...ToolName:"process"...}
}
```
Reserve the registry id AFTER the gate (or abandon on refusal, as shown) so a
refused start leaves no ledger/log residue. In `pkg/run/permissions.go:115`,
extend the defer condition to `strings.EqualFold(name, "shell") ||
strings.EqualFold(name, "process")`.

**Verify**: Step-1 cases 1 and 3 now PASS; 2 and 4 still PASS;
`CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/run -count=1` → ok. The
existing shell gate tests are unchanged.

### Step 5: Reminder clause (model guidance)

In `pkg/run/config.go` coding full reminder tool guidance (the block 387
rewrote), add one line: `- process start: same plan-mode rules as shell
(project read-only; OS temp scratch free)`. Update the matching exact-text
assertion in `pkg/run/config_test.go`. Office variants: no change (office
planning rarely starts servers; the runtime rule still applies).

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -count=1` → ok.

### Step 6: Proportionate broader checks

- `gofmt -l pkg/tool pkg/run pkg/safety` → empty.
- `CGO_ENABLED=1 go vet -tags fts5 ./pkg/tool ./pkg/run ./pkg/safety` → exit 0.
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/run ./pkg/safety -count=1` → 3× ok.
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` → no NEW failure vs baseline.
- Optional: full scratch-home suite `-p 2`, same expectation.

### Step 7: Real-run acceptance (run-forebrain skill)

Plan mode + danger-full-access session via the fake-provider TUI harness:
1. `process start "echo probe > pkg/tool/plan-mode-probe.txt"` → refused with
   the `exit_plan_mode` sentence; file absent.
2. `process start` of a scratch writer (e.g. `sh -c 'echo hi > /tmp/fb-388.log'`)
   → runs; `/tmp/fb-388.log` exists.
3. `process start "python3 server.py"` (unproven) → approval card renders
   (process approval already supported by the surfaces); approve ⇒ runs.
4. Agent mode: a normal project-writing `process start` still runs.
If the skill fails on unrelated mechanics, STOP and report it stale.

**Verify**: the four outcomes on the rendered screen (capture paths in report).

### Step 8: Index and status

Append one row after the 387 row in `plans/README.md` (append-only):
`| [388-process-plan-mode-read-only-parity](388-process-plan-mode-read-only-parity.md) | process start 与 shell 同享 plan 模式只读墙（per-call profile 降级＋scratch 写例外＋go cache＋danger 门拒/问，复用 shell 机制） | Bug | TODO | P1 | L | MED |`
Set this plan's Status to DONE with evidence when Steps 1-7 pass.

## Test plan

- New: the four Step-1 cases (reproduction, scratch regression, unproven ask,
  agent-mode guard) + a Step-3 sandboxed/profile assertion. Pattern:
  `process_tool_test.go` harness + `shell_output_test.go:445` ask assertion.
- The 28 existing process tests and all shell tests must pass **unmodified**
  through Step 2 (extraction proof) and after Step 4.
- Failure path: refused start leaves no registry/ledger residue (assert
  `processList` empty after a refusal).
- Verification: Step 6 commands → all pass.

## Done criteria (ALL must hold)

- [ ] Step-1 failing output recorded; cases 1/3 green after Step 4.
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/run ./pkg/safety -count=1` → 3× ok.
- [ ] `gofmt` empty; `go vet` exit 0 on those packages.
- [ ] `pkg/architecture` no NEW failure vs `9a3ae0c2`.
- [ ] `grep -n "ProfileForConfig(cfg)" pkg/tool/process_tool.go` → no matches (replaced by `callSandboxProfile`).
- [ ] `grep -n '"process"' pkg/run/permissions.go` → the defer condition names it.
- [ ] Real-run Step 7 outcomes observed (or stale-skill STOP report).
- [ ] `git status` only in-scope files (+ plan/README in main checkout).
- [ ] README row appended; Status updated.

## STOP conditions

- plans/387 not landed (dependency), or Current-state excerpts drifted.
- The extraction (Step 2) moves ANY existing shell/process test — the helpers
  must be behavior-preserving; a moved test means the extraction is wrong.
- A project write can still land through `process` in plan mode after Step 4
  (any config) — the wall is the contract.
- The process Ask approval does not render on a surface (then report; do not
  build new UI).
- A step's verification fails twice after a reasonable fix attempt.
- The fix needs an out-of-scope file (batch boundary, reviewer scope, safety
  internals, a new cross-layer import).

## Maintenance notes

- **Reviewer scrutiny**: the two extracted helpers are byte-for-byte behavior
  -preserving for shell; `processStart`'s gate runs before any registry
  reservation leaks; the exclusive write set never inherits the workspace
  (the `ExclusiveWritablePaths` comment names this exact trap).
- **Covered beyond plan mode**: read-only typed workers (explore/plan/
  verification) get the same downgrade through `shellSandboxProfile` — the
  fix closes their `process` write path too, at no extra cost.
- **Deferred, reported not fixed**: `processStart` ignores
  `BatchBoundaryFromContext` — a batch task can escape its workspace via
  `process`. Separate plan (different contract: unattended task confinement).
- **Interaction**: if 387's gate signature or `ShellScratchRoots` changes,
  this plan's call sites follow; the two helpers are the single seam both
  tools read, so a future profile rule changes in one place.
