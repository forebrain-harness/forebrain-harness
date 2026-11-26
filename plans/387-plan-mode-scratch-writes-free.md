# Plan 387: Plan mode keeps the project read-only but lets scratch research flow free

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: `git diff --stat 9a3ae0c2 -- pkg/safety/shell.go pkg/safety/sandbox_config.go pkg/tool/shell_tool.go pkg/tool/state.go pkg/run/config.go pkg/run/permissions.go pkg/agent/advisor_prompts.go`
> and `git status --porcelain -- pkg/safety pkg/tool pkg/run pkg/agent`. If any
> in-scope file has changed since this plan was written — committed or not —
> compare the "Current state" excerpts against the live code before proceeding;
> on a content mismatch, treat it as a STOP condition (moved line numbers alone
> are not drift; re-locate by the quoted symbols/text).

## Status

- **Priority**: P1
- **Effort**: L
- **Risk**: MED (touches the plan-mode write wall; the proof must be sound)
- **Depends on**: none
- **Category**: bug / dx (owner ruling: cut unnecessary agent restrictions; execution efficiency and result quality are the top priority)
- **Planned at**: commit `9a3ae0c2` (dev), 2026-11-26
**Landed**：2026-11-26 owner 指示「合并回主树dev分支」→ worktree 19 文件合成单提交 `bfc3ac17`
  （`fix(planmode): allow OS scratch, keep project read-only`，含 388 的追加），`9a3ae0c2..bfc3ac17`
  ff-only 落 dev，未 push；落 dev 门禁复跑绿（gofmt 空、vet exit 0、`go build -tags fts5 ./cmd/forebrain`
  ok、四包全量 ok 11.3/22.1/42.6/0.5s、`pkg/architecture` 仅预存红 `TestTestFilesCorrespondToProductionFiles`）；
  worktree/分支已于 2026-11-26 按 owner「删」删除（`git worktree remove` + `git branch -d`，
  内容全在 dev 历史内），`plans/` 仍未提交。
- **Implementation**: **DONE, review APPROVE（2026-11-26, round 2）** — worktree
  `.worktrees/plan-mode-scratch-writes`（分支同名，BASE `9a3ae0c2`），按 owner 常设规则
  **改动未提交**，执行器 `alibaba/deepseek-v4.1-flash-1017`；未 push、未合并。
  - **Round 1 REVISE（承重缺陷，评审亲测）**：`shellScratchAbsPath` 先 `filepath.Clean` 再解析符号
    链接，`<link>/../x` 被证明成 `<root>/x` 而内核把写入落在链接目标父目录 —— 评审用
    `go test -overlay` 探针实测 `echo >`/`mkdir`/`cp`/`rm -rf` 四种形态全部 `admitted=true` 且真的
    落在项目里（`rm -rf` 删掉项目文件）。修法＝`shellScratchWalkPath` 组件级行走（`..` 作用于**已解析**
    的 base，逐组件 `EvalSymlinks`，全程不 Clean），`cd` 操作数与 `rootsOutside` 同走该行走；
    回归 `TestShellWritesConfinedToRootsResolvesDotDotThroughLinks`（7 负例 + 4 正例）。
  - **评审独立复跑**（不采信执行器报告）：同样四种逃逸形态 `admitted=false`；gofmt 空、vet exit 0、
    `./pkg/safety ./pkg/tool ./pkg/run ./pkg/agent` 全绿（8.6s／20.4s／45.8s／0.3s）、
    `./pkg/architecture` 只有预存红 `TestTestFilesCorrespondToProductionFiles` + 本 worktree 环境红
    `TestWebBuildLoadsNoExternalAsset`（主树实跑该测试 ok，确认与改动无关）。
  - **已接受的偏差**：①walker 增加第三返回值 `doubt`（计划写的「floor 为 Unproven 即拒」不成立：重定向
    floor=Writes 会盖住扩张的 Unproven，`echo $(date) > /tmp/x` 曾被放行，新测试抓到）；②`pkg/run/testdata/
    office_plan_prompts.golden.json`（office 常量字节级 pin，计划漏列）与
    `pkg/run/orchestration_llm_test.go`（原 fixture 是 `t.TempDir()`，即 `$TMPDIR`，与新合同直接冲突，
    改到非临时路径并注明）两个计划外文件，均为必需且最小；③评审要求补的两条健全性规则（「项目压过
    scratch 区域」：包含 cwd/state root 的 scratch root 从证明、`GuardWrite`、sandbox 写例外三处一并剔除；
    `cp/install` 在有重定向时走严格形态）与 office 头句 `any file`→`any project file` 的措辞修正；
    ④`ln -s <项目文件> /tmp/l` 等保守假阴性，落在 Ask/Refuse，不产生静默写入（已记录，后续可放宽）。
  - **真机（run-forebrain）**：plan + full access 下转录命令首次调用即 `● Ran …`（无拒绝卡、无审批），
    同模式 `echo probe > pkg/tool/plan-mode-probe.txt` 仍以 `exit_plan_mode` 句拒绝且文件不存在；
    `tar` 植入 `/tmp` 符号链接后 `echo hi > /tmp/fbl/zz/../escaped.txt` 被拒、两处 `escaped.txt` 均不存在；
    scratch `write_file` 成功、项目 `write_file` 仍被 GuardWrite 拒。抓屏在
    `$TMPDIR/plan387-evidence/`。
  - **后续（未做，报告即可）**：`ln -s`/`cp … > /tmp/log` 类假阴性；`resolveShellPath` 里
    「先 Clean 再解析」的同一弱点在既有只读分类器（`pathUnderAnyShellRoot`）中仍存在，属**先于本计划**
    的既有行为，未改；`process start` 的 plan 模式洞由 [388](388-process-plan-mode-read-only-parity.md) 补上。

