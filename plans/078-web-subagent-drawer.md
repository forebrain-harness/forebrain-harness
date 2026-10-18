# Plan 078：web 子代理会话改到右侧抽屉（含主对话与子代理的运行状态行）——每个子代理一个标签（实时与回放），标签自带输入框（＋ 附件与技能、/ 命令、@ 引用文件、粘贴拖入附件）；去掉对话顶部的子代理切换条

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不建分支、不建 worktree、不 commit、不 push、不 stash；改动留在当前工作区。
> 先跑 `scripts/fetch-libtokenizers.sh`；Go 测试要 pdfium：
> ```
> P=$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)
> export PKG_CONFIG_PATH=$P CGO_CFLAGS="-I$P/include" CGO_LDFLAGS="-L$P/lib -lpdfium"
> ```
> 本计划只改前端，但 e2e 要起真 gateway。
>
> **前置**：plan 077 已执行完（尤其 Step 8：花名册行带 `parent_session_id`，`useAgentRoster.ts` 有 `agentRosterConversationTarget`）。行号按 2026-10-18 工作区（HEAD `7e11f9a` + 未提交改动），**一律用给出的代码片段 grep 定位**。
>
> **Drift check（先跑）**：下面每条都必须**各有输出**，否则 STOP：
> ```
> grep -n "export function agentRosterConversationTarget" frontend/src/composables/useAgentRoster.ts
> grep -n "parentSessionId" frontend/src/lib/api.ts
> grep -n "import AgentViewTabs from '@/components/chat/AgentViewTabs.vue'" frontend/src/views/ChatView.vue
> grep -n "const activeAgentView = ref('')" frontend/src/views/ChatView.vue
> grep -n "async function handleSubagentSubmit(message" frontend/src/views/ChatView.vue
> grep -n "function handleAgentViewKeydown" frontend/src/views/ChatView.vue
> grep -n "(e: 'back'): void" frontend/src/components/chat/SubagentConversation.vue
> grep -n "subagentCard.open" frontend/src/components/chat/SubagentCallCard.vue
> grep -n "sideConversation: props.sideConversation === true," frontend/src/components/ForebrainPromptTextarea.vue
> grep -n "chat.approvalChip" frontend/src/components/chat/ApprovalPresetPicker.vue
> grep -n "<Cpu class=\"size-3.5\" aria-hidden=\"true\" />" frontend/src/components/chat/ModelChip.vue
> ```
> 前两条没有输出 = 077 还没执行，先执行 077。

## Status

- **Priority**: P1
- **Effort**: L
- **Risk**: MED（重排对话页的子代理视图与输入框；会话浏览状态的本地存储结构变化）
- **Depends on**: plans/077（Step 8）
- **Category**: direction（设计稿落地）
- **Planned at**: commit `7e11f9a` + 工作区未提交改动，2026-10-18

## Why this matters

owner 2026-10-18 新需求：子代理的会话，无论运行中（实时）还是已结束（回放），都在**右侧抽屉的标签页**里看（参照 Codex 桌面版右侧面板），主对话始终留在左边。

现状：
- 对话顶部有一排“主对话 / 子代理”标签（`AgentViewTabs`）；
- 点子代理会**替换整个消息区**；
- 唯一的输入框跟着当前视图切换发送对象。

owner 同意的设计（原型 v60）：
1. **一个子代理一个标签**，可关闭；“＋”列出本对话的全部子代理；没看过的新结果在标签上带圆点。
2. 标签里依次是：交给它的任务（默认折叠，可展开）、它的思考 / 工具 / 回答；回放时末尾是“已工作 …”。
   - **owner 2026-10-18：运行中的“工作中”与终端一致，放在输入框正上方**（终端子代理视图的临时状态行 `⠋ Working (41s) · ☑1/3 · 当前任务` 就在输入框上方），不在会话记录里；内容是转圈 + “工作中 {时长}” + 待办进度 ☑完成/总数 · 当前任务；结束后这一行消失。
   - 回到底部按钮在这一行之上。
   - **owner 2026-10-18 追加：主对话也要有这一行，与终端一致**（原型 v65，状态按钮“对话 · 运行中（状态行）”）。
     - 位置在主输入框正上方，顺序与终端相同：状态行 → 排队消息预览 → 输入框；
     - 内容：`工作中（1m 12s · Esc 中断）· ☑3/5 · 当前任务 · 首 token 1.2s`；其他写法同终端（等待后台终端、正在重新连接… N/5、目标的“正在推进目标 · 第 N 轮”“正在检查目标是否达成”）；
     - 结束后消失。
     - 现状：`useChatStream` 早就算好了 `runtimeStatus` 与 `formatRuntimeStatusLabel`，但页面上**没有任何地方画出来**。
3. 标签底部有**这个子代理自己的输入框**：
   - 保留现有“给子代理发消息”的能力，以及原来子代理视图支持的 / 命令；
   - **owner 明确要求：必须支持 @ 引用文件**，与主输入框同一个选择器，范围是本对话所在项目；
   - **owner 明确要求：支持粘贴、拖入附件，就必须有“＋”按钮**。输入框最左边是“＋”，菜单里有“附件”（与主输入框同一个“保存到”弹窗）和技能列表，底部是 @ 与 / 的提示；“目标”属于整个对话，不放进这里；
   - 运行中有“停止这个子代理”按钮。
   - 输入框与主输入框同一个结构：上面输入，下面一条底栏。底栏左边“＋”；右边依次是**“模型与上下文”胶囊**、停止、发送。
3b. **owner 2026-10-18 要求重新设计：把子代理的模型名称、思考强度、上下文窗口预算与百分比优雅地展示出来**（原型 v62–v66）。
   - 标签顶部不再有“正在使用 … · 上下文 …”那一行；这些信息放进输入框底栏的胶囊，与主对话把模型、思考强度、上下文圆环放在输入框底栏的位置一致。
   - **胶囊**：上下文圆环（16px）+ 百分比（**一直写出**，≥80% 变警告色）+ 细分隔线 + 模型名（等宽）+ “· 思考强度”。
   - **点胶囊弹出卡片**（向上展开，右对齐，宽 300px），内容依次是：
     - 34px 大圆环 + 大号百分比 + “上下文已用 {已用} / {窗口}”；
     - 进度条；
     - ≥80% 时一行警告“快满了，可在下面输入 /compact 压缩它”；
     - 键值表：模型（服务商/模型）、思考强度、上下文窗口、本次用量（输入 · 输出）；
     - 底部灰字“模型与思考强度在派发时确定”。**卡片里没有“上下文明细”按钮**（owner 2026-10-18 删掉）；在子代理输入框里输入 `/context` 仍按现有方式在标签顶部显示上下文明细。
   - 模型与思考强度是只读的（派发时就定了），所以胶囊不带下拉箭头。
4. **抽屉与工作台共用右侧，一次只开一个**：打开子代理会收起工作台，打开工作台会收起抽屉，已打开的标签保留。
5. **去掉对话顶部的子代理切换条**：主输入框永远发给主代理。
6. 顶栏新增“子代理 N”按钮：对话派过子代理才出现，N 是运行中的个数，点它开关抽屉。工作台按钮**不再带数字**。
7. 抽屉宽 440px。抽屉打开时主输入框变紧凑：模型按钮去掉前置图标。
7b. **owner 2026-10-18：审批按钮一律去掉“审批：”前缀**（不只是抽屉打开时），只写档位（默认 / 只读 / 完全访问），悬停提示沿用现有的 `approval.pickerTitle`（本会话的审批模式）。原型 v66。
8. 对话里的子代理卡片沿用现有组件（一句概要 + 每个任务一行：任务名 · 用时、“最近：…”、查看）。**在抽屉里打开的那一行高亮**。
9. 工作台概览里的运行中代理直接打开这个子代理的标签（跨对话时打开那条对话再打开标签）。菜单栏“子代理”页改由后续计划重做成配置页，本计划不动它。

## 设计稿（验收依据，实施须 1:1 还原）

- 原型 https://claude.ai/artifact/KKfPW8dXWScuRdTzonsk2F（本地 `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html`，v66）。
- 状态按钮：**子代理 · 实时（右侧抽屉）**、**子代理 · 回放**、**子代理 · 打开其他子代理**、**子代理 · @ 引用文件**、**子代理 · 加号菜单**、**子代理 · 模型与上下文**、**子代理 · 上下文快满**、**工作台 · 概览**（运行中代理可点）、**子代理**（菜单栏页）。
- 原型代码（在文件里搜函数名）：
  - `saCallCard` / `saRow`：对话里的卡片，打开的行 `aria-current="true"`；
  - `saDrawer`：抽屉外壳，含标签、`＋`、收起；
  - `saBody`：标签内容，含任务卡、工具行；`saDrawer` 里 `.sa-run`：子代理输入框正上方的“工作中”状态行；`chat()` 里 `.run-line`：主输入框正上方的状态行（状态按钮“对话 · 运行中（状态行）”）；
  - `saInfoPill` / `saInfoCard` / `saRing`：底栏的“模型与上下文”胶囊与它的卡片；`.sa-comp .bar` 是两行输入框的底栏；
  - `saMenu`：“本对话的子代理 · N”；
  - 输入框里的 `.sa-at`：@ 候选；`.sa-plus` 与 `.sa-comp .plus-pop`：输入框左边的“＋”和它的菜单（与输入框同宽，向上展开）；
  - `topbar()` 里的“子代理”按钮；`benchOverview` 的 `btn-row`。
- 原型 CSS：`.sad*`、`.sa-*`、`.sa-info*`、`.sa-infocard*`（尺寸、间距照抄，颜色换成下面的 token）、`.chat.narrow .composer`（抽屉打开时主输入框的紧凑规则）。
- 颜色 token 对照：

  | 原型 | 实现 |
  |---|---|
  | `--line` | `--forebrain-divider` |
  | `--brand` | `--forebrain-brand-1` |
  | `--brand-soft` | `--forebrain-brand-soft` |
  | `--soft` | `--forebrain-button-alt-bg` |
  | `--subtle` | `--forebrain-bg-alt` |
  | `--muted` | `--forebrain-muted-text` |
  | `--text-2` | `--forebrain-text-2` |
  | `--success` | `--forebrain-success` |
  | `--danger` | `--forebrain-danger` |
  | `--warning` / `--warning-soft` | `--forebrain-warning` / `--forebrain-warning-soft` |
  | `--line-2` | `--forebrain-divider-strong` |
  | `--soft-2`（进度条底） | `--forebrain-divider` |
  | `--page` | `--forebrain-surface` |

  深浅色都靠 token，**不写死颜色**。

