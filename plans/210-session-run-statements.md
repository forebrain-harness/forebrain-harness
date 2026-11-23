# 210 — 会话与运行相关语句：MySQL / PostgreSQL 版本

- **基线**：`06f1bc57`（dev），并假定 203–209 已落地。
- **状态**：TODO
- **优先级 / 工作量 / 风险**：P1 / L / MED-HIGH（约 156 处语句，覆盖对话、消息、事件、运行归属、撤回、审批动作）
- **依赖**：209 DONE（`dbx`、两份结构文件、测试矩阵、SQLite 语句快照）。之后是 211。
- **设计**：`docs/plan/MULTI_DB_MULTI_REPLICA_DESIGN.md` §2.2、§2.3、§2.9（**改写规则表必读，逐条遵守**）。
- **执行隔离**：基于 dev HEAD 的 worktree 或主树 in-tree；**零 commit**（owner 手动提交）。

> **执行者须知**：逐文件执行，每个文件改完跑一次「验证」。碰到 §8 STOP 条件就停下报告。完成后把 `plans/README.md` 里 210 那一行的状态改为 DONE 并写一句结果。
>
> **Drift check（先跑）**：README 里 209 是 DONE；`go test ./pkg/architecture/ -run TestSQLiteStatementsUnchanged -count=1` 通过；`ls pkg/state/dbx pkg/state/schema_mysql.sql pkg/state/schema_postgres.sql` 都在。

## 0. owner 裁决（执行期不得改）

1. **禁止任何 ORM**；**SQLite 的 SQL 一句不改**；MySQL、PostgreSQL 各写一套，运行时按库类型调用。
2. 三种库上**结果必须等价**（同样的行、顺序、空值）。
3. 最低版本 MySQL 8.0、PostgreSQL 14。

## 1. 范围

| 文件 | 语句处 | 需特别处理的位置（行号为 209 落地前的基线位置，以 grep 为准） |
|---|---|---|
| `pkg/state/session_store.go` | 75 | upsert 269、3063、3141、3246、3386、3472；`INSERT OR` 2765、2770、2997；`LIKE` 870、1000、1072；JSON 函数 573-579；`IN (…)` 动态列表 375 起十余处；带聚合的裸列嫌疑 1611、3034；`rowid` 3031 |
| `pkg/state/runrt.go` | 41 | upsert 595、1203；`RETURNING` 1306、1319；`IFNULL` 474-476、529；`WITH RECURSIVE` 394、469、524、752；`rowid` 1175 |
| `pkg/state/session_events.go` | 9 | upsert 113 + `RETURNING` 114；`IFNULL` 143、335、336、350、399 |
| `pkg/state/rewind.go` | 13 | `IFNULL` 339-373；裸列嫌疑 366 |
| `pkg/state/message_sync.go` | 0（只有事务） | 209 已改 `dbx.BeginTx`，核对即可 |
| `pkg/state/llm_attempts.go` | 4 | `rowid` 130、163；`WITH RECURSIVE` 161 |
| `pkg/state/session_ui_state.go` | 2 | upsert 44 |
| `pkg/state/action_service.go` | 12 | `RETURNING` 370；`IFNULL` 155 |

**不在范围**：其余 `pkg/state` 文件（211）、`pkg/memory`（212）、`schema_migrations.go` 的历史迁移（只服务 SQLite）。

## 2. 写法（照抄这个形状）

```go
q := dbx.SQL{
	SQLite: `SELECT id, title FROM fb_sessions WHERE agent_id=? AND id=?`, // 原文，一个字符都不动
	MySQL:  `SELECT id, title FROM fb_sessions WHERE agent_id=? AND id=?`,
	Postgres: `SELECT id, title FROM fb_sessions WHERE agent_id=$1 AND id=$2`,
}
row := s.DB.QueryRowContext(ctx, q.For(dbx.DialectOf(s.DB)), agentID, id)
```

- 语句挪进 `SQLite:` 字段时**整段剪切粘贴**，不改缩进以外的任何字符（快照测试按文本比较；若 209 的快照对缩进敏感，以快照测试为准）。
- 同一函数里多条语句：在函数开头取一次 `d := dbx.DialectOf(s.DB)`（事务里用 `dbx.DialectOf(tx)`）。
- **动态 `IN (…)`**：在 `dbx` 加 `func Placeholders(d Dialect, start, n int) string`（SQLite / MySQL 返回 `?,?,…`；PG 返回 `$start,…`），拼接处三种方言各拼一次；SQLite 分支继续用原来的拼法（原拼法不动）。
- **`RETURNING`**：MySQL 分支改成同一事务里「写 → `SELECT`」；若原语句不在事务里，用 `dbx.BeginTx` 包起来（SQLite 分支保持原样、不包）。自增主键用 `LAST_INSERT_ID()`。
- **upsert**：MySQL 用 `ON DUPLICATE KEY UPDATE c=VALUES(c)`；先确认冲突目标在 MySQL 结构里是主键或唯一索引（不是就 STOP）；带 `WHERE` 的 `DO UPDATE` 改成 `c=IF(条件, VALUES(c), c)`，条件里引用旧值直接写列名、引用新值写 `VALUES(c)`。
- **事务外的写**：MySQL / PG 分支一律经 `dbx.Exec`（设计 §2.3），SQLite 分支照旧 `ExecContext`：

