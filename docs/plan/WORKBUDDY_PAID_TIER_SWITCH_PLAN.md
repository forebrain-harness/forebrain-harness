# Plan: WorkBuddy 免费/付费档合成一行，并移植官方客户端的切换语义（TUI + web）

> **执行者须知**：按步骤执行，每步跑验证命令并对照预期输出；遇到 “STOP conditions” 立即停下报告，
> 不要即兴改方案。owner 纪律：**批准前不改任何源码；批准后在当前源码树直接实施，不建 worktree、不 commit**；
> 改动留工作区由 owner 审阅。同树有并行会话（`pkg/tui/render.go`、`pkg/tui/reducer.go` 子代理卡片；
> `pkg/turn/model.go`、`pkg/assembly` 的 `/status` 行；`pkg/tui` exit_plan 审批面板 filter）——改任何
> 不属于本计划的文件前先 `peer_list`/`peer_send` 对齐。

## Status

- **Priority**: P1（owner 报告的交互缺陷 + owner 指定的语义移植）
- **Effort**: L（插件目录/状态机/请求路径 + TUI 与 web 两个 surface + 事件与运行时状态）
- **Risk**: MED（涉及模型发送 id 与计费档；只读流量与 fake 验收可证的部分都必须在测试里钉住）
- **Depends on**: none
- **Category**: bug + direction（switching semantics port）
- **Planned at**: commit `3dff52cd`，2026-11-15。基线时 `git status -- plugins/subscriptions/workbuddy`
  干净；共享树里其它 agent 的 WIP **不属于** drift 范围。
- **计划文件位置**：本文件先落在会话计划目录；批准后复制到
  `docs/plan/WORKBUDDY_PAID_TIER_SWITCH_PLAN.md`（`docs/plan` 已 gitignore、不入库）并回填验收记录。
  旧计划 `workbuddy-free-tier-model-names.md` 被本计划取代（保留在计划目录作历史，不删）。

## Why this matters

TUI 的 `/connect` → WorkBuddy 模型勾选页里，`hy4free`/`hy4std` 两行都叫 `Hy4`、`hy3`/`hy3-x` 两行都叫
`Hy3`，endpoint 列也相同，用户无法分辨（owner 截图即此现象，原话：“实际有一个 hy4 free 和 hy3 free，
但是模型列表里无法区分”）。

owner 裁决（2026-11-15，本节原话）：**「付费档保持 Hy4/Hy3」「按厂商 allowPaidSwitch 合成一行加切换」
「逆向 workbuddy，确认 workbuddy 的切换语义，并严格对齐和移植到 forebrain harness」「TUI + web 一起做」**。
所以本计划不是“给名字加后缀”，而是：按官方客户端的真实语义——**一对计费档只显示一行（免费档），
额度用尽（业务码 6004）时给出“改用付费档 / 关闭”的选择，选择后把同一行的请求切到付费 id，
额度重置后自动回到免费 id**——并把这两个 surface 一起对齐。

## Current state

### 1. 缺陷证据（现场，2026-11-15 只读 GET `https://www.workbuddy.cn/v3/config`）

用本机已保存登录按插件自己的头集合发只读 GET（无 chat 请求、不消耗配额），64022 字节，其中：

| id | name | credits |
| :-- | :-- | :-- |
| `hy4free` | `Hy4` | `x0.00` |
| `hy4std` | `Hy4` | `x0.29` |
| `hy3` | `Hy3` | `x0.00` |
| `hy3-x` | `Hy3` | `x0.05` |

`data.productFeaturesConfig.ModelRateLimitCap.lines`（厂商原文）：

```json
{"lines": [
  {"allowPaidSwitch": true, "displayName": "Hy3", "freeId": "hy3",     "paidId": "hy3-x"},
  {"allowPaidSwitch": true, "displayName": "Hy4", "freeId": "hy4free", "paidId": "hy4std"}
]}
```

同一次应答的 `data.models` 共 **61 条**，同名行不止两对：`Hy3` 有 **4 行**（`hy3`/`hy3-b`/`hy3-c`/`hy3-x`），
`Hy4 preview` 有 **5 行**（`hy4-preview-ctrl`/`hy4-preview-exp`/`hy4preview-exp1`/`hy4preview-exp2`/`hy4-preview-x`）。
它们今天不进列表，因为 `plugins/subscriptions/workbuddy/models.go:293-315 narrowToSurface` 只取会话 agent
（`cli`，tag `model:craft`）声明的 16 个 id。**这条不变式必须写进测试**：厂商把 `hy3-b` 之类加进 agent
清单时，唯一性测试必须变红（否则用户又看到两行同名）。

会话 agent 的 16 个 id 顺序：`hy4free, hy4std, hy3, hy3-x, deepseek-v4.1-flash, glm-5.3,
glm-5.3-flash, glm-5.2, space-bunny, minimax-m3.1-flash, kimi-k3.1-pro, kimi-k3-1,
kimi-k2.8-preview, deepseek-v4-pro, step-5-preview, mimo-v2.6-pro`（`auto` 由插件隐藏）。

账号：`GET /v2/plugin/account`（只读）返回 `type: "personal"`、无 `enterpriseId`（与切换资格判定相关，见 §3.1）。

### 2. 缺陷代码链（file:line，当前源码）

- `plugins/subscriptions/workbuddy/models_static.go:6-43` — 离线兜底表四行同名（`hy4free`/`hy4std` 均
  `Hy4`，`hy3`/`hy3-x` 均 `Hy3`）；`plugin.go:60` 在 `init()` 里 `installBuiltinModels(chatModels(staticWorkBuddyModels()))`。
- `plugins/subscriptions/workbuddy/models.go:221-257 mergeLiveModel` — 直播清单 `name` 非空即采用。
- `plugins/subscriptions/workbuddy/models.go:338-357 fetchAccountModels` — 只解 `data.models` + `data.agents`，
  **丢弃** `productFeaturesConfig.ModelRateLimitCap`（根因之一）。
- `plugins/subscriptions/workbuddy/plugin.go:204-228 Offers` — `subscription.Offer{Model: record.ID, Name: record.Name,
  BaseURL: workBuddyBaseURL+"/v2"}`：所有 offer 的 endpoint 相同。
- `pkg/tui/setup.go:1372-1379 pickOffers`（及 `:1852-1860 pickOfferSubagents`）— 一行只有 `Label=offerDisplayName`
  与 `Description=ModelEndpointLabel`，两项都相同 ⇒ 无法区分。
- `pkg/llm/catalog.go:933-950 ListingDisplayName/FormatProviderModelDisplay` — `/model`、footer、plan-review、
  web chip 都读 builtin 表 `Name`（`pkg/turn/models.go:736-745`）。
- `plugins/subscriptions/workbuddy/transport.go:56-107` — 模型请求的唯一出口（`RoundTrip`→`send`），
  体内 `model` 与 `X-Model-ID` 都来自请求体（`:61 modelIDFromBody`、`:183-185`）。
- `plugins/subscriptions/workbuddy/plugin.go:73-88 newWorkBuddyLLM` — `openai.NewCompatLLMWithTransport(...)`，
  `Transport{Home: root}`（无 DB）。

### 3. 官方客户端切换语义（逆向证据，`/Applications/WorkBuddy.app/Contents/Resources/app.asar`，只读）

| 语义 | 客户端实现（`app.asar` 内联 JS，按 needle 定位） |
| :-- | :-- |
| 一对档只显示一行 | `filterVisiblePaidLineModels(models, {eligible, lines})`：eligible 时**过滤掉 paidId 行**；`mergePaidLines` 只把 freeId 行留下 |
| 两个 id 都在目录里 | `injectPaidLineModels`：缺失时把 `freeId`/`paidId` 补进 `models` 与 agent 列表（补在 `hy3-preview-agent` 之后或末尾） |
| 资格 | `isEligiblePaidLineUser(account)` = `(type==="personal" || type==="pro") && !denyList.includes(enterpriseId)` |
| 额度用尽的判定 | `isConversationRateLimitCapError(e)` = `e.terminal?.bizCode===6004 || e.code==="6004" || Number(e.code)===6004` |
| 重置时间 | `getConversationErrorResetSource` 取 `terminal.details/message/message` 文本，`parseResetAt` 用正则 `(\d{4}-\d{2}-\d{2})[ T](\d{2}:\d{2}:\d{2})(?: UTC+8)?` 并按 **UTC+8** 解释 |
| 每账号状态 | 存储键 `model-rate-limit-cap`，键 `${uid}:${enterpriseId}`，每 freeId 一条 `{resetAt, optedIn}`；`markCapHit(account,freeId,resetAt)`、`markPaidOptIn`、`isCapHit`（`resetAt` 过期即删除）、`resolveEffectiveCapHit = 持久命中 \|\| live 命中`、`setLiveBannerCapHit`（无 resetAt 时的会话态） |
| 用尽后的界面 | `RateLimitInfoBanner{title, effectiveAllowPaidSwitch, paidLabel, autoLabel, paidHint, onPaid, onAuto, onDismiss}`：标题含模型名与重置时间（`error.rateLimitCap.freeTitle`）；「usePaid」= `markPaidOptIn` + `setModel(paidId)` + 重发 `error.rateLimitCap.continuePrompt`；「switchAuto」= 换到 Auto 兜底模型并重发；另有 dismiss |
| 运行时发送 id | `resolvePaidLineRuntimeId(selectedId, state)`：选中 paidId 且 `isLineInWindow(freeId)===false`（窗口已重新开放）时，**发 freeId**；否则按选中 id 发；`onChange` 选中 freeId 行时同样经它解析 |
| 资格不足（`type` 非 personal/pro） | `filterVisiblePaidLineModels` 不过滤 ⇒ 两行都显示（官方 picker 用 credits 区分，我们无该列） |

客户端内置兜底声明（`ModelRateLimitCap` 缺失时使用）：`[{freeId:"hy3",paidId:"hy3-x"},{freeId:"hy4",paidId:"hy4-x"}]`。