## Current state（2026-10-18 工作区，已核实）

**`frontend/src/views/ChatView.vue`（约 2300 行）**

模板：
- 顶部 `<AgentViewTabs :records="agentTabRecords" :active="activeAgentView" :seen="seenAgentActivity" @select="openAgentView" />`。
- 消息区 `<Conversation ref="conversationRef">` 里有 `<Transition name="forebrain-agent-view"><div v-if="activeSubagent">…<ContextDebugPanel v-if="subagentContextTarget === activeSubagent.agentId" …/><SubagentConversation :record="activeSubagent" … @back="openAgentView('')" @open-agent="openAgentView" @fold-change="updateAgentFold(…)"/></div></Transition>`，紧接着 `<div v-show="!activeSubagent" key="primary-view">` 包住主对话。
- 主对话里打开子代理的地方：
  - 两处 `<SubagentCallCard … @open="openAgentView" />`（`block.kind === 'tool'` 与 `'subagent'`）；
  - `<GoalLine … @open-check="openAgentView" />`；
  - `<ApprovalCard … can-open-agent @open-agent="openAgentView" />`。
- 唯一的输入框：
  - `<PromptInputProvider … @submit="handleSubmit">`；
  - `<AutoContinueBanner :state="activeAutoContinue" @cancel="cancelAutoContinue(activeAgentView)" />`；
  - `<PendingInputQueuePopover :preview="composerPendingInput" …>`；
  - `<ForebrainPromptTextarea ref="promptRef" :placeholder="inputPlaceholder" … :scope="workbenchScope" attach-save @files-picked="onFilesPicked" … @typed="cancelAutoContinue(activeAgentView)" @escape="cancelAutoContinue(activeAgentView)" />`；
  - 底栏 `<ApprovalPresetPicker :session-id="sessionId" />`、`<ModelChip … />`。
- `</main>` 之后是 `<ChatWorkbench v-if="workbench.open.value && !isFreshChat" … :overlay="workbenchOverlay" @view="viewRoster" …/>`。

脚本：
- 视图状态：
  - `const activeAgentView = ref('')`、`seenAgentActivity`；
  - 本地存储 `forebrain:chat-browse:v1:<session>`，形状 `{activeAgentView, seenAgentActivity, views}`（`readSessionBrowseState` / `persistSessionBrowseState`）；
  - `viewBrowseStates` 按视图存 `scrollTop/follow/folds`，`updateAgentFold`。
- 一个输入框两个主人：
  - `activeAutoContinue`、`composerPendingInput`、`composerQueueVisible`、`inputPlaceholder` 都按 `activeAgentView` 二选一；
  - `viewDrafts` + `takeComposerDraft` / `applyComposerDraft` 在切换视图时互换草稿；
  - `restoreLastQueuedMessage`、`interruptAndSendPendingSteers` 也按视图分流。
- `handleSubmit` 开头 `if (activeAgentView.value) { await handleSubagentSubmit(message); return }`。`handleSubagentSubmit(message)` 处理以下情况：
  - `!` 拒绝；
  - `/compact` → `compactActiveSubagent()`；
  - `/context` → `openSubagentContext(agentId)`；
  - 属于对话的命令 → `send(text, …)`；
  - 不认识的命令 → 一句话拒绝；
  - 技能命令或普通文本 → `sendToSubagent(agentId, submission, queued)`。
  - 拒绝时写 `composerNotice`。
- `openAgentView(agentId)` 依次做：互换草稿、记录滚动、`markAgentSeen`、`loadSubagentBudget(agentId)`、`refreshSubagentSlashCommands()`（`forebrainApi.slashCommands('webchat','',{subagentView:true})`）、`applySubagentReturnedDraft(agentId)`。
- Esc：`handleAgentViewKeydown` 挂在 `window` 上，只在 `activeAgentView` 非空时调用 `handleAgentViewEscape()`，顺序是：
  1. `withdrawSubagentInput` 拿回刚发的消息；
  2. 否则 `interruptSubagentToSend`；
  3. 否则 `openAgentView('')` 回到主对话。
- `agentTabRecords` 是 `subagents` 去掉 `FOREBRAIN_GOAL_CHECK_AGENT_TYPE` 的结果（正在看的那条除外）。
- `isFreshChat` 的条件里有 `!activeAgentView.value`。
- 顶栏 `setTopbar({ trailing })` 里，工作台按钮带 `runningRoster.value.length` 的数字（`forebrain-chip-count`）。
- `viewRoster(row)` 调 `router.replace(agentRosterViewTarget(row))`，跳到子代理自己的 worker 会话。
- 附件：
  - `onFilesPicked(files)` → `AttachSaveDialog`；
  - `confirmAttach(dir)` 上传后 `promptRef.value?.addUploaded(uploaded)`；
  - `waitForPendingAttachUpload()`。
- 文件末尾 `<style>` 里有 `forebrain-agent-view` 的过渡样式（搜 `/* Switching between the conversation and a subagent's view`）。

**`useChatStream`（不改）提供的子代理接口**（`frontend/src/composables/useChatStream.ts`）
- 状态：`subagents`（`SubagentTranscript[]`：`agentId, agentType, title, task, status, blocks, tokenBudget, startedAt, model*, plan, updatedSeq…`）、`subagentCallTasks`、`subagentNow`、`subagentPendingInput: Ref<Record<agentId, PendingInputPreview>>`、`subagentReturnedDraft`。
- 读写函数：
  - `takeSubagentReturnedDraft(agentId)`；
  - `sendToSubagent(agentId, submission, followUp)`；
  - `recallSubagentInput(agentId)`；
  - `interruptSubagentToSend(agentId): Promise<boolean>`；
  - `withdrawSubagentInput(agentId): Promise<ComposerSubmission[]>`；
  - `compactSubagent(agentId): Promise<'ok'|'running'|'error'>`；
  - `loadSubagentContext(agentId)`、`loadSubagentBudget(agentId)`。
- 自动续跑：`autoContinueForView(view)`、`cancelAutoContinue(agentId = '')`。
- 停止单个子代理：`forebrainApi.subagentCancel(id)`（`lib/api.ts`）。

**组件**
- `components/chat/SubagentConversation.vue`：
  - 头部有返回按钮（`emit('back')`）、标题、状态、`agentId`、模型、`data-testid="subagent-budget"` 的上下文占用、token 数；
  - `prompt` 块画成带 Bot 头像的用户气泡；
  - 运行中时，“正在工作（时长）”在**最底部**；
  - 嵌套派出的子代理用 `SubagentCallCard` 画，`@open` → `emit('open-agent')`。
- `components/chat/SubagentCallCard.vue`：
  - 卡片头一句概要，每个任务一行（`data-testid="subagent-task-row"`）；
  - 行尾是文字“查看”（`subagentCard.open`），点击 `emit('open', agentId)`。
- `components/chat/AgentViewTabs.vue` + `.test.ts`：要删除。
- `components/ForebrainPromptTextarea.vue`：
  - 用 `usePromptInput()`，所以每个输入框要有自己的 `PromptInputProvider`；
  - @ 选择器按 `scope` prop 取候选；
  - / 菜单在 `watch([slashOpen, …])` 里调 `forebrainApi.slashCommands('webchat', query, { duringRun, sideConversation })`，**没有传 `subagentView`**，所以子代理视图的 / 菜单列的是主对话的命令；
  - Esc：菜单打开时 `preventDefault` 并关菜单；`duringRun` 时 `preventDefault` 并 `emit('interrupt-run')`；其余 `emit('escape')`（不 `preventDefault`）。
- `components/chat/ComposerPlusMenu.vue`（主输入框的“＋”）：
  - 菜单项依次是“附件”（`usePromptInput().openFileDialog()`）、“目标”（`emit('insert', '/goal ')`）、技能列表（`forebrainApi.slashCommands('webchat', '', {})` 里 `category === 'skill'` 的项，点击 `emit('insert', '/<name> ')`），底部是 @ / 提示；
  - 根元素是 `<div class="relative" ref="rootRef">`，弹层 `.forebrain-plus-pop` 是 `left: 0; right: 0; bottom: calc(100% + 6px)`；
  - ChatView 用 `@insert="insertPlusCommand"`，即 `promptRef.value?.restoreSubmission({ text, attachments: [], mentionImages: [] })`。
- `components/chat/ApprovalPresetPicker.vue`：按钮文字 `t('chat.approvalChip', { preset: currentLabel })`（“审批：{preset}”）。
- `components/chat/ModelChip.vue`：按钮里 `<Cpu class="size-3.5" aria-hidden="true" />` + 名称。
- `components/chat/ChatWorkbench.vue`：外壳写法、宽度与窄屏覆盖层（`chat-workbench--overlay`，`@media (max-width: 1023px)`）是新抽屉的**样式范例**。
- `views/SubagentsView.vue`（077 执行后）：
  - “所在会话”按钮 → `jumpToConversation(row)`（`agentRosterConversationTarget`）；
  - 操作栏有 `打开会话`（worker 会话）+ `停止`。
- 输入框分组：`@repo/shadcn-vue/components/ui/input-group` 的 `InputGroup` 只有在含 `data-align=block-start/block-end` 的 addon 时才变成竖排；用 `InputGroupAddon align="inline-end"` 则保持**一行**。

**现有测试（会受影响）**
- `src/views/ChatView.test.ts` 的 `describe('ChatView subagent view')`：
  - 5 个用例，点 `[data-testid="agent-tab-…"]` / `.forebrain-agent-tab`；
  - Esc 用 `window.dispatchEvent`；
  - 断言 `subagent-conversation-stub`。
- `src/components/chat/SubagentConversation.test.ts`、`src/components/chat/AgentViewTabs.test.ts`。
- `e2e/subagent-conversation.spec.ts`：
  - 3 个用例点 `.forebrain-agent-tab`，在主输入框里发给子代理；
  - 用 `[data-testid="subagent-budget"]`、`[data-testid="subagent-user-message"]`；
  - Esc 回主对话。

## Commands you will need

