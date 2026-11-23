# WORKBUDDY 订阅登录插件移植计划（对齐 Qoder CN 插件机制）

状态：待 owner 批准
基线：`git rev-parse HEAD` = `3506aea8`（工作区另有并行会话 WIP，与本次范围不重叠）
计划编写日期：2026-11-15
落盘位置说明：本文件写在 plan-mode 的会话计划目录；批准实施时按本仓惯例另存一份到
`docs/plan/WORKBUDDY_SUBSCRIPTION_PLAN.md`（该路径已 gitignore，不入库）。

---

## 1. 目标（Why this matters）

WorkBuddy（腾讯 CodeBuddy 家族的桌面 Agent，本机已安装 5.9.3 且已登录）是一个「订阅制」模型入口：
账号登录后即可用其订阅额度调用它自家的模型网关。forebrain harness 已有**第三方客户端订阅登录插件机制**
（2026-11-12 owner 三条裁决 + plan 302 落地，dev `4479d6c9`）：闭源插件放 `plugins/subscriptions/<id>/`，
由生成脚本桥接进二进制与前端，TUI `/connect` 与 Web「设置 › 模型」自动出现该订阅。

本计划把 WorkBuddy 的**订阅登录 + 用登录凭证发 LLM 请求**这两段语义完整移植为**第二个**插件
`plugins/subscriptions/workbuddy/`，与 `qodercn` 插件同构：TUI 与 Web 两个 surface 都有入口，行为一致，
宿主树（`pkg/`、`cmd/`、`frontend/src/` 非生成文件）零改动。

**重要结论（决定了工作量）**：WorkBuddy 的模型调用是**标准 OpenAI Chat Completions 协议**
（`POST {endpoint}/v2/chat/completions`，Bearer token + 一组产品身份头，SSE 流），不是 qodercn 那种自研加密信封协议。
因此本插件不需要 `crypto.go`（约 344 行）与 SSE 解包器（约 300 行），核心是「登录 + 一条伪装 transport + 模型目录」。

---

## 2. 已证事实（逆向 + 实测，全部可复现）

### 2.1 宿主侧插件契约（已存在，不需要新建）

| 关注点 | 位置 | 说明 |
|---|---|---|
| 订阅注册表 | `pkg/subscription/registry.go:30`（`Subscription` 接口）、`:55`（`Login`）、`:65`（`Offer`）、`:123`（`Env`）、`:196`（`Register`）、`:396`（`ProvisionRows`） | 插件通过 `subscription.Register()` 进入 `Registry()`，两个 surface 只认这个接口 |
| flow 常量 | `pkg/subscription/registry.go:21-26` | `FlowBrowserCallback` / `FlowPasteCallback` / `FlowDeviceCode` |
| provider 注册 | `pkg/config/provider_registry.go:275` `RegisterProviderPlugin(BuiltinProvider)` | `LoginBacked: true` + `Endpoints[]` 让模型页出现「用 WorkBuddy 登录」 |
| LLM 工厂 | `pkg/llm/llm.go:560` `RegisterProviderFactory(provider string, factory ProviderLLMFactory)` | 模型行 `provider=workbuddy` 时按工厂构造客户端 |
| 静态能力行 | `pkg/llm/catalog.go:771` `SetBuiltinModels(provider string, models []Model)` | 登录前的静态能力（上下文窗口、推理档位）；只吃 `[]llm.Model`，所以要 `toLLMModel` 转换 |
| OpenAI 兼容客户端 | `pkg/llm/openai/compat_llm.go:68` `NewCompatLLMWithTransport(apiKey, baseURL, model string, temperature float32, maxTokens int, apiPath string, paramsJSON []byte, extra http.RoundTripper)` | `extra` 就是伪装 transport 的挂载点 |
| 桥接生成 | `scripts/gen-subscription-plugins.sh` | 扫 `plugins/subscriptions/*/plugin.go` → `cmd/forebrain/plugins_local_gen.go`；扫 `*/frontend/index.ts` → `frontend/src/plugins/local_subscriptions.gen.ts` |
| 前端插件契约 | `frontend/src/plugins/subscriptionPlugin.ts:5` | `{id, describe?, waitingHint?, hideEmptyUserCode?, customStepComponent?}`；`hideEmptyUserCode` 注释即 “browser-confirm device flow” |
| TUI 入口 | `/connect` 派发在 `pkg/tui/run.go:1324`（`case "connect":`），实现在 `pkg/tui/setup.go:2218`（`handleConnect`）；flow 选择与 device_code 驱动在 `pkg/tui/setup.go:1244-1310` | 同时 `commands.go` 的斜杠命令表里登记（该表已有 `connect`，无需改） |
| Web 入口 | `pkg/gateway/api_extra.go:432-434`（`GET /api/subscriptions`、`/offers`、`POST /provision`）+ `frontend/src/components/models/SubscriptionLoginModal.vue` | 「设置 › 模型」 |

闭源隔离现状（实测）：`git check-ignore -v plugins/subscriptions/qodercn/plugin.go` →
`.gitignore:80 /plugins/`；`.gitignore:76 docs/plan/`、`:77 plans/`、`:78 .workbuddy/`。
参考实现 `plugins/subscriptions/qodercn/`（4487 行，gitignored，本机在盘）是本计划的结构模板。

### 2.2 WorkBuddy 客户端（本机安装物）

- 应用：`/Applications/WorkBuddy.app`，v5.9.3，bundle id `com.tencent.workbuddy.mac`，Electron；
  内核是腾讯 `@genie/agent-harness` + CodeBuddy CLI。
- **产品权威配置**：`/Applications/WorkBuddy.app/Contents/Resources/app.asar.unpacked/cli/product.json`（385 KB，明文 JSON）
  关键字段（原文）：
  ```json
  "endpoint": "https://www.workbuddy.cn",
  "stagingEndpoint": "https://staging.workbuddy.cn",
  "officialEndpoints": ["https://www.workbuddy.cn", "https://staging.workbuddy.cn", "https://wb.tencentbuddy.com", "https://copilot.tencent.com", "https://staging-copilot.tencent.com", "https://www.codebuddy.ai", "https://staging-codebuddy.tencent.com"],
  "authentication": {
    "id": "workbuddy-desktop",
    "type": "account-scoped-external-link",
    "label": "TencentCloud",
    "attributes": {
      "platform": "workbuddy",
      "prefixPath": "/plugin",
      "tokenHeader": "Authorization", "tokenType": "bearerToken",
      "usernameHeader": "X-User-Id", "usernameEncode": "URLEncode"
    }
  },
  "models": [ /* 58 行，见 2.5 */ ],
  "dataFolderName": ".workbuddy",
  "genieVersion": "5.9.3"
  ```
- 客户端实现：`.../app.asar.unpacked/cli/dist/codebuddy-lite-wb.mjs`（9.8 MB，rolldown 压缩）；
  其中的 `AccountScopedExternalLinkAuthenticationProvider` 与 `ModelProvider` 是本节所有协议事实的来源。
- 桌面端凭据文件（只读对照，本计划不解析）：`~/Library/Application Support/CodeBuddyExtension/Data/Public/auth/workbuddy-desktop.info`，
  顶层 `{auth, account, accounts, allAccounts}`；`account.uid/uin/type`（本机 `type:"personal"`、无 `enterpriseId`、`sso.domain` 空），
  `auth.accessToken` / `auth.refreshToken` 形如 `{"$wbEncrypted":1,"envelope":"<base64>"}`（加密），`auth.expiresIn/refreshExpiresIn/tokenType/sessionState/domain` 明文。

### 2.3 登录协议（account-scoped external link）

来源：`codebuddy-lite-wb.mjs` 内 `AccountScopedExternalLinkAuthenticationProvider`
（`createSession` / `openAuthUrl` / `fetchAuthState` / `loopGetToken` / `loopGetCurrentAccount` / `refreshSession` / `switchAccount`）。

1. **申请登录态**
   `POST {endpoint}/v2/plugin/auth/state?platform=workbuddy`，body `{}`，头：
   `X-No-Authorization: true`、`X-No-User-Id: true`、`X-No-Enterprise-Id: true`、`X-No-Department-Info: true`
   （官方客户端在会话内还会带 `X-Domain: <endpoint host>`，实测不带也成功）。
   响应：`{"code":0,"msg":"OK","requestId":"…","data":{"state":"<uuid>","authUrl":"https://www.workbuddy.cn/login?platform=workbuddy&state=<uuid>"}}`
2. **打开浏览器**：官方客户端把 `authUrl` 交给系统浏览器，并追加 `version=<客户端版本>`；
   `platform=workbuddy` 时再追加 `ww_device_nonce=<本地设备 nonce>`（来源未逆向完，见 §9 STOP）。
3. **轮询取 token**：`GET {endpoint}/v2/plugin/auth/token?state={state}`，间隔 1000 ms，上限 300 s。
   - 未完成：HTTP 200 + `{"code":11217,"msg":"login ing..."}`（`LOGIN_TOKEN_PENDING_CODE`）→ 继续轮询。
   - 完成：`data` = token 载荷，字段（与桌面端凭据文件同名字段互为佐证）：
     `accessToken`、`refreshToken`、`expiresIn`、`refreshExpiresIn`、`tokenType`、`sessionState`、`domain`、`lastRefreshTime`。
