# 187 — 配置入库地基：fb_config 表 + 启动组装 + 每 turn 快照同步 + 存量导入

- **基线**：`6f68cb21`（dev）。写计划时的工作树状态：干净基线上的已提交代码。
- **状态**：TODO
- **依赖**：无（本族 187→188→189→190→191 的第一个）。
- **执行隔离**：默认 in-tree 零 commit（owner 手动审阅提交）；若 owner 指定 worktree，按其指令。

## 0. 背景与目标（为什么做这件事）

owner 裁决（2026-10-30，四条）：

1. **生效语义**：文件热更新（fsnotify watcher + reload 链）将被删除（189）；通过 TUI/Web 写入的配置**每 turn 统一从数据库重读快照**——写入方也不做立即应用，改完下个 turn 生效；正在跑的 turn 永不中途换配置。
2. **存量迁移**：首次启动新版本时把 forebrain.yaml 的运行时配置段**导入数据库并重写 YAML**（YAML 只留启动段；改写前留一份备份）。
3. **YAML 留存最小清单**：`gateway.http_addr`、`gateway.auth`（mode/token）、`credentials.chatgpt`。其余全部段（approval_policy / approvals_reviewer / auto_review / default_permissions / permissions / sandbox_mode / sandbox_workspace_write / compact / cron / agents / hooks / memories / tools / features / lsp / windows）从数据库读写。
4. forebrain.yaml 从此**只能用户手动编辑**；TUI / gateway 接口 / Web 界面只写数据库。
5. **同日三条追加裁决（凭据明文入库，细则见 §1.3）**：provider api_key 等秘钥、ChatGPT/OpenCode 订阅登录后的鉴权信息、MCP 服务的 oauth 与请求头凭据，全部**明文存入数据库、明文读写**。

本计划（187）交付地基：表、Store、启动组装、turn 边界同步、存量导入。**不迁移写端点**（188）、**不删 watcher**（189）、**不做 UI**（190/191）。

## 1. 共享架构（全族一致，各计划重复此节）

### 1.1 表结构（本计划新增，schema 迁移号按仓库惯例取执行时 `stateSchemaVersion+1`，写计划当下即 v24）

当前 schema 最高版本 23（`pkg/state/schema_migrations.go:21` 常量 `stateSchemaVersion = 23`，迁移表最后一条 `{version: 23, apply: migrateStateV22ToV23}`，`schema_migrations.go:63-64`）。新增 version 24，**两张表**：

```sql
CREATE TABLE IF NOT EXISTS fb_config (
  section       TEXT PRIMARY KEY,
  value_json    TEXT NOT NULL,
  updated_at_ms INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS fb_mcp_oauth (
  project_key   TEXT NOT NULL,
  server_name   TEXT NOT NULL,
  value_json    TEXT NOT NULL,
  updated_at_ms INTEGER NOT NULL,
  PRIMARY KEY (project_key, server_name)
);
```

- `fb_config.section` 有两类取值：
  - **Root 段**（16 个，`pkg/config/config.go:119-142` 的顶层段，除 gateway/credentials）：`approval_policy, approvals_reviewer, auto_review, default_permissions, permissions, sandbox_mode, sandbox_workspace_write, compact, cron, agents, hooks, memories, tools, features, lsp, windows`。`value_json`＝该段 Marshal 后的 JSON（与 Root 各段 `json:` tag 一致）。
  - **凭据段**（owner 2026-10-30 追加裁决，不是 Root 的一部分，composeConfig 不合成它们）：`auth.chatgpt`、`auth.opencode` —— `value_json`＝各包**现有 on-disk JSON 形状原样**（ChatGPT 用 `storedAuth` Codex 形状 `pkg/llm/openai/auth.go:59-64`；OpenCode 用 `Credentials` 形状 `pkg/llm/opencode/auth.go:55-64`），迁移零解析损耗、未来导出互操作不变形。
- `gateway` 与 `credentials` **永远不进表**：gateway 段只从 YAML 读；`credentials.chatgpt` 段随裁决三**废弃**（见 §1.3）。
- `fb_mcp_oauth`：MCP oauth 刷新后的 token 覆盖层，替代今天的 `<home>/state/mcp-oauth/[projects/<slug>/]<server>.json` 文件（`pkg/mcp/token_store.go:16-38`）；`project_key` 空串＝全局（与 overlay 的全局目录对应）。