| 用途 | 命令 | 成功预期 |
|------|------|----------|
| 前端单测（单文件） | `cd frontend && pnpm test -- <文件>` | 通过 |
| 前端单测（全部） | `cd frontend && pnpm test` | 全绿 |
| 类型检查 | `cd frontend && npx vue-tsc --noEmit -p tsconfig.json` | exit 0 |
| e2e 单文件 | `FOREBRAIN_E2E_SPEC=e2e/<文件> scripts/acceptance/web_e2e.sh` | 末行 `web e2e: PASS` |
| e2e 全套 | `scripts/acceptance/web_e2e.sh` | 末行 `web e2e: PASS` |

开工前 `git status --short > $TMPDIR/078-before.txt`。

## Scope

**In scope**
- 新建：
  - `frontend/src/components/chat/SubagentDrawer.vue` + `.test.ts`；
  - `frontend/src/components/chat/SubagentTab.vue` + `.test.ts`；
  - `frontend/src/lib/subagentStatus.ts` + `.test.ts`；
  - `frontend/src/components/chat/SubagentRunPill.vue` + `.test.ts`；
  - `frontend/src/components/chat/RunStatusLine.vue` + `.test.ts`（主对话与子代理标签共用的状态行）；
  - `frontend/src/lib/effortLabel.ts` + `.test.ts`；
  - `frontend/e2e/subagent-drawer.spec.ts`。
- 修改：
  - `frontend/src/views/ChatView.vue` + `ChatView.test.ts`；
  - `frontend/src/components/chat/SubagentConversation.vue` + `.test.ts`；
  - `frontend/src/components/chat/SubagentCallCard.vue`（+ 测试，若没有就新建 `SubagentCallCard.test.ts`）；
  - `frontend/src/components/ForebrainPromptTextarea.vue` + `.test.ts`；
  - `frontend/src/components/chat/ApprovalPresetPicker.vue`、`frontend/src/components/chat/ModelChip.vue`、`frontend/src/components/chat/ComposerPlusMenu.vue`、`frontend/src/components/chat/EffortChip.vue`（只改为调用 `effortLevelLabel`）及其测试；
  - `frontend/src/composables/useAgentRoster.ts` + `.test.ts`（只改 077 加的 `agentRosterConversationTarget`）；
  - `frontend/src/locales/index.ts`；
  - `frontend/e2e/subagent-conversation.spec.ts`。
- 删除：`frontend/src/components/chat/AgentViewTabs.vue`、`frontend/src/components/chat/AgentViewTabs.test.ts`。

**Out of scope**
- `useChatStream.ts`（子代理的流、发送、撤回、压缩、预算都不改）、gateway 与 Go 代码、`ai-elements-vue/`（共享组件库，不改）。
- 技能工坊页（`WorkshopView.vue` 不画子代理卡片，不受影响）。
- 主对话的消息样式（头像等）。
- 工作台本身的内容。

## Steps

### Step 1：输入框与两个按钮的小改动

1. `ForebrainPromptTextarea.vue`：
   - 加 prop `subagentView?: boolean`，注释写“这个输入框属于子代理的标签：/ 菜单列出子代理视图可用的命令”；
   - 在 `forebrainApi.slashCommands('webchat', String(query), { … })` 的参数里加 `subagentView: props.subagentView === true`；
   - `watch` 的依赖数组同时加上 `() => props.subagentView`。
2. `ApprovalPresetPicker.vue`：按钮文字改成只写档位 `{{ currentLabel }}`，**任何时候都不带“审批：”前缀**，也**不加** `compact` prop。按钮上已有的 `v-tip:top="t('approval.pickerTitle')"` 保留，`aria-label` 若没有就补成同一句。`chat.approvalChip` 不再使用，Step 9 删除。
3. `ModelChip.vue`：加 prop `compact?: boolean`，`compact` 时不画 `<Cpu …/>`。
3a. `ComposerPlusMenu.vue`：
   - 加 prop `subagentView?: boolean`，注释写“这是子代理标签的‘＋’：目标属于整个对话，不出现；技能按子代理视图的分类取”；
   - `subagentView` 时不画“目标”那一项（`data-testid="plus-goal"`）；
   - `loadSkills` 调 `forebrainApi.slashCommands('webchat', '', props.subagentView ? { subagentView: true } : {})`。
4. 测试：
   - `ForebrainPromptTextarea.test.ts`：照文件里已有的 slash 用例，`subagentView: true` 时 `forebrainApi.slashCommands` 收到的第三个参数含 `subagentView: true`。
   - `ApprovalPresetPicker`：按钮文字就是档位名（如英文界面 `Default`），不含 `Approvals:`；`ModelChip`：`compact` 时 `[data-testid="model-chip"] svg` 少一个。测试文件存在就加用例，不存在就新建；挂载写法照 `ModelChip.test.ts`。
   - `ComposerPlusMenu`：`subagentView` 时打开菜单没有 `plus-goal`，`slashCommands` 收到 `{ subagentView: true }`；不传时“目标”仍在（原有行为不变）。

**Verify**：`cd frontend && pnpm test -- src/components/ForebrainPromptTextarea.test.ts src/components/chat/ModelChip.test.ts src/components/chat/ComposerPlusMenu` 通过，`ApprovalPresetPicker` 的测试也通过。

### Step 2：`lib/subagentStatus.ts`——状态的统一说法

新建 `frontend/src/lib/subagentStatus.ts`，导出：
```ts
import type { SubagentTranscript } from '@/composables/useChatStream'
export type SubagentPhase = 'active' | 'done' | 'failed'
/** In progress while it runs or waits on the operator; done; or ended without an answer. */
export function subagentPhase(status: SubagentTranscript['status']): SubagentPhase
/** The status in words, for the "+" menu and the conversation's own header. */
export function subagentStatusLabel(status: SubagentTranscript['status']): string
/** "{type} · {title}", the way the terminal's roster names a subagent; the type alone when there is no title. */
export function subagentTabLabel(record: Pick<SubagentTranscript, 'agentType' | 'title'>): string
```
- `subagentPhase` 的取值：
  - `running`、`waiting_approval`、`waiting_input` → `active`；
  - `done` → `done`；
  - `failed`、`cancelled`、`interrupted` → `failed`。
- `subagentStatusLabel` 用 locale 键：
  - 已有：`chat.subagentRunning`（运行中）、`chat.subagentDone`（已完成）、`chat.subagentFailed`（失败）；
  - 新增：`chat.subagentWaitingApproval`（等待批准 / Waiting for approval）、`chat.subagentWaitingInput`（等待输入 / Waiting for input）、`chat.subagentCancelled`（已取消 / Cancelled）、`chat.subagentInterrupted`（已中断 / Interrupted）。
  - 这样就取代了 `SubagentConversation.vue` 里 `props.record.status.replace(/_/g, ' ')` 的原始写法。
- `subagentTabLabel` 逻辑照 `AgentViewTabs.vue` 的 `tabLabel`：`type = entry.agentType || t('agents.subagent')`。
- `subagentStatus.test.ts` 覆盖 7 种 status 与有 / 无 title 两种名字。

另新建 `frontend/src/lib/effortLabel.ts`：
- 把 `EffortChip.vue` 里的 `levelLabel(id)` 搬过来，导出为 `effortLevelLabel(id: string): string`：`low/medium/high/xhigh` → `chat.effortLow/Medium/High/Xhigh`，空串 → `chat.effortDefault`，其它原样返回。
- `EffortChip.vue` 改为调用它，行为不变。
- `effortLabel.test.ts` 覆盖 6 种输入。

**Verify**：`cd frontend && pnpm test -- src/lib/subagentStatus.test.ts src/lib/effortLabel.test.ts src/components/chat/EffortChip` 通过。

### Step 3：`SubagentConversation.vue` 改成抽屉标签的内容

按原型 `saBody` 改：
1. **删掉整个 `<header>`**（返回按钮、标题、状态、`agentId`、token 数），并删 `emit('back')` 与 `ArrowLeft` 导入。
2. **不画模型与上下文**：原头部里的模型、上下文占用、token 数改由输入框底栏的 `SubagentRunPill`（Step 5a）显示。
   - 删掉本组件的 `modelLabel`、`budgetLabel` 两个 computed，以及 `formatTokenCount` 的导入（不再使用的话）。
   - `data-testid="subagent-budget"` 移到胶囊上。
3. **`prompt` 块改成任务卡**，照原型 `.sa-task`：
   - 圆角 10px、`--forebrain-bg-alt` 底、`--forebrain-divider` 边；
   - 顶部小字 `t('chat.subagentTaskCap')`（交给它的任务）；
   - 正文 `whitespace-pre-wrap`；
   - **默认折叠**：`max-height: 76px` + 底部渐隐 mask。卡片底边居中有 24px 圆形按钮，用 `ChevronDown` / `ChevronUp` 图标，`aria-label` 取 `t('chat.subagentTaskExpand' / 'chat.subagentTaskCollapse')`。
   - 展开状态存进 fold 状态（`blockFoldId(block, idx)`，默认 `false`），**沿用** `foldOpen` / `updateFold`，所以会随会话的浏览状态保存。
4. **工作中那一行移出本组件**：删掉底部的“正在工作（时长）”那一块，以及 `nowMs`、`workingTimer`、`workingDuration` 与相关的 `watch` / `onBeforeUnmount`，这些挪到 `SubagentTab`（Step 5）。“尚未给出回答”（`chat.subagentNoAnswer`）的判断不变，仍在底部，条件是“不在运行且没有回答”。
5. 嵌套的 `SubagentCallCard` 加 `:active-agent-id="activeAgentId"`（新 prop，Step 4 定义），`SubagentConversation` 加同名可选 prop 透传。
6. `isRunning` 改用 `subagentPhase(props.record.status) === 'active'`，`statusLabel` / `statusClass` 删除（头部已删）。
7. `SubagentConversation.test.ts`：
   - 删与头部、返回按钮相关的断言；
   - 新增：任务卡默认折叠，点按钮展开后发出 `fold-change`；
   - 新增：运行中时本组件里**没有**“工作中”文字（它在 `SubagentTab` 的输入框上方）；
   - 新增：组件里不再出现 `subagent-budget`。

**Verify**：`cd frontend && pnpm test -- src/components/chat/SubagentConversation.test.ts` 通过；`grep -n "emit('back')\|ArrowLeft" frontend/src/components/chat/SubagentConversation.vue` 无输出。

### Step 4：`SubagentCallCard.vue`——在抽屉里打开的那一行高亮

