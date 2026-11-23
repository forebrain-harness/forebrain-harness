# Plan 262: 旧模型配置一次性迁进表，所有写入改写表

> **Executor instructions**: 按步骤顺序执行，每步的验证命令通过后再做下一步。遇到
> 「STOP conditions」里的情况就停下来报告，不要自己发挥。**不要 commit / push / merge**：
> 改动留在工作区里，做完报告（仓库主人手动提交）。做完后把 `plans/README.md` 里本计划那一行
> 的状态改成 DONE，并附一句执行摘要——除非派你来的评审说索引由他维护。
>
> **Drift check (run first)**:
> `git diff --stat 0a698bf2..HEAD -- pkg/process/open.go pkg/process/setup.go pkg/process/agentyaml.go pkg/process/config_import.go pkg/gateway/api_extra.go pkg/tui/setup.go pkg/home/seed.go`
> → 只允许出现 plan 260/261 改过的 `pkg/tui/setup.go`、`pkg/gateway/api_extra.go`（会话模型接口那段）。其它变化即对照「Current state」，对不上 STOP。

## Status

- **Priority**: P1
- **Effort**: L
- **Risk**: HIGH（改用户的库：搬 key、改引用；失败会让用户丢模型配置）
- **Depends on**: plans/259-…、plans/260-…、plans/261-…（表、视图、`LegacyLLMModelID`、会话行 `model_id`）
- **Category**: migration
- **Planned at**: commit `0a698bf2`, 2026-11-07
- **Executed**: 2026-11-08, worktree `.worktrees/exec-259-268`, 基线 `d9c45060`（未提交）——**DONE**，判据实测：
  `go build ./...` 0；`pkg/config`/`pkg/process`/`pkg/gateway`/`pkg/tui`/`pkg/run`/`pkg/turn`/`pkg/state`/`pkg/home` 全包 ok；
  迁移组 13 个 PASS（`-run 'LLMModelsMigrate|UpsertLLMModel|SetAgentDefaultModel'`）；`test-db.sh check` 0；
  三条 done grep 成立（`ApplyAgentLLM*` 零命中、seed 无 `llm_providers`、`StatusGone` 恰一处）；gofmt 空；
  `pkg/architecture` 只剩本基线三条既有红（`pkg/llm`/`pkg/tool` 各 32、`pkg/skill/audit_skill_test.go`——另一会话已在主树修掉，合并后应消失）。
  **偏差（均写入报告）**：
  ① 三个新生产文件并成一个 `pkg/process/llm_models.go`（+ `llm_models_test.go`）：`pkg/process` 已在 30，31 是上限。
  ② `pkg/state/config_store.go` 增 `CurrentSectionValidator()`（超出文件清单）：`Execute` 把自己的临时库绑到进程级段校验器后关闭，
     该进程之后每次配置写入都会 `sql: database is closed`；现在改成「绑定 → 写 → 还原」。
  ③ 删掉了 `pkg/process/runtime.go` 的 `SaveConfig`（唯一调用方是旧 `Execute`，留着就是第二条整段覆写写路径）。
  ④ **子代理 `subagents[].model` 改成行 id 需要先放宽 loader**：`validateSubagentModel` 原本只认 `provider/model`，写 id 会让
     每次 agents 段写入被拒。已按 `default_model` 的既有做法给 `ValidateAgentSubagents` 加 `refResolves func(string) bool`
     （`pkg/config/load.go` 与 `pkg/gateway/subagents_api.go` 两个调用点传 `ResolveModelRef` 闭包），迁移据此把 label 改写为 id。
  ⑤ `memories.extract_model` 实际存**裸模型名**或 `current`（不是计划写的 `provider/model`）：裸名/`current`/行 id 一律不动。
  ⑥ 身份比较跟随 259 的规范化（provider 折叠、model 保大小写）：同一模型换大小写重连现在会新增一行（旧行为是原地更新）。
  ⑦ `state.LLMModelStore.Create` 不补默认 `api_path`——plan 265/266 的「新增」入口必须走 `UpsertLLMModel`，否则同一行会以两种 identity 并存。
  **中间态**：`PUT /api/providers` 返回 410，旧「模型」页只读，网页 ChatGPT/OpenCode 登录后的「保存模型」暂不可用，直到 265+266；
  `frontend/e2e/providers-keys.spec.ts`、`opencode-login.spec.ts` 预期失败（未运行、未修，`frontend/` 零改动）。
  **未做**：run-forebrain driver + fake provider 的真机冒烟（迁移那半边已由真实 `Open` 路径的用例覆盖，见计划 Step 6）。

