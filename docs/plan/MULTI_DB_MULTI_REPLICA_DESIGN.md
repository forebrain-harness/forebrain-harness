# 多数据库（SQLite / MySQL / PostgreSQL）与多副本设计

- 基线：`06f1bc57`（dev），并假定本期运行时收口 203–207、企业内网 208 已落地。
- 状态：设计稿（owner 裁决已定，见 §0）；实施计划见 §4。
- 关联：`plans/191-web-config-ui.md` §8.27–§8.28；记忆 `multi-db-multi-replica`、`enterprise-intranet-offline`。

## 0. owner 裁决（2026-10-31，执行期不得改）

1. 状态库除 SQLite 外**必须支持 MySQL、PostgreSQL**。
2. **forebrain.yaml 必须能配 MySQL / PostgreSQL 的连接地址**。
3. **禁止引入 GORM 等任何 ORM**；**SQLite 的 SQL 语句一句不改**；为 MySQL、PostgreSQL **分别新增**各自语法的 SQL；运行时**判断数据库类型、调用对应语法的 SQL**。
4. 全文检索用**各库原生**：SQLite 照旧 FTS5；MySQL 内置 FULLTEXT（ngram）；PostgreSQL 内置 tsvector（`simple` 配置 + GIN），写入前用现有中文词典在 Go 里切词，**不装扩展**。
5. **不搬旧数据**：切到 MySQL / PostgreSQL 就是新库。
6. **多网关共享一个库**：K8s 多副本、共享存储、共享同一个 FOREBRAIN_HOME。
7. 跨副本：**副本间转发**——按运行归属把流式订阅、审批决定、插话、中断经内网 HTTP 转给正在跑的副本；不落库、不加中间件（不引入 Redis）。
8. 最低版本：**MySQL 8.0+、PostgreSQL 14+**。
9. 企业内网离线部署（主机 / Docker / K8s）必须可用，配三份部署文档与 gateway 的 Dockerfile。

## 1. 现状事实（基线已核实）

### 1.1 SQL 规模

| 位置 | 调用处（`ExecContext` / `QueryContext` / `QueryRowContext`） |
|---|---|
| `pkg/state/schema_migrations.go` | 160（历史迁移，只服务 SQLite 老库） |
| `pkg/state` 其余 15 个文件 | 259（`session_store.go` 75、`runrt.go` 41、`cron.go` 27、`project_store.go` 22、`task_store.go` 20、`rewind.go` 13、`config_store.go` 12、`action_service.go` 12、`session_events.go` 9、`project_channel_store.go` 7、`file_service.go` 6、`artifact_store.go` 5、`llm_attempts.go` 4、`db.go` 4、`session_ui_state.go` 2） |
| `pkg/memory` | 49（`store.go`、`jobs.go`、`ftsindex.go`） |
| `pkg/migrate` | 15 |
| `pkg/tool/loaded_skills.go` | 13（**第二个 SQLite 文件**：工具输出历史 `output_records`，自带 `PRAGMA user_version` 迁移，`loaded_skills.go:601-720`） |
| `pkg/process` | 5 |
| `pkg/gateway/session_files.go` | 1 |

需要 MySQL / PostgreSQL 版本的运行时语句约 **340 处**（历史迁移不需要：新库从当前结构直接建）。`schema.sql` 31 张表。

### 1.2 SQLite 专有写法（运行时 + 结构）

`rowid`（84 处，消息排序等靠它）、`ON CONFLICT … DO UPDATE … excluded.`（约 20 处 upsert）、`RETURNING`（6）、部分索引（17，MySQL 不支持）、`TEXT PRIMARY KEY`（24，MySQL 不能直接给 TEXT 建主键）、`IFNULL`（运行时少量，迁移里多）、`STRICT`、`AUTOINCREMENT`、`WITH RECURSIVE`（6，三家都支持）、`PRAGMA` / `sqlite_master`（迁移与工具输出历史）、`VACUUM INTO`（迁移前备份）、FTS5 虚表 `fb_memory_fts`（`schema.sql:561`，`tokenize='unicode61 remove_diacritics 2'`；**词已在 Go 里切好**，`pkg/memory/ftsindex.go` 的 `searchTerms`，FTS5 只按空格再切一次，最终由 Go 做整词比较）。错误码依赖：`pkg/state/project_store.go`、`db.go`、`pkg/memory/store.go` 各有 1–2 处按 SQLite 错误判断（唯一约束 / 忙）。

### 1.3 事务语义（最关键）

