# Plan 310: exit_plan_mode 审批面板选完模型后再选思考强度（TUI + Web 端到端）

> **Executor instructions**：按 Phase A→H 顺序逐步执行，每步跑完验证命令、对照预期结果再往下。遇到「STOP 条件」任一条就停下报告，不要自行发挥。完成后把本计划的状态写回 `plans/README.md` 表格行（若派发方声明由它维护索引则跳过）。
>
> **Drift check（开工第一步，必跑）**：
> ```sh
> cd /Users/doudou/workspace/unionj-cloud/forebrain-harness
> git rev-parse --short HEAD            # 撰写基线 1c824c33
> git diff --stat 1c824c33..HEAD -- pkg/turn pkg/gateway pkg/run pkg/tui pkg/process pkg/state pkg/tool frontend/src
> grep -n 'type PlanReviewModelOption struct' -A 17 pkg/turn/model.go
> grep -n 'type SubagentModelOverride struct' -A 12 pkg/run/subagent.go
> grep -n 'func PlanReviewModelOptions' -A 48 pkg/turn/approval.go
> sed -n '355,372p' frontend/src/components/chat/PlanApprovalDock.vue
> ```
> 期望：`PlanReviewModelOption` 只有 7 个字段且无 `Efforts`/`DefaultEffort`；`SubagentModelOverride` **已有** `Effort string`（第 4 个字段）；`PlanReviewModelOptions` 的 `out = append(...)` 只填 7 个字段；前端 `pickModel()` 是「点选即提交」。任一处对不上即 STOP（代码已漂移，先报告）。

## Status

- **Priority**: P1（owner 2026-11-15 直接报障/需求）
- **Effort**: L（两端原型 + 数据层 + Go 协议/网关/引擎 + TUI 审批 overlay + TUI `/model` 第二步 + Web 审批面板，约 24 个文件；含测试）
- **Risk**: MED（改的是审批决策的持久化形状与两端交互；但**零 DB 迁移、零新表**）
- **Depends on**: none（计划 300 / 304 / 283 / 289 均已落地；本计划建立在它们的现状上）
- **Category**: direction（功能补齐）
- **Planned at**: commit `1c824c33`，2026-11-15
- **Executed**: 2026-11-16（`execute`，两轮修订后 APPROVE，owner 裁决「rebase 后 ff-merge」→ 已进 `dev` `7611d6cc`，未 push）——分支 `advisor/310-exit-plan-effort`，提交 `39c564b7`（生产 + 测试）→ `56c484b4`（e2e 修正）→ `7611d6cc`（Web picker 焦点根因修复），基于 `91c868f6`；改动 34 文件 +2402/−95。评审亲跑证据：9 包 `go test -tags fts5` 全绿、`go build`/`go vet`/`gofmt -l` 干净、`scripts/package-graph.sh` + `pkg/architecture` 绿、前端 `typecheck` 0 + 1489 tests + `build` 0、e2e 两 spec `9 passed`。三处偏离已裁定接受（Step 14/14a 空行不重复新增而由渲染断言守住、`EffortEnglishLabel` 导出、`graph.json` loc 刷新随 CI）。另修掉一处本计划依赖的既有缺陷：Web 审批面板点击后焦点落到 `<body>` 导致 dock 的 Esc 从不触发（283/289 起即有，310 第二屏同样依赖），修法照 `AskTabs.vue` 的「容器可聚焦 + 仅在焦点无主时接管」。执行记录全文见 `plans/README.md`「最新审核信息」顶部。
- **Gate**: **TUI 与 Web 两份原型都是前置闸门**（owner 2026-11-15 追加裁决「tui端和web端都必须先出原型」）。两份原型都必须先出、都必须经 owner 确认；**确认前不得修改任何源码文件**（含 `pkg/` 后端）。见 Step 0。

---

## 0. 已定案的语义（先读这一节，不要自行改设计）

owner 本轮**六条**裁决（编号 1–4、6、7）+ 一条由代码事实推出的技术裁决（编号 5）：

1. **覆盖范围**：审批面板里**两个「选模型」入口都加**第二步思考强度选择器 —— 「交给其他模型执行（hand off）」和「请其他模型评审（ask review）」。**「请上次评审模型再审（review again）」不在范围**：它不打开模型选择器（今天只发 `{again:true}`），继续沿用上次评审的模型。
2. **语义与「聊天页会话模型链路」（计划 300）保持一致**（owner 对「行默认 / 跟随模型设置是否同义」的追问裁决）：
   - 档位列表**按所选模型行**生成（`Efforts`），不是全局 7 档硬编码；
   - **默认回显该行配置的 `default_effort` 具体档位名**，并给等于该值的那一项打**齿轮图标**标记（`fromSetting`，对齐 `EffortChip.vue:89`；渲染为紧贴档位名的 `⚙`，见裁决 7）；
   - **不并列「跟随模型设置」空值项**——它和行默认在本链路里指向同一个值，并列会出现两个看起来一样的选项；
   - 用户不改直接确认 → 提交该**具体档位值**（快照，等价 `/model` 的「派生一次并钉住」），**不是**空值实时跟随；手动改选 → 提交所选具体值；
   - 仅当该行**没配任何档位**时才出现「默认」空值项（对齐 `EffortChip.vue:80` 的 `showDefault`），提交 `""`；
   - 模型**没有任何可选档位**（`len(Efforts)==0`）→ **跳过第二步**，沿用今天的一步直达（对齐 `pkg/turn/executor.go:914`）。
   - ⚠️ 明确**不采用**计划 299 的「等于行默认就存空、实时跟随」映射（`plans/README.md:67`）。owner 要的是聊天页语义。理由留痕：299 是长期配置项（需要「恢复为跟随」动作），本次是一次性审批决策。
3. **两端都必须先出原型，且先于一切源码改动**（owner 2026-11-15 追加裁决「tui端和web端都必须先出原型」，覆盖计划 283 时期「Part B 引擎/TUI/网关不受闸门限制」的旧惯例）：
   - **TUI 原型**：`docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/tui-prototype.html` —— 该文件是终端 UI 的权威基准，计划 282 明写「这就是目标，逐行对照」（`plans/282...md:37`）。要在它的 `E · 计划审批` 场景组加第二屏。
   - **Web 原型**：`docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/plan-handoff-preview.html` —— 审批卡的 `.xp-pick` 区块，加第二屏。
   - 两份都改完、都记录哈希、**都经 owner 确认**之后，才允许开始 Step 1。任一未确认即 STOP，整份计划标 `BLOCKED-on-prototype`。
   - 原型即 UI 稿：应用内部不得出现给执行者/评审者看的说明（owner 2026-11-12 硬约束）；说明写进本计划。
   - ⚠️ **TUI 原型还要覆盖 `/model` 第二步**（见下一条裁决），因为它也是 TUI 端的面板。
4. **TUI `/model` 第二步的档位列表也必须对齐 Web 对话页输入框的 `EffortChip`**（owner 2026-11-15 追加：「tui端slash命令/model面板里选择思考强度时的思考强度列表的语义和UI/UX也必须跟web端对话页的输入框里的思考强度选择对齐」）：
   - 不只是给审批面板加第二屏；`/model` 现有的 `model-effort` picker **本身就要改造**，让**列表语义与 UI/UX** 与 `frontend/src/components/chat/EffortChip.vue` 一致：固定 7 档顺序 + 未知档位追加、「默认」空值项的显示条件、等于行默认项的齿轮图标标记（裁决 7）、档位显示名与值名双写、光标落在会话当前钉值、选中同值不写。
   - 逐项差异与改造点见 **§2.7**，实施在 **Phase E2**。原本被列为「只作样板读、不改」的 `pkg/turn/executor.go` 因此转入 In scope。
   - **对齐的是列表语义，不是把终端做成下拉 chip**：TUI 保持 rich picker 形态与两列布局，`Title: "Reasoning effort"` / `Hint` 保留；onboard `/connect` 向导（末项 `Provider default`）不改。`SlashPickerCard.vue` **不改**（Web 无内置 slash 命令入口，见 §0.6）。
6. **档位列表的语言规则（owner 2026-11-15 第三条追加裁决）**：「**tui端是全英文，档位列表必须全英文。web端的档位列表在中文界面用中文，英文界面用英文**」。因此：
   - **TUI**（`/model` 第二步 + 审批面板第二屏）：一律英文 —— `Default` / `Off` / `Minimal` / `Low` / `Medium` / `High` / `xHigh` / `Max`；「模型设置」是齿轮图标 `⚙` 不是档位文案（裁决 7），不进这张英文表。Go 侧英文表由 §2.7 / Step 15a 提供。
   - **Web 只有两处档位列表**：对话页输入框的 `EffortChip`（计划 300 已按 i18n 落地，本计划不改它）与审批面板第二屏（Step 18）。两者一律走 `effortLevelLabel()` + `chat.effort*` i18n 键 —— 中文界面「默认 / 关闭思考 / 极低 / 低 / 中 / 高 / 最高 / 极致」，英文界面 `Default / Off / Low / … / Max`，**不得硬编码任一语言**。「模型设置」标记是齿轮图标（裁决 7），其 tooltip / aria-label 走 `chat.effortModelSetting`（zh「模型设置」/ en「Model setting」）。
   - **Web 对话页输入框不提供 TUI 内置 slash 命令**（owner 2026-11-15 裁决）：`/model` 等内置命令仅 TUI 端可用；Web 输入框的 slash 只用于**显式触发技能**。因此 **Web 端不存在 `/model` 第二步档位列表**，`frontend/src/components/chat/SlashPickerCard.vue` 不在本计划范围。⚠️ 代码现状 `pkg/turn/slash.go:78` 的 `/model` 仍标 `AllowedSurfaces: []Surface{SurfaceWebChat, SurfaceTUI}`、`executor.go:774` 注释写 "on every surface" —— 这是与产品语义不一致的**残留**，本计划**不把它当作 Web 可达的依据，也不修改它**（另记于 §10，另立项处理）。
   - **保留「`PickerItem.Label` 保持裸协议 id + 两个 `omitempty` 字段」的设计**：Web 侧没有该列表后，把 `Label` 直接改成英文显示名在技术上已可行，但保留裸 id 不依赖「Web 永远不会有 picker」这个假设，且把 TUI 文案留在 TUI 渲染层，更稳；`ValueLabel` 只服务 TUI。
   - **模型名称**（`provider/model`、`modelName`、第一屏的 `Label`）不受此约束，中英文界面都保持原样英文。
7. **「模型设置」标记一律用齿轮图标，不用文字；且必须紧贴档位名**（owner 2026-11-15 追加：「Model setting改成图标，不要用文字」「(model setting)也改成图标」；同日截图反馈「齿轮图标与思考档位距离离的太远……与第二张截图中的UI效果保持一致」）：
   - **TUI**（审批第二屏 Step 14 + `/model` 第二步 Step 15c）：等于行默认（`DefaultEffort` / `ConfiguredEffort`）的那一项，在档位显示名**紧右侧**渲染强调色齿轮 `⚙`（`panelAccentStyle`，fg110），**不是**文字 `(model setting)`、也**不**甩到远端描述列。原型 `modelEffortScene`（`tui-prototype.html:880-881`：`label:o.label+(o.value?'  '+o.value:'')`、`mark:o.fromSetting?{glyph:'⚙',style:ACC}:null`、`desc:''`）与审批第二屏即此效果（「High ⚙」紧贴，值名双写也紧跟在名字列）。
   - **Web 审批第二屏**（Step 18）：用 lucide `Settings` 图标（`PlanApprovalDock.vue:225` 已 import lucide，补 `Settings`），`title` / `aria-label` 复用 `t('chat.effortModelSetting')`（zh「模型设置」/ en「Model setting」）——**图标本身无文字**，文案只进 tooltip / 无障碍标签。原型 `plan-handoff-preview.html:680` 的 `.etag`（`<use href="#i-gear"/>`）即此效果，且 `.eleft` 用 `gap:7px` 把齿轮紧贴档位名（`:300`）。
   - **i18n 键 `chat.effortModelSetting` 保留**（tooltip / aria-label 用），不新增逐档文案。
   - **对话页 `EffortChip.vue:33` 的文字 `<em>` 标签刻意不改**（计划 300 已落地、§4 Out-of-scope）：owner 的图标裁决只针对本计划新增的审批 / `/model` 两处列表，不回改聊天页 chip。
   - **齿轮紧贴是硬要求**：TUI `/model` 第二步因 `SelectItem`（`channels.go:605-615`）无 mark 字段、`SelectRich` 名字列（`panel_columns.go:109-134` `columnRows` 的 `l.col`）单样式纯文本，需一处最小渲染扩展才能把强调色齿轮紧贴名字（见 Step 15c）；**不得**把齿轮塞回远端描述列（那正是 owner 报障的「距离太远」）。
5. **零 DB 迁移**（技术裁决，由代码事实推出）：hand-off 选中的模型今天只落在 `fb_actions.answer_json`（JSON 文本）与 `<home>/state/modes/<sid>.json` 的 `PlanHandOff`（JSON 文件），两者都是可加 `omitempty` 字段的 JSON 结构；**不需要**改 `fb_session_model_state`、不需要 `stateSchemaVersion`（当前 36）递增、不需要三方言迁移。

### 行为矩阵（★ = 本计划新增/改变）

| 场景 | 今天 | 落地后 |
| --- | --- | --- |
| 选模型（无档位行） | 点/按 Enter 即提交 | 不变（★ 跳过第二步，effort 为空） |
| 选模型（有档位行） | 点/按 Enter 即提交 | ★ 进入第二屏；默认停在 `default_effort` 项并标齿轮图标 `⚙`（裁决 7） |
| 第二屏直接确认 | — | ★ 提交 `default_effort` 具体值，随决策落盘并作用于子代理 |
| 第二屏改选别的档位 | — | ★ 提交所选具体值 |
| 第二屏 Esc / 返回 | — | ★ 回到第一屏（模型列表与光标保留），再 Esc 回 verdict 行（TUI）/ 关掉 picker（Web，保持 289 语义） |
| 该行没配 `default_effort` | — | ★ 首项「默认」(`""`) + 档位；选「默认」提交 `""`（引擎不覆盖 params） |
| 伪造/越界档位请求 | 无此字段 | ★ 网关 400（`reasoning effort is not offered for this model`） |
| 「再审」 | 沿用记录里的模型 | 不变（**已知限制见 §10**） |
| 旧 `answer_json`（无 effort）回放 | — | 必须仍可解码、行为与今天一致（兼容回归测试） |
| 原型闸门 | 仅 Web 有（283 时期 TUI/网关不受限） | ★ **TUI + Web 两份都先出、都经 owner 确认，且先于任何源码改动**；TUI 稿还须含 `/model` 第二步 |
| TUI `/model` 第二步的档位顺序 | `chosen.ReasoningEfforts` 原序 | ★ 固定 7 档序 + 未知追加（对齐 `EFFORT_LEVELS`） |
| TUI `/model` 第二步的「默认」项 | 没有 | ★ 会话无钉值**或**行没配 `default_effort` 时首项 `Default`（`""`） |
| TUI `/model` 第二步的行默认标记 | 只有 `Current`（会话当前钉值） | ★ 等于 `ConfiguredEffort` 的项在档位名紧右侧另标齿轮 `⚙`（语义与 `Current` 分开，裁决 7） |
| TUI `/model` 第二步的每行文案 | 裸 id（`high` / `xhigh`） | ★ 英文显示名（`High` / `xHigh`）；显示名与值名忽略大小写后不同时才双写值名（英文仅 `Off  none`），对齐 Web `effortLevelLabel` 的 en 表与 `EffortChip.vue:88` |

---

## 1. Why this matters

计划 283 给 `exit_plan_mode` 审批面板加了「换模型执行」「请其他模型评审」两个动作，用户能挑模型；但 `PlanReviewModelOption` 从构造到两端 UI 都没有思考强度字段，审批决策、落盘、子代理派发也都没有。结果是：用户能指定「用哪个模型」，却无法指定「它想多久」，只能被动接受模型行配置的默认档位。

尴尬的是**引擎侧早就准备好了**：`run.SubagentModelOverride` 有 `Effort string` 字段（`pkg/run/subagent.go:4349-4353`），`clientFor` 的缓存键已含 effort（`:4680-4682`），`ConfiguredModelClientWithEffort` 明确「非空覆盖、空则保留行默认」（`:4744-4746`）。缺的只是把 UI 选择一路接到这个字段。同理，档位数据在 `turn.ModelChoices` 里也已算好（`ReasoningEfforts` / `ConfiguredEffort`），只是 `PlanReviewModelOptions` 没把它拷进审批候选。所以这是一次**接通既有能力**的工作，不是新建机制。

顺带修掉一处**已经存在的不一致**：TUI `/model` 第二步的档位列表今天用 catalog 原序、只有裸 id、没有「默认」项、也不区分「模型行配置的档位」与「会话当前钉值」；而 Web 对话页输入框的 `EffortChip` 早已按固定 7 档序、有条件地给首项「默认」、并给等于行默认的那一项打「模型设置」文字标签（聊天页 chip 保留文字；本计划新增的审批 / `/model` 两处列表改用紧贴档位名的齿轮图标，裁决 7）。同一个产品里两处「选思考强度」呈现两种语义，会让人以为改了设置没生效。本轮把 `/model` 也对齐到 Web 的语义与 UI/UX（§2.7 / Phase E2）。

---

## 2. Current state（全部由本次调研逐行打开核实）

### 2.1 两端审批面板

**TUI** —— `pkg/tui/overlays.go` 的 `approvalOverlay` 是**阻塞式 overlay**，直接读 `rawSelector` 按键，**不经过 reducer/Msg 循环**（`overlays.go:33-37` 注释）。状态字段在 `:38-103`，其中：

```go
// pkg/tui/overlays.go:65-80
planReviewPicking bool
planPickerPurpose planPickerPurpose   // :131 定义；:134-135 常量 pickReview / pickHandOff
planReviewCursor  int
planReviewShown   bool
planReviewTopRow  int
planReviewFilter      filterField
planReviewInFilter    bool
planReviewFiltered    []int
```

关键函数链：`enterPlanPickerMode(purpose)` `:1393-1407` → `handlePlanReviewKey` `:1431-1542` → `exitPlanReviewMode()` `:1417-1425`。**选模型即返回决策**（`:1504-1519`）：

```go
case rawKeyEnter:
    if len(models) == 0 { return turn.ToolApprovalDecision{}, false }
    selected := models[o.planReviewCursor]
    purpose := o.planPickerPurpose
    o.exitPlanReviewMode()
    if purpose == pickHandOff {
        return turn.ToolApprovalDecision{Approved: true, HandOffModel: &selected}, true
    }
    return turn.ToolApprovalDecision{RequestPlanReview: &selected}, true
```
Esc 在 `:1436-1437` 是「回 verdict 行」，不是「回上一步」。渲染：`buildApprovalContent` `:522-939`，问题行 `:612-621`、列表分支 `:755-802`、footer `:920-933`；两列模型列表 `planReviewList` `:1560-1625`（计划 282 产物）。搜索/过滤在 `pkg/tui/review_filter.go:20-52`。

