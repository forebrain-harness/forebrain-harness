# Plan 391: close the residual plan-mode items from plan 390 (seven fixes, two settled by ruling)

> **Executor instructions**: continue in the existing worktree
> `.worktrees/plan-mode-scratch-verdict` (branch `plan-mode-scratch-verdict`) on top of
> commit `45b10529`, as a **third commit**. Run every verification command and confirm
> the expected result before moving on. If a STOP condition occurs, stop and report.
> Owner ruling for this round: 390 评审里“只报告未修”的项必须本期一并修复；其中
> 「执行/播放读不到的代码」一类（`sh script.sh`、`source`/`.`、`python3 -c`、`node -e`、
> 分页器）按 387 的档位结案 —— 除能证明的（`eval` 字面文本、分页器移出只读表）之外，
> 不新增拒绝。

**Baseline**: `45b10529` (worktree HEAD). Drift check, run first:
`git -C .worktrees/plan-mode-scratch-verdict log --oneline -1` and
`git -C .worktrees/plan-mode-scratch-verdict status --porcelain` (must be empty).

## Why this matters

Plan 390 shipped and was approved, but its record listed seven defects it did not
close, all in the same proof. Each is small and each has a test below. The two items
that cannot be closed soundly, or that the owner ruled closed, are recorded as
settled (see §Settled by ruling) rather than left dangling.

## The seven fixes

### F1 — `tar -t` / `--list` lists, it does not extract (`pkg/safety/shell_scratch.go`)

`tar -tzf /tmp/a.tgz` answers `Unplaceable` today: the `tar` branch derives its
`-C`/cwd bound from the extraction path even when the mode only lists. Measured:
`ShellScratchWriteVerdict("tar -tzf /tmp/a.tgz", roots, "/workspace/forebrain-harness")`
= `unplaceable`; `plans/387` answered Ask for the same line, so this is an
over-refusal introduced by 390.

Fix, inside `case "tar":` after `creates`/`reads` are known and before any `-C`/cwd
work: `if !creates && parsed.any("-t", "--list") { return ShellScratchWritesClean }`
— a listing writes nothing anywhere, whatever `-C` says. Extracting (`-x`) keeps its
current bound.

Tests (`pkg/safety/shell_scratch_test.go`, verdict table): Clean —
`tar -tzf /tmp/a.tgz`, `tar -tf /tmp/a.tar`, `tar -tzf /tmp/a.tgz -C pkg/tool`;
unchanged Outside — `tar -xzf /tmp/a.tgz`, `tar -xzf /tmp/a.tgz -C pkg/tool`.

### F2 — a closed subshell restores the tracked directory (`pkg/safety/shell.go` + `shell_scratch.go`)

`(cd /tmp) && echo hi > x` must be refused: bash runs the `cd` in a subshell, so the
second clause's `x` lands in the project, while the proof keeps `/tmp` and answers
Clean. Needs a subshell stack, not a smarter `cd`.

- `shellClause` gains `SubshellOpen, SubshellClose int`: the number of `(`/`{` and
  `)`/`}` tokens in the clause text that are **not** inside a recorded substitution
  (count them in `recordFacts` with the same scan that sets `Grouping`).
- The verdict walk (`shellScratchWriteVerdictDepth`) keeps a stack of the
  `(dir, known)` pair taken **before** the clause that opened a subshell; after the
  clause's own answer, pop one frame per `SubshellClose` and restore the saved pair.
  A clause with `SubshellClose > SubshellOpen` on an empty stack leaves the directory
  **unknown** (a `)` whose `(` the walker never saw).

Tests: Refuse (`Outside`) — `(cd /tmp) && echo hi > x`, `(cd pkg && echo hi > state.go)`,
`(cd /tmp && rm -rf pkg/tool)`; Clean — `(cd /tmp && echo hi > x)` (the write is inside
the subshell, in `/tmp`), `{ ls; }`.

### F3 — `classifyBase64Command` compares `-o` exactly (`pkg/safety/shell.go`)

