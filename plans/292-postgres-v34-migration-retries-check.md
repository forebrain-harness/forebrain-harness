# Plan 292: PostgreSQL v34 迁移在「列已存在但约束缺失」时补上 CHECK

> **Executor instructions**：按步骤执行，每一步都跑完验证命令并确认预期输出再进入下一步。
> 遇到「STOP 条件」里任意一条就停下报告，不要自行发挥。完成后更新 `plans/README.md`
> 里本计划的状态行（除非派发你的评审者说他维护索引）。
>
> **Drift check（开工第一步）**：
> `cd /Users/doudou/workspace/unionj-cloud/forebrain-harness && git status --porcelain -- pkg/state/schema_migrations.go pkg/state/schema_migrations_test.go && git diff d715dafd -- pkg/state/schema_migrations.go pkg/state/schema_migrations_test.go`
> 若两个文件在 `d715dafd` 之后已有改动，把「当前状态」的摘录与活代码比对；对不上即 STOP。
> `plans/` 被 `.gitignore` 忽略，不能用 `git status` 干净推断计划文件没动过。

## Status

- **Priority**: P2
- **Effort**: S
- **Risk**: LOW
- **Depends on**: none
- **Category**: bug
- **Planned at**: commit `d715dafd`, 2026-11-11

## Why this matters

v34 迁移给 `fb_llm_models` 加 `subagent_allowed`。SQLite 与 MySQL 都是**单条** `ADD COLUMN ... CHECK (...)`，列与约束同生共死；PostgreSQL 版本却拆成了两条独立 `ExecContext`：先 `ADD COLUMN`，再 `ADD CONSTRAINT fb_llm_models_subagent_allowed_check`。方言迁移循环没有事务包裹，版本只在全部迁移成功后才写入（`pkg/state/schema_dialect.go:107-119`）。

于是若进程在两条 DDL 之间崩溃 / 连接断开，列已落地、CHECK 没建、版本仍停在 33；下次启动重试时，函数开头「列存在就 `return nil`」会直接跳过，约束**永久缺失**。任何本来就没有该命名约束的库（手工加过列、旧版本遗留）也会被静默接受。`subagent_allowed` 因此失去 `0/1` 取值保护，升级库与全新库（`schema_postgres.sql:684` 的 inline CHECK）对象不一致。现有测试 `TestStateV33UpgradesToV34AddsSubagentAllowed` 的「列已存在时重跑不报错」分支恰好把这个缺口当成通过。

## Current state

仓库：`/Users/doudou/workspace/unionj-cloud/forebrain-harness`（Go 1.26，CGO，`-tags fts5`；状态库三方言 SQLite / MySQL 8 / PostgreSQL 14）。

- `pkg/state/schema_migrations.go:1739-1795`（三份 v34 迁移）：
  ```go
  func migrateStateV33ToV34(ctx context.Context, conn *sql.Conn) error {
      var present int
      if err := conn.QueryRowContext(ctx,
          `SELECT COUNT(*) FROM pragma_table_info('fb_llm_models') WHERE name='subagent_allowed'`).Scan(&present); err != nil {
          return err
      }
      if present > 0 { return nil }
      if _, err := conn.ExecContext(ctx,
          `ALTER TABLE fb_llm_models ADD COLUMN subagent_allowed INTEGER NOT NULL DEFAULT 0 CHECK (subagent_allowed IN (0, 1))`); err != nil {
          return fmt.Errorf("add fb_llm_models.subagent_allowed: %w", err)
      }
      return nil
  }
  // ... MySQL 同形，单条 ADD COLUMN ... CHECK ...

  func migrateStateV33ToV34Postgres(ctx context.Context, conn *sql.Conn) error {
      var present int
      if err := conn.QueryRowContext(ctx,
          `SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = CURRENT_SCHEMA() AND table_name = 'fb_llm_models' AND column_name = 'subagent_allowed'`).Scan(&present); err != nil {
          return err
      }
      if present > 0 {
          return nil
      }
      if _, err := conn.ExecContext(ctx,
          `ALTER TABLE fb_llm_models ADD COLUMN subagent_allowed BIGINT NOT NULL DEFAULT 0`); err != nil {
          return fmt.Errorf("add fb_llm_models.subagent_allowed: %w", err)
      }
      if _, err := conn.ExecContext(ctx,
          `ALTER TABLE fb_llm_models ADD CONSTRAINT fb_llm_models_subagent_allowed_check CHECK (subagent_allowed IN (0, 1))`); err != nil {
          return fmt.Errorf("add fb_llm_models.subagent_allowed check: %w", err)
      }
      return nil
  }
  ```
  函数注释（`:1774-1777`）写明：约束名取 `schema.sql` inline CHECK 会得到的名字，让升级库与全新库持有同样的对象 —— 当前实现对「全新迁移」成立，对「重试 / 半迁移」不成立。

