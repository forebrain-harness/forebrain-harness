# Plan 255: 三库矩阵的反腐化守卫按包精确匹配，引擎腿按「真的跑过」计数

> **Executor instructions**: 按步骤顺序执行，每步的验证命令通过后再做下一步。遇到
> 「STOP conditions」里的情况就停下来报告，不要自己发挥。**不要 commit / push / merge**：
> 改动留在工作区里，做完报告（仓库主人手动提交）。做完后把 `plans/README.md` 里本计划那一行
> 的状态改成 DONE，并附一句执行摘要——除非派你来的评审说索引由他维护。
>
> **Drift check (run first)**:
> `git diff --stat 236fc32a..HEAD -- scripts/test-db.sh`
> → 必须为空。不为空就把下面「Current state」里的摘录和实际脚本逐段对照，对不上即 STOP。

## Status

- **Priority**: P2
- **Effort**: S
- **Risk**: LOW（只改测试脚本；`engine_arms` 的 12 行内容不动）
- **Depends on**: none（与 247/248 无冲突：它们只改测试文件与 `engine_arms` 的模式片段）
- **Category**: tests
- **Planned at**: commit `236fc32a`, 2026-11-06
- **Finding**: 2026-11-06 分支审计（238–245 实现细节），发现 3（`introduced` by plan 245）

## Why this matters

`scripts/test-db.sh` 让 MySQL、PostgreSQL 两条引擎腿逐行跑 `engine_arms` 表。表里每行是
「有引擎臂的包 + 需要服务器的测试名片段」。

`check` 子命令是 245 加的反腐化守卫：只要一个包的测试摸到了 `OpenStateForTest` 或
`FOREBRAIN_TEST_DATABASE_URL`，它就必须在表里有一行，否则它的引擎臂只在 SQLite 上跑，矩阵却报全绿。

守卫用的是 `grep -q "$pkg"` 子串匹配。**已实测**（scratchpad 里拷一份脚本，删掉 `./pkg/state/` 那一行，
在仓库根跑 `check`）：exit 0，输出「engine arms: 11 rows, every package with arms listed」。原因是
`pkg/state` 是 `./pkg/state/dbx/...` 的子串。被删掉的恰好是全表最大的一行，MySQL 腿上 125 个测试。
245 的完成判据只拿 `pkg/hook` 做了负对照，所以没发现。

另一半：引擎腿用 `grep -c '^=== RUN'` 判断「这一行跑到了测试」。但 `t.Skip` 的测试也会打
`=== RUN`。如果某行匹配到的测试在服务器引擎上全部跳过（仓库里就有这种测试，比如
`TestCheckSQLiteSingleHost` 在服务器上 `t.Skip`），这一行就是同一种假绿。改成数顶层 `--- PASS`。

## Current state

`scripts/test-db.sh`（`set -euo pipefail`，脚本开头 `cd "$(git rev-parse --show-toplevel)"`）。

`engine_arms` 表（行 63-76）。第一个 `|` 前是 `go test` 的包参数，形态有三种：
- `./pkg/state/`，单个包、带尾斜杠；
- `./pkg/state/dbx/...`，包及其子包；
- `./pkg/process`，单个包。

```bash
engine_arms=(
  "./pkg/state/dbx/...|Dialect|Open|Tx|Lock"
  "./pkg/state/|Dialect|Schema|Open|Reap|SearchBackslash|StoreRepairs|Repair|Parts|Output|Events|Artifact|Project"
  "./pkg/process|Lease|ImportLegacy|ConfigDB|Startup|PeerInbound"
  …
)
```

`run_dialect` 里的计数（行 80-108 附近）：

```bash
    ran=$(grep -c '^=== RUN' "$log" || true)
    rm -f "$log"
    # A -run that matches nothing is not a pass: it is the silent skip this
    # whole change exists to remove.
    if [ "${ran:-0}" -eq 0 ]; then
      echo "no test matched '$pattern' in $pkgs — the row is stale" >&2
      return 1
    fi
    echo "   $ran tests matched"
```

`check`（行 114-131 附近）：

```bash
check() {
  local missing=0 pkg
  local have
  have=$(grep -rlE 'FOREBRAIN_TEST_DATABASE_URL|OpenStateForTest\(' --include='*_test.go' pkg 2>/dev/null \
    | xargs -n1 dirname 2>/dev/null | sort -u)
  for pkg in $have; do
    if ! printf '%s\n' "${engine_arms[@]}" | grep -q "$pkg"; then
      echo "engine arms in ./$pkg but no row in engine_arms" >&2
      missing=1
    fi
  done
  …
```