- 加 prop `activeAgentId?: string`。
- 行的 `component` 上加 `:aria-current="row.agentId && row.agentId === activeAgentId ? 'true' : undefined"`。
- scoped 样式加：
  ```css
  .forebrain-subagent-row[aria-current='true'] { background: var(--forebrain-brand-soft); box-shadow: inset 2px 0 0 var(--forebrain-brand-1); }
  ```
  这是原型 `.sa-row[aria-current="true"]` 的写法。
- 行尾的“查看”文字**保留**（原型 `.sa-open`）。
- 测试：`activeAgentId` 与某行相同时该行 `aria-current="true"`，其它行没有。

**Verify**：`cd frontend && pnpm test -- src/components/chat/SubagentCallCard` 通过。

### Step 5a：`SubagentRunPill.vue`——“模型与上下文”胶囊与卡片

新建 `frontend/src/components/chat/SubagentRunPill.vue`，照原型 `saInfoPill` / `saInfoCard` / `saRing`。

**props**：`record: SubagentTranscript`。没有 emits。

**数据**（全部来自 `record`，不另取接口）
- `percent`：`record.tokenBudget.percentLeft` 是有限数字时为 `Math.round(100 - percentLeft)`，否则 `null`。
- `used = formatTokenCount(record.tokenBudget?.tokenUsage)`、`window = formatTokenCount(record.tokenBudget?.contextWindow)`；`formatTokenCount` 在 `lib/forebrainGatewayRuntime.ts`。
- `model`：`record.model`；`provider`：`record.modelProvider`；`effort = effortLevelLabel(record.reasoningEffort ?? '')`。`reasoningEffort` 为空时**不显示**思考强度（不要写“默认”，它可能只是没上报）。
- `usage = t('chat.subagentRunUsageValue', { input: formatTokenCount(record.inputTokens), output: formatTokenCount(record.outputTokens) })`；两者都是 0 时这一行不画。
- `warn = percent != null && percent >= 80`。

**胶囊**（`<button type="button" class="subagent-run-pill" data-testid="subagent-run-pill" :aria-expanded="open" aria-haspopup="dialog" :aria-label="t('chat.subagentRunPillAria')" v-tip:top="t('chat.subagentRunPillTip')" @click="open = !open">`）
- 有 `percent` 时：16px 圆环 + `<span data-testid="subagent-budget">{{ percent }}%</span>`。
  - 圆环画法照 `ContextRing.vue`：底圈 `--forebrain-divider-strong`，进度圈在 `warn` 时用 `--forebrain-warning`，否则用 `--forebrain-brand-1`。
  - 百分比 11.5px 等宽，`warn` 时 `--forebrain-warning`。
- 有 `percent` 且有 `model` 时，中间一条 1×12px 的分隔线 `--forebrain-divider-strong`。
- 有 `model` 时：`<span class="font-mono">{{ model }}</span>`（最大宽 130px，截断）；有 `effort` 时后接灰字 `· {{ effort }}`。
- `percent` 与 `model` 都没有时，**整个组件不渲染**。
- 胶囊样式：高 28px、圆角 999px、边 `--forebrain-divider`、12px 字、`--forebrain-text-2`；悬停或展开时底色 `--forebrain-bg-alt`。

**卡片**（`open` 时，`role="dialog"`、`data-testid="subagent-run-card"`）
- 位置：`position: absolute; right: 0; bottom: calc(100% + 6px); width: 300px`，相对输入框外层（Step 5 给外层加 `relative`）；圆角 12px、阴影 `--forebrain-shadow-pop`、内边距 14px、项间距 10px。
- 头部：34px 圆环 + 20px 粗体 `{{ percent }}%`（`warn` 时警告色）+ 下一行等宽灰字 `t('chat.subagentCtxUsed', { used, window })`。没有 `percent` 时头部与进度条都不画。
- 进度条：高 6px，底色 `--forebrain-divider`，填充色同圆环。
- `warn` 时一行：`TriangleAlert` 图标 + `t('chat.subagentCtxNearFull')`，`--forebrain-warning` 字、`--forebrain-warning-soft` 底、圆角 6px。
- 键值表 `<dl>`（`grid-template-columns: 76px 1fr`，`dt` 灰字），只画有值的行：
  - `chat.subagentRunModel` → `{provider}/{model}`（没有 provider 只写 model）；
  - `chat.subagentRunEffort` → `effort`；
  - `chat.subagentRunWindow` → `window`，`data-testid="subagent-context-window"`；
  - `chat.subagentRunUsage` → `usage`。
- 底部：顶边线 + 灰字 `t('chat.subagentRunFixed')`。**不放任何按钮或链接**。
- 点卡片外或按 Esc 关闭卡片。写法照 `ApprovalPresetPicker.vue` 关菜单的方式；Esc 时 `preventDefault`，不让抽屉收起。

**`SubagentRunPill.test.ts`**
- `percentLeft: 35, contextWindow: 1_000_000, tokenUsage: 650_000` + `modelProvider: 'zhipuai', model: 'glm-5.3-flash', reasoningEffort: 'high'`：胶囊文字含 `65%`、`glm-5.3-flash`、`High`（英文界面）；打开卡片后含 `650K / 1M`、`zhipuai/glm-5.3-flash`，`subagent-context-window` 是 `1M`。
- `percentLeft: 16`（84%）：百分比带警告色的类，卡片有 `/compact` 提示。
- 没有 `tokenBudget`：只有模型，没有 `subagent-budget`。
- 两者都没有：组件不渲染。
- 卡片里没有按钮或链接（`subagent-run-card` 里 `button, a` 的数量为 0）。

**Verify**：`cd frontend && pnpm test -- src/components/chat/SubagentRunPill.test.ts` 通过。

### Step 5：`SubagentTab.vue`——一个标签：会话 + 自己的输入框

新建 `frontend/src/components/chat/SubagentTab.vue`。

**props**
```ts
record: SubagentTranscript
foldState: Record<string, boolean>
callTasks?: ReadonlyMap<string, ReadonlyMap<number, SubagentCallTaskLive>>
now?: number
scope?: WorkspaceScope
activeAgentId?: string           // 透传给嵌套卡片
pendingInput: PendingInputPreview
autoContinue: AutoContinueState | null   // 类型从 useChatStream 导入（autoContinueForView 的返回类型）
notice: { title: string; detail: string } | null
context: { data: SessionContextDebug | null; loading: boolean } | null  // 这个子代理的 /context 面板，null = 没开
```

**emits**
```ts
submit(message), fold-change({id, open}), open-agent(agentId), stop(),
edit-last-queued(), interrupt-run(), queue-follow-up(), typed(), escape-key(),
files-picked(files: PickedFile[]), cancel-auto-continue(), refresh-context(), dismiss-notice()
```

**defineExpose**：转发内部 `ForebrainPromptTextarea` 的 `takeAttached`、`hasAttached`、`restoreSubmission`、`addUploaded`、`insertWorkspacePath`。

**模板**（根元素 `flex h-full min-h-0 flex-col`）
1. **会话区**：
   - 用 `<Conversation class="min-h-0 flex-1 overflow-y-auto">` + `<ConversationContent class="px-[18px] pt-4 pb-3">`（自动跟随到底部），里面依次是：
     - `context` 非空时的 `<ContextDebugPanel :data="context.data" :loading="context.loading" :error="null" @refresh="emit('refresh-context')" />`；
     - `<SubagentConversation … @fold-change @open-agent />`；
   - 再加 `<ConversationScrollButton />`，即原型的回到底部按钮。
   - 写法照 `ChatView.vue` 主对话的 `<Conversation>` 用法。
1b. **工作中状态行**（会话区与输入区之间，`flex: none`，只在 `active` 时出现），对应终端子代理视图输入框上方的 `Working (41s) · ☑1/3 · 当前任务`：
   - `<RunStatusLine data-testid="subagent-working" :label="t('chat.subagentWorking', { duration })" :plan="record.plan" class="px-[18px] pb-1.5" />`（组件定义在 Step 7b 第 1 点；**做本步前先按那一点建好 `RunStatusLine.vue` 并通过它的测试**）；
   - 时长：从 `SubagentConversation` 搬来的计时代码，按 `record.startedAt` 每秒刷新，用 `formatRuntimeDuration` 格式化（与终端 `formatWorkingElapsed` 同样的 `41s` / `1m 02s` 写法）；不在运行时停掉计时器。
2. **输入区**，`flex: none; margin: 0 12px 12px`：
   - `<PromptInputProvider :max-files="5" accept="image/*,application/pdf" @submit="(m) => emit('submit', m)">`，里面依次是：
     - `notice` 的 `<Alert variant="destructive">`，写法照 ChatView 的 `composerNotice`；
     - `<AutoContinueBanner :state="autoContinue" @cancel="emit('cancel-auto-continue')" />`；
     - `<div class="relative">` 里放 `<PendingInputQueuePopover :preview="pendingInput" @edit-last-queued="emit('edit-last-queued')" @interrupt-run="emit('interrupt-run')" />`。
   - 接着是 `<PromptInput class="subagent-composer">`，结构与主输入框相同（两行：上面输入，下面底栏），写法照 ChatView 主输入框的 `PromptInput` / `PromptInputBody` / `PromptInputFooter`：
     - `<PromptInputBody>` 里放 `<ForebrainPromptTextarea ref="promptRef" :placeholder="t('chat.subagentComposerPlaceholder')" :bots="[]" :during-run="active" :queue-visible="queueVisible" :scope="scope" subagent-view attach-save …>`；
     - 它的事件逐个转发：`@files-picked`、`@queue-follow-up`、`@edit-last-queued`、`@typed`；`@escape` 转成 `emit('cancel-auto-continue')`，与主输入框的语义一致。
     - **输入框的 `@interrupt-run` 转成 `emit('escape-key')`**：这是运行中按 Esc，要走“撤回 → 中断后发送 → 收起”三层。只有 `PendingInputQueuePopover` 的 `@interrupt-run`（“中断并发送”按钮）才发出 `interrupt-run`，它直接中断后发送，**不先撤回**。
     - `<PromptInputFooter>` 左边 `<ComposerPlusMenu subagent-view @insert="insertCommand" />`；右边 `<div class="flex items-center gap-1">` 里依次是：
       - `<SubagentRunPill :record="record" />`；
       - `active` 时一个 28px 的方形按钮：`Square` 图标，`--forebrain-danger` 色，`v-tip:top="t('chat.subagentStop')"`、`:aria-label="t('chat.subagentStop')"`、`data-testid="subagent-stop"`，点击 `emit('stop')`；
       - `<PromptInputSubmit status="ready" />`。
     - `insertCommand(text)` = `promptRef.value?.restoreSubmission({ text, attachments: [], mentionImages: [] })`，与 ChatView 的 `insertPlusCommand` 相同。
     - “附件”走 `openFileDialog()`：文件进入本标签自己的 `PromptInputProvider`，再由输入框（`attach-save`）交给 `files-picked`，于是和粘贴、拖入一样弹出“保存到”。
     - “＋”菜单的弹层与主输入框的“＋”表现一致（与输入框同宽、向上展开，原型 `.sa-comp .plus-pop`）。输入框外层加 `relative`，供胶囊卡片定位。
   - **不加** `global-drop`：拖放只落在这个输入框上，不和主输入框抢。
