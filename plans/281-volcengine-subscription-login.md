# Plan 281: 火山方舟订阅登录（设备码 → 套餐/模型 → 个人版手填 Key、团队席位自动取 Key）

> **Executor instructions**: 按步骤顺序执行，每步的验证命令通过后再做下一步。遇到
> 「STOP conditions」里的情况就停下来报告，不要自己发挥。**不要 commit / push / merge**：
> 改动留在工作区里，做完报告（仓库主人手动提交）。`plans/README.md` 由评审方维护，不要改。
>
> **Drift check (run first)**:
> `git rev-parse --short HEAD`（计划基线 `ecc4e1aa`，2026-11-12 reconcile 刷新；后代提交亦可）
> `git diff --stat ecc4e1aa..HEAD -- pkg/subscription/ pkg/gateway/login_api.go pkg/gateway/subscriptions_api.go frontend/src/components/models/SubscriptionLoginModal.vue pkg/tui/setup.go`
> → 空输出即无漂移；有输出则对照「Current state」逐段核对（行号漂移不算，内容对不上 STOP）。
> 工作区里其它并行会话的未提交改动是预期前置，不要回退。
>
> **2026-11-12 reconcile 留痕**：原基线 `0a698bf2` 的 diff 里 `pkg/subscription/` 全量新增是 264 自身落地（`990ec9dd`），其后 282/283 又改了 `setup.go`（+219，订阅授权页/模型列表）、`SubscriptionLoginModal.vue`（+13）、`subscriptions_api.go`（+20）——均为预期。本计划锚点已按 `ecc4e1aa` 复核：`registry.go` 的 `FlowDeviceCode`（:25）与 `ProvisionOptions`（:78）在位且注释仍点名 plan 281；`setup.go:291` 仍 `var subscriptions = subscription.Registry` 按注册表渲染；弹窗的 `device_code` 流程模板在位。核心发现仍成立：注册表无 volcengine 实现。

## Status

- **Priority**: P1
- **Effort**: L
- **Risk**: HIGH（第三方登录协议；凭证）
- **Depends on**: plan 264（注册表）、265（`/api/subscriptions`）、266（订阅弹窗）、267（TUI 订阅入口）
- **Category**: direction
- **Planned at**: commit `ecc4e1aa`（2026-11-12 reconcile 刷新锚点；原 `0a698bf2`——plan 264 的 Step 1 调研在 2026-11-08 执行并 STOP，owner 当日裁定把火山拆到这里）
- **来源**: plan 264 的 Step 1 调研产物（`docs/plan/VOLCENGINE_LOGIN_FACTS.md`，worktree 内、gitignore）已把可核实的事实原样抄进本计划 §Current state，执行者不需要访问该文件。

## Why this matters

owner 2026-11-07：「火山方舟 volcengine 的 coding plan 和 agent plan 也必须提供**内置登录**支持，参考 ark-cli」。裁定只做国内 cn-beijing（个人版与团队席位）；BytePlus 海外只作内置地址手填 Key（plan 263 已加）；默认协议 Anthropic Messages。

**2026-11-08 调研结论改写了「自动取 Key」这一半**：火山公开 SDK 与官方 API 参考里**没有** `ListApiKeys` / `GetRawApiKey` / `CreateApiKey` / `UpdateApiKey`，Agent Plan 个人版套餐 Key 也没有公开的明文取法（官方 `RegeneratePersonalApiKey` 明确不回传新 Key）。因此本计划的取 Key 口径是：

- **个人版（Coding Plan / Agent Plan）**：登录只用来**证明账号身份、判定持有哪些套餐、列出套餐支持的模型**；API Key 由用户从方舟控制台复制后粘进登录流程（一次，用于本次选中的所有个人版套餐行）。不做任何私有接口、不抓包、不反编译控制台。
- **团队席位**：官方 `GetTeamSeatApiKey` 返回席位 Key 明文（需 `ArkFullAccess`），可以自动取。