## Why this matters

Owner report (real TUI transcript, plan mode): a research command
`cd /tmp && curl -s -o claude-code.vsix <url> && … && rm -rf claude-x && mkdir claude-x && … && unzip -q ../claude-code.vsix …`
was hard-refused with `plan mode: "…" modifies files; call exit_plan_mode first`,
while a bare `curl -o /tmp/claude-ext.json` earlier passed. The agent had to
improvise an in-memory pipe workaround. Owner ruling (2026-11-26, verbatim):
「必须砍掉对代理的不必要的限制，以执行效率和最终结果质量为最高优先级」.

The restriction that is **necessary** and stays: plan mode must not edit the
project/codebase before the user approves (the owner's own plan-gated workflow
depends on it). The restrictions that are **unnecessary** and this plan removes:
disposable scratch work in the OS temp regions — downloading, extracting,
creating/removing temp files, reading them back, and drafting scratch files with
the file tools — is investigation, not implementation, and must run first-call
with no approval on every enforcement path. A second concrete hit in this Go
repo: `go test` / `go vet` / `go run` are classified as proven writes, so under
danger-full-access plan mode the agent cannot even reproduce a bug while
planning; they join the unproven tier (like `python3 script.py` already is).

## Current state (verified against `9a3ae0c2`, excerpts from my own reads)

### The three enforcement seams

1. **Tool gate (danger-full-access only)** — `pkg/tool/shell_tool.go`:
   - call site (~:182-190):
     ```go
     mutation := safety.ClassifyShellCommand(cmd, shellAllowedRoots(ctx, st, rt), wdAbs)
     readOnlyCommand := mutation == safety.ShellMutationReadOnly
     planGate := planModeShellGate(ctx, cfg != nil && cfg.DangerFullAccessEnabled(), mutation)
     if planGate == PlanModeShellRefuse {
         err := fmt.Errorf("plan mode: %q modifies files; call exit_plan_mode first", strings.TrimSpace(cmd))
     ```
   - gate (~:1847-1864): `ReadOnly → Allow`; not plan mode → Allow;
     `!dangerFullAccess → Allow` (sandbox enforces); approved action → Allow;
     `Writes → Refuse`; else `Ask`.
   - `PlanModeShellGateForCommand` (~:1877-1879) is the same gate for the
     approval boundary, called with `safety.ClassifyShellCommand(cmd, nil, "")`.
     Its only caller is `pkg/run/permissions.go:115-118`: when the answer is
     **not** Allow the boundary defers to the shell tool (no approval of its
     own); when Allow it continues its normal flow. **No change needed there**
     once ForCommand answers Allow for scratch-confined commands.

2. **OS sandbox (every other config)** — `pkg/tool/shell_tool.go`:
   - `shellSandboxProfile` (~:1767-1779): plan mode ⇒ `readOnlyWorker` ⇒
     `ProfileReadOnly` (unless an approved action replays).
   - `runSandboxedShellCommand` read-only branch (~:357-371):
     ```go
     if req.profile == safety.ProfileReadOnly {
         writablePaths = nil
         for _, grant := range snap.FileSystemGrants { …write grants… }
         reviewRoots = ReviewWriteScopeFromContext(ctx).shellWritableRoots()
         writablePaths = append(writablePaths, reviewRoots...)
     }
     ```
     `writablePaths` → `extraBinds` → `allowRead = promoteReadOnlyWriteScope(&req, extraBinds, allowRead)` (~:477).
     `promoteReadOnlyWriteScope` (~:1793-1807) turns read-only + write exceptions
     into `ProfileManaged` with `exclusiveWritablePaths = true` — **the exact
     mechanism plan-reviewer scratch already uses** (comment at ~:1748-1750).
     This is the seam to reuse, not a new one.
   - `pkg/safety/sandbox_config.go` (~:290-297): the workspace-write profile
     grants `tempDir` (`os.TempDir()`) and `/tmp` (non-Windows) "by definition
     (plan 206)". The read-only profile grants nothing.

3. **File tools** — `pkg/tool/state.go` `GuardWrite` (~:1112-1121):
   ```go
   planTarget := isSanctionedPlanWrite(ctx, absPath)
   mode := strings.ToLower(strings.TrimSpace(ModeFromContext(ctx)))
   if mode == "plan" && !planTarget {
       return fmt.Errorf("plan mode: write blocked (only the plan directory is writable); call exit_plan_mode first")
   }
   ```
   Containment helpers that already exist: `PathWithinRoot(path, root)`
   (`pkg/tool/state.go:2482`, symlink-resolving) and
   `pathUnderAnyShellRoot(path, roots)` (`pkg/safety/shell.go:2939`,
   `filepath.EvalSymlinks` on the deepest existing parent).

