# Plan 248: 穷举并修掉测试代码里全部原始 `?` 占位符字面量（247 的已知剩余）

> **2026-11-12 reconcile 留痕（评审方，非执行者）**：本计划的修复已随 `236fc32a` 入库。
> 今日在 HEAD `ecc4e1aa` 的 `git archive` 干净副本上复跑下文枚举脚本：**`TOTAL 1`**——
> `990ec9dd`（plan 268 批量落地）新增的 `TestDotEnvMigrateTurnsChannelSecretsIntoPlaintext`
> （`pkg/process/dotenv_migrate_test.go`，HEAD :171）带进一条未包裹的
> `dbx.Exec(ctx, db, \`INSERT INTO fb_project_channels … VALUES (?,?,?,?,?,?,?)\`)`。
> 该测试助手恒开 SQLite（`OpenStateFromHome` 空 URL），不是现网矩阵红灯，但违反本计划不变量。
> **已另立 `plans/301-dotenv-test-literal-speaks-its-engine.md` 修复**（本计划文件与范围不动）。
> 三引擎电池与负控制仍未复跑（本机 Docker 未启动）——索引状态保持「待复核」，301 落地且电池复跑后再改 DONE。

> **2026-11-17 reconcile 留痕（HEAD `fcb241bc`）**：301 已落地并核实（`pkg/process/dotenv_migrate_test.go:263`
> 的 INSERT 已包 `dbx.Rebind(dbx.DialectOf(db), …)`）。但不变量再次回归：下文枚举脚本在主树复跑得 **`TOTAL 6`**——
> `pkg/subscription/volcengine_test.go:331/362/398/434/458`（plan 281 带进，随 `09b44fe3` 落 dev）与
> `plugins/subscriptions/workbuddy/workbuddy_test.go:1366`（workbuddy 插件 in-tree 提交带进）。
> 两文件的助手都走 `state.OpenStateForTest`（引擎感知），但两包不在 `engine_arms` 行内，今天不红——与本计划
> 原 71 处同形。**已另立 `plans/317-test-literals-volcengine-workbuddy.md` 修复**（本计划文件与范围不动）。
> 另：脚本的 skip 集需补 `.worktrees`（否则把两个 worktree 副本各数一遍，TOTAL 18 假象）；317 携带修正版脚本。
> 三引擎电池与负控制仍未复跑——索引状态保持「待复核」，317 落地且电池复跑后再改 DONE。

> **2026-11-24 reconcile 留痕（HEAD `cba1bfba`，工作区干净）**：317 已落地（dev `1013b50f`）。下文枚举脚本
> 在 skip 集补上 `.worktrees`（317 的修正版）后复跑得 **`TOTAL 3`**，全部落在同一个文件：
> `pkg/process/subagent_migration_test.go:179/:210/:224` 的
> `` env.SQL.QueryRowContext(ctx, `SELECT value_json FROM fb_config WHERE section = ?`, …) ``。
> 该文件由 `d7c2e5b6`（2026-11-19「merge the shared subagent registry into definitions」）带入，
> 句柄 `env.SQL` 跟随引擎（`importOpenForTest`），故本计划的不变量适用、不是「钉死 SQLite」的豁免情形。
> **两处判据随 owner 裁决更新**：
> ① 三引擎电池与负控制**不再要求**——owner 常设裁决「本仓永不执行 `scripts/test-db.sh`」，SQL 改动只以
> SQLite 单测 + 代码核对验证；计划里「电池复跑后再改 DONE」的句子作废，此后再也不要为它起容器。
> ② 因此本计划现在**唯一的**完成判据就是：把上面三处包进 `dbx.Rebind(dbx.DialectOf(<同一句柄>), …)`
> 后复跑枚举脚本到 `TOTAL 0`。索引行已按此复位为 TODO。

