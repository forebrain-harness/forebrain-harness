# WorkBuddy 订阅模型列表与官方客户端下拉框保持一致（根因：请求产品身份 + 列表选取源）

状态：待审批（owner 批准后实施）
计划日期：2026-11-15
基线：工作区 `dev`，HEAD `9f23024a`（`git rev-parse --short HEAD`），主树有他人未提交改动（`pkg/tui/*`、`pkg/architecture`、前端等，见 §4 Out of scope）
实施目录：`/Users/doudou/workspace/unionj-cloud/forebrain-harness`（当前源码，不用 worktree）
计划落地：批准后把本文件复制到 `docs/plan/WORKBUDDY_MODEL_CATALOG_PLAN.md`（`docs/plan/` 已 gitignore，不入库）

---

## 1. 目标（Why this matters）

owner 报告：TUI 里 `/connect` 登录 WorkBuddy 订阅后拿到的模型列表全是旧模型，**Hy 家族只有 hy3，没有 Hy4**，也没有 GLM-5.3 / Space-Bunny / MiniMax-M3.1-Flash / Kimi-K3.1-Pro 等最新模型，与 WorkBuddy 软件模型下拉框不一致。

验收标准（owner 原话）：「forebrain tui 拿到模型列表必须与 workbuddy 软件模型下拉框里的模型列表保持一致」；「请求头必须完全对齐 workbuddy，必须完全伪装成 workbuddy 发起请求」。

owner 本次两项裁决（2026-11-15，本会话）：
1. TUI 列表**隐藏 `auto`** 行（官方下拉框不显示 Auto）。
2. 允许验收时发**一次最小真实调用**（hy4free，计费倍率 x0.00）证明新模型 id 可被网关接受；除此之外 agent 执行的验收仍用 fake/离线手段。

---

## 2. 根因（证据链，已现场复现）

### 2.1 触发路径

1. `/connect` → 选 WorkBuddy → 插件 `workBuddySubscription.Offers`
   源码：`plugins/subscriptions/workbuddy/plugin.go:194-218`
   ```go
   func (workBuddySubscription) Offers(ctx context.Context, env subscription.Env) ([]subscription.Offer, error) {
       records, fromStatic := FetchModels(ctx, env.DB, env.Home)
       installBuiltinModels(records)
       ...
   ```
2. `FetchModels` → `fetchAccountModels` 取 `GET <endpoint>/v3/config` 的 `data.models`
   源码：`plugins/subscriptions/workbuddy/models.go:144-182`、`models.go:267-283`
   ```go
   func fetchAccountModels(ctx context.Context, creds Credentials) ([]wireModel, error) {
       body, err := listingJSON(ctx, creds, "/v3/config")
       ...
           Models json.RawMessage `json:"models"`
   ```
3. 该 GET 的请求头来自 `disguiseHeaders`
   源码：`plugins/subscriptions/workbuddy/transport.go:131-171`
   ```go
   header.Set("User-Agent", productName+"/"+productVersion)   // → "WorkBuddy/5.9.3"
   header.Set("X-Product", productName)                       // → "WorkBuddy"
   header.Set("X-Product-Version", productVersion)
   ```
   常量在 `plugins/subscriptions/workbuddy/plugin.go:25-32`（`productName = "WorkBuddy"`、`productVersion = "5.9.3"`、`productPlatform`、`authMethod`）。

### 2.2 复现（真实端点，只读 GET，不消耗模型配额）

用本机已保存的登录（`~/.forebrain/workbuddy.json`，access_token 有效期至 2026-12-13）对 `https://www.workbuddy.cn/v3/config` 发同一 GET，只改身份头：

| 变体 | 结果 |
| :--- | :--- |
| A 现插件头（`User-Agent: WorkBuddy/5.9.3`、`X-Product: WorkBuddy`、`X-IDE-*: 5.9.3` …） | 200，`data.models` **39 条**，Hy 家族只有 `hy3` |
| B 仅把 `X-Product` 换成 `SaaS`、`X-IDE-Version` 换 `0.0.0` | 200，39 条（不变） |
| C A + `X-Agent-Purpose`/`x-codebuddy-request` | 200，39 条（不变） |
| D `User-Agent: WorkBuddy/5.9.3 CLI/2.165.0`、`X-Product: WorkBuddy`、带 bearer | 200，**61 条**，含 `hy4free/hy4std/glm-5.3/glm-5.3-flash/glm-5.2/space-bunny/minimax-m3.1-flash/kimi-k3.1-pro/step-5-preview/mimo-v2.6-pro` |
| E `User-Agent: … CLI/1.0.0`（版本号不同） | 200，61 条（`CLI/` 段存在即可，版本号不参与判定） |
| F `CLI/2.165.0` 但**不带** `X-Product` | 200，61 条 |
| G 只带 `CLI/2.165.0` + bearer + Accept（最小头集） | 200，61 条 |

结论：**决定产品配置版本的是 User-Agent 里的 `CLI/<cliVersion>` 段**；`X-Product`、`X-IDE-*`、`X-Agent-Purpose` 等不决定打哪一版配置。

### 2.3 官方客户端自己怎么发

- 客户端主进程日志（`~/.workbuddy/logs/main.log`）记录它自己的 product-config 请求头：
  ```json
  {"user-agent":"WorkBuddy/5.9.3 CLI/2.165.0","x-product":"SaaS","x-request-id":"…"}
  ```
  以及每 10 分钟一次 `[RemoteConfig] Fetch success … remoteModels=61 … modelPromotions,modelTiers,models …`；同日志记录下拉框所用 agent 清单：
  `cli.models=["auto","hy4free","hy4std","hy3","hy3-x","deepseek-v4.1-flash","glm-5.3","glm-5.3-flash","glm-5.2","space-bunny","minimax-m3.1-flash","kimi-k3.1-pro","kimi-k3-1","kimi-k2.8-preview","deepseek-v4-pro","step-5-preview","mimo-v2.6-pro"]`
