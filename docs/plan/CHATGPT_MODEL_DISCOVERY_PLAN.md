# ChatGPT 订阅模型动态发现：设计方案与开发计划

状态：P0 已完成真实端点实测（见 §3.4），P1–P5 待评审实施；尚未修改业务代码。
调研日期：2026-09-23。代码基线：Forebrain Harness `cc3cb428`；本地官方 Codex `5c5308fc9a`（提交日期 2026-09-20，不代表已验证为上游最新版本）；实测参照官方 `codex-cli 0.153.4`。

## 1. 任务契约

- **Outcome**：执行 `/connect`、完成 ChatGPT 订阅登录后，向订阅后端请求当前账户可用模型，再显示选择列表；服务端新增模型无需修改 Forebrain Harness 模型名称列表即可出现。
- **Scope**：ChatGPT 模型发现客户端、共享目录查询、TUI 登录后的选择流程、已有 Web 模型目录查询；删除 `pkg/tui/setup.go` 的硬编码模型候选列表。
- **Constraints**：不以更新几个模型名代替动态发现；不把硬编码列表搬到其他文件；不把旧列表伪装成最新列表；不改系统提示词；不读写其他 agent 的配置；复用 OAuth 和现有包边界。
- **Proof**：协议契约测试、登录到选择的流程测试、TUI/Web 一致性测试、隔离真机验证及用户授权的订阅端点验证。

### 1.1 用户明确要求

> `pkg/tui/setup.go` 中的硬编码列表必须删掉。

本计划据此删除该文件内**所有 provider 的 `ModelOptions` 模型字面列表**及该字段，不只删除 ChatGPT 一行。配套移除 `DefaultModel` 的硬编码型号，防止默认值继续引导用户选择过时型号。

- ChatGPT：唯一自动候选来源是本次认证账户的远端目录。
- 其他 provider：复用已有 `llm.Catalog`；本期不建设其远端发现协议。目录没有匹配项时显式手输，不重新增加推荐名单。
- 保留 provider 分组、名称、认证方式、端点等连接元数据；它们不是本次要删除的模型列表。
- `Manual input` 是交互动作，可以保留，但不得作为模型记录混进共享目录。

## 2. 现状与根因

以下位置均来自本轮读取的当前工作区，行号是调研基线位置。

| 位置 | 已确认事实 | 影响 |
| --- | --- | --- |
| `pkg/tui/setup.go:346-376` | `providerChoice` 包含 `DefaultModel`、`ModelOptions`，ChatGPT 候选列表写在源代码中 | 发布新模型必须改代码 |
| `pkg/tui/setup.go:716-750` | `configureChatGPTProvider` 调用登录后直接 `promptModel(...choice.ModelOptions...)` | OAuth 成功不会触发发现 |
| `pkg/tui/setup.go:776-818` | `promptModel` 直接使用传入字符串列表；默认值不在列表时转手输 | 旧默认值可能掩盖新目录 |
| `pkg/tui/setup.go:845-882` | 推理档位统一为 low/medium/high/xhigh，依赖静态 `llm.Lookup` 判断能力 | 换成动态模型名后仍可能给出不支持的档位 |
| `pkg/llm/openai/auth.go:473-542` | `Transport` 已实现到期刷新、Bearer 和账户路由头 | 可复用，不重写登录 |
| `pkg/turn/models.go:14-83` | `ListModelCatalog` 投影 `llm.AllModels()`，再按名称排序 | 没有账户远端发现及来源状态 |
| `pkg/gateway/api_extra.go:297-323` | Web `/models` 调用上述静态查询 | 只修 TUI 会形成两份目录逻辑 |
| `frontend/src/lib/api.ts:1133-1135` | `getModels` 只返回 records | 新增来源/错误信息时必须同步消费 |
| `pkg/tui/commands.go:2095-2135` | `/model` 枚举已配置的 `llm_providers`，不是全部可发现模型 | 本期保持该产品语义，不自动配置全部远端模型 |

根因是**缺少认证后的模型发现链路**，而不是某几个常量未更新。

## 3. 外部协议依据与“最新”的定义