## Current state（事实与出处，采集于 2026-11-08）

参考物：`github.com/volcengine/volcengine-cli` 提交 `2e2cb43685bb55dd28b25bc39186763812aa3313`（`cmd/consolelogin_*.go`）、`github.com/volcengine/volcengine-go-sdk` tag `v1.2.62`（`volcengine/signer/volc/volc.go`）、官方 API 参考 `https://docs.volcengine.com/docs/ark/<slug>?lang=zh`。

### Console 登录（OAuth 2.0 设备码，RFC 8628）

| # | 事实 | 值 | 出处 |
|---|---|---|---|
| 1.1 | 设备授权端点 | `POST https://signin.volcengine.com/authorize/oauth/device_authorization` | volcengine-cli `cmd/consolelogin_oauth_client.go:13`、`:24` |
| 1.2 | 取 token 端点 | `POST https://signin.volcengine.com/authorize/oauth/token` | 同上 `:21`；表单拼装 `:220-:230`、`:281-:300` |
| 1.3 | `client_id` | `trn:signin:::devtools/cross-device` | 同上 `:39`；`cmd/consolelogin_login.go:130`、`:171` |
| 1.4 | `scope` | `Console:All:All` | `cmd/consolelogin_login.go:19` |
| 1.5 | `device_info` | 调用方名字，官方 CLI 传 `Volcengine CLI` | 同上 `:22`、`:173` |
| 1.6 | 设备授权请求字段 | form：`client_id`、`scope`、`device_info` | `cmd/consolelogin_oauth_client.go:220-:230` |
| 1.7 | 设备授权响应字段 | `device_code`、`user_code`、`verification_uri`、`verification_uri_complete`（可选）、`expires_in`、`interval`（可选） | 同上 `:157-:165` |
| 1.8 | 轮询间隔 | 缺省 5s；`slow_down` 每次 +5s | `cmd/consolelogin_device_code_poll.go:10`、`:11` |
| 1.9 | 每次轮询只发一次 HTTP | 设备码轮询传 `attempts=1`，关掉传输层重试 | `cmd/consolelogin_oauth_client.go:264-:274` |
| 1.10 | 轮询错误分支 | `authorization_pending` 继续；`slow_down` 加间隔；`access_denied` 终止；`expired_token`/`invalid_device_code` 终止；`server_error`/`temporarily_unavailable` 计入连续瞬时错误预算 | `cmd/consolelogin_device_code_poll.go:54-:65` |
| 1.11 | 瞬时错误预算 | 连续 5 次 | 同上 `:23` |
| 1.12 | token 响应 | `access_token`（**一段 JSON 字符串**，即 STS）、`token_type`、`refresh_token`、`id_token`（JWT）、`expires_in`（秒，约 900） | `cmd/consolelogin_oauth_client.go:169-:174` |
| 1.13 | `access_token` 的内容 | JSON：`access_key_id`、`secret_access_key`、`session_token`（**没有**额外的「token → STS 换取」步骤） | 同上 `:177-:183` |
| 1.14 | 刷新 | `grant_type=refresh_token` + `refresh_token` + **签发时的同一个 `client_id`** + `scope`；返回新 STS 与新 `issued_at` | 同上 `:143-:146`、`:262-:300`；`cmd/consolelogin_login.go:41-:47`、`:524` |
| 1.15 | 过期判定 | `issued_at + expires_in`，留 60s 安全余量 | `cmd/consolelogin_login.go:495-:502` |

厂商侧的授权码流程（`trn:signin:::devtools/same-device`）与其遗留粘码方式**已被移除**：不做同机回环回调、不做粘码、不做 PKCE。

### 方舟管控面 API

统一形状：`POST https://ark.cn-beijing.volcengineapi.com/?Action=<Action>&Version=2024-01-01`，`Service=ark`、`Region=cn-beijing`，Access Key 鉴权：