`$have` 当前是 12 个目录：`pkg/assembly pkg/gateway pkg/hook pkg/memory pkg/migrate pkg/process
pkg/run pkg/state pkg/state/dbx pkg/tool pkg/tui pkg/turn`。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Guard | `./scripts/test-db.sh check` | exit 0，「engine arms: 12 rows, every package with arms listed」 |
| Syntax | `bash -n scripts/test-db.sh` | exit 0 |
| Servers | `./scripts/test-db.sh up` | 「both servers are up」（需要 Docker） |
| One leg | `./scripts/test-db.sh postgres` | exit 0，每行打印「N tests passed」 |

## Scope

**In scope**:
- `scripts/test-db.sh`：只改 `check` 的匹配和 `run_dialect` 的计数

**Out of scope**:
- `engine_arms` 的任何一行（包与片段都不动）。
- `up`、`down`、`all`、`sqlite` 子命令。
- 任何 `*_test.go`。

## Steps

### Step 1: `check` 按包字段精确匹配

把 `check` 改成先把每行的包字段规范化，再逐包精确比较：

```bash
# row_covers <pkg> <row>: does this row's package field name pkg? The field is
# what `go test` is handed: "./pkg/x" or "./pkg/x/" names that package alone,
# "./pkg/x/..." names it and everything under it. A substring test is not
# enough — "pkg/state" is inside "./pkg/state/dbx/..." — so the comparison is
# on whole package paths.
row_covers() {
  local pkg=$1 row=$2 field base
  for field in ${row%%|*}; do
    field=${field#./}
    case $field in
      */...)
        base=${field%/...}
        if [ "$pkg" = "$base" ] || [ "${pkg#"$base"/}" != "$pkg" ]; then
          return 0
        fi
        ;;
      *)
        if [ "$pkg" = "${field%/}" ]; then
          return 0
        fi
        ;;
    esac
  done
  return 1
}

# The anti-rot guard: every package whose tests reach the engine must have a row
# above, or its arms run on SQLite while the matrix reports success.
check() {
  local missing=0 pkg row covered
  local have
  have=$(grep -rlE 'FOREBRAIN_TEST_DATABASE_URL|OpenStateForTest\(' --include='*_test.go' pkg 2>/dev/null \
    | xargs -n1 dirname 2>/dev/null | sort -u)
  for pkg in $have; do
    covered=0
    for row in "${engine_arms[@]}"; do
      if row_covers "$pkg" "$row"; then
        covered=1
        break
      fi
    done
    if [ "$covered" -eq 0 ]; then
      echo "engine arms in ./$pkg but no row in engine_arms" >&2
      missing=1
    fi
  done
  if [ "$missing" -ne 0 ]; then
    echo "add the package to engine_arms (with the tests that need a server)" >&2
    return 1
  fi
  echo "engine arms: $(printf '%s\n' "${engine_arms[@]}" | wc -l | tr -d ' ') rows, every package with arms listed"
}
```

`${row%%|*}` 故意不加引号：一行的包字段可以写多个包、用空格分隔，与 `run_dialect` 里 `$pkgs` 不加引号的用法一致。

**Verify**:
- `bash -n scripts/test-db.sh` → exit 0
- `./scripts/test-db.sh check` → exit 0，「engine arms: 12 rows, every package with arms listed」

### Step 2: 三个负对照

都在 scratchpad 里做，用临时副本，**不要改仓库里的脚本**。`TMPD` 取你的临时目录：

```bash
TMPD=$(mktemp -d)
grep -v '^  "./pkg/state/|' scripts/test-db.sh > "$TMPD/no-state.sh"
grep -v '^  "./pkg/state/dbx/' scripts/test-db.sh > "$TMPD/no-dbx.sh"
grep -v '^  "./pkg/hook|' scripts/test-db.sh > "$TMPD/no-hook.sh"
bash "$TMPD/no-state.sh" check; echo "exit=$?"
bash "$TMPD/no-dbx.sh" check; echo "exit=$?"
bash "$TMPD/no-hook.sh" check; echo "exit=$?"
```

在仓库根执行（脚本自己 `cd` 到 git 根，从仓库根跑才能找到 `pkg/`）。

**Verify**：三条都 `exit=1`，stderr 分别点名：
- `engine arms in ./pkg/state but no row`
- `engine arms in ./pkg/state/dbx but no row`
- `engine arms in ./pkg/hook but no row`

再确认修复前确实漏报：`git stash` 不可用（不许动工作区），所以改用 `git show 236fc32a:scripts/test-db.sh | grep -v '^  "./pkg/state/|' > "$TMPD/old-no-state.sh" && bash "$TMPD/old-no-state.sh" check; echo "exit=$?"`。应为 `exit=0`，这就是被修掉的漏报。

### Step 3: 引擎腿按顶层 `--- PASS` 计数

把 `run_dialect` 里计数那段改成：

