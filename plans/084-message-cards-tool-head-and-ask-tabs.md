# 084 — 对话消息卡：工具卡单行头、待办卡说中文、提问卡走标签页、视图模式下拉

## Status

- **DONE**（2026-10-20 立项，基线 `cd11539`；2026-10-20 执行完毕，评审 APPROVE，改动留工作区未提交）
- 复审：2026-10-20 冷读复核（基线 `cd11539`）。校准了 6 处引用，其中 3 处会直接做错：
  摘要函数名（`toolCompletionSummary` 不存在）、`PlanUpdateCard` 的目录、
  **子代理输入框不在 `SubagentDrawer.vue` 而在 `SubagentTab.vue`**（原逃生口 2 已命中，见 Step 5）。
- **owner 2026-10-20 追加裁决**：简洁模式**连清单卡一起隐藏**（原 Step 5.4 只列了
  `thinking`/`tool`/`subagent`）。实现即按此落地：`lib/viewMode.ts` 的 `plan` 分支 +
  `ChatView.vue` 两处 `viewMode === 'default'`（`PlanUpdateCard` 与 plan 块）+
  `SubagentConversation.vue` 的 `blockVisibleIn` 过滤；`ChatView.test.ts` 的
  「默认画清单、简洁不画」用例钉住它。清单的进度与记录仍在工作台「待办」卡与终端。
- 优先级 **P1**，工作量 **L**，风险 **MED**
- 依据：owner 2026-10-20 已审批的原型 `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html`：
  「对话 · 全部消息卡（工具 / 思考 / 子代理）」（`#ij-cards`）、「对话 · 简洁模式（只看回答）」
  （`#ij-cardsbrief`）、「对话 · 提问卡（全部表单类型）」（`#ij-asks`）、「子代理 · 视图模式下拉」
  （`#ij-subview`）。
- 以及 owner 2026-10-20 的单条裁决（本轮新增，必须一起落）：
  「现在终端 tui 的 user_interaction 消息卡的 UI 效果必须与 web 端对齐，去掉 Asked user 后面的文案，
  output 区域改成：问题原文 → 用户回答原文」。

本计划**同时改 web 与 TUI**（这是 owner 明确要求的一处跨端对齐）。除此之外 TUI 其它部分仍按
owner 早前的「不涉及终端 tui，tui 保持现状」处理，不要顺手改。

---

## 为什么要做四件事

1. **工具卡的头会被 2 KB 命令撑高。** 头是「一句调用摘要」，而摘要在 shell 调用里就是整条命令
   （引擎下发 `ran <command>`）。头一旦折行，同一张卡在半屏里只看得见头。原型把规则定死：
   头永远一行（超出省略 + 悬停看全文），放不下的调用原样搬到正文顶部。
2. **待办卡在中文界面里说英文。** 引擎下发 `Title: "Updated Plan"`（`pkg/tool/session_tools.go:234`），
   web 直接印它（`useChatStream.ts:1601`），于是中文界面里出现 `Updated Plan`。owner 定的中文名是
   **更新待办**（`chat.updatedPlan` 现在 zh 也是英文，`locales/index.ts:1533`）。
3. **提问卡在 web 上是一屏四道题。** 终端是一道题一屏、上方一排标签页（`pkg/tui/overlays.go:2924-2973`
   的 `renderNavBar` 与 `:2713-2782` 的 `renderQuestionView`），web 现在是平铺所有题 + 一堆 radio
   （`ApprovalCard.vue:218-252`）。owner 要求 web 按终端形状重画。
4. **没有「简洁」这个阅读方式。** owner 定的两个视图：默认全量、简洁只留助手回答。它同时出现在
   主对话输入框与每个子代理标签的输入框里，是「阅读方式」不是「删消息」。

---

## 现状事实（行号基于 `cd11539`，执行前自己复核）

### 工具卡头