### Why the transcript command is refused (the classifier)

`pkg/safety/shell.go`:
- `ShellMutation` (~:1655-1667): `ReadOnly` / `Unproven` / `Writes`, ordered;
  `ClassifyShellCommand(cmd, allowedRoots, cwd)` (~:1697) takes the worst over
  clauses.
- `shellMutatingCommands` (~:1980-1984): `rm rmdir mv cp mkdir touch ln chmod
  chown chgrp dd truncate tee install patch shred unlink` → **unconditional
  `Writes`, target paths never inspected**.
- Redirects: `splitShellClassifyClauses` (~:1721) sets `floor = Writes` for
  `>`/`>>`/`&>` (~:1794/1810) **without recording the target**; the target word
  stays in the clause text. `/dev/null` redirects are stripped first
  (`stripDevNullRedirects`, ~:2744). Expansions (`$`, backticks), subshells/
  braces, and word-start `~` floor `Unproven`.
- `curl`, `unzip`, `tar`, `wget` are in no table → `Unproven`.
- `cd` (~:2055) and read-only commands' path args (~:2033
  `readOnlyArgsUnderRoots`) must land inside `allowedRoots`;
  `shellAllowedRoots` (`pkg/tool/shell_tool.go:1895-1918`) = workspace + skill
  roots + read grants — **never the OS temp regions**. So `cd /tmp` and
  `cat /tmp/x` are `Unproven` (Ask), not ReadOnly.
- `classifyGoCommand` (~:2160-2161):
  ```go
  case "build", "test", "install", "generate", "run", "vet", "fix", "clean", "work", "tool", "fmt", "get":
      return ShellMutationWrites
  ```
- The package comment at ~:1641-1644 warns: mutating knowledge lives in one
  place; a second copy drifts. The new proof below therefore **reuses** this
  package's walker and tables; it does not fork them.

### Reproduction (real run of the current classifier, 2026-11-26)

A throwaway module in `/tmp/fb-plan-repro` (`go.mod` with
`replace github.com/forebrain-harness/forebrain-harness => <this repo>`,
`main.go` printing `safety.ClassifyShellCommand(cmd, []string{<repo>}, <repo>)`)
run with `CGO_ENABLED=1 GOFLAGS=-mod=mod GOPROXY=off GOTOOLCHAIN=go1.26.10 go run .`
printed:

```
unproven | curl -s --max-time 60 -o /tmp/claude-ext.json 'https://marketplace…'
writes   | cd /tmp && curl -s --max-time 60 -o claude-code.vsix 'https://anthropic.gallerycdn…' && … rm -rf claude-x && mkdir claude-x && … unzip …
unproven | cd /tmp
writes   | mkdir /tmp/claude-x
writes   | rm -rf /tmp/claude-x
unproven | cat /tmp/claude-ext.json
writes   | echo hi > /tmp/x.txt
unproven | curl -s -o /tmp/x.zip 'https://example.com/x.zip'
unproven | unzip -q /tmp/x.zip -d /tmp/claude-x
writes   | sed -i '' s/a/b/ main.go
writes   | rm -rf pkg/tool
writes   | echo hi > pkg/tool/x.txt
```

Causal chain: `mkdir`/`rm` are proven writes ⇒ whole chain `Writes` ⇒ gate
`Refuse` (hard, no approval escape). The bare `curl` was `Unproven` ⇒ `Ask` ⇒
passed via YOLO/approval — exactly the inconsistency the transcript noticed.
Under YOLO, shell approvals are bypassed (`rt.YOLO` ⇒
`approval_bypassed_by_yolo`, `pkg/tool/shell_tool.go:733-737`), so **the Refuse
tier is the only wall the owner actually hits**; scratch work must land on the
Allow side of it. Project writes (`sed -i`, `rm -rf pkg/tool`, redirect into
`pkg/`) must keep landing on Refuse.

### Prompt texts that must stay truthful

- `pkg/run/config.go` `buildPlanModeReminder`:
  - coding full (~:1201-1204): `write_file, edit_file: ALLOWED only for files
    directly inside the plan directory…`, `shell commands that modify files:
    BLOCKED until exit_plan_mode`, `…cannot be proven read-only: you will be
    asked to approve them…`.
  - coding sparse (~:1137/1142): `…all other writes blocked…`.
  - coding subagent (~:1104/1114/1120-1123): same claims.
  - office full `officeMainPlanFullFormat` (~:1223): `Every other write is
    blocked.` / `Anything that creates or changes other files: blocked until
    the plan is approved.`; office sparse/subagent variants nearby.
- `pkg/agent/advisor_prompts.go` `SinglePlanModePrompt` (~:51-55):
  `…no other path is writable in plan mode…`. Pinned byte-for-byte by
  `pkg/agent/testdata/advisor_prompts.golden.json` (test comment: deliberate
  contract change ⇒ golden in the same diff; the test never rewrites it).
- Exact-text reminder assertions: `pkg/run/config_test.go` ~:2138-2338.

### Relationship to plan-reviewer semantics (verified at `9a3ae0c2`)

Same mechanism and discipline, deliberately different scope — they are two
roles, not one concept:

- **Same seam**: both add write exceptions to the read-only profile and ride
  `promoteReadOnlyWriteScope` (managed + `exclusiveWritablePaths`). Step 4
  appends the scratch roots next to `reviewRoots` in the same branch; there is
  no second promotion path.