- `pkg/state/schema_dialect.go:107-119`：迁移循环逐条 `apply(ctx, conn)`，失败即返回；**没有事务**；成功后才 `writeDialectSchemaVersion(...)`。
- `pkg/state/schema_postgres.sql:673-684`：`subagent_allowed BIGINT NOT NULL DEFAULT 0` 与表级 `CHECK (subagent_allowed IN (0, 1))`。
- `pkg/state/schema_migrations_test.go:4143 起`：`TestStateV33UpgradesToV34AddsSubagentAllowed` 用 `openStateDB(t)`（SQLite 默认，`FOREBRAIN_TEST_DATABASE_URL` 指向服务器时切成 MySQL/PG，见 `pkg/state/db.go:249-284`），末尾三段是「重跑已有列不报错」：
  ```go
  switch dialect {
  case dbx.SQLite:
      if err := migrateStateV33ToV34(ctx, conn); err != nil { ... }
  case dbx.MySQL:
      if err := migrateStateV33ToV34MySQL(ctx, conn); err != nil { ... }
  case dbx.Postgres:
      if err := migrateStateV33ToV34Postgres(ctx, conn); err != nil { ... }
  }
  ```
- 三方言测试入口：`scripts/test-db.sh postgres`（脚本会起 `fb-pg` 容器并设置 `FOREBRAIN_TEST_DATABASE_URL`，URL 见脚本头部 `postgres_url`）。