3. **键盘**：根元素 `@keydown.esc="(e) => { if (!e.defaultPrevented) emit('escape-key') }"`。菜单打开时的 Esc 已被输入框 `preventDefault` 吃掉，不会冒上来。
4. `active = subagentPhase(record.status) === 'active'`；`queueVisible` 的算法照 ChatView 的 `composerQueueVisible`，用 `pendingInput` 算。

**样式**（scoped）：输入组边框 `--forebrain-divider-strong`，圆角 10px，与原型 `.sa-comp` 一致；textarea 最小一行、自动增高，最高 160px；底栏高 28px 的控件对齐。

**`SubagentTab.test.ts`**
- 输入框占位符是 `chat.subagentComposerPlaceholder`；
- 输入框左边有 `[data-testid="composer-plus"]`，打开后没有 `plus-goal`；点一个技能项后，输入框文字是 `/<技能名> `；
- `active` 时有 `subagent-stop`，点击发出 `stop`；`done` 时没有；
- `active` 时有 `[data-testid="subagent-working"]`，在会话区之后、输入区之前，文字含“工作中”（中文界面）与 `☑` 进度（record 带 `plan` 时）；`done` 时没有；
- 底栏里有 `subagent-run-pill`（record 带模型或预算时）；
- 输入框（`ForebrainPromptTextarea`）收到 `subagent-view` 与 `attach-save` 两个 prop，`scope` 原样传进去；
- 在输入框里按 Esc（未被阻止）发出 `escape-key`；
- `context` 非空时画 `ContextDebugPanel`。

**Verify**：`cd frontend && pnpm test -- src/components/chat/SubagentTab.test.ts` 通过。

### Step 6：`SubagentDrawer.vue`——抽屉外壳（标签、＋、收起）

新建 `frontend/src/components/chat/SubagentDrawer.vue`。只负责外壳，每个标签的内容由父组件通过默认插槽放进来。

**props**
```ts
tabs: SubagentTranscript[]        // 已打开的，按打开顺序
active: string
records: SubagentTranscript[]     // 本对话的全部（＋ 菜单用）
seen: Record<string, number>
overlay: boolean
now?: number                      // 菜单里运行中的用时
```

**emits**：`select(agentId)`、`close-tab(agentId)`、`open(agentId)`（菜单里点了一个）、`collapse()`、`escape-key()`。

**模板**，照原型 `saDrawer`：
```
<aside class="subagent-drawer" :class="{ 'subagent-drawer--overlay': overlay }" data-testid="subagent-drawer" :aria-label="t('chat.subagentDrawer')" @keydown.esc="onEsc">
  <header class="subagent-drawer-head">   <!-- 44px，底边线 -->
    <div role="tablist" class="subagent-drawer-tabs">
      每个标签 <div class="subagent-drawer-tab" :class="{ 'is-on': on }" :data-testid="`subagent-tab-${id}`">
        <button role="tab" :aria-selected="on" @click="emit('select', id)">
          状态图标（active → Loader2 animate-spin 主题色；done → CheckCircle2 成功色；failed → XCircle 危险色）
          <span class="lb" v-tip:bottom.trunc="label">{{ label }}</span>
          未读圆点：!on && record.updatedSeq > (seen[id] ?? 0) → <span class="new" :aria-label="t('chat.subagentUnseen')" />
        </button>
        <button class="x" :aria-label="t('chat.subagentTabCloseAria', { name: label })" v-tip:bottom="t('chat.subagentTabClose')" :data-testid="`subagent-tab-close-${id}`" @click="emit('close-tab', id)"><X/></button>
      </div>
    </div>
    <button aria-haspopup="menu" :aria-expanded="menuOpen" v-tip:bottom="t('chat.subagentOpenOther')" data-testid="subagent-drawer-add" @click="menuOpen = !menuOpen"><Plus/></button>
    <span class="flex-1" />
    <button v-tip:bottom="t('chat.subagentDrawerCollapse')" :aria-label="…" data-testid="subagent-drawer-collapse" @click="emit('collapse')"><PanelRightClose/></button>
    菜单（menuOpen）：<div role="menu" class="subagent-drawer-menu">  <!-- 照原型 .sa-menu：top 40px，宽 320px -->
      <div class="cap">{{ t('chat.subagentDrawerList', { n: records.length }) }}</div>
      每个 record 一行 <button role="menuitem" :data-testid="`subagent-menu-${id}`" @click="emit('open', id); menuOpen = false">
        状态图标 + 名字（截断）+ 右侧小字：已打开 → t('chat.subagentOpened')；active → 用时（formatRuntimeDuration(now - startedAt)）；其余 → subagentStatusLabel
    </div>
  </header>
  <div class="subagent-drawer-body"><slot /></div>   <!-- flex-1 min-h-0 -->
</aside>
```
- 名字用 `subagentTabLabel`。
- 菜单要在点外面或按 Esc 时关闭。点外面的处理照 `ApprovalPresetPicker.vue` 关闭菜单的写法；Esc 时 `preventDefault`，不再发出 `escape-key`。
- `onEsc(e)`：菜单开着就关菜单并 `e.preventDefault()`；否则 `if (!e.defaultPrevented) emit('escape-key')`。

**样式**（scoped，照 `ChatWorkbench.vue` 与原型 `.sad*`）
- `.subagent-drawer`：`width: 440px; flex: none; display: flex; flex-direction: column; min-height: 0; border-left: 1px solid var(--forebrain-divider); background: var(--forebrain-surface)`。
- `--overlay`：照 `.chat-workbench--overlay`（fixed、右侧贴边、`z-index: 44`、`box-shadow: var(--forebrain-shadow-pop)`）。
- `@media (max-width: 1023px)` 下宽度为 `min(440px, calc(100vw - 64px))`。
- 标签：最大宽 200px，高 30px，圆角 7px；选中时背景 `--forebrain-button-alt-bg`。
- 圆点 6px 主题色；关闭按钮 22px。

**`SubagentDrawer.test.ts`**
- 标签顺序与 `tabs` 一致，选中项 `aria-selected="true"`；
- 未读圆点：不在当前标签且 `updatedSeq > seen` 时出现；
- 点关闭发出 `close-tab`；
- 点 ＋ 打开菜单，菜单行数等于 `records`，已打开的显示“已打开”，点一行发出 `open` 并关闭菜单；
- Esc：菜单开着时只关菜单，不发出 `escape-key`；菜单关着时发出 `escape-key`；
- 点收起发出 `collapse`。

**Verify**：`cd frontend && pnpm test -- src/components/chat/SubagentDrawer.test.ts` 通过。

### Step 7：`ChatView.vue`——接上抽屉，拆掉“一个输入框两个主人”

按下面逐项改。**每删一个名字，都 `grep -n` 确认文件里没有残留引用。**

**7.1 删除**
- `AgentViewTabs` 的导入与模板块。
- 消息区里的 `<Transition name="forebrain-agent-view">…</Transition>` 整块，以及包主对话的 `<div v-show="!activeSubagent" key="primary-view">`（只删外壳，保留里面的内容）。
- 文件末尾 `forebrain-agent-view` 的过渡样式。
- 下面这些脚本里的名字：
  - `activeAgentView`、`activeSubagent`、`activeAutoContinue`、`composerPendingInput`；
  - `viewDrafts`、`takeComposerDraft`、`applyComposerDraft`；
  - `openAgentView`、`handleAgentViewEscape`、`handleAgentViewKeydown`，以及它在 `onMounted` / `onUnmounted` 里的 `window` 监听；
  - `inputPlaceholder` 里对子代理的分支；
  - `isFreshChat` 里的 `!activeAgentView.value`。
- `composerQueueVisible` 改成只看主对话的 `pendingInputPreview`。

**7.2 主输入框只属于主对话**
- `<AutoContinueBanner :state="autoContinueForView('')" @cancel="cancelAutoContinue('')" />`；
- 输入框的 `@typed` / `@escape` 改为 `cancelAutoContinue('')`；
- `PendingInputQueuePopover :preview="pendingInputPreview"`；
- `restoreLastQueuedMessage` 只调用 `editLastQueuedMessage()`；
- `interruptAndSendPendingSteers` 只调用 `sendPendingSteersAfterInterrupt()`；
- `handleSubmit` 删掉开头的子代理分支；
- 占位符固定 `t('chat.placeholder')`。

**7.3 抽屉状态**（放在原 `activeAgentView` 的位置）
```ts
// The right-hand subagent drawer (owner 2026-10-18): which subagents are open as tabs, which one is in front, and whether the drawer is shown.
const drawerOpen = ref(false)
const drawerTabs = ref<string[]>([])
const drawerActive = ref('')
/** This conversation's subagents as the "+" menu lists them: a goal's checks only once opened. */
const conversationSubagents = computed(() => subagents.value.filter((r) => r.agentType !== FOREBRAIN_GOAL_CHECK_AGENT_TYPE || drawerTabs.value.includes(r.agentId)))
const drawerRecords = computed(() => drawerTabs.value.map((id) => subagents.value.find((r) => r.agentId === id)).filter((r): r is SubagentTranscript => !!r))
const drawerVisible = computed(() => drawerOpen.value && drawerRecords.value.length > 0 && !isFreshChat.value)
const runningSubagentCount = computed(() => conversationSubagents.value.filter((r) => subagentPhase(r.status) === 'active').length)
```
- `drawerActive` 指向的记录不存在时，画第一个已存在的标签。用 computed `shownActive`：`drawerRecords` 里有 `drawerActive` 就取它，否则取 `drawerRecords[0]?.agentId ?? ''`。