### 3.1 官方文档

本轮已在线读取官方文档：

- `https://learn.chatgpt.com/docs/app-server`，Models / List models：应先发现再渲染选择器，模型、推理档位和默认值依赖账户及客户端，不应硬编码示例。
- `https://learn.chatgpt.com/docs/auth`：区分 ChatGPT 订阅认证和 API key 认证。

文档中的 App Server `model/list` 是 JSON-RPC 接口，不是本项目要直连的 HTTP 模型端点。本项目已有直接访问订阅 Responses 后端的实现，不为列模型新增 Codex 二进制或 App Server 依赖。

### 3.2 本地官方源码协议

参考根目录：`/Users/doudou/workspace/unionj-cloud/codex/codex-rs`。

| 源码 | 用途 |
| --- | --- |
| `codex-api/src/endpoint/models.rs:33-90` | 请求 `models?client_version=...`；解析 `{ "models": [...] }` |
| `model-provider/src/models_endpoint.rs` | 端点装配、认证和请求超时 |
| `protocol/src/openai_models.rs:402-471` | slug、显示名、可见性、优先级、推理档位、上下文及模态字段 |
| `protocol/src/openai_models.rs:908-929` | ChatGPT 模式不以 `supported_in_api` 剔除模型；默认模型由可见项顺序决定 |
| `models-manager/src/manager.rs` | 按 priority 排序、刷新策略、账户身份及缓存处理 |
| `models-manager/src/lib.rs` | `client_version` 的版本格式处理 |

实测已验证的请求形态（2026-09-23，见 §3.4）：

```text
GET https://chatgpt.com/backend-api/codex/models?client_version=<产品内固定的协议版本常量>
Authorization: Bearer <由现有 Transport 注入>
ChatGPT-Account-ID: <由现有 Transport 注入>
OpenAI-Beta: codex-1 / originator: forebrain <由现有 Transport 注入>
```

- 不用 API 平台 `/v1/models` 代替，不混用其 `data` 响应格式。
- 不访问或抓取 ChatGPT 网页模型选择器。
- 不按 `supported_in_api == true` 过滤订阅模型。
- “最新”指**查询完成时，当前账户和已验证客户端协议下后端返回的可见模型**，不是官网所有模型，也不保证和其他账户相同。

### 3.3 `client_version` 策略（2026-09-24 owner 终裁：运行时解析，禁止硬编码）

- `client_version` 是必填参数：缺失返回 400 `invalid_request_error`。
- 声明值实质影响名单：`999.0.0` 比 `0.153.4` 多返回 2 个 `minimal_client_version>=0.155.0` 的模型。产品内**禁止**发送伪造的高版本换取更长名单。
- ~~产品内固定一个有出处的真实官方 codex-cli 版本常量~~（2026-09-23 方案）**已被 owner 推翻（2026-09-24）**：版本号不得硬编码，也不得落任何缓存（磁盘/进程内都不做），必须**每次发现在运行时查询并解析**。
- 现行实现：`openai.ResolveClientVersion(ctx)` 每次调用 `GET https://registry.npmjs.org/-/package/@openai/codex/dist-tags`，取 `latest` 标签、按 codex-cli 同样规则剥掉 pre-release 后缀、校验 whole semver（`^\d+\.\d+\.\d+$`）；registry 不可达或载荷非法即失败并如实上报，不猜版本。选 npm dist-tags 的理由：`@openai/codex` 是官方 CLI 的正式分发渠道，其 `latest` 即"最新已发布 codex 客户端"，无鉴权、无速率门槛、响应数百字节。解析超时 5s、响应上限 64KB，与发现总预算兼容。
- 实测（2026-09-24）：dist-tags `latest=0.156.1`；隔离 gateway 全链路（npm 解析 → 订阅 models 拉取）首答 ~2s，可见模型由 5 个增至 7 个——`minimal_client_version=0.155.0` 门控的 `gpt-6-sol`、`gpt-6-luna` 随动态版本出现，证明硬编码 0.153.4 确实丢失了当前可用模型。
- 观测到 `0.0.0` 与 `0.153.4` 返回完全相同（7 模型、同 ETag），但这不代表 `0.0.0` 语义安全，服务端门控语义未完全确定，禁止据此推断规则。