```go
if d == dbx.SQLite {
	_, err = s.DB.ExecContext(ctx, q.SQLite, args...)
} else {
	_, err = dbx.Exec(ctx, s.DB, q.For(d), pgOrMySQLArgs...)
}
```

  （若 209 的 `dbx.Exec` 已按方言自动处理 SQLite，则统一写 `dbx.Exec(ctx, s.DB, q.For(d), args...)`——以 209 的实际 API 为准。）
- **PG 参数复用**：同一个值在 SQLite 语句里出现两次（两个 `?`），PG 分支可以写 `$1` 两次只传一次——但参数列表就和 SQLite 不同了；为降低出错，**PG 分支按 SQLite 的 `?` 顺序逐个编号、参数列表与 SQLite 完全相同**。
- **裸列聚合**（`SELECT MAX(x), y …` 取最大那行的 `y`）：先确认是否真依赖 SQLite 语义（`y` 不在 `GROUP BY` 里且不是聚合）；是 → MySQL / PG 用 `ROW_NUMBER() OVER (…)` 子查询改写；不是 → 照常翻译。
- **NULL 排序**：`ORDER BY` 的列可空时，PG 分支写明 `NULLS FIRST`（升序）/ `NULLS LAST`（降序）。
- **保留字列**：`key`、`trigger` 在 MySQL 分支加反引号（以 209 保留字测试的清单为准）。

## 3. 步骤

每个文件改完都跑：

```bash
CGO_ENABLED=1 go build -tags fts5 ./... && go vet -tags fts5 ./pkg/state/...
go test ./pkg/architecture/ -run TestSQLiteStatementsUnchanged -count=1     # SQLite 语句集合不变
CGO_ENABLED=1 go test -tags fts5 ./pkg/state/ -count=1                     # SQLite 全绿
```

1. `session_ui_state.go`、`llm_attempts.go`（小文件，先熟悉写法）。
2. `session_events.go`、`action_service.go`、`rewind.go`。
3. `runrt.go`。
4. `session_store.go`（最大，分函数逐个改）。
5. `message_sync.go` 核对。
6. 三库矩阵（§5）。

## 4. 测试

- 不新增业务测试：现有 `pkg/state` 测试经 209 的 `OpenStateForTest` 分派后直接在 MySQL / PG 上跑，就是等价性测试。
- 若某个 SQLite 专有语义（`LIKE` 大小写、NULL 排序、裸列）在现有测试里没有覆盖，在对应 `_test.go` 里补一条最小用例（三库都跑）。

## 5. 完成判据（机器可查）

```bash
tests=$(grep -ho '^func Test[A-Za-z0-9_]*' pkg/state/{session_store,runrt,session_events,rewind,message_sync,llm_attempts,session_ui_state,action_service}_test.go 2>/dev/null | sed 's/^func //' | paste -sd'|' -)
for url in 'mysql://root:fb@127.0.0.1:13306/fbt' 'postgres://postgres:fb@127.0.0.1:15432/postgres?sslmode=disable'; do
  FOREBRAIN_TEST_DATABASE_URL="$url" CGO_ENABLED=1 go test -tags fts5 ./pkg/state/ -run "^($tests)$" -count=1
done
CGO_ENABLED=1 go test -tags fts5 ./... -count=1 -p 2        # SQLite 全量全绿
go test ./pkg/architecture/ -run TestSQLiteStatementsUnchanged -count=1
```

上面两个矩阵命令里，**因调用了 211 / 212 范围内尚未翻译的语句而 panic 的用例**（panic 信息里是 `dbx.SQL.For` 的空方言）允许暂时失败——把这些用例名与它们碰到的文件列进 README 结果，211 / 212 完成时必须全部转绿。其它失败一律不允许。

## 6. 维护注意

- 210–212 期间不要合并别的改动同一批文件的计划（SQLite 语句快照会冲突）。
- 新增的 `dbx.Placeholders` 211 / 212 会复用。

## 7. 不做

- 不改表结构（209 已建好）；发现结构不够用（例如缺显式自增列），STOP。
- 不优化语句；只翻译。

## 8. STOP 条件

- upsert 的冲突目标在 MySQL 结构里不是主键 / 唯一索引。
- 某条 SQLite 语句依赖的语义在 MySQL / PG 上无法等价表达（列出语句与原因）。
- 快照测试因挪动语句而失败、且原因不是缩进。
- `RETURNING` 改写需要的事务会和调用方已有事务嵌套。
