# 订阅登录类模型的思考档位：官方来源调研（2026-11-07）

用途：支撑「订阅登录类模型打开编辑抽屉时必须**实时**调用官方接口查询思考档位」这条要求（owner 2026-11-07）。
每一条事实都注明出处：仓库代码路径，或官方文档 URL。**没有出处的就写「未找到」**，不猜。

## 结论速览

| 订阅 | 官方能实时查到档位吗 | 依据 |
|---|---|---|
| **ChatGPT**（chatgpt 订阅） | **能**，实现里已经在用 | `pkg/llm/openai/models.go:22-39` 的 `ModelInfo`：`supported_reasoning_levels[].effort` 与 `default_reasoning_level`；`pkg/turn/models.go:262-293` 把它逐条写进 `ModelRecord.ReasoningEfforts` |
| **OpenCode Go / Console**（opencode-go、opencode） | **不能**：官方 `/v1/models` **不返回**档位 | `pkg/llm/opencode/models.go:19-22` 原文注释：*"The endpoint carries only id/object/created/owned_by: no protocol, no context window, no capabilities."*；`ModelInfo` 只有一个 `ID string` 字段 |
| **火山方舟**（volcengine，Coding Plan / Agent Plan） | **能**：有官方 Models API | `GET https://ark.cn-beijing.volces.com/api/v3/models`，响应 `data[].effort.supported_levels[]` / `data[].effort.default_level`（见下节原文） |

也就是说：owner 说的「已经支持的 chatgpt 订阅和 opencode 订阅都可以从官方查到思考档位」——**ChatGPT 成立；OpenCode 不成立**。
OpenCode 今天的档位来自合并后的模型目录（`llm.ReasoningEfforts(provider, model)`，`pkg/turn/models.go:171-186`），再按协议过滤（`pkg/llm/opencode/models_table.go:151` `EffortsForProtocol`）。若要先做 OpenCode 的「实时档位」，必须先找一个官方端点；**仓库与官方文档里都没有**（见「未找到」一节）。

## 火山方舟（新增订阅登录）官方接口

### Models API（数据面，API Key 鉴权）——推荐接入点

- 页面：<https://docs.volcengine.com/docs/ark/models-api>（`lang=zh`，抓取日期 2026-11-07）
- 请求（文档原文）：

```
curl "https://ark.cn-beijing.volces.com/api/v3/models" \
  -H "Authorization: Bearer $ARK_API_KEY"
```

- 与档位相关的响应字段（文档原文摘录）：
  - `data.effort.supported_levels` — **string[]**，「模型支持的推理强度等级」
  - `data.effort.default_level` — **string**，「模型默认使用的推理强度等级」
  - 同一条里还有：`data.context_window`、`data.max_output_tokens`、`data.token_limits.{context_window,max_input_token_length,max_output_token_length,max_reasoning_token_length}`、
    `data.modalities.input_modalities`（`text` / `image` / `video` / `audio`）、`data.features.thinking.type`（`enabled` / `disabled`）、`data.api_supported`、`data.status`（`Published` / `Retiring`）、`data.name`、`data.version`、`data.id`
- 页面自述用途原文：「查询当前账号可见的基础模型版本及其能力、模态、Token 限制、**推理强度**和支持的 API。返回全部可见模型版本，按创建时间降序排列。」
- 鉴权原文：「API Key 鉴权，请在 API Key 管理页面，获取长效 API Key。」

**接入要点（落在 plan 263 Step 3c）**
1. 一次列表调用即可拿到全部模型的档位：`GET /api/v3/models`，Bearer 用该行的套餐 Key；响应里按 `data.id`（模型版本 ID，如 `doubao-seed-2-1-pro-260915`）与 `data.name`（基础模型名，如 `doubao-seed-2-1-pro`）**双双匹配**，因为我们存的模型名两种写法都可能出现。
2. 这条响应同时解决三件事：档位（`effort`）、上下文窗口与最大输出（`token_limits`）、输入模态（`modalities.input_modalities`）——比 models.json 更权威，按「官方接口实时 > 官方文档表 > 用户勾选」的优先级覆盖。
3. **未确认的一点（执行时必须先试）**：套餐 Key 是发给 `/api/coding`、`/api/plan` 数据面的。`/api/v3/models` 是否接受同一把套餐 Key，文档没有写。执行时先直接试；401/403 就记进「未能核实」并回落文档表（见下）。**不要**用 AK/SK 管控面接口替代（`GetFoundationModel` 只返回厂商/标签/描述，**不含** `effort`，见下）。