4. **取账号**：`GET {endpoint}/v2/plugin/account`，头 `Authorization: Bearer <accessToken>` →
   account（`uid`、`uin`、`type`、`nickname`、`enterpriseId`、`idSource` …）；`X-User-Id` 头取 `uid`（URL 编码）。
5. **刷新**：`POST {endpoint}/v2/plugin/auth/token/refresh`，body `{}`，头
   `X-Refresh-Token: <refreshToken>`、`X-Auth-Refresh-Source: plugin`、`X-Domain: <domain 或 endpoint host>`；
   401/403 视为登录失效。
6. 辅助（本期不实现，记录备查）：`GET /v2/plugin/accounts`、`POST /v2/plugin/account/switch`、
   `POST /v2/plugin/auth/logout`、`GET /v2/plugin/account`。

### 2.4 模型调用协议（标准 OpenAI Chat Completions）

来源：`codebuddy-lite-wb.mjs` 的 `resolveModelBaseURL` / `getCustomHeaders` / `runIdentityHeaders` / `shouldApplyInternalModelRequestHeaders`。

- **URL**：`resolveModelBaseURL()` 在模型没有自定义 `url` 时返回产品 `endpoint`，OpenAI SDK 再拼 `/chat/completions`
  （函数体原文：`return \`${E}/v2\``，`E` = product endpoint）→ 最终 **`POST https://www.workbuddy.cn/v2/chat/completions`**。
- **必带头**（伪装要求，owner 裁决「严格伪装成官方客户端」；标 †（推断）的先按此发，Step 4b 的真机 spike 用真实请求核对）：
  ```
  Authorization: Bearer <accessToken>                                    # 已证：runIdentityHeaders/ModelProvider
  X-Product: WorkBuddy †                  # product.json productName + attributes.usernameHeader
  X-Product-Version: 5.9.3 †              # product.json genieVersion
  X-IDE-Type: WorkBuddy † / X-IDE-Name: WorkBuddy † / X-IDE-Version: 5.9.3 †
  X-Agent-Intent: craft                   # 已证：ModelProvider 写死 "craft"
  X-Agent-Runtime: local-agent            # 已证：AGENT_RUNTIMES 之一
  X-User-Id: <account.uid>                # 已证：product.json usernameHeader + URLEncode
  X-Enterprise-Id: <account.enterpriseId> # 企业账号才有
  X-Auth-Method                            # 取**本插件**的登录方式常量 "browser"（桌面凭据文件里没有 method 字段，别去那里找）；
                                          # Step 4b 的 (c) 档顺带核对网关是否接受这个值
  X-Userinfo: base64(JSON {"uin":uid,"owner_uin":enterpriseId,"id_source":…,"token_source":auth.method})
                                          # 条件头：仅当 uid 非空且（enterpriseId 或 idSource 或 auth.method 至少一个有值）才发；
                                          # 个人账号三项皆空时官方**根本不发**，不要无条件加
  X-Private-Data: "true"                  # 已证：模型优化开启时 "false"，否则 "true"
  X-Model-ID: <model id>                  # 已证：MODEL_ID_HEADER
  X-Request-ID / X-Conversation-ID: 每次请求一个 uuid
  X-Domain: <凭证 domain，缺则 endpoint host>
  User-Agent: WorkBuddy/5.9.3 †            # bundle 里没有 WorkBuddy/ 字面，UA 由 SDK/identity 层拼
  ```
- **请求体**：标准 Chat Completions（`model`、`messages`、`tools`、`stream: true`、`max_tokens`、`temperature`、
  `reasoning_effort`，以及开启 thinking 摘要时的 `reasoning_summary: "auto"`）。
- **流式**：OpenAI SSE（`data: {…}` 分片、末尾 `data: [DONE]`）。

### 2.5 模型清单：有实时接口，静态表只是回落（回答 owner 的提问）

官方客户端 **两处并用**：
1. **内置表**：`product.json` 的 `models[]`（58 行），字段
   `id`、`name`、`maxInputTokens`、`maxOutputTokens`、`supportsToolCall`、`supportsImages`、
   `reasoning{effort | supportedEfforts | defaultEffort | canDisableThinking | summary}`、`isDefault`、`credits`、`vendor`、`tags` …
   这是随客户端发布的**兜底 + 能力数据（窗口/档位/图片）**。
2. **实时清单**：`GET {endpoint}/v3/config`（Bearer 凭证）→ `data` 是产品配置对象，模型在 **`data.models`**。
   官方按顺序合并：先 `CloudProductProvider`（`/v3/config`，priority CLOUD）再 `ModelsProductProvider`
   （**`GET {endpoint}/console/enterprises/{enterpriseId}/config/models`**，priority MODELS、SmartMerge；
   `enterpriseId` 取 `account.enterpriseId`；实测无凭证 401）。
   **两个端点响应形状不同**：`/v3/config` → `{"data":{"models":[…]}}`；企业清单 → **`{"data":{…}, "data":[…]}` 即数组直接挂在 body 的 `data` 上**，
   按 `data.data` 取数组（官方 `ModelsProductProvider.provide()` 读的就是 `data.data`，且 `normalizeRemoteModels` 要求入参本身是数组，见
   `lib/product/authority.js` 的 `Te` 常量与 `Ye`）。
   合并实现在 harness lib（`lib/components/product.mjs` 导出的 `mergeCloudProduct` / `mergeEnterpriseModels`），
   不在 `cli/dist` 的压缩 bundle 里——重取证时别去 CLI bundle 里 grep 这两个名字。
   owner 裁决（2026-11-15）：企业清单**本期一并移植**。

因此本插件的 `Offers()` 顺序是 **live 优先、静态回落**（与 qodercn 一致：`FetchModels` 失败才用 `staticQoderModels()`）：
- live 枚举顺序：`/v3/config` → （`account.enterpriseId` 非空时）企业清单 → 静态表补能力。
  企业清单失败**不**让整个列表失败：记日志、跳过，个人清单照出（企业端点对一个非企业账号本来就是 401/403）。
- 有 live 结果 → 用它的 id/名称/窗口/档位；静态表补齐 live 缺的能力字段（窗口、图片、档位）。
- live 为空/失败/未登录 → 全部用静态表，并标 `FromStaticList`，让模型页提示「以官方控制台为准」。
- 两者都只保留**可对话子集**：`supportsToolCall == true` 且 `maxInputTokens > 0` 且 `id` 不匹配
  `image|video|kling|hunyuan-image|codewise|nes|jump|rewrite|completion-gf|completions`。
  **实测该规则从 58 行中留下 42 行**（已用 product.json 实算，见 §6 Step 3 断言）。
  其中 `default`、`auto`、`default-1.1`、`default-1.2` 是网关侧解析的别名（显示名取静态表的 `Claude-3.7-Sonnet` 等），
  照旧保留并把显示名带进 `Offer.Name`。

### 2.6 实测取证（本机 curl，可复现）

```
$ curl -s https://www.workbuddy.cn/ -o /dev/null -w '%{http_code}\n'
200
$ curl -s 'https://www.workbuddy.cn/v2/plugin/auth/token?state=probe0000'
{"code":11217,"msg":"login ing...","requestId":"195c4e14-…"}
$ curl -s -X POST 'https://www.workbuddy.cn/v2/plugin/auth/state?platform=workbuddy' \
    -H 'Content-Type: application/json' -H 'X-No-Authorization: true' -H 'X-No-User-Id: true' \
    -H 'X-No-Enterprise-Id: true' -H 'X-No-Department-Info: true' -d '{}'
{"code":0,"msg":"OK","requestId":"edc0d4a1-…","data":{"state":"c1fdeead-…","authUrl":"https://www.workbuddy.cn/login?platform=workbuddy&state=c1fdeead-…"}}
$ curl -s -X POST 'https://www.workbuddy.cn/v2/chat/completions' -H 'Content-Type: application/json' \
    -H 'X-Product: WorkBuddy' -d '{"model":"glm-5.0","messages":[{"role":"user","content":"hi"}]}'
<html><head><title>401 Authorization Required</title></head>… （APISIX 网关，无 token 时 401）
$ curl -s https://www.workbuddy.cn/v3/config
{"code":0,"msg":"OK","requestId":"b4f09755-…","data":{"agent":{"agents":null},"models":null,…}}
```
结论：四个端点在盘、语义如 §2.3/§2.4/§2.5；匿名访问 `/v3/config` 返回 `models: null`（需 Bearer）。

---

## 3. 设计

### 3.1 新增文件（全部在 `plugins/subscriptions/workbuddy/`，gitignored）

