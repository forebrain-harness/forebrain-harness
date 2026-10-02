# 计划 014：模型服务——API Key 密文输入与 .env 引用存储、多模型标签输入、模型目录建议

> **执行者须知**：逐步执行，每一步都运行"验证"命令并确认结果后再进入下一步。出现"STOP 条件"中的任何情况，立即停止并报告。
> 完成后更新 README 中本计划的状态行。**不要提交代码。**
>
> **漂移检查（先运行）**：
> `git diff --stat b92a1b1..HEAD -- pkg/gateway/api_extra.go pkg/process/config_io.go pkg/config/secrets.go frontend/src/views/ProvidersView.vue`
> 计划 007 改过 `api_extra.go` 路由注册行。那是预期的。

## 状态

- **优先级**：P1
- **工作量**：M
- **风险**：MED（密钥处理路径：掩码回显、明文转引用、保留旧值，三件事的往返语义要测试钉死）
- **依赖**：计划 007（`/providers` 菜单位、设置页结构）
- **类别**：feature
- **基线**：提交 `b92a1b1`，2026-09-29

## 为什么要做

owner 要求（第 32 条）："模型服务里的api key要支持用户明文输入，输入一个字符马上隐藏，采用输入密码的效果，模型输入框必须支持按英文逗号分隔输入多个模型id"。预览 captions 的 `providers` 条（402 行）是合同：按顺序尝试的模型服务，第一条主用其余回退；模型框逗号生成标签；API Key 输入即圆点，已保存只显示后四位，点"更换"重新输入。

## 现状

- **providers API**（`pkg/gateway/api_extra.go:2946` `handleProviders`）：GET 返回当前主代理的 `[]AgentLLMProviderConfig` 并 `config.RedactSecrets`（`pkg/config/secrets.go:157`：secret 样字段非空 → `[REDACTED]` 占位，`:150` `RedactedSecretPlaceholder`；空仍为空——"未设置"与"已设置但隐藏"是两个事实）；PUT 接收整表替换，`ClearRedactedSecrets :167` 把仍是占位符的值清空（= 该条不改），随后 **`api_key` 明文直接写进 yaml**——没有转 `${ENV}` 引用。第一条 = 主用、其余回退的语义就是数组顺序。
- **env 引用设施已存在**：`pkg/process/config_io.go:26` `apiKeyConfigReference(home, provider, apiKey)`——非引用明文 → `ProviderKeyEnvMap`（`pkg/process/provider.go:13`，env 名 = `UPPER(provider)_API_KEY`）→ `writeEnvReference :35`（合并写 `~/.forebrain/.env`、chmod 0600、返回 `${ENV_NAME}`）。现有调用方只有 TUI setup 流程（`pkg/process/setup.go:87`）。
- **多模型**：`pkg/config/agent_llm.go:253` 磁盘线格式 `agentLLMProviderWire.Model` 是 `StringList`（标量或列表均可）；`AgentLLMProviderConfig`（定义在 `pkg/config/agents.go:25`）的 `Models` 字段是 `json:"-"`（`agents.go:32`）——**HTTP API 不输出也不接受列表**；`expandLLMProviderConfigs :63` 在读取侧把多模型展开为每模型一条。
- **模型目录**：`GET /api/models`（路由 `api_extra.go:49`，`handleModelsList :301`，query `q`/`provider`/`limit`）返回模型目录记录——"点下方该服务商的模型快速加入"的数据源。
- **前端**：`frontend/src/views/ProvidersView.vue`（105 行）是普通表单：api_key 明文 `<input>`（不隐藏、不转引用）、model 单值输入。
- README 全局规则（90-115 行）全部适用。

## 设计

### 1. API：providers 的 DTO 化与密钥引用化（改 `handleProviders`）

