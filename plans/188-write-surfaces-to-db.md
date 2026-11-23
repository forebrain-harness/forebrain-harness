# 188 — 写面迁移：全部配置与凭据写入改走数据库，forebrain.yaml 不再被程序写入

- **基线**：`6f68cb21`（dev）＋ **前置：187 已落地**（fb_config / fb_mcp_oauth / ConfigStore / composeConfig / 凭据端口与 DB 安装 / 每 turn 同步已存在）。
- **状态**：TODO
- **依赖**：187。执行前先 `git log --oneline -5` 确认 187 的 commit 在树里；若 187 未落地，本计划 STOP。

## 0. 背景

187 之后配置的**读**已经从数据库来，但所有**写**面还在写 forebrain.yaml / `.env` / 凭据文件。本计划把每一个程序写配置与凭据的入口改为写数据库，forebrain.yaml 从此只被 187 的存量导入重写过一次，之后**任何代码路径不得再写它**。

生效语义按 owner 裁决：写库成功 ≠ 立即应用；下一个 turn 由 187 的 `WithConfigSync` 统一重读生效。**本计划不存在"写后应用"代码。**

owner 2026-10-30 三条追加裁决（187 §1.3 已固化）：provider api_key、ChatGPT/OpenCode 登录鉴权、MCP oauth/headers 凭据**明文入库、明文读写**。

## 1. 共享架构（同 187 §1，此处只列要点）

- 表 `fb_config(section, value_json, updated_at_ms)`（16 个 Root 段 + `auth.chatgpt`/`auth.opencode` 凭据段）＋ `fb_mcp_oauth(project_key, server_name, value_json, …)`。
- 写路径统一形状：`ConfigStore.UpdateSection(ctx, section, fn)`——`BEGIN IMMEDIATE` 事务内读-改-写。**写前校验只有 `validateLoadedRoot`（值域）；秘密扫描（`rejectPlaintextConfigSecrets`/`RejectPlaintextMCPSecrets`）不跑**——那是文件侧护栏，DB 里凭据合法明文（187 §1.3）。不过 → 事务回滚、4xx 给调用方。
- **明文读写**：`/api/providers`、MCP 写 API 的 GET 响应带**真实明文凭据值**（`api_key` 填真值；`api_key_set`/`api_key_hint` 下线）；写请求收明文值直接入段。

## 2. 现状事实：今天所有写配置/凭据的面（基线已核实，逐个迁移）