- `frontend/src/components/chat/ToolCallCard.vue:1-44`：`<Tool>` → `<ToolHeader :title="title" :state="toolState">`
  → `<ToolContent><ToolOutput/></ToolContent>`。
- `ai-elements-vue/packages/elements/src/tool/ToolHeader.vue`（pnpm workspace 里 `@repo/elements` 指向它，
  `frontend/pnpm-workspace.yaml` 第 2-3 行）：
  标题那一行是 `<span class="min-w-0 break-words text-left font-medium text-sm">{{ title ?? derivedName }}</span>`
  ——`break-words` 就是「长命令把卡头撑高」的原因。
- `title` 从 `step.summary` 来；引擎给 shell 的摘要形如 `ran <整条命令>`
  （`pkg/tool/format.go` 的 `SummarizeToolStep`，函数起点 `:274`；`"ran "` 那几个字面量在
  `:367-402`。**没有**叫 `toolCompletionSummary` 的函数）。

### 待办卡标题

- 引擎：`pkg/tool/session_tools.go:230-241` 的 `emitPlanUpdateStep` 输出
  `PlanUpdatedPayload{Title: "Updated Plan", Items: [...], …}`（`Title` 恰在 `:234`；
  待办项的字段名是 `Items`，不是 `todos`）。
- web：`frontend/src/composables/useChatStream.ts:1598-1604` 取 `data.title`；
  `frontend/src/components/chat/PlanUpdateCard.vue:7` 回退到 `t('chat.updatedPlan')`；
  `locales/index.ts:1533`（zh）与 `:3190`（en）都是 `'Updated Plan'`。
  （注意目录是 `components/chat/`，不是 `components/workbench/`。）
- 终端：`pkg/tui/render.go:6794` 也返回 `"Updated Plan"`。**TUI 这一条保持现状**（owner 只要网页说中文）。

### 提问卡

- web 现状：`ApprovalCard.vue:218-252`（工作台里还有一份 `PendingActionsPanel.vue:166-203`）——
  每题一段 prompt + 一排 radio/checkbox + 「其他」输入框 + 一个提交按钮。
- 提交协议：`usePendingActions.ts:271-285` `submitAsk` → `{answers: [{questionId, optionIds, otherText?}]}`。
  **这条线不动**，只换呈现。
- 终端形状（照它画）：`pkg/tui/overlays.go:2924-2973` 的标签页栏
  （`← [☐ header] … [✔ 提交] →`，未答 ☐ / 已答 ✔ / 当前反显；单选一题时也画那一个 chip、不画箭头与提交；
  函数文档注释在 `:2918`），`:2713-2782` 的单题正文（加粗 prompt、编号选项 `1. 2. 3.` + `(recommended)` + 说明缩进、最后一项
  `N. 其他`、多选才有 Next/Submit、聚焦项 preview、`:2779` 的 `N. Chat about this`、`:2782` 的帮助行）。
- 终端头行：`pkg/tui/render.go:2793-2803` 的 `user_interaction` 分支现在把
  `tool.UserInteractionQuestionsLabel(meta.Input)` 塞进 `target`（赋值在 `:2803`），于是卡头变成
  `● Asked user 项目类型 · 覆盖文件卡 <0.1s`。
  `pkg/tool/format.go:2102-2107` 的 `userInteractionLabel`（`:2104`）同样拼了 `" · " + questions`
  （它产出 step summary，两个表面共用）。
- 正文（问题原文 → 回答原文）**已经**由引擎生成：`pkg/tool/format.go:1947-2025` 的
  `FormatUserInteractionResult` 用问题原文做展示行、按 header 查答案（本仓库未提交改动已含这条，
  `pkg/tool/format_test.go:2770` 的 `TestFormatUserInteractionResultPrintsTheQuestionNotTheHeader`
  钉住了 `问题\n  → 答案` 的形状）。本计划要做的是**把头的尾巴去掉**，
  并给 TUI 补一条钉住「头只有 Asked user、正文是问题→答案」的测试。