Same defect class as 390's Cause 1, left in place: `strings.HasPrefix(a, "-o")` makes
`base64 -o…`-looking arguments write. Replace the loose prefix with the exact forms
`a == "-o"`, `a == "--output"`, `strings.HasPrefix(a, "--output=")`,
`strings.HasPrefix(a, "-o=")`; drop the prefix test. Keep `readOnlyArgsUnderRoots` as
the fallback.

Tests (`pkg/safety/shell_test.go`, classifier): `base64 -o /tmp/x` and
`base64 --output=/tmp/x` ⇒ `Writes`; `base64 -overlay x` and `base64 --output-dir x`
⇒ not `Writes`.

### F4 — `hasShellHeredocOperator` must not treat `<<<` as a heredoc (`pkg/safety/shell.go`)

The helper (used for approval-prefix proposals, `runtime.go:915`) returns true for any
`<<`. A herestring feeds data and has no body, so `cmd <<< data` was denied a reusable
prefix for no reason.

Fix: return true only when `ch == '<' && next == '<' && the character after that is not
'<'` (and, for `<<<`, keep scanning). Same rule the walker already applies.

Tests (`pkg/safety/shell_test.go`): `hasShellHeredocOperator("cat <<'EOF'\nx\nEOF")` ⇒
true; `hasShellHeredocOperator("cat <<< hi")` ⇒ false; `grep -c '<<<' file` ⇒ false
(inside quotes); and the existing prefix-proposal rows stay green.

### F5 — `eval '<literal text>'` is read like the line it is (`pkg/safety/shell.go`)

`eval` runs its operand as shell source; when that operand is literal it is a line this
package can classify, exactly like a `sh -c` script — and today it is not read at all.

- `classifyCommandDepth`: `case "eval":` — one operand whose text is literal (no `$`,
  backtick, `~`, glob) ⇒ `shellScriptMutation(operand, allowedRoots, cwd, depth+1)`;
  anything else keeps `ShellMutationUnproven` (the owner's ruling: an unreadable
  operand is not a new refusal).
- The verdict side already funnels through `shellScriptFromClause`; extend it so an
  `eval` clause with one literal operand returns that operand as the script (the
  depth cap and worst-of logic then apply unchanged). A clause cannot both write and
  `eval`, so the existing worst-of is enough.

Tests: Refuse — `eval 'rm -rf pkg/tool'`, `eval 'cd pkg && rm -rf tool'`,
`eval 'echo hi > state.go'`; Clean — `eval 'echo hi > /tmp/x'`, `eval 'ls -la'`;
Unread (unchanged) — `eval "$X"`; Writes tier — `ClassifyShellCommand("eval 'rm -rf pkg/tool'", …)`.

### F6 — pagers leave the read-only table (`pkg/safety/shell.go`)

`less`, `more`, `bat` and `batcat` are in `shellReadOnlyCommands` (lines around 2599 and
2625). They can hand the user a shell (`!cmd`, `s file`, `-o`) when stdin is a tty, so
the table's promise ("writes nothing whatever its arguments") is not true of them.
Remove the four names; they fall to `Unproven` (Ask), i.e. Unread in the verdict — the
same class as the other "runs more than it looks" commands the owner ruled closed by
387's tier. No other table entry changes.

Tests: `TestShellReadOnlyTableAdmitsNoArgumentAwareCommand` still passes (it iterates
the table); classifier rows `less /tmp/f`, `cat /tmp/f` (unchanged `ReadOnly`);
verdict row `less /tmp/f` ⇒ `Unread`; the gate row for it ⇒ Ask.

### F7 — `go -C <dir> test -c` finds its subcommand (`pkg/safety/shell.go`)

`classifyGoCommand` reads `args[0]` as the subcommand, so the global `-C <dir>` form
hides it: `go -C pkg test -c ./tool` is `Unproven` ⇒ runs under full access and writes
a test binary into the project.

Fix: before reading the subcommand, skip go's leading global flags — `-C <dir>` and
`-C=<dir>` (consume the value), and any `-flag=value` form; a bare unknown `-flag`
before a subcommand stays `Unproven` (conservative, do not guess whether it takes a
value). Then the existing `switch sub` handles the rest.