### 3.4 P0 实测记录（2026-09-23，脱敏）

授权来源：owner 本轮明确同意使用 `/Users/doudou/.forebrain/auth.json` 订阅凭据测试模型拉取接口。探测复用 `openai.Transport`（自动注入认证与账户头），全程未打印、未落盘任何 token；auth.json 内容未被读取进上下文。

| 探测项 | 结果 |
| --- | --- |
| 凭据状态 | `auth_mode=chatgpt`，account_id 存在，token 距过期约 239 小时 |
| 网络 | 非交互 shell 无代理环境变量时直连超时；经系统代理（127.0.0.1:7897，HTTPS_PROXY）后可达，延迟约 0.4–1.7s |
| 缺失 `client_version` | 400，`Field required`，错误体 209 字节 |
| `client_version=0.0.0` | 200，7 模型，361437 字节，ETag `W/"cfd4aef4824f9ff9d68a776f88e1f2d0"` |
| `client_version=0.153.4` | 200，7 模型，与 `0.0.0` 字节数、ETag 完全一致 |
| `client_version=999.0.0` | 200，9 模型，522500 字节，不同 ETag；多出的 `gpt-6-sol`、`gpt-6-luna` 标注 `minimal_client_version=0.155.0` |
| 条件请求 | 携带 `If-None-Match` 复查仍返回 200 全量，未观测到 304 |
| 缓存头 | `Cache-Control: private, no-store` |
| 响应规模 | 7 模型约 361KB、9 模型约 522KB；解析字节上限按 8MB 设置有充分余量 |
| schema | 观测到 57 个字段，与本地官方 `protocol/src/openai_models.rs` 的 `ModelInfo` 基本一致，另含 `minimal_client_version`、`available_in_plans`、`supports_reasoning_summaries` 等；解码按“未知字段忽略、必需字段缺失即协议错误”处理 |

该账户在 `client_version=0.153.4` 下的可见（`visibility=list`）模型，按 priority 排序：

| priority | slug | 档位 | 默认档位 | context_window / max_context_window | comp_hash |
| --- | --- | --- | --- | --- | --- |
| 1 | gpt-6-astra | low, medium, high, xhigh, max, ultra | low | 272000 / 872000 | 3000 |
| 4 | gpt-5.6-sol | low … ultra | low | 272000 / 872000 | 3000 |
| 7 | gpt-5.6-terra | low … ultra | medium | 272000 / 872000 | 3000 |
| 8 | gpt-5.6-luna | low … max | medium | 272000 / 872000 | 3000 |
| 12 | gpt-5.5 | low, medium, high, xhigh | medium | 272000 / 272000 | 2911 |

隐藏项：`gpt-reserve`（priority 3）、`codex-auto-review`（priority 43）。这些值仅是 2026-09-23 该账户快照，**不是**产品内白名单；测试必须使用虚构 slug。硬编码列表中的 `gpt-5.4`、`gpt-5.3-codex`、`gpt-5.2-codex` 已不在后端返回中——用户的“不是最新”完全成立。

脱敏完整响应样本（仅模型元数据，无凭据）：`/tmp/forebrain_models_probe/models_latest-known.json`、`models_far-future.json`，供 P1 契约 fixtures 提炼。

## 4. 推荐设计

### 4.1 最小分层

```text
TUI /connect / 初次配置             Web 既有 /models
       │                              │
       └────── 共享模型发现/投影 ──────┘
                     pkg/turn
                        │
             pkg/llm/openai/models.go
                        │
         现有 openai.Transport → 订阅后端

其他 provider → 共享查询 → 既有 llm.Catalog
```