### 官方文档里的档位表（回落来源）

- <https://www.volcengine.com/docs/ark/model-parameter-support>（模型支持的参数列表），逐模型列 `thinking` / `reasoning_effort` / `service_tier` 可取的值与默认值。原文示例：
  - `doubao-seed-evolving`：`thinking` = `enabled`（默认）、`disabled`；`reasoning_effort` = `low`、`medium`、`high`（默认）、`minimal`
  - `doubao-seed-2-1-pro-260628`：同上（`high` 默认）
  - `doubao-seed-2-0-lite-260428`：`low`、`medium`（默认）、`high`、`minimal`
- <https://www.volcengine.com/docs/ark/deep-thinking>（深度思考）：`reasoning_effort`（Chat API）/ `reasoning.effort`（Responses API）用于调节思维链长度，取值 `none`、`minimal`、`low`、`medium`、`high`、`xhigh`、`max`，并给出各模型默认值与兼容映射。
- <https://www.volcengine.com/docs/ark/model-deprecation-migration-guide>：同一组字段的取值与映射的另一份官方表述。

### 不能用作档位来源的接口（已核实）

- `POST https://ark.cn-beijing.volcengineapi.com/?Action=GetFoundationModel&Version=2024-01-01`（<https://docs.volcengine.com/docs/ark/get-foundation-model-api>）：
  响应字段为 `AccessType`、`Description`、`DisplayName`、`FeaturedImage`、`FoundationModelTag`（`Domains` / `Languages` / `TaskTypes` / `UsedLibraries`）、`Introduction`、`PrimaryVersion`、`RegionWhiteList`、`VendorName` 等——**没有 `effort` 或任何推理档位字段**；且**仅支持 Access Key 鉴权**（不是 API Key）。故不用。

## ChatGPT（已在用，照抄即可）

- 端点与字段：`pkg/llm/openai/models.go`（`ModelInfo`）
  - `supported_reasoning_levels` → `[]ReasoningEffortPreset{ Effort, Description }`
  - `default_reasoning_level` → `DefaultReasoningEffort`
  - 另有 `context_window`、`input_modalities`、`visibility`、`supported_in_api`、`priority`
- 投影：`pkg/turn/models.go:262-293` `chatGPTModelRecord` 把这些写进 `ModelRecord`（`ReasoningEfforts` / `DefaultReasoningEffort` / `CanReason`）；`turn.DiscoverChatGPTModels` 就是实时发现。
- 结论：ChatGPT 订阅行打开编辑抽屉时，直接复用 `turn.DiscoverChatGPTModels`（或用 `ProbeChatGPTModels` 的同一路径取列表）就能拿到**实时**档位。

## OpenCode（官方接口没有档位）

- `GET {InferenceBase|GoInferenceBase}/v1/models`：`pkg/llm/opencode/models.go:48-`（`FetchModels`），`ModelInfo` 只有 `ID`；
  文件头注释（`:19-22`）明确写「no protocol, no context window, no capabilities」。
- 现有档位来源：`llm.ReasoningEfforts`（`pkg/llm/catalog.go:630-`，合并目录 = 出厂 models.json + 用户 overlay）→ `EffortsForProtocol` 按协议筛（`pkg/llm/opencode/models_table.go:151-`）。
- **未找到**：OpenCode 官方有返回「思考档位」的端点。已查：仓库内所有 `pkg/llm/opencode/*.go`（只有 id 列表）、官方模型列表端点的字段说明（不在仓库内，但从其解码结构体可见端点形状）。
- 因此对 OpenCode 只有三条路，需 owner 选：
  1. 维持现状（档位来自 models.json，**不是实时**），界面对 OpenCode 订阅行标注「档位来自内置数据」；
  2. OpenCode 订阅行的「思考强度」不给档位（只有「默认」）；
  3. owner 提供官方档位接口的 URL 与字段，按 plan 263 Step 3c 的 `Source` 接口接入。

## 其它新增 provider（不是订阅登录，仅备查）

美团 LongCat、腾讯云 TokenHub、BytePlus、智谱 Coding Plan 都是**手填 Key**，不在「订阅登录类」范围。已查到的官方档位表述仅见于火山方舟文档（上）；这三家官方文档里**未找到**逐模型档位表或查询接口——它们的档位仍按 plan 263 Step 3b 的「官方文档表」处理，查不到就让用户勾选。