- 客户端缓存（`~/.workbuddy/local_storage/wb_entry_d43e96994f944cfb77961c2ea7d04605.info`）保存的整份产品配置：`data.agents[0] = {name:"cli", tags:["cli","default","model:craft"], modelTags:["craft"], models:[…同上 17 个]}` + 61 条 `data.models`，其中 `hy4free` 名称 `Hy4`、`credits:"x0.00"`，`glm-5.3` `credits:"x0.69"`，`space-bunny` `x0.08`，`minimax-m3.1-flash` `x0.08`，`kimi-k3.1-pro` `x0.97`——与 owner 截图逐行一致（Hy4 0.00x、DeepSeek-V4.1-Fl 0.11x、GLM-5.3 0.69x、GLM-5.2 0.79x…）。截图第一行 Hy4 之后是 Hy3、DeepSeek-V4.1-Fl…、GLM-5.3…，**没有 Auto**。
- `CLI/2.165.0` 的来源：客户端自带 agent CLI 包版本，`/Applications/WorkBuddy.app/Contents/Resources/app.asar.unpacked/cli/package.json` → `{"name":"@genie/agent-cli","version":"2.165.0"}`。
- 聊天请求（`/v2/chat/completions`）用的是同一套身份：客户端代码里 `X-Agent-Intent: craft`、`X-Agent-Purpose: conversation`、`x-codebuddy-request: 1`、`X-Requested-With: XMLHttpRequest`、`X-Conversation-ID/-Request-ID/-Message-ID`、`X-User-Id`、`X-Enterprise-Id`+`X-Tenant-Id`、`X-Domain`、`X-Private-Data: true`、`X-Product: SaaS`（`deploymentType`）、`X-Auth-Method`/`X-Id-Source`/`X-Userinfo`（认证模块常量 `HTTP_HEADER_AUTH_METHOD="X-Auth-Method"` 等）。

### 2.4 第二处根因：即使拿到新配置，选取源也不对

`fetchAccountModels` 只读 `data.models`（新配置下 61 条，含 `fast-model/balanced-model/deep-model` 路由别名、`hy3-b/hy3-c`、`hy4-preview-*` 预览别名、`wb-lite`（`{"disabled":true}`）以及视频/图片/补全模型），随后 `FetchModels` 用**离线兜底表**（`models_static.go`，由 WorkBuddy 5.9.3 的 `product.json` 生成，58 行）判定「是不是会话模型」（`keepLiveModel`/`chatCapable`，`models.go:113-128`、`189-197`）。官方下拉框的清单不是这个，而是 **`data.agents` 里会话 agent（`cli`，tag `model:craft`）自己声明的 `models` 有序清单**（17 个 id）。

后果：只改 UA 而不改选取源，TUI 会列出一堆不该出现的东西（路由别名、预览别名、`wb-lite`、视频模型），仍与下拉框不一致。

### 2.5 第三处根因：离线兜底表本身是旧的

`plugins/subscriptions/workbuddy/models_static.go` 由 5.9.3 的 `product.json` 生成（58 行）。17 个会话模型里有 7 个不在表里：`hy4free`、`hy4std`、`space-bunny`、`minimax-m3.1-flash`、`kimi-k3.1-pro`、`kimi-k3-1`、`step-5-preview`、`mimo-v2.6-pro`（共 8 个）；表里还留着 `hy4-preview*` 等旧别名。列表请求失败/未登录时走兜底（`FetchModels` 的 `fromStatic` 分支），症状与本次 bug 相同。

### 2.6 排除的竞争解释

| 解释 | 排除依据 |
| :--- | :--- |
| 端点选错（`copilot.tencent.com` / `codebuddy.cn` 才是新表） | 三个主机实测都返回同一份 41764 字节响应；差别只在 UA |
| 需要 `X-Product: SaaS` 才能拿新表 | 变体 B 仍是 39 条；变体 F（无 `X-Product`）仍是 61 条 |
| 需要 `X-Agent-Purpose` / `x-codebuddy-request` | 变体 C 仍是 39 条 |
| CLI 版本号写死有风险 | 变体 E：`CLI/1.0.0` 同样返回 61 条——只有段存在与否重要 |
| 登录 token 权限不同 | 同一 token、同一主机，只改 UA 就换版本 |
| 企业列表接口的问题 | 企业列表是叠加项（`mergeEnterpriseModels`），与个人账号无关；本次差异出现在个人 `/v3/config` |
| 只是 TUI 缓存/旧 DB 行 | 同机会话的 `fb_llm_models` 里没有 `provider='workbuddy'` 的行；问题出现在 `/connect` 的 offers 列表，由 `Offers()` 现算 |

---

## 3. 修复设计

### 3.1 请求身份完全对齐官方客户端（`transport.go`，唯一常量源）

改 `plugins/subscriptions/workbuddy/plugin.go` 的常量块：

```go
const (
	productName     = "WorkBuddy"
	productVersion  = "5.9.3"
	// cliVersion is the agent CLI build the product ships beside the desktop app
	// (@genie/agent-cli); the gateway answers its product config per client
	// identity, and only the User-Agent carrying "CLI/<cliVersion>" gets the
	// current catalog (Hy4, GLM-5.3, Space-Bunny, …). Without it the legacy
	// desktop catalog (Hy3 and older) comes back.
	cliVersion = "2.165.0"
	// deploymentType is product.json's deploymentType: the cloud (SaaS) product.
	deploymentType = "SaaS"
	productPlatform = "workbuddy"
	authMethod      = "browser"
)
```