状态库以 `_txlock=immediate` 打开（`pkg/state/db.go:154`）：**每个事务在 BEGIN 时就拿写锁，所有写事务全局串行**。代码里大量「先读后写」的事务（会话忙时拒绝、任务认领、配置段读改写、运行归属）依赖这一点保证互斥，没有显式行锁。MySQL / PostgreSQL 的默认隔离级别做不到这一点。

### 1.4 只在单进程内存里的协调

- 定时任务认领：`pkg/turn/scheduler.go:577` `claim` 用进程内 map——多副本会重复触发。
- 审批等待、插话、Esc 中断、流式推送：都在正在跑这一轮的进程内存里（`pkg/run/controller.go`、网关 WS）。
- 运行归属已经在库里：`fb_run_owners(owner, heartbeat_at_ms)` 心跳租约（`pkg/state/runrt.go:1185-1210` `HoldOwnerLease`），活性判断 `runrt.go:217,290`——跨副本可直接沿用。
- 跨进程协同（plans/194–201）走本机 unix socket——只在同一节点内有效。
- 健康检查：`/healthz`、`/readyz`（`pkg/gateway/server.go:794-795`）。

## 2. 设计

### 2.1 配置

forebrain.yaml 新增 `database` 段（加进 207 的「文件支持项」白名单单一来源 `FileSupportedKeys()`）：

```yaml
database:
  url: postgres://forebrain:密码@pg.corp:5432/forebrain?sslmode=disable
  # 或 mysql://forebrain:密码@mysql.corp:3306/forebrain
  # 不写 / 留空 = SQLite（<FOREBRAIN_HOME>/forebrain.state.sqlite，现状）
```

- 库类型由 URL scheme 决定（`mysql` / `postgres` / `postgresql`）；密码按凭据规则明文写在文件里。
- MySQL：把 URL 转成 go-sql-driver 的 DSN，强制 `charset=utf8mb4`、`collation=utf8mb4_bin`（大小写敏感，保持 SQLite 的比较语义）、`parseTime=false`（库里时间都是整数毫秒）、会话级 `transaction_isolation='READ-COMMITTED'`。
- PostgreSQL：pgx 原样使用 URL。
- 驱动：`github.com/go-sql-driver/mysql`（MPL-2.0）、`github.com/jackc/pgx/v5/stdlib`（MIT）——都是纯 Go，不引 ORM。执行前确认许可证与仓库发行方式兼容。

### 2.2 方言分派（不引 ORM）

新包 `pkg/state/dbx`（或放在 `pkg/state` 内，执行计划定）：

```go
type Dialect int
const ( SQLite Dialect = iota; MySQL; Postgres )

// SQL holds one statement in each dialect. SQLite is the text that existed
// before multi-database support, unchanged.
type SQL struct{ SQLite, MySQL, Postgres string }

func Register(db *sql.DB, d Dialect)      // Open 时登记
func DialectOf(db *sql.DB) Dialect        // 按 *sql.DB 查（sync.Map），不改现有函数签名
func (q SQL) For(d Dialect) string        // 某方言为空 → panic（测试期即暴露漏写）
```

调用处改写形如：

```go
// 前
row := s.DB.QueryRowContext(ctx, `SELECT … FROM fb_sessions WHERE id=?`, id)
// 后
row := s.DB.QueryRowContext(ctx, dbx.SQL{
	SQLite:   `SELECT … FROM fb_sessions WHERE id=?`, // 原文，一字不改
	MySQL:    `SELECT … FROM fb_sessions WHERE id=?`,
	Postgres: `SELECT … FROM fb_sessions WHERE id=$1`,
}.For(dbx.DialectOf(s.DB)), id)
```

- 动态拼接的语句（`IN (…)` 列表、`fmt.Sprintf`）同样三份；PostgreSQL 的占位符按位置编号。
- 错误分类：`dbx.IsUniqueViolation(err)`、`dbx.IsBusy(err)` 按方言判断，替换 §1.2 那几处 SQLite 错误判断（SQLite 分支的判断逻辑原样搬进去）。
- **「SQLite 的 SQL 不改」的守护**：一个测试 / 脚本提取仓库里所有 `SQLite:` 字段与仍直接传给 `*Context` 的 SQL 字面量，与 203–207 落地后的基线快照逐条比较，集合必须相等（只允许位置移动）。

### 2.3 写入串行化（对齐 SQLite 的 immediate 事务）

为不在 340 处逐一设计行锁，MySQL / PostgreSQL 用**一把全局写锁**复刻 SQLite 的语义：