**7.4 打开 / 切换 / 关闭**
- `openSubagentTab(agentId)`：
  1. 不在 `drawerTabs` 里就追加；
  2. `drawerActive = agentId`、`drawerOpen = true`；
  3. `workbench.close()`；
  4. `markAgentSeen(agentId)`；
  5. `void loadSubagentBudget(agentId)`；
  6. 子代理的 / 命令表没取过就 `void refreshSubagentSlashCommands()`（只取一次，存在 `subagentSlashRecords`）；
  7. `await nextTick()` 后 `applySubagentReturnedDraft(agentId)`；
  8. `scheduleBrowsePersist()`。
- `selectSubagentTab(id)`：设为当前、标记已读、`loadSubagentBudget`、持久化。
- `closeSubagentTab(id)`：
  - 从 `drawerTabs` 移除；关掉的是当前标签时，选它右边的，没有右边就选左边；
  - 没有标签了就 `drawerOpen = false`；
  - **关闭标签不停止子代理**；
  - 持久化。
- `toggleSubagentDrawer()`：开着就关；关着时有标签就打开（并 `workbench.close()`），没有标签就 `openSubagentTab`，优先第一个运行中的，没有就打开第一个。
- 互斥：`watch(() => workbench.open.value, (open) => { if (open) drawerOpen.value = false })`。
- 渲染上也互斥：`<ChatWorkbench v-if="workbench.open.value && !isFreshChat && !drawerVisible" …>`。
- 原来所有 `@open="openAgentView"`、`@open-check="openAgentView"`、`@open-agent="openAgentView"`（两张卡、`GoalLine`、`ApprovalCard`）改成 `openSubagentTab`。两张主对话的 `SubagentCallCard` 加 `:active-agent-id="drawerVisible ? shownActive : ''"`。
- `watch(subagents, …)` 改成 `if (drawerVisible.value && shownActive.value) markAgentSeen(shownActive.value)`。

**7.5 模板**：在 `</main>` 之后、`<ChatWorkbench` 之前加
```vue
<SubagentDrawer
  v-if="drawerVisible"
  :tabs="drawerRecords"
  :active="shownActive"
  :records="conversationSubagents"
  :seen="seenAgentActivity"
  :overlay="workbenchOverlay"
  :now="subagentNow"
  @select="selectSubagentTab"
  @close-tab="closeSubagentTab"
  @open="openSubagentTab"
  @collapse="drawerOpen = false; scheduleBrowsePersist()"
  @escape-key="handleSubagentEscape(shownActive)"
>
  <SubagentTab
    v-for="record in drawerRecords"
    v-show="record.agentId === shownActive"
    :key="record.agentId"
    :ref="(el) => setSubagentTabRef(record.agentId, el)"
    :record="record"
    :fold-state="browseStateFor(record.agentId).folds"
    :call-tasks="subagentCallTasks"
    :now="subagentNow"
    :scope="workbenchScope"
    :active-agent-id="shownActive"
    :pending-input="subagentPendingInput[record.agentId] ?? emptyPendingInputPreview()"
    :auto-continue="autoContinueForView(record.agentId)"
    :notice="subagentNotices[record.agentId] ?? null"
    :context="subagentContextTarget === record.agentId ? { data: subagentContextDebug, loading: subagentContextLoading } : null"
    @submit="(m) => handleSubagentSubmit(record.agentId, m)"
    @fold-change="(v) => updateAgentFold(record.agentId, v)"
    @open-agent="openSubagentTab"
    @stop="stopSubagent(record.agentId)"
    @edit-last-queued="recallIntoTab(record.agentId)"
    @interrupt-run="interruptSubagentToSend(record.agentId)"
    @queue-follow-up="subagentDisposition[record.agentId] = 'queue'"
    @typed="cancelAutoContinue(record.agentId)"
    @escape-key="handleSubagentEscape(record.agentId)"
    @files-picked="(files) => onFilesPicked(files, record.agentId)"
    @cancel-auto-continue="cancelAutoContinue(record.agentId)"
    @refresh-context="openSubagentContext(record.agentId)"
    @dismiss-notice="subagentNotices[record.agentId] = null"
  />
</SubagentDrawer>
```
- 已打开的标签都挂载，用 `v-show` 切换，这样各自的草稿、附件、滚动都留在自己的组件里。
- `setSubagentTabRef` 把实例存进 `const subagentTabRefs = new Map<string, InstanceType<typeof SubagentTab>>()`，`el` 为 null 时删除。
- 两个辅助函数：
  - `subagentComposer(agentId)` 返回 `subagentTabRefs.get(agentId) ?? null`；
  - `recallIntoTab(id)`：`const s = await recallSubagentInput(id); if (s) subagentComposer(id)?.restoreSubmission(s)`。
- `SubagentTab` 的 `@escape-key` 与 `SubagentDrawer` 的 `@escape-key` 可能对同一次按键各触发一次，**必须防重**：
  - 在 `SubagentTab` 根元素的处理里，发出事件后调用 `e.preventDefault()`，抽屉的 `onEsc` 见到 `defaultPrevented` 就不再发出；
  - 两边都按 `defaultPrevented` 判断；
  - 测试要覆盖“按一次 Esc 只撤回一次”。

**7.6 发给子代理**
- `handleSubagentSubmit(agentId, message)`：在现有函数上改签名，逻辑不变，但有三处替换：
  - `promptRef` → `subagentComposer(agentId)`；
  - `composerNotice` → `subagentNotices.value[agentId]`（新 `ref<Record<string, {title, detail} | null>>({})`）；
  - `nextSubmissionDisposition` → `subagentDisposition.value[agentId]`（新 `ref<Record<string, 'steer'|'queue'>>({})`，用完重置为 `'steer'`）。
- `compactActiveSubagent` → `compactSubagentIn(agentId)`；拒绝时写该标签的 notice。
- 属于对话的命令仍然 `send(...)` 到主对话，回答落在主对话里。
- `stopSubagent(agentId)`：`await forebrainApi.subagentCancel(agentId)`，出错写该标签的 notice（`getErrorMessage`）。
- `handleSubagentEscape(agentId)`：
  1. `withdrawSubagentInput(agentId)` 有结果 → 逐条 `subagentComposer(agentId)?.restoreSubmission(...)`，结束；
  2. 否则 `if (await interruptSubagentToSend(agentId)) return`；
  3. 否则 `drawerOpen.value = false`。收起抽屉，对应终端“离开子代理视图”。
- 被交还的草稿：`watch(subagentReturnedDraft, …)` 改为对 `drawerTabs` 里每个 id 调 `applySubagentReturnedDraft(id)`；`applySubagentReturnedDraft` 写进 `subagentComposer(id)`。

**7.7 附件**
- `onFilesPicked(files, agentId = '')`：记下 `attachTarget.value = agentId`（新 `ref('')`），其余不变。
- `confirmAttach` 里 `promptRef.value?.addUploaded(uploaded)` 改成：`attachTarget.value ? subagentComposer(attachTarget.value)?.addUploaded(uploaded) : promptRef.value?.addUploaded(uploaded)`。
- `closeAttach` 时清空 `attachTarget`。

**7.8 浏览状态的本地存储**
- `PersistedSessionBrowseState` 改为：
  ```ts
  { seenAgentActivity: Record<string, number>; views: Record<string, ViewBrowseState>; drawer?: { open: boolean; tabs: string[]; active: string } }
  ```
- `readSessionBrowseState` 读 `drawer`，字段缺失或类型不对时用 `{ open: false, tabs: [], active: '' }`。**旧数据里的 `activeAgentView` 忽略**：升级后抽屉一律收起，不迁移。
- `persistSessionBrowseState` 写 `drawer: { open: drawerOpen, tabs: drawerTabs, active: drawerActive }`。
- `watch(sessionId, …, { flush: 'sync' })` 里恢复 `drawerOpen/drawerTabs/drawerActive`，替代原来的 `activeAgentView`；同时清空 `subagentNotices`、`subagentDisposition`、`subagentContextTarget`。
- `currentBrowseViewKey()` 固定返回 `primaryViewKey`。主对话的滚动照旧；标签的滚动交给 `Conversation` 自动跟随，不再保存。

**7.9 地址栏 `agent` 参数**
- 新增 watch，依赖 `[() => route.query.agent, () => subagents.value.length, historyLoading]`：
  - `agent` 非空且 `subagents` 里有它 → `openSubagentTab(agent)`，再 `router.replace` 去掉 `agent`（其余参数保留，写法照文件里“去掉 new”的那段）；
  - 历史已加载完（`!historyLoading.value`），并且 `subagents` 里找不到 → 也去掉 `agent`，什么都不打开。

**7.10 顶栏**（`setTopbar` 的 `trailing`）
- 在工作台按钮**之前**，`!isFreshChat.value && conversationSubagents.value.length > 0` 时加一个按钮，写法照工作台按钮：
  - `class: 'forebrain-chip-btn'`、`'aria-pressed': String(drawerVisible.value)`、`'data-testid': 'subagent-drawer-toggle'`、`onClick: toggleSubagentDrawer`；
  - 内容是 `Bot` 图标、`t('chat.subagentDrawer')`，`runningSubagentCount > 0` 时加 `forebrain-chip-count`。
- 工作台按钮**删掉** `runningRoster` 的数字。

**7.11 主输入框变紧凑**：`<ModelChip … :compact="drawerVisible" />`（审批按钮已在 Step 1 一律去掉前缀，不需要紧凑参数）。

**7.12 工作台概览的“查看”**：`viewRoster(row)` 改为：
- `row.kind === 'subagent'` 且 `subagents` 里有 `row.id` → `openSubagentTab(row.id)`；
- 否则 `const target = agentRosterConversationTarget(row)`（Step 8 会给子代理行带上 `agent`），有就 `router.push(target)`；
- 否则什么都不做。
- `agentRosterViewTarget` 的导入若已不用，就删掉。

