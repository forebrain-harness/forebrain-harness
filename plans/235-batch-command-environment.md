# Plan 235: a batch command gets the environment the plan promised, and explicit values win

> **Executor instructions**: Step 1 is a measurement that decides step 3 — record
> its output before writing code. Follow the steps in order; if a STOP condition
> occurs, stop and report. When done, update this plan's row in `plans/README.md`.
>
> **Drift check (run first)**:
> `git diff --stat 7d505aa3..HEAD -- pkg/home/envguard.go pkg/tool/shell_tool.go`
> Content mismatch against the excerpts is a STOP condition.

## Status

- **Priority**: P2
- **Effort**: S (steps 1–2), plus a decision for step 3
- **Risk**: LOW
- **Depends on**: none
- **Category**: bug / correctness
- **Planned at**: commit `7d505aa3`, 2026-11-04
- **Finding**: branch audit 2026-11-04, finding 15 (`introduced` by plans 208/216)

## Why this matters

Two things are wrong with the environment a batch (unattended) task's shell
commands run under.

**1. An explicit value can lose to the inherited one.** `SafeSubprocessEnv` merges
inherited and explicit variables into one slice and then `sort.Strings` the whole
thing. `os/exec` keeps the **last** entry for a duplicated key, so which of two
values wins is decided lexicographically by the whole `KEY=value` string. For
`TMPDIR`, the parent's `/var/folders/…` sorts after the task's
`<ws>/.work/tmp` (`pkg/tool/shell_tool.go:545-552`), so the task's own redirection
silently loses — and every cache the task writes lands back in the user's home (or
fails, when the home is denied). Which one sorts last depends on the two paths, so
the bug is not reproducible on every machine; the rule is.

**2. `HOME` is rewritten, which the approved plan says not to do.** Plan 216 §3.1
records: caches are redirected per variable, and "`HOME` 不改" — the user-level
config files (`~/.gitconfig`, `~/.npmrc`, `~/.config/pip/pip.conf`) stay readable
because they are outside `FOREBRAIN_HOME`. The shipped code instead sets
`HOME=<workspace>/.work/home`, which makes every tool blind to those files: in the
offline-intranet deployment plan 208 targets, a batch run then cannot see the
mirror the operator configured in `~/.npmrc`.

## Current state

`pkg/home/envguard.go:60-104` — the merge, and the sort that decides precedence:

```go
	out := make([]string, 0, len(parent)+len(opts.ExplicitEnv))
	for _, item := range parent { ... out = append(out, key+"="+val) }
	for key, val := range opts.ExplicitEnv { ... out = append(out, k+"="+val) }
	sort.Strings(out)
	return out
```

`pkg/tool/shell_tool.go:543-563` — the task's own variables, then the `HOME`
rewrite:

```go
		for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
			shellEnv = append(shellEnv, key+"="+tmpDir)
		}
		// The task's home lives in its workspace too, because the real one is
		// unreadable across the task's boundary: a login shell still reads
		// $HOME/.profile, so leaving HOME at the real home puts
		// "sh: /Users/<user>/.profile: Operation not permitted" on stderr of
		// every command the task runs. Appended last so no earlier entry
		// overrides it.
		homeDir := filepath.Join(filepath.Clean(boundary.Workspace), ".work", "home")
		...
		shellEnv = append(shellEnv, "HOME="+homeDir)
```

`pkg/tool/batch_boundary.go:42-70` — what the boundary actually denies. Read the
doc comment: `home` is **FOREBRAIN_HOME**, and the second denial is the project
root. The user's own `$HOME` is not named here — which is why plan 216 says the
config files are "naturally readable".

`plans/216-batch-project-semantics.md:100` — the contract, verbatim:
"…`npm_config_store_dir`（pnpm）=`<ws>/.cache/pnpm`；`HOME` 不改。"
and `:101` — "`plans/208-enterprise-intranet-offline.md` §3.2「跑批读集合加包管理器配置文件」被本节取代（这些文件在 FOREBRAIN_HOME 外，天然可读）".

So the code's premise ("the real home is unreadable") contradicts the plan's
premise ("naturally readable"). Step 1 settles it with a measurement.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Vet | `go vet ./...` | exit 0 |
| Focused | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/home ./pkg/tool -count=1` | `ok` |

## Scope

**In scope**:
- `pkg/home/envguard.go`
- `pkg/tool/shell_tool.go` (the batch environment block only)
- the matching `_test.go` files

**Out of scope**:
- `pkg/tool/batch_boundary.go`'s denial set — if step 1 shows the real home must be
  denied, that is the owner's decision (plan 229's territory), not this plan's.
- The cache variables' values (`batchCacheEnv`) — they are per plan 216.
- The sandbox backends' profile generation.
- Any change to how a non-batch command's environment is built.

## Steps

### Step 1 — measure (no code changes)

Write a throwaway Go test (or a small program) in `pkg/tool` that, for a batch
boundary whose workspace is under a temp dir, prints the environment
`SafeSubprocessEnv` would produce for the task and then resolves, for one real
batch command, whether the **real** `$HOME/.gitconfig` (or `$HOME/.npmrc` when that
exists) is readable inside the sandbox. Use the sandbox's own decision path
(`safety.NewManager().DecideShellCommand(...)` for a batch boundary is the closest
seam; if it is not reachable, run the command through the tool with `HOME` left at
the real home and read stderr).

Record four facts in your report:

1. which `TMPDIR`/`HOME` value wins today (the raw slice, before `os/exec`);
2. whether a login shell succeeds with `HOME` at the real home;
3. whether `~/.gitconfig` / `~/.npmrc` are readable from inside the boundary;
4. whether `$HOME` is **writable** from inside the boundary at the value the
   implementation sets today (`<ws>/.work/home`) and at the real home. This is
   the fact that decides step 3: the batch sandbox allows writes only inside the
   workspace, and `pkg/tool/shell_tool_test.go:2031-2056` already asserts both
   that `$HOME` is `<ws>/.work/home` and that `echo hi > "$HOME/notes.txt"`
   succeeds. Do not plan a change that makes that test fail.

**Verify**: the throwaway test prints the three facts. Delete it before step 2.

### Step 2 — explicit values must win, regardless of spelling

Replace the `sort.Strings(out)` merge with a precedence-preserving one:

- build a map keyed by the variable name (case-insensitive on Windows — use
  `strings.EqualFold` only under `runtime.GOOS == "windows"`, and keep the parent's
  spelling otherwise);
- inherited values first, then `opts.ExplicitEnv` overwrites by key;
- return a sorted-by-key slice **of the deduplicated set** (sorting is fine once
  duplicates are gone; it keeps the output deterministic for tests).

Add tests: an explicit `TMPDIR` beats the parent's; an explicit `HOME` beats the
parent's; a parent-only variable survives; on Windows, `Path` and `PATH` are the
same key.

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/home ./pkg/tool -count=1` → `ok`; the new precedence tests pass.

