# Plan 265: 网关接口——模型配置增删改查与查重、内置 provider、测试连接、统一订阅、变量

> **Executor instructions**: 按步骤顺序执行，每步的验证命令通过后再做下一步。遇到
> 「STOP conditions」里的情况就停下来报告，不要自己发挥。**不要 commit / push / merge**：
> 改动留在工作区里，做完报告（仓库主人手动提交）。做完后把 `plans/README.md` 里本计划那一行
> 的状态改成 DONE，并附一句执行摘要——除非派你来的评审说索引由他维护。
>
> **Drift check (run first)**:
> `git diff --stat 0a698bf2..HEAD -- pkg/gateway/login_api.go pkg/gateway/subagents_api.go pkg/gateway/list_page.go`
> → `login_api.go`、`list_page.go` 必须为空；`subagents_api.go` 只允许有 plan 260 的改动。对不上即 STOP。

## Status

- **Priority**: P1
- **Effort**: L
- **Risk**: MED（新接口为主；旧的登录路由改为走注册表，网页登录行为要保持）
- **Depends on**: plans/259-…（store）、260、261、262（`UpsertLLMModel`、`SetAgentDefaultModel`）、263（注册表、`ProbeLLMModel`）、264（订阅注册表）
- **Category**: direction
- **Planned at**: commit `0a698bf2`, 2026-11-07
- **Revised**: 2026-11-08（plan 264 只落注册表 + ChatGPT / OpenCode Go；火山方舟由 plan 281 加，网关零改动。`provision` 的 409 `key_choice` / `needs_key_consent` 今天没有内置订阅会触发——它们是带 Key 订阅的契约，由测试里的假订阅覆盖，plan 281 的实现直接复用，**不要删**。）

## Why this matters

网页（plan 266）需要一组新接口来实现 owner 2026-11-07 的要求：

- 模型配置**全局一张表**，按行 id 增删改；**同 (provider, model, base_url, api_path) 只能一条，保存前查重**（库里唯一索引兜底，plan 259）；
- 「添加模型」弹窗选内置 provider（含美团、腾讯、火山方舟、BytePlus、智谱 Coding Plan 专用端点）或「自定义」（不填 provider，模型名自由文本，**接口协议必选**）；
- 「测试连接」；
- 「**订阅登录入口统一**，方便以后扩展其他订阅」：ChatGPT、OpenCode Go 走同一组接口（plan 264 的注册表；火山方舟由 plan 281 加，网关零改动）；**不做登录的（例如智谱 Coding Plan）不进订阅入口**，它们只是内置端点，在「添加模型」里手填 Key；
- 模型下拉框与选择器要显示 provider / model / base url / api path（选项数据要带这四项）；
- `.env` 删除后，MCP/LSP 的 `${VAR}` 改从库里「变量」表取（plan 268 接运行时），网页需要增删改它。

## Current state

- 路由集中在 `pkg/gateway/api_extra.go` 的 `AttachExtraRoutes`（`:42` 起）：`api := routes.Group("/api")`，写法 `api.Get("/providers", s.handleProviders)`（`:412-413`）；
  登录路由 `:58-67`（`auth := api.Group("/auth")`，`/login/:provider`、`/login/:provider/:loginId`、`…/cancel`、`…/callback`）。路径参数用 `pathParam(r, "name")`。
- 分页：`readListQuery(r)`、`pageOf(items, q)`、`writeListPage(w, key, page, total, servedPage, q, extra)`（`pkg/gateway/list_page.go:54-121`）；所有列表必须服务端分页带总数（owner 规则）。范例：`handleProviders` 的 GET（`api_extra.go:6577-6608`）。
- 配置读写：`s.persistedConfig(ctx)`（`:5739`，组合后的持久层，含 plan 260 的 `LLMModels`）、`s.withConfigSection(ctx, section, fn)`（`:5755`）、`configBadRequest(...)`（`:5772`）；
  当前主代理 `s.activePrimarySummary()`（`pkg/gateway/agents_api.go:38`）；JSON 输出 `writeAgentsJSON`（`agents_api.go:408`）。状态库 `s.Env.SQL`。