- **Same containment discipline**: symlink-resolving containment; production
  code stays read-only.
- **Different scratch shape**: the reviewer owns a fresh private directory
  (`os.MkdirTemp("", "forebrain-plan-review-")`, `review_scope.go:75`, tidied
  by `Release`); plan mode gets the whole OS temp pair (no per-session
  ownership, no cleanup). Deliberate: the owner's efficiency ruling, the
  transcript's natural `cd /tmp` flow, and the pair is exactly what the
  workspace-write sandbox already grants by definition (plan 206). A review
  is a bounded one-shot reproduction; planning is open-ended research.
- **Different project access**: the reviewer MAY write new test files in the
  project and anything in its isolated worktree (`admitWrite`,
  `review_scope.go:173-200`); the plan-mode main agent may write NO project
  file, test or otherwise. Plan mode's wall is stricter here on purpose:
  implementation follows approval, while a reviewer's job is falsification.
- **No interference (verified)**: `plan-reviewer` blocks `write_file`/
  `edit_file` at the type level (`pkg/tool/permissions.go:805-808`), so
  `subagentDispatchBlocksFileWrites` is true and the child does NOT inherit
  plan mode (`pkg/run/subagent.go:2038-2046`); it runs in agent mode and its
  writes answer only to `ReviewWriteScope`. Step 5's plan-branch scratch
  admission therefore cannot widen the reviewer, and the reviewer's scope
  cannot widen plan mode. Keep it that way: no change to the inheritance
  condition or to `admitWrite` is in scope.

### Conventions to match

- Three-state classifier answers, worst-of composition, tables in one place:
  `pkg/safety/shell.go` (exemplar: `classifySortCommand` ~:2089).
- Read-only write exceptions via `promoteReadOnlyWriteScope`, never a second
  profile: exemplar `ReviewWriteScope.shellWritableRoots`
  (`pkg/tool/review_scope.go:162-168`).
- Gate tests: `pkg/tool/shell_output_test.go`
  `TestPlanModeShellGateSplitsReadOnlyWritesAndUnproven` (:401) and
  `TestShellToolPlanModeGateAsksOnlyForUnprovenCommands` (:445) — model new
  cases on these.