### Step 3 — follow the measurement

**默认答案已定（advisor 建议，2026-11-04，复核后改判）：保留 `HOME` 改写。** 依据是仓里已经存在的
契约，不是偏好：跑批沙箱只允许写工作区（`pkg/safety/backend_darwin.go:513` 的 `(deny default)` 加工作区
白名单），而 `pkg/tool/shell_tool_test.go:2031-2056` 断言 `$HOME == <ws>/.work/home` **且**写
`$HOME/notes.txt` 必须成功并落在工作区。把 `HOME` 改回真实家目录会让这条测试必挂，并让跑批工具写
`~/.cache`、`~/.npm`、`~/.local` 时 EPERM。216 的正文自相矛盾：`:99` 写「`HOME` 不改」，
`:187` 的手工验收却写「`TMPDIR`/`HOME` 指向任务自己的工作区」——实现跟随的是 `:187`。本计划按 `:187`
执行，并把 `:99` 作为文档缺陷写进报告（不改 `plans/216`，计划文档属 owner）。

要做的是让真实配置在隔离的 `HOME` 下仍然可见：

- 保留 `HOME=` 改写与缓存变量重定向；
- 按 step 1 的测量结果，给 `GIT_CONFIG_GLOBAL`、`NPM_CONFIG_USERCONFIG`、`PIP_CONFIG_FILE`、`GOENV`
  设置真实文件路径（存在**且沙箱内可读**才设），注释写明这是对 216 `:99` 的有意偏离、依据是 `:187`
  与沙箱写边界；
- 若测量显示真实 home 在沙箱内完全不可读（`cat ~/.gitconfig` 失败），**不设**这些指针：
  在报告里写明「216 `:99` 的『天然可读』前提不成立」，并把镜像/凭据配置的可达性留给 229 的决策表，
  不要用伪造的路径掩盖。

**Verify**: a test asserts the chosen invariant: `HOME` equals `<ws>/.work/home`, and each
pointer that is set names a file that exists. Do not assert a pointer for a file the
measurement could not read.

## Test plan

- New: precedence tests (step 2), the chosen `HOME` invariant (step 3).
- Structural pattern: existing `pkg/home/envguard_test.go` cases (they pin the
  allowlist and the secret filter — keep them green).
- Failure paths: duplicate key from parent and explicit; `Path`/`PATH` on Windows;
  a config file that does not exist (no pointer is set).

## Done criteria

ALL must hold:

- [ ] `go vet ./...` exits 0
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/home ./pkg/tool -count=1` passes
- [ ] `grep -n "sort.Strings(out)" pkg/home/envguard.go` returns nothing, or the sort provably runs after deduplication
- [ ] An explicit `TMPDIR` wins (asserted)
- [ ] `HOME` still equals `<ws>/.work/home` (asserted), and every config pointer that is set names an existing, sandbox-readable file
- [ ] The report records the four measurements from step 1 and names the 216 `:99` / `:187` conflict (a removed rewrite must not be presented as "following 216")
- [ ] Only in-scope files modified
- [ ] `plans/README.md` row for 235 updated

## STOP conditions

- Step 1 cannot be measured without changing production code: stop and report the
  seam you would need — do not guess which premise is true.
- The secret filter (`looksSecretKey`) rejects a variable you now route explicitly
  (e.g. `NPM_CONFIG_USERCONFIG` contains no secret but a mirrored registry URL might
  carry credentials): report it; credential-bearing variables stay out.
- You are tempted to delete the `HOME` rewrite because 216 `:99` says "HOME 不改":
  stop. The sandbox write boundary and `pkg/tool/shell_tool_test.go:2031-2056`
  are the contract, and 216 `:187` says the same as the code. Record the document
  conflict in the report; do not "fix" it by breaking batch tasks.

## Maintenance notes

- Precedence is now by key, not by spelling: any future caller relying on
  "append last and sort" is broken and should be fixed at its call site.
- A reviewer should check the Windows case (`Path` vs `PATH`) and that no
  credential-bearing variable was added to the explicit allowlist.
- Deliberately deferred: whether the batch boundary should deny the user's home at
  all. That is plan 229's decision table; this plan only makes the environment
  honest about whichever premise the owner picks.