- 登录：`pkg/gateway/login_api.go` 按 `authLoginProvider(raw)` 只认 `opencode`/`chatgpt`，`runAuthLogin` 里 `switch`；状态体 `authLoginStatusBody{Status, URL, UserCode, Error, BaseURL}`（`:367-395`）。
  网页登录完成后用 `GET /api/models?provider=…` 的目录拉可选模型，再经 `PUT /api/providers` 保存（plan 262 起 PUT 返回 410）。
- 子代理配置接口 `pkg/gateway/subagents_api.go`：子代理 `model` 字段今天是 "provider/model"。
- 设置接口 `handleSettings`（`api_extra.go` 里，`/api/settings`）：`memories.extract_model`、`memories.consolidation_model` 是 `settingsText`（`:9625-9626`），值 `"current"` 或 "provider/model"。
- key 的对外形态：**owner 2026-11-07 推翻了 plan 191 起的「永不回传明文、只给末四位」**——模型配置接口要回传 **明文 `api_key`**，因为「编辑模型」抽屉里要能点眼睛显示已存的 Key（原型 v4 的交互）。
  旧的 `api_key_set` / `api_key_last4` 保留（表格与登录类行还要它们），但不再是界面拿到 Key 的唯一途径。请求里的语义不变：「字段缺省 = 不动，空串 = 清除」（`providerPutRow.APIKey *string` 的注释，`api_extra.go:6279-6286`）。
  安全边界照旧且必须做到：接口在网关的控制面鉴权之后（与 `/api/*` 同一条链）；明文**只出现在这个接口的响应体**，不进日志、不进错误文本、不进事件；`api_key` 在 `choices` 端点与事件里**不出现**（choices 只给 id 与展示用的名称/端点）。

## Commands you will need