**Web** —— `frontend/src/components/chat/PlanApprovalDock.vue` 的 `picking` 分支（`:52-72`）是**内联展开的按钮列表**，`pickModel()` `:355-361` **点选即提交**，组件内无中间暂存：

```ts
function pickModel(model: PlanReviewModelOption) {
  if (pickPurpose.value === 'handoff') { void handOffTo(props.action, model); return }
  void requestReview(props.action, model)
}
```
状态在 `frontend/src/composables/usePendingActions.ts:81-84`：`modelPicking` / `modelPickPurpose`（均按 `action.id` 键控）。`closeModelPicker` `:425-427` 只关不切 purpose（计划 289 的修复，不要破坏）；提交函数 `requestReview` `:432-439`、`handOffTo` `:444-454`、`reviewAgain` `:459-462`。`onEsc()` `PlanApprovalDock.vue:365-371` 在 `picking` 时只调 `closeModelPicker`。

### 2.2 数据层：档位已算好，只差下发

```go
// pkg/turn/models.go:676-712（节选）
type ModelChoice struct {
    ID string; Label string; Provider string; Model string; ModelName string
    BaseURL, APIPath, URLVerbatim string
    ReasoningEfforts []string   // :703  「empty means the picker has nothing to ask」
    ConfiguredEffort string     // :708  模型页「思考强度」，即该行 default_effort
    SupportsImages bool
}
```
`ModelChoices`（`:729-759`）在 `:753-754` 已把两者算好：`choice.ReasoningEfforts = ModelReasoningEffortsRow(...)`（按行 + 协议裁剪，见 `:198`）：`choice.ConfiguredEffort = appcfg.ReasoningEffort(configured.Params)`。

但 `pkg/turn/approval.go:1289-1335` 的 `PlanReviewModelOptions` 遍历 `entries := ModelChoices(cfg, agentName)`（`:1290`），`out = append(...)`（`:1324-1332`）**只填 7 个字段**：`ID / Provider / Model / ModelName / Current / Endpoint / EndpointTail`。候选类型定义在 `pkg/turn/model.go:629-645`，同样 7 个字段。

**候选注入点**（两 surface 共用，改源头即可，不用动注入点）：
- `pkg/gateway/approval.go:49-59` `gate.ReviewModels = func(sessionID string) …`
- `pkg/tui/chat_surface.go:611-613` `ReviewModels: func(sessionID string) …`
- `pkg/turn/approval.go:1868-1870` `req.PlanReviewModels = g.ReviewModels(sessionID)`

**网关 wire**（REST `GET /api/chat/sessions/:id/approval-request`，前端在收到 `requires_action` 后另发此请求拉取，见 `usePendingActions.ts:258-270`）：`pkg/gateway/api_extra.go:4524` handler、`:4560-4567` `req.PlanReviewModels → planReviewModelWire` 逐字段拷贝、`:4624-4634` `planReviewModelWire` 定义（6 字段，无 effort）。前端类型 `frontend/src/lib/api.ts:1659-1672` `PlanReviewModelOption`（6 字段）。

> 注意：`/api/llm-models/choices` 是**另一套**端点（`pkg/gateway/models_api.go:624-653`，返回 `efforts`/`default_effort`，agent 作用域）；本计划**不走它**，理由：审批候选来自 `ReviewModels`（配置行 + 会话当前标记），另发请求会引入不一致与竞态。改为在既有 `PlanReviewModelOptions` 里就地补齐，零新请求。

### 2.3 协议层：effort 一个字段都没有（逐字段拷贝点全清单）

| # | 位置 | 现状 |
| --- | --- | --- |
| 1 | `pkg/turn/model.go:629-645` | `PlanReviewModelOption` 7 字段 |
| 2 | `pkg/turn/model.go:679-704` | `ToolApprovalDecision`：`RequestPlanReview *PlanReviewModelOption` `:695`、`HandOffModel *PlanReviewModelOption` `:699`、`RequestPlanReviewAgain bool` `:703` |
| 3 | `pkg/turn/approval.go:1008-1014` | `type Model struct { ID, Provider, Model string }` |
| 4 | `pkg/turn/approval.go:62-75` | `ApprovalReply.HandOffModel *Model` |
| 5 | `pkg/turn/approval.go:81-85` | `handOffModelJSON{ID,Provider,Model}`，写进 `answer_json` 的 `hand_off_model` |
| 6 | `pkg/turn/approval.go:89-98` | `handOffModelJSONOf` 逐字段投影 |
| 7 | `pkg/turn/approval.go:238-262` | `ApplyResolvedEffect` 解码 + `:257-262` 构造 `Model` |
| 8 | `pkg/turn/approval.go:356-364` | `Decide` 编码 `answer` |
| 9 | `pkg/turn/approval.go:419-444` | `sameApprovedReply` 解码 + `sameHandOff` 三字段比较（幂等） |
| 10 | `pkg/turn/approval.go:741-757` | `ActionHandOffModel` 解码 + 构造 `Model` |
| 11 | `pkg/turn/approval.go:1625-1630` | `RunPlanReview` 逐字段构造 `model := Model{ID,Provider,Model}` ← **会吞掉 Effort，必须改** |
| 12 | `pkg/tool/session_tools.go:479-502` | `actionHandOffModel` 是 tool 包的 twin 解码（注释说明因包依赖不能复用 turn），产出 `state.PlanHandOff` |
| 13 | `pkg/state/mode.go:45-53` | `PlanHandOff{ModelID,Provider,Model}` JSON（`<home>/state/modes/<sid>.json`） |
| 14 | `pkg/gateway/api_extra.go:4424-4437` | `handleActionsApprove` body 的 `HandOffModel *struct{ID,Provider,Model}` |
| 15 | `pkg/gateway/api_extra.go:4488-4492` | `reply.HandOffModel = &turn.Model{ID,Provider,Model}` |
| 16 | `pkg/gateway/api_extra.go:4670-4680` | `handleActionPlanReview` body `{ModelID,Provider,Model,Again}` |
| 17 | `pkg/gateway/api_extra.go:4732-4739` | 构造 `model := turn.Model{...}`，`again` 分支逐字段覆盖 |
| 18 | `frontend/src/lib/api.ts:1630-1656` | `ActionApprovalBody.handOffModel?: {id,provider,model}` |
| 19 | `frontend/src/lib/api.ts:3716-3731` | `actionsApprove` 的 `toSnakeCase({...})` 白名单（`:3730` 只展开 `handOffModel`） |
| 20 | `frontend/src/lib/api.ts:3765-3776` | `actionPlanReview(id, body:{provider?,model?,modelId?,again?})` |
| 21 | `frontend/src/composables/usePendingActions.ts:437 / :448-452 / :461` | 三处请求体逐字段组装 |
| 22 | `pkg/tui/chat_surface.go:1130-1139` | `handOffModelOf(option) *turn.Model` 逐字段投影 |
| 23 | `pkg/tui/chat_surface.go:1113-1125` | `completeSurfacePlanReviewRequest` → `s.runPlanReview(ctx, actionID, selection, nil)` |
| 24 | `pkg/tui/chat_surface.go:717-728` | `service.Decide(ctx, actionID, turn.ApprovalReply{…, HandOffModel: handOffModelOf(...)})` |

`ws/SSE` 侧无需改动：`pkg/gateway/wsevents.go:188-201` 的 `approvalRequiresActionPayload` 是 map 整体透传；`approvalWSData`（`pkg/gateway/approval.go:222-254`）本就不带 `plan_review_models`，前端靠 REST 拉取。`pkg/gateway/run_control.go:681,828-836` 的 `forwardRunOpToActionOwner` 是请求体整体原样透传，新增字段自动通过。

### 2.4 引擎层：字段已存在，只差接线

```go
// pkg/run/subagent.go:4342-4353
type SubagentModelOverride struct {
    ModelID, Provider, Model string
    Effort   string   // :4347-4348 注释「empty when the model's own configuration decides」
    Entry *appcfg.AgentLLMProviderConfig; Version int64; Caps *llm.ModelOverride
}
```
- 空值语义：`ConfiguredModelClientWithEffort` `:4725-4748`，`:4744-4746` `if effort != "" { entry.Params = appcfg.WithReasoningEffort(entry.Params, effort) }`，注释 `:4729-4731`「An empty effort leaves the effort the chain entry configures for that model in place」。
- `modelClientFromEntry` `:4769-4777` 同规则；`subagentOverrideClient` `:4755-4760` 把 `override.Effort` 传下去。
- 缓存键已含 effort：`:4680-4682` `key += "/e" + effort`。
- 记录写入：`prepareSubagentExecutionResolved` `:2029-2036` `entry.ReasoningEffort = strings.TrimSpace(pin.Effort)`；`:2395` 事件带 `ReasoningEffort: effort`。
- **review 派发**（缺口）：`pkg/process/run_executor.go:427-459`，`:438`：
  ```go
  override := run.SubagentModelOverride{ModelID: modelID, Provider: r.Model.Provider, Model: r.Model.Model}
  ```
  无 Effort；`:472` `run.ConfiguredModelClientWithEffort(cfg, agentName, modelID, …, "")` 也用空值。`r.Model` 是 `turn.Model`（`PlanReviewer` 结构），由 `pkg/gateway/api_extra.go:4761-4769` 的 `PlanReviewRun{Model: model}` / `process.PlanReviewer{Model: model}` 传入。
- **hand-off 派发**（缺口）：`handOffModelID(ctx, fac)` `pkg/run/subagent.go:4556-4572` 从 `state.Get(...).PlanHandOff.ModelID` 读行 id；`usableModelByID` `:4511-4554` 以它作为 `SubagentAllowed` 例外（`:4546-4548`）；`subagentModelOverrideForRef` `:4580-4599` 生成 pin，`:4594` `Effort: appcfg.ReasoningEffort(entry.Params)` —— **恒取行默认，没有用户选择的位置**。
- 「再审」`ContinuePlanReviewSubagent` `:4987-5033`，`:5026-5028` 注释「No model override here: the round runs on the model the record carries」；续跑 pin 由 `subagentRecordPin` `:2469-2498` 从记录读 `record.ReasoningEffort`（`:2480`），**但** `record.ModelLive && modelID != ""` 时 `:2494` 会用 `appcfg.ReasoningEffort(entry.Params)`（行默认）覆盖它。见 §10 已知限制。

### 2.5 语义样板（必须对齐的对象）

**聊天页 `frontend/src/components/chat/EffortChip.vue:72-92`** —— 本计划 Web 端档位计算的逐字样板：

```ts
const known  = EFFORT_LEVELS.filter((level) => props.efforts.includes(level))
const extra  = props.efforts.filter((level) => !(EFFORT_LEVELS as readonly string[]).includes(level))
const configured = props.defaultEffort ?? ''
const showDefault = props.effort === '' || configured === ''
const ids = showDefault ? ['', ...known, ...extra] : [...known, ...extra]
// fromSetting: id !== '' && id === configured   ← 行默认标记（EffortChip 渲染文字 <em>；本计划新增列表渲染紧贴档位名的齿轮 ⚙，裁决 7）
```
- `EFFORT_LEVELS = ['none','minimal','low','medium','high','xhigh','max']`（`frontend/src/lib/effortLabel.ts:34`），`effortLevelLabel(id)` `:26-30`；`''` → `chat.effortDefault`（「默认」/「Default」）。
- 聊天页提交：`ChatView.vue:1926-1941` `saveSessionModel(sid, { modelId, provider, model, effort })`；服务端 `pkg/gateway/api_extra.go:2090-2101`：「省略 effort = 模型切换，派生行配置值一次并钉住」「命名一个（含 `""`）则钉该值」「具体值必须在 `sessionModelEffortsFor` 内否则 400」。
- TUI `/model` 两步 picker（结构样板）：`pkg/turn/executor.go:866-934` `chooseModel` → `Picker{Command:"model-effort", Title:"Reasoning effort", …}`；`:914` 无档位直接结束；`chooseReasoningEffort` `:940-980` 用 `slices.Contains(chosen.ReasoningEfforts, effort)` 校验。TUI 递归开第二个 picker 在 `pkg/tui/run.go:5496-5498`。**注意这是 slash picker 递归，不是 overlay**；本计划在 `approvalOverlay` 内新增一屏，不复用 `runSlashPicker`。
- ⚠️ **不要照抄** `frontend/src/components/settings/GoalCheckModelGroup.vue:135-148` 的「首项 `''` = 跟随模型设置」方案（计划 299），语义已被 §0 裁决排除。

### 2.6 仓库约定（执行器必须遵守）

- **TUI 文案全英文**（owner 2026-11-12 裁决；`overlays.go:615/618` 现有问题行即英文）。
- **Web i18n 成对**：`frontend/src/locales/index.ts:7 const zh = {` / `:2538 const en: Record<keyof typeof zh, string> = {` —— 英文表键集由中文表约束，漏一个 `pnpm typecheck` 直接红。可复用 `chat.effortChipTitle`(`:2112/:4630`)、`chat.effortModelSetting`(`:2113/:4631`)、`chat.effortDefault`(`:2117/:4635`)、`chat.effortNone`…`chat.effortMax`(`:2114-2121/:4632-4639`)。
- **两端 UI 各自有权威原型**（本计划的硬闸门，见 Step 0）：
  - **TUI**：`docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/tui-prototype.html`（**1784 行**；原始基线 SHA-256 `519e7228d99590efd532dc0c311bbe1bccba14acf45d6fc66dafe6f18a8a8228`，advisor 设计稿 SHA-256 `8f169c3e9a90650d43fc3b04f0489ee5fa33a137b79fce148e924703518ec197`，owner 2026-11-16 已确认）。**行号以设计稿为准、可能再漂移，定位优先 grep 符号名**：`/* ---- E. 计划审批（exit_plan_mode） ---- */`（`:1196`）、`GATE_MODELS`（`:1207`，9 个演示模型含 efforts/defaultEffort）、`gateContent`（`:1309`，第一屏单问句 `:1356-1358` + **空一行** + ≥8 filter 条 `:1364-1371`、第二屏单列 + 问句下空一行 `:1345`）、`gateScreen`（`:1448`）、`gateState`（`:1484`，含 `filter/fc/inFilter/filtered` + `effortPicking/effortCursor/effortModel/effortOptions`）、`modelEffortScene`（`:876`，齿轮紧贴 `:880-881`）；scene 名 `exit-plan-effort` / `exit-plan-effort-handoff` / `exit-plan-effort-default` / `exit-plan-filter`（`:1660`）/ `model-effort`（`:893`）/ `model-effort-default`。另有 `/connect` 的强度页可作**视觉**样板（grep `LEVELS` 与 `{id:'effort',kind:'picker'}`）—— ⚠️ 它用的是「`Provider default` 末项」，**语义已被 §0.2 排除，只借视觉、不要照抄选项结构**。计划 282 的 `:37` 明写该原型「这就是目标，逐行对照」。
  - **Web**：`docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/plan-handoff-preview.html`（**852 行**；原始基线 SHA-256 `ca478be73dd40e6a8a525b06883e024f84e48cd9878c0c79ba265b6c87a5779c`，advisor 设计稿 SHA-256 `5e082cb36bc2a89e55315cf1aa9c7b332028eee3cae9e879973fb7fb7f5ed297`，owner 2026-11-16 已确认）。`PlanApprovalDock.vue` 的 scoped CSS 注释逐条引用它；关键锚点（grep 符号）：`.xp-dock` / `.gate-ask` / `.xp-pick` / `.xp-model`、`chooseModel`（`:661`）、`effortScreen`（`:668`，齿轮 `.etag` 在 `.eleft` 内 `:680`）、`.eleft { gap:7px }`（`:300`，齿轮紧贴）。
  - 原型目录 `docs/plan/` **被 `.gitignore:76` 忽略**，git 门发现不了改动 → 用 `shasum -a 256` 记录前后哈希作为闸门证据（历史教训：`plans/README.md:140/144`）。
- **Go 错误文案**：网关既有双语文案用 `llmRefuse`（`pkg/gateway/models_api.go:198-200`），但审批 handler 现有错误都是英文短句（`:4474`、`:4485`、`:4748`），**沿用英文短句**，不要引入新机制。
- **测试落点**：Go 测试函数名 `TestXxx` 驼峰、与生产文件同名（`pkg/architecture` 的 `TestTestFilesCorrespondToProductionFiles` 要求 `X_test.go` 必须有 `X.go`，**不要新建没有同名生产文件的测试文件**）。前端 vitest 用 `describe/it` 句子式用例名，与 `PlanApprovalDock.test.ts` 现有风格一致。
- **不要放宽既有断言**；凡是需要改既有断言的，必须先证明是「形状新增」而非行为回退（见 §8）。

### 2.7 `/model` 第二步的现状与对齐清单（本轮追加范围）

**改造对象**：`pkg/turn/executor.go:920-933` `chooseModel` 构造的第二个 picker（`Command: "model-effort"`）：

```go
// pkg/turn/executor.go:925-933（现状，逐字）
current := settings.Selection.Effort
if !settings.Selection.Set {
    current = settings.LiveSelection.Effort
}
items := make([]PickerItem, len(chosen.ReasoningEfforts))
for i, effort := range chosen.ReasoningEfforts {
    items[i] = PickerItem{Value: encodeEffortChoice(chosen.ID, chosen.Label, effort, changed), Label: effort, Current: effort == current}
}
return Result{Handled: true, Reply: reply, Picker: &Picker{Command: "model-effort", Title: "Reasoning effort", Hint: "How long " + name + " thinks before it answers", Items: items}}
```

链路上其余相关点：`:900-913` 选模型变化时 `SelectModel(..., nil)`（runner 派生行配置值一次并钉进会话行）、`:914` 无档位不出第二步；`:940-980` `chooseReasoningEffort`（`:961` 校验 `slices.Contains(choices[at].ReasoningEfforts, effort)`，`:969-975` 同值不写，`:976` `SelectModel(..., &effort)`）；`:985-1004` `encodeEffortChoice` / `decodeEffortChoice`（4 段换行串）；`pkg/turn/model.go:385-402` `Picker` / `PickerItem`（`Value/Label/Description/Current/EndpointTail`）；`pkg/tui/run.go:5356-5390` `runSlashPicker`（`:5361` `SelectItem{Label, Description}`、`:5368-5370` 从 `item.Current` 取 `defaultIdx`、`:5376` `SelectRich`）；`pkg/tui/overlays.go:5676-5699` `richPickerState.panel` 的两列渲染、`:5751-5759` `defaultIdx` 决定光标落点。

**对齐目标**：`frontend/src/components/chat/EffortChip.vue:20-34, 72-100` + `frontend/src/lib/effortLabel.ts:16-38` + `frontend/src/locales/index.ts:4630-4639`（en 表）。语义样板已在 §2.5 逐字摘录。