**7.13 测试**（`ChatView.test.ts`）
- 删掉 `describe('ChatView subagent view')` 里点 `agent-tab-*` / `.forebrain-agent-tab` 的写法。
- stubs 里把 `SubagentConversation: true` 换成 `SubagentTab: true`；`SubagentDrawer` **不要 stub**，用真组件，因为它只是外壳和插槽。
- 新写 `describe('ChatView subagent drawer')`。开抽屉的方式：主对话的 `SubagentCallCard` stub 发出 `open`（`wrapper.findComponent({ name: 'SubagentCallCard' }).vm.$emit('open', 'task-1')`），先 `seedSubagent()` 并让 `messages` 里有一张卡（照文件里造消息的写法；若造不出卡，就改为在 `SubagentDrawer` 的 `open` 事件上触发）。用例：
  1. 打开后有 `[data-testid="subagent-drawer"]` 与 `subagent-tab-stub`，工作台被关掉（`useWorkbench().open.value === false`）；再打开工作台，抽屉消失。
  2. Esc（在 `subagent-tab-stub` 上 `$emit('escape-key')`）时，`withdrawSubagentInput` 有返回就调用该标签实例的 `restoreSubmission`（stub 的实例方法用 `vi.fn()` 准备；做不到就断言 `withdrawSubagentInput` 与 `interruptSubagentToSend` 的调用顺序），抽屉不收起。
  3. 两者都没有动作时，Esc 收起抽屉。
  4. 标签的 `auto-continue` prop 等于 `autoContinueForView('task-1')`；发出 `cancel-auto-continue` 时调用 `cancelAutoContinue('task-1')`。
  5. 主输入框提交只走主对话：`sendToSubagent` 没被调用。
  6. 顶栏：有子代理时 `subagent-drawer-toggle` 出现，带运行中个数；工作台按钮没有数字。
  7. 路由 `/?session=s1&agent=task-1`：挂载后抽屉打开到 `task-1`，地址栏不再带 `agent`。
- 原“keeps each view’s composer draft to that view”删除。各标签的草稿天然分开，由 `SubagentTab` 自己保存。

**Verify**：
- `cd frontend && pnpm test -- src/views/ChatView.test.ts` 通过；
- `grep -n "activeAgentView\|openAgentView\|AgentViewTabs\|viewDrafts\|handleAgentViewKeydown" frontend/src/views/ChatView.vue` 无输出；
- `npx vue-tsc --noEmit -p tsconfig.json` exit 0。

### Step 7b：主对话的运行状态行（与终端一致）

1. 新建 `frontend/src/components/chat/RunStatusLine.vue`，主对话与子代理标签共用：
   - props：`label: string`、`plan?: WorkedPlanProgress`、`tail?: string`（追加在待办进度之后的一段，主对话用来放“首 token …”）；
   - 渲染：`<div role="status" class="run-status-line">`，依次是：
     - `Loader2`（13px、`--forebrain-brand-1`、`animate-spin`）；
     - 等宽 `label`；
     - 有 `plan` 时 ` · ` + `<PlanProgressSegments :plan="plan" />`；
     - 有 `tail` 时 ` · ` + 等宽 `tail`；
   - 样式照原型 `.run-line`：12px、`--forebrain-muted-text`、不换行，过长时最后一段截断；分隔点是灰色的 `·`，左右各 6px。
   - `RunStatusLine.test.ts`：三段齐全时的顺序；没有 `plan` / `tail` 时不出现多余的 `·`。
2. `ChatView.vue`：
   - 从 `useChatStream()` 解构出 `runtimeStatus`（已导出）；
   - 从 `@/composables/useChatStream` 导入 `formatRuntimeStatusLabel` 与 `formatFirstTokenDuration`（都已导出）。
   - computed `runLine`：
     ```ts
     const runLine = computed(() => {
       const status = runtimeStatus.value
       if (status.kind === 'idle') return null
       // The terminal's order: the state, then the checklist, then the first-token wait.
       const { label, plan } = formatRuntimeStatusLabel({ ...status, firstTokenMs: undefined }, t)
       if (!label) return null
       const wait = formatFirstTokenDuration(status.firstTokenMs)
       return { label, plan, tail: wait ? t('chat.firstToken', { duration: wait }) : '' }
     })
     ```
     `formatRuntimeStatusLabel` 本身会把首 token 拼进 `label`，顺序是“状态 · 首 token”。这里先去掉首 token，再作为 `tail` 放到待办进度之后，得到终端的顺序“状态 · ☑ · 当前任务 · 首 token”。**不要改 `useChatStream.ts`**。
     `formatRuntimeStatusLabel` 的第二个参数要的是翻译函数：传 ChatView 里的 `t`；类型不合时用 `(key, params) => t(key as I18nKey, params)` 包一层。
   - 模板：在输入区容器（`<div class="z-10 shrink-0 bg-[var(--forebrain-surface)]" …>`）里、`<PromptInputProvider` 之前，加 `<RunStatusLine v-if="runLine && !isFreshChat" data-testid="run-status-line" :label="runLine.label" :plan="runLine.plan" :tail="runLine.tail" class="mb-1.5" />`。
   - 排队消息预览（`PendingInputQueuePopover`）必须在状态行之下、输入框之上，两者不重叠。若预览是绝对定位、盖住了状态行，在 ChatView 里给状态行留出位置，**不改** `PendingInputQueuePopover.vue`。截图确认。
3. `locales/index.ts`：下面三个键的**中文**改成中文（英文保持终端原文不变）：

   | 键 | 中文 |
   |---|---|
   | `chat.runtimeWorking` | 工作中（{duration} · Esc 中断） |
   | `chat.runtimeShell` | 等待后台终端（{duration} · Esc 中断） |
   | `chat.runtimeReconnecting` | 正在重新连接… {attempt}/{max}（{duration} · Esc 中断） |

   目标的两句（`chat.goal.runtimeWorking` / `runtimeChecking`）中文已有，不动。
4. `ChatView.test.ts` 加 `describe('ChatView run status line')`：
   - `fake.runtimeStatus.value = { kind: 'working', elapsedMs: 72_000, planCompleted: 3, planTotal: 5, planActive: 'fix the middleware', firstTokenMs: 1200 }` → `[data-testid="run-status-line"]` 依次含 `Working (1m 12s • esc to interrupt)`、`3/5`、`fix the middleware`、`first token 1.2s`，且首 token 在最后；
   - `kind: 'idle'` → 没有这一行；
   - 新对话页（`isFreshChat`）→ 没有。
   - 测试的 fake 已有 `runtimeStatus` 与 `formatRuntimeStatusLabel`（取真实实现），如缺 `formatFirstTokenDuration` 就照它们补上。

**Verify**：`cd frontend && pnpm test -- src/components/chat/RunStatusLine.test.ts src/views/ChatView.test.ts` 通过。

### Step 8：花名册跳转带上子代理（`agent` 参数）

1. `useAgentRoster.ts` 的 `agentRosterConversationTarget(row)`（077 加的）：子代理行（`row.kind === 'subagent'`）的 query 加 `agent: row.id`，即 `{ path: '/', query: { session, agent: row.id } }`；主代理行不加。`useAgentRoster.test.ts` 补断言。
2. ~~`SubagentsView.vue`~~ **本点取消（2026-10-18）**：owner 要求把菜单栏“子代理”页整页重做成“当前主代理的子代理配置”（不再是运行中列表），由后续计划实施；本计划**不改** `SubagentsView.vue` 与它的测试。下面原文仅作记录，不要执行：
   `SubagentsView.vue`：
   - **删掉**操作栏的 `打开会话`（`MessageSquare`、`jumpToSession`、`sessionTarget`，以及没有会话时的占位 `RowActionSlot`）。操作栏只剩 `停止`，与原型一致。
   - “所在会话”按钮仍调 `jumpToConversation(row)`，现在会打开父对话并打开这个子代理的标签。
   - `SubagentsView.test.ts`：删 `打开会话` 相关断言；加断言：点标题跳到 `?session=<parentSessionId>&agent=<id>`。

**Verify**：`cd frontend && pnpm test -- src/composables/useAgentRoster.test.ts` 通过。

### Step 9：删 `AgentViewTabs`、整理文案

1. 删除 `components/chat/AgentViewTabs.vue` 与 `.test.ts`；确认 `grep -rn "AgentViewTabs\|forebrain-agent-tab" frontend/src` 无输出（若 `main.css` 里有 `.forebrain-agent-tab*` 样式，一并删掉）。
2. `locales/index.ts` 中英文新增或修改下列键。**先 `grep -n "'键名'"` 确认是否已存在**：已存在只改值，避免 TS1117。

   | 键 | 中文 | 英文 |
   |---|---|---|
   | `chat.subagentDrawer` | 子代理 | Subagents |
   | `chat.subagentDrawerCollapse` | 收起 | Collapse |
   | `chat.subagentOpenOther` | 打开其他子代理 | Open another subagent |
   | `chat.subagentDrawerList` | 本对话的子代理 · {n} | Subagents in this conversation · {n} |
   | `chat.subagentOpened` | 已打开 | Open |
   | `chat.subagentUnseen` | 有新内容 | New activity |
   | `chat.subagentTabClose` | 关闭 | Close |
   | `chat.subagentTabCloseAria` | 关闭 {name} | Close {name} |
   | `chat.subagentTaskCap` | 交给它的任务 | Its task |
   | `chat.subagentTaskExpand` | 展开任务 | Show the whole task |
   | `chat.subagentTaskCollapse` | 收起任务 | Collapse the task |
   | `chat.subagentRunPillAria` | 这个子代理的模型与上下文 | This subagent's model and context |
   | `chat.subagentRunPillTip` | 模型与上下文 | Model and context |
   | `chat.subagentCtxUsed` | 上下文已用 {used} / {window} | Context used {used} / {window} |
   | `chat.subagentCtxNearFull` | 快满了，可在下面输入 /compact 压缩它 | Nearly full — type /compact below to compact it |
   | `chat.subagentRunModel` | 模型 | Model |
   | `chat.subagentRunEffort` | 思考强度 | Reasoning |
   | `chat.subagentRunWindow` | 上下文窗口 | Context window |
   | `chat.subagentRunUsage` | 本次用量 | Usage |
   | `chat.subagentRunUsageValue` | 输入 {input} · 输出 {output} | {input} in · {output} out |
   | `chat.subagentRunFixed` | 模型与思考强度在派发时确定 | Model and reasoning are fixed at dispatch |
   | `chat.subagentStop` | 停止这个子代理 | Stop this subagent |
   | `chat.subagentWaitingApproval` | 等待批准 | Waiting for approval |
   | `chat.subagentWaitingInput` | 等待输入 | Waiting for input |
   | `chat.subagentCancelled` | 已取消 | Cancelled |
   | `chat.subagentInterrupted` | 已中断 | Interrupted |
   | `chat.subagentWorking`（只改中文） | 工作中（{duration}） | Working ({duration})（不变，与终端一致） |
   | `chat.subagentComposerPlaceholder`（改值） | 给这个子代理发消息，/ 调出命令，@ 引用文件 | Message this subagent — / for commands, @ to reference files |
   | `chat.subagentViewCommand`（改值；原文要按 Esc 回主对话，现在主输入框一直在） | 这个命令要在主对话的输入框里执行。 | Run this from the main view's composer. |

   注意表里中英文模板中的 `{model}`、`{budget}`、`{n}`、`{name}`、`{duration}` 都是占位符，原样保留。