| Purpose | Command | Expected |
|---|---|---|
| 构建 | `go build ./...` | exit 0 |
| 网关测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/ -count=1` | ok |
| 全量 | `CGO_ENABLED=1 go test -tags fts5 ./... -count=1` | ok |
| 引擎矩阵 | `./scripts/test-db.sh check` | exit 0 |

## Scope

**In scope**:
- `pkg/gateway/llm_models_api.go`（新建）、`pkg/gateway/subscriptions_api.go`（新建）、`pkg/gateway/env_vars_api.go`（新建）及各自 `_test.go`
- `pkg/gateway/api_extra.go`：只加路由行；`handleSettings` 对两个记忆模型字段的校验
- `pkg/gateway/login_api.go`：改为调用 `pkg/subscription` 注册表（见 Step 5）
- `pkg/gateway/subagents_api.go`：子代理 `model` 收发 id
- `scripts/test-db.sh`：`./pkg/gateway` 行追加 `LLMModelsAPI|SubscriptionsAPI|EnvVarsAPI`

**Out of scope**:
- 删除旧 `/api/providers` 处理器与旧页面（plan 266 换页面时一起删）。
- 网页、TUI。
- 运行时读变量表（plan 268）。

## Steps

### Step 1: 模型配置接口（`pkg/gateway/llm_models_api.go`）

路由（加在 `AttachExtraRoutes` 里 `/providers` 两行旁边）：

```
GET    /api/llm-models                 列表（分页，q 搜 provider/model/base_url/api_path）
GET    /api/llm-models/choices         下拉框用的全量精简列表（不分页，见下）
POST   /api/llm-models                 新建（严格查重）
PUT    /api/llm-models/:id             修改（严格查重）
DELETE /api/llm-models/:id             删除
POST   /api/llm-models/:id/refresh-key 订阅行重新取 Key
PUT    /api/llm-models/:id/default     设为当前主代理的默认模型
POST   /api/llm-models/test            测试连接
GET    /api/llm-models/catalog-caps    ?provider=&model=&base_url=&id= （POST 变体带 api_key）→ 模型的默认能力（llmModelCaps 形状 + `efforts_known` + `efforts_source`）。按模型来源分发（plan 263 Step 3b）：ChatGPT/火山订阅行走各自接口，其余走 models.dev 实时；查不到 `efforts_known=false`；表单填好 base_url、key、模型名后取一次，改动其中任一项再取
GET    /api/llm-providers              内置 provider 注册表 + 自定义 + 协议列表
```

行 DTO（snake_case，前端用 camelcase-keys 转换）：

```go
type llmModelRow struct {
	ID           string `json:"id"`
	Provider     string `json:"provider"`
	ProviderName string `json:"provider_name"`    // 注册表 Label/LabelZH 按请求的 Accept-Language 选；custom 为 "自定义"/"Custom"
	Custom       bool   `json:"custom"`
	Model        string `json:"model"`
	BaseURL      string `json:"base_url"`
	APIPath      string `json:"api_path"`
	URLVerbatim  bool   `json:"url_verbatim"`     // 「自定义协议」：原样请求 base_url
	Protocol     string `json:"protocol"`         // "chat_completions" | "responses" | "anthropic_messages" | "path"（其它路径）| "login"
	Endpoint     string `json:"endpoint"`         // turn.ModelEndpointLabel(base, path)
	APIKeySet    bool   `json:"api_key_set"`
	APIKeyLast4  string `json:"api_key_last4,omitempty"` // 表格/登录标记用；明文见下一行
	// APIKey is the stored key in plaintext (owner 2026-11-07: the edit drawer
	// must be able to reveal it). Empty for a row with no key. Never logged,
	// never in errors, never in /choices or events.
	APIKey       string `json:"api_key,omitempty"`
	LoginBacked  bool   `json:"login_backed"`
	Source       string `json:"source,omitempty"`       // 订阅 id（行由订阅登录写入时），'' = 手动添加
	SourcePlan   string `json:"source_plan,omitempty"`
	SourceName   string `json:"source_name,omitempty"`  // 「火山方舟 · Coding Plan 个人版」，按语言
	SourceState  string `json:"source_state,omitempty"` // usable | missing | dead（订阅的 Probe；仅订阅行）
	Params       string `json:"params,omitempty"` // JSON 文本（原样，不让前端转键名）
	Caps         llmModelCaps `json:"caps"`
	EffortsKnown bool        `json:"efforts_known"`   // 档位来源（ChatGPT/火山订阅接口或 models.dev）查到了：界面只读显示 catalog_caps.efforts，不给勾选；查不到=false → 界面显示「支持的思考强度」复选
	CatalogCaps  llmModelCaps `json:"catalog_caps"`  // 不含本行覆盖的目录值（images/reasoning/reasoning_off/efforts/context_window/max_output），给网页开关做初始值与比较
	IsDefault    bool   `json:"is_default"`       // 当前主代理的默认
	DefaultFor   []string `json:"default_for,omitempty"` // 以它为默认的主代理显示名
	CreatedAt    int64  `json:"created_at"`
}
type llmModelCaps struct {
	Images, PDF, Video, Audio, Reasoning, ReasoningOff *bool `json:",omitempty"` // 字段名分别 images/pdf/video/audio/reasoning/reasoning_off
	Efforts       []string `json:"efforts,omitempty"`
	DefaultEffort string   `json:"default_effort,omitempty"`
	ContextWindow *int64   `json:"context_window,omitempty"`
	MaxOutput     *int64   `json:"max_output,omitempty"`
}
```

列表：数据来自 `config.ConfiguredLLMs(&cfg)` 的顺序 + 表里的行（视图里只会是表行，plan 262 已迁移）；`IsDefault` 用 `config.EffectiveDefaultLLMFor(&cfg, active.ID)`；`DefaultFor` 遍历所有主代理定义的 `DefaultModel` 是否等于该 id。
`extra` 里带 `default_model_id`。

`/choices`：`[]{id, provider, provider_name, model, base_url, api_path, endpoint, label("provider/model"), efforts, supports_images}`，按列表顺序，**不含 key 任何信息**。这是子代理配置、记忆设置、新会话等所有下拉框的唯一数据源。

新建/修改的请求体：

```go
type llmModelWrite struct {
	Provider string  `json:"provider"`  // 内置 id 或 "custom"
	Model    string  `json:"model"`
	BaseURL  string  `json:"base_url"`
	APIPath  string  `json:"api_path"`
	APIKey   *string `json:"api_key"`   // 缺省=不动（PUT）；空串=清除
	Params   *string `json:"params"`
	Caps     *llmModelCaps `json:"caps"`
	URLVerbatim *bool  `json:"url_verbatim"` // 缺省 = 不动（PUT）/ false（POST）
}
```

校验（按顺序，错误一律 400 + 一句中文/英文按 Accept-Language 的短句；网页直接显示）：
1. `provider` 必须是注册表 id 或 `custom`（`config.BuiltinProviderByID`）；登录类 provider（`LoginBacked`）**不许**经此接口新建（它们由订阅登录写入）→ 400「请通过订阅登录添加」。
2. `model` 非空。`base_url` 非空且是 `http(s)://` URL。
3a. `url_verbatim = true`（「自定义协议」）：**只允许 `provider == custom`**（内置服务商与订阅行 → 400「只有自定义模型可以用自定义协议」）；`api_path` 必须是 `/chat/completions`（请求体按 Chat Completions）→ 否则 400；`base_url` 即完整请求地址，不拼接任何路径。
3. `custom`：`api_path` 必须是三个协议常量之一（`config.APIPathChatCompletions/Responses/AnthropicMessages`）→ 否则 400「请选择接口协议」。内置：`api_path` 为空时由规范化补全；非空时原样（允许其它路径，与今天一致）。
4. 新建时非登录类必须有 key（`api_key` 非空）。
5. `params` 非空时必须是 JSON 对象（`config.ParseLLMRequestParamsJSON`）。
6. `caps.efforts`：该模型的档位能由 ChatGPT/火山订阅接口或 models.dev（plan 263 Step 3b）查到时**不允许提交**（400「这个模型的思考强度由官方文档决定」）；无条目时每项 ∈ `minimal/low/medium/high/xhigh/max`；`default_effort` 为空或 ∈ efforts（若 efforts 为空则 ∈ 目录档位）；`context_window`/`max_output` 为正整数。
7. **查重（主路径）**：`config.NormalizeLLMModelIdentity` 后 `state.LLMModelStore.FindByIdentity`；命中且（新建，或修改时 id 不同）→ **409**，
   body `{"error": "相同的服务商、模型、接口地址与路径已经配置过", "existing_id": "<id>"}`（英文 `"this provider, model, base URL and path are already configured"`）。
   写库时 store 返回 `state.ErrLLMModelExists`（唯一索引兜底的并发情况）→ 同样 409。