### 4. harness 侧已有的可复用机制（不要新造同类）

- **额度错误说明**：`pkg/llm/explain.go` 的 `Explain(err)` + `llm.RateLimit`（`pkg/llm/ratelimit.go:27-46`）
  已有 `Quota`/`ResetAt`/`RetryAfter`；`isQuotaReason`（`:340`）已认 `quota/credit/使用上限…`；
  `resetAtFromProse`（`:260`）能从散文里解出时间戳（带时区时按 RFC3339）。
  **注意**：`ParseRateLimit` 只在 HTTP 429 生效（`:71-74`），WorkBuddy 的 6004 不是 429，所以今天
  额度语义整条链路都触发不到——这是本次要补的缝。
- **自动续跑**：`pkg/turn/submit.go:703-747 planFor/continuationFor` + `AutoContinuePlan`（`:373-435`）、
  `pkg/turn` 事件 `AutoContinueScheduled`，TUI 侧 `pkg/tui/notify.go:2395-2585`（输入框上方的
  notice + 取消动作），web 侧 `frontend/src/components/chat/AutoContinueBanner.vue`（同一个运行时状态、
  两个 surface 各画一次、动作回调用运行时）。**这就是“额度用尽提示 + 动作 + 重发”的既有范式**。
- **模型行/显示名**：`pkg/subscription.Offer`（`pkg/subscription/registry.go:63-104`）与
  `pkg/llm.SetBuiltinModels`/`ListingDisplayName`。
- **网关 offers 路由**：`pkg/gateway/subscriptions_api.go:157-208`（`GET /subscriptions/:id/offers`、
  `POST /subscriptions/:id/provision`）；web 端 `frontend/src/components/models/SubscriptionLoginModal.vue`
  （`offers` 列表 + 勾选 + provision）。

### 5. 仓库约定

- 错误分类与“额度用尽”的词汇都在 `pkg/llm`（Layer 0；仓库规则在 `FOREBRAIN.md` 的 Architecture/Layering
  段：`pkg/architecture` 测试强制 `pkg/*` 的分层、`pkg/llm` 根不 import provider SDK，适配器只在
  `pkg/llm/<provider>`），provider 特有形状只能留在插件里——插件不在 `pkg/` 的分层表内，这条是仓库约定
  而非某个测试强制。
- 显示名只在 builtin 表一处（`pkg/llm/catalog.go:928-950` 注释）：surface 不做去重/拼接。
- 生成文件必须由生成器产出：`plugins/subscriptions/workbuddy/gen_models_static.go`（`//go:build ignore`，
  镜像 decode 结构，不能 import 插件包）。今天用 live 配置重跑生成器与入库表**逐字节一致**（已实测）。
- TUI 文案英文、web 文案走 i18n（`frontend/src/locales/index.ts`）。

## 修复设计

### 3.1 目录与选项：一对计费档只出一行（对齐 `filterVisiblePaidLineModels` / `injectPaidLineModels`）

- **资格（按客户端原文）**：`isEligiblePaidLineUser` = `type ∈ {personal, pro}` **且** `enterpriseId` 不在
  客户端的 `denyList`。`denyList` 是客户端里的常量数组、无公开来源，本计划**不移植**（写进注释与
  §7 的差异表）：只按 `type` 判定（`personal`/`pro` 即为资格内；其它值、包括企业账号常见的非
  personal/pro 值，即为资格外）。
  **`type` 取不到时**：记一条 warn 日志并按**资格外**继续（两行都出、名字已可辨），**不 STOP**；
  只有“拿到了 `personal`/`pro` 却仍出两行”这种与预期相反的情况才 STOP（见 STOP#8）。
- **老凭据补 type**：`auth.go:290-300` 的 `account.Type` 今天被 `applyTo` 丢掉，而且已保存的
  `~/.forebrain/workbuddy.json` **没有 type 字段**（已核实键集合：uid/name/domain/access_token/
  refresh_token/expires_at/refresh_expires_at/session_state/login_method）。若“未知 ⇒ 按资格外”，
  owner 升级后仍会看到 `Hy4 Free`/`Hy4` 两行——缺陷原地不动。所以：`Credentials` 增 `AccountType`，
  `Probe`/`FetchModels` 发现 `AccountType == ""` 时**补一次只读 `GET /v2/plugin/account`** 并
  `saveCredentials` 持久化（走凭据文件的同一把锁）；仍取不到按上面那条“记日志、按资格外继续”；
  并加用例 `TestLegacyCredentialGetsAccountType`（老文件缺 type → 补上 → 变成资格内）。
- `FetchModels` **仍返回两个 id 的记录**（`hy4free` 与 `hy4std` 的能力行都要进 builtin 表，`/model`、
  footer、capability 查询才不失明），并额外返回声明（pairs）供 `Offers` 合并。
- **命名规则（只有一条，全表面同字面）**：`freeId` 的显示名 = 厂商 `displayName` + ` Free`（幂等：已以
  ` Free` 结尾则不追加）；`paidId` 保持厂商 `displayName`。所以 `hy4free→Hy4 Free`、`hy4std→Hy4`、
  `hy3→Hy3 Free`、`hy3-x→Hy3`，在 builtin 表、`Offer.Name`、`/model`、footer、web chip 里完全一致。
  与官方客户端的**唯一措辞差异**（写进 `Offer`/`models.go` 注释）：官方单行标签是 `Hy4`，因为它同一行
  右侧有 credits 列（`x0.00`/`x0.29`）标明档位；我们的 picker 没有 credits 列，资格外账号又是两行，
  所以档位必须落在名字上。这不是发明词汇：owner 原话就是 “hy4 free”，仓库参考目录已有 `Hy3 Free`
  （`assets/models.json:27433`）。换成付费档后该行仍叫 `Hy4 Free`，当前档位由 §3.5 的 notice 说明
  （官方同样不换行标签）。
- `Offers`：
  - 资格内：每对只产 **一个** offer —— `Model`=`freeId`、`Name`=`displayName + " Free"`（如 `Hy4 Free`），
    并带 `PaidModel`=`paidId`、`AllowPaidSwitch`=`line.allowPaidSwitch`；paidId 那一行的 offer 不产出。
  - 资格外：两行都产出（客户端同样不过滤）：`Hy4 Free` 与 `Hy4` 两两不同，我们的 picker 可辨。
- 静态兜底表：`models_static.go` 的 free 行名同样带 ` Free`（生成器同规则、幂等），资格外两行在任何
  时刻都可辨。

- `pkg/subscription.Offer` 追加两个字段（加性，其它订阅零值不受影响）：

```go
	// PaidModel is the paid twin of a model the product sells at two rates
	// (a free tier and its paid twin): "" for a plain model. A surface shows
	// the pair as one row and never provisions PaidModel — requests reach it
	// only through the cap switch (PaidModel/AllowPaidSwitch).
	PaidModel string
	// AllowPaidSwitch is the product's own flag for that pair: requests may
	// fall back to PaidModel once the free tier's allowance is spent.
	AllowPaidSwitch bool
```

### 3.2 每账号额度状态（对齐 `model-rate-limit-cap`）

- 插件私有状态文件 `<home>/workbuddy-rate-limits.json`（与凭据文件同目录；不是 app 状态库——插件工厂
  只拿到 `home.Root()`，凭据也一直走文件；理由写进注释）。结构：

```json
{"<uid>:<enterpriseId>": {"hy4free": {"resetAt": 1763... , "optedIn": true}}}
```

- 与客户端同名语义的四个函数（`models.go` 旁新文件 `ratelimit.go`）：`markCapHit(home, account, freeID, resetAt)`、
  `markPaidOptIn(...)`、`capHitResetAt(...)`（过期即删）、`paidOptedIn(...)`（过期即删）；读写用
  `llm.CredentialMutex(file)` 同款按路径的互斥（并发/多进程安全，沿用凭据文件的既有纪律）。
- 无 `resetAt` 的命中（客户端 `setLiveBannerCapHit`）**只能活在会话里**：进程内 map，**不落盘**；
  用户仍可“改用付费档”，但那只记在这张 map 里（进程结束即失效），**不写持久 opt-in**——持久 opt-in
  要求有效 `resetAt`（客户端 `markPaidOptIn` 同款）。

### 3.3 业务码 6004 的识别（对齐 `isConversationRateLimitCapError`；形状以 Step 0b 为准）

已证事实：`go-openai` 的 `APIError.Code` 对**数字** code 归一为空（`pkg/llm/openai/error.go:74 codeString`），
`Message` 只取 `message` 字段；而 WorkBuddy 的失败应答很可能用 `msg`/`code` 命名（Step 0b 定形状）。
⇒ 不管最终形状是 `{code,msg}`、`{error:{code,…}}` 还是 SSE 帧，**分类都必须落在插件自己看得见原始
响应的地方：`Transport.RoundTrip`**；上层的 `llm` 分类只吃插件归一化后的形状。

- 在 `Transport.RoundTrip`（`transport.go:56-80`）拿到响应后，**闸按 Step 0b 证到的实际状态码定**：
  客户端 `extractRateLimitBizCode` 不按状态码取样（`parseResponseBodyObject` 直接解析体），所以闸不能
  只写 `status >= 400`。规则：**响应是 JSON（`Content-Type` 非 `text/event-stream`）时，无论 2xx 还是
  4xx，只要体里按 Step 0b 的路径读出的业务码是 6004 就分类**；SSE 流**不预读**（预读会破坏协议），
  若 Step 0b 证实错误只出现在 SSE 帧里，则按 STOP#3 停下重定方案。读 body 用常量上限
  （新增 `maxErrorBody = 64 << 10`，超限按“形状不可读”处理，绝不整包读内存），读后把
  `resp.Body` 换回可重放 reader，内容不丢。