- `pkg/llm/openai`：负责请求、响应解析、订阅协议字段映射和安全错误，不包含 TUI 交互。
- `pkg/turn`：负责目录投影、筛选、排序、默认选择、来源信息；通过显式注入的单方法发现能力取得远端记录。
- 装配层：从已解析的 Forebrain Harness home/credentials 配置构建客户端；首次配置和运行中连接都能调用，不能要求已有完整运行会话才能发现模型。
- `pkg/tui`：只做登录确认、loading、选择、错误动作与结果展示；不维护另一套候选名单或可见性规则。
- Gateway：解析请求、使用现有认证作用域、调用共享服务、返回 DTO；不能由请求参数指定凭据路径或任意远端 URL。
- 无新增 Go 依赖、无新包、无全局可变账户目录、无数据库迁移。

具体符号可遵循现有命名调整，但消费侧接口仅需一个查询方法；不设计通用 provider 插件框架。现有纯静态 `ListModelCatalog` 可以继续作为本地目录查询路径，由新的上下文感知发现入口复用，不把所有 `llm.Lookup` 强行改为网络调用。

### 4.2 目录契约

共享结果保留现有 `ModelRecord` 的 model_id、model_name、provider、api_model 等字段，并增加实际需要的结构化信息：

| 字段 | 语义 |
| --- | --- |
| `model_id` | `chatgpt/<slug>`，与 API provider 命名空间隔离 |
| `api_model` | 后端原始 slug，最终请求和配置均使用该值 |
| `model_name` | display_name；缺少显示名时使用 slug，不影响模型身份 |
| `supported_reasoning_efforts` | 后端支持的档位列表，不能扩充成本地固定四档（实测已见 low/medium/high/xhigh/max/ultra 六档，静态四档已过时） |
| `default_reasoning_effort` | 后端默认档位；无可用值时保持参数未设置 |
| `is_default` | 经过排序后的首个可见项；不称为“版本号最大” |
| `context_window` / `input_modalities` | 后端提供时原样投影；取 `context_window`（当前默认窗口），不拿 `max_context_window` 冒充（实测两者可达 272000 vs 872000）；未提供保持未知，不套另一个模型的数值 |
| `source` / `fetched_at` | 查询来源和成功获取时间；只对成功远端响应标注实时来源 |

规则：

1. 只把 `visibility == list` 的记录作为自动选择项；隐藏项不出现在普通 picker。
2. priority 升序，等优先级保留服务端顺序；不再按型号字符串猜新旧，也不能被通用目录的字母排序覆盖。
3. 搜索、provider 过滤和 limit 在共享层统一执行；不额外限制 ChatGPT 只能返回原有四个型号。
4. 空 slug 等破坏模型身份的必需字段视为协议错误；未知扩展字段允许忽略。缺失整个 models 字段与合法空数组应区分。
5. 未知型号只要满足协议就能进入列表；测试使用虚构 slug，禁止把测试写成对当前型号的白名单。
6. 远端数据不能覆盖系统提示词、工具规则或权限；不消费 model_messages 等指令字段。

### 4.3 登录后的严格时序

1. 用户明确确认浏览器登录，继续走现有 `chatGPTLogin`。
2. 登录成功后显示“已登录，正在获取可用模型”，使用同一份解析后的凭据路径发起新请求。
3. 本次请求成功后生成列表，显示名称与 slug；底层选择值必须是 slug，不从显示文本反向解析。
4. 只有原配置 provider 也是 ChatGPT，且原模型仍在本次可见列表中，才保留它为当前选择；否则选择远端排序的首项。不得使用别的 provider 的 Defaults.Model。
5. 推理档位来自所选记录。原档位仍受支持时保留，否则用有效远端默认值或 provider default；没有档位时不发送 reasoning effort。
6. 最终确认后复用 `process.Execute`、`ReloadConfig`，保存 `provider=chatgpt`、原始 slug 和所选参数，下一条消息立即生效。
7. 每次成功登录均重新请求，不被之前查询结果拦截。重试目录查询无需重新浏览器登录。

同一 `PromptMainLLMSetup` 供初次配置和 `/connect` 复用，两条入口都必须测试。

### 4.4 请求、取消与错误