| # | 维度 | TUI 现状 | Web `EffortChip` | 要改 |
|---|---|---|---|---|
| 1 | 档位顺序 | `chosen.ReasoningEfforts` 原序（catalog/override 决定） | `EFFORT_LEVELS`（`none,minimal,low,medium,high,xhigh,max`）过滤 + 未知档位追加在后 | ✔ |
| 2 | 「默认」空值项 | 完全没有（`len(items)==len(ReasoningEfforts)`） | `showDefault = props.effort === '' \|\| configured === ''` 时首项 `''`（`effortLevelLabel('')` = `Default`） | ✔ |
| 3 | 行默认标记 | 只有 `Current`（会话当前钉值），语义是「现在用的是哪个」 | `fromSetting: id !== '' && id === configured` → EffortChip 渲染文字 `<em>Model setting</em>`（聊天页保留），语义是「模型页给这行配的档位」 | ✔ **两者必须分开**；本计划新增列表渲染为紧贴档位名的齿轮 `⚙`（裁决 7） |
| 4 | 每行标签 | 裸 id `high` / `xhigh` | Web：`effortLevelLabel(id)` **按界面语言**（中文「高」/ 英文 `High`）；`option.value` 在与 label 不同时双写值名 `<code>` | ✔ TUI 固定英文、Web 走 i18n |
| 5 | 当前选中 / 光标 | `Current: effort == current` → `SelectRich` 光标落该项 | `aria-selected` / `--on` 同样是会话钉值 | ✔ 已一致，保留 |
| 6 | 选中同值 | `:969-975` 相同值不写、给 "Still using…" 回复 | `EffortChip.vue:98` `if (next === props.effort) return` | ✔ 已一致，保留 |
| 7 | 空值提交 | `:961` `slices.Contains` 会拒 `""`；`:974/:979` 的回复文案会拼出 `at  reasoning effort.` | 选「默认」提交 `effort: ''`；服务端 `api_extra.go:2096` 只校验非空档位 | ✔ 需放行并修文案 |

**几个必须知道的事实（防踩坑）**：

- **Web 对话页输入框没有 TUI 内置 slash 命令**（owner 2026-11-15 裁决）：`/model` 等内置命令仅 TUI 端可用，Web 输入框的 slash 只用于**显式触发技能**。因此 **Web 端不存在 `/model` 第二步档位列表**，`frontend/src/components/chat/SlashPickerCard.vue` 与 `useChatStream.ts` 的 `SlashPicker` 都不在本计划范围。
  - ⚠️ **不要被单元测试误导**：`pkg/gateway/slash_handlers_test.go:222-226` 用 `"webchat"` 频道调 `parseSlashCommandWithOptions` 并断言 `res.Picker.Command == "model"`，那只是**直接调内部辅助函数**（`server.go:3033`），不是 Web 输入框的实际产品入口；`server.go:2152-2157` 的注释也写明 Web 输入框的 slash 是「how a skill's own command is activated」。曾据此误判「Web 也会显示档位列表」，已撤销。
  - ⚠️ **代码残留（不在本计划修）**：`pkg/turn/slash.go:78` 的 `/model` 仍标 `AllowedSurfaces: []Surface{SurfaceWebChat, SurfaceTUI}`，`executor.go:774` 注释写 "on every surface"。这与产品语义不一致，但**本计划不把它当作 Web 可达的依据，也不改这两处**；如需收口另立项（§10 留痕）。
- **`PickerItem.Label` 保持裸协议 id**：`ValueLabel` 只供 TUI 渲染层取英文，`FromSetting` 供渲染层打标记。Web 侧没有这个列表，因此不需要任何 Web 侧本地化改造；保留裸 id 是为了不依赖「Web 永远不会有 picker」这个假设（§0.6）。
- **空 effort 的编码天然兼容**：`encodeEffortChoice(id, label, "", false)` → `"id\nlabel\n\nfalse"`，`decodeEffortChoice` 的 `strings.SplitN(value, "\n", 4)` 得到 `parts[2] == ""` ✔。但要补一条往返测试钉住它。
- **语言规则（§0.6）**：**TUI 文案全英文** —— `Default` / `Off` / `Minimal` / `Low` / `Medium` / `High` / `xHigh` / `Max`，逐字对齐 `frontend/src/locales/index.ts:4630-4639` 的 en 表；Go 侧自建英文表（不读前端 i18n），由跨端一致性测试钉住。「模型设置」是齿轮图标 `⚙` 不是档位文案（裁决 7）。**Web 两处档位列表**（对话页 `EffortChip`、审批第二屏）一律走 `effortLevelLabel()` + `chat.effort*` 键，中文界面「默认 / 关闭思考 / 极低 / 低 / 中 / 高 / 最高 / 极致」、英文界面 `Default / Off / … / Max`，**不硬编码任何语言**；齿轮的 tooltip / aria-label 走 `chat.effortModelSetting`。模型名称（第一屏 `Label`、`provider/model`、`modelName`）不受此约束，本来就是英文。
- **onboard `/connect` 向导不改**：`pkg/tui/setup.go:2063-2128` 的 `promptEffortChoice` 把 `Provider default` 放**末项**（`setup_test.go:1569` 断言 `{"minimal","low","medium","high", reasoningEffortUnset}`），与本次「首项 + 有条件的默认项」语义不同。它是独立向导，不在本轮范围；**不要顺手改它，也不要放宽它的断言**。
- **`/model` 第一步模型列表不改**：端点列由计划 267 定稿（见 `pkg/tui/run.go:5363-5365` 注释「the path is what tells two rows of one provider apart (plan 267)」），按行 id 去重属 259–268 模型配置入库系列；本轮只改第二步 `model-effort` 的 items。
- **原型落点**：`tui-prototype.html` 的 `A · /model 选择器` 场景组（计划 282 称「原型 01」）与 `:829 LEVELS` / `:907 {id:'effort',kind:'picker'}` 的 `/connect` 强度页；Step 0a 要在原型里画出对齐后的 `/model` 第二步。

---

## 3. Commands you will need

全部来自 `FOREBRAIN.md`、`Makefile`、`.github/workflows/ci.yml`、`frontend/package.json` 与历史计划（`plans/282`、`plans/283`、`plans/295`），**本次未亲跑，执行器第一步先自检命令可用**。

| Purpose | Command | Expected on success |
|---|---|---|
| Go 构建 | `CGO_ENABLED=1 go build -tags fts5 ./...` | exit 0 |
| Go vet | `go vet ./...` | exit 0 |
| gofmt | `gofmt -l cmd pkg third_party plugins` | 无输出 |
| Go 定向单测 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/turn -run '^TestName$' -count=1 -v` | PASS，且 `grep -c '^--- PASS: Test'` ≥ 1 |
| Go 回归面 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/turn ./pkg/gateway ./pkg/tui ./pkg/run ./pkg/process ./pkg/state ./pkg/tool ./pkg/config ./pkg/architecture -count=1` | 与干净基线逐条一致（见下方基线噪音） |
| 架构守卫 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` + `scripts/package-graph.sh` + `diff /tmp/graph-310-before.json pkg/architecture/testdata/graph.json`（开工前快照见 §5；**不用 `git diff --exit-code` 对 HEAD**——graph.json 有其它批次的未提交改动） | 全绿、相对开工前无新增 diff |
| 前端 typecheck | `cd frontend && pnpm typecheck` | exit 0（`vue-tsc --noEmit`） |
| 前端单文件测试 | `cd frontend && pnpm exec vitest run src/components/chat/PlanApprovalDock.test.ts` | 全部 PASS |
| 前端构建 | `cd frontend && pnpm run build` | exit 0 |
| 原型哈希（Step 0） | `shasum -a 256 docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/{tui-prototype,plan-handoff-preview}.html` | 两条输出；改后两值都与 §2.6 基线不同 |
| TUI 原型脚本语法 | `python3 -c 'import re;print("\n".join(re.findall(r"<script[^>]*>(.*?)</script>", open("docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/tui-prototype.html").read(), re.S)))' > /tmp/tui-proto-310.js && node --check /tmp/tui-proto-310.js` | exit 0 |
| Web 原型第二屏结构 | `grep -c 'exit-plan-effort' docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/plan-handoff-preview.html` | ≥ 1 |
| e2e 单 spec | `FOREBRAIN_E2E_SPEC="e2e/exit-plan-approval.spec.ts e2e/office-plan-approval.spec.ts" scripts/acceptance/web_e2e.sh` | 全 PASS |

**已知基线噪音（不要当成新回归，也不要放宽）**：
- `pnpm test -- <路径>` **不过滤**，会跑全量并因 `frontend/src/components/models/ModelFormDrawer.vue:575` 的 `capsTimer` TDZ 产生 ~10 个 unhandled rejection 以 exit 1 结束（`plans/295:177`）。**单文件必须用 `pnpm exec vitest run <path>`**。
- 干净基线在 macOS 无 sandbox 的机器上，`pkg/tui` / `pkg/gateway` / `pkg/run` 有既有失败（`plans/README.md:117`：`no sandbox available`、临时 home 缺 model catalog）。判据是「FAIL 名单与干净基线逐条一致、无新增」，**不得写成通过**。需要对照时把 `git archive 1c824c33 | tar -x -C /tmp/...` 建干净副本跑同一命令。
- `FOREBRAIN_HOME` 必须是临时目录（`FOREBRAIN.md:139`：不 pin 会写真实 home 并卡十分钟锁等待）；冷构建若 pdfium 报错需 `export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"`（`FOREBRAIN.md:17`）。

---

## 4. Scope

**In scope（只改这些）**

Go：`pkg/turn/model.go`、`pkg/turn/approval.go`、`pkg/turn/models.go`（**共用档位排序 helper** 放这里）、`pkg/turn/executor.go`（**`/model` 第二步**）、`pkg/turn/approval_test.go`、`pkg/turn/model_test.go`、`pkg/turn/models_test.go`、`pkg/gateway/api_extra.go`、`pkg/gateway/api_extra_test.go`、`pkg/state/mode.go`、`pkg/state/mode_test.go`、`pkg/tool/session_tools.go`、`pkg/tool/session_tools_test.go`、`pkg/process/run_executor.go`、`pkg/run/subagent.go`、`pkg/run/subagent_test.go`、`pkg/tui/overlays.go`、`pkg/tui/run.go`（**`runSlashPicker` 的 effort 显示映射**）、`pkg/tui/chat_surface.go`、`pkg/tui/overlays_test.go`、`pkg/tui/chat_session_test.go`、`pkg/tui/commands_test.go`、`pkg/tui/channels.go`（确认行若需显示 effort；**+ `SelectItem.Mark` 字段**，Step 15c 齿轮）、`pkg/tui/render.go`（**`panelLine.mark` 字段**，Step 15c）、`pkg/tui/panel_columns.go`（**`selectableColumns` 变参 + `columnRows` 齿轮分支**，Step 15c）。⚠️ `tabbed_picker.go` / `project_servers.go` 虽也调 `selectableColumns`，但变参兼容、**零改动**（不在 scope）。

**原型（均 gitignored，Step 0 专用，两份都必须改）**：`docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/tui-prototype.html`、`docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/plan-handoff-preview.html`。

Web：`frontend/src/lib/api.ts`、`frontend/src/lib/api.test.ts`、`frontend/src/composables/usePendingActions.ts`、`frontend/src/components/chat/PlanApprovalDock.vue`、`frontend/src/components/chat/PlanApprovalDock.test.ts`、`frontend/src/locales/index.ts`（仅**新增** `chat.exitPlanEffort*` 两键）、`frontend/e2e/exit-plan-approval.spec.ts`。

**Out of scope（即使看起来相关也不要动）**

- `pkg/state/schema.sql` / `schema_mysql.sql` / `schema_postgres.sql` / `schema_migrations.go` —— **零迁移**，理由见 §0.5。
- `frontend/src/components/chat/SlashPickerCard.vue` 与 `frontend/src/composables/useChatStream.ts` 的 `SlashPicker` 类型 —— Web 输入框不提供 TUI 内置 slash 命令（§0.6），**没有 `/model` 的档位列表**，两者都零改动；`pkg/turn/slash.go:78` 的 `AllowedSurfaces` 残留也不在本计划修。
- `pkg/tui/setup.go`（onboard `/connect` 向导的 `promptEffortChoice`）—— 它的 `Provider default` 末项语义与本轮不同，**不改、不放宽 `setup_test.go:1569` 的断言**；`pkg/turn/executor.go` 的 `/model` **第一步模型列表**（267 定稿的端点列 + 按行 id 去重）也不改。第一屏模型名在中文界面保持英文（§0.6 已允许），不要顺手本地化。
- `pkg/gateway/models_api.go` 的 `/api/llm-models/choices`、`frontend/src/components/chat/EffortChip.vue`、`ChatView.vue`、`ModelChip.vue`、`frontend/src/locales/index.ts` 的**既有** `chat.effort*` 键 —— 聊天页语义已落地，本计划只对齐它，不改它（只**新增** `chat.exitPlanEffort*` 两个键）。
- `ContinuePlanReviewSubagent` / `ContinuePlanReviewSubagent` 相关续跑链、`reviewAgain` 请求体、`PreviousPlanReview` —— 「再审」不在范围（§10 有已知限制留痕）。
- `PlanReviewModelOption.MatchesSelection` 的**匹配规则**（pair + id 语义不变）、`hasNonCurrentModel` / `HandOffAvailable` 判定、`usableModels` 的授权与凭据规则、`list_usable_models` 工具定义与 schema 字节（前缀缓存）。
- `pkg/tool/format.go:2082` 的审批历史 `hand_off_model` 解码结构 —— 历史渲染只读 3 个字段即可，新增 effort 不显示；**但若该结构用 `json.Unmarshal` 到匿名 struct，新增字段不影响，无需改**（执行时确认一次，见 Step 6 的 Verify）。
- `frontend/src/lib/settingsSchema.ts` / `settingsSchema.test.ts` —— 本次两份原型都是审批面板/终端面板原型，**不要**把审批面板并进设置页的 `SETTINGS_FORMS`（会撞 `settingsSchema.test.ts:176` 的分组顺序全等断言）；`settingsSchema.test.ts` 的原型逐字对比只覆盖设置页，本计划不扩展它。

---

## 5. Git workflow

- 沿用仓库近期约定：**in-tree、执行者零 commit**（`plans/README.md:113`，289–297 批次；309 的改动也留工作区未 commit）。若派发方另建 worktree，按 `advisor/310-exit-plan-effort` 命名分支。
- 提交信息风格（若被要求提交）：conventional commits，参考 `git log --oneline -20`。
- **不 push、不建 PR、不合并**，除非 operator 明确指示。
- **两份**原型 HTML 都被 gitignore，改动不会出现在 `git status`；用 `shasum -a 256` 记录各自的前后哈希写进执行报告（Step 0 的闸门证据）。
- **开工前快照（Step 0c 闸门解除后、动任何源码前必做）**：工作区常有**其它会话/批次的未提交改动**——评审时见到 309 遗留的 `frontend/src/lib/settingsSchema.test.ts`、`pkg/architecture/testdata/graph.json`、`frontend/src/lib/testdata/`，以及另一会话正在改 `pkg/turn/slash.go` 的 `AllowedSurfaces`（把内置命令收窄到 TUI-only）。这些**都不是本计划的事**。先记录基线脏状态，收口时只判「本计划有没有新增越界改动」，不判「工作区是否干净」：
  ```sh
  git status --short > /tmp/status-310-before.txt
  cp pkg/architecture/testdata/graph.json /tmp/graph-310-before.json
  ```
  预先存在的脏文件**一律不碰、不提交、不还原**（尤其 `slash.go` 属 §4 Out-of-scope，见 §10）。

---

## 6. Steps

> **执行序：Step 0（两端原型 + owner 确认）→ Phase A…E → Phase E2（`/model` 第二步）→ Phase G/H。** Step 0 未收口前**不得修改任何源码文件**（`pkg/`、`frontend/src/`、测试文件都不行）。这是 owner 2026-11-15 追加裁决，比计划 283 时期「Part B 引擎/TUI/网关不受网页闸门限制」更严格，见 §10 留痕。Phase E2 与审批链路零文件冲突（除共用 `orderedEffortLevels`），可在 Phase A 落地后单独实施；但**同样受 Step 0a 的 `/model` 原型闸门约束**。

### Phase 0 — 两端原型都必须先出、都必须确认（**全计划的前置闸门**）

#### Step 0a：改 TUI 原型 `tui-prototype.html`

文件：`docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/tui-prototype.html`（gitignored，基线哈希见 §2.6）。**两处都要改**：① `/* ---- E. 计划审批（exit_plan_mode） ---- */` 场景组（`:1151` 起）加审批面板第二屏；② `A · /model 选择器` 场景组加**对齐后的第二步强度列表**（§0.4 的裁决也属 TUI 端，必须先出原型）。

在 `E` 场景组里加第二屏：

