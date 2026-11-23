# 217 — 把 `pkg/turn` 与 `pkg/gateway` 的测试方言化（裸 `?` 占位符 → `dbx.Rebind`）

- **基线**：`68700fb9`（dev 顶端；2026-11-03 刷新——原基线 `f1a05780`（exec/213 顶端）已随 209–213 族 ff-only 并入 dev，此后 pkg/turn / pkg/gateway 测试侧仅纯增量（api_extra_test.go +205、slash_test.go +4，零删改），§2 的 25 条清点与 7 文件分布经重跑 grep 核实不变）。
- **状态**：DONE（2026-11-03 执行 + 评审 APPROVE；已合入 dev `9ea9f4a0`——主树 bulk commit `b8d27627` 后 rebase 干净、ff-only 合并、主树终态双包 ok、worktree 已清。判据 #3 的 PG/MySQL gateway 腿未全达——残余测试侧问题已转出 plan 218，见 `plans/README.md`）
- **优先级 / 工作量 / 风险**：P2 / S / LOW（只改 `_test.go`，不动产品代码；风险在于改动面广、需要三库矩阵确认）
- **依赖**：209（`dbx` 方言层）、213（`5ce6f559` 已修掉这条路径上的一个产品缺陷）。
- **执行隔离**：worktree + `exec/217` 分支 + 增量提交（owner 2026-11-03 裁定）；合并在 owner 批准后 ff-only。

> **执行者须知**：逐步执行，每步跑「验证」确认结果再进下一步。碰到 §7 STOP 条件就停下报告。完成后把 `plans/README.md` 里 217 那一行的状态改为 DONE 并写一句结果。
>
> **Drift check（先跑）**：在干净检出上 `git diff --stat 68700fb9 -- pkg/turn pkg/gateway` 应无输出；`grep -rnE '(SELECT|INSERT|UPDATE|DELETE)[^`"]*\?' --include='*_test.go' pkg/turn/ pkg/gateway/ | wc -l` 应为 **25**。任一不符就 STOP（说明基线已变，本计划的清点要重做）。

## 0. 为什么做

仓库从 209 起支持三种库（SQLite / MySQL 8.0+ / PostgreSQL 14+），`pkg/state/dbx` 提供方言层：`dbx.SQL{SQLite, MySQL, Postgres}` 三臂、`dbx.Placeholder`、`dbx.Rebind(d, sql)`。产品代码全部走这一层。

**测试没有全部跟上**。`pkg/turn` 与 `pkg/gateway` 里有 25 条 SQL 直接写裸 `?` 占位符。SQLite 与 MySQL 接受 `?`，**PostgreSQL 只认 `$1`/`$2`**，所以这些语句在 PG 上直接报 `syntax error at or near ","`（`SQLSTATE 42601`）。

后果（2026-11-03 在 `exec/213` 上实测）：`FOREBRAIN_TEST_DATABASE_URL` 指向 PG 时 `pkg/gateway` 与 `pkg/turn` 大面积失败，其中 **93 条错误是 `SQLSTATE 42601`**；由于若干裸 SQL 落在共享的测试辅助函数里（如 `writeConfigSection`，`pkg/gateway/api_extra_test.go:212`），一个辅助函数就带倒数以十计的用例，最终 **327 个用例失败**。

这直接削弱三库支持的可信度：**`pkg/gateway` 与 `pkg/turn` 的 PG/MySQL 行为从来没有被真正测过**。213 的集成测试正是在这条盲区里撞出一个真实产品缺陷（`ListResolvedWaitActionIDs` 在 PG 上 `SQLSTATE 42P10`，网关每次启动的审批恢复扫描都失败，见 `5ce6f559`）——那个缺陷本可以由矩阵提前发现，只因为调用它的既有用例用裸 `Open(...)` 只跑 SQLite。

## 1. 范围（只有测试文件）

**只动**：`pkg/turn/**/*_test.go`、`pkg/gateway/**/*_test.go`。

**不动**：任何产品代码；`pkg/state`、`pkg/memory`、`pkg/tool`、`pkg/process`、`pkg/migrate` 的测试（它们已经方言化）；`pkg/architecture` 的守卫与其白名单。