- 发现请求建议总预算 10 秒，继承调用方 context；超时包含必要的 token 刷新，不沿用浏览器登录的 10 分钟等待预算。
- 发现请求与现有 Responses 流量一样经默认 transport 走环境变量代理（实测非交互 shell 需显式 `HTTPS_PROXY` 才可达 chatgpt.com；产品内不新增独立代理配置）。解析字节上限 8MB：实测全量响应 0.36–0.52MB，上限只防异常膨胀。
- 复用到期前刷新逻辑；本期不扩展为新的 OAuth 恢复状态机。401 提示重新登录，403 明确无权访问，不无限刷新 token。
- 429、5xx、网络失败、解析错误均显示可区分的安全错误；用户可重试或取消，不后台无限重试。
- 成功空目录明确显示“当前账户未返回可选模型”，不显示硬编码旧名单。
- 保留已有 Manual input：用户必须主动选择，明确“未通过目录验证”，不宣称最新/可用，不凭型号猜能力；推理参数默认不设置。
- 失败/取消不调用 `process.Execute`，不保存一个错误模型。但 OAuth 登录本身已按现状保存凭据；文案不得谎称整个登录和模型配置是原子回滚。
- 请求中的凭据只发往固定订阅端点；发现客户端禁止自动跟随重定向，防止 Transport 在另一 host 上再次注入凭据。HTTP client/transport 的注入仅供内部装配和测试，不新增用户可控端点入口。
- 错误和日志只记录状态分类、耗时、模型数；不输出 token、原始认证响应、完整请求头或账户标识。外部响应按协议规模设置明确字节上限并测试超限路径。

### 4.5 刷新与缓存：首版不引入持久缓存

此需求不需要磁盘缓存、TTL、ETag 条件请求、后台轮询或跨节点失效协议。

- 每次登录后强制请求；打开 ChatGPT 远端目录时请求一次，界面搜索在本次结果上过滤，不每次按键请求。
- 本次交互内保留不可变结果，结束后不把它注册进全局 `llm.Catalog`。
- Web 新的列表加载重新查询；不依赖 TUI 进程内存，因此不同进程/节点也能使用同一发现实现。
- Gateway 仍以该节点已有、已认证的凭据作用域查询；无凭据节点显式返回需要登录，不新增凭据同步机制。
- 将来若实际频率要求缓存，另行设计身份隔离和失效，不在此计划中预建。

### 4.6 删除其他 provider 硬编码列表后的行为

- 从现有 catalog 按精确 provider 筛选，确定性排序，展示显示名与 APIModel。
- 已配置且同 provider 的当前模型可保留为“当前配置”，但不能伪装成目录确认可用的模型。
- 不从别的 provider 猜别名或套餐支持范围，尤其不能把 API 目录当作 Coding Plan 的账户授权证明；来源明确标记为本地目录。
- 无目录记录时走手输，保留现有配置能力，不补一份硬编码列表。
- 不更新 `model_assets/models.json` 来假装完成 ChatGPT 动态发现；本期不承诺其他 provider 名单实时更新。

### 4.7 Web 与现有 /model 边界

- 扩展已有 `/models`，显式查询 ChatGPT 时返回共享发现结果；不把订阅 token 暴露给浏览器。
- 未指定 provider 的聚合查询保留原有本地目录，并仅在已连接 ChatGPT 的认证作用域加入远端记录。无凭据时标为未连接；发现失败时保留其他 provider 的 records，同时附 ChatGPT 的结构化失败状态，不能静默吞错。
- 保持已有 records 包装，增加 provider 级来源/时间/错误信息，避免一个聚合目录只有一个模糊的 fetched_at。
- 同步更新 `frontend/src/lib/api.ts` 与 `useChatStream.ts` 的消费和实际展示调用点：展示 loading/失败，不丢弃新增状态。TUI/Web 使用相同记录、顺序和推理选项，不各自筛选。
- `/model` 仍是已配置模型切换器，不把所有发现记录自动追加到配置。其能力目录、全局上下文预算和 compaction 不是本期重构目标。
- 已发现运行时 `pkg/run/config.go:371` 等仍有静态能力查询；本期不宣称完成全运行时能力目录动态化。必须验证新 slug 和合法 reasoning effort 能真正发送；若新模型必须依赖额外运行时能力改造才能工作，作为阻塞项停下评审，不能仅让列表“看起来支持”。