- GuardWrite plan tests: `pkg/tool/request_permissions_test.go` ~:2669/2756/2973.
- `promoteReadOnlyWriteScope` tests: `pkg/tool/shell_tool_test.go` ~:993-1017.
- Layering: `tool → safety`, `run → tool`, `run → agent` already exist; this
  plan adds **no new cross-package import** (fan-out budgets unchanged).

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Env (cgo/pdf) | `export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"` | — |
| Focused tests | `CGO_ENABLED=1 go test -tags fts5 ./pkg/safety ./pkg/tool ./pkg/run ./pkg/agent -count=1` | all 4 packages `ok` |
| Single test | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run TestPlanModeShellGate -count=1 -v` | named tests PASS |
| Format | `gofmt -l pkg/safety pkg/tool pkg/run pkg/agent` | prints nothing |
| Vet | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/safety ./pkg/tool ./pkg/run ./pkg/agent` | exit 0 |
| Layering | `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` | only the pre-existing red `TestTestFilesCorrespondToProductionFiles` (`deliverables_outcome_test.go`, introduced by `80bb73b4`, red at baseline) — do NOT fix it |
| Full suite (optional, scratch home) | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./... -count=1 -p 2` | only the same pre-existing red |
| Real run | the project's `run-forebrain` skill (fake-provider TUI harness) | see Step 8 |

`go.mod` requires go ≥ 1.26.9; the local toolchain auto-switches to the cached
1.26.10 (`GOTOOLCHAIN=auto`). If a cold environment lacks it, let go fetch it.

## Scope

**In scope** (the only files you should modify):
- `pkg/safety/shell_scratch.go` (create) — scratch roots + confinement proof.
- `pkg/safety/shell_scratch_test.go` (create).
- `pkg/safety/shell.go` — redirect-target capture in the clause walker; go-tier precision.
- `pkg/safety/shell_test.go` — update tier assertions the go change moves; walker regression.
- `pkg/safety/sandbox_config.go` — the workspace-write temp pair calls the new `ShellScratchRoots()` (one implementation of "the disposable regions").
- `pkg/tool/shell_tool.go` — gate + ForCommand + `shellAllowedRoots` + read-only-profile scratch exception.
- `pkg/tool/state.go` — `GuardWrite` plan branch admits scratch targets.
- `pkg/tool/shell_output_test.go`, `pkg/tool/shell_tool_test.go`, `pkg/tool/request_permissions_test.go` — new/updated cases.
- `pkg/run/config.go`, `pkg/run/config_test.go` — reminder wording (coding + office, full/sparse/subagent).
- `pkg/agent/advisor_prompts.go`, `pkg/agent/testdata/advisor_prompts.golden.json` — `SinglePlanModePrompt` wording.
- `plans/README.md` — **append one index row only** (parallel agents own the rest of this file; never rewrite existing rows).

**Out of scope** (do NOT touch, even though they look related):
- `pkg/run/permissions.go` — the boundary already defers correctly once
  ForCommand answers Allow; verify, don't edit.
- `pkg/tool/process_tool.go` — it runs `safety.ProfileForConfig(cfg)` with no
  plan-mode downgrade (a pre-existing hole in the *opposite* direction:
  `process start` can write the project in plan mode). Report it in one line;
  tightening it is a separate plan, not this contract.
- `pkg/turn`, `pkg/gateway`, `pkg/tui`, `frontend/`, `editors/` — no surface
  changes; the refusal/approval UX is unchanged.
- Interpreter classification (`python3`, `node`), `go build -o` cache
  admission, `npm`/`pip`/`cargo` download forms, `git` beyond `clone` —
  deferred (see Maintenance notes).
- The pre-existing `pkg/architecture` red named above.
- Public tool schemas, approval payload shapes, `ShellMutation` constant
  values/order (the proof is a separate function; the three-state type is
  unchanged).

## Git workflow

- Owner standing rule: no commits unless instructed. Work in an isolated
  worktree `.worktrees/plan-mode-scratch-writes` (branch of the same name,
  created from HEAD by the dispatch); leave changes **uncommitted** for review
  and say so in the report. If the operator instructs landing, follow the
  repo's recent pattern: rebase onto current dev, ff-only, never push.
- `plans/` lives in the main checkout; write the plan/status there, not in the
  worktree. Append-only on `plans/README.md`.

## Steps

### Step 1: Pin the desired behavior with failing tests (the reproduction gate)

Create `pkg/safety/shell_scratch_test.go` and add cases to the existing test
files **before** any production change:

1. `pkg/safety/shell_scratch_test.go` — table-driven
   `TestShellWritesConfinedToRoots` calling
   `ShellWritesConfinedToRoots(cmd, []string{os.TempDir(), "/tmp"}, cwd)` with
   `cwd` = a project dir. TRUE cases: the exact transcript command (Current
   state §Reproduction, full form); `mkdir /tmp/x`; `rm -rf /tmp/x`;
   `echo hi > /tmp/x.txt`; `curl -s -o /tmp/x.zip <url>`;
   `unzip -q /tmp/x.zip -d /tmp/x`; `cd /tmp && curl -s -o x.zip <url> && unzip -q x.zip`;
   `cp pkg/tool/state.go /tmp/copy.go` (read project, write scratch);
   `tee /tmp/a /tmp/b`; `dd if=/dev/zero of=/tmp/z bs=1k count=1`;
   `tar -xzf /tmp/a.tgz -C /tmp/x`; `git clone <url> /tmp/repo`.
   FALSE cases: `sed -i '' s/a/b/ main.go`; `rm -rf pkg/tool`;
   `echo hi > pkg/tool/x.txt`; `mv pkg/tool/state.go /tmp/x` (moves a project
   file out); `rm -rf /tmp` (the root itself); `rm -rf /` ;
   `echo hi > /tmp/../etc/x`; `curl -o /tmp/x --cookie-jar /tmp/c <url>`;
   `echo $(date) > /tmp/x`; `echo hi > /tmp/$NAME`; `rm -rf ~/Documents`;
   `python3 /tmp/x.py`; `unzip /tmp/x.zip` with cwd=project (extracts into
   project); a symlink inside `/tmp` pointing at the project used as a
   redirect target (create it in `t.TempDir()`-style setup under a fake root).
2. `pkg/tool/shell_output_test.go` — extend the gate test pattern (:401):
   under `planCtx` + `danger`, the transcript command and `mkdir /tmp/x` ⇒
   `PlanModeShellAllow`; `sed -i … main.go` and `rm -rf pkg/tool` still
   `PlanModeShellRefuse`; sandboxed config and agent mode unchanged.
3. `pkg/tool/request_permissions_test.go` — model on :2973: in plan mode
   `GuardWrite(ctx, filepath.Join(os.TempDir(), "draft.md"))` returns nil;
   a project path still returns the `plan mode: write blocked` error.
4. `pkg/safety/shell_test.go` — `ClassifyShellCommand("go test ./pkg/tool", …)`
   and `go vet ./…`, `go run .` ⇒ `ShellMutationUnproven`;
   `go test -c -o /tmp/t ./pkg/tool` and `go generate ./…`, `go mod tidy`
   still `ShellMutationWrites`.

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/safety ./pkg/tool -count=1`
→ FAIL: the new scratch/gate/GuardWrite tests fail (undefined
`ShellWritesConfinedToRoots` is a compile error — that is the expected red for
the safety package; write the function signature stub returning `false` first
if you need the tool tests to compile and fail on assertions), and the go-tier
test fails with `writes` where `unproven` is wanted. Record the failing output.

### Step 2: The confinement proof in `pkg/safety`

Create `pkg/safety/shell_scratch.go`:

- `func ShellScratchRoots() []string` — `os.TempDir()` plus `/tmp` when
  `filepath.Separator == '/'` and `/tmp` is a directory (mirror the existing
  `Resolve` semantics of `FileSystemSpecialPathSlashTmp`,
  `permission_types.go:339-345`). Deduplicate. Then change
  `sandbox_config.go` ~:292-297 to append `ShellScratchRoots()` instead of its
  own two lines (behavior identical; one implementation).
