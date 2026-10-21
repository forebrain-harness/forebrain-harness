# Plan 089: 提问卡与消息队列卡上印出来的每个操作都真的存在（Esc/←→/聊一聊、取回、滚动、进度）

> **Executor instructions**: 按步骤执行，每步跑完「Verify」并确认输出符合预期再进入下一步。
> 命中「STOP conditions」任一条就停下来报告，不要即兴改动。全部完成后更新
> `plans/README.md` 里本计划那一行的状态（除非派发你的评审方说他维护索引）。
>
> **Drift check（先跑）**: `git diff --stat 8d3322c..HEAD -- frontend/src/components/PendingInputQueuePopover.vue frontend/src/components/chat/AskTabs.vue frontend/src/components/chat/ApprovalCard.vue frontend/src/composables/usePendingActions.ts frontend/src/views/ChatView.vue`
> 若有变动，先把「Current state」的摘录与现码对照；对不上按 STOP 处理。

## Status

- **Priority**: P1
- **Effort**: M
- **Risk**: LOW–MED
- **Depends on**: none
- **Category**: bug
- **Planned at**: commit `8d3322c`, 2026-10-21

## Why this matters

这两张卡都在**印出它做不到的操作**：提问卡底部写着「Esc 取消 / 点标签或 ←/→ 切题」，而 `AskTabs.vue` 里没有任何键盘绑定，
`usePendingActions` 也没有取消提问的入口——用户按 Esc 毫无反应，想让 agent 别等了只能把 run 杀掉；卡片头部还会在多题表单上
显示 `3/2` 这种不存在的进度。消息队列卡把内容裁在 42vh 且没有滚动条（本仓铁律：长内容滚动、不截断），行内「取回这条消息」
无论点哪一行都只会取回**最后一条**。修完之后：卡上的每个字都能兑现，队列长了能滚，取回只有一个真实的语义。

## Current state

- `frontend/src/components/chat/AskTabs.vue:164-173` — 底部提示条（文案来自设计稿 `.ask-foot`）：

```html
<div class="ask-foot">
  <span class="ask-foot-num">{{ onReview ? '' : `${(currentQuestion?.options.length ?? 0) + 2}.` }}</span>
  <span>{{ t('chat.askChatAboutIt') }}</span>
  <span class="ask-foot-grow" />
  <span class="ask-foot-cap">
    {{ questions.length > 1 ? t('chat.askFootKeysMany') : t('chat.askFootKeysOne') }}
  </span>
</div>
```

  `frontend/src/locales/index.ts:1475-1476` = `'Esc 取消'` / `'点标签或 ←/→ 切题 · Esc 取消'`；
  `:1474` = `'和它聊聊'`。**整个 `AskTabs.vue` 没有任何 `keydown`**（`grep -n keydown` 零命中），
  「和它聊聊」是一个没有 handler 的 `<span>`。
- `frontend/src/composables/usePendingActions.ts:289-303` — 提问只有"提交"，没有取消：

```ts
async function submitAsk(actionId: string) {
  ...
  await submitAction(actionId, () => forebrainApi.actionsAnswer(actionId, { answers: payload }))
}
```

  共用提交助手是 `submitAction(actionId, request)`（`:128-153`，负责 `submitting` 去重、错误、刷新）。
- 引擎侧**支持**取消一个提问：`pkg/turn/approval.go:379-384`（`decideAsk`）：

```go
if choice == "cancel" {
	next, err := s.Actions.Cancel(ctx, act.ID, approvalReason(reply.Reason, "cancelled"))
	return ApprovalResult{Action: next, Cancelled: err == nil}, err
}
```

  网关入口是 `POST /api/actions/:id/approve` 带 `{"decision":"cancel"}`
  （路由 `pkg/gateway/api_extra.go:207-210`，`decision` 字段在 `:3541`，默认 `accept` 在 `:3559-3562`）；
  前端已有 `forebrainApi.actionsApprove(id, { decision })`（`frontend/src/lib/api.ts:2895-2914`）。