| # | 写面 | 位置 | 迁移动作 |
|---|---|---|---|
| W1 | `saveAndReload`（Save 文件 + `Env.ReloadConfig()`） | `pkg/gateway/api_extra.go:5282-5295` | 删函数；调用方改 `UpdateSection`。**不再 reload** |
| W2 | `withConfigWrite`（文件级读-改-写互斥） | `pkg/gateway/api_extra.go:5297-5301` | 改名/改义为 `withConfigSection(section, fn)`：事务互斥（Store 已提供），锁语义等价 |
| W3 | `/api/config` PUT（`handleConfigFile`） | `pkg/gateway/api_extra.go:366-367` | PUT 请求体里的 16 段逐段写 DB；gateway/credentials 段出现在请求体 → 400 带"只能手编 YAML+重启生效" |
| W4 | `/api/providers` GET/PUT（providers 合并、`providerSignature`/`replaces` 折叠逻辑） | `api_extra.go:5692-5800`、明文 key 分支 `api_extra.go:6127-6155` | 合并算法不动，落点改 `UpdateSection("agents", …)`；**`APIKeyPlain` 分支删**——明文 key 直接入段（不再经 `ProviderAPIKeyConfigReference` 写 `.env` 存 `${ENV}` 引用）；**GET 改回明文**（`api_key` 填真值，hint 字段下线，前端显示归 191） |
| W5 | `/api/hooks`、`/api/cron-settings` PUT | `api_extra.go:369-373` | 各写 `hooks` / `cron` 段 |
| W6 | agents API 3 处（主代理增删改） | `pkg/gateway/agents_api.go:592,681,722` | 读-改-写 `agents` 段 |
| W7 | subagents API 2 处 | `pkg/gateway/subagents_api.go:528,665` | 同上（子代理是 primary 定义内的 `SubagentConfig` 列表，`pkg/config/agents.go:54,62-82`） |
| W8 | MCP 写 API | `pkg/gateway/mcp_write_api.go:632` | 写 `agents.defaults.mcp_servers` 或某 primary 的 `mcp_servers`；**headers/oauth 凭据明文直存**（写路径的 `RejectPlaintextMCPSecrets` 调用删）；GET 同样回明文 |
| W9 | `/connect` 登录写 providers | `pkg/process/runtime.go:122` `SaveConfig(ctx)` 写 `appcfg.Save(ctx.ConfigPath, ctx.Config)` | `Context` 增加 store 句柄（或经 Environment 注入）；写 `agents` 段；登录得到的 provider 明文 key 直接入段 |
| W10 | TUI 记忆设置 | `pkg/tui/chat_session.go` `UpdateMemorySettings` → `appcfg.PatchMemory(path,…)`（唯一 Patch 助手，`pkg/config/memory_patch.go:18`）→ `reloadConfigFromDisk` | 改为 `UpdateSection("memories",…)`；`PatchMemory` 改造为对段的 patch（或新 `PatchMemorySection`），文件版删除；**去掉写后 reload** |
| W11 | `pkg/migrate` 导入配置 | `pkg/migrate/assets.go`（`appcfg.Save`） | 导入的运行时段（含明文凭据）写 fb_config；gateway 段仍可写 YAML（迁移工具是"用户的代理手编"——见 §4 例外） |
| W12 | **凭据 IO 切换**：openai/opencode/mcp 三包的 Load/Save 从文件默认实现切到 187 安装的 DB store | `pkg/llm/openai/auth.go`、`pkg/llm/opencode/auth.go`、`pkg/mcp/token_store.go`（`LoadOAuthOverlay`/`SaveOAuthOverlayMerge`） | 切换前做**一次性终同步**（187↔188 期间 token 刷新写了文件：按文件 mtime 比对，较新者回写 DB）；此后文件只读不写；`SetCredentialsPath` 调用与 `credentials.chatgpt` 段读取删除（YAML 里遗留段不主动清理，读取侧忽略） |
| W13 | 凭据文件的**直接读者**改 DB | web 登录流 `pkg/gateway/login_api.go`；模型发现 `pkg/process/models.go`（ChatGPT/OpenCode 列表）；`pkg/run/config.go:528`（OPENCODE_API_KEY fallback → `opencode.json`） | 全部改读 187 的凭据 store / `auth.*` 段 |
| W14 | `.env` 写入路径 | `ProviderAPIKeyConfigReference`/`apiKeyConfigReference`/`writeEnvReference`（`pkg/process/config_io.go:64-110`）+ setup 流的同路径调用 | **删**（明文 key 直接入段）；`.env` **读取**保留（用户自有 env 文件，compose 时 `${ENV}` 展开仍用它） |

读面：`handleConfigFile` GET 改为返回**合成 Root**（YAML gateway + DB 其余段；`credentials` 段不再出现），响应形状不变，Web 在 191 之前零改动可用。

## 3. 步骤

1. **Store 写助手**（若 187 未含）：`UpdateSection` 内置值域校验 + 测试（非法值回滚、**明文 api_key/MCP secret 被接受**）。
   验证：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state -count=1`。
2. **W1/W2**：删 `saveAndReload`、改 `withConfigWrite` → `withConfigSection`；本步先留编译桩（调用方逐个迁）。
   验证：`go build ./...`。
3. **W3–W8 逐端点迁移**（每端点一个 commit-sized 改动）：除 W4/W8 的凭据字段明文化外，请求/响应契约**不变**；写成功响应统一加 `"applies": "next_turn"` 字段（增量字段，前端旧版忽略无害）。
   验证：每个端点对应的 `pkg/gateway` 测试改跑 DB 断言（写后 `select value_json from fb_config where section=…`，再断言文件未变——mtime/内容双查；W4/W8 加"明文凭据值在段 JSON 里"断言）；`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -count=1`。
4. **W12 凭据 IO 切换**：终同步（mtime 较新者回写 DB）→ 三包 Load/Save 切 store → 删 `SetCredentialsPath` 装配。单测：刷新流写 DB 不写文件（token_store/openai Save 的既有文件断言测试改为 store 断言）；DB 行缺失＋文件存在＝终同步语义。
   验证：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/... ./pkg/mcp ./pkg/process -count=1`。
5. **W13 直接读者**：login_api / models.go / run config fallback 改 DB 读。
   验证：`./pkg/gateway ./pkg/process ./pkg/run` 相关包测试。