## 5. 具体开发计划

每个阶段完成后更新本节状态与证据。批准实施前不修改业务代码；实施时先记录工作区 diff 基线，保留用户改动。

### P0 [S] 锁定订阅发现协议和兼容版本 — 已完成（2026-09-23）

- 依赖：无。
- 工具/资源：官方文档、本地 Codex 源码、owner 授权的订阅请求；未打印凭据。
- 结果：endpoint、必填 `client_version`、schema、版本门控、ETag/缓存行为、响应规模与代理要求均已实测确认，证据见 §3.4；脱敏样本在 `/tmp/forebrain_models_probe/`。
- 遗留拍板项：产品内 `client_version` 常量值（建议 `0.153.4`，有出处且实测通过），owner 确认后进入 P1。
- 验收达成：不是 `/v1/models`，不是 App Server JSON-RPC；版本策略有实测依据，不猜型号名单。

### P1 [M] 实现订阅模型发现客户端

- 依赖：P0 的协议契约。
- 文件：新增 `pkg/llm/openai/models.go`、`models_test.go`；必要时更新该包 `doc.go`。
- 工作：HTTP 请求、复用 Transport、context/超时、解析、字段校验、安全错误、拒绝重定向和响应上限；无静态模型 fallback。
- 验收：httptest 覆盖成功、新 slug、空数组、缺失 models、缺失 `client_version` 的 400 形态、隐藏项原始数据、unknown fields、401/403/429/5xx、超时/取消、损坏 JSON、超限和重定向；认证头路径不重复实现；fixtures 参照 §3.4 实测样本在仓内合成为虚构 slug 数据。

### P2 [M] 建立共享目录投影与装配

- 依赖：P1。
- 文件：`pkg/turn/models.go`、`models_test.go`，必要时 `service.go` 的注入字段；`pkg/process` 中新增 `models.go` / `models_test.go` 提供首次配置可用的装配入口。
- 工作：新增 context 感知发现入口，复用现有静态查询；实现 DTO、可见性、排序、默认项、推理档位、来源和错误；显式注入凭据路径，避免账户全局状态。
- 验收：ChatGPT `supported_in_api=false` 不误删；未知 slug 展示；旧型号从远端消失后不残留；两个身份/两个 home 的结果不串用；其他 provider 查询不触发订阅网络。

### P3 [M] 改造 /connect 并删除硬编码

- 依赖：P2。
- 文件：`pkg/tui/setup.go`、`setup_test.go`；确有需要时只修改现有 selector 的记录标签到值映射。
- 工作：删除所有 `ModelOptions` 字面列表和字段、删除硬编码 `DefaultModel`；登录后请求；接入来源/错误动作、动态推理档位、默认项规则、手动输入标识；其他 provider 复用本地目录。
- 验收：初次配置和 `/connect` 都走动态链路；不再存在生产用型号推荐切片；拉取失败不走旧列表；无重新登录的查询重试；取消不写模型配置；选择 slug 正确落盘并热生效。

### P4 [M] 接入已有 Web 模型目录

- 依赖：P2，可与 P3 并行。
- 文件：`pkg/gateway/api_extra.go`、对应 `api_extra_test.go`；`serve_run.go` 的装配；`frontend/src/lib/api.ts`、`frontend/src/composables/useChatStream.ts` 及经引用确认的列表展示点。
- 工作：接入共享发现，保留 records，增加 provider 状态并完整消费；请求取消传递给发现客户端；不新增浏览器 OAuth 流程。
- 验收：同一 fixture 下 TUI/Web 的 slug、顺序、默认项、effort 相同；未连接/失败与真实空目录可区分；客户端不能指定 credentials 路径或上游 URL；非 ChatGPT 目录仍可使用。

### P5 [M] 回归、真机和最终收口 — 已完成（2026-09-24，一项待授权）

