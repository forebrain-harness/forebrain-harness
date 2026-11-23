# Plan 268: 删除 home 的 `.env`——语义与代码全部去掉，旧值一次性搬进库

> **Executor instructions**: 按步骤顺序执行，每步的验证命令通过后再做下一步。遇到
> 「STOP conditions」里的情况就停下来报告，不要自己发挥。**不要 commit / push / merge**：
> 改动留在工作区里，做完报告（仓库主人手动提交）。做完后把 `plans/README.md` 里本计划那一行
> 的状态改成 DONE，并附一句执行摘要——除非派你来的评审说索引由他维护。
>
> **Drift check (run first)**:
> `git diff --stat 0a698bf2..HEAD -- pkg/config/envfile.go pkg/config/secrets.go pkg/home/seed.go pkg/home/data.go pkg/lsp/instance.go pkg/mcp/stdio_secure.go pkg/process/config_io.go deploy docs/deploy`
> → `pkg/home/seed.go` 只允许有 plan 262 的改动（删占位 `llm_providers`）；其余必须为空。对不上即 STOP。

## Status

- **Priority**: P1
- **Effort**: L
- **Risk**: HIGH（动到网关登录令牌、渠道密钥、搜索 key、MCP/LSP 的取值；迁移改用户的 home 与库）
- **Depends on**: plans/259（`EnvVarStore`）、262（`readLegacyDotEnv`、LLM 迁移已先跑）、265（变量接口）；建议在 266/267 之后执行
- **Category**: migration
- **Planned at**: commit `0a698bf2`, 2026-11-07

## Why this matters

owner 2026-11-07：「forebrain harness home 里的 `.env` 文件的语义和代码逻辑必须要删除，不再使用 `.env` 保存和加载 api key」。裁定（全部选推荐）：
- **整个 `.env` 全删**：代码不再读写它（唯一例外：一次性迁移读一次旧文件）；
- LLM key 已在模型表（plan 262）；**环境变量完全不提供 LLM key**（运行时不再按 `PROVIDER_API_KEY` 兜底，`${VAR}` 也不展开到模型行）；
- 渠道密钥、网页搜索 key → 库里明文；网关登录令牌 → `forebrain.yaml` 明文（文件 0600）或进程环境变量（Docker/K8s）；
- MCP/LSP 的 `${名称}`：**项目级条目只从库里的「变量」表取**（不能读进程环境——克隆来的仓库会借此读走宿主密钥），**全局条目先查变量表再查进程环境**；
- 迁移时旧 `.env` 里不属于 LLM/渠道/搜索/网关令牌的变量全部搬进变量表，旧文件改名留档。
- Docker/K8s：部署后登录网页在「模型」页添加模型；部署文档与清单删掉 LLM key 变量。

## Current state（逐个 `.env` 触点）

- 读入与合并：`pkg/config/envfile.go`（全文件）——`MergeDotEnvToProcess`、`applyDotEnvAfterNormalize`、`applyMergedEnvOverrides`（网关令牌 `FOREBRAIN_GATEWAY_TOKEN`/`GATEWAY_TOKEN`；`TAVILY_API_KEY`、`BRAVE_SEARCH_API_KEY`、`BAIDU_WEB_SEARCH_API_KEY`、`DEEPSEEK_API_KEY` 填网页搜索/抓取）、`applyLLMProvidersFromMap`。
  调用：`FinishLoad`（`pkg/config/load.go:80-89`）、`pkg/run/runner.go:881-890`（`MergeDotEnvToProcess`）。`Root.SourceFiles` 的注释提到 `.env`（`pkg/config/config.go:103-106`）。