| 文件 | 职责 | 参照 |
|---|---|---|
| `plugin.go` | `init()`：`installBuiltinModels(staticWorkBuddyModels())`、`config.RegisterProviderPlugin(...)`、`llm.RegisterProviderFactory("workbuddy", newWorkBuddyLLM)`、`subscription.Register(workBuddySubscription{})`；`workBuddySubscription` 七个方法；`workBuddyLogin` handle（`mu/url/err/urlCh/cancel/done/once`） | `qodercn/plugin.go:22-58`（注册）、`:63-93`（Subscription 方法）、**`:105-175`（Login handle，必须照抄）**、`:176-213`（Offers/Provision 手工构造 `subscription.Offer`） |
| `auth.go` | `Credentials` 结构、DB+文件双写存储、`LoginExternalLink`（state→轮询→account）、`EnsureFreshCredentials`、`refreshCredentials`、`isUnauthorized` | `qodercn/auth.go:43-121,507-768` |
| `transport.go` | `Transport{Home string}` 实现 `http.RoundTripper`：注入 §2.4 头、空 Authorization 时补 Bearer、401 时刷新一次并重试 | `qodercn/transport.go:44-160` |
| `models.go` | `workBuddyModel` 结构 + `FetchModels`（`/v3/config` → 企业清单 → 静态表，见 §2.5/§3.5）+ 可对话过滤 + `toLLMModel` + `installBuiltinModels`（只做 `llm.SetBuiltinModels`，**没有**输入预算 map） | `qodercn/models.go:1-120,234,392-441` |
| `models_static.go` | 由 `product.json` 生成的静态表（一次性脚本，见附录 A），**生成物入库无意义（插件本身闭源）**，但必须与 5.9.3 一致 | — |
| `frontend/index.ts` | `export default { id: 'workbuddy', describe, waitingHint, hideEmptyUserCode: true }` | `qodercn/frontend/index.ts`（16 行） |
| `workbuddy_test.go` | httptest 假网关覆盖全流程（§7） | `qodercn/qodercn_test.go` 的 httptest 手法 |

### 3.2 `Subscription` 契约 → WorkBuddy 端点映射

| 方法 | 实现 |
|---|---|
| `ID()` | `"workbuddy"`（模型行 `provider=workbuddy`，`source="workbuddy"`） |
| `Label()` | `("WorkBuddy", "WorkBuddy")` |
| `Flows()` | `[]string{subscription.FlowBrowserCallback, subscription.FlowDeviceCode}`。**两个都要声明**：flow 是 surface 词汇，不是协议分类。gateway 一侧 `authLoginFlow`（`pkg/gateway/login_api.go:209-215`）主动跳过 `browser_callback`、退到 `device_code`，而 Web 的等待文案由前端插件的 `waitingHint`/`hideEmptyUserCode` 覆盖；TUI 一侧任何 flow 都在拿到 URL 后自己开浏览器（`pkg/tui/setup.go:1166-1175`），但 `FlowDeviceCode` 的文案是「shows a code to confirm in the browser」/「Waiting for you to confirm the code…」（`pkg/tui/setup.go:1282-1295`），本插件永远没有 code。声明 `browser_callback` 在前，桌面 TUI 走对的文案，远端 web 仍退到 device_code，与 `qodercn/plugin.go:76-78` 完全一致 |
| `Probe()` | 无凭据 → `turn.SignInMissing`；刷新 401/403 → `turn.SignInDead`；网络失败 → `turn.SignInUnreachable`；能刷新 → `turn.SignInUsable`（照 `qodercn/plugin.go:74-93`） |
| `Start()` | 宿主助手 `startLogin/setURL/setUserCode`（`pkg/subscription/registry.go:265/285/292`）**未导出，插件调不到**：照 `qodercn/plugin.go:105-175` 自己写一个 `workBuddyLogin` handle —— `AuthorizeURL()` 在 `state` 到手后立刻可读、`UserCode()` 恒空、`Submit()` 返回「此登录在浏览器确认，无回调可粘贴」、`Wait(ctx)` 在 ctx 取消时 `Cancel()` 并返回 `ctx.Err()` |
| `Offers()` | §2.5 三级清单（`/v3/config` → 企业清单 → 静态能力表）→ 过滤可对话 → `[]subscription.Offer`：`BaseURL:"https://www.workbuddy.cn/v2"`、`ContextWindow = MaxInputTokens`、`SupportsImages`、**`ImagesUndeclared: !known`**（live 独有 id 或静态表没写模态时必须置 true，否则会替供应商回答「不支持图片」——见 `pkg/subscription/registry.go:79-83` 字段注释与 `offerRowInput` 的 `if !offer.ImagesUndeclared` 分支，`pkg/subscription/registry.go:432`）、`Efforts/DefaultEffort/ReasoningOff`、`Default`；插件包内不能用 `pkg/subscription` 的未导出助手，必须像 `qodercn/plugin.go:176-200` 那样手工构造，`SourcePlan` 留空（单一 plan） |
| `Provision()` | `subscription.ProvisionRows(ctx, env, offers, "workbuddy")` |
| `RefreshKey()` | `subscription.ErrNoKeyToRefresh`（登录即凭证） |

### 3.3 凭据存储（与 qodercn 同一模式）

- DB：`state.ConfigStore{DB}.LoadCredential/SaveCredential(ctx, "auth.workbuddy", raw)`（`qodercn/auth.go:68-121` 的用法）。
- 文件回落：`$FOREBRAIN_HOME/workbuddy.json`，`MkdirAll(home, 0o700)` + `WriteFile(..., 0o600)`。
  原因与 qodercn 相同：transport 在运行期没有 state handle，只能写文件；两处都写，读时取 `ExpiresAt` 较新的一份。
- **刷新必须单飞**：`lockPath := credentialPath(home)`（即 `workbuddy.json` 路径），
  `llm.CredentialMutex(lockPath).Lock()` → **在锁内重新 `loadCredentials`** → 判断是否仍需刷新 → 写回 →
  `Unlock()`（`defer`）。这是 `qodercn/auth.go:600-615` 的同一模式（`pkg/llm/credential_lock.go:17` 按绝对路径给同一进程内的互斥锁），
  保证 TUI 与同进程 handler 并发时只刷新一次。
- 写文件用原子替换（同目录 `workbuddy.json.tmp` + `os.Rename`），避免读到半个 JSON。
  已知限制（与 qodercn 相同，本期不扩大）：`CredentialMutex` 是**进程内**锁，forebrain TUI 与 gateway 两个进程同时刷新仍可能撞车；
  这一点写进 §10 遗留项。
- 结构体（JSON 字段，全部 snake_case 与仓库一致）：
  ```go
  type Credentials struct {
      UID, Name, EnterpriseID, IDSource, AuthMethod, Domain string
      AccessToken, RefreshToken string
      ExpiresAt, RefreshExpiresAt time.Time
      SessionState string
      LoginMethod string // "browser"
  }
  ```
- 密钥/D-Bus 之类的系统钥匙串**不使用**：本期自建登录，自己持有 token（owner 已裁决「自有浏览器登录」）。

### 3.4 Transport 伪装（对齐 owner「严格伪装成官方客户端」裁决）

**接入方式（唯一硬要求 + 两条说明）**：
- **必须**实现 `func (t *Transport) SetBase(base http.RoundTripper)`：`NewCompatLLMWithTransport` 只在 `extra` 有 `SetBase`
  时把它套在客户端包装链**最外层**（`pkg/llm/openai/compat_llm.go:100` → `roundTripperWithBase`），
  否则 reasoning 归一化/用量统计包装会被丢掉（`qodercn/transport.go:44` 同样理由）。
- `OwnsUserAgent() bool` **不是本路径的要求**：唯一消费者是 Responses 链（`pkg/llm/openai/responses_llm.go:262`），
  compat 客户端不查它。UA 的真实机制是 transport 在 `RoundTrip` 里覆写请求头（它在最外层，内层设的 UA 都看得见）。
  为与 qodercn 对齐可以顺带实现，但不要把它写进验收断言。
- **可注入点**（测试与 spike 都靠它）：包级变量 `workBuddyBaseURL = "https://www.workbuddy.cn"`、
  `loginPollInterval = time.Second`、`loginTimeout = 300 * time.Second`；所有端点、轮询间隔、超时都读它们，
  测试改成 `httptest.Server.URL` / 50 ms / 500 ms 并在 `t.Cleanup` 还原（不需要 `t.Setenv`）。

`Transport.RoundTrip` 的每个请求：
1. `loadCredentials`（文件优先，DB 不在手）+ `EnsureFreshCredentials`（过期前 60 s 刷新，单飞见 §3.3）。
2. 目标 host **恒为 `workBuddyBaseURL` 的 host**，路径固定 `/v2/chat/completions`（改写调用方传入的 base，语义同 `qodercn/transport.go:59-107`）。
   凭证里的 `domain` **只**用于 `X-Domain` 头，不参与路由：官方 `resolveModelBaseURL` 的 host 也恒取产品 endpoint
   （`return \`${E}/v2\``），官方反而把**登录页**对齐到 API origin（`alignLoginPageUrl(authUrl, resolveAuthApiOrigin(...))`）。
   别从「本机这个个人账号 `auth.domain == www.workbuddy.cn`」推广成路由规则——企业/SSO 账号的 domain 可能是
   `*.sso.copilot.tencent.com` 这类（product.json 的 `iOADomain`/`officialEndpoints` 里就有），按 domain 打会把模型流量打到登录域名。