- 事务：`dbx.BeginTx` 在 MySQL / PostgreSQL 上开事务后立即取锁——PostgreSQL `SELECT pg_advisory_xact_lock(<常量>)`（事务结束自动释放）；MySQL `SELECT GET_LOCK('forebrain_state_write', <超时>)`，提交 / 回滚后在同一连接上 `RELEASE_LOCK`。
- 事务外的单条写语句：在 MySQL / PostgreSQL 上由 `dbx.Exec` 包成「开事务 → 取锁 → 执行 → 提交」，与 SQLite 下「单条写也要排队拿写锁」一致。
- 读不取锁（READ COMMITTED），等价于 SQLite WAL 下的读。
- 代价：所有副本的写入串行。forebrain 的写入量（消息、事件、配置）在这个量级内可接受；若压测显示瓶颈，再按表拆锁——不在本期做。
- 现有所有 `BeginTx` 调用点改为 `dbx.BeginTx`（SQLite 下行为不变）。

### 2.4 表结构

- `schema.sql` 不改；新增 `schema_mysql.sql`、`schema_postgres.sql`，表示**同一版本**的同一结构。
- 类型映射：`INTEGER` → `BIGINT`；作主键 / 索引 / 唯一的 `TEXT` → `VARCHAR(n)`（MySQL；`n` 按内容定，ID 类 191，路径类 1024 并用前缀索引或哈希列——逐列定在计划里）；大文本 → `LONGTEXT`（MySQL）/ `TEXT`（PG）；`BLOB` → `LONGBLOB` / `BYTEA`；`AUTOINCREMENT` → `BIGINT AUTO_INCREMENT` / `BIGINT GENERATED BY DEFAULT AS IDENTITY`；`STRICT` 去掉；`CHECK` 保留（MySQL 8.0.16 起强制执行）。
- `rowid`：SQLite 代码用到 `rowid` 的表，在 MySQL / PG 结构里加显式自增列（如 `row_id`），这两方言的语句引用它。
- 部分索引：PostgreSQL 原样保留 `WHERE`；MySQL——唯一部分索引用生成列 + 唯一索引（如 `NULLIF(account_key, '')`，多个 NULL 不冲突），普通部分索引改成普通索引。
- 版本：SQLite 照旧 `PRAGMA user_version` + 历史迁移；MySQL / PG 用表 `fb_schema_version(version BIGINT)`，**新库直接按当前结构建并写入当前版本**（不跑历史迁移）。之后每个迁移都要提供三种方言的实现（`schemaMigration` 增加 `applyMySQL` / `applyPostgres`；缺一个就在加载迁移表时报错）。
- 迁移前备份：`VACUUM INTO` 只对 SQLite；MySQL / PG 的备份交给 DBA（部署文档写明）。
- 结构一致性测试：三库建好后比对表名、列名、索引名（`information_schema` / `pg_catalog` / `sqlite_master`），类型按映射表核对。

### 2.5 全文检索

`fb_memory_fts` 存的是 Go 已切好、以空格分隔的词：

- SQLite：照旧 FTS5 + `bm25()`。
- MySQL：普通表，`terms` 列建 `FULLTEXT INDEX … WITH PARSER ngram`；查询 `MATCH(terms) AGAINST(? IN BOOLEAN MODE)`，每个词作为带引号的短语；分数用 `MATCH` 返回值。
- PostgreSQL：`terms_tsv tsvector GENERATED ALWAYS AS (to_tsvector('simple', terms)) STORED` + GIN 索引；查询 `terms_tsv @@ to_tsquery('simple', 'a | b | …')`，分数 `ts_rank`。
- 三家的候选都由 Go 侧现有的整词比较最终裁定；分数口径不同导致排序可能略有差异（owner 已知悉）。

### 2.6 第二个 SQLite 文件（工具输出历史）

SQLite 模式照旧是独立文件；**MySQL / PG 模式下 `output_records` 表建在主库里**（三方言结构 + 语句），因为共享存储上的 SQLite 文件不安全。

### 2.7 多副本