`disguiseHeaders`（`transport.go:131-171`）改为官方客户端的头集合，值都取客户端自己的值：

| Header | 值 | 证据 |
| :--- | :--- | :--- |
| `User-Agent` | `WorkBuddy/5.9.3 CLI/2.165.0`（`productName+"/"+productVersion+" CLI/"+cliVersion`） | 客户端日志 + `cli/package.json` |
| `X-Product` | `SaaS` | 客户端代码 `X-Product: deploymentType`；日志 `x-product: SaaS` |
| `X-Product-Version` | `5.9.3`（保留现状） | CLI 常量 `PRODUCT_VERSION_HEADER` 存在；值不决定配置版本（变体 E/F） |
| `X-IDE-Type` / `X-IDE-Name` | `WorkBuddy` | 客户端代码默认真值 |
| `X-IDE-Version` | `5.9.3`（保留现状） | 同上；客户端默认 `0.0.0` 由宿主版本覆盖 |
| `X-Agent-Intent` | `craft` | 客户端 `meta["codebuddy.ai/mode"] ?? "craft"` |
| `X-Agent-Purpose` | `conversation`（**新增**） | 客户端会话请求代码 |
| `X-Agent-Runtime` | `local-agent` | `AGENT_RUNTIMES=["local-agent","cloud-agent","agent-agent"]` |
| `X-Requested-With` | `XMLHttpRequest`（**新增**） | 客户端请求头代码 |
| `x-codebuddy-request` | `1`（**新增**） | 客户端请求头代码 |
| `X-Conversation-ID` | 每请求 uuid（现状） | 客户端 |
| `X-Conversation-Request-ID` | 同 `X-Conversation-ID`（**新增**，客户端用 conversation 的 requestId，本插件没有独立会话，故同值） | 客户端 |
| `X-Conversation-Message-ID` | 同 `X-Request-ID`（**新增**，客户端内部请求把二者置同值） | 客户端 |
| `X-Request-ID` | 每请求 uuid（现状） | 客户端 |
| `X-Private-Data` | `true` | 客户端 |
| `X-User-Id` | URL-encoded uid（现状） | `product.json` `authentication.attributes.usernameHeader/usernameEncode` |
| `X-Enterprise-Id` | 有企业时（现状） | 客户端 |
| `X-Tenant-Id` | 有企业时同 `X-Enterprise-Id`（**新增**） | 客户端 |
| `X-Auth-Method` / `X-Userinfo` | 现状规则（`LoginMethod` 非空时；身份四元组） | 客户端认证模块常量 |
| `X-Domain` | 凭据 domain（现状） | 客户端 |
| `X-Model-ID` | 本次模型 id（现状） | 客户端 `MODEL_ID_HEADER` |
| `Accept` / `Content-Type` | `application/json`（现状；聊天时沿用客户端原始 Accept/Content-Type） | 现状 |

不改的头一行都不多加（客户端不发的头不加）；`auth.go` 的登录/刷新路径不经过 `disguiseHeaders`（`grep -n disguiseHeaders plugins/subscriptions/workbuddy/*.go` 只有 `models.go:353`、`transport.go:96`），本次不动。

同一函数服务两条路径：`GET /v3/config`（`models.go:353`）与 `POST /v2/chat/completions`（`transport.go:96`），因此列表与聊天身份一起对齐。

### 3.2 列表选取源对齐下拉框（`models.go`）

1. `wireModel` 增补一个字段（配置里 `wb-lite` 就是 `{"disabled":true}`）：
   ```go
   Disabled bool `json:"disabled"`
   ```
2. 新增线上 agent 形状与选取函数：
   ```go
   // wireAgent is one agent of a product config: the surface it serves (its
   // tags) and the models it offers, in order.
   type wireAgent struct {
       Name   string   `json:"name"`
       Tags   []string `json:"tags"`
       Models []string `json:"models"`
   }

   // surfaceAgentTag marks the agent a conversation picker reads: the product
   // tags it "model:craft", the intent every request this plugin sends declares.
   const surfaceAgentTag = "model:craft"
   // routerModelID is the product's routing alias: the official picker does not
   // list it (owner 2026-11-15), so neither does a surface.
   const routerModelID = "auto"
   ```
   `fetchAgentModels`/`narrowToSurface(catalog []wireModel, agents []wireAgent) []wireModel`：
   - 在 `agents` 里找 `Tags` 含 `model:craft` 的 agent（找不到时退回 `Name == "cli"`）；
   - 找不到任何会话 agent → 原样返回目录（保底：产品没给 surface 清单时不猜测）；
   - 否则按 agent 的 `models` 顺序，从目录里取对应条目，跳过目录里不存在的 id、`Disabled == true` 的条目、以及 `routerModelID`；
   - 保持 agent 声明顺序（= 下拉框顺序）。
3. `fetchAccountModels` 同时解出 `data.agents`，返回 `narrowToSurface(models, agents)`：
   ```go
   var answer struct {
       Data struct {
           Models json.RawMessage `json:"models"`
           Agents []wireAgent     `json:"agents"`
       } `json:"data"`
   }
   ```
4. `FetchModels` 保持现有流水线（个人列表 → 企业列表叠加 → `keepLiveModel` → `mergeLiveModel`），企业列表仍可追加账户清单里没有的 id（现有 `mergeEnterpriseModels` 语义不变）。

### 3.3 离线兜底表重生成（`models_static.go`）

