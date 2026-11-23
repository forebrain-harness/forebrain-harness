# Plan 259: 状态库新增全局模型表与变量表（三库），会话模型行加 model_id

> **Executor instructions**: 按步骤顺序执行，每步的验证命令通过后再做下一步。遇到
> 「STOP conditions」里的情况就停下来报告，不要自己发挥。**不要 commit / push / merge**：
> 改动留在工作区里，做完报告（仓库主人手动提交）。做完后把 `plans/README.md` 里本计划那一行
> 的状态改成 DONE，并附一句执行摘要——除非派你来的评审说索引由他维护。
>
> **Drift check (run first)**:
> `git diff --stat 0a698bf2..HEAD -- pkg/state/schema.sql pkg/state/schema_mysql.sql pkg/state/schema_postgres.sql pkg/state/schema_migrations.go pkg/state/session_store.go pkg/architecture/testdata/sqlite_sql_snapshot.json`
> → 必须为空。不为空就把下面「Current state」里的摘录和实际代码逐段对照，对不上即 STOP。

## Status

- **Priority**: P1（模型配置入库系列 259–268 的地基）
- **Effort**: M
- **Risk**: LOW（纯新增表、新增列与新 store；没有任何调用方改动）
- **Depends on**: none
- **Category**: migration
- **Planned at**: commit `0a698bf2`, 2026-11-07
- **Executed**: 2026-11-08, worktree `.worktrees/exec-259-268`, 基线 `d9c45060`（未提交）——**DONE，两处与正文偏差**：
  ① 两个 store 并入 `pkg/state/config_store.go`、测试并入 `pkg/state/config_store_test.go`（原计划新建的四个文件名被
  owner 架构守卫拒绝：`maxProductionFilesPerPackage` = 31 而 `pkg/state` 已正好 31，且两下划线生产文件的 `_test` 名字
  会算作三下划线；守卫注释自己写明出路是并入同域文件，`project_channel_store.go` 的测试就住在 `project_store_test.go`）。
  导出符号、类型、方法、错误与 SQL 全部按计划逐字保留，260 起照原名调用。
  ② `pkg/architecture` 全包绿这条判据在基线 `d9c45060` 上不可满足：`pkg/llm` 32、`pkg/tool` 32（上限 31）与
  `pkg/skill/audit_skill_test.go` 无同名生产文件都是 HEAD 既有红灯；259 的验收改为「不新增任何守卫违规，且
  `TestSQLiteStatementsUnchanged` 绿」——两者实测通过。
  其它判据实测：`pkg/state/...` 全绿、`LLMModel` 11 个 PASS、`EnvVar` 绿、`test-db.sh check` exit 0、build/vet/gofmt 绿。
  MySQL/PG 腿未跑（docker 守护进程不可达），矩阵行已登记 `|LLMModel|EnvVar`，docker 恢复后自动覆盖。

## Why this matters

owner 2026-11-07 裁定（系列 259–268 共用，后续计划不再重复解释）：

1. 大模型配置**全部持久化到数据库表**，不再存在 `fb_config` 的 `agents` 段 JSON 里，也不再经 `~/.forebrain/.env`。
2. **全局一张模型表**：所有主代理共用。同一 provider + 同一 model 可以配多条（不同 base url / api path / api key），
   同一 model 也可以挂在不同 provider 下；但 **(provider, model, base_url, api_path) 只能有一条**——数据库加唯一索引，
   保存前先查重。
3. **api key 明文入库**；**接口也回传明文**（owner 2026-11-07 推翻「永不回显」，供编辑抽屉的眼睛显示），但只在那一个接口的响应体里，不进日志与事件。
4. home 里的 `.env` 整个删除；原来靠 `.env` 给 MCP/LSP 的 `${VAR}` 取值，改为库里一张「变量」表（只有操作者能写）。
5. 同 provider/model 能有多条之后，"provider/model" 字符串不再能唯一指认一条配置，所以会话的模型选择要记**行 id**。