### 视图模式

- 现在没有这个功能（`grep -rn "简洁\|concise" frontend/src` 无命中）。
- 块渲染有两个入口，都要认这个开关：
  - 主对话：`frontend/src/views/ChatView.vue:189-240`（`block.kind === 'thinking' | 'assistant' | 'tool' | 'subagent' | 'approval' …`）
  - 子代理标签：`frontend/src/components/chat/SubagentConversation.vue:41-80`
- 输入框底栏（左侧）：`views/ChatView.vue:443-446`（容器 + `ComposerPlusMenu` 444 + `ModeChip` 445 +
  `ApprovalPresetPicker` 446）。
- 子代理的输入框**不在 `SubagentDrawer.vue` 里**：抽屉只是一个标签外壳（头栏有一个 `Plus` 图标按钮，
  `SubagentDrawer.vue:47-58`，`data-testid="subagent-drawer-add"`），每个标签的正文与输入框由
  `frontend/src/components/chat/SubagentTab.vue` 渲染——`.subagent-composer` 在 `:40`，
  `PromptInputFooter` 在 `:78`，`ComposerPlusMenu` 在 `:79`，右侧是运行 pill / stop / submit。
  （`sa-comp` / `sa-plus` 这两个类名在 `frontend/src` 里不存在。）

---

## Steps

每一步都要跑自己的验证命令；全部命令在仓库根执行（除非写明 `cd frontend`）。

### Step 1 — 工具卡：头永远一行，放不下的调用进正文

1. `ai-elements-vue/packages/elements/src/tool/ToolHeader.vue`：加一个可选 prop

   ```ts
   singleLine?: boolean   // 默认 false，其它调用方行为不变
   ```

   **注意这个组件已经有一个 `title?: string` prop（`:11`）**，它的值是标题文本（`:41` 的 span 读
   `props.title ?? derivedName`）。本步要加的是**另一个东西**：在那个 span（或它所在的头行）上加一个
   原生 `title` **DOM 属性**，值等于全文，用来做悬停 tooltip。两者名字相同、含义不同——
   不要去改既有 prop 的类型，也不要让它和 DOM 属性互相覆盖。
   标题 span 在 `singleLine` 时用 `truncate whitespace-nowrap`（去掉 `break-words`）。
2. `frontend/src/components/chat/ToolCallCard.vue`：
   - 传 `single-line`；
   - 正文开头加一块「放不下的调用」：当 `step.summary` 长度 > 120 或含 `\n` 时，
     渲染 `<pre class="cmd-full ...">{{ step.summary }}</pre>`
     （等宽 12px、`whitespace-pre-wrap break-words`、`max-h-64 overflow-auto`）；
   - 这段话**只在超长时出现**，短摘要不重复（别让每张卡都多一行）。
3. 测试 `ToolCallCard.test.ts`：加三条——短摘要不出现 `.cmd-full`；长摘要出现且与 `summary` 逐字相同；
   头上有 `title` 属性且等于 `summary`。

验证：

```bash
cd frontend && corepack pnpm exec vue-tsc --noEmit
cd frontend && corepack pnpm test src/components/chat/ToolCallCard.test.ts
cd ai-elements-vue/packages/elements && corepack pnpm test
```

（`@repo/elements` 有 `"test": "vitest run"` 脚本，直接跑即可；若那一步报「没有测试文件」，
不要为此新建测试框架——把断言放在 `ToolCallCard.test.ts` 里通过 props 观测。）

### Step 2 — 待办卡说中文「更新待办」

1. `frontend/src/locales/index.ts`：把 **zh** 的 `'chat.updatedPlan'`（`:1533`）改成 `更新待办`；
   en（`:3190`）保持 `Updated Plan`。