### 1.2 分层

- `pkg/state/config_store.go`（新）：`ConfigStore` 持有 `*sql.DB`，方法：
  - `LoadSections(ctx) (map[string]json.RawMessage, error)` —— 全表读。
  - `UpdateSection(ctx, section string, fn func(cur json.RawMessage) (json.RawMessage, error)) error` —— 单段读-改-写，`BEGIN IMMEDIATE` 事务内完成（写串行化，等价今天 `withConfigWrite` 的文件级互斥，见 `pkg/gateway/api_extra.go:5297-5301`）。**Root 段的写前校验只有 `validateLoadedRoot`；秘密扫描不跑（§1.3）**。
  - `ReplaceAll(ctx, sections map[string]json.RawMessage) error` —— 仅存量导入用。
  - `LoadCredential/SaveCredential`（或等价方法）：`auth.chatgpt`/`auth.opencode` 段与 `fb_mcp_oauth` 行的读写，供 1.4 的 DB 实现调用。
- `pkg/process/config_db.go`（新）：`composeConfig(ctx, env)` —— YAML(`appcfg.Load`) 取 gateway+credentials，DB 取其余段，合成 `appcfg.Root`，跑现有校验（`validateLoadedRoot`，`pkg/config/load.go:110-172`）与 env 展开（`expandRootEnvReferences`）。
- `pkg/config` 不 import `pkg/state`（方向只能是 state→config，已有边：`pkg/state/db.go:15`）。合成逻辑放 `pkg/process`。

### 1.3 凭据与秘密（owner 2026-10-30 三条追加裁决，统称「凭据明文入库」）

**所有凭据明文存入 fb_config / fb_mcp_oauth，明文读写**：

1. **provider api_key**（web 模型服务表单、setup、TUI `/connect` 写入的全部配置，含 api key）——明文存 `agents` 段的 `llm_providers[].api_key`；
2. **ChatGPT / OpenCode 订阅登录后的鉴权信息**（OAuth access/refresh/id token、account、email、plan、org）——明文存 `auth.chatgpt` / `auth.opencode` 段；
3. **MCP 服务的 oauth（client_secret/access_token/refresh_token 等）与请求头凭据（headers 如 x-api-key）**——明文存 `agents` 段 `mcp_servers[].oauth` / `.headers`，刷新 token 明文存 `fb_mcp_oauth`。

配套规则：

- **秘密扫描只属于文件**：`rejectPlaintextConfigSecrets`（`pkg/config/secrets.go:13`）与 `RejectPlaintextMCPSecrets`（`secrets.go:34+`）**只**保留在 forebrain.yaml 的读写路径与项目级 MCP 文件上；fb_config / fb_mcp_oauth 的读写路径**一道都不跑**。段写只保留 `validateLoadedRoot` 的值域校验（approval_policy 等）。
- **`.env` 写入路径作废**（188 删）：今天明文 key 经 `ProviderAPIKeyConfigReference`（`pkg/process/config_io.go:74-89`）写 home `.env`、YAML 只存 `${ENV}` 引用——该模式整体废除。
- **`credentials.chatgpt` 段废弃**：鉴权信息入库后，指向 auth.json 的路径指针失去意义。YAML 最小清单缩为 `gateway.http_addr` + `gateway.auth` 两项（owner 裁决三的字面结果；`SetCredentialsPath` 在 open.go:287 的安装随之删除）。
- **`${ENV}` 展开保留在 compose 时刻**（对 DB 段同样生效）：用户仍可手写 `${ENV}` 引用，明文字面量原样通过；恰好长得像 `${FOO}` 的明文 key 会被展开——极小概率，接受。
- **先例**：状态库早已承载明文凭据——项目渠道 bot token 就存在 fb 项目行 config JSON 里（`pkg/gateway/channels.go:48-74`）。后果（一句，入维护注意）：状态库文件及其 `state/backups/` 备份从此承载 live 凭据（provider key、OAuth token、MCP secret），文件权限模型沿用状态库现状。