- 当业务码为 **6004** 且请求模型属于声明中的 `freeId`：
  - 从 Step 0b 证到的文本字段解析重置时间（同客户端正则、按 **UTC+8** 解释），
  - `markCapHit(..., resetAt)`（有 resetAt）或标 live 命中（无），
  - **把响应体改写成 OpenAI 形状**（状态码保留原值），让上层可分类且用户读得到原话：
    `{"error":{"message":"<原话 + 归一化时间戳>","type":"quota_exhausted","code":"6004"}}`；
    时间戳归一化为 `YYYY-MM-DD HH:MM:SS+08:00`，这是 `pkg/llm` 的
    `resetAtFromProse`（`pkg/llm/ratelimit.go:260-295`）能按 RFC3339 解析的形式。
- 非模型请求、成功响应（不含 6004）、其它状态码：字节不变。
- `pkg/llm/explain.go` 增一个 provider 无关分支：`APIError` 的 `Code`/`Type` 命中“额度已用尽”词汇
  （`isQuotaReason`）且散文里有未来时间戳 ⇒ 归类为**新的** `ExplainAllowanceSpent`（不是
  `ExplainRateLimitQuota`），字段 `ResetAt`/`RetryAfter` 照填。新码的理由：WorkBuddy 的语义是“给出换档选择”，
  不能让现有 auto-continue（`pkg/turn/submit.go:727-731` 只认 quota/throttle）把它当成“等重置再自动重发”。
  只在 `Code/Type` 命中额度词汇时生效，其它 provider 的 400 不受影响；`isQuotaReason` 已含 `quota`，
  `type:"quota_exhausted"` 天然命中。

  （备选被否决：把 6004 响应改写成 429。那会让 surface 报“429”，并把 WorkBuddy 额度错误喂给 auto-continue，
  与官方“给选择、不干等”的语义相反。）

### 3.4 发送 id 的运行时解析（对齐 `resolvePaidLineRuntimeId`）

- 客户端的三个前置条件必须一起移植（缺一不切）：
  1. 该 id 属于声明中的 `freeId`；
  2. `line.allowPaidSwitch === true`；
  3. 目录里确实有 `paidId`（客户端 `catalogHasPaid(sourceHasPaid, paidId)`；我们的目录即 `FetchModels`
     返回的记录集）。
- `Transport` 在发请求前解析“有效发送 id”：
  `effective := freeID`；若上面三条成立且 `paidSwitchActive(account, freeID)` —— 即
  **持久 opt-in 未过期 或 进程内 live opt-in**（客户端 `resolveEffectiveCapHit = isCapHit \|\| liveHit`）
  —— ⇒ `effective = paidId`；然后**只改写请求体里的 `"model"` 与随之而来的 `X-Model-ID`**（`disguiseHeaders`
  仍从请求体取值，改成用解析后的 id），body 的其余字段保持字节不变（`map[string]json.RawMessage`
  只替换 `model`）。**只查持久状态是不够的**：无 `resetAt` 的匹配只有 live 状态，漏了它 §3.6 的
  “无 resetAt 仍可 Use paid”那一行就是空转。
- **热路径**：每个模型请求都读状态文件会白白付 I/O。状态按 `(path, mtime, size)` 缓存一份解析结果，
  写路径（`markCapHit`/`markPaidOptIn`）在持锁时更新缓存；命中则零文件读（与凭据文件的
  `llm.CredentialMutex(path)` 同一把按路径的锁，跨进程一致）。
- 反方向（客户端 `paid→free`：选中 paidId 且窗口重新开放时发 freeId）在**资格内账号不适用**——付费 id
  根本不进 picker；资格外账号没有 opt-in 语义。**不实现，写进 Maintenance**（不写不可达代码）。
- 重置后自动回免费：状态里 `resetAt` 过期即删（§3.2），`effective` 自然回落 `freeID`。

### 3.5 用尽后的选择界面（对齐 `RateLimitInfoBanner`，TUI + web 同一运行时状态）

**为什么不用“让插件的 LLM 实现一个接口、turn 断言它”**：已核实 `pkg/turn` 不持有 `llm.LLM`
（`grep -rn "llm.LLM" pkg/turn` 无命中），LLM 由 `pkg/run` 构造并被装饰器包在结构体字段里
（`pkg/run/skills.go:41-42 inner llm.LLM`），装饰器不转发可选接口——插件实现的接口到不了 turn。
所以走本仓既有的注册表先例（`RegisterProviderFactory`，`pkg/llm/llm.go:560`；`SetBuiltinModels`，
`catalog.go:771`）：

- `pkg/llm/paid_tier.go`（新增）：

```go
// PaidTierError is one failed request a provider may satisfy again by switching
// the same model row to its paid tier: the pair, the display name a surface
// shows, and when the free allowance returns (zero when the product named none).
// It carries provider and model itself because pkg/turn holds no llm.LLM and
// llm.APIError carries no model.
type PaidTierError struct {
	Provider    string
	FreeModel   string
	PaidModel   string
	DisplayName string
	ResetAt     time.Time
	Err         error // the underlying APIError, for the surfaces' own text
}

func (e *PaidTierError) Error() string { return e.Err.Error() }
func (e *PaidTierError) Unwrap() error { return e.Err }

// PaidTier registers what a provider does with the user's choice.
type PaidTier interface {
	// UsePaidTier records the opt-in for the pair's free model. A valid ResetAt
	// persists it until that time; ResetAt zero keeps it in this process only
	// (the product named no reset, so nothing may be written to disk).
	// Idempotent.
	UsePaidTier(ctx context.Context, freeModel string, resetAt time.Time) error
}

func RegisterPaidTier(provider string, tier PaidTier)
func UsePaidTier(ctx context.Context, provider, freeModel string, resetAt time.Time) error
```

- 插件侧：`newWorkBuddyLLM` 的装饰器把 `*llm.APIError` 映射成 `*llm.PaidTierError`（条件：Step 0b 证到的
  6004 + 请求模型是声明中的 `freeId` + `allowPaidSwitch` + `paidId` 在目录里）；注册
  `llm.RegisterPaidTier("workbuddy", …)` 放在第一次构造 LLM 时（拿到 `home.Root()`，不在 `init()` 读 home）。
- `pkg/turn`：失败 turn 的错误链里 `errors.As` 命中 `*llm.PaidTierError` 时，按 auto-continue 的范式发布
  事件 `RunEventPaidTierOffered`（payload：`SessionID/RunID/Provider/FreeModel/PaidModel/DisplayName/ResetAt`），
  并记一条待决选择（per session，内存态）。两个入口：
  - `UsePaidTier(sessionID)`：`llm.UsePaidTier(provider, freeModel, resetAt)` → 走既有 continuation 路径重发上一次
    输入（复用 `pkg/turn/submit.go:703-747` 的 fire 机制与同一提示语常量），
  - `DismissPaidTierOffer(sessionID)`：清掉待决选择。
  与 auto-continue 一样按 sessionID 定位；`resetAt` 到点后待决选择与 opt-in 都自然过期。**注意**：命中
  `PaidTierError` 时**不要**再走 `autoContinuer`（新码不属于 quota/throttle，`continuationFor` 的 switch
  天然排除——加用例钉住）。
- **TUI**（`pkg/tui/notify.go`，照 `:2395-2585` 的 notice 范式）：输入框上方一行
  `Free tier spent until 2026-11-16 00:00:00 (UTC+8). Ctrl-P use paid (Hy4) · Esc dismiss`。
  键位用**非文本键 `Ctrl+P`**（裸 `p` 与打字抢键）；`Esc` 关闭；**打字不关闭 offer**（与会自动续跑的
  notice 不同，注释写明 + 加用例）。只在会话自己的视图显示（`renderer.ActiveView()` 为空）。
- **web**：照 auto-continue 的既有接线——`pkg/gateway/api_extra.go:386-387` 的 `api.Get/Delete("/auto-continue")`
  与 `pkg/gateway/run_control.go:1366` 的 WS op `cancel_auto_continue` + ack。新增：
  `api.Get("/paid-tier-offer", s.handlePaidTierOffer)`（`?session_id=`，**照 `handleAutoContinue`
  （`run_control.go:1856-1896`）取 query 并调 `s.sessionOwned(ctx, sid)`，未拥有返回 404**）与
  `api.Post("/paid-tier-offer?session_id=…&action=use|dismiss", s.handlePaidTierOffer)`（参数同样走 query，
  与先例一致，不从 body 取）；WS op `use_paid_tier` / `dismiss_paid_tier_offer` 与对应 ack（同一 op 表）；
  组件 `frontend/src/components/chat/PaidTierBanner.vue` 两个按钮「Use paid model / Dismiss」，
  i18n 键 `chat.paidTier.*`（en + zh）。**`pkg/gateway` 下不得新增生产文件**（文件数已顶格，
  见 Commands 表的 package-graph 检查）——handler 放进既有 `subscriptions_api.go` 或 `run_control.go`。
- **不移植官方 `switchAuto`（切到 Auto 兜底）**：本仓 2026-11-15 已裁决 `auto` 不进任何 surface
  （`models.go:86-88 routerModelID`），且 `auto` 的 chat 行为未验证。差异写进 Scope，等 owner 需要再单开计划。

### 3.6 行为变化矩阵