- `func ShellWritesConfinedToRoots(cmd string, roots []string, cwd string) bool`
  — conservative proof that **every file the command writes lands strictly
  inside one of `roots`** (never the root itself, never an ancestor). Returns
  false on anything it cannot read. Structure:
  1. `cmd = stripDevNullRedirects(cmd)`; empty ⇒ false; empty roots ⇒ false.
  2. Walk clauses with the existing walker (see Step 2a). Reject if the
     structural floor is `Unproven` (expansions, subshells, braces, word-start
     `~`) or any heredoc (`<<`) was seen.
  3. Track `cwd` across clauses in order: a `cd` clause with a plain path
     operand updates it (relative to the current cwd); `cd` writes nothing.
  4. Every redirect target the walker recorded must resolve strictly inside
     roots (relative targets against the tracked cwd).
  5. Each clause's command must pass the whitelist below; anything else ⇒
     false. Read-only table commands (`shellReadOnlyCommands`) pass with
     unrestricted operands (reads are not bounded by this proof).
  6. Containment: resolve the target (relative → tracked cwd), reuse the
     `pathUnderAnyShellRoot` probe logic (`EvalSymlinks` on the deepest
     existing parent, shell.go:2939-2967) and additionally require the
     resolved target ≠ the resolved root. Unquote target words with
     `SplitShellWords`.

  Whitelist (operand rules; `--` ends flags; unknown flags or operands ⇒
  false; combined short flags like `-rf` are flags):
  - `rm rmdir mkdir touch unlink shred truncate ln`: every non-flag operand
    strictly inside roots.
  - `chmod chown chgrp`: skip the first non-flag operand (mode/owner), rest
    strictly inside roots.
  - `cp install`: destination = last non-flag operand (or `-t`/
    `--target-directory` value) strictly inside roots; sources unrestricted.
  - `mv`: every non-flag operand strictly inside roots.
  - `tee`: every non-flag operand strictly inside roots.
  - `dd`: `of=` strictly inside roots (no `of=` ⇒ writes no file); `if=`
    unrestricted; other `key=value` operands ignored; a bare operand ⇒ false.
  - `curl`: `-o`/`--output` (separate, attached `-o<path>`, or `=<path>`)
    strictly inside roots; `-O`/`--remote-name` ⇒ tracked cwd strictly inside
    roots; any of `--cookie-jar/-c/--dump-header/-D/--trace/--trace-ascii/
    --trace-time/-K/--config/--libcurl` ⇒ false; URL operands unrestricted.
  - `wget`: `-O/--output-document`, `-P/--directory-prefix`,
    `-o/--output-file` values strictly inside roots.
  - `unzip`: `-d` value strictly inside roots, else tracked cwd strictly
    inside roots; known no-value flags `-q -o -j -n -X`; archive operand
    unrestricted.
  - `tar`: `-C/--directory` value strictly inside roots, else tracked cwd
    strictly inside roots; `-f/--file` value strictly inside roots when the
    mode creates (`-c/--create/-r/--append/-u/--update`), unrestricted when it
    reads (`-x/--extract/-t/--list`); no mode flag ⇒ false; compression flags
    `-z -j -J -a` allowed.
  - `git`: only `clone`; destination = last non-flag operand strictly inside
    roots; allowed flags `-b/--branch <v> --depth <v> --single-branch --bare
    -q/--quiet -v/--verbose -n/--no-checkout -c/--config <v> --template <v>`;
    every other subcommand ⇒ false.

- Step 2a — minimal walker change in `shell.go`:
  `splitShellClassifyClauses` (only caller: `ClassifyShellCommand`, verified)
  returns `([]shellClause, ShellMutation)` where
  `shellClause{Text string; Redirects []string; Heredoc bool}`. In the `>` and
  `&>` branches, **peek** the following word (`consumeShellWord`) and record it
  in the current clause's `Redirects` — do NOT remove it from `Text` (clause
  texts stay byte-identical, so existing tiers cannot move). Detect `<<` and
  set `Heredoc`. `ClassifyShellCommand` adapts by classifying `.Text` exactly
  as before.

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/safety -count=1` → ok,
including every Step-1 proof case and all pre-existing classifier tests
(the walker refactor must not move any existing tier).

### Step 3: Go toolchain precision in `classifyGoCommand`

In `pkg/safety/shell.go` `classifyGoCommand` (~:2131-2163): split the
`"build", "test", …` case. `test` (unless `-c` or `-o` present), `vet`, `run`
⇒ `ShellMutationUnproven` (they write build caches/temp and execute code the
classifier cannot see — the same tier `python3 script.py` already holds).
`test -c`/`-o`, `build`, `install`, `generate`, `fix`, `fmt`, `clean`, `work`,
`tool`, `get`, and the mutating `mod` forms keep `ShellMutationWrites`.
Update the tier assertions in `shell_test.go` that this moves (grep
`"go test"`, `"go vet"`, `"go run"` there); leave engine-rule tests that merely
use `go test` as a prefix example untouched.

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/safety -count=1` → ok;
the Step-1 go-tier test now passes.

### Step 4: Wire the shell tool (gate, boundary, roots, sandbox)