- `${VAR}` 展开：`expandRootEnvReferences(r)`（`pkg/config/load.go:182`）用反射遍历 Root **所有字段**，`expandBracedEnvReferences` 用 `os.Getenv`。注意：它会遍历到 plan 260 新加的 `Root.LLMModels`。
- 运行时 LLM 兜底：`pkg/run/config.go` `NewLLMFromYAML` 里 `apiKey == ""` → `appcfg.ProviderAPIKeyLookup(os.Getenv, prov)`、`baseURL == ""` → `ProviderBaseURLLookup`；`pkg/config/agent_llm.go:398-408` 两个 Lookup；`pkg/process/provider.go:11-20` `ProviderKeyEnvMap`；`pkg/config/load.go` 的 `providerKeyEnvName`、`foreignProviderKey`、`restoreSliceSecretsWithIdentity` 等 `.env` 时代的密钥回填逻辑（`:308-496`）。
- 写入：`pkg/home/seed.go:25-56` `UpsertEnvVar`、`:70-75` 首启把网关令牌写进 `.env`、种子 yaml `token: "${FOREBRAIN_GATEWAY_TOKEN}"`（`:113-116`）；`pkg/home/data.go:46-52` 建 home 时创建空 `.env`；
  `pkg/process/config_io.go:81-205` `SecretConfigReferenceForOnboard`、`storeSecretEnvReference`、`ChannelSecretEnvName*`、`RemoveChannelSecretsForProject`；网关 `storeChannelSecretRefs`（`pkg/gateway/api_extra.go:6146-6162`，渠道保存时把密钥挪进 `.env`）、`providerKeyHint`（`:6551-6566`，读 `.env` 解析引用）。
- 读取：`pkg/mcp/stdio_secure.go:51-87` `envLookupForServer`/`projectScopedEnvMap`（项目级只读 `.env`，全局 `os.LookupEnv`）；`pkg/lsp/instance.go:44-112` `EnvSpec.FromProject` 与 `projectEnvLookup`（同义）；
  `pkg/process/config_import.go:163-168`（yaml 旧导入时读 `.env`）；`pkg/tui/setup.go:686`（文案「Saved to … .env … never in forebrain.yaml」）、`:1573`（启动报错提示去看 `.env`）。
- 守卫文案：`pkg/config/secrets.go:61`、`pkg/lsp/project.go:63`、`pkg/mcp/project_config.go:127`（「use ${ENV_NAME} or ~/.forebrain/.env」）。
- 渠道表注释 `pkg/state/schema.sql:47-55`（及 MySQL/PG 同处）：说密钥只存 `${FOREBRAIN_CH_*}` 引用、值在 `.env`。
- 部署：`deploy/docker/entrypoint.sh:23-90`（令牌写 `.env`）、`deploy/k8s/manifests/secret.yaml`（`FOREBRAIN_LLM_API_KEY` 与 `.env` 种子）、`deploy/k8s/manifests/deployment.yaml:11,86-89`（init 容器拷 `.env`）、`deploy/docker/compose.yaml`、`deploy/compose/multi-replica.yaml`、
  `docs/deploy/README.md`、`linux-host.md`（`:89-105`、`:175`、`:231`）、`docker.md`（`:56-63`、`:169`）、`kubernetes.md`。
- 旧 `.env` 读取器：plan 262 的 `readLegacyDotEnv(home)`、`resolveLegacySecret` 就在 `pkg/process/llm_models.go:69`/`:87`（**没有** `legacy_dotenv.go` 这个文件）；网关另有一处直接 `godotenv.Read(<home>/.env)`：`pkg/gateway/api_extra.go:6264`（`providerKeyHint`）。

## Commands you will need

| Purpose | Command | Expected |
|---|---|---|
| 构建 | `go build ./...` | exit 0 |
| 全量 | `CGO_ENABLED=1 go test -tags fts5 ./... -count=1` | ok |
| 引擎矩阵 | `./scripts/test-db.sh check`；有 docker 时 mysql/postgres 两腿 | 全过 |
| 前端（文案变化时） | `cd frontend && pnpm typecheck && pnpm test` | 全过 |
| 残留检查 | 见 Done criteria 的 grep | 无输出 |