### 1.4 凭据存储端口（Layer 0 不能 import state）

`pkg/llm/openai`、`pkg/llm/opencode`、`pkg/mcp` 都在 Layer 0/低层，**不得 import `pkg/state`**。照 open.go:241-246 的既有模式（组合根在 llm 的端口后面装实现，`llm.SetHTTPTransportWrapper`/`SetDebugLogger` 先例）：

- 各包声明自己的存储端口：`openai.SetCredentialsStore(store)`、`opencode.SetCredentialsStore(store)`、`mcp.SetOAuthOverlayStore(store)`；`store` 接口＝`Load(kind/key) (json.RawMessage, error)` / `Save(kind/key, raw) error` 形状（各包自定，语义＝「给我凭据字节/存这些凭据字节」）。
- **默认实现＝现有文件路径**（未安装时行为与今天完全一致——包内单测、无 Environment 的路径不受影响）。
- `pkg/process.Open` 在 DB open 之后安装 **DB 实现**（`pkg/process/config_db.go`：`auth.chatgpt`/`auth.opencode`/`fb_mcp_oauth` 的读写适配）。包的 Load/Save **何时**切到已安装的 store、`/connect` 与 web 登录流何时改写 DB，归 188（写面迁移）；187 只交付存储、端口、导入与安装能力。

## 2. 现状事实（本计划要动的每一处，均已对基线核实）

| # | 事实 | 位置 |
|---|---|---|
| F1 | 启动序：`appcfg.Load(cfgPath)` → `openaiauth.SetCredentialsPath(loaded.Credentials.ChatGPT)` → launchProject 解析 → `effectiveFor` → `StartupCheck` → `activePrimaryAgent(root, cfgRoot)`（读 agents 段）→ telemetry → `state.OpenStateFromHome` | `pkg/process/open.go:266-330` |
| F2 | `OpenStateFromHome` 对配置**零数据依赖**（`_ = cfg`） | `pkg/state/db.go:61-64` |
| F3 | TUI 与 gateway 的每个 turn 都走 `Service.Submit`：TUI 两处 `s.Core.Submit`（已核）；gateway ws/channel 由 `serve_run.go:176-181` 注释明说；**cron 例外待核**——`Scheduler.fire` 经 `Deps.StartFire` 回调派发（`pkg/turn/scheduler.go:291-318`），其绑定是否最终落到 `Service.Submit` 需执行器 grep `StartFire` 的装配点确认，未落到的入口必须同样接入 sync | `pkg/turn/submit.go:72`；`pkg/tui/chat_session.go:2388`、`chat_turn.go:996`；`pkg/gateway/serve_run.go:176-181`；`pkg/turn/scheduler.go:309` |
| F4 | 热更新链（本计划**不删**，但要换数据源）：watcher → `RequestConfigReload` → `ConfigManager` → `reloadConfig`（读文件）→ `applyRawConfigLocked` | `pkg/process/config_reload.go:21-111,148-206` |
| F5 | `applyRawConfigLocked` 是全量应用器：RawConfig → effectiveFor → StartupCheck → `Runner.LoadConfig` → LSP Reconcile → pool Propagate → SetMemoryMode → sandbox 刷新 → hooks/skills → `OnConfigReload` → `RunEventConfigReloaded` | `pkg/process/config_reload.go:213-291` |
| F6 | MCP 列表会话内冻结（reload 只 replay 不 reconcile） | `pkg/process/config_reload.go:233-239` |
| F7 | fsnotify watcher 启用点两处（189 才删） | `pkg/tui/notify.go:1734`；`pkg/gateway/serve_run.go:65` |
| F8 | 凭据存储现状四件：① provider 明文 key 经 `ProviderAPIKeyConfigReference` 写 home `.env`（env 名由 `ProviderKeyEnvMap(provider,key)` 派生，`pkg/process/config_io.go:64-89`），配置只存 `${ENV}` 引用，`.env` 在 Load 时经 `applyDotEnvAfterNormalize` 合入；② ChatGPT OAuth＝Codex 形状 `auth.json`（`<home>/auth.json` 或 `credentials.chatgpt` 覆盖路径，`pkg/llm/openai/auth.go:34-71,104-131`）；③ OpenCode OAuth＝`<home>/opencode.json`（`pkg/llm/opencode/auth.go:28,55-90`）；④ MCP oauth 刷新 token 覆盖层＝`<home>/state/mcp-oauth/[projects/<slug>/]<server>.json`（`pkg/mcp/token_store.go:16-38,98-146`，0600），MCP headers/oauth 值在 YAML 被强制 `${ENV}` 引用（`RejectPlaintextMCPSecrets`） | 见左 |