## 2. 现状事实（已对基线 `f1a05780` 核实）

25 条带 `?` 的 SQL，分布在 7 个文件：

| 文件 | 条数 |
|---|---|
| `pkg/gateway/api_extra_test.go` | 15 |
| `pkg/gateway/run_control_test.go` | 5 |
| `pkg/gateway/files_write_test.go` | 1 |
| `pkg/gateway/server_test.go` | 1 |
| `pkg/turn/approval_test.go` | 1 |
| `pkg/turn/events_test.go` | 1 |
| `pkg/turn/rewind_test.go` | 1 |

复现命令（应当只列出这 25 条）：

```bash
grep -rnE '(SELECT|INSERT|UPDATE|DELETE)[^`"]*\?' --include='*_test.go' pkg/turn/ pkg/gateway/
```

**这两个包确实会跑矩阵**：`OpenStateForTest` 在 `pkg/turn` 与 `pkg/gateway` 里共出现 **108 次**（如 `pkg/turn/events_test.go:190`、`pkg/turn/batch_test.go:34`），所以 `FOREBRAIN_TEST_DATABASE_URL` 一设，它们就在 MySQL/PG 上跑——这正是这些裸 SQL 会炸的原因。

**仓库既有惯例**（照抄，不要发明新写法）：

```go
// pkg/memory/store_test.go:48
_, err := store.DB.ExecContext(context.Background(), dbx.Rebind(dbx.DialectOf(store.DB), `INSERT INTO fb_sessions(...)
```

`dbx.Rebind(d, q)` 只对 `dbx.Postgres` 生效：把 `?` 依次换成 `$1`、`$2`……；SQLite 与 MySQL 原样返回。所以**一处改法对三种库都对**。

## 3. 设计

对每一条带 `?` 的测试 SQL，把裸字符串包进 `dbx.Rebind(dbx.DialectOf(<该库句柄>), <原 SQL>)`：

```go
// 改前
db.QueryRow(`SELECT finished_at_ms FROM fb_runs WHERE id=?`, child.ID)

// 改后
db.QueryRow(dbx.Rebind(dbx.DialectOf(db), `SELECT finished_at_ms FROM fb_runs WHERE id=?`), child.ID)
```

要点：

- **句柄就是该语句实际执行的那个 `*sql.DB`**（`db`、`s.Env.SQL`、`s.Sessions.DB()`、`g.server.RunRT.DB`……）。不要传别的库的句柄，也不要缓存 `DialectOf` 的结果到包级变量——测试里每个用例可能开不同的库。
- **SQL 文本一个字都不要改**：只加一层 `dbx.Rebind(...)` 包装。参数顺序、占位符个数、语义全部不变。
- 需要新 import `github.com/forebrain-harness/forebrain-harness/pkg/state/dbx` 的文件才加；`pkg/turn` 里的文件若已 import `state`，仍要单独加 `dbx`。
- `?` 出现在**字符串字面量或注释里**（不是占位符）的，不要包。逐条看一眼再改。
- 若某条 SQL 本身在 PG 上有别的语法问题（例如 213 撞到的 `SELECT DISTINCT … ORDER BY` 不在选择列表，`SQLSTATE 42P10`），那要按 §5 报告，不要顺手改 SQL 语义——除非该语句只在测试里用于取数、且改法等价（此时在执行记录里写清理由）。

## 4. 步骤

1. **`pkg/gateway/api_extra_test.go`（15 条）**。这是最大的一块，且含共享辅助函数（`writeConfigSection` 一带，`:212`/`:233`/`:242`），改完它的收益最大。
   验证：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -count=1` → ok（SQLite 不回归）。
2. **`pkg/gateway/run_control_test.go`（5）、`files_write_test.go`（1）、`server_test.go`（1）**。
   验证：同上 → ok。
3. **`pkg/turn` 三个文件（各 1 条）**。
   验证：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/turn -count=1` → ok。
4. **在 PostgreSQL 上跑这两个包**，把剩下的失败逐条归因。
   验证：
   ```bash
   FOREBRAIN_HOME="$(mktemp -d)" FOREBRAIN_TEST_DATABASE_URL="postgres://postgres:fb@127.0.0.1:15432/postgres?sslmode=disable" \
     CGO_ENABLED=1 go test -tags fts5 ./pkg/turn ./pkg/gateway -count=1
   ```
   → 应当 **ok**。仍有失败时：**`SQLSTATE 42601`** ⇒ 还有裸占位符没找到（回到步骤 1–3，并重新跑 §2 的 grep 确认是 0）；**其它 SQLSTATE** ⇒ 可能是另一个产品缺陷，按 §5 记录并**停下报告**，不要在本计划里改产品代码。
5. **在 MySQL 上跑一遍**（`mysql://root:fb@127.0.0.1:13306/fbt`）。MySQL 接受 `?`，这一步主要是确认没有把 MySQL 改坏。
   验证：同命令换 URL → ok。
6. **收口**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./... -count=1 -p 2` → 全绿（SQLite 全量）；`go vet -tags fts5 ./pkg/...` → 无输出；`go test ./pkg/architecture/ -run 'TestSQLiteStatementsUnchanged' -count=1` → ok（本计划只改测试，快照不该动）。

## 5. 边界与「发现产品缺陷时怎么办」

- **只动** §1 列出的两个包的测试文件。任何产品代码改动都需要停下来报告。
- 若 PG 上出现**非 42601** 的失败：那是产品缺陷或测试自身别的方言假设（例如硬编码 SQLite 的类型亲和、`STRICT` 表行为、`INSERT … RETURNING`）。**记录现象、SQLSTATE、复现命令、最小用例，停下报告**——它应该变成一张新计划（或并入既有计划），不塞进本计划。
- 不要为了「让测试过」而放宽断言或跳过用例。任何 `t.Skip` 都需要在报告里逐条说明理由。

## 6. 完成判据（机器可查）

```bash
# 1) 裸占位符清零
grep -rnE '(SELECT|INSERT|UPDATE|DELETE)[^`"]*\?' --include='*_test.go' pkg/turn/ pkg/gateway/ | wc -l    # → 0

# 2) SQLite 全量不回归
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./... -count=1 -p 2                      # → 全绿

# 3) 这两个包在三种库上都绿
for url in '' 'mysql://root:fb@127.0.0.1:13306/fbt' 'postgres://postgres:fb@127.0.0.1:15432/postgres?sslmode=disable'; do
  FOREBRAIN_HOME="$(mktemp -d)" FOREBRAIN_TEST_DATABASE_URL="$url" CGO_ENABLED=1 \
    go test -tags fts5 ./pkg/turn ./pkg/gateway -count=1
done                                                                                                     # → 三次 ok

# 4) 守卫未被动过
go test ./pkg/architecture/ -run 'TestSQLiteStatementsUnchanged' -count=1                                # → ok
go vet -tags fts5 ./pkg/...                                                                              # → 无输出
```

（每条 Go 命令都带 `FOREBRAIN_HOME="$(mktemp -d)"`：house rule 禁止测试继承环境里的 `FOREBRAIN_HOME`；`pkg/process` 的 `TestApplyComposedConfigRollsBackEveryFieldWhenRunnerLoadFails` 尤其会因此假失败。）

## 7. STOP 条件

- Drift check 不符（25 条不再是 25 条）——基线已变，清点要重做。
- 需要改任何产品代码才能让矩阵通过。
- PG 上出现非 `42601` 的失败，且无法用「测试自身的方言假设」解释。
- 某条 SQL 的 `?` 其实是字符串字面量内容，包上 `Rebind` 会改变语义（逐条确认时遇到）。
- 改动导致 SQLite 全量出现新的失败。

## 8. 维护注意

- 今后在 `pkg/turn` / `pkg/gateway` 的测试里写 SQL：一律 `dbx.Rebind(dbx.DialectOf(db), …)`，即使当下只跑 SQLite——这个包已经会跑矩阵，裸 `?` 会在下一次矩阵运行时炸。
- 仓库里其余包的测试（`pkg/state`、`pkg/memory` 等）已经是这个写法，可作范例。
- 本计划不覆盖 `pkg/gateway` 的 **e2e**（`frontend/e2e/*.spec.ts`）：那些跑的是真实网关，走产品代码的方言层，不需要改。