## Scope

**In scope**：Current state 列出的全部 Go 文件与测试、`pkg/process/dotenv_migrate.go`（新建）+ 测试、`pkg/state/schema*.sql` 的渠道表**注释**、部署脚本/清单/文档、`go.mod`（`github.com/joho/godotenv` 若只剩 `legacy_dotenv.go` 一处使用则保留；全仓库无其它使用后确认它只被迁移读取）；验收脚本 `scripts/acceptance/web_e2e.sh`、`scripts/multi-replica-test.sh`、`scripts/acceptance/native_advisor_workflows.sh`（.env 分支，见 Step 5b）；`pkg/config/channels.go:95`、`pkg/config/file_keys.go:59`、`pkg/process/config_db.go:93/:120/:260/:373` 描述 `.env` 合并的陈旧注释（只改注释）。

**Out of scope**：模型表与其接口；网页页面（变量页在 plan 266 已做）。

## Steps

### Step 1: 一次性搬迁（`pkg/process/dotenv_migrate.go`）

```go
// migrateLegacyDotEnv moves what <home>/.env held into the state database and
// the configuration file, then renames the file aside: after this nothing
// reads .env again. Gated on the file existing; idempotent per value.
func migrateLegacyDotEnv(ctx context.Context, db *sql.DB, home, cfgPath string) error
```

在 `pkg/process/open.go` 里紧跟 plan 262 的 `migrateLegacyLLMModels` 之后调用（同样 `if opts.Config == nil`、失败即 Open 失败）。整个函数在 `dbx.AcquireMigrationLock` 下。步骤：
1. `env := readLegacyDotEnv(home)`；文件不存在 → 返回 nil。
2. **网关令牌**：读 yaml（`appcfg.LoadPersisted(cfgPath)`）；若 `gateway.auth.token` 是 `${FOREBRAIN_GATEWAY_TOKEN}`（或 `${GATEWAY_TOKEN}`）**且进程环境里没有该变量**且 `env` 里有 → 把 yaml 的该值改成明文（只改这一键：读原文、按 yaml 节点替换后原子写，0600；找不到稳妥的「只改一键」写法就用 `appcfg` 现有的 yaml 写入函数并确认它不会把运行段写回文件）。进程环境里有 → 不动（Docker/K8s 照旧从环境取）。
3. **渠道密钥**：遍历所有项目的 `fb_project_channels.config_json`，把其中整值为 `${NAME}` 的字段用 `resolveLegacySecret(v, env)` 换成明文；解析不到 → 保持原样并 `slog.Warn("a channel secret reference did not resolve; re-enter it on the channel page", "project", id, "kind", kind)`。重算 `account_key`（网关的 `channelAccountKey` 逻辑；把它挪到 `pkg/process` 或复制其调用），写回。
4. **网页搜索/抓取 key**：`TAVILY_API_KEY`、`BRAVE_SEARCH_API_KEY`、`BAIDU_WEB_SEARCH_API_KEY`、`DEEPSEEK_API_KEY` 在 `env` 里有、且 agents 段 `defaults.web_search.<x>.api_key`（DeepSeek 还有 `defaults.web_fetch.deepseek.api_key`）为空 → 用 `ConfigStore.UpdateSection("agents", …)` 写入明文（照 `applyMergedEnvOverrides` 今天的映射，`pkg/config/envfile.go:117-163`，token 分支在 `:126`）。
5. **其余变量 → 变量表**：`env` 里除去 ①网关令牌两个名字 ②已被第 3、4 步消费的名字 ③形如 `<PROVIDER>_API_KEY`/`<PROVIDER>_BASE_URL` 且 provider 是注册表 id 或曾出现在模型表里的 LLM 变量（plan 262 已把用到的解析进模型表）之外，全部 `EnvVarStore.Set`（已存在同名则不覆盖）。
6. 把 `.env` 改名为 `.env.migrated-YYYYMMDDHHMMSS`（同目录，权限不变）。`slog.Info("moved .env values into the state database", "variables", n, "renamed_to", base)`（不打印任何值与变量名列表）。