- 终端侧同一张卡的形状基准在 `pkg/tui/overlays.go:1976-2003`（注释列了 Esc/←→/「Chat about this」的真实绑定）。
- `frontend/src/components/chat/ApprovalCard.vue:226-231` — 头部进度（**设计稿的 `askHead` 只印动词，没有分数**，
  见 `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html:3298-3300`）：

```html
<span v-if="qa.form.questions.length > 1" class="xp-head-progress">
  {{ (askTabs[qa.id] ?? 0) + 1 }}/{{ qa.form.questions.length }}
</span>
```

  `askTabs`（`:369`）、`@tab` 绑定（`:251`）、`.xp-head-progress` 样式（`:423`）都只服务这一行；
  `AskTabs.vue:247-248` 的 `watch(current, (index) => emit('tab', index))` 是它的唯一来源，而进入 review 屏时
  `current = questions.length`（`:45`、`:150`、`:283-285`）⇒ 头部印出 `(n+1)/n`。
- `frontend/src/components/PendingInputQueuePopover.vue:172-183` — 队列卡容器（**重写时丢掉了旧实现的滚动容器**）：

```css
.queue-card { width: 100%; max-width: 780px; max-height: 42vh; margin-inline: auto; gap: 0;
  overflow: hidden; /* ← 卡片内没有任何 overflow-y:auto 的容器 */ ... }
```

  被删掉的旧实现（`git diff` 可见）：`<div class="max-h-[calc(42vh-45px)] overflow-y-auto px-3 py-2">`。
  `ai-elements-vue/packages/elements/src/queue/` 下没有任何 `overflow` 规则（`grep -rn overflow …/queue/` 零命中）。
- `frontend/src/components/PendingInputQueuePopover.vue:150-157` — 行内「取回」按钮对**每一行**都发同一个事件：

```html
<QueueItemAction :aria-label="t('chat.queueEditItem')" data-testid="queue-item-edit" @click="emit('edit-last-queued')">
```

  接收端 `frontend/src/views/ChatView.vue:1205-1209` → `useChatStream.editLastQueuedMessage()`
  （`frontend/src/composables/useChatStream.ts:3669-3689`）= `held[held.length-1]` 或服务端 `action:'edit_last'`。
  文案 `frontend/src/locales/index.ts:1146`：`'取回这条消息'`。
  协议层面只支持"取回最后一条"，本计划不改协议（见 Out of scope）。
- 设计稿 `app-preview.html` 里**没有**队列卡（`grep -n queue` 零命中），所以队列卡的形状不受 1:1 约束；
  但 `.ask-foot` 在设计稿里存在（`:3314-3316`），因此提问卡底部**必须保留**、只能让文字变成真的。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| 前端单测 | `cd frontend && pnpm test -- src/components/chat/AskTabs.test.ts src/components/chat/ApprovalCard.test.ts src/components/PendingInputQueuePopover.test.ts` | 全 passed |
| 前端全量 | `cd frontend && pnpm test` | 全 passed |
| 类型检查 | `cd frontend && pnpm exec vue-tsc --noEmit` | exit 0 |
| e2e（相关问题卡） | `FOREBRAIN_E2E_SPEC="e2e/message-cards.spec.ts e2e/approval-families.spec.ts" scripts/acceptance/web_e2e.sh` | `web e2e: PASS` |

## Scope

**In scope**：
- `frontend/src/components/chat/AskTabs.vue` + `AskTabs.test.ts`
- `frontend/src/components/chat/ApprovalCard.vue` + `ApprovalCard.test.ts`
- `frontend/src/composables/usePendingActions.ts` + `usePendingActions.test.ts`
- `frontend/src/components/PendingInputQueuePopover.vue` + `PendingInputQueuePopover.test.ts`
- `frontend/src/components/ForebrainPromptTextarea.vue`（只在 `defineExpose` 上加 `focus`）
- `frontend/src/views/ChatView.vue`（只改提问卡的 `chat-about` 接线与队列卡事件）
- `frontend/src/locales/index.ts`（只改/删本计划的键）
- `ai-elements-vue/packages/elements/src/queue/*.vue`（只在确实需要给 `QueueSectionContent` 加滚动时改）