## Why this matters

owner 2026-11-07：大模型配置必须**全部持久化到数据库表、读写数据库**；改状态库结构必须迁移用户旧库
（owner 早前裁定：不写迁移、清库重来的做法已废除）。plan 260 让运行时能同时读表和旧 `llm_providers`，
本计划把旧数据**真正搬进表**、把引用改成行 id，并让每一个写入点改写表——之后 `fb_config` 的 agents 段里
不再出现 `llm_providers`。

**中间态说明（必须写进报告）**：旧网页「模型」页的保存接口（`PUT /api/providers`）在本计划里改为返回 410，
页面只能看不能存，直到 plan 265（新接口）+ plan 266（新页面）落地。网页的 ChatGPT/OpenCode 登录后「保存模型」
也走这个接口，同样暂时不可用。`frontend/e2e/providers-keys.spec.ts`、`opencode-login.spec.ts` 预期失败，**不要修**，由 plan 266 重写。

## Current state

- 每个主代理的旧数据都在 `fb_config` 表 `section='agents'` 那一行的 JSON 里：
  `definitions.<id>.llm_providers[]`（`provider/model/api_key/base_url/api_path/params`，`model` 可能是数组）、
  `definitions.<id>.default_model`（"provider/model"）、`definitions.<id>.subagents[].model`（"provider/model"）；
  记忆段 `section='memories'` 的 `extract_model` / `consolidation_model`（值是 `"current"` 或 "provider/model"）。
- api_key 的现状：owner 2026-10-30 起新写入的是**明文**（`pkg/process/setup.go:68-72` 注释），老安装可能还是
  `${OPENAI_API_KEY}` 这种引用，值在 `~/.forebrain/.env` 或进程环境里。迁移必须把引用解析成明文写进表
  （引用解析一次，之后表里只有明文；owner 裁定「环境变量不再提供 LLM key」指的是运行时，不包括这次一次性迁移）。
- Open 的启动顺序 `pkg/process/open.go:352-410`：开库 → `importLegacyConfigIfNeeded`（yaml 运行段导入 fb_config）
  → `installCredentialStores` → `InstallConfigSectionValidator` → `ensureStoredConfigDefaults` → `syncLegacyCredentialFilesIfNeeded`
  → `composeConfig`。其中 `importLegacyConfigIfNeeded`/`ensureStoredConfigDefaults`/`syncLegacyCredentialFilesIfNeeded`/`composeConfig`
  四步在 `if opts.Config == nil` 守卫**内**；`installCredentialStores` 与 `InstallConfigSectionValidator` 无条件执行（核对时不要把这
  一点当成 drift）。
- 多副本：迁移类操作用 `dbx.AcquireMigrationLock(ctx, db)`（`pkg/state/dbx/tx.go:170`，返回 `release func()`）串行化。
- 读写 fb_config 段：`state.ConfigStore{DB}` 的 `LoadSections`（`pkg/state/config_store.go:147`）、
  `UpdateSections(ctx, names, fn)`（`:228`，事务内读改写，**会跑段校验器**——组合后跑 `FinishLoad`）。
- 写入点（今天都写 agents 段的 `llm_providers`）：
  1. `pkg/process/setup.go:34-104` `Execute(rt, opts)`：TUI 首次设置与 `/connect` 都走它（`pkg/tui/setup.go:135`、`:1479`），
     内部 `ApplyAgentLLMToConfigForAgent`（`pkg/process/agentyaml.go:58`，按 provider+model「有则改、无则追加」）再 `SaveConfig`。
     TUI 在 `pkg/tui/setup.go:1499` 随后 `c.session.SelectModel(ctx, sessionID, turn.ModelChoice{...})` 让当前会话切到新模型。
  2. `pkg/gateway/api_extra.go:6570` `handleProviders`：GET 读 `def.LLMProviders`（`:6583-6584`），PUT（`:6610` 起）整表合并写 agents 段。