- 依赖：P3、P4。
- 工具：Go focused tests、architecture 检查、frontend build、run-forebrain 隔离 harness、授权真实订阅账号。
- 结果：全仓 `CGO_ENABLED=1 go test -tags fts5 ./... -count=1` 通过（首轮 4 个包因机器磁盘满 `errno=28` 链接失败，清理 Go 构建缓存后逐包重跑全绿，非代码回归）；`go vet ./...` 通过；`forebrain` 二进制构建通过；`pnpm --dir frontend build` 与 `vue-tsc --noEmit` 通过；`go test -race`（openai/turn）通过；architecture 测试通过，包图已再生成。
- 真机证据（2026-09-24，隔离 FOREBRAIN_HOME，凭据为 owner 授权的 auth.json 副本，结束后已删除）：
  - gateway `GET /api/models?provider=chatgpt` 实拉成功：5 个可见模型按 priority 返回（`gpt-6-astra` 标记默认，六档 effort 透出），status `{provider: chatgpt, source: chatgpt-account, fetched_at}`，与 §3.4 P0 探测一致——完整生产链路（Transport→FetchModels→turn 投影→HTTP）验证。
  - 聚合 `?q=luna` 同时命中静态 `openai/gpt-5.6-luna` 与远端 `chatgpt/gpt-5.6-luna`；`?provider=openai` 只走静态目录、无 chatgpt status（不触发订阅网络）。
  - 无凭据节点（主 agent 非 chatgpt）`provider=chatgpt` → 200 + `status.error = "ChatGPT login required …"`，records 为空但错误显式，不伪装空目录；主 agent 为 chatgpt 且无凭据时 gateway 启动即正确拒绝（运行配置校验，非 /models 层问题）。
  - 真机复查中发现并修复：错误状态曾序列化零值 `fetched_at`（0001-01-01），已改为成功时才携带（`*time.Time`），并加负断言。
  - TUI（run-forebrain 隔离会话）：`/connect` → ChatGPT 选项 → 登录确认对话框渲染正常，Esc 干净返回 composer。登录后的完整交互无法无头驱动（需浏览器完成 OAuth），由流程测试 + 上述同链路真机网关验证覆盖。
- 待授权项：① 用真实账号发一条最小消息验证所选 slug 经 `/responses` 可调用（消耗订阅用量）；② 由 owner 亲手完成一次浏览器登录的 `/connect` 全流程目检（预期列表首项为 `gpt-6-astra`）。

## 6. 验证矩阵与命令

| 场景 | 必须证明 |
| --- | --- |
| 服务端新增源码从未出现的 slug | 自动显示、可选、配置保存为该 slug |
| 两次登录返回不同集合 | 第二次实际请求，列表按第二次结果完整替换 |
| 模型被隐藏/移除 | 不混入旧硬编码项 |
| API 不支持但订阅可见 | 不被 supported_in_api 误过滤 |
| priority、同优先级、默认旧模型失效 | 顺序确定且默认规则一致 |
| 支持档位与旧配置不一致 | 只允许当前支持值或不设置 |
| 刷新成功/凭据失效/权限拒绝 | 复用凭据机制，错误可操作且无泄露 |
| 超时/取消/429/5xx/错误 JSON | 无挂死、无旧名单伪成功、无错误配置写入 |
| OAuth 成功但目录失败 | 明确凭据已保存，模型配置未完成 |
| 非 ChatGPT provider/无本地目录 | 不走 ChatGPT 请求，仍可配置或手输 |
| TUI 与 Web | 相同源数据下内容与顺序一致，失败状态不丢失 |
| 隔离真机与真实账号 | 假后端证明动态链路；真实请求证明协议/账户可用性，两者不能互相替代 |

实施阶段按风险运行：

```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/openai ./pkg/turn ./pkg/process ./pkg/tui ./pkg/gateway -count=1
CGO_ENABLED=1 go test -race -tags fts5 ./pkg/llm/openai ./pkg/turn -count=1
CGO_ENABLED=1 go vet ./pkg/llm/openai ./pkg/turn ./pkg/process ./pkg/tui ./pkg/gateway
CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1
CGO_ENABLED=1 go build -o ./build/bin/forebrain ./cmd/forebrain
pnpm --dir frontend build
git diff --check
```