`catalog_caps` 与 `/catalog-caps` 都必须读**不含覆盖层**的目录（plan 263 的 `llm.Lookup` 会合并覆盖层；在 `pkg/llm` 加一个 `LookupCatalog(provider, model)` 只查目录与内置模型表，供这里用）。

**订阅行的修改限制**：`source` 非空的行，PUT 里出现 `provider`/`model`/`base_url`/`api_path`/`api_key`/`url_verbatim` 任一字段且与库中不同 → 400「订阅来的模型只能改高级配置；要换模型或协议请在订阅登录里重新选择」；只接受 `caps`、`params`。
`POST /api/llm-models/:id/refresh-key` → 该行订阅的 `RefreshKey`；非订阅行 404；`ErrKeyChoice`/`ErrNoKey` 与 provision 同样返回 409。

删除：该行是任一主代理的 `default_model`（显式引用）→ 409 `{"error":"它是「<代理名>」的默认模型，请先把默认改成别的", "default_for": [...]}`；否则删除。会话、子代理、记忆里对它的引用不清理（运行时悬空回落，plan 260/261）。

设为默认：`process.SetAgentDefaultModel(ctx, s.Env.SQL, active.ID, id)`；id 不存在 → 404。

测试连接 `POST /api/llm-models/test`：请求体同 `llmModelWrite` 再加 `id`（可选）。有 `id` 且 `api_key` 缺省 → 用库里那行的 key（**只在进程内**）；调用 `process.ProbeLLMModel`；返回 `200 {"ok":true}` 或 `200 {"ok":false,"error":"<短句>"}`（探测失败不是 HTTP 错误）。

`GET /api/llm-providers`：`{"providers":[{id, name, login_backed, endpoints:[{name, base_url, api_path}]}], "protocols":[{api_path:"/chat/completions", name:"OpenAI Chat Completions"}, {"/responses","OpenAI Responses"}, {"/v1/messages","Anthropic Messages"}], "custom_id":"custom"}`。顺序即注册表顺序；`name` 按语言选。

