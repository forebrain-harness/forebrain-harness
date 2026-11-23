# 209 — 数据库地基：forebrain.yaml 配连接地址、方言分派、全局写锁、MySQL / PostgreSQL 结构与测试矩阵

- **基线**：`06f1bc57`（dev），并假定 203–208、215、216 已落地（本计划之后的状态库版本 = 216 落地后的 `stateSchemaVersion`，下文记作 `V`）。
- **状态**：TODO
- **优先级 / 工作量 / 风险**：P1 / L / HIGH（改状态库打开路径与所有事务的入口）
- **依赖**：203–208、215、216 全部 DONE（它们带来的 SQLite 迁移不需要移植到新方言：MySQL / PG 新库直接按 `V` 的结构建）。之后是 210。
- **设计**：`docs/plan/MULTI_DB_MULTI_REPLICA_DESIGN.md`（必读 §0、§1.3、§2.1–§2.4、§2.8、§2.9）。
- **执行隔离**：基于 dev HEAD 的 worktree 或主树 in-tree；**零 commit**（owner 手动提交）。

> **执行者须知**：逐步执行，每步跑「验证」确认结果再进下一步。碰到 §8 STOP 条件就停下报告。完成后把 `plans/README.md` 里 209 那一行的状态改为 DONE 并写一句结果。
>
> **Drift check（先跑）**：README 里 203–207 都是 DONE；`grep -n "const stateSchemaVersion" pkg/state/schema_migrations.go` 记下 `V`；`git diff --stat 06f1bc57 -- pkg/state/db.go` 只有 203–207 带来的改动。

## 0. owner 裁决（执行期不得改）

1. 状态库除 SQLite 外**必须支持 MySQL 8.0+、PostgreSQL 14+**；**forebrain.yaml 能配连接地址**。
2. **禁止引入 GORM 等任何 ORM**。**SQLite 的 SQL 语句一句不改**；MySQL、PostgreSQL **各写一套**，运行时**按数据库类型调用对应语法**。
3. **不搬旧数据**：切到 MySQL / PG 就是新库。
4. 状态库结构 / 数据变更必须有版本号的迁移；今后每个迁移都要有三种方言的实现。
5. 多副本（213）要求所有副本写入串行——本计划提供全局写锁（设计 §2.3，owner 已同意）。

## 1. 交付

- `database.url` 配置（文件白名单追加）+ 按 URL 打开 SQLite / MySQL / PostgreSQL。
- 新包 `pkg/state/dbx`：方言登记与查询、三联语句 `SQL`、错误分类、带全局写锁的 `BeginTx` / `Exec`。
- `pkg/state/schema_mysql.sql`、`pkg/state/schema_postgres.sql`（版本 `V`）+ 版本表 + 迁移框架三方言化。
- 现有全部 `BeginTx` 改走 `dbx.BeginTx`（SQLite 下行为不变）。
- 三库测试矩阵骨架：`FOREBRAIN_TEST_DATABASE_URL`。
- 「SQLite 语句不变」守护。
- **本计划不改写业务语句**（210–212 做）；本计划结束时 MySQL / PG 只保证「能打开、能建表、`pkg/state/dbx` 与结构测试通过」。

## 2. 现状事实（已对基线核实）

### 2.1 打开路径（`pkg/state/db.go`）

```go
//go:embed schema.sql
var schemaSQL string

func Open(ctx context.Context, absSQLitePath string, opts *OpenOptions) (*sql.DB, error) {
	if err := requireSQLiteBuild(ctx); err != nil { … }
	…
	db, err := sql.Open(sqliteDriverName(), dsn)
	…
	if readOnly { requireCurrentSchemaVersion(ctx, db) … return db, nil }
	if err := migrateStateSchema(ctx, db, abs); err != nil { … }
	return db, nil
}

func StateDBPath(home string) string { return filepath.Join(home, "state", "forebrain.state.sqlite") }

func OpenStateFromHome(ctx context.Context, home string, cfg *appcfg.Root) (*sql.DB, error) {
	_ = cfg
	return Open(ctx, StateDBPath(home), nil)
}
func OpenStateForTest(ctx context.Context, absSQLitePath string) (*sql.DB, error) { return Open(ctx, absSQLitePath, nil) }
```