- **GET 响应改用 DTO**（不动 yaml 结构）：每条 `{provider, base_url, api_path, params, model: string, models: []string, api_key_set: bool, api_key_hint: string}`——`models` = 磁盘 `Model` 标量 + `Models` 列表合并后的顺序列表（空列表时回退 `[model]`）；`api_key_set` = 原 api_key 非空；`api_key_hint` = 末 4 位（长度 ≤ 4 时返回 `"••••"` 全遮）。**删除 `[REDACTED]` 回显**：DTO 化后前端不再需要区分占位符与真值。
- **PUT 请求改收 DTO**：`{providers: [{provider, base_url, api_path, params, model, models, api_key?: string, api_key_plain?: string}]}`：
  - `api_key_plain` 非空 → `apiKeyConfigReference(home, provider, api_key_plain)` 转 `${ENV}` 引用后落盘（env 名注意：现方案是 `UPPER(provider)_API_KEY`，同一 provider 的同一把 key 在主代理之间共享 `.env` 条目——与 TUI 现状一致，属既有事实，不在本计划改变）；
  - `api_key_plain` 为空且 `api_key` 非空 → 视为"用户直接给了引用串或显式值"，原样落盘（保持能粘贴 `${ENV}` 的高级用法）；
  - 两者都空 → **保留该条旧 api_key 不变**（取代 `ClearRedactedSecrets` 语义：DTO 里没有旧值可清，"空 = 不改"由 handler 从 persistedConfig 旧表回填实现）。
  - 顺序即优先级（第一条主用），整表替换语义不变；写后 `saveAndReload`。
- `config.RedactSecrets/ClearRedactedSecrets` 对本端点的使用随之移除；这两个函数的其他调用方（channels 等）不动。
- Go 测试必须覆盖往返：设置 → 读回（`api_key_set`+hint 正确、无明文无占位符）→ 不带 key 的 PUT 保留原 key → 带 `api_key_plain` 的 PUT 换新且 yaml 里是 `${...}`、`.env` 里是明文且权限 0600。

### 2. 前端表单（`ProvidersView.vue` 重写）

- **服务列表**：可排序卡片/行（上移/下移按钮 = 数组顺序），标注"主用"/"回退"；每条：provider 下拉（从 `GET /api/models` 目录聚合的 provider 集 + 自由输入）、base_url、api_path、params（高级折叠）。
- **API Key**：`type="password"` 输入框 + `autocomplete="new-password"`，每输入一字符立即显示圆点（浏览器原生行为，不加额外延迟逻辑）；已保存状态显示 `已保存（••••<hint>）` + "更换"按钮——点击才显示密码框（`keyEdit` 状态，预览 371 行 `keyEdit: false` 同款交互）；未保存状态显示空密码框。**永不明文回显**。
- **模型多选输入**：单行输入 + chips：输入英文逗号（或失焦）把已输文本生成一个 chip；chips 可删除；下方"常用模型"按钮组按当前 provider 从 `GET /api/models?provider=<p>&limit=20` 拉取，点击加入 chips；提交 = `models` 数组（单模型时也提交数组，DTO 统一）。
- 保存按钮 PUT 整表；密钥未动时 `api_key` 字段不上送。

## 需要的命令