- `ReadAgentLLMDefaults*`（`pkg/process/agentyaml.go:18-51`）读代理的默认 provider/model/params 给 TUI 设置页当缺省值。
- 种子 yaml `pkg/home/seed.go:113-138` 带一个空占位 `llm_providers: [{provider: "", model: ""}]`。
- 旧 `.env` 读取：`godotenv.Read(path)`（`github.com/joho/godotenv`，`pkg/process/config_import.go:26` 已经 import）；
  路径 `homepkg.DefaultEnvPath(home)`（`pkg/home/data.go`，文件名 `.env`）。

## Commands you will need

| Purpose | Command | Expected |
|---|---|---|
| 构建 | `go build ./...` | exit 0 |
| process | `CGO_ENABLED=1 go test -tags fts5 ./pkg/process/... -count=1` | ok |
| 受影响包 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/... ./pkg/tui/... ./pkg/run/... -count=1` | ok |
| 全量 | `CGO_ENABLED=1 go test -tags fts5 ./... -count=1` | ok |
| 引擎矩阵 | `./scripts/test-db.sh check`；有 docker 时 `./scripts/test-db.sh up && ./scripts/test-db.sh mysql && ./scripts/test-db.sh postgres` | 全过 |

## Scope

**In scope**:
- `pkg/process/llm_models_migrate.go`（新建）、`pkg/process/llm_models_migrate_test.go`（新建）
- `pkg/process/legacy_dotenv.go`（新建：一次性迁移专用的旧 `.env` 读取器，plan 268 也会复用）
- `pkg/process/llm_models_write.go`（新建：写表的进程级 API）
- `pkg/process/open.go`（调用迁移）、`pkg/process/setup.go`、`pkg/process/agentyaml.go`
- `pkg/gateway/api_extra.go`（只改 `handleProviders`）
- `pkg/tui/setup.go`（只改 `Execute` 之后选模型那几行，带上 id）
- `pkg/home/seed.go`（删占位 `llm_providers`）
- `scripts/test-db.sh`（`./pkg/process` 行追加片段，见 Step 6）
- 对应 `_test.go`

**Out of scope**:
- `pkg/config` 里旧字段 `LLMProviders`/`DefaultModel` 的**类型定义**——保留，yaml 导入与测试还在用它；视图（plan 260）照常提升。
- `.env` 的其它用途（网关令牌、渠道、搜索 key、MCP/LSP）——plan 268。
- 新网关接口与新网页——plan 265/266。
- `normalize()` 给 main 塞占位条目的逻辑（`pkg/config/load.go:633-640`）——plan 268 随 `.env` 一起清理。

## Steps

### Step 1: 旧 `.env` 读取器（`pkg/process/legacy_dotenv.go`）

```go
// readLegacyDotEnv reads <home>/.env for the one-time migrations that move its
// values into the state database (plans 262, 268). Nothing else reads the file:
// a missing or unreadable file is an empty map, never an error.
func readLegacyDotEnv(home string) map[string]string
```

用 `godotenv.Read(homepkg.DefaultEnvPath(home))`；`\r\n` 先替换为 `\n` 再解析的话照 `pkg/config/envfile.go:20` 的做法用 `godotenv.Unmarshal`。

```go
// resolveLegacySecret turns a stored value into the plaintext it stood for: a
// whole-value ${NAME} reference resolves from the legacy .env first, then the
// process environment; any other value is already plaintext. ok is false when
// a reference resolved to nothing.
func resolveLegacySecret(value string, dotenv map[string]string) (string, bool)
```

只认**整个值**就是一个 `${NAME}` 的情况（与 `pkg/config/load.go` 的 `isSingleEnvReference` 同义）；部分包含引用的值原样返回 `ok=true`。

### Step 2: 迁移函数（`pkg/process/llm_models_migrate.go`）

```go
// migrateLegacyLLMModels moves every model configuration still stored in the
// agents section (llm_providers) into fb_llm_models and rewrites every
// reference to a row id: agents' default_model, subagents' model, and the
// memory models. It is gated on content, not on a marker — a legacy yaml
// imported into a fresh home later (test harnesses do this) is migrated on the
// next start the same way — and is idempotent: a row whose identity is already
// in the table is reused, never duplicated.
func migrateLegacyLLMModels(ctx context.Context, db *sql.DB, home string) error
```

算法（**严格按顺序**；整个函数包在 `release, err := dbx.AcquireMigrationLock(ctx, db); defer release()` 里）：

1. `sections := ConfigStore.LoadSections`；把 `agents` 段 unmarshal 进 `appcfg.AgentsSection`，`memories` 段进 `appcfg.MemorySection`（段不存在就是零值）。
2. **是否需要迁移**：任一定义的 `LLMProviders` 里有 provider 或 model 非空的条目；或任一 `DefaultModel`、`Subagents[].Model`、记忆两个模型字段是**旧 label**
   （含 `/`、不等于 `appcfg.MemoryModelCurrent`、且不是表里任何行的 id）。都没有 → 若存在只有占位条目的 `llm_providers` 就只做第 7 步的剥离；否则直接返回 nil。
3. 组一个内存 `root := appcfg.Root{Agents: agents, Memories: memories}`，`root.LLMModels` = 表里现有行（用 plan 260 的 `llmModelsFromRows`）。
   `view := appcfg.ConfiguredLLMs(&root)`——**视图里旧条目的 id 已经是 `LegacyLLMModelID`**。
4. **建引用映射（在剥离之前算）**：
   - 每个定义 `id`：`ref := def.DefaultModel`；为空且该定义自己有非占位旧条目 → 取它自己展开后第一条的身份；
     解析：`appcfg.ResolveModelRef(&root, id, ref)`（或按身份在 view 里找）→ 新值 = 行 id。解析不到的旧 label → 置空并 `slog.Warn("dropping a default model that matches no configured model", "agent", id)`。
   - 每个 `Subagents[i].Model` 非空：`ResolveModelRef(&root, id, model)` → 行 id；解析不到 → 置空 + Warn。
   - 记忆两字段：`"current"`/空不动；旧 label → `ResolveModelRef(&root, "main", v)` → 行 id；解析不到 → `"current"` + Warn。
   - 会话行回填表：`SELECT DISTINCT s.agent_id, m.provider, m.model FROM fb_session_model_state m JOIN fb_sessions s ON s.id=m.session_id WHERE m.model_id=''`
     （三方言各写一份 `dbx.SQL`），每组 `ResolveModelRef(&root, agent_id, provider+"/"+model)` → 行 id。
5. **写行**：对 view 里每个 `ID` 以 `llm-legacy-` 开头的条目：
   - `key, ok := resolveLegacySecret(entry.APIKey, dotenv)`；`!ok` → `key = ""` 并 `slog.Warn("a model key reference did not resolve; set the key on the models page", "provider", p, "model", m)`（**不打印引用名以外的任何东西，绝不打印值**）；
   - `store.FindByIdentity(...)` 命中 → 跳过（已经迁过或用户新加过同身份行）；否则 `store.Create(state.LLMModel{ID: entry.ID, …, APIKey: key, ParamsJSON: string(entry.Params)})`。
     `Create` 的 `ErrLLMModelExists` 视为已存在、跳过。
   - 登录类 provider（`config.CredentialsBackedProvider`：chatgpt / opencode / opencode-go）的行写 `Source` = provider 名（opencode 的 Console 行也记 `opencode`），其余旧行 `Source` 为空——它们是用户手填的。
   - **登录类行的 `api_path` 必须补成真实值**（owner 2026-11-07：订阅行也要显示真实路径，不再有「由登录决定」）：
     `chatgpt` → `/responses`（客户端走 Responses 协议，base 是 `openai.CodexBaseURL`）；
     `opencode` / `opencode-go` → 该模型的协议对应的路径（`opencode.ProtocolFor(provider, model)`：`ProtocolResponses` → `/responses`、`ProtocolAnthropic` → `/v1/messages`、其它 → `/chat/completions`）；`ProtocolGoogle` 的模型（`gemini-*`，走 Gemini 原生 `generateContent`，没有 OpenAI 风格路径）与 `ProtocolFor` 报 `ErrUnsupportedProtocol` 的模型（`jev-*`，非对话模型）**保留原 `api_path` 并记一条 `slog.Warn`**，界面把这类行显示为「原生协议」而不是路径块；其余查不到的按 `DefaultModelMeta` 的默认（`/chat/completions`）。
     旧行里已经是这些值的保持原样；为空或其它值一律按上表重写（它们的值今天由代码推断，表里存着却与代码不一致就会让界面和请求各说一套）。登录类行的 `base_url` 同样补成注册表里的默认（`chatgpt` → `openai.CodexBaseURL`，OpenCode 两站 → `opencode.ProviderBaseURL(provider)`）。
6. **回填会话行**：每组映射 `UPDATE fb_session_model_state SET model_id=? WHERE model_id='' AND provider=? AND model=? AND session_id IN (SELECT id FROM fb_sessions WHERE agent_id=?)`（三方言）。
7. **改写段**：`ConfigStore.UpdateSections(ctx, []string{"agents", "memories"}, fn)`：在 fn 里重新 unmarshal 当前值（不要用第 1 步的副本——事务内以库为准），
   对每个定义：`LLMProviders = nil`，`DefaultModel`/`Subagents[i].Model` 换成第 4 步算好的新值；记忆两字段换新值；marshal 回去。
   段不存在就不新建。**校验器会跑**：如果它拒绝（例如别的校验失败），把错误原样返回——Open 会失败并报出原因，用户数据不变（行已写入也无害，下次启动幂等）。
8. 结束 `slog.Info("moved model configurations into the state database", "rows", n)`（只打条数）。

在 `pkg/process/open.go` 里，`syncLegacyCredentialFilesIfNeeded` 那段之后、`composeConfig` 之前，加：

```go
	if opts.Config == nil {
		if err := migrateLegacyLLMModels(ctx, sqlDB, root); err != nil {
			_ = sqlDB.Close()
			return nil, fmt.Errorf("move model configurations into the state database: %w", err)
		}
	}