- DSN（`db.go:154`）：`mode=rwc&_busy_timeout=5000&_journal_mode=WAL&_synchronous=NORMAL&_foreign_keys=1&_txlock=immediate`——**每个事务在 BEGIN 时拿写锁**；`stateBusyTimeoutMs = 5000`（163 行）。
- 驱动注册名 `forebrain_sqlite`（`db.go:84-90`）；`dbtx` 接口（77-82 行）是 `*sql.DB` / `*sql.Tx` 的共同查询面。
- 生产调用者：`pkg/process/open.go:319`、`pkg/process/runtime.go:150`（都是 `OpenStateFromHome(ctx, home, nil)`）、`pkg/process/config_db.go:378`（只读打开）。测试里 74 个文件用 `OpenStateForTest` / `state.Open`。
- 配置文件路径：`pkg/process/open.go:270-273`（`opts.ConfigPath`，否则 `home.ResolveConfigPath(root)`）。

### 2.2 迁移执行器（`pkg/state/schema_migrations.go:1882-2020`）

一个连接上：`PRAGMA busy_timeout` → `PRAGMA foreign_keys = OFF` → v11 前备份（`VACUUM INTO`）→ `BEGIN IMMEDIATE` → 读 `PRAGMA user_version` → 新库（无 `fb_` 表）直接执行 `schemaSQL` 并写版本 → 否则逐个 `stateSchemaMigrations` → 外键检查 → 写版本 → `COMMIT` → 视空闲页 `VACUUM`。`requireCurrentSchemaVersion`（2005）与 `setSchemaVersion`（2016）都用 `PRAGMA user_version`。

### 2.3 事务入口

`BeginTx` 调用分布（非测试）：`pkg/state/session_store.go` 5、`task_store.go` 3、`config_store.go` 2、`runrt.go` 2、`message_sync.go` 2、`project_store.go` 1、`pkg/memory/store.go` 4、`ftsindex.go` 1、`jobs.go` 1、`pkg/migrate/sessions.go` 3；另有显式 `BEGIN IMMEDIATE`：`schema_migrations.go`（迁移）、`pkg/tool/loaded_skills.go`（第二个 SQLite 文件，212 处理）。执行时以 `grep -rn "BeginTx(\|BEGIN IMMEDIATE" pkg --include='*.go' | grep -v _test` 为准。

### 2.4 SQLite 错误判断

`pkg/state/project_store.go`（2 处）、`pkg/state/db.go`（2 处）、`pkg/memory/store.go`（1 处）按 `sqlite3.Error` / 错误文本判断唯一约束或忙。

## 3. 设计

### 3.1 配置

- 207 的 `FileRoot` 加 `Database struct{ URL string \`yaml:"url,omitempty"\` }`；`FileSupportedKeys()` 追加 `database.url`。
- 新增 `state.OpenFromConfig(ctx, home, databaseURL string) (*sql.DB, error)`：URL 空 → `Open(ctx, StateDBPath(home), nil)`（现状）；`mysql://` → MySQL；`postgres://` / `postgresql://` → PostgreSQL；其它 scheme → 错误「不支持的数据库类型」。`OpenStateFromHome` 改为读 `<ConfigPath>` 的 `FileRoot.Database.URL` 后调用它（签名换成接收 URL，三处生产调用者同步改；调用者已经知道 `cfgPath`）。
- MySQL DSN 转换（`mysql://user:pass@host:port/db?x=y` → go-sql-driver 的 `user:pass@tcp(host:port)/db?…`）：强制 `charset=utf8mb4&collation=utf8mb4_bin&parseTime=false&multiStatements=false`，并在每个新连接上执行 `SET SESSION transaction_isolation='READ-COMMITTED'`（用驱动的 connector 回调或 `db.SetConnMaxLifetime` + 首次使用时设置；做不到就 STOP）。用户名 / 密码里的特殊字符按 URL 解码。
- PostgreSQL：`pgx/v5/stdlib` 直接用 URL；默认隔离级别 READ COMMITTED（不改）。
- 依赖：`go get github.com/go-sql-driver/mysql@<最新稳定版> github.com/jackc/pgx/v5@<最新稳定版>`；在 `NOTICE` / 第三方许可证清单里登记（照仓库现有做法，`grep -rn "mattn/go-sqlite3" NOTICE third_party` 找位置）。