## 待办（执行时）

1. 用 owner 的火山账号试 `GET /api/v3/models`（Coding Plan Key 与 Agent Plan Key 各一把），把结果记进本文件的「未能核实」小节。
2. ChatGPT 的实时查询接到 `catalog-caps`（订阅行打开抽屉时调用）。
3. OpenCode 按 owner 选的方案落地，并在 `catalog-caps` 的 `efforts_source` 上如实标注来源。

### 未能核实

- 火山套餐 Key 能否调用 `/api/v3/models`（上面第 1 条）。
- OpenCode 是否有官方档位端点（页面级文档未找到；仓库解码结构说明端点不含档位）。

## 实测结果（owner 提供两把真 Key，2026-11-07）

用两把真 Key 各打了一轮（curl 记录见本节末尾的命令摘要）：

| Key | 调用 | 结果 |
|---|---|---|
| **Agent Plan 个人版 Key**（`ark-f606…bc204`） | `GET /api/v3/models` | **HTTP 200**，53 个模型版本，每个都带 `effort.supported_levels` / `default_level`、`token_limits`、`modalities.input_modalities` |
| 同上 | `GET /api/coding/v3/models`（Coding Plan 数据面列表） | **HTTP 200，同一份列表** —— 也就是说**任何一把套餐 Key 都能调 `/api/coding/v3/models` 拿全量模型+档位** |
| 同上 | `POST /api/coding/v1/messages`（Anthropic，`kimi-k3-1-pro`、`glm-5-3-flash` 等多个 id） | **HTTP 200，真实推理成功**（`model: kimi-k3.1-pro`，含 thinking 内容块）——**Agent Plan Key 在 Coding Plan 数据面也能推理** |
| 同上 | `POST /api/plan/v1/messages`（第二次，`thinking: disabled`） | **HTTP 401**（第一次同参数曾 200；行为不稳定，见下「注意」） |
| 同上 | `GET /api/plan/v3/models` | 404（该路径不存在） |
| **Coding Plan 个人版 Key**（`ark-0802…ac7ac`） | `GET /api/coding/v3/models`、`/api/v3/models`、`/api/coding/v1/models` | **HTTP 401** AuthenticationError |
| 同上 | `POST /api/coding/v1/messages`（多模型多写法：`kimi-k3-1-pro`、`kimi-k3-1-pro-261024`、`glm-5-3-flash-260828`、`doubao-seed-2-0-code-preview-260215`、`ark-code-latest`；`Authorization: Bearer` 与 `X-Api-Key` 两种头） | **HTTP 401**；唯一例外：`doubao-seed-2-0-code-preview-260215` 返回 **404 UnsupportedModel "The requested model does not support the coding plan feature"** —— 说明请求到达了Coding Plan 网关并通过了某种前置判定，但这把 Key 本身仍然鉴权失败 |
| 同上 | `POST /api/coding/v3/chat/completions`（`ark-code-latest`）、`/api/v3/chat/completions` | HTTP 401 |
| 同上 | `POST .../api/coding/v1/messages` 打到 BytePlus 基址 | 401「API key doesn't exist」（区域不匹配，符合预期） |

命令摘要（复测用）：

```
curl -H "Authorization: Bearer <KEY>" https://ark.cn-beijing.volces.com/api/v3/models        # 200（Agent Key）
curl -H "Authorization: Bearer <KEY>" https://ark.cn-beijing.volces.com/api/coding/v3/models # 200（Agent Key）；401（Coding Key）
curl -X POST https://ark.cn-beijing.volces.com/api/coding/v1/messages \
  -H "Authorization: Bearer <KEY>" -H "anthropic-version: 2023-06-01" \
  -d '{"model":"kimi-k3-1-pro","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}'  # 200（Agent Key）；401（Coding Key）
```

### 从成功响应里拿到的档位（官方实时值，可直接进文档表）