`Authorization: HMAC-SHA256 Credential=<AK>/<X-Date>/cn-beijing/ark/request, SignedHeaders=content-type;host;x-content-sha256;x-date, Signature=<Signature>`，另带 `X-Date`、`X-Content-Sha256`（出处：`docs/ark/base-url-and-authentication` 的「管控面 API 鉴权」）。

| # | Action | 请求 | 响应路径 | 出处 |
|---|---|---|---|---|
| 2.1 | `GetPersonalPlan` | Body `Plan`：`AgentPlan` \| `CodingPlan` | `Result.PlanType`（AgentPlan → Small/Medium/Large/Max；CodingPlan → Lite/Pro）、`Result.Status`（Running/Expired）、`Result.StartTime`、`Result.EndTime`；未购买 → `ResourceNotFound.Plan` | `docs/ark/get-personal-plan-api` |
| 2.2 | `ListArkCodingPlanModel` | 无参数 | `Result.Datas[].ModelID` | `docs/ark/list-ark-coding-plan-model-api` |
| 2.3 | `ListArkAgentPlanModel` | Body `Edition`：`personal` \| `enterprise` | `Result.Datas[].ModelID` | `docs/ark/list-ark-agent-plan-model-api` |
| 2.4 | `ListSeatInfos` | Body `Filter`：`BillingStatus`（1 pending/2 running/3 expired/4 reclaimed）、`BizInfo`、`SeatIDs`、`SeatStatus`（1 Idle/2 Active）、`UserID`、`UserName`，支持分页 | `Result.BizSummaries[]`（`BizInfo`、`ActiveCount`、`TotalCount`）、`Result.Data[]`（`SeatID`、`AccountID`、`ProjectName`、`BizInfo`、`BillingStatus` …） | `docs/ark/list-seat-infos-api` |
| 2.5 | `GetTeamSeatApiKey` | Body `SeatId`（来自 2.4） | `Result.APIKey`（明文）；需 `ArkFullAccess` | `docs/ark/get-team-seat-api-key-api` |
| 2.6 | `RegeneratePersonalApiKey` | Body `Plan`（只接受 `AgentPlan`）、`ProjectName` | `Result.Status`/`Result.Message`；**不返回新 Key** | `docs/ark/regenerate-personal-api-key-api` |

**没有公开出处、本计划不使用**：`ListApiKeys`、`GetRawApiKey`、`CreateApiKey`、`UpdateApiKey`、任何「读出个人版套餐 Key 明文」的接口。团队席位档位值（Agent Plan 企业版是 Small..Max 还是别的）公开文档也没定义，实现里只能用 2.4 返回的 `BizInfo` 原样展示。

数据面（plan 263 注册表里已有）：Coding Plan Anthropic 端点 `https://ark.cn-beijing.volces.com/api/coding`（SDK 追加 `/v1/messages`）、Agent Plan `…/api/plan`；OpenAI 兼容 `…/api/coding/v3`、`…/api/plan/v3`。本计划默认 Anthropic 协议（owner 裁定）。

## Commands you will need