> **2026-11-25 execute 留痕（评审方）**：owner 指令「execute 374 与 248」。执行器 worktree
> `.worktrees/exec-248-rebind`（分支 `exec-248-rebind`，基线 `db4f3f4a`，**零提交，改动留工作区**，
> no-git-commits）。完成的就是上面三处：`env.SQL.QueryRowContext` 的三条
> `SELECT value_json FROM fb_config WHERE section = ?` 包 `dbx.Rebind(dbx.DialectOf(env.SQL), …)`
> （语句逐字未动，`pkg/state/dbx` import 按序补入），+8/−3 单文件。评审独立复跑全部判据：
> 派发前主树扫描 **TOTAL 3**（三处行号与本留痕一致）→ 改后 worktree 扫描 **TOTAL 0**；
> `FOREBRAIN_HOME=$(mktemp -d) CGO_ENABLED=1 go test -tags fts5 ./pkg/process -count=1` → ok 228s；
> `gofmt -l` 空；`go vet -tags fts5 ./pkg/process` exit 0；`git status --porcelain` 恰好一个文件。
> 评审读全量 diff：三处包裹形状照同包 `llm_models_test.go` 的多行先例，句柄与方法同表达式。
> **评审判定 APPROVE；合并与否归 owner。** 按本留痕前的一条判据更新，本计划至此完成
> （三引擎电池与负控制按 owner 常设裁决永不起容器，SQLite 单测＋代码核对即为验证）。

> **同日落地留痕（owner「合并回主树dev分支」）**：worktree 内提交
> `test(process): rebind the migration literals to their engine (plan 248)` → rebase 到
> 377 落地后的 dev 尖 `9c964e8a`（无冲突，提交变 `aad6fb9d`）→ rebase 后门禁复跑
> （`./pkg/process` ok 225.8s、gofmt 净、vet 0、扫描 **TOTAL 0**）→ 主树
> `git merge --ff-only` 落 dev（`9c964e8a..aad6fb9d`，源码与测试提交逐字一致），
> 主树全量门禁随 374 落地后一并复跑。worktree 与分支已删，未 push。

> **Executor instructions**: Follow the steps in order; confirm each verification
> before the next. If a STOP condition occurs, stop and report — do not improvise.
> Do **not** commit, push or merge: leave the change in the working tree and report
> it. Do not edit `plans/` beyond the two Step 0.1 artifacts (the plan copy and the
> one README row, whose text Step 0.1 carries verbatim).
>
> **Drift check (run first)**:
> 1. `git rev-parse --short HEAD` → must be `d79a186b` (or a descendant that did
>    not touch the nine in-scope files: confirm with
>    `git diff --stat d79a186b..HEAD -- pkg/process/cron_service_test.go pkg/process/open_test.go pkg/process/config_db_test.go pkg/process/config_import_test.go pkg/state/db_test.go pkg/state/rewind_test.go pkg/state/schema_migrations_test.go pkg/state/session_events_test.go pkg/state/session_store_test.go`
>    → empty).
> 2. `git diff | git hash-object --stdin` → must print
>    `4fb18528944623420d671a5e6669581593c783cc` (plan 247's uncommitted six-file
>    diff — disjoint from this plan, keep it).
> 3. Re-run the enumeration script from "Current state" → must print `TOTAL 71`.
>    Any other number is a STOP: the tree moved; re-enumerate and report instead
>    of editing stale line numbers.

## Status

- **Priority**: P1 — owner's ruling 2026-11-06: 「必须穷举所有同类问题，全部修复」.
  This plan closes what 247 deliberately left as "the known remainder".
- **Effort**: M — 71 one-line wrappings across 9 test files, no production code.
- **Risk**: LOW — every site is a test fixture on a handle whose dialect is or
  reads as SQLite today, so `dbx.Rebind` is a no-op there; the batteries below
  prove nothing changes on any engine.
- **Depends on**: 247 (DONE — its nine wrappings are the pattern and its
  convention is the rule), 245 (DONE — the matrix that guards the packages).
- **Category**: tests (multi-database)

## Why this matters