仓库约定：
- 每个迁移函数在自身方言里自洽；不要给 SQLite / MySQL 加无意义的约束查询。
- MySQL 的 DDL 隐式提交，**不要**给迁移循环套事务。
- 迁移必须可重入（column-exists 检查存在的原因）；新增的判断也必须可重入。
- 若新增 SQL 语句导致 `TestSQLiteStatementsUnchanged` / SQL 快照金样变化，按 `go run ./scripts/sqlite-sql-snapshot -out pkg/architecture/testdata/sqlite_sql_snapshot.json` 再生 —— 但本计划只动 **Postgres 专用函数**，正常情况下不应触发；若触发，先 STOP 报告。
- `gofmt` 干净；注释用英文。

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| 定向测试（SQLite） | `CGO_ENABLED=1 go test -tags fts5 ./pkg/state -run '^TestStateV34PostgresMigrationRepairsAMissingCheck$' -count=1 -v` | `--- SKIP: ...`（无 PG 服务器时，见下） |
| 定向测试（PG） | `scripts/test-db.sh up && FOREBRAIN_TEST_DATABASE_URL='postgres://postgres:fb@127.0.0.1:15432/postgres?sslmode=disable' CGO_ENABLED=1 go test -tags fts5 ./pkg/state -run '^TestStateV34PostgresMigrationRepairsAMissingCheck$' -count=1 -v` | `--- PASS` |
| 方言矩阵 | `scripts/test-db.sh postgres` | 相关 engine arm 全部 PASS |
| 回归包 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/state ./pkg/architecture -count=1` | 全部 `ok` |
| 构建 / Vet / 格式 | `CGO_ENABLED=1 go build -tags fts5 ./...` / `CGO_ENABLED=1 go vet -tags fts5 ./...` / `gofmt -l pkg cmd` | exit 0 / 无输出 / 无输出 |

## Scope

**In scope**（只允许改这两个文件）：
- `pkg/state/schema_migrations.go` —— 只改 `migrateStateV33ToV34Postgres`。
- `pkg/state/schema_migrations_test.go` —— 新增一条测试。

**Out of scope**（不许动）：
- `migrateStateV33ToV34`（SQLite）与 `migrateStateV33ToV34MySQL` —— 它们单条 DDL 已自洽。
- `pkg/state/schema*.sql`、`pkg/state/schema_dialect.go`、迁移版本号与 `stateSchemaMigrations` 表。
- `pkg/architecture/testdata/sqlite_sql_snapshot.json`（除非 STOP 条件里说明的金样变化被确认）。
- `subagent_allowed` 的默认值、授权语义、投影与指纹 —— 计划 283 已定稿。

## Git workflow

- 按 `plans/README.md` 既有约定：**in-tree、零 commit**，改动留在工作区由 owner 审阅。不要自行 `git merge` / `rebase` / `push` / 删 worktree。
- 若 owner 另行要求提交：Conventional Commits 标题，如 `fix(state): let the postgres v34 migration restore a missing check constraint`；subject 小写开头、≤100 字符，`git commit -s`（DCO）。

## Steps

### Step 1: 先写会红的 PostgreSQL 回归测试

在 `pkg/state/schema_migrations_test.go` 末尾新增：

```go
func TestStateV34PostgresMigrationRepairsAMissingCheck(t *testing.T) {
    ctx := context.Background()
    db := openStateDB(t)
    if dbx.DialectOf(db) != dbx.Postgres {
        t.Skipf("requires %s pointing at PostgreSQL", testDatabaseURLVar)
    }
    conn, err := db.Conn(ctx)
    if err != nil { t.Fatal(err) }
    defer conn.Close()
    // Simulate the crash between the two ALTERs of the v34 postgres migration:
    // the column is there, the named CHECK is not.
    if _, err := conn.ExecContext(ctx,
        `ALTER TABLE fb_llm_models DROP CONSTRAINT IF EXISTS fb_llm_models_subagent_allowed_check`); err != nil {
        t.Fatalf("drop the check: %v", err)
    }
    if err := migrateStateV33ToV34Postgres(ctx, conn); err != nil {
        t.Fatalf("retry the v34 migration: %v", err)
    }
    var present int
    if err := conn.QueryRowContext(ctx,
        `SELECT COUNT(*) FROM pg_constraint WHERE conname = 'fb_llm_models_subagent_allowed_check' AND conrelid = 'fb_llm_models'::regclass`).Scan(&present); err != nil {
        t.Fatal(err)
    }
    if present != 1 { t.Fatalf("named check constraints = %d, want 1", present) }
    // Re-running stays a no-op rather than failing on a duplicate constraint.
    if err := migrateStateV33ToV34Postgres(ctx, conn); err != nil {
        t.Fatalf("re-run the v34 migration: %v", err)
    }
}
```

`testDatabaseURLVar` 与 `dbx`、`context` 在本测试文件已有引用（见 `schema_dialect_test.go` 同包用法）；若本文件未 import `dbx`，按现有 import 风格补上，不要新增包依赖边。

**Verify（预期红）**：在有 PG 服务器的环境跑上面「定向测试（PG）」命令 → **FAIL**，因为当前 `present > 0` 直接 `return nil`，约束一直是 0。把红输出记进执行报告。无 PG 服务器时先跑 `scripts/test-db.sh up`；若 Docker 不可用，见 STOP 条件。

### Step 2: 把「列」与「约束」拆成两个独立的幂等判断

改写 `migrateStateV33ToV34Postgres`：保留列存在查询，但把 `if present > 0 { return nil }` 改成「只在列不存在时才 ADD COLUMN」，然后**无论列是否新加**都检查命名约束是否存在，缺则补：

```go
func migrateStateV33ToV34Postgres(ctx context.Context, conn *sql.Conn) error {
    var present int
    if err := conn.QueryRowContext(ctx,
        `SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = CURRENT_SCHEMA() AND table_name = 'fb_llm_models' AND column_name = 'subagent_allowed'`).Scan(&present); err != nil {
        return err
    }
    if present == 0 {
        if _, err := conn.ExecContext(ctx,
            `ALTER TABLE fb_llm_models ADD COLUMN subagent_allowed BIGINT NOT NULL DEFAULT 0`); err != nil {
            return fmt.Errorf("add fb_llm_models.subagent_allowed: %w", err)
        }
    }
    // A crash between the two ALTERs leaves the column without the named
    // CHECK, and the next start would otherwise skip the constraint forever.
    var checked int
    if err := conn.QueryRowContext(ctx,
        `SELECT COUNT(*) FROM pg_constraint WHERE conname = 'fb_llm_models_subagent_allowed_check' AND conrelid = 'fb_llm_models'::regclass`).Scan(&checked); err != nil {
        return err
    }
    if checked > 0 {
        return nil
    }
    if _, err := conn.ExecContext(ctx,
        `ALTER TABLE fb_llm_models ADD CONSTRAINT fb_llm_models_subagent_allowed_check CHECK (subagent_allowed IN (0, 1))`); err != nil {
        return fmt.Errorf("add fb_llm_models.subagent_allowed check: %w", err)
    }
    return nil
}
```

保持函数注释里「升级库与全新库持有同样对象」那句，并补一句说明重试语义。不要合并成单条 DDL —— 列已存在时无法再 `ADD COLUMN ... CHECK`；这正是要分开的原因。

**Verify**：
- 有 PG：`FOREBRAIN_TEST_DATABASE_URL=... CGO_ENABLED=1 go test -tags fts5 ./pkg/state -run '^TestStateV34PostgresMigrationRepairsAMissingCheck$' -count=1 -v` → `--- PASS`。
- 无 PG：至少跑 `CGO_ENABLED=1 go test -tags fts5 ./pkg/state -count=1` → `ok`，并在报告里注明 PG 专测 SKIP（见 STOP 条件）。
- `grep -n 'pg_constraint' pkg/state/schema_migrations.go` 有 1 处命中。

### Step 3: 收口回归

**Verify**（全部要跑）：
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/state ./pkg/architecture -count=1` → 全部 `ok`（确认 `TestSQLiteStatementsUnchanged` 没被新 SQL 影响）
- `scripts/test-db.sh postgres` → 相关 arm 全 PASS（若 Docker 可用）
- `CGO_ENABLED=1 go build -tags fts5 ./...` → exit 0
- `CGO_ENABLED=1 go vet -tags fts5 ./...` → 无输出
- `gofmt -l pkg cmd` → 无输出