2. `frontend/src/composables/useChatStream.ts:1598-1604`：引擎下发的 `title` 若是已知的规范名，
   就用本地化名字显示。实现方式：一张小映射

   ```ts
   const ENGINE_TITLES: Record<string, I18nKey> = { 'Updated Plan': 'chat.updatedPlan' }
   ```

   命中就用 `translate(key)`，否则原样显示（引擎将来换标题也不会显示成空白）。
3. `frontend/src/components/chat/PlanUpdateCard.vue:7` 的回退文案自动跟着 `chat.updatedPlan` 走，不用改。
4. 测试：`useChatStream.ts` 的既有用例里已有 `title: 'Updated Plan'` 那一类数据，加一条断言 zh 下
   渲染为 `更新待办`。（`PlanUpdateCard.test.ts` **不存在**——不要假设它存在；要覆盖这个组件就在
   `useChatStream.test.ts` 或 `ChatView.test.ts` 里通过渲染结果断言。）

**不要**改 `pkg/tool/session_tools.go` 的 `Title`（TUI 还印它，owner 说 TUI 保持现状）。

验证：

```bash
cd frontend && corepack pnpm test src/composables/useChatStream.test.ts
```

### Step 3 — TUI 与 web 的提问卡头对齐：`Asked user` 之后什么都不说

1. `pkg/tool/format.go:2102-2107`：`userInteractionLabel(verb, evt)` 只返回 `verb`（`return verb`
   替代 `:2104` 的 `return verb + " · " + questions`）。
   `UserInteractionQuestionsLabel`（函数体 `:2113-2139`，文档注释 `:2109-2112`）随之没有调用方 →
   **删掉它**，连同 `pkg/tool/format_test.go:1248-1254`
   （`TestUserInteractionCardNamesItsQuestions`）那条断言它的用例
   （仓库规则：删掉的语义就当做从来没有存在过）。
2. `pkg/tui/render.go:2793-2803`：`user_interaction` 分支不再设置 `target`（`:2803` 那行删掉）
   （保留 `Asking user` / `Asked user` / `Failed to …` 三种 action）。
3. `pkg/tool/format.go:2619`（`"ask user"` 那一处）跟着变成只有动词，不要另拼问题名。
4. TUI 测试：`pkg/tui/chat_session_test.go` 新增一条（照同文件里 `TestApprovalOverlay…` 的形状，
   那一族在 `:3265` 起）。同文件已有 `TestRendererUserInteractionShowsFriendlyAnswers`（`:20385`）
   钉住正文的问答形状——先读它，能扩展就扩展，别复制一份近似的：
   喂一个已完成的 `user_interaction` 步骤 + 结果文本，断言：
   - 卡头恰为 `Asked user`（可带时长后缀，但**不含**任何问题 header）；
   - 正文按 `问题原文` → `→ 回答原文` 两行出现，且不含裸 header 行。

验证：

```bash
gofmt -l pkg/tool pkg/tui
go vet -tags fts5 ./pkg/tool ./pkg/tui
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -count=1
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'UserInteraction|AskedUser' -count=1
```

### Step 4 — web 提问卡改标签页（新组件 `AskTabs.vue`）

新建 `frontend/src/components/chat/AskTabs.vue`，形状照终端：

- 顶部标签栏：`← [☐ 问题 header] … [✔ 提交] →`
  - chip 文本 = `question.header || question.prompt`（header 缺就退回问题原文，≤12 字时就是它自己）；
  - 未答 ☐ / 已答 ✔ / 当前一枚反显（`aria-selected`）；
  - 只有**多选**题才画「提交」chip 与左右箭头；单选一题时不画箭头。
- 正文一次只画一题：
  - 加粗 prompt；
  - 编号选项 `1.` `2.` …，选中即高亮整行；选项 `description` 缩进第二行；
    `recommended` 显示为标签后缀「（推荐）」（**新增** `chat.askRecommended`，zh `（推荐）` / en `(recommended)`）；
    `preview` 在选中项下方展开（等宽、限高、可滚动）。
  - 最后一档「其他」：输入框沿用**已有**的 `chat.otherOptional`（zh `其他（可选）`，`locales/index.ts:1409`），不要新造键；选中后该行变成输入框。
  - 多选：底部 `下一题` / `提交` 按钮行。