247 fixed the 35 tests that were **red** on PostgreSQL and left 71 same-shape
sites recorded in its Maintenance as "not red today". This plan is the owner's
exhaustive order: the class is closed everywhere, so the invariant「no `?`
placeholder literal reaches a query method in test code unwrapped」holds
machine-checkably, and the next raw `?` somebody pastes into a test is visible
as the sweep's only hit instead of hiding in a crowd of 71.

The 71 sites are **not red today, and cannot be**, because every one of them
runs on a handle that is SQLite by construction (three paths, all verified):

1. `state.Open(path, …)` — `pkg/state/db.go:29` opens SQLite and registers
   SQLite; it never consults `FOREBRAIN_TEST_DATABASE_URL` (only
   `OpenStateForTest` does, `db.go:249`).
2. a raw `sql.Open("sqlite3"/sqliteDriverName(), …)` in a legacy-file builder —
   unregistered, and `dbx.DialectOf` deliberately reads the default SQLite for
   such a handle (`pkg/state/dbx/dbx.go:139-175`).
3. `process.Open` → `state.OpenStateFromHome(home, databaseURL)` where the URL
   comes from the test's own yaml (`pkg/process/open.go:343-354`) — and the
   tests that own the 20 pkg/process sites write no `database:` section in
   their yaml (grep-verified; `config_db_test.go:493/:541` do write one, but
   they own none of the sites — `:50`'s helper opens through `state.Open` at
   `:36`, path 1), so the URL is empty → SQLite.

So the fix is uniformity, not defect repair: wrap every site in
`dbx.Rebind(dbx.DialectOf(<same handle>), …)`, the exact convention 247 landed
and `pkg/state/dbx/dbx.go:89-97` names ("a test's fixture, a one-off probe").
The wrap is a no-op while the handle is SQLite, and is already correct the day
any of these tests is ever switched to `OpenStateForTest` or given a
`database.url` **on PostgreSQL** — the one engine where `?` is an operator
rather than a placeholder (MySQL accepts `?` as-is, so the wrap is a no-op
there too). That is the accident the invariant exists to survive.
(Carve-outs: statements that ask SQLite's own catalog — `sqlite_master`,
`pragma_table_info` at `db_test.go:77`, `rewind_test.go:212/224`,
`schema_migrations_test.go:130/848/911/3007/3101` — can never run on a server
no matter the wrapping; and `config_db_test.go:50`'s
`ON CONFLICT … excluded.value_json` is not MySQL syntax either. Those wraps
are pure uniformity, and that is all they claim.)

## Current state

**Enumeration (the authoritative list, 71 sites, 9 files, 2 packages).** The
count closes over three shapes: 69 plain literals passed directly as the
statement argument, 1 concatenated statement (`session_store_test.go:1499`),
1 literal assigned to a variable before execution (`schema_migrations_test.go:3676`).
247's prose said "71 literals" with a slightly different per-file split; the
reconciliation: its BasicLit-only AST could not see the concatenated site, and
its `schema_migrations` count (34) equals this plan's 33 direct + 1 var-assigned.