- 在插件目录新增一次性生成器 `plugins/subscriptions/workbuddy/gen_models_static.go`，带 `//go:build ignore`，读一个产品配置 JSON 路径作为参数，输出新的 `models_static.go`。生成规则：
  - 只取**会话清单**：`data.agents` 中含 `model:craft` 的 agent 的 `models`，按顺序，**跳过 `auto`**；
  - 每条映射：`id`、`name`、`maxInputTokens`、`maxOutputTokens`、`supportsToolCall`、`supportsImages`、`reasoning.supportedEfforts`（为空则取 `reasoning.effort`）、`reasoning.defaultEffort`、`reasoning.canDisableThinking`（布尔指针，缺省 nil）；
  - 文件头注释改为真实来源：`// Code generated by gen_models_static.go from the WorkBuddy product config (WorkBuddy 5.9.3, CLI 2.165.0, 2026-11-15); DO NOT EDIT.`
  - 期望行数 16（17 − auto）。
- 生成器读取的 JSON 用一次真实 GET 落盘（命令见 §5 Step 3，不含任何密钥；请求头带敏感值，脚本只打印模型统计）。

新表关键值（测试断言依据，来自线上配置）：`hy4free`/`hy4std` 名 `Hy4`、窗口 960000/64000、`efforts=[high]`、`defaultEffort=high`、`canDisableThinking=false`；`glm-5.3` 1000000/64000、`[low high max]`/`high`；`glm-5.2` 1000000/64000、`[high xhigh]`/`high`、`canDisableThinking=true`；`space-bunny` 1000000/128000、`[low medium high xhigh max]`/`max`；`minimax-m3.1-flash` 1000000/128000、`[low medium high xhigh max]`/`max`；`kimi-k3.1-pro` 1000000/32000、`[low high max]`/`high`；`step-5-preview` 1000000/64000、`[low medium high]`/`high`；`mimo-v2.6-pro` 1000000/128000、无 efforts。

### 3.4 行为变化矩阵

| 场景 | 旧行为 | 新行为 | 是否刻意修订 |
| :--- | :--- | :--- | :--- |
| 已登录，`/v3/config` 正常 | 39 条旧目录 → offers 里 Hy 家族只有 Hy3，无 GLM-5.3/Space-Bunny/Kimi-K3.1/… | 61 条当前目录，按会话 agent 顺序给出 16 个 offer（Hy4、Hy4、Hy3、Hy3、DeepSeek-V4.1-Flash、GLM-5.3、GLM-5.3-Flash、GLM-5.2、Space-Bunny、MiniMax-M3.1-Flash、Kimi-K3.1-Pro、Kimi-K3、Kimi-K2.8-Preview、DeepSeek-V4-Pro、Step-5-Preview、Xiaomi MiMo-V2.6-Pro） | 是（bug 修复） |
| 列表里出现 `auto` | 作为 isDefault 出现在 offers | 不作为 offer（owner 2026-11-15 裁决：与下拉框一致） | 是 |
| 列表里的路由别名/预览别名/`wb-lite`/视频图片模型 | 部分会漏进 offers（未在兜底表里的 id 默认保留） | 不再出现（不在会话 agent 清单） | 是 |
| 聊天请求（`/v2/chat/completions`） | UA `WorkBuddy/5.9.3`（旧版产品身份） | UA `WorkBuddy/5.9.3 CLI/2.165.0` + 客户端头集合；新模型 id 由当前版本配置解析 | 是 |
| 未登录 / 列表请求失败 | 兜底表 58 行 → 42 个会话模型（含 `default`、`hy4-preview`、`kimi-k3-2` 等旧行） | 兜底表 16 行 = 与下拉框同一清单 | 是 |
| 企业账号 | 个人列表 + 企业覆盖/追加 | 同上（会话清单 + 企业覆盖/追加） | 否（语义保持） |
| 401 刷新后重发 | 逻辑不变 | 逻辑不变 | 否 |

---

## 4. Scope

**In scope（只改这些文件）**
- `plugins/subscriptions/workbuddy/transport.go`（`disguiseHeaders` 对齐客户端头集合）
- `plugins/subscriptions/workbuddy/plugin.go`（常量：`cliVersion`、`deploymentType`）
- `plugins/subscriptions/workbuddy/models.go`（`wireModel.Disabled`、`wireAgent`、`narrowToSurface`、`fetchAccountModels`、`FetchModels` 接线）
- `plugins/subscriptions/workbuddy/models_static.go`（重生成）
- `plugins/subscriptions/workbuddy/gen_models_static.go`（新生成器，`//go:build ignore`）
- `plugins/subscriptions/workbuddy/workbuddy_test.go`（更新受影响断言 + 新增回归）
- `docs/plan/WORKBUDDY_MODEL_CATALOG_PLAN.md`（本计划复制件 + 验收记录；gitignore）

**Out of scope（明确不碰）**
- `plugins/subscriptions/workbuddy/auth.go`（登录/刷新/凭据存储；头集合变更不涉及）
- `plugins/subscriptions/qodercn/**`（另一插件）
- `pkg/tui/**`、`pkg/turn/**`、`pkg/subscription/**`、`pkg/llm/**`（offers→rows 的通用链路未发现缺陷；`xhigh` 已是宿主认可的努力等级，见 `pkg/llm/catalog.go:106-111` 的 `reasoningEffortLevels`）
- 主树他人未提交改动（`pkg/tui/image_open.go`、`reducer.go`、`render.go` 及其测试、`pkg/architecture`、前端 `settingsSchema.test.ts` 等）
- `plugins/`、`cmd/forebrain/plugins_local_gen.go`、`frontend/src/plugins/local_subscriptions.gen.ts` 均为 gitignore（owner 裁决：闭源不入库），本计划**不做任何 git commit**

**不许变的公共形状**：`subscription.Subscription` 接口、`Subscription.Offer` 字段集、`llm.Model` 转换（`toLLMModel`）、插件 ID/标签/Flows/`Provision`/`RefreshKey` 语义。