| Purpose | Command | Expected |
|---|---|---|
| 构建 | `go build ./...` | exit 0 |
| 新包测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/subscription/... -count=1` | ok |
| 网关回归 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/ -run 'Subscriptions|Login' -count=1` | ok |
| 前端 | `cd frontend && pnpm typecheck && pnpm test` | 全过 |
| 全量 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./... -count=1` | 除既有三条 architecture 红外无新失败 |

## Scope

**In scope**:
- `pkg/subscription/volcengine/`（新子包）：`doc.go`、`oauth.go`、`sts.go`、`openapi.go`、`plans.go`、`subscription.go` 及测试
- `pkg/subscription/registry.go`：`ProvisionOptions` 加 `Key string`（个人版手填 Key）与 `Offers`/`Provision` 的文档注释；`Registry()` 加 `volcengine`
- `pkg/architecture/cache_test.go`：`allowedSubpackage` 加 `subscription/volcengine`；`graph.json` 重生成
- `pkg/gateway/subscriptions_api.go`：`provision` 请求体加 `key`（只在 `ProvisionOptions.Key` 用到；转发给注册表），409 `needs_key` 语义改为「该套餐需要一把 Key」；`pkg/gateway/llm_models_api.go` 的 `refresh-key` 对个人版返回 400 短句「个人版套餐的 Key 用手填，重新粘一次即可」
- `frontend/src/components/models/SubscriptionLoginModal.vue`（+ 测试）：个人版套餐的 Key 输入步骤（密码框 + 说明「在方舟控制台复制 API Key，只保存在本机」）；`frontend/src/lib/api.ts` 类型
- `pkg/tui/setup.go`（+ 测试）：订阅列表出现火山方舟；设备码展示与自动轮询；个人版套餐在选完模型后要求手填 Key
- `docs/config/*` 与 `docs/zh/config/*`（若已有订阅登录说明，补一节；没有就跳过）

**Out of scope**:
- 购买/续费套餐、轮换 Key、创建 Key、禁用 Key。
- BytePlus 登录（owner 裁定不做；只有内置地址手填 Key）。
- 任何私有接口、控制台页面抓取、浏览器自动化。

## Steps

### Step 1: `openapi.go` —— V4 签名与调用

标准库实现火山 V4 签名（参考 `volcengine-go-sdk` v1.2.62 `volcengine/signer/volc/volc.go`；该 tag 下没有签名测试向量，需自建向量并用官方文档的 cURL/Response 示例校验）。提供：

```go
type Client struct{ Transport http.RoundTripper; Endpoint string /* https://ark.cn-beijing.volcengineapi.com */ }
func (c *Client) Do(ctx context.Context, action string, creds STS, in, out any) error
```

- 请求：`POST {Endpoint}/?Action=<action>&Version=2024-01-01`，JSON body，头 `X-Date`、`X-Content-Sha256`、`Authorization`（形状见 Current state 2.0.1）。
- 凭证过期（`issued_at + expires_in - 60s`）时先刷新（Step 2）再发。
- 错误统一转成短句：`ark <action>: <code>`（`ResponseMetadata.Error.Code/Message` 里只取 code，`Message` 只在**不含**账号/Key 信息时附带；不打印响应体）。

**Verify**: `go test ./pkg/subscription/volcengine/ -run 'Signer|Client'` → ok（含一条逐字节比对的签名向量：固定 AK/SK、固定 `X-Date`、固定 body，断言最终 `Authorization` 头）。

### Step 2: `oauth.go` + `sts.go` —— 设备码与 STS

- `oauth.go`：`StartDevice(ctx) (DeviceAuth, error)`、`Poll(ctx, d DeviceAuth) (Token, error)`，字段与错误分支照 Current state 1.1–1.15（每次轮询只发一次 HTTP；`slow_down` 加间隔；`access_denied`/`expired_token`/`invalid_device_code` 短句终止；瞬时错误预算 5）。
- `sts.go`：`ParseSTS(accessToken string) (STS, error)`（内嵌 JSON），`Refresh(ctx, refreshToken, clientID) (Token, error)`；`STS` 带 `AccessKeyID/SecretAccessKey/SessionToken/IssuedAt/ExpiresAt`。
- 凭证存库：`ConfigStore.SaveCredential(ctx, "auth.volcengine", json)`（形状与 `auth.chatgpt` 同一种存法）；`Probe` 读它判断已登录/可刷新。

**Verify**: `go test ./pkg/subscription/volcengine/ -run 'Device|STS|Refresh'` → ok（`httptest.Server` 冒充 signin，不连真网；断言请求字段 client_id/scope/device_info、间隔推进、刷新带原 client_id）。

### Step 3: `plans.go` + `subscription.go` —— 套餐、模型与写入

- `plans.go`：`Plans(ctx, client, sts) ([]Plan, error)`：`GetPersonalPlan{Plan:"CodingPlan"}` 与 `{Plan:"AgentPlan"}` 各一次（`ResourceNotFound.Plan` = 没有该套餐，不算错误），再 `ListSeatInfos`（`SeatStatus=2`，分页取全）得到团队席位；每个持有的套餐列出模型：个人 Coding → `ListArkCodingPlanModel`；个人 Agent → `ListArkAgentPlanModel{Edition:"personal"}`；团队 → `ListArkAgentPlanModel{Edition:"enterprise"}` + 席位 `BizInfo` 原样展示。
- `subscription.go`：实现 `subscription.Subscription`：
  - `ID()="volcengine"`、`Label()=("Volcengine Ark","火山方舟")`、`Flows()=[FlowDeviceCode]`；
  - `Offers`：每套餐 × 每模型一条，`Provider:"volcengine"`、`BaseURL` 取 plan 263 注册表里该套餐的 Anthropic 端点、`APIPath:"/v1/messages"`、`Plan` 用 `coding-plan`/`agent-plan`/`coding-plan-team`/`agent-plan-team`；**任何列表接口失败时回落 plan 263 注册表里的内置模型表**（`Offer` 上标记静态来源，界面显示「可用模型以控制台为准」）；
  - `Provision`：团队席位 → `GetTeamSeatApiKey{SeatId}` 取明文；个人版 → 要求 `opts.Key` 非空（空则返回 `ErrNoKey` 之外的专用错误 `errPersonalKeyRequired`，由网关翻成 409 `needs_key`）；写 `process.UpsertLLMModel(…, Source:"volcengine", SourcePlan:<plan>)`；**明文只在进程内传递，不进日志、不进错误文本、不进测试输出**；
  - `RefreshKey`：团队席位 → 重新 `GetTeamSeatApiKey`；个人版 → `ErrNoKeyToRefresh`（界面提示重新粘一次）。
- 设备码的浏览器自动打开由宿主注入（TUI 打开 `verification_uri_complete`，无则 `verification_uri`；网关只展示 URL + code）。无图形环境（`DISPLAY`/`WAYLAND_DISPLAY` 皆空或设了 `SSH_CONNECTION`）时不打开，流程照常轮询。

**Verify**: `go test ./pkg/subscription/volcengine/ -run 'Plans|Provision|Offers'` → ok；测试覆盖：三种套餐分支、无套餐、静态回落、`needs_key`、团队席位 Key 明文写入、错误文本不含桩里的 key/token。

### Step 4: 网关与前端、TUI 接上

- `pkg/gateway/subscriptions_api.go`：`provision` 请求体加 `key`；`needs_key` 409 body 带 `plan`（让界面知道哪一组要 Key）。
- `frontend/src/components/models/SubscriptionLoginModal.vue`：`device_code` 步骤之后，若返回的 offer 里有个人版套餐，显示 Key 输入（密码框 + 一行说明）；提交时把 Key 放进 `provision` 的 `key`；错误短句就地显示。`lib/api.ts` 类型同步。
- `pkg/tui/setup.go`（订阅组已由 plan 267 按注册表渲染，本计划只加分支）：
  - `device_code` + **自动打开浏览器**：进入这一步就调用 opener 打开 `verification_uri_complete`（无则 `verification_uri`，URL 已预填 user code）；面板显示验证 URL、user code 与倒计时（`waiting · expires in 9:41`），**没有粘码输入框**；无图形环境（`DISPLAY` 与 `WAYLAND_DISPLAY` 皆空、或设了 `SSH_CONNECTION`）时不打开浏览器，同一页少一行 `Opening your browser…`，URL/code 照常显示；授权完成后由轮询自动推进，Esc 取消并丢弃 pending 会话。
  - Offers 多选（plan 267 的页面，不用改）→ `Provision`：`ErrKeyChoice` → 单选页选 Key（名称 + 尾号）后重试；`ErrNoKey` → 是/否确认「Create one key named forebrain-…?」，是则 `CreateKey: true` 重试；个人版套餐返回 `errPersonalKeyRequired` → 密码输入页（掩码）收一把 Key 后重试。确认页附两句说明：这把 Key 会出现在方舟控制台（`https://console.volcengine.com/ark/region:ark+cn-beijing/apiKey`，可查看/管理）；拒绝则不写任何模型行。

