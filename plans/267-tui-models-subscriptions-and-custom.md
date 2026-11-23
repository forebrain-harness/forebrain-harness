# Plan 267: TUI——设置/连接改走订阅注册表与模型表，新增自定义模型，选择器显示四项

> **Executor instructions**: 按步骤顺序执行，每步的验证命令通过后再做下一步。遇到
> 「STOP conditions」里的情况就停下来报告，不要自己发挥。**不要 commit / push / merge**：
> 改动留在工作区里，做完报告（仓库主人手动提交）。做完后把 `plans/README.md` 里本计划那一行
> 的状态改成 DONE，并附一句执行摘要——除非派你来的评审说索引由他维护。
>
> **Drift check (run first)**:
> `git diff --stat 0a698bf2..HEAD -- pkg/tui/setup.go pkg/tui/run.go pkg/tui/overlays.go pkg/turn/executor.go`
> → 只允许出现 plan 260–263 的改动（`setup.go` 的默认解析、选模型带 id、`providerGroupsFromRegistry`；`executor.go` 的 `/model` 按 id）。对不上即 STOP。

## Status

- **Priority**: P1
- **Effort**: L
- **Risk**: MED（TUI 首次设置与 `/connect` 是新用户的第一屏）
- **Depends on**: plans/261、262（`process.Execute` 写表、返回 `ModelID`）、263（注册表、`ProbeLLMModel`）、264（订阅注册表）
- **Category**: direction
- **Planned at**: commit `0a698bf2`, 2026-11-07

## Why this matters

owner 2026-11-07：「tui 端和 web 端的 UI 和 UX 都要根据最新语义改造」；「模型下拉框和 picker 必须把 provider/model/base url/api path 都显示出来」；
自定义模型（不填 provider、协议必选、模型名可带 `vendor/` 前缀）；美团、腾讯进内置列表；
订阅登录统一，且**只放支持 OAuth 或其他协议登录的订阅**（今天 ChatGPT、OpenCode Go；火山方舟由 plan 281 加，TUI 零改动）。网页在 plan 266；本计划是 TUI 的同一套语义（owner 规则：TUI 是标准，网页向它对齐）。

## 原型与视觉基准（2026-11-07 定稿）

- **可交互原型**：`docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/tui-prototype.html`。18 个场景覆盖本计划的 `/model`、第一页三组、四种登录流程、Offers 多选、Key 选择/创建、自定义全流程、查重、首启与边界；可直接按键操作、切换 120/88/68 列。实施以它的面板内容、字段顺序与文案为准。
- **视觉不是新设计**：原型把 `pkg/tui/render.go` 的 `slashPanel.layout`、`panelField`、`panelBuilder` 与 `pkg/tui/panel_columns.go` 的两列布局移植到了浏览器，并用真机 `tmux capture-pane -e` 核对了 SGR。实现时**复用现有 panel/picker，不另造卡片、边框、颜色或图标**。
- 硬规则（都有真机或源码依据）：accent `38;5;110`（标题加粗），hint/滚动提示 faint，过滤条 `48;5;238`；`❯` 与过滤条 caret 互斥；可选行 `<8` 不显示过滤条，placeholder 固定 `Type to filter`；两列名字列上限 40 且不超行宽 2/5、列间距 2，任一名字超列宽则**整列表堆叠**；多选行首 `●`/`○`；密钥输入 `›` + `•` 掩码、长度 >8 只露末四位；复核页沿用 facts 对齐。
- **文案只说当前事实**（owner 2026-11-07）：被移除的配置位置、旧写法与「以前 / 不再」式对照一律不出现在界面上，按全新产品设计。API Key 页副标题固定为 `Saved on this machine only.`（与 plan 268 同一处改动）；复核页副标题用 `Check the details, then save.`，不写保存到哪个旧配置文件。

## Current state

- TUI 模型设置是一组翻页式选择（`pkg/tui/setup.go`）：`newLLMSetup`（`:576`）→ `run`（`:583`）在 `setupPageProvider → setupPageCredentials → setupPageModel → setupPageReview`（`:524-530`）之间前进后退；
  `pickProvider`（`:691`，读 `providerGroups`，plan 263 起由注册表生成）、`credentials`（`:764`，填 key / 改 endpoint）、`signInChatGPT`（`:853`）、`signInOpenCode`（`:934`）、
  `fetchSignedInModels`（`:983`）、`pickModel`（`:1031`）、`review`（`:1074`）、`editEndpoint`（`:1135`）。保存经 `process.Execute`（`:135`、`:1479`；plan 262 起写表并返回 `ModelID`），随后 `SelectModel` 让当前会话切过去（`:1499`）。