Tests: `go -C pkg test -c ./tool` ⇒ `Writes`; `go -C pkg test -o=/tmp/t ./tool` ⇒
`Writes`; `go -C pkg test ./tool` ⇒ `Unproven`; `go -C pkg vet ./...` ⇒ `Unproven`;
`go -overlay=/tmp/x.json test ./pkg/tool` ⇒ `Unproven`.

## Settled by ruling (no code; recorded so the record has no dangling items)

- **`sh script.sh`, `source`/`.`, `python3 -c`, `node -e`**: stay `Unproven` (⇒ Ask ⇒
  runs under full access), per the owner's ruling this round: they are the same class as
  `go test`/`go run`/`python3 x.py`, which `plans/387` deliberately lets run so plan mode
  can reproduce things. Closing only these would contradict that.
- **Substitutions in a write position** (`mkdir /tmp/x-$(date +%s)`, `echo hi > $OUT`):
  stay `Unplaceable`. This is not an open item but a proven impossibility: the
  substitution's *output* decides the path, `$(echo ..)` is read-only code, and no
  textual proof can bound a value it cannot see. The refusal is the correct answer;
  the plan's old "deferred" wording is what made it look open.

## Scope

**In scope**: `pkg/safety/shell.go`, `pkg/safety/shell_scratch.go`,
`pkg/safety/shell_test.go`, `pkg/safety/shell_scratch_test.go`,
`pkg/tool/shell_output_test.go` (gate rows for F2/F5/F6 only).
**Out of scope**: everything else, including `pkg/run/*`, `pkg/tool/process_tool.go`,
the sandbox path, and the two settled items above.

## Steps

### Step 1: add the failing tests for F1-F7 (each red before its fix)
Add the rows named above; record the failing output.

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/safety ./pkg/tool -count=1` fails on
the new rows only.

### Step 2: F1, F3, F4 (three independent one-function fixes).
**Verify**: `go test -tags fts5 ./pkg/safety -run 'TestShellScratchWriteVerdict|TestClassifyShellCommand|TestShellHeredoc|TestGetSimpleCommandPrefix' -count=1` ⇒ PASS.

### Step 3: F5 then F7 (both in `classifyCommandDepth`/`classifyGoCommand`).
**Verify**: `go test -tags fts5 ./pkg/safety -run 'TestClassifyShellCommand|TestClassifyGoCommand' -count=1 -v` ⇒ PASS.

### Step 4: F6 (table edit).
**Verify**: `go test -tags fts5 ./pkg/safety -count=1` ⇒ ok; `grep -n '"less"\|"batcat"' pkg/safety/shell.go` ⇒ no match in the read-only table.

### Step 5: F2 (subshell stack).
**Verify**: `go test -tags fts5 ./pkg/safety ./pkg/tool -run 'TestShellScratchWriteVerdict|TestPlanModeShellGate' -count=1 -v` ⇒ PASS, with the F2 rows exactly as listed.

### Step 6: broader checks
- `gofmt -l pkg/safety pkg/tool` ⇒ nothing; `go vet -tags fts5` on those three + `pkg/run` ⇒ exit 0.
- `go test -tags fts5 ./pkg/safety ./pkg/tool ./pkg/run -count=1` ⇒ 3× ok.
- `go test -tags fts5 ./pkg/architecture -count=1` ⇒ only the two known reds
  (`TestTestFilesCorrespondToProductionFiles`, and `TestWebBuildLoadsNoExternalAsset`
  because a fresh worktree has no `pkg/gateway/dist`).
- `go build -tags fts5 -o /tmp/fb391-bin ./cmd/forebrain` ⇒ ok.
- The 390 acceptance test (`TestShellToolPlanModeDangerFullAccessScratchResearch`) and
  the gate matrix still pass unmodified.

## Done criteria (all must hold)

- [ ] Every F1-F7 row is red before its fix and green after; Step 1 output recorded.
- [ ] `go test -tags fts5 ./pkg/safety ./pkg/tool ./pkg/run -count=1` ⇒ 3× ok.
- [ ] `gofmt` empty, `go vet -tags fts5` exit 0 on those three packages.
- [ ] `pkg/architecture` no NEW failure.
- [ ] The 390 rows still hold: `git clone /tmp/a /tmp/b`, `mkdir /tmp/x`,
      `curl -o /tmp/x.zip`, `cat > /tmp/x <<'EOF'`, the two reported transcripts ⇒ Allow;
      `echo hi > pkg/tool/x.txt`, `rm -rf pkg/tool`, `sed -i '' s/a/b/ main.go`,
      `(rm -rf pkg/tool)`, `{ rm -rf pkg/tool; }`, `! sed -i …`, `exec go test -c …`,
      `sh -c 'rm -rf pkg/tool'`, `bash -lc 'rm -rf pkg/tool'`, `> /tmp/cat sh -c '…'`,
      `ln -t pkg/tool /tmp/x`, `trap 'rm -rf pkg/tool' EXIT`, `cd $(rm -rf pkg/tool)`,
      `mkdir /tmp/$X`, `echo hi > $OUT` ⇒ Refuse.
- [ ] One commit on the branch (`fix(planmode): close plan 390's residual items`), no amend.

