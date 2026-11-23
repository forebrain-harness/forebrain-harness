# Plan 138：web 审批挂在等待中的工具卡上（取代 129 Part B Step 9）、计划审批占输入框的位置

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发 "STOP conditions" 时停下报告。
> **不要更新 `plans/README.md`**。**提交**：按 owner 当次派发指令（worktree 必须基于 dev HEAD）；没有指令时不 commit、不 push。
>
> **原型是唯一标准**：`docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html`，入口「对话 · 审批卡（全部工具）」（12 张审批卡 + ⑬⑭ 答完之后）、
> 「对话 · 计划审批 ① 只有计划」…「⑥ 批准之后」、「对话 · 全部消息卡」（其中一张等你批准的申请权限）、「对话 · 抓取审批」「对话 · 搜索审批」「对话 · 记忆写入审批」；
> 页面顶部「2026-10-25 审批卡重画（plan 129 评审）」一节是逐条说明。1:1 还原，尺寸颜色从原型 CSS 抄（`.tool.waiting`、`.gate-ask`、`.gate-acts`、
> `.gate-fb`、`.xp-dock*` 等）。
>
> **组件规则（owner 2026-10-25）**：先 ai-elements-vue（`@repo/elements`：`confirmation`），再 shadcn-vue（`button`、`input-group`、`kbd`、`scroll-area`），
> 都没有才自写，并在注释写明原因。
>
> **前置（否则 STOP 1）**：`129` Part A 已在 dev（`grep -n "RejectedStopText" pkg/tool/format.go`）；`135`、`136`、`137` 已落地
> （`grep -n "deny_feedback" pkg/tool/notification.go`、`test -f frontend/src/components/chat/ActivityLine.vue`、`test -f frontend/src/components/chat/DiffBlock.vue`）。
>
> **Drift check**：按符号与 `data-testid` 定位（行号写于 dev `1c8f5d9`）；对不上 → STOP 1。

## Status

- **Priority**: P1
- **Effort**: L
- **Risk**: MED–HIGH（审批是阻塞回合的交互；拒绝的两种走向不能做反）
- **Depends on**: 129 Part A、135、136、137
- **Supersedes**: `plans/129-rejection-semantics-like-claude-code.md` 的 Part B Step 9（Step 8 的原型已由 owner 评审后重画，见 129 文件里
  「2026-10-25 评审重画」一段与本计划）。本计划完成即视为 129 Part B 完成。
- **Planned at**: dev `1c8f5d9` + 工作区原型，2026-10-25

## Why this matters（owner 2026-10-25 定稿）

1. **一张卡，一个节点**：等你批准的调用就是活动线上的一张普通工具卡——节点是时钟，卡头是引擎的进行时说法（136 已做，中文界面：正在写入 /
   正在运行 / 正在申请权限 …）。不再是“旧式调用行 + 单独的审批框”，也不再把所有待批准请求堆在对话末尾。
2. **面板 + 底栏**：面板只放决定要看的事实（行数、diff、命令会做什么、主机、参数、记忆笔记正文）；问题与回答在卡底部的浅品牌色底栏，先看事实再回答。
   等待中的卡边框用品牌色——整条对话只有这里更醒目。
3. **拒绝按引擎分两族**（`pkg/safety/permission_types.go` `BuildApprovalDecisionOptions`）：
   - **cancel 族**（写文件、改文件、补丁、命令、外网、取页面、搜索、MCP、进入计划模式、兜底）：拒绝按钮保留自己的动词（不要写 / 不要运行 / 不要取 …），
     点开说明框（占位“告诉它该怎么做（可不填）”/ `and tell Forebrain Harness what to do differently`）；发送按钮随内容变——空着是「拒绝并停下」
     （= 不带说明拒绝，回合中止，129 语义），有字是「发送」（= 带说明拒绝，只拒这一次，回合继续）；Esc = 拒绝并停下；点任何“允许”类按钮收起说明框。
   - **decline 族**（`request_permissions` 的「不授予」、记忆工具的「不要记」）：按钮直接提交，回合继续，**没有说明框**。
4. **答完之后**：写了说明 → 卡收成“已拒绝 …”，一句“你拒绝了，文件没有写入。”（命令：“你拒绝了这条命令，它没有运行。”；其它：“你拒绝了这次调用，它没有执行。”），
   下面一行是用户的说明；空着拒绝或 Esc → 卡收成“已拒绝 …”，同一回答里未开始的调用不出卡（129 Part A 的隐藏行），“已工作 …”收尾。