In `pkg/tool/shell_tool.go`:
1. `planModeShellGate`: add `cmd, cwd string` parameters; after the
   not-plan/not-danger/approved early answers and **before** `Writes → Refuse`,
   insert `if safety.ShellWritesConfinedToRoots(cmd, safety.ShellScratchRoots(), cwd) { return PlanModeShellAllow }`.
   Update the call site (~:185) to pass `cmd, wdAbs`. Update the doc comment:
   scratch-confined writes are investigation, not implementation.
2. `PlanModeShellGateForCommand`: run the same proof with `cwd = ""`
   (cd-chains still resolve; a leading relative write is unprovable here and
   falls through to today's behavior — the boundary then defers to the tool,
   which has the real cwd, so no spurious prompt).
3. `shellAllowedRoots` (~:1895): when `ModeFromContext(ctx)` is plan, merge
   `safety.ShellScratchRoots()` into the returned roots — scratch reads and
   `cd /tmp` classify ReadOnly and run unasked. Agent mode unchanged.
4. Read-only branch of `runSandboxedShellCommand` (~:357-371): when
   `ModeFromContext(ctx)` is plan, append `safety.ShellScratchRoots()` to
   `writablePaths` (next to `reviewRoots`). The existing
   `promoteReadOnlyWriteScope` path turns this into a managed, exclusive
   write scope — the OS then allows exactly the scratch writes and nothing
   else. Do not add a new profile or a second promotion path.

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run 'TestPlanModeShellGate|TestShellToolPlanMode' -count=1 -v`
→ PASS including the new scratch cases; then add a sandbox test modelled on
`shell_tool_test.go:993-1017`: a plan-mode read-only request gains the scratch
roots as `extraBinds`, promotes to `ProfileManaged` with
`exclusiveWritablePaths`, and the project cwd is NOT writable. Run
`CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -count=1` → ok.

### Step 5: File tools — `GuardWrite` admits scratch in plan mode

In `pkg/tool/state.go` `GuardWrite` (~:1119): the plan branch becomes
`if mode == "plan" && !planTarget && !scratchTarget`, where `scratchTarget`
is `PathWithinRoot(absPath, root)` for some `root` in
`safety.ShellScratchRoots()` **and** `absPath` is not the root itself
(`filepath.Clean` compare). Keep the error text for everything else. Update
the function's doc comment (plan dir + OS temp scratch are the writable
regions; the project stays read-only).

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run 'GuardWrite|PlanMode' -count=1`
→ PASS, including the Step-1 scratch case and the existing plan-rejection
tests (:2669/2756/2973) unchanged. The 7 tests in
`pkg/tool/review_scope_test.go` must pass **unmodified** — they are the
no-interference proof that the reviewer's scope is untouched.

### Step 6: Make the prompts truthful