---

## 5. Steps（每步都以验证收尾）

### Step 0 — 基线核对（开工前）
```bash
cd /Users/doudou/workspace/unionj-cloud/forebrain-harness
git rev-parse --short HEAD                     # 期望 9f23024a（或 owner 确认的新基线）
git status --porcelain | head                  # 期望只有他人未提交改动，不包含 plugins/
grep -n "productVersion\|User-Agent" plugins/subscriptions/workbuddy/{plugin.go,transport.go}
```
STOP：`plugins/subscriptions/workbuddy/transport.go` 里已不是 §2.1 摘录的形状（内容漂移），报告后停。

### Step 1 — 请求身份常量（`plugin.go`）
按 §3.1 加 `cliVersion`、`deploymentType` 常量与注释。
```bash
gofmt -l plugins/subscriptions/workbuddy   # 期望无输出
CGO_ENABLED=1 go build -tags fts5 ./...    # 期望通过
```

### Step 2 — `disguiseHeaders` 对齐（`transport.go`）
按 §3.1 表格改；保持函数签名与调用点不变。
```bash
CGO_ENABLED=1 go test -tags fts5 ./plugins/subscriptions/workbuddy -run 'TestTransport' -count=1
# 期望：失败仅出现在旧的 UA / X-Product 断言（Step 6 更新），其他 transport 行为绿
```

### Step 3 — 落盘一份真实产品配置（供生成器与比对用）
```bash
python3 - <<'PY'   # 只打印统计，不打印 token
import json,urllib.request,uuid
c=json.load(open("/Users/doudou/.forebrain/workbuddy.json"))
h={"user-agent":"WorkBuddy/5.9.3 CLI/2.165.0","x-product":"SaaS","Accept":"application/json",
   "Authorization":"Bearer "+c["access_token"],"x-request-id":uuid.uuid4().hex}
r=urllib.request.urlopen(urllib.request.Request("https://www.workbuddy.cn/v3/config",headers=h),timeout=30)
open("/tmp/wb_product_config.json","wb").write(r.read())
d=json.load(open("/tmp/wb_product_config.json"))["data"]
print("models",len(d["models"]),"agents",[a["name"] for a in d["agents"]],
      "surface",len(next(a for a in d["agents"] if "model:craft" in a.get("tags",[]))["models"]))
PY
# 期望输出：models 61 agents ['cli'] surface 17
```
（该文件只落在 `/tmp`，不入库；生成器用它。）

### Step 4 — 选取源（`models.go`）
按 §3.2 改；`FetchModels` 里把 `fetchAccountModels` 的返回值直接使用（已收窄），企业合并与 `keepLiveModel` 保持。
```bash
gofmt -l plugins/subscriptions/workbuddy
CGO_ENABLED=1 go build -tags fts5 ./...
```

### Step 5 — 重生成兜底表（`gen_models_static.go` + `models_static.go`）
```bash
cd /Users/doudou/workspace/unionj-cloud/forebrain-harness
go run -tags fts5 plugins/subscriptions/workbuddy/gen_models_static.go /tmp/wb_product_config.json
grep -c 'ID: "' plugins/subscriptions/workbuddy/models_static.go    # 期望 16
grep -n 'hy4free\|space-bunny\|kimi-k3.1-pro\|"auto"' plugins/subscriptions/workbuddy/models_static.go
# 期望：前三者存在，"auto" 不存在
gofmt -l plugins/subscriptions/workbuddy
CGO_ENABLED=1 go build -tags fts5 ./...
```

### Step 6 — 测试更新与新增（`workbuddy_test.go`）
受影响断言（逐条给理由）：
- `TestStaticTableMatchesOfficialProduct`（`:111-145`）：行数 58 → 16；首行断言（`rows[0].ID == "default"`）改为 `hy4free` 且名为 `Hy4`；保留 `glm-5.3-flash` 的 efforts/off 断言，`glm-5.2` 图像断言按新表（新表 `glm-5.2` 图像 **true**）；`deepseek-v4-pro` 窗口 1000000 保留。
- `TestChatModelsKeepTheConversationSubset`（`:147-170`）：会话子集 42 → 16；被过滤样例改为新表里不存在的旧 id（如 `hunyuan-image-alpha` 已不在表内，改为断言「表内每一行都在会话子集」+ 断言 `auto` 不在表内）。
- `TestFetchModelsFallsBackToShippedTable`（`:174-188`）：42 → 16。
- `TestFetchModelsPrefersLiveAndFillsCapabilities`（`:190-241`）：伪造响应加 `agents` 节点（`tags:["model:craft"]`，`models:["glm-5.3-flash","brand-new-model","tool-less","hunyuan-image-alpha"]`），断言仍是 2 条；补一条：目录里存在但 agent 未声明的 id **不**出现。
- `TestFetchModelsMergesEnterpriseList`（`:243-300`）：账户响应加 `agents`（声明 `glm-5.3`、`acct-only`），断言企业与追加语义不变。
- `TestProvisionWritesLoginBackedRows`（`:1123-1197`）：伪响应保持无 `agents`（走「产品没给 surface 清单」的保底分支），offers 仍 2 条；仅窗口/efforts 断言随新表更新（`glm-5.3-flash` 1000000/131072、`[low high max]`/`high`、`canDisableThinking` 新表为 `false` → 断言 `offers[0].ReasoningOff` 为 `*false`）。
- `TestTransport…`（`:890-913`）：UA 期望 `WorkBuddy/5.9.3 CLI/2.165.0`、`X-Product: SaaS`，并新增断言 `X-Agent-Purpose: conversation`、`x-codebuddy-request: 1`、`X-Requested-With: XMLHttpRequest`、`X-Conversation-Message-ID == X-Request-ID`、`X-Conversation-Request-ID == X-Conversation-ID`。