测试 `pkg/process/dotenv_migrate_test.go`（`TestDotEnvMigrate*`，真实状态库 + 临时 home）：令牌三种情况（环境有/无、.env 有/无）；渠道引用解析与未解析；搜索 key 写入且不覆盖已有；其余变量进变量表、LLM 变量与令牌不进；文件被改名；再跑一次无副作用。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/process/ -run 'DotEnvMigrate' -count=1` → ok。

### Step 2: 删运行时的 `.env` 读入与 LLM 环境兜底

- 删 `pkg/config/envfile.go`（全部）；`FinishLoad` 只剩 `expandRootEnvReferences` + 校验；删 `pkg/run/runner.go:881-890` 的合并调用。
- `expandRootEnvReferences` 改签名为 `expandRootEnvReferences(r *Root, lookup func(string) (string, bool))`：**跳过 `Root.LLMModels`**（模型行永不展开，owner：环境变量不提供 LLM key）；
  `FinishLoad(r)` 用 `os.LookupEnv`；新增 `FinishLoadWith(r, lookup)`，`pkg/process/config_db.go` 的 `composeConfigFrom` 用「变量表 → 进程环境」的 lookup（`EnvVarStore.Map` 读一次）。
- `NewLLMFromYAML` 删两处 Lookup 兜底；删 `ProviderAPIKeyLookup`、`ProviderBaseURLLookup`、`providerEnvKey/providerEnvBaseURL`、`process.ProviderKeyEnvMap` 及仅为它们服务的测试；
  `pkg/config/load.go` 里 `.env` 时代的密钥回填（`restorePersistedSecrets` 中针对 provider 条目的 `foreignProviderKey`、`providerKeyEnvName`、`keyClearedByWrite` 等）——只删「为 `${ENV}` 引用服务」的分支，保留与 yaml 文件安全写回相关的部分；拿不准就 STOP。
- `normalize()` 给 main 塞空占位 `llm_providers` 的逻辑（`pkg/config/load.go:633-640`，注释写「slot to merge .env values into」）删除；随之调整依赖它的测试。

### Step 3: MCP / LSP 从变量表取值

- `pkg/mcp/stdio_secure.go`：`envLookupForServer(homeDir, srv)` 改为接收一个 `vars map[string]string`（由调用方从 `EnvVarStore.Map` 取，调用链从拿得到 `*sql.DB` 的地方传下来；若 mcp 包拿不到库，就在 `pkg/process` 组装 MCP 时把 map 注入 `mcp` 的配置结构）：
  项目级 → 只查 `vars`；全局 → 先 `vars` 再 `os.LookupEnv`。删 `projectScopedEnvMap`。
- `pkg/lsp/instance.go`：`EnvSpec` 加 `Vars map[string]string`，`FromProject` 时只查 `Vars`，否则先 `Vars` 再进程环境；删 `projectEnvLookup` 的 `.env` 读取。
- 守卫文案三处改为「use ${NAME} and set it under Settings › Variables」（中文界面若有对应文案同步改：`设置 · 变量`）。
- 测试：项目级引用一个只在进程环境里有的变量 → **解析不到**（安全回归测试，必须有）；变量表里有 → 解析到；全局条目两处都有时取变量表。

### Step 4: 写 `.env` 的代码全部删除

- `pkg/home/seed.go`：删 `UpsertEnvVar`；首启生成令牌后**写进 yaml 明文**：种子模板的 `token` 行改为占位，写文件前替换成生成的令牌；`pkg/config/secrets.go` 的明文守卫对 `gateway.auth.token` 放行（只放行这一个路径，加测试）。
- `pkg/home/data.go`：删创建空 `.env` 的那段；`DefaultEnvFile`/`DefaultEnvPath` 改名为 `LegacyEnvFile`/`LegacyEnvPath`，注释写明「只给一次性迁移用」，并修正所有引用（应只剩 `legacy_dotenv.go`）。
- `pkg/process/config_io.go`：删 `SecretConfigReferenceForOnboard`、`storeSecretEnvReference`、`ChannelSecretEnvName*`、`RemoveChannelSecretsForProject` 及调用点（项目删除时不再需要清 `.env`）。
- 网关：`storeChannelSecretRefs` 删除——渠道保存直接存明文（掩码回传规则不变）；`providerKeyHint` 不再解析 `${…}`（值就是明文；仍是 `${…}` 的旧值直接整段掩码）。
- `pkg/process/config_import.go:163-168`：旧 yaml 导入不再读 `.env`——导入时 `${VAR}` 原样进 fb_config，随后由 plan 262 / 本计划的迁移解析（迁移在导入之后跑）。
- `pkg/tui/setup.go:686` 文案改为「Saved on this machine only.」（owner 2026-11-07：删掉的语义当从未存在——原计划里的「never in forebrain.yaml」是在向用户解释一个已经消失的存储位置，不许出现；界面文案只讲当前事实，不提 `.env`、不提 `forebrain.yaml`、不写「不再…」）；`pkg/tui/setup.go:1573` 一带的启动报错去掉 `.env` 提示，改为「Add a model with /connect, or on the web Models page.」
- 渠道表注释（三份 schema 文件）改为「secret fields are stored in plaintext (owner 2026-11-07); pages only ever see a mask」。**只改注释，不改 DDL**（`TestSchemaDialect*` 只比结构）。

### Step 5: 部署与文档

- `deploy/docker/entrypoint.sh`：不再写 `.env`；令牌来自环境变量 `FOREBRAIN_GATEWAY_TOKEN`（yaml 里保持 `${FOREBRAIN_GATEWAY_TOKEN}`，由进程环境展开）或首启生成后写进 yaml。
- `deploy/k8s/manifests/secret.yaml`：删 `FOREBRAIN_LLM_API_KEY` 与 `.env` 种子，只留网关令牌；`deployment.yaml` 的 init 容器不再拷 `.env`；compose 文件同理。
- `docs/deploy/*.md`：所有 `.env` 说明改为「令牌在环境变量或 forebrain.yaml」；新增一节「部署后添加模型」：登录网页 → 模型 → 添加模型（或订阅登录）；排障表里 `.env` 相关行改写。

### Step 5b: 验收脚本改走新通道

- `scripts/acceptance/web_e2e.sh:142` 现在把 `FOREBRAIN_GATEWAY_TOKEN` 写进 `$HOME_DIR/.env`：改为以进程环境变量传给网关进程（或写进隔离 home 的 `forebrain.yaml` 明文，0600）——脚本行为不变，任务级 e2e 照跑。
- `scripts/multi-replica-test.sh:71-76` 往 `$home/.env` 写网关令牌与 `FAKE_LLM_KEY`（注释明说 `${ENV_NAME}` 从 home 的 `.env` 解析）：令牌同上改进程环境；`FAKE_LLM_KEY` 改为直接往隔离 home 的状态库插一行 `fb_llm_models`（`sqlite3 <home>/state/state.db`，provider/model/base_url/api_path 照脚本现有 fake 端点；若脚本的 yaml 走旧 `llm_providers` + `${FAKE_LLM_KEY}` 写法，照 plan 262 的迁移语义改为直接插表），yaml 里的 `${…}` 引用同步改掉。
- `scripts/acceptance/native_advisor_workflows.sh:1249-1257` 删掉从 `~/.forebrain/.env` source `ZHIPUAI_API_KEY` 的第一分支（保留 `FOREBRAIN_E2E_ZHIPU_KEY` 与 `e2e-zhipu.env` 兜底两条）。
- 顺带把 `pkg/config/channels.go:95`、`pkg/config/file_keys.go:59`、`pkg/process/config_db.go:93/:120/:260/:373` 描述 `.env` 合并的陈旧注释改掉（只改注释，不改逻辑）。

**Verify**: `bash -n` 三个脚本通过；`FOREBRAIN_E2E_SPEC=<任一现存 spec> scripts/acceptance/web_e2e.sh` 跑通（266 落地后换 `e2e/settings-models.spec.ts`）。

### Step 6: 全量与残留检查

**Verify**:
- `go build ./... && CGO_ENABLED=1 go test -tags fts5 ./... -count=1` → ok
- `./scripts/test-db.sh check` → exit 0；有 docker 时两腿全过
- 见 Done criteria 的 grep 全部无输出
- 真机冒烟：拿一个**带旧 `.env`**（含网关令牌、一个渠道密钥引用、TAVILY key、一个 MCP 用的 `GITHUB_TOKEN`）的 home 副本启动一次 → `.env` 被改名；令牌、渠道、搜索 key 在库/yaml 中为明文；`GITHUB_TOKEN` 在变量表；网页能登录；隔离 home 里不再出现新的 `.env`。

## Done criteria

- [ ] `go build ./...`、全量测试 exit 0
- [ ] `grep -rn "godotenv" --include='*.go' pkg cmd | grep -v _test | grep -v "pkg/process/llm_models.go"` 无输出（迁移读取器是唯一保留点，它在 llm_models.go 里）
- [ ] `grep -rn "MergeDotEnvToProcess\|UpsertEnvVar\|ProviderAPIKeyLookup\|ProviderBaseURLLookup\|ProviderKeyEnvMap\|SecretConfigReferenceForOnboard\|RemoveChannelSecretsForProject\|DefaultEnvPath" --include='*.go' pkg cmd` 无输出
- [ ] `grep -rn "\.forebrain/\.env\|~/.forebrain/.env" pkg docs/deploy deploy` 无输出（迁移读取器与注释除外，需在报告列出例外）
- [ ] `grep -n "FOREBRAIN_LLM_API_KEY" -r deploy docs/deploy` 无输出
- [ ] 项目级 MCP/LSP 不能读进程环境的回归测试存在并通过
- [ ] 报告含真机冒烟结果或手动验收清单
- [ ] `plans/README.md` 本行已更新

## STOP conditions

- 改 yaml 令牌时找不到「只改一个键、不把运行段写回文件」的写法。
- `restorePersistedSecrets` 一带的逻辑无法分清哪些只服务 `${ENV}` 引用。
- MCP 包拿到变量表需要大范围改签名（超过 3 个包）——停下给出调用链，由 owner 决定注入点。
- 发现别的子系统（hooks 的 `allowed_env_vars`、批处理沙箱的环境变量放行）依赖 `.env` 被合并进进程环境——停下列出。
- 系统技能的脚本从进程环境读 LLM key（如 `pkg/skill/system_assets/caveman/caveman-compress/scripts/compress.go` 读 `OPENAI_API_KEY`/`ANTHROPIC_API_KEY`/`OPENAI_BASE_URL`）——`.env` 不再合并进进程环境后它们静默失源。停下列出受影响的技能脚本，由 owner 决定它们从哪取 key（变量表不是进程环境，不自动顶上）。

## Maintenance notes

- 迁移按「`.env` 文件存在」触发；改名后的 `.env.migrated-*` 由用户自行删除，代码永不再读。
- 变量表是 MCP/LSP `${名称}` 的唯一操作者来源；项目级条目**永远不能**回落到进程环境（安全约束，测试钉住）。
- 网关令牌：环境变量优先（容器），否则 yaml 明文；两者都没有时首启生成写 yaml。