1. `GATE_MODELS`（`:1158-1169`）每个演示模型补 `efforts` / `defaultEffort` 字段，至少覆盖三种行：① 有 `default_effort` 且有多档；② 无任何档位（不出现第二屏）；③ 无 `default_effort` 但有多档（首项「默认」）。
2. `gateState` 的状态加 `effortPicking` / `effortCursor` / `effortModel` / `effortOptions`（照 `:1377-1425` 的 `state.picking` 状态机写法）。
3. `key(k)` 的 `state.picking` 分支（`:1389-1403`）：`Enter`（`:1391-1399`）改成「选中模型后进入第二屏」而不是直接 `state.requested` / `state.handed`；第二屏的 `Enter` 才产生决策，`Esc` 回第一屏且保留 `pickCursor`，第二屏再 `Esc` 回 verdict 行。
4. 渲染：新增问句 `Which reasoning effort should <provider/model> use? (Esc to go back)`、**空一行（owner 反馈 F）**、档位行（等于 `defaultEffort` 的行在档位显示名**紧右侧**标强调色齿轮 `⚙`，裁决 7）、footer `Esc to go back · Enter to confirm`。**单列即可**，不要复用模型列表的分栏（`columnRows`）—— 档位没有第二列内容；齿轮直接 `writeLine` 内联渲染（审批第二屏不走 `SelectRich`，无需 mark 扩展）。评审 / 交接 / 无默认档三条第二屏场景共用同一渲染分支，空行一处生效三处。
5. 新增 `scene('E · 计划审批','exit-plan-effort', ...)` 与交接版第二屏各一条；在说明区 `<div class="note"><h3>计划审批（<code>exit_plan_mode</code>）的语义</h3><ul>`（`:130`，注意 `exit_plan_mode` 外包 `<code>` 标签，grep 须按此字面）补一段第二屏语义（默认落在行配置档位、无档位跳过、Esc 分层）。
6. ⚠️ **只借视觉、不要照抄 `/connect` 强度页的选项结构**：`:829` `LEVELS` / `:907` `{id:'effort',kind:'picker'}` 那套把 `Provider default` 放在**末项**，语义已被 §0.2 排除。
7. **`/model` 第二步的新场景**（对齐 §2.7 的 7 项差异）：在 `A · /model 选择器` 场景组加一条 `model-effort` 的场景，逐行画出与 Web `EffortChip` 一致的列表 —— 固定顺序 `Off / Minimal / Low / Medium / High / xHigh / Max`、等于行默认的那行档位名**紧右侧**标强调色齿轮 `⚙`（裁决 7，**不**甩到第二列）、显示名右侧双写值名（**仅当显示名与值名忽略大小写后仍不同**，对齐 `EffortChip.vue:88` 的 `label.toLowerCase() !== id.toLowerCase()`；英文界面只有 `Off  none` 这一行双写，`High/high`、`xHigh/xhigh` 等忽略大小写后相同、**不**双写）、光标停在会话当前钉值；再加一条「行未配 `default_effort`」的场景展示首项 `Default`。⚠️ 原型里**不要**出现末项 `Provider default`（那是 `/connect` 向导的旧语义）。在对应的说明 `<div class="note">` 区补一段「此列表与网页输入框的思考强度选择同语义」的说明。
8. **第一屏必须镜像现有源码（owner 2026-11-15 截图报障「UI 界面已经过时」）**：选模型时**只有一行问句**，不再有 `Would you like to proceed?` —— 评审分支问 `Which model should review this plan? (Esc to go back)`、交接分支问 `Which model should carry out this plan? (Esc to go back)`（问句**替换**了 proceed 行，对齐 `pkg/tui/overlays.go:607-621` 的 question switch）；可选模型 **≥8** 行时问句下出现 filter 输入框（`pickerFilterMinRows = 8`，`overlays.go:4620`；过滤逻辑 `pkg/tui/review_filter.go`）。原型 `gateContent`（`tui-prototype.html:1356-1371`）已照此画：单问句 + **空一行** + 三行 filter 条（空 pad / 文本 / 空 pad）+ 空行接列表，键盘在 filter 时硬件 caret 反显、列表 `❯` 消失（二者互斥）。**问句与 filter 条之间的空一行是 owner 反馈 E**：现有源码 `overlays.go:757-759` 注释承诺「air above it」但 `:760-762` 漏写（仅其后 `:776` 有空行），原型原先照抄了该遗漏、现已补 `blank()`；执行器须按 **Step 14a** 在源码过滤条块首补 `writeLine("")`。单问句 + ≥8 filter 本身是现有行为；执行器不得把它「修回」旧的双问句 / 无 filter 形态。Web 第一屏同理只有一行 `.cap` 问句、**无 filter**（filter 是 TUI 专属，`PlanApprovalDock.vue:52-72`）。

**Verify**：
```sh
shasum -a 256 docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/tui-prototype.html   # 应 == owner 确认的设计稿 8f169c3e9a90650d43fc3b04f0489ee5fa33a137b79fce148e924703518ec197（advisor 已出稿，执行器核对相等即可，不重画；原始基线 519e7228…）
python3 -c 'import re;print("\n".join(re.findall(r"<script[^>]*>(.*?)</script>", open("docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/tui-prototype.html").read(), re.S)))' > /tmp/tui-proto-310.js && node --check /tmp/tui-proto-310.js   # exit 0
grep -c "exit-plan-effort" docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/tui-prototype.html   # >= 1
grep -c "model-effort" docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/tui-prototype.html       # >= 1
```
再在浏览器里打开原型，逐条点过：**审批面板**的选评审模型（第一屏单问句 + ≥8 行时的 filter）→ 第二屏 → 改档 → 确认；选交接模型 → 第二屏 → Esc 回模型列表 → 再 Esc 回 verdict；选无档位模型 → 直接提交；**`/model`** 的选模型 → 第二步列表（含紧贴档位名的齿轮 `⚙` 标记 + 首项 `Default` 两种行）→ 选中即生效。把四条路径的截图或逐帧描述写进报告。

#### Step 0b：改 Web 原型 `plan-handoff-preview.html`

文件：`docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/plan-handoff-preview.html`（gitignored，基线哈希见 §2.6）。在 `.xp-pick` 区块加**第二屏**（锚点：CSS `.xp-pick` `:283`、`.xp-model` `:286`、`.xp-dock` `:200`、`.gate-ask` `:244`；对应 HTML 用法在 `:418 class="xp-dock"`、`:434 class="gate-ask"`）：

- 标题**点名具体模型**「{model} 用哪档思考强度？」/「Which reasoning effort should {model} use?」（`{model}` 插值当前选中模型名，与 TUI 第二屏问句对齐，owner 反馈 D）+「返回选模型」入口；
- 档位按钮沿用 `.xp-model` 视觉，**按中文界面画档位名**（「默认 / 关闭思考 / 极低 / 低 / 中 / 高 / 最高 / 极致」，等于行默认的项在档位名**紧右侧**标齿轮图标，tooltip / aria「模型设置」，裁决 7）；行未配 `default_effort` 时首项「默认」。在原型内部以 `lang` 切换或注释给出**英文界面对应文案**（`Default / Off / Minimal / Low / Medium / High / xHigh / Max`，齿轮 tooltip「Model setting」），供 owner 一并确认；实现侧一律走 `effortLevelLabel()` + `chat.effort*` 键，**不得硬编码任一语言**（§0.6）；
- 演示数据至少覆盖与 Step 0a 相同的三种行；
- **应用内部不得出现给执行者/评审者看的说明**（owner 2026-11-12 硬约束：原型即 UI 稿、1:1 还原）；说明写进本计划。

**Verify**：
```sh
shasum -a 256 docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/plan-handoff-preview.html   # 应 == owner 确认的设计稿 5e082cb36bc2a89e55315cf1aa9c7b332028eee3cae9e879973fb7fb7f5ed297（advisor 已出稿，执行器核对相等即可，不重画；原始基线 ca478be7…）
grep -c 'exit-plan-effort' docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/plan-handoff-preview.html   # >= 1
```
浏览器打开逐条点过三条路径（同 Step 0a），写进报告。

#### Step 0c：owner 确认闸门

把**两份原型**（改后哈希 + 逐帧描述）一起交给 owner 确认，确认点至少含：① 第二屏的问句与档位行样式；② 默认项与**齿轮图标**标记（裁决 7，不是文字）；③ Esc 分层（第二屏 → 模型列表 → verdict / 关闭）；④ 无档位行跳过第二屏；⑤ 交接与评审两条路径共用同一第二屏；⑥ **TUI `/model` 第二步的对齐后列表**（顺序、齿轮 `⚙` 标记、显示名+值名双写、首项 `Default` 的出现条件）与 Web 输入框 `EffortChip` 是否一致；⑦ **齿轮紧贴档位名**（「High ⚙」，不甩到第二列 / 远端描述列，owner 截图反馈）；⑧ **第一屏镜像现有源码**（单问句 `Which model should review/carry out this plan? (Esc to go back)`、≥8 行出 filter；Web 无 filter）；⑨ **第一屏问句与 filter 输入框之间空一行**（反馈 E）；⑩ **第二屏问句与思考强度列表之间空一行**（评审 / 交接 / 无默认档三场景，反馈 F）。

**闸门判据（机器可查）**：执行报告里必须同时存在「两份原型的改后 SHA-256」与「owner 对两份原型均确认」的记录。任一缺失 → **STOP**，整份计划标 `BLOCKED-on-prototype`，不进入 Step 1。

**闸门状态：已解除（2026-11-16）**。owner 于 2026-11-16 明确确认两份原型（「两份原型已确认，必须将本期针对原型稿的修改写进计划，要求执行者按原型稿实现」）。确认稿哈希：**TUI `8f169c3e9a90650d43fc3b04f0489ee5fa33a137b79fce148e924703518ec197`（1784 行）**、**Web `5e082cb36bc2a89e55315cf1aa9c7b332028eee3cae9e879973fb7fb7f5ed297`（852 行）**。两条判据（改后哈希 + owner 双确认）均已满足 → **不再 `BLOCKED-on-prototype`，可进入 Step 1**。执行器开工前仍须 `shasum -a 256` 核对两份原型 == 上述确认稿哈希；若不等（原型此后又被改动）→ **STOP** 回报 advisor/owner，不得自行重画、不得忽略差异继续。

> **原型设计状态（2026-11-15 出稿，2026-11-16 按 owner 六轮反馈修订，owner 2026-11-16 已确认 → 闸门解除）**：两份原型均已按 Step 0a/0b 改完并自验，**owner 已对两份原型均确认 → 闸门解除，计划脱离 `BLOCKED-on-prototype`、可进入 Step 1**（确认记录与哈希见 Step 0c）。本轮已并入 owner 六条反馈：**(A)** 模型设置标记改齿轮图标（裁决 7）；**(B)** 第一屏镜像现有源码（单问句 + ≥8 行 filter）；**(C)** 齿轮紧贴档位名（「High ⚙」，不甩到第二列）；**(D)** 第二屏问句点名具体模型（TUI 本就是 `Which reasoning effort should <provider/model> use? (Esc to go back)`；Web 标题由 generic「这个模型 / this model」改为点名 `{model}`，zh/en 同步）；**(E)** 第一屏问句与 filter 输入框之间空一行（TUI 审批选择器补 `blank()`；`/model` layout 本就有；源码遗漏见 Step 14a）；**(F)** 第二屏问句与思考强度列表之间空一行（评审 / 交接 / 无默认档三场景共用分支，一处 `blank()` 三处生效；源码见 Step 14）。
> - **TUI** `tui-prototype.html`：1545 → **1784** 行；改后 SHA-256 `8f169c3e9a90650d43fc3b04f0489ee5fa33a137b79fce148e924703518ec197`（原始基线 `519e7228…`）。新增 `GATE_MODELS` 的 `efforts`/`defaultEffort`（三种行齐全，并扩到 9 个模型以触发 ≥8 filter）、`EFFORT_LEVELS`/`EFFORT_EN`/`effortEn`/`effortOptions`（与 `EffortChip.optionsList` 同构，`xhigh→xHigh`，**不复用** `/connect` 的 `LEVELS`）、`gateState` 的 `effortPicking/effortCursor/effortModel/effortOptions` 与两屏 `key(k)`（Enter 进第二屏、第二屏 Enter 才决策、Esc 逐层返回）、`gateContent` 单列第二屏渲染（问句与档位列表间空一行，反馈 F）。**(B)** 第一屏 `gateContent`（`:1356-1371`）单问句（`Which model should review/carry out this plan? (Esc to go back)`，替换 proceed 行）+ **空一行（反馈 E）** + ≥8 行三行 filter 条 + `gateState` 的 `filter/fc/inFilter/filtered` 与 `rebuildFilter()`、`key(k)` 的 filter 键路由（Esc/Enter/Up/Down/ printable/Backspace/Left/Right/Home/End，`❯` 与 caret 互斥）。**(C)+(A)** `modelEffortScene`（`:880-881`）`label:o.label+(o.value?'  '+o.value:'')`、`mark:o.fromSetting?{glyph:'⚙',style:ACC}:null`、`desc:''` —— 齿轮强调色紧贴名字、值名双写也在名字列；`columnRows`/`PB.columns`/`pickPanel` 扩 `mark` 参数渲染紧贴标记。新场景 6 条：`exit-plan-effort`、`exit-plan-effort-handoff`、`exit-plan-effort-default`、`exit-plan-filter`、`model-effort`、`model-effort-default`。
> - **Web** `plan-handoff-preview.html`：721 → **852** 行；改后 SHA-256 `5e082cb36bc2a89e55315cf1aa9c7b332028eee3cae9e879973fb7fb7f5ed297`（原始基线 `ca478be7…`）。`MODELS` 补三种行；新增 `LANG`/`I18N`（zh/en，逐字对齐 `chat.effort*`）/`effortLabel`/`effortOptions`；`chooseModel`（无档位跳过）+ `effortScreen`（标题**点名模型**「{model} 用哪档思考强度？」/「Which reasoning effort should {model} use?」+「返回选模型」+ 勾选 + 双写值 + **齿轮图标**标记）；`handOff`/`reviewing` 带选中档位；Esc 分层；rail 加「挑思考强度」步；frame 级「中 / EN」切换按钮（应用内无执行者说明）；静态终端区补第二屏对照块。**(C)+(A)** `effortScreen`（`:680`）把 `.etag` 齿轮（`<use href="#i-gear"/>`，`title`/`aria-label`=`chat.effortModelSetting`）移进 `.eleft`（`:300` `gap:7px`）紧贴档位名，不再被 `.xp-model` 的 `space-between` 甩到远端。第一屏（picker `:642-648`）本就单 `.cap` 问句、无 filter，无需 (B) 改动。
> - **自验证据**：两份 `node --check` 均 exit 0；TUI 24 条 node 断言 + 7 条浏览器按键断言全过、5 个新场景浏览器渲染正常；Web 25 条浏览器断言全过、zh/en 标记逐行核对；两份浏览器 console 均无 error/warn。**跳过判据已修正**：第二屏跳过看原始 `efforts.length===0`，不是 `effortOptions` 长度（后者在无默认档时会补 `Default`）。
> - **以原型为准的措辞修正**：Step 14 原「其余显示档位 id」与 §0.6 英文显示名裁决冲突，已改为「英文显示名 + 可选双写值名」，与原型一致。

#### Step 0d：执行 mandate —— 按确认稿 1:1 实现（owner 2026-11-16 确认后生效）

owner 原话：「两份原型已确认，必须将本期针对原型稿的修改写进计划，要求执行者按原型稿实现。」据此：

1. **两份确认稿是本期 UI 的唯一权威**（哈希见 Step 0c）。实施步的 prose 只解释「怎么改源码」；**视觉 / 文案 / 空行 spacing 一律以确认稿为准**；prose 与原型冲突时以原型为准并回报，不得自行折中。
2. **本期六项原型改动（A–F）已全部映射到实施步**，执行器须逐条还原，不得漏项、不得「顺手优化」偏离原型：

   | 反馈 | 原型改动（确认稿可见） | 实施步 |
   |---|---|---|
   | A | 模型设置标记 = 齿轮**图标**（TUI `⚙` 强调色；Web lucide `Settings` + tooltip/aria `chat.effortModelSetting`），**不是文字** | Step 14（TUI 第二屏内联）、Step 15c（`/model` mark 渲染扩展）、Step 18（Web） |
   | B | 第一屏**单问句** + ≥8 行 filter（镜像现有源码，非新增） | Step 0a#8；既有行为，不得改回双问句 / 无 filter |
   | C | 齿轮**紧贴**档位名（「High ⚙」），不甩到第二列 / 远端描述列 | Step 15c（TUI）、Step 18（Web `.eleft` `gap:7px`） |
   | D | 第二屏问句**点名具体模型**（TUI `Which reasoning effort should <provider/model> use? (Esc to go back)`；Web `{model}` 插值，zh/en） | Step 14（TUI）、Step 18 + i18n `chat.exitPlanEffortChoose`（Web） |
   | E | 第一屏**问句与 filter 输入框之间空一行** | **Step 14a**（源码过滤条块首补 `writeLine("")`） |
   | F | 第二屏**问句与思考强度列表之间空一行**（评审 / 交接 / 无默认档三场景） | **Step 14**（新分支首行 `writeLine("")`） |

3. **验收加逐帧比对**：除 §8 机器判据外，执行报告须附实现后 TUI 截屏（第一屏含 filter、第二屏三场景、`/model` 第二步）与 Web 截屏（zh/en 第二屏），与确认稿逐帧比对空行、齿轮位置、问句文案、双写值、`Default` 首项；任一差异即 FAIL 并修正后重比。
4. **原型脚手架不进实现**：rail 步骤条、「中 / EN」frame 级切换按钮、静态终端对照区、`SCENES`/`go()` 驱动器等只是原型演示设施，**不要**搬进 `PlanApprovalDock.vue` / TUI 源码。

---

### Phase A — 候选列表下发档位（数据层，零新查询）

#### Step 1：`turn.PlanReviewModelOption` 加两个字段

`pkg/turn/model.go:629-645` 增：

```go
// Efforts is the ordered list of reasoning levels this row actually offers,
// already trimmed to what its protocol can send. Empty = nothing to ask.
Efforts []string
// DefaultEffort is the level this row is configured with ("" when it
// configures none) — the effort the approval picker opens on.
DefaultEffort string
```

`pkg/turn/approval.go:1324-1332` 的 `out = append(...)` 补两行：`Efforts: orderedEffortLevels(entry.ReasoningEfforts)`、`DefaultEffort: strings.TrimSpace(entry.ConfiguredEffort)`。