| 用途 | 命令 | 成功标志 |
| --- | --- | --- |
| Go 静态检查 | `go vet ./...` | 退出码 0 |
| Go 测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/... ./pkg/config/... -count=1` | 全部 `ok` |
| 前端单测 | `cd frontend && corepack pnpm test` | 全部通过 |
| 真机验证 | `scripts/acceptance/web_e2e.sh` | 最后一行 `web e2e: PASS` |
| 缓存不变式 | `git diff --stat -- pkg/agent pkg/assembly pkg/llm pkg/turn/prompt* internal/agentrun` | 无输出 |

## 范围

**只允许修改**：
- `pkg/gateway/api_extra.go`（`handleProviders` 重写为 DTO 往返；其余 handler 不动）、`pkg/gateway/providers_dto_test.go`（新建）
- `frontend/src/views/ProvidersView.vue`（重写）、`frontend/src/components/providers/ModelChipsInput.vue`（新建）及其测试、`frontend/src/locales/index.ts`、`frontend/src/lib/api.ts`（DTO 类型）
- `frontend/e2e/providers-keys.spec.ts`（新建）

**不要碰**：
- `pkg/process/config_io.go`、`pkg/process/provider.go`（env 引用设施只读复用——若 `apiKeyConfigReference` 因包边界不可从 gateway 调用：**STOP 上报**，由 owner 拍板是导出函数还是在 gateway 加薄封装，不允许复制实现）。
- 通道密钥路径（`ChannelSecretEnvName`）；`config/secrets.go` 本体。
- `pkg/gateway/dist/**`；`pkg/tui/**`。

## 步骤

### 第 1 步：DTO 与密钥引用化

实现设计第 1 条（含 STOP 条件里的包边界确认）。测试覆盖往返全路径与 0600 权限断言。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/... ./pkg/config/... -count=1` 全部 `ok`；`go vet ./...` 退出码 0。

### 第 2 步：前端表单

实现设计第 2 条；`ModelChipsInput` 单测覆盖逗号生成 chip、失焦生成、删除、去重。

**验证**：`cd frontend && corepack pnpm test` 全部通过。

### 第 3 步：真机验证

新建 `frontend/e2e/providers-keys.spec.ts`：
1. `/providers` 新建服务：provider=deepseek、models 输入 `deepseek-v4, deepseek-v4-flash` 生成 2 个 chip、api_key 输入（密码框圆点）；保存。
2. 断言 yaml（经设置 → 配置文件标签）里 `api_key` 是 `${...}` 且不含明文；`.env`（e2e 脚手架的 FOREBRAIN_HOME 下）含对应条目且权限 0600。
3. 刷新页面：key 显示 `已保存（••••` + 末 4 位 + `）`，页面源码与网络响应中**无明文 key、无 `[REDACTED]`**。
4. 不动 key 修改模型列表保存 → `.env` 的 key 条目不变（读文件字节对比）。
5. 点"更换"输入新 key → `.env` 更新。
6. 上移/下移改变主用顺序，保存后顺序保持；截图 `providers-form.png`。

**验证**：`scripts/acceptance/web_e2e.sh` → 最后一行 `web e2e: PASS`。

## 完成标准（全部满足）

- [x] 第 1-2 步的验证全部通过
- [x] `scripts/acceptance/web_e2e.sh` 最后一行 `web e2e: PASS`（69/69，2.8 分钟）
- [x] GET 响应、前端渲染、页面源码三处都无 API key 明文（e2e 断言）
- [x] 多模型往返：web 输入 → yaml → 引擎展开（`expandLLMProviderConfigs` 路径）全链路成立（Go 测试覆盖 yaml 形状）
- [x] `git status --short pkg/gateway/dist` 无输出；缓存不变式检查无输出
- [x] README 状态行已更新

## STOP 条件

- "现状"摘录与代码对不上（尤其 `handleProviders` 现行为、`StringList` 线格式、`apiKeyConfigReference` 签名）。
- `pkg/process` 的密钥函数无法从 gateway 复用且 owner 未拍板封装方式。
- 发现通道（channels）等其他密钥面也存在明文入 yaml 的问题——记录并报告，通道密钥不在本计划范围（另行立项），不得顺手改。
- 需要修改"范围"之外的文件。

## 维护说明

- 密钥往返语义：**GET 永不给值，PUT 空 = 不改**。任何新的设置面（channels、hooks 等）引入密钥时应复用同一 DTO 模式，而不是 `RedactSecrets` 占位符往返。
- `.env` 条目命名沿用 `ProviderKeyEnvMap`（`UPPER(provider)_API_KEY`，无 agent 前缀）：同一 provider 多主代理共享一把 key 是既有事实；将来要按主代理隔离时改 `ProviderKeyEnvMap` 一处 + 迁移脚本，另立计划。
- 模型 chips 的目录数据源是 `GET /api/models`（实时目录）；目录为空时按钮组隐藏，不阻塞手输。