| 场景 | 旧行为 | 新行为 | 刻意修订 |
| :-- | :-- | :-- | :-- |
| 资格内账号 `/connect` 勾选页 | `Hy4`/`Hy4`/`Hy3`/`Hy3` 四行 | `Hy4 Free`/`Hy3 Free` 两行（免费档，带 `PaidModel` 元数据） | 是（对齐官方） |
| 资格内 `/model` | 两行 `workbuddy/Hy4`（endpoint 相同） | 一行 `workbuddy/Hy4 Free` | 是 |
| `type` 非 personal/pro | 两行同名 | 两行 `Hy4 Free` / `Hy4`（可辨） | 是（本仓适配） |
| footer / web chip | `workbuddy/Hy4` | 所选行名字（`Hy4 Free`） | 是 |
| 免费额度用尽（业务码 6004） | 泛化报错，无分档信息 | 错误带官方原话与重置时间；输入框上方/聊天页出现换档选择 | 是（对齐官方） |
| 选择「Use paid」 | — | 记录 opt-in（到 resetAt 为止）+ 重发上次输入；之后请求发 `paidId` | 是 |
| `resetAt` 到期 | — | opt-in 过期删除，请求自动回到 `freeId` | 是 |
| 无 resetAt 的 6004 | — | 仍给“Use paid / Dismiss”，但 opt-in 只活在本次进程（不写盘） | 是（对齐官方 live 态） |
| `type` 取不到（老凭据且账号接口失败） | — | 记 warn，按资格外：两行都出（`Hy4 Free`/`Hy4`） | 是（本仓适配） |
| 其它 provider 的 400/429 | — | 不变（新分支只在 `Code/Type` 命中额度词汇时生效） | 否 |
| wire id、endpoint、窗口、思考档位、images | — | 不变 | 否 |
| auto-continue（等重置自动重发） | 只对 429 quota/throttle | 对 WorkBuddy 6004 **不**触发 | 是（对齐官方） |

## Commands you will need