## STOP conditions

- A 390 wall row (the list in the last done criterion) starts answering Allow/Ask.
- Any `plans/387` flagship starts being refused.
- A herestring (`<<<`) or a quoted `<<<` starts being skipped as a heredoc body.
- `eval` with a non-literal operand starts being refused (owner ruling: unproven stays unproven).
- The subshell stack changes any row that does not contain `(`/`)`/`{`/`}`.
- A step's verification fails twice after a reasonable fix attempt.

## Maintenance notes

- **Reviewer scrutiny**: F2's counting must exclude parentheses inside substitutions
  (`echo $(date)`, `$((1+2))`); F5 must not read `eval $X`; F7 must not guess a value for
  a bare unknown flag.
- **One implementation**: the `<<<` rule exists in the walker already — F4 makes the
  helper agree with it, and a comment should say so; `eval` reuses `shellScriptMutation`
  and the verdict's `shellScriptFromClause` rather than a third path.
- **After this**: plan mode's proof has no item left that is "reported but not fixed";
  the two settled ones are decisions, and recorded as such in `plans/390-…` and the index.
## Implementation Status

**VERDICT: APPROVE**（2026-11-26；0 轮 REVISE，一处计划错误由执行器按诚实判读处理并被我复核）。
执行器 `alibaba/deepseek-v4.1-flash-1017`，worktree
`.worktrees/plan-mode-residuals`（分支同名，`6341ce52` → `git merge --ff-only plan-mode-scratch-verdict`
→ `45b10529` → 单提交 `85e91e89`）。5 文件 +295/−13，全在 §Scope 内，**已按 owner 指示 ff-only 落 dev**（2026-11-26：`6341ce52..85e91e89`，三提交 `7ef15483`/`45b10529`/`85e91e89`，未 push；落 dev 门禁复跑绿：gofmt 空、vet exit 0、`go build -tags fts5 ./cmd/forebrain` ok、三包全量 ok 11.3/21.3/42.0s、`pkg/architecture` 仅预存红 `TestTestFilesCorrespondToProductionFiles`；worktree/分支已按 owner「清理掉」删除（`git worktree remove` + `git branch -d`，内容全在 dev 历史内））。

| 任务 | 状态 |
|---|---|
| Pre-step ff 到 390 基线 `45b10529`（drift 空） | 完成 |
| Step 1 七项先红（含执行器记录的逐行旧值） | 完成 |
| F1 tar `-t/--list` ⇒ Clean | 完成 |
| F2 子 shell cwd 栈 | 完成（含计划行修正，见下） |
| F3 `base64 -o` 精确匹配 | 完成 |
| F4 `hasShellHeredocOperator` 识 `<<<` | 完成 |
| F5 `eval '<字面>'` 递归 | 完成 |
| F6 分页器移出只读表 | 完成 |
| F7 `go -C <dir> test -c` 找子命令 | 完成 |
| Step 6 更广检查 | 完成 |

### 评审自己跑出的证据

- `gofmt -l pkg/safety pkg/tool` 空；`go vet -tags fts5 ./pkg/safety ./pkg/tool ./pkg/run` exit 0；
  `go test -tags fts5` 三包 `-count=1` 全绿（8.8/19.9/45.7s）；`pkg/architecture` 仅两条已知红；
  `go build -tags fts5 ./cmd/forebrain` ok。