新增回归（放同一测试文件、命名对齐被测生产文件）：
1. `TestDisguiseHeadersWearTheAgentCLIIdentity`：断言 §3.1 表格全部已知值 + `X-Product-Version`/`X-IDE-*` 仍在。
2. `TestFetchModelsOffersTheSurfaceAgentListInOrder`：伪造 `/v3/config` 返回 61-ish 最小集 + `agents:[{name:"cli",tags:["cli","default","model:craft"],models:["auto","hy4free","glm-5.3","kimi-k3.1-pro"]}]` 与目录里多出的 `fast-model`/`wb-lite{disabled:true}`；断言结果 = `[hy4free, glm-5.3, kimi-k3.1-pro]`（顺序保持、`auto` 被隐藏、未声明的目录条目被丢弃、disabled 被丢弃）。
3. `TestFetchModelsFallsBackWhenListingNamesNoAgent`：无 `agents` 节点 → 走兜底表（16 条），`fromStatic=true`。

```bash
CGO_ENABLED=1 go test -tags fts5 ./plugins/subscriptions/workbuddy -count=1 -v 2>&1 | tail -30
# 期望：全部 ok，0 失败
CGO_ENABLED=1 go build -tags fts5 ./...
CGO_ENABLED=1 go vet -tags fts5 ./...
gofmt -l plugins cmd pkg third_party           # 期望无输出
```

### Step 7 — 真实端点比对（只读，无模型调用）
临时测试文件（跑完删除）验证插件自身取到的 offers：
```bash
cat > plugins/subscriptions/workbuddy/zz_real_listing_probe_test.go <<'EOF'
package workbuddy

import "testing"

func TestRealListingProbe(t *testing.T) {
	records, fromStatic := FetchModels(t.Context(), nil, "/Users/doudou/.forebrain")
	t.Logf("fromStatic=%v count=%d", fromStatic, len(records))
	for _, r := range records {
		t.Logf("%-22s %-22s in=%d out=%d efforts=%v", r.ID, r.Name, r.MaxInputTokens, r.MaxOutputTokens, r.Efforts)
	}
}
EOF
CGO_ENABLED=1 go test -tags fts5 ./plugins/subscriptions/workbuddy -run TestRealListingProbe -count=1 -v
rm plugins/subscriptions/workbuddy/zz_real_listing_probe_test.go
```
期望：`fromStatic=false count=16`，顺序与 §3.4 矩阵一致（Hy4、Hy4、Hy3、Hy3、DeepSeek-V4.1-Flash、GLM-5.3、GLM-5.3-Flash、GLM-5.2、Space-Bunny、MiniMax-M3.1-Flash、Kimi-K3.1-Pro、Kimi-K3、Kimi-K2.8-Preview、DeepSeek-V4-Pro、Step-5-Preview、Xiaomi MiMo-V2.6-Pro），无 `auto`。

### Step 8 — 一次最小真实调用（owner 已批准豁免）
用插件自己的 `Transport` 发一次 `hy4free` 请求（证明聊天路径的新身份被网关接受）：
```bash
cat > plugins/subscriptions/workbuddy/zz_real_chat_probe_test.go <<'EOF'
package workbuddy

import (
	"net/http/httptest"
	"strings"
	"testing"
	"io"
)

func TestRealChatProbe(t *testing.T) {
	body := `{"model":"hy4free","max_tokens":8,"stream":false,"messages":[{"role":"user","content":"ping"}]}`
	req := httptest.NewRequest("POST", "http://placeholder/v2/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := (&Transport{Home: "/Users/doudou/.forebrain"}).RoundTrip(req)
	if err != nil { t.Fatalf("round trip: %v", err) }
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	t.Logf("status=%d body=%s", resp.StatusCode, out)
	if resp.StatusCode != 200 { t.Fatalf("status %d", resp.StatusCode) }
}
EOF
CGO_ENABLED=1 go test -tags fts5 ./plugins/subscriptions/workbuddy -run TestRealChatProbe -count=1 -v 2>&1 | tail -20
rm plugins/subscriptions/workbuddy/zz_real_chat_probe_test.go
```
期望：`status=200` 且响应含 choices/增量文本。**只发这一次**。

### Step 9 — tmux 真机验收（隔离 home，禁污染真实 `~/.forebrain`）
```bash
R=/tmp/wb-accept-$$; mkdir -p "$R/home" "$R/proj"; chmod 700 "$R/home"
cp ~/.forebrain/workbuddy.json "$R/home/workbuddy.json"; chmod 600 "$R/home/workbuddy.json"
go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain
tmux new-session -d -s wbacc -x 120 -y 42 \
  "cd $R/proj && FOREBRAIN_HOME=$R/home /Users/doudou/workspace/unionj-cloud/forebrain-harness/build/bin/forebrain; echo EXIT_CODE=\$?; sleep 300"
tmux capture-pane -t wbacc -p | tail -20            # 场景 A：信任页/首屏
tmux send-keys -t wbacc '/connect' Enter
tmux capture-pane -t wbacc -p | tail -25            # 场景 B：选 WorkBuddy
tmux send-keys -t wbacc 'Down' Enter                # 定位到 WorkBuddy 那一项（按屏幕实际位置驱动）
tmux capture-pane -t wbacc -p | tail -30            # 场景 C：模型清单取证
```
验收断言（逐屏 capture 取证）：
- 模型清单出现 `Hy4`（两行，分别 hy4free/hy4std）、`GLM-5.3`、`GLM-5.3-Flash`、`GLM-5.2`、`Space-Bunny`、`MiniMax-M3.1-Flash`、`Kimi-K3.1-Pro`、`Step-5-Preview`；
- **没有** `Auto` 行；
- **没有** `Hy4 preview` / `Kimi-K3-1` 之外的旧别名行（`hy4-preview*`、`kimi-k3-2`、`default`）；
- Esc 退出 → `EXIT_CODE=0`，再启动可恢复（场景 D）；
- 非 TTY：`FOREBRAIN_HOME=$R/home ./build/bin/forebrain gateway start </dev/null` 有诊断、不挂起（场景 E）。
```bash
tmux kill-session -t wbacc; rm -rf "$R"      # 含 token 副本，必须删
```