| File | Sites (line numbers) | Handle(s) | Open path (class) |
|---|---|---|---|
| `pkg/process/cron_service_test.go` | 15: 177, 205, 211, 226, 232, 295 (env.SQL); 533, 543, 598, 615, 640 (db); 683, 686 (db); 745, 748 (db) | `env.SQL`, `db` (= env.SQL) | `process.Open` on a yaml with no `database:` → SQLite (path 3) |
| `pkg/process/open_test.go` | 3: 188, 397 (env.SQL); 414 (`after`) | `env.SQL`, `after` | env.SQL: path 3; `after` = `state.Open` (path 1) |
| `pkg/process/config_db_test.go` | 1: 50 (`env.SQL`, in helper `storeSectionForTest`) | `env.SQL` | `state.Open` at `:36` (path 1) |
| `pkg/process/config_import_test.go` | 1: 349 (`env.SQL`, in `TestImportLegacyCredentialsAndMCPOverlay`) | `env.SQL` | `importOpenForTest` → `process.Open`, yaml without `database:` (path 3) |
| `pkg/state/db_test.go` | 1: 77 (`db`) | `db` | `Open` at `:71` (path 1) |
| `pkg/state/rewind_test.go` | 6: 189, 194 (`db`); 208, 212, 220, 224 (`migrated`) | `db` raw, `migrated` = `Open(path)` | `:165` raw sqlite (path 2); `:202` `Open` (path 1) |
| `pkg/state/schema_migrations_test.go` | 34: direct 130, 157, 237, 246, 848, 911, 920, 926, 1853, 1868, 1924, 1928, 2002, 2086, 2092, 2122, 2158, 2189, 2197, 2228, 2236, 2251, 2377, 2388, 2440, 2447, 2738, 3007, 3101, 3475, 3504, 3582, 3805 (`db` seeders / `migrated`); var-assigned 3676 (executed at 3677 on `conn`) | `db`, `migrated`, `conn` | builders `sql.Open("sqlite3")` (path 2); `migrated` = `Open(legacy file)` (path 1) |
| `pkg/state/session_events_test.go` | 9: 28, 84, 250, 291, 408, 420, 425, 430, 463 (`db`) | `db` | `Open` per test (path 1); file already imports `dbx` |
| `pkg/state/session_store_test.go` | 1: 1499 (`db`, **concatenated** statement: `` `…agent_id = ?…` + visible + `… = ?…` ``) | `db` | raw sqlite (path 2, `:1443`) |

Statements that must **not** be touched (verified): literals without `?`
(e.g. `subagent_test.go:5303`-style seeds, `PRAGMA user_version = 27`), the
already-wrapped sites (`pkg/state/db_test.go:395`, `rewind_test.go:45`,
`schema_migrations_test.go:3947/4006/4052`, everything 247 landed), and the
scanner fixtures in `pkg/architecture/dialect_coverage_test.go` (slice data,
never executed). `pkg/architecture/sqlite_sql.go:19-28` is the shared notion of
"query method" (Exec/ExecContext/Prepare/PrepareContext/Query/QueryContext/
QueryRow/QueryRowContext); no Sprintf-built SQL with `?` exists in test code
(grep-verified).

**Enumeration script** (write to /tmp/248-sweep.py, run
`python3 /tmp/248-sweep.py <repo-root>`; prints every hit + `TOTAL n`):

```python
import os, re, sys
root=sys.argv[1]
skip={'.git','node_modules','frontend','third_party','build','dist','vendor','docs','plans'}
hits=[]
for dirpath,dirnames,filenames in os.walk(root):
    dirnames[:]=[d for d in dirnames if d not in skip]
    for fn in filenames:
        if not fn.endswith('_test.go'): continue
        p=os.path.join(dirpath,fn)
        text=open(p,encoding='utf-8',errors='replace').read()
        func_starts=[(m.start(),m.group(1)) for m in re.finditer(r'^func (?:\([^)]*\) )?(\w+)\(',text,re.M)]
        for m in re.finditer(r'\.(QueryRow|QueryRowContext|Query|QueryContext|Exec|ExecContext|Prepare|PrepareContext)\(',text):
            i=m.end(); depth=1; j=i; lit=None
            while j<len(text) and depth>0:
                c=text[j]
                if c=='`':
                    k=text.find('`',j+1)
                    if k==-1: break
                    if depth==1 and '?' in text[j+1:k]: lit=text[j+1:k]; break
                    j=k+1; continue
                if c=='"':
                    k=j+1
                    while k<len(text):
                        if text[k]=='\\': k+=2; continue
                        if text[k]=='"': break
                        k+=1
                    if depth==1 and '?' in text[j+1:k]: lit=text[j+1:k]; break
                    j=k+1; continue
                if c=='(': depth+=1
                elif c==')':
                    depth-=1
                    if depth==0: break
                j+=1
            if lit:
                fn_name=''
                for pos,name in func_starts:
                    if pos<=m.start(): fn_name=name
                    else: break
                hits.append((os.path.relpath(p,root),text.count('\n',0,m.start())+1,m.group(1),fn_name,' '.join(lit.split())[:60]))