3. 删除不再使用的键：`chat.agentViewMain`、`chat.agentViewsAria`、`chat.agentViewUnseen`、`chat.agentViewBack`、`chat.approvalChip`，以及 `SubagentConversation` 头部删掉后不再使用的 `chat.subagentTokens`（**先 grep，确认没有别处使用再删**）。`chat.subagentViewCommand` **保留**（隐藏命令的提示仍用它，e2e 断言其英文开头 `Run this from the main view`）。

**Verify**：`cd frontend && npx vue-tsc --noEmit -p tsconfig.json` exit 0；`pnpm test` 全绿。

### Step 10：e2e

1. 改 `e2e/subagent-conversation.spec.ts`，三个用例都改成通过抽屉操作：
   - 打开子代理：点卡片里的任务行 `[data-testid="subagent-task-row"]`；第三个用例只有种在库里的子代理、没有卡片，就点顶栏 `[data-testid="subagent-drawer-toggle"]`。
   - 断言 `[data-testid="subagent-drawer"]` 出现。
   - 输入框改用抽屉里的：`const tabInput = page.locator('[data-testid="subagent-drawer"] textarea')`，提交按钮用 `page.locator('[data-testid="subagent-drawer"] button[aria-label="Submit"]')`。
   - 文字断言的范围从 `.chat-shell main` 改为 `[data-testid="subagent-drawer"]`。
   - 最后一步“Esc 回到主对话”改为：在抽屉输入框里按 Escape → 抽屉收起（`subagent-drawer` 数量为 0），**并且**主对话 `.chat-shell main` 不含子代理的回答。
   - `subagent-budget` 现在在输入框底栏的胶囊里，文字只有百分比：第一个用例改为 `toHaveText(/^\d+%$/)`；第三个用例（plan-reviewer）改为 `toHaveText('65%')`，再点 `subagent-run-pill`，断言卡片含 `zhipuai/glm-5.3-flash`、`subagent-context-window` 为 `1M`（原来断言 `.chat-shell main` 含模型名的那句删掉）。
   - 其余断言（`subagent-user-message`、隐藏命令那句、`/compact` 的卡片）保持不变。
2. 新建 `e2e/subagent-drawer.spec.ts`，用假模型（`[[e2e:subagent-net]]`，写法照 `subagent-conversation.spec.ts` 的第一个用例），视口 1280×800：
   1. 派出子代理后，顶栏有 `subagent-drawer-toggle`；工作台按钮里没有 `.forebrain-chip-count`。
   2. 点任务行 → 抽屉打开，标签 `subagent-tab-<id>` 选中，该任务行 `aria-current="true"`；截图 `078-drawer-live.png`。
   3. **主输入框不溢出**：`await page.locator('main [data-slot=input-group]').evaluate((el) => el.scrollWidth <= el.clientWidth)` 为 `true`，主输入框的提交按钮在视口内；`[data-testid="approval-picker"]` 的文字是 `Default`，不含 `Approvals:`（英文界面）。
   4. 点顶栏工作台 → 抽屉消失、工作台出现；再点 `subagent-drawer-toggle` → 工作台消失、抽屉回来，标签还在。
   1a. 主对话运行时，`[data-testid="run-status-line"]` 在主输入框正上方（下边缘 ≤ 输入框上边缘，间距 < 16px），英文界面的文字以 `Working (` 开头并含 `esc to interrupt`；这一轮结束后它消失。截图 `078-main-run-line.png`。
   2a. 子代理运行时，`[data-testid="subagent-working"]` 在抽屉输入框正上方（它的下边缘 ≤ 输入框上边缘，且两者间距 < 16px），会话区里不含 “Working (”；子代理结束后它消失。
   3a. 抽屉输入框底栏有 `subagent-run-pill`，文字含百分比与模型名；点开出现 `subagent-run-card`，卡片里没有按钮；截图 `078-drawer-run-card.png`。
   4a. 点抽屉输入框左边的 `[data-testid="subagent-drawer"] [data-testid="composer-plus"]`：菜单里有 `plus-attach`，没有 `plus-goal`；菜单宽度与抽屉输入区相同（误差 ≤ 2px）；截图 `078-drawer-plus.png`。点 `plus-attach` 后用 `page.setInputFiles` 选一张 png，出现“保存到”弹窗；确认后这张图出现在**抽屉**输入框的附件里，主输入框里没有。
   5. 在抽屉输入框里输入 `@`，出现文件候选（选择器的候选列表，按 `ForebrainPromptTextarea` 现有的 @ 菜单 DOM 定位）；选第一项后，输入框里出现 `@` 引用；截图 `078-drawer-mention.png`。
   6. 关闭唯一的标签（`subagent-tab-close-<id>`）→ 抽屉收起，子代理**没有被停止**（卡片里的行仍是运行中或已完成，不是已取消）。点顶栏 `subagent-drawer-toggle` → 抽屉重新打开这个子代理。点 `subagent-drawer-add`，菜单里这一行写“Open”（英文界面的“已打开”）。
   7. 刷新页面后，抽屉与标签恢复（本地存储）。
3. 跑全套 e2e。

**Verify**：
- `FOREBRAIN_E2E_SPEC=e2e/subagent-drawer.spec.ts scripts/acceptance/web_e2e.sh` 末行 `web e2e: PASS`；
- `FOREBRAIN_E2E_SPEC=e2e/subagent-conversation.spec.ts scripts/acceptance/web_e2e.sh` 末行 `web e2e: PASS`；
- 全套 `scripts/acceptance/web_e2e.sh` 末行 `web e2e: PASS`。

## Test plan

- 单测：
  - `subagentStatus.test.ts`、`effortLabel.test.ts`、`SubagentRunPill.test.ts`、`RunStatusLine.test.ts`；
  - `SubagentConversation.test.ts`（改）、`SubagentCallCard`（高亮）；
  - `SubagentTab.test.ts`、`SubagentDrawer.test.ts`；
  - `ForebrainPromptTextarea.test.ts`（`subagentView`）、`ComposerPlusMenu`（`subagentView`：无“目标”）、`ApprovalPresetPicker`（无前缀）/ `ModelChip`（`compact`）；
  - `ChatView.test.ts`（新的 drawer 组）；
  - `useAgentRoster.test.ts`。
- e2e：`subagent-conversation.spec.ts`（改）、`subagent-drawer.spec.ts`（新），然后跑全套。
- 截图对照原型 v65 的“子代理 · 实时 / 回放 / 打开其他子代理 / @ 引用文件 / 加号菜单 / 模型与上下文 / 上下文快满”与“对话 · 运行中（状态行）”八个状态，在报告里并排列出。

## Done criteria

- [ ] Drift check 全部有输出（记在报告里）
- [ ] `cd frontend && pnpm test` 全绿；`npx vue-tsc --noEmit -p tsconfig.json` exit 0
- [ ] `grep -rn "AgentViewTabs\|forebrain-agent-tab\|activeAgentView\|openAgentView" frontend/src` 无输出
- [ ] `grep -n "subagentView: props.subagentView === true" frontend/src/components/ForebrainPromptTextarea.vue` 有输出
- [ ] 两个 e2e 单文件与全套 `scripts/acceptance/web_e2e.sh` 末行都是 `web e2e: PASS`
- [ ] 截图与原型 v65 八个状态一致（报告里列出差异，没有就写“无”）
- [ ] `git status --short | diff $TMPDIR/078-before.txt -` 里新出现或变化的路径都在 Scope 内

## STOP conditions

- Drift check 前两条没有输出（077 未执行）。
- 花名册行的 `id`（`row.id`）与 `SubagentTranscript.agentId` 不是同一个值（Step 7.12 的“工作台概览打开标签”对不上）。报告两者的实际取值，**不要**在前端做映射猜测。
- 抽屉输入框底栏在 440px 宽时放不下（＋、胶囊、停止、发送溢出或换行）。报告各控件实测宽度，不要自行删掉胶囊里的信息，也不要去改 `ai-elements-vue/`。
- e2e 第 3 条（主输入框不溢出）在 1280 宽度下失败。报告各按钮的实测宽度，不要自行再删按钮或文字。
- 要改 `useChatStream.ts` 才能实现某项（例如需要新的子代理接口）。
- 要改 Scope 以外的文件才能通过测试。

## Maintenance notes

- **子代理的一切交互都在 `SubagentTab` 里**；主输入框只属于主对话。以后给子代理加能力（比如新的 / 命令），改 `handleSubagentSubmit(agentId, …)` 与 `SubagentTab`，不要再让主输入框按视图分流。
- 运行状态行（主对话与子代理标签）都用 `RunStatusLine`，文字来自 `formatRuntimeStatusLabel` 与终端同一套写法。以后终端的状态行改了（新增状态、改顺序），web 跟着改这两处。
- 子代理的模型、思考强度、上下文都在输入框底栏的 `SubagentRunPill` 里，数据全部来自 `SubagentTranscript`（派发时的模型、预算事件）。以后要加“改子代理模型”之类的能力，先改原型，不要把胶囊直接改成下拉。
- 已打开的标签都挂载着（`v-show`）。打开的标签很多时，每个标签都在实时渲染；如果以后出现性能问题，再考虑只挂载当前标签，并把草稿存到 `ChatView`。
- 浏览状态的本地存储形状变了：`drawer` 字段取代了 `activeAgentView`。旧数据里的 `activeAgentView` 被忽略，所以升级后第一次打开时抽屉是收起的，这是有意的。
- 抽屉与工作台互斥靠两处保证：`watch(workbench.open)` 与模板里 `ChatWorkbench` 的 `!drawerVisible` 条件。改其中一处时两处一起看。
- Esc 的三层含义（撤回 → 中断后发送 → 收起抽屉）对应终端子代理视图的规则，改动前先对照终端。