5. **计划审批不在对话里画卡**（owner：「终端不画卡，web 端就不画卡，与终端保持一致」）：等待时**计划与回答占输入框的位置**（终端的审批层占 composer）——
   头一行“计划 + 文件名 + 眼睛（在抽屉打开计划标签）”，中间计划正文（滚动），底栏问题与选择（批准并清空上下文（已用 N%）/ 批准 / 请其他模型评审 / 继续规划）；
   评审进行中底栏只留“X 正在评审这份计划… + 停止”；选评审模型时底栏展开模型列表。继续规划：同一种说明框，**说明必填**（空时发送置灰），旁边提示 `Esc 拒绝并停下`；
   去掉单独的「取消」。批准后输入框回来，对话里落一张“已退出计划模式”。

## Current state

- `frontend/src/components/chat/ApprovalCard.vue`：两种用法。带 `block`：已决定审批的那一行字；不带：**对话末尾**列出本会话所有待处理请求
  （`usePendingActions` 的 `pendingApprovals` / `pendingAsk`）：exit_plan（`data-testid="exit-plan-approval"`，计划正文 `MessageResponse`、按钮
  `exit-plan-approve-clear` / `exit-plan-approve` / `exit-plan-keep-planning` / `exit-plan-ask-review`、说明框 `exit-plan-feedback` + 发送 + 取消、评审进行中 / 选模型）、
  web_fetch（`Confirmation` 自己拼）、其余全部走 `ApprovalGateCard`。ChatView 在 `</ConversationContent>` 之前渲染一个不带 block 的 `ApprovalCard`。
- `frontend/src/components/chat/ApprovalGateCard.vue`：`@repo/elements/confirmation` 的 `Confirmation` / `ConfirmationRequest` / `ConfirmationActions` /
  `ConfirmationTitle` / `ConfirmationAction`；问题 `gate.question`，事实 `gate.facts.facts`，记忆正文 `gate.facts.bodyMarkdown`，diff `diffRows`（`.gate-diff`），按钮是
  `options`（引擎的决定选项，`approvalDecisionLabel` 取文案），点击 `submit(option)`。
- `frontend/src/composables/usePendingActions.ts`：`submitApprovalDecision(action, option)` → `forebrainApi.actionsApprove(action.id, { reason: \`web:${option.decision}\`, decision: option.decision, … })`；
  `approveExitPlan(a, clearContext)` → `actionsApprove(a.id, clearContext ? {clearContext: true} : {})`；`keepPlanning(a)` → `actionsDeny(a.id, reason ? {reason} : {})`；
  还有 `exitPlanRequest`、`isExitPlan`、`requestReview`、`stopReview`、`toggleModelPicker`、`approvalDecisionOptions`、`approvalDecisionLabel`、`approvalGate`、
  `isWebFetchAction`、`webFetchPage`、`requesterLabel`；`ActionRow` 没有 tool step id。
- 网关决定语义（`pkg/turn/approval.go`）：`deny` / `decline` → `Actions.Deny(ctx, id, reason)`；`cancel` → `Actions.Cancel`（129 Part A 之后：主代理的 cancel =
  写 `RejectedStopText` 并中止回合）。
- 待处理请求与工具卡的对应：时间线里有 `kind: 'approval'` 块（`actionId`、`toolStepId`，`useChatStream.ts` 按 `toolStepId` 把它放到被拦的工具卡后面）；网关的
  `requires_action` 数据带 `tool_step_id`（`pkg/gateway/approval.go` `withApprovalToolStepID`）。exit_plan 的等待不画工具卡（`isExitPlanGate`）。
- 计划标签：`ChatView.vue` `openPlanTab(plan: SessionPlan)`、`forebrainApi.sessionPlans(sessionId)`。
- e2e：`frontend/e2e/approval-families.spec.ts`、`exit-plan-approval.spec.ts`、`web-fetch-approval.spec.ts`、`message-cards.spec.ts`。

## Commands you will need

```bash
cd frontend && pnpm vitest run <pattern> && pnpm typecheck
cd .. && FOREBRAIN_E2E_SPEC="e2e/approval-families.spec.ts e2e/exit-plan-approval.spec.ts e2e/web-fetch-approval.spec.ts e2e/message-cards.spec.ts" scripts/acceptance/web_e2e.sh
```
真机（fake driver 驱动不了的拒绝两种走向，按仓库规则用 glm-5.3 真模型跑，见 Step 8）。