| 用途 | 命令 | 成功预期 |
| :-- | :-- | :-- |
| 构建 | `export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"; CGO_ENABLED=1 go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain` | exit 0 |
| 插件测试 | `CGO_ENABLED=1 go test -tags fts5 ./plugins/subscriptions/workbuddy -count=1` | 全 PASS |
| 受影响包 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/llm ./pkg/turn ./pkg/run ./pkg/subscription ./pkg/gateway ./pkg/tui -count=1` | 全 PASS |
| vet / 格式 | `CGO_ENABLED=1 go vet -tags fts5 ./plugins/... ./pkg/...`；`gofmt -l cmd pkg third_party plugins` | 无输出 |
| 前端 | `cd frontend && pnpm typecheck && pnpm test` | 全 PASS |
| web e2e（相关 spec） | `scripts/acceptance/web_e2e.sh`（按 skill `run-forebrain` 起 fake 网关，只跑相关 spec） | PASS |
| 生成器重跑 | `go run -tags fts5 plugins/subscriptions/workbuddy/gen_models_static.go /tmp/wb_product_config.json`（**必须在仓库根目录执行**；`outputPath` 是相对 CWD 的常量 `gen_models_static.go:32`） | `wrote …: 16 models` |
| 包图/文件预算（CI 前必跑，`FOREBRAIN.md:140`） | `scripts/package-graph.sh` | exit 0；`pkg/architecture/testdata/graph.json` 不漂移 |
| 生产文件预算 | `grep -c "^func\|^type" /dev/null` 不用；直接看 `pkg/architecture/cache_test.go:1541` `maxProductionFilesPerPackage = 34` | `pkg/gateway` 已 34（顶格）、`pkg/llm` 32、`pkg/tui` 31 ⇒ **`pkg/gateway` 下新增生产文件会被门禁拒绝** |

## Scope

**In scope**
- `plugins/subscriptions/workbuddy/{plugin.go,models.go,transport.go,auth.go,gen_models_static.go,models_static.go,workbuddy_test.go}`
- `plugins/subscriptions/workbuddy/ratelimit.go`（新增：状态文件 + 6004/重置解析 + 有效 id 解析；与客户端同名函数）
- `pkg/subscription/registry.go`（`Offer` 两个加性字段）
- `pkg/llm/{error.go,explain.go,paid_tier.go}`（`PaidTierError`/`PaidTier` 注册表/`ExplainAllowanceSpent`；
  `paid_tier.go` 新增）
- `pkg/turn/*`（待决选择的状态、事件、`UsePaidTier`/`DismissPaidTierOffer`、重发走既有 continuation 路径）
- `pkg/event/*`（新事件 payload：`RunEventPaidTierOffered`）
- `pkg/tui/{notify.go,reducer.go,render.go}`（notice 行；**只加 notice 与它的两行渲染/键，不改子代理卡片，
  与并行会话按 peer 对齐**）
- `pkg/gateway/{api_extra.go,run_control.go,subscriptions_api.go}`（`/paid-tier-offer` 的 GET/POST、
  WS op `use_paid_tier`/`dismiss_paid_tier_offer` + ack、offers 的 `PaidModel/AllowPaidSwitch` 字段透出）
- `frontend/src/components/chat/PaidTierBanner.vue`（新增）+ 其挂载点 + `frontend/src/locales/index.ts`
- 计划副本 `docs/plan/WORKBUDDY_PAID_TIER_SWITCH_PLAN.md`（gitignored）

**Out of scope**
- `auto` 路由模型与官方 `switchAuto` 动作（owner 2026-11-15 已裁决 auto 不进 surface）。
- 官方 `paid→free` 反方向解析（资格内不可达，§3.4 已说明）。
- 客户端 `denyList`（无公开来源；我们只按 `type` + `enterpriseId` 判定资格，差异写进注释）。
- `plugins/subscriptions/qodercn/**`：它的静态名表是 `DisplayName`（`qodercn/models.go:118-145`），
  名字本身唯一、live listing 无同名证据；不并入本计划。
- `assets/models.json`（models.dev 参考目录，无 workbuddy 行）。
- 任何凭据、额度、真实 chat 请求：验收只用只读 `GET /v3/config`、`GET /v2/plugin/account` 与 fake/httptest。
  **唯一例外**：Step 0b 第 2 步那一次“必然被拒、不产生 completion、不消耗配额”的探针——owner 在
  2026-11-15 的计划问答里已**明确批准**（选项原文：不存在的模型 id、`stream:true`、`max_tokens:8`，
  只打印 `status`/`Content-Type`/响应体用于取证）；不得用于任何计费判断，也不得重放。
- 不改写 `docs/plan/WORKBUDDY_MODEL_CATALOG_PLAN.md` 历史记录。

## Steps

### Step 0a（先做，只读核对；不通过就 STOP）

把本计划依赖的 6 个事实在当前源码上再核一遍（计划写于 `3dff52cd`，共享树可能有并行改动）：

```bash
grep -n "func (t \*Transport) RoundTrip" -A 30 plugins/subscriptions/workbuddy/transport.go
grep -n "func modelIDFromBody" -A 12 plugins/subscriptions/workbuddy/transport.go
grep -n "func Explain" -A 40 pkg/llm/explain.go
grep -n "func (a \*autoContinuer) continuationFor" -A 30 pkg/turn/submit.go
grep -n "case turn.SignInUsable" -A 6 pkg/tui/setup.go
grep -n "func (t account) applyTo" -A 10 plugins/subscriptions/workbuddy/auth.go
```

预期：与 §2/§4 的摘录内容一致。任何一处**内容**不同（不是行号漂移）→ 停下报告。

**Verify**: 上述命令输出与本文摘录一致；`git status --porcelain -- plugins/subscriptions/workbuddy pkg/llm pkg/turn pkg/event pkg/tui pkg/gateway frontend/src` 只列出并行会话的文件（记录它们，便于后面区分归属）。

### Step 0b（诊断 6004 的真实线上信封；**这是本计划的前提，不许跳过**）

计划写到这里时，6004 到底以什么形状从模型网关返回**尚未证实**：

- 官方客户端的 `isConversationRateLimitCapError` 读的是 `e.terminal.bizCode`，而 `terminal` 是
  daemon 把一次失败的模型请求装配出来的对象（`app.asar` 里 `classifyErrorAsRequestError(...)`、
  `bizCode: n.data?.code`），**不是**它直接读网关响应体的地方；
- 已安装客户端的 CLI 源码是解包的：`/Applications/WorkBuddy.app/Contents/Resources/app.asar.unpacked/cli/dist/codebuddy-lite-wb.mjs`
  里能搜到 `n.data?.code`（`bizCode=${n.data?.code}` 的日志行），但 body 的完整形状仍要看 `classifyErrorAsRequestError`
  的解析分支；
- 我们的插件走标准 OpenAI 协议（`transport.go:1-5`），可能的真实形状（Step 0b 要逐条排除/确认）：
  `{code:6004,msg:"…"}`、`{"error":{"code":6004,…}}`、**`{"error":{"data":{"code":6004}}}`（客户端
  `extractRateLimitBizCode` 的**首选**分支 `errorDataCode`）**、或 SSE `data:` 帧里带错误对象。
  数字 code 会被 `pkg/llm/openai/error.go:74 codeString` 归一成空，所以还不能靠上层解析。

**做法（按顺序，前一步够用就停）**：

1. 只读解剖 CLI（针脚按客户端真正的解析链取，不是 `classifyErrorAsRequestError` 的名字）：
   ```bash
   python3 - <<'PY'
   p='/Applications/WorkBuddy.app/Contents/Resources/app.asar.unpacked/cli/dist/codebuddy-lite-wb.mjs'
   d=open(p,'rb').read()
   for needle in [b'parseResponseBodyObject', b'extractRateLimitBizCode', b'extractBizCode',
                  b'isQuotaExhaustedError', b'error.data.code']:
       i=d.find(needle)
       print('===',needle,i,'===')
       if i>0: print(d[max(0,i-2200):i+2200].decode('utf-8','replace'))
   PY
   ```
   已由 review 独立收窄的结论：CLI 的 `parseResponseBodyObject` 从**响应体**取 `code` / `error.code` /
   `error.data.code`，`extractRateLimitBizCode` 的优先级是 `errorDataCode → errorCode → code`。
   本步要把“6004 出现时到底是哪一条路径、文案字段叫什么、**HTTP 状态是什么**”写成记录。
2. 若第 1 步仍不能确定（例如 `code` 来自 SSE 事件、或 200 也可能是错误体），用 **owner 在选定本方案时
   已批准的那一次探针**：用插件自己的 `Transport` 对 `/v2/chat/completions` 发一次**必然被拒、不产生
   completion、不消耗配额**的请求（不存在的模型 id，`stream:true`，`max_tokens:8`），打印
   `status`、`Content-Type` 与**响应体原文**（脱敏后写进记录，不写 token）。探针只为取证信封，不做修复推断。

**产出（决定 §3.3 的闸与测试 fixture）**：HTTP 状态（400？200？SSE？）、字段路径（`data.code` /
`error.code` / `error.data.code`）、文案字段名（`msg`/`message`/`details`）、以及“重置时间出现在哪一段文本”。
**用例 4 的 fixture 必须取自这一步证据**，不得沿用本计划的猜测形状。

**Verify**: 记录 `status`、`Content-Type`、字段路径与原文（脱敏）；`grep -n` 在 CLI 里给出解析分支的
位置。若两种手段都定不了形状 → **STOP**（不按猜测写分类代码）。

### Step 1：先写复现测试（改生产代码之前必须失败）

`plugins/subscriptions/workbuddy/workbuddy_test.go`：

1. `TestOffersShowOneRowPerRateLimitedPair`（复现 owner 截图场景；用 §1 的 4 个 Hy id + 2 条 lines，
   `storeCredentials(t, nil, home, freshCredentials())` 的 `freshCredentials()` 需要补 `AccountType: "personal"`）：
   - 断言 `FetchModels` 仍返回 4 条记录（能力行都在）；
   - 断言 `Offers()` 只产出 2 个 offer：`Model` 为 `hy4free`/`hy3`，`Name` 为 `Hy4 Free`/`Hy3 Free`，
     `PaidModel` 为 `hy4std`/`hy3-x`，`AllowPaidSwitch` 为 true；
   - 断言所有 offer 的 `Name` 互不相同（这就是 picker 依赖的性质）。
2. `TestOffersKeepBothRowsForANonEligibleAccount`：`freshCredentials()` 的 `AccountType` 设为资格外的值
   （如 `"enterprise"`；**不能是 `personal`**，否则按 §3.1 就是资格内）⇒ 4 个 offer，
   名字 `Hy4 Free`/`Hy4`/`Hy3 Free`/`Hy3` 两两不同（资格外适配）。
3. `TestStaticTableNamesEachFreeTierApart`：16 行静态表**无任何两行同名**，并逐 id 钉住
   `hy4free→Hy4 Free`、`hy4std→Hy4`、`hy3→Hy3 Free`、`hy3-x→Hy3`；注释写明它是
   `narrowToSurface` 不变式的守卫（厂商把更多同名 id 放进 agent 清单时它会变红）。
4. `TestTransportClassifiesCapCodeAndRecordsReset`（fixture **由 Step 0b 证据填入**——把
   `// FIXTURE FROM STEP 0b: <status>, <field path>, <text field>` 写明在测试注释里；未取证前不得编造形状）：
   - 断言状态文件里 `<uid>:` 的 `hy4free` 记下 `resetAt`（UTC+8 解释）；
   - 断言返回给上层的响应体已是 OpenAI 形状（`error.code=="6004"`、`type=="quota_exhausted"`）。
5. `TestTransportSendsPaidTwinWhileOptedIn`：先 `markPaidOptIn`，再断言发出的 body `model` 与
   `X-Model-ID` 都是 `hy4std`；把 `resetAt` 改成过去时间后，再断言回到 `hy4free`。
6. `TestExplainAllowanceSpentCarriesReset`：`llm.Explain` 对该形状返回 `ExplainAllowanceSpent` + `ResetAt`。
7. `TestLegacyCredentialGetsAccountType`：凭据文件缺 `type`（照今天的真实键集合写 fixture，**不含任何
   token 值**）→ `FetchModels` 补一次只读 `GET /v2/plugin/account`（httptest）→ 变成资格内（每对一行）。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./plugins/subscriptions/workbuddy -run 'TestOffersShowOneRowPerRateLimitedPair|TestOffersKeepBothRowsForANonEligibleAccount|TestStaticTableNamesEachFreeTierApart|TestTransportClassifiesCapCodeAndRecordsReset|TestTransportSendsPaidTwinWhileOptedIn|TestLegacyCredentialGetsAccountType' -count=1`
→ **全部失败**，且失败信息正是“两行同名 / 6004 未被识别 / 仍发 freeId / 老凭据没有资格”。把这段输出原样
记进报告（复现证据）。

### Step 2：目录与 offer 合并（§3.1）

- `auth.go`：`account` 的 `Type` 落到 `Credentials.AccountType`（跟随 `saveCredentials` 持久化）；
  `Probe`/`FetchModels` 发现 `AccountType == ""` 时**补一次只读 `GET /v2/plugin/account`** 并持久化
  （与凭据文件同一把锁）；仍取不到按资格外处理。已保存的凭据文件今天没有 type 字段，这条是必须的，
  否则 owner 升级后仍看到两行。
- `models.go`：`FetchModels` 返回 `([]workBuddyModel, []ratePair)`（`ratePair{DisplayName, FreeID, PaidID,
  AllowPaidSwitch}`，来自 live `ModelRateLimitCap.lines`，缺失时用生成的 `shippedRatePairs()`）；
  free id 的 `Name` 统一为 `displayName + " Free"`（幂等：已以 ` Free` 结尾则不加）。
- `gen_models_static.go` + 重生成 `models_static.go`：生成 `shippedRatePairs()` 与带 ` Free` 的 free 行名；
  重生成前把 `models_static.go` 备份到 `/tmp`，`diff` 必须**只有** 2 处 `Name` 与新增的 pairs 表。
- `plugin.go` `Offers`：资格内按 §3.1 合并；资格外两行都出。
- `pkg/subscription/registry.go`：`Offer` 加 `PaidModel`/`AllowPaidSwitch`（加性）。
- 资格外账号的 `hy4std` 行也必须是可 provision 的完整 offer（现有字段齐全）。

**Verify**: Step 1 的用例 1/2/3/7（老凭据补 type）通过；另加一条标签层断言（同一个 `restoreShippedTable(t)`
之后 `llm.FormatProviderModelDisplay("workbuddy","hy4free") == "workbuddy/Hy4 Free"`、
`...("hy4std") == "workbuddy/Hy4"`）——这是 `/model`、footer、web chip 的机械验证，不只靠真机抓屏；
`CGO_ENABLED=1 go test -tags fts5 ./plugins/... ./pkg/subscription ./pkg/llm -count=1` 全 PASS。

### Step 3：额度状态与 6004 分类（§3.2、§3.3）

- 新增 `plugins/subscriptions/workbuddy/ratelimit.go`：状态文件读写（`llm.CredentialMutex` 同款互斥 +
  按 `(path, mtime, size)` 的读缓存，写路径在持锁时更新缓存）、
  `markCapHit`/`markPaidOptIn`/`capHitResetAt`/`paidOptedIn`、live 命中（进程内 map）、
  `parseCapResetAt(text)`（同客户端正则、UTC+8）。
- `transport.go`：按 §3.3 的闸（JSON 响应时**不论 2xx/4xx**）读 body（`maxErrorBody = 64 << 10` 常量）、
  识别 6004、记状态、把体改写成 OpenAI 形状；其它响应字节不变。读法必须无损：只读前 `maxErrorBody`
  字节作 head，回填用 `io.MultiReader(bytes.NewReader(head), rest)`，**绝不 64KB 截断后回填**——一个
  非流式成功响应可以大于 64KB，截断回填就是损坏它。若 Step 0b 证实 2xx 也可能是错误体，才在 2xx
  路径做同样的 head 检查；否则 2xx 直通。
- `pkg/llm/explain.go` + `pkg/llm/error.go`：`ExplainAllowanceSpent` 与判定（只对 `Code/Type` 命中额度词汇
  + 散文含未来时间戳生效）。另加一条护栏用例：**含 `quota` 但 code 不是 6004** 的样本仍归既有
  `ExplainRateLimitQuota`/`ExplainBilling`（客户端把 `isQuotaExhaustedError`（码 `-32003`）单列一类，
  不能与 6004 混淆）。

**Verify**: 用例 4/6 通过（用例 4 的 fixture 来自 Step 0b 证据）；`CGO_ENABLED=1 go test -tags fts5 ./pkg/llm -count=1` 全 PASS
（含既有 `TestExplain*` 不回归）。

### Step 4：有效发送 id（§3.4）

- `transport.go`：`send` 前解析有效 id，**三条前置条件齐备才切**（freeId ∈ 声明、`allowPaidSwitch`、
  `paidId` 在目录里）+ opt-in 且 `now < resetAt` ⇒ `paidId`；只替换 body 的 `model`，`disguiseHeaders`
  用有效 id；body 其余字段字节不变。
- 注意 `Transport{Home: root}` 没有 DB：状态与凭据都按 home 读，工厂不改签名。

**Verify**: 用例 5 通过（opt-in 时发 `hy4std`、过期后发 `hy4free`、`allowPaidSwitch=false` 时不切）。

### Step 5：运行时端口与两个 surface 的选择（§3.5）

- `pkg/llm/paid_tier.go`（新增；测试落 `pkg/llm/paid_tier_test.go`，`FOREBRAIN.md:145` 要求测试文件与
  生产文件同名）：`PaidTierError` + `PaidTier` 注册表（照 `RegisterProviderFactory`）；
  `plugin.go` 的 LLM 装饰器把 `*llm.APIError` 映射成 `*llm.PaidTierError`，并在第一次构造 LLM 时
  `llm.RegisterPaidTier("workbuddy", …)`（`UsePaidTier` 记 opt-in）。
- `pkg/event`：`RunEventPaidTierOffered` payload（**不属于** `pkg/event/run_events_test.go:44-62`
  `TestCanonicalLifecycleEventNames` 的 lifecycle 清单——auto-continue 事件同样不在其中，所以不改那个测试，
  只在插件/turn 的用例里钉住新事件名）；`pkg/turn`：检测落点写在 `turnEnded`（`pkg/turn/submit.go:653-663`
  拿到的是未字符串化的原始 `err`，`errors.As` 在这里做）+ 待决选择 map + `UsePaidTier`/`DismissPaidTierOffer`，
  重发走既有 continuation 路径（**照 `pkg/turn/submit.go:703-747` 与 auto-continue 的 fire 路径**）；
  并确认这一步**不**触发 `autoContinuer`（加用例）。
- `pkg/llm/paid_tier_test.go`（新增）覆盖：`errors.As` 识别、注册表查表/未注册、零值 ResetAt 的语义。
- `pkg/tui`：notice 行 + `Ctrl+P`/`Esc`（照 `notify.go:2395-2585`），只改 notice 相关函数；
  **打字不关闭 offer**（与 auto-continue 的 cancel-on-typing 不同，加用例）。
- `pkg/gateway`：`api.Get/Post("/paid-tier-offer")` + WS op `use_paid_tier`/`dismiss_paid_tier_offer` + ack
  （照 `api_extra.go:386-387`、`run_control.go:1366` 的 auto-continue 接线）+ offers 透出
  `PaidModel/AllowPaidSwitch`。
- web：`PaidTierBanner.vue` + 挂载 + i18n 键（en/zh）；按钮分别调用 use/dismiss。

**Verify**:
`CGO_ENABLED=1 go test -tags fts5 ./pkg/llm ./pkg/turn ./pkg/run ./pkg/gateway ./pkg/tui -count=1` 全 PASS
（新增：auto-continue 不收新码、TUI 打字不关 offer、`POST /paid-tier-offer` 的两个动作与 ack）；
`cd frontend && pnpm typecheck && pnpm test` 全 PASS。

### Step 6：真机验收（fake 模型 + 只读 live 列表）

1. `CGO_ENABLED=1 go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain`。
2. **只读 live 列表**（不发 chat、不耗配额）：临时 `FOREBRAIN_HOME` + 临时项目目录，复制
   `~/.forebrain/workbuddy.json`（只复制登录），tmux 起 TUI → workspace trust → `/connect` → `WorkBuddy`
   （`Probe` 返回 `SignInUsable` 直接进勾选页）→ 抓屏断言**只有 `Hy4 Free`、`Hy3 Free` 两行**
   （加上其余 12 行），没有第二行 `Hy4`。
3. 勾选、Enter 到 Subagents 页、完成写入 → footer `workbuddy/Hy4 Free · high …`；`/model` 只有一行
   `workbuddy/Hy4 Free`。
4. **换档提示（fake）**：用本仓 fake 模型/网关配方（skill `run-forebrain`）让一次请求返回
   `{"code":6004,...}` 等价错误（若 fake 不支持自定义错误体，就问 owner 是否允许对真实网关发一次
   无效模型 id 的错误请求；不允许则本步只做单测覆盖，并在报告里声明限制）→ 断言 TUI 输入框上方出现
   `Free tier spent until … Ctrl-P use paid (Hy4) · Esc dismiss`；按 `Ctrl+P` 后请求 `model` 变 `hy4std`。
5. web：`scripts/acceptance/web_e2e.sh` 跑相关 spec（`SubscriptionLoginModal` 两行变一行、
   `PaidTierBanner` 两按钮），逐张截图留证。
6. Esc/Ctrl+C 干净退出（`EXIT_CODE=0`），清理 tmux 与临时目录。

**Verify**: 上述抓屏/截图为证；`EXIT_CODE=0`。

### Step 7：收口

计划副本复制到 `docs/plan/WORKBUDDY_PAID_TIER_SWITCH_PLAN.md` 并回填「验收记录」（复现失败输出、
生成器 diff、测试/vet/gofmt 结果、tmux/web 截图、踩坑）；报告根因、最小改动、证据、遗留；
改动留工作区不 commit。

## Test plan

- 新增（Step 1，先失败后通过）：上述 7 个用例，覆盖：一对一行（资格内）、两行可辨（资格外）、
  静态表唯一性（`narrowToSurface` 不变式守卫）、6004 分类与 resetAt（UTC+8）、有效 id 切换与过期回落、
  `Explain` 新码、老凭据补 `type`。
- 既有断言更新：`workbuddy_test.go:111-145 TestStaticTableMatchesOfficialProduct` 首行名字
  → `Hy4 Free`（理由：免费档带后缀，付费档保持厂商名）；`TestFetchModelsOffersTheSurfaceAgentListInOrder`
  的假 config 不含 `ModelRateLimitCap` ⇒ `records[0].Name` 仍是 `Hy4`（保留这条“无声明不猜”的契约）。
- 结构范式：`workbuddy_test.go:384-425`（`httptest` + `withEndpoint` + `storeCredentials`）。
- 回归：`pkg/llm` 的 `TestExplain*`、`pkg/turn` 的 auto-continue 用例（新码不得被它接受）、
  `pkg/tui` notice 用例、web `SubscriptionLoginModal.test.ts` 与 `AutoContinueBanner.test.ts`（作为 banner 范式）。
- e2e：`scripts/acceptance/web_e2e.sh` 相关 spec。

## Done criteria

- [ ] **Step 0b 的证据已记录**：HTTP 状态、`Content-Type`、字段路径（`data.code`/`error.code`/`error.data.code`）、
      文案字段名、重置时间所在文本；用例 4 的 fixture 注释里写着“FIXTURE FROM STEP 0b: …”，与记录一致。
- [ ] Step 1 的 7 个用例在改生产代码前**确实失败**，失败输出已记录。
- [ ] 标签层机械验证：`llm.FormatProviderModelDisplay("workbuddy","hy4free") == "workbuddy/Hy4 Free"`、
      `...("hy4std") == "workbuddy/Hy4"`（同一 `restoreShippedTable` 之后）。
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./plugins/subscriptions/workbuddy -count=1` 全 PASS；
      其中 `grep -c 'Name: "Hy4 Free"' models_static.go` = 1、`grep -c 'Name: "Hy4"'` = 1（`Hy3` 同理）。
- [ ] 生成器重跑 diff 只含 2 处 `Name` + 新增 pairs 表（外加可能的日期行）。
- [ ] 只读 live 抓屏：勾选页仅 `Hy4 Free`/`Hy3 Free` 两行 Hy 家族；`/model` 一行。
- [ ] fake 换档：TUI notice 出现、`Ctrl+P` 后请求 `model`=`hy4std`、过期后回 `hy4free`；web banner 两按钮可用。
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/llm ./pkg/turn ./pkg/run ./pkg/subscription ./pkg/gateway ./pkg/tui -count=1` 全 PASS；
      其中新增：auto-continue 不接收新码、TUI 打字不关闭 offer、`POST /paid-tier-offer` 的 use/dismiss 与 ack、
      “含 quota 但非 6004”仍归旧码、`RunEventPaidTierOffered` 名钉住；
      `CGO_ENABLED=1 go vet -tags fts5 ./plugins/... ./pkg/...` 无输出；`gofmt -l cmd pkg third_party plugins` 无输出；
      `cd frontend && pnpm typecheck && pnpm test` 全 PASS。
- [ ] `git diff --stat -- plugins/subscriptions/workbuddy pkg/llm pkg/turn pkg/event pkg/tui pkg/gateway pkg/subscription frontend/src`
      **只**列出 Scope 里的文件（不看全局 `git status`：共享树有他人 WIP）。
- [ ] 计划副本已回填验收记录；改动未 commit。

## STOP conditions

1. Step 0a 的核对与本文摘录**内容**不一致（并行会话已改到这些符号）→ 停下报告，重新定位再改。
2. 真实 `/v3/config` 不再带 `productFeaturesConfig.ModelRateLimitCap.lines`，或四个 Hy 行的
   `name`/`credits` 与 §1 不同 → 厂商契约变了，先重新取证，**不要**按 id 猜哪档免费。
3. 6004 的实际信封与 Step 0b 证到的形状不一致（例如藏在 SSE 事件或另一层对象里）→ 停下，按新形状
   重定分类点；不要用“看谁都像 6004”的宽匹配。
4. `go-openai` 对改写后的错误体仍解析不出 `code`/`message`（版本行为与 `pkg/llm/openai/error.go` 的
   假设不符）→ 停下报告，改用插件装饰器包装错误（不要放宽全局分类）。
5. 重生成 `models_static.go` 出现 2 处名字与 pairs 之外的差异（窗口、思考档位、images 变了）→ 上游
   配置已更新，本计划不夹带能力刷新；停下报告。
6. 回归出现 **与本改动无关** 的失败（并行会话的 `pkg/tui` 卡片、`pkg/turn/model.go`、`pkg/assembly`
   改动）→ 用 `peer_send` 与对应 agent 确认归属，不代修、不为变绿越界改文件。
7. Step 0b 的探针在本机被网络阻断（或凭证失效）而 CLI 源码也定不了形状 → 停下报告；不得把
   “未取证”写成已验收，也不得按猜测实现分类。
8. 资格判定：拿到 `personal`/`pro` 却仍出两行（与预期相反）→ 停下报告；**取不到 `type` 不算 STOP**，
   记 warn 后按资格外继续（§3.1 已定）。资格错判会改变谁能看到付费档，所以这一条要在验收记录里写明
   实际取到的 `type`。

## Maintenance notes

- **唯一权威**：免费/付费配对、`allowPaidSwitch`、displayName 都来自厂商 `/v3/config` 的
  `ModelRateLimitCap.lines`；状态文件与 live 补注都以它为准。它改形状时同时改
  `ratelimit.go`、`models.go`、`gen_models_static.go`（生成器不能 import 插件包，靠注释互相指认）。
- **状态是缓存的额度窗口**，不是配置：`resetAt` 过期即删；进程内存里的 live 命中随进程消失；
  不做迁移、不进 app 状态库。
- 发送 id 的改写只在 `Transport` 一处；新增 provider 若有同类分档，应注册 `llm.PaidTier` 并把错误包成
  `*llm.PaidTierError`，而不是在 surface 上加分支。
- 官方 `paid→free` 反方向解析与 `switchAuto` 未移植（理由见 §3.4/§3.5）；将来若放开 `auto` 或允许
  预选付费档，需要重做这一层。
- 审查重点：`Transport` 改写是否只动 `model` 字段与 `X-Model-ID` 且三条前置齐备；状态文件互斥/缓存是否
  与凭据文件同款；新 Explanation 码是否被 auto-continue 的 `switch` 排除；两个 surface 是否读同一个
  运行时状态；资格判定是否只用可证字段（`type`，`denyList` 未移植）。

## 评审意见处理（2026-11-15，review model: opencode-go/deepseek-v4.1-flash）

| 评审意见 | 处理 |
| :-- | :-- |
| Done criteria 的全局 `git status` 在共享树不可执行 | **采纳**：改成对 Scope 路径的 `git diff --stat`，并说明不看全局状态的理由 |
| 根因写窄：同名行不止两对（`hy3-b/hy3-c`、`hy4-preview*`） | **采纳**：§1 补 61 条目录里的 4 个 `Hy3`、5 个 `Hy4 preview`，并把 `narrowToSurface` 不变式写进测试（Step 1 用例 3） |
| `/model`、footer、web chip 声明无机械验证 | **采纳**：Step 2 的静态表唯一性测试 + Step 1 用例 1/2 直接钉住 `Offers()` 的名字互异（web/TUI 同读 `Offer.Name`）；真机抓屏补 `/model` |
| Step 1 的复现是自造小配置，建议提交真实 product config 作 testdata | **部分采纳**：不提交整份 64KB 厂商配置——它内含 `skillhubKey`/`skillSecurityAppKey` 等客户端密钥，且会过期；改为：用例集钉住 4 个线上 id 与 lines 的形状，唯一性测试守卫 `narrowToSurface` 不变式，重生成时用 live 配置做逐字节对照 |
| Step 3 抓配置脚本偏离 `disguiseHeaders`（缺企业头、`owner_uin` 硬编码） | **采纳**：默认用插件自己的 `listingJSON` 语义；脚本路径只作个人账号兜底，并补齐 `X-Conversation-Request-ID`/`X-Conversation-Message-ID`/`X-Enterprise-Id`/`X-Tenant-Id` 与 `X-Userinfo.owner_uin=enterprise` |
| 漂移检查只覆盖 3 个文件、“计划时 git status 干净”在共享树失效 | **采纳**：Step 0 覆盖全部 in-scope 路径；基线说明改为“`plugins/subscriptions/workbuddy` 干净，其它 agent 的 WIP 不在 drift 范围” |
| `nameFreeTiers` 不幂等（“Hy4 Free Free”） | **采纳**：命名规则写成幂等（已以 ` Free` 结尾则不追加），运行时与生成器同一规则 |
| 生成器 `outputPath` 相对 CWD | **采纳**：Commands 表写明必须在仓库根目录执行 |
| qodercn 排除理由写错（它有 `staticQoderModels`） | **采纳**：§Scope 改成“静态名唯一、live 无同名证据”，不并入 |

### 第二轮

| 评审意见 | 处理 |
| :-- | :-- |
| §3.3 建立在未证实的线上信封上 | **采纳**：新增 Step 0b 诊断（先只读解剖解包的 CLI 源码 `codebuddy-lite-wb.mjs` 的 `classifyErrorAsRequestError`/`data?.code` 分支；仍不定则在 **owner 已批准的一次**必然被拒探针里取证）；用例 4 的 fixture 必须来自该证据；定不了就 STOP |
| §3.5 的切换端口到不了 runtime（`pkg/turn` 无 `llm.LLM`，`pkg/run` 装饰器不转发） | **采纳并改设计**：改为 `*llm.PaidTierError`（错误自身携带 provider/free/paid/reset/display）+ `pkg/llm` 的 `RegisterPaidTier`/`UsePaidTier` 注册表（照 `RegisterProviderFactory` 先例），turn 用 `errors.As` 检测、按 provider 调动作；不依赖 `pkg/run` |
| `PaidTierOffer(err)` 无法指认是哪一对（`APIError` 无 model 字段） | **采纳**：`PaidTierError` 自带 `Provider/FreeModel/PaidModel/DisplayName/ResetAt` |
| 资格判定会让 owner 的原始缺陷继续存在（已保存凭据无 `type`） | **采纳**：`AccountType` 缺失时补一次只读 `GET /v2/plugin/account` 并持久化，加 `TestLegacyCredentialGetsAccountType` |
| “照客户端”与事实不符（客户端是 `type` + `denyList`；切换还要求 `allowPaidSwitch` + catalog 有 paid） | **采纳**：资格按 `type ∈ {personal,pro}`，`denyList` 明确标注**未移植**；发送 id 改写加上 `allowPaidSwitch` 与“目录有 paidId”两条前置 |
| TUI 裸 `p` 与打字/“输入即取消”冲突 | **采纳**：改 `Ctrl+P`，并明确“打字不关闭 offer”（加用例） |
| web 端点留成 `?` | **采纳**：点名 `api.Get/Post("/paid-tier-offer")` + WS op `use_paid_tier`/`dismiss_paid_tier_offer` + ack（照 auto-continue 接线） |
| 热路径成本（每请求读状态文件）与 body 上限 | **采纳**：状态按 `(path, mtime, size)` 缓存、写路径持锁更新；新增 `maxErrorBody = 64 << 10` |
| 把 `pkg/architecture` 分层测试当事实引用（plugins 不在其内） | **采纳**：改为引用 `AGENTS.md` 的真实规则（`pkg/llm` 根不 import provider SDK），并说明插件不在分层表内 |
| 建议拆成两步（先命名/状态，换档另开计划） | **未采纳**——owner 2026-11-15 明确选择「等换档一起落地」；拆分会交付一个没有付费档可用路径的中间态 |

### 第三轮

| 评审意见 | 处理 |
| :-- | :-- |
| P1 资格判定与用例 2 互斥（`personal` 就是资格内） | **采纳**：用例 2 改为 `TestOffersKeepBothRowsForANonEligibleAccount` + `AccountType:"enterprise"`；§3.6 矩阵同步 |
| P2 “`type` 取不到”三处说法不一 | **采纳**：统一为“记 warn、按资格外继续、不 STOP”；STOP#8 改为只在“拿到 personal/pro 却仍两行”时停 |
| P3 Step 0b 针脚选错一级、候选形状漏了 `error.data.code` | **采纳**：针脚改为 `parseResponseBodyObject`/`extractRateLimitBizCode`/`extractBizCode`/`isQuotaExhaustedError`/`error.data.code`；候选形状补齐 `{"error":{"data":{"code":6004}}}` |
| P4 `status >= 400` 的闸无证据 | **采纳**：闸改为“JSON 响应（非 SSE）时不论 2xx/4xx，体带 6004 即分类”；Step 0b 记录状态与 `Content-Type`；SSE-only 的错误按 STOP#3 重定 |
| P5 探针与 Scope 冲突、批准无法证实 | **采纳**：Scope 明写唯一例外（owner 选定本方案时已批准的那一次探针），并限定用途；不再暗示其他 chat 请求 |
| P6 无 resetAt 是否 opt-in 两处相反 | **采纳**：`UsePaidTier(freeModel, resetAt)`；有 `resetAt` 才落盘，无 `resetAt` 只记进程内 live 态（客户端 `setLiveBannerCapHit` 同款） |
| P7 用例 4 fixture 写死猜测形状 | **采纳**：fixture 改为 `// FIXTURE FROM STEP 0b: …` 占位，Done criteria 挂钩 |
| `TestCanonicalLifecycleEventNames` 可能需加新事件 | **核实后不加**：该清单只含 turn/approval/compact/config/session 类（`pkg/event/run_events_test.go:44-62`），auto-continue 事件同样不在其中；改为在插件/turn 用例里钉事件名 |
| GET/POST 同一 handler 的 session 来源 | **采纳**：GET 用 `?session_id=`（照 auto-continue 的 GET） |
| `isQuotaExhaustedError`（码 `-32003`）可能与新码撞车 | **采纳**：Step 3 加护栏用例——“含 quota 但非 6004”仍归既有码 |
| `AGENTS.md` 在仓库不存在 | **采纳**：改为引用 `FOREBRAIN.md` 的 Architecture/Layering 段 |

### 第四轮

| 评审意见 | 处理 |
| :-- | :-- |
| N1 §3.3 与 Step 3 的分类闸矛盾 | **采纳**：Step 3 对齐 §3.3（JSON 响应时不论 2xx/4xx 都查 6004） |
| N2 2xx 读 body + 64KB 上限 + “内容不丢”不相容 | **采纳**：只读 head（≤`maxErrorBody`），回填 `io.MultiReader(bytes.NewReader(head), rest)`；2xx 是否检查取决于 Step 0b 证据，否则 2xx 直通 |
| N3 §3.4 只认持久 opt-in，无 resetAt 的 Use paid 空转 | **采纳**：切换条件改为 `paidSwitchActive = 持久未过期 \|\| 进程内 live`（客户端 `resolveEffectiveCapHit` 同款） |
| N4 新路由缺授权、参数风格不一致 | **采纳**：GET/POST 都走 `?session_id=&action=`，并调 `s.sessionOwned`（未拥有 404），照 `handleAutoContinue`（`run_control.go:1856-1896`） |
| N5 `pkg/*` 生产文件预算（`maxProductionFilesPerPackage = 34`，`pkg/gateway` 顶格） | **采纳**：`pkg/gateway` **不得新增生产文件**（handler 进既有文件）；Commands 表加 `scripts/package-graph.sh` 与预算检查 |
| N6 Step 0b 候选形状漏了 `error.data.code` | **采纳**：候选形状与产出对齐（含 `error.data.code`，客户端首选分支） |
| N7 检测落点与测试文件命名 | **采纳**：检测写在 `turnEnded`（`submit.go:653-663`）；`pkg/llm` 测试落 `paid_tier_test.go` |
| “owner 是否批准探针”无法由计划自证 | **已由 owner 本人在本轮问答中明确批准**，计划已改写为“2026-11-15 明确批准”并限定用途 |

## Implementation Status

- **Verdict**: APPROVE（两项验收只做到“机制层证据”，见下；不是 STOP，也不越 Scope）
- **Worktree**: `/Users/doudou/workspace/unionj-cloud/forebrain-harness/.worktrees/workbuddy-paid-tier`，
  分支 `workbuddy-paid-tier`，基线 `624aea88`（owner 2026-11-15 指令：先本地提交主树 dev 全部改动，
  即 `624aea88 feat(status): …`，再从该 HEAD 起 worktree）。
- **Executor commits**: `3975c7dc feat(workbuddy): free/paid tiers as one row with the paid switch`、
  `63f915ad fix(workbuddy): parse Ctrl-P, read the cap code through the normalizer`。未 push、未 merge；
  worktree 树干净。
- **Task 状态**
  - Step 0a（drift 核对）：**done**（6 处符号按内容核对一致；两处行号漂移、一处 `(t account)`→`(a account)` 笔误）。
  - Step 0b（6004 信封取证）：**done**。CLI 解析顺序 `error.data.code → error.code → code`（`parseResponseBodyObject`/`extractRateLimitBizCode`）；
    owner 已批准的那一次探针（不存在的模型 id）返回 **HTTP 400**、`application/json; charset=utf-8`、
    `{"code":11102,"msg":"model [...] service info not found",…}`；6004 为 CLI 的 `CraftRateTPDLimit`，
    重置时间在 `msg` 文本里按 UTC+8 解释；用例 4 的 fixture 注明取自该证据。
  - Step 1（先失败后通过的复现测试）：**done**（含资格内一行/资格外两行/静态表唯一性/6004+resetAt/发送 id/`Explain` 新码/老凭据补 `type`/label 层）。
  - Step 2（目录与 offer 合并）：**done**（生成器重跑 diff 仅 2 处 `Name` + pairs 表；资格内 14 个 offer）。
  - Step 3（额度状态与 6004 分类）：**done**（含“含 quota 但非 6004 归旧码”护栏）。
  - Step 4（有效发送 id）：**done**（三条前置 + 持久/live 两种归属；过期回落）。
  - Step 5（运行时端口与两个 surface）：**done**（`*llm.PaidTierError` + `RegisterPaidTier` 注册表；
    TUI notice/`Ctrl+P`/`Esc`；`GET/POST /paid-tier-offer` + `sessionOwned` 404 + WS op/ack；web banner + i18n）。
  - Step 6（真机验收）：**partially done**。已抓屏：`/connect` 勾选页 14 行（`Hy4 Free`/`Hy3 Free` 各一行，
    无第二行 `Hy4`/`Hy3`）、Subagents 页一行、legacy 登录补 `account_type: personal`、provision 只写
    一行 `workbuddy|hy4free`。未做到：`/model`+footer 的真机抓屏（隔离 home 的 onboarding 在 Subagents
    步之后退出，报 `startup config incomplete`；执行者给出 capture 与 `ComposedStartupConfig rows:1 defs:0`
    证据，属隔离环境阻塞，非本改动）。伪 6004 端到端只做单测覆盖并声明限制（fake provider 无自定义错误体模式）。
    web banner 只有组件测试（仓内没有覆盖该 banner 的 e2e spec），无截图。
  - Step 7（收口）：**partially done**（计划副本与本节由 reviewer 回填；执行者按派发跳过 plan/backlog 更新）。

### 我（reviewer）自己跑过的验证

- `CGO_ENABLED=1 go test -tags fts5 ./plugins/subscriptions/workbuddy` → `ok`（修前与修后各一轮）；
- `./pkg/llm ./pkg/turn ./pkg/run ./pkg/subscription ./pkg/gateway ./pkg/tui ./pkg/event ./pkg/architecture`
  → 全 `ok`（第一轮全跑，第二轮复跑 llm/turn/gateway/tui/event/workbuddy）；
- `CGO_ENABLED=1 go vet -tags fts5 ./plugins/... ./pkg/...` → 无输出；`gofmt -l cmd pkg third_party plugins` → 无输出；
- 生成器以 live `/tmp/wb_product_config.json` 重跑 → 与入库 `models_static.go` **零差异**（含 pairs 表与 `… Free` 名）；
- 静态表：`ID: "hy4free", Name: "Hy4 Free"`、`ID: "hy4std", Name: "Hy4"`、`hy3/hy3-x` 同理；pairs 表与线上声明一致；
- Scope：`git diff --stat 624aea88..HEAD` = 36 个文件，全部在计划 Scope 或两份已记录的扩展内
  （`pkg/tui/input_events.go`、`pkg/tui/run.go`、`frontend/src/lib/paidTier.ts`）；`pkg/gateway` 未新增生产文件；
- 密钥扫描：diff 内只有 struct tag 与测试假 token，无真实凭据；
- 复跑 `TestReadRawInputEventsCtrlPEmitsThePaidTierSwitch` 所属包 → PASS（确认 `Ctrl+P` 真有按键生产者）。

### 评审轮次与修正

- **第 1 轮（REVISE，1 个阻塞）**：`hotkeyPaidTierSwitch` 只有常量与比较、**没有按键解析点**（Ctrl+P 实际是死键）。
  修正：控制字节 switch 增加 `case 0x10`（`input_events.go:1639`）+ 走解析器的字节级用例。
  同时要求并已完成：`pairedPaidRow` 补 `listed[freeID]` 前置、`bizCode` 接受字符串码、
  “notice 是 transcript `FrameStatus` 行”写成注释。
- **第 2 轮**：执行者另发现并修的同类缺陷——`pkg/llm` 的错误归一化会在插件 transport **下方**把 ≥400 体包成
  `{"error":{"code":<status>,"message":"<原体>",…}}`，因此 `readCapRefusal` 现在跟随 `msg`/`error.message`
  里的 JSON 文档（深度 2）；`TestPaidTierLLMNamesTheCapRefusal` 走真实 factory 钉住端到端分类。
  我复核后 APPROVE。

### 刻意偏离（有理由，未越 Scope）

| 偏离 | 理由 |
| :-- | :-- |
| TUI 提示画成 transcript `FrameStatus` 行，而非“输入框上方一行” | 沿用 auto-continue 的既有范式（`notify.go:2395-2585` 家族）；已在函数注释与本记录写明 |
| 6004 闸＝“4xx + JSON”，不是“任何 JSON 响应” | Step 0b 证明拒绝是 400（`application/json`）；2xx/SSE 不预读，避免破坏流与读大成功体 |
| free 档改名只在**有 live 声明**时生效 | 保住“无声明不猜”的既有契约；静态兜底表自带 `… Free` 名 |
| 不移植官方 `switchAuto` / paid→free 反方向 | 沿用 owner 2026-11-15 裁决（`auto` 不进 surface）与不可达论证 |
| `pkg/tui/input_events.go`、`pkg/tui/run.go`、`frontend/src/lib/paidTier.ts` 超出计划文件清单 | Ctrl+P/Esc 必须落在按键分发与事件循环；web 共享一个解析器；均已在执行报告 NOTES 说明 |

### 遗留（不阻塞合并，供 owner 决定）

1. `/model`+footer 真机抓屏：机制层已由 `FormatProviderModelDisplay` 标签用例 + footer 同一函数
   （`pkg/tui/run.go:1202`）+ “只 provision 一行”共同证明；隔离 home 的 onboarding 阻塞见上。
2. 伪 6004 的 TUI 端到端（真机按键→发 `hy4std`）：只有单测；需 fake provider 支持自定义错误体后才可冲。
3. web banner 的视觉确认与 e2e：只有组件测试，仓内无对应 spec；按前端视觉确认硬流程仍需 owner 过目。

### 本期已修的两处预存缺陷（owner 2026-11-15 指令「预存问题必须本期修复」）

两处都在**主树**（不在本计划的 worktree 分支），未提交，留 owner 审阅。

1. **`pkg/architecture/testdata/graph.json` 已漂移，CI 的“Verify package graph”必红**。
   根因：fixture 没随早先的提交重生成（不是脚本不确定——脚本输出排序、两次运行逐字节一致，已实测）。
   修法：在 `624aea88` 上用 `scripts/package-graph.sh` 重生成并作为新 fixture（diff 43+/35-：`agent` 2373→2410、
   `assembly` 3019→3084、`channel` 5444→5456、`config` 5759→5856 且 `fan_in` 15→16、`fan_out` 1→0、
   `event` 2141→2183、`gateway` 30738→31664、`home` 1552→1526 等，另有三条边变化）。
   证明：`scripts/package-graph.sh` 连跑两次 + `diff -q` 一致（`FIXTURE_IS_SCRIPT_OUTPUT`）；
   `go test ./pkg/architecture` `ok`。**注意**：`workbuddy-paid-tier` 分支合入后 `pkg` 的 LOC/fan-out 会再变，
   合并后必须再跑一次该脚本（本记录不把那次重生成算作已做）。
2. **`frontend/src/lib/settingsSchema.test.ts` import 了 gitignore 掉的原型，干净检出/CI 必失败**。
   根因：测试的受控输入（owner 的原型 `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/config-db-settings-preview.html`，
   116 KB）放在 `docs/plan/`，而该目录 gitignore——CI 里根本没有这个文件，所谓“机器硬约束”跑不起来。
   修法：把原型**入库一份受控副本** `frontend/src/lib/testdata/config-db-settings-preview.html`，测试改读它；
   原型本体仍留在 `docs/plan/`（owner 的编辑面）。测试头注释写明：原型每次改动必须在同一提交里刷新这份副本，
   schema 随后跟上。
   证明：`pnpm typecheck` 干净；`pnpm test -- src/lib/settingsSchema.test.ts` 该文件 **9 tests passed**；
   全量 `pnpm test` **189 files / 1458 tests passed**（此前干净检出下这个文件直接不可收集）；
   仓内已无任何 `?raw` import 指向 `docs/plan`。