### 3.2 `pkg/state/dbx`

```go
package dbx

type Dialect int

const (
	SQLite Dialect = iota
	MySQL
	Postgres
)

// SQL is one statement in each dialect. SQLite is the statement as it was
// before multi-database support and is never edited.
type SQL struct{ SQLite, MySQL, Postgres string }

// For returns the statement for d; an empty one panics so a missing
// translation fails the first test that reaches it.
func (q SQL) For(d Dialect) string

func Register(db *sql.DB, d Dialect)
// DialectOf answers for a *sql.DB registered by Open, a *Tx from BeginTx, or
// a *sql.Conn obtained from a registered DB through Conn; anything
// unregistered is SQLite — only SQLite can be opened outside Open.
func DialectOf(q any) Dialect

type Tx struct{ *sql.Tx /* + conn, dialect, release */ }
func BeginTx(ctx context.Context, db *sql.DB, opts *sql.TxOptions) (*Tx, error)
func (t *Tx) Commit() error
func (t *Tx) Rollback() error

// Exec runs one write outside a transaction. On MySQL / PostgreSQL it takes
// the global write lock in a short transaction, as SQLite's immediate
// transactions serialise every write.
func Exec(ctx context.Context, db *sql.DB, q string, args ...any) (sql.Result, error)

func IsUniqueViolation(err error) bool
func IsBusy(err error) bool
```

- **全局写锁**：
  - SQLite：`BeginTx` = `db.BeginTx`（DSN 的 `_txlock=immediate` 已串行）；`Exec` = `db.ExecContext`。
  - PostgreSQL：`BeginTx` 开事务后执行 `SET LOCAL lock_timeout = '5s'` 与 `SELECT pg_advisory_xact_lock(<固定 int64 常量>)`；提交 / 回滚自动释放。
  - MySQL：`db.Conn(ctx)` 拿独占连接 → `SELECT GET_LOCK('forebrain_state_write', 5)`（返回 0 / NULL 即超时，按 `IsBusy` 报错）→ `conn.BeginTx` → `Commit` / `Rollback` 之后在同一连接上 `SELECT RELEASE_LOCK('forebrain_state_write')` 再 `conn.Close()`。
  - 超时 5 秒与 SQLite `_busy_timeout=5000` 对齐。
- 只读打开（`OpenOptions.ReadOnly`）：MySQL / PG 不跑迁移、只校验版本。
- 错误分类：SQLite 分支把 §2.4 那几处现有判断原样搬进来；MySQL 唯一约束 1062、锁等待 1205 / 死锁 1213；PG `23505`、`55P03` / `40P01`。

### 3.3 结构与版本

- `schema_mysql.sql`、`schema_postgres.sql`：与 `schema.sql`（版本 `V`）**同表、同列、同索引名**；按设计 §2.4 与 §2.9 的类型与 DDL 规则逐表翻译。`fb_memory_fts`：MySQL 普通表 + `FULLTEXT INDEX … WITH PARSER ngram`；PG 普通表 + `tsvector` 生成列 + GIN（设计 §2.5）。用到 `rowid` 的表在两方言里加显式自增列（以 `grep -rn "rowid" pkg --include='*.go' | grep -v _test | grep -v schema_migrations` 的命中为准，基线有 `ftsindex.go` 6、`artifact_store.go` 2、`llm_attempts.go` 2、`runrt.go` 1、`session_store.go` 1）。
- 每个文件按「一条语句一个 `;` 结尾」写，执行器逐条执行（不依赖 `multiStatements`）。
- 版本表 `fb_schema_version (version BIGINT NOT NULL)`，只有一行。
- 迁移执行器：`migrateStateSchema` 按方言分派——SQLite 原样；MySQL / PG：取 `GET_LOCK` / `pg_advisory_lock`（迁移锁，与写锁不同名）→ 无 `fb_` 表则执行方言结构文件、写版本 `V` → 否则从 `fb_schema_version` 读版本，按 `stateSchemaMigrations` 里 `version > 当前` 的条目执行其 `applyMySQL` / `applyPostgres`。
- `schemaMigration` 增加 `applyMySQL`、`applyPostgres func(ctx, *sql.Conn) error`；**版本 ≤ V 的条目两者为 nil 且永不在新方言上执行**；版本 > V 的条目若缺任一个，`Open` 时报错（加单测）。