## Scope

- 新建：`frontend/src/components/chat/ToolGateFooter.vue`、`ToolGateFacts.vue`、`PlanApprovalDock.vue`、`frontend/src/composables/providePendingActions.ts`（+测试）。
- 改：`usePendingActions.ts`（两个拒绝方法、按 step 查请求）、`ToolCallCard.vue`（等待时的面板与底栏、拒绝后的正文）、`ChatView.vue`（提供 pending、去掉末尾的待处理列表、
  输入框位置换成计划审批）、`SubagentConversation.vue` / `SubagentTab.vue`（子代理里的等待卡同样挂底栏）、`WorkshopView.vue`（同上）、`ApprovalCard.vue`（只保留
  已决定那一行字与提问卡 `pendingAsk` 的渲染；exit_plan / gate / web_fetch 分支删掉）、`ApprovalGateCard.vue`（删除或缩成 `ToolGateFacts` 的薄壳——见 Step 3）、
  locales、对应单测与 e2e。
- **不改**：引擎、网关（若 Step 1 发现必须让网关在待处理列表里带 `tool_step_id`，按 STOP 3 回报）、提问卡（`AskTabs`）的交互。

## Steps

### Step 1：请求 ↔ 工具卡的对应

1.1 `providePendingActions.ts`：ChatView 调一次 `usePendingActions(...)`，用 `provide` 交给子树；`injectPendingActions()` 在没有 provider 时返回 `null`
（卡片在线外 / 测试里照常工作）。`ApprovalCard`（不带 block 的用法）改为 inject 同一个实例，**不能**再自己建第二个（否则两份状态、两次加载）。
1.2 `usePendingActions` 加 `function pendingForStep(stepId: string, approvalBlocks: {actionId: string; toolStepId?: string}[]): ActionRow | null`：
先在时间线 `approval` 块里按 `toolStepId === stepId` 找 `actionId`，再在 `pendingApprovals` 里按 id 找；找不到返回 null。时间线块由调用方传入
（ChatView 从当前消息的 `blocks` 收集；子代理从它自己的 `entry.blocks` 收集）。
1.3 **找不到对应工具卡的待处理请求**（例如没有工具卡的老会话、`network_inline`）：仍在对话末尾显示，但用同一套 `ToolGateFacts` + `ToolGateFooter`，外面套一个
等待中卡片的外框（品牌色边框、无卡头节点），卡头文字用 `approvalGate(a).question`。

**Verify**：`usePendingActions.test.ts` 加 `pendingForStep` 三例（对上、`toolStepId` 缺失、请求已不在列表）。

### Step 2：两种拒绝（`usePendingActions.ts`）

```ts
  /** A refusal with the user's words: this one call is refused, the turn
   *  carries on, and the words reach the model (plan 129). */
  async function refuseWithWords(action: ActionRow, words: string)   // actionsApprove(id, { decision: 'deny', reason: words })
  /** A refusal without words (empty send, or Esc): the call is refused and the
   *  turn stops, waiting for the user (plan 129's cancel). */
  async function refuseAndStop(action: ActionRow)                     // actionsApprove(id, { decision: 'cancel', reason: 'web:cancel' })
```
decline 族照旧 `submitApprovalDecision(a, option)`（`option.decision === 'decline'`）。
**先确认语义（否则 STOP 4）**：读 `pkg/turn/approval.go` 的 deny 分支与 129 Part A 的续跑，确认 `deny` + `reason` 会作为 `DenyFeedback`（用户原话）送进续跑、
主代理拒绝结果是 `To tell you how to proceed, the user said:\n<words>`；`cancel` 走 `RejectedStopText` 并中止回合。在报告里贴出你读到的代码位置。

### Step 3：等待中的卡（`ToolGateFacts.vue`、`ToolGateFooter.vue`、`ToolCallCard.vue`）