| 模型 | supported_levels | default | 输入模态 | context / max_output |
|---|---|---|---|---|
| kimi-k3-1-pro（261024） | low, high, max | max | text,image,video | 1M / 1M |
| doubao-seed-2-1-pro（260915 / 260628） | low, medium, high, minimal | high | text,image,video | 1M·262K / 262K |
| doubao-seed-evolving | low, medium, high, minimal | high | text,image,video | 1M / 262K |
| deepseek-v4-flash（261017 / 260910） | low, high, max, none | high | text,image,video | 1M / 393K |
| deepseek-v4-pro | low, high, max, minimal | high | text | 1M / 393K |
| glm-5-3-flash（260828） | low, high, max | max | text,image,video | 1M / 131K |
| glm-5-2（260617） | none, minimal, high, max | max | text | 1M / 131K |
| doubao-seed-2-0-code-preview（260215） | high, medium, low, minimal | high | text,image,video | 262K / 131K |
| doubao-seed-2-0-lite（260428） | low, medium, high, minimal | medium | text,image,video,audio | 262K / 131K |
| doubao-seed-character（260628） | low, medium, high, minimal | minimal | text,image,video | 131K / 32K |

（图片/视频/3D/向量类模型 `effort` 为 null——不是对话模型，接入时跳过。）

### 结论（修正上一节的推测）

1. **档位查询不用挑 Key 的套餐类型**：`GET /api/coding/v3/models`（或 `/api/v3/models`，二者同表）用任一把有效的套餐 Key 都能调。plan 263 Step 3c / 265 `catalog-caps` 落地时：**用该行自己的套餐 Key 调 `/api/coding/v3/models` 一次，按 `data.name` 或 `data.id` 匹配模型，取 `effort.supported_levels` / `default_level`**。上一节「Key 可能 401 就回落文档表」的担心基本消除（仍保留回落逻辑兜底）。
2. **注意（行为不稳定）**：Agent Plan Key 第二次打 `/api/plan/v1/messages` 时 401（第一次同参数 200）。执行时不要把「一次 401」当成 Key 失效去清用户配置——重试一次再判定，并把这种不稳定记进运维备注。
3. **未解之谜（不阻塞）**：Coding Plan Key 在 Coding Plan 网关上鉴权失败（`doubao-seed-2-0-code-preview` 还给了「不支持 Coding Plan」的 404，说明路由到了正确网关）。可能与 Key 的状态（未签协议/未选模型/ restricted）有关。owner 在控制台核对这把 Key 的状态即可；**不影响档位查询功能**（用 Agent Key 或任一有效 Key 都能拿全表）。
4. OpenCode 仍无官方档位端点（本文件上文），维持三个备选方案等 owner 选。

## OpenCode 档位的官方来源已确认（owner 2026-11-07 指定）

**OpenCode Go / Console 的思考档位必须调用 <https://models.dev/catalog.json?type=all> 实时查询。**

实测（2026-11-07，HTTP 200，6.5 MB）：

- 顶层是 `{providers, models}` 两张表。
- `providers['opencode-go']`：39 个模型；`providers['opencode']`：136 个模型。
- 每个模型条目带：
  - `reasoning`（bool）——是否推理模型；
  - `reasoning_options` ——**就是档位**：`[{"type":"effort","values":["low","medium","high"]}]`；`{"type":"toggle"}` 表示只有开关没有档位；`[]` 表示没有档位信息；
  - `limit.{context,input,output}`、`status`（`deprecated` 的要过滤）。
- 例：`opencode-go/step-5-preview` = low/medium/high；`opencode/claude-opus-4-8` = low/medium/high/xhigh/max；`opencode/gpt-5.6-sol` = none/low/medium/high/xhigh/max；`opencode-go/longcat-2.0` = toggle（无档位）。

### 落地口径（plan 263 Step 3c 的 OpenCode `Source`）

1. **实时抓取**：`GET https://models.dev/catalog.json?type=all`（无鉴权），解析 `providers['opencode-go'].models` / `providers['opencode'].models`；命中 `id` 即取 `reasoning_options`。6.5 MB 每次拉太大，进程内缓存按（provider,id）存结果，TTL 建议 6 小时；失败/超时回落内置表（models.json 的 reasoning_options 本来就是从这份上游同步的）。
2. **过滤**：`status == 'deprecated'` 的行不作为档位来源；`type: "toggle"` 或 `values` 为空 → 视为「无档位」（界面不给思考强度选择，与今天 `EffortsForProtocol` 的空档位语义一致）。
3. **`efforts_source` 如实标注**：OpenCode 订阅行的档位来源是 `"models.dev"`（第三方上游），不是 OpenCode 自家 API——界面不需要展示这个字段的原文，但回退链要正确：models.dev 实时 > 内置表（同一上游的快照）> 用户勾选。