3. 覆写 §2.4 头表（含 `User-Agent: WorkBuddy/5.9.3`，见上面的 `OwnsUserAgent`）。
4. 401 → `ForceRefreshCredentials` 一次 → 重试一次；再 401 原样返回（让上层显示「登录失效」）。
5. 关闭重定向跟随以外的任何降级：不降级为裸请求（裁决①）。

### 3.5 模型目录

`workBuddyModel`（**扁平字段，与附录 A 的生成物逐字段对应**）：

```go
type workBuddyModel struct {
    ID, Name                                       string
    MaxInputTokens, MaxOutputTokens                int64
    SupportsToolCall, SupportsImages, IsDefault    bool
    Efforts                                        []string // reasoning.supportedEfforts，缺则 []string{reasoning.effort}
    DefaultEffort                                  string   // reasoning.defaultEffort
    CanDisableThinking                             *bool    // 原样：true→&true, false→&false, 未给→nil
}
```

`llm.SetBuiltinModels(provider string, models []llm.Model)`（`pkg/llm/catalog.go:771`）只吃 `[]llm.Model`，
所以必须写一个 `toLLMModel(m workBuddyModel) llm.Model` 做转换（照 `qodercn/models.go:234` 的同名函数：
`ID/Name/APIModel/ContextWindow/DefaultMaxTokens/CanReason/SupportsAttachments/ReasoningOptions`），
不能把 `workBuddyModel` 直接传给 `SetBuiltinModels`。
- `FetchModels(ctx, db, home)`：先 `EnsureFreshCredentials`，再按 §2.5 的三级顺序拉取，**两个解析器分开写**：
  1. `GET {endpoint}/v3/config`（Bearer + 身份头）→ 取 **`data.models`**（`[]model`；也容忍 `{"model":{…}}` 包裹形态）；
  2. `account.enterpriseId != ""` 时 `GET {endpoint}/console/enterprises/{enterpriseId}/config/models`（Bearer）→
     取 **`data.data`**（`[]model`，与个人清单形状不同），按 id 覆盖/追加在 1 之上；**失败只跳过**（401/403 正常，非企业账号）；
  3. 静态表补齐能力字段。
  **口径**：live 决定「有哪些模型」（枚举权威），静态表决定「每个模型的能力」（窗口/图片/档位权威，live 没给的字段从静态表补）；
  live 失败或为空 → 静态表 + `FromStaticList`。
- **live 独有 id（静态表没有）一律保留**：它是账号确实能用的模型，丢弃等于少给用户可用模型；
  能力未知时 `ContextWindow=0`（交给 catalog 回答）、`ImagesUndeclared=true`、`Efforts=nil`。
  「可对话过滤」只对**静态表里有**的 id 用静态字段判定；live 条目自己显式给了 `supportsToolCall:false` 才丢。
- 过滤：`supportsToolCall == true` 且 id 不匹配 `image|video|kling|hunyuan-image|codewise|nes|jump|rewrite|completion-gf|completions` 且 `maxInputTokens > 0`。
- 档位：`reasoning.supportedEfforts`（无则用 `reasoning.effort` 单档）→ `Offer.Efforts`；
  `defaultEffort` → `Offer.DefaultEffort`；`canDisableThinking` → `Offer.ReasoningOff`（`canDisableThinking:true` ⇒ `&true`，
  显式 `false` ⇒ `&false`，未给 ⇒ `nil`）。
- **窗口映射（单一含义，不要发明第二个）**：`Offer.ContextWindow = workBuddyModel.MaxInputTokens`
  （官方表里 `maxInputTokens` 就是「能塞进去的输入」，`maxOutputTokens` 走行上的 `DefaultMaxTokens`）。
  **不要**建 qodercn 那样的 `inputLimits map`：它的唯一消费者是 qodercn 自己的加密信封协议（`qodercn/transport.go:306`），
  标准 Chat Completions 体里没有这个字段，照抄就是死代码；`llm.Model.InputTokenLimit` 在本仓是 compaction 上限，
  不是窗口（`pkg/state` 的 `EffectiveInputWindow` 优先用它），本插件一律留 0。
- `installBuiltinModels`：`llm.SetBuiltinModels("workbuddy", []llm.Model)` —— 只为「登录前就有能力行」，
  不做别的（没有输入预算 map）。

### 3.6 前端插件

```ts
// plugins/subscriptions/workbuddy/frontend/index.ts
import type { SubscriptionFrontendPlugin } from '../../../../frontend/src/plugins/subscriptionPlugin'
export default {
  id: 'workbuddy',
  describe: (locale) => locale === 'zh' ? '腾讯 WorkBuddy 订阅额度' : 'Tencent WorkBuddy plan quota',
  waitingHint: (locale) => locale === 'zh' ? '已在浏览器打开 WorkBuddy 登录页，完成后这里会自动继续' : 'Finish signing in to WorkBuddy in the browser; this continues by itself',
  hideEmptyUserCode: true,   // 浏览器确认式登录，没有 user code 可显示
} satisfies SubscriptionFrontendPlugin
```

---

## 4. 行为矩阵（旧 → 新）

| 场景 | 当前行为 | 目标行为 | 备注 |
|---|---|---|---|
| 无插件目录（开源主树） | `/connect` 无 WorkBuddy；模型页无该 provider | 完全不变；生成脚本桥接 0 个 workbuddy 插件，二进制照常编译/测试全绿 | 红线：闭源隔离 |
| 插件在盘、未登录 | — | `/connect` 出现 `WorkBuddy`；`Probe=SignInMissing`；选中后开始登录 | |
| 登录进行中 | — | 展示 `authorizeURL`，`userCode` 为空且不显示输入框；轮询 1 s，最多 300 s | |
| 浏览器里完成登录 | — | 自动继续：写凭据（DB+文件）、拉账号、列出模型 | |
| 列出模型 | — | live `/v3/config` 优先，失败回落静态表并标 `FromStaticList` | owner 追问项 |
| 写入模型行 | — | `provider=workbuddy`、`source=workbuddy`，无 key | `ProvisionRows` |
| token 过期 | — | transport 静默刷新一次并重试 | |
| refresh 失效（401/403） | — | `Probe=SignInDead`，surface 提示重新登录；请求返回 401 | |
| Web「设置 › 模型」 | — | 同一订阅、同一 flow、同一份模型 | 三面一致 |
| 桌面端 WorkBuddy 正在运行 | — | 不受任何影响：插件只调服务端 API，不读不写 `workbuddy-desktop.info` | owner 裁决②（自有登录） |

---

## 5. Scope

**In scope**
- 新建 `plugins/subscriptions/workbuddy/`（7 个文件，全部 gitignored）。
- 重新运行 `scripts/gen-subscription-plugins.sh`，产出两个 gitignored 桥接文件。
- **企业账号的模型清单**：`/console/enterprises/{enterpriseId}/config/models`（owner 2026-11-15 裁决本期一并做；
  真机验证受账号类型限制，owner 同时裁决接受「企业路径只做单测」）。
- 计划文件按惯例另存 `docs/plan/WORKBUDDY_SUBSCRIPTION_PLAN.md`。

**Out of scope（明确不碰）**
- 宿主树任何文件：`pkg/**`、`cmd/forebrain/*.go`（除生成的 `plugins_local_gen.go`）、`frontend/src/**`（除生成的 `local_subscriptions.gen.ts`）。
- 桌面端凭据解密/复用（`workbuddy-desktop.info` 的 `$wbEncrypted` envelope）——本期不做，作为后续可选计划。
- 企业账号的**切换/登出**（`/v2/plugin/accounts`、`/account/switch`、`/auth/logout`）——清单本期做，切换不做。
- 图像/视频/代码补全模型接入。
- `qodercn` 插件与 `zcode` 插件的行为改动。
- 并行会话正在改的文件（`pkg/tui/*`、`pkg/tool/*`、`pkg/event/*`、`pkg/run/*`）——一律不碰。

---

## 6. Steps（每步一个可验证产出）

> 前置：`export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"`（CGO + PDFium，见 FOREBRAIN.md）。

**Step 1 — 目录骨架 + 注册**
建 `plugins/subscriptions/workbuddy/{plugin.go,auth.go,transport.go,models.go,models_static.go,frontend/index.ts}`，
`plugin.go` 的 `init()` 照 qodercn 写全四项注册，`workBuddySubscription` 先返回 `ErrNoKeyToRefresh` / 空 offers（可编译骨架）。
验证：
```bash
gofmt -l plugins/subscriptions/workbuddy        # 无输出
scripts/gen-subscription-plugins.sh             # 期望：Go plugin 数与 frontend 数都比生成前各 +1（当前盘上只有 qodercn → 2 / 2）
CGO_ENABLED=1 go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain   # 通过
./build/bin/forebrain --help >/dev/null         # 不 panic（init 跑过）
```

