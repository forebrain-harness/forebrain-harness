# 211 — 其余存储语句：项目、任务、定时任务、配置、文件、产物、渠道、进程层、网关、导入

- **基线**：`06f1bc57`（dev），并假定 203–210 已落地。
- **状态**：TODO
- **优先级 / 工作量 / 风险**：P1 / M / MED（约 103 处语句）
- **依赖**：210 DONE。之后是 212。
- **设计**：`docs/plan/MULTI_DB_MULTI_REPLICA_DESIGN.md` §2.2、§2.3、§2.9（**改写规则表必读**）；写法与 `plans/210-session-run-statements.md` §2 完全相同（三联 `dbx.SQL`、`dbx.Placeholders`、事务外写走 `dbx.Exec`、PG 参数按 SQLite 的 `?` 顺序编号、upsert / RETURNING / 裸列 / NULL 排序 / 保留字的处理）。
- **执行隔离**：基于 dev HEAD 的 worktree 或主树 in-tree；**零 commit**（owner 手动提交）。

> **执行者须知**：先读 210 的 §2「写法」，本计划不重复解释。逐文件执行，每个文件改完跑一次「验证」。碰到 §8 STOP 条件就停下报告。完成后把 `plans/README.md` 里 211 那一行的状态改为 DONE 并写一句结果。
>
> **Drift check（先跑）**：README 里 210 是 DONE；`go test ./pkg/architecture/ -run TestSQLiteStatementsUnchanged -count=1` 通过。

## 0. owner 裁决（执行期不得改）

1. **禁止任何 ORM**；**SQLite 的 SQL 一句不改**；MySQL、PostgreSQL 各写一套，运行时按库类型调用。
2. 三种库上结果必须等价。
3. 不搬旧数据（`pkg/migrate` 是「从外部工具导入会话」，不是 SQLite→MySQL 搬迁，照常翻译）。

## 1. 范围

| 文件 | 语句处 | 需特别处理的位置（基线行号，以 grep 为准） |
|---|---|---|
| `pkg/state/project_store.go` | 22 | upsert 553；`LIKE` 819；`COLLATE NOCASE` 840；唯一索引 `idx_fb_projects_agent_root_unique(agent_id, root)`——MySQL 结构里若用了哈希生成列（209），这里的冲突判断按 209 的 `dbx.IsUniqueViolation` |
| `pkg/state/task_store.go` | 20 | `LIKE` 565；`IN (…)` 478、488；裸列嫌疑 604 |
| `pkg/state/cron.go` | 27 | upsert 542；`IN (…)` 510、515；`fb_cron_runs.trigger` 列（MySQL 保留字，279 行的 INSERT 等处加反引号） |
| `pkg/state/config_store.go` | 12 | upsert 147、220、261；`UpdateSection` 的读改写事务（209 已改 `dbx.BeginTx`，全局写锁保证互斥） |
| `pkg/state/file_service.go` | 6 | `IN (…)` 801 |
| `pkg/state/artifact_store.go` | 5 | `rowid` 141、173（用 209 在结构里加的显式自增列） |
| `pkg/state/project_channel_store.go` | 7 | upsert 212；部分唯一索引 `idx_fb_project_channels_account … WHERE account_key <> ''`（MySQL 是 `NULLIF` 生成列，冲突目标要对应改） |
| `pkg/state/db.go` | 4 | 只有 SQLite 构建检查（`sqlite_compileoption_used`）——只在 SQLite 下执行，MySQL / PG 跳过，不需要翻译 |
| `pkg/process/config_credential_sync.go` | 2 | — |
| `pkg/process/config_import.go` | 2 | — |
| `pkg/process/runner_pool.go` | 1 | — |
| `pkg/gateway/session_files.go` | 1 | — |
| `pkg/migrate/ledger.go` | 6 | upsert 203 |
| `pkg/migrate/sessions.go` | 9 | 3 个事务（209 已改 `dbx.BeginTx`） |

**不在范围**：`pkg/memory`、`pkg/tool/loaded_skills.go`（212）。

## 2. 步骤

每个文件改完都跑：

```bash
CGO_ENABLED=1 go build -tags fts5 ./... && go vet -tags fts5 ./pkg/...
go test ./pkg/architecture/ -run TestSQLiteStatementsUnchanged -count=1
CGO_ENABLED=1 go test -tags fts5 ./pkg/state/ ./pkg/process/ ./pkg/gateway/ ./pkg/migrate/ -count=1 -p 2
```

1. `artifact_store.go`、`file_service.go`、`project_channel_store.go`、`config_store.go`。
2. `project_store.go`、`task_store.go`、`cron.go`。
3. `pkg/process` 三个文件、`pkg/gateway/session_files.go`。
4. `pkg/migrate` 两个文件。
5. 三库矩阵（§4）。

## 3. 测试

- 现有测试经 209 的分派直接在 MySQL / PG 上跑。
- 补最小用例（三库都跑）：项目名大小写不敏感的查找（`NOCASE`）；渠道账号空串可重复、非空唯一（部分唯一索引）；`fb_cron_runs` 写入带 `trigger` 列。

## 4. 完成判据（机器可查）

```bash
for url in 'mysql://root:fb@127.0.0.1:13306/fbt' 'postgres://postgres:fb@127.0.0.1:15432/postgres?sslmode=disable'; do
  FOREBRAIN_TEST_DATABASE_URL="$url" CGO_ENABLED=1 go test -tags fts5 ./pkg/state/... ./pkg/process/... ./pkg/migrate/... -count=1 -p 2
done
CGO_ENABLED=1 go test -tags fts5 ./... -count=1 -p 2        # SQLite 全量全绿
go test ./pkg/architecture/ -run TestSQLiteStatementsUnchanged -count=1
```

`pkg/state` 在两种库上**必须全绿**（210 遗留的暂时失败用例此时应全部转绿；还剩的只能是调用了 `pkg/memory` / `pkg/tool` 语句的用例，列进 README 结果，212 收口）。`pkg/process`、`pkg/migrate` 同理。

## 5. 维护注意

- `pkg/process` 的测试若直接 `sql.Open` 了 SQLite（没走 `OpenStateForTest`），在矩阵里仍是 SQLite——列出这些测试，评估是否改走 `OpenStateForTest`（只改测试的打开方式，不改断言）。

## 6. 不做

- 不改结构；不优化语句。

## 8. STOP 条件

- upsert 的冲突目标在 MySQL 结构里对不上（尤其是两处部分唯一 / 哈希唯一索引）。
- `pkg/migrate` 的导入依赖 SQLite 专有行为（如 `INSERT OR REPLACE` 的删除再插入语义）且三家无法等价。
- 210 遗留的失败用例在本计划结束时仍因 `pkg/state` 内的语句失败。