## 3. 设计

### 3.1 启动序调整（open.go）

新序（把 DB open 提到 agent 解析之前）：

```
home/seed/cfgPath 解析（不变）
appcfg.Load(cfgPath)                     → 只为 gateway/credentials 两段 + 存量导入判断
openaiauth.SetCredentialsPath（过渡期保留，读的是 YAML credentials 段；188 切 DB 读取时删）
launchProject 解析（不变）
state.OpenStateFromHome(ctx, root, nil)   → 提前；cfg 参数本来就没人用（F2）
importLegacyConfigIfNeeded(...)           → 新：见 3.3（YAML 段 + 凭据文件 + MCP overlay）
installCredentialStores(...)              → 新：把 §1.4 的 DB 实现装进 openai/opencode/mcp 端口
dbRoot, err := composeConfig(...)         → 新：YAML(gateway) + fb_config(其余段)
cfgRoot := effectiveFor(dbRoot, launchProject)
StartupCheck → activePrimaryAgent（现在读的是 dbRoot）→ 其余不变
```

注意：`OpenOptions.Config`（调用方自带 Root）路径保留原语义——它绕过文件与 DB，仅供测试；合成只发生在 `opts.Config == nil` 的生产路径。`Environment.RawConfig` 存**合成后的 dbRoot**（pool 的 per-project 重推导 `effectiveFor(env.RawConfig, cfgLaunch)` 输入不变，`pkg/process/runner_pool.go:1065,1182`）。

**过渡期语义（187 与 188 之间）**：187 装好 DB store 但 openai/opencode/mcp 的 Load/Save **仍走文件**（默认实现，切换归 188）——期间 token 刷新写文件、DB 副本可能滞后，188 切换时按 mtime 较新者做一次性终同步（见 188 §3 步骤 4）。`credentials.chatgpt` 段同理：187 的 YAML 重写**暂保留**该段（过渡期 boot 的 `SetCredentialsPath` 仍要读它），188 切 DB 读取时才删段、删调用。

### 3.2 每 turn 快照同步（核心新语义）

1. `turn.New` 新增 option `WithConfigSync(sync func(context.Context) error)`；`Service.Submit`（`pkg/turn/submit.go:72`）入口处、在任何排队/执行之前调用；`sync` 返回 error 时**该 turn 直接失败**并把这个错误回给提交方（存储侧写入前已校验，读失败=库坏了，fail-closed）。
2. `Environment.SyncConfigFromDB(ctx)`（新，`pkg/process/config_db.go`）：
   - `composeConfig` 重读 → 规范化 JSON（固定字段序 marshal）→ sha256；
   - 与当前有效快照 hash 相同 → 直接返回（常态零重建：一次 sqlite 读 + 一次 hash 比较）；
   - 不同 → `reloadMu` 下走 `applyRawConfigLocked(dbRoot)`（F5 全量应用器原样复用，MCP 冻结语义 F6 原样保留）。
   - 锁序不变：不得在持有 `OnConfigReload` 回调所取锁时调用（现有注释约束，`pkg/process/config_reload.go:336-338`）。
3. 两个 surface 装配：TUI `pkg/tui/notify.go` 的 `turn.New(...)` 与 gateway `pkg/gateway/serve_run.go` 的 `turn.New(...)` 各加 `turn.WithConfigSync(env.SyncConfigFromDB)`。随后按 F3 核对 `StartFire` 装配点：若 cron fire 不经 `Service.Submit`，在 `StartFire` 回调入口同样先调 `env.SyncConfigFromDB`。
4. watcher 的文件 reload（F4）同样改走 `composeConfig`（不再 `appcfg.Load` 整文件当 raw）——187 落地后**运行时段的唯一来源永远是 DB**；watcher 触发的 reload 与 turn 同步走同一条合成路径，YAML 手编 gateway/auth 的热应用作为 189 删除前的已知过渡行为（见 README 审核记录）。