`orderedEffortLevels` 新建在 **`pkg/turn/models.go`**（与 `ModelReasoningEfforts` / `ModelReasoningEffortsRow` 同处；`pkg/turn` 包内 `approval.go` 与 `executor.go` 都能直接调用，**不要新建生产文件**）：按固定顺序 `[]string{"none","minimal","low","medium","high","xhigh","max"}` 排已知档位，未知档位按原相对顺序追加，去重；与 `frontend/src/lib/effortLabel.ts:34` / `EffortChip.vue:75-76` 同构。**理由**：所有选择器必须同序，Go 侧 `ModelChoice.ReasoningEfforts` 的顺序来自 catalog/override，不保证是这个顺序。**这个 helper 在 Phase E2 的 `/model` 第二步也要复用**，所以本步就要落在两处都能调到的文件里。

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/turn -run '^TestPlanReviewModelOptions' -count=1 -v` → 既有 3 例（`approval_test.go:780/818/839`）全 PASS；新写 `TestPlanReviewModelOptionsCarryTheRowsEfforts`（见 §7）先红后绿。

#### Step 2：网关 wire 下发

`pkg/gateway/api_extra.go:4624-4634` `planReviewModelWire` 加 `Efforts []string \`json:"efforts,omitempty"\``、`DefaultEffort string \`json:"default_effort,omitempty"\``；`:4560-4567` 的逐字段拷贝补两项；`:4579-4587` 的 `PreviousPlanReview.Model → planReviewModelWire` 同样补两项（保持「再审」回显形状一致）。

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run '^TestSessionApprovalRequestCarriesPreviousReviewAndHandOff$' -count=1 -v` PASS；新断言 `efforts` / `default_effort` 出现在 JSON。

#### Step 3：前端类型

`frontend/src/lib/api.ts:1659-1672` `PlanReviewModelOption` 加 `efforts?: string[]`、`defaultEffort?: string`（可选，兼容旧服务端）。

**Verify**：`cd frontend && pnpm typecheck` → exit 0。

---

### Phase B — 协议与落盘（逐字段点按 §2.3 清单逐个改）

#### Step 4：决策载体加 `Effort`

- `pkg/turn/model.go:679-704` `ToolApprovalDecision` 加 `Effort string`（注释写明「the reasoning effort picked after a model; empty when the model's own configuration decides」）。
- `pkg/turn/approval.go:1008-1014` `Model` 加 `Effort string`。
- `pkg/tui/chat_surface.go:1130-1139` `handOffModelOf` 增加参数或在调用点传 effort —— **推荐**：把签名改成 `handOffModelOf(option *turn.PlanReviewModelOption, effort string) *turn.Model`，`:727` 调用改成 `handOffModelOf(decision.HandOffModel, decision.Effort)`；全仓仅此一个调用点，改前用 `grep -rn 'handOffModelOf' pkg/` 确认。
- `pkg/tui/chat_surface.go:1113-1125` `completeSurfacePlanReviewRequest` 增加 effort 参数，`s.runPlanReview(ctx, actionID, selection, nil, effort)`；`runPlanReview`（`:1178-1232`）与 `turn.RunPlanReview` 的 `PlanReviewRun`（`approval.go:1575-1598`）需要在 `Model` 里携带 effort（下一步）。
  ⚠️ **`runPlanReview` 有第二个调用点**：`completeSurfacePlanReviewAgain` `:1146-1164` 在 `:1162` 也调它。改签名后**必须**同步把该处改成传 `""`（「再审」沿用记录、不打开选择器，effort 恒空，§0.1）。漏改这一处会编译失败。改前 `grep -n 'runPlanReview' pkg/tui/chat_surface.go` 确认全部调用点。

**Verify**：`CGO_ENABLED=1 go build -tags fts5 ./pkg/tui ./pkg/turn` → exit 0。

#### Step 5：`answer_json` 与回放链加 effort

- `pkg/turn/approval.go:81-85` `handOffModelJSON` 加 `Effort string \`json:"effort,omitempty"\``；`:89-98` `handOffModelJSONOf` 补 `Effort: strings.TrimSpace(m.Effort)`。
- `:255-262` `ApplyResolvedEffect` 构造 `Model` 补 Effort；`:741-757` `ActionHandOffModel` 构造 `Model` 补 Effort。
- `:433-440` `sameHandOff` 的比较补 `strings.TrimSpace(a.Effort) == strings.TrimSpace(b.Effort)` —— **必须补**，否则「同模型不同强度」的重复决策会被误判为幂等。
- `:1625-1630` `RunPlanReview` 的 `model := Model{...}` 补 `Effort: strings.TrimSpace(in.Model.Effort)`（**这是最易漏的一处，它会静默吞掉 effort**）。
- `pkg/tool/session_tools.go:483-502` 的 twin 解码结构体加 `Effort`，`:494-498` 构造 `state.PlanHandOff` 补 `Effort`。
- `pkg/state/mode.go:45-53` `PlanHandOff` 加 `Effort string \`json:"effort,omitempty"\``。
- `pkg/tool/format.go:2082` 的审批历史解码结构：**先 grep 确认**它是否用 `json.Unmarshal` 到匿名 struct；若是，`omitempty` 新字段天然兼容，不改（在报告里写明已确认）。