6. **W9 `/connect`**：流程手测依赖真登录，单测覆盖"写段+文件不动"；真机验收归 §6。
7. **W10 TUI**：`UpdateMemorySettings` 改段写；TUI 相关测试改为断言段写。
8. **W11 migrate**：迁移测试断言"运行时段（含明文凭据）进 DB、gateway 段可进 YAML"。
9. **W14 `.env` 写路径删除**：grep `ProviderAPIKeyConfigReference|writeEnvReference` 归零（读取侧 `applyDotEnvAfterNormalize` 保留）。
10. **全量**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./... -count=1`；`go vet ./...`。
11. **防回归哨兵**（`pkg/architecture`）：① `pkg/gateway`、`pkg/tui`、`pkg/process/runtime.go` 不得出现 `appcfg.Save(`/`config.Save(`（W11 migrate 白名单除外）；② 运行码不得调用 `ProviderAPIKeyConfigReference`；③ `pkg/llm/openai`、`pkg/llm/opencode`、`pkg/mcp` 的生产路径不得绕过 store 直接 `os.WriteFile` 凭据文件（包内测试夹具除外）。

## 4. 边界

- **不动**：providers 合并/折叠算法；MCP 会话冻结语义；187 的同步机制；`.env` 读取（compose 展开）；项目级 MCP 文件的秘密扫描（repo 文件护栏照旧）。
- **不做**：前端改动（191；含 api_key 字段显示真值的 UI 调整）、TUI 新面板（190）、watcher 删除（189）。
- **明确例外**：`pkg/migrate` 写 gateway 进 YAML 允许；187 的存量导入重写是唯一另一次 YAML 写；`auth.json`/`opencode.json`/`state/mcp-oauth/` 遗留文件不删（用户数据，读取侧已忽略）。

## 5. 完成判据

1. 全量测试绿、`go vet` 无输出。
2. 哨兵测试三条存在且红绿可验证。
3. scratch home 手测：`forebrain gateway` 起来后——① Web Providers 页填一个明文 api_key 保存 → `fb_config.agents` 的 value_json 里是明文 key、`.env` 未变、GET 回显明文值；② MCP 服务加一条带 `x-api-key` header 的配置 → 段 JSON 明文；③ ChatGPT/OpenCode 登录（fake 流程）→ `auth.*` 段有值、`auth.json`/`opencode.json` 未被写；④ forebrain.yaml mtime 全程不变；⑤ 下一个对话 turn 日志出现 config 快照应用。
4. `/connect` 真机：登录成功 → providers 段进 DB；YAML 未被触碰。

## 6. 真机验收（owner 参与项）

- 用 owner 真实 home 的**只读副本**先跑一遍 /connect、Providers 修改与 MCP 凭据写入（凭据从用户配置取，统一 fake 模型 `deepseek/deepseek-v4-flash` 验收线）；副本验证通过才在真 home 执行。

## 7. 维护注意

- 以后新增任何"改配置/存凭据"的 API/slash，一律 `UpdateSection` / 凭据 store；想写 YAML 或凭据文件必须先改哨兵白名单——这个摩擦是故意的。
- `applies: "next_turn"` 字段是给 191 的 UI 提示用的，别删。
- 状态库承载 live 凭据（187 §8）：排障输出/测试快照先脱敏。

## 8. 逃生口

- 发现某端点的配置面跨两段以上原子修改（如 agents+hooks 联动）→ 单段事务不够，停，报告需要 `ReplaceAll` 级事务的端点清单。
- 发现 W3 PUT 请求体被前端以"整文件上传"方式使用（丢弃部分段会丢配置）→ 停，报告该使用面，等 191 一起改。
- 发现凭据文件还有本表未列的读者/写者（grep `auth.json`/`opencode.json`/`mcp-oauth` 出现新调用点）→ 停，补全清单再继续。

**第四轮（owner 2026-10-30）**：预设 id 统一 `read_only`/`workspace_write`/`full_access`；权限档案整块去掉；信任度只收紧；继续精简；「配置文件」编辑器保留并改写磁盘文件；状态库迁移规则保留——见 [191 §8.6](191-web-config-ui.md)。本计划受影响：W3（`/api/config` 改回读写磁盘 forebrain.yaml，提示重启生效）、`approval-default` 写 `approval_preset`、预设 id 改名。

**补充（owner 2026-10-30）**：187 的首次导入 + 重写 YAML 保留（重写时只留「文件支持项」白名单）；`/api/config` PUT 只在 YAML 语法错误时拒绝，不支持项照存、读取时静默忽略——见 [191 §8.7](191-web-config-ui.md)。

**第六轮（owner 2026-10-30）**：凭据**不再明文回显**——模型服务 API Key、联网搜索 / 网页抓取 Key、MCP Bearer token 界面只露后四位；建议 GET 只回「已设置 + 后四位」、PUT 空值保持原值。本计划 W4/W8 的「GET 回明文」须按 [191 §8.8](191-web-config-ui.md) 修订。