3.1 `ToolGateFacts.vue`（卡片面板里的事实）：props `action`、`gate`（`approvalGate(a)`）。照原型 `.tool-b .facts`（12.5px、`--text-2`，`.lit` 等宽 `--text`，
多条时一行一条）画 `gate.facts.facts`；`gate.facts.bodyMarkdown`（记忆笔记）照原型 `.tool-b .memo`（左 2px 灰线、`max-height: 160px`、底部淡出遮罩）用 `MessageResponse`；
diff 用 137 的 `DiffBlock`（`approvalGates.ts` 里产出 `diffRows` 的地方改成产出 `DiffRow[]`：有行号就带上，没有就 `null`）；补丁（apply_patch）多文件时用 137 的
`FileChangeList`。要求者（子代理发起时 `requesterLabel(a)`）照现有样式留在事实最上面。现有 `ApprovalGateCard.vue` 的这部分搬过来，`ApprovalGateCard.vue` 删除
（其测试按新组件改写）。
3.2 `ToolGateFooter.vue`：外层用 `@repo/elements/confirmation` 的 `Confirmation`（`state="approval-requested"`，`approval={ id }`），样式覆盖成原型 `.gate-ask`
（`padding: 10px 12px 11px`、`gap: 9px`、`background: var(--brand-soft)`、有事实面板时上边线 `var(--brand-border)`）；问题用 `ConfirmationTitle`（13px、600）；
按钮行用 `ConfirmationActions`（左对齐、可换行、`gap: 6px`，覆盖掉它默认的 `justify-end self-end`）+ `ConfirmationAction`（shadcn `Button`，`sm`；第一个“允许”类是
主按钮，其余 outline）。按钮来自 `approvalDecisionOptions(a)` 的顺序：
- 允许类（`accept*`、`grant*`、`apply_network_policy_amendment`、`accept_with_execpolicy_amendment` …）→ 点击 `submitApprovalDecision` 并收起说明框；
- `decline` → 普通按钮，直接 `submitApprovalDecision`；
- `cancel` → **切换按钮**（`aria-expanded`），文案 = `approvalDecisionLabel(option, a)`（各族动词：不要写 / 不要运行 …——若现有文案是一个“不”字或 `No`，改 locales 回到动词），
  按下态照原型 `.app .gate-acts .btn.on`（白底、brand 边框与文字、内描边）。
说明框（cancel 按钮按下后出现）：shadcn `InputGroup`（输入框 + 尾部按钮）：输入框 28px 高、白底；占位 `t('chat.refuseWordsPlaceholder')`（新增：`告诉它该怎么做（可不填）` /
`and tell Forebrain Harness what to do differently`）；尾部按钮（主按钮）文案随内容实时变：空 → `t('chat.refuseAndStop')`（`拒绝并停下` / `Refuse and stop`），有字 →
`t('chat.refuseSend')`（`发送` / `Send`）；悬停提示同步变（`拒绝并停下本轮，等你说下一步（Esc 同）` / `只拒绝这一次调用，按你的说明继续`）。打开时自动聚焦输入框；
Enter = 点发送；**Esc = `refuseAndStop`**（不是“收起”）。发送：有字 → `refuseWithWords(a, words.trim())`；空 → `refuseAndStop(a)`。提交中按钮禁用；错误一行红字。
不画任何解释性长句（owner 文案规则）。
3.3 `ToolCallCard.vue`：新增可选 prop `pendingAction?: ActionRow | null`（由父组件用 `pendingForStep` 算出传入）。有它时：
- 卡片加等待态外观：面板边框换 `var(--brand-border)`（原型 `.tool.waiting, .al-run > .tool.waiting > :not(.tool-h)`），卡片保持展开（不允许折叠成一行）；
- 面板 = `ToolGateFacts`（取代平时的结果正文），面板下 = `ToolGateFooter`；
- 节点是时钟（136 已按状态 `awaiting` 画）。
3.4 拒绝后（状态 `denied`）：正文 = 一句话（按工具：shell → `你拒绝了这条命令，它没有运行。`；write_file / edit_file / apply_patch → `你拒绝了，文件没有写入。`；其它 →
`你拒绝了这次调用，它没有执行。`，英文各一句，进 locales）+ 有 `step.denyFeedback` 时一行说明（前面 lucide `CornerDownRight` 14px 灰，原型 `.tool-b .said`）。
3.5 子代理视图（`SubagentConversation`）与技能工坊同样传 `pendingAction`（子代理的请求带 `agentId`；`pendingForStep` 用子代理自己的时间线块）。

**Verify**：新建 `ToolGateFooter.test.ts`：① cancel 族点拒绝按钮出现说明框且聚焦，空发送调 `refuseAndStop`、有字调 `refuseWithWords`、Esc 调 `refuseAndStop`、按钮文案随内容在
「拒绝并停下」/「发送」间切换；② decline 族没有说明框、点击即 `submitApprovalDecision(decline)`；③ 点允许按钮收起说明框；`ToolCallCard.test.ts`：有 `pendingAction`
时面板是事实、底栏存在、边框类存在；`denied` + `denyFeedback` 时两行正文。