1. **副本登记**：每个网关进程有一个副本 ID（沿用运行归属的 owner ID）；`fb_run_owners` 加 `internal_addr`（副本间可达的内网地址，`FOREBRAIN_INTERNAL_ADDR` 环境变量，K8s 用 downward API 注入 `POD_IP` 拼端口；主机部署默认 `http_addr`）——结构变更，三方言迁移。
2. **副本间转发**：入口副本收到某会话的**流式订阅、审批决定、插话、中断、子代理视图操作**时，查该会话的运行归属；归属不是自己且对方心跳新鲜 → 经内网 HTTP 调对方的内部端点（`/internal/…`），流式订阅做副本间透传（WS 或 SSE）。内部端点用网关令牌鉴权（所有副本共享同一个 forebrain.yaml），并只接受来自已登记副本地址的请求。流式增量照旧只推不存。
3. **定时任务**：每次触发在库里认领——以 (job_id, 计划触发时间) 为唯一键插入 `fb_cron_runs`，插入成功的副本执行；心跳任务同理。
4. **记忆流水线**：核实第一 / 第二阶段的认领是否已是库内租约（今天要支持终端与网关共享同一个库，大概率已是）；不是则改成库内租约。
5. **聊天渠道**（飞书、钉钉、企业微信、Telegram 等的长连接 / 轮询）：每个渠道账号一个库内租约，只有持租约的副本连接，租约过期由别的副本接手。
6. **忙时拒绝、任务认领**：§2.3 的全局写锁下，现有「事务内先查后写」的逻辑跨副本照样互斥。
7. **跨进程协同（unix socket）**：只在同一节点内；多副本下各副本各自独立，部署文档注明。
8. **共享 home**：同一会话同一时刻只有一个副本在跑（运行归属保证），工作目录、记忆文件、技能、缓存的写入不会两边同时发生；执行计划要逐个核查 home 里「单写者」假设（日志、spool、worktree 创建、种子写入）。
9. **SQLite 不能多副本**：网关启动时，若库是 SQLite 且发现另一个活着的网关副本登记在**别的主机**上 → 拒绝启动并说明「多副本需要 MySQL 或 PostgreSQL」。同一主机的终端 + 网关共享 SQLite 照旧允许。

### 2.8 测试

- 环境变量 `FOREBRAIN_TEST_DATABASE_URL` 指向 MySQL 8 / PostgreSQL 14 容器时，`pkg/state`、`pkg/memory`、`pkg/migrate`、`pkg/process`、`pkg/tool` 的库测试对该库再跑一遍；不设时只跑 SQLite（现状不变）。测试库每个用例独立建库或建 schema。
- 多副本集成：docker compose 起 PostgreSQL + 两个网关 + 共享目录，脚本驱动「在副本 A 发起对话、从副本 B 订阅流式并批准审批、在 B 上 Esc 中断」，以及「定时任务只触发一次」「渠道只连一次」。

### 2.9 方言改写规则（210–212 逐条遵守）

写 MySQL / PostgreSQL 版本时，**与 SQLite 语句的结果必须等价**（同样的行、同样的顺序、同样的空值）。逐条对照：

| SQLite 写法 | MySQL 8.0 | PostgreSQL 14 |
|---|---|---|
| `?` 占位符 | `?` | `$1, $2, …`（按出现顺序编号；同一参数出现两次就传两次或复用编号） |
| `INSERT … ON CONFLICT(k) DO UPDATE SET c=excluded.c` | `INSERT … ON DUPLICATE KEY UPDATE c=VALUES(c)`（冲突键必须是主键或唯一索引；`WHERE` 条件的 upsert 改成 `c=IF(条件, VALUES(c), c)`） | 原样（`excluded.c`） |
| `INSERT OR IGNORE` / `ON CONFLICT DO NOTHING` | `INSERT … ON DUPLICATE KEY UPDATE <主键>=<主键>`（**不用** `INSERT IGNORE`，它会吞掉别的错误） | `ON CONFLICT DO NOTHING` |
| `RETURNING` | 不支持：同一事务里先写再 `SELECT`（自增主键用 `LAST_INSERT_ID()`） | 原样 |
| `IFNULL(a,b)` | 原样 | `COALESCE(a,b)` |
| `a \|\| b` 字符串拼接 | `CONCAT(a,b)`（MySQL 的 `\|\|` 是逻辑或） | 原样 |
| `LIKE`（SQLite 对 ASCII **不区分大小写**） | `LOWER(a) LIKE LOWER(?)`（库排序规则是 `utf8mb4_bin`） | `ILIKE` |
| `COLLATE NOCASE` 比较 | `LOWER(a) = LOWER(b)` | `LOWER(a) = LOWER(b)` |
| `length(s)`（字符数） | `CHAR_LENGTH(s)`（`LENGTH` 是字节数） | `length(s)` |
| `instr(a,b)` | `INSTR(a,b)` | `strpos(a,b)` |
| `CAST(x AS INTEGER)` / `AS TEXT` | `CAST(x AS SIGNED)` / `AS CHAR` | `CAST(x AS BIGINT)` / `AS TEXT` |
| `json_extract(c,'$.a')` | `JSON_UNQUOTE(JSON_EXTRACT(c,'$.a'))`（取字符串）/ `JSON_EXTRACT`（取 JSON） | `c::jsonb ->> 'a'` / `c::jsonb -> 'a'` |
| 带聚合的「裸列」（`SELECT max(x), y FROM t` 取最大那行的 `y`，SQLite 专有） | 改成子查询或 `ROW_NUMBER()` 窗口 | 同左 |
| `ORDER BY` 可空列（SQLite 升序时 NULL 在前） | 升序 NULL 在前（一致） | 写明 `NULLS FIRST`（升序）/ `NULLS LAST`（降序） |
| `WHERE flag`（整数当布尔） | 原样 | `flag <> 0`（列一律整数，不用 BOOLEAN） |
| `rowid` | 表里的显式自增列（结构里加，§2.4） | 同左 |
| `WITH RECURSIVE` | 原样 | 原样 |
| 保留字列名（如 `key`、`trigger`） | 反引号 `` `key` `` | 双引号（仅 PG 保留字需要） |
| 事务 | `dbx.BeginTx`（§2.3） | `dbx.BeginTx` |
| 事务外单条写 | `dbx.Exec`（§2.3） | `dbx.Exec` |