```

**Verify**: `go build ./... && CGO_ENABLED=1 go test -tags fts5 ./pkg/process/ -run 'ImportLegacy|ConfigDB|Startup' -count=1` → ok（既有测试不应受影响；受影响就看是不是它们断言了 agents 段里还有 `llm_providers`——那是本计划有意的改变，改断言并在报告列出）。

### Step 3: 迁移测试（`pkg/process/llm_models_migrate_test.go`，测试名以 `TestLLMModelsMigrate` 开头）

用真实状态库（照 `pkg/process` 里 `TestImportLegacy*` 的开库与写 fb_config 的写法），每例先往 `agents`/`memories` 段写旧 JSON：

1. main 两条（同 provider/model、不同 base_url）+ `default_model` 指第二条的 provider/model（label 只能匹配到第一条——断言迁移后 `default_model` 是**第一条**的 id，与今天 `EffectiveDefaultLLM` 的行为一致）；表里两行，id 都是 `LegacyLLMModelID`；agents 段不再有 `llm_providers`。
2. 两个主代理各有同身份条目、key 不同 → 表里只有一行，key 是 main 的；两代理的 `default_model` 都是这一行 id。
3. key 是 `${FOO_KEY}`：`.env` 里有 → 表里明文；`.env` 没有但 `t.Setenv("FOO_KEY", …)` → 明文；都没有 → 表里空串，日志有 Warn 且日志文本里**不含**任何 key 字样的值。
4. `subagents[].model` 与 `memories.extract_model` 的 label → 行 id；`consolidation_model: current` 不变；指不到的 label → 子代理置空、记忆变 `current`。
5. 会话行：先插一个 session + `fb_session_model_state(provider, model, model_id='')` → 迁移后 `model_id` 是对应行 id。
6. 幂等：连跑两次，表行数不变，第二次不改任何段（比较 `updated_at_ms` 或段字节）。
7. 表里已有一行同身份（用户先加的）→ 不新建，引用指向**已有行的 id**。
8. 只有占位 `[{}]` → 不建行，占位被剥掉。
9. 登录类行的路径补全：旧 yaml 里 `chatgpt` 条目 `api_path` 为空 → 迁移后表里是 `/responses`；`opencode-go` 上一个 Anthropic 协议的模型 → `/v1/messages`，一个 Responses 协议的 → `/responses`，其余 → `/chat/completions`。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/process/ -run 'LLMModelsMigrate' -count=1 -v 2>&1 | grep -c '^--- PASS'` → ≥ 8。