### 3.3 存量导入（一次性）

`importLegacyConfigIfNeeded(ctx, sqlDB, home, cfgPath, yamlRoot)`，在 DB open 后、compose 前调用，导入三部分：

**A. YAML 运行时段 → fb_config**
- 条件：`fb_config` 全表为空 **且** YAML Root 的 16 个运行时段中任一非零值。
- 动作：各非零段 marshal 进 `ReplaceAll` → 重写 `cfgPath`：只保留 `gateway` + `credentials` 两段（credentials 为过渡期保留，见 §3.1；`appcfg.Save` 语义按段过滤后写，0600 原子写），文件头加一行注释 `# runtime configuration lives in the state database (fb_config)` → 改写前复制一份 `forebrain.yaml.pre-db-<UTC时间戳>` 到同目录 → slog Info 汇总（导入了哪些段、备份路径）。
- **`.env` 引用解析**（凭据明文入库裁决的字面要求）：段内 `llm_providers[].api_key`、`mcp_servers[].headers`、`mcp_servers[].oauth` 的 `${NAME}` 引用，凡 `NAME` 能在 home 的 `.env`（`homepkg.DefaultEnvPath`，与 `applyDotEnvAfterNormalize` 同一来源）解析到的，**替换为明文值入库**；解析不到的保留引用（compose 时仍展开）。
- 幂等：`fb_config` 非空保证只跑一次；备份已存在则不覆盖。

**B. 登录凭据文件 → `auth.chatgpt` / `auth.opcode` 段**
- 条件：对应 DB 段为空。
- 来源：ChatGPT 用 `openaiauth.ResolvePath(home, yamlRoot.Credentials.ChatGPT)` 解析出的路径（**覆盖路径感知**——用户把 auth.json 指到 `~/.codex/auth.json` 的场景不丢）；OpenCode 用 `<home>/opencode.json`。文件字节原样入段（§1.1 的形状约定）。
- **文件本身不动**（留在磁盘作为用户备份；188 切 DB 读写后它们成为死数据，README 记录一句即可，不删用户文件）。

**C. MCP oauth overlay → `fb_mcp_oauth`**
- 条件：表空。遍历 `<home>/state/mcp-oauth/`：顶层 `<server>.json` → `project_key=''`；`projects/<slug>/<server>.json` → `project_key=<slug 反解>`（slug 规则见 `token_store.go:25-31`；反解不唯一的跳过并 Warn）。文件不动。
- 失败语义：A/B/C 任一导入事务失败 → 启动失败并报错（不能带着空 DB 配置静默跑）。

## 4. 步骤（每步带验证）

1. **schema 迁移**：`pkg/state/schema_migrations.go` 加 `{version: stateSchemaVersion+1, apply: migrateStateVNToVN+1}` 建两表（版本号按落地时实际值，参照 `migrateStateV19ToV20` 的写法，`schema_migrations.go:822-849`）。
   验证：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state -run TestMigrations -count=1`（若该名不存在，跑 `./pkg/state -count=1` 全包）。
2. **ConfigStore**：`pkg/state/config_store.go` + `config_store_test.go`（roundtrip、UpdateSection 并发写串行、事务回滚不落半段）。
   验证：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state -count=1`。
3. **composeConfig + 校验接线**：`pkg/process/config_db.go`；单测：DB 段覆盖 YAML 段、gateway 段恒来自 YAML、非法段（如 `approval_policy: bogus`）被 `validateLoadedRoot` 拒、**段 JSON 含明文 `api_key` / MCP headers secret 被接受并原样保留**（§1.3 裁决，秘密扫描不跑）。
   验证：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/process -run TestComposeConfig -count=1`。
4. **启动序调整 + 存量导入**：改 `pkg/process/open.go`；单测：老 YAML（含 agents/hooks）启动 → fb_config 有行、YAML 被重写为两段、备份存在、二次启动不重复导入；`.env` 里带 provider key + YAML 引用 `${NAME}` → 导入后 DB 段是**明文**；`auth.json`（含覆盖路径）/`opencode.json`/`state/mcp-oauth/` 存在 → DB 段/表有值且磁盘文件未动。
   验证：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/process -count=1`。