**Out of scope**：
- **不改协议**："按行取回任意一条"需要引擎与网关新增接口，本计划只让 UI 不再撒谎（详见 Step 5 的取舍与
  Maintenance notes 里的后续建议）。
- `frontend/src/composables/useChatStream.ts`：`editLastQueuedMessage` 的语义正确，不要改。
- 提问卡的选项/复核屏渲染（`AskTabs.vue` 的上半部分）：084 已定稿，不要顺手改。
- 设计稿 `app-preview.html`：**不要**为了这次改稿（`.ask-foot` 保留，队列卡稿里没有）。
- `frontend/src/components/chat/PendingActionsPanel.vue`：死代码，由 093 处理。

## Git workflow

- 直接改当前 `dev` 工作区；**不 commit、不 push**，除非派发方明确要求。
- 若要求提交：`fix(web): …`，body 用 `Why:` / `What:` / `Verification:`，`git commit -s`。

## Steps

### Step 1: 提问卡能真的取消（引擎通路已存在）

1. `frontend/src/composables/usePendingActions.ts` 增加：

```ts
/** The web's Esc: the same "cancel" the terminal's overlay returns for an
 *  asked form (pkg/turn/approval.go decideAsk), so the run stops waiting
 *  instead of hanging on a question nobody will answer. */
async function cancelAsk(actionId: string) {
  await submitAction(actionId, () => forebrainApi.actionsApprove(actionId, { decision: 'cancel' }))
}
```

   并加进 `return { … }`（与 `submitAsk` 同处）。

2. `frontend/src/components/chat/AskTabs.vue` 加键盘绑定（挂在卡片根元素上，`tabindex="-1"` 或 `@keydown` 在根 div）：

```ts
function onKeydown(event: KeyboardEvent) {
  if (event.key === 'Escape') { event.preventDefault(); emit('cancel'); return }
  if (event.key === 'ArrowRight' || event.key === 'Tab' && !event.shiftKey) {
    event.preventDefault(); goto(Math.min(current.value + 1, questions.length)); return
  }
  if (event.key === 'ArrowLeft' || event.key === 'Tab' && event.shiftKey) {
    event.preventDefault(); goto(Math.max(current.value - 1, 0))
  }
}
```

   `goto(i)` 只夹在 `[0, questions.length-1]`（review 屏由点击「✔ 提交」标签进入，不受方向键影响）。
   在输入「其他」的 `textarea` 上不要拦 `ArrowLeft/ArrowRight/Tab`（正在写字），只在 `event.target` 不是
   `HTMLInputElement`/`HTMLTextAreaElement` 时处理方向键。
3. 「和它聊聊」从 `<span>` 改成 `<button type="button" @click="emit('chat-about')">`（保持 `.ask-foot` 的排版；
   样式沿用现有 span 的颜色即可）。
4. `ApprovalCard.vue` 的 `<AskTabs>` 上接线：

```html
@cancel="cancelAsk(qa.id)"
@chat-about="() => { void cancelAsk(qa.id); emit('focus-composer') }"
```

   `ChatView.vue` 收 `focus-composer` → `promptRef.value?.focus()`。
5. `ForebrainPromptTextarea.vue` 在 `defineExpose({...})` 里加 `focus`：

```ts
function focus() { textareaRef.value?.focus() }
```

**Verify**: `cd frontend && pnpm exec vue-tsc --noEmit` → exit 0。

### Step 2: 删掉设计稿里没有的头部进度（顺带消灭 `3/2`）

1. `ApprovalCard.vue` 删除 `:226-231` 的 `.xp-head-progress` 整块、`:369` 的 `askTabs`、`:251` 的 `@tab` 绑定、`:423` 的样式块。
2. `AskTabs.vue` 删除 `watch(current, (index) => emit('tab', index), { immediate: true })` 与 `tab` 事件声明
   （头部既然不再显示分数，就没有消费者）。