**Step 2 — 生成静态模型表**
按附录 A 的脚本从 `product.json` 生成 `models_static.go`（58 行全量，含 `supportsToolCall/Images`、窗口、reasoning）。
验证：
```bash
grep -c 'ID:' plugins/subscriptions/workbuddy/models_static.go            # 58
CGO_ENABLED=1 go build -tags fts5 ./plugins/subscriptions/workbuddy/       # 必须编过（真正的验证）
CGO_ENABLED=1 go test -tags fts5 ./plugins/subscriptions/workbuddy -run TestStaticTableMatchesOfficialProduct -count=1
# 该测试断言（硬编码期望值，**不读** /Applications，插件在没有 WorkBuddy 的机器上也能测）：
# len(staticWorkBuddyModels())==58、首个 id 是 "default"、
# "glm-5.3-flash" 的 Efforts==["low","high","max"] 且 CanDisableThinking!=nil && *==true、
# "glm-5.2".SupportsImages==false、"deepseek-v4-pro".MaxInputTokens==1000000
```

**Step 3 — 模型过滤 + live 清单**
实现 `FetchModels`：`GET {endpoint}/v3/config`（Bearer），合并静态，过滤可对话子集。
验证（单测）：`TestFetchModelsPrefersLiveAndFallsBackToStatic`、`TestFilterKeepsChatModelsOnly`、
`TestFetchModelsMergesEnterpriseList`（假网关：非企业账号 `enterpriseId==""` 时**不**请求企业端点；
企业账号时企业端点返回 **`{"data":[…model…]}`**（注意不是 `{"data":{"models":…}}`），断言企业清单的 id 覆盖同 id 的个人清单、
并追加个人清单没有的 id、该新 id 的能力未知时 `ContextWindow==0 && ImagesUndeclared==true`；
企业端点 403 时个人清单照出、不报错）
（断言：静态 58 → **可对话子集恰好 42 行**（已用 product.json 实算）；`hunyuan-image-alpha`、`kling-v3-t2v`、
`codewise-completions`、`hunyuan-3b` 不在结果里；live 返回一条静态表没有的 id 时该 id 出现且 `ImagesUndeclared==true`；
静态表里有、live 没有的 id 仍保留窗口与档位）。

**Step 4 — 登录流程**
`LoginExternalLink`：state → 发布 URL → 轮询（`code:11217` 继续）→ token → account → 落盘。轮询间隔/总超时/endpoint 全部走 §3.4 的包级可注入变量。
验证（单测，httptest 假网关）：`TestLoginExternalLinkPollsUntilToken`（断言 3 次 pending 后成功、`AuthorizeURL()` 在第一次
state 后立即可读、`UserCode()==""`）、`TestLoginExternalLinkStopsAtTimeout`（`loginPollInterval=50ms`、`loginTimeout=500ms`）。

**Step 4b — 真机 spike（必须先于 Step 5–8 做完，否则可能白扔四步）**
目的：在写 transport/offers 之前，用**真实登录**回答两个承重未知（都需要 owner 在浏览器里登录一次，见 §6 Step 9 的运行方式）：
1. **`ww_device_nonce` 是否必需**：先按 §2.3 不带 nonce 打开 `authUrl` 完成登录；若登录页拒绝或轮询永远 11217，
   按 §9 STOP#3 处理 —— owner 已裁决继续逆向宿主层（不改路线、不砍功能），先报告现象再动 `app.asar` 的 `main/*`。
   **成功判据**：`$FOREBRAIN_HOME/workbuddy.json` 出现且含 `access_token`（此时 `Offers()` 还是 Step 1 的空壳，
   TUI 走完登录后会报 `… offers no models this build can run`，**这是预期**，不是失败）。
2. **伪装头最小集合**：拿到 token 后跑下面三档（token 只进本机变量，不落文件、不进日志、不贴进计划）：
   ```bash
   TOKEN=$(python3 -c "import json;print(json.load(open('$FOREBRAIN_HOME/workbuddy.json'))['access_token'])")
   UID=$(python3 -c "import json;print(json.load(open('$FOREBRAIN_HOME/workbuddy.json'))['uid'])")
   body='{"model":"glm-5.0","stream":false,"messages":[{"role":"user","content":"hi"}]}'
   # (a) 只 Bearer
   curl -s -o /dev/null -w 'a=%{http_code}\n' -X POST https://www.workbuddy.cn/v2/chat/completions \
     -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d "$body"
   # (b) + 产品头
   curl -s -o /dev/null -w 'b=%{http_code}\n' -X POST https://www.workbuddy.cn/v2/chat/completions \
     -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
     -H 'X-Product: WorkBuddy' -H 'X-Product-Version: 5.9.3' \
     -H 'X-IDE-Type: WorkBuddy' -H 'X-IDE-Name: WorkBuddy' -H 'X-IDE-Version: 5.9.3' \
     -H 'X-Agent-Intent: craft' -H 'X-Agent-Runtime: local-agent' \
     -H 'X-Model-ID: glm-5.0' -H "User-Agent: WorkBuddy/5.9.3" -d "$body"
   # (c) + 身份头
   curl -s -o /dev/null -w 'c=%{http_code}\n' -X POST https://www.workbuddy.cn/v2/chat/completions \
     -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
     -H 'X-Product: WorkBuddy' -H 'X-Product-Version: 5.9.3' \
     -H 'X-IDE-Type: WorkBuddy' -H 'X-IDE-Name: WorkBuddy' -H 'X-IDE-Version: 5.9.3' \
     -H 'X-Agent-Intent: craft' -H 'X-Agent-Runtime: local-agent' \
     -H "X-User-Id: $UID" -H 'X-Domain: www.workbuddy.cn' -H 'X-Private-Data: true' \
     -H "X-Request-ID: $(uuidgen)" -H 'X-Model-ID: glm-5.0' -H "User-Agent: WorkBuddy/5.9.3" -d "$body"
   ```
   三档状态码记进附录 C。判断：哪一档首次 200，就按那一档的头集合实现 transport（全量伪装优先，但必须知道哪些是承重的）；
   三档全 401/403 → 触发 §9 STOP#4。
3. **顺带核对两个清单**：`curl -s -H "Authorization: Bearer $TOKEN" https://www.workbuddy.cn/v3/config`
   → 记 `data.models` 的形态与条数；若 `account.enterpriseId` 非空，再
   `curl -s -H "Authorization: Bearer $TOKEN" "https://www.workbuddy.cn/console/enterprises/$ENT/config/models"`
   → 记形态与条数、是否与个人清单同构。两项都回填 §3.5 与附录 C。
把三项结果写进计划文件末尾的「spike 记录」小节（脱敏，不含 token），再继续 Step 5。

**Step 5 — 刷新与失效**
`EnsureFreshCredentials`（提前 60 s）、`refreshCredentials`（`X-Refresh-Token`、`X-Auth-Refresh-Source: plugin`）、`isUnauthorized`。
验证：`TestRefreshUsesOfficialHeaders`、`TestRefresh401ReportsSignInDead`、`TestProbeStates`（Missing/Usable/Dead/Unreachable 四态）。

**Step 6 — Transport 伪装**
实现并接进 `newWorkBuddyLLM`：`NewCompatLLMWithTransport("", "https://www.workbuddy.cn/v2", req.Model, 0, req.MaxOutputTokens, "", req.ParamsJSON, &Transport{Home: root})`。
验证（单测）：`TestTransportSendsOfficialHeadersAndPath`
（httptest 断言：`r.URL.Path == "/v2/chat/completions"` 且 host == `workBuddyBaseURL` 的 host（即使凭证 `domain` 是别的值）；
`Authorization`、`X-Product`、`X-Product-Version`、`X-IDE-*`、`X-Agent-Intent: craft`、`X-Agent-Runtime: local-agent`、
`X-User-Id`、`X-Private-Data`、`User-Agent` 全在；`X-Request-ID` 非空；`X-Userinfo`/`X-Auth-Method`/`X-Enterprise-Id`
只在对应字段有值时才出现——用两份 fixture 各断言一次）、
`TestTransportRefreshesOnceOn401`、`TestTransportDoesNotLeakHostAuthorization`（上游自带的
`Authorization` 被替换成 Bearer，不被沿用）。

**Step 7 — Offers / Provision / 前端插件**
`Offers()` 手工构造 `[]subscription.Offer`（`BaseURL:"https://www.workbuddy.cn/v2"`、`APIPath:""`）；
`Provision()` → `subscription.ProvisionRows`；`RefreshKey()` → `ErrNoKeyToRefresh`；`frontend/index.ts` 如 §3.6。
验证：
```bash
scripts/gen-subscription-plugins.sh   # 数字各 +1（当前 2 / 2）
cd frontend && pnpm typecheck         # 生成文件与本插件 index.ts 一起通过
```

**Step 8 — 全量回归**
```bash
gofmt -l cmd pkg third_party plugins/subscriptions/workbuddy    # 无输出
CGO_ENABLED=1 go vet -tags fts5 ./...                            # 通过
CGO_ENABLED=1 go test -tags fts5 ./plugins/subscriptions/workbuddy ./pkg/subscription ./pkg/tui -count=1
# 全量测试必须用临时 home：agent 会话里 FOREBRAIN_HOME 已指向真实 home，直跑会迁移/写真实状态库
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./... -count=1     # 全绿（含插件包）
cd frontend && pnpm test && pnpm build                           # 通过
git status --porcelain                                          # 只剩并行会话的 WIP；本计划零跟踪文件改动
git grep -il workbuddy -- pkg cmd frontend/src                   # 仅 pkg/state/schema.sql:744 既有注释
```