### Step 4：对话末尾不再堆审批（`ChatView.vue`、`ApprovalCard.vue`）

- ChatView 末尾的 `<ApprovalCard :session-id … />` 只渲染：提问卡（`pendingAsk`，交互不变）与 Step 1.3 的“找不到工具卡的请求”。exit_plan 不在这里（Step 5）。
- `ApprovalCard.vue` 删掉 exit_plan、web_fetch、`ApprovalGateCard` 三个分支与它们的样式；保留已决定那一行字。
- web_fetch / web_search / 记忆写入也走 Step 3（它们有工具卡）。

### Step 5：计划审批占输入框（`PlanApprovalDock.vue`、`ChatView.vue`）

5.1 ChatView：当 `pendingApprovals` 里有 `isExitPlan(a)` 的请求时，`.chat-composer` 里**不渲染** `PromptInputProvider` 那一整块（输入框、队列预览、附件……），改渲染
`PlanApprovalDock`；“工作中”状态行（`RunStatusLine`）照旧在它上面。批准 / 继续规划提交成功、请求离开列表后，输入框回来（草稿保留：输入框组件用 `v-show` 还是重建，
以草稿不丢为准——先查 `ForebrainPromptTextarea` 的草稿是否存在别处，存在就可以重建）。
5.2 `PlanApprovalDock.vue`（照原型 `.xp-dock*`：宽度与输入框相同 `min(760px, 100% - 48px)`、12px 圆角、`var(--brand-border)` 边框、白底、
`box-shadow: 0 -8px 24px rgba(16,24,40,.06)`）：
- 头一行：lucide `Map`（brand 色）+「计划」+ 计划文件路径（灰、等宽、省略）+ 右侧眼睛按钮（136 的眼睛组件，提示“显示全部”）→ `openPlanTab(最新计划)`。
  路径与要打开的计划：挂载时 `forebrainApi.sessionPlans(sessionId)` 取最新一条（`path`、`markdown`）；取不到就不显示路径，眼睛打开一个用 `exitPlanRequest(a).planText`
  临时构造的计划标签。
- 中间：计划正文 `MessageResponse :content="planText"`，放在 shadcn `ScrollArea` 里，`max-height: min(240px, 30vh)`，`padding: 12px 14px`，底部淡出遮罩（原型 `.xp-dock .xp-doc`）。
- 底栏：与 `ToolGateFooter` **同一种外观**（`.gate-ask`，上边线 `var(--brand-border)`），内容沿用 `ApprovalCard` 现有的 exit_plan 逻辑：
  - 正常：问题「要开始执行吗？」+ 按钮「批准并清空上下文（已用 N%）」（主）/「批准」/「请其他模型评审」（有评审模型时）/「继续规划」（切换按钮，按下态同 3.2）；
  - 继续规划展开：`InputGroup`（占位 `t('chat.exitPlanFeedbackPlaceholder')`）+ 尾部「发送给规划模型」（**说明为空时禁用**）+ 旁边灰字 shadcn `Kbd` `Esc` +「拒绝并停下」；
    Enter = 发送（有字时）；**Esc = `refuseAndStop(a)`**（计划审批的不带说明拒绝，129）；去掉原来的「取消」按钮（再点「继续规划」收起）；
  - 评审进行中：一行“<模型> 正在评审这份计划…”+「停止」（`stopReview`）；
  - 选评审模型：底栏里展开模型列表（`requestReview`，当前那台标「当前」）。
  保留现有 `data-testid`（`exit-plan-approval`、`exit-plan-approve-clear`、`exit-plan-approve`、`exit-plan-keep-planning`、`exit-plan-ask-review`、`exit-plan-feedback`、
  `exit-plan-stop-review`、`plan-review-model`），e2e 继续用。
- 子代理的计划审批（`agentId` 非空）：不占主输入框，按 Step 3 挂在子代理视图里（若子代理的 exit_plan 也不画工具卡，就用 Step 1.3 的外框，放在子代理消息流末尾）。

**Verify**：新建 `PlanApprovalDock.test.ts`：四种状态渲染；继续规划空说明时发送禁用、Esc 调 `refuseAndStop`；眼睛调 `openPlanTab`；`ChatView.test.ts`：有待处理 exit_plan 时
输入框不在、`PlanApprovalDock` 在，请求消失后输入框回来且草稿还在。

### Step 6：文案与旧样式清理