### Step 4: 写表的进程级 API（`pkg/process/llm_models_write.go`）

```go
// LLMModelInput is what a surface hands in to configure one model.
type LLMModelInput struct {
	Provider, Model, BaseURL, APIPath string
	// APIKey nil keeps the stored key on an update; "" clears it.
	APIKey *string
	ParamsJSON *string // nil keeps; "" clears
	// Source/SourcePlan: the subscription that wrote the row (plan 264 sets
	// them); "" from setup and /connect with a typed key. An update keeps a
	// non-empty stored source unless the input names one.
	Source, SourcePlan string
}

// UpsertLLMModel stores one model by identity (normalized with
// appcfg.NormalizeLLMModelIdentity): an existing row with the same provider,
// model, base URL and API path is updated in place — /connect signing in
// again replaces the key it had — and any other input adds a row. It returns
// the row id. Surfaces that must refuse a duplicate instead (the web "add"
// dialog) call the store's Create directly and get state.ErrLLMModelExists.
func UpsertLLMModel(ctx context.Context, db *sql.DB, in LLMModelInput) (string, error)

// SetAgentDefaultModel stores modelID as agents.definitions.<agent>.default_model.
// The id must name a row; the definition must exist, except "main", which is
// created (Primary: true) the way setup always did.
func SetAgentDefaultModel(ctx context.Context, db *sql.DB, agentName, modelID string) error
```