### 3.4 测试矩阵

- `OpenStateForTest(ctx, path)`：环境变量 `FOREBRAIN_TEST_DATABASE_URL` 为空 → 现状；指向 MySQL / PG → 以 `path` 的哈希派生一个库名（`fbt_<16 位十六进制>`），先 `DROP DATABASE IF EXISTS` 再 `CREATE DATABASE`，然后按 3.1 打开。这样 74 个测试文件不改一行就能对新库跑。
- 本地起库（写进 `docs/plan/MULTI_DB_MULTI_REPLICA_DESIGN.md` 末尾的「开发者备忘」小节或 `scripts/test-db.sh`）：

```bash
docker run -d --name fb-mysql -e MYSQL_ROOT_PASSWORD=fb -p 13306:3306 mysql:8.0
docker run -d --name fb-pg -e POSTGRES_PASSWORD=fb -p 15432:5432 postgres:14
FOREBRAIN_TEST_DATABASE_URL='mysql://root:fb@127.0.0.1:13306/fbt' CGO_ENABLED=1 go test -tags fts5 ./pkg/state/dbx/... -count=1
FOREBRAIN_TEST_DATABASE_URL='postgres://postgres:fb@127.0.0.1:15432/postgres?sslmode=disable' CGO_ENABLED=1 go test -tags fts5 ./pkg/state/dbx/... -count=1
```

- 结构一致性测试（`pkg/state/schema_dialect_test.go`，只在设置了 URL 时跑）：对比 SQLite（`sqlite_master` + `pragma_table_info` + `pragma_index_list`）与目标库（`information_schema` / `pg_catalog`）的表名、列名、索引名集合相等；列类型按 §2.9 映射核对。
- 保留字测试：目标库里 `information_schema.KEYWORDS WHERE RESERVED=1`（MySQL）/ `pg_get_keywords() WHERE catcode='R'`（PG）与 `schema.sql` 的列名求交集，交集里的每个列名在该方言的结构文件里都必须加了引号（反引号 / 双引号）。基线已知：`key`、`trigger`。

### 3.5 「SQLite 语句不变」守护

- 本计划开始前，用一个小 Go 程序（放 `scripts/sqlite-sql-snapshot/main.go`，用 `go/ast` 遍历 `pkg/**/*.go` 非测试文件）提取：①所有传给 `ExecContext` / `QueryContext` / `QueryRowContext` / `Exec` / `Query` / `QueryRow` / `Prepare*` 的字符串字面量；②所有 `dbx.SQL{…}` 的 `SQLite` 字段字面量（此时还没有）。输出排序去重后的文本到 `pkg/architecture/testdata/sqlite_sql_snapshot.txt`。
- 加测试 `TestSQLiteStatementsUnchanged`（`pkg/architecture`）：重新提取并与快照比较，必须完全相等。210–212 把语句挪进 `dbx.SQL{SQLite: …}` 后集合不变；谁改了一个字符这个测试就红。
- 动态拼接的语句（`fmt.Sprintf`、`+` 拼接）提取不到完整文本——快照只覆盖字面量；在 README 结果里报告动态语句的个数与位置，210–212 人工核对。

## 4. 步骤

每步结束都跑：`CGO_ENABLED=1 go build -tags fts5 ./... && go vet -tags fts5 ./pkg/...`（期望：无输出）。