**Verify**: `cd frontend && pnpm typecheck && pnpm test` 全过；`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/ ./pkg/tui/ -run 'Subscriptions' -count=1` → ok。

### Step 5: 全量与真机清单

**Verify**: `go build ./... && FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./... -count=1` → 只有既有三条红；`cd frontend && pnpm build` → exit 0。

**手动验收清单（需要 owner 的火山账号）**：
1. 选中「火山方舟」→ 浏览器自动打开（URL 已预填 user code）→ 面板显示 URL/code/倒计时 → 浏览器确认 → 自动完成，**全程无需粘贴任何码**。
2. 列出持有的套餐与模型；Coding Plan 选 `doubao-seed-2.1-pro` → 粘一把控制台的 API Key → 表里一行、发一条消息成功。
3. 在 `SSH_CONNECTION` 或 `DISPLAY`/`WAYLAND_DISPLAY` 皆空的环境跑一遍：不打开浏览器，URL + code 照常显示，仍在任意设备确认后自动完成。
4. Agent Plan 个人版、团队席位（有就测）各一遍；团队席位不需要手填 Key。

## Test plan

Step 1–4 的 `_test.go` 与组件测试。桩一律 `httptest.Server` / 包级函数变量，**不连真网**。

## Done criteria

- [ ] `go build ./...`、`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./... -count=1`（失败清单只有既有三条红）、`cd frontend && pnpm typecheck && pnpm test && pnpm build` 全过
- [ ] `grep -rn "ListApiKeys\|GetRawApiKey\|CreateApiKey\|UpdateApiKey" pkg/` 无输出（不用没有公开出处的 action）
- [ ] `grep -rn "zcode.z.ai\|oauth/cli" pkg/subscription` 无输出
- [ ] `grep -rn "slog\." pkg/subscription/volcengine | grep -i "token\|key\|secret"` 只允许出现在描述性文案里，不允许把凭证变量作为字段值
- [ ] `grep -n "subscription/volcengine" pkg/architecture/cache_test.go` 有（`allowedSubpackage`）
- [ ] `Registry()` 依次返回 `chatgpt`、`opencode-go`、`volcengine`
- [ ] 报告里有手动验收清单的逐条结论（能做/不能做，为什么）

## STOP conditions

- 设备码流程在实测中被 signin 服务拒绝（`device_authorization` 返回 4xx）→ 停下报告 `error`/`error_description`，不要改回粘码或同机回环。
- 管控面 action 的实际响应与 Current state 的表不符（字段缺失/改名）→ 停下报告实测响应形状（**不要贴任何 Key/token 值**），不要猜字段。
- 团队席位 `GetTeamSeatApiKey` 要求「签署协议/实名」等交互 → 停下报告需要什么交互，不要自动签署。
- 需要的依赖不止标准库之外的既有依赖 → 停下。

## Maintenance notes

- 只有**有登录流程**的订阅进注册表（owner 规则）；火山是第三个，也是第一个「登录与 Key 分离」的订阅。
- 个人版 Key 永远是用户手填（公开 API 没有读取它的合法途径）。若将来火山开放套餐 Key 的公开接口，加一个 `RefreshKey` 实现即可，界面零改动。
- 团队席位的档位值（`BizInfo`）以接口原样为准，不要硬编码 Agent Plan 企业版档位名。
- 登录状态仍是每个网关副本的内存态（与 ChatGPT/OpenCode 相同）；多副本部署下轮询必须打到同一副本——既有约束，本计划不改变。