- `/connect` 在 `pkg/tui/run.go:1308` 进入同一设置流程（以当前主代理为目标）。
- `/model` 选择器（`pkg/turn/executor.go` `execModel`；plan 261 起 `Value` 是行 id、`Description` 是完整请求 URL：`base_url` 直拼 `api_path`），TUI 用 `pkg/tui/overlays.go:5356` 的两列渲染（名字列 + 说明列），过滤框搜两列（`:5305`）。
- TUI 选择器规则（owner）：❯ 与光标互斥；少于 8 项不显示过滤框；⌕ 底色条。
- 订阅注册表（plan 264）：`subscription.Registry()` = chatgpt / opencode-go（火山方舟由 plan 281 加），各有 `Flows()`；TUI 能驱动 `browser_callback`（本机起回调）、`device_code`（OpenCode）、`paste_callback`（ChatGPT）；`paste_code` 已删除。

## Commands you will need

| Purpose | Command | Expected |
|---|---|---|
| 构建 | `go build ./...` | exit 0 |
| TUI 测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui/ -count=1` | ok |
| 全量 | `CGO_ENABLED=1 go test -tags fts5 ./... -count=1` | ok |
| 真机 | 仓库 `.claude/skills/run-forebrain`（`driver.sh` 起 TUI，读屏、发键） | 见 Step 6 |

## Scope

**In scope**: `pkg/tui/setup.go`、`pkg/tui/run.go`（`/connect` 分支）、`pkg/tui/overlays.go`（仅当选择器说明列需要更宽/截断策略调整）、`pkg/turn/executor.go`（`/model` 末尾的「添加自定义模型…」项）及对应测试。

**Out of scope**：后端接口语义；网页；`.env`（plan 268）。

## Steps

### Step 1: 第一页改成三组

`pickProvider` 的列表改为三组（组名即分隔标题，TUI 现有的分组渲染；前两组沿用现有常量 `providerSignInCategory` / `providerAPIKeyCategory`，不另起译名）：
1. **`Sign in with a subscription`**：`subscription.Registry()` 的每一项，顺序即注册表顺序（今天 ChatGPT、OpenCode Go；火山方舟由 plan 281 加），名称取注册表 `Label`，说明一行，已登录时说明末尾追加 `  · Signed in`；本组**只有注册表里的订阅**，智谱 Coding Plan 等只填 Key 的套餐不进来。
2. **`Use an API key`**：注册表里非登录类 provider（plan 263 生成的 `providerGroups` 去掉 chatgpt/opencode-go 两组；美团、腾讯、火山方舟、BytePlus、智谱 Coding Plan 接入点都在这里）。
3. **`Custom`**：一项 `Custom model`，说明 `OpenAI- or Anthropic-compatible, or custom protocol`（自定义模型同样支持「自定义协议」：请求原样发到填写的地址，不追加路径）。
今天 ChatGPT / OpenCode 的选项改由第 1 组承担，走 Step 2；文案保持英文为主（TUI 现状）。三组的行内容逐字照原型 `connect` 场景：ChatGPT 沿用 `Use Codex included with your ChatGPT plan`，OpenCode Go 沿用 `Device-code login; use your Go/Go Plus quota`（火山方舟那一行由 plan 281 加）。

端点子页（组内多于一个接入点时出现，`setup.go:727`）副标题改成一句：`Plans are subscriptions; everything else is billed per use. The endpoint is shown under each.` —— 现有句 `Standard is pay-as-you-go; Coding Plan is a subscription.` 只点了 Coding Plan，漏掉同样属于订阅的 Agent Plan；改成「Plan 都是订阅、其余按量计费」后，新增任何 Plan 都不会再漏，地区/协议分组（腾讯、MiniMax、美团）也成立。

**Verify**: 更新 `pkg/tui` 里断言第一页选项的测试（`grep -ln "providerGroups\|pickProvider" pkg/tui/*_test.go`）；`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui/ -run 'Setup' -count=1` 通过。

### Step 2: 订阅登录走注册表

**浏览器由 TUI 打开，flow 不打开**（plan 264 的取舍：`pkg/subscription` 的 flow 一律不碰浏览器，只有 surface 知道这台机器上有没有用户的浏览器；网关今天也不打开）。`Start` 之后由 TUI 调现有 `pkg/tui/image_open.go:58` 的 `startDetached`（`open` / `xdg-open` / `rundll32`，见 `:97`/`:101`）打开 `login.AuthorizeURL()`——`browser_callback`、`paste_callback`、`device_code` 三种都要；URL 是 flow 稍后才交出来的，就绪前显示 `Waiting for the sign-in URL…`，拿到后再打开一次。打开失败只在面板上显示一行提示，流程继续（URL 与 code 已可见）。`openai.Login`/`opencode.LoginDeviceFlow` 自带的开浏览器行为不再经过本流程——这正是 plan 264 换成 no-op opener 的原因，别在这里补回去。

选中第 1 组某项 → 取 `sub.Flows()` 中 TUI 能驱动的第一个：
- `browser_callback`：打开浏览器（沿用 `signInChatGPT` 的打开方式与进度行），本机回调；**检测不到图形环境时退到 `paste_callback`（ChatGPT 粘回调地址）——这是本计划新增的判断（基线没有 SSH/图形环境探测的先例，`pkg/` 里 grep 不到 `DISPLAY`/`SSH_CONNECTION`）：`DISPLAY` 与 `WAYLAND_DISPLAY` 都为空、或设置了 `SSH_CONNECTION`，即视为无本机浏览器；`device_code` 订阅在同一判定下只是不打开浏览器，流程不变、仍自动轮询**；
- `device_code`：显示验证码（沿用 `signInOpenCode`），面板副标题 `Open https://opencode.ai/auth and enter this code.`，等待行沿用 `Waiting for you to confirm the code in OpenCode…`；
- `device_code` + **自动打开浏览器**、`ErrKeyChoice` / `ErrNoKey` 的 Key 选择与创建页、手填 Key 输入：**都不在本计划**——今天没有带 Key 的订阅，这些属于 plan 281（火山方舟）的那一步，届时只加分支，不改本计划的结构。

- 登录完成 → `sub.Offers` → 新的多选页（标题 `<订阅 Label>\nChoose the models to add; each row shows where requests go.`；按 `PlanLabel` 分组；每项名字列 `model`、说明列 `ModelEndpointLabel(base, path)`；行首 `●`/`○`，空格切换、回车确认）→ `sub.Provision(ctx, env, chosen, subscription.ProvisionOptions{})`；失败把注册表返回的短句显示在同一页，不写任何行。
- 成功后：若当前会话需要切换（今天 `/connect` 完成会切到新模型），用返回的第一个 id `SelectModel`（带 `ID`）。
- `signInChatGPT`/`signInOpenCode` 里与注册表重复的逻辑删除，改为调用注册表；它们的现有测试改桩到 `pkg/subscription`，**断言不变**。

### Step 3: 自定义模型

第 3 组 → 依次：Base URL（文本，必填，校验 http(s)，placeholder `https://api.example.com/v1`）→ 协议（单选四项：`Anthropic Messages  /v1/messages`、`OpenAI Chat Completions  /chat/completions`、`OpenAI Responses  /responses`、`Custom protocol  Send to the URL as is`；第四项即「自定义协议」，存 `/chat/completions` + `URLVerbatim`）→
在 API key 页副标题显示一行「Requests go to <base><path>」（「自定义协议」只显示 `<base>`）并接上 `Saved on this machine only.` → API key（隐藏输入）→ Model name（文本，提示 `e.g. gpt-4o or openai/gpt-4o`，副标题 `Sent verbatim as the request’s model parameter; a vendor/ prefix is fine.`）→ 只有 plan 263 Step 3b 的来源链查不到这个模型的档位（models.dev 无条目/无档位）时，多选页「Thinking levels this model supports」（可不选，中英双写：`minimal · 极低`、`low · 低`、`medium · 中`、`high · 高`、`xhigh · 最高`、`max · 极致`）→ 「Test connection?」是/否（是则 `process.ProbeLLMModel`，成功页 `Connection OK\n<model> answered in <耗时>.`；失败显示短句，如 `Connection failed\n401 unauthorized — check the API key and the base URL.`，并允许返回修改）→ 复核页（Provider 恒为 `custom`，列出 Model / Protocol / Endpoint / Thinking）。逐步画面照原型 `custom`、`custom-test-fail`。
保存：`process.Execute` 以 `Provider: "custom"`、`APIPath: <协议路径>` 写入。

### Step 4: 重复配置的提示

`process.Execute`（plan 262）对同身份是「更新 key」。TUI 在保存前先查：`state.LLMModelStore.FindByIdentity`（规范化后），命中 → 是/否确认
「This provider, model, base URL and path are already configured. Replace its key?」——是：照常保存（更新）；否：回到复核页。
**这一步是 owner「保存之前需要做是否已存在的校验」在 TUI 的体现**：不静默覆盖。

### Step 5: `/model` 选择器

plan 261 已让每项显示 `provider/model` + 说明列完整请求 URL（`base_url` 直拼 `api_path`）。本计划：
- 说明列在窄终端截断时**保留路径**（从 base_url 中间省略，例如 `https://ark.cn-beijing.volces.com/api/cod…/v1/messages`）：在 `overlays.go` 的两列渲染里给说明列加「中间省略」选项，只对 `/model` 用。省略规则照原型 `elideEndpoint(url, path, width)`：说明列宽度足够时不处理；不足时保留整段 `<api_path>` 尾部，base 从左侧保留到剩余宽度后接一个 `…`。**说明列现在是一条直拼的 URL，字符串里没有分隔可切**——`/model` 选择器必须把该行的 `ModelChoice.APIPath` 一并交给两列渲染（行上带省略尾字段），`URLVerbatim` 行该字段为空、整条 URL 参与省略；其它选择器不设这个字段，行为不变；**名字列超宽触发现有的整列表堆叠时，仍先堆叠**（原型 68 列场景），堆叠后的说明行同样按可用宽度做中部省略，不丢路径；
- 列表末尾加一项「Add a custom model…」（`Value` 为保留值 `__add_custom__`），选中进入 Step 3 的流程；网关/频道上的 `/model` 不加这一项（它们没有交互式表单）。

### Step 6: 测试与真机

- 单测：第一页三组；订阅三种流程（桩 `pkg/subscription`）；Offers 多选；自定义流程写出 `provider=custom` 且 `api_path` 为所选协议；重复确认「否」不写库；`/model` 末项进入自定义流程；说明列中间省略保留路径。
- 真机（owner 规则：fake driver 驱动不了的流程必须用真模型跑通）：用 `run-forebrain` 技能隔离 home 起 TUI —
  1. 首次设置走「自定义」，指向 fake provider（`fake_provider.py`）的 base url + `/chat/completions`，发一条消息拿到回复；
  2. `/model` 看到两列四项，`Add a custom model…` 能进入流程；
  3. 用 glm-5.3（key 从仓库外 `.env`/环境注入到**隔离 home 的表里**——经本计划的自定义流程或 `/connect` 输入，不要写进 home 的 `.env`）跑一轮真实对话。
     （火山方舟登录需要 owner 账号，属于 plan 281 的手动验收清单。）

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui/ ./pkg/turn/ -count=1` → ok；全量 ok；真机 1–3 的截图/读屏结果写进报告。

## Done criteria

- [ ] `go build ./...`、`CGO_ENABLED=1 go test -tags fts5 ./... -count=1` exit 0
- [ ] 第一页三组；订阅组只有 chatgpt、opencode-go（plan 281 加 volcengine 时 TUI 不改代码）
- [ ] 面板结构、配色、过滤条、两列/堆叠与提示语与原型一致；`pkg/tui/setup.go` 不再出现指向旧配置位置的说明（`grep -n "never in forebrain.yaml\|\.env file on this machine" pkg/tui/setup.go` 无输出）
- [ ] `grep -n "__add_custom__" pkg/turn/executor.go pkg/tui/*.go` 有
- [ ] 真机 1–3 结果与手动验收清单在报告里
- [ ] `plans/README.md` 本行已更新

## STOP conditions

- 现有 ChatGPT/OpenCode 设置测试只有改断言才能过（行为变了）。
- `overlays.go` 的两列渲染被多个选择器共享，加「中间省略」会改变其它选择器的显示——只对 `/model` 生效做不到时停下。
- 频道（非 TUI）上 `/model` 的选择器会把 `__add_custom__` 发给用户。

## Maintenance notes

- 订阅组的内容完全来自注册表；新订阅不需要改 TUI，除非引入新的 flow。
- 自定义模型的 provider 恒为 `custom`；以后若要「自定义 + 显示名」，在表上加列，不要复用 provider。