1. **快照**（3.5）：先生成快照再动任何代码。`go test ./pkg/architecture/ -run TestSQLiteStatementsUnchanged` 通过。
2. **依赖**：加两个驱动与许可证登记。
3. **`dbx`**（3.2）：先写单测（SQLite 下全部行为不变；`SQL.For` 空方言 panic；`DialectOf` 对 Tx / Conn 的回答）。
4. **事务入口**：§2.3 的所有 `BeginTx` 改为 `dbx.BeginTx`（`pkg/migrate`、`pkg/memory` 一并）；类型 `*sql.Tx` 的形参改为 `*dbx.Tx` 或 `dbtx`。**不改任何 SQL 文本**。SQLite 全量测试照旧全绿。
5. **错误分类**：§2.4 的判断改为 `dbx.IsUniqueViolation` / `dbx.IsBusy`。
6. **配置与打开**（3.1）：`OpenFromConfig`、`FileRoot.Database`、三处调用者、只读打开。
7. **结构文件与版本表**（3.3）：写两份结构；迁移执行器分派；`schemaMigration` 三方言字段与校验。
8. **测试矩阵**（3.4）：`OpenStateForTest` 分派；结构一致性与保留字测试。
9. **包图**：`scripts/package-graph.sh`。

## 5. 测试

- `pkg/state/dbx/*_test.go`：方言登记、`SQL.For`、错误分类（各方言造一次唯一冲突）、写锁——两个 goroutine 各开 `BeginTx` 并在事务里 sleep，第二个必须等第一个提交（三方言都测；MySQL / PG 在设置 URL 时测）；`Exec` 在 MySQL / PG 上与进行中的事务互斥。
- `pkg/state/db_test.go`：`OpenFromConfig` 对三种 URL 的分派；不支持的 scheme 报错；MySQL DSN 转换（含特殊字符密码）。
- `pkg/state/schema_migrations_test.go`：版本 > V 的迁移缺方言实现时 `Open` 报错；SQLite 迁移测试原样通过。
- `pkg/state/schema_dialect_test.go`：结构一致性、保留字（设置 URL 时）。
- `pkg/architecture`：`TestSQLiteStatementsUnchanged`。

## 6. 完成判据（机器可查）

```bash
CGO_ENABLED=1 go test -tags fts5 ./... -count=1 -p 2                               # SQLite：全量全绿（行为不变）
go vet -tags fts5 ./...                                                             # 无输出
FOREBRAIN_TEST_DATABASE_URL='mysql://root:fb@127.0.0.1:13306/fbt' CGO_ENABLED=1 go test -tags fts5 ./pkg/state/dbx/... ./pkg/state/ -run 'Dialect|Schema|Open' -count=1        # ok
FOREBRAIN_TEST_DATABASE_URL='postgres://postgres:fb@127.0.0.1:15432/postgres?sslmode=disable' CGO_ENABLED=1 go test -tags fts5 ./pkg/state/dbx/... ./pkg/state/ -run 'Dialect|Schema|Open' -count=1   # ok
grep -rn "\.BeginTx(" pkg --include='*.go' | grep -v _test | grep -v "pkg/state/dbx/"    # 无输出
```

## 7. 维护注意

- **今后任何新 SQL**：用 `dbx.SQL{SQLite, MySQL, Postgres}` 三联写；任何新迁移：三个 apply 都写。
- 210–212 把业务语句搬进三联；搬完之前，MySQL / PG 上跑业务测试会因 `SQL.For` 的空方言 panic——这是预期的进度信号。
- 写锁常量与锁名定义在 `dbx` 里，213 的多副本也依赖它。

## 8. STOP 条件

- go-sql-driver 做不到「每个新连接设置隔离级别」且没有等价办法。
- 某个现有 `BeginTx` 的调用方式无法换成 `dbx.Tx`（例如跨包传 `*sql.Tx` 给第三方库）。
- 快照提取器无法区分 SQL 字面量与普通字符串，误报大量非 SQL 文本。
- 某张表在 MySQL 里无法在 3072 字节索引上限内保持原有唯一约束，且哈希生成列也不适用。