### Step 10 — 收口
- 回填本计划「验收记录」章节（场景 → 证据：屏幕片段、测试输出、真实调用状态码）。
- `git status --porcelain` 只应显示他人既有改动；`plugins/`、`docs/plan/` 均被 gitignore（`git status --ignored --porcelain plugins docs/plan | head` 可见 `!!`）。**不 commit、不 push、不 merge**。

---

## 6. Done criteria（机器可查）

1. `git diff --stat` 对跟踪文件为空；`git status --ignored --porcelain plugins/subscriptions/workbuddy docs/plan | wc -l` ≥ 6（忽略文件存在）。
2. `grep -c 'ID: "' plugins/subscriptions/workbuddy/models_static.go` = 16；`grep -c '"auto"' plugins/subscriptions/workbuddy/models_static.go` = 0。
3. `grep -n 'CLI/' plugins/subscriptions/workbuddy/transport.go` 命中（UA 由 `productName+"/"+productVersion+" CLI/"+cliVersion` 组成）。
4. `CGO_ENABLED=1 go test -tags fts5 ./plugins/subscriptions/workbuddy -count=1` 全绿。
5. `CGO_ENABLED=1 go build -tags fts5 ./... && CGO_ENABLED=1 go vet -tags fts5 ./...` 通过；`gofmt -l cmd pkg third_party plugins` 无输出。
6. Step 7 探针输出 `fromStatic=false count=16` 且顺序与 §3.4 一致。
7. Step 8 真实调用 `status=200`（一次）。
8. Step 9 四/五场景取证满足断言，临时 home 已删除（`ls /tmp/wb-accept-*` 为空）。

---

## 7. STOP conditions（真实风险，不是客套）

1. **基线漂移**：`transport.go`/`models.go`/`plugin.go` 的摘录内容与 §2/§3 不符（不是行号偏移，是内容不同）→ 停，报告。
2. **UA 决定权被推翻**：Step 3 用 `CLI/2.165.0` 拿到 `models ≠ 61` 或 `agents` 里没有 `model:craft` 的会话 agent → 停，把实际响应统计贴出来（不要改回旧身份继续）。
3. **真实调用失败**：Step 8 返回 4xx/5xx（尤其 400 未知模型、403 身份拒）→ 停，贴状态码与响应体；**不要**自行拆成「列表用 CLI UA、聊天用旧 UA」这种双身份方案，等 owner 裁决。
4. **测试红点非本计划引入**：`pkg/...` 里已有他人未提交改动（`pkg/tui`、`pkg/architecture`、前端），若 `go test ./...` 失败在那些文件 → 只报告，不改。
5. **需要 to 超出 §4 In scope 的文件**（例如要改 `pkg/subscription`/`pkg/tui`）→ 停，先问。
6. **企业列表语义被破坏**（`mergeEnterpriseModels` 的覆盖/追加断言红）→ 停，回看 §3.2 写回。

---

## 8. Maintenance notes

- **产品身份是版本绑定的**：`productVersion = 5.9.3` 与 `cliVersion = 2.165.0` 是「客户端身份」，随 WorkBuddy 发版更新。升级时只改这两个常量；`CLI/` 段存在与否决定网关返回哪一代产品配置（实测版本号不参与判定）。
- **新模型上线不需要改代码**：列表来自线上 `data.agents` 的会话清单，产品加模型即生效；只有兜底表（`models_static.go`）需要按 §5 Step 5 重生成（它决定离线/失败路径的清单）。重生成命令写进本计划，后续照做即可。
- **`auto` 被刻意隐藏**（owner 2026-11-15）：若以后要恢复，只去掉 `routerModelID` 的跳过与兜底表里的一行。
- **审查重点**：`disguiseHeaders` 里每个头都能在 §3.1 表里找到客户端证据；新增头必须同样是客户端真实发送的头（客户端不发的头不要加——「严格伪装」既禁止降级为裸请求，也禁止自创头）。
- **后续可做（不在本计划）**：`modelPromotions`/`modelTiers` 的角标（限时免费/夜间折扣/订阅优先）与 `credits` 倍率目前不进入 `subscription.Offer`；若要 TUI 显示倍率，需要在宿主 `Offer` 上扩一个字段——属于产品需求，另行计划。

---

## 9. 验收记录（2026-11-15 回填）