## Test plan

- 新测试：`pkg/state/schema_migrations_test.go` 的 `TestStateV34PostgresMigrationRepairsAMissingCheck`，覆盖：
  - 半迁移状态（列在、命名约束不在）→ 重跑迁移补上约束；
  - 幂等：再跑一次不报重复约束错，约束仍恰好 1 个；
  - 非 PG 方言 `t.Skip`，不产生假绿。
- 既有测试 `TestStateV33UpgradesToV34AddsSubagentAllowed`（`:4143`）与「重跑已有列」分支必须继续通过（全新迁移路径不变）。
- 结构样板：同文件 `:4143 起`，以及 `pkg/state/schema_dialect_test.go:283` 附近的方言 skip 写法。
- 验证：Step 1 红、Step 2 绿；禁止用 `-run` 零匹配或 `t.Skip` 当通过证据。

## Done criteria

- [ ] `grep -c 'present > 0' pkg/state/schema_migrations.go` 不为 0 但 Postgres 函数内**没有** `if present > 0 { return nil }`（用 `sed -n '1777,1810p' pkg/state/schema_migrations.go` 目视确认）
- [ ] `grep -n 'pg_constraint' pkg/state/schema_migrations.go` 有 1 处命中
- [ ] `scripts/test-db.sh postgres` 中 `TestStateV34PostgresMigrationRepairsAMissingCheck` 为 `--- PASS`（不是 SKIP、不是零匹配）。**若环境没有 Docker / PG，本计划不得标 DONE**：把 SQLite 回归结果、SKIP 证据与「PG 专测未真跑」写进报告，状态置 **BLOCKED**（被改的函数是 PG 专用，SQLite 侧无法证明它）
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/state ./pkg/architecture -count=1` 全 `ok`
- [ ] `CGO_ENABLED=1 go build -tags fts5 ./...`、`CGO_ENABLED=1 go vet -tags fts5 ./...`、`gofmt -l pkg cmd` 全绿
- [ ] `git status --porcelain -- pkg/state` 只列出 `schema_migrations.go` 与 `schema_migrations_test.go`
- [ ] `plans/README.md` 中本计划状态行已更新

## STOP conditions

- Drift check 显示两个文件在 `d715dafd` 之后被改过，且摘录对不上。
- 新 SQL 让 `TestSQLiteStatementsUnchanged` / `TestStatementsHaveDialectArms` 变红，或要求再生 `pkg/architecture/testdata/sqlite_sql_snapshot.json` —— 本计划只动 Postgres 专用函数，正常不该发生；先报告，不要自行再生金样。
- `pg_constraint` 查询在目标 PG 版本上报 `regclass` 解析错误，或新鲜库的 inline CHECK 名字不是 `fb_llm_models_subagent_allowed_check` —— 说明命名假设不成立，先报告再定查询方式。
- 修复看起来需要给 SQLite / MySQL 也加同样判断，或需要包裹事务 —— 那是不同方言的 DDL 行为，先报告。
- 环境没有 Docker 且 `FOREBRAIN_TEST_DATABASE_URL` 未设置：不要声称 PG 专测通过，也不要把本计划标 DONE。把 SQLite 回归结果与 SKIP 证据写进报告，状态置 **BLOCKED**（原因：PG 专测没有真跑）。若 owner 提供了 PG 服务器地址，再用 `FOREBRAIN_TEST_DATABASE_URL=… CGO_ENABLED=1 go test -tags fts5 ./pkg/state -run '^TestStateV34PostgresMigrationRepairsAMissingCheck$' -count=1 -v` 取 `--- PASS`。
- 同一验证连续两次失败。

## Maintenance notes

- 以后再加「多条 DDL 组成的迁移」时，每个对象都要有自己独立的存在性判断，不能靠第一个对象的存在推断后续对象已建。`schema_dialect.go` 的循环没有事务，MySQL 更是 DDL 隐式提交，这条约束会一直在。
- 评审 PR 时重点看：重跑路径是否真的会走到约束检查（而不是又早退），以及 `pg_constraint` 是否按 `conrelid` 限定（只按 `conname` 会误判别的 schema/表上的同名约束）。
- 本计划只修「命名约束缺失」；一个名字不同但语义等价的 CHECK 仍会再被加一个约束 —— 这是有意的范围外，记录在此以免下轮误报。