本计划只铺地基：建两张表、给会话模型行加 `model_id` 列、写两个 store。没有任何运行时行为变化——之后的计划
（260 起）才让运行时读它们。

## Current state

- 状态库有三套 DDL，必须同步改：`pkg/state/schema.sql`（SQLite，**唯一会被老库逐版迁移的那套**）、
  `pkg/state/schema_mysql.sql`、`pkg/state/schema_postgres.sql`。三套由
  `pkg/state/schema_dialect_test.go` 的 `TestSchemaDialectMatchesSQLite` / `TestSchemaDialectForeignKeysMatchSQLite`
  / `TestSchemaDialectVersionRow` 互相对照（表、列、索引名都要一致）。
- 版本号 `pkg/state/schema_migrations.go:23`：`const stateSchemaVersion = 32`；迁移表在同文件
  `stateSchemaMigrations`（`:51` 起），最新一条是 v32（`:94-98`），**从 v30 起每条迁移三库都要实现**：

```go
	{
		version:       32,
		apply:         migrateStateV31ToV32,
		applyMySQL:    migrateStateV31ToV32MySQL,
		applyPostgres: migrateStateV31ToV32Postgres,
	},
```

- 建表迁移的范本就是 v32（`pkg/state/schema_migrations.go:1482-1528`）：SQLite 先查 `sqlite_master` 已有就跳过，
  MySQL/PG 用 `CREATE TABLE IF NOT EXISTS`：