- 按名选取 390 的验收与矩阵（`TestShellToolPlanModeDangerFullAccessScratchResearch`、
  `TestPlanModeShellGateScratchVerdictMatrix`、两个旧 gate 测试、`TestShellScratchWriteVerdict*`、
  `TestSplitShellClassifyClauses*`、`TestClassifyShellCommand*`、`TestClassifyGoCommand*`、
  `TestShellHeredoc*`、`TestGetSimpleCommandPrefix*`、`TestDangerousCommand*`）全 ok。
- **我自己的探针**（`go test -overlay` 直调 verdict/tier）：F1 三条 list 全 Clean、两条 extract 与
  `tar -czf pkg/tool/x.tar .` 仍 Outside；F2 `(cd /tmp) && echo hi > x` 与 `(cd /tmp) && rm -rf pkg/tool`
  ⇒ Outside、`(cd pkg && echo hi > state.go)` ⇒ Outside、`) && echo hi > x` ⇒ Unplaceable、
  `{ ls; }`/`echo $(date) > /tmp/x` 仍 Clean（替换里的括号不计入子 shell）；F5 `eval 'rm -rf pkg/tool'`/
  `eval 'cd pkg && rm -rf tool'`/`eval 'echo hi > state.go'` ⇒ Outside，`eval 'echo hi > /tmp/x'`/
  `eval 'ls -la'` ⇒ Clean，`eval "$X"` ⇒ Unread；F6 `less /tmp/f` ⇒ Unread、`cat /tmp/f` ⇒ Clean；
  F3 `base64 -overlay x` ⇒ read-only、`-o`/`--output=` ⇒ writes；F4 `<<'EOF'` ⇒ true、`<<<` 与引号内 `<<<` ⇒ false；
  F7 `go -C pkg test -c`/`-o=` ⇒ writes、`go -C pkg test|vet`、`go -overlay=… test` ⇒ unproven。
- **390 无回归**：17 条墙行（`echo hi > pkg/tool/x.txt`、`rm -rf pkg/tool`、`sed -i`、`(rm -rf …)`、`{ … }`、
  `! sed -i`、`exec go test -c`、`sh -c`/`bash -lc`、`> /tmp/cat sh -c …`、`ln -t`、`trap … EXIT`、
  `cd $(rm -rf …)`、`mkdir /tmp/$X`、`echo hi > $OUT`、`nohup rm -rf pkg/tool`、`git -C pkg checkout`）
  全部非 Clean；10 条 387/390 放行行（`git clone`、`mkdir /tmp/x`、`curl -o /tmp/x.zip`、
  `cat > /tmp/x <<'EOF'`、`printf $PWD`、`sh -c 'echo hi > /tmp/x'`、387 旗舰、`tar -czf … -C`、
  `grep -c`、`unzip -c`）全部 Clean。

### 偏差（1 处，计划错误，已接受）

计划 F2 把 `(cd /tmp && rm -rf pkg/tool)` 列为 Refuse（Outside），但它与计划同时要求保持 Clean 的
`(cd /tmp && echo hi > x)` 是同一形态：写发生在子 shell 内、落在 `/tmp`，Clean 才是对的。执行器按机制
实现、把该行钉成 Clean 并写明理由，真正的洞 `(cd /tmp) && …`（子 shell 先关闭）与
`(cd pkg && rm -rf tool)` 两边都钉成 Refuse；我用探针复核了这三行，判读成立。

另加两条超出计划行的钉子（评审建议项）：`TestSplitShellClassifyClausesSubshellCounts`（替换/算术里的
括号不计子 shell）与 `) && echo hi > x` ⇒ Unplaceable。

### 结案（无代码，见 §Settled by ruling）

`sh script.sh`、`source`/`.`、`python3 -c`、`node -e` 维持 Unproven（owner 本轮裁决：与 `go test`/`go run`
同类，387 的档位结案）；写位置替换（`mkdir /tmp/x-$(date +%s)`、`echo hi > $OUT`）维持 Unplaceable——
不是待修项，而是文本证明不可能覆盖“替换的输出去决定路径”这一事实。