5. **凭据端口 + DB 实现 + 安装**：`openai/opencode/mcp` 三包各加 `SetXxxStore` 端口（默认实现＝现有文件路径，行为不变）；`config_db.go` 提供 DB 实现；Open 在 DB open 后安装。单测：安装后走 DB 读写（用 fake store 验证端口形状）、未安装走文件（既有测试不红）。
   验证：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/openai ./pkg/llm/opencode ./pkg/mcp ./pkg/process -count=1`。
6. **WithConfigSync + SyncConfigFromDB**：改 `pkg/turn/submit.go`（option 定义 + Submit 入口调用）、新增 `Environment.SyncConfigFromDB`；单测：hash 相同不触发 `Runner.LoadConfig`（计数器证明）、hash 不同触发全量应用、sync 错误使 Submit 失败。watcher reload 改合成源。
   验证：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/turn ./pkg/process -count=1`。
7. **两个 surface 装配** + 包图：`bash scripts/package-graph.sh` 无 diff（若 pkg/turn 增加了对外 import 则先修）。
   验证：`go vet ./...` 无输出；`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./... -count=1`（全量，核心路径变更）。

## 5. 边界

- **不动**：写端点全部（188）、watcher/ConfigManager 删除（189）、`applyRawConfigLocked` 内部逻辑（原样复用）、MCP 冻结语义、`AdoptConfig`/`AdoptLaunchTrust`、UI。
- **187 不切凭据 IO**：openai/opencode/mcp 的 Load/Save 仍走文件（端口装好、默认实现不变）；`ProviderAPIKeyConfigReference` 的 `.env` 写入路径仍在（188 删）；`SetCredentialsPath` 调用保留（188 删）。切换、终同步、余下文件读者（`login_api.go`、`pkg/process/models.go`、`pkg/run/config.go:528`）全在 188。
- **明确不改**：`OpenOptions.Config` 测试旁路；`appcfg.Save`（YAML 写仍存在，导入重写用）；项目级 `.forebrain/mcp_servers.yaml` / `.mcp.json`（仓库数据，不属 home 配置，owner 已确认不在本期）。

## 6. 完成判据（机器可查）

1. `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./... -count=1` 全绿。
2. `go vet ./...` 无输出；`scripts/package-graph.sh` 无 diff。
3. scratch home 手测脚本：写一个含 `agents:`/`hooks:` 段 + `.env` 带 provider key 的 forebrain.yaml + `auth.json`/`opencode.json`/`state/mcp-oauth/x.json` → 启动 → `sqlite3 $home/state/forebrain.state.sqlite 'select section from fb_config'` 列出非空段（含 `auth.chatgpt`/`auth.opencode`）；`fb_config.agents` 的 value_json 里 api_key 是**明文**（无 `${ENV}`）；`fb_mcp_oauth` 有行；forebrain.yaml 只剩 gateway/credentials；备份文件存在；三个凭据文件原样未动。
4. turn 同步手测：改 fb_config 一行（合法值）→ 下一个 Submit 后 `fb_runs`/事件流出现 `config_reloaded`（或 Runner 配置指针变化，任一可观测点）。

## 7. 测试计划

- `pkg/state/config_store_test.go`：参照 `pkg/state/db_test.go` 的建库方式。
- `pkg/process/config_db_test.go`：参照 `pkg/process/config_reload_test.go` 的 Environment 构造。
- `pkg/turn`：Submit 入口 sync 失败用例参照现有 Submit 早期失败用例。

## 8. 维护注意

- fb_config 段集合与 `Root` 顶层字段必须同步演进：新增 Root 段时同时决定它进 YAML 还是进 DB，并更新本文件 1.1 的清单。
- `Service.Submit` 的 sync 调用是所有配置生效的唯一边界——未来新增绕过 Submit 的执行入口（如新调度器）必须同样接入。
- **状态库（及其 `state/backups/` 备份）承载 live 凭据**（provider key、OAuth token、MCP secret，owner 2026-10-30 裁决明文入库）：库文件即敏感文件，任何"把状态库发给别人/入库到 git"的操作都等于泄露凭据——排障时先脱敏。