- `pkg/run/config.go`: in every plan-mode reminder variant (coding full/sparse/
  subagent, office full/sparse/subagent) replace the blanket "all other writes
  blocked" claims with the two-region rule, e.g. coding full tool guidance:
  `- write_file, edit_file: ALLOWED inside the plan directory above and inside
  OS temp scratch ($TMPDIR, /tmp); every project path is read-only` and
  `- shell writes confined to OS temp scratch (downloads, extraction, temp
  files): free use, no prompt` / `- shell writes inside the project: BLOCKED
  until exit_plan_mode`. Keep the unproven-command sentence as is. Sparse and
  subagent variants get one short clause each ("project writes blocked; OS
  temp scratch ($TMPDIR, /tmp) is writable"). Office variants: same two-region
  wording, keeping their deliverable rules intact.
- `pkg/agent/advisor_prompts.go` `SinglePlanModePrompt`: replace
  `no other path is writable in plan mode` with
  `project files stay read-only in plan mode (OS temp scratch is writable)`.
  Update `pkg/agent/testdata/advisor_prompts.golden.json` in the same diff
  (the test compares bytes and never rewrites the golden).
- Update the exact-text assertions in `pkg/run/config_test.go` (~:2138-2338)
  to the new wording, and add one assertion per variant family that the
  scratch sentence is present and the old blanket sentence is gone.

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/agent -count=1`
→ ok. `grep -rn "all other writes blocked\|BLOCKED until exit_plan_mode" pkg/run/config.go`
→ only the project-write lines remain (no blanket claim).

### Step 7: Proportionate broader checks

**Verify** (all must hold):
- `gofmt -l pkg/safety pkg/tool pkg/run pkg/agent` → empty.
- `CGO_ENABLED=1 go vet -tags fts5 ./pkg/safety ./pkg/tool ./pkg/run ./pkg/agent` → exit 0.
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/safety ./pkg/tool ./pkg/run ./pkg/agent -count=1` → 4× ok.
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` → only the
  pre-existing `TestTestFilesCorrespondToProductionFiles` red (baseline red,
  out of scope). Any NEW architecture failure is a STOP.
- Optional but recommended: full scratch-home suite (`-p 2`), same expectation.

### Step 8: Real-run acceptance (run-forebrain skill)

Use the project's `run-forebrain` skill (fake-provider TUI harness; build with
`-tags fts5`). Configure the session sandbox mode `danger-full-access` and
enter plan mode. Drive three shell calls and read the rendered screen:
1. The exact transcript command (Current state §Reproduction) → runs on the
   first call, no refusal card, output shows the `ls -la` listing.
2. `go test ./pkg/safety -run TestShellWritesConfinedToRoots -count=1` → runs
   (no refusal).
3. `echo probe > pkg/tool/plan-mode-probe.txt` → still refused with
   `plan mode: … modifies files; call exit_plan_mode first`; the file does not
   exist afterwards.
Also verify a scratch `write_file` (e.g. `<os.TempDir()>/fb-387-draft.md`)
succeeds in plan mode and a project `write_file` is still blocked.
If the harness cannot set danger-full-access or the skill's mechanics fail on
something unrelated to this change, STOP and report the skill as stale with
the exact error — do not charge it to the code under audit and do not
improvise a different harness.

**Verify**: the three outcomes above observed on the rendered screen (capture
paths in the report).

### Step 9: Index and status

Append one row to the main index table in `plans/README.md` (after the last
`| [385-…]` row; do not edit any existing row — parallel agents own them):
`| [387-plan-mode-scratch-writes-free](387-plan-mode-scratch-writes-free.md) | plan 模式只读项目、临时目录调查读写全放行（scratch 证明 + go test/vet/run 归 unproven + 提示词 truthful） | Bug/dx | TODO | P1 | L | MED |`
Set this plan's Status to DONE with the evidence summary when Steps 1-8 pass.

## Test plan

- New: `pkg/safety/shell_scratch_test.go` (Step 1 list — happy paths, the exact
  transcript regression, project-write negatives, expansion/heredoc/symlink/
  root-itself negatives). Structural pattern: the table tests in
  `pkg/safety/shell_test.go`.
- Extended: gate tests (`shell_output_test.go:401/445` pattern), sandbox
  promotion test (`shell_tool_test.go:993` pattern), GuardWrite plan tests
  (`request_permissions_test.go:2973` pattern), go-tier assertions, reminder
  exact-text tests, advisor golden.
- Failure paths that must stay red-then-green: every Step-1 test fails before
  its production step and passes after; the project-write negatives must pass
  at EVERY step (they are the wall — if one ever goes green→red, STOP).
- Verification: the Step 7 commands → all pass.

## Done criteria (ALL must hold)

- [ ] Step 1 failing output recorded; every new test passes after its step.
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/safety ./pkg/tool ./pkg/run ./pkg/agent -count=1` → 4× ok.
- [ ] `gofmt -l pkg/safety pkg/tool pkg/run pkg/agent` empty; `go vet -tags fts5` on those packages exit 0.
- [ ] `pkg/architecture` shows no NEW failure vs baseline `9a3ae0c2`.
- [ ] `grep -n "ShellWritesConfinedToRoots" pkg/tool/shell_tool.go` → gate AND ForCommand call sites.
- [ ] `grep -rn "all other writes blocked" pkg/run/config.go` → no matches.
- [ ] Real-run Step 8 outcomes observed (or a stale-skill STOP report).
- [ ] `git status` shows only the in-scope files (plus `plans/387-…` and the appended README row in the main checkout).
- [ ] `plans/README.md` row appended; this plan's Status updated.

## STOP conditions

Stop and report (do not improvise) if:
- The Current-state excerpts no longer match the live code (content drift).
- You find ANY command the proof admits whose write can land in the project
  (symlink escape, ancestor target, expansion, flag the whitelist mis-reads).
  The wall is the contract; a soundness doubt outranks the efficiency goal.
- A step's verification fails twice after a reasonable fix attempt.
- The fix appears to require an out-of-scope file (e.g. `pkg/turn`,
  `process_tool.go`, a new cross-layer import, a fan-out budget change).
- The assumption "the boundary defers when ForCommand ≠ Allow" is false at
  `pkg/run/permissions.go:115-118`.
- The run skill fails on mechanics unrelated to this change (report stale).

## Maintenance notes

- **Reviewer scrutiny**: the proof's soundness (strict containment, symlink
  resolution via the existing probe, global expansion/heredoc rejection,
  default-false for unknown commands/flags) and that the walker refactor kept
  every pre-existing tier byte-identical. The go re-tier deliberately moves
  `go test/vet/run` to the same tier as `python3 script.py`: under YOLO the
  operator already accepts unproven code execution; under a sandbox the OS
  confines it; only non-YOLO danger-full-access asks. That is the recorded
  tradeoff, not an oversight.
- **Known pre-existing gap, reported not fixed**: `process start`
  (`pkg/tool/process_tool.go:408`) uses `ProfileForConfig` with no plan-mode
  downgrade — plan mode's read-only promise does not cover the process tool.
  Separate plan.
- **Extension point**: new scratch-safe command forms are added to the
  whitelist in `shell_scratch.go` only (one place, per the package's
  one-table rule). Deferred candidates: `go build -o <scratch>` (needs build
  cache admission), `npm pack`/`pip download`/`cargo fetch`, interpreters
  reading scratch scripts, `git` beyond `clone`.
- **Interaction with future changes**: if `ShellMutation` ever gains a tier or
  `shellAllowedRoots` changes shape, revisit the gate order (scratch proof
  must run before `Writes → Refuse`). If the sandbox temp policy (plan 206)
  changes, `ShellScratchRoots()` is the single source both seams read.
- TOCTOU note: containment resolves symlinks at proof time; a link created
  between proof and execution is the same residual risk the batch/review
  guards already carry (plan mode walls accidental edits, not an adversarial
  agent under YOLO).