# one indirect shape the call-site scan cannot see: schema_migrations_test.go:3676
# assigns the statement to a variable before ExecContext runs it. It counts only
# while the bare literal is still there; once wrapped at the assignment the line
# stops matching and the total reaches zero. (session_store_test.go:1499 needs
# NO extra: the scan sees its first backtick segment directly.)
sm=open(os.path.join(root,'pkg/state/schema_migrations_test.go'),encoding='utf-8',errors='replace').read()
if 'stmt := `INSERT INTO fb_config(section, value_json, updated_at_ms) VALUES(?,?,10)`' in sm:
    hits.append(('pkg/state/schema_migrations_test.go',3676,'var','','(indirect) INSERT INTO fb_config'))
hits.sort()
for rel,line,meth,fn_name,stmt in hits: print(f"{rel}:{line}\t{fn_name}\t{stmt}")
print('TOTAL',len(hits))
```

After the fix this script must print `TOTAL 0` — that is the plan's invariant.

## Commands you will need

```bash
PG='postgres://postgres:fb@127.0.0.1:15432/postgres?sslmode=disable'
bash -n scripts/test-db.sh
./scripts/test-db.sh check          # engine arms: 12 rows, every package with arms listed
# full packages (the strong check — wider than any row pattern):
FOREBRAIN_TEST_DATABASE_URL="$PG" FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 \
  go test -tags fts5 ./pkg/state ./pkg/process -count=1
# MySQL full packages:
FOREBRAIN_TEST_DATABASE_URL='mysql://root:fb@127.0.0.1:13306/fbt' FOREBRAIN_HOME="$(mktemp -d)" \
  CGO_ENABLED=1 go test -tags fts5 ./pkg/state ./pkg/process -count=1