`UpsertLLMModel` 校验：provider、model 非空；非登录类 provider 时 base_url 非空、key（新建时）非空——报错文字沿用 `Execute` 现有的（`"api_key required…"` 等）。
**api_path 先过 `appcfg.NormalizeLLMModelIdentity` 补全再查重**（否则 `""` 与 `/chat/completions` 会被当成两条）。

**Verify**: 新单测 `TestUpsertLLMModel*`：同身份两次 → 一行、key 更新、id 不变；`APIKey: nil` 更新时 key 保留；不同 base_url → 两行。`go test` 通过。

### Step 5: 把写入点切到表

1. `pkg/process/setup.go` `Execute`：删掉 `ApplyAgentLLMToConfigForAgent` + `SaveConfig` 那段（`:68-86`），换成：
   - `ensureStartupConfigFile(rt.ConfigPath)` 与开库：照 `SaveConfig`（`pkg/process/runtime.go:127-152`）的开库方式拿到 `*sql.DB`（抽一个小函数 `openStateForWrite(rt)` 复用，不要复制粘贴两份）；
   - `id, err := UpsertLLMModel(ctx, db, LLMModelInput{...})`（`opts.APIKey` 为空且是登录类 provider → `APIKey: ptr("")`；为空且非登录类 → 前面已报错；`UpdateParams` 为假 → `ParamsJSON: nil`）；
   - 该代理**当前没有能解析的默认**（`appcfg.EffectiveDefaultLLMFor(composed, agent) == nil`，composed 用 `StartupConfig`/`composeConfigFrom` 读）时才 `SetAgentDefaultModel(ctx, db, agentName, id)`——与今天「首个条目即默认、后加的不抢默认」一致；
   - `Report` 加 `ModelID string`，填 `id`；`MissingFields` 校验改为校验**刚写入这一行**是否完整（provider/model/base_url/key-或登录类）。
2. `pkg/tui/setup.go:1499` 附近：`turn.ModelChoice{...}` 加 `ID: rep.ModelID`（`rep` 是 `process.Execute` 的返回；若该处拿不到 `rep`，就从调用链把它传过来）。
3. `pkg/process/agentyaml.go`：`ReadAgentLLMDefaults*` 改为读组合配置（`StartupConfig(ctx, home)`）后 `appcfg.EffectiveDefaultLLMFor`；删掉 `ApplyAgentLLMToConfigForAgent`、`applyAgentLLMFields`、`agentLLMDefaults` 别名等不再有调用方的函数，以及只测它们的测试（`grep -rn "ApplyAgentLLMToConfigForAgent" --include='*_test.go' pkg` 找到后整段删除，在报告里列出删除的测试名）。
4. `pkg/gateway/api_extra.go` `handleProviders`：
   - GET：`providers := providerRowsFor(config.ConfiguredLLMs(&cfg))`，`defaultModel` 用 `config.EffectiveDefaultLLMFor(&cfg, active.ID)` 拼 "provider/model"；其余不动。
   - PUT：整个分支换成
     `http.Error(w, "model configuration moved to the state database; this page is replaced by the models page", http.StatusGone); return`
     并在函数注释写明「TEMPORARY until plan 265/266 replace this handler」。PUT 专用的辅助函数（`mergeProviderEntries`、`storedKeyForRow` 等）**先不删**（plan 266 一起删），只要能编译；`go vet` 报未使用就删。
5. `pkg/home/seed.go`：`defaultForebrainYAML` 删掉 `llm_providers` 那三行（`:130-132`；`main:` 下若因此为空，整段 `definitions: main:` 也删）。
   跑 `CGO_ENABLED=1 go test -tags fts5 ./pkg/home/ -count=1`，有测试断言种子内容就按新内容改。