```go
func migrateStateV31ToV32(ctx context.Context, conn *sql.Conn) error {
	var present int
	if err := conn.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='fb_leases'`).Scan(&present); err != nil {
		return err
	}
	if present > 0 {
		return nil
	}
	if _, err := conn.ExecContext(ctx, `
CREATE TABLE fb_leases (
  name TEXT PRIMARY KEY CHECK (TRIM(name) <> ''),
  holder TEXT NOT NULL,
  expires_at_ms INTEGER NOT NULL
) STRICT`); err != nil {
		return fmt.Errorf("create fb_leases: %w", err)
	}
	return nil
}
```

- 加列迁移的范本：`migrateStateV3ToV4`（`pkg/state/schema_migrations.go` 里，查 `pragma_table_info` 已有就跳过，
  再 `ALTER TABLE … ADD COLUMN … NOT NULL DEFAULT ''`）。
- MySQL 的翻译规则写在 `schema_mysql.sql` 文件头（`:1-34`）：id/key 类 TEXT → `VARCHAR(191)`；长度没上限的值做
  唯一键时取 `SHA2(x, 256)` 表达式（范例 `fb_projects`：`UNIQUE KEY idx_fb_projects_agent_root_unique (agent_id, (SHA2(root, 256)))`）；
  所有表 `ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin`（**区分大小写**，与 SQLite/PG 的默认比较一致）；
  外键统一在文件末尾 ALTER 块里加。
- 会话模型行，三库现状（SQLite `schema.sql:198-205`）：

```sql
CREATE TABLE fb_session_model_state (
  session_id TEXT NOT NULL REFERENCES fb_sessions(id) ON DELETE CASCADE,
  provider TEXT NOT NULL,
  model TEXT NOT NULL,
  effort TEXT NOT NULL DEFAULT '',
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (session_id)
) STRICT, WITHOUT ROWID;
```

  MySQL（`schema_mysql.sql:233-240`）列宽是 `VARCHAR(191)`；PG（`schema_postgres.sql:248-255`）是 `TEXT`。
- store 写法范本：`pkg/state/project_channel_store.go`——
  - 每条语句一个 `dbx.SQL{SQLite:…, MySQL:…, Postgres:…}`，执行时 `.For(dbx.DialectOf(s.db))`；PG 占位符 `$1…`；
  - 业务冲突用带字段的错误类型（`ErrChannelAccountTaken{Project}`，`:33-44`），并用 `dbx.IsUniqueViolation(err)`
    （`pkg/state/dbx/errors.go:20`）把唯一索引冲突翻成同一个错误类型——**唯一索引是兜底，先查重是主路径**；
  - 写操作经 `dbx.Exec`（`pkg/state/dbx/tx.go:144`，MySQL/PG 下自带全局写锁）或 `dbx.BeginTx`。
- 新 id 用 `state.NewID(prefix)`（`pkg/state/id.go:68`）。
- 引擎矩阵：`scripts/test-db.sh` 的 `engine_arms` 已有
  `"./pkg/state/|Dialect|Schema|Open|Reap|SearchBackslash|StoreRepairs|Repair|Parts|Output|Events|Artifact|Project"`
  这一行（`scripts/test-db.sh:66`）；新测试名带 `LLMModel` / `EnvVar` 时**必须**把这两个片段加进这一行
  （`./scripts/test-db.sh check` 会核对）。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| 构建 | `go build ./...` | exit 0 |
| state 包测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/state/... -count=1` | ok |
| 三库对照 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/state/ -run 'SchemaDialect' -count=1` | ok |
| 引擎矩阵登记 | `./scripts/test-db.sh check` | exit 0 |
| MySQL/PG 腿（有 docker 时） | `./scripts/test-db.sh up && ./scripts/test-db.sh mysql && ./scripts/test-db.sh postgres` | 全过；没有 docker 就在报告里写「未跑」 |
| vet | `go vet ./pkg/state/...` | exit 0 |

## Scope

**In scope**:
- `pkg/state/schema.sql`、`pkg/state/schema_mysql.sql`、`pkg/state/schema_postgres.sql`
- `pkg/state/schema_migrations.go`（版本 33 与三个迁移函数）
- `pkg/state/config_store.go`（**执行时改为并入此文件**：`LLMModelStore`、`NormalizeLLMModelIdentity`、`EnvVarStore` 及其 SQL 块）
- `pkg/state/config_store_test.go`（**执行时改为并入此文件**：`TestLLMModel*` 10 个、`TestEnvVar*` 2 个）
- `pkg/state/session_store.go`：只给 `SessionModelSelection` 加 `ModelID` 字段并让三处 SQL 读写它（见 Step 5）
- `pkg/state/schema_migrations_test.go`（加 v33 用例）
- `pkg/architecture/testdata/sqlite_sql_snapshot.json`（Step 5 改的两条 SELECT 是该快照守卫的语句，需再生）
- `scripts/test-db.sh`（只在 `./pkg/state/` 那一行追加片段）

**Out of scope**（不要碰）：
- `pkg/config`、`pkg/process`、`pkg/run`、`pkg/gateway`、`pkg/tui`、前端——没有任何调用方在本计划里改。
- `fb_session_image_refusals`（仍按 provider+model 记；图片拒收是模型本身的属性，不随 endpoint 变）。
- `fb_messages.model`（只服务压缩判断，不动）。

## Steps

### Step 1: SQLite 的两张新表 + 一列（`schema.sql`）

在 `fb_config` 表定义之后（`schema.sql:699` 附近）追加，**注释照仓库风格写清用途**：

```sql
-- The install's model configurations: one row per (provider, model, base_url,
-- api_path), shared by every primary agent (owner 2026-11-07). The same model
-- may be configured several times — behind different endpoints, paths or keys —
-- so a model is referred to by this row's id, never by "provider/model".
-- api_key is stored in plaintext (owner ruling) and never leaves the gateway.
-- The cap_* columns override the model catalog for this row; NULL (or '')
-- means "use the catalog".
CREATE TABLE fb_llm_models (
  id TEXT PRIMARY KEY CHECK (TRIM(id) <> ''),
  provider TEXT NOT NULL CHECK (TRIM(provider) <> ''),
  model TEXT NOT NULL CHECK (TRIM(model) <> ''),
  base_url TEXT NOT NULL DEFAULT '',
  api_path TEXT NOT NULL DEFAULT '',
  api_key TEXT NOT NULL DEFAULT '',
  params_json TEXT NOT NULL DEFAULT '',
  cap_images INTEGER CHECK (cap_images IN (0, 1)),
  cap_pdf INTEGER CHECK (cap_pdf IN (0, 1)),
  cap_video INTEGER CHECK (cap_video IN (0, 1)),
  cap_audio INTEGER CHECK (cap_audio IN (0, 1)),
  cap_reasoning INTEGER CHECK (cap_reasoning IN (0, 1)),
  cap_reasoning_off INTEGER CHECK (cap_reasoning_off IN (0, 1)),
  cap_efforts_json TEXT NOT NULL DEFAULT '',
  cap_default_effort TEXT NOT NULL DEFAULT '',
  cap_context_window INTEGER,
  cap_max_output INTEGER,
  -- Which subscription sign-in wrote this row ('' = added by hand) and the
  -- plan inside it ('coding-plan', 'agent-plan-team', …). A subscription row's
  -- identity and key belong to the subscription: pages edit only its cap_*.
  source TEXT NOT NULL DEFAULT '',
  source_plan TEXT NOT NULL DEFAULT '',
  -- 「自定义协议」（owner 2026-11-07，参考 workbuddy）：1 = base_url 即完整请求
  -- 地址，不拼接任何协议路径；只允许 provider='custom' 的行，api_path 存
  -- '/chat/completions'（请求体格式）。不参与唯一性。
  url_verbatim INTEGER NOT NULL DEFAULT 0 CHECK (url_verbatim IN (0, 1)),
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX idx_fb_llm_models_identity ON fb_llm_models(provider, model, base_url, api_path);
CREATE INDEX idx_fb_llm_models_created ON fb_llm_models(created_at, id);

-- Operator-owned variables that ${NAME} references in MCP and LSP server
-- entries resolve from (owner 2026-11-07, replacing ~/.forebrain/.env). A
-- project-scoped entry resolves only from here — never from the process
-- environment — so a repository cannot read the host's secrets.
CREATE TABLE fb_env_vars (
  name TEXT PRIMARY KEY CHECK (TRIM(name) <> ''),
  value TEXT NOT NULL,
  updated_at_ms INTEGER NOT NULL
) STRICT;
```

并给 `fb_session_model_state` 加一列（在 `effort` 后）：

```sql
  -- The fb_llm_models row this selection names. '' for a row written before
  -- model ids existed; readers fall back to provider+model for those.
  model_id TEXT NOT NULL DEFAULT '',
```

**Verify**: `sqlite3 :memory: < pkg/state/schema.sql && echo ok` → `ok`（若本机没有 sqlite3，跳过此验证，Step 4 的测试会覆盖）。

### Step 2: MySQL 与 PG 的同形 DDL

`schema_mysql.sql`（放在 `fb_config` 定义后，同名索引）：

```sql
CREATE TABLE fb_llm_models (
  id VARCHAR(191) NOT NULL PRIMARY KEY,
  provider VARCHAR(191) NOT NULL CHECK (TRIM(provider) <> ''),
  model VARCHAR(191) NOT NULL CHECK (TRIM(model) <> ''),
  base_url TEXT NOT NULL DEFAULT (''),
  api_path VARCHAR(191) NOT NULL DEFAULT '',
  api_key TEXT NOT NULL DEFAULT (''),
  params_json LONGTEXT NOT NULL DEFAULT (''),
  cap_images BIGINT CHECK (cap_images IN (0, 1)),
  cap_pdf BIGINT CHECK (cap_pdf IN (0, 1)),
  cap_video BIGINT CHECK (cap_video IN (0, 1)),
  cap_audio BIGINT CHECK (cap_audio IN (0, 1)),
  cap_reasoning BIGINT CHECK (cap_reasoning IN (0, 1)),
  cap_reasoning_off BIGINT CHECK (cap_reasoning_off IN (0, 1)),
  cap_efforts_json TEXT NOT NULL DEFAULT (''),
  cap_default_effort VARCHAR(32) NOT NULL DEFAULT '',
  cap_context_window BIGINT,
  cap_max_output BIGINT,
  source VARCHAR(64) NOT NULL DEFAULT '',
  source_plan VARCHAR(64) NOT NULL DEFAULT '',
  url_verbatim BIGINT NOT NULL DEFAULT 0 CHECK (url_verbatim IN (0, 1)),
  created_at BIGINT NOT NULL,
  updated_at BIGINT NOT NULL,
  -- base_url has no length the store can promise: keyed over its SHA-256,
  -- exactly as unique (the fb_projects root precedent).
  UNIQUE KEY idx_fb_llm_models_identity (provider, model, (SHA2(base_url, 256)), api_path),
  KEY idx_fb_llm_models_created (created_at, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE fb_env_vars (
  name VARCHAR(191) NOT NULL PRIMARY KEY,
  value LONGTEXT NOT NULL,
  updated_at_ms BIGINT NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
```

`fb_session_model_state` 在 MySQL 加 `model_id VARCHAR(191) NOT NULL DEFAULT '',`。

`schema_postgres.sql`：同列名（含 `source`、`source_plan`，`TEXT NOT NULL DEFAULT ''`；`url_verbatim BIGINT NOT NULL DEFAULT 0` + CHECK），`TEXT`/`BIGINT`，`CHECK` 写在表尾（照该文件 `fb_project_channels` 的写法），
`CREATE UNIQUE INDEX idx_fb_llm_models_identity ON fb_llm_models (provider, model, base_url, api_path);`
`CREATE INDEX idx_fb_llm_models_created ON fb_llm_models (created_at, id);`，
`fb_session_model_state` 加 `model_id TEXT NOT NULL DEFAULT ''`。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/state/ -run 'SchemaDialect' -count=1` → ok。
失败时读报错：它会点名哪张表/列/索引与 SQLite 不一致，照着补齐（不要改测试）。

### Step 3: 迁移 v33（三库）

`stateSchemaVersion = 33`，`stateSchemaMigrations` 末尾加
`{version: 33, apply: migrateStateV32ToV33, applyMySQL: migrateStateV32ToV33MySQL, applyPostgres: migrateStateV32ToV33Postgres}`。

- `migrateStateV32ToV33`（SQLite）：两张表各自照 v32 范本「`sqlite_master` 已有就跳过」再 `CREATE TABLE` + 建索引；
  `fb_session_model_state.model_id` 照 V3ToV4 范本「`pragma_table_info` 已有就跳过」再 `ALTER TABLE … ADD COLUMN model_id TEXT NOT NULL DEFAULT ''`。
- MySQL：`CREATE TABLE IF NOT EXISTS …`（与 Step 2 同文）；加列前查 `information_schema.COLUMNS`
  （`TABLE_SCHEMA = DATABASE() AND TABLE_NAME='fb_session_model_state' AND COLUMN_NAME='model_id'`）已有就跳过。
- PG：`CREATE TABLE IF NOT EXISTS`、`CREATE UNIQUE INDEX IF NOT EXISTS`、`ALTER TABLE fb_session_model_state ADD COLUMN IF NOT EXISTS model_id TEXT NOT NULL DEFAULT ''`。

在 `pkg/state/schema_migrations_test.go` 里照现有 v32 用例（搜 `fb_leases` 找到它）写 v33：
建一个 v32 形状的库 → 打开 → 断言两张表存在、`model_id` 列存在且旧行读出 `''`。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/state/ -run 'Schema|Migrat' -count=1` → ok。

### Step 4: `LLMModelStore`（新文件 `pkg/state/llm_model_store.go`）

导出类型与函数（名字照写，后续计划会按这些名字调用）：

```go
// LLMModel is one fb_llm_models row.
type LLMModel struct {
	ID, Provider, Model, BaseURL, APIPath, APIKey, ParamsJSON string
	CapImages, CapPDF, CapVideo, CapAudio, CapReasoning, CapReasoningOff *bool  // nil = catalog
	CapEfforts      []string // nil = catalog; stored as JSON array text, '' = nil
	CapDefaultEffort string  // '' = catalog / request default
	CapContextWindow, CapMaxOutput *int64 // nil = catalog
	// Source is the subscription id that wrote the row ('' = added by hand);
	// SourcePlan the plan inside it. Neither is part of the identity.
	Source, SourcePlan string
	// URLVerbatim: send requests to BaseURL exactly as stored (「自定义协议」);
	// APIPath still names the wire protocol. Not part of the identity.
	URLVerbatim bool
	CreatedAt, UpdatedAt int64 // unix ms
}

// ErrLLMModelExists reports that another row already has this identity.
type ErrLLMModelExists struct{ ID string }
func (e ErrLLMModelExists) Error() string // "a model with this provider, model, base URL and API path is already configured (<id>)"

var ErrLLMModelNotFound = errors.New("model configuration not found")

type LLMModelStore struct{ DB *sql.DB }

func NormalizeLLMModelIdentity(m *LLMModel)
func (s *LLMModelStore) List(ctx context.Context) ([]LLMModel, error)            // ORDER BY created_at, id
func (s *LLMModelStore) Get(ctx context.Context, id string) (LLMModel, error)    // ErrLLMModelNotFound
func (s *LLMModelStore) FindByIdentity(ctx context.Context, provider, model, baseURL, apiPath string) (LLMModel, bool, error)
func (s *LLMModelStore) Create(ctx context.Context, m LLMModel) (LLMModel, error) // fills ID (NewID("llm") when empty), timestamps
func (s *LLMModelStore) Update(ctx context.Context, m LLMModel) (LLMModel, error) // by m.ID; keeps CreatedAt
func (s *LLMModelStore) Delete(ctx context.Context, id string) error             // ErrLLMModelNotFound when 0 rows
```

规则（逐条写进代码注释）：

1. **规范化**（`NormalizeLLMModelIdentity`，`Create`/`Update`/`FindByIdentity` 入口都先调）：
   - `Provider`：`strings.ToLower(strings.TrimSpace(...))`；
   - `Model`：只 `TrimSpace`，**保留大小写**（有的服务 model 参数区分大小写；自定义模型可带 `vendor/` 前缀，原样保留）；
   - `BaseURL`：`TrimSpace` 后去掉末尾所有 `/`；
   - `APIPath`：`TrimSpace`；非空且不以 `/` 开头就补 `/`；去掉末尾 `/`（单独的 `/` 视为空）。
     **空 api_path 的补全（填默认协议路径）不在本层做**——它需要 provider 知识，由 plan 260 的 `pkg/config` 层负责；本层只保证同一写法得到同一字节。
2. **查重是主路径**：`Create` 先 `FindByIdentity`，命中返回 `ErrLLMModelExists{ID: hit.ID}`；`Update` 命中的行 `ID != m.ID` 时同样返回。
   然后才写；写入报 `dbx.IsUniqueViolation(err)` 时**再查一次**拿到冲突行 id，返回同一个 `ErrLLMModelExists`（并发兜底）。
3. `CapEfforts` 存 JSON 数组文本；`nil` 存 `''`；读出 `''` → `nil`，`'[]'` → 空切片（「明确没有档位」与「用目录」不同）。
4. 三库 SQL 都写成 `dbx.SQL{…}`；MySQL 的 `FindByIdentity` 直接 `WHERE provider=? AND model=? AND base_url=? AND api_path=?`（utf8mb4_bin 精确比较）。
5. 不写日志；**错误信息里不许出现 api_key**。

`pkg/state/llm_model_store_test.go`（测试名都以 `TestLLMModel` 开头，用 `OpenStateForTest`，照
`pkg/state/project_store_test.go` 的开库写法）：

- Create → List 读回全部字段（含六个 `*bool`、`CapEfforts` nil/空/非空三态、两个 `*int64`、`Source`/`SourcePlan`）；
- 同 provider+model、不同 base_url → 两条都能建；同 model、不同 provider → 能建；
- 完全相同四元组 → `ErrLLMModelExists`，且 `ID` 是先建那条；
- 规范化后相同（`"OpenAI"` vs `"openai"`、`base_url` 末尾带 `/`、`api_path` 少前导 `/`）→ `ErrLLMModelExists`；
- **model 大小写不同不算重复**（`LongCat-2.0` vs `longcat-2.0` 可并存）；
- Update 改成与另一行相同的四元组 → `ErrLLMModelExists{ID: 另一行}`；Update 不存在的 id → `ErrLLMModelNotFound`；
- 绕过 store 直接 `INSERT` 一条重复行 → 库报唯一冲突（证明索引真的在）；
- Delete 不存在 → `ErrLLMModelNotFound`。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/state/ -run 'LLMModel' -count=1 -v 2>&1 | grep -c '^--- PASS'` → ≥ 8。

### Step 5: 会话模型行读写 `model_id`

`pkg/state/session_store.go:3940`：

```go
type SessionModelSelection struct {
	Provider string
	Model    string
	Effort   string
}
```

加 `ModelID string`（放第一个）。然后让下面这些语句把它带上（**列表照读出的 grep 逐条改，三个方言都改**）：

- `sessionModelSelectionSelectSQL`（`:442` 附近）、`latestSessionModelSelectionSQL`（`:480` 附近）的 SELECT 列表加 `m.model_id`/`p.model_id`，对应 `Scan` 加 `&sel.ModelID`；
- `saveSessionModelSelectionSQL`（`:550`）与 `copySessionModelSelectionSQL`（`:574`）的列表与值加 `model_id`；
- `SaveSessionModelSelection`（`:3964`）传入 `sel.ModelID`（`TrimSpace`）。**校验不变**：仍要求 Provider 与 Model 非空——
  provider/model 继续写，作为显示与旧行回退用。

`grep -n "fb_session_model_state" pkg/state/session_store.go` 列出的每一处都要看：只读 provider/model 的查询保持能编译即可，
需要扫描的地方补 `model_id`。

**快照守卫（必做）**：`sessionModelSelectionSelectSQL` / `latestSessionModelSelectionSQL` 这两条 SELECT 的 SQLite 字面量
在 `pkg/architecture` 的 `TestSQLiteStatementsUnchanged` 守卫快照里（`pkg/architecture/testdata/sqlite_sql_snapshot.json:297/:304`），
加列后必须再生：`go run ./scripts/sqlite-sql-snapshot -out pkg/architecture/testdata/sqlite_sql_snapshot.json`。
（`saveSessionModelSelectionSQL`/`copySessionModelSelectionSQL` 是方言 switch 拼的动态语句，不在快照里，不触发该测试。）

在 `pkg/state/session_store_test.go` 里找现有的 `SaveSessionModelSelection` 用例，加：写入带 `ModelID` → `SessionModelSelection` 与
`LatestSessionModelSelection` 都读回同一个 `ModelID`；`CopySessionModelSelection` 后目标行 `ModelID` 相同。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/state/ -run 'Session' -count=1` → ok；
`CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture/ -count=1` → ok（快照已再生）。

### Step 6: `EnvVarStore`（新文件 `pkg/state/env_var_store.go`）

```go
type EnvVar struct{ Name, Value string; UpdatedAtMs int64 }
var ErrEnvVarName = errors.New("variable name must match [A-Za-z_][A-Za-z0-9_]*")
type EnvVarStore struct{ DB *sql.DB }
func (s *EnvVarStore) List(ctx context.Context) ([]EnvVar, error)          // ORDER BY name
func (s *EnvVarStore) Map(ctx context.Context) (map[string]string, error)  // name → value
func (s *EnvVarStore) Set(ctx context.Context, name, value string) error   // upsert; name validated
func (s *EnvVarStore) Delete(ctx context.Context, name string) error       // missing = nil
```

名字校验用正则 `^[A-Za-z_][A-Za-z0-9_]*$`（与 `pkg/lsp/instance.go` 的 `envRefPattern` 同一套字符）。
upsert：SQLite/PG `ON CONFLICT(name) DO UPDATE`，MySQL `ON DUPLICATE KEY UPDATE`（本表只有主键一个唯一键，可以用）。
测试 `TestEnvVar…`：Set/覆盖/Map/Delete/非法名字被拒。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/state/ -run 'EnvVar' -count=1` → ok。

### Step 7: 引擎矩阵与全量

在 `scripts/test-db.sh` 的 `engine_arms` 里，把 `"./pkg/state/|…|Project"` 这一行末尾追加 `|LLMModel|EnvVar`。

**Verify**:
- `./scripts/test-db.sh check` → exit 0；
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/state/... -count=1` → ok；
- `go build ./... && go vet ./pkg/state/...` → exit 0；
- 有 docker：`./scripts/test-db.sh up && ./scripts/test-db.sh mysql && ./scripts/test-db.sh postgres` → 全过。

## Test plan

见 Step 3/4/5/6。新增测试都在 `pkg/state`，以 `TestLLMModel*`、`TestEnvVar*` 命名，v33 迁移用例放
`schema_migrations_test.go`，会话行用例放 `session_store_test.go`。

## Done criteria

- [ ] `grep -c "fb_llm_models" pkg/state/schema.sql pkg/state/schema_mysql.sql pkg/state/schema_postgres.sql` 三个文件都 ≥ 1
- [ ] `grep -n "stateSchemaVersion = 33" pkg/state/schema_migrations.go` 有一行
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/state/... -count=1` exit 0
- [ ] 快照已再生：`CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture/ -count=1` exit 0
- [ ] `./scripts/test-db.sh check` exit 0
- [ ] `go build ./...` exit 0
- [ ] `git status --short` 只列出 Scope 里的文件
- [ ] `grep -rn "api_key\|APIKey" pkg/state/llm_model_store.go | grep -i "errorf\|slog"` 无输出（错误与日志不带 key）
- [ ] `plans/README.md` 本行状态已更新

## STOP conditions

- `stateSchemaVersion` 已不是 32（别的计划先占了 33）：停下报告，不要自己改号。
- `TestSchemaDialect*` 要求的改法需要动测试本身才能过。
- MySQL 建唯一键报 key 太长（`Specified key was too long`）：停下报告实际列宽，不要自己把 `model`/`provider` 缩短。
- 发现 `fb_session_model_state` 的写入除 `session_store.go` 外还有别处直接拼 SQL（`schema_migrations.go` 里带版本号的历史迁移语句不算——那是一次性迁移，不要动它们）。

## Maintenance notes

- 本表是全局的：**不要**给它加 agent_id。主代理只在 `fb_config` 的 agents 段里存自己的 `default_model`（值是本表的行 id；plan 260 解析、plan 262 迁移）。
- 唯一性按「规范化后的字节」判断；以后若要放宽/收紧规范化（例如 base_url 大小写），必须同时迁移已有行，否则老行与新写法会并存。
- `cap_*` 列是「覆盖目录」的语义，NULL/'' 永远表示「用 models.json」；运行时如何合并由 plan 263 负责。
- `fb_env_vars` 只给 MCP/LSP 的 `${NAME}` 用（plan 268 接入运行时，plan 265 提供增删改接口）；LLM 的 key **不**从这里取。