**Verify**: `grep -rn "askTabs\|xp-head-progress\|emit('tab'" frontend/src` → 无输出；
`cd frontend && pnpm test -- src/components/chat/AskTabs.test.ts src/components/chat/ApprovalCard.test.ts` → 通过
（若有断言依赖被删的进度，按 Step 6 改成新的语义，不要为了让旧断言过而把进度加回来）。

### Step 3: 队列卡能滚动（长内容不截断）

`PendingInputQueuePopover.vue`：把 `max-height` 从卡片移到列表区，卡片只做高度约束与外框，头部固定、列表可滚：

```css
.queue-card { display: flex; flex-direction: column; max-height: 42vh; overflow: hidden; /* 外框仍是 hidden */ }
.queue-body { min-height: 0; overflow-y: auto; }   /* 新增：包住 QueueSection 循环 */
```

模板里把 `v-for` 的 `QueueSection` 包进 `<div class="queue-body">`。要求：

- 头部（`.queue-head`）不滚；
- 三段 section 折叠时卡片自然收缩（`max-height` 只是上限）；
- 滚动条出现时不得压掉行内按钮的点击区（给 `.queue-body` 留 `padding-right`）。

**Verify**: 在 Step 6 加的测试里断言 `.queue-body` 存在且是滚动容器（见下方用例）。

### Step 4: 行内「取回」不再是假承诺

行内按钮对每一行都发同一个"取回最后一条"事件，这是本计划唯一必须做**减法**的地方。
在 `PendingInputQueuePopover.vue` 里：

- 删除 `QueueItemActions` / `QueueItemAction` 的引入与 `:150-157` 的整段行内按钮；
- 头部的「⌥ + ↑」按钮保留（它本来就是"取回最后一条"的语义，`data-testid="queue-edit"` 不变）；
- 若 `QueueItemActions` 因此在别处也不用，保持 `ai-elements-vue` 的导出不动（库是共享层，不因一次调用丢失就删组件）。

如果执行到这里时你发现引擎已经能按行取回（`grep -n "edit_last\|edit_queued" pkg/run/*.go`），先看 STOP conditions。

**Verify**: `grep -rn "queue-item-edit\|queueEditItem" frontend/src` → 无输出（含 locales 的两处键已删）。

### Step 5: 文案与键收敛

`frontend/src/locales/index.ts`：
- 删 `chat.queueEditItem`（zh `:1146` / en `:2898`）；
- `chat.askFootKeysOne` / `chat.askFootKeysMany` **保留原文**（现在它们是真的了）；
- 若 Step 2 让任何键失去消费者（`grep` 确认），一并删除，zh/en 两处成对处理。

**Verify**: `cd frontend && pnpm test` → 全 passed（语言文件有键完整性用例，见 `frontend/src/locales/*.test.ts`，若有则会被它抓到）。

### Step 6: 用例

1. `frontend/src/components/chat/AskTabs.test.ts`：
   - `Esc` ⇒ `emit('cancel')` 恰好一次；
   - `ArrowRight`/`ArrowLeft` 切换当前题（断言屏幕上换到第二题）；
   - 在「其他」输入框里按 `ArrowLeft` **不**触发切题；
   - 点「和它聊聊」⇒ `emit('chat-about')`；
   - review 屏（`current === questions.length`）时不再有进度文本（`wrapper.text()` 不含 `/`）。
2. `frontend/src/components/chat/ApprovalCard.test.ts`：`@cancel` 接线调用 `cancelAsk`（用 stub 注入
   `usePendingActions` 或断言 API 调用一次 `POST /api/actions/:id/approve` 带 `decision:'cancel'`，照现有测试的 mock 方式）。
3. `frontend/src/components/PendingInputQueuePopover.test.ts`：
   - 渲染 30 条 queued message ⇒ 存在 `.queue-body`，且 `getComputedStyle`/class 断言它带 `overflow-y: auto`；
   - 全文件**不得**再有 `queue-item-edit` 的点击断言；
   - 点头部 `queue-edit` ⇒ `emit('edit-last-queued')` 一次。