- 若修改 pkg 导入关系，先按项目要求运行 `scripts/package-graph.sh` 更新图，再跑 architecture 检查；不得把架构快照失败当成可忽略噪音。
- 新测试与生产文件同名对应，Go 文件遵守项目下划线限制。
- 若实际新增并发共享状态，补对应 race 用例；首版不为假想缓存建立并发框架。
- 真机阶段加载 `run-forebrain` 技能，使用隔离 FOREBRAIN_HOME，先检查渲染再操作；不在用户日常配置上做破坏性测试。
- 对首次出现的环境失败先诊断，必要时在 clean HEAD 隔离基线核实；不通过反复重跑掩盖。

## 7. 风险与不做事项

| 风险 | 控制 |
| --- | --- |
| 私有订阅协议/版本门槛变化 | P0 锁协议、契约 fixtures、授权实测；未知则阻塞，不造名单 |
| 登录成功但发现不可用 | 清楚区分两阶段；重试/取消/显式手输，不静默降级 |
| 删除其他 provider 列表影响特定套餐 | 来源标本地目录、不声称授权，保留手输；不迁移旧白名单 |
| 不同账号或集群节点看到不同结果 | 无全局账户 catalog；每次从当前已认证作用域查询，无凭据显式报错 |
| 新型号元数据不完整或运行时不支持 | 不猜参数，不自动注入远端指令；必要运行时适配先停下评审 |

**不在本期范围**：所有 provider 的远端发现、模型价格抓取、按名字自动升级已有配置、自动追加全部模型到 `/model`、重写 OAuth、Web 新登录页、后台轮询、磁盘/分布式目录缓存、全局能力/compaction 目录重构、模型提示词同步。

## 8. Implementation Status

| 项目 | 状态 | 证据 |
| --- | --- | --- |
| 根因与调用链调研 | 完成 | 本文件 §2、本轮源码读取 |
| 官方协议源码/公开文档核对 | 完成 | 本文件 §3 |
| P0 兼容版本及真实端点验证 | 已完成（2026-09-23） | §3.4 实测记录；`client_version=0.153.4` 已由 owner 确认 |
| P1 客户端 | 已完成（2026-09-23） | `pkg/llm/openai/models.go` + 契约测试全绿 |
| P2 共享目录 | 已完成（2026-09-23） | `pkg/turn` 投影/聚合 + `pkg/process` 装配测试全绿；包图已再生成 |
| P2 共享目录 | 已完成（2026-09-23） | `pkg/turn` 投影/聚合 + `pkg/process` 装配测试全绿；包图已再生成 |
| P3 TUI 与硬编码删除 | 已完成（2026-09-23） | `providerChoice` 的 `DefaultModel`/`ModelOptions` 字段及全部字面列表已删除；登录后拉取（失败可免登录重试）；推理档位来自账户记录；目录 provider 复用共享目录（显示上限 20 + Manual input） |
| P4 Web 共用 | 已完成（2026-09-23） | gateway `/models` 走 `ListModelCatalogLive`（10s 预算、请求取消传递、provider 级 status）；`serve_run.go` 注入本节点凭据源；前端 `getModels`/`fetchModels` 贯通 records+status。核实：web 当前没有任何视图渲染模型目录（`fetchModels` 无组件消费），故无展示点可改，数据已无损贯通 |
| P5 测试与真机收口 | 已完成（2026-09-24） | 本节 P5 记录；两项消费型验证待 owner 授权/亲手目检 |
| P6 client_version 运行时解析（owner 2026-09-24 终裁，替代 §3.3 原固定常量方案） | 已完成（2026-09-24） | `pkg/llm/openai/client_version.go`：每次发现在线查 npm dist-tags 并解析，无硬编码、无磁盘/进程缓存，失败如实报错；真机全链路见 §3.3——动态版本使可见模型 5→7（解锁 `gpt-6-sol`/`gpt-6-luna`） |

评审重点：确认以上范围及无静态 fallback 策略；批准后从 P0 开始，不将“计划文件已生成”报告成“硬编码已删除”。