```bash
    # A row counts as run only if a test in it passed. `=== RUN` is printed for
    # a test that then calls t.Skip, so a row whose every match skips on this
    # engine would read as covered while it ran nothing. Top-level results only
    # (subtests are indented).
    ran=$(grep -cE '^--- PASS: ' "$log" || true)
    skipped=$(grep -cE '^--- SKIP: ' "$log" || true)
    rm -f "$log"
    if [ "${ran:-0}" -eq 0 ]; then
      echo "no test passed for '$pattern' in $pkgs (${skipped:-0} skipped) — the row is stale" >&2
      return 1
    fi
    echo "   $ran tests passed, ${skipped:-0} skipped"
```

并在函数开头的 `local` 里加上 `skipped`。

**Verify**: `bash -n scripts/test-db.sh` → exit 0

### Step 4: 引擎腿实跑（需要 Docker）

```bash
./scripts/test-db.sh up
./scripts/test-db.sh postgres
./scripts/test-db.sh mysql
```

**Verify**：两条腿都 exit 0，12 行每行打印「N tests passed, M skipped」且 N ≥ 1。

负对照（临时副本）：

```bash
# A copy whose table is one row that matches a single test that skips on servers.
awk '/^engine_arms=\(/{print; print "  \"./pkg/state/|^TestCheckSQLiteSingleHost$\""; skip=1; next}
     skip && /^\)/{skip=0}
     !skip' scripts/test-db.sh > "$TMPD/skip-only.sh"
grep -A2 '^engine_arms=(' "$TMPD/skip-only.sh"   # expect the one row, then ")"
bash "$TMPD/skip-only.sh" postgres; echo "exit=$?"
```

这一行只匹配一个在服务器上 `t.Skip` 的测试，应 `exit=1`，并打印 `no test passed … (1 skipped)`。
如果 `TestCheckSQLiteSingleHost` 在 PG 上并没有跳过（输出里没有 `--- SKIP: TestCheckSQLiteSingleHost`），
换一个 `--- SKIP` 的测试名，并在报告里写明换了哪个。

如果本机没有 Docker、或 `up` 失败：跳过 step 4，在报告和 `plans/README.md` 里写「引擎腿未实跑，需 owner 在有 Docker 的机器上补验」。其余判据照常。

## Test plan

- 本计划没有 Go 测试，验证全靠 step 2 与 step 4 的脚本负对照：子串漏报修复前绿、修复后红；全跳过行修复后红。
- 保持绿：`./scripts/test-db.sh check`（12 行），以及两条引擎腿（有 Docker 时）。

## Done criteria

ALL must hold:

- [ ] `bash -n scripts/test-db.sh` exit 0
- [ ] `./scripts/test-db.sh check` exit 0，打印 12 rows
- [ ] step 2 的三个副本 `check` 全部 exit 1 并点名正确的包；旧脚本副本 exit 0（漏报已复现）
- [ ] `grep -nF 'grep -q "$pkg"' scripts/test-db.sh` → 无输出（子串匹配已去掉；改动前命中 `:121`）
- [ ] `grep -nF "'^=== RUN'" scripts/test-db.sh` → 无输出（改动前命中 `:97`）；`grep -nF "'^--- PASS: '" scripts/test-db.sh` → 1 行
- [ ] 有 Docker 时：两条引擎腿 exit 0，skip-only 副本 exit 1；无 Docker 时按 step 4 写明未验
- [ ] `git status --porcelain` 只有 `scripts/test-db.sh` 和 `plans/README.md`
- [ ] `plans/README.md` 的 255 行已更新

## STOP conditions

- `check` 修好后，真实脚本的 `check` 报某个包缺行：这说明表现在就漏了一个包。报告是哪个包，**不要**顺手加行，加行要先确认片段能匹配到测试。
- 某条引擎腿上，原本绿的某一行在改成数 PASS 后变红（整行全是 SKIP）：报告是哪一行、哪些测试在跳过，不要改片段。这是本计划要暴露的那类假绿，修法等裁决。
- macOS 自带的 bash 3.2 不支持你用到的某个语法：只用上面写出的语法，它们在 3.2 下可用；跑不通就报告。

## Maintenance notes

- 包字段的三种写法（`./pkg/x`、`./pkg/x/`、`./pkg/x/...`）都由 `row_covers` 处理。新增第四种写法，比如带通配，先改 `row_covers`。
- 「数 PASS」守的是「这一行在这个引擎上真的验证了点什么」。如果将来某一行本来就该在某个引擎上全跳过，说明这一行不该出现在那条腿上，应该拆表，而不是放宽计数。
- `engine_arms` 的行与 247/248 的测试改动互相独立。片段变了，只要还匹配到会 PASS 的测试，守卫就不报。