**Step 9 — 真机验收（owner 在浏览器完成一次登录）**
```bash
export FOREBRAIN_HOME=$(mktemp -d); mkdir -p /tmp/wb-accept && cd /tmp/wb-accept
tmux new-session -d -s wb -x 120 -y 42 "cd /tmp/wb-accept && FOREBRAIN_HOME=$FOREBRAIN_HOME /Users/doudou/workspace/unionj-cloud/forebrain-harness/build/bin/forebrain; echo EXIT=$?; sleep 900"
tmux send-keys -t wb '/connect' Enter      # 选中 WorkBuddy → Enter
tmux capture-pane -t wb -p                 # 取下 authorizeURL 交给 owner 在浏览器登录
```
acceptance 断言（逐屏 capture 取证）：
1. `/connect` 列表出现 `WorkBuddy`。
2. 选中后展示登录链接、**不出现 code 相关文案**（应为「opens your browser to sign in」/「Waiting for you to finish signing in…」）且无 user code 输入框；
   浏览器完成登录后自动进入模型选择，候选模型**包含静态表的 42 行全集**；若 Step 4b 记录到 live 清单条数不同，以 live 为准并在验收记录里写出差集。
3. 选择 2–3 个模型后落库：
   `sqlite3 $FOREBRAIN_HOME/state/forebrain.state.sqlite "select id,provider,model from fb_llm_models where provider='workbuddy'"` 有行
   （注意库在 `$FOREBRAIN_HOME/state/` 下，不是 home 根）。
4. **头集合回归**：重跑 Step 4b 那三档完整 curl（命令在 Step 4b，token 从 `$FOREBRAIN_HOME/workbuddy.json` 取），
   状态码必须与附录 C 的 spike 记录一致；不一致（例如实现后反而变差）→ 触发 §9 STOP#4，先报告实测响应体（脱敏）。
5. 真实对话一次（短 prompt）：TUI 有流式回复。
6. 刷新路径单独验（不依赖自然过期）：把 `workbuddy.json` 的 `expires_at` 手改为过去时间 → 发一轮对话 →
   断言文件里 `expires_at` 前移、`access_token` 变化、请求仍成功（证明 transport 会刷新并重试）。
7. Web 面：`FOREBRAIN_GATEWAY_TOKEN=<随机串> FOREBRAIN_HOME=$FOREBRAIN_HOME ./build/bin/forebrain gateway start`，
   浏览器打开 `http://127.0.0.1:<port>/?token=<同上>` →「设置 › 模型」可见 WorkBuddy 订阅与同一批模型；
   命令行侧用 `curl -H "Authorization: Bearer $FOREBRAIN_GATEWAY_TOKEN" http://127.0.0.1:<port>/api/subscriptions`
   取证其中含 `"workbuddy"`。
   **企业清单的真机验收取决于 owner 账号类型**：`account.enterpriseId` 为空（本机实测就是个人账号）时，
   企业路径只能靠 Step 3 的单测证明 —— **owner 已裁决接受这个缺口（2026-11-15）**，验收记录写「企业真机路径未验，原因：账号非企业」即可，
   不许伪造通过。
8. 企业清单真机项：`python3 -c "import json;print(json.load(open('$FOREBRAIN_HOME/workbuddy.json'))['enterprise_id'])"` 为空 →
   记录「未请求企业端点（非企业账号）」并附上这一行输出；非空 → 按 Step 4b 第 3 项的命令取证企业清单条数，
   并与模型页候选数对账。**不许把「没请求过」写成「通过」**。
9. 失效路径：把 `workbuddy.json` 的 `refresh_token` 改成无效值后重跑 `/connect` → `Probe` 报登录失效（`SignInDead` 文案），可重新登录。
10. 收尾：`tmux kill-session -t wb`，删除临时 home 与 `/tmp/wb-accept`。

---

## 7. 测试计划（新代码自带）

位置：`plugins/subscriptions/workbuddy/workbuddy_test.go`（与被测代码同包，闭源，不进主树）。
模式：`httptest.Server` 假网关 + `t.Setenv`/结构体字段注入把 `endpoint` 指向 `srv.URL`（照 `qodercn/qodercn_test.go` 的做法）。
必测清单：
1. 登录：state → 3×pending(11217) → token；timeout；context 取消后不留 goroutine（`goleak` 风格手写检查或 `runtime.NumGoroutine` 断言）。
2. 刷新：头正确、401 → `SignInDead`、成功则 `ExpiresAt` 前移；
   **单飞**：假网关轮换 refresh token，10 个 goroutine 同时 `EnsureFreshCredentials` → 断言端点只被调用 1 次、文件只写一次（token 不变）。
3. Transport：路径 `/v2/chat/completions`、host 恒为 `workBuddyBaseURL`（凭证 `domain` 改成别的值时也不改路由，只改 `X-Domain`）、
   头集合逐项断言（含条件头两份 fixture）、401 刷新重试一次、上游 `Authorization` 被替换。
4. 模型：live 优先、静态回落、`FromStaticList` 标记、**企业清单合并（覆盖同 id / 追加新 id / 403 时跳过）**、
   可对话过滤、`ImagesUndeclared`、档位映射（`supportedEfforts`/`defaultEffort`/`canDisableThinking`）。
5. Provision：`provider=workbuddy` 行写入、无 key、`RefreshKey` 返回 `ErrNoKeyToRefresh`。
6. 凭据：DB+文件双写、0600 权限、两处冲突时取 `ExpiresAt` 较新的一份。
7. SSE：`Transport` 透传 `data: {...}`/`[DONE]`，`openai` 客户端能解析（用一段固定的 SSE 字节流）。
8. nonce 结论：Step 4b 若判定 `ww_device_nonce` 必需，则为 `nonceFor(...)` 加一条固定输入的黄金用例（生成逻辑来自宿主层逆向）；
   若判定不需要，不写代码，只把附录 C 的结论钉住。

---

## 8. Done criteria（机器可查）

1. `scripts/gen-subscription-plugins.sh` 输出的 Go/frontend 插件数都比生成前各 +1（当前 2/2），且生成的
   `cmd/forebrain/plugins_local_gen.go` 含 `plugins/subscriptions/workbuddy`。
2. `CGO_ENABLED=1 go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain` 成功。
3. `CGO_ENABLED=1 go test -tags fts5 ./plugins/subscriptions/workbuddy -count=1` 全绿，且用例数 ≥ 15。
4. `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./... -count=1` 全绿；
   `CGO_ENABLED=1 go vet -tags fts5 ./...` 干净；
   `gofmt -l cmd pkg third_party plugins/subscriptions/workbuddy` 无输出。
5. `cd frontend && pnpm typecheck && pnpm test && pnpm build` 通过。
6. `git status --porcelain` 无本计划引入的跟踪文件改动；`git status --ignored --porcelain | grep plugins/subscriptions/workbuddy` 显示 `!!`。
7. Step 9 的 10 条 acceptance 全部有 capture/命令输出取证；Step 4b 的三项 spike 结果写进末尾「spike 记录」。
8. 把 `plugins/subscriptions/workbuddy/` **移到临时目录**（`mv`，不要 `rm -rf`——该目录 gitignored 且是唯一副本），
   跑 `scripts/gen-subscription-plugins.sh`，再
   `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/subscription ./pkg/tui -count=1`
   （**必须带 `-tags fts5` + `CGO_ENABLED=1`**：实测裸 `go test ./pkg/subscription` 会以
   `this forebrain was built without cgo or SQLite FTS5` 失败），仍全绿后把目录移回（回归证明宿主不依赖插件）。

---

## 9. STOP conditions（遇到就停，先报告，不要即兴绕）

1. `/v2/plugin/auth/token` 成功载荷的字段名与 `accessToken/refreshToken/expiresIn/refreshExpiresIn` 不一致（例如嵌在 `data.data`）。
2. `/v3/config` 的 `data.models` 形态与 §3.5 假设都不符（既不是 model 对象数组，也不是 `{model:{…}}`）——报告脱敏后的响应结构再定。
   企业清单（`/console/enterprises/{enterpriseId}/config/models`）形态与之不同，或它对个人账号返回的不是 401/403 而是别的语义——同样先报告再定。
3. **Step 4b 证明 `ww_device_nonce` 是必需的**（登录页拒绝、或轮询永远停在 11217）：该 nonce 由 Electron 宿主层
   （`app.asar` 的 `main/*`）注入，**不在 `cli/dist` bundle 里**。owner 已裁决（2026-11-15）：**继续逆向宿主层拿它的生成方式**，
   不为它砍功能、也不改走桌面会话复用路线。做法：把现象（截图/响应码/是否只有 `platform=workbuddy` 要求）先报一次，
   然后在 `app.asar` 的 `main/*` 里按 `getLoginDeviceNonce` / `ww_device_nonce` / `setLoginStepCallback` 定位生成逻辑并移植进插件；
   宿主树仍然零改动。