结构（DDL）另有：`TEXT` 主键 / 索引列在 MySQL 用 `VARCHAR(255)`；可能超过 700 字符又要唯一的列（路径、根目录）在 MySQL 用 `CHAR(64)` 的 `SHA2(...,256)` 生成列建唯一索引；复合索引总长不超过 3072 字节；`CHECK` 原样；外键与 `ON DELETE CASCADE` 原样（MySQL 用 InnoDB）。保留字以库自身为准：MySQL `information_schema.KEYWORDS WHERE RESERVED=1`、PostgreSQL `pg_get_keywords() WHERE catcode='R'`——209 用测试自动检出。

## 3. 风险

- **工作量**：约 340 处语句 × 2 种方言 + 两份结构 + 迁移框架 + 多副本，量级 XL；按 §4 拆分串行执行。
- **语义漂移**：三份语句可能写得不等价——靠三库测试矩阵兜底；每个计划的完成判据都要求矩阵全绿。
- **全局写锁**：所有副本写入串行，吞吐有上限（本期接受）。
- **MySQL upsert**：`INSERT … ON DUPLICATE KEY UPDATE col = VALUES(col)` 在 8.0.20 起提示弃用，但 8.0–8.4 / 9.x 仍可用；为支持 8.0 早期版本，本期用 `VALUES()`。
- **共享存储**：NFS / CephFS 的语义（重命名原子性、mtime）与本地盘不同；部署文档要求 RWX 且支持原子重命名。

## 4. 实施计划拆分（编号接 208）

| 计划 | 内容 | 依赖 |
|---|---|---|
| 209 数据库地基 | `database.url` 配置与白名单；驱动；`dbx`（方言登记、`SQL` 三联、错误分类、全局写锁 `BeginTx` / `Exec`）；`Open` 按类型分派；`schema_mysql.sql` / `schema_postgres.sql` 与版本表；迁移框架三方言化；三库测试矩阵骨架；「SQLite 语句不变」守护 | 203–207 |
| 210 会话与运行语句 | `session_store`、`runrt`、`session_events`、`rewind`、`message_sync`、`llm_attempts`、`session_ui_state`、`action_service` 的 MySQL / PG 语句 | 209 |
| 211 其余存储语句 | `project_store`、`task_store`、`cron`、`config_store`、`file_service`、`artifact_store`、`project_channel_store`、`db.go`，以及 `pkg/process`、`pkg/gateway`、`pkg/migrate` | 210 |
| 212 记忆与工具输出 | `pkg/memory` 三方言（含全文）；工具输出历史在 MySQL / PG 下进主库 | 211 |
| 213 多副本 | 副本登记与内网地址、副本间转发、定时任务与渠道租约、记忆认领核查、共享 home 单写者核查、SQLite 多副本拒绝；多副本集成测试 | 212 |
| 214 部署 | gateway Dockerfile；Linux 主机 / Docker / K8s 三份部署文档（含离线内网、镜像变量、数据库、共享存储、多副本）；K8s 清单重写 | 208、213 |