# SQLite (no URL):
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state ./pkg/process -count=1
# engine legs, serially:
./scripts/test-db.sh postgres
./scripts/test-db.sh mysql
```

Docker notes: `fb-pg`/`fb-mysql` were just recovered (2026-11-06); if they
degrade, `docker rm -f -v fb-mysql fb-pg && ./scripts/test-db.sh up` is the
script's own remedy — allowed once, then report. Watch
`df -h /System/Volumes/Data`: below ~5Gi free, stop and report (the 2026-11-06
incident filled the disk twice; `docker builder prune -af` reclaimed 9.4Gi).

## Scope

**In scope** (only these nine files, only the 71 sites + the `dbx` import where
the file lacks it — `cron_service_test.go`, `open_test.go`, `config_db_test.go`
lack it; the other six already import `pkg/state/dbx`):
- `pkg/process/cron_service_test.go`, `pkg/process/open_test.go`,
  `pkg/process/config_db_test.go`, `pkg/process/config_import_test.go`
- `pkg/state/db_test.go`, `pkg/state/rewind_test.go`,
  `pkg/state/schema_migrations_test.go`, `pkg/state/session_events_test.go`,
  `pkg/state/session_store_test.go`
- Step 0's copy: `plans/248-every-test-literal-speaks-its-engine.md` (+ README row).

**Out of scope**:
- Any production file — `TestStatementsHaveDialectArms` already guards them
  (`pkg/architecture/dialect_coverage.go:124` skips `_test.go`; production is
  clean and CI-enforced).
- `scripts/test-db.sh` rows — unchanged. A row fragment over a pinned-SQLite
  test is a tautology; widening nothing is the honest state (see Maintenance).
- A new architecture rule over `_test.go` — 247 rejected it with the owner's
  ratification; the sweep script above is the review-time tool, not a gate.
- The temporary control edits in Step 4 are restored before reporting; the tree
  ends with only the 71 wrappings + 247's diff.

## Steps

### Step 0: baseline and bookkeeping

1. Drift check (above). Copy this plan to
   `plans/248-every-test-literal-speaks-its-engine.md`; then append this row to
   the status table in `plans/README.md` (below 247's), as its own line —
   nothing else in the README changes:
   ```
   | [248-every-test-literal-speaks-its-engine](248-every-test-literal-speaks-its-engine.md) | 247 留下的 71 处同形字面量（owner 裁令「必须穷举所有同类问题，全部修复」）：全部钉死 SQLite（state.Open／裸 sql.Open／process.Open 空 yaml URL 三条路径，均不读 FOREBRAIN_TEST_DATABASE_URL），今天不红但同类——统一包 `dbx.Rebind(dbx.DialectOf(<同一句柄>), …)`，含 1 处拼接语句（session_store_test.go:1499）与 1 处变量赋值（schema_migrations_test.go:3676）；脚本清点归零（TOTAL 0）为不变量，三引擎双包全绿为准绳，负控制证包裹有效（OpenStateForTest 切换红→绿）。不加宽 engine_arms（钉死测试入行是同义反复） | Tests (multi-db) | TODO | **P1** | M | LOW |
   ```
2. **Red-check (recorded, expected green)**: run the full two packages on
   PostgreSQL (Commands). Expected: `ok ./pkg/state`, `ok ./pkg/process`.
   This is the honest baseline: it proves the 71 sites are latent, not live.
   If any test fails with `SQLSTATE 42601`, the classification table is wrong
   for that site — record it, continue (the wrap fixes it the same way), and
   call it out in the report. A failure that is **not** 42601 is a STOP:
   report, do not fix.
3. Same two packages on SQLite → `ok` (both), recorded.

### Step 1: wrap the 20 `pkg/process` sites

Pattern (statement text unchanged — only the call that hands it over):

```go
// before
if _, err := env.SQL.Exec(`UPDATE fb_sessions SET updated_at=? WHERE id=?`, quiet, sid); err != nil {
// after
if _, err := env.SQL.Exec(dbx.Rebind(dbx.DialectOf(env.SQL), `UPDATE fb_sessions SET updated_at=? WHERE id=?`), quiet, sid); err != nil {
```

Add `"github.com/forebrain-harness/forebrain-harness/pkg/state/dbx"` to the
import block of `cron_service_test.go`, `open_test.go`, `config_db_test.go`
(alphabetical, beside `pkg/state`); `config_import_test.go` already has it.
`DialectOf` is called on the same expression the method runs on (`env.SQL`,
`db`, `after`) — never on a different connection.

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/process -count=1` → `ok`.

### Step 2: wrap the 51 `pkg/state` sites

Same pattern. The two special shapes:

- `schema_migrations_test.go:3676` (var-assigned) — wrap at the assignment, on
  the owning handle:
  ```go
  stmt := dbx.Rebind(dbx.DialectOf(db), `INSERT INTO fb_config(section, value_json, updated_at_ms) VALUES(?,?,10)`)
  ```
  (`conn` at `:3677` is a `*sql.Conn`; `DialectOf` deliberately reads the
  default — SQLite — for one rather than answering for it, so the wrap names
  `db`, the handle `conn` came from, and the dialect is the one that handle
  actually speaks. Statement: read the function; if the parent handle has
  another name there, use that name.)
- `session_store_test.go:1499` (concatenated) — wrap the whole expression:
  ```go
  err = db.QueryRowContext(ctx,
      dbx.Rebind(dbx.DialectOf(db), `SELECT m.session_id FROM fb_messages m
  		 JOIN fb_sessions s ON s.id = m.session_id AND s.agent_id = ?
  		 WHERE IFNULL(TRIM(m.session_id), '') != ''
  		 AND m.`+visible+` = ?
  		 GROUP BY m.session_id
  		 ORDER BY MAX(m.created_at) DESC`),
      agentID, equivalent).Scan(&sid)
  ```
  (match the file's actual continuation lines; `Rebind` takes the assembled
  string, and a `?` never appears inside the spliced `visible` column name.)

The other 49 are plain one-line wrappings; `db_test.go`, `rewind_test.go`,
`schema_migrations_test.go`, `session_events_test.go`,
`session_store_test.go` already import `dbx`.

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state -count=1` → `ok`.

### Step 3: the invariant

`python3 /tmp/248-sweep.py <repo-root>` → **`TOTAL 0`**.

### Step 4: the negative control (mandatory)

Proves the wraps are load-bearing the day a pinned test is pointed at a server —
red without the wrap, green with it, on `pkg/state/session_events_test.go:28`:

1. Record the tree fingerprint: `git diff | git hash-object --stdin`.
2. Temporarily change that one test's open
   (`session_events_test.go:22`, `Open(ctx, filepath.Join(t.TempDir(), "state.db"), nil)`)
   to `OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.db"))`.
3. With the wrap from Step 2 in place, run on PostgreSQL:
   `FOREBRAIN_TEST_DATABASE_URL="$PG" … go test -tags fts5 ./pkg/state -count=1 -run 'TestSessionEventsAreIdempotentOrderedAndPageAtFixedHighWater'`
   → expect `ok` (the wrapped INSERT speaks PostgreSQL).
4. Temporarily revert only the `:28` wrap (raw literal again), same command →
   expect `FAIL` with `SQLSTATE 42601` at the INSERT. Restore the wrap.
5. Restore the `Open(ctx, …)` open at `:22`. Fingerprint must equal step 1's.
   If the test fails at step 3 for a non-42601 reason, record it, restore
   everything, and rerun the control on the fallback site
   `session_events_test.go:250` (`TestClaimWaitResumeIsAtomicAndPreservesSourceMetadata`,
   same two edits at its open `:223`). A fallback that also fails non-42601 is a
   finding to report, not a reason to skip the control.

### Step 5: the engine batteries

1. Full `./pkg/state ./pkg/process` on PostgreSQL → `ok` (Commands).
2. Full `./pkg/state ./pkg/process` on MySQL → `ok`.
3. Full `./pkg/state ./pkg/process` on SQLite → `ok`.
4. `./scripts/test-db.sh postgres` → exit 0, twelve rows, every match count > 0.
5. `./scripts/test-db.sh mysql` → same. (Serially, not concurrently.)

## Test plan

The change is to tests; the proof is batteries + invariant + control:

- **Baseline green recorded** (Step 0.2) — the 71 are latent, not live.
- **Green→green on three engines** (Step 5) — the no-op claim, on both changed
  packages, full suites (wider than every row pattern).
- **Sweep zero** (Step 3) — the class is closed; the next raw `?` is the only
  hit a reviewer will ever see.
- **Negative control** (Step 4) — one site demonstrated red-without/green-with
  the wrap on a real PostgreSQL; restored byte-identical afterwards.
- **No new tests**: there is nothing to unit-test about wrapping fixtures.

## Done criteria

ALL must hold:

- [ ] Fingerprints: `4fb18528…` is the pre-execution drift gate only (drift
      check #2); immediately after Step 2 record the new fingerprint
      `F2=$(git diff | git hash-object --stdin)`; Step 4's temporary control
      edits, once restored, must make `git diff | git hash-object --stdin`
      print `F2` again, byte-identically; sweep prints `TOTAL 0`
- [ ] `FOREBRAIN_TEST_DATABASE_URL=<pg> … go test -tags fts5 ./pkg/state ./pkg/process -count=1` → `ok` (full packages)
- [ ] Same on `mysql://root:fb@127.0.0.1:13306/fbt` → `ok`
- [ ] Same with no URL (SQLite) → `ok`
- [ ] `./scripts/test-db.sh postgres` and `mysql` each exit 0, twelve rows, all counts > 0
- [ ] Negative control observed red (42601) then green; opens restored; fingerprint identical
- [ ] `go build ./...` and `go vet ./...` exit 0 (needs
      `PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"`)
- [ ] `gofmt -l` on the nine files prints nothing
- [ ] `grep -c 'dbx.Rebind(dbx.DialectOf'` per file, post-fix expectations
      (pre-existing counts of that exact form measured on `d79a186b` + this
      plan's wrappings): cron 15, open 3, config_db 1, config_import **3**
      (2 pre-existing at `:793/:828`), db_test **5** (4 pre-existing at
      `:246/:296/:359/:395`), rewind **9** (3 pre-existing at
      `:45/:126/:128`), schema_migrations **34** (its 7 older wraps at
      `:3938…:4061` use the `dbx.Rebind(dialect, …)` variable form this grep
      does not match — that is expected, not a miss), session_events **9**
      (0 of this form pre-existing), session_store **21** (20 pre-existing)
- [ ] `git status --porcelain` shows exactly 15 modified files — 247's six
      (untouched) + this plan's nine — and nothing else (`plans/` is
      gitignored, so its two artifacts are checked by existence instead:
      `ls plans/248-every-test-literal-speaks-its-engine.md` succeeds and
      `grep -c '248-every-test-literal-speaks-its-engine' plans/README.md`
      prints 1)
- [ ] Plans row exists (copy + README line); index flip to DONE is the reviewer's

## STOP conditions

- Drift check fails (HEAD moved the nine files, or fingerprint ≠ `4fb1852…`):
  re-enumerate with the script, reconcile the table, report — do not edit from
  stale line numbers.
- Step 0 shows a **non-42601** failure on PostgreSQL in either package: report
  (engine defect outside this plan's class), do not fix.
- A statement among the 71 carries a `?` that is an operator, not a placeholder
  (a `LIKE` pattern holding `?`, a JSONB existence test): leave that one,
  report it. (None known; all 71 use `?` as value parameters.)
- `session_store_test.go:1499`'s splice can never be other than a column name —
  if you find the `?` placeholders there are NOT plain value parameters, stop
  and report.
- Docker dies and `./scripts/test-db.sh up` after `docker rm -f -v` does not
  revive it: report the two engine legs as **not run**, name the unverified
  criteria, run everything SQLite-side. Do not fake.
- Disk below ~5Gi during the batteries: stop, report (2026-11-06 precedent:
  two 100% fills; remedy is `docker builder prune -af`, owner's call).

## Maintenance notes

- **The rule** (247's, now closed over the whole tree): a test that hands SQL
  to a query method wraps it in `dbx.Rebind(dbx.DialectOf(<same handle>), …)`
  — regardless of whether the test's handle is engine-following today. A
  statement with only literal values needs neither; a statement shared with
  production becomes a `dbx.SQL` arm instead.
- **What guards it**: the enumeration script above, whose durable home is this
  plan file itself (copied to `plans/248-every-test-literal-speaks-its-engine.md`
  at Step 0.1) — a reviewer re-runs it by extracting the block from the plan to
  `/tmp` and running it against the root; `TOTAL 0` is the invariant, one grep,
  zero judgment. Deliberately NOT a repo script: a gate nobody wired into CI
  should not live in `scripts/` pretending to be one. The three-engine matrix
  guards the engine-following packages; `TestStatementsHaveDialectArms` guards
  production. A future raw `?` in a test shows up as the sweep's only hit.
- **Rows stay as they are, on purpose**: `./pkg/state` and `./pkg/process` row
  fragments cover the engine-following tests; the migration and legacy-file
  suites are SQLite by construction (their subject is a SQLite file), so a row
  over them would promise nothing. Before ever widening a row, run that
  package on PostgreSQL first (247's rule; a 42601 there is this class).
- **Why the pinned sites are wrapped too**: uniformity is the point — the
  invariant must be checkable without classifying every handle, and a pinned
  test that later switches to `OpenStateForTest` (or gets a `database.url` in
  its yaml) is already correct the day it happens.
- A reviewer should check: statement texts unchanged (diff shows only
  `dbx.Rebind(dbx.DialectOf(<handle>),` insertions and import lines),
  `DialectOf` on the same expression the method runs on, the two indirect
  sites wrapped at the right place, and Step 4's restoration verified by hash.