**Verify**: `go build ./... && CGO_ENABLED=1 go test -tags fts5 ./pkg/process/... ./pkg/gateway/... ./pkg/tui/... ./pkg/home/... -count=1` → ok。
gateway 里断言 PUT 合并行为的既有测试（`grep -ln "handleProviders\|/api/providers" pkg/gateway/*_test.go`）：PUT 用例改成断言 410；GET 用例按全局列表改断言。逐条列进报告。

### Step 6: 引擎矩阵与全量

`scripts/test-db.sh` 的 `"./pkg/process|Lease|ImportLegacy|ConfigDB|Startup|PeerInbound"` 行追加 `|LLMModelsMigrate|UpsertLLMModel`。

**Verify**:
- `./scripts/test-db.sh check` → exit 0
- `CGO_ENABLED=1 go test -tags fts5 ./... -count=1` → ok
- 有 docker：MySQL、PG 两腿全过
- 真机冒烟（隔离 home，按仓库 `run-forebrain` 技能的 `driver.sh` 起 fake provider）：用 driver 写的旧式 yaml（含 `llm_providers` + `${FAKE_LLM_KEY}`）起一次 → `sqlite3 <home>/state/state.db "SELECT id, provider, model, length(api_key) FROM fb_llm_models"` 有一行、key 非空；`SELECT value_json FROM fb_config WHERE section='agents'` 不含 `llm_providers`；发一条消息能拿到 fake 回复。没有 tmux/python 环境就把这一条写进报告的「手动验收清单」。

## Test plan

Step 3 的 8 例、Step 4 的 upsert 例；gateway GET/PUT(410) 改写的用例；`pkg/process` 既有 setup 测试（`grep -ln "Execute(" pkg/process/*_test.go`）改成断言表里的行与 `default_model` 是 id。

## Done criteria

- [ ] `go build ./...`、`CGO_ENABLED=1 go test -tags fts5 ./... -count=1` exit 0
- [ ] `./scripts/test-db.sh check` exit 0
- [ ] `grep -rn "ApplyAgentLLMToConfigForAgent\|applyAgentLLMFields" --include='*.go' pkg` 无输出
- [ ] `grep -n "llm_providers" pkg/home/seed.go` 无输出
- [ ] 迁移后每一行 `api_path` 都符合上表（登录类与 `ProtocolFor` 一致；Gemini 原生协议与 `jev-*` 保留原值并有 WARN；其余不为空）
- [ ] `grep -n "StatusGone" pkg/gateway/api_extra.go` 一处（providers PUT）
- [ ] `grep -rn "slog\.\(Warn\|Info\|Error\)" pkg/process/llm_models_migrate.go | grep -i "key\b\|api_key\|APIKey"` 只允许出现文案里的英文单词 key，不允许把变量 `key`/`APIKey` 作为日志字段值
- [ ] 报告含：改动/删除的既有测试清单、中间态说明（旧页面只读、两个 e2e 预期失败）、真机冒烟结果或手动验收清单
- [ ] `plans/README.md` 本行已更新

## STOP conditions

- `UpdateSections` 的校验器拒绝迁移后的段，且原因不是本计划引入的（例如老库里本来就有别的非法值）——停下报告，不要绕过校验器。
- 发现 `default_model` / `subagents[].model` / 记忆模型之外，还有别的配置字段存 "provider/model"（例如 hooks 的 `model`、批处理）且运行时用它找模型——停下列出。
- `dbx.AcquireMigrationLock` 在 SQLite 下不可用或语义不是「跨进程互斥」。
- 迁移在任何一个引擎上需要 DDL（不应该需要，表由 259 建好）。

## Maintenance notes

- 迁移按内容触发、可重复跑：以后谁再往 agents 段写 `llm_providers`（不应该），下次启动就会被搬走。
- `readLegacyDotEnv` 是全仓库**唯一**允许读 `.env` 的地方（plan 268 之后），只服务一次性迁移；不要给运行时用。
- 旧 providers PUT 返回 410、相关合并代码暂留，plan 266 删除整个旧处理器与旧页面。
- `UpsertLLMModel`（同身份覆盖）与网页「添加」（同身份拒绝，plan 265）是**两种有意不同**的语义：前者是重新登录/重新 /connect，后者是用户新建一条。