- 提交：沿用 `usePendingActions.submitAsk`（`:271-285`），**协议一个字不改**。
  多选复核：提交前画一屏「你选了 …」（照 `pkg/tui/overlays.go:2977` 的 `renderSubmitView`），
  确认后调 `submitAsk`。
- 已答完留在会话里的卡（`ApprovalCard` 的 decided 分支）不变：它已经由引擎的
  `FormatUserInteractionResult` 给出「问题原文 → 回答原文」。
- 工作台面板（`PendingActionsPanel.vue:166-203`）换成同一个 `AskTabs`。

测试：`frontend/src/components/chat/AskTabs.test.ts`：单选一组、多选一组、带 preview 一组、
「其他」输入、标签栏 ☐/✔ 切换、提交载荷 `{questionId, optionIds, otherText}` 与现在逐字一致。

验证：

```bash
cd frontend && corepack pnpm exec vue-tsc --noEmit
cd frontend && corepack pnpm test src/components/chat/AskTabs.test.ts src/components/chat/ApprovalCard.test.ts
```

### Step 5 — 视图模式（默认 / 简洁）

1. 新建 `frontend/src/composables/useViewMode.ts`：

   ```ts
   export type ViewMode = 'default' | 'concise'
   // localStorage 键：主对话 forebrain.viewMode.<sessionId>
   //                  子代理 forebrain.viewMode.<sessionId>.<agentId>
   // 缺省 'default'；读写都吞异常（隐私模式下 localStorage 会抛）
   ```

   （对照 `ChatWorkbench.vue:160-172` 的同款写法，别引第三方 store。）
2. 新建 `frontend/src/components/chat/ViewModeChip.vue`：胶囊按钮，展开两项
   `默认` / `简洁`（`chat.viewModeDefault` / `chat.viewModeConcise`），带一行说明
   `chat.viewModeDefaultHint` = `工具调用、思考、子代理调用都画出来`、
   `chat.viewModeConciseHint` = `隐藏工具调用卡与思考卡`；与 `ModeChip` 同一视觉。
3. 接线：
   - 主对话：`views/ChatView.vue:443-446` 的左侧组里，`ApprovalPresetPicker`（`:446`）之后加
     `<ViewModeChip v-model="viewMode" />`；
   - 子代理：`components/chat/SubagentTab.vue:78-79` 的 `PromptInputFooter` 里，
     `ComposerPlusMenu`（`:79`）之后加同一枚（每个标签各一份，状态按 `sessionId + agentId` 分开）。
4. 过滤：新建 `frontend/src/lib/viewMode.ts`

   ```ts
   export function blockVisibleIn(mode: ViewMode, kind: TimelineBlock['kind']): boolean
   // concise: 'assistant' 与 'approval' 可见；'thinking' / 'tool' / 'subagent' 不可见
   //          （approval 必须保留：它是等你决定的东西，藏起来等于把闸门藏起来）
   ```

   两个渲染入口都用它：`ChatView.vue` 的 `v-for` 里加 `v-if`（或先 `computed` 过滤一份），
   `SubagentConversation.vue` 同理。
5. 空消息：简洁模式下一条消息可能只剩空气泡（block 全被隐藏）→ 复用 `hasVisibleBlocks`
   的语义（`useChatStream.ts:1059-1064`）把整条消息隐掉。
6. 测试：`useViewMode.test.ts`（键名、缺省、异常吞掉）、`viewMode.test.ts`（kind 表）、
   `ChatView` 的相关用例（简洁下工具卡不渲染、助手文字仍在、审批卡仍在）。

验证：

```bash
cd frontend && corepack pnpm exec vue-tsc --noEmit
cd frontend && corepack pnpm test src/composables/useViewMode.test.ts src/lib/viewMode.test.ts
```