**兼容性硬要求**：不带 `effort` 的旧 `answer_json` / 旧 `modes/*.json` 必须仍能解码（Go 零值 `""`），且行为与今天一致。

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/turn ./pkg/tool ./pkg/state -run 'HandOff|PlanReview|Decide' -count=1 -v` → PASS；新写 `TestHandOffAnswerWithoutEffortStillReplays`（§7）PASS。

#### Step 6：两侧 surface 把 effort 带进 `turn.Model`

- TUI：`pkg/tui/chat_surface.go:663-664` 把 `decision.Effort` 传给 `completeSurfacePlanReviewRequest`；`:1124` 的 `runPlanReview` 构造 `PlanReviewRun.Model` 时逐字段拷贝补 Effort（改前 grep `turn.Model{` 在 `chat_surface.go` 的所有出现）。
- Web：`pkg/gateway/api_extra.go:4732` `model := turn.Model{...}` 补 Effort；`:4733-4739` 的 `again` 分支**不覆盖** Effort（保持「沿用记录」）；`:4761-4769` 的 `PlanReviewRun{Model: model}` / `process.PlanReviewer{Model: model}` 自动带上。

**Verify**：`CGO_ENABLED=1 go build -tags fts5 ./pkg/tui ./pkg/gateway ./pkg/turn ./pkg/process` → exit 0；`grep -n 'turn.Model{' pkg/tui/chat_surface.go pkg/gateway/api_extra.go pkg/turn/approval.go` 人工核对每处都有 Effort 或明确不需要。

---

### Phase C — 网关入站（白名单 + 越界拒绝）

#### Step 7：`handleActionsApprove`

`pkg/gateway/api_extra.go:4432-4436` 匿名结构加 `Effort string \`json:"effort"\``。在 `:4477-4487` 的 `for _, option := range req.PlanReviewModels { if option.MatchesSelection(...) }` 循环里，命中后额外校验：

```go
if effort := strings.ToLower(strings.TrimSpace(body.HandOffModel.Effort)); effort != "" &&
    !effortOffered(option.Efforts, effort) {
    http.Error(w, "reasoning effort is not offered for this model", http.StatusBadRequest)
    return
}
```
（把命中项存下来，不要在循环里提前 `break` 掉校验。）`:4488-4492` 构造 `turn.Model` 补 `Effort`。`slices` 已在 `api_extra.go` 使用（`:2096`），不要新增 import。

**`effortOffered` 是本步新建的小 helper**（放 `api_extra.go`，Step 8 复用）：catalog 档位 id 本就是小写，归一化只需对两侧 `ToLower`+`TrimSpace`：
```go
func effortOffered(efforts []string, want string) bool {
    for _, e := range efforts {
        if strings.ToLower(strings.TrimSpace(e)) == want { return true }
    }
    return false
}
```
与前端 `effortLevelLabel` / `appcfg.WithReasoningEffort`（`agent_llm.go:158` 会 `ToLower`）的大小写口径对齐。**不要**引用未定义的 `normalizedEfforts`。

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run '^TestGatewayApproveHandOffModel$' -count=1 -v`。⚠️ 既有精确断言（`api_extra_test.go:10487` 的 `require.Contains(... '"hand_off_model":{"id":"row-2","provider":"openai","model":"gpt-5.1"}')`）在该用例**不发送 effort** 时因 `omitempty` 仍**保持绿**——它不会自动变红。要钉住新形状，需**新增一条发送 effort 的用例**断言 `"hand_off_model":{...,"effort":"high"}`，并**保留 :10487 这条不带 effort 的断言**（旧客户端兼容）。另写 2 条新用例（合法档位 / 越界 400）。

#### Step 8：`handleActionPlanReview`

`pkg/gateway/api_extra.go:4670-4680` body 加 `Effort *string \`json:"effort"\``（**指针**，区分「没传」与「传空串」）。在 `:4740-4750` 的 `ReviewModels` 白名单循环里做与 Step 7 同样的档位校验。`Effort == nil` 时不写字段（等价 `""`）；非 nil 时 `strings.TrimSpace(*body.Effort)`。`again` 分支（`:4716-4728`）**忽略 body 的 effort**，与今天忽略 model 字段的口径一致。

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run '^Test(PlanReviewAcceptsTheRowTheBodyNames|ActionPlanReviewValidatesBeforeStarting|ActionPlanReviewAgainContinuesThePreviousReview)$' -count=1 -v` → PASS；新用例见 §7。

#### Step 9：前端请求体

- `frontend/src/lib/api.ts:1655` `handOffModel?: { id; provider; model }` → 加 `effort?: string`；`:3730` 的展开自动带上（`toSnakeCase` 会把 `effort` 保持为 `effort`）。
- `:3765` `actionPlanReview` body 类型加 `effort?: string`。
- `frontend/src/composables/usePendingActions.ts:437`（`requestReview`）、`:448-452`（`handOffTo`）两处补 effort；`:461`（`reviewAgain`）**不动**。
- `frontend/src/lib/api.test.ts` 加一条 wire 断言（见 §7）。

**Verify**：`cd frontend && pnpm typecheck && pnpm exec vitest run src/lib/api.test.ts` → 全绿。

---

### Phase D — 引擎生效（两个派发入口）

#### Step 10：review 派发

`pkg/process/run_executor.go:438`：

```go
override := run.SubagentModelOverride{
    ModelID: modelID, Provider: r.Model.Provider, Model: r.Model.Model,
    Effort: strings.TrimSpace(r.Model.Effort),
}
```
`:472` `planReviewModelRow` 的 `ConfiguredModelClientWithEffort(..., "")` 改为传 `r.Model.Effort`（让行解析用实际 effort；**真正的合法性校验在 Step 8 的网关层**，这里不要重复造规则）。`planReviewModelRow` 签名加 effort 参数或直接从 `model.Effort` 取（它已收 `model turn.Model`）—— **推荐后者，零签名改动**。

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/process ./pkg/run -run 'PlanReview|SubagentModelOverride' -count=1 -v` → PASS；新用例 `TestPlanReviewOverrideCarriesThePickedEffort`（§7）先红后绿。

#### Step 11：hand-off 派发

新增 `handOffPlanEffort(ctx context.Context, fac Factory) string` 于 `pkg/run/subagent.go`（**不要改 `handOffModelID` 的签名**，它有 4+ 调用点）：照 `handOffModelID` `:4559-4572` 的写法读 `state.Get(...)`，返回 `strings.TrimSpace(st.PlanHandOff.Effort)`，任何错误/空返回 `""`。

`subagentModelOverrideForRef` `:4590-4598` 构造 pin 时，把 `Effort` 改为：

```go
effort := appcfg.ReasoningEffort(entry.Params)      // 今天的行为
if usable.HandedOff {
    if picked := handOffPlanEffort(ctx, fac); picked != "" { effort = picked }
}
```
即：**只有交接行、且审批指定了具体档位时才覆盖**；空值保持今天的行默认（兼容旧记录、兼容用户没改第二步）。

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run '^(TestHandOff|TestUsableModels|TestSubagentModelOverride)' -count=1 -v` → PASS；新用例 `TestHandOffPinnedEffortReachesTheOverride`（§7）先红后绿。`TestUsableModelsLegacyHandOffNeedsCredentials`（`subagent_test.go:9094`）必须仍 PASS 且断言不放宽。

> 性能约束：每次派发至多一次额外 `state.Get`（小 JSON 文件）；不得在 `usableModelByID` 的循环里读。

---

### Phase E — TUI 第二屏（overlay 内，不动 slash picker）

> **以 Step 0a 定稿的 `tui-prototype.html` `E · 计划审批` 场景组为逐行对照基准**（含新加的 `exit-plan-effort` 场景）。下面的问句、档位行、默认项标记、Esc 分层、单列布局均须与原型一致；原型若与本文措辞有出入，**以原型为准**并在报告里写明。

#### Step 12：状态与进入/退出

`pkg/tui/overlays.go:38-103` 的 `approvalOverlay` 加：

```go
planEffortPicking  bool
planEffortModel    turn.PlanReviewModelOption  // 第一步选中的行（含 Efforts/DefaultEffort）
planEffortPurpose  planPickerPurpose
planEffortOptions  []string                    // 已排序的档位 id；无档位时为空
planEffortCursor   int
```
新增 `enterPlanEffortMode(selected turn.PlanReviewModelOption, purpose planPickerPurpose)`：
- `len(selected.Efforts) == 0` → **不进入**，由调用方直接提交（见 Step 13）；
- `planEffortOptions` = `selected.Efforts`；若 `selected.DefaultEffort == ""` 则在**首位**插 `""`（「Provider default」/ 「默认」项，对齐 `EffortChip.vue:80`）；
- `planEffortCursor` 落在 `selected.DefaultEffort` 那一项（找不到则 0）；
- 保留第一步的模型列表状态（`planReviewFiltered`、`planReviewCursor`、`planReviewFilter` **不清**），以便 Esc 原样返回。

新增 `exitPlanEffortMode()`：只清第二屏 5 个字段；`exitPlanReviewMode()` `:1417-1425` 增加清第二屏字段（彻底退出时全清）。

#### Step 13：键盘

`handlePlanReviewKey` `:1504-1519` 的 `rawKeyEnter` 改为：

```go
selected := models[o.planReviewCursor]
purpose := o.planPickerPurpose
if len(selected.Efforts) != 0 {
    o.enterPlanEffortMode(selected, purpose)
    return turn.ToolApprovalDecision{}, false
}
o.exitPlanReviewMode()
return o.planPickerDecision(selected, purpose, ""), true
```
抽出 `planPickerDecision(selected, purpose, effort) (turn.ToolApprovalDecision, bool)`（放 `overlays.go`），内部即今天的 `:1516-1519` 两条返回，并把 `Effort: effort` 填进 `ToolApprovalDecision`。

新增 `handlePlanEffortKey(key parsedKey)`。分派点在 **`handleKey` `:985-991`**（`Run()` `:476` 只是 `decision, done := o.handleKey(key)` 的外层调用，真正的分派在 `handleKey` 里）：在现有 `if o.planReviewPicking { return o.handlePlanReviewKey(key) }` **之前**加 `if o.planEffortPicking { return o.handlePlanEffortKey(key) }`（第二屏优先于第一屏）。
- `rawKeyUp/Down` → `noFilter := false; navigatePicker(&noFilter, &o.planEffortCursor, len(o.planEffortOptions), ±1, false)`（`navigatePicker` `:4642-4653` 的签名是 `(inFilter *bool, cursor *int, count, delta int, hasFilter bool)`；`hasFilter=false` 时它只做边界 clamp、不碰过滤态，传一个局部 `false` 即可，档位 ≤8 项无过滤框）；
- `rawKeyEnter` → **先捕获再清状态**（`exitPlanEffortMode` 会清掉 `planEffortModel`/`planEffortPurpose`，清完再读就拿到零值）：
  ```go
  effort := o.planEffortOptions[o.planEffortCursor]
  model, purpose := o.planEffortModel, o.planEffortPurpose
  o.exitPlanEffortMode()
  o.exitPlanReviewMode()
  return o.planPickerDecision(model, purpose, effort), true
  ```
- `rawKeyEscape` → `o.exitPlanEffortMode()`（回第一屏，**不**产生决策）；
- `rawKeyCtrlC` → `Cancelled: true`（与 `:1438-1439` 一致）；
- 数字键 / `j`/`k`：**第二步不响应**（对齐计划 282 的 `TestExitPlanReviewPickerIgnoresDigitKeys` 口径），避免误触。

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run '^Test(ExitPlanOverlayPicksHandOffModel|ExitPlanOverlayPicksReviewerWithoutDecidingApproval|ExitPlanOverlayEscapeLeavesPickerWithApprovalIntact)$' -count=1 -v` → 三条既有用例必须**仍 PASS**（它们选的是无 effort 字段的 fixture，走 Step 13 的「无档位直达」分支，因此行为不变）。这是本计划最重要的回归闸门。

#### Step 14：渲染

`pkg/tui/overlays.go:612-621` 的问题行 `switch` **在最前面**加第二屏分支：

```go
case o.planEffortPicking:
    o.planReviewTopRow = lineCount
    writeLine("Which reasoning effort should %s use? (Esc to go back)",
              planReviewModelDisplay(o.planEffortModel.Provider, o.planEffortModel.Model))
```
`:755-802` 的 `case o.planReviewPicking:` 之前加 `case o.planEffortPicking:` 分支：**分支首行先 `writeLine("")`**（问句与档位列表之间空一行，owner 反馈 F，对齐原型 `gateContent` 的 `blank()`），再逐档 `writeLine`，行首 `panelCursor` / 空格对齐（复用 `:788` 的 `lead` 模式）。每行 = 英文显示名（`effortEnglishLabel`：`""` 项显示 `"Default"`，其余如 `High`/`xHigh`，**不是**裸档位 id —— 与 Step 0a 原型一致）+ 可选双写值名（仅当显示名与值名忽略大小写后仍不同，对齐 `EffortChip.vue:88`；英文界面只有 `Off  none` 这一行双写，值名紧跟显示名）+ 等于 `DefaultEffort` 的项在显示名**紧右侧**渲染强调色齿轮 `⚙`（`panelAccentStyle`，裁决 7；**不是**文字 `(model setting)`、**不**进描述列）。审批第二屏走 `writeLine` 单列直渲，齿轮内联即可，无需 `SelectItem` mark 扩展。`:920-933` footer 第二屏显示 `"Esc to go back · Enter to confirm"`。

**TUI 文案一律英文**（§2.6）。样式复用 `panelAccentStyle` / `selectorCheckedGlyph` / `panelCursor`（`render.go:7537/7552`、`overlays.go:4412`）；**不要新建样式常量**。

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run '^TestExitPlanReviewPicker(RendersTwoColumns|MarksCurrentWithGlyph|HasNoRowNumbers|IgnoresDigitKeys)$' -count=1 -v` → 全 PASS（第一屏不受影响）；新增渲染用例见 §7。

#### Step 14a：第一屏问句与 filter 条之间补空行（owner 反馈 E）

`pkg/tui/overlays.go:755-777` 的 `case o.planReviewPicking:` 中，`:757-759` 注释承诺过滤条「**air above it**, a padded row, the text's row, a padded row, then air before the list」，但代码只在过滤条**后** `:776` `writeLine("")`，**漏了过滤条前的空行**：问句（`:615`/`:618`）紧贴 `field.padRow(width)`（`:762`）。在 `if o.planReviewShowsFilter() {`（`:760`）块首、`field := …` 与 `lines = append(lines, field.padRow(width))` 之前补一行 `writeLine("")`，使「问句 → 空行 → 三行 filter 条 → 空行 → 列表」，与 `/model` 的 `slashPanel.layout`（head→blank→field→blank→body）及原型 `gateContent` 一致。**只补这一行**：不动过滤条三行结构、不动 `:776` 的后空行、不动问句 switch、不动 `pickerFilterMinRows`。这是修正源码与自身注释不符的既有遗漏，不是新设计。

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run '^TestExitPlanReviewPicker' -count=1 -v` → 既有全 PASS；新增一条「问句行与 filter 首 pad 行之间恰有一空行」的渲染断言（见 §7），先红后绿。

#### Step 15：确认行

`pkg/tui/channels.go:298-344` 的确认行今天打印所选模型；若需要展示强度，在 `:301-304`（评审）/`:334-337`（交接）两处补 ` (effort: <level>)`，**仅当非空**。先读这两段再决定；若改动会破坏既有 `channels_test.go` 断言，改为不改并在报告里说明（确认行不是功能必需）。

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` → FAIL 名单与干净基线一致。

---

### Phase E2 — TUI `/model` 第二步对齐 Web `EffortChip`（§0.4 / §2.7）

> **以 Step 0a 定稿的 `A · /model 选择器` 新场景为逐行对照基准**。本 Phase 只改 `model-effort` 的 items（Step 15a–15b）与 TUI 显示映射（Step 15c）；`/model` 第一步模型列表、onboard `/connect` 向导、Web `SlashPickerCard.vue` / `useChatStream.ts` 都不动（§4 Out of scope）。**`/model` 第二步是 TUI 专属面板，输出全英文**；Web 的语言规则在 Step 18 落地，不在本 Phase。

#### Step 15a：`PickerItem` 加两个可选字段、Go 侧建英文标签表

`pkg/turn/model.go:392-402` 的 `PickerItem` 加两个 `omitempty` 字段（**不改 `Label` 的语义**，它是协议值，Web 的 `SlashPickerCard.vue:17` 直接渲染它）：

```go
// ValueLabel is the level's English display label, for the terminal picker
// only (TUI is English-only per owner 2026-11-15). The web ignores it and
// localizes from Label via effortLevelLabel(); Label stays the raw level id.
ValueLabel string `json:"value_label,omitempty"`
// FromSetting marks the level the model row itself is configured with.
FromSetting bool `json:"from_setting,omitempty"`
```

`pkg/turn/models.go` 新增 `effortEnglishLabel(id string) string`：`""→"Default"`、`none→"Off"`、`minimal→"Minimal"`、`low→"Low"`、`medium→"Medium"`、`high→"High"`、`xhigh→"xHigh"`、`max→"Max"`，未知档位原样返回。**逐字对齐** `frontend/src/locales/index.ts:4630-4639`（en 表）与 `frontend/src/lib/effortLabel.ts:16-30`。排序复用 Step 1 落在同文件的 `orderedEffortLevels`。

**Verify**：`CGO_ENABLED=1 go build -tags fts5 ./pkg/turn` exit 0；新用例 `TestEffortEnglishLabelMatchesTheWebEnTable`（把 8 个 id 的预期英文写死断言）PASS。

#### Step 15b：`chooseModel` 生成与 `EffortChip.optionsList` 同构的列表

`pkg/turn/executor.go:925-933` 重写 items 生成（保持 `current` 的取法 `:925-928`、`Title` / `Hint` / `Command` 不变）：

```go
configured := strings.TrimSpace(chosen.ConfiguredEffort)
ids := orderedEffortLevels(chosen.ReasoningEfforts)
if current == "" || configured == "" {          // EffortChip.vue:80 showDefault
    ids = append([]string{""}, ids...)           // 首项「默认」，不是末项
}
items := make([]PickerItem, len(ids))
for i, effort := range ids {
    items[i] = PickerItem{
        Value:       encodeEffortChoice(chosen.ID, chosen.Label, effort, changed),
        Label:       effort,                      // 裸 id，协议值不变
        ValueLabel:  effortEnglishLabel(effort),
        FromSetting: effort != "" && effort == configured,   // EffortChip.vue:89
        Current:     effort == current,          // 保留：光标落会话钉值
    }
}
```
`encodeEffortChoice` 的空值往返天然兼容（§2.7），但**必须补一条往返测试**钉住它。

`pkg/turn/executor.go:940-980` `chooseReasoningEffort` 同步两处：
- `:961` 校验改为 `if at < 0 || (effort != "" && !slices.Contains(choices[at].ReasoningEfforts, effort))` —— 放行「默认」项的 `""`（对齐 `api_extra.go:2096` 只校验非空档位的规则）；
- `:974` / `:979` 的回复文案用 `effortEnglishLabel(effort)` 显示，避免出现 `at  reasoning effort.` / `Reasoning effort set to .`。`:969-975` 的「同值不写」保持不变（对齐 `EffortChip.vue:98`）。

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/turn -run '^Test(ChooseModel|ChooseReasoningEffort|EffortChoice|EffortEnglishLabel)' -count=1 -v` → 全 PASS。既有四条（`model_test.go:564/587/701/772`）断言**只能按新语义扩展、不得放宽**；新用例见 §7，先红后绿。

#### Step 15c：TUI 显示映射（只作用于 `model-effort`）——齿轮紧贴档位名（裁决 7 / owner 反馈 C）

`pkg/tui/run.go:5356-5372` `runSlashPicker`，在 `:5361` 的 `items[i] = SelectItem{Label, Description}` 之后加一段**仅当 `picker.Command == "model-effort"`** 的映射。**值名双写与齿轮都进名字列（紧贴），描述列留空**——对齐原型 `modelEffortScene`（`tui-prototype.html:880-881`：`label:o.label+(o.value?'  '+o.value:'')`、`mark:{glyph:'⚙',style:ACC}`、`desc:''`）：

```go
if item.ValueLabel != "" {                          // 旧 fixture（commands_test.go:642）不带 ValueLabel：整段跳过，渲染与今天逐字一致
    label := item.ValueLabel                        // "High" / "Default"
    if item.Label != "" && !strings.EqualFold(item.Label, item.ValueLabel) {
        label += "  " + item.Label                  // 值名双写紧跟名字（EffortChip.vue:88，忽略大小写；英文仅 Off  none）
    }
    items[i].Label = label
    items[i].Description = ""                       // 单列：不再把值名/标记甩到远端第二列
    if item.FromSetting {
        items[i].Mark = "⚙"                         // 行默认档的齿轮，渲染层上强调色（见下）
    }
}
```

**齿轮要强调色且紧贴名字，需一处最小渲染扩展**（现状 `SelectItem`（`channels.go:605-615`）无 mark 字段、`SelectRich` 名字列 `panelLine.col`（`render.go:7564-7581`）单样式纯文本，`columnRows`（`panel_columns.go:109-134`）把 `Description` 渲染成远端第二列——直接 append 文字会丢掉强调色、放进 `Description` 会甩远，两者都违背原型）：

1. `pkg/tui/channels.go:605-615` `SelectItem` 加 `Mark string`（裸 glyph，`""`=无；**不引入 lipgloss import**，染色在渲染层做）。
2. `pkg/tui/render.go:7564` `panelLine` 加 `mark string`（**已染色**的 glyph 串）。
3. `pkg/tui/panel_columns.go:177` `selectableColumns` 签名加**变参** `mark ...string`（现有三个调用点 `tabbed_picker.go:282` / `overlays.go:5688` / `project_servers.go:132` 传 6 参仍编译，**前两个与第三个文件零改动**），取 `m := ""; if len(mark) > 0 { m = mark[0] }` 透传进 `panelLine{…, mark: m}`；`columnRows` 的空描述分支（`:110-112`）改为：`if l.mark != "" { return []string{l.lead + l.col + " " + l.mark} }`，否则维持今天的 `panelLine{lead: l.lead, text: l.col}.rows(width)`。
4. `pkg/tui/overlays.go:5688` `richPickerState.panel` 调用处补第 7 参：`mark := ""; if item.Mark != "" { mark = panelAccentStyle.Render(item.Mark) }`（`panelAccentStyle` = fg110，`render.go:7537`），`b.selectableColumns(…, item.ElideTail, mark)`。**只有这一处传 mark**。

- `item.Current` → `defaultIdx` 的 `:5368-5370` 逻辑**不改**（光标仍落在会话钉值）；`SelectRich` 的 `:5751-5759` 也不改。
- 其它 command 的渲染路径一个字不改（`mark` 恒 `""`，走原分支）；`picker.Command == "model"` 的 `ElideTail`（`:5362-5367`）保持。
- **不新建样式常量**（复用 `panelAccentStyle`）、不新建组件。

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run '^TestSlashPicker' -count=1 -v` → `commands_test.go:570/591/620/642` 四条全 PASS；新用例断言 `model-effort` 的 `SelectItem.Label == "High"`（双写行如 `"Off  none"`）、`FromSetting` 行 `Mark == "⚙"`、`Description == ""`，渲染输出含紧贴的 `⚙`（`High ⚙`），且非 `model-effort` 的 picker 渲染与今天逐字一致。

> **STOP / 降级闸门**：若上述渲染扩展牵动过多调用点或破坏既有两列断言（`TestSlashPicker*`），**停下报告**，不要自行把齿轮塞回 `Description` 远端列（那正是 owner 报障的形态）。可接受的降级只有「齿轮紧贴名字但用默认色（不上强调色）」——仍须紧贴、仍须报告偏差；**不得**改成远端列或文字 `(model setting)`。

> **本 Phase 只有 Step 15a–15c，没有 Web 侧步骤**：Web 输入框不提供 TUI 内置 slash 命令（§0.6），不存在 `/model` 第二步档位列表；Web 的语言规则由 Step 18 在审批面板第二屏落地（对话页 `EffortChip` 计划 300 已本地化）。

---

### Phase F — 原型闸门已上移（本阶段无实施步骤）

> **两端原型都已在 Phase 0 / Step 0a–0c 完成并确认**，本阶段只是执行序上的占位（原 Step 16 已上移为 Step 0b，**编号 16 留空不复用**），不要在这里再改一次原型、也不要重复提交确认。Phase E（TUI 审批第二屏）、**Phase E2（TUI `/model` 第二步）** 与 Phase G（Web 实施）都必须以 Step 0a / 0b 定稿的原型为**逐行对照基准**：问句、档位行样式、顺序、显示名与值名双写、默认项与齿轮图标标记（裁决 7，紧贴档位名）、Esc 分层、无档位跳过，全部按原型落地。源码与原型出现任何偏差，以原型为准（这是仓库的一贯约束，见 `plans/282...md:37`），并把偏差写进执行报告。

---

### Phase G — Web 实施（Step 0c 闸门解除后）

> **以 Step 0b 定稿的 `plan-handoff-preview.html` 为逐行对照基准**：第二屏标题、`.xp-model` 档位按钮、等于行默认项的齿轮图标（紧贴档位名，tooltip「模型设置」，裁决 7）、返回入口与 Esc 分层，全部按原型落地；偏差在报告中写明，不得自行设计。

#### Step 17：composable 加第二步状态与纯函数

`frontend/src/composables/usePendingActions.ts` 加（均按 `action.id` 键控，与 `:81-84` 同形）：

```ts
const modelPickStep = ref<Record<string, 'model' | 'effort'>>({})
const pickedModel   = ref<Record<string, PlanReviewModelOption | null>>({})
const pickedEffort  = ref<Record<string, string>>({})
```

新增**导出的纯函数** `planEffortOptions(model: PlanReviewModelOption): {id:string; fromSetting:boolean}[]`，逻辑**逐字对齐** `EffortChip.vue:72-92`（§2.5）：`known = EFFORT_LEVELS.filter(l => model.efforts?.includes(l))`、`extra` 追加未知档位、`configured = model.defaultEffort ?? ''`、`showDefault = configured === ''`（审批面板没有会话存档位，故只需这一条）、`ids = showDefault ? ['', ...known, ...extra] : [...known, ...extra]`、`fromSetting = id !== '' && id === configured`。

新增 `choosePlanModel(a, model)`：若 `planEffortOptions(model).length === 0` → 直接走今天的 `requestReview`/`handOffTo`（effort 省略）；否则暂存 `pickedModel[a.id] = model`、`pickedEffort[a.id] = model.defaultEffort ?? ''`、`modelPickStep[a.id] = 'effort'`，**不发请求**。
新增 `choosePlanEffort(a, effort)`：取暂存的 model + purpose，调 `requestReview(a, model, effort)` / `handOffTo(a, model, effort)`，随后清空三个暂存态。
`toggleModelPicker` `:416-420` 打开时把 `modelPickStep[a.id] = 'model'` 并清暂存；`closeModelPicker` `:425-427` 清暂存但**仍不改 purpose**（289 的修复不能破坏）。`requestReview` / `handOffTo` 的签名加 `effort?: string`，`:437` / `:448-452` 的请求体补字段。

**Verify**：`cd frontend && pnpm typecheck && pnpm exec vitest run src/components/chat/PlanApprovalDock.test.ts` → 全绿；新纯函数单测见 §7。

#### Step 18：组件渲染与 Esc 分层

`PlanApprovalDock.vue:52-72` 的 `v-else-if="picking"` 分支内按 `modelPickStep[action.id] === 'effort'` 分两屏：
- 第一屏：今天的模型按钮列表不变（`:58` 的 `@click="pickModel(model)"` 改成 `@click="choosePlanModel(action, model)"`，其余 `data-testid` 一个不动）；
- 第二屏：顶部标题 `.cap` 渲染 `t('chat.exitPlanEffortChoose', { model: modelLabel(pickedModel[action.id]) })`（**点名具体模型**，owner 反馈 D，见下方 i18n 键）；新 `data-testid="exit-plan-effort-option"`，`v-for="opt in planEffortOptions(pickedModel)"`，标签 `effortLevelLabel(opt.id)`，`opt.fromSetting` 时渲染 lucide `Settings` 齿轮图标（`PlanApprovalDock.vue:225` 的 `import { Eye, LoaderCircle, Map } from 'lucide-vue-next'` 补 `Settings`；`title` / `aria-label` = `t('chat.effortModelSetting')`，裁决 7），**紧贴档位名**（对齐原型 `.eleft` `gap:7px`，`plan-handoff-preview.html:300/680`）；点击 `choosePlanEffort(action, opt.id)`；上方一个「返回选模型」按钮 `data-testid="exit-plan-effort-back"`。

`onEsc()` `:365-371` 改为三层：
```ts
if (picking.value && modelPickStep[a.id] === 'effort') { backToModels(action); return }
if (picking.value) { closeModelPicker(action); return }
if (feedbackOpen.value) await refuseAndStop(action)
```
`backToModels` 只把 step 改回 `'model'` 并清 `pickedEffort`，**保留 purpose**（289 语义）。

i18n 新增 2 个键（zh `:7` 区块 + en `:2538` 区块**成对**，放在 `:2232-2243` 的 `chat.exitPlan*` 附近）：
- `chat.exitPlanEffortChoose` = `'{model} 用哪档思考强度？'` / `'Which reasoning effort should {model} use?'`（**点名具体模型**，owner 反馈 D，与 TUI 第二屏问句对齐）。`{model}` 走 vue-i18n 命名插值：渲染 `t('chat.exitPlanEffortChoose', { model: modelLabel(pickedModel[action.id]) })`，对照既有 `chat.exitPlanReviewRunning` 的 `{ model: activeReviewLabel(action) }`（`PlanApprovalDock.vue:40`）；`modelLabel` 即模型列表用的同一显示函数（`:67`/`:261`）。**不得**再写 generic「这个模型 / this model」。
- `chat.exitPlanEffortBack` = `'返回选模型'` / `'Back to models'`

其余文案全部复用 `chat.effortChipTitle` / `chat.effortModelSetting` / `chat.effortDefault` 与 `effortLevelLabel()`（`frontend/src/lib/effortLabel.ts:26-30`），**不新增逐档文案**。⚠️ **语言硬约束（§0.6）**：档位名、「默认」一律走 `effortLevelLabel()` + `t(...)`，齿轮图标的 tooltip / aria-label 走 `t('chat.effortModelSetting')`（裁决 7，图标本身无文字）；**中文界面出中文、英文界面出英文，不得在模板或 `planEffortOptions` 里硬编码 `"高"` / `"High"` / `"模型设置"` 等任一语言**；新增的两个 `chat.exitPlanEffort*` 键本身就是 zh/en 成对。Step 0b 的原型是中文界面稿，实现不得照抄原型里的中文字符串。

**Verify**：`cd frontend && pnpm typecheck && pnpm exec vitest run src/components/chat/PlanApprovalDock.test.ts` → 全 PASS；新增用例见 §7；`pnpm run build` exit 0。

#### Step 19：e2e

`frontend/e2e/exit-plan-approval.spec.ts`：扩 `:204`（评审）与 `:241`（交接）两条既有 spec，在点到模型行之后、断言请求之前插一步「选强度」，并断言 wire 带 `effort`（`:228` 的 `reviewRequest.postDataJSON()` 加 `effort: expect.any(String)`；`:271` 的 `body?.hand_off_model` 加 `effort`）。新增一条「第二屏 Esc 回模型列表、不发请求」的 spec。⚠️ 本 e2e 文件**没有**既有 Esc 用例（`:227` 是 `PlanApprovalDock.test.ts` 的行号，不是本 spec 的）；Esc 分层的断言写法对照 `frontend/src/components/chat/PlanApprovalDock.test.ts:227`（289 的「Esc 后无请求发出」用例），e2e 侧用 `page.keyboard.press('Escape')` + 断言无新请求发出。`office-plan-approval.spec.ts` 的 `:112-121` 隐藏断言保持不变。

**Verify**：`FOREBRAIN_E2E_SPEC="e2e/exit-plan-approval.spec.ts e2e/office-plan-approval.spec.ts" scripts/acceptance/web_e2e.sh` → 全 PASS。若环境缺少 `E2E_PROJECT_PARENT` 等变量导致无法启动，按 STOP 条件报告，不要放宽断言。

---

### Phase H — 收口

#### Step 20：全量回归

```sh
CGO_ENABLED=1 go build -tags fts5 ./...
go vet ./...
gofmt -l cmd pkg third_party plugins                 # 必须无输出
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/turn ./pkg/gateway ./pkg/tui ./pkg/run ./pkg/process ./pkg/state ./pkg/tool ./pkg/config ./pkg/architecture -count=1
CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1
scripts/package-graph.sh
# ⚠️ graph.json 已有其它批次/会话的工作区改动（见 §5 开工前快照），不能用 `git diff --exit-code` 对 HEAD 判空。
# 本计划不改包依赖图：比对「相对开工前」无新增 diff。
diff /tmp/graph-310-before.json pkg/architecture/testdata/graph.json   # 应为空（快照在 §5 开工前捕获）
cd frontend && pnpm typecheck && pnpm run build && pnpm exec vitest run src/components/chat/PlanApprovalDock.test.ts src/lib/api.test.ts
```
**预期**：全绿；若出现干净基线已有的 FAIL，逐条列出并保证「无新增」，不得写成通过（§3）。

#### Step 21：范围自检

```sh
git status --short > /tmp/status-310-after.txt
diff /tmp/status-310-before.txt /tmp/status-310-after.txt   # 新增/变化的行必须全是 §4 In-scope 文件
grep -rn 'effort' pkg/turn/approval.go pkg/turn/model.go pkg/gateway/api_extra.go | head -40
```
确认：相对 §5 的开工前快照，**新增或新改动的文件全部是 §4 In-scope**（gitignored 原型不进 git status）；无 schema/迁移、无 `EffortChip.vue`/`ChatView.vue`/`settingsSchema.ts`/`slash.go` 改动。开工前已存在的脏文件（309 遗留三项 + 并发的 `slash.go` 等，**非穷举**）原样保留，不得因它们判 FAIL，也不得顺手提交或还原。

---

## 7. Test plan

**Go（新增，全部落在既有 `*_test.go` 文件，不新建文件）**

| 文件 | 新用例 | 断言 |
|---|---|---|
| `pkg/turn/approval_test.go` | `TestPlanReviewModelOptionsCarryTheRowsEfforts` | 两行不同 `ReasoningEfforts`/`ConfiguredEffort` 的候选，`Efforts` 顺序为固定 7 档序、未知档位追加在后、`DefaultEffort` 逐字一致 |
| `pkg/turn/approval_test.go` | `TestHandOffAnswerCarriesThePickedEffort` | `Decide` 后 `answer_json` 的 `hand_off_model.effort` == 所选值；`ActionHandOffModel` 读回同值 |
| `pkg/turn/approval_test.go` | `TestHandOffAnswerWithoutEffortStillReplays` | 手工构造无 `effort` 的旧 `answer_json`，`ApplyResolvedEffect` / `sameApprovedReply` / `ActionHandOffModel` 三处行为与今天一致 |
| `pkg/turn/approval_test.go` | `TestSameHandOffTreatsDifferentEffortsAsDifferent` | 同模型不同 effort 的两条 reply 不被判幂等 |
| `pkg/turn/approval_test.go` | `TestRunPlanReviewKeepsTheModelsEffort` | `RunPlanReview` 的 `in.Model.Effort` 不被 `:1626-1630` 的逐字段构造吞掉（这是最易漏点） |
| `pkg/gateway/api_extra_test.go` | `TestGatewayApproveHandOffCarriesEffort` / `TestGatewayApproveRejectsAnEffortTheRowDoesNotOffer` | wire 往返 / 400 + 精确错误文案 |
| `pkg/gateway/api_extra_test.go` | `TestPlanReviewCarriesEffortAndRejectsAnUnknownLevel` / `TestPlanReviewAgainIgnoresTheBodysEffort` | 评审带 effort、`again:true` 时不采信 body 的 effort |
| `pkg/gateway/api_extra_test.go` | `TestSessionApprovalRequestCarriesRowEfforts` | `plan_review_models[].efforts` / `.default_effort` 出现在 JSON；`previous_plan_review.model` 同形 |
| `pkg/state/mode_test.go` | `TestHandOffPlanRecordsTheEffort` | `PlanHandOff.Effort` 落 JSON、重进 plan mode 时被清（`:127`） |
| `pkg/tool/session_tools_test.go` | `TestActionHandOffModelCarriesEffort` / `TestActionHandOffModelToleratesLegacyAnswer` | twin 解码新旧两形状 |
| `pkg/run/subagent_test.go` | `TestHandOffPinnedEffortReachesTheOverride` | `state.HandOffPlan` 带 effort → `subagentModelOverrideForRef` 的 `pin.Effort` == 该值；effort 为空时回落 `appcfg.ReasoningEffort(entry.Params)`；非交接行不受影响 |
| `pkg/process/run_executor_test.go`（若无同名生产文件则并入该包既有测试文件） | `TestPlanReviewOverrideCarriesThePickedEffort` | `PlanReviewer.Review` 派发的 `SubagentModelOverride.Effort` == `r.Model.Effort`；样板 `pkg/run/subagent_test.go:6987 TestCustomSubagentModelAndEffortReachTheEntry` |
| `pkg/tui/overlays_test.go` | `TestExitPlanEffortPickerOpensOnTheRowsDefault` / `TestExitPlanEffortPickerEscReturnsToTheModelList` / `TestExitPlanEffortPickerConfirmsWithThePickedLevel` | 夹具 `newApprovalOverlay` + `buildApprovalContent` + `paintedScreen`，样板 `chat_session_test.go:10839/10880/10917` |
| `pkg/tui/overlays_test.go` | `TestExitPlanEffortPickerLeavesAirBelowTheQuestion` | 第二屏问句行与首档位行之间**恰有一空行**（Step 14 / 反馈 F）；评审、交接、无默认档三个夹具各断言一次，先红后绿 |
| `pkg/tui/overlays_test.go` | `TestExitPlanPickerSkipsEffortStepWhenTheRowHasNoLevels` | 无档位行一步直达，**保证 283/289 既有行为不回退** |
| `pkg/tui/overlays_test.go` | `TestExitPlanPickerLeavesAirAboveTheFilterBar` | ≥8 行时断言「问句行」与「filter 首 pad 行」之间**恰有一空行**、filter 末 pad 与列表首行之间亦恰有一空行（Step 14a / 反馈 E）；夹具同 `TestExitPlanReviewPickerRendersTwoColumns`，先红后绿 |
| `pkg/tui/chat_session_test.go` | `TestExitPlanReviewPickerPassesTheEffortItChose` | 决策的 `Effort` 抵达 `completeSurfacePlanReviewRequest` / `handOffModelOf` |

**Phase E2（`/model` 第二步对齐）新增**：

| 文件 | 新用例 | 断言 |
|---|---|---|
| `pkg/turn/model_test.go` | `TestModelEffortPickerMatchesTheWebChipOrder` | items 顺序 = 固定 7 档过滤 + 未知档位追加，与 `EFFORT_LEVELS` 逐字一致；`orderedEffortLevels` 与审批候选共用同一结果 |
| `pkg/turn/model_test.go` | `TestModelEffortPickerOffersDefaultOnlyWhenTheTruthIsToShow` | 三段：行未配 `default_effort` → 首项 `""`；会话无钉值 → 首项 `""`；两者都有具体值 → **无**首项。对齐 `EffortChip.vue:80` |
| `pkg/turn/model_test.go` | `TestModelEffortPickerMarksFromSettingApartFromCurrent` | 会话档位被手动改成别的之后，`FromSetting` 仍只标 `ConfiguredEffort` 那一项、`Current` 仍标会话钉值 —— 两者不同，这是本次语义修正的核心 |
| `pkg/turn/model_test.go` | `TestEffortEnglishLabelMatchesTheWebEnTable` | 8 个 id 的英文标签逐字对齐 `frontend/src/locales/index.ts:4630-4639` |
| `pkg/turn/model_test.go` | `TestEffortChoiceRoundTripsTheEmptyDefault` | `encodeEffortChoice(id,label,"",false)` → `decodeEffortChoice` 得回 `effort == ""` 且 `modelChanged == false` |
| `pkg/turn/model_test.go` | `TestChooseReasoningEffortAcceptsTheExplicitDefault` | `""` 放行并写入会话行；越界的非空档位仍被拒；`Reply` 不出现 `at  reasoning effort.` |
| `pkg/tui/commands_test.go` | `TestSlashModelEffortPickerRendersTheWebLabels` | `model-effort` 的 `SelectItem.Label == "High"`（双写行如 `"Off  none"`）、等于行默认项 `Mark == "⚙"` 且 `Description == ""`、渲染输出含紧贴的 `High ⚙`、非 `model-effort` 的 picker 渲染与今天逐字一致 |

**既有断言（只能按新语义扩展、不得放宽）**：`pkg/turn/model_test.go:564/587/701/772`、`pkg/tui/commands_test.go:570/591/620/642`（注意 `:642` 的 `TestSlashPickerFollowsTheNextPicker` 断言 `Value: "b high"` 是简化 fixture，改后仍须 PASS）、`pkg/tui/setup_test.go:1569`（onboard 的 `{"minimal","low","medium","high", reasoningEffortUnset}` **一字不改**，作为 onboard 未被误改的回归证据）。

**前端 vitest**

- `frontend/src/components/chat/PlanApprovalDock.test.ts`：`'offers the picked row's effort levels before it submits'`、`'opens on the row's configured effort and marks it with the setting gear'`（断言齿轮 `Settings` 图标存在、`aria-label` == `t('chat.effortModelSetting')`）、`'offers 默认 for a row with no configured effort'`、`'skips the effort step for a row with no levels'`、`'Esc from the effort list returns to the models without a request'`、`'hands the plan over with the effort the user picked'`、`'asks for a review with the effort the user picked'`、`'does not send effort when the review is asked again'`。样板：`:199`、`:227`、`:291`。
- `frontend/src/lib/api.test.ts`：`'sends hand_off_model.effort and plan-review effort on the wire'`。
- 纯函数 `planEffortOptions` 的用例放 `PlanApprovalDock.test.ts` 内（或该 composable 已有的测试文件），逐条钉：7 档顺序、未知档位追加、有配置无「默认」项、无配置有「默认」项、`fromSetting` 只在等于配置值的项上为真。
- **语言两版（§0.6，Step 18）**：`PlanApprovalDock.test.ts` 补一条「切换 locale 后档位名随之变化」的用例 —— zh 下 `high→「高」`、`''→「默认」`、fromSetting 齿轮的 `aria-label`→「模型设置」，en 下 `high→High`、`''→Default`、齿轮 `aria-label`→`Model setting`，防止有人把中文原型稿的文案硬编码进模板。上面既有用例里的「默认」按 **zh locale**（测试默认）断言，不要把英文界面也断言成中文。**Web 没有 `/model` 的档位列表，`SlashPickerCard.test.ts` 不需要新建**（§0.6）。

**e2e**：`frontend/e2e/exit-plan-approval.spec.ts` 按 Step 19 扩 2 条 + 增 1 条。

**先红后绿要求**：Step 1、10、11、**15b** 的核心新用例必须提供「回退该步生产改动即红」的证据，写进执行报告（沿用 `plans/304` Step 4 的做法）。

---

## 8. Done criteria（全部机器可判）

- [ ] **原型闸门（Step 0，一票否决）**：`tui-prototype.html` 与 `plan-handoff-preview.html` 的改后 SHA-256 均与 §2.6 基线不同；**TUI 稿同时含审批第二屏与 `/model` 第二步两个新场景**；执行报告含「owner 对**两份**原型（含 `/model` 对齐项）均确认」的记录与四条交互路径的逐帧描述；且报告能证明确认发生在**任何源码文件被修改之前**（不得补票）
- [ ] `grep -n 'Efforts\|DefaultEffort' pkg/turn/model.go pkg/turn/approval.go` 有结果；`PlanReviewModelOptions` 的 out 字面量含这两个字段
- [ ] `curl` 等价的网关单测断言 `plan_review_models[].efforts` / `.default_effort` 在 JSON 中
- [ ] `grep -n 'Effort' pkg/turn/approval.go | wc -l` ≥ 10（§2.3 的 6/7/8/9/10/11 号点全部覆盖），且 `RunPlanReview` 的 `model := Model{...}` 含 Effort
- [ ] `answer_json` 新形状带 `hand_off_model.effort`；**不带 effort 的旧形状三条解码路径全 PASS**
- [ ] `sameHandOff` 比较含 Effort（同模型不同强度不判幂等）
- [ ] 网关两个 handler 对越界档位返回 400 `reasoning effort is not offered for this model`；空值/未传放行
- [ ] `pkg/process/run_executor.go` 的 override 含 `Effort`；`subagentModelOverrideForRef` 仅对 `usable.HandedOff` 且 effort 非空时覆盖
- [ ] TUI 审批：第二屏与 Step 0a 原型逐行一致；`grep -n 'planEffortPicking' pkg/tui/overlays.go` 有结果；`TestExitPlanOverlayPicksHandOffModel` 等 3 条 283/289 既有用例**断言未改**且 PASS；新第二屏用例全 PASS
- [ ] **TUI `/model` 第二步**：`grep -n 'ValueLabel\|FromSetting' pkg/turn/model.go pkg/turn/executor.go pkg/tui/run.go` 三处都有结果；`TestModelEffortPickerMatchesTheWebChipOrder` / `TestModelEffortPickerMarksFromSettingApartFromCurrent` / `TestChooseReasoningEffortAcceptsTheExplicitDefault` / `TestSlashModelEffortPickerRendersTheWebLabels` 全 PASS；`pkg/turn/model_test.go:564/587/701/772`、`pkg/tui/commands_test.go:570/591/620/642`、`pkg/tui/setup_test.go:1569` 断言未放宽且 PASS；`frontend/src/components/chat/SlashPickerCard.vue` 与 `useChatStream.ts` **零改动**
- [ ] Web：第二屏与 Step 0b 原型逐行一致；`PlanApprovalDock.test.ts` 全 PASS 且 283 的 `:199/:227/:291` 三条既有用例断言仅按新语义扩展、未放宽
- [ ] **语言硬约束（§0.6）**：TUI 两处档位列表全英文（渲染输出无中文）；Web **两处**（对话页 `EffortChip` 既有、审批第二屏新增）在 zh/en 两版下分别出中文/英文，由 `PlanApprovalDock.test.ts` 的 locale 切换用例钉住；`grep -rn '"高"\|"High"\|"模型设置"' frontend/src/components/chat/PlanApprovalDock.vue` **无硬编码命中**（模板里只出现 `effortLevelLabel` / `t('chat.effort*')`）
- [ ] `cd frontend && pnpm typecheck` exit 0（zh/en 新键成对）；`pnpm run build` exit 0
- [ ] e2e 两 spec 全 PASS，wire 断言含 `effort`
- [ ] `CGO_ENABLED=1 go build -tags fts5 ./...`、`go vet ./...`、`gofmt -l cmd pkg third_party plugins`（空）、`pkg/architecture` 守卫全绿
- [ ] 相对 §5 开工前 `git status` 快照，**新增/新改动文件全是 §4 In-scope**；开工前已存在的脏文件（309 遗留 + 并发 `slash.go` 等）原样未碰；**无任何 schema / 迁移 / `EffortChip.vue` / `ChatView.vue` / `settingsSchema.ts` / `slash.go` 改动**
- [ ] 全量回归 FAIL 名单与干净基线逐条一致、无新增（若不一致，逐条归因，不得写成通过）
- [ ] `plans/README.md` 表格行状态更新

---

## 9. STOP conditions（遇到即停，不要自行发挥）

- **Drift check 不通过**：§2 的任一摘录与活代码不符（尤其 `SubagentModelOverride.Effort` 不存在、`ModelChoice.ReasoningEfforts`/`ConfiguredEffort` 不存在）。
- **任一原型未出、或 owner 未对两份都确认（Step 0c 闸门未解除）**：**任何源码文件都不得修改**（`pkg/`、`frontend/src/`、测试文件），整份计划标 `BLOCKED-on-prototype`，本轮交付物只有两份原型 + 改后哈希 + 逐帧描述。**不得「先做后端、后补原型」**。
- **需要 DB 迁移才能落地**：本设计的 effort 只走 JSON（`answer_json` + `modes/<sid>.json`），若发现必须改表才能生效，**停下报告**，不要自行加 v37 迁移或三方言 SQL。
- **旧 `answer_json` / 旧 mode JSON 回放失败**，或 `TestGatewayIdempotentApproveStillCancelsAnInFlightPlanReview` 因 `sameHandOff` 补字段而变红且无法用兼容规则解释。
- **「再审」需要改 `ContinuePlanReviewSubagent` 才能保持一致**：不在范围，停下报告（§10 已留痕）。
- **283/289 的三条既有 TUI/Web 用例无法通过「无档位直达」分支保持原断言**：说明语义被破坏，停下报告，不得放宽断言。
- **`/model` 对齐看起来必须改 `PickerItem.Label` 的语义（裸 id → 显示名）**、改 `frontend/src/components/chat/SlashPickerCard.vue` / `useChatStream.ts`、或改 `pkg/tui/setup.go` 的 onboard 向导才能成立 —— 这些都在 §4 Out of scope，停下报告，不要自行扩大范围。
- **任何一步需要改 Out-of-scope 文件**（schema、`EffortChip.vue`、`ChatView.vue`、`settingsSchema.ts`、`/model` 第一步模型列表、`list_usable_models` schema 字节）。
- 同一步的验证命令连续两次失败且原因不明。
- `pnpm test -- <路径>` 或 Go 全量测试出现**基线之外**的新失败：先归因，不自行放宽。

---

## 10. Maintenance notes

- **语言规则是长期契约（owner 2026-11-15 第三条追加裁决，务必留痕）**：「**tui端是全英文，档位列表必须全英文。web端的档位列表在中文界面用中文，英文界面用英文**」。适用范围：TUI 的 `/model` 第二步与审批第二屏（Go 侧 `effortEnglishLabel` 提供英文）；Web 的**两处**档位列表 —— 对话页 `EffortChip`（计划 300 已落地）、审批面板第二屏（一律走 `effortLevelLabel()` + `chat.effort*` 键，禁止硬编码）。**模型名称**（`/model` 第一屏 `Label`、`provider/model`、`modelName`）不适用此规则，中文界面也保持英文。
- **Web 对话页不提供 TUI 内置 slash 命令（owner 同日裁决，别再误判）**：`/model` 等内置命令仅 TUI 端可用，Web 输入框的 slash 只用于**显式触发技能**；`SlashPickerCard.vue` / `useChatStream.ts` 因此不在本计划范围。曾把 `pkg/gateway/slash_handlers_test.go:222` 的**单元测试（直接调内部函数 `parseSlashCommandWithOptions`，`server.go:3033`）**误当成 Web 产品入口，据此加的 Step 15d 已整体撤销；`server.go:2152-2157` 的注释才是产品语义（slash 用于激活技能命令）。**残留待另立项**：`pkg/turn/slash.go:78` 的 `/model` 仍标 `AllowedSurfaces: []Surface{SurfaceWebChat, SurfaceTUI}`、`executor.go:774` 注释 "on every surface"，与产品语义不一致；本计划**不改**，将来收口需同时复核 `server.go:3033/3069-3075` 的执行面过滤与 `ListSlashCommands(SurfaceWebChat, …)` 的可见性。**评审时（2026-11-15）已观察到另一会话的未提交改动正在做这件事**：`pkg/turn/slash.go` 的 `commands` 表把 `/model` 等多条内置命令的 `AllowedSurfaces` 从 `{SurfaceWebChat, SurfaceTUI}` 收窄到 `{SurfaceTUI}`。因此执行本计划时：① **`slash.go` 仍是 Out-of-scope，一个字节都不改**（§4/§9）；② §2.7 关于「`slash.go:78` 仍标 WebChat」的描述是**基线 `1c824c33` 的committed 状态**，工作区可能已被并发会话改掉——执行前以 `git show 1c824c33:pkg/turn/slash.go` 与活工作区分别核对，不要因工作区已收窄就以为本计划的 drift check 失败，也不要顺手「补完」它；③ 该并发改动若先落地，本计划 §0.6「Web 无内置 slash」的产品语义反而更一致，但本计划不依赖它、也不验收它。
- **原型闸门比 283 时期更严格（owner 2026-11-15 追加裁决，务必留痕）**：计划 283 的惯例是「网页原型经 owner 确认后网页闸门解除，Part B 引擎/TUI/网关不受闸门限制」（`plans/README.md:150`）；本计划 owner 明确「tui端和web端都必须先出原型」，因此 **Step 0 的两份原型是全计划的前置闸门，确认前连后端源码也不得改**。以后若有人援引 283 的旧惯例想「先做后端」，以本条为准。两端原型都是 gitignored 的权威 UI 稿：TUI 看 `tui-prototype.html` 的 `E · 计划审批` 场景组，Web 看 `plan-handoff-preview.html` 的 `.xp-pick`；本次各自新增的第二屏场景就是后续改动的对照基准。
- **`/model` 第二步也纳入对齐（owner 2026-11-15 第二条追加裁决）**：原本本计划把 `pkg/turn/executor.go` 的 `/model` 两步 picker 列为「只作样板读、不改」；owner 追加「tui端slash命令/model面板里选择思考强度时的思考强度列表的语义和UI/UX也必须跟web端对话页的输入框里的思考强度选择对齐」后，它转入 In scope（§2.7 差异清单、Phase E2）。**TUI 原型因此要出两个新场景**（审批第二屏 + `/model` 第二步），Step 0c 的确认点也加了第 ⑥ 条。关键设计取舍留痕：`PickerItem.Label` 保持裸协议 id，新增 `ValueLabel` / `FromSetting` 两个 `omitempty` 字段专供 TUI 渲染层；这个隔离**不依赖**「Web 永远不会有 picker」的假设，所以即便 Web 侧没有该列表也保留。
- **「再审」的已知限制（明确延后，本计划不修）**：首次评审带 effort 后，`pkg/run/subagent.go:2029-2036` 会把 `pin.Effort` 写进子代理历史记录的 `ReasoningEffort`；`subagentRecordPin`（`:2469-2498`）在 `record.ModelLive && modelID != ""` 分支（`:2494`）用 `appcfg.ReasoningEffort(entry.Params)`（行默认）覆盖它。因此「请上次评审模型再审」那一轮**可能回退到模型行默认**，不保证沿用用户覆盖的强度。这是既有续跑语义（影响所有子代理续跑，不只 plan-reviewer），修它要动 `subagentRecordPin` 的公共路径，风险与范围都超出本计划 → **另立项**，并在 UI 上不暗示「再审沿用强度」。
- **前缀缓存**：本计划不改任何工具定义 / schema 描述 / 系统提示词字节；`list_usable_models` 与 `subagent_*` 的 schema 保持不动（历史裁决：schema 字节稳定是有意为之）。
- **空值语义分叉要长期留意**：会话模型链路的 `effort=""` 是 `WithReasoningEffort` 的「显式删键、让 provider 决定」（`pkg/config/agent_llm.go:151-152`），而子代理派发链路的 `Effort=""` 是「不覆盖、保留行默认」（`pkg/run/subagent.go:4744-4746`）。本计划走的是**后者**（提交具体值，只有「默认」项才传空）。将来若有人把聊天页的保存逻辑复用到审批面板，先读这两处注释。
- **档位顺序与英文标签都是跨端契约**：`orderedEffortLevels`（`pkg/turn/models.go`，被审批候选、`/model` 第二步、TUI 审批第二屏共用）与 `EFFORT_LEVELS`（`frontend/src/lib/effortLabel.ts:34`）必须同步；`effortEnglishLabel`（Go）与 `frontend/src/locales/index.ts:4630-4639` 的 en 表、`effortLevelLabel` 必须逐字同步。新增档位或改文案时**四处一起改**，由 §7 的排序与标签用例钉住。前端 en 表变更时，PR 评审要顺手检查 Go 侧这张表是否跟上。
- **`/model` 第二步与审批第二屏共享的语义来源**：`showDefault`（`current == "" || configured == ""`）、`fromSetting`（`effort != "" && effort == configured`）、`Current`（会话当前钉值）三者在 Go 侧是三件不同的事。本次修正的核心就是把「行配置值」和「会话钉值」分开；以后若有人把 `FromSetting` 合并回 `Current`，`TestModelEffortPickerMarksFromSettingApartFromCurrent` 会红——先读该用例的注释再改。
- **PR 评审重点**：① §2.3 的 24 个逐字段拷贝点是否全部同步（漏一个就是「UI 选了但没生效」的静默 bug，尤其 `RunPlanReview:1626-1630`）；② 旧 `answer_json` 兼容性；③ `closeModelPicker` 的 289 语义与新的三层 Esc 是否互相破坏；④ `subagentModelOverrideForRef` 的覆盖是否严格限于 `usable.HandedOff`（越界会污染所有子代理派发）；⑤ `PickerItem` 新字段是否严格 `omitempty` 且 Web `SlashPickerCard` 零改动；⑥ `chooseReasoningEffort` 放行 `""` 后，越界的非空档位是否仍被拒。
- **后续可接续**：审批卡的历史渲染是否显示所选强度（`pkg/tool/format.go:2082` 一带）、`plan_review_started` 事件是否带 effort 便于观测 —— 本期都刻意不做。

---

## 11. 评审修订记录（2026-11-15，`improve review-plan`）

本轮评审把 §2 的约 100 处引用逐条打开核对（源码、测试、两份原型、`plans/README.md`），**现状证据高度准确、设计可行**（`usable.HandedOff` 在 `subagentModelOverrideForRef` 作用域内、`planReviewModelRow` 可零签名改动、`SelectModel` 的 `&""` 显式钉空、`handOffModelOf` 全仓仅一个调用点、en 表与 `locales/index.ts:4630-4639` 逐字一致、两份原型哈希与基线相符）。下列为可执行性缺陷的就地修补，**不改发现本身、不改设计、不改状态**（评审当时仍 `BLOCKED-on-prototype`；该闸门已于 2026-11-16 经 owner 双确认解除，见 Step 0c/0d 与 §12 文末「确认」条）：

**A 类（会导致执行失败或语义错误，必修）**
1. **Step 0a 第 7 项 / 行为矩阵**：双写示例 `xHigh  xhigh` 与 `EffortChip.vue:88` 的**忽略大小写**规则（`label.toLowerCase() !== id.toLowerCase()`）矛盾——英文界面只有 `Off  none` 双写。已改示例并写明规则（Step 15c 的 `strings.EqualFold` 代码本就正确，仅原型示例错）。
2. **Step 19**：「对照 `:227` 的既有 Esc 用例」指向错误——`exit-plan-approval.spec.ts` **没有** Esc 用例，`:227` 是 `PlanApprovalDock.test.ts` 的行号。已改为对照 `PlanApprovalDock.test.ts:227`（289 用例）+ e2e 用 `page.keyboard.press('Escape')`。
3. **Step 13 分派点**：`Run()` `:476` 只是外层 `o.handleKey(key)` 调用，真正分派在 **`handleKey` `:985-991`**。已改为在 `handleKey` 的 `planReviewPicking` 检查之前插 `planEffortPicking` 分支。
4. **Step 4 编译缺口**：`runPlanReview` 有**第二个调用点** `completeSurfacePlanReviewAgain`（`chat_surface.go:1162`），改签名后漏改会编译失败。已补「该处传 `""`（再审沿用记录）」+ grep 全部调用点。
5. **Step 13 Enter 陷阱**：原snippet在 `exitPlanEffortMode()` 清掉 `planEffortModel`/`planEffortPurpose` **之后**才读它们，会拿到零值。已改为**先捕获 `model, purpose` 再清状态**。
6. **Step 7 未定义 helper**：`normalizedEfforts(option.Efforts)` 从未定义。已新建 `effortOffered(efforts, want)`（两侧 `ToLower`+`TrimSpace`，catalog id 本是小写），Step 8 复用。
7. **§3 命令表 / Step 20/21/Done 不可满足的门**：`git diff --exit-code -- graph.json` 与「git status 只含 in-scope」会被**工作区里其它会话/批次的未提交改动**判红。改为**开工前快照**（§5：`git status` + `graph.json` 各存一份到 /tmp），收口只判「相对快照新增/新改动的文件全是 §4 In-scope」「graph.json 相对快照无新增 diff」，不再要求工作区干净。评审时见到的预存脏文件：309 遗留 `settingsSchema.test.ts`/`graph.json`/`frontend/src/lib/testdata/`，**以及另一会话正在改的 `pkg/turn/slash.go`**（把内置命令 `AllowedSurfaces` 收窄到 TUI-only——正是 §0.6/§10 记的「残留」，见下条）。快照法对并发会话免疫，无需穷举白名单。

**B 类（精度/健壮性，宜修）**
8. **Step 15c**：整段映射加 `if item.ValueLabel != ""` 守卫——旧 fixture（`commands_test.go:642` 的 `model-effort` 项）不带 `ValueLabel`，无守卫会改它的 `Description`。
9. **Step 13**：给出 `navigatePicker` 的精确调用 `noFilter := false; navigatePicker(&noFilter, &cursor, count, ±1, false)`（签名 `:4642-4653`，`hasFilter=false` 只 clamp）。
10. **Step 7 Verify**：`:10487` 的精确断言在不发 effort 时因 `omitempty` **保持绿**、不会自动变红；已改为「新增一条发 effort 的用例钉新形状 + 保留 :10487 旧形状」。
11. **陈旧引用**：`plans/README.md` 行号系统性偏移——299 映射 `:53→:67`、哈希教训 `:131→:140/144`、基线噪声 `:103→:117`、零 commit `:99→:113`、283 闸门 `:133/137→:150`。
12. **误署权威**：「`/model` 第一步端点列」原署计划 293（实为凭据兜底文案计划），正确是 **267**（`pkg/tui/run.go:5363-5365` 注释「plan 267」）；§2.7 与 §4 两处已改。
13. **原型锚点**：TUI 说明区标题实为 `<h3>计划审批（<code>exit_plan_mode</code>）的语义</h3>`（`:130`，含 `<code>` 标签），原 grep 字面不匹配，已补；Web 原型 `:418/:434` 是 HTML 用法行、CSS 在 `:200/:244`，已分别注明。
14. **示例完整性**：§0.6 / §2.7 的中文档位示例补回 `极低`（Minimal），与 `locales` zh 表一致（绑定规则仍是 `effortLevelLabel()`，示例仅 illustrative）。

**评审时发现的并发状态（非本计划缺陷，已留痕）**：`git status` 显示 `pkg/turn/slash.go` 有**另一会话的未提交改动**（18 行，把 `/model` 等内置命令 `AllowedSurfaces` 收窄到 `{SurfaceTUI}`），正是 §0.6/§2.7/§10 记为「残留、另立项」的那处。本评审**未触碰它**（Hard Rule：只动 `plans/`）。已在 §5 把范围门改为快照法（对并发免疫）、§10 补「执行前分别核对 committed 基线与活工作区、`slash.go` 仍 Out-of-scope、不顺手补完」。**给 owner 的提醒**：这处残留似乎已有人在修；若它先落地，310 的「Web 无内置 slash」语义更一致，但 310 不依赖也不验收它，派发 310 前最好确认该并发改动已收尾，避免两份改动在工作区纠缠。

**未改（核实后判定无需改）**：Step 17 的 `closeModelPicker` 已写明「清暂存但不改 purpose」（289 语义），无需补；`ChatView.vue` 实位于 `frontend/src/views/`，但计划只用裸文件名 + 正确行号 `:1926-1941`（全仓唯一），不构成歧义；`key(k)` `:1377` 是对象方法（非 `function` 声明），引用正确。

---

## 12. 原型反馈同步记录（2026-11-16，`improve review-plan` 续）

owner 在 2026-11-15 出稿后给了六条原型反馈，advisor 已改完两份原型并自验（哈希见 Step 0c / §2.6），本轮把计划规格同步到确认稿。**不改设计方向**；A–F 为确认前的修订留痕，**状态变更见文末「确认」条**（owner 2026-11-16 双确认 → 闸门解除）：

- **(A) 模型设置标记改齿轮图标**（owner：「Model setting改成图标，不要用文字」「(model setting)也改成图标」）：新增 **§0 裁决 7**；把全文约 25 处「Model setting / (model setting) / 模型设置」**文字标记**改为「紧贴档位名的齿轮图标」——TUI `⚙`（`panelAccentStyle` fg110）、Web lucide `Settings`（tooltip/aria-label 复用 `chat.effortModelSetting`）。**i18n 键保留**（tooltip 用）；**`EffortChip.vue:33` 聊天页文字标签刻意不改**（§4 Out-of-scope）。涉及 §0.2/§0.4/§0.6、行为矩阵、§1、§2.5/§2.7、Step 0a/0b/0c、Step 14/15c/18、Phase F/G、§7/§8。
- **(B) 第一屏镜像现有源码**（owner 截图：「UI 界面已经过时……Would you like to proceed? 改成了 Which model should review/carry out this plan? (Esc to go back)，且加上了 filter 输入框」）：Step 0a 新增第 8 项，写明第一屏**单问句**（替换 proceed 行，`overlays.go:607-621`）+ **≥8 行 filter**（`pickerFilterMinRows=8`，`overlays.go:4620`、`review_filter.go`），原型 `gateContent:1356-1369` 已照此画；**这是现有行为不是新增**，执行器不得修回旧形态。Web 第一屏本就单 `.cap`、无 filter（TUI 专属）。
- **(C) 齿轮紧贴档位名**（owner 截图：「齿轮图标与思考档位距离离的太远……与第二张截图中的UI效果保持一致」）：原型把齿轮从远端描述列移到名字列紧贴（TUI `modelEffortScene:880-881` `mark:{glyph:'⚙',style:ACC}`、`desc:''`；Web `.etag` 移进 `.eleft` `gap:7px`）。**Step 15c 重写**：值名双写 + 齿轮都进名字列、描述列留空；因 `SelectItem` 无 mark 字段、`SelectRich` 名字列单样式，给出**最小渲染扩展**（`SelectItem.Mark` + `panelLine.mark` + `selectableColumns` 变参 + `columnRows` 空描述分支 + `richPickerState.panel` 染色），`tabbed_picker.go`/`project_servers.go` 因变参兼容**零改动**；§4 相应加入 `render.go`/`panel_columns.go`、拓宽 `channels.go`。附 STOP/降级闸门（只允许「紧贴但默认色」降级，禁止塞回远端列）。
- **(D) 第二屏问句点名具体模型**（owner：「exit_plan_mode审批面板里选择思考强度时，Would you like to proceed? 改成Which reasoning effort should deepseek/deepseek-v4-flash use? (Esc to go back)」）：浏览器逐帧核对——**TUI 第二屏本就渲染** `Which reasoning effort should deepseek/deepseek-v4-flash use? (Esc to go back)`（`gateContent` 仅在 `!picking&&!effortPicking` 时出 proceed 行；第二屏走 `modelLabel(st.effortModel)`，无 proceed、齿轮紧贴 `❯ High ⚙`），与 owner 要求逐字一致，**无需改 TUI**。**Web 交互 dock 原为 generic**「这个模型用哪档思考强度？」/「Which reasoning effort should this model use?」且整屏不出现模型名 → 改为点名 `{model}`：原型 `I18N.effortCap`（zh/en）改 `{model} 用哪档思考强度？`/`Which reasoning effort should {model} use?`，`effortScreen` 标题 `tr().effortCap.replace('{model}', esc(model.name))`；浏览器核对 zh「deepseek/deepseek-v4-flash 用哪档思考强度？」、en「Which reasoning effort should deepseek/deepseek-v4-flash use?」，齿轮仍紧贴默认档 High、tooltip/aria「Model setting」、console 无 error。计划侧：Step 0b 标题描述、Step 18 第二屏 `.cap` 渲染 `t('chat.exitPlanEffortChoose', { model: modelLabel(pickedModel[action.id]) })`（对照 `PlanApprovalDock.vue:40` 既有命名插值）、i18n 键 `chat.exitPlanEffortChoose` 改 `{model}` 占位。**Web 不加「(Esc to go back)」后缀**（dock 用「返回选模型」按钮 + Esc 键分层，后缀是 TUI 终端专属）。Web 设计稿哈希 `6143bacf…` → `5e082cb3…`（852 行不变，仅文案）。
- **(E) 第一屏问句与 filter 输入框之间空一行**（owner 截图：「Which model should review this plan? (Esc to go back) 和下方的filter输入框之间空一行。原型稿中的同类问题一并修改」）：排查两处 filter 渲染点——`/model` 走的通用 `layout`（原型 `:369`）本就在 head 与 field 之间 `push(LB.of(''))`，**无需改**；审批选择器 `gateContent` 问句后直接 `fieldPad`、漏空行 → 在 `if(showsFilter)` 块首补 `blank()`（原型 `:1365`），浏览器按行背景核对「问句 → 真空气行(无 bar 底) → 三行灰 filter 条 → 真空气行 → 列表」，评审/交接/已输入过滤三场景一致、console 无 error。**同类问题追到源码**：`overlays.go:757-759` 注释承诺「air above it」但 `:760-762` 漏写（仅 `:776` 后空行），原型原先照抄该遗漏 → 新增 **Step 14a** 令执行器在 `if o.planReviewShowsFilter()` 块首补 `writeLine("")`。Web 第一屏无 filter，不涉及。TUI 设计稿哈希 `ddb28205…` → `1f266a54…`（1780 → 1782 行）。
- **(F) 第二屏问句与思考强度列表之间空一行**（owner 截图：「Which reasoning effort should deepseek/deepseek-v4-flash use? (Esc to go back) 和下方的思考强度列表之间空一行。评审第二屏，交接第二屏和第二屏：行没配默认档都要一并修改」）：三条第二屏场景（`exit-plan-effort` / `exit-plan-effort-handoff` / `exit-plan-effort-default`）共用 `gateContent` 的 `effortPicking` 分支，在问句 `text(...)` 后、`effortOptions.forEach` 前补一处 `blank()`（原型 `:1345`）即三处生效；浏览器按行背景核对三场景均「问句 → 真空气行 → 首档位行」（无默认档场景首行为 `❯ Default`），console 无 error。`/model` 第二步走通用 `layout`（head→blank→body，`:373`）本就有空行，无需改。源码侧 **Step 14** 的新 `case o.planEffortPicking:` 分支首行补 `writeLine("")`；§7 加 `TestExitPlanEffortPickerLeavesAirBelowTheQuestion`（三夹具各断言一次）。TUI 设计稿哈希 `1f266a54…` → `8f169c3e…`（1782 → 1784 行）。
- **哈希/锚点刷新**：Step 0c 与 §2.6 改记设计稿哈希（TUI `8f169c3e…` 1784 行、Web `5e082cb3…` 852 行）+ 原始基线；§2.6 行号锚点改为「grep 符号名优先」（文件已增长，旧行号漂移）；Step 0a/0b Verify 改为「核对 == 确认稿哈希」。
- **新增 scope 提醒**：(C) 的渲染扩展使 `pkg/tui/render.go`、`pkg/tui/panel_columns.go` 转入 In-scope（原仅 `channels.go`「确认行」）；这是 owner「以原型为准 + 齿轮紧贴」裁决的必要代价，已在 §4 留痕。
- **（确认）owner 2026-11-16 双确认 → 闸门解除**：owner 原话「两份原型已确认，必须将本期针对原型稿的修改写进计划，要求执行者按原型稿实现」。确认稿哈希 TUI `8f169c3e…`（1784 行）/ Web `5e082cb3…`（852 行），与 Step 0a/0b Verify、§2.6 一致。据此：① Step 0c 闸门判据两条均满足，计划**脱离 `BLOCKED-on-prototype`**、可进入 Step 1；② 新增 **Step 0d 执行 mandate**——确认稿为唯一权威、A–F 六项改动逐条映射到实施步（14/14a/15c/18 + i18n）、验收加「实现截屏 vs 确认稿逐帧比对」、原型脚手架不进实现；③ §2.6 两处「待 owner 确认」改记「owner 2026-11-16 已确认」。**执行器仍须开工前 `shasum` 核对原型 == 确认稿哈希，不等即 STOP。**