**Verify**: `cd frontend && pnpm test -- src/components/chat/AskTabs.test.ts src/components/chat/ApprovalCard.test.ts src/components/PendingInputQueuePopover.test.ts` → 全 passed。

### Step 7: 全量回归与真机抽查

**Verify**:
1. `cd frontend && pnpm test` → 全 passed。
2. `cd frontend && pnpm exec vue-tsc --noEmit` → exit 0。
3. `FOREBRAIN_E2E_SPEC="e2e/message-cards.spec.ts e2e/approval-families.spec.ts" scripts/acceptance/web_e2e.sh` → `web e2e: PASS`。
4. 真机（fake provider）：进入一个会提多个问题的会话 ⇒ 按 Esc 卡片消失、run 停止等待；队列里塞 20 条以上 ⇒ 列表能滚到头。

## Test plan

见 Step 6。结构参照：`frontend/src/components/chat/AskTabs.test.ts` 现有的 `mount(AskTabs, { props: … })` 写法；
`PendingInputQueuePopover.test.ts` 现有的 `preview` prop 构造。

## Done criteria

- [ ] `cd frontend && pnpm test` exit 0；`pnpm exec vue-tsc --noEmit` exit 0
- [ ] `grep -rn "queue-item-edit\|queueEditItem\|xp-head-progress" frontend/src` → 无输出
- [ ] `grep -n "cancelAsk" frontend/src/composables/usePendingActions.ts frontend/src/components/chat/ApprovalCard.vue` → 两处命中
- [ ] `grep -n "queue-body" frontend/src/components/PendingInputQueuePopover.vue` → 命中毒性与模板各一处
- [ ] `grep -n "onKeydown\|@keydown" frontend/src/components/chat/AskTabs.vue` → 命中
- [ ] `FOREBRAIN_E2E_SPEC="e2e/message-cards.spec.ts e2e/approval-families.spec.ts" scripts/acceptance/web_e2e.sh` → PASS
- [ ] `git status --porcelain` 只含 Scope 列出的文件
- [ ] `plans/README.md` 本计划状态行已更新

## STOP conditions

- `pkg/turn/approval.go` 的 `decideAsk` 不再接受 `cancel`，或网关 `/approve` 不再把 `decision` 透传——停下来报告
  （那意味着"取消提问"的通路本身已经没了）。
- 引擎已支持按行取回任意一条（`pkg/run` 或 `pkg/turn` 出现新接口）——那么 Step 4 的减法就不再是最小修法，
  停下来报告并改方案。
- `AskTabs.vue` 实际的 tab 切换不是 `current` ref（重构过）——按现码适配，但不要改变"←/→ 切题"的语义。
- 某个断言必须放宽才过（例如把"Esc 触发 cancel"改成"Esc 不报错"）——停下来报告。
- 任一 Verify 连试两次仍失败。

## Maintenance notes

- **不要再印做不到的键**：`chat.askFootKeysOne/Many` 现在与 `AskTabs.vue` 的 `onKeydown` 一一对应；
  改任一处必须同步另一处，并让 Step 6 的用例跟着变。
- `cancelAsk` 走的是 `/approve` 带 `decision:'cancel'`——这与终端 Esc 返回 `ToolApprovalDecision{Cancelled:true}` 是同一件事；
  以后若有 `/cancel` 专线，两处一起改。
- 队列卡的滚动容器放在 `.queue-body` 而不是库里：`ai-elements-vue` 的 queue 族是共享件，终端侧不用它，
  因此只在本组件补偿；若将来库组件自己加了滚动，删掉 `.queue-body` 的 overflow 以免双重滚动。
- **后续建议（本计划不做）**：行内"取回任意一条"要引擎支持（现在只有 `edit_last`）。若 owner 要这个能力，
  单独立项，按"引擎先给接口、UI 后跟上"的顺序。
- 评审要点：Esc 不会连带取消 run（只取消这一张提问）；方向键在「其他」输入框里不被吞；review 屏不受方向键影响；
  队列头部的「取回」按钮语义未变。