### Step 6 — e2e

新增 `frontend/e2e/message-cards.spec.ts`（照 `frontend/e2e/exit-plan-approval.spec.ts` 的种数据方式）：

1. 一条超长 shell 调用 → 卡头单行（`getBoundingClientRect().height` 与普通卡头相同）、
   正文里有 `.cmd-full` 且文本等于整条命令。
2. 一条 `session_todo` → 卡片标题是 `更新待办`。
3. 一条待答 `user_interaction`（两题，第二题多选）→ 标签栏两个 ☐ chip + 一个「提交」chip；
   答完第一题后第一个 chip 变 ✔；提交后卡变「问题原文 → 回答原文」。
4. 输入框的视图模式胶囊：切到「简洁」后工具卡消失、助手文字还在；刷新页面后仍是「简洁」。

验证：

```bash
FOREBRAIN_E2E_SPEC=e2e/message-cards.spec.ts scripts/acceptance/web_e2e.sh
```

---

## Done criteria

```bash
gofmt -l pkg/tool pkg/tui                          # 无输出
go vet -tags fts5 ./pkg/tool ./pkg/tui              # 无输出
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/tui -count=1   # ok
cd frontend && corepack pnpm exec vue-tsc --noEmit  # 退出 0
cd frontend && corepack pnpm test                   # 全绿
FOREBRAIN_E2E_SPEC=e2e/message-cards.spec.ts scripts/acceptance/web_e2e.sh   # web e2e: PASS
```

另外两条人工核对：

- `grep -rn "UserInteractionQuestionsLabel" pkg/` 无命中（已删）。
- 真机 dump（`driver.sh` 或 `run-forebrain` 技能）里 `user_interaction` 卡头恰为 `Asked user …`，
  正文是「问题原文 / → 回答原文」；这条要贴证据（截图或 tmux 抓屏），不能只贴单测。

---

## 不在本计划范围

- 审批卡本身的分族与文案（见 083）。
- 工作台与抽屉的改造（见 085）：本计划的视图模式胶囊只加在**两个输入框**里，
  不动工作台标签与抽屉结构。
- 思考卡的折叠（已实现并验收过，`ChatView.vue:189-197` 一带），不要顺手重做。
- 终端 TUI 的其它任何卡片（owner：保持现状）。

---

## 维护提示

- 简洁模式是**阅读方式**：新增一种 block kind 时必须同时决定它在简洁模式下是否可见
  （`lib/viewMode.ts` 的表），否则会默认漏出来。
- 工具卡头「永远一行」依赖 `ToolHeader` 的 `singleLine`。以后有人给 `ToolHeader` 加新用法，
  默认值 `false` 保证既有调用方不受影响——别把默认值改成 `true`。
- `user_interaction` 的头现在只有动词；想再显示「问了什么」，请加在**正文**，不要加回头上
  （owner 2026-10-20 明确删掉过头上的文案）。

## 逃生口

1. `@repo/elements` 没有测试入口 → 不改测试框架，在报告里写明，并把该组件的断言放到
   `frontend` 侧的 `ToolCallCard.test.ts`（通过 props 观测）。
2. `SubagentTab.vue` 的底栏结构与本文写的不符（本文：`:40` `.subagent-composer`、`:78-79` 的
   `PromptInputFooter` + `ComposerPlusMenu`）→ 停并报告，不要在抽屉外壳（`SubagentDrawer.vue`）
   上凭猜塞一个胶囊。
3. 简洁模式下出现「整轮只有审批卡」的情况而 `hasVisibleBlocks` 把它判为不可见 → 停，
   这条属于语义 bug（审批卡是闸门，必须可见），按 083 的 `approval` block 可见规则修，
   不要用「简洁模式也藏审批」绕过去。
4. TUI 测试里 `Asked user` 头与会话回放冲突（历史卡走另一条渲染路径）→ 停并报告，
   历史卡要与 live 一致，不能只改 live。