4. `/v2/chat/completions` 除 Bearer 外还要求签名头（如 `X-Private-Data` 实际是签名而非布尔）——报告实测 401/403 响应体再改。
5. 任何「必须改宿主树（`pkg/`、`cmd/`、`frontend/src/` 非生成文件）才能跑通」的需求——闭源隔离红线，停下来报告。
6. 需要读取/解密桌面端 `workbuddy-desktop.info` 才能继续——owner 已裁决本期不做，停。

---

## 10. Maintenance notes

- **版本漂移**：`X-Product-Version`、`User-Agent`、静态模型表都取自 5.9.3 的 `product.json`（`genieVersion` 字段）。
  WorkBuddy 升级后需重跑附录 A 的脚本并同步这两个常量；端点与 `/plugin` 前缀来自 `authentication.attributes`，同理。
- **协议漂移**：登录 pending 码 11217 与 `/v2/plugin/*` 路径来自闭源客户端 bundle；若登录突然一直 pending，
  先用 §2.6 的 curl 复现，再按附录 B 重新读 bundle（`AccountScopedExternalLinkAuthenticationProvider` 仍可 grep 到）。
  `ww_device_nonce` 的生成在宿主层（`app.asar` 的 `main/*`），不在 CLI bundle；它的必要性由 Step 4b 的 spike 结论固定，
  一旦 spike 记录了「不需要」，后续不要因为登录偶发失败就回头怀疑它。
- **审阅重点**：transport 是否**替换**而非追加 `Authorization`；`X-User-Id` 是否 URL 编码；凭据文件权限是否 0600；
  日志里绝不出现 token（照 qodercn 的惯例，日志只打端点与状态码）。
- **已知遗留（本期不做）**：桌面端凭据复用（envelope 解密）、多账号切换/登出、图像/视频模型；
  企业清单的真机验证（取决于账号类型，单测已覆盖）。
  另：`CredentialMutex` 是进程内锁，TUI 与 gateway **两个进程**同时刷新同一份 `workbuddy.json` 仍可能撞车——
  与 qodercn 同源限制，不属本期；真出现再单独立项（跨进程文件锁）。
- **扩展点**：未来 `zcode` 插件同样走 `plugins/subscriptions/<id>/` + 生成脚本，无需再动宿主。

---

## 附录 A：从 product.json 生成 `models_static.go`（一次性）

生成物必须是**合法 Go**：字符串用双引号（`json.dumps` 输出与 Go 字面量兼容），布尔用小写 `true/false`，
`*bool` 用 `true/false/nil` 而非 Python 的 `True/None`。

```bash
python3 - <<'PY' > plugins/subscriptions/workbuddy/models_static.go
import json
d = json.load(open('/Applications/WorkBuddy.app/Contents/Resources/app.asar.unpacked/cli/product.json'))

def go_str(s):   return json.dumps(s, ensure_ascii=False)
def go_bool(b):  return 'true' if b else 'false'
def go_opt_bool(v):
    return 'nil' if v is None else ('&__boolTrue' if v else '&__boolFalse')

print('// Code generated from WorkBuddy 5.9.3 product.json; DO NOT EDIT by hand.')
print('package workbuddy')
print()
print('var __boolTrue, __boolFalse = true, false')
print()
print('func staticWorkBuddyModels() []workBuddyModel {')
print('\treturn []workBuddyModel{')
for m in d['models']:
    r = m.get('reasoning') or {}
    efforts = r.get('supportedEfforts') or ([r['effort']] if r.get('effort') else [])
    print('\t\t{')
    print(f'\t\t\tID: {go_str(m["id"])}, Name: {go_str(m.get("name", ""))},')
    print(f'\t\t\tMaxInputTokens: {int(m.get("maxInputTokens") or 0)}, MaxOutputTokens: {int(m.get("maxOutputTokens") or 0)},')
    print(f'\t\t\tSupportsToolCall: {go_bool(bool(m.get("supportsToolCall")))}, SupportsImages: {go_bool(bool(m.get("supportsImages")))},')
    print(f'\t\t\tIsDefault: {go_bool(bool(m.get("isDefault")))},')
    print(f'\t\t\tEfforts: []string{{{", ".join(go_str(e) for e in efforts)}}},')
    print(f'\t\t\tDefaultEffort: {go_str(r.get("defaultEffort", "") or "")},')
    print(f'\t\t\tCanDisableThinking: {go_opt_bool(r.get("canDisableThinking"))},')
    print('\t\t},')
print('\t}')
print('}')
PY
gofmt -w plugins/subscriptions/workbuddy/models_static.go
grep -c 'ID:' plugins/subscriptions/workbuddy/models_static.go            # 期望 58
CGO_ENABLED=1 go build -tags fts5 ./plugins/subscriptions/workbuddy/       # 必须编过（Step 2 的真验证）
```

> 注意 `__boolTrue/__boolFalse` 是给 `*bool` 取的地址；若仓库 lint 不允许包级可变变量，
> 改成生成 `func boolPtr(v bool) *bool { return &v }` 并在表里写 `CanDisableThinking: boolPtr(true)`。

## 附录 B：重新取证（WorkBuddy 升级后照着做）

```bash
APP=/Applications/WorkBuddy.app/Contents/Resources/app.asar.unpacked
# 1) 产品配置（endpoint / prefixPath / models / genieVersion）
python3 -c "import json;d=json.load(open('$APP/cli/product.json'));print(d['endpoint']);print(d['authentication']['attributes']);print(len(d['models']),d['genieVersion'])"
# 2) 登录与请求实现（压缩 bundle，按符号名定位后读窗口）
grep -c 'AccountScopedExternalLinkAuthenticationProvider' $APP/cli/dist/codebuddy-lite-wb.mjs
# 2b) 产品清单的远端合并（在 harness lib，不在 CLI bundle）
grep -o 'console/enterprises' $APP/node_modules/@genie/agent-harness/lib/product/authority.js | head -1
grep -o 'data?.data' $APP/node_modules/@genie/agent-harness/cli/dist-server/shared.cjs | head -3
# 3) 端点活性（四个，互不依赖）
curl -s -o /dev/null -w '%{http_code}\n' https://www.workbuddy.cn/
curl -s 'https://www.workbuddy.cn/v2/plugin/auth/token?state=probe0000'
curl -s -X POST 'https://www.workbuddy.cn/v2/plugin/auth/state?platform=workbuddy' -H 'Content-Type: application/json' -H 'X-No-Authorization: true' -H 'X-No-User-Id: true' -H 'X-No-Enterprise-Id: true' -H 'X-No-Department-Info: true' -d '{}'
curl -s 'https://www.workbuddy.cn/v3/config'
```

## 附录 C：spike 记录（Step 4b 回填，脱敏：不写 token）

| 项 | 结果 | 证据 |
|---|---|---|
| 不带 `ww_device_nonce` 能否完成登录 | 待填（能 / 不能 + 现象） | 截图或轮询响应码 |
| 伪装头最小集合 | 待填（(a) 只 Authorization → 状态码；(b) +产品头 → 状态码；(c) +身份头 → 状态码） | curl 输出 |
| `/v3/config` 带 Bearer 后的 `data.models` 形态与条数 | 待填（元素形态 + 条数，与静态 42 的差集） | 脱敏 JSON 片段 |
| 企业清单 `data.data` 形态与条数（仅企业账号） | 待填（个人账号记「未请求」，附 `account.enterpriseId` 为空） | 脱敏 JSON 片段 |
| 首次真实对话 | 待填（模型 id、是否流式、耗时） | TUI capture |

---

## Implementation Status

**结论：APPROVE（代码完成；真机 4b/9 按裁决留给 owner）。**
执行模型：opencode-go/deepseek-v4.1-flash（general-purpose 执行者，1 轮修订）。
Worktree：`/Users/doudou/workspace/unionj-cloud/forebrain-harness/.worktrees/workbuddy-subscription`，分支 `workbuddy-subscription`，基线 `9f23024a`。
**提交：零 commit**（本计划的 git 流程即「不留提交」：交付物全部 gitignored）；合并/安装与否由 owner 决定。

### 任务完成度