**Verify**: `go build ./... && CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/ -run 'LLMModelsAPI' -count=1` → ok（测试见 Test plan 1–8）。

### Step 2: 记忆模型与子代理模型收发 id

- `handleSettings`：`memories.extract_model`/`consolidation_model` 写入时值必须是 `"current"` 或表里存在的 id（旧 label 也接受但转换成 id 再存：`config.ResolveModelRef(&cfg, "main", v)`）；否则 400。读出时原样（id 或 current）。
- `subagents_api.go`：子代理 DTO 的 `model` 写入接受 id（或旧 label → 转 id）；读出加 `model_id`、`model_label`（"provider/model"）、`model_endpoint`（`turn.ModelEndpointLabel`）。显示用的继承默认（`:262-285` 那个函数）同样带出 id 与端点。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/ -run 'Settings|Subagent' -count=1` → ok。

### Step 3: 变量接口（`pkg/gateway/env_vars_api.go`）

```
GET    /api/env-vars          分页；行 {name, value_set:true, value_last4, updated_at}；q 搜 name
PUT    /api/env-vars/:name    {value}；名字非法 400（state.ErrEnvVarName）；value 不许为空（要删就 DELETE）
DELETE /api/env-vars/:name
```

值永不回传明文（与 key 同规则）。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/ -run 'EnvVarsAPI' -count=1` → ok。

### Step 4: 统一订阅接口（`pkg/gateway/subscriptions_api.go`）

```
GET  /api/subscriptions                       [{id, name, flows, state: usable|missing|unreachable|dead}]
GET  /api/subscriptions/:id/offers            登录完成后可选的模型 [{plan, plan_name, provider, model, base_url, api_path, endpoint, context_window, supports_images, default, from_static_list}]
POST /api/subscriptions/:id/provision         {offers:[{provider, model, base_url, api_path}], key_id?, create_key?}
                                              → 200 {ids:[...]}；409 {key_choice:[{id,name,tail}]}；409 {needs_key_consent:true}；400 短句
```

- `state` 由 `sub.Probe` 得出（超时用登录接口已有的 `modelsDiscoveryTimeout`）。
- `offers` 调 `sub.Offers`；`provision` 把请求里的 offer 与 `sub.Offers` 的结果按四元组匹配（**只允许 provision 订阅自己列出的 offer**，防止借订阅接口写任意行），再 `sub.Provision`。
  `subscription.ErrKeyChoice` → 409 `key_choice`；`subscription.ErrNoKey` → 409 `needs_key_consent`。
- 成功后若当前主代理没有可解析的默认模型，把第一个 id 设为默认（与 plan 262 setup 的规则一致）。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/ -run 'SubscriptionsAPI' -count=1` → ok。

### Step 5: 登录路由改走注册表（`pkg/gateway/login_api.go`）

路由路径不变（`/api/auth/login/:provider…`），`:provider` 现在是订阅 id：
- `authLoginProvider(raw)` → `subscription.ByID(raw)`，不存在 404；
- 选流程：按 `sub.Flows()` 顺序取第一个网关能驱动的——网关**能**驱动 `paste_callback`（ChatGPT：今天在网关起 localhost 回调 + 允许粘贴，保留这一组合）与 `device_code`（OpenCode；火山方舟在 plan 281 落地后走同一支，网关不改）；**不**驱动 `browser_callback`（浏览器自动打开是 TUI 的事；网关页只展示 URL + user code 并轮询）；`paste_code` 已删除，没有订阅使用；
- `runAuthLogin` 的 `switch` 删除，改为 `login, err := sub.Start(ctx, env, flow)` → 状态里存 `login.AuthorizeURL()`、`login.UserCode()`，后台 goroutine `login.Wait(ctx)`；
- `…/callback` → `login.Submit(ctx, body.pasted)`（**只有 ChatGPT 粘回调地址走这里**）；请求体字段兼容旧名（今天是 `url`）；火山没有 `Submit` 这一步，设备码完成由轮询推进，`code` 字段不再需要；
- 状态体加 `flow` 字段（让网页知道显示「设备码」还是「粘回地址」）；
- `authLoginProbe`（已登录直接完成）改成调用 `sub.Probe`。
- **ChatGPT 与 OpenCode 的网页行为必须不变**：既有 `pkg/gateway/login_api_test.go` 全部原样通过（只允许把桩从 `openai`/`opencode` 包变量改到 `pkg/subscription` 的对应桩）。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/ -run 'Login|Auth' -count=1` → ok。