| 场景 | 证据 | 结果 |
| :--- | :--- | :--- |
| Step 3 真实配置（只读 GET） | `models 61 agents ['cli'] surface 17` | ✅ 与 §3.4 一致 |
| Step 5 生成器 | `wrote plugins/subscriptions/workbuddy/models_static.go: 16 models`；`grep -c 'ID: "'` = 16；表头 `WorkBuddy product config (WorkBuddy 5.9.3, CLI 2.165.0, 2026-11-15)` | ✅ |
| Step 6 包测试 | `go test -tags fts5 ./plugins/subscriptions/workbuddy -count=3` → `ok`（新增 `TestDisguiseHeadersWearTheAgentCLIIdentity`、`TestFetchModelsOffersTheSurfaceAgentListInOrder`、`TestFetchModelsKeepsTheCatalogWhenTheListingNamesNoAgent`） | ✅ 0 失败 |
| Step 6 全仓 | `go build -tags fts5 ./...` exit 0；`go vet -tags fts5 ./...` exit 0；`gofmt -l plugins cmd pkg third_party` 无输出 | ✅ |
| Step 7 真实列表探针（插件自身 FetchModels，真实端点） | `fromStatic=false count=16`，顺序 hy4free/Hy4、hy4std/Hy4、hy3、hy3-x、deepseek-v4.1-flash、glm-5.3、glm-5.3-flash、glm-5.2、space-bunny、minimax-m3.1-flash、kimi-k3.1-pro、kimi-k3-1、kimi-k2.8-preview、deepseek-v4-pro、step-5-preview、mimo-v2.6-pro | ✅ 与 §3.4 逐行一致，无 auto |
| Step 8 最小真实调用（hy4free） | 第 1 次（计划原文 `stream:false`）：`status=400 {"code":11101,"msg":"Non-stream chat request is currently not supported"}` → 按 §7.3 停；owner 批准后再发一次 `stream:true`（`Accept: text/event-stream`）：`status=200`，3572 字节 SSE，`chat.completion.chunk` ×9，`model:"hy4-free"`，`finish_reason:"length"`，`usage{prompt 22, completion 8, reasoning 8}`，`credit:0 / original_credit:0.01 / quota.model:"hy4"` | ✅ 通过（共 2 次；被拒那次无 completion、未计费） |
| Step 9-A/B 首启（隔离 home `/tmp/wb-accept`） | Step 1 of 5 列表：`❯ WorkBuddy  Browser authorization; use your WorkBuddy plan quota · Signed in` | ✅ 登录被识别 |
| Step 9-C 模型清单（向导 Step 2） | 16 行全部可见：`Hy4 / Hy4 / Hy3 / Hy3 / DeepSeek-V4.1-Flash / GLM-5.3 / GLM-5.3-Flash / GLM-5.2 / Space-Bunny / MiniMax-M3.1-Flash / Kimi-K3.1-Pro / Kimi-K3 / Kimi-K2.8-Preview / DeepSeek-V4-Pro / Step-5-Preview / Xiaomi MiMo-V2.6-Pro` | ✅ 有 Hy4、GLM-5.3、Space-Bunny、Kimi-K3.1-Pro、Step-5-Preview；**无 Auto** |
| Step 9-C `/connect` 路径（完成向导后） | `/connect` → WorkBuddy → 同一份 16 行（可见 11 行 + `↓ 5 more`）；过滤 `mimo` → `Xiaomi MiMo-V2.6-Pro`，过滤 `step` → `Step-5-Preview` | ✅ 两个入口同一清单 |
| Step 9-D 退出/重启 | 首启 Esc 退出 → `EXIT_CODE=0`；重启直达 composer，footer `workbuddy/Hy4 · high 100%/960k`；Ctrl+C 一次提示 `Press Ctrl-C again to exit`，二次 → `EXIT_CODE=0` | ✅ 可恢复 |
| Step 9-E 非 TTY | `FOREBRAIN_HOME=$R/home ./build/bin/forebrain gateway start </dev/null` → 打印 banner + `Listening on http://127.0.0.1:6060`，不等待 TTY 输入（8s 后手工 kill，端口已释放） | ✅ 有诊断、不挂起 |
| Step 10 清理 | `tmux kill-session -t wbacc`；`rm -rf /tmp/wb-accept`（含 token 副本）；`ls -d /tmp/wb-accept*` 为空 | ✅ |
| Step 10 工作树 | `git status --porcelain` 与 Step 0 基线逐行相同（仅他人 `pkg/tui`、`pkg/run`、`pkg/architecture` 改动）；未 commit/push/merge | ✅ |

## Implementation Status

- Step 0–10 全部完成。Step 8 第 1 次按计划用 `stream:false` 被网关拒为 400 `Non-stream chat request is currently not supported`（非流式），按 §7.3 停止并报 owner；获准补发一次 `stream:true` → `status=200`、SSE `chat.completion.chunk`、`model:"hy4-free"`、usage/credit 正常，新身份 + 新模型 id 在聊天路径被接受。
- 落地：`plugin.go`（`cliVersion`/`deploymentType`）、`transport.go`（客户端头集合 + `CLI/<version>` UA）、`models.go`（`wireModel.Disabled`、`wireAgent`、`narrowToSurface`、`surfaceAgent`、`fetchAccountModels` 收窄）、`models_static.go`（16 行，重生成）、`gen_models_static.go`（新生成器）、`workbuddy_test.go`（8 处断言更新 + 3 个回归）。
- 偏差：
  1. `TestFetchModelsPrefersLiveAndFillsCapabilities` 的 `hunyuan-image-alpha` 只留在目录、不写进 agent 清单（若声明它，按 §3.2 会保留 → 3 条，与计划「2 条」冲突）；断言「2 条 + 未声明 id 不出现」。
  2. 计划新增 #3 的期望（无 agent → `fromStatic=true`）与 §3.2 冲突；按 §3.2 实现，测试为 `TestFetchModelsKeepsTheCatalogWhenTheListingNamesNoAgent`（`fromStatic=false`，目录原样）。
  3. `mimo-v2.6-pro` 线上是 `reasoning.effort="high"`，按 §3.3 映射规则生成 `Efforts: ["high"]`（计划文字「无 efforts」指无 supportedEfforts 集合）。
  4. Done criteria #1 的忽略文件计数用 `--untracked-files=all` 才展开（默认把 `docs/plan/`、`plugins/` 折叠成 2 行）。