| 计划项 | 状态 | 说明 |
|---|---|---|
| Step 1 骨架 + 四项注册 | 完成 | `plugin.go` 228 行 |
| Step 2 静态模型表（58 行） | 完成 | 附录 A 脚本生成，`TestStaticTableMatchesOfficialProduct` 硬编码断言 |
| Step 3 模型清单/过滤/企业合并 | 完成 | `chatModels` 58→42；`/v3/config` `data.models` → 企业 `data.data` → 静态补能力；live 独有 id 保留 + `ImagesUndeclared` |
| Step 4 登录流程 | 完成 | state → 发布 URL → 1 s 轮询（11217 继续）→ token → account → 落盘；超时/取消都有用例 |
| **Step 4b 真机 spike** | **未做（DEFERRED）** | 需要 owner 在浏览器登录一次；`ww_device_nonce` 是否必需、伪装头最小集合、两个清单真实形态均未实测 |
| Step 5 刷新与失效 | 完成 | 单飞（锁内重读）、`X-Refresh-Token`/`X-Auth-Refresh-Source: plugin`、401/403→Dead |
| Step 6 Transport 伪装 | 完成 | 路径/host/头表/替换 Authorization/401 刷新一次重试/SSE 透传 |
| Step 7 Offers/Provision/前端插件 | 完成 | `ProvisionRows`、`ErrNoKeyToRefresh`、`frontend/index.ts` + typecheck |
| Step 8 全量回归 | 完成（有外部噪声） | 见下「我实跑的验收」 |
| **Step 9 真机验收（10 条）** | **未做（DEFERRED）** | 需要 owner 登录 + 真实对话；附录 C 保持 `待填` |
| 计划另存 `docs/plan/WORKBUDDY_SUBSCRIPTION_PLAN.md` | 未做 | 属 owner 收口动作，执行者按派发约束未动 `docs/` |

### 我实跑的验收（tech-lead 复核，均在 worktree 内）

| 检查 | 结果 |
|---|---|
| `scripts/gen-subscription-plugins.sh` | **PASS**：2 Go / 2 frontend（生成前 1/1），桥文件含 workbuddy |
| `CGO_ENABLED=1 go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain` + `--help` | **PASS** |
| `gofmt -l plugins/subscriptions/workbuddy` / `go vet ./plugins/subscriptions/workbuddy` | **PASS**（干净） |
| `go test ./plugins/subscriptions/workbuddy`（19 个测试函数） | **PASS** |
| `-count=3 -race` / `-count=2 -shuffle=on` / `-count=2` | **PASS**（修订后；修订前 `-count=2` 稳定失败，见「修订」） |
| `go test ./pkg/subscription`（带插件 & 插件移走后） | **PASS** |
| `git status --porcelain` / `--ignored` | **PASS**：跟踪树零改动；`plugins/`、两个桥文件都是 `!!` |
| `git grep -il workbuddy -- pkg cmd frontend/src` | **PASS**：仅 `pkg/state/schema.sql`（既有注释） |
| DoD#8 插件移走回归（`mv` → gen 1/1 → 测试 → 移回 → gen 2/2） | **PASS** |
| `cd frontend && pnpm typecheck` | **PASS** |
| `gofmt -l cmd pkg third_party` | **FAIL（预存）**：`pkg/tui/subagent_footer_display_test.go` 最后一次改动是 `3506aea8`，非本次 |
| `go vet ./pkg/tui` | **FAIL（预存）**：`tty_session_test.go` 引用 `composerHintRows`，该字段在 `9f23024a` 的生产代码里不存在（由并行会话未提交 WIP 提供） |
| `go test ./...`（临时 home） | 37 包 ok；**`pkg/tui` build 失败**、**`pkg/architecture` SQLite 快照 3 条 `volcengine` 语句** 失败——两者在主检出用同一命令复现**完全相同**，与插件无关（插件不含任何 `volcengine`/SQL） |
| `cd frontend && pnpm test` | 1449/1449 用例通过；1 个文件加载失败：`settingsSchema.test.ts` import 了 gitignored 的 `docs/plan/.../config-db-settings-preview.html?raw`，新 worktree 无 `docs/plan/`（主检出该文件存在）→ 环境产物 |
| `cd frontend && pnpm build` | PASS（执行者运行） |

### 修订（1 轮）

**REVISE 原因**：测试非幂等——`workbuddy_test.go` 用 `llm.SetBuiltinModels("workbuddy", nil)` 清理，抹掉了 `init()` 发布的包级能力表，`-count=2` 稳定失败（`builtin levels for glm-5.3-flash = [] known=false`）。
**修法（仅测试文件）**：新增 `restoreShippedTable(t)`（安装/复原 `installBuiltinModels(chatModels(staticWorkBuddyModels()))`）、`savePluginKnobs(t)`（三个旋钮整体快照/还原），注册测试改为自装表后再断言、端点断言改用产品端点字面量。生产代码一字未动（mtime 可证）。修订后 `-count=3 -race`、`-count=2 -shuffle=on`、`-count=2` 全绿。

### 偏离计划之处（均已审阅，接受）

1. `Credentials` 把 `AuthMethod` 与 `LoginMethod` 合并成单个 `LoginMethod`（两者恒为 `"browser"`，一个概念一个实现）。
2. `init()` 发布 `chatModels(staticWorkBuddyModels())`（42 行可对话子集）而非裸 58 行，避免图像/视频/补全模型进能力目录。
3. `toLLMModel` 不设 `APIModel`（`ID` 已足够，qodercn 同）。
4. `FetchModels` 返回 `(records, fromStatic)` 双值（`FromStaticList` 需要它）。
5. `workBuddyModel` 多一个未导出字段 `imagesUndeclared`，承载 live 独有 id 的「模态未知」。
6. `X-Auth-Method` 无条件发送（§2.4 定义为常量；Step 6 括注把它列进条件头，属计划措辞不一致）。
7. 请求体 > 32 MiB **报错拒绝**而非静默截断；account/清单解码各容忍一层包裹。

### 仍未验证（不得当作已通过）

- Step 4b：`ww_device_nonce` 是否必需、伪装头最小集合（a/b/c）、`/v3/config` 与 `/v2/plugin/auth/token` 的真实载荷字段名与清单形态。
- Step 9 全部 10 条 acceptance（含 TUI `/connect` 文案、模型数、真实对话、刷新、Web 入口、失效路径）。
- 企业清单真机路径（本机个人账号；owner 已裁决接受只做单测）。

### 安装到主检出（已完成 2026-11-15）

- 「合并」的实际含义：分支 `workbuddy-subscription` **零提交**（`git log 9f23024a..workbuddy-subscription` 为空），
  `git merge` 是 no-op，**dev HEAD 保持 `9f23024a` 不变**；交付物全部 gitignored，故以文件安装 + 重生成桥接代替分支合并。
- 已执行：`plugins/subscriptions/workbuddy/`（7 文件）拷入主检出 → `scripts/gen-subscription-plugins.sh`（2 Go / 2 frontend）
  → `gofmt`/`go vet` 干净 → `go test ./plugins/subscriptions/workbuddy` PASS → `go build -tags fts5 ./cmd/forebrain` 成功
  → 二进制内含 `Sign in with WorkBuddy`（插件已链入）→ `frontend pnpm build` 重出 `pkg/gateway/dist`（`SettingsView-*.js` 含 WorkBuddy 插件）
  → 再重建二进制以嵌入新 dist。
- 主检出 `git status` 里属于本计划的**跟踪改动为零**（插件与桥接全部 gitignored）；其余改动见下节。
- worktree `.worktrees/workbuddy-subscription` 与空分支 `workbuddy-subscription`（原指向 `9f23024a`）已删除。

### 本期一并修掉的预存失败（owner 指令：预存失败不许留到下一期）

| 预存失败 | 根因 | 修法 | 复核 |
|---|---|---|---|
| `gofmt -l cmd pkg third_party` 报 `pkg/tui/subagent_footer_display_test.go` | 该文件（`3506aea8`）缺文件尾换行 | 补换行 | `gofmt -l` 全空 |
| `pkg/architecture` `TestSQLiteStatementsUnchanged`：437 快照 / 440 实存 | `a1da14e4` 在 `pkg/state/schema_migrations.go` 加了 3 条 volcengine `UPDATE fb_llm_models …` 却没重生成快照 | `go run ./scripts/sqlite-sql-snapshot`（diff 只多这 3 条语句） | 守卫 PASS |
| `pkg/architecture` `TestTestFilesCorrespondToProductionFiles`：4 个 `_test.go` 没有同名生产文件 | `9f23024a` 与 `3506aea8` 新建的独立测试文件违反「测试文件按被测生产文件命名」 | 内容并入被测生产文件的测试：`deliverables_test.go`→`reducer_test.go`、`glyph_test.go`+`subagent_footer_display_test.go`→`render_test.go`、`pkg/run/tool_shape_test.go`→`pkg/run/subagent_test.go`，删原文件（12 个测试函数逐一确认仍在） | 守卫 PASS；`pkg/tui`、`pkg/run` 全绿 |
| `frontend pnpm test`：`settingsSchema.test.ts` `expected 34 to be 33` | 设计稿 `docs/plan/.../config-db-settings-preview.html` 仍画着 `features.skill_offer` 开关，而 `4700956c` 已把该机制连同 schema 一起删除（设计稿是 gitignored 文档，当时没跟着改） | 从设计稿 `TBSW` 删掉那一行（只改 gitignored 设计稿，不动 schema） | 该文件 9/9 PASS，前端 189/189 |

最终复核（我实跑）：`FOREBRAIN_HOME=$(mktemp -d) CGO_ENABLED=1 go test -tags fts5 ./... -count=1` → **exit 0，39 包 ok，零 FAIL**；`go vet -tags fts5 ./...` 无输出；`gofmt -l cmd pkg third_party` 无输出；`cd frontend && pnpm test` → 189 文件 / 1458 用例全过。