### Step 6: 全量

**Verify**: `go build ./... && CGO_ENABLED=1 go test -tags fts5 ./... -count=1` → ok；`./scripts/test-db.sh check` → exit 0。

## Test plan

`pkg/gateway/llm_models_api_test.go`（测试名 `TestLLMModelsAPI*`，照 `api_extra_test.go:3994` 的 `providersGet` 脚手架与 `:4033` 起 `TestProvidersDTORoundTripStoresKeyInPlaintext` 的建 Server 方式，用真实状态库）：
1. 新建同 provider/model、不同 base_url 两行 → 都 201；列表两行 `endpoint` 不同。
2. 新建完全相同四元组 → 409，`existing_id` 是第一行；规范化后相同（尾斜杠、大写 provider、空 path vs `/chat/completions`）→ 409。
3. PUT 把 B 改成与 A 相同 → 409；PUT 不带 `api_key` → key 不变（读回 `api_key` 与 `api_key_last4` 都不变）；带空串 → `api_key_set=false`、`api_key=""`。
4. `custom` 不带 `api_path` → 400；带 `/responses` → 201，行 `protocol=responses`、`provider_name` 为「自定义」。
5. `provider: chatgpt` → 400「请通过订阅登录添加」。
6. 删除某主代理的默认行 → 409 带 `default_for`；删除普通行 → 204。
7. **单行与列表**返回写入时的明文 `api_key`（逐字节相等）；**`/choices`、错误响应体、事件与日志**都不含它（grep 三个地方：choices JSON、`slog` 输出、`api_key` 错误的 body）。
8. `/test`：用 httptest 当端点，200 → `{"ok":true}`；401 → `{"ok":false,"error":"…refused"}`；只给 `id` 时用库里的 key（断言 httptest 收到的 Authorization 头）。
8b. 订阅行：PUT 改 `base_url` → 400；只改 `caps` → 200；`refresh-key` 调到假订阅的 `RefreshKey`；列表行带 `source`/`source_name`/`source_state`。
9. `SubscriptionsAPI`：桩一个假订阅（实现 `subscription.Subscription`）注册进去，覆盖 offers、provision 正常/越权 offer 被拒/`key_choice`/`needs_key_consent`。
10. `EnvVarsAPI`：增改删、非法名、列表不含明文。

## Done criteria

- [ ] `go build ./...`、`CGO_ENABLED=1 go test -tags fts5 ./... -count=1` exit 0
- [ ] `./scripts/test-db.sh check` exit 0
- [ ] `grep -n '"/llm-models\|"/llm-providers\|"/subscriptions\|"/env-vars' pkg/gateway/api_extra.go` 都在路由里
- [ ] `grep -n 'json:"api_key,omitempty"' pkg/gateway/llm_models_api.go` 有；`grep -rn 'api_key' pkg/gateway/llm_models_api.go | grep -i 'slog\|Errorf'` 无输出
- [ ] `grep -n "authLoginOpenCode\|authLoginChatGPT" pkg/gateway/login_api.go` 无输出（分支已收进注册表）
- [ ] 既有 `login_api_test.go` 用例全部通过
- [ ] `plans/README.md` 本行已更新

## STOP conditions

- 既有登录测试只有改断言（而不只是改桩的位置）才能通过——说明行为变了，停下。
- 路由框架不支持 `/llm-models/choices` 与 `/llm-models/:id` 并存（静态段与参数段冲突）——停下报告，不要改成别的路径风格。
- 「当前主代理」的概念在某些请求里取不到（例如多副本转发场景）。

## Maintenance notes

- `/api/llm-models/choices` 是**所有**模型下拉框的唯一数据源；新加下拉框不要再去拼目录或配置。
- 登录状态仍是每个网关副本的内存态（今天就是如此）；多副本部署下轮询必须打到同一副本——这是既有限制，未在本计划改变。
- 新订阅只需在 `pkg/subscription` 注册，网关零改动；若出现新的流程类型，需要在 Step 5 的「网关能驱动的流程」里加一支并告诉网页如何显示。