- locales 新增：`chat.refuseWordsPlaceholder`、`chat.refuseAndStop`、`chat.refuseSend`、两条发送按钮提示、三句“你拒绝了…”、`chat.showAll`（若 136 未加）。
- 删除不再用的 locales 键与样式（旧的 `.xp-head`、`.xp-doc` 等）；`grep -rn "exit-plan-feedback\|gate-diff" frontend/src` 只剩新组件。

### Step 7：e2e

- `approval-families.spec.ts`：每族一张——等待卡在被拦的工具卡底部（不是对话末尾）、节点是时钟、cancel 族点拒绝出现说明框、decline 族没有；写一句说明发送 → 卡变“已拒绝”
  且下面显示原话（用 fake provider 的既有夹具；若夹具无法产生某族的请求，在报告里列出未覆盖的族）。
- `exit-plan-approval.spec.ts`：等待时输入框被计划审批替换、没有对话里的卡；继续规划空说明发送禁用；批准后输入框回来、对话里出现“已退出计划模式”。
- `web-fetch-approval.spec.ts`：抓取审批挂在“正在抓取 …”卡底部。
- 截图：原型「审批卡（全部工具）」12 张 + ⑬⑭、「计划审批 ①–⑥」对应的实现截图并排放进报告，逐项改到一致。

### Step 8：真机（拒绝的两种走向）

fake provider 不发需要审批的工具调用的场景，按仓库规则在主树用 glm-5.3 真模型跑（隔离 home，key 从 `.env` 注入；见 `run-forebrain` 技能与
`scripts/acceptance/web_e2e.sh` 的 `FOREBRAIN_E2E_REAL_LLM=1`）：
1. 让模型执行 `rm -rf <临时目录里的文件>`（会触发 forced_rm 审批）→ web 上点「不要运行」→ 写“改成移到 trash/” → 发送：卡变“已拒绝”+ 原话，回合继续，模型给出移到 trash 的做法。
2. 同样的请求 → 「不要运行」→ 空着发送（或 Esc）：卡变“已拒绝”，回合结束，没有后续模型回答，输入框可用。
3. 让模型进入计划模式写一个小计划 → 输入框被计划审批替换 → 「继续规划」空说明不能发送 → 写一句发送 → 回到规划；再次出现后「批准」→ 输入框回来、对话出现“已退出计划模式”。
每段截图放进报告。

## Done criteria

- [ ] 等待中的请求挂在对应工具卡底部（主视图、子代理、工坊），对话末尾只剩提问卡与无工具卡的请求
- [ ] cancel 族：说明框、发送文案切换、空 / Esc = 拒绝并停下、有字 = 带说明拒绝；decline 族直接生效无说明框
- [ ] 拒绝后卡片两行正文（一句话 + 原话）
- [ ] 计划审批占输入框位置，四种状态与原型一致，继续规划说明必填，Esc = 拒绝并停下，批准后输入框回来且草稿不丢
- [ ] `ApprovalGateCard.vue` 已删除，审批相关 diff 用 `DiffBlock`
- [ ] `pnpm typecheck` 0；`pnpm vitest run` 无新增失败；上面四个 e2e 通过；真机三段截图；原型对照截图
- [ ] 129 Part B 在本计划完成后可标 DONE

## STOP conditions

1. 前置（129 Part A、135–137）未落地或锚点对不上。
2. 某族的 `approvalDecisionOptions` 里没有 `cancel` 也没有 `decline`（拒绝无从判断）——回报该族的选项。
3. 时间线 `approval` 块没有 `toolStepId`、待处理请求又对不上工具卡，且这种情况在当前代码里很常见（不是老会话的个例）——回报，可能需要网关在 `/api/actions` 里带 `tool_step_id`。
4. `deny` + `reason` 没有作为用户原话进入续跑，或 `cancel` 没有走“拒绝并中止”（129 Part A 的语义）。
5. 计划审批占输入框后，输入框的草稿、附件或队列在请求结束后丢失，且无法在不改 `PromptInput` 内部的前提下保住。
6. 某一步验证连续两次修正后仍失败。

## Maintenance notes

- 拒绝的走向只由“有没有说明”决定；以后新增的审批族只要在 `approvalDecisionOptions` 里给出 `cancel` 或 `decline`，底栏自动选对交互，不要为某一族写特例。
- 计划审批与终端一致：等待时不画卡、占输入位置。终端若改变这一点，web 跟着改，不要单独演化。
- `ToolGateFooter` 是唯一的审批回答组件；提问卡（`AskTabs`）是另一类，不要合并。