### 修正此前结论

上一节「OpenCode 未找到官方档位端点」仍然成立（OpenCode 自家的 `/v1/models` 只有 id）；本节是 owner 指定的**替代实时来源**，按此执行，不再是「三个备选方案等 owner 选」。

## 范围扩展（owner 2026-11-07）：models.dev 是「除两类外的所有模型」的档位来源

owner 裁定：`https://models.dev/catalog.json?type=all` 用于 **OpenCode 订阅、内置支持的厂商的模型、自定义模型**——即**除**以下两类之外的**全部**模型来源：
1. **ChatGPT 订阅**（自己接口实时返回 `supported_reasoning_levels`）；
2. **火山方舟订阅**（`GET /api/coding/v3/models` 实时返回 `effort.supported_levels`）。

**回落规则（owner 同日定稿）**：models.dev 里**查不到**的模型（没有条目，或条目里没有 effort 档位）→ 界面显示「支持的思考强度」让用户自己勾选。不再有第三层「官方文档表」——plan 263 Step 3b 的档位表只作为**内置数据**存在于 models.json（它本来就同步自 models.dev），运行时的回落链只有两级：**models.dev 实时 → 用户勾选**。

### 覆盖范围与查询方式（实测 2026-11-07）

| 模型来源 | 在 models.dev 里怎么查 |
|---|---|
| OpenCode Go / Console 订阅 | `providers['opencode-go']`（39 个）、`providers['opencode']`（136 个），按模型 `id` 匹配 |
| 内置厂商的模型 | `providers['<厂商id>'].models` 按模型名匹配；实测各厂商条目：openai 51/anthropic 17/alibaba 53/google 36/deepseek 4/xai 12/mistral 36/moonshotai 6/zhipuai 17/minimax 8/volcengine 16（带 effort 档位的分别为 33/13/6/18/4/5/13/3/4/1/14） |
| 自定义模型 | ①带 `vendor/` 前缀（如 `openai/gpt-4o`）：直接在 `models` 顶层表按完整 `vendor/model` 键查；②不带前缀（如 `deepseek-v4-flash`）：按「末段等于该名字」在 `models` 顶层表匹配（`deepseek-v4-flash` → `deepseek/deepseek-v4-flash`，`glm-5.3` → `zhipuai/glm-5.3`；**多命中时取第一个**（按响应里的稳定顺序），不算查不到） |

档位字段：每条模型的 `reasoning_options`（`{"type":"effort","values":[...]}`；`toggle` / 空 = 查不到档位 → 用户勾选）；`status:"deprecated"` 过滤；`limit` 顺带给出窗口。

### 已知缺口（如实记录，均按 owner 规则落到「用户勾选」）

- `meituan`、`tencent`、`byteplus` 在 models.dev **没有条目**（实测 NOT FOUND）→ 这三家内置模型的档位由用户勾选。
- 内置厂商条目里带档位的模型是少数（如 alibaba 53 个里只有 6 个带档位）：models.dev 没给档位的模型同样由用户勾选，不把它当成全量权威。
- models.dev 是第三方上游（models.json 的同一来源），不是各厂商官方——`efforts_source` 如实标 `"models.dev"`；失败/超时同样落到用户勾选。

### 对 plan 263 Step 3c 的影响

`pkg/llm/modelinfo` 的 `Source` 接口按「模型来源」分发，优先级固定为：
1. ChatGPT 订阅行 → OpenAI/Codex 后端模型列表（实时，已在用）；
2. 火山方舟订阅行 → `GET /api/coding/v3/models`（实时，实测 200）;
3. **其余一切（OpenCode 订阅、内置厂商、自定义）→ models.dev 实时**；
4. **models.dev 查不到 → 「支持的思考强度」用户勾选**（唯一的回落；plan 263 Step 3b 的文档表不再作为运行时回落层，只留在 models.json 里作为非档位数据的来源）。

### 对 plan 266/267 的影响

- 网页/TUI 判断「显示勾选还是下拉」只看一个字段：`catalog-caps` 的 `efforts_known`。**`efforts_known = models.dev 查到了（或两类订阅接口查到了）`**；models.dev 查不到 → `efforts_known=false` → 显示「支持的思考强度」复选。
- 内置厂商模型在编辑抽屉里可能因此出现复选（例如 meituan/tencent 的模型）——这是**有意的**，不再以「内置=必有官方档位」为由隐藏勾选。