## 9. 逃生口（STOP 条件）

- 发现 `activePrimaryAgent` 或其下游在 DB open 前还有别的配置消费者（除 `SetCredentialsPath`）→ 停，报告，不要硬挪。
- 发现 `pkg/turn` 已有等价 pre-submit 钩子（避免两个边界）→ 停，报告命名。
- 存量导入在真实 home 上试跑（只读副本）失败 → 停，带现场回来。

## 追加裁决（owner 2026-10-30，C 端精简）

`approval_policy`、`approvals_reviewer`、`auto_review`、`sandbox_mode`、`sandbox_workspace_write.go_cache_mode`、`sandbox_workspace_write.exclude_tmpdir_env_var`、`sandbox_workspace_write.exclude_slash_tmp`、`windows` **只在 forebrain.yaml 手配**，web 与 TUI 都不显示、程序不写、harness 只从 YAML 读；配置界面不出现英文配置键、不要「默认」选项（布尔两态、默认值预选）。对本计划的具体改动与待定项见 [191 §8.4](191-web-config-ui.md)——执行或续做本计划前先按它修订段集合。

**再追加（owner 2026-10-30，第三轮）**：`approval_policy` 与 `sandbox_mode` **彻底不再是配置项**（YAML 也不能配），由新增入库配置项「审批预设」（只读 / 默认 / 完全访问）取代，见 [191 §8.5](191-web-config-ui.md)；本计划的段集合、存量导入与合成逻辑须按它修订，含老用户迁移。

**第四轮（owner 2026-10-30）**：预设 id 统一 `read_only`/`workspace_write`/`full_access`；权限档案整块去掉；信任度只收紧；继续精简；「配置文件」编辑器保留并改写磁盘文件；状态库迁移规则保留——见 [191 §8.6](191-web-config-ui.md)。本计划受影响：段集合（再减 `permissions`、`default_permissions`，加 `approval_preset`）与 v25 数据迁移。

**最终裁决（owner 2026-10-30，取代同日「不碰文件」的说法）**：§0 第 2 条与 §3.3 存量导入**整条保留**（导入库 + 备份 + 重写 YAML）；重写时把不能通过 forebrain.yaml 配置的内容全部删掉，只留「文件支持项」白名单（网关启动项 + 只在文件里配的高级项，清单见 [191 §8.4、§8.6 第 4 条、§8.7](191-web-config-ui.md)）——§3.3 「只保留 gateway + credentials 两段」改为「只保留白名单项」；已取消的审批策略、沙箱模式、权限档案直接删、不换算。`config.Load` 对白名单外的键静默忽略。

**配置来源定稿（owner 2026-10-31）**：原型保留的所有配置项只存数据库、不认文件；文件只放少数高级项——见 [191 §8.13](191-web-config-ui.md)。本计划受影响：导入范围与 YAML 重写的保留白名单按 191 §8.13 的两张清单。

**追加（owner 2026-10-31）**：自动压缩阈值按百分比配置；存库配置项一律显示库里的值；初始化数据库时写入全部默认值——见 [191 §8.14](191-web-config-ui.md)。本计划受影响：建库即写入全部默认值；首次导入改用独立导入标记，顺序为默认值 → 文件覆盖 → 重写文件；迁移补齐默认值。

**再精简（owner 2026-10-31）**：`gateway.auth.mode`（固定 token）、`windows.sandbox_private_desktop`（固定开）、`sandbox_workspace_write.go_cache_mode`（固定 isolated）、`exclude_tmpdir_env_var` / `exclude_slash_tmp`（固定可写）五项删除——见 [191 §8.24](191-web-config-ui.md)。本计划受影响：YAML 重写白名单去掉这五项；v25 迁移剔除它们。

**再精简二（owner 2026-10-31）**：`gateway.public_url` 删除（改自动识别）、`windows` 段整段删除（Windows 固定受限令牌沙箱）、`features.network_proxy` 整段删除——见 [191 §8.25](191-web-config-ui.md)。本计划受影响：YAML 重写白名单只剩 `gateway.http_addr`、`gateway.auth.token`；v25 迁移删 `features.network_proxy`。
